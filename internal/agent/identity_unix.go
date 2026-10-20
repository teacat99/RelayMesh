//go:build !windows

package agent

import (
	"errors"
	"fmt"
	"os"
	"syscall"
)

// checkKeyFileSecurity 在读取私钥之前核查文件的归属与权限。
//
//   - 属主必须是当前有效用户：若有人在共享目录里预先放了一个自己持有私钥的身份文件，
//     Agent 会沿用它，人类在 Web 上启用该机器码后，预先放置者就能冒充这台主机。
//   - 组 / 其他用户不得有任何权限：私钥可读意味着身份可被冒用。能自己修正的（属主是自己）直接收紧并提示。
//
// 文件不存在时不报错（交给调用方决定是否创建）。
func checkKeyFileSecurity(path string) error {
	st, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !st.Mode().IsRegular() {
		return fmt.Errorf("身份文件 %s 不是普通文件（拒绝跟随符号链接或特殊文件）", path)
	}
	if sys, ok := st.Sys().(*syscall.Stat_t); ok && int(sys.Uid) != os.Geteuid() {
		return fmt.Errorf("身份文件 %s 的属主不是当前用户（uid %d），拒绝使用", path, sys.Uid)
	}
	if st.Mode().Perm()&0o077 != 0 {
		if err := os.Chmod(path, 0o600); err != nil {
			return fmt.Errorf("身份文件 %s 权限过宽（%o）且无法收紧: %w", path, st.Mode().Perm(), err)
		}
	}
	return nil
}

// hardenDir 在 Unix 上由 MkdirAll 的 0700 权限位保证，无需额外处理。
func hardenDir(string) error { return nil }

// restrictFile 收紧刚创建的临时文件的权限；必须在写入内容之前调用。
func restrictFile(f *os.File, _ string, perm os.FileMode) error { return f.Chmod(perm) }
