//go:build unix

package hookcli

import "syscall"

// detachAttr 让子进程自成会话：父进程（hook 进程）退出、甚至 Cursor 回收整个进程组时，它都不受牵连。
func detachAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setsid: true}
}
