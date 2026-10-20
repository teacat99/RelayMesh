//go:build darwin

package holder

import (
	"strings"

	"golang.org/x/sys/unix"
)

// bootIdentity 返回本次开机的标识：macOS 为每次开机生成一个 bootsessionuuid；
// 取不到时退回启动时间（kern.boottime），由调用方按「允许几秒误差」比较。
func bootIdentity() (id string, bootTime int64) {
	if s, err := unix.Sysctl("kern.bootsessionuuid"); err == nil {
		id = strings.TrimSpace(s)
	}
	if tv, err := unix.SysctlTimeval("kern.boottime"); err == nil {
		bootTime = int64(tv.Sec)
	}
	return id, bootTime
}
