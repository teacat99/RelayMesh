package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/teacat99/RelayMesh/v2/internal/model"
)

func saveTestCheckpoint(t *testing.T, srv *Server, workflowID string, content model.CheckpointContent) {
	t.Helper()
	raw, err := json.Marshal(content)
	if err != nil {
		t.Fatal(err)
	}
	cp := &model.WorkflowCheckpoint{WorkflowID: workflowID, ContentJSON: string(raw)}
	if err := srv.store.SaveCheckpoint(context.Background(), cp); err != nil {
		t.Fatalf("保存胶囊: %v", err)
	}
}

func checkpointItems(t *testing.T, res wiredResp) []map[string]any {
	t.Helper()
	rawItems, ok := res.Body["checkpoints"].([]any)
	if !ok {
		t.Fatalf("checkpoints 必须是数组（即使为空）: %s", res.Raw)
	}
	items := make([]map[string]any, 0, len(rawItems))
	for _, it := range rawItems {
		items = append(items, it.(map[string]any))
	}
	return items
}

func TestListWorkflowCheckpointsReadOnlyView(t *testing.T) {
	srv, dbToken := newConvergenceServer(t, nil)
	jwt := wiredLogin(t, srv)
	bearer := []string{"Authorization", "Bearer " + jwt}
	const wf = "wf-capsule-view"

	longText := strings.Repeat("长", 900)
	manyItems := make([]string, 0, 20)
	for i := 0; i < 20; i++ {
		manyItems = append(manyItems, "下一步事项")
	}
	saveTestCheckpoint(t, srv, wf, model.CheckpointContent{
		Objective:   "第一版胶囊",
		NextActions: []string{"继续"},
		Basis:       model.CheckpointBasis{GitCommit: "57bb6d1", AnchorSessionID: "sess-aaaa1111"},
	})
	saveTestCheckpoint(t, srv, wf, model.CheckpointContent{
		Objective:    longText,
		CurrentStage: &model.CheckpointStage{ID: "dev", Status: "in_progress"},
		NextActions:  manyItems,
		Blockers:     []string{"等待用户拍板"},
		Decisions:    []model.CheckpointDecision{{DecisionID: "D-1", Status: "locked", Summary: longText}},
		Basis:        model.CheckpointBasis{GitCommit: "7321232", AnchorSessionID: "sess-bbbb2222"},
	})
	// 一份损坏的内容也不能拖垮整个列表
	if err := srv.store.SaveCheckpoint(context.Background(), &model.WorkflowCheckpoint{WorkflowID: wf, ContentJSON: "{not-json"}); err != nil {
		t.Fatal(err)
	}

	res := wiredCall(srv, "GET", "/api/v1/workflows/"+wf+"/checkpoints", nil, bearer...)
	if res.Code != http.StatusOK {
		t.Fatalf("人类 JWT 应可读取: %d %s", res.Code, res.Raw)
	}
	items := checkpointItems(t, res)
	if len(items) != 3 {
		t.Fatalf("应返回 3 个修订: %s", res.Raw)
	}
	for i, want := range []float64{1, 2, 3} {
		if items[i]["revision"] != want {
			t.Fatalf("必须按修订号升序（从旧到新）: %s", res.Raw)
		}
	}

	first := items[0]
	basis, _ := first["basis"].(map[string]any)
	if basis["git_commit"] != "57bb6d1" || basis["anchor_session_id"] != "sess-aaaa1111" {
		t.Fatalf("应带出基准与关联会话: %#v", first)
	}

	second := items[1]
	if got := len([]rune(second["objective"].(string))); got > 401 {
		t.Fatalf("目标应被截断到 400 字符以内（含省略号），实际 %d", got)
	}
	if next, _ := second["next_actions"].([]any); len(next) != 8 {
		t.Fatalf("下一步最多返回 8 条，实际 %d", len(next))
	}
	decisions, _ := second["decisions"].([]any)
	if len(decisions) != 1 || len([]rune(decisions[0].(map[string]any)["summary"].(string))) > 401 {
		t.Fatalf("决策摘要应被截断: %#v", second["decisions"])
	}
	if stage, _ := second["current_stage"].(map[string]any); stage["id"] != "dev" {
		t.Fatalf("应带出当前阶段: %#v", second)
	}

	if items[2]["objective"] != "" {
		t.Fatalf("损坏的胶囊只保留元信息: %#v", items[2])
	}

	// limit 取最新的 N 个，仍按升序返回
	res = wiredCall(srv, "GET", "/api/v1/workflows/"+wf+"/checkpoints?limit=2", nil, bearer...)
	items = checkpointItems(t, res)
	if len(items) != 2 || items[0]["revision"] != float64(2) || items[1]["revision"] != float64(3) {
		t.Fatalf("limit=2 应返回最新两个修订（升序）: %s", res.Raw)
	}

	// 没有胶囊的工作流：空数组而不是 null
	res = wiredCall(srv, "GET", "/api/v1/workflows/wf-nothing-here/checkpoints", nil, bearer...)
	if res.Code != http.StatusOK || len(checkpointItems(t, res)) != 0 {
		t.Fatalf("无胶囊应返回空数组: %d %s", res.Code, res.Raw)
	}

	// 只读视图同样受 REST 收敛约束：MCP 令牌 403，未登录 401
	if res := wiredCall(srv, "GET", "/api/v1/workflows/"+wf+"/checkpoints", nil, "Authorization", "Bearer "+dbToken); res.Code != http.StatusForbidden {
		t.Fatalf("MCP 令牌必须 403，实际 %d", res.Code)
	}
	if res := wiredCall(srv, "GET", "/api/v1/workflows/"+wf+"/checkpoints", nil); res.Code != http.StatusUnauthorized {
		t.Fatalf("未登录必须 401，实际 %d", res.Code)
	}
}
