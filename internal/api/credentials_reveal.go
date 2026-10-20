package api

import (
	"crypto/subtle"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
)

// 「查看令牌」审计结果，取值与终端审计的 ok / fail / deny 一致。
const (
	revealResultOK   = "ok"
	revealResultFail = "fail"
	revealResultDeny = "deny"
)

// revealCredentialRequest 是「查看令牌」的请求体：开启登录保护时必须再次提交 Web 登录密码。
type revealCredentialRequest struct {
	Password string `json:"password"`
}

// RevealCredentialToken 在再次验证 Web 登录密码后返回某个 MCP 令牌的完整明文
// （POST /api/v1/credentials/:id/reveal，请求体 {"password": "..."}）。
//
// 令牌明文存放在库里（MCP 客户端靠它直连），列表接口只给掩码；这里是唯一能「读出旧令牌」的入口，所以：
//   - 只有人类登录态能到这里：路由挂在受 JWT 保护的组内，任何 MCP 令牌都会被中间件挡掉；
//   - 登录 JWT 之外必须再提交一次 Web 密码，JWT 泄露了也读不出令牌；
//   - 密码错误与登录共用同一份按 IP 的反爆破额度，达到上限即锁定；
//   - 每次尝试（成功、失败、被锁）都写审计，只记谁、从哪、哪个令牌，绝不记令牌与密码；
//   - 响应禁止缓存；密码错误用 403 而不是 401——401 会被前端当成「登录过期」而踢回登录页。
//
// 未设置 Web 密码（开放模式）时没有可验证的对象，直接放行：此时任何人本来就能重新生成令牌。
func (a *AuthHandler) RevealCredentialToken(c *gin.Context) {
	// 令牌明文只出现在这一个响应里，禁止任何中间层缓存。
	c.Header("Cache-Control", "no-store")
	c.Header("Pragma", "no-cache")

	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid credential id"})
		return
	}

	if a.store == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "存储不可用", "code": "reveal_unavailable"})
		return
	}

	ctx := c.Request.Context()
	ip := c.ClientIP()
	actor := c.GetString("auth_subject")
	target := fmt.Sprintf("令牌 #%d", id)

	// 放出机密属于「安全放行」，要用严格语义：读不到登录凭据就拒绝，而不是回退到环境变量值。
	_, expected, credErr := a.EffectiveCredentials(ctx)
	if credErr != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "暂时无法读取登录凭据，不能验证密码", "code": "reveal_unavailable"})
		return
	}

	verified := false
	if expected != "" {
		security := a.securitySettings(ctx)
		lockout := time.Duration(security.LockoutMinutes) * time.Minute

		if security.BruteForceProtection {
			if locked, remaining := a.limiter.CheckLocked(ip); locked {
				a.auditCredentialReveal(actor, ip, revealResultDeny, target+"（来源已被锁定）")
				c.JSON(http.StatusTooManyRequests, gin.H{
					"error":             fmt.Sprintf("由于连续多次尝试失败，该 IP 已被安全锁定，请在 %d 分钟后重试", int(remaining.Minutes())+1),
					"code":              "reveal_locked",
					"locked":            true,
					"remaining_seconds": int64(remaining.Seconds()),
					"lockout_minutes":   security.LockoutMinutes,
				})
				return
			}
		}

		var req revealCredentialRequest
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 16<<10)
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request format", "code": "bad_request"})
			return
		}

		// 恒定时间比对，防时序侧信道反推。
		if subtle.ConstantTimeCompare([]byte(req.Password), []byte(expected)) != 1 {
			if security.BruteForceProtection {
				if locked, remaining := a.limiter.RecordFailure(ip, security.MaxFailedAttempts, lockout); locked {
					a.auditCredentialReveal(actor, ip, revealResultDeny, target+"（连续密码错误，来源已被锁定）")
					c.JSON(http.StatusTooManyRequests, gin.H{
						"error":             fmt.Sprintf("连续尝试失败达到 %d 次上限，该 IP 已被锁定 %d 分钟", security.MaxFailedAttempts, security.LockoutMinutes),
						"code":              "reveal_locked",
						"locked":            true,
						"remaining_seconds": int64(remaining.Seconds()),
						"lockout_minutes":   security.LockoutMinutes,
					})
					return
				}
			}
			a.auditCredentialReveal(actor, ip, revealResultFail, target+"（密码错误）")
			c.JSON(http.StatusForbidden, gin.H{"error": "密码错误，请重新输入", "code": "reveal_bad_password"})
			return
		}

		if security.BruteForceProtection {
			a.limiter.RecordSuccess(ip)
		}
		verified = true
	}

	cred, err := a.store.GetCredentialIfVisible(ctx, uint(id))
	if err != nil || cred == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "令牌不存在或已被删除", "code": "credential_not_found"})
		return
	}

	detail := fmt.Sprintf("令牌 #%d「%s」", cred.ID, cred.Name)
	if !verified {
		detail += "（未设置登录密码，无需验证）"
	}
	a.auditCredentialReveal(actor, ip, revealResultOK, detail)

	resp := gin.H{"status": "ok", "token": cred.Token}
	if cred.Capability {
		resp["capability_path"] = capabilityPathFor(cred.Token)
	}
	c.JSON(http.StatusOK, resp)
}

// auditCredentialReveal 把一次「查看令牌」尝试交给审计；没有注册审计回调时（终端子系统未启用）退化为一行日志。
// 无论哪条路径，写出的内容都只有操作者、来源、结果和令牌编号 / 名称。
func (a *AuthHandler) auditCredentialReveal(actor, ip, result, detail string) {
	if a.onCredentialReveal != nil {
		a.onCredentialReveal(actor, ip, result, detail)
		return
	}
	log.Printf("[auth] 查看令牌 结果=%s 操作者=%q 来源=%s 详情=%q", result, actor, ip, detail)
}
