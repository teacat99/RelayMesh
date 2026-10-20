//go:build windows

package agent

import (
	"golang.org/x/sys/windows"

	"github.com/teacat99/RelayMesh/v2/internal/termproto"
)

// IsElevated 报告当前进程是否以提升（管理员）令牌运行。
func IsElevated() bool {
	return windows.GetCurrentProcessToken().IsElevated()
}

// PrivLevel 返回自报的特权等级：提升的管理员令牌为 admin，否则 user（Windows 没有 root 的概念）。
func PrivLevel() string {
	if IsElevated() {
		return termproto.PrivAdmin
	}
	return termproto.PrivUser
}

func darwinVersion() string { return "macOS" }
