package api

import (
	"net/http"
	"testing"
)

// 凭据的「运行预设」位（#179）：解锁之后创建，位被原样存下并在读取时带回；关掉它是收紧，只需登录。
func TestLauncherPermissionRoundTripAndTighten(t *testing.T) {
	srv, _ := newConvergenceServer(t, nil)
	jwt := wiredLogin(t, srv)
	login := []string{"Authorization", "Bearer " + jwt}
	unlocked := append(append([]string{}, login...), "X-Terminal-Unlock", wiredUnlock(t, srv, jwt))

	created := wiredCall(srv, "POST", "/api/v1/credentials",
		map[string]any{"name": "manager-agent", "permissions": map[string]any{"feedback": true, "sessions": true, "launcher": true}}, unlocked...)
	if created.Code != http.StatusOK {
		t.Fatalf("解锁后应能创建带「运行预设」位的令牌: %d %s", created.Code, created.Raw)
	}
	cred, _ := created.Body["credential"].(map[string]any)
	perms, _ := cred["permissions"].(map[string]any)
	if on, _ := perms["launcher"].(bool); !on {
		t.Fatalf("创建响应里应带回 launcher=true: %s", created.Raw)
	}
	id := credID(t, created)

	// 没带这一位的令牌：读回来也是 false（旧凭据没有这个字段，缺省就是关）。
	plain := wiredCall(srv, "POST", "/api/v1/credentials", map[string]any{"name": "plain-agent", "permissions": map[string]any{"feedback": true}}, login...)
	if plain.Code != http.StatusOK {
		t.Fatalf("普通令牌只需登录: %d %s", plain.Code, plain.Raw)
	}
	pc, _ := plain.Body["credential"].(map[string]any)
	pp, _ := pc["permissions"].(map[string]any)
	if on, _ := pp["launcher"].(bool); on {
		t.Fatalf("没有勾选时 launcher 必须是 false: %s", plain.Raw)
	}

	// 只凭登录不能把它打开；关掉是收紧，只需登录。
	if res := wiredCall(srv, "PUT", "/api/v1/credentials/"+credID(t, plain), map[string]any{"permissions": map[string]any{"feedback": true, "launcher": true}}, login...); res.Code != http.StatusForbidden {
		t.Fatalf("只凭登录不能授予「运行预设」位: %d %s", res.Code, res.Raw)
	}
	if res := wiredCall(srv, "PUT", "/api/v1/credentials/"+id, map[string]any{"permissions": map[string]any{"feedback": true, "sessions": true}}, login...); res.Code != http.StatusOK {
		t.Fatalf("关掉「运行预设」位是收紧，只需登录: %d %s", res.Code, res.Raw)
	}
}
