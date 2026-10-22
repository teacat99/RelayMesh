package terminal

import (
	"fmt"
	"strings"
)

// ResolveFiles 规范化 RELAYMESH_TERMINAL_FILES，返回「是否允许模型访问主机上的文件」与需要提示给运维的警告。
//
//   - 空 / off / false / 0 / no：关闭（默认）。文件能力让模型可以读取主机上的内容、在主机上写入文件，
//     和执行命令一样是需要运维显式开启的授权面；
//   - on / true / 1 / yes：开启（还需要 RELAYMESH_TERMINAL_EXEC=on 与终端总闸，以及令牌的文件权限与允许目录）；
//   - 其他取值：保持关闭并给出警告（fail-closed）。
func ResolveFiles(raw string) (on bool, warning string) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", "off", "false", "0", "no":
		return false, ""
	case "on", "true", "1", "yes":
		return true, ""
	}
	return false, fmt.Sprintf("RELAYMESH_TERMINAL_FILES=%q 无法识别（可选 on / off），文件能力已保持关闭", raw)
}
