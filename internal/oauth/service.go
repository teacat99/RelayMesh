package oauth

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/teacat99/RelayMesh/v2/internal/config"
	"github.com/teacat99/RelayMesh/v2/internal/model"
	"github.com/teacat99/RelayMesh/v2/internal/store"
)

const refreshGrace = 10 * time.Second

// AuditFn 记录 OAuth 审计事件（不含机密）。
type AuditFn func(event, result, detail string)

// RevokeFn 撤销隐藏凭据时结束进行中的命令（与删除令牌相同）。
type RevokeFn func(credentialID uint, reason string)

// Service OAuth 授权服务器与资源元数据。
type Service struct {
	cfg    *config.Config
	store  *store.Store
	mem    *memoryStore
	audit  AuditFn
	revoke RevokeFn
	clock  func() time.Time // 测试注入；nil 时用 package 级 serviceNow

	dcrMu   sync.Mutex
	dcrHits map[string][]time.Time
	// dcrWin 是自动注册的「接入窗口」（#239）：只存内存，Hub 重启即关闭；见 dcr_window.go。
	dcrWin dcrWindow

	oauthRL *oauthRateLimiter

	grantRefreshMu    sync.Mutex
	grantRefreshLocks map[uint]*sync.Mutex
}

func NewService(cfg *config.Config, st *store.Store, audit AuditFn, revoke RevokeFn) *Service {
	// 待决请求占的限流名额由 memoryStore 在请求离开待决状态时归还（#240），所以两者在这里接起来。
	rl := newOAuthRateLimiter()
	mem := newMemoryStore()
	mem.release = rl.releasePending
	return &Service{
		cfg:               cfg,
		store:             st,
		mem:               mem,
		audit:             audit,
		revoke:            revoke,
		dcrHits:           make(map[string][]time.Time),
		oauthRL:           rl,
		grantRefreshLocks: make(map[uint]*sync.Mutex),
	}
}

func (s *Service) lockGrantRefresh(grantID uint) func() {
	s.grantRefreshMu.Lock()
	mu, ok := s.grantRefreshLocks[grantID]
	if !ok {
		mu = &sync.Mutex{}
		s.grantRefreshLocks[grantID] = mu
	}
	s.grantRefreshMu.Unlock()
	mu.Lock()
	return mu.Unlock
}

func (s *Service) Enabled() bool {
	return ProtocolEnabled(s.cfg, s.store)
}

func (s *Service) WWWAuthenticate(r *http.Request) string {
	return WWWAuthenticateValue(s.cfg, s.store, r)
}

// Mount 注册 /.well-known/* 与 /oauth/*（须在 SPA NoRoute 之前注册）。
func (s *Service) Mount(engine *gin.Engine) {
	if s == nil || s.cfg == nil || s.cfg.OAuthOff {
		return
	}
	engine.GET("/.well-known/oauth-protected-resource", s.handleProtectedResource)
	engine.GET("/.well-known/oauth-protected-resource/mcp", s.handleProtectedResource)
	engine.GET("/.well-known/oauth-authorization-server", s.handleAuthServerMetadata)
	engine.GET("/.well-known/openid-configuration", s.handleOpenIDConfigurationDisabled)
	engine.GET("/oauth/authorize", s.handleAuthorize)
	engine.POST("/oauth/token", s.handleToken)
	engine.POST("/oauth/register", s.handleRegister)
	engine.POST("/oauth/revoke", s.handleRevoke)
}

func (s *Service) json404(c *gin.Context) {
	c.JSON(http.StatusNotFound, gin.H{"error": "not_found"})
}

func (s *Service) handleProtectedResource(c *gin.Context) {
	if !s.Enabled() {
		s.json404(c)
		return
	}
	issuer, derived := s.issuerDerived(c.Request)
	resource, _ := PublicBase(s.cfg, s.store, c.Request)
	resource += resourcePath
	s.applyDerivedHeaders(c, derived)
	if !derived {
		c.Header("Cache-Control", "public, max-age=3600")
	}
	c.JSON(http.StatusOK, gin.H{
		"resource":                 resource,
		"authorization_servers":    []string{issuer},
		"scopes_supported":         []string{"exec", "exec:files"},
		"bearer_methods_supported": []string{"header"},
	})
}

func (s *Service) handleOpenIDConfigurationDisabled(c *gin.Context) {
	if !s.Enabled() {
		s.json404(c)
		return
	}
	s.json404(c)
}

func (s *Service) handleAuthServerMetadata(c *gin.Context) {
	if !s.Enabled() {
		s.json404(c)
		return
	}
	base, derived := s.issuerDerived(c.Request)
	s.applyDerivedHeaders(c, derived)
	if !derived {
		c.Header("Cache-Control", "public, max-age=3600")
	}
	c.JSON(http.StatusOK, gin.H{
		"issuer":                                         base,
		"authorization_endpoint":                         base + "/oauth/authorize",
		"token_endpoint":                                 base + "/oauth/token",
		"registration_endpoint":                          base + "/oauth/register",
		"revocation_endpoint":                            base + "/oauth/revoke",
		"response_types_supported":                       []string{"code"},
		"grant_types_supported":                          []string{"authorization_code", "refresh_token"},
		"code_challenge_methods_supported":               []string{"S256"},
		"token_endpoint_auth_methods_supported":          []string{"none"},
		"client_id_metadata_document_supported":          true,
		"authorization_response_iss_parameter_supported": true,
	})
}

func (s *Service) requireHTTPS(c *gin.Context) bool {
	if c.Request.TLS != nil {
		return true
	}
	if strings.EqualFold(c.GetHeader("X-Forwarded-Proto"), "https") {
		return true
	}
	host := c.Request.Host
	if i := strings.Index(host, ":"); i >= 0 {
		host = host[:i]
	}
	if isLoopbackHost(host) {
		return true
	}
	return false
}

func (s *Service) authErrorRedirect(c *gin.Context, redirectURI, state, errCode, desc string) {
	iss, _ := s.issuerDerived(c.Request)
	if redirectURI == "" {
		s.authClientError(c, errCode, desc)
		return
	}
	u, _ := url.Parse(redirectURI)
	q := u.Query()
	q.Set("error", errCode)
	if desc != "" {
		q.Set("error_description", desc)
	}
	if state != "" {
		q.Set("state", state)
	}
	q.Set("iss", iss)
	u.RawQuery = q.Encode()
	c.Redirect(http.StatusFound, u.String())
}

func (s *Service) handleAuthorize(c *gin.Context) {
	if !s.Enabled() {
		s.json404(c)
		return
	}
	ip := c.ClientIP()
	if !s.oauthRL.allowEndpoint(ip, s.now()) {
		writeOAuthRateLimited(c, 60)
		return
	}
	if !s.requireHTTPS(c) {
		s.authClientError(c, "invalid_request", "authorization endpoint requires https")
		return
	}
	q := c.Request.URL.Query()
	clientID := strings.TrimSpace(q.Get("client_id"))
	redirectURI := strings.TrimSpace(q.Get("redirect_uri"))
	responseType := strings.TrimSpace(q.Get("response_type"))
	scope := strings.TrimSpace(q.Get("scope"))
	state := strings.TrimSpace(q.Get("state"))
	resource := strings.TrimSpace(q.Get("resource"))
	codeChallenge := strings.TrimSpace(q.Get("code_challenge"))
	codeChallengeMethod := strings.TrimSpace(q.Get("code_challenge_method"))

	if clientID == "" {
		s.authClientError(c, "invalid_client", "client_id required")
		return
	}
	clientName, redirectURIs, registered, err := s.resolveClient(c.Request.Context(), clientID)
	if err != nil {
		s.authClientError(c, "invalid_client", "")
		return
	}
	if redirectURI == "" {
		s.authClientError(c, "invalid_request", "redirect_uri required")
		return
	}
	if err := ValidateRedirectURI(redirectURI); err != nil {
		s.authClientError(c, "invalid_request", "redirect_uri invalid")
		return
	}
	if !RedirectURIInList(redirectURIs, redirectURI) {
		s.authClientError(c, "invalid_request", "redirect_uri mismatch")
		return
	}

	if state == "" {
		s.authErrorRedirect(c, redirectURI, state, "invalid_request", "state required")
		return
	}
	if responseType != "code" {
		s.authErrorRedirect(c, redirectURI, state, "unsupported_response_type", "")
		return
	}
	if codeChallengeMethod != "S256" || !ValidateCodeChallenge(codeChallenge) {
		s.authErrorRedirect(c, redirectURI, state, "invalid_request", "pkce S256 required")
		return
	}
	wantResource := ResourceURL(s.cfg, s.store, c.Request)
	if resource != "" && resource != wantResource {
		s.authErrorRedirect(c, redirectURI, state, "invalid_target", "resource mismatch")
		return
	}
	if resource == "" {
		resource = wantResource
	}

	// 先回收已过期的待决请求（及其占的名额），再申请新的名额：被放弃的请求过期后名额自动归还，
	// 不会因为用户打开授权页又关掉、或客户端发起后不再回来而一直占着（#240）。
	s.mem.sweep()
	if !s.oauthRL.reservePending(ip, clientID) {
		writeOAuthRateLimited(c, 120)
		return
	}

	id, err := randomID()
	if err != nil {
		s.oauthRL.releasePending(ip, clientID)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "server_error"})
		return
	}
	s.mem.putPending(&pendingRequest{
		ID: id, ClientID: clientID, ClientName: clientName, Registered: registered, RedirectURI: redirectURI,
		Resource: resource, Scope: scope, State: state,
		CodeChallenge: codeChallenge, CodeChallengeMethod: codeChallengeMethod,
		CreatedAt: s.now(), IP: ip,
	})
	base, derived := s.issuerDerived(c.Request)
	s.applyDerivedHeaders(c, derived)
	c.Header("X-Frame-Options", "DENY")
	c.Header("Content-Security-Policy", "frame-ancestors 'none'")
	c.Redirect(http.StatusFound, base+"/oauth/consent?req="+url.QueryEscape(id))
}

// resolveClient 找到客户端的名字与登记的回调地址。registered 为真表示它是本服务登记的 DCR 客户端，
// 为假表示是 CIMD（现取元数据）——批准页的陌生来源提示要按这个区分，见 WarnUntrustedClient。
func (s *Service) resolveClient(ctx context.Context, clientID string) (name string, redirects []string, registered bool, err error) {
	if cl, e := s.store.GetOAuthClient(ctx, clientID); e == nil && cl != nil {
		return cl.ClientName, []string(cl.RedirectURIs), true, nil
	}
	// CIMD：client_id 必须等于元数据文档 URL
	if strings.HasPrefix(strings.ToLower(clientID), "https://") {
		doc, e := FetchCIMD(ctx, clientID)
		if e != nil {
			return "", nil, false, e
		}
		return doc.ClientName, doc.RedirectURIs, false, nil
	}
	return "", nil, false, fmt.Errorf("invalid_client")
}

func randomID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// PendingRequest 供 REST 读取待决授权。
type PendingRequestView struct {
	ID            string   `json:"id"`
	ClientID      string   `json:"client_id"`
	ClientName    string   `json:"client_name"`
	RedirectHost  string   `json:"redirect_host"`
	Scopes        []string `json:"scopes"`
	Resource      string   `json:"resource"`
	WarnUntrusted bool     `json:"warn_untrusted"`
	WarnLoopback  bool     `json:"warn_loopback"`
}

func (s *Service) GetPending(id string) (*PendingRequestView, bool) {
	p, ok := s.mem.getPending(id)
	if !ok {
		return nil, false
	}
	return &PendingRequestView{
		ID: id, ClientID: p.ClientID, ClientName: p.ClientName,
		RedirectHost:  RedirectHost(p.RedirectURI),
		Scopes:        ParseScopeString(p.Scope),
		Resource:      p.Resource,
		WarnUntrusted: WarnUntrustedClient(p.ClientID, p.RedirectURI, p.Registered),
		WarnLoopback:  IsLoopbackRedirect(p.RedirectURI),
	}, true
}

// ApproveDecision 用户批准授权（已通过 Web 登录与解锁校验）。
type ApproveDecision struct {
	TerminalHosts   []string
	TerminalModeCap string
	Approve         bool
	DenyReason      string
}

func (s *Service) Decide(ctx context.Context, reqID string, d ApproveDecision) (redirectURL string, err error) {
	// 原子地取走待决请求（同时归还它占的限流名额）：并发到达的多个决定里只有一个能取到。
	p, ok := s.mem.takePending(reqID)
	if !ok {
		return "", fmt.Errorf("request expired")
	}
	iss, _ := ResolvePublicBase(s.cfg, s.store, nil)

	if !d.Approve {
		if s.audit != nil {
			s.audit("oauth_consent_denied", "ok", "client="+p.ClientName)
		}
		u, _ := url.Parse(p.RedirectURI)
		q := u.Query()
		q.Set("error", "access_denied")
		q.Set("state", p.State)
		q.Set("iss", iss)
		u.RawQuery = q.Encode()
		return u.String(), nil
	}

	scopes := ParseScopeString(p.Scope)
	perms, scopeOK := ScopesToPermissions(scopes)
	if !scopeOK {
		perms = model.Permissions{Terminal: true}
	}
	policy, _ := s.store.GetOAuthPolicy(ctx)
	idle := time.Duration(policy.RefreshIdleDays) * 24 * time.Hour
	abs := time.Duration(policy.RefreshAbsoluteDays) * 24 * time.Hour
	now := s.now()
	refExp := now.Add(idle)
	absExp := now.Add(abs)

	grant := &model.OAuthGrant{
		ClientID: p.ClientID, ClientDisplayName: p.ClientName,
		RedirectURI: p.RedirectURI, Resource: p.Resource,
		Scopes: model.StringList(scopes), CreatedAt: now, UpdatedAt: now,
		RefreshExpiresAt: &refExp, AbsoluteExpiresAt: &absExp,
	}
	if err := s.store.CreateOAuthGrant(ctx, grant); err != nil {
		return "", err
	}
	credName := fmt.Sprintf("OAuth: %s · %d", p.ClientName, grant.ID)
	if strings.TrimSpace(p.ClientName) == "" {
		credName = fmt.Sprintf("OAuth 客户端 · %d", grant.ID)
	}
	cred := &model.MCPCredential{
		IsActive: false, Permissions: perms,
		TerminalHosts:   model.StringList(d.TerminalHosts),
		TerminalModeCap: d.TerminalModeCap,
	}
	if err := s.store.CreateHiddenOAuthCredential(ctx, grant.ID, credName, cred); err != nil {
		return "", err
	}
	if err := s.store.UpdateOAuthGrantCredential(ctx, grant.ID, cred.ID); err != nil {
		return "", err
	}

	code, err := GenerateAuthCode()
	if err != nil {
		return "", err
	}
	s.mem.putCode(&authCodeEntry{
		Code: code, ClientID: p.ClientID, RedirectURI: p.RedirectURI, Resource: p.Resource,
		Scope: p.Scope, CodeChallenge: p.CodeChallenge, CodeChallengeMethod: p.CodeChallengeMethod,
		GrantID: grant.ID, ExpiresAt: now.Add(authCodeTTL),
	})
	if s.audit != nil {
		s.audit("oauth_consent_granted", "ok", "client="+p.ClientName)
	}
	u, _ := url.Parse(p.RedirectURI)
	q := u.Query()
	q.Set("code", code)
	q.Set("state", p.State)
	q.Set("iss", iss)
	u.RawQuery = q.Encode()
	return u.String(), nil
}

const tokenEndpointMaxBody = 16 << 10

func (s *Service) handleToken(c *gin.Context) {
	if !s.Enabled() {
		s.json404(c)
		return
	}
	if !s.oauthRL.allowEndpoint(c.ClientIP(), s.now()) {
		writeOAuthRateLimited(c, 60)
		return
	}
	ct := strings.ToLower(strings.TrimSpace(c.GetHeader("Content-Type")))
	if ct != "" && !strings.HasPrefix(ct, "application/x-www-form-urlencoded") {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_request"})
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, tokenEndpointMaxBody)
	if err := c.Request.ParseForm(); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_request"})
		return
	}
	grantType := c.PostForm("grant_type")
	switch grantType {
	case "authorization_code":
		s.tokenAuthorizationCode(c)
	case "refresh_token":
		s.tokenRefresh(c)
	default:
		c.JSON(http.StatusBadRequest, gin.H{"error": "unsupported_grant_type"})
	}
}

func (s *Service) tokenAuthorizationCode(c *gin.Context) {
	code := strings.TrimSpace(c.PostForm("code"))
	redirectURI := strings.TrimSpace(c.PostForm("redirect_uri"))
	clientID := strings.TrimSpace(c.PostForm("client_id"))
	verifier := strings.TrimSpace(c.PostForm("code_verifier"))
	resource := strings.TrimSpace(c.PostForm("resource"))

	entry, replay, ok := s.mem.redeemCode(code)
	if replay && entry != nil {
		ctx := c.Request.Context()
		_ = s.store.RevokeOAuthGrant(ctx, entry.GrantID)
		_ = s.store.DeleteOAuthAccessTokensForGrant(ctx, entry.GrantID)
		grant, _ := s.store.GetOAuthGrant(ctx, entry.GrantID)
		if grant != nil {
			_ = s.store.DeactivateCredential(ctx, grant.CredentialID)
			if s.revoke != nil {
				s.revoke(grant.CredentialID, "oauth_code_replay")
			}
		}
		if s.audit != nil {
			s.audit("oauth_code_replay", "deny", "")
		}
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_grant"})
		return
	}
	if !ok {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_grant"})
		return
	}
	if entry.ClientID != clientID || !RedirectURIMatches(entry.RedirectURI, redirectURI) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_grant"})
		return
	}
	if resource != "" && resource != entry.Resource {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_target"})
		return
	}
	if !ValidateCodeVerifier(verifier) || !VerifyPKCE(entry.CodeChallenge, entry.CodeChallengeMethod, verifier) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_grant"})
		return
	}
	s.issueTokens(c, entry.GrantID)
}

func (s *Service) tokenRefresh(c *gin.Context) {
	refresh := strings.TrimSpace(c.PostForm("refresh_token"))
	clientID := strings.TrimSpace(c.PostForm("client_id"))
	resource := strings.TrimSpace(c.PostForm("resource"))
	hash := store.HashOAuthToken(refresh)
	ctx := c.Request.Context()

	grant, err := s.store.GetOAuthGrantByRefreshHash(ctx, hash)
	if err != nil || grant == nil || grant.RevokedAt != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_grant"})
		return
	}
	if grant.ClientID != clientID {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_grant"})
		return
	}
	if resource != "" && resource != grant.Resource {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_target"})
		return
	}
	unlock := s.lockGrantRefresh(grant.ID)
	defer unlock()

	now := s.now()
	if grant.RefreshExpiresAt != nil && now.After(*grant.RefreshExpiresAt) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_grant"})
		return
	}
	if grant.AbsoluteExpiresAt != nil && now.After(*grant.AbsoluteExpiresAt) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_grant"})
		return
	}

	// 重放检测：若使用的是上一枚刷新令牌且已过宽限期 → 作废整条授权
	usingPrevious := grant.PreviousRefreshHash != "" && hash == grant.PreviousRefreshHash
	if usingPrevious && (grant.RefreshGraceUntil == nil || now.After(*grant.RefreshGraceUntil)) {
		_ = s.store.InvalidateOAuthGrantFamily(ctx, grant.ID)
		_ = s.store.DeleteOAuthAccessTokensForGrant(ctx, grant.ID)
		if s.revoke != nil {
			s.revoke(grant.CredentialID, "oauth_refresh_replay")
		}
		if s.audit != nil {
			s.audit("oauth_refresh_replay", "deny", "")
		}
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_grant"})
		return
	}

	newRef, newHash, err := GenerateRefreshToken()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "server_error"})
		return
	}
	grace := now.Add(refreshGrace)
	_ = s.store.UpdateOAuthGrantRefresh(ctx, grant.ID, newHash, &grace)
	policy, _ := s.store.GetOAuthPolicy(ctx)
	idle := now.Add(time.Duration(policy.RefreshIdleDays) * 24 * time.Hour)
	_ = s.store.UpdateOAuthGrantRefreshIdle(ctx, grant.ID, &idle)

	accessPlain, accessHash, err := GenerateAccessToken()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "server_error"})
		return
	}
	ttl := time.Duration(policy.AccessTokenTTLMinutes) * time.Minute
	exp := now.Add(ttl)
	_ = s.store.InsertOAuthAccessToken(ctx, accessHash, grant.ID, exp)
	if s.audit != nil {
		s.audit("oauth_token_refresh", "ok", "")
	}
	s.writeTokenResponse(c, accessPlain, newRef, int(ttl.Seconds()), strings.Join([]string(grant.Scopes), " "))
}

func (s *Service) issueTokens(c *gin.Context, grantID uint) {
	ctx := c.Request.Context()
	policy, _ := s.store.GetOAuthPolicy(ctx)
	ttl := time.Duration(policy.AccessTokenTTLMinutes) * time.Minute
	now := s.now()
	exp := now.Add(ttl)

	accessPlain, accessHash, err := GenerateAccessToken()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "server_error"})
		return
	}
	_ = s.store.InsertOAuthAccessToken(ctx, accessHash, grantID, exp)

	grant, _ := s.store.GetOAuthGrant(ctx, grantID)
	if grant == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "server_error"})
		return
	}
	refPlain, _, err := GenerateRefreshToken()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "server_error"})
		return
	}
	refHash := store.HashOAuthToken(refPlain)
	policy, _ = s.store.GetOAuthPolicy(ctx)
	idle := now.Add(time.Duration(policy.RefreshIdleDays) * 24 * time.Hour)
	abs := grant.AbsoluteExpiresAt
	_ = s.store.UpdateOAuthGrantRefreshMeta(ctx, grantID, refHash, &idle, abs)
	_ = s.store.ActivateCredential(ctx, grant.CredentialID)
	s.store.TouchOAuthClient(ctx, grant.ClientID)
	if s.audit != nil {
		s.audit("oauth_token_issued", "ok", "")
	}
	s.writeTokenResponse(c, accessPlain, refPlain, int(ttl.Seconds()), strings.Join([]string(grant.Scopes), " "))
}

func (s *Service) writeTokenResponse(c *gin.Context, access, refresh string, expiresIn int, scope string) {
	c.Header("Cache-Control", "no-store")
	c.Header("Pragma", "no-cache")
	c.JSON(http.StatusOK, gin.H{
		"access_token":  access,
		"token_type":    "Bearer",
		"expires_in":    expiresIn,
		"refresh_token": refresh,
		"scope":         scope,
	})
}

func (s *Service) handleRegister(c *gin.Context) {
	if !s.Enabled() {
		s.json404(c)
		return
	}
	if !s.oauthRL.allowEndpoint(c.ClientIP(), s.now()) {
		writeOAuthRateLimited(c, 60)
		return
	}
	policy, _ := s.store.GetOAuthPolicy(c.Request.Context())
	if ok, reason := s.dcrAccepting(policy); !ok {
		body := gin.H{"error": reason}
		if reason == "registration_window_closed" {
			// 客户端（ChatGPT / Claude 等）常把这句话原样显示给用户，所以写清楚去哪开；中英文各一句。
			body["error_description"] = "自动注册当前没有开放：请 RelayMesh 管理员在「设置 → OAuth 授权」里开启接入窗口，然后重新添加连接器。" +
				" Registration is closed: the RelayMesh administrator must open the registration window in Settings → OAuth, then add the connector again."
		}
		c.JSON(http.StatusForbidden, body)
		return
	}
	if !s.allowDCR(c.ClientIP()) {
		c.JSON(http.StatusTooManyRequests, gin.H{"error": "slow_down"})
		return
	}
	var body struct {
		ClientName    string   `json:"client_name"`
		RedirectURIs  []string `json:"redirect_uris"`
		GrantTypes    []string `json:"grant_types"`
		ResponseTypes []string `json:"response_types"`
		TokenAuth     string   `json:"token_endpoint_auth_method"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_client_metadata"})
		return
	}
	if err := ValidateRedirectURIList(body.RedirectURIs); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_redirect_uri"})
		return
	}
	if body.TokenAuth != "" && body.TokenAuth != "none" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_client_metadata"})
		return
	}
	_ = s.store.PurgeUnusedOAuthClients(c.Request.Context(), UnusedClientMaxAge)
	n, _ := s.store.CountOAuthClients(c.Request.Context())
	if n >= store.OAuthClientMax {
		if freed := s.store.RecycleOldestUnusedOAuthClient(c.Request.Context()); !freed {
			c.Header("Retry-After", "3600")
			c.JSON(http.StatusTooManyRequests, gin.H{"error": "registration_quota"})
			return
		}
	}
	id, err := randomID()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "server_error"})
		return
	}
	clientID := Issuer(s.cfg, s.store, c.Request) + clientRefPrefix + id
	now := s.now()
	cl := &model.OAuthClient{
		ClientID: clientID, ClientName: body.ClientName,
		RedirectURIs:  model.StringList(body.RedirectURIs),
		GrantTypes:    model.StringList(coalesce(body.GrantTypes, []string{"authorization_code", "refresh_token"})),
		ResponseTypes: model.StringList(coalesce(body.ResponseTypes, []string{"code"})),
		TokenAuth:     "none", CreatedAt: now, CreatedIP: c.ClientIP(),
	}
	if err := s.store.CreateOAuthClient(c.Request.Context(), cl); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "server_error"})
		return
	}
	if s.audit != nil {
		s.audit("oauth_client_register", "ok", cl.ClientName)
	}
	c.JSON(http.StatusCreated, gin.H{
		"client_id":                  clientID,
		"client_name":                cl.ClientName,
		"redirect_uris":              body.RedirectURIs,
		"grant_types":                cl.GrantTypes,
		"response_types":             cl.ResponseTypes,
		"token_endpoint_auth_method": "none",
		"client_id_issued_at":        now.Unix(),
	})
}

func coalesce(a []string, def []string) []string {
	if len(a) == 0 {
		return def
	}
	return a
}

func (s *Service) allowDCR(ip string) bool {
	s.dcrMu.Lock()
	defer s.dcrMu.Unlock()
	now := s.now()
	list := s.dcrHits[ip]
	cut := now.Add(-time.Minute)
	j := 0
	for _, t := range list {
		if t.After(cut) {
			list[j] = t
			j++
		}
	}
	list = list[:j]
	if len(list) >= 10 {
		s.dcrHits[ip] = list
		return false
	}
	s.dcrHits[ip] = append(list, now)
	return true
}

func (s *Service) handleRevoke(c *gin.Context) {
	if !s.Enabled() {
		s.json404(c)
		return
	}
	if !s.oauthRL.allowEndpoint(c.ClientIP(), s.now()) {
		writeOAuthRateLimited(c, 60)
		return
	}
	token := strings.TrimSpace(c.PostForm("token"))
	if token == "" {
		c.Status(http.StatusOK)
		return
	}
	ctx := c.Request.Context()
	if IsAccessTokenFormat(token) {
		_, grant, cred, _ := s.store.FindOAuthAccessToken(ctx, token)
		if grant != nil {
			_ = s.store.RevokeOAuthGrant(ctx, grant.ID)
			_ = s.store.DeleteOAuthAccessTokensForGrant(ctx, grant.ID)
			if cred != nil {
				_ = s.store.DeactivateCredential(ctx, cred.ID)
				if s.revoke != nil {
					s.revoke(cred.ID, "oauth_revoke")
				}
			}
			if s.audit != nil {
				s.audit("oauth_revoke", "ok", "")
			}
		}
	} else {
		hash := store.HashOAuthToken(token)
		grant, _ := s.store.GetOAuthGrantByRefreshHash(ctx, hash)
		if grant != nil {
			_ = s.store.RevokeOAuthGrant(ctx, grant.ID)
			if s.revoke != nil {
				s.revoke(grant.CredentialID, "oauth_revoke")
			}
		}
	}
	c.Status(http.StatusOK)
}

// RevokeGrant 管理端撤销授权。
func (s *Service) RevokeGrant(ctx context.Context, grantID uint) error {
	grant, err := s.store.GetOAuthGrant(ctx, grantID)
	if err != nil || grant == nil {
		return fmt.Errorf("not found")
	}
	if err := s.store.RevokeOAuthGrant(ctx, grantID); err != nil {
		return err
	}
	_ = s.store.DeleteOAuthAccessTokensForGrant(ctx, grantID)
	_ = s.store.DeactivateCredential(ctx, grant.CredentialID)
	if s.revoke != nil {
		s.revoke(grant.CredentialID, "oauth_revoke")
	}
	if s.audit != nil {
		s.audit("oauth_revoke", "ok", "grant")
	}
	return nil
}

func (s *Service) ResolveCredential(ctx context.Context, plain string) (*model.MCPCredential, error) {
	if !IsAccessTokenFormat(plain) {
		return nil, nil
	}
	_, _, cred, err := s.store.FindOAuthAccessToken(ctx, plain)
	return cred, err
}

// Store 暴露给 api 包
func (s *Service) Store() *store.Store { return s.store }

// SavePolicy 保存 OAuth 策略并记审计。detail 只记各开关的开 / 关与「是否填了对外地址」，不写地址本身，也不含任何机密。
func (s *Service) SavePolicy(ctx context.Context, p model.OAuthPolicy) error {
	if err := s.store.SaveOAuthPolicy(ctx, p); err != nil {
		return err
	}
	// 接入窗口只在「授权服务器开着、自动注册是限时窗口」时才有意义：关掉总开关或改成别的方式，窗口一并关闭，
	// 免得以后再切回来时冒出一个早就被忘记、却还开着的窗口。
	if !p.Enabled || DCRModeOf(p) != DCRModeWindow {
		s.dcrWin.close(s.now())
	}
	if s.audit != nil {
		s.audit("oauth_policy_changed", "ok", fmt.Sprintf(
			"enabled=%t dcr=%t dcr_always=%t public_url_set=%t",
			p.Enabled, p.AllowDCR, p.DCRAlways, strings.TrimSpace(p.PublicURL) != ""))
	}
	return nil
}

// UnusedClientMaxAge：DCR 登记之后超过这么久还没走完授权，会在下一次有客户端登记时被清理。
// 设置页「刚登记、还没授权」清单只列还没到期的，免得列出一条随时会被清掉的。
const UnusedClientMaxAge = 24 * time.Hour

// clientRefPrefix 是本服务签发的 DCR client_id 里随机段之前的固定路径：client_id = <对外地址> + clientRefPrefix + <随机段>。
const clientRefPrefix = "/oauth/clients/"

// ClientRef 取 DCR client_id 里的随机段（设置页用它指认一条登记，不把完整的 client_id 放进 URL）；不是本服务签发的返回空串。
func ClientRef(clientID string) string {
	i := strings.LastIndex(clientID, clientRefPrefix)
	if i < 0 {
		return ""
	}
	return clientID[i+len(clientRefPrefix):]
}

// ListUnusedClients 返回「刚登记、还没授权」的客户端：登记之后还没走完授权（没有签发过令牌）、也没过期的 DCR 登记，新的在前。
// 已授权的应用在授权列表里；CIMD 客户端不登记，不会出现在这里。
func (s *Service) ListUnusedClients(ctx context.Context) ([]model.OAuthClient, error) {
	return s.store.ListUnusedOAuthClients(ctx, s.now().Add(-UnusedClientMaxAge))
}

// FindUnusedClientByRef 按 ClientRef 找一条还没授权过的登记（不看是否过期，过期没清的也能删）；找不到返回 nil。
func (s *Service) FindUnusedClientByRef(ctx context.Context, ref string) (*model.OAuthClient, error) {
	if ref == "" {
		return nil, nil
	}
	list, err := s.store.ListUnusedOAuthClients(ctx, time.Time{})
	if err != nil {
		return nil, err
	}
	for i := range list {
		if ClientRef(list[i].ClientID) == ref {
			return &list[i], nil
		}
	}
	return nil, nil
}

// DeleteRegisteredClient 删除一条还没授权过的登记，返回是否真的删了（不存在或已经授权过的不删，返回 false）。
// 删除之后这个 client_id 不能再发起授权；它正在进行中的授权请求（批准页还开着）和还没兑换的授权码一并作废，
// 否则管理员刚删掉的登记仍能在批准页上被批准。
func (s *Service) DeleteRegisteredClient(ctx context.Context, clientID string) (bool, error) {
	ok, err := s.store.DeleteUnusedOAuthClient(ctx, clientID)
	if err != nil || !ok {
		return false, err
	}
	s.mem.dropClient(clientID)
	// 批准了、但令牌还没兑换的授权（授权记录与隐藏凭据已经建好）也一并撤销，不留下任何还活着的授权。
	if grants, e := s.store.ListLiveOAuthGrantsByClient(ctx, clientID); e == nil {
		for _, g := range grants {
			_ = s.RevokeGrant(ctx, g.ID)
		}
	}
	return true, nil
}

func (s *Service) Config() *config.Config { return s.cfg }

func (s *Service) issuerDerived(r *http.Request) (string, bool) {
	return ResolvePublicBase(s.cfg, s.store, r)
}

func (s *Service) applyDerivedHeaders(c *gin.Context, derived bool) {
	if !derived {
		return
	}
	c.Header("Cache-Control", "no-store")
	c.Header("Pragma", "no-cache")
	c.Header("Vary", "Host, X-Forwarded-Host, X-Forwarded-Proto")
}

func (s *Service) authClientError(c *gin.Context, errCode, desc string) {
	accept := strings.ToLower(c.GetHeader("Accept"))
	if strings.Contains(accept, "application/json") {
		c.JSON(http.StatusBadRequest, gin.H{"error": errCode, "error_description": desc})
		return
	}
	body := "<!DOCTYPE html><html><body><h1>Authorization Error</h1><p>" + htmlEscape(errCode)
	if desc != "" {
		body += ": " + htmlEscape(desc)
	}
	body += "</p></body></html>"
	c.Data(http.StatusBadRequest, "text/html; charset=utf-8", []byte(body))
}

func htmlEscape(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	return s
}
