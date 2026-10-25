package oauth_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/teacat99/RelayMesh/v2/internal/api"
	"github.com/teacat99/RelayMesh/v2/internal/config"
	"github.com/teacat99/RelayMesh/v2/internal/oauth"
	"github.com/teacat99/RelayMesh/v2/internal/store"
)

func TestOAuthMetadataAndMCP401(t *testing.T) {
	gin.SetMode(gin.TestMode)
	st, _ := store.New(":memory:")
	enableOAuthServer(t, st)
	cfg := config.LoadWithMode("stdio")
	cfg.OAuthOff = false
	cfg.PublicURL = "https://hub.example.test"
	cfg.MCPToken = "rm-test-token-for-oauth-401-check-only"
	srv := api.NewServer(cfg, st, nil)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/.well-known/oauth-authorization-server", nil)
	srv.Engine().ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("metadata status %d", w.Code)
	}
	var meta map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &meta)
	if meta["issuer"] != "https://hub.example.test" {
		t.Fatalf("issuer=%v", meta["issuer"])
	}

	w2 := httptest.NewRecorder()
	req2 := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
	srv.Engine().ServeHTTP(w2, req2)
	if w2.Code != 401 {
		t.Fatalf("mcp status %d", w2.Code)
	}
	if !strings.Contains(w2.Header().Get("WWW-Authenticate"), "resource_metadata=") {
		t.Fatalf("missing WWW-Authenticate: %s", w2.Header().Get("WWW-Authenticate"))
	}
}

func TestOAuthCodePKCEFlowDCR(t *testing.T) {
	gin.SetMode(gin.TestMode)
	st, _ := store.New(":memory:")
	enableOAuthServer(t, st)
	cfg := config.LoadWithMode("stdio")
	cfg.OAuthOff = false
	cfg.PublicURL = "https://hub.example.test"
	cfg.WebPassword = "oauth-test-pass"
	cfg.JWTSecret = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789ab"
	srv := api.NewServer(cfg, st, nil)
	engine := srv.Engine()

	jwt := loginJWT(t, engine, "admin", "oauth-test-pass")
	unlockToken := terminalUnlock(t, engine, jwt, "oauth-test-pass")

	regBody := `{"client_name":"e2e","redirect_uris":["http://127.0.0.1:9999/cb"],"grant_types":["authorization_code","refresh_token"],"response_types":["code"]}`
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/oauth/register", strings.NewReader(regBody))
	req.Header.Set("Content-Type", "application/json")
	engine.ServeHTTP(w, req)
	if w.Code != 201 {
		t.Fatalf("register %d %s", w.Code, w.Body.String())
	}
	var reg map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &reg)
	clientID, _ := reg["client_id"].(string)

	verifier := strings.Repeat("a", 43)
	challenge := pkceS256(verifier)
	resource := "https://hub.example.test/mcp"
	authURL := "/oauth/authorize?response_type=code&client_id=" + url.QueryEscape(clientID) +
		"&redirect_uri=" + url.QueryEscape("http://127.0.0.1:9999/cb") +
		"&scope=exec&state=st1&resource=" + url.QueryEscape(resource) +
		"&code_challenge=" + url.QueryEscape(challenge) + "&code_challenge_method=S256"
	w2 := httptest.NewRecorder()
	req2 := httptest.NewRequest(http.MethodGet, authURL, nil)
	req2.Header.Set("X-Forwarded-Proto", "https")
	engine.ServeHTTP(w2, req2)
	if w2.Code != 302 {
		t.Fatalf("authorize %d %s", w2.Code, w2.Body.String())
	}
	loc := w2.Header().Get("Location")
	i := strings.Index(loc, "req=")
	if i < 0 {
		t.Fatalf("unexpected location %s", loc)
	}
	reqID, _ := url.QueryUnescape(loc[i+4:])
	if j := strings.IndexAny(reqID, "&"); j >= 0 {
		reqID = reqID[:j]
	}

	dec, _ := json.Marshal(map[string]any{
		"approve": true, "nonce": reqID, "terminal_hosts": []string{"*"}, "terminal_mode_cap": "approve",
	})
	w3 := httptest.NewRecorder()
	req3 := httptest.NewRequest(http.MethodPost, "/api/v1/oauth/requests/"+reqID+"/decision", bytes.NewReader(dec))
	req3.Header.Set("Content-Type", "application/json")
	req3.Header.Set("Authorization", "Bearer "+jwt)
	req3.Header.Set("X-Terminal-Unlock", unlockToken)
	engine.ServeHTTP(w3, req3)
	if w3.Code != 200 {
		t.Fatalf("decision %d %s", w3.Code, w3.Body.String())
	}
	var decRes map[string]any
	_ = json.Unmarshal(w3.Body.Bytes(), &decRes)
	redir, _ := decRes["redirect_url"].(string)
	u, _ := url.Parse(redir)
	code := u.Query().Get("code")
	if code == "" {
		t.Fatalf("no code in redirect")
	}

	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("code", code)
	form.Set("redirect_uri", "http://127.0.0.1:9999/cb")
	form.Set("client_id", clientID)
	form.Set("code_verifier", verifier)
	form.Set("resource", resource)
	w4 := httptest.NewRecorder()
	req4 := httptest.NewRequest(http.MethodPost, "/oauth/token", strings.NewReader(form.Encode()))
	req4.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	engine.ServeHTTP(w4, req4)
	if w4.Code != 200 {
		t.Fatalf("token %d %s", w4.Code, w4.Body.String())
	}
	var tok map[string]any
	_ = json.Unmarshal(w4.Body.Bytes(), &tok)
	access, _ := tok["access_token"].(string)
	if !strings.HasPrefix(access, oauth.AccessTokenPrefix) {
		t.Fatalf("bad access token")
	}

	w5 := httptest.NewRecorder()
	req5 := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`))
	req5.Header.Set("Authorization", "Bearer "+access)
	engine.ServeHTTP(w5, req5)
	if w5.Code != 200 {
		t.Fatalf("mcp %d %s", w5.Code, w5.Body.String())
	}
}

func TestCIMDRejectsWhenFetcherBlocked(t *testing.T) {
	oauth.SetCIMDHTTPDoer(roundTripFunc(func(req *http.Request) (*http.Response, error) {
		body := io.NopCloser(strings.NewReader(`{"client_id":"https://127.0.0.1/meta","redirect_uris":["http://127.0.0.1:1/cb"]}`))
		return &http.Response{StatusCode: 200, Body: body, Header: http.Header{"Content-Type": {"application/json"}}}, nil
	}))
	defer oauth.SetCIMDHTTPDoer(nil)
	_, err := oauth.FetchCIMD(context.Background(), "https://127.0.0.1/meta")
	if err == nil {
		t.Fatal("expected error for loopback cimd url")
	}
}

func pkceS256(verifier string) string {
	h := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(h[:])
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) Do(req *http.Request) (*http.Response, error) { return f(req) }

func loginJWT(t *testing.T, engine http.Handler, user, pass string) string {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"username": user, "password": pass})
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	engine.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("login %d %s", w.Code, w.Body.String())
	}
	var res map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &res)
	tok, _ := res["token"].(string)
	return tok
}

func terminalUnlock(t *testing.T, engine http.Handler, jwt, pass string) string {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"password": pass})
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/terminal/unlock", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+jwt)
	engine.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("unlock %d %s", w.Code, w.Body.String())
	}
	var res map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &res)
	tok, _ := res["token"].(string)
	return tok
}

func oauthTestCfg() *config.Config {
	cfg := config.LoadWithMode("stdio")
	cfg.OAuthOff = false
	cfg.PublicURL = "https://hub.example.test"
	cfg.WebPassword = "oauth-test-pass"
	cfg.JWTSecret = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789ab"
	return cfg
}

// enableOAuthServer 打开授权服务器，并把自动注册设成「常开」：这些测试关心的是授权流程本身，
// 不想每个都先开一次接入窗口（窗口的行为见 dcr_window_flow_test.go）。
func enableOAuthServer(t *testing.T, st *store.Store) {
	t.Helper()
	p, _ := st.GetOAuthPolicy(context.Background())
	p.Enabled = true
	p.DCRAlways = true
	if err := st.SaveOAuthPolicy(context.Background(), p); err != nil {
		t.Fatal(err)
	}
}

func registerTestClient(t *testing.T, engine http.Handler) (clientID, redirectURI, resource, verifier, challenge string) {
	t.Helper()
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	name := "neg-test-" + hex.EncodeToString(b)
	regBody := `{"client_name":"` + name + `","redirect_uris":["http://127.0.0.1:9999/cb"],"grant_types":["authorization_code","refresh_token"],"response_types":["code"]}`
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/oauth/register", strings.NewReader(regBody))
	req.Header.Set("Content-Type", "application/json")
	engine.ServeHTTP(w, req)
	if w.Code != 201 {
		t.Fatalf("register %d %s", w.Code, w.Body.String())
	}
	var reg map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &reg)
	clientID, _ = reg["client_id"].(string)
	redirectURI = "http://127.0.0.1:9999/cb"
	resource = "https://hub.example.test/mcp"
	verifier = strings.Repeat("b", 43)
	challenge = pkceS256(verifier)
	return clientID, redirectURI, resource, verifier, challenge
}

func buildAuthorizeURL(clientID, redirectURI, resource, challenge, state string) string {
	return "/oauth/authorize?response_type=code&client_id=" + url.QueryEscape(clientID) +
		"&redirect_uri=" + url.QueryEscape(redirectURI) +
		"&scope=exec&state=" + url.QueryEscape(state) + "&resource=" + url.QueryEscape(resource) +
		"&code_challenge=" + url.QueryEscape(challenge) + "&code_challenge_method=S256"
}

func startPendingAuthorize(t *testing.T, engine http.Handler, clientID, redirectURI, resource, challenge string) string {
	t.Helper()
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, buildAuthorizeURL(clientID, redirectURI, resource, challenge, "st"), nil)
	req.Header.Set("X-Forwarded-Proto", "https")
	engine.ServeHTTP(w, req)
	if w.Code != http.StatusFound {
		t.Fatalf("authorize %d", w.Code)
	}
	loc := w.Header().Get("Location")
	i := strings.Index(loc, "req=")
	if i < 0 {
		t.Fatalf("location %s", loc)
	}
	reqID, _ := url.QueryUnescape(loc[i+4:])
	if j := strings.IndexAny(reqID, "&"); j >= 0 {
		reqID = reqID[:j]
	}
	return reqID
}

func approveFlow(t *testing.T, engine http.Handler, jwt, unlock, clientID, redirectURI, resource, challenge, verifier string) string {
	t.Helper()
	_ = verifier
	reqID := startPendingAuthorize(t, engine, clientID, redirectURI, resource, challenge)
	dec, _ := json.Marshal(map[string]any{
		"approve": true, "nonce": reqID, "terminal_hosts": []string{"*"}, "terminal_mode_cap": "approve",
	})
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/oauth/requests/"+reqID+"/decision", bytes.NewReader(dec))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+jwt)
	req.Header.Set("X-Terminal-Unlock", unlock)
	engine.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("decision %d %s", w.Code, w.Body.String())
	}
	var decRes map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &decRes)
	redir, _ := decRes["redirect_url"].(string)
	u, _ := url.Parse(redir)
	code := u.Query().Get("code")
	if code == "" {
		t.Fatalf("no code")
	}
	return code
}

func postToken(t *testing.T, engine http.Handler, code, clientID, redirectURI, resource, verifier string, want int) string {
	t.Helper()
	_, ref := postTokenPair(t, engine, code, clientID, redirectURI, resource, verifier, want)
	return ref
}

func postTokenPair(t *testing.T, engine http.Handler, code, clientID, redirectURI, resource, verifier string, want int) (access, refresh string) {
	t.Helper()
	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("code", code)
	form.Set("redirect_uri", redirectURI)
	form.Set("client_id", clientID)
	form.Set("code_verifier", verifier)
	form.Set("resource", resource)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/oauth/token", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	engine.ServeHTTP(w, req)
	if w.Code != want {
		t.Fatalf("token want %d got %d %s", want, w.Code, w.Body.String())
	}
	if want != http.StatusOK {
		return "", ""
	}
	var tok map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &tok)
	access, _ = tok["access_token"].(string)
	refresh, _ = tok["refresh_token"].(string)
	return access, refresh
}

func postRefresh(t *testing.T, engine http.Handler, refresh, clientID, resource string, want int) string {
	t.Helper()
	form := url.Values{}
	form.Set("grant_type", "refresh_token")
	form.Set("refresh_token", refresh)
	form.Set("client_id", clientID)
	form.Set("resource", resource)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/oauth/token", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	engine.ServeHTTP(w, req)
	if w.Code != want {
		t.Fatalf("refresh want %d got %d %s", want, w.Code, w.Body.String())
	}
	if want != http.StatusOK {
		return ""
	}
	var tok map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &tok)
	ref, _ := tok["refresh_token"].(string)
	return ref
}
