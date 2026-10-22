package store

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/teacat99/RelayMesh/v2/internal/model"
)

// 共享任务板的行为测试（#175 B1a）：同一个项目的工作流共用一套 #N 与一棵规划树。

func boardTask(t *testing.T, st *Store, wf, title string) *model.WorkItem {
	t.Helper()
	res, err := st.CreateWorkItem(context.Background(), WorkItemInput{WorkflowID: wf, Title: title}, WorkActor{Kind: model.WorkActorHuman})
	if err != nil {
		t.Fatalf("%s 建任务「%s」失败: %v", wf, title, err)
	}
	return res.Item
}

// 同一个项目里的两个工作流共用一套序号：交替创建得到连续的 #1 #2 #3，互相看得到、按序号读得到。
func TestSharedBoardNumberingAcrossWorkflows(t *testing.T) {
	st, ctx, a := boardFixture(t)
	t1 := boardTask(t, st, "wf-1", "甲")
	t2 := boardTask(t, st, "wf-2", "乙")
	t3 := boardTask(t, st, "wf-1", "丙")
	for i, it := range []*model.WorkItem{t1, t2, t3} {
		if it.Seq != i+1 || it.BoardKey != ProjectBoardKey(a.ID) {
			t.Fatalf("第 %d 条应是项目板 #%d: seq=%d board=%s", i+1, i+1, it.Seq, it.BoardKey)
		}
	}
	if t2.WorkflowID != "wf-2" {
		t.Fatalf("workflow_id 是创建者: %s", t2.WorkflowID)
	}
	// wf-2 按序号读到 wf-1 创建的 #1。
	got, err := st.GetWorkItemBySeq(ctx, "wf-2", 1)
	if err != nil || got.ID != t1.ID || got.Title != "甲" {
		t.Fatalf("wf-2 应读到项目板的 #1: %+v err=%v", got, err)
	}
	// 两个工作流列出同一份清单；项目外的工作流看不到。
	for _, wf := range []string{"wf-1", "wf-2"} {
		list, err := st.ListWorkItems(ctx, WorkItemFilter{WorkflowID: wf})
		if err != nil || len(list) != 3 {
			t.Fatalf("%s 应看到项目板的 3 条: %d err=%v", wf, len(list), err)
		}
	}
	if list, err := st.ListWorkItems(ctx, WorkItemFilter{WorkflowID: "wf-solo"}); err != nil || len(list) != 0 {
		t.Fatalf("项目外的工作流不应看到项目板: %d err=%v", len(list), err)
	}
	// 按板统计 = 整张板；按创建者统计 = 自己的。
	boardCounts, err := st.CountBoardWorkItemsByState(ctx, "wf-2")
	if err != nil || boardCounts[model.WorkStateTodo] != 3 {
		t.Fatalf("板统计应是 3: %v err=%v", boardCounts, err)
	}
	own, err := st.CountWorkItemsByState(ctx, "wf-2")
	if err != nil || own[model.WorkStateTodo] != 1 {
		t.Fatalf("创建者统计应是 1: %v err=%v", own, err)
	}
	// 项目外的工作流自己一张板，序号从 #1 起，互不影响。
	solo := boardTask(t, st, "wf-solo", "独")
	if solo.Seq != 1 || solo.BoardKey != "w:wf-solo" {
		t.Fatalf("独立板应从 #1 起: seq=%d board=%s", solo.Seq, solo.BoardKey)
	}
	if it := boardTask(t, st, "wf-2", "丁"); it.Seq != 4 {
		t.Fatalf("项目板继续发 #4: %d", it.Seq)
	}
}

// 已经有任务的工作流，目录挪走之后仍留在原来的板（粘性）；新工作流按当前目录归属。
func TestBoardStaysStickyAfterDirectoryChange(t *testing.T) {
	st, ctx, a := boardFixture(t)
	boardTask(t, st, "wf-1", "项目里的")
	seedWorkflowDir(t, st, "wf-1", "devhost", "/tmp/other", time.Now().Add(time.Minute))
	it := boardTask(t, st, "wf-1", "目录挪走之后")
	if it.BoardKey != ProjectBoardKey(a.ID) || it.Seq != 2 {
		t.Fatalf("应仍在项目板 #2: seq=%d board=%s", it.Seq, it.BoardKey)
	}
	list, err := st.ListWorkItems(ctx, WorkItemFilter{WorkflowID: "wf-1"})
	if err != nil || len(list) != 2 {
		t.Fatalf("应还看得到两条: %d err=%v", len(list), err)
	}
	// 目录一开始就在项目外的新工作流：独立板。
	seedWorkflowDir(t, st, "wf-new", "devhost", "/tmp/other", time.Now())
	if n := boardTask(t, st, "wf-new", "新"); n.BoardKey != "w:wf-new" || n.Seq != 1 {
		t.Fatalf("新工作流在项目外应是独立板: %+v", n)
	}
	// 反过来：没有任务的工作流之后进入项目目录，下一条任务就归项目板。
	seedWorkflowDir(t, st, "wf-late", "devhost", "/tmp/other", time.Now().Add(-time.Hour))
	seedWorkflowDir(t, st, "wf-late", "devhost", "/home/user/a", time.Now())
	if n := boardTask(t, st, "wf-late", "晚到"); n.BoardKey != ProjectBoardKey(a.ID) || n.Seq != 3 {
		t.Fatalf("进入项目目录后应归项目板 #3: %+v", n)
	}
}

// 规划树是板的：一个工作流建的模块，另一个工作流看得到、能把任务归进去、能在同一棵树上继续规划；别的板的模块不能用。
func TestSharedPlanTreeAcrossWorkflows(t *testing.T) {
	st, ctx, _ := boardFixture(t)
	backend, err := st.CreateWorkModule(ctx, "wf-1", 0, "后端", "")
	if err != nil {
		t.Fatal(err)
	}
	if backend.BoardKey == "" || backend.WorkflowID != "wf-1" {
		t.Fatalf("节点属于板、记创建者: %+v", backend)
	}
	l2, err := st.ListWorkModules(ctx, "wf-2")
	if err != nil || len(l2.Modules) != 1 || l2.Modules[0].Name != "后端" {
		t.Fatalf("wf-2 应看到 wf-1 建的模块: %+v err=%v", l2, err)
	}
	res, err := st.CreateWorkItem(ctx, WorkItemInput{WorkflowID: "wf-2", Title: "归进去", ModuleID: backend.ID}, WorkActor{Kind: model.WorkActorHuman})
	if err != nil || res.Item.ModulePath != "后端" {
		t.Fatalf("wf-2 应能把任务归进共享的模块: %+v err=%v", res, err)
	}
	// wf-2 在同一棵树上追加功能；重复提交幂等；wf-1 看得到。
	plan := []WorkPlanNode{{Name: "后端", Features: []WorkPlanNode{{Name: "存储"}}}}
	pr, err := st.ApplyWorkPlan(ctx, "wf-2", plan, false)
	if err != nil || len(pr.Created) != 1 || pr.Created[0] != "后端/存储" {
		t.Fatalf("应只新增「后端/存储」: %+v err=%v", pr, err)
	}
	if pr, err = st.ApplyWorkPlan(ctx, "wf-1", plan, false); err != nil || pr.Changed() {
		t.Fatalf("同一份计划从另一个工作流提交应幂等: %+v err=%v", pr, err)
	}
	l1, err := st.ListWorkModules(ctx, "wf-1")
	if err != nil || len(l1.Modules) != 2 {
		t.Fatalf("wf-1 应看到 2 个节点: %+v err=%v", l1, err)
	}
	if got, err := st.ResolveWorkModule(ctx, "wf-2", "后端/存储"); err != nil || got.WorkflowID != "wf-2" {
		t.Fatalf("按路径解析应能找到 wf-2 建的功能: %+v err=%v", got, err)
	}
	if n, err := st.CountWorkModules(ctx, "wf-1"); err != nil || n != 2 {
		t.Fatalf("节点数按板统计: %d err=%v", n, err)
	}
	// 项目外工作流的同名模块是另一张板的：可以同名，但不能拿来归项目板的任务。
	other, err := st.CreateWorkModule(ctx, "wf-solo", 0, "后端", "")
	if err != nil {
		t.Fatalf("别的板可以有同名模块: %v", err)
	}
	_, err = st.CreateWorkItem(ctx, WorkItemInput{WorkflowID: "wf-1", Title: "串板", ModuleID: other.ID}, WorkActor{Kind: model.WorkActorHuman})
	wantAppErr(t, err, "模块必须属于任务所在的任务板")
	// 按模块 id 读 / 改 / 移动 / 删除都落在模块自己的板上。
	if m, err := st.GetWorkModule(ctx, backend.ID); err != nil || m.Path != "后端" {
		t.Fatalf("读模块: %+v err=%v", m, err)
	}
	name := "服务端"
	if _, err := st.UpdateWorkModule(ctx, backend.ID, WorkModulePatch{Name: &name}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.MoveWorkModule(ctx, backend.ID, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DeleteWorkModule(ctx, other.ID); err != nil {
		t.Fatal(err)
	}
	if l, _ := st.ListWorkModules(ctx, "wf-1"); len(l.Modules) != 2 || l.Modules[0].Name != "服务端" {
		t.Fatalf("改名应在共享树上生效，删别的板的模块不影响: %+v", l)
	}
}

// 父子任务可以跨工作流（同一张板内）；跨板不行。
func TestParentMustBeOnSameBoard(t *testing.T) {
	st, ctx, _ := boardFixture(t)
	parent := boardTask(t, st, "wf-1", "父")
	res, err := st.CreateWorkItem(ctx, WorkItemInput{WorkflowID: "wf-2", Title: "子", ParentID: parent.ID}, WorkActor{Kind: model.WorkActorHuman})
	if err != nil || res.Item.ParentSeq != parent.Seq {
		t.Fatalf("同一张板内应允许跨工作流的父子: %+v err=%v", res, err)
	}
	solo := boardTask(t, st, "wf-solo", "别的板")
	_, err = st.CreateWorkItem(ctx, WorkItemInput{WorkflowID: "wf-1", Title: "串板", ParentID: solo.ID}, WorkActor{Kind: model.WorkActorHuman})
	wantAppErr(t, err, "父任务必须在同一张任务板里")
	// 独立板上的提示保持原样。
	other := boardTask(t, st, "wf-solo", "独立板上的子任务")
	_, err = st.UpdateWorkItem(ctx, other.ID, WorkItemPatch{ParentID: &parent.ID}, WorkActor{Kind: model.WorkActorHuman})
	wantAppErr(t, err, "父任务必须在同一个工作流里")
	// 在板内更新：把 wf-1 的任务挂到 wf-2 的任务下。
	loose := boardTask(t, st, "wf-1", "待挂靠")
	if _, err := st.UpdateWorkItem(ctx, loose.ID, WorkItemPatch{ParentID: &res.Item.ID}, WorkActor{Kind: model.WorkActorHuman}); err == nil {
		t.Fatal("子任务不能再当父任务（只支持一层嵌套）")
	}
	if _, err := st.UpdateWorkItem(ctx, loose.ID, WorkItemPatch{ParentID: &parent.ID}, WorkActor{Kind: model.WorkActorHuman}); err != nil {
		t.Fatalf("同板更新父任务应成功: %v", err)
	}
}

// 唯一索引：同一张板里序号不能重；旧行（board_key 为空）不受这条索引约束，仍只受旧的 (工作流, 序号) 索引管。
func TestBoardSeqUniqueIndex(t *testing.T) {
	st, _, _ := boardFixture(t)
	mk := func(wf, board string, seq int) error {
		now := time.Now()
		return st.db.Create(&model.WorkItem{WorkflowID: wf, BoardKey: board, Seq: seq, Title: "t", Kind: model.WorkKindFeature,
			State: model.WorkStateTodo, CreatedBy: model.WorkActorHuman, CreatedAt: now, UpdatedAt: now}).Error
	}
	if err := mk("wf-1", "p1", 1); err != nil {
		t.Fatal(err)
	}
	if err := mk("wf-2", "p1", 1); err == nil {
		t.Fatal("同一张板里序号重复应被唯一索引拒绝")
	}
	if err := mk("wf-2", "p2", 1); err != nil {
		t.Fatalf("不同板可以同号: %v", err)
	}
	if err := mk("wf-1", "", 7); err != nil {
		t.Fatal(err)
	}
	if err := mk("wf-2", "", 7); err != nil {
		t.Fatalf("未归板的旧行不受板内唯一索引约束: %v", err)
	}
	if err := mk("wf-2", "", 7); err == nil {
		t.Fatal("旧的 (工作流, 序号) 唯一索引仍应生效")
	}
}

// 创建者的旧计数写穿：回滚到旧版本后它按旧计数发号，不会倒回已经发过的号。
func TestOldCounterWriteThrough(t *testing.T) {
	st, _, _ := boardFixture(t)
	boardTask(t, st, "wf-1", "1")
	boardTask(t, st, "wf-2", "2")
	boardTask(t, st, "wf-1", "3")
	boardTask(t, st, "wf-2", "4")
	counters := map[string]int{}
	var rows []model.WorkItemCounter
	st.db.Find(&rows)
	for _, c := range rows {
		counters[c.WorkflowID] = c.LastSeq
	}
	if counters["wf-1"] != 3 || counters["wf-2"] != 4 {
		t.Fatalf("旧计数应记各创建者用过的最大号: %v", counters)
	}
}

// 合并时改过号的任务：旧 (工作流, 序号) 别名在原序号查不到时指向新任务；游离的旧行（没有 board_key）也读得到。
func TestGetWorkItemBySeqFallbacks(t *testing.T) {
	st, ctx, _ := boardFixture(t)
	it := boardTask(t, st, "wf-2", "改过号的任务") // 项目板 #1
	if err := st.db.Create(&model.WorkItemAlias{OldWorkflowID: "wf-2", OldSeq: 7, WorkItemID: it.ID, CreatedAt: time.Now()}).Error; err != nil {
		t.Fatal(err)
	}
	got, err := st.GetWorkItemBySeq(ctx, "wf-2", 7)
	if err != nil || got.ID != it.ID {
		t.Fatalf("别名应指向新任务: %+v err=%v", got, err)
	}
	// 别名只对原来的工作流有效。
	if _, err := st.GetWorkItemBySeq(ctx, "wf-1", 7); err == nil {
		t.Fatal("别名不应对别的工作流生效")
	}
	// 板内真实存在的序号优先于别名。
	if got, err := st.GetWorkItemBySeq(ctx, "wf-2", 1); err != nil || got.ID != it.ID {
		t.Fatalf("板内序号优先: %+v err=%v", got, err)
	}
	// 游离的旧行。
	ids := legacyTasks(t, st, "wf-solo", 0, 5)
	if got, err := st.GetWorkItemBySeq(ctx, "wf-solo", 5); err != nil || got.ID != ids[0] {
		t.Fatalf("没有 board_key 的旧行也应读得到: %+v err=%v", got, err)
	}
	// 删除任务同时清掉它的别名。
	if _, err := st.DeleteWorkItem(ctx, it.ID); err != nil {
		t.Fatal(err)
	}
	var n int64
	st.db.Model(&model.WorkItemAlias{}).Count(&n)
	if n != 0 {
		t.Fatalf("删除任务应清掉别名: %d", n)
	}
	_, err = st.GetWorkItemBySeq(ctx, "wf-2", 7)
	wantAppErr(t, err, "项目任务板里没有 #7")
}

// 两个工作流同时往同一张板里建任务：序号连续不重复，也不会把单连接的 SQLite 卡死。
func TestSharedBoardConcurrentCreate(t *testing.T) {
	st, ctx, _ := boardFixture(t)
	const per = 12
	var wg sync.WaitGroup
	errs := make(chan error, 2*per)
	for _, wf := range []string{"wf-1", "wf-2"} {
		wf := wf
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < per; i++ {
				if _, err := st.CreateWorkItem(ctx, WorkItemInput{WorkflowID: wf, Title: fmt.Sprintf("%s-%d", wf, i)}, WorkActor{Kind: model.WorkActorHuman}); err != nil {
					errs <- err
					return
				}
			}
		}()
	}
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(60 * time.Second):
		t.Fatal("并发创建超时，疑似死锁")
	}
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	var seqs []int
	st.db.Model(&model.WorkItem{}).Order("seq").Pluck("seq", &seqs)
	if len(seqs) != 2*per {
		t.Fatalf("应建出 %d 条: %d", 2*per, len(seqs))
	}
	for i, s := range seqs {
		if s != i+1 {
			t.Fatalf("序号应连续 1..%d: %v", 2*per, seqs)
		}
	}
}

// 工作流列表：同一张板上的工作流各占一条、数字是整张板的，并带 board_key 与成员；单工作流的板不带成员。
func TestListWorkItemWorkflowsBoardStats(t *testing.T) {
	st, ctx, a := boardFixture(t)
	boardTask(t, st, "wf-1", "甲")
	boardTask(t, st, "wf-2", "乙")
	b3 := boardTask(t, st, "wf-2", "丙")
	if _, err := st.UpdateWorkItem(ctx, b3.ID, WorkItemPatch{State: ptrOf(model.WorkStateDoing)}, WorkActor{Kind: model.WorkActorAgent}); err != nil {
		t.Fatal(err)
	}
	boardTask(t, st, "wf-solo", "独")

	stats, err := st.ListWorkItemWorkflows(ctx)
	if err != nil {
		t.Fatal(err)
	}
	by := map[string]WorkflowStat{}
	for _, s := range stats {
		by[s.WorkflowID] = s
	}
	if len(stats) != 3 {
		t.Fatalf("应有 3 条（wf-1 wf-2 wf-solo）: %+v", stats)
	}
	for _, wf := range []string{"wf-1", "wf-2"} {
		s := by[wf]
		if s.Total != 3 || s.Active != 3 || s.Doing != 1 || s.BoardKey != ProjectBoardKey(a.ID) || len(s.Members) != 2 {
			t.Errorf("%s 应是整张项目板的数字: %+v", wf, s)
		}
		if s.BoardName != a.Name {
			t.Errorf("%s: 共用的项目板应带项目名 %q: %+v", wf, a.Name, s)
		}
	}
	if s := by["wf-solo"]; s.Total != 1 || s.BoardKey != "w:wf-solo" || len(s.Members) != 0 || s.BoardName != "" {
		t.Errorf("独立板不带成员与板名: %+v", s)
	}
}

// 项目板只有一个工作流有任务时没有「共用」，不带板名（界面照旧用会话标题），也不为它查项目表。
// 任务清单一次最多返回多少条：没指定或不合理（≤0、>1000）取默认 500；REST 用它判断清单有没有被截断。
func TestWorkItemListLimit(t *testing.T) {
	for _, c := range []struct{ in, want int }{{0, 500}, {-3, 500}, {1, 1}, {250, 250}, {1000, 1000}, {1001, 500}} {
		if got := WorkItemListLimit(c.in); got != c.want {
			t.Errorf("WorkItemListLimit(%d) = %d，期望 %d", c.in, got, c.want)
		}
	}
}

func TestListWorkItemWorkflowsBoardNameOnlyWhenShared(t *testing.T) {
	st, ctx, _ := boardFixture(t)
	boardTask(t, st, "wf-1", "甲")
	stats, err := st.ListWorkItemWorkflows(ctx)
	if err != nil || len(stats) != 1 {
		t.Fatalf("只有一条概况: %+v err=%v", stats, err)
	}
	if s := stats[0]; len(s.Members) != 0 || s.BoardName != "" {
		t.Fatalf("单工作流的项目板不带成员与板名: %+v", s)
	}
	// 第二个工作流也建了任务之后才共用，板名跟着出现。
	boardTask(t, st, "wf-2", "乙")
	stats, err = st.ListWorkItemWorkflows(ctx)
	if err != nil || len(stats) != 2 {
		t.Fatalf("两条概况: %+v err=%v", stats, err)
	}
	for _, s := range stats {
		if s.BoardName != "A" || len(s.Members) != 2 {
			t.Fatalf("共用之后应带板名 A 与成员: %+v", s)
		}
	}
}

// 回执头与概览用的摘要是整张板的：各状态数量、规划节点数、未归类数与最近更新的未结束任务。
func TestWorkDigestForIsBoardWide(t *testing.T) {
	st, ctx, _ := boardFixture(t)
	if _, err := st.CreateWorkModule(ctx, "wf-1", 0, "后端", ""); err != nil {
		t.Fatal(err)
	}
	boardTask(t, st, "wf-1", "甲")
	last := boardTask(t, st, "wf-2", "乙")
	d, err := st.WorkDigestFor(ctx, "wf-1")
	if err != nil {
		t.Fatal(err)
	}
	if d.Total != 2 || d.Modules != 1 || d.Unassigned != 2 || d.Latest == nil || d.Latest.ID != last.ID {
		t.Fatalf("摘要应是整张板的: total=%d modules=%d unassigned=%d latest=%+v", d.Total, d.Modules, d.Unassigned, d.Latest)
	}
	d2, err := st.WorkDigestFor(ctx, "wf-2")
	if err != nil || d2.Total != 2 {
		t.Fatalf("另一个成员看到同一份摘要: %+v err=%v", d2, err)
	}
	if _, err := st.WorkDigestFor(ctx, " "); err == nil {
		t.Fatal("空工作流应报错")
	}
}

func ptrOf[T any](v T) *T { return &v }
