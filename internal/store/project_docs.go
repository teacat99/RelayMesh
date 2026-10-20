package store

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/teacat99/RelayMesh/v2/internal/docs"
	"github.com/teacat99/RelayMesh/v2/internal/model"
	"gorm.io/gorm"
)

// DocActor 描述写入者。
type DocActor struct {
	Kind string // human / agent / import
	// IdentityID 是写入者的 Agent 身份（#114）；新建文档时记为负责人，0 = 不记。
	IdentityID uint
}

func (a DocActor) kind() string {
	if a.Kind == model.DocActorAgent || a.Kind == model.DocActorImport {
		return a.Kind
	}
	return model.DocActorHuman
}

// ProjectDocFilter 列表筛选。
type ProjectDocFilter struct {
	WorkflowID      string
	Query           string
	Kinds           []string
	Statuses        []string
	Tag             string
	Prefix          string
	Limit           int
	IncludeArchived bool
	// 项目范围（#114）：ProjectID > 0 时按项目取（跨该项目的所有工作流），此时 WorkflowID 被忽略。
	ProjectID       uint
	ModulePath      string // 模块路径前缀（含子模块）
	OwnerIdentityID uint
	Render          string
	UpdatedAfter    time.Time
	// OmitBody：列表只要元数据（目录树、索引、选择器）时置 true，SQL 不再把正文列读出来（#177：正文是这张表最重的一列，
	// 每次开页面列表都带着全文，生产上一个工作流的文档列表解压后 383 KB）。有 Query 过滤时仍要读正文来打分，此时本项不生效，
	// 查完由调用方自己清空正文（见 api.ListProjectDocs）。返回的 Body 为空串，不代表文档是空的。
	OmitBody bool
}

// ProjectDocInput 新建文档。
type ProjectDocInput struct {
	WorkflowID string
	Path       string
	Title      string
	Kind       string
	Status     string
	Tags       []string
	Body       string
	Summary    string
	// 项目知识库（#114）：空 = 不绑定模块 / 默认渲染。模块必须已在项目里建立。
	ModulePath string
	Render     string
}

// ProjectDocPatch 更新文档。
type ProjectDocPatch struct {
	Title       *string
	Kind        *string
	Status      *string
	Tags        *[]string
	Body        *string
	Path        *string
	ModulePath  *string // 空串 = 解除模块绑定
	Render      *string
	Summary     string
	ExpectedRev int // 0 = 不校验
}

// ProjectDocSaveResult 保存结果。
type ProjectDocSaveResult struct {
	Doc       *model.ProjectDoc
	Changed   bool
	Unchanged bool
	Warnings  []string
}

// DocWorkflowStat 供页头角标。
type DocWorkflowStat struct {
	WorkflowID string    `json:"workflow_id"`
	Total      int       `json:"total"`
	UpdatedAt  time.Time `json:"updated_at"`
}

func validateDocTags(tags []string) ([]string, error) {
	if len(tags) > docs.TagMax {
		return nil, NewInvalidInputError(fmt.Sprintf("标签最多 %d 个", docs.TagMax))
	}
	out := make([]string, 0, len(tags))
	seen := map[string]bool{}
	for _, t := range tags {
		t = strings.TrimSpace(t)
		if t == "" {
			continue
		}
		if utf8.RuneCountInString(t) > docs.TagItemMax {
			return nil, NewInvalidInputError(fmt.Sprintf("单个标签最长 %d 字", docs.TagItemMax))
		}
		if seen[t] {
			continue
		}
		seen[t] = true
		out = append(out, t)
	}
	return out, nil
}

func validateDocBody(body string) error {
	if len(body) > docs.BodyMaxBytes {
		return NewInvalidInputError(fmt.Sprintf("正文不能超过 %d KB", docs.BodyMaxBytes/1024))
	}
	return nil
}

func nextDocSeq(tx *gorm.DB, workflowID string) (int, error) {
	var c model.ProjectDocCounter
	err := tx.Where("workflow_id = ?", workflowID).First(&c).Error
	switch {
	case err == nil:
	case errors.Is(err, gorm.ErrRecordNotFound):
		var max int
		if e := tx.Model(&model.ProjectDoc{}).Where("workflow_id = ?", workflowID).Select("COALESCE(MAX(seq), 0)").Scan(&max).Error; e != nil {
			return 0, e
		}
		c = model.ProjectDocCounter{WorkflowID: workflowID, LastSeq: max}
	default:
		return 0, err
	}
	c.LastSeq++
	if err := tx.Save(&c).Error; err != nil {
		return 0, err
	}
	return c.LastSeq, nil
}

func trimDocRevisions(tx *gorm.DB, docID uint) error {
	var ids []uint
	if err := tx.Model(&model.ProjectDocRevision{}).Where("doc_id = ?", docID).
		Order("rev DESC").Offset(docs.RevisionKeep).Pluck("id", &ids).Error; err != nil {
		return err
	}
	if len(ids) > 0 {
		return tx.Where("id IN ?", ids).Delete(&model.ProjectDocRevision{}).Error
	}
	return nil
}

func countActiveDocs(tx *gorm.DB, workflowID string) (int64, error) {
	var n int64
	err := tx.Model(&model.ProjectDoc{}).Where("workflow_id = ? AND deleted_at IS NULL AND status != ?", workflowID, model.DocStatusArchived).Count(&n).Error
	return n, err
}

// CreateProjectDoc 新建文档。
func (s *Store) CreateProjectDoc(ctx context.Context, in ProjectDocInput, actor DocActor) (*ProjectDocSaveResult, error) {
	path, err := docs.NormalizePath(in.Path)
	if err != nil {
		return nil, NewInvalidInputError(err.Error())
	}
	if err := validateDocBody(in.Body); err != nil {
		return nil, err
	}
	tags, err := validateDocTags(in.Tags)
	if err != nil {
		return nil, err
	}
	kind := model.NormalizeDocKind(in.Kind)
	if kind == "" {
		kind = model.DocKindNote
	}
	status := model.NormalizeDocStatus(in.Status)
	if status == "" {
		status = model.DocStatusDraft
	}
	title := strings.TrimSpace(in.Title)
	if title == "" {
		title = docs.ExtractTitle(in.Body, path)
	}
	warnings := docSaveWarnings(in.Body)
	render := model.NormalizeDocRender(in.Render)
	if render == "" {
		return nil, NewInvalidInputError("渲染类型只能是 md、mermaid、html 或 data")
	}
	// 工作流属于哪个项目：必须在事务外解析（SQLite 单连接，事务里再用 s.db 会互相等待）。
	proj, err := s.ProjectOfWorkflow(ctx, in.WorkflowID)
	if err != nil {
		return nil, err
	}
	var projectID uint
	if proj != nil {
		projectID = proj.ID
	}
	modulePath := ""
	if m := strings.TrimSpace(in.ModulePath); m != "" {
		if projectID == 0 {
			return nil, NewInvalidInputError("这个工作流还没有归属项目，不能绑定模块")
		}
		if modulePath, err = s.requireModule(ctx, projectID, m); err != nil {
			return nil, err
		}
	} else if projectID != 0 {
		modulePath = s.deriveModulePath(ctx, projectID, path)
	}

	var doc model.ProjectDoc
	err = s.WithTx(ctx, func(tx *gorm.DB) error {
		if projectID != 0 {
			if other, err := projectPathOwner(tx, projectID, path, in.WorkflowID, 0); err != nil {
				return err
			} else if other != "" {
				return NewConflictError(fmt.Sprintf("项目里已有这个路径（属于工作流 %s）。换一个路径，或用 sessions/<工作流>/ 放自己的会话文档", other), 0)
			}
		}
		if n, err := countActiveDocs(tx, in.WorkflowID); err != nil {
			return err
		} else if n >= docs.DocsPerWFMax {
			return NewInvalidInputError(fmt.Sprintf("每个工作流最多 %d 篇文档", docs.DocsPerWFMax))
		}
		var exists int64
		if err := tx.Model(&model.ProjectDoc{}).Where("workflow_id = ? AND path = ? AND deleted_at IS NULL", in.WorkflowID, path).Count(&exists).Error; err != nil {
			return err
		}
		if exists > 0 {
			return NewConflictError("该路径已有文档，请用更新或 upsert", 0)
		}
		seq, err := nextDocSeq(tx, in.WorkflowID)
		if err != nil {
			return err
		}
		now := time.Now()
		hash := docs.BodyHash(in.Body)
		doc = model.ProjectDoc{
			WorkflowID:      in.WorkflowID,
			Seq:             seq,
			Path:            path,
			Title:           title,
			Kind:            kind,
			Status:          status,
			Tags:            model.StringArray(tags),
			Body:            in.Body,
			Rev:             1,
			Hash:            hash,
			ProjectID:       projectID,
			ModulePath:      modulePath,
			Render:          render,
			OwnerIdentityID: actor.IdentityID,
			CreatedBy:       actor.kind(),
			UpdatedBy:       actor.kind(),
			CreatedAt:       now,
			UpdatedAt:       now,
		}
		if err := tx.Create(&doc).Error; err != nil {
			return err
		}
		sum := strings.TrimSpace(in.Summary)
		if sum == "" {
			sum = "创建文档"
		}
		return tx.Create(&model.ProjectDocRevision{
			DocID: doc.ID, Rev: 1, Body: in.Body, Summary: sum, Actor: actor.kind(), CreatedAt: now,
		}).Error
	})
	if err != nil {
		return nil, err
	}
	s.syncDocDecisions(ctx, &doc)
	return &ProjectDocSaveResult{Doc: &doc, Changed: true, Warnings: warnings}, nil
}

// syncDocDecisions 文档保存之后对齐决策卡与确认项。失败只记日志、不让保存失败：文档已经落库，
// 下一次保存或下次启动的回填会补齐。
func (s *Store) syncDocDecisions(ctx context.Context, doc *model.ProjectDoc) {
	if doc == nil {
		return
	}
	if _, err := s.SyncDecisionConfirmItems(ctx, doc, doc.Body); err != nil {
		log.Printf("[confirm] 同步决策卡确认项失败（文档 #%d）: %v", doc.ID, err)
	}
}

func docSaveWarnings(body string) []string {
	var w []string
	ds := docs.ParseDecisions(body)
	for _, d := range ds {
		w = append(w, d.Errors...)
	}
	for _, id := range docs.UniqueDecisionIDs(ds) {
		w = append(w, fmt.Sprintf("decision id 重复：%s", id))
	}
	return w
}

// UpdateProjectDoc 更新文档（expected_rev 冲突返回 ErrConflict）。
func (s *Store) UpdateProjectDoc(ctx context.Context, docID uint, patch ProjectDocPatch, actor DocActor) (*ProjectDocSaveResult, error) {
	var result ProjectDocSaveResult
	// 模块与渲染类型的校验要查别的表：在事务外先做（SQLite 单连接，事务里再用 s.db 会互相等待）。
	newModule, moduleSet := "", false
	if patch.ModulePath != nil {
		moduleSet = true
		if m := strings.TrimSpace(*patch.ModulePath); m != "" {
			cur, err := s.GetProjectDocByID(ctx, docID)
			if err != nil {
				return nil, err
			}
			if cur.ProjectID == 0 {
				return nil, NewInvalidInputError("这篇文档还没有归属项目，不能绑定模块")
			}
			if newModule, err = s.requireModule(ctx, cur.ProjectID, m); err != nil {
				return nil, err
			}
		}
	}
	newRender := ""
	if patch.Render != nil {
		if newRender = model.NormalizeDocRender(*patch.Render); newRender == "" {
			return nil, NewInvalidInputError("渲染类型只能是 md、mermaid、html 或 data")
		}
	}
	err := s.WithTx(ctx, func(tx *gorm.DB) error {
		var doc model.ProjectDoc
		if err := tx.First(&doc, docID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return NewNotFoundError("文档不存在")
			}
			return err
		}
		if doc.DeletedAt != nil {
			return NewNotFoundError("文档不存在")
		}
		if patch.ExpectedRev > 0 && doc.Rev != patch.ExpectedRev {
			return NewConflictError(fmt.Sprintf("版本冲突：当前 rev=%d，提交的是 %d", doc.Rev, patch.ExpectedRev), int64(doc.Rev))
		}
		body := doc.Body
		if patch.Body != nil {
			if err := validateDocBody(*patch.Body); err != nil {
				return err
			}
			body = *patch.Body
		}
		newHash := docs.BodyHash(body)
		if newHash == doc.Hash && patch.Title == nil && patch.Kind == nil && patch.Status == nil && patch.Tags == nil && patch.Path == nil && !moduleSet && newRender == "" {
			result.Doc = &doc
			result.Unchanged = true
			result.Warnings = docSaveWarnings(body)
			return nil
		}
		if patch.Path != nil {
			p, err := docs.NormalizePath(*patch.Path)
			if err != nil {
				return NewInvalidInputError(err.Error())
			}
			var exists int64
			if err := tx.Model(&model.ProjectDoc{}).Where("workflow_id = ? AND path = ? AND id != ? AND deleted_at IS NULL", doc.WorkflowID, p, doc.ID).Count(&exists).Error; err != nil {
				return err
			}
			if exists > 0 {
				return NewConflictError("目标路径已被占用", 0)
			}
			if doc.ProjectID != 0 {
				if other, err := projectPathOwner(tx, doc.ProjectID, p, doc.WorkflowID, doc.ID); err != nil {
					return err
				} else if other != "" {
					return NewConflictError(fmt.Sprintf("项目里已有这个路径（属于工作流 %s）", other), 0)
				}
			}
			doc.Path = p
		}
		if patch.Title != nil {
			doc.Title = strings.TrimSpace(*patch.Title)
		}
		if doc.Title == "" {
			doc.Title = docs.ExtractTitle(body, doc.Path)
		}
		if patch.Kind != nil {
			if k := model.NormalizeDocKind(*patch.Kind); k != "" {
				doc.Kind = k
			}
		}
		if patch.Status != nil {
			if st := model.NormalizeDocStatus(*patch.Status); st != "" {
				doc.Status = st
			}
		}
		if patch.Tags != nil {
			tags, err := validateDocTags(*patch.Tags)
			if err != nil {
				return err
			}
			doc.Tags = model.StringArray(tags)
		}
		if moduleSet {
			doc.ModulePath = newModule
		}
		if newRender != "" {
			doc.Render = newRender
		}
		doc.Body = body
		doc.Hash = newHash
		doc.Rev++
		doc.UpdatedBy = actor.kind()
		doc.UpdatedAt = time.Now()
		result.Warnings = docSaveWarnings(body)
		if err := tx.Save(&doc).Error; err != nil {
			return err
		}
		sum := strings.TrimSpace(patch.Summary)
		if sum == "" {
			sum = "更新文档"
		}
		if err := tx.Create(&model.ProjectDocRevision{
			DocID: doc.ID, Rev: doc.Rev, Body: body, Summary: sum, Actor: actor.kind(), CreatedAt: doc.UpdatedAt,
		}).Error; err != nil {
			return err
		}
		if err := trimDocRevisions(tx, doc.ID); err != nil {
			return err
		}
		result.Doc = &doc
		result.Changed = true
		return nil
	})
	if err != nil {
		return nil, err
	}
	if result.Doc != nil && (result.Changed || result.Unchanged) {
		s.syncDocDecisions(ctx, result.Doc)
	}
	return &result, nil
}

// GetProjectDocByID 按主键读取。
func (s *Store) GetProjectDocByID(ctx context.Context, id uint) (*model.ProjectDoc, error) {
	var doc model.ProjectDoc
	if err := s.db.WithContext(ctx).First(&doc, id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, NewNotFoundError("文档不存在")
		}
		return nil, err
	}
	if doc.DeletedAt != nil {
		return nil, NewNotFoundError("文档不存在")
	}
	return &doc, nil
}

// GetProjectDocByRef 按 path、seq 或 id 读取。
func (s *Store) GetProjectDocByRef(ctx context.Context, workflowID, ref string) (*model.ProjectDoc, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return nil, NewInvalidInputError("缺少文档引用")
	}
	if strings.HasPrefix(ref, "d") && len(ref) > 1 {
		if _, err := fmt.Sscanf(ref[1:], "%d", new(int)); err == nil {
			ref = ref[1:]
		}
	}
	q := s.db.WithContext(ctx).Where("workflow_id = ? AND deleted_at IS NULL", workflowID).Session(&gorm.Session{})
	var doc model.ProjectDoc
	tryPath := strings.Contains(ref, "/") || strings.HasSuffix(ref, ".md")
	if tryPath {
		if err := q.Where("path = ?", ref).First(&doc).Error; err == nil {
			return &doc, nil
		}
	}
	var seq int
	if _, err := fmt.Sscanf(ref, "%d", &seq); err == nil && seq > 0 {
		if err := q.Where("seq = ?", seq).First(&doc).Error; err == nil {
			return &doc, nil
		}
		if !tryPath {
			if err := q.Where("id = ?", seq).First(&doc).Error; err == nil {
				return &doc, nil
			}
		}
	}
	if !tryPath {
		if err := q.Where("path = ?", ref).First(&doc).Error; err == nil {
			return &doc, nil
		}
	}
	return nil, NewNotFoundError("文档不存在")
}

// ListProjectDocs 列表。
func (s *Store) ListProjectDocs(ctx context.Context, f ProjectDocFilter) ([]model.ProjectDoc, error) {
	q := s.db.WithContext(ctx).Where("workflow_id = ? AND deleted_at IS NULL", f.WorkflowID)
	if f.ProjectID > 0 {
		q = s.db.WithContext(ctx).Where("project_id = ? AND deleted_at IS NULL", f.ProjectID)
	}
	if f.ModulePath != "" {
		m := strings.Trim(f.ModulePath, "/")
		q = q.Where("module_path = ? OR module_path LIKE ?", m, m+"/%")
	}
	if f.OwnerIdentityID > 0 {
		q = q.Where("owner_identity_id = ?", f.OwnerIdentityID)
	}
	if f.Render != "" {
		q = q.Where("render = ?", f.Render)
	}
	if !f.UpdatedAfter.IsZero() {
		q = q.Where("updated_at > ?", f.UpdatedAfter)
	}
	if !f.IncludeArchived {
		q = q.Where("status != ?", model.DocStatusArchived)
	}
	if len(f.Kinds) > 0 {
		q = q.Where("kind IN ?", f.Kinds)
	}
	if len(f.Statuses) > 0 {
		q = q.Where("status IN ?", f.Statuses)
	}
	if f.Tag != "" {
		q = q.Where("tags LIKE ?", "%\""+f.Tag+"\"%")
	}
	if f.Prefix != "" {
		p := strings.TrimSuffix(f.Prefix, "/")
		q = q.Where("path = ? OR path LIKE ?", p, p+"/%")
	}
	limit := f.Limit
	maxN := docs.DocsPerWFMax
	if f.ProjectID > 0 {
		maxN = docs.DocsPerProjectMax
	}
	if limit <= 0 || limit > maxN {
		limit = maxN
	}
	if f.OmitBody && strings.TrimSpace(f.Query) == "" {
		q = q.Omit("body")
	}
	var docsList []model.ProjectDoc
	if err := q.Order("path ASC").Limit(limit).Find(&docsList).Error; err != nil {
		return nil, err
	}
	if f.Query != "" {
		ql := strings.ToLower(strings.TrimSpace(f.Query))
		var filtered []model.ProjectDoc
		for _, d := range docsList {
			score, _, _ := docs.ScoreDocument(ql, d.Path, d.Title, d.Body, d.Tags)
			if score > 0 || strings.Contains(strings.ToLower(d.Path), ql) {
				filtered = append(filtered, d)
			}
		}
		docsList = filtered
	}
	return docsList, nil
}

// SearchProjectDocs 搜索（打分排序）。
func (s *Store) SearchProjectDocs(ctx context.Context, workflowID, query string, limit int) ([]docs.SearchHit, error) {
	all, err := s.ListProjectDocs(ctx, ProjectDocFilter{WorkflowID: workflowID, Limit: docs.DocsPerWFMax})
	if err != nil {
		return nil, err
	}
	ql := strings.ToLower(strings.TrimSpace(query))
	if ql == "" {
		return nil, nil
	}
	var hits []docs.SearchHit
	for _, d := range all {
		score, snip, anchor := docs.ScoreDocument(ql, d.Path, d.Title, d.Body, d.Tags)
		if score <= 0 {
			continue
		}
		hits = append(hits, docs.SearchHit{
			Path: d.Path, Title: d.Title, Score: score, Snippet: snip, Anchor: anchor,
		})
	}
	sort.Slice(hits, func(i, j int) bool { return hits[i].Score > hits[j].Score })
	if limit <= 0 {
		limit = 30
	}
	if len(hits) > limit {
		hits = hits[:limit]
	}
	return hits, nil
}

// ArchiveProjectDoc 归档（MCP delete 只归档）。
func (s *Store) ArchiveProjectDoc(ctx context.Context, docID uint, actor DocActor) (*model.ProjectDoc, error) {
	var doc model.ProjectDoc
	err := s.WithTx(ctx, func(tx *gorm.DB) error {
		if err := tx.First(&doc, docID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return NewNotFoundError("文档不存在")
			}
			return err
		}
		doc.Status = model.DocStatusArchived
		doc.UpdatedBy = actor.kind()
		doc.UpdatedAt = time.Now()
		return tx.Save(&doc).Error
	})
	if err == nil {
		_ = s.WithdrawDocPendingConfirms(ctx, docID)
	}
	return &doc, err
}

// SoftDeleteProjectDoc 硬删（REST delete）。
func (s *Store) SoftDeleteProjectDoc(ctx context.Context, docID uint) error {
	now := time.Now()
	if err := s.db.WithContext(ctx).Model(&model.ProjectDoc{}).Where("id = ?", docID).Update("deleted_at", now).Error; err != nil {
		return err
	}
	return s.WithdrawDocPendingConfirms(ctx, docID)
}

// ListProjectDocRevisions 修订历史。
func (s *Store) ListProjectDocRevisions(ctx context.Context, docID uint) ([]model.ProjectDocRevision, error) {
	var revs []model.ProjectDocRevision
	err := s.db.WithContext(ctx).Where("doc_id = ?", docID).Order("rev DESC").Find(&revs).Error
	return revs, err
}

// GetProjectDocRevision 某一版。
func (s *Store) GetProjectDocRevision(ctx context.Context, docID uint, rev int) (*model.ProjectDocRevision, error) {
	var r model.ProjectDocRevision
	err := s.db.WithContext(ctx).Where("doc_id = ? AND rev = ?", docID, rev).First(&r).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, NewNotFoundError("修订不存在")
	}
	return &r, err
}

// RestoreProjectDocRevision 还原为新修订。
func (s *Store) RestoreProjectDocRevision(ctx context.Context, docID uint, rev int, actor DocActor) (*ProjectDocSaveResult, error) {
	r, err := s.GetProjectDocRevision(ctx, docID, rev)
	if err != nil {
		return nil, err
	}
	body := r.Body
	sum := fmt.Sprintf("从 rev %d 还原", rev)
	return s.UpdateProjectDoc(ctx, docID, ProjectDocPatch{Body: &body, Summary: sum}, actor)
}

// ImportProjectDocs 批量导入。
type ImportDocFile struct {
	Path    string
	Content string
	Kind    string
	Status  string
	Tags    []string
	Title   string
}

type ImportDocResult struct {
	Created   int
	Updated   int
	Unchanged int
	Errors    []string
}

func (s *Store) ImportProjectDocs(ctx context.Context, workflowID string, files []ImportDocFile, mode string, dryRun bool, actor DocActor) (*ImportDocResult, error) {
	if len(files) > docs.ImportMaxFiles {
		return nil, NewInvalidInputError(fmt.Sprintf("单次导入最多 %d 篇", docs.ImportMaxFiles))
	}
	totalBytes := 0
	for _, f := range files {
		totalBytes += len(f.Content)
	}
	if totalBytes > docs.ImportMaxBytes {
		return nil, NewInvalidInputError("导入内容超过 2 MB 上限")
	}
	res := &ImportDocResult{}
	for _, f := range files {
		path, err := docs.NormalizePath(f.Path)
		if err != nil {
			res.Errors = append(res.Errors, fmt.Sprintf("%s: %v", f.Path, err))
			continue
		}
		existing, _ := s.GetProjectDocByRef(ctx, workflowID, path)
		if existing == nil {
			if mode == "upsert" || mode == "create" || mode == "" {
				if dryRun {
					res.Created++
					continue
				}
				_, err := s.CreateProjectDoc(ctx, ProjectDocInput{
					WorkflowID: workflowID, Path: path, Body: f.Content,
					Kind: f.Kind, Status: f.Status, Tags: f.Tags, Title: f.Title,
				}, actor)
				if err != nil {
					res.Errors = append(res.Errors, err.Error())
				} else {
					res.Created++
				}
			}
			continue
		}
		if mode == "create" {
			res.Errors = append(res.Errors, fmt.Sprintf("%s: 已存在", path))
			continue
		}
		if docs.BodyHash(f.Content) == existing.Hash {
			res.Unchanged++
			continue
		}
		if dryRun {
			res.Updated++
			continue
		}
		body := f.Content
		if _, errUp := s.UpdateProjectDoc(ctx, existing.ID, ProjectDocPatch{Body: &body, Summary: "导入更新"}, actor); errUp != nil {
			res.Errors = append(res.Errors, errUp.Error())
		} else {
			res.Updated++
		}
	}
	return res, nil
}

func parseDocAggUpdatedAt(raw string) time.Time {
	if raw == "" {
		return time.Time{}
	}
	for _, layout := range []string{
		time.RFC3339Nano,
		"2006-01-02 15:04:05.999999999-07:00",
		"2006-01-02 15:04:05-07:00",
		"2006-01-02 15:04:05",
	} {
		if t, err := time.Parse(layout, raw); err == nil {
			return t
		}
	}
	return time.Time{}
}

// ListDocWorkflowStats 各工作流文档数。
func (s *Store) ListDocWorkflowStats(ctx context.Context) ([]DocWorkflowStat, error) {
	var rows []struct {
		WorkflowID string
		Total      int64
		UpdatedAt  string
	}
	err := s.db.WithContext(ctx).Model(&model.ProjectDoc{}).
		Select("workflow_id, COUNT(*) as total, MAX(updated_at) as updated_at").
		Where("deleted_at IS NULL AND status != ?", model.DocStatusArchived).
		Group("workflow_id").Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	out := make([]DocWorkflowStat, len(rows))
	for i, r := range rows {
		out[i] = DocWorkflowStat{
			WorkflowID: r.WorkflowID,
			Total:      int(r.Total),
			UpdatedAt:  parseDocAggUpdatedAt(r.UpdatedAt),
		}
	}
	return out, nil
}

// CountProjectDocs 工作流内文档数。
func (s *Store) CountProjectDocs(ctx context.Context, workflowID string) (int, error) {
	n, err := countActiveDocs(s.db.WithContext(ctx), workflowID)
	return int(n), err
}

// projectPathOwner 返回项目里占着这个路径的「另一个工作流」的编号；没人占返回空串。excludeID 是正在改的那篇。
func projectPathOwner(tx *gorm.DB, projectID uint, path, workflowID string, excludeID uint) (string, error) {
	var other model.ProjectDoc
	err := tx.Select("workflow_id").
		Where("project_id = ? AND path = ? AND workflow_id <> ? AND id <> ? AND deleted_at IS NULL", projectID, path, workflowID, excludeID).
		Limit(1).Take(&other).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return other.WorkflowID, nil
}

// requireModule 校验模块已在项目里建立，返回规整后的模块路径。
func (s *Store) requireModule(ctx context.Context, projectID uint, raw string) (string, error) {
	p, err := NormalizeModulePath(raw)
	if err != nil {
		return "", NewInvalidInputError(err.Error())
	}
	var n int64
	if err := s.db.WithContext(ctx).Model(&model.ProjectModule{}).Where("project_id = ? AND path = ?", projectID, p).Count(&n).Error; err != nil {
		return "", err
	}
	if n == 0 {
		return "", NewInvalidInputError("模块「" + p + "」还没有建立：先在项目的模块树里添加")
	}
	return p, nil
}

// deriveModulePath 文档路径形如 modules/<模块路径>/…，且该模块已建立时，自动绑定到「最长匹配」的模块；否则返回空串。
func (s *Store) deriveModulePath(ctx context.Context, projectID uint, docPath string) string {
	rest, ok := strings.CutPrefix(docPath, "modules/")
	if !ok {
		return ""
	}
	segs := strings.Split(rest, "/")
	if len(segs) < 2 { // modules/x.md 不是模块下的文档
		return ""
	}
	dirs := segs[:len(segs)-1]
	for i := len(dirs); i >= 1; i-- {
		cand := strings.Join(dirs[:i], "/")
		var n int64
		if err := s.db.WithContext(ctx).Model(&model.ProjectModule{}).Where("project_id = ? AND path = ?", projectID, cand).Count(&n).Error; err == nil && n > 0 {
			return cand
		}
	}
	return ""
}

// GetProjectDocInProject 在整个项目里按路径或文档编号（全局 id，可带前缀 d）取一篇文档。
// 项目内路径唯一（只对 project_id>0 的文档由应用层保证），所以按路径取到的是唯一一篇。
func (s *Store) GetProjectDocInProject(ctx context.Context, projectID uint, ref string) (*model.ProjectDoc, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return nil, NewInvalidInputError("缺少文档引用")
	}
	// Session 让 q 成为可复用的起点：否则第二次 Where 会叠加在第一次的条件上。
	q := s.db.WithContext(ctx).Where("project_id = ? AND deleted_at IS NULL", projectID).Session(&gorm.Session{})
	var doc model.ProjectDoc
	if err := q.Where("path = ?", ref).First(&doc).Error; err == nil {
		return &doc, nil
	}
	num := strings.TrimPrefix(ref, "d")
	if id, err := strconv.ParseUint(num, 10, 32); err == nil && id > 0 {
		if err := q.Where("id = ?", id).First(&doc).Error; err == nil {
			return &doc, nil
		}
	}
	return nil, NewNotFoundError("项目里没有这篇文档：" + ref)
}
