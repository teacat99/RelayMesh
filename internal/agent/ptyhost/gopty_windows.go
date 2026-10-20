//go:build windows

package ptyhost

import (
	"os"

	pty "github.com/aymanbagabas/go-pty"
)

// afterStart 在 Windows 上无需额外处理（ConPTY 由 go-pty 完整管理）。
func afterStart(pty.Pty) {}

// killTree 终止子进程。ConPTY 关闭时会一并结束挂在该控制台上的进程。
func killTree(cmd *pty.Cmd, done <-chan struct{}) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	select {
	case <-done:
		return
	default:
	}
	_ = cmd.Process.Kill()
}

func exitStatus(ps *os.ProcessState) ExitStatus {
	if ps == nil {
		return ExitStatus{Code: -1}
	}
	return ExitStatus{Code: ps.ExitCode()}
}

// openIO 在 Windows 上返回 nil：直接使用 ConPTY 自身的读写；读期限不可用，调用方改用 Close 来解除阻塞的 Read。
func openIO(pty.Pty) (ioFile, error) { return nil, nil }

func closePty(p pty.Pty) error { return p.Close() }
