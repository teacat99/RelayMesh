package api

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/teacat99/RelayMesh/v2/internal/model"
	"github.com/teacat99/RelayMesh/v2/internal/store"
)

func workPath(item map[string]any) string {
	return fmt.Sprintf("/api/v1/work-items/%d", num(item["id"]))
}

func entries(t *testing.T, res wiredResp, key string) []map[string]any {
	t.Helper()
	raw, ok := res.Body[key].([]any)
	if !ok {
		t.Fatalf("响应里应有数组 %s: %d %s", key, res.Code, res.Raw)
	}
	out := make([]map[string]any, 0, len(raw))
	for _, r := range raw {
		out = append(out, r.(map[string]any))
	}
	return out
}

// 在已完成的任务上发起改进需求：任务回到进行中并标为「改进」阶段，需求记在时间线上；
// 同时把这段话交给工作流里的智能体——不在等待就进收件箱排队，正在等回复就是这一轮的回复。
func TestWorkItemRESTImprove(t *testing.T) {
	srv, _ := newConvergenceServer(t, nil)
	jwt := wiredLogin(t, srv)
	auth := []string{"Authorization", "Bearer " + jwt}
	ctx := context.Background()

	res := wiredCall(srv, "POST", "/api/v1/work-items", map[string]any{"workflow_id": "wf-improve", "title": "导出 CSV", "state": "done", "stage": "dev"}, auth...)
	item := itemOf(t, res)
	path := workPath(item)
	improve := path + "/improve"

	// 参数校验：空 / 超长 / 不存在的任务
	if r := wiredCall(srv, "POST", improve, map[string]any{"text": "   "}, auth...); r.Code != http.StatusBadRequest {
		t.Fatalf("没写内容应 400: %d %s", r.Code, r.Raw)
	}
	if r := wiredCall(srv, "POST", improve, map[string]any{"text": strings.Repeat("改", model.WorkImproveMax+1)}, auth...); r.Code != http.StatusBadRequest || !strings.Contains(r.str("error"), "最长 450") {
		t.Fatalf("超长应 400 并点明上限: %d %s", r.Code, r.Raw)
	}
	if r := wiredCall(srv, "POST", "/api/v1/work-items/99999/improve", map[string]any{"text": "x"}, auth...); r.Code != http.StatusNotFound {
		t.Fatalf("任务不存在应 404: %d %s", r.Code, r.Raw)
	}
	if r := wiredCall(srv, "POST", improve, map[string]any{"text": "x"}); r.Code != http.StatusUnauthorized {
		t.Fatalf("未登录应 401: %d", r.Code)
	}
	if got := itemOf(t, wiredCall(srv, "GET", path, nil, auth...)); got["state"] != "done" {
		t.Fatalf("校验失败时任务不应被改动: %v", got["state"])
	}

	// 1) 智能体当前不在等待：重新开工 + 收件箱排队
	res = wiredCall(srv, "POST", improve, map[string]any{"text": "  Excel 打开中文乱码，需要带 BOM  "}, auth...)
	if res.Code != http.StatusOK || res.Body["delivered"] != "queued" {
		t.Fatalf("没有等待中的会话时应排队: %d %s", res.Code, res.Raw)
	}
	got := itemOf(t, res)
	if got["state"] != "doing" || got["stage"] != "improve" || got["closed_at"] != nil {
		t.Fatalf("应回到进行中、改进阶段、清掉关闭时间: %v", got)
	}
	top := got["updates"].([]any)[0].(map[string]any)
	if top["text"] != "改进需求：Excel 打开中文乱码，需要带 BOM" || top["from_state"] != "done" || top["to_state"] != "doing" || top["actor_kind"] != "human" {
		t.Fatalf("需求应记在时间线上（去掉首尾空白）: %v", top)
	}
	inbox := wiredCall(srv, "GET", "/api/v1/workflows/wf-improve/inbox", nil, auth...)
	rawItems, _ := inbox.Body["items"].([]any)
	if len(rawItems) != 1 {
		t.Fatalf("收件箱里应有 1 条排队的追加: %s", inbox.Raw)
	}
	q := rawItems[0].(map[string]any)
	if q["response_text"] != "改进需求 #1「导出 CSV」：Excel 打开中文乱码，需要带 BOM" || q["state"] != "queued" {
		t.Fatalf("消息应自带编号与标题，智能体不必先翻清单: %v", q)
	}

	// 2) 已经在改进中的任务再发起一次：状态与阶段都没变，只多记一条时间线，不会出现「进行中 → 进行中」
	res = wiredCall(srv, "POST", improve, map[string]any{"text": "再加一个导出进度条"}, auth...)
	if res.Code != http.StatusOK || res.Body["delivered"] != "queued" {
		t.Fatalf("再次发起仍应排队: %d %s", res.Code, res.Raw)
	}
	got = itemOf(t, res)
	last := got["updates"].([]any)[0].(map[string]any)
	if got["state"] != "doing" || got["stage"] != "improve" || last["text"] != "改进需求：再加一个导出进度条" || last["from_state"] != nil || last["to_state"] != nil {
		t.Fatalf("重复发起应只追加一条记录、不带状态变化: %v", last)
	}
	if n := len(got["updates"].([]any)); n != 3 {
		t.Fatalf("时间线应是 创建 / 第一次改进 / 第二次改进 共 3 条，实际 %d 条: %v", n, got["updates"])
	}

	// 3) 智能体正在等回复：这段话就是这一轮的回复，不再进收件箱。
	// 另开一个工作流：新会话一创建就会把本工作流里排队的追加当作回复带走，所以不能与上面共用。
	res = wiredCall(srv, "POST", "/api/v1/work-items", map[string]any{"workflow_id": "wf-improve-live", "title": "进度条", "state": "review"}, auth...)
	live := itemOf(t, res)
	sess, err := srv.store.CreateFeedbackSession(ctx, store.CreateSessionInput{SessionID: "sess-improve", WorkflowID: "wf-improve-live", Title: "等回复", Summary: "做完了，请看"})
	if err != nil || sess.Status != "pending" {
		t.Fatalf("应有一个等待回复的会话: %v %+v", err, sess)
	}
	res = wiredCall(srv, "POST", workPath(live)+"/improve", map[string]any{"text": "进度条要显示百分比"}, auth...)
	if res.Code != http.StatusOK || res.Body["delivered"] != "session" {
		t.Fatalf("有等待中的会话时应作为这一轮的回复: %d %s", res.Code, res.Raw)
	}
	replied, err := srv.store.GetFeedbackSession(ctx, sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if replied.Status != "completed" || replied.ResponseText != "改进需求 #1「进度条」：进度条要显示百分比" {
		t.Fatalf("会话应收到这条需求: status=%s text=%q", replied.Status, replied.ResponseText)
	}
	if got := itemOf(t, res); got["state"] != "doing" || got["stage"] != "improve" {
		t.Fatalf("验证中的任务也能直接发起改进: %v", got)
	}
	if live := wiredCall(srv, "GET", "/api/v1/workflows/wf-improve-live/inbox", nil, auth...); len(live.Body["items"].([]any)) != 0 {
		t.Fatalf("作为会话回复交付后，收件箱里不应再有这一条: %s", live.Raw)
	}
}
