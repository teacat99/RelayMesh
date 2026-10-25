package model

import "time"

// 会话级常驻提示词（#232）：按工作流存一段用户在 Web 里写的文字，智能体每次回执都能读到。
//
// 为什么需要它：回执头里原来只有全局的 context:（用户记忆）和 active_skills:（规范摘要），
// 异步契约已经到字数上限，授权档位 auth: 又是全局共享的档位——没有任何一条「按会话」的自由文本通道，
// 所以分支会话、「做完所有非暂缓任务再收尾」这类规则只能每次开工手填。
// 智能体只能读不能写：写入口只有 Web 登录态（/api/v1/workflows/:id/prompts），MCP 没有对应动作。

const (
	// ResidentPromptMax 是常驻提示词的最大字数（按字符计，不是字节）。每轮回执最多因此多出这么多字。
	ResidentPromptMax = 1200
	// PromptRevisionKeep 是每个会话每个字段保留的历史版本数，超出的按字段滚动删除。
	PromptRevisionKeep = 10
	// PromptFieldResident 是版本记录里常驻提示词的字段名（唤醒词模板等字段沿用同一张表）。
	PromptFieldResident = "resident"
	// PromptFieldWakeResume / PromptFieldWakeNew 是会话自己的唤醒词模板在版本记录里的字段名（#232 D3）。
	PromptFieldWakeResume = "wake_resume"
	PromptFieldWakeNew    = "wake_new"
)

// WorkflowPrompt 是某个工作流的提示词设置；每个工作流至多一行，没有行等于「从没配置过」。
type WorkflowPrompt struct {
	WorkflowID string `gorm:"primaryKey;size:128" json:"workflow_id"`
	// Resident 是常驻提示词全文；空串表示没有（或已清除）。
	Resident string `gorm:"type:text" json:"resident"`
	// ResidentRev 每次保存（内容有变化时）加一；从没保存过为 0。
	ResidentRev int `gorm:"default:0" json:"resident_rev"`
	// ResidentToldRev 是已经告诉过智能体的版本：回执头 / 恢复路径带上全文时追平；
	// 它落后于 ResidentRev，说明用户改过而智能体还没见到新版本，下一次任意工具回执会补一段「常驻指令已更新」（只提醒一次）。
	ResidentToldRev int `gorm:"default:0" json:"resident_told_rev"`
	// WakeResume / WakeNew 是这个会话自己的唤醒词模板（#232 D3）：Hub 在非交互续聊 / 新开对话时发给智能体的那段话。
	// 空串 = 没有，沿用全局设置里的覆盖，再没有就用内置默认。模板里的 %变量 在每次发送时现算（见 internal/sessionprompt）。
	// 它们没有「已告知」状态：模板由 Hub 在发送时读取，不是智能体在回执里读的，所以保存即生效、不涨 ResidentRev。
	WakeResume string    `gorm:"type:text" json:"wake_resume"`
	WakeNew    string    `gorm:"type:text" json:"wake_new"`
	UpdatedAt  time.Time `json:"updated_at"`
	// UpdatedBy 是最近一次保存的操作者（Web 登录用户名；回滚时同样记录）。
	UpdatedBy string `gorm:"size:64" json:"updated_by,omitempty"`
}

// WorkflowPromptRevision 是提示词的一个历史版本，用于回滚与审计。
type WorkflowPromptRevision struct {
	ID         uint      `gorm:"primaryKey" json:"id"`
	WorkflowID string    `gorm:"size:128;not null;index:idx_wpr_wf_field,priority:1" json:"workflow_id"`
	Field      string    `gorm:"size:32;not null;index:idx_wpr_wf_field,priority:2" json:"field"`
	Rev        int       `json:"rev"`
	Text       string    `gorm:"type:text" json:"text"`
	Actor      string    `gorm:"size:64" json:"actor,omitempty"`
	CreatedAt  time.Time `json:"created_at"`
}
