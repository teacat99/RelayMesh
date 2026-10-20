package api

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/teacat99/RelayMesh/v2/internal/model"
)

func mustUint(t *testing.T, s string) uint {
	t.Helper()
	n, err := strconv.ParseUint(s, 10, 64)
	if err != nil {
		t.Fatalf("bad id %q: %v", s, err)
	}
	return uint(n)
}

// 「允许整个文件系统」把令牌的文件访问边界整个拆掉，所以：开启要求刚刚重新输入过密码（不只是已解锁），
// 并写审计；关闭、收窄是刹车，只需要普通解锁。

func TestCredentialUpdateGrantsWholeFSRules(t *testing.T) {
	files := model.Permissions{Terminal: true, TerminalFiles: true}
	noFiles := model.Permissions{Terminal: true}
	dirs := model.TerminalLimits{AllowedPaths: []string{"/srv/data"}}
	whole := model.TerminalLimits{AllowWholeFS: true}
	cred := func(p model.Permissions, l model.TerminalLimits) *model.MCPCredential {
		return &model.MCPCredential{ID: 5, Name: "n", Permissions: p, TerminalLimits: l}
	}
	body := func(perms map[string]any, limits map[string]any) map[string]any {
		out := map[string]any{}
		if perms != nil {
			out["permissions"] = perms
		}
		if limits != nil {
			out["terminal_limits"] = limits
		}
		return out
	}
	wholeLimits := map[string]any{"allow_whole_fs": true}
	cases := []struct {
		name   string
		before *model.MCPCredential
		body   map[string]any
		want   bool
	}{
		{"目录令牌 → 打开开关", cred(files, dirs), body(nil, wholeLimits), true},
		{"普通令牌 → 同时授予文件权限与开关", cred(model.Permissions{Feedback: true}, model.TerminalLimits{}),
			body(map[string]any{"terminal_files": true}, wholeLimits), true},
		{"已经生效 → 只改备注", cred(files, whole), map[string]any{"note": "x"}, false},
		{"已经生效 → 保持开关、调整别的限额", cred(files, whole), body(nil, map[string]any{"allow_whole_fs": true, "max_concurrent": 3}), false},
		{"已经生效 → 关闭开关（收窄）", cred(files, whole), body(nil, map[string]any{"allowed_paths": []any{"/srv/data"}}), false},
		{"已经生效 → 关闭文件权限位（收窄）", cred(files, whole), body(map[string]any{"terminal": true}, nil), false},
		{"开关存着、权限位关着 → 单独打开权限位（绕过尝试）", cred(noFiles, whole), body(map[string]any{"terminal": true, "terminal_files": true}, nil), true},
		{"开关存着、权限位关着 → 只改别的", cred(noFiles, whole), map[string]any{"note": "x"}, false},
		{"权限位关着 → 再写一次开关（仍不生效）", cred(noFiles, dirs), body(nil, wholeLimits), false},
		{"目录令牌 → 改目录", cred(files, dirs), body(nil, map[string]any{"allowed_paths": []any{"/srv/other"}}), false},
		{"令牌不存在 → 直接授予", nil, body(map[string]any{"terminal_files": true}, wholeLimits), true},
		{"令牌不存在 → 只给开关没有权限位", nil, body(nil, wholeLimits), false},
		{"permissions 无法解析 → 不构成授予（随后会被 store 当作非法输入拒绝）", cred(files, dirs), map[string]any{"permissions": "oops", "terminal_limits": wholeLimits}, true},
		{"terminal_limits 为 null → 清空，不构成授予", cred(files, whole), map[string]any{"terminal_limits": nil}, false},
	}
	for _, tc := range cases {
		if got := credentialUpdateGrantsWholeFS(tc.before, tc.body); got != tc.want {
			t.Errorf("%s: got %v, want %v", tc.name, got, tc.want)
		}
	}
}

func wholeFSAuditRows(t *testing.T, srv *Server) []model.TerminalAuditLog {
	t.Helper()
	rows, err := srv.store.ListTerminalAudit(context.Background(), 300, 0)
	if err != nil {
		t.Fatalf("读取审计: %v", err)
	}
	var out []model.TerminalAuditLog
	for _, r := range rows {
		if r.Event == "files_scope" {
			out = append(out, r)
		}
	}
	return out
}

func TestWholeFSGrantNeedsAFreshPasswordAndIsAudited(t *testing.T) {
	srv, _ := newConvergenceServer(t, nil)
	hub := srv.Terminal()
	if hub == nil {
		t.Fatal("测试装配里应有终端 Hub")
	}
	jwt := wiredLogin(t, srv)
	login := []string{"Authorization", "Bearer " + jwt}
	unlocked := append(append([]string{}, login...), "X-Terminal-Unlock", wiredUnlock(t, srv, jwt))

	filesCred := func(limits map[string]any, name ...string) map[string]any {
		n := "wholefs"
		if len(name) > 0 {
			n = name[0]
		}
		return map[string]any{
			"name":            n,
			"permissions":     map[string]any{"terminal_files": true},
			"terminal_limits": limits,
		}
	}
	whole := map[string]any{"allow_whole_fs": true}

	// 先建一把普通的目录令牌（只需要普通解锁）。
	created := wiredCall(srv, "POST", "/api/v1/credentials", filesCred(map[string]any{"allowed_paths": []string{"/srv/data"}}), unlocked...)
	if created.Code != http.StatusOK {
		t.Fatalf("目录令牌应能创建: %d %s", created.Code, created.Raw)
	}
	id := credID(t, created)
	if n := len(wholeFSAuditRows(t, srv)); n != 0 {
		t.Fatalf("目录令牌不应产生 files_scope 审计: %d", n)
	}

	t.Run("只凭登录：要求解锁（沿用终端令牌的规则）", func(t *testing.T) {
		res := wiredCall(srv, "PUT", "/api/v1/credentials/"+id, map[string]any{"terminal_limits": whole}, login...)
		if res.Code != http.StatusForbidden || res.str("code") != "unlock_required" {
			t.Fatalf("应 403 unlock_required: %d %s", res.Code, res.Raw)
		}
		res = wiredCall(srv, "POST", "/api/v1/credentials", filesCred(whole), login...)
		if res.Code != http.StatusForbidden || res.str("code") != "unlock_required" {
			t.Fatalf("新建也应 403 unlock_required: %d %s", res.Code, res.Raw)
		}
	})

	t.Run("已解锁但不是刚刚输入的密码：要求重新验证，并写一条被拒绝的审计", func(t *testing.T) {
		hub.SetFreshUnlockWindow(-1) // 模拟「很久以前解锁的令牌」
		defer hub.SetFreshUnlockWindow(0)
		res := wiredCall(srv, "PUT", "/api/v1/credentials/"+id, map[string]any{"terminal_limits": whole}, unlocked...)
		if res.Code != http.StatusForbidden || res.str("code") != "unlock_fresh_required" {
			t.Fatalf("应 403 unlock_fresh_required: %d %s", res.Code, res.Raw)
		}
		res = wiredCall(srv, "POST", "/api/v1/credentials", filesCred(whole), unlocked...)
		if res.Code != http.StatusForbidden || res.str("code") != "unlock_fresh_required" {
			t.Fatalf("新建同样应 403 unlock_fresh_required: %d %s", res.Code, res.Raw)
		}
		// 被拒绝的请求没有改动任何东西
		got, err := srv.store.GetCredential(context.Background(), mustUint(t, id))
		if err != nil || got.TerminalLimits.AllowWholeFS {
			t.Fatalf("被拒绝的请求不得改动令牌: %+v %v", got, err)
		}
		rows := wholeFSAuditRows(t, srv)
		if len(rows) == 0 || rows[0].Result != "deny" || !strings.Contains(rows[0].Detail, "允许整个文件系统") {
			t.Fatalf("被拒绝应写 files_scope 审计: %+v", rows)
		}
		// 同一把（只是不新鲜的）解锁令牌做普通的终端令牌管理仍然有效：不新鲜不等于失效
		if res := wiredCall(srv, "PUT", "/api/v1/credentials/"+id, map[string]any{"note": "还是可以改备注"}, unlocked...); res.Code != http.StatusOK {
			t.Fatalf("不新鲜的解锁令牌仍可做普通管理: %d %s", res.Code, res.Raw)
		}
	})

	t.Run("刚刚重新输入密码：可以开启，写审计", func(t *testing.T) {
		fresh := append(append([]string{}, login...), "X-Terminal-Unlock", wiredUnlock(t, srv, jwt))
		res := wiredCall(srv, "PUT", "/api/v1/credentials/"+id, map[string]any{"terminal_limits": whole}, fresh...)
		if res.Code != http.StatusOK {
			t.Fatalf("刚验证过密码应能开启: %d %s", res.Code, res.Raw)
		}
		got, err := srv.store.GetCredential(context.Background(), mustUint(t, id))
		if err != nil || !got.TerminalLimits.AllowWholeFS || !got.Permissions.TerminalFiles {
			t.Fatalf("开关应已保存: %+v %v", got, err)
		}
		var opened bool
		for _, r := range wholeFSAuditRows(t, srv) {
			if r.Result == "ok" && strings.Contains(r.Detail, "开启") && strings.Contains(r.Detail, "wholefs") {
				opened = true
			}
			if strings.Contains(r.Detail, got.Token) {
				t.Fatalf("审计不得含令牌: %+v", r)
			}
		}
		if !opened {
			t.Fatalf("开启应写 files_scope 审计（含令牌名与动作）: %+v", wholeFSAuditRows(t, srv))
		}
	})

	t.Run("关闭只需要普通解锁（刹车），也写审计", func(t *testing.T) {
		hub.SetFreshUnlockWindow(-1)
		defer hub.SetFreshUnlockWindow(0)
		res := wiredCall(srv, "PUT", "/api/v1/credentials/"+id, map[string]any{"terminal_limits": map[string]any{"allowed_paths": []string{"/srv/data"}}}, unlocked...)
		if res.Code != http.StatusOK {
			t.Fatalf("关闭开关不应要求重新验证密码: %d %s", res.Code, res.Raw)
		}
		got, _ := srv.store.GetCredential(context.Background(), mustUint(t, id))
		if got.TerminalLimits.AllowWholeFS {
			t.Fatalf("开关应已关闭: %+v", got.TerminalLimits)
		}
		var closed bool
		for _, r := range wholeFSAuditRows(t, srv) {
			if r.Result == "ok" && strings.Contains(r.Detail, "关闭") {
				closed = true
			}
		}
		if !closed {
			t.Fatalf("关闭应写审计: %+v", wholeFSAuditRows(t, srv))
		}
	})

	t.Run("绕过尝试：先关掉文件权限位（开关保留），再单独打开权限位", func(t *testing.T) {
		fresh := append(append([]string{}, login...), "X-Terminal-Unlock", wiredUnlock(t, srv, jwt))
		// 重新开启开关（刚验证过密码）
		if res := wiredCall(srv, "PUT", "/api/v1/credentials/"+id, map[string]any{"terminal_limits": whole}, fresh...); res.Code != http.StatusOK {
			t.Fatalf("准备：开启: %d %s", res.Code, res.Raw)
		}
		// 关闭文件权限位：收窄，开关作为设置被保留
		if res := wiredCall(srv, "PUT", "/api/v1/credentials/"+id, map[string]any{"permissions": map[string]any{"feedback": true}}, login...); res.Code != http.StatusOK {
			t.Fatalf("关闭文件权限位只需登录: %d %s", res.Code, res.Raw)
		}
		got, _ := srv.store.GetCredential(context.Background(), mustUint(t, id))
		if !got.TerminalLimits.AllowWholeFS || model.EffectiveWholeFS(got.Permissions, got.TerminalLimits) {
			t.Fatalf("开关应被保留但不生效: %+v %+v", got.Permissions, got.TerminalLimits)
		}
		// 单独把权限位打开：这会让整个文件系统重新生效，所以同样要求刚刚验证过密码
		hub.SetFreshUnlockWindow(-1)
		defer hub.SetFreshUnlockWindow(0)
		reopen := map[string]any{"permissions": map[string]any{"terminal_files": true}}
		res := wiredCall(srv, "PUT", "/api/v1/credentials/"+id, reopen, unlocked...)
		if res.Code != http.StatusForbidden || res.str("code") != "unlock_fresh_required" {
			t.Fatalf("单独打开权限位应要求重新验证密码: %d %s", res.Code, res.Raw)
		}
		hub.SetFreshUnlockWindow(0)
		fresh2 := append(append([]string{}, login...), "X-Terminal-Unlock", wiredUnlock(t, srv, jwt))
		if res := wiredCall(srv, "PUT", "/api/v1/credentials/"+id, reopen, fresh2...); res.Code != http.StatusOK {
			t.Fatalf("刚验证过密码应能重新生效: %d %s", res.Code, res.Raw)
		}
	})

	t.Run("新建时直接带开关", func(t *testing.T) {
		fresh := append(append([]string{}, login...), "X-Terminal-Unlock", wiredUnlock(t, srv, jwt))
		res := wiredCall(srv, "POST", "/api/v1/credentials", filesCred(whole, "wholefs-new"), fresh...)
		if res.Code != http.StatusOK {
			t.Fatalf("刚验证过密码应能新建: %d %s", res.Code, res.Raw)
		}
		cred, _ := res.Body["credential"].(map[string]any)
		lim, _ := cred["terminal_limits"].(map[string]any)
		if lim["allow_whole_fs"] != true {
			t.Fatalf("响应里应回显 allow_whole_fs: %s", res.Raw)
		}
		list := wiredCall(srv, "GET", "/api/v1/credentials", nil, login...)
		if !strings.Contains(list.Raw, `"allow_whole_fs":true`) {
			t.Fatalf("列表里应能读到 allow_whole_fs（前端据此显示红色徽标）: %s", list.Raw)
		}
	})

	t.Run("在目录里写根仍被拒绝，并指向开关", func(t *testing.T) {
		fresh := append(append([]string{}, login...), "X-Terminal-Unlock", wiredUnlock(t, srv, jwt))
		res := wiredCall(srv, "POST", "/api/v1/credentials", filesCred(map[string]any{"allowed_paths": []string{"/"}}, "wholefs-root"), fresh...)
		if res.Code != http.StatusBadRequest || !strings.Contains(res.Raw, "允许整个文件系统") {
			t.Fatalf("`/` 应被拒绝并指向开关: %d %s", res.Code, res.Raw)
		}
	})
}
