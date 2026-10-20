package docs

import (
	"strings"
	"unicode/utf8"
)

// SearchHit 是一条搜索命中。
type SearchHit struct {
	Path    string  `json:"path"`
	Title   string  `json:"title"`
	Score   float64 `json:"score"`
	Snippet string  `json:"snippet"`
	Anchor  string  `json:"anchor,omitempty"`
}

// ScoreDocument 对单篇文档打分（q 已在调用方 trim 并小写）。
func ScoreDocument(q, path, title, body string, tags []string) (float64, string, string) {
	if q == "" {
		return 0, "", ""
	}
	ql := strings.ToLower(q)
	score := 0.0
	if strings.Contains(strings.ToLower(path), ql) {
		score += 3
	}
	if strings.Contains(strings.ToLower(title), ql) {
		score += 5
	}
	for _, t := range tags {
		if strings.Contains(strings.ToLower(t), ql) {
			score += 2
		}
	}
	bodyLower := strings.ToLower(body)
	idx := strings.Index(bodyLower, ql)
	if idx >= 0 {
		score += 4
		snip, anchor := snippetAround(body, idx, len(q))
		return score, snip, anchor
	}
	// 标题锚点匹配
	for _, h := range ParseHeadings(body) {
		if strings.Contains(strings.ToLower(h.Text), ql) {
			score += 3
			return score, h.Text, h.Anchor
		}
	}
	return score, "", ""
}

func snippetAround(body string, byteIdx, qLen int) (snippet, anchor string) {
	const half = 80
	runes := []rune(body)
	// 将 byte 索引转为 rune 索引（近似：对 ASCII 为主的内容足够）
	runeIdx := utf8.RuneCountInString(body[:byteIdx])
	start := runeIdx - half
	if start < 0 {
		start = 0
	}
	end := runeIdx + qLen + half
	if end > len(runes) {
		end = len(runes)
	}
	snip := string(runes[start:end])
	if start > 0 {
		snip = "…" + snip
	}
	if end < len(runes) {
		snip = snip + "…"
	}
	// 找最近的标题作为锚点
	linesBefore := strings.Count(body[:byteIdx], "\n")
	anchor = nearestHeadingAnchor(body, linesBefore+1)
	return snip, anchor
}

func nearestHeadingAnchor(body string, line1Based int) string {
	var last string
	for _, h := range ParseHeadings(body) {
		if h.Line <= line1Based {
			last = h.Anchor
		}
	}
	return last
}
