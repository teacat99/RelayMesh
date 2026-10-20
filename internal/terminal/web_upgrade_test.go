package terminal

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/teacat99/RelayMesh/v2/internal/termproto"
)

// Web 一键升级（Hub 侧）：
//   - 前提不成立（不在线 / 不支持 / 主人没有允许 / 已是最新 / 没有对应平台的程序）一律不发请求、不开窗口；
//   - 有正在运行的会话或模型运行时必须由管理员确认；
//   - 同一台主机同时只有一次请求；
//   - 进展来自 Agent 的 upgrade_status 帧，结果以「主机重新上线时的版本」为准；新版本没连上而被还原也能看出来；
//   - 审计里记谁在什么时候对哪台主机发起了升级，但没有票据。

type webUpgradeFixture struct {
	env   *testEnv
	agent *fakeAgent
	meta  termproto.Meta
	linux []byte // 分发目录里 linux-amd64 的程序内容
}

// newWebUpgradeFixture 造一台装了旧版本 Agent、主人已允许从 Web 升级的主机；服务端自带 v1.7.0。
func newWebUpgradeFixture(t *testing.T, mutate func(*termproto.Meta), opts ...envOption) *webUpgradeFixture {
	t.Helper()
	dir, linux, _ := newDistDir(t)
	writeDistVersion(t, dir, "v1.7.0")
	e := newTestEnv(t, append([]envOption{withAgentDist(dir), withFakeClock()}, opts...)...)
	m := testMeta("web-upgrade-box")
	m.Ver, m.Plat, m.WebUpgrade = "v1.6.0", "linux-amd64", termproto.WebUpgradeOn
	if mutate != nil {
		mutate(&m)
	}
	a := e.enrollAgent(m, "可升级的主机")
	// 关掉发现窗口：之后能取到安装文件的只可能是升级窗口。
	e.call("DELETE", "/api/v1/terminal/discovery", nil, e.admin()...)
	return &webUpgradeFixture{env: e, agent: a, meta: m, linux: linux}
}

func (f *webUpgradeFixture) url() string {
	return "/api/v1/terminal/hosts/" + f.agent.code + "/web-upgrade"
}

func (f *webUpgradeFixture) post(body map[string]any) apiResult {
	f.env.t.Helper()
	return f.env.call("POST", f.url(), body, f.env.admin()...)
}

// frames 返回 Agent 到目前为止收到的升级请求。
func (f *webUpgradeFixture) frames() []termproto.Upgrade {
	f.agent.mu.Lock()
	defer f.agent.mu.Unlock()
	return append([]termproto.Upgrade(nil), f.agent.upgrades...)
}

func (f *webUpgradeFixture) job() map[string]any {
	f.env.t.Helper()
	j, _ := hostViewOf(f.env.t, f.env, f.agent.code)["upgrade_job"].(map[string]any)
	return j
}

func (f *webUpgradeFixture) jobState() string {
	s, _ := f.job()["state"].(string)
	return s
}

func (f *webUpgradeFixture) waitState(want string) {
	f.env.t.Helper()
	eventually(f.env.t, 3*time.Second, "升级请求进入 "+want, func() bool { return f.jobState() == want })
}

// report 让假 Agent 向 Hub 报告升级进展。
func (f *webUpgradeFixture) report(id, stage, code, detail string) {
	f.agent.send(termproto.UpgradeStatus{T: termproto.TUpgradeStatus, ID: id, Stage: stage, Code: code, Detail: detail})
}

// request 发起一次升级并返回请求编号；同时确认 Agent 收到了对应的帧。
func (f *webUpgradeFixture) request(t *testing.T) string {
	t.Helper()
	return f.requestWith(t, nil)
}

// requestWith 与 request 相同，但请求体可自定义（例如带 confirm_sessions）。
func (f *webUpgradeFixture) requestWith(t *testing.T, body map[string]any) string {
	t.Helper()
	res := f.post(body)
	if res.Status != http.StatusOK {
		t.Fatalf("发起升级失败: %d %s", res.Status, res.Raw)
	}
	job, _ := res.Body["upgrade_job"].(map[string]any)
	id, _ := job["id"].(string)
	if !termproto.ValidUpgradeID(id) {
		t.Fatalf("返回的请求编号不合规: %v", job)
	}
	eventually(t, 3*time.Second, "Agent 收到升级请求", func() bool {
		fr := f.frames()
		return len(fr) > 0 && fr[len(fr)-1].ID == id
	})
	return id
}

// reconnect 让这台主机的 Agent 带着给定的自报信息重新上线（旧连接被取代），之后的请求帧由新连接接收。
func (f *webUpgradeFixture) reconnect(t *testing.T, meta termproto.Meta) {
	t.Helper()
	old := f.agent
	f.agent = f.env.connectEnabled(old.priv, meta)
	if code := old.waitClosed(3 * time.Second); code != termproto.CloseSuperseded {
		t.Fatalf("旧连接应被新连接取代: %d", code)
	}
}

// markExited 把假 Agent 上的一个会话标成已结束（进程退出但会话还在列表里），并向 Hub 重新上报会话列表。
func (a *fakeAgent) markExited(sid string) {
	a.mu.Lock()
	if s := a.sessions[sid]; s != nil {
		s.info.State = termproto.SessionExited
	}
	a.mu.Unlock()
	a.sendSessions()
}

func (f *webUpgradeFixture) windowOpen() bool {
	f.env.t.Helper()
	w, _ := hostViewOf(f.env.t, f.env, f.agent.code)["upgrade_window"].(map[string]any)
	return w != nil
}

func (f *webUpgradeFixture) noRequestSent(t *testing.T) {
	t.Helper()
	time.Sleep(100 * time.Millisecond) // 给「万一发出去了」留出到达的时间
	if n := len(f.frames()); n != 0 {
		t.Fatalf("不该向 Agent 发出任何升级请求，实际 %d 个", n)
	}
	if f.windowOpen() {
		t.Fatal("被拒绝的请求不该开启升级窗口")
	}
	if j := f.job(); j != nil {
		t.Fatalf("被拒绝的请求不该留下升级记录: %v", j)
	}
}

func TestWebUpgradeHappyPath(t *testing.T) {
	f := newWebUpgradeFixture(t, nil)
	e, a := f.env, f.agent

	// 发起前：能力可见，没有请求，没有升级窗口。
	v := hostViewOf(t, e, a.code)
	if v["web_upgrade"] != "on" || v["upgrade_job"] != nil || v["upgrade_window"] != nil {
		t.Fatalf("发起前的主机视图不对: %v", v)
	}

	res := f.post(nil)
	if res.Status != http.StatusOK {
		t.Fatalf("发起升级失败: %d %s", res.Status, res.Raw)
	}
	job, _ := res.Body["upgrade_job"].(map[string]any)
	if job["state"] != "requested" || job["from"] != "v1.6.0" || job["to"] != "v1.7.0" {
		t.Fatalf("请求内容不对: %v", job)
	}

	// Agent 收到的帧：合规、编号一致、带目标版本与这个平台程序的 SHA-256；票据正是这台主机升级窗口的票据，并且真能取到程序。
	eventually(t, 3*time.Second, "Agent 收到升级请求", func() bool { return len(f.frames()) == 1 })
	fr := f.frames()[0]
	if err := fr.Validate(); err != nil {
		t.Fatalf("发给 Agent 的帧不合规: %v (%+v)", err, fr)
	}
	sum := sha256.Sum256(f.linux)
	if fr.ID != job["id"] || fr.Version != "v1.7.0" || fr.SHA256 != hex.EncodeToString(sum[:]) {
		t.Fatalf("帧的内容不对: %+v", fr)
	}
	win, _ := hostViewOf(t, e, a.code)["upgrade_window"].(map[string]any)
	if win == nil || win["ticket"] != fr.Ticket || win["base_version"] != "v1.6.0" {
		t.Fatalf("应为这次升级开启升级窗口: %v", win)
	}
	st, _, body := e.callRaw("GET", "/i/"+fr.Ticket+"/relaymesh-agent-linux-amd64")
	if st != http.StatusOK || !bytes.Equal(body, f.linux) {
		t.Fatalf("用帧里的票据应能取到这个平台的程序: %d (%d 字节)", st, len(body))
	}

	// 进展只往前走。
	f.report(fr.ID, termproto.UpgradeStageDownloading, "", "")
	f.waitState("downloading")
	f.report(fr.ID, termproto.UpgradeStageHandoff, "", "")
	f.waitState("handoff")
	f.report(fr.ID, termproto.UpgradeStageDownloading, "", "") // 乱序的旧进展不能把状态倒回去
	time.Sleep(100 * time.Millisecond)
	if got := f.jobState(); got != "handoff" {
		t.Fatalf("乱序的进展帧不应让状态倒退: %s", got)
	}

	// Agent 被重启，带着新版本回来：请求完成，窗口自动关闭，票据失效。
	up := f.meta
	up.Ver = "v1.7.0"
	e.connectEnabled(a.priv, up)
	f.waitState("done")
	j := f.job()
	if j["to"] != "v1.7.0" || j["from"] != "v1.6.0" {
		t.Fatalf("完成后的请求内容不对: %v", j)
	}
	eventually(t, 2*time.Second, "升级窗口自动关闭", func() bool { return !f.windowOpen() })
	if st, _, _ := e.callRaw("GET", "/i/"+fr.Ticket+"/relaymesh-agent-linux-amd64"); st != http.StatusNotFound {
		t.Fatalf("升级完成后票据应失效: %d", st)
	}
	v = hostViewOf(t, e, a.code)
	if _, has := v["upgrade_available"]; has || v["agent_version"] != "v1.7.0" {
		t.Fatalf("升级后不应再提醒，版本应已更新: %v", v)
	}

	// 审计：发起、交接、完成都有；谁发起的有记录；票据与校验值不在其中。
	eventually(t, 3*time.Second, "审计写入", func() bool {
		return auditHas(t, e, EvWebUpgrade, "request id="+fr.ID) && auditHas(t, e, EvWebUpgrade, "handoff id="+fr.ID) && auditHas(t, e, EvWebUpgrade, "done id="+fr.ID)
	})
	logs := e.auditEvents(EvWebUpgrade)
	if len(logs) != 3 {
		t.Fatalf("应有 3 条升级审计（发起 / 交接 / 完成），实际 %d: %+v", len(logs), logs)
	}
	for _, l := range logs {
		if strings.Contains(l.Detail, fr.Ticket) || strings.Contains(l.Detail, fr.SHA256) {
			t.Fatalf("审计里不应出现票据或校验值: %+v", l)
		}
		if l.MachineCode != a.code {
			t.Fatalf("审计应记在这台主机名下: %+v", l)
		}
	}
	var requester string
	for _, l := range logs {
		if strings.HasPrefix(l.Detail, "request ") {
			requester = l.Actor
		}
	}
	if requester != testUser {
		t.Fatalf("发起审计应记录是谁发起的，实际 %q", requester)
	}
}

func TestWebUpgradeRefusals(t *testing.T) {
	cases := []struct {
		name     string
		mutate   func(*termproto.Meta)
		noDist   bool // 分发目录里没有 VERSION
		wantCode string
	}{
		{"不支持（旧版本 / 前台运行）", func(m *termproto.Meta) { m.WebUpgrade = "" }, false, "not_supported"},
		{"主人没有允许", func(m *termproto.Meta) { m.WebUpgrade = termproto.WebUpgradeOff }, false, "not_allowed"},
		{"已是服务端自带的版本", func(m *termproto.Meta) { m.Ver = "v1.7.0" }, false, "up_to_date"},
		{"比服务端自带的更新", func(m *termproto.Meta) { m.Ver = "v1.8.0" }, false, "up_to_date"},
		{"服务端不知道自带版本", nil, true, "no_target_version"},
		{"没有适用于这个平台的程序", func(m *termproto.Meta) { m.Plat = "linux-arm64" }, false, "no_asset"},
		{"主机没有报告平台", func(m *termproto.Meta) { m.Plat = "" }, false, "no_asset"},
		{"平台写法不合规", func(m *termproto.Meta) { m.Plat = "../../etc/passwd" }, false, "no_asset"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newWebUpgradeFixture(t, tc.mutate)
			if tc.noDist {
				// 删掉 VERSION：服务端无法判断该升到哪个版本。
				if err := removeDistVersion(f.env); err != nil {
					t.Fatal(err)
				}
			}
			res := f.post(nil)
			if res.Status != http.StatusConflict || res.str("code") != tc.wantCode {
				t.Fatalf("应以 %s 拒绝: %d %s", tc.wantCode, res.Status, res.Raw)
			}
			if res.str("error") == "" {
				t.Fatalf("拒绝时应给出人能读懂的说明: %s", res.Raw)
			}
			f.noRequestSent(t)
		})
	}
}

// 主机不在线（连接已断开）时不能发起。
func TestWebUpgradeNeedsTheAgentOnline(t *testing.T) {
	f := newWebUpgradeFixture(t, nil)
	f.agent.close()
	eventually(t, 3*time.Second, "Hub 得知 Agent 已离线", func() bool { return f.env.hub.onlineAgent(f.agent.code) == nil })
	res := f.post(nil)
	if res.Status != http.StatusConflict || res.str("code") != "not_online" {
		t.Fatalf("离线的主机应以 not_online 拒绝: %d %s", res.Status, res.Raw)
	}
}

// 没有安装源 / 主机不存在 / 没有解锁：与升级窗口一样的前置检查。
func TestWebUpgradeGuards(t *testing.T) {
	f := newWebUpgradeFixture(t, nil)
	e := f.env
	if res := e.call("POST", f.url(), nil, "Authorization", humanAuth(testUser)); res.Status == http.StatusOK {
		t.Fatal("发起升级需要解锁令牌")
	}
	if res := e.call("POST", "/api/v1/terminal/hosts/RM-ZZZZ-ZZZZ-ZZZZ-ZZZZ/web-upgrade", nil, e.admin()...); res.Status != http.StatusNotFound && res.Status != http.StatusBadRequest {
		t.Fatalf("不存在的主机应拒绝: %d", res.Status)
	}
	f.noRequestSent(t)

	// 没有分发目录的服务端：明确说明，而不是悄悄失败。
	bare := newTestEnv(t)
	m := testMeta("bare-box")
	m.Ver, m.Plat, m.WebUpgrade = "v1.6.0", "linux-amd64", termproto.WebUpgradeOn
	a := bare.enrollAgent(m, "")
	res := bare.call("POST", "/api/v1/terminal/hosts/"+a.code+"/web-upgrade", nil, bare.admin()...)
	if res.Status != http.StatusConflict || res.str("code") != "install_source_unavailable" {
		t.Fatalf("没有安装源时应明确拒绝: %d %s", res.Status, res.Raw)
	}
}

// 有正在运行的会话时必须由管理员确认；已结束的会话不算；确认后才发出请求。
func TestWebUpgradeAsksBeforeInterruptingSessions(t *testing.T) {
	f := newWebUpgradeFixture(t, nil)
	e, a := f.env, f.agent
	hdr := e.admin()
	e.openSession(a, hdr, "正在跑的任务")
	dead := e.openSession(a, hdr, "已经结束的")
	a.markExited(dead)
	eventually(t, 3*time.Second, "Hub 看到一个运行中、一个已结束的会话", func() bool {
		running, exited := 0, 0
		for _, s := range e.hub.onlineAgent(a.code).sessionList() {
			switch s.State {
			case termproto.SessionRunning:
				running++
			case termproto.SessionExited:
				exited++
			}
		}
		return running == 1 && exited == 1
	})

	res := f.post(nil)
	if res.Status != http.StatusConflict || res.str("code") != "sessions_active" {
		t.Fatalf("有运行中的会话时应要求确认: %d %s", res.Status, res.Raw)
	}
	sess, _ := res.Body["sessions"].(map[string]any)
	if sess["running"] != float64(1) || sess["persistent"] != false {
		t.Fatalf("确认提示里应说明有 1 个运行中的会话、且会话随 Agent 同生共死: %v", sess)
	}
	f.noRequestSent(t)

	// 会话由守护进程持有时，提示里如实说明升级不会打断它们（仍然要确认）。
	a.send(termproto.Holder{T: termproto.THolder, State: termproto.HolderReady, Persistent: true, Version: "test"})
	eventually(t, 3*time.Second, "Hub 得知守护进程就绪", func() bool {
		hv := e.hub.onlineAgent(a.code).holderView()
		return hv != nil && hv.Persistent
	})
	res = f.post(map[string]any{"confirm_sessions": false})
	sess, _ = res.Body["sessions"].(map[string]any)
	if res.Status != http.StatusConflict || sess["persistent"] != true {
		t.Fatalf("守护进程持有会话时应如实说明: %d %s", res.Status, res.Raw)
	}

	// 确认后才发出请求；确认这件事进审计。
	id := f.requestWith(t, map[string]any{"confirm_sessions": true})
	eventually(t, 3*time.Second, "审计里记录了确认过的会话数", func() bool {
		return auditHas(t, e, EvWebUpgrade, "request id="+id) && auditHas(t, e, EvWebUpgrade, "running=1") && auditHas(t, e, EvWebUpgrade, "confirmed=true")
	})
	// 会话本身没有被动过。
	if got := e.hub.onlineAgent(a.code).sessionList(); len(got) != 2 {
		t.Fatalf("发起升级不应改动会话: %+v", got)
	}
}

func TestWebUpgradeSessionSummaryCountsWhatWouldBeInterrupted(t *testing.T) {
	if (WebUpgradeSessions{}).needsConfirm() {
		t.Fatal("什么都没有时不需要确认")
	}
	if !(WebUpgradeSessions{Running: 1}).needsConfirm() || !(WebUpgradeSessions{ModelRunning: 1}).needsConfirm() {
		t.Fatal("有运行中的会话或模型运行时都需要确认")
	}
	if (WebUpgradeSessions{Attached: 1, Alt: 1}).needsConfirm() {
		t.Fatal("附着数与全屏程序数只是对运行中会话的补充说明，本身不触发确认")
	}
}

// 同一台主机同时只有一次请求；同时到达的多次点击只有一个成功；过了放弃时限才能重新发起。
func TestWebUpgradeIsExclusive(t *testing.T) {
	f := newWebUpgradeFixture(t, nil)
	e := f.env
	hdr := e.admin()

	const clicks = 8
	results := make([]apiResult, clicks)
	var wg sync.WaitGroup
	for i := range results {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i] = e.call("POST", f.url(), nil, hdr...)
		}(i)
	}
	wg.Wait()
	ok, busy := 0, 0
	for _, r := range results {
		switch {
		case r.Status == http.StatusOK:
			ok++
		case r.Status == http.StatusConflict && r.str("code") == "in_progress":
			busy++
			if j, _ := r.Body["upgrade_job"].(map[string]any); j == nil || j["state"] != "requested" {
				t.Errorf("in_progress 应带上正在进行的那一次: %s", r.Raw)
			}
		default:
			t.Errorf("意外的结果: %d %s", r.Status, r.Raw)
		}
	}
	if ok != 1 || busy != clicks-1 {
		t.Fatalf("同时到达的点击应只有一个成功: ok=%d busy=%d", ok, busy)
	}
	eventually(t, 3*time.Second, "Agent 收到升级请求", func() bool { return len(f.frames()) == 1 })
	time.Sleep(150 * time.Millisecond)
	if n := len(f.frames()); n != 1 {
		t.Fatalf("Agent 应只收到一个请求，实际 %d", n)
	}
	first := f.frames()[0]

	// 请求停滞：5 分钟没有进展，界面标成「停滞」，但仍然占着（不能再点）。
	e.clock.Advance(webUpgradeStall + time.Second)
	if j := f.job(); j["stalled"] != true {
		t.Fatalf("5 分钟没有进展应标成停滞: %v", j)
	}
	if res := f.post(nil); res.Status != http.StatusConflict || res.str("code") != "in_progress" {
		t.Fatalf("停滞中仍应占着: %d %s", res.Status, res.Raw)
	}

	// 10 分钟没有进展：当作已放弃，可以重新发起——新请求、新编号、新的升级窗口。
	e.clock.Advance(webUpgradeExpire)
	id := f.request(t)
	if id == first.ID {
		t.Fatal("重新发起应有新的请求编号")
	}
	if fr := f.frames(); len(fr) != 2 || fr[1].Ticket == first.Ticket {
		t.Fatalf("过期的升级窗口应换发新的票据: %+v", fr)
	}
}

// Agent 拒绝或失败：请求结束、窗口关闭、原因如实显示；之后可以重新发起。迟到或不认识的进展帧不起作用。
func TestWebUpgradeFailureIsReported(t *testing.T) {
	f := newWebUpgradeFixture(t, nil)
	e := f.env
	id := f.request(t)

	// 别的编号 / 编号格式不对的进展：忽略。
	f.report(strings.Repeat("a", 32), termproto.UpgradeStageFailed, "checksum", "别人的")
	f.report("not-an-id", termproto.UpgradeStageFailed, "checksum", "格式不对")
	f.report(id, "weird-stage", "", "")
	time.Sleep(150 * time.Millisecond)
	if got := f.jobState(); got != "requested" {
		t.Fatalf("不相干的进展帧不应改变状态: %s", got)
	}

	f.report(id, termproto.UpgradeStageFailed, termproto.UpgradeCodeChecksum, "下载内容与服务端给出的 SHA-256 不一致")
	f.waitState("failed")
	j := f.job()
	if j["code"] != "checksum" || !strings.Contains(j["detail"].(string), "SHA-256") || j["rolled_back"] == true {
		t.Fatalf("失败的原因应如实显示: %v", j)
	}
	eventually(t, 2*time.Second, "升级窗口随失败关闭", func() bool { return !f.windowOpen() })
	eventually(t, 3*time.Second, "失败进审计", func() bool {
		return auditHas(t, e, EvWebUpgrade, "failed id="+id+" code=checksum")
	})

	// 已结束的请求不会被迟到的帧改写。
	f.report(id, termproto.UpgradeStageHandoff, "", "")
	time.Sleep(150 * time.Millisecond)
	if got := f.jobState(); got != "failed" {
		t.Fatalf("已结束的请求不应被改写: %s", got)
	}

	// 主人拒绝：同样如实显示，且不挡着下一次。
	second := f.request(t)
	if second == id {
		t.Fatal("重新发起应有新的请求编号")
	}
	f.report(second, termproto.UpgradeStageRefused, termproto.UpgradeCodeNotAllowed, "主机所有者没有允许从 Web 升级")
	f.waitState("failed")
	if j := f.job(); j["code"] != "not_allowed" {
		t.Fatalf("被主人拒绝应如实显示: %v", j)
	}
}

// 新版本没有连上服务端，升级进程把旧版本还原了：旧版本带着回滚记录重新上线，请求应被判定为失败（已还原）。
// 同一版本、没有回滚记录的重连（网络闪断）以及早于本次请求的旧记录，都不能改变请求的状态。
func TestWebUpgradeRollbackIsRecognised(t *testing.T) {
	f := newWebUpgradeFixture(t, nil)
	e, a := f.env, f.agent
	id := f.request(t)
	f.report(id, termproto.UpgradeStageHandoff, "", "")
	f.waitState("handoff")

	// 网络闪断：同一版本、没有任何记录。
	f.reconnect(t, f.meta)
	// 带着一份早于本次请求的旧记录（上一次升级留下的）。
	stale := f.meta
	stale.LastUpgrade = &termproto.UpgradeNote{Outcome: termproto.UpgradeOutcomeRolledBack, From: "v1.6.0", To: "v1.6.5", At: e.clock.Now().Add(-time.Hour).Unix(), Detail: "上一次的"}
	f.reconnect(t, stale)
	time.Sleep(200 * time.Millisecond)
	if got := f.jobState(); got != "handoff" {
		t.Fatalf("闪断与旧记录都不应改变请求状态: %s", got)
	}

	// 这一次的回滚记录。
	rolled := f.meta
	rolled.LastUpgrade = &termproto.UpgradeNote{
		Outcome: termproto.UpgradeOutcomeRolledBack, From: "v1.6.0", To: "v1.7.0", At: e.clock.Now().Unix(),
		Detail: "新版本 90 秒内没有连上服务端，已还原为旧版本",
	}
	f.reconnect(t, rolled)
	f.waitState("failed")
	j := f.job()
	if j["rolled_back"] != true || j["code"] != "rolled_back" || !strings.Contains(j["detail"].(string), "90 秒") {
		t.Fatalf("应判定为失败且已还原: %v", j)
	}
	v := hostViewOf(t, e, a.code)
	note, _ := v["last_upgrade"].(map[string]any)
	if note["outcome"] != "rolled_back" || v["agent_version"] != "v1.6.0" {
		t.Fatalf("主机视图应带上主机记录的上一次升级结果，版本仍是旧版本: %v", v)
	}
	eventually(t, 3*time.Second, "回滚进审计", func() bool {
		return auditHas(t, e, EvWebUpgrade, "failed id="+id+" code=rolled_back rolled_back=true")
	})
	eventually(t, 2*time.Second, "升级窗口随失败关闭", func() bool { return !f.windowOpen() })

	// 之后可以重新发起；这一次旧记录（早于新请求）不会误伤它。
	e.clock.Advance(5 * time.Minute)
	again := f.request(t)
	f.reconnect(t, rolled) // 仍带着上一次的回滚记录重连
	time.Sleep(200 * time.Millisecond)
	if got := f.jobState(); got != "requested" {
		t.Fatalf("上一次的回滚记录不应判定新请求失败: %s (%s)", got, again)
	}
}

// 升级进程没能完成替换或重启（Agent 一直活着）时，Agent 自己会报告 failed；没有报告就一直停滞，不会被误判成功。
func TestWebUpgradeNeverSucceedsWithoutAVersionChange(t *testing.T) {
	f := newWebUpgradeFixture(t, nil)
	id := f.request(t)
	f.report(id, termproto.UpgradeStageHandoff, "", "")
	f.waitState("handoff")
	f.env.clock.Advance(webUpgradeStall + time.Minute)
	j := f.job()
	if j["state"] != "handoff" || j["stalled"] != true {
		t.Fatalf("没有结果时应停滞而不是成功: %v", j)
	}
	f.report(id, termproto.UpgradeStageFailed, termproto.UpgradeCodeApply, "systemctl restart 失败")
	f.waitState("failed")
}

// 删除（或停用）主机后，这台主机的请求记录一并清掉；保留期过后也会被清理。
func TestWebUpgradeStateIsDroppedWithTheHost(t *testing.T) {
	f := newWebUpgradeFixture(t, nil)
	e, a := f.env, f.agent
	f.request(t)
	if e.hub.webUpgrades.view(a.code, e.hub.now()) == nil {
		t.Fatal("发起后应有记录")
	}
	if res := e.call("DELETE", "/api/v1/terminal/hosts/"+a.code, nil, e.admin()...); res.Status != http.StatusOK {
		t.Fatalf("删除主机失败: %d %s", res.Status, res.Raw)
	}
	if e.hub.webUpgrades.view(a.code, e.hub.now()) != nil {
		t.Fatal("删除主机后不应还留着升级记录")
	}

	// 保留期：完成的只留 10 分钟，没有结果的留 1 小时，失败的留 24 小时（原因值得事后还能看到）。
	var jobs upgradeJobs
	now := time.Now()
	for i, state := range []string{UpgradeJobRequested, UpgradeJobDone, UpgradeJobFailed} {
		code := "RM-" + string(rune('1'+i))
		jobs.claim(&upgradeJob{id: "j" + code, code: code, state: state, started: now, updated: now}, now)
	}
	present := func() (inflight, done, failed bool) {
		return jobs.view("RM-1", now) != nil, jobs.view("RM-2", now) != nil, jobs.view("RM-3", now) != nil
	}
	jobs.sweep(now.Add(webUpgradeKeepDone - time.Minute))
	if a, b, c := present(); !a || !b || !c {
		t.Fatalf("保留期内都不应清理: %v %v %v", a, b, c)
	}
	jobs.sweep(now.Add(webUpgradeKeepDone + time.Minute))
	if a, b, c := present(); !a || b || !c {
		t.Fatalf("10 分钟后只应清掉完成的: %v %v %v", a, b, c)
	}
	jobs.sweep(now.Add(webUpgradeKeepInflight + time.Minute))
	if a, b, c := present(); a || b || !c {
		t.Fatalf("1 小时后应再清掉没有结果的: %v %v %v", a, b, c)
	}
	jobs.sweep(now.Add(webUpgradeKeepFailed + time.Minute))
	if a, b, c := present(); a || b || c {
		t.Fatalf("24 小时后失败的也应清掉: %v %v %v", a, b, c)
	}
}

// 已结束的请求不挡着新的请求（claim 只看未结束且未过期的）。
func TestUpgradeJobsClaimIgnoresFinishedAndExpired(t *testing.T) {
	var jobs upgradeJobs
	now := time.Now()
	mk := func(id, state string, updated time.Time) *upgradeJob {
		return &upgradeJob{id: id, code: "RM-1", state: state, started: updated, updated: updated}
	}
	if jobs.claim(mk("1", UpgradeJobRequested, now), now) != nil {
		t.Fatal("第一次登记应成功")
	}
	if cur := jobs.claim(mk("2", UpgradeJobRequested, now), now); cur == nil || cur.id != "1" {
		t.Fatalf("已有未结束的请求时应返回它: %+v", cur)
	}
	jobs.advance("RM-1", "1", now, func(j *upgradeJob) { j.state = UpgradeJobFailed })
	if jobs.claim(mk("3", UpgradeJobRequested, now), now) != nil {
		t.Fatal("已结束的请求不应挡着新的请求")
	}
	later := now.Add(webUpgradeExpire + time.Second)
	if jobs.claim(mk("4", UpgradeJobRequested, later), later) != nil {
		t.Fatal("过期（被放弃）的请求不应挡着新的请求")
	}
	// 撤销只撤销自己登记的那一个。
	jobs.abort("RM-1", "3")
	if jobs.view("RM-1", later) == nil {
		t.Fatal("编号对不上的撤销不应生效")
	}
	jobs.abort("RM-1", "4")
	if jobs.view("RM-1", later) != nil {
		t.Fatal("撤销应移除刚登记的请求")
	}
}

// removeDistVersion 删掉测试环境分发目录里的 VERSION 文件。
func removeDistVersion(e *testEnv) error {
	return os.Remove(filepath.Join(e.hub.dist.Dir(), "VERSION"))
}
