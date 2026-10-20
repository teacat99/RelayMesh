package model

import "time"

// 群聊（设计稿 03，阶段 2b）。1:1 会话读时合成、不建行；群有原生的会话行、成员行与消息行。
// 四张表都是只增表（新增表 / 可空列），旧二进制可以打开新库；回滚后群数据留在库里无害。

// 会话种类（目前只有群）。
const ConversationKindGroup = "group"

// 群成员的种类与角色。
const (
	MemberKindHuman    = "human"    // 你（Web）；当前只有一个人，字段按通用设计
	MemberKindWorkflow = "workflow" // 一个工作流里的 Agent（成员单位 = 工作流）

	GroupRoleOwner  = "owner"
	GroupRoleMember = "member"
)

// 成员的推送策略：决定「没点名这个成员的话」要不要把消息扔进它的收件箱。
const (
	PushMentions = "mentions" // 默认：只在被 @ 时
	PushAll      = "all"      // 你发的每条消息都扔给它
	PushNone     = "none"     // 连 @ 也不扔（只留在群里，成员自己 read 才看得到）
)

// 群消息的种类与发送者。
const (
	ChatKindText   = "text"
	ChatKindSystem = "system" // 系统行：成员加入 / 离开、冷却、回复链过长；Body 是事件码，SenderRef 是相关成员

	ChatSenderHuman    = "human"
	ChatSenderWorkflow = "workflow"
	ChatSenderSystem   = "system"

	// ChatMentionAll / ChatMentionHuman 是 mentions 里除工作流编号之外的两个特殊值。
	ChatMentionAll   = "all"
	ChatMentionHuman = "human"
)

// 系统行的事件码（ChatMessage.Body）。
const (
	GroupEventCreated      = "group_created"
	GroupEventJoined       = "member_joined"
	GroupEventLeft         = "member_left"
	GroupEventArchived     = "group_archived"
	GroupEventHopLimit     = "hop_limit"     // 某成员的消息因回复链过长没有发出
	GroupEventCooling      = "cooling"       // 群进入冷却：Agent 之间的新消息只落库、不投递
	GroupEventPolicyChange = "policy_change" // 预留
)

// InboxKindChatMention 是群消息扔进成员收件箱时的 kind：不并入 interactive_feedback 的秒回，也不算「你追加的话」。
const InboxKindChatMention = "chat_mention"

// Conversation 是一个群。1:1 会话不建行。
type Conversation struct {
	ID         string     `gorm:"primaryKey;size:64" json:"id"` // grp:<8 位十六进制>
	Kind       string     `gorm:"size:16;not null;default:'group'" json:"kind"`
	Title      string     `gorm:"size:120;not null" json:"title"`
	ProjectID  string     `gorm:"size:128" json:"project_id,omitempty"` // 可选绑定的项目（跨项目群留空）
	CreatedBy  string     `gorm:"size:64" json:"created_by"`
	Settings   string     `gorm:"type:text" json:"settings,omitempty"` // JSON，预留
	CreatedAt  time.Time  `json:"created_at"`
	UpdatedAt  time.Time  `json:"updated_at"`
	ArchivedAt *time.Time `json:"archived_at,omitempty"`
}

// ConversationMember 是群成员。成员单位是工作流；离开后保留一行（LeftAt 不为空），看不到离开之后的消息。
type ConversationMember struct {
	ID             uint       `gorm:"primaryKey" json:"id"`
	ConversationID string     `gorm:"size:64;not null;uniqueIndex:idx_conv_member,priority:1" json:"conversation_id"`
	MemberKind     string     `gorm:"size:16;not null;uniqueIndex:idx_conv_member,priority:2" json:"member_kind"`
	MemberRef      string     `gorm:"size:128;not null;uniqueIndex:idx_conv_member,priority:3" json:"member_ref"`
	Role           string     `gorm:"size:16;not null;default:'member'" json:"role"`
	AddedBy        string     `gorm:"size:64" json:"added_by"`
	JoinedAt       time.Time  `json:"joined_at"`
	LeftAt         *time.Time `json:"left_at,omitempty"`
	PushPolicy     string     `gorm:"size:16;not null;default:'mentions'" json:"push_policy"`
	MutedUntil     *time.Time `json:"muted_until,omitempty"`
}

// ChatMessage 是群消息，会话内序号 Seq 唯一递增。
type ChatMessage struct {
	ID             uint        `gorm:"primaryKey" json:"id"`
	ConversationID string      `gorm:"size:64;not null;uniqueIndex:idx_chat_conv_seq,priority:1;index:idx_chat_conv_time,priority:1" json:"conversation_id"`
	Seq            int64       `gorm:"not null;uniqueIndex:idx_chat_conv_seq,priority:2" json:"seq"`
	SenderKind     string      `gorm:"size:16;not null" json:"sender_kind"`
	SenderRef      string      `gorm:"size:128;index:idx_chat_sender" json:"sender_ref"`
	Kind           string      `gorm:"size:16;not null;default:'text'" json:"kind"`
	Body           string      `gorm:"type:text" json:"body"`
	ReplyTo        string      `gorm:"size:32" json:"reply_to,omitempty"` // m:<消息 id>
	Mentions       StringArray `gorm:"type:text" json:"mentions,omitempty"`
	Hop            int         `gorm:"not null;default:0" json:"hop"`
	// IdemKey 是「会话|发送者|幂等键」拼成的整串，唯一；Agent 重试同一条不会重复落库。没有幂等键时为空（空值互不冲突）。
	IdemKey   *string   `gorm:"size:320;uniqueIndex:idx_chat_idem" json:"-"`
	CreatedAt time.Time `gorm:"index:idx_chat_conv_time,priority:2" json:"created_at"`
}
