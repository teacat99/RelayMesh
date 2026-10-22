package store

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/teacat99/RelayMesh/v2/internal/model"
)

func TestWorkItemStageCreateAndAliases(t *testing.T) {
	st := setupTestStore(t)

	// 默认未标注
	if it := mustCreateWork(t, st, "w", "没标阶段", nil); it.Stage != "" {
		t.Fatalf("默认应是未标注: %q", it.Stage)
	}
	// 标准 id、中文名、英文别名、前后空白与大小写都能规整
	cases := map[string]string{
		"assess": model.WorkStageAssess, "评估": model.WorkStageAssess, " 调研 ": model.WorkStageAssess, "Discuss": model.WorkStageAssess,
		"plan": model.WorkStagePlan, "方案": model.WorkStagePlan, "设计": model.WorkStagePlan,
		"dev": model.WorkStageDev, "开发": model.WorkStageDev, "Implement": model.WorkStageDev,
		"improve": model.WorkStageImprove, "改进": model.WorkStageImprove, "优化": model.WorkStageImprove, "迭代": model.WorkStageImprove,
	}
	for in, want := range cases {
		it := mustCreateWork(t, st, "w", "阶段 "+in, func(i *WorkItemInput) { i.Stage = in })
		if it.Stage != want {
			t.Fatalf("阶段 %q 应规整为 %q，实际 %q", in, want, it.Stage)
		}
	}
	// 无法识别：报错并列出可用值，不创建
	_, err := st.CreateWorkItem(context.Background(), WorkItemInput{WorkflowID: "w", Title: "坏阶段", Stage: "上线"}, human)
	wantInvalid(t, err, "未知的阶段")
	for _, id := range model.WorkStages {
		if !strings.Contains(err.Error(), id) {
			t.Fatalf("错误信息应列出可用阶段 %q: %v", id, err)
		}
	}
	items, _ := st.ListWorkItems(context.Background(), WorkItemFilter{WorkflowID: "w", Query: "坏阶段"})
	if len(items) != 0 {
		t.Fatalf("无法识别的阶段不应创建任务: %d", len(items))
	}
	if model.WorkStageLabel("") != "未标注" || model.WorkStageLabel("nope") != "未标注" || model.WorkStageLabel(model.WorkStageImprove) != "改进" {
		t.Fatal("WorkStageLabel 不符")
	}
}

func TestWorkItemStageUpdateClearAndTimeline(t *testing.T) {
	st := setupTestStore(t)
	ctx := context.Background()
	it := mustCreateWork(t, st, "w", "阶段更新", func(i *WorkItemInput) { i.Stage = "评估" })
	created := it.UpdatedAt

	// 只改阶段：算一次进展（刷新更新时间），时间线写一条「阶段：评估 → 方案」，状态不变
	res, err := st.UpdateWorkItem(ctx, it.ID, WorkItemPatch{Stage: ptr("方案")}, agent)
	if err != nil || !res.Changed || res.Item.Stage != model.WorkStagePlan {
		t.Fatalf("改阶段失败: %v %+v", err, res)
	}
	if !res.Item.UpdatedAt.After(created) || res.Item.State != model.WorkStateTodo {
		t.Fatalf("改阶段应刷新更新时间且不动状态: %+v", res.Item)
	}
	if len(res.Item.Updates) == 0 || res.Item.Updates[0].Text != "阶段：评估 → 方案" || res.Item.Updates[0].ToState != "" {
		t.Fatalf("时间线最新一条应记录阶段变化: %+v", res.Item.Updates)
	}

	// 相同阶段是空操作：不写库
	res, err = st.UpdateWorkItem(ctx, it.ID, WorkItemPatch{Stage: ptr("plan")}, agent)
	if err != nil || res.Changed {
		t.Fatalf("相同阶段应是空操作: %v %+v", err, res)
	}

	// 阶段与状态一起变：时间线只有一条，文字不被阶段变化覆盖（状态变化自带 from / to）
	res, err = st.UpdateWorkItem(ctx, it.ID, WorkItemPatch{Stage: ptr("开发"), State: ptr("doing"), Progress: ptr("开始做")}, agent)
	if err != nil || !res.Changed || res.Item.Stage != model.WorkStageDev || res.Item.State != model.WorkStateDoing {
		t.Fatalf("同时改阶段与状态失败: %v %+v", err, res)
	}
	if u := res.Item.Updates[0]; u.FromState != model.WorkStateTodo || u.ToState != model.WorkStateDoing || u.Text != "开始做" {
		t.Fatalf("时间线应记录状态变化与进度文字: %+v", u)
	}

	// 无法识别：整个补丁不生效
	before := res.Item.UpdatedAt
	_, err = st.UpdateWorkItem(ctx, it.ID, WorkItemPatch{Stage: ptr("上线"), Progress: ptr("不该写入")}, agent)
	wantInvalid(t, err, "未知的阶段")
	got, _ := st.GetWorkItem(ctx, it.ID)
	if got.Stage != model.WorkStageDev || got.Progress != "开始做" || !got.UpdatedAt.Equal(before) {
		t.Fatalf("非法阶段不应有任何副作用: %+v", got)
	}

	// 空串清除：回到未标注，并在时间线里留痕
	res, err = st.UpdateWorkItem(ctx, it.ID, WorkItemPatch{Stage: ptr("")}, human)
	if err != nil || !res.Changed || res.Item.Stage != "" {
		t.Fatalf("清除阶段失败: %v %+v", err, res)
	}
	if res.Item.Updates[0].Text != "阶段：开发 → 未标注" {
		t.Fatalf("清除应记入时间线: %+v", res.Item.Updates[0])
	}
	// 阶段与状态各管各的：智能体也可以在任务验证中时调整阶段
	if _, err = st.UpdateWorkItem(ctx, it.ID, WorkItemPatch{State: ptr("review")}, agent); err != nil {
		t.Fatal(err)
	}
	if res, err = st.UpdateWorkItem(ctx, it.ID, WorkItemPatch{Stage: ptr("improve")}, agent); err != nil || res.Item.Stage != model.WorkStageImprove || res.Item.State != model.WorkStateReview {
		t.Fatalf("验证中的任务也应能调整阶段且不动状态: %v %+v", err, res)
	}
}

func TestWorkItemStageFilter(t *testing.T) {
	st := setupTestStore(t)
	ctx := context.Background()
	mustCreateWork(t, st, "w", "甲", func(i *WorkItemInput) { i.Stage = "assess" })
	mustCreateWork(t, st, "w", "乙", func(i *WorkItemInput) { i.Stage = "dev" })
	mustCreateWork(t, st, "w", "丙", nil)
	mustCreateWork(t, st, "w", "丁", func(i *WorkItemInput) { i.Stage = "improve" })

	titles := func(f WorkItemFilter) string {
		f.WorkflowID = "w"
		items, err := st.ListWorkItems(ctx, f)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, it := range items {
			out = append(out, it.Title)
		}
		// 列表按更新时间倒序；这里只关心集合，排序后比较
		slices.Sort(out)
		return strings.Join(out, ",")
	}
	if got := titles(WorkItemFilter{Stages: []string{"dev"}}); got != "乙" {
		t.Fatalf("按开发阶段过滤: %q", got)
	}
	if got := titles(WorkItemFilter{Stages: []string{"评估", "improve"}}); got != "丁,甲" {
		t.Fatalf("多个阶段（中文名与 id 混用）: %q", got)
	}
	if got := titles(WorkItemFilter{Stages: []string{"none"}}); got != "丙" {
		t.Fatalf("none = 未标注: %q", got)
	}
	if got := titles(WorkItemFilter{Stages: []string{"未标注", "plan"}}); got != "丙" {
		t.Fatalf("未标注 + 没有任务的阶段: %q", got)
	}
	if got := titles(WorkItemFilter{Stages: []string{"上线"}}); got != "" {
		t.Fatalf("全部无法识别应返回空: %q", got)
	}
	if got := titles(WorkItemFilter{}); got != "丁,丙,乙,甲" {
		t.Fatalf("不过滤应返回全部: %q", got)
	}
}

func TestWorkItemSessionCtxAndFilter(t *testing.T) {
	st := setupTestStore(t)
	ctx := context.Background()
	seedSession(t, st, "sess-aaaa", "w", "会话甲")
	seedSession(t, st, "sess-bbbb", "w", "会话乙")
	seedSession(t, st, "sess-cccc", "w", "会话丙")
	sa := WorkActor{Kind: model.WorkActorAgent, SessionID: "sess-aaaa"}
	sb := WorkActor{Kind: model.WorkActorAgent, SessionID: "sess-bbbb"}

	// 甲创建两个任务；乙推进其中一个；丙什么都没碰
	r1, err := st.CreateWorkItem(ctx, WorkItemInput{WorkflowID: "w", Title: "任务一", Stage: "plan"}, sa)
	if err != nil {
		t.Fatal(err)
	}
	r2, err := st.CreateWorkItem(ctx, WorkItemInput{WorkflowID: "w", Title: "任务二"}, sa)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = st.UpdateWorkItem(ctx, r1.Item.ID, WorkItemPatch{State: ptr("doing")}, sa); err != nil {
		t.Fatal(err)
	}
	if _, err = st.UpdateWorkItem(ctx, r1.Item.ID, WorkItemPatch{Progress: ptr("乙补充了进度")}, sb); err != nil {
		t.Fatal(err)
	}
	if _, err = st.UpdateWorkItem(ctx, r1.Item.ID, WorkItemPatch{State: ptr("review"), Conclusion: ptr("做完了")}, sa); err != nil {
		t.Fatal(err)
	}
	mustCreateWork(t, st, "w", "无会话的任务", nil) // 人在网页上建的，没有会话

	// 不带会话过滤：没有 session_ctx
	all, err := st.ListWorkItems(ctx, WorkItemFilter{WorkflowID: "w"})
	if err != nil || len(all) != 3 {
		t.Fatalf("总共应有 3 个任务: %v %d", err, len(all))
	}
	for _, it := range all {
		if it.SessionCtx != nil {
			t.Fatalf("没有按会话查询时不应附 session_ctx: %+v", it.SessionCtx)
		}
	}

	// 甲：两个任务；任务一创建 + 两次状态变化（todo→doing，doing→review）
	got, err := st.ListWorkItems(ctx, WorkItemFilter{SessionID: "sess-aaaa"})
	if err != nil || len(got) != 2 {
		t.Fatalf("甲应关联 2 个任务: %v %d", err, len(got))
	}
	byTitle := map[string]model.WorkItem{}
	for _, it := range got {
		byTitle[it.Title] = it
	}
	one, two := byTitle["任务一"], byTitle["任务二"]
	if one.SessionCtx == nil || !one.SessionCtx.Created || one.SessionCtx.Updates != 3 || one.SessionCtx.FromState != model.WorkStateTodo || one.SessionCtx.ToState != model.WorkStateReview || one.SessionCtx.LastAt == nil {
		t.Fatalf("甲对任务一：创建 + 3 条时间线 + todo→review: %+v", one.SessionCtx)
	}
	if one.Stage != model.WorkStagePlan {
		t.Fatalf("列表项应带阶段: %+v", one)
	}
	if two.SessionCtx == nil || !two.SessionCtx.Created || two.SessionCtx.Updates != 1 || two.SessionCtx.FromState != "" || two.SessionCtx.ToState != "" {
		t.Fatalf("甲对任务二：只有创建那一条、没有状态变化: %+v", two.SessionCtx)
	}

	// 乙：只推进过任务一，不是创建者，也没改过状态
	got, err = st.ListWorkItems(ctx, WorkItemFilter{SessionID: "sess-bbbb"})
	if err != nil || len(got) != 1 || got[0].Title != "任务一" {
		t.Fatalf("乙应只关联任务一: %v %+v", err, got)
	}
	if c := got[0].SessionCtx; c == nil || c.Created || c.Updates != 1 || c.FromState != "" || c.ToState != "" {
		t.Fatalf("乙对任务一：不是创建者、1 条、没有状态变化: %+v", c)
	}

	// 丙：没有关联任何任务 → 空列表（不是全部）
	got, err = st.ListWorkItems(ctx, WorkItemFilter{SessionID: "sess-cccc"})
	if err != nil || len(got) != 0 {
		t.Fatalf("丙没有关联任务，应返回空: %v %d", err, len(got))
	}
	// 会话过滤可以与其它条件叠加
	got, err = st.ListWorkItems(ctx, WorkItemFilter{SessionID: "sess-aaaa", States: []string{"review"}})
	if err != nil || len(got) != 1 || got[0].Title != "任务一" {
		t.Fatalf("会话 + 状态过滤: %v %+v", err, got)
	}
	// 手工关联（只是被关联、时间线上没有它的记录）：Updates=0，LastAt 为空
	if err := st.LinkWorkItemSession(ctx, r2.Item.ID, "sess-cccc"); err != nil {
		t.Fatal(err)
	}
	got, err = st.ListWorkItems(ctx, WorkItemFilter{SessionID: "sess-cccc"})
	if err != nil || len(got) != 1 {
		t.Fatalf("手工关联后丙应有 1 个任务: %v %d", err, len(got))
	}
	if c := got[0].SessionCtx; c == nil || c.Created || c.Updates != 0 || c.LastAt != nil {
		t.Fatalf("只是被关联：Updates=0 且没有 LastAt: %+v", c)
	}
}

// 会话页按轮次展开任务：一次取回整个工作流的「会话 ↔ 任务」关联，结果与逐个会话查询一致，且不串工作流。
func TestListWorkflowSessionLinks(t *testing.T) {
	st := setupTestStore(t)
	ctx := context.Background()
	seedSession(t, st, "sess-aaaa", "w", "会话甲")
	seedSession(t, st, "sess-bbbb", "w", "会话乙")
	seedSession(t, st, "sess-cccc", "w", "会话丙")
	seedSession(t, st, "sess-dddd", "other", "别的项目")
	sa := WorkActor{Kind: model.WorkActorAgent, SessionID: "sess-aaaa"}
	sb := WorkActor{Kind: model.WorkActorAgent, SessionID: "sess-bbbb"}

	r1, _ := st.CreateWorkItem(ctx, WorkItemInput{WorkflowID: "w", Title: "任务一"}, sa)
	r2, _ := st.CreateWorkItem(ctx, WorkItemInput{WorkflowID: "w", Title: "任务二"}, sa)
	if _, err := st.CreateWorkItem(ctx, WorkItemInput{WorkflowID: "other", Title: "别的项目的任务"}, WorkActor{Kind: model.WorkActorAgent, SessionID: "sess-dddd"}); err != nil {
		t.Fatal(err)
	}
	mustUpdate := func(id uint, p WorkItemPatch, a WorkActor) {
		t.Helper()
		if _, err := st.UpdateWorkItem(ctx, id, p, a); err != nil {
			t.Fatal(err)
		}
	}
	mustUpdate(r1.Item.ID, WorkItemPatch{State: ptr("doing")}, sa)
	mustUpdate(r1.Item.ID, WorkItemPatch{Progress: ptr("乙补充了进度")}, sb)
	mustUpdate(r1.Item.ID, WorkItemPatch{State: ptr("review"), Conclusion: ptr("做完了")}, sa)
	if err := st.LinkWorkItemSession(ctx, r2.Item.ID, "sess-cccc"); err != nil { // 手工关联：时间线上没有记录
		t.Fatal(err)
	}

	links, err := st.ListWorkflowSessionLinks(ctx, "w")
	if err != nil {
		t.Fatal(err)
	}
	type row struct {
		session string
		item    uint
		created bool
		updates int
		from    string
		to      string
	}
	var got []row
	for _, l := range links {
		got = append(got, row{l.SessionID, l.WorkItemID, l.Created, l.Updates, l.FromState, l.ToState})
	}
	want := []row{
		{"sess-aaaa", r1.Item.ID, true, 3, model.WorkStateTodo, model.WorkStateReview},
		{"sess-aaaa", r2.Item.ID, true, 1, "", ""},
		{"sess-bbbb", r1.Item.ID, false, 1, "", ""},
		{"sess-cccc", r2.Item.ID, false, 0, "", ""},
	}
	if len(got) != len(want) {
		t.Fatalf("应有 %d 条关联，实际 %d: %+v", len(want), len(got), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("第 %d 条关联不符：期望 %+v，实际 %+v", i, want[i], got[i])
		}
	}
	// 手工关联没有时间线记录：没有 LastAt；有记录的有
	if links[3].LastAt != nil || links[0].LastAt == nil {
		t.Fatalf("LastAt 不符: %+v %+v", links[3], links[0])
	}
	// 与按会话逐个查询的结果一致
	one, err := st.ListWorkItems(ctx, WorkItemFilter{SessionID: "sess-aaaa"})
	if err != nil || len(one) != 2 {
		t.Fatalf("%v %d", err, len(one))
	}
	for _, it := range one {
		var match *model.WorkItemSessionLink
		for i := range links {
			if links[i].SessionID == "sess-aaaa" && links[i].WorkItemID == it.ID {
				match = &links[i]
			}
		}
		if match == nil || match.Created != it.SessionCtx.Created || match.Updates != it.SessionCtx.Updates || match.FromState != it.SessionCtx.FromState || match.ToState != it.SessionCtx.ToState {
			t.Fatalf("批量与逐个查询不一致: %+v vs %+v", match, it.SessionCtx)
		}
	}
	// 别的工作流只看到自己的；空 / 不存在的工作流返回空数组（不是 nil）
	other, err := st.ListWorkflowSessionLinks(ctx, "other")
	if err != nil || len(other) != 1 || other[0].SessionID != "sess-dddd" {
		t.Fatalf("别的工作流: %v %+v", err, other)
	}
	for _, wf := range []string{"", "  ", "nope"} {
		empty, err := st.ListWorkflowSessionLinks(ctx, wf)
		if err != nil || empty == nil || len(empty) != 0 {
			t.Fatalf("工作流 %q 应返回空数组: %v %v", wf, err, empty)
		}
	}
}
