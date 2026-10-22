package mcp

import (
	"context"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/teacat99/RelayMesh/v2/internal/model"
)

// 等待用户回复的两种流程写进 initialize.instructions（#121 / #123）：
//   - 能发起 interactive_feedback 的令牌收到这一节，其余令牌（只读会话、终端）不收；
//   - 这一节里提到的回执标记必须真的出现在回执里：回执改了措辞，这里会失败，提醒同步改这一节，不让两边悄悄漂开。
func TestInitializeInstructionsDescribeWaitFlows(t *testing.T) {
	srv := setupTestMCPServer(t)
	ctx := context.Background()
	const heading = "13. 等待用户回复的两种流程"

	// 发放范围
	for name, perms := range map[string]model.Permissions{
		"治理":      {Feedback: true},
		"治理 + 终端": {Feedback: true, Terminal: true},
	} {
		if text := srv.InstructionsForPermissions(ctx, perms); !strings.Contains(text, heading) {
			t.Errorf("%s 令牌的指引应带「等待用户回复的两种流程」一节", name)
		}
	}
	for name, perms := range map[string]model.Permissions{
		"只读会话":    {Sessions: true},
		"系统信息":    {SystemInfo: true},
		"终端（原生）":  {Terminal: true},
		"终端（兼容）":  {Terminal: true, TerminalCompat: true},
		"终端文件":    {TerminalFiles: true},
		"终端 + 文件": {Terminal: true, TerminalFiles: true},
	} {
		if text := srv.InstructionsForPermissions(ctx, perms); strings.Contains(text, "等待用户回复的两种流程") {
			t.Errorf("%s 令牌不会发起 interactive_feedback，不该收到等待流程一节", name)
		}
	}
	// 追加在全局指引末尾：自定义过全局指引的部署也能收到，它排在任务清单与收件箱两节之后
	full := srv.InstructionsForPermissions(ctx, model.Permissions{Feedback: true})
	if !strings.HasSuffix(full, waitFlowsInstructionsSection) {
		t.Error("等待流程一节应追加在指引最末尾")
	}
	if n := utf8.RuneCountInString(waitFlowsInstructionsSection); n > 1100 {
		t.Errorf("这一节每次 initialize 都会下发，要保持精炼：%d 字，上限 1100", n)
	}

	section := waitFlowsInstructionsSection

	// 同步 1:1：等待回执与超时回执
	const syncWF = "wf-wait-flows-sync"
	waiting := srv.formatKeepaliveResult(ctx, &model.FeedbackSession{ID: "sess-wait-flows", WorkflowID: syncWF, PromptWaitMinutes: 1, MaxNoFeedbackChecks: 3})
	timedOut := srv.formatKeepaliveResult(ctx, &model.FeedbackSession{ID: "sess-wait-flows", WorkflowID: syncWF, PromptWaitMinutes: 1, NoFeedbackChecks: 3, MaxNoFeedbackChecks: 3})
	if strings.Contains(waiting, "mode:") {
		t.Fatalf("同步工作流的回执不该带 mode: 行：\n%s", waiting)
	}
	for _, mark := range []string{"=== 等待回执 ===", "AwaitShell", "continue_feedback_session", "【收尾宽限】", "+1 分钟"} {
		if !strings.Contains(waiting, mark) {
			t.Errorf("同步等待回执里没有 %q（这一节还在引用它）：\n%s", mark, waiting)
		}
		if !strings.Contains(section, mark) {
			t.Errorf("等待流程一节没有提到同步回执里的 %q", mark)
		}
	}
	if !strings.Contains(timedOut, "=== 反馈超时 ===") || !strings.Contains(section, "=== 反馈超时 ===") {
		t.Errorf("超时回执与这一节都应有 === 反馈超时 ===：\n%s", timedOut)
	}

	// 异步：发布回执带 mode: async / async: 契约 / window: 行
	const asyncWF = "wf-wait-flows-async"
	seedRound(t, srv, asyncWF)
	enableAsync(t, srv, asyncWF)
	notice := resultText(inboxCall(t, srv, asyncCred(), "interactive_feedback", map[string]any{
		"workflow_id": asyncWF, "summary": "进度", "title": "进度", "project_directory": asyncTestProject, "wait": false,
	}))
	for _, mark := range []string{"mode: async", "async:", "window: 回复窗口剩余", "AwaitShell", "wait:false", "inbox"} {
		if !strings.Contains(notice, mark) {
			t.Errorf("异步回执里没有 %q（这一节还在引用它）：\n%s", mark, notice)
		}
		short := strings.TrimSuffix(mark, " 回复窗口剩余") // 一节里只写 window: 行
		if !strings.Contains(section, short) {
			t.Errorf("等待流程一节没有提到异步回执里的 %q", short)
		}
	}
	for _, mark := range []string{"暂缓", "回复窗口"} {
		if !strings.Contains(section, mark) {
			t.Errorf("等待流程一节漏了异步规则 %q", mark)
		}
	}
}
