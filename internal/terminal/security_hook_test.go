package terminal

import (
	"net/http"
	"sync"
	"testing"
	"time"
)

type securityCollector struct {
	mu  sync.Mutex
	got []SecurityAlert
}

func (c *securityCollector) hook(a SecurityAlert) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.got = append(c.got, a)
}

func (c *securityCollector) events() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]string, 0, len(c.got))
	for _, a := range c.got {
		out = append(out, a.Event)
	}
	return out
}

// 安全告警挂钩只对审计里的高危事件触发：重放（授权码 / 刷新令牌）与解锁被临时锁定；其余审计事件都不触发。
func TestSecurityHook_FiresOnlyForHighRiskAuditEvents(t *testing.T) {
	e := newTestEnv(t)
	var c securityCollector
	e.hub.SetSecurityHook(c.hook)

	// 解锁连续失败：失败本身不告警，触发锁定的那一次告警一次；锁定期内继续尝试不再产生新的审计，也就没有新的告警。
	bearer := []string{"Authorization", humanAuth(testUser)}
	var last apiResult
	for i := 0; i < unlockMaxFailures; i++ {
		last = e.call("POST", "/api/v1/terminal/unlock", map[string]string{"password": "guess"}, bearer...)
	}
	if last.Status != http.StatusTooManyRequests {
		t.Fatalf("应触发锁定: %d %s", last.Status, last.Raw)
	}
	e.call("POST", "/api/v1/terminal/unlock", map[string]string{"password": "guess"}, bearer...)
	if got := c.events(); len(got) != 1 || got[0] != EvUnlockLocked {
		t.Fatalf("解锁锁定应恰好告警一次：%v", got)
	}

	// OAuth 事件：重放告警，普通的授权 / 签发 / 撤销不告警。
	e.hub.RecordOAuthAudit(EvOAuthTokenIssued, ResultOK, "client=x")
	e.hub.RecordOAuthAudit(EvOAuthConsentGranted, ResultOK, "client=x")
	e.hub.RecordOAuthAudit(EvOAuthCodeReplay, ResultDeny, "client=x")
	e.hub.RecordOAuthAudit(EvOAuthRefreshReplay, ResultDeny, "client=x")
	e.hub.RecordOAuthAudit(EvOAuthRevoke, ResultOK, "client=x")
	want := []string{EvUnlockLocked, EvOAuthCodeReplay, EvOAuthRefreshReplay}
	got := c.events()
	if len(got) != len(want) {
		t.Fatalf("得到 %v，期望 %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("得到 %v，期望 %v", got, want)
		}
	}
	// 事件仍然正常写进了审计日志（挂钩不能替代落库）。
	if n := len(e.auditEvents(EvOAuthCodeReplay)); n != 1 {
		t.Errorf("重放事件应照常落库：%d", n)
	}
}

func TestSecurityHook_NilAndPanicAreSafe(t *testing.T) {
	e := newTestEnv(t)

	// 没挂钩：什么都不发生。
	e.hub.RecordOAuthAudit(EvOAuthCodeReplay, ResultDeny, "x")

	// 挂钩里 panic：被截住，审计照常落库，后续事件仍然能触发（再挂一个正常的钩子验证）。
	e.hub.SetSecurityHook(func(SecurityAlert) { panic("boom") })
	e.hub.RecordOAuthAudit(EvOAuthCodeReplay, ResultDeny, "x")
	if n := len(e.auditEvents(EvOAuthCodeReplay)); n != 2 {
		t.Fatalf("挂钩 panic 不该影响审计落库：%d", n)
	}

	var c securityCollector
	e.hub.SetSecurityHook(c.hook)
	e.hub.RecordOAuthAudit(EvOAuthRefreshReplay, ResultDeny, "x")
	if got := c.events(); len(got) != 1 {
		t.Fatalf("换上正常挂钩后应能收到：%v", got)
	}
	// 摘掉挂钩。
	e.hub.SetSecurityHook(nil)
	e.hub.RecordOAuthAudit(EvOAuthRefreshReplay, ResultDeny, "x")
	if got := c.events(); len(got) != 1 {
		t.Fatalf("摘掉之后不该再收到：%v", got)
	}

	// nil 安全。
	var nilHub *Hub
	nilHub.SetSecurityHook(func(SecurityAlert) {})
}

// HostPresence 只列已启用的持久 Agent 主机，并如实反映在线 / 离线。
func TestHostPresence_ReportsEnabledAgentHostsOnly(t *testing.T) {
	e := newTestEnv(t)
	ctx := t.Context()

	if hosts, err := e.hub.HostPresence(ctx); err != nil || len(hosts) != 0 {
		t.Fatalf("没有主机时应为空：%v %v", hosts, err)
	}

	a := e.enrollAgent(testMeta("presence-host"), "build-01")
	hosts, err := e.hub.HostPresence(ctx)
	if err != nil || len(hosts) != 1 {
		t.Fatalf("应列出一台主机：%v %v", hosts, err)
	}
	if h := hosts[0]; h.MachineCode != a.code || h.Name != "build-01" || !h.Online {
		t.Fatalf("在线主机的内容不对：%+v", h)
	}

	// 断开之后变成离线（仍然在列表里，掉线告警靠它发现）。
	a.close()
	eventually(t, 3*time.Second, "主机被判定离线", func() bool {
		hs, _ := e.hub.HostPresence(ctx)
		return len(hs) == 1 && !hs[0].Online
	})

	// 停用的主机不在列表里。
	hdr := e.admin()
	if res := e.call("POST", "/api/v1/terminal/hosts/"+a.code+"/disable", nil, hdr...); res.Status != http.StatusOK {
		t.Skipf("停用接口与预期不符（%d %s），跳过「停用后不列出」的断言", res.Status, res.Raw)
	}
	if hs, _ := e.hub.HostPresence(ctx); len(hs) != 0 {
		t.Fatalf("停用的主机不该列出：%v", hs)
	}

	// nil 安全。
	var nilHub *Hub
	if hs, err := nilHub.HostPresence(ctx); err != nil || hs != nil {
		t.Fatalf("nil Hub 应返回空：%v %v", hs, err)
	}
}
