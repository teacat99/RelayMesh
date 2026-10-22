package api

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/teacat99/RelayMesh/v2/internal/store"
)

// watchSSE 订阅服务端的 SSE 广播，返回读取已收到事件的函数。
func watchSSE(t *testing.T, srv *Server) func() []string {
	t.Helper()
	ch := make(chan string, 64)
	b := srv.Broker()
	b.clientsMu.Lock()
	b.clients[ch] = true
	b.clientsMu.Unlock()
	t.Cleanup(func() {
		b.clientsMu.Lock()
		delete(b.clients, ch)
		b.clientsMu.Unlock()
	})
	return func() []string {
		var out []string
		for {
			select {
			case m := <-ch:
				out = append(out, m)
			default:
				return out
			}
		}
	}
}

func workEvents(msgs []string) int {
	n := 0
	for _, m := range msgs {
		if strings.HasPrefix(m, "event: work_item_updated\n") {
			n++
		}
	}
	return n
}

func itemOf(t *testing.T, res wiredResp) map[string]any {
	t.Helper()
	item, ok := res.Body["item"].(map[string]any)
	if !ok {
		t.Fatalf("响应里没有 item: %d %s", res.Code, res.Raw)
	}
	return item
}

func num(v any) int {
	f, _ := v.(float64)
	return int(f)
}

// 任务清单只给登录后的管理员：未登录 401，任何 MCP 令牌 403（智能体走 MCP 的 workflow_context）。
func TestWorkItemRESTAuthBoundaries(t *testing.T) {
	srv, dbToken := newConvergenceServer(t, nil)
	if res := wiredCall(srv, "GET", "/api/v1/work-items", nil); res.Code != http.StatusUnauthorized {
		t.Fatalf("未登录应 401: %d %s", res.Code, res.Raw)
	}
	for name, tok := range map[string]string{"环境令牌": wiredMCPToken, "凭据库令牌": dbToken} {
		for _, call := range []struct{ method, path string }{{"GET", "/api/v1/work-items"}, {"POST", "/api/v1/work-items"}, {"PUT", "/api/v1/work-items/1"}, {"DELETE", "/api/v1/work-items/1"}} {
			res := wiredCall(srv, call.method, call.path, map[string]any{"title": "x", "workflow_id": "w"}, "Authorization", "Bearer "+tok)
			if res.Code != http.StatusForbidden || res.str("code") != "mcp_token_forbidden" {
				t.Fatalf("%s 访问 %s %s 应得到 403 mcp_token_forbidden: %d %s", name, call.method, call.path, res.Code, res.Raw)
			}
		}
	}
}

func TestWorkItemRESTLifecycle(t *testing.T) {
	srv, _ := newConvergenceServer(t, nil)
	jwt := wiredLogin(t, srv)
	auth := []string{"Authorization", "Bearer " + jwt}
	events := watchSSE(t, srv)

	// 创建
	res := wiredCall(srv, "POST", "/api/v1/work-items", map[string]any{
		"workflow_id": "wf-rest", "title": "整理设置页", "kind": "improve", "labels": []string{"设置", "ui"}, "progress": "先梳理分组", "refs": []string{"D-201"},
	}, auth...)
	if res.Code != http.StatusCreated {
		t.Fatalf("创建应 201: %d %s", res.Code, res.Raw)
	}
	item := itemOf(t, res)
	id := num(item["id"])
	if id == 0 || num(item["seq"]) != 1 || item["kind"] != "improve" || item["state"] != "todo" || item["created_by"] != "human" || item["progress"] != "先梳理分组" {
		t.Fatalf("创建结果不符: %s", res.Raw)
	}
	path := fmt.Sprintf("/api/v1/work-items/%d", id)

	// 校验失败：标题为空 / 未知类型 / 不存在的任务
	if res := wiredCall(srv, "POST", "/api/v1/work-items", map[string]any{"workflow_id": "wf-rest", "title": "  "}, auth...); res.Code != http.StatusBadRequest {
		t.Fatalf("空标题应 400: %d %s", res.Code, res.Raw)
	}
	if res := wiredCall(srv, "POST", "/api/v1/work-items", map[string]any{"workflow_id": "wf-rest", "title": "x", "kind": "nope"}, auth...); res.Code != http.StatusBadRequest || !strings.Contains(res.str("error"), "bug") {
		t.Fatalf("未知类型应 400 并列出可选值: %d %s", res.Code, res.Raw)
	}
	if res := wiredCall(srv, "PUT", "/api/v1/work-items/9999", map[string]any{"title": "x"}, auth...); res.Code != http.StatusNotFound {
		t.Fatalf("不存在的任务应 404: %d %s", res.Code, res.Raw)
	}
	if res := wiredCall(srv, "GET", "/api/v1/work-items/abc", nil, auth...); res.Code != http.StatusBadRequest {
		t.Fatalf("无效编号应 400: %d %s", res.Code, res.Raw)
	}

	// 列表：附计数与工作流概况
	res = wiredCall(srv, "GET", "/api/v1/work-items?workflow_id=wf-rest", nil, auth...)
	if res.Code != 200 {
		t.Fatalf("列表: %d %s", res.Code, res.Raw)
	}
	items, _ := res.Body["items"].([]any)
	counts, _ := res.Body["counts"].(map[string]any)
	workflows, _ := res.Body["workflows"].([]any)
	if len(items) != 1 || num(counts["todo"]) != 1 || len(workflows) != 1 {
		t.Fatalf("列表 / 计数 / 工作流概况不符: %s", res.Raw)
	}

	// 人可以确认完成；关闭时间落库
	res = wiredCall(srv, "PUT", path, map[string]any{"state": "done", "conclusion": "设置页按模块分组完成", "note": "已验收"}, auth...)
	item = itemOf(t, res)
	if res.Code != 200 || item["state"] != "done" || item["closed_at"] == nil || res.Body["changed"] != true || item["conclusion"] != "设置页按模块分组完成" {
		t.Fatalf("人确认完成: %d %s", res.Code, res.Raw)
	}
	// 同样的更新再来一次：没有变化，不重复广播
	before := workEvents(events())
	res = wiredCall(srv, "PUT", path, map[string]any{"state": "done"}, auth...)
	if res.Code != 200 || res.Body["changed"] != false {
		t.Fatalf("无变化应 changed=false: %d %s", res.Code, res.Raw)
	}
	if n := workEvents(events()); n != 0 {
		t.Fatalf("无变化的更新不应广播事件，却收到 %d 条（之前 %d）", n, before)
	}

	// 人可以重新打开
	res = wiredCall(srv, "PUT", path, map[string]any{"state": "doing"}, auth...)
	if item = itemOf(t, res); item["state"] != "doing" || item["closed_at"] != nil {
		t.Fatalf("人重新打开: %s", res.Raw)
	}

	// 会话关联：不存在的会话 404；存在则关联 / 解除
	if res := wiredCall(srv, "POST", path+"/sessions", map[string]any{"session_id": "sess-none"}, auth...); res.Code != http.StatusNotFound {
		t.Fatalf("不存在的会话应 404: %d %s", res.Code, res.Raw)
	}
	if _, err := srv.store.CreateFeedbackSession(context.Background(), store.CreateSessionInput{SessionID: "sess-rest", WorkflowID: "wf-rest", Title: "关联用会话", Summary: "s"}); err != nil {
		t.Fatal(err)
	}
	res = wiredCall(srv, "POST", path+"/sessions", map[string]any{"session_id": "sess-rest"}, auth...)
	sessions, _ := itemOf(t, res)["sessions"].([]any)
	if res.Code != 200 || len(sessions) != 1 {
		t.Fatalf("关联会话: %d %s", res.Code, res.Raw)
	}
	res = wiredCall(srv, "GET", path, nil, auth...)
	if sessions, _ = itemOf(t, res)["sessions"].([]any); len(sessions) != 1 || sessions[0].(map[string]any)["title"] != "关联用会话" {
		t.Fatalf("详情应带会话标题: %s", res.Raw)
	}
	res = wiredCall(srv, "DELETE", path+"/sessions/sess-rest", nil, auth...)
	if sessions, _ = itemOf(t, res)["sessions"].([]any); res.Code != 200 || len(sessions) != 0 {
		t.Fatalf("解除关联: %d %s", res.Code, res.Raw)
	}
	// 会话本身不受影响
	if res := wiredCall(srv, "GET", "/api/v1/sessions/sess-rest", nil, auth...); res.Code != 200 {
		t.Fatalf("解除关联不应影响会话: %d %s", res.Code, res.Raw)
	}

	// 删除：之后 404；广播带 deleted
	events() // 清空
	if res := wiredCall(srv, "DELETE", path, nil, auth...); res.Code != 200 {
		t.Fatalf("删除: %d %s", res.Code, res.Raw)
	}
	if res := wiredCall(srv, "GET", path, nil, auth...); res.Code != http.StatusNotFound {
		t.Fatalf("删除后应 404: %d %s", res.Code, res.Raw)
	}
	var deletedEvent bool
	for _, m := range events() {
		if strings.HasPrefix(m, "event: work_item_updated\n") && strings.Contains(m, `"deleted":true`) {
			deletedEvent = true
		}
	}
	if !deletedEvent {
		t.Fatal("删除应广播 work_item_updated 且带 deleted=true")
	}
}

// 子任务、批次统计、按条件筛选通过 REST 同样可用。
func TestWorkItemRESTParentAndFilters(t *testing.T) {
	srv, _ := newConvergenceServer(t, nil)
	auth := []string{"Authorization", "Bearer " + wiredLogin(t, srv)}
	create := func(body map[string]any) map[string]any {
		body["workflow_id"] = "wf-batch"
		res := wiredCall(srv, "POST", "/api/v1/work-items", body, auth...)
		if res.Code != http.StatusCreated {
			t.Fatalf("创建 %v: %d %s", body, res.Code, res.Raw)
		}
		return itemOf(t, res)
	}
	batch := create(map[string]any{"title": "一批问题"})
	bid := num(batch["id"])
	create(map[string]any{"title": "子一", "parent_id": bid, "state": "done"})
	create(map[string]any{"title": "子二", "parent_id": bid, "kind": "bug"})
	create(map[string]any{"title": "独立的", "kind": "ops"})

	res := wiredCall(srv, "GET", "/api/v1/work-items?workflow_id=wf-batch&top_level=1", nil, auth...)
	items, _ := res.Body["items"].([]any)
	if len(items) != 2 {
		t.Fatalf("top_level 只应返回顶层任务: %s", res.Raw)
	}
	var top map[string]any
	for _, it := range items {
		if m := it.(map[string]any); m["title"] == "一批问题" {
			top = m
		}
	}
	if top == nil || num(top["child_total"]) != 2 || num(top["child_done"]) != 1 {
		t.Fatalf("批次应汇总子任务进度: %v", top)
	}
	res = wiredCall(srv, "GET", fmt.Sprintf("/api/v1/work-items?workflow_id=wf-batch&parent_id=%d", bid), nil, auth...)
	if items, _ = res.Body["items"].([]any); len(items) != 2 {
		t.Fatalf("parent_id 筛选: %s", res.Raw)
	}
	res = wiredCall(srv, "GET", "/api/v1/work-items?workflow_id=wf-batch&kind=bug,ops&state=todo", nil, auth...)
	if items, _ = res.Body["items"].([]any); len(items) != 2 {
		t.Fatalf("kind + state 筛选: %s", res.Raw)
	}
	// 子任务的 parent 信息
	res = wiredCall(srv, "GET", "/api/v1/work-items?workflow_id=wf-batch&q=子二", nil, auth...)
	items, _ = res.Body["items"].([]any)
	if len(items) != 1 || items[0].(map[string]any)["parent_title"] != "一批问题" {
		t.Fatalf("子任务应带父任务标题: %s", res.Raw)
	}
	// 批次不能有第二层
	res = wiredCall(srv, "POST", "/api/v1/work-items", map[string]any{"workflow_id": "wf-batch", "title": "孙", "parent_id": num(items[0].(map[string]any)["id"])}, auth...)
	if res.Code != http.StatusBadRequest || !strings.Contains(res.str("error"), "一层") {
		t.Fatalf("不允许多层嵌套: %d %s", res.Code, res.Raw)
	}
	// 参数校验
	for _, q := range []string{"parent_id=x", "closed_days=-1", "limit=zz"} {
		if res := wiredCall(srv, "GET", "/api/v1/work-items?"+q, nil, auth...); res.Code != http.StatusBadRequest {
			t.Fatalf("%s 应 400: %d %s", q, res.Code, res.Raw)
		}
	}
}
