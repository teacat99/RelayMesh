package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/teacat99/RelayMesh/v2/internal/confirm"
	"github.com/teacat99/RelayMesh/v2/internal/docs"
	"github.com/teacat99/RelayMesh/v2/internal/model"
)

func exportHashOf(doc *model.ProjectDoc, rows []docs.DecisionExportRow) string {
	return confirm.ExportHash(doc.Hash, rows)
}

func newConfirmStore(t *testing.T) *Store {
	t.Helper()
	st, err := New(":memory:")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func appKind(err error) ErrorKind {
	var ae *AppError
	if errors.As(err, &ae) {
		return ae.Kind
	}
	return ""
}

func wantKind(t *testing.T, err error, kind ErrorKind, what string) {
	t.Helper()
	if err == nil {
		t.Fatalf("%s：期望 %s 错误，却成功了", what, kind)
	}
	if got := appKind(err); got != kind {
		t.Fatalf("%s：期望 %s，得到 %q（%v）", what, kind, got, err)
	}
}

func abOptions() []model.ConfirmOption {
	return []model.ConfirmOption{
		{ID: "a", Label: "方案 A", Recommended: true},
		{ID: "b", Label: "方案 B"},
	}
}

func newAsk(t *testing.T, st *Store, wf, title string) *model.ConfirmItem {
	t.Helper()
	item, created, err := st.CreateConfirmItem(context.Background(), ConfirmCreateInput{
		WorkflowID: wf, Title: title, Options: abOptions(), Risk: "L1",
	})
	if err != nil || !created {
		t.Fatalf("创建确认项 %q 失败: created=%v err=%v", title, created, err)
	}
	return item
}

// answerMessages 返回工作流收件箱里所有「确认项答复」消息（按创建顺序）。
func answerMessages(t *testing.T, st *Store, wf string) []model.QueuedFeedback {
	t.Helper()
	var rows []model.QueuedFeedback
	if err := st.DB().Where("workflow_id = ? AND kind = ?", wf, model.InboxKindAnswer).Order("id ASC").Find(&rows).Error; err != nil {
		t.Fatalf("读取答复消息: %v", err)
	}
	return rows
}

func TestConfirm_CreateValidation(t *testing.T) {
	st := newConfirmStore(t)
	ctx := context.Background()
	nine := make([]model.ConfirmOption, 9)
	for i := range nine {
		nine[i] = model.ConfirmOption{ID: fmt.Sprintf("o%d", i), Label: "x"}
	}
	cases := []struct {
		name string
		in   ConfirmCreateInput
	}{
		{"没有标题", ConfirmCreateInput{WorkflowID: "wf", Title: "  ", Options: abOptions()}},
		{"没有工作流", ConfirmCreateInput{Title: "t", Options: abOptions()}},
		{"只有一个选项", ConfirmCreateInput{WorkflowID: "wf", Title: "t", Options: abOptions()[:1]}},
		{"九个选项", ConfirmCreateInput{WorkflowID: "wf", Title: "t", Options: nine}},
		{"选项 id 重复", ConfirmCreateInput{WorkflowID: "wf", Title: "t", Options: []model.ConfirmOption{{ID: "a", Label: "x"}, {ID: "a", Label: "y"}}}},
		{"选项缺文案", ConfirmCreateInput{WorkflowID: "wf", Title: "t", Options: []model.ConfirmOption{{ID: "a", Label: "x"}, {ID: "b"}}}},
		{"默认项不在选项里", ConfirmCreateInput{WorkflowID: "wf", Title: "t", Options: abOptions(), DefaultOption: "zzz"}},
		{"未知种类", ConfirmCreateInput{WorkflowID: "wf", Title: "t", Options: abOptions(), Kind: "weird"}},
		{"计划确认没有任务", ConfirmCreateInput{WorkflowID: "wf", Title: "t", Options: abOptions(), Kind: model.ConfirmKindPlan}},
		{"标题过长", ConfirmCreateInput{WorkflowID: "wf", Title: strings.Repeat("题", model.ConfirmTitleMax+1), Options: abOptions()}},
	}
	for _, c := range cases {
		_, _, err := st.CreateConfirmItem(ctx, c.in)
		wantKind(t, err, ErrInvalidInput, c.name)
	}
	// 计划确认绑定的任务必须真实存在
	_, _, err := st.CreateConfirmItem(ctx, ConfirmCreateInput{WorkflowID: "wf", Title: "t", Options: abOptions(), Kind: model.ConfirmKindPlan, TaskSeq: 7})
	wantKind(t, err, ErrNotFound, "计划确认绑定不存在的任务")
	task, err := st.CreateWorkItem(ctx, WorkItemInput{WorkflowID: "wf", Title: "真任务"}, WorkActor{Kind: model.WorkActorAgent})
	if err != nil {
		t.Fatalf("建任务: %v", err)
	}
	item, created, err := st.CreateConfirmItem(ctx, ConfirmCreateInput{
		WorkflowID: "wf", Title: "计划", Options: abOptions(), Kind: model.ConfirmKindPlan, TaskSeq: task.Item.Seq,
	})
	if err != nil || !created || item.TaskSeq != task.Item.Seq || item.Kind != model.ConfirmKindPlan {
		t.Fatalf("计划确认应能创建: %+v %v %v", item, created, err)
	}
}

func TestConfirm_SeqNeverReusedAndRefLookup(t *testing.T) {
	st := newConfirmStore(t)
	ctx := context.Background()
	a := newAsk(t, st, "wf", "第一问")
	b := newAsk(t, st, "wf", "第二问")
	if a.Seq != 1 || b.Seq != 2 {
		t.Fatalf("序号应从 1 递增: %d %d", a.Seq, b.Seq)
	}
	if err := st.DB().Delete(&model.ConfirmItem{}, b.ID).Error; err != nil {
		t.Fatal(err)
	}
	c := newAsk(t, st, "wf", "第三问")
	if c.Seq != 3 {
		t.Fatalf("删除后序号不能复用，应是 3，得到 %d", c.Seq)
	}
	// 另一个工作流各自从 1 开始
	if o := newAsk(t, st, "wf-other", "别处的问题"); o.Seq != 1 {
		t.Fatalf("不同工作流的序号相互独立: %d", o.Seq)
	}
	// 引用写法：Q1 / q1 / 1 / 数据库 id
	for _, ref := range []string{"Q1", "q1", "1", fmt.Sprint(a.ID + 1000000)} {
		got, err := st.GetConfirmItemByRef(ctx, "wf", ref)
		if ref == fmt.Sprint(a.ID+1000000) {
			wantKind(t, err, ErrNotFound, "不存在的数据库 id")
			continue
		}
		if err != nil || got.ID != a.ID {
			t.Fatalf("引用 %q 应取到 Q1: %+v %v", ref, got, err)
		}
	}
	// 数据库 id 引用（大于序号阈值才会被当 id；这里 id 很小，所以走序号——验证非法引用）
	_, err := st.GetConfirmItemByRef(ctx, "wf", "Q0")
	wantKind(t, err, ErrInvalidInput, "Q0")
	_, err = st.GetConfirmItemByRef(ctx, "wf", "abc")
	wantKind(t, err, ErrInvalidInput, "abc")
	// 不能跨工作流读取
	_, err = st.GetConfirmItemByRef(ctx, "wf-other", "Q3")
	wantKind(t, err, ErrNotFound, "跨工作流")
}

func TestConfirm_DedupeAndRateLimit(t *testing.T) {
	st := newConfirmStore(t)
	ctx := context.Background()
	first := newAsk(t, st, "wf", "要不要重构")
	again, created, err := st.CreateConfirmItem(ctx, ConfirmCreateInput{WorkflowID: "wf", Title: "要不要重构", Options: abOptions(), Risk: "L1"})
	if err != nil || created || again.ID != first.ID {
		t.Fatalf("同一问题仍待处理时应返回已有那条: created=%v id=%d/%d err=%v", created, again.ID, first.ID, err)
	}
	// 选项不同就是另一个问题
	diff, created, err := st.CreateConfirmItem(ctx, ConfirmCreateInput{WorkflowID: "wf", Title: "要不要重构", Options: []model.ConfirmOption{{ID: "x", Label: "做"}, {ID: "y", Label: "不做"}}})
	if err != nil || !created || diff.ID == first.ID {
		t.Fatalf("选项不同应新建: %+v %v", diff, err)
	}
	// 作答之后同一问题可以再问
	if _, err := st.AnswerConfirmItem(ctx, "wf", "Q1", ConfirmAnswerInput{OptionIDs: []string{"a"}}); err != nil {
		t.Fatal(err)
	}
	re, created, err := st.CreateConfirmItem(ctx, ConfirmCreateInput{WorkflowID: "wf", Title: "要不要重构", Options: abOptions(), Risk: "L1"})
	if err != nil || !created || re.ID == first.ID {
		t.Fatalf("已答复后同一问题可以重新提: %+v %v", re, err)
	}

	// 速率限制：一小时内智能体最多新建 30 条（已有 3 条）
	for i := 0; i < model.ConfirmCreateRateHour-3; i++ {
		newAsk(t, st, "wf", fmt.Sprintf("独立问题 %d", i))
	}
	_, _, err = st.CreateConfirmItem(ctx, ConfirmCreateInput{WorkflowID: "wf", Title: "第 31 条", Options: abOptions()})
	wantKind(t, err, ErrConflict, "超过每小时上限")
	// 别的工作流不受影响；文档决策项（created_by=doc）不计入
	newAsk(t, st, "wf-other", "别处")
}

func TestConfirm_AnswerSingleWritesInboxMessageAtomically(t *testing.T) {
	st := newConfirmStore(t)
	ctx := context.Background()
	item, _, err := st.CreateConfirmItem(ctx, ConfirmCreateInput{
		WorkflowID: "wf", Title: "选方案", Options: abOptions(), AllowNote: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := st.AnswerConfirmItem(ctx, "wf", "Q1", ConfirmAnswerInput{OptionIDs: []string{"b"}, Note: "  先小范围试  "})
	if err != nil {
		t.Fatalf("作答失败: %v", err)
	}
	if got.Status != model.ConfirmStatusAnswered || got.AnsweredAt == nil {
		t.Fatalf("应为 answered: %+v", got)
	}
	view := model.NewConfirmItemView(got)
	if view.Answer == nil || len(view.Answer.OptionIDs) != 1 || view.Answer.OptionIDs[0] != "b" || view.Answer.Note != "先小范围试" || view.Answer.By != "human" {
		t.Fatalf("答复内容不对: %+v", view.Answer)
	}
	msgs := answerMessages(t, st, "wf")
	if len(msgs) != 1 {
		t.Fatalf("应投递一条答复消息，得到 %d", len(msgs))
	}
	for _, want := range []string{"Q1", "选方案", "b（方案 B）", "先小范围试"} {
		if !strings.Contains(msgs[0].ResponseText, want) {
			t.Fatalf("答复文案应含 %q: %s", want, msgs[0].ResponseText)
		}
	}
	// 重复作答 → 409，不再多一条消息，答复保持第一次的
	_, err = st.AnswerConfirmItem(ctx, "wf", "Q1", ConfirmAnswerInput{OptionIDs: []string{"a"}})
	wantKind(t, err, ErrConflict, "重复作答")
	if n := len(answerMessages(t, st, "wf")); n != 1 {
		t.Fatalf("重复作答不能多投递消息，得到 %d", n)
	}
	re, _ := st.GetConfirmItemByRef(ctx, "wf", "Q1")
	if v := model.NewConfirmItemView(re); v.Answer == nil || v.Answer.OptionIDs[0] != "b" {
		t.Fatalf("答复被覆盖了: %+v", v.Answer)
	}
	_ = item
}

func TestConfirm_AnswerValidation(t *testing.T) {
	st := newConfirmStore(t)
	ctx := context.Background()
	newAsk(t, st, "wf", "单选无备注") // Q1
	multi, _, err := st.CreateConfirmItem(ctx, ConfirmCreateInput{
		WorkflowID: "wf", Title: "多选", Multi: true, AllowNote: true,
		Options: []model.ConfirmOption{{ID: "a", Label: "A", Recommended: true}, {ID: "b", Label: "B", Recommended: true}, {ID: "c", Label: "C"}},
	}) // Q2
	if err != nil {
		t.Fatal(err)
	}
	bad := []struct {
		ref  string
		in   ConfirmAnswerInput
		what string
	}{
		{"Q1", ConfirmAnswerInput{}, "没选"},
		{"Q1", ConfirmAnswerInput{OptionIDs: []string{"a", "b"}}, "单选选了两个"},
		{"Q1", ConfirmAnswerInput{OptionIDs: []string{"zzz"}}, "未知选项"},
		{"Q1", ConfirmAnswerInput{OptionIDs: []string{"a"}, Note: "不允许备注"}, "不接受备注"},
		{"Q2", ConfirmAnswerInput{OptionIDs: []string{"a", "a"}}, "重复选项"},
		{"Q2", ConfirmAnswerInput{OptionIDs: []string{"a"}, Note: strings.Repeat("备", model.ConfirmNoteMax+1)}, "备注过长"},
	}
	for _, c := range bad {
		_, err := st.AnswerConfirmItem(ctx, "wf", c.ref, c.in)
		wantKind(t, err, ErrInvalidInput, c.what)
	}
	if n := len(answerMessages(t, st, "wf")); n != 0 {
		t.Fatalf("校验失败不应投递任何消息，得到 %d", n)
	}
	for _, ref := range []string{"Q1", "Q2"} {
		it, _ := st.GetConfirmItemByRef(ctx, "wf", ref)
		if it.Status != model.ConfirmStatusPending {
			t.Fatalf("%s 校验失败后应仍是待处理: %s", ref, it.Status)
		}
	}
	// 多选的合法作答
	got, err := st.AnswerConfirmItem(ctx, "wf", "Q2", ConfirmAnswerInput{OptionIDs: []string{"a", "c"}, Note: "A 优先"})
	if err != nil || got.Status != model.ConfirmStatusAnswered {
		t.Fatalf("多选作答: %+v %v", got, err)
	}
	_ = multi
	// 不能跨工作流作答
	_, err = st.AnswerConfirmItem(ctx, "wf-other", "Q1", ConfirmAnswerInput{OptionIDs: []string{"a"}})
	wantKind(t, err, ErrNotFound, "跨工作流作答")
}

// 自定义答复（#183）：预设选项都不合适时的出路，所有确认项都接受（不需要 allow_note）；
// 单选时与选项二选一，多选时可与选项并存；只写自定义、不选任何选项是合法的；答复与收件箱消息同事务。
func TestConfirm_AnswerCustom(t *testing.T) {
	st := newConfirmStore(t)
	ctx := context.Background()
	newAsk(t, st, "wf", "单选无备注") // Q1：不接受备注，但接受自定义答复
	if _, _, err := st.CreateConfirmItem(ctx, ConfirmCreateInput{
		WorkflowID: "wf", Title: "多选", Multi: true, AllowNote: true,
		Options: []model.ConfirmOption{{ID: "a", Label: "A"}, {ID: "b", Label: "B"}, {ID: "c", Label: "C"}},
	}); err != nil { // Q2
		t.Fatal(err)
	}

	bad := []struct {
		ref  string
		in   ConfirmAnswerInput
		what string
	}{
		{"Q1", ConfirmAnswerInput{Custom: "   "}, "全是空白的自定义答复等于没写"},
		{"Q1", ConfirmAnswerInput{OptionIDs: []string{"a"}, Custom: "都不对"}, "单选不能既选选项又写自定义"},
		{"Q1", ConfirmAnswerInput{Custom: strings.Repeat("长", model.ConfirmCustomMax+1)}, "自定义答复过长"},
		{"Q1", ConfirmAnswerInput{Custom: "改成 C 方案", Note: "单选项不接受备注"}, "不接受备注的项仍不能带备注"},
		{"Q2", ConfirmAnswerInput{OptionIDs: []string{"zzz"}, Custom: "x"}, "多选里的未知选项照样拒绝"},
	}
	for _, c := range bad {
		_, err := st.AnswerConfirmItem(ctx, "wf", c.ref, c.in)
		wantKind(t, err, ErrInvalidInput, c.what)
	}
	if n := len(answerMessages(t, st, "wf")); n != 0 {
		t.Fatalf("校验失败不应投递任何消息，得到 %d", n)
	}

	// 单选：只写自定义（首尾空白被规整，换行保留）
	got, err := st.AnswerConfirmItem(ctx, "wf", "Q1", ConfirmAnswerInput{Custom: "  先做灰度，\n再看数据  "})
	if err != nil || got.Status != model.ConfirmStatusAnswered {
		t.Fatalf("单选只写自定义应当合法: %+v %v", got, err)
	}
	view := model.NewConfirmItemView(got)
	if view.Answer == nil || view.Answer.OptionIDs == nil || len(view.Answer.OptionIDs) != 0 || view.Answer.Custom != "先做灰度，\n再看数据" || view.Answer.By != "human" {
		t.Fatalf("答复内容不对（选项应是空数组而不是 null）: %+v", view.Answer)
	}
	if !strings.Contains(got.AnswerJSON, `"options":[]`) {
		t.Fatalf("库里的答复 JSON 里选项应序列化成 []: %s", got.AnswerJSON)
	}

	// 多选：选项 + 备注 + 自定义并存
	got2, err := st.AnswerConfirmItem(ctx, "wf", "Q2", ConfirmAnswerInput{OptionIDs: []string{"a", "c"}, Note: "A 优先", Custom: "再加一个开关"})
	if err != nil {
		t.Fatalf("多选 + 备注 + 自定义: %v", err)
	}
	if v := model.NewConfirmItemView(got2); v.Answer == nil || len(v.Answer.OptionIDs) != 2 || v.Answer.Note != "A 优先" || v.Answer.Custom != "再加一个开关" {
		t.Fatalf("多选答复内容不对: %+v", v.Answer)
	}

	msgs := answerMessages(t, st, "wf")
	if len(msgs) != 2 {
		t.Fatalf("应投递 2 条答复消息，得到 %d", len(msgs))
	}
	for _, want := range []string{"Q1", "没有选预设选项", "自定义答复：先做灰度，\n再看数据"} {
		if !strings.Contains(msgs[0].ResponseText, want) {
			t.Fatalf("单选自定义的文案应含 %q: %q", want, msgs[0].ResponseText)
		}
	}
	for _, want := range []string{"Q2", "选 a（A）、c（C）", "备注：A 优先", "自定义答复：再加一个开关"} {
		if !strings.Contains(msgs[1].ResponseText, want) {
			t.Fatalf("多选自定义的文案应含 %q: %q", want, msgs[1].ResponseText)
		}
	}

	// 老答复（没有 custom 字段）读出来 Custom 为空，行为不变
	var legacy model.ConfirmAnswer
	if err := json.Unmarshal([]byte(`{"options":["a"],"note":"旧","by":"human"}`), &legacy); err != nil || legacy.Custom != "" || len(legacy.OptionIDs) != 1 {
		t.Fatalf("老答复应能照常解析: %+v %v", legacy, err)
	}
}

func TestConfirm_ConcurrentAnswersExactlyOneWins(t *testing.T) {
	st := newConfirmStore(t)
	ctx := context.Background()
	newAsk(t, st, "wf", "并发作答")
	var wg sync.WaitGroup
	var mu sync.Mutex
	ok, conflict := 0, 0
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			opt := "a"
			if i%2 == 1 {
				opt = "b"
			}
			_, err := st.AnswerConfirmItem(ctx, "wf", "Q1", ConfirmAnswerInput{OptionIDs: []string{opt}})
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				ok++
			case appKind(err) == ErrConflict:
				conflict++
			default:
				t.Errorf("意外错误: %v", err)
			}
		}(i)
	}
	wg.Wait()
	if ok != 1 || conflict != 11 {
		t.Fatalf("应恰有 1 个成功、11 个 409：ok=%d conflict=%d", ok, conflict)
	}
	if n := len(answerMessages(t, st, "wf")); n != 1 {
		t.Fatalf("并发作答只能投递一条消息，得到 %d", n)
	}
}

func TestConfirm_BatchIsAtomicAndAdoptsRecommended(t *testing.T) {
	st := newConfirmStore(t)
	ctx := context.Background()
	for i := 1; i <= 3; i++ {
		newAsk(t, st, "wf", fmt.Sprintf("问题 %d", i)) // Q1..Q3，推荐都是 a
	}
	// 一项非法 → 整批不落库
	_, err := st.AnswerConfirmBatch(ctx, "wf", []ConfirmBatchItem{
		{Ref: "Q1"}, {Ref: "Q2", Options: []string{"zzz"}}, {Ref: "Q9"},
	}, true)
	wantKind(t, err, ErrInvalidInput, "批量含非法项")
	if !strings.Contains(err.Error(), "Q2") || !strings.Contains(err.Error(), "Q9") {
		t.Fatalf("错误应一次列出全部问题: %v", err)
	}
	for _, ref := range []string{"Q1", "Q2", "Q3"} {
		if it, _ := st.GetConfirmItemByRef(ctx, "wf", ref); it.Status != model.ConfirmStatusPending {
			t.Fatalf("批量失败后 %s 应仍待处理: %s", ref, it.Status)
		}
	}
	if len(answerMessages(t, st, "wf")) != 0 {
		t.Fatal("批量失败不应投递消息")
	}

	// 采用推荐 + 个别项手选
	res, err := st.AnswerConfirmBatch(ctx, "wf", []ConfirmBatchItem{{Ref: "Q1"}, {Ref: "Q2", Options: []string{"b"}}, {Ref: "Q3"}}, true)
	if err != nil {
		t.Fatalf("批量作答失败: %v", err)
	}
	if len(res.Items) != 3 {
		t.Fatalf("应作答 3 条: %d", len(res.Items))
	}
	wantOpt := map[string]string{"Q1": "a", "Q2": "b", "Q3": "a"}
	for ref, opt := range wantOpt {
		it, _ := st.GetConfirmItemByRef(ctx, "wf", ref)
		v := model.NewConfirmItemView(it)
		if it.Status != model.ConfirmStatusAnswered || v.Answer == nil || v.Answer.OptionIDs[0] != opt {
			t.Fatalf("%s 应选 %s: %+v", ref, opt, v)
		}
	}
	msgs := answerMessages(t, st, "wf")
	if len(msgs) != 1 || strings.Count(msgs[0].ResponseText, "\n") != 2 {
		t.Fatalf("整批应只投递一条、含三行的消息: %d %+v", len(msgs), msgs)
	}
}

func TestConfirm_BatchRefusesL2DuplicatesAndMissingRecommendation(t *testing.T) {
	st := newConfirmStore(t)
	ctx := context.Background()
	l2, _, err := st.CreateConfirmItem(ctx, ConfirmCreateInput{WorkflowID: "wf", Title: "不可逆", Options: abOptions(), Risk: "L2"}) // Q1
	if err != nil {
		t.Fatal(err)
	}
	noRec, _, err := st.CreateConfirmItem(ctx, ConfirmCreateInput{WorkflowID: "wf", Title: "没推荐", Options: []model.ConfirmOption{{ID: "x", Label: "X"}, {ID: "y", Label: "Y"}}}) // Q2
	if err != nil {
		t.Fatal(err)
	}
	newAsk(t, st, "wf", "正常") // Q3
	cases := []struct {
		name  string
		items []ConfirmBatchItem
		adopt bool
		want  string
	}{
		{"L2 不进批量（即使显式选了）", []ConfirmBatchItem{{Ref: "Q1", Options: []string{"a"}}}, false, "L2"},
		{"L2 不能隐式采用推荐", []ConfirmBatchItem{{Ref: "Q1"}}, true, "L2"},
		{"没有推荐项", []ConfirmBatchItem{{Ref: "Q2"}}, true, "没有推荐"},
		{"同一批里重复", []ConfirmBatchItem{{Ref: "Q3"}, {Ref: "q3"}}, true, "重复"},
		{"没写选项也没要求采用推荐", []ConfirmBatchItem{{Ref: "Q3"}}, false, "至少一个选项"},
	}
	for _, c := range cases {
		_, err := st.AnswerConfirmBatch(ctx, "wf", c.items, c.adopt)
		wantKind(t, err, ErrInvalidInput, c.name)
		if !strings.Contains(err.Error(), c.want) {
			t.Fatalf("%s：错误应含 %q: %v", c.name, c.want, err)
		}
	}
	_, err = st.AnswerConfirmBatch(ctx, "wf", nil, true)
	wantKind(t, err, ErrInvalidInput, "空批")
	tooMany := make([]ConfirmBatchItem, model.ConfirmBatchMax+1)
	for i := range tooMany {
		tooMany[i] = ConfirmBatchItem{Ref: "Q3"}
	}
	_, err = st.AnswerConfirmBatch(ctx, "wf", tooMany, true)
	wantKind(t, err, ErrInvalidInput, "超过批量上限")
	for _, id := range []uint{l2.ID, noRec.ID} {
		var it model.ConfirmItem
		st.DB().First(&it, id)
		if it.Status != model.ConfirmStatusPending {
			t.Fatalf("拒绝的批量不应改动任何项: %+v", it)
		}
	}
	if len(answerMessages(t, st, "wf")) != 0 {
		t.Fatal("被拒绝的批量不应投递消息")
	}
}

func TestConfirm_DismissAndWithdraw(t *testing.T) {
	st := newConfirmStore(t)
	ctx := context.Background()
	newAsk(t, st, "wf", "要忽略的") // Q1
	newAsk(t, st, "wf", "要撤回的") // Q2
	it, err := st.DismissConfirmItem(ctx, "wf", "Q1", "不重要")
	if err != nil || it.Status != model.ConfirmStatusDismissed {
		t.Fatalf("忽略: %+v %v", it, err)
	}
	msgs := answerMessages(t, st, "wf")
	if len(msgs) != 1 || !strings.Contains(msgs[0].ResponseText, "Q1") || !strings.Contains(msgs[0].ResponseText, "忽略") || !strings.Contains(msgs[0].ResponseText, "不重要") {
		t.Fatalf("忽略应告诉智能体: %+v", msgs)
	}
	_, err = st.DismissConfirmItem(ctx, "wf", "Q1", "")
	wantKind(t, err, ErrConflict, "重复忽略")
	_, err = st.AnswerConfirmItem(ctx, "wf", "Q1", ConfirmAnswerInput{OptionIDs: []string{"a"}})
	wantKind(t, err, ErrConflict, "对已忽略的作答")

	w, err := st.WithdrawConfirmItem(ctx, "wf", "Q2", true)
	if err != nil || w.Status != model.ConfirmStatusWithdrawn {
		t.Fatalf("智能体撤回自己的项: %+v %v", w, err)
	}
	if len(answerMessages(t, st, "wf")) != 1 {
		t.Fatal("撤回是智能体自己的动作，不应投递消息")
	}
	_, err = st.WithdrawConfirmItem(ctx, "wf", "Q2", true)
	wantKind(t, err, ErrConflict, "重复撤回")

	// 文档决策项智能体不能撤回
	doc := newDecisionDoc(t, st, "wf", "design/d.md", fence("d1", "决策一", "a", "b"))
	_ = doc
	items, _ := st.ListConfirmItems(ctx, ConfirmFilter{WorkflowID: "wf", Kinds: []string{model.ConfirmKindDecision}})
	if len(items) != 1 {
		t.Fatalf("应同步出一条决策项: %d", len(items))
	}
	_, err = st.WithdrawConfirmItem(ctx, "wf", model.ConfirmRef(items[0].Seq), true)
	wantKind(t, err, ErrConflict, "智能体撤回文档决策项")
}

// ---------- 文档决策卡 ↔ 确认项 ----------

func fence(id, title string, optIDs ...string) string {
	var opts []string
	for i, o := range optIDs {
		rec := ""
		if i == 0 {
			rec = `,"recommended":true`
		}
		opts = append(opts, fmt.Sprintf(`{"id":%q,"label":"选项 %s"%s}`, o, o, rec))
	}
	return fmt.Sprintf("```decision\n{\"id\":%q,\"title\":%q,\"options\":[%s]}\n```\n", id, title, strings.Join(opts, ","))
}

func newDecisionDoc(t *testing.T, st *Store, wf, path, body string) *model.ProjectDoc {
	t.Helper()
	res, err := st.CreateProjectDoc(context.Background(), ProjectDocInput{WorkflowID: wf, Path: path, Title: path, Body: body}, DocActor{Kind: "human"})
	if err != nil {
		t.Fatalf("建文档: %v", err)
	}
	return res.Doc
}

func saveDocBody(t *testing.T, st *Store, doc *model.ProjectDoc, body string) {
	t.Helper()
	if _, err := st.UpdateProjectDoc(context.Background(), doc.ID, ProjectDocPatch{Body: &body}, DocActor{Kind: "human"}); err != nil {
		t.Fatalf("改文档: %v", err)
	}
}

func decisionItem(t *testing.T, st *Store, doc *model.ProjectDoc, key string) *model.ConfirmItem {
	t.Helper()
	m, err := st.DecisionConfirmsByKey(context.Background(), doc.ID)
	if err != nil {
		t.Fatal(err)
	}
	return m[key]
}

func countDecisionRows(t *testing.T, st *Store, docID uint) int64 {
	t.Helper()
	var n int64
	st.DB().Model(&model.ConfirmItem{}).Where("doc_id = ? AND kind = ?", docID, model.ConfirmKindDecision).Count(&n)
	return n
}

func TestConfirmSync_CreatesIdempotentlyAndUpdatesPendingInPlace(t *testing.T) {
	st := newConfirmStore(t)
	doc := newDecisionDoc(t, st, "wf", "design/a.md", "# 评估\n\n"+fence("d1", "选哪个", "a", "b")+"\n"+fence("d2", "再选一个", "x", "y"))
	if n := countDecisionRows(t, st, doc.ID); n != 2 {
		t.Fatalf("两张决策卡应同步出 2 条确认项: %d", n)
	}
	d1 := decisionItem(t, st, doc, "d1")
	d2 := decisionItem(t, st, doc, "d2")
	if d1.Seq != 1 || d2.Seq != 2 || d1.Status != model.ConfirmStatusPending || d1.CreatedBy != model.ConfirmCreatedByDoc || d1.Risk != model.ConfirmRiskL1 {
		t.Fatalf("初始同步不对: %+v %+v", d1, d2)
	}
	if d1.DefaultOption != "a" {
		t.Fatalf("没写 default 时应取推荐项兜底: %q", d1.DefaultOption)
	}

	// 内容没变的重复保存：不写库、不换序号（保存同样的正文 → Unchanged）
	saveDocBody(t, st, doc, doc.Body)
	changed, err := st.SyncDecisionConfirmItems(context.Background(), doc, doc.Body)
	if err != nil || changed {
		t.Fatalf("重复同步应是空操作: changed=%v err=%v", changed, err)
	}
	if n := countDecisionRows(t, st, doc.ID); n != 2 {
		t.Fatalf("重复保存不应多出确认项: %d", n)
	}

	// 只改标题：待处理项原地更新、序号不变
	body2 := "# 评估\n\n" + fence("d1", "改过的标题", "a", "b") + "\n" + fence("d2", "再选一个", "x", "y")
	saveDocBody(t, st, doc, body2)
	got := decisionItem(t, st, doc, "d1")
	if got.Seq != 1 || got.Title != "改过的标题" || got.Status != model.ConfirmStatusPending {
		t.Fatalf("改标题应原地更新: %+v", got)
	}
	// 待处理时改选项：同样原地更新，指纹跟着变
	body3 := "# 评估\n\n" + fence("d1", "改过的标题", "a", "b", "c") + "\n" + fence("d2", "再选一个", "x", "y")
	saveDocBody(t, st, doc, body3)
	got3 := decisionItem(t, st, doc, "d1")
	if got3.Seq != 1 || got3.OptionsFP == got.OptionsFP || len(model.NewConfirmItemView(got3).Options) != 3 {
		t.Fatalf("待处理时改选项应原地更新: %+v", got3)
	}
	if n := countDecisionRows(t, st, doc.ID); n != 2 {
		t.Fatalf("原地更新不应新增行: %d", n)
	}
}

func TestConfirmSync_AnsweredDismissedWithdrawnLifecycle(t *testing.T) {
	st := newConfirmStore(t)
	ctx := context.Background()
	doc := newDecisionDoc(t, st, "wf", "design/a.md", fence("d1", "决策一", "a", "b")+fence("d2", "决策二", "x", "y"))

	// d1 作答；d2 忽略
	if _, err := st.AnswerConfirmItem(ctx, "wf", "Q1", ConfirmAnswerInput{OptionIDs: []string{"b"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DismissConfirmItem(ctx, "wf", "Q2", ""); err != nil {
		t.Fatal(err)
	}
	// 同样的选项再次保存：答复保留、忽略保持，不会「复活」
	saveDocBody(t, st, doc, fence("d1", "决策一（改了措辞）", "a", "b")+fence("d2", "决策二（改了措辞）", "x", "y")+"\n补一段正文")
	if it := decisionItem(t, st, doc, "d1"); it.Seq != 1 || it.Status != model.ConfirmStatusAnswered {
		t.Fatalf("选项没变，答复应保留: %+v", it)
	}
	if it := decisionItem(t, st, doc, "d2"); it.Seq != 2 || it.Status != model.ConfirmStatusDismissed {
		t.Fatalf("选项没变，忽略应保持: %+v", it)
	}
	if n := countDecisionRows(t, st, doc.ID); n != 2 {
		t.Fatalf("没有新增行: %d", n)
	}

	// 选项变了：旧答复作废（expired），新建待处理；忽略的也会因选项变化重新出现
	saveDocBody(t, st, doc, fence("d1", "决策一", "a", "b", "c")+fence("d2", "决策二", "x", "y", "z"))
	var q1 model.ConfirmItem
	st.DB().Where("workflow_id = ? AND seq = 1", "wf").First(&q1)
	if q1.Status != model.ConfirmStatusExpired || q1.AnswerJSON == "" {
		t.Fatalf("旧答复应作废但保留记录: %+v", q1)
	}
	n1, n2 := decisionItem(t, st, doc, "d1"), decisionItem(t, st, doc, "d2")
	if n1.Seq != 3 || n1.Status != model.ConfirmStatusPending || n2.Seq != 4 || n2.Status != model.ConfirmStatusPending {
		t.Fatalf("选项变了应新建待处理: %+v %+v", n1, n2)
	}

	// 围栏被删：待处理的撤回；再加回来：新建一条
	saveDocBody(t, st, doc, fence("d2", "决策二", "x", "y", "z"))
	if it := decisionItem(t, st, doc, "d1"); it.Seq != 3 || it.Status != model.ConfirmStatusWithdrawn {
		t.Fatalf("围栏删除后待处理项应撤回: %+v", it)
	}
	saveDocBody(t, st, doc, fence("d1", "决策一", "a", "b", "c")+fence("d2", "决策二", "x", "y", "z"))
	if it := decisionItem(t, st, doc, "d1"); it.Seq != 5 || it.Status != model.ConfirmStatusPending {
		t.Fatalf("围栏加回来应新建: %+v", it)
	}

	// 作答后围栏被删：已答复的历史保留
	if _, err := st.AnswerConfirmItem(ctx, "wf", "Q5", ConfirmAnswerInput{OptionIDs: []string{"a"}}); err != nil {
		t.Fatal(err)
	}
	saveDocBody(t, st, doc, fence("d2", "决策二", "x", "y", "z"))
	if it := decisionItem(t, st, doc, "d1"); it.Status != model.ConfirmStatusAnswered {
		t.Fatalf("已答复的项不因围栏删除而改变: %+v", it)
	}
}

func TestConfirmSync_ArchiveAndDeleteWithdrawPending(t *testing.T) {
	st := newConfirmStore(t)
	ctx := context.Background()
	a := newDecisionDoc(t, st, "wf", "design/a.md", fence("d1", "决策", "a", "b"))
	b := newDecisionDoc(t, st, "wf", "design/b.md", fence("d1", "同名 id 但在另一篇", "a", "b"))
	if _, err := st.ArchiveProjectDoc(ctx, a.ID, DocActor{Kind: "human"}); err != nil {
		t.Fatal(err)
	}
	if it := decisionItem(t, st, a, "d1"); it.Status != model.ConfirmStatusWithdrawn {
		t.Fatalf("归档应撤回待处理项: %+v", it)
	}
	if it := decisionItem(t, st, b, "d1"); it.Status != model.ConfirmStatusPending {
		t.Fatalf("另一篇文档的同名 id 不受影响: %+v", it)
	}
	if err := st.SoftDeleteProjectDoc(ctx, b.ID); err != nil {
		t.Fatal(err)
	}
	if it := decisionItem(t, st, b, "d1"); it.Status != model.ConfirmStatusWithdrawn {
		t.Fatalf("删除应撤回待处理项: %+v", it)
	}
	// 归档的文档再被保存（仍是归档状态）：不会重新造出待处理项
	saveDocBody(t, st, a, a.Body+"\n追加")
	if it := decisionItem(t, st, a, "d1"); it.Status != model.ConfirmStatusWithdrawn {
		t.Fatalf("归档文档保存后不应复活: %+v", it)
	}
}

func TestConfirmSync_IgnoresInvalidAndDuplicateFences(t *testing.T) {
	st := newConfirmStore(t)
	broken := "```decision\n{这不是 json}\n```\n"
	oneOption := "```decision\n{\"id\":\"solo\",\"title\":\"只有一个选项\",\"options\":[{\"id\":\"a\",\"label\":\"A\"}]}\n```\n"
	doc := newDecisionDoc(t, st, "wf", "design/a.md", broken+oneOption+fence("dup", "第一张", "a", "b")+fence("dup", "重复 id 的第二张", "c", "d")+fence("ok", "正常", "a", "b"))
	if n := countDecisionRows(t, st, doc.ID); n != 2 {
		t.Fatalf("只有 dup（取第一张）和 ok 应入库，得到 %d 条", n)
	}
	if it := decisionItem(t, st, doc, "dup"); it == nil || it.Title != "第一张" {
		t.Fatalf("重复 id 应取第一张: %+v", it)
	}
	if it := decisionItem(t, st, doc, "solo"); it != nil {
		t.Fatalf("校验不过的围栏不应入库: %+v", it)
	}
	plain := newDecisionDoc(t, st, "wf", "design/plain.md", "# 没有决策\n")
	if n := countDecisionRows(t, st, plain.ID); n != 0 {
		t.Fatalf("没有围栏的文档不产生确认项: %d", n)
	}
}

func TestConfirmSync_BackfillRebuildsMissingItemsOnce(t *testing.T) {
	st := newConfirmStore(t)
	ctx := context.Background()
	doc := newDecisionDoc(t, st, "wf", "design/a.md", fence("d1", "升级前就存在的文档", "a", "b"))
	// 模拟升级前：文档在、确认项与计数器都没有
	st.DB().Where("1 = 1").Delete(&model.ConfirmItem{})
	st.DB().Where("1 = 1").Delete(&model.ConfirmItemCounter{})
	if n := countDecisionRows(t, st, doc.ID); n != 0 {
		t.Fatal("前置：应已清空")
	}
	st.BackfillDecisionConfirmItems(ctx)
	st.BackfillDecisionConfirmItems(ctx) // 幂等
	if n := countDecisionRows(t, st, doc.ID); n != 1 {
		t.Fatalf("回填应恰好补出 1 条: %d", n)
	}
	if it := decisionItem(t, st, doc, "d1"); it.Seq != 1 || it.Status != model.ConfirmStatusPending {
		t.Fatalf("回填项不对: %+v", it)
	}
}

func TestConfirmDocViews_StatusAnswerAndExportHash(t *testing.T) {
	st := newConfirmStore(t)
	ctx := context.Background()
	doc := newDecisionDoc(t, st, "wf", "design/a.md", "# 评估\n\n"+fence("d1", "选哪个", "a", "b"))
	views := st.DocDecisionViews(ctx, doc, doc.Body, false)
	if len(views) != 1 || views[0].Status != "pending" || views[0].ConfirmRef != "Q1" {
		t.Fatalf("初始视图: %+v", views)
	}
	rows := st.DocDecisionExportRows(ctx, doc)
	if len(rows) != 1 || rows[0].Status != "pending" || rows[0].Result != "待作答" {
		t.Fatalf("初始导出行: %+v", rows)
	}
	h0 := exportHashOf(doc, rows)

	if _, err := st.AnswerConfirmItem(ctx, "wf", "Q1", ConfirmAnswerInput{OptionIDs: []string{"b"}}); err != nil {
		t.Fatal(err)
	}
	views = st.DocDecisionViews(ctx, doc, doc.Body, false)
	if views[0].Status != "answered" || views[0].Answer == nil || views[0].Answer.OptionIDs[0] != "b" {
		t.Fatalf("作答后视图: %+v", views[0])
	}
	rows = st.DocDecisionExportRows(ctx, doc)
	if rows[0].Status != "answered" || !strings.Contains(rows[0].Result, "b（选项 b）") {
		t.Fatalf("作答后导出行应含选项文案: %+v", rows)
	}
	if h1 := exportHashOf(doc, rows); h1 == h0 {
		t.Fatal("决策被作答后，导出哈希必须变化，否则智能体的差量导出会漏掉它")
	}
	// 历史版本不挂确认项
	hist := st.DocDecisionViews(ctx, doc, doc.Body, true)
	if hist[0].ConfirmRef != "" || hist[0].Answer != nil || !strings.Contains(hist[0].StatusNote, "历史") {
		t.Fatalf("历史版本不应挂确认项: %+v", hist[0])
	}
	// 没有决策的文档：没有导出行，哈希就是正文哈希
	plain := newDecisionDoc(t, st, "wf", "design/plain.md", "# 没有决策\n")
	if r := st.DocDecisionExportRows(ctx, plain); r != nil {
		t.Fatalf("无决策文档不应有导出行: %+v", r)
	}
}

// ---------- 与收件箱的配合 ----------

func TestConfirmInbox_AnswersAreSystemMessages(t *testing.T) {
	st := newConfirmStore(t)
	ctx := context.Background()
	newAsk(t, st, "wf", "问题") // Q1
	if _, err := st.AnswerConfirmItem(ctx, "wf", "Q1", ConfirmAnswerInput{OptionIDs: []string{"a"}}); err != nil {
		t.Fatal(err)
	}
	ans := answerMessages(t, st, "wf")[0]

	// 1) 不在网页的追加气泡里（确认项自己的卡片已经展示了答复）
	items, err := st.ListInboxItems(ctx, "wf")
	if err != nil || len(items) != 0 {
		t.Fatalf("答复不应出现在追加气泡列表: %v %+v", err, items)
	}
	// 2) 但算在「智能体还没读的消息」里：回执提示行、hook 提醒都靠它
	sum, err := st.InboxSummary(ctx, "wf", time.Now())
	if err != nil || sum.Unread != 1 || !strings.Contains(sum.LatestPreview, "Q1") {
		t.Fatalf("答复应计入未读: %+v %v", sum, err)
	}
	// 3) 用户不能撤回答复
	_, err = st.RevokeQueuedFeedback(ctx, ans.ID)
	wantKind(t, err, ErrConflict, "撤回答复")

	// 4) 不会随 interactive_feedback 的秒回被带走；有用户追加时只带走追加
	queueText(t, st, "wf", "用户的普通追加")
	sess, err := st.CreateFeedbackSession(ctx, CreateSessionInput{WorkflowID: "wf", Title: "汇报", Summary: "s", TimeoutSeconds: 60})
	if err != nil {
		t.Fatal(err)
	}
	if sess.ResponseText != "用户的普通追加" || strings.Contains(sess.ResponseText, "Q1") {
		t.Fatalf("秒回只能带走普通追加，不含答复: %q", sess.ResponseText)
	}
	if row := rowByID(t, st, ans.ID); row.Status != "pending" || row.DeliveredAt != nil {
		t.Fatalf("答复仍应是未读: %+v", row)
	}
	// 只有答复时，新汇报不会被秒回（答复不是对这次汇报的回复）
	sess2, err := st.CreateFeedbackSession(ctx, CreateSessionInput{WorkflowID: "wf", Title: "再汇报", Summary: "s", TimeoutSeconds: 60})
	if err != nil || sess2.Status != "pending" || sess2.ResponseText != "" {
		t.Fatalf("只有未读答复时不应秒回: %+v %v", sess2, err)
	}

	// 5) 智能体用 inbox 读取后送达；读过的随下一次交互隐式确认
	// （先让上一轮结束：同一工作流有待回复的会话时，新汇报只是覆盖它，不会走收件箱）
	if err := st.DB().Model(&model.FeedbackSession{}).Where("id = ?", sess2.ID).Update("status", "completed").Error; err != nil {
		t.Fatal(err)
	}
	read, err := st.InboxRead(ctx, "wf", InboxReadOptions{}, time.Now())
	if err != nil || len(read.New) != 1 || read.New[0].ID != ans.ID {
		t.Fatalf("inbox 应能读到答复: %+v %v", read, err)
	}
	if _, err := st.CreateFeedbackSession(ctx, CreateSessionInput{WorkflowID: "wf", Title: "第三次汇报", Summary: "s", TimeoutSeconds: 60}); err != nil {
		t.Fatal(err)
	}
	if row := rowByID(t, st, ans.ID); row.Status != "consumed" || row.AckedAt == nil {
		t.Fatalf("读过的答复应随下一次交互隐式确认: %+v", row)
	}
}

func TestConfirmInbox_AnswersDoNotCountTowardAppendCap(t *testing.T) {
	st := newConfirmStore(t)
	ctx := context.Background()
	now := time.Now()
	rows := make([]model.QueuedFeedback, InboxMaxPending)
	for i := range rows {
		rows[i] = model.QueuedFeedback{WorkflowID: "wf", ResponseText: fmt.Sprintf("答复 %d", i), Status: "pending", Kind: model.InboxKindAnswer, CreatedAt: now, UpdatedAt: now}
	}
	if err := st.DB().Create(&rows).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := st.QueueWorkflowFeedback(ctx, QueueFeedbackInput{WorkflowID: "wf", ResponseText: "用户还能正常追加"}); err != nil {
		t.Fatalf("大量未读答复不应挤占用户的追加额度: %v", err)
	}
}
