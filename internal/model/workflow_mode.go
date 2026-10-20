package model

import "time"

// 异步对话模式（#42）的持久化模型。
//
// 默认（同步）模式下，智能体每次汇报都要等用户回复；用户在 hook 芯片里为某个工作流开启「异步对话」之后，
// 智能体可以不阻塞地发布进度与回答（FeedbackSession.Kind = notice）、用 async_ask 带默认值提问，
// 并按用户选定的「授权档位」自行处理可回滚的事。有效模式 = 用户开启 且 最近见过 hook（见 internal/asyncmode）。

const (
	// FeedbackKindFeedback 是常规的阻塞式汇报（库里存空串，保持旧版本读写不变）；
	// FeedbackKindNotice 是异步模式下不阻塞发布的进度 / 回答气泡：没有回复框，发布即完成。
	FeedbackKindFeedback = "feedback"
	FeedbackKindNotice   = "notice"

	// 授权档位的风险上限：智能体不必先问就能处理到哪一级。
	// L0 只做读与分析，任何写入都先问；L1 可回滚的改动（工作树内修改、本地提交）直接做。
	// 不可逆的操作（L2）在任何档位下都要阻塞式确认（INV-3），所以没有 L2 档位。
	AuthLevelL0 = "L0"
	AuthLevelL1 = "L1"

	AuthProfileNameMax  = 24
	AuthProfileRulesMax = 240
	AuthProfileMax      = 12
)

// WorkflowMode 记录某个工作流是否开启了异步对话，以及选用的授权档位。每个工作流至多一行，没有行等于「从未开启」。
type WorkflowMode struct {
	WorkflowID   string `gorm:"primaryKey;size:128" json:"workflow_id"`
	AsyncEnabled bool   `gorm:"default:false" json:"async_enabled"`
	// EverEnabled：曾经开启过（关掉之后仍为真）。只有开启过的工作流，回执头里才会出现 mode: 行（INV-2：
	// 没用过异步的工作流，回执与升级之前逐字一致）。
	EverEnabled bool `gorm:"default:false" json:"ever_enabled"`
	// ProfileID 是选用的授权档位；0 或档位已被删除时按默认档位处理。
	ProfileID uint `json:"profile_id"`
	// EnabledAt 是最近一次开启的时间：时间线里的任务更新卡只显示这之后的变化，免得一开启就刷出一屏历史。
	EnabledAt *time.Time `json:"enabled_at,omitempty"`
	// ReplyWindowUntil 是异步对话的「回复窗口」截止时间（#123）：智能体每发一条新消息就累加一段，用户一回复就清零；
	// nil = 当前没有在等回复。窗口走完而没有新消息，智能体才可以收尾。
	ReplyWindowUntil *time.Time `json:"reply_window_until,omitempty"`
	// ReplyWindowBankSec 是窗口暂停时攒着的剩余秒数：只要智能体还有进行中 / 验证中的任务，回复窗口就不走，
	// 因为它没有「空下来等你」；任务清零后从这里接着走（用户 2026-10-15：倒计时仅在没有任务时触发，否则不算异步）。
	ReplyWindowBankSec int `gorm:"default:0" json:"reply_window_bank_sec,omitempty"`
	// ToldSig 是最近一次告诉智能体的「模式签名」（见 asyncmode.Signature）：模式或档位变了、而智能体还没被告知时，
	// 下一次任意工具回执会补一段说明（只提醒一次），让它在工作中途就知道升级 / 降级了。
	ToldSig   string    `gorm:"size:64" json:"-"`
	UpdatedAt time.Time `json:"updated_at"`
}

// WorkflowHookSeen 记下某个工作流的编辑器 hook 最近一次来过的时间，唯一目的是让 Hub 重启后不丢（#103）。
// 写入有节流（同一工作流最多每分钟一次，见 store.HookSeenPersistEvery），所以落库的时间最多比真实时间旧一分钟：
// 重启后恢复出来的值只会偏旧、不会偏新——对 60 分钟的新鲜度窗口可以忽略，而且方向是安全的。
type WorkflowHookSeen struct {
	WorkflowID string    `gorm:"primaryKey;size:128" json:"workflow_id"`
	SeenAt     time.Time `gorm:"not null;index" json:"seen_at"`
}

// AuthProfile 是全局的授权档位：异步模式下取代「讨论 / 方案 / 开发」阶段提示词，告诉智能体哪些事可以直接做。
// 默认有一档「标准授权」，其余由用户手动添加；工作流各自选择当前档位。
type AuthProfile struct {
	ID    uint   `gorm:"primaryKey" json:"id"`
	Name  string `gorm:"size:96;uniqueIndex;not null" json:"name"`
	Level string `gorm:"size:4;not null;default:'L1'" json:"level"`
	// Rules 是写给智能体看的规则摘要（回执里的 auth: 行整行不超过 300 字，名称与档位占用一部分）。
	Rules     string    `gorm:"size:960;not null" json:"rules"`
	IsDefault bool      `gorm:"default:false" json:"is_default"`
	Sort      int       `gorm:"default:0" json:"sort"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}
