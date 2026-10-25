package oauth_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/gin-gonic/gin"
	"github.com/teacat99/RelayMesh/v2/internal/api"
	"github.com/teacat99/RelayMesh/v2/internal/model"
	"github.com/teacat99/RelayMesh/v2/internal/store"
)

func TestHiddenOAuthInternalTokenNotBearer(t *testing.T) {
	gin.SetMode(gin.TestMode)
	st, _ := store.New(":memory:")
	enableOAuthServer(t, st)
	cfg := oauthTestCfg()
	cfg.MCPToken = "rm-test-mcp-env-token-sec-o1"
	srv := api.NewServer(cfg, st, nil)
	engine := srv.Engine()

	ctx := context.Background()
	grant := &model.OAuthGrant{ClientID: "c", CredentialID: 0}
	_ = st.CreateOAuthGrant(ctx, grant)
	cred := &model.MCPCredential{Token: "rm-oauth-internal-1", IsActive: true, Permissions: model.Permissions{Terminal: true}}
	_ = st.CreateHiddenOAuthCredential(ctx, grant.ID, "hidden", cred)
	_ = st.BackfillHiddenOAuthCredentialTokens(ctx)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
	req.Header.Set("Authorization", "Bearer rm-oauth-internal-1")
	engine.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("predictable token want 401 got %d", w.Code)
	}

	got, _ := st.GetCredential(ctx, cred.ID)
	w2 := httptest.NewRecorder()
	req2 := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
	req2.Header.Set("Authorization", "Bearer "+got.Token)
	engine.ServeHTTP(w2, req2)
	if w2.Code != http.StatusUnauthorized {
		t.Fatalf("random hidden token want 401 got %d", w2.Code)
	}
}

func TestBackfillHiddenOAuthCredentialTokensIdempotent(t *testing.T) {
	st, _ := store.New(":memory:")
	ctx := context.Background()
	grant := &model.OAuthGrant{ClientID: "c"}
	_ = st.CreateOAuthGrant(ctx, grant)
	cred := &model.MCPCredential{Token: "rm-oauth-internal-99"}
	_ = st.CreateHiddenOAuthCredential(ctx, grant.ID, "h", cred)
	_ = st.BackfillHiddenOAuthCredentialTokens(ctx)
	first, _ := st.GetCredential(ctx, cred.ID)
	_ = st.BackfillHiddenOAuthCredentialTokens(ctx)
	second, _ := st.GetCredential(ctx, cred.ID)
	if first.Token != second.Token || strings.HasPrefix(first.Token, "rm-oauth-internal-") {
		t.Fatalf("backfill not idempotent: %q", first.Token)
	}
}

func TestOAuthSoftDisabled404(t *testing.T) {
	gin.SetMode(gin.TestMode)
	st, _ := store.New(":memory:")
	cfg := oauthTestCfg()
	srv := api.NewServer(cfg, st, nil)
	engine := srv.Engine()
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/.well-known/oauth-authorization-server", nil)
	engine.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("disabled oauth want 404 got %d", w.Code)
	}
}

func TestHiddenCredentialREST404(t *testing.T) {
	gin.SetMode(gin.TestMode)
	st, _ := store.New(":memory:")
	enableOAuthServer(t, st)
	cfg := oauthTestCfg()
	srv := api.NewServer(cfg, st, nil)
	engine := srv.Engine()
	jwt := loginJWT(t, engine, "admin", cfg.WebPassword)
	ctx := context.Background()
	grant := &model.OAuthGrant{ClientID: "x"}
	_ = st.CreateOAuthGrant(ctx, grant)
	cred := &model.MCPCredential{}
	_ = st.CreateHiddenOAuthCredential(ctx, grant.ID, "OAuth hidden", cred)

	paths := []struct {
		method, path string
		body         []byte
	}{
		{http.MethodPut, "/api/v1/credentials/" + itoa(cred.ID), []byte(`{"note":"x"}`)},
		{http.MethodDelete, "/api/v1/credentials/" + itoa(cred.ID), nil},
		{http.MethodPost, "/api/v1/credentials/" + itoa(cred.ID) + "/regenerate", nil},
		{http.MethodPost, "/api/v1/credentials/" + itoa(cred.ID) + "/reveal", []byte(`{"password":"` + cfg.WebPassword + `"}`)},
	}
	for _, tc := range paths {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(tc.method, tc.path, bytes.NewReader(tc.body))
		req.Header.Set("Authorization", "Bearer "+jwt)
		if tc.body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		engine.ServeHTTP(w, req)
		if w.Code != http.StatusNotFound {
			t.Fatalf("%s %s want 404 got %d", tc.method, tc.path, w.Code)
		}
	}
}

func itoa(id uint) string {
	return strconv.FormatUint(uint64(id), 10)
}

// 关闭总开关 = 立刻断开：已签发的访问令牌失效，重新开启后恢复（授权记录与隐藏凭据保留）。
func TestOAuthSoftSwitchOffRejectsIssuedAccessTokens(t *testing.T) {
	gin.SetMode(gin.TestMode)
	st, _ := store.New(":memory:")
	enableOAuthServer(t, st)
	cfg := oauthTestCfg()
	srv := api.NewServer(cfg, st, nil)
	engine := srv.Engine()
	jwt := loginJWT(t, engine, "admin", cfg.WebPassword)
	unlock := terminalUnlock(t, engine, jwt, cfg.WebPassword)
	clientID, redirectURI, resource, verifier, challenge := registerTestClient(t, engine)
	code := approveFlow(t, engine, jwt, unlock, clientID, redirectURI, resource, challenge, verifier)
	access, _ := postTokenPair(t, engine, code, clientID, redirectURI, resource, verifier, http.StatusOK)

	call := func() int {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`))
		req.Header.Set("Authorization", "Bearer "+access)
		engine.ServeHTTP(w, req)
		return w.Code
	}
	if got := call(); got != http.StatusOK {
		t.Fatalf("开关开启时访问令牌应可用，got %d", got)
	}

	ctx := context.Background()
	p, _ := st.GetOAuthPolicy(ctx)
	p.Enabled = false
	if err := st.SaveOAuthPolicy(ctx, p); err != nil {
		t.Fatal(err)
	}
	if got := call(); got != http.StatusUnauthorized {
		t.Fatalf("关闭总开关后已签发的访问令牌应立即失效，got %d", got)
	}

	p.Enabled = true
	if err := st.SaveOAuthPolicy(ctx, p); err != nil {
		t.Fatal(err)
	}
	if got := call(); got != http.StatusOK {
		t.Fatalf("重新开启后授权应恢复，got %d", got)
	}
}

// 策略（总开关 / 对外地址 / DCR）与签发令牌同级：保存要终端解锁；读取不要。
func TestOAuthPolicyPutRequiresUnlock(t *testing.T) {
	gin.SetMode(gin.TestMode)
	st, _ := store.New(":memory:")
	enableOAuthServer(t, st)
	cfg := oauthTestCfg()
	srv := api.NewServer(cfg, st, nil)
	engine := srv.Engine()
	jwt := loginJWT(t, engine, "admin", cfg.WebPassword)
	unlock := terminalUnlock(t, engine, jwt, cfg.WebPassword)

	get := httptest.NewRecorder()
	greq := httptest.NewRequest(http.MethodGet, "/api/v1/oauth/policy", nil)
	greq.Header.Set("Authorization", "Bearer "+jwt)
	engine.ServeHTTP(get, greq)
	if get.Code != http.StatusOK {
		t.Fatalf("读取策略不需要解锁，got %d %s", get.Code, get.Body.String())
	}
	var cur struct {
		Policy map[string]any `json:"policy"`
	}
	_ = json.Unmarshal(get.Body.Bytes(), &cur)
	body, _ := json.Marshal(map[string]any{"policy": cur.Policy})

	put := func(withUnlock bool) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPut, "/api/v1/oauth/policy", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+jwt)
		if withUnlock {
			req.Header.Set("X-Terminal-Unlock", unlock)
		}
		engine.ServeHTTP(w, req)
		return w
	}
	if w := put(false); w.Code == http.StatusOK || !strings.Contains(w.Body.String(), "unlock") {
		t.Fatalf("未解锁保存策略应被拒绝并提示解锁，got %d %s", w.Code, w.Body.String())
	}
	if w := put(true); w.Code != http.StatusOK {
		t.Fatalf("解锁后保存策略应成功，got %d %s", w.Code, w.Body.String())
	}
}

// 拒绝授权只会减少授权，不需要解锁（前端的「拒绝」也不会去解锁）；批准必须解锁。
func TestOAuthDenyDoesNotNeedUnlock(t *testing.T) {
	gin.SetMode(gin.TestMode)
	st, _ := store.New(":memory:")
	enableOAuthServer(t, st)
	cfg := oauthTestCfg()
	srv := api.NewServer(cfg, st, nil)
	engine := srv.Engine()
	jwt := loginJWT(t, engine, "admin", cfg.WebPassword)
	clientID, redirectURI, resource, _, challenge := registerTestClient(t, engine)

	decide := func(reqID string, approve bool) *httptest.ResponseRecorder {
		dec, _ := json.Marshal(map[string]any{"approve": approve, "nonce": reqID})
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/v1/oauth/requests/"+reqID+"/decision", bytes.NewReader(dec))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+jwt)
		engine.ServeHTTP(w, req)
		return w
	}

	reqID := startPendingAuthorize(t, engine, clientID, redirectURI, resource, challenge)
	if w := decide(reqID, true); w.Code == http.StatusOK {
		t.Fatalf("未解锁就批准应被拒绝，got %d %s", w.Code, w.Body.String())
	}
	w := decide(reqID, false)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "access_denied") {
		t.Fatalf("拒绝授权不需要解锁，got %d %s", w.Code, w.Body.String())
	}
}

// /hooks/ 下没匹配到的路径（含 %2f 穿越写法）不能落进 SPA 兜底页返回 200 text/html。
func TestUnknownHookPathsAre404JSON(t *testing.T) {
	gin.SetMode(gin.TestMode)
	st, _ := store.New(":memory:")
	cfg := oauthTestCfg()
	spa := fstest.MapFS{"index.html": &fstest.MapFile{Data: []byte("<html>spa</html>")}}
	engine := api.NewServer(cfg, st, spa).Engine()
	for _, path := range []string{
		"/hooks/cursor/bin/..%2f..%2fetc%2fpasswd",
		"/hooks/cursor/bin/%2e%2e/%2e%2e/etc/passwd",
		"/hooks/cursor/unknown",
		"/hooks/",
	} {
		w := httptest.NewRecorder()
		engine.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
		if w.Code != http.StatusNotFound || strings.Contains(w.Header().Get("Content-Type"), "text/html") {
			t.Fatalf("%s 应为 404 JSON，got %d %s", path, w.Code, w.Header().Get("Content-Type"))
		}
	}
	// SPA 兜底仍然对普通前端路由有效
	w := httptest.NewRecorder()
	engine.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/some/frontend/route", nil))
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "spa") {
		t.Fatalf("SPA 兜底应保持有效，got %d", w.Code)
	}
}

// 总开关是运行时在设置页改的：默认关闭的 Hub 在设置页打开之后，授权页用到的接口必须立刻可用，不能等重启。
// 回归：路由曾在启动时按开关注册一次，运行中打开后授权页的 GET /oauth/requests/:id 一直 404；
// 其余测试都是「先开开关、再建服务」，覆盖不到这一条。
func TestOAuthConsentRoutesFollowRuntimeSwitch(t *testing.T) {
	gin.SetMode(gin.TestMode)
	st, _ := store.New(":memory:")
	cfg := oauthTestCfg()
	srv := api.NewServer(cfg, st, nil) // 启动时总开关是关的（默认）
	engine := srv.Engine()
	jwt := loginJWT(t, engine, "admin", cfg.WebPassword)
	unlock := terminalUnlock(t, engine, jwt, cfg.WebPassword)

	call := func(method, path string, body []byte) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(method, path, bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+jwt)
		req.Header.Set("X-Terminal-Unlock", unlock)
		engine.ServeHTTP(w, req)
		return w
	}
	setEnabled := func(enabled bool) {
		t.Helper()
		var cur struct {
			Policy map[string]any `json:"policy"`
		}
		_ = json.Unmarshal(call(http.MethodGet, "/api/v1/oauth/policy", nil).Body.Bytes(), &cur)
		cur.Policy["enabled"] = enabled
		b, _ := json.Marshal(map[string]any{"policy": cur.Policy})
		if w := call(http.MethodPut, "/api/v1/oauth/policy", b); w.Code != http.StatusOK {
			t.Fatalf("保存策略 enabled=%v: %d %s", enabled, w.Code, w.Body.String())
		}
	}
	// 授权页的接口「不存在」是路由层面的 404；「存在但没有这条请求」是业务层面的 404，文案里带 expired。
	routeMissing := func(w *httptest.ResponseRecorder) bool {
		return w.Code == http.StatusNotFound && !strings.Contains(w.Body.String(), "expired")
	}

	if w := call(http.MethodGet, "/api/v1/oauth/requests/nope", nil); !routeMissing(w) {
		t.Fatalf("总开关关闭时授权页接口应不存在，got %d %s", w.Code, w.Body.String())
	}
	if w := call(http.MethodPost, "/api/v1/oauth/requests/nope/decision", []byte(`{"approve":false,"nonce":"nope"}`)); !routeMissing(w) {
		t.Fatalf("总开关关闭时决策接口应不存在，got %d %s", w.Code, w.Body.String())
	}
	if w := call(http.MethodGet, "/api/v1/oauth/grants", nil); w.Code != http.StatusOK {
		t.Fatalf("授权记录不受总开关限制（关掉之后还要能撤销），got %d %s", w.Code, w.Body.String())
	}

	// 同一个服务实例，运行中打开：授权全流程立刻可用。
	setEnabled(true)
	if w := call(http.MethodGet, "/api/v1/oauth/requests/nope", nil); w.Code != http.StatusNotFound || !strings.Contains(w.Body.String(), "expired") {
		t.Fatalf("打开后接口应存在（业务层 404），got %d %s", w.Code, w.Body.String())
	}
	// 默认的自动注册方式是限时窗口（#239）：登记之前先开一个接入窗口，和用户在设置里做的一样。
	if w := call(http.MethodPost, "/api/v1/oauth/window", []byte(`{}`)); w.Code != http.StatusOK {
		t.Fatalf("运行中打开总开关后应能开接入窗口，got %d %s", w.Code, w.Body.String())
	}
	clientID, redirectURI, resource, verifier, challenge := registerTestClient(t, engine)
	if code := approveFlow(t, engine, jwt, unlock, clientID, redirectURI, resource, challenge, verifier); code == "" {
		t.Fatal("运行中打开总开关后应能走完授权")
	}

	setEnabled(false)
	if w := call(http.MethodGet, "/api/v1/oauth/requests/nope", nil); !routeMissing(w) {
		t.Fatalf("再次关闭后授权页接口应又不存在，got %d %s", w.Code, w.Body.String())
	}
}
