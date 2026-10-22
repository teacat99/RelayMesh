//go:build windows

package holder

import (
	"syscall"
	"time"
)

var procGetTickCount64 = syscall.NewLazyDLL("kernel32.dll").NewProc("GetTickCount64")

// bootIdentity 返回本次开机的标识：Windows 没有现成的开机 ID，用「当前时间 − 开机至今的毫秒数」得到启动时间，
// 由调用方按「允许几秒误差」比较。
func bootIdentity() (id string, bootTime int64) {
	r, _, _ := procGetTickCount64.Call()
	if r == 0 {
		return "", 0
	}
	up := time.Duration(r) * time.Millisecond
	return "", time.Now().Add(-up).Unix()
}
