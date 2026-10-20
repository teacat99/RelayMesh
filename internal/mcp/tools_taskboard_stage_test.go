package mcp

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

// task_board batch：创建时带阶段、成批套同一个阶段、写错只让这一条失败，回执与列表里都能看到阶段。
func TestTaskBoardBatchStage(t *testing.T) {
	srv, _ := setupWorkItemMCP(t)
	const wf = "wf-stage-batch"

	res := mustBoard(t, srv, wf, map[string]any{"action": "batch", "ops": []any{
		map[string]any{"title": "评估一下", "stage": "assess", "kind": "research"},
		map[string]any{"title": "写个方案", "stage": "方案"},
		map[string]any{"title": "直接开发", "stage": "dev", "state": "doing"},
		map[string]any{"title": "没标阶段"},
		map[string]any{"title": "阶段写错", "stage": "上线"},
	}})
	rs := resultsOf(res)
	if res["status"] != "partial" || len(rs) != 5 {
		t.Fatalf("应是 partial（4 成功 1 失败）: %#v", res)
	}
	if rs[0]["stage"] != "assess" || rs[1]["stage"] != "plan" || rs[2]["stage"] != "dev" {
		t.Fatalf("创建回执应带规整后的阶段: %#v", rs[:3])
	}
	if _, has := rs[3]["stage"]; has {
		t.Fatalf("没标阶段的任务回执里不应有 stage: %#v", rs[3])
	}
	if rs[4]["ok"] != false || !strings.Contains(fmt.Sprint(rs[4]["error"]), "未知的阶段") {
		t.Fatalf("写错的阶段应单独报错并列出可用值: %#v", rs[4])
	}

	// 成批更新：task_ids 套同一个阶段；单个任务传 "" 清除
	res = mustBoard(t, srv, wf, map[string]any{"action": "batch", "ops": []any{
		map[string]any{"task_ids": []int{1, 2}, "stage": "improve"},
		map[string]any{"task_id": 3, "stage": ""},
	}})
	rs = resultsOf(res)
	if res["status"] != "ok" || rs[0]["stage"] != "improve" || rs[1]["stage"] != "improve" {
		t.Fatalf("task_ids 应逐个带上新阶段: %#v", res)
	}
	if _, has := rs[2]["stage"]; has {
		t.Fatalf("传 \"\" 应清除阶段（回执里不再有 stage）: %#v", rs[2])
	}
	// 没带 stage 的更新不动阶段
	res = mustBoard(t, srv, wf, map[string]any{"action": "batch", "ops": []any{map[string]any{"task_id": 1, "progress": "继续"}}})
	if rs = resultsOf(res); rs[0]["stage"] != "improve" {
		t.Fatalf("不传 stage 不应改动阶段: %#v", rs[0])
	}

	// list / overview 里的任务带 stage
	list := mustBoard(t, srv, wf, map[string]any{"action": "list", "include_closed": true})
	stages := map[string]string{}
	for _, v := range asList(list["tasks"]) {
		m := asMap(v)
		s, _ := m["stage"].(string)
		stages[fmt.Sprint(m["title"])] = s
	}
	if stages["评估一下"] != "improve" || stages["写个方案"] != "improve" || stages["直接开发"] != "" || stages["没标阶段"] != "" {
		t.Fatalf("list 应带每个任务的阶段（未标注则没有 stage）: %v", stages)
	}
	// 落库的是规整后的 id
	got, err := srv.store.GetWorkItemBySeq(context.Background(), wf, 2)
	if err != nil || got.Stage != "improve" {
		t.Fatalf("落库阶段: %+v %v", got, err)
	}
}

// workflow_context 的 task_create / task_update / task_get 同样支持 stage。
func TestWorkflowContextTaskStage(t *testing.T) {
	srv, _ := setupWorkItemMCP(t)
	const wf = "wf-stage-compat"
	call := func(args map[string]any) map[string]any {
		args["workflow_id"] = wf
		return callToolJSON(t, srv, "workflow_context", args)
	}
	ctx := context.Background()

	task := taskOf(t, call(map[string]any{"action": "task_create", "title": "带阶段创建", "stage": "评估"}))
	if task["stage"] != "assess" {
		t.Fatalf("task_create 应带 stage: %#v", task)
	}
	task = taskOf(t, call(map[string]any{"action": "task_create", "title": "不带阶段"}))
	if _, has := task["stage"]; has {
		t.Fatalf("没标阶段不应输出 stage: %#v", task)
	}
	task = taskOf(t, call(map[string]any{"action": "task_update", "task_id": 2, "stage": "dev"}))
	if task["stage"] != "dev" {
		t.Fatalf("task_update 改阶段: %#v", task)
	}
	task = taskOf(t, call(map[string]any{"action": "task_update", "task_id": 2, "stage": ""}))
	if _, has := task["stage"]; has {
		t.Fatalf("stage=\"\" 应清除: %#v", task)
	}
	// 不传 stage 的更新不动它
	call(map[string]any{"action": "task_update", "task_id": 1, "progress": "推进中"})
	if got := taskOf(t, call(map[string]any{"action": "task_get", "task_id": 1})); got["stage"] != "assess" {
		t.Fatalf("task_get 详情应带 stage，且不传时不被清除: %#v", got)
	}
	// 写错：错误里带可用值
	_, err := srv.handleTaskUpdate(ctx, workflowContextArgs{WorkflowID: wf, TaskID: 1, Stage: strPtr("上线")})
	if err == nil || !strings.Contains(err.Error(), "未知的阶段") || !strings.Contains(err.Error(), "improve") {
		t.Fatalf("写错的阶段应报错并列出可用值: %v", err)
	}
	_, err = srv.handleTaskCreate(ctx, workflowContextArgs{WorkflowID: wf, Title: "坏", Stage: strPtr("上线")})
	if err == nil || !strings.HasPrefix(err.Error(), "task_create: ") || !strings.Contains(err.Error(), "未知的阶段") {
		t.Fatalf("task_create 写错阶段: %v", err)
	}
}
