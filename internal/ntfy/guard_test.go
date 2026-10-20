package ntfy

import (
	"context"
	"strings"
	"testing"
	"time"
)

var guardNow = time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC)

func stalledInfo() GuardInfo {
	return GuardInfo{
		Key:        "p1:stalled:100:0",
		Name:       "Cursor CLI",
		Machine:    "RM-AAAA-BBBB",
		Reason:     "上一轮对话因出错中断（6 小时前），之后没有新的活动",
		Since:      guardNow.Add(-6 * time.Hour),
		WorkflowID: "relaymesh-continuation",
	}
}

func TestBuildGuardMessage_StalledAndCrashedWording(t *testing.T) {
	cfg := Config{Priority: 4, ClickURL: "https://rm.example.com/"}

	m := BuildGuardMessage(cfg, stalledInfo(), "build-box", 0, guardNow)
	if m.Title != "「Cursor CLI」疑似停摆" {
		t.Errorf("停摆的标题不对：%q", m.Title)
	}
	for _, want := range []string{"上一轮对话因出错中断", "主机：build-box", "项目：relaymesh-continuation", "已持续 6 小时", "未自动唤醒"} {
		if !strings.Contains(m.Body, want) {
			t.Errorf("正文应包含 %q：\n%s", want, m.Body)
		}
	}
	if m.Priority != 4 || m.Click != "https://rm.example.com/" || len(m.Tags) != 1 || m.Tags[0] != "hourglass_flowing_sand" {
		t.Errorf("优先级 / 点击地址 / 标签不对：%+v", m)
	}

	crashed := stalledInfo()
	crashed.Crashed, crashed.Reason = true, "命令异常退出（退出码 137）"
	m = BuildGuardMessage(cfg, crashed, "", 0, guardNow)
	if m.Title != "「Cursor CLI」意外结束" || m.Tags[0] != "rotating_light" {
		t.Errorf("意外结束的标题 / 标签不对：%+v", m)
	}
	if strings.Contains(m.Body, "主机：") {
		t.Errorf("没解析出主机名就不该写主机行（更不能拿机器码凑数）：\n%s", m.Body)
	}
	if strings.Contains(m.Body, "RM-AAAA-BBBB") {
		t.Errorf("正文里不能出现机器码：\n%s", m.Body)
	}
}

// 「不再自动续跑」（#151）复用守护的推送通道，但标题、标签与末尾说明都要换成它自己的，不能说成「疑似停摆」或「未自动唤醒」。
func TestBuildGuardMessage_AutoResumeGaveUpWording(t *testing.T) {
	in := GuardInfo{
		Key: "arz:p1:run-9", Kind: GuardKindAutoResumeGaveUp, Name: "Cursor CLI", Machine: "RM-AAAA-BBBB",
		Reason: "24 小时内已经自动续跑 3 次，达到上限；这次的失败原因：Cursor CLI 报了「可重试」的错误", WorkflowID: "relaymesh-continuation",
	}
	m := BuildGuardMessage(Config{Priority: 4}, in, "build-box", 0, guardNow)
	if m.Title != "「Cursor CLI」不再自动续跑" || len(m.Tags) != 1 || m.Tags[0] != "no_entry_sign" {
		t.Fatalf("标题 / 标签不对：%+v", m)
	}
	for _, want := range []string{"达到上限", "主机：build-box", "项目：relaymesh-continuation", "手动运行", "关掉自动续跑"} {
		if !strings.Contains(m.Body, want) {
			t.Errorf("正文应包含 %q：\n%s", want, m.Body)
		}
	}
	for _, bad := range []string{"疑似停摆", "未自动唤醒", "一键唤醒", "已持续", "RM-AAAA-BBBB"} {
		if strings.Contains(m.Title+m.Body, bad) {
			t.Errorf("不该出现 %q：\n%s\n%s", bad, m.Title, m.Body)
		}
	}
}

func TestBuildGuardMessage_ReminderMergedAndUnknownStart(t *testing.T) {
	in := stalledInfo()
	in.Reminder = 1
	in.Since = time.Time{}
	m := BuildGuardMessage(Config{Priority: 3}, in, "", 3, guardNow)
	if !strings.HasPrefix(m.Title, "（仍未处理）") {
		t.Errorf("提醒应在标题里说明还没处理：%q", m.Title)
	}
	if strings.Contains(m.Body, "已持续") {
		t.Errorf("起点不详时不该写持续时间：\n%s", m.Body)
	}
	if !strings.Contains(m.Body, "另有 3 条通知") {
		t.Errorf("被合并的条数应说明：\n%s", m.Body)
	}

	// 起点晚于现在（时钟偏差）也不写负的持续时间。
	in.Since = guardNow.Add(time.Hour)
	if m := BuildGuardMessage(Config{}, in, "", 0, guardNow); strings.Contains(m.Body, "已持续") {
		t.Errorf("起点在未来不该写持续时间：\n%s", m.Body)
	}
}

func TestBuildGuardMessage_BoundsAndSanitizesText(t *testing.T) {
	in := stalledInfo()
	in.Name = "名字\n很长" + strings.Repeat("长", 80)
	in.Reason = "第一行\n\n第二行 " + strings.Repeat("因", 400)
	in.WorkflowID = ""
	m := BuildGuardMessage(Config{}, in, strings.Repeat("机", 100), 0, guardNow)
	if strings.Contains(m.Title, "\n") || len([]rune(m.Title)) > guardNameMaxLen+12 {
		t.Errorf("标题应是单行且有上限：%q", m.Title)
	}
	lines := strings.Split(m.Body, "\n")
	if len([]rune(lines[0])) > guardReasonMaxLen+1 || strings.Contains(lines[0], "\n") {
		t.Errorf("原因应折成一行并截短：%d 字 %q", len([]rune(lines[0])), lines[0])
	}
	if strings.Contains(m.Body, "项目：") {
		t.Errorf("没有工作流编号就不写项目行：\n%s", m.Body)
	}

	blank := BuildGuardMessage(Config{}, GuardInfo{Key: "k"}, "", 0, guardNow)
	if blank.Title != "「终端会话」疑似停摆" {
		t.Errorf("没有预设名时用兜底名字：%q", blank.Title)
	}
}

func TestNotifier_GuardPushesOnceAndResolvesHostName(t *testing.T) {
	h := newHarness(t, func(c *Config) { c.Priority = 5; c.Token = "tk_abc" }, func(o *Options) {
		o.HostName = func(_ context.Context, machine string) string {
			if machine == "RM-AAAA-BBBB" {
				return "build-box"
			}
			return ""
		}
	})
	h.n.NotifyGuard(stalledInfo())
	h.n.NotifyGuard(stalledInfo()) // 同一条（同一个 Key）：进程内去重
	waitFor(t, "收到守护推送", func() bool { return h.rec.count() >= 1 })
	settle()
	if got := h.rec.count(); got != 1 {
		t.Fatalf("同一个 Key 只应推一条，实际 %d 条", got)
	}
	got := h.rec.last()
	if got.Body.Topic != "topic-1" || got.Body.Title != "「Cursor CLI」疑似停摆" || got.Body.Priority != 5 {
		t.Errorf("推送内容不对：%+v", got.Body)
	}
	if !strings.Contains(got.Body.Message, "主机：build-box") || strings.Contains(got.Body.Message, "RM-AAAA-BBBB") {
		t.Errorf("应写主机名、不写机器码：%q", got.Body.Message)
	}
	if got.Header.Get("Authorization") != "Bearer tk_abc" {
		t.Errorf("认证头不对：%v", got.Header)
	}

	// 提醒带着不同的 Key，是另一条推送。
	again := stalledInfo()
	again.Key, again.Reminder = "p1:stalled:100:1", 1
	h.n.NotifyGuard(again)
	waitFor(t, "提醒也推送", func() bool { return h.rec.count() == 2 })
}

func TestNotifier_GuardRespectsMasterSwitchTopicAndEmptyKey(t *testing.T) {
	h := newHarness(t, nil)
	h.setConfig(func(c *Config) { c.Enabled = false })
	h.n.NotifyGuard(stalledInfo())
	settle()
	if h.rec.count() != 0 {
		t.Fatal("总开关关闭时不应推送守护事件")
	}

	h.setConfig(func(c *Config) { c.Enabled = true; c.Topic = "" })
	in := stalledInfo()
	in.Key = "other"
	h.n.NotifyGuard(in)
	settle()
	if h.rec.count() != 0 {
		t.Fatal("没有主题时不应推送")
	}

	h.n.NotifyGuard(GuardInfo{Name: "没有 Key"})
	var nilN *Notifier
	nilN.NotifyGuard(stalledInfo()) // 没有接线（nil 接收者）也不能 panic
	settle()
	if h.rec.count() != 0 {
		t.Fatal("没有 Key 的事件不应推送")
	}

	// 对照：配置改好之后立即生效。
	h.setConfig(func(c *Config) { c.Topic = "ok-topic" })
	in.Key = "fresh"
	h.n.NotifyGuard(in)
	waitFor(t, "打开后推送", func() bool { return h.rec.count() == 1 })
}

func TestNotifier_GuardSharesTheRateLimitWithOtherEvents(t *testing.T) {
	h := newHarness(t, nil)
	for i := 0; i < rateMaxPerWindow+2; i++ {
		in := stalledInfo()
		in.Key = "burst-" + string(rune('a'+i))
		h.n.NotifyGuard(in)
	}
	waitFor(t, "限额内的都推了", func() bool { return h.rec.count() >= rateMaxPerWindow })
	settle()
	if got := h.rec.count(); got != rateMaxPerWindow {
		t.Fatalf("一分钟内最多 %d 条，超出的合并，实际 %d 条", rateMaxPerWindow, got)
	}
}
