package mcp

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/teacat99/RelayMesh/v2/internal/acl"
	"github.com/teacat99/RelayMesh/v2/internal/model"
	"github.com/teacat99/RelayMesh/v2/internal/store"
)

// 智能体对「自己负责的范围」做授权 / 撤销（设计稿 06 §7.4）。
// 管理权限（manage）永远不能转授；写入权只来自自己的负责范围、用户的授权或负责人 Agent 的授权。

func (s *Server) grantCaller(ctx context.Context, p *model.Project) (*aclCaller, error) {
	c := aclCallerFrom(ctx)
	if c == nil || c.IdentityID == 0 || !c.Asserted {
		return nil, fmt.Errorf("identity_required：授权操作需要声明调用方身份：请在调用里带上 project_directory 与 host_name")
	}
	if c.Label == "" {
		c.Label = s.store.IdentityLabelOf(ctx, c.IdentityID)
	}
	return c, nil
}

// grantScopeResource 把授权范围还原成判定用的资源，并给出规整后的引用。
func (s *Server) grantScopeResource(ctx context.Context, p *model.Project, kind, ref string) (acl.Resource, string, error) {
	switch kind {
	case model.ScopeModule:
		path, err := store.NormalizeModulePath(ref)
		if err != nil {
			return acl.Resource{}, "", err
		}
		m, err := s.store.GetProjectModule(ctx, p.ID, path)
		if err != nil {
			return acl.Resource{}, "", err
		}
		if m == nil {
			return acl.Resource{}, "", fmt.Errorf("项目里没有这个模块：%s", path)
		}
		return acl.Resource{Kind: acl.KindModule, ProjectID: p.ID, ModulePath: path, ModuleOwnerID: s.store.ModuleOwnerFor(ctx, p.ID, path)}, path, nil
	case model.ScopeDoc:
		doc, err := s.store.GetProjectDocInProject(ctx, p.ID, ref)
		if err != nil {
			return acl.Resource{}, "", err
		}
		return acl.Resource{
			Kind: acl.KindDoc, ProjectID: p.ID, ModulePath: doc.ModulePath, DocPath: doc.Path, DocID: doc.ID, WorkflowID: doc.WorkflowID,
			OwnerIdentityID: doc.OwnerIdentityID, ModuleOwnerID: s.store.ModuleOwnerFor(ctx, p.ID, doc.ModulePath),
			WorkflowIdentityID: s.store.WorkflowIdentityID(ctx, doc.WorkflowID),
		}, doc.Path, nil
	case model.ScopeWorkflow:
		ref = strings.TrimSpace(ref)
		if ref == "" {
			return acl.Resource{}, "", fmt.Errorf("grant_ref 需要工作流编号")
		}
		return acl.Resource{Kind: acl.KindSession, ProjectID: p.ID, WorkflowID: ref, WorkflowIdentityID: s.store.WorkflowIdentityID(ctx, ref)}, ref, nil
	}
	return acl.Resource{}, "", fmt.Errorf("grant_scope 只能是 module / doc / workflow（项目级授权只有用户能做）")
}

// canManage 判定调用方对范围是否有 manage（一律按 enforce 判定：授权是新能力，不能因为项目在 audit 档就放宽）。
func (s *Server) canManage(ctx context.Context, p *model.Project, c *aclCaller, res acl.Resource) acl.Decision {
	return acl.Authorize(acl.Request{
		Mode:   acl.ModeEnforce,
		Caller: acl.Caller{IdentityID: c.IdentityID, ProjectID: s.store.IdentityProjectID(ctx, c.IdentityID)},
		Res:    res, Need: acl.RoleManage,
	})
}

func identityRef(id uint) string { return "identity:" + strconv.FormatUint(uint64(id), 10) }

func (s *Server) projectDocsGrants(ctx context.Context, p *model.Project, a projectDocsArgs, action string) (any, error) {
	c, err := s.grantCaller(ctx, p)
	if err != nil {
		return nil, err
	}
	switch action {
	case "grants":
		all, err := s.store.ListGrants(ctx, p.ID, false)
		if err != nil {
			return nil, err
		}
		me := strconv.FormatUint(uint64(c.IdentityID), 10)
		out := []model.ACLGrant{}
		for _, g := range all {
			if (g.PrincipalKind == model.PrincipalIdentity && g.PrincipalRef == me) || g.GrantedByRef == identityRef(c.IdentityID) {
				out = append(out, g)
			}
		}
		return map[string]any{"project": projectRef(p), "grants": out, "count": len(out)}, nil

	case "grant":
		gid, err := strconv.ParseUint(strings.TrimSpace(a.Grantee), 10, 32)
		if err != nil || gid == 0 {
			return nil, fmt.Errorf("grant 需要 grantee（对方的身份编号，数字）")
		}
		if uint(gid) == c.IdentityID {
			return nil, fmt.Errorf("不需要给自己授权")
		}
		res, ref, err := s.grantScopeResource(ctx, p, a.GrantScope, a.GrantRef)
		if err != nil {
			return nil, err
		}
		if d := s.canManage(ctx, p, c, res); !d.Allow {
			s.aclAudit(ctx, p, c, a.WorkflowID, model.AuditACLDenied, model.AuditDeny, a.GrantScope, ref, "grant",
				store.AuditCrossReadDedupe, map[string]any{"reason": "只能对自己负责的范围授权", "have": d.Have.String()})
			return nil, fmt.Errorf("acl_denied：你对这个范围没有管理权限，只能对自己负责的模块 / 文档 / 工作流授权（需要的话请用户授权或让负责人授权）")
		}
		in := store.GrantInput{
			ProjectID: p.ID, PrincipalKind: model.PrincipalIdentity, PrincipalRef: strconv.FormatUint(gid, 10),
			ScopeKind: a.GrantScope, ScopeRef: ref, Role: strings.ToLower(strings.TrimSpace(a.Role)),
			GrantedByKind: "identity", GrantedByRef: identityRef(c.IdentityID), GrantedByLabel: c.Label, Note: a.Summary,
		}
		if a.ExpiresHours > 0 {
			t := time.Now().Add(time.Duration(a.ExpiresHours) * time.Hour)
			in.ExpiresAt = &t
		}
		g, err := s.store.CreateGrant(ctx, in)
		if err != nil {
			return nil, err
		}
		s.aclAudit(ctx, p, c, a.WorkflowID, model.AuditGrant, model.AuditAllow, g.ScopeKind, g.ScopeRef, "→identity:"+g.PrincipalRef, 0,
			map[string]any{"grant_id": g.ID, "role": g.Role, "expires_at": g.ExpiresAt})
		s.notifyGrantee(ctx, p, uint(gid), c.Label, fmt.Sprintf("你现在对%s拥有 %s 权限", scopeLabel(g.ScopeKind, g.ScopeRef), g.Role), g.ExpiresAt)
		return map[string]any{"grant": g, "hint": "已授权；对方的收件箱里会有一条提醒。撤销用 revoke（id=授权编号）。"}, nil

	case "revoke":
		gid, err := strconv.ParseUint(strings.TrimSpace(a.ID), 10, 32)
		if err != nil || gid == 0 {
			return nil, fmt.Errorf("revoke 需要 id（授权编号，见 grants）")
		}
		g, err := s.store.GetGrant(ctx, p.ID, uint(gid))
		if err != nil {
			return nil, err
		}
		allowed := g.GrantedByRef == identityRef(c.IdentityID)
		if !allowed {
			if res, _, e := s.grantScopeResource(ctx, p, g.ScopeKind, g.ScopeRef); e == nil {
				allowed = s.canManage(ctx, p, c, res).Allow
			}
		}
		if !allowed {
			return nil, fmt.Errorf("acl_denied：这条授权不是你授予的，也不在你负责的范围内，不能撤销")
		}
		if _, err := s.store.RevokeGrant(ctx, p.ID, uint(gid)); err != nil {
			return nil, err
		}
		s.aclAudit(ctx, p, c, a.WorkflowID, model.AuditRevoke, model.AuditAllow, g.ScopeKind, g.ScopeRef, "→"+g.PrincipalKind+":"+g.PrincipalRef, 0, map[string]any{"grant_id": g.ID})
		if g.PrincipalKind == model.PrincipalIdentity {
			if gi, e := strconv.ParseUint(g.PrincipalRef, 10, 32); e == nil {
				s.notifyGrantee(ctx, p, uint(gi), c.Label, fmt.Sprintf("你对%s的 %s 权限已被撤销", scopeLabel(g.ScopeKind, g.ScopeRef), g.Role), nil)
			}
		}
		return map[string]any{"revoked": true, "id": g.ID}, nil
	}
	return nil, fmt.Errorf("未知 action：%s", action)
}

func scopeLabel(kind, ref string) string {
	switch kind {
	case model.ScopeModule:
		return "模块「" + ref + "」"
	case model.ScopeDoc:
		return "文档「" + ref + "」"
	case model.ScopeWorkflow:
		return "工作流 " + ref
	}
	return "项目"
}

// notifyGrantee 给被授权方最近活跃的工作流的收件箱发一条提醒；找不到工作流就算了（授权本身已生效）。
func (s *Server) notifyGrantee(ctx context.Context, p *model.Project, identityID uint, granter, msg string, expires *time.Time) {
	wf := s.store.LatestWorkflowOfIdentity(ctx, identityID)
	if wf == "" {
		return
	}
	text := fmt.Sprintf("【授权通知】项目「%s」：%s（操作人：%s）", p.Name, msg, granter)
	if expires != nil {
		text += "，有效期至 " + expires.Format("2006-01-02 15:04")
	}
	if _, err := s.store.QueueWorkflowFeedback(ctx, store.QueueFeedbackInput{WorkflowID: wf, ResponseText: text}); err == nil && s.onUpdate != nil {
		s.notifyInbox(wf)
	}
}
