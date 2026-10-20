//go:build windows

package cli

func init() { logFilePreamble = []byte{0xEF, 0xBB, 0xBF} }
