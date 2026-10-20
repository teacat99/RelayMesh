package api

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/teacat99/RelayMesh/v2/internal/config"
	"github.com/teacat99/RelayMesh/v2/internal/model"
)

// wiredUnlock 用登录密码换取终端解锁令牌。
func wiredUnlock(t *testing.T, srv *Server, jwtToken string) string {
	t.Helper()
	res := wiredCall(srv, "POST", "/api/v1/terminal/unlock", map[string]string{"password": wiredPassword}, "Authorization", "Bearer "+jwtToken)
	if res.Code != http.StatusOK || res.str("token") == "" {
		t.Fatalf("解锁失败: %d %s", res.Code, res.Raw)
	}
	return res.str("token")
}

func credID(t *testing.T, res wiredResp) string {
	t.Helper()
	cred, _ := res.Body["credential"].(map[string]any)
	id, _ := cred["id"].(float64)
	if id == 0 {
		t.Fatalf("响应里没有令牌 ID: %s", res.Raw)
	}
	return strconv.Itoa(int(id))
}

// 授予或放宽终端能力需要二次解锁；收紧类的操作（停用、删除、清空主机授权、关闭终端位）只需登录。
func TestTerminalCredentialManagementRequiresUnlock(t *testing.T) {
	srv, _ := newConvergenceServer(t, nil)
	jwt := wiredLogin(t, srv)
	login := []string{"Authorization", "Bearer " + jwt}
	unlocked := append(append([]string{}, login...), "X-Terminal-Unlock", wiredUnlock(t, srv, jwt))

	// 创建
	for name, body := range map[string]map[string]any{
		"终端权限位":  {"name": "a", "permissions": map[string]any{"terminal": true}},
		"文件权限位":  {"name": "b", "permissions": map[string]any{"terminal_files": true}},
		"只带主机授权": {"name": "c", "permissions": map[string]any{"feedback": true}, "terminal_hosts": []string{"RM-AAAA-BBBB-CCCC-DDDD"}},
		"只带模式上限": {"name": "d", "permissions": map[string]any{"feedback": true}, "terminal_mode_cap": "readonly"},
		"只带限额":   {"name": "e", "permissions": map[string]any{"feedback": true}, "terminal_limits": map[string]any{"max_concurrent": 1}},
		"运行预设位":  {"name": "f", "permissions": map[string]any{"feedback": true, "launcher": true}}, // #179：让项目管理 Agent 能拉起别的智能体
	} {
		res := wiredCall(srv, "POST", "/api/v1/credentials", body, login...)
		if res.Code != http.StatusForbidden || res.str("code") != "unlock_required" {
			t.Errorf("创建（%s）只凭登录应被拒绝并要求解锁: %d %s", name, res.Code, res.Raw)
		}
	}
	// 与终端无关的令牌：仍然只需登录。
	plain := wiredCall(srv, "POST", "/api/v1/credentials", map[string]any{"name": "plain", "permissions": map[string]any{"feedback": true}}, login...)
	if plain.Code != http.StatusOK {
		t.Fatalf("普通令牌不应要求解锁: %d %s", plain.Code, plain.Raw)
	}
	plainID := credID(t, plain)

	// 授予：给普通令牌打开终端位需要解锁。
	grant := map[string]any{"permissions": map[string]any{"feedback": true, "terminal": true}}
	if res := wiredCall(srv, "PUT", "/api/v1/credentials/"+plainID, grant, login...); res.Code != http.StatusForbidden {
		t.Fatalf("授予终端位只凭登录应被拒绝: %d %s", res.Code, res.Raw)
	}
	if res := wiredCall(srv, "PUT", "/api/v1/credentials/"+plainID, grant, unlocked...); res.Code != http.StatusOK {
		t.Fatalf("解锁后应能授予: %d %s", res.Code, res.Raw)
	}

	// 现在它带终端能力了：放宽性质的修改需要解锁，改名 / 备注不需要。
	for name, body := range map[string]map[string]any{
		"加主机":    {"terminal_hosts": []string{"RM-AAAA-BBBB-CCCC-DDDD"}},
		"改限额":    {"terminal_limits": map[string]any{"max_concurrent": 9}},
		"清除模式上限": {"terminal_mode_cap": ""},
		"延长有效期":  {"expires_at": "2099-01-01T00:00:00Z"},
		"改来源白名单": {"allowed_cidrs": []string{"0.0.0.0/0"}},
	} {
		if res := wiredCall(srv, "PUT", "/api/v1/credentials/"+plainID, body, login...); res.Code != http.StatusForbidden {
			t.Errorf("%s 只凭登录应被拒绝: %d %s", name, res.Code, res.Raw)
		}
	}
	if res := wiredCall(srv, "PUT", "/api/v1/credentials/"+plainID, map[string]any{"note": "已备注", "name": "renamed"}, login...); res.Code != http.StatusOK {
		t.Errorf("改名 / 备注不应要求解锁: %d %s", res.Code, res.Raw)
	}
	if res := wiredCall(srv, "PUT", "/api/v1/credentials/"+plainID, map[string]any{"terminal_hosts": []string{}}, login...); res.Code != http.StatusOK {
		t.Errorf("清空主机授权是收紧，只需登录: %d %s", res.Code, res.Raw)
	}

	// 刹车：停用只需登录；重新启用是放开，需要解锁。
	if res := wiredCall(srv, "PUT", "/api/v1/credentials/"+plainID, map[string]any{"is_active": false}, login...); res.Code != http.StatusOK {
		t.Errorf("停用只需登录: %d %s", res.Code, res.Raw)
	}
	if res := wiredCall(srv, "PUT", "/api/v1/credentials/"+plainID, map[string]any{"is_active": true}, login...); res.Code != http.StatusForbidden {
		t.Errorf("重新启用带终端能力的令牌需要解锁: %d %s", res.Code, res.Raw)
	}
	if res := wiredCall(srv, "PUT", "/api/v1/credentials/"+plainID, map[string]any{"is_active": true}, unlocked...); res.Code != http.StatusOK {
		t.Errorf("解锁后可重新启用: %d %s", res.Code, res.Raw)
	}

	// 重新生成带终端能力的令牌会把新密钥交到调用者手里：需要解锁。
	if res := wiredCall(srv, "POST", "/api/v1/credentials/"+plainID+"/regenerate", nil, login...); res.Code != http.StatusForbidden {
		t.Errorf("重新生成终端令牌只凭登录应被拒绝: %d %s", res.Code, res.Raw)
	}
	regen := wiredCall(srv, "POST", "/api/v1/credentials/"+plainID+"/regenerate", nil, unlocked...)
	if regen.Code != http.StatusOK || regen.str("token") == "" {
		t.Errorf("解锁后可重新生成: %d %s", regen.Code, regen.Raw)
	}

	// 关闭终端位是收紧，只需登录；之后它不再带终端能力，重新生成也不再需要解锁。
	if res := wiredCall(srv, "PUT", "/api/v1/credentials/"+plainID, map[string]any{"permissions": map[string]any{"feedback": true}}, login...); res.Code != http.StatusOK {
		t.Errorf("关闭终端位只需登录: %d %s", res.Code, res.Raw)
	}

	// 删除永远只需登录（刹车）。
	exec := wiredCall(srv, "POST", "/api/v1/credentials", map[string]any{"name": "to-delete", "permissions": map[string]any{"terminal": true}}, unlocked...)
	if exec.Code != http.StatusOK {
		t.Fatalf("创建: %d %s", exec.Code, exec.Raw)
	}
	if res := wiredCall(srv, "DELETE", "/api/v1/credentials/"+credID(t, exec), nil, login...); res.Code != http.StatusOK {
		t.Errorf("删除终端令牌只需登录: %d %s", res.Code, res.Raw)
	}

	// 错误的解锁令牌与别人的登录主体不通过；响应不回显任何令牌。
	bogus := append(append([]string{}, login...), "X-Terminal-Unlock", "not-a-real-unlock-token")
	res := wiredCall(srv, "POST", "/api/v1/credentials", map[string]any{"name": "z", "permissions": map[string]any{"terminal": true}}, bogus...)
	if res.Code != http.StatusForbidden || strings.Contains(res.Raw, "not-a-real-unlock-token") {
		t.Errorf("伪造的解锁令牌应被拒绝且不回显: %d %s", res.Code, res.Raw)
	}
}

// 终端总闸关闭（未设置 Web 密码）时，不能授予终端权限。
func TestTerminalCredentialGrantUnavailableWithoutGate(t *testing.T) {
	srv, _ := newConvergenceServer(t, func(c *config.Config) { c.WebPassword = "" })
	res := wiredCall(srv, "POST", "/api/v1/credentials", map[string]any{"name": "x", "permissions": map[string]any{"terminal": true}})
	if res.Code == http.StatusOK {
		t.Fatalf("总闸关闭时不应能创建终端令牌: %d %s", res.Code, res.Raw)
	}
	// 无关令牌不受影响。
	ok := wiredCall(srv, "POST", "/api/v1/credentials", map[string]any{"name": "y", "permissions": map[string]any{"feedback": true}})
	if ok.Code != http.StatusOK {
		t.Fatalf("普通令牌不受影响: %d %s", ok.Code, ok.Raw)
	}
}

func TestCredentialUpdateNeedsUnlockRules(t *testing.T) {
	plain := &model.MCPCredential{Permissions: model.Permissions{Feedback: true}}
	termCred := &model.MCPCredential{Permissions: model.Permissions{Terminal: true}, TerminalHosts: model.StringList{"RM-AAAA-BBBB-CCCC-DDDD"}}
	launcherCred := &model.MCPCredential{Permissions: model.Permissions{Feedback: true, Launcher: true}}
	cases := []struct {
		name   string
		before *model.MCPCredential
		body   map[string]any
		want   bool
	}{
		{"普通令牌改名", plain, map[string]any{"name": "x"}, false},
		{"普通令牌授予终端位", plain, map[string]any{"permissions": map[string]any{"terminal": true}}, true},
		{"普通令牌授予文件位", plain, map[string]any{"permissions": map[string]any{"terminal_files": true}}, true},
		{"普通令牌改其他权限", plain, map[string]any{"permissions": map[string]any{"feedback": true, "sessions": true}}, false},
		{"终端令牌改名", termCred, map[string]any{"name": "x", "note": "y"}, false},
		{"终端令牌停用", termCred, map[string]any{"is_active": false}, false},
		{"终端令牌启用", termCred, map[string]any{"is_active": true}, true},
		{"终端令牌清空主机", termCred, map[string]any{"terminal_hosts": []any{}}, false},
		{"终端令牌加主机", termCred, map[string]any{"terminal_hosts": []any{"RM-1111-2222-3333-4444"}}, true},
		{"终端令牌关闭终端位", termCred, map[string]any{"permissions": map[string]any{"feedback": true}}, false},
		{"终端令牌模式上限", termCred, map[string]any{"terminal_mode_cap": "readonly"}, true},
		{"终端令牌未知字段", termCred, map[string]any{"mystery": 1}, true},
		{"令牌不存在", nil, map[string]any{"name": "x"}, false},
		{"令牌不存在却授予", nil, map[string]any{"permissions": map[string]any{"terminal": true}}, true},
		// 运行预设（#179）能让项目管理 Agent 在主机上拉起一个智能体，授予它与授予终端位同级。
		{"普通令牌授予运行预设位", plain, map[string]any{"permissions": map[string]any{"feedback": true, "launcher": true}}, true},
		{"运行预设令牌改名", launcherCred, map[string]any{"name": "x", "note": "y"}, false},
		{"运行预设令牌停用", launcherCred, map[string]any{"is_active": false}, false},
		{"运行预设令牌启用", launcherCred, map[string]any{"is_active": true}, true},
		{"运行预设令牌关闭该位", launcherCred, map[string]any{"permissions": map[string]any{"feedback": true}}, false},
		{"运行预设令牌延长有效期", launcherCred, map[string]any{"expires_at": "2099-01-01T00:00:00Z"}, true},
	}
	for _, tc := range cases {
		if got := credentialUpdateNeedsUnlock(tc.before, tc.body); got != tc.want {
			t.Errorf("%s: got %v, want %v", tc.name, got, tc.want)
		}
	}
}

// 令牌被删除 / 重新生成 / 停用后，它名下进行中的模型运行必须被取消。
func TestCredentialChangeCancelsItsRuns(t *testing.T) {
	srv, _ := newConvergenceServer(t, nil)
	jwt := wiredLogin(t, srv)
	unlocked := []string{"Authorization", "Bearer " + jwt, "X-Terminal-Unlock", wiredUnlock(t, srv, jwt)}
	created := wiredCall(srv, "POST", "/api/v1/credentials", map[string]any{"name": "runner", "permissions": map[string]any{"terminal": true}}, unlocked...)
	if created.Code != http.StatusOK {
		t.Fatalf("创建: %d %s", created.Code, created.Raw)
	}
	id := credID(t, created)

	// 与 Hub 的真实交互（含假 Agent）在 internal/terminal 的测试里覆盖；这里只验证 API 层的调用链不会 panic，
	// 并且在终端 Hub 存在 / 缺席两种情况下都能完成。
	for _, step := range []struct {
		method, path string
		body         any
	}{
		{"PUT", "/api/v1/credentials/" + id, map[string]any{"is_active": false}},
		{"POST", "/api/v1/credentials/" + id + "/regenerate", nil},
		{"DELETE", "/api/v1/credentials/" + id, nil},
	} {
		res := wiredCall(srv, step.method, step.path, step.body, unlocked...)
		if res.Code != http.StatusOK {
			t.Fatalf("%s %s: %d %s", step.method, step.path, res.Code, res.Raw)
		}
	}
	if _, err := srv.store.GetCredential(context.Background(), 0); err == nil {
		t.Log("(id 0 不应存在)")
	}
}
