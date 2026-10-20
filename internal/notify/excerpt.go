package notify

import (
	"regexp"
	"strings"
)

// 预览是 Markdown：只保留正文文字，去掉标题井号、引用与列表符号、粗体与行内代码的标记，代码围栏整段丢弃。
var (
	mdLinePrefix = regexp.MustCompile(`^(#{1,6}\s+|>\s*|[-*+]\s+|\d+[.)]\s+)`)
	mdInline     = strings.NewReplacer("**", "", "__", "", "`", "")
)

// Excerpt 取 Markdown 开头的一小段纯文本作为预览：最多取前 6 个非空行，折成一行，并按 MaxPreviewRunes 截断。
// 结果仍会在 Event.Normalize 里再规整一次，所以这里只负责「去掉标记」。
func Excerpt(markdown string) string {
	parts := make([]string, 0, 6)
	inFence := false
	for _, line := range strings.Split(markdown, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "```") || strings.HasPrefix(line, "~~~") {
			inFence = !inFence
			continue
		}
		if inFence || line == "" {
			continue
		}
		line = strings.TrimSpace(mdInline.Replace(mdLinePrefix.ReplaceAllString(line, "")))
		if line != "" {
			parts = append(parts, line)
		}
		if len(parts) >= 6 {
			break
		}
	}
	return clipRunes(oneLine(strings.Join(parts, " ")), MaxPreviewRunes)
}
