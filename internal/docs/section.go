package docs

import (
	"fmt"
	"strings"
)

// GetSection 按标题文本或锚点取一节（从该标题到同级或更高级标题之前）。
func GetSection(body, section string) (string, bool) {
	heads := ParseHeadings(body)
	if len(heads) == 0 {
		return "", false
	}
	lines := strings.Split(body, "\n")
	target := -1
	section = strings.TrimSpace(section)
	for i, h := range heads {
		if h.Text == section || h.Anchor == section || strings.EqualFold(h.Text, section) {
			target = i
			break
		}
	}
	if target < 0 {
		return "", false
	}
	startLine := heads[target].Line - 1
	endLine := len(lines)
	level := heads[target].Level
	for j := target + 1; j < len(heads); j++ {
		if heads[j].Level <= level {
			endLine = heads[j].Line - 1
			break
		}
	}
	return strings.Join(lines[startLine:endLine], "\n"), true
}

// ReplaceSection 替换一节；找不到标题时返回 error。
func ReplaceSection(body, section, newSection string) (string, error) {
	heads := ParseHeadings(body)
	if len(heads) == 0 {
		return "", fmt.Errorf("文档没有标题，无法按节替换")
	}
	lines := strings.Split(body, "\n")
	target := -1
	section = strings.TrimSpace(section)
	for i, h := range heads {
		if h.Text == section || h.Anchor == section || strings.EqualFold(h.Text, section) {
			target = i
			break
		}
	}
	if target < 0 {
		return "", fmt.Errorf("找不到标题：%s", section)
	}
	startLine := heads[target].Line - 1
	endLine := len(lines)
	level := heads[target].Level
	for j := target + 1; j < len(heads); j++ {
		if heads[j].Level <= level {
			endLine = heads[j].Line - 1
			break
		}
	}
	var parts []string
	if startLine > 0 {
		parts = append(parts, strings.Join(lines[:startLine], "\n"))
	}
	parts = append(parts, strings.TrimRight(newSection, "\n"))
	if endLine < len(lines) {
		rest := strings.Join(lines[endLine:], "\n")
		if rest != "" {
			// 后面还有别的节：新节体去掉了末尾换行，而节与节之间的空行原本属于被替换的那一节，所以要补一个空行，
			// 否则下一个标题会紧贴在上一节的最后一行后面（列表项、段落都会被它打断得很难看）。
			parts = append(parts, "", rest)
		}
	}
	return strings.Join(parts, "\n"), nil
}

// AppendToBody 在正文末尾追加内容（保证中间有空行）。
func AppendToBody(body, chunk string) string {
	body = strings.TrimRight(body, "\n")
	chunk = strings.TrimSpace(chunk)
	if chunk == "" {
		return body
	}
	if body == "" {
		return chunk
	}
	return body + "\n\n" + chunk
}

// PrependToBody 在正文开头插入内容。
func PrependToBody(body, chunk string) string {
	chunk = strings.TrimSpace(chunk)
	if chunk == "" {
		return body
	}
	body = strings.TrimLeft(body, "\n")
	if body == "" {
		return chunk
	}
	return chunk + "\n\n" + body
}
