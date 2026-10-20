package terminal

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/teacat99/RelayMesh/v2/internal/termproto"
)

const sampleCode = "RM-AAAA-BBBB-CCCC-DDDD"
const sampleSID = "0123456789abcdef0123456789abcdef"

func concretePath(path string) string {
	p := strings.ReplaceAll(path, ":code", sampleCode)
	return strings.ReplaceAll(p, ":sid", sampleSID)
}

// 总闸关闭时，除 GET /status 外的每一条终端路由都必须拒绝请求（AC-1）。
// 用路由枚举而不是手写清单：将来新增路由却忘了挂闸门时，这个测试会立刻失败。
func TestEveryTerminalRouteIsClosedWhenGateIsClosed(t *testing.T) {
	cases := []struct {
		name string
		opts []envOption
		prep func(e *testEnv)
		// 无登录头时守卫路由的期望状态：未设密码时直接 403，其余情况先验证身份所以是 401。
		anonStatus int
	}{
		{"未设置密码", nil, func(e *testEnv) { e.creds.set(testUser, "", nil) }, http.StatusForbidden},
		{"JWT 密钥过弱", []envOption{withJWTSecret("short")}, nil, http.StatusUnauthorized},
		{"环境强制关闭", []envOption{withMode(ModeOff)}, nil, http.StatusUnauthorized},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newTestEnv(t, tc.opts...)
			if tc.prep != nil {
				tc.prep(e)
			}

			routes := e.engine.Routes()
			checked := 0
			for _, r := range routes {
				if !strings.HasPrefix(r.Path, "/api/v1/terminal") {
					continue
				}
				if r.Method == http.MethodGet && r.Path == "/api/v1/terminal/status" {
					continue
				}
				checked++
				path := concretePath(r.Path)
				isWS := strings.HasSuffix(r.Path, "/ws")

				anon := e.call(r.Method, path, nil)
				full := e.call(r.Method, path, nil, "Authorization", humanAuth(testUser), unlockHeader, "whatever")

				wantAnon := tc.anonStatus
				if isWS {
					wantAnon = http.StatusForbidden // WebSocket 入口先判总闸，与是否登录无关
				}
				if anon.Status != wantAnon {
					t.Errorf("%s %s 匿名访问: 得到 %d，期望 %d（%s）", r.Method, r.Path, anon.Status, wantAnon, anon.Raw)
				}
				if full.Status != http.StatusForbidden {
					t.Errorf("%s %s 持有登录头访问: 得到 %d，期望 403（%s）", r.Method, r.Path, full.Status, full.Raw)
				}
			}
			if checked < 19 {
				t.Fatalf("路由枚举异常：只检查了 %d 条，终端路由（不含 status）应不少于 19 条", checked)
			}

			// 拒绝不得留下任何副作用：没有主机、没有待启用条目。
			recs, err := e.store.ListTerminalHosts(t.Context())
			if err != nil || len(recs) != 0 {
				t.Fatalf("总闸关闭时不应产生主机记录: %v %d", err, len(recs))
			}
			if n := len(e.hub.pending.snapshot()); n != 0 {
				t.Fatalf("总闸关闭时不应产生待启用条目: %d", n)
			}
		})
	}
}

func TestStatusIsTheOnlyRouteReachableWhenClosed(t *testing.T) {
	t.Run("未设置密码时匿名可读且只报告原因", func(t *testing.T) {
		e := newTestEnv(t)
		e.creds.set(testUser, "", nil)
		res := e.call("GET", "/api/v1/terminal/status", nil)
		if res.Status != 200 || res.Body["enabled"] != false || res.str("reason") != string(ReasonNoPassword) {
			t.Fatalf("状态不符: %d %s", res.Status, res.Raw)
		}
		for _, k := range []string{"discovery", "tls_pin", "unlocked"} {
			if v, ok := res.Body[k]; ok && v != false {
				t.Fatalf("总闸关闭时不得返回 %s: %s", k, res.Raw)
			}
		}
	})
	t.Run("密钥过弱时匿名不可读，登录后报告原因", func(t *testing.T) {
		e := newTestEnv(t, withJWTSecret("short"))
		if res := e.call("GET", "/api/v1/terminal/status", nil); res.Status != http.StatusUnauthorized {
			t.Fatalf("匿名应为 401: %d %s", res.Status, res.Raw)
		}
		res := e.call("GET", "/api/v1/terminal/status", nil, "Authorization", humanAuth(testUser))
		if res.Status != 200 || res.Body["enabled"] != false || res.str("reason") != string(ReasonWeakJWTSecret) {
			t.Fatalf("状态不符: %d %s", res.Status, res.Raw)
		}
	})
	t.Run("开启时返回发现窗口与解锁状态", func(t *testing.T) {
		e := newTestEnv(t)
		res := e.call("GET", "/api/v1/terminal/status", nil, "Authorization", humanAuth(testUser))
		if res.Status != 200 || res.Body["enabled"] != true || res.Body["unlocked"] != false {
			t.Fatalf("状态不符: %d %s", res.Status, res.Raw)
		}
		disc, _ := res.Body["discovery"].(map[string]any)
		if disc == nil || disc["open"] != false {
			t.Fatalf("应返回关闭的发现窗口: %s", res.Raw)
		}
		// 记录与保留策略（只读）：默认不额外限制主机记录，清单与模型运行记录都保留 30 天
		ret, _ := res.Body["retention"].(map[string]any)
		if ret == nil {
			t.Fatalf("总闸开启时应返回 retention: %s", res.Raw)
		}
		if ret["record_off"] != false || ret["record_days"] != float64(0) || ret["index_days"] != float64(30) || ret["run_days"] != float64(30) {
			t.Fatalf("retention 内容不符: %s", res.Raw)
		}
	})
	t.Run("总闸关闭时不返回 retention", func(t *testing.T) {
		e := newTestEnv(t, withJWTSecret("short"))
		res := e.call("GET", "/api/v1/terminal/status", nil, "Authorization", humanAuth(testUser))
		if _, has := res.Body["retention"]; has {
			t.Fatalf("总闸关闭时不应返回 retention: %s", res.Raw)
		}
	})
}

// 有效的 MCP 令牌访问终端 REST 得到明确的 403 mcp_token_forbidden（与普通管理接口一致，便于排查），
// 未知令牌仍是 401。两种拒绝都发生在评估总闸之前，因此总闸开启与否不影响响应——不泄露总闸状态。
func TestValidMCPTokenGetsExplicitForbidden(t *testing.T) {
	for name, opts := range map[string][]envOption{
		"总闸开启": nil,
		"总闸关闭": {withJWTSecret("short")},
	} {
		e := newTestEnv(t, opts...)
		for _, route := range []struct{ method, path string }{
			{"GET", "/api/v1/terminal/status"},
			{"GET", "/api/v1/terminal/hosts"},
			{"POST", "/api/v1/terminal/hosts/RM-AAAA-BBBB-CCCC-DDDD/enable"},
		} {
			res := e.call(route.method, route.path, nil, "Authorization", "Bearer mcp-valid-1", unlockHeader, "x")
			if res.Status != http.StatusForbidden || res.str("code") != "mcp_token_forbidden" {
				t.Errorf("%s %s %s: 有效 MCP 令牌应得 403 mcp_token_forbidden，实际 %d %s", name, route.method, route.path, res.Status, res.Raw)
			}
			res = e.call(route.method, route.path, nil, "Authorization", "Bearer mcp-token-unknown")
			if res.Status != http.StatusUnauthorized || res.str("code") != "auth_required" {
				t.Errorf("%s %s %s: 未知令牌应得 401 auth_required，实际 %d %s", name, route.method, route.path, res.Status, res.Raw)
			}
		}
	}
}

func TestOnlyHumanJWTIsAccepted(t *testing.T) {
	e := newTestEnv(t)
	// MCP 令牌、空令牌、不带 Bearer 前缀、URL 参数形式的令牌都不能通过。
	for name, hdr := range map[string][]string{
		"MCP 令牌":   {"Authorization", "Bearer mcp-token-abcdef"},
		"无前缀":      {"Authorization", "human-admin"},
		"空 Bearer": {"Authorization", "Bearer "},
		"未携带":      nil,
	} {
		res := e.call("GET", "/api/v1/terminal/hosts", nil, hdr...)
		if res.Status != http.StatusUnauthorized {
			t.Errorf("%s: 得到 %d，期望 401", name, res.Status)
		}
	}
	if res := e.call("GET", "/api/v1/terminal/hosts?token=human-admin", nil); res.Status != http.StatusUnauthorized {
		t.Errorf("URL 参数令牌不应被接受: %d", res.Status)
	}
	if res := e.call("GET", "/api/v1/terminal/hosts", nil, "Authorization", humanAuth(testUser)); res.Status != 200 {
		t.Errorf("人类登录应通过: %d %s", res.Status, res.Raw)
	}
}

func TestUnlockFlow(t *testing.T) {
	e := newTestEnv(t)
	bearer := []string{"Authorization", humanAuth(testUser)}

	// 未解锁：高危操作被拒
	res := e.call("POST", "/api/v1/terminal/discovery", nil, bearer...)
	if res.Status != http.StatusForbidden || res.str("code") != "unlock_required" {
		t.Fatalf("未解锁应返回 unlock_required: %d %s", res.Status, res.Raw)
	}

	// 密码错误
	bad := e.call("POST", "/api/v1/terminal/unlock", map[string]string{"password": "nope"}, bearer...)
	// 必须是 403：401 会被前端当成登录过期，输错一次解锁密码就被踢回登录页。
	if bad.Status != http.StatusForbidden || bad.str("code") != "unlock_failed" {
		t.Fatalf("错误密码应返回 403 unlock_failed: %d %s", bad.Status, bad.Raw)
	}

	// 正确密码：令牌可用
	token := e.unlock(testUser)
	hdr := append(append([]string{}, bearer...), unlockHeader, token)
	if res := e.call("POST", "/api/v1/terminal/discovery", nil, hdr...); res.Status != 200 {
		t.Fatalf("解锁后应可操作: %d %s", res.Status, res.Raw)
	}

	// 令牌绑定主体：换一个登录身份使用同一令牌必须失败
	other := []string{"Authorization", humanAuth("mallory"), unlockHeader, token}
	if res := e.call("POST", "/api/v1/terminal/discovery", nil, other...); res.Status != http.StatusForbidden {
		t.Fatalf("令牌不得跨主体使用: %d %s", res.Status, res.Raw)
	}

	// 状态接口能看到解锁状态，且只读查看不顺延空闲期
	st := e.call("GET", "/api/v1/terminal/status", nil, hdr...)
	if st.Body["unlocked"] != true {
		t.Fatalf("状态应显示已解锁: %s", st.Raw)
	}

	// 主动上锁后令牌失效
	if res := e.call("POST", "/api/v1/terminal/lock", nil, hdr...); res.Status != 200 {
		t.Fatalf("上锁失败: %d %s", res.Status, res.Raw)
	}
	if res := e.call("POST", "/api/v1/terminal/discovery", nil, hdr...); res.Status != http.StatusForbidden {
		t.Fatalf("上锁后令牌应失效: %d %s", res.Status, res.Raw)
	}
}

func TestUnlockBruteForceIsThrottled(t *testing.T) {
	e := newTestEnv(t)
	bearer := []string{"Authorization", humanAuth(testUser)}
	var last apiResult
	for i := 0; i < unlockMaxFailures; i++ {
		last = e.call("POST", "/api/v1/terminal/unlock", map[string]string{"password": "guess"}, bearer...)
	}
	if last.Status != http.StatusTooManyRequests || last.str("code") != "unlock_locked" {
		t.Fatalf("第 %d 次失败应触发锁定: %d %s", unlockMaxFailures, last.Status, last.Raw)
	}
	// 锁定期内即使密码正确也必须拒绝
	good := e.call("POST", "/api/v1/terminal/unlock", map[string]string{"password": testPassword}, bearer...)
	if good.Status != http.StatusTooManyRequests {
		t.Fatalf("锁定期内不应接受正确密码: %d %s", good.Status, good.Raw)
	}
	logs, _ := e.store.ListTerminalAudit(t.Context(), 50, 0)
	var fails, locks int
	for _, l := range logs {
		switch l.Event {
		case EvUnlockFail:
			fails++
		case EvUnlockLocked:
			locks++
		}
	}
	if fails != unlockMaxFailures || locks != 1 {
		t.Fatalf("审计应记录 %d 次失败与 1 次锁定，实际 %d / %d", unlockMaxFailures, fails, locks)
	}
}

func TestUnlockTokenExpiresByIdleTime(t *testing.T) {
	e := newTestEnv(t, withFakeClock(), withUnlockTTL(10*time.Minute, time.Hour))
	bearer := []string{"Authorization", humanAuth(testUser)}
	token := e.unlock(testUser)
	hdr := append(append([]string{}, bearer...), unlockHeader, token)

	e.clock.Advance(9 * time.Minute)
	// 状态轮询不得顺延
	if res := e.call("GET", "/api/v1/terminal/status", nil, hdr...); res.Body["unlocked"] != true {
		t.Fatalf("9 分钟时应仍处于解锁状态: %s", res.Raw)
	}
	e.clock.Advance(2 * time.Minute)
	if res := e.call("GET", "/api/v1/terminal/status", nil, hdr...); res.Body["unlocked"] != false {
		t.Fatalf("状态轮询不应顺延空闲期，11 分钟时应已失效: %s", res.Raw)
	}
	if res := e.call("POST", "/api/v1/terminal/discovery", nil, hdr...); res.Status != http.StatusForbidden {
		t.Fatalf("过期令牌应被拒绝: %d", res.Status)
	}
}

func TestUnlockKeepaliveExtendsIdleButNotAbsolute(t *testing.T) {
	e := newTestEnv(t, withFakeClock(), withUnlockTTL(10*time.Minute, 30*time.Minute))
	bearer := []string{"Authorization", humanAuth(testUser)}
	token := e.unlock(testUser)
	hdr := append(append([]string{}, bearer...), unlockHeader, token)

	// 没有解锁令牌 / 令牌不对：不能用来「保活」。
	if res := e.call("POST", "/api/v1/terminal/unlock/keepalive", nil, bearer...); res.Status != http.StatusForbidden || res.str("code") != "unlock_required" {
		t.Fatalf("无令牌应被拒绝: %d %s", res.Status, res.Raw)
	}
	if res := e.call("POST", "/api/v1/terminal/unlock/keepalive", nil, append(append([]string{}, bearer...), unlockHeader, "not-a-token")...); res.Status != http.StatusForbidden {
		t.Fatalf("错误令牌应被拒绝: %d", res.Status)
	}

	// 每 8 分钟保活一次：总共 24 分钟（远超 10 分钟空闲期）仍然有效。
	for i := 0; i < 3; i++ {
		e.clock.Advance(8 * time.Minute)
		res := e.call("POST", "/api/v1/terminal/unlock/keepalive", nil, hdr...)
		if res.Status != http.StatusOK {
			t.Fatalf("第 %d 次保活失败: %d %s", i+1, res.Status, res.Raw)
		}
		if rem, _ := res.Body["remaining_seconds"].(float64); rem <= 0 {
			t.Fatalf("应返回剩余秒数: %s", res.Raw)
		}
	}
	if res := e.call("GET", "/api/v1/terminal/status", nil, hdr...); res.Body["unlocked"] != true {
		t.Fatalf("保活后应仍处于解锁状态: %s", res.Raw)
	}

	// 绝对期限（30 分钟）不会被保活顺延：第 32 分钟起必须重新输入密码。
	e.clock.Advance(8 * time.Minute)
	if res := e.call("POST", "/api/v1/terminal/unlock/keepalive", nil, hdr...); res.Status != http.StatusForbidden {
		t.Fatalf("超过绝对期限后保活必须失败: %d %s", res.Status, res.Raw)
	}
}

func TestStatusReportsHTTPSPortAndPinOnlyToLoggedInUsers(t *testing.T) {
	e := newTestEnv(t)
	e.hub.SetTLSPin("sha256:" + strings.Repeat("ab", 32))
	e.hub.SetHTTPSPort(18776)

	res := e.call("GET", "/api/v1/terminal/status", nil, "Authorization", humanAuth(testUser))
	if res.Status != http.StatusOK || res.Body["https_port"] != float64(18776) || !strings.HasPrefix(res.str("tls_pin"), "sha256:") {
		t.Fatalf("登录用户应看到 https_port 与 tls_pin: %d %s", res.Status, res.Raw)
	}
	// 匿名请求在总闸开启时不得看到任何配置细节。
	if res := e.call("GET", "/api/v1/terminal/status", nil); res.Status != http.StatusUnauthorized || strings.Contains(res.Raw, "18776") || strings.Contains(res.Raw, "sha256") {
		t.Fatalf("匿名访问不应泄露配置: %d %s", res.Status, res.Raw)
	}
}

func TestAuditDoesNotContainSecrets(t *testing.T) {
	e := newTestEnv(t)
	hdr := e.admin()
	a := e.enrollAgent(testMeta("host-a"), "备注-a")
	sid := e.openSession(a, hdr, "s1")
	ticket := e.ticket(a, sid, hdr, 0)
	_ = e.call("POST", "/api/v1/terminal/unlock", map[string]string{"password": "wrong-password-xyz"}, "Authorization", humanAuth(testUser))

	secrets := []string{testPassword, "wrong-password-xyz", ticket, hdr[3]}
	logs, err := e.store.ListTerminalAudit(t.Context(), 500, 0)
	if err != nil || len(logs) == 0 {
		t.Fatalf("应有审计记录: %v %d", err, len(logs))
	}
	for _, l := range logs {
		blob := strings.Join([]string{l.Event, l.Result, l.Actor, l.IP, l.MachineCode, l.SessionID, l.Detail}, "|")
		for _, s := range secrets {
			if strings.Contains(blob, s) {
				t.Fatalf("审计日志泄露了敏感值 %q: %+v", s[:4]+"…", l)
			}
		}
	}
	_ = termproto.MaxPending
}
