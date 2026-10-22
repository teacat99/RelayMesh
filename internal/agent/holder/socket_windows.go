//go:build windows

package holder

import (
	"errors"
	"net"
	"os"
	"syscall"
)

// checkSocketDir 在 Windows 上核对目录存在且是真实目录；访问控制由目录的 ACL（仅当前用户 / SYSTEM / 管理员）保证，
// 由调用方传入的 harden 钩子负责设置。
func checkSocketDir(dir string) error {
	st, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	if !st.IsDir() {
		return errors.New("守护进程目录不是真实目录")
	}
	return nil
}

// verifyPeer 在 Windows 上没有对端凭据接口，靠目录 ACL 兜底。
func verifyPeer(net.Conn) error { return nil }

func isConnRefused(err error) bool {
	// WSAECONNREFUSED = 10061
	var errno syscall.Errno
	return errors.As(err, &errno) && errno == 10061
}
