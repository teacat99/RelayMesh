package terminal

import (
	"net/http"
	"testing"
	"time"

	"github.com/teacat99/RelayMesh/v2/internal/store"
	"github.com/teacat99/RelayMesh/v2/internal/termproto"
)

// 终端偏好：宽度保持（登录即可改）与终端限高（需要刚刚解锁、写审计、立刻下发给在线主机）。

func prefsOf(t *testing.T, e *testEnv) map[string]any {
	t.Helper()
	res := e.call("GET", "/api/v1/terminal/status", nil, "Authorization", humanAuth(testUser))
	if res.Status != 200 {
		t.Fatalf("status: %d %s", res.Status, res.Raw)
	}
	p, _ := res.Body["prefs"].(map[string]any)
	if p == nil {
		t.Fatalf("status 应带 prefs: %s", res.Raw)
	}
	return p
}

func TestPrefsDefaultToOffAndAreExposedInStatus(t *testing.T) {
	e := newTestEnv(t)
	p := prefsOf(t, e)
	if p["fixed_cols"] != float64(0) || p["record_max_lines"] != float64(0) {
		t.Fatalf("默认应全部关闭: %v", p)
	}
	lim, _ := p["limits"].(map[string]any)
	if lim["fixed_cols_min"] != float64(store.MinFixedCols) || lim["fixed_cols_max"] != float64(store.MaxFixedCols) ||
		lim["record_max_lines_min"] != float64(store.MinRecordMaxLines) || lim["record_max_lines_max"] != float64(store.MaxRecordMaxLines) {
		t.Fatalf("应带取值范围: %v", lim)
	}
	if _, has := p["record_lines"]; has {
		t.Fatalf("没设行数上限时不应带覆盖情况: %v", p)
	}
}

func TestPrefsDisplayNeedsOnlyLoginAndValidatesRange(t *testing.T) {
	e := newTestEnv(t)
	login := []string{"Authorization", humanAuth(testUser)}

	if res := e.call("PUT", "/api/v1/terminal/prefs/display", map[string]int{"fixed_cols": 120}); res.Status != http.StatusUnauthorized && res.Status != http.StatusForbidden {
		t.Fatalf("未登录不应能修改: %d %s", res.Status, res.Raw)
	}
	res := e.call("PUT", "/api/v1/terminal/prefs/display", map[string]int{"fixed_cols": 120}, login...)
	if res.Status != 200 {
		t.Fatalf("登录即可修改宽度保持: %d %s", res.Status, res.Raw)
	}
	if p := prefsOf(t, e); p["fixed_cols"] != float64(120) {
		t.Fatalf("状态应反映新值: %v", p)
	}
	for _, bad := range []int{1, store.MinFixedCols - 1, store.MaxFixedCols + 1, -3} {
		res := e.call("PUT", "/api/v1/terminal/prefs/display", map[string]int{"fixed_cols": bad}, login...)
		if res.Status != http.StatusBadRequest || res.str("code") != "bad_prefs" {
			t.Fatalf("越界的列数 %d 应 400: %d %s", bad, res.Status, res.Raw)
		}
	}
	if p := prefsOf(t, e); p["fixed_cols"] != float64(120) {
		t.Fatalf("被拒绝的修改不应改动已保存的值: %v", p)
	}
	if res := e.call("PUT", "/api/v1/terminal/prefs/display", map[string]int{"fixed_cols": 0}, login...); res.Status != 200 {
		t.Fatalf("改回 0 即关闭: %d %s", res.Status, res.Raw)
	}
	if p := prefsOf(t, e); p["fixed_cols"] != float64(0) {
		t.Fatalf("应已关闭: %v", p)
	}
	// 宽度保持只影响浏览器，不下发给主机，也不写审计。
	if n := len(e.auditEvents(EvRecordPolicy)); n != 0 {
		t.Fatalf("宽度保持不应产生记录策略审计: %d", n)
	}
}

func TestPrefsRetentionNeedsUnlockAuditsAndPushesToOnlineHosts(t *testing.T) {
	e := newTestEnv(t)
	meta := testMeta("lines-host")
	a := e.enrollAgent(meta, "")
	login := []string{"Authorization", humanAuth(testUser)}

	if a.welcome.Policy.RecMaxLines != 0 {
		t.Fatalf("默认不应带行数上限: %+v", a.welcome.Policy)
	}
	body := map[string]int{"record_max_lines": 20000}
	if res := e.call("PUT", "/api/v1/terminal/prefs/retention", body, login...); res.Status != http.StatusForbidden || res.str("code") != "unlock_required" {
		t.Fatalf("没有解锁不应能修改: %d %s", res.Status, res.Raw)
	}
	if p := prefsOf(t, e); p["record_max_lines"] != float64(0) {
		t.Fatalf("被拒绝的修改不应生效: %v", p)
	}

	hdr := e.admin()
	for _, bad := range []int{1, store.MinRecordMaxLines - 1, store.MaxRecordMaxLines + 1, -1} {
		res := e.call("PUT", "/api/v1/terminal/prefs/retention", map[string]int{"record_max_lines": bad}, hdr...)
		if res.Status != http.StatusBadRequest || res.str("code") != "bad_prefs" {
			t.Fatalf("越界的行数 %d 应 400: %d %s", bad, res.Status, res.Raw)
		}
	}

	if res := e.call("PUT", "/api/v1/terminal/prefs/retention", body, hdr...); res.Status != 200 {
		t.Fatalf("解锁后应可修改: %d %s", res.Status, res.Raw)
	}
	eventually(t, 2*time.Second, "在线主机收到行数上限", func() bool {
		p, ok := lastPolicy(a)
		return ok && p.RecMaxLines == 20000 && p.Terminal
	})
	logs := e.auditEvents(EvRecordPolicy)
	if len(logs) != 1 || logs[0].Detail != "max_lines=20000" {
		t.Fatalf("应有 1 条审计（max_lines=20000）: %+v", logs)
	}
	// 重复设置同样的值：不再审计、不再下发。
	a.mu.Lock()
	pushed := len(a.policies)
	a.mu.Unlock()
	if res := e.call("PUT", "/api/v1/terminal/prefs/retention", body, hdr...); res.Status != 200 {
		t.Fatalf("重复设置: %d %s", res.Status, res.Raw)
	}
	if n := len(e.auditEvents(EvRecordPolicy)); n != 1 {
		t.Fatalf("重复设置不应再审计: %d", n)
	}
	a.mu.Lock()
	again := len(a.policies)
	a.mu.Unlock()
	if again != pushed {
		t.Fatalf("重复设置不应再下发策略: %d → %d", pushed, again)
	}

	// 取消上限：同样下发（让主机回到本机配置），并审计。
	if res := e.call("PUT", "/api/v1/terminal/prefs/retention", map[string]int{"record_max_lines": 0}, hdr...); res.Status != 200 {
		t.Fatalf("取消上限: %d %s", res.Status, res.Raw)
	}
	eventually(t, 2*time.Second, "在线主机收到「无上限」", func() bool {
		p, ok := lastPolicy(a)
		return ok && p.RecMaxLines == 0 && p.Terminal
	})
	if n := len(e.auditEvents(EvRecordPolicy)); n != 2 {
		t.Fatalf("取消上限应再记一条审计: %d", n)
	}
}

func TestPrefsRetentionSurvivesReconnectAndHubRestart(t *testing.T) {
	e := newTestEnv(t)
	meta := testMeta("lines-reconnect")
	a := e.enrollAgent(meta, "")
	hdr := e.admin()
	if res := e.call("PUT", "/api/v1/terminal/prefs/retention", map[string]int{"record_max_lines": 50000}, hdr...); res.Status != 200 {
		t.Fatalf("设置: %d %s", res.Status, res.Raw)
	}
	a.close()
	eventually(t, 2*time.Second, "离线", func() bool { return e.hub.onlineAgent(a.code) == nil })

	// 模拟 Hub 重启：丢掉内存副本，必须从库里读回。
	e.hub.prefs.mu.Lock()
	e.hub.prefs.loaded, e.hub.prefs.v = false, store.TerminalPrefs{}
	e.hub.prefs.mu.Unlock()

	b := e.connectEnabled(a.priv, meta)
	if p := b.welcome.Policy; p.RecMaxLines != 50000 {
		t.Fatalf("重连的 welcome 应带行数上限: %+v", p)
	}
	if p := prefsOf(t, e); p["record_max_lines"] != float64(50000) {
		t.Fatalf("状态应从库里读回: %v", p)
	}
}

func TestPrefsRetentionReportsHoldersThatCannotApplyIt(t *testing.T) {
	e := newTestEnv(t)
	oldHost := e.enrollAgent(testMeta("old-holder"), "")
	newHost := e.enrollAgent(testMeta("new-holder"), "")
	oldHost.send(termproto.Holder{T: termproto.THolder, State: termproto.HolderReady, Persistent: true, Caps: []string{"policy", "drain", "lost"}})
	newHost.send(termproto.Holder{T: termproto.THolder, State: termproto.HolderReady, Persistent: true, Caps: []string{"policy", "drain", "lost", termproto.HolderCapRecLines}})
	eventually(t, 2*time.Second, "两台主机的守护进程状态都已登记", func() bool {
		return oldHost != nil && e.hub.onlineAgent(oldHost.code) != nil && e.hub.onlineAgent(newHost.code) != nil &&
			e.hub.onlineAgent(newHost.code).hasHolderCap(termproto.HolderCapRecLines) &&
			e.hub.onlineAgent(oldHost.code).hasHolderCap("policy")
	})

	if res := e.call("PUT", "/api/v1/terminal/prefs/retention", map[string]int{"record_max_lines": 20000}, e.admin()...); res.Status != 200 {
		t.Fatalf("设置: %d %s", res.Status, res.Raw)
	}
	p := prefsOf(t, e)
	cov, _ := p["record_lines"].(map[string]any)
	if cov["online"] != float64(2) || cov["unsupported"] != float64(1) {
		t.Fatalf("应报告 2 台在线、1 台不支持: %v", p)
	}
}
