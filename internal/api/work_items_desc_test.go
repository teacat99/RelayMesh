package api

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
)

// 任务描述：创建时可写，详情完整返回；只改描述会广播（页面要刷新）但不占时间线；超长给出明确的 400。
func TestWorkItemRESTDescription(t *testing.T) {
	srv, _ := newConvergenceServer(t, nil)
	jwt := wiredLogin(t, srv)
	auth := []string{"Authorization", "Bearer " + jwt}
	events := watchSSE(t, srv)

	res := wiredCall(srv, "POST", "/api/v1/work-items", map[string]any{
		"workflow_id": "wf-desc", "title": "终端历史按需加载", "description": "打开会话只拉最后一屏；向上滚动再按窗口加载更早的内容。",
	}, auth...)
	if res.Code != http.StatusCreated {
		t.Fatalf("创建应 201: %d %s", res.Code, res.Raw)
	}
	item := itemOf(t, res)
	if item["description"] != "打开会话只拉最后一屏；向上滚动再按窗口加载更早的内容。" {
		t.Fatalf("创建时的描述应原样返回: %s", res.Raw)
	}
	path := fmt.Sprintf("/api/v1/work-items/%d", num(item["id"]))

	// 详情 / 列表都带描述字段（没有描述的任务是空串而不是缺字段，前端按空串判断「去补充」）
	res = wiredCall(srv, "GET", path, nil, auth...)
	if got := itemOf(t, res)["description"]; got != "打开会话只拉最后一屏；向上滚动再按窗口加载更早的内容。" {
		t.Fatalf("详情应带描述: %s", res.Raw)
	}
	res = wiredCall(srv, "POST", "/api/v1/work-items", map[string]any{"workflow_id": "wf-desc", "title": "没写描述"}, auth...)
	if d, ok := itemOf(t, res)["description"]; !ok || d != "" {
		t.Fatalf("没写描述应返回空串: %s", res.Raw)
	}

	// 只改描述：changed=true 且广播；时间线保持 1 条
	events() // 丢掉创建时的广播，只看这次更新
	res = wiredCall(srv, "PUT", path, map[string]any{"description": "改写后的说明"}, auth...)
	item = itemOf(t, res)
	if res.Code != 200 || res.Body["changed"] != true || item["description"] != "改写后的说明" {
		t.Fatalf("改描述: %d %s", res.Code, res.Raw)
	}
	if n := workEvents(events()); n != 1 {
		t.Fatalf("改描述应广播 1 次 work_item_updated 让页面刷新，实际 %d", n)
	}
	if ups, _ := item["updates"].([]any); len(ups) != 1 {
		t.Fatalf("只改描述不应占时间线: %s", res.Raw)
	}

	// 空串清除；超长 400 并点明字段
	res = wiredCall(srv, "PUT", path, map[string]any{"description": ""}, auth...)
	if res.Code != 200 || itemOf(t, res)["description"] != "" {
		t.Fatalf("空串应清除描述: %d %s", res.Code, res.Raw)
	}
	res = wiredCall(srv, "PUT", path, map[string]any{"description": strings.Repeat("字", 1001)}, auth...)
	if res.Code != http.StatusBadRequest || !strings.Contains(res.str("error"), "描述最长") {
		t.Fatalf("超长描述应 400 并点明字段: %d %s", res.Code, res.Raw)
	}
}

// 页面上状态误点后改回：时间线上这一来一回互相抵消，不留无效记录。
func TestWorkItemRESTStateFlipLeavesNoNoise(t *testing.T) {
	srv, _ := newConvergenceServer(t, nil)
	jwt := wiredLogin(t, srv)
	auth := []string{"Authorization", "Bearer " + jwt}

	res := wiredCall(srv, "POST", "/api/v1/work-items", map[string]any{"workflow_id": "wf-flip", "title": "待确认的事", "state": "review"}, auth...)
	path := fmt.Sprintf("/api/v1/work-items/%d", num(itemOf(t, res)["id"]))

	for i := 0; i < 3; i++ {
		wiredCall(srv, "PUT", path, map[string]any{"state": "doing"}, auth...)
		res = wiredCall(srv, "PUT", path, map[string]any{"state": "review"}, auth...)
	}
	item := itemOf(t, res)
	ups, _ := item["updates"].([]any)
	if item["state"] != "review" || len(ups) != 1 {
		t.Fatalf("来回改状态后应回到 review 且时间线只剩创建记录: state=%v updates=%d %s", item["state"], len(ups), res.Raw)
	}
}
