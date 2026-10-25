package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/teacat99/RelayMesh/v2/internal/agentstate"
	"github.com/teacat99/RelayMesh/v2/internal/chat"
	"github.com/teacat99/RelayMesh/v2/internal/model"
	"github.com/teacat99/RelayMesh/v2/internal/store"
)

// chat：智能体一侧的聊天入口（#116）。把「对话」当成一件事来做，而不是一串各管一摊的工具：
// list 看有哪些会话、read 取你还没读的消息、send 发一条（不阻塞）、members 看谁在里面、status 看现状、end 主动结束。
//
// 只在异步对话（聊天）模式生效的工作流里可用——这和 interactive_feedback 的 wait:false 是同一个开关；
// 没开的工作流调用会得到明确的 not_chat_mode 说明，同步会话的回执一个字也不变（INV-2）。

type chatArgs struct {
	Action           string   `json:"action,omitempty"`
	WorkflowID       string   `json:"workflow_id"`
	Text             string   `json:"text,omitempty"`
	Title            string   `json:"title,omitempty"`
	Phase            string   `json:"phase,omitempty"`
	ProjectDirectory string   `json:"project_directory,omitempty"`
	Limit            looseInt `json:"limit,omitempty"`
	Before           string   `json:"before,omitempty"`
	After            string   `json:"after,omitempty"`
	Query            string   `json:"q,omitempty"`
	Reason           string   `json:"reason,omitempty"`
	Summary          string   `json:"summary,omitempty"`
	Conversation     string   `json:"conversation_id,omitempty"`
	Mentions         []string `json:"mentions,omitempty"`
	ReplyTo          string   `json:"reply_to,omitempty"`
	IdemKey          string   `json:"idem_key,omitempty"`
	SinceSeq         looseInt `json:"since_seq,omitempty"`
	Peek             bool     `json:"peek,omitempty"`
	Targets          []string `json:"targets,omitempty"`
	// ArtifactRef 是 send 时附带的文档 / Artifact（path 或 id）：消息里放一枚引用，聊天界面右侧可直接预览。
	ArtifactRef string `json:"artifact_ref,omitempty"`
}

const chatReadDefault, chatReadMax = 30, 100

const notChatModeText = "not_chat_mode: 这个工作流没有开启异步对话（聊天）模式，chat 工具不可用。" +
	"请照常使用 interactive_feedback；是否开启由用户在 Web 或 hook 芯片里决定，不要替用户开启。"

func (s *Server) handleChat(ctx context.Context, args json.RawMessage) (any, error) {
	var a chatArgs
	if err := json.Unmarshal(args, &a); err != nil {
		return nil, fmt.Errorf("参数无效: %w", err)
	}
	a.WorkflowID = strings.TrimSpace(a.WorkflowID)
	if a.WorkflowID == "" {
		return nil, fmt.Errorf("workflow_id 必填（用回执头里的 workflow_id）")
	}
	// 会话编号：自己的 1:1（wf:<工作流>）或所在的群（grp:<id>）；写了别人的 1:1 就明说，别悄悄当成自己这一个。
	a.Conversation = strings.TrimSpace(a.Conversation)
	group := chat.IsGroupID(a.Conversation)
	if c := a.Conversation; c != "" && !group && c != chat.DirectID(a.WorkflowID) {
		return nil, store.NewInvalidInputError("conversation_id 无效：只能是你自己的 1:1 会话 " + chat.DirectID(a.WorkflowID) + "，或你所在的群 grp:<id>（chat list 可以看到）")
	}
	info, err := s.store.WorkflowModeInfo(ctx, a.WorkflowID, time.Now())
	if err != nil {
		return nil, err
	}
	if !info.State.Effective {
		return nil, fmt.Errorf("%s", notChatModeText)
	}
	svc := chat.NewService(s.store)
	action := strings.ToLower(strings.TrimSpace(a.Action))
	if action == "send" && strings.TrimSpace(a.ArtifactRef) != "" {
		token, err := s.artifactRefToken(ctx, a.WorkflowID, a.ArtifactRef)
		if err != nil {
			return nil, err
		}
		a.Text = strings.TrimSpace(strings.TrimSpace(a.Text) + "\n\n" + token)
	}
	if group {
		return s.handleGroupChat(ctx, svc, action, a)
	}
	switch action {
	case "", "read":
		return s.chatRead(ctx, svc, a)
	case "list":
		return s.chatList(ctx, svc, a)
	case "send":
		return s.chatSend(ctx, svc, a)
	case "members":
		return s.chatMembers(ctx, svc, a)
	case "status":
		return s.chatStatus(ctx, svc, a)
	case "end":
		return s.chatEnd(ctx, a)
	default:
		return nil, fmt.Errorf("未知 action：%s（可用 list / read / send / members / status / end）", action)
	}
}

// chatMessage 是给智能体看的精简消息：没有游标、卡片结构与布局字段。
type chatMessage struct {
	ID        string `json:"id"`
	At        string `json:"at"`
	From      string `json:"from"` // you（你自己发的）| human | system
	Kind      string `json:"kind"`
	Text      string `json:"text,omitempty"`
	Title     string `json:"title,omitempty"`
	Status    string `json:"status,omitempty"`
	Images    int    `json:"images,omitempty"`
	ReplyHash string `json:"reply_hash,omitempty"`
}

func toChatMessages(ms []chat.Message) []chatMessage {
	out := make([]chatMessage, 0, len(ms))
	for _, m := range ms {
		from := "human"
		switch m.Sender.Kind {
		case chat.SenderAgent:
			from = "you"
		case chat.SenderSystem:
			from = "system"
		}
		out = append(out, chatMessage{
			ID: m.ID, At: m.SentAt.Format(time.RFC3339), From: from, Kind: m.Kind,
			Text: m.Text, Title: m.Title, Status: m.Status, Images: len(m.Attachments), ReplyHash: m.Hash,
		})
	}
	return out
}

func clampChatLimit(n looseInt, def int) int {
	switch {
	case n <= 0:
		return def
	case int(n) > chatReadMax:
		return chatReadMax
	}
	return int(n)
}

// agentReadMs 是智能体自己的读游标（毫秒）；第一次读时为 0，表示从头开始。
func (s *Server) agentReadMs(ctx context.Context, wf string) int64 {
	row, err := s.store.GetConversationRead(ctx, chat.DirectID(wf), model.ConversationReaderWorkflow, chat.AgentReader(wf))
	if err != nil {
		return 0
	}
	return row.ReadMs
}

// chatRead 取你还没读的消息（用户发的话、对卡的答复、系统行），并推进你的读游标。
// 带 before / after / q 时是翻历史：返回双方的消息，不动读游标。
func (s *Server) chatRead(ctx context.Context, svc *chat.Service, a chatArgs) (any, error) {
	wf := a.WorkflowID
	limit := clampChatLimit(a.Limit, chatReadDefault)

	if a.Before != "" || a.After != "" || strings.TrimSpace(a.Query) != "" {
		page, err := svc.List(ctx, wf, chat.ListParams{Limit: limit, Before: a.Before, After: a.After, Query: a.Query})
		if err != nil {
			return nil, err
		}
		return map[string]any{
			"conversation_id": page.ConversationID,
			"messages":        toChatMessages(page.Items),
			"has_more_before": page.HasMoreBefore,
			"has_more_after":  page.HasMoreAfter,
			"before_cursor":   page.BeforeCursor,
			"after_cursor":    page.AfterCursor,
			"note":            "这是历史翻页，没有推进你的读游标。",
		}, nil
	}

	now := time.Now()
	// 先把收件箱里排队的追加标成已读：它们的内容马上就在下面的消息里，状态要如实。
	if res, err := s.store.InboxRead(ctx, wf, store.InboxReadOptions{}, now); err == nil && (len(res.New) > 0 || len(res.Retracted) > 0) {
		s.notifyInbox(wf)
		s.emitBranchRead(wf, res.New)
	}
	since := s.agentReadMs(ctx, wf)
	items, more, err := svc.ListSince(ctx, wf, since, limit)
	if err != nil {
		return nil, err
	}
	// 你自己发的话不用读回来：只留用户与系统的。
	var theirs []chat.Message
	for _, m := range items {
		if !m.IsFromAgent() {
			theirs = append(theirs, m)
		}
	}
	advanced := since
	if n := len(items); n > 0 {
		advanced = items[n-1].SentAt.UnixMilli()
		if _, err := s.store.AdvanceConversationRead(ctx, chat.DirectID(wf), model.ConversationReaderWorkflow, chat.AgentReader(wf), advanced, 0); err != nil {
			return nil, err
		}
	}
	out := map[string]any{
		"conversation_id": chat.DirectID(wf),
		"messages":        toChatMessages(theirs),
		"has_more":        more,
	}
	if len(theirs) == 0 {
		out["note"] = "没有新消息。"
	}
	if more {
		out["note"] = "还有更多没读的消息，再调用一次 chat(action:\"read\") 继续。"
	}
	return out, nil
}

func (s *Server) chatList(ctx context.Context, svc *chat.Service, a chatArgs) (any, error) {
	sums, err := svc.Summaries(ctx, []string{a.WorkflowID})
	if err != nil {
		return nil, err
	}
	items := make([]map[string]any, 0, len(sums))
	for _, sum := range sums {
		items = append(items, map[string]any{
			"conversation_id": sum.ID,
			"type":            "direct",
			"with":            "你的用户",
			"last_message":    sum.Preview,
			"unread_for_user": sum.Unread,
		})
	}
	// 你所在的群：只列有你的群；未读只算你入群之后的消息。
	digests, err := svc.DigestForAgent(ctx, a.WorkflowID)
	if err != nil {
		return nil, err
	}
	for _, d := range digests {
		items = append(items, map[string]any{
			"conversation_id": d.GroupID,
			"type":            "group",
			"title":           d.Title,
			"unread_for_you":  d.Unread,
			"mentioned_you":   d.Mentioned,
		})
	}
	return map[string]any{"conversations": items}, nil
}

// chatSend 发一条不阻塞的消息（进度 / 回答气泡），立刻返回；用户的回复要靠 read、hook 提醒或回执末尾的 inbox: 行取得。
func (s *Server) chatSend(ctx context.Context, svc *chat.Service, a chatArgs) (any, error) {
	text := strings.TrimSpace(a.Text)
	if text == "" {
		return nil, store.NewInvalidInputError("text 必填：要发的消息内容")
	}
	sess, err := s.createNotice(ctx, interactiveFeedbackArgs{
		WorkflowID: a.WorkflowID, ProjectDirectory: a.ProjectDirectory, Title: a.Title, Summary: text, Phase: a.Phase,
	}, "", "")
	if err != nil {
		return nil, err
	}
	s.notifyConversation(a.WorkflowID)
	return map[string]any{
		"ok":              true,
		"conversation_id": chat.DirectID(a.WorkflowID),
		"message_id":      chat.PrefixReport + ":" + sess.ID,
		"note":            "已发送（不阻塞）。用户的回复用 chat(action:\"read\") 取；需要用户拍板且不能继续时，改用 interactive_feedback。",
	}, nil
}

func (s *Server) chatMembers(ctx context.Context, svc *chat.Service, a chatArgs) (any, error) {
	page, err := svc.List(ctx, a.WorkflowID, chat.ListParams{Limit: 1})
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"conversation_id": page.ConversationID,
		"members": []map[string]any{
			{"kind": "human", "id": "user", "name": chat.HumanName},
			{"kind": "agent", "id": page.Agent.ID, "name": page.Agent.Name, "you": true},
		},
	}, nil
}

func (s *Server) chatStatus(ctx context.Context, svc *chat.Service, a chatArgs) (any, error) {
	since := s.agentReadMs(ctx, a.WorkflowID)
	items, more, err := svc.ListSince(ctx, a.WorkflowID, since, chatReadMax)
	if err != nil {
		return nil, err
	}
	unread := 0
	for _, m := range items {
		if !m.IsFromAgent() {
			unread++
		}
	}
	out := map[string]any{
		"conversation_id": chat.DirectID(a.WorkflowID),
		"mode":            "chat",
		"unread_for_you":  unread,
	}
	if more {
		out["unread_for_you_at_least"] = true
	}
	if sum, err := s.store.InboxSummary(ctx, a.WorkflowID, time.Now()); err == nil {
		out["inbox_unread"] = sum.Unread
	}
	return out, nil
}

// chatEnd 主动结束这次对话：留一条系统消息、把状态记成「已结束」，让用户知道你是做完了、卡住了还是交棒了。
func (s *Server) chatEnd(ctx context.Context, a chatArgs) (any, error) {
	reason := agentstate.NormalizeEndReason(a.Reason, "")
	switch reason {
	case agentstate.EndCompleted, agentstate.EndBlocked, agentstate.EndAborted, agentstate.EndHandoff:
	case "":
		if strings.TrimSpace(a.Reason) != "" {
			return nil, store.NewInvalidInputError("reason 只能是 completed / blocked / aborted / handoff")
		}
		reason = agentstate.EndCompleted
	default:
		return nil, store.NewInvalidInputError("reason 只能是 completed / blocked / aborted / handoff")
	}
	now := time.Now()
	meta, _ := json.Marshal(map[string]string{"reason": reason})
	if _, err := s.store.AddConversationEvent(ctx, model.ConversationEvent{
		ConversationID: chat.DirectID(a.WorkflowID), WorkflowID: a.WorkflowID,
		Kind: model.ConversationEventChatEnd, ActorKind: "agent", ActorRef: a.WorkflowID,
		Text: strings.TrimSpace(a.Summary), Meta: string(meta), CreatedAt: now,
	}); err != nil {
		return nil, err
	}

	in := store.AgentIngestInput{
		WorkflowID: a.WorkflowID, ProjectDirectory: a.ProjectDirectory,
		Event: agentstate.Event{Source: agentstate.SourceMCP, Name: agentstate.EvChatEnd, Status: reason, At: now, Received: now},
	}
	if credCtx := CredentialFromContext(ctx); credCtx != nil {
		in.CredentialID, in.HostName = credCtx.CredentialID, credCtx.HostName
	}
	res, _ := s.store.IngestAgentEvent(ctx, in)
	if res.IdentityID != 0 && (res.Change.StateChanged || res.NewSession) {
		s.notifyAgentState(a.WorkflowID, res)
	}
	s.notifyConversation(a.WorkflowID)
	return map[string]any{
		"ok":              true,
		"conversation_id": chat.DirectID(a.WorkflowID),
		"reason":          reason,
		"note":            "已记录结束。用户之后再发消息，会在你下次被唤起时通过 chat(action:\"read\") 看到。",
	}, nil
}

// notifyConversation 通知 Web「这个工作流的聊天消息有变化」（SSE 侧会防抖合并）。
func (s *Server) notifyConversation(workflowID string) {
	if s.onUpdate != nil {
		s.onUpdate("conversation_changed", map[string]string{"workflow_id": workflowID})
	}
}

// artifactRefToken 把文档（path 或 id）变成消息里的引用记号 [[artifact:<文档编号>]]：先在工作流所属项目里找，再在本工作流里找。
// 记号是普通文本，聊天界面把它渲染成可点开的卡片，右侧打开预览；找不到文档时明确报错，不发一条指向空的消息。
func (s *Server) artifactRefToken(ctx context.Context, workflowID, ref string) (string, error) {
	ref = strings.TrimSpace(ref)
	var doc *model.ProjectDoc
	if p, _ := s.store.ProjectOfWorkflow(ctx, workflowID); p != nil {
		if d, err := s.store.GetProjectDocInProject(ctx, p.ID, ref); err == nil {
			doc = d
		}
	}
	if doc == nil {
		d, err := s.store.GetProjectDocByRef(ctx, workflowID, ref)
		if err != nil {
			return "", store.NewInvalidInputError("artifact_ref 找不到这篇文档：" + ref + "（用 project_docs list 看路径，或先 save 再引用）")
		}
		doc = d
	}
	return fmt.Sprintf("[[artifact:%d]]", doc.ID), nil
}
