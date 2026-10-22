package mcp

import (
	"context"
	"fmt"
	"strings"

	"github.com/teacat99/RelayMesh/v2/internal/acl"
	"github.com/teacat99/RelayMesh/v2/internal/model"
	"github.com/teacat99/RelayMesh/v2/internal/store"
)

// project_docs 的项目范围（#114，设计稿 02 / 06）。
//
// 只在调用方显式传 scope=project（或 action=module）时才走这里；没传时 handleProjectDocs 的行为与以前逐字一致。
// 项目是用户建的：工作流没有归入任何项目时，这里给出明确的原因与下一步，而不是悄悄退回工作流范围。

const projectScopeRecent = 8

// resolveProjectForDocs 先按 project_directory（+ host_name）找项目，再按工作流最近一次会话所在目录找。
func (s *Server) resolveProjectForDocs(ctx context.Context, a projectDocsArgs) (*model.Project, error) {
	if strings.TrimSpace(a.ProjectDirectory) != "" {
		p, err := s.store.ProjectOfDir(ctx, a.HostName, a.ProjectDirectory)
		if err != nil {
			return nil, err
		}
		if p != nil {
			return p, nil
		}
	}
	p, err := s.store.ProjectOfWorkflow(ctx, a.WorkflowID)
	if err != nil {
		return nil, err
	}
	if p == nil {
		return nil, fmt.Errorf("这个工作流还没有归入任何项目，scope=project 暂不可用：项目由用户在 Web「项目」页面创建并把目录归入（智能体不能自建项目）。请先用不带 scope 的调用，或请用户建立项目")
	}
	return p, nil
}

func (s *Server) projectDocsProjectScope(ctx context.Context, a projectDocsArgs, action string, actor store.DocActor) (any, error) {
	p, err := s.resolveProjectForDocs(ctx, a)
	if err != nil {
		return nil, err
	}
	switch action {
	case "overview":
		return s.projectScopeOverview(ctx, p)
	case "list":
		return s.projectScopeList(ctx, p, a)
	case "search":
		since, err := parseSince(a.Since)
		if err != nil {
			return nil, err
		}
		hits, err := s.store.SearchProject(ctx, store.ProjectSearchFilter{
			ProjectID: p.ID, Query: a.Query, ModulePath: firstNonEmpty(a.Module, a.ModulePath),
			Kinds: a.KindFilter, Statuses: a.StatusFilter, Tag: a.Tag, Render: a.Render, Limit: a.Limit, UpdatedAfter: since,
		})
		if err != nil {
			return nil, err
		}
		return map[string]any{"project": projectRef(p), "hits": hits}, nil
	case "get":
		ref := firstNonEmpty(a.Path, a.ID)
		if ref == "" {
			return nil, fmt.Errorf("get 需要 path 或 id")
		}
		doc, err := s.store.GetProjectDocInProject(ctx, p.ID, ref)
		if err != nil {
			return nil, err
		}
		if err := s.aclDocAccess(ctx, p, a, doc, doc.Path, acl.RoleRead); err != nil {
			return nil, err
		}
		return s.projectDocsRead(ctx, doc, a)
	case "save":
		if strings.TrimSpace(a.Path) == "" {
			return nil, fmt.Errorf("save 需要 path")
		}
		// 项目范围的 save 按「项目内路径」找已有文档：别的工作流写的文档也能在这里改（用 expected_rev 防冲突）。
		existing, _ := s.store.GetProjectDocInProject(ctx, p.ID, a.Path)
		if existing != nil && existing.WorkflowID != a.WorkflowID && a.ExpectedRev == 0 {
			return nil, fmt.Errorf("%s 由工作流 %s 创建；修改别的工作流的文档请带上 expected_rev（先 get 看当前 rev），避免互相覆盖", a.Path, existing.WorkflowID)
		}
		out, err := s.projectDocsSaveTo(ctx, a, actor, existing, p)
		if err == nil {
			s.auditDocWrite(ctx, a, "save", a.Path, nil)
		}
		return out, err
	case "blueprint":
		items, err := s.store.ProjectBlueprint(ctx, p.ID, a.WithTemplates)
		if err != nil {
			return nil, err
		}
		gaps := 0
		for _, it := range items {
			if !it.Present {
				gaps++
			}
		}
		return map[string]any{"project": projectRef(p), "items": items, "gaps": gaps}, nil
	case "module":
		path := firstNonEmpty(a.Module, a.ModulePath, a.Path)
		if path == "" {
			return nil, fmt.Errorf("module 需要 module（模块路径，如 chat/groups）")
		}
		v, err := s.store.ModulePage(ctx, p.ID, path)
		if err != nil {
			return nil, err
		}
		return map[string]any{"project": projectRef(p), "page": v}, nil
	case "grant", "revoke", "grants":
		return s.projectDocsGrants(ctx, p, a, action)
	default:
		return nil, fmt.Errorf("scope=project 目前支持 overview / list / get / search / save / blueprint / module / grant / revoke / grants；%s 请不带 scope（只作用于本工作流）", action)
	}
}

func projectRef(p *model.Project) map[string]any {
	return map[string]any{"id": p.ID, "slug": p.Slug, "name": p.Name, "acl_mode": p.ACLMode}
}

func firstNonEmpty(vs ...string) string {
	for _, v := range vs {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func (s *Server) projectScopeOverview(ctx context.Context, p *model.Project) (any, error) {
	items, err := s.store.ListProjectDocs(ctx, store.ProjectDocFilter{ProjectID: p.ID})
	if err != nil {
		return nil, err
	}
	mods, _ := s.store.ListProjectModules(ctx, p.ID)
	tree := make([]map[string]any, 0, len(mods))
	for _, m := range mods {
		tree = append(tree, map[string]any{"path": m.Path, "title": m.Title, "description": m.Description})
	}
	recent := make([]map[string]any, 0, projectScopeRecent)
	for i, d := range items {
		if i >= projectScopeRecent {
			break
		}
		recent = append(recent, map[string]any{
			"id": d.ID, "workflow_id": d.WorkflowID, "path": d.Path, "title": d.Title, "rev": d.Rev, "module_path": d.ModulePath, "updated_at": d.UpdatedAt,
		})
	}
	gaps := 0
	if bp, err := s.store.ProjectBlueprint(ctx, p.ID, false); err == nil {
		for _, it := range bp {
			if !it.Present {
				gaps++
			}
		}
	}
	paths := make([]string, 0, len(items))
	for _, d := range items {
		paths = append(paths, d.Path)
	}
	hint := "按模块读：action=module + module=<路径> 一次拿到总述、设计、决策与相关任务；找东西用 search（跨工作流）；写入 save（path 在项目内唯一，modules/<模块>/… 会自动归入模块）。"
	if len(items) == 0 {
		hint = "项目里还没有文档：可先 blueprint(scope=project, with_templates=true) 看推荐结构。"
	}
	return map[string]any{
		"project": projectRef(p), "total": len(items), "modules": tree, "tree": paths, "recent": recent,
		"blueprint_gaps": gaps, "hint": hint,
	}, nil
}

func (s *Server) projectScopeList(ctx context.Context, p *model.Project, a projectDocsArgs) (any, error) {
	since, err := parseSince(a.Since)
	if err != nil {
		return nil, err
	}
	items, err := s.store.ListProjectDocs(ctx, store.ProjectDocFilter{
		ProjectID: p.ID, Query: a.Query, Kinds: a.KindFilter, Statuses: a.StatusFilter, Tag: a.Tag,
		Prefix: a.Prefix, ModulePath: firstNonEmpty(a.Module, a.ModulePath), Render: a.Render, Limit: a.Limit, UpdatedAfter: since,
	})
	if err != nil {
		return nil, err
	}
	// 项目里可能有上千篇：只给目录与元数据，不带正文（要正文再 get）。
	rows := make([]map[string]any, 0, len(items))
	for _, d := range items {
		rows = append(rows, map[string]any{
			"id": d.ID, "workflow_id": d.WorkflowID, "path": d.Path, "title": d.Title, "kind": d.Kind, "status": d.Status,
			"rev": d.Rev, "module_path": d.ModulePath, "render": d.Render, "tags": []string(d.Tags), "updated_at": d.UpdatedAt,
		})
	}
	return map[string]any{"project": projectRef(p), "items": rows, "count": len(rows)}, nil
}
