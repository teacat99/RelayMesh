//go:build darwin

package holder

import (
	"net"

	"golang.org/x/sys/unix"
)

// peerUID 通过 LOCAL_PEERCRED 取得套接字对端进程的用户 ID。
func peerUID(nc net.Conn) (int, bool) {
	uc, ok := nc.(*net.UnixConn)
	if !ok {
		return 0, false
	}
	raw, err := uc.SyscallConn()
	if err != nil {
		return 0, false
	}
	var cred *unix.Xucred
	var gerr error
	if err := raw.Control(func(fd uintptr) {
		cred, gerr = unix.GetsockoptXucred(int(fd), unix.SOL_LOCAL, unix.LOCAL_PEERCRED)
	}); err != nil || gerr != nil || cred == nil {
		return 0, false
	}
	return int(cred.Uid), true
}
