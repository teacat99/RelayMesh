package api

import (
	"crypto/subtle"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"github.com/teacat99/RelayMesh/internal/config"
	"github.com/teacat99/RelayMesh/internal/store"
)

type AuthHandler struct {
	cfg     *config.Config
	store   *store.Store
	limiter *RateLimiter
}

func NewAuthHandler(cfg *config.Config, st *store.Store, limiter *RateLimiter) *AuthHandler {
	if limiter == nil {
		limiter = NewRateLimiter()
	}
	return &AuthHandler{
		cfg:     cfg,
		store:   st,
		limiter: limiter,
	}
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

// GetEffectiveCredentials 获取当前生效的账号和密码（优先数据库，兜底环境变量）
func (a *AuthHandler) GetEffectiveCredentials(c *gin.Context) (username string, password string) {
	// 默认环境变量值
	username = a.cfg.WebUsername
	if username == "" {
		username = "admin"
	}
	password = a.cfg.WebPassword

	if a.store != nil {
		if dbCreds, err := a.store.GetAuthCredentials(c.Request.Context()); err == nil && dbCreds != nil {
			if dbCreds.Username != "" {
				username = dbCreds.Username
			}
			if dbCreds.Password != "" {
				password = dbCreds.Password
			}
		}
	}
	return username, password
}

func (a *AuthHandler) Login(c *gin.Context) {
	expectedUser, expectedPass := a.GetEffectiveCredentials(c)

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
	security := store.SecuritySettings{
		BruteForceProtection: true,
		MaxFailedAttempts:    5,
		LockoutMinutes:       15,
	}
	if a.store != nil {
		if appSettings, err := a.store.GetGlobalAppSettings(c.Request.Context()); err == nil && appSettings != nil {
			security = appSettings.Security
			if security.MaxFailedAttempts <= 0 {
				security.MaxFailedAttempts = 5
			}
			if security.LockoutMinutes <= 0 {
				security.LockoutMinutes = 15
			}
		}
	}

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
	effectiveUser, effectivePass := a.GetEffectiveCredentials(c)
	isCustomized := false
	if a.store != nil {
		if dbCreds, err := a.store.GetAuthCredentials(c.Request.Context()); err == nil && dbCreds != nil {
			isCustomized = true
		}
	}

	c.JSON(http.StatusOK, gin.H{
		"auth_required":     effectivePass != "",
		"mcp_auth_required": a.cfg.MCPToken != "" || a.cfg.ConfigureToken != "" || a.cfg.ExecutionToken != "",
		"project_id":        a.cfg.ProjectID,
		"host_name":         a.cfg.HostName,
		"current_username":  effectiveUser,
		"is_customized":     isCustomized,
	})
}

// ChangeCredentials 修改管理账号和密码（需提供旧密码进行鉴权验证）
func (a *AuthHandler) ChangeCredentials(c *gin.Context) {
	_, currentPass := a.GetEffectiveCredentials(c)

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
			c.JSON(http.StatusUnauthorized, gin.H{"error": "当前旧密码不正确，拒绝修改"})
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

func (a *AuthHandler) VerifyToken(tokenString string) bool {
	if strings.TrimSpace(tokenString) == "" {
		return false
	}
	token, err := jwt.Parse(tokenString, func(t *jwt.Token) (interface{}, error) {
		return []byte(a.cfg.JWTSecret), nil
	})
	return err == nil && token.Valid
}

func (a *AuthHandler) Middleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		_, currentPass := a.GetEffectiveCredentials(c)
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

		// 2. 尝试从 URL Query 中获取 token (支持 SSE、图片预览或下载等特殊场景)
		if tokenString == "" {
			tokenString = c.Query("token")
		}

		if !a.VerifyToken(tokenString) {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{
				"error":         "authorization required or token expired",
				"auth_required": true,
			})
			return
		}

		c.Next()
	}
}
