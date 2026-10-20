package confirm

import (
	"strings"

	"github.com/teacat99/RelayMesh/v2/internal/docs"
	"github.com/teacat99/RelayMesh/v2/internal/model"
)

// answerResultText 把一次作答写成导出表里的「结果」一格：选项 id（文案）[；备注：…][；自定义答复：…]。
// 只写了自定义答复（#183）时没有选项部分，写成「没有选预设选项」；自定义答复永远在最后（表格换行由导出表统一折成空格）。
func answerResultText(v DecisionView) string {
	if v.Answer == nil {
		return ""
	}
	custom := strings.TrimSpace(v.Answer.Custom)
	if len(v.Answer.OptionIDs) == 0 && custom == "" {
		return ""
	}
	labels := make(map[string]string, len(v.Card.Options))
	for _, o := range v.Card.Options {
		labels[o.ID] = o.Label
	}
	parts := make([]string, 0, len(v.Answer.OptionIDs))
	for _, id := range v.Answer.OptionIDs {
		if l := labels[id]; l != "" {
			parts = append(parts, id+"（"+l+"）")
		} else {
			parts = append(parts, id)
		}
	}
	out := strings.Join(parts, "、")
	if out == "" {
		out = "没有选预设选项"
	}
	if n := strings.TrimSpace(v.Answer.Note); n != "" {
		out += "；备注：" + n
	}
	if custom != "" {
		out += "；自定义答复：" + custom
	}
	return out
}

// ExportDecisionRows 从对齐后的决策视图生成导出表行；围栏校验没过的不进表。
func ExportDecisionRows(views []DecisionView) []docs.DecisionExportRow {
	var rows []docs.DecisionExportRow
	for _, v := range views {
		if len(v.Errors) > 0 {
			continue
		}
		row := docs.DecisionExportRow{ID: v.Card.ID, Title: v.Card.Title, Status: v.Status, Ref: v.ConfirmRef}
		switch {
		case v.Status == "answered" || v.Status == "expired":
			row.Result = answerResultText(v)
		case v.Status == "pending":
			row.Result = "待作答"
		case v.Status == "dismissed":
			row.Result = "已忽略"
		}
		rows = append(rows, row)
	}
	return rows
}

// ExportDecisionRowsFromItems 在只有解析结果与确认项映射时生成导出行。
func ExportDecisionRowsFromItems(parsed []docs.ParsedDecision, byKey map[string]*model.ConfirmItem) []docs.DecisionExportRow {
	return ExportDecisionRows(EnrichDecisions(parsed, byKey))
}
