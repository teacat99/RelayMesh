package model

import (
	"strings"
	"time"
)

// 项目文档类型与状态（见 docs/project-docs.md）。
const (
	DocKindDesign    = "design"
	DocKindDecision  = "decision"
	DocKindPlan      = "plan"
	DocKindEval      = "eval"
	DocKindSpec      = "spec"
	DocKindGuide     = "guide"
	DocKindReference = "reference"
	DocKindRunbook   = "runbook"
	DocKindNote      = "note"
	DocKindChangelog = "changelog"
	DocKindOther     = "other"

	DocStatusDraft      = "draft"
	DocStatusReview     = "review"
	DocStatusApproved   = "approved"
	DocStatusSuperseded = "superseded"
	DocStatusArchived   = "archived"

	DocActorHuman  = "human"
	DocActorAgent  = "agent"
	DocActorImport = "import"
)

var DocKinds = []string{
	DocKindDesign, DocKindDecision, DocKindPlan, DocKindEval, DocKindSpec,
	DocKindGuide, DocKindReference, DocKindRunbook, DocKindNote, DocKindChangelog, DocKindOther,
}

var DocStatuses = []string{
	DocStatusDraft, DocStatusReview, DocStatusApproved, DocStatusSuperseded, DocStatusArchived,
}

// ProjectDoc 是工作流内的一篇 Markdown 文档。
type ProjectDoc struct {
	ID         uint        `gorm:"primaryKey;autoIncrement" json:"id"`
	WorkflowID string      `gorm:"size:128;not null;index;uniqueIndex:ux_project_doc_seq,priority:1;uniqueIndex:ux_project_doc_path,priority:1" json:"workflow_id"`
	Seq        int         `gorm:"not null;uniqueIndex:ux_project_doc_seq,priority:2" json:"seq"`
	Path       string      `gorm:"size:512;not null;uniqueIndex:ux_project_doc_path,priority:2" json:"path"`
	Title      string      `gorm:"size:256;not null" json:"title"`
	Kind       string      `gorm:"size:16;not null;default:'note';index" json:"kind"`
	Status     string      `gorm:"size:16;not null;default:'draft';index" json:"status"`
	Tags       StringArray `gorm:"type:text" json:"tags"`
	Body       string      `gorm:"type:text" json:"body"`
	Rev        int         `gorm:"not null;default:1" json:"rev"`
	Hash       string      `gorm:"size:64;not null" json:"hash"`
	// 项目知识库（#114）：文档仍归属某个工作流（老唯一键不动），project_id 把它归入项目；0 = 没有归属项目。
	ProjectID       uint       `gorm:"not null;default:0;index" json:"project_id,omitempty"`
	ModulePath      string     `gorm:"size:256;not null;default:'';index" json:"module_path,omitempty"`
	OwnerIdentityID uint       `gorm:"not null;default:0" json:"owner_identity_id,omitempty"`
	Render          string     `gorm:"size:16;not null;default:'md'" json:"render,omitempty"`
	CreatedBy       string     `gorm:"size:16;not null" json:"created_by"`
	UpdatedBy       string     `gorm:"size:16;not null" json:"updated_by"`
	CreatedAt       time.Time  `json:"created_at"`
	UpdatedAt       time.Time  `json:"updated_at"`
	DeletedAt       *time.Time `gorm:"index" json:"deleted_at,omitempty"`
}

// ProjectDocRevision 保存历史正文（每篇最多保留 50 版）。
type ProjectDocRevision struct {
	ID        uint      `gorm:"primaryKey;autoIncrement" json:"id"`
	DocID     uint      `gorm:"not null;index;uniqueIndex:ux_doc_rev,priority:1" json:"doc_id"`
	Rev       int       `gorm:"not null;uniqueIndex:ux_doc_rev,priority:2" json:"rev"`
	Body      string    `gorm:"type:text" json:"body"`
	Summary   string    `gorm:"size:500" json:"summary"`
	Actor     string    `gorm:"size:16;not null" json:"actor"`
	CreatedAt time.Time `json:"created_at"`
}

// ProjectDocCounter 保证 seq 单调不复用。
type ProjectDocCounter struct {
	WorkflowID string `gorm:"primaryKey;size:128" json:"workflow_id"`
	LastSeq    int    `gorm:"not null;default:0" json:"last_seq"`
}

func NormalizeDocKind(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	for _, k := range DocKinds {
		if k == s {
			return k
		}
	}
	return ""
}

func NormalizeDocStatus(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	for _, k := range DocStatuses {
		if k == s {
			return k
		}
	}
	return ""
}
