package terminal

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/teacat99/RelayMesh/v2/internal/store"
	"github.com/teacat99/RelayMesh/v2/internal/termproto"
)

// Mount 注册全部终端路由到 /api/v1/terminal。
//
// 路由分三类：
//   - 自带鉴权的入口：/status（按需登录）、/agent/ws（通用 Agent 入口，多层预认证防护）、/ws（一次性票据）；
//   - 「登录」类：只需人类登录 JWT，用于查看列表、改备注、看审计、上锁；
//   - 「解锁」类：还需 X-Terminal-Unlock，用于一切会扩大攻击面或终止会话的操作。
//
// 所有路由（/status 除外）在总闸关闭时一律拒绝（见 guard 与各入口的前置判定）。
// 另有两个挂在根路径、靠一次性 / 窗口票据保护的下载入口：/files/dl/:ticket（模型文件拉取）与 /i/:ticket/:name（Agent 安装源）。
func (h *Hub) Mount(engine *gin.Engine) {
	g := engine.Group("/api/v1/terminal")

	g.GET("/status", h.handleStatus)
	g.GET("/agent/ws", h.handleAgentWS)
	g.GET("/ws", h.handleBrowserWS)

	login := g.Group("", h.guard(false))
	{
		login.POST("/unlock", h.handleUnlock)
		login.POST("/lock", h.handleLock)
		login.GET("/hosts", h.handleListHosts)
		login.POST("/hosts/:code/remark", h.handleRemark)
		login.GET("/hosts/:code/sessions", h.handleListSessions)
		login.GET("/hosts/:code/history", h.handleHistory)
		login.POST("/hosts/:code/sessions/:sid/name", h.handleRenameSession)
		login.POST("/archive", h.handleArchive)
		login.POST("/hosts/:code/lost/dismiss", h.handleDismissLost)
		login.GET("/audit", h.handleAudit)
		login.PUT("/prefs/display", h.handlePrefsDisplay)
		login.GET("/launchers/runs", h.handleLauncherRuns)
		login.POST("/launchers/runs/:run_id/auto/cancel", h.handleAutoResumeCancel)
		login.GET("/launchers/for-session", h.handleSessionLaunchers)
		login.POST("/session-run/preview", h.handleSessionRunPreview)
		login.GET("/session-run/status", h.handleSessionRunStatus)
		login.POST("/session-enable/preview", h.handleSessionEnablePreview)
	}

	unlocked := g.Group("", h.guard(true))
	{
		unlocked.POST("/unlock/keepalive", h.handleUnlockKeepalive)
		unlocked.POST("/discovery", h.handleDiscoveryOpen)
		unlocked.DELETE("/discovery", h.handleDiscoveryClose)
		unlocked.POST("/hosts/:code/enable", h.handleEnable)
		unlocked.POST("/hosts/:code/ignore", h.handleIgnore)
		unlocked.POST("/hosts/:code/unignore", h.handleUnignore)
		unlocked.POST("/hosts/:code/disable", h.handleDisable)
		unlocked.DELETE("/hosts/:code", h.handleDelete)
		unlocked.POST("/hosts/:code/sessions", h.handleOpenSession)
		unlocked.POST("/hosts/:code/sessions/:sid/ticket", h.handleTicket)
		unlocked.POST("/hosts/:code/sessions/:sid/kill", h.handleKill)
		unlocked.GET("/hosts/:code/sessions/:sid/record", h.handleRecordInfo)
		unlocked.GET("/hosts/:code/sessions/:sid/record/data", h.handleRecordData)
		unlocked.POST("/hosts/:code/recording", h.handleRecordingPolicy)
		unlocked.PUT("/prefs/retention", h.handlePrefsRetention)
		unlocked.PUT("/prefs/auto-upgrade", h.handlePrefsAutoUpgrade)
		unlocked.PUT("/launchers/:id", h.handleLauncherSave)
		unlocked.DELETE("/launchers/:id", h.handleLauncherDelete)
		unlocked.POST("/launchers/:id/run", h.handleLauncherRun)
		unlocked.POST("/session-run", h.handleSessionRun)
		unlocked.POST("/session-enable", h.handleSessionEnable)
		unlocked.POST("/hosts/:code/upgrade-window", h.handleUpgradeWindowOpen)
		unlocked.DELETE("/hosts/:code/upgrade-window", h.handleUpgradeWindowClose)
		unlocked.POST("/hosts/:code/web-upgrade", h.handleWebUpgrade)
		unlocked.POST("/hosts/:code/lifecycle", h.handleHostLifecycle)
		unlocked.POST("/hosts/:code/sessions/:sid/lifecycle", h.handleSessionLifecycle)
	}

	h.mountExec(login, unlocked)
	h.mountSSH(unlocked)
	h.mountFiles(engine)
	h.mountInstaller(engine)
}

func (h *Hub) fail(c *gin.Context, err error) {
	if errors.Is(err, ErrHostNotFound) {
		c.JSON(http.StatusNotFound, gin.H{"error": "主机不存在或状态不符", "code": "host_not_found"})
		return
	}
	var inUse *ErrGatewayInUse
	if errors.As(err, &inUse) {
		c.JSON(http.StatusConflict, gin.H{
			"error": fmt.Sprintf("还有 %d 台 SSH 主机经由这台主机接入：请先删除它们，或把它们改接到别的网关", inUse.Count),
			"code":  "gateway_in_use", "count": inUse.Count,
		})
		return
	}
	c.JSON(http.StatusInternalServerError, gin.H{"error": "操作失败，请查看服务端日志", "code": "internal_error"})
}

// ---------- 主机列表与状态变更 ----------

func (h *Hub) handleListHosts(c *gin.Context) {
	hosts, pending, err := h.listHosts(c.Request.Context())
	if err != nil {
		h.fail(c, err)
		return
	}
	archived, err := h.store.ListTerminalArchives(c.Request.Context())
	if err != nil {
		h.fail(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"hosts":     hosts,
		"pending":   pending,
		"archived":  archived,
		"discovery": h.discoveryView(),
	})
}

type remarkRequest struct {
	Remark string `json:"remark"`
}

func (h *Hub) handleRemark(c *gin.Context) {
	code, ok := machineCodeParam(c)
	if !ok {
		return
	}
	var req remarkRequest
	if !bindJSON(c, &req) {
		return
	}
	if err := h.setHostRemark(c.Request.Context(), code, req.Remark, subjectOf(c), c.ClientIP()); err != nil {
		h.fail(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

type enableRequest struct {
	Remark string `json:"remark"`
	// DefaultForNewModels 是这台主机对新建模型授权的默认态（allow / deny，缺省 deny），只在新增主机时生效。
	DefaultForNewModels string `json:"default_for_new_models"`
}

func (h *Hub) handleEnable(c *gin.Context) {
	code, ok := machineCodeParam(c)
	if !ok {
		return
	}
	var req enableRequest
	// 请求体可以为空（不改备注）。
	if c.Request.ContentLength != 0 && !bindJSON(c, &req) {
		return
	}
	if err := store.ValidateHostDefaultForNewModels(req.DefaultForNewModels); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error(), "code": "bad_request"})
		return
	}
	view, grant, err := h.enableHost(c.Request.Context(), code, req.Remark, req.DefaultForNewModels, subjectOf(c), c.ClientIP())
	if err != nil {
		h.fail(c, err)
		return
	}
	resp := gin.H{"host": view}
	if grant != nil {
		// 新增主机并按默认态授予了现有模型令牌：把结果告诉界面，让管理员看到「授予了谁」。
		resp["grant"] = grant
	}
	c.JSON(http.StatusOK, resp)
}

func (h *Hub) handleIgnore(c *gin.Context) {
	code, ok := machineCodeParam(c)
	if !ok {
		return
	}
	if err := h.ignoreHost(c.Request.Context(), code, subjectOf(c), c.ClientIP()); err != nil {
		h.fail(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

func (h *Hub) handleUnignore(c *gin.Context) {
	code, ok := machineCodeParam(c)
	if !ok {
		return
	}
	if err := h.unignoreHost(c.Request.Context(), code, subjectOf(c), c.ClientIP()); err != nil {
		h.fail(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

func (h *Hub) handleDisable(c *gin.Context) {
	code, ok := machineCodeParam(c)
	if !ok {
		return
	}
	if err := h.disableHost(c.Request.Context(), code, subjectOf(c), c.ClientIP()); err != nil {
		h.fail(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

func (h *Hub) handleDelete(c *gin.Context) {
	code, ok := machineCodeParam(c)
	if !ok {
		return
	}
	// ?uninstall=1：同时请求在线的 Agent 卸载自己。用查询参数而不是请求体——DELETE 带请求体在不少代理 / 客户端上不可靠。
	uninstall := c.Query("uninstall") == "1" || c.Query("uninstall") == "true"
	outcome, err := h.deleteHost(c.Request.Context(), code, subjectOf(c), c.ClientIP(), uninstall)
	if err != nil {
		h.fail(c, err)
		return
	}
	resp := gin.H{"ok": true}
	if outcome != UninstallNone {
		resp["uninstall"] = outcome
	}
	c.JSON(http.StatusOK, resp)
}

type discoveryRequest struct {
	Minutes int `json:"minutes"`
	// Extend 为 true 时是「续期」：在现有剩余时间上再加 Minutes 分钟（总剩余不超过上限），而不是重置为 Minutes 分钟。
	Extend bool `json:"extend"`
}

func (h *Hub) handleDiscoveryOpen(c *gin.Context) {
	var req discoveryRequest
	if c.Request.ContentLength != 0 && !bindJSON(c, &req) {
		return
	}
	dur := time.Duration(req.Minutes) * time.Minute
	var view DiscoveryView
	if req.Extend {
		view = h.extendDiscovery(c.Request.Context(), subjectOf(c), c.ClientIP(), dur)
	} else {
		view = h.openDiscovery(c.Request.Context(), subjectOf(c), c.ClientIP(), dur)
	}
	c.JSON(http.StatusOK, gin.H{"discovery": view})
}

func (h *Hub) handleDiscoveryClose(c *gin.Context) {
	view := h.closeDiscovery(c.Request.Context(), subjectOf(c), c.ClientIP())
	c.JSON(http.StatusOK, gin.H{"discovery": view})
}

// ---------- 会话 ----------

// agentFor 取得目标主机的在线 Agent；不在线时已写入 409 响应。
func (h *Hub) agentFor(c *gin.Context, code string) *agentConn {
	a := h.onlineAgent(code)
	if a == nil || !a.isEnabled() {
		if rec, _ := h.store.GetTerminalHost(h.ctx, code); rec != nil && rec.IsSSH() {
			if rec.ViaGateway() {
				c.JSON(http.StatusConflict, gin.H{"error": "这台 SSH 主机经网关主机接入：请在网关主机上为它打开终端", "code": "ssh_via_gateway"})
				return nil
			}
			c.JSON(http.StatusConflict, gin.H{"error": "Hub 直连的 SSH 主机不提供文件传输，终端会话也不保存记录", "code": "ssh_no_terminal"})
			return nil
		}
		c.JSON(http.StatusConflict, gin.H{"error": "主机当前不在线", "code": "host_offline"})
		return nil
	}
	return a
}

func (h *Hub) handleListSessions(c *gin.Context) {
	code, ok := machineCodeParam(c)
	if !ok {
		return
	}
	sessions := []termproto.SessionInfo{}
	online := false
	if a := h.onlineAgent(code); a != nil && a.isEnabled() {
		online = true
		sessions = a.sessionList()
	} else if lh := h.localSessions(c.Request.Context(), code); lh != nil {
		online = true // Hub 直连的 SSH 主机：会话就在 Hub 里，不依赖 Agent 在线
		sessions = lh.list()
	}
	c.JSON(http.StatusOK, gin.H{"online": online, "sessions": sessions})
}

type openSessionRequest struct {
	Name string `json:"name"`
	Rows uint16 `json:"rows"`
	Cols uint16 `json:"cols"`
	// Incognito 为 true 时这个会话完全不记录（主机上不留任何文件），用于输入敏感内容的临时会话。
	Incognito bool `json:"incognito"`
	// Compact 为 true 时开精简会话：回放缓冲只有 32 KiB、宽度不超过 120 列（会话守护预设启动的会话用）。
	Compact bool `json:"compact"`
	// IdleSeconds 为本会话单独设置的空闲回收时长（0 = 永久保持）；缺省沿用主机默认。
	IdleSeconds *int `json:"idle_seconds"`
	// Cwd 是希望新会话启动时所在的目录（「在原目录重建会话」）；主机上不存在则回落到默认目录。
	Cwd string `json:"cwd"`
	// SSHTarget 非空表示这是一个「经网关出站连接 SSH 主机」的人工终端：值是那台 SSH 主机的机器码，
	// URL 里的主机必须是它的网关。登录凭据只保存在网关自己的密封存储里：open 帧里只有地址、用户与已固定的主机公钥
	// （stored 引用），由网关出示它保存的凭据，不经过 Hub 也不经过浏览器。
	SSHTarget string `json:"ssh_target"`
}

func (h *Hub) handleOpenSession(c *gin.Context) {
	code, ok := machineCodeParam(c)
	if !ok {
		return
	}
	var req openSessionRequest
	if c.Request.ContentLength != 0 && !bindJSON(c, &req) {
		return
	}
	if req.SSHTarget != "" {
		h.openSSHSession(c, code, req)
		return
	}
	// Hub 直连的 SSH 主机：会话持有在 Hub 里（localterm.go）。
	if rec := h.directSSHRecord(c.Request.Context(), code); rec != nil {
		h.openLocalSession(c, rec, req)
		return
	}
	a := h.agentFor(c, code)
	if a == nil {
		return
	}
	if req.IdleSeconds != nil && !validIdleSeconds(*req.IdleSeconds) {
		badIdleResponse(c)
		return
	}
	info, err := a.openSession(c.Request.Context(), openSpec{
		Name: req.Name, Rows: req.Rows, Cols: req.Cols, Incognito: req.Incognito, Compact: req.Compact, IdleSec: req.IdleSeconds, Cwd: req.Cwd,
	})
	if err != nil {
		switch {
		case errors.Is(err, errSessionLimit):
			c.JSON(http.StatusConflict, gin.H{"error": "该主机的会话数已达上限", "code": "session_limit"})
		case errors.Is(err, errTooManyOpens):
			c.JSON(http.StatusTooManyRequests, gin.H{"error": "操作过于频繁", "code": "busy"})
		case errors.Is(err, errOpenTimeout):
			c.JSON(http.StatusGatewayTimeout, gin.H{"error": "主机未在规定时间内响应", "code": "agent_timeout"})
		case errors.Is(err, errAgentClosed), errors.Is(err, errAgentNotActive):
			c.JSON(http.StatusConflict, gin.H{"error": "主机当前不在线", "code": "host_offline"})
		default:
			c.JSON(http.StatusBadGateway, gin.H{"error": termproto.SanitizeText(err.Error(), 160), "code": "agent_refused"})
		}
		return
	}
	detail := ""
	if req.Incognito {
		detail = "incognito"
	}
	if req.Cwd != "" {
		detail = strings.TrimSpace(detail + " cwd")
	}
	h.audit.record(c.Request.Context(), auditEvent{
		Event: EvSessionOpen, Actor: subjectOf(c), IP: c.ClientIP(), MachineCode: code, SessionID: info.SID, Detail: detail,
	})
	c.JSON(http.StatusOK, gin.H{"session": info})
}

type ticketRequest struct {
	Since uint64 `json:"since"`
	Rows  uint16 `json:"rows"`
	Cols  uint16 `json:"cols"`
	// TailLines：全新附着时只回放末尾这么多行（缓冲很大的会话才生效；0 或不传 = 整体回放）。
	TailLines uint16 `json:"tail_lines"`
}

// handleTicket 签发一次性终端票据（30 秒有效，绑定主机 / 会话 / 回放起点 / 尺寸）。
func (h *Hub) handleTicket(c *gin.Context) {
	code, ok := machineCodeParam(c)
	if !ok {
		return
	}
	sid := c.Param("sid")
	if !termproto.ValidSessionID(sid) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "会话 ID 格式不正确", "code": "bad_session_id"})
		return
	}
	var req ticketRequest
	if c.Request.ContentLength != 0 && !bindJSON(c, &req) {
		return
	}
	found := false
	if lh := h.localSessions(c.Request.Context(), code); lh != nil {
		_, found = lh.find(sid)
	} else {
		a := h.agentFor(c, code)
		if a == nil {
			return
		}
		_, found = a.findSession(sid)
	}
	if !found {
		c.JSON(http.StatusNotFound, gin.H{"error": "会话不存在", "code": "session_not_found"})
		return
	}
	tail := min(req.TailLines, termproto.MaxTailLines) // 过大的请求按上限截断
	if req.Since != 0 {
		tail = 0 // 续传从 since 接着发，用不着裁剪回放
	}
	bind, err := json.Marshal(ticketBind{Code: code, SID: sid, Since: req.Since, Rows: req.Rows, Cols: req.Cols, Tail: tail})
	if err != nil {
		h.fail(c, err)
		return
	}
	ticket, err := h.tickets.Issue(ticketPurposeTerm, subjectOf(c), string(bind), ticketTTL)
	if err != nil {
		c.JSON(http.StatusTooManyRequests, gin.H{"error": "票据数量过多，请稍后重试", "code": "busy"})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"ticket":     ticket,
		"expires_in": int(ticketTTL / time.Second),
		"ws_path":    "/api/v1/terminal/ws",
		"protocols":  []string{termproto.SubprotocolBrowser, termproto.TicketSubprotocolPrefix + "<ticket>"},
	})
}

func (h *Hub) handleKill(c *gin.Context) {
	code, ok := machineCodeParam(c)
	if !ok {
		return
	}
	sid := c.Param("sid")
	if !termproto.ValidSessionID(sid) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "会话 ID 格式不正确", "code": "bad_session_id"})
		return
	}
	if lh := h.localSessions(c.Request.Context(), code); lh != nil {
		h.killLocalSession(c, lh, code, sid)
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
	if err := a.writeJSON(termproto.Kill{T: termproto.TKill, SID: sid}); err != nil {
		c.JSON(http.StatusConflict, gin.H{"error": "主机当前不在线", "code": "host_offline"})
		return
	}
	// 已经自行退出的会话只是被清出列表：它的结束原因保持「正常退出」，不改写成「被终止」。
	if info.State != termproto.SessionExited {
		a.indexEnded(store.TerminalSessionEnd{SID: sid, Reason: "killed"})
	}
	h.audit.record(c.Request.Context(), auditEvent{
		Event: EvSessionKill, Actor: subjectOf(c), IP: c.ClientIP(), MachineCode: code, SessionID: sid,
	})
	c.JSON(http.StatusAccepted, gin.H{"ok": true})
}

type renameRequest struct {
	Name string `json:"name"`
}

func (h *Hub) handleRenameSession(c *gin.Context) {
	code, ok := machineCodeParam(c)
	if !ok {
		return
	}
	sid := c.Param("sid")
	if !termproto.ValidSessionID(sid) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "会话 ID 格式不正确", "code": "bad_session_id"})
		return
	}
	var req renameRequest
	if !bindJSON(c, &req) {
		return
	}
	if lh := h.localSessions(c.Request.Context(), code); lh != nil {
		h.renameLocalSession(c, lh, sid, req.Name)
		return
	}
	a := h.agentFor(c, code)
	if a == nil {
		return
	}
	if _, found := a.findSession(sid); !found {
		c.JSON(http.StatusNotFound, gin.H{"error": "会话不存在", "code": "session_not_found"})
		return
	}
	name := termproto.SanitizeText(req.Name, termproto.MaxSessionNameRunes)
	if err := a.writeJSON(termproto.Rename{T: termproto.TRename, SID: sid, Name: name}); err != nil {
		c.JSON(http.StatusConflict, gin.H{"error": "主机当前不在线", "code": "host_offline"})
		return
	}
	c.JSON(http.StatusAccepted, gin.H{"ok": true})
}

// ---------- 审计 ----------

func (h *Hub) handleAudit(c *gin.Context) {
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "100"))
	before, _ := strconv.ParseUint(c.Query("before"), 10, 64)
	logs, err := h.store.ListTerminalAudit(c.Request.Context(), limit, uint(before))
	if err != nil {
		h.fail(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"logs": logs})
}
