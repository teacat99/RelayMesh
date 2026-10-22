//go:build linux

package holder

import (
	"os"
	"strings"
)

// bootIdentity 返回本次开机的标识：Linux 内核每次启动生成一个随机的 boot_id。
func bootIdentity() (id string, bootTime int64) {
	if b, err := os.ReadFile("/proc/sys/kernel/random/boot_id"); err == nil {
		id = strings.TrimSpace(string(b))
	}
	return id, 0
}
