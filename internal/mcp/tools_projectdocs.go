package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/teacat99/RelayMesh/v2/internal/acl"
	"github.com/teacat99/RelayMesh/v2/internal/confirm"
	"github.com/teacat99/RelayMesh/v2/internal/docs"
	"github.com/teacat99/RelayMesh/v2/internal/model"
	"github.com/teacat99/RelayMesh/v2/internal/store"
)

const (
	projectDocsExportDefaultBytes = 512 * 1024
	projectDocsGetDefaultChars    = 12000
)

type projectDocsArgs struct {
	Action        string            `json:"action,omitempty"`
	WorkflowID    string            `json:"workflow_id"`
	Path          string            `json:"path,omitempty"`
	ID            string            `json:"id,omitempty"`
	Section       string            `json:"section,omitempty"`
	MaxChars      int               `json:"max_chars,omitempty"`
	NextOffset    int               `json:"next_offset,omitempty"`
	Rev           int               `json:"rev,omitempty"`
	Query         string            `json:"query,omitempty"`
	Body          string            `json:"body,omitempty"`
	Append        string            `json:"append,omitempty"`
	Prepend       string            `json:"prepend,omitempty"`
	SectionBody   string            `json:"section_body,omitempty"`
	ExpectedRev   int               `json:"expected_rev,omitempty"`
	Summary       string            `json:"summary,omitempty"`
	Kind          string            `json:"kind,omitempty"`
	Status        string            `json:"status,omitempty"`
	Tags          []string          `json:"tags,omitempty"`
	Title         string            `json:"title,omitempty"`
	KindFilter    []string          `json:"kinds,omitempty"`
	StatusFilter  []string          `json:"statuses,omitempty"`
	Prefix        string            `json:"prefix,omitempty"`
	Tag           string            `json:"tag,omitempty"`
	Limit         int               `json:"limit,omitempty"`
	Paths         []string          `json:"paths,omitempty"`
	Known         map[string]string `json:"known,omitempty"`
	MaxBytes      int               `json:"max_bytes,omitempty"`
	Cursor        string            `json:"cursor,omitempty"`
	BaseDir       string            `json:"base_dir,omitempty"`
	Files         []importDocFile   `json:"files,omitempty"`
	Mode          string            `json:"mode,omitempty"`
	DryRun        bool              `json:"dry_run,omitempty"`
	WithTemplates bool              `json:"with_templates,omitempty"`
	// 项目知识库（#114）：都是可选的；不传时与以前完全一致。
	Scope            string `json:"scope,omitempty"`
	Module           string `json:"module,omitempty"`
	ModulePath       string `json:"module_path,omitempty"`
	Render           string `json:"render,omitempty"`
	Since            string `json:"since,omitempty"` // list / search：只要这个时间之后更新过的（RFC3339）
	ProjectDirectory string `json:"project_directory,omitempty"`
	HostName         string `json:"host_name,omitempty"`
	// 授权（grant / revoke / grants）。
	Grantee      string `json:"grantee,omitempty"`
	GrantScope   string `json:"grant_scope,omitempty"`
	GrantRef     string `json:"grant_ref,omitempty"`
	Role         string `json:"role,omitempty"`
	ExpiresHours int    `json:"expires_hours,omitempty"`
	// Artifact 数据与评论（data_* / comments / comment / comment_resolve）。
	Key             string          `json:"key,omitempty"`
	Value           json.RawMessage `json:"value,omitempty"`
	ExpectedVersion *int            `json:"expected_version,omitempty"`
	Anchor          string          `json:"anchor,omitempty"`
	CommentID       uint            `json:"comment_id,omitempty"`
	Resolved        *bool           `json:"resolved,omitempty"`
	IncludeResolved bool            `json:"include_resolved,omitempty"`
}

type importDocFile struct {
	Path    string   `json:"path"`
	Content string   `json:"content"`
	Kind    string   `json:"kind,omitempty"`
	Status  string   `json:"status,omitempty"`
	Tags    []string `json:"tags,omitempty"`
	Title   string   `json:"title,omitempty"`
}

func (s *Server) notifyProjectDocUpdated(doc *model.ProjectDoc, deleted bool) {
	if s.onUpdate == nil || doc == nil {
		return
	}
	s.onUpdate("project_doc_updated", map[string]any{
		"id": doc.ID, "workflow_id": doc.WorkflowID, "path": doc.Path, "deleted": deleted,
	})
}

func (s *Server) notifyProjectDocsWorkflow(workflowID string) {
	if s.onUpdate == nil || workflowID == "" {
		return
	}
	s.onUpdate("project_doc_updated", map[string]any{"workflow_id": workflowID})
}

func (s *Server) handleProjectDocs(ctx context.Context, args json.RawMessage) (any, error) {
	var a projectDocsArgs
	if len(args) > 0 {
		if err := json.Unmarshal(args, &a); err != nil {
			return nil, fmt.Errorf("参数解析失败：%v", err)
		}
	}
	if strings.TrimSpace(a.WorkflowID) == "" {
		return nil, fmt.Errorf("workflow_id 必填")
	}
	action := strings.TrimSpace(a.Action)
	if action == "" {
		action = "overview"
	}
	actor := store.DocActor{Kind: model.DocActorAgent}
	if c := aclCallerFrom(ctx); c != nil {
		actor.IdentityID = c.IdentityID // 新建的文档记为这个身份负责
	}
	if isArtifactAction(action) {
		return s.projectDocsArtifact(ctx, a, action)
	}
	if a.Scope == "project" || action == "module" || isGrantAction(action) {
		return s.projectDocsProjectScope(ctx, a, action, actor)
	}
	out, err := s.projectDocsWorkflowScope(ctx, a, action, actor)
	if err == nil {
		switch action {
		case "save":
			s.auditDocWrite(ctx, a, action, a.Path, nil)
		case "delete":
			s.auditDocWrite(ctx, a, action, a.Path, nil)
		case "import":
			if !a.DryRun {
				s.auditDocWrite(ctx, a, action, fmt.Sprintf("import:%d files", len(a.Files)), nil)
			}
		case "import_sheet":
			s.auditDocWrite(ctx, a, action, "workflow-sheet", nil)
		}
	}
	return out, err
}

// projectDocsWorkflowScope 是不带 scope 的老路径：只作用于本工作流自己的文档，行为与项目化之前一致。
func (s *Server) projectDocsWorkflowScope(ctx context.Context, a projectDocsArgs, action string, actor store.DocActor) (any, error) {
	switch action {
	case "overview":
		return s.projectDocsOverview(ctx, a.WorkflowID)
	case "list":
		return s.projectDocsList(ctx, a)
	case "get":
		return s.projectDocsGet(ctx, a)
	case "search":
		return s.projectDocsSearch(ctx, a)
	case "save":
		return s.projectDocsSave(ctx, a, actor)
	case "history":
		return s.projectDocsHistory(ctx, a)
	case "links":
		return s.projectDocsLinks(ctx, a)
	case "delete":
		return s.projectDocsDelete(ctx, a, actor)
	case "export":
		return s.projectDocsExport(ctx, a)
	case "import":
		return s.projectDocsImport(ctx, a, actor)
	case "blueprint":
		return s.projectDocsBlueprint(ctx, a)
	case "import_sheet":
		return s.projectDocsImportSheet(ctx, a, actor)
	default:
		return nil, fmt.Errorf("未知 action：%s", action)
	}
}

func (s *Server) projectDocsOverview(ctx context.Context, wf string) (any, error) {
	items, err := s.store.ListProjectDocs(ctx, store.ProjectDocFilter{WorkflowID: wf})
	if err != nil {
		return nil, err
	}
	paths := map[string]bool{}
	var recent []map[string]any
	for _, d := range items {
		paths[d.Path] = true
	}
	for i, d := range items {
		if i >= 5 {
			break
		}
		recent = append(recent, map[string]any{"seq": d.Seq, "path": d.Path, "title": d.Title, "rev": d.Rev, "updated_at": d.UpdatedAt})
	}
	bp := docs.Blueprint(paths, false)
	gaps := docs.CountBlueprintGaps(bp)
	tree := docPathTree(items)
	hint := "开工或恢复上下文时先调 overview；写设计/决策用 save（path 即目录结构）；老项目梳理用 import；一次性落到本地用 export。"
	if len(items) == 0 {
		hint = "清单为空：可先调 blueprint(with_templates=true) 看推荐结构，或 import_sheet 把 Workflow Sheet 复制为 session/workflow-sheet.md。"
	}
	return map[string]any{
		"workflow_id": wf, "total": len(items), "tree": tree, "recent": recent,
		"blueprint_gaps": gaps, "hint": hint,
	}, nil
}

func docPathTree(items []model.ProjectDoc) []string {
	out := make([]string, 0, len(items))
	for _, d := range items {
		out = append(out, d.Path)
	}
	return out
}

// parseSince 解析 since 参数（RFC3339）；空串返回零值。
func parseSince(raw string) (time.Time, error) {
	if strings.TrimSpace(raw) == "" {
		return time.Time{}, nil
	}
	t, err := time.Parse(time.RFC3339, strings.TrimSpace(raw))
	if err != nil {
		return time.Time{}, fmt.Errorf("since 需要 RFC3339 时间（如 2026-10-15T08:00:00Z）：%v", err)
	}
	return t, nil
}

// projectDocRow 是工作流范围的 list 返回的一行：文档的全部元数据，不含正文（要正文用 get，批量取用 export）。
// 字段名与 model.ProjectDoc 的 JSON 一致，只是少了 body（#136）。
type projectDocRow struct {
	ID         uint      `json:"id"`
	WorkflowID string    `json:"workflow_id"`
	Seq        int       `json:"seq"`
	Path       string    `json:"path"`
	Title      string    `json:"title"`
	Kind       string    `json:"kind"`
	Status     string    `json:"status"`
	Tags       []string  `json:"tags"`
	Rev        int       `json:"rev"`
	Hash       string    `json:"hash"`
	ProjectID  uint      `json:"project_id,omitempty"`
	ModulePath string    `json:"module_path,omitempty"`
	Render     string    `json:"render,omitempty"`
	CreatedBy  string    `json:"created_by"`
	UpdatedBy  string    `json:"updated_by"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

func newProjectDocRow(d model.ProjectDoc) projectDocRow {
	return projectDocRow{
		ID: d.ID, WorkflowID: d.WorkflowID, Seq: d.Seq, Path: d.Path, Title: d.Title, Kind: d.Kind, Status: d.Status,
		Tags: []string(d.Tags), Rev: d.Rev, Hash: d.Hash, ProjectID: d.ProjectID, ModulePath: d.ModulePath, Render: d.Render,
		CreatedBy: d.CreatedBy, UpdatedBy: d.UpdatedBy, CreatedAt: d.CreatedAt, UpdatedAt: d.UpdatedAt,
	}
}

func (s *Server) projectDocsList(ctx context.Context, a projectDocsArgs) (any, error) {
	since, err := parseSince(a.Since)
	if err != nil {
		return nil, err
	}
	items, err := s.store.ListProjectDocs(ctx, store.ProjectDocFilter{
		WorkflowID: a.WorkflowID, Query: a.Query, Kinds: a.KindFilter,
		Statuses: a.StatusFilter, Tag: a.Tag, Prefix: a.Prefix, Limit: a.Limit,
		UpdatedAfter: since,
	})
	if err != nil {
		return nil, err
	}
	// 只给目录与元数据，不带正文：11 篇文档的正文就有约 200 KB，对智能体是不必要的上下文开销（与项目范围的 list 一致）。
	rows := make([]projectDocRow, 0, len(items))
	for _, d := range items {
		rows = append(rows, newProjectDocRow(d))
	}
	return map[string]any{"items": rows, "count": len(rows)}, nil
}

func (s *Server) projectDocsGet(ctx context.Context, a projectDocsArgs) (any, error) {
	ref := a.Path
	if ref == "" {
		ref = a.ID
	}
	if ref == "" {
		return nil, fmt.Errorf("get 需要 path 或 id")
	}
	doc, err := s.store.GetProjectDocByRef(ctx, a.WorkflowID, ref)
	if err != nil {
		return nil, err
	}
	return s.projectDocsRead(ctx, doc, a)
}

// projectDocsRead 把一篇已找到的文档按 section / rev / 分块参数读出来（工作流范围与项目范围共用）。
func (s *Server) projectDocsRead(ctx context.Context, doc *model.ProjectDoc, a projectDocsArgs) (any, error) {
	body := doc.Body
	historic := false
	if a.Rev > 0 && a.Rev != doc.Rev {
		r, err := s.store.GetProjectDocRevision(ctx, doc.ID, a.Rev)
		if err != nil {
			return nil, err
		}
		body, historic = r.Body, true
	}
	if a.Section != "" {
		sec, ok := docs.GetSection(body, a.Section)
		if !ok {
			return nil, fmt.Errorf("找不到章节：%s", a.Section)
		}
		body = sec
	}
	max := a.MaxChars
	if max <= 0 {
		max = projectDocsGetDefaultChars
	}
	off := a.NextOffset
	runes := []rune(body)
	if off > len(runes) {
		off = len(runes)
	}
	end := off + max
	nextOff := 0
	if end < len(runes) {
		nextOff = end
		end = off + max
	} else {
		end = len(runes)
	}
	chunk := string(runes[off:end])
	// 决策卡带上确认项的状态与答复（confirm_ref 是 Q12 这样的编号）：智能体读文档就能知道哪些决定用户已经拍了板。
	out := map[string]any{
		"doc": doc, "body": chunk, "headings": docs.ParseHeadings(body),
		"decisions": s.store.DocDecisionViews(ctx, doc, body, historic),
	}
	if nextOff > 0 {
		out["next_offset"] = nextOff
	}
	return out, nil
}

func (s *Server) projectDocsSearch(ctx context.Context, a projectDocsArgs) (any, error) {
	hits, err := s.store.SearchProjectDocs(ctx, a.WorkflowID, a.Query, a.Limit)
	if err != nil {
		return nil, err
	}
	return map[string]any{"hits": hits}, nil
}

func (s *Server) projectDocsSave(ctx context.Context, a projectDocsArgs, actor store.DocActor) (any, error) {
	if strings.TrimSpace(a.Path) == "" {
		return nil, fmt.Errorf("save 需要 path")
	}
	existing, _ := s.store.GetProjectDocByRef(ctx, a.WorkflowID, a.Path)
	var p *model.Project
	if c := aclCallerFrom(ctx); c != nil {
		p = c.Project
	}
	return s.projectDocsSaveTo(ctx, a, actor, existing, p)
}

// projectDocsSaveTo 是 save 的主体：existing 为空就新建，否则按 append / section / 整篇更新。
func (s *Server) projectDocsSaveTo(ctx context.Context, a projectDocsArgs, actor store.DocActor, existing *model.ProjectDoc, p *model.Project) (any, error) {
	if err := s.aclDocAccess(ctx, p, a, existing, a.Path, acl.RoleEdit); err != nil {
		return nil, err
	}
	body := a.Body
	if existing != nil {
		body = existing.Body
		if a.Body != "" {
			body = a.Body
		} else if a.Append != "" {
			body = docs.AppendToBody(body, a.Append)
		} else if a.Prepend != "" {
			body = docs.PrependToBody(body, a.Prepend)
		} else if a.Section != "" && a.SectionBody != "" {
			var e error
			body, e = docs.ReplaceSection(body, a.Section, a.SectionBody)
			if e != nil {
				return nil, fmt.Errorf("%s", e.Error())
			}
		}
		patch := store.ProjectDocPatch{
			ExpectedRev: a.ExpectedRev, Summary: a.Summary,
		}
		if body != existing.Body {
			patch.Body = &body
		}
		if a.Title != "" {
			t := a.Title
			patch.Title = &t
		}
		if a.Kind != "" {
			k := a.Kind
			patch.Kind = &k
		}
		if a.Status != "" {
			st := a.Status
			patch.Status = &st
		}
		if a.Tags != nil {
			patch.Tags = &a.Tags
		}
		if a.ModulePath != "" {
			m := a.ModulePath
			patch.ModulePath = &m
		}
		if a.Render != "" {
			r := a.Render
			patch.Render = &r
		}
		res, err := s.store.UpdateProjectDoc(ctx, existing.ID, patch, actor)
		if err != nil {
			return nil, err
		}
		if !res.Unchanged {
			s.notifyProjectDocUpdated(res.Doc, false)
		}
		return map[string]any{"doc": res.Doc, "unchanged": res.Unchanged, "warnings": res.Warnings}, nil
	}
	if body == "" && a.SectionBody != "" {
		body = a.SectionBody
	}
	res, err := s.store.CreateProjectDoc(ctx, store.ProjectDocInput{
		WorkflowID: a.WorkflowID, Path: a.Path, Body: body, Title: a.Title,
		Kind: a.Kind, Status: a.Status, Tags: a.Tags, Summary: a.Summary,
		ModulePath: a.ModulePath, Render: a.Render,
	}, actor)
	if err != nil {
		return nil, err
	}
	s.notifyProjectDocUpdated(res.Doc, false)
	return map[string]any{"doc": res.Doc, "created": true, "warnings": res.Warnings}, nil
}

func (s *Server) projectDocsHistory(ctx context.Context, a projectDocsArgs) (any, error) {
	ref := a.Path
	if ref == "" {
		ref = a.ID
	}
	doc, err := s.store.GetProjectDocByRef(ctx, a.WorkflowID, ref)
	if err != nil {
		return nil, err
	}
	revs, err := s.store.ListProjectDocRevisions(ctx, doc.ID)
	if err != nil {
		return nil, err
	}
	return map[string]any{"revisions": revs}, nil
}

// projectDocsLinks 返回一篇文档的出链（含是否存在）与反链。
func (s *Server) projectDocsLinks(ctx context.Context, a projectDocsArgs) (any, error) {
	ref := a.Path
	if ref == "" {
		ref = a.ID
	}
	doc, err := s.store.GetProjectDocByRef(ctx, a.WorkflowID, ref)
	if err != nil {
		return nil, err
	}
	l, err := s.store.DocLinks(ctx, doc)
	if err != nil {
		return nil, err
	}
	return map[string]any{"path": doc.Path, "outlinks": l.Outlinks, "backlinks": l.Backlinks}, nil
}

func (s *Server) projectDocsDelete(ctx context.Context, a projectDocsArgs, actor store.DocActor) (any, error) {
	ref := a.Path
	if ref == "" {
		ref = a.ID
	}
	doc, err := s.store.GetProjectDocByRef(ctx, a.WorkflowID, ref)
	if err != nil {
		return nil, err
	}
	out, err := s.store.ArchiveProjectDoc(ctx, doc.ID, actor)
	if err != nil {
		return nil, err
	}
	s.notifyProjectDocUpdated(out, true)
	return map[string]any{"doc": out, "archived": true}, nil
}

func (s *Server) projectDocsExport(ctx context.Context, a projectDocsArgs) (any, error) {
	items, err := s.store.ListProjectDocs(ctx, store.ProjectDocFilter{
		WorkflowID: a.WorkflowID, Kinds: a.KindFilter, Statuses: a.StatusFilter, Prefix: a.Prefix,
	})
	if err != nil {
		return nil, err
	}
	if len(a.Paths) > 0 {
		want := map[string]bool{}
		for _, p := range a.Paths {
			want[p] = true
		}
		var filtered []model.ProjectDoc
		for _, d := range items {
			if want[d.Path] {
				filtered = append(filtered, d)
			}
		}
		items = filtered
	}
	maxB := a.MaxBytes
	if maxB <= 0 {
		maxB = projectDocsExportDefaultBytes
	}
	start := 0
	if a.Cursor != "" {
		fmt.Sscanf(a.Cursor, "%d", &start)
	}
	var files []map[string]any
	used := 0
	i := start
	for ; i < len(items); i++ {
		d := items[i]
		// 决策结果会随作答变化而正文不变，所以有决策的文档用「正文哈希 + 决策结果摘要」做差量比较，
		// 否则智能体带着旧哈希来，会漏掉「决策刚被作答」的文档。
		rows := s.store.DocDecisionExportRows(ctx, &d)
		hash := confirm.ExportHash(d.Hash, rows)
		if a.Known != nil && a.Known[d.Path] == hash {
			continue
		}
		content := docs.AppendDecisionResultsTable(d.Body, rows)
		sz := len(content)
		if used+sz > maxB && len(files) > 0 {
			break
		}
		used += sz
		p := d.Path
		if a.BaseDir != "" {
			p = strings.TrimSuffix(a.BaseDir, "/") + "/" + d.Path
		}
		files = append(files, map[string]any{
			"path": p, "rev": d.Rev, "hash": hash, "content": content,
		})
		if used >= maxB {
			i++
			break
		}
	}
	var entries []docs.ExportEntry
	for _, d := range items {
		entries = append(entries, docs.ExportEntry{
			Path: d.Path, Title: d.Title, Kind: d.Kind, Status: d.Status,
			Rev: d.Rev, Hash: confirm.ExportHash(d.Hash, s.store.DocDecisionExportRows(ctx, &d)), Updated: d.UpdatedAt.Format("2006-01-02"),
		})
	}
	out := map[string]any{"files": files, "index_md": docs.BuildIndexMarkdown(a.WorkflowID, entries)}
	if i < len(items) {
		out["cursor"] = fmt.Sprintf("%d", i)
	}
	return out, nil
}

func (s *Server) projectDocsImport(ctx context.Context, a projectDocsArgs, actor store.DocActor) (any, error) {
	var files []store.ImportDocFile
	for _, f := range a.Files {
		files = append(files, store.ImportDocFile(f))
	}
	mode := a.Mode
	if mode == "" {
		mode = "upsert"
	}
	res, err := s.store.ImportProjectDocs(ctx, a.WorkflowID, files, mode, a.DryRun, actor)
	if err != nil {
		return nil, err
	}
	if !a.DryRun {
		s.notifyProjectDocsWorkflow(a.WorkflowID)
	}
	return res, nil
}

func (s *Server) projectDocsBlueprint(ctx context.Context, a projectDocsArgs) (any, error) {
	items, _ := s.store.ListProjectDocs(ctx, store.ProjectDocFilter{WorkflowID: a.WorkflowID})
	paths := map[string]bool{}
	for _, d := range items {
		paths[d.Path] = true
	}
	bp := docs.Blueprint(paths, a.WithTemplates)
	return map[string]any{"items": bp, "gaps": docs.CountBlueprintGaps(bp)}, nil
}

func (s *Server) projectDocsImportSheet(ctx context.Context, a projectDocsArgs, actor store.DocActor) (any, error) {
	note, err := s.store.GetNoteByKey(ctx, a.WorkflowID, "session_doc")
	if err != nil || note == nil || strings.TrimSpace(note.Content) == "" {
		return map[string]any{"skipped": true, "reason": "工作表为空或不存在"}, nil
	}
	path := "session/workflow-sheet.md"
	// 工作流属于项目时，工作表放进项目的 sessions/<工作流>/ 下，项目里按工作流分开存放、互不撞路径。
	if p, _ := s.store.ProjectOfWorkflow(ctx, a.WorkflowID); p != nil {
		path = "sessions/" + a.WorkflowID + "/workflow-sheet.md"
	}
	existing, _ := s.store.GetProjectDocByRef(ctx, a.WorkflowID, path)
	if existing != nil {
		body := note.Content
		res, err := s.store.UpdateProjectDoc(ctx, existing.ID, store.ProjectDocPatch{
			Body: &body, Summary: "从 Workflow Sheet 同步",
		}, actor)
		if err != nil {
			return nil, err
		}
		s.notifyProjectDocUpdated(res.Doc, false)
		return map[string]any{"doc": res.Doc, "updated": true}, nil
	}
	res, err := s.store.CreateProjectDoc(ctx, store.ProjectDocInput{
		WorkflowID: a.WorkflowID, Path: path, Body: note.Content,
		Title: "Workflow Sheet", Kind: model.DocKindNote, Summary: "从 Workflow Sheet 导入",
	}, actor)
	if err != nil {
		return nil, err
	}
	s.notifyProjectDocUpdated(res.Doc, false)
	return map[string]any{"doc": res.Doc, "created": true}, nil
}
