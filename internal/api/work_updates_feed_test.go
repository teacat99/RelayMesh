package api

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/teacat99/RelayMesh/v2/internal/model"
	"github.com/teacat99/RelayMesh/v2/internal/store"
)

// 会话页进展卡的数据源接口：需要登录；只含智能体的更新；since / limit 参数有校验。
func TestWorkflowWorkUpdatesEndpoint(t *testing.T) {
	e := newCfEnv(t)
	ctx := context.Background()
	const wf = "wf-work-updates-rest"
	agent := store.WorkActor{Kind: model.WorkActorAgent}
	human := store.WorkActor{Kind: model.WorkActorHuman}
	doing, review := "doing", "review"

	res, err := e.srv.store.CreateWorkItem(ctx, store.WorkItemInput{WorkflowID: wf, Title: "接口任务"}, agent)
	if err != nil {
		t.Fatalf("建任务失败: %v", err)
	}
	id := res.Item.ID
	time.Sleep(2 * time.Millisecond)
	if _, err := e.srv.store.UpdateWorkItem(ctx, id, store.WorkItemPatch{State: &doing, Note: "开工"}, agent); err != nil {
		t.Fatalf("智能体更新失败: %v", err)
	}
	time.Sleep(2 * time.Millisecond)
	if _, err := e.srv.store.UpdateWorkItem(ctx, id, store.WorkItemPatch{Note: "人加的备注"}, human); err != nil {
		t.Fatalf("人更新失败: %v", err)
	}
	time.Sleep(2 * time.Millisecond)
	if _, err := e.srv.store.UpdateWorkItem(ctx, id, store.WorkItemPatch{State: &review, Conclusion: strPtrForTest("完成")}, agent); err != nil {
		t.Fatalf("智能体提交验收失败: %v", err)
	}

	path := "/api/v1/workflows/" + wf + "/work-updates"
	got := e.call("GET", path, nil)
	if got.Code != http.StatusOK {
		t.Fatalf("应返回 200，实际 %d：%s", got.Code, got.Raw)
	}
	list, _ := got.Body["updates"].([]any)
	// 创建（智能体）+ 开工 + 提交验收 = 3 条；人的备注不在其中
	if len(list) != 3 {
		t.Fatalf("应有 3 条智能体进展，实际 %d：%s", len(list), got.Raw)
	}
	first, _ := list[0].(map[string]any)
	last, _ := list[len(list)-1].(map[string]any)
	if first["to_state"] != "todo" || first["title"] != "接口任务" {
		t.Fatalf("第一条应是创建记录：%v", first)
	}
	if last["to_state"] != "review" || last["actor_kind"] != "agent" {
		t.Fatalf("最后一条应是提交验收：%v", last)
	}

	// since 之后没有更新 → 空数组而不是 null
	future := time.Now().Add(time.Hour).UTC().Format(time.RFC3339Nano)
	empty := e.call("GET", path+"?since="+future, nil)
	if l, ok := empty.Body["updates"].([]any); !ok || len(l) != 0 {
		t.Fatalf("since 在未来应返回空数组：%s", empty.Raw)
	}

	if bad := e.call("GET", path+"?since=昨天", nil); bad.Code != http.StatusBadRequest {
		t.Fatalf("无效 since 应 400，实际 %d", bad.Code)
	}
	if bad := e.call("GET", path+"?limit=0", nil); bad.Code != http.StatusBadRequest {
		t.Fatalf("无效 limit 应 400，实际 %d", bad.Code)
	}
	if anon := wiredCall(e.srv, "GET", path, nil); anon.Code != http.StatusUnauthorized {
		t.Fatalf("未登录应 401，实际 %d", anon.Code)
	}
}

func strPtrForTest(s string) *string { return &s }
