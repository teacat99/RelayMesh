package model

import (
	"database/sql/driver"
	"encoding/json"
	"errors"
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
	State                        string     `gorm:"size:32;default:'active'" json:"state"`          // active, closed, archived, paused
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
	Data     string `json:"data"` // base64
	DataType string `json:"data_type,omitempty"`
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

type FeedbackSession struct {
	ID                   string        `gorm:"primaryKey;size:128" json:"session_id"`
	WorkflowID           string        `gorm:"index;size:128" json:"workflow_id"`
	CredentialID         *uint         `gorm:"index" json:"credential_id,omitempty"`
	HostName             string        `gorm:"size:128" json:"host_name,omitempty"`
	ProjectDirectory     string        `gorm:"size:512" json:"project_directory"`
	Title                string        `gorm:"size:256" json:"title"`
	Summary              string        `gorm:"type:text;not null" json:"summary"`
	Status               string        `gorm:"size:32;default:'pending'" json:"status"` // pending, completed, timeout, cancelled, archived
	UserPresence         string        `gorm:"size:32;default:'online'" json:"user_presence"` // online, away, autopilot
	ResponseText         string        `gorm:"type:text" json:"response_text"`
	UserMessages         StringArray   `gorm:"type:text" json:"user_messages"`
	Images               SessionImages `gorm:"type:text" json:"images"`
	ConsumedByAI         bool          `gorm:"default:false" json:"consumed_by_ai"`
	TimeoutSeconds       int           `gorm:"default:600" json:"timeout_seconds"`
	NoFeedbackChecks     int           `gorm:"default:0" json:"no_feedback_checks"`
	MaxNoFeedbackChecks  int           `gorm:"default:24" json:"max_no_feedback_checks"`
	PromptWaitMinutes    int           `gorm:"default:2" json:"prompt_wait_minutes"`
	WaitCountdownMinutes int           `gorm:"default:2" json:"wait_countdown_minutes"`
	IsMCPActive          bool          `gorm:"-" json:"is_mcp_active"`
	MCPActiveAt          *time.Time    `gorm:"-" json:"mcp_active_at,omitempty"`
	MCPTimeoutSec        int           `gorm:"-" json:"mcp_timeout_seconds,omitempty"`
	LastKeepaliveAt      *time.Time    `gorm:"-" json:"last_keepalive_at,omitempty"`
	DeadlineAt           *time.Time    `json:"deadline_at,omitempty"`
	CreatedAt            time.Time     `json:"created_at"`
	UpdatedAt            time.Time     `json:"updated_at"`
}

type QueuedFeedback struct {
	ID               uint          `gorm:"primaryKey" json:"id"`
	WorkflowID       string        `gorm:"index;size:128" json:"workflow_id"`
	HostName         string        `gorm:"size:128" json:"host_name,omitempty"`
	ProjectDirectory string        `gorm:"size:512" json:"project_directory"`
	ResponseText     string        `gorm:"type:text" json:"response_text"`
	UserMessages     StringArray   `gorm:"type:text" json:"user_messages"`
	Images           SessionImages `gorm:"type:text" json:"images"`
	Status           string        `gorm:"size:32;default:'pending'" json:"status"` // pending, consumed, revoked
	CreatedAt        time.Time     `json:"created_at"`
	UpdatedAt        time.Time     `json:"updated_at"`
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
	Execute    bool `json:"execute"`
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
	default:
		return false
	}
}

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
}

type WorkflowDraft struct {
	WorkflowID  string    `gorm:"primaryKey;size:128" json:"workflow_id"`
	ActiveIndex int       `gorm:"default:0" json:"active_index"`
	DraftsJSON  string    `gorm:"type:text" json:"drafts_json"`
	UpdatedAt   time.Time `json:"updated_at"`
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

func DefaultPhaseTemplate() []PhaseItem {
	return []PhaseItem{
		{ID: "assess", Label: "评估", Description: "需求接入与理解确认", Prompt: "当前处于需求评估阶段。通过 feedback 收集用户描述，逐条记录到会话文档，保留用户原话。对每条需求复述自己的理解：真实场景、根因推测、期望行为、验收标准。等待用户确认后再进入方案阶段。不急于敲定方案选型，先听完并理解真实需求，并引导用户完善需求，汇报不同方案的利弊，对每个需求列出推荐方案、风险、改动范围与备选。方向敲定后可调整到方案阶段。⚠️ 本阶段禁止修改代码。可以读取代码验证可行性，但不得创建、修改或删除任何源代码文件。如确需修改代码，必须先通过 feedback 获得用户二次确认并切换到开发阶段。"},
		{ID: "plan", Label: "方案", Description: "方案设计与评审拍板", Prompt: "当前处于方案设计阶段。决策确认→问题拆解→数据流→不变量→边界→影响面→备选→验收。阅读代码和文档，注意核对方案可行性，确保实施阶段的逻辑闭环；通过 feedback 与用户逐项确认，将每条决策写入会话文档「关键决策」。决策全部锁定后等待用户确认再进入开发。⚠️ 本阶段禁止修改代码。可以读取代码验证可行性，但不得创建、修改或删除任何源代码文件。如确需修改代码，必须先通过 feedback 获得用户二次确认并切换到开发阶段。"},
		{ID: "dev", Label: "开发", Description: "编码实施与增量验证", Prompt: "当前处于开发执行阶段。改前先读相关代码与文档，沿用项目惯用模式。每完成一个逻辑单元立即增量验证（lint/type-check→build），不等全部完成再统一修。改 import/接口/类型时检查所有引用方。每 200-500 行改动即 commit。如发现方案和代码冲突，先记录并尝试解决，解决不了则向用户汇报，并回退到方案阶段。开发完成后进入验证阶段。"},
		{ID: "verify", Label: "验证", Description: "三件套通过与功能验证", Prompt: "当前处于部署验证阶段。执行三件套：lint/type-check→build→功能验证。部署、DB迁移、push main 等不可逆操作必须二次确认。完成标准 = 功能 + 类型 + 编译 + 校验 + 文档同步 + 配置同步 + 开发记录，每条须有可验证证据。验证失败回退到开发阶段修复，验证成功进入完成阶段，使用 feedback 汇报。"},
		{ID: "done", Label: "完成", Description: "汇报完成与等待下一步", Prompt: "当前阶段的开发任务完成。通过 feedback 提交最终汇报：修改内容、原因、影响范围、验证结果与后续建议。盘点后台进程和未提交变更，归档会话文档。等待用户确认下一步需求或结束会话。"},
	}
}
