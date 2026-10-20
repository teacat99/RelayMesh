package store

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/teacat99/RelayMesh/v2/internal/model"
)

func mustModule(t *testing.T, st *Store, wf string, parent uint, name string) *model.WorkModule {
	t.Helper()
	m, err := st.CreateWorkModule(context.Background(), wf, parent, name, "")
	if err != nil {
		t.Fatalf("创建模块 %q: %v", name, err)
	}
	return m
}

func modulePaths(t *testing.T, st *Store, wf string) []string {
	t.Helper()
	list, err := st.ListWorkModules(context.Background(), wf)
	if err != nil {
		t.Fatalf("列出模块: %v", err)
	}
	out := make([]string, 0, len(list.Modules))
	for _, m := range list.Modules {
		out = append(out, m.Path)
	}
	return out
}

func wantPaths(t *testing.T, st *Store, wf string, want ...string) {
	t.Helper()
	got := modulePaths(t, st, wf)
	if !slices.Equal(got, want) {
		t.Fatalf("规划树 = %v，期望 %v", got, want)
	}
}

func TestWorkModuleCreateRulesAndOrder(t *testing.T) {
	st := setupTestStore(t)
	ctx := context.Background()

	term := mustModule(t, st, "wf", 0, "终端")
	task := mustModule(t, st, "wf", 0, "任务")
	hist := mustModule(t, st, "wf", term.ID, "历史日志")
	mustModule(t, st, "wf", term.ID, "输入法")
	// 树的顺序：顶层按创建顺序，功能紧跟在它的模块后面。
	wantPaths(t, st, "wf", "终端", "终端/历史日志", "终端/输入法", "任务")
	if hist.Path != "终端/历史日志" || hist.Depth != 2 || task.Depth != 1 {
		t.Fatalf("路径 / 深度不对: %+v %+v", hist, task)
	}

	// 同级不区分大小写不重名；不同模块下可以有同名功能。
	if _, err := st.CreateWorkModule(ctx, "wf", 0, " 终端 ", ""); err == nil {
		t.Fatal("同名模块应被拒绝")
	} else {
		wantInvalid(t, err, "不能重名")
	}
	mustModule(t, st, "wf", 0, "API")
	if _, err := st.CreateWorkModule(ctx, "wf", 0, "api", ""); err == nil {
		t.Fatal("大小写不同的同名模块应被拒绝")
	}
	mustModule(t, st, "wf", task.ID, "历史日志")

	// 名称规则。
	for name, want := range map[string]string{
		"":                      "不能为空",
		"   ":                   "不能为空",
		"a/b":                   "分隔符",
		"a>b":                   "分隔符",
		"a→b":                   "分隔符",
		"换\n行":                  "分隔符",
		strings.Repeat("长", 41): "最长 40",
	} {
		if _, err := st.CreateWorkModule(ctx, "wf", 0, name, ""); err == nil {
			t.Fatalf("名称 %q 应被拒绝", name)
		} else {
			wantInvalid(t, err, want)
		}
	}
	if _, err := st.CreateWorkModule(ctx, "wf", 0, "说明过长", strings.Repeat("长", 201)); err == nil {
		t.Fatal("说明过长应被拒绝")
	}

	// 深度 ≤ 2；上级必须存在且属于同一工作流。
	if _, err := st.CreateWorkModule(ctx, "wf", hist.ID, "更深一层", ""); err == nil {
		t.Fatal("功能下面不能再建子级")
	} else {
		wantInvalid(t, err, "最多 2 层")
	}
	if _, err := st.CreateWorkModule(ctx, "wf", 99999, "孤儿", ""); err == nil {
		t.Fatal("上级不存在应被拒绝")
	} else {
		wantNotFound(t, err)
	}
	other := mustModule(t, st, "wf-other", 0, "别处")
	if _, err := st.CreateWorkModule(ctx, "wf", other.ID, "跨工作流", ""); err == nil {
		t.Fatal("上级属于别的工作流应被拒绝")
	}
	if _, err := st.CreateWorkModule(ctx, "", 0, "x", ""); err == nil {
		t.Fatal("workflow_id 为空应被拒绝")
	}
}

func TestWorkModuleLimits(t *testing.T) {
	st := setupTestStore(t)
	ctx := context.Background()

	var first *model.WorkModule
	for i := 0; i < model.WorkModulesMax; i++ {
		m := mustModule(t, st, "wf", 0, fmt.Sprintf("模块%02d", i))
		if first == nil {
			first = m
		}
	}
	if _, err := st.CreateWorkModule(ctx, "wf", 0, "多出来的", ""); err == nil {
		t.Fatal("超过顶层模块上限应被拒绝")
	} else {
		wantInvalid(t, err, "最多 30 个模块")
	}
	for i := 0; i < model.WorkFeaturesMax; i++ {
		mustModule(t, st, "wf", first.ID, fmt.Sprintf("功能%02d", i))
	}
	if _, err := st.CreateWorkModule(ctx, "wf", first.ID, "多出来的功能", ""); err == nil {
		t.Fatal("超过功能上限应被拒绝")
	} else {
		wantInvalid(t, err, "最多 30 个功能")
	}
	// 别的工作流不受影响。
	mustModule(t, st, "wf-2", 0, "照常能建")
}

func TestWorkModuleResolvePaths(t *testing.T) {
	st := setupTestStore(t)
	ctx := context.Background()

	// 空规划时的报错要带上手把手的指引。
	if _, err := st.ResolveWorkModule(ctx, "wf", "终端"); err == nil {
		t.Fatal("没有规划时解析应失败")
	} else {
		wantInvalid(t, err, "task_board action=plan")
	}

	term := mustModule(t, st, "wf", 0, "终端")
	task := mustModule(t, st, "wf", 0, "任务")
	mustModule(t, st, "wf", term.ID, "历史日志")
	mustModule(t, st, "wf", term.ID, "输入法")
	mustModule(t, st, "wf", task.ID, "输入法") // 与终端下的功能同名 → 单段解析有歧义
	mustModule(t, st, "wf", task.ID, "进度条")

	okCases := map[string]string{
		"终端":        "终端",
		" 终端 ":      "终端",
		"终端/历史日志":   "终端/历史日志",
		"终端 / 历史日志": "终端/历史日志",
		"终端>历史日志":   "终端/历史日志",
		"终端→历史日志":   "终端/历史日志",
		"历史日志":      "终端/历史日志", // 全树唯一的功能名可以省略模块
		"进度条":       "任务/进度条",
	}
	for in, want := range okCases {
		m, err := st.ResolveWorkModule(ctx, "wf", in)
		if err != nil {
			t.Fatalf("解析 %q: %v", in, err)
		}
		if m.Path != want {
			t.Fatalf("解析 %q = %q，期望 %q", in, m.Path, want)
		}
	}
	// 不区分大小写。
	mustModule(t, st, "wf", 0, "API")
	if m, err := st.ResolveWorkModule(ctx, "wf", "api"); err != nil || m.Path != "API" {
		t.Fatalf("大小写不敏感解析失败: %v %v", m, err)
	}

	// 有歧义要写全路径，并列出候选。
	if _, err := st.ResolveWorkModule(ctx, "wf", "输入法"); err == nil {
		t.Fatal("同名功能应报歧义")
	} else {
		wantInvalid(t, err, "终端/输入法")
		wantInvalid(t, err, "任务/输入法")
	}
	// 不存在：列出已有路径，提示先建。
	if _, err := st.ResolveWorkModule(ctx, "wf", "终端/不存在"); err == nil {
		t.Fatal("不存在应失败")
	} else {
		wantInvalid(t, err, "已有：")
		wantInvalid(t, err, "终端/历史日志")
		wantInvalid(t, err, "task_board action=plan")
	}
	if _, err := st.ResolveWorkModule(ctx, "wf", "终端/历史日志/更深"); err == nil {
		t.Fatal("三层路径应失败")
	} else {
		wantInvalid(t, err, "最多 2 层")
	}
	if _, err := st.ResolveWorkModule(ctx, "wf", " / "); err == nil {
		t.Fatal("空路径应失败")
	}
	// 别的工作流看不到。
	if _, err := st.ResolveWorkModule(ctx, "wf-other", "终端"); err == nil {
		t.Fatal("别的工作流不应解析到")
	}
}

func TestWorkModuleRenameDescribeMove(t *testing.T) {
	st := setupTestStore(t)
	ctx := context.Background()

	a := mustModule(t, st, "wf", 0, "A")
	b := mustModule(t, st, "wf", 0, "B")
	c := mustModule(t, st, "wf", 0, "C")
	a1 := mustModule(t, st, "wf", a.ID, "a1")
	a2 := mustModule(t, st, "wf", a.ID, "a2")

	// 上移 / 下移，边界处无操作。
	list, err := st.MoveWorkModule(ctx, c.ID, -1)
	if err != nil || len(list.Modules) != 5 {
		t.Fatalf("上移: %v", err)
	}
	wantPaths(t, st, "wf", "A", "A/a1", "A/a2", "C", "B")
	if _, err := st.MoveWorkModule(ctx, c.ID, -1); err != nil {
		t.Fatal(err)
	}
	wantPaths(t, st, "wf", "C", "A", "A/a1", "A/a2", "B")
	if _, err := st.MoveWorkModule(ctx, c.ID, -1); err != nil { // 已在最前
		t.Fatal(err)
	}
	wantPaths(t, st, "wf", "C", "A", "A/a1", "A/a2", "B")
	if _, err := st.MoveWorkModule(ctx, b.ID, +1); err != nil { // 已在最后
		t.Fatal(err)
	}
	wantPaths(t, st, "wf", "C", "A", "A/a1", "A/a2", "B")
	if _, err := st.MoveWorkModule(ctx, a2.ID, -1); err != nil { // 功能在自己模块内移动
		t.Fatal(err)
	}
	wantPaths(t, st, "wf", "C", "A", "A/a2", "A/a1", "B")
	if _, err := st.MoveWorkModule(ctx, 99999, 1); err == nil {
		t.Fatal("不存在的模块")
	} else {
		wantNotFound(t, err)
	}

	// 改名：重名被拒绝，只改大小写允许；任务上的模块路径跟着变。
	if _, err := st.UpdateWorkModule(ctx, a1.ID, WorkModulePatch{Name: ptr("a2")}); err == nil {
		t.Fatal("同级重名应被拒绝")
	}
	if _, err := st.UpdateWorkModule(ctx, a1.ID, WorkModulePatch{Name: ptr("bad/name")}); err == nil {
		t.Fatal("非法名称应被拒绝")
	}
	task := mustCreateWork(t, st, "wf", "归到 a1", func(in *WorkItemInput) { in.ModuleID = a1.ID })
	m, err := st.UpdateWorkModule(ctx, a1.ID, WorkModulePatch{Name: ptr("A1"), Description: ptr("  第一个功能 ")})
	if err != nil {
		t.Fatal(err)
	}
	if m.Name != "A1" || m.Description != "第一个功能" || m.Path != "A/A1" {
		t.Fatalf("改名结果不对: %+v", m)
	}
	got, err := st.GetWorkItem(ctx, task.ID)
	if err != nil || got.ModulePath != "A/A1" {
		t.Fatalf("任务的模块路径应跟随改名: %v %q", err, got.ModulePath)
	}
	if _, err := st.UpdateWorkModule(ctx, 99999, WorkModulePatch{Name: ptr("x")}); err == nil {
		t.Fatal("不存在的模块")
	}
}

func TestWorkModuleDeleteFallsBackTasks(t *testing.T) {
	st := setupTestStore(t)
	ctx := context.Background()

	term := mustModule(t, st, "wf", 0, "终端")
	hist := mustModule(t, st, "wf", term.ID, "历史日志")
	ime := mustModule(t, st, "wf", term.ID, "输入法")
	other := mustModule(t, st, "wf", 0, "任务")

	tTop := mustCreateWork(t, st, "wf", "挂在模块上", func(in *WorkItemInput) { in.ModuleID = term.ID })
	tHist := mustCreateWork(t, st, "wf", "挂在历史日志", func(in *WorkItemInput) { in.ModuleID = hist.ID })
	tIme := mustCreateWork(t, st, "wf", "挂在输入法", func(in *WorkItemInput) { in.ModuleID = ime.ID })
	tOther := mustCreateWork(t, st, "wf", "挂在任务", func(in *WorkItemInput) { in.ModuleID = other.ID })
	before := map[uint]time.Time{}
	for _, it := range []*model.WorkItem{tTop, tHist, tIme, tOther} {
		before[it.ID] = it.UpdatedAt
	}

	// 删功能：任务回到它的模块。
	res, err := st.DeleteWorkModule(ctx, hist.ID)
	if err != nil {
		t.Fatal(err)
	}
	if res.MovedTasks != 1 || res.RemovedFeatures != 0 || res.Module.Path != "终端/历史日志" {
		t.Fatalf("删除功能结果不对: %+v", res)
	}
	wantPaths(t, st, "wf", "终端", "终端/输入法", "任务")
	got, _ := st.GetWorkItem(ctx, tHist.ID)
	if got.ModuleID == nil || *got.ModuleID != term.ID || got.ModulePath != "终端" {
		t.Fatalf("任务应回到上级模块: %+v", got.ModuleID)
	}

	// 删顶层模块：功能一并删除，所有任务（含功能里的）变成未归类。
	res, err = st.DeleteWorkModule(ctx, term.ID)
	if err != nil {
		t.Fatal(err)
	}
	if res.MovedTasks != 3 || res.RemovedFeatures != 1 {
		t.Fatalf("删除模块结果不对: %+v", res)
	}
	wantPaths(t, st, "wf", "任务")
	for _, id := range []uint{tTop.ID, tHist.ID, tIme.ID} {
		it, _ := st.GetWorkItem(ctx, id)
		if it.ModuleID != nil || it.ModulePath != "" {
			t.Fatalf("任务 %d 应变成未归类: %+v", id, it.ModuleID)
		}
	}
	it, _ := st.GetWorkItem(ctx, tOther.ID)
	if it.ModuleID == nil || *it.ModuleID != other.ID {
		t.Fatal("别的模块下的任务不应受影响")
	}
	// 整理分类不是进展：任务的 updated_at 保持不变。
	for id, ts := range before {
		it, _ := st.GetWorkItem(ctx, id)
		if !it.UpdatedAt.Equal(ts) {
			t.Fatalf("删除模块不应改动任务 %d 的 updated_at: %v → %v", id, ts, it.UpdatedAt)
		}
	}
	// 同级顺序被规整：删除后新建的模块仍然排在最后。
	mustModule(t, st, "wf", 0, "新模块")
	wantPaths(t, st, "wf", "任务", "新模块")
	if _, err := st.DeleteWorkModule(ctx, 99999); err == nil {
		t.Fatal("不存在的模块")
	} else {
		wantNotFound(t, err)
	}
}

func TestWorkPlanIsIdempotentAndAtomic(t *testing.T) {
	st := setupTestStore(t)
	ctx := context.Background()

	plan := []WorkPlanNode{
		{Name: "终端", Features: []WorkPlanNode{{Name: "历史日志"}, {Name: "输入法"}}},
		{Name: "任务", Description: ptr("任务清单与规划")},
	}
	res, err := st.ApplyWorkPlan(ctx, "wf", plan, false)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"终端", "终端/历史日志", "终端/输入法", "任务"}; !slices.Equal(res.Created, want) {
		t.Fatalf("created = %v，期望 %v", res.Created, want)
	}
	wantPaths(t, st, "wf", "终端", "终端/历史日志", "终端/输入法", "任务")
	list, _ := st.ListWorkModules(ctx, "wf")
	if list.Modules[3].Description != "任务清单与规划" {
		t.Fatalf("说明没有写入: %+v", list.Modules[3])
	}

	// 重复提交同一份计划：什么都不变。
	res, err = st.ApplyWorkPlan(ctx, "wf", plan, false)
	if err != nil {
		t.Fatal(err)
	}
	if res.Changed() || res.Unchanged != 4 {
		t.Fatalf("重复提交应幂等: %+v", res)
	}

	// 改说明、改名；重复提交改名计划同样幂等。
	rename := []WorkPlanNode{
		{Name: "终端", Features: []WorkPlanNode{{Name: "输入法", Rename: "输入", Description: ptr("中文输入")}}},
		{Name: "任务", Description: ptr("任务页")},
	}
	res, err = st.ApplyWorkPlan(ctx, "wf", rename, false)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(res.Renamed, []string{"终端/输入法→输入"}) || !slices.Equal(res.Updated, []string{"终端/输入", "任务"}) {
		t.Fatalf("改名 / 改说明结果不对: %+v", res)
	}
	wantPaths(t, st, "wf", "终端", "终端/历史日志", "终端/输入", "任务")
	res, err = st.ApplyWorkPlan(ctx, "wf", rename, false)
	if err != nil || res.Changed() {
		t.Fatalf("重复提交改名计划应幂等: %+v %v", res, err)
	}
	// 找不到要改名的节点，且新名字也不存在 → 报错。
	if _, err := st.ApplyWorkPlan(ctx, "wf", []WorkPlanNode{{Name: "没有这个", Rename: "也没有"}}, false); err == nil {
		t.Fatal("找不到改名对象应失败")
	} else {
		wantInvalid(t, err, "找不到要改名")
	}
	// 改成已存在的同级名称 → 报错。
	if _, err := st.ApplyWorkPlan(ctx, "wf", []WorkPlanNode{{Name: "终端", Features: []WorkPlanNode{{Name: "输入", Rename: "历史日志"}}}}, false); err == nil {
		t.Fatal("改名撞同级应失败")
	}

	// 默认只追加不重排：只列出「任务」不会改变顺序；新模块追加在末尾。
	if _, err := st.ApplyWorkPlan(ctx, "wf", []WorkPlanNode{{Name: "任务"}, {Name: "同步"}}, false); err != nil {
		t.Fatal(err)
	}
	wantPaths(t, st, "wf", "终端", "终端/历史日志", "终端/输入", "任务", "同步")
	// reorder=true：列出的节点按给定顺序排前面，没列出的跟在后面保持原序。
	reorder := []WorkPlanNode{
		{Name: "同步"},
		{Name: "终端", Features: []WorkPlanNode{{Name: "输入"}}},
	}
	res, err = st.ApplyWorkPlan(ctx, "wf", reorder, true)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Reordered || !res.Changed() || len(res.Created) != 0 {
		t.Fatalf("只改顺序也算有变化: %+v", res)
	}
	wantPaths(t, st, "wf", "同步", "终端", "终端/输入", "终端/历史日志", "任务")
	// 顺序已经是这样：再提交一次不算变化（调用方据此决定是否广播）。
	res, err = st.ApplyWorkPlan(ctx, "wf", reorder, true)
	if err != nil || res.Reordered || res.Changed() {
		t.Fatalf("顺序没变时不应标记变化: %+v %v", res, err)
	}

	// 原子性：计划里有一个节点不合法，整份计划都不生效。
	before := modulePaths(t, st, "wf")
	if _, err := st.ApplyWorkPlan(ctx, "wf", []WorkPlanNode{{Name: "新模块"}, {Name: "bad/name"}}, false); err == nil {
		t.Fatal("非法节点应让整份计划失败")
	}
	if got := modulePaths(t, st, "wf"); !slices.Equal(got, before) {
		t.Fatalf("失败的计划不应留下任何改动: %v → %v", before, got)
	}
	// 深度：功能下面不能再有子级。
	if _, err := st.ApplyWorkPlan(ctx, "wf", []WorkPlanNode{{Name: "终端", Features: []WorkPlanNode{{Name: "输入", Features: []WorkPlanNode{{Name: "更深"}}}}}}, false); err == nil {
		t.Fatal("三层计划应失败")
	} else {
		wantInvalid(t, err, "最多 2 层")
	}
	if _, err := st.ApplyWorkPlan(ctx, "wf", nil, false); err == nil {
		t.Fatal("空计划应失败")
	} else {
		wantInvalid(t, err, "modules 不能为空")
	}
}

func TestWorkPlanRemoveRules(t *testing.T) {
	st := setupTestStore(t)
	ctx := context.Background()

	if _, err := st.ApplyWorkPlan(ctx, "wf", []WorkPlanNode{
		{Name: "终端", Features: []WorkPlanNode{{Name: "历史日志"}, {Name: "输入法"}}},
		{Name: "空模块"},
	}, false); err != nil {
		t.Fatal(err)
	}
	hist, err := st.ResolveWorkModule(ctx, "wf", "终端/历史日志")
	if err != nil {
		t.Fatal(err)
	}
	mustCreateWork(t, st, "wf", "挂着任务", func(in *WorkItemInput) { in.ModuleID = hist.ID })

	// 下面还有任务：不能移除；整个调用回滚（输入法也不会被移除）。
	if _, err := st.ApplyWorkPlan(ctx, "wf", []WorkPlanNode{
		{Name: "终端", Features: []WorkPlanNode{{Name: "输入法", Remove: true}, {Name: "历史日志", Remove: true}}},
	}, false); err == nil {
		t.Fatal("有任务的模块不能移除")
	} else {
		wantInvalid(t, err, "还有 1 个任务")
	}
	wantPaths(t, st, "wf", "终端", "终端/历史日志", "终端/输入法", "空模块")

	// 还有功能的模块不能直接移除：要把功能也标上 remove。
	if _, err := st.ApplyWorkPlan(ctx, "wf", []WorkPlanNode{{Name: "终端", Remove: true}}, false); err == nil {
		t.Fatal("还有功能的模块不能移除")
	} else {
		wantInvalid(t, err, "还有 2 个功能")
	}
	// 空节点可以移除；不存在的节点移除是幂等的空操作。
	res, err := st.ApplyWorkPlan(ctx, "wf", []WorkPlanNode{
		{Name: "空模块", Remove: true},
		{Name: "终端", Features: []WorkPlanNode{{Name: "输入法", Remove: true}}},
		{Name: "从没有过", Remove: true},
	}, false)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(res.Removed, []string{"空模块", "终端/输入法"}) {
		t.Fatalf("removed = %v", res.Removed)
	}
	wantPaths(t, st, "wf", "终端", "终端/历史日志")
}

func TestWorkItemModuleAndPercent(t *testing.T) {
	st := setupTestStore(t)
	ctx := context.Background()

	term := mustModule(t, st, "wf", 0, "终端")
	hist := mustModule(t, st, "wf", term.ID, "历史日志")
	foreign := mustModule(t, st, "wf-other", 0, "别处")

	// 创建时归类 + 显式完成度。
	it := mustCreateWork(t, st, "wf", "平铺历史", func(in *WorkItemInput) {
		in.ModuleID, in.Percent, in.State = hist.ID, ptr(30), "doing"
	})
	if it.ModuleID == nil || *it.ModuleID != hist.ID || it.ModulePath != "终端/历史日志" {
		t.Fatalf("创建时归类失败: %+v %q", it.ModuleID, it.ModulePath)
	}
	if it.Percent == nil || *it.Percent != 30 || it.ProgressPct != 30 || it.ProgressFrom != "explicit" {
		t.Fatalf("显式完成度不对: %+v %d %s", it.Percent, it.ProgressPct, it.ProgressFrom)
	}

	// 校验：模块不存在 / 属于别的工作流 / percent 越界。
	if _, err := st.CreateWorkItem(ctx, WorkItemInput{WorkflowID: "wf", Title: "x", ModuleID: 99999}, human); err == nil {
		t.Fatal("不存在的模块应被拒绝")
	}
	if _, err := st.CreateWorkItem(ctx, WorkItemInput{WorkflowID: "wf", Title: "x", ModuleID: foreign.ID}, human); err == nil {
		t.Fatal("别的工作流的模块应被拒绝")
	}
	for _, bad := range []int{101, -2, 1000} {
		if _, err := st.CreateWorkItem(ctx, WorkItemInput{WorkflowID: "wf", Title: "x", Percent: ptr(bad)}, human); err == nil {
			t.Fatalf("percent=%d 应被拒绝", bad)
		} else {
			wantInvalid(t, err, "percent")
		}
		if _, err := st.UpdateWorkItem(ctx, it.ID, WorkItemPatch{Percent: ptr(bad)}, human); err == nil {
			t.Fatalf("更新 percent=%d 应被拒绝", bad)
		}
	}
	if _, err := st.UpdateWorkItem(ctx, it.ID, WorkItemPatch{ModuleID: ptr(foreign.ID)}, agent); err == nil {
		t.Fatal("更新为别的工作流的模块应被拒绝")
	}

	// 更新完成度：改变会更新 updated_at；-1 清除后回到状态推导（进行中 = 50）。
	time.Sleep(15 * time.Millisecond)
	res, err := st.UpdateWorkItem(ctx, it.ID, WorkItemPatch{Percent: ptr(60)}, agent)
	if err != nil || !res.Changed || res.Item.ProgressPct != 60 {
		t.Fatalf("更新完成度: %+v %v", res, err)
	}
	if !res.Item.UpdatedAt.After(it.UpdatedAt) {
		t.Fatal("完成度变化属于进展，应更新 updated_at")
	}
	res, _ = st.UpdateWorkItem(ctx, it.ID, WorkItemPatch{Percent: ptr(60)}, agent)
	if res.Changed {
		t.Fatal("相同完成度不应算变化")
	}
	res, err = st.UpdateWorkItem(ctx, it.ID, WorkItemPatch{Percent: ptr(-1)}, agent)
	if err != nil || !res.Changed || res.Item.Percent != nil || res.Item.ProgressPct != 50 || res.Item.ProgressFrom != "state" {
		t.Fatalf("清除完成度: %+v %v", res.Item, err)
	}
	res, _ = st.UpdateWorkItem(ctx, it.ID, WorkItemPatch{Percent: ptr(-1)}, agent)
	if res.Changed {
		t.Fatal("没有显式完成度时再清除不应算变化")
	}

	// 只调整归属：生效，但不动 updated_at，也不占时间线。
	cur, _ := st.GetWorkItem(ctx, it.ID)
	timeline := len(cur.Updates)
	time.Sleep(15 * time.Millisecond)
	res, err = st.UpdateWorkItem(ctx, it.ID, WorkItemPatch{ModuleID: ptr(term.ID)}, human)
	if err != nil || !res.Changed || res.Item.ModulePath != "终端" {
		t.Fatalf("调整归属: %+v %v", res, err)
	}
	if !res.Item.UpdatedAt.Equal(cur.UpdatedAt) || len(res.Item.Updates) != timeline {
		t.Fatalf("只调整归属不应改动 updated_at / 时间线: %v → %v, %d → %d", cur.UpdatedAt, res.Item.UpdatedAt, timeline, len(res.Item.Updates))
	}
	res, _ = st.UpdateWorkItem(ctx, it.ID, WorkItemPatch{ModuleID: ptr(term.ID)}, human)
	if res.Changed {
		t.Fatal("归属没变不应算变化")
	}
	res, err = st.UpdateWorkItem(ctx, it.ID, WorkItemPatch{ModuleID: ptr(uint(0))}, human)
	if err != nil || !res.Changed || res.Item.ModuleID != nil || res.Item.ModulePath != "" {
		t.Fatalf("取消归类: %+v %v", res.Item, err)
	}

	// 归属与其他字段一起改时走正常路径（更新 updated_at）。
	res, err = st.UpdateWorkItem(ctx, it.ID, WorkItemPatch{ModuleID: ptr(hist.ID), Progress: ptr("写完了 xterm 的最后一屏处理")}, agent)
	if err != nil || res.Item.ModulePath != "终端/历史日志" || !res.Item.UpdatedAt.After(cur.UpdatedAt) {
		t.Fatalf("组合更新: %+v %v", res.Item, err)
	}
}

func TestWorkItemProgressDerivation(t *testing.T) {
	st := setupTestStore(t)
	ctx := context.Background()

	cases := []struct {
		state   string
		percent *int
		wantPct int
		from    string
	}{
		{"todo", nil, 0, "state"},
		{"doing", nil, 50, "state"},
		{"review", nil, 90, "state"},
		{"shelved", nil, 0, "state"},
		{"doing", ptr(20), 20, "explicit"},
		{"todo", ptr(0), 0, "explicit"},
	}
	for i, c := range cases {
		it := mustCreateWork(t, st, "wf", fmt.Sprintf("case-%d-%s", i, c.state), func(in *WorkItemInput) {
			in.State, in.Percent = c.state, c.percent
		})
		if it.ProgressPct != c.wantPct || it.ProgressFrom != c.from {
			t.Fatalf("case %d (%s): 得到 %d/%s，期望 %d/%s", i, c.state, it.ProgressPct, it.ProgressFrom, c.wantPct, c.from)
		}
	}
	// 已完成永远是 100，哪怕写过显式完成度。
	done := mustCreateWork(t, st, "wf", "完成", func(in *WorkItemInput) { in.State, in.Percent = "done", ptr(10) })
	if done.ProgressPct != 100 {
		t.Fatalf("done 应为 100，得到 %d", done.ProgressPct)
	}
	// 有子任务：按完成比例。
	parent := mustCreateWork(t, st, "wf", "批次", nil)
	for i, state := range []string{"done", "done", "doing", "todo"} {
		mustCreateWork(t, st, "wf", fmt.Sprintf("子%d", i), func(in *WorkItemInput) { in.ParentID, in.State = parent.ID, state })
	}
	got, _ := st.GetWorkItem(ctx, parent.ID)
	if got.ProgressPct != 50 || got.ProgressFrom != "children" {
		t.Fatalf("批次进度 = %d/%s，期望 50/children", got.ProgressPct, got.ProgressFrom)
	}
	// 列表上同样带着派生进度。
	items, _ := st.ListWorkItems(ctx, WorkItemFilter{WorkflowID: "wf", ParentID: nil})
	for _, it := range items {
		if it.ID == parent.ID && (it.ProgressPct != 50 || it.ProgressFrom != "children") {
			t.Fatalf("列表里的批次进度不对: %d/%s", it.ProgressPct, it.ProgressFrom)
		}
	}
}

func TestWorkModuleStatsCountLeafTasksOnly(t *testing.T) {
	st := setupTestStore(t)
	ctx := context.Background()

	a := mustModule(t, st, "wf", 0, "A")
	x := mustModule(t, st, "wf", a.ID, "x")
	y := mustModule(t, st, "wf", a.ID, "y")
	b := mustModule(t, st, "wf", 0, "B")
	mk := func(title, state string, module uint, pct *int) *model.WorkItem {
		return mustCreateWork(t, st, "wf", title, func(in *WorkItemInput) {
			in.State, in.ModuleID, in.Percent = state, module, pct
		})
	}
	mk("t1", "todo", x.ID, nil)
	mk("t2", "doing", x.ID, ptr(60))
	mk("t3", "done", y.ID, nil)
	mk("t4", "review", b.ID, nil)
	mk("t5", "shelved", a.ID, nil)
	mk("t6", "doing", 0, nil)
	// 批次任务本身不计入（它的子任务各自计入）。
	batch := mk("batch", "doing", a.ID, nil)
	mustCreateWork(t, st, "wf", "子1", func(in *WorkItemInput) { in.ParentID, in.State, in.ModuleID = batch.ID, "done", y.ID })

	list, err := st.ListWorkModules(ctx, "wf")
	if err != nil {
		t.Fatal(err)
	}
	by := map[string]model.WorkModuleStats{}
	for _, m := range list.Modules {
		by[m.Path] = m.Stats
	}
	if s := by["A/x"]; s.Total != 2 || s.Todo != 1 || s.Doing != 1 || s.Percent != 30 {
		t.Fatalf("A/x = %+v", s)
	}
	if s := by["A/y"]; s.Total != 2 || s.Done != 2 || s.Percent != 100 {
		t.Fatalf("A/y = %+v", s)
	}
	// A 汇总：A/x(2) + A/y(2) + 直接挂在 A 上的 t5(搁置)；批次本身不计。
	// 平均进度只算未搁置的叶子任务：(0 + 60 + 100 + 100) / 4 = 65。
	if s := by["A"]; s.Total != 5 || s.Shelved != 1 || s.Done != 2 || s.Todo != 1 || s.Doing != 1 || s.Open() != 2 || s.Percent != 65 {
		t.Fatalf("A = %+v", s)
	}
	if s := by["B"]; s.Total != 1 || s.Review != 1 || s.Percent != 90 {
		t.Fatalf("B = %+v", s)
	}
	if s := list.Unassigned; s.Total != 1 || s.Doing != 1 || s.Percent != 50 {
		t.Fatalf("未归类 = %+v", s)
	}
}

func TestWorkItemListFilterByModule(t *testing.T) {
	st := setupTestStore(t)
	ctx := context.Background()

	term := mustModule(t, st, "wf", 0, "终端")
	hist := mustModule(t, st, "wf", term.ID, "历史日志")
	task := mustModule(t, st, "wf", 0, "任务")
	mustCreateWork(t, st, "wf", "在模块上", func(in *WorkItemInput) { in.ModuleID = term.ID })
	mustCreateWork(t, st, "wf", "在功能上", func(in *WorkItemInput) { in.ModuleID = hist.ID })
	mustCreateWork(t, st, "wf", "在任务上", func(in *WorkItemInput) { in.ModuleID = task.ID })
	mustCreateWork(t, st, "wf", "未归类", nil)

	titles := func(f WorkItemFilter) []string {
		f.WorkflowID = "wf"
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
	if got := titles(WorkItemFilter{ModuleID: term.ID}); !slices.Equal(got, []string{"在功能上", "在模块上"}) {
		t.Fatalf("按模块筛选应包含功能里的任务: %v", got)
	}
	if got := titles(WorkItemFilter{ModuleID: hist.ID}); !slices.Equal(got, []string{"在功能上"}) {
		t.Fatalf("按功能筛选: %v", got)
	}
	if got := titles(WorkItemFilter{Unassigned: true}); !slices.Equal(got, []string{"未归类"}) {
		t.Fatalf("未归类筛选: %v", got)
	}
	if got := titles(WorkItemFilter{}); len(got) != 4 {
		t.Fatalf("不筛选应有 4 条: %v", got)
	}
}

func TestWorkDigest(t *testing.T) {
	st := setupTestStore(t)
	ctx := context.Background()

	d, err := st.WorkDigestFor(ctx, "wf")
	if err != nil || d.Total != 0 || d.Modules != 0 || d.Latest != nil {
		t.Fatalf("空工作流概况: %+v %v", d, err)
	}
	mod := mustModule(t, st, "wf", 0, "终端")
	mustCreateWork(t, st, "wf", "旧的", func(in *WorkItemInput) { in.State = "done" })
	time.Sleep(15 * time.Millisecond)
	mustCreateWork(t, st, "wf", "较早进行中", func(in *WorkItemInput) { in.State = "doing" })
	time.Sleep(15 * time.Millisecond)
	latest := mustCreateWork(t, st, "wf", "最近的待办", func(in *WorkItemInput) { in.ModuleID, in.Percent = mod.ID, ptr(10) })
	d, err = st.WorkDigestFor(ctx, "wf")
	if err != nil {
		t.Fatal(err)
	}
	if d.Total != 3 || d.Modules != 1 || d.Counts["done"] != 1 || d.Counts["doing"] != 1 || d.Counts["todo"] != 1 {
		t.Fatalf("概况: %+v", d)
	}
	if d.Latest == nil || d.Latest.ID != latest.ID || d.Latest.ModulePath != "终端" || d.Latest.ProgressPct != 10 {
		t.Fatalf("最近的未结束任务不对: %+v", d.Latest)
	}
}
