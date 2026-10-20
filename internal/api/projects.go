package api

import (
	"encoding/csv"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/teacat99/RelayMesh/v2/internal/model"
	"github.com/teacat99/RelayMesh/v2/internal/store"
)

// 项目知识库的 REST（#114，设计稿 06）。
//
// 这些接口都在登录之后：调用者就是「人」。项目的创建、目录归属、迁移确认、ACL 档位、模块树都只有这里能做，
// MCP 工具层没有对应入口（Agent 不能自建项目，避免污染）。每次变更写一条审计（不含正文）。

func projectID(c *gin.Context) (uint, bool) {
	n, err := strconv.ParseUint(c.Param("id"), 10, 32)
	if err != nil || n == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "项目编号无效"})
		return 0, false
	}
	return uint(n), true
}

// humanAudit 记一条「人」做的审计；失败只记日志不影响操作本身。
func (h *APIHandler) humanAudit(c *gin.Context, projectID uint, action, scopeKind, scopeRef, target string, detail any) {
	_, _ = h.store.AddAuditEvent(c.Request.Context(), store.AuditInput{
		ProjectID: projectID, ActorKind: "human", ActorRef: "human", ActorLabel: "你",
		Action: action, ScopeKind: scopeKind, ScopeRef: scopeRef, Target: target, Detail: detail,
	})
}

type projectView struct {
	model.Project
	Stats store.ProjectStats `json:"stats"`
}

// ListProjects GET /api/v1/projects?archived=1
func (h *APIHandler) ListProjects(c *gin.Context) {
	ctx := c.Request.Context()
	list, err := h.store.ListProjects(ctx, c.Query("archived") == "1")
	if err != nil {
		respondDocError(c, err)
		return
	}
	out := make([]projectView, 0, len(list))
	for _, p := range list {
		st, _ := h.store.ProjectStatsOf(ctx, p.ID)
		out = append(out, projectView{Project: p, Stats: st})
	}
	c.JSON(http.StatusOK, gin.H{"items": out})
}

// GetProject GET /api/v1/projects/:id
func (h *APIHandler) GetProject(c *gin.Context) {
	id, ok := projectID(c)
	if !ok {
		return
	}
	ctx := c.Request.Context()
	p, err := h.store.GetProject(ctx, id)
	if err != nil {
		respondDocError(c, err)
		return
	}
	dirs, _ := h.store.ListProjectDirs(ctx, id)
	mods, _ := h.store.ListProjectModules(ctx, id)
	wfs, _ := h.store.ProjectWorkflowIDs(ctx, id)
	st, _ := h.store.ProjectStatsOf(ctx, id)
	c.JSON(http.StatusOK, gin.H{"project": projectView{Project: *p, Stats: st}, "dirs": dirs, "modules": mods, "workflows": wfs})
}

// CreateProject POST /api/v1/projects
func (h *APIHandler) CreateProject(c *gin.Context) {
	var req struct {
		Name        string `json:"name"`
		Slug        string `json:"slug"`
		Description string `json:"description"`
		ACLMode     string `json:"acl_mode"`
		// 创建时一并归入的目录（来自「项目建议」）。
		Dirs []struct {
			HostName         string `json:"host_name"`
			ProjectDirectory string `json:"project_directory"`
			BranchHint       string `json:"branch_hint"`
		} `json:"dirs"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请求格式无效"})
		return
	}
	ctx := c.Request.Context()
	p, err := h.store.CreateProject(ctx, store.ProjectInput{Name: req.Name, Slug: req.Slug, Description: req.Description, ACLMode: req.ACLMode})
	if err != nil {
		respondDocError(c, err)
		return
	}
	h.humanAudit(c, p.ID, model.AuditProjectCreate, "project", p.Slug, p.Name, map[string]any{"acl_mode": p.ACLMode})
	var dirErrs []string
	for _, d := range req.Dirs {
		pd, err := h.store.AddProjectDir(ctx, p.ID, d.HostName, d.ProjectDirectory, d.BranchHint)
		if err != nil {
			dirErrs = append(dirErrs, d.ProjectDirectory+"："+err.Error())
			continue
		}
		h.humanAudit(c, p.ID, model.AuditProjectDirAdd, "project", p.Slug, pd.HostName+":"+pd.ProjectDirectory, nil)
	}
	c.JSON(http.StatusCreated, gin.H{"project": p, "dir_errors": dirErrs})
}

// UpdateProject PUT /api/v1/projects/:id
func (h *APIHandler) UpdateProject(c *gin.Context) {
	id, ok := projectID(c)
	if !ok {
		return
	}
	var req struct {
		Name        *string `json:"name"`
		Description *string `json:"description"`
		ACLMode     *string `json:"acl_mode"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请求格式无效"})
		return
	}
	ctx := c.Request.Context()
	before, err := h.store.GetProject(ctx, id)
	if err != nil {
		respondDocError(c, err)
		return
	}
	p, err := h.store.UpdateProject(ctx, id, store.ProjectPatch{Name: req.Name, Description: req.Description, ACLMode: req.ACLMode})
	if err != nil {
		respondDocError(c, err)
		return
	}
	detail := map[string]any{}
	if req.Name != nil && p.Name != before.Name {
		detail["name"] = true
	}
	if req.Description != nil && p.Description != before.Description {
		detail["description"] = true
	}
	if p.ACLMode != before.ACLMode {
		detail["acl_mode"] = before.ACLMode + "→" + p.ACLMode
	}
	if len(detail) > 0 {
		h.humanAudit(c, id, model.AuditProjectUpdate, "project", p.Slug, p.Name, detail)
	}
	c.JSON(http.StatusOK, gin.H{"project": p})
}

// ArchiveProject DELETE /api/v1/projects/:id
func (h *APIHandler) ArchiveProject(c *gin.Context) {
	id, ok := projectID(c)
	if !ok {
		return
	}
	p, err := h.store.GetProject(c.Request.Context(), id)
	if err != nil {
		respondDocError(c, err)
		return
	}
	if err := h.store.ArchiveProject(c.Request.Context(), id); err != nil {
		respondDocError(c, err)
		return
	}
	h.humanAudit(c, id, model.AuditProjectUpdate, "project", p.Slug, p.Name, map[string]any{"archived": true})
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// AddProjectDir POST /api/v1/projects/:id/dirs
func (h *APIHandler) AddProjectDir(c *gin.Context) {
	id, ok := projectID(c)
	if !ok {
		return
	}
	var req struct {
		HostName         string `json:"host_name"`
		ProjectDirectory string `json:"project_directory"`
		BranchHint       string `json:"branch_hint"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请求格式无效"})
		return
	}
	pd, err := h.store.AddProjectDir(c.Request.Context(), id, req.HostName, req.ProjectDirectory, req.BranchHint)
	if err != nil {
		respondDocError(c, err)
		return
	}
	h.humanAudit(c, id, model.AuditProjectDirAdd, "project", strconv.Itoa(int(id)), pd.HostName+":"+pd.ProjectDirectory, nil)
	c.JSON(http.StatusCreated, gin.H{"dir": pd})
}

// RemoveProjectDir DELETE /api/v1/projects/:id/dirs/:dir_id
func (h *APIHandler) RemoveProjectDir(c *gin.Context) {
	id, ok := projectID(c)
	if !ok {
		return
	}
	did, err := strconv.ParseUint(c.Param("dir_id"), 10, 32)
	if err != nil || did == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "目录编号无效"})
		return
	}
	if err := h.store.RemoveProjectDir(c.Request.Context(), id, uint(did)); err != nil {
		respondDocError(c, err)
		return
	}
	h.humanAudit(c, id, model.AuditProjectDirRemove, "project", strconv.Itoa(int(id)), "dir#"+strconv.Itoa(int(did)), nil)
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// ProjectSuggestions GET /api/v1/projects/suggestions
func (h *APIHandler) ProjectSuggestions(c *gin.Context) {
	items, err := h.store.ProjectSuggestions(c.Request.Context())
	if err != nil {
		respondDocError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"items": items})
}

// UpsertProjectModule PUT /api/v1/projects/:id/modules
func (h *APIHandler) UpsertProjectModule(c *gin.Context) {
	id, ok := projectID(c)
	if !ok {
		return
	}
	var req struct {
		Path            string `json:"path"`
		Title           string `json:"title"`
		Description     string `json:"description"`
		OwnerIdentityID uint   `json:"owner_identity_id"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请求格式无效"})
		return
	}
	m, err := h.store.UpsertProjectModule(c.Request.Context(), id, store.ModuleInput{Path: req.Path, Title: req.Title, Description: req.Description, OwnerIdentityID: req.OwnerIdentityID})
	if err != nil {
		respondDocError(c, err)
		return
	}
	h.humanAudit(c, id, model.AuditModuleWrite, "module", m.Path, m.Title, nil)
	c.JSON(http.StatusOK, gin.H{"module": m})
}

// DeleteProjectModule DELETE /api/v1/projects/:id/modules?path=
func (h *APIHandler) DeleteProjectModule(c *gin.Context) {
	id, ok := projectID(c)
	if !ok {
		return
	}
	path := c.Query("path")
	if err := h.store.DeleteProjectModule(c.Request.Context(), id, path); err != nil {
		respondDocError(c, err)
		return
	}
	h.humanAudit(c, id, model.AuditModuleWrite, "module", path, "deleted", map[string]any{"deleted": true})
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// ProjectMigration GET /api/v1/projects/:id/migration —— dry-run，只读。
func (h *APIHandler) ProjectMigration(c *gin.Context) {
	id, ok := projectID(c)
	if !ok {
		return
	}
	rep, err := h.store.MigrationDryRun(c.Request.Context(), id)
	if err != nil {
		respondDocError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"report": rep})
}

// ApplyProjectMigration POST /api/v1/projects/:id/migration {hash} —— 确认迁移：带上 dry-run 的哈希。
func (h *APIHandler) ApplyProjectMigration(c *gin.Context) {
	id, ok := projectID(c)
	if !ok {
		return
	}
	var req struct {
		Hash string `json:"hash"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请求格式无效"})
		return
	}
	rep, err := h.store.ApplyMigration(c.Request.Context(), id, req.Hash)
	if err != nil {
		respondDocError(c, err)
		return
	}
	// 审计里留下每篇文档的去向（路径与编号，不含正文），将来要还原就照着它：
	// doc_ids 是这次并入项目的全部文档（还原 = 把它们的 project_id 置回 0）；moves 是其中改过路径的（还原 = path 改回 from_path）。
	// 只记 moves 的话路径不变的那些文档没法从审计里找回来。
	docIDs := make([]uint, 0, len(rep.Items))
	for _, it := range rep.Items {
		docIDs = append(docIDs, it.DocID)
	}
	h.humanAudit(c, id, model.AuditProjectMigrate, "project", strconv.Itoa(int(id)), "", map[string]any{
		"docs": rep.Docs, "assigned": rep.Assigned, "moves": rep.Moves, "doc_ids": docIDs, "hash": rep.Hash,
	})
	if h.broker != nil {
		h.broker.Broadcast("project_updated", gin.H{"project_id": id})
	}
	c.JSON(http.StatusOK, gin.H{"report": rep})
}

type projectDocRow struct {
	store.DocBrief
	Tags       []string `json:"tags,omitempty"`
	ModulePath string   `json:"module_path,omitempty"`
}

func projectDocFilters(c *gin.Context, id uint) store.ProjectSearchFilter {
	f := store.ProjectSearchFilter{
		ProjectID:  id,
		Query:      strings.TrimSpace(c.Query("q")),
		ModulePath: strings.Trim(strings.TrimSpace(c.Query("module")), "/"),
		Kinds:      splitCSV(c.Query("kind")),
		Statuses:   splitCSV(c.Query("status")),
		Tag:        strings.TrimSpace(c.Query("tag")),
		Render:     strings.TrimSpace(c.Query("render")),
	}
	if n, err := strconv.Atoi(c.Query("limit")); err == nil && n > 0 {
		f.Limit = n
	}
	if n, err := strconv.ParseUint(c.Query("owner"), 10, 32); err == nil {
		f.OwnerIdentityID = uint(n)
	}
	if v := c.Query("since"); v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			f.UpdatedAfter = t
		}
	}
	return f
}

// ListProjectKnowledge GET /api/v1/projects/:id/docs —— 项目里的全部文档（不含正文），给知识库的目录树用。
// ExportProjectKnowledgeZip GET /api/v1/projects/:id/docs/export.zip
// 整个项目的文档（含已归档）打成一个压缩包；项目内路径唯一，所以不会重名。
func (h *APIHandler) ExportProjectKnowledgeZip(c *gin.Context) {
	id, ok := projectID(c)
	if !ok {
		return
	}
	p, err := h.store.GetProject(c.Request.Context(), id)
	if err != nil {
		respondDocError(c, err)
		return
	}
	items, err := h.store.ListProjectDocs(c.Request.Context(), store.ProjectDocFilter{ProjectID: id, IncludeArchived: true})
	if err != nil {
		respondDocError(c, err)
		return
	}
	h.writeDocsZip(c, "项目 "+p.Name, fmt.Sprintf("project-%d", id), items)
}

func (h *APIHandler) ListProjectKnowledge(c *gin.Context) {
	id, ok := projectID(c)
	if !ok {
		return
	}
	f := projectDocFilters(c, id)
	items, err := h.store.ListProjectDocs(c.Request.Context(), store.ProjectDocFilter{
		ProjectID: id, Kinds: f.Kinds, Statuses: f.Statuses, Tag: f.Tag, ModulePath: f.ModulePath,
		OwnerIdentityID: f.OwnerIdentityID, Render: f.Render, UpdatedAfter: f.UpdatedAfter,
		OmitBody: true, // 下面只取元数据列，不需要正文（#177）
	})
	if err != nil {
		respondDocError(c, err)
		return
	}
	rows := make([]projectDocRow, 0, len(items))
	for _, d := range items {
		rows = append(rows, projectDocRow{
			DocBrief: store.DocBrief{ID: d.ID, WorkflowID: d.WorkflowID, Path: d.Path, Title: d.Title, Kind: d.Kind, Status: d.Status, Render: d.Render, UpdatedAt: d.UpdatedAt},
			Tags:     []string(d.Tags), ModulePath: d.ModulePath,
		})
	}
	c.JSON(http.StatusOK, gin.H{"items": rows})
}

// SearchProjectKnowledge GET /api/v1/projects/:id/docs/search
func (h *APIHandler) SearchProjectKnowledge(c *gin.Context) {
	id, ok := projectID(c)
	if !ok {
		return
	}
	hits, err := h.store.SearchProject(c.Request.Context(), projectDocFilters(c, id))
	if err != nil {
		respondDocError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"items": hits})
}

// ProjectModulePage GET /api/v1/projects/:id/module-page?path=
func (h *APIHandler) ProjectModulePage(c *gin.Context) {
	id, ok := projectID(c)
	if !ok {
		return
	}
	v, err := h.store.ModulePage(c.Request.Context(), id, c.Query("path"))
	if err != nil {
		respondDocError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"page": v})
}

// ProjectBlueprint GET /api/v1/projects/:id/blueprint?templates=1
func (h *APIHandler) ProjectBlueprint(c *gin.Context) {
	id, ok := projectID(c)
	if !ok {
		return
	}
	items, err := h.store.ProjectBlueprint(c.Request.Context(), id, c.Query("templates") == "1")
	if err != nil {
		respondDocError(c, err)
		return
	}
	gaps := 0
	for _, it := range items {
		if !it.Present {
			gaps++
		}
	}
	c.JSON(http.StatusOK, gin.H{"items": items, "gaps": gaps})
}

// WorkflowProject GET /api/v1/workflows/:workflow_id/project —— 这个工作流属于哪个项目（没有就是 null）。
func (h *APIHandler) WorkflowProject(c *gin.Context) {
	p, err := h.store.ProjectOfWorkflow(c.Request.Context(), c.Param("workflow_id"))
	if err != nil {
		respondDocError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"project": p})
}

func auditFilterFrom(c *gin.Context) store.AuditFilter {
	f := store.AuditFilter{
		ActorRef: strings.TrimSpace(c.Query("actor")),
		Action:   strings.TrimSpace(c.Query("action")),
		Result:   strings.TrimSpace(c.Query("result")),
		ScopeRef: strings.TrimSpace(c.Query("scope")),
	}
	if n, err := strconv.ParseUint(c.Query("project"), 10, 32); err == nil {
		f.ProjectID = uint(n)
	}
	if n, err := strconv.Atoi(c.Query("limit")); err == nil && n > 0 {
		f.Limit = n
	}
	if n, err := strconv.ParseUint(c.Query("before"), 10, 32); err == nil {
		f.BeforeID = uint(n)
	}
	if t, err := time.Parse(time.RFC3339, c.Query("since")); err == nil {
		f.Since = t
	}
	if t, err := time.Parse(time.RFC3339, c.Query("until")); err == nil {
		f.Until = t
	}
	return f
}

// ListAudit GET /api/v1/audit
func (h *APIHandler) ListAudit(c *gin.Context) {
	f := auditFilterFrom(c)
	items, err := h.store.ListAuditEvents(c.Request.Context(), f)
	if err != nil {
		respondDocError(c, err)
		return
	}
	next := uint(0)
	if lim := f.Limit; (lim == 0 && len(items) == 100) || (lim > 0 && len(items) == lim) {
		next = items[len(items)-1].ID
	}
	c.JSON(http.StatusOK, gin.H{"items": items, "next_before": next})
}

// csvSafe 防 CSV 公式注入：以 = + - @ 开头的单元格前面加一个单引号。
func csvSafe(s string) string {
	if s != "" && strings.ContainsRune("=+-@\t\r", rune(s[0])) {
		return "'" + s
	}
	return s
}

// ExportAuditCSV GET /api/v1/audit/export.csv（同样的筛选条件，最多 500 条一页，用 before 翻页）。
func (h *APIHandler) ExportAuditCSV(c *gin.Context) {
	f := auditFilterFrom(c)
	if f.Limit == 0 {
		f.Limit = 500
	}
	items, err := h.store.ListAuditEvents(c.Request.Context(), f)
	if err != nil {
		respondDocError(c, err)
		return
	}
	c.Header("Content-Type", "text/csv; charset=utf-8")
	c.Header("Content-Disposition", `attachment; filename="relaymesh-audit.csv"`)
	c.Status(http.StatusOK)
	_, _ = c.Writer.WriteString("\uFEFF") // Excel 识别 UTF-8
	w := csv.NewWriter(c.Writer)
	_ = w.Write([]string{"id", "时间", "项目", "主体类型", "主体", "标签", "动作", "范围类型", "范围", "对象", "结果"})
	for _, e := range items {
		_ = w.Write([]string{
			strconv.Itoa(int(e.ID)), e.At.Format(time.RFC3339), strconv.Itoa(int(e.ProjectID)), e.ActorKind, csvSafe(e.ActorRef), csvSafe(e.ActorLabel),
			e.Action, e.ScopeKind, csvSafe(e.ScopeRef), csvSafe(e.Target), e.Result,
		})
	}
	w.Flush()
}

// ListProjectGrants GET /api/v1/projects/:id/grants?all=1 —— 默认只给有效的授权。
func (h *APIHandler) ListProjectGrants(c *gin.Context) {
	id, ok := projectID(c)
	if !ok {
		return
	}
	items, err := h.store.ListGrants(c.Request.Context(), id, c.Query("all") == "1")
	if err != nil {
		respondDocError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"items": items})
}

// CreateProjectGrant POST /api/v1/projects/:id/grants —— 用户对任意范围授权（read / comment / edit）。
func (h *APIHandler) CreateProjectGrant(c *gin.Context) {
	id, ok := projectID(c)
	if !ok {
		return
	}
	var req struct {
		PrincipalKind string `json:"principal_kind"`
		PrincipalRef  string `json:"principal_ref"`
		ScopeKind     string `json:"scope_kind"`
		ScopeRef      string `json:"scope_ref"`
		Role          string `json:"role"`
		ExpiresHours  int    `json:"expires_hours"`
		Note          string `json:"note"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请求格式无效"})
		return
	}
	ctx := c.Request.Context()
	in := store.GrantInput{
		ProjectID: id, PrincipalKind: req.PrincipalKind, PrincipalRef: req.PrincipalRef,
		ScopeKind: req.ScopeKind, ScopeRef: req.ScopeRef, Role: req.Role,
		GrantedByKind: "human", GrantedByRef: "human", GrantedByLabel: "你", Note: req.Note,
	}
	if req.ExpiresHours > 0 {
		t := time.Now().Add(time.Duration(req.ExpiresHours) * time.Hour)
		in.ExpiresAt = &t
	}
	g, err := h.store.CreateGrant(ctx, in)
	if err != nil {
		respondDocError(c, err)
		return
	}
	h.humanAudit(c, id, model.AuditGrant, g.ScopeKind, g.ScopeRef, "→"+g.PrincipalKind+":"+g.PrincipalRef, map[string]any{"grant_id": g.ID, "role": g.Role, "expires_at": g.ExpiresAt})
	h.notifyGrantee(c, id, g, false)
	c.JSON(http.StatusCreated, gin.H{"grant": g})
}

// RevokeProjectGrant DELETE /api/v1/projects/:id/grants/:grant_id
func (h *APIHandler) RevokeProjectGrant(c *gin.Context) {
	id, ok := projectID(c)
	if !ok {
		return
	}
	gid, err := strconv.ParseUint(c.Param("grant_id"), 10, 32)
	if err != nil || gid == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "授权编号无效"})
		return
	}
	g, err := h.store.RevokeGrant(c.Request.Context(), id, uint(gid))
	if err != nil {
		respondDocError(c, err)
		return
	}
	h.humanAudit(c, id, model.AuditRevoke, g.ScopeKind, g.ScopeRef, "→"+g.PrincipalKind+":"+g.PrincipalRef, map[string]any{"grant_id": g.ID})
	h.notifyGrantee(c, id, g, true)
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// notifyGrantee 给被授权的 Agent 最近活跃的工作流收件箱发一条提醒（群主体不发）；找不到就算了。
func (h *APIHandler) notifyGrantee(c *gin.Context, projectID uint, g *model.ACLGrant, revoked bool) {
	if g.PrincipalKind != model.PrincipalIdentity {
		return
	}
	gi, err := strconv.ParseUint(g.PrincipalRef, 10, 32)
	if err != nil {
		return
	}
	ctx := c.Request.Context()
	wf := h.store.LatestWorkflowOfIdentity(ctx, uint(gi))
	if wf == "" {
		return
	}
	name := ""
	if p, err := h.store.GetProject(ctx, projectID); err == nil {
		name = p.Name
	}
	what := g.ScopeKind + " " + g.ScopeRef
	text := "【授权通知】项目「" + name + "」：你现在对 " + what + " 拥有 " + g.Role + " 权限（操作人：你）"
	if revoked {
		text = "【授权通知】项目「" + name + "」：你对 " + what + " 的 " + g.Role + " 权限已被撤销（操作人：你）"
	} else if g.ExpiresAt != nil {
		text += "，有效期至 " + g.ExpiresAt.Format("2006-01-02 15:04")
	}
	if g.ScopeKind == model.ScopeTasks {
		// 任务管理权（#175 B2c）：说人话——它改变的是「你能对任务板做什么」，不是某个范围的读写。
		if revoked {
			text = "【授权通知】项目「" + name + "」：你不再是项目管理 Agent——只能写自己的任务（自己创建的、或指派给你的），其余任务只读（操作人：你）"
		} else {
			text = "【授权通知】项目「" + name + "」：你被指定为项目管理 Agent——可以用 task_board 的 batch（assignee）把任务派给同一张任务板上的其他工作流（一次至少派 2 个任务，拆成最小的独立块），并能编辑所有人的任务、验收承接者的交付（claim=accept_delivery / reject_delivery）（操作人：你）"
		}
	}
	if _, err := h.store.QueueWorkflowFeedback(ctx, store.QueueFeedbackInput{WorkflowID: wf, ResponseText: text}); err == nil {
		h.broadcastInboxChanged(wf)
	}
}
