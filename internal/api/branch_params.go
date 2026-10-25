package api

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/teacat99/RelayMesh/v2/internal/model"
	"github.com/teacat99/RelayMesh/v2/internal/store"
)

// 分支会话「会话参数」（#236 P3）的 Web 接口，以及「放宽授权要终端解锁」的把关。
// 设计见 .cursor/planning/2026-10-24-236-p3-impl-plan.md。
//
//   - 放宽开放范围（「改参数」档位调高、「启动」从关到开）要终端二次解锁，与令牌管理里「授予终端能力」同一把锁：
//     主会话经 session_params 改的是分支的常驻规则 / 阶段 / 唤醒词，档位调高等于让一个 Agent 获得改另一个 Agent 行为的能力，
//     不能只凭一个登录令牌；收紧（档位调低、启动关掉）与无关项（读历史、发消息、定夺、任务）只需登录，刹车不设门槛。
//   - 终端总闸关着（没设密码 / 密钥偏弱 / 被运维关闭）时没人能解锁：不拒绝整个请求，把放宽的那两项压回当前取值、其余照办，
//     响应里的 clamped 列出被压回的项（params / launch），界面据此提示「要先开启终端并解锁」。
//   - GET  /workflows/:workflow_id/branch/params-events                          主会话改过的参数记录（新的在前，每条带「现在能不能撤销」）；
//   - POST /workflows/:workflow_id/branch/params-events/:event_id/revert          撤销主会话的一次修改（只有人能撤销；撤销是收紧类操作，登录即可）。
//
// workflow_id 一律是分支自己的编号（与 PUT /workflows/:workflow_id/branch 一致）。

// terminalGateOpen 报告终端总闸现在是否开着；没有注入终端 Hub 视为关。
func (h *APIHandler) terminalGateOpen(ctx context.Context) bool {
	return h.terminal != nil && h.terminal.Enabled(ctx)
}

// gateBranchWiden 处理一次「把开放范围从 before 改成 want」里放宽的部分（放宽的定义见 model.ExposeWidens）：
//   - 没有放宽：原样放行，不要求解锁；
//   - 有放宽、终端总闸开着：要求终端解锁，没解锁时已写入 403 响应，ok=false，调用方直接返回；
//   - 有放宽、终端总闸关着：没人能解锁，把放宽的项压回 before 的取值（final 是压回之后的开放范围，clamped 是被压回的项名）。
func (h *APIHandler) gateBranchWiden(c *gin.Context, before, want model.BranchExpose) (final model.BranchExpose, clamped []string, ok bool) {
	if !model.ExposeWidens(before, want) {
		return want, nil, true
	}
	if h.terminalGateOpen(c.Request.Context()) {
		if !h.requireTerminalUnlock(c) {
			return want, nil, false
		}
		return want, nil, true
	}
	final, clamped = model.ClampExposeWiden(before, want)
	return final, clamped, true
}

// requestedExpose 取一次请求想要的开放范围：预设名与显式取值二选一。没带、两个都带、预设不认识、取值不合法一律返回 false，
// 交给 store 用它自己的错误信息拒绝，这里不抢着报错。
func requestedExpose(preset string, expose *model.BranchExpose) (model.BranchExpose, bool) {
	p := strings.TrimSpace(preset)
	switch {
	case p != "" && expose != nil:
		return model.BranchExpose{}, false
	case p != "":
		return model.BranchExposePreset(p)
	case expose != nil:
		if expose.Validate() != nil {
			return model.BranchExpose{}, false
		}
		return *expose, true
	}
	return model.BranchExpose{}, false
}

// gateBranchUpdate 给「改分支设置」把关：以这个分支现在的开放范围为基准判断放宽。
// 终端不可用而发生压回时，把请求改写成显式取值（预设名换成压回之后的 expose），由 store 照常校验与落库。
// 这条分支还不是任何会话的分支时什么都不做（store 会报 404）。基准取自写入之前读到的关系：
// 两次人工操作在毫秒级同时发生时可能用到旧值，这只会让一次本来需要解锁的放宽少问一次，而发起者必须是已登录的本人，不构成绕过。
func (h *APIHandler) gateBranchUpdate(c *gin.Context, branch string, in *store.UpdateBranchInput) (clamped []string, ok bool) {
	cur, err := h.store.BranchOf(c.Request.Context(), branch)
	if err != nil || cur == nil {
		return nil, true
	}
	want, valid := requestedExpose(in.Preset, in.Expose)
	if !valid {
		return nil, true
	}
	final, clamped, ok := h.gateBranchWiden(c, cur.Expose, want)
	if !ok {
		return nil, false
	}
	if len(clamped) > 0 {
		in.Preset = ""
		in.Expose = &final
	}
	return clamped, true
}

// newBranchBaseline 是「新建一个分支」时的放宽基准：什么都还没开放，改参数是 off、启动是关。
var newBranchBaseline = model.BranchExpose{Params: model.ExposeParamsOff}

// gateBranchCreate 给「网页新建分支」把关：协作预设（改常驻规则）与托管预设（改全部参数、启动）相对「什么都没开放」是放宽，
// 要终端解锁；仅可见预设不要。终端不可用时同样压回，并把预设名换成压回之后的显式取值。
func (h *APIHandler) gateBranchCreate(c *gin.Context, spec *store.CreateWebBranchSpec) (clamped []string, ok bool) {
	name := strings.TrimSpace(spec.ExposePreset)
	if name == "" && spec.Expose == nil {
		name = model.ExposePresetCollab // 与 store 的缺省一致
	}
	want, valid := requestedExpose(name, spec.Expose)
	if !valid {
		return nil, true
	}
	final, clamped, ok := h.gateBranchWiden(c, newBranchBaseline, want)
	if !ok {
		return nil, false
	}
	if len(clamped) > 0 {
		spec.ExposePreset = ""
		spec.Expose = &final
	}
	return clamped, true
}

// branchParamEventJSON 是列表里的一条参数记录：不含旧值 / 新值全文（常驻规则与唤醒词的全文在各自的弹窗里看，这里只要一句话）。
type branchParamEventJSON struct {
	ID            uint       `json:"id"`
	CreatedAt     time.Time  `json:"created_at"`
	Field         string     `json:"field"`
	FieldLabel    string     `json:"field_label"`
	By            string     `json:"by"`
	Summary       string     `json:"summary"`
	Rev           int        `json:"rev,omitempty"`
	Told          bool       `json:"told"`
	RevertedAt    *time.Time `json:"reverted_at,omitempty"`
	RevertedBy    string     `json:"reverted_by,omitempty"`
	RevertOf      uint       `json:"revert_of,omitempty"`
	Revertable    bool       `json:"revertable"`
	RevertBlocked string     `json:"revert_blocked,omitempty"`
}

func paramEventJSON(ev model.BranchParamEvent, revertable bool, blocked string) branchParamEventJSON {
	return branchParamEventJSON{
		ID: ev.ID, CreatedAt: ev.CreatedAt, Field: ev.Field, FieldLabel: model.ParamFieldLabel(ev.Field), By: ev.By,
		Summary: ev.Summary, Rev: ev.Rev, Told: ev.ToldAt != nil, RevertedAt: ev.RevertedAt, RevertedBy: ev.RevertedBy, RevertOf: ev.RevertOf,
		Revertable: revertable, RevertBlocked: blocked,
	}
}

// ListBranchParamEvents GET /api/v1/workflows/:workflow_id/branch/params-events?limit=
// 默认 50 条、最多 200 条（库里每个分支本来就只留最近 200 条）。这个工作流不是任何会话的分支返回 404。
func (h *APIHandler) ListBranchParamEvents(c *gin.Context) {
	ctx := c.Request.Context()
	wf := c.Param("workflow_id")
	rel, err := h.store.BranchOf(ctx, wf)
	if err != nil {
		respondDocError(c, err)
		return
	}
	if rel == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "工作流 \"" + strings.TrimSpace(wf) + "\" 不是任何会话的分支"})
		return
	}
	limit, _ := strconv.Atoi(c.Query("limit"))
	views, err := h.store.ListBranchParamEventViews(ctx, wf, limit, time.Now())
	if err != nil {
		respondDocError(c, err)
		return
	}
	rows := make([]branchParamEventJSON, 0, len(views))
	for _, v := range views {
		rows = append(rows, paramEventJSON(v.BranchParamEvent, v.Revertable, v.RevertBlocked))
	}
	c.JSON(http.StatusOK, gin.H{"events": rows})
}

// RevertBranchParamEvent POST /api/v1/workflows/:workflow_id/branch/params-events/:event_id/revert
// 只有人能撤销（登录令牌，actor 记登录者）。规则在 store：只能撤销主会话写入的、该字段最新一条未撤销的、当前值仍是它写入的值的修改，
// 否则 409 并写明原因；撤销与关系是否已结束无关。成功后广播 branch_changed(params_reverted) 与被改字段原有的刷新事件。
func (h *APIHandler) RevertBranchParamEvent(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("event_id"), 10, 64)
	if err != nil || id == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "event_id 必须是正整数"})
		return
	}
	ctx := c.Request.Context()
	wf := c.Param("workflow_id")
	res, err := h.store.RevertBranchParamEvent(ctx, wf, uint(id), c.GetString("auth_subject"), time.Now())
	if err != nil {
		var ae *store.AppError
		if !errors.As(err, &ae) {
			// 改回旧值之后记录环节出错这类半途失败：原因是我们自己写的话，如实交给用户。
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		respondDocError(c, err)
		return
	}
	h.broadcastBranchChanged(res.Event.ParentWorkflowID, wf, "params_reverted")
	for _, ef := range h.store.ParamEffects(ctx, wf, []model.BranchParamEvent{res.Event}) {
		h.emitParamEffect(ef)
	}
	c.JSON(http.StatusOK, gin.H{
		"status":   "ok",
		"reverted": paramEventJSON(res.Reverted, false, ""),
		"event":    paramEventJSON(res.Event, false, ""),
	})
}

// emitParamEffect 广播 store.ParamEffects 给出的一条事件；会话对象走与其它 session_updated 相同的载荷规整。
func (h *APIHandler) emitParamEffect(ef store.ParamEffect) {
	if h.broker == nil {
		return
	}
	if sess, ok := ef.Payload.(*model.FeedbackSession); ok {
		h.broadcastSession(ef.Event, sess)
		return
	}
	h.broker.Broadcast(ef.Event, ef.Payload)
}
