package oauth_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/teacat99/RelayMesh/v2/internal/api"
	"github.com/teacat99/RelayMesh/v2/internal/config"
	"github.com/teacat99/RelayMesh/v2/internal/oauth"
	"github.com/teacat99/RelayMesh/v2/internal/store"
)

func TestOAuthDisabled(t *testing.T) {
	gin.SetMode(gin.TestMode)
	st, _ := store.New(":memory:")
	cfg := config.LoadWithMode("stdio")
	cfg.OAuthOff = true
	cfg.MCPToken = "rm-test-mcp-token-oauth-off"
	srv := api.NewServer(cfg, st, nil)
	engine := srv.Engine()

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/.well-known/oauth-authorization-server", nil)
	engine.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("metadata want 404 got %d", w.Code)
	}

	w2 := httptest.NewRecorder()
	req2 := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
	engine.ServeHTTP(w2, req2)
	if w2.Code != http.StatusUnauthorized {
		t.Fatalf("mcp want 401 got %d", w2.Code)
	}
	if w2.Header().Get("WWW-Authenticate") != "" {
		t.Fatal("WWW-Authenticate should be absent when OAuth off")
	}
}

func TestOAuthNegativeCases(t *testing.T) {
	gin.SetMode(gin.TestMode)
	st, _ := store.New(":memory:")
	enableOAuthServer(t, st)
	cfg := oauthTestCfg()
	srv := api.NewServer(cfg, st, nil)
	engine := srv.Engine()
	jwt := loginJWT(t, engine, "admin", cfg.WebPassword)
	unlock := terminalUnlock(t, engine, jwt, cfg.WebPassword)
	clientID, redirectURI, resource, verifier, challenge := registerTestClient(t, engine)

	t.Run("missing_state", func(t *testing.T) {
		authURL := "/oauth/authorize?response_type=code&client_id=" + clientID +
			"&redirect_uri=" + redirectURI +
			"&scope=exec&code_challenge=" + challenge + "&code_challenge_method=S256"
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, authURL, nil)
		req.Header.Set("X-Forwarded-Proto", "https")
		engine.ServeHTTP(w, req)
		if w.Code != http.StatusFound && w.Code != http.StatusBadRequest {
			t.Fatalf("status %d", w.Code)
		}
	})

	t.Run("wrong_redirect_uri", func(t *testing.T) {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, buildAuthorizeURL(clientID, "http://127.0.0.1:1/evil", resource, challenge, "st"), nil)
		req.Header.Set("X-Forwarded-Proto", "https")
		engine.ServeHTTP(w, req)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("status %d body %s", w.Code, w.Body.String())
		}
		if strings.Contains(w.Header().Get("Location"), "127.0.0.1:1/evil") {
			t.Fatal("must not redirect to unregistered redirect_uri")
		}
	})

	t.Run("non_S256", func(t *testing.T) {
		authURL := "/oauth/authorize?response_type=code&client_id=" + clientID +
			"&redirect_uri=" + redirectURI +
			"&scope=exec&state=st&resource=" + resource +
			"&code_challenge=plain&code_challenge_method=plain"
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, authURL, nil)
		req.Header.Set("X-Forwarded-Proto", "https")
		engine.ServeHTTP(w, req)
		if !strings.Contains(w.Header().Get("Location"), "error=") {
			t.Fatal("expected error in redirect")
		}
	})

	code := approveFlow(t, engine, jwt, unlock, clientID, redirectURI, resource, challenge, verifier)

	t.Run("wrong_code_verifier", func(t *testing.T) {
		code2 := approveFlow(t, engine, jwt, unlock, clientID, redirectURI, resource, challenge, verifier)
		postToken(t, engine, code2, clientID, redirectURI, resource, "wrong-verifier-xxxxxxxxxxxxxxxxxxxxxxx", http.StatusBadRequest)
	})

	t.Run("code_replay", func(t *testing.T) {
		postToken(t, engine, code, clientID, redirectURI, resource, verifier, http.StatusOK)
		postToken(t, engine, code, clientID, redirectURI, resource, verifier, http.StatusBadRequest)
	})

	t.Run("wrong_resource", func(t *testing.T) {
		code3 := approveFlow(t, engine, jwt, unlock, clientID, redirectURI, resource, challenge, verifier)
		postToken(t, engine, code3, clientID, redirectURI, "https://evil.example/mcp", verifier, http.StatusBadRequest)
	})

	t.Run("decision_without_jwt", func(t *testing.T) {
		reqID := startPendingAuthorize(t, engine, clientID, redirectURI, resource, challenge)
		body, _ := json.Marshal(map[string]any{"approve": true, "nonce": reqID, "terminal_hosts": []string{"*"}})
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/v1/oauth/requests/"+reqID+"/decision", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Terminal-Unlock", unlock)
		engine.ServeHTTP(w, req)
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("want 401 got %d", w.Code)
		}
	})

	t.Run("decision_without_unlock", func(t *testing.T) {
		reqID := startPendingAuthorize(t, engine, clientID, redirectURI, resource, challenge)
		body, _ := json.Marshal(map[string]any{"approve": true, "nonce": reqID, "terminal_hosts": []string{"*"}})
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/v1/oauth/requests/"+reqID+"/decision", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+jwt)
		engine.ServeHTTP(w, req)
		if w.Code != http.StatusForbidden {
			t.Fatalf("want 403 got %d", w.Code)
		}
	})
}

func TestCIMDRejectsOversizedBody(t *testing.T) {
	oauth.SetCIMDHTTPDoer(roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: 200,
			Body:       io.NopCloser(strings.NewReader(strings.Repeat("a", 64<<10+2))),
			Header:     http.Header{"Content-Type": {"application/json"}},
		}, nil
	}))
	defer oauth.SetCIMDHTTPDoer(nil)
	_, err := oauth.FetchCIMD(context.Background(), "https://example.com/oauth-client.json")
	if err == nil {
		t.Fatal("expected oversize rejection")
	}
}
