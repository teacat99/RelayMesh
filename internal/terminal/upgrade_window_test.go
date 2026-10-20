package terminal

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// ---------- 注册表本身 ----------

func TestUpgradeWindowsLifecycle(t *testing.T) {
	var u upgradeWindows
	now := time.Unix(1_800_000_000, 0)
	const code = "RM-AAAA-BBBB-CCCC-DDDD"

	if u.view(code, now) != nil {
		t.Fatal("没开窗口时不应有视图")
	}
	v := u.open(code, "v1.6.0", now, 0)
	if !ticketPattern.MatchString(v.Ticket) || v.RemainingSeconds != 600 || v.BaseVersion != "v1.6.0" {
		t.Fatalf("默认应开 10 分钟并带 32 位十六进制票据: %+v", v)
	}
	// 重复开启保持原票据（已经复制出去的命令继续可用），剩余时间补到至少 dur。
	later := now.Add(8 * time.Minute)
	again := u.open(code, "v1.6.0", later, 10*time.Minute)
	if again.Ticket != v.Ticket || again.RemainingSeconds != 600 {
		t.Fatalf("重复开启应保持票据并补足剩余时间: %+v", again)
	}
	// 延时是在现有剩余上累加，总剩余不超过上限。
	ext, ok := u.extend(code, later, 10*time.Minute)
	if !ok || ext.RemainingSeconds != 1200 || ext.Ticket != v.Ticket {
		t.Fatalf("延时后应剩 20 分钟: %+v ok=%v", ext, ok)
	}
	for i := 0; i < 10; i++ {
		ext, _ = u.extend(code, later, 30*time.Minute)
	}
	if ext.RemainingSeconds != int(MaxUpgradeWindow/time.Second) {
		t.Fatalf("剩余时间不应超过上限: %d", ext.RemainingSeconds)
	}
	if _, ok := u.extend("RM-NONE-NONE-NONE-NONE", later, time.Minute); ok {
		t.Fatal("没有窗口时延时应失败")
	}
	// 结束。
	if !u.close(code, later) || u.view(code, later) != nil {
		t.Fatal("结束后窗口应消失")
	}
	if u.close(code, later) {
		t.Fatal("重复结束应返回 false")
	}
}

func TestUpgradeWindowsTicketLookup(t *testing.T) {
	var u upgradeWindows
	now := time.Unix(1_800_000_000, 0)
	a := u.open("RM-AAAA-AAAA-AAAA-AAAA", "v1", now, time.Minute)
	b := u.open("RM-BBBB-BBBB-BBBB-BBBB", "v1", now, 5*time.Minute)
	if a.Ticket == b.Ticket {
		t.Fatal("不同主机的票据必须不同")
	}
	if code, ok := u.ticketHost(a.Ticket, now); !ok || code != "RM-AAAA-AAAA-AAAA-AAAA" {
		t.Fatalf("应识别 A 的票据: %q %v", code, ok)
	}
	if code, ok := u.ticketHost(b.Ticket, now); !ok || code != "RM-BBBB-BBBB-BBBB-BBBB" {
		t.Fatalf("应识别 B 的票据: %q %v", code, ok)
	}
	for _, bad := range []string{"", "x", strings.Repeat("0", 32), a.Ticket + "0", a.Ticket[:31]} {
		if _, ok := u.ticketHost(bad, now); ok {
			t.Errorf("%q 不应通过", bad)
		}
	}
	// 到期后票据立刻失效，哪怕还没被清扫。
	if _, ok := u.ticketHost(a.Ticket, now.Add(61*time.Second)); ok {
		t.Fatal("到期的票据不应通过")
	}
	if _, ok := u.ticketHost(b.Ticket, now.Add(61*time.Second)); !ok {
		t.Fatal("没到期的票据应继续有效")
	}
	// 清扫只收尾到期的，且每个只报告一次。
	if got := u.sweep(now.Add(61 * time.Second)); len(got) != 1 || got[0] != "RM-AAAA-AAAA-AAAA-AAAA" {
		t.Fatalf("应只清扫到期的 A: %v", got)
	}
	if got := u.sweep(now.Add(61 * time.Second)); len(got) != 0 {
		t.Fatalf("同一个窗口不应被报告两次: %v", got)
	}
}

func TestUpgradeWindowsObserve(t *testing.T) {
	var u upgradeWindows
	now := time.Unix(1_800_000_000, 0)
	const code = "RM-AAAA-BBBB-CCCC-DDDD"
	if _, closed := u.observe(code, "v2", now); closed {
		t.Fatal("没有窗口时不应有动作")
	}
	u.open(code, "v1", now, 10*time.Minute)
	if _, closed := u.observe(code, "v1", now); closed {
		t.Fatal("版本没变不算升级")
	}
	if _, closed := u.observe(code, "", now); closed {
		t.Fatal("版本为空不算升级")
	}
	from, closed := u.observe(code, "v2", now)
	if !closed || from != "v1" {
		t.Fatalf("版本变了应关闭窗口并报告旧版本: %q %v", from, closed)
	}
	if u.view(code, now) != nil {
		t.Fatal("关闭之后不应还有窗口")
	}
}

// ---------- 通过 REST 与 /i/ ----------

func writeDistVersion(t *testing.T, dir, v string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "VERSION"), []byte(v+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func upgradeWindowOf(t *testing.T, e *testEnv, code string) map[string]any {
	t.Helper()
	w, _ := hostViewOf(t, e, code)["upgrade_window"].(map[string]any)
	return w
}

// 升级提醒：服务端自带版本与主机版本不同时在主机视图里提示；VERSION 不是可分发的文件。
func TestHostViewShowsUpgradeReminder(t *testing.T) {
	dir, _, _ := newDistDir(t)
	writeDistVersion(t, dir, "v1.7.0")
	e := newTestEnv(t, withAgentDist(dir))

	old := testMeta("old-box")
	old.Ver = "v1.6.0"
	a := e.enrollAgent(old, "旧版本")
	v := hostViewOf(t, e, a.code)
	if v["latest_agent_version"] != "v1.7.0" || v["upgrade_available"] != true || v["upgrade_kind"] != "older" {
		t.Fatalf("旧版本应被提醒: %v", v)
	}

	hash := testMeta("hash-box")
	hash.Ver = "5c23d7b"
	b := e.enrollAgent(hash, "短哈希")
	v = hostViewOf(t, e, b.code)
	if v["upgrade_available"] != true || v["upgrade_kind"] != "different" {
		t.Fatalf("短哈希版本应提示「不同」: %v", v)
	}

	cur := testMeta("new-box")
	cur.Ver = "v1.7.0"
	c := e.enrollAgent(cur, "已是最新")
	v = hostViewOf(t, e, c.code)
	if _, has := v["upgrade_available"]; has {
		t.Fatalf("已是最新的主机不应被提醒: %v", v)
	}
	if v["latest_agent_version"] != "v1.7.0" {
		t.Fatalf("仍应给出服务端自带版本: %v", v)
	}

	// VERSION 永远不会被当作安装文件发出去。
	ticket := e.openWindow()
	if st, _, _ := e.callRaw("GET", "/i/"+ticket+"/VERSION"); st != http.StatusNotFound {
		t.Fatalf("VERSION 不应可下载: %d", st)
	}
}

func TestHostViewHasNoReminderWithoutVersionFile(t *testing.T) {
	dir, _, _ := newDistDir(t) // 没有 VERSION
	e := newTestEnv(t, withAgentDist(dir))
	m := testMeta("box")
	m.Ver = "v1.0.0"
	a := e.enrollAgent(m, "")
	v := hostViewOf(t, e, a.code)
	if _, has := v["upgrade_available"]; has {
		t.Fatalf("没有 VERSION 时无法判断，不应提醒: %v", v)
	}
	if _, has := v["latest_agent_version"]; has {
		t.Fatalf("没有 VERSION 时不应给出最新版本: %v", v)
	}
}

func TestUpgradeWindowRESTFlow(t *testing.T) {
	dir, _, _ := newDistDir(t)
	writeDistVersion(t, dir, "v1.7.0")
	e := newTestEnv(t, withAgentDist(dir), withFakeClock())
	m := testMeta("old-box")
	m.Ver = "v1.6.0"
	a := e.enrollAgent(m, "旧主机")
	hdr := e.admin()
	// 关掉发现窗口：之后能取到安装文件的只可能是升级窗口。
	if res := e.call("DELETE", "/api/v1/terminal/discovery", nil, hdr...); res.Status != http.StatusOK {
		t.Fatalf("关闭发现窗口失败: %d", res.Status)
	}
	if upgradeWindowOf(t, e, a.code) != nil {
		t.Fatal("还没开窗口，视图里不应有 upgrade_window")
	}

	// 开启：10 分钟，票据 32 位十六进制。
	res := e.call("POST", "/api/v1/terminal/hosts/"+a.code+"/upgrade-window", nil, hdr...)
	if res.Status != http.StatusOK {
		t.Fatalf("开启升级窗口失败: %d %s", res.Status, res.Raw)
	}
	w, _ := res.Body["upgrade_window"].(map[string]any)
	ticket, _ := w["ticket"].(string)
	if w["open"] != true || !ticketPattern.MatchString(ticket) || w["remaining_seconds"] != float64(600) || w["base_version"] != "v1.6.0" {
		t.Fatalf("升级窗口内容不对: %v", w)
	}
	// 主机列表也能看到（刷新页面后界面靠它恢复倒计时与命令）。
	if got := upgradeWindowOf(t, e, a.code); got == nil || got["ticket"] != ticket {
		t.Fatalf("主机视图应带升级窗口: %v", got)
	}

	// 票据能取到安装脚本、校验文件与二进制；别的票据、改一位的票据都是同一个 404。
	for _, name := range []string{"install.sh", "install.ps1", "checksums.txt", "relaymesh-agent-linux-amd64"} {
		if st, _, _ := e.callRaw("GET", "/i/"+ticket+"/"+name); st != http.StatusOK {
			t.Errorf("%s: 升级窗口的票据应能下载，实际 %d", name, st)
		}
	}
	other := strings.Repeat("a", 32)
	flipped := ticket[:31] + map[bool]string{true: "0", false: "1"}[ticket[31] != '0']
	for _, bad := range []string{other, flipped} {
		if st, _, _ := e.callRaw("GET", "/i/"+bad+"/install.sh"); st != http.StatusNotFound {
			t.Errorf("错误的票据应为 404，实际 %d", st)
		}
	}
	if st, _, _ := e.callRaw("GET", "/i/"+ticket+"/secret.env"); st != http.StatusNotFound {
		t.Error("白名单之外的文件即使有票据也不能下载")
	}

	// 延时：+10 分钟，票据不变。
	res = e.call("POST", "/api/v1/terminal/hosts/"+a.code+"/upgrade-window", map[string]any{"minutes": 10, "extend": true}, hdr...)
	w, _ = res.Body["upgrade_window"].(map[string]any)
	if res.Status != http.StatusOK || w["remaining_seconds"] != float64(1200) || w["ticket"] != ticket {
		t.Fatalf("延时后应剩 20 分钟且票据不变: %d %v", res.Status, w)
	}
	// 时间流逝。
	e.clock.Advance(5 * time.Minute)
	if got := upgradeWindowOf(t, e, a.code); got == nil || got["remaining_seconds"] != float64(900) {
		t.Fatalf("5 分钟后应剩 15 分钟: %v", got)
	}

	// 提前结束：票据立刻失效。（时间流逝后重新取一份解锁头，免得被空闲超时挡住。）
	hdr = e.admin()
	if res := e.call("DELETE", "/api/v1/terminal/hosts/"+a.code+"/upgrade-window", nil, hdr...); res.Status != http.StatusOK {
		t.Fatalf("结束失败: %d %s", res.Status, res.Raw)
	}
	if upgradeWindowOf(t, e, a.code) != nil {
		t.Fatal("结束后视图里不应还有窗口")
	}
	if st, _, _ := e.callRaw("GET", "/i/"+ticket+"/install.sh"); st != http.StatusNotFound {
		t.Fatalf("结束后票据应失效，实际 %d", st)
	}
}

func TestUpgradeWindowExpires(t *testing.T) {
	dir, _, _ := newDistDir(t)
	writeDistVersion(t, dir, "v1.7.0")
	e := newTestEnv(t, withAgentDist(dir), withFakeClock())
	m := testMeta("box")
	m.Ver = "v1.6.0"
	a := e.enrollAgent(m, "")
	e.call("DELETE", "/api/v1/terminal/discovery", nil, e.admin()...)
	res := e.call("POST", "/api/v1/terminal/hosts/"+a.code+"/upgrade-window", nil, e.admin()...)
	ticket, _ := res.Body["upgrade_window"].(map[string]any)["ticket"].(string)

	e.clock.Advance(9*time.Minute + 59*time.Second)
	if st, _, _ := e.callRaw("GET", "/i/"+ticket+"/install.sh"); st != http.StatusOK {
		t.Fatalf("到期之前应可用: %d", st)
	}
	e.clock.Advance(2 * time.Second)
	if st, _, _ := e.callRaw("GET", "/i/"+ticket+"/install.sh"); st != http.StatusNotFound {
		t.Fatalf("到期后应失效: %d", st)
	}
	if upgradeWindowOf(t, e, a.code) != nil {
		t.Fatal("到期后视图里不应还有窗口")
	}
	// 周期任务清扫时补记一条「到期」审计，且只记一次。
	e.hub.sweepUpgradeWindows()
	e.hub.sweepUpgradeWindows()
	logs, err := e.store.ListTerminalAudit(context.Background(), 200, 0)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, l := range logs {
		if l.Event == EvUpgradeWindow && strings.Contains(l.Detail, "close expired") {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("到期审计应恰好一条，实际 %d", n)
	}
}

// 升级成功（主机带着不同的版本回来）：窗口自动关闭，票据失效；版本没变则不动。
func TestUpgradeWindowClosesWhenHostComesBackUpgraded(t *testing.T) {
	dir, _, _ := newDistDir(t)
	writeDistVersion(t, dir, "v1.7.0")
	e := newTestEnv(t, withAgentDist(dir))
	m := testMeta("box")
	m.Ver = "v1.6.0"
	a := e.enrollAgent(m, "")
	e.call("DELETE", "/api/v1/terminal/discovery", nil, e.admin()...)
	res := e.call("POST", "/api/v1/terminal/hosts/"+a.code+"/upgrade-window", nil, e.admin()...)
	ticket, _ := res.Body["upgrade_window"].(map[string]any)["ticket"].(string)

	// 重连但版本没变（比如只是网络抖动）：窗口保持。
	same := e.connectEnabled(a.priv, m)
	eventually(t, 2*time.Second, "新连接被登记", func() bool { return e.hub.onlineAgent(a.code) != nil })
	if upgradeWindowOf(t, e, a.code) == nil {
		t.Fatal("版本没变不应关闭窗口")
	}

	// 带着新版本回来：窗口关闭。
	m.Ver = "v1.7.0"
	e.connectEnabled(a.priv, m)
	_ = same
	eventually(t, 2*time.Second, "升级后窗口自动关闭", func() bool { return upgradeWindowOf(t, e, a.code) == nil })
	if st, _, _ := e.callRaw("GET", "/i/"+ticket+"/install.sh"); st != http.StatusNotFound {
		t.Fatalf("自动关闭后票据应失效: %d", st)
	}
	v := hostViewOf(t, e, a.code)
	if _, has := v["upgrade_available"]; has || v["agent_version"] != "v1.7.0" {
		t.Fatalf("升级后不应再提醒，且版本已更新: %v", v)
	}
}

func TestUpgradeWindowClosesWhenHostDisabledOrDeleted(t *testing.T) {
	dir, _, _ := newDistDir(t)
	writeDistVersion(t, dir, "v1.7.0")
	e := newTestEnv(t, withAgentDist(dir))
	m := testMeta("box")
	m.Ver = "v1.6.0"
	for _, action := range []string{"disable", "delete"} {
		a := e.enrollAgent(m, action)
		e.call("DELETE", "/api/v1/terminal/discovery", nil, e.admin()...)
		res := e.call("POST", "/api/v1/terminal/hosts/"+a.code+"/upgrade-window", nil, e.admin()...)
		ticket, _ := res.Body["upgrade_window"].(map[string]any)["ticket"].(string)
		if action == "disable" {
			e.call("POST", "/api/v1/terminal/hosts/"+a.code+"/disable", nil, e.admin()...)
		} else {
			e.call("DELETE", "/api/v1/terminal/hosts/"+a.code, nil, e.admin()...)
		}
		if st, _, _ := e.callRaw("GET", "/i/"+ticket+"/install.sh"); st != http.StatusNotFound {
			t.Errorf("%s 之后票据应失效: %d", action, st)
		}
	}
}

func TestUpgradeWindowRefusals(t *testing.T) {
	dir, _, _ := newDistDir(t)
	writeDistVersion(t, dir, "v1.7.0")
	e := newTestEnv(t, withAgentDist(dir))
	m := testMeta("box")
	m.Ver = "v1.6.0"
	a := e.enrollAgent(m, "")
	path := "/api/v1/terminal/hosts/" + a.code + "/upgrade-window"

	// 仅登录不行：需要解锁令牌。
	if res := e.call("POST", path, nil, "Authorization", humanAuth(testUser)); res.Status == http.StatusOK {
		t.Fatal("开启升级窗口需要解锁令牌")
	}
	if res := e.call("DELETE", path, nil, "Authorization", humanAuth(testUser)); res.Status == http.StatusOK {
		t.Fatal("结束升级窗口同样需要解锁令牌")
	}
	// 不存在的主机。
	if res := e.call("POST", "/api/v1/terminal/hosts/RM-ZZZZ-ZZZZ-ZZZZ-ZZZZ/upgrade-window", nil, e.admin()...); res.Status != http.StatusNotFound && res.Status != http.StatusBadRequest {
		t.Fatalf("不存在的主机应拒绝: %d", res.Status)
	}
	// 停用的主机不能开。
	e.call("POST", "/api/v1/terminal/hosts/"+a.code+"/disable", nil, e.admin()...)
	if res := e.call("POST", path, nil, e.admin()...); res.Status != http.StatusConflict || res.str("code") != "host_not_enabled" {
		t.Fatalf("停用的主机不能开升级窗口: %d %s", res.Status, res.Raw)
	}
}

func TestUpgradeWindowNeedsInstallSource(t *testing.T) {
	e := newTestEnv(t) // 没有分发目录
	a := e.enrollAgent(testMeta("box"), "")
	res := e.call("POST", "/api/v1/terminal/hosts/"+a.code+"/upgrade-window", nil, e.admin()...)
	if res.Status != http.StatusConflict || res.str("code") != "install_source_unavailable" {
		t.Fatalf("没有安装源时应明确拒绝: %d %s", res.Status, res.Raw)
	}
}

// 升级窗口的开启 / 延时 / 结束 / 到期 / 自动关闭都会进审计，且审计里没有票据。
func TestUpgradeWindowIsAudited(t *testing.T) {
	dir, _, _ := newDistDir(t)
	writeDistVersion(t, dir, "v1.7.0")
	e := newTestEnv(t, withAgentDist(dir), withFakeClock())
	m := testMeta("box")
	m.Ver = "v1.6.0"
	a := e.enrollAgent(m, "")
	e.call("DELETE", "/api/v1/terminal/discovery", nil, e.admin()...)
	res := e.call("POST", "/api/v1/terminal/hosts/"+a.code+"/upgrade-window", nil, e.admin()...)
	ticket, _ := res.Body["upgrade_window"].(map[string]any)["ticket"].(string)
	e.call("POST", "/api/v1/terminal/hosts/"+a.code+"/upgrade-window", map[string]any{"extend": true}, e.admin()...)
	e.callRaw("GET", "/i/"+ticket+"/install.sh")
	e.call("DELETE", "/api/v1/terminal/hosts/"+a.code+"/upgrade-window", nil, e.admin()...)

	eventually(t, 2*time.Second, "审计写入", func() bool {
		return auditHas(t, e, EvUpgradeWindow, "close manual") && auditHas(t, e, EvInstallServe, "window=upgrade host="+a.code)
	})
	for _, want := range []string{"open from=v1.6.0", "extend until=", "close manual"} {
		if !auditHas(t, e, EvUpgradeWindow, want) {
			t.Errorf("审计里应有升级窗口事件 %q", want)
		}
	}
	// 票据是凭据，任何审计字段里都不能出现。
	logs, err := e.store.ListTerminalAudit(context.Background(), 200, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range logs {
		if strings.Contains(l.Detail, ticket) || strings.Contains(l.Event, ticket) {
			t.Fatalf("审计里不应出现升级票据: %+v", l)
		}
	}
}
