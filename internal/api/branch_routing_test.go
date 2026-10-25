package api

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/teacat99/RelayMesh/v2/internal/model"
	"github.com/teacat99/RelayMesh/v2/internal/store"
)

// 决策路由（#236 P2）的接口层：分支偏好、关掉「定夺」/ 结束分支时请示改由用户定夺并通知、分支流里的转交 / 代答事件、超时升级的后台扫描。
// 路由条件、代答 / 转交 / 越权 / 先到先得等不变量见 internal/store/branch_routing_test.go 与 internal/mcp/tools_branch_routing_test.go。

type askNotifierSpy struct {
	mu   sync.Mutex
	asks []string
}

func (s *askNotifierSpy) NotifyAsk(it *model.ConfirmItem) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.asks = append(s.asks, it.WorkflowID+":"+model.ConfirmRef(it.Seq))
}

func (s *askNotifierSpy) list() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.asks...)
}

func routedAsk(t *testing.T, st *store.Store, bwf, title, risk string) *model.ConfirmItem {
	t.Helper()
	it, created, err := st.CreateConfirmItem(context.Background(), store.ConfirmCreateInput{
		WorkflowID: bwf, Title: title, Risk: risk,
		Options: []model.ConfirmOption{{ID: "a", Label: "甲"}, {ID: "b", Label: "乙"}},
	})
	if err != nil || !created {
		t.Fatalf("CreateConfirmItem: created=%v err=%v", created, err)
	}
	if !it.RoutedToMain() {
		t.Fatalf("分支里的请示应路由给主会话: %+v", it)
	}
	return it
}

func TestBranchPrefsAPI(t *testing.T) {
	srv, _ := branchAPIFixture(t)

	code, out, raw := ntfyCall(t, srv, http.MethodGet, "/api/v1/branch/prefs", nil)
	if code != http.StatusOK {
		t.Fatalf("GET 应 200: %d %s", code, raw)
	}
	p, _ := out["prefs"].(map[string]any)
	if p["l2_guard"] != false || toIntAPI(p["escalate_after_minutes"]) != 30 || toIntAPI(p["default_escalate_minutes"]) != 30 ||
		toIntAPI(p["min_escalate_minutes"]) != 5 || toIntAPI(p["max_escalate_minutes"]) != 1440 {
		t.Fatalf("默认偏好不对: %v", p)
	}

	code, out, raw = ntfyCall(t, srv, http.MethodPut, "/api/v1/branch/prefs", map[string]any{"l2_guard": true, "escalate_minutes": 45})
	if code != http.StatusOK {
		t.Fatalf("PUT 应 200: %d %s", code, raw)
	}
	p, _ = out["prefs"].(map[string]any)
	if p["l2_guard"] != true || toIntAPI(p["escalate_after_minutes"]) != 45 || toIntAPI(p["escalate_minutes"]) != 45 {
		t.Fatalf("更新后偏好不对: %v", p)
	}
	// 局部修改：只改开关，时限保持
	_, out, _ = ntfyCall(t, srv, http.MethodPut, "/api/v1/branch/prefs", map[string]any{"l2_guard": false})
	if p, _ = out["prefs"].(map[string]any); p["l2_guard"] != false || toIntAPI(p["escalate_after_minutes"]) != 45 {
		t.Fatalf("局部修改应保持其它字段: %v", p)
	}
	// 非法：400 并点明字段；库里保持原样
	for _, bad := range []any{3, 1441, -5} {
		if code, _, raw := ntfyCall(t, srv, http.MethodPut, "/api/v1/branch/prefs", map[string]any{"escalate_minutes": bad}); code != http.StatusBadRequest || !strings.Contains(raw, "escalate_minutes") {
			t.Fatalf("escalate_minutes=%v 应 400: %d %s", bad, code, raw)
		}
	}
	if code, _, raw := ntfyCall(t, srv, http.MethodPut, "/api/v1/branch/prefs", map[string]any{}); code != http.StatusBadRequest || !strings.Contains(raw, "没有要修改的字段") {
		t.Fatalf("空请求应 400: %d %s", code, raw)
	}
	_, out, _ = ntfyCall(t, srv, http.MethodGet, "/api/v1/branch/prefs", nil)
	if p, _ = out["prefs"].(map[string]any); toIntAPI(p["escalate_after_minutes"]) != 45 {
		t.Fatalf("校验失败不应改动库: %v", p)
	}
	// 0 = 恢复默认
	_, out, _ = ntfyCall(t, srv, http.MethodPut, "/api/v1/branch/prefs", map[string]any{"escalate_minutes": 0})
	if p, _ = out["prefs"].(map[string]any); toIntAPI(p["escalate_after_minutes"]) != 30 {
		t.Fatalf("0 应恢复默认: %v", p)
	}
}

func toIntAPI(v any) int {
	if f, ok := v.(float64); ok {
		return int(f)
	}
	return -1
}

func TestUpdateBranch_DecideOffEscalatesRoutedAsksAndNotifies(t *testing.T) {
	srv, st := branchAPIFixture(t)
	spy := &askNotifierSpy{}
	srv.handler.SetAskNotifier(spy)
	bwf, _ := newBranchViaAPI(t, srv, "路由分支", nil)
	it := routedAsk(t, st, bwf, "关掉定夺前的请示", "L1")
	sse := listenSSE(t, srv)

	// 改成仍开放定夺的预设：不动请示
	if code, _, raw := ntfyCall(t, srv, http.MethodPut, "/api/v1/workflows/"+bwf+"/branch", map[string]any{"expose_preset": "managed"}); code != http.StatusOK {
		t.Fatalf("PUT 应 200: %d %s", code, raw)
	}
	if got := spy.list(); len(got) != 0 {
		t.Fatalf("decide 仍开不应通知: %v", got)
	}

	// 关掉定夺：请示立即转交给用户，并按新请示通知
	if code, _, raw := ntfyCall(t, srv, http.MethodPut, "/api/v1/workflows/"+bwf+"/branch", map[string]any{"expose_preset": "visible"}); code != http.StatusOK {
		t.Fatalf("PUT 应 200: %d %s", code, raw)
	}
	if got := spy.list(); len(got) != 1 || got[0] != bwf+":"+model.ConfirmRef(it.Seq) {
		t.Fatalf("应按新请示通知用户: %v", got)
	}
	joined := strings.Join(drainSSE(sse), "\n")
	if !strings.Contains(joined, "confirm_updated") || !strings.Contains(joined, bwf) {
		t.Fatalf("应广播 confirm_updated:\n%s", joined)
	}
	got, err := st.GetConfirmItemByRef(context.Background(), bwf, model.ConfirmRef(it.Seq))
	if err != nil || got.RoutedToMain() || got.EscalatedAt == nil {
		t.Fatalf("请示应已转交给用户: %+v %v", got, err)
	}
	// /branches 里待定夺数归零
	_, out, _ := ntfyCall(t, srv, http.MethodGet, "/api/v1/branches", nil)
	for _, r := range out["branches"].([]any) {
		if b := r.(map[string]any); b["branch_workflow_id"] == bwf && b["pending_asks"] != nil {
			t.Fatalf("转交后不应再有待定夺数: %v", b)
		}
	}
}

func TestCloseBranch_APIEscalatesRoutedAsksAndNotifies(t *testing.T) {
	srv, st := branchAPIFixture(t)
	spy := &askNotifierSpy{}
	srv.handler.SetAskNotifier(spy)
	bwf, _ := newBranchViaAPI(t, srv, "要结束的分支", nil)
	routedAsk(t, st, bwf, "结束前的请示", "L1")

	// 结束前 /branches 带待定夺数
	_, out, _ := ntfyCall(t, srv, http.MethodGet, "/api/v1/branches", nil)
	var seen bool
	for _, r := range out["branches"].([]any) {
		if b := r.(map[string]any); b["branch_workflow_id"] == bwf {
			seen = toIntAPI(b["pending_asks"]) == 1
		}
	}
	if !seen {
		t.Fatalf("/branches 应带 pending_asks=1: %v", out)
	}
	if code, _, raw := ntfyCall(t, srv, http.MethodPost, "/api/v1/workflows/"+bwf+"/branch/close", nil); code != http.StatusOK {
		t.Fatalf("结束应 200: %d %s", code, raw)
	}
	if got := spy.list(); len(got) != 1 {
		t.Fatalf("结束时被转交的请示应通知用户一次: %v", got)
	}
	// 幂等：再结束一次不重复通知
	ntfyCall(t, srv, http.MethodPost, "/api/v1/workflows/"+bwf+"/branch/close", nil)
	if got := spy.list(); len(got) != 1 {
		t.Fatalf("重复结束不应重复通知: %v", got)
	}
}

func TestBranchFeedAPI_RoutingEvents(t *testing.T) {
	srv, st := branchAPIFixture(t)
	ctx := context.Background()
	bwf, _ := newBranchViaAPI(t, srv, "事件分支", nil)
	q1 := routedAsk(t, st, bwf, "转交的请示", "L1")
	q2 := routedAsk(t, st, bwf, "代答的请示", "L1")
	q3 := routedAsk(t, st, bwf, "用户自己答的请示", "L1")
	if _, err := st.BranchEscalate(ctx, store.BranchEscalateInput{Main: branchMain, Branch: bwf, Ref: model.ConfirmRef(q1.Seq), Note: "涉及预算"}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.BranchAnswer(ctx, store.BranchAnswerInput{Main: branchMain, Branch: bwf, Ref: model.ConfirmRef(q2.Seq), OptionIDs: []string{"a"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.AnswerConfirmItem(ctx, bwf, model.ConfirmRef(q3.Seq), store.ConfirmAnswerInput{OptionIDs: []string{"b"}}); err != nil {
		t.Fatal(err)
	}

	for _, wf := range []string{branchMain, bwf} { // 主会话页与分支页都看得到
		code, out, raw := ntfyCall(t, srv, http.MethodGet, "/api/v1/workflows/"+wf+"/branch-feed", nil)
		if code != http.StatusOK {
			t.Fatalf("branch-feed 应 200: %d %s", code, raw)
		}
		feed, _ := out["branch_feed"].(map[string]any)
		var escalated, answered, other int
		routed := map[string]bool{}
		for _, r := range feed["items"].([]any) {
			it := r.(map[string]any)
			if txt, _ := it["text"].(string); it["kind"] == "message" && strings.Contains(txt, "有一项请示在等你定夺") {
				// 路由请示写进主会话收件箱的那一行是给智能体看的（带工具用法），不能在分支流里当「分支说的话」列出
				t.Fatalf("%s 的分支流里不应有路由请示的消息行（它是事件行）: %v", wf, it)
			}
			switch it["event"] {
			case "ask_routed":
				ref, _ := it["ref"].(string)
				if routed[ref] || it["branch"] != bwf || it["ref_title"] == "" {
					t.Fatalf("请示送出事件重复或内容不对: %v", it)
				}
				routed[ref] = true
			case "escalated":
				escalated++
				if it["ref"] != model.ConfirmRef(q1.Seq) || it["text"] != "涉及预算" || it["by"] != "agent:"+branchMain || it["ref_title"] != "转交的请示" || it["branch"] != bwf {
					t.Fatalf("转交事件内容不对: %v", it)
				}
			case "main_answered":
				answered++
				if it["ref"] != model.ConfirmRef(q2.Seq) || it["by"] != "agent:"+branchMain {
					t.Fatalf("代答事件内容不对: %v", it)
				}
			default:
				if r, _ := it["ref"].(string); r == model.ConfirmRef(q3.Seq) {
					other++
				}
			}
		}
		if escalated != 1 || answered != 1 || other != 0 {
			t.Fatalf("%s 的分支流应有 1 条转交、1 条代答事件（用户自己答的不算）: escalated=%d answered=%d other=%d", wf, escalated, answered, other)
		}
		// 三条请示都路由过（转交的、代答的、用户在主会话答复前自己答的）：各一条「请示送出」事件
		for _, q := range []*model.ConfirmItem{q1, q2, q3} {
			if !routed[model.ConfirmRef(q.Seq)] {
				t.Fatalf("%s 的分支流缺 %s 的请示送出事件: %v", wf, model.ConfirmRef(q.Seq), routed)
			}
		}
	}
}

func TestBranchEscalator_ScanEscalatesNotifiesAndIsIdempotent(t *testing.T) {
	srv, st := branchAPIFixture(t)
	ctx := context.Background()
	bwf, _ := newBranchViaAPI(t, srv, "超时分支", nil)
	it := routedAsk(t, st, bwf, "没人答的请示", "L1")

	var mu sync.Mutex
	var got []model.ConfirmItem
	esc := newBranchEscalator(st, 10, func(items []model.ConfirmItem) {
		mu.Lock()
		defer mu.Unlock()
		got = append(got, items...)
	})
	if esc.unit != 10*time.Millisecond {
		t.Fatalf("unit 应按毫秒换算: %v", esc.unit)
	}
	// 还没到时限（默认 30 分钟 = 300ms）：什么都不动
	if n := esc.scan(ctx); n != 0 {
		t.Fatalf("未超时不应升级: %d", n)
	}
	esc.now = func() time.Time { return time.Now().Add(time.Hour) }
	if n := esc.scan(ctx); n != 1 {
		t.Fatalf("超时应升级 1 条: %d", n)
	}
	mu.Lock()
	if len(got) != 1 || got[0].Seq != it.Seq || got[0].EscalatedAt == nil {
		t.Fatalf("回调应拿到被升级的请示: %+v", got)
	}
	mu.Unlock()
	row, _ := st.GetConfirmItemByRef(ctx, bwf, model.ConfirmRef(it.Seq))
	v := model.NewConfirmItemView(row)
	if v.Escalation == nil || v.Escalation.By != "timeout" || !strings.Contains(v.Escalation.Note, "主会话 30 分钟没有答复") {
		t.Fatalf("升级说明应写明时限: %+v", v.Escalation)
	}
	if n := esc.scan(ctx); n != 0 {
		t.Fatalf("重复扫描应幂等: %d", n)
	}
	// 时限跟随偏好：改成 5 分钟，新请示在 6 个「分钟」后就升级
	if _, err := st.UpdateBranchPrefs(ctx, func(cur model.BranchPrefs) (model.BranchPrefs, error) { cur.EscalateMinutes = 5; return cur, nil }); err != nil {
		t.Fatal(err)
	}
	routedAsk(t, st, bwf, "第二条", "L1")
	esc.now = func() time.Time { return time.Now().Add(80 * time.Millisecond) } // 5 分钟 = 50ms
	if n := esc.scan(ctx); n != 1 {
		t.Fatalf("按 5 分钟时限应升级第二条: %d", n)
	}
	row2, _ := st.GetConfirmItemByRef(ctx, bwf, "Q2")
	if v2 := model.NewConfirmItemView(row2); v2.Escalation == nil || !strings.Contains(v2.Escalation.Note, "主会话 5 分钟没有答复") {
		t.Fatalf("说明应写偏好里的时限: %+v", v2.Escalation)
	}
}

func TestBranchEscalator_LoopRunsAndStops(t *testing.T) {
	srv, st := branchAPIFixture(t)
	ctx := context.Background()
	bwf, _ := newBranchViaAPI(t, srv, "循环分支", nil)
	if _, err := st.UpdateBranchPrefs(ctx, func(cur model.BranchPrefs) (model.BranchPrefs, error) { cur.EscalateMinutes = 5; return cur, nil }); err != nil {
		t.Fatal(err)
	}
	routedAsk(t, st, bwf, "等着被升级", "L1")

	done := make(chan struct{}, 1)
	esc := newBranchEscalator(st, 10, func(items []model.ConfirmItem) {
		select {
		case done <- struct{}{}:
		default:
		}
	})
	esc.start()
	esc.start() // 重复启动无副作用
	defer esc.stop()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("后台循环应在时限（5×10ms）过后把请示升级")
	}
	esc.stop()
	esc.stop() // 重复停止无副作用
	newBranchEscalator(nil, 0, nil).start()
	var nilEsc *branchEscalator
	nilEsc.start()
	nilEsc.stop()
}

func TestBranchEscalator_SurvivesPanics(t *testing.T) {
	srv, st := branchAPIFixture(t)
	ctx := context.Background()
	bwf, _ := newBranchViaAPI(t, srv, "panic 分支", nil)
	routedAsk(t, st, bwf, "触发回调", "L1")
	esc := newBranchEscalator(st, 10, func([]model.ConfirmItem) { panic("回调炸了") })
	esc.now = func() time.Time { return time.Now().Add(time.Hour) }
	esc.safeScan(ctx) // 不应把 panic 带出来
}

// 后台超时升级没有任何页面操作可以触发刷新：notifyEscalatedAsks 对每个分支补一次 branch_changed（带主会话编号），
// 主会话页的分支流据此补上「请示已转交」事件行；同一个分支的多条请示只发一次。
func TestNotifyEscalatedAsks_BroadcastsBranchChanged(t *testing.T) {
	srv, st := branchAPIFixture(t)
	ctx := context.Background()
	spy := &askNotifierSpy{}
	srv.handler.SetAskNotifier(spy)
	bwf, _ := newBranchViaAPI(t, srv, "广播分支", nil)
	routedAsk(t, st, bwf, "第一条", "L1")
	routedAsk(t, st, bwf, "第二条", "L1")
	items, err := st.EscalateOverdueBranchAsks(ctx, time.Now().Add(time.Hour), time.Minute, "到点了", 10)
	if err != nil || len(items) != 2 {
		t.Fatalf("应升级两条: %d %v", len(items), err)
	}
	sse := listenSSE(t, srv)
	srv.handler.notifyEscalatedAsks(items)

	joined := strings.Join(drainSSE(sse), "\n")
	if n := strings.Count(joined, "confirm_updated"); n < 2 {
		t.Fatalf("每条请示都应刷新确认卡: %d\n%s", n, joined)
	}
	if n := strings.Count(joined, `"change":"ask_escalated"`); n != 1 {
		t.Fatalf("同一个分支只发一次 branch_changed: %d\n%s", n, joined)
	}
	if !strings.Contains(joined, `"parent":"`+branchMain+`"`) || !strings.Contains(joined, `"branch":"`+bwf+`"`) {
		t.Fatalf("branch_changed 应带主会话与分支编号:\n%s", joined)
	}
	if got := spy.list(); len(got) != 2 {
		t.Fatalf("每条请示都按新请示通知一次: %v", got)
	}
}
