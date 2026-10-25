package api

import (
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/teacat99/RelayMesh/v2/internal/model"
	"github.com/teacat99/RelayMesh/v2/internal/oauth"
	"github.com/teacat99/RelayMesh/v2/internal/terminal"
)

func (h *APIHandler) registerOAuthRoutes(v1 *gin.RouterGroup) {
	g := v1.Group("/oauth")
	// 策略的读取（连同「实际生效」的状态）一直可用，包括被环境变量 RELAYMESH_OAUTH=off 整个关掉的 Hub：
	// 设置页要靠它告诉用户「为什么没生效」，而不是一个 404。其余路由依赖授权服务本身，关掉时不存在。
	g.GET("/policy", h.oauthGetPolicy)
	if h.oauth == nil {
		return
	}
	g.PUT("/policy", h.oauthPutPolicy)
	// 总开关（策略里的 enabled）是运行时在设置页改的：这些路由必须一直注册、每次请求再判断。
	// 过去只在启动时看一眼开关再决定注册与否，默认关闭的 Hub 在设置页打开之后，授权页的接口要等到重启才存在，
	// 整个授权流程在运行中根本走不通（单元测试都是先开开关再建服务，没有覆盖到）。
	g.GET("/requests/:id", h.requireOAuthEnabled, h.oauthGetRequest)
	g.POST("/requests/:id/decision", h.requireOAuthEnabled, h.oauthDecision)
	// 授权记录的查看与撤销不受总开关限制：关掉之后仍然要能把已有的授权清理掉。
	g.GET("/grants", h.oauthListGrants)
	g.DELETE("/grants/:id", h.oauthRevokeGrant)
	// 接入窗口与「刚登记、还没授权的客户端」（#239）：与 ExecMesh 的「接入主机」窗口同一套做法。读取随时可用；
	// 开启 / 续期 / 关闭窗口与删除登记都改变「公网上谁能向这台 Hub 登记」，需要终端解锁。
	g.GET("/clients", h.oauthListClients)
	g.DELETE("/clients/:id", h.oauthDeleteClient)
	g.POST("/window", h.oauthOpenWindow)
	g.DELETE("/window", h.oauthCloseWindow)
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

// oauthGetPolicy 返回可编辑的策略，以及只读的 effective（现在到底有没有在服务、对外地址与来源、连接器地址）。
// effective 是新增字段：旧前端忽略它，新前端遇到没有它的旧后端会退回到只显示策略。
func (h *APIHandler) oauthGetPolicy(c *gin.Context) {
	p, err := h.store.GetOAuthPolicy(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	eff := oauth.Effective(h.cfg, h.store, c.Request)
	if h.oauth != nil {
		// 接入窗口的开合在授权服务里（只存内存），Effective 只能按策略算出方式，这里用服务里的真实状态覆盖。
		eff.DCR = h.oauth.DCRStatus(p)
	}
	c.JSON(http.StatusOK, gin.H{"policy": p, "effective": eff})
}

// oauthAdminAudit 把管理员在设置里对 OAuth 做的操作记进审计（操作者与来源地址取自当前登录，detail 不含机密）。
func (h *APIHandler) oauthAdminAudit(c *gin.Context, event, detail string) {
	h.terminal.RecordOAuthAdmin(event, c.GetString("auth_subject"), c.ClientIP(), detail)
}

// oauthClientRefRE 校验设置页传来的登记指认（client_id 末尾的随机段：16 字节的小写十六进制）。
var oauthClientRefRE = regexp.MustCompile(`^[0-9a-f]{32}$`)

// oauthWindowConflict 在接入窗口不适用的情况下回 409，并带稳定的 code 让前端给出对应的说明；返回 true 表示已经回复。
// 窗口只在「授权服务器开着、自动注册是限时窗口」时有意义：关着、关闭注册或常开时开 / 续窗口都没有效果，不如明说。
func (h *APIHandler) oauthWindowConflict(c *gin.Context, p model.OAuthPolicy) bool {
	switch {
	case !h.oauth.Enabled():
		c.JSON(http.StatusConflict, gin.H{"error": "OAuth 授权服务器没有开启，先在「总开关」里打开它", "code": "oauth_disabled"})
	case oauth.DCRModeOf(p) == oauth.DCRModeOff:
		c.JSON(http.StatusConflict, gin.H{"error": "自动注册已关闭，先把「自动注册」改成「限时窗口」", "code": "dcr_off"})
	case oauth.DCRModeOf(p) == oauth.DCRModeAlways:
		c.JSON(http.StatusConflict, gin.H{"error": "自动注册是「常开」，随时都能登记，不需要窗口", "code": "dcr_always"})
	default:
		return false
	}
	return true
}

// oauthOpenWindow 开启（extend=false：把剩余时间重置为 minutes）或续期（extend=true：在剩余时间上累加 minutes）接入窗口。
// minutes 省略或 ≤0 用默认 10 分钟，超过 60 分钟按 60 分钟；总剩余永远不超过 60 分钟。
func (h *APIHandler) oauthOpenWindow(c *gin.Context) {
	if !h.requireTerminalUnlock(c) {
		return
	}
	var body struct {
		Minutes int  `json:"minutes"`
		Extend  bool `json:"extend"`
	}
	// 没带请求体等同于「开默认时长」；带了就必须是合法 JSON。
	if c.Request.ContentLength != 0 {
		if err := c.ShouldBindJSON(&body); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body"})
			return
		}
	}
	p, err := h.store.GetOAuthPolicy(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if h.oauthWindowConflict(c, p) {
		return
	}
	until := h.oauth.OpenDCRWindow(time.Duration(body.Minutes)*time.Minute, body.Extend)
	action := "open"
	if body.Extend {
		action = "extend"
	}
	h.oauthAdminAudit(c, terminal.EvOAuthWindowOpen, fmt.Sprintf("%s until=%s", action, until.UTC().Format(time.RFC3339)))
	c.JSON(http.StatusOK, gin.H{"dcr": h.oauth.DCRStatus(p)})
}

// oauthCloseWindow 立即关闭接入窗口。窗口本来就没开着也是成功（幂等），只是不记审计。
func (h *APIHandler) oauthCloseWindow(c *gin.Context) {
	if !h.requireTerminalUnlock(c) {
		return
	}
	p, err := h.store.GetOAuthPolicy(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if h.oauth.CloseDCRWindow() {
		h.oauthAdminAudit(c, terminal.EvOAuthWindowClose, "manual")
	}
	c.JSON(http.StatusOK, gin.H{"dcr": h.oauth.DCRStatus(p)})
}

// oauthListClients 列出「刚登记、还没授权」的客户端（DCR 登记之后还没走完授权、也没过期的），新的在前。
// 给管理员在窗口开着时核对「刚才来登记的是不是我自己的连接器」：名字、回调主机、陌生来源标记、登记时间与来源地址。
func (h *APIHandler) oauthListClients(c *gin.Context) {
	list, err := h.oauth.ListUnusedClients(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	type row struct {
		ID            string   `json:"id"`
		ClientName    string   `json:"client_name"`
		RedirectHosts []string `json:"redirect_hosts"`
		Untrusted     bool     `json:"untrusted"`
		Loopback      bool     `json:"loopback"`
		CreatedAt     string   `json:"created_at"`
		CreatedIP     string   `json:"created_ip"`
	}
	out := make([]row, 0, len(list))
	for _, cl := range list {
		sum := oauth.SummarizeRedirects([]string(cl.RedirectURIs))
		hosts := sum.Hosts
		if hosts == nil {
			hosts = []string{}
		}
		out = append(out, row{
			ID: oauth.ClientRef(cl.ClientID), ClientName: cl.ClientName, RedirectHosts: hosts,
			Untrusted: sum.Untrusted, Loopback: sum.Loopback,
			CreatedAt: cl.CreatedAt.Format(time.RFC3339), CreatedIP: cl.CreatedIP,
		})
	}
	c.JSON(http.StatusOK, gin.H{"clients": out, "expire_hours": int(oauth.UnusedClientMaxAge / time.Hour)})
}

// oauthDeleteClient 删除一条还没授权过的登记（已经授权过的不删，要撤销请在「已授权的应用」里操作）。
func (h *APIHandler) oauthDeleteClient(c *gin.Context) {
	if !h.requireTerminalUnlock(c) {
		return
	}
	ref := c.Param("id")
	if !oauthClientRefRE.MatchString(ref) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid id"})
		return
	}
	cl, err := h.oauth.FindUnusedClientByRef(c.Request.Context(), ref)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if cl == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "client not found, or already authorized"})
		return
	}
	ok, err := h.oauth.DeleteRegisteredClient(c.Request.Context(), cl.ClientID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if !ok {
		// 找到之后、删除之前它刚好被授权了：按「已经授权」处理，不删。
		c.JSON(http.StatusNotFound, gin.H{"error": "client not found, or already authorized"})
		return
	}
	h.oauthAdminAudit(c, terminal.EvOAuthClientDelete, cl.ClientName)
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

func (h *APIHandler) oauthPutPolicy(c *gin.Context) {
	// 总开关、对外地址与 DCR 决定谁能向公网上的这台 Hub 申请终端能力，与签发令牌同级，需要解锁。
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
