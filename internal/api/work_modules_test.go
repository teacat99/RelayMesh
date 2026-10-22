package api

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
)

func moduleEvents(msgs []string) int {
	n := 0
	for _, m := range msgs {
		if strings.HasPrefix(m, "event: work_module_updated\n") {
			n++
		}
	}
	return n
}

func moduleOf(t *testing.T, res wiredResp) map[string]any {
	t.Helper()
	m, ok := res.Body["module"].(map[string]any)
	if !ok {
		t.Fatalf("响应里没有 module: %d %s", res.Code, res.Raw)
	}
	return m
}

func modulePathsOf(t *testing.T, res wiredResp) []string {
	t.Helper()
	list, ok := res.Body["modules"].([]any)
	if !ok {
		t.Fatalf("响应里没有 modules: %d %s", res.Code, res.Raw)
	}
	out := make([]string, 0, len(list))
	for _, v := range list {
		m, _ := v.(map[string]any)
		out = append(out, fmt.Sprint(m["path"]))
	}
	return out
}

// 规划接口同样只给登录后的管理员：未登录 401，任何 MCP 令牌 403。
func TestWorkModuleRESTAuthBoundaries(t *testing.T) {
	srv, dbToken := newConvergenceServer(t, nil)
	calls := []struct{ method, path string }{
		{"GET", "/api/v1/work-modules?workflow_id=w"}, {"POST", "/api/v1/work-modules"}, {"PUT", "/api/v1/work-modules/1"},
		{"POST", "/api/v1/work-modules/1/move"}, {"DELETE", "/api/v1/work-modules/1"},
	}
	for _, call := range calls {
		if res := wiredCall(srv, call.method, call.path, nil); res.Code != http.StatusUnauthorized {
			t.Fatalf("未登录访问 %s %s 应 401: %d %s", call.method, call.path, res.Code, res.Raw)
		}
	}
	for name, tok := range map[string]string{"环境令牌": wiredMCPToken, "凭据库令牌": dbToken} {
		for _, call := range calls {
			res := wiredCall(srv, call.method, call.path, map[string]any{"name": "x", "workflow_id": "w"}, "Authorization", "Bearer "+tok)
			if res.Code != http.StatusForbidden || res.str("code") != "mcp_token_forbidden" {
				t.Fatalf("%s 访问 %s %s 应得到 403 mcp_token_forbidden: %d %s", name, call.method, call.path, res.Code, res.Raw)
			}
		}
	}
}

func TestWorkModuleRESTLifecycle(t *testing.T) {
	srv, _ := newConvergenceServer(t, nil)
	jwt := wiredLogin(t, srv)
	auth := []string{"Authorization", "Bearer " + jwt}
	const wf = "wf-mod"

	// 建模块与功能。
	res := wiredCall(srv, "POST", "/api/v1/work-modules", map[string]any{"workflow_id": wf, "name": "终端", "description": "终端模式"}, auth...)
	if res.Code != http.StatusCreated {
		t.Fatalf("创建模块应 201: %d %s", res.Code, res.Raw)
	}
	term := moduleOf(t, res)
	termID := num(term["id"])
	if term["path"] != "终端" || term["description"] != "终端模式" || num(term["depth"]) != 1 {
		t.Fatalf("模块内容不符: %s", res.Raw)
	}
	res = wiredCall(srv, "POST", "/api/v1/work-modules", map[string]any{"workflow_id": wf, "parent_id": termID, "name": "历史日志"}, auth...)
	if res.Code != http.StatusCreated {
		t.Fatalf("创建功能应 201: %d %s", res.Code, res.Raw)
	}
	hist := moduleOf(t, res)
	histID := num(hist["id"])
	if hist["path"] != "终端/历史日志" || num(hist["depth"]) != 2 {
		t.Fatalf("功能内容不符: %s", res.Raw)
	}
	res = wiredCall(srv, "POST", "/api/v1/work-modules", map[string]any{"workflow_id": wf, "name": "任务"}, auth...)
	taskModID := num(moduleOf(t, res)["id"])

	// 校验失败：重名 / 非法名称 / 太深 / 空 workflow / 无效编号。
	if res := wiredCall(srv, "POST", "/api/v1/work-modules", map[string]any{"workflow_id": wf, "name": " 终端 "}, auth...); res.Code != http.StatusBadRequest || !strings.Contains(res.str("error"), "不能重名") {
		t.Fatalf("重名应 400: %d %s", res.Code, res.Raw)
	}
	if res := wiredCall(srv, "POST", "/api/v1/work-modules", map[string]any{"workflow_id": wf, "name": "a/b"}, auth...); res.Code != http.StatusBadRequest {
		t.Fatalf("含分隔符应 400: %d %s", res.Code, res.Raw)
	}
	if res := wiredCall(srv, "POST", "/api/v1/work-modules", map[string]any{"workflow_id": wf, "parent_id": histID, "name": "更深"}, auth...); res.Code != http.StatusBadRequest || !strings.Contains(res.str("error"), "最多 2 层") {
		t.Fatalf("三层应 400: %d %s", res.Code, res.Raw)
	}
	if res := wiredCall(srv, "POST", "/api/v1/work-modules", map[string]any{"workflow_id": wf, "parent_id": 99999, "name": "孤儿"}, auth...); res.Code != http.StatusNotFound {
		t.Fatalf("上级不存在应 404: %d %s", res.Code, res.Raw)
	}
	if res := wiredCall(srv, "GET", "/api/v1/work-modules", nil, auth...); res.Code != http.StatusBadRequest {
		t.Fatalf("缺 workflow_id 应 400: %d %s", res.Code, res.Raw)
	}
	if res := wiredCall(srv, "PUT", "/api/v1/work-modules/abc", map[string]any{"name": "x"}, auth...); res.Code != http.StatusBadRequest {
		t.Fatalf("无效编号应 400: %d %s", res.Code, res.Raw)
	}
	if res := wiredCall(srv, "PUT", "/api/v1/work-modules/99999", map[string]any{"name": "x"}, auth...); res.Code != http.StatusNotFound {
		t.Fatalf("不存在的模块应 404: %d %s", res.Code, res.Raw)
	}

	// 任务归类 + 显式完成度。
	res = wiredCall(srv, "POST", "/api/v1/work-items", map[string]any{
		"workflow_id": wf, "title": "平铺历史", "state": "doing", "module_id": histID, "percent": 40,
	}, auth...)
	if res.Code != http.StatusCreated {
		t.Fatalf("创建任务应 201: %d %s", res.Code, res.Raw)
	}
	item := itemOf(t, res)
	itemID := num(item["id"])
	if item["module"] != "终端/历史日志" || num(item["module_id"]) != histID || num(item["percent"]) != 40 || num(item["progress_pct"]) != 40 || item["progress_from"] != "explicit" {
		t.Fatalf("任务的模块 / 进度字段不符: %s", res.Raw)
	}
	res = wiredCall(srv, "POST", "/api/v1/work-items", map[string]any{"workflow_id": wf, "title": "没归类"}, auth...)
	loose := num(itemOf(t, res)["id"])
	if res := wiredCall(srv, "POST", "/api/v1/work-items", map[string]any{"workflow_id": wf, "title": "x", "module_id": 99999}, auth...); res.Code != http.StatusBadRequest {
		t.Fatalf("不存在的模块应 400: %d %s", res.Code, res.Raw)
	}
	if res := wiredCall(srv, "POST", "/api/v1/work-items", map[string]any{"workflow_id": wf, "title": "x", "percent": 120}, auth...); res.Code != http.StatusBadRequest || !strings.Contains(res.str("error"), "percent") {
		t.Fatalf("percent 越界应 400: %d %s", res.Code, res.Raw)
	}

	// 列表：指定工作流时附带规划树与未归类汇总；按模块 / 未归类筛选。
	res = wiredCall(srv, "GET", "/api/v1/work-items?workflow_id="+wf, nil, auth...)
	if got := modulePathsOf(t, res); strings.Join(got, ",") != "终端,终端/历史日志,任务" {
		t.Fatalf("work-items 响应里的规划树不对: %v", got)
	}
	un, _ := res.Body["unassigned"].(map[string]any)
	if num(un["total"]) != 1 || num(un["todo"]) != 1 {
		t.Fatalf("未归类汇总不对: %s", res.Raw)
	}
	idsOf := func(path string) []int {
		r := wiredCall(srv, "GET", path, nil, auth...)
		list, _ := r.Body["items"].([]any)
		out := make([]int, 0, len(list))
		for _, v := range list {
			m, _ := v.(map[string]any)
			out = append(out, num(m["id"]))
		}
		return out
	}
	if got := idsOf(fmt.Sprintf("/api/v1/work-items?workflow_id=%s&module_id=%d", wf, termID)); len(got) != 1 || got[0] != itemID {
		t.Fatalf("按模块筛选应包含功能里的任务: %v", got)
	}
	if got := idsOf(fmt.Sprintf("/api/v1/work-items?workflow_id=%s&unassigned=1", wf)); len(got) != 1 || got[0] != loose {
		t.Fatalf("按未归类筛选: %v", got)
	}
	if res := wiredCall(srv, "GET", "/api/v1/work-items?module_id=abc", nil, auth...); res.Code != http.StatusBadRequest {
		t.Fatalf("无效 module_id 应 400: %d %s", res.Code, res.Raw)
	}

	// 规划树接口自带汇总。
	res = wiredCall(srv, "GET", "/api/v1/work-modules?workflow_id="+wf, nil, auth...)
	mods, _ := res.Body["modules"].([]any)
	if len(mods) != 3 {
		t.Fatalf("规划树应有 3 个节点: %s", res.Raw)
	}
	top, _ := mods[0].(map[string]any)
	stats, _ := top["stats"].(map[string]any)
	if num(stats["total"]) != 1 || num(stats["doing"]) != 1 || num(stats["percent"]) != 40 {
		t.Fatalf("终端模块的汇总不对: %s", res.Raw)
	}

	// 改名 / 改说明 / 排序。
	res = wiredCall(srv, "PUT", fmt.Sprintf("/api/v1/work-modules/%d", histID), map[string]any{"name": "历史", "description": "xterm 历史"}, auth...)
	if res.Code != 200 || moduleOf(t, res)["path"] != "终端/历史" {
		t.Fatalf("改名: %d %s", res.Code, res.Raw)
	}
	res = wiredCall(srv, "POST", fmt.Sprintf("/api/v1/work-modules/%d/move", taskModID), map[string]any{"direction": "up"}, auth...)
	if got := modulePathsOf(t, res); strings.Join(got, ",") != "任务,终端,终端/历史" {
		t.Fatalf("上移后顺序不对: %v (%s)", got, res.Raw)
	}
	if res := wiredCall(srv, "POST", fmt.Sprintf("/api/v1/work-modules/%d/move", taskModID), map[string]any{"direction": "sideways"}, auth...); res.Code != http.StatusBadRequest {
		t.Fatalf("非法方向应 400: %d %s", res.Code, res.Raw)
	}
	if res := wiredCall(srv, "POST", "/api/v1/work-modules/99999/move", map[string]any{"direction": "up"}, auth...); res.Code != http.StatusNotFound {
		t.Fatalf("不存在的模块应 404: %d %s", res.Code, res.Raw)
	}

	// 改任务归属：移到「任务」模块；0 取消归类；别的工作流的模块被拒绝。
	itemPath := fmt.Sprintf("/api/v1/work-items/%d", itemID)
	res = wiredCall(srv, "PUT", itemPath, map[string]any{"module_id": taskModID}, auth...)
	if res.Code != 200 || itemOf(t, res)["module"] != "任务" {
		t.Fatalf("改归属: %d %s", res.Code, res.Raw)
	}
	res = wiredCall(srv, "PUT", itemPath, map[string]any{"percent": -1}, auth...)
	if got := itemOf(t, res); res.Code != 200 || got["percent"] != nil || num(got["progress_pct"]) != 50 || got["progress_from"] != "state" {
		t.Fatalf("清除完成度: %d %s", res.Code, res.Raw)
	}
	other := wiredCall(srv, "POST", "/api/v1/work-modules", map[string]any{"workflow_id": "wf-else", "name": "别处"}, auth...)
	if res := wiredCall(srv, "PUT", itemPath, map[string]any{"module_id": num(moduleOf(t, other)["id"])}, auth...); res.Code != http.StatusBadRequest {
		t.Fatalf("别的工作流的模块应 400: %d %s", res.Code, res.Raw)
	}

	// 删除模块：任务不删，回落（顶层模块 → 未归类）；功能随模块一起删。
	res = wiredCall(srv, "DELETE", fmt.Sprintf("/api/v1/work-modules/%d", taskModID), nil, auth...)
	if res.Code != 200 || num(res.Body["moved_tasks"]) != 1 || res.str("path") != "任务" {
		t.Fatalf("删除模块: %d %s", res.Code, res.Raw)
	}
	res = wiredCall(srv, "GET", itemPath, nil, auth...)
	if got := itemOf(t, res); got["module_id"] != nil || got["module"] != nil {
		t.Fatalf("任务应变成未归类: %s", res.Raw)
	}
	res = wiredCall(srv, "DELETE", fmt.Sprintf("/api/v1/work-modules/%d", termID), nil, auth...)
	if res.Code != 200 || num(res.Body["removed_features"]) != 1 {
		t.Fatalf("删除顶层模块应连带功能: %d %s", res.Code, res.Raw)
	}
	if res := wiredCall(srv, "DELETE", fmt.Sprintf("/api/v1/work-modules/%d", termID), nil, auth...); res.Code != http.StatusNotFound {
		t.Fatalf("重复删除应 404: %d %s", res.Code, res.Raw)
	}
}

// 每一次规划树的改动都要通过 SSE 通知前端重新取数。
func TestWorkModuleRESTBroadcastsChanges(t *testing.T) {
	srv, _ := newConvergenceServer(t, nil)
	jwt := wiredLogin(t, srv)
	auth := []string{"Authorization", "Bearer " + jwt}
	events := watchSSE(t, srv)

	res := wiredCall(srv, "POST", "/api/v1/work-modules", map[string]any{"workflow_id": "wf-sse", "name": "A"}, auth...)
	id := num(moduleOf(t, res)["id"])
	wiredCall(srv, "POST", "/api/v1/work-modules", map[string]any{"workflow_id": "wf-sse", "name": "B"}, auth...)
	if n := moduleEvents(events()); n != 2 {
		t.Fatalf("两次创建应广播 2 次，实际 %d", n)
	}
	wiredCall(srv, "PUT", fmt.Sprintf("/api/v1/work-modules/%d", id), map[string]any{"name": "A2"}, auth...)
	wiredCall(srv, "POST", fmt.Sprintf("/api/v1/work-modules/%d/move", id), map[string]any{"direction": "down"}, auth...)
	wiredCall(srv, "DELETE", fmt.Sprintf("/api/v1/work-modules/%d", id), nil, auth...)
	if n := moduleEvents(events()); n != 3 {
		t.Fatalf("改名 / 移动 / 删除应广播 3 次，实际 %d", n)
	}
	// 失败的请求不广播。
	wiredCall(srv, "POST", "/api/v1/work-modules", map[string]any{"workflow_id": "wf-sse", "name": "B"}, auth...)
	if n := moduleEvents(events()); n != 0 {
		t.Fatalf("失败的请求不应广播，实际 %d", n)
	}
}
