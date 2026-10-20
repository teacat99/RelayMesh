package model

import "time"

// 审计与授权（#114，设计稿 06 §7）。

// 审计结果：allow 放行；deny 被拒绝（audit 档里表示「若启用 enforce 会被拒绝」）；assumed 调用方没有身份断言，按工作流绑定推断。
const (
	AuditAllow   = "allow"
	AuditDeny    = "deny"
	AuditAssumed = "assumed"
)

// 审计动作（设计稿 06 §7.6）。审计从不记录文档正文与聊天正文。
const (
	AuditDocWrite         = "doc.write"
	AuditDocDelete        = "doc.delete"
	AuditDocReadCross     = "doc.read.cross"
	AuditCrossWrite       = "write.cross" // 非文档的跨工作流写（任务、会话数据等），被授权放行时记录
	AuditGrant            = "grant"
	AuditRevoke           = "revoke"
	AuditACLDenied        = "acl.denied"
	AuditArtifactDataSet  = "artifact.data_write"
	AuditGroupCreate      = "group.create"
	AuditGroupMemberAdd   = "group.member_add"
	AuditGroupMemberRm    = "group.member_remove"
	AuditGroupCooling     = "group.cooling"
	AuditWakeManual       = "wake.manual"
	AuditLauncherRun      = "launcher.run" // 项目管理 Agent 经 MCP 运行了预设（#179）：范围 tasks，allow = 提交成功，deny = 被拒绝
	AuditIdentityRename   = "identity.rename"
	AuditIdentityDisable  = "identity.disable"
	AuditProjectCreate    = "project.create"
	AuditProjectUpdate    = "project.update"
	AuditProjectDirAdd    = "project.dir_add"
	AuditProjectDirRemove = "project.dir_remove"
	AuditProjectMigrate   = "project.migrate"
	AuditModuleWrite      = "module.write"
)

// AuditEvent 是一条审计记录。ActorLabel 在写入时快照，改名之后历史不变。
type AuditEvent struct {
	ID         uint      `gorm:"primaryKey;autoIncrement" json:"id"`
	At         time.Time `gorm:"not null;index" json:"at"`
	ProjectID  uint      `gorm:"not null;default:0;index" json:"project_id,omitempty"`
	ActorKind  string    `gorm:"size:16;not null" json:"actor_kind"` // human | identity | system
	ActorRef   string    `gorm:"size:160;not null;index" json:"actor_ref"`
	ActorLabel string    `gorm:"size:200" json:"actor_label,omitempty"`
	Action     string    `gorm:"size:32;not null;index" json:"action"`
	ScopeKind  string    `gorm:"size:16" json:"scope_kind,omitempty"`
	ScopeRef   string    `gorm:"size:256" json:"scope_ref,omitempty"`
	Target     string    `gorm:"size:512" json:"target,omitempty"`
	Result     string    `gorm:"size:8;not null;default:'allow'" json:"result"`
	Detail     string    `gorm:"type:text" json:"detail,omitempty"` // JSON；不含正文
}

// 授权角色（逐级包含）与范围。
const (
	RoleRead    = "read"
	RoleComment = "comment"
	RoleEdit    = "edit"
	RoleManage  = "manage"

	ScopeProject  = "project"
	ScopeModule   = "module"
	ScopeDoc      = "doc"
	ScopeWorkflow = "workflow"
	// ScopeTasks 是项目任务板的管理权（#175 B2c）：被授予的 Agent 是「项目管理 Agent」，能派任务、改所有人的任务。
	// 范围引用 = 项目编号，角色只能是 edit，主体只能是单个 Agent 身份，只有用户能授予（Agent 不能自授、不能转授）。
	ScopeTasks = "tasks"

	PrincipalIdentity = "identity"
	PrincipalGroup    = "group"
)

// ACLGrant 是一条显式授权。「群共享」就是 principal_kind=group、role=read 的一条授权。
type ACLGrant struct {
	ID             uint       `gorm:"primaryKey;autoIncrement" json:"id"`
	ProjectID      uint       `gorm:"not null;default:0;index" json:"project_id,omitempty"`
	PrincipalKind  string     `gorm:"size:16;not null;index:idx_grant_principal,priority:1" json:"principal_kind"`
	PrincipalRef   string     `gorm:"size:160;not null;index:idx_grant_principal,priority:2" json:"principal_ref"`
	ScopeKind      string     `gorm:"size:16;not null;index:idx_grant_scope,priority:1" json:"scope_kind"`
	ScopeRef       string     `gorm:"size:256;not null;index:idx_grant_scope,priority:2" json:"scope_ref"`
	Role           string     `gorm:"size:8;not null" json:"role"`
	GrantedByKind  string     `gorm:"size:16;not null" json:"granted_by_kind"`
	GrantedByRef   string     `gorm:"size:160;not null" json:"granted_by_ref"`
	GrantedByLabel string     `gorm:"size:200" json:"granted_by_label,omitempty"`
	ExpiresAt      *time.Time `json:"expires_at,omitempty"`
	CreatedAt      time.Time  `json:"created_at"`
	RevokedAt      *time.Time `json:"revoked_at,omitempty"`
	Note           string     `gorm:"size:500" json:"note,omitempty"`
}
