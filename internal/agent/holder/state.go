package holder

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/teacat99/RelayMesh/v2/internal/termproto"
)

const (
	// stateFileName 是守护进程的本地状态：会话索引、启动标识与最近一次策略。
	// 它的作用不是「恢复会话」（进程已经没了，恢复不了），而是在守护进程重启后
	// 能告诉用户「这些会话为什么没了」，并让策略在 Hub 离线期间继续生效。
	stateFileName = "state.json"
	stateVersion  = 1

	// maxLostEntries / lostTTL 限制「已丢失会话」记录的体积与保留期，避免状态文件无限增长。
	maxLostEntries = 64
	lostTTL        = 7 * 24 * time.Hour

	// stateSaveDebounce 合并短时间内连续的会话变化；stateRefreshInterval 周期性刷新（工作目录会变化）。
	stateSaveDebounce    = time.Second
	stateRefreshInterval = 30 * time.Second
)

type persistedSession struct {
	SID     string `json:"sid"`
	Name    string `json:"name,omitempty"`
	Shell   string `json:"shell,omitempty"`
	PID     int    `json:"pid,omitempty"`
	Created int64  `json:"created,omitempty"`
	Cwd     string `json:"cwd,omitempty"`
}

type persistedState struct {
	Version  int                `json:"version"`
	BootID   string             `json:"boot_id,omitempty"`
	BootTime int64              `json:"boot_time,omitempty"`
	PID      int                `json:"pid"`
	Started  int64              `json:"started"`
	Saved    int64              `json:"saved"`
	Clean    bool               `json:"clean"` // 上一次是否是正常关停
	Sessions []persistedSession `json:"sessions"`
	Lost     []Lost             `json:"lost,omitempty"`
	Policy   *termproto.Policy  `json:"policy,omitempty"`
}

// stateStore 负责把状态原子地写到磁盘（临时文件 + 重命名），权限 0600。
type stateStore struct {
	dir string
}

func newStateStore(dir string) *stateStore { return &stateStore{dir: dir} }

func (s *stateStore) path() string { return filepath.Join(s.dir, stateFileName) }

// load 读取上一次的状态；文件不存在返回 (nil, nil)。损坏的文件按「没有」处理并保留副本，
// 状态只是辅助信息，绝不能因为它而让守护进程起不来。
func (s *stateStore) load() (*persistedState, error) {
	data, err := os.ReadFile(s.path())
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var st persistedState
	if err := json.Unmarshal(data, &st); err != nil || st.Version != stateVersion {
		_ = os.Rename(s.path(), s.path()+".bad")
		return nil, nil
	}
	return &st, nil
}

func (s *stateStore) save(st *persistedState) error {
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return err
	}
	st.Version = stateVersion
	st.Saved = time.Now().Unix()
	data, err := json.Marshal(st)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(s.dir, stateFileName+".*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name) // 成功重命名之后这是个无害的失败
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(name, s.path()); err != nil {
		return fmt.Errorf("save holder state: %w", err)
	}
	return nil
}

// sameBoot 判断两次记录是否属于同一次开机。有启动标识就比标识；没有就比启动时间（允许几秒误差）；
// 都不可知时假定相同——宁可少报「主机重启」，也不要凭空宣称它发生过。
func sameBoot(oldID string, oldTime int64, curID string, curTime int64) bool {
	if oldID != "" && curID != "" {
		return oldID == curID
	}
	if oldTime == 0 || curTime == 0 {
		return true
	}
	d := oldTime - curTime
	if d < 0 {
		d = -d
	}
	return d <= 5
}

// lostReason 判断上一次留下的会话 / 运行为什么不存在了：主机重启 > 守护进程被有意停止 > 守护进程意外退出。
func lostReason(old *persistedState, curBootID string, curBootTime int64) string {
	reason := LostHolderExit
	if old.Clean {
		reason = LostHolderStopped
	}
	if !sameBoot(old.BootID, old.BootTime, curBootID, curBootTime) {
		reason = LostHostReboot
	}
	return reason
}

// computeLost 根据上一次的状态算出「已经不存在的会话」及原因，并并入尚未过期的旧记录。
func computeLost(old *persistedState, curBootID string, curBootTime int64, now time.Time) []Lost {
	if old == nil {
		return nil
	}
	var out []Lost
	reason := lostReason(old, curBootID, curBootTime)
	for _, ps := range old.Sessions {
		out = append(out, Lost{
			SID:     ps.SID,
			Name:    ps.Name,
			Shell:   ps.Shell,
			Cwd:     ps.Cwd,
			Created: ps.Created,
			Reason:  reason,
			At:      now.Unix(),
		})
	}
	// 并入旧的未过期记录（同一 SID 以新的为准）。
	seen := make(map[string]struct{}, len(out))
	for _, l := range out {
		seen[l.SID] = struct{}{}
	}
	cutoff := now.Add(-lostTTL).Unix()
	for _, l := range old.Lost {
		if _, dup := seen[l.SID]; dup || l.At < cutoff {
			continue
		}
		out = append(out, l)
	}
	if len(out) > maxLostEntries {
		out = out[:maxLostEntries]
	}
	return out
}

// PurgeState 删除守护进程在数据目录下留下的状态文件、残留的套接字与（清空后的）目录，返回被删除的路径。
// 不碰目录里的其他文件。
func PurgeState(dataDir string) []string {
	dir := StateDir(dataDir)
	var removed []string
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	for _, e := range entries {
		name := e.Name()
		if name == stateFileName || name == stateFileName+".bad" || name == socketFileName || strings.HasPrefix(name, stateFileName+".") {
			p := filepath.Join(dir, name)
			if os.Remove(p) == nil {
				removed = append(removed, p)
			}
		}
	}
	if os.Remove(dir) == nil {
		removed = append(removed, dir)
	}
	return removed
}
