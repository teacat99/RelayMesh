package guide

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"testing/fstest"
	"unicode/utf8"
)

const alphaDoc = `# Alpha 手册

这是 alpha 的导语，介绍整体。

## 安装步骤

先下载二进制，再运行 install 命令。

### 校验

用 SHA256 校验下载的文件。

## 运行预设

preset_run 需要 launcher 权限位。

## 运行预设

第二处同名标题，用来测试重名。

## 其他

无关内容。
`

const betaDoc = `# Beta 说明

## 运行预设入门

beta 里也提到 preset_run，但这里只是入门。

## 部署

部署时先安装，再运行。安装 安装 安装 安装 安装 安装 安装。
`

const zhDoc = `# 收件箱

## 追加指令

追加指令放进收件箱，怎么读取见下文。
`

func testGuide(t *testing.T) *Guide {
	t.Helper()
	fsys := fstest.MapFS{
		"alpha.md": {Data: []byte(alphaDoc)},
		"beta.md":  {Data: []byte(betaDoc)},
		"zh.md":    {Data: []byte(zhDoc)},
	}
	return New(fsys, []Topic{
		{ID: "alpha", File: "alpha.md", Title: "阿尔法手册", Summary: "安装与运行 alpha", Keywords: []string{"setup", "install-guide"}},
		{ID: "beta", File: "beta.md", Title: "贝塔说明", Summary: "beta 的入门与部署"},
		{ID: "zh", File: "zh.md", Title: "中文收件箱", Summary: "收件箱说明"},
	})
}

func mustTopic(t *testing.T, g *Guide, id string) Topic {
	t.Helper()
	tp, ok := g.Lookup(id)
	if !ok {
		t.Fatalf("topic %q not found", id)
	}
	return tp
}

func TestLookup(t *testing.T) {
	g := testGuide(t)
	for _, ref := range []string{"alpha", "ALPHA", "alpha.md", " Alpha.MD "} {
		tp, ok := g.Lookup(ref)
		if !ok || tp.ID != "alpha" {
			t.Fatalf("Lookup(%q) = %+v, %v; want alpha", ref, tp, ok)
		}
	}
	for _, ref := range []string{"", "  ", "nope", ".md"} {
		if tp, ok := g.Lookup(ref); ok {
			t.Fatalf("Lookup(%q) = %+v; want not found", ref, tp)
		}
	}
}

func TestTopicsReturnsCopy(t *testing.T) {
	g := testGuide(t)
	got := g.Topics()
	if len(got) != 3 || got[0].ID != "alpha" || got[2].ID != "zh" {
		t.Fatalf("Topics() order wrong: %+v", got)
	}
	got[0].ID = "changed"
	if g.Topics()[0].ID != "alpha" {
		t.Fatal("mutating the returned slice must not change the guide")
	}
}

func TestInfoBodyOutline(t *testing.T) {
	g := testGuide(t)
	tp := mustTopic(t, g, "alpha")

	info, err := g.Info(tp)
	if err != nil {
		t.Fatal(err)
	}
	if info.Chars != utf8.RuneCountInString(alphaDoc) {
		t.Fatalf("Chars = %d, want %d", info.Chars, utf8.RuneCountInString(alphaDoc))
	}
	// 二～三级标题：安装步骤、校验、运行预设、运行预设、其他
	if info.Sections != 5 {
		t.Fatalf("Sections = %d, want 5", info.Sections)
	}
	body, err := g.Body(tp)
	if err != nil || body != alphaDoc {
		t.Fatalf("Body mismatch (err=%v)", err)
	}

	two, err := g.Outline(tp, 2)
	if err != nil || len(two) != 5 {
		t.Fatalf("Outline(2) = %d headings (err=%v), want 5 (H1 + four H2)", len(two), err)
	}
	three, err := g.Outline(tp, 0)
	if err != nil || len(three) != 6 {
		t.Fatalf("Outline(0) = %d headings (err=%v), want 6 (default depth 3)", len(three), err)
	}
}

func TestSectionResolution(t *testing.T) {
	g := testGuide(t)
	tp := mustTopic(t, g, "alpha")

	// 精确标题：连同下面更深层的小节一起返回。
	text, h, err := g.Section(tp, "安装步骤")
	if err != nil {
		t.Fatal(err)
	}
	if h.Level != 2 || h.Anchor != "安装步骤" || !strings.Contains(text, "先下载二进制") || !strings.Contains(text, "### 校验") || !strings.Contains(text, "SHA256") {
		t.Fatalf("exact section wrong: heading=%+v text=%q", h, text)
	}
	if strings.Contains(text, "preset_run") {
		t.Fatalf("section must stop at the next same-level heading: %q", text)
	}

	// 锚点：只取三级小节本身。
	text, h, err = g.Section(tp, "校验")
	if err != nil || h.Level != 3 || strings.Contains(text, "先下载") || !strings.Contains(text, "SHA256") {
		t.Fatalf("anchor section wrong: err=%v heading=%+v text=%q", err, h, text)
	}

	// 唯一前缀、唯一子串。
	for _, ref := range []string{"安装", "步骤", "其"} {
		_, h, err := g.Section(tp, ref)
		if err != nil {
			t.Fatalf("Section(%q): %v", ref, err)
		}
		if h.Text == "" {
			t.Fatalf("Section(%q) returned empty heading", ref)
		}
	}
	if _, h, _ := g.Section(tp, "安装"); h.Text != "安装步骤" {
		t.Fatalf("prefix should resolve to 安装步骤, got %q", h.Text)
	}
	// 忽略大小写（英文标题）。
	bt := mustTopic(t, g, "beta")
	if _, h, err := g.Section(bt, "BETA 说明"); err != nil || h.Level != 1 {
		t.Fatalf("case-insensitive title failed: err=%v heading=%+v", err, h)
	}
}

func TestSectionDuplicateTitlesNeedAnchor(t *testing.T) {
	g := testGuide(t)
	tp := mustTopic(t, g, "alpha")

	_, _, err := g.Section(tp, "运行预设")
	var se *SectionError
	if !errors.As(err, &se) || !se.Ambiguous {
		t.Fatalf("duplicate exact title must be ambiguous, got %v", err)
	}
	// 候选里要带 #锚点，调用方才分得清、也照着写就能取到。
	joined := strings.Join(se.Candidates, "|")
	if !strings.Contains(joined, "锚点 #运行预设）") || !strings.Contains(joined, "锚点 #运行预设-2）") {
		t.Fatalf("candidates should carry #anchors: %v", se.Candidates)
	}
	if !strings.Contains(se.Error(), "多个小节") {
		t.Fatalf("error text should say ambiguous: %s", se.Error())
	}

	// 第一个同名标题的锚点和标题文字相同，所以要用 # 明确指定。
	text, h, err := g.Section(tp, "#运行预设")
	if err != nil || !strings.Contains(text, "launcher") || strings.Contains(text, "第二处同名") || h.Anchor != "运行预设" {
		t.Fatalf("first duplicate by #anchor wrong: err=%v heading=%+v text=%q", err, h, text)
	}
	for _, ref := range []string{"#运行预设-2", "运行预设-2"} {
		text, _, err = g.Section(tp, ref)
		if err != nil || !strings.Contains(text, "第二处同名") || strings.Contains(text, "launcher") {
			t.Fatalf("second duplicate via %q wrong: err=%v text=%q", ref, err, text)
		}
	}
	// 不存在的锚点是「找不到」，不是歧义。
	if _, _, err = g.Section(tp, "#没有这个锚点"); !errors.As(err, &se) || se.Ambiguous {
		t.Fatalf("unknown #anchor should be a plain not-found error, got %v", err)
	}
	// 前缀也会撞上两个同名标题。
	_, _, err = g.Section(tp, "运行")
	if !errors.As(err, &se) || !se.Ambiguous {
		t.Fatalf("ambiguous prefix expected, got %v", err)
	}
}

func TestSectionNotFoundListsCandidates(t *testing.T) {
	g := testGuide(t)
	tp := mustTopic(t, g, "alpha")

	for _, ref := range []string{"完全不存在", ""} {
		_, _, err := g.Section(tp, ref)
		var se *SectionError
		if !errors.As(err, &se) {
			t.Fatalf("Section(%q) want *SectionError, got %v", ref, err)
		}
		if se.Ambiguous || len(se.Candidates) == 0 {
			t.Fatalf("Section(%q): want not-ambiguous error with candidates, got %+v", ref, se)
		}
		if !strings.Contains(se.Error(), "安装步骤") {
			t.Fatalf("error should list available titles: %s", se.Error())
		}
	}
}

func TestSearchBasics(t *testing.T) {
	g := testGuide(t)

	if got := g.Search("", 5); got != nil {
		t.Fatalf("empty query must return nil, got %v", got)
	}
	if got := g.Search("   ", 5); got != nil {
		t.Fatalf("blank query must return nil, got %v", got)
	}
	if got := g.Search("这个词哪里都没有xyz", 5); len(got) != 0 {
		t.Fatalf("no match expected, got %v", got)
	}

	hits := g.Search("preset_run", 10)
	if len(hits) != 2 {
		t.Fatalf("preset_run should hit alpha and beta, got %+v", hits)
	}
	if hits[0].Topic != "alpha" || hits[0].Section != "运行预设" || hits[1].Topic != "beta" {
		t.Fatalf("equal scores must keep reading order: %+v", hits)
	}
	if !strings.Contains(hits[0].Snippet, "preset_run") || hits[0].Anchor == "" || hits[0].Title != "阿尔法手册" {
		t.Fatalf("hit fields incomplete: %+v", hits[0])
	}
}

func TestSearchAllTermsMustMatch(t *testing.T) {
	g := testGuide(t)
	hits := g.Search("安装 运行", 10)
	if len(hits) == 0 {
		t.Fatal("expected hits for 安装 运行")
	}
	for _, h := range hits {
		if h.Section == "运行预设" || h.Section == "运行预设入门" {
			t.Fatalf("a section lacking one of the terms must not match: %+v", h)
		}
	}
}

func TestSearchHeadingBeatsBodyCount(t *testing.T) {
	g := testGuide(t)
	hits := g.Search("安装", 10)
	if len(hits) < 2 {
		t.Fatalf("expected at least two hits, got %+v", hits)
	}
	// alpha「安装步骤」标题命中（权重 8），beta「部署」正文里重复很多次也只计到上限。
	if hits[0].Topic != "alpha" || hits[0].Section != "安装步骤" {
		t.Fatalf("heading match must rank first: %+v", hits)
	}
	var beta *Hit
	for i := range hits {
		if hits[i].Topic == "beta" {
			beta = &hits[i]
		}
	}
	if beta == nil || beta.Score > 7 {
		t.Fatalf("body occurrences must be capped (score <= 5 + all-terms bonus 2), got %+v", beta)
	}
}

func TestSearchTopicBonusAndKeywords(t *testing.T) {
	g := testGuide(t)
	// 「setup」只出现在 alpha 的同义词里，不在任何正文里：不会造出小节命中，只给一条指向 alpha 开头的主题级命中。
	hits := g.Search("setup", 5)
	if len(hits) != 1 || hits[0].Topic != "alpha" || hits[0].Section != "" || hits[0].Anchor != "" || hits[0].Snippet != "安装与运行 alpha" {
		t.Fatalf("a keyword-only query must yield exactly one topic-level pointer: %+v", hits)
	}

	// 同一份正文，主题的同义词命中会给它的小节加分。
	score := func(keywords []string) float64 {
		gg := New(fstest.MapFS{"a.md": {Data: []byte(alphaDoc)}}, []Topic{{ID: "a", File: "a.md", Title: "甲", Summary: "乙", Keywords: keywords}})
		hits := gg.Search("install", 5)
		if len(hits) != 1 {
			t.Fatalf("want one hit for install, got %+v", hits)
		}
		return hits[0].Score
	}
	plain, boosted := score(nil), score([]string{"install-guide"})
	if boosted <= plain {
		t.Fatalf("keyword match should raise the score: plain=%v boosted=%v", plain, boosted)
	}
}

// 登记表里的同义词让同义问法落到该篇的相关小节上，但不会放宽成「整篇文档都命中」，也不会让别的主题蹭进来。
func TestSearchKeywordSynonymsReachRelevantSections(t *testing.T) {
	const board = "# 任务板\n\n## 派任务\n\n用 assignee 把任务交给别的工作流。\n\n## 任务状态\n\n状态有 todo 与 doing，任务做完要写结论。\n\n## 无关\n\n这里只讲颜色。\n"
	const other = "# 其他\n\n## 杂项\n\n这里也提到指派，但属于别的主题。\n"
	g := New(fstest.MapFS{"board.md": {Data: []byte(board)}, "other.md": {Data: []byte(other)}}, []Topic{
		{ID: "board", File: "board.md", Title: "任务板", Summary: "任务协作", Keywords: []string{"指派", "认领"}},
		{ID: "other", File: "other.md", Title: "其他", Summary: "杂项"},
	})

	// 「任务 指派」：board 里没有「指派」二字，但它是登记的同义词：字面含「任务」的小节命中，
	// 一个词都没字面命中的「无关」不会混进来；other 里有「指派」却没有「任务」，也不是 board 的同义词范围，不命中。
	sections := map[string]bool{}
	for _, h := range g.Search("任务 指派", 10) {
		if h.Topic != "board" {
			t.Fatalf("only board may answer 任务 指派, got %+v", h)
		}
		sections[h.Section] = true
	}
	if !sections["派任务"] || !sections["任务状态"] || sections["无关"] {
		t.Fatalf("synonym search should reach the task sections only: %v", sections)
	}

	// 只问同义词、哪里都没有字面出现：给一条指向 board 开头的主题级命中。
	hits := g.Search("认领", 10)
	if len(hits) != 1 || hits[0].Topic != "board" || hits[0].Section != "" || hits[0].Snippet != "任务协作" {
		t.Fatalf("synonym-only query must point at the topic: %+v", hits)
	}

	// 字面命中在前、主题级命中在后；已经有小节命中的主题不重复给指针。
	hits = g.Search("指派", 10)
	if len(hits) != 2 || hits[0].Topic != "other" || hits[0].Section != "杂项" || hits[1].Topic != "board" || hits[1].Section != "" {
		t.Fatalf("literal hits first, topic pointer after: %+v", hits)
	}
	hits = g.Search("任务", 10)
	for _, h := range hits {
		if h.Topic == "board" && h.Section == "" && h.Snippet == "任务协作" {
			t.Fatalf("a topic that already has literal hits must not get a pointer too: %+v", hits)
		}
	}

	// 混进一个谁都不认识的词：同义词救不了它。
	if got := g.Search("认领 zzqq", 10); len(got) != 0 {
		t.Fatalf("an unknown word must still sink the query: %+v", got)
	}

	// 标题 / 摘要里的词不算同义词：「协作」只在摘要里，不会把没写「协作」的小节拉进来，只给主题级命中。
	for _, h := range g.Search("任务 协作", 10) {
		if h.Section != "" {
			t.Fatalf("summary words must not loosen section matching: %+v", h)
		}
	}
}

func TestSearchPerTopicCapAndLimit(t *testing.T) {
	var b strings.Builder
	b.WriteString("# Many\n")
	for i := 0; i < 6; i++ {
		fmt.Fprintf(&b, "\n## 小节%d\n\n这里有 keyword 出现。\n", i)
	}
	g := New(fstest.MapFS{
		"many.md":  {Data: []byte(b.String())},
		"other.md": {Data: []byte("# Other\n\n## 另一篇\n\n也有 keyword。\n")},
	}, []Topic{
		{ID: "many", File: "many.md", Title: "很多", Summary: "x"},
		{ID: "other", File: "other.md", Title: "另一", Summary: "y"},
	})

	hits := g.Search("keyword", 30)
	perTopic := map[string]int{}
	for _, h := range hits {
		perTopic[h.Topic]++
	}
	if perTopic["many"] != maxHitsPerTopic || perTopic["other"] != 1 {
		t.Fatalf("per-topic cap broken: %v", perTopic)
	}
	if got := g.Search("keyword", 2); len(got) != 2 {
		t.Fatalf("limit 2 returned %d", len(got))
	}
	if got := g.Search("keyword", 0); len(got) != 4 {
		t.Fatalf("default limit should return all 4 capped hits, got %d", len(got))
	}
}

func TestSearchFuzzyChineseFallback(t *testing.T) {
	g := testGuide(t)
	// 整句没有空格：严格模式找不到完整的这一串，退到二字组匹配。
	hits := g.Search("收件箱追加指令怎么读", 5)
	if len(hits) == 0 || hits[0].Topic != "zh" {
		t.Fatalf("fuzzy CJK fallback should find zh, got %+v", hits)
	}
	// 差得太远的整句不能被宽松匹配硬凑出结果。
	if got := g.Search("完全无关的一整句中文话", 5); len(got) != 0 {
		t.Fatalf("unrelated sentence must not match: %+v", got)
	}
}

func TestSearchSnippetIsTrimmed(t *testing.T) {
	long := "# Long\n\n## 长节\n\n" + strings.Repeat("前文填充", 80) + "关键词MARK" + strings.Repeat("后文填充", 80) + "\n"
	g := New(fstest.MapFS{"long.md": {Data: []byte(long)}}, []Topic{{ID: "long", File: "long.md", Title: "长", Summary: "z"}})
	hits := g.Search("mark", 3)
	if len(hits) != 1 {
		t.Fatalf("want one hit, got %+v", hits)
	}
	s := hits[0].Snippet
	if !strings.Contains(s, "MARK") || !strings.HasPrefix(s, "…") || !strings.HasSuffix(s, "…") {
		t.Fatalf("snippet should be centered with ellipses: %q", s)
	}
	if n := utf8.RuneCountInString(s); n > snippetHalf*3+4 {
		t.Fatalf("snippet too long: %d runes", n)
	}
	if strings.Contains(s, "\n") {
		t.Fatalf("snippet must be one line: %q", s)
	}
}

func TestPrefaceHitHasNoSection(t *testing.T) {
	g := testGuide(t)
	hits := g.Search("alpha 的导语", 5)
	if len(hits) == 0 {
		t.Fatal("expected a hit in the preface")
	}
	if hits[0].Section != "" || hits[0].Anchor != "" || hits[0].Topic != "alpha" {
		t.Fatalf("preface hit should carry no section: %+v", hits[0])
	}
}

func TestMissingFileSurfacesAsError(t *testing.T) {
	g := New(fstest.MapFS{}, []Topic{{ID: "ghost", File: "ghost.md", Title: "鬼", Summary: "无"}})
	tp, _ := g.Lookup("ghost")
	if _, err := g.Info(tp); err == nil || !strings.Contains(err.Error(), "ghost.md") {
		t.Fatalf("Info should report the missing file, got %v", err)
	}
	if _, _, err := g.Section(tp, "x"); err == nil {
		t.Fatal("Section should fail when the file is missing")
	}
	if got := g.Search("anything", 5); got != nil {
		t.Fatalf("Search must return nil when the manual cannot load, got %v", got)
	}
}

func TestParseDocHandlesCRLF(t *testing.T) {
	lf := parseDoc(alphaDoc)
	crlf := parseDoc(strings.ReplaceAll(alphaDoc, "\n", "\r\n"))
	if len(lf.headings) != len(crlf.headings) || len(lf.units) != len(crlf.units) {
		t.Fatalf("CRLF changes parsing: lf=%d/%d crlf=%d/%d", len(lf.headings), len(lf.units), len(crlf.headings), len(crlf.units))
	}
	if strings.Contains(crlf.body, "\r") {
		t.Fatal("body should be normalised to LF")
	}
}

func TestFuzzyTermsOnlySplitsLongCJK(t *testing.T) {
	if got := fuzzyTerms([]string{"abc", "install"}); got != nil {
		t.Fatalf("nothing to split, want nil, got %v", got)
	}
	if got := fuzzyTerms([]string{"短词"}); got != nil {
		t.Fatalf("short CJK words stay intact, got %v", got)
	}
	got := fuzzyTerms([]string{"能力网址", "token"})
	want := []string{"能力", "力网", "网址", "token"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("fuzzyTerms = %v, want %v", got, want)
	}
}
