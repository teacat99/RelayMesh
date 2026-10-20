package ntfy

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/teacat99/RelayMesh/v2/internal/model"
)

func TestBuildRequestMessage_DefaultIsTitleOnly(t *testing.T) {
	cfg := Config{Priority: 5, ClickURL: "https://rm.example.com/"}.Normalize()
	in := RequestInfo{
		SessionID: "s1", Title: "第十批：方案确认", Summary: "这里是机密的长摘要，不应该出现在推送里",
		ProjectDirectory: "/home/user/RelayMesh", HostName: "host-1",
	}
	m := BuildRequestMessage(cfg, in, 0)

	if m.Title != "智能体在等你回复" || m.Priority != 5 || m.Click != "https://rm.example.com/" {
		t.Errorf("标题 / 优先级 / 点击地址不对：%+v", m)
	}
	if len(m.Tags) != 1 || m.Tags[0] != "speech_balloon" {
		t.Errorf("标签不对：%v", m.Tags)
	}
	if strings.Contains(m.Body, "机密") {
		t.Errorf("默认不应附带摘要：%q", m.Body)
	}
	for _, want := range []string{"第十批：方案确认", "项目：RelayMesh", "主机：host-1"} {
		if !strings.Contains(m.Body, want) {
			t.Errorf("正文应含 %q：%q", want, m.Body)
		}
	}
}

func TestBuildRequestMessage_IncludeSummaryStripsMarkdown(t *testing.T) {
	cfg := Config{IncludeSummary: true}.Normalize()
	summary := "## 本轮结论\n\n- 已完成 **A**\n- 已完成 B\n\n```go\nfmt.Println(\"secret code\")\n```\n\n> 需要你确认\n1. 是否发布"
	m := BuildRequestMessage(cfg, RequestInfo{Title: "t", Summary: summary}, 0)

	if !strings.Contains(m.Body, "本轮结论 已完成 A 已完成 B 需要你确认 是否发布") {
		t.Errorf("摘要应折成一行纯文本：%q", m.Body)
	}
	if strings.Contains(m.Body, "secret code") || strings.Contains(m.Body, "```") {
		t.Errorf("代码块应整段丢弃：%q", m.Body)
	}
}

func TestBuildRequestMessage_ExcerptTruncatesByRunes(t *testing.T) {
	cfg := Config{IncludeSummary: true}.Normalize()
	m := BuildRequestMessage(cfg, RequestInfo{Title: "t", Summary: strings.Repeat("汉", 500)}, 0)

	if !utf8.ValidString(m.Body) {
		t.Fatalf("截断不应劈开汉字：%q", m.Body)
	}
	lines := strings.Split(m.Body, "\n")
	excerpt := lines[1]
	if r := []rune(excerpt); len(r) != summaryExcerptLen+1 || r[len(r)-1] != '…' {
		t.Errorf("摘要应截成 %d 个字符加省略号，实际 %d：%q", summaryExcerptLen, len(r), excerpt)
	}
}

func TestBuildRequestMessage_FallbacksAndFolding(t *testing.T) {
	cfg := Config{}.Normalize()

	m := BuildRequestMessage(cfg, RequestInfo{}, 0)
	if m.Body != "收到新的 AI 反馈请求，请及时查看。" {
		t.Errorf("没有任何信息时应有兜底文案：%q", m.Body)
	}

	// 标题里的换行折成一行；没有项目目录时退回工作流标识。
	m = BuildRequestMessage(cfg, RequestInfo{Title: "第一行\n第二行", WorkflowID: "relaymesh-continuation"}, 0)
	if !strings.HasPrefix(m.Body, "第一行 第二行\n") || !strings.Contains(m.Body, "项目：relaymesh-continuation") {
		t.Errorf("折叠 / 退回工作流标识不对：%q", m.Body)
	}

	// Windows 路径与末尾分隔符。
	m = BuildRequestMessage(cfg, RequestInfo{Title: "t", ProjectDirectory: `C:\Users\teaca\Projects\Demo\`}, 0)
	if !strings.Contains(m.Body, "项目：Demo") {
		t.Errorf("应取 Windows 路径的最后一段：%q", m.Body)
	}
}

func TestBuildRequestMessage_MergedNote(t *testing.T) {
	m := BuildRequestMessage(Config{}.Normalize(), RequestInfo{Title: "t"}, 3)
	// 被合并的既可能是请求、也可能是运行结束，所以统一说「通知」。
	if !strings.Contains(m.Body, "另有 3 条通知因推送过于频繁被合并") {
		t.Errorf("应说明被合并的条数：%q", m.Body)
	}
}

func TestBuildTestMessage(t *testing.T) {
	m := BuildTestMessage(Config{Priority: 2, ClickURL: "https://x.test/"}.Normalize(), time.Date(2026, 10, 9, 14, 3, 5, 0, time.UTC))
	if m.Title != "RelayMesh 测试推送" || !strings.Contains(m.Body, "14:03:05") || m.Priority != 2 || m.Click != "https://x.test/" {
		t.Errorf("测试推送内容不对：%+v", m)
	}
}

func TestInfoFromSession_Snapshot(t *testing.T) {
	s := &model.FeedbackSession{ID: "s1", WorkflowID: "wf", Title: "标题", Summary: "摘要", ProjectDirectory: "/a/b", HostName: "h"}
	in := InfoFromSession(s)
	s.Title = "之后被改了" // 推送在别的协程里异步完成，快照不能随会话对象变化
	if in.Title != "标题" || in.SessionID != "s1" || in.WorkflowID != "wf" || in.HostName != "h" {
		t.Errorf("快照不对：%+v", in)
	}
	if got := InfoFromSession(nil); got != (RequestInfo{}) {
		t.Errorf("nil 应得到空快照：%+v", got)
	}
}
