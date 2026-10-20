//go:build windows

package hookcli

import "syscall"

// detachedProcess 是 Windows 的 DETACHED_PROCESS 创建标志：子进程不继承父进程的控制台。
const detachedProcess = 0x00000008

// detachAttr 让子进程脱离父进程的控制台与进程组，不弹窗，父进程退出后照常运行。
func detachAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{HideWindow: true, CreationFlags: detachedProcess | syscall.CREATE_NEW_PROCESS_GROUP}
}
