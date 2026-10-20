package model

import (
	"strings"
	"time"
)

// 项目知识库（#114，设计稿 06 §3）：Project 是知识库与 Artifact 的归属单位，一个项目对应多个（主机，目录）。
// 项目只能由用户（Web）创建与确认；Agent 没有创建入口。

// ACL 档位：off（现状，不记录）→ audit（只记录「若启用会被拒绝」的调用，不拦）→ enforce（真正拦截，只由用户打开）。
const (
	ACLOff     = "off"
	ACLAudit   = "audit"
	ACLEnforce = "enforce"
)

// NormalizeACLMode 把输入规整成合法档位；不认识的返回空串。
func NormalizeACLMode(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case ACLOff:
		return ACLOff
	case ACLAudit:
		return ACLAudit
	case ACLEnforce:
		return ACLEnforce
	}
	return ""
}

// 文档渲染类型。
const (
	DocRenderMarkdown = "md"
	DocRenderMermaid  = "mermaid"
	DocRenderHTML     = "html"
	DocRenderData     = "data"
)

// NormalizeDocRender 规整渲染类型；空串等价于 md，不认识的返回空串。
func NormalizeDocRender(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", DocRenderMarkdown:
		return DocRenderMarkdown
	case DocRenderMermaid:
		return DocRenderMermaid
	case DocRenderHTML:
		return DocRenderHTML
	case DocRenderData:
		return DocRenderData
	}
	return ""
}

// Project 是项目知识库的根。
type Project struct {
	ID          uint       `gorm:"primaryKey;autoIncrement" json:"id"`
	Slug        string     `gorm:"size:64;not null;uniqueIndex:ux_project_slug" json:"slug"`
	Name        string     `gorm:"size:128;not null" json:"name"`
	Description string     `gorm:"size:1000" json:"description,omitempty"`
	ACLMode     string     `gorm:"size:8;not null;default:'audit'" json:"acl_mode"`
	CreatedBy   string     `gorm:"size:16;not null;default:'human'" json:"created_by"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
	ArchivedAt  *time.Time `gorm:"index" json:"archived_at,omitempty"`
}

// ProjectDir 把一个（主机，目录）归入项目：一个目录只属于一个项目。
type ProjectDir struct {
	ID               uint      `gorm:"primaryKey;autoIncrement" json:"id"`
	ProjectID        uint      `gorm:"not null;index" json:"project_id"`
	HostName         string    `gorm:"size:128;not null;uniqueIndex:ux_project_dir,priority:1" json:"host_name"`
	ProjectDirectory string    `gorm:"size:512;not null;uniqueIndex:ux_project_dir,priority:2" json:"project_directory"`
	BranchHint       string    `gorm:"size:128" json:"branch_hint,omitempty"`
	AddedBy          string    `gorm:"size:16;not null;default:'human'" json:"added_by"`
	AddedAt          time.Time `json:"added_at"`
}

// ProjectModule 是项目的功能模块（路径是封闭列表）；设计信息绑定到模块路径。
type ProjectModule struct {
	ID              uint      `gorm:"primaryKey;autoIncrement" json:"id"`
	ProjectID       uint      `gorm:"not null;uniqueIndex:ux_project_module,priority:1" json:"project_id"`
	Path            string    `gorm:"size:256;not null;uniqueIndex:ux_project_module,priority:2" json:"path"`
	Title           string    `gorm:"size:128;not null" json:"title"`
	Description     string    `gorm:"size:1000" json:"description,omitempty"`
	OwnerIdentityID uint      `gorm:"not null;default:0" json:"owner_identity_id,omitempty"`
	CreatedAt       time.Time `json:"created_at"`
}
