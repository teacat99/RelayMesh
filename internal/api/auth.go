package api

import (
	"context"
	"crypto/subtle"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"github.com/teacat99/RelayMesh/v2/internal/config"
	"github.com/teacat99/RelayMesh/v2/internal/secure"
	"github.com/teacat99/RelayMesh/v2/internal/store"
)

// ticketPurposeSSE 是 SSE 一次性票据的用途标识，票据不能被挪用到其他端点。
const (
	ticketPurposeSSE = "sse"
	sseTicketTTL     = 30 * time.Second
)

type AuthHandler struct {
	cfg     *config.Config
	store   *store.Store
	limiter *RateLimiter
	tickets *secure.TicketStore

	// onCredentialsChanged 在账号或密码被成功修改 / 重置后调用（终端 Hub 借此撤销全部解锁令牌、票据与浏览器终端连接）。
	onCredentialsChanged func(subject, ip string)

	// onCredentialReveal 在每次「查看令牌」尝试（成功 / 密码错误 / 被锁定）后调用，交给审计日志记录；
	// 参数里只有操作者、来源、结果和令牌的编号名称，绝不会有令牌或密码本身。
	onCredentialReveal func(actor, ip, result, detail string)

	// onLoginLocked 在登录连续失败触发 IP 锁定的那一次调用（安全告警推送用，#235）。不带任何参数：
	// 来源 IP 与账号名都不需要出现在推送里（它们只属于本机日志与审计）。
	onLoginLocked func()

	// lastCredUnavailableLog 是上次记录「凭据不可用」日志的时间（Unix 纳秒），用来限频：数据库故障期间每个请求都会走到这里。
	lastCredUnavailableLog atomic.Int64
}

// credUnavailableLogEvery 是「凭据不可用」日志的最小间隔。
const credUnavailableLogEvery = 30 * time.Second

func NewAuthHandler(cfg *config.Config, st *store.Store, limiter *RateLimiter) *AuthHandler {
	if limiter == nil {
		limiter = NewRateLimiter()
	}
	return &AuthHandler{
		cfg:     cfg,
		store:   st,
		limiter: limiter,
		tickets: secure.NewTicketStore(1024),
	}
}

// SetCredentialsChangedHook 注册凭据变更回调；必须在对外提供服务之前调用。
func (a *AuthHandler) SetCredentialsChangedHook(fn func(subject, ip string)) {
	a.onCredentialsChanged = fn
}

func (a *AuthHandler) notifyCredentialsChanged(subject, ip string) {
	if a.onCredentialsChanged != nil {
		a.onCredentialsChanged(subject, ip)
	}
}

// SetCredentialRevealHook 注册「查看令牌」的审计回调；必须在对外提供服务之前调用。
// result 取 ok / fail / deny，含义与终端审计一致。
func (a *AuthHandler) SetCredentialRevealHook(fn func(actor, ip, result, detail string)) {
	a.onCredentialReveal = fn
}

// SetLoginLockedHook 注册「登录失败过多、来源被锁定」的回调；必须在对外提供服务之前调用。
func (a *AuthHandler) SetLoginLockedHook(fn func()) {
	a.onLoginLocked = fn
}

// securitySettings 读取反爆破配置，读取失败或字段缺省时回落到默认值（开启、5 次、锁 15 分钟）。
// 登录与「查看令牌」共用，保证两条路径吃同一份额度、遵守同一份设置。
func (a *AuthHandler) securitySettings(ctx context.Context) store.SecuritySettings {
	security := store.SecuritySettings{
		BruteForceProtection: true,
		MaxFailedAttempts:    5,
		LockoutMinutes:       15,
	}
	if a.store != nil {
		if appSettings, err := a.store.GetGlobalAppSettings(ctx); err == nil && appSettings != nil {
			security = appSettings.Security
			if security.MaxFailedAttempts <= 0 {
				security.MaxFailedAttempts = 5
			}
			if security.LockoutMinutes <= 0 {
				security.LockoutMinutes = 15
			}
		}
	}
	return security
}

type LoginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

type ChangeCredentialsRequest struct {
	OldPassword string `json:"old_password"`
	NewUsername string `json:"new_username"`
	NewPassword string `json:"new_password"`
}

// credentialsOrUnavailable 是所有「空密码 = 未启用鉴权」判定的统一入口。
// （原先忽略读取错误的 GetEffectiveCredentials 已删除，避免「读不到 = 免登录」的写法再次出现；
// 需要自行处理错误的调用方直接用 EffectiveCredentials。）
//
// 数据库读取失败时 EffectiveCredentials 会回退到环境变量值。如果环境变量里也没有密码——凭据只存在数据库里，
// 这是部署方轮换密码后清理环境变量的常见做法——回退值就是空串，而空串恰好代表「免登录」：一次数据库故障
// 会被误判成「无需登录」。此时 ok=false，调用方必须拒绝请求（失败即关闭），而不是放行。
// 环境变量里有密码时保持既有行为：读取失败仍回退到它，鉴权不会被绕过。
func (a *AuthHandler) credentialsOrUnavailable(c *gin.Context) (username string, password string, ok bool) {
	username, password, err := a.EffectiveCredentials(c.Request.Context())
	if err != nil && password == "" {
		now := time.Now().UnixNano()
		if last := a.lastCredUnavailableLog.Load(); now-last >= int64(credUnavailableLogEvery) && a.lastCredUnavailableLog.CompareAndSwap(last, now) {
			log.Printf("[auth] 读取登录凭据失败且没有环境变量兜底，请求按失败即关闭处理（此日志每 %s 最多一条）: %v", credUnavailableLogEvery, err)
		}
		return username, "", false
	}
	return username, password, true
}

// respondCredentialsUnavailable 统一的「凭据暂不可用」响应：503 而不是 401，让客户端知道这是服务端的暂时故障而不是账号密码错误。
func respondCredentialsUnavailable(c *gin.Context) {
	c.AbortWithStatusJSON(http.StatusServiceUnavailable, gin.H{
		"error":         "credentials temporarily unavailable, retry shortly",
		"code":          "credentials_unavailable",
		"auth_required": true,
	})
}

// EffectiveCredentials 是账号密码判定的唯一真源，每次调用都实时读取数据库，不做缓存。
// 返回的 error 表示数据库读取或解析失败：此时 username/password 仍是「环境变量兜底值」，
// 但调用方若据此做安全放行（如终端总闸）必须把 error 当作失败处理。
func (a *AuthHandler) EffectiveCredentials(ctx context.Context) (username string, password string, err error) {
	// 默认环境变量值
	username = a.cfg.WebUsername
	if username == "" {
		username = "admin"
	}
	password = a.cfg.WebPassword

	if a.store != nil {
		dbCreds, dbErr := a.store.GetAuthCredentials(ctx)
		if dbErr != nil {
			return username, password, dbErr
		}
		if dbCreds != nil {
			if dbCreds.Username != "" {
				username = dbCreds.Username
			}
			if dbCreds.Password != "" {
				password = dbCreds.Password
			}
		}
	}
	return username, password, nil
}

func (a *AuthHandler) Login(c *gin.Context) {
	expectedUser, expectedPass, credsOK := a.credentialsOrUnavailable(c)
	if !credsOK {
		respondCredentialsUnavailable(c)
		return
	}

	if expectedPass == "" {
		c.JSON(http.StatusOK, gin.H{
			"status":        "ok",
			"auth_required": false,
			"token":         "",
			"message":       "no authentication required",
		})
		return
	}

	clientIP := c.ClientIP()

	// 1. 读取系统反爆破安全配置
	security := a.securitySettings(c.Request.Context())

	// 2. 检查当前 IP 是否处于锁定状态
	if security.BruteForceProtection {
		if isLocked, remaining := a.limiter.CheckLocked(clientIP); isLocked {
			remainingSecs := int64(remaining.Seconds())
			c.JSON(http.StatusTooManyRequests, gin.H{
				"error":             fmt.Sprintf("由于连续多次尝试失败，该 IP 已被安全锁定，请在 %d 分钟后重试", int(remaining.Minutes())+1),
				"locked":            true,
				"remaining_seconds": remainingSecs,
				"lockout_minutes":   security.LockoutMinutes,
			})
			return
		}
	}

	var req LoginRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request format"})
		return
	}

	// 3. 恒定时间安全比对，防时序侧信道反推
	userMatch := subtle.ConstantTimeCompare([]byte(req.Username), []byte(expectedUser)) == 1
	passMatch := subtle.ConstantTimeCompare([]byte(req.Password), []byte(expectedPass)) == 1

	if !userMatch || !passMatch {
		// 记录一次失败
		if security.BruteForceProtection {
			isLocked, remaining := a.limiter.RecordFailure(
				clientIP,
				security.MaxFailedAttempts,
				time.Duration(security.LockoutMinutes)*time.Minute,
			)
			if isLocked {
				if a.onLoginLocked != nil {
					a.onLoginLocked()
				}
				c.JSON(http.StatusTooManyRequests, gin.H{
					"error":             fmt.Sprintf("连续尝试失败达到 %d 次上限，该 IP 已被锁定 %d 分钟", security.MaxFailedAttempts, security.LockoutMinutes),
					"locked":            true,
					"remaining_seconds": int64(remaining.Seconds()),
					"lockout_minutes":   security.LockoutMinutes,
				})
				return
			}
		}

		c.JSON(http.StatusUnauthorized, gin.H{
			"error": "账号或密码错误，请重新输入",
		})
		return
	}

	// 4. 登录成功，清除该 IP 失败计数
	if security.BruteForceProtection {
		a.limiter.RecordSuccess(clientIP)
	}

	// 5. 签发 7 天有效期的 JWT Token
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"sub":  req.Username,
		"role": "admin",
		"exp":  time.Now().Add(7 * 24 * time.Hour).Unix(),
		"iat":  time.Now().Unix(),
	})

	tokenString, err := token.SignedString([]byte(a.cfg.JWTSecret))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to generate token"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"status":        "ok",
		"auth_required": true,
		"token":         tokenString,
		"expires_in":    7 * 24 * 3600,
	})
}

func (a *AuthHandler) Status(c *gin.Context) {
	// 注意：这是匿名可访问的端点，刻意不返回任何终端相关状态（终端是否启用属于登录后才应知道的信息，
	// 见 GET /api/v1/terminal/status）。
	effectiveUser, effectivePass, credsOK := a.credentialsOrUnavailable(c)
	isCustomized := false
	if a.store != nil {
		if dbCreds, err := a.store.GetAuthCredentials(c.Request.Context()); err == nil && dbCreds != nil {
			isCustomized = true
		}
	}

	c.JSON(http.StatusOK, gin.H{
		// 读不到凭据（且没有环境变量兜底）时按「需要登录」报告：前端据此显示登录框，随后的登录请求会得到明确的 503，
		// 而不是把一次数据库故障展示成「无需登录」。
		"auth_required":     effectivePass != "" || !credsOK,
		"mcp_auth_required": a.cfg.MCPToken != "" || a.cfg.ConfigureToken != "" || a.cfg.ExecutionToken != "",
		"project_id":        a.cfg.ProjectID,
		"host_name":         a.cfg.HostName,
		"current_username":  effectiveUser,
		"is_customized":     isCustomized,
		"version":           a.cfg.Version,
		// 运行方式：serve（常驻服务）/ stdio（由 AI 客户端拉起的直驱进程）。前端据此调整首次打开的默认布局
		// （stdio 单窗口时默认收起侧栏）；只是个公开的枚举值，不含任何机密。
		"run_mode": runModeName(a.cfg.Mode),
	})
}

// runModeName 把配置里的运行方式规整成对外公开的两个取值，未知或空都按 serve。
func runModeName(mode string) string {
	if mode == "stdio" {
		return "stdio"
	}
	return "serve"
}

// ChangeCredentials 修改管理账号和密码（需提供旧密码进行鉴权验证）
func (a *AuthHandler) ChangeCredentials(c *gin.Context) {
	_, currentPass, credsOK := a.credentialsOrUnavailable(c)
	if !credsOK {
		// 读不到当前凭据就无法核对旧密码：宁可拒绝，也不能把「读不到」当成「没有旧密码」而跳过校验。
		respondCredentialsUnavailable(c)
		return
	}

	var req ChangeCredentialsRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}

	req.NewUsername = strings.TrimSpace(req.NewUsername)
	req.NewPassword = strings.TrimSpace(req.NewPassword)

	if req.NewUsername == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "新账号不能为空"})
		return
	}
	if req.NewPassword == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "新密码不能为空"})
		return
	}
	if len(req.NewPassword) < 6 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "新密码长度至少需要 6 个字符"})
		return
	}

	// 校验旧密码
	if currentPass != "" {
		if subtle.ConstantTimeCompare([]byte(req.OldPassword), []byte(currentPass)) != 1 {
			// 403 而不是 401：已登录的人旧密码输错不是会话失效，401 会被前端全局拦截器当成登录过期而踢回登录页。
			c.JSON(http.StatusForbidden, gin.H{"error": "当前旧密码不正确，拒绝修改", "code": "old_password_incorrect"})
			return
		}
	}

	if a.store == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "数据库连接不可用"})
		return
	}

	// 保存新凭据至数据库
	if err := a.store.SaveAuthCredentials(c.Request.Context(), req.NewUsername, req.NewPassword); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("保存凭据失败: %v", err)})
		return
	}

	// 签发新的有效 Token
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"sub":  req.NewUsername,
		"role": "admin",
		"exp":  time.Now().Add(7 * 24 * time.Hour).Unix(),
		"iat":  time.Now().Unix(),
	})
	tokenString, _ := token.SignedString([]byte(a.cfg.JWTSecret))

	a.notifyCredentialsChanged(req.NewUsername, c.ClientIP())

	c.JSON(http.StatusOK, gin.H{
		"status":   "ok",
		"message":  "管理账号与访问密码已成功更新",
		"username": req.NewUsername,
		"token":    tokenString,
	})
}

// ResetCredentials 重置账号密码回退至环境变量初始值
func (a *AuthHandler) ResetCredentials(c *gin.Context) {
	if a.store == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "数据库连接不可用"})
		return
	}

	if err := a.store.ResetAuthCredentials(c.Request.Context()); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("重置失败: %v", err)})
		return
	}

	a.notifyCredentialsChanged(a.cfg.WebUsername, c.ClientIP())

	c.JSON(http.StatusOK, gin.H{
		"status":   "ok",
		"message":  "账号与密码已重置为环境变量初始设定值",
		"username": a.cfg.WebUsername,
	})
}

func (a *AuthHandler) GetBlockedIPs(c *gin.Context) {
	list := a.limiter.GetBlockedIPs()
	c.JSON(http.StatusOK, gin.H{
		"blocked_ips": list,
		"total":       len(list),
	})
}

func (a *AuthHandler) UnblockIP(c *gin.Context) {
	var req struct {
		IP string `json:"ip"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.IP == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid ip"})
		return
	}
	a.limiter.UnblockIP(req.IP)
	c.JSON(http.StatusOK, gin.H{"status": "ok", "message": "ip unblocked"})
}

func (a *AuthHandler) ClearAllBlockedIPs(c *gin.Context) {
	a.limiter.ClearAll()
	c.JSON(http.StatusOK, gin.H{"status": "ok", "message": "all blocked ips cleared"})
}

// VerifyJWTToken 校验 JWT 并返回 claims
func (a *AuthHandler) VerifyJWTToken(tokenString string) (*jwt.MapClaims, bool) {
	// 只接受本系统签发所用的 HS256，杜绝算法混淆（如声明为 none / 非对称算法）。
	token, err := jwt.Parse(tokenString, func(t *jwt.Token) (interface{}, error) {
		return []byte(a.cfg.JWTSecret), nil
	}, jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}))
	if err == nil && token.Valid {
		if claims, ok := token.Claims.(jwt.MapClaims); ok {
			return &claims, true
		}
	}
	return nil, false
}

// VerifyHumanJWT 只认「Web 登录签发的管理员 JWT」：不接受 MCP 令牌、凭据库令牌或任何其他来源。
// 自 v1.7 起整个 REST / SSE 管理面都只认人类 JWT（见 authMiddleware），本函数保留给需要同时核对 role 与 sub 的终端端点。
func (a *AuthHandler) VerifyHumanJWT(tokenString string) (subject string, ok bool) {
	tokenString = strings.TrimSpace(tokenString)
	if tokenString == "" {
		return "", false
	}
	claims, valid := a.VerifyJWTToken(tokenString)
	if !valid {
		return "", false
	}
	if role, _ := (*claims)["role"].(string); role != "admin" {
		return "", false
	}
	sub, _ := (*claims)["sub"].(string)
	if sub == "" {
		return "", false
	}
	return sub, true
}

// IssueSSETicket 签发 SSE 一次性票据（POST /api/v1/auth/sse_ticket，需已登录）。
// 浏览器的 EventSource 无法自定义请求头，过去把 7 天有效的 JWT 直接拼进 URL，会进入反代访问日志；
// 现在改为 30 秒、单次有效的票据，泄漏后也无法再被利用。
func (a *AuthHandler) IssueSSETicket(c *gin.Context) {
	_, currentPass, credsOK := a.credentialsOrUnavailable(c)
	if !credsOK {
		respondCredentialsUnavailable(c)
		return
	}
	if currentPass == "" {
		// 未开启鉴权：SSE 端点本就放行，无需票据。
		c.JSON(http.StatusOK, gin.H{"auth_required": false, "ticket": "", "expires_in": 0})
		return
	}
	subject := "admin"
	if v, ok := c.Get("auth_subject"); ok {
		if str, isStr := v.(string); isStr && str != "" {
			subject = str
		}
	}
	ticket, err := a.tickets.Issue(ticketPurposeSSE, subject, "", sseTicketTTL)
	if err != nil {
		c.JSON(http.StatusTooManyRequests, gin.H{"error": "too many pending tickets, retry shortly"})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"auth_required": true,
		"ticket":        ticket,
		"expires_in":    int(sseTicketTTL.Seconds()),
	})
}

// isMCPToken 判断字符串是否是某个 MCP 令牌（环境变量配置的，或凭据库里的，无论是否启用）。
// 它只用来给出明确的拒绝原因，绝不能据此放行：MCP 令牌代表「某个智能体」，而 REST / SSE 是给人类管理员的。
// IsMCPToken 判断给定字符串是否是一把有效的 MCP 令牌（环境变量令牌或凭据库令牌），供终端 REST 的闸门使用。
func (a *AuthHandler) IsMCPToken(tokenString string) bool { return a.isMCPToken(tokenString) }

func (a *AuthHandler) isMCPToken(tokenString string) bool {
	tokenString = strings.TrimSpace(tokenString)
	if tokenString == "" {
		return false
	}
	for _, envToken := range []string{a.cfg.MCPToken, a.cfg.ConfigureToken, a.cfg.ExecutionToken} {
		if envToken != "" && subtle.ConstantTimeCompare([]byte(tokenString), []byte(envToken)) == 1 {
			return true
		}
	}
	if a.store != nil {
		if cred, err := a.store.FindCredentialByToken(context.Background(), tokenString); err == nil && cred != nil {
			return true
		}
		if _, _, cred, err := a.store.FindOAuthAccessToken(context.Background(), tokenString); err == nil && cred != nil {
			return true
		}
	}
	return false
}

// Middleware 是业务接口的鉴权中间件：只接受 Authorization: Bearer 头。
// URL 查询串里的 token 会被访问日志、反代日志、浏览器历史与 Referer 泄漏，因此不再用于普通接口；
// 需要在 URL 上携带凭据的长连接（SSE）请使用 StreamMiddleware。
func (a *AuthHandler) Middleware() gin.HandlerFunc {
	return a.authMiddleware(false)
}

// StreamMiddleware 用于 SSE 等浏览器原生 API 无法自定义请求头的流式端点，凭据来源依次为：
//  1. Authorization: Bearer 头；
//  2. ?ticket=：由 POST /api/v1/auth/sse_ticket 签发的一次性票据（30 秒、单次，泄漏进日志也无价值）；
//  3. ?token=：旧版前端与第三方脚本的兼容通道，仅在 RELAYMESH_ALLOW_QUERY_TOKEN 未关闭时接受。
func (a *AuthHandler) StreamMiddleware() gin.HandlerFunc {
	return a.authMiddleware(true)
}

func (a *AuthHandler) authMiddleware(stream bool) gin.HandlerFunc {
	return func(c *gin.Context) {
		_, currentPass, credsOK := a.credentialsOrUnavailable(c)
		if !credsOK {
			respondCredentialsUnavailable(c)
			return
		}
		if currentPass == "" {
			c.Next()
			return
		}

		// 1. 尝试从 Authorization Header 获取 Bearer Token
		tokenString := ""
		authHeader := c.GetHeader("Authorization")
		if authHeader != "" && strings.HasPrefix(authHeader, "Bearer ") {
			tokenString = strings.TrimPrefix(authHeader, "Bearer ")
		}

		if stream && tokenString == "" {
			// 2. 一次性票据（推荐）
			if ticket := c.Query("ticket"); ticket != "" {
				if info, ok := a.tickets.Redeem(ticket, ticketPurposeSSE); ok {
					c.Set("auth_subject", info.Subject)
					c.Next()
					return
				}
				c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{
					"error":         "ticket invalid or expired",
					"auth_required": true,
				})
				return
			}
			// 3. 兼容：URL 上的长期凭据（可按环境变量关闭）
			if a.cfg.AllowQueryToken {
				tokenString = c.Query("token")
			}
		}

		// 校验 JWT 并支持 D-160 响应头静默滑动延期 (Sliding Expiration)
		if claims, ok := a.VerifyJWTToken(tokenString); ok {
			if sub, _ := (*claims)["sub"].(string); sub != "" {
				c.Set("auth_subject", sub)
			}
			if expVal, hasExp := (*claims)["exp"]; hasExp {
				var expSec int64
				switch v := expVal.(type) {
				case float64:
					expSec = int64(v)
				case int64:
					expSec = v
				}
				now := time.Now().Unix()
				// 若剩余时长不足 3 天（3 * 24 * 3600 = 259200 秒），自动无感顺延 7 天
				if expSec > now && (expSec-now) < 3*24*3600 {
					sub, _ := (*claims)["sub"].(string)
					if sub == "" {
						sub = "admin"
					}
					newToken := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
						"sub":  sub,
						"role": "admin",
						"exp":  time.Now().Add(7 * 24 * time.Hour).Unix(),
						"iat":  now,
					})
					if newTokenStr, err := newToken.SignedString([]byte(a.cfg.JWTSecret)); err == nil {
						c.Header("X-Renewed-Token", newTokenStr)
					}
				}
			}
			c.Next()
			return
		}

		// 管理接口（REST / SSE）只认人类登录签发的 JWT（SSE 另认一次性票据）。
		// MCP 令牌哪怕有效也一律拒绝：过去任何一把智能体令牌都等同管理员，可以改凭据、改设置、读全部会话。
		if a.isMCPToken(tokenString) {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{
				"error":         "MCP 令牌只能访问 /mcp 端点，不能调用管理接口（REST / SSE）；请使用 Web 登录会话",
				"code":          "mcp_token_forbidden",
				"auth_required": true,
			})
			return
		}
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{
			"error":         "authorization required or token expired",
			"auth_required": true,
		})
	}
}
