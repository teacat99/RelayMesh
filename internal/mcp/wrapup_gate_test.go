package mcp

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/teacat99/RelayMesh/v2/internal/store"
)

// 严格收尾门（#232 D2，D-375）在智能体能看到的几个位置：回执 window: 行、回执 tasks: 行、task_board overview。

func gateCall(t *testing.T, srv *Server, tool string, args map[string]any) string {
	t.Helper()
	return resultText(inboxCall(t, srv, asyncCred(), tool, args))
}

func gateNotice(t *testing.T, srv *Server, wf string) string {
	t.Helper()
	return gateCall(t, srv, "interactive_feedback", map[string]any{
		"workflow_id": wf, "summary": "进度", "title": "进度", "project_directory": asyncTestProject, "wait": false,
	})
}

// gateLine 取回执里以 prefix 开头的那一行（找不到返回空串）。
func gateLine(text, prefix string) string {
	for _, l := range strings.Split(text, "\n") {
		if strings.HasPrefix(l, prefix) {
			return l
		}
	}
	return ""
}

// seedGateTasks 建一个模块并按状态建任务，返回按创建顺序的编号（#1 起）。
func seedGateTasks(t *testing.T, srv *Server, wf string, states ...string) {
	t.Helper()
	plan := gateCall(t, srv, "task_board", map[string]any{"action": "plan", "workflow_id": wf, "modules": []map[string]any{{"name": "核心", "features": []map[string]any{{"name": "功能甲"}}}}})
	if strings.Contains(plan, "error") {
		t.Fatalf("建模块失败: %s", plan)
	}
	for i, st := range states {
		out := gateCall(t, srv, "task_board", map[string]any{"action": "batch", "workflow_id": wf, "ops": []map[string]any{
			{"op": "create", "title": "任务" + string(rune('A'+i)), "module": "核心/功能甲", "description": "测试用", "state": st},
		}})
		if !strings.Contains(out, `"created": 1`) {
			t.Fatalf("建任务失败: %s", out)
		}
	}
}

func TestStrictGateReceiptLines(t *testing.T) {
	srv := setupTestMCPServer(t)
	const wf = "wf-strict-gate"
	seedRound(t, srv, wf)
	enableAsync(t, srv, wf)
	seedGateTasks(t, srv, wf, "todo", "doing")

	notice := gateNotice(t, srv, wf)
	win := gateLine(notice, "window:")
	for _, want := range []string{"回复窗口已暂停", "收尾门开着", "2 个未完成任务挡着（#1 #2）", "确认卡（async_ask）"} {
		if !strings.Contains(win, want) {
			t.Fatalf("window: 行缺少 %q：\n%s", want, notice)
		}
	}
	tasks := gateLine(notice, "tasks:")
	if !strings.Contains(tasks, "收尾门：2 个未完成任务挡着收尾（#1 #2）") {
		t.Fatalf("tasks: 行应带收尾门提示：\n%s", tasks)
	}
	if strings.Contains(tasks, "暂缓未写缘由") {
		t.Fatalf("没有暂缓任务不该有暂缓提醒：\n%s", tasks)
	}

	// 把两个任务都做完 → 收尾门提示消失，窗口开始走
	for _, id := range []int{1, 2} {
		gateCall(t, srv, "task_board", map[string]any{"action": "batch", "workflow_id": wf, "ops": []map[string]any{
			{"op": "update", "task_id": id, "state": "done", "conclusion": "测试完成"},
		}})
	}
	w, _, err := srv.store.ReconcileReplyWindow(context.Background(), wf, time.Now())
	if err != nil || w.Until == nil || w.ActiveTasks != 0 {
		t.Fatalf("任务清零后窗口应开始走: %+v %v", w, err)
	}
	after := gateNotice(t, srv, wf)
	if strings.Contains(gateLine(after, "tasks:"), "收尾门") || strings.Contains(gateLine(after, "window:"), "已暂停") {
		t.Fatalf("清零后不该再有收尾门提示：\n%s", after)
	}
}

// 暂缓必须写缘由：刚置暂缓却没写 Q<N> / 「等」的，回执与概览温和提醒；补上缘由后提醒消失；暂缓本身不挡收尾。
func TestShelveReasonReminder(t *testing.T) {
	srv := setupTestMCPServer(t)
	const wf = "wf-shelve-reason"
	seedRound(t, srv, wf)
	enableAsync(t, srv, wf)
	seedGateTasks(t, srv, wf, "todo")
	gateCall(t, srv, "task_board", map[string]any{"action": "batch", "workflow_id": wf, "ops": []map[string]any{
		{"op": "update", "task_id": 1, "state": "shelved"},
	}})

	notice := gateNotice(t, srv, wf)
	tasks := gateLine(notice, "tasks:")
	if !strings.Contains(tasks, "暂缓未写缘由 #1") || !strings.Contains(tasks, "Q<N>") {
		t.Fatalf("刚暂缓没写缘由应被提醒：\n%s", tasks)
	}
	if strings.Contains(tasks, "收尾门：") {
		t.Fatalf("暂缓的任务不挡收尾，不该出现收尾门提示：\n%s", tasks)
	}
	if strings.Contains(gateLine(notice, "window:"), "已暂停") {
		t.Fatalf("只有暂缓任务时窗口不该暂停：\n%s", notice)
	}
	ov := gateCall(t, srv, "task_board", map[string]any{"action": "overview", "workflow_id": wf})
	if !strings.Contains(ov, "刚置「暂缓」的 #1 进展里没写缘由") {
		t.Fatalf("overview 的 warnings 里应有暂缓提醒：\n%s", ov)
	}
	if strings.Contains(ov, "wrapup_gate") {
		t.Fatalf("没有未完成任务时 overview 不该有 wrapup_gate：\n%s", ov)
	}

	gateCall(t, srv, "task_board", map[string]any{"action": "batch", "workflow_id": wf, "ops": []map[string]any{
		{"op": "update", "task_id": 1, "progress": "等用户在 Q83 拍板"},
	}})
	if got := gateLine(gateNotice(t, srv, wf), "tasks:"); strings.Contains(got, "暂缓未写缘由") {
		t.Fatalf("写了卡号之后提醒应消失：\n%s", got)
	}
}

func TestStrictGateOverviewAndSwitches(t *testing.T) {
	srv := setupTestMCPServer(t)
	const wf = "wf-gate-overview"
	seedRound(t, srv, wf)
	enableAsync(t, srv, wf)
	seedGateTasks(t, srv, wf, "todo", "review", "doing")
	ctx := context.Background()

	ov := gateCall(t, srv, "task_board", map[string]any{"action": "overview", "workflow_id": wf})
	for _, want := range []string{`"wrapup_gate"`, `"total": 3`, `"seq": 1`, `"seq": 3`, "这些任务清零前回复窗口不走", "置 shelved"} {
		if !strings.Contains(ov, want) {
			t.Fatalf("overview 缺少 %q：\n%s", want, ov)
		}
	}

	// 本次放行：tasks: 行不再说「挡着收尾」，window: 行说明是用户放行；overview 标 released
	if _, err := srv.store.ExtendReplyWindow(ctx, wf, 5*time.Minute, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := srv.store.ReleaseGate(ctx, wf, time.Now()); err != nil {
		t.Fatal(err)
	}
	notice := gateNotice(t, srv, wf)
	if strings.Contains(gateLine(notice, "tasks:"), "收尾门：") {
		t.Fatalf("放行后 tasks: 行不该再有收尾门提示：\n%s", notice)
	}
	if win := gateLine(notice, "window:"); !strings.Contains(win, "用户已「本次放行」收尾门") || !strings.Contains(win, "AwaitShell") {
		t.Fatalf("放行后 window: 行应照常倒计时并说明原因：\n%s", win)
	}
	if ov := gateCall(t, srv, "task_board", map[string]any{"action": "overview", "workflow_id": wf}); !strings.Contains(ov, `"released": true`) {
		t.Fatalf("overview 应标出已放行：\n%s", ov)
	}

	// 关掉收尾门：回到老口径，回执与 overview 都不再有任何收尾门字样；进行中的任务仍按老口径暂停窗口
	off := false
	if _, err := srv.store.UpdateWorkflowMode(ctx, wf, store.ModePatch{StrictWrapup: &off}, time.Now()); err != nil {
		t.Fatal(err)
	}
	notice = gateNotice(t, srv, wf)
	if strings.Contains(notice, "收尾门") {
		t.Fatalf("门关着时回执里不该出现收尾门字样：\n%s", notice)
	}
	if win := gateLine(notice, "window:"); !strings.Contains(win, "还有 2 个进行中 / 验证中的任务") {
		t.Fatalf("门关着时应是老文案（进行中 + 验证中共 2 个）：\n%s", win)
	}
	if ov := gateCall(t, srv, "task_board", map[string]any{"action": "overview", "workflow_id": wf}); strings.Contains(ov, "wrapup_gate") {
		t.Fatalf("门关着时 overview 不该有 wrapup_gate：\n%s", ov)
	}
}

// INV-2：同步会话（从没开过异步）的回执与概览与升级前逐字一致——没有任何收尾门 / 暂缓提醒字样。
func TestStrictGateSilentForSyncWorkflow(t *testing.T) {
	srv := setupTestMCPServer(t)
	const wf = "wf-gate-sync"
	seedRound(t, srv, wf)
	seedGateTasks(t, srv, wf, "todo", "doing", "shelved")

	got := gateCall(t, srv, "workflow_context", map[string]any{"action": "inbox", "workflow_id": wf})
	if strings.Contains(got, "收尾门") || strings.Contains(got, "暂缓未写缘由") {
		t.Fatalf("同步会话的回执不该有收尾门字样：\n%s", got)
	}
	ov := gateCall(t, srv, "task_board", map[string]any{"action": "overview", "workflow_id": wf})
	if strings.Contains(ov, "wrapup_gate") || strings.Contains(ov, "进展里没写缘由") {
		t.Fatalf("同步会话的 overview 不该有收尾门字样：\n%s", ov)
	}
}

// 反向软提醒：确认卡已答复、挂着它的任务还在暂缓、答复之后没人动过 → 回执 tasks: 行与 overview warnings 点名一次；
// 任务一被更新就消失；同步会话看不到。
func TestAnsweredCardReminder(t *testing.T) {
	srv := setupTestMCPServer(t)
	const wf = "wf-answered-nag"
	seedRound(t, srv, wf)
	enableAsync(t, srv, wf)
	seedGateTasks(t, srv, wf, "todo")
	out := askOK(t, srv, map[string]any{"workflow_id": wf, "title": "批准这个方案吗？", "options": twoOptions()})
	ref, _ := out["ref"].(string)
	if ref != "Q1" {
		t.Fatalf("第一张卡应是 Q1：%+v", out)
	}
	gateCall(t, srv, "task_board", map[string]any{"action": "batch", "workflow_id": wf, "ops": []map[string]any{
		{"op": "update", "task_id": 1, "state": "shelved", "progress": "等用户在 Q1 拍板"},
	}})

	// 卡还没答复：缘由写明了，也没有反向提醒
	tasks := gateLine(gateNotice(t, srv, wf), "tasks:")
	if strings.Contains(tasks, "确认卡已答复") || strings.Contains(tasks, "暂缓未写缘由") {
		t.Fatalf("卡未答复时不该有任何暂缓提醒：\n%s", tasks)
	}

	// 用户答复了，任务还停在暂缓：点名
	if _, err := srv.store.AnswerConfirmItem(context.Background(), wf, "Q1", store.ConfirmAnswerInput{OptionIDs: []string{"a"}, By: "human"}); err != nil {
		t.Fatal(err)
	}
	tasks = gateLine(gateNotice(t, srv, wf), "tasks:")
	for _, want := range []string{"确认卡已答复、任务还在暂缓 Q1→#1", "按答复接着做并置进行中"} {
		if !strings.Contains(tasks, want) {
			t.Fatalf("tasks: 行缺少 %q：\n%s", want, tasks)
		}
	}
	if strings.Contains(tasks, "收尾门：") {
		t.Fatalf("暂缓的任务不挡收尾：\n%s", tasks)
	}
	ov := gateCall(t, srv, "task_board", map[string]any{"action": "overview", "workflow_id": wf})
	if !strings.Contains(ov, "确认卡已答复、任务还在暂缓：Q1→#1") {
		t.Fatalf("overview 的 warnings 里应有反向提醒：\n%s", ov)
	}

	// 接着做（置进行中）后提醒消失；再放回暂缓时只要写了进展（任务被更新过）也不再点名
	gateCall(t, srv, "task_board", map[string]any{"action": "batch", "workflow_id": wf, "ops": []map[string]any{
		{"op": "update", "task_id": 1, "state": "doing", "progress": "Q1 已答复，按答复继续"},
	}})
	if got := gateLine(gateNotice(t, srv, wf), "tasks:"); strings.Contains(got, "确认卡已答复") {
		t.Fatalf("任务被更新后提醒应消失：\n%s", got)
	}
	gateCall(t, srv, "task_board", map[string]any{"action": "batch", "workflow_id": wf, "ops": []map[string]any{
		{"op": "update", "task_id": 1, "state": "shelved", "progress": "Q1 已答复，但还要等采样器结束"},
	}})
	if got := gateLine(gateNotice(t, srv, wf), "tasks:"); strings.Contains(got, "确认卡已答复") {
		t.Fatalf("答复之后写过进展的暂缓不该再被点名：\n%s", got)
	}

	// 同步会话（没开过异步）：回执不带这类提醒
	const sync = "wf-answered-nag-sync"
	seedRound(t, srv, sync)
	seedGateTasks(t, srv, sync, "shelved")
	askOK(t, srv, map[string]any{"workflow_id": sync, "title": "批准？", "options": twoOptions()})
	if _, err := srv.store.AnswerConfirmItem(context.Background(), sync, "Q1", store.ConfirmAnswerInput{OptionIDs: []string{"a"}, By: "human"}); err != nil {
		t.Fatal(err)
	}
	if got := gateNotice(t, srv, sync); strings.Contains(got, "确认卡已答复") {
		t.Fatalf("同步会话不该出现反向提醒：\n%s", got)
	}
}
