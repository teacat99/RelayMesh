package model

import "time"

// ArtifactData 是一篇 Artifact（render 为 html / data 的文档）自带的 JSON 键值数据。
// 设计稿 06 §6：每篇 ≤ 256 KB、≤ 200 键；写入记审计（不记值）。
type ArtifactData struct {
	ID    uint   `gorm:"primaryKey" json:"id"`
	DocID uint   `gorm:"not null;uniqueIndex:ux_artifact_data,priority:1" json:"doc_id"`
	Key   string `gorm:"column:data_key;size:128;not null;uniqueIndex:ux_artifact_data,priority:2" json:"key"`
	// Value 是合法 JSON 文本。
	Value string `gorm:"type:text;not null" json:"value"`
	// Version 每次写入 +1，用于乐观并发（看板被多个 Agent 同时写时避免互相覆盖）。
	Version      int       `gorm:"not null;default:1" json:"version"`
	UpdatedBy    string    `gorm:"size:128" json:"updated_by"`
	UpdatedAt    time.Time `json:"updated_at"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedByRef string    `gorm:"size:64" json:"updated_by_ref,omitempty"`
}

// Artifact 数据的上限。
const (
	ArtifactDataMaxBytes = 256 * 1024
	ArtifactDataMaxKeys  = 200
	ArtifactKeyMaxLen    = 128
)

// ArtifactComment 是对一篇文档（尤其是 Artifact）的评论 / 批注，Agent 经 project_docs 的 comments 动作读取。
type ArtifactComment struct {
	ID          uint       `gorm:"primaryKey" json:"id"`
	DocID       uint       `gorm:"not null;index:idx_artifact_comment_doc,priority:1" json:"doc_id"`
	Anchor      string     `gorm:"size:128" json:"anchor,omitempty"`
	Body        string     `gorm:"type:text;not null" json:"body"`
	AuthorKind  string     `gorm:"size:16;not null" json:"author_kind"` // human | identity
	AuthorRef   string     `gorm:"size:64" json:"author_ref,omitempty"`
	AuthorLabel string     `gorm:"size:128" json:"author_label"`
	Resolved    bool       `gorm:"not null;default:false;index:idx_artifact_comment_doc,priority:2" json:"resolved"`
	ResolvedAt  *time.Time `json:"resolved_at,omitempty"`
	CreatedAt   time.Time  `gorm:"index" json:"created_at"`
}

// ArtifactCommentMaxLen 是一条评论的字数上限。
const ArtifactCommentMaxLen = 4000
