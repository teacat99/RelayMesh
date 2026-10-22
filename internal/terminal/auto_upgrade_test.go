package terminal

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/teacat99/RelayMesh/v2/internal/termproto"
)

// 自动升级（Hub 侧）：
//   - Hub 只回答 Agent 主动发来的检查，从不主动推送；回答里没有票据、地址或校验值；
//   - 回答依据：主机自己声明的模式 → Hub 的总闸（默认关）→ 版本比较（只有「确定更旧」才算有新版本）；
//   - 太频繁的检查得到 too_soon，且不覆盖界面上看到的上一次结果；
//   - 总闸只能由刚刚解锁的管理员改，只在状态真的变化时写审计，且不影响其他终端偏好。

type autoUpgradeFixture struct {
	env   *testEnv
	agent *fakeAgent
	n     int
}

// newAutoUpgradeFixture 造一台装了 v1.6.0 的主机（主人在本机选了提示模式）；服务端自带 v1.7.0。
// 总闸保持默认（关），由各测试自己决定要不要打开。
func newAutoUpgradeFixture(t *testing.T, mutate func(*termproto.Meta), opts ...envOption) *autoUpgradeFixture {
	t.Helper()
	dir, _, _ := newDistDir(t)
	writeDistVersion(t, dir, "v1.7.0")
	e := newTestEnv(t, append([]envOption{withAgentDist(dir), withFakeClock()}, opts...)...)
	m := testMeta("auto-upgrade-box")
	m.Ver, m.Plat, m.AutoUpgrade = "v1.6.0", "linux-amd64", termproto.AutoUpgradeNotify
	if mutate != nil {
		mutate(&m)
	}
	a := e.enrollAgent(m, "自动升级的主机")
	return &autoUpgradeFixture{env: e, agent: a}
}

// setSwitch 通过 REST 改总闸（需要解锁令牌）。
func (f *autoUpgradeFixture) setSwitch(t *testing.T, enabled bool) {
	t.Helper()
	res := f.env.call("PUT", "/api/v1/terminal/prefs/auto-upgrade", map[string]any{"enabled": enabled}, f.env.admin()...)
	if res.Status != http.StatusOK {
		t.Fatalf("改总闸失败: %d %s", res.Status, res.Raw)
	}
}

// check 让假 Agent 发一次检查，等 Hub 的回答并返回。
func (f *autoUpgradeFixture) check(t *testing.T) termproto.AutoUpgradeOffer {
	t.Helper()
	f.n++
	id := strings.Repeat("0", 31) + string(rune('0'+f.n))
	before := f.offerCount()
	f.agent.send(termproto.AutoUpgradeCheck{T: termproto.TAutoUpgradeCheck, ID: id})
	eventually(t, 3*time.Second, "收到自动升级的回答", func() bool { return f.offerCount() > before })
	f.agent.mu.Lock()
	defer f.agent.mu.Unlock()
	got := f.agent.offers[len(f.agent.offers)-1]
	if got.T != termproto.TAutoUpgradeOffer || got.ID != id {
		t.Fatalf("回答应原样带回检查的标识: %+v（期望 id=%s）", got, id)
	}
	return got
}

func (f *autoUpgradeFixture) offerCount() int {
	f.agent.mu.Lock()
	defer f.agent.mu.Unlock()
	return len(f.agent.offers)
}

func (f *autoUpgradeFixture) view(t *testing.T) map[string]any {
	t.Helper()
	return hostViewOf(t, f.env, f.agent.code)
}

func TestAutoUpgradeCheckAnswers(t *testing.T) {
	const (
		same    = "v1.7.0"
		newer   = "v1.8.0"
		sameDay = "v1.7.0-dev-20261016-aaaaaaa" // 与服务端的 v1.7.0-dev-20261016-bbbbbbb 无法比较先后
		hubDay  = "v1.7.0-dev-20261016-bbbbbbb"
	)
	cases := []struct {
		name     string
		hubVer   string // 空 = 用默认的 v1.7.0
		mutate   func(*termproto.Meta)
		sw       bool
		want     string
		wantVer  string
		skipDist bool
	}{
		{"总闸默认关：一律不提供，即使主人已经同意", "", nil, false, termproto.AutoOfferDisabled, "", false},
		{"总闸打开、提示模式、主机更旧：有新版本", "", nil, true, termproto.AutoOfferAvailable, "v1.7.0", false},
		{"总闸打开、静默模式、主机更旧：同样有新版本（这一层只回答，不给任何可下载的东西）", "", func(m *termproto.Meta) { m.AutoUpgrade = termproto.AutoUpgradeSilent }, true, termproto.AutoOfferAvailable, "v1.7.0", false},
		{"主人没有开启（off）：不允许，不看总闸与版本", "", func(m *termproto.Meta) { m.AutoUpgrade = termproto.AutoUpgradeOff }, true, termproto.AutoOfferNotAllowed, "", false},
		{"主机没有声明（旧版本 Agent 误发了检查）：不允许", "", func(m *termproto.Meta) { m.AutoUpgrade = "" }, true, termproto.AutoOfferNotAllowed, "", false},
		{"临时身份即使（错误地）声明了模式也不允许：它没有持久的主人，无人值守地升级它没有意义", "", func(m *termproto.Meta) { m.Kind = termproto.KindTemporary }, true, termproto.AutoOfferNotAllowed, "", false},
		{"版本一致：已是最新", "", func(m *termproto.Meta) { m.Ver = same }, true, termproto.AutoOfferUpToDate, "v1.7.0", false},
		{"主机版本比服务端新（服务端回退过）：不降级", "", func(m *termproto.Meta) { m.Ver = newer }, true, termproto.AutoOfferUpToDate, "v1.7.0", false},
		{"版本不同但无法比较新旧：无人值守时绝不当作新版本", hubDay, func(m *termproto.Meta) { m.Ver = sameDay }, true, termproto.AutoOfferUpToDate, hubDay, false},
		{"服务端没有适用于这台主机的程序", "", func(m *termproto.Meta) { m.Plat = "darwin-arm64" }, true, termproto.AutoOfferUnavailable, "", false},
		{"服务端没有记录自带版本（缺少 VERSION）", "", nil, true, termproto.AutoOfferUnavailable, "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newAutoUpgradeFixture(t, tc.mutate)
			if tc.hubVer != "" {
				writeDistVersion(t, f.env.hub.dist.Dir(), tc.hubVer)
			}
			if tc.skipDist {
				if err := removeDistVersion(f.env); err != nil {
					t.Fatal(err)
				}
			}
			if tc.sw {
				f.setSwitch(t, true)
			}
			got := f.check(t)
			if got.Status != tc.want || got.Version != tc.wantVer {
				t.Fatalf("回答 = %+v，期望 status=%s version=%q", got, tc.want, tc.wantVer)
			}
		})
	}
}

func TestAutoUpgradeOfferCarriesNothingDownloadable(t *testing.T) {
	// 回答里只有 t / id / status / version：没有票据、地址、文件名或校验值——
	// 所以哪怕 Agent 一侧出了问题，从这一层也拿不到任何可以下载并运行的东西。
	f := newAutoUpgradeFixture(t, nil)
	f.setSwitch(t, true)
	if got := f.check(t); got.Status != termproto.AutoOfferAvailable {
		t.Fatalf("前提：应有新版本: %+v", got)
	}
	f.agent.mu.Lock()
	raw := f.agent.offerRaw[len(f.agent.offerRaw)-1]
	f.agent.mu.Unlock()
	var m map[string]any
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		t.Fatal(err)
	}
	for k := range m {
		switch k {
		case "t", "id", "status", "version":
		default:
			t.Errorf("回答里不应有字段 %q: %s", k, raw)
		}
	}
	for _, banned := range []string{"ticket", "url", "sha256", "/i/", "relaymesh-agent-"} {
		if strings.Contains(raw, banned) {
			t.Errorf("回答里不应出现 %q: %s", banned, raw)
		}
	}
}

func TestAutoUpgradeCheckIsRecordedOnTheHostView(t *testing.T) {
	f := newAutoUpgradeFixture(t, nil)
	v := f.view(t)
	if v["auto_upgrade"] != termproto.AutoUpgradeNotify {
		t.Fatalf("在线主机应带上它声明的模式: %v", v["auto_upgrade"])
	}
	if _, has := v["auto_check"]; has {
		t.Fatalf("还没检查过时不应有 auto_check: %v", v["auto_check"])
	}

	f.setSwitch(t, true)
	f.check(t)
	c, _ := f.view(t)["auto_check"].(map[string]any)
	if c == nil || c["status"] != termproto.AutoOfferAvailable || c["version"] != "v1.7.0" {
		t.Fatalf("检查后主机视图应带结果: %v", c)
	}
	if at, _ := c["at"].(string); at == "" {
		t.Fatalf("结果应带检查时刻: %v", c)
	}
}

func TestAutoUpgradeResultIsDroppedWhenTheHostGoesOffline(t *testing.T) {
	// 结果只属于这条连接：主机离线后视图里不再带模式与检查结果（离线的主机什么都没声明过），
	// 重新连上后要重新检查才有。
	f := newAutoUpgradeFixture(t, nil)
	f.setSwitch(t, true)
	f.check(t)
	f.agent.close()
	eventually(t, 3*time.Second, "主机离线", func() bool {
		v := f.view(t)
		_, hasMode := v["auto_upgrade"]
		_, hasCheck := v["auto_check"]
		return v["online"] != true && !hasMode && !hasCheck
	})
}

func TestAutoUpgradeChecksAreRateLimitedPerConnection(t *testing.T) {
	f := newAutoUpgradeFixture(t, nil)
	f.setSwitch(t, true)

	first := f.check(t)
	if first.Status != termproto.AutoOfferAvailable {
		t.Fatalf("第一次应正常回答: %+v", first)
	}
	// 马上再来一次：得到 too_soon，并且界面上看到的仍是上一次正常的结果。
	if second := f.check(t); second.Status != termproto.AutoOfferTooSoon || second.Version != "" {
		t.Fatalf("太频繁应得到 too_soon: %+v", second)
	}
	if c, _ := f.view(t)["auto_check"].(map[string]any); c == nil || c["status"] != termproto.AutoOfferAvailable {
		t.Fatalf("too_soon 不应覆盖上一次的结果: %v", c)
	}
	// 过了最短间隔就恢复正常；总闸中途关掉也会马上反映在下一次回答里。
	f.env.clock.Advance(autoCheckMinInterval + time.Second)
	f.setSwitch(t, false)
	if third := f.check(t); third.Status != termproto.AutoOfferDisabled {
		t.Fatalf("总闸关了之后的检查应得到 disabled: %+v", third)
	}
}

func TestAutoUpgradeIgnoresMalformedChecks(t *testing.T) {
	f := newAutoUpgradeFixture(t, nil)
	f.setSwitch(t, true)
	for _, id := range []string{"", "short", strings.Repeat("Z", 32), "../../etc/passwd", strings.Repeat("a", 64)} {
		f.agent.send(termproto.AutoUpgradeCheck{T: termproto.TAutoUpgradeCheck, ID: id})
	}
	// 不合规的检查不回答，也不占用频率额度：紧接着一次合规的检查应当得到正常回答（而不是 too_soon）。
	if got := f.check(t); got.Status != termproto.AutoOfferAvailable {
		t.Fatalf("不合规的检查不应影响后续的正常检查: %+v", got)
	}
	if n := f.offerCount(); n != 1 {
		t.Fatalf("只应收到一条回答（合规的那次），得到 %d", n)
	}
}

func TestAutoUpgradeIgnoresChecksBeforeApproval(t *testing.T) {
	// 待启用（还没被管理员批准）的主机只应静默等待：它的检查不会得到回答。
	dir, _, _ := newDistDir(t)
	writeDistVersion(t, dir, "v1.7.0")
	e := newTestEnv(t, withAgentDist(dir), withFakeClock())
	e.openDiscovery(e.admin())
	m := testMeta("pending-auto-box")
	m.Ver, m.Plat, m.AutoUpgrade = "v1.6.0", "linux-amd64", termproto.AutoUpgradeNotify
	_, priv := newFakeIdentity(t)
	a, _, err := e.dialAgent(priv, m, nil)
	if err != nil {
		t.Fatalf("拨号失败: %v", err)
	}
	defer a.close()
	if err := a.handshake(m); err != nil || a.welcome.Status != termproto.WelcomePending {
		t.Fatalf("前提：应处于待启用: %v %+v", err, a.welcome)
	}
	a.start()
	a.send(termproto.AutoUpgradeCheck{T: termproto.TAutoUpgradeCheck, ID: strings.Repeat("1", 32)})
	time.Sleep(200 * time.Millisecond)
	a.mu.Lock()
	n := len(a.offers)
	a.mu.Unlock()
	if n != 0 {
		t.Fatalf("待启用的主机不应得到回答: %d", n)
	}
}

func TestAutoUpgradeSwitchDefaultsOffAndNeedsUnlock(t *testing.T) {
	e := newTestEnv(t)
	if p := prefsOf(t, e); p["agent_auto_upgrade"] != false {
		t.Fatalf("总闸默认应关闭: %v", p["agent_auto_upgrade"])
	}
	path := "/api/v1/terminal/prefs/auto-upgrade"
	if res := e.call("PUT", path, map[string]any{"enabled": true}); res.Status != http.StatusUnauthorized && res.Status != http.StatusForbidden {
		t.Fatalf("未登录不应能改总闸: %d %s", res.Status, res.Raw)
	}
	if res := e.call("PUT", path, map[string]any{"enabled": true}, "Authorization", humanAuth(testUser)); res.Status == http.StatusOK {
		t.Fatal("只登录、没有解锁令牌时不应能改总闸（它放开了 Hub 对主机检查的回答）")
	}
	if p := prefsOf(t, e); p["agent_auto_upgrade"] != false {
		t.Fatalf("被拒绝的请求不应改动总闸: %v", p["agent_auto_upgrade"])
	}
}

func TestAutoUpgradeSwitchValidatesAndAudits(t *testing.T) {
	e := newTestEnv(t)
	path := "/api/v1/terminal/prefs/auto-upgrade"
	put := func(body any) apiResult { return e.call("PUT", path, body, e.admin()...) }

	// 缺省或类型不对一律 400，且不改动总闸（一个空请求体不该悄悄改动安全开关）。
	for _, bad := range []any{map[string]any{}, map[string]any{"enabled": "yes"}, map[string]any{"enabled": nil}, map[string]any{"enable": true}} {
		res := put(bad)
		if res.Status != http.StatusBadRequest {
			t.Fatalf("%v 应 400: %d %s", bad, res.Status, res.Raw)
		}
	}
	if p := prefsOf(t, e); p["agent_auto_upgrade"] != false {
		t.Fatalf("非法请求不应改动总闸: %v", p["agent_auto_upgrade"])
	}
	if n := len(e.auditEvents(EvAutoUpgradeSwitch)); n != 0 {
		t.Fatalf("没有变化不应写审计: %d", n)
	}

	if res := put(map[string]any{"enabled": true}); res.Status != http.StatusOK {
		t.Fatalf("打开: %d %s", res.Status, res.Raw)
	}
	if p := prefsOf(t, e); p["agent_auto_upgrade"] != true {
		t.Fatalf("状态应反映总闸已打开: %v", p["agent_auto_upgrade"])
	}
	stored, err := e.store.GetTerminalPrefs(context.Background())
	if err != nil || !stored.AgentAutoUpgrade {
		t.Fatalf("总闸应持久化: %+v %v", stored, err)
	}
	if logs := e.auditEvents(EvAutoUpgradeSwitch); len(logs) != 1 || logs[0].Detail != "enabled=true" {
		t.Fatalf("打开应写一条审计: %+v", logs)
	}

	// 重复打开：不写第二条审计。
	if res := put(map[string]any{"enabled": true}); res.Status != http.StatusOK {
		t.Fatalf("重复打开: %d %s", res.Status, res.Raw)
	}
	if n := len(e.auditEvents(EvAutoUpgradeSwitch)); n != 1 {
		t.Fatalf("状态没变不应再写审计: %d", n)
	}

	if res := put(map[string]any{"enabled": false}); res.Status != http.StatusOK {
		t.Fatalf("关闭: %d %s", res.Status, res.Raw)
	}
	if logs := e.auditEvents(EvAutoUpgradeSwitch); len(logs) != 2 || logs[0].Detail != "enabled=false" {
		t.Fatalf("关闭应再写一条审计（最新的在前）: %+v", logs)
	}
	if p := prefsOf(t, e); p["agent_auto_upgrade"] != false {
		t.Fatalf("状态应反映总闸已关闭: %v", p["agent_auto_upgrade"])
	}
}

func TestAutoUpgradeSwitchLeavesOtherPrefsAlone(t *testing.T) {
	e := newTestEnv(t)
	login := []string{"Authorization", humanAuth(testUser)}
	if res := e.call("PUT", "/api/v1/terminal/prefs/display", map[string]int{"fixed_cols": 120}, login...); res.Status != http.StatusOK {
		t.Fatalf("前提：设宽度保持: %d %s", res.Status, res.Raw)
	}
	if res := e.call("PUT", "/api/v1/terminal/prefs/auto-upgrade", map[string]any{"enabled": true}, e.admin()...); res.Status != http.StatusOK {
		t.Fatalf("打开总闸: %d %s", res.Status, res.Raw)
	}
	p := prefsOf(t, e)
	if p["fixed_cols"] != float64(120) || p["agent_auto_upgrade"] != true {
		t.Fatalf("改总闸不应覆盖别的偏好，改别的偏好也不应带走总闸: %v", p)
	}
	if res := e.call("PUT", "/api/v1/terminal/prefs/display", map[string]int{"fixed_cols": 0}, login...); res.Status != http.StatusOK {
		t.Fatalf("关宽度保持: %d %s", res.Status, res.Raw)
	}
	if p := prefsOf(t, e); p["agent_auto_upgrade"] != true || p["fixed_cols"] != float64(0) {
		t.Fatalf("改别的偏好不应带走总闸: %v", p)
	}
}
