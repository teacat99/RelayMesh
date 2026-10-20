package ntfy

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/teacat99/RelayMesh/v2/internal/model"
)

// runCfg 打开运行结束推送、门槛 60 秒。
func runCfg(c *Config) { c.RunNotify, c.RunMinSeconds = true, 60 }

// newSlowServer 是一个迟迟不回应的假 ntfy：直到 release 被关闭才放行。
func newSlowServer(release <-chan struct{}) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { <-release }))
}

func TestNotifier_RunEndSendsPush(t *testing.T) {
	h := newHarness(t, func(c *Config) { runCfg(c); c.Priority = 5; c.Token = "tk_abc"; c.ClickURL = "https://rm.example.com/" })
	h.n.NotifyRun(okRun())

	waitFor(t, "收到运行结束推送", func() bool { return h.rec.count() == 1 })
	got := h.rec.last()
	if got.Body.Topic != "topic-1" || got.Body.Title != "模型命令已完成" {
		t.Errorf("推送内容不对：%+v", got.Body)
	}
	if !strings.Contains(got.Body.Message, "跑全量回归测试") || !strings.Contains(got.Body.Message, "用时 5 分钟") {
		t.Errorf("正文应有说明与用时：%q", got.Body.Message)
	}
	if got.Body.Priority != 3 || got.Body.Click != "https://rm.example.com/" || got.Header.Get("Authorization") != "Bearer tk_abc" {
		t.Errorf("优先级 / 点击地址 / 认证不对：%+v %v", got.Body, got.Header)
	}
	waitFor(t, "状态更新", func() bool { return h.n.Status().Sent == 1 })
	if st := h.n.Status(); !st.LastOK || st.LastTest || st.Failed != 0 {
		t.Errorf("状态不对：%+v", st)
	}
}

// 默认关闭：升级之后，没有明确打开的人不会突然收到新类型的推送。
func TestNotifier_RunEndOffByDefaultAndNeedsMasterSwitch(t *testing.T) {
	h := newHarness(t, nil) // 总开关开着、运行结束推送没开
	h.n.NotifyRun(okRun())
	settle()
	if h.rec.count() != 0 {
		t.Fatal("运行结束推送默认关闭，不应推送")
	}

	h.setConfig(func(c *Config) { runCfg(c); c.Enabled = false })
	h.n.NotifyRun(with(okRun(), func(r *RunInfo) { r.RunID = "r2" }))
	settle()
	if h.rec.count() != 0 {
		t.Fatal("总开关关闭时，运行结束也不应推送")
	}

	h.setConfig(func(c *Config) { c.Enabled = true; c.Topic = "" })
	h.n.NotifyRun(with(okRun(), func(r *RunInfo) { r.RunID = "r3" }))
	settle()
	if h.rec.count() != 0 {
		t.Fatal("没有主题时不应推送")
	}

	// 对照：改好配置后，同一类事件立即生效（每条事件现读配置）。
	h.setConfig(func(c *Config) { c.Topic = "ok-topic" })
	h.n.NotifyRun(with(okRun(), func(r *RunInfo) { r.RunID = "r4" }))
	waitFor(t, "打开后推送", func() bool { return h.rec.count() == 1 })
}

func with(r RunInfo, f func(*RunInfo)) RunInfo { f(&r); return r }

func TestNotifier_RunEndThresholdAndStates(t *testing.T) {
	h := newHarness(t, runCfg)
	cases := []struct {
		id   string
		in   RunInfo
		want bool
	}{
		{"short", with(okRun(), func(r *RunInfo) { r.Duration = 59 * time.Second }), false},
		{"sub-second", with(okRun(), func(r *RunInfo) { r.Duration = 300 * time.Millisecond }), false},
		{"rejected", with(okRun(), func(r *RunInfo) { r.State = model.RunRejected }), false},
		{"denied", with(okRun(), func(r *RunInfo) { r.State = model.RunDenied }), false},
		{"user-cancel", with(okRun(), func(r *RunInfo) { r.State, r.CancelBy = model.RunCancelled, "user" }), false},
		{"model-cancel", with(okRun(), func(r *RunInfo) { r.State, r.CancelBy = model.RunCancelled, "model" }), false},
		{"failed", with(okRun(), func(r *RunInfo) { r.State, r.ExitCode = model.RunFailed, intp(2) }), true},
		{"timeout", with(okRun(), func(r *RunInfo) { r.State = model.RunTimedOut }), true},
		{"lost", with(okRun(), func(r *RunInfo) { r.State = model.RunLost }), true},
		{"host-interrupt", with(okRun(), func(r *RunInfo) {
			r.State, r.Why = model.RunCancelled, "主机上的会话守护进程已退出，进程已丢失"
		}), true},
	}
	want := 0
	for _, tc := range cases {
		tc.in.RunID = tc.id
		h.n.NotifyRun(tc.in)
		if tc.want {
			want++
		}
	}
	waitFor(t, "该推的都推了", func() bool { return h.rec.count() >= want })
	settle()
	if got := h.rec.count(); got != want {
		t.Fatalf("应推送 %d 条（失败 / 超时 / 丢失 / 被主机侧中断），实际 %d 条", want, got)
	}
}

func TestNotifier_RunEndDedupesByRunID(t *testing.T) {
	h := newHarness(t, runCfg)
	h.n.NotifyRun(okRun())
	h.n.NotifyRun(okRun())
	h.n.NotifyRun(with(okRun(), func(r *RunInfo) { r.RunID = "r2" }))
	waitFor(t, "两条不同的运行", func() bool { return h.rec.count() >= 2 })
	settle()
	if got := h.rec.count(); got != 2 {
		t.Fatalf("同一次运行只应推一次，共应 2 条，实际 %d", got)
	}
}

// 运行编号与会话编号各管各的：不会因为恰好同名就把对方当成重复。
func TestNotifier_RunEndAndRequestDoNotCollideOnID(t *testing.T) {
	h := newHarness(t, runCfg)
	h.n.NotifyRequest(sess("same", "t"))
	h.n.NotifyRun(with(okRun(), func(r *RunInfo) { r.RunID = "same" }))
	waitFor(t, "一条请求 + 一条运行结束", func() bool { return h.rec.count() >= 2 })
}

// 运行结束从不延迟：延迟是给「请求」用的（你在网页上处理掉就不再推送），运行结束没有「处理掉」这回事。
func TestNotifier_RunEndIsNeverDelayed(t *testing.T) {
	h := newHarness(t, func(c *Config) { runCfg(c); c.DelaySeconds = 5 })
	h.n.delayUnit = time.Hour // 如果错误地套用了延迟，这条推送要一个小时后才会发
	h.n.NotifyRun(okRun())
	waitFor(t, "立即推送", func() bool { return h.rec.count() == 1 })
}

func TestNotifier_RunEndSharesRateLimitWithRequests(t *testing.T) {
	h := newHarness(t, runCfg)
	var clock atomic.Int64
	clock.Store(time.Date(2026, 10, 10, 9, 0, 0, 0, time.UTC).UnixNano())
	h.n.now = func() time.Time { return time.Unix(0, clock.Load()) }

	half := rateMaxPerWindow / 2
	for i := 0; i < half; i++ {
		h.n.NotifyRequest(sess(fmt.Sprintf("s%02d", i), "t"))
	}
	for i := 0; i < half+3; i++ {
		h.n.NotifyRun(with(okRun(), func(r *RunInfo) { r.RunID = fmt.Sprintf("r%02d", i) }))
	}
	waitFor(t, "额度内的推送发出", func() bool { return h.rec.count() >= rateMaxPerWindow })
	settle()
	if got := h.rec.count(); got != rateMaxPerWindow {
		t.Fatalf("请求与运行结束共用一分钟 %d 条的额度，实际 %d 条", rateMaxPerWindow, got)
	}
	if st := h.n.Status(); st.Dropped != 3 {
		t.Errorf("被合并的 3 条应计入 Dropped：%+v", st)
	}

	clock.Add(int64(rateWindow + time.Second))
	h.n.NotifyRun(with(okRun(), func(r *RunInfo) { r.RunID = "later" }))
	waitFor(t, "窗口过后再推送", func() bool { return h.rec.count() == rateMaxPerWindow+1 })
	if msg := h.rec.last().Body.Message; !strings.Contains(msg, "另有 3 条通知") {
		t.Errorf("运行结束的推送也应说明被合并的条数：%q", msg)
	}
}

func TestNotifier_RunEndResolvesHostNameOnlyWhenSending(t *testing.T) {
	var lookups atomic.Int32
	h := newHarness(t, runCfg, func(o *Options) {
		o.HostName = func(_ context.Context, machine string) string {
			lookups.Add(1)
			if machine == "RM-AAAA-BBBB" {
				return "build-box"
			}
			return ""
		}
	})
	// 不推送的事件（太短、被拒绝、用户取消）不该去查库。
	h.n.NotifyRun(with(okRun(), func(r *RunInfo) { r.RunID = "a"; r.Duration = 5 * time.Second }))
	h.n.NotifyRun(with(okRun(), func(r *RunInfo) { r.RunID = "b"; r.State = model.RunDenied }))
	h.n.NotifyRun(with(okRun(), func(r *RunInfo) { r.RunID = "c"; r.State, r.CancelBy = model.RunCancelled, "user" }))
	settle()
	if h.rec.count() != 0 || lookups.Load() != 0 {
		t.Fatalf("不推送的事件不应查主机名：推送 %d 条、查询 %d 次", h.rec.count(), lookups.Load())
	}

	h.n.NotifyRun(okRun())
	waitFor(t, "推送", func() bool { return h.rec.count() == 1 })
	if msg := h.rec.last().Body.Message; !strings.Contains(msg, "主机：build-box") || strings.Contains(msg, "RM-AAAA-BBBB") {
		t.Errorf("应写主机名、不写机器码：%q", msg)
	}
	if lookups.Load() != 1 {
		t.Errorf("只应查一次：%d", lookups.Load())
	}
}

// 查主机名出了任何岔子（报错、panic、没有接线），推送照发，只是少一行主机。
func TestNotifier_RunEndSurvivesHostNameTrouble(t *testing.T) {
	h := newHarness(t, runCfg, func(o *Options) {
		o.HostName = func(context.Context, string) string { panic("db exploded") }
	})
	h.n.NotifyRun(okRun())
	waitFor(t, "仍然推送", func() bool { return h.rec.count() == 1 })
	if msg := h.rec.last().Body.Message; strings.Contains(msg, "主机：") {
		t.Errorf("查不到主机名时不应写主机行：%q", msg)
	}

	h2 := newHarness(t, runCfg) // 没有接线 HostName
	h2.n.NotifyRun(okRun())
	waitFor(t, "没接线也推送", func() bool { return h2.rec.count() == 1 })
}

func TestNotifier_RunEndIgnoresNilAndEmpty(t *testing.T) {
	h := newHarness(t, runCfg)
	h.n.NotifyRun(RunInfo{})
	h.n.NotifyRun(with(okRun(), func(r *RunInfo) { r.RunID = "" }))
	var nilN *Notifier
	nilN.NotifyRun(okRun()) // 没有接线（nil 接收者）也不能 panic
	settle()
	if h.rec.count() != 0 {
		t.Fatal("空事件不应推送")
	}
}

// 事件在登记的那一刻就复制：调用方之后再改它（退出码是个指针）不影响推送内容。
func TestNotifier_RunEndTakesASnapshot(t *testing.T) {
	h := newHarness(t, runCfg)
	code := 3
	in := with(okRun(), func(r *RunInfo) { r.State, r.ExitCode = model.RunFailed, &code })
	h.n.NotifyRun(in)
	code = 0 // 登记之后被改了
	waitFor(t, "推送", func() bool { return h.rec.count() == 1 })
	if got := h.rec.last().Body.Title; got != "模型命令失败（退出码 3）" {
		t.Errorf("推送内容应是登记时的快照：%q", got)
	}
}

func TestNotifier_RunEndFailureIsRecordedAndKeepsMergedCount(t *testing.T) {
	h := newHarness(t, runCfg)
	deny := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":"forbidden"}`))
	})
	h.respond.Store(&deny)
	h.n.mu.Lock()
	h.n.suppressed = 2
	h.n.mu.Unlock()

	h.n.NotifyRun(okRun())
	waitFor(t, "记录失败", func() bool { return h.n.Status().Failed == 1 })
	if st := h.n.Status(); st.LastOK || st.Sent != 0 || st.LastHTTPStatus != 403 {
		t.Errorf("失败状态不对：%+v", st)
	}

	// 没发出去：被合并的 2 条仍没人说明过，留给下一条。
	h.respond.Store(nil)
	h.n.NotifyRun(with(okRun(), func(r *RunInfo) { r.RunID = "r2" }))
	waitFor(t, "恢复后推送", func() bool { return h.rec.count() == 1 })
	if msg := h.rec.last().Body.Message; !strings.Contains(msg, "另有 2 条") {
		t.Errorf("上次没发出去的合并说明应留到下一条：%q", msg)
	}
}

// 推送服务再慢，NotifyRun 也必须立刻返回：它跑在运行终结的路径上。
func TestNotifier_RunEndNeverBlocksCaller(t *testing.T) {
	release := make(chan struct{})
	slow := newSlowServer(release)
	defer slow.Close()
	defer close(release)

	h := newHarness(t, func(c *Config) { runCfg(c); c.Server = slow.URL })
	start := time.Now()
	for i := 0; i < queueSize*3; i++ {
		h.n.NotifyRun(with(okRun(), func(r *RunInfo) { r.RunID = fmt.Sprintf("r%d", i) }))
	}
	if d := time.Since(start); d > 500*time.Millisecond {
		t.Fatalf("NotifyRun 不应被慢服务拖住，%d 次调用用了 %s", queueSize*3, d)
	}
	waitFor(t, "队列满后丢弃计数", func() bool { return h.n.Status().Dropped > 0 })
}
