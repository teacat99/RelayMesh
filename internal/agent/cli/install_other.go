//go:build !windows

package cli

import "context"

// 以下三个函数只会在 Windows 上被调用；其他系统提供占位实现以便统一编译。

func installWindows(_ context.Context, env Env, _ common, _ resolved) int {
	env.errorf("此命令只适用于 Windows\n")
	return 1
}

func uninstallWindows(_ context.Context, env Env, _ common, _ bool, _ resolved) int {
	env.errorf("此命令只适用于 Windows\n")
	return 1
}

func upgradeWindows(_ context.Context, env Env, _ bool) int {
	env.errorf("此命令只适用于 Windows\n")
	return 1
}

func statusWindows(_ context.Context, env Env) int {
	env.errorf("此命令只适用于 Windows\n")
	return 1
}

// kickHolderTask 只有 Windows 需要（守护进程由计划任务拉起）；其他系统的服务管理器自己会立刻拉起它。
func kickHolderTask(Env) (string, error) {
	return "", errNoHolderTask
}
