package terminal

import (
	"fmt"
	"strings"
)

// ResolveExec 规范化 RELAYMESH_TERMINAL_EXEC，返回「是否允许模型执行」与需要提示给运维的警告。
//
//   - 空 / off / false / 0 / no：关闭（默认）。模型执行是比人工终端更大的授权面（令牌可以代表一个不在场的执行者），
//     所以需要运维显式开启，且仍受终端总闸与逐主机授权约束；
//   - on / true / 1 / yes：开启；
//   - 其他取值：保持关闭并给出警告（fail-closed）。
func ResolveExec(raw string) (on bool, warning string) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", "off", "false", "0", "no":
		return false, ""
	case "on", "true", "1", "yes":
		return true, ""
	}
	return false, fmt.Sprintf("RELAYMESH_TERMINAL_EXEC=%q 无法识别（可选 on / off），模型执行已保持关闭", raw)
}
