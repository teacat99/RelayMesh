package model

import "time"

const (
	MaxSegmentBytes           = 32 * 1024
	MaxWorkOrderBytes         = 48 * 1024
	MaxTaskContentBytes       = 128 * 1024
	MaxReportingGuideBytes    = 12 * 1024
	MaxReportingExampleBytes  = 16 * 1024
	MaxMessageBytes           = 32 * 1024
	MaxPathReferences         = 50
	MaxSegments               = 64
	MaxPageSize               = 100
	MaxCommunicationPageBytes = 160 * 1024
)

type TaskSummary struct {
	ID                         string     `json:"task_id"`
	ProjectID                  string     `json:"project_id"`
	Title                      string     `json:"title"`
	Mode                       string     `json:"mode"`
	State                      string     `json:"state"`
	CurrentStageID             string     `json:"current_stage_id,omitempty"`
	Stages                     TaskStages `json:"stages,omitempty"`
	CommanderSessionID         string     `json:"commander_session_id,omitempty"`
	ExecutorSessionID          string     `json:"executor_session_id,omitempty"`
	Revision                   int64      `json:"revision"`
	ReportSequence             int64      `json:"report_sequence"`
	FeedbackSequence           int64      `json:"feedback_sequence"`
	AcknowledgedReportSequence int64      `json:"acknowledged_report_sequence"`
	UnreadReportCount          int64      `json:"unread_report_count"`
	UpdatedAt                  time.Time  `json:"updated_at"`
}

type CreateTaskInput struct {
	ProjectID      string     `json:"project_id"`
	TaskID         string     `json:"task_id,omitempty"`
	Title          string     `json:"title,omitempty"`
	Mode           string     `json:"mode,omitempty"`
	Stages         TaskStages `json:"stages,omitempty"`
	Segments       []Segment  `json:"segments"`
	WaitPolicy     WaitPolicy `json:"wait_policy"`
	IdempotencyKey string     `json:"idempotency_key,omitempty"`
}

type UpdateTaskInput struct {
	ProjectID        string `json:"project_id"`
	TaskID           string `json:"task_id"`
	ExpectedRevision int64  `json:"expected_revision"`
	Mode             string `json:"mode"` // replace, patch
	Segment          string `json:"segment"`
	OldText          string `json:"old_text,omitempty"`
	NewText          string `json:"new_text"`
	IdempotencyKey   string `json:"idempotency_key,omitempty"`
}

type UpdateStagesInput struct {
	ProjectID        string     `json:"project_id"`
	TaskID           string     `json:"task_id"`
	ExpectedRevision int64      `json:"expected_revision,omitempty"`
	CurrentStageID   string     `json:"current_stage_id,omitempty"`
	Stages           TaskStages `json:"stages"`
	IdempotencyKey   string     `json:"idempotency_key,omitempty"`
}

type SetWaitPolicyInput struct {
	ProjectID        string     `json:"project_id"`
	TaskID           string     `json:"task_id"`
	ExpectedRevision int64      `json:"expected_revision"`
	WaitPolicy       WaitPolicy `json:"wait_policy"`
	IdempotencyKey   string     `json:"idempotency_key,omitempty"`
}

type MutationResult struct {
	TaskID   string `json:"task_id"`
	Revision int64  `json:"revision"`
	State    string `json:"state,omitempty"`
}

type SendFeedbackInput struct {
	ProjectID        string          `json:"project_id"`
	TaskID           string          `json:"task_id"`
	ExpectedRevision int64           `json:"expected_revision"`
	Source           string          `json:"source,omitempty"` // "human" | "commander"
	Body             string          `json:"body"`
	References       []PathReference `json:"references,omitempty"`
	IdempotencyKey   string          `json:"idempotency_key,omitempty"`
}

type AddReportInput struct {
	ProjectID                    string          `json:"project_id"`
	TaskID                       string          `json:"task_id"`
	AcknowledgedTaskRevision     int64           `json:"acknowledged_task_revision"`
	AcknowledgedFeedbackSequence int64           `json:"acknowledged_feedback_sequence"`
	Kind                         string          `json:"kind"`
	Body                         string          `json:"body"`
	References                   []PathReference `json:"references,omitempty"`
	IdempotencyKey               string          `json:"idempotency_key,omitempty"`
}

type ReportResult struct {
	Report           Report     `json:"report"`
	FeedbackSequence int64      `json:"feedback_sequence"`
	Feedback         []Feedback `json:"feedback,omitempty"`
	Wait             WaitStatus `json:"wait"`
}

type SyncInput struct {
	ProjectID                    string `json:"project_id"`
	TaskID                       string `json:"task_id"`
	KnownTaskRevision            int64  `json:"known_task_revision"`
	AcknowledgedTaskRevision     int64  `json:"acknowledged_task_revision"`
	AfterFeedbackSequence        int64  `json:"after_feedback_sequence"`
	AcknowledgedFeedbackSequence int64  `json:"acknowledged_feedback_sequence"`
}

type SyncResult struct {
	TaskID                       string      `json:"task_id"`
	State                        string      `json:"state"`
	Revision                     int64       `json:"revision"`
	ReportSequence               int64       `json:"report_sequence"`
	FeedbackSequence             int64       `json:"feedback_sequence"`
	AcknowledgedTaskRevision     int64       `json:"acknowledged_task_revision"`
	AcknowledgedFeedbackSequence int64       `json:"acknowledged_feedback_sequence"`
	Segments                     []Segment   `json:"segments,omitempty"`
	Feedback                     []Feedback  `json:"feedback,omitempty"`
	Wait                         *WaitStatus `json:"wait,omitempty"`
}

type CheckFeedbackInput struct {
	ProjectID                    string `json:"project_id"`
	TaskID                       string `json:"task_id"`
	AfterFeedbackSequence        int64  `json:"after_feedback_sequence"`
	AcknowledgedTaskRevision     int64  `json:"acknowledged_task_revision"`
	AcknowledgedFeedbackSequence int64  `json:"acknowledged_feedback_sequence"`
	IdempotencyKey               string `json:"idempotency_key,omitempty"`
}

type CheckFeedbackResult struct {
	Feedback []Feedback `json:"feedback,omitempty"`
	Wait     WaitStatus `json:"wait"`
}

type TaskPage struct {
	Tasks      []TaskSummary `json:"tasks"`
	NextCursor string        `json:"next_cursor,omitempty"`
}

type ReportPage struct {
	Reports      []Report `json:"reports"`
	NextSequence int64    `json:"next_sequence"`
	HasMore      bool     `json:"has_more"`
}

type AckSummary struct {
	TaskID                     string `json:"task_id"`
	AcknowledgedReportSequence int64  `json:"acknowledged_report_sequence"`
	UnreadReportCount          int64  `json:"unread_report_count"`
}
