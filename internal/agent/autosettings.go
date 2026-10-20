package agent

import (
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/teacat99/RelayMesh/v2/internal/relsign"
	"github.com/teacat99/RelayMesh/v2/internal/relsign/trust"
	"github.com/teacat99/RelayMesh/v2/internal/termproto"
)

// 静默自动升级在本机的设置与记录（方案见文档库 design/agent-auto-upgrade.md B 层）。
//
// 设置在 <数据目录>/auto-upgrade.json 里（与同意放在一起，由所有者用 auto-upgrade enable 写入）；
// 「尝试记录」在 <数据目录>/upgrade/auto-attempts.json 里，由 Agent 自己写，记的是「同一个版本已经试过几次」。

// maxPinnedKeys 限制主机所有者能钉住的公钥个数：名单只需要容纳「主钥 + 备用钥」这样的少数几把。
const maxPinnedKeys = 8

// AutoSettings 是读出来之后经过校验的静默升级设置。
type AutoSettings struct {
	// Mode 是 notify 或 silent。
	Mode string
	// Window 是允许开始升级的时间段；WindowText 是它的原文（不限为空）。
	Window     AutoWindow
	WindowText string
	// TrustKeys 是主机所有者钉住的公钥（已解析、已去重）。完整的信任名单还要加上编进程序的公钥，见 Keys。
	TrustKeys []ed25519.PublicKey
	// Problem 非空表示设置文件里有读不懂的内容（时间段写法不对、公钥无法解析、公钥太多）。
	// 静默安装遇到它一律不做——读不清楚的时候必须落在不升级的一侧。
	Problem string
}

// Settings 把同意文件的内容整理成可用的设置；写法不对的地方记在 Problem 里，而不是悄悄忽略。
func (c AutoUpgradeConfig) Settings() AutoSettings {
	s := AutoSettings{Mode: c.Mode}
	if w, err := ParseAutoWindow(c.Window); err != nil {
		s.Problem = "设置里的时间段写法不对：" + err.Error()
	} else {
		s.Window, s.WindowText = w, c.Window
	}
	if len(c.TrustKeys) > maxPinnedKeys {
		s.Problem = fmt.Sprintf("设置里钉住的公钥太多（最多 %d 把）", maxPinnedKeys)
		return s
	}
	seen := make(map[string]bool, len(c.TrustKeys))
	for _, text := range c.TrustKeys {
		k, err := relsign.ParsePublicKey(text)
		if err != nil {
			s.Problem = "设置里有一把公钥无法解析：" + err.Error()
			continue
		}
		if id := relsign.KeyID(k); !seen[id] {
			seen[id] = true
			s.TrustKeys = append(s.TrustKeys, k)
		}
	}
	return s
}

// ReadAutoSettings 读取并整理同意文件；没有同意（或读不清）时第二个返回值为 false。
func ReadAutoSettings(dataDir string) (AutoSettings, bool) {
	cfg, ok := ReadAutoUpgrade(dataDir)
	if !ok {
		return AutoSettings{}, false
	}
	return cfg.Settings(), true
}

// Keys 是静默升级的完整信任名单：编进程序的发布公钥，加上主机所有者钉住的公钥，按密钥标识去重。
// 名单为空时静默模式没有任何办法验证新版本，所以既不能开启也不会安装。
func (s AutoSettings) Keys() []ed25519.PublicKey { return s.keysWith(embeddedReleaseKeys()) }

// keysWith 是 Keys 的实际逻辑，编进程序的公钥由调用方给出：测试用它验证「并集 + 去重」，不必去改写包级变量
// （后台协程还在读它时改写会是一个数据竞争）。
func (s AutoSettings) keysWith(embedded []ed25519.PublicKey) []ed25519.PublicKey {
	out := make([]ed25519.PublicKey, 0, len(embedded)+len(s.TrustKeys))
	seen := map[string]bool{}
	for _, group := range [][]ed25519.PublicKey{embedded, s.TrustKeys} {
		for _, k := range group {
			if id := relsign.KeyID(k); !seen[id] {
				seen[id] = true
				out = append(out, k)
			}
		}
	}
	return out
}

// embeddedReleaseKeys 是编进程序的发布公钥（信任根，见 internal/relsign/trust：主钥 + 备用钥）。
// 有它们，主机所有者开启静默模式时不必再用 --trust-key 钉公钥；用 --trust-key 钉的公钥照旧叠加在它们之上。
func embeddedReleaseKeys() []ed25519.PublicKey { return trust.Keys() }

// ---------- 尝试记录 ----------

// maxAutoAttempts 是同一个版本最多尝试几次：连续失败两次就停下，直到出现另一个版本或所有者重新 enable。
const maxAutoAttempts = 2

const (
	autoAttemptsFile     = "auto-attempts.json"
	maxAutoAttemptsBytes = 4 << 10
)

// AutoAttempts 记录「对哪个版本已经开始过几次静默升级」。
type AutoAttempts struct {
	Version string `json:"version"`
	Count   int    `json:"count"`
	// At 是最近一次尝试的 Unix 秒，只用于展示。
	At int64 `json:"at,omitempty"`
	// Unreadable 表示记录文件存在但读不清：按「已经试满」处理（不写进文件）。
	Unreadable bool `json:"-"`
}

// Paused 判断对 version 是否已经停止尝试：同一个版本试满次数，或记录本身读不清。
func (a AutoAttempts) Paused(version string) bool {
	if a.Unreadable {
		return true
	}
	return a.Version == version && a.Count >= maxAutoAttempts
}

// AutoAttemptsPath 是尝试记录的路径（与升级相关的其他文件放在一起）。
func AutoAttemptsPath(dataDir string) string {
	return filepath.Join(UpgradeDir(dataDir), autoAttemptsFile)
}

// ReadAutoAttempts 读取尝试记录。文件不存在是「没有试过」；存在但不是合规的内容则标记 Unreadable，
// 交给调用方按「已经试满」处理——宁可停下，也不要因为记录坏了而无限重试。
func ReadAutoAttempts(dataDir string) AutoAttempts {
	if dataDir == "" {
		return AutoAttempts{Unreadable: true}
	}
	path := AutoAttemptsPath(dataDir)
	st, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return AutoAttempts{}
	}
	if err != nil || !st.Mode().IsRegular() || st.Size() > maxAutoAttemptsBytes {
		return AutoAttempts{Unreadable: true}
	}
	f, err := os.Open(path)
	if err != nil {
		return AutoAttempts{Unreadable: true}
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxAutoAttemptsBytes+1))
	if err != nil || len(data) > maxAutoAttemptsBytes {
		return AutoAttempts{Unreadable: true}
	}
	var a AutoAttempts
	if json.Unmarshal(data, &a) != nil || !termproto.ValidAgentVersion(a.Version) || a.Count < 1 || a.Count > 1000 {
		return AutoAttempts{Unreadable: true}
	}
	return a
}

// RecordAutoAttempt 记一次「对 version 开始静默升级」。同一个版本累加，换了版本从 1 重新数。
// 要在开始下载之前写好：Agent 在升级中途崩溃、或升级后被还原重启，这次尝试也不会丢。
func RecordAutoAttempt(dataDir, version string, now time.Time) error {
	if dataDir == "" {
		return errors.New("数据目录为空")
	}
	if !termproto.ValidAgentVersion(version) {
		return errors.New("版本号写法不合规")
	}
	cur := ReadAutoAttempts(dataDir)
	next := AutoAttempts{Version: version, Count: 1, At: now.Unix()}
	if !cur.Unreadable && cur.Version == version {
		next.Count = cur.Count + 1
	}
	data, err := json.Marshal(next)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(UpgradeDir(dataDir), 0o700); err != nil {
		return err
	}
	return writeFileAtomic(AutoAttemptsPath(dataDir), append(data, '\n'), 0o600)
}

// ClearAutoAttempts 清掉尝试记录（所有者重新 enable、或收回同意时）。文件不存在不算错。
func ClearAutoAttempts(dataDir string) error {
	if dataDir == "" {
		return errors.New("数据目录为空")
	}
	if err := os.Remove(AutoAttemptsPath(dataDir)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}
