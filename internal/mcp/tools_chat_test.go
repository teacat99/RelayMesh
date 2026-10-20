package mcp

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/teacat99/RelayMesh/v2/internal/chat"
	"github.com/teacat99/RelayMesh/v2/internal/store"
)

func chatCall(t *testing.T, srv *Server, args map[string]any) (map[string]any, string, bool) {
	t.Helper()
	res := inboxCall(t, srv, asyncCred(), "chat", args)
	text := resultText(res)
	if res.IsError {
		return nil, text, true
	}
	return firstJSON(t, res), text, false
}

// 同步工作流（默认）里 chat 不可用，且说得明白；不该有任何副作用。
func TestChat_NotAvailableInSyncWorkflow(t *testing.T) {
	srv := setupTestMCPServer(t)
	const wf = "wf-chat-sync"
	seedRound(t, srv, wf)
	for _, action := range []string{"read", "send", "status", "end", "list", "members"} {
		_, text, isErr := chatCall(t, srv, map[string]any{"workflow_id": wf, "action": action, "text": "x"})
		if !isErr || !strings.Contains(text, "not_chat_mode") {
			t.Fatalf("%s 在同步工作流里应报 not_chat_mode: %v %s", action, isErr, text)
		}
	}
	if _, text, isErr := chatCall(t, srv, map[string]any{"action": "read"}); !isErr || !strings.Contains(text, "workflow_id") {
		t.Fatalf("缺 workflow_id 应报错: %s", text)
	}
	n, _ := srv.store.CountRecentNotices(context.Background(), wf, time.Now().Add(-time.Hour))
	if n != 0 {
		t.Fatalf("被拒绝的 send 不该留下通知: %d", n)
	}
}

func TestChat_SendReadStatusEnd(t *testing.T) {
	srv := setupTestMCPServer(t)
	const wf = "wf-chat-flow"
	seedRound(t, srv, wf)
	enableAsync(t, srv, wf)
	ctx := context.Background()

	// send：发布一条不阻塞的消息。
	out, _, isErr := chatCall(t, srv, map[string]any{"workflow_id": wf, "action": "send", "text": "第一步做完了", "project_directory": asyncTestProject})
	if isErr || out["ok"] != true || !strings.HasPrefix(out["message_id"].(string), "s:sess-") {
		t.Fatalf("send: %v", out)
	}
	if out["conversation_id"] != chat.DirectID(wf) {
		t.Fatalf("会话编号不对: %v", out["conversation_id"])
	}
	if _, text, isErr := chatCall(t, srv, map[string]any{"workflow_id": wf, "action": "send", "text": "  "}); !isErr || !strings.Contains(text, "text") {
		t.Fatalf("空 text 应报错: %s", text)
	}

	// 用户的话与读取：只返回用户的，且读完游标前进、不重复。
	if _, err := srv.store.QueueWorkflowFeedback(ctx, store.QueueFeedbackInput{WorkflowID: wf, ResponseText: "继续第二步"}); err != nil {
		t.Fatal(err)
	}
	out, _, isErr = chatCall(t, srv, map[string]any{"workflow_id": wf, "action": "read"})
	if isErr {
		t.Fatalf("read 失败: %v", out)
	}
	msgs := out["messages"].([]any)
	if len(msgs) == 0 {
		t.Fatalf("应读到用户的追加: %v", out)
	}
	for _, m := range msgs {
		mm := m.(map[string]any)
		if mm["from"] == "you" {
			t.Fatalf("不该把自己发的读回来: %v", mm)
		}
	}
	if last := msgs[len(msgs)-1].(map[string]any); last["text"] != "继续第二步" {
		t.Fatalf("读到的内容不对: %v", last)
	}
	out, _, _ = chatCall(t, srv, map[string]any{"workflow_id": wf, "action": "read"})
	if len(out["messages"].([]any)) != 0 || out["note"] != "没有新消息。" {
		t.Fatalf("第二次读应没有新消息: %v", out)
	}
	// 读过之后收件箱里那条的状态是已读（不是还停在 sent）。
	page, err := chat.NewService(srv.store).List(ctx, wf, chat.ListParams{Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range page.Items {
		if m.Source.Type == "queued_feedback" && m.Status == chat.StatusSent {
			t.Fatalf("读过的追加不该还是 sent: %s", m.ID)
		}
	}

	// 历史翻页不动读游标：新来一条后，read 仍然能读到它。
	if _, err := srv.store.QueueWorkflowFeedback(ctx, store.QueueFeedbackInput{WorkflowID: wf, ResponseText: "再补一句"}); err != nil {
		t.Fatal(err)
	}
	hist, _, _ := chatCall(t, srv, map[string]any{"workflow_id": wf, "action": "read", "q": "第一步"})
	if len(hist["messages"].([]any)) != 1 {
		t.Fatalf("搜索应命中自己发的那条: %v", hist)
	}
	out, _, _ = chatCall(t, srv, map[string]any{"workflow_id": wf, "action": "status"})
	if out["unread_for_you"].(float64) != 1 {
		t.Fatalf("status 应有 1 条待读: %v", out)
	}
	out, _, _ = chatCall(t, srv, map[string]any{"workflow_id": wf, "action": "read"})
	if len(out["messages"].([]any)) != 1 {
		t.Fatalf("翻历史不该推进读游标: %v", out)
	}

	// members / list。
	out, _, _ = chatCall(t, srv, map[string]any{"workflow_id": wf, "action": "members"})
	if len(out["members"].([]any)) != 2 {
		t.Fatalf("1:1 应有两个成员: %v", out)
	}
	out, _, _ = chatCall(t, srv, map[string]any{"workflow_id": wf, "action": "list"})
	if len(out["conversations"].([]any)) != 1 {
		t.Fatalf("list: %v", out)
	}
	if _, text, isErr := chatCall(t, srv, map[string]any{"workflow_id": wf, "action": "read", "conversation_id": "wf:other"}); !isErr || !strings.Contains(text, "conversation_id 无效") {
		t.Fatalf("别人的会话编号应被拒绝: %s", text)
	}

	// end：系统行出现在消息流里，原因被校验。
	if _, text, isErr := chatCall(t, srv, map[string]any{"workflow_id": wf, "action": "end", "reason": "bogus"}); !isErr || !strings.Contains(text, "reason") {
		t.Fatalf("坏 reason 应报错: %s", text)
	}
	out, _, isErr = chatCall(t, srv, map[string]any{"workflow_id": wf, "action": "end", "reason": "handoff", "summary": "交给下一位"})
	if isErr || out["reason"] != "handoff" {
		t.Fatalf("end: %v", out)
	}
	page, _ = chat.NewService(srv.store).List(ctx, wf, chat.ListParams{Limit: 50})
	last := page.Items[len(page.Items)-1]
	if last.Kind != chat.KindSystem || !strings.Contains(last.Text, "交给下一位") {
		t.Fatalf("消息流末尾应是结束系统行: %+v", last)
	}
	if _, _, isErr := chatCall(t, srv, map[string]any{"workflow_id": wf, "action": "nope"}); !isErr {
		t.Fatal("未知 action 应报错")
	}
}

// artifact_ref：消息里带上引用记号，找不到文档时明确报错且不发消息。
func TestChat_SendArtifactRef(t *testing.T) {
	srv := setupTestMCPServer(t)
	const wf = "wf-chat-art"
	seedRound(t, srv, wf)
	enableAsync(t, srv, wf)
	ctx := context.Background()
	doc, err := srv.store.CreateProjectDoc(ctx, store.ProjectDocInput{WorkflowID: wf, Path: "artifacts/board.md", Body: "<p>x</p>", Render: "html"}, store.DocActor{Kind: "agent"})
	if err != nil {
		t.Fatal(err)
	}
	before, _ := srv.store.CountRecentNotices(ctx, wf, time.Now().Add(-time.Hour))
	if _, text, isErr := chatCall(t, srv, map[string]any{"workflow_id": wf, "action": "send", "text": "看这个", "artifact_ref": "nope.md", "project_directory": asyncTestProject}); !isErr || !strings.Contains(text, "artifact_ref") {
		t.Fatalf("找不到文档应报错: %s", text)
	}
	if n, _ := srv.store.CountRecentNotices(ctx, wf, time.Now().Add(-time.Hour)); n != before {
		t.Fatalf("报错时不应发出消息: %d -> %d", before, n)
	}
	out, _, isErr := chatCall(t, srv, map[string]any{"workflow_id": wf, "action": "send", "text": "看板做好了", "artifact_ref": "artifacts/board.md", "project_directory": asyncTestProject})
	if isErr || out["ok"] != true {
		t.Fatalf("send: %v", out)
	}
	notices, _ := srv.store.GetSessionsByWorkflow(ctx, wf, 50)
	found := false
	for _, s := range notices {
		found = found || strings.Contains(s.Summary, fmt.Sprintf("[[artifact:%d]]", doc.Doc.ID))
	}
	if !found {
		t.Fatalf("消息里应带引用记号 [[artifact:%d]]", doc.Doc.ID)
	}
}
