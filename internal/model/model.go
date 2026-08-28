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

type Task struct {
	ID                           string     `gorm:"primaryKey;size:128" json:"task_id"`
	ProjectID                    string     `gorm:"index;size:128;not null" json:"project_id"`
	State                        string     `gorm:"size:32;default:'active'" json:"state"` // active, closed, archived
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
