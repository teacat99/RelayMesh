package api

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/teacat99/RelayMesh/v2/internal/model"
)

// 恢复胶囊的只读视图。
//
// 胶囊本质上是模型写给「未来的自己」的笔记；Web 不提供编辑，只在交付时间线上把它们标出来：
// 每个胶囊按创建时间落在对应两次交付之间，显示修订号与基准哈希，点开可看目标 / 决策 / 下一步 / 阻塞。
// 这里只投影展示所需的字段，并限制条目数与单条长度，避免一个超长胶囊拖慢时间线。

const (
	checkpointViewMaxItems  = 8
	checkpointViewMaxRunes  = 400
	checkpointViewMaxLimit  = 50
	checkpointViewDefaultLn = 30
)

type checkpointDecisionView struct {
	DecisionID string `json:"decision_id,omitempty"`
	Status     string `json:"status,omitempty"`
	Summary    string `json:"summary"`
}

type checkpointView struct {
	Revision     int                      `json:"revision"`
	Status       string                   `json:"status"`
	CreatedAt    time.Time                `json:"created_at"`
	Objective    string                   `json:"objective"`
	CurrentStage *model.CheckpointStage   `json:"current_stage,omitempty"`
	Decisions    []checkpointDecisionView `json:"decisions,omitempty"`
	NextActions  []string                 `json:"next_actions,omitempty"`
	InProgress   []string                 `json:"in_progress,omitempty"`
	Blockers     []string                 `json:"blockers,omitempty"`
	Basis        model.CheckpointBasis    `json:"basis"`
}

func clipRunes(s string, max int) string {
	s = strings.TrimSpace(s)
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max]) + "…"
}

func clipList(items []string) []string {
	if len(items) == 0 {
		return nil
	}
	if len(items) > checkpointViewMaxItems {
		items = items[:checkpointViewMaxItems]
	}
	out := make([]string, 0, len(items))
	for _, it := range items {
		if t := clipRunes(it, checkpointViewMaxRunes); t != "" {
			out = append(out, t)
		}
	}
	return out
}

func buildCheckpointView(cp model.WorkflowCheckpoint) checkpointView {
	view := checkpointView{Revision: cp.Revision, Status: cp.Status, CreatedAt: cp.CreatedAt}
	var content model.CheckpointContent
	if err := json.Unmarshal([]byte(cp.ContentJSON), &content); err != nil {
		return view
	}
	view.Objective = clipRunes(content.Objective, checkpointViewMaxRunes)
	view.CurrentStage = content.CurrentStage
	view.NextActions = clipList(content.NextActions)
	view.InProgress = clipList(content.InProgress)
	view.Blockers = clipList(content.Blockers)
	view.Basis = content.Basis
	for i, d := range content.Decisions {
		if i >= checkpointViewMaxItems {
			break
		}
		view.Decisions = append(view.Decisions, checkpointDecisionView{
			DecisionID: clipRunes(d.DecisionID, 64),
			Status:     clipRunes(d.Status, 32),
			Summary:    clipRunes(d.Summary, checkpointViewMaxRunes),
		})
	}
	return view
}

// ListWorkflowCheckpoints 返回某工作流的恢复胶囊修订（只读，按修订号升序）。
func (h *APIHandler) ListWorkflowCheckpoints(c *gin.Context) {
	workflowID := strings.TrimSpace(c.Param("workflow_id"))
	if workflowID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "workflow_id is required"})
		return
	}
	limit := checkpointViewDefaultLn
	if raw := strings.TrimSpace(c.Query("limit")); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 {
			limit = n
		}
	}
	if limit > checkpointViewMaxLimit {
		limit = checkpointViewMaxLimit
	}

	cps, err := h.store.ListCheckpoints(c.Request.Context(), workflowID, limit)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	// 存储层按修订号倒序返回；时间线需要从旧到新。
	views := make([]checkpointView, 0, len(cps))
	for i := len(cps) - 1; i >= 0; i-- {
		views = append(views, buildCheckpointView(cps[i]))
	}
	c.JSON(http.StatusOK, gin.H{"workflow_id": workflowID, "checkpoints": views})
}
