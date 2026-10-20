package agent

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"

	"github.com/teacat99/RelayMesh/v2/internal/termproto"
)

// 「Web 一键升级」在本机数据目录里留下的几个小文件。Agent 与升级进程（relaymesh-agent upgrade --auto-rollback）
// 是两个不同的进程，靠这些文件交换信息，所以路径与格式放在一处：
//
//	<数据目录>/web-upgrade              开关：文件存在 = 主机所有者允许从 Web 升级（relaymesh-agent web-upgrade on/off）
//	<数据目录>/upgrade/relaymesh-agent.new   Agent 下载并校验过的新版本，等待升级进程取用
//	<数据目录>/upgrade/online.json      「上线标记」：Agent 每次与 Hub 握手成功就写一次（含自己的版本号）；升级进程据此判断新版本是否真的连上了
//	<数据目录>/upgrade/last.json        上一次升级的结果（UpgradeNote）：升级进程写，Agent 在握手时带给 Hub
const (
	webUpgradeFlagFile = "web-upgrade"
	upgradeDirName     = "upgrade"
	stagedBinaryName   = "relaymesh-agent.new"
	onlineMarkerFile   = "online.json"
	upgradeNoteFile    = "last.json"
)

// WebUpgradeFlagPath 是「允许从 Web 升级」开关文件的路径。
func WebUpgradeFlagPath(dataDir string) string { return filepath.Join(dataDir, webUpgradeFlagFile) }

// UpgradeDir 是升级相关文件（下载的新版本、上线标记、结果）所在的目录。
func UpgradeDir(dataDir string) string { return filepath.Join(dataDir, upgradeDirName) }

// StagedBinaryPath 是下载并校验过的新版本的存放路径。
func StagedBinaryPath(dataDir string) string {
	return filepath.Join(UpgradeDir(dataDir), stagedBinaryName)
}

// WebUpgradeAllowed 报告主机所有者是否允许从 Web 升级：开关文件存在且是普通文件。
// 每次都现读，不缓存——所有者随时可以用 `web-upgrade off` 收回。
func WebUpgradeAllowed(dataDir string) bool {
	if dataDir == "" {
		return false
	}
	st, err := os.Lstat(WebUpgradeFlagPath(dataDir))
	return err == nil && st.Mode().IsRegular()
}

// SetWebUpgradeAllowed 打开或关闭开关。关闭时文件本来就不存在不算错。
func SetWebUpgradeAllowed(dataDir string, on bool) error {
	if dataDir == "" {
		return errors.New("数据目录为空")
	}
	path := WebUpgradeFlagPath(dataDir)
	if !on {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return err
	}
	return writeFileAtomic(path, []byte("allowed\n"), 0o644)
}

// OnlineMarker 是新版本 Agent 握手成功后留下的标记。
type OnlineMarker struct {
	Version string `json:"version"`
	PID     int    `json:"pid"`
	// At 是写入时刻（Unix 秒）。
	At int64 `json:"at"`
}

// WriteOnlineMarker 记下「本 Agent（version）已经与 Hub 握手成功」。
func WriteOnlineMarker(dataDir, version string, now time.Time) error {
	if dataDir == "" {
		return errors.New("数据目录为空")
	}
	if err := os.MkdirAll(UpgradeDir(dataDir), 0o700); err != nil {
		return err
	}
	data, err := json.Marshal(OnlineMarker{Version: version, PID: os.Getpid(), At: now.Unix()})
	if err != nil {
		return err
	}
	return writeFileAtomic(filepath.Join(UpgradeDir(dataDir), onlineMarkerFile), data, 0o600)
}

// ReadOnlineMarker 读取上线标记；不存在或损坏时返回 false。
func ReadOnlineMarker(dataDir string) (OnlineMarker, bool) {
	data, err := os.ReadFile(filepath.Join(UpgradeDir(dataDir), onlineMarkerFile))
	if err != nil {
		return OnlineMarker{}, false
	}
	var m OnlineMarker
	if json.Unmarshal(data, &m) != nil || m.Version == "" {
		return OnlineMarker{}, false
	}
	return m, true
}

// RemoveOnlineMarker 删除上线标记（升级进程在重启服务之前清掉旧标记，避免把旧版本留下的标记当成新版本上线）。
func RemoveOnlineMarker(dataDir string) {
	_ = os.Remove(filepath.Join(UpgradeDir(dataDir), onlineMarkerFile))
}

// WriteUpgradeNote 保存上一次升级的结果。
func WriteUpgradeNote(dataDir string, n termproto.UpgradeNote) error {
	if dataDir == "" {
		return errors.New("数据目录为空")
	}
	if err := os.MkdirAll(UpgradeDir(dataDir), 0o700); err != nil {
		return err
	}
	data, err := json.Marshal(n)
	if err != nil {
		return err
	}
	return writeFileAtomic(filepath.Join(UpgradeDir(dataDir), upgradeNoteFile), data, 0o600)
}

// ReadUpgradeNote 读取上一次升级的结果（已净化）；没有或损坏时返回 nil。
func ReadUpgradeNote(dataDir string) *termproto.UpgradeNote {
	if dataDir == "" {
		return nil
	}
	data, err := os.ReadFile(filepath.Join(UpgradeDir(dataDir), upgradeNoteFile))
	if err != nil {
		return nil
	}
	var n termproto.UpgradeNote
	if json.Unmarshal(data, &n) != nil {
		return nil
	}
	return n.Sanitized()
}
