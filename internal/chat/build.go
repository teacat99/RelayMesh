package chat

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/teacat99/RelayMesh/v2/internal/confirm"
	"github.com/teacat99/RelayMesh/v2/internal/model"
	"github.com/teacat99/RelayMesh/v2/internal/store"
)

// HumanName 是你在聊天里的显示名。
const HumanName = "你"

// Builder 把旧来源的行合成成消息。它不碰数据库，输入输出都是内存里的值，方便表驱动测试。
type Builder struct {
	ConversationID string
	WorkflowID     string
	// Agent 是 Agent 消息的发送者（取这个工作流当前身份的显示名）。
	Agent Sender
	// ReadMs 是你的已读游标（Unix 毫秒）：Agent 的消息发送时间不晚于它就算已读。
	ReadMs int64
}

// NewBuilder 为一个工作流的 1:1 会话建合成器。agentName 为空时回落到「Agent」。
func NewBuilder(workflowID, agentName string, readMs int64) Builder {
	name := strings.TrimSpace(agentName)
	if name == "" {
		name = "Agent"
	}
	return Builder{
		ConversationID: DirectID(workflowID),
		WorkflowID:     workflowID,
		Agent:          Sender{Kind: SenderAgent, ID: DirectID(workflowID), Name: name, WorkflowID: workflowID},
		ReadMs:         readMs,
	}
}

func human() Sender  { return Sender{Kind: SenderHuman, ID: "user", Name: HumanName} }
func system() Sender { return Sender{Kind: SenderSystem, Name: "系统"} }

// finish 补上所有消息共有的字段：会话编号、游标与发送毫秒。
func (b Builder) finish(m Message, rank int, key string) Message {
	m.ConversationID = b.ConversationID
	m.key = Key{Ms: m.SentAt.UnixMilli(), Rank: rank, ID: key}
	m.Cursor = m.key.Encode()
	return m
}

// agentStatus：Agent 发的消息，你的已读游标越过它就是已读。
func (b Builder) agentStatus(at time.Time) string {
	if at.UnixMilli() <= b.ReadMs {
		return StatusRead
	}
	return StatusSent
}

// shortHash 取会话编号 sess- 之后的那一段（引用回复时写的 8 位哈希）；编号不是这个形态时返回空串。
func shortHash(sessionID string) string {
	h := strings.TrimPrefix(sessionID, "sess-")
	if h == sessionID || len(h) < 6 {
		return ""
	}
	if len(h) > 8 {
		h = h[:8]
	}
	return h
}

// ReplyTime 是你的回复的时间：优先 responded_at，旧数据依次回退到 consumed_at、updated_at。
func ReplyTime(s model.FeedbackSession) time.Time {
	switch {
	case s.RespondedAt != nil:
		return *s.RespondedAt
	case s.ConsumedAt != nil:
		return *s.ConsumedAt
	default:
		return s.UpdatedAt
	}
}

func anchorOf(s model.FeedbackSession) *Anchor {
	if strings.TrimSpace(s.GitCommit) == "" {
		return nil
	}
	return &Anchor{Commit: s.GitCommit, Subject: s.GitSubject}
}

// Report 是 Agent 的一轮汇报。Agent 正停在这一轮等你（pending）时是「阻塞确认卡」。
func (b Builder) Report(s model.FeedbackSession) Message {
	m := Message{
		ID:     PrefixReport + ":" + s.ID,
		Kind:   KindText,
		Sender: b.Agent,
		SentAt: s.CreatedAt,
		Title:  s.Title,
		Text:   s.Summary,
		Hash:   shortHash(s.ID),
		Anchor: anchorOf(s),
		Status: b.agentStatus(s.CreatedAt),
		Source: Source{Type: SourceFeedbackSession, ID: s.ID},
	}
	if s.Status == "pending" {
		m.Kind = KindBlock
		m.Status = "pending"
	}
	return b.finish(m, rankReport, s.ID)
}

// Notice 是 Agent 不阻塞地发布的进度气泡。
func (b Builder) Notice(s model.FeedbackSession) Message {
	m := Message{
		ID:     PrefixReport + ":" + s.ID,
		Kind:   KindProgress,
		Sender: b.Agent,
		SentAt: s.CreatedAt,
		Title:  s.Title,
		Text:   s.Summary,
		Hash:   shortHash(s.ID),
		Anchor: anchorOf(s),
		Status: b.agentStatus(s.CreatedAt),
		Source: Source{Type: SourceFeedbackSession, ID: s.ID},
	}
	return b.finish(m, rankNotice, s.ID)
}

// Reply 是你对某一轮汇报的回复（含「秒回」带走的追加）。user_messages 是回复里的快捷短语，放进 meta.chips。
func (b Builder) Reply(s model.FeedbackSession) Message {
	at := ReplyTime(s)
	m := Message{
		ID:          PrefixReply + ":" + s.ID,
		Kind:        KindText,
		Sender:      human(),
		SentAt:      at,
		Text:        s.ResponseText,
		Status:      StatusSent,
		Attachments: attachments(s.Images, s.ID),
		Source:      Source{Type: SourceFeedbackSession, ID: s.ID},
	}
	if s.ConsumedAt != nil {
		m.Status, m.StatusAt = StatusRead, s.ConsumedAt
	} else if s.ConsumedByAI {
		m.Status, m.StatusAt = StatusRead, &s.UpdatedAt
	}
	if len(s.UserMessages) > 0 {
		m.Meta = map[string]any{"chips": []string(s.UserMessages)}
	}
	return b.finish(m, rankReply, s.ID)
}

// ReplyAfterMerge 是 Reply 的变体：这一轮的回复里有一部分是「秒回」时并进来的追加（merged，按被取走的顺序），
// 它们在聊天里已经各是一条独立的消息，这里只留合并之外的部分（秒回之后你又补的话 / 图片 / 快捷短语）。
// 剩下的是空的就返回 false（这一轮没有单独的回复）。回复正文对不上合并的写法时（例如被改过）原样保留，宁可重复也不丢内容。
func (b Builder) ReplyAfterMerge(s model.FeedbackSession, merged []model.QueuedFeedback) (Message, bool) {
	var texts []string
	msgs, imgs := 0, 0
	for _, q := range merged {
		if t := strings.TrimSpace(q.ResponseText); t != "" {
			texts = append(texts, q.ResponseText)
		}
		msgs += len(q.UserMessages)
		imgs += len(q.Images)
	}
	joined := strings.Join(texts, "\n\n")
	rest := s
	if joined != "" {
		if strings.HasPrefix(s.ResponseText, joined) {
			rest.ResponseText = strings.TrimLeft(strings.TrimPrefix(s.ResponseText, joined), "\n")
		}
	}
	if msgs <= len(s.UserMessages) {
		rest.UserMessages = s.UserMessages[msgs:]
	}
	skip := 0
	if imgs <= len(s.Images) {
		skip = imgs
		rest.Images = s.Images[imgs:]
	}
	if strings.TrimSpace(rest.ResponseText) == "" && len(rest.UserMessages) == 0 && len(rest.Images) == 0 {
		return Message{}, false
	}
	m := b.Reply(rest)
	m.Attachments = attachmentsFrom(s.Images, s.ID, skip)
	return m, true
}

// Ending 是「这一轮等待超时 / 被取消」的系统行。
func (b Builder) Ending(s model.FeedbackSession) Message {
	text := "等待超时"
	if s.Status == "cancelled" {
		text = "已取消"
	}
	m := Message{
		ID:     PrefixEnding + ":" + s.ID,
		Kind:   KindSystem,
		Sender: system(),
		SentAt: s.UpdatedAt,
		Text:   text,
		Status: StatusSent,
		Source: Source{Type: SourceFeedbackSession, ID: s.ID},
		Meta:   map[string]any{"reason": s.Status},
	}
	return b.finish(m, rankSystem, s.ID)
}

// InboxStatus 由追加行（State 已由 store 推导）给出你发的消息的状态与进入该状态的时间。
// 已送达只有「被提醒过但还没读全文」才成立：hook 提醒过，或回执里的 inbox: 提示行带到过它面前。
func InboxStatus(q model.QueuedFeedback) (string, *time.Time) {
	switch q.State {
	case model.InboxStateAcked:
		return StatusHandled, q.AckedAt
	case model.InboxStateDelivered:
		return StatusRead, q.DeliveredAt
	case model.InboxStateRevoked:
		at := q.UpdatedAt
		return StatusRetracted, &at
	case model.InboxStateRetracted:
		if q.RetractedAt != nil {
			return StatusRetracted, q.RetractedAt
		}
		at := q.UpdatedAt
		return StatusRetracted, &at
	case model.InboxStateExpired:
		at := q.UpdatedAt
		return StatusExpired, &at
	}
	if q.ReceiptHintAt != nil {
		return StatusDelivered, q.ReceiptHintAt
	}
	if q.NotifiedCount > 0 {
		return StatusDelivered, q.LastNotifiedAt
	}
	return StatusSent, nil
}

// InboxItem 是你随时追加的话。
func (b Builder) InboxItem(q model.QueuedFeedback) Message {
	st, at := InboxStatus(q)
	if q.DeliveredVia == store.InboxViaFeedback && q.DeliveredAt != nil && st != StatusRetracted {
		// 随秒回带走：Agent 发起下一轮时一并取到了全文
		st, at = StatusRead, q.DeliveredAt
	}
	m := Message{
		ID:          fmt.Sprintf("%s:%d", PrefixInbox, q.ID),
		Kind:        KindText,
		Sender:      human(),
		SentAt:      q.CreatedAt,
		Text:        q.ResponseText,
		Status:      st,
		StatusAt:    at,
		Attachments: attachments(q.Images, store.InboxSessionID(q.ID)),
		Source:      Source{Type: SourceQueuedFeedback, ID: fmt.Sprintf("%d", q.ID)},
	}
	if len(q.UserMessages) > 0 {
		m.Meta = map[string]any{"chips": []string(q.UserMessages)}
	}
	return b.finish(m, rankInbox, numKey(q.ID))
}

// Retraction 是「你撤回了一条消息」的系统行（不带正文），时间是撤回的时刻。
func (b Builder) Retraction(q model.QueuedFeedback) Message {
	m := Message{
		ID:     fmt.Sprintf("%s:%d", PrefixRevoke, q.ID),
		Kind:   KindSystem,
		Sender: system(),
		SentAt: q.UpdatedAt,
		Text:   "你撤回了一条消息",
		Status: StatusSent,
		Source: Source{Type: SourceQueuedFeedback, ID: fmt.Sprintf("%d", q.ID)},
		Meta:   map[string]any{"retracted": fmt.Sprintf("%s:%d", PrefixInbox, q.ID)},
	}
	return b.finish(m, rankSystem, numKey(q.ID))
}

// Card 是 Agent 的提问卡 / 计划确认卡（带任务关联的提问卡就是授权卡，界面按 card.task_seq 判断）。
func (b Builder) Card(v model.ConfirmItemView) Message {
	kind := KindAsk
	if v.Kind == model.ConfirmKindPlan {
		kind = KindPlan
	}
	card := v
	m := Message{
		ID:     PrefixCard + ":" + v.Ref,
		Kind:   kind,
		Sender: b.Agent,
		SentAt: v.CreatedAt,
		Title:  v.Title,
		Text:   v.Body,
		Status: v.Status,
		Card:   &card,
		Source: Source{Type: SourceConfirmItem, ID: fmt.Sprintf("%d", v.ID)},
	}
	if v.Status != model.ConfirmStatusPending {
		at := v.UpdatedAt
		m.StatusAt = &at
	}
	return b.finish(m, rankCard, numKey(v.ID))
}

// AnswerTime 是你对卡的答复的时间：作答时间；放弃的卡没有作答时间，取最后更新时间。
func AnswerTime(it model.ConfirmItem) time.Time {
	if it.AnsweredAt != nil {
		return *it.AnsweredAt
	}
	return it.UpdatedAt
}

// Answer 是你对提问卡 / 计划卡的答复。delivery 是它对应的收件箱投递行（旧数据没有，传 nil）：
// 有就用它的四态，没有只能说「已答复」。
func (b Builder) Answer(it model.ConfirmItem, v model.ConfirmItemView, delivery *model.QueuedFeedback) Message {
	at := AnswerTime(it)
	text := fmt.Sprintf("%s「%s」已忽略", v.Ref, clipOneLine(v.Title, 60))
	if it.Status == model.ConfirmStatusAnswered && v.Answer != nil {
		text = confirm.FormatAnswerLine(&it, v.Answer.OptionIDs, v.Answer.Note, v.Answer.Custom)
	}
	m := Message{
		ID:      PrefixAnswer + ":" + v.Ref,
		Kind:    KindText,
		Sender:  human(),
		SentAt:  at,
		Text:    text,
		ReplyTo: PrefixCard + ":" + v.Ref,
		Status:  StatusAnswered,
		Source:  Source{Type: SourceConfirmItem, ID: fmt.Sprintf("%d", v.ID)},
		Meta:    map[string]any{"ref": v.Ref, "item_status": it.Status},
	}
	if delivery != nil {
		m.Status, m.StatusAt = InboxStatus(*delivery)
	}
	return b.finish(m, rankAnswer, numKey(v.ID))
}

// Decision 是一篇文档里的决策汇总卡；时间取该文档最早一项的创建时间。
func (b Builder) Decision(g DecisionGroup, first time.Time) Message {
	grp := g
	status := model.ConfirmStatusAnswered
	if g.Pending > 0 {
		status = model.ConfirmStatusPending
	}
	m := Message{
		ID:       fmt.Sprintf("%s:%d", PrefixDecision, g.DocID),
		Kind:     KindDecision,
		Sender:   b.Agent,
		SentAt:   first,
		Title:    g.Title,
		Status:   status,
		Decision: &grp,
		Source:   Source{Type: SourceConfirmItem, ID: fmt.Sprintf("doc:%d", g.DocID)},
	}
	return b.finish(m, rankDecision, numKey(g.DocID))
}

// TaskUpdate 是 Agent 写下的一条任务进展。
func (b Builder) TaskUpdate(u store.WorkflowUpdate) Message {
	m := Message{
		ID:     fmt.Sprintf("%s:%d", PrefixUpdate, u.ID),
		Kind:   KindProgress,
		Sender: b.Agent,
		SentAt: u.CreatedAt,
		Title:  fmt.Sprintf("#%d %s", u.Seq, u.Title),
		Text:   u.Text,
		Status: b.agentStatus(u.CreatedAt),
		Task:   &TaskRef{Seq: u.Seq, Title: u.Title, State: u.State, FromState: u.FromState, ToState: u.ToState},
		Source: Source{Type: SourceWorkItemUpdate, ID: fmt.Sprintf("%d", u.ID)},
	}
	return b.finish(m, rankUpdate, numKey(u.ID))
}

// Event 是会话时间线上的系统事件。
func (b Builder) Event(ev model.ConversationEvent) Message {
	m := Message{
		ID:     fmt.Sprintf("%s:%d", PrefixEvent, ev.ID),
		Kind:   KindSystem,
		Sender: system(),
		SentAt: ev.CreatedAt,
		Text:   ev.Text,
		Status: StatusSent,
		Source: Source{Type: SourceEvent, ID: fmt.Sprintf("%d", ev.ID)},
		Meta:   map[string]any{"event": ev.Kind},
	}
	if ev.ActorKind != "" {
		m.Meta["actor"] = ev.ActorKind
	}
	return b.finish(m, rankSystem, numKey(ev.ID))
}

// attachments 把消息里的图片换成带内容哈希的直链；Base64 不进消息流（体积会把一页历史撑到几十兆）。
func attachments(images model.SessionImages, ownerID string) []Attachment {
	return attachmentsFrom(images, ownerID, 0)
}

// attachmentsFrom 与 attachments 相同，但跳过前 skip 张：直链里的序号仍按原始列表算（被并走的那几张不再显示，剩下的还能取到）。
func attachmentsFrom(images model.SessionImages, ownerID string, skip int) []Attachment {
	if skip < 0 || skip > len(images) {
		skip = 0
	}
	if len(images)-skip == 0 {
		return nil
	}
	out := make([]Attachment, 0, len(images)-skip)
	for i := skip; i < len(images); i++ {
		img := images[i]
		url := img.URL
		if url == "" {
			hash := img.Hash
			if hash == "" && img.Data != "" {
				hash = imageHash(img.Data)
			}
			url = model.BuildSessionImageRelativeURL(ownerID, i, hash)
		}
		out = append(out, Attachment{Type: "image", URL: url, Name: img.Name, Width: img.Width, Height: img.Height})
	}
	return out
}

// imageHash 与 api.projectSessionImages 同口径：去掉 data URL 前缀后取 SHA-256 的前 8 字节。
func imageHash(data string) string {
	raw := strings.TrimSpace(data)
	if i := strings.Index(raw, ","); i != -1 {
		raw = raw[i+1:]
	}
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:8])
}

// clipOneLine 折成一行并按字数截断。
func clipOneLine(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n]) + "…"
}

// Preview 取一条消息的一行预览（会话列表、通知、chat list 用）：优先正文，其次标题。
func Preview(m Message, n int) string {
	text := strings.TrimSpace(m.Text)
	if text == "" {
		text = strings.TrimSpace(m.Title)
	}
	if text == "" && m.Card != nil {
		text = m.Card.Title
	}
	if text == "" && len(m.Attachments) > 0 {
		text = "[图片]"
	}
	return clipOneLine(text, n)
}
