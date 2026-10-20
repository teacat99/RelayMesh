package docs

import (
	"regexp"
	"strings"
	"unicode"
)

// Heading 是正文里的一级～六级标题（围栏代码块内的 # 不算）。
type Heading struct {
	Level  int    `json:"level"`
	Text   string `json:"text"`
	Anchor string `json:"anchor"`
	Line   int    `json:"line"` // 1-based
}

var headingRe = regexp.MustCompile(`^(#{1,6})\s+(.+?)\s*$`)

// ParseHeadings 解析 Markdown 标题并生成锚点（中文友好 slug，同名加序号）。
func ParseHeadings(body string) []Heading {
	lines := strings.Split(body, "\n")
	inFence := false
	var fenceMarker string
	var out []Heading
	used := map[string]int{}

	for i, line := range lines {
		trim := strings.TrimSpace(line)
		if strings.HasPrefix(trim, "```") {
			if !inFence {
				inFence = true
				fenceMarker = strings.TrimPrefix(trim, "```")
			} else if fenceMarker == "" || strings.HasPrefix(trim, "```"+fenceMarker) || trim == "```" {
				inFence = false
				fenceMarker = ""
			}
			continue
		}
		if inFence {
			continue
		}
		m := headingRe.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		level := len(m[1])
		text := strings.TrimSpace(m[2])
		base := slugAnchor(text)
		used[base]++
		anchor := base
		if used[base] > 1 {
			anchor = base + "-" + itoa(used[base])
		}
		out = append(out, Heading{
			Level:  level,
			Text:   text,
			Anchor: anchor,
			Line:   i + 1,
		})
	}
	return out
}

// slugAnchor 生成标题锚点：保留 CJK，空白变 -，去掉其它标点。
func slugAnchor(text string) string {
	var b strings.Builder
	lastDash := false
	for _, r := range strings.TrimSpace(text) {
		if isCJK(r) || unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(unicode.ToLower(r))
			lastDash = false
			continue
		}
		if unicode.IsSpace(r) || r == '-' || r == '_' {
			if !lastDash && b.Len() > 0 {
				b.WriteByte('-')
				lastDash = true
			}
		}
	}
	s := strings.Trim(b.String(), "-")
	if s == "" {
		return "section"
	}
	return s
}

func itoa(n int) string {
	if n < 10 {
		return string(rune('0' + n))
	}
	var digits []byte
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	return string(digits)
}

// ExtractTitle 取第一个 H1，否则用文件名（不含 .md）。
func ExtractTitle(body, fileName string) string {
	for _, h := range ParseHeadings(body) {
		if h.Level == 1 {
			return h.Text
		}
	}
	return strings.TrimSuffix(pathBase(fileName), ".md")
}

func pathBase(p string) string {
	if i := strings.LastIndex(p, "/"); i >= 0 {
		return p[i+1:]
	}
	return p
}
