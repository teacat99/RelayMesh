package api

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/teacat99/RelayMesh/v2/internal/config"
	"github.com/teacat99/RelayMesh/v2/internal/model"
)

// revealFixture 在库里建一把令牌，返回编号与明文令牌。
func revealFixture(t *testing.T, srv *Server, name string) (uint, string) {
	t.Helper()
	cred := &model.MCPCredential{Name: name, Permissions: model.AllPermissions()}
	if err := srv.store.CreateCredential(context.Background(), cred); err != nil {
		t.Fatalf("创建凭据: %v", err)
	}
	return cred.ID, cred.Token
}

func revealPath(id uint) string { return fmt.Sprintf("/api/v1/credentials/%d/reveal", id) }

func revealAuditRows(t *testing.T, srv *Server) []model.TerminalAuditLog {
	t.Helper()
	rows, err := srv.store.ListTerminalAudit(context.Background(), 200, 0)
	if err != nil {
		t.Fatalf("读取审计: %v", err)
	}
	var out []model.TerminalAuditLog
	for _, r := range rows {
		if r.Event == "credential_reveal" {
			out = append(out, r)
		}
	}
	return out
}

// 看旧令牌必须在登录之外再给一次 Web 密码：缺少 / 错误一律 403 且不泄漏令牌，正确才返回明文；
// 列表接口始终只给掩码。
func TestRevealCredentialTokenNeedsPassword(t *testing.T) {
	srv := newWiredServer(t, nil)
	jwt := wiredLogin(t, srv)
	login := []string{"Authorization", "Bearer " + jwt}
	id, token := revealFixture(t, srv, "reveal-me")

	list := wiredCall(srv, "GET", "/api/v1/credentials", nil, login...)
	if list.Code != http.StatusOK || strings.Contains(list.Raw, token) {
		t.Fatalf("列表只能给掩码，不能含完整令牌: %d", list.Code)
	}

	for name, body := range map[string]map[string]string{
		"密码错误": {"password": "definitely-wrong"},
		"空密码":  {"password": ""},
		"缺字段":  {},
	} {
		res := wiredCall(srv, "POST", revealPath(id), body, login...)
		if res.Code != http.StatusForbidden || res.str("code") != "reveal_bad_password" {
			t.Errorf("%s：应 403 reveal_bad_password，得到 %d %s", name, res.Code, res.Raw)
		}
		// 绝不能是 401：401 会被前端当成「登录过期」把人踢回登录页。
		if res.Code == http.StatusUnauthorized {
			t.Errorf("%s：不得返回 401", name)
		}
		if strings.Contains(res.Raw, token) {
			t.Errorf("%s：响应里出现了令牌明文", name)
		}
	}

	ok := wiredCall(srv, "POST", revealPath(id), map[string]string{"password": wiredPassword}, login...)
	if ok.Code != http.StatusOK || ok.str("token") != token {
		t.Fatalf("正确密码应返回令牌明文: %d %s", ok.Code, ok.Raw)
	}
	if ok.str("capability_path") != "" {
		t.Errorf("普通令牌不应带能力网址路径: %s", ok.Raw)
	}
}

// 令牌明文只在这一个响应里出现，必须禁止缓存；错误响应同样不缓存。
func TestRevealCredentialTokenResponsesAreNotCacheable(t *testing.T) {
	srv := newWiredServer(t, nil)
	jwt := wiredLogin(t, srv)
	id, _ := revealFixture(t, srv, "no-cache")

	for name, pw := range map[string]string{"成功": wiredPassword, "失败": "wrong"} {
		req := httptest.NewRequest(http.MethodPost, revealPath(id), strings.NewReader(fmt.Sprintf(`{"password":%q}`, pw)))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+jwt)
		w := httptest.NewRecorder()
		srv.Engine().ServeHTTP(w, req)
		if cc := w.Header().Get("Cache-Control"); cc != "no-store" {
			t.Errorf("%s：Cache-Control 应为 no-store，得到 %q", name, cc)
		}
	}
}

// 只有人类登录态能到这里：未登录 401，任何一种 MCP 令牌（库内 / 环境变量）都是 403。
func TestRevealCredentialTokenOnlyForHumanLogin(t *testing.T) {
	srv := newWiredServer(t, nil)
	id, token := revealFixture(t, srv, "human-only")
	body := map[string]string{"password": wiredPassword}

	if res := wiredCall(srv, "POST", revealPath(id), body); res.Code != http.StatusUnauthorized {
		t.Errorf("未登录应 401，得到 %d %s", res.Code, res.Raw)
	}
	for name, tok := range map[string]string{"库内令牌": token, "环境变量令牌": wiredMCPToken} {
		res := wiredCall(srv, "POST", revealPath(id), body, "Authorization", "Bearer "+tok)
		if res.Code != http.StatusForbidden || res.str("code") != "mcp_token_forbidden" {
			t.Errorf("%s不得查看令牌：应 403 mcp_token_forbidden，得到 %d %s", name, res.Code, res.Raw)
		}
		if strings.Contains(res.Raw, token) {
			t.Errorf("%s：响应里出现了令牌明文", name)
		}
	}
}

// 密码错误与登录共用同一份按 IP 的反爆破额度：累计到上限后锁定，此后连正确密码都不接受，登录同样被锁。
func TestRevealCredentialTokenLockoutSharesLoginBudget(t *testing.T) {
	srv := newWiredServer(t, nil)
	jwt := wiredLogin(t, srv)
	login := []string{"Authorization", "Bearer " + jwt}
	id, token := revealFixture(t, srv, "lockout")

	for i := 1; i <= 5; i++ {
		res := wiredCall(srv, "POST", revealPath(id), map[string]string{"password": "wrong"}, login...)
		want := http.StatusForbidden
		if i == 5 {
			want = http.StatusTooManyRequests
		}
		if res.Code != want {
			t.Fatalf("第 %d 次错误：应 %d，得到 %d %s", i, want, res.Code, res.Raw)
		}
	}

	locked := wiredCall(srv, "POST", revealPath(id), map[string]string{"password": wiredPassword}, login...)
	if locked.Code != http.StatusTooManyRequests || locked.str("code") != "reveal_locked" || strings.Contains(locked.Raw, token) {
		t.Fatalf("被锁定后不应再接受正确密码: %d %s", locked.Code, locked.Raw)
	}

	loginAgain := wiredCall(srv, "POST", "/api/v1/auth/login", map[string]string{"username": "admin", "password": wiredPassword})
	if loginAgain.Code != http.StatusTooManyRequests {
		t.Fatalf("同一来源的登录应共用锁定状态: %d %s", loginAgain.Code, loginAgain.Raw)
	}
}

// 输对密码会清掉此前的失败计数，不会因为「偶尔输错几次」累积成锁定。
func TestRevealCredentialTokenSuccessResetsFailures(t *testing.T) {
	srv := newWiredServer(t, nil)
	jwt := wiredLogin(t, srv)
	login := []string{"Authorization", "Bearer " + jwt}
	id, _ := revealFixture(t, srv, "reset")

	for round := 0; round < 3; round++ {
		for i := 0; i < 4; i++ {
			if res := wiredCall(srv, "POST", revealPath(id), map[string]string{"password": "wrong"}, login...); res.Code != http.StatusForbidden {
				t.Fatalf("第 %d 轮第 %d 次错误应 403，得到 %d", round, i+1, res.Code)
			}
		}
		if res := wiredCall(srv, "POST", revealPath(id), map[string]string{"password": wiredPassword}, login...); res.Code != http.StatusOK {
			t.Fatalf("第 %d 轮正确密码应 200，得到 %d %s", round, res.Code, res.Raw)
		}
	}
}

// 密码先于存在性校验：编号不存在时，错误密码也只得到 403，不能当作「探测编号」的口子；
// 密码正确才如实说「不存在」；编号不是数字是 400。
func TestRevealCredentialTokenUnknownID(t *testing.T) {
	srv := newWiredServer(t, nil)
	jwt := wiredLogin(t, srv)
	login := []string{"Authorization", "Bearer " + jwt}

	if res := wiredCall(srv, "POST", revealPath(9999), map[string]string{"password": "wrong"}, login...); res.Code != http.StatusForbidden {
		t.Errorf("不存在的编号 + 错误密码：应 403，得到 %d %s", res.Code, res.Raw)
	}
	if res := wiredCall(srv, "POST", revealPath(9999), map[string]string{"password": wiredPassword}, login...); res.Code != http.StatusNotFound || res.str("code") != "credential_not_found" {
		t.Errorf("不存在的编号 + 正确密码：应 404，得到 %d %s", res.Code, res.Raw)
	}
	if res := wiredCall(srv, "POST", "/api/v1/credentials/abc/reveal", map[string]string{"password": wiredPassword}, login...); res.Code != http.StatusBadRequest {
		t.Errorf("编号不是数字：应 400，得到 %d %s", res.Code, res.Raw)
	}
	if res := wiredCall(srv, "POST", revealPath(1), nil, login...); res.Code != http.StatusBadRequest {
		t.Errorf("没有请求体：应 400，得到 %d %s", res.Code, res.Raw)
	}
}

// 每次尝试都留审计，且审计里没有令牌也没有密码，只有谁、哪个令牌、结果。
func TestRevealCredentialTokenIsAuditedWithoutSecrets(t *testing.T) {
	srv := newWiredServer(t, nil)
	jwt := wiredLogin(t, srv)
	login := []string{"Authorization", "Bearer " + jwt}
	id, token := revealFixture(t, srv, "audited-token")

	const wrong = "wrong-password-for-audit-9"
	wiredCall(srv, "POST", revealPath(id), map[string]string{"password": wrong}, login...)
	wiredCall(srv, "POST", revealPath(id), map[string]string{"password": wiredPassword}, login...)

	rows := revealAuditRows(t, srv)
	var okRow, failRow *model.TerminalAuditLog
	for i := range rows {
		switch rows[i].Result {
		case "ok":
			okRow = &rows[i]
		case "fail":
			failRow = &rows[i]
		}
	}
	if okRow == nil || failRow == nil {
		t.Fatalf("应有一条成功和一条失败审计，得到 %+v", rows)
	}
	if okRow.Actor != "admin" || !strings.Contains(okRow.Detail, "audited-token") {
		t.Errorf("成功审计应记录操作者与令牌名: %+v", *okRow)
	}
	for _, r := range rows {
		for _, secret := range []string{token, wrong, wiredPassword} {
			if strings.Contains(r.Detail, secret) || strings.Contains(r.Actor, secret) {
				t.Errorf("审计里出现了令牌或密码: %+v", r)
			}
		}
	}
}

// 被锁定同样入审计（deny），攻击者无法在审计里「静默」试探。
func TestRevealCredentialTokenLockoutIsAudited(t *testing.T) {
	srv := newWiredServer(t, nil)
	jwt := wiredLogin(t, srv)
	login := []string{"Authorization", "Bearer " + jwt}
	id, _ := revealFixture(t, srv, "audit-lock")

	for i := 0; i < 6; i++ {
		wiredCall(srv, "POST", revealPath(id), map[string]string{"password": "wrong"}, login...)
	}
	denied := 0
	for _, r := range revealAuditRows(t, srv) {
		if r.Result == "deny" {
			denied++
		}
	}
	if denied == 0 {
		t.Fatalf("锁定应记一条 deny 审计")
	}
}

// 开放模式（没有设置 Web 密码）没有可验证的对象，直接放行；审计里标明是免验证。
func TestRevealCredentialTokenOpenMode(t *testing.T) {
	srv := newWiredServer(t, func(c *config.Config) { c.WebPassword = "" })
	id, token := revealFixture(t, srv, "open-mode")

	res := wiredCall(srv, "POST", revealPath(id), nil)
	if res.Code != http.StatusOK || res.str("token") != token {
		t.Fatalf("开放模式应直接返回令牌: %d %s", res.Code, res.Raw)
	}
	rows := revealAuditRows(t, srv)
	if len(rows) != 1 || !strings.Contains(rows[0].Detail, "未设置登录密码") {
		t.Fatalf("开放模式的审计应标明免验证: %+v", rows)
	}
}

// 能力 URL 凭据：令牌本身是网址的一部分，查看时一并给出网址路径，前端据此补全完整网址。
func TestRevealCapabilityCredentialReturnsPath(t *testing.T) {
	srv := newWiredServer(t, nil)
	jwt := wiredLogin(t, srv)
	login := []string{"Authorization", "Bearer " + jwt}

	cred := &model.MCPCredential{
		Name:          "cap-reveal",
		Capability:    true,
		Permissions:   model.Permissions{Terminal: true},
		TerminalHosts: []string{capAPIHost},
	}
	if err := srv.store.CreateCredential(context.Background(), cred); err != nil {
		t.Fatalf("创建能力凭据: %v", err)
	}

	res := wiredCall(srv, "POST", revealPath(cred.ID), map[string]string{"password": wiredPassword}, login...)
	if res.Code != http.StatusOK || res.str("token") != cred.Token || res.str("capability_path") != "/mcp/c/"+cred.Token {
		t.Fatalf("应返回令牌与能力网址路径: %d %s", res.Code, res.Raw)
	}
}

// 用户要求长期兼容的两种 MCP 写法：纯 URL（?token=）与 Authorization 头，对库内（每机器一把）令牌都成立，
// 并且与 RELAYMESH_ALLOW_QUERY_TOKEN 无关——该开关只管 Web 登录令牌能否走 SSE 的 ?token=，从不影响 /mcp。
func TestMCPEndpointKeepsBothTokenFormsForDBCredentials(t *testing.T) {
	for _, allowQuery := range []bool{true, false} {
		t.Run(fmt.Sprintf("allow_query_token=%t", allowQuery), func(t *testing.T) {
			srv := newWiredServer(t, func(c *config.Config) { c.AllowQueryToken = allowQuery })
			_, token := revealFixture(t, srv, "per-machine")

			if res := capToolsListCall(srv, "/mcp?token="+token); res.Code != http.StatusOK {
				t.Errorf("纯 URL ?token= 写法应可用: %d %s", res.Code, res.Raw)
			}
			if res := capToolsListCall(srv, "/mcp", "Authorization", "Bearer "+token); res.Code != http.StatusOK {
				t.Errorf("Authorization 头写法应可用: %d %s", res.Code, res.Raw)
			}
			if res := capToolsListCall(srv, "/mcp"); res.Code != http.StatusUnauthorized {
				t.Errorf("不带令牌应 401: %d", res.Code)
			}
			if res := capToolsListCall(srv, "/mcp?token="+token+"x"); res.Code != http.StatusUnauthorized {
				t.Errorf("错误令牌应 401: %d", res.Code)
			}
			if res := capToolsListCall(srv, "/mcp", "Authorization", "Bearer "+token+"x"); res.Code != http.StatusUnauthorized {
				t.Errorf("错误的头令牌应 401: %d", res.Code)
			}
		})
	}
}
