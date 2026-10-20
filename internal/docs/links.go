package docs

import (
	"path"
	"regexp"
	"strings"
)

var (
	wikiLinkRe = regexp.MustCompile(`\[\[([^\[\]\n]+?)\]\]`)
	mdLinkRe   = regexp.MustCompile(`\[[^\[\]\n]*\]\(([^()\s]+)(?:\s+"[^"]*")?\)`)
	schemeRe   = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9+.-]*:`)
)

// stripCode 去掉围栏代码块和行内代码，避免把示例里的 [[x]] 当成真链接。
func stripCode(body string) string {
	var out strings.Builder
	inFence := false
	for _, line := range strings.Split(body, "\n") {
		t := strings.TrimSpace(line)
		if strings.HasPrefix(t, "```") || strings.HasPrefix(t, "~~~") {
			inFence = !inFence
			continue
		}
		if inFence {
			continue
		}
		for {
			i := strings.IndexByte(line, '`')
			if i < 0 {
				break
			}
			j := strings.IndexByte(line[i+1:], '`')
			if j < 0 {
				break
			}
			line = line[:i] + line[i+1+j+1:]
		}
		out.WriteString(line)
		out.WriteByte('\n')
	}
	return out.String()
}

// ExtractLinks 从正文里取出指向其他文档的链接目标（已规范化为 .md 路径）。
// 识别 [[path]] / [[path|显示名]] / [[path#锚点]]，以及 [文字](相对路径.md)；
// 带协议的外链、纯锚点、绝对路径、Artifact 引用（[[artifact:N]]）都跳过。
// fromPath 是当前文档路径，用来解析 ./ 和 ../ 开头的相对链接；结果去重并保持出现顺序。
func ExtractLinks(fromPath, body string) []string {
	text := stripCode(body)
	seen := map[string]bool{}
	var out []string
	add := func(raw string, rootRelative bool) {
		t := strings.TrimSpace(raw)
		if i := strings.IndexAny(t, "#?"); i >= 0 {
			t = t[:i]
		}
		if t == "" || schemeRe.MatchString(t) || strings.HasPrefix(t, "/") {
			return
		}
		t = strings.ReplaceAll(t, `\`, `/`)
		if ext := path.Ext(t); ext != "" && !strings.EqualFold(ext, ".md") {
			return // 图片、PDF 等不是文档链接
		}
		rel := strings.HasPrefix(t, "./") || strings.HasPrefix(t, "../")
		if rel || !rootRelative {
			t = path.Join(PathDir(fromPath), t)
		}
		n, err := NormalizePath(t)
		if err != nil || seen[n] {
			return
		}
		seen[n] = true
		out = append(out, n)
	}
	type hit struct {
		pos  int
		raw  string
		root bool
	}
	var hits []hit
	for _, m := range wikiLinkRe.FindAllStringSubmatchIndex(text, -1) {
		raw := text[m[2]:m[3]]
		if i := strings.IndexByte(raw, '|'); i >= 0 {
			raw = raw[:i]
		}
		if strings.HasPrefix(strings.TrimSpace(raw), "artifact:") {
			continue
		}
		hits = append(hits, hit{m[0], raw, true})
	}
	for _, m := range mdLinkRe.FindAllStringSubmatchIndex(text, -1) {
		hits = append(hits, hit{m[0], text[m[2]:m[3]], false})
	}
	// 按出现位置排序（两类各自有序，这里合并）
	for i := 1; i < len(hits); i++ {
		for j := i; j > 0 && hits[j].pos < hits[j-1].pos; j-- {
			hits[j], hits[j-1] = hits[j-1], hits[j]
		}
	}
	for _, h := range hits {
		add(h.raw, h.root)
	}
	return out
}
