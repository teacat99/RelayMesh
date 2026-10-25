package api

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
	"github.com/teacat99/RelayMesh/v2/internal/model"
	"github.com/teacat99/RelayMesh/v2/internal/store"
)

// 任务（WorkItem）的 REST 接口。登录后的管理员是唯一的调用者，所以所有写入都以「人」的身份进行：
// 只有人能把任务确认为「已完成」；智能体走的是 MCP 的 workflow_context(task_*)，规则在 store 层统一执行。

// respondWorkError 把 store 的错误翻译成 HTTP 状态；不识别的错误只返回笼统提示，不把内部细节交给浏览器。
func respondWorkError(c *gin.Context, err error) {
	var ae *store.AppError
	if errors.As(err, &ae) {
		code := http.StatusInternalServerError
		switch ae.Kind {
		case store.ErrNotFound:
			code = http.StatusNotFound
		case store.ErrInvalidInput:
			code = http.StatusBadRequest
		case store.ErrConflict:
			code = http.StatusConflict
		case store.ErrForbidden:
			code = http.StatusForbidden
		}
		c.JSON(code, gin.H{"error": ae.Message})
		return
	}
	c.JSON(http.StatusInternalServerError, gin.H{"error": "操作失败，请稍后重试"})
}

func parseWorkID(c *gin.Context) (uint, bool) {
	n, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || n == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "无效的任务编号"})
		return 0, false
	}
	return uint(n), true
}

func (h *APIHandler) broadcastWorkItem(item *model.WorkItem, deleted bool) {
	if h.broker == nil || item == nil {
		return
	}
	// workflow_id 是创建者；同一个项目的工作流共用一张任务板，前端按 board_key 判断「是不是我正在看的这张板」（#175）。
	h.broker.Broadcast("work_item_updated", gin.H{"id": item.ID, "workflow_id": item.WorkflowID, "board_key": item.BoardKey, "deleted": deleted})
}

// GetWorkflowUpdates 返回一个工作流里智能体写下的任务进展（会话页的进展卡：每次更新一张，按提交时间升序）。
// 人在网页上的操作不在其中；since 是 RFC3339 时间，只要它之后的；limit 缺省取最新 60 条。
//
//	GET /api/v1/workflows/:workflow_id/work-updates?since=&limit=
func (h *APIHandler) GetWorkflowUpdates(c *gin.Context) {
	workflowID := strings.TrimSpace(c.Param("workflow_id"))
	if workflowID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "缺少 workflow_id"})
		return
	}
	var since time.Time
	if v := strings.TrimSpace(c.Query("since")); v != "" {
		t, err := time.Parse(time.RFC3339Nano, v)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "无效的 since，需要 RFC3339 时间"})
			return
		}
		since = t
	}
	limit := 60
	if v := strings.TrimSpace(c.Query("limit")); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "无效的 limit"})
			return
		}
		limit = n
	}
	updates, err := h.store.ListWorkflowUpdates(c.Request.Context(), workflowID, since, limit)
	if err != nil {
		respondWorkError(c, err)
		return
	}
	if updates == nil {
		updates = []store.WorkflowUpdate{}
	}
	c.JSON(http.StatusOK, gin.H{"updates": updates})
}

// ListWorkItems 返回任务列表，附各状态计数与「有任务的工作流」概况（任务页顶部的工作流切换用）。
// 指定了 workflow_id 时还附带这个工作流的规划树（modules）与未归类任务的汇总（unassigned）。
//
//	GET /api/v1/work-items?workflow_id=&state=todo,doing&kind=bug&stage=dev,none&session_id=&q=&parent_id=&module_id=&unassigned=1&top_level=1&closed_days=30&assignee=&limit=
//
// assignee（共享任务板，#175 B2）：只要指派给这个工作流的任务；none = 还没有指派的。注意它和 unassigned（没有归入模块）是两回事。
//
// stage 可写多个（逗号分隔），none 表示「未标注」；带 session_id 时只返回与该会话关联的任务，并给每条任务附上 session_ctx（这个会话对它做了什么）。
func (h *APIHandler) ListWorkItems(c *gin.Context) {
	f := store.WorkItemFilter{
		WorkflowID: strings.TrimSpace(c.Query("workflow_id")),
		States:     splitCSV(c.Query("state")),
		Kinds:      splitCSV(c.Query("kind")),
		Stages:     splitCSV(c.Query("stage")),
		SessionID:  strings.TrimSpace(c.Query("session_id")),
		Query:      c.Query("q"),
		TopLevel:   c.Query("top_level") == "1" || c.Query("top_level") == "true",
		Unassigned: c.Query("unassigned") == "1" || c.Query("unassigned") == "true",
		Assignee:   strings.TrimSpace(c.Query("assignee")),
	}
	if v := strings.TrimSpace(c.Query("module_id")); v != "" {
		n, err := strconv.ParseUint(v, 10, 64)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "无效的 module_id"})
			return
		}
		f.ModuleID = uint(n)
	}
	if v := strings.TrimSpace(c.Query("parent_id")); v != "" {
		n, err := strconv.ParseUint(v, 10, 64)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "无效的 parent_id"})
			return
		}
		pid := uint(n)
		f.ParentID = &pid
	}
	if v := strings.TrimSpace(c.Query("closed_days")); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "无效的 closed_days"})
			return
		}
		f.ClosedDays = n
	}
	if v := strings.TrimSpace(c.Query("limit")); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "无效的 limit"})
			return
		}
		f.Limit = n
	}
	ctx := c.Request.Context()
	items, err := h.store.ListWorkItems(ctx, f)
	if err != nil {
		respondWorkError(c, err)
		return
	}
	// 指定了工作流时按它所在的任务板统计（与上面的清单口径一致）；没指定 = 全部。
	var counts map[string]int
	if f.WorkflowID != "" {
		counts, err = h.store.CountBoardWorkItemsByState(ctx, f.WorkflowID)
	} else {
		counts, err = h.store.CountWorkItemsByState(ctx, "")
	}
	if err != nil {
		respondWorkError(c, err)
		return
	}
	workflows, err := h.store.ListWorkItemWorkflows(ctx)
	if err != nil {
		respondWorkError(c, err)
		return
	}
	resp := gin.H{"items": items, "counts": counts, "workflows": workflows}
	// offered（#180 B3b）：每个工作流手上「指派给它、还没接取」的协作任务个数与最早派出时间，侧栏会话卡的「N 项待接取」徽标用；
	// 全局口径（不随 workflow_id 过滤变），没有待接取的指派时不带这个字段，响应与以前一致。
	offered, err := h.store.BoardOfferedByWorkflow(ctx)
	if err != nil {
		respondWorkError(c, err)
		return
	}
	if len(offered) > 0 {
		resp["offered"] = offered
	}
	// 清单按最近更新取前 N 条：条数顶到上限时，更早更新的可能被略去了。带上标记，页面据此如实说明；没顶到就不带这个字段。
	if len(items) >= store.WorkItemListLimit(f.Limit) {
		resp["truncated"] = true
	}
	if f.WorkflowID != "" {
		// board_key：这个工作流所在的任务板；前端据此判断 SSE 事件是不是同一张板上的变动。
		boardKey, err := h.store.BoardKeyOf(ctx, f.WorkflowID)
		if err != nil {
			respondWorkError(c, err)
			return
		}
		resp["board_key"] = boardKey
		list, err := h.store.ListWorkModules(ctx, f.WorkflowID)
		if err != nil {
			respondWorkError(c, err)
			return
		}
		resp["modules"], resp["unassigned"] = list.Modules, list.Unassigned
		// 会话页按轮次展开任务：同时带回这个工作流里「哪个会话涉及哪个任务、做了什么」
		if v := c.Query("with_links"); v == "1" || v == "true" {
			links, err := h.store.ListWorkflowSessionLinks(ctx, f.WorkflowID)
			if err != nil {
				respondWorkError(c, err)
				return
			}
			resp["links"] = links
		}
	}
	c.JSON(http.StatusOK, resp)
}

type workItemCreateRequest struct {
	WorkflowID string   `json:"workflow_id"`
	Title      string   `json:"title"`
	Kind       string   `json:"kind"`
	Stage      string   `json:"stage"` // assess / plan / dev / improve；缺省 = 未标注
	State      string   `json:"state"`
	Labels     []string `json:"labels"`
	Refs       []string `json:"refs"`
	// Description 是任务内容的说明（这件事是什么、做到什么算完成），最长 model.WorkDescriptionMax 个字。
	Description string `json:"description"`
	Progress    string `json:"progress"`
	Note        string `json:"note"`
	ParentID    uint   `json:"parent_id"`
	ModuleID    uint   `json:"module_id"` // 0 = 未归类
	Percent     *int   `json:"percent"`   // 0–100；缺省 = 由状态 / 子任务推导
	// AssigneeWorkflowID 新建时直接指派给同一张任务板上的另一个工作流（人类可以指派）；缺省 / 空 = 不指派。
	AssigneeWorkflowID string `json:"assignee_workflow_id"`
}

// CreateWorkItem 新建任务（以「人」的身份）。
func (h *APIHandler) CreateWorkItem(c *gin.Context) {
	var req workItemCreateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "无效的请求内容"})
		return
	}
	res, err := h.store.CreateWorkItem(c.Request.Context(), store.WorkItemInput{
		WorkflowID: req.WorkflowID, Title: req.Title, Kind: req.Kind, Stage: req.Stage, State: req.State,
		Labels: req.Labels, Refs: req.Refs, Description: req.Description, Progress: req.Progress, Note: req.Note, ParentID: req.ParentID,
		ModuleID: req.ModuleID, Percent: req.Percent, Assignee: req.AssigneeWorkflowID,
	}, store.WorkActor{Kind: model.WorkActorHuman})
	if err != nil {
		respondWorkError(c, err)
		return
	}
	h.broadcastWorkItem(res.Item, false)
	c.JSON(http.StatusCreated, gin.H{"item": res.Item})
}

// ListWorkItemAssignees 返回一张任务板上能被指派的工作流（共享任务板，#175 B2b）：详情里「指派」下拉按需取，不放进清单响应。
// 独立板（工作流不属于任何项目）没有可指派的对象，members 为空。
//
//	GET /api/v1/work-items/assignees?workflow_id=
func (h *APIHandler) ListWorkItemAssignees(c *gin.Context) {
	wf := strings.TrimSpace(c.Query("workflow_id"))
	if wf == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "缺少 workflow_id"})
		return
	}
	ctx := c.Request.Context()
	boardKey, err := h.store.BoardKeyOf(ctx, wf)
	if err != nil {
		respondWorkError(c, err)
		return
	}
	members, err := h.store.BoardWorkflowIDs(ctx, boardKey)
	if err != nil {
		respondWorkError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"board_key": boardKey, "members": members})
}

// GetWorkItem 返回任务详情（含最近的时间线与关联会话）。
func (h *APIHandler) GetWorkItem(c *gin.Context) {
	id, ok := parseWorkID(c)
	if !ok {
		return
	}
	item, err := h.store.GetWorkItem(c.Request.Context(), id)
	if err != nil {
		respondWorkError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"item": item})
}

// GetWorkItemBySeq 按「工作流 + 板内序号 #N」返回任务详情（响应形态与 GetWorkItem 相同）。
//
//	GET /api/v1/work-items/by-seq?workflow_id=&seq=
//
// 正文、汇报与链接里写的 #N 是板内序号：它在工作流所在的任务板内唯一（同一个项目的工作流共用一套 #N），和 /work-items/:id 用的
// 数据库编号是两回事——把 #N 当数据库编号用，打开的就是别的任务（#242）。任务页的 ?seq= 深链靠这个接口把序号解析成任务，
// 解析与智能体按 #N 取任务走同一条路（含合并任务板时改过号的旧序号），不依赖页面已加载的清单（已结束 / 已暂缓 / 很早以前的任务一样能开）。
// 这个板上没有这个序号 = 404。
func (h *APIHandler) GetWorkItemBySeq(c *gin.Context) {
	wf := strings.TrimSpace(c.Query("workflow_id"))
	if wf == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "缺少 workflow_id"})
		return
	}
	seq, err := strconv.Atoi(strings.TrimSpace(c.Query("seq")))
	if err != nil || seq <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "无效的 seq（应是正整数的板内序号）"})
		return
	}
	item, err := h.store.GetWorkItemBySeq(c.Request.Context(), wf, seq)
	if err != nil {
		respondWorkError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"item": item})
}

type workItemUpdateRequest struct {
	Title  *string   `json:"title"`
	Kind   *string   `json:"kind"`
	Stage  *string   `json:"stage"` // 空串 = 清除（回到未标注）
	State  *string   `json:"state"`
	Labels *[]string `json:"labels"`
	Refs   *[]string `json:"refs"`
	// Description 空串 = 清除；只改描述不算进展（不动 updated_at、不占时间线）。
	Description *string `json:"description"`
	Progress    *string `json:"progress"`
	Conclusion  *string `json:"conclusion"`
	ParentID    *uint   `json:"parent_id"` // 0 = 解除父任务
	ModuleID    *uint   `json:"module_id"` // 0 = 取消归类
	Percent     *int    `json:"percent"`   // -1 = 清除显式完成度
	Note        string  `json:"note"`
	// AssigneeWorkflowID 指派 / 改派 / 撤销（共享任务板，#175 B2）：缺省（null）= 不动，空串 = 撤销指派，非空 = 指派给这个工作流。
	AssigneeWorkflowID *string `json:"assignee_workflow_id"`
	// Claim 协作动作：accept / decline / deliver / accept_delivery / reject_delivery；不能和 assignee_workflow_id 同时给。
	Claim string `json:"claim"`
}

// UpdateWorkItem 更新任务；缺省的字段保持不变。
func (h *APIHandler) UpdateWorkItem(c *gin.Context) {
	id, ok := parseWorkID(c)
	if !ok {
		return
	}
	var req workItemUpdateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "无效的请求内容"})
		return
	}
	res, err := h.store.UpdateWorkItem(c.Request.Context(), id, store.WorkItemPatch{
		Title: req.Title, Kind: req.Kind, Stage: req.Stage, State: req.State, Labels: req.Labels, Refs: req.Refs,
		Description: req.Description, Progress: req.Progress, Conclusion: req.Conclusion, ParentID: req.ParentID, ModuleID: req.ModuleID, Percent: req.Percent, Note: req.Note,
		Assignee: req.AssigneeWorkflowID, Claim: req.Claim,
	}, store.WorkActor{Kind: model.WorkActorHuman})
	if err != nil {
		respondWorkError(c, err)
		return
	}
	if res.Changed {
		h.broadcastWorkItem(res.Item, false)
		h.postBoardLines(c.Request.Context(), res)
	}
	c.JSON(http.StatusOK, gin.H{"item": res.Item, "changed": res.Changed})
}

// DeleteWorkItem 删除任务；子任务变成顶层任务，关联的会话不受影响。
func (h *APIHandler) DeleteWorkItem(c *gin.Context) {
	id, ok := parseWorkID(c)
	if !ok {
		return
	}
	item, err := h.store.DeleteWorkItem(c.Request.Context(), id)
	if err != nil {
		respondWorkError(c, err)
		return
	}
	h.broadcastWorkItem(item, true)
	c.JSON(http.StatusOK, gin.H{"status": "deleted"})
}

// LinkWorkItemSession 把一个会话关联到任务。
func (h *APIHandler) LinkWorkItemSession(c *gin.Context) {
	id, ok := parseWorkID(c)
	if !ok {
		return
	}
	var req struct {
		SessionID string `json:"session_id"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "无效的请求内容"})
		return
	}
	if err := h.store.LinkWorkItemSession(c.Request.Context(), id, req.SessionID); err != nil {
		respondWorkError(c, err)
		return
	}
	item, err := h.store.GetWorkItem(c.Request.Context(), id)
	if err != nil {
		respondWorkError(c, err)
		return
	}
	h.broadcastWorkItem(item, false)
	c.JSON(http.StatusOK, gin.H{"item": item})
}

// UnlinkWorkItemSession 解除任务与会话的关联。
func (h *APIHandler) UnlinkWorkItemSession(c *gin.Context) {
	id, ok := parseWorkID(c)
	if !ok {
		return
	}
	if err := h.store.UnlinkWorkItemSession(c.Request.Context(), id, c.Param("session_id")); err != nil {
		respondWorkError(c, err)
		return
	}
	item, err := h.store.GetWorkItem(c.Request.Context(), id)
	if err != nil {
		respondWorkError(c, err)
		return
	}
	h.broadcastWorkItem(item, false)
	c.JSON(http.StatusOK, gin.H{"item": item})
}

type workImproveRequest struct {
	Text string `json:"text"`
}

// ImproveWorkItem 在一个任务上发起改进需求（人的操作）：验收后发现问题、想改成别的样子时，不用另起一个任务，
// 直接在原任务上说清楚——任务回到「进行中」并标为「改进」阶段，需求记在它的时间线上，同时把这段话交给工作流里的智能体
// （与「追加指令」同一套投递：它正在等回复就是这一轮的回复，否则进收件箱排队）。
//
//	POST /api/v1/work-items/:id/improve   {"text": "导出的 CSV 在 Excel 里中文乱码"}
func (h *APIHandler) ImproveWorkItem(c *gin.Context) {
	id, ok := parseWorkID(c)
	if !ok {
		return
	}
	var req workImproveRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "无效的请求内容"})
		return
	}
	text := strings.TrimSpace(req.Text)
	if text == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请写一下要改进什么：发现了什么问题，或想改成什么样"})
		return
	}
	if n := utf8.RuneCountInString(text); n > model.WorkImproveMax {
		c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("改进需求最长 %d 个字，现在是 %d 个字；更详细的内容请直接在会话里告诉智能体", model.WorkImproveMax, n)})
		return
	}
	item, err := h.store.GetWorkItem(c.Request.Context(), id)
	if err != nil {
		respondWorkError(c, err)
		return
	}

	// 1. 重新开工：回到进行中、标为改进阶段，需求记在时间线上（再看这个任务的人和智能体都读得到）
	doing, improve := model.WorkStateDoing, model.WorkStageImprove
	res, err := h.store.UpdateWorkItem(c.Request.Context(), id, store.WorkItemPatch{State: &doing, Stage: &improve, Note: "改进需求：" + text}, store.WorkActor{Kind: model.WorkActorHuman})
	if err != nil {
		respondWorkError(c, err)
		return
	}
	h.broadcastWorkItem(res.Item, false)
	h.postBoardLines(c.Request.Context(), res) // 验证中的协作任务被退回改进，也是一次「退回」

	// 2. 告诉智能体：消息自己带着编号与标题，它不必先翻任务清单就知道说的是哪件事
	msg := fmt.Sprintf("改进需求 #%d「%s」：%s", item.Seq, item.Title, text)
	d, err := h.deliverWorkflowFeedback(c.Request.Context(), item.WorkflowID, workflowFeedbackInput{ResponseText: msg})
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "任务已回到进行中，但没能把需求交给智能体（" + err.Error() + "）；请在会话里直接告诉它", "item": res.Item})
		return
	}
	delivered := "queued"
	if d.Session != nil {
		delivered = "session"
	}
	c.JSON(http.StatusOK, gin.H{"item": res.Item, "delivered": delivered})
}
