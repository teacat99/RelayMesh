package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/teacat99/RelayMesh/v2/internal/model"
	"github.com/teacat99/RelayMesh/v2/internal/store"
)

// boardRaw 直接调用 task_board 的处理器，并把结果过一遍 JSON，得到和线上一致的 map[string]any / []any 形状。
func boardRaw(t *testing.T, srv *Server, wf string, args map[string]any) (map[string]any, error) {
	t.Helper()
	if _, ok := args["workflow_id"]; !ok && wf != "" {
		args["workflow_id"] = wf
	}
	raw, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	res, err := srv.handleTaskBoard(context.Background(), raw)
	if err != nil {
		return nil, err
	}
	b, err := json.Marshal(res)
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	return out, nil
}

func mustBoard(t *testing.T, srv *Server, wf string, args map[string]any) map[string]any {
	t.Helper()
	res, err := boardRaw(t, srv, wf, args)
	if err != nil {
		t.Fatalf("task_board %v: %v", args["action"], err)
	}
	return res
}

func asList(v any) []any {
	l, _ := v.([]any)
	return l
}

func asMap(v any) map[string]any {
	m, _ := v.(map[string]any)
	return m
}

func strs(v any) []string {
	var out []string
	for _, x := range asList(v) {
		out = append(out, fmt.Sprint(x))
	}
	return out
}

func taskTitles(res map[string]any) []string {
	var out []string
	for _, v := range asList(res["tasks"]) {
		out = append(out, asMap(v)["title"].(string))
	}
	return out
}

func resultsOf(res map[string]any) []map[string]any {
	var out []map[string]any
	for _, v := range asList(res["results"]) {
		out = append(out, asMap(v))
	}
	return out
}

var samplePlan = []any{
	map[string]any{"name": "终端", "description": "终端模式", "features": []any{
		map[string]any{"name": "历史日志"}, map[string]any{"name": "输入法"},
	}},
	map[string]any{"name": "任务"},
}

func TestTaskBoardEmptyOverviewIsAHowTo(t *testing.T) {
	srv, _ := setupWorkItemMCP(t)
	// 不带 action：默认就是 overview。
	res := mustBoard(t, srv, "wf-empty", map[string]any{})
	if res["empty"] != true {
		t.Fatalf("空清单应标记 empty: %#v", res)
	}
	howTo := strings.Join(strs(res["how_to"]), "\n")
	for _, want := range []string{"action=plan", "action=batch", "modules=[", "ops=[", "review", "「任务」页", "由你自己置 done", "conclusion 必填"} {
		if !strings.Contains(howTo, want) {
			t.Fatalf("空清单的用法里应包含 %q:\n%s", want, howTo)
		}
	}
	// D-223：「已完成」由智能体验证通过后自己置，用法里不能再留旧规则（由用户确认、done 被存成 review）。
	for _, stale := range []string{"由用户在 Web", "会被存成 review"} {
		if strings.Contains(howTo, stale) {
			t.Fatalf("空清单的用法里还留着旧规则 %q:\n%s", stale, howTo)
		}
	}
	if len(asList(res["how_to"])) < 4 {
		t.Fatalf("用法应分步给出: %#v", res["how_to"])
	}
	counts := asMap(res["counts"])
	if toInt(counts["total"]) != 0 || toInt(counts["open"]) != 0 {
		t.Fatalf("空清单的计数应为 0: %#v", counts)
	}
	if !strings.Contains(fmt.Sprint(res["message"]), "还没有任务清单") {
		t.Fatalf("应有一句话说明现状: %#v", res["message"])
	}

	// 只有模块、没有任务：不再是 empty，提示去建任务。
	mustBoard(t, srv, "wf-modules-only", map[string]any{"action": "plan", "modules": samplePlan})
	res = mustBoard(t, srv, "wf-modules-only", map[string]any{})
	if res["empty"] != false || !strings.Contains(fmt.Sprint(res["hint"]), "action=batch") {
		t.Fatalf("只有模块时应提示用 batch 建任务: %#v", res)
	}
	if len(asList(res["modules"])) != 2 {
		t.Fatalf("概览应列出模块树: %#v", res["modules"])
	}
}

func TestTaskBoardPlanIsIdempotentAndAtomic(t *testing.T) {
	srv, events := setupWorkItemMCP(t)
	const wf = "wf-plan"

	res := mustBoard(t, srv, wf, map[string]any{"action": "plan", "modules": samplePlan})
	if res["status"] != "applied" || strings.Join(strs(res["created"]), ",") != "终端,终端/历史日志,终端/输入法,任务" {
		t.Fatalf("首次规划: %#v", res)
	}
	tree := asList(res["tree"])
	if len(tree) != 2 {
		t.Fatalf("回执应带规划树: %#v", res["tree"])
	}
	top := asMap(tree[0])
	if top["path"] != "终端" || top["description"] != "终端模式" || len(asList(top["features"])) != 2 {
		t.Fatalf("模块树内容不对: %#v", top)
	}
	if asMap(asList(top["features"])[0])["path"] != "终端/历史日志" {
		t.Fatalf("功能的路径应是完整路径: %#v", top)
	}
	evBefore := len(*events)
	if evBefore == 0 || (*events)[evBefore-1].Name != "work_module_updated" || asMap((*events)[evBefore-1].Data)["workflow_id"] != wf {
		t.Fatalf("规划变化应广播 work_module_updated: %#v", *events)
	}

	// 重复提交：不变，也不广播。
	res = mustBoard(t, srv, wf, map[string]any{"action": "plan", "modules": samplePlan})
	if res["status"] != "unchanged" || res["created"] != nil || len(*events) != evBefore {
		t.Fatalf("重复规划应是 unchanged 且不广播: %#v", res)
	}

	// 原子：有一个节点不合法，整份计划不生效，错误里说明。
	_, err := boardRaw(t, srv, wf, map[string]any{"action": "plan", "modules": []any{
		map[string]any{"name": "新模块"}, map[string]any{"name": "bad/name"},
	}})
	if err == nil || !strings.Contains(err.Error(), "整份计划都没有生效") || !strings.Contains(err.Error(), "分隔符") {
		t.Fatalf("非法节点应让整份计划失败并说明: %v", err)
	}
	if got := asList(mustBoard(t, srv, wf, map[string]any{})["modules"]); len(got) != 2 {
		t.Fatalf("失败的计划不应留下「新模块」: %#v", got)
	}

	// reorder：按给定顺序，只列出的节点前移；顺序变化也算 applied。
	res = mustBoard(t, srv, wf, map[string]any{"action": "plan", "reorder": true, "modules": []any{map[string]any{"name": "任务"}}})
	tree = asList(res["tree"])
	if res["status"] != "applied" || res["reordered"] != true || asMap(tree[0])["path"] != "任务" {
		t.Fatalf("reorder 应把「任务」排到最前: %#v", res)
	}
	res = mustBoard(t, srv, wf, map[string]any{"action": "plan", "modules": []any{map[string]any{"name": "终端"}}})
	if res["status"] != "unchanged" || asMap(asList(res["tree"])[0])["path"] != "任务" {
		t.Fatalf("不带 reorder 时不应改变顺序: %#v", res)
	}

	// 参数错误。
	if _, err := boardRaw(t, srv, wf, map[string]any{"action": "plan"}); err == nil || !strings.Contains(err.Error(), "modules is required") {
		t.Fatalf("缺 modules 应报错并带示例: %v", err)
	}
	if _, err := boardRaw(t, srv, "", map[string]any{"action": "plan", "modules": samplePlan}); err == nil || !strings.Contains(err.Error(), "workflow_id is required") {
		t.Fatalf("缺 workflow_id 应报错: %v", err)
	}
	if _, err := boardRaw(t, srv, wf, map[string]any{"action": "nope"}); err == nil || !strings.Contains(err.Error(), "valid actions") {
		t.Fatalf("未知 action 应列出可用值: %v", err)
	}
}

func TestTaskBoardBatchCreateUpdateAndPartialFailure(t *testing.T) {
	srv, events := setupWorkItemMCP(t)
	const wf = "wf-batch"

	// 规划 + 建任务：一次调用。
	res := mustBoard(t, srv, wf, map[string]any{"action": "batch", "modules": samplePlan, "ops": []any{
		map[string]any{"title": "历史日志平铺", "module": "终端/历史日志", "kind": "feature", "state": "doing", "percent": 40},
		map[string]any{"title": "输入法修复", "module": "输入法", "kind": "bug"}, // 全树唯一的功能名可以省略模块
		map[string]any{"title": "任务页", "module": "任务", "labels": []string{"ui"}},
		map[string]any{"title": "没归类的"},
	}})
	if res["status"] != "ok" || toInt(asMap(res["summary"])["created"]) != 4 || asMap(res["plan"])["changed"] != true {
		t.Fatalf("规划 + 建任务: %#v", res)
	}
	rs := resultsOf(res)
	if len(rs) != 4 || rs[0]["ref"] != "#1" || rs[0]["module"] != "终端/历史日志" || toInt(rs[0]["percent"]) != 40 || rs[0]["state"] != "doing" {
		t.Fatalf("逐条回执不对: %#v", rs)
	}
	if rs[1]["module"] != "终端/输入法" || rs[3]["ref"] != "#4" {
		t.Fatalf("功能名简写 / 序号不对: %#v", rs)
	}
	if _, has := rs[3]["module"]; has {
		t.Fatalf("没归类的任务回执里不应有 module: %#v", rs[3])
	}
	// 一次批量只广播一次（不带具体任务编号）。
	var itemEvents []workEvent
	for _, e := range *events {
		if e.Name == "work_item_updated" {
			itemEvents = append(itemEvents, e)
		}
	}
	if len(itemEvents) != 1 || asMap(itemEvents[0].Data)["bulk"] != true || asMap(itemEvents[0].Data)["workflow_id"] != wf {
		t.Fatalf("批量写入应只广播一次 work_item_updated(bulk): %#v", itemEvents)
	}

	// 成批更新 + 部分失败：task_ids 套同一个改动；验证通过的「done」带结论直接生效；不存在的任务 / 模块各自报错，不拖累其余。
	res = mustBoard(t, srv, wf, map[string]any{"action": "batch", "ops": []any{
		map[string]any{"task_ids": []int{1, 2}, "state": "review", "conclusion": "完成并验证"},
		map[string]any{"task_id": 3, "state": "done", "conclusion": "回归 + e2e 通过，已验证"},
		map[string]any{"task_id": 99, "state": "doing"},
		map[string]any{"title": "x", "module": "不存在的模块"},
		map[string]any{"title": "y", "module": "不存在的模块"},
	}})
	rs = resultsOf(res)
	if res["status"] != "partial" || toInt(asMap(res["summary"])["updated"]) != 3 || toInt(asMap(res["summary"])["failed"]) != 3 {
		t.Fatalf("应是 partial（3 成功 3 失败）: %#v", res)
	}
	if len(rs) != 6 || rs[0]["ref"] != "#1" || rs[0]["action"] != "updated" || rs[0]["state"] != "review" || rs[1]["ref"] != "#2" {
		t.Fatalf("task_ids 应逐个回执: %#v", rs)
	}
	if rs[2]["state"] != "done" || rs[2]["action"] != "updated" {
		t.Fatalf("智能体验证通过后传 done（带结论）应直接生效: %#v", rs[2])
	}
	if _, has := rs[2]["state_note"]; has {
		t.Fatalf("状态不再被改写，回执里不应有 state_note: %#v", rs[2])
	}
	if rs[3]["ok"] != false || rs[3]["ref"] != "#99" || !strings.Contains(fmt.Sprint(rs[3]["error"]), "#99") {
		t.Fatalf("不存在的任务应单独报错: %#v", rs[3])
	}
	first, second := fmt.Sprint(rs[4]["error"]), fmt.Sprint(rs[5]["error"])
	if !strings.Contains(first, "已有：") || !strings.Contains(first, "终端/历史日志") || !strings.Contains(first, "task_board action=plan") {
		t.Fatalf("写错的模块路径第一次应列出已有路径并提示先 plan: %s", first)
	}
	if !strings.Contains(second, "同上") || strings.Contains(second, "终端/历史日志") {
		t.Fatalf("同一个写错的路径第二次只需简短提示: %s", second)
	}
	// 成功的更新真的生效了。
	got, err := srv.store.GetWorkItemBySeq(context.Background(), wf, 1)
	if err != nil || got.State != "review" || got.Conclusion != "完成并验证" {
		t.Fatalf("批量更新没有生效: %+v %v", got, err)
	}

	// 没带结论的 done：只这一条被拒绝并说清怎么写，状态不动
	res = mustBoard(t, srv, wf, map[string]any{"action": "batch", "ops": []any{
		map[string]any{"task_id": 4, "state": "done"},
	}})
	rs = resultsOf(res)
	if len(rs) != 1 || rs[0]["ok"] != false || !strings.Contains(fmt.Sprint(rs[0]["error"]), "要同时写结论") {
		t.Fatalf("没有结论的 done 应被拒绝并说清怎么写: %#v", rs)
	}
	if t4, err := srv.store.GetWorkItemBySeq(context.Background(), wf, 4); err != nil || t4.State == "done" {
		t.Fatalf("被拒绝的 done 不应改动状态: %+v %v", t4, err)
	}

	// 去重：同模块下同名的未结束任务不重复创建；不同模块允许同名。
	res = mustBoard(t, srv, wf, map[string]any{"action": "batch", "ops": []any{
		map[string]any{"title": "历史日志平铺", "module": "终端/历史日志"},
		map[string]any{"title": " 历史日志平铺 ", "module": "任务"},
	}})
	rs = resultsOf(res)
	if rs[0]["action"] != "exists" || rs[0]["ref"] != "#1" || rs[1]["action"] != "created" || toInt(asMap(res["summary"])["exists"]) != 1 {
		t.Fatalf("去重不对: %#v", rs)
	}

	// 取消归类 / 改模块：module="" 取消；整理分类不改 updated_at。
	before, _ := srv.store.GetWorkItemBySeq(context.Background(), wf, 4)
	res = mustBoard(t, srv, wf, map[string]any{"action": "batch", "ops": []any{map[string]any{"task_id": 4, "module": "任务"}, map[string]any{"task_id": 3, "module": ""}}})
	rs = resultsOf(res)
	if rs[0]["module"] != "任务" || rs[1]["ref"] != "#3" {
		t.Fatalf("改模块: %#v", rs)
	}
	if _, has := rs[1]["module"]; has {
		t.Fatalf("module=\"\" 应取消归类: %#v", rs[1])
	}
	after, _ := srv.store.GetWorkItemBySeq(context.Background(), wf, 4)
	if !after.UpdatedAt.Equal(before.UpdatedAt) {
		t.Fatalf("只改模块不应改动 updated_at: %v → %v", before.UpdatedAt, after.UpdatedAt)
	}

	// 全部失败 → failed；参数错误。
	res = mustBoard(t, srv, wf, map[string]any{"action": "batch", "ops": []any{map[string]any{"task_id": 98, "state": "doing"}}})
	if res["status"] != "failed" {
		t.Fatalf("全部失败应是 failed: %#v", res)
	}
	res = mustBoard(t, srv, wf, map[string]any{"action": "batch", "ops": []any{
		map[string]any{"task_ids": []int{1, 2}, "title": "不能同时改名"},
		map[string]any{"state": "todo"},
	}})
	rs = resultsOf(res)
	if !strings.Contains(fmt.Sprint(rs[0]["error"]), "单个任务") || !strings.Contains(fmt.Sprint(rs[1]["error"]), "需要 title") {
		t.Fatalf("多任务改名 / 缺 title 的提示不对: %#v", rs)
	}
	ops := make([]any, 31)
	for i := range ops {
		ops[i] = map[string]any{"title": fmt.Sprintf("t%d", i)}
	}
	if _, err := boardRaw(t, srv, wf, map[string]any{"action": "batch", "ops": ops}); err == nil || !strings.Contains(err.Error(), "最多 30") {
		t.Fatalf("超过 30 条应拒绝: %v", err)
	}
	if _, err := boardRaw(t, srv, wf, map[string]any{"action": "batch"}); err == nil || !strings.Contains(err.Error(), "ops is required") {
		t.Fatalf("缺 ops 应报错并带示例: %v", err)
	}

	// 规划失败时 ops 一条也不执行（整份计划原子）。
	n := len(mustBoard(t, srv, wf, map[string]any{"action": "list", "include_closed": true})["tasks"].([]any))
	_, err = boardRaw(t, srv, wf, map[string]any{"action": "batch",
		"modules": []any{map[string]any{"name": "bad>name"}},
		"ops":     []any{map[string]any{"title": "不该被创建"}},
	})
	if err == nil || !strings.Contains(err.Error(), "ops 都没有执行") {
		t.Fatalf("规划失败应整体失败: %v", err)
	}
	if m := len(mustBoard(t, srv, wf, map[string]any{"action": "list", "include_closed": true})["tasks"].([]any)); m != n {
		t.Fatalf("规划失败时不应创建任务: %d → %d", n, m)
	}
}

func TestTaskBoardOverviewAndList(t *testing.T) {
	srv, _ := setupWorkItemMCP(t)
	const wf = "wf-overview"
	mustBoard(t, srv, wf, map[string]any{"action": "batch", "modules": samplePlan, "ops": []any{
		map[string]any{"title": "t1 历史日志", "module": "终端/历史日志", "state": "doing", "percent": 40, "progress": "xterm 最后一屏"},
		map[string]any{"title": "t2 输入法", "module": "终端/输入法"},
		map[string]any{"title": "t3 任务页", "module": "任务", "state": "review"},
		map[string]any{"title": "t4 没归类"},
		map[string]any{"title": "t5 终端直属", "module": "终端", "state": "review"},
	}})
	// 这里不关心「谁有资格置已完成」（那是 work_items 的规则测试），只是造一条已完成的任务，用人的身份直接置 done。
	t5, err := srv.store.GetWorkItemBySeq(context.Background(), wf, 5)
	if err != nil {
		t.Fatal(err)
	}
	doneState := "done"
	if _, err := srv.store.UpdateWorkItem(context.Background(), t5.ID, store.WorkItemPatch{State: &doneState}, store.WorkActor{Kind: model.WorkActorHuman}); err != nil {
		t.Fatal(err)
	}

	ov := mustBoard(t, srv, wf, map[string]any{})
	counts := asMap(ov["counts"])
	if toInt(counts["todo"]) != 2 || toInt(counts["doing"]) != 1 || toInt(counts["review"]) != 1 || toInt(counts["done"]) != 1 || toInt(counts["open"]) != 4 || toInt(counts["total"]) != 5 {
		t.Fatalf("概览计数: %#v", counts)
	}
	tree := asList(ov["modules"])
	term := asMap(tree[0])
	// 终端 = t1(40) + t2(0) + t5(100) 三个叶子任务：open 2 / done 1 / total 3，平均 (40+0+100)/3 = 47。
	if term["path"] != "终端" || toInt(term["total"]) != 3 || toInt(term["open"]) != 2 || toInt(term["done"]) != 1 || toInt(term["percent"]) != 47 {
		t.Fatalf("终端模块汇总: %#v", term)
	}
	if un := asMap(ov["unassigned"]); toInt(un["total"]) != 1 || toInt(un["open"]) != 1 {
		t.Fatalf("未归类汇总: %#v", ov["unassigned"])
	}
	focus := asList(ov["focus"])
	if len(focus) != 2 || asMap(focus[0])["state"] == "todo" {
		t.Fatalf("focus 只含进行中与验证中: %#v", focus)
	}
	hist := asMap(asList(term["features"])[0])
	if hist["path"] != "终端/历史日志" || toInt(hist["percent"]) != 40 {
		t.Fatalf("功能汇总: %#v", hist)
	}
	if len(asList(ov["todo_preview"])) != 2 || ov["todo_more"] != nil {
		t.Fatalf("todo 预览: %#v", ov["todo_preview"])
	}

	// list 默认按规划树顺序（未归类最后），同模块内 进行中 → 待确认 → 待办。
	res := mustBoard(t, srv, wf, map[string]any{"action": "list"})
	if got := strings.Join(taskTitles(res), "|"); got != "t1 历史日志|t2 输入法|t3 任务页|t4 没归类" {
		t.Fatalf("plan 顺序: %s", got)
	}
	if res["sort"] != "plan" || toInt(res["count"]) != 4 {
		t.Fatalf("list 回执: %#v", res)
	}
	first := asMap(asList(res["tasks"])[0])
	if first["ref"] != "#1" || first["module"] != "终端/历史日志" || toInt(first["percent"]) != 40 || first["progress"] != "xterm 最后一屏" {
		t.Fatalf("slim 任务形状: %#v", first)
	}
	if _, has := first["labels"]; has {
		t.Fatalf("列表里的任务不带 labels（完整内容用 task_get）: %#v", first)
	}
	// module 筛选包含功能里的任务；include_closed 带上已完成。
	if got := strings.Join(taskTitles(mustBoard(t, srv, wf, map[string]any{"action": "list", "module": "终端"})), "|"); got != "t1 历史日志|t2 输入法" {
		t.Fatalf("模块筛选: %s", got)
	}
	if got := strings.Join(taskTitles(mustBoard(t, srv, wf, map[string]any{"action": "list", "module": "终端", "include_closed": true})), "|"); got != "t5 终端直属|t1 历史日志|t2 输入法" {
		t.Fatalf("模块直属任务应排在功能之前: %s", got)
	}
	if got := strings.Join(taskTitles(mustBoard(t, srv, wf, map[string]any{"action": "list", "module": "未归类"})), "|"); got != "t4 没归类" {
		t.Fatalf("未归类筛选: %s", got)
	}
	if got := taskTitles(mustBoard(t, srv, wf, map[string]any{"action": "list", "module": "终端/历史日志"})); len(got) != 1 {
		t.Fatalf("功能筛选: %v", got)
	}
	if _, err := boardRaw(t, srv, wf, map[string]any{"action": "list", "module": "终端/不存在"}); err == nil || !strings.Contains(err.Error(), "已有：") {
		t.Fatalf("不存在的模块应列出已有路径: %v", err)
	}
	// recent：最近写入的在前（t4 最后创建）。
	rec := mustBoard(t, srv, wf, map[string]any{"action": "list", "sort": "recent"})
	if got := taskTitles(rec); got[0] != "t4 没归类" || rec["sort"] != "recent" {
		t.Fatalf("recent 顺序: %v", got)
	}
	// 更新 t2 之后它排到 recent 最前。
	mustBoard(t, srv, wf, map[string]any{"action": "batch", "ops": []any{map[string]any{"task_id": 2, "progress": "开始做了"}}})
	if got := taskTitles(mustBoard(t, srv, wf, map[string]any{"action": "list", "sort": "recent"})); got[0] != "t2 输入法" {
		t.Fatalf("更新后 t2 应排在 recent 最前: %v", got)
	}
	// limit 截断。
	res = mustBoard(t, srv, wf, map[string]any{"action": "list", "limit": 2})
	if toInt(res["count"]) != 2 || toInt(res["total"]) != 4 || res["truncated"] != true || !strings.Contains(fmt.Sprint(res["hint"]), "缩小范围") {
		t.Fatalf("limit 截断: %#v", res)
	}
}

// 兼容：workflow_context 的 task_create / task_update 也能带模块与百分比，并共用同一套校验与提示。
func TestWorkflowContextTaskModuleAndPercent(t *testing.T) {
	srv, _ := setupWorkItemMCP(t)
	const wf = "wf-compat"
	call := func(args map[string]any) map[string]any {
		args["workflow_id"] = wf
		return callToolJSON(t, srv, "workflow_context", args)
	}
	ctx := context.Background()

	// 还没有模块规划时，写模块名要得到可操作的提示。
	_, err := srv.handleTaskCreate(ctx, workflowContextArgs{WorkflowID: wf, Title: "x", Module: strPtr("终端")})
	if err == nil || !strings.HasPrefix(err.Error(), "task_create: ") || !strings.Contains(err.Error(), "task_board action=plan") {
		t.Fatalf("没有规划时应提示先 plan: %v", err)
	}
	mustBoard(t, srv, wf, map[string]any{"action": "plan", "modules": samplePlan})

	res := call(map[string]any{"action": "task_create", "title": "归类的任务", "module": "终端/历史日志", "percent": 30})
	task := taskOf(t, res)
	if task["module"] != "终端/历史日志" || toInt(task["percent"]) != 30 {
		t.Fatalf("task_create 应带 module / percent: %#v", task)
	}
	// 字符串形式的数字也接受（"60%"）。
	res = call(map[string]any{"action": "task_update", "task_id": 1, "percent": "60%"})
	if toInt(taskOf(t, res)["percent"]) != 60 {
		t.Fatalf("percent 应容忍 \"60%%\": %#v", res)
	}
	res = call(map[string]any{"action": "task_update", "task_id": 1, "module": "任务"})
	if taskOf(t, res)["module"] != "任务" {
		t.Fatalf("task_update 改模块: %#v", res)
	}
	res = call(map[string]any{"action": "task_update", "task_id": 1, "module": ""})
	if _, has := taskOf(t, res)["module"]; has {
		t.Fatalf("module=\"\" 应取消归类: %#v", res)
	}
	res = call(map[string]any{"action": "task_update", "task_id": 1, "percent": -1})
	if toInt(taskOf(t, res)["percent"]) != 0 { // 清除后由状态推导（待办 = 0）
		t.Fatalf("percent=-1 应清除显式值: %#v", res)
	}
	_, err = srv.handleTaskUpdate(ctx, workflowContextArgs{WorkflowID: wf, TaskID: 1, Module: strPtr("终端/不存在")})
	if err == nil || !strings.Contains(err.Error(), "已有：") {
		t.Fatalf("不存在的模块应列出已有路径: %v", err)
	}
	p := looseInt(150)
	if _, err = srv.handleTaskUpdate(ctx, workflowContextArgs{WorkflowID: wf, TaskID: 1, Percent: &p}); err == nil || !strings.Contains(err.Error(), "percent") {
		t.Fatalf("percent 越界应报错: %v", err)
	}
	// task_get 详情同样带 module / percent。
	call(map[string]any{"action": "task_update", "task_id": 1, "module": "终端", "percent": 80})
	got := taskOf(t, call(map[string]any{"action": "task_get", "task_id": 1}))
	if got["module"] != "终端" || toInt(got["percent"]) != 80 {
		t.Fatalf("task_get 详情: %#v", got)
	}
}

func strPtr(s string) *string { return &s }

// 模型常把整数写成字符串：task_id "#1"、task_ids ["2"]、percent "60%"、limit "1" 都要能读懂。
func TestTaskBoardAcceptsLooseIntegers(t *testing.T) {
	srv, _ := setupWorkItemMCP(t)
	const wf = "wf-loose"
	mustBoard(t, srv, wf, map[string]any{"action": "batch", "ops": []any{map[string]any{"title": "甲"}, map[string]any{"title": "乙"}}})
	res := mustBoard(t, srv, wf, map[string]any{"action": "batch", "ops": []any{
		map[string]any{"task_id": "#1", "state": "doing", "percent": "60%"},
		map[string]any{"task_ids": []any{"2", 2.0}, "progress": "写了一半"},
	}})
	rs := resultsOf(res)
	if res["status"] != "ok" || len(rs) != 3 || rs[0]["state"] != "doing" || toInt(rs[0]["percent"]) != 60 {
		t.Fatalf("宽松整数: %#v", res)
	}
	if got := taskTitles(mustBoard(t, srv, wf, map[string]any{"action": "list", "limit": "1"})); len(got) != 1 {
		t.Fatalf("limit 字符串: %v", got)
	}
	// 真正不是整数的要明确报错。
	raw := json.RawMessage(fmt.Sprintf(`{"workflow_id":%q,"action":"batch","ops":[{"task_id":"abc"}]}`, wf))
	if _, err := srv.handleTaskBoard(context.Background(), raw); err == nil || !strings.Contains(err.Error(), "invalid arguments") {
		t.Fatalf("非整数应报 invalid arguments: %v", err)
	}
}

func TestTaskBoardToolVisibilityPermissionsAndInstructions(t *testing.T) {
	srv, _ := setupWorkItemMCP(t)
	ctx := context.Background()
	has := func(perms model.Permissions) bool {
		for _, d := range GetToolDefinitionsForPermissions(perms) {
			if d.Name == "task_board" {
				return true
			}
		}
		return false
	}
	if !has(model.Permissions{Feedback: true}) || !has(model.AllPermissions()) {
		t.Fatal("带反馈权限的令牌应能看到 task_board")
	}
	if has(model.Permissions{Terminal: true}) || has(model.Permissions{TerminalFiles: true}) {
		t.Fatal("只有终端权限的令牌不应看到 task_board")
	}
	if !(model.Permissions{Feedback: true}).AllowsTool("task_board") || !(model.Permissions{Sessions: true}).AllowsTool("task_board") {
		t.Fatal("task_board 与 workflow_context 同权限")
	}
	if (model.Permissions{Terminal: true}).AllowsTool("task_board") || (model.Permissions{SystemInfo: true}).AllowsTool("task_board") {
		t.Fatal("没有反馈 / 会话权限的令牌不应能调用 task_board")
	}

	// 描述要让「还没有清单」的模型也知道该用它。
	var desc string
	for _, d := range GetToolDefinitionsForPermissions(model.AllPermissions()) {
		if d.Name == "task_board" {
			desc = d.Description
		}
	}
	for _, want := range []string{"EMPTY board", `action="overview"`, "how-to", "plan", "batch", "bound to your own development flow", "CLOSED list", "Web \"任务\" page"} {
		if !strings.Contains(desc, want) {
			t.Errorf("task_board 描述里应说明 %q", want)
		}
	}

	// initialize 指引：能用 task_board 的令牌收到「任务清单」章节，终端档不收。
	gov := srv.InstructionsForPermissions(ctx, model.Permissions{Feedback: true})
	for _, want := range []string{"10. 任务清单", "task_board", "action=plan", "action=batch", "tasks 行"} {
		if !strings.Contains(gov, want) {
			t.Errorf("治理档指引里应包含 %q", want)
		}
	}
	if !strings.HasPrefix(gov, store.DefaultServerInstructions[:200]) {
		t.Error("任务清单章节应追加在全局指引之后")
	}
	if strings.Contains(srv.InstructionsForPermissions(ctx, model.Permissions{Terminal: true}), "task_board") {
		t.Error("终端档指引不应提到 task_board")
	}
	hybrid := srv.InstructionsForPermissions(ctx, model.Permissions{Feedback: true, Terminal: true})
	if !strings.Contains(hybrid, "terminal_exec") || !strings.Contains(hybrid, "task_board") || strings.Index(hybrid, "terminal_exec") > strings.Index(hybrid, "10. 任务清单") {
		t.Error("混合档指引应先终端章节、后任务清单章节")
	}
	if strings.Contains(srv.InstructionsForPermissions(ctx, model.Permissions{SystemInfo: true}), "10. 任务清单") {
		t.Error("没有反馈 / 会话权限的令牌不应收到任务清单章节")
	}
}

// 工具声明与调度表保持同步：声明的每个 action 都能被调度，每个参数都有声明。
func TestTaskBoardSchemaMatchesDispatcher(t *testing.T) {
	srv, _ := setupWorkItemMCP(t)
	var schema struct {
		Required   []string `json:"required"`
		Properties map[string]struct {
			Enum []string `json:"enum"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(taskBoardSchema, &schema); err != nil {
		t.Fatal(err)
	}
	if len(schema.Required) != 1 || schema.Required[0] != "workflow_id" {
		t.Errorf("只有 workflow_id 是必填（action 默认 overview）: %v", schema.Required)
	}
	actions := schema.Properties["action"].Enum
	if strings.Join(actions, ",") != "overview,list,plan,batch" {
		t.Fatalf("action 枚举: %v", actions)
	}
	for _, a := range actions {
		_, err := boardRaw(t, srv, "wf-schema", map[string]any{"action": a})
		if err != nil && strings.Contains(err.Error(), "unknown action") {
			t.Errorf("声明了 action %q，但调度表不认识它", a)
		}
	}
	for _, p := range []string{"action", "workflow_id", "session_id", "module", "states", "include_closed", "kind", "query", "sort", "limit", "modules", "reorder", "ops"} {
		if _, ok := schema.Properties[p]; !ok {
			t.Errorf("工具声明缺少参数 %s", p)
		}
	}
	// 声明里的每个 JSON 键都必须能被参数结构体读到（防止声明了却被悄悄忽略）。
	var full struct {
		Properties map[string]json.RawMessage `json:"properties"`
	}
	_ = json.Unmarshal(taskBoardSchema, &full)
	probe := map[string]any{}
	for k := range full.Properties {
		probe[k] = nil
	}
	raw, _ := json.Marshal(probe)
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	var args taskBoardArgs
	if err := dec.Decode(&args); err != nil {
		t.Errorf("声明里有参数结构体读不到: %v", err)
	}
}

// 回执头里的 tasks 行：没有清单时是提醒，有清单时是概况。
func TestTaskDigestHeaderInReceipts(t *testing.T) {
	srv := setupTestMCPServer(t)
	ctx := context.Background()
	const wf = "wf-header"

	empty := callToolText(t, srv, "interactive_feedback", feedbackArgs(wf, ""))
	if !strings.Contains(empty, "tasks: 本工作流还没有任务清单") || !strings.Contains(empty, "task_board") {
		t.Fatalf("没有清单时回执头应提醒使用 task_board:\n%s", empty)
	}
	if strings.Index(empty, "tasks:") > strings.Index(empty, "=== ") {
		t.Fatalf("tasks 行必须位于响应头内、标记行之前:\n%s", empty)
	}
	completeLatestPending(t, srv, wf)

	// 只有模块、没有任务。
	if _, err := srv.store.ApplyWorkPlan(ctx, wf, []store.WorkPlanNode{{Name: "终端"}, {Name: "任务"}}, false); err != nil {
		t.Fatal(err)
	}
	line := srv.taskDigestHeader(ctx, wf)
	if !strings.Contains(line, "已规划 2 个模块") || !strings.Contains(line, "batch") {
		t.Fatalf("只有模块时的提示: %s", line)
	}

	// 有任务：各状态数量 + 最近的未结束任务。
	term, _ := srv.store.ResolveWorkModule(ctx, wf, "终端")
	agent, human := store.WorkActor{Kind: model.WorkActorAgent}, store.WorkActor{Kind: model.WorkActorHuman}
	mk := func(actor store.WorkActor, title, state string, mod uint, pct *int) {
		if _, err := srv.store.CreateWorkItem(ctx, store.WorkItemInput{WorkflowID: wf, Title: title, State: state, ModuleID: mod, Percent: pct}, actor); err != nil {
			t.Fatal(err)
		}
		time.Sleep(5 * time.Millisecond)
	}
	longTitle := "历史日志平铺" + strings.Repeat("很长", 20)
	mk(human, "已经做完的事", "done", 0, nil) // 智能体新建任务不能直接是「已完成」，补记已做完的事走人的入口
	mk(agent, "验证中的事", "review", 0, nil)
	mk(agent, "待办一", "todo", 0, nil)
	mk(agent, longTitle, "doing", term.ID, intPtr(60))
	line = srv.taskDigestHeader(ctx, wf)
	for _, want := range []string{"tasks: ", "进行中 1", "验证中 1", "待办 1", "共 4，已完成 1", "最近 #4 进行中 60%", "〔终端〕", "task_board 查看 / 成批更新"} {
		if !strings.Contains(line, want) {
			t.Fatalf("概况行应包含 %q: %s", want, line)
		}
	}
	if strings.Contains(line, strings.Repeat("很长", 20)) || strings.Contains(line, "\n") {
		t.Fatalf("标题应截断、整行必须是单行: %s", line)
	}
	next := callToolText(t, srv, "interactive_feedback", feedbackArgs(wf, ""))
	if !strings.Contains(next, "tasks: 进行中 1 · 验证中 1 · 待办 1") {
		t.Fatalf("回执头应回显任务概况:\n%s", next)
	}

	// 没有未结束任务。
	wf2 := "wf-header-done"
	if _, err := srv.store.CreateWorkItem(ctx, store.WorkItemInput{WorkflowID: wf2, Title: "完成的", State: "done"}, store.WorkActor{Kind: model.WorkActorHuman}); err != nil {
		t.Fatal(err)
	}
	if line := srv.taskDigestHeader(ctx, wf2); !strings.Contains(line, "无未结束任务") || strings.Contains(line, "最近") {
		t.Fatalf("没有未结束任务时的概况: %s", line)
	}
}

// 没有变化的调用不广播：重复的计划、全部失败的批量、什么都没改的更新。
func TestTaskBoardBroadcastsOnlyRealChanges(t *testing.T) {
	srv, events := setupWorkItemMCP(t)
	const wf = "wf-events"
	count := func() int { return len(*events) }

	mustBoard(t, srv, wf, map[string]any{"action": "batch", "ops": []any{map[string]any{"title": "唯一的任务"}}})
	base := count()
	mustBoard(t, srv, wf, map[string]any{"action": "batch", "ops": []any{map[string]any{"task_id": 1, "state": "todo"}}})   // 没变化
	mustBoard(t, srv, wf, map[string]any{"action": "batch", "ops": []any{map[string]any{"task_id": 77, "state": "doing"}}}) // 失败
	mustBoard(t, srv, wf, map[string]any{"action": "batch", "ops": []any{map[string]any{"title": "唯一的任务"}}})                // 去重 exists
	mustBoard(t, srv, wf, map[string]any{"action": "list"})                                                                 // 只读
	mustBoard(t, srv, wf, map[string]any{})                                                                                 // 只读
	if count() != base {
		t.Fatalf("没有真实变化的调用不应广播: %d → %d (%#v)", base, count(), (*events)[base:])
	}
	mustBoard(t, srv, wf, map[string]any{"action": "batch", "ops": []any{map[string]any{"task_id": 1, "state": "doing"}}})
	if count() != base+1 || (*events)[base].Name != "work_item_updated" {
		t.Fatalf("真实变化应广播一次: %#v", (*events)[base:])
	}
}
