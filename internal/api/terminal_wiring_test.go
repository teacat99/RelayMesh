package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/teacat99/RelayMesh/v2/internal/config"
	"github.com/teacat99/RelayMesh/v2/internal/store"
)

const (
	wiredPassword = "correct-password-123"
	wiredMCPToken = "mcp-secret-token"
	// 仅用于测试的假密钥：需要通过 config.IsStrongJWTSecret（不能含 "test-jwt-secret" 之类的占位片段）。
	wiredJWTSecret = "k9Xv2mQ7pL4wZ8rT1nB6yH3cD5fG0jA9eS2uV7xM"
)

func newWiredServer(t *testing.T, mutate func(*config.Config)) *Server {
	t.Helper()
	cfg := &config.Config{
		ProjectID:   "wired-proj",
		WebUsername: "admin",
		WebPassword: wiredPassword,
		JWTSecret:   wiredJWTSecret,
		MCPToken:    wiredMCPToken,
	}
	if mutate != nil {
		mutate(cfg)
	}
	st, err := store.New(":memory:")
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	srv := NewServer(cfg, st, nil)
	srv.Start()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		srv.Shutdown(ctx)
		_ = st.Close()
	})
	return srv
}

type wiredResp struct {
	Code int
	Body map[string]any
	Raw  string
}

func (r wiredResp) str(k string) string {
	v, _ := r.Body[k].(string)
	return v
}

func wiredCall(srv *Server, method, path string, body any, headers ...string) wiredResp {
	var rdr *bytes.Reader
	if body != nil {
		raw, _ := json.Marshal(body)
		rdr = bytes.NewReader(raw)
	} else {
		rdr = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, path, rdr)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	w := httptest.NewRecorder()
	srv.Engine().ServeHTTP(w, req)
	res := wiredResp{Code: w.Code, Raw: w.Body.String()}
	_ = json.Unmarshal(w.Body.Bytes(), &res.Body)
	return res
}

func wiredLogin(t *testing.T, srv *Server) string {
	t.Helper()
	res := wiredCall(srv, "POST", "/api/v1/auth/login", map[string]string{"username": "admin", "password": wiredPassword})
	token := res.str("token")
	if res.Code != http.StatusOK || token == "" {
		t.Fatalf("登录失败: %d %s", res.Code, res.Raw)
	}
	return token
}

// 真实装配下：登录 JWT 可用，MCP 令牌不可用；未登录不可用；状态接口反映总闸。
func TestTerminalWiringAuthBoundaries(t *testing.T) {
	srv := newWiredServer(t, nil)
	jwtToken := wiredLogin(t, srv)
	bearer := func(tok string) []string { return []string{"Authorization", "Bearer " + tok} }

	if res := wiredCall(srv, "GET", "/api/v1/terminal/status", nil, bearer(jwtToken)...); res.Code != 200 || res.Body["enabled"] != true {
		t.Fatalf("登录后状态应为 enabled: %d %s", res.Code, res.Raw)
	}
	if res := wiredCall(srv, "GET", "/api/v1/terminal/hosts", nil, bearer(jwtToken)...); res.Code != 200 {
		t.Fatalf("登录 JWT 应能读取主机列表: %d %s", res.Code, res.Raw)
	}
	// v1.7 起 MCP 令牌在整个管理面（REST / SSE）都不被接受：普通 API 与终端端点一律返回带明确原因的 403，
	// 匿名访问与随便一个令牌仍是 401。
	if res := wiredCall(srv, "GET", "/api/v1/sessions", nil, bearer(wiredMCPToken)...); res.Code != http.StatusForbidden || res.str("code") != "mcp_token_forbidden" {
		t.Fatalf("MCP 令牌访问普通管理 API 应得到 403 mcp_token_forbidden: %d %s", res.Code, res.Raw)
	}
	for _, path := range []string{"/api/v1/terminal/status", "/api/v1/terminal/hosts", "/api/v1/terminal/audit"} {
		if res := wiredCall(srv, "GET", path, nil, bearer(wiredMCPToken)...); res.Code != http.StatusForbidden || res.str("code") != "mcp_token_forbidden" {
			t.Errorf("MCP 令牌访问 %s 必须 403 mcp_token_forbidden，得到 %d %s", path, res.Code, res.Raw)
		}
		if res := wiredCall(srv, "GET", path, nil, bearer("not-a-real-token-0123456789")...); res.Code != http.StatusUnauthorized {
			t.Errorf("未知令牌访问 %s 必须 401，得到 %d", path, res.Code)
		}
		if res := wiredCall(srv, "GET", path, nil); res.Code != http.StatusUnauthorized {
			t.Errorf("匿名访问 %s 必须 401，得到 %d", path, res.Code)
		}
	}
	// 令牌放在 URL 上不被接受
	if res := wiredCall(srv, "GET", "/api/v1/terminal/hosts?token="+jwtToken, nil); res.Code != http.StatusUnauthorized {
		t.Errorf("URL 令牌必须被拒: %d", res.Code)
	}
}

func TestTerminalWiringGateReasons(t *testing.T) {
	t.Run("环境强制关闭", func(t *testing.T) {
		srv := newWiredServer(t, func(c *config.Config) { c.TerminalMode = "off" })
		tok := wiredLogin(t, srv)
		res := wiredCall(srv, "GET", "/api/v1/terminal/status", nil, "Authorization", "Bearer "+tok)
		if res.Code != 200 || res.Body["enabled"] != false || res.str("reason") != "disabled_by_env" {
			t.Fatalf("状态不符: %d %s", res.Code, res.Raw)
		}
		if res := wiredCall(srv, "POST", "/api/v1/terminal/unlock", map[string]string{"password": wiredPassword}, "Authorization", "Bearer "+tok); res.Code != http.StatusForbidden {
			t.Fatalf("总闸关闭时即使密码正确也不能解锁: %d %s", res.Code, res.Raw)
		}
	})
	t.Run("无法识别的取值按关闭处理", func(t *testing.T) {
		srv := newWiredServer(t, func(c *config.Config) { c.TerminalMode = "enable-please" })
		tok := wiredLogin(t, srv)
		res := wiredCall(srv, "GET", "/api/v1/terminal/status", nil, "Authorization", "Bearer "+tok)
		if res.Body["enabled"] != false || res.str("reason") != "disabled_by_env" {
			t.Fatalf("写错的取值必须保持关闭: %s", res.Raw)
		}
	})
	t.Run("未设置 Web 密码", func(t *testing.T) {
		srv := newWiredServer(t, func(c *config.Config) { c.WebPassword = "" })
		res := wiredCall(srv, "GET", "/api/v1/terminal/status", nil)
		if res.Code != 200 || res.Body["enabled"] != false || res.str("reason") != "no_password" {
			t.Fatalf("状态不符: %d %s", res.Code, res.Raw)
		}
		if res := wiredCall(srv, "GET", "/api/v1/terminal/hosts", nil); res.Code != http.StatusForbidden {
			t.Fatalf("无密码模式下终端接口必须 403: %d %s", res.Code, res.Raw)
		}
	})
	t.Run("JWT 密钥过弱", func(t *testing.T) {
		srv := newWiredServer(t, func(c *config.Config) { c.JWTSecret = "weak" })
		tok := wiredLogin(t, srv) // 登录仍可用（沿用既有行为），但终端关闭
		res := wiredCall(srv, "GET", "/api/v1/terminal/status", nil, "Authorization", "Bearer "+tok)
		if res.Body["enabled"] != false || res.str("reason") != "weak_jwt_secret" {
			t.Fatalf("弱密钥必须关闭终端: %s", res.Raw)
		}
	})
}

// 真实装配下的完整「解锁 → 写操作 → 改密撤销」链路。
func TestTerminalWiringUnlockAndCredentialChange(t *testing.T) {
	srv := newWiredServer(t, nil)
	tok := wiredLogin(t, srv)
	bearer := []string{"Authorization", "Bearer " + tok}

	if res := wiredCall(srv, "POST", "/api/v1/terminal/discovery", nil, bearer...); res.Code != http.StatusForbidden || res.str("code") != "unlock_required" {
		t.Fatalf("未解锁应拒绝: %d %s", res.Code, res.Raw)
	}
	un := wiredCall(srv, "POST", "/api/v1/terminal/unlock", map[string]string{"password": wiredPassword}, bearer...)
	unlockTok := un.str("token")
	if un.Code != 200 || unlockTok == "" {
		t.Fatalf("解锁失败: %d %s", un.Code, un.Raw)
	}
	hdr := append(append([]string{}, bearer...), "X-Terminal-Unlock", unlockTok)
	if res := wiredCall(srv, "POST", "/api/v1/terminal/discovery", map[string]int{"minutes": 5}, hdr...); res.Code != 200 {
		t.Fatalf("解锁后应可开启发现窗口: %d %s", res.Code, res.Raw)
	}
	st := wiredCall(srv, "GET", "/api/v1/terminal/status", nil, hdr...)
	if disc, _ := st.Body["discovery"].(map[string]any); disc["open"] != true {
		t.Fatalf("窗口应已开启: %s", st.Raw)
	}

	// 通过真实的改密接口修改密码：解锁令牌必须随之作废，发现窗口之外的状态不受影响。
	ch := wiredCall(srv, "POST", "/api/v1/auth/change_credentials",
		map[string]string{"old_password": wiredPassword, "new_username": "admin", "new_password": "brand-new-password"}, bearer...)
	if ch.Code != 200 {
		t.Fatalf("改密失败: %d %s", ch.Code, ch.Raw)
	}
	if res := wiredCall(srv, "POST", "/api/v1/terminal/discovery", nil, hdr...); res.Code != http.StatusForbidden || res.str("code") != "unlock_required" {
		t.Fatalf("改密后旧解锁令牌必须作废: %d %s", res.Code, res.Raw)
	}
	// 旧密码不能再解锁，新密码可以
	bad := wiredCall(srv, "POST", "/api/v1/terminal/unlock", map[string]string{"password": wiredPassword}, bearer...)
	// 403 而不是 401：401 会被前端当成登录过期而踢回登录页。
	if bad.Code != http.StatusForbidden || bad.str("code") != "unlock_failed" {
		t.Fatalf("旧密码不应再能解锁（应 403 unlock_failed）: %d %s", bad.Code, bad.Raw)
	}
	good := wiredCall(srv, "POST", "/api/v1/terminal/unlock", map[string]string{"password": "brand-new-password"}, bearer...)
	if good.Code != 200 {
		t.Fatalf("新密码应能解锁: %d %s", good.Code, good.Raw)
	}
}

// 终端路由必须全部挂在 Hub 的闸门之后：枚举真实引擎上的路由，总闸关闭时逐条验证（AC-1 的装配级复核）。
func TestTerminalWiringEveryRouteClosedWhenDisabled(t *testing.T) {
	srv := newWiredServer(t, func(c *config.Config) { c.TerminalMode = "off" })
	tok := wiredLogin(t, srv)

	count := 0
	for _, r := range srv.Engine().Routes() {
		if !strings.HasPrefix(r.Path, "/api/v1/terminal") || (r.Method == http.MethodGet && r.Path == "/api/v1/terminal/status") {
			continue
		}
		count++
		path := strings.NewReplacer(":code", "RM-AAAA-BBBB-CCCC-DDDD", ":sid", "0123456789abcdef0123456789abcdef").Replace(r.Path)
		res := wiredCall(srv, r.Method, path, nil, "Authorization", "Bearer "+tok, "X-Terminal-Unlock", "x")
		if res.Code != http.StatusForbidden {
			t.Errorf("%s %s 在总闸关闭时应 403，得到 %d (%s)", r.Method, r.Path, res.Code, res.Raw)
		}
	}
	if count < 19 {
		t.Fatalf("终端路由数量异常: %d", count)
	}
	_ = gin.Mode()
}
