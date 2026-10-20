//go:build linux

package ptyhost

import (
	pty "github.com/aymanbagabas/go-pty"
	"golang.org/x/sys/unix"
)

// enableUTF8Input 给伪终端打开 IUTF8（尽力而为，失败不影响会话）。
//
// 浏览器 / xterm.js 发来的永远是 UTF-8。内核终端行规程默认按「一个字节一个字符」处理规范模式下的退格：
// 在 cat、read 这类不带 readline 的程序里，退格只会删掉中文字符的最后一个字节，留下残缺的乱码。
// 打开 IUTF8 后，行规程按 UTF-8 字符为单位擦除（tmux、sshd 在客户端是 UTF-8 时也会这么做）。
func enableUTF8Input(p pty.Pty) {
	up, ok := p.(pty.UnixPty)
	if !ok {
		return
	}
	fd := int(up.Slave().Fd())
	t, err := unix.IoctlGetTermios(fd, unix.TCGETS)
	if err != nil {
		return
	}
	t.Iflag |= unix.IUTF8
	_ = unix.IoctlSetTermios(fd, unix.TCSETS, t)
}
