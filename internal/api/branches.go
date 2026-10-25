package api

import (
	"context"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/teacat99/RelayMesh/v2/internal/model"
	"github.com/teacat99/RelayMesh/v2/internal/store"
)

// 分支会话的 Web 接口（#236 P1）。设计见 .cursor/planning/2026-10-23-236-branch-sessions-plan.md 与 2026-10-24-236-p1-impl-plan.md。
//
//   - GET  /branches                               全部分支关系（含已结束），每条带标题、最近活动、是否在等回复、主会话未读数；
//   - GET  /workflows/:workflow_id/branch-feed     一个会话页的分支消息流：自己的角色（主会话 / 分支 / 无）、关系概况、消息与建立 / 结束事件；
//   - PUT  /workflows/:workflow_id/branch          改这个分支向主会话开放的范围与「抄送」开关（只有人能改）；
//   - POST /workflows/:workflow_id/branch/close    结束这个分支（关系保留，历史仍可查；两边的 Agent 各收到一条系统通知）。
//   - GET / POST …/branch/params-events[/:event_id/revert]  主会话改过的参数记录与撤销（#236 P3，见 branch_params.go）。
//
// 新建分支走 POST /workflows（带 parent_workflow_id，见 web_workflow.go）；解除关系只有运维命令 branch-unlink。

// broadcastBranchChanged 告诉其它页面「分支关系有变化」（建立 / 设置 / 结束）：侧栏分支树、页头芯片与分支面板据此重新拉取。
func (h *APIHandler) broadcastBranchChanged(parent, branch, change string) {
	if h.broker == nil {
		return
	}
	h.broker.Broadcast("branch_changed", gin.H{"parent": parent, "branch": branch, "change": change})
}

// broadcastBranchMessage 告诉其它页面「主会话 / 分支之间有了新消息」（用户发言的抄送走这里；Agent 之间互发由 MCP 侧广播同名事件）。
func (h *APIHandler) broadcastBranchMessage(parent, branch string, id uint) {
	if h.broker == nil {
		return
	}
	h.broker.Broadcast("branch_message", gin.H{"parent": parent, "branch": branch, "id": id})
}

// ccBranchNote 把用户刚在这个会话里说的话抄送给它的主会话（这个会话不是活动分支 / 没开抄送时什么都不做）。
// 抄送失败绝不影响用户原本的操作：用户的话已经送到了这个会话的 Agent，抄送只是让主会话知情。
func (h *APIHandler) ccBranchNote(ctx context.Context, workflowID, text string, tags []string, images int) {
	if strings.TrimSpace(text) == "" && len(tags) > 0 {
		text = strings.Join(tags, "、")
	}
	row, err := h.store.CopyUserNoteToMain(ctx, workflowID, text, images)
	if err != nil || row == nil {
		return
	}
	h.broadcastBranchMessage(row.WorkflowID, workflowID, row.ID)
}

// ListBranches GET /api/v1/branches
func (h *APIHandler) ListBranches(c *gin.Context) {
	ctx := c.Request.Context()
	rows, err := h.store.ListAllBranches(ctx)
	if err != nil {
		respondDocError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"branches": h.store.BranchBriefs(ctx, rows)})
}

// GetBranchFeed GET /api/v1/workflows/:workflow_id/branch-feed?limit=
func (h *APIHandler) GetBranchFeed(c *gin.Context) {
	limit, _ := strconv.Atoi(c.Query("limit"))
	feed, err := h.store.BranchFeed(c.Request.Context(), c.Param("workflow_id"), limit)
	if err != nil {
		respondDocError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"branch_feed": feed})
}

// UpdateBranch PUT /api/v1/workflows/:workflow_id/branch  { "expose_preset"?: "visible|collab|managed", "expose"?: {…}, "copy_user_notes"?: bool }
// workflow_id 是分支自己的工作流编号。预设与显式取值二选一；都不带也不带 copy_user_notes 视为空请求，返回 400。
// 放宽「改参数」档位或打开「启动」要终端解锁（终端不可用时压回当前取值，响应带 clamped）；收紧与其它项只需登录。
func (h *APIHandler) UpdateBranch(c *gin.Context) {
	wf := c.Param("workflow_id")
	var req struct {
		ExposePreset  string              `json:"expose_preset"`
		Expose        *model.BranchExpose `json:"expose"`
		CopyUserNotes *bool               `json:"copy_user_notes"`
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 4<<10)
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请求格式不对：" + err.Error()})
		return
	}
	if strings.TrimSpace(req.ExposePreset) == "" && req.Expose == nil && req.CopyUserNotes == nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "没有要修改的内容：请带 expose_preset、expose 或 copy_user_notes"})
		return
	}
	ctx := c.Request.Context()
	in := store.UpdateBranchInput{Preset: req.ExposePreset, Expose: req.Expose, CopyUserNotes: req.CopyUserNotes}
	// 放宽（改参数档位调高 / 启动打开）要终端解锁；终端不可用时放宽的项压回当前取值，其余照办（#236 P3，见 branch_params.go）。
	clamped, ok := h.gateBranchUpdate(c, wf, &in)
	if !ok {
		return
	}
	row, escalated, err := h.store.UpdateBranchSettingsFull(ctx, wf, in)
	if err != nil {
		respondDocError(c, err)
		return
	}
	h.broadcastBranchChanged(row.ParentWorkflowID, row.BranchWorkflowID, "settings")
	// 关掉「定夺」时，原本路由给主会话、还没答的请示立即转交给用户（#236 P2）：刷新确认卡并按新请示通知。
	h.notifyEscalatedAsks(escalated)
	out := gin.H{"status": "ok", "branch": h.store.BranchBriefs(ctx, []model.WorkflowBranch{*row})[0]}
	if len(clamped) > 0 {
		out["clamped"] = clamped
	}
	c.JSON(http.StatusOK, out)
}

// CloseBranch POST /api/v1/workflows/:workflow_id/branch/close  （workflow_id 是分支自己的编号）
// 幂等：已经结束时返回 changed:false。
func (h *APIHandler) CloseBranch(c *gin.Context) {
	ctx := c.Request.Context()
	res, err := h.store.CloseBranch(ctx, c.Param("workflow_id"), model.BranchCreatedByHuman)
	if err != nil {
		respondDocError(c, err)
		return
	}
	if res.Changed {
		h.broadcastBranchChanged(res.Branch.ParentWorkflowID, res.Branch.BranchWorkflowID, "closed")
		// 结束时已路由未答的请示转交给用户（#236 P2）：刷新确认卡并按新请示通知。
		h.notifyEscalatedAsks(res.Escalated)
		// 结束时两边收件箱各写了一条系统通知：让还开着的页面刷新未读与消息流。
		if h.broker != nil {
			h.broker.Broadcast("queued_feedback_updated", gin.H{"workflow_id": res.Branch.BranchWorkflowID})
			h.broker.Broadcast("queued_feedback_updated", gin.H{"workflow_id": res.Branch.ParentWorkflowID})
			// 主会话派给它、还没交付的任务退回了主会话（#236 P5）：任务换了承接者，刷新任务页与侧栏计数。
			if len(res.Returned) > 0 {
				boardKey, _ := h.store.BoardKeyOf(ctx, res.Branch.ParentWorkflowID)
				h.broker.Broadcast("work_item_updated", gin.H{"workflow_id": res.Branch.ParentWorkflowID, "board_key": boardKey, "bulk": true})
			}
		}
	}
	out := gin.H{
		"status": "ok", "changed": res.Changed,
		"branch": h.store.BranchBriefs(ctx, []model.WorkflowBranch{res.Branch})[0],
	}
	if len(res.Returned) > 0 {
		out["returned_tasks"] = res.Returned // 退回主会话名下的任务序号
	}
	c.JSON(http.StatusOK, out)
}
