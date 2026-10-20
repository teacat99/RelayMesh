package mcp

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/teacat99/RelayMesh/v2/internal/agentstate"
	"github.com/teacat99/RelayMesh/v2/internal/store"
)

// Agent 身份与状态核心在 MCP 一侧的接线（#113，设计稿 04 §2.5）。
//
// 两条输入：
//   - 编辑器 hook 随 inbox_peek 汇报的事件（agent_event 等字段）→ 状态机（store.IngestAgentEvent）；
//   - 任何带 workflow_id 的 MCP 调用 → 只更新身份的「最近交互 / 最近查询」时间戳（store.TouchAgentIdentity），不产生事件。
//
// 旧链路（AgentLink，会话守护依赖它）原样保留：hook 汇报的事件先折成旧的 tool / stop 两种再交给它（legacyEvent），
// 新旧两边互不依赖。所有字段都是调用方自报，清洗在 agentstate / store；出错一律静默——它们不能影响工具调用本身。

// agentReport 是 hook 随 inbox_peek 汇报的运行情况。
type agentReport struct {
	Event            string  `json:"agent_event"`
	Status           string  `json:"agent_status"`
	ConversationID   string  `json:"conversation_id"`
	Tool             string  `json:"agent_tool"`
	TS               float64 `json:"agent_ts"`
	DurationMS       float64 `json:"agent_duration_ms"`
	ClientKind       string  `json:"client_kind"`
	CursorVersion    string  `json:"cursor_version"`
	ProjectDirectory string  `json:"project_directory"`
}

// parseAgentReport 取出汇报字段。个别字段类型不对时，其余字段照样可用（json 会尽量解完）。
func parseAgentReport(raw json.RawMessage) agentReport {
	var r agentReport
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &r)
	}
	return r
}

// legacyEvent 把汇报折成旧链路认的两种事件：说明智能体还在干活的（工具开始 / 结束 / 失败、思考、回复、新一轮、压缩）都算 tool，
// stop 照旧，session_start / session_end 与不认识的只留对话编号。
func (r agentReport) legacyEvent() store.AgentEvent {
	kind := strings.ToLower(strings.TrimSpace(r.Event))
	switch kind {
	case store.AgentEventTool, store.AgentEventStop:
	case "tool_start", "tool_end", "tool_fail", "thought", "prompt", "response", "compact":
		kind = store.AgentEventTool
	default:
		kind = ""
	}
	return store.AgentEvent{Kind: kind, Status: r.Status, ConversationID: r.ConversationID}
}

// ingestAgentState 把 hook 汇报折进状态核心；状态变了（或出现了新会话）就给 Web 推一条 agent_state。
func (s *Server) ingestAgentState(ctx context.Context, credCtx *CredentialContext, wf string, r agentReport, now time.Time) {
	name, status, ok := agentstate.ParseWire(r.Event, r.Status)
	if !ok {
		// 没带（或带了不认识的）运行情况：只算一次 hook 轮询，让身份知道 hook 还活着。
		in := store.AgentTouchInput{WorkflowID: wf, ProjectDirectory: r.ProjectDirectory, Hook: true, ClientKind: r.ClientKind, CursorVersion: r.CursorVersion}
		if credCtx != nil {
			in.CredentialID, in.HostName = credCtx.CredentialID, credCtx.HostName
		}
		_ = s.store.TouchAgentIdentity(ctx, in, now)
		return
	}
	var at time.Time
	if r.TS > 0 && r.TS < 1e15 {
		at = agentstate.ParseUnixMilli(int64(r.TS), now)
	}
	var dur time.Duration
	if r.DurationMS > 0 && r.DurationMS < 24*3600*1000 {
		dur = time.Duration(r.DurationMS) * time.Millisecond
	}
	in := store.AgentIngestInput{
		WorkflowID:       wf,
		ConversationID:   r.ConversationID,
		ClientKind:       r.ClientKind,
		CursorVersion:    r.CursorVersion,
		ProjectDirectory: r.ProjectDirectory,
		Event: agentstate.Event{
			Source: agentstate.SourceHook, Name: name, At: at, Received: now,
			Tool: agentstate.SanitizeTool(r.Tool), Status: status, Duration: dur,
		},
	}
	if credCtx != nil {
		in.CredentialID, in.HostName = credCtx.CredentialID, credCtx.HostName
	}
	res, _ := s.store.IngestAgentEvent(ctx, in) // 写库失败只丢持久化，内存里的状态照常前进
	if res.IdentityID != 0 && (res.Change.StateChanged || res.NewSession) {
		s.notifyAgentState(wf, res)
	}
}

// claimedWorkflow 返回 hook 汇报（或轮询）该归属的工作流：hook 所在的对话被 Hub 发起的运行认领了（#182）就是认领者，
// 否则原样返回 wf。hook 带的工作流来自项目目录里的 .cursor/relaymesh.json，同一个仓库的多个工作树共用一份配置时，
// 它写的常常是另一个工作流；而运行是 Hub 替某个工作流发起的，对话（以及它的提醒、活动、状态）归发起者。
func (s *Server) claimedWorkflow(conv, wf string, now time.Time) string {
	if owner := s.store.ConversationOwner(conv, now); owner != "" {
		return owner
	}
	return wf
}

// agentStateEvent 是状态变化时广播给 Web 的事件名。
const agentStateEvent = "agent_state"

func (s *Server) notifyAgentState(workflowID string, res store.AgentIngestResult) {
	if s.onUpdate == nil {
		return
	}
	s.onUpdate(agentStateEvent, map[string]any{
		"workflow_id": workflowID,
		"identity_id": res.IdentityID,
		"session_id":  res.SessionID,
		"state":       string(res.Snapshot.State),
		"sub":         res.Snapshot.Sub,
		"source":      string(res.Snapshot.Source),
		"since":       res.Snapshot.Since,
	})
}

// touchAgentIdentity 记下这个身份刚刚调用过 RelayMesh（读类调用另记一个时间）。
func (s *Server) touchAgentIdentity(ctx context.Context, credCtx *CredentialContext, tool string, raw json.RawMessage, wf string, now time.Time) {
	var meta struct {
		ProjectDirectory string `json:"project_directory"`
		Action           string `json:"action"`
	}
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &meta)
	}
	in := store.AgentTouchInput{WorkflowID: wf, ProjectDirectory: meta.ProjectDirectory, Query: isQueryCall(tool, meta.Action)}
	if credCtx != nil {
		in.CredentialID, in.HostName = credCtx.CredentialID, credCtx.HostName
	}
	_ = s.store.TouchAgentIdentity(ctx, in, now)
}

// isQueryCall 判断一次调用是不是「读类」：只看信息、不改东西（收件箱、会话列表、文档读取、清单概览……）。
func isQueryCall(tool, action string) bool {
	switch tool {
	case "list_sessions", "get_session_history", "get_session_image", "get_system_info", "terminal_targets", "usage_guide":
		return true
	case "workflow_context", "task_board", "project_docs", "async_ask", "chat", "manage_skills":
		return isReadAction(action)
	}
	return false
}

func isReadAction(action string) bool {
	a := strings.ToLower(strings.TrimSpace(action))
	switch a {
	case "":
		return false
	case "inbox", "overview", "export", "history", "search", "index", "list", "get", "read":
		return true
	}
	return strings.HasPrefix(a, "get_") || strings.HasPrefix(a, "list_") || strings.HasPrefix(a, "read_") ||
		strings.HasSuffix(a, "_get") || strings.HasSuffix(a, "_list")
}
