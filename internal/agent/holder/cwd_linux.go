//go:build linux

package holder

import (
	"fmt"
	"os"
)

// processCwd 尽力读取进程当前的工作目录（会话 shell 的 cd 会改变它）。读不到就返回空串。
func processCwd(pid int) string {
	if pid <= 0 {
		return ""
	}
	p, err := os.Readlink(fmt.Sprintf("/proc/%d/cwd", pid))
	if err != nil {
		return ""
	}
	return p
}
