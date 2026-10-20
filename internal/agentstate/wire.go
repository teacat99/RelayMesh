package agentstate

import (
	"regexp"
	"strings"
	"time"
)

// hook 自报字段的清洗。字段都由调用方自报，所以一律收敛到已知取值 / 安全字符集；事件表里不会出现提示词、思考或工具参数正文。

var (
	toolUnsafeRe = regexp.MustCompile(`[^A-Za-z0-9_:.\-]`)
	versionRe    = regexp.MustCompile(`^[A-Za-z0-9._+\-]{1,32}$`)
)

// ParseWire 把 hook 随 inbox_peek 汇报的 agent_event / agent_status 翻译成状态机事件。
// 旧版 hook 只会报 tool（= postToolUse）与 stop；chat_end、approval_* 只能由 Hub 自己产生，hook 报这些名字一律不认。
func ParseWire(kind, status string) (name, norm string, ok bool) {
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case "tool", EvToolEnd:
		return EvToolEnd, "", true
	case EvToolStart:
		return EvToolStart, "", true
	case EvToolFail:
		return EvToolFail, "", true
	case EvStop:
		return EvStop, NormalizeStopStatus(status), true
	case EvSessionStart:
		return EvSessionStart, "", true
	case EvSessionEnd:
		return EvSessionEnd, NormalizeEndReason(status, EndClosed), true
	case EvPrompt:
		return EvPrompt, "", true
	case EvThought:
		return EvThought, "", true
	case EvResponse:
		return EvResponse, "", true
	case EvCompact:
		return EvCompact, "", true
	}
	return "", "", false
}

// SanitizeTool 清洗工具名：只留字母数字与 _ : . -，其余换成 _，最长 64 个字符。
func SanitizeTool(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	s = toolUnsafeRe.ReplaceAllString(s, "_")
	if r := []rune(s); len(r) > 64 {
		s = string(r[:64])
	}
	return s
}

// ParseUnixMilli 把 hook 自报的毫秒时间戳转成 time.Time；明显不对（非正、早于 2020 年、比现在晚一天以上）就当没有。
func ParseUnixMilli(ms int64, now time.Time) time.Time {
	if ms <= 0 {
		return time.Time{}
	}
	t := time.UnixMilli(ms)
	if t.Year() < 2020 || t.After(now.Add(24*time.Hour)) {
		return time.Time{}
	}
	return t
}

// NormalizeClientKind 把客户端类型收敛到 ide / cli / unknown；没报就是空串。
func NormalizeClientKind(s string) string {
	switch v := strings.ToLower(strings.TrimSpace(s)); v {
	case "":
		return ""
	case "ide", "cli":
		return v
	}
	return "unknown"
}

// NormalizeVersion 清洗客户端版本号：只认字母数字与 . _ + -，最长 32 位；不合格当作没有。
func NormalizeVersion(s string) string {
	s = strings.TrimSpace(s)
	if versionRe.MatchString(s) {
		return s
	}
	return ""
}
