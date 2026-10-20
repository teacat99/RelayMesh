//go:build linux || darwin

package record

import "golang.org/x/sys/unix"

// freeSpace 返回 path 所在文件系统对非特权用户可用的字节数。
func freeSpace(path string) (uint64, error) {
	var st unix.Statfs_t
	if err := unix.Statfs(path, &st); err != nil {
		return 0, err
	}
	return uint64(st.Bavail) * uint64(st.Bsize), nil
}
