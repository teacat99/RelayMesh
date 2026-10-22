package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/teacat99/RelayMesh/v2/internal/termproto"
)

// 自动升级的检查一侧：本机同意了才问、问到「有新版本」只提示、不盲信 Hub、问不到答案不是错误。

// ---------- 本机保存的检查结果 ----------

func TestAutoCheckNoteRoundTripAndFailsClosed(t *testing.T) {
	dir := t.TempDir()
	if _, ok := ReadAutoCheckNote(dir); ok {
		t.Fatal("没有检查过时读不到结果")
	}
	want := AutoCheckNote{At: 1_800_000_000, Status: termproto.AutoOfferAvailable, Latest: "v1.7.0", Current: "v1.6.0"}
	if err := WriteAutoCheckNote(dir, want); err != nil {
		t.Fatal(err)
	}
	if got, ok := ReadAutoCheckNote(dir); !ok || got != want {
		t.Fatalf("写入后应读到同样的结果: %+v %v", got, ok)
	}
	if err := RemoveAutoCheckNote(dir); err != nil {
		t.Fatal(err)
	}
	if _, ok := ReadAutoCheckNote(dir); ok {
		t.Fatal("删除后读不到")
	}
	if err := RemoveAutoCheckNote(dir); err != nil {
		t.Fatalf("重复删除不应报错: %v", err)
	}
	if err := WriteAutoCheckNote("", want); err == nil {
		t.Fatal("数据目录为空应报错")
	}

	// 文件是磁盘上的内容：读到不合规的东西一律当作没有，展示前版本号也要净化。
	write := func(content string) string {
		d := t.TempDir()
		if err := os.MkdirAll(UpgradeDir(d), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(AutoCheckNotePath(d), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		return d
	}
	for name, content := range map[string]string{
		"空文件":      "",
		"损坏":       `{"at":1`,
		"没有时刻":     `{"status":"up_to_date"}`,
		"没有状态":     `{"at":1}`,
		"不认识的状态":   `{"at":1,"status":"upgrade_now"}`,
		"状态里带了控制符": `{"at":1,"status":"available\u001b[31m"}`,
		"过大":       `{"at":1,"status":"available","pad":"` + strings.Repeat("x", maxAutoCheckNoteBytes) + `"}`,
	} {
		if n, ok := ReadAutoCheckNote(write(content)); ok {
			t.Errorf("%s：不合规的结果应被丢弃: %+v", name, n)
		}
	}
	n, ok := ReadAutoCheckNote(write(`{"at":5,"status":"available","latest":"v1 7 <b>","current":"\u001b[31mv1"}`))
	if !ok || n.Latest != "" || n.Current != "" || n.Status != termproto.AutoOfferAvailable {
		t.Fatalf("不合规的版本号应被去掉而保留其余: %+v %v", n, ok)
	}

	t.Run("符号链接不算", func(t *testing.T) {
		d := t.TempDir()
		if err := os.MkdirAll(UpgradeDir(d), 0o700); err != nil {
			t.Fatal(err)
		}
		target := filepath.Join(t.TempDir(), "elsewhere.json")
		if err := os.WriteFile(target, []byte(`{"at":1,"status":"up_to_date"}`), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, AutoCheckNotePath(d)); err != nil {
			t.Skipf("不能创建符号链接: %v", err)
		}
		if _, ok := ReadAutoCheckNote(d); ok {
			t.Fatal("符号链接不能当作检查结果")
		}
	})
}

func TestDescribeAutoCheck(t *testing.T) {
	cases := []struct {
		mode, status, latest string
		want                 []string
		wantNot              []string
	}{
		{termproto.AutoUpgradeNotify, termproto.AutoOfferAvailable, "v1.7.0", []string{"自动升级（提示）", "新版本 v1.7.0", "本机 v1.6.0", "不会自己下载或安装", "升级"}, nil},
		{termproto.AutoUpgradeSilent, termproto.AutoOfferAvailable, "v1.7.0", []string{"自动升级（静默）", "新版本 v1.7.0"}, []string{"不会自己下载"}},
		{termproto.AutoUpgradeNotify, termproto.AutoOfferUpToDate, "v1.6.0", []string{"已是最新", "v1.6.0"}, nil},
		{termproto.AutoUpgradeNotify, termproto.AutoOfferDisabled, "", []string{"总闸是关的", "Hub 管理员"}, nil},
		{termproto.AutoUpgradeNotify, termproto.AutoOfferUnavailable, "", []string{"没有适用于本机"}, nil},
		{termproto.AutoUpgradeNotify, termproto.AutoOfferNotAllowed, "", []string{"没有开启自动升级"}, nil},
		{termproto.AutoUpgradeNotify, autoCheckNoReply, "", []string{"没有回答", "较旧"}, nil},
	}
	for _, tc := range cases {
		got := describeAutoCheck(tc.mode, "v1.6.0", tc.status, tc.latest)
		for _, w := range tc.want {
			if !strings.Contains(got, w) {
				t.Errorf("%s/%s 的描述里应有 %q: %s", tc.mode, tc.status, w, got)
			}
		}
		for _, w := range tc.wantNot {
			if strings.Contains(got, w) {
				t.Errorf("%s/%s 的描述里不应有 %q: %s", tc.mode, tc.status, w, got)
			}
		}
	}
	if got := describeAutoCheck(termproto.AutoUpgradeNotify, "", termproto.AutoOfferAvailable, "v1.7.0"); !strings.Contains(got, "未知") {
		t.Errorf("本机版本未知时应如实说明: %s", got)
	}
}

// ---------- 检查器 ----------

type checkerFixture struct {
	c     *autoChecker
	mode  atomic.Value
	mu    sync.Mutex
	notes []AutoCheckNote
	logs  []string
	sent  []termproto.AutoUpgradeCheck
}

func newCheckerFixture(t *testing.T, version, mode string) *checkerFixture {
	t.Helper()
	f := &checkerFixture{}
	f.mode.Store(mode)
	cfg := &SelfUpgrade{
		AutoMode:    func() string { return f.mode.Load().(string) },
		AutoChecked: func(n AutoCheckNote) { f.mu.Lock(); f.notes = append(f.notes, n); f.mu.Unlock() },
		Now:         func() time.Time { return time.Unix(1_800_000_000, 0) },
	}
	f.c = newAutoChecker(cfg, version, func(format string, args ...any) {
		f.mu.Lock()
		f.logs = append(f.logs, fmt.Sprintf(format, args...))
		f.mu.Unlock()
	})
	return f
}

// answer 返回一个「收到检查就按给定回答应答」的 send 函数。answer 为 nil 时不应答（模拟 Hub 不认识这个帧）。
func (f *checkerFixture) sender(answer func(id string) *termproto.AutoUpgradeOffer) func(any) error {
	return func(v any) error {
		m, ok := v.(termproto.AutoUpgradeCheck)
		if !ok {
			return fmt.Errorf("发出了不是检查的帧: %T", v)
		}
		f.mu.Lock()
		f.sent = append(f.sent, m)
		f.mu.Unlock()
		if answer != nil {
			if o := answer(m.ID); o != nil {
				go f.c.deliver(*o)
			}
		}
		return nil
	}
}

func (f *checkerFixture) snapshot() (notes []AutoCheckNote, logs []string, sent []termproto.AutoUpgradeCheck) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]AutoCheckNote(nil), f.notes...), append([]string(nil), f.logs...), append([]termproto.AutoUpgradeCheck(nil), f.sent...)
}

func offerOf(status, version string) func(string) *termproto.AutoUpgradeOffer {
	return func(id string) *termproto.AutoUpgradeOffer {
		return &termproto.AutoUpgradeOffer{T: termproto.TAutoUpgradeOffer, ID: id, Status: status, Version: version}
	}
}

func TestAutoCheckAsksOnlyWhileTheOwnerAgrees(t *testing.T) {
	f := newCheckerFixture(t, "v1.6.0", termproto.AutoUpgradeOff)
	ctx := context.Background()
	send := f.sender(offerOf(termproto.AutoOfferUpToDate, "v1.6.0"))

	f.c.checkOnce(ctx, time.Second, send)
	if _, _, sent := f.snapshot(); len(sent) != 0 {
		t.Fatalf("主人没有开启时不应发出任何检查: %+v", sent)
	}

	f.mode.Store(termproto.AutoUpgradeNotify)
	f.c.checkOnce(ctx, time.Second, send)
	notes, _, sent := f.snapshot()
	if len(sent) != 1 || sent[0].T != termproto.TAutoUpgradeCheck || sent[0].Validate() != nil {
		t.Fatalf("开启后应发出一次合规的检查: %+v", sent)
	}
	if len(notes) != 1 || notes[0].Status != termproto.AutoOfferUpToDate {
		t.Fatalf("应记下结果: %+v", notes)
	}

	// 所有者收回之后，下一次检查（每次都现读模式）不再发出。
	f.mode.Store(termproto.AutoUpgradeOff)
	f.c.checkOnce(ctx, time.Second, send)
	if _, _, sent := f.snapshot(); len(sent) != 1 {
		t.Fatalf("收回后不应再发出检查: %d", len(sent))
	}

	// 读到不认识的模式按关闭处理。
	f.mode.Store("garbage")
	f.c.checkOnce(ctx, time.Second, send)
	if _, _, sent := f.snapshot(); len(sent) != 1 {
		t.Fatalf("不认识的模式不应发出检查: %d", len(sent))
	}
}

func TestAutoCheckConclusions(t *testing.T) {
	cases := []struct {
		name       string
		version    string
		status     string
		offered    string
		wantStatus string // 空 = 不是结论，不记录
		wantLatest string
		wantLog    string
		settled    bool // 是不是确定的答案（否则早一点再问）
	}{
		{"Hub 说有新版本，本机自己比较也确实更旧", "v1.6.0", termproto.AutoOfferAvailable, "v1.7.0", termproto.AutoOfferAvailable, "v1.7.0", "新版本 v1.7.0", true},
		{"Hub 说有新版本，但版本其实一样：不盲信", "v1.7.0", termproto.AutoOfferAvailable, "v1.7.0", termproto.AutoOfferUpToDate, "v1.7.0", "已是最新", true},
		{"Hub 说有新版本，但其实是回退到更旧的：版本只升不降", "v1.8.0", termproto.AutoOfferAvailable, "v1.7.0", termproto.AutoOfferUpToDate, "v1.7.0", "已是最新", true},
		{"Hub 说有新版本，但版本无法比较新旧：不当作新版本", "v1.7.0-dev-20261016-aaaaaaa", termproto.AutoOfferAvailable, "v1.7.0-dev-20261016-bbbbbbb", termproto.AutoOfferUpToDate, "v1.7.0-dev-20261016-bbbbbbb", "已是最新", true},
		{"已是最新", "v1.7.0", termproto.AutoOfferUpToDate, "v1.7.0", termproto.AutoOfferUpToDate, "v1.7.0", "已是最新", true},
		{"总闸关着", "v1.6.0", termproto.AutoOfferDisabled, "", termproto.AutoOfferDisabled, "", "总闸是关的", false},
		{"Hub 没有适用的程序", "v1.6.0", termproto.AutoOfferUnavailable, "", termproto.AutoOfferUnavailable, "", "没有适用于本机", false},
		{"Hub 认为本机没开启", "v1.6.0", termproto.AutoOfferNotAllowed, "", termproto.AutoOfferNotAllowed, "", "没有开启自动升级", false},
		{"限流不是结论", "v1.6.0", termproto.AutoOfferTooSoon, "", "", "", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newCheckerFixture(t, tc.version, termproto.AutoUpgradeNotify)
			settled := f.c.checkOnce(context.Background(), time.Second, f.sender(offerOf(tc.status, tc.offered)))
			if settled != tc.settled {
				t.Fatalf("settled = %v，期望 %v", settled, tc.settled)
			}
			notes, logs, sent := f.snapshot()
			if len(sent) != 1 {
				t.Fatalf("应发出一次检查: %d", len(sent))
			}
			if tc.wantStatus == "" {
				if len(notes) != 0 || len(logs) != 0 {
					t.Fatalf("这不是结论，不应记录: notes=%+v logs=%v", notes, logs)
				}
				return
			}
			if len(notes) != 1 || notes[0].Status != tc.wantStatus || notes[0].Latest != tc.wantLatest || notes[0].Current != tc.version || notes[0].At != 1_800_000_000 {
				t.Fatalf("结果 = %+v，期望 status=%s latest=%q current=%s", notes, tc.wantStatus, tc.wantLatest, tc.version)
			}
			if len(logs) != 1 || !strings.Contains(logs[0], tc.wantLog) {
				t.Fatalf("日志应含 %q: %v", tc.wantLog, logs)
			}
		})
	}
}

func TestAutoCheckLogsOnlyWhenTheConclusionChanges(t *testing.T) {
	f := newCheckerFixture(t, "v1.6.0", termproto.AutoUpgradeNotify)
	ctx := context.Background()
	var next atomic.Value
	next.Store(offerOf(termproto.AutoOfferAvailable, "v1.7.0"))
	send := f.sender(func(id string) *termproto.AutoUpgradeOffer {
		return next.Load().(func(string) *termproto.AutoUpgradeOffer)(id)
	})

	for range 3 {
		f.c.checkOnce(ctx, time.Second, send)
	}
	notes, logs, _ := f.snapshot()
	if len(notes) != 3 {
		t.Fatalf("每次检查都应更新本机保存的结果（让「上一次检查」的时间是新的）: %d", len(notes))
	}
	if len(logs) != 1 {
		t.Fatalf("结论没变时只应写一行日志（否则每个周期都刷一遍）: %v", logs)
	}

	next.Store(offerOf(termproto.AutoOfferAvailable, "v1.8.0")) // 又出了更新的版本：这是新消息
	f.c.checkOnce(ctx, time.Second, send)
	next.Store(offerOf(termproto.AutoOfferUpToDate, "v1.6.0"))
	f.c.checkOnce(ctx, time.Second, send)
	if _, logs, _ := f.snapshot(); len(logs) != 3 {
		t.Fatalf("结论变化时应各写一行: %v", logs)
	}
}

func TestAutoCheckNoReplyIsNotAnError(t *testing.T) {
	// Hub 较旧、不认识这个帧：不回答。Agent 记一次「没有回答」，不报错、不重试风暴。
	f := newCheckerFixture(t, "v1.6.0", termproto.AutoUpgradeNotify)
	start := time.Now()
	if f.c.checkOnce(context.Background(), 50*time.Millisecond, f.sender(nil)) {
		t.Fatal("没有回答不是确定的答案：应早一点再问")
	}
	if time.Since(start) < 40*time.Millisecond {
		t.Fatal("应当等满时限再下结论")
	}
	notes, logs, sent := f.snapshot()
	if len(sent) != 1 || len(notes) != 1 || notes[0].Status != autoCheckNoReply || len(logs) != 1 || !strings.Contains(logs[0], "没有回答") {
		t.Fatalf("应记下「没有回答」: sent=%d notes=%+v logs=%v", len(sent), notes, logs)
	}
	if n := f.c.pendingChecks(); n != 0 {
		t.Fatalf("超时之后不应留下等待项: %d", n)
	}
}

func TestAutoCheckDiscardsOffersNobodyAskedFor(t *testing.T) {
	f := newCheckerFixture(t, "v1.6.0", termproto.AutoUpgradeNotify)
	// 凭空来的、别的连接留下的、已经超时的回答：一律丢弃，不阻塞也不记录。
	f.c.deliver(termproto.AutoUpgradeOffer{T: termproto.TAutoUpgradeOffer, ID: strings.Repeat("a", 32), Status: termproto.AutoOfferAvailable, Version: "v9.9.9"})

	var staleID string
	f.c.checkOnce(context.Background(), 30*time.Millisecond, f.sender(func(id string) *termproto.AutoUpgradeOffer { staleID = id; return nil }))
	done := make(chan struct{})
	go func() {
		// 超时之后才到的回答：同一个标识再来一次也不能触发任何东西。
		f.c.deliver(termproto.AutoUpgradeOffer{T: termproto.TAutoUpgradeOffer, ID: staleID, Status: termproto.AutoOfferAvailable, Version: "v9.9.9"})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("deliver 不应阻塞")
	}
	notes, logs, _ := f.snapshot()
	if len(notes) != 1 || notes[0].Status != autoCheckNoReply {
		t.Fatalf("只应有超时那一条结果: %+v", notes)
	}
	for _, l := range logs {
		if strings.Contains(l, "v9.9.9") {
			t.Fatalf("没人问过的回答不应进入日志: %v", logs)
		}
	}
}

func TestAutoCheckStopsWhenTheConnectionBreaks(t *testing.T) {
	f := newCheckerFixture(t, "v1.6.0", termproto.AutoUpgradeNotify)
	if f.c.checkOnce(context.Background(), time.Second, func(any) error { return context.Canceled }) {
		t.Fatal("发不出去不是确定的答案")
	}
	if notes, logs, _ := f.snapshot(); len(notes) != 0 || len(logs) != 0 {
		t.Fatalf("发不出去不是检查结论: %+v %v", notes, logs)
	}
	if n := f.c.pendingChecks(); n != 0 {
		t.Fatalf("发送失败后不应留下等待项: %d", n)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	f.c.checkOnce(ctx, time.Hour, f.sender(nil)) // 已经取消的上下文：不会卡住
}

func TestAutoCheckLoopRepeatsAndRereadsTheOwnersChoice(t *testing.T) {
	f := newCheckerFixture(t, "v1.6.0", termproto.AutoUpgradeNotify)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		f.c.run(ctx, autoCheckTiming{First: 20 * time.Millisecond, Every: 30 * time.Millisecond, Timeout: time.Second},
			f.sender(offerOf(termproto.AutoOfferUpToDate, "v1.6.0")))
	}()
	eventually(t, 5*time.Second, "循环检查了至少三次", func() bool { _, _, s := f.snapshot(); return len(s) >= 3 })

	f.mode.Store(termproto.AutoUpgradeOff) // 所有者在循环运行期间收回了同意
	time.Sleep(100 * time.Millisecond)     // 让在途的一次走完
	_, _, before := f.snapshot()
	time.Sleep(200 * time.Millisecond)
	if _, _, after := f.snapshot(); len(after) != len(before) {
		t.Fatalf("收回之后循环不应再发出检查: %d → %d", len(before), len(after))
	}

	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("上下文取消后循环应结束")
	}
}

func TestAutoCheckAsksAgainSoonUntilItGetsAnAnswerItCanRelyOn(t *testing.T) {
	// Hub 管理员事后才打开总闸：主机不必等满一个周期（6 小时）才知道，而是按较短的间隔再问；拿到确定的答案之后才回到长周期。
	f := newCheckerFixture(t, "v1.6.0", termproto.AutoUpgradeNotify)
	var calls atomic.Int32
	send := f.sender(func(id string) *termproto.AutoUpgradeOffer {
		if calls.Add(1) <= 2 {
			return offerOf(termproto.AutoOfferDisabled, "")(id)
		}
		return offerOf(termproto.AutoOfferUpToDate, "v1.6.0")(id)
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		f.c.run(ctx, autoCheckTiming{First: 10 * time.Millisecond, Every: time.Hour, Retry: 20 * time.Millisecond, Timeout: time.Second}, send)
	}()
	eventually(t, 5*time.Second, "总闸关着时按较短的间隔重试，直到拿到确定的答案", func() bool { _, _, s := f.snapshot(); return len(s) >= 3 })
	time.Sleep(200 * time.Millisecond) // 确定的答案之后回到长周期：不会再有第四次
	notes, _, sent := f.snapshot()
	if len(sent) != 3 {
		t.Fatalf("拿到确定的答案后应回到长周期: %d", len(sent))
	}
	if len(notes) != 3 || notes[0].Status != termproto.AutoOfferDisabled || notes[2].Status != termproto.AutoOfferUpToDate {
		t.Fatalf("结果 = %+v", notes)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("上下文取消后循环应结束")
	}
}

// connectInBackground 与 connectWith 相同，但在后台连接并返回结果通道（connectWith 会在非测试协程里调用 t.Fatal）。
func connectInBackground(a *Agent) <-chan connResult {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	a.mgr.Start()
	done := make(chan connResult, 1)
	go func() {
		defer cancel()
		defer a.mgr.Shutdown(time.Second)
		done <- a.connectOnce(ctx)
	}()
	return done
}

func waitConnected(t *testing.T, done <-chan connResult) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("connectOnce 超时")
	}
}

// pendingChecks 返回检查器里还在等回答的检查数量。
func (c *autoChecker) pendingChecks() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.waiting)
}

// eventually 轮询 cond 直到成立；超时算失败。
func eventually(t *testing.T, timeout time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("等待超时：%s", what)
}

func TestAutoCheckJitterStaysInRange(t *testing.T) {
	for range 200 {
		if d := jitterBetween(10*time.Second, 40*time.Second); d < 10*time.Second || d > 40*time.Second {
			t.Fatalf("jitterBetween 越界: %v", d)
		}
		if d := jitterAround(6*time.Hour, 0.1); d < 6*time.Hour-36*time.Minute || d > 6*time.Hour+36*time.Minute {
			t.Fatalf("jitterAround 越界: %v", d)
		}
	}
	if d := jitterBetween(0, 0); d <= 0 {
		t.Fatalf("时长至少为正: %v", d)
	}
	if d := jitterBetween(time.Second, time.Millisecond); d != time.Second {
		t.Fatalf("上界小于下界时取下界: %v", d)
	}
}

// ---------- 与 Hub 连接上的端到端 ----------

func fastAutoCheckTiming(t *testing.T) {
	t.Helper()
	old := autoCheckDefaults
	autoCheckDefaults = autoCheckTiming{First: 20 * time.Millisecond, Every: time.Hour, Timeout: 5 * time.Second}
	t.Cleanup(func() { autoCheckDefaults = old })
}

func TestAgentAsksTheHubAboutNewVersionsAndRecordsTheAnswer(t *testing.T) {
	fastAutoCheckTiming(t)
	var notes atomic.Value
	noted := make(chan struct{}, 4)
	h := newUpgradeHarness(t, []byte("payload"), func(c *SelfUpgrade) {
		c.AutoMode = func() string { return termproto.AutoUpgradeNotify }
		c.AutoChecked = func(n AutoCheckNote) { notes.Store(n); noted <- struct{}{} }
	})
	hellos := make(chan termproto.Hello, 1)
	var checks atomic.Int32
	release := make(chan struct{}) // 测试确认 Agent 处理完回答之后才让 Hub 收线
	h.helloCapturingHub(t, func(hello termproto.Hello) { hellos <- hello }, func(ctx context.Context, ws *websocket.Conn) {
		for {
			_, data, err := ws.Read(ctx)
			if err != nil {
				return
			}
			var chk termproto.AutoUpgradeCheck
			if json.Unmarshal(data, &chk) != nil || chk.T != termproto.TAutoUpgradeCheck {
				continue
			}
			checks.Add(1)
			sendJSON(t, ctx, ws, termproto.AutoUpgradeOffer{T: termproto.TAutoUpgradeOffer, ID: chk.ID, Status: termproto.AutoOfferAvailable, Version: "9.9.9"})
			select {
			case <-release:
			case <-ctx.Done():
			}
			_ = ws.Close(websocket.StatusNormalClosure, "done")
			return
		}
	})
	a := newAgentFor(t, h.target, func(o *Options) {
		o.Identity = persistentIdentity(t)
		o.Version = "1.0.0"
		o.SelfUpgrade = h.cfg
	})
	done := connectInBackground(a)

	select {
	case <-noted:
	case <-time.After(5 * time.Second):
		t.Fatal("Agent 应在被启用后问 Hub 一次，并记下回答")
	}
	close(release)
	n, _ := notes.Load().(AutoCheckNote)
	if n.Status != termproto.AutoOfferAvailable || n.Latest != "9.9.9" || n.Current != "1.0.0" {
		t.Fatalf("应记下「有新版本 9.9.9」: %+v", n)
	}
	if hello := <-hellos; hello.Meta.AutoUpgrade != termproto.AutoUpgradeNotify {
		t.Fatalf("握手应声明提示模式: %q", hello.Meta.AutoUpgrade)
	}
	if checks.Load() != 1 {
		t.Fatalf("应只问了一次: %d", checks.Load())
	}
	h.mu.Lock()
	handoffs := len(h.handoffs)
	h.mu.Unlock()
	if handoffs != 0 || len(h.stageFiles()) != 0 {
		t.Fatalf("提示模式下绝不下载、不交接: handoffs=%d staged=%v", handoffs, h.stageFiles())
	}
	if h.hits.Load() != 0 {
		t.Fatalf("提示模式下不应访问升级文件: %d", h.hits.Load())
	}
	waitConnected(t, done)
}

func TestAgentDoesNotAskBeforeBeingApproved(t *testing.T) {
	fastAutoCheckTiming(t)
	h := newUpgradeHarness(t, []byte("payload"), func(c *SelfUpgrade) {
		c.AutoMode = func() string { return termproto.AutoUpgradeNotify }
	})
	var (
		mu         sync.Mutex
		approvedAt time.Time
		checkTimes []time.Time
	)
	gotCheck := make(chan struct{}, 1)
	h.setScript(func(ctx context.Context, ws *websocket.Conn) {
		ch, _ := challenge(t, termproto.ProtocolVersion)
		sendJSON(t, ctx, ws, ch)
		_, data, err := ws.Read(ctx)
		if err != nil {
			return
		}
		var hello termproto.Hello
		if json.Unmarshal(data, &hello) != nil {
			return
		}
		pub, _ := termproto.DecodeKey(hello.Pub, 32)
		sendJSON(t, ctx, ws, termproto.Welcome{T: termproto.TWelcome, Status: termproto.WelcomePending, MachineCode: termproto.MachineCode(pub)})
		go func() {
			// 待启用期间 Agent 只应静默等待：这段时间里它不能发出检查（First 只有 20ms，不等批准的循环早就发了）。
			time.Sleep(300 * time.Millisecond)
			mu.Lock()
			approvedAt = time.Now()
			mu.Unlock()
			sendJSON(t, ctx, ws, termproto.Approved{T: termproto.TApproved, Policy: termproto.Policy{Terminal: true}})
		}()
		for {
			_, data, err := ws.Read(ctx)
			if err != nil {
				return
			}
			var chk termproto.AutoUpgradeCheck
			if json.Unmarshal(data, &chk) == nil && chk.T == termproto.TAutoUpgradeCheck {
				mu.Lock()
				checkTimes = append(checkTimes, time.Now())
				mu.Unlock()
				select {
				case gotCheck <- struct{}{}:
				default:
				}
				_ = ws.Close(websocket.StatusNormalClosure, "done")
				return
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
	case <-gotCheck:
	case <-time.After(5 * time.Second):
		t.Fatal("被批准之后 Agent 应开始检查")
	}
	waitConnected(t, done)
	mu.Lock()
	defer mu.Unlock()
	if len(checkTimes) != 1 || approvedAt.IsZero() || checkTimes[0].Before(approvedAt) {
		t.Fatalf("检查必须发生在被批准之后: approved=%v checks=%v", approvedAt, checkTimes)
	}
}

func TestAgentWithoutAutoUpgradeNeverAsks(t *testing.T) {
	fastAutoCheckTiming(t)
	h := newUpgradeHarness(t, []byte("payload"), nil) // 只有 Web 升级，没有 AutoMode
	var checks atomic.Int32
	h.helloCapturingHub(t, func(termproto.Hello) {}, func(ctx context.Context, ws *websocket.Conn) {
		deadline := time.After(400 * time.Millisecond)
		frames := make(chan []byte, 8)
		go func() {
			for {
				_, data, err := ws.Read(ctx)
				if err != nil {
					close(frames)
					return
				}
				frames <- data
			}
		}()
		for {
			select {
			case data, ok := <-frames:
				if !ok {
					return
				}
				var chk termproto.AutoUpgradeCheck
				if json.Unmarshal(data, &chk) == nil && chk.T == termproto.TAutoUpgradeCheck {
					checks.Add(1)
				}
			case <-deadline:
				_ = ws.Close(websocket.StatusNormalClosure, "done")
				return
			}
		}
	})
	a := newAgentFor(t, h.target, func(o *Options) {
		o.Identity = persistentIdentity(t)
		o.Version = "1.0.0"
		o.SelfUpgrade = h.cfg
	})
	connectWith(t, a)
	if checks.Load() != 0 {
		t.Fatalf("没有提供自动升级的 Agent 不应发出检查: %d", checks.Load())
	}
}
