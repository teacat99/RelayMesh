//go:build !windows

package cli

import "os"

// isTerminalFile 判断 f 是否连着终端（字符设备）。
func isTerminalFile(f *os.File) bool {
	st, err := f.Stat()
	return err == nil && st.Mode()&os.ModeCharDevice != 0
}
