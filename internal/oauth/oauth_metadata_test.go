package oauth_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/teacat99/RelayMesh/v2/internal/api"
	"github.com/teacat99/RelayMesh/v2/internal/config"
	"github.com/teacat99/RelayMesh/v2/internal/store"
)

func TestOAuthWellKnownPathsAndWWWAuthenticate(t *testing.T) {
	gin.SetMode(gin.TestMode)
	st, _ := store.New(":memory:")
	p, _ := st.GetOAuthPolicy(t.Context())
	p.Enabled = true
	_ = st.SaveOAuthPolicy(t.Context(), p)
	cfg := config.LoadWithMode("stdio")
	cfg.OAuthOff = false
	cfg.PublicURL = "https://hub.example.test"
	cfg.MCPToken = "rm-test-oauth-metadata-401-only"
	srv := api.NewServer(cfg, st, nil)
	engine := srv.Engine()
	issuer := "https://hub.example.test"
	resource := issuer + "/mcp"
	metaURL := issuer + "/.well-known/oauth-protected-resource/mcp"

	paths := []string{
		"/.well-known/oauth-protected-resource",
		"/.well-known/oauth-protected-resource/mcp",
	}
	for _, path := range paths {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, path, nil)
		engine.ServeHTTP(w, req)
		if w.Code != 200 {
			t.Fatalf("%s status %d", path, w.Code)
		}
		var j map[string]any
		_ = json.Unmarshal(w.Body.Bytes(), &j)
		if j["resource"] != resource {
			t.Fatalf("%s resource=%v", path, j["resource"])
		}
		servers, _ := j["authorization_servers"].([]any)
		if len(servers) != 1 || servers[0] != issuer {
			t.Fatalf("%s authorization_servers=%v", path, j["authorization_servers"])
		}
	}

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/.well-known/oauth-authorization-server", nil)
	engine.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("auth server metadata %d", w.Code)
	}
	var authMeta map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &authMeta)
	if authMeta["issuer"] != issuer {
		t.Fatalf("issuer=%v", authMeta["issuer"])
	}

	wOIDC := httptest.NewRecorder()
	reqOIDC := httptest.NewRequest(http.MethodGet, "/.well-known/openid-configuration", nil)
	engine.ServeHTTP(wOIDC, reqOIDC)
	if wOIDC.Code != 404 {
		t.Fatalf("openid-configuration want 404 got %d %s", wOIDC.Code, wOIDC.Body.String())
	}
	if !strings.Contains(wOIDC.Body.String(), "not_found") {
		t.Fatalf("openid-configuration want json not_found got %s", wOIDC.Body.String())
	}
	if strings.Contains(wOIDC.Header().Get("Content-Type"), "text/html") {
		t.Fatal("openid-configuration must not be SPA")
	}

	wMCP := httptest.NewRecorder()
	reqMCP := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
	engine.ServeHTTP(wMCP, reqMCP)
	www := wMCP.Header().Get("WWW-Authenticate")
	if !strings.Contains(www, metaURL) {
		t.Fatalf("WWW-Authenticate missing metadata url: %s", www)
	}
	wMeta := httptest.NewRecorder()
	reqMeta := httptest.NewRequest(http.MethodGet, "/.well-known/oauth-protected-resource/mcp", nil)
	engine.ServeHTTP(wMeta, reqMeta)
	if wMeta.Code != 200 {
		t.Fatalf("resource metadata unreachable %d", wMeta.Code)
	}
}

func TestOAuthTokenResponseNoStore(t *testing.T) {
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
	w := httptest.NewRecorder()
	form := "grant_type=authorization_code&code=" + code + "&redirect_uri=" + redirectURI + "&client_id=" + clientID + "&code_verifier=" + verifier + "&resource=" + resource
	req := httptest.NewRequest(http.MethodPost, "/oauth/token", strings.NewReader(form))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	engine.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("token %d", w.Code)
	}
	if cc := w.Header().Get("Cache-Control"); !strings.Contains(cc, "no-store") {
		t.Fatalf("Cache-Control=%q", cc)
	}
	if w.Header().Get("Pragma") != "no-cache" {
		t.Fatalf("Pragma=%q", w.Header().Get("Pragma"))
	}
}

func TestOAuthAuthorizeFrameDeny(t *testing.T) {
	gin.SetMode(gin.TestMode)
	st, _ := store.New(":memory:")
	enableOAuthServer(t, st)
	cfg := oauthTestCfg()
	srv := api.NewServer(cfg, st, nil)
	engine := srv.Engine()
	clientID, redirectURI, resource, _, challenge := registerTestClient(t, engine)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, buildAuthorizeURL(clientID, redirectURI, resource, challenge, "st"), nil)
	req.Header.Set("X-Forwarded-Proto", "https")
	engine.ServeHTTP(w, req)
	if w.Header().Get("X-Frame-Options") != "DENY" {
		t.Fatalf("X-Frame-Options=%q", w.Header().Get("X-Frame-Options"))
	}
	if !strings.Contains(w.Header().Get("Content-Security-Policy"), "frame-ancestors") {
		t.Fatalf("CSP=%q", w.Header().Get("Content-Security-Policy"))
	}
}

func TestMCPRejectsOAuthTokenWrongResourceAudience(t *testing.T) {
	gin.SetMode(gin.TestMode)
	st, _ := store.New(":memory:")
	enableOAuthServer(t, st)
	cfg := oauthTestCfg()
	cfg.PublicURL = "https://hub-a.example.test"
	srv := api.NewServer(cfg, st, nil)
	engine := srv.Engine()
	jwt := loginJWT(t, engine, "admin", cfg.WebPassword)
	unlock := terminalUnlock(t, engine, jwt, cfg.WebPassword)
	clientID, redirectURI, _, verifier, challenge := registerTestClient(t, engine)
	resource := "https://hub-a.example.test/mcp"
	code := approveFlow(t, engine, jwt, unlock, clientID, redirectURI, resource, challenge, verifier)
	access, _ := postTokenPair(t, engine, code, clientID, redirectURI, resource, verifier, http.StatusOK)

	cfg2 := config.LoadWithMode("stdio")
	cfg2.OAuthOff = false
	cfg2.PublicURL = "https://hub-b.example.test"
	cfg2.MCPToken = cfg.MCPToken
	srv2 := api.NewServer(cfg2, st, nil)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
	req.Header.Set("Authorization", "Bearer "+access)
	req.Header.Set("X-Forwarded-Proto", "https")
	srv2.Engine().ServeHTTP(w, req)
	if w.Code != 401 {
		t.Fatalf("want audience mismatch 401 got %d %s", w.Code, w.Body.String())
	}
}
