package ntfy

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/teacat99/RelayMesh/v2/internal/model"
)

// harness 是一个对着本地假 ntfy 服务的 Notifier；配置可以在测试中途改。
type harness struct {
	t   *testing.T
	n   *Notifier
	rec *recorder
	srv *httptest.Server

	mu  sync.Mutex
	cfg Config

	// respond 非 nil 时接管假服务的响应（用来模拟失败）；为 nil 时记录请求并回 200。
	respond atomic.Pointer[http.HandlerFunc]
}

func newHarness(t *testing.T, mutate func(*Config), opts ...func(*Options)) *harness {
	t.Helper()
	h := &harness{t: t, rec: &recorder{}}
	h.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if f := h.respond.Load(); f != nil {
			(*f)(w, r)
			return
		}
		h.rec.add(r)
	}))
	t.Cleanup(h.srv.Close)

	h.cfg = Config{Enabled: true, Server: h.srv.URL, Topic: "topic-1", Priority: 4}.Normalize()
	if mutate != nil {
		mutate(&h.cfg)
		h.cfg = h.cfg.Normalize()
	}
	o := Options{
		Load: func(context.Context) (Config, error) {
			h.mu.Lock()
			defer h.mu.Unlock()
			return h.cfg, nil
		},
		Sender: fastSender(0),
	}
	for _, f := range opts {
		f(&o)
	}
	h.n = New(o)
	h.n.delayUnit = 20 * time.Millisecond // 延迟 1 秒 = 20 毫秒
	t.Cleanup(h.n.Stop)
	return h
}

func (h *harness) setConfig(f func(*Config)) {
	h.mu.Lock()
	defer h.mu.Unlock()
	f(&h.cfg)
}

func sess(id, title string) *model.FeedbackSession {
	return &model.FeedbackSession{ID: id, WorkflowID: "wf", Title: title, Summary: "摘要", ProjectDirectory: "/p/Demo", HostName: "host-1"}
}

// waitFor 轮询直到条件成立；超时则让测试失败。
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("等待超时：%s", what)
}

// settle 等一小会儿，用来断言「什么都没有发生」。
func settle() { time.Sleep(150 * time.Millisecond) }

func TestNotifier_SendsNewRequest(t *testing.T) {
	h := newHarness(t, func(c *Config) { c.Token = "tk_abc"; c.ClickURL = "https://rm.example.com/" })
	h.n.NotifyRequest(sess("s1", "第十批方案"))

	waitFor(t, "收到推送", func() bool { return h.rec.count() == 1 })
	got := h.rec.last()
	if got.Body.Topic != "topic-1" || got.Body.Title != "智能体在等你回复" || !strings.Contains(got.Body.Message, "第十批方案") {
		t.Errorf("推送内容不对：%+v", got.Body)
	}
	if got.Body.Click != "https://rm.example.com/" || got.Header.Get("Authorization") != "Bearer tk_abc" {
		t.Errorf("点击地址 / 认证不对：%+v %v", got.Body, got.Header)
	}
	waitFor(t, "状态更新", func() bool { return h.n.Status().Sent == 1 })
	st := h.n.Status()
	if !st.LastOK || st.LastTest || st.LastAttemptAt == nil || st.LastHTTPStatus != 200 || st.Failed != 0 {
		t.Errorf("状态不对：%+v", st)
	}
}

func TestNotifier_DisabledOrIncompleteSendsNothing(t *testing.T) {
	h := newHarness(t, func(c *Config) { c.Enabled = false })
	h.n.NotifyRequest(sess("s1", "t"))
	settle()
	if h.rec.count() != 0 {
		t.Fatal("总开关关闭时不应推送")
	}

	h.setConfig(func(c *Config) { c.Enabled = true; c.Topic = "" })
	h.n.NotifyRequest(sess("s2", "t"))
	settle()
	if h.rec.count() != 0 {
		t.Fatal("没有主题时不应推送")
	}

	h.setConfig(func(c *Config) { c.Topic = "topic bad!" })
	h.n.NotifyRequest(sess("s3", "t"))
	settle()
	if h.rec.count() != 0 {
		t.Fatal("配置不合法时不应推送")
	}

	// 配置修好后立即生效（每条事件现读配置）。
	h.setConfig(func(c *Config) { c.Topic = "ok-topic" })
	h.n.NotifyRequest(sess("s4", "t"))
	waitFor(t, "修好配置后推送", func() bool { return h.rec.count() == 1 })
}

func TestNotifier_DedupesBySession(t *testing.T) {
	h := newHarness(t, nil)
	s := sess("same", "t")
	h.n.NotifyRequest(s)
	h.n.NotifyRequest(s)
	h.n.NotifyRequest(sess("same", "t"))
	h.n.NotifyRequest(sess("other", "t"))
	waitFor(t, "两条不同会话", func() bool { return h.rec.count() >= 2 })
	settle()
	if got := h.rec.count(); got != 2 {
		t.Fatalf("同一会话只应推一次，共应 2 条，实际 %d", got)
	}
}

func TestNotifier_IgnoresNilAndEmpty(t *testing.T) {
	h := newHarness(t, nil)
	h.n.NotifyRequest(nil)
	h.n.NotifyRequest(&model.FeedbackSession{})
	var nilN *Notifier
	nilN.NotifyRequest(sess("x", "t")) // 没有接线（nil 接收者）也不能 panic
	nilN.Stop()
	settle()
	if h.rec.count() != 0 {
		t.Fatal("空事件不应推送")
	}
}

func TestNotifier_DelayedPushSkippedWhenAnswered(t *testing.T) {
	var pending atomic.Bool
	pending.Store(true)
	h := newHarness(t, func(c *Config) { c.DelaySeconds = 5 }, func(o *Options) {
		o.StillPending = func(_ context.Context, id string) bool { return pending.Load() }
	})
	h.n.delayUnit = 80 * time.Millisecond // 延迟 5 秒 = 400 毫秒

	h.n.NotifyRequest(sess("s1", "t"))
	time.Sleep(150 * time.Millisecond)
	if h.rec.count() != 0 {
		t.Fatal("延迟期间不应推送")
	}
	pending.Store(false) // 你在网页上处理掉了
	time.Sleep(500 * time.Millisecond)
	if h.rec.count() != 0 {
		t.Fatal("延迟期间已处理的请求不应再推送")
	}

	// 对照：仍在等待的请求，延迟到期后推送。
	pending.Store(true)
	h.n.NotifyRequest(sess("s2", "t"))
	waitFor(t, "延迟到期后推送", func() bool { return h.rec.count() == 1 })
}

func TestNotifier_DelayedPushRechecksConfig(t *testing.T) {
	h := newHarness(t, func(c *Config) { c.DelaySeconds = 5 })
	h.n.delayUnit = 80 * time.Millisecond

	h.n.NotifyRequest(sess("s1", "t"))
	time.Sleep(100 * time.Millisecond)
	h.setConfig(func(c *Config) { c.Enabled = false }) // 等待期间关掉了推送
	time.Sleep(500 * time.Millisecond)
	if h.rec.count() != 0 {
		t.Fatal("等待期间关闭了推送，到期后不应再发")
	}
}

func TestNotifier_RateLimitMergesIntoNextMessage(t *testing.T) {
	h := newHarness(t, nil)
	var clock atomic.Int64
	clock.Store(time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC).UnixNano())
	h.n.now = func() time.Time { return time.Unix(0, clock.Load()) }

	total := rateMaxPerWindow + 3
	for i := 0; i < total; i++ {
		h.n.NotifyRequest(sess(fmt.Sprintf("s%02d", i), "t"))
	}
	waitFor(t, "额度内的推送发出", func() bool { return h.rec.count() >= rateMaxPerWindow })
	settle()
	if got := h.rec.count(); got != rateMaxPerWindow {
		t.Fatalf("一分钟内最多推送 %d 条，实际 %d", rateMaxPerWindow, got)
	}
	if st := h.n.Status(); st.Dropped != 3 {
		t.Errorf("被合并的 3 条应计入 Dropped：%+v", st)
	}

	// 窗口过去之后的下一条要交代「另有 3 条被合并」。
	clock.Add(int64(rateWindow + time.Second))
	h.n.NotifyRequest(sess("later", "t"))
	waitFor(t, "窗口过后再推送", func() bool { return h.rec.count() == rateMaxPerWindow+1 })
	if msg := h.rec.last().Body.Message; !strings.Contains(msg, "另有 3 条") {
		t.Errorf("应说明被合并的条数：%q", msg)
	}
	// 说明过一次就清零。
	h.n.NotifyRequest(sess("later2", "t"))
	waitFor(t, "再下一条", func() bool { return h.rec.count() == rateMaxPerWindow+2 })
	if msg := h.rec.last().Body.Message; strings.Contains(msg, "另有") {
		t.Errorf("合并说明只应出现一次：%q", msg)
	}
}

func TestNotifier_FailureIsRecordedAndKeepsMergedCount(t *testing.T) {
	h := newHarness(t, nil)
	deny := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":"forbidden"}`))
	})
	h.respond.Store(&deny)

	h.n.mu.Lock()
	h.n.suppressed = 2 // 此前有 2 条被合并、还没被说明过
	h.n.mu.Unlock()
	h.n.NotifyRequest(sess("s1", "t"))
	waitFor(t, "记录失败", func() bool { return h.n.Status().Failed == 1 })
	st := h.n.Status()
	if st.LastOK || st.Sent != 0 || !strings.Contains(st.LastError, "认证失败") || st.LastHTTPStatus != 403 {
		t.Errorf("失败状态不对：%+v", st)
	}

	// 这一条没发出去，被合并的 2 条仍然没人说明，应留给下一条。
	h.respond.Store(nil)
	h.n.NotifyRequest(sess("s2", "t"))
	waitFor(t, "恢复后推送", func() bool { return h.rec.count() == 1 })
	if msg := h.rec.last().Body.Message; !strings.Contains(msg, "另有 2 条") {
		t.Errorf("上次没发出去的合并说明应留到下一条：%q", msg)
	}
	waitFor(t, "状态恢复", func() bool { return h.n.Status().LastOK })
}

// 推送服务再慢，NotifyRequest 也必须立刻返回：它跑在智能体的 MCP 请求里。
func TestNotifier_NeverBlocksCaller(t *testing.T) {
	release := make(chan struct{})
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-release }))
	defer slow.Close()
	defer close(release)

	h := newHarness(t, func(c *Config) { c.Server = slow.URL })
	start := time.Now()
	for i := 0; i < queueSize*3; i++ {
		h.n.NotifyRequest(sess(fmt.Sprintf("s%d", i), "t"))
	}
	if d := time.Since(start); d > 500*time.Millisecond {
		t.Fatalf("NotifyRequest 不应被慢服务拖住，%d 次调用用了 %s", queueSize*3, d)
	}
	waitFor(t, "队列满后丢弃计数", func() bool { return h.n.Status().Dropped > 0 })
}

func TestNotifier_StopIsSafeAndFinal(t *testing.T) {
	h := newHarness(t, nil)
	h.n.NotifyRequest(sess("s1", "t"))
	waitFor(t, "先推送一条", func() bool { return h.rec.count() == 1 })

	h.n.Stop()
	h.n.Stop() // 可重复调用
	h.n.NotifyRequest(sess("s2", "t"))
	settle()
	if h.rec.count() != 1 {
		t.Fatal("停止后不应再推送")
	}

	// 从没用过就停止：不应卡住，也不应泄漏协程。
	idle := New(Options{Load: func(context.Context) (Config, error) { return Config{}, nil }})
	done := make(chan struct{})
	go func() { idle.Stop(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("没启动过的 Notifier 停止时卡住了")
	}
}

func TestNotifier_EnvTokenOverrides(t *testing.T) {
	h := newHarness(t, func(c *Config) { c.Token = "tk_db" }, func(o *Options) { o.EnvToken = "tk_env" })
	h.n.NotifyRequest(sess("s1", "t"))
	waitFor(t, "推送", func() bool { return h.rec.count() == 1 })
	if got := h.rec.last().Header.Get("Authorization"); got != "Bearer tk_env" {
		t.Errorf("环境变量里的令牌应盖过数据库里的：%q", got)
	}
}

func TestNotifier_SendTestIgnoresEnabledAndIsRecorded(t *testing.T) {
	h := newHarness(t, func(c *Config) { c.Enabled = false })
	res, err := h.n.SendTest(context.Background(), h.cfg)
	if err != nil || res.Status != 200 {
		t.Fatalf("测试推送应成功（无视总开关）：%v %+v", err, res)
	}
	if got := h.rec.last().Body.Title; got != "RelayMesh 测试推送" {
		t.Errorf("测试推送标题不对：%q", got)
	}
	st := h.n.Status()
	if !st.LastOK || !st.LastTest || st.Sent != 0 {
		t.Errorf("测试推送应记为「最近一次」，但不计入已推送条数：%+v", st)
	}
}

func TestNotifier_SendTestDoesNotRetry(t *testing.T) {
	h := newHarness(t, nil, func(o *Options) { o.Sender = fastSender(3) })
	var hits atomic.Int32
	failing := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusBadGateway)
	})
	h.respond.Store(&failing)

	res, err := h.n.SendTest(context.Background(), h.cfg)
	if err == nil || res.Attempts != 1 || hits.Load() != 1 {
		t.Fatalf("测试推送应只试一次就如实报告失败：err=%v res=%+v hits=%d", err, res, hits.Load())
	}
	if st := h.n.Status(); st.LastOK || !st.LastTest || st.Failed != 0 {
		t.Errorf("测试推送的失败应记为最近一次，但不计入失败条数：%+v", st)
	}
}

func TestNotifier_SurvivesPanicInLoader(t *testing.T) {
	var calls atomic.Int32
	h := newHarness(t, nil)
	good := h.n.opts.Load
	h.n.opts.Load = func(ctx context.Context) (Config, error) {
		if calls.Add(1) == 1 {
			panic("boom")
		}
		return good(ctx)
	}
	h.n.NotifyRequest(sess("s1", "t")) // 第一次读配置时 panic
	h.n.NotifyRequest(sess("s2", "t"))
	waitFor(t, "工作协程挺过 panic 后继续推送", func() bool { return h.rec.count() >= 1 })
}

func TestNotifier_LoadErrorDropsQuietly(t *testing.T) {
	h := newHarness(t, nil)
	h.n.opts.Load = func(context.Context) (Config, error) { return Config{}, errors.New("db down") }
	h.n.NotifyRequest(sess("s1", "t"))
	settle()
	if h.rec.count() != 0 {
		t.Fatal("读不到配置时不应推送")
	}
}
