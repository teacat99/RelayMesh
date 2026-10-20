package api

import (
	"net/http"
	"strings"
	"testing"
)

const capAPIHost = "RM-AAAA-BBBB-CCCC-DDDD"

func capToolsListCall(srv *Server, path string, headers ...string) wiredResp {
	return wiredCall(srv, "POST", path, map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/list", "params": map[string]any{}}, headers...)
}

// 能力 URL 凭据的 REST 生命周期：创建（需解锁，网址只在创建时给出）→ 列表脱敏 → 经完整路由可用 →
// 令牌换成 Bearer 在管理面被拒 → 重新生成使旧网址失效 → 吊销即时生效。
func TestCapabilityCredentialRESTLifecycle(t *testing.T) {
	srv, _ := newConvergenceServer(t, nil)
	jwt := wiredLogin(t, srv)
	login := []string{"Authorization", "Bearer " + jwt}
	unlocked := append(append([]string{}, login...), "X-Terminal-Unlock", wiredUnlock(t, srv, jwt))

	create := map[string]any{
		"name":           "chatgpt-connector",
		"capability":     true,
		"permissions":    map[string]any{"terminal": true},
		"terminal_hosts": []string{capAPIHost},
	}

	// 带终端权限，只凭登录不行。
	if res := wiredCall(srv, "POST", "/api/v1/credentials", create, login...); res.Code != http.StatusForbidden || res.str("code") != "unlock_required" {
		t.Fatalf("创建能力凭据只凭登录应要求解锁: %d %s", res.Code, res.Raw)
	}

	// 违规配置被拒绝：治理权限、全部主机、没有主机。
	for name, mut := range map[string]func(map[string]any){
		"治理权限": func(m map[string]any) { m["permissions"] = map[string]any{"terminal": true, "feedback": true} },
		"全部主机": func(m map[string]any) { m["terminal_hosts"] = []string{"*"} },
		"没有主机": func(m map[string]any) { m["terminal_hosts"] = []string{} },
		"自带令牌": func(m map[string]any) { m["token"] = "rmc-" + strings.Repeat("a", 64) },
	} {
		body := map[string]any{}
		for k, v := range create {
			body[k] = v
		}
		body["name"] = "bad-" + name
		mut(body)
		res := wiredCall(srv, "POST", "/api/v1/credentials", body, unlocked...)
		if res.Code != http.StatusBadRequest && name != "自带令牌" {
			t.Errorf("%s：应 400，得到 %d %s", name, res.Code, res.Raw)
		}
		if name == "自带令牌" && res.Code == http.StatusOK && res.str("token") == "rmc-"+strings.Repeat("a", 64) {
			t.Errorf("客户端自带的令牌不得被采用: %s", res.Raw)
		}
	}

	res := wiredCall(srv, "POST", "/api/v1/credentials", create, unlocked...)
	if res.Code != http.StatusOK {
		t.Fatalf("创建能力凭据: %d %s", res.Code, res.Raw)
	}
	token := res.str("token")
	path := res.str("capability_path")
	if !strings.HasPrefix(token, "rmc-") || path != "/mcp/c/"+token {
		t.Fatalf("创建响应应给出令牌与一次性网址路径: token=%q path=%q", token, path)
	}
	id := credID(t, res)

	// 列表只给脱敏令牌，标出能力凭据，不再给出可用网址。
	list := wiredCall(srv, "GET", "/api/v1/credentials", nil, login...)
	if list.Code != http.StatusOK || strings.Contains(list.Raw, token) {
		t.Fatalf("列表不得包含完整令牌: %d", list.Code)
	}
	if !strings.Contains(list.Raw, `"capability":true`) {
		t.Fatalf("列表应标出能力凭据: %s", list.Raw)
	}

	// 经完整路由可用。
	if r := capToolsListCall(srv, path); r.Code != http.StatusOK {
		t.Fatalf("经能力网址调用应成功: %d %s", r.Code, r.Raw)
	}

	// 能力令牌作为 Bearer 访问管理面：明确的 403；作为 SSE 查询串令牌同样被拒。
	for _, p := range []string{"/api/v1/sessions", "/api/v1/credentials", "/api/v1/terminal/hosts"} {
		if r := wiredCall(srv, "GET", p, nil, "Authorization", "Bearer "+token); r.Code != http.StatusForbidden || r.str("code") != "mcp_token_forbidden" {
			t.Errorf("能力令牌访问 %s 应 403 mcp_token_forbidden: %d %s", p, r.Code, r.Raw)
		}
	}
	if r := wiredCall(srv, "GET", "/api/v1/events?token="+token, nil); r.Code != http.StatusForbidden && r.Code != http.StatusUnauthorized {
		t.Errorf("能力令牌作为 SSE 查询串令牌应被拒绝: %d %s", r.Code, r.Raw)
	}
	// 能力令牌放进请求头或旧式路径调用 MCP 也不行。
	if r := capToolsListCall(srv, "/mcp", "Authorization", "Bearer "+token); r.Code != http.StatusUnauthorized {
		t.Errorf("能力令牌经请求头调用 MCP 应拒绝: %d", r.Code)
	}
	if r := capToolsListCall(srv, "/mcp/"+token); r.Code != http.StatusUnauthorized {
		t.Errorf("能力令牌经旧式路径调用 MCP 应拒绝: %d", r.Code)
	}

	// 更新里的 capability 字段被忽略（不能变回普通凭据），且放宽性质的修改仍被校验。
	if r := wiredCall(srv, "PUT", "/api/v1/credentials/"+id, map[string]any{"capability": false, "note": "x"}, login...); r.Code != http.StatusOK {
		t.Fatalf("更新备注应成功: %d %s", r.Code, r.Raw)
	}
	if r := capToolsListCall(srv, path); r.Code != http.StatusOK {
		t.Fatalf("capability:false 不应让能力凭据变形: %d", r.Code)
	}
	if r := wiredCall(srv, "PUT", "/api/v1/credentials/"+id, map[string]any{"terminal_hosts": []string{"*"}}, unlocked...); r.Code != http.StatusBadRequest {
		t.Fatalf("把能力凭据改成全部主机应 400: %d %s", r.Code, r.Raw)
	}

	// 重新生成：新网址路径一次性给出，旧网址立即失效。
	regen := wiredCall(srv, "POST", "/api/v1/credentials/"+id+"/regenerate", nil, unlocked...)
	if regen.Code != http.StatusOK {
		t.Fatalf("重新生成: %d %s", regen.Code, regen.Raw)
	}
	newToken := regen.str("token")
	newPath := regen.str("capability_path")
	if newToken == token || newPath != "/mcp/c/"+newToken || !strings.HasPrefix(newToken, "rmc-") {
		t.Fatalf("重新生成应返回新的能力令牌与网址: %s", regen.Raw)
	}
	if r := capToolsListCall(srv, path); r.Code != http.StatusUnauthorized {
		t.Fatalf("旧网址应立刻失效: %d", r.Code)
	}
	if r := capToolsListCall(srv, newPath); r.Code != http.StatusOK {
		t.Fatalf("新网址应可用: %d %s", r.Code, r.Raw)
	}

	// 吊销（删除）即时生效。
	if r := wiredCall(srv, "DELETE", "/api/v1/credentials/"+id, nil, login...); r.Code != http.StatusOK {
		t.Fatalf("删除: %d %s", r.Code, r.Raw)
	}
	if r := capToolsListCall(srv, newPath); r.Code != http.StatusUnauthorized {
		t.Fatalf("吊销后应立刻拒绝: %d", r.Code)
	}
}

// 非能力凭据的创建 / 重新生成响应里没有 capability_path。
func TestOrdinaryCredentialHasNoCapabilityPath(t *testing.T) {
	srv, _ := newConvergenceServer(t, nil)
	jwt := wiredLogin(t, srv)
	login := []string{"Authorization", "Bearer " + jwt}

	res := wiredCall(srv, "POST", "/api/v1/credentials", map[string]any{"name": "plain", "permissions": map[string]any{"feedback": true}}, login...)
	if res.Code != http.StatusOK {
		t.Fatalf("创建: %d %s", res.Code, res.Raw)
	}
	if _, has := res.Body["capability_path"]; has {
		t.Fatalf("普通凭据不应有 capability_path: %s", res.Raw)
	}
	id := credID(t, res)
	regen := wiredCall(srv, "POST", "/api/v1/credentials/"+id+"/regenerate", nil, login...)
	if regen.Code != http.StatusOK {
		t.Fatalf("重新生成: %d %s", regen.Code, regen.Raw)
	}
	if _, has := regen.Body["capability_path"]; has {
		t.Fatalf("普通凭据重新生成不应有 capability_path: %s", regen.Raw)
	}
	// 普通凭据的令牌放进能力路径同样被拒。
	if r := capToolsListCall(srv, "/mcp/c/"+res.str("token")); r.Code != http.StatusUnauthorized {
		t.Fatalf("普通令牌不得通过能力路径: %d", r.Code)
	}
}
