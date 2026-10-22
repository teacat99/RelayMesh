package mcp

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/teacat99/RelayMesh/v2/internal/store"
)

// 对话认领（#182）在 MCP 一侧的接线：Hub 发起的运行认领了某个对话之后，这个对话里 hook 的汇报与轮询都归认领它的工作流，
// 而不是 hook 配置（项目目录里的 .cursor/relaymesh.json）里写的那个——同一个仓库的多个工作树共用一份配置时，那常常是别的工作流。

const (
	claimRunWF = "wf-run"       // 发起运行、认领对话的工作流
	claimCfgWF = "wf-cfg"       // hook 配置里写的工作流
	claimConv  = "conv-claimed" // 被认领的对话
)

func claimConversation(t *testing.T, srv *Server, wf, conv string) {
	t.Helper()
	now := time.Now()
	if _, err := srv.store.ClaimConversation(context.Background(), wf, conv, now.Add(time.Minute), now); err != nil {
		t.Fatal(err)
	}
}

func hookPeek(t *testing.T, srv *Server, cred *CredentialContext, extra map[string]any) map[string]any {
	t.Helper()
	args := map[string]any{"action": "inbox_peek", "workflow_id": claimCfgWF, "source": "hook", "conversation_id": claimConv}
	for k, v := range extra {
		if v == nil {
			delete(args, k)
			continue
		}
		args[k] = v
	}
	return firstJSON(t, inboxCall(t, srv, cred, "workflow_context", args))
}

func TestAgentClaim_HookPeekFollowsTheClaimedWorkflow(t *testing.T) {
	srv := setupTestMCPServer(t)
	cred := agentStateCred(7, "dev-box")
	queueInbox(t, srv, claimRunWF, "给发起运行的工作流的追加")
	queueInbox(t, srv, claimCfgWF, "别人的追加一")
	queueInbox(t, srv, claimCfgWF, "别人的追加二")

	// 没有认领：照旧按 hook 带的工作流查。
	if p := hookPeek(t, srv, cred, nil); p["workflow_id"] != claimCfgWF || p["unread"].(float64) != 2 {
		t.Fatalf("未认领时应查 hook 配置里的工作流: %+v", p)
	}

	claimConversation(t, srv, claimRunWF, claimConv)

	// 认领之后：这个对话的 hook 轮询查认领者的收件箱——提醒与「读哪个工作流的追加」都对上运行所属的工作流。
	p := hookPeek(t, srv, cred, nil)
	if p["workflow_id"] != claimRunWF || p["unread"].(float64) != 1 {
		t.Fatalf("认领后应查认领它的工作流: %+v", p)
	}

	// 只对 hook 的轮询生效；智能体自己调用、别的对话、没带对话编号的轮询都不受影响。
	for name, extra := range map[string]map[string]any{
		"智能体自己调用":  {"source": nil},
		"安装检查":     {"source": "check"},
		"别的对话":     {"conversation_id": "conv-other"},
		"没带对话编号":   {"conversation_id": nil},
		"对话编号格式不对": {"conversation_id": "bad conv/.."},
	} {
		if got := hookPeek(t, srv, cred, extra); got["workflow_id"] != claimCfgWF || got["unread"].(float64) != 2 {
			t.Fatalf("%s：不该被认领影响: %+v", name, got)
		}
	}
}

func TestAgentClaim_HookReportsAreRecordedUnderTheClaimedWorkflow(t *testing.T) {
	srv := setupTestMCPServer(t)
	cred := agentStateCred(7, "dev-box")
	ctx := context.Background()
	var (
		mu     sync.Mutex
		pushed []string
	)
	srv.onUpdate = func(eventType string, data any) {
		if eventType != agentStateEvent {
			return
		}
		if m, ok := data.(map[string]any); ok {
			mu.Lock()
			pushed = append(pushed, m["workflow_id"].(string))
			mu.Unlock()
		}
	}
	claimConversation(t, srv, claimRunWF, claimConv)

	report := func(event string) {
		inboxCall(t, srv, cred, "workflow_context", map[string]any{
			"action": "inbox_peek", "workflow_id": claimCfgWF, "source": "hook", "conversation_id": claimConv,
			"project_directory": "/home/u/projects/Alpha/", "client_kind": "cli", "agent_event": event,
		})
	}
	report("session_start")
	report("prompt")

	list := func(wf string) []store.AgentSessionView {
		out, err := srv.store.ListAgentSessions(ctx, store.AgentSessionFilter{WorkflowID: wf}, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	if got := list(claimRunWF); len(got) != 1 || got[0].ConversationID != claimConv || got[0].State != "working" {
		t.Fatalf("状态会话应建在认领者名下: %+v", got)
	}
	if got := list(claimCfgWF); len(got) != 0 {
		t.Fatalf("hook 配置里的工作流名下不该有这个会话: %+v", got)
	}
	// 旧链路：智能体活动时间与对话编号归认领者。
	if l := srv.store.AgentLinkOf(ctx, claimRunWF); l.ConversationID != claimConv || l.LastToolAt.IsZero() {
		t.Fatalf("认领者的旧链路应记着这次活动: %+v", l)
	}
	if l := srv.store.AgentLinkOf(ctx, claimCfgWF); l.ConversationID != "" || !l.LastToolAt.IsZero() {
		t.Fatalf("hook 配置里的工作流的旧链路不该被它污染: %+v", l)
	}
	// hook 在线的标记也归认领者；Web 的 agent_state 推送带的是认领者的工作流。
	if _, ok := srv.store.HookLastSeen(claimRunWF); !ok {
		t.Fatal("认领者应记下 hook 最近一次轮询")
	}
	if _, ok := srv.store.HookLastSeen(claimCfgWF); ok {
		t.Fatal("hook 配置里的工作流不该被这个对话的轮询刷新")
	}
	mu.Lock()
	defer mu.Unlock()
	if len(pushed) == 0 {
		t.Fatal("状态变化应推送给 Web")
	}
	for _, wf := range pushed {
		if wf != claimRunWF {
			t.Fatalf("agent_state 推送应带认领者的工作流，得到 %q（全部 %v）", wf, pushed)
		}
	}
}
