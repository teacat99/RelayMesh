package confirm

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/teacat99/RelayMesh/v2/internal/docs"
	"github.com/teacat99/RelayMesh/v2/internal/model"
)

func TestParseRef(t *testing.T) {
	ok := []struct {
		in  string
		seq int
		id  uint
	}{
		{"Q12", 12, 0}, {"q7", 7, 0}, {"12", 12, 0}, {" 3 ", 3, 0},
		{"999999", 999999, 0}, {"1000000", 0, 1000000},
	}
	for _, c := range ok {
		seq, id, good := ParseRef(c.in)
		if !good || seq != c.seq || id != c.id {
			t.Errorf("ParseRef(%q) = %d,%d,%v，期望 %d,%d,true", c.in, seq, id, good, c.seq, c.id)
		}
	}
	for _, bad := range []string{"", "  ", "Q", "Q0", "Q-1", "Qx", "abc", "0", "-5", "1.5", "Q 1"} {
		if _, _, good := ParseRef(bad); good {
			t.Errorf("ParseRef(%q) 不应被接受", bad)
		}
	}
}

func opts(ids ...string) []model.ConfirmOption {
	out := make([]model.ConfirmOption, len(ids))
	for i, id := range ids {
		out[i] = model.ConfirmOption{ID: id, Label: "选项 " + id}
	}
	return out
}

func TestOptionsFingerprint(t *testing.T) {
	base := opts("a", "b", "c")
	fp := OptionsFingerprint(base)
	if fp == "" || fp != OptionsFingerprint(base) {
		t.Fatal("同样的选项应得到稳定的非空指纹")
	}
	if OptionsFingerprint([]model.ConfirmOption{base[2], base[0], base[1]}) != fp {
		t.Error("只调换顺序不应改变指纹")
	}
	changes := map[string][]model.ConfirmOption{
		"删一个":  base[:2],
		"加一个":  append(append([]model.ConfirmOption{}, base...), model.ConfirmOption{ID: "d", Label: "选项 d"}),
		"改文案":  {{ID: "a", Label: "改过"}, base[1], base[2]},
		"改说明":  {{ID: "a", Label: "选项 a", Detail: "多了说明"}, base[1], base[2]},
		"改推荐":  {{ID: "a", Label: "选项 a", Recommended: true}, base[1], base[2]},
		"换 id": {{ID: "z", Label: "选项 a"}, base[1], base[2]},
	}
	for name, o := range changes {
		if OptionsFingerprint(o) == fp {
			t.Errorf("%s 应改变指纹", name)
		}
	}
}

func TestFromDecisionCard(t *testing.T) {
	card := docs.DecisionCard{
		ID: "d1", Title: "选哪个", Multi: true, Note: true, Risk: "l2",
		Options: []docs.DecisionOption{
			{ID: "a", Label: "甲", Detail: "详情", Recommended: false},
			{ID: "b", Label: "乙", Recommended: true},
		},
	}
	o, def, multi, note, risk := FromDecisionCard(card)
	if len(o) != 2 || o[0].Detail != "详情" || !o[1].Recommended || !multi || !note || risk != model.ConfirmRiskL2 {
		t.Fatalf("转换不对: %+v %v %v %v", o, multi, note, risk)
	}
	if def != "b" {
		t.Errorf("没写 default 时应取推荐项兜底: %q", def)
	}
	card.Default, card.Risk = "a", ""
	_, def, _, _, risk = FromDecisionCard(card)
	if def != "a" || risk != model.ConfirmRiskL1 {
		t.Errorf("写了 default 就用它；没写风险默认 L1: %q %q", def, risk)
	}
	card.Risk = "乱写"
	if _, _, _, _, risk = FromDecisionCard(card); risk != model.ConfirmRiskL1 {
		t.Errorf("不认识的风险等级应规整为 L1: %q", risk)
	}
}

func TestRecommendedOptionIDs(t *testing.T) {
	o := []model.ConfirmOption{{ID: "a"}, {ID: "b", Recommended: true}, {ID: "c", Recommended: true}}
	if got := RecommendedOptionIDs(o, false); len(got) != 1 || got[0] != "b" {
		t.Errorf("单选只取第一个推荐项: %v", got)
	}
	if got := RecommendedOptionIDs(o, true); len(got) != 2 || got[0] != "b" || got[1] != "c" {
		t.Errorf("多选取全部推荐项: %v", got)
	}
	if got := RecommendedOptionIDs(opts("a", "b"), true); got != nil {
		t.Errorf("没有推荐项应返回 nil: %v", got)
	}
}

func itemWithOptions(seq int, title string, o []model.ConfirmOption) *model.ConfirmItem {
	raw, _ := json.Marshal(o)
	return &model.ConfirmItem{Seq: seq, Title: title, OptionsJSON: string(raw)}
}

func TestFormatAnswerAndDismissLines(t *testing.T) {
	it := itemWithOptions(12, "要不要上线", []model.ConfirmOption{{ID: "a", Label: "上线"}, {ID: "b", Label: "再等等"}})
	line := FormatAnswerLine(it, []string{"a"}, "", "")
	if line != "Q12「要不要上线」已答复：选 a（上线）" {
		t.Errorf("单选文案: %q", line)
	}
	line = FormatAnswerLine(it, []string{"a", "b"}, "  先灰度  ", "")
	if !strings.Contains(line, "选 a（上线）、b（再等等）") || !strings.HasSuffix(line, "；备注：先灰度") {
		t.Errorf("多选带备注: %q", line)
	}
	if got := FormatAnswerLine(it, []string{"zz"}, "", ""); !strings.HasSuffix(got, "选 zz") {
		t.Errorf("选项文案找不到时回退为 id: %q", got)
	}
	long := itemWithOptions(1, strings.Repeat("长", 100), opts("a", "b"))
	if got := FormatAnswerLine(long, []string{"a"}, "", ""); !strings.Contains(got, strings.Repeat("长", 60)+"…") || strings.Contains(got, strings.Repeat("长", 61)) {
		t.Errorf("标题应截断到 60 字: %q", got)
	}
	multiline := itemWithOptions(2, "第一行\n第二行", opts("a", "b"))
	if got := FormatAnswerLine(multiline, []string{"a"}, "", ""); strings.Contains(got, "\n") {
		t.Errorf("标题里的换行要折成空格，答复必须是单行: %q", got)
	}
	if got := FormatDismissLine(it, ""); got != "Q12「要不要上线」已被用户忽略（不需要你再等它的答复）" {
		t.Errorf("忽略文案: %q", got)
	}
	if got := FormatDismissLine(it, " 需求取消 "); !strings.HasSuffix(got, "：需求取消") {
		t.Errorf("忽略带原因: %q", got)
	}
}

// 自定义答复（#183）：只写自定义时没有「选 …」，自定义永远是最后一段且原样保留（可多行）；
// 计划确认上的自定义答复要在文案里写明「不等于批准」；老调用（没有自定义）的文案一个字都不变。
func TestFormatAnswerLineCustom(t *testing.T) {
	it := itemWithOptions(12, "要不要上线", []model.ConfirmOption{{ID: "a", Label: "上线"}, {ID: "b", Label: "再等等"}})
	if got, want := FormatAnswerLine(it, nil, "", "  先做灰度，\n再看数据  "), "Q12「要不要上线」已答复：没有选预设选项；自定义答复：先做灰度，\n再看数据"; got != want {
		t.Errorf("只写自定义:\n got %q\nwant %q", got, want)
	}
	if got, want := FormatAnswerLine(it, []string{"a", "b"}, "两个都要", "另外加一个开关"), "Q12「要不要上线」已答复：选 a（上线）、b（再等等）；备注：两个都要；自定义答复：另外加一个开关"; got != want {
		t.Errorf("选项 + 备注 + 自定义:\n got %q\nwant %q", got, want)
	}
	if got := FormatAnswerLine(it, []string{"a"}, "", "   "); got != "Q12「要不要上线」已答复：选 a（上线）" {
		t.Errorf("全是空白的自定义答复等于没写: %q", got)
	}
	plan := itemWithOptions(3, "批准计划", opts("a", "b"))
	plan.Kind = model.ConfirmKindPlan
	got := FormatAnswerLine(plan, nil, "", "把第二步拆开")
	if !strings.Contains(got, "自定义答复不等于批准") || !strings.HasSuffix(got, "；自定义答复：把第二步拆开") {
		t.Errorf("计划确认的自定义答复要写明不等于批准，且自定义在最后: %q", got)
	}
	if got := FormatAnswerLine(plan, []string{"a"}, "", ""); strings.Contains(got, "不等于批准") {
		t.Errorf("没有自定义答复时不加这句提醒: %q", got)
	}
}

func TestExportHash(t *testing.T) {
	if got := ExportHash("abc", nil); got != "abc" {
		t.Errorf("没有决策行时应原样返回文档哈希: %q", got)
	}
	rows := []docs.DecisionExportRow{{ID: "d1", Status: "pending", Ref: "Q1", Result: "待作答"}}
	h1 := ExportHash("abc", rows)
	if !strings.HasPrefix(h1, "abc+") || h1 != ExportHash("abc", rows) {
		t.Errorf("有决策行时应在文档哈希后拼接稳定的摘要: %q", h1)
	}
	rows[0].Status, rows[0].Result = "answered", "a（甲）"
	if h2 := ExportHash("abc", rows); h2 == h1 || !strings.HasPrefix(h2, "abc+") {
		t.Errorf("决策被作答后哈希必须变化，否则差量导出会漏掉它: %q %q", h1, h2)
	}
	if ExportHash("abd", rows) == ExportHash("abc", rows) {
		t.Error("文档正文变了哈希也要变")
	}
}

func parsed(id string, errs ...string) docs.ParsedDecision {
	return docs.ParsedDecision{
		Card: docs.DecisionCard{ID: id, Title: "决策 " + id, Options: []docs.DecisionOption{
			{ID: "a", Label: "甲"}, {ID: "b", Label: "乙"},
		}},
		StartLine: 3, EndLine: 9, Errors: errs,
	}
}

func answered(seq int, status string, optionIDs []string, note string) *model.ConfirmItem {
	raw, _ := json.Marshal(model.ConfirmAnswer{OptionIDs: optionIDs, Note: note})
	now := time.Now()
	return &model.ConfirmItem{Seq: seq, Status: status, AnswerJSON: string(raw), AnsweredAt: &now}
}

func TestEnrichDecisionsStatuses(t *testing.T) {
	byKey := map[string]*model.ConfirmItem{
		"pend":  {Seq: 1, Status: model.ConfirmStatusPending},
		"done":  answered(2, model.ConfirmStatusAnswered, []string{"b"}, "先稳"),
		"stale": answered(3, model.ConfirmStatusExpired, []string{"a"}, ""),
		"gone":  {Seq: 4, Status: model.ConfirmStatusWithdrawn},
		"skip":  {Seq: 5, Status: model.ConfirmStatusDismissed},
	}
	in := []docs.ParsedDecision{
		parsed("pend"), parsed("done"), parsed("stale"), parsed("gone"), parsed("skip"),
		parsed("nobody"), parsed("bad", "缺少选项"),
	}
	got := EnrichDecisions(in, byKey)
	if len(got) != len(in) {
		t.Fatalf("数量应一致: %d", len(got))
	}
	want := []struct{ status, ref string }{
		{"pending", "Q1"}, {"answered", "Q2"}, {"expired", "Q3"}, {"withdrawn", "Q4"}, {"dismissed", "Q5"},
		{"pending", ""}, {"invalid", ""},
	}
	for i, w := range want {
		if got[i].Status != w.status || got[i].ConfirmRef != w.ref {
			t.Errorf("第 %d 张: 期望 %s/%s，实际 %s/%s", i, w.status, w.ref, got[i].Status, got[i].ConfirmRef)
		}
	}
	if a := got[1].Answer; a == nil || len(a.OptionIDs) != 1 || a.OptionIDs[0] != "b" || a.Note != "先稳" || got[1].AnsweredAt == nil {
		t.Errorf("已作答应带出答案: %+v", got[1])
	}
	if got[2].Answer == nil {
		t.Error("已过期的也保留旧答案供查看")
	}
	if got[0].Answer != nil || got[3].Answer != nil {
		t.Error("待作答 / 已撤回没有答案")
	}
	if !strings.Contains(got[5].StatusNote, "确认项尚未生成") {
		t.Errorf("没有对应确认项应提示保存一次文档: %q", got[5].StatusNote)
	}
	if len(got[6].Errors) != 1 || got[6].ConfirmRef != "" {
		t.Errorf("围栏校验没过的不挂确认项: %+v", got[6])
	}
}

func TestExportDecisionRows(t *testing.T) {
	byKey := map[string]*model.ConfirmItem{
		"pend": {Seq: 1, Status: model.ConfirmStatusPending},
		"one":  answered(2, model.ConfirmStatusAnswered, []string{"b"}, ""),
		"two":  answered(3, model.ConfirmStatusAnswered, []string{"a", "b"}, "都要"),
		"skip": {Seq: 4, Status: model.ConfirmStatusDismissed},
		"gone": {Seq: 5, Status: model.ConfirmStatusWithdrawn},
	}
	rows := ExportDecisionRowsFromItems([]docs.ParsedDecision{
		parsed("pend"), parsed("one"), parsed("two"), parsed("skip"), parsed("gone"), parsed("bad", "错"),
	}, byKey)
	if len(rows) != 5 {
		t.Fatalf("围栏校验没过的不进表，其余都要: %d %+v", len(rows), rows)
	}
	wantResult := []string{"待作答", "b（乙）", "a（甲）、b（乙）；备注：都要", "已忽略", ""}
	for i, w := range wantResult {
		if rows[i].Result != w {
			t.Errorf("第 %d 行结果: 期望 %q，实际 %q", i, w, rows[i].Result)
		}
	}
	if rows[1].Ref != "Q2" || rows[1].Status != "answered" || rows[1].Title != "决策 one" {
		t.Errorf("行字段: %+v", rows[1])
	}
}

// 自定义答复（#183）也要进导出表的「结果」一格：只写自定义的没有选项部分，选项 + 备注 + 自定义并存时自定义在最后。
func TestExportDecisionRowsCustomAnswer(t *testing.T) {
	custom := func(seq int, optionIDs []string, note, text string) *model.ConfirmItem {
		raw, _ := json.Marshal(model.ConfirmAnswer{OptionIDs: optionIDs, Note: note, Custom: text})
		now := time.Now()
		return &model.ConfirmItem{Seq: seq, Status: model.ConfirmStatusAnswered, AnswerJSON: string(raw), AnsweredAt: &now}
	}
	byKey := map[string]*model.ConfirmItem{
		"only": custom(1, []string{}, "", "先备份，再迁移"),
		"both": custom(2, []string{"a"}, "稳一点", "另外通知运维"),
	}
	rows := ExportDecisionRowsFromItems([]docs.ParsedDecision{parsed("only"), parsed("both")}, byKey)
	want := []string{"没有选预设选项；自定义答复：先备份，再迁移", "a（甲）；备注：稳一点；自定义答复：另外通知运维"}
	if len(rows) != len(want) {
		t.Fatalf("行数: %d %+v", len(rows), rows)
	}
	for i, w := range want {
		if rows[i].Result != w {
			t.Errorf("第 %d 行结果: 期望 %q，实际 %q", i, w, rows[i].Result)
		}
	}
}
