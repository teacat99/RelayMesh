package docs

import (
	"fmt"
	"path"
	"strings"
	"unicode"
)

const (
	PathMaxLen   = 512
	BodyMaxBytes = 200 * 1024
	TagMax       = 8
	TagItemMax   = 24
	DocsPerWFMax = 500
	// DocsPerProjectMax 是项目范围列表一次最多取多少篇（设计稿 06 §3.3：每项目 2000）。
	DocsPerProjectMax = 2000
	ImportMaxFiles    = 100
	ImportMaxBytes    = 2 * 1024 * 1024
	RevisionKeep      = 50
)

// NormalizePath 规范化文档路径：去首尾空白、反斜杠转正斜杠、无扩展名补 .md。
// 不通过校验时返回 error。
func NormalizePath(raw string) (string, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return "", fmt.Errorf("路径不能为空")
	}
	s = strings.ReplaceAll(s, `\`, `/`)
	if strings.HasPrefix(s, "/") {
		return "", fmt.Errorf("路径不能为绝对路径")
	}
	s = path.Clean(s)
	s = strings.TrimPrefix(s, "/")
	if s == "" || s == "." {
		return "", fmt.Errorf("路径无效")
	}
	if strings.Contains(s, "..") {
		return "", fmt.Errorf("路径不能包含 ..")
	}
	for _, r := range s {
		if r < 32 || r == 127 {
			return "", fmt.Errorf("路径不能包含控制字符")
		}
	}
	if len(s) > PathMaxLen {
		return "", fmt.Errorf("路径过长（最多 %d 字符）", PathMaxLen)
	}
	if !strings.HasSuffix(strings.ToLower(s), ".md") {
		s += ".md"
	}
	return s, nil
}

// PathDir 返回路径的目录部分（不含末尾斜杠）；根文档返回 ""。
func PathDir(p string) string {
	d := path.Dir(p)
	if d == "." {
		return ""
	}
	return d
}

// PathBaseName 返回不带 .md 的文件名。
func PathBaseName(p string) string {
	base := path.Base(p)
	return strings.TrimSuffix(base, ".md")
}

// ParentPrefix 若 prefix 是 parent 的前缀（按路径段），返回 true。
func ParentPrefix(parent, child string) bool {
	if parent == "" {
		return true
	}
	if child == parent {
		return true
	}
	return strings.HasPrefix(child, parent+"/")
}

func isCJK(r rune) bool {
	return unicode.Is(unicode.Han, r) ||
		(r >= 0x3040 && r <= 0x30FF) || // Hiragana/Katakana
		(r >= 0xAC00 && r <= 0xD7AF) // Hangul
}
