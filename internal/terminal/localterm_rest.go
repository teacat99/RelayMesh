package terminal

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/teacat99/RelayMesh/v2/internal/model"
	"github.com/teacat99/RelayMesh/v2/internal/sshexec"
	"github.com/teacat99/RelayMesh/v2/internal/termproto"
)

// Hub 直连 SSH 主机的人工终端在 REST 层的入口（引擎见 localterm.go）。
// 会话相关的各个接口先问 localSessions：是直连 SSH 主机就走本地会话，否则仍按 Agent 主机处理。

// directSSHRecord 返回机器码对应的 Hub 直连 SSH 主机记录（任意状态）；不是直连 SSH 主机时返回 nil。
func (h *Hub) directSSHRecord(ctx context.Context, code string) *model.TerminalHost {
	rec, err := h.store.GetTerminalHost(ctx, code)
	if err != nil || rec == nil || !rec.IsSSH() || rec.ViaGateway() {
		return nil
	}
	return rec
}

// localSessions 返回 Hub 直连 SSH 主机（code）的本地会话集合；code 不是直连 SSH 主机时返回 nil。
func (h *Hub) localSessions(ctx context.Context, code string) *localHost {
	if lh := h.local.peek(code); lh != nil {
		return lh
	}
	if h.directSSHRecord(ctx, code) == nil {
		return nil
	}
	return h.local.host(code)
}

// endLocalSessions 终止一台直连 SSH 主机上的全部本地会话（停用 / 删除 / 改了地址或用户之后），并写审计。
// 会话运行在 Hub 里，不会随主机记录一起消失；不处理的话，管理员以为已经切断的连接其实还开着。
func (h *Hub) endLocalSessions(ctx context.Context, code, reason, actor, ip string) int {
	n := h.local.endHost(code)
	if n > 0 {
		h.audit.record(ctx, auditEvent{
			Event: EvSessionKill, Actor: actor, IP: ip, MachineCode: code,
			Detail: "ssh:direct " + reason + " sessions=" + strconv.Itoa(n),
		})
	}
	return n
}

// openLocalSession 在 Hub 直连的 SSH 主机上打开人工终端：Hub 出站建立 SSH 连接，会话持有在 Hub 里。
func (h *Hub) openLocalSession(c *gin.Context, rec *model.TerminalHost, req openSessionRequest) {
	ctx := c.Request.Context()
	code := rec.MachineCode
	if rec.Status != termproto.HostStatusEnabled {
		c.JSON(http.StatusNotFound, gin.H{"error": "SSH 主机不存在或已停用", "code": "host_not_found"})
		return
	}
	if req.IdleSeconds != nil && !validIdleSeconds(*req.IdleSeconds) {
		badIdleResponse(c)
		return
	}
	spec, err := h.ssh.buildSpec(*rec)
	if err != nil {
		h.sshFail(c, mapSSHErr(err))
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		name = firstNonEmpty(strings.TrimSpace(rec.Remark), termproto.SSHLabel(rec.SSHUser, rec.SSHAddr))
	}
	lh := h.local.host(code)
	lh.setHostIdle(rec.IdleSec)
	info, err := lh.open(ctx, localOpen{
		Name: name, Rows: req.Rows, Cols: req.Cols, IdleSec: req.IdleSeconds,
		Dial: func(ctx context.Context) (*sshexec.Client, error) {
			cl, derr := sshexec.Dial(ctx, spec, sshexec.DefaultDialTimeout)
			h.ssh.noteResult(code, derr) // 与模型执行共用同一份健康状态：连不上 / 密钥不一致会反映在主机卡片上
			return cl, derr
		},
	})
	if err != nil {
		h.audit.record(ctx, auditEvent{
			Event: EvSessionOpen, Actor: subjectOf(c), IP: c.ClientIP(), MachineCode: code, Result: ResultFail, Detail: "ssh:direct",
		})
		switch {
		case errors.Is(err, errSessionLimit):
			c.JSON(http.StatusConflict, gin.H{"error": "该主机的会话数已达上限", "code": "session_limit"})
		case errors.Is(err, errLocalClosed):
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "服务正在关闭", "code": "shutting_down"})
		default:
			h.sshFail(c, mapSSHErr(err))
		}
		return
	}
	h.audit.record(ctx, auditEvent{
		Event: EvSessionOpen, Actor: subjectOf(c), IP: c.ClientIP(), MachineCode: code, SessionID: info.SID, Detail: "ssh:direct",
	})
	c.JSON(http.StatusOK, gin.H{"session": info})
}

// killLocalSession 终止（运行中）或清除（已退出）一个本地会话。
func (h *Hub) killLocalSession(c *gin.Context, lh *localHost, code, sid string) {
	s := lh.get(sid)
	if s == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "会话不存在", "code": "session_not_found"})
		return
	}
	s.kill()
	h.audit.record(c.Request.Context(), auditEvent{
		Event: EvSessionKill, Actor: subjectOf(c), IP: c.ClientIP(), MachineCode: code, SessionID: sid, Detail: "ssh:direct",
	})
	c.JSON(http.StatusAccepted, gin.H{"ok": true})
}

// renameLocalSession 修改本地会话的名称。
func (h *Hub) renameLocalSession(c *gin.Context, lh *localHost, sid, name string) {
	s := lh.get(sid)
	if s == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "会话不存在", "code": "session_not_found"})
		return
	}
	s.rename(name)
	c.JSON(http.StatusAccepted, gin.H{"ok": true})
}

// lifecycleLocalSession 修改本地会话自己的空闲回收时长（nil = 改回沿用主机默认）。
func (h *Hub) lifecycleLocalSession(c *gin.Context, lh *localHost, code, sid string, idle *int) {
	s := lh.get(sid)
	if s == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "会话不存在", "code": "session_not_found"})
		return
	}
	if !s.setLifecycle(idle) {
		c.JSON(http.StatusConflict, gin.H{"error": "会话已经结束", "code": "session_exited"})
		return
	}
	detail := "ssh:direct idle=inherit"
	if idle != nil {
		detail = "ssh:direct idle=" + strconv.Itoa(*idle)
	}
	h.audit.record(c.Request.Context(), auditEvent{
		Event: EvLifecycle, Actor: subjectOf(c), IP: c.ClientIP(), MachineCode: code, SessionID: sid, Detail: detail,
	})
	c.JSON(http.StatusOK, gin.H{"ok": true})
}
