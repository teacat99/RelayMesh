// Package chat 是聊天消息接口的核心（设计稿 03 / 旧稿 rev 5）。
//
// 1:1 会话「隐式存在」：id = wf:<workflow_id>，消息由四个旧来源（汇报与回复、你追加的话、提问卡与答复、任务进展）
// 读时合成一条按发送时间排序的流，旧表一行不动；已读游标与系统事件落在两张新表里。
// 群会话（阶段 2b）有原生的 chat_messages，由同一个接口对外，用 grp: 前缀区分。
//
// 这个包只放「合成、排序、分页、状态推导」这些纯逻辑，取数在 internal/store（chat_stream.go）。
package chat

import (
	"strings"
	"time"

	"github.com/teacat99/RelayMesh/v2/internal/model"
)

// 消息种类。
const (
	KindText       = "text"       // 文字消息（Agent 的汇报正文、你的回复与追加）
	KindProgress   = "progress"   // 进展：Agent 的进度气泡与任务进展卡（连续出现时界面叠放）
	KindAsk        = "ask"        // 提问卡（含授权卡：task_seq > 0）
	KindPlan       = "plan"       // 计划确认卡
	KindBlock      = "block"      // 阻塞确认卡：Agent 正停在这一轮等你
	KindDecision   = "decision"   // 文档决策卡（按文档汇总）
	KindAlert      = "alert"      // 守护告警卡（预留）
	KindSystem     = "system"     // 系统行：居中的灰色小字
	KindCheckpoint = "checkpoint" // 检查点胶囊（预留）
)

// 发送者种类。
const (
	SenderHuman  = "human"
	SenderAgent  = "agent"
	SenderSystem = "system"
)

// 消息状态。你发的消息：sent → delivered → read → handled，旁支 retracted / expired；
// Agent 发的消息：sent → read（你读到了）；卡片沿用确认项的状态（pending / answered / dismissed / withdrawn / expired）。
const (
	StatusSent      = "sent"      // Hub 收下了
	StatusDelivered = "delivered" // Agent 已被提醒（hook 提醒或回执提示行带到了它面前），但还没读全文
	StatusRead      = "read"      // 读到了全文（你发的：Agent 取走；Agent 发的：你的已读游标越过了它）
	StatusHandled   = "handled"   // Agent 确认处理了
	StatusRetracted = "retracted" // 读到之后被撤回
	StatusExpired   = "expired"   // 保留期内一直没被读到
	StatusAnswered  = "answered"  // 你的答复（投递状态未知的旧数据）
)

// 消息来源（调试与对拍用，前端不依赖）。
const (
	SourceFeedbackSession = "feedback_session"
	SourceQueuedFeedback  = "queued_feedback"
	SourceConfirmItem     = "confirm_item"
	SourceWorkItemUpdate  = "work_item_update"
	SourceEvent           = "event"
)

// 消息 id 的来源前缀：旧表主键不变，id 就不变，引用与书签都稳。
const (
	PrefixReport   = "s" // s:sess-ab12cd34  Agent 的汇报（或进度气泡）
	PrefixReply    = "r" // r:sess-ab12cd34  你对这一轮的回复
	PrefixEnding   = "x" // x:sess-ab12cd34  这一轮等待超时 / 被取消（系统行）
	PrefixInbox    = "q" // q:12             你追加的话
	PrefixRevoke   = "v" // v:12             你撤回了一条追加（系统行）
	PrefixCard     = "c" // c:Q12            Agent 的提问 / 计划卡
	PrefixAnswer   = "a" // a:Q12            你对卡的答复
	PrefixDecision = "d" // d:7              一篇文档的决策汇总卡
	PrefixUpdate   = "u" // u:345            任务进展
	PrefixEvent    = "e" // e:77             系统事件
)

// 同一毫秒内的先后顺序（来源序）：汇报在先，随后是回复、追加、进度气泡……沿用前端 timeline.ts 的 KIND_RANK
// （inbox < notice < capsule < task < confirm < decisions < guard），汇报与回复排在它们之前。
const (
	rankReport   = 0
	rankReply    = 1
	rankInbox    = 2
	rankNotice   = 3
	rankCapsule  = 4
	rankUpdate   = 5
	rankCard     = 6
	rankAnswer   = 7
	rankDecision = 8
	rankGuard    = 9
	rankSystem   = 10
)

// TieOrder 按「同一毫秒内的先后」列出来源名，用于与前端 timeline.ts 的 KIND_RANK 对拍（见 testdata/tie_order.json）。
func TieOrder() []string {
	return []string{"report", "reply", "inbox", "notice", "capsule", "update", "card", "answer", "decision", "guard", "system"}
}

// Sender 是消息的发送者。
type Sender struct {
	Kind       string `json:"kind"`                  // human | agent | system
	ID         string `json:"id,omitempty"`          // human 恒为 "user"；agent 是身份编号或 wf:<工作流>
	Name       string `json:"name"`                  // 显示名：你 / Agent 身份标签 / 系统
	WorkflowID string `json:"workflow_id,omitempty"` // agent 所属的工作流
}

// Attachment 是消息里的图片：url 是带内容哈希的直链（不含令牌），Base64 一律不进消息流。
type Attachment struct {
	Type   string `json:"type"` // 目前只有 image
	URL    string `json:"url"`
	Name   string `json:"name,omitempty"`
	Width  int    `json:"width,omitempty"`
	Height int    `json:"height,omitempty"`
}

// Anchor 是 Agent 申报的交付锚点（仓库最新提交）。
type Anchor struct {
	Commit  string `json:"commit"`
	Subject string `json:"subject,omitempty"`
}

// TaskRef 是任务进展消息指向的任务。
type TaskRef struct {
	Seq       int    `json:"seq"`
	Title     string `json:"title"`
	State     string `json:"state"`
	FromState string `json:"from_state,omitempty"`
	ToState   string `json:"to_state,omitempty"`
}

// DecisionGroup 是一篇文档里的全部决策项（按文档汇总成一张卡）。
type DecisionGroup struct {
	DocID   uint                    `json:"doc_id"`
	DocPath string                  `json:"doc_path,omitempty"`
	Title   string                  `json:"title,omitempty"`
	Pending int                     `json:"pending"`
	Items   []model.ConfirmItemView `json:"items"`
}

// Source 标明一条消息来自哪张旧表的哪一行。
type Source struct {
	Type string `json:"type"`
	ID   string `json:"id"`
}

// Message 是统一消息接口里的一条消息。
type Message struct {
	ID             string    `json:"id"`
	ConversationID string    `json:"conversation_id"`
	Kind           string    `json:"kind"`
	Sender         Sender    `json:"sender"`
	SentAt         time.Time `json:"sent_at"`
	// Cursor 是不透明游标（编码 发送毫秒 + 来源序 + 主键）：翻页、已读、定位都用它。
	Cursor string `json:"cursor"`

	Title string `json:"title,omitempty"`
	Text  string `json:"text,omitempty"` // Markdown
	// Hash 是 8 位短哈希：别人（你）引用这条回复时写成「【回复 哈希】」；只有 Agent 的汇报与进度气泡有。
	Hash    string `json:"hash,omitempty"`
	ReplyTo string `json:"reply_to,omitempty"`

	Status   string     `json:"status"`
	StatusAt *time.Time `json:"status_at,omitempty"`

	Attachments []Attachment           `json:"attachments,omitempty"`
	Card        *model.ConfirmItemView `json:"card,omitempty"`
	Task        *TaskRef               `json:"task,omitempty"`
	Anchor      *Anchor                `json:"anchor,omitempty"`
	Decision    *DecisionGroup         `json:"decision,omitempty"`
	Source      Source                 `json:"source"`
	Hops        int                    `json:"hops"`
	Meta        map[string]any         `json:"meta,omitempty"`

	// 群消息专有（阶段 2b）：点名了谁、谁读过了、被点名的成员各自的投递状态。
	Mentions   []MentionRef `json:"mentions,omitempty"`
	Reads      *ReadInfo    `json:"reads,omitempty"`
	Deliveries []Delivery   `json:"deliveries,omitempty"`

	key Key // 完整排序键（与 Cursor 同源），分页裁剪用
}

// Key 返回消息的完整排序键。
func (m Message) Key() Key { return m.key }

// IsFromAgent 报告这条是不是 Agent 发的（已读游标与未读数只看它们）。
func (m Message) IsFromAgent() bool { return m.Sender.Kind == SenderAgent }

// ---- 会话 id ----

// DirectPrefix 是 1:1 会话 id 的前缀。
const DirectPrefix = "wf:"

// GroupPrefix 是群会话 id 的前缀（阶段 2b）。
const GroupPrefix = "grp:"

// DirectID 返回一个工作流的 1:1 会话 id。
func DirectID(workflowID string) string { return DirectPrefix + strings.TrimSpace(workflowID) }

// ParseDirectID 解析 1:1 会话 id；不是 wf:<非空> 时 ok 为 false。
func ParseDirectID(conversationID string) (workflowID string, ok bool) {
	rest, found := strings.CutPrefix(strings.TrimSpace(conversationID), DirectPrefix)
	rest = strings.TrimSpace(rest)
	if !found || rest == "" {
		return "", false
	}
	return rest, true
}

// ReplyHash 把 reply_to 引用（s:<session_id>）换成 8 位短哈希。
func ReplyHash(ref string) (string, bool) {
	id, ok := strings.CutPrefix(strings.TrimSpace(ref), PrefixReport+":")
	if !ok {
		return "", false
	}
	h := shortHash(id)
	return h, h != ""
}
