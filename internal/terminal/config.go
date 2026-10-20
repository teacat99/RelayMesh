// Package terminal 实现 RelayMesh 的 Web 终端 Hub：
// 接收主机上常驻 Agent 的通用入口连接，管理「发现 → 启用 → 备注」流程，并把浏览器与 Agent 上的持久化 PTY 会话连接起来。
//
// 安全模型的三道闸门（任何一道不满足，终端功能对外完全不可达）：
//  1. 总闸（gate.go）：已设置 Web 密码 ∧ JWT 签名密钥强度合格 ∧ 未被 RELAYMESH_TERMINAL=off 强制关闭；
//  2. 人类身份：终端 REST / WebSocket 只接受登录 JWT，MCP 凭据一律不被接受；
//  3. 解锁令牌：启用主机、开会话、发票据、终止会话等高危操作需要再次输入密码换取短期令牌。
package terminal

import (
	"fmt"
	"strings"
)

// Mode 是 RELAYMESH_TERMINAL 的解析结果。
type Mode string

const (
	// ModeAuto 表示满足安全前置条件时自动启用（默认）。
	ModeAuto Mode = "auto"
	// ModeOff 表示强制关闭。环境变量只能「关」不能「开」：无法绕过总闸的其余条件。
	ModeOff Mode = "off"
)

// ResolveMode 规范化 RELAYMESH_TERMINAL。
//
// 无法识别的取值按 off 处理并返回提示：配置写错时宁可关闭高危功能，也不能静默地放开。
func ResolveMode(raw string) (mode Mode, warning string) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", "auto", "on", "true", "1", "yes", "enabled", "enable":
		return ModeAuto, ""
	case "off", "false", "0", "no", "disabled", "disable":
		return ModeOff, ""
	default:
		return ModeOff, fmt.Sprintf("RELAYMESH_TERMINAL=%q 无法识别（可选 auto / off），终端功能已保持关闭", raw)
	}
}

// ResolveDiscovery 规范化 RELAYMESH_TERMINAL_DISCOVERY：
// window（默认）表示只在管理员手动开启「发现窗口」期间接受未知主机；always 表示常开（不建议暴露在公网）。
// 无法识别的取值回落到更安全的 window 并返回提示。
func ResolveDiscovery(raw string) (always bool, warning string) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", "window", "manual", "auto", "off", "false", "0", "no":
		return false, ""
	case "always", "on", "true", "1", "yes", "open":
		return true, ""
	default:
		return false, fmt.Sprintf("RELAYMESH_TERMINAL_DISCOVERY=%q 无法识别（可选 window / always），已按 window 处理", raw)
	}
}

// ResolveRecord 规范化 RELAYMESH_TERMINAL_RECORD：auto（默认）表示各主机按本机与主机级策略记录终端显示内容；
// off 表示服务端全局关闭记录（下发给每台 Agent，本机无法覆盖）。
// 无法识别的取值按 off 处理并返回提示：记录是否关闭关乎隐私，配置写错时宁可不记，也不能让管理员以为已关而实际在记。
func ResolveRecord(raw string) (off bool, warning string) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", "auto", "on", "true", "1", "yes", "enabled", "enable":
		return false, ""
	case "off", "false", "0", "no", "disabled", "disable":
		return true, ""
	default:
		return true, fmt.Sprintf("RELAYMESH_TERMINAL_RECORD=%q 无法识别（可选 auto / off），终端记录已保持关闭", raw)
	}
}
