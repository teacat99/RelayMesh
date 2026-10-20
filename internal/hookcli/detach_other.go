//go:build !unix && !windows

package hookcli

import "syscall"

// detachAttr 在既不是 Unix 也不是 Windows 的平台上没有可用的分离方式；子进程照常启动，只是不脱离父进程。
func detachAttr() *syscall.SysProcAttr { return nil }
