// Package guide 是随二进制内嵌的「使用手册」：docs/ 目录里的 Markdown 文档，经 MCP 工具 usage_guide 按需读取。
//
// 目的：接入的智能体不会用某个功能时，可以直接查手册（而不是凭记忆回答），用户也可以一句话让会话自己去查。
// 手册随 Hub 的版本一起发布，所以描述的就是这个 Hub 现在能做的事，不会出现「文档比程序新或旧」。
//
// 这个包只负责「有哪些主题、怎么按标题 / 关键词取内容」，不依赖数据库，也不知道 MCP；工具层在 internal/mcp。
package guide

import (
	"fmt"
	"io/fs"
	"math"
	"sort"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"

	"github.com/teacat99/RelayMesh/v2/docs"
	idocs "github.com/teacat99/RelayMesh/v2/internal/docs"
)

// Topic 是手册里的一个主题，对应 docs/ 下的一个 Markdown 文件。
type Topic struct {
	// ID 是稳定的短标识，usage_guide 用它取内容；只用小写字母、数字与连字符。
	ID string `json:"id"`
	// File 是 docs/ 下的文件名（扁平，没有子目录）。
	File string `json:"file"`
	// Title 是给人看的标题；不取文件里的 H1，因为 H1 常带「操作手册」「迁移指南」之类的后缀，列表里太长。
	Title string `json:"title"`
	// Summary 一两句话：这个主题回答什么问题。list 与搜索都用它，写成「读者会怎么问」的样子。
	Summary string `json:"summary"`
	// Keywords 是搜索用的同义词 / 英文名 / 常见叫法（不出现在列表里）。
	Keywords []string `json:"-"`
}

// Info 是主题的体量信息，列表里给出，方便智能体决定「整篇读」还是「先看大纲再读一节」。
type Info struct {
	Chars    int // 全文字符数（rune）
	Sections int // 二、三级标题的个数
}

// SectionError 是按标题取一节失败的原因：没有这个标题，或者有多个同样接近的标题。
type SectionError struct {
	Ref        string
	Ambiguous  bool
	Candidates []string // 最多 12 个相近的标题；没有相近的就是文档里靠前的标题
}

func (e *SectionError) Error() string {
	if e.Ambiguous {
		return fmt.Sprintf("标题 %q 匹配到多个小节，请用更完整的标题或锚点：%s", e.Ref, strings.Join(e.Candidates, " | "))
	}
	return fmt.Sprintf("没有找到标题 %q；可用的标题（节选）：%s", e.Ref, strings.Join(e.Candidates, " | "))
}

// doc 是一个主题解析后的内容（只读）。
type doc struct {
	body     string
	chars    int
	headings []idocs.Heading
	units    []unit
}

// unit 是搜索的最小单位：一个二～三级标题连同它下面直到下一个二～三级标题之前的正文（更深层的标题并入）。
type unit struct {
	heading idocs.Heading // 文档开头第一个标题之前的部分用 Level 0 表示
	text    string        // 含标题行本身
	lower   string
}

// Guide 是一组主题与它们的内容。
type Guide struct {
	fsys   fs.FS
	topics []Topic

	once sync.Once
	docs map[string]*doc
	err  error
}

// New 用给定的文件系统与主题登记表建一个 Guide（测试用合成文档；正式使用见 Default）。
func New(fsys fs.FS, topics []Topic) *Guide {
	cp := make([]Topic, len(topics))
	copy(cp, topics)
	return &Guide{fsys: fsys, topics: cp}
}

// Default 是随二进制内嵌的手册：docs/ 下的全部 Markdown，主题登记在 topics.go。
var Default = New(docs.FS, registry)

func (g *Guide) load() {
	g.once.Do(func() {
		g.docs = make(map[string]*doc, len(g.topics))
		for _, t := range g.topics {
			raw, err := fs.ReadFile(g.fsys, t.File)
			if err != nil {
				g.err = fmt.Errorf("手册主题 %s 的文件 %s 读不到：%w", t.ID, t.File, err)
				return
			}
			g.docs[t.ID] = parseDoc(string(raw))
		}
	})
}

func (g *Guide) doc(t Topic) (*doc, error) {
	g.load()
	if g.err != nil {
		return nil, g.err
	}
	d, ok := g.docs[t.ID]
	if !ok {
		return nil, fmt.Errorf("手册里没有主题 %s", t.ID)
	}
	return d, nil
}

func parseDoc(body string) *doc {
	body = strings.ReplaceAll(body, "\r\n", "\n")
	d := &doc{body: body, chars: utf8.RuneCountInString(body), headings: idocs.ParseHeadings(body)}
	lines := strings.Split(body, "\n")
	// 单位边界：二～三级标题（一级标题是文档题目，并入第一个单位）。
	var cuts []idocs.Heading
	for _, h := range d.headings {
		if h.Level == 2 || h.Level == 3 {
			cuts = append(cuts, h)
		}
	}
	start := 0
	flush := func(end int, h idocs.Heading) {
		if end <= start {
			return
		}
		text := strings.Join(lines[start:end], "\n")
		if strings.TrimSpace(text) == "" {
			return
		}
		d.units = append(d.units, unit{heading: h, text: text, lower: strings.ToLower(text)})
	}
	cur := idocs.Heading{} // 第一个二～三级标题之前的导语（含文档题目）：Level 0
	for _, h := range cuts {
		flush(h.Line-1, cur)
		start = h.Line - 1
		cur = h
	}
	flush(len(lines), cur)
	return d
}

// Topics 按阅读顺序返回全部主题（副本）。
func (g *Guide) Topics() []Topic {
	out := make([]Topic, len(g.topics))
	copy(out, g.topics)
	return out
}

// Lookup 按标识取主题：不区分大小写，也接受文件名（带或不带 .md）。
func (g *Guide) Lookup(ref string) (Topic, bool) {
	r := strings.ToLower(strings.TrimSpace(ref))
	r = strings.TrimSuffix(r, ".md")
	if r == "" {
		return Topic{}, false
	}
	for _, t := range g.topics {
		if strings.ToLower(t.ID) == r || strings.TrimSuffix(strings.ToLower(t.File), ".md") == r {
			return t, true
		}
	}
	return Topic{}, false
}

// Info 返回主题的体量。
func (g *Guide) Info(t Topic) (Info, error) {
	d, err := g.doc(t)
	if err != nil {
		return Info{}, err
	}
	n := 0
	for _, h := range d.headings {
		if h.Level == 2 || h.Level == 3 {
			n++
		}
	}
	return Info{Chars: d.chars, Sections: n}, nil
}

// Body 返回主题的 Markdown 全文。
func (g *Guide) Body(t Topic) (string, error) {
	d, err := g.doc(t)
	if err != nil {
		return "", err
	}
	return d.body, nil
}

// Outline 返回主题的标题大纲（一到 maxLevel 级；maxLevel<=0 表示三级）。
func (g *Guide) Outline(t Topic, maxLevel int) ([]idocs.Heading, error) {
	d, err := g.doc(t)
	if err != nil {
		return nil, err
	}
	if maxLevel <= 0 {
		maxLevel = 3
	}
	var out []idocs.Heading
	for _, h := range d.headings {
		if h.Level <= maxLevel {
			out = append(out, h)
		}
	}
	return out, nil
}

// Section 取主题里的一节（连同它下面更深层的小节）。ref 可以是标题文字、锚点，或标题文字的唯一前缀 / 唯一子串
// （不区分大小写）——智能体常常只记得「8.12」或「运行预设」；以 # 开头表示明确指定锚点。
// 找不到、有歧义（含同名标题不止一个）时返回 *SectionError，同名标题的候选带着 #锚点，照着写就能取到指定的那一个。
func (g *Guide) Section(t Topic, ref string) (string, idocs.Heading, error) {
	d, err := g.doc(t)
	if err != nil {
		return "", idocs.Heading{}, err
	}
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return "", idocs.Heading{}, &SectionError{Ref: ref, Candidates: candidateTitles(d.headings, "", 12)}
	}
	lref := strings.ToLower(ref)
	// 0. 以 # 开头：明确指定锚点（同名标题里要取第一个时用它，因为第一个的锚点和标题文字相同）。
	if anchor, ok := strings.CutPrefix(ref, "#"); ok {
		for _, h := range d.headings {
			if h.Anchor == anchor {
				return sectionText(d, h)
			}
		}
		return "", idocs.Heading{}, &SectionError{Ref: ref, Candidates: candidateTitles(d.headings, "", 12)}
	}
	// 1. 标题文字：先区分大小写，再忽略大小写；同名标题不止一个时不替调用方挑第一个，让它改用 #锚点。
	for _, same := range []func(a, b string) bool{func(a, b string) bool { return a == b }, strings.EqualFold} {
		var hits []idocs.Heading
		for _, h := range d.headings {
			if same(h.Text, ref) {
				hits = append(hits, h)
			}
		}
		if len(hits) == 1 {
			return sectionText(d, hits[0])
		}
		if len(hits) > 1 {
			return "", idocs.Heading{}, &SectionError{Ref: ref, Ambiguous: true, Candidates: titlesOf(hits, 12)}
		}
	}
	// 2. 锚点（全文唯一，同名标题的后几个带序号，如 运行预设-2）。
	for _, h := range d.headings {
		if h.Anchor == ref {
			return sectionText(d, h)
		}
	}
	// 3. 唯一前缀，再 4. 唯一子串。
	for _, mode := range []string{"prefix", "contains"} {
		var hits []idocs.Heading
		for _, h := range d.headings {
			lt := strings.ToLower(h.Text)
			if (mode == "prefix" && strings.HasPrefix(lt, lref)) || (mode == "contains" && strings.Contains(lt, lref)) {
				hits = append(hits, h)
			}
		}
		if len(hits) == 1 {
			return sectionText(d, hits[0])
		}
		if len(hits) > 1 {
			return "", idocs.Heading{}, &SectionError{Ref: ref, Ambiguous: true, Candidates: titlesOf(hits, 12)}
		}
	}
	return "", idocs.Heading{}, &SectionError{Ref: ref, Candidates: candidateTitles(d.headings, lref, 12)}
}

func sectionText(d *doc, h idocs.Heading) (string, idocs.Heading, error) {
	// 用锚点取：同名标题的锚点带序号，保证拿到的就是这一个。
	text, ok := idocs.GetSection(d.body, h.Anchor)
	if !ok {
		return "", idocs.Heading{}, &SectionError{Ref: h.Text}
	}
	return text, h, nil
}

// titlesOf 列出候选标题；标题文字重复的，在后面标上锚点，调用方才有办法分辨并用锚点取。
func titlesOf(hs []idocs.Heading, max int) []string {
	count := map[string]int{}
	for _, h := range hs {
		count[h.Text]++
	}
	var out []string
	for _, h := range hs {
		if count[h.Text] > 1 {
			out = append(out, fmt.Sprintf("%s（锚点 #%s）", h.Text, h.Anchor))
		} else {
			out = append(out, h.Text)
		}
		if len(out) >= max {
			break
		}
	}
	return out
}

// candidateTitles 给「找不到」的提示挑候选：先放与 lref 有公共字符的，不够再放靠前的二～三级标题。
func candidateTitles(hs []idocs.Heading, lref string, max int) []string {
	var out []string
	seen := map[string]bool{}
	add := func(h idocs.Heading) {
		if len(out) < max && !seen[h.Text] {
			seen[h.Text] = true
			out = append(out, h.Text)
		}
	}
	if lref != "" {
		for _, h := range hs {
			if h.Level < 2 || h.Level > 3 {
				continue
			}
			lt := strings.ToLower(h.Text)
			for _, term := range strings.Fields(lref) {
				if strings.Contains(lt, term) {
					add(h)
					break
				}
			}
		}
	}
	for _, h := range hs {
		if h.Level == 2 || h.Level == 3 {
			add(h)
		}
	}
	return out
}

// Hit 是一条搜索命中（落在某个小节上）。
type Hit struct {
	Topic   string  `json:"topic"`
	Title   string  `json:"title"`
	Section string  `json:"section,omitempty"` // 命中的小节标题（文档开头的导语没有）
	Anchor  string  `json:"anchor,omitempty"`
	Snippet string  `json:"snippet"`
	Score   float64 `json:"score"`
}

const (
	maxHitsPerTopic = 3
	snippetHalf     = 70
)

// Search 在全部主题里按关键词找小节。
//
// 规则：查询按空白切成词，每个词都要在这一节（标题或正文）里出现（词多于三个时有三分之二即可）；
// 词没有字面出现在这一节、但是登记表给该篇登记的同义词（Keywords）时算一个较弱的命中，
// 前提是至少有一个词字面命中，这样「任务 指派」这类同义问法也能落到任务板那一篇。
// 标题命中比正文命中权重高，主题标题 / 摘要 / 同义词命中再加分。整段中文没有空格时，上面的严格规则可能一个都找不到，
// 此时退到「二字组」宽松匹配（至少 60% 的二字组命中）。每个主题最多给三条，避免一篇长文把结果占满。
// 最后，词全是某一篇的主题词、而这一篇没有任何小节字面命中时，补一条指向该篇开头的命中（见 topicHits）。
func (g *Guide) Search(query string, limit int) []Hit {
	q := strings.ToLower(strings.TrimSpace(query))
	if q == "" {
		return nil
	}
	if limit <= 0 {
		limit = 8
	}
	if limit > 30 {
		limit = 30
	}
	g.load()
	if g.err != nil {
		return nil
	}
	terms := strings.Fields(q)
	hits := g.search(terms, false)
	if len(hits) == 0 {
		if fuzzy := fuzzyTerms(terms); len(fuzzy) > 0 {
			hits = g.search(fuzzy, true)
		}
	}
	hits = append(hits, g.topicHits(terms, hits)...)
	if len(hits) > limit {
		hits = hits[:limit]
	}
	return hits
}

type scored struct {
	hit   Hit
	order int // 主题序 * 1e6 + 单位序：同分时按阅读顺序
}

// hasKeyword 报告 term（已转小写）是否是登记表给本篇登记的同义词之一。
func (t Topic) hasKeyword(term string) bool {
	for _, k := range t.Keywords {
		if strings.Contains(strings.ToLower(k), term) {
			return true
		}
	}
	return false
}

// wordScore 返回 term（已转小写）作为本篇主题词的得分：标题 3、摘要 2、登记表 Keywords 2，可叠加；0 表示它不是本篇的主题词。
func (t Topic) wordScore(term string) float64 {
	s := 0.0
	if strings.Contains(strings.ToLower(t.Title), term) {
		s += 3
	}
	if strings.Contains(strings.ToLower(t.Summary), term) {
		s += 2
	}
	if t.hasKeyword(term) {
		s += 2
	}
	return s
}

// topicHits 补上「整篇对得上、但词没有字面出现」的主题：查询的词全是某一篇的主题词 / 同义词，而这一篇还没有任何小节命中
// （比如只问「认领」，而任务板那一篇写的是「接取」）。命中没有小节，指向那一篇的开头，用 get 读主题再按目录选小节。
// have 是已有的命中（这些主题不重复给）；结果排在字面命中之后。
func (g *Guide) topicHits(terms []string, have []Hit) []Hit {
	seen := make(map[string]bool, len(have))
	for _, h := range have {
		seen[h.Topic] = true
	}
	var all []scored
	for ti, t := range g.topics {
		if g.docs[t.ID] == nil || seen[t.ID] {
			continue
		}
		total, every := 0.0, true
		for _, term := range terms {
			ws := t.wordScore(term)
			if ws == 0 {
				every = false
				break
			}
			total += ws
		}
		if !every {
			continue
		}
		all = append(all, scored{hit: Hit{Topic: t.ID, Title: t.Title, Snippet: t.Summary, Score: total}, order: ti})
	}
	sort.SliceStable(all, func(i, j int) bool {
		if all[i].hit.Score != all[j].hit.Score {
			return all[i].hit.Score > all[j].hit.Score
		}
		return all[i].order < all[j].order
	})
	out := make([]Hit, 0, len(all))
	for _, s := range all {
		out = append(out, s.hit)
	}
	return out
}

func (g *Guide) search(terms []string, fuzzy bool) []Hit {
	need := len(terms)
	switch {
	case fuzzy:
		need = int(math.Ceil(float64(len(terms)) * 0.6))
	case len(terms) > 3:
		need = int(math.Ceil(float64(len(terms)) * 2 / 3))
	}
	if need < 1 {
		need = 1
	}
	var all []scored
	for ti, t := range g.topics {
		d := g.docs[t.ID]
		if d == nil {
			continue
		}
		topicBonus := 0.0
		synonym := make([]bool, len(terms)) // 这个词是不是登记表里给本篇登记的同义词（Keywords）
		for i, term := range terms {
			topicBonus += t.wordScore(term)
			synonym[i] = t.hasKeyword(term)
		}
		var perTopic []scored
		for ui, u := range d.units {
			headLower := strings.ToLower(u.heading.Text)
			matched, literal, score := 0, 0, 0.0
			first := -1
			for i, term := range terms {
				inHead := u.heading.Text != "" && strings.Contains(headLower, term)
				n := strings.Count(u.lower, term)
				if inHead {
					score += 8
				}
				if n > 0 {
					score += math.Min(float64(n), 5)
					if first < 0 {
						first = strings.Index(u.lower, term)
					}
				}
				switch {
				case inHead || n > 0:
					matched++
					literal++
				case synonym[i]:
					// 词没有字面出现在这一节，但它是登记表给这一篇登记的同义词（比如问「指派」而任务板写的是「派任务」）：
					// 算一个较弱的命中，让同义问法也能落到该篇的相关小节上。只认登记的 Keywords，标题 / 摘要里的词
					// 不算（它们本来就该字面出现在正文里，放宽了会让不相干的小节混进来）。
					matched++
					score++
				}
			}
			// 至少要有一个词字面命中：否则一篇文档的所有小节都会因为主题词而被拉进结果。
			if literal == 0 || matched < need {
				continue
			}
			if matched == len(terms) {
				score += 2
			}
			score += topicBonus
			snip := snippet(u.text, u.lower, first)
			perTopic = append(perTopic, scored{
				hit: Hit{
					Topic: t.ID, Title: t.Title, Section: u.heading.Text, Anchor: u.heading.Anchor,
					Snippet: snip, Score: math.Round(score*10) / 10,
				},
				order: ti*1_000_000 + ui,
			})
		}
		sort.SliceStable(perTopic, func(i, j int) bool { return perTopic[i].hit.Score > perTopic[j].hit.Score })
		if len(perTopic) > maxHitsPerTopic {
			perTopic = perTopic[:maxHitsPerTopic]
		}
		all = append(all, perTopic...)
	}
	sort.SliceStable(all, func(i, j int) bool {
		if all[i].hit.Score != all[j].hit.Score {
			return all[i].hit.Score > all[j].hit.Score
		}
		return all[i].order < all[j].order
	})
	out := make([]Hit, 0, len(all))
	for _, s := range all {
		out = append(out, s.hit)
	}
	return out
}

// snippet 取命中位置前后各 snippetHalf 个字符，换行与连续空白压成一个空格。
func snippet(text, lower string, byteIdx int) string {
	if byteIdx < 0 {
		// 只有标题命中：取标题之后的开头。
		byteIdx = 0
	}
	// text 与 lower 的字节偏移可能因大小写折叠而不同（极少见，如个别 Unicode 字母），取 rune 偏移更稳。
	runeIdx := utf8.RuneCountInString(lower[:min(byteIdx, len(lower))])
	runes := []rune(text)
	start := runeIdx - snippetHalf
	if start < 0 {
		start = 0
	}
	end := runeIdx + snippetHalf*2
	if end > len(runes) {
		end = len(runes)
	}
	if start > end {
		start = end
	}
	s := string(runes[start:end])
	s = strings.Join(strings.Fields(s), " ")
	if start > 0 {
		s = "…" + s
	}
	if end < len(runes) {
		s += "…"
	}
	return s
}

// fuzzyTerms 把含中日韩字符且长于三个字符的词拆成相邻二字组；其余词原样保留。没有可拆的词时返回 nil（不必再搜一遍）。
func fuzzyTerms(terms []string) []string {
	var out []string
	changed := false
	for _, t := range terms {
		rs := []rune(t)
		if len(rs) > 3 && hasCJK(rs) {
			changed = true
			for i := 0; i+1 < len(rs); i++ {
				out = append(out, string(rs[i:i+2]))
			}
			continue
		}
		out = append(out, t)
	}
	if !changed {
		return nil
	}
	return out
}

func hasCJK(rs []rune) bool {
	for _, r := range rs {
		if unicode.Is(unicode.Han, r) {
			return true
		}
	}
	return false
}
