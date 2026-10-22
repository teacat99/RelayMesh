//go:build !linux && !darwin

package ptyhost

import pty "github.com/aymanbagabas/go-pty"

// enableUTF8Input 在没有 IUTF8 的平台（Windows ConPTY、各类 BSD）上什么也不做。
func enableUTF8Input(pty.Pty) {}
