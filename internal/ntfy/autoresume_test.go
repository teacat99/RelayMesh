package ntfy

import (
	"context"
	"strings"
	"testing"
)

func gaveUpInfo() AutoResumeInfo {
	return AutoResumeInfo{
		Key: "arz:p1:run-9", Name: "Cursor CLI", Machine: "RM-AAAA-BBBB",
		Reason:     "24 小时内已经自动续跑 3 次，达到上限；这次的失败原因：Cursor CLI 报了「可重试」的错误",
		WorkflowID: "relaymesh-continuation",
	}
}

func TestBuildAutoResumeMessage_Wording(t *testing.T) {
	cfg := Config{Priority: 4, ClickURL: "https://rm.example.com/"}
	m := BuildAutoResumeMessage(cfg, gaveUpInfo(), "build-box", 0)
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
	if m.Priority != 4 || m.Click != "https://rm.example.com/" {
		t.Errorf("优先级 / 点击地址不对：%+v", m)
	}

	// 没解析出主机名就不写主机行（更不能拿机器码凑数）。
	m = BuildAutoResumeMessage(cfg, gaveUpInfo(), "", 0)
	if strings.Contains(m.Body, "主机：") || strings.Contains(m.Body, "RM-AAAA-BBBB") {
		t.Errorf("没解析出主机名就不该写主机行：\n%s", m.Body)
	}
}

func TestBuildAutoResumeMessage_MergedNote(t *testing.T) {
	m := BuildAutoResumeMessage(Config{Priority: 3}, gaveUpInfo(), "", 3)
	if !strings.Contains(m.Body, "另有 3 条通知") {
		t.Errorf("被合并的条数应说明：\n%s", m.Body)
	}
}

func TestBuildAutoResumeMessage_BoundsAndSanitizesText(t *testing.T) {
	in := gaveUpInfo()
	in.Name = "名字\n很长" + strings.Repeat("长", 80)
	in.Reason = "第一行\n\n第二行 " + strings.Repeat("因", 400)
	in.WorkflowID = ""
	m := BuildAutoResumeMessage(Config{}, in, strings.Repeat("机", 100), 0)
	if strings.Contains(m.Title, "\n") || len([]rune(m.Title)) > nameMaxLen+12 {
		t.Errorf("标题应是单行且有上限：%q", m.Title)
	}
	lines := strings.Split(m.Body, "\n")
	if len([]rune(lines[0])) > reasonMaxLen+1 || strings.Contains(lines[0], "\n") {
		t.Errorf("原因应折成一行并截短：%d 字 %q", len([]rune(lines[0])), lines[0])
	}
	if strings.Contains(m.Body, "项目：") {
		t.Errorf("没有工作流编号就不写项目行：\n%s", m.Body)
	}

	blank := BuildAutoResumeMessage(Config{}, AutoResumeInfo{Key: "k"}, "", 0)
	if blank.Title != "「终端会话」不再自动续跑" {
		t.Errorf("没有预设名时用兜底名字：%q", blank.Title)
	}
}

func TestNotifier_AutoResumePushesOnceAndResolvesHostName(t *testing.T) {
	h := newHarness(t, func(c *Config) { c.Priority = 5; c.Token = "tk_abc" }, func(o *Options) {
		o.HostName = func(_ context.Context, machine string) string {
			if machine == "RM-AAAA-BBBB" {
				return "build-box"
			}
			return ""
		}
	})
	h.n.NotifyAutoResume(gaveUpInfo())
	h.n.NotifyAutoResume(gaveUpInfo()) // 同一条（同一个 Key）：进程内去重
	waitFor(t, "收到推送", func() bool { return h.rec.count() >= 1 })
	settle()
	if got := h.rec.count(); got != 1 {
		t.Fatalf("同一个 Key 只应推一条，实际 %d 条", got)
	}
	got := h.rec.last()
	if got.Body.Topic != "topic-1" || got.Body.Title != "「Cursor CLI」不再自动续跑" || got.Body.Priority != 5 {
		t.Errorf("推送内容不对：%+v", got.Body)
	}
	if !strings.Contains(got.Body.Message, "主机：build-box") || strings.Contains(got.Body.Message, "RM-AAAA-BBBB") {
		t.Errorf("应写主机名、不写机器码：%q", got.Body.Message)
	}
	if got.Header.Get("Authorization") != "Bearer tk_abc" {
		t.Errorf("认证头不对：%v", got.Header)
	}

	// 另一次放弃（别的运行）带着不同的 Key，是另一条推送。
	again := gaveUpInfo()
	again.Key = "arz:p1:run-10"
	h.n.NotifyAutoResume(again)
	waitFor(t, "另一次也推送", func() bool { return h.rec.count() == 2 })
}

func TestNotifier_AutoResumeRespectsMasterSwitchTopicAndEmptyKey(t *testing.T) {
	h := newHarness(t, nil)
	h.setConfig(func(c *Config) { c.Enabled = false })
	h.n.NotifyAutoResume(gaveUpInfo())
	settle()
	if h.rec.count() != 0 {
		t.Fatal("总开关关闭时不应推送")
	}

	h.setConfig(func(c *Config) { c.Enabled = true; c.Topic = "" })
	in := gaveUpInfo()
	in.Key = "other"
	h.n.NotifyAutoResume(in)
	settle()
	if h.rec.count() != 0 {
		t.Fatal("没有主题时不应推送")
	}

	h.n.NotifyAutoResume(AutoResumeInfo{Name: "没有 Key"})
	var nilN *Notifier
	nilN.NotifyAutoResume(gaveUpInfo()) // 没有接线（nil 接收者）也不能 panic
	settle()
	if h.rec.count() != 0 {
		t.Fatal("没有 Key 的事件不应推送")
	}

	// 对照：配置改好之后立即生效。
	h.setConfig(func(c *Config) { c.Topic = "ok-topic" })
	in.Key = "fresh"
	h.n.NotifyAutoResume(in)
	waitFor(t, "打开后推送", func() bool { return h.rec.count() == 1 })
}

func TestNotifier_AutoResumeSharesTheRateLimitWithOtherEvents(t *testing.T) {
	h := newHarness(t, nil)
	for i := 0; i < rateMaxPerWindow+2; i++ {
		in := gaveUpInfo()
		in.Key = "burst-" + string(rune('a'+i))
		h.n.NotifyAutoResume(in)
	}
	waitFor(t, "限额内的都推了", func() bool { return h.rec.count() >= rateMaxPerWindow })
	settle()
	if got := h.rec.count(); got != rateMaxPerWindow {
		t.Fatalf("一分钟内最多 %d 条，超出的合并，实际 %d 条", rateMaxPerWindow, got)
	}
}
