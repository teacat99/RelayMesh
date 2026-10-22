package mcp

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/teacat99/RelayMesh/v2/internal/store"
)

// 异步对话的「回复窗口」（#123）：新消息累加、用户回复清零、窗口行写进回执、同步工作流不受影响。
func TestReplyWindowLifecycle(t *testing.T) {
	srv := setupTestMCPServer(t)
	const wf = "wf-reply-window"
	seedRound(t, srv, wf)
	enableAsync(t, srv, wf)
	ctx := context.Background()

	notice := func(summary string) string {
		return resultText(inboxCall(t, srv, asyncCred(), "interactive_feedback", map[string]any{
			"workflow_id": wf, "summary": summary, "title": "进度", "project_directory": asyncTestProject, "wait": false,
		}))
	}
	untilOf := func() *time.Time {
		info, err := srv.store.WorkflowModeInfo(ctx, wf, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		return info.Row.ReplyWindowUntil
	}

	if untilOf() != nil {
		t.Fatal("还没发新消息，不该有回复窗口")
	}
	first := notice("第一条")
	if !strings.Contains(first, "window: 回复窗口剩余 0") || !strings.Contains(first, "AwaitShell") {
		t.Fatalf("发布回执里应带窗口行：\n%s", first)
	}
	u1 := untilOf()
	if u1 == nil || time.Until(*u1) < 4*time.Minute+50*time.Second || time.Until(*u1) > 5*time.Minute+5*time.Second {
		t.Fatalf("第一条应给窗口 +5 分钟，实际 %v", u1)
	}
	notice("第二条")
	u2 := untilOf()
	if u2 == nil || u2.Sub(*u1) < 4*time.Minute+50*time.Second {
		t.Fatalf("第二条应在原窗口上累加 5 分钟：%v → %v", u1, u2)
	}

	// 读收件箱：窗口还在走时，回执末尾告诉智能体怎么等
	inbox := resultText(inboxCall(t, srv, asyncCred(), "workflow_context", map[string]any{"action": "inbox", "workflow_id": wf}))
	if !strings.Contains(inbox, "window: 回复窗口剩余") {
		t.Fatalf("读收件箱应附窗口行：\n%s", inbox)
	}

	// 用户回复 → 窗口清零
	if _, err := srv.store.QueueWorkflowFeedback(ctx, store.QueueFeedbackInput{WorkflowID: wf, ResponseText: "我回来了"}); err != nil {
		t.Fatal(err)
	}
	if untilOf() != nil {
		t.Fatal("用户回复后窗口应清零")
	}
	if got := resultText(inboxCall(t, srv, asyncCred(), "workflow_context", map[string]any{"action": "inbox", "workflow_id": wf})); strings.Contains(got, "window:") {
		t.Fatalf("清零之后不该再有窗口行：\n%s", got)
	}
}

func TestReplyWindowNotCreatedForSyncWorkflow(t *testing.T) {
	srv := setupTestMCPServer(t)
	const wf = "wf-reply-window-sync"
	seedRound(t, srv, wf)
	if line := srv.extendReplyWindow(context.Background(), wf); line != "" {
		t.Fatalf("同步工作流不该有回复窗口: %s", line)
	}
}

// 用户（2026-10-15）：倒计时只在没有任务时触发，否则不算异步。有进行中的任务时窗口暂停，任务清零后接着走。
func TestReplyWindowPausedWhileTasksActive(t *testing.T) {
	srv := setupTestMCPServer(t)
	const wf = "wf-reply-window-tasks"
	seedRound(t, srv, wf)
	enableAsync(t, srv, wf)
	ctx := context.Background()
	call := func(tool string, args map[string]any) string {
		return resultText(inboxCall(t, srv, asyncCred(), tool, args))
	}
	plan := call("task_board", map[string]any{"action": "plan", "workflow_id": wf, "modules": []map[string]any{{"name": "核心", "features": []map[string]any{{"name": "功能甲"}}}}})
	if strings.Contains(plan, "error") {
		t.Fatalf("建模块失败: %s", plan)
	}
	created := call("task_board", map[string]any{"action": "batch", "workflow_id": wf, "ops": []map[string]any{{"op": "create", "title": "做事", "module": "核心/功能甲", "description": "测试用", "state": "doing"}}})
	if !strings.Contains(created, `"created": 1`) {
		t.Fatalf("建任务失败: %s", created)
	}

	notice := resultText(inboxCall(t, srv, asyncCred(), "interactive_feedback", map[string]any{
		"workflow_id": wf, "summary": "进度", "title": "进度", "project_directory": asyncTestProject, "wait": false,
	}))
	if !strings.Contains(notice, "window: 回复窗口已暂停") || strings.Contains(notice, "AwaitShell") {
		t.Fatalf("有任务在做时窗口应暂停：\n%s", notice)
	}
	w, _, err := srv.store.ReconcileReplyWindow(ctx, wf, time.Now())
	if err != nil || w.Until != nil || w.BankSec < 295 || w.ActiveTasks != 1 {
		t.Fatalf("应攒着 5 分钟且不走: %+v %v", w, err)
	}

	// 最后一个任务做完 → 窗口开始走（在任务工具调用结束的那一刻，不用等谁来读）
	done := call("task_board", map[string]any{"action": "batch", "workflow_id": wf, "ops": []map[string]any{{"op": "update", "task_id": 1, "state": "done", "conclusion": "测试完成"}}})
	if strings.Contains(done, "\"failed\": 1") {
		t.Fatalf("置完成失败: %s", done)
	}
	w, _, err = srv.store.ReconcileReplyWindow(ctx, wf, time.Now())
	if err != nil || w.Until == nil || w.BankSec != 0 || time.Until(*w.Until) < 4*time.Minute+50*time.Second {
		t.Fatalf("任务清零后窗口应从攒着的 5 分钟接着走: %+v %v", w, err)
	}
}
