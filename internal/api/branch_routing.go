package api

import (
	"context"
	"errors"
	"log"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/teacat99/RelayMesh/v2/internal/model"
)

// 决策路由（#236 P2）的 Web 接口：分支里智能体发的请示先送主会话定夺，设计见 .cursor/planning/2026-10-24-236-p2-impl-plan.md。
//
//   - GET /branch/prefs   分支全局偏好：升级时限（路由给主会话的请示超过多久没人答复自动转交用户）与
//     「L2 请示不由主会话代答」的兜底开关；
//   - PUT /branch/prefs   局部修改，只带要改的字段（escalate_minutes 取 5~1440，0 = 恢复默认 30；l2_guard 布尔）。
//
// 用户直接答复请示走现有的 POST /workflows/:id/confirms/:ref/answer（先到先得），转交 / 代答 / 超时升级由 Agent 与后台扫描触发，不在这里。

// askNotifier 是「新请示」通知的出口（通知闸）：请示转交 / 升级给用户的那一刻，按新请示通知他（免打扰与会话静音照常生效）。
type askNotifier interface {
	NotifyAsk(item *model.ConfirmItem)
}

// SetAskNotifier 接入通知闸；NewServer 里接线。传 nil 表示不发通知（测试与精简装配）。
func (h *APIHandler) SetAskNotifier(n askNotifier) { h.askNotifier = n }

// notifyEscalatedAsks 给一批刚转交 / 升级给用户的请示发通知：先刷新页面上的确认卡与计数，再按新请示走通知闸；
// 同时对每个分支发一次 branch_changed（change = ask_escalated），主会话页的分支流据此补上「请示已转交」事件行
// （超时升级发生在后台，没有任何页面操作会触发那一页刷新）。
func (h *APIHandler) notifyEscalatedAsks(items []model.ConfirmItem) {
	seen := map[string]bool{}
	for i := range items {
		it := items[i]
		h.broadcastConfirmUpdated(it.WorkflowID, &it)
		if h.askNotifier != nil {
			h.askNotifier.NotifyAsk(&it)
		}
		if seen[it.WorkflowID] {
			continue
		}
		seen[it.WorkflowID] = true
		parent := ""
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		if rel, err := h.store.BranchOf(ctx, it.WorkflowID); err == nil && rel != nil {
			parent = rel.ParentWorkflowID
		}
		cancel()
		h.broadcastBranchChanged(parent, it.WorkflowID, "ask_escalated")
	}
}

// branchPrefsView 是偏好给界面看的样子：原始取值 + 实际生效的升级时限 + 取值范围（界面据此限制输入）。
func branchPrefsView(p model.BranchPrefs) gin.H {
	return gin.H{
		"l2_guard":                 p.L2Guard,
		"escalate_minutes":         p.EscalateMinutes,
		"escalate_after_minutes":   p.EscalateAfterMinutes(),
		"default_escalate_minutes": model.BranchEscalateDefaultMinutes,
		"min_escalate_minutes":     model.BranchEscalateMinMinutes,
		"max_escalate_minutes":     model.BranchEscalateMaxMinutes,
	}
}

// GetBranchPrefs GET /api/v1/branch/prefs
func (h *APIHandler) GetBranchPrefs(c *gin.Context) {
	p, err := h.store.GetBranchPrefs(c.Request.Context())
	if err != nil {
		respondDocError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"prefs": branchPrefsView(p)})
}

// UpdateBranchPrefs PUT /api/v1/branch/prefs  { "l2_guard"?: bool, "escalate_minutes"?: int }
func (h *APIHandler) UpdateBranchPrefs(c *gin.Context) {
	var req struct {
		L2Guard         *bool `json:"l2_guard"`
		EscalateMinutes *int  `json:"escalate_minutes"`
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 4<<10)
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请求格式不对：" + err.Error()})
		return
	}
	if req.L2Guard == nil && req.EscalateMinutes == nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "没有要修改的字段（l2_guard / escalate_minutes）"})
		return
	}
	next, err := h.store.UpdateBranchPrefs(c.Request.Context(), func(cur model.BranchPrefs) (model.BranchPrefs, error) {
		if req.L2Guard != nil {
			cur.L2Guard = *req.L2Guard
		}
		if req.EscalateMinutes != nil {
			cur.EscalateMinutes = *req.EscalateMinutes
		}
		return cur, nil
	})
	if err != nil {
		if errors.Is(err, model.ErrInvalidBranchPrefs) {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		respondDocError(c, err)
		return
	}
	log.Printf("[branch] 偏好已更新：l2_guard=%v escalate_minutes=%d（生效 %d 分钟）", next.L2Guard, next.EscalateMinutes, next.EscalateAfterMinutes())
	c.JSON(http.StatusOK, gin.H{"status": "ok", "prefs": branchPrefsView(next)})
}
