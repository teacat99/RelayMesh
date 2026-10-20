package api

import (
	"context"
	"log"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/teacat99/RelayMesh/v2/internal/model"
	"github.com/teacat99/RelayMesh/v2/internal/store"
	"github.com/teacat99/RelayMesh/v2/internal/terminal"
)

// 确认项 REST：只给 Web 登录用户用（智能体经 MCP async_ask 提问，不能经这里替用户作答——
// 这一点由路由所在的鉴权组保证：/api/v1 下全部要求登录 JWT，MCP 凭据进不来）。

// broadcastConfirmUpdated 通知前端「这个工作流的确认项有变化」，前端据此重新拉待确认区。
func (h *APIHandler) broadcastConfirmUpdated(workflowID string, item *model.ConfirmItem) {
	if h.broker == nil || workflowID == "" {
		return
	}
	payload := gin.H{"workflow_id": workflowID}
	if item != nil {
		payload["seq"], payload["ref"], payload["status"] = item.Seq, model.ConfirmRef(item.Seq), item.Status
	}
	// 顺带带上这个工作流此刻待处理的条数：侧栏里不是当前工作流的会话卡据此直接校准「等待您 · N 项」，不必为此重拉整张会话列表（#159）
	if h.store != nil {
		if n, err := h.store.CountPendingConfirms(context.Background(), workflowID); err == nil {
			payload["pending_count"] = n
		}
	}
	h.broker.Broadcast("confirm_updated", payload)
}

// broadcastInboxChanged 答复 / 忽略会往收件箱投递一条消息，收件箱状态块要跟着刷新。
func (h *APIHandler) broadcastInboxChanged(workflowID string) {
	if h.broker == nil || workflowID == "" {
		return
	}
	h.broker.Broadcast("queued_feedback_updated", gin.H{"workflow_id": workflowID})
}

// ListConfirms GET /api/v1/workflows/:workflow_id/confirms?status=&kind=&task_seq=&doc_id=&limit=&offset=
func (h *APIHandler) ListConfirms(c *gin.Context) {
	wf := c.Param("workflow_id")
	limit, _ := strconv.Atoi(c.Query("limit"))
	offset, _ := strconv.Atoi(c.Query("offset"))
	taskSeq, _ := strconv.Atoi(c.Query("task_seq"))
	docID, _ := strconv.ParseUint(c.Query("doc_id"), 10, 64)
	list, err := h.store.ListConfirmItems(c.Request.Context(), store.ConfirmFilter{
		WorkflowID: wf,
		Statuses:   splitCSV(c.Query("status")),
		Kinds:      splitCSV(c.Query("kind")),
		TaskSeq:    taskSeq,
		DocID:      uint(docID),
		Limit:      limit,
		Offset:     offset,
		Ascending:  c.Query("order") == "asc",
	})
	if err != nil {
		respondDocError(c, err)
		return
	}
	views, err := h.store.ConfirmViews(c.Request.Context(), list)
	if err != nil {
		respondDocError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"items": views})
}

// GetConfirm GET /api/v1/workflows/:workflow_id/confirms/:ref
func (h *APIHandler) GetConfirm(c *gin.Context) {
	item, err := h.store.GetConfirmItemByRef(c.Request.Context(), c.Param("workflow_id"), c.Param("ref"))
	if err != nil {
		respondDocError(c, err)
		return
	}
	view, err := h.store.ConfirmView(c.Request.Context(), item)
	if err != nil {
		respondDocError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"item": view})
}

// confirmBoardGuard 是「待确认」区里一条守护告警（会话崩溃 / 停摆，需要人决定要不要唤醒）。
type confirmBoardGuard struct {
	PresetID string                `json:"preset_id"`
	Name     string                `json:"name"`
	Host     string                `json:"host,omitempty"`
	Verdict  terminal.GuardVerdict `json:"verdict"`
	WakeMode string                `json:"wake_mode,omitempty"`
}

func guardNeedsAttention(g terminal.GuardItem) bool {
	if !g.Enabled || g.PermitMarked {
		return false
	}
	st := g.Verdict.State
	return st == terminal.GuardCrashed || st == terminal.GuardStalled
}

// GetConfirmsBoard GET /api/v1/workflows/:workflow_id/confirms/board
// 交流页「待答复」区的一次性数据：待处理的确认项（旧的在前）、本工作流的守护告警，外加计数。
//
// 任务不再需要人逐条确认（#110，状态绑定智能体的开发流程），所以这里不再带「待验收的任务」；
// review_tasks / counts.review_tasks 两个字段保留为恒空（[] / 0），只为兼容还没刷新的旧页面，新页面不读。
func (h *APIHandler) GetConfirmsBoard(c *gin.Context) {
	wf := c.Param("workflow_id")
	ctx := c.Request.Context()

	pending, err := h.store.ListConfirmItems(ctx, store.ConfirmFilter{
		WorkflowID: wf, Statuses: []string{model.ConfirmStatusPending}, Ascending: true, Limit: 200,
	})
	if err != nil {
		respondDocError(c, err)
		return
	}
	items, err := h.store.ConfirmViews(ctx, pending)
	if err != nil {
		respondDocError(c, err)
		return
	}
	byRisk := map[string]int{model.ConfirmRiskL0: 0, model.ConfirmRiskL1: 0, model.ConfirmRiskL2: 0}
	byKind := map[string]int{model.ConfirmKindAsk: 0, model.ConfirmKindPlan: 0, model.ConfirmKindDecision: 0}
	for _, it := range items {
		byRisk[it.Risk]++
		byKind[it.Kind]++
	}

	guards := []confirmBoardGuard{}
	if h.terminal != nil {
		if view, gerr := h.terminal.GuardView(ctx); gerr != nil {
			log.Printf("[confirm] 待确认区读取会话守护状态失败: %v", gerr)
		} else {
			for _, g := range view.Items {
				if strings.TrimSpace(g.WorkflowID) != wf || !guardNeedsAttention(g) {
					continue
				}
				guards = append(guards, confirmBoardGuard{
					PresetID: g.PresetID, Name: g.Name, Host: g.Host, Verdict: g.Verdict, WakeMode: g.WakeMode,
				})
			}
		}
	}

	c.JSON(http.StatusOK, gin.H{
		"items":        items,
		"review_tasks": []any{}, // 兼容旧页面，恒空
		"guard_alerts": guards,
		"counts": gin.H{
			"pending":      len(items) + len(guards),
			"items":        len(items),
			"review_tasks": 0,
			"guard_alerts": len(guards),
			"by_risk":      byRisk,
			"by_kind":      byKind,
		},
	})
}

type confirmAnswerBody struct {
	Options []string `json:"options"`
	Note    string   `json:"note,omitempty"`
	// Custom 是「预设选项都不合适」时用户自己写的答复（#183）：单选时与 options 二选一，多选时可并存。
	Custom string `json:"custom,omitempty"`
}

// AnswerConfirm POST /api/v1/workflows/:workflow_id/confirms/:ref/answer  {options:[...], note?, custom?}
func (h *APIHandler) AnswerConfirm(c *gin.Context) {
	var body confirmAnswerBody
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请求格式无效"})
		return
	}
	wf := c.Param("workflow_id")
	item, err := h.store.AnswerConfirmItem(c.Request.Context(), wf, c.Param("ref"), store.ConfirmAnswerInput{
		OptionIDs: body.Options, Note: body.Note, Custom: body.Custom,
	})
	if err != nil {
		respondDocError(c, err)
		return
	}
	view, err := h.store.ConfirmView(c.Request.Context(), item)
	if err != nil {
		respondDocError(c, err)
		return
	}
	h.broadcastInboxChanged(wf)
	h.broadcastConfirmUpdated(wf, item)
	c.JSON(http.StatusOK, gin.H{"item": view})
}

type confirmBatchBody struct {
	Items            []store.ConfirmBatchItem `json:"items"`
	AdoptRecommended bool                     `json:"adopt_recommended,omitempty"`
}

// AnswerConfirmBatch POST /api/v1/workflows/:workflow_id/confirms/answer-batch
// {items:[{ref, options?, note?}], adopt_recommended?}：整批原子，任何一项不合格都不保存；L2 项不接受批量。
func (h *APIHandler) AnswerConfirmBatch(c *gin.Context) {
	var body confirmBatchBody
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请求格式无效"})
		return
	}
	wf := c.Param("workflow_id")
	res, err := h.store.AnswerConfirmBatch(c.Request.Context(), wf, body.Items, body.AdoptRecommended)
	if err != nil {
		respondDocError(c, err)
		return
	}
	list := make([]model.ConfirmItem, len(res.Items))
	for i := range res.Items {
		list[i] = *res.Items[i]
	}
	views, err := h.store.ConfirmViews(c.Request.Context(), list)
	if err != nil {
		respondDocError(c, err)
		return
	}
	h.broadcastInboxChanged(wf)
	h.broadcastConfirmUpdated(wf, nil)
	c.JSON(http.StatusOK, gin.H{"items": views, "inbox_message": res.Message})
}

type confirmDismissBody struct {
	Reason string `json:"reason,omitempty"`
}

// DismissConfirm POST /api/v1/workflows/:workflow_id/confirms/:ref/dismiss  {reason?}
func (h *APIHandler) DismissConfirm(c *gin.Context) {
	var body confirmDismissBody
	_ = c.ShouldBindJSON(&body) // 请求体可省略
	wf := c.Param("workflow_id")
	item, err := h.store.DismissConfirmItem(c.Request.Context(), wf, c.Param("ref"), body.Reason)
	if err != nil {
		respondDocError(c, err)
		return
	}
	view, err := h.store.ConfirmView(c.Request.Context(), item)
	if err != nil {
		respondDocError(c, err)
		return
	}
	h.broadcastInboxChanged(wf)
	h.broadcastConfirmUpdated(wf, item)
	c.JSON(http.StatusOK, gin.H{"item": view})
}
