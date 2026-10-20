package agent

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/teacat99/RelayMesh/v2/internal/termproto"
)

// 「自动升级」在本机数据目录里留下的同意文件（方案见文档库 design/agent-auto-upgrade.md）：
//
//	<数据目录>/auto-upgrade.json      主机所有者的同意：文件存在且合规 = 已开启（relaymesh-agent auto-upgrade enable / disable）
//
// 与 `web-upgrade` 开关一样，同意只放在主机本地：Hub 既看不到这个文件的内容，也无法创建它；
// Agent 每次都现读（不缓存），所有者随时可以收回，下一次检查与握手就会反映出来。
const autoUpgradeFile = "auto-upgrade.json"

// maxAutoUpgradeFileBytes 限制同意文件的大小：它只有几十个字节，更大的一律当作损坏。
const maxAutoUpgradeFileBytes = 4 << 10

// AutoUpgradeConfig 是主机所有者对「自动升级」的同意。
type AutoUpgradeConfig struct {
	// Mode 是 termproto.AutoUpgradeNotify 或 termproto.AutoUpgradeSilent。
	Mode string `json:"mode"`
	// At 是写入时刻（Unix 秒），只用于 `auto-upgrade status` 展示。
	At int64 `json:"at,omitempty"`
	// Window 是静默升级允许开始的时间段（HH:MM-HH:MM，主机本地时间）；空表示不限。只对静默模式有意义。
	Window string `json:"window,omitempty"`
	// TrustKeys 是主机所有者钉住的发布公钥（rmpub1:…）：与编进程序的公钥一起构成静默升级的信任名单。
	// 只对静默模式有意义；Hub 既不能读到它，也无法添加。
	TrustKeys []string `json:"trust_keys,omitempty"`
}

// AutoUpgradePath 是同意文件的路径。
func AutoUpgradePath(dataDir string) string { return filepath.Join(dataDir, autoUpgradeFile) }

// ReadAutoUpgrade 读取同意。文件不存在、不是普通文件、过大、不是合规的 JSON、模式不认识，一律视为「没有同意」（false）：
// 自动升级是一项会改动程序的能力，读不清楚的时候必须落在关闭的一侧。
func ReadAutoUpgrade(dataDir string) (AutoUpgradeConfig, bool) {
	if dataDir == "" {
		return AutoUpgradeConfig{}, false
	}
	path := AutoUpgradePath(dataDir)
	st, err := os.Lstat(path)
	if err != nil || !st.Mode().IsRegular() || st.Size() > maxAutoUpgradeFileBytes {
		return AutoUpgradeConfig{}, false
	}
	f, err := os.Open(path)
	if err != nil {
		return AutoUpgradeConfig{}, false
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxAutoUpgradeFileBytes+1))
	if err != nil || len(data) > maxAutoUpgradeFileBytes {
		return AutoUpgradeConfig{}, false
	}
	var cfg AutoUpgradeConfig
	if json.Unmarshal(data, &cfg) != nil || !termproto.ValidAutoUpgradeMode(cfg.Mode) {
		return AutoUpgradeConfig{}, false
	}
	return cfg, true
}

// AutoUpgradeMode 返回握手里声明的模式：已开启时是 notify / silent，否则是 off。
func AutoUpgradeMode(dataDir string) string {
	if cfg, ok := ReadAutoUpgrade(dataDir); ok {
		return cfg.Mode
	}
	return termproto.AutoUpgradeOff
}

// WriteAutoUpgrade 写入同意（先写临时文件再改名，读的一方不会看到写了一半的内容）。
func WriteAutoUpgrade(dataDir string, cfg AutoUpgradeConfig) error {
	if dataDir == "" {
		return errors.New("数据目录为空")
	}
	if !termproto.ValidAutoUpgradeMode(cfg.Mode) {
		return errors.New("自动升级的模式必须是 notify 或 silent")
	}
	if cfg.Mode != termproto.AutoUpgradeSilent && (cfg.Window != "" || len(cfg.TrustKeys) > 0) {
		return errors.New("时间段与发布公钥只用于静默模式")
	}
	if problem := cfg.Settings().Problem; problem != "" {
		return errors.New(problem)
	}
	if cfg.At == 0 {
		cfg.At = time.Now().Unix()
	}
	data, err := json.Marshal(cfg)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return err
	}
	return writeFileAtomic(AutoUpgradePath(dataDir), append(data, '\n'), 0o600)
}

// RemoveAutoUpgrade 收回同意。文件本来就不存在不算错。
func RemoveAutoUpgrade(dataDir string) error {
	if dataDir == "" {
		return errors.New("数据目录为空")
	}
	if err := os.Remove(AutoUpgradePath(dataDir)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}
