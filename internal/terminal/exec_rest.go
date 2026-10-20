package terminal

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
	"github.com/teacat99/RelayMesh/v2/internal/termexec"
	"github.com/teacat99/RelayMesh/v2/internal/termproto"
)

// 模型运行的人类侧 REST。所有路由都挂在终端的 guard 之后：总闸关闭时一律拒绝，MCP 令牌一律拒绝。
//
// 安全刹车（暂停 / 取消 / 拒绝）只需登录，越快越好；
// 会放宽权限或暴露数据的操作（批准 / 恢复 / 读取完整输出 / 修改主机的模型设置）还需要解锁。

func (h *Hub) mountExec(login, unlocked *gin.RouterGroup) {
	login.GET("/exec/status", h.handleExecStatus)
	login.GET("/exec/threads", h.handleExecThreads)
	login.GET("/exec/runs", h.handleExecRuns)
	login.GET("/exec/runs/:id", h.handleExecRun)
	login.POST("/exec/runs/:id/cancel", h.handleExecCancel)
	login.POST("/exec/runs/:id/reject", h.handleExecReject)
	login.POST("/exec/pause", h.handleExecPause)
	login.GET("/exec/events", h.handleExecEvents)

	unlocked.POST("/exec/runs/:id/approve", h.handleExecApprove)
	unlocked.GET("/exec/runs/:id/output", h.handleExecOutput)
	unlocked.POST("/exec/resume", h.handleExecResume)
	unlocked.POST("/hosts/:code/model", h.handleHostModel)
	// 把一台主机写入现有模型令牌的主机名单：先预览（只读，登录即可），再执行（解锁）。
	login.GET("/hosts/:code/grant-models", h.handleGrantModelsPreview)
	unlocked.POST("/hosts/:code/grant-models", h.handleGrantModels)
}

// ExecStatus 是模型执行的整体状态（供界面决定显示什么入口与提示）。
type ExecStatus struct {
	// Enabled 是服务端总开关（RELAYMESH_TERMINAL_EXEC）；Available 还要求终端总闸开启。
	Enabled       bool   `json:"enabled"`
	FilesEnabled  bool   `json:"files_enabled"` // 文件能力（RELAYMESH_TERMINAL_FILES）是否开启
	Available     bool   `json:"available"`
	Reason        string `json:"reason,omitempty"`
	Paused        bool   `json:"paused"`
	Active        int    `json:"active"`
	Awaiting      int    `json:"awaiting"`
	Subscribers   int    `json:"subscribers"`
	RetentionDays int    `json:"retention_days"`
}

// Status 汇总模型执行的状态。
func (s *ExecService) Status(ctx context.Context) ExecStatus {
	st := ExecStatus{Enabled: s.enabled, FilesEnabled: s.h.files.enabled, RetentionDays: int(execRetention.Hours() / 24), Subscribers: s.bus.count()}
	switch {
	case !s.enabled:
		st.Reason = "exec_off"
	default:
		if d := s.h.gate.Evaluate(ctx); !d.Open {
			st.Reason = string(d.Reason)
		} else {
			st.Available = true
		}
	}
	for _, lr := range s.snapshotLive() {
		rec := lr.snapshot()
		switch rec.State {
		case model.RunAwaitingApproval:
			st.Awaiting++
			st.Active++
		case model.RunRunning:
			st.Active++
		}
	}
	st.Paused = s.isPaused()
	return st
}

// ActiveOnHost 返回某台主机上进行中的模型运行数（含待批准）。
func (s *ExecService) ActiveOnHost(machine string) int {
	n := 0
	for _, lr := range s.snapshotLive() {
		rec := lr.snapshot()
		if rec.Target == machine && !model.IsFinalRunState(rec.State) {
			n++
		}
	}
	return n
}

func (h *Hub) handleExecStatus(c *gin.Context) {
	c.JSON(http.StatusOK, h.exec.Status(c.Request.Context()))
}

// ThreadView 是模型线程的汇总（侧栏条目用）。线程键 = 凭据 × 主机 × 线程标签，混合令牌且带 workflow_id 时为 工作流 × 主机。
type ThreadView struct {
	Key            string    `json:"key"`
	Target         string    `json:"target"`
	CredentialID   uint      `json:"credential_id"`
	CredentialName string    `json:"credential_name"`
	ClientName     string    `json:"client_name,omitempty"`
	Thread         string    `json:"thread,omitempty"`
	WorkflowID     string    `json:"workflow_id,omitempty"`
	Total          int       `json:"total"`
	Active         int       `json:"active"`
	Awaiting       int       `json:"awaiting"`
	FirstAt        time.Time `json:"first_at"`
	LastAt         time.Time `json:"last_at"`
	LastRunID      string    `json:"last_run_id"`
	LastState      string    `json:"last_state"`
	LastTitle      string    `json:"last_title"`
}

// runTitle 取一次运行的显示标题：模型填写的意图，没有则取命令的第一行。
func runTitle(r *model.TerminalRun) string {
	t := strings.TrimSpace(r.Description)
	if t == "" {
		t, _, _ = strings.Cut(strings.TrimSpace(r.Command), "\n")
	}
	return label(t, 80)
}

// Threads 汇总模型线程。线程按最近一次运行倒序，计数来自数据库（状态变更都先落库再广播）。
func (s *ExecService) Threads(ctx context.Context, f store.ThreadFilter) ([]ThreadView, error) {
	stats, err := s.h.store.ListTerminalThreads(ctx, f)
	if err != nil {
		return nil, err
	}
	ids := make([]uint, 0, len(stats)*2)
	for _, st := range stats {
		ids = append(ids, st.FirstID)
		if st.LastID != st.FirstID {
			ids = append(ids, st.LastID)
		}
	}
	rows, err := s.h.store.ListTerminalRunsByIDs(ctx, ids)
	if err != nil {
		return nil, err
	}
	byID := make(map[uint]*model.TerminalRun, len(rows))
	for i := range rows {
		byID[rows[i].ID] = &rows[i]
	}
	out := make([]ThreadView, 0, len(stats))
	for _, st := range stats {
		last, first := byID[st.LastID], byID[st.FirstID]
		if last == nil {
			continue // 统计之后被清理：下一次刷新自然消失
		}
		if first == nil {
			first = last
		}
		out = append(out, ThreadView{
			Key:            st.ThreadKey,
			Target:         last.Target,
			CredentialID:   last.CredentialID,
			CredentialName: last.CredentialName,
			ClientName:     last.ClientName,
			Thread:         last.Thread,
			WorkflowID:     last.WorkflowID,
			Total:          int(st.Total),
			Active:         int(st.Active),
			Awaiting:       int(st.Awaiting),
			FirstAt:        first.CreatedAt,
			LastAt:         last.UpdatedAt,
			LastRunID:      last.RunID,
			LastState:      last.State,
			LastTitle:      runTitle(last),
		})
	}
	return out, nil
}

func (h *Hub) handleExecThreads(c *gin.Context) {
	f := store.ThreadFilter{Target: strings.TrimSpace(c.Query("target"))}
	if v := strings.TrimSpace(c.Query("credential")); v != "" {
		n, err := strconv.ParseUint(v, 10, 32)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "credential 参数无效", "code": "bad_request"})
			return
		}
		f.CredentialID = uint(n)
	}
	if v := strings.TrimSpace(c.Query("limit")); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "limit 参数无效", "code": "bad_request"})
			return
		}
		f.Limit = n
	}
	threads, err := h.exec.Threads(c.Request.Context(), f)
	if err != nil {
		h.fail(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"threads": threads})
}

func (h *Hub) handleExecRuns(c *gin.Context) {
	f := store.RunFilter{
		Target:    strings.TrimSpace(c.Query("target")),
		ThreadKey: strings.TrimSpace(c.Query("thread")),
	}
	if v := strings.TrimSpace(c.Query("credential")); v != "" {
		n, err := strconv.ParseUint(v, 10, 32)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "credential 参数无效"})
			return
		}
		f.CredentialID = uint(n)
	}
	if v := strings.TrimSpace(c.Query("state")); v != "" {
		for _, st := range strings.Split(v, ",") {
			if st = strings.TrimSpace(st); st != "" {
				f.States = append(f.States, st)
			}
		}
	}
	if v := strings.TrimSpace(c.Query("before")); v != "" {
		n, err := strconv.ParseUint(v, 10, 32)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "before 参数无效"})
			return
		}
		f.BeforeID = uint(n)
	}
	if v := strings.TrimSpace(c.Query("limit")); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "limit 参数无效"})
			return
		}
		f.Limit = n
	}
	runs, err := h.store.ListTerminalRuns(c.Request.Context(), f)
	if err != nil {
		h.fail(c, err)
		return
	}
	var cursor uint
	if n := len(runs); n > 0 {
		cursor = runs[n-1].ID
	}
	c.JSON(http.StatusOK, gin.H{"runs": runs, "cursor": cursor})
}

func runIDParam(c *gin.Context) (string, bool) {
	id := strings.ToLower(strings.TrimSpace(c.Param("id")))
	if !termproto.ValidRunID(id) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "运行 ID 格式无效", "code": "bad_request"})
		return "", false
	}
	return id, true
}

func (h *Hub) handleExecRun(c *gin.Context) {
	id, ok := runIDParam(c)
	if !ok {
		return
	}
	rec, err := h.store.GetTerminalRun(c.Request.Context(), id)
	if err != nil {
		h.fail(c, err)
		return
	}
	if rec == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "运行不存在或已被清理", "code": "run_not_found"})
		return
	}
	if lr := h.exec.liveRun(id); lr != nil {
		live := lr.snapshot()
		rec = &live
	}
	c.JSON(http.StatusOK, gin.H{"run": rec})
}

// execFail 把运行操作的错误翻译成 HTTP 响应。
func (h *Hub) execFail(c *gin.Context, err error) {
	var te *termexec.Error
	switch {
	case errors.Is(err, ErrRunNotFound):
		c.JSON(http.StatusNotFound, gin.H{"error": "运行不存在、已结束或已被清理", "code": "run_not_found"})
	case errors.Is(err, ErrRunNotAwaiting):
		c.JSON(http.StatusConflict, gin.H{"error": "这次运行不在待批准状态", "code": "not_awaiting"})
	case errors.Is(err, store.ErrTerminalHostNotFound):
		c.JSON(http.StatusNotFound, gin.H{"error": "主机不存在", "code": "host_not_found"})
	case errors.As(err, &te):
		c.JSON(http.StatusConflict, gin.H{"error": te.Message, "code": te.Code})
	default:
		h.fail(c, err)
	}
}

func (h *Hub) handleExecCancel(c *gin.Context) {
	id, ok := runIDParam(c)
	if !ok {
		return
	}
	rec, err := h.exec.CancelByHuman(c.Request.Context(), id, subjectOf(c), c.ClientIP())
	if err != nil {
		h.execFail(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"run": rec})
}

func (h *Hub) handleExecReject(c *gin.Context) {
	id, ok := runIDParam(c)
	if !ok {
		return
	}
	rec, err := h.exec.Reject(c.Request.Context(), id, subjectOf(c), c.ClientIP())
	if err != nil {
		h.execFail(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"run": rec})
}

func (h *Hub) handleExecApprove(c *gin.Context) {
	id, ok := runIDParam(c)
	if !ok {
		return
	}
	rec, err := h.exec.Approve(c.Request.Context(), id, subjectOf(c), c.ClientIP())
	if err != nil {
		h.execFail(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"run": rec})
}

type pauseRequest struct {
	Scope       string `json:"scope"` // all | host
	MachineCode string `json:"machine_code"`
}

func (h *Hub) handlePause(c *gin.Context, paused bool) {
	var req pauseRequest
	if !bindJSON(c, &req) {
		return
	}
	req.Scope = strings.ToLower(strings.TrimSpace(req.Scope))
	if req.Scope == "host" {
		code, err := termproto.NormalizeMachineCode(req.MachineCode)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "机器码格式不正确", "code": "bad_machine_code"})
			return
		}
		req.MachineCode = code
	}
	if err := h.exec.SetPaused(c.Request.Context(), req.Scope, req.MachineCode, paused, subjectOf(c), c.ClientIP()); err != nil {
		if req.Scope != "all" && req.Scope != "host" {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error(), "code": "bad_request"})
			return
		}
		h.execFail(c, err)
		return
	}
	c.JSON(http.StatusOK, h.exec.Status(c.Request.Context()))
}

func (h *Hub) handleExecPause(c *gin.Context)  { h.handlePause(c, true) }
func (h *Hub) handleExecResume(c *gin.Context) { h.handlePause(c, false) }

type hostModelRequest struct {
	AllowModel *bool   `json:"allow_model"`
	ExecMode   *string `json:"exec_mode"`
	// DefaultForNewModels 是这台主机对新建模型授权的默认态（allow / deny）。改它只影响以后新建的令牌，
	// 不会改动任何现有令牌的主机名单；要给现有令牌授予，请用 grant-models。
	DefaultForNewModels *string `json:"default_for_new_models"`
}

func (h *Hub) handleHostModel(c *gin.Context) {
	code, ok := machineCodeParam(c)
	if !ok {
		return
	}
	var req hostModelRequest
	if !bindJSON(c, &req) {
		return
	}
	if req.AllowModel == nil && req.ExecMode == nil && req.DefaultForNewModels == nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "没有需要修改的字段", "code": "bad_request"})
		return
	}
	if req.ExecMode != nil {
		if err := store.ValidateHostExecMode(*req.ExecMode); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error(), "code": "bad_request"})
			return
		}
	}
	if req.DefaultForNewModels != nil {
		if err := store.ValidateHostDefaultForNewModels(*req.DefaultForNewModels); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error(), "code": "bad_request"})
			return
		}
	}
	upd := store.HostModelUpdate{AllowModel: req.AllowModel, ExecMode: req.ExecMode, DefaultForNewModels: req.DefaultForNewModels}
	if _, err := h.exec.ModelSettings(c.Request.Context(), code, upd, subjectOf(c), c.ClientIP()); err != nil {
		h.execFail(c, err)
		return
	}
	view, err := h.hostView(c.Request.Context(), code)
	if err != nil {
		h.fail(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"host": view})
}

// grantableHost 取出一台可以被授予给模型令牌的主机：必须是已启用的持久主机（临时主机只存在内存里，不写进令牌名单）。
func (h *Hub) grantableHost(c *gin.Context, code string) bool {
	rec, err := h.store.GetTerminalHost(c.Request.Context(), code)
	if err != nil {
		h.fail(c, err)
		return false
	}
	if rec == nil || rec.Status != termproto.HostStatusEnabled {
		h.fail(c, ErrHostNotFound)
		return false
	}
	return true
}

// handleGrantModelsPreview 预览「把这台主机授予现有模型令牌」会改动哪些令牌（只读，不写任何数据）。
// 待启用的持久主机也可以预览：启用前就能在确认框里告诉管理员「将授予 N 个模型」。
func (h *Hub) handleGrantModelsPreview(c *gin.Context) {
	code, ok := machineCodeParam(c)
	if !ok {
		return
	}
	if e, pending := h.pending.get(code); !(pending && e.Meta.Kind != termproto.KindTemporary) && !h.grantableHost(c, code) {
		return
	}
	res, err := h.store.PreviewHostGrant(c.Request.Context(), code)
	if err != nil {
		h.fail(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"grant": res})
}

// handleGrantModels 把这台主机写入现有模型令牌的主机名单（只增不减；已有授权与通配令牌不动）。
func (h *Hub) handleGrantModels(c *gin.Context) {
	code, ok := machineCodeParam(c)
	if !ok || !h.grantableHost(c, code) {
		return
	}
	res, err := h.grantHostToModels(c.Request.Context(), code, "manual", subjectOf(c), c.ClientIP())
	if err != nil {
		h.fail(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"grant": res})
}

// ReadOutput 供人类读取某次运行的完整输出的一页（不经过数据库，原样来自主机；仅做 UTF-8 规整）。
func (s *ExecService) ReadOutput(ctx context.Context, runID, stream string, off uint64, limit int) (*RunPage, string, error) {
	rec, err := s.h.store.GetTerminalRun(ctx, runID)
	if err != nil {
		return nil, "", err
	}
	if rec == nil {
		return nil, "", ErrRunNotFound
	}
	a := s.peerFor(rec.Target)
	if a == nil || !a.usable() {
		return nil, "", execErr(termexec.CodeHostOffline, "主机当前不在线，无法读取输出")
	}
	rctx, cancel := context.WithTimeout(ctx, runReadTimeout)
	defer cancel()
	page, err := a.readRun(rctx, runID, stream, off, limit)
	if err != nil {
		switch {
		case errors.Is(err, errRunReadNotFound):
			return nil, "", execErr(termexec.CodeOutputGone, "主机上已找不到这次运行的输出（可能已过保留期被清理）")
		case errors.Is(err, errRunReadBusy):
			return nil, "", execErr(termexec.CodeBusy, "主机正忙于读取其他输出，请稍后重试")
		}
		return nil, "", execErr(termexec.CodeOutputGone, "读取输出失败：%v", err)
	}
	data, next := page.Data, page.Next
	if !(page.EOF && next >= page.Total) {
		kept, cut := trimIncompleteTail(data)
		data, next = kept, next-uint64(cut)
	}
	page.Data, page.Next = data, next
	return page, strings.ToValidUTF8(string(data), "\uFFFD"), nil
}

func (h *Hub) handleExecOutput(c *gin.Context) {
	id, ok := runIDParam(c)
	if !ok {
		return
	}
	stream := strings.ToLower(strings.TrimSpace(c.DefaultQuery("stream", "stdout")))
	if _, ok := termproto.ParseStream(stream); !ok {
		c.JSON(http.StatusBadRequest, gin.H{"error": "stream 只能是 stdout 或 stderr", "code": "bad_request"})
		return
	}
	var off uint64
	if v := strings.TrimSpace(c.Query("offset")); v != "" {
		n, err := strconv.ParseUint(v, 10, 63)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "offset 参数无效", "code": "bad_request"})
			return
		}
		off = n
	}
	limit := 0
	if v := strings.TrimSpace(c.Query("limit")); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "limit 参数无效", "code": "bad_request"})
			return
		}
		limit = n
	}
	page, text, err := h.exec.ReadOutput(c.Request.Context(), id, stream, off, limit)
	if err != nil {
		h.execFail(c, err)
		return
	}
	h.audit.recordLimited(c.Request.Context(), subjectOf(c)+"|"+id, auditEvent{
		Event: EvExecRead, Actor: subjectOf(c), IP: c.ClientIP(), ActorKind: ActorHuman, RunID: id,
		Detail: "stream=" + stream,
	})
	c.JSON(http.StatusOK, gin.H{
		"run_id":      id,
		"stream":      stream,
		"offset":      page.Off,
		"next_offset": page.Next,
		"total_bytes": page.Total,
		"text":        text,
		"eof":         page.EOF && page.Next >= page.Total,
		"running":     page.Running,
		"gap":         page.Gap,
	})
}
