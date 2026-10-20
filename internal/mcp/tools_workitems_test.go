package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/teacat99/RelayMesh/v2/internal/config"
	"github.com/teacat99/RelayMesh/v2/internal/model"
	"github.com/teacat99/RelayMesh/v2/internal/store"
)

type workEvent struct {
	Name string
	Data any
}

func setupWorkItemMCP(t *testing.T) (*Server, *[]workEvent) {
	t.Helper()
	st, err := store.New(":memory:")
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	var mu sync.Mutex
	events := &[]workEvent{}
	srv := NewServer(&config.Config{ProjectID: "test-proj", FeedbackTimeoutSeconds: 1}, st, func(name string, data any) {
		mu.Lock()
		defer mu.Unlock()
		*events = append(*events, workEvent{name, data})
	})
	return srv, events
}

func taskOf(t *testing.T, res map[string]any) map[string]any {
	t.Helper()
	task, ok := res["task"].(map[string]any)
	if !ok {
		t.Fatalf("回执里没有 task: %#v", res)
	}
	return task
}

func toInt(v any) int {
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	}
	return -1
}

func TestWorkflowContextTaskLifecycleOverMCP(t *testing.T) {
	srv, events := setupWorkItemMCP(t)
	const wf = "wf-task-lifecycle"
	call := func(args map[string]any) map[string]any {
		args["workflow_id"] = wf
		return callToolJSON(t, srv, "workflow_context", args)
	}

	// 创建
	res := call(map[string]any{"action": "task_create", "title": "修复终端闪烁", "kind": "缺陷", "labels": []string{"终端"}, "progress": "已复现", "refs": []string{"commit:abc1234"}})
	task := taskOf(t, res)
	if res["status"] != "created" || toInt(task["task_id"]) != 1 || task["ref"] != "#1" || task["kind"] != "bug" || task["state"] != "todo" || task["progress"] != "已复现" || task["created_by"] != "agent" {
		t.Fatalf("创建回执不符: %#v", res)
	}
	if _, leaked := task["id"]; leaked {
		t.Fatalf("内部主键不应外露给智能体（用 task_id 引用）: %#v", task)
	}
	if _, has := res["session_linked"]; has {
		t.Fatalf("没传 session_id 时回执里不应有 session_linked: %#v", res)
	}

	// 序号按工作流递增
	res = call(map[string]any{"action": "task_create", "title": "第二件事"})
	if toInt(taskOf(t, res)["task_id"]) != 2 {
		t.Fatalf("第二个任务应是 #2: %#v", res)
	}

	// 更新：进度覆盖、状态流转
	res = call(map[string]any{"action": "task_update", "task_id": 1, "state": "doing", "progress": "定位到 resize 竞态", "note": "决定先改 resize 防抖"})
	task = taskOf(t, res)
	if res["status"] != "updated" || task["state"] != "doing" || task["progress"] != "定位到 resize 竞态" {
		t.Fatalf("更新回执不符: %#v", res)
	}

	// 没有变化：unchanged，且不广播
	n := len(*events)
	res = call(map[string]any{"action": "task_update", "task_id": 1, "state": "doing", "progress": "定位到 resize 竞态"})
	if res["status"] != "unchanged" {
		t.Fatalf("无变化的更新应返回 unchanged: %#v", res)
	}
	if len(*events) != n {
		t.Fatalf("无变化的更新不应广播事件")
	}

	// 没写结论就想关闭：被拒绝并说清怎么写，任务状态不变
	if _, err := srv.handleTaskUpdate(context.Background(), workflowContextArgs{WorkflowID: wf, TaskID: 1, State: "done"}); err == nil || !strings.Contains(err.Error(), "要同时写结论") {
		t.Fatalf("没有结论的「已完成」应被拒绝并说清怎么写: %v", err)
	}

	// 状态绑定开发流程（#110）：先验证中；验证通过后结论与状态放在同一次调用里，直接置已完成，不再被改写成待确认
	res = call(map[string]any{"action": "task_update", "task_id": 1, "state": "review", "progress": "回归与 e2e 跑完，等上线冒烟"})
	if taskOf(t, res)["state"] != "review" {
		t.Fatalf("应进入验证中: %#v", res)
	}
	res = call(map[string]any{"action": "task_update", "task_id": 1, "state": "done", "conclusion": "改了 resize 防抖，已验证"})
	task = taskOf(t, res)
	if task["state"] != "done" || task["conclusion"] != "改了 resize 防抖，已验证" {
		t.Fatalf("验证通过后智能体应能直接置为已完成: %#v", res)
	}
	if _, has := res["state_note"]; has {
		t.Fatalf("状态不再被改写，回执里不应有 state_note: %#v", res)
	}

	// 已完成之后智能体仍可补充备注
	res = call(map[string]any{"action": "task_update", "task_id": 1, "note": "补记：回归用例已加"})
	if res["status"] != "updated" || taskOf(t, res)["state"] != "done" {
		t.Fatalf("已完成的任务仍可补充备注: %#v", res)
	}

	// 详情：时间线（新的在前）与会话
	res = call(map[string]any{"action": "task_get", "task_id": 1})
	task = taskOf(t, res)
	timeline, _ := task["timeline"].([]any)
	if len(timeline) < 4 {
		t.Fatalf("时间线应包含创建 / 流转 / 备注: %#v", task["timeline"])
	}
	first, _ := timeline[0].(map[string]any)
	if first["text"] != "补记：回归用例已加" {
		t.Fatalf("时间线应新的在前: %#v", first)
	}
	if _, ok := task["sessions"].([]any); !ok {
		t.Fatalf("详情应包含 sessions 数组: %#v", task)
	}

	// 事件：创建 / 更新都广播 work_item_updated
	var got int
	for _, e := range *events {
		if e.Name == "work_item_updated" {
			got++
		}
	}
	if got < 5 {
		t.Fatalf("写入应广播 work_item_updated（至少 5 次），实际 %d", got)
	}
}

func TestWorkflowContextTaskListDefaultsAndFilters(t *testing.T) {
	srv, _ := setupWorkItemMCP(t)
	const wf = "wf-task-list"
	call := func(args map[string]any) map[string]any {
		args["workflow_id"] = wf
		return callToolJSON(t, srv, "workflow_context", args)
	}
	mk := func(title, state, kind string) {
		args := map[string]any{"action": "task_create", "title": title}
		if state != "" {
			args["state"] = state
		}
		if kind != "" {
			args["kind"] = kind
		}
		call(args)
	}
	mk("待办一", "todo", "bug")
	mk("进行中一", "doing", "feature")
	mk("验证中一", "review", "")
	mk("搁置一", "shelved", "ops")
	mk("待办二", "todo", "research")
	// 另一个工作流的任务不应混入
	callToolJSON(t, srv, "workflow_context", map[string]any{"action": "task_create", "workflow_id": "other", "title": "别处的任务"})

	titles := func(res map[string]any) []string {
		var out []string
		for _, v := range res["tasks"].([]any) {
			out = append(out, v.(map[string]any)["title"].(string))
		}
		return out
	}
	res := call(map[string]any{"action": "task_list"})
	got := titles(res)
	want := []string{"进行中一", "验证中一", "待办二", "待办一"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("默认只列未关闭任务，按 进行中 → 验证中 → 待办 排序（同状态新的在前）:\n got=%v\nwant=%v", got, want)
	}
	if toInt(res["count"]) != 4 || toInt(res["total"]) != 4 {
		t.Fatalf("count / total: %#v", res)
	}
	counts := res["counts_by_state"].(map[string]any)
	if toInt(counts["todo"]) != 2 || toInt(counts["doing"]) != 1 || toInt(counts["shelved"]) != 1 || toInt(counts["done"]) != 0 {
		t.Fatalf("counts_by_state: %#v", counts)
	}
	if got := titles(call(map[string]any{"action": "task_list", "include_closed": true})); len(got) != 5 || got[len(got)-1] != "搁置一" {
		t.Fatalf("include_closed 应带上已搁置的任务并排在最后: %v", got)
	}
	if got := titles(call(map[string]any{"action": "task_list", "states": []string{"shelved"}})); len(got) != 1 || got[0] != "搁置一" {
		t.Fatalf("states 筛选: %v", got)
	}
	if got := titles(call(map[string]any{"action": "task_list", "kind": "bug"})); len(got) != 1 || got[0] != "待办一" {
		t.Fatalf("kind 筛选: %v", got)
	}
	if got := titles(call(map[string]any{"action": "task_list", "query": "进行"})); len(got) != 1 || got[0] != "进行中一" {
		t.Fatalf("query 筛选: %v", got)
	}
	res = call(map[string]any{"action": "task_list", "limit": 2})
	if toInt(res["count"]) != 2 || toInt(res["total"]) != 4 || res["truncated"] != true || !strings.Contains(fmt.Sprint(res["hint"]), "缩小范围") {
		t.Fatalf("超过 limit 应截断并给出提示: %#v", res)
	}
}

func TestWorkflowContextTaskParentAndSession(t *testing.T) {
	srv, _ := setupWorkItemMCP(t)
	ctx := context.Background()
	const wf = "wf-task-parent"
	if _, err := srv.store.CreateFeedbackSession(ctx, store.CreateSessionInput{SessionID: "sess-real", WorkflowID: wf, Title: "真实会话", Summary: "s"}); err != nil {
		t.Fatal(err)
	}
	call := func(args map[string]any) map[string]any {
		args["workflow_id"] = wf
		return callToolJSON(t, srv, "workflow_context", args)
	}
	batch := taskOf(t, call(map[string]any{"action": "task_create", "title": "一批问题", "session_id": "sess-real"}))
	child := call(map[string]any{"action": "task_create", "title": "其中一个", "parent_task_id": toInt(batch["task_id"]), "session_id": "sess-real"})
	if child["session_linked"] != true {
		t.Fatalf("存在的会话应被关联: %#v", child)
	}
	if toInt(taskOf(t, child)["parent_task_id"]) != 1 {
		t.Fatalf("子任务应带 parent_task_id: %#v", child)
	}
	ghost := call(map[string]any{"action": "task_create", "title": "不存在的会话", "session_id": "sess-ghost"})
	if ghost["session_linked"] != false {
		t.Fatalf("不存在的会话不应被关联，且回执要说明: %#v", ghost)
	}
	parent := taskOf(t, call(map[string]any{"action": "task_get", "task_id": 1}))
	if ch, _ := parent["children"].(map[string]any); toInt(ch["total"]) != 1 {
		t.Fatalf("批次应汇总子任务: %#v", parent)
	}
	sess, _ := parent["sessions"].([]any)
	if len(sess) != 1 || sess[0].(map[string]any)["title"] != "真实会话" {
		t.Fatalf("详情应带关联会话: %#v", parent["sessions"])
	}
	// 解除父任务：parent_task_id=0
	res := call(map[string]any{"action": "task_update", "task_id": 2, "parent_task_id": 0})
	if _, has := taskOf(t, res)["parent_task_id"]; has {
		t.Fatalf("parent_task_id=0 应解除父任务: %#v", res)
	}
	// 父任务不存在
	if _, err := srv.handleTaskCreate(ctx, workflowContextArgs{WorkflowID: wf, Title: "x", ParentTaskID: intPtr(99)}); err == nil || !strings.Contains(err.Error(), "#99") {
		t.Fatalf("不存在的父任务应报错并带序号: %v", err)
	}
}

func intPtr(n int) *int { return &n }

func TestWorkflowContextTaskValidationMessages(t *testing.T) {
	srv, _ := setupWorkItemMCP(t)
	ctx := context.Background()
	cases := []struct {
		name string
		run  func() error
		want string
	}{
		{"create 缺 workflow", func() error { _, err := srv.handleTaskCreate(ctx, workflowContextArgs{Title: "x"}); return err }, "workflow_id is required"},
		{"create 缺 title", func() error { _, err := srv.handleTaskCreate(ctx, workflowContextArgs{WorkflowID: "w"}); return err }, "title is required"},
		{"create 未知类型", func() error {
			_, err := srv.handleTaskCreate(ctx, workflowContextArgs{WorkflowID: "w", Title: "x", Kind: "nope"})
			return err
		}, "bug / feature"},
		{"create 进度超长", func() error {
			p := strings.Repeat("长", 501)
			_, err := srv.handleTaskCreate(ctx, workflowContextArgs{WorkflowID: "w", Title: "x", Progress: &p})
			return err
		}, "最长 500"},
		{"update 缺 task_id", func() error { _, err := srv.handleTaskUpdate(ctx, workflowContextArgs{WorkflowID: "w"}); return err }, "task_id is required"},
		{"update 任务不存在", func() error {
			_, err := srv.handleTaskUpdate(ctx, workflowContextArgs{WorkflowID: "w", TaskID: 5})
			return err
		}, "#5"},
		{"get 缺 task_id", func() error { _, err := srv.handleTaskGet(ctx, workflowContextArgs{WorkflowID: "w"}); return err }, "task_id is required"},
		{"list 缺 workflow", func() error { _, err := srv.handleTaskList(ctx, workflowContextArgs{}); return err }, "workflow_id is required"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := c.run()
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("期望错误包含 %q，实际 %v", c.want, err)
			}
		})
	}
	// 业务错误要带上动作名，方便智能体判断是哪次调用出的问题
	_, err := srv.handleTaskCreate(ctx, workflowContextArgs{WorkflowID: "w", Title: "x", Kind: "nope"})
	if err == nil || !strings.HasPrefix(err.Error(), "task_create: ") {
		t.Fatalf("业务错误应以动作名开头: %v", err)
	}
}

// 调度表与工具声明保持同步：声明里列出的每个 action 都能被调度（不会落到 unknown action），反之亦然。
func TestWorkflowContextSchemaMatchesDispatcher(t *testing.T) {
	srv, _ := setupWorkItemMCP(t)
	var schema struct {
		Properties map[string]struct {
			Enum []string `json:"enum"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(workflowContextSchema, &schema); err != nil {
		t.Fatal(err)
	}
	actions := schema.Properties["action"].Enum
	for _, a := range []string{"task_create", "task_update", "task_get", "task_list"} {
		found := false
		for _, e := range actions {
			found = found || e == a
		}
		if !found {
			t.Errorf("工具声明的 action 枚举里缺少 %s", a)
		}
	}
	for _, a := range actions {
		raw, _ := json.Marshal(map[string]any{"action": a})
		if _, err := srv.handleWorkflowContext(context.Background(), raw); err != nil && strings.Contains(err.Error(), "unknown action") {
			t.Errorf("声明了 action %q，但调度表不认识它", a)
		}
	}
	if _, err := srv.handleWorkflowContext(context.Background(), json.RawMessage(`{"action":"task_nope"}`)); err == nil || !strings.Contains(err.Error(), "unknown action") {
		t.Errorf("未知 action 应报错: %v", err)
	}
	for _, p := range []string{"task_id", "title", "kind", "state", "states", "labels", "refs", "progress", "conclusion", "note", "parent_task_id", "query", "include_closed"} {
		if _, ok := schema.Properties[p]; !ok {
			t.Errorf("工具声明缺少参数 %s", p)
		}
	}
	defs := GetToolDefinitionsForPermissions(model.AllPermissions())
	var desc string
	for _, d := range defs {
		if d.Name == "workflow_context" {
			desc = d.Description
		}
	}
	for _, want := range []string{"task_create", "task_update", "task_get", "task_list", "bound to your own development flow"} {
		if !strings.Contains(desc, want) {
			t.Errorf("workflow_context 的描述里应说明 %q", want)
		}
	}
}
