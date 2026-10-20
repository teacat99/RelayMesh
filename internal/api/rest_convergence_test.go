package api

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/teacat99/RelayMesh/v2/internal/config"
	"github.com/teacat99/RelayMesh/v2/internal/model"
)

const (
	convergenceConfigureToken = "env-configure-token-for-tests-123456"
	convergenceExecuteToken   = "env-execute-token-for-tests-7890123"
)

// newConvergenceServer 返回配置了环境变量令牌与一把凭据库令牌的完整装配服务。
func newConvergenceServer(t *testing.T, mutate func(*config.Config)) (*Server, string) {
	t.Helper()
	srv := newWiredServer(t, func(c *config.Config) {
		c.ConfigureToken = convergenceConfigureToken
		c.ExecutionToken = convergenceExecuteToken
		c.AllowQueryToken = true
		if mutate != nil {
			mutate(c)
		}
	})
	cred := &model.MCPCredential{Name: "converge-agent", Permissions: model.AllPermissions()}
	if err := srv.store.CreateCredential(context.Background(), cred); err != nil {
		t.Fatalf("创建凭据: %v", err)
	}
	return srv, cred.Token
}

// 任何一种 MCP 令牌都不能再访问 REST / SSE 管理面，且拒绝原因明确（403 + mcp_token_forbidden）。
func TestRESTRejectsEveryKindOfMCPToken(t *testing.T) {
	srv, dbToken := newConvergenceServer(t, nil)

	tokens := map[string]string{
		"环境变量 MCP 令牌":    wiredMCPToken,
		"环境变量 configure": convergenceConfigureToken,
		"环境变量 execute":   convergenceExecuteToken,
		"凭据库令牌":          dbToken,
	}
	routes := []struct{ method, path string }{
		{"GET", "/api/v1/sessions"},
		{"GET", "/api/v1/sessions/current"},
		{"GET", "/api/v1/credentials"},
		{"POST", "/api/v1/credentials"},
		{"POST", "/api/v1/credentials/preview"},
		{"POST", "/api/v1/credentials/1/regenerate"},
		{"POST", "/api/v1/credentials/1/reveal"},
		{"GET", "/api/v1/settings"},
		{"PUT", "/api/v1/settings"},
		{"GET", "/api/v1/tasks"},
		{"GET", "/api/v1/norms"},
		{"POST", "/api/v1/auth/sse_ticket"},
		{"POST", "/api/v1/auth/change_credentials"},
		{"GET", "/api/v1/auth/blocked_ips"},
		{"GET", "/api/v1/events"},
		{"GET", "/sse/events"},
	}
	for name, token := range tokens {
		for _, rt := range routes {
			res := wiredCall(srv, rt.method, rt.path, nil, "Authorization", "Bearer "+token)
			if res.Code != http.StatusForbidden || res.str("code") != "mcp_token_forbidden" {
				t.Errorf("%s 访问 %s %s 应得到 403 mcp_token_forbidden，实际 %d %s", name, rt.method, rt.path, res.Code, res.Raw)
			}
			if strings.Contains(res.Raw, token) {
				t.Errorf("拒绝响应不得回显令牌")
			}
		}
	}
}

// SSE 的 ?token= 兼容通道（即便打开）也只认人类 JWT，不再认 MCP 令牌。
func TestSSEQueryTokenNoLongerAcceptsMCPTokens(t *testing.T) {
	srv, dbToken := newConvergenceServer(t, nil)
	for _, token := range []string{wiredMCPToken, convergenceConfigureToken, dbToken} {
		for _, path := range []string{"/api/v1/events", "/sse/events"} {
			res := wiredCall(srv, "GET", path+"?token="+token, nil)
			if res.Code != http.StatusForbidden {
				t.Errorf("%s?token=<MCP 令牌> 应 403，实际 %d", path, res.Code)
			}
		}
	}
	// 无效 / 缺失的凭据仍是 401，而不是 403
	if res := wiredCall(srv, "GET", "/api/v1/events?token=not-a-real-token", nil); res.Code != http.StatusUnauthorized {
		t.Errorf("无效令牌应 401，实际 %d", res.Code)
	}
	if res := wiredCall(srv, "GET", "/api/v1/events", nil); res.Code != http.StatusUnauthorized {
		t.Errorf("未带凭据应 401，实际 %d", res.Code)
	}
	if res := wiredCall(srv, "GET", "/api/v1/events?ticket=bogus", nil); res.Code != http.StatusUnauthorized {
		t.Errorf("无效票据应 401，实际 %d", res.Code)
	}
}

// 收敛不得误伤正常路径：人类 JWT、一次性票据、/mcp 端点与匿名图片路由。
func TestConvergenceKeepsLegitimatePathsWorking(t *testing.T) {
	srv, dbToken := newConvergenceServer(t, nil)
	jwtToken := wiredLogin(t, srv)
	bearer := []string{"Authorization", "Bearer " + jwtToken}

	for _, path := range []string{"/api/v1/sessions", "/api/v1/credentials", "/api/v1/settings", "/api/v1/tasks"} {
		if res := wiredCall(srv, "GET", path, nil, bearer...); res.Code != http.StatusOK {
			t.Errorf("人类 JWT 访问 %s 应为 200，实际 %d %s", path, res.Code, res.Raw)
		}
	}

	// 一次性票据仍可签发（供 SSE 使用）
	res := wiredCall(srv, "POST", "/api/v1/auth/sse_ticket", nil, bearer...)
	if res.Code != http.StatusOK || res.str("ticket") == "" {
		t.Fatalf("人类 JWT 应能签发 SSE 票据: %d %s", res.Code, res.Raw)
	}

	// /mcp 端点用 MCP 令牌照常工作（收敛的只是 REST / SSE）
	for name, token := range map[string]string{"环境变量": wiredMCPToken, "凭据库": dbToken} {
		res := wiredCall(srv, "POST", "/mcp", map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/list", "params": map[string]any{}},
			"Authorization", "Bearer "+token)
		if res.Code != http.StatusOK || res.Body["error"] != nil {
			t.Errorf("%s 令牌访问 /mcp 应成功: %d %s", name, res.Code, res.Raw)
		}
	}

	// 图片只读路由保持匿名可达（不存在的会话是 404，而不是 401/403）
	if res := wiredCall(srv, "GET", "/api/v1/sessions/sess-nonexistent/images/0", nil); res.Code == http.StatusUnauthorized || res.Code == http.StatusForbidden {
		t.Errorf("图片路由应保持匿名，实际 %d", res.Code)
	}
}

// 未设置 Web 密码时管理面本就不设防（行为不变），终端等能力在此状态下由总闸关闭。
func TestConvergenceWithoutPasswordIsUnchanged(t *testing.T) {
	srv, _ := newConvergenceServer(t, func(c *config.Config) { c.WebPassword = "" })
	if res := wiredCall(srv, "GET", "/api/v1/sessions", nil); res.Code != http.StatusOK {
		t.Errorf("未设密码时管理接口保持开放: %d %s", res.Code, res.Raw)
	}
}

// 凭据 REST：新字段往返、权限预览与「弱令牌不得带终端权限」。
func TestCredentialsRESTTerminalFields(t *testing.T) {
	srv, _ := newConvergenceServer(t, nil)
	jwtToken := wiredLogin(t, srv)
	// 授予终端能力的令牌管理需要二次解锁（见 credentials_terminal_test.go），这里先解锁。
	bearer := []string{"Authorization", "Bearer " + jwtToken, "X-Terminal-Unlock", wiredUnlock(t, srv, jwtToken)}

	created := wiredCall(srv, "POST", "/api/v1/credentials", map[string]any{
		"name":              "exec-only",
		"permissions":       map[string]any{"terminal": true},
		"terminal_hosts":    []string{"host-a", "host-a", "host-b"},
		"terminal_mode_cap": "approve",
		"terminal_limits":   map[string]any{"max_concurrent": 2},
		"allowed_cidrs":     []string{"10.0.0.0/8"},
	}, bearer...)
	if created.Code != http.StatusOK || created.str("token") == "" {
		t.Fatalf("创建 ExecMesh 档令牌失败: %d %s", created.Code, created.Raw)
	}

	list := wiredCall(srv, "GET", "/api/v1/credentials", nil, bearer...)
	items, _ := list.Body["credentials"].([]any)
	var found map[string]any
	for _, it := range items {
		m, _ := it.(map[string]any)
		if m["name"] == "exec-only" {
			found = m
		}
	}
	if found == nil {
		t.Fatalf("列表里应有刚创建的令牌: %s", list.Raw)
	}
	if found["tier"] != "execmesh" || found["terminal_mode_cap"] != "approve" || found["token_weakness"] != "" {
		t.Fatalf("档位 / 模式 / 强度不符: %#v", found)
	}
	hosts, _ := found["terminal_hosts"].([]any)
	if len(hosts) != 2 {
		t.Fatalf("主机应去重为 2 个: %#v", found["terminal_hosts"])
	}
	if tok, _ := found["token"].(string); strings.Contains(tok, created.str("token")) {
		t.Fatal("列表不得返回完整令牌")
	}

	// 弱令牌不得带终端权限创建
	weak := wiredCall(srv, "POST", "/api/v1/credentials", map[string]any{
		"name": "weak-exec", "token": "short", "permissions": map[string]any{"terminal": true},
	}, bearer...)
	if weak.Code != http.StatusBadRequest {
		t.Fatalf("弱令牌带终端权限应 400: %d %s", weak.Code, weak.Raw)
	}

	// 非法字段被拒
	bad := wiredCall(srv, "PUT", "/api/v1/credentials/1", map[string]any{"terminal_mode_cap": "root"}, bearer...)
	if bad.Code != http.StatusBadRequest {
		t.Fatalf("非法模式应 400: %d %s", bad.Code, bad.Raw)
	}
	unknown := wiredCall(srv, "PUT", "/api/v1/credentials/1", map[string]any{"token_hash": "x"}, bearer...)
	if unknown.Code != http.StatusBadRequest {
		t.Fatalf("未知字段应 400: %d %s", unknown.Code, unknown.Raw)
	}

	// 预览：不同权限组合对应不同档位与工具集
	preview := func(perms map[string]any) wiredResp {
		return wiredCall(srv, "POST", "/api/v1/credentials/preview", map[string]any{"permissions": perms}, bearer...)
	}
	if r := preview(map[string]any{"feedback": true, "sessions": true}); r.Code != http.StatusOK || r.str("tier") != "relaymesh" || r.str("instructions") != "governance" {
		t.Fatalf("治理档预览不符: %d %s", r.Code, r.Raw)
	}
	if r := preview(map[string]any{"terminal": true}); r.str("tier") != "execmesh" || r.str("instructions") != "terminal" {
		t.Fatalf("ExecMesh 档预览不符: %s", r.Raw)
	}
	if r := preview(map[string]any{"feedback": true, "terminal": true}); r.str("tier") != "hybrid" || r.Body["terminal_requires_workflow"] != true {
		t.Fatalf("混合档预览不符: %s", r.Raw)
	}
	if r := preview(map[string]any{}); r.str("tier") != "none" {
		t.Fatalf("无权限预览不符: %s", r.Raw)
	}

	// 只有文件权限：ExecMesh 档；暴露 terminal_files，且不要求 workflow_id（这个工具没有这类参数）
	if r := preview(map[string]any{"terminal_files": true}); r.str("tier") != "execmesh" || r.Body["terminal_requires_workflow"] != false ||
		!strings.Contains(r.Raw, `"terminal_files"`) {
		t.Fatalf("纯文件权限预览不符: %s", r.Raw)
	}
	// 治理 + 文件：混合档，但没有终端执行权限，所以同样不要求 workflow_id
	if r := preview(map[string]any{"feedback": true, "terminal_files": true}); r.str("tier") != "hybrid" || r.Body["terminal_requires_workflow"] != false {
		t.Fatalf("治理 + 文件预览不符: %s", r.Raw)
	}
	// 治理 + 执行 + 文件：混合档，要求 workflow_id
	if r := preview(map[string]any{"feedback": true, "terminal": true, "terminal_files": true}); r.str("tier") != "hybrid" || r.Body["terminal_requires_workflow"] != true {
		t.Fatalf("治理 + 执行 + 文件预览不符: %s", r.Raw)
	}
}
