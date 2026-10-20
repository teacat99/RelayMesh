//go:build !linux

package cli

import "github.com/teacat99/RelayMesh/v2/internal/agent"

// selfUpgrader 在 Windows、macOS 等系统上总是返回 nil：暂不支持从 Web 一键升级（Agent 不声明升级能力，
// 界面上只提供「升级窗口 + 在主机上执行命令」这条手动路径）。
func selfUpgrader(Env, common, resolved) *agent.SelfUpgrade { return nil }
