package api

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/teacat99/RelayMesh/v2/internal/mcp"
	"github.com/teacat99/RelayMesh/v2/internal/model"
	"github.com/teacat99/RelayMesh/v2/internal/sessionprompt"
	"github.com/teacat99/RelayMesh/v2/internal/store"
)

// 会话级常驻提示词 REST（#232）：只给 Web 登录用户用（/api/v1 下全部要求登录 JWT，MCP 凭据进不来）。
// 智能体只能在回执里读到它（resident: 行 / workflow_context 的 resident_get），写入口只有这里。

// residentRevisionView 是历史版本列表里的一项。
type residentRevisionView struct {
	ID        uint      `json:"id"`
	Rev       int       `json:"rev"`
	Text      string    `json:"text"`
	Actor     string    `json:"actor,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

// workflowPromptsView 是 Web 看到的某个工作流的提示词设置。
type workflowPromptsView struct {
	WorkflowID  string `json:"workflow_id"`
	Resident    string `json:"resident"`
	ResidentRev int    `json:"resident_rev"`
	// Told：智能体是否已经见到当前版本（resident_told_rev 追平了 resident_rev）。从没配置过也算已告知——没有什么要告诉的。
	Told      bool                   `json:"told"`
	ToldRev   int                    `json:"told_rev"`
	UpdatedAt *time.Time             `json:"updated_at,omitempty"`
	UpdatedBy string                 `json:"updated_by,omitempty"`
	Max       int                    `json:"max"`
	Revisions []residentRevisionView `json:"revisions"`
	// 会话自己的唤醒词模板（#232 D3）：空串 = 没有，沿用全局设置里的覆盖，再没有就用内置默认。
	WakeResume string `json:"wake_resume"`
	WakeNew    string `json:"wake_new"`
	WakeMax    int    `json:"wake_max"`
}

func (h *APIHandler) workflowPromptsViewFor(c *gin.Context, workflowID string) (workflowPromptsView, error) {
	ctx := c.Request.Context()
	row, err := h.store.GetWorkflowPrompt(ctx, workflowID)
	if err != nil {
		return workflowPromptsView{}, err
	}
	revs, err := h.store.ListPromptRevisions(ctx, workflowID, model.PromptFieldResident)
	if err != nil {
		return workflowPromptsView{}, err
	}
	v := workflowPromptsView{
		WorkflowID: workflowID, Resident: row.Resident, ResidentRev: row.ResidentRev,
		Told: row.ResidentToldRev >= row.ResidentRev, ToldRev: row.ResidentToldRev,
		UpdatedBy: row.UpdatedBy, Max: model.ResidentPromptMax, Revisions: make([]residentRevisionView, 0, len(revs)),
		WakeResume: row.WakeResume, WakeNew: row.WakeNew, WakeMax: sessionprompt.TemplateMaxRunes,
	}
	if row.ResidentRev > 0 || row.WakeResume != "" || row.WakeNew != "" {
		at := row.UpdatedAt
		v.UpdatedAt = &at
	}
	for _, r := range revs {
		v.Revisions = append(v.Revisions, residentRevisionView{ID: r.ID, Rev: r.Rev, Text: r.Text, Actor: r.Actor, CreatedAt: r.CreatedAt})
	}
	return v, nil
}

func (h *APIHandler) broadcastWorkflowPrompts(workflowID string) {
	if h.broker == nil || workflowID == "" {
		return
	}
	h.broker.Broadcast("workflow_prompts_updated", gin.H{"workflow_id": workflowID})
}

// GetWorkflowPrompts GET /api/v1/workflows/:workflow_id/prompts
func (h *APIHandler) GetWorkflowPrompts(c *gin.Context) {
	v, err := h.workflowPromptsViewFor(c, c.Param("workflow_id"))
	if err != nil {
		respondDocError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"prompts": v})
}

// UpdateWorkflowPrompts PUT /api/v1/workflows/:workflow_id/prompts  { "resident"?: string, "wake_resume"?: string, "wake_new"?: string }
// 保存常驻提示词与 / 或会话自己的唤醒词模板（至少带一项）；内容没变不涨版本，空串表示清除，超过字数上限 400。
// 先全部校验再写：一项不合规，整个请求什么也不保存。
func (h *APIHandler) UpdateWorkflowPrompts(c *gin.Context) {
	wf := c.Param("workflow_id")
	var req struct {
		Resident   *string `json:"resident"`
		WakeResume *string `json:"wake_resume"`
		WakeNew    *string `json:"wake_new"`
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 32<<10)
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请求格式不对：" + err.Error()})
		return
	}
	if req.Resident == nil && req.WakeResume == nil && req.WakeNew == nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "没有要修改的内容：请带 resident、wake_resume 或 wake_new"})
		return
	}
	if req.Resident != nil {
		if _, err := store.NormalizePromptText(*req.Resident, model.ResidentPromptMax); err != nil {
			respondDocError(c, err)
			return
		}
	}
	for _, w := range []struct {
		kind sessionprompt.Kind
		text *string
	}{{sessionprompt.KindResume, req.WakeResume}, {sessionprompt.KindNew, req.WakeNew}} {
		if w.text == nil {
			continue
		}
		if _, err := store.NormalizeWakeTemplate(w.kind, *w.text); err != nil {
			respondDocError(c, err)
			return
		}
	}

	ctx, actor, now := c.Request.Context(), c.GetString("auth_subject"), time.Now()
	changed := false
	if req.Resident != nil {
		_, ch, err := h.store.SaveResidentPrompt(ctx, wf, *req.Resident, actor, now)
		if err != nil {
			respondDocError(c, err)
			return
		}
		changed = changed || ch
	}
	for _, w := range []struct {
		kind sessionprompt.Kind
		text *string
	}{{sessionprompt.KindResume, req.WakeResume}, {sessionprompt.KindNew, req.WakeNew}} {
		if w.text == nil {
			continue
		}
		_, ch, err := h.store.SaveWakeTemplate(ctx, wf, w.kind, *w.text, actor, now)
		if err != nil {
			respondDocError(c, err)
			return
		}
		changed = changed || ch
	}
	if changed {
		h.broadcastWorkflowPrompts(wf)
	}
	v, err := h.workflowPromptsViewFor(c, wf)
	if err != nil {
		respondDocError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"prompts": v, "changed": changed})
}

// PreviewWorkflowPrompts POST /api/v1/workflows/:workflow_id/prompts/preview  { "resident": string }
// 不保存，只返回「智能体在回执里实际看到的样子」（与回执头同一个格式化函数）、整理后的字数与会得到的版本号：
// 文字与已保存的一致就是当前版本，不一致就是保存后的下一个版本。
func (h *APIHandler) PreviewWorkflowPrompts(c *gin.Context) {
	wf := c.Param("workflow_id")
	var req struct {
		Resident *string `json:"resident"`
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 32<<10)
	if err := c.ShouldBindJSON(&req); err != nil || req.Resident == nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请带 resident"})
		return
	}
	text, err := store.NormalizePromptText(*req.Resident, model.ResidentPromptMax)
	if err != nil {
		respondDocError(c, err)
		return
	}
	row, err := h.store.GetWorkflowPrompt(c.Request.Context(), wf)
	if err != nil {
		respondDocError(c, err)
		return
	}
	rev := row.ResidentRev
	if text != row.Resident {
		rev++
	}
	c.JSON(http.StatusOK, gin.H{
		"preview": mcp.ResidentPreview(text, rev),
		"empty":   text == "",
		"length":  len([]rune(text)),
		"max":     model.ResidentPromptMax,
		"rev":     rev,
	})
}

// RestoreWorkflowPrompts POST /api/v1/workflows/:workflow_id/prompts/restore  { "revision_id": number }
// 把某个历史版本的文字存成新的当前版本（版本号继续往前走）。
func (h *APIHandler) RestoreWorkflowPrompts(c *gin.Context) {
	wf := c.Param("workflow_id")
	var req struct {
		RevisionID uint `json:"revision_id"`
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 1<<10)
	if err := c.ShouldBindJSON(&req); err != nil || req.RevisionID == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请带 revision_id"})
		return
	}
	_, changed, err := h.store.RestoreResidentRevision(c.Request.Context(), wf, req.RevisionID, c.GetString("auth_subject"), time.Now())
	if err != nil {
		respondDocError(c, err)
		return
	}
	if changed {
		h.broadcastWorkflowPrompts(wf)
	}
	v, err := h.workflowPromptsViewFor(c, wf)
	if err != nil {
		respondDocError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"prompts": v, "changed": changed})
}
