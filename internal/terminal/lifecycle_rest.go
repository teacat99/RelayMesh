package terminal

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/teacat99/RelayMesh/v2/internal/store"
	"github.com/teacat99/RelayMesh/v2/internal/termproto"
)

// 会话生命周期（H1）：人工终端默认永久保持，除非人明确选择「空闲回收」。
// 「空闲」= 没有人附着，并且没有输入；输出本身不算交互。强制执行者是主机上的守护进程，
// Hub 只负责保存默认值、下发策略和转发修改，所以 Hub 或 Agent 离线时计时照常进行。

// 空闲回收时长在 REST 层的取值范围（秒）：0 = 永久保持，其余 1 小时 ~ 30 天。
// 守护进程自身允许更小的值（termproto.MinIdleSec），只是界面不提供。
const (
	restMinIdleSec = 3600
	restMaxIdleSec = 30 * 24 * 3600
)

func validIdleSeconds(v int) bool {
	return v == 0 || (v >= restMinIdleSec && v <= restMaxIdleSec)
}

func badIdleResponse(c *gin.Context) {
	c.JSON(http.StatusBadRequest, gin.H{
		"error": fmt.Sprintf("空闲回收时长需为 0（空闲不回收）或 %d 秒到 %d 秒之间", restMinIdleSec, restMaxIdleSec),
		"code":  "bad_idle",
	})
}

type hostLifecycleRequest struct {
	IdleSeconds int `json:"idle_seconds"`
}

// handleHostLifecycle 修改一台主机上新会话的默认生命周期，并立即把新策略推给在线的 Agent。
// 已经单独设置过生命周期的会话不受影响。
func (h *Hub) handleHostLifecycle(c *gin.Context) {
	code, ok := machineCodeParam(c)
	if !ok {
		return
	}
	var req hostLifecycleRequest
	if !bindJSON(c, &req) {
		return
	}
	if !validIdleSeconds(req.IdleSeconds) {
		badIdleResponse(c)
		return
	}
	changed, err := h.store.SetTerminalHostIdle(c.Request.Context(), code, req.IdleSeconds)
	if err != nil {
		if errors.Is(err, store.ErrTerminalHostNotFound) {
			err = ErrHostNotFound
		}
		h.fail(c, err)
		return
	}
	if changed {
		h.audit.record(c.Request.Context(), auditEvent{
			Event: EvLifecycle, Actor: subjectOf(c), IP: c.ClientIP(), MachineCode: code,
			Detail: fmt.Sprintf("host_idle=%d", req.IdleSeconds),
		})
		if a := h.onlineAgent(code); a != nil && a.isEnabled() {
			_ = a.writeJSON(termproto.PolicyMsg{T: termproto.TPolicy, Policy: h.policyFor(c.Request.Context(), code)})
		}
		if lh := h.local.peek(code); lh != nil {
			lh.setHostIdle(req.IdleSeconds) // Hub 直连 SSH 主机的本地会话沿用这个默认值
		}
	}
	view, err := h.hostView(c.Request.Context(), code)
	if err != nil {
		h.fail(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"host": view})
}

type sessionLifecycleRequest struct {
	// IdleSeconds 为 null（或缺省）表示改回沿用主机默认，0 表示永久保持。
	IdleSeconds *int `json:"idle_seconds"`
}

// handleSessionLifecycle 修改一个会话自己的生命周期。
func (h *Hub) handleSessionLifecycle(c *gin.Context) {
	code, sid, ok := hostSessionParams(c)
	if !ok {
		return
	}
	var req sessionLifecycleRequest
	if !bindJSON(c, &req) {
		return
	}
	if req.IdleSeconds != nil && !validIdleSeconds(*req.IdleSeconds) {
		badIdleResponse(c)
		return
	}
	if lh := h.localSessions(c.Request.Context(), code); lh != nil {
		h.lifecycleLocalSession(c, lh, code, sid, req.IdleSeconds)
		return
	}
	a := h.agentFor(c, code)
	if a == nil {
		return
	}
	info, found := a.findSession(sid)
	if !found {
		c.JSON(http.StatusNotFound, gin.H{"error": "会话不存在", "code": "session_not_found"})
		return
	}
	if info.State == termproto.SessionExited {
		c.JSON(http.StatusConflict, gin.H{"error": "会话已经结束", "code": "session_exited"})
		return
	}
	if err := a.writeJSON(termproto.SetLifecycle{T: termproto.TLifecycle, SID: sid, IdleSec: req.IdleSeconds}); err != nil {
		c.JSON(http.StatusConflict, gin.H{"error": "主机当前不在线", "code": "host_offline"})
		return
	}
	detail := "idle=inherit"
	if req.IdleSeconds != nil {
		detail = fmt.Sprintf("idle=%d", *req.IdleSeconds)
	}
	h.audit.record(c.Request.Context(), auditEvent{
		Event: EvLifecycle, Actor: subjectOf(c), IP: c.ClientIP(), MachineCode: code, SessionID: sid, Detail: detail,
	})
	c.JSON(http.StatusOK, gin.H{"ok": true})
}
