package mcp

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/teacat99/RelayMesh/v2/internal/store"
)

// 同一个项目的工作流共用一张任务板（#175 B1a）：智能体用 task_board 看到的是同一份清单、同一棵规划树、同一套 #N。
func TestTaskBoardSharedByWorkflowsOfOneProject(t *testing.T) {
	srv, events := setupWorkItemMCP(t)
	ctx := context.Background()
	p, err := srv.store.CreateProject(ctx, store.ProjectInput{Name: "A", Slug: "a"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := srv.store.AddProjectDir(ctx, p.ID, "h", "/code/a", ""); err != nil {
		t.Fatal(err)
	}
	for wf, dir := range map[string]string{"wf-a": "/code/a", "wf-b": "/code/a", "wf-out": "/code/x"} {
		if _, err := srv.store.CreateFeedbackSession(ctx, store.CreateSessionInput{SessionID: "s-" + wf, WorkflowID: wf, EnvHostName: "h", ProjectDirectory: dir, Title: "t", Summary: "s"}); err != nil {
			t.Fatal(err)
		}
	}
	board := fmt.Sprintf("p%d", p.ID)

	// wf-a 规划并建 #1。
	res := mustBoard(t, srv, "wf-a", map[string]any{"action": "batch", "modules": samplePlan, "ops": []any{
		map[string]any{"title": "甲", "module": "终端/历史日志"},
	}})
	if res["status"] != "ok" {
		t.Fatalf("wf-a 建任务: %#v", res)
	}

	// wf-b 看到同一份清单；建 #2（规划树共用，功能名全树唯一时可省略模块）；同标题的任务去重在板内生效。
	if got := taskTitles(mustBoard(t, srv, "wf-b", map[string]any{"action": "list"})); len(got) != 1 || got[0] != "甲" {
		t.Fatalf("wf-b 应看到 wf-a 的任务: %v", got)
	}
	res = mustBoard(t, srv, "wf-b", map[string]any{"action": "batch", "ops": []any{
		map[string]any{"title": "乙", "module": "历史日志"},
		map[string]any{"title": "甲", "module": "终端/历史日志"}, // 同标题同模块 = 重复
	}})
	rs := resultsOf(res)
	if len(rs) != 2 || rs[0]["action"] != "created" || rs[0]["ref"] != "#2" || rs[1]["action"] != "exists" || rs[1]["ref"] != "#1" {
		t.Fatalf("wf-b 应建出 #2，并识别出 wf-a 已有「甲」: %#v", rs)
	}

	// wf-b 按 #1 更新 wf-a 建的任务（序号在板内唯一）。
	res = mustBoard(t, srv, "wf-b", map[string]any{"action": "batch", "ops": []any{map[string]any{"task_id": 1, "state": "doing"}}})
	if rs = resultsOf(res); len(rs) != 1 || rs[0]["ok"] != true || rs[0]["state"] != "doing" {
		t.Fatalf("wf-b 应能更新 #1: %#v", res)
	}

	// 概览的计数是整张板的；项目外的工作流是独立板，看不到。
	ov := mustBoard(t, srv, "wf-a", map[string]any{})
	if c := asMap(ov["counts"]); toInt(c["total"]) != 2 || toInt(c["open"]) != 2 {
		t.Fatalf("概览应是整张板的 2 条: %#v", ov["counts"])
	}
	list := mustBoard(t, srv, "wf-b", map[string]any{"action": "list"})
	if c := asMap(list["counts_by_state"]); toInt(c["doing"]) != 1 || toInt(c["todo"]) != 1 || toInt(list["total"]) != 2 {
		t.Fatalf("清单与计数同口径（整张板）: %#v", list)
	}
	if got := taskTitles(mustBoard(t, srv, "wf-out", map[string]any{"action": "list"})); len(got) != 0 {
		t.Fatalf("项目外的工作流不应看到项目板: %v", got)
	}

	// 事件带 board_key，前端据此判断是不是自己正在看的那张板。
	var itemEvents int
	for _, e := range *events {
		m, _ := e.Data.(map[string]any)
		if e.Name == "work_item_updated" || e.Name == "work_module_updated" {
			if m["board_key"] != board {
				t.Fatalf("事件 %s 缺少 board_key=%s: %#v", e.Name, board, m)
			}
			itemEvents++
		}
	}
	if itemEvents == 0 || !strings.HasPrefix(board, "p") {
		t.Fatalf("应收到带 board_key 的任务事件: %d", itemEvents)
	}
}
