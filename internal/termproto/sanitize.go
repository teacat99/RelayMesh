package termproto

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// maxConsecutiveMarks 限制连续的组合记号个数，防止「Zalgo」文本撑破界面布局。
const maxConsecutiveMarks = 3

// SanitizeText 把不可信文本（主机名、系统名、备注、会话名等）规整为可安全展示的纯文本：
//
//   - 非法 UTF-8 整体丢弃对应字节；
//   - 只保留「可显示」字符（字母、组合记号、数字、标点、符号与普通空格）；
//     控制字符（含换行、制表、ESC）、格式字符（双向控制符、零宽字符、BOM）、行 / 段分隔符、
//     私用区与未分配码位全部去除；其他各类空白统一映射为普通空格并折叠；
//   - 连续组合记号最多保留 maxConsecutiveMarks 个；
//   - 首尾空白去除，并按 rune 数截断到 maxRunes。
//
// 界面与日志只应按纯文本输出其结果（绝不使用 v-html）。maxRunes<=0 表示不限长，仅用于内部测试。
func SanitizeText(s string, maxRunes int) string {
	if s == "" {
		return ""
	}
	var b strings.Builder
	b.Grow(min(len(s), 4*max(maxRunes, 16)))

	runes := 0
	marks := 0
	lastSpace := true // 开头的空白直接丢弃
	for len(s) > 0 {
		r, size := utf8.DecodeRuneInString(s)
		s = s[size:]
		if r == utf8.RuneError && size <= 1 {
			continue // 非法字节
		}

		switch {
		case r == ' ' || unicode.Is(unicode.Zs, r):
			if lastSpace {
				continue
			}
			lastSpace = true
			marks = 0
			r = ' '
		case unicode.IsMark(r):
			if lastSpace || marks >= maxConsecutiveMarks {
				continue // 不允许以组合记号开头，也限制堆叠
			}
			marks++
			lastSpace = false
		case unicode.IsGraphic(r):
			marks = 0
			lastSpace = false
		default:
			continue // 控制 / 格式 / 分隔 / 私用 / 未分配
		}

		if maxRunes > 0 && runes >= maxRunes {
			break
		}
		b.WriteRune(r)
		runes++
	}
	return strings.TrimRight(b.String(), " ")
}
