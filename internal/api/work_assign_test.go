package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/teacat99/RelayMesh/v2/internal/store"
)

// 人在网页上指派 / 撤销 / 代接取 / 验收（#175 B2b）：REST 请求面新增 assignee_workflow_id 与 claim，列表可按承接者过滤。
func TestWorkAssignREST(t *testing.T) {
	srv := newWiredServer(t, nil)
	tok := wiredLogin(t, srv)
	auth := []string{"Authorization", "Bearer " + tok}
	call := func(m, p string, body any) wiredResp { return wiredCall(srv, m, p, body, auth...) }
	ctx := context.Background()

	for _, wf := range []struct{ id, dir string }{{"wf-a", "/code/proj"}, {"wf-b", "/code/proj-feat"}, {"wf-out", "/code/elsewhere"}} {
		if _, err := srv.store.CreateFeedbackSession(ctx, store.CreateSessionInput{SessionID: "s-" + wf.id, WorkflowID: wf.id, EnvHostName: "h", ProjectDirectory: wf.dir, Title: "t", Summary: "s"}); err != nil {
			t.Fatal(err)
		}
	}
	res := call("POST", "/api/v1/projects", map[string]any{
		"name": "主项目", "slug": "main",
		"dirs": []map[string]any{{"host_name": "h", "project_directory": "/code/proj"}, {"host_name": "h", "project_directory": "/code/proj-feat"}},
	})
	if res.Code != http.StatusCreated {
		t.Fatalf("创建项目: %d %s", res.Code, res.Raw)
	}

	// 新建时直接指派给 wf-b（人类派发：assigned_by 为空）；不带指派的任务没有这些字段。
	res = call("POST", "/api/v1/work-items", map[string]any{"workflow_id": "wf-a", "title": "写迁移脚本", "assignee_workflow_id": "wf-b"})
	if res.Code != http.StatusCreated {
		t.Fatalf("带指派的新建: %d %s", res.Code, res.Raw)
	}
	item := itemOf(t, res)
	id := strconv.Itoa(num(item["id"]))
	if item["assignee_workflow_id"] != "wf-b" || item["claim_state"] != "offered" {
		t.Fatalf("应指派给 wf-b 且待接取: %v", item)
	}
	if _, has := item["assigned_by_workflow_id"]; has {
		t.Fatalf("人类派发时 assigned_by_workflow_id 应省略: %v", item)
	}
	plain := itemOf(t, call("POST", "/api/v1/work-items", map[string]any{"workflow_id": "wf-a", "title": "自己的活"}))
	for _, k := range []string{"assignee_workflow_id", "claim_state", "assigned_at", "accepted_at"} {
		if _, has := plain[k]; has {
			t.Fatalf("没有指派的任务不应带 %s: %v", k, plain)
		}
	}

	// 按承接者过滤：工作流编号 / none；与「未归入模块」的 unassigned 互不相干。
	count := func(query string) int {
		t.Helper()
		r := call("GET", "/api/v1/work-items?workflow_id=wf-a"+query, nil)
		if r.Code != http.StatusOK {
			t.Fatalf("列表 %s: %d %s", query, r.Code, r.Raw)
		}
		items, _ := r.Body["items"].([]any)
		return len(items)
	}
	if count("&assignee=wf-b") != 1 || count("&assignee=none") != 1 || count("") != 2 || count("&assignee=wf-a") != 0 {
		t.Fatalf("按承接者过滤的条数不符")
	}

	// 非法：不在板上的工作流、assignee 与 claim 同给、未知 claim。
	if r := call("PUT", "/api/v1/work-items/"+id, map[string]any{"assignee_workflow_id": "wf-out"}); r.Code != http.StatusBadRequest || !strings.Contains(r.Raw, "不在这张任务板") {
		t.Fatalf("指派给板外工作流应 400: %d %s", r.Code, r.Raw)
	}
	if r := call("PUT", "/api/v1/work-items/"+id, map[string]any{"assignee_workflow_id": "wf-a", "claim": "accept"}); r.Code != http.StatusBadRequest {
		t.Fatalf("assignee 与 claim 同给应 400: %d %s", r.Code, r.Raw)
	}
	if r := call("PUT", "/api/v1/work-items/"+id, map[string]any{"claim": "nope"}); r.Code != http.StatusBadRequest {
		t.Fatalf("未知 claim 应 400: %d %s", r.Code, r.Raw)
	}

	// 人代承接者接取 → 交付 → 验收（人类不受任务级限制，验收完成要结论）。
	put := func(body map[string]any) (int, map[string]any) {
		t.Helper()
		r := call("PUT", "/api/v1/work-items/"+id, body)
		it, _ := r.Body["item"].(map[string]any)
		return r.Code, it
	}
	if code, it := put(map[string]any{"claim": "accept"}); code != 200 || it["claim_state"] != "accepted" || it["state"] != "doing" {
		t.Fatalf("接取: %d %v", code, it)
	}
	if code, _ := put(map[string]any{"claim": "deliver"}); code != http.StatusBadRequest {
		t.Fatalf("交付没有说明应 400: %d", code)
	}
	if code, it := put(map[string]any{"claim": "deliver", "note": "脚本好了"}); code != 200 || it["state"] != "review" {
		t.Fatalf("交付: %d %v", code, it)
	}
	if code, it := put(map[string]any{"claim": "accept_delivery", "conclusion": "已验证"}); code != 200 || it["state"] != "done" {
		t.Fatalf("验收: %d %v", code, it)
	}

	// 详情的时间线带协作事件；人类写入没有操作者工作流。
	res = call("GET", "/api/v1/work-items/"+id, nil)
	events := map[string]bool{}
	for _, v := range res.Body["item"].(map[string]any)["updates"].([]any) {
		u := v.(map[string]any)
		if ev, _ := u["event"].(string); ev != "" {
			events[ev] = true
		}
		if _, has := u["actor_workflow_id"]; has {
			t.Fatalf("人类写入不应带 actor_workflow_id: %v", u)
		}
	}
	for _, ev := range []string{"assign", "accept", "deliver", "accept_delivery"} {
		if !events[ev] {
			t.Fatalf("时间线缺少 %s 事件: %v", ev, events)
		}
	}

	// 已完成的任务不能改派；重新打开后可以改派，空串撤销指派。
	if code, _ := put(map[string]any{"assignee_workflow_id": "wf-a"}); code != http.StatusBadRequest {
		t.Fatalf("已完成的任务不能改派: %d", code)
	}
	if code, it := put(map[string]any{"state": "doing"}); code != 200 || it["state"] != "doing" {
		t.Fatalf("重新打开: %d %v", code, it)
	}
	if code, it := put(map[string]any{"assignee_workflow_id": "wf-a"}); code != 200 || it["assignee_workflow_id"] != "wf-a" || it["claim_state"] != "offered" {
		t.Fatalf("改派给 wf-a: %d %v", code, it)
	}
	if code, it := put(map[string]any{"assignee_workflow_id": ""}); code != 200 {
		t.Fatalf("撤销指派: %d %v", code, it)
	} else if _, has := it["assignee_workflow_id"]; has {
		t.Fatalf("撤销后不应再有承接者: %v", it)
	}
}

// ErrForbidden 映射成 403（人类走 REST 不会触发，但统一错误出口不能把它当成 500）。
func TestRespondWorkErrorForbidden(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	respondWorkError(c, store.NewForbiddenError("没有权限"))
	if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "没有权限") {
		t.Fatalf("应是 403: %d %s", w.Code, w.Body.String())
	}
}

// 「指派」下拉的候选按需取：项目板 = 项目里的全部工作流（包括还没有任务的），独立板为空，缺参数 400，未登录 401。
func TestWorkItemAssigneesREST(t *testing.T) {
	srv := newWiredServer(t, nil)
	tok := wiredLogin(t, srv)
	auth := []string{"Authorization", "Bearer " + tok}
	call := func(m, p string, body any) wiredResp { return wiredCall(srv, m, p, body, auth...) }
	ctx := context.Background()
	for _, wf := range []struct{ id, dir string }{{"wf-a", "/code/proj"}, {"wf-b", "/code/proj-feat"}, {"wf-out", "/code/elsewhere"}} {
		if _, err := srv.store.CreateFeedbackSession(ctx, store.CreateSessionInput{SessionID: "s-" + wf.id, WorkflowID: wf.id, EnvHostName: "h", ProjectDirectory: wf.dir, Title: "t", Summary: "s"}); err != nil {
			t.Fatal(err)
		}
	}
	if r := call("POST", "/api/v1/projects", map[string]any{
		"name": "主项目", "slug": "main",
		"dirs": []map[string]any{{"host_name": "h", "project_directory": "/code/proj"}, {"host_name": "h", "project_directory": "/code/proj-feat"}},
	}); r.Code != http.StatusCreated {
		t.Fatalf("创建项目: %d %s", r.Code, r.Raw)
	}

	members := func(wf string) ([]any, string) {
		t.Helper()
		r := call("GET", "/api/v1/work-items/assignees?workflow_id="+wf, nil)
		if r.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", wf, r.Code, r.Raw)
		}
		list, _ := r.Body["members"].([]any)
		key, _ := r.Body["board_key"].(string)
		return list, key
	}
	list, key := members("wf-a")
	if len(list) != 2 || list[0] != "wf-a" || list[1] != "wf-b" || !strings.HasPrefix(key, "p") {
		t.Fatalf("项目板候选应是 wf-a / wf-b（还没有任何任务也在）: %v board=%s", list, key)
	}
	if list, key = members("wf-out"); len(list) != 0 || !strings.HasPrefix(key, "w:") {
		t.Fatalf("独立板没有候选: %v board=%s", list, key)
	}
	if r := call("GET", "/api/v1/work-items/assignees", nil); r.Code != http.StatusBadRequest {
		t.Fatalf("缺 workflow_id 应 400: %d", r.Code)
	}
	if r := wiredCall(srv, "GET", "/api/v1/work-items/assignees?workflow_id=wf-a", nil); r.Code != http.StatusUnauthorized {
		t.Fatalf("未登录应 401: %d", r.Code)
	}
}
