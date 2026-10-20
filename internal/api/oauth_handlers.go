package api

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/teacat99/RelayMesh/v2/internal/model"
	"github.com/teacat99/RelayMesh/v2/internal/oauth"
)

func (h *APIHandler) registerOAuthRoutes(v1 *gin.RouterGroup) {
	if h.oauth == nil {
		return
	}
	g := v1.Group("/oauth")
	g.GET("/policy", h.oauthGetPolicy)
	g.PUT("/policy", h.oauthPutPolicy)
	// 总开关（策略里的 enabled）是运行时在设置页改的：这些路由必须一直注册、每次请求再判断。
	// 过去只在启动时看一眼开关再决定注册与否，默认关闭的 Hub 在设置页打开之后，授权页的接口要等到重启才存在，
	// 整个授权流程在运行中根本走不通（单元测试都是先开开关再建服务，没有覆盖到）。
	g.GET("/requests/:id", h.requireOAuthEnabled, h.oauthGetRequest)
	g.POST("/requests/:id/decision", h.requireOAuthEnabled, h.oauthDecision)
	// 授权记录的查看与撤销不受总开关限制：关掉之后仍然要能把已有的授权清理掉。
	g.GET("/grants", h.oauthListGrants)
	g.DELETE("/grants/:id", h.oauthRevokeGrant)
}

// requireOAuthEnabled 总开关关闭时，新的授权请求（授权页读取 / 批准 / 拒绝）一律当作不存在。
func (h *APIHandler) requireOAuthEnabled(c *gin.Context) {
	if !h.oauth.Enabled() {
		c.AbortWithStatusJSON(http.StatusNotFound, gin.H{"error": "not found"})
	}
}

func (h *APIHandler) oauthGetRequest(c *gin.Context) {
	id := c.Param("id")
	view, ok := h.oauth.GetPending(id)
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"error": "request not found or expired"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"request": view})
}

func (h *APIHandler) oauthDecision(c *gin.Context) {
	id := c.Param("id")
	var body struct {
		Approve         bool     `json:"approve"`
		TerminalHosts   []string `json:"terminal_hosts"`
		TerminalModeCap string   `json:"terminal_mode_cap"`
		Nonce           string   `json:"nonce"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body"})
		return
	}
	// CSRF：nonce 必须等于待决请求编号
	if body.Nonce != id {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid nonce"})
		return
	}
	// 批准要授予终端能力，必须先解锁；拒绝只会减少授权，不需要解锁（前端的「拒绝」按钮也不会去解锁）。
	if body.Approve && !h.requireTerminalUnlock(c) {
		return
	}
	redirect, err := h.oauth.Decide(c.Request.Context(), id, oauth.ApproveDecision{
		Approve: body.Approve, TerminalHosts: body.TerminalHosts, TerminalModeCap: body.TerminalModeCap,
	})
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"redirect_url": redirect})
}

func (h *APIHandler) oauthListGrants(c *gin.Context) {
	grants, err := h.oauth.Store().ListOAuthGrants(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	type row struct {
		ID         uint     `json:"id"`
		ClientName string   `json:"client_name"`
		Hosts      []string `json:"terminal_hosts"`
		ModeCap    string   `json:"terminal_mode_cap"`
		Revoked    bool     `json:"revoked"`
		LastUsedAt *string  `json:"last_used_at"`
		LastUsedIP string   `json:"last_used_ip"`
		CreatedAt  string   `json:"created_at"`
	}
	out := make([]row, 0, len(grants))
	for _, g := range grants {
		cred, _ := h.store.GetCredential(c.Request.Context(), g.CredentialID)
		var hosts []string
		mode := ""
		var lastAt *string
		lastIP := ""
		if cred != nil {
			hosts = []string(cred.TerminalHosts)
			mode = cred.TerminalModeCap
			if cred.LastUsedAt != nil {
				s := cred.LastUsedAt.Format("2006-01-02T15:04:05Z07:00")
				lastAt = &s
			}
			lastIP = cred.LastUsedIP
		}
		out = append(out, row{
			ID: g.ID, ClientName: g.ClientDisplayName, Hosts: hosts, ModeCap: mode,
			Revoked: g.RevokedAt != nil, LastUsedAt: lastAt, LastUsedIP: lastIP,
			CreatedAt: g.CreatedAt.Format("2006-01-02T15:04:05Z07:00"),
		})
	}
	c.JSON(http.StatusOK, gin.H{"grants": out})
}

func (h *APIHandler) oauthRevokeGrant(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid id"})
		return
	}
	if err := h.oauth.RevokeGrant(c.Request.Context(), uint(id)); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

func (h *APIHandler) oauthGetPolicy(c *gin.Context) {
	p, err := h.oauth.Store().GetOAuthPolicy(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"policy": p})
}

func (h *APIHandler) oauthPutPolicy(c *gin.Context) {
	// 总开关、对外地址、DCR 与受信来源决定谁能向公网上的这台 Hub 申请终端能力，与签发令牌同级，需要解锁。
	if !h.requireTerminalUnlock(c) {
		return
	}
	var body struct {
		Policy model.OAuthPolicy `json:"policy"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body"})
		return
	}
	norm, err := oauth.NormalizeAndValidatePolicy(body.Policy)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if err := h.oauth.SavePolicy(c.Request.Context(), norm); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}
