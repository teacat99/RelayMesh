package store

import (
	"context"
	"sort"
	"strings"
	"time"

	"github.com/teacat99/RelayMesh/v2/internal/docs"
	"github.com/teacat99/RelayMesh/v2/internal/model"
)

// 项目范围的查询（#114 M4a.2，设计稿 06 §5）：跨项目内所有工作流的搜索、模块页聚合、推荐结构缺口。

// ProjectSearchFilter 项目范围搜索的条件。
type ProjectSearchFilter struct {
	ProjectID       uint
	Query           string
	ModulePath      string
	Kinds           []string
	Statuses        []string
	Tag             string
	OwnerIdentityID uint
	Render          string
	UpdatedAfter    time.Time
	Limit           int
}

// ProjectSearchHit 是一条项目范围的命中（在单工作流命中的基础上带上归属信息，让界面能直接打开）。
type ProjectSearchHit struct {
	docs.SearchHit
	DocID      uint      `json:"doc_id"`
	WorkflowID string    `json:"workflow_id"`
	ModulePath string    `json:"module_path,omitempty"`
	Kind       string    `json:"kind"`
	Status     string    `json:"status"`
	Render     string    `json:"render,omitempty"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// SearchProject 在项目的全部文档里搜索（打分排序）。没有关键词时按最近更新排序返回过滤后的文档。
func (s *Store) SearchProject(ctx context.Context, f ProjectSearchFilter) ([]ProjectSearchHit, error) {
	all, err := s.ListProjectDocs(ctx, ProjectDocFilter{
		ProjectID: f.ProjectID, Kinds: f.Kinds, Statuses: f.Statuses, Tag: f.Tag, ModulePath: f.ModulePath,
		OwnerIdentityID: f.OwnerIdentityID, Render: f.Render, UpdatedAfter: f.UpdatedAfter,
	})
	if err != nil {
		return nil, err
	}
	limit := f.Limit
	if limit <= 0 {
		limit = 30
	}
	q := strings.ToLower(strings.TrimSpace(f.Query))
	hits := make([]ProjectSearchHit, 0, len(all))
	for _, d := range all {
		h := ProjectSearchHit{
			SearchHit: docs.SearchHit{Path: d.Path, Title: d.Title},
			DocID:     d.ID, WorkflowID: d.WorkflowID, ModulePath: d.ModulePath, Kind: d.Kind, Status: d.Status, Render: d.Render, UpdatedAt: d.UpdatedAt,
		}
		if q != "" {
			score, snip, anchor := docs.ScoreDocument(q, d.Path, d.Title, d.Body, d.Tags)
			if score <= 0 {
				continue
			}
			h.Score, h.Snippet, h.Anchor = score, snip, anchor
		}
		hits = append(hits, h)
	}
	sort.SliceStable(hits, func(i, j int) bool {
		if hits[i].Score != hits[j].Score {
			return hits[i].Score > hits[j].Score
		}
		return hits[i].UpdatedAt.After(hits[j].UpdatedAt)
	})
	if len(hits) > limit {
		hits = hits[:limit]
	}
	return hits, nil
}

// DocBrief 是模块页里一篇文档的摘要（不含正文）。
type DocBrief struct {
	ID         uint      `json:"id"`
	WorkflowID string    `json:"workflow_id"`
	Path       string    `json:"path"`
	Title      string    `json:"title"`
	Kind       string    `json:"kind"`
	Status     string    `json:"status"`
	Render     string    `json:"render,omitempty"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// ModuleTask 是模块页里的相关任务（按名称与任务看板的模块对齐，设计稿 06 §3.2；任务本身不动）。
type ModuleTask struct {
	WorkflowID string `json:"workflow_id"`
	Seq        int    `json:"seq"`
	Title      string `json:"title"`
	State      string `json:"state"`
}

// ModulePageView 是模块页：总述 + 设计 + 决策 + 相关任务 + 最近修订 + 渲染型文档 + 负责人。
type ModulePageView struct {
	Module     model.ProjectModule   `json:"module"`
	Children   []model.ProjectModule `json:"children"`
	OwnerLabel string                `json:"owner_label,omitempty"`
	Index      *DocBrief             `json:"index,omitempty"`  // modules/<m>/index.md
	Design     *DocBrief             `json:"design,omitempty"` // modules/<m>/design.md
	Decisions  []DocBrief            `json:"decisions"`
	Artifacts  []DocBrief            `json:"artifacts"` // 渲染类型不是 md 的
	Docs       []DocBrief            `json:"docs"`      // 其余
	Recent     []DocBrief            `json:"recent"`
	Tasks      []ModuleTask          `json:"tasks"`
}

func briefOf(d model.ProjectDoc) DocBrief {
	return DocBrief{ID: d.ID, WorkflowID: d.WorkflowID, Path: d.Path, Title: d.Title, Kind: d.Kind, Status: d.Status, Render: d.Render, UpdatedAt: d.UpdatedAt}
}

// ModulePage 聚合一个模块（含子模块下的文档）的页面。
func (s *Store) ModulePage(ctx context.Context, projectID uint, modulePath string) (*ModulePageView, error) {
	path, err := NormalizeModulePath(modulePath)
	if err != nil {
		return nil, NewInvalidInputError(err.Error())
	}
	mods, err := s.ListProjectModules(ctx, projectID)
	if err != nil {
		return nil, err
	}
	view := &ModulePageView{Children: []model.ProjectModule{}, Decisions: []DocBrief{}, Artifacts: []DocBrief{}, Docs: []DocBrief{}, Recent: []DocBrief{}, Tasks: []ModuleTask{}}
	found := false
	for _, m := range mods {
		switch {
		case m.Path == path:
			view.Module, found = m, true
		case strings.HasPrefix(m.Path, path+"/") && !strings.Contains(strings.TrimPrefix(m.Path, path+"/"), "/"):
			view.Children = append(view.Children, m)
		}
	}
	if !found {
		return nil, NewNotFoundError("模块不存在")
	}
	if view.Module.OwnerIdentityID != 0 {
		var ident model.AgentIdentity
		if err := s.db.WithContext(ctx).First(&ident, view.Module.OwnerIdentityID).Error; err == nil {
			view.OwnerLabel = strings.TrimSpace(ident.Name)
			if view.OwnerLabel == "" {
				view.OwnerLabel = dirBase(ident.ProjectDirectory)
			}
		}
	}
	list, err := s.ListProjectDocs(ctx, ProjectDocFilter{ProjectID: projectID, ModulePath: path})
	if err != nil {
		return nil, err
	}
	for _, d := range list {
		b := briefOf(d)
		switch {
		case d.Path == "modules/"+path+"/index.md":
			view.Index = &b
		case d.Path == "modules/"+path+"/design.md":
			view.Design = &b
		case d.Kind == model.DocKindDecision:
			view.Decisions = append(view.Decisions, b)
		case d.Render != "" && d.Render != model.DocRenderMarkdown:
			view.Artifacts = append(view.Artifacts, b)
		default:
			view.Docs = append(view.Docs, b)
		}
		view.Recent = append(view.Recent, b)
	}
	sort.SliceStable(view.Recent, func(i, j int) bool { return view.Recent[i].UpdatedAt.After(view.Recent[j].UpdatedAt) })
	if len(view.Recent) > 8 {
		view.Recent = view.Recent[:8]
	}
	view.Tasks, err = s.moduleTasks(ctx, projectID, view.Module)
	if err != nil {
		return nil, err
	}
	return view, nil
}

// moduleTasks 取与模块名称对齐的任务：项目的任务板（及各工作流的独立板）里，模块（或其任一祖先）的名称等于项目模块的标题或路径末段。
func (s *Store) moduleTasks(ctx context.Context, projectID uint, m model.ProjectModule) ([]ModuleTask, error) {
	wfs, err := s.ProjectWorkflowIDs(ctx, projectID)
	if err != nil {
		return []ModuleTask{}, err
	}
	names := []string{strings.ToLower(strings.TrimSpace(m.Title))}
	if i := strings.LastIndex(m.Path, "/"); i >= 0 {
		names = append(names, strings.ToLower(m.Path[i+1:]))
	} else {
		names = append(names, strings.ToLower(m.Path))
	}
	var wm []model.WorkModule
	// 项目的任务板（同一个项目的工作流共用）；再加上各工作流各自的独立板（冲突回退或尚未归入项目的）。
	if err := s.db.WithContext(ctx).Where("board_key = ? OR workflow_id IN ?", ProjectBoardKey(projectID), wfs).Find(&wm).Error; err != nil {
		return nil, err
	}
	match := map[uint]bool{}
	for _, w := range wm {
		n := strings.ToLower(strings.TrimSpace(w.Name))
		for _, want := range names {
			if want != "" && n == want {
				match[w.ID] = true
			}
		}
	}
	if len(match) == 0 {
		return []ModuleTask{}, nil
	}
	// 命中模块的所有后代也算。
	for changed := true; changed; {
		changed = false
		for _, w := range wm {
			if w.ParentID != nil && match[*w.ParentID] && !match[w.ID] {
				match[w.ID], changed = true, true
			}
		}
	}
	ids := make([]uint, 0, len(match))
	for id := range match {
		ids = append(ids, id)
	}
	var items []model.WorkItem
	if err := s.db.WithContext(ctx).Select("workflow_id", "seq", "title", "state").
		Where("module_id IN ?", ids).Order("updated_at DESC").Limit(30).Find(&items).Error; err != nil {
		return nil, err
	}
	out := make([]ModuleTask, 0, len(items))
	for _, it := range items {
		out = append(out, ModuleTask{WorkflowID: it.WorkflowID, Seq: it.Seq, Title: it.Title, State: it.State})
	}
	return out, nil
}

// ProjectBlueprint 项目知识库的推荐结构与缺口。
func (s *Store) ProjectBlueprint(ctx context.Context, projectID uint, withTemplates bool) ([]docs.BlueprintItem, error) {
	list, err := s.ListProjectDocs(ctx, ProjectDocFilter{ProjectID: projectID, IncludeArchived: true})
	if err != nil {
		return nil, err
	}
	have := make(map[string]bool, len(list))
	for _, d := range list {
		have[d.Path] = true
	}
	mods, err := s.ListProjectModules(ctx, projectID)
	if err != nil {
		return nil, err
	}
	paths := make([]string, 0, len(mods))
	for _, m := range mods {
		paths = append(paths, m.Path)
	}
	return docs.ProjectBlueprint(have, paths, withTemplates), nil
}
