package api

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/teacat99/RelayMesh/v2/internal/store"
)

// 阶段（stage）与「按会话查看任务」通过 REST 的行为：创建 / 修改 / 清除 / 过滤 / 带会话上下文。
func TestWorkItemRESTStageAndSessionScope(t *testing.T) {
	srv, _ := newConvergenceServer(t, nil)
	auth := []string{"Authorization", "Bearer " + wiredLogin(t, srv)}
	create := func(body map[string]any) wiredResp {
		body["workflow_id"] = "wf-stage"
		return wiredCall(srv, "POST", "/api/v1/work-items", body, auth...)
	}

	// 创建：中文别名被规整；无法识别的阶段 400 并列出可用值；不带则未标注
	res := create(map[string]any{"title": "先评估", "stage": "评估"})
	if res.Code != http.StatusCreated || itemOf(t, res)["stage"] != "assess" {
		t.Fatalf("创建带阶段: %d %s", res.Code, res.Raw)
	}
	first := num(itemOf(t, res)["id"])
	res = create(map[string]any{"title": "没标阶段"})
	if _, has := itemOf(t, res)["stage"]; res.Code != http.StatusCreated || has {
		t.Fatalf("没标阶段的任务不应有 stage: %d %s", res.Code, res.Raw)
	}
	res = create(map[string]any{"title": "坏阶段", "stage": "上线"})
	if res.Code != http.StatusBadRequest || !strings.Contains(res.str("error"), "未知的阶段") || !strings.Contains(res.str("error"), "improve") {
		t.Fatalf("无法识别的阶段应 400 并列出可用值: %d %s", res.Code, res.Raw)
	}

	// 修改 / 相同值空操作 / 清除
	path := fmt.Sprintf("/api/v1/work-items/%d", first)
	res = wiredCall(srv, "PUT", path, map[string]any{"stage": "dev"}, auth...)
	if res.Code != 200 || res.Body["changed"] != true || itemOf(t, res)["stage"] != "dev" {
		t.Fatalf("改阶段: %d %s", res.Code, res.Raw)
	}
	res = wiredCall(srv, "PUT", path, map[string]any{"stage": "开发"}, auth...)
	if res.Code != 200 || res.Body["changed"] != false {
		t.Fatalf("相同阶段应是空操作: %d %s", res.Code, res.Raw)
	}
	res = wiredCall(srv, "PUT", path, map[string]any{"stage": "上线"}, auth...)
	if res.Code != http.StatusBadRequest {
		t.Fatalf("改成无法识别的阶段应 400: %d %s", res.Code, res.Raw)
	}
	res = wiredCall(srv, "PUT", path, map[string]any{"stage": ""}, auth...)
	if _, has := itemOf(t, res)["stage"]; res.Code != 200 || res.Body["changed"] != true || has {
		t.Fatalf("空串应清除阶段: %d %s", res.Code, res.Raw)
	}
	wiredCall(srv, "PUT", path, map[string]any{"stage": "improve"}, auth...)

	// 过滤：id / 中文名 / none
	count := func(q string) int {
		res := wiredCall(srv, "GET", "/api/v1/work-items?workflow_id=wf-stage&"+q, nil, auth...)
		if res.Code != 200 {
			t.Fatalf("%s: %d %s", q, res.Code, res.Raw)
		}
		items, _ := res.Body["items"].([]any)
		return len(items)
	}
	if count("stage=improve") != 1 || count("stage=改进,none") != 2 || count("stage=none") != 1 || count("stage=assess") != 0 || count("stage=上线") != 0 {
		t.Fatal("stage 过滤结果不符")
	}

	// 按会话查看：只返回该会话涉及的任务，并附带该会话对每个任务做了什么
	ctx := context.Background()
	if _, err := srv.store.CreateFeedbackSession(ctx, store.CreateSessionInput{SessionID: "sess-stage", WorkflowID: "wf-stage", Title: "本对话", Summary: "s"}); err != nil {
		t.Fatal(err)
	}
	if res := wiredCall(srv, "POST", fmt.Sprintf("/api/v1/work-items/%d/sessions", first), map[string]any{"session_id": "sess-stage"}, auth...); res.Code != 200 {
		t.Fatalf("关联会话: %d %s", res.Code, res.Raw)
	}
	res = wiredCall(srv, "GET", "/api/v1/work-items?session_id=sess-stage", nil, auth...)
	items, _ := res.Body["items"].([]any)
	if res.Code != 200 || len(items) != 1 {
		t.Fatalf("按会话过滤应只返回关联的任务: %d %s", res.Code, res.Raw)
	}
	row := items[0].(map[string]any)
	ctxInfo, _ := row["session_ctx"].(map[string]any)
	if num(row["id"]) != first || ctxInfo == nil || ctxInfo["created"] == true || num(ctxInfo["updates"]) != 0 {
		t.Fatalf("手工关联：应有 session_ctx 且没有时间线记录: %s", res.Raw)
	}
	res = wiredCall(srv, "GET", "/api/v1/work-items?workflow_id=wf-stage", nil, auth...)
	for _, it := range res.Body["items"].([]any) {
		if _, has := it.(map[string]any)["session_ctx"]; has {
			t.Fatalf("不按会话过滤时不应有 session_ctx: %s", res.Raw)
		}
	}
}

// with_links=1：同时带回这个工作流里「哪个会话涉及哪个任务、做了什么」（会话页按轮次展开任务用）。
func TestWorkItemRESTListWithLinks(t *testing.T) {
	srv, _ := newConvergenceServer(t, nil)
	auth := []string{"Authorization", "Bearer " + wiredLogin(t, srv)}
	ctx := context.Background()
	// 同一个工作流里有 pending 会话时，再创建会就地覆写它；这里让两个会话分属不同工作流，只验证「按任务所在工作流取关联」。
	for sid, wf := range map[string]string{"sess-l1": "wf-links", "sess-l2": "wf-links-b"} {
		if _, err := srv.store.CreateFeedbackSession(ctx, store.CreateSessionInput{SessionID: sid, WorkflowID: wf, Title: sid, Summary: "s"}); err != nil {
			t.Fatal(err)
		}
	}
	res1, err := srv.store.CreateWorkItem(ctx, store.WorkItemInput{WorkflowID: "wf-links", Title: "链接任务", Stage: "plan"}, store.WorkActor{Kind: "agent", SessionID: "sess-l1"})
	if err != nil {
		t.Fatal(err)
	}
	state := "doing"
	if _, err = srv.store.UpdateWorkItem(ctx, res1.Item.ID, store.WorkItemPatch{State: &state}, store.WorkActor{Kind: "agent", SessionID: "sess-l2"}); err != nil {
		t.Fatal(err)
	}

	res := wiredCall(srv, "GET", "/api/v1/work-items?workflow_id=wf-links&with_links=1", nil, auth...)
	links, _ := res.Body["links"].([]any)
	if res.Code != 200 || len(links) != 2 {
		t.Fatalf("with_links=1 应带回 2 条关联: %d %s", res.Code, res.Raw)
	}
	first, second := links[0].(map[string]any), links[1].(map[string]any)
	if first["session_id"] != "sess-l1" || first["created"] != true || num(first["work_item_id"]) != int(res1.Item.ID) {
		t.Fatalf("第一条应是 sess-l1 创建的任务: %s", res.Raw)
	}
	if second["session_id"] != "sess-l2" || second["created"] == true || second["from_state"] != "todo" || second["to_state"] != "doing" {
		t.Fatalf("第二条应是 sess-l2 把任务从待办推到进行中: %s", res.Raw)
	}
	// 不带参数不返回 links；没有 workflow_id 时参数无效（不会把全部工作流的关联都吐出来）
	for _, q := range []string{"workflow_id=wf-links", "with_links=1"} {
		res = wiredCall(srv, "GET", "/api/v1/work-items?"+q, nil, auth...)
		if _, has := res.Body["links"]; res.Code != 200 || has {
			t.Fatalf("%s 不应带 links: %d %s", q, res.Code, res.Raw)
		}
	}
}
