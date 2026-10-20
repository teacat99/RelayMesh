package api

import (
	"archive/zip"
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/teacat99/RelayMesh/v2/internal/model"
	"github.com/teacat99/RelayMesh/v2/internal/store"
)

const cfWF = "wf-confirm-rest"

// cfEnv 是确认项 REST 测试用的完整装配环境：登录 JWT（Web 用户）+ 一把 MCP 凭据库令牌（智能体）。
type cfEnv struct {
	srv  *Server
	auth []string
	cred string
}

func newCfEnv(t *testing.T) *cfEnv {
	t.Helper()
	srv, cred := newConvergenceServer(t, nil)
	return &cfEnv{srv: srv, auth: []string{"Authorization", "Bearer " + wiredLogin(t, srv)}, cred: cred}
}

func (e *cfEnv) call(method, path string, body any) wiredResp {
	return wiredCall(e.srv, method, path, body, e.auth...)
}

func cfOptions() []model.ConfirmOption {
	return []model.ConfirmOption{
		{ID: "a", Label: "方案 A", Recommended: true},
		{ID: "b", Label: "方案 B"},
	}
}

func cfBase(wf string) string { return "/api/v1/workflows/" + wf + "/confirms" }

// ask 直接在库里建一条智能体提的确认项（智能体经 MCP async_ask 提问的效果）。
func (e *cfEnv) ask(t *testing.T, wf, title, risk string) *model.ConfirmItem {
	t.Helper()
	item, created, err := e.srv.store.CreateConfirmItem(context.Background(), store.ConfirmCreateInput{
		WorkflowID: wf, Title: title, Options: cfOptions(), Risk: risk, AllowNote: true, CreatedBy: model.ConfirmCreatedByAgent,
	})
	if err != nil || !created {
		t.Fatalf("建确认项 %q: created=%v err=%v", title, created, err)
	}
	return item
}

func (e *cfEnv) status(t *testing.T, wf, ref string) string {
	t.Helper()
	item, err := e.srv.store.GetConfirmItemByRef(context.Background(), wf, ref)
	if err != nil {
		t.Fatalf("读取 %s: %v", ref, err)
	}
	return item.Status
}

// answerMessages 返回工作流收件箱里的「确认项答复」系统消息。
func (e *cfEnv) answerMessages(t *testing.T, wf string) []model.QueuedFeedback {
	t.Helper()
	var rows []model.QueuedFeedback
	if err := e.srv.store.DB().Where("workflow_id = ? AND kind = ?", wf, model.InboxKindAnswer).Order("id ASC").Find(&rows).Error; err != nil {
		t.Fatalf("读取答复消息: %v", err)
	}
	return rows
}

func cfItem(t *testing.T, res wiredResp) map[string]any {
	t.Helper()
	item, ok := res.Body["item"].(map[string]any)
	if !ok {
		t.Fatalf("响应里没有 item: %d %s", res.Code, res.Raw)
	}
	return item
}

func cfRefs(items []map[string]any) []string {
	out := make([]string, 0, len(items))
	for _, it := range items {
		s, _ := it["ref"].(string)
		out = append(out, s)
	}
	return out
}

func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// 确认项接口只给登录后的 Web 用户：匿名 401；任何 MCP 令牌（含智能体的凭据）403——智能体不能替用户作答。
func TestConfirmsREST_AuthBoundaries(t *testing.T) {
	e := newCfEnv(t)
	e.ask(t, cfWF, "要不要上线", "L1")
	base := cfBase(cfWF)
	routes := []struct {
		method, path string
		body         any
	}{
		{"GET", base, nil},
		{"GET", base + "/board", nil},
		{"GET", base + "/Q1", nil},
		{"POST", base + "/Q1/answer", map[string]any{"options": []string{"a"}}},
		{"POST", base + "/Q1/dismiss", map[string]any{"reason": "不需要了"}},
		{"POST", base + "/answer-batch", map[string]any{"adopt_recommended": true, "items": []map[string]any{{"ref": "Q1"}}}},
	}
	tokens := map[string]string{"环境变量 MCP 令牌": wiredMCPToken, "凭据库令牌": e.cred}
	jwt := strings.TrimPrefix(e.auth[1], "Bearer ")
	for _, rt := range routes {
		if res := wiredCall(e.srv, rt.method, rt.path, rt.body); res.Code != http.StatusUnauthorized {
			t.Errorf("匿名 %s %s 应 401，实际 %d %s", rt.method, rt.path, res.Code, res.Raw)
		}
		for name, tok := range tokens {
			res := wiredCall(e.srv, rt.method, rt.path, rt.body, "Authorization", "Bearer "+tok)
			if res.Code != http.StatusForbidden || res.str("code") != "mcp_token_forbidden" {
				t.Errorf("%s 访问 %s %s 应 403 mcp_token_forbidden，实际 %d %s", name, rt.method, rt.path, res.Code, res.Raw)
			}
		}
		// 令牌放在 URL 上不被接受
		sep := "?"
		if strings.Contains(rt.path, "?") {
			sep = "&"
		}
		if res := wiredCall(e.srv, rt.method, rt.path+sep+"token="+url.QueryEscape(jwt), rt.body); res.Code != http.StatusUnauthorized {
			t.Errorf("URL 令牌访问 %s %s 必须 401，实际 %d", rt.method, rt.path, res.Code)
		}
	}
	// 被拒绝的请求什么都没改：确认项仍待处理，收件箱没有任何答复。
	if got := e.status(t, cfWF, "Q1"); got != model.ConfirmStatusPending {
		t.Fatalf("被拒绝的请求不应改变确认项: %s", got)
	}
	if n := len(e.answerMessages(t, cfWF)); n != 0 {
		t.Fatalf("被拒绝的请求不应写入答复: %d", n)
	}
	// 登录后的 Web 用户可以读。
	if res := e.call("GET", base, nil); res.Code != http.StatusOK {
		t.Fatalf("登录后应能读列表: %d %s", res.Code, res.Raw)
	}
}

func TestConfirmsREST_ListGetAndBoard(t *testing.T) {
	e := newCfEnv(t)
	task := itemOf(t, e.call("POST", "/api/v1/work-items", map[string]any{"workflow_id": cfWF, "title": "上线任务", "state": "doing"}))
	itemOf(t, e.call("POST", "/api/v1/work-items", map[string]any{"workflow_id": cfWF, "title": "验证中的任务", "state": "review"}))
	taskSeq := num(task["seq"])

	e.ask(t, cfWF, "甲：低风险", "L0")
	e.ask(t, cfWF, "乙：不可逆", "L2")
	plan, _, err := e.srv.store.CreateConfirmItem(context.Background(), store.ConfirmCreateInput{
		WorkflowID: cfWF, Kind: model.ConfirmKindPlan, Title: "丙：方案确认", Options: cfOptions(), Risk: "L1", TaskSeq: taskSeq,
		CreatedBy: model.ConfirmCreatedByAgent,
	})
	if err != nil {
		t.Fatal(err)
	}
	e.ask(t, "wf-other", "别的工作流的确认项", "L1")

	base := cfBase(cfWF)
	// 默认新的在前；asc 旧的在前；其他工作流的不混进来
	res := e.call("GET", base, nil)
	if got := cfRefs(entries(t, res, "items")); !sameStrings(got, []string{"Q3", "Q2", "Q1"}) {
		t.Fatalf("默认应新的在前且只含本工作流: %v %s", got, res.Raw)
	}
	res = e.call("GET", base+"?order=asc", nil)
	if got := cfRefs(entries(t, res, "items")); !sameStrings(got, []string{"Q1", "Q2", "Q3"}) {
		t.Fatalf("asc 应旧的在前: %v", got)
	}
	res = e.call("GET", base+"?kind=plan&task_seq="+strconv.Itoa(taskSeq), nil)
	if got := cfRefs(entries(t, res, "items")); !sameStrings(got, []string{"Q3"}) {
		t.Fatalf("按类型 + 任务筛选: %v", got)
	}
	// 没有命中时返回空数组而不是 null（前端直接 .map）
	res = e.call("GET", base+"?status=answered", nil)
	if got := entries(t, res, "items"); len(got) != 0 {
		t.Fatalf("尚无已答复项: %v", got)
	}
	res = e.call("GET", base+"?limit=2", nil)
	if got := entries(t, res, "items"); len(got) != 2 {
		t.Fatalf("limit=2: %d", len(got))
	}

	// 单条：Q 编号和纯数字都认；其他工作流 / 不存在 → 404；写法不对 → 400
	if it := cfItem(t, e.call("GET", base+"/Q2", nil)); it["ref"] != "Q2" || it["risk"] != "L2" || it["status"] != "pending" {
		t.Fatalf("Q2 内容不对: %v", it)
	}
	if it := cfItem(t, e.call("GET", base+"/2", nil)); it["ref"] != "Q2" {
		t.Fatalf("纯数字也应能定位: %v", it)
	}
	if it := cfItem(t, e.call("GET", base+"/q3", nil)); it["ref"] != "Q3" || num(it["task_seq"]) != taskSeq || it["kind"] != "plan" {
		t.Fatalf("小写编号 + 任务绑定: %v", it)
	}
	if res := e.call("GET", base+"/Q99", nil); res.Code != http.StatusNotFound {
		t.Fatalf("不存在应 404: %d %s", res.Code, res.Raw)
	}
	if res := e.call("GET", cfBase("wf-other")+"/Q2", nil); res.Code != http.StatusNotFound {
		t.Fatalf("别的工作流里没有 Q2（不能跨工作流读）: %d %s", res.Code, res.Raw)
	}
	if res := e.call("GET", base+"/not-a-ref", nil); res.Code != http.StatusBadRequest {
		t.Fatalf("写法无效应 400: %d %s", res.Code, res.Raw)
	}
	_ = plan

	// 待确认区
	board := e.call("GET", base+"/board", nil)
	if board.Code != http.StatusOK {
		t.Fatalf("board: %d %s", board.Code, board.Raw)
	}
	if got := cfRefs(entries(t, board, "items")); !sameStrings(got, []string{"Q1", "Q2", "Q3"}) {
		t.Fatalf("待确认区应旧的在前: %v", got)
	}
	// 任务不再需要人逐条确认（#110）：验证中的任务不进待答复区；review_tasks 只作为恒空字段留给旧页面
	if got := entries(t, board, "review_tasks"); len(got) != 0 {
		t.Fatalf("验证中的任务不应出现在待答复区: %v", got)
	}
	if got := entries(t, board, "guard_alerts"); len(got) != 0 {
		t.Fatalf("没有守护告警: %v", got)
	}
	counts, _ := board.Body["counts"].(map[string]any)
	byRisk, _ := counts["by_risk"].(map[string]any)
	byKind, _ := counts["by_kind"].(map[string]any)
	if num(counts["pending"]) != 3 || num(counts["items"]) != 3 || num(counts["review_tasks"]) != 0 || num(counts["guard_alerts"]) != 0 {
		t.Fatalf("计数不对: %v", counts)
	}
	if num(byRisk["L0"]) != 1 || num(byRisk["L1"]) != 1 || num(byRisk["L2"]) != 1 || num(byKind["ask"]) != 2 || num(byKind["plan"]) != 1 || num(byKind["decision"]) != 0 {
		t.Fatalf("按风险 / 类型计数不对: %v %v", byRisk, byKind)
	}

	// 答复一条之后：不再出现在待确认区，其他工作流不受影响
	if res := e.call("POST", base+"/Q1/answer", map[string]any{"options": []string{"a"}}); res.Code != http.StatusOK {
		t.Fatalf("作答: %d %s", res.Code, res.Raw)
	}
	board = e.call("GET", base+"/board", nil)
	if c, _ := board.Body["counts"].(map[string]any); num(c["pending"]) != 2 || num(c["items"]) != 2 {
		t.Fatalf("作答后待处理应减少: %v", c)
	}
	other := e.call("GET", cfBase("wf-other")+"/board", nil)
	if c, _ := other.Body["counts"].(map[string]any); num(c["items"]) != 1 {
		t.Fatalf("别的工作流的计数不受影响: %v", c)
	}
}

func TestConfirmsREST_AnswerFlowWritesInboxMessageAndBroadcasts(t *testing.T) {
	e := newCfEnv(t)
	events := watchSSE(t, e.srv)
	e.ask(t, cfWF, "选哪个方案", "L1")
	e.ask(t, cfWF, "再选一个", "L1")
	events()
	base := cfBase(cfWF)

	res := e.call("POST", base+"/Q1/answer", map[string]any{"options": []string{"b"}, "note": "更稳妥"})
	if res.Code != http.StatusOK {
		t.Fatalf("作答应 200: %d %s", res.Code, res.Raw)
	}
	it := cfItem(t, res)
	ans, _ := it["answer"].(map[string]any)
	opts, _ := ans["options"].([]any)
	if it["status"] != "answered" || len(opts) != 1 || opts[0] != "b" || ans["note"] != "更稳妥" || it["answered_at"] == nil {
		t.Fatalf("作答结果不对: %v", it)
	}
	msgs := e.answerMessages(t, cfWF)
	if len(msgs) != 1 || !strings.Contains(msgs[0].ResponseText, "Q1") || !strings.Contains(msgs[0].ResponseText, "方案 B") || !strings.Contains(msgs[0].ResponseText, "更稳妥") {
		t.Fatalf("应往收件箱写一条答复系统消息: %+v", msgs)
	}
	got := strings.Join(events(), "")
	if !strings.Contains(got, "event: confirm_updated") || !strings.Contains(got, "event: queued_feedback_updated") {
		t.Fatalf("作答后应广播确认项与收件箱变化: %q", got)
	}

	// 重复作答 409，不会再写一条消息
	if res := e.call("POST", base+"/Q1/answer", map[string]any{"options": []string{"a"}}); res.Code != http.StatusConflict {
		t.Fatalf("重复作答应 409: %d %s", res.Code, res.Raw)
	}
	// 非法选项 / 没选 / 请求体坏掉 → 400，且确认项保持待处理
	for name, body := range map[string]any{
		"未知选项": map[string]any{"options": []string{"zzz"}},
		"没有选":  map[string]any{"options": []string{}},
		"单选多选": map[string]any{"options": []string{"a", "b"}},
	} {
		if res := e.call("POST", base+"/Q2/answer", body); res.Code != http.StatusBadRequest {
			t.Errorf("%s 应 400: %d %s", name, res.Code, res.Raw)
		}
	}
	if res := wiredCall(e.srv, "POST", base+"/Q2/answer", "不是对象", e.auth...); res.Code != http.StatusBadRequest {
		t.Errorf("请求体不是对象应 400: %d %s", res.Code, res.Raw)
	}
	if got := e.status(t, cfWF, "Q2"); got != model.ConfirmStatusPending {
		t.Fatalf("校验失败不应改变状态: %s", got)
	}
	if n := len(e.answerMessages(t, cfWF)); n != 1 {
		t.Fatalf("失败的请求不应再写消息: %d", n)
	}
	// 别的工作流不能作答本工作流的确认项
	if res := e.call("POST", cfBase("wf-other")+"/Q2/answer", map[string]any{"options": []string{"a"}}); res.Code != http.StatusNotFound {
		t.Fatalf("跨工作流作答应 404: %d %s", res.Code, res.Raw)
	}
	if got := e.status(t, cfWF, "Q2"); got != model.ConfirmStatusPending {
		t.Fatalf("跨工作流请求不应改变状态: %s", got)
	}
}

// 自定义答复（#183）：预设选项都不合适时用户自己写的话，REST 只多一个 custom 字段；
// 单选时与 options 二选一，答复里的 options 是 []（不是 null），收件箱消息带自定义原文；不带 custom 的老式作答完全不变。
func TestConfirmsREST_CustomAnswer(t *testing.T) {
	e := newCfEnv(t)
	e.ask(t, cfWF, "选哪个方案", "L1") // Q1
	e.ask(t, cfWF, "再选一个", "L1")  // Q2
	base := cfBase(cfWF)

	for name, body := range map[string]any{
		"单选同时给选项和自定义": map[string]any{"options": []string{"a"}, "custom": "都不对"},
		"自定义全是空白":     map[string]any{"options": []string{}, "custom": "   "},
	} {
		if res := e.call("POST", base+"/Q1/answer", body); res.Code != http.StatusBadRequest {
			t.Errorf("%s 应 400: %d %s", name, res.Code, res.Raw)
		}
	}
	if got := e.status(t, cfWF, "Q1"); got != model.ConfirmStatusPending {
		t.Fatalf("校验失败不应改变状态: %s", got)
	}

	res := e.call("POST", base+"/Q1/answer", map[string]any{"custom": " 先做灰度 "})
	if res.Code != http.StatusOK {
		t.Fatalf("只写自定义答复应 200: %d %s", res.Code, res.Raw)
	}
	it := cfItem(t, res)
	ans, _ := it["answer"].(map[string]any)
	opts, isArray := ans["options"].([]any)
	if it["status"] != "answered" || !isArray || len(opts) != 0 || ans["custom"] != "先做灰度" {
		t.Fatalf("答复里选项应是空数组、自定义应是规整后的原文: %v", it)
	}
	msgs := e.answerMessages(t, cfWF)
	if len(msgs) != 1 || !strings.Contains(msgs[0].ResponseText, "没有选预设选项") || !strings.Contains(msgs[0].ResponseText, "自定义答复：先做灰度") {
		t.Fatalf("收件箱消息应带自定义答复原文: %+v", msgs)
	}

	// 不带 custom 的老式作答：返回里没有 custom 字段
	res = e.call("POST", base+"/Q2/answer", map[string]any{"options": []string{"a"}})
	if res.Code != http.StatusOK {
		t.Fatalf("老式作答应 200: %d %s", res.Code, res.Raw)
	}
	if ans2, _ := cfItem(t, res)["answer"].(map[string]any); ans2 == nil || ans2["custom"] != nil {
		t.Fatalf("没写自定义答复时返回里不应有 custom: %v", ans2)
	}
}

func TestConfirmsREST_BatchIsAtomicAndRefusesL2(t *testing.T) {
	e := newCfEnv(t)
	e.ask(t, cfWF, "甲", "L0")
	e.ask(t, cfWF, "乙", "L1")
	e.ask(t, cfWF, "丙：不可逆", "L2")
	base := cfBase(cfWF) + "/answer-batch"

	// 带 L2 的批：整批拒绝，L0 / L1 也不保存
	res := e.call("POST", base, map[string]any{"adopt_recommended": true, "items": []map[string]any{{"ref": "Q1"}, {"ref": "Q3"}}})
	if res.Code != http.StatusBadRequest || !strings.Contains(res.str("error"), "L2") {
		t.Fatalf("带 L2 的批量应 400 并说明原因: %d %s", res.Code, res.Raw)
	}
	// 含不存在的项 / 重复项 → 整批失败
	if res := e.call("POST", base, map[string]any{"adopt_recommended": true, "items": []map[string]any{{"ref": "Q1"}, {"ref": "Q99"}}}); res.Code != http.StatusBadRequest {
		t.Fatalf("含不存在项应整批失败: %d %s", res.Code, res.Raw)
	}
	if res := e.call("POST", base, map[string]any{"adopt_recommended": true, "items": []map[string]any{{"ref": "Q1"}, {"ref": "1"}}}); res.Code != http.StatusBadRequest {
		t.Fatalf("同一项重复出现应整批失败: %d %s", res.Code, res.Raw)
	}
	// 没写 options 又没要求采用推荐 → 失败
	if res := e.call("POST", base, map[string]any{"items": []map[string]any{{"ref": "Q1"}}}); res.Code != http.StatusBadRequest {
		t.Fatalf("没有选项也没有采用推荐应失败: %d %s", res.Code, res.Raw)
	}
	// 空批 / 请求体坏掉
	if res := e.call("POST", base, map[string]any{"items": []map[string]any{}}); res.Code != http.StatusBadRequest {
		t.Fatalf("空批应 400: %d %s", res.Code, res.Raw)
	}
	if res := wiredCall(e.srv, "POST", base, "坏的", e.auth...); res.Code != http.StatusBadRequest {
		t.Fatalf("请求体不是对象应 400: %d %s", res.Code, res.Raw)
	}
	for _, ref := range []string{"Q1", "Q2", "Q3"} {
		if got := e.status(t, cfWF, ref); got != model.ConfirmStatusPending {
			t.Fatalf("失败的批量不得保存任何一项: %s=%s", ref, got)
		}
	}
	if n := len(e.answerMessages(t, cfWF)); n != 0 {
		t.Fatalf("失败的批量不得写消息: %d", n)
	}

	// 合格的批：采用推荐，一批只写一条答复消息
	events := watchSSE(t, e.srv)
	res = e.call("POST", base, map[string]any{"adopt_recommended": true, "items": []map[string]any{{"ref": "Q1"}, {"ref": "Q2"}}})
	if res.Code != http.StatusOK {
		t.Fatalf("合格的批量应 200: %d %s", res.Code, res.Raw)
	}
	items := entries(t, res, "items")
	if len(items) != 2 {
		t.Fatalf("应返回两项: %s", res.Raw)
	}
	for _, it := range items {
		ans, _ := it["answer"].(map[string]any)
		opts, _ := ans["options"].([]any)
		if it["status"] != "answered" || len(opts) != 1 || opts[0] != "a" {
			t.Fatalf("应采用推荐项 a: %v", it)
		}
	}
	msgs := e.answerMessages(t, cfWF)
	msg, _ := res.Body["inbox_message"].(string)
	if len(msgs) != 1 || !strings.Contains(msgs[0].ResponseText, "Q1") || !strings.Contains(msgs[0].ResponseText, "Q2") || msg != msgs[0].ResponseText {
		t.Fatalf("一批只应写一条消息，且内容与响应一致: %+v %q", msgs, msg)
	}
	if !strings.Contains(strings.Join(events(), ""), "event: confirm_updated") {
		t.Fatal("批量作答后应广播 confirm_updated")
	}
	if got := e.status(t, cfWF, "Q3"); got != model.ConfirmStatusPending {
		t.Fatalf("L2 项不受批量影响: %s", got)
	}
	// L2 只能逐条确认
	if res := e.call("POST", cfBase(cfWF)+"/Q3/answer", map[string]any{"options": []string{"a"}}); res.Code != http.StatusOK {
		t.Fatalf("L2 逐条作答应 200: %d %s", res.Code, res.Raw)
	}
}

func TestConfirmsREST_DismissAndTerminalStatesAreFinal(t *testing.T) {
	e := newCfEnv(t)
	e.ask(t, cfWF, "要不要做", "L1")
	e.ask(t, cfWF, "另一个", "L1")
	base := cfBase(cfWF)

	res := e.call("POST", base+"/Q1/dismiss", map[string]any{"reason": "这个需求取消了"})
	if res.Code != http.StatusOK || cfItem(t, res)["status"] != "dismissed" {
		t.Fatalf("忽略应 200 且状态 dismissed: %d %s", res.Code, res.Raw)
	}
	msgs := e.answerMessages(t, cfWF)
	if len(msgs) != 1 || !strings.Contains(msgs[0].ResponseText, "Q1") || !strings.Contains(msgs[0].ResponseText, "这个需求取消了") {
		t.Fatalf("忽略应告诉智能体不用再等: %+v", msgs)
	}
	// 终态不可再变
	if res := e.call("POST", base+"/Q1/answer", map[string]any{"options": []string{"a"}}); res.Code != http.StatusConflict {
		t.Fatalf("已忽略的不能再作答: %d %s", res.Code, res.Raw)
	}
	if res := e.call("POST", base+"/Q1/dismiss", nil); res.Code != http.StatusConflict {
		t.Fatalf("已忽略的不能再忽略: %d %s", res.Code, res.Raw)
	}
	// 请求体可以省略
	if res := e.call("POST", base+"/Q2/dismiss", nil); res.Code != http.StatusOK {
		t.Fatalf("省略请求体也应能忽略: %d %s", res.Code, res.Raw)
	}
	if res := e.call("POST", cfBase("wf-other")+"/Q1/dismiss", nil); res.Code != http.StatusNotFound {
		t.Fatalf("跨工作流忽略应 404: %d %s", res.Code, res.Raw)
	}
	// 已忽略的不再出现在待确认区
	board := e.call("GET", base+"/board", nil)
	if c, _ := board.Body["counts"].(map[string]any); num(c["pending"]) != 0 {
		t.Fatalf("全部处理后待确认区应为空: %v", c)
	}
}

// 文档里的决策卡与确认项互通：文档详情带出确认项编号 / 状态 / 答案，在待确认区答复后文档里同步；
// 历史版本不带确认项（只读）；导出的 zip 在文档末尾追加决策结果表。
func TestConfirmsREST_DocDecisionsAreAnsweredThroughConfirmItems(t *testing.T) {
	e := newCfEnv(t)
	ctx := context.Background()
	fence := func(id, title string) string {
		return "```decision\n{\"id\":\"" + id + "\",\"title\":\"" + title + "\",\"note\":true,\"options\":[{\"id\":\"a\",\"label\":\"方案 A\",\"recommended\":true},{\"id\":\"b\",\"label\":\"方案 B\"}]}\n```\n"
	}
	created, err := e.srv.store.CreateProjectDoc(ctx, store.ProjectDocInput{
		WorkflowID: cfWF, Path: "design/choose.md", Title: "评估",
		Body: "# 评估\n\n" + fence("d1", "选哪个") + "\n" + fence("d2", "再选一个"),
	}, store.DocActor{Kind: "human"})
	if err != nil {
		t.Fatal(err)
	}
	docPath := "/api/v1/workflows/" + cfWF + "/docs/content?path=" + url.QueryEscape("design/choose.md")
	decisions := func(path string) []map[string]any {
		res := e.call("GET", path, nil)
		if res.Code != http.StatusOK {
			t.Fatalf("取文档: %d %s", res.Code, res.Raw)
		}
		return entries(t, res, "decisions")
	}

	ds := decisions(docPath)
	if len(ds) != 2 || ds[0]["confirm_ref"] != "Q1" || ds[1]["confirm_ref"] != "Q2" || ds[0]["status"] != "pending" {
		t.Fatalf("保存文档后决策卡应各有一个待作答的确认项: %v", ds)
	}
	// 文档里的决策卡同样出现在待确认区，并带上文档位置
	board := e.call("GET", cfBase(cfWF)+"/board", nil)
	items := entries(t, board, "items")
	if len(items) != 2 || items[0]["kind"] != "decision" || items[0]["doc_path"] != "design/choose.md" || items[0]["doc_key"] != "d1" {
		t.Fatalf("待确认区应含文档决策项并带文档信息: %s", board.Raw)
	}

	// 在待确认区答复 → 文档里同步成已作答
	if res := e.call("POST", cfBase(cfWF)+"/Q1/answer", map[string]any{"options": []string{"b"}, "note": "先稳"}); res.Code != http.StatusOK {
		t.Fatalf("作答: %d %s", res.Code, res.Raw)
	}
	ds = decisions(docPath)
	ans, _ := ds[0]["answer"].(map[string]any)
	opts, _ := ans["options"].([]any)
	if ds[0]["status"] != "answered" || len(opts) != 1 || opts[0] != "b" || ds[1]["status"] != "pending" {
		t.Fatalf("作答应反映到文档的决策视图: %v", ds)
	}

	// 导出：zip 里的文档末尾带决策结果表（结果含选项文案与备注）
	zres := httptest.NewRecorder()
	e.srv.Engine().ServeHTTP(zres, newAuthedRequest(t, e, "GET", "/api/v1/workflows/"+cfWF+"/docs/export.zip"))
	if zres.Code != http.StatusOK {
		t.Fatalf("导出: %d %s", zres.Code, zres.Body.String())
	}
	zr, err := zip.NewReader(bytes.NewReader(zres.Body.Bytes()), int64(zres.Body.Len()))
	if err != nil {
		t.Fatal(err)
	}
	var exported string
	for _, f := range zr.File {
		if f.Name == "design/choose.md" {
			rc, oerr := f.Open()
			if oerr != nil {
				t.Fatal(oerr)
			}
			raw, _ := io.ReadAll(rc)
			rc.Close()
			exported = string(raw)
		}
	}
	for _, want := range []string{"## 决策结果", "| d1 | 选哪个 | answered | Q1 | b（方案 B）；备注：先稳 |", "| d2 | 再选一个 | pending | Q2 | 待作答 |"} {
		if !strings.Contains(exported, want) {
			t.Fatalf("导出的文档缺少 %q:\n%s", want, exported)
		}
	}

	// 改文档生成第 2 版；读第 1 版是历史快照：只读，不带确认项编号
	body2 := "# 评估（改）\n\n" + fence("d1", "选哪个") + "\n" + fence("d2", "再选一个")
	if _, err := e.srv.store.UpdateProjectDoc(ctx, created.Doc.ID, store.ProjectDocPatch{Body: &body2}, store.DocActor{Kind: "human"}); err != nil {
		t.Fatal(err)
	}
	res := e.call("GET", docPath+"&rev=1", nil)
	if res.Code != http.StatusOK || res.Body["historic"] != true {
		t.Fatalf("历史版本应标记 historic: %d %s", res.Code, res.Raw)
	}
	for _, d := range entries(t, res, "decisions") {
		if d["confirm_ref"] != nil && d["confirm_ref"] != "" {
			t.Fatalf("历史版本不应带确认项编号（不能作答）: %v", d)
		}
	}
	// 当前版本不受影响，答案保留（决策卡内容没变）
	ds = decisions(docPath)
	if ds[0]["status"] != "answered" {
		t.Fatalf("改了无关的文字不应使答案失效: %v", ds)
	}
}

func newAuthedRequest(t *testing.T, e *cfEnv, method, path string) *http.Request {
	t.Helper()
	req := httptest.NewRequest(method, path, nil)
	req.Header.Set(e.auth[0], e.auth[1])
	return req
}
