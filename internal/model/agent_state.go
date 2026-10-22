package model

import "time"

// ---- Agent 身份与状态（#113，设计稿 04）----
//
// 三张表都是只增表：新版本只会新建表，不改老表，旧二进制可以直接打开新库。
// 事件表不存提示词、思考、工具参数正文，也不存任何邮箱（见 agentstate/wire.go 的清洗）。

// AgentIdentity 是「谁」：Token + 主机 + 项目路径。同一个 Token 可以被多个项目共用，所以光有凭据区分不了项目。
// 身份来自调用方自报（MCP 调用里断言的 project_directory、hook 上报的工作区根目录），不是安全边界；
// 真正的边界仍然是 Token（谁能连上 Hub）。
type AgentIdentity struct {
	ID               uint       `gorm:"primaryKey" json:"id"`
	CredentialID     uint       `gorm:"uniqueIndex:ux_agent_identity,priority:1" json:"credential_id"`
	HostName         string     `gorm:"size:128;uniqueIndex:ux_agent_identity,priority:2" json:"host_name"`
	ProjectDirectory string     `gorm:"size:512;uniqueIndex:ux_agent_identity,priority:3" json:"project_directory"`
	Name             string     `gorm:"size:80" json:"name"`                  // 用户改的名字；空 = 用目录名
	ClientKind       string     `gorm:"size:16" json:"client_kind,omitempty"` // ide | cli | unknown
	CursorVersion    string     `gorm:"size:32" json:"cursor_version,omitempty"`
	FirstSeenAt      time.Time  `json:"first_seen_at"`
	LastSeenAt       time.Time  `gorm:"index" json:"last_seen_at"`
	LastHookAt       *time.Time `json:"last_hook_at,omitempty"`  // 最近一次收到 hook 事件
	LastMCPAt        *time.Time `json:"last_mcp_at,omitempty"`   // 最近一次 MCP 调用
	LastQueryAt      *time.Time `json:"last_query_at,omitempty"` // 最近一次「读类」MCP 调用（收件箱、会话列表、文档读取……）
	CreatedAt        time.Time  `json:"created_at"`
	UpdatedAt        time.Time  `json:"updated_at"`
}

// AgentSession 是一个 Cursor 对话在某个身份下的运行状态；状态机在这一层跑。
type AgentSession struct {
	ID             uint   `gorm:"primaryKey" json:"id"`
	IdentityID     uint   `gorm:"uniqueIndex:ux_agent_session,priority:1" json:"identity_id"`
	ConversationID string `gorm:"size:64;uniqueIndex:ux_agent_session,priority:2" json:"conversation_id"`
	WorkflowID     string `gorm:"size:128;index" json:"workflow_id"`
	ClientKind     string `gorm:"size:16" json:"client_kind,omitempty"`
	CursorVersion  string `gorm:"size:32" json:"cursor_version,omitempty"`

	StartedAt time.Time `json:"started_at"`
	EndedAt   time.Time `json:"ended_at"`
	EndReason string    `gorm:"size:16" json:"end_reason,omitempty"`

	State         string    `gorm:"size:24" json:"state"`
	Sub           string    `gorm:"size:96" json:"sub,omitempty"`          // thinking | responding | tool:<名>
	StateSource   string    `gorm:"size:12" json:"state_source,omitempty"` // hook | tui | run：现在这个状态是谁说的
	StateSince    time.Time `json:"state_since"`
	LastEventAt   time.Time `gorm:"index" json:"last_event_at"`
	LastTool      string    `gorm:"size:64" json:"last_tool,omitempty"`
	ToolStartedAt time.Time `json:"tool_started_at"`
	TurnStartedAt time.Time `json:"turn_started_at"`
	TurnEndedAt   time.Time `json:"turn_ended_at"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// AgentEventRecord 是状态机事件的流水（环形：保留 7 天或 2 万行）。表名 agent_events。
type AgentEventRecord struct {
	ID         uint      `gorm:"primaryKey" json:"id"`
	IdentityID uint      `gorm:"index" json:"identity_id"`
	SessionID  uint      `gorm:"index" json:"session_id"` // 0 = 不属于某个会话（比如改名审计）
	At         time.Time `gorm:"index" json:"at"`
	Source     string    `gorm:"size:12" json:"source"` // hook | mcp | tui | guard | web
	Event      string    `gorm:"size:24" json:"event"`
	Tool       string    `gorm:"size:64" json:"tool,omitempty"`
	DurationMS int       `json:"duration_ms,omitempty"`
	Extra      string    `gorm:"type:text" json:"extra,omitempty"` // 很小的 JSON：note、stop 状态等，不含任何正文
}

// TableName 固定表名，免得被复数化成 agent_event_records。
func (AgentEventRecord) TableName() string { return "agent_events" }
