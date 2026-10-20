//go:build darwin

package ptyhost

import (
	pty "github.com/aymanbagabas/go-pty"
	"golang.org/x/sys/unix"
)

// enableUTF8Input 给伪终端打开 IUTF8（尽力而为，失败不影响会话），含义见 iutf8_linux.go。
func enableUTF8Input(p pty.Pty) {
	up, ok := p.(pty.UnixPty)
	if !ok {
		return
	}
	fd := int(up.Slave().Fd())
	t, err := unix.IoctlGetTermios(fd, unix.TIOCGETA)
	if err != nil {
		return
	}
	t.Iflag |= unix.IUTF8
	_ = unix.IoctlSetTermios(fd, unix.TIOCSETA, t)
}
