package ntfy

import (
	"strings"
	"testing"
	"time"
)

// 「指派久无人接」这一类推送（#180 B3d）：表里的那一行、报文、默认不推、打开后只推一次。

func staleInfo(id string) EventInfo {
	return EventInfo{
		ID: id, Kind: "board_stale", Title: "指派久无人接",
		Preview: "#12「接口改造」派给 后端组，已超过 2 小时没人接", ShowPreview: true,
		DeepLink: "/work/wf-2?task=12",
	}
}

func TestBoardStale_KindRow(t *testing.T) {
	k, ok := kindIndex[KindBoardStale]
	if !ok || !KnownKind(KindBoardStale) || KindBoardStale != "board_stale" {
		t.Fatal("board_stale 应是已登记的类型，标识不能改名（它存在配置里）")
	}
	if k.Group != GroupException || k.DefaultOn || k.SuggestedPriority < 1 || k.SuggestedPriority > 5 || k.Label == "" || k.Desc == "" {
		t.Fatalf("应是默认不推送的异常类，带建议优先级: %+v", k)
	}
	// 没有覆盖：默认关；覆盖成 1~5 才打开；设成 0 仍是关。
	cfg := Config{Enabled: true, Topic: "t", Priority: 4}.Normalize()
	if _, on := cfg.PriorityFor(KindBoardStale); on {
		t.Error("默认不该推送")
	}
	cfg.PriorityByKind = map[string]int{KindBoardStale: 2}
	if p, on := cfg.PriorityFor(KindBoardStale); !on || p != 2 {
		t.Errorf("覆盖成 2 级应打开: (%d, %v)", p, on)
	}
	cfg.PriorityByKind = map[string]int{KindBoardStale: 0}
	if _, on := cfg.PriorityFor(KindBoardStale); on {
		t.Error("设成 0 是不推送")
	}
	// 闸里的种类名与表里的类型对得上。
	if got, ok := eventPushKind("board_stale"); !ok || got != KindBoardStale {
		t.Errorf("eventPushKind(board_stale) = (%q, %v)", got, ok)
	}
}

func TestBoardStale_EventMessage(t *testing.T) {
	cfg := Config{ClickURL: "https://rm.example.com/", PriorityByKind: map[string]int{KindBoardStale: 2}}.Normalize()

	m := BuildEventMessage(cfg, staleInfo("bst:1"), 1, 0)
	if m.Title != "指派久无人接" || m.Body != "#12「接口改造」派给 后端组，已超过 2 小时没人接" {
		t.Fatalf("标题 / 正文不对: %+v", m)
	}
	if m.Priority != 2 {
		t.Errorf("应取表里设的 2 级: %d", m.Priority)
	}
	if len(m.Tags) != 1 || m.Tags[0] != "hourglass_flowing_sand" {
		t.Errorf("图标不对: %v", m.Tags)
	}
	if m.Click != "https://rm.example.com/work/wf-2?task=12" {
		t.Errorf("点击应打开任务页的深链: %q", m.Click)
	}

	// 预览被关掉（偏好 never，或跨项目）：不能把任务标题带出去，只留这一类的一句话。
	hidden := staleInfo("bst:2")
	hidden.ShowPreview = false
	got := BuildEventMessage(cfg, hidden, 1, 0).Body
	if got != "有指派很久没人接" || strings.Contains(got, "接口改造") {
		t.Errorf("隐藏预览时只写一句话: %q", got)
	}
}

// 默认不推；在表里打开之后每个事件只推一次，同一个事件重复交来不会再推。
func TestBoardStale_NotifierDefaultOffThenOn(t *testing.T) {
	off := newEventHarness(t, 10*time.Millisecond, nil)
	off.n.NotifyEvent(staleInfo("bst:off"))
	settle()
	if got := off.rec.count(); got != 0 {
		t.Fatalf("默认不推送，却发了 %d 条", got)
	}

	on := newEventHarness(t, 10*time.Millisecond, withOverrides(map[string]int{KindBoardStale: 3}))
	on.n.NotifyEvent(staleInfo("bst:1"))
	on.n.NotifyEvent(staleInfo("bst:1")) // 同一个事件重复交来
	on.n.NotifyEvent(staleInfo("bst:2"))
	waitFor(t, "两个不同的事件各推一条", func() bool { return on.rec.count() == 2 })
	settle()
	if got := on.rec.count(); got != 2 {
		t.Fatalf("同一个事件只该推一次: %d", got)
	}
	if b := on.rec.last().Body; b.Title != "指派久无人接" || b.Priority != 3 {
		t.Errorf("报文不对: %+v", b)
	}
}
