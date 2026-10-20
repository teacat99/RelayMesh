package oauth_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/teacat99/RelayMesh/v2/internal/api"
	"github.com/teacat99/RelayMesh/v2/internal/oauth"
	"github.com/teacat99/RelayMesh/v2/internal/store"
)

func TestOAuthCodePKCEFlowCIMD(t *testing.T) {
	gin.SetMode(gin.TestMode)
	oauth.ClearCIMDCache()
	oauth.SetCIMDLookupHost(func(ctx context.Context, host string) error { return nil })
	defer oauth.SetCIMDLookupHost(nil)

	clientID := "https://cimd-positive.example/client.json"
	redirectURI := "http://127.0.0.1:9876/cb"
	doc := oauth.CIMDDocument{
		ClientID: clientID, ClientName: "cimd-e2e", RedirectURIs: []string{redirectURI}, TokenAuth: "none",
	}
	rawDoc, _ := json.Marshal(doc)
	var fetches int32
	oauth.SetCIMDHTTPDoer(roundTripFunc(func(req *http.Request) (*http.Response, error) {
		atomic.AddInt32(&fetches, 1)
		if req.URL.String() != clientID {
			t.Fatalf("fetch url %s", req.URL)
		}
		return &http.Response{
			StatusCode: 200,
			Body:       io.NopCloser(bytes.NewReader(rawDoc)),
			Header:     http.Header{"Content-Type": {"application/json"}},
		}, nil
	}))
	defer oauth.SetCIMDHTTPDoer(nil)

	st, _ := store.New(":memory:")
	enableOAuthServer(t, st)
	cfg := oauthTestCfg()
	srv := api.NewServer(cfg, st, nil)
	engine := srv.Engine()
	jwt := loginJWT(t, engine, "admin", cfg.WebPassword)
	unlock := terminalUnlock(t, engine, jwt, cfg.WebPassword)

	verifier := strings.Repeat("c", 43)
	challenge := pkceS256(verifier)
	resource := "https://hub.example.test/mcp"
	authURL := "/oauth/authorize?response_type=code&client_id=" + url.QueryEscape(clientID) +
		"&redirect_uri=" + url.QueryEscape(redirectURI) +
		"&scope=exec&state=st-cimd&resource=" + url.QueryEscape(resource) +
		"&code_challenge=" + url.QueryEscape(challenge) + "&code_challenge_method=S256"
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, authURL, nil)
	req.Header.Set("X-Forwarded-Proto", "https")
	engine.ServeHTTP(w, req)
	if w.Code != 302 {
		t.Fatalf("authorize %d %s", w.Code, w.Body.String())
	}
	if atomic.LoadInt32(&fetches) < 1 {
		t.Fatal("expected CIMD fetch")
	}

	loc := w.Header().Get("Location")
	i := strings.Index(loc, "req=")
	reqID, _ := url.QueryUnescape(loc[i+4:])
	if j := strings.IndexAny(reqID, "&"); j >= 0 {
		reqID = reqID[:j]
	}
	dec, _ := json.Marshal(map[string]any{
		"approve": true, "nonce": reqID, "terminal_hosts": []string{"*"}, "terminal_mode_cap": "approve",
	})
	w2 := httptest.NewRecorder()
	req2 := httptest.NewRequest(http.MethodPost, "/api/v1/oauth/requests/"+reqID+"/decision", bytes.NewReader(dec))
	req2.Header.Set("Content-Type", "application/json")
	req2.Header.Set("Authorization", "Bearer "+jwt)
	req2.Header.Set("X-Terminal-Unlock", unlock)
	engine.ServeHTTP(w2, req2)
	if w2.Code != 200 {
		t.Fatalf("decision %d %s", w2.Code, w2.Body.String())
	}
	var decRes map[string]any
	_ = json.Unmarshal(w2.Body.Bytes(), &decRes)
	redir, _ := decRes["redirect_url"].(string)
	u, _ := url.Parse(redir)
	code := u.Query().Get("code")
	access, _ := postTokenPair(t, engine, code, clientID, redirectURI, resource, verifier, http.StatusOK)
	if !strings.HasPrefix(access, oauth.AccessTokenPrefix) {
		t.Fatalf("bad access token")
	}

	// 第二次 authorize 应命中 CIMD 缓存
	w3 := httptest.NewRecorder()
	req3 := httptest.NewRequest(http.MethodGet, authURL, nil)
	req3.Header.Set("X-Forwarded-Proto", "https")
	engine.ServeHTTP(w3, req3)
	if atomic.LoadInt32(&fetches) != 1 {
		t.Fatalf("want cached CIMD fetch count 1 got %d", fetches)
	}
}

func TestCIMDNegativeCases(t *testing.T) {
	oauth.ClearCIMDCache()
	oauth.SetCIMDLookupHost(func(ctx context.Context, host string) error { return nil })
	defer oauth.SetCIMDLookupHost(nil)

	baseURL := "https://cimd-neg.example/doc"
	makeDoer := func(body string, status int) {
		oauth.SetCIMDHTTPDoer(roundTripFunc(func(req *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: status,
				Body:       io.NopCloser(strings.NewReader(body)),
				Header:     http.Header{"Content-Type": {"application/json"}},
			}, nil
		}))
	}
	defer oauth.SetCIMDHTTPDoer(nil)

	t.Run("client_id mismatch", func(t *testing.T) {
		oauth.ClearCIMDCache()
		makeDoer(`{"client_id":"https://other/doc","redirect_uris":["http://127.0.0.1/cb"],"token_endpoint_auth_method":"none"}`, 200)
		_, err := oauth.FetchCIMD(context.Background(), baseURL)
		if err == nil {
			t.Fatal("expected error")
		}
	})
	t.Run("redirect not listed", func(t *testing.T) {
		oauth.ClearCIMDCache()
		gin.SetMode(gin.TestMode)
		st, _ := store.New(":memory:")
		enableOAuthServer(t, st)
		cfg := oauthTestCfg()
		srv := api.NewServer(cfg, st, nil)
		engine := srv.Engine()
		doc := `{"client_id":"` + baseURL + `","client_name":"n","redirect_uris":["http://127.0.0.1:1/ok"],"token_endpoint_auth_method":"none"}`
		makeDoer(doc, 200)
		authURL := "/oauth/authorize?response_type=code&client_id=" + url.QueryEscape(baseURL) +
			"&redirect_uri=" + url.QueryEscape("http://127.0.0.1:9/bad") +
			"&scope=exec&state=st&resource=https://hub.example.test/mcp&code_challenge=" + pkceS256(strings.Repeat("d", 43)) + "&code_challenge_method=S256"
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, authURL, nil)
		req.Header.Set("X-Forwarded-Proto", "https")
		engine.ServeHTTP(w, req)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("want 400 for redirect mismatch got %d loc=%s", w.Code, w.Header().Get("Location"))
		}
	})
	t.Run("non json", func(t *testing.T) {
		oauth.ClearCIMDCache()
		makeDoer("not-json", 200)
		_, err := oauth.FetchCIMD(context.Background(), baseURL)
		if err == nil {
			t.Fatal("expected error")
		}
	})
	t.Run("redirect response", func(t *testing.T) {
		oauth.ClearCIMDCache()
		oauth.SetCIMDHTTPDoer(roundTripFunc(func(req *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: 302,
				Header:     http.Header{"Location": {"http://127.0.0.1/evil"}},
				Body:       io.NopCloser(strings.NewReader("")),
			}, nil
		}))
		_, err := oauth.FetchCIMD(context.Background(), baseURL)
		if err == nil {
			t.Fatal("expected error")
		}
	})
}
