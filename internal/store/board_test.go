package store

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/teacat99/RelayMesh/v2/internal/model"
)

// 任务板的规则与迁移规划的测试（#175 B0）。

func TestBoardKeyHelpers(t *testing.T) {
	if got := ProjectBoardKey(12); got != "p12" {
		t.Fatalf("项目板 key: %q", got)
	}
	if got := WorkflowBoardKey("wf-1"); got != "w:wf-1" {
		t.Fatalf("独立板 key: %q", got)
	}
	cases := []struct {
		key  string
		proj uint
		wf   string
		ok   bool
	}{
		{"p12", 12, "", true},
		{"w:wf-1", 0, "wf-1", true},
		{"w:a:b", 0, "a:b", true}, // 工作流编号里带冒号不影响拆分
		{"p0", 0, "", false},
		{"p", 0, "", false},
		{"px", 0, "", false},
		{"w:", 0, "", false},
		{"", 0, "", false},
		{"12", 0, "", false},
	}
	for _, c := range cases {
		proj, wf, ok := ParseBoardKey(c.key)
		if proj != c.proj || wf != c.wf || ok != c.ok {
			t.Errorf("ParseBoardKey(%q) = (%d, %q, %v)，期望 (%d, %q, %v)", c.key, proj, wf, ok, c.proj, c.wf, c.ok)
		}
	}
	// 往返：拼出来的 key 一定能拆回去。
	if p, _, ok := ParseBoardKey(ProjectBoardKey(7)); !ok || p != 7 {
		t.Fatal("项目板 key 往返失败")
	}
	if _, w, ok := ParseBoardKey(WorkflowBoardKey("x y")); !ok || w != "x y" {
		t.Fatal("独立板 key 往返失败")
	}
}

func TestResolveBoardByDirectory(t *testing.T) {
	st, ctx := projTestStore(t)
	a, _ := st.CreateProject(ctx, ProjectInput{Name: "A", Slug: "a"})
	b, _ := st.CreateProject(ctx, ProjectInput{Name: "B", Slug: "b"})
	if _, err := st.AddProjectDir(ctx, a.ID, "devhost", "/home/user/a", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := st.AddProjectDir(ctx, b.ID, "devhost", "/home/user/b", ""); err != nil {
		t.Fatal(err)
	}
	if err := st.ArchiveProject(ctx, b.ID); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	seedWorkflowDir(t, st, "wf-a", "DEVHOST", "/home/user/a/", now) // 主机大小写、目录末尾斜杠都不影响命中
	seedWorkflowDir(t, st, "wf-b", "devhost", "/home/user/b", now)
	seedWorkflowDir(t, st, "wf-x", "devhost", "/tmp/other", now)
	// 以最近一条带目录的会话为准：先在项目里、后来挪走 = 独立板；反过来 = 项目板。
	seedWorkflowDir(t, st, "wf-left", "devhost", "/home/user/a", now.Add(-time.Hour))
	seedWorkflowDir(t, st, "wf-left", "devhost", "/tmp/other", now)
	seedWorkflowDir(t, st, "wf-joined", "devhost", "/tmp/other", now.Add(-time.Hour))
	seedWorkflowDir(t, st, "wf-joined", "devhost", "/home/user/a", now)

	cases := []struct{ wf, key, reason string }{
		{"wf-a", ProjectBoardKey(a.ID), "属于项目 a"},
		{"wf-b", WorkflowBoardKey("wf-b"), "已归档的项目 b"},
		{"wf-x", WorkflowBoardKey("wf-x"), "不属于任何项目"},
		{"wf-none", WorkflowBoardKey("wf-none"), "没有带目录"},
		{"wf-left", WorkflowBoardKey("wf-left"), "不属于任何项目"},
		{"wf-joined", ProjectBoardKey(a.ID), "属于项目 a"},
	}
	for _, c := range cases {
		info, err := st.resolveBoard(ctx, c.wf)
		if err != nil {
			t.Fatalf("%s: %v", c.wf, err)
		}
		if info.Key != c.key || !strings.Contains(info.Reason, c.reason) {
			t.Errorf("%s: key=%q reason=%q，期望 key=%q 且说明含 %q", c.wf, info.Key, info.Reason, c.key, c.reason)
		}
		if (info.Project != nil) != strings.HasPrefix(c.key, "p") {
			t.Errorf("%s: Project 字段与板类型不一致: %+v", c.wf, info.Project)
		}
	}
	if _, err := st.resolveBoard(ctx, "  "); err == nil {
		t.Fatal("空工作流编号应报错")
	}
	// 解析结果与现有的 ProjectOfWorkflow 完全一致（它是源头规则）。
	for _, wf := range []string{"wf-a", "wf-b", "wf-x", "wf-none", "wf-left", "wf-joined"} {
		info, _ := st.resolveBoard(ctx, wf)
		p, err := st.ProjectOfWorkflow(ctx, wf)
		if err != nil {
			t.Fatal(err)
		}
		if (p != nil) != (info.Project != nil) || (p != nil && p.ID != info.Project.ID) {
			t.Errorf("%s: resolveBoard 与 ProjectOfWorkflow 不一致: %+v vs %+v", wf, info.Project, p)
		}
	}
}

// legacyTasks 按旧版本的方式给工作流写任务：不带 board_key，序号由调用者指定（各工作流各自从 1 起），
// 并写旧的按工作流计数。新版本的创建路径已经按板发号，造不出「两个工作流序号重叠」的存量，
// 所以迁移 / 预演类测试直接写库来模拟迁移前的数据。返回各任务的 id（与 seqs 同序）。
func legacyTasks(t *testing.T, st *Store, wf string, moduleID uint, seqs ...int) []uint {
	t.Helper()
	now := time.Now()
	var ids []uint
	maxSeq := 0
	for _, seq := range seqs {
		it := &model.WorkItem{WorkflowID: wf, Seq: seq, Title: fmt.Sprintf("%s-任务%d", wf, seq), Kind: model.WorkKindFeature,
			State: model.WorkStateTodo, CreatedBy: model.WorkActorHuman, CreatedAt: now, UpdatedAt: now}
		if moduleID != 0 {
			m := moduleID
			it.ModuleID = &m
		}
		if err := st.db.Create(it).Error; err != nil {
			t.Fatal(err)
		}
		ids = append(ids, it.ID)
		if seq > maxSeq {
			maxSeq = seq
		}
	}
	var c model.WorkItemCounter
	if err := st.db.Where("workflow_id = ?", wf).First(&c).Error; err == nil {
		if maxSeq > c.LastSeq {
			if err := st.db.Model(&c).Update("last_seq", maxSeq).Error; err != nil {
				t.Fatal(err)
			}
		}
	} else if err := st.db.Create(&model.WorkItemCounter{WorkflowID: wf, LastSeq: maxSeq}).Error; err != nil {
		t.Fatal(err)
	}
	return ids
}

// legacyModule 按旧版本的方式写规划节点：不带 board_key，创建者 = wf。
func legacyModule(t *testing.T, st *Store, wf string, parent uint, name string) *model.WorkModule {
	t.Helper()
	m := &model.WorkModule{WorkflowID: wf, Name: name}
	if parent != 0 {
		p := parent
		m.ParentID = &p
	}
	if err := st.db.Create(m).Error; err != nil {
		t.Fatal(err)
	}
	return m
}

// setTasksCreatedAt 把工作流所有任务的创建时间改成 at，用来控制「谁更早」。
func setTasksCreatedAt(t *testing.T, st *Store, wf string, at time.Time) {
	t.Helper()
	if err := st.db.Model(&model.WorkItem{}).Where("workflow_id = ?", wf).Update("created_at", at).Error; err != nil {
		t.Fatal(err)
	}
}

// boardFixture：项目 a（目录 /home/user/a）里有 wf-1、wf-2，目录外还有 wf-solo 与没有会话的 wf-nodir。
func boardFixture(t *testing.T) (*Store, context.Context, *model.Project) {
	t.Helper()
	st, ctx := projTestStore(t)
	a, err := st.CreateProject(ctx, ProjectInput{Name: "A", Slug: "a"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.AddProjectDir(ctx, a.ID, "devhost", "/home/user/a", ""); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	seedWorkflowDir(t, st, "wf-1", "devhost", "/home/user/a", now)
	seedWorkflowDir(t, st, "wf-2", "devhost", "/home/user/a", now)
	seedWorkflowDir(t, st, "wf-solo", "devhost", "/tmp/other", now)
	return st, ctx, a
}

func boardByKey(plan *BoardMergePlan, key string) *BoardMergeBoard {
	for i := range plan.Boards {
		if plan.Boards[i].Key == key {
			return &plan.Boards[i]
		}
	}
	return nil
}

func TestPlanBoardMergeEmptyDB(t *testing.T) {
	st, ctx := projTestStore(t)
	plan, err := PlanBoardMerge(ctx, st.db)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Items != 0 || len(plan.Boards) != 0 || len(plan.Workflows) != 0 || len(plan.Collisions) != 0 || len(plan.Renumbers) != 0 {
		t.Fatalf("空库应得到空计划: %+v", plan)
	}
}

// 存量里一个工作流一张板：不冲突、不改号，计数取各工作流旧计数与最大序号的较大者；预演不写任何东西。
func TestPlanBoardMergeSingleWorkflowBoards(t *testing.T) {
	st, ctx, a := boardFixture(t)
	legacyTasks(t, st, "wf-1", 0, 1, 2, 3)
	legacyTasks(t, st, "wf-solo", 0, 1, 2)
	legacyTasks(t, st, "wf-nodir", 0, 1)

	countRows := func() string {
		var items, sumSeq, cols int64
		st.db.Model(&model.WorkItem{}).Count(&items)
		st.db.Model(&model.WorkItem{}).Select("COALESCE(SUM(seq), 0)").Scan(&sumSeq)
		st.db.Raw("SELECT COUNT(*) FROM pragma_table_info('work_items')").Scan(&cols)
		return fmt.Sprintf("%d/%d/%d", items, sumSeq, cols)
	}
	before := countRows()
	plan, err := PlanBoardMerge(ctx, st.db)
	if err != nil {
		t.Fatal(err)
	}
	if after := countRows(); after != before {
		t.Fatalf("预演不应改库: %s -> %s", before, after)
	}

	if plan.Items != 6 || len(plan.Workflows) != 3 || len(plan.Boards) != 3 {
		t.Fatalf("规模不对: items=%d workflows=%d boards=%d", plan.Items, len(plan.Workflows), len(plan.Boards))
	}
	if len(plan.Collisions) != 0 || len(plan.Renumbers) != 0 || len(plan.ModuleDups) != 0 {
		t.Fatalf("单工作流板不应有冲突 / 改号 / 重复节点: %+v", plan)
	}
	want := map[string]struct{ tasks, counter int }{
		ProjectBoardKey(a.ID):        {3, 3},
		WorkflowBoardKey("wf-solo"):  {2, 2},
		WorkflowBoardKey("wf-nodir"): {1, 1},
	}
	for key, w := range want {
		b := boardByKey(plan, key)
		if b == nil {
			t.Fatalf("缺板 %s: %+v", key, plan.Boards)
		}
		if b.Tasks != w.tasks || b.Counter != w.counter || b.Renumbered != 0 {
			t.Errorf("板 %s: %+v，期望任务 %d 计数 %d", key, b, w.tasks, w.counter)
		}
	}
	if pb := boardByKey(plan, ProjectBoardKey(a.ID)); pb.ProjectSlug != "a" || pb.ACLMode != model.ACLAudit || len(pb.Workflows) != 1 || pb.Workflows[0] != "wf-1" {
		t.Errorf("项目板信息不对: %+v", pb)
	}
	var warned bool
	for _, w := range plan.Warnings {
		if strings.Contains(w, "wf-nodir") && strings.Contains(w, "没有带目录") {
			warned = true
		}
	}
	if !warned {
		t.Errorf("没有会话目录的工作流应给警告: %v", plan.Warnings)
	}
}

// 同一项目里两个工作流序号重叠：排在前面（更早）的保留全部序号，后来者只重编冲突的那几条，接在板内最大序号之后。
func TestPlanBoardMergeCollisionsRenumberOnlyConflicts(t *testing.T) {
	st, ctx, a := boardFixture(t)
	legacyTasks(t, st, "wf-1", 0, 1, 2, 3)         // #1 #2 #3
	ids2 := legacyTasks(t, st, "wf-2", 0, 2, 3, 4) // wf-2 只有 #2 #3 #4（#1 已删），计数是 4
	now := time.Now()
	setTasksCreatedAt(t, st, "wf-1", now.Add(-2*time.Hour))
	setTasksCreatedAt(t, st, "wf-2", now.Add(-time.Hour))
	key := ProjectBoardKey(a.ID)

	plan, err := PlanBoardMerge(ctx, st.db)
	if err != nil {
		t.Fatal(err)
	}
	b := boardByKey(plan, key)
	if b == nil || strings.Join(b.Workflows, ",") != "wf-1,wf-2" {
		t.Fatalf("板与成员顺序不对（wf-1 更早）: %+v", plan.Boards)
	}
	if b.Tasks != 6 || b.Counter != 6 || b.Renumbered != 2 {
		t.Fatalf("板汇总不对: %+v（应 6 条任务、计数 6、改号 2 条）", b)
	}
	if len(plan.Collisions) != 2 || plan.Collisions[0].Seq != 2 || plan.Collisions[1].Seq != 3 {
		t.Fatalf("冲突应是 #2 #3: %+v", plan.Collisions)
	}
	for _, c := range plan.Collisions {
		if len(c.Items) != 2 || c.Items[0].WorkflowID != "wf-1" || c.Items[1].WorkflowID != "wf-2" {
			t.Errorf("冲突条目应是 wf-1 与 wf-2 各一条（更早的在前）: %+v", c)
		}
	}
	if len(plan.Renumbers) != 2 {
		t.Fatalf("应只重编 2 条: %+v", plan.Renumbers)
	}
	r0, r1 := plan.Renumbers[0], plan.Renumbers[1]
	if r0.WorkflowID != "wf-2" || r0.OldSeq != 2 || r0.NewSeq != 5 || r0.ItemID != ids2[0] ||
		r1.WorkflowID != "wf-2" || r1.OldSeq != 3 || r1.NewSeq != 6 || r1.ItemID != ids2[1] {
		t.Fatalf("改号动作不对: %+v", plan.Renumbers)
	}

	// 反过来：wf-2 更早 → wf-2 保留 #2 #3 #4，wf-1 的 #1 不冲突保留，#2 #3 改成 #5 #6。
	setTasksCreatedAt(t, st, "wf-1", now.Add(-time.Hour))
	setTasksCreatedAt(t, st, "wf-2", now.Add(-2*time.Hour))
	plan, err = PlanBoardMerge(ctx, st.db)
	if err != nil {
		t.Fatal(err)
	}
	b = boardByKey(plan, key)
	if strings.Join(b.Workflows, ",") != "wf-2,wf-1" || b.Counter != 6 || b.Renumbered != 2 {
		t.Fatalf("反过来的汇总不对: %+v", b)
	}
	if len(plan.Renumbers) != 2 || plan.Renumbers[0].WorkflowID != "wf-1" || plan.Renumbers[0].OldSeq != 2 || plan.Renumbers[0].NewSeq != 5 ||
		plan.Renumbers[1].OldSeq != 3 || plan.Renumbers[1].NewSeq != 6 {
		t.Fatalf("反过来的改号不对: %+v", plan.Renumbers)
	}

	// 创建时间完全相同：按工作流编号稳定排序，结果可重复。
	same := now.Add(-3 * time.Hour)
	setTasksCreatedAt(t, st, "wf-1", same)
	setTasksCreatedAt(t, st, "wf-2", same)
	for i := 0; i < 3; i++ {
		plan, err = PlanBoardMerge(ctx, st.db)
		if err != nil {
			t.Fatal(err)
		}
		if got := strings.Join(boardByKey(plan, key).Workflows, ","); got != "wf-1,wf-2" {
			t.Fatalf("同一时间应按编号排序，得到 %s", got)
		}
	}
}

// 板内没有冲突的多工作流：一条都不改，计数 = 板内最大序号与旧计数的较大者。
func TestPlanBoardMergeNoCollisionKeepsEverything(t *testing.T) {
	st, ctx, a := boardFixture(t)
	legacyTasks(t, st, "wf-1", 0, 1, 2)    // #1 #2
	legacyTasks(t, st, "wf-2", 0, 3, 4, 5) // wf-2 删掉了 #1 #2，剩 #3 #4 #5；与 wf-1 的 #1 #2 不冲突
	plan, err := PlanBoardMerge(ctx, st.db)
	if err != nil {
		t.Fatal(err)
	}
	b := boardByKey(plan, ProjectBoardKey(a.ID))
	if b == nil || len(b.Workflows) != 2 || b.Tasks != 5 || b.Counter != 5 || b.Renumbered != 0 {
		t.Fatalf("不冲突的板不应改号: %+v", b)
	}
	if len(plan.Collisions) != 0 || len(plan.Renumbers) != 0 {
		t.Fatalf("不应有冲突: %+v %+v", plan.Collisions, plan.Renumbers)
	}
}

// 同一张板里路径相同的规划节点要合并；别的板里的同名节点不算。
func TestPlanBoardMergeModuleDups(t *testing.T) {
	st, ctx, a := boardFixture(t)
	mk := func(wf string, parent uint, name string) *model.WorkModule {
		return legacyModule(t, st, wf, parent, name)
	}
	m1, m2, m3 := mk("wf-1", 0, "后端"), mk("wf-2", 0, "后端"), mk("wf-solo", 0, "后端")
	f1, f2 := mk("wf-1", m1.ID, "存储"), mk("wf-2", m2.ID, "存储")
	mk("wf-2", m2.ID, "接口") // 只有 wf-2 有：不算重复
	legacyTasks(t, st, "wf-1", f1.ID, 1, 2)
	legacyTasks(t, st, "wf-2", f2.ID, 1)
	legacyTasks(t, st, "wf-solo", m3.ID, 1)

	plan, err := PlanBoardMerge(ctx, st.db)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.ModuleDups) != 2 {
		t.Fatalf("应有「后端」与「后端/存储」两处重复: %+v", plan.ModuleDups)
	}
	d0, d1 := plan.ModuleDups[0], plan.ModuleDups[1]
	if d0.Path != "后端" || d1.Path != "后端/存储" {
		t.Fatalf("路径不对: %q %q", d0.Path, d1.Path)
	}
	if d0.BoardKey != ProjectBoardKey(a.ID) || strings.Join(d0.Workflows, ",") != "wf-1,wf-2" || len(d0.ModuleIDs) != 2 || d0.Tasks != 0 {
		t.Errorf("「后端」重复信息不对: %+v", d0)
	}
	if d1.Tasks != 3 || len(d1.ModuleIDs) != 2 {
		t.Errorf("「后端/存储」上挂了 3 条任务: %+v", d1)
	}
	if b := boardByKey(plan, ProjectBoardKey(a.ID)); b.DupModules != 2 {
		t.Errorf("板汇总的重复节点数应为 2: %+v", b)
	}
	if b := boardByKey(plan, WorkflowBoardKey("wf-solo")); b.DupModules != 0 {
		t.Errorf("别的板里的同名节点不应算重复: %+v", b)
	}
}

// 旧计数落后、旧计数里有没有任务的工作流、没有计数记录：都要在警告里说明，计数取较大者。
func TestPlanBoardMergeCountersAndWarnings(t *testing.T) {
	st, ctx, a := boardFixture(t)
	legacyTasks(t, st, "wf-1", 0, 1, 2, 3)
	if err := st.db.Model(&model.WorkItemCounter{}).Where("workflow_id = ?", "wf-1").Update("last_seq", 1).Error; err != nil {
		t.Fatal(err)
	}
	// wf-2 没有任务，但旧计数是 7（任务删光了）：它所在的项目板迁移后计数不能回退到 3。
	if err := st.db.Create(&model.WorkItemCounter{WorkflowID: "wf-2", LastSeq: 7}).Error; err != nil {
		t.Fatal(err)
	}
	// wf-solo 有任务，但计数记录被删了。
	legacyTasks(t, st, "wf-solo", 0, 1, 2)
	if err := st.db.Where("workflow_id = ?", "wf-solo").Delete(&model.WorkItemCounter{}).Error; err != nil {
		t.Fatal(err)
	}

	plan, err := PlanBoardMerge(ctx, st.db)
	if err != nil {
		t.Fatal(err)
	}
	if b := boardByKey(plan, ProjectBoardKey(a.ID)); b == nil || b.Counter != 7 || b.Tasks != 3 {
		t.Fatalf("项目板计数应取 7（含没有任务的 wf-2 的旧计数）: %+v", b)
	}
	if b := boardByKey(plan, WorkflowBoardKey("wf-solo")); b == nil || b.Counter != 2 {
		t.Fatalf("没有计数记录时按最大序号补: %+v", b)
	}
	joined := strings.Join(plan.Warnings, "\n")
	for _, sub := range []string{"wf-1 的旧计数 1 落后于最大序号 3", "wf-solo 有 2 条任务但没有旧计数记录"} {
		if !strings.Contains(joined, sub) {
			t.Errorf("警告里应含 %q:\n%s", sub, joined)
		}
	}
}

// 历史数据里的跨工作流引用（存储层禁止，库里仍可能有）：给警告，不报错、不影响规划。
func TestPlanBoardMergeCrossWorkflowReferencesWarn(t *testing.T) {
	st, ctx, _ := boardFixture(t)
	ids1 := legacyTasks(t, st, "wf-1", 0, 1)
	ids2 := legacyTasks(t, st, "wf-2", 0, 1)
	m1 := legacyModule(t, st, "wf-1", 0, "模块")
	// 绕过存储层的校验直接改库：wf-2 的任务指向 wf-1 的父任务与模块。
	if err := st.db.Model(&model.WorkItem{}).Where("id = ?", ids2[0]).Updates(map[string]any{"parent_id": ids1[0], "module_id": m1.ID}).Error; err != nil {
		t.Fatal(err)
	}
	plan, err := PlanBoardMerge(ctx, st.db)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(plan.Warnings, "\n")
	if !strings.Contains(joined, "父任务属于别的工作流 wf-1") || !strings.Contains(joined, "模块属于别的工作流 wf-1") {
		t.Fatalf("跨工作流引用应给警告:\n%s", joined)
	}
}
