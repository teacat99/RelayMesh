package api

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/teacat99/RelayMesh/v2/internal/store"
)

// CreateWorkflow 网页手动新建会话（#174 第一期）：落一行占位会话，把「启动提示词」交给用户去发给智能体。
// 形态与取舍见 store/web_workflow.go。
//
//	POST /api/v1/workflows  {"title": "...", "workflow_id": "（可选）",
//	                         "first_message": "（可选）", "phase": "（可选）discuss|plan|dev",
//	                         "project_directory": "（可选）绝对路径", "host_name": "（可选）"}
//	201 {"status":"ok","workflow_id":"...","session":{占位会话},"start_prompt":"...",
//	     "applied":{…实际生效的高级选项，没填就没有这个字段…},"warnings":[…落库之后没做成的事，没有就没有这个字段…]}
//	400 名字 / 编号 / 阶段 / 首条指令 / 目录 / 主机不合规（都发生在落库之前，不会留下半个会话）   409 给定的编号已存在
//
// 高级选项（#184）全部可选，都不填时与改动前的请求 / 响应逐字节相同：
//   - project_directory：占位会话（以及这个工作流）绑定到这个目录，D-161 从此要求智能体第一次汇报带同一个目录；
//   - host_name：占位会话的归属主机标签（智能体接入后，凭据绑定的主机仍以凭据为准）；
//   - phase：起始阶段，按「人点的阶段」写入（同时是人工偏好阶段），广播 phase_changed；
//   - first_message：首条指令，经现有的投递规则一律排进收件箱（占位会话已完成且已被消费），智能体接入后读到。
//
// 校验类错误一律发生在落库之前；会话建好之后，阶段 / 首条指令这两步若失败，会话仍然保留，原因写进 warnings
// （用户可以在会话页里再设一次阶段、再写一遍指令），不把整个请求判成失败。
func (h *APIHandler) CreateWorkflow(c *gin.Context) {
	var req struct {
		Title            string `json:"title"`
		WorkflowID       string `json:"workflow_id"`
		FirstMessage     string `json:"first_message"`
		Phase            string `json:"phase"`
		ProjectDirectory string `json:"project_directory"`
		HostName         string `json:"host_name"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// 首条指令与起始阶段也在落库之前校验（名字 / 编号 / 目录 / 主机由 CreateWebWorkflow 在落库前校验）。
	firstMessage, err := store.NormalizeWebFirstMessage(req.FirstMessage)
	if err != nil {
		writeWebWorkflowError(c, err)
		return
	}
	phase, err := store.NormalizeWebPhase(req.Phase)
	if err != nil {
		writeWebWorkflowError(c, err)
		return
	}

	envHost := ""
	if h.cfg != nil {
		envHost = h.cfg.HostName
	}
	ctx := c.Request.Context()
	res, err := h.store.CreateWebWorkflow(ctx, store.CreateWebWorkflowInput{
		Title:            req.Title,
		WorkflowID:       req.WorkflowID,
		EnvHostName:      envHost,
		ProjectDirectory: req.ProjectDirectory,
		HostName:         req.HostName,
	})
	if err != nil {
		writeWebWorkflowError(c, err)
		return
	}
	wf := res.Session.WorkflowID

	applied := gin.H{}
	var warnings []gin.H
	if req.ProjectDirectory != "" && res.Session.ProjectDirectory != "" {
		applied["project_directory"] = res.Session.ProjectDirectory
	}
	if req.HostName != "" {
		applied["host_name"] = res.Session.HostName
	}
	if phase != "" {
		// 人选的起始阶段：与会话页里点阶段滑块同一条路径（isHumanClick = true：同时写人工偏好阶段）。
		if err := h.store.SetWorkflowPhase(ctx, wf, phase, true); err != nil {
			warnings = append(warnings, gin.H{"field": "phase", "error": err.Error()})
		} else {
			h.broker.Broadcast("phase_changed", gin.H{"workflow_id": wf, "phase": phase})
			applied["phase"] = phase
		}
	}
	if firstMessage != "" {
		// ForceInbox：不并入占位会话的「回复」，一律排进收件箱，智能体接入后（回执里或 workflow_context inbox）读到。
		if _, err := h.deliverWorkflowFeedback(ctx, wf, workflowFeedbackInput{
			ResponseText: firstMessage,
			HostName:     res.Session.HostName,
			ProjectDir:   res.Session.ProjectDirectory,
			ForceInbox:   true,
		}); err != nil {
			warnings = append(warnings, gin.H{"field": "first_message", "error": err.Error()})
		} else {
			applied["first_message"] = true
		}
	}

	// 其它已打开的网页（和手机）靠这条事件立刻看到新会话；载荷与 GET /sessions/:id 等价。
	h.broadcastSession("session_updated", res.Session)
	out := gin.H{
		"status":       "ok",
		"workflow_id":  wf,
		"session":      h.sessionPayload(res.Session),
		"start_prompt": res.StartPrompt,
	}
	if len(applied) > 0 {
		out["applied"] = applied
	}
	if len(warnings) > 0 {
		out["warnings"] = warnings
	}
	c.JSON(http.StatusCreated, out)
}

// writeWebWorkflowError 把新建会话的错误翻译成状态码：编号撞了 409、入参不合规 400、其余 500。
func writeWebWorkflowError(c *gin.Context, err error) {
	var appErr *store.AppError
	switch {
	case errors.As(err, &appErr) && appErr.Kind == store.ErrConflict:
		c.JSON(http.StatusConflict, gin.H{"error": appErr.Message})
	case errors.As(err, &appErr) && appErr.Kind == store.ErrInvalidInput:
		c.JSON(http.StatusBadRequest, gin.H{"error": appErr.Message})
	default:
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
	}
}
