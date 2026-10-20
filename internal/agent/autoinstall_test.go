package agent

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/teacat99/RelayMesh/v2/internal/relsign"
	"github.com/teacat99/RelayMesh/v2/internal/termproto"
)

// 静默自动升级（B 层）的 Agent 一侧。要钉住的几件事：
//   - 签名对不上 / 没有可信公钥 / 设置读不清 / 同意被收回：绝不运行下载到的程序，也不交接；
//   - 一律是 Agent 先发请求，Hub 才给下载凭据；没有对应请求的批准被丢弃，版本对不上的批准被忽略；
//   - 尝试记录先于下载写好，同一个版本最多试两次；记录写不下去就放弃；
//   - 时间段之外不请求，睡到时间段开始；提示模式永远只检查。

// silentHarness 在 upgradeHarness（假服务端 + 配好的 upgrader）之上，加一个静默模式的检查器和一台按剧本回答的假 Hub。
type silentHarness struct {
	t       *testing.T
	h       *upgradeHarness
	c       *autoChecker
	dataDir string
	pub     ed25519.PublicKey
	priv    ed25519.PrivateKey

	settings    atomic.Value // AutoSettings
	hasSettings atomic.Bool
	mode        atomic.Value // string
	verifies    atomic.Int32 // 「运行新程序核对版本」被调用的次数
	attemptErr  atomic.Value // error：让「记一次尝试」失败

	mu       sync.Mutex
	frames   []string
	requests []termproto.AutoUpgradeRequest
	statuses []termproto.UpgradeStatus
	notes    []AutoCheckNote
	offer    func() termproto.AutoUpgradeOffer
	grant    func(termproto.AutoUpgradeRequest) (termproto.AutoUpgradeGrant, bool) // false：Hub 不回应
}

func newSilentHarness(t *testing.T) *silentHarness {
	t.Helper()
	s := &silentHarness{t: t, dataDir: t.TempDir()}
	s.pub, s.priv = testKeyPair(1)
	s.setSettings(AutoSettings{Mode: termproto.AutoUpgradeSilent, TrustKeys: []ed25519.PublicKey{s.pub}})
	s.mode.Store(termproto.AutoUpgradeSilent)
	s.offer = func() termproto.AutoUpgradeOffer {
		return termproto.AutoUpgradeOffer{Status: termproto.AutoOfferAvailable, Version: upgradeTestVersion}
	}
	s.grant = func(req termproto.AutoUpgradeRequest) (termproto.AutoUpgradeGrant, bool) {
		return s.goodGrant(req), true
	}

	s.h = newUpgradeHarness(t, []byte("silent agent payload"), func(cfg *SelfUpgrade) {
		cfg.AutoMode = func() string { return s.mode.Load().(string) }
		cfg.AutoSettings = func() (AutoSettings, bool) {
			if !s.hasSettings.Load() {
				return AutoSettings{}, false
			}
			return s.settings.Load().(AutoSettings), true
		}
		cfg.AutoAttempts = func() AutoAttempts { return ReadAutoAttempts(s.dataDir) }
		cfg.AutoAttempted = func(version string) error {
			if e := s.attemptErr.Load(); e != nil {
				return e.(error)
			}
			return RecordAutoAttempt(s.dataDir, version, time.Now())
		}
		cfg.AutoChecked = func(n AutoCheckNote) { s.mu.Lock(); s.notes = append(s.notes, n); s.mu.Unlock() }
		cfg.Verify = func(context.Context, string) (string, error) {
			s.verifies.Add(1)
			return upgradeTestVersion, nil
		}
	})
	s.c = newAutoChecker(s.h.cfg, "1.0.0", t.Logf)
	s.c.upg = s.h.u
	return s
}

func (s *silentHarness) setSettings(v AutoSettings) { s.settings.Store(v); s.hasSettings.Store(true) }
func (s *silentHarness) setMode(m string)           { s.mode.Store(m) }

// setWindow 设置时间段，并把「现在」拨到本地 2026-07-15 的 hh:mm（选一个没有夏令时跳变的日子）。
func (s *silentHarness) setWindow(text string, hh, mm int) {
	w, err := ParseAutoWindow(text)
	if err != nil {
		s.t.Fatal(err)
	}
	s.setSettings(AutoSettings{Mode: termproto.AutoUpgradeSilent, TrustKeys: []ed25519.PublicKey{s.pub}, Window: w, WindowText: text})
	s.h.clock.Store(time.Date(2026, 7, 15, hh, mm, 0, 0, time.Local).UnixNano())
}

func (s *silentHarness) sign(version, platform, sum string) string {
	s.t.Helper()
	sig, err := relsign.Sign(s.priv, version, platform, sum)
	if err != nil {
		s.t.Fatal(err)
	}
	return sig
}

// goodGrant 是 Hub 批准一次合规请求时的样子：票据、SHA-256 与发布签名都对得上。
func (s *silentHarness) goodGrant(req termproto.AutoUpgradeRequest) termproto.AutoUpgradeGrant {
	return termproto.AutoUpgradeGrant{
		Status: termproto.AutoGrantGranted, Version: req.Version,
		Ticket: upgradeTestTicket, SHA256: s.h.sum,
		Sig: s.sign(upgradeTestVersion, "linux-amd64", s.h.sum),
	}
}

func (s *silentHarness) grantWith(status string) {
	s.grant = func(req termproto.AutoUpgradeRequest) (termproto.AutoUpgradeGrant, bool) {
		return termproto.AutoUpgradeGrant{Status: status}, true
	}
}

// send 是 Agent 发给 Hub 的通道：记下每一帧，并按剧本替假 Hub 回话（与 relay 一样先过 Sanitized）。
func (s *silentHarness) send(v any) error {
	switch m := v.(type) {
	case termproto.AutoUpgradeCheck:
		s.record("check")
		o := s.offer()
		o.T, o.ID = termproto.TAutoUpgradeOffer, m.ID
		if o2, ok := o.Sanitized(); ok {
			go s.c.deliver(o2)
		}
	case termproto.AutoUpgradeRequest:
		s.record("request")
		s.mu.Lock()
		s.requests = append(s.requests, m)
		s.mu.Unlock()
		if g, reply := s.grant(m); reply {
			g.T, g.ID = termproto.TAutoUpgradeGrant, m.ID
			if g2, ok := g.Sanitized(); ok {
				go s.c.deliverGrant(g2)
			}
		}
	case termproto.UpgradeStatus:
		s.record("status:" + m.Stage + "/" + m.Code)
		s.mu.Lock()
		s.statuses = append(s.statuses, m)
		s.mu.Unlock()
	default:
		return fmt.Errorf("发出了意料之外的帧: %T", v)
	}
	return nil
}

func (s *silentHarness) record(name string) {
	s.mu.Lock()
	s.frames = append(s.frames, name)
	s.mu.Unlock()
}

// inFlight 是检查器里在途的静默升级请求数。
func (s *silentHarness) inFlight() int {
	s.c.mu.Lock()
	defer s.c.mu.Unlock()
	return len(s.c.granting)
}

func (s *silentHarness) timing() autoCheckTiming {
	return autoCheckTiming{First: time.Second, Every: 6 * time.Hour, Retry: 15 * time.Minute, Timeout: 300 * time.Millisecond}
}

func (s *silentHarness) cycle() (time.Duration, bool) {
	s.t.Helper()
	return s.c.cycle(context.Background(), s.timing(), s.send)
}

func (s *silentHarness) wantFrames(want ...string) {
	s.t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	if strings.Join(s.frames, " ") != strings.Join(want, " ") {
		s.t.Fatalf("Agent 发出的帧 = %v，期望 %v", s.frames, want)
	}
}

func (s *silentHarness) lastNote() AutoCheckNote {
	s.t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.notes) == 0 {
		s.t.Fatal("没有写过任何检查结果")
	}
	return s.notes[len(s.notes)-1]
}

// wantNeverRanAnything 断言没有下载、没有运行新程序、没有交接、暂存目录是空的。
func (s *silentHarness) wantNeverRanAnything() {
	s.t.Helper()
	if n := s.verifies.Load(); n != 0 {
		s.t.Fatalf("新程序被运行了 %d 次：没有通过验证的程序绝不能被运行", n)
	}
	if calls := s.h.calls(); len(calls) != 0 {
		s.t.Fatalf("不该交接，却交接了 %d 次", len(calls))
	}
	if files := s.h.stageFiles(); len(files) != 0 {
		s.t.Fatalf("暂存目录里不该留下任何文件: %v", files)
	}
}

func TestSilentUpgradeHappyPath(t *testing.T) {
	s := newSilentHarness(t)
	wait, exact := s.cycle()

	s.wantFrames("check", "request", "status:downloading/", "status:handoff/")
	if len(s.requests) != 1 || s.requests[0].Version != upgradeTestVersion {
		t.Fatalf("应发出一次对 %s 的请求: %+v", upgradeTestVersion, s.requests)
	}
	for _, st := range s.statuses {
		if st.ID != s.requests[0].ID {
			t.Errorf("进展报告应沿用请求的标识（Hub 据此认出这次升级）: %+v", st)
		}
	}
	calls := s.h.calls()
	if len(calls) != 1 || calls[0].version != upgradeTestVersion || string(calls[0].data) != string(s.h.payload) {
		t.Fatalf("应把验过签名的程序原样交接一次: %+v", calls)
	}
	if n := s.verifies.Load(); n != 1 {
		t.Fatalf("验过签名之后应运行一次新程序核对版本，得到 %d 次", n)
	}
	if a := ReadAutoAttempts(s.dataDir); a.Version != upgradeTestVersion || a.Count != 1 {
		t.Fatalf("应记下一次尝试: %+v", a)
	}
	if n := s.lastNote(); n.Install != autoInstallStarted || n.Status != termproto.AutoOfferAvailable || n.Latest != upgradeTestVersion {
		t.Fatalf("检查结果里应记着静默升级已经开始: %+v", n)
	}
	if wait != 15*time.Minute || exact {
		t.Fatalf("交接之后不指望还有下一轮（Agent 会被重启）；万一没重启，15 分钟后再看: %v exact=%v", wait, exact)
	}
}

func TestSilentRequestCarriesOnlyTheIDAndTheVersion(t *testing.T) {
	s := newSilentHarness(t)
	s.cycle()
	raw, err := json.Marshal(s.requests[0])
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	if len(fields) != 3 || fields["t"] != termproto.TAutoUpgradeRequest || fields["id"] == "" || fields["version"] != upgradeTestVersion {
		t.Fatalf("请求里只应有类型、标识与版本（平台与模式以握手为准，Agent 无法借请求索要别的东西）: %s", raw)
	}
}

// ---------- 验签：先验、再执行 ----------

func TestSilentUpgradeRejectsSignaturesThatDoNotBindTheDownload(t *testing.T) {
	_, otherPriv := testKeyPair(2)
	badSig := func(s *silentHarness, version, platform, sum string, priv ed25519.PrivateKey) string {
		sig, err := relsign.Sign(priv, version, platform, sum)
		if err != nil {
			s.t.Fatal(err)
		}
		return sig
	}
	cases := map[string]func(s *silentHarness) string{
		"别的密钥签的": func(s *silentHarness) string {
			return badSig(s, upgradeTestVersion, "linux-amd64", s.h.sum, otherPriv)
		},
		"签的是别的版本（拿旧签名配新版本号）": func(s *silentHarness) string {
			return badSig(s, "9.9.8", "linux-amd64", s.h.sum, s.priv)
		},
		"签的是别的平台": func(s *silentHarness) string {
			return badSig(s, upgradeTestVersion, "linux-arm64", s.h.sum, s.priv)
		},
		"签的是别的内容（下载到的与签名的不是同一份）": func(s *silentHarness) string {
			return badSig(s, upgradeTestVersion, "linux-amd64", strings.Repeat("ab", 32), s.priv)
		},
		"根本不是签名": func(*silentHarness) string { return "rmsig1:0123456789abcdef:not-a-signature" },
	}
	for name, mk := range cases {
		t.Run(name, func(t *testing.T) {
			s := newSilentHarness(t)
			sig := mk(s)
			s.grant = func(req termproto.AutoUpgradeRequest) (termproto.AutoUpgradeGrant, bool) {
				g := s.goodGrant(req)
				g.Sig = sig
				return g, true
			}
			s.cycle()

			s.wantFrames("check", "request", "status:downloading/", "status:failed/signature")
			s.wantNeverRanAnything()
			if s.h.hits.Load() != 1 {
				t.Fatalf("下载到文件之后才验签，应有一次下载: %d", s.h.hits.Load())
			}
			if a := ReadAutoAttempts(s.dataDir); a.Count != 1 {
				t.Fatalf("失败的尝试也要记数: %+v", a)
			}
			if n := s.lastNote(); n.Install != autoInstallFailed {
				t.Fatalf("检查结果里应记着失败: %+v", n)
			}
		})
	}
}

func TestSilentUpgradeStopsAfterTwoFailedAttempts(t *testing.T) {
	s := newSilentHarness(t)
	s.grant = func(req termproto.AutoUpgradeRequest) (termproto.AutoUpgradeGrant, bool) {
		g := s.goodGrant(req)
		g.Sig = "rmsig1:0123456789abcdef:" + strings.Repeat("A", 86)
		return g, true
	}
	s.cycle()
	s.cycle()
	if a := ReadAutoAttempts(s.dataDir); a.Count != 2 || !a.Paused(upgradeTestVersion) {
		t.Fatalf("两次失败之后应暂停: %+v", a)
	}
	hits := s.h.hits.Load()

	// 第三个周期：检查照常，但不再请求、不再下载；记录里说明为什么，并且回到普通的检查周期。
	s.mu.Lock()
	s.frames = nil
	s.mu.Unlock()
	wait, exact := s.cycle()
	s.wantFrames("check")
	if s.h.hits.Load() != hits {
		t.Fatal("暂停之后不应再下载")
	}
	if n := s.lastNote(); n.Install != autoInstallPaused || !strings.Contains(n.Detail, "已暂停") || !strings.Contains(n.Detail, "auto-upgrade enable") {
		t.Fatalf("应说明已暂停并给出出路: %+v", n)
	}
	if wait != 6*time.Hour || exact {
		t.Fatalf("暂停时不必频繁回来: %v exact=%v", wait, exact)
	}

	// 所有者重新 enable 会清掉记录：之后重新获得机会。
	if err := ClearAutoAttempts(s.dataDir); err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	s.frames = nil
	s.mu.Unlock()
	s.cycle()
	s.wantFrames("check", "request", "status:downloading/", "status:failed/signature")
}

func TestSilentUpgradeDoesNotRunTheDownloadUntilItIsSigned(t *testing.T) {
	// 验签发生在「运行新程序核对版本」之前：用一个会记下调用顺序的 Verify 钉住这一点。
	s := newSilentHarness(t)
	var order []string
	var mu sync.Mutex
	s.h.cfg.Verify = func(context.Context, string) (string, error) {
		mu.Lock()
		order = append(order, "run-version")
		mu.Unlock()
		return upgradeTestVersion, nil
	}
	guard := s.c.autoGuard(termproto.AutoUpgradeGrant{Version: upgradeTestVersion, Sig: s.sign(upgradeTestVersion, "linux-amd64", s.h.sum)})
	inner := guard.authenticate
	guard.authenticate = func(file string) error {
		err := inner(file)
		mu.Lock()
		order = append(order, "verify-signature")
		mu.Unlock()
		return err
	}
	g := s.goodGrant(termproto.AutoUpgradeRequest{Version: upgradeTestVersion})
	g.ID = upgradeTestID
	res := s.h.u.runGuarded(context.Background(), g.ToUpgrade(), func(termproto.UpgradeStatus) {}, guard)
	if res != upgradeHandedOff {
		t.Fatalf("result = %v", res)
	}
	if strings.Join(order, ",") != "verify-signature,run-version" {
		t.Fatalf("必须先验签、再运行新程序: %v", order)
	}
}

// 被验证的是磁盘上那份文件的内容，而不是 Hub 口头给的 SHA-256：即使有人把暂存文件换掉，验签也过不了。
func TestSilentAuthenticateHashesTheFileOnDisk(t *testing.T) {
	s := newSilentHarness(t)
	g := s.goodGrant(termproto.AutoUpgradeRequest{Version: upgradeTestVersion})
	guard := s.c.autoGuard(g)

	good := t.TempDir() + "/good"
	if err := writeTestFile(good, s.h.payload); err != nil {
		t.Fatal(err)
	}
	if err := guard.authenticate(good); err != nil {
		t.Fatalf("内容与签名一致应当通过: %v", err)
	}
	swapped := t.TempDir() + "/swapped"
	if err := writeTestFile(swapped, append([]byte("tampered:"), s.h.payload...)); err != nil {
		t.Fatal(err)
	}
	if err := guard.authenticate(swapped); !errors.Is(err, relsign.ErrBadSignature) {
		t.Fatalf("被换过的文件应验签失败: %v", err)
	}
	if err := guard.authenticate(t.TempDir() + "/missing"); err == nil {
		t.Fatal("读不到文件应当失败，而不是放行")
	}
}

// ---------- 信任名单与设置 ----------

func TestSilentUpgradeWithoutTrustedKeysNeverRequests(t *testing.T) {
	s := newSilentHarness(t)
	s.setSettings(AutoSettings{Mode: termproto.AutoUpgradeSilent}) // 没有任何公钥
	wait, exact := s.cycle()
	s.wantFrames("check")
	s.wantNeverRanAnything()
	if n := s.lastNote(); n.Install != autoInstallNoKeys {
		t.Fatalf("应记着没有可信公钥: %+v", n)
	}
	if wait != 6*time.Hour || exact {
		t.Fatalf("wait=%v exact=%v", wait, exact)
	}
}

func TestSilentUpgradeWithAnUnreadableSettingsFileNeverRequests(t *testing.T) {
	s := newSilentHarness(t)
	s.setSettings(AutoSettings{Mode: termproto.AutoUpgradeSilent, TrustKeys: []ed25519.PublicKey{s.pub}, Problem: "设置里的时间段写法不对"})
	s.cycle()
	s.wantFrames("check")
	s.wantNeverRanAnything()
	if n := s.lastNote(); n.Install != autoInstallBadSettings || !strings.Contains(n.Detail, "时间段") {
		t.Fatalf("应记着设置有问题: %+v", n)
	}
}

func TestSilentUpgradeNeedsAWayToRecordAttempts(t *testing.T) {
	for name, tweak := range map[string]func(*SelfUpgrade){
		"读不了记录": func(c *SelfUpgrade) { c.AutoAttempts = nil },
		"写不了记录": func(c *SelfUpgrade) { c.AutoAttempted = nil },
	} {
		t.Run(name, func(t *testing.T) {
			s := newSilentHarness(t)
			tweak(s.h.cfg)
			s.cycle()
			s.wantFrames("check")
			s.wantNeverRanAnything()
			if n := s.lastNote(); n.Install != autoInstallFailed {
				t.Fatalf("应记着无法进行: %+v", n)
			}
		})
	}
}

func TestSilentUpgradeWithoutAnUpgraderOrSettingsDoesNothing(t *testing.T) {
	s := newSilentHarness(t)
	s.c.upg = nil
	s.cycle()
	s.wantFrames("check")
	s.wantNeverRanAnything()

	s2 := newSilentHarness(t)
	s2.hasSettings.Store(false) // 读不到设置（没有同意）
	s2.cycle()
	s2.wantFrames("check")
	s2.wantNeverRanAnything()
}

func TestSilentUpgradeFollowsTheOwnersLatestChoice(t *testing.T) {
	// 检查之后所有者改成了提示模式：同一个周期里就不再往下走。
	s := newSilentHarness(t)
	s.offer = func() termproto.AutoUpgradeOffer {
		s.setSettings(AutoSettings{Mode: termproto.AutoUpgradeNotify}) // 检查的回答到达之前，所有者收回了静默
		return termproto.AutoUpgradeOffer{Status: termproto.AutoOfferAvailable, Version: upgradeTestVersion}
	}
	s.cycle()
	s.wantFrames("check")
	s.wantNeverRanAnything()
}

func TestNotifyModeOnlyEverChecks(t *testing.T) {
	s := newSilentHarness(t)
	s.setMode(termproto.AutoUpgradeNotify)
	s.setSettings(AutoSettings{Mode: termproto.AutoUpgradeNotify})
	for i := 0; i < 3; i++ {
		s.cycle()
	}
	s.wantFrames("check", "check", "check")
	s.wantNeverRanAnything()
	if n := s.lastNote(); n.Install != "" || n.Status != termproto.AutoOfferAvailable {
		t.Fatalf("提示模式没有静默升级的记录: %+v", n)
	}
}

func TestNotifyModeNeverInstallsEvenIfTheSettingsReaderDisagrees(t *testing.T) {
	// 两处读到的同意不一致时（比如正好赶上所有者改文件），以更保守的为准：模式是提示，就不请求。
	s := newSilentHarness(t)
	s.setMode(termproto.AutoUpgradeNotify) // 设置读取方式仍然说「静默、有公钥」
	s.cycle()
	s.wantFrames("check")
	s.wantNeverRanAnything()
}

func TestSilentUpgradeNeverUpgradesToANonNewerVersion(t *testing.T) {
	for _, v := range []string{"1.0.0", "0.9.0", "abc1234"} { // 相同、更旧、无法比较
		t.Run(v, func(t *testing.T) {
			s := newSilentHarness(t)
			s.offer = func() termproto.AutoUpgradeOffer {
				return termproto.AutoUpgradeOffer{Status: termproto.AutoOfferAvailable, Version: v}
			}
			s.cycle()
			s.wantFrames("check")
			s.wantNeverRanAnything()
		})
	}
}

// ---------- 时间段 ----------

func TestSilentUpgradeWaitsForTheWindowToOpen(t *testing.T) {
	s := newSilentHarness(t)
	s.setWindow("03:00-05:00", 2, 0)
	wait, exact := s.cycle()
	s.wantFrames("check")
	s.wantNeverRanAnything()
	if wait != time.Hour || !exact {
		t.Fatalf("应精确地睡到时间段开始: wait=%v exact=%v", wait, exact)
	}
	if n := s.lastNote(); n.Install != autoInstallWaitingWindow || !strings.Contains(n.Detail, "03:00-05:00") || !strings.Contains(n.Detail, "1h0m0s") {
		t.Fatalf("应说明在等时间段: %+v", n)
	}

	// 时间到了，同一份设置就放行。
	s.h.clock.Store(time.Date(2026, 7, 15, 3, 0, 5, 0, time.Local).UnixNano())
	s.cycle()
	s.wantFrames("check", "check", "request", "status:downloading/", "status:handoff/")
	if len(s.h.calls()) != 1 {
		t.Fatal("在时间段内应当升级")
	}
}

func TestSilentUpgradeFarFromTheWindowComesBackToRefreshTheCheck(t *testing.T) {
	s := newSilentHarness(t)
	s.setWindow("03:00-05:00", 12, 0) // 15 小时后才开始，超过检查周期
	wait, exact := s.cycle()
	if wait != 6*time.Hour || exact {
		t.Fatalf("隔得太久时先按检查周期回来刷新结果: wait=%v exact=%v", wait, exact)
	}
}

func TestSilentUpgradeCrossingMidnight(t *testing.T) {
	s := newSilentHarness(t)
	s.setWindow("23:00-02:00", 0, 30)
	s.cycle()
	s.wantFrames("check", "request", "status:downloading/", "status:handoff/")
}

func TestWindowWakeNeverFiresBeforeTheWindowOpens(t *testing.T) {
	for i := 0; i < 200; i++ {
		d := windowWake(time.Hour)
		if d < time.Hour+time.Second || d > time.Hour+90*time.Second {
			t.Fatalf("windowWake(1h) = %v，应在 [1h1s, 1h1m30s]", d)
		}
	}
}

// ---------- 请求 / 批准 ----------

func TestSilentUpgradeIgnoresGrantsItDidNotAskFor(t *testing.T) {
	s := newSilentHarness(t)
	g := s.goodGrant(termproto.AutoUpgradeRequest{Version: upgradeTestVersion})
	g.T, g.ID = termproto.TAutoUpgradeGrant, upgradeTestID
	clean, ok := g.Sanitized()
	if !ok {
		t.Fatal("夹具里的批准应当合规")
	}
	s.c.deliverGrant(clean) // 没有任何在途的请求：丢弃
	if n := s.inFlight(); n != 0 {
		t.Fatalf("不该留下任何在途请求: %d", n)
	}
	// 周期结束之后，同一份批准被重放：同样丢弃。
	s.cycle()
	hits, calls := s.h.hits.Load(), len(s.h.calls())
	s.c.deliverGrant(clean)
	s.c.deliverGrant(termproto.AutoUpgradeGrant{ID: s.requests[0].ID, Status: termproto.AutoGrantGranted, Version: upgradeTestVersion})
	time.Sleep(50 * time.Millisecond)
	if s.h.hits.Load() != hits || len(s.h.calls()) != calls {
		t.Fatal("重放的批准不应触发任何下载或交接")
	}
}

func TestSilentUpgradeIgnoresAGrantForAnotherVersion(t *testing.T) {
	s := newSilentHarness(t)
	s.grant = func(req termproto.AutoUpgradeRequest) (termproto.AutoUpgradeGrant, bool) {
		g := s.goodGrant(req)
		g.Version = "9.9.8" // Hub 想让主机升到另一个版本
		return g, true
	}
	wait, _ := s.cycle()
	s.wantFrames("check", "request")
	s.wantNeverRanAnything()
	if s.h.hits.Load() != 0 {
		t.Fatal("版本对不上的批准不应触发下载")
	}
	if a := ReadAutoAttempts(s.dataDir); a.Count != 0 {
		t.Fatalf("没有开始升级就不记尝试: %+v", a)
	}
	if n := s.lastNote(); n.Install != autoInstallDeferred || !strings.Contains(n.Detail, "不一致") {
		t.Fatalf("应说明版本不一致: %+v", n)
	}
	if wait != 15*time.Minute {
		t.Fatalf("wait = %v", wait)
	}
}

func TestSilentUpgradeHubVerdicts(t *testing.T) {
	cases := []struct {
		status      string
		install     string
		wait        time.Duration
		detailWords []string
	}{
		{termproto.AutoGrantBusy, autoInstallDeferred, 15 * time.Minute, []string{"暂时不批准", "稍后再试"}},
		{termproto.AutoGrantDisabled, autoInstallDeferred, 15 * time.Minute, []string{"总闸"}},
		{termproto.AutoGrantNotAllowed, autoInstallDeferred, 15 * time.Minute, []string{"没有开启静默"}},
		{termproto.AutoGrantStale, autoInstallDeferred, 15 * time.Minute, []string{"重新检查"}},
		{termproto.AutoGrantUnavailable, autoInstallDeferred, 15 * time.Minute, []string{"没有适用于本机"}},
		{termproto.AutoGrantTooSoon, autoInstallDeferred, 15 * time.Minute, []string{"太频繁"}},
		{termproto.AutoGrantUpToDate, autoInstallDeferred, 6 * time.Hour, []string{"已是最新"}},
		{termproto.AutoGrantUnsigned, autoInstallUnsigned, 6 * time.Hour, []string{"没有发布签名", "不会升级到它"}},
	}
	for _, c := range cases {
		t.Run(c.status, func(t *testing.T) {
			s := newSilentHarness(t)
			s.grantWith(c.status)
			wait, exact := s.cycle()
			s.wantFrames("check", "request")
			s.wantNeverRanAnything()
			if s.h.hits.Load() != 0 {
				t.Fatal("没有被批准就不应下载")
			}
			if a := ReadAutoAttempts(s.dataDir); a.Count != 0 {
				t.Fatalf("没有开始升级就不记尝试: %+v", a)
			}
			n := s.lastNote()
			if n.Install != c.install {
				t.Fatalf("install = %q，期望 %q", n.Install, c.install)
			}
			for _, w := range c.detailWords {
				if !strings.Contains(n.Detail, w) {
					t.Errorf("说明里应有 %q: %q", w, n.Detail)
				}
			}
			if wait != c.wait || exact {
				t.Fatalf("wait=%v exact=%v，期望 %v", wait, exact, c.wait)
			}
		})
	}
}

func TestSilentUpgradeWhenTheHubDoesNotAnswerTheRequest(t *testing.T) {
	s := newSilentHarness(t)
	s.grant = func(termproto.AutoUpgradeRequest) (termproto.AutoUpgradeGrant, bool) {
		return termproto.AutoUpgradeGrant{}, false
	}
	wait, _ := s.cycle()
	s.wantFrames("check", "request")
	s.wantNeverRanAnything()
	if n := s.lastNote(); n.Install != autoInstallDeferred || !strings.Contains(n.Detail, "没有回答") {
		t.Fatalf("应记着 Hub 没有回答: %+v", n)
	}
	if wait != 15*time.Minute {
		t.Fatalf("wait = %v", wait)
	}
	if n := s.inFlight(); n != 0 {
		t.Fatalf("超时之后不应留下在途请求: %d", n)
	}
}

// ---------- 记录先于下载；同意在途中被收回 ----------

func TestSilentUpgradeAbandonsWhenTheAttemptCannotBeRecorded(t *testing.T) {
	s := newSilentHarness(t)
	s.attemptErr.Store(errors.New("disk full"))
	s.cycle()
	s.wantFrames("check", "request", "status:refused/record")
	s.wantNeverRanAnything()
	if s.h.hits.Load() != 0 {
		t.Fatal("记不下这次尝试就不能开始下载")
	}
}

func TestSilentUpgradeRecordsTheAttemptBeforeDownloading(t *testing.T) {
	s := newSilentHarness(t)
	seen := make(chan AutoAttempts, 1)
	s.h.cfg.AutoAttempted = func(version string) error {
		if err := RecordAutoAttempt(s.dataDir, version, time.Now()); err != nil {
			return err
		}
		seen <- ReadAutoAttempts(s.dataDir)
		if s.h.hits.Load() != 0 {
			t.Error("记录尝试时不应已经开始下载")
		}
		return nil
	}
	s.cycle()
	select {
	case a := <-seen:
		if a.Count != 1 {
			t.Fatalf("a = %+v", a)
		}
	default:
		t.Fatal("没有记录尝试")
	}
}

func TestSilentUpgradeHonoursTheOwnerRevokingBeforeHandoff(t *testing.T) {
	s := newSilentHarness(t)
	s.h.cfg.Verify = func(context.Context, string) (string, error) {
		s.verifies.Add(1)
		s.setSettings(AutoSettings{Mode: termproto.AutoUpgradeNotify}) // 验过签、核对版本的这一刻，所有者收回了静默
		return upgradeTestVersion, nil
	}
	s.cycle()
	s.wantFrames("check", "request", "status:downloading/", "status:refused/not_allowed")
	if len(s.h.calls()) != 0 {
		t.Fatal("同意被收回之后不应交接")
	}
	if files := s.h.stageFiles(); len(files) != 0 {
		t.Fatalf("收回之后应清掉暂存文件: %v", files)
	}
	if a := ReadAutoAttempts(s.dataDir); a.Count != 1 {
		t.Fatalf("已经开始下载，这次尝试要算: %+v", a)
	}
}

func TestSilentUpgradeHonoursTheTrustedKeysDisappearingBeforeHandoff(t *testing.T) {
	// 验过签、核对版本的这一刻，信任名单没了（所有者撤掉了钉住的公钥）：不交接。
	s := newSilentHarness(t)
	s.h.cfg.Verify = func(context.Context, string) (string, error) {
		s.verifies.Add(1)
		s.setSettings(AutoSettings{Mode: termproto.AutoUpgradeSilent})
		return upgradeTestVersion, nil
	}
	s.cycle()
	s.wantFrames("check", "request", "status:downloading/", "status:refused/not_allowed")
	if len(s.h.calls()) != 0 || len(s.h.stageFiles()) != 0 {
		t.Fatalf("信任名单没了之后不应交接，也不应留下文件: %v %v", s.h.calls(), s.h.stageFiles())
	}
}

func TestSilentUpgradeSharesTheInProgressSlotWithWebUpgrade(t *testing.T) {
	s := newSilentHarness(t)
	if !s.h.u.begin() { // 模拟管理员刚点了「一键升级」，正在进行
		t.Fatal("前提：应能占到名额")
	}
	s.cycle()
	s.wantFrames("check", "request", "status:refused/busy")
	s.wantNeverRanAnything()
	if s.h.hits.Load() != 0 {
		t.Fatal("名额被占着时不应下载")
	}
	if a := ReadAutoAttempts(s.dataDir); a.Count != 0 {
		t.Fatalf("没有占到名额就没有开始，不记尝试: %+v", a)
	}
}

func TestSilentUpgradeDoesNotMixUpWithTheWebUpgradeSwitch(t *testing.T) {
	// Web 一键升级的开关是另一回事：它关着不影响静默升级，它开着也不放宽静默升级的任何条件。
	s := newSilentHarness(t)
	s.h.allowed.Store(false)
	s.cycle()
	s.wantFrames("check", "request", "status:downloading/", "status:handoff/")

	s2 := newSilentHarness(t)
	s2.h.allowed.Store(true)
	s2.setSettings(AutoSettings{Mode: termproto.AutoUpgradeSilent}) // 没有公钥
	s2.cycle()
	s2.wantFrames("check")
	s2.wantNeverRanAnything()
}

func TestWebUpgradeStillTrustsTheHubChecksumAndNeedsNoSignature(t *testing.T) {
	// 重构之后 Web 一键升级的行为不变：管理员亲手点的升级，不要求发布签名。
	s := newSilentHarness(t)
	s.setSettings(AutoSettings{}) // 静默升级的设置（公钥、同意）与它无关
	wantStages(t, s.h.run(s.h.frame()), "downloading/", "handoff/")
	if len(s.h.calls()) != 1 || s.verifies.Load() != 1 {
		t.Fatal("Web 一键升级应当交接，且仍然核对新程序的版本")
	}
}

func TestAutoGuardRequiresOlderWhileTheWebGuardAcceptsDifferentBuilds(t *testing.T) {
	s := newSilentHarness(t)
	web := s.h.u.webGuard()
	auto := s.c.autoGuard(termproto.AutoUpgradeGrant{Version: "abc1234"})
	if web.requireOlder || !auto.requireOlder {
		t.Fatal("静默升级只升级到能证明更新的版本；Web 一键升级允许「版本不同但无法比较」（管理员亲手点的）")
	}
	m := termproto.Upgrade{T: termproto.TUpgrade, ID: upgradeTestID, Version: "abc1234", Ticket: upgradeTestTicket, SHA256: s.h.sum}
	if err := s.h.u.precheck(m, web); err != nil {
		t.Fatalf("Web 一键升级应放行无法比较的版本: %v", err)
	}
	var f *upgradeFail
	if err := s.h.u.precheck(m, auto); !errors.As(err, &f) || f.code != termproto.UpgradeCodeUpToDate {
		t.Fatalf("静默升级应拒绝无法比较的版本: %v", err)
	}
}

func writeTestFile(path string, data []byte) error {
	return os.WriteFile(path, data, 0o600)
}

// ---------- 整个 Agent 的端到端：握手声明 → 检查 → 请求 → 批准 → 下载 → 验签 → 交接 ----------

type silentWire struct {
	hello    termproto.Hello
	requests []termproto.AutoUpgradeRequest
	statuses []termproto.UpgradeStatus
}

// runSilentAgentAgainstScriptedHub 起一个真的 Agent，连上按剧本回话的 Hub：检查回答「有新版本」，请求回答由 grant 决定。
// 等到 Agent 报出终态（handoff / failed / refused）或 deadline 才返回。
func runSilentAgentAgainstScriptedHub(t *testing.T, windowText string, trusted []ed25519.PublicKey, grant func(h *upgradeHarness, req termproto.AutoUpgradeRequest) termproto.AutoUpgradeGrant) (*upgradeHarness, *silentWire, string) {
	t.Helper()
	fastAutoCheckTiming(t)
	dataDir := t.TempDir()
	window, err := ParseAutoWindow(windowText)
	if err != nil {
		t.Fatal(err)
	}
	h := newUpgradeHarness(t, []byte("end to end silent payload"), func(c *SelfUpgrade) {
		c.AutoMode = func() string { return termproto.AutoUpgradeSilent }
		c.AutoSettings = func() (AutoSettings, bool) {
			return AutoSettings{Mode: termproto.AutoUpgradeSilent, TrustKeys: trusted, Window: window, WindowText: windowText}, true
		}
		c.AutoAttempts = func() AutoAttempts { return ReadAutoAttempts(dataDir) }
		c.AutoAttempted = func(v string) error { return RecordAutoAttempt(dataDir, v, time.Now()) }
		c.AutoChecked = func(AutoCheckNote) {}
	})
	wire := &silentWire{}
	var mu sync.Mutex
	terminal := make(chan struct{})
	var once sync.Once
	release := make(chan struct{})
	h.helloCapturingHub(t, func(hello termproto.Hello) { mu.Lock(); wire.hello = hello; mu.Unlock() }, func(ctx context.Context, ws *websocket.Conn) {
		go func() { // 测试看够了就让 Hub 收线，Agent 的这次连接随之结束
			select {
			case <-release:
				_ = ws.Close(websocket.StatusNormalClosure, "done")
			case <-ctx.Done():
			}
		}()
		for {
			_, data, err := ws.Read(ctx)
			if err != nil {
				return
			}
			typ, _ := termproto.ParseType(data)
			switch typ {
			case termproto.TAutoUpgradeCheck:
				var chk termproto.AutoUpgradeCheck
				if json.Unmarshal(data, &chk) == nil {
					sendJSON(t, ctx, ws, termproto.AutoUpgradeOffer{T: termproto.TAutoUpgradeOffer, ID: chk.ID, Status: termproto.AutoOfferAvailable, Version: upgradeTestVersion})
				}
			case termproto.TAutoUpgradeRequest:
				var req termproto.AutoUpgradeRequest
				if json.Unmarshal(data, &req) != nil {
					continue
				}
				mu.Lock()
				wire.requests = append(wire.requests, req)
				mu.Unlock()
				g := grant(h, req)
				g.T, g.ID = termproto.TAutoUpgradeGrant, req.ID
				sendJSON(t, ctx, ws, g)
			case termproto.TUpgradeStatus:
				var st termproto.UpgradeStatus
				if json.Unmarshal(data, &st) != nil {
					continue
				}
				mu.Lock()
				wire.statuses = append(wire.statuses, st)
				mu.Unlock()
				if st.Stage != termproto.UpgradeStageDownloading {
					once.Do(func() { close(terminal) })
				}
			}
		}
	})
	a := newAgentFor(t, h.target, func(o *Options) {
		o.Identity = persistentIdentity(t)
		o.Version = "1.0.0"
		o.SelfUpgrade = h.cfg
	})
	done := connectInBackground(a)
	select {
	case <-terminal:
	case <-time.After(10 * time.Second):
		t.Fatal("Agent 应在 10 秒内走完静默升级（交接或失败）")
	}
	close(release)
	waitConnected(t, done)
	mu.Lock()
	defer mu.Unlock()
	cp := *wire
	cp.requests = append([]termproto.AutoUpgradeRequest(nil), wire.requests...)
	cp.statuses = append([]termproto.UpgradeStatus(nil), wire.statuses...)
	return h, &cp, dataDir
}

func goodSilentGrant(priv ed25519.PrivateKey) func(h *upgradeHarness, req termproto.AutoUpgradeRequest) termproto.AutoUpgradeGrant {
	return func(h *upgradeHarness, req termproto.AutoUpgradeRequest) termproto.AutoUpgradeGrant {
		sig, err := relsign.Sign(priv, upgradeTestVersion, "linux-amd64", h.sum)
		if err != nil {
			panic(err)
		}
		return termproto.AutoUpgradeGrant{Status: termproto.AutoGrantGranted, Version: req.Version, Ticket: upgradeTestTicket, SHA256: h.sum, Sig: sig}
	}
}

func openWindowAroundNow() string {
	now := time.Now()
	return now.Add(-time.Hour).Format("15:04") + "-" + now.Add(time.Hour).Format("15:04")
}

func TestAgentSilentUpgradeEndToEnd(t *testing.T) {
	pub, priv := testKeyPair(1)
	window := openWindowAroundNow()
	h, wire, dataDir := runSilentAgentAgainstScriptedHub(t, window, []ed25519.PublicKey{pub}, goodSilentGrant(priv))

	if wire.hello.Meta.AutoUpgrade != termproto.AutoUpgradeSilent || wire.hello.Meta.AutoWindow != window {
		t.Fatalf("握手应声明静默模式与时间段: %q %q", wire.hello.Meta.AutoUpgrade, wire.hello.Meta.AutoWindow)
	}
	if len(wire.requests) != 1 || wire.requests[0].Version != upgradeTestVersion {
		t.Fatalf("应发出一次对 %s 的请求: %+v", upgradeTestVersion, wire.requests)
	}
	var stages []string
	for _, st := range wire.statuses {
		if st.ID != wire.requests[0].ID {
			t.Errorf("进展报告应沿用请求的标识: %+v", st)
		}
		stages = append(stages, st.Stage+"/"+st.Code)
	}
	if strings.Join(stages, " ") != "downloading/ handoff/" {
		t.Fatalf("进展 = %v", stages)
	}
	calls := h.waitCalls(1)
	if len(calls) != 1 || string(calls[0].data) != string(h.payload) || calls[0].version != upgradeTestVersion {
		t.Fatalf("应把验过签名的程序原样交接一次: %+v", calls)
	}
	if a := ReadAutoAttempts(dataDir); a.Version != upgradeTestVersion || a.Count != 1 {
		t.Fatalf("应记下一次尝试: %+v", a)
	}
}

func TestAgentSilentUpgradeRefusesAForgedSignatureEndToEnd(t *testing.T) {
	pub, _ := testKeyPair(1)
	_, evil := testKeyPair(2) // Hub（或冒充它的人）用自己的密钥签：公钥不在主机信任的名单里
	h, wire, dataDir := runSilentAgentAgainstScriptedHub(t, "", []ed25519.PublicKey{pub}, goodSilentGrant(evil))

	var stages []string
	for _, st := range wire.statuses {
		stages = append(stages, st.Stage+"/"+st.Code)
	}
	if strings.Join(stages, " ") != "downloading/ failed/signature" {
		t.Fatalf("进展 = %v，期望下载之后因签名失败", stages)
	}
	if calls := h.calls(); len(calls) != 0 {
		t.Fatalf("签名不对绝不能交接: %+v", calls)
	}
	if files := h.stageFiles(); len(files) != 0 {
		t.Fatalf("下载的程序应被丢弃: %v", files)
	}
	if a := ReadAutoAttempts(dataDir); a.Count != 1 {
		t.Fatalf("失败的尝试要记数: %+v", a)
	}
}

func TestAgentSilentUpgradeWithoutTrustedKeysNeverEvenAsks(t *testing.T) {
	// 没有可信公钥时，Agent 连请求都不发——Hub 想给它凭据也没有机会。
	fastAutoCheckTiming(t)
	h := newUpgradeHarness(t, []byte("payload"), func(c *SelfUpgrade) {
		c.AutoMode = func() string { return termproto.AutoUpgradeSilent }
		c.AutoSettings = func() (AutoSettings, bool) { return AutoSettings{Mode: termproto.AutoUpgradeSilent}, true }
		c.AutoAttempts = func() AutoAttempts { return AutoAttempts{} }
		c.AutoAttempted = func(string) error { return nil }
		c.AutoChecked = func(AutoCheckNote) {}
	})
	var requests atomic.Int32
	checked := make(chan struct{}, 1)
	release := make(chan struct{})
	h.helloCapturingHub(t, func(termproto.Hello) {}, func(ctx context.Context, ws *websocket.Conn) {
		go func() {
			select {
			case <-release:
				_ = ws.Close(websocket.StatusNormalClosure, "done")
			case <-ctx.Done():
			}
		}()
		for {
			_, data, err := ws.Read(ctx)
			if err != nil {
				return
			}
			typ, _ := termproto.ParseType(data)
			switch typ {
			case termproto.TAutoUpgradeCheck:
				var chk termproto.AutoUpgradeCheck
				if json.Unmarshal(data, &chk) == nil {
					sendJSON(t, ctx, ws, termproto.AutoUpgradeOffer{T: termproto.TAutoUpgradeOffer, ID: chk.ID, Status: termproto.AutoOfferAvailable, Version: upgradeTestVersion})
					select {
					case checked <- struct{}{}:
					default:
					}
				}
			case termproto.TAutoUpgradeRequest:
				requests.Add(1)
			}
		}
	})
	a := newAgentFor(t, h.target, func(o *Options) {
		o.Identity = persistentIdentity(t)
		o.Version = "1.0.0"
		o.SelfUpgrade = h.cfg
	})
	done := connectInBackground(a)
	select {
	case <-checked:
	case <-time.After(5 * time.Second):
		t.Fatal("Agent 应先做版本检查")
	}
	time.Sleep(300 * time.Millisecond) // 给「万一发了请求」留出到达的时间
	if n := requests.Load(); n != 0 {
		t.Fatalf("没有可信公钥时不应发请求: %d", n)
	}
	if h.hits.Load() != 0 {
		t.Fatalf("不应下载: %d", h.hits.Load())
	}
	close(release)
	waitConnected(t, done)
}
