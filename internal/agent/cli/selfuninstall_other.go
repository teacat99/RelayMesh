//go:build !linux && !windows

package cli

// selfUninstaller 在 macOS 等其他系统上总是返回 nil：暂不支持远程卸载（Agent 不声明卸载能力，
// 界面上也不会提供「同时卸载设备端」，只给出需要在主机上手动执行的命令）。
func selfUninstaller(Env, common, resolved) func(purge bool) error { return nil }
