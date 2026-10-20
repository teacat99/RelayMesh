package terminal

import (
	"crypto/subtle"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/teacat99/RelayMesh/v2/internal/termproto"
)

const (
	ctxKeySubject = "terminal.subject"
	unlockHeader  = "X-Terminal-Unlock"
)

// bearerToken 取出 Authorization: Bearer 头中的令牌；终端接口不接受 URL 参数形式的令牌。
func bearerToken(r *http.Request) string {
	auth := r.Header.Get("Authorization")
	const prefix = "Bearer "
	if len(auth) > len(prefix) && strings.EqualFold(auth[:len(prefix)], prefix) {
		return strings.TrimSpace(auth[len(prefix):])
	}
	return ""
}

// humanSubject 验证请求携带的是「人类登录」签发的 JWT（MCP 凭据不被接受），返回登录主体。
func (h *Hub) humanSubject(r *http.Request) (string, bool) {
	token := bearerToken(r)
	if token == "" {
		return "", false
	}
	return h.opts.Auth.VerifyHumanJWT(token)
}

// denyNotHuman 在请求没有人类登录身份时拒绝：携带有效 MCP 令牌的得到明确的 403 mcp_token_forbidden
// （与普通管理接口 /api/v1/* 一致，便于排查“为什么我的令牌不能访问”），其余一律 401。
// 这两种拒绝都发生在评估总闸之前，响应差异只与令牌本身有关，不会泄露总闸状态。
func (h *Hub) denyNotHuman(c *gin.Context) {
	if tok := bearerToken(c.Request); tok != "" && h.opts.Auth.IsMCPToken(tok) {
		c.AbortWithStatusJSON(http.StatusForbidden, gin.H{
			"error":         "MCP 令牌只能访问 /mcp 端点，不能调用管理接口（REST / SSE）；请使用 Web 登录会话",
			"code":          "mcp_token_forbidden",
			"auth_required": true,
		})
		return
	}
	c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "需要登录", "code": "auth_required"})
}

func (h *Hub) denyDisabled(c *gin.Context, d Decision) {
	c.AbortWithStatusJSON(http.StatusForbidden, gin.H{
		"error":  "终端功能当前不可用",
		"code":   "terminal_disabled",
		"reason": d.Reason,
	})
}

// guard 是终端 REST 的统一闸门，顺序即安全语义：
//
//  1. 未设置 Web 密码：任何人都等同管理员，直接声明终端不可用（此时没有 JWT 可言，也没有信息可泄露）；
//  2. 必须是人类登录 JWT（401）—— 未登录的访问者无法借响应差异探测总闸状态；
//  3. 总闸必须开启（403 terminal_disabled）；
//  4. requireUnlock 时还需要有效的解锁令牌（403 unlock_required）。
func (h *Hub) guard(requireUnlock bool) gin.HandlerFunc {
	return func(c *gin.Context) {
		if !h.authorize(c, requireUnlock) {
			return
		}
		c.Next()
	}
}

// authorize 执行 guard 的全部检查。不通过时已写入响应并中止请求，返回 false；通过时记录登录主体并返回 true。
func (h *Hub) authorize(c *gin.Context, requireUnlock bool) bool {
	ctx := c.Request.Context()
	d := h.gate.Evaluate(ctx)
	if !d.Open && d.Reason == ReasonNoPassword {
		h.denyDisabled(c, d)
		return false
	}
	subject, ok := h.humanSubject(c.Request)
	if !ok {
		h.denyNotHuman(c)
		return false
	}
	if !d.Open {
		h.audit.recordLimited(ctx, subject, auditEvent{
			Event: EvGateDenied, Result: ResultDeny, Actor: subject, IP: c.ClientIP(),
			Detail: c.Request.Method + " " + c.FullPath() + ": " + string(d.Reason),
		})
		h.denyDisabled(c, d)
		return false
	}
	if requireUnlock && !h.unlocks.Validate(c.GetHeader(unlockHeader), subject) {
		c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "该操作需要先解锁终端（重新输入密码）", "code": "unlock_required"})
		return false
	}
	c.Set(ctxKeySubject, subject)
	return true
}

// RequireUnlock 供终端之外的管理接口（例如 MCP 令牌管理）在需要二次确认的操作里调用：
// 要求调用者是已登录的人类、终端总闸开启且持有有效的解锁令牌。不满足时已写入响应并返回 false。
func (h *Hub) RequireUnlock(c *gin.Context) bool { return h.authorize(c, true) }

// FreshUnlockWindow 是「刚刚重新输入过密码」的时间窗口：授予式的高风险操作（开启「允许整个文件系统」）
// 要求解锁令牌创建于此窗口之内。
const FreshUnlockWindow = 2 * time.Minute

// SetFreshUnlockWindow 调整这个窗口（仅供测试：把它压到负数就能模拟「很久以前解锁的令牌」，
// 不必真的等待两分钟）。d==0 恢复默认的 FreshUnlockWindow。
func (h *Hub) SetFreshUnlockWindow(d time.Duration) { h.freshNanos.Store(int64(d)) }

func (h *Hub) freshWindow() time.Duration {
	if d := time.Duration(h.freshNanos.Load()); d != 0 {
		return d
	}
	return FreshUnlockWindow
}

// RequireFreshUnlock 在 RequireUnlock 的基础上，要求解锁令牌是刚刚（FreshUnlockWindow 之内）才用密码换取的：
// 已经解锁了半天的令牌不能替「授予整个文件系统」这类操作代为确认。不满足时返回 403 unlock_fresh_required，
// 前端据此弹出密码框、换取新令牌后重试；这次被拒绝也会写一条（限流的）审计，subject 说明被拒绝的是什么操作。
// 不满足时已写入响应并返回 false。
func (h *Hub) RequireFreshUnlock(c *gin.Context, subject string) bool {
	if !h.authorize(c, true) {
		return false
	}
	window := h.freshWindow()
	if h.unlocks.ValidateFresh(c.GetHeader(unlockHeader), subjectOf(c), window) {
		return true
	}
	h.FilesScopeChanged(subjectOf(c), c.ClientIP(), ResultDeny, subject+"：解锁令牌不够新，需要刚刚重新输入密码确认")
	c.AbortWithStatusJSON(http.StatusForbidden, gin.H{
		"error": "开启「允许整个文件系统」前，需要重新输入一次密码确认", "code": "unlock_fresh_required",
		"fresh_window_seconds": max(0, int(window/time.Second)),
	})
	return false
}

func subjectOf(c *gin.Context) string {
	if v, ok := c.Get(ctxKeySubject); ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

// statusResponse 是 GET /status 的返回。
type statusResponse struct {
	Enabled bool   `json:"enabled"`
	Reason  Reason `json:"reason"`

	// 以下字段仅在总闸开启时返回。
	Unlocked          bool           `json:"unlocked"`
	UnlockRemaining   int            `json:"unlock_remaining_seconds"`
	UnlockIdleSeconds int            `json:"unlock_idle_seconds,omitempty"`
	Discovery         *DiscoveryView `json:"discovery,omitempty"`
	TLSPin            string         `json:"tls_pin,omitempty"`
	HTTPSPort         int            `json:"https_port,omitempty"`
	Install           *InstallStatus `json:"install,omitempty"`
	SSH               *SSHStatus     `json:"ssh,omitempty"`
	Retention         *RetentionView `json:"retention,omitempty"`
	// Prefs 是终端偏好（宽度保持、终端限高），保存在服务端，所有浏览器共享。
	Prefs *PrefsView `json:"prefs,omitempty"`
}

// RetentionView 是服务端生效的记录与保留策略，供设置页只读展示。
// 这些值来自环境变量（RELAYMESH_TERMINAL_RECORD*），运行期间不能在界面里修改。
type RetentionView struct {
	// RecordOff 为真表示服务端全局关闭终端记录（下发给每台主机的 Agent）。
	RecordOff bool `json:"record_off"`
	// RecordDays / RecordSessionMB / RecordHostMB 大于 0 时是服务端给主机记录加的上限，0 表示不额外限制（按主机本机配置）。
	RecordDays      int `json:"record_days"`
	RecordSessionMB int `json:"record_session_mb"`
	RecordHostMB    int `json:"record_host_mb"`
	// IndexDays 是已结束会话在清单里的保留天数；RunDays 是模型运行记录的保留天数。
	IndexDays int `json:"index_days"`
	RunDays   int `json:"run_days"`
}

// retentionView 汇总当前生效的记录与保留策略。
func (h *Hub) retentionView() *RetentionView {
	return &RetentionView{
		RecordOff:       h.opts.RecordOff,
		RecordDays:      h.opts.RecordRetentionDays,
		RecordSessionMB: h.opts.RecordSessionMB,
		RecordHostMB:    h.opts.RecordHostMB,
		IndexDays:       int(h.sessionIndexKeep().Hours() / 24),
		RunDays:         int(execRetention.Hours() / 24),
	}
}

// handleStatus 是唯一在总闸关闭时仍可访问的终端路由，只报告「是否可用及原因」：
// 未设密码时无需登录（此时没有任何秘密可保护）；其余情况要求登录，避免向匿名访问者暴露配置细节。
func (h *Hub) handleStatus(c *gin.Context) {
	ctx := c.Request.Context()
	d := h.gate.Evaluate(ctx)
	if !d.Open && d.Reason == ReasonNoPassword {
		c.JSON(http.StatusOK, statusResponse{Enabled: false, Reason: d.Reason})
		return
	}
	subject, ok := h.humanSubject(c.Request)
	if !ok {
		h.denyNotHuman(c)
		return
	}
	resp := statusResponse{Enabled: d.Open, Reason: d.Reason}
	if d.Open {
		if left, ok := h.unlocks.Peek(c.GetHeader(unlockHeader), subject); ok {
			resp.Unlocked = true
			resp.UnlockRemaining = int(left / time.Second)
		}
		resp.UnlockIdleSeconds = int(h.unlocks.IdleTTL() / time.Second)
		dv := h.discoveryView()
		resp.Discovery = &dv
		resp.TLSPin = h.currentTLSPin()
		resp.HTTPSPort = h.currentHTTPSPort()
		resp.Install = h.installStatus()
		ss := h.ssh.status()
		resp.SSH = &ss
		resp.Retention = h.retentionView()
		resp.Prefs = h.prefsView(ctx)
	}
	c.JSON(http.StatusOK, resp)
}

// handleUnlockKeepalive 在解锁令牌仍有效时顺延其空闲期（guard(true) 的校验本身就会顺延），并返回剩余时间。
// 前端只在用户仍在操作终端时低频调用，避免「打字几个小时后网络抖动重连，却因解锁令牌空闲过期而被要求重新输入密码」。
// 绝对期限不会被顺延，到期后必须重新输入密码。
func (h *Hub) handleUnlockKeepalive(c *gin.Context) {
	left, ok := h.unlocks.Peek(c.GetHeader(unlockHeader), subjectOf(c))
	if !ok {
		c.JSON(http.StatusForbidden, gin.H{"error": "该操作需要先解锁终端（重新输入密码）", "code": "unlock_required"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"remaining_seconds": int(left / time.Second)})
}

type unlockRequest struct {
	Password string `json:"password"`
}

// handleUnlock 用登录密码换取短期解锁令牌。失败次数按来源 IP 限制，达到上限后锁定一段时间。
func (h *Hub) handleUnlock(c *gin.Context) {
	ctx := c.Request.Context()
	ip := c.ClientIP()
	subject := subjectOf(c)
	key := "unlock:" + ip

	if locked, remaining := h.limiter.CheckLocked(key); locked {
		c.JSON(http.StatusTooManyRequests, gin.H{
			"error":             "解锁尝试过于频繁，请稍后再试",
			"code":              "unlock_locked",
			"locked":            true,
			"remaining_seconds": int64(remaining.Seconds()),
		})
		return
	}

	var req unlockRequest
	if !bindJSON(c, &req) {
		return
	}

	_, expected, err := h.opts.Auth.EffectiveCredentials(ctx)
	if err != nil || expected == "" {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "无法验证密码", "code": "terminal_disabled"})
		return
	}
	if subtle.ConstantTimeCompare([]byte(req.Password), []byte(expected)) != 1 {
		locked, remaining := h.limiter.RecordFailure(key, unlockMaxFailures, unlockLockout)
		h.audit.record(ctx, auditEvent{Event: EvUnlockFail, Result: ResultFail, Actor: subject, IP: ip})
		if locked {
			h.audit.record(ctx, auditEvent{Event: EvUnlockLocked, Result: ResultDeny, Actor: subject, IP: ip, Detail: remaining.String()})
			c.JSON(http.StatusTooManyRequests, gin.H{
				"error":             "连续多次密码错误，已临时锁定解锁功能",
				"code":              "unlock_locked",
				"locked":            true,
				"remaining_seconds": int64(remaining.Seconds()),
			})
			return
		}
		// 403 而不是 401：这是「已登录的人二次验证没通过」，不是会话失效。401 会被前端的全局拦截器当成登录过期，
		// 在解锁框里输错一次密码就会把整个页面踢回登录页。
		c.JSON(http.StatusForbidden, gin.H{"error": "密码错误", "code": "unlock_failed"})
		return
	}

	h.limiter.RecordSuccess(key)
	token, absExpiry, err := h.unlocks.Issue(subject)
	if err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "解锁令牌数量已达上限，请稍后重试", "code": "unlock_capacity"})
		return
	}
	h.audit.record(ctx, auditEvent{Event: EvUnlockOK, Actor: subject, IP: ip})
	c.JSON(http.StatusOK, gin.H{
		"token":            token,
		"expires_at":       absExpiry,
		"idle_seconds":     int(h.unlocks.IdleTTL() / time.Second),
		"absolute_seconds": int(h.unlocks.AbsoluteTTL() / time.Second),
	})
}

// handleLock 主动上锁：作废当前解锁令牌与该用户尚未使用的终端票据。
func (h *Hub) handleLock(c *gin.Context) {
	subject := subjectOf(c)
	h.unlocks.Revoke(c.GetHeader(unlockHeader))
	h.tickets.RevokeSubject(subject)
	h.audit.record(c.Request.Context(), auditEvent{Event: EvLock, Actor: subject, IP: c.ClientIP()})
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// bindJSON 解析请求体（限制 16 KiB）；失败时已写入 400 响应。
func bindJSON(c *gin.Context, dst any) bool {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 16<<10)
	if err := c.ShouldBindJSON(dst); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请求格式不正确", "code": "bad_request"})
		return false
	}
	return true
}

// machineCodeParam 解析并规范化路径参数中的机器码；失败时已写入 400 响应。
func machineCodeParam(c *gin.Context) (string, bool) {
	code, err := termproto.NormalizeMachineCode(c.Param("code"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "机器码格式不正确", "code": "bad_machine_code"})
		return "", false
	}
	return code, true
}
