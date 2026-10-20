package api

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/teacat99/RelayMesh/v2/internal/model"
	"github.com/teacat99/RelayMesh/v2/internal/store"
)

// 会话列表与当前会话接口要带上「整个工作流」的只读聚合（#159）：待处理确认项数 + 智能体最近动作时间。
// 同一个工作流的多行取值一致；没有记录的不带 agent_last_activity_at，没有确认项的 pending_confirm_count 为 0（不是缺省）。
func TestListSessions_AttachesWorkflowAggregates(t *testing.T) {
	e := newCfEnv(t)
	ctx := context.Background()

	newRound := func(wf string) string {
		t.Helper()
		sess, err := e.srv.store.CreateFeedbackSession(ctx, store.CreateSessionInput{WorkflowID: wf, ProjectDirectory: "/tmp/agg", Summary: "汇报 " + wf, TimeoutSeconds: 60})
		if err != nil {
			t.Fatalf("建会话 %s: %v", wf, err)
		}
		return sess.ID
	}
	// 同一个工作流同时只能有一轮待答复：wf-agg-a 先取消第一轮，再开第二轮，这样列表里它有两行
	first := newRound("wf-agg-a")
	if res := e.call("POST", "/api/v1/sessions/"+first+"/cancel", nil); res.Code != http.StatusOK {
		t.Fatalf("取消第一轮应 200: %d %s", res.Code, res.Raw)
	}
	newRound("wf-agg-a")
	newRound("wf-agg-b")
	e.ask(t, "wf-agg-a", "第一个问题", "L1")
	e.ask(t, "wf-agg-a", "第二个问题", "L1")
	e.ask(t, "wf-agg-a", "第三个问题", "L1")
	if res := e.call("POST", cfBase("wf-agg-a")+"/Q3/dismiss", map[string]any{"reason": "不需要了"}); res.Code != http.StatusOK {
		t.Fatalf("忽略 Q3 应 200: %d %s", res.Code, res.Raw)
	}
	at := time.Now().Add(-90 * time.Second).Truncate(time.Second)
	e.srv.store.RecordAgentActivity("wf-agg-a", at)

	for _, query := range []string{"?brief=true", ""} {
		res := e.call("GET", "/api/v1/sessions"+query, nil)
		if res.Code != http.StatusOK {
			t.Fatalf("会话列表%s应 200: %d %s", query, res.Code, res.Raw)
		}
		rows, _ := res.Body["sessions"].([]any)
		if len(rows) != 3 {
			t.Fatalf("应有 3 轮会话: %d %s", len(rows), res.Raw)
		}
		seenA := 0
		for _, r := range rows {
			row, _ := r.(map[string]any)
			wf, _ := row["workflow_id"].(string)
			count, hasCount := row["pending_confirm_count"].(float64)
			activity, hasActivity := row["agent_last_activity_at"].(string)
			switch wf {
			case "wf-agg-a":
				seenA++
				if !hasCount || int(count) != 2 {
					t.Fatalf("wf-agg-a 应有 2 条待处理（3 条里忽略了 1 条）: %v", row)
				}
				got, err := time.Parse(time.RFC3339Nano, activity)
				if !hasActivity || err != nil || got.Sub(at).Abs() > time.Second {
					t.Fatalf("wf-agg-a 的最近动作时间不对: %q err=%v want≈%s", activity, err, at.Format(time.RFC3339))
				}
			case "wf-agg-b":
				if !hasCount || int(count) != 0 {
					t.Fatalf("wf-agg-b 没有确认项时 pending_confirm_count 应带上且为 0（不是缺省）: %v", row)
				}
				if hasActivity {
					t.Fatalf("wf-agg-b 没有智能体动作记录，不应带 agent_last_activity_at: %v", row)
				}
			default:
				t.Fatalf("意外的工作流 %q", wf)
			}
		}
		if seenA != 2 {
			t.Fatalf("wf-agg-a 应有 2 行: %d", seenA)
		}
	}

	// 当前会话（待答复的最早一轮）同样带聚合，侧栏拿它替换列表里的那一行时不丢数据
	res := e.call("GET", "/api/v1/sessions/current", nil)
	if res.Code != http.StatusOK {
		t.Fatalf("当前会话应 200: %d %s", res.Code, res.Raw)
	}
	cur, _ := res.Body["session"].(map[string]any)
	if cur == nil {
		t.Fatalf("应有当前会话: %s", res.Raw)
	}
	if _, ok := cur["pending_confirm_count"].(float64); !ok {
		t.Fatalf("当前会话也应带 pending_confirm_count: %v", cur)
	}
}

// confirm_updated 事件要带上这个工作流此刻待处理的条数：侧栏里不是当前工作流的会话卡据此直接校准，不必重拉整张列表（#159）。
func TestConfirmUpdatedEventCarriesPendingCount(t *testing.T) {
	e := newCfEnv(t)
	events := watchSSE(t, e.srv)
	e.ask(t, "wf-agg-d", "问题一", "L1")
	e.ask(t, "wf-agg-d", "问题二", "L1")
	events()

	if res := e.call("POST", cfBase("wf-agg-d")+"/Q1/answer", map[string]any{"options": []string{"a"}}); res.Code != http.StatusOK {
		t.Fatalf("作答应 200: %d %s", res.Code, res.Raw)
	}
	got := strings.Join(events(), "")
	if !strings.Contains(got, "event: confirm_updated") || !strings.Contains(got, `"pending_count":1`) {
		t.Fatalf("答复一条后事件应带 pending_count=1: %q", got)
	}

	if res := e.call("POST", cfBase("wf-agg-d")+"/Q2/dismiss", map[string]any{"reason": "不需要了"}); res.Code != http.StatusOK {
		t.Fatalf("忽略应 200: %d %s", res.Code, res.Raw)
	}
	got = strings.Join(events(), "")
	if !strings.Contains(got, `"pending_count":0`) {
		t.Fatalf("全部处理完后事件应带 pending_count=0（0 也要带，前端据此清掉徽标）: %q", got)
	}
}

// 没有工作流编号的会话行不带聚合（前端按「没拿到」处理，退回按本轮状态显示）。
func TestAttachWorkflowAggregates_SkipsRowsWithoutWorkflow(t *testing.T) {
	e := newCfEnv(t)
	e.ask(t, "wf-agg-c", "问题", "L1")
	e.srv.store.RecordAgentActivity("wf-agg-c", time.Now())

	// 直接构造行，避免依赖会话创建时对工作流编号的补全；nil 行也不能 panic
	orphan := &model.FeedbackSession{}
	e.srv.handler.attachWorkflowAggregates(context.Background(), []*model.FeedbackSession{nil, orphan})
	if orphan.PendingConfirmCount != nil || orphan.AgentLastActivityAt != nil || orphan.WorkflowRounds != nil {
		t.Fatalf("没有工作流编号的行不应带聚合: %+v", orphan)
	}
	if orphan.WorkflowID != "" {
		t.Fatalf("不应改写工作流编号: %q", orphan.WorkflowID)
	}
}

// 侧栏与归档库只拿「最新 N 行会话」时，话痨的工作流会把较早的工作流挤出窗口（#173）。
// cover_workflows=true 在窗口之外给每个工作流补一行，并带上整个工作流的总轮数（侧栏「N 轮」徽标用）；
// 不带该参数时行为与以前逐字一致，非 brief 列表也不受影响。
func TestListSessions_CoverWorkflowsKeepsOldWorkflowsVisible(t *testing.T) {
	e := newCfEnv(t)
	ctx := context.Background()

	round := func(wf, dir string, i int) {
		t.Helper()
		id := wf + "-r" + string(rune('a'+i))
		if _, err := e.srv.store.CreateFeedbackSession(ctx, store.CreateSessionInput{SessionID: id, WorkflowID: wf, ProjectDirectory: dir, Summary: "汇报 " + id, TimeoutSeconds: 60}); err != nil {
			t.Fatalf("建会话 %s: %v", id, err)
		}
		if _, err := e.srv.store.SubmitFeedback(ctx, store.SubmitFeedbackInput{SessionID: id, ResponseText: "ok"}); err != nil {
			t.Fatalf("完成会话 %s: %v", id, err)
		}
		time.Sleep(3 * time.Millisecond) // created_at 严格递增，窗口取哪几行可预期
	}
	round("wf-cov-old", "/tmp/cov-old", 0)
	for i := 0; i < 5; i++ {
		round("wf-cov-busy", "/tmp/cov-busy", i)
	}

	list := func(query string) []map[string]any {
		t.Helper()
		res := e.call("GET", "/api/v1/sessions"+query, nil)
		if res.Code != http.StatusOK {
			t.Fatalf("会话列表%s应 200: %d %s", query, res.Code, res.Raw)
		}
		raw, _ := res.Body["sessions"].([]any)
		out := make([]map[string]any, 0, len(raw))
		for _, r := range raw {
			row, _ := r.(map[string]any)
			out = append(out, row)
		}
		return out
	}
	count := func(rows []map[string]any, wf string) int {
		n := 0
		for _, row := range rows {
			if row["workflow_id"] == wf {
				n++
			}
		}
		return n
	}

	// 前提：不带参数时窗口里只有话痨的工作流——这就是「丢失」
	plain := list("?brief=true&limit=3")
	if len(plain) != 3 || count(plain, "wf-cov-old") != 0 {
		t.Fatalf("前提不成立：3 行窗口里应只有 wf-cov-busy: %v", plain)
	}

	covered := list("?brief=true&limit=3&cover_workflows=true")
	if len(covered) != 4 || count(covered, "wf-cov-busy") != 3 || count(covered, "wf-cov-old") != 1 {
		t.Fatalf("应是 3 行窗口 + 窗口外 wf-cov-old 一行: %v", covered)
	}
	for _, row := range covered {
		wf, _ := row["workflow_id"].(string)
		rounds, ok := row["workflow_rounds"].(float64)
		if !ok {
			t.Fatalf("每一行都应带 workflow_rounds: %v", row)
		}
		want := map[string]int{"wf-cov-old": 1, "wf-cov-busy": 5}[wf]
		if int(rounds) != want {
			t.Fatalf("%s 的总轮数应是 %d（不是列表里恰好带出的行数）: %v", wf, want, row)
		}
	}

	// 参数只对 brief 生效：非 brief 列表仍严格按 limit 取行
	if full := list("?limit=3&cover_workflows=true"); len(full) != 3 || count(full, "wf-cov-old") != 0 {
		t.Fatalf("非 brief 列表不应受 cover_workflows 影响: %v", full)
	}
}
