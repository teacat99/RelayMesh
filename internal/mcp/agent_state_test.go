package mcp

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/teacat99/RelayMesh/v2/internal/model"
	"github.com/teacat99/RelayMesh/v2/internal/store"
)

// Agent 身份与状态核心的 MCP 接线（#113）。状态机本身的行为由 agentstate / store 的测试覆盖，这里只验证接线：
// 哪些调用会进状态核心、旧链路是不是原样工作、汇报的字段被怎么清洗、Web 收到什么推送。

func agentStateCred(id uint, host string) *CredentialContext {
	return &CredentialContext{CredentialID: id, CredentialName: "agent", HostName: host, Permissions: model.Permissions{Feedback: true}, Source: "db_credential"}
}

type agentStateHarness struct {
	t      *testing.T
	srv    *Server
	cred   *CredentialContext
	wf     string
	dir    string
	mu     sync.Mutex
	pushed []map[string]any
}

func newAgentStateHarness(t *testing.T, wf string) *agentStateHarness {
	t.Helper()
	h := &agentStateHarness{t: t, srv: setupTestMCPServer(t), cred: agentStateCred(7, "dev-box"), wf: wf, dir: "/home/u/projects/Alpha/"}
	h.srv.onUpdate = func(eventType string, data any) {
		if eventType != agentStateEvent {
			return
		}
		if m, ok := data.(map[string]any); ok && m["workflow_id"] == wf {
			h.mu.Lock()
			h.pushed = append(h.pushed, m)
			h.mu.Unlock()
		}
	}
	return h
}

// report 模拟 hook 随 inbox_peek 汇报一件事。
func (h *agentStateHarness) report(extra map[string]any) {
	h.t.Helper()
	args := map[string]any{
		"action": "inbox_peek", "workflow_id": h.wf, "source": "hook", "conversation_id": "conv-1",
		"project_directory": h.dir, "client_kind": "cli", "cursor_version": "3.1.0",
	}
	for k, v := range extra {
		if v == nil {
			delete(args, k)
			continue
		}
		args[k] = v
	}
	inboxCall(h.t, h.srv, h.cred, "workflow_context", args)
}

func (h *agentStateHarness) pushCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.pushed)
}

func (h *agentStateHarness) sessions() []store.AgentSessionView {
	h.t.Helper()
	list, err := h.srv.store.ListAgentSessions(context.Background(), store.AgentSessionFilter{WorkflowID: h.wf}, time.Now())
	if err != nil {
		h.t.Fatalf("读会话失败: %v", err)
	}
	return list
}

func TestAgentState_HookReportsDriveStateAndBroadcast(t *testing.T) {
	h := newAgentStateHarness(t, "wf-as-state")

	h.report(map[string]any{"agent_event": "session_start"})
	list := h.sessions()
	if len(list) != 1 {
		t.Fatalf("session_start 应建出一个会话: %+v", list)
	}
	s := list[0]
	if s.State != "idle" || s.ClientKind != "cli" || s.CursorVersion != "3.1.0" || s.HostName != "dev-box" ||
		s.ProjectDir != "/home/u/projects/Alpha" || s.IdentityLabel != "Alpha" || s.ConversationID != "conv-1" {
		t.Fatalf("新会话的内容不对: %+v", s)
	}
	if h.pushCount() != 1 {
		t.Fatalf("新会话应推送一次 agent_state: %d", h.pushCount())
	}

	h.report(map[string]any{"agent_event": "prompt"})
	if s = h.sessions()[0]; s.State != "working" || s.Sub != "thinking" {
		t.Fatalf("prompt 之后应在思考: %+v", s)
	}
	h.report(map[string]any{"agent_event": "tool_start", "agent_tool": "Shell"})
	if s = h.sessions()[0]; s.Sub != "tool:Shell" {
		t.Fatalf("工具开始应显示工具名: %+v", s)
	}
	h.report(map[string]any{"agent_event": "tool_end", "agent_tool": "Shell", "agent_duration_ms": 1500})
	if s = h.sessions()[0]; s.State != "working" || s.Sub != "thinking" || s.LastTool != "Shell" {
		t.Fatalf("工具结束应回到思考并记下最近工具: %+v", s)
	}
	before := h.pushCount()
	if before != 4 {
		t.Fatalf("每次状态变化各推送一次，应共 4 次: %d", before)
	}
	// 同一件事马上又来一遍（hook 重试 / 重复触发）：被去重，状态没变，不再推送。
	h.report(map[string]any{"agent_event": "tool_end", "agent_tool": "Shell", "agent_duration_ms": 1500})
	if h.pushCount() != before {
		t.Fatalf("重复事件不应再推送: %d", h.pushCount())
	}
	h.report(map[string]any{"agent_event": "stop", "agent_status": "completed"})
	if s = h.sessions()[0]; s.State != "idle" || s.TurnEndedAt == nil {
		t.Fatalf("正常收尾应回到空闲: %+v", s)
	}
	if h.pushCount() != before+1 {
		t.Fatalf("收尾应再推送一次: %d", h.pushCount())
	}
	last := h.pushed[len(h.pushed)-1]
	if last["state"] != "idle" || last["identity_id"] == uint(0) || last["session_id"] == uint(0) {
		t.Fatalf("推送内容不对: %+v", last)
	}

	// 老版本 hook 只会报 tool / stop，同样进状态核心（工具调用 = 一次工具结束）。
	h2 := newAgentStateHarness(t, "wf-as-old")
	h2.report(map[string]any{"agent_event": "tool", "client_kind": nil, "cursor_version": nil})
	list = h2.sessions()
	if len(list) != 1 || list[0].State != "working" {
		t.Fatalf("老版本 hook 的 tool 事件也应让会话进入工作中: %+v", list)
	}
}

func TestAgentState_LegacyLinkKeepsWorkingForNewEvents(t *testing.T) {
	h := newAgentStateHarness(t, "wf-as-legacy")
	ctx := context.Background()

	h.report(map[string]any{"agent_event": "session_start"})
	l := h.srv.store.AgentLinkOf(ctx, h.wf)
	if l.ConversationID != "conv-1" || !l.LastToolAt.IsZero() {
		t.Fatalf("session_start 只应留下对话编号: %+v", l)
	}
	h.report(map[string]any{"agent_event": "prompt"})
	if l = h.srv.store.AgentLinkOf(ctx, h.wf); l.LastToolAt.IsZero() {
		t.Fatalf("新的一轮开始说明智能体在干活，旧链路应记下: %+v", l)
	}
	l0 := l.LastToolAt
	time.Sleep(5 * time.Millisecond)
	h.report(map[string]any{"agent_event": "thought"})
	if l = h.srv.store.AgentLinkOf(ctx, h.wf); !l.LastToolAt.After(l0) {
		t.Fatalf("思考也应刷新旧链路的最近活动: %+v", l)
	}
	h.report(map[string]any{"agent_event": "stop", "agent_status": "aborted"})
	if l = h.srv.store.AgentLinkOf(ctx, h.wf); l.StopAt.IsZero() || l.StopStatus != store.StopAborted {
		t.Fatalf("stop 照旧记下收尾方式: %+v", l)
	}
	stopAt := l.StopAt
	h.report(map[string]any{"agent_event": "session_end", "agent_status": "closed"})
	if l = h.srv.store.AgentLinkOf(ctx, h.wf); !l.StopAt.Equal(stopAt) || l.ConversationID != "conv-1" {
		t.Fatalf("session_end 不应改动旧链路的收尾记录: %+v", l)
	}
	if got := h.sessions()[0]; got.State != "ended" {
		t.Fatalf("session_end 应结束会话: %+v", got)
	}
}

// 只有 source=hook 的汇报才进状态核心：智能体自己调用、安装检查带上这些字段都不算，免得被用来伪造状态。
func TestAgentState_OnlyHookSourceFeedsTheStateMachine(t *testing.T) {
	h := newAgentStateHarness(t, "wf-as-source")
	for _, src := range []any{nil, "check", "whatever"} {
		h.report(map[string]any{"agent_event": "prompt", "source": src})
	}
	if list := h.sessions(); len(list) != 0 {
		t.Fatalf("非 hook 来源的汇报不应建出会话: %+v", list)
	}
	if h.pushCount() != 0 {
		t.Fatalf("不应推送: %d", h.pushCount())
	}
}

// hook 的轮询不带运行情况（老版本 / 纯提醒轮询）：只让身份记下 hook 还活着，不建会话、不推送。
func TestAgentState_HookPollWithoutEventTouchesIdentityOnly(t *testing.T) {
	h := newAgentStateHarness(t, "wf-as-poll")
	h.report(map[string]any{"reminder": true})
	if list := h.sessions(); len(list) != 0 {
		t.Fatalf("没带运行情况不应建会话: %+v", list)
	}
	ids, err := h.srv.store.ListAgentIdentities(context.Background())
	if err != nil || len(ids) != 1 {
		t.Fatalf("应记下一个身份: %+v, %v", ids, err)
	}
	id := ids[0]
	if id.LastHookAt == nil || id.LastMCPAt != nil || id.ClientKind != "cli" || id.CursorVersion != "3.1.0" {
		t.Fatalf("只应记 hook 时间与客户端信息: %+v", id)
	}
	if h.pushCount() != 0 {
		t.Fatalf("不应推送: %d", h.pushCount())
	}
	// 汇报了不认识的事件名：当作一次普通 hook 轮询，不报错。
	h.report(map[string]any{"agent_event": "chat_end"})
	h.report(map[string]any{"agent_event": "approval_wait"})
	if list := h.sessions(); len(list) != 0 {
		t.Fatalf("hook 不能汇报 Hub 才有的事件: %+v", list)
	}
}

// 自报字段一律清洗：乱写的工具名 / 版本 / 客户端类型 / 时间戳 / 耗时都不会让调用失败，也不会原样进库。
func TestAgentState_HostileFieldsAreSanitised(t *testing.T) {
	h := newAgentStateHarness(t, "wf-as-hostile")
	h.report(map[string]any{"agent_event": "session_start"})
	h.report(map[string]any{
		"agent_event": "tool_start", "agent_tool": "rm -rf / && mail bob@example.com " + strings.Repeat("x", 200),
		"agent_ts": "not-a-number", "agent_duration_ms": -5,
		"client_kind": "weird-client", "cursor_version": "1.0 <script>alert(1)</script>",
	})
	list := h.sessions()
	if len(list) != 1 {
		t.Fatalf("应只有一个会话: %+v", list)
	}
	s := list[0]
	if strings.ContainsAny(s.LastTool, " @/&<>") || len(s.LastTool) > 64 || !strings.HasPrefix(s.LastTool, "rm_-rf") {
		t.Fatalf("工具名应被清洗: %q", s.LastTool)
	}
	if s.ClientKind != "cli" {
		t.Fatalf("已知的客户端类型不应被乱写的值覆盖: %q", s.ClientKind)
	}
	if s.CursorVersion != "3.1.0" {
		t.Fatalf("不合格的版本号应被丢弃并保留已有的: %q", s.CursorVersion)
	}
	// 时间戳：很远的未来、很早的过去都当没有（按收到时间算）。
	h.report(map[string]any{"agent_event": "thought", "agent_ts": float64(time.Now().Add(72 * time.Hour).UnixMilli())})
	h.report(map[string]any{"agent_event": "stop", "agent_status": "completed", "agent_ts": 1})
	if s = h.sessions()[0]; s.State != "idle" {
		t.Fatalf("乱写的时间戳不应打乱状态: %+v", s)
	}
}

// 没有项目目录又没有绑定可查：认不出身份，什么都不记，但调用本身照常成功。
func TestAgentState_UnknownProjectDirectoryIsSkipped(t *testing.T) {
	h := newAgentStateHarness(t, "wf-as-nodir")
	h.report(map[string]any{"agent_event": "prompt", "project_directory": nil})
	if list := h.sessions(); len(list) != 0 {
		t.Fatalf("认不出项目时不应建会话: %+v", list)
	}
	if ids, _ := h.srv.store.ListAgentIdentities(context.Background()); len(ids) != 0 {
		t.Fatalf("认不出项目时不应建身份: %+v", ids)
	}
	// 之后哪次汇报带了目录，就开始记。
	h.report(map[string]any{"agent_event": "prompt"})
	if list := h.sessions(); len(list) != 1 {
		t.Fatalf("带了目录之后应开始记: %+v", list)
	}
}

// 工作流上已经有会话记着项目目录（异步对话里 Agent 报过）：之后不带目录的汇报也认得出身份。
func TestAgentState_UsesWorkflowBindingWhenDirectoryMissing(t *testing.T) {
	h := newAgentStateHarness(t, "wf-as-bound")
	ctx := context.Background()
	if _, err := h.srv.store.CreateFeedbackSession(ctx, store.CreateSessionInput{
		WorkflowID: h.wf, CredentialHostName: "dev-box", ProjectDirectory: "/srv/Beta", Summary: "绑定用的会话",
	}); err != nil {
		t.Fatalf("预置会话失败: %v", err)
	}
	h.report(map[string]any{"agent_event": "prompt", "project_directory": nil})
	list := h.sessions()
	if len(list) != 1 || list[0].ProjectDir != "/srv/Beta" || list[0].HostName != "dev-box" {
		t.Fatalf("应按工作流上的绑定认出身份: %+v", list)
	}
}

// 任何带 workflow_id 的 MCP 调用都更新身份的「最近交互」；读类调用另记「最近查询」。
func TestAgentState_MCPCallsTouchIdentity(t *testing.T) {
	srv := setupTestMCPServer(t)
	cred := agentStateCred(9, "dev-box")
	ctx := context.Background()

	inboxCall(t, srv, cred, "workflow_context", map[string]any{"action": "checkpoint_list", "workflow_id": "wf-touch-read", "project_directory": "/p/Read"})
	inboxCall(t, srv, cred, "workflow_context", map[string]any{"action": "set_phase", "workflow_id": "wf-touch-write", "phase_id": "dev", "project_directory": "/p/Write"})
	// 没带项目目录、工作流上也没有绑定：不记。
	inboxCall(t, srv, cred, "workflow_context", map[string]any{"action": "checkpoint_list", "workflow_id": "wf-touch-none"})

	ids, err := srv.store.ListAgentIdentities(ctx)
	if err != nil {
		t.Fatal(err)
	}
	byDir := map[string]store.AgentIdentityView{}
	for _, v := range ids {
		byDir[v.ProjectDirectory] = v
	}
	if len(ids) != 2 {
		t.Fatalf("应只有读 / 写两个身份: %+v", ids)
	}
	if v := byDir["/p/Read"]; v.LastMCPAt == nil || v.LastQueryAt == nil || v.LastHookAt != nil || v.HostName != "dev-box" || v.CredentialID != 9 {
		t.Fatalf("读类调用应同时记最近交互与最近查询: %+v", v)
	}
	if v := byDir["/p/Write"]; v.LastMCPAt == nil || v.LastQueryAt != nil {
		t.Fatalf("写类调用只记最近交互: %+v", v)
	}
}

func TestIsQueryCall(t *testing.T) {
	cases := []struct {
		tool, action string
		want         bool
	}{
		{"list_sessions", "", true},
		{"get_session_history", "", true},
		{"get_session_image", "", true},
		{"usage_guide", "", true},
		{"usage_guide", "search", true},
		{"terminal_targets", "", true},
		{"workflow_context", "inbox", true},
		{"workflow_context", "checkpoint_get", true},
		{"workflow_context", "checkpoint_list", true},
		{"workflow_context", "task_get", true},
		{"workflow_context", "session_doc_get", true},
		{"workflow_context", "inbox_ack", false},
		{"workflow_context", "inbox_peek", false},
		{"workflow_context", "set_phase", false},
		{"workflow_context", "checkpoint_save", false},
		{"workflow_context", "", false},
		{"task_board", "overview", true},
		{"task_board", "batch", false},
		{"project_docs", "export", true},
		{"project_docs", "get", true},
		{"project_docs", "save", false},
		{"project_docs", "search", true},
		{"interactive_feedback", "", false},
		{"report_progress", "", false},
		{"terminal_exec", "", false},
	}
	for _, c := range cases {
		if got := isQueryCall(c.tool, c.action); got != c.want {
			t.Errorf("isQueryCall(%q, %q) = %v, 想要 %v", c.tool, c.action, got, c.want)
		}
	}
}

func TestAgentReportLegacyEvent(t *testing.T) {
	cases := []struct {
		event, status string
		wantKind      string
	}{
		{"tool", "", store.AgentEventTool},
		{"stop", "aborted", store.AgentEventStop},
		{"tool_start", "", store.AgentEventTool},
		{"tool_end", "", store.AgentEventTool},
		{"tool_fail", "", store.AgentEventTool},
		{"thought", "", store.AgentEventTool},
		{"response", "", store.AgentEventTool},
		{"prompt", "", store.AgentEventTool},
		{"compact", "", store.AgentEventTool},
		{" PROMPT ", "", store.AgentEventTool},
		{"session_start", "", ""},
		{"session_end", "closed", ""},
		{"chat_end", "", ""},
		{"", "", ""},
		{"anything", "", ""},
	}
	for _, c := range cases {
		ev := agentReport{Event: c.event, Status: c.status, ConversationID: "conv-9"}.legacyEvent()
		if ev.Kind != c.wantKind || ev.ConversationID != "conv-9" {
			t.Errorf("legacyEvent(%q) = %+v, 想要 kind=%q", c.event, ev, c.wantKind)
		}
	}
}
