package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/teacat99/RelayMesh/v2/internal/acl"
	"github.com/teacat99/RelayMesh/v2/internal/docs"
	"github.com/teacat99/RelayMesh/v2/internal/model"
	"github.com/teacat99/RelayMesh/v2/internal/store"
)

// 工作流范围闸（#114，设计稿 06 §7.7）。
//
// 在 MCP 分发层判定「调用方身份」能不能碰目标工作流的数据；档位按目标工作流所属项目的 acl_mode：
//   - off（含：工作流不属于任何项目）：完全不判定，和以前一样；
//   - audit：只记录「若启用 enforce 会被拒绝」的调用，一律放行；
//   - enforce：真正拦截（只有用户能切到这一档，智能体不能）。
//
// 身份是调用方自报的（project_directory + host_name），不是安全边界；这套机制防误操作、越权写与「拿着别人的 ID 就改」。

// aclGatedTools 是按工作流访问数据的工具：接入闸的只有这几个（交互、终端等不属于「数据」）。
var aclGatedTools = map[string]bool{"project_docs": true, "task_board": true, "workflow_context": true}

// aclCaller 是闸判定出的调用方，放进 ctx 里给工具处理函数用（文档负责人、文档级判定、审计标注）。
type aclCaller struct {
	IdentityID uint
	Asserted   bool
	Label      string
	// Project 是目标工作流所属项目；nil = 不属于任何项目。
	Project *model.Project
}

type aclCtxKey struct{}

func aclCallerFrom(ctx context.Context) *aclCaller {
	c, _ := ctx.Value(aclCtxKey{}).(*aclCaller)
	return c
}

type aclMeta struct {
	WorkflowID       string `json:"workflow_id"`
	Action           string `json:"action"`
	Scope            string `json:"scope"`
	ProjectDirectory string `json:"project_directory"`
	HostName         string `json:"host_name"`
}

// 动作分类：读 = 只看；其余（含收件箱：读取会标成已送达）都算写。
func aclIsWrite(tool, action string) bool {
	a := strings.ToLower(strings.TrimSpace(action))
	switch a {
	case "inbox", "inbox_peek", "inbox_ack":
		return true
	case "":
		// 缺省动作：project_docs / task_board 是 overview（读）；workflow_context 没有缺省读。
		return tool == "workflow_context"
	case "grants", "module", "blueprint", "import_sheet_dry", "comments", "links":
		return false
	}
	return !isReadAction(a)
}

func actorOf(c *aclCaller, wf string) (kind, ref string) {
	if c != nil && c.IdentityID != 0 {
		return "identity", "identity:" + strconv.FormatUint(uint64(c.IdentityID), 10)
	}
	return "identity", "workflow:" + wf
}

func (s *Server) aclAudit(ctx context.Context, p *model.Project, c *aclCaller, wf, action, result, scopeKind, scopeRef, target string, dedupe time.Duration, detail any) {
	s.aclAuditIn(ctx, p, c, wf, action, result, scopeKind, scopeRef, target, dedupe, false, detail)
}

// aclAuditIn 是 aclAudit 的完整形式：exact 为 true 时去重要求目标与详情也相同（见 store.AuditInput.DedupeExact）。
func (s *Server) aclAuditIn(ctx context.Context, p *model.Project, c *aclCaller, wf, action, result, scopeKind, scopeRef, target string, dedupe time.Duration, exact bool, detail any) {
	kind, ref := actorOf(c, wf)
	label := ""
	if c != nil {
		label = c.Label
	}
	if label == "" {
		label = wf
	}
	var pid uint
	if p != nil {
		pid = p.ID
	}
	_, _ = s.store.AddAuditEvent(ctx, store.AuditInput{
		ProjectID: pid, ActorKind: kind, ActorRef: ref, ActorLabel: label,
		Action: action, ScopeKind: scopeKind, ScopeRef: scopeRef, Target: target, Result: result, Dedupe: dedupe, DedupeExact: exact, Detail: detail,
	})
}

// aclGate 在工具分发前判定；返回带调用方信息的 ctx。enforce 档下被拒绝时返回错误。
func (s *Server) aclGate(ctx context.Context, credCtx *CredentialContext, tool string, raw json.RawMessage) (context.Context, error) {
	if !aclGatedTools[tool] {
		return ctx, nil
	}
	var m aclMeta
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &m)
	}
	wf := strings.TrimSpace(m.WorkflowID)
	if wf == "" {
		return ctx, nil
	}
	var cred uint
	host := strings.TrimSpace(m.HostName)
	if credCtx != nil {
		cred = credCtx.CredentialID
		if host == "" {
			host = credCtx.HostName
		}
	}
	id, asserted := s.store.ResolveCallerIdentity(ctx, wf, cred, host, m.ProjectDirectory, time.Now())
	caller := &aclCaller{IdentityID: id, Asserted: asserted}
	ctx = context.WithValue(ctx, aclCtxKey{}, caller)

	proj, _ := s.store.ProjectOfWorkflow(ctx, wf)
	if proj == nil || (proj.ACLMode != acl.ModeAudit && proj.ACLMode != acl.ModeEnforce) {
		return ctx, nil
	}
	caller.Project = proj
	caller.Label = s.store.IdentityLabelOf(ctx, id)

	// 项目范围的文档调用，目标是整个项目知识库而不是某个工作流：由文档级判定负责。
	if tool == "project_docs" && (m.Scope == "project" || m.Action == "module" || isGrantAction(m.Action)) {
		return ctx, nil
	}

	write := aclIsWrite(tool, m.Action)
	kind := acl.KindSession
	switch {
	case tool == "project_docs":
		kind = acl.KindDoc
	case isInboxAction(tool, raw):
		kind = acl.KindInbox
	}
	targetID := s.store.WorkflowIdentityID(ctx, wf)
	if id != 0 && id == targetID {
		return ctx, nil // R3：自己的工作流
	}
	enforce := proj.ACLMode == acl.ModeEnforce
	if !asserted {
		if write || kind == acl.KindInbox {
			return ctx, s.aclUnasserted(ctx, proj, caller, wf, tool, m.Action)
		}
		return ctx, nil
	}

	need := acl.RoleRead
	if write {
		need = acl.RoleEdit
	}
	grants, _ := s.store.ActiveGrantsForCaller(ctx, proj.ID, id, wf)
	d := acl.Authorize(acl.Request{
		Mode:   proj.ACLMode,
		Caller: acl.Caller{IdentityID: id, ProjectID: s.store.IdentityProjectID(ctx, id)},
		Res:    acl.Resource{Kind: kind, ProjectID: proj.ID, WorkflowID: wf, WorkflowIdentityID: targetID},
		Need:   need, Grants: grants,
	})
	target := tool + ":" + strings.TrimSpace(m.Action)
	if d.Allow {
		switch {
		case !write:
			s.aclAudit(ctx, proj, caller, wf, model.AuditDocReadCross, model.AuditAllow, "workflow", wf, target, store.AuditCrossReadDedupe, map[string]any{"rule": d.Rule})
		case tool != "project_docs": // 文档写入由文档处理函数记 doc.write
			s.aclAudit(ctx, proj, caller, wf, model.AuditCrossWrite, model.AuditAllow, "workflow", wf, target, 0, map[string]any{"rule": d.Rule})
		}
		return ctx, nil
	}
	s.aclAudit(ctx, proj, caller, wf, model.AuditACLDenied, model.AuditDeny, "workflow", wf, target, store.AuditCrossReadDedupe,
		map[string]any{"mode": proj.ACLMode, "rule": d.Rule, "have": d.Have.String(), "need": need.String(), "reason": d.Reason, "would_deny": !enforce})
	if enforce {
		return ctx, fmt.Errorf("acl_denied：%s", d.Reason)
	}
	return ctx, nil
}

func isGrantAction(a string) bool { return a == "grant" || a == "revoke" || a == "grants" }

// aclUnasserted 处理「没有身份断言的写入」：audit 档记一条 assumed（30 分钟内去重），enforce 档要求补上身份。
func (s *Server) aclUnasserted(ctx context.Context, p *model.Project, c *aclCaller, wf, tool, action string) error {
	target := tool + ":" + strings.TrimSpace(action)
	s.aclAudit(ctx, p, c, wf, model.AuditACLDenied, model.AuditAssumed, "workflow", wf, target, store.AuditCrossReadDedupe,
		map[string]any{"mode": p.ACLMode, "reason": "identity_required", "would_deny": p.ACLMode != acl.ModeEnforce})
	if p.ACLMode == acl.ModeEnforce {
		return fmt.Errorf("identity_required：项目「%s」已启用权限校验（enforce），写入需要声明调用方身份：请在调用里带上 project_directory 与 host_name", p.Name)
	}
	return nil
}

// ---- 文档级判定（项目范围的读写） ----

// aclDocAccess 判定调用方对一篇文档（doc 为空表示要新建 path）的访问；返回的错误只在 enforce 档出现。
func (s *Server) aclDocAccess(ctx context.Context, p *model.Project, a projectDocsArgs, doc *model.ProjectDoc, path string, need acl.Role) error {
	if p == nil || (p.ACLMode != acl.ModeAudit && p.ACLMode != acl.ModeEnforce) {
		return nil
	}
	caller := aclCallerFrom(ctx)
	if caller == nil {
		caller = &aclCaller{}
	}
	if caller.Label == "" && caller.IdentityID != 0 {
		caller.Label = s.store.IdentityLabelOf(ctx, caller.IdentityID)
	}
	write := need > acl.RoleRead
	if normalized, err := docs.NormalizePath(path); err == nil {
		path = normalized
	}
	own := doc != nil && doc.WorkflowID == a.WorkflowID
	if !write && own {
		return nil
	}
	if !caller.Asserted {
		if write {
			return s.aclUnasserted(ctx, p, caller, a.WorkflowID, "project_docs", "save")
		}
		// 读别的工作流的文档：没有身份断言，只能记 assumed。
		s.aclAudit(ctx, p, caller, a.WorkflowID, model.AuditDocReadCross, model.AuditAssumed, "doc", path, path, store.AuditCrossReadDedupe, nil)
		return nil
	}

	res := acl.Resource{Kind: acl.KindDoc, ProjectID: p.ID, DocPath: path, WorkflowID: a.WorkflowID}
	if doc != nil {
		res.DocID, res.ModulePath, res.OwnerIdentityID, res.WorkflowID = doc.ID, doc.ModulePath, doc.OwnerIdentityID, doc.WorkflowID
		res.WorkflowIdentityID = s.store.WorkflowIdentityID(ctx, doc.WorkflowID)
		res.ModuleOwnerID = s.store.ModuleOwnerFor(ctx, p.ID, doc.ModulePath)
	} else {
		res.ModulePath = s.store.ResolveDocModule(ctx, p.ID, path, firstNonEmpty(a.ModulePath, a.Module))
		res.ModuleOwnerID = s.store.ModuleOwnerFor(ctx, p.ID, res.ModulePath)
		if rest, ok := strings.CutPrefix(path, "sessions/"); ok {
			// sessions/<工作流>/… 属于那个工作流自己。
			if wf, _, ok := strings.Cut(rest, "/"); ok && wf != "" {
				res.WorkflowID, res.WorkflowIdentityID = wf, s.store.WorkflowIdentityID(ctx, wf)
			}
		} else if res.ModuleOwnerID == 0 || res.ModuleOwnerID == caller.IdentityID {
			// 没有负责人的模块（或自己负责的）：创建者成为这篇文档的负责人。
			res.OwnerIdentityID = caller.IdentityID
			res.WorkflowIdentityID = s.store.WorkflowIdentityID(ctx, a.WorkflowID)
		}
		// 模块有别的负责人：不给创建者任何隐含权限，要么是负责人，要么被授权。
	}
	grants, _ := s.store.ActiveGrantsForCaller(ctx, p.ID, caller.IdentityID, a.WorkflowID)
	d := acl.Authorize(acl.Request{
		Mode:   p.ACLMode,
		Caller: acl.Caller{IdentityID: caller.IdentityID, ProjectID: s.store.IdentityProjectID(ctx, caller.IdentityID)},
		Res:    res, Need: need, Grants: grants,
	})
	cross := doc != nil && doc.WorkflowID != a.WorkflowID
	if d.Allow {
		if !write && cross {
			s.aclAudit(ctx, p, caller, a.WorkflowID, model.AuditDocReadCross, model.AuditAllow, "doc", path, path, store.AuditCrossReadDedupe, map[string]any{"rule": d.Rule})
		}
		return nil
	}
	enforce := p.ACLMode == acl.ModeEnforce
	s.aclAudit(ctx, p, caller, a.WorkflowID, model.AuditACLDenied, model.AuditDeny, "doc", path, path, store.AuditCrossReadDedupe,
		map[string]any{"mode": p.ACLMode, "rule": d.Rule, "have": d.Have.String(), "need": need.String(), "reason": d.Reason, "would_deny": !enforce})
	if enforce {
		return fmt.Errorf("acl_denied：%s", d.Reason)
	}
	return nil
}

// auditDocWrite 在文档写入成功之后记一条 doc.write / doc.delete（不含正文）。
func (s *Server) auditDocWrite(ctx context.Context, a projectDocsArgs, action, target string, detail map[string]any) {
	caller := aclCallerFrom(ctx)
	if caller == nil || caller.Project == nil {
		return
	}
	result := model.AuditAllow
	if !caller.Asserted {
		result = model.AuditAssumed
	}
	act := model.AuditDocWrite
	if action == "delete" {
		act = model.AuditDocDelete
	}
	if detail == nil {
		detail = map[string]any{}
	}
	detail["workflow_id"] = a.WorkflowID
	s.aclAudit(ctx, caller.Project, caller, a.WorkflowID, act, result, "doc", target, target, 0, detail)
}
