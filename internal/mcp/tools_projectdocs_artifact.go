package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/teacat99/RelayMesh/v2/internal/acl"
	"github.com/teacat99/RelayMesh/v2/internal/model"
	"github.com/teacat99/RelayMesh/v2/internal/store"
)

// Artifact 相关的 project_docs 动作：数据键值（data_*）与评论（comments / comment / comment_resolve）。
// 文档用 path 或 id 指定；带 scope=project 时在项目知识库里找，并按文档级判定（read / comment / edit）。
func isArtifactAction(a string) bool {
	switch a {
	case "data_get", "data_set", "data_list", "data_delete", "comments", "comment", "comment_resolve":
		return true
	}
	return false
}

func artifactNeed(action string) acl.Role {
	switch action {
	case "data_get", "data_list", "comments":
		return acl.RoleRead
	case "comment":
		return acl.RoleComment
	}
	return acl.RoleEdit
}

func (s *Server) projectDocsArtifact(ctx context.Context, a projectDocsArgs, action string) (any, error) {
	ref := firstNonEmpty(a.Path, a.ID)
	if ref == "" {
		return nil, fmt.Errorf("%s 需要 path 或 id（指向那篇文档）", action)
	}
	var (
		doc *model.ProjectDoc
		p   *model.Project
		err error
	)
	if a.Scope == "project" {
		if p, err = s.resolveProjectForDocs(ctx, a); err != nil {
			return nil, err
		}
		if doc, err = s.store.GetProjectDocInProject(ctx, p.ID, ref); err != nil {
			return nil, err
		}
		if err := s.aclDocAccess(ctx, p, a, doc, doc.Path, artifactNeed(action)); err != nil {
			return nil, err
		}
	} else {
		if doc, err = s.store.GetProjectDocByRef(ctx, a.WorkflowID, ref); err != nil {
			return nil, err
		}
	}
	if strings.HasPrefix(action, "data_") && doc.Render != model.DocRenderHTML && doc.Render != model.DocRenderData {
		return nil, fmt.Errorf("%s 不是 Artifact（render=%s）：数据只能挂在 render 为 html 或 data 的文档上，保存时带 render 参数", doc.Path, doc.Render)
	}
	label, ref2 := a.WorkflowID, "workflow:"+a.WorkflowID
	if c := aclCallerFrom(ctx); c != nil {
		if c.Label != "" {
			label = c.Label
		}
		if c.IdentityID != 0 {
			ref2 = fmt.Sprintf("identity:%d", c.IdentityID)
		}
	}

	switch action {
	case "data_list":
		rows, err := s.store.ArtifactDataList(ctx, doc.ID)
		if err != nil {
			return nil, err
		}
		items := make([]map[string]any, 0, len(rows))
		for _, r := range rows {
			items = append(items, dataItem(r))
		}
		return map[string]any{"path": doc.Path, "items": items, "count": len(items)}, nil
	case "data_get":
		if strings.TrimSpace(a.Key) == "" {
			return nil, fmt.Errorf("data_get 需要 key")
		}
		r, err := s.store.ArtifactDataGet(ctx, doc.ID, a.Key)
		if err != nil {
			return nil, err
		}
		return dataItem(*r), nil
	case "data_set":
		if strings.TrimSpace(a.Key) == "" || len(a.Value) == 0 {
			return nil, fmt.Errorf("data_set 需要 key 与 value（任意 JSON）；并发写同一个键时带 expected_version（读到的 version，新建填 0）")
		}
		r, err := s.store.ArtifactDataSet(ctx, store.ArtifactDataSetInput{
			DocID: doc.ID, Key: a.Key, ValueJSON: string(a.Value), ExpectedVersion: a.ExpectedVersion, ActorLabel: label, ActorRef: ref2,
		})
		if err != nil {
			return nil, err
		}
		s.auditArtifactData(ctx, a, doc, r.Key, map[string]any{"version": r.Version, "bytes": len(r.Value)})
		s.notifyArtifact("artifact_data_updated", doc, r.Key, r.Version, false)
		return map[string]any{"key": r.Key, "version": r.Version, "bytes": len(r.Value)}, nil
	case "data_delete":
		if strings.TrimSpace(a.Key) == "" {
			return nil, fmt.Errorf("data_delete 需要 key")
		}
		ok, err := s.store.ArtifactDataDelete(ctx, doc.ID, a.Key)
		if err != nil {
			return nil, err
		}
		if ok {
			s.auditArtifactData(ctx, a, doc, a.Key, map[string]any{"deleted": true})
			s.notifyArtifact("artifact_data_updated", doc, a.Key, 0, true)
		}
		return map[string]any{"deleted": ok}, nil
	case "comments":
		rows, err := s.store.ListArtifactComments(ctx, doc.ID, a.IncludeResolved, a.Limit)
		if err != nil {
			return nil, err
		}
		return map[string]any{"path": doc.Path, "items": rows, "count": len(rows), "open": s.store.CountOpenArtifactComments(ctx, doc.ID)}, nil
	case "comment":
		cm, err := s.store.AddArtifactComment(ctx, store.ArtifactCommentInput{
			DocID: doc.ID, Anchor: a.Anchor, Body: a.Body, AuthorKind: "identity", AuthorRef: ref2, AuthorLabel: label,
		})
		if err != nil {
			return nil, err
		}
		s.notifyArtifact("artifact_comment_updated", doc, "", 0, false)
		return map[string]any{"id": cm.ID}, nil
	default: // comment_resolve
		if a.CommentID == 0 {
			return nil, fmt.Errorf("comment_resolve 需要 comment_id（用 comments 读到的 id）")
		}
		resolved := a.Resolved == nil || *a.Resolved
		cm, err := s.store.ResolveArtifactComment(ctx, doc.ID, a.CommentID, resolved)
		if err != nil {
			return nil, err
		}
		s.notifyArtifact("artifact_comment_updated", doc, "", 0, false)
		return map[string]any{"id": cm.ID, "resolved": cm.Resolved}, nil
	}
}

func dataItem(r model.ArtifactData) map[string]any {
	return map[string]any{
		"key": r.Key, "value": json.RawMessage(r.Value), "version": r.Version,
		"updated_by": r.UpdatedBy, "updated_at": r.UpdatedAt,
	}
}

func (s *Server) notifyArtifact(event string, doc *model.ProjectDoc, key string, version int, deleted bool) {
	if s.onUpdate == nil || doc == nil {
		return
	}
	s.onUpdate(event, map[string]any{"doc_id": doc.ID, "workflow_id": doc.WorkflowID, "project_id": doc.ProjectID, "key": key, "version": version, "deleted": deleted})
}

// auditArtifactData 记一条 artifact.data_write（只记键、版本和大小，不记值）。
func (s *Server) auditArtifactData(ctx context.Context, a projectDocsArgs, doc *model.ProjectDoc, key string, detail map[string]any) {
	caller := aclCallerFrom(ctx)
	result := model.AuditAllow
	if caller != nil && !caller.Asserted {
		result = model.AuditAssumed
	}
	var p *model.Project
	if doc.ProjectID != 0 {
		p = &model.Project{ID: doc.ProjectID}
	}
	detail["workflow_id"] = a.WorkflowID
	s.aclAudit(ctx, p, caller, a.WorkflowID, model.AuditArtifactDataSet, result, "doc", doc.Path, key, 0, detail)
}
