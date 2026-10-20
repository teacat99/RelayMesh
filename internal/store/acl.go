package store

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/teacat99/RelayMesh/v2/internal/acl"
	"github.com/teacat99/RelayMesh/v2/internal/model"
	"gorm.io/gorm"
)

// 授权的存储与判定所需的事实查询（#114，设计稿 06 §7.3–§7.7）。判定本身在 internal/acl（纯函数）。

const grantsPerProjectMax = 500

// GrantInput 是一条新授权。
type GrantInput struct {
	ProjectID      uint
	PrincipalKind  string // identity | group
	PrincipalRef   string // identity：身份编号；group：会话编号
	ScopeKind      string // project | module | doc | workflow | tasks（项目任务板的管理权，只能由用户授予）
	ScopeRef       string
	Role           string // read | comment | edit（manage 不能授予）
	GrantedByKind  string // human | identity
	GrantedByRef   string
	GrantedByLabel string
	ExpiresAt      *time.Time
	Note           string
}

// CreateGrant 新增（或续期）一条授权：同一主体 + 范围已有有效授权时更新角色与有效期，不重复建。
func (s *Store) CreateGrant(ctx context.Context, in GrantInput) (*model.ACLGrant, error) {
	if _, err := s.GetProject(ctx, in.ProjectID); err != nil {
		return nil, err
	}
	switch in.Role {
	case model.RoleRead, model.RoleComment, model.RoleEdit:
	case model.RoleManage:
		return nil, NewInvalidInputError("管理权限不能转授；只有用户和负责范围的负责人本身拥有 manage")
	default:
		return nil, NewInvalidInputError("角色只能是 read / comment / edit")
	}
	in.PrincipalRef = strings.TrimSpace(in.PrincipalRef)
	if in.ScopeKind == model.ScopeTasks {
		// 任务管理权（#175 B2c）：给单个 Agent 身份、只有用户能授予，角色只能是 edit。
		if in.Role != model.RoleEdit {
			return nil, NewInvalidInputError("任务管理授权的角色只能是 edit（项目管理 Agent 能派任务、改所有人的任务）")
		}
		if in.PrincipalKind != model.PrincipalIdentity {
			return nil, NewInvalidInputError("任务管理只能授予某个 Agent 身份，不能授予群")
		}
		if in.GrantedByKind != "" && in.GrantedByKind != "human" {
			return nil, NewInvalidInputError("任务管理只能由用户授予：Agent 不能自授，也不能转授")
		}
	}
	switch in.PrincipalKind {
	case model.PrincipalIdentity:
		id, err := strconv.ParseUint(in.PrincipalRef, 10, 32)
		if err != nil || id == 0 {
			return nil, NewInvalidInputError("身份编号无效")
		}
		var n int64
		if err := s.db.WithContext(ctx).Model(&model.AgentIdentity{}).Where("id = ?", id).Count(&n).Error; err != nil {
			return nil, err
		}
		if n == 0 {
			return nil, NewNotFoundError("没有这个身份")
		}
		in.PrincipalRef = strconv.FormatUint(id, 10)
	case model.PrincipalGroup:
		if in.PrincipalRef == "" {
			return nil, NewInvalidInputError("群编号不能为空")
		}
		if _, err := s.GetGroup(ctx, in.PrincipalRef); err != nil {
			return nil, err
		}
	default:
		return nil, NewInvalidInputError("主体只能是 identity 或 group")
	}
	ref, err := s.normalizeGrantScope(ctx, in.ProjectID, in.ScopeKind, in.ScopeRef)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	if in.ExpiresAt != nil && !in.ExpiresAt.After(now) {
		return nil, NewInvalidInputError("有效期必须晚于现在")
	}
	if in.GrantedByKind == "" {
		in.GrantedByKind = "human"
	}

	var out model.ACLGrant
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		active := tx.Where("project_id = ? AND revoked_at IS NULL AND (expires_at IS NULL OR expires_at > ?)", in.ProjectID, now)
		var existing model.ACLGrant
		e := active.Where("principal_kind = ? AND principal_ref = ? AND scope_kind = ? AND scope_ref = ?",
			in.PrincipalKind, in.PrincipalRef, in.ScopeKind, ref).Take(&existing).Error
		switch {
		case e == nil:
			existing.Role, existing.ExpiresAt, existing.Note = in.Role, in.ExpiresAt, truncRunes(in.Note, 500)
			existing.GrantedByKind, existing.GrantedByRef, existing.GrantedByLabel = in.GrantedByKind, in.GrantedByRef, truncRunes(in.GrantedByLabel, 200)
			out = existing
			return tx.Save(&existing).Error
		case !errors.Is(e, gorm.ErrRecordNotFound):
			return e
		}
		var n int64
		if err := tx.Model(&model.ACLGrant{}).Where("project_id = ? AND revoked_at IS NULL", in.ProjectID).Count(&n).Error; err != nil {
			return err
		}
		if n >= grantsPerProjectMax {
			return NewConflictError("这个项目的有效授权已达上限（500），请先撤销不用的", 0)
		}
		out = model.ACLGrant{
			ProjectID: in.ProjectID, PrincipalKind: in.PrincipalKind, PrincipalRef: in.PrincipalRef,
			ScopeKind: in.ScopeKind, ScopeRef: ref, Role: in.Role,
			GrantedByKind: in.GrantedByKind, GrantedByRef: in.GrantedByRef, GrantedByLabel: truncRunes(in.GrantedByLabel, 200),
			ExpiresAt: in.ExpiresAt, CreatedAt: now, Note: truncRunes(in.Note, 500),
		}
		return tx.Create(&out).Error
	})
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// normalizeGrantScope 校验授权范围存在，并把引用规整（项目范围固定成项目编号；模块路径规整）。
func (s *Store) normalizeGrantScope(ctx context.Context, projectID uint, kind, ref string) (string, error) {
	ref = strings.TrimSpace(ref)
	switch kind {
	case model.ScopeProject:
		return strconv.FormatUint(uint64(projectID), 10), nil
	case model.ScopeModule:
		p, err := NormalizeModulePath(ref)
		if err != nil {
			return "", err
		}
		var n int64
		if err := s.db.WithContext(ctx).Model(&model.ProjectModule{}).Where("project_id = ? AND path = ?", projectID, p).Count(&n).Error; err != nil {
			return "", err
		}
		if n == 0 {
			return "", NewNotFoundError("项目里没有这个模块：" + p)
		}
		return p, nil
	case model.ScopeDoc:
		doc, err := s.GetProjectDocInProject(ctx, projectID, ref)
		if err != nil {
			return "", err
		}
		return doc.Path, nil
	case model.ScopeWorkflow:
		if ref == "" {
			return "", NewInvalidInputError("工作流编号不能为空")
		}
		return ref, nil
	case model.ScopeTasks:
		// 和项目范围一样固定成项目编号：任务管理权是「这个项目的任务板」，没有更细的范围。
		return strconv.FormatUint(uint64(projectID), 10), nil
	}
	return "", NewInvalidInputError("授权范围只能是 project / module / doc / workflow / tasks")
}

// RevokeGrant 撤销一条授权（幂等：已撤销的再撤销不报错）。
func (s *Store) RevokeGrant(ctx context.Context, projectID, grantID uint) (*model.ACLGrant, error) {
	var g model.ACLGrant
	if err := s.db.WithContext(ctx).Where("id = ? AND project_id = ?", grantID, projectID).Take(&g).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, NewNotFoundError("没有这条授权")
		}
		return nil, err
	}
	if g.RevokedAt == nil {
		now := time.Now()
		g.RevokedAt = &now
		if err := s.db.WithContext(ctx).Model(&g).Update("revoked_at", now).Error; err != nil {
			return nil, err
		}
	}
	return &g, nil
}

// GetGrant 取一条授权（限定项目）。
func (s *Store) GetGrant(ctx context.Context, projectID, grantID uint) (*model.ACLGrant, error) {
	var g model.ACLGrant
	if err := s.db.WithContext(ctx).Where("id = ? AND project_id = ?", grantID, projectID).Take(&g).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, NewNotFoundError("没有这条授权")
		}
		return nil, err
	}
	return &g, nil
}

// ListGrants 列出项目的授权，新的在前；默认只给有效的（没撤销、没过期）。
func (s *Store) ListGrants(ctx context.Context, projectID uint, includeInactive bool) ([]model.ACLGrant, error) {
	q := s.db.WithContext(ctx).Where("project_id = ?", projectID)
	if !includeInactive {
		q = q.Where("revoked_at IS NULL AND (expires_at IS NULL OR expires_at > ?)", time.Now())
	}
	var out []model.ACLGrant
	if err := q.Order("id DESC").Limit(grantsPerProjectMax * 2).Find(&out).Error; err != nil {
		return nil, err
	}
	return out, nil
}

// ActiveGrantsFor 给判定函数用：这个身份（以及它所在的群）在项目里有效的授权。
func (s *Store) ActiveGrantsFor(ctx context.Context, projectID, identityID uint, groupRefs []string) ([]acl.Grant, error) {
	if identityID == 0 && len(groupRefs) == 0 {
		return nil, nil
	}
	var rows []model.ACLGrant
	q := s.db.WithContext(ctx).Where("project_id = ? AND revoked_at IS NULL AND (expires_at IS NULL OR expires_at > ?)", projectID, time.Now())
	cond := "(1 = 0)"
	args := []any{}
	if identityID != 0 {
		cond = "(principal_kind = ? AND principal_ref = ?)"
		args = append(args, model.PrincipalIdentity, strconv.FormatUint(uint64(identityID), 10))
	}
	if len(groupRefs) > 0 {
		cond += " OR (principal_kind = ? AND principal_ref IN ?)"
		args = append(args, model.PrincipalGroup, groupRefs)
	}
	if err := q.Where(cond, args...).Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]acl.Grant, 0, len(rows))
	for _, g := range rows {
		out = append(out, acl.Grant{ProjectID: g.ProjectID, ScopeKind: g.ScopeKind, ScopeRef: g.ScopeRef, Role: acl.ParseRole(g.Role)})
	}
	return out, nil
}

// GroupRefsOfIdentity 是这个身份当前所在的群（群授权的主体）：群成员单位是工作流，
// 所以取「本次调用的工作流」和「这个身份名下所有工作流」里仍在群内的那些。
func (s *Store) GroupRefsOfIdentity(ctx context.Context, workflowID string, identityID uint) []string {
	wfs := map[string]bool{}
	if wf := strings.TrimSpace(workflowID); wf != "" {
		wfs[wf] = true
	}
	if identityID != 0 {
		var ids []string
		_ = s.db.WithContext(ctx).Model(&model.AgentSession{}).Where("identity_id = ? AND workflow_id <> ''", identityID).
			Distinct().Limit(500).Pluck("workflow_id", &ids).Error
		for _, id := range ids {
			wfs[id] = true
		}
	}
	if len(wfs) == 0 {
		return nil
	}
	list := make([]string, 0, len(wfs))
	for id := range wfs {
		list = append(list, id)
	}
	var groups []string
	if err := s.db.WithContext(ctx).Model(&model.ConversationMember{}).
		Where("member_kind = ? AND member_ref IN ? AND left_at IS NULL", model.MemberKindWorkflow, list).
		Distinct().Pluck("conversation_id", &groups).Error; err != nil {
		return nil
	}
	return groups
}

// ActiveGrantsForCaller 在 ActiveGrantsFor 的基础上补上调用方所在的群（群共享授权）。
func (s *Store) ActiveGrantsForCaller(ctx context.Context, projectID, identityID uint, workflowID string) ([]acl.Grant, error) {
	return s.ActiveGrantsFor(ctx, projectID, identityID, s.GroupRefsOfIdentity(ctx, workflowID, identityID))
}

// ResolveCallerIdentity 回答「这次调用是谁」：带了 project_directory 的是明确断言（asserted=true）；
// 没带就按工作流绑定的目录推断（asserted=false，审计里标 assumed）。认不出目录返回 0。
func (s *Store) ResolveCallerIdentity(ctx context.Context, workflowID string, credentialID uint, host, dir string, now time.Time) (id uint, asserted bool) {
	wf := strings.TrimSpace(workflowID)
	if wf == "" {
		return 0, false
	}
	asserted = normalizeProjectDir(dir) != ""
	m := &s.agent
	m.mu.Lock()
	defer m.mu.Unlock()
	m.init()
	key, ok := s.agentIdentityKeyLocked(ctx, wf, credentialID, host, dir, now)
	if !ok {
		return 0, asserted
	}
	e, err := s.touchIdentityLocked(ctx, key, agentTouch{}, now)
	if err != nil || e == nil {
		return 0, asserted
	}
	return e.id, asserted
}

// WorkflowIdentityID 是工作流绑定的身份：先看对话会话（agent_sessions），再按最近一次汇报会话的目录找身份。认不出返回 0。
func (s *Store) WorkflowIdentityID(ctx context.Context, workflowID string) uint {
	wf := strings.TrimSpace(workflowID)
	if wf == "" {
		return 0
	}
	var sess model.AgentSession
	if err := s.db.WithContext(ctx).Select("identity_id").Where("workflow_id = ? AND identity_id <> 0", wf).
		Order("last_event_at DESC").Limit(1).Take(&sess).Error; err == nil && sess.IdentityID != 0 {
		return sess.IdentityID
	}
	host, dir := s.workflowDir(ctx, wf)
	if dir == "" {
		return 0
	}
	var row model.AgentIdentity
	q := s.db.WithContext(ctx).Select("id").Where("project_directory = ?", dir)
	if host != "" {
		q = q.Where("host_name = ?", host)
	}
	if err := q.Order("last_seen_at DESC").Limit(1).Take(&row).Error; err != nil {
		return 0
	}
	return row.ID
}

// IdentityProjectID 是身份所在目录归属的项目；没有归属返回 0。
func (s *Store) IdentityProjectID(ctx context.Context, identityID uint) uint {
	if identityID == 0 {
		return 0
	}
	var row model.AgentIdentity
	if err := s.db.WithContext(ctx).Select("host_name", "project_directory").Where("id = ?", identityID).Take(&row).Error; err != nil {
		return 0
	}
	p, err := s.ProjectOfDir(ctx, row.HostName, row.ProjectDirectory)
	if err != nil || p == nil {
		return 0
	}
	return p.ID
}

// IdentityLabelOf 给审计用的身份标签：用户改的名字，没有就用目录名。
func (s *Store) IdentityLabelOf(ctx context.Context, identityID uint) string {
	if identityID == 0 {
		return ""
	}
	var row model.AgentIdentity
	if err := s.db.WithContext(ctx).Select("name", "project_directory").Where("id = ?", identityID).Take(&row).Error; err != nil {
		return "身份#" + strconv.FormatUint(uint64(identityID), 10)
	}
	if n := strings.TrimSpace(row.Name); n != "" {
		return n
	}
	if b := dirBase(row.ProjectDirectory); b != "" {
		return b
	}
	return "身份#" + strconv.FormatUint(uint64(identityID), 10)
}
