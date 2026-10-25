package ntfy

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/teacat99/RelayMesh/v2/internal/model"
)

func withOverrides(over map[string]int) func(*Config) {
	return func(c *Config) { c.PriorityByKind = over }
}

func agentErrorInfo(key string) ExceptionInfo {
	return ExceptionInfo{
		Kind: KindAgentError, Sub: SubAgentError, Key: key, Name: "Demo", Host: "host-1",
		Reason: "上一轮对话因出错中断", Since: time.Now().Add(-3 * time.Minute), DeepLink: "/?wf=demo",
	}
}

func TestException_DefaultOffSendsNothing(t *testing.T) {
	h := newHarness(t, nil)
	for _, kind := range []string{KindAgentError, KindAgentStalled, KindHostOffline, KindSecurityAlert} {
		in := agentErrorInfo("k-" + kind)
		in.Kind = kind
		h.n.NotifyException(in)
	}
	settle()
	if got := h.rec.count(); got != 0 {
		t.Fatalf("异常类默认不推送，却发了 %d 条", got)
	}
}

func TestException_SendsWhenEnabled(t *testing.T) {
	h := newHarness(t, func(c *Config) {
		c.ClickURL = "https://rm.example.com/"
		c.PriorityByKind = map[string]int{KindAgentError: 4}
	})
	h.n.NotifyException(agentErrorInfo("agent_error:s1"))
	waitFor(t, "收到异常推送", func() bool { return h.rec.count() == 1 })

	b := h.rec.last().Body
	if b.Title != "「Demo」的 Agent 出错结束" || b.Priority != 4 {
		t.Errorf("标题 / 优先级不对：%+v", b)
	}
	for _, want := range []string{"上一轮对话因出错中断", "主机：host-1", "前发生", "到控制台的 Agent 状态里看看"} {
		if !strings.Contains(b.Message, want) {
			t.Errorf("正文缺少 %q：%q", want, b.Message)
		}
	}
	if b.Click != "https://rm.example.com/?wf=demo" {
		t.Errorf("点击地址应是对外地址 + 深链：%q", b.Click)
	}
	// 没打开的另一类仍然不推。
	other := agentErrorInfo("host_offline:m1")
	other.Kind = KindHostOffline
	h.n.NotifyException(other)
	settle()
	if h.rec.count() != 1 {
		t.Errorf("没打开的类型不该推送：%d", h.rec.count())
	}
}

func TestException_PriorityFollowsTable(t *testing.T) {
	for _, p := range []int{1, 3, 5} {
		h := newHarness(t, withOverrides(map[string]int{KindSecurityAlert: p}))
		h.n.NotifyException(ExceptionInfo{Kind: KindSecurityAlert, Sub: SubOAuthCodeReplay, Key: "sec:code"})
		waitFor(t, "收到安全告警", func() bool { return h.rec.count() == 1 })
		if got := h.rec.last().Body.Priority; got != p {
			t.Errorf("表里设 %d 级，实际 %d", p, got)
		}
	}
}

func TestException_DedupWithinWindowAndRepeatNote(t *testing.T) {
	h := newHarness(t, withOverrides(map[string]int{KindSecurityAlert: 5}))
	var clock atomic.Int64
	clock.Store(time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC).UnixNano())
	h.n.now = func() time.Time { return time.Unix(0, clock.Load()) }

	in := ExceptionInfo{Kind: KindSecurityAlert, Sub: SubUnlockLocked, Key: "sec:unlock"}
	h.n.NotifyException(in)
	waitFor(t, "第一条", func() bool { return h.rec.count() == 1 })

	// 窗口内同一件事又发生了两次：不再推送，只计数。
	h.n.NotifyException(in)
	h.n.NotifyException(in)
	settle()
	if h.rec.count() != 1 {
		t.Fatalf("10 分钟内同一个 Key 只推一次：%d", h.rec.count())
	}
	if strings.Contains(h.rec.last().Body.Message, "又发生了") {
		t.Errorf("第一条不该带合并说明：%q", h.rec.last().Body.Message)
	}

	// 窗口过后再发生一次：推送，并交代期间合并了 2 次；说明过一次就清零。
	clock.Add(int64(seenTTL + time.Second))
	h.n.NotifyException(in)
	waitFor(t, "窗口过后再推", func() bool { return h.rec.count() == 2 })
	if msg := h.rec.last().Body.Message; !strings.Contains(msg, "又发生了 2 次") {
		t.Errorf("应说明合并的次数：%q", msg)
	}
	clock.Add(int64(seenTTL + time.Second))
	h.n.NotifyException(in)
	waitFor(t, "再下一条", func() bool { return h.rec.count() == 3 })
	if msg := h.rec.last().Body.Message; strings.Contains(msg, "又发生了") {
		t.Errorf("合并说明只应出现一次：%q", msg)
	}
}

func TestException_DifferentKeysAreIndependent(t *testing.T) {
	h := newHarness(t, withOverrides(map[string]int{KindSecurityAlert: 5}))
	h.n.NotifyException(ExceptionInfo{Kind: KindSecurityAlert, Sub: SubOAuthCodeReplay, Key: "sec:code"})
	h.n.NotifyException(ExceptionInfo{Kind: KindSecurityAlert, Sub: SubUnlockLocked, Key: "sec:unlock"})
	waitFor(t, "两种安全事件各推一条", func() bool { return h.rec.count() == 2 })
}

func TestException_IgnoresMalformed(t *testing.T) {
	h := newHarness(t, withOverrides(map[string]int{KindAgentError: 4, KindMention: 4}))
	h.n.NotifyException(ExceptionInfo{Kind: KindAgentError})                        // 没有 Key
	h.n.NotifyException(ExceptionInfo{Kind: KindMention, Key: "x"})                 // 不是异常类
	h.n.NotifyException(ExceptionInfo{Kind: "no_such_kind", Key: "y"})              // 没登记
	(*Notifier)(nil).NotifyException(ExceptionInfo{Kind: KindAgentError, Key: "z"}) // nil 安全
	settle()
	if h.rec.count() != 0 {
		t.Fatalf("不合格的异常不该推送：%d", h.rec.count())
	}
}

func TestException_TurnedOffMeansNoMore(t *testing.T) {
	h := newHarness(t, withOverrides(map[string]int{KindAgentError: 4}))
	h.n.NotifyException(agentErrorInfo("agent_error:a"))
	waitFor(t, "打开时推送", func() bool { return h.rec.count() == 1 })

	h.setConfig(func(c *Config) { c.PriorityByKind = map[string]int{KindAgentError: 0} })
	h.n.NotifyException(agentErrorInfo("agent_error:b"))
	settle()
	if h.rec.count() != 1 {
		t.Fatalf("设成不推送之后不该再推：%d", h.rec.count())
	}

	// 被挡掉的事件不占去重记录：重新打开之后，同一个 Key 仍能推出来。
	h.setConfig(func(c *Config) { c.PriorityByKind = map[string]int{KindAgentError: 4} })
	h.n.NotifyException(agentErrorInfo("agent_error:b"))
	waitFor(t, "重新打开后推送", func() bool { return h.rec.count() == 2 })
}

func TestExceptionRepeatsMapIsBounded(t *testing.T) {
	n := New(Options{Load: func(context.Context) (Config, error) { return Config{}, nil }})
	t.Cleanup(n.Stop)
	for i := 0; i < maxExceptionRepeats*2; i++ {
		n.countExceptionRepeat(fmt.Sprintf("k%d", i))
	}
	n.mu.Lock()
	size := len(n.excRepeats)
	n.mu.Unlock()
	if size > maxExceptionRepeats {
		t.Fatalf("重复计数表应有上限 %d：%d", maxExceptionRepeats, size)
	}
}

func TestBuildExceptionMessage(t *testing.T) {
	now := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	cfg := Config{Priority: 3, ClickURL: "https://rm.example.com", PriorityByKind: map[string]int{
		KindAgentError: 4, KindAgentStalled: 3, KindHostOffline: 4, KindSecurityAlert: 5,
	}}.Normalize()

	cases := []struct {
		name         string
		in           ExceptionInfo
		wantTitle    string
		wantPriority int
		wantInBody   []string
		notInBody    []string
	}{
		{"出错", ExceptionInfo{Kind: KindAgentError, Sub: SubAgentError, Name: "Demo", Host: "h1", Since: now.Add(-5 * time.Minute)},
			"「Demo」的 Agent 出错结束", 4, []string{"5 分钟前发生", "主机：h1"}, []string{"已持续"}},
		{"被中断", ExceptionInfo{Kind: KindAgentError, Sub: SubAgentAborted, Name: "Demo"},
			"「Demo」的 Agent 被中断", 4, nil, nil},
		{"没有名字时不拿占位词凑数", ExceptionInfo{Kind: KindAgentError, Sub: SubAgentError},
			"Agent 出错结束", 4, nil, []string{"「"}},
		{"疑似卡住", ExceptionInfo{Kind: KindAgentStalled, Name: "Demo", Reason: "静默超过 30 分钟", Since: now.Add(-2 * time.Hour)},
			"「Demo」的 Agent 疑似卡住", 3, []string{"静默超过 30 分钟", "已持续 2 小时"}, nil},
		{"主机掉线", ExceptionInfo{Kind: KindHostOffline, Name: "build-01", Since: now.Add(-7 * time.Minute)},
			"主机「build-01」已掉线", 4, []string{"已持续 7 分钟", "终端页的主机列表"}, nil},
		{"授权码重放", ExceptionInfo{Kind: KindSecurityAlert, Sub: SubOAuthCodeReplay, Reason: "同一个授权码被换了两次", Since: now.Add(-time.Minute)},
			"安全告警：OAuth 授权码被重放", 5, []string{"审计日志"}, []string{"已持续", "前发生"}},
		{"刷新令牌重放", ExceptionInfo{Kind: KindSecurityAlert, Sub: SubOAuthRefreshReplay},
			"安全告警：OAuth 刷新令牌被重放", 5, nil, nil},
		{"解锁被锁定", ExceptionInfo{Kind: KindSecurityAlert, Sub: SubUnlockLocked},
			"安全告警：终端解锁尝试过多", 5, nil, nil},
		{"登录被锁定", ExceptionInfo{Kind: KindSecurityAlert, Sub: SubLoginLocked},
			"安全告警：登录尝试过多", 5, nil, nil},
		{"没有细分的安全告警", ExceptionInfo{Kind: KindSecurityAlert}, "安全告警", 5, nil, nil},
	}
	for _, c := range cases {
		m := BuildExceptionMessage(cfg, c.in, 0, 0, now)
		if m.Title != c.wantTitle || m.Priority != c.wantPriority {
			t.Errorf("%s：标题 %q（%d 级），期望 %q（%d 级）", c.name, m.Title, m.Priority, c.wantTitle, c.wantPriority)
		}
		for _, w := range c.wantInBody {
			if !strings.Contains(m.Body, w) {
				t.Errorf("%s：正文缺少 %q：%q", c.name, w, m.Body)
			}
		}
		for _, w := range c.notInBody {
			if strings.Contains(m.Body+m.Title, w) {
				t.Errorf("%s：不该出现 %q：%q / %q", c.name, w, m.Title, m.Body)
			}
		}
		if len(m.Tags) != 1 || m.Tags[0] == "" {
			t.Errorf("%s：应带一个图标：%v", c.name, m.Tags)
		}
	}

	// 合并说明：去重挡掉的次数与限流合并的条数各说各的。
	m := BuildExceptionMessage(cfg, ExceptionInfo{Kind: KindSecurityAlert, Sub: SubLoginLocked}, 3, 2, now)
	if !strings.Contains(m.Body, "又发生了 3 次") || !strings.Contains(m.Body, "另有 2 条通知") {
		t.Errorf("合并说明不对：%q", m.Body)
	}

	// 原因被折成一行并截断；名字也被截断。
	long := BuildExceptionMessage(cfg, ExceptionInfo{
		Kind: KindAgentStalled, Name: strings.Repeat("名", 200), Reason: "第一行\n第二行" + strings.Repeat("长", 400),
	}, 0, 0, now)
	if strings.Contains(long.Body, "\n第二行") || strings.Count(long.Title, "名") > nameMaxLen {
		t.Errorf("原因要折成一行、名字要截断：%q / %q", long.Title, long.Body)
	}
}

// 每一类消息都有开关：设成不推送之后，对应的入口什么都不发，也不占去重与限流。
func TestKindGating_OffMeansSilent(t *testing.T) {
	zero := func(kinds ...string) func(*Config) {
		return func(c *Config) {
			c.RunNotify = true
			c.PriorityByKind = map[string]int{}
			for _, k := range kinds {
				c.PriorityByKind[k] = 0
			}
		}
	}
	run := func(state string) RunInfo {
		return RunInfo{RunID: "r-" + state, State: state, Duration: 2 * time.Minute, Machine: "m1"}
	}

	t.Run("等你回复", func(t *testing.T) {
		h := newHarness(t, zero(KindRequest))
		h.n.NotifyRequest(sess("s1", "t"))
		settle()
		if h.rec.count() != 0 {
			t.Fatalf("不该推送：%d", h.rec.count())
		}
		// 去重记录没被占：重新打开后同一个请求还能推出来。
		h.setConfig(func(c *Config) { c.PriorityByKind = nil })
		h.n.NotifyRequest(sess("s1", "t"))
		waitFor(t, "重新打开后推送", func() bool { return h.rec.count() == 1 })
	})

	t.Run("命令成功与出状况分开", func(t *testing.T) {
		h := newHarness(t, zero(KindRunOK))
		h.n.NotifyRun(run(model.RunSucceeded))
		settle()
		if h.rec.count() != 0 {
			t.Fatalf("成功不推：%d", h.rec.count())
		}
		h.n.NotifyRun(run(model.RunFailed))
		waitFor(t, "失败仍推", func() bool { return h.rec.count() == 1 })
		if p := h.rec.last().Body.Priority; p != 4 {
			t.Errorf("出状况跟随默认优先级 4：%d", p)
		}

		h2 := newHarness(t, zero(KindRunTrouble))
		h2.n.NotifyRun(run(model.RunFailed))
		h2.n.NotifyRun(run(model.RunTimedOut))
		settle()
		if h2.rec.count() != 0 {
			t.Fatalf("出状况不推：%d", h2.rec.count())
		}
		h2.n.NotifyRun(run(model.RunSucceeded))
		waitFor(t, "成功仍推", func() bool { return h2.rec.count() == 1 })
		if p := h2.rec.last().Body.Priority; p != 3 {
			t.Errorf("成功默认最多按 3：%d", p)
		}
	})

	t.Run("守护告警", func(t *testing.T) {
		h := newHarness(t, zero(KindAutoResume))
		h.n.NotifyAutoResume(AutoResumeInfo{Key: "g1", Name: "预设"})
		settle()
		if h.rec.count() != 0 {
			t.Fatalf("不该推送：%d", h.rec.count())
		}
	})

	t.Run("新消息类", func(t *testing.T) {
		h := newHarness(t, zero(KindAgentText, KindAskCard))
		h.n.SetTiming(0, 10*time.Millisecond) // 聚合与冷却缩成毫秒级
		h.n.NotifyEvent(EventInfo{ID: "e1", Kind: "agent_text", Conversation: "c1"})
		h.n.NotifyEvent(EventInfo{ID: "e2", Kind: "ask_card", Conversation: "c2"})
		time.Sleep(400 * time.Millisecond)
		if h.rec.count() != 0 {
			t.Fatalf("不该推送：%d", h.rec.count())
		}
		// 没关的类型不受影响，并且用表里的优先级。
		h.n.NotifyEvent(EventInfo{ID: "e3", Kind: "mention", Penetrate: true})
		waitFor(t, "@你仍推", func() bool { return h.rec.count() == 1 })
		if p := h.rec.last().Body.Priority; p != 4 {
			t.Errorf("mention 内置默认 4：%d", p)
		}
	})

	t.Run("聚合期间被关掉", func(t *testing.T) {
		h := newHarness(t, nil)
		h.n.SetTiming(0, 100*time.Millisecond)
		h.n.NotifyEvent(EventInfo{ID: "e1", Kind: "agent_text", Conversation: "c1"})
		time.Sleep(50 * time.Millisecond) // 进了聚合批次，还没到期
		h.setConfig(func(c *Config) { c.PriorityByKind = map[string]int{KindAgentText: 0} })
		time.Sleep(700 * time.Millisecond)
		if h.rec.count() != 0 {
			t.Fatalf("聚合期间设成不推送，到期后不该再发：%d", h.rec.count())
		}
	})
}

// 表里改的优先级真的出现在发出去的报文里。
func TestKindGating_PriorityOverridesReachTheWire(t *testing.T) {
	h := newHarness(t, func(c *Config) {
		c.RunNotify = true
		c.PriorityByKind = map[string]int{KindRequest: 2, KindAutoResume: 1, KindRunOK: 5, KindMention: 2, KindAgentText: 5}
	})
	h.n.SetTiming(0, 10*time.Millisecond)

	check := func(what string, want int, send func()) {
		t.Helper()
		before := h.rec.count()
		send()
		waitFor(t, what, func() bool { return h.rec.count() == before+1 })
		if got := h.rec.last().Body.Priority; got != want {
			t.Errorf("%s：期望 %d 级，实际 %d", what, want, got)
		}
	}
	check("等你回复", 2, func() { h.n.NotifyRequest(sess("s1", "t")) })
	check("自动续跑告警", 1, func() { h.n.NotifyAutoResume(AutoResumeInfo{Key: "g1", Name: "预设"}) })
	check("命令成功单独改成 5（不再受最多 3 限制）", 5, func() {
		h.n.NotifyRun(RunInfo{RunID: "r1", State: model.RunSucceeded, Duration: 2 * time.Minute})
	})
	check("@你", 2, func() { h.n.NotifyEvent(EventInfo{ID: "e1", Kind: "mention", Penetrate: true}) })
	check("Agent 文本（聚合后发出）", 5, func() { h.n.NotifyEvent(EventInfo{ID: "e2", Kind: "agent_text", Conversation: "c1"}) })
}

func TestSendKindTest_UsesKindPriority(t *testing.T) {
	h := newHarness(t, withOverrides(map[string]int{KindMention: 2}))
	cfg, _ := h.n.EffectiveConfig(context.Background())

	_, p, err := h.n.SendKindTest(context.Background(), cfg, KindMention)
	if err != nil || p != 2 {
		t.Fatalf("应按 2 级试发：%d %v", p, err)
	}
	if got := h.rec.last().Body; got.Priority != 2 || !strings.Contains(got.Title, "群里 @ 了你") {
		t.Errorf("报文不对：%+v", got)
	}
	if st := h.n.Status(); !st.LastTest || !st.LastOK {
		t.Errorf("试发应记成最近一次测试推送：%+v", st)
	}

	// 默认关闭的异常类也能试发（用建议值），不看开关。
	_, p, err = h.n.SendKindTest(context.Background(), cfg, KindHostOffline)
	if err != nil || p != 4 {
		t.Fatalf("默认关闭的异常类试发应走建议值 4：%d %v", p, err)
	}

	// 兼容老入口：SendTest 就是不带类型的试发。
	if _, err := h.n.SendTest(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	if got := h.rec.last().Body; got.Title != "RelayMesh 测试推送" || got.Priority != 4 {
		t.Errorf("SendTest 应是普通测试推送：%+v", got)
	}
}
