package store

import (
	"context"
	"log"
	"strings"

	"github.com/teacat99/RelayMesh/v2/internal/confirm"
	"github.com/teacat99/RelayMesh/v2/internal/docs"
	"github.com/teacat99/RelayMesh/v2/internal/model"
)

const decisionFenceMarker = "```decision"

// DocDecisionViews 把文档正文里的决策围栏与确认项对齐，给网页和 MCP 读文档时用。
// historic=true（看历史修订）时不挂确认项：历史版本里的决策不能作答，也不该显示「现在」的答复。
// 读确认项失败只记日志、退化为「没挂确认项」，不让一次读文档因此失败。
func (s *Store) DocDecisionViews(ctx context.Context, doc *model.ProjectDoc, body string, historic bool) []confirm.DecisionView {
	parsed := docs.ParseDecisions(body)
	if len(parsed) == 0 {
		return []confirm.DecisionView{}
	}
	var byKey map[string]*model.ConfirmItem
	if !historic && doc != nil {
		m, err := s.DecisionConfirmsByKey(ctx, doc.ID)
		if err != nil {
			log.Printf("[confirm] 读取文档决策确认项失败（文档 #%d）: %v", doc.ID, err)
		} else {
			byKey = m
		}
	}
	views := confirm.EnrichDecisions(parsed, byKey)
	if historic {
		for i := range views {
			if len(views[i].Errors) == 0 {
				views[i].StatusNote = "历史版本，不能作答"
			}
		}
	}
	return views
}

// DocDecisionExportRows 返回导出时要追加到文档末尾的「决策结果」表的行；没有决策围栏的文档返回 nil。
func (s *Store) DocDecisionExportRows(ctx context.Context, doc *model.ProjectDoc) []docs.DecisionExportRow {
	if doc == nil || !strings.Contains(doc.Body, decisionFenceMarker) {
		return nil
	}
	return confirm.ExportDecisionRows(s.DocDecisionViews(ctx, doc, doc.Body, false))
}
