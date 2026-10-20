package notify

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestEventNormalizeTextFields(t *testing.T) {
	ev := Event{
		ID:         "  e1 ",
		Kind:       KindAgentText,
		WorkflowID: " demo ",
		Sender:     "  老板\n机",
		Title:      "项目\t甲",
		Preview:    "第一行\n\n第二行   第三段",
	}.Normalize(t0)
	if ev.ID != "e1" || ev.WorkflowID != "demo" {
		t.Errorf("ID / WorkflowID 应去掉首尾空白：%+v", ev)
	}
	if ev.Conversation != "wf:demo" {
		t.Errorf("缺省会话应由工作流推出：%q", ev.Conversation)
	}
	if ev.Sender != "老板 机" || ev.Title != "项目 甲" || ev.Preview != "第一行 第二行 第三段" {
		t.Errorf("文本应折成一行：%+v", ev)
	}
	if !ev.CreatedAt.Equal(t0) {
		t.Error("缺省时间应补当前时间")
	}
	// 显式会话不被覆盖。
	ev = Event{Conversation: "grp:g", WorkflowID: "demo"}.Normalize(t0)
	if ev.Conversation != "grp:g" {
		t.Errorf("显式会话不应被覆盖：%q", ev.Conversation)
	}
}

func TestEventNormalizeClipsByRunes(t *testing.T) {
	long := strings.Repeat("汉", 300)
	ev := Event{Preview: long, Title: long, Sender: long}.Normalize(t0)
	for name, s := range map[string]string{"preview": ev.Preview, "title": ev.Title, "sender": ev.Sender} {
		if !utf8.ValidString(s) {
			t.Errorf("%s 截断后不是合法 UTF-8", name)
		}
		if !strings.HasSuffix(s, "…") {
			t.Errorf("%s 截断后应以省略号结尾：%q", name, s)
		}
	}
	if n := utf8.RuneCountInString(ev.Preview); n != MaxPreviewRunes {
		t.Errorf("预览应恰好 %d 字（含省略号），得到 %d", MaxPreviewRunes, n)
	}
	// 恰好等于上限时不截断。
	exact := strings.Repeat("a", MaxPreviewRunes)
	if got := (Event{Preview: exact}).Normalize(t0).Preview; got != exact {
		t.Error("恰好等于上限时不应截断")
	}
	// 短文本原样保留。
	if got := (Event{Preview: "你好"}).Normalize(t0).Preview; got != "你好" {
		t.Errorf("短文本应原样保留：%q", got)
	}
}

func TestEventNormalizeDeepLink(t *testing.T) {
	cases := map[string]string{
		"/":                            "/",
		"/?wf=demo&s=abc":              "/?wf=demo&s=abc",
		"  /sessions/1  ":              "/sessions/1",
		"":                             "",
		"https://evil.example/x":       "",
		"//evil.example/x":             "",
		`/\evil.example`:               "",
		"javascript:alert(1)":          "",
		"relative/path":                "",
		"/has space":                   "",
		"/has\nnewline":                "",
		"/" + strings.Repeat("x", 600): "",
	}
	for in, want := range cases {
		if got := (Event{DeepLink: in}).Normalize(t0).DeepLink; got != want {
			t.Errorf("deep_link %q：得到 %q，期望 %q", in, got, want)
		}
	}
}

func TestEventValid(t *testing.T) {
	if !(Event{ID: "e1", Kind: KindAskCard}).Valid() {
		t.Error("有 ID 与已登记种类应有效")
	}
	for _, ev := range []Event{
		{Kind: KindAskCard},       // 没有 ID
		{ID: "e1"},                // 没有种类
		{ID: "e1", Kind: "bogus"}, // 未登记种类
		{ID: strings.Repeat("x", 201), Kind: KindAskCard}, // ID 过长
	} {
		if ev.Valid() {
			t.Errorf("不应有效：%+v", ev)
		}
	}
}

func TestConversationOf(t *testing.T) {
	if ConversationOf(" demo ") != "wf:demo" {
		t.Error("会话 id 应由工作流推出并去掉空白")
	}
}
