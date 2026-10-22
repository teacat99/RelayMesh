//go:build windows

package cli

import (
	"os"

	"golang.org/x/sys/windows"
)

// isTerminalFile 判断 f 是否是真正的控制台。
// 不能只看「字符设备」：Go 会把 NUL 也报成字符设备，而把 stdin 重定向到 NUL 的脚本或 CI 里没有人去点 UAC 或回答提示。
func isTerminalFile(f *os.File) bool {
	var mode uint32
	return windows.GetConsoleMode(windows.Handle(f.Fd()), &mode) == nil
}
