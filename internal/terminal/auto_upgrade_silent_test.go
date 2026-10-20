package terminal

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/teacat99/RelayMesh/v2/internal/relsign"
	"github.com/teacat99/RelayMesh/v2/internal/termproto"
)

// 静默自动升级（Hub 侧的请求 / 批准）。要钉住的几件事：
//   - 没有 Agent 的请求就没有批准；批准只在一串前提都成立时才给：主机自己声明的同意、Hub 的总闸、版本确实更新且就是请求的那个、
//     分发目录里有这个平台的程序与发布签名、主机上没有会因升级而中断的会话、没有别的升级在进行；
//   - 不批准时什么也不改：不认领升级请求、不开窗口、不记审计；
//   - 批准时：认领的升级请求标明来源是 auto，窗口里的票据真能取到程序，批准里的签名与校验值彼此一致；
//   - 之后的进展、完成、失败、还原都复用现有的升级状态机，审计用 agent_auto_upgrade_* 记。

const silentAsset = "relaymesh-agent-linux-amd64"

type silentFixture struct {
	env   *testEnv
	agent *fakeAgent
	meta  termproto.Meta
	dir   string
	linux []byte
	pub   ed25519.PublicKey
	priv  ed25519.PrivateKey
	n     int
}

// newSilentFixture 造一台装了 v1.6.0、主人在本机选了静默模式的主机；服务端自带 v1.7.0 且已签名，总闸已打开。
func newSilentFixture(t *testing.T, mutate func(*termproto.Meta), opts ...envOption) *silentFixture {
	t.Helper()
	dir, linux, _ := newDistDir(t)
	writeDistVersion(t, dir, "v1.7.0")
	priv := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{5}, ed25519.SeedSize))
	f := &silentFixture{dir: dir, linux: linux, priv: priv, pub: priv.Public().(ed25519.PublicKey)}
	f.signDist(t, "v1.7.0", "linux-amd64", linux)

	e := newTestEnv(t, append([]envOption{withAgentDist(dir), withFakeClock()}, opts...)...)
	m := testMeta("silent-box")
	m.Ver, m.Plat, m.AutoUpgrade = "v1.6.0", "linux-amd64", termproto.AutoUpgradeSilent
	if mutate != nil {
		mutate(&m)
	}
	a := e.enrollAgent(m, "静默升级的主机")
	e.call("DELETE", "/api/v1/terminal/discovery", nil, e.admin()...) // 之后能取到安装文件的只可能是升级窗口
	f.env, f.agent, f.meta = e, a, m
	f.setSwitch(t, true)
	return f
}

func (f *silentFixture) signDist(t *testing.T, version, platform string, content []byte) {
	t.Helper()
	sum := sha256.Sum256(content)
	sig, err := relsign.Sign(f.priv, version, platform, hex.EncodeToString(sum[:]))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.dir, silentAsset+".sig"), []byte(sig+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func (f *silentFixture) setSwitch(t *testing.T, enabled bool) {
	t.Helper()
	res := f.env.call("PUT", "/api/v1/terminal/prefs/auto-upgrade", map[string]any{"enabled": enabled}, f.env.admin()...)
	if res.Status != http.StatusOK {
		t.Fatalf("改总闸失败: %d %s", res.Status, res.Raw)
	}
}

func (f *silentFixture) grantCount() int {
	f.agent.mu.Lock()
	defer f.agent.mu.Unlock()
	return len(f.agent.grants)
}

// request 让假 Agent 发一次升级请求，等 Hub 的回答并返回。每次先把 Hub 的时钟拨过限流间隔，免得误撞 too_soon。
func (f *silentFixture) request(t *testing.T, version string) termproto.AutoUpgradeGrant {
	t.Helper()
	f.env.clock.Advance(autoRequestMinInterval + time.Second)
	return f.requestNow(t, version)
}

func (f *silentFixture) requestNow(t *testing.T, version string) termproto.AutoUpgradeGrant {
	t.Helper()
	f.n++
	id := fmt.Sprintf("%032x", f.n)
	before := f.grantCount()
	f.agent.send(termproto.AutoUpgradeRequest{T: termproto.TAutoUpgradeRequest, ID: id, Version: version})
	eventually(t, 3*time.Second, "收到静默升级的回答", func() bool { return f.grantCount() > before })
	f.agent.mu.Lock()
	defer f.agent.mu.Unlock()
	got := f.agent.grants[len(f.agent.grants)-1]
	if got.T != termproto.TAutoUpgradeGrant || got.ID != id {
		t.Fatalf("回答应原样带回请求的标识: %+v（期望 id=%s）", got, id)
	}
	return got
}

func (f *silentFixture) view(t *testing.T) map[string]any { return hostViewOf(t, f.env, f.agent.code) }

func (f *silentFixture) job(t *testing.T) map[string]any {
	j, _ := f.view(t)["upgrade_job"].(map[string]any)
	return j
}

func (f *silentFixture) window(t *testing.T) map[string]any {
	w, _ := f.view(t)["upgrade_window"].(map[string]any)
	return w
}

// wantUntouched 断言没有被批准：没有升级请求、没有升级窗口、没有「开始」审计。
func (f *silentFixture) wantUntouched(t *testing.T) {
	t.Helper()
	if j := f.job(t); j != nil {
		t.Fatalf("没有批准就不该认领升级请求: %v", j)
	}
	if w := f.window(t); w != nil {
		t.Fatalf("没有批准就不该开升级窗口: %v", w)
	}
	if n := len(f.env.auditEvents(EvAutoUpgradeStart)); n != 0 {
		t.Fatalf("没有批准就不该记「开始」审计: %d", n)
	}
}

func (f *silentFixture) report(id, stage, code, detail string) {
	f.agent.send(termproto.UpgradeStatus{T: termproto.TUpgradeStatus, ID: id, Stage: stage, Code: code, Detail: detail})
}

func (f *silentFixture) waitState(t *testing.T, want string) {
	t.Helper()
	eventually(t, 3*time.Second, "升级请求进入 "+want, func() bool {
		s, _ := f.job(t)["state"].(string)
		return s == want
	})
}

// ---------- 批准 ----------

func TestSilentGrantHappyPath(t *testing.T) {
	f := newSilentFixture(t, nil)
	e, a := f.env, f.agent

	g := f.request(t, "v1.7.0")
	if g.Status != termproto.AutoGrantGranted || g.Version != "v1.7.0" {
		t.Fatalf("应批准: %+v", g)
	}

	// 批准里的东西彼此一致：校验值是这个平台程序的 SHA-256，签名确实是对（版本 + 平台 + 这个校验值）的签名。
	sum := sha256.Sum256(f.linux)
	if g.SHA256 != hex.EncodeToString(sum[:]) {
		t.Fatalf("校验值应是 linux-amd64 程序的 SHA-256: %s", g.SHA256)
	}
	if err := relsign.Verify([]ed25519.PublicKey{f.pub}, g.Sig, g.Version, "linux-amd64", g.SHA256); err != nil {
		t.Fatalf("批准里的签名应能验过: %v (%s)", err, g.Sig)
	}
	if err := g.ToUpgrade().Validate(); err != nil {
		t.Fatalf("批准里的下载凭据应合规: %v", err)
	}

	// 票据正是这台主机升级窗口的票据，并且真能取到程序。
	w := f.window(t)
	if w == nil || w["ticket"] != g.Ticket || w["base_version"] != "v1.6.0" {
		t.Fatalf("应为这次升级开升级窗口: %v", w)
	}
	st, _, body := e.callRaw("GET", "/i/"+g.Ticket+"/"+silentAsset)
	if st != http.StatusOK || !bytes.Equal(body, f.linux) {
		t.Fatalf("用批准里的票据应能取到程序: %d (%d 字节)", st, len(body))
	}
	// 签名文件本身不对外提供。
	if st, _, _ := e.callRaw("GET", "/i/"+g.Ticket+"/"+silentAsset+".sig"); st != http.StatusNotFound {
		t.Fatalf("签名文件不应经 /i/ 提供: %d", st)
	}

	// 认领的升级请求：标识就是请求的标识，来源标明是 auto。
	j := f.job(t)
	if j == nil || j["id"] != g.ID || j["origin"] != "auto" || j["state"] != "requested" || j["from"] != "v1.6.0" || j["to"] != "v1.7.0" {
		t.Fatalf("升级请求不对: %v", j)
	}

	// 审计：记了「开始」，只有版本与会话数，没有票据、校验值或签名。
	eventually(t, 3*time.Second, "审计写入", func() bool { return auditHas(t, e, EvAutoUpgradeStart, "id="+g.ID) })
	logs := e.auditEvents(EvAutoUpgradeStart)
	if len(logs) != 1 || logs[0].MachineCode != a.code || logs[0].Actor != ActorSystem {
		t.Fatalf("应记一条 system 的「开始」审计: %+v", logs)
	}
	for _, banned := range []string{g.Ticket, g.SHA256, g.Sig} {
		if strings.Contains(logs[0].Detail, banned) {
			t.Fatalf("审计里不应出现票据 / 校验值 / 签名: %q", logs[0].Detail)
		}
	}
	if !strings.Contains(logs[0].Detail, "from=v1.6.0") || !strings.Contains(logs[0].Detail, "to=v1.7.0") {
		t.Fatalf("审计应写明从哪个版本到哪个版本: %q", logs[0].Detail)
	}
}

func TestSilentGrantCarriesOnlyWhatTheAgentNeeds(t *testing.T) {
	f := newSilentFixture(t, nil)
	g := f.request(t, "v1.7.0")
	if g.Status != termproto.AutoGrantGranted {
		t.Fatalf("前提：应批准: %+v", g)
	}
	f.agent.mu.Lock()
	raw := f.agent.grantRaw[len(f.agent.grantRaw)-1]
	f.agent.mu.Unlock()
	var m map[string]any
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		t.Fatal(err)
	}
	for k := range m {
		switch k {
		case "t", "id", "status", "version", "ticket", "sha256", "sig":
		default:
			t.Errorf("批准里不应有字段 %q: %s", k, raw)
		}
	}
	// 没有下载地址、文件名或公钥：地址由 Agent 自己推出，公钥只来自程序本身或主机所有者。
	for _, banned := range []string{"url", "/i/", "relaymesh-agent-", "rmpub1", "pubkey"} {
		if strings.Contains(raw, banned) {
			t.Errorf("批准里不应出现 %q: %s", banned, raw)
		}
	}
}

func TestSilentGrantVerdicts(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(*termproto.Meta)
		setup   func(t *testing.T, f *silentFixture)
		version string // 空 = v1.7.0
		want    string
	}{
		{"总闸关着：不批准，即使主人已经同意", nil, func(t *testing.T, f *silentFixture) { f.setSwitch(t, false) }, "", termproto.AutoGrantDisabled},
		{"主人选的是提示模式：不批准", func(m *termproto.Meta) { m.AutoUpgrade = termproto.AutoUpgradeNotify }, nil, "", termproto.AutoGrantNotAllowed},
		{"主人没有开启（off）", func(m *termproto.Meta) { m.AutoUpgrade = termproto.AutoUpgradeOff }, nil, "", termproto.AutoGrantNotAllowed},
		{"没有声明（旧版本 Agent 误发了请求）", func(m *termproto.Meta) { m.AutoUpgrade = "" }, nil, "", termproto.AutoGrantNotAllowed},
		{"临时身份即使声明了也不批准", func(m *termproto.Meta) { m.Kind = termproto.KindTemporary }, nil, "", termproto.AutoGrantNotAllowed},
		{"服务端没有适用于这个平台的程序", func(m *termproto.Meta) { m.Plat = "darwin-arm64" }, nil, "", termproto.AutoGrantUnavailable},
		{"主机没有报告平台", func(m *termproto.Meta) { m.Plat = "" }, nil, "", termproto.AutoGrantUnavailable},
		{"服务端不知道自带版本", nil, func(t *testing.T, f *silentFixture) {
			if err := removeDistVersion(f.env); err != nil {
				t.Fatal(err)
			}
		}, "", termproto.AutoGrantUnavailable},
		{"版本一致：已是最新", func(m *termproto.Meta) { m.Ver = "v1.7.0" }, nil, "", termproto.AutoGrantUpToDate},
		{"主机比服务端新（服务端回退过）：不降级", func(m *termproto.Meta) { m.Ver = "v1.8.0" }, nil, "", termproto.AutoGrantUpToDate},
		{"版本不同但无法比较：无人值守时绝不升级", func(m *termproto.Meta) { m.Ver = "v1.7.0-dev-20261016-aaaaaaa" }, func(t *testing.T, f *silentFixture) {
			writeDistVersion(t, f.dir, "v1.7.0-dev-20261016-bbbbbbb")
		}, "v1.7.0-dev-20261016-bbbbbbb", termproto.AutoGrantUpToDate},
		{"请求的版本不是 Hub 现在的最新：让 Agent 重新检查，不替它换目标", nil, nil, "v1.6.5", termproto.AutoGrantStale},
		{"这个版本没有签名文件：不批准", nil, func(t *testing.T, f *silentFixture) {
			if err := os.Remove(filepath.Join(f.dir, silentAsset+".sig")); err != nil {
				t.Fatal(err)
			}
		}, "", termproto.AutoGrantUnsigned},
		{"签名文件写得不对：按没有签名处理", nil, func(t *testing.T, f *silentFixture) {
			if err := os.WriteFile(filepath.Join(f.dir, silentAsset+".sig"), []byte("this is not a signature\n"), 0o644); err != nil {
				t.Fatal(err)
			}
		}, "", termproto.AutoGrantUnsigned},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newSilentFixture(t, tc.mutate)
			if tc.setup != nil {
				tc.setup(t, f)
			}
			version := tc.version
			if version == "" {
				version = "v1.7.0"
			}
			g := f.request(t, version)
			if g.Status != tc.want {
				t.Fatalf("回答 = %+v，期望 %s", g, tc.want)
			}
			if g.Ticket != "" || g.SHA256 != "" || g.Sig != "" {
				t.Fatalf("没有批准就不该带任何下载凭据: %+v", g)
			}
			f.wantUntouched(t)
		})
	}
}

// 两个版本之间的次序与 Hub 的「可升级」判断同源：服务端回答的版本、Agent 请求的版本、批准里的版本必须是同一个。
func TestSilentGrantMustMatchTheRequestedVersion(t *testing.T) {
	f := newSilentFixture(t, nil)
	if g := f.request(t, "v1.7.0"); g.Status != termproto.AutoGrantGranted || g.Version != "v1.7.0" {
		t.Fatalf("%+v", g)
	}
}

// ---------- 忙 ----------

func TestSilentGrantRefusesWhileSessionsWouldBeInterrupted(t *testing.T) {
	f := newSilentFixture(t, nil)
	e, a := f.env, f.agent
	e.openSession(a, e.admin(), "正在跑的任务")
	eventually(t, 3*time.Second, "Hub 看到一个运行中的会话", func() bool {
		for _, s := range e.hub.onlineAgent(a.code).sessionList() {
			if s.State == termproto.SessionRunning {
				return true
			}
		}
		return false
	})

	// 会话与 Agent 同生共死（没有守护进程）：升级会结束它们，静默升级没有人在场确认——不批准。
	if g := f.request(t, "v1.7.0"); g.Status != termproto.AutoGrantBusy {
		t.Fatalf("有会取决于 Agent 的运行中会话时应 busy: %+v", g)
	}
	f.wantUntouched(t)

	// 守护进程持有会话：升级只重启 Agent，会话不受影响——批准。
	a.send(termproto.Holder{T: termproto.THolder, State: termproto.HolderReady, Persistent: true, Version: "test"})
	eventually(t, 3*time.Second, "Hub 得知守护进程就绪", func() bool {
		hv := e.hub.onlineAgent(a.code).holderView()
		return hv != nil && hv.Persistent
	})
	g := f.request(t, "v1.7.0")
	if g.Status != termproto.AutoGrantGranted {
		t.Fatalf("会话由守护进程持有时应批准: %+v", g)
	}
	// 审计里如实记下升级时有多少运行中的会话、会话是否由守护进程持有。
	eventually(t, 3*time.Second, "审计写入", func() bool {
		return auditHas(t, e, EvAutoUpgradeStart, "running=1") && auditHas(t, e, EvAutoUpgradeStart, "persistent=true")
	})
	// 会话本身没有被动过。
	if got := e.hub.onlineAgent(a.code).sessionList(); len(got) != 1 || got[0].State != termproto.SessionRunning {
		t.Fatalf("批准升级不应改动会话: %+v", got)
	}
}

func TestSilentGrantRefusesWhileAnotherUpgradeIsInProgress(t *testing.T) {
	f := newSilentFixture(t, func(m *termproto.Meta) { m.WebUpgrade = termproto.WebUpgradeOn })
	e := f.env

	// 管理员刚在 Web 上发起了一次升级。
	res := e.call("POST", "/api/v1/terminal/hosts/"+f.agent.code+"/web-upgrade", nil, e.admin()...)
	if res.Status != http.StatusOK {
		t.Fatalf("前提：Web 升级应能发起: %d %s", res.Status, res.Raw)
	}
	webJob := f.job(t)

	if g := f.request(t, "v1.7.0"); g.Status != termproto.AutoGrantBusy {
		t.Fatalf("已有升级在进行时应 busy: %+v", g)
	}
	if got := f.job(t); got == nil || got["id"] != webJob["id"] || got["origin"] != nil {
		t.Fatalf("不能顶掉别人的升级请求: %v", got)
	}
	if n := len(e.auditEvents(EvAutoUpgradeStart)); n != 0 {
		t.Fatalf("不该记「开始」审计: %d", n)
	}

	// 那次升级失败结束后，静默升级就可以进行了。
	f.report(webJob["id"].(string), termproto.UpgradeStageFailed, termproto.UpgradeCodeDownload, "网络不通")
	f.waitState(t, "failed")
	if g := f.request(t, "v1.7.0"); g.Status != termproto.AutoGrantGranted {
		t.Fatalf("前一次已结束，应批准: %+v", g)
	}
	if j := f.job(t); j["origin"] != "auto" {
		t.Fatalf("现在的升级请求应是 auto 来源: %v", j)
	}
}

func TestSilentGrantIsExclusive(t *testing.T) {
	f := newSilentFixture(t, nil)
	if g := f.request(t, "v1.7.0"); g.Status != termproto.AutoGrantGranted {
		t.Fatalf("%+v", g)
	}
	// 第一次批准之后、升级还没结束时，又来一个请求（比如 Agent 重启过又发了一次）：busy，且不动第一次的认领。
	first := f.job(t)
	if g := f.request(t, "v1.7.0"); g.Status != termproto.AutoGrantBusy {
		t.Fatalf("同一台主机同时只有一次升级: %+v", g)
	}
	if got := f.job(t); got["id"] != first["id"] {
		t.Fatalf("第二个请求不该改动第一次的认领: %v → %v", first, got)
	}
	if n := len(f.env.auditEvents(EvAutoUpgradeStart)); n != 1 {
		t.Fatalf("只应有一条「开始」审计: %d", n)
	}
}

func TestSilentGrantReusesAnOpenWindowAndUndoKeepsIt(t *testing.T) {
	f := newSilentFixture(t, nil)
	e := f.env
	// 管理员已经为手动升级开了窗口：批准沿用它的票据。
	res := e.call("POST", "/api/v1/terminal/hosts/"+f.agent.code+"/upgrade-window", nil, e.admin()...)
	if res.Status != http.StatusOK {
		t.Fatalf("前提：开窗口: %d %s", res.Status, res.Raw)
	}
	ticket := f.window(t)["ticket"]
	g := f.request(t, "v1.7.0")
	if g.Status != termproto.AutoGrantGranted || g.Ticket != ticket {
		t.Fatalf("应沿用已开着的窗口的票据: %+v（窗口票据 %v）", g, ticket)
	}

	// 批准发不出去时的撤销：认领的升级请求取消；窗口原本就开着，不能被一起关掉。
	a := e.hub.onlineAgent(f.agent.code)
	now := e.hub.now()
	e.hub.webUpgrades.drop(f.agent.code)
	claim := &autoClaim{job: upgradeJob{id: strings.Repeat("a", 32), code: f.agent.code}, wasOpen: true}
	e.hub.webUpgrades.claim(&claim.job, now)
	e.hub.undoAutoClaim(a.code, claim, now)
	if e.hub.webUpgrades.busy(f.agent.code, now) != nil {
		t.Fatal("撤销后不该还占着升级请求")
	}
	if e.hub.upgrades.view(f.agent.code, now) == nil {
		t.Fatal("窗口原本就开着，撤销不该把它关掉")
	}
}

// 临时身份在握手时就不会带着模式进来（视图与检查的测试已经覆盖），这里直接对一条「假设带着静默模式的临时连接」判一次：
// 判断本身也要拒绝它，而不是依赖握手那一层。
func TestSilentGrantRefusesTemporaryIdentitiesEvenIfTheyDeclareSilent(t *testing.T) {
	f := newSilentFixture(t, nil)
	e := f.env
	meta := f.meta
	meta.Kind = termproto.KindTemporary
	conn := &agentConn{hub: e.hub, code: f.agent.code, meta: meta, temp: true}
	e.clock.Advance(autoRequestMinInterval + time.Second)
	req := termproto.AutoUpgradeRequest{T: termproto.TAutoUpgradeRequest, ID: strings.Repeat("c", 32), Version: "v1.7.0"}
	g, claim := e.hub.judgeAutoUpgrade(e.hub.ctx, conn, req, e.hub.now())
	if g.Status != termproto.AutoGrantNotAllowed || claim != nil {
		t.Fatalf("临时身份不应被批准: %+v %+v", g, claim)
	}
	f.wantUntouched(t)
}

func TestSilentGrantUndoClosesAWindowItOpened(t *testing.T) {
	f := newSilentFixture(t, nil)
	e := f.env
	a := e.hub.onlineAgent(f.agent.code)
	now := e.hub.now()
	e.clock.Advance(autoRequestMinInterval + time.Second)
	now = e.hub.now()

	req := termproto.AutoUpgradeRequest{T: termproto.TAutoUpgradeRequest, ID: strings.Repeat("b", 32), Version: "v1.7.0"}
	g, claim := e.hub.judgeAutoUpgrade(e.hub.ctx, a, req, now)
	if g.Status != termproto.AutoGrantGranted || claim == nil || claim.wasOpen {
		t.Fatalf("前提：应批准且窗口是新开的: %+v %+v", g, claim)
	}
	if e.hub.upgrades.view(f.agent.code, now) == nil || e.hub.webUpgrades.busy(f.agent.code, now) == nil {
		t.Fatal("批准时应已认领并开了窗口")
	}
	e.hub.undoAutoClaim(a.code, claim, now)
	if e.hub.upgrades.view(f.agent.code, now) != nil || e.hub.webUpgrades.busy(f.agent.code, now) != nil {
		t.Fatal("批准没送达，撤销应让认领与（新开的）窗口都消失")
	}
}

// ---------- 限流与不合规的请求 ----------

func TestSilentRequestsAreRateLimitedPerConnection(t *testing.T) {
	f := newSilentFixture(t, nil)
	first := f.request(t, "v1.7.0")
	if first.Status != termproto.AutoGrantGranted {
		t.Fatalf("%+v", first)
	}
	second := f.requestNow(t, "v1.7.0")
	if second.Status != termproto.AutoGrantTooSoon || second.Ticket != "" {
		t.Fatalf("太频繁应得到 too_soon: %+v", second)
	}
	// too_soon 不影响第一次的认领；过了间隔，请求才重新按前提判断（这里是「已有升级在进行」）。
	if j := f.job(t); j == nil || j["id"] != first.ID {
		t.Fatalf("too_soon 不应改动第一次的认领: %v", j)
	}
	if third := f.request(t, "v1.7.0"); third.Status != termproto.AutoGrantBusy {
		t.Fatalf("过了间隔应重新判断: %+v", third)
	}
}

func TestSilentIgnoresMalformedRequests(t *testing.T) {
	f := newSilentFixture(t, nil)
	for _, bad := range []termproto.AutoUpgradeRequest{
		{T: termproto.TAutoUpgradeRequest, ID: "", Version: "v1.7.0"},
		{T: termproto.TAutoUpgradeRequest, ID: "short", Version: "v1.7.0"},
		{T: termproto.TAutoUpgradeRequest, ID: strings.Repeat("Z", 32), Version: "v1.7.0"},
		{T: termproto.TAutoUpgradeRequest, ID: "../../etc/passwd", Version: "v1.7.0"},
		{T: termproto.TAutoUpgradeRequest, ID: strings.Repeat("a", 64), Version: "v1.7.0"},
		{T: termproto.TAutoUpgradeRequest, ID: strings.Repeat("a", 32), Version: ""},
		{T: termproto.TAutoUpgradeRequest, ID: strings.Repeat("a", 32), Version: "v1 7"},
		{T: termproto.TAutoUpgradeRequest, ID: strings.Repeat("a", 32), Version: "../x"},
	} {
		f.agent.send(bad)
	}
	time.Sleep(150 * time.Millisecond)
	if n := f.grantCount(); n != 0 {
		t.Fatalf("不合规的请求不应得到任何回答: %d", n)
	}
	// 也不占用限流额度：紧接着一次合规的请求（不拨时钟）应得到正常回答，而不是 too_soon。
	if g := f.requestNow(t, "v1.7.0"); g.Status != termproto.AutoGrantGranted {
		t.Fatalf("不合规的请求不应影响后续的正常请求: %+v", g)
	}
	f.agent.mu.Lock()
	n := len(f.agent.grants)
	f.agent.mu.Unlock()
	if n != 1 {
		t.Fatalf("只应收到一条回答: %d", n)
	}
}

func TestSilentIgnoresRequestsBeforeApproval(t *testing.T) {
	// 待启用（还没被管理员批准）的主机只应静默等待：它的请求不会得到回答，也不会被批准。
	dir, _, _ := newDistDir(t)
	writeDistVersion(t, dir, "v1.7.0")
	e := newTestEnv(t, withAgentDist(dir), withFakeClock())
	e.openDiscovery(e.admin())
	m := testMeta("pending-silent-box")
	m.Ver, m.Plat, m.AutoUpgrade = "v1.6.0", "linux-amd64", termproto.AutoUpgradeSilent
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
	a.send(termproto.AutoUpgradeRequest{T: termproto.TAutoUpgradeRequest, ID: strings.Repeat("1", 32), Version: "v1.7.0"})
	time.Sleep(200 * time.Millisecond)
	a.mu.Lock()
	n := len(a.grants)
	a.mu.Unlock()
	if n != 0 {
		t.Fatalf("待启用的主机不应得到回答: %d", n)
	}
}

// ---------- 之后的进展：复用现有的升级状态机，审计用 agent_auto_upgrade_* ----------

func TestSilentUpgradeLifecycleToDone(t *testing.T) {
	f := newSilentFixture(t, nil)
	e, a := f.env, f.agent
	g := f.request(t, "v1.7.0")
	if g.Status != termproto.AutoGrantGranted {
		t.Fatalf("%+v", g)
	}

	f.report(g.ID, termproto.UpgradeStageDownloading, "", "")
	f.waitState(t, "downloading")
	f.report(g.ID, termproto.UpgradeStageHandoff, "", "")
	f.waitState(t, "handoff")

	// Agent 被重启，带着新版本回来：请求完成、窗口自动关闭、票据失效。
	up := f.meta
	up.Ver = "v1.7.0"
	e.connectEnabled(a.priv, up)
	f.waitState(t, "done")
	if j := f.job(t); j["origin"] != "auto" || j["to"] != "v1.7.0" {
		t.Fatalf("完成后的请求内容不对: %v", j)
	}
	eventually(t, 2*time.Second, "升级窗口自动关闭", func() bool { return f.window(t) == nil })
	if st, _, _ := e.callRaw("GET", "/i/"+g.Ticket+"/"+silentAsset); st != http.StatusNotFound {
		t.Fatalf("升级完成后票据应失效: %d", st)
	}

	eventually(t, 3*time.Second, "审计写入", func() bool { return auditHas(t, e, EvAutoUpgradeOK, "id="+g.ID) })
	if n := len(e.auditEvents(EvAutoUpgradeStart)); n != 1 {
		t.Fatalf("应有一条「开始」: %d", n)
	}
	if n := len(e.auditEvents(EvAutoUpgradeOK)); n != 1 {
		t.Fatalf("应有一条「完成」: %d", n)
	}
	// 自动升级不写 agent_web_upgrade：管理员发起的升级与主机自己发起的升级在审计里一眼分得清。
	if n := len(e.auditEvents(EvWebUpgrade)); n != 0 {
		t.Fatalf("自动升级不应记 Web 升级的审计: %d", n)
	}
	if ok := e.auditEvents(EvAutoUpgradeOK)[0]; ok.Result != ResultOK || !strings.Contains(ok.Detail, "from=v1.6.0") || !strings.Contains(ok.Detail, "to=v1.7.0") {
		t.Fatalf("完成审计不对: %+v", ok)
	}
}

func TestSilentUpgradeFailureIsAuditedWithTheReason(t *testing.T) {
	f := newSilentFixture(t, nil)
	e := f.env
	g := f.request(t, "v1.7.0")
	f.report(g.ID, termproto.UpgradeStageDownloading, "", "")
	f.waitState(t, "downloading")
	f.report(g.ID, termproto.UpgradeStageFailed, termproto.UpgradeCodeSignature, "发布签名验证失败，已丢弃下载的程序")
	f.waitState(t, "failed")

	j := f.job(t)
	if j["code"] != termproto.UpgradeCodeSignature || j["origin"] != "auto" || !strings.Contains(j["detail"].(string), "签名") {
		t.Fatalf("失败原因应原样显示: %v", j)
	}
	eventually(t, 3*time.Second, "审计写入", func() bool { return auditHas(t, e, EvAutoUpgradeFail, "code=signature") })
	l := e.auditEvents(EvAutoUpgradeFail)[0]
	if l.Result != ResultFail || !strings.Contains(l.Detail, "id="+g.ID) || strings.Contains(l.Detail, g.Ticket) {
		t.Fatalf("失败审计不对: %+v", l)
	}
	eventually(t, 2*time.Second, "失败后升级窗口关闭", func() bool { return f.window(t) == nil })
	// 失败的请求结束后，主机可以再次被批准（Agent 一侧自己限制同一版本最多试两次）。
	if g2 := f.request(t, "v1.7.0"); g2.Status != termproto.AutoGrantGranted {
		t.Fatalf("失败结束后应能再次批准: %+v", g2)
	}
}

func TestSilentUpgradeRolledBackIsAuditedAsRollback(t *testing.T) {
	f := newSilentFixture(t, nil)
	e, a := f.env, f.agent
	g := f.request(t, "v1.7.0")
	f.report(g.ID, termproto.UpgradeStageHandoff, "", "")
	f.waitState(t, "handoff")

	// 新版本没连上，升级进程还原了旧版本：Agent 以旧版本重新上线，并带着「发起之后」的还原记录。
	back := f.meta
	back.LastUpgrade = &termproto.UpgradeNote{
		Outcome: termproto.UpgradeOutcomeRolledBack, From: "v1.6.0", To: "v1.7.0",
		At: e.clock.Now().Unix(), Detail: "新版本没有连上服务端",
	}
	e.connectEnabled(a.priv, back)
	f.waitState(t, "failed")
	if j := f.job(t); j["rolled_back"] != true || j["origin"] != "auto" {
		t.Fatalf("应标明已还原: %v", j)
	}
	eventually(t, 3*time.Second, "审计写入", func() bool { return auditHas(t, e, EvAutoUpgradeRollback, "id="+g.ID) })
	if n := len(e.auditEvents(EvAutoUpgradeFail)); n != 0 {
		t.Fatalf("已还原的升级只记 rollback，不再另记 fail: %d", n)
	}
}

func TestSilentUpgradeHandoffIsNotAuditedSeparately(t *testing.T) {
	// 管理员发起的升级在交接时多记一条；静默升级在批准时已经记了「开始」，交接不再重复。
	f := newSilentFixture(t, nil)
	e := f.env
	g := f.request(t, "v1.7.0")
	f.report(g.ID, termproto.UpgradeStageHandoff, "", "")
	f.waitState(t, "handoff")
	time.Sleep(100 * time.Millisecond)
	if n := len(e.auditEvents(EvWebUpgrade)); n != 0 {
		t.Fatalf("静默升级的交接不应记 agent_web_upgrade: %d", n)
	}
}

// 管理员发起的升级的审计与展示保持原样：来源不写进视图，事件仍是 agent_web_upgrade。
func TestWebUpgradeJobHasNoOriginAndKeepsItsAuditEvents(t *testing.T) {
	f := newWebUpgradeFixture(t, nil)
	id := f.request(t)
	if j := f.job(); j["origin"] != nil {
		t.Fatalf("管理员发起的升级不带 origin: %v", j)
	}
	f.report(id, termproto.UpgradeStageFailed, termproto.UpgradeCodeDownload, "网络不通")
	f.waitState("failed")
	eventually(t, 3*time.Second, "审计写入", func() bool { return auditHas(t, f.env, EvWebUpgrade, "failed id="+id) })
	if n := len(f.env.auditEvents(EvAutoUpgradeFail)); n != 0 {
		t.Fatalf("管理员发起的升级不应记自动升级的审计: %d", n)
	}
}

// ---------- 主机视图 ----------

func TestSilentHostViewCarriesTheWindowForDisplayOnly(t *testing.T) {
	f := newSilentFixture(t, func(m *termproto.Meta) { m.AutoWindow = "03:00-05:00" })
	v := f.view(t)
	if v["auto_upgrade"] != termproto.AutoUpgradeSilent || v["auto_window"] != "03:00-05:00" {
		t.Fatalf("主机视图应带静默模式与它声明的时间段: %v", v)
	}
	// 时间段只是展示：Hub 不据此拒绝（是否在时间段内由 Agent 在本机判断，Agent 在窗口外根本不会发请求）。
	if g := f.request(t, "v1.7.0"); g.Status != termproto.AutoGrantGranted {
		t.Fatalf("Hub 的裁决不看时间段: %+v", g)
	}
}

func TestHostViewHasNoWindowOutsideSilentMode(t *testing.T) {
	// 握手时 Hub 只保留静默模式下的时间段：提示模式 / 关闭时 Agent 即使带了，也不会出现在视图里。
	f := newSilentFixture(t, func(m *termproto.Meta) {
		m.AutoUpgrade = termproto.AutoUpgradeNotify
		m.AutoWindow = "03:00-05:00"
	})
	if w, has := f.view(t)["auto_window"]; has {
		t.Fatalf("提示模式下不应有时间段: %v", w)
	}
}

func TestSilentHostViewShowsTheLatestVerdictWithoutCredentials(t *testing.T) {
	f := newSilentFixture(t, nil)
	if g, has := f.view(t)["auto_grant"]; has {
		t.Fatalf("还没请求过就不该有裁决: %v", g)
	}

	// 没有签名：界面要能解释「为什么没有自动升级」。
	if err := os.Remove(filepath.Join(f.dir, silentAsset+".sig")); err != nil {
		t.Fatal(err)
	}
	if g := f.request(t, "v1.7.0"); g.Status != termproto.AutoGrantUnsigned {
		t.Fatalf("前提：应是 unsigned: %+v", g)
	}
	var shown map[string]any
	eventually(t, 3*time.Second, "主机视图出现 unsigned 裁决", func() bool {
		shown, _ = f.view(t)["auto_grant"].(map[string]any)
		return shown != nil
	})
	if shown["status"] != termproto.AutoGrantUnsigned || shown["version"] != "v1.7.0" || shown["at"] == nil {
		t.Fatalf("裁决 = %v", shown)
	}

	// 太频繁的请求得到 too_soon，但不覆盖上一次的正常裁决。
	if g := f.requestNow(t, "v1.7.0"); g.Status != termproto.AutoGrantTooSoon {
		t.Fatalf("前提：应是 too_soon: %+v", g)
	}
	time.Sleep(50 * time.Millisecond)
	if again, _ := f.view(t)["auto_grant"].(map[string]any); again["status"] != termproto.AutoGrantUnsigned {
		t.Fatalf("too_soon 不应覆盖上一次的裁决: %v", again)
	}

	// 补上签名后批准：视图换成 granted，且视图里永远没有票据、校验值或签名。
	f.signDist(t, "v1.7.0", "linux-amd64", f.linux)
	g := f.request(t, "v1.7.0")
	if g.Status != termproto.AutoGrantGranted {
		t.Fatalf("前提：应批准: %+v", g)
	}
	eventually(t, 3*time.Second, "主机视图换成 granted", func() bool {
		cur, _ := f.view(t)["auto_grant"].(map[string]any)
		return cur != nil && cur["status"] == termproto.AutoGrantGranted
	})
	// 裁决视图里只有 at / status / version：凭据（票据、校验值、签名）只在发给 Agent 的那一帧里。
	// （升级窗口自己的视图里有票据——那是管理员手动开窗口时本来就能看到的，不在这个对象里。）
	cur, _ := f.view(t)["auto_grant"].(map[string]any)
	raw, err := json.Marshal(cur)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{g.Ticket, g.SHA256, g.Sig} {
		if secret != "" && bytes.Contains(raw, []byte(secret)) {
			t.Fatalf("裁决视图不应带下载凭据 %q", secret)
		}
	}
	for k := range cur {
		if k != "at" && k != "status" && k != "version" {
			t.Fatalf("裁决视图只该有 at / status / version，却多了 %q", k)
		}
	}
}

func TestSilentGrantThatCannotBeDeliveredLeavesNoTraceAndNoVerdict(t *testing.T) {
	// 批准没能送达 Agent：撤销刚才的认领与（新开的）窗口，不记「开始」审计，界面也不该留下「已批准」。
	f := newSilentFixture(t, nil)
	e := f.env
	a := e.hub.onlineAgent(f.agent.code)
	a.mu.Lock()
	a.closed = true // 这条连接已经关了：write 立刻失败
	a.mu.Unlock()
	e.clock.Advance(autoRequestMinInterval + time.Second)
	before := e.auditEvents(EvAutoUpgradeStart)

	e.hub.onAutoUpgradeRequest(a, termproto.AutoUpgradeRequest{T: termproto.TAutoUpgradeRequest, ID: strings.Repeat("d", 32), Version: "v1.7.0"})

	now := e.hub.now()
	if e.hub.webUpgrades.busy(f.agent.code, now) != nil || e.hub.upgrades.view(f.agent.code, now) != nil {
		t.Fatal("批准没送达，认领与新开的窗口都应被撤销")
	}
	if v := a.autoGrantView(); v != nil {
		t.Fatalf("没送达的批准不该留下裁决显示: %+v", v)
	}
	if after := e.auditEvents(EvAutoUpgradeStart); len(after) != len(before) {
		t.Fatalf("没送达的批准不该记「开始」审计: %v", after)
	}
}
