package mcp

import (
	"context"
	"strings"
	"testing"
)

// 任务描述回答「这件事到底是什么」：创建时写一次；列表 / 回执只给开头的预览，task_get 给全文；
// 只改描述不算进展（不占时间线）。
func TestTaskDescriptionOverMCP(t *testing.T) {
	srv, _ := setupWorkItemMCP(t)
	const wf = "wf-task-desc"
	call := func(args map[string]any) map[string]any {
		args["workflow_id"] = wf
		return callToolJSON(t, srv, "workflow_context", args)
	}
	board := func(args map[string]any) map[string]any {
		args["workflow_id"] = wf
		return callToolJSON(t, srv, "task_board", args)
	}

	long := strings.Repeat("说明", 100) // 200 个字，超过列表预览上限
	res := call(map[string]any{"action": "task_create", "title": "写说明", "description": long})
	task := taskOf(t, res)
	preview, _ := task["description"].(string)
	if !strings.HasSuffix(preview, "…") || len([]rune(preview)) != taskBoardDescMax+1 {
		t.Fatalf("回执里的描述应是截断的预览（%d 字 + …）: %d 字 %q", taskBoardDescMax, len([]rune(preview)), preview)
	}

	// task_get 给全文，task_list / task_board 给预览
	res = call(map[string]any{"action": "task_get", "task_id": 1})
	if taskOf(t, res)["description"] != long {
		t.Fatalf("task_get 应返回描述全文: %v", taskOf(t, res)["description"])
	}
	res = call(map[string]any{"action": "task_list"})
	tasks, _ := res["tasks"].([]any)
	if len(tasks) != 1 {
		t.Fatalf("task_list: %#v", res)
	}
	if d, _ := tasks[0].(map[string]any)["description"].(string); d != preview {
		t.Fatalf("task_list 应只给描述预览: %q", d)
	}
	res = board(map[string]any{"action": "list"})
	listed, _ := res["tasks"].([]any)
	if len(listed) != 1 {
		t.Fatalf("task_board list: %#v", res)
	}
	if d, _ := listed[0].(map[string]any)["description"].(string); d != preview {
		t.Fatalf("task_board list 应只给描述预览: %q", d)
	}

	// 只改描述：算更新，但不占时间线
	res = call(map[string]any{"action": "task_update", "task_id": 1, "description": "新的说明：做到打开会话只拉最后一屏"})
	if res["status"] != "updated" || taskOf(t, res)["description"] != "新的说明：做到打开会话只拉最后一屏" {
		t.Fatalf("改描述应返回 updated 且带回新描述: %#v", res)
	}
	res = call(map[string]any{"action": "task_get", "task_id": 1})
	if tl, _ := taskOf(t, res)["timeline"].([]any); len(tl) != 1 {
		t.Fatalf("只改描述不应新增时间线: %#v", taskOf(t, res)["timeline"])
	}

	// 传空串清除：回执里不再有 description
	res = call(map[string]any{"action": "task_update", "task_id": 1, "description": ""})
	if _, has := taskOf(t, res)["description"]; has || res["status"] != "updated" {
		t.Fatalf("空串应清除描述: %#v", res)
	}

	// 超长被拒，错误里点明字段与上限
	tooLong := strings.Repeat("字", 1001)
	_, err := srv.handleTaskUpdate(context.Background(), workflowContextArgs{WorkflowID: wf, TaskID: 1, Description: &tooLong})
	if err == nil || !strings.Contains(err.Error(), "描述最长 1000") {
		t.Fatalf("超长描述应给出明确的错误: %v", err)
	}

	// task_board batch：新建带描述，更新也能改描述
	res = board(map[string]any{"action": "batch", "ops": []map[string]any{
		{"title": "批量新建", "description": "批量创建时带的说明"},
	}})
	if sum, _ := res["summary"].(map[string]any); toInt(sum["created"]) != 1 {
		t.Fatalf("batch 应创建 1 个任务: %#v", res)
	}
	item, err := srv.store.GetWorkItemBySeq(context.Background(), wf, 2)
	if err != nil || item.Description != "批量创建时带的说明" {
		t.Fatalf("batch 新建应写入描述: err=%v item=%+v", err, item)
	}
	res = board(map[string]any{"action": "batch", "ops": []map[string]any{
		{"task_id": 2, "description": "批量更新后的说明"},
	}})
	if sum, _ := res["summary"].(map[string]any); toInt(sum["updated"]) != 1 {
		t.Fatalf("batch 应更新 1 个任务: %#v", res)
	}
	item, _ = srv.store.GetWorkItemBySeq(context.Background(), wf, 2)
	if item.Description != "批量更新后的说明" {
		t.Fatalf("batch 更新应写入描述: %q", item.Description)
	}
}
