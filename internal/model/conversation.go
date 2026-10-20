package model

import "time"

// 会话（设计稿 03）：1:1 会话「隐式存在」（id = wf:<workflow_id>，不建行），消息由读接口从四个旧来源合成；
// 这里只放合成做不到的东西——已读游标与系统事件。两张表都是只增表，旧二进制可以打开新库。

// 已读游标的读者种类。
const (
	ConversationReaderHuman    = "human"    // 你（Web）
	ConversationReaderWorkflow = "workflow" // 某个工作流里的 Agent
)

// ConversationRead 是已读游标：每个会话、每个读者一行。
//
// 1:1 会话的消息没有序号，按「发送时间（Unix 毫秒）」推进，读到的位置存在 ReadMs；
// 群会话（阶段 2b）的消息有会话内序号，存在 ReadSeq。两者互不干扰，各自只增不减。
type ConversationRead struct {
	ID             uint      `gorm:"primaryKey" json:"id"`
	ConversationID string    `gorm:"size:160;not null;uniqueIndex:idx_conv_reader,priority:1" json:"conversation_id"`
	ReaderKind     string    `gorm:"size:16;not null;uniqueIndex:idx_conv_reader,priority:2" json:"reader_kind"`
	ReaderRef      string    `gorm:"size:128;not null;uniqueIndex:idx_conv_reader,priority:3" json:"reader_ref"`
	ReadMs         int64     `gorm:"not null;default:0" json:"read_ms"`
	ReadSeq        int64     `gorm:"not null;default:0" json:"read_seq"`
	UpdatedAt      time.Time `json:"updated_at"`
}

// 会话事件的种类。
const (
	ConversationEventChatEnd  = "chat_end"  // Agent 声明本会话结束（chat end）
	ConversationEventAsyncOn  = "async_on"  // 开启异步对话
	ConversationEventAsyncOff = "async_off" // 关闭异步对话
	ConversationEventWake     = "wake"      // 用户手动唤醒了托管终端里的智能体（只记次数与方式，不记提示词）
)

// ConversationEvent 是会话时间线上的一条系统行（居中灰字）：模式切换、Agent 收尾等。
// 文字是给人看的一句话，不含任何提示词或正文；需要结构化信息时放 Meta（JSON）。
type ConversationEvent struct {
	ID             uint      `gorm:"primaryKey" json:"id"`
	ConversationID string    `gorm:"size:160;not null;index:idx_conv_event_time,priority:1" json:"conversation_id"`
	WorkflowID     string    `gorm:"size:128;index" json:"workflow_id"`
	Kind           string    `gorm:"size:24;not null" json:"kind"`
	ActorKind      string    `gorm:"size:16" json:"actor_kind,omitempty"` // human | workflow | system
	ActorRef       string    `gorm:"size:128" json:"actor_ref,omitempty"`
	Text           string    `gorm:"size:400" json:"text"`
	Meta           string    `gorm:"type:text" json:"meta,omitempty"`
	CreatedAt      time.Time `gorm:"index:idx_conv_event_time,priority:2" json:"created_at"`
}
