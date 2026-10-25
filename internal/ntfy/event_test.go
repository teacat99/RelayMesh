package ntfy

import (
	"fmt"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func evInfo(id, conv, preview string) EventInfo {
	return EventInfo{ID: id, Kind: "agent_text", Conversation: conv, Title: "RelayMesh", Sender: "后端组",
		Preview: preview, ShowPreview: true, DeepLink: "/?wf=demo&s=s1"}
}

func TestBuildEventMessage_TitleBodyPriority(t *testing.T) {
	cfg := Config{Priority: 5, ClickURL: "https://rm.example.com/"}.Normalize()

	m := BuildEventMessage(cfg, evInfo("e1", "wf:demo", "构建完成，等你确认"), 1, 0)
	if m.Title != "RelayMesh · 后端组" || m.Body != "构建完成，等你确认" {
		t.Fatalf("标题 / 正文不对：%+v", m)
	}
	// 新消息类的优先级取「各类消息的优先级」表里这一类的内置默认（agent_text = 3），不跟配置里的「默认优先级」走。
	if m.Priority != 3 {
		t.Errorf("agent_text 的内置默认应是 3：%d", m.Priority)
	}
	if m.Click != "https://rm.example.com/?wf=demo&s=s1" {
		t.Errorf("点击地址应是对外地址 + 深链：%q", m.Click)
	}
	if len(m.Tags) != 1 || m.Tags[0] != "speech_balloon" {
		t.Errorf("图标不对：%v", m.Tags)
	}

	// 穿透类（@你、等你批准、群聊过密）的内置默认是 4。
	in := evInfo("e2", "wf:demo", "x")
	in.Kind, in.Penetrate = "mention", true
	if p := BuildEventMessage(cfg, in, 1, 0).Priority; p != 4 {
		t.Errorf("mention 的内置默认应是 4：%d", p)
	}
	// 表里没有对应行的种类退回老规则：穿透类 4，其余 3。
	in.Kind = "some_future_kind"
	if p := BuildEventMessage(cfg, in, 1, 0).Priority; p != 4 {
		t.Errorf("未登记的穿透类应退回 4：%d", p)
	}
	in.Penetrate = false
	if p := BuildEventMessage(cfg, in, 1, 0).Priority; p != 3 {
		t.Errorf("未登记的非穿透类应退回 3：%d", p)
	}
}

func TestBuildEventMessage_TitleVariants(t *testing.T) {
	cfg := Config{}.Normalize()
	cases := []struct {
		name          string
		title, sender string
		want          string
	}{
		{"都有", "RelayMesh", "后端组", "RelayMesh · 后端组"},
		{"发送者与会话名相同（忽略大小写）", "RelayMesh", "relaymesh", "RelayMesh"},
		{"没有发送者", "RelayMesh", "", "RelayMesh"},
		{"没有会话名", "", "后端组", "RelayMesh · 后端组"},
		{"都没有", "", "", "RelayMesh"},
		{"折成一行", "多行\n标题", "  发 送  者 ", "多行 标题 · 发 送 者"},
	}
	for _, c := range cases {
		in := EventInfo{ID: "e", Title: c.title, Sender: c.sender}
		if got := BuildEventMessage(cfg, in, 1, 0).Title; got != c.want {
			t.Errorf("%s：得到 %q，期望 %q", c.name, got, c.want)
		}
	}
	long := BuildEventMessage(cfg, EventInfo{ID: "e", Title: strings.Repeat("长", 200)}, 1, 0).Title
	if n := utf8.RuneCountInString(long); n > eventTitleMax {
		t.Errorf("标题不应超过 %d 字：%d", eventTitleMax, n)
	}
}

func TestBuildEventMessage_BodyVariants(t *testing.T) {
	cfg := Config{}.Normalize()
	hidden := func(kind string) EventInfo {
		return EventInfo{ID: "e", Kind: kind, Preview: "不该出现的正文", ShowPreview: false}
	}
	for kind, want := range map[string]string{
		"agent_text":       "有新消息",
		"ask_card":         "有新的待确认提问",
		"mention":          "有人 @ 了你",
		"approval_waiting": "终端在等你批准",
		"":                 "有新消息",
	} {
		got := BuildEventMessage(cfg, hidden(kind), 1, 0).Body
		if got != want {
			t.Errorf("隐藏预览时 %q 应写 %q，得到 %q", kind, want, got)
		}
		if strings.Contains(got, "不该出现") {
			t.Errorf("隐藏预览时正文不能出现预览：%q", got)
		}
	}

	// 显示预览：折成一行并截到 120 字。
	shown := EventInfo{ID: "e", Preview: "第一行\n第二行", ShowPreview: true}
	if got := BuildEventMessage(cfg, shown, 1, 0).Body; got != "第一行 第二行" {
		t.Errorf("预览应折成一行：%q", got)
	}
	shown.Preview = strings.Repeat("汉", 300)
	if n := utf8.RuneCountInString(BuildEventMessage(cfg, shown, 1, 0).Body); n > eventPreviewMax {
		t.Errorf("预览不应超过 %d 字：%d", eventPreviewMax, n)
	}
	// 显示预览但预览为空：退回「有新消息」。
	if got := BuildEventMessage(cfg, EventInfo{ID: "e", ShowPreview: true}, 1, 0).Body; got != "有新消息" {
		t.Errorf("没有预览时应写「有新消息」：%q", got)
	}

	// 聚合：N 条新消息；显示预览时附最新一条。
	if got := BuildEventMessage(cfg, hidden("agent_text"), 10, 0).Body; got != "10 条新消息" {
		t.Errorf("聚合后应写「N 条新消息」：%q", got)
	}
	if got := BuildEventMessage(cfg, EventInfo{ID: "e", Preview: "最后一条", ShowPreview: true}, 3, 0).Body; got != "3 条新消息\n最新：最后一条" {
		t.Errorf("聚合 + 预览：%q", got)
	}
	// 全局限流的说明追加在末尾。
	if got := BuildEventMessage(cfg, hidden("agent_text"), 1, 4).Body; !strings.HasSuffix(got, "（另有 4 条通知因推送过于频繁被合并，请到控制台查看）") {
		t.Errorf("应附限流说明：%q", got)
	}
}

func TestEventClick(t *testing.T) {
	cases := []struct{ base, link, want string }{
		{"", "/?wf=a", ""},
		{"https://rm.example.com", "", "https://rm.example.com"},
		{"https://rm.example.com/", "/?wf=a&s=b", "https://rm.example.com/?wf=a&s=b"},
		{"https://rm.example.com", "/?wf=a", "https://rm.example.com/?wf=a"},
		{"https://rm.example.com/app/", "/?wf=a", "https://rm.example.com/app/?wf=a"},
		{"https://rm.example.com/app", "/docs/x?y=1", "https://rm.example.com/app/docs/x?y=1"},
		{"https://rm.example.com/?lang=zh", "/?wf=a", "https://rm.example.com/?lang=zh&wf=a"},
		{"https://rm.example.com/", "relative", "https://rm.example.com/"},
		{"  https://rm.example.com/  ", "/?wf=a", "https://rm.example.com/?wf=a"},
		{"%zz", "/?wf=a", "%zz"}, // 解析失败：原样返回，不拼接
	}
	for _, c := range cases {
		if got := eventClick(c.base, c.link); got != c.want {
			t.Errorf("eventClick(%q, %q) = %q，期望 %q", c.base, c.link, got, c.want)
		}
	}
}

// 事件聚合用的测试夹具：聚合 5 → 5×单位，冷却 20 → 20×单位。
func newEventHarness(t *testing.T, unit time.Duration, mutate func(*Config)) *harness {
	t.Helper()
	h := newHarness(t, mutate)
	h.n.mergeUnit = unit
	return h
}

func TestNotifier_EventSingleSendsOriginal(t *testing.T) {
	h := newEventHarness(t, 10*time.Millisecond, func(c *Config) { c.ClickURL = "https://rm.example.com/" })
	h.n.NotifyEvent(evInfo("e1", "wf:demo", "只有一条"))

	waitFor(t, "收到推送", func() bool { return h.rec.count() == 1 })
	got := h.rec.last().Body
	if got.Topic != "topic-1" || got.Title != "RelayMesh · 后端组" || got.Message != "只有一条" || got.Priority != 3 ||
		got.Click != "https://rm.example.com/?wf=demo&s=s1" {
		t.Fatalf("推送内容不对：%+v", got)
	}
	settle()
	if h.rec.count() != 1 {
		t.Fatalf("单条消息只应推送一次：%d", h.rec.count())
	}
}

func TestNotifier_EventBurstCoalescesIntoOne(t *testing.T) {
	// 设计验收：突发的 10 条同会话消息合并成 1 条推送。
	// 直接走处理函数把 10 条在微秒内并入同一批次（不依赖工作协程的调度速度），再等聚合窗口（5×60ms = 300ms）到期。
	h := newEventHarness(t, 60*time.Millisecond, nil)
	for i := 1; i <= 10; i++ {
		h.n.handleEvent(h.cfg, evInfo(fmt.Sprintf("e%d", i), "wf:demo", fmt.Sprintf("消息 %d", i)))
	}
	waitFor(t, "收到合并后的推送", func() bool { return h.rec.count() >= 1 })
	time.Sleep(300 * time.Millisecond)
	if h.rec.count() != 1 {
		t.Fatalf("10 条突发消息应合并成 1 条推送，实际 %d 条", h.rec.count())
	}
	got := h.rec.last().Body
	if !strings.HasPrefix(got.Message, "10 条新消息") || !strings.Contains(got.Message, "最新：消息 10") {
		t.Fatalf("应写「10 条新消息」并附最新一条：%q", got.Message)
	}
}

func TestNotifier_EventCooldownSpacesPushesOfOneConversation(t *testing.T) {
	// 单位 40ms：聚合 200ms，冷却 800ms；中途检查点（200ms）离冷却结束留足 600ms 的余量。
	h := newEventHarness(t, 40*time.Millisecond, nil)
	start := time.Now()
	h.n.NotifyEvent(evInfo("a1", "wf:demo", "第一批"))
	waitFor(t, "第一批推送", func() bool { return h.rec.count() == 1 })

	// 第一批刚推完，又来一条：要等到距上次推送满冷却才推。
	h.n.NotifyEvent(evInfo("a2", "wf:demo", "第二批"))
	time.Sleep(200 * time.Millisecond)
	if h.rec.count() != 1 {
		t.Fatalf("冷却期内不应再推送同一会话：%d", h.rec.count())
	}
	waitFor(t, "冷却结束后推送第二批", func() bool { return h.rec.count() == 2 })
	if gap := time.Since(start); gap < 800*time.Millisecond {
		t.Fatalf("两次推送应至少隔一个冷却（约 800ms），整体只用了 %s", gap)
	}
	if got := h.rec.last().Body.Message; got != "第二批" {
		t.Fatalf("第二批应是原文：%q", got)
	}
}

func TestNotifier_EventDifferentConversationsDoNotMerge(t *testing.T) {
	h := newEventHarness(t, 10*time.Millisecond, nil)
	h.n.NotifyEvent(evInfo("a", "wf:one", "甲"))
	h.n.NotifyEvent(evInfo("b", "wf:two", "乙"))
	waitFor(t, "两个会话各一条", func() bool { return h.rec.count() == 2 })
	settle()
	if h.rec.count() != 2 {
		t.Fatalf("不同会话互不聚合：%d", h.rec.count())
	}
}

func TestNotifier_EventWithoutConversationIsNotMerged(t *testing.T) {
	h := newEventHarness(t, 10*time.Millisecond, nil)
	h.n.NotifyEvent(evInfo("a", "", "甲"))
	h.n.NotifyEvent(evInfo("b", "", "乙"))
	waitFor(t, "没有会话的事件各推各的", func() bool { return h.rec.count() == 2 })
}

func TestNotifier_EventPenetrateIsImmediateAndNeverMerged(t *testing.T) {
	// 聚合设成 5 秒：穿透类如果被聚合，测试里根本等不到。
	h := newEventHarness(t, time.Second, nil)
	for i := 1; i <= 3; i++ {
		in := evInfo(fmt.Sprintf("p%d", i), "wf:demo", fmt.Sprintf("急 %d", i))
		in.Kind, in.Penetrate = "mention", true
		h.n.NotifyEvent(in)
	}
	waitFor(t, "穿透类立即推送", func() bool { return h.rec.count() == 3 })
	for _, c := range h.rec.calls {
		if c.Body.Priority != 4 || len(c.Body.Tags) != 1 || c.Body.Tags[0] != "bell" {
			t.Fatalf("穿透类应是优先级 4：%+v", c.Body)
		}
	}
}

func TestNotifier_EventDedupeAndDisabled(t *testing.T) {
	h := newEventHarness(t, 100*time.Millisecond, nil) // 聚合窗口 500ms：足够宽，机器忙时「关开关」也赶得及在到期前
	h.n.NotifyEvent(evInfo("same", "wf:demo", "一条"))
	h.n.NotifyEvent(evInfo("same", "wf:demo", "一条"))
	waitFor(t, "收到推送", func() bool { return h.rec.count() >= 1 })
	settle()
	if h.rec.count() != 1 {
		t.Fatalf("同一个 ID 只应推送一次：%d", h.rec.count())
	}

	// 总开关关闭：不推送；聚合期间被关掉，到期时也不推送（到期时现读配置）。
	h.setConfig(func(c *Config) { c.Enabled = false })
	h.n.NotifyEvent(evInfo("off", "wf:other", "关了"))
	settle()
	if h.rec.count() != 1 {
		t.Fatalf("关闭后不应推送：%d", h.rec.count())
	}

	h.setConfig(func(c *Config) { c.Enabled = true })
	h.n.NotifyEvent(evInfo("mid", "wf:third", "聚合中被关掉"))
	// 等批次确实登记了再关总开关：这样验证的才是「到期时现读配置」，而不是「入队时就被挡掉」。
	waitFor(t, "批次已登记", func() bool {
		h.n.mu.Lock()
		defer h.n.mu.Unlock()
		return h.n.batches["wf:third"] != nil
	})
	h.setConfig(func(c *Config) { c.Enabled = false })
	time.Sleep(800 * time.Millisecond) // 聚合窗口 5×100ms = 500ms，再留余量
	if h.rec.count() != 1 {
		t.Fatalf("聚合期间被关掉，到期时不应推送：%d", h.rec.count())
	}
}

func TestNotifier_EventSharesGlobalRateLimit(t *testing.T) {
	h := newEventHarness(t, 10*time.Millisecond, nil)
	for i := 0; i < rateMaxPerWindow+5; i++ {
		in := evInfo(fmt.Sprintf("p%02d", i), "wf:demo", "急")
		in.Kind, in.Penetrate = "mention", true
		h.n.NotifyEvent(in)
	}
	waitFor(t, "限流之前的推送发完", func() bool { return h.rec.count() == rateMaxPerWindow })
	settle()
	if h.rec.count() != rateMaxPerWindow {
		t.Fatalf("每分钟最多 %d 条，实际 %d", rateMaxPerWindow, h.rec.count())
	}
	if st := h.n.Status(); st.Dropped < 5 {
		t.Fatalf("超出的应记为被合并：%+v", st)
	}
}

func TestNotifier_EventStopCancelsPendingBatches(t *testing.T) {
	h := newEventHarness(t, 100*time.Millisecond, nil) // 聚合 500ms：Stop 一定赶在到期之前
	h.n.NotifyEvent(evInfo("e1", "wf:demo", "待发"))
	waitFor(t, "批次已登记", func() bool {
		h.n.mu.Lock()
		defer h.n.mu.Unlock()
		return len(h.n.batches) == 1
	})
	h.n.Stop()
	time.Sleep(700 * time.Millisecond)
	if h.rec.count() != 0 {
		t.Fatalf("Stop 之后不应再推送：%d", h.rec.count())
	}
	h.n.mu.Lock()
	left := len(h.n.batches)
	h.n.mu.Unlock()
	if left != 0 {
		t.Fatalf("Stop 应清空等待中的批次：%d", left)
	}
	h.n.NotifyEvent(evInfo("late", "wf:demo", "停止后")) // 不能 panic
}

func TestNotifier_EventBatchesAreBounded(t *testing.T) {
	h := newEventHarness(t, time.Hour, nil) // 永不到期
	// 直接走处理函数：这里验证的是「同时等待聚合的会话数有上限」。
	// 若经 NotifyEvent 入队，结果会取决于工作协程消化 64 格队列的速度（队列满时丢弃是另一道防线，机器忙时会让本测试随机失败）。
	for i := 0; i < maxBatches+20; i++ {
		h.n.handleEvent(h.cfg, evInfo(fmt.Sprintf("e%d", i), fmt.Sprintf("wf:c%d", i), "x"))
	}
	h.n.mu.Lock()
	size := len(h.n.batches)
	h.n.mu.Unlock()
	if size != maxBatches {
		t.Fatalf("等待聚合的会话数应恰好封顶在 %d：%d", maxBatches, size)
	}
	if got := h.n.Status().Dropped; got != 20 {
		t.Fatalf("超出上限的 20 条事件应记为丢弃：%+v", h.n.Status())
	}
	// 已在批次里的会话仍然可以继续并入（只拒绝新开的会话）。
	h.n.handleEvent(h.cfg, evInfo("extra", "wf:c0", "x"))
	h.n.mu.Lock()
	merged := h.n.batches["wf:c0"].count
	h.n.mu.Unlock()
	if merged != 2 {
		t.Fatalf("上限不应挡住已有会话的并入：count=%d", merged)
	}
}

func TestNotifier_EventNilAndInvalidAreIgnored(t *testing.T) {
	var nilN *Notifier
	nilN.NotifyEvent(evInfo("x", "wf:demo", "x")) // 没有接线（nil 接收者）也不能 panic
	h := newEventHarness(t, 10*time.Millisecond, nil)
	h.n.NotifyEvent(EventInfo{}) // 没有 ID
	settle()
	if h.rec.count() != 0 {
		t.Fatalf("没有 ID 的事件不应推送：%d", h.rec.count())
	}
}

// 隐私：隐藏预览时报文里不含正文；日志里也没有预览与令牌。
func TestNotifier_EventHiddenPreviewNeverOnTheWire(t *testing.T) {
	h := newEventHarness(t, 10*time.Millisecond, func(c *Config) { c.Token = "tk_SECRETTOKEN" })
	in := evInfo("e1", "wf:demo", "绝密预览正文-QWERTY")
	in.ShowPreview = false
	h.n.NotifyEvent(in)
	waitFor(t, "收到推送", func() bool { return h.rec.count() == 1 })
	raw := h.rec.last().Raw
	if strings.Contains(raw, "QWERTY") || strings.Contains(raw, "绝密") {
		t.Fatalf("隐藏预览时报文不应含正文：%s", raw)
	}
	if !strings.Contains(raw, "有新消息") {
		t.Fatalf("隐藏预览时应只写「有新消息」：%s", raw)
	}
}
