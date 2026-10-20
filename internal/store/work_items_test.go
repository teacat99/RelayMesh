package store

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/teacat99/RelayMesh/v2/internal/model"
)

var (
	human = WorkActor{Kind: model.WorkActorHuman}
	agent = WorkActor{Kind: model.WorkActorAgent}
)

func mustCreateWork(t *testing.T, st *Store, workflow, title string, mut func(*WorkItemInput)) *model.WorkItem {
	t.Helper()
	in := WorkItemInput{WorkflowID: workflow, Title: title}
	if mut != nil {
		mut(&in)
	}
	res, err := st.CreateWorkItem(context.Background(), in, human)
	if err != nil {
		t.Fatalf("创建任务 %q: %v", title, err)
	}
	return res.Item
}

func ptr[T any](v T) *T { return &v }

func wantInvalid(t *testing.T, err error, contains string) {
	t.Helper()
	var ae *AppError
	if !errors.As(err, &ae) || ae.Kind != ErrInvalidInput {
		t.Fatalf("期望 invalid_input 错误，得到 %v", err)
	}
	if contains != "" && !strings.Contains(ae.Message, contains) {
		t.Fatalf("错误信息应包含 %q，实际 %q", contains, ae.Message)
	}
}

func wantNotFound(t *testing.T, err error) {
	t.Helper()
	var ae *AppError
	if !errors.As(err, &ae) || ae.Kind != ErrNotFound {
		t.Fatalf("期望 not_found 错误，得到 %v", err)
	}
}

func TestWorkItemSeqIsPerWorkflowAndNeverReused(t *testing.T) {
	st := setupTestStore(t)
	ctx := context.Background()

	a1 := mustCreateWork(t, st, "wf-a", "第一件", nil)
	a2 := mustCreateWork(t, st, "wf-a", "第二件", nil)
	b1 := mustCreateWork(t, st, "wf-b", "别的工作流", nil)
	if a1.Seq != 1 || a2.Seq != 2 || b1.Seq != 1 {
		t.Fatalf("序号应按工作流各自从 1 递增: a=%d,%d b=%d", a1.Seq, a2.Seq, b1.Seq)
	}
	if _, err := st.DeleteWorkItem(ctx, a2.ID); err != nil {
		t.Fatalf("删除: %v", err)
	}
	a3 := mustCreateWork(t, st, "wf-a", "删除之后的新任务", nil)
	if a3.Seq != 3 {
		t.Fatalf("删除 #2 之后新任务应是 #3（序号不复用），得到 #%d", a3.Seq)
	}
	got, err := st.GetWorkItemBySeq(ctx, "wf-a", 3)
	if err != nil || got.ID != a3.ID {
		t.Fatalf("按序号读取失败: %v %+v", err, got)
	}
	if _, err := st.GetWorkItemBySeq(ctx, "wf-a", 2); err == nil {
		t.Fatalf("已删除的 #2 不应再能读到")
	} else {
		wantNotFound(t, err)
	}
}

func TestWorkItemSeqIsUniqueUnderConcurrency(t *testing.T) {
	st := setupTestStore(t)
	const n = 24
	var wg sync.WaitGroup
	seqs := make(chan int, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			res, err := st.CreateWorkItem(context.Background(), WorkItemInput{WorkflowID: "wf", Title: fmt.Sprintf("并发 %d", i)}, agent)
			if err != nil {
				t.Errorf("并发创建失败: %v", err)
				return
			}
			seqs <- res.Item.Seq
		}(i)
	}
	wg.Wait()
	close(seqs)
	var got []int
	for s := range seqs {
		got = append(got, s)
	}
	slices.Sort(got)
	for i, s := range got {
		if s != i+1 {
			t.Fatalf("序号应连续且不重复: %v", got)
		}
	}
	if len(got) != n {
		t.Fatalf("应创建 %d 个，实际 %d", n, len(got))
	}
}

func TestWorkItemCounterContinuesFromExistingRows(t *testing.T) {
	st := setupTestStore(t)
	ctx := context.Background()
	// 模拟计数器出现之前就存在的条目（例如手工导入）。
	if err := st.db.Create(&model.WorkItem{WorkflowID: "legacy", Seq: 7, Title: "旧条目", Kind: "bug", State: "todo", CreatedBy: "human", CreatedAt: time.Now(), UpdatedAt: time.Now()}).Error; err != nil {
		t.Fatal(err)
	}
	res, err := st.CreateWorkItem(ctx, WorkItemInput{WorkflowID: "legacy", Title: "接着发"}, human)
	if err != nil {
		t.Fatal(err)
	}
	if res.Item.Seq != 8 {
		t.Fatalf("应接着现有最大序号 7 发 8，得到 %d", res.Item.Seq)
	}
}

func TestWorkItemCreateValidationAndNormalization(t *testing.T) {
	st := setupTestStore(t)
	ctx := context.Background()
	create := func(in WorkItemInput) (*WorkItemResult, error) { return st.CreateWorkItem(ctx, in, human) }

	if _, err := create(WorkItemInput{Title: "没有工作流"}); err == nil {
		t.Fatal("缺少 workflow_id 应被拒绝")
	} else {
		wantInvalid(t, err, "workflow_id")
	}
	if _, err := create(WorkItemInput{WorkflowID: "w", Title: "   "}); err == nil {
		t.Fatal("空标题应被拒绝")
	} else {
		wantInvalid(t, err, "标题")
	}
	if _, err := create(WorkItemInput{WorkflowID: "w", Title: strings.Repeat("长", 201)}); err == nil {
		t.Fatal("超长标题应被拒绝")
	} else {
		wantInvalid(t, err, "200")
	}
	if _, err := create(WorkItemInput{WorkflowID: "w", Title: "两行\n标题"}); err == nil {
		t.Fatal("多行标题应被拒绝")
	}
	if _, err := create(WorkItemInput{WorkflowID: "w", Title: "t", Kind: "nonsense"}); err == nil {
		t.Fatal("未知类型应被拒绝")
	} else {
		wantInvalid(t, err, "bug / feature")
	}
	if _, err := create(WorkItemInput{WorkflowID: "w", Title: "t", State: "nonsense"}); err == nil {
		t.Fatal("未知状态应被拒绝")
	}
	if _, err := create(WorkItemInput{WorkflowID: "w", Title: "t", Progress: strings.Repeat("字", 501)}); err == nil {
		t.Fatal("超长进度应被拒绝")
	} else {
		wantInvalid(t, err, "会话文档")
	}
	if _, err := create(WorkItemInput{WorkflowID: "w", Title: "t", Labels: []string{"1", "2", "3", "4", "5", "6", "7", "8", "9"}}); err == nil {
		t.Fatal("超过 8 个标签应被拒绝")
	}
	if _, err := create(WorkItemInput{WorkflowID: "w", Title: "t", Refs: make([]string, 0)}); err != nil {
		t.Fatalf("空引用合法: %v", err)
	}
	refs := make([]string, 11)
	for i := range refs {
		refs[i] = fmt.Sprintf("ref-%d", i)
	}
	if _, err := create(WorkItemInput{WorkflowID: "w", Title: "t", Refs: refs}); err == nil {
		t.Fatal("超过 10 条引用应被拒绝")
	}

	res, err := create(WorkItemInput{
		WorkflowID: " w ", Title: "  修复设置页  ", Kind: "缺陷", State: "进行中",
		Labels: []string{"设置", " 设置 ", "UI", "ui", ""}, Refs: []string{"commit:abc1234", "commit:abc1234", " D-201 "},
		Progress: " 已定位 ",
	})
	if err != nil {
		t.Fatalf("规整后的创建应成功: %v", err)
	}
	it := res.Item
	if it.WorkflowID != "w" || it.Title != "修复设置页" || it.Kind != "bug" || it.State != "doing" || it.Progress != "已定位" {
		t.Fatalf("字段没有按预期规整: %+v", it)
	}
	if !slices.Equal(it.Labels, []string{"设置", "UI"}) {
		t.Fatalf("标签应去空白并去重（不区分大小写）: %v", it.Labels)
	}
	if !slices.Equal(it.Refs, []string{"commit:abc1234", "D-201"}) {
		t.Fatalf("引用应去空白并去重: %v", it.Refs)
	}
	if it.CreatedBy != "human" || it.ClosedAt != nil {
		t.Fatalf("创建者 / 关闭时间不符: %+v", it)
	}
	if len(it.Updates) != 1 || it.Updates[0].ToState != "doing" || it.Updates[0].Text != "已定位" {
		t.Fatalf("创建应在时间线留一条记录: %+v", it.Updates)
	}
}

func TestWorkItemDefaults(t *testing.T) {
	st := setupTestStore(t)
	it := mustCreateWork(t, st, "w", "只给标题", nil)
	if it.Kind != model.WorkKindFeature || it.State != model.WorkStateTodo {
		t.Fatalf("缺省应是 功能 / 待办，得到 %s / %s", it.Kind, it.State)
	}
	if it.Labels == nil || it.Refs == nil {
		t.Fatalf("列表字段不应为 nil（序列化成 null 会让前端出错）: %#v %#v", it.Labels, it.Refs)
	}
}

// 状态绑定智能体自己的开发流程（#110）：验证通过后智能体直接置「已完成」，但必须带结论；
// 不再被改写成「待确认」，也不再有「已完成只能由人改动」的限制。
func TestWorkItemDoneRequiresConclusionFromAgent(t *testing.T) {
	st := setupTestStore(t)
	ctx := context.Background()

	// 新建时还没有结论可写：智能体不能直接建出「已完成」的任务
	if _, err := st.CreateWorkItem(ctx, WorkItemInput{WorkflowID: "w", Title: "补记", State: "done"}, agent); err == nil {
		t.Fatal("智能体不应能直接创建已完成的任务")
	} else {
		wantInvalid(t, err, "新建任务不能直接是「已完成」")
	}
	// 人想补记一条已经做完的事是可以的（也没有结论要求）
	legacy, err := st.CreateWorkItem(ctx, WorkItemInput{WorkflowID: "w", Title: "补记", State: "done"}, human)
	if err != nil || legacy.Item.State != model.WorkStateDone || legacy.Item.ClosedAt == nil {
		t.Fatalf("人可以直接建出已完成的任务: %v %+v", err, legacy)
	}

	it := mustCreateWork(t, st, "w", "要做的事", nil)

	// 没写结论：拒绝，任务原样不动，也不留时间线
	if _, err := st.UpdateWorkItem(ctx, it.ID, WorkItemPatch{State: ptr("done")}, agent); err == nil {
		t.Fatal("没有结论的「已完成」不应被接受")
	} else {
		wantInvalid(t, err, "要同时写结论")
	}
	if _, err := st.UpdateWorkItem(ctx, it.ID, WorkItemPatch{State: ptr("done"), Conclusion: ptr("   ")}, agent); err == nil {
		t.Fatal("只有空白的结论不算结论")
	}
	got, err := st.GetWorkItem(ctx, it.ID)
	if err != nil || got.State != model.WorkStateTodo || got.Conclusion != "" || len(got.Updates) != 1 {
		t.Fatalf("被拒绝的写入不应留下任何痕迹: %v %+v", err, got)
	}

	// 验证中不是关闭状态
	r, err := st.UpdateWorkItem(ctx, it.ID, WorkItemPatch{State: ptr("review")}, agent)
	if err != nil || r.Item.State != model.WorkStateReview || r.Item.ClosedAt != nil {
		t.Fatalf("验证中不是关闭状态: %v %+v", err, r)
	}

	// 结论和状态放在同一次调用里：直接置「已完成」，不被改写
	const evidence = "回归全过；e2e 通过；热更后冒烟正常"
	r, err = st.UpdateWorkItem(ctx, it.ID, WorkItemPatch{State: ptr("已完成"), Conclusion: ptr(evidence)}, agent)
	if err != nil {
		t.Fatal(err)
	}
	if r.Item.State != model.WorkStateDone || r.Item.ClosedAt == nil || r.Item.Conclusion != evidence {
		t.Fatalf("智能体验证通过后应能直接置已完成: %+v", r.Item)
	}
	if top := r.Item.Updates[0]; top.FromState != model.WorkStateReview || top.ToState != model.WorkStateDone || top.ActorKind != model.WorkActorAgent {
		t.Fatalf("时间线应记「验证中 → 已完成」且是智能体做的: %+v", top)
	}

	// 结论先前已经写过时，单独置「已完成」也行
	it2 := mustCreateWork(t, st, "w", "先写结论再关", nil)
	if _, err := st.UpdateWorkItem(ctx, it2.ID, WorkItemPatch{Conclusion: ptr("做完了")}, agent); err != nil {
		t.Fatal(err)
	}
	if r, err := st.UpdateWorkItem(ctx, it2.ID, WorkItemPatch{State: ptr("done")}, agent); err != nil || r.Item.State != model.WorkStateDone {
		t.Fatalf("结论已经写过时可以单独置已完成: %v %+v", err, r)
	}

	// 对已完成的任务再次置「已完成」是空操作，哪怕它是历史遗留、没有结论
	if r, err := st.UpdateWorkItem(ctx, legacy.Item.ID, WorkItemPatch{State: ptr("done")}, agent); err != nil || r.Changed {
		t.Fatalf("对已完成任务再次置为已完成应是空操作: %v %+v", err, r)
	}

	// 智能体可以重新打开已完成的任务（它不再是「用户盖过章的东西」）：关闭时间被清除，时间线记着是谁做的
	r, err = st.UpdateWorkItem(ctx, it.ID, WorkItemPatch{State: ptr("doing"), Note: "回归时发现还差一处"}, agent)
	if err != nil {
		t.Fatalf("智能体应能重新打开已完成的任务: %v", err)
	}
	if r.Item.State != model.WorkStateDoing || r.Item.ClosedAt != nil {
		t.Fatalf("重新打开应清除关闭时间: %+v", r.Item)
	}
	if top := r.Item.Updates[0]; top.FromState != model.WorkStateDone || top.ToState != model.WorkStateDoing || top.ActorKind != model.WorkActorAgent {
		t.Fatalf("时间线应记「已完成 → 进行中」且是智能体做的: %+v", top)
	}

	// 人置「已完成」不要求结论
	it3 := mustCreateWork(t, st, "w", "人关的", nil)
	if r, err := st.UpdateWorkItem(ctx, it3.ID, WorkItemPatch{State: ptr("已完成")}, human); err != nil || r.Item.State != model.WorkStateDone || r.Item.ClosedAt == nil {
		t.Fatalf("人置已完成不需要结论: %v %+v", err, r)
	}

	// 补充备注与引用照旧可以，不动状态
	r, err = st.UpdateWorkItem(ctx, it2.ID, WorkItemPatch{Note: "补一条说明", Refs: &[]string{"commit:deadbee"}}, agent)
	if err != nil || r.Item.State != model.WorkStateDone || !slices.Equal(r.Item.Refs, []string{"commit:deadbee"}) {
		t.Fatalf("已完成任务仍可补充备注与引用，且不改变状态: %v %+v", err, r)
	}
}

func TestWorkItemShelvedClosesAndReopens(t *testing.T) {
	st := setupTestStore(t)
	ctx := context.Background()
	it := mustCreateWork(t, st, "w", "先放放", nil)
	r, err := st.UpdateWorkItem(ctx, it.ID, WorkItemPatch{State: ptr("搁置")}, agent)
	if err != nil {
		t.Fatal(err)
	}
	if r.Item.State != model.WorkStateShelved || r.Item.ClosedAt == nil {
		t.Fatalf("搁置应记录关闭时间: %+v", r.Item)
	}
	closedAt := *r.Item.ClosedAt
	// 搁置 → 完成（人）：关闭时间保持第一次进入关闭态的时间
	r, err = st.UpdateWorkItem(ctx, it.ID, WorkItemPatch{State: ptr("done")}, human)
	if err != nil {
		t.Fatal(err)
	}
	if r.Item.ClosedAt == nil || !r.Item.ClosedAt.Equal(closedAt) {
		t.Fatalf("在两个关闭态之间切换不应改写关闭时间")
	}
	// 智能体可以把搁置的任务拉回主线
	it2 := mustCreateWork(t, st, "w", "另一件", nil)
	if _, err := st.UpdateWorkItem(ctx, it2.ID, WorkItemPatch{State: ptr("shelved")}, human); err != nil {
		t.Fatal(err)
	}
	r, err = st.UpdateWorkItem(ctx, it2.ID, WorkItemPatch{State: ptr("doing")}, agent)
	if err != nil {
		t.Fatalf("智能体可以重新开始搁置的任务: %v", err)
	}
	if r.Item.State != model.WorkStateDoing || r.Item.ClosedAt != nil {
		t.Fatalf("%+v", r.Item)
	}
}

func TestWorkItemUpdateTimelineAndNoOp(t *testing.T) {
	st := setupTestStore(t)
	ctx := context.Background()
	it := mustCreateWork(t, st, "w", "写时间线", nil)

	// 没有任何变化：不写库、不动 updated_at、不产生时间线
	before := it.UpdatedAt
	time.Sleep(5 * time.Millisecond)
	r, err := st.UpdateWorkItem(ctx, it.ID, WorkItemPatch{Title: ptr("写时间线"), Labels: ptr([]string{}), Progress: ptr("")}, human)
	if err != nil {
		t.Fatal(err)
	}
	if r.Changed || !r.Item.UpdatedAt.Equal(before) || len(r.Item.Updates) != 1 {
		t.Fatalf("无变化的更新不应写库: changed=%v updates=%d", r.Changed, len(r.Item.Updates))
	}

	// 进度覆盖：只有最新进度，历史在时间线里
	for i := 1; i <= 3; i++ {
		if _, err := st.UpdateWorkItem(ctx, it.ID, WorkItemPatch{Progress: ptr(fmt.Sprintf("进度 %d", i))}, agent); err != nil {
			t.Fatal(err)
		}
	}
	got, _ := st.GetWorkItem(ctx, it.ID)
	if got.Progress != "进度 3" {
		t.Fatalf("进度应被覆盖为最新: %q", got.Progress)
	}
	if len(got.Updates) != 4 || got.Updates[0].Text != "进度 3" || got.Updates[3].ToState != "todo" {
		t.Fatalf("时间线应新的在前，并保留创建记录: %+v", got.Updates)
	}

	// 只改标题 / 标签 / 引用不占时间线，但会刷新 updated_at
	r, err = st.UpdateWorkItem(ctx, it.ID, WorkItemPatch{Title: ptr("改了标题"), Labels: ptr([]string{"x"})}, human)
	if err != nil || !r.Changed || len(r.Item.Updates) != 4 {
		t.Fatalf("改标题 / 标签不应新增时间线: %v updates=%d", err, len(r.Item.Updates))
	}

	// 备注 + 进度同时给：时间线文本取备注
	r, err = st.UpdateWorkItem(ctx, it.ID, WorkItemPatch{Progress: ptr("新进度"), Note: "关键决策：不做 B 方案"}, agent)
	if err != nil {
		t.Fatal(err)
	}
	if r.Item.Updates[0].Text != "关键决策：不做 B 方案" || r.Item.Progress != "新进度" {
		t.Fatalf("%+v", r.Item.Updates[0])
	}

	// 状态变化记录 from → to
	r, err = st.UpdateWorkItem(ctx, it.ID, WorkItemPatch{State: ptr("doing")}, agent)
	if err != nil {
		t.Fatal(err)
	}
	if u := r.Item.Updates[0]; u.FromState != "todo" || u.ToState != "doing" || u.ActorKind != "agent" {
		t.Fatalf("状态变化应记录 from→to 与操作者: %+v", u)
	}
}

func TestWorkItemTimelineKeepsLatestTwenty(t *testing.T) {
	st := setupTestStore(t)
	ctx := context.Background()
	it := mustCreateWork(t, st, "w", "很多进展", nil)
	for i := 1; i <= 30; i++ {
		if _, err := st.UpdateWorkItem(ctx, it.ID, WorkItemPatch{Note: fmt.Sprintf("第 %d 条", i)}, agent); err != nil {
			t.Fatal(err)
		}
	}
	got, _ := st.GetWorkItem(ctx, it.ID)
	if len(got.Updates) != model.WorkUpdatesKeep {
		t.Fatalf("时间线应只留最近 %d 条，得到 %d", model.WorkUpdatesKeep, len(got.Updates))
	}
	if got.Updates[0].Text != "第 30 条" || got.Updates[model.WorkUpdatesKeep-1].Text != "第 11 条" {
		t.Fatalf("保留的应是最新的 20 条: %q … %q", got.Updates[0].Text, got.Updates[model.WorkUpdatesKeep-1].Text)
	}
	var total int64
	st.db.Model(&model.WorkItemUpdate{}).Where("work_item_id = ?", it.ID).Count(&total)
	if total != int64(model.WorkUpdatesKeep) {
		t.Fatalf("库里也只应有 %d 条，实际 %d", model.WorkUpdatesKeep, total)
	}
}

func TestWorkItemParentRules(t *testing.T) {
	st := setupTestStore(t)
	ctx := context.Background()
	batch := mustCreateWork(t, st, "w", "一批问题", nil)
	c1 := mustCreateWork(t, st, "w", "子任务 1", func(in *WorkItemInput) { in.ParentID = batch.ID })
	c2 := mustCreateWork(t, st, "w", "子任务 2", func(in *WorkItemInput) { in.ParentID = batch.ID })
	other := mustCreateWork(t, st, "other", "别的工作流", nil)

	if c1.ParentID == nil || *c1.ParentID != batch.ID || c1.ParentSeq != batch.Seq || c1.ParentTitle != "一批问题" {
		t.Fatalf("子任务应带父任务信息: %+v", c1)
	}
	// 子任务进度
	if _, err := st.UpdateWorkItem(ctx, c1.ID, WorkItemPatch{State: ptr("done")}, human); err != nil {
		t.Fatal(err)
	}
	got, _ := st.GetWorkItem(ctx, batch.ID)
	if got.ChildTotal != 2 || got.ChildDone != 1 {
		t.Fatalf("批次应汇总子任务进度 1/2，得到 %d/%d", got.ChildDone, got.ChildTotal)
	}

	// 规则
	if _, err := st.CreateWorkItem(ctx, WorkItemInput{WorkflowID: "w", Title: "跨工作流", ParentID: other.ID}, human); err == nil {
		t.Fatal("父任务必须在同一工作流")
	} else {
		wantInvalid(t, err, "同一个工作流")
	}
	if _, err := st.CreateWorkItem(ctx, WorkItemInput{WorkflowID: "w", Title: "不存在", ParentID: 99999}, human); err == nil {
		t.Fatal("不存在的父任务应被拒绝")
	} else {
		wantNotFound(t, err)
	}
	if _, err := st.CreateWorkItem(ctx, WorkItemInput{WorkflowID: "w", Title: "孙任务", ParentID: c2.ID}, human); err == nil {
		t.Fatal("只支持一层嵌套")
	} else {
		wantInvalid(t, err, "一层嵌套")
	}
	top := mustCreateWork(t, st, "w", "另一个顶层任务", nil)
	if _, err := st.UpdateWorkItem(ctx, batch.ID, WorkItemPatch{ParentID: ptr(top.ID)}, human); err == nil {
		t.Fatal("有子任务的任务不能再成为子任务")
	} else {
		wantInvalid(t, err, "已经有子任务")
	}
	if _, err := st.UpdateWorkItem(ctx, batch.ID, WorkItemPatch{ParentID: ptr(c2.ID)}, human); err == nil {
		t.Fatal("不能挂到别的任务的子任务下面")
	} else {
		wantInvalid(t, err, "一层嵌套")
	}
	if _, err := st.UpdateWorkItem(ctx, c2.ID, WorkItemPatch{ParentID: ptr(c2.ID)}, human); err == nil {
		t.Fatal("不能以自己为父任务")
	}

	// 解除与重新挂靠
	r, err := st.UpdateWorkItem(ctx, c2.ID, WorkItemPatch{ParentID: ptr(uint(0))}, human)
	if err != nil || r.Item.ParentID != nil {
		t.Fatalf("parent_id=0 应解除父任务: %v %+v", err, r.Item.ParentID)
	}
	r, err = st.UpdateWorkItem(ctx, c2.ID, WorkItemPatch{ParentID: ptr(batch.ID)}, human)
	if err != nil || r.Item.ParentID == nil || *r.Item.ParentID != batch.ID {
		t.Fatalf("应能重新挂靠: %v", err)
	}

	// 删除父任务：子任务变成顶层
	if _, err := st.DeleteWorkItem(ctx, batch.ID); err != nil {
		t.Fatal(err)
	}
	for _, id := range []uint{c1.ID, c2.ID} {
		kid, err := st.GetWorkItem(ctx, id)
		if err != nil || kid.ParentID != nil {
			t.Fatalf("删除父任务后子任务应成为顶层: %v %+v", err, kid)
		}
	}
}

func seedSession(t *testing.T, st *Store, id, workflow, title string) {
	t.Helper()
	if err := st.db.Create(&model.FeedbackSession{ID: id, WorkflowID: workflow, Title: title, Summary: "摘要 " + id, Status: "completed"}).Error; err != nil {
		t.Fatalf("写入会话 %s: %v", id, err)
	}
}

func TestWorkItemSessionsAreManyToMany(t *testing.T) {
	st := setupTestStore(t)
	ctx := context.Background()
	seedSession(t, st, "sess-aaaa", "w", "会话甲")
	seedSession(t, st, "sess-bbbb", "w", "")

	// 创建时带会话：来源与最近会话都记下，并关联
	res, err := st.CreateWorkItem(ctx, WorkItemInput{WorkflowID: "w", Title: "任务一"}, WorkActor{Kind: "agent", SessionID: "sess-aaaa"})
	if err != nil || !res.SessionLinked {
		t.Fatalf("existing session should be linked: %v %+v", err, res)
	}
	one := res.Item
	if one.SourceSessionID != "sess-aaaa" || one.LastSessionID != "sess-aaaa" || len(one.Sessions) != 1 || one.Sessions[0].Title != "会话甲" {
		t.Fatalf("来源 / 最近会话 / 关联不符: %+v", one)
	}
	// 同一个会话再涉及另一个任务
	res2, err := st.CreateWorkItem(ctx, WorkItemInput{WorkflowID: "w", Title: "任务二"}, WorkActor{Kind: "agent", SessionID: "sess-aaaa"})
	if err != nil || !res2.SessionLinked {
		t.Fatal(err)
	}
	// 另一个会话更新任务一：多对多，最近会话变化，来源不变
	r, err := st.UpdateWorkItem(ctx, one.ID, WorkItemPatch{Progress: ptr("继续")}, WorkActor{Kind: "agent", SessionID: "sess-bbbb"})
	if err != nil || !r.SessionLinked {
		t.Fatalf("%v", err)
	}
	if r.Item.SourceSessionID != "sess-aaaa" || r.Item.LastSessionID != "sess-bbbb" || len(r.Item.Sessions) != 2 {
		t.Fatalf("一个任务应能关联多个会话: %+v", r.Item)
	}
	if r.Item.Sessions[1].Title == "" {
		t.Fatalf("没有标题的会话应退回到摘要片段: %+v", r.Item.Sessions[1])
	}
	// 重复关联幂等
	if _, err := st.UpdateWorkItem(ctx, one.ID, WorkItemPatch{Note: "再来一条"}, WorkActor{Kind: "agent", SessionID: "sess-bbbb"}); err != nil {
		t.Fatal(err)
	}
	if got, _ := st.GetWorkItem(ctx, one.ID); len(got.Sessions) != 2 {
		t.Fatalf("重复关联应幂等: %d", len(got.Sessions))
	}
	// 不存在的会话：不报错、不关联
	res3, err := st.CreateWorkItem(ctx, WorkItemInput{WorkflowID: "w", Title: "任务三"}, WorkActor{Kind: "agent", SessionID: "sess-ghost"})
	if err != nil || res3.SessionLinked || len(res3.Item.Sessions) != 0 {
		t.Fatalf("不存在的会话不应被关联: %v %+v", err, res3)
	}

	// 手工关联 / 解除
	if err := st.LinkWorkItemSession(ctx, res3.Item.ID, "sess-ghost"); err == nil {
		t.Fatal("手工关联不存在的会话应报错")
	} else {
		wantNotFound(t, err)
	}
	if err := st.LinkWorkItemSession(ctx, res3.Item.ID, "sess-bbbb"); err != nil {
		t.Fatal(err)
	}
	if err := st.UnlinkWorkItemSession(ctx, res3.Item.ID, "sess-bbbb"); err != nil {
		t.Fatal(err)
	}
	if err := st.UnlinkWorkItemSession(ctx, res3.Item.ID, "sess-bbbb"); err == nil {
		t.Fatal("解除不存在的关联应报错")
	}

	// 列表上的会话数
	items, err := st.ListWorkItems(ctx, WorkItemFilter{WorkflowID: "w"})
	if err != nil {
		t.Fatal(err)
	}
	counts := map[string]int{}
	for _, it := range items {
		counts[it.Title] = it.SessionNum
	}
	if counts["任务一"] != 2 || counts["任务二"] != 1 || counts["任务三"] != 0 {
		t.Fatalf("列表应带关联会话数: %v", counts)
	}
	// 删除任务不影响会话
	if _, err := st.DeleteWorkItem(ctx, one.ID); err != nil {
		t.Fatal(err)
	}
	var n int64
	st.db.Model(&model.FeedbackSession{}).Count(&n)
	if n != 2 {
		t.Fatalf("删除任务不应删除会话: %d", n)
	}
	st.db.Model(&model.WorkItemSession{}).Where("work_item_id = ?", one.ID).Count(&n)
	if n != 0 {
		t.Fatalf("删除任务应清掉它的会话关联: %d", n)
	}
}

func TestWorkItemListFilters(t *testing.T) {
	st := setupTestStore(t)
	ctx := context.Background()
	mustCreateWork(t, st, "w1", "修复终端闪烁", func(in *WorkItemInput) { in.Kind = "bug"; in.Labels = []string{"终端"} })
	mustCreateWork(t, st, "w1", "新增设置跳转", func(in *WorkItemInput) { in.Kind = "feature"; in.Labels = []string{"设置"}; in.State = "doing" })
	done := mustCreateWork(t, st, "w1", "旧功能", func(in *WorkItemInput) { in.Kind = "feature" })
	mustCreateWork(t, st, "w2", "别的工作流 100%_完成", func(in *WorkItemInput) { in.Kind = "ops"; in.Progress = "进度含 100%_特殊字符" })
	if _, err := st.UpdateWorkItem(ctx, done.ID, WorkItemPatch{State: ptr("done")}, human); err != nil {
		t.Fatal(err)
	}

	titles := func(f WorkItemFilter) []string {
		items, err := st.ListWorkItems(ctx, f)
		if err != nil {
			t.Fatal(err)
		}
		out := make([]string, 0, len(items))
		for _, it := range items {
			out = append(out, it.Title)
		}
		slices.Sort(out)
		return out
	}
	if got := titles(WorkItemFilter{}); len(got) != 4 {
		t.Fatalf("不筛选应返回全部: %v", got)
	}
	if got := titles(WorkItemFilter{WorkflowID: "w1"}); len(got) != 3 {
		t.Fatalf("按工作流筛选: %v", got)
	}
	if got := titles(WorkItemFilter{WorkflowID: "w1", States: []string{"doing", "待办"}}); !slices.Equal(got, []string{"修复终端闪烁", "新增设置跳转"}) {
		t.Fatalf("按状态筛选（接受中文别名）: %v", got)
	}
	if got := titles(WorkItemFilter{States: []string{"nonsense"}}); len(got) != 0 {
		t.Fatalf("未知状态不应匹配任何任务: %v", got)
	}
	if got := titles(WorkItemFilter{Kinds: []string{"缺陷"}}); !slices.Equal(got, []string{"修复终端闪烁"}) {
		t.Fatalf("按类型筛选: %v", got)
	}
	if got := titles(WorkItemFilter{Query: "设置"}); !slices.Equal(got, []string{"新增设置跳转"}) {
		t.Fatalf("搜索应覆盖标题与标签: %v", got)
	}
	if got := titles(WorkItemFilter{Query: "终端"}); !slices.Equal(got, []string{"修复终端闪烁"}) {
		t.Fatalf("搜索标签 / 标题: %v", got)
	}
	// LIKE 通配符要按字面匹配
	if got := titles(WorkItemFilter{Query: "100%_"}); len(got) != 1 {
		t.Fatalf("%% 与 _ 应按字面匹配: %v", got)
	}
	if got := titles(WorkItemFilter{Query: "%"}); len(got) != 1 {
		t.Fatalf("单独的 %% 只应匹配含百分号的任务: %v", got)
	}
	// 关闭天数：把「旧功能」的关闭时间改到 40 天前
	old := time.Now().AddDate(0, 0, -40)
	if err := st.db.Model(&model.WorkItem{}).Where("id = ?", done.ID).UpdateColumn("closed_at", old).Error; err != nil {
		t.Fatal(err)
	}
	if got := titles(WorkItemFilter{WorkflowID: "w1", ClosedDays: 30}); slices.Contains(got, "旧功能") || len(got) != 2 {
		t.Fatalf("关闭超过 30 天的任务应被排除: %v", got)
	}
	if got := titles(WorkItemFilter{WorkflowID: "w1", ClosedDays: 60}); !slices.Contains(got, "旧功能") {
		t.Fatalf("60 天内关闭的任务应保留: %v", got)
	}

	counts, err := st.CountWorkItemsByState(ctx, "w1")
	if err != nil {
		t.Fatal(err)
	}
	if counts["todo"] != 1 || counts["doing"] != 1 || counts["done"] != 1 || counts["review"] != 0 || counts["shelved"] != 0 {
		t.Fatalf("按状态计数: %v", counts)
	}
	stats, err := st.ListWorkItemWorkflows(ctx)
	if err != nil || len(stats) != 2 {
		t.Fatalf("工作流概况: %v %+v", err, stats)
	}
	for _, s := range stats {
		if s.WorkflowID == "w1" && (s.Total != 3 || s.Active != 2 || s.Doing != 1 || s.Review != 0) {
			t.Fatalf("w1 概况: %+v", s)
		}
		if s.UpdatedAt.IsZero() {
			t.Fatalf("更新时间应被汇总: %+v", s)
		}
	}
}

func TestWorkItemCompactionKeepsWhatLocatesATask(t *testing.T) {
	st := setupTestStore(t)
	ctx := context.Background()
	now := time.Now()
	seedSession(t, st, "sess-keep", "w", "保留的会话")

	mk := func(title string, state string, closedDaysAgo int, conclusion string) *model.WorkItem {
		res, err := st.CreateWorkItem(ctx, WorkItemInput{
			WorkflowID: "w", Title: title, Kind: "bug", Labels: []string{"终端"}, Refs: []string{"commit:abc1234"}, Progress: "做完了 " + title,
		}, WorkActor{Kind: "human", SessionID: "sess-keep"})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := st.UpdateWorkItem(ctx, res.Item.ID, WorkItemPatch{Note: "中间的说明", Conclusion: ptr(conclusion)}, human); err != nil {
			t.Fatal(err)
		}
		if state != "" {
			if _, err := st.UpdateWorkItem(ctx, res.Item.ID, WorkItemPatch{State: ptr(state)}, human); err != nil {
				t.Fatal(err)
			}
			closed := now.AddDate(0, 0, -closedDaysAgo)
			if err := st.db.Model(&model.WorkItem{}).Where("id = ?", res.Item.ID).UpdateColumn("closed_at", closed).Error; err != nil {
				t.Fatal(err)
			}
		}
		return res.Item
	}
	oldNoConclusion := mk("旧-无结论", "done", 120, "")
	oldWithConclusion := mk("旧-有结论", "shelved", 100, "结论：不做了，原因是 X")
	recent := mk("近期关闭", "done", 30, "")
	open := mk("进行中", "", 0, "")

	// 记下 updated_at：压缩不应让旧任务重新排到前面
	var beforeUpdated time.Time
	st.db.Model(&model.WorkItem{}).Select("updated_at").Where("id = ?", oldNoConclusion.ID).Scan(&beforeUpdated)

	n, err := st.CompactWorkItems(ctx, now)
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("应压缩 2 个任务，得到 %d", n)
	}

	a, _ := st.GetWorkItem(ctx, oldNoConclusion.ID)
	if a.Progress != "" || len(a.Updates) != 0 {
		t.Fatalf("压缩后不应留进度与时间线: %+v", a)
	}
	if a.Conclusion != "做完了 旧-无结论" {
		t.Fatalf("没有结论的任务应从进度截取结论: %q", a.Conclusion)
	}
	if a.Title != "旧-无结论" || a.Kind != "bug" || !slices.Equal(a.Labels, []string{"终端"}) || !slices.Equal(a.Refs, []string{"commit:abc1234"}) ||
		a.ClosedAt == nil || len(a.Sessions) != 1 || a.State != "done" {
		t.Fatalf("标题 / 类型 / 标签 / 引用 / 日期 / 关联会话必须保留: %+v", a)
	}
	if !a.UpdatedAt.Equal(beforeUpdated) {
		t.Fatalf("压缩不应改 updated_at: %v → %v", beforeUpdated, a.UpdatedAt)
	}
	b, _ := st.GetWorkItem(ctx, oldWithConclusion.ID)
	if b.Conclusion != "结论：不做了，原因是 X" || b.Progress != "" || len(b.Updates) != 0 {
		t.Fatalf("已有结论必须原样保留: %+v", b)
	}
	c, _ := st.GetWorkItem(ctx, recent.ID)
	if c.Progress == "" || len(c.Updates) == 0 {
		t.Fatalf("关闭未满 90 天的任务不应被压缩")
	}
	d, _ := st.GetWorkItem(ctx, open.ID)
	if d.Progress == "" || len(d.Updates) == 0 {
		t.Fatalf("未关闭的任务不应被压缩")
	}
	// 幂等
	if n, err := st.CompactWorkItems(ctx, now); err != nil || n != 0 {
		t.Fatalf("重复压缩应无事可做: %d %v", n, err)
	}
}

func TestWorkItemLongProgressFallbackIsTruncated(t *testing.T) {
	st := setupTestStore(t)
	ctx := context.Background()
	res, err := st.CreateWorkItem(ctx, WorkItemInput{WorkflowID: "w", Title: "长进度", Progress: strings.Repeat("字", 480)}, human)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.UpdateWorkItem(ctx, res.Item.ID, WorkItemPatch{State: ptr("done")}, human); err != nil {
		t.Fatal(err)
	}
	st.db.Model(&model.WorkItem{}).Where("id = ?", res.Item.ID).UpdateColumn("closed_at", time.Now().AddDate(0, 0, -200))
	if _, err := st.CompactWorkItems(ctx, time.Now()); err != nil {
		t.Fatal(err)
	}
	got, _ := st.GetWorkItem(ctx, res.Item.ID)
	if runeLen(got.Conclusion) != model.WorkConclusionFallback {
		t.Fatalf("回退结论应截到 %d 字，得到 %d", model.WorkConclusionFallback, runeLen(got.Conclusion))
	}
}

func TestWorkItemNormalizers(t *testing.T) {
	for in, want := range map[string]string{"todo": "todo", " 进行中 ": "doing", "In_Progress": "doing", "验证中": "review", "待确认": "review", "COMPLETED": "done", "搁置": "shelved", "": "", "x": ""} {
		if got := model.NormalizeWorkState(in); got != want {
			t.Errorf("NormalizeWorkState(%q) = %q，期望 %q", in, got, want)
		}
	}
	for in, want := range map[string]string{"bug": "bug", "缺陷": "bug", "Feature": "feature", "refactor": "improve", "调研与方案": "research", "运维": "ops", "": "", "x": ""} {
		if got := model.NormalizeWorkKind(in); got != want {
			t.Errorf("NormalizeWorkKind(%q) = %q，期望 %q", in, got, want)
		}
	}
}
