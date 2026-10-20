//go:build !windows && !linux && !darwin

package holder

import "net"

// peerUID 在没有对端凭据接口的平台上不可用，目录权限（0700）是唯一的防线。
func peerUID(net.Conn) (int, bool) { return 0, false }
