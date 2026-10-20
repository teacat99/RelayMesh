package agent

import (
	"os"
	"runtime"
	"strings"

	"github.com/teacat99/RelayMesh/v2/internal/termproto"
)

// HostMeta 收集握手时自报的主机信息。它们全部是「不可信自报字段」，服务端会净化并只按纯文本展示，
// 仅用于让管理员在启用时辨认主机——真正的身份是公钥。
func HostMeta(version, kind string) termproto.Meta {
	host, _ := os.Hostname()
	return termproto.Meta{
		Host: host,
		OS:   osDescription(),
		Arch: runtime.GOARCH,
		Plat: runtime.GOOS + "-" + runtime.GOARCH,
		Ver:  version,
		Priv: PrivLevel(),
		Kind: kind,
		// 文件能力（stat / hash / 拉取 / push_url）永远具备；是否允许使用由服务端闸门与本机一票否决决定。
		Files: true,
		// 认识「整个文件系统」哨兵（termproto.FileRootAll）：令牌被明确授予该开关时才会用到。
		FilesAll: true,
	}
}

// osDescription 返回人类可读的系统描述（限长由服务端处理）。
func osDescription() string {
	switch runtime.GOOS {
	case "linux":
		if raw, err := os.ReadFile("/etc/os-release"); err == nil {
			if v := osReleaseValue(string(raw), "PRETTY_NAME"); v != "" {
				return v
			}
		}
		return "Linux"
	case "darwin":
		return darwinVersion()
	default:
		return runtime.GOOS
	}
}

func osReleaseValue(content, key string) string {
	for _, line := range strings.Split(content, "\n") {
		k, v, ok := strings.Cut(strings.TrimSpace(line), "=")
		if ok && k == key {
			return strings.Trim(strings.TrimSpace(v), `"'`)
		}
	}
	return ""
}
