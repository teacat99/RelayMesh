package mcp

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/teacat99/RelayMesh/v2/internal/model"
	"github.com/teacat99/RelayMesh/v2/internal/store"
)

// 群里的协作系统行（#180 B3c）经 MCP 入口的端到端：workflow_context 与 task_board 的派活 / 交付 / 退回都会在
// 派发者与承接者共同的群里留一条，批量合并成一条，并通知网页刷新那个群；不在同一个群的不说。

func TestBoardLinesPostedToSharedGroupOverMCP(t *testing.T) {
	srv := assignFixture(t)
	ctx := context.Background()
	g, err := srv.store.CreateGroup(ctx, store.CreateGroupInput{Title: "协作群", Members: []string{"wf-a", "wf-b"}})
	if err != nil {
		t.Fatal(err)
	}

	var mu sync.Mutex
	var notified []string
	srv.onUpdate = func(event string, data any) {
		if event != "conversation_changed" {
			return
		}
		if m, ok := data.(map[string]string); ok {
			mu.Lock()
			notified = append(notified, m["group_id"])
			mu.Unlock()
		}
	}
	lines := func() []string {
		rows, err := srv.store.StreamGroupMessages(ctx, g.ID, store.StreamRange{Dir: store.StreamOlder, Limit: 100}, nil, 0, false)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for i := len(rows) - 1; i >= 0; i-- {
			if rows[i].Kind == model.ChatKindSystem && strings.Contains(rows[i].Body, "{{m:") {
				out = append(out, rows[i].Body)
			}
		}
		return out
	}
	notifiedCount := func() int {
		mu.Lock()
		defer mu.Unlock()
		for _, id := range notified {
			if id != g.ID {
				t.Fatalf("通知的应是共同的群 %s，得到 %s", g.ID, id)
			}
		}
		return len(notified)
	}
	wantLines := func(step string, want ...string) {
		t.Helper()
		got := lines()
		if len(got) != len(want) {
			t.Fatalf("%s：想要 %d 条 %q，得到 %d 条 %q", step, len(want), want, len(got), got)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("%s：第 %d 条\n想要 %q\n得到 %q", step, i+1, want[i], got[i])
			}
		}
	}

	// 1. task_board 批量派 2 个任务给 wf-b（第 3 个自己做）：合成一条，通知 1 次。
	mustBoard(t, srv, "wf-a", map[string]any{"action": "batch", "ops": []any{
		map[string]any{"title": "甲", "description": "第一件", "assignee": "wf-b"},
		map[string]any{"title": "乙", "description": "第二件", "assignee": "wf-b"},
		map[string]any{"title": "丙", "description": "自己做"},
	}})
	one := "{{m:wf-a}} 一次派给 {{m:wf-b}} 2 个任务：#1 #2"
	wantLines("批量派活", one)
	if n := notifiedCount(); n != 1 {
		t.Fatalf("批量派活应只通知 1 次，得到 %d", n)
	}

	// 2. workflow_context 单个 task_create 带 assignee：再一条。
	wcDo(t, srv, "wf-a", map[string]any{"action": "task_create", "title": "丁", "assignee": "wf-b"})
	two := "{{m:wf-a}} 把 #4「丁」派给了 {{m:wf-b}}"
	wantLines("单个派活", one, two)

	// 3. 接取不说；交付说。
	wcDo(t, srv, "wf-b", map[string]any{"action": "task_update", "task_id": 1, "claim": "accept"})
	wantLines("接取之后", one, two)
	wcDo(t, srv, "wf-b", map[string]any{"action": "task_update", "task_id": 1, "claim": "deliver", "note": "写好了，自测通过"})
	three := "{{m:wf-b}} 交付了 #1「甲」，等 {{m:wf-a}} 验收"
	wantLines("交付", one, two, three)

	// 4. 同一个交付再来一次（已在验证中）：不重复说。
	wcDo(t, srv, "wf-b", map[string]any{"action": "task_update", "task_id": 1, "claim": "deliver", "note": "补充说明"})
	wantLines("重复交付", one, two, three)

	// 5. 派发者经 task_board 退回：说。
	mustBoard(t, srv, "wf-a", map[string]any{"action": "batch", "ops": []any{map[string]any{"task_id": 1, "claim": "reject_delivery", "note": "漏了边界用例"}}})
	four := "{{m:wf-a}} 把 #1「甲」退回给 {{m:wf-b}} 返工"
	wantLines("退回", one, two, three, four)
	if n := notifiedCount(); n != 4 {
		t.Fatalf("四次写入群里的动作应通知 4 次，得到 %d", n)
	}

	// 6. 派给不在这个群里的 wf-c：没有共同的群，不说也不通知。
	wcDo(t, srv, "wf-a", map[string]any{"action": "task_create", "title": "戊", "assignee": "wf-c"})
	wantLines("派给群外的人", one, two, three, four)
	if n := notifiedCount(); n != 4 {
		t.Fatalf("群外的派活不该通知，得到 %d 次", n)
	}
}
