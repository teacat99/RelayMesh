package mcp

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/teacat99/RelayMesh/v2/internal/store"
)

// 共享任务板上的协作（#175 B2a）：指派 / 接取 / 拒绝 / 交付 / 验收 / 退回，经 workflow_context 与 task_board 两个入口走通。
// B2a 的智能体不受角色限制（权限放行在 B2c），这里验证的是协作协议本身与回执形状。

// assignFixture 建一个项目 A（目录 /code/a）与三个在其中的工作流 wf-a / wf-b / wf-c，另有项目外的 wf-out。
func assignFixture(t *testing.T) *Server {
	t.Helper()
	srv, _ := setupWorkItemMCP(t)
	ctx := context.Background()
	p, err := srv.store.CreateProject(ctx, store.ProjectInput{Name: "A", Slug: "a"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := srv.store.AddProjectDir(ctx, p.ID, "h", "/code/a", ""); err != nil {
		t.Fatal(err)
	}
	for wf, dir := range map[string]string{"wf-a": "/code/a", "wf-b": "/code/a", "wf-c": "/code/a", "wf-out": "/code/x"} {
		if _, err := srv.store.CreateFeedbackSession(ctx, store.CreateSessionInput{SessionID: "s-" + wf, WorkflowID: wf, EnvHostName: "h", ProjectDirectory: dir, Title: "t", Summary: "s"}); err != nil {
			t.Fatal(err)
		}
	}
	return srv
}

// wcDo 以 wf 的身份调用 workflow_context，返回 JSON 结果。
func wcDo(t *testing.T, srv *Server, wf string, args map[string]any) map[string]any {
	t.Helper()
	args["workflow_id"] = wf
	return callToolJSON(t, srv, "workflow_context", args)
}

// wcFail 期望调用失败，返回错误文本。
func wcFail(t *testing.T, srv *Server, wf string, args map[string]any) string {
	t.Helper()
	args["workflow_id"] = wf
	text, isErr := callToolRaw(t, srv, "workflow_context", args)
	if !isErr {
		t.Fatalf("期望失败但成功了: %v → %s", args, text)
	}
	return text
}

func hasKey(m map[string]any, k string) bool {
	_, ok := m[k]
	return ok
}

func TestTaskAssignFullFlowOverMCP(t *testing.T) {
	srv := assignFixture(t)

	// wf-a 建任务并直接指派给 wf-b。
	res := wcDo(t, srv, "wf-a", map[string]any{"action": "task_create", "title": "写迁移脚本", "description": "把旧表迁到新表，验证行数一致", "assignee": "wf-b"})
	task := taskOf(t, res)
	if task["assignee"] != "wf-b" || task["claim"] != "offered" || task["assigned_by"] != "wf-a" || task["state"] != "todo" {
		t.Fatalf("指派回执不符: %#v", task)
	}
	if hasKey(task, "workflow") {
		t.Fatalf("创建者看自己的任务不应带 workflow: %#v", task)
	}

	// 承接者概览：指派给你 + 待接取提示；创建者看到「你派出的」。
	ov := mustBoard(t, srv, "wf-b", map[string]any{})
	toMe := asList(ov["assigned_to_you"])
	if len(toMe) != 1 || asMap(toMe[0])["claim"] != "offered" || asMap(toMe[0])["workflow"] != "wf-a" {
		t.Fatalf("wf-b 概览应列出待接取的指派（带创建者）: %#v", ov["assigned_to_you"])
	}
	if h, _ := ov["assigned_to_you_hint"].(string); !strings.Contains(h, "accept") || !strings.Contains(h, "decline") {
		t.Fatalf("应提示怎么接取 / 拒绝: %#v", ov["assigned_to_you_hint"])
	}
	ovA := mustBoard(t, srv, "wf-a", map[string]any{})
	if s := asMap(ovA["assigned_by_you"]); toInt(s["offered"]) != 1 || toInt(s["awaiting_acceptance"]) != 0 {
		t.Fatalf("wf-a 概览的派出汇总不符: %#v", ovA["assigned_by_you"])
	}
	if hasKey(ovA, "assigned_to_you") || hasKey(ovA, "awaiting_your_acceptance") {
		t.Fatalf("派发者自己没有被指派的任务，也还没有待验收: %#v", ovA)
	}

	// 接取：todo → doing，claim = accepted。
	res = wcDo(t, srv, "wf-b", map[string]any{"action": "task_update", "task_id": 1, "claim": "accept"})
	task = taskOf(t, res)
	if task["state"] != "doing" || task["claim"] != "accepted" {
		t.Fatalf("接取后应是 doing / accepted: %#v", task)
	}
	if ovB := mustBoard(t, srv, "wf-b", map[string]any{}); hasKey(ovB, "assigned_to_you_hint") {
		t.Fatalf("已接取就不再提示接取: %#v", ovB["assigned_to_you_hint"])
	}

	// 交付没有说明会被拒；有说明则进 review。
	if msg := wcFail(t, srv, "wf-b", map[string]any{"action": "task_update", "task_id": 1, "claim": "deliver"}); !strings.Contains(msg, "交付") {
		t.Fatalf("交付没写说明应被拒: %s", msg)
	}
	res = wcDo(t, srv, "wf-b", map[string]any{"action": "task_update", "task_id": 1, "claim": "deliver", "note": "脚本已写好，行数核对一致（12 万行）"})
	if task = taskOf(t, res); task["state"] != "review" || task["claim"] != "accepted" {
		t.Fatalf("交付后应是 review: %#v", task)
	}

	// 派发者概览：等你验收。
	ovA = mustBoard(t, srv, "wf-a", map[string]any{})
	wait := asList(ovA["awaiting_your_acceptance"])
	if len(wait) != 1 || asMap(wait[0])["assignee"] != "wf-b" {
		t.Fatalf("wf-a 应看到待验收的任务: %#v", ovA)
	}
	if s := asMap(ovA["assigned_by_you"]); toInt(s["accepted"]) != 1 || toInt(s["awaiting_acceptance"]) != 1 {
		t.Fatalf("派出汇总应是 accepted 1 / 待验收 1: %#v", s)
	}
	if h, _ := ovA["awaiting_your_acceptance_hint"].(string); !strings.Contains(h, "accept_delivery") || !strings.Contains(h, "reject_delivery") {
		t.Fatalf("应提示验收 / 退回的用法: %#v", ovA["awaiting_your_acceptance_hint"])
	}

	// 先退回再返工，再次交付，最后验收。
	if msg := wcFail(t, srv, "wf-a", map[string]any{"action": "task_update", "task_id": 1, "claim": "reject_delivery"}); !strings.Contains(msg, "note") {
		t.Fatalf("退回没写原因应被拒: %s", msg)
	}
	res = wcDo(t, srv, "wf-a", map[string]any{"action": "task_update", "task_id": 1, "claim": "reject_delivery", "note": "缺回滚脚本"})
	if task = taskOf(t, res); task["state"] != "doing" {
		t.Fatalf("退回后应是 doing: %#v", task)
	}
	wcDo(t, srv, "wf-b", map[string]any{"action": "task_update", "task_id": 1, "claim": "deliver", "progress": "补了回滚脚本并演练通过"})
	if msg := wcFail(t, srv, "wf-a", map[string]any{"action": "task_update", "task_id": 1, "claim": "accept_delivery"}); !strings.Contains(msg, "结论") {
		t.Fatalf("验收完成要结论: %s", msg)
	}
	res = wcDo(t, srv, "wf-a", map[string]any{"action": "task_update", "task_id": 1, "claim": "accept_delivery", "conclusion": "迁移脚本与回滚脚本均演练通过"})
	if task = taskOf(t, res); task["state"] != "done" {
		t.Fatalf("验收后应是 done: %#v", task)
	}

	// 完成后不再出现在「指派给你 / 等你验收」里。
	for _, wf := range []string{"wf-a", "wf-b"} {
		ov = mustBoard(t, srv, wf, map[string]any{})
		if hasKey(ov, "assigned_to_you") || hasKey(ov, "awaiting_your_acceptance") || hasKey(ov, "assigned_by_you") {
			t.Fatalf("%s: 已完成的指派不应再出现在概览的协作区: %#v", wf, ov)
		}
	}

	// task_get 的时间线带事件与操作者的工作流。
	got := wcDo(t, srv, "wf-c", map[string]any{"action": "task_get", "task_id": 1})
	gt := taskOf(t, got)
	events := map[string]string{}
	for _, v := range asList(gt["timeline"]) {
		e := asMap(v)
		if ev, _ := e["event"].(string); ev != "" {
			events[ev], _ = e["workflow"].(string)
		}
	}
	for ev, by := range map[string]string{"assign": "wf-a", "accept": "wf-b", "deliver": "wf-b", "reject_delivery": "wf-a", "accept_delivery": "wf-a"} {
		if events[ev] != by {
			t.Fatalf("时间线应有 %s 事件且由 %s 完成: %#v", ev, by, events)
		}
	}
	if gt["workflow"] != "wf-a" {
		t.Fatalf("wf-c 看别人建的任务应带创建者: %#v", gt)
	}
	if !hasKey(gt, "assigned_at") || !hasKey(gt, "accepted_at") {
		t.Fatalf("task_get 应带指派 / 接取时间: %#v", gt)
	}
}

func TestTaskAssignDeclineAndReofferViaBatch(t *testing.T) {
	srv := assignFixture(t)

	// 一次派 2 个任务（派活至少 2 个是对智能体的提示，接口本身不限制个数）。
	res := mustBoard(t, srv, "wf-a", map[string]any{"action": "batch", "ops": []any{
		map[string]any{"title": "甲", "description": "第一件", "assignee": "wf-b"},
		map[string]any{"title": "乙", "description": "第二件", "assignee": "wf-b"},
		map[string]any{"title": "丙", "description": "自己做"},
	}})
	rs := resultsOf(res)
	if len(rs) != 3 || rs[0]["assignee"] != "wf-b" || rs[0]["claim"] != "offered" || rs[1]["assignee"] != "wf-b" || hasKey(rs[2], "assignee") {
		t.Fatalf("批量指派回执不符: %#v", rs)
	}

	// 承接者按 assignee=me 过滤；派发者按 none 过滤只剩没指派的。
	mine := mustBoard(t, srv, "wf-b", map[string]any{"action": "list", "assignee": "me"})
	if got := taskTitles(mine); len(got) != 2 {
		t.Fatalf("assignee=me 应得 2 条: %v", got)
	}
	none := mustBoard(t, srv, "wf-a", map[string]any{"action": "list", "assignee": "none"})
	if got := taskTitles(none); len(got) != 1 || got[0] != "丙" {
		t.Fatalf("assignee=none 应只剩「丙」: %v", got)
	}
	byID := mustBoard(t, srv, "wf-a", map[string]any{"action": "list", "assignee": "wf-b"})
	if got := taskTitles(byID); len(got) != 2 {
		t.Fatalf("按工作流编号过滤应得 2 条: %v", got)
	}

	// 批量拒绝与接取；拒绝没有理由失败而不拖累其余。
	res = mustBoard(t, srv, "wf-b", map[string]any{"action": "batch", "ops": []any{
		map[string]any{"task_id": 1, "claim": "decline"},
		map[string]any{"task_id": 1, "claim": "decline", "note": "我手上没有这个环境"},
		map[string]any{"task_id": 2, "claim": "accept"},
	}})
	rs = resultsOf(res)
	if len(rs) != 3 || rs[0]["ok"] != false || rs[1]["ok"] != true || rs[1]["claim"] != "declined" || rs[2]["ok"] != true || rs[2]["state"] != "doing" {
		t.Fatalf("批量拒绝 / 接取回执不符: %#v", rs)
	}

	// 被拒的任务不再算「指派给你」；派发者的汇总里是 declined，并能改派给别人。
	ov := mustBoard(t, srv, "wf-b", map[string]any{})
	if l := asList(ov["assigned_to_you"]); len(l) != 1 || asMap(l[0])["ref"] != "#2" {
		t.Fatalf("被拒的 #1 不应再在 wf-b 的「指派给你」里: %#v", ov["assigned_to_you"])
	}
	ovA := mustBoard(t, srv, "wf-a", map[string]any{})
	if s := asMap(ovA["assigned_by_you"]); toInt(s["declined"]) != 1 || toInt(s["accepted"]) != 1 {
		t.Fatalf("派发者汇总应是 declined 1 / accepted 1: %#v", s)
	}
	res = mustBoard(t, srv, "wf-a", map[string]any{"action": "batch", "ops": []any{map[string]any{"task_id": 1, "assignee": "wf-c"}}})
	if rs = resultsOf(res); len(rs) != 1 || rs[0]["ok"] != true || rs[0]["assignee"] != "wf-c" || rs[0]["claim"] != "offered" {
		t.Fatalf("改派给 wf-c 应成功并回到待接取: %#v", rs)
	}
	// 撤销指派：assignee = ""。
	res = mustBoard(t, srv, "wf-a", map[string]any{"action": "batch", "ops": []any{map[string]any{"task_id": 1, "assignee": ""}}})
	if rs = resultsOf(res); len(rs) != 1 || rs[0]["ok"] != true || hasKey(rs[0], "assignee") {
		t.Fatalf("撤销指派后不应再带 assignee: %#v", rs)
	}
}

func TestTaskAssignRejectedInputs(t *testing.T) {
	srv := assignFixture(t)
	wcDo(t, srv, "wf-a", map[string]any{"action": "task_create", "title": "任务"})

	if msg := wcFail(t, srv, "wf-a", map[string]any{"action": "task_update", "task_id": 1, "assignee": "wf-out"}); !strings.Contains(msg, "不在这张任务板") {
		t.Fatalf("指派给项目外的工作流应被拒: %s", msg)
	}
	if msg := wcFail(t, srv, "wf-a", map[string]any{"action": "task_update", "task_id": 1, "assignee": "wf-b", "claim": "accept"}); !strings.Contains(msg, "不能在同一次调用") {
		t.Fatalf("assignee 与 claim 不能同时给: %s", msg)
	}
	if msg := wcFail(t, srv, "wf-a", map[string]any{"action": "task_update", "task_id": 1, "claim": "nope"}); !strings.Contains(msg, "未知的 claim") {
		t.Fatalf("未知的 claim 应被拒: %s", msg)
	}
	if msg := wcFail(t, srv, "wf-b", map[string]any{"action": "task_update", "task_id": 1, "claim": "accept"}); !strings.Contains(msg, "还没有指派") {
		t.Fatalf("没有指派的任务不能 claim: %s", msg)
	}

	// 独立板（项目外的工作流）上没有可指派的对象。
	wcDo(t, srv, "wf-out", map[string]any{"action": "task_create", "title": "独立任务"})
	if msg := wcFail(t, srv, "wf-out", map[string]any{"action": "task_update", "task_id": 1, "assignee": "wf-a"}); !strings.Contains(msg, "独立任务板") {
		t.Fatalf("独立板上不能指派: %s", msg)
	}
}

// 没有任何指派的板：回执与概览的形状与以前完全一致（没有 assignee / claim / assigned_by / workflow，也没有协作区）。
func TestTaskShapesUnchangedWithoutAssignment(t *testing.T) {
	srv, _ := setupWorkItemMCP(t)
	const wf = "wf-solo-shape"
	res := wcDo(t, srv, wf, map[string]any{"action": "task_create", "title": "单干任务", "description": "没有指派"})
	task := taskOf(t, res)
	for _, k := range []string{"assignee", "claim", "assigned_by", "workflow"} {
		if hasKey(task, k) {
			t.Fatalf("没有指派的任务不应带 %s: %#v", k, task)
		}
	}
	list := mustBoard(t, srv, wf, map[string]any{"action": "list"})
	for _, v := range asList(list["tasks"]) {
		for _, k := range []string{"assignee", "claim", "assigned_by", "workflow"} {
			if hasKey(asMap(v), k) {
				t.Fatalf("list 里没有指派的任务不应带 %s: %#v", k, v)
			}
		}
	}
	ov := mustBoard(t, srv, wf, map[string]any{})
	for _, k := range []string{"assigned_to_you", "assigned_to_you_hint", "assigned_by_you", "awaiting_your_acceptance"} {
		if hasKey(ov, k) {
			t.Fatalf("没有指派的板概览不应带 %s: %#v", k, ov)
		}
	}
	got := wcDo(t, srv, wf, map[string]any{"action": "task_get", "task_id": 1})
	for _, v := range asList(taskOf(t, got)["timeline"]) {
		if hasKey(asMap(v), "event") || hasKey(asMap(v), "workflow") {
			t.Fatalf("没有协作事件的时间线条目不应带 event / workflow: %#v", v)
		}
	}
}

// 共享板：别人创建的任务在 list / task_list 里带创建者，自己创建的不带。
func TestTaskCreatorShownForOthersOnSharedBoard(t *testing.T) {
	srv := assignFixture(t)
	wcDo(t, srv, "wf-a", map[string]any{"action": "task_create", "title": "A 的任务"})
	wcDo(t, srv, "wf-b", map[string]any{"action": "task_create", "title": "B 的任务"})

	check := func(wf string, tasks []any) {
		t.Helper()
		if len(tasks) != 2 {
			t.Fatalf("%s 应看到整张板的 2 条: %#v", wf, tasks)
		}
		for _, v := range tasks {
			m := asMap(v)
			own := (wf == "wf-a" && m["title"] == "A 的任务") || (wf == "wf-b" && m["title"] == "B 的任务")
			_, has := m["workflow"]
			if own == has {
				t.Fatalf("%s 看 %v：自己的任务不带 workflow、别人的才带: %#v", wf, m["title"], m)
			}
		}
	}
	for _, wf := range []string{"wf-a", "wf-b"} {
		check(wf, asList(mustBoard(t, srv, wf, map[string]any{"action": "list"})["tasks"]))
		check(wf, asList(wcDo(t, srv, wf, map[string]any{"action": "task_list"})["tasks"]))
	}
	// task_list 也支持 assignee 过滤。
	wcDo(t, srv, "wf-a", map[string]any{"action": "task_update", "task_id": 1, "assignee": "wf-b"})
	l := asList(wcDo(t, srv, "wf-b", map[string]any{"action": "task_list", "assignee": "me"})["tasks"])
	if len(l) != 1 || asMap(l[0])["title"] != "A 的任务" {
		t.Fatalf("task_list assignee=me: %#v", l)
	}
}

// 声明里新增的 assignee / claim 必须被两个工具的参数结构体读到（schema 与实现一致）。
func TestAssignClaimDeclaredAndDecoded(t *testing.T) {
	for name, schema := range map[string]json.RawMessage{"workflow_context": workflowContextSchema, "task_board": taskBoardSchema} {
		var s struct {
			Properties map[string]json.RawMessage `json:"properties"`
		}
		if err := json.Unmarshal(schema, &s); err != nil {
			t.Fatal(err)
		}
		if _, ok := s.Properties["assignee"]; !ok {
			t.Errorf("%s 的声明缺少 assignee", name)
		}
	}
	var wc struct {
		Properties map[string]struct {
			Enum []string `json:"enum"`
		} `json:"properties"`
	}
	_ = json.Unmarshal(workflowContextSchema, &wc)
	if got := strings.Join(wc.Properties["claim"].Enum, ","); got != "accept,decline,deliver,accept_delivery,reject_delivery" {
		t.Errorf("workflow_context 的 claim 枚举: %s", got)
	}
}
