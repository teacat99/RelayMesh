package oauth_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/teacat99/RelayMesh/v2/internal/api"
	"github.com/teacat99/RelayMesh/v2/internal/oauth"
	"github.com/teacat99/RelayMesh/v2/internal/store"
)

func TestOAuthRefreshTable(t *testing.T) {
	gin.SetMode(gin.TestMode)
	t0 := time.Now()
	oauth.SetServiceClock(func() time.Time { return t0 })
	defer oauth.SetServiceClock(nil)

	st, _ := store.New(":memory:")
	enableOAuthServer(t, st)
	cfg := oauthTestCfg()
	srv := api.NewServer(cfg, st, nil)
	engine := srv.Engine()
	jwt := loginJWT(t, engine, "admin", cfg.WebPassword)
	unlock := terminalUnlock(t, engine, jwt, cfg.WebPassword)
	clientID, redirectURI, resource, verifier, challenge := registerTestClient(t, engine)

	mk := func() (access, refresh string) {
		code := approveFlow(t, engine, jwt, unlock, clientID, redirectURI, resource, challenge, verifier)
		return postTokenPair(t, engine, code, clientID, redirectURI, resource, verifier, http.StatusOK)
	}

	t.Run("grace_reuse_old_refresh", func(t *testing.T) {
		oauth.SetServiceClock(func() time.Time { return t0 })
		_, ref0 := mk()
		ref1 := postRefresh(t, engine, ref0, clientID, resource, http.StatusOK)
		oauth.SetServiceClock(func() time.Time { return t0.Add(5 * time.Second) })
		ref2 := postRefresh(t, engine, ref0, clientID, resource, http.StatusOK)
		if ref2 == "" || ref2 == ref1 {
			t.Fatalf("grace refresh should issue new token")
		}
		_ = ref1
	})

	t.Run("replay_after_grace_revokes", func(t *testing.T) {
		oauth.SetServiceClock(func() time.Time { return t0 })
		access, ref0 := mk()
		ref1 := postRefresh(t, engine, ref0, clientID, resource, http.StatusOK)
		oauth.SetServiceClock(func() time.Time { return t0.Add(11 * time.Second) })
		postRefresh(t, engine, ref0, clientID, resource, http.StatusBadRequest)
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
		req.Header.Set("Authorization", "Bearer "+access)
		engine.ServeHTTP(w, req)
		if w.Code != 401 {
			t.Fatalf("access should be dead after replay revoke got %d", w.Code)
		}
		postRefresh(t, engine, ref1, clientID, resource, http.StatusBadRequest)
	})

	t.Run("idle_expired", func(t *testing.T) {
		oauth.SetServiceClock(func() time.Time { return t0 })
		_, ref0 := mk()
		oauth.SetServiceClock(func() time.Time { return t0.Add(31 * 24 * time.Hour) })
		postRefresh(t, engine, ref0, clientID, resource, http.StatusBadRequest)
	})

	t.Run("absolute_expired", func(t *testing.T) {
		oauth.SetServiceClock(func() time.Time { return t0 })
		_, ref0 := mk()
		oauth.SetServiceClock(func() time.Time { return t0.Add(91 * 24 * time.Hour) })
		postRefresh(t, engine, ref0, clientID, resource, http.StatusBadRequest)
	})

	t.Run("revoked_grant_refresh", func(t *testing.T) {
		oauth.SetServiceClock(func() time.Time { return t0 })
		_, ref0 := mk()
		grants, _ := st.ListOAuthGrants(t.Context())
		if len(grants) == 0 {
			t.Fatal("no grants")
		}
		gid := grants[len(grants)-1].ID
		_ = srv // use api revoke via REST
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodDelete, "/api/v1/oauth/grants/"+strconv.FormatUint(uint64(gid), 10), nil)
		req.Header.Set("Authorization", "Bearer "+jwt)
		engine.ServeHTTP(w, req)
		if w.Code != 200 {
			t.Fatalf("revoke grant %d", w.Code)
		}
		postRefresh(t, engine, ref0, clientID, resource, http.StatusBadRequest)
	})
}

func TestOAuthConcurrentRefreshRace(t *testing.T) {
	gin.SetMode(gin.TestMode)
	t0 := time.Now()
	oauth.SetServiceClock(func() time.Time { return t0 })
	defer oauth.SetServiceClock(nil)

	st, _ := store.New(":memory:")
	enableOAuthServer(t, st)
	cfg := oauthTestCfg()
	srv := api.NewServer(cfg, st, nil)
	engine := srv.Engine()
	jwt := loginJWT(t, engine, "admin", cfg.WebPassword)
	unlock := terminalUnlock(t, engine, jwt, cfg.WebPassword)
	clientID, redirectURI, resource, verifier, challenge := registerTestClient(t, engine)
	code := approveFlow(t, engine, jwt, unlock, clientID, redirectURI, resource, challenge, verifier)
	_, ref0 := postTokenPair(t, engine, code, clientID, redirectURI, resource, verifier, http.StatusOK)
	_ = srv

	var wg sync.WaitGroup
	results := make([]string, 2)
	codes := make([]int, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			form := "grant_type=refresh_token&refresh_token=" + ref0 + "&client_id=" + clientID + "&resource=" + resource
			w := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/oauth/token", strings.NewReader(form))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			engine.ServeHTTP(w, req)
			codes[idx] = w.Code
			if w.Code == 200 {
				var tok map[string]any
				_ = json.Unmarshal(w.Body.Bytes(), &tok)
				results[idx], _ = tok["refresh_token"].(string)
			}
		}(i)
	}
	wg.Wait()
	ok := 0
	var lastOK string
	for i := 0; i < 2; i++ {
		if codes[i] == 200 {
			ok++
			lastOK = results[i]
		}
	}
	if ok < 1 {
		t.Fatalf("want at least one successful refresh got codes %v", codes)
	}
	if lastOK == ref0 {
		t.Fatal("refresh token must rotate")
	}
	// 最终只有最新链可继续刷新（互斥后不应留下两条并行有效链）
	postRefresh(t, engine, lastOK, clientID, resource, http.StatusOK)
}
