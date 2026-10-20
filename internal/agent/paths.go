package agent

import (
	"os"
	"path/filepath"
	"runtime"
)

// DefaultDataDir 返回身份文件等持久数据的默认目录（按是否以管理员权限运行区分）。
//
//	Linux  root : /var/lib/relaymesh-agent          普通用户: $XDG_STATE_HOME 或 ~/.local/state 下的 relaymesh-agent
//	macOS  root : /Library/Application Support/RelayMesh Agent   普通用户: ~/Library/Application Support/RelayMesh Agent
//	Windows 管理员: %ProgramData%\RelayMesh\Agent    普通用户: %LocalAppData%\RelayMesh\Agent
func DefaultDataDir() string {
	elevated := IsElevated()
	switch runtime.GOOS {
	case "windows":
		if elevated {
			return filepath.Join(envOr("ProgramData", `C:\ProgramData`), "RelayMesh", "Agent")
		}
		return filepath.Join(envOr("LocalAppData", filepath.Join(homeDir(), "AppData", "Local")), "RelayMesh", "Agent")
	case "darwin":
		if elevated {
			return "/Library/Application Support/RelayMesh Agent"
		}
		return filepath.Join(homeDir(), "Library", "Application Support", "RelayMesh Agent")
	default:
		if elevated {
			return "/var/lib/relaymesh-agent"
		}
		return filepath.Join(envOr("XDG_STATE_HOME", filepath.Join(homeDir(), ".local", "state")), "relaymesh-agent")
	}
}

// DefaultConfigDir 返回服务配置与已信任服务器指纹（known_servers）的默认目录。
func DefaultConfigDir() string {
	elevated := IsElevated()
	switch runtime.GOOS {
	case "windows", "darwin":
		return DefaultDataDir()
	default:
		if elevated {
			return "/etc/relaymesh-agent"
		}
		return filepath.Join(envOr("XDG_CONFIG_HOME", filepath.Join(homeDir(), ".config")), "relaymesh-agent")
	}
}

// HardenDir 把目录收紧为仅本人（与管理员 / SYSTEM）可访问：Unix 由创建时的 0700 保证，Windows 设置私有 ACL。
// 会话守护进程的目录（状态文件与本地套接字）靠它防止其他用户接入。
func HardenDir(dir string) error { return hardenDir(dir) }

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func homeDir() string {
	if h, err := os.UserHomeDir(); err == nil && h != "" {
		return h
	}
	if runtime.GOOS == "windows" {
		return `C:\Users\Default`
	}
	return "/tmp"
}
