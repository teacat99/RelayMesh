package confirm

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/teacat99/RelayMesh/v2/internal/docs"
	"github.com/teacat99/RelayMesh/v2/internal/model"
)

// DecisionView 是文档决策卡与确认项对齐后的展示字段。
type DecisionView struct {
	Card       docs.DecisionCard    `json:"card"`
	StartLine  int                  `json:"start_line"`
	EndLine    int                  `json:"end_line"`
	Errors     []string             `json:"errors,omitempty"`
	Status     string               `json:"status"`
	StatusNote string               `json:"status_note,omitempty"`
	ConfirmRef string               `json:"confirm_ref,omitempty"`
	Answer     *model.ConfirmAnswer `json:"answer,omitempty"`
	AnsweredAt *time.Time           `json:"answered_at,omitempty"`
}

// EnrichDecisions 把 ParseDecisions 的结果与文档 decision 确认项对齐。
func EnrichDecisions(parsed []docs.ParsedDecision, byKey map[string]*model.ConfirmItem) []DecisionView {
	out := make([]DecisionView, 0, len(parsed))
	for _, d := range parsed {
		v := DecisionView{
			Card: d.Card, StartLine: d.StartLine, EndLine: d.EndLine,
			Errors: d.Errors, Status: "pending", StatusNote: "待作答",
		}
		if len(d.Errors) > 0 {
			v.Status = "invalid"
			v.StatusNote = "围栏校验未通过"
			out = append(out, v)
			continue
		}
		key := strings.TrimSpace(d.Card.ID)
		item := byKey[key]
		if item == nil {
			// 还没有对应的确认项（例如同步失败）：保存一次文档即可补上；界面据此禁用作答。
			v.StatusNote = "待作答（确认项尚未生成，保存一次文档即可）"
			out = append(out, v)
			continue
		}
		v.ConfirmRef = model.ConfirmRef(item.Seq)
		switch item.Status {
		case model.ConfirmStatusPending:
			v.Status = "pending"
			v.StatusNote = "待作答"
		case model.ConfirmStatusAnswered:
			v.Status = "answered"
			v.StatusNote = "已作答"
			decodeAnswer(item, &v)
		case model.ConfirmStatusExpired:
			v.Status = "expired"
			v.StatusNote = "选项已变更，旧答复已过期"
			decodeAnswer(item, &v)
		case model.ConfirmStatusWithdrawn:
			v.Status = "withdrawn"
			v.StatusNote = "决策卡已移除或文档已归档"
		case model.ConfirmStatusDismissed:
			v.Status = "dismissed"
			v.StatusNote = "已忽略"
		default:
			v.Status = item.Status
		}
		out = append(out, v)
	}
	return out
}

func decodeAnswer(item *model.ConfirmItem, v *DecisionView) {
	if item.AnswerJSON == "" {
		return
	}
	var a model.ConfirmAnswer
	if json.Unmarshal([]byte(item.AnswerJSON), &a) == nil {
		v.Answer = &a
	}
	v.AnsweredAt = item.AnsweredAt
}
