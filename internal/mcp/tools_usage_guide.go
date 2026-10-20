package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/teacat99/RelayMesh/v2/internal/guide"
	"github.com/teacat99/RelayMesh/v2/internal/store"
)

// usage_guide：内置使用手册（#181）。纯只读，不碰数据库，也不需要 workflow_id；内容来自随二进制内嵌的 docs/（internal/guide）。

const (
	usageGuideDefaultChars = 12000
	usageGuideMaxChars     = 30000
	usageGuideSearchLimit  = 8
)

type usageGuideArgs struct {
	Action     string `json:"action"`
	Query      string `json:"query"`
	Limit      int    `json:"limit"`
	Topic      string `json:"topic"`
	Section    string `json:"section"`
	MaxChars   int    `json:"max_chars"`
	NextOffset int    `json:"next_offset"`
}

func (s *Server) handleUsageGuide(_ context.Context, raw json.RawMessage) (any, error) {
	var args usageGuideArgs
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &args); err != nil {
			return nil, store.NewInvalidInputError(fmt.Sprintf("invalid arguments: %v", err))
		}
	}
	g := guide.Default
	switch action := strings.ToLower(strings.TrimSpace(args.Action)); action {
	case "", "list":
		return usageGuideList(g, s.cfg.Version)
	case "search":
		return usageGuideSearch(g, args)
	case "get":
		return usageGuideGet(g, args)
	default:
		return nil, store.NewInvalidInputError(fmt.Sprintf("unknown action %q: expected list, search or get", args.Action))
	}
}

func usageGuideList(g *guide.Guide, version string) (any, error) {
	topics := g.Topics()
	items := make([]map[string]any, 0, len(topics))
	for _, t := range topics {
		info, err := g.Info(t)
		if err != nil {
			return nil, err
		}
		items = append(items, map[string]any{
			"id": t.ID, "title": t.Title, "summary": t.Summary,
			"chars": info.Chars, "sections": info.Sections,
		})
	}
	return map[string]any{
		"hub_version": version,
		"total":       len(items),
		"topics":      items,
		"hint": "手册随这个 Hub 的版本内嵌，描述的是它现在的功能。按关键词找小节：search {query}；读一篇：get {topic}（长文先看返回的 headings，再用 section 读一节）；" +
			"你实际能用哪些工具取决于你的凭据权限（见 tools/list），手册里写的网页操作是给用户看的。",
	}, nil
}

func usageGuideSearch(g *guide.Guide, args usageGuideArgs) (any, error) {
	q := strings.TrimSpace(args.Query)
	if q == "" {
		return nil, store.NewInvalidInputError("search 需要 query（关键词，空格分隔；每个词都要出现在同一小节里）")
	}
	limit := args.Limit
	if limit <= 0 {
		limit = usageGuideSearchLimit
	}
	hits := g.Search(q, limit)
	if hits == nil {
		hits = []guide.Hit{}
	}
	res := map[string]any{"query": q, "total": len(hits), "hits": hits}
	if len(hits) == 0 {
		res["hint"] = "没有命中：换同义词或更短的关键词再试，或用 list 看全部主题。手册里确实没有的，请如实告诉用户「手册里没有这部分」，不要凭记忆编造。"
	} else {
		res["hint"] = "用 get {topic, section} 读命中的小节：section 填命中里的 anchor 并在前面加 #（例如 \"#运行预设\"）最稳，也可以直接填小节标题；没有 section 的命中指向整个主题（开头的导语，或只是同义词对得上），直接 get {topic} 看目录再选小节。"
	}
	return res, nil
}

func usageGuideGet(g *guide.Guide, args usageGuideArgs) (any, error) {
	if strings.TrimSpace(args.Topic) == "" {
		return nil, store.NewInvalidInputError("get 需要 topic（主题标识，见 action=list）")
	}
	t, ok := g.Lookup(args.Topic)
	if !ok {
		ids := make([]string, 0, len(g.Topics()))
		for _, x := range g.Topics() {
			ids = append(ids, x.ID)
		}
		return nil, store.NewInvalidInputError(fmt.Sprintf("没有这个主题 %q；可用主题：%s", args.Topic, strings.Join(ids, ", ")))
	}
	if args.NextOffset < 0 || args.MaxChars < 0 {
		return nil, store.NewInvalidInputError("next_offset 与 max_chars 不能为负数")
	}
	maxChars := args.MaxChars
	if maxChars == 0 {
		maxChars = usageGuideDefaultChars
	}
	if maxChars > usageGuideMaxChars {
		maxChars = usageGuideMaxChars
	}

	var (
		text    string
		heading string
		anchor  string
		err     error
	)
	if strings.TrimSpace(args.Section) != "" {
		sec, hd, serr := g.Section(t, args.Section)
		if serr != nil {
			var se *guide.SectionError
			if errors.As(serr, &se) {
				return nil, store.NewInvalidInputError(se.Error())
			}
			return nil, serr
		}
		text, heading, anchor = sec, hd.Text, hd.Anchor
	} else {
		text, err = g.Body(t)
		if err != nil {
			return nil, err
		}
	}

	chunk, next, total, perr := pageText(text, args.NextOffset, maxChars)
	if perr != nil {
		return nil, store.NewInvalidInputError(perr.Error())
	}
	res := map[string]any{
		"topic":          t.ID,
		"title":          t.Title,
		"total_chars":    total,
		"offset":         args.NextOffset,
		"returned_chars": len([]rune(chunk)),
		"body":           chunk,
	}
	if heading != "" {
		res["section"] = heading
	}
	if next > 0 {
		res["next_offset"] = next
		res["hint"] = fmt.Sprintf("还没读完（共 %d 字符）：继续用 get {topic: %q, %snext_offset: %d}。", total, t.ID, sectionArgHint(anchor), next)
	}
	// 第一页、没指定小节时附上大纲：长文先看大纲，再用 section 读需要的一节。
	if heading == "" && args.NextOffset == 0 {
		outline, oerr := g.Outline(t, 3)
		if oerr != nil {
			return nil, oerr
		}
		heads := make([]map[string]any, 0, len(outline))
		for _, h := range outline {
			heads = append(heads, map[string]any{"level": h.Level, "text": h.Text, "anchor": h.Anchor})
		}
		res["headings"] = heads
	}
	return res, nil
}

// sectionArgHint 是翻页提示里要原样带上的 section 参数：用 #锚点，同名标题也不会取错。
func sectionArgHint(anchor string) string {
	if anchor == "" {
		return ""
	}
	return fmt.Sprintf("section: %q, ", "#"+anchor)
}

// pageText 按字符（rune）分页：从 offset 取最多 maxChars 个字符；还有剩余时尽量在最近的换行处断开（只回看页长的 15%），
// 返回本页内容、下一页的起点（没有下一页是 0）与全文字符数。
func pageText(text string, offset, maxChars int) (chunk string, next, total int, err error) {
	runes := []rune(text)
	total = len(runes)
	if offset > total {
		return "", 0, total, fmt.Errorf("next_offset %d 超出全文长度 %d", offset, total)
	}
	end := offset + maxChars
	if end >= total {
		return string(runes[offset:]), 0, total, nil
	}
	// 在 [end-back, end) 里找最后一个换行，找到就断在它后面。
	back := maxChars * 15 / 100
	for i := end - 1; i > offset && i >= end-back; i-- {
		if runes[i] == '\n' {
			end = i + 1
			break
		}
	}
	return string(runes[offset:end]), end, total, nil
}
