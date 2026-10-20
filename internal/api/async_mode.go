package api

import (
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/teacat99/RelayMesh/v2/internal/asyncmode"
	"github.com/teacat99/RelayMesh/v2/internal/model"
	"github.com/teacat99/RelayMesh/v2/internal/store"
)

// 异步对话模式 REST：只给 Web 登录用户用（/api/v1 下全部要求登录 JWT，MCP 凭据进不来）。
// 智能体不能自己打开异步模式——开关是用户的决定；它只能在回执里读到当前状态。

// asyncModeView 是 Web 看到的某个工作流的异步状态。
type asyncModeView struct {
	WorkflowID string `json:"workflow_id"`
	// Enabled 是用户的开关；Effective 才是真正生效（开关开着 且 hook 在线）。
	Enabled   bool   `json:"enabled"`
	Ever      bool   `json:"ever_enabled"`
	Effective bool   `json:"effective"`
	Mode      string `json:"mode"` // async | sync
	// Reason 在开关开着 / 开过却没生效时给出原因：disabled | no_hook | hook_stale。
	Reason     string `json:"reason,omitempty"`
	ReasonText string `json:"reason_text,omitempty"`
	// CanEnable：这个工作流至少收到过一次 hook 事件。从没收到过说明编辑器里没装 hook，开启没有意义（接口会拒绝）。
	CanEnable        bool       `json:"can_enable"`
	HookLastSeenAt   *time.Time `json:"hook_last_seen_at,omitempty"`
	HookFreshMinutes int        `json:"hook_fresh_minutes"`
	EnabledAt        *time.Time `json:"enabled_at,omitempty"`
	// ProfileID 是工作流选用的档位（0 表示跟随默认）；Profile 是解析之后实际生效的档位。
	ProfileID uint               `json:"profile_id"`
	Profile   *model.AuthProfile `json:"profile,omitempty"`
	// 回复窗口（#123）：智能体每发一条新消息累加 WindowAddMinutes，用户回复清零；到期无消息即可收尾。
	// ReplyWindowUntil 为空 = 当前没有在等回复。WaitSliceMinutes 是智能体单次计时器最长等待。
	ReplyWindowUntil *time.Time `json:"reply_window_until,omitempty"`
	// ReplyWindowPaused：智能体手头有进行中 / 验证中的任务，窗口暂停（只有任务清零后才走）；
	// 此时 ReplyWindowBankSec 是攒着的剩余秒数，ActiveTasks 是在做的任务数。
	ReplyWindowPaused  bool `json:"reply_window_paused,omitempty"`
	ReplyWindowBankSec int  `json:"reply_window_bank_sec,omitempty"`
	ActiveTasks        int  `json:"active_tasks,omitempty"`
	WindowAddMinutes   int  `json:"window_add_minutes"`
	WaitSliceMinutes   int  `json:"wait_slice_minutes"`
}

func (h *APIHandler) asyncModeViewFor(c *gin.Context, workflowID string) (asyncModeView, error) {
	ctx := c.Request.Context()
	info, err := h.store.WorkflowModeInfo(ctx, workflowID, time.Now())
	if err != nil {
		return asyncModeView{}, err
	}
	profile := info.Profile
	if profile == nil {
		if profile, err = h.store.ResolveAuthProfile(ctx, info.Row.ProfileID); err != nil {
			return asyncModeView{}, err
		}
	}
	v := asyncModeView{
		WorkflowID:       info.WorkflowID,
		Enabled:          info.State.Enabled,
		Ever:             info.State.Ever,
		Effective:        info.State.Effective,
		Mode:             info.State.Mode(),
		Reason:           string(info.State.Reason),
		ReasonText:       asyncmode.ReasonText(info.State.Reason),
		CanEnable:        !info.HookSeen.IsZero(),
		HookFreshMinutes: int(asyncmode.HookFreshness / time.Minute),
		EnabledAt:        info.Row.EnabledAt,
		ProfileID:        info.Row.ProfileID,
		Profile:          profile,
	}
	if info.State.Effective {
		if w, _, werr := h.store.ReconcileReplyWindow(ctx, workflowID, time.Now()); werr == nil {
			v.ReplyWindowUntil = w.Until
			v.ReplyWindowPaused = w.Paused()
			v.ReplyWindowBankSec = w.BankSec
			v.ActiveTasks = w.ActiveTasks
		}
	}
	add, slice := h.store.AsyncWindowTimes(ctx)
	v.WindowAddMinutes, v.WaitSliceMinutes = int(add/time.Minute), int(slice/time.Minute)
	if !info.HookSeen.IsZero() {
		at := info.HookSeen
		v.HookLastSeenAt = &at
	}
	return v, nil
}

func (h *APIHandler) broadcastWorkflowMode(workflowID string) {
	if h.broker == nil || workflowID == "" {
		return
	}
	h.broker.Broadcast("workflow_mode_updated", gin.H{"workflow_id": workflowID})
}

func (h *APIHandler) broadcastAuthProfiles() {
	if h.broker == nil {
		return
	}
	h.broker.Broadcast("auth_profiles_updated", gin.H{})
}

// GetAsyncMode GET /api/v1/workflows/:workflow_id/async-mode
func (h *APIHandler) GetAsyncMode(c *gin.Context) {
	v, err := h.asyncModeViewFor(c, c.Param("workflow_id"))
	if err != nil {
		respondDocError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"async_mode": v})
}

// UpdateAsyncMode PUT /api/v1/workflows/:workflow_id/async-mode  { "enabled"?: bool, "profile_id"?: number }
// 开启需要这个工作流收到过 hook 事件，否则 409 并说明怎么办；关闭永远允许。
func (h *APIHandler) UpdateAsyncMode(c *gin.Context) {
	wf := c.Param("workflow_id")
	var req struct {
		Enabled   *bool `json:"enabled"`
		ProfileID *uint `json:"profile_id"`
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 4<<10)
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请求格式不对：" + err.Error()})
		return
	}
	if req.Enabled == nil && req.ProfileID == nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "没有要修改的内容：请带 enabled 或 profile_id"})
		return
	}
	if _, err := h.store.UpdateWorkflowMode(c.Request.Context(), wf, store.ModePatch{Enabled: req.Enabled, ProfileID: req.ProfileID}, time.Now()); err != nil {
		respondDocError(c, err)
		return
	}
	h.broadcastWorkflowMode(wf)
	v, err := h.asyncModeViewFor(c, wf)
	if err != nil {
		respondDocError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"async_mode": v})
}

// ReplyWindow POST /api/v1/workflows/:workflow_id/reply-window  { "kind": "plus" | "typing" }
// 用户在 Web 上告诉服务端「我还在」：plus 给回复窗口加 1 分钟（可连点），typing 是输入心跳（窗口等于暂停倒计时）。
func (h *APIHandler) ReplyWindow(c *gin.Context) {
	wf := c.Param("workflow_id")
	var req struct {
		Kind string `json:"kind"`
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 1<<10)
	if err := c.ShouldBindJSON(&req); err != nil || (req.Kind != "plus" && req.Kind != "typing") {
		c.JSON(http.StatusBadRequest, gin.H{"error": "kind 必须是 plus 或 typing"})
		return
	}
	ctx := c.Request.Context()
	now := time.Now()
	info, err := h.store.WorkflowModeInfo(ctx, wf, now)
	if err != nil {
		respondDocError(c, err)
		return
	}
	if !info.State.Effective {
		c.JSON(http.StatusOK, gin.H{"status": "ok", "active": false})
		return
	}
	var w store.ReplyWindow
	if req.Kind == "plus" {
		w, err = h.store.ExtendReplyWindow(ctx, wf, time.Minute, now)
	} else {
		w, err = h.store.BeatReplyWindow(ctx, wf, now)
	}
	if err != nil {
		respondDocError(c, err)
		return
	}
	if req.Kind == "plus" {
		h.broadcastWorkflowMode(wf)
	}
	c.JSON(http.StatusOK, gin.H{"status": "ok", "active": !w.Empty(), "reply_window_until": w.Until, "reply_window_paused": w.Paused(), "reply_window_bank_sec": w.BankSec})
}

// authProfileLimits 把校验口径一并告诉界面，免得前端再抄一份常量。
func authProfileLimits() gin.H {
	return gin.H{"max": model.AuthProfileMax, "name_max": model.AuthProfileNameMax, "rules_max": model.AuthProfileRulesMax}
}

// ListAuthProfiles GET /api/v1/auth-profiles
func (h *APIHandler) ListAuthProfiles(c *gin.Context) {
	list, err := h.store.ListAuthProfiles(c.Request.Context())
	if err != nil {
		respondDocError(c, err)
		return
	}
	if list == nil {
		list = []model.AuthProfile{}
	}
	var defaultID uint
	for _, p := range list {
		if p.IsDefault {
			defaultID = p.ID
		}
	}
	c.JSON(http.StatusOK, gin.H{"items": list, "default_id": defaultID, "limits": authProfileLimits()})
}

type authProfileRequest struct {
	Name  *string `json:"name"`
	Level *string `json:"level"`
	Rules *string `json:"rules"`
	Sort  *int    `json:"sort"`
}

func (r authProfileRequest) input() store.AuthProfileInput {
	return store.AuthProfileInput{Name: r.Name, Level: r.Level, Rules: r.Rules, Sort: r.Sort}
}

// CreateAuthProfile POST /api/v1/auth-profiles
func (h *APIHandler) CreateAuthProfile(c *gin.Context) {
	var req authProfileRequest
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 8<<10)
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请求格式不对：" + err.Error()})
		return
	}
	p, err := h.store.CreateAuthProfile(c.Request.Context(), req.input())
	if err != nil {
		respondDocError(c, err)
		return
	}
	h.broadcastAuthProfiles()
	c.JSON(http.StatusCreated, gin.H{"item": p})
}

func profileIDParam(c *gin.Context) (uint, bool) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || id == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "档位编号不对"})
		return 0, false
	}
	return uint(id), true
}

// UpdateAuthProfile PUT /api/v1/auth-profiles/:id
func (h *APIHandler) UpdateAuthProfile(c *gin.Context) {
	id, ok := profileIDParam(c)
	if !ok {
		return
	}
	var req authProfileRequest
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 8<<10)
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请求格式不对：" + err.Error()})
		return
	}
	p, err := h.store.UpdateAuthProfile(c.Request.Context(), id, req.input())
	if err != nil {
		respondDocError(c, err)
		return
	}
	h.broadcastAuthProfiles()
	c.JSON(http.StatusOK, gin.H{"item": p})
}

// SetDefaultAuthProfile POST /api/v1/auth-profiles/:id/default
func (h *APIHandler) SetDefaultAuthProfile(c *gin.Context) {
	id, ok := profileIDParam(c)
	if !ok {
		return
	}
	p, err := h.store.SetDefaultAuthProfile(c.Request.Context(), id)
	if err != nil {
		respondDocError(c, err)
		return
	}
	h.broadcastAuthProfiles()
	c.JSON(http.StatusOK, gin.H{"item": p})
}

// DeleteAuthProfile DELETE /api/v1/auth-profiles/:id
// 默认档位不能删；选用了被删档位的工作流自动回落到默认档位（见 ResolveAuthProfile）。
func (h *APIHandler) DeleteAuthProfile(c *gin.Context) {
	id, ok := profileIDParam(c)
	if !ok {
		return
	}
	if err := h.store.DeleteAuthProfile(c.Request.Context(), id); err != nil {
		respondDocError(c, err)
		return
	}
	h.broadcastAuthProfiles()
	c.JSON(http.StatusOK, gin.H{"deleted": id})
}
