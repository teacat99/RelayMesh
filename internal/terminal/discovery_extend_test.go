package terminal

import (
	"net/http"
	"testing"
	"time"
)

// 发现窗口「续期」：在现有剩余时间上累加，而不是重置；纪元不变，已经发出去的安装票据继续有效。

func (e *testEnv) discoveryRemaining(res apiResult) int {
	e.t.Helper()
	if res.Status != http.StatusOK {
		e.t.Fatalf("发现窗口请求失败: %d %s", res.Status, res.Raw)
	}
	d, _ := res.Body["discovery"].(map[string]any)
	if d == nil {
		e.t.Fatalf("响应里应有 discovery: %s", res.Raw)
	}
	rem, _ := d["remaining_seconds"].(float64)
	return int(rem)
}

func TestDiscoveryExtendAddsToRemaining(t *testing.T) {
	e := newTestEnv(t, withFakeClock())
	e.openDiscovery(e.admin()) // 5 分钟
	e.clock.Advance(3 * time.Minute)

	res := e.call("POST", "/api/v1/terminal/discovery", map[string]any{"minutes": 10, "extend": true}, e.admin()...)
	// 剩 2 分钟 + 续 10 分钟 = 12 分钟；「重置」语义下只会是 10 分钟。
	if got := e.discoveryRemaining(res); got != 12*60 {
		t.Fatalf("续期后剩余应为 12 分钟（720 秒），实际 %d 秒", got)
	}

	// 对比：不带 extend 的「开启」是重置为 10 分钟，不是累加。
	res = e.call("POST", "/api/v1/terminal/discovery", map[string]any{"minutes": 10}, e.admin()...)
	if got := e.discoveryRemaining(res); got != 10*60 {
		t.Fatalf("普通开启应重置为 10 分钟（600 秒），实际 %d 秒", got)
	}
}

func TestDiscoveryExtendIsCappedAtMaxWindow(t *testing.T) {
	e := newTestEnv(t, withFakeClock())
	e.openDiscovery(e.admin())
	for i := 0; i < 20; i++ {
		res := e.call("POST", "/api/v1/terminal/discovery", map[string]any{"minutes": 30, "extend": true}, e.admin()...)
		if got := e.discoveryRemaining(res); got > int(MaxDiscoveryWindow/time.Second) {
			t.Fatalf("第 %d 次续期后剩余 %d 秒，超过了上限 %d 秒", i+1, got, int(MaxDiscoveryWindow/time.Second))
		}
	}
	res := e.call("POST", "/api/v1/terminal/discovery", map[string]any{"minutes": 1, "extend": true}, e.admin()...)
	if got := e.discoveryRemaining(res); got != int(MaxDiscoveryWindow/time.Second) {
		t.Fatalf("已到上限时再续期应保持在上限，实际 %d 秒", got)
	}
}

func TestDiscoveryExtendKeepsInstallTicket(t *testing.T) {
	dir, _, _ := newDistDir(t)
	e := newTestEnv(t, withAgentDist(dir), withFakeClock())
	ticket := e.openWindow() // 5 分钟

	e.clock.Advance(4 * time.Minute)
	if res := e.call("POST", "/api/v1/terminal/discovery", map[string]any{"minutes": 10, "extend": true}, e.admin()...); res.Status != http.StatusOK {
		t.Fatalf("续期失败: %d %s", res.Status, res.Raw)
	}
	if again, _ := e.installStatus()["ticket"].(string); again != ticket {
		t.Fatal("续期不应换发安装票据")
	}
	// 原窗口早就该到期了（4 分钟 + 3 分钟 > 5 分钟），续期后票据仍然可用。
	e.clock.Advance(3 * time.Minute)
	if st, _, _ := e.callRaw("GET", "/i/"+ticket+"/install.sh"); st != http.StatusOK {
		t.Fatalf("续期后票据应继续可用，实际 %d", st)
	}
	// 续期也有尽头：再过 20 分钟窗口到期，票据失效。
	e.clock.Advance(20 * time.Minute)
	if st, _, _ := e.callRaw("GET", "/i/"+ticket+"/install.sh"); st != http.StatusNotFound {
		t.Fatalf("续期的窗口到期后票据应失效，实际 %d", st)
	}
}

func TestDiscoveryExtendOnClosedWindowOpensIt(t *testing.T) {
	e := newTestEnv(t, withFakeClock())
	res := e.call("POST", "/api/v1/terminal/discovery", map[string]any{"minutes": 7, "extend": true}, e.admin()...)
	if got := e.discoveryRemaining(res); got != 7*60 {
		t.Fatalf("窗口没开着时续期应等同于开启 7 分钟，实际 %d 秒", got)
	}

	// 已经到期的窗口也一样：重新开启，而不是在过期时间上累加。
	e.clock.Advance(30 * time.Minute)
	res = e.call("POST", "/api/v1/terminal/discovery", map[string]any{"minutes": 7, "extend": true}, e.admin()...)
	if got := e.discoveryRemaining(res); got != 7*60 {
		t.Fatalf("到期后续期应重新开启 7 分钟，实际 %d 秒", got)
	}
}

func TestDiscoveryExtendRequiresUnlock(t *testing.T) {
	e := newTestEnv(t)
	res := e.call("POST", "/api/v1/terminal/discovery", map[string]any{"minutes": 10, "extend": true}, "Authorization", humanAuth(testUser))
	if res.Status == http.StatusOK {
		t.Fatal("续期和开启一样需要解锁令牌，仅登录不应通过")
	}
}
