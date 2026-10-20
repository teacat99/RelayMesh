package store

import (
	"context"
	"sort"

	"github.com/teacat99/RelayMesh/v2/internal/docs"
	"github.com/teacat99/RelayMesh/v2/internal/model"
)

// DocLinkRef 是链接关系里的一端：出链指向的文档（可能不存在），或反链来自的文档。
type DocLinkRef struct {
	Target     string `json:"target,omitempty"` // 出链：正文里写的目标（已规范化路径）
	ID         uint   `json:"id,omitempty"`
	WorkflowID string `json:"workflow_id,omitempty"`
	Path       string `json:"path"`
	Title      string `json:"title,omitempty"`
	Exists     bool   `json:"exists"`
}

// DocLinks 是一篇文档的出链与反链。
type DocLinks struct {
	Outlinks  []DocLinkRef `json:"outlinks"`
	Backlinks []DocLinkRef `json:"backlinks"`
}

// DocLinks 解析一篇文档的出链与反链（解析 [[path]] 与相对 .md 链接，见 docs.ExtractLinks）。
// 解析范围：同一工作流的文档；文档属于项目时再加上同项目的其他文档（同路径时本工作流优先）。
// 不落库：文档数量有上限（每工作流 500、每项目 2000），按需扫描正文即可，也就不会有「索引与正文不一致」的问题。
func (s *Store) DocLinks(ctx context.Context, doc *model.ProjectDoc) (*DocLinks, error) {
	q := s.db.WithContext(ctx).Where("deleted_at IS NULL")
	if doc.ProjectID > 0 {
		q = q.Where("workflow_id = ? OR project_id = ?", doc.WorkflowID, doc.ProjectID)
	} else {
		q = q.Where("workflow_id = ?", doc.WorkflowID)
	}
	var all []model.ProjectDoc
	if err := q.Select("id", "workflow_id", "path", "title", "body", "project_id").Find(&all).Error; err != nil {
		return nil, err
	}
	// 路径 → 文档：本工作流优先
	byPath := map[string]*model.ProjectDoc{}
	for i := range all {
		d := &all[i]
		if cur, ok := byPath[d.Path]; ok && cur.WorkflowID == doc.WorkflowID {
			continue
		}
		if _, ok := byPath[d.Path]; !ok || d.WorkflowID == doc.WorkflowID {
			byPath[d.Path] = d
		}
	}
	res := &DocLinks{Outlinks: []DocLinkRef{}, Backlinks: []DocLinkRef{}}
	for _, target := range docs.ExtractLinks(doc.Path, doc.Body) {
		ref := DocLinkRef{Target: target, Path: target}
		if d, ok := byPath[target]; ok {
			ref.ID, ref.WorkflowID, ref.Title, ref.Exists = d.ID, d.WorkflowID, d.Title, true
		}
		res.Outlinks = append(res.Outlinks, ref)
	}
	for i := range all {
		d := &all[i]
		if d.ID == doc.ID {
			continue
		}
		// 反链按「这篇文档解析到的那个路径」算：同路径文档只有 byPath 里胜出的才算
		if byPath[doc.Path] != nil && byPath[doc.Path].ID != doc.ID {
			continue
		}
		for _, t := range docs.ExtractLinks(d.Path, d.Body) {
			if t == doc.Path {
				res.Backlinks = append(res.Backlinks, DocLinkRef{ID: d.ID, WorkflowID: d.WorkflowID, Path: d.Path, Title: d.Title, Exists: true})
				break
			}
		}
	}
	sort.Slice(res.Backlinks, func(i, j int) bool { return res.Backlinks[i].Path < res.Backlinks[j].Path })
	return res, nil
}
