package model

import (
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

type Segment struct {
	ID              uint      `gorm:"primaryKey" json:"-"`
	TaskID          string    `gorm:"index;not null;size:128" json:"task_id,omitempty"`
	Name            string    `gorm:"size:64;not null" json:"name"`
	Content         string    `gorm:"type:text;not null" json:"content"`
	Position        int       `gorm:"default:0" json:"position"`
	UpdatedRevision int64     `gorm:"default:1" json:"updated_revision"`
	CreatedAt       time.Time `json:"created_at,omitempty"`
	UpdatedAt       time.Time `json:"updated_at,omitempty"`
}

type PathReference struct {
	Path        string `json:"path"`
	Line        int    `json:"line,omitempty"`
	Description string `json:"description,omitempty"`
}

type PathReferences []PathReference

func (p PathReferences) Value() (driver.Value, error) {
	if p == nil {
		return "[]", nil
	}
	return json.Marshal(p)
}

func (p *PathReferences) Scan(value interface{}) error {
	if value == nil {
		*p = []PathReference{}
		return nil
	}
	bytes, ok := value.([]byte)
	if !ok {
		str, ok := value.(string)
		if !ok {
			return errors.New("type assertion to []byte or string failed")
		}
		bytes = []byte(str)
	}
	if len(bytes) == 0 {
		*p = []PathReference{}
		return nil
	}
	return json.Unmarshal(bytes, p)
}

type WaitPolicy struct {
	AfterMinutes         int    `json:"after_minutes"`
	MaxNoFeedbackChecks  int    `json:"max_no_feedback_checks"`
	WaitInstruction      string `json:"wait_instruction"`
	ExhaustedInstruction string `json:"exhausted_instruction"`
}

func (w WaitPolicy) Value() (driver.Value, error) {
	return json.Marshal(w)
}

func (w *WaitPolicy) Scan(value interface{}) error {
	if value == nil {
		return nil
	}
	bytes, ok := value.([]byte)
	if !ok {
		str, ok := value.(string)
		if !ok {
			return errors.New("type assertion to []byte or string failed")
		}
		bytes = []byte(str)
	}
	if len(bytes) == 0 {
		return nil
	}
	return json.Unmarshal(bytes, w)
}

type TaskStage struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Status    string    `json:"status"` // pending, in_progress, completed, blocked
	Summary   string    `json:"summary,omitempty"`
	Evidence  string    `json:"evidence,omitempty"`
	UpdatedAt time.Time `json:"updated_at,omitempty"`
}

type TaskStages []TaskStage

func (s TaskStages) Value() (driver.Value, error) {
	if s == nil {
		return "[]", nil
	}
	return json.Marshal(s)
}

func (s *TaskStages) Scan(value interface{}) error {
	if value == nil {
		*s = []TaskStage{}
		return nil
	}
	bytes, ok := value.([]byte)
	if !ok {
		str, ok := value.(string)
		if !ok {
			return errors.New("type assertion to []byte or string failed")
		}
		bytes = []byte(str)
	}
	if len(bytes) == 0 {
		*s = []TaskStage{}
		return nil
	}
	return json.Unmarshal(bytes, s)
}

type Task struct {
	ID                           string     `gorm:"primaryKey;size:128" json:"task_id"`
	ProjectID                    string     `gorm:"index;size:128;not null" json:"project_id"`
	Title                        string     `gorm:"size:256;default:''" json:"title"`
	Mode                         string     `gorm:"size:64;default:'managed_autopilot'" json:"mode"` // managed_autopilot, lite
	State                        string     `gorm:"size:32;default:'active'" json:"state"`           // active, closed, archived, paused
	CurrentStageID               string     `gorm:"size:64;default:''" json:"current_stage_id"`
	Stages                       TaskStages `gorm:"type:text" json:"stages,omitempty"`
	CommanderSessionID           string     `gorm:"size:128;default:''" json:"commander_session_id,omitempty"`
	ExecutorSessionID            string     `gorm:"size:128;default:''" json:"executor_session_id,omitempty"`
	CommanderHeartbeatAt         *time.Time `json:"commander_heartbeat_at,omitempty"`
	ExecutorHeartbeatAt          *time.Time `json:"executor_heartbeat_at,omitempty"`
	Revision                     int64      `gorm:"default:1" json:"revision"`
	ReportSequence               int64      `gorm:"default:0" json:"report_sequence"`
	FeedbackSequence             int64      `gorm:"default:0" json:"feedback_sequence"`
	AcknowledgedTaskRevision     int64      `gorm:"default:0" json:"acknowledged_task_revision"`
	AcknowledgedFeedbackSequence int64      `gorm:"default:0" json:"acknowledged_feedback_sequence"`
	AcknowledgedReportSequence   int64      `gorm:"default:0" json:"acknowledged_report_sequence"`
	WaitPolicy                   WaitPolicy `gorm:"type:text" json:"wait_policy"`
	Segments                     []Segment  `gorm:"foreignKey:TaskID;constraint:OnDelete:CASCADE" json:"segments,omitempty"`
	CreatedAt                    time.Time  `json:"created_at"`
	UpdatedAt                    time.Time  `json:"updated_at"`
}

type Report struct {
	ID                       uint           `gorm:"primaryKey" json:"-"`
	TaskID                   string         `gorm:"index;not null;size:128" json:"task_id"`
	Sequence                 int64          `gorm:"not null" json:"sequence"`
	AcknowledgedTaskRevision int64          `gorm:"default:0" json:"acknowledged_task_revision"`
	Kind                     string         `gorm:"size:32;not null" json:"kind"` // progress, stage, evidence, question, completion
	Body                     string         `gorm:"type:text;not null" json:"body"`
	References               PathReferences `gorm:"type:text" json:"references,omitempty"`
	IdempotencyKey           string         `gorm:"size:128;index" json:"idempotency_key,omitempty"`
	CreatedAt                time.Time      `json:"created_at"`
}

type Feedback struct {
	ID             uint           `gorm:"primaryKey" json:"-"`
	TaskID         string         `gorm:"index;not null;size:128" json:"task_id"`
	Sequence       int64          `gorm:"not null" json:"sequence"`
	TaskRevision   int64          `gorm:"default:0" json:"task_revision"`
	Source         string         `gorm:"size:32;default:'human'" json:"source"` // "human" | "commander"
	Body           string         `gorm:"type:text;not null" json:"body"`
	References     PathReferences `gorm:"type:text" json:"references,omitempty"`
	IdempotencyKey string         `gorm:"size:128;index" json:"idempotency_key,omitempty"`
	CreatedAt      time.Time      `json:"created_at"`
}

type StringArray []string

func (s StringArray) Value() (driver.Value, error) {
	if s == nil {
		return "[]", nil
	}
	return json.Marshal(s)
}

func (s *StringArray) Scan(value interface{}) error {
	if value == nil {
		*s = []string{}
		return nil
	}
	bytes, ok := value.([]byte)
	if !ok {
		str, ok := value.(string)
		if !ok {
			return errors.New("type assertion to []byte or string failed")
		}
		bytes = []byte(str)
	}
	if len(bytes) == 0 {
		*s = []string{}
		return nil
	}
	return json.Unmarshal(bytes, s)
}

type SessionImage struct {
	Name     string `json:"name,omitempty"`
	Format   string `json:"format,omitempty"`
	Data     string `json:"data,omitempty"` // base64 (轻量传输时可省略)
	DataType string `json:"data_type,omitempty"`
	Hash     string `json:"hash,omitempty"`
	Width    int    `json:"width,omitempty"`
	Height   int    `json:"height,omitempty"`
	URL      string `json:"url,omitempty"`
}

// BuildSessionImageRelativeURL 统一定义图片标准相对访问路径（带不可变内容哈希）
func BuildSessionImageRelativeURL(sessionID string, index int, hash string) string {
	hashQuery := ""
	if strings.TrimSpace(hash) != "" {
		hashQuery = "?hash=" + strings.TrimSpace(hash)
	}
	return fmt.Sprintf("/api/v1/sessions/%s/images/%d%s", sessionID, index, hashQuery)
}

type SessionImages []SessionImage

func (s SessionImages) Value() (driver.Value, error) {
	if s == nil {
		return "[]", nil
	}
	return json.Marshal(s)
}

func (s *SessionImages) Scan(value interface{}) error {
	if value == nil {
		*s = []SessionImage{}
		return nil
	}
	bytes, ok := value.([]byte)
	if !ok {
		str, ok := value.(string)
		if !ok {
			return errors.New("type assertion to []byte or string failed")
		}
		bytes = []byte(str)
	}
	if len(bytes) == 0 {
		*s = []SessionImage{}
		return nil
	}
	return json.Unmarshal(bytes, s)
}

// FeedbackSession 是一次「AI 交付 + 人类回复」的轮次。
//
// 交付锚点（GitCommit / GitSubject）：模型随本次交付申报的「仓库最新提交」；时间不单独存，
// 就是本会话的 CreatedAt（服务端时间）。两列均为可空的只增列：旧二进制可打开新库，历史会话保持空值。
//
// 回复时间（RespondedAt）：人类提交这条回复的时刻——合并追加时保留第一次，撤回后清空，「秒回」取走收件箱时取其中最早一条的创建时间。
// 不能用 UpdatedAt 代替：智能体取走回复时 UpdatedAt 会被刷新，页面上「您的确认回复」的时间就成了「被取走」的时间。
// 同样是可空的只增列，历史会话保持空值（界面回退到 UpdatedAt）。
type FeedbackSession struct {
	ID string `gorm:"primaryKey;size:128" json:"session_id"`
	// WorkflowID 另有一个与 CreatedAt 的复合索引：聊天消息流按「工作流 + 时间」分页取（设计稿 03），不走它就是全表排序。
	WorkflowID string `gorm:"index;index:idx_fs_wf_created,priority:1;size:128" json:"workflow_id"`
	// Kind 区分常规阻塞式汇报与异步模式下不阻塞发布的进度气泡：空串 / feedback 是前者，notice 是后者
	//（见 workflow_mode.go）。库里不写 feedback，旧会话与旧版本保持原样，接口里也只在 notice 时才有这个字段。
	Kind             string `gorm:"size:16" json:"kind,omitempty"`
	CredentialID     *uint  `gorm:"index" json:"credential_id,omitempty"`
	HostName         string `gorm:"size:128" json:"host_name,omitempty"`
	ProjectDirectory string `gorm:"size:512" json:"project_directory"`
	Title            string `gorm:"size:256" json:"title"`
	Summary          string `gorm:"type:text;not null" json:"summary"`
	// Status 与 CreatedAt 另有复合索引 idx_fs_status_created：「当前待答会话」（GET /sessions/current，各页面轮询）按
	// status = 'pending' 过滤再按创建时间倒序取 1 行，没有它就是整表扫描 + 临时排序（#177）。
	Status               string        `gorm:"size:32;default:'pending';index:idx_fs_status_created,priority:1" json:"status"` // pending, completed, timeout, cancelled, archived
	ResponseText         string        `gorm:"type:text" json:"response_text"`
	UserMessages         StringArray   `gorm:"type:text" json:"user_messages"`
	Images               SessionImages `gorm:"type:text" json:"images"`
	ConsumedByAI         bool          `gorm:"default:false" json:"consumed_by_ai"`
	ConsumedAt           *time.Time    `json:"consumed_at,omitempty"`
	RespondedAt          *time.Time    `json:"responded_at,omitempty"`
	TimeoutSeconds       int           `gorm:"default:600" json:"timeout_seconds"`
	NoFeedbackChecks     int           `gorm:"default:0" json:"no_feedback_checks"`
	MaxNoFeedbackChecks  int           `gorm:"default:24" json:"max_no_feedback_checks"`
	PromptWaitMinutes    int           `gorm:"default:2" json:"prompt_wait_minutes"`
	WaitCountdownMinutes int           `gorm:"default:2" json:"wait_countdown_minutes"`
	// WaitCountdownSeconds 是秒级的挂起倒计时（秒回 0 / 30 / 60 …）；-1 表示没设过，沿用 WaitCountdownMinutes（#121）。
	WaitCountdownSeconds int        `gorm:"default:-1" json:"wait_countdown_seconds"`
	IsMCPActive          bool       `gorm:"-" json:"is_mcp_active"`
	MCPActiveAt          *time.Time `gorm:"-" json:"mcp_active_at,omitempty"`
	MCPTimeoutSec        int        `gorm:"-" json:"mcp_timeout_seconds,omitempty"`
	LastKeepaliveAt      *time.Time `gorm:"-" json:"last_keepalive_at,omitempty"`
	DeadlineAt           *time.Time `json:"deadline_at,omitempty"`
	GitCommit            string     `gorm:"size:40" json:"git_commit,omitempty"`
	GitSubject           string     `gorm:"size:200" json:"git_subject,omitempty"`
	// CreatedAt 还有两个索引：单列 idx_fs_created 让「按创建时间倒序取最新 N 行」（侧栏会话列表的窗口查询）直接顺着索引读 N 行，
	// 不再整表扫描 + 临时排序；与 Status 的复合索引 idx_fs_status_created 见上（#177，向后兼容的只增索引，旧版本照常读写）。
	CreatedAt time.Time `gorm:"index:idx_fs_wf_created,priority:2;index:idx_fs_created;index:idx_fs_status_created,priority:2" json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`

	// 以下两项是「整个工作流」的只读聚合（不入库）：只有会话列表与当前会话接口会带，侧栏会话卡据此推出会话级状态（#159）。
	// 用指针 + omitempty：没带（nil）与「带了、值为 0」要能区分——前者表示旧服务端 / 没算，后者表示确实没有待处理。
	PendingConfirmCount *int       `gorm:"-" json:"pending_confirm_count,omitempty"`
	AgentLastActivityAt *time.Time `gorm:"-" json:"agent_last_activity_at,omitempty"`
	// WorkflowRounds 是这个工作流在库里一共有多少行会话（含已归档，#173）：侧栏「N 轮」徽标要的是总数，
	// 而不是列表里恰好带出的行数（列表只带最新一批，老工作流往往只剩一行）。没算出来（nil）时前端退回按列表里的行数显示。
	WorkflowRounds *int `gorm:"-" json:"workflow_rounds,omitempty"`
	// BodyOmitted 标记「轻量行」（#169 第二期）：工作流会话表在 light 模式下，较早的行只带首行预览（summary / response_text 被截成第一行），
	// user_messages 与 images 为空，完整正文要按 id 另取（GET /workflows/{id}/sessions/bodies）。不入库；不是轻量行时不出现在响应里。
	BodyOmitted bool `gorm:"-" json:"body_omitted,omitempty"`
}

type QueuedFeedback struct {
	ID               uint          `gorm:"primaryKey" json:"id"`
	WorkflowID       string        `gorm:"index;index:idx_qf_wf_created,priority:1;size:128" json:"workflow_id"`
	HostName         string        `gorm:"size:128" json:"host_name,omitempty"`
	ProjectDirectory string        `gorm:"size:512" json:"project_directory"`
	ResponseText     string        `gorm:"type:text" json:"response_text"`
	UserMessages     StringArray   `gorm:"type:text" json:"user_messages"`
	Images           SessionImages `gorm:"type:text" json:"images"`
	Status           string        `gorm:"size:32;default:'pending'" json:"status"` // pending, consumed, revoked, expired
	CreatedAt        time.Time     `gorm:"index:idx_qf_wf_created,priority:2" json:"created_at"`
	UpdatedAt        time.Time     `json:"updated_at"`

	// OriginRef 记录这条追加「去了哪里」：被并进某一轮的秒回时写 s:<会话编号>；确认项答复写 c:Q12（多个用逗号分隔）。
	// 旧数据为空。聊天消息流靠它把确认项答复的投递状态挂回对应的卡（设计稿 03 / 旧稿 rev 5 附录 C）。可空的只增列。
	OriginRef string `gorm:"size:160" json:"origin_ref,omitempty"`

	// 群聊投递（阶段 2b）：kind = chat_mention 的行记下它对应哪个群的哪条消息、谁发的。可空的只增列。
	ConversationID string `gorm:"size:64" json:"conversation_id,omitempty"`
	MessageID      uint   `json:"message_id,omitempty"`
	SenderRef      string `gorm:"size:128" json:"sender_ref,omitempty"`

	// ---- 追加指令收件箱（D-210）----
	// 每条追加独立一行，各有各的状态：queued（已排队）→ delivered（Agent 读到了全文）→ acked（已确认）。
	// 旁支：revoked（Agent 完全不知道时被撤回）、retracted（Agent 读到、或被 hook / 提示行提醒过之后被撤回）、expired（超过保留期）。
	// 状态由 Status 与下面的时间戳推导（见 DeriveState），旧版本只认 Status，因此回滚到旧版本时：
	// pending 的行照旧会在 Agent 下次发起交互时被取走，不会丢。
	Kind             string     `gorm:"size:16;default:'append'" json:"kind"`   // append（预留 interrupt）；空串按 append 处理
	DeliveredAt      *time.Time `json:"delivered_at,omitempty"`                 // Agent 第一次读到全文的时间
	DeliveredVia     string     `gorm:"size:16" json:"delivered_via,omitempty"` // feedback | hook | receipt | tool：是经哪条路径被发现的
	AckedAt          *time.Time `json:"acked_at,omitempty"`
	NotifiedCount    int        `gorm:"default:0" json:"notified_count"` // hook 提醒过几次（有上限）
	LastNotifiedAt   *time.Time `json:"last_notified_at,omitempty"`
	ReceiptHintAt    *time.Time `json:"receipt_hint_at,omitempty"` // 回执里的 inbox: 提示行最近一次提醒的时间
	RetractedAt      *time.Time `json:"retracted_at,omitempty"`    // 智能体读到之后、或被 hook / 提示行提醒过之后又被撤回的时间（完全不知道的撤回不记）
	RetractNoticedAt *time.Time `json:"-"`                         // 已把「这条撤回了」告诉 Agent 的时间

	// State 是派生字段，不入库：queued | delivered | acked | revoked | retracted | expired。
	State string `gorm:"-" json:"state"`
}

// 收件箱条目的状态名。
const (
	InboxStateQueued    = "queued"
	InboxStateDelivered = "delivered"
	InboxStateAcked     = "acked"
	InboxStateRevoked   = "revoked"
	InboxStateRetracted = "retracted"
	InboxStateExpired   = "expired"
)

// DeriveState 由 Status 与时间戳推导收件箱状态；读出来的行由 store 统一填进 State。
func (q *QueuedFeedback) DeriveState() string {
	switch q.Status {
	case "consumed":
		return InboxStateAcked
	case "revoked":
		// 撤回时智能体已经知道这条（读过全文，或被 hook / 提示行提醒过——后者记在 RetractedAt 上）→ retracted；
		// 它完全不知道的 → revoked（#199）。
		if q.DeliveredAt != nil || q.RetractedAt != nil {
			return InboxStateRetracted
		}
		return InboxStateRevoked
	case "expired":
		return InboxStateExpired
	default: // pending（含历史行里的空串）
		if q.DeliveredAt != nil {
			return InboxStateDelivered
		}
		return InboxStateQueued
	}
}

type SystemSetting struct {
	Key       string    `gorm:"primaryKey;size:64" json:"key"`
	Value     string    `gorm:"type:text;not null" json:"value"`
	UpdatedAt time.Time `json:"updated_at"`
}

type UserNorm struct {
	Name      string    `gorm:"primaryKey;size:128" json:"name"`
	Summary   string    `gorm:"size:500;not null" json:"summary"`
	Content   string    `gorm:"type:text;not null" json:"content"`
	IsActive  bool      `gorm:"default:true" json:"is_active"`
	SortOrder int       `gorm:"default:0" json:"sort_order"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type Permissions struct {
	Feedback   bool `json:"feedback"`
	Sessions   bool `json:"sessions"`
	SystemInfo bool `json:"system_info"`
	Skills     bool `json:"skills"`
	Configure  bool `json:"configure"`
	// Execute 只对应自驾（autopilot）的 report_progress，与「执行命令」无关；界面文案为「自驾上报」。
	Execute bool `json:"execute"`
	// Terminal 允许使用终端执行类工具（terminal_targets / terminal_exec / terminal_run）。
	// 缺省 false，且不包含在 AllPermissions() 里：远程执行必须由人在权限页显式授予。
	Terminal bool `json:"terminal"`
	// TerminalFiles 允许终端文件类工具（P2）。同样缺省 false。
	TerminalFiles bool `json:"terminal_files"`
	// TerminalCompat 让终端工具以 ExecMesh 的工具名（targets_list / target_info / exec / exec_start / job_status /
	// job_output / job_cancel）与返回形状暴露，方便既有 ExecMesh 客户端与提示词迁移。
	// 它只改变名字与返回形状，不增加任何能力；没有 Terminal 位时没有意义（Normalized 会把它清为 false）。
	// 开启后 tools/list 与 tools/call 都只认兼容名，原生的 terminal_* 名字不再可用（两处由同一个位决定）。
	TerminalCompat bool `json:"terminal_compat"`
	// Launcher 允许使用 preset_run 工具：项目管理 Agent 运行已有的「非交互续聊」预设（#179，设计稿 design/headless-new-conversation.md §8）。
	// 缺省 false，且不包含在 AllPermissions() 里：让一个智能体去拉起另一个智能体，必须由人在凭据页显式授予；没有这一位的凭据，
	// tools/list 里根本没有这个工具（可见性按凭据而不是按身份决定，所以它同时是一个随时能关掉的总闸）。
	// 光有这一位还不够：调用时调用方还必须是所在项目的管理 Agent（acl.CanManageTasks），两道门缺一不可。
	Launcher bool `json:"launcher"`
}

// Normalized 返回归一化后的权限：兼容命名只在带有终端执行权限时才有意义。
func (p Permissions) Normalized() Permissions {
	if !p.Terminal {
		p.TerminalCompat = false
	}
	return p
}

// HasTerminal 表示凭据是否带有任一终端类权限。
func (p Permissions) HasTerminal() bool {
	return p.Terminal || p.TerminalFiles
}

// HasGovernanceTools 表示凭据是否带有 RelayMesh 治理类（工作流 / 反馈 / 自驾等）工具权限。
// 返回 false 且 HasTerminal 为 true 的凭据就是「ExecMesh 档」令牌：只暴露终端工具，也不应收到 RelayMesh 的治理指引。
func (p Permissions) HasGovernanceTools() bool {
	return p.Feedback || p.Sessions || p.SystemInfo || p.Skills || p.Configure || p.Execute
}

// IsTerminalOnly 判断是否为只含终端类权限的令牌（ExecMesh 档）。
func (p Permissions) IsTerminalOnly() bool {
	return p.HasTerminal() && !p.HasGovernanceTools()
}

func (p Permissions) Value() (driver.Value, error) {
	return json.Marshal(p)
}

func (p *Permissions) Scan(value interface{}) error {
	if value == nil {
		*p = Permissions{}
		return nil
	}
	bytes, ok := value.([]byte)
	if !ok {
		str, ok := value.(string)
		if !ok {
			return errors.New("type assertion to []byte or string failed")
		}
		bytes = []byte(str)
	}
	if len(bytes) == 0 {
		*p = Permissions{}
		return nil
	}
	return json.Unmarshal(bytes, p)
}

// AllPermissions 返回 RelayMesh 治理类的全部权限。
// 注意：它**不包含** Terminal / TerminalFiles——开放访问、环境变量令牌与本地 stdio 都不应隐式获得远程执行能力。
func AllPermissions() Permissions {
	return Permissions{
		Feedback:   true,
		Sessions:   true,
		SystemInfo: true,
		Skills:     true,
		Configure:  true,
		Execute:    true,
	}
}

func (p Permissions) AllowsTool(toolName string) bool {
	switch toolName {
	case "interactive_feedback", "continue_feedback_session", "get_session_image":
		return p.Feedback
	case "list_sessions", "get_session_history":
		return p.Sessions
	case "get_system_info":
		return p.SystemInfo
	case "manage_skills":
		return p.Skills
	case "configure_task":
		return p.Configure
	case "report_progress":
		return p.Execute
	case "workflow_context", "task_board", "project_docs", "async_ask", "chat":
		return p.Feedback || p.Sessions
	case "usage_guide":
		// 内置使用手册（#181）：只读；与上面几个治理工具同一档，ExecMesh 档（只能执行命令）的令牌看不到它。
		return p.Feedback || p.Sessions
	case "terminal_targets":
		// 只带文件权限的令牌同样需要它：模型靠它得知有哪些主机、各自的家目录与路径分隔符，以及允许访问的目录。
		return (p.Terminal && !p.TerminalCompat) || (p.TerminalFiles && !p.Terminal)
	case "terminal_exec", "terminal_run":
		return p.Terminal && !p.TerminalCompat
	case "targets_list", "target_info", "exec", "exec_start", "job_status", "job_output", "job_cancel":
		return p.Terminal && p.TerminalCompat
	case "terminal_files":
		return p.TerminalFiles
	case "preset_run":
		return p.Launcher
	default:
		return false
	}
}

// StringList 以 JSON 文本存放的字符串列表（SQLite 里是 text 列，API 里是数组）。
type StringList []string

func (l StringList) Value() (driver.Value, error) {
	if len(l) == 0 {
		return "", nil
	}
	return json.Marshal([]string(l))
}

func (l *StringList) Scan(value interface{}) error {
	if value == nil {
		*l = nil
		return nil
	}
	var raw []byte
	switch v := value.(type) {
	case []byte:
		raw = v
	case string:
		raw = []byte(v)
	default:
		return errors.New("type assertion to []byte or string failed")
	}
	if len(strings.TrimSpace(string(raw))) == 0 {
		*l = nil
		return nil
	}
	var out []string
	if err := json.Unmarshal(raw, &out); err != nil {
		return err
	}
	*l = out
	return nil
}

// TerminalLimits 是令牌级的终端限额。全部为零值表示「不设限」——这是默认值，由人在权限页主动设置。
type TerminalLimits struct {
	MaxConcurrent     int      `json:"max_concurrent,omitempty"`      // 同时运行的上限
	MaxWaitSeconds    int      `json:"max_wait_seconds,omitempty"`    // terminal_exec 的 wait_seconds 上限
	MaxTimeoutSeconds int      `json:"max_timeout_seconds,omitempty"` // 单次运行 timeout_seconds 上限
	MaxOutputBytes    int      `json:"max_output_bytes,omitempty"`    // 单次返回的输出字节上限
	AllowedDirs       []string `json:"allowed_dirs,omitempty"`        // 工作目录白名单（前缀匹配）；为空表示不限
	// AllowedPaths 是文件工具（terminal_files）可以访问的目录。与 AllowedDirs 不同，它是强制边界：
	// 文件权限位打开时必须非空（或明确开启 AllowWholeFS），路径经符号链接也不能逃出这些目录；为空表示此令牌没有任何文件访问范围。
	AllowedPaths []string `json:"allowed_paths,omitempty"`
	// AllowWholeFS 是「允许整个文件系统」开关：文件工具不再限制在 AllowedPaths 里，而是可以访问这台主机上
	// Agent 所属账户能访问的任何路径（Windows 上是任一盘符）。这是一个显式的高风险开关，默认关闭：
	// 在允许目录里写 `/` 仍然被拒绝（防手误），只有这个开关能放开；开启时必须刚刚重新验证过密码并写审计，
	// 且只在文件权限位打开时才有意义。开关开着时 AllowedPaths 在运行时不再起作用，但原样保留（仍要合法）：
	// 关闭开关后回到管理员原本明确列出的目录，而不是更宽的范围。
	AllowWholeFS bool `json:"allow_whole_fs,omitempty"`
}

func (l TerminalLimits) IsZero() bool {
	return l.MaxConcurrent == 0 && l.MaxWaitSeconds == 0 && l.MaxTimeoutSeconds == 0 &&
		l.MaxOutputBytes == 0 && len(l.AllowedDirs) == 0 && len(l.AllowedPaths) == 0 && !l.AllowWholeFS
}

// HasFileScope 报告文件工具的访问范围是否非空：至少一个允许目录，或明确开启了「允许整个文件系统」。
// 文件权限没有「默认全部可访问」的含义——两者都没有就等于没有范围。
func (l TerminalLimits) HasFileScope() bool { return l.AllowWholeFS || len(l.AllowedPaths) > 0 }

// EffectiveWholeFS 报告这把令牌是否真的拥有「整个文件系统」的访问：开关打开且文件权限位同时打开。
// 文件权限位关闭时，开关只是一个被保留的设置，不产生任何访问；权限位再次打开时它会重新生效——
// 所以「从无到有」的判断（是否需要重新验证密码、是否写审计）必须用这个值，而不是只看开关本身。
func EffectiveWholeFS(p Permissions, l TerminalLimits) bool { return p.TerminalFiles && l.AllowWholeFS }

func (l TerminalLimits) Value() (driver.Value, error) {
	if l.IsZero() {
		return "", nil
	}
	return json.Marshal(l)
}

func (l *TerminalLimits) Scan(value interface{}) error {
	if value == nil {
		*l = TerminalLimits{}
		return nil
	}
	var raw []byte
	switch v := value.(type) {
	case []byte:
		raw = v
	case string:
		raw = []byte(v)
	default:
		return errors.New("type assertion to []byte or string failed")
	}
	if len(strings.TrimSpace(string(raw))) == 0 {
		*l = TerminalLimits{}
		return nil
	}
	return json.Unmarshal(raw, l)
}

// 令牌的终端执行模式上限；空字符串表示跟随主机设置。
const (
	TerminalModeFollow   = ""
	TerminalModeAuto     = "auto"
	TerminalModeApprove  = "approve"
	TerminalModeReadonly = "readonly"
)

type MCPCredential struct {
	ID          uint        `gorm:"primaryKey" json:"id"`
	Name        string      `gorm:"size:128;uniqueIndex;not null" json:"name"`
	Token       string      `gorm:"size:256;uniqueIndex;not null" json:"token"`
	HostName    string      `gorm:"size:128" json:"host_name"`
	IsActive    bool        `gorm:"default:true" json:"is_active"`
	Permissions Permissions `gorm:"type:text" json:"permissions"`
	Note        string      `gorm:"size:512" json:"note"`
	CreatedAt   time.Time   `json:"created_at"`
	UpdatedAt   time.Time   `json:"updated_at"`

	// 终端相关（只增字段，旧库自动迁移，旧版二进制仍可打开）。
	TerminalHosts   StringList     `gorm:"type:text" json:"terminal_hosts"`                     // 可用主机机器码；["*"] 表示全部；空表示无
	TerminalModeCap string         `gorm:"size:16" json:"terminal_mode_cap"`                    // "" 跟随主机 / auto / approve / readonly（只能比主机更严）
	TerminalLimits  TerminalLimits `gorm:"type:text" json:"terminal_limits"`                    // 令牌级限额，默认不设限
	ExpiresAt       *time.Time     `json:"expires_at"`                                          // 过期时间；为空表示不过期
	AllowedCIDRs    StringList     `gorm:"column:allowed_cidrs;type:text" json:"allowed_cidrs"` // 来源 IP 白名单（CIDR 或单个 IP）；空表示不限
	LastUsedAt      *time.Time     `json:"last_used_at"`
	LastUsedIP      string         `gorm:"size:64" json:"last_used_ip"`

	// Capability 表示这是一把「能力 URL」凭据：令牌本身是网址的一部分（/mcp/c/<令牌>），给无法发送请求头的客户端
	// （例如只能填一个网址的自定义连接器）使用。它只在该网址上有效（请求头 / 查询串 / 旧式路径一律不认），
	// 只能是 ExecMesh 档并逐台授权主机，创建后不可更改。
	Capability bool `gorm:"default:false" json:"capability"`

	// Hidden 为 true 时凭据由 OAuth 授权自动创建，不在默认列表与手动创建接口中暴露。
	Hidden bool `gorm:"default:false" json:"hidden"`
	// OAuthSource 标记凭据来源（如 oauth）；空表示普通手动令牌。
	OAuthSource  string `gorm:"size:32" json:"oauth_source,omitempty"`
	OAuthGrantID *uint  `gorm:"index" json:"oauth_grant_id,omitempty"`
}

// CapabilityProblem 检查能力 URL 凭据必须长期满足的约束；不是能力 URL 凭据，或没有问题时返回空串。
// 创建 / 更新时用它拒绝违规配置，运行时再用它兜底（例如数据库被手工改动过）。
func (c MCPCredential) CapabilityProblem() string {
	if !c.Capability {
		return ""
	}
	if !c.Permissions.Terminal || c.Permissions.HasGovernanceTools() {
		return "能力 URL 凭据只能是 ExecMesh 档：只带终端执行权限，不能带 RelayMesh 治理类权限"
	}
	if c.Permissions.Launcher {
		// 网址一旦泄露就等于口令泄露：让拿到网址的人去唤醒别的智能体，授权面太大；运行预设只给普通的请求头令牌。
		return "能力 URL 凭据不能带「运行预设」权限"
	}
	for _, h := range c.TerminalHosts {
		if h == "*" {
			return "能力 URL 凭据不支持「全部主机」，请逐台授权"
		}
	}
	return ""
}

// AllowsTerminalHost 判断令牌是否可使用指定机器码的主机。
func (c MCPCredential) AllowsTerminalHost(machineCode string) bool {
	if !c.Permissions.Terminal && !c.Permissions.TerminalFiles {
		return false
	}
	for _, h := range c.TerminalHosts {
		if h == "*" || h == machineCode {
			return true
		}
	}
	return false
}

type WorkflowDraft struct {
	WorkflowID  string    `gorm:"primaryKey;size:128" json:"workflow_id"`
	ActiveIndex int       `gorm:"default:0" json:"active_index"`
	DraftsJSON  string    `gorm:"type:text" json:"drafts_json"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// DraftImage 草稿区的一张图片（云存储）：粘贴即上传，草稿 JSON 里只留引用；
// 提交时会话直接复用这份正文（不再重传），草稿销毁时删除。主键 (工作流, 内容键)：同一张图在一个工作流里只存一份。
type DraftImage struct {
	WorkflowID string `gorm:"primaryKey;size:128" json:"workflow_id"`
	// Key 是前端对整段正文算出的内容键（长度 + 53 位哈希），服务端只当作不透明标识使用。
	Key      string `gorm:"primaryKey;size:96" json:"key"`
	Name     string `gorm:"size:255" json:"name,omitempty"`
	Format   string `gorm:"size:16" json:"format,omitempty"`
	DataType string `gorm:"size:16" json:"data_type,omitempty"`
	Hash     string `gorm:"size:64" json:"hash,omitempty"`
	Width    int    `json:"width,omitempty"`
	Height   int    `json:"height,omitempty"`
	// Size 是正文（Base64 文本）的长度，用于配额统计，列表时不必读出正文。
	Size int    `json:"size"`
	Data string `gorm:"type:text" json:"data,omitempty"`
	// UpdatedAt 是最近一次写入（上传 / 重新确认）的时间，后台清理据此判断「遗弃多久了」。
	UpdatedAt time.Time `gorm:"index" json:"updated_at"`
}

type WaitStatus struct {
	State                string `json:"state"` // waiting, exhausted
	SourceReportSequence int64  `json:"source_report_sequence,omitempty"`
	NoFeedbackChecks     int    `json:"no_feedback_checks,omitempty"`
	MaxChecks            int    `json:"max_checks,omitempty"`
	AfterMinutes         int    `json:"after_minutes,omitempty"`
	Instruction          string `json:"instruction"`
}

type PhaseItem struct {
	ID          string `json:"id"`
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
	Prompt      string `json:"prompt,omitempty"`
}

type WorkflowPhaseState struct {
	WorkflowID            string    `gorm:"primaryKey;size:128" json:"workflow_id"`
	CurrentPhaseID        string    `gorm:"size:64;not null;default:'assess'" json:"current_phase_id"`
	HumanPreferredPhaseID string    `gorm:"size:64;not null;default:'assess'" json:"human_preferred_phase_id"`
	PhasesJSON            string    `gorm:"type:text" json:"phases_json"`
	UpdatedAt             time.Time `json:"updated_at"`
}

// ==========================================
// Recovery Capsule — Workflow Checkpoints
// ==========================================

type CheckpointBasis struct {
	TaskRevision     int    `json:"task_revision,omitempty"`
	ReportSequence   int    `json:"report_sequence,omitempty"`
	FeedbackSequence int    `json:"feedback_sequence,omitempty"`
	GitCommit        string `json:"git_commit,omitempty"`
	// AnchorSessionID 指向胶囊所基于的那次交付（其 git_commit 与上面的哈希一致），
	// 供 Web 时间线把胶囊与交付关联起来。缺省基准时由服务端自动填写。
	AnchorSessionID string `json:"anchor_session_id,omitempty"`
}

type CheckpointStage struct {
	ID     string `json:"id"`
	Status string `json:"status"`
}

type CheckpointClaim struct {
	Claim    string   `json:"claim"`
	Evidence []string `json:"evidence,omitempty"`
}

type CheckpointDecision struct {
	DecisionID string `json:"decision_id"`
	Status     string `json:"status"`
	Summary    string `json:"summary"`
	Supersedes string `json:"supersedes,omitempty"`
	Source     string `json:"source,omitempty"`
}

type CheckpointContent struct {
	Basis              CheckpointBasis      `json:"basis"`
	Objective          string               `json:"objective"`
	AcceptanceCriteria []string             `json:"acceptance_criteria,omitempty"`
	CurrentStage       *CheckpointStage     `json:"current_stage,omitempty"`
	Completed          []CheckpointClaim    `json:"completed,omitempty"`
	Decisions          []CheckpointDecision `json:"decisions,omitempty"`
	InProgress         []string             `json:"in_progress,omitempty"`
	NextActions        []string             `json:"next_actions,omitempty"`
	Blockers           []string             `json:"blockers,omitempty"`
	OpenQuestions      []string             `json:"open_questions,omitempty"`
	Constraints        []string             `json:"constraints,omitempty"`
	ChangedFiles       []string             `json:"changed_files,omitempty"`
	Verification       []string             `json:"verification,omitempty"`
	Unknowns           []string             `json:"unknowns,omitempty"`
	ResumeInstruction  string               `json:"resume_instruction,omitempty"`
}

type WorkflowCheckpoint struct {
	ID          uint      `gorm:"primaryKey;autoIncrement" json:"id"`
	WorkflowID  string    `gorm:"index;size:128;not null" json:"workflow_id"`
	Revision    int       `gorm:"not null" json:"revision"`
	Status      string    `gorm:"size:32;not null;default:'submitted'" json:"status"`
	ContentJSON string    `gorm:"type:text;not null" json:"content_json"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// ==========================================
// AI Notes — session-level and workflow-level
// ==========================================

type WorkflowNote struct {
	ID         uint      `gorm:"primaryKey;autoIncrement" json:"id"`
	WorkflowID string    `gorm:"index;size:128" json:"workflow_id"`
	SessionID  string    `gorm:"index;size:128" json:"session_id,omitempty"`
	NoteKey    string    `gorm:"size:128" json:"note_key,omitempty"`
	Content    string    `gorm:"type:text;not null" json:"content"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// HoldSeconds 返回秒级挂起倒计时；没有设过（-1）时 ok=false，调用方回落到分钟值。
func (s *FeedbackSession) HoldSeconds() (int, bool) {
	if s == nil || s.WaitCountdownSeconds < 0 {
		return 0, false
	}
	return s.WaitCountdownSeconds, true
}
