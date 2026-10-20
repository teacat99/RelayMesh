package mcp

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/teacat99/RelayMesh/v2/internal/model"
)

// 名字体检：按轮次 / 日期 / 阶段 / 杂物箱命名的会被点名，正经的功能域名字不能误伤。
func TestNodeNameIssue(t *testing.T) {
	flagged := []struct {
		name string
		top  bool
	}{
		{"第三批", true},
		{"第 3 轮需求", true},
		{"第二期", false},
		{"Round 2", true},
		{"sprint-5", true},
		{"batch 12", false},
		{"2026-10", true},
		{"2026-10-08 需求", true},
		{"10月8日", true},
		{"sess-861ab954", true},
		{"wf-demo", false},
		{"本次需求", true},
		{"这一轮", false},
		{"其他", true},
		{"杂项", false},
		{"Misc", true},
		{"default", false},
		{"未归类", true},
		{"开发", true},
		{"测试", true},
		{"评估", true},
		{"进行中", true},
		{"TODO", true},
	}
	for _, c := range flagged {
		if nodeNameIssue(c.name, c.top) == "" {
			t.Errorf("%q（顶层=%v）应被点名", c.name, c.top)
		}
	}

	fine := []struct {
		name string
		top  bool
	}{
		{"终端", true},
		{"历史日志", false},
		{"会话与反馈", true}, // 「会话」是本项目的功能域，不能因为字面带「会话」就误伤
		{"任务清单", true},
		{"任务", true},
		{"发布与运维", true},
		{"测试与质量", true},
		{"工程质量与测试", true},
		{"知识库与待办", true},
		{"动环 POWER", true},
		{"Round-trip 加密", true}, // 英文 round 后面没有数字
		{"background jobs", true},
		{"第一性原理", true},
		{"测试", false}, // 功能一级叫「测试」是正常的，阶段类名字只在顶层算问题
		{"设计", false},
		{"", true},
	}
	for _, c := range fine {
		if why := nodeNameIssue(c.name, c.top); why != "" {
			t.Errorf("%q（顶层=%v）不该被点名：%s", c.name, c.top, why)
		}
	}
}

func TestIsRoundTitle(t *testing.T) {
	for _, title := range []string{"第八批：用户对第七批汇报的答复", "第 3 轮：修复若干问题", "Round 4 fixes", "batch 2 的收尾"} {
		if !isRoundTitle(title) {
			t.Errorf("%q 应被识别为轮次标题", title)
		}
	}
	for _, title := range []string{"历史日志平铺", "修复第三方登录", "第一性原理评估", "把 round-trip 测试补上"} {
		if isRoundTitle(title) {
			t.Errorf("%q 不是轮次标题", title)
		}
	}
}

// 清单已有结构时，overview 带着维护守则；结构上有毛病时带 warnings，而且空清单 / 干净清单不带多余的提醒。
func TestTaskBoardOverviewCarriesMaintainRulesAndWarnings(t *testing.T) {
	srv, _ := setupWorkItemMCP(t)
	const wf = "wf-structure"

	// 空清单：how_to 先讲怎么分模块，没有 maintain / warnings。
	res := mustBoard(t, srv, wf, map[string]any{})
	howTo := strings.Join(strs(res["how_to"]), "\n")
	for _, want := range []string{"功能域", "不要按「第 N 轮", "横切", "overview"} {
		if !strings.Contains(howTo, want) {
			t.Errorf("空清单的用法应先讲怎么分模块，缺 %q:\n%s", want, howTo)
		}
	}
	if _, ok := res["maintain"]; ok {
		t.Error("空清单不需要维护守则")
	}

	// 干净的清单：有 maintain，没有 warnings。
	mustBoard(t, srv, wf, map[string]any{"action": "batch", "modules": samplePlan, "ops": []any{
		map[string]any{"title": "历史日志平铺", "module": "终端/历史日志"},
	}})
	res = mustBoard(t, srv, wf, map[string]any{})
	maintain := strings.Join(strs(res["maintain"]), "\n")
	for _, want := range []string{"模块树里找归属", "不是轮次", "同一个任务在推进"} {
		if !strings.Contains(maintain, want) {
			t.Errorf("清单有结构时应附带维护守则，缺 %q:\n%s", want, maintain)
		}
	}
	if w := asList(res["warnings"]); len(w) != 0 {
		t.Errorf("干净的清单不应有提醒: %v", w)
	}

	// 有毛病：未归类任务 + 按轮次命名的模块 + 杂物箱。
	mustBoard(t, srv, wf, map[string]any{"action": "plan", "modules": []any{
		map[string]any{"name": "第三批"},
		map[string]any{"name": "杂项"},
	}})
	mustBoard(t, srv, wf, map[string]any{"action": "batch", "ops": []any{
		map[string]any{"title": "漏填模块的任务"},
	}})
	res = mustBoard(t, srv, wf, map[string]any{})
	warnings := strings.Join(strs(res["warnings"]), "\n")
	for _, want := range []string{"1 个未结束的任务没有归类", `module="未归类"`, "「第三批」", "「杂项」", "rename"} {
		if !strings.Contains(warnings, want) {
			t.Errorf("提醒里应包含 %q:\n%s", want, warnings)
		}
	}
	if n := len(asList(res["warnings"])); n > boardWarningsMax {
		t.Errorf("提醒最多 %d 条，实际 %d 条", boardWarningsMax, n)
	}
}

// 顶层模块太多、单个模块任务很多却没有功能分层，也要说出来。
func TestTaskBoardOverviewWarnsOnSprawlAndFlatModules(t *testing.T) {
	srv, _ := setupWorkItemMCP(t)
	const wf = "wf-sprawl"

	var mods []any
	for i := 1; i <= boardModulesAdvised+1; i++ {
		mods = append(mods, map[string]any{"name": fmt.Sprintf("功能域%c", 'A'+rune(i-1))})
	}
	var ops []any
	for i := 0; i < boardFlatTasks; i++ {
		ops = append(ops, map[string]any{"title": fmt.Sprintf("任务 %d", i), "module": "功能域A"})
	}
	mustBoard(t, srv, wf, map[string]any{"action": "batch", "modules": mods, "ops": ops})

	res := mustBoard(t, srv, wf, map[string]any{})
	warnings := strings.Join(strs(res["warnings"]), "\n")
	if !strings.Contains(warnings, fmt.Sprintf("顶层模块已有 %d 个", boardModulesAdvised+1)) {
		t.Errorf("应提醒顶层模块过多:\n%s", warnings)
	}
	if !strings.Contains(warnings, "模块「功能域A」下已有 10 个任务，却没有功能分层") {
		t.Errorf("应提醒模块没有功能分层:\n%s", warnings)
	}
}

// plan / batch 里新建的节点名不合适时当场提醒（不拒绝）。
func TestTaskBoardPlanAndBatchWarnAboutBadNames(t *testing.T) {
	srv, _ := setupWorkItemMCP(t)
	const wf = "wf-plan-warn"

	res := mustBoard(t, srv, wf, map[string]any{"action": "plan", "modules": []any{
		map[string]any{"name": "终端", "features": []any{map[string]any{"name": "历史日志"}}},
		map[string]any{"name": "第二批需求"},
	}})
	if res["status"] != "applied" {
		t.Fatalf("不合适的名字只提醒、不拒绝: %#v", res)
	}
	w := strings.Join(strs(res["warnings"]), "\n")
	if !strings.Contains(w, "「第二批需求」") || strings.Contains(w, "「终端」") || !strings.Contains(w, "功能域") {
		t.Errorf("plan 的提醒应只点名不合适的节点并说明怎么改:\n%s", w)
	}

	// 好名字不出提醒。
	res = mustBoard(t, srv, "wf-plan-ok", map[string]any{"action": "plan", "modules": samplePlan})
	if _, ok := res["warnings"]; ok {
		t.Errorf("好名字不该有提醒: %#v", res["warnings"])
	}

	// batch：同一次调用里带的 modules 也要检查；没写 module 的新任务、以轮次开头的标题各有一条提醒。
	res = mustBoard(t, srv, "wf-batch-warn", map[string]any{"action": "batch",
		"modules": []any{map[string]any{"name": "终端"}, map[string]any{"name": "Round 3"}},
		"ops": []any{
			map[string]any{"title": "第八批：用户的一堆需求", "module": "终端"},
			map[string]any{"title": "忘了写模块"},
			map[string]any{"title": "写了模块的", "module": "终端"},
		}})
	bw := strings.Join(strs(res["warnings"]), "\n")
	for _, want := range []string{"「Round 3」", "本次新建的 1 个任务没有填 module", "任务标题以轮次 / 批次开头", "「第八批：用户的一堆需求」", "不要按「这一轮」打包"} {
		if !strings.Contains(bw, want) {
			t.Errorf("batch 的提醒里应包含 %q:\n%s", want, bw)
		}
	}
	if results := resultsOf(res); len(results) != 3 || results[0]["action"] != "created" {
		t.Errorf("提醒不影响任务本身的创建: %#v", results)
	}

	// 全部写好模块、标题正常：没有 warnings。
	res = mustBoard(t, srv, "wf-batch-ok", map[string]any{"action": "batch", "modules": samplePlan, "ops": []any{
		map[string]any{"title": "历史日志平铺", "module": "终端/历史日志"},
	}})
	if _, ok := res["warnings"]; ok {
		t.Errorf("规范的 batch 不该有提醒: %#v", res["warnings"])
	}
}

// 回执头：有未归类任务时当场提醒；没有模块规划但已有任务时催着去规划。
func TestTaskDigestHeaderNudgesUnfiledTasks(t *testing.T) {
	srv, _ := setupWorkItemMCP(t)
	ctx := context.Background()

	// 没有任何模块、却已经有任务。
	const flat = "wf-header-flat"
	mustBoard(t, srv, flat, map[string]any{"action": "batch", "ops": []any{map[string]any{"title": "散着放的任务"}}})
	line := srv.taskDigestHeader(ctx, flat)
	if !strings.Contains(line, "还没有模块规划") || !strings.Contains(line, "plan") {
		t.Errorf("没有模块规划时应催着去规划: %s", line)
	}

	// 有模块、有一条未归类。
	const mixed = "wf-header-mixed"
	mustBoard(t, srv, mixed, map[string]any{"action": "batch", "modules": samplePlan, "ops": []any{
		map[string]any{"title": "归好类的", "module": "终端"},
		map[string]any{"title": "没归类的"},
	}})
	line = srv.taskDigestHeader(ctx, mixed)
	if !strings.Contains(line, "未归类 1 条") || !strings.Contains(line, "batch") {
		t.Errorf("有未归类任务时应提醒归类: %s", line)
	}

	// 归类之后提醒消失；已结束的未归类任务不计。
	mustBoard(t, srv, mixed, map[string]any{"action": "batch", "ops": []any{map[string]any{"task_id": 2, "module": "任务"}}})
	line = srv.taskDigestHeader(ctx, mixed)
	if strings.Contains(line, "未归类") {
		t.Errorf("归类后不应再提醒: %s", line)
	}
}

// 「首次规划」与「后续维护」两段话都要出现在智能体会读到的各处：初始化指引、工具说明、空清单用法、模块不存在的报错。
func TestTaskBoardGuidanceCoversFirstAndLaterUse(t *testing.T) {
	srv, _ := setupWorkItemMCP(t)
	ctx := context.Background()

	gov := srv.InstructionsForPermissions(ctx, model.Permissions{Feedback: true})
	for _, want := range []string{"首次规划", "后续使用", "功能域", "第 N 轮 / 第 N 批", "其他 / 杂项", "未归类", "父任务", "maintain", "warnings"} {
		if !strings.Contains(gov, want) {
			t.Errorf("初始化指引里应包含 %q", want)
		}
	}

	var board, workflow string
	for _, d := range GetToolDefinitionsForPermissions(model.AllPermissions()) {
		switch d.Name {
		case "task_board":
			board = d.Description
		case "workflow_context":
			workflow = d.Description
		}
	}
	for _, want := range []string{"Choosing modules", "Keeping it tidy", "STABLE functional area", "Do NOT group by conversation round", "catch-all", "EXISTING module/feature path", "not a parent \"round\" task"} {
		if !strings.Contains(board, want) {
			t.Errorf("task_board 描述里应包含 %q", want)
		}
	}
	if !strings.Contains(workflow, "Choosing modules") {
		t.Error("workflow_context 的任务动作说明应指向 task_board 的模块划分原则")
	}

	// 写了不存在的模块：报错先让找已有路径，再说怎么追加，并点明命名原则。
	mustBoard(t, srv, "wf-guidance", map[string]any{"action": "plan", "modules": samplePlan})
	res := mustBoard(t, srv, "wf-guidance", map[string]any{"action": "batch", "ops": []any{
		map[string]any{"title": "写错路径的任务", "module": "第九批"},
	}})
	results := resultsOf(res)
	if len(results) != 1 || results[0]["ok"] != false {
		t.Fatalf("不存在的模块应让这一条失败: %#v", res)
	}
	msg := fmt.Sprint(results[0]["error"])
	for _, want := range []string{"已有：", "最贴近", "action=plan", "不要按轮次"} {
		if !strings.Contains(msg, want) {
			t.Errorf("模块不存在的报错里应包含 %q: %s", want, msg)
		}
	}
}
