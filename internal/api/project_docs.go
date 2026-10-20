package api

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/teacat99/RelayMesh/v2/internal/confirm"
	docpkg "github.com/teacat99/RelayMesh/v2/internal/docs"
	"github.com/teacat99/RelayMesh/v2/internal/model"
	"github.com/teacat99/RelayMesh/v2/internal/store"
)

// docPathParam 从通配路由 *docpath 读取文档路径（如 design/e2e.md）。
func docPathParam(c *gin.Context) string {
	if q := strings.TrimSpace(c.Query("path")); q != "" {
		return q
	}
	raw := strings.TrimSpace(c.Param("doc"))
	if raw == "" {
		raw = strings.TrimPrefix(c.Param("docpath"), "/")
	}
	dec, err := url.PathUnescape(raw)
	if err != nil {
		return raw
	}
	return dec
}

func respondDocError(c *gin.Context, err error) {
	var ae *store.AppError
	if errors.As(err, &ae) {
		code := http.StatusInternalServerError
		switch ae.Kind {
		case store.ErrNotFound:
			code = http.StatusNotFound
		case store.ErrInvalidInput:
			code = http.StatusBadRequest
		case store.ErrConflict:
			code = http.StatusConflict
		}
		body := gin.H{"error": ae.Message}
		if ae.CurrentRevision > 0 {
			body["current_rev"] = ae.CurrentRevision
		}
		c.JSON(code, body)
		return
	}
	c.JSON(http.StatusInternalServerError, gin.H{"error": "操作失败，请稍后重试"})
}

func (h *APIHandler) broadcastProjectDoc(doc *model.ProjectDoc, deleted bool) {
	if h.broker == nil || doc == nil {
		return
	}
	h.broker.Broadcast("project_doc_updated", gin.H{
		"id": doc.ID, "workflow_id": doc.WorkflowID, "path": doc.Path, "deleted": deleted,
	})
	// 保存 / 归档 / 删除文档会让决策卡对应的确认项同步变化，待确认区要跟着刷新。
	h.broadcastConfirmUpdated(doc.WorkflowID, nil)
}

func (h *APIHandler) docDetail(ctx context.Context, doc *model.ProjectDoc, body string, historic bool) gin.H {
	return gin.H{
		"doc":       doc,
		"body":      body,
		"headings":  docpkg.ParseHeadings(body),
		"decisions": h.store.DocDecisionViews(ctx, doc, body, historic),
		"historic":  historic,
	}
}

// ListProjectDocs GET /api/v1/workflows/:workflow_id/docs
//
// 默认只返回文档元数据，不带正文（items[].body 为空串，响应里 body_omitted=true）：列表的使用方（目录树、链接索引、选择器）
// 从来不读正文，而正文是这张表最重的一列，每次开页面都带着全文太浪费（#177）。要正文请加 ?with_body=1，
// 或按路径取单篇（GET .../docs/content?path=…，那里一直带正文）。
func (h *APIHandler) ListProjectDocs(c *gin.Context) {
	wf := c.Param("workflow_id")
	withBody := queryTruthy(c.Query("with_body"))
	f := store.ProjectDocFilter{
		WorkflowID: wf,
		Query:      c.Query("q"),
		Kinds:      splitCSV(c.Query("kind")),
		Statuses:   splitCSV(c.Query("status")),
		Tag:        strings.TrimSpace(c.Query("tag")),
		Prefix:     strings.TrimSpace(c.Query("prefix")),
		OmitBody:   !withBody,
	}
	items, err := h.store.ListProjectDocs(c.Request.Context(), f)
	if err != nil {
		respondDocError(c, err)
		return
	}
	if !withBody {
		// 带 q= 搜索时 store 仍读了正文来打分，这里统一清掉，保证「不带 with_body 就没有正文」。
		for i := range items {
			items[i].Body = ""
		}
	}
	stats, err := h.store.ListDocWorkflowStats(c.Request.Context())
	if err != nil {
		log.Printf("[project_docs] 工作流文档统计查询失败")
		stats = []store.DocWorkflowStat{}
	}
	c.JSON(http.StatusOK, gin.H{"items": items, "workflows": stats, "body_omitted": !withBody})
}

// queryTruthy 把 ?flag=1 / true / yes 视为开（大小写不敏感），其余（含缺省）为关。
func queryTruthy(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes":
		return true
	}
	return false
}

// CreateProjectDoc POST /api/v1/workflows/:workflow_id/docs
func (h *APIHandler) CreateProjectDoc(c *gin.Context) {
	wf := c.Param("workflow_id")
	var req struct {
		Path   string   `json:"path"`
		Title  string   `json:"title"`
		Kind   string   `json:"kind"`
		Status string   `json:"status"`
		Tags   []string `json:"tags"`
		Body   string   `json:"body"`
		// 项目知识库（#114）：模块必须已在项目里建立。
		ModulePath string `json:"module_path"`
		Render     string `json:"render"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请求格式无效"})
		return
	}
	res, err := h.store.CreateProjectDoc(c.Request.Context(), store.ProjectDocInput{
		WorkflowID: wf, Path: req.Path, Title: req.Title, Kind: req.Kind,
		Status: req.Status, Tags: req.Tags, Body: req.Body, ModulePath: req.ModulePath, Render: req.Render,
	}, store.DocActor{Kind: model.DocActorHuman})
	if err != nil {
		respondDocError(c, err)
		return
	}
	h.broadcastProjectDoc(res.Doc, false)
	c.JSON(http.StatusCreated, gin.H{"doc": res.Doc, "warnings": res.Warnings})
}

// GetProjectDoc GET /api/v1/workflows/:workflow_id/docs/:doc
func (h *APIHandler) GetProjectDoc(c *gin.Context) {
	wf := c.Param("workflow_id")
	ref := docPathParam(c)
	doc, err := h.store.GetProjectDocByRef(c.Request.Context(), wf, ref)
	if err != nil {
		respondDocError(c, err)
		return
	}
	body := doc.Body
	historic := false
	if revStr := c.Query("rev"); revStr != "" {
		rev, _ := strconv.Atoi(revStr)
		if rev > 0 && rev != doc.Rev {
			r, err := h.store.GetProjectDocRevision(c.Request.Context(), doc.ID, rev)
			if err != nil {
				respondDocError(c, err)
				return
			}
			body, historic = r.Body, true
		}
	}
	c.JSON(http.StatusOK, h.docDetail(c.Request.Context(), doc, body, historic))
}

// GetProjectDocLinks GET /api/v1/workflows/:workflow_id/docs/content/links?path=
// 一篇文档的出链（含目标是否存在）与反链。
func (h *APIHandler) GetProjectDocLinks(c *gin.Context) {
	doc, err := h.store.GetProjectDocByRef(c.Request.Context(), c.Param("workflow_id"), docPathParam(c))
	if err != nil {
		respondDocError(c, err)
		return
	}
	l, err := h.store.DocLinks(c.Request.Context(), doc)
	if err != nil {
		respondDocError(c, err)
		return
	}
	c.JSON(http.StatusOK, l)
}

// UpdateProjectDoc PUT /api/v1/workflows/:workflow_id/docs/:doc
func (h *APIHandler) UpdateProjectDoc(c *gin.Context) {
	wf := c.Param("workflow_id")
	ref := docPathParam(c)
	doc, err := h.store.GetProjectDocByRef(c.Request.Context(), wf, ref)
	if err != nil {
		respondDocError(c, err)
		return
	}
	var req struct {
		Title       *string   `json:"title"`
		Kind        *string   `json:"kind"`
		Status      *string   `json:"status"`
		Tags        *[]string `json:"tags"`
		Body        *string   `json:"body"`
		Path        *string   `json:"path"`
		ModulePath  *string   `json:"module_path"`
		Render      *string   `json:"render"`
		ExpectedRev int       `json:"expected_rev"`
		Summary     string    `json:"summary"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请求格式无效"})
		return
	}
	res, err := h.store.UpdateProjectDoc(c.Request.Context(), doc.ID, store.ProjectDocPatch{
		Title: req.Title, Kind: req.Kind, Status: req.Status, Tags: req.Tags,
		Body: req.Body, Path: req.Path, ModulePath: req.ModulePath, Render: req.Render, ExpectedRev: req.ExpectedRev, Summary: req.Summary,
	}, store.DocActor{Kind: model.DocActorHuman})
	if err != nil {
		respondDocError(c, err)
		return
	}
	if res.Unchanged {
		c.JSON(http.StatusOK, gin.H{"unchanged": true, "doc": res.Doc, "warnings": res.Warnings})
		return
	}
	h.broadcastProjectDoc(res.Doc, false)
	c.JSON(http.StatusOK, gin.H{"doc": res.Doc, "warnings": res.Warnings})
}

// DeleteProjectDoc DELETE /api/v1/workflows/:workflow_id/docs/:doc
func (h *APIHandler) DeleteProjectDoc(c *gin.Context) {
	wf := c.Param("workflow_id")
	ref := docPathParam(c)
	doc, err := h.store.GetProjectDocByRef(c.Request.Context(), wf, ref)
	if err != nil {
		respondDocError(c, err)
		return
	}
	if err := h.store.SoftDeleteProjectDoc(c.Request.Context(), doc.ID); err != nil {
		respondDocError(c, err)
		return
	}
	h.broadcastProjectDoc(doc, true)
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// ArchiveProjectDoc POST /api/v1/workflows/:workflow_id/docs/:doc/archive
func (h *APIHandler) ArchiveProjectDoc(c *gin.Context) {
	wf := c.Param("workflow_id")
	ref := docPathParam(c)
	doc, err := h.store.GetProjectDocByRef(c.Request.Context(), wf, ref)
	if err != nil {
		respondDocError(c, err)
		return
	}
	out, err := h.store.ArchiveProjectDoc(c.Request.Context(), doc.ID, store.DocActor{Kind: model.DocActorHuman})
	if err != nil {
		respondDocError(c, err)
		return
	}
	h.broadcastProjectDoc(out, false)
	c.JSON(http.StatusOK, gin.H{"doc": out})
}

// SearchProjectDocs GET /api/v1/workflows/:workflow_id/docs/search
func (h *APIHandler) SearchProjectDocs(c *gin.Context) {
	wf := c.Param("workflow_id")
	q := c.Query("q")
	hits, err := h.store.SearchProjectDocs(c.Request.Context(), wf, q, 50)
	if err != nil {
		respondDocError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"hits": hits})
}

// ListProjectDocRevisions GET /api/v1/workflows/:workflow_id/docs/:doc/revisions
func (h *APIHandler) ListProjectDocRevisions(c *gin.Context) {
	wf := c.Param("workflow_id")
	ref := docPathParam(c)
	doc, err := h.store.GetProjectDocByRef(c.Request.Context(), wf, ref)
	if err != nil {
		respondDocError(c, err)
		return
	}
	revs, err := h.store.ListProjectDocRevisions(c.Request.Context(), doc.ID)
	if err != nil {
		respondDocError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"revisions": revs})
}

// RestoreProjectDocRevision POST /api/v1/workflows/:workflow_id/docs/:doc/revisions/:rev/restore
func (h *APIHandler) RestoreProjectDocRevision(c *gin.Context) {
	wf := c.Param("workflow_id")
	ref := c.Param("doc")
	rev, _ := strconv.Atoi(c.Param("rev"))
	doc, err := h.store.GetProjectDocByRef(c.Request.Context(), wf, ref)
	if err != nil {
		respondDocError(c, err)
		return
	}
	res, err := h.store.RestoreProjectDocRevision(c.Request.Context(), doc.ID, rev, store.DocActor{Kind: model.DocActorHuman})
	if err != nil {
		respondDocError(c, err)
		return
	}
	h.broadcastProjectDoc(res.Doc, false)
	c.JSON(http.StatusOK, gin.H{"doc": res.Doc})
}

// PostProjectDocComment POST /api/v1/workflows/:workflow_id/docs/:doc/comments
func (h *APIHandler) PostProjectDocComment(c *gin.Context) {
	wf := c.Param("workflow_id")
	ref := docPathParam(c)
	doc, err := h.store.GetProjectDocByRef(c.Request.Context(), wf, ref)
	if err != nil {
		respondDocError(c, err)
		return
	}
	var req struct {
		Anchor string `json:"anchor"`
		Text   string `json:"text"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || strings.TrimSpace(req.Text) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请填写评论内容"})
		return
	}
	anchor := strings.TrimSpace(req.Anchor)
	msg := fmt.Sprintf("【文档评论】《%s》", doc.Title)
	if anchor != "" {
		msg += "#" + anchor
	}
	msg += "：" + strings.TrimSpace(req.Text)
	_, err = h.store.QueueWorkflowFeedback(c.Request.Context(), store.QueueFeedbackInput{
		WorkflowID: wf, ResponseText: msg,
	})
	if err != nil {
		respondDocError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// ExportProjectDocsZip GET /api/v1/workflows/:workflow_id/docs/export.zip
func (h *APIHandler) ExportProjectDocsZip(c *gin.Context) {
	wf := c.Param("workflow_id")
	items, err := h.store.ListProjectDocs(c.Request.Context(), store.ProjectDocFilter{WorkflowID: wf, IncludeArchived: true})
	if err != nil {
		respondDocError(c, err)
		return
	}
	h.writeDocsZip(c, wf, wf, items)
}

// writeDocsZip 把一组文档打成压缩包写给客户端（工作流导出与项目导出共用）：
// label 写进 INDEX.md 的抬头，fileLabel 用于下载文件名。
func (h *APIHandler) writeDocsZip(c *gin.Context, label, fileLabel string, items []model.ProjectDoc) {
	var entries []docpkg.ExportEntry
	buf := new(bytes.Buffer)
	zw := zip.NewWriter(buf)
	indexName := "INDEX.md"
	for _, d := range items {
		// 压缩包里的文件名在导出时再规整一次：库里理论上只有已校验的路径，但即便被脏写进来
		// 一条带 ../ 或绝对路径的，也不能让它变成会逃出解压目录的 zip 条目（zip slip 在解压方发生）。
		name, nerr := docpkg.NormalizePath(d.Path)
		if nerr != nil {
			continue
		}
		if strings.EqualFold(name, indexName) {
			indexName = "INDEX.generated.md" // 不与文档自己的 INDEX.md 重名
		}
		rows := h.store.DocDecisionExportRows(c.Request.Context(), &d)
		content := docpkg.AppendDecisionResultsTable(d.Body, rows)
		fw, err := zw.Create(name)
		if err != nil {
			respondDocError(c, err)
			return
		}
		_, _ = fw.Write([]byte(content))
		entries = append(entries, docpkg.ExportEntry{
			Path: name, Title: d.Title, Kind: d.Kind, Status: d.Status,
			Rev: d.Rev, Hash: confirm.ExportHash(d.Hash, rows), Updated: d.UpdatedAt.Format("2006-01-02"),
		})
	}
	idx := docpkg.BuildIndexMarkdown(label, entries)
	iw, _ := zw.Create(indexName)
	_, _ = iw.Write([]byte(idx))
	_ = zw.Close()
	c.Header("Content-Type", "application/zip")
	c.Header("Content-Disposition", fmt.Sprintf("attachment; filename=\"%s-docs.zip\"", safeDownloadName(fileLabel)))
	c.Data(http.StatusOK, "application/zip", buf.Bytes())
}

// GetProjectDocsBlueprint GET /api/v1/workflows/:workflow_id/docs/blueprint
func (h *APIHandler) GetProjectDocsBlueprint(c *gin.Context) {
	wf := c.Param("workflow_id")
	items, _ := h.store.ListProjectDocs(c.Request.Context(), store.ProjectDocFilter{WorkflowID: wf})
	paths := map[string]bool{}
	for _, d := range items {
		paths[d.Path] = true
	}
	withT := c.Query("with_templates") == "1" || c.Query("with_templates") == "true"
	bp := docpkg.Blueprint(paths, withT)
	c.JSON(http.StatusOK, gin.H{
		"items": bp, "gaps": docpkg.CountBlueprintGaps(bp),
	})
}

// safeDownloadName 把工作流标识收成只含 [A-Za-z0-9._-] 的下载文件名片段（其余字符换成下划线），
// 避免它里面的引号等字符破坏 Content-Disposition 的写法。
func safeDownloadName(raw string) string {
	var b strings.Builder
	for _, r := range raw {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	out := b.String()
	if out == "" || len(out) > 80 {
		return "workflow"
	}
	return out
}
