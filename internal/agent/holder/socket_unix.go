//go:build !windows

package holder

import (
	"errors"
	"fmt"
	"net"
	"os"
	"syscall"
)

// checkSocketDir 核对套接字目录：必须是真实目录（不跟随符号链接）、属主是当前有效用户、
// 组 / 其他用户没有任何权限。能自己修正的（属主是自己）直接收紧。
func checkSocketDir(dir string) error {
	st, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	if !st.IsDir() {
		return fmt.Errorf("守护进程目录 %s 不是真实目录（拒绝跟随符号链接）", dir)
	}
	if sys, ok := st.Sys().(*syscall.Stat_t); ok && int(sys.Uid) != os.Geteuid() {
		return fmt.Errorf("守护进程目录 %s 的属主不是当前用户（uid %d），拒绝使用", dir, sys.Uid)
	}
	if st.Mode().Perm()&0o077 != 0 {
		if err := os.Chmod(dir, 0o700); err != nil {
			return fmt.Errorf("守护进程目录 %s 权限过宽（%o）且无法收紧: %w", dir, st.Mode().Perm(), err)
		}
	}
	return nil
}

// verifyPeer 核对连接对端的用户：必须是当前有效用户或 root。
func verifyPeer(nc net.Conn) error {
	uid, ok := peerUID(nc)
	if !ok {
		return nil // 平台无法获取对端凭据：靠目录权限兜底
	}
	if uid != os.Geteuid() && uid != 0 {
		return fmt.Errorf("对端用户（uid %d）不是当前用户，拒绝连接", uid)
	}
	return nil
}

func isConnRefused(err error) bool { return errors.Is(err, syscall.ECONNREFUSED) }
