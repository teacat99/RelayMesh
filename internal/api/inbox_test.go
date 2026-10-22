package api

import (
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/teacat99/RelayMesh/v2/internal/config"
	"github.com/teacat99/RelayMesh/v2/internal/cursorhook"
	"github.com/teacat99/RelayMesh/v2/internal/store"
)

// 1×1 的 PNG。
const inboxAPITestPNG = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR4nGP4z8DwHwAFAAH/q842iQAAAABJRU5ErkJggg=="

// listenSSE 登记一个假的 SSE 订阅者，收集广播出去的原始消息。
func listenSSE(t *testing.T, srv *Server) chan string {
	t.Helper()
	ch := make(chan string, 64)
	srv.broker.clientsMu.Lock()
	srv.broker.clients[ch] = true
	srv.broker.clientsMu.Unlock()
	t.Cleanup(func() {
		srv.broker.clientsMu.Lock()
		delete(srv.broker.clients, ch)
		srv.broker.clientsMu.Unlock()
	})
	return ch
}

func drainSSE(ch chan string) []string {
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

func inboxItems(t *testing.T, srv *Server, wf string) (items []map[string]any, out map[string]any) {
	t.Helper()
	code, out, raw := ntfyCall(t, srv, http.MethodGet, "/api/v1/workflows/"+wf+"/inbox", nil)
	if code != http.StatusOK {
		t.Fatalf("GET inbox 应返回 200: %d %s", code, raw)
	}
	raws, _ := out["items"].([]any)
	for _, r := range raws {
		items = append(items, r.(map[string]any))
	}
	return items, out
}

func appendText(t *testing.T, srv *Server, wf, text string) map[string]any {
	t.Helper()
	code, out, raw := ntfyCall(t, srv, http.MethodPost, "/api/v1/workflows/"+wf+"/append", map[string]any{"response_text": text})
	if code != http.StatusOK || out["type"] != "queued" {
		t.Fatalf("追加应排队: %d %s", code, raw)
	}
	return out["queued"].(map[string]any)
}

func TestInboxAPI_EachAppendIsListedWithItsOwnState(t *testing.T) {
	srv, st := newNtfyTestServer(t, nil)
	ctx := context.Background()
	wf := "wf-api-inbox"

	items, out := inboxItems(t, srv, wf)
	if len(items) != 0 || out["agent_last_activity_at"] != nil {
		t.Fatalf("空收件箱应没有条目、没有活动记录: %+v", out)
	}
	cfg := out["config"].(map[string]any)
	eff := cfg["effective"].(map[string]any)
	for _, k := range []string{"enabled", "receipt_hint", "hooks", "prompt_clause"} {
		if cfg[k] != true || eff[k] != true {
			t.Fatalf("开关默认应全开: %+v", cfg)
		}
	}

	first := appendText(t, srv, wf, "第一条追加")
	second := appendText(t, srv, wf, "第二条追加")
	if first["id"] == second["id"] || first["state"] != "queued" || first["kind"] != "append" {
		t.Fatalf("两条追加应各自一行、状态为 queued: %+v %+v", first, second)
	}
	items, out = inboxItems(t, srv, wf)
	if len(items) != 2 || items[0]["response_text"] != "第一条追加" || items[1]["state"] != "queued" {
		t.Fatalf("应按时间顺序列出两条 queued: %+v", items)
	}
	sum := out["summary"].(map[string]any)
	if sum["unread"].(float64) != 2 || sum["latest_preview"] != "第二条追加" {
		t.Fatalf("概况不对: %+v", sum)
	}

	// 智能体主动读取后：delivered（经 tool），再确认：acked
	if _, err := st.InboxRead(ctx, wf, store.InboxReadOptions{}, time.Now()); err != nil {
		t.Fatal(err)
	}
	items, _ = inboxItems(t, srv, wf)
	if len(items) != 2 || items[0]["state"] != "delivered" || items[0]["delivered_via"] != "tool" {
		t.Fatalf("读取后应是 delivered / tool: %+v", items)
	}
	if _, err := st.InboxAck(ctx, wf, nil, time.Now()); err != nil {
		t.Fatal(err)
	}
	items, _ = inboxItems(t, srv, wf)
	if len(items) != 2 || items[0]["state"] != "acked" || items[1]["state"] != "acked" {
		t.Fatalf("确认后应是 acked: %+v", items)
	}

	// 智能体最近一次交互的时间
	at := time.Now().Add(-90 * time.Second).Truncate(time.Second)
	st.RecordAgentActivity(wf, at)
	_, out = inboxItems(t, srv, wf)
	got, _ := out["agent_last_activity_at"].(string)
	parsed, err := time.Parse(time.RFC3339Nano, got)
	if err != nil || !parsed.Equal(at) {
		t.Fatalf("应返回最近一次交互的时间 %v，得到 %q (%v)", at, got, err)
	}
}

func TestInboxAPI_HookLastSeen(t *testing.T) {
	srv, st := newNtfyTestServer(t, nil)
	wf := "wf-api-hook"
	_, out := inboxItems(t, srv, wf)
	if v, has := out["hook_last_seen_at"]; !has || v != nil {
		t.Fatalf("没有 hook 来过时应返回 null: %+v", out)
	}
	at := time.Now().Add(-45 * time.Second).Truncate(time.Second)
	st.RecordHookSeen(wf, at)
	_, out = inboxItems(t, srv, wf)
	got, _ := out["hook_last_seen_at"].(string)
	parsed, err := time.Parse(time.RFC3339Nano, got)
	if err != nil || !parsed.Equal(at) {
		t.Fatalf("应返回 hook 最近一次查询的时间 %v，得到 %q (%v)", at, got, err)
	}
	if out["agent_last_activity_at"] != nil {
		t.Fatalf("hook 查询不应变成智能体活动: %+v", out)
	}
}

// Python 版 hook 已移除：旧路径不再提供脚本；Go 版的安装入口（install.sh / install.ps1）仍然公开，
// 即使设了登录密码也不需要登录——项目里一条 curl 就能装上。
func TestInboxAPI_PythonHookScriptIsGoneInstallStaysPublic(t *testing.T) {
	srv, _ := newNtfyTestServer(t, func(c *config.Config) { c.WebPassword = "a-strong-password" })

	if code, _, _ := ntfyCall(t, srv, http.MethodGet, "/api/v1/workflows/wf/inbox", nil); code != http.StatusUnauthorized {
		t.Fatalf("前提不成立：设了密码后受保护接口应要求登录，实际 %d", code)
	}

	gone := httptest.NewRecorder()
	srv.Engine().ServeHTTP(gone, httptest.NewRequest(http.MethodGet, "/hooks/cursor/relaymesh-inbox.py", nil))
	if body := strings.TrimSpace(gone.Body.String()); gone.Code == http.StatusOK && (strings.HasPrefix(body, "#!") || strings.Contains(body, "python3")) {
		t.Fatalf("Python hook 脚本不应再被提供: %d %.80s", gone.Code, body)
	}

	for _, path := range []string{cursorhook.InstallShPath, cursorhook.InstallPs1Path} {
		w := httptest.NewRecorder()
		srv.Engine().ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
		if w.Code != http.StatusOK {
			t.Fatalf("%s 应可匿名下载: %d %s", path, w.Code, w.Body.String())
		}
		if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
			t.Fatalf("%s 应按纯文本发送（浏览器不渲染不执行）: %q", path, ct)
		}
		if w.Header().Get("X-Content-Type-Options") != "nosniff" || w.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("%s 应带 nosniff 与 no-store: %v", path, w.Header())
		}
		// 脚本里不能夹带任何运行环境的东西
		for _, banned := range []string{"a-strong-password", "ntfy-test-secret", "tok-ntfy"} {
			if strings.Contains(w.Body.String(), banned) {
				t.Fatalf("%s 里不能出现 %q", path, banned)
			}
		}
	}
}

func TestInboxAPI_FeedbackDeliveredRowsStayOutOfTheStrip(t *testing.T) {
	srv, st := newNtfyTestServer(t, nil)
	wf := "wf-api-feedback"
	appendText(t, srv, wf, "随秒回带走")
	// 智能体发起交互：排队的追加被合并成一次秒回，随后出现在对话记录里，不再出现在收件箱条带中
	if _, err := st.CreateFeedbackSession(context.Background(), store.CreateSessionInput{WorkflowID: wf, Summary: "汇报"}); err != nil {
		t.Fatalf("CreateFeedbackSession: %v", err)
	}
	items, _ := inboxItems(t, srv, wf)
	if len(items) != 0 {
		t.Fatalf("经秒回送达的追加不应再列在收件箱里: %+v", items)
	}
}

func TestInboxAPI_RevokeBeforeAndAfterTheAgentReadIt(t *testing.T) {
	srv, st := newNtfyTestServer(t, nil)
	ctx := context.Background()
	wf := "wf-api-revoke"
	ch := listenSSE(t, srv)

	unread := appendText(t, srv, wf, "还没读就撤回")
	read := appendText(t, srv, wf, "读了之后撤回")
	drainSSE(ch)

	code, out, raw := ntfyCall(t, srv, http.MethodPost, "/api/v1/feedbacks/queued/"+idString(unread["id"])+"/revoke", nil)
	revoked := out["revoked"].(map[string]any)
	if code != http.StatusOK || revoked["delivered"] == true {
		t.Fatalf("未读撤回应成功且 delivered=false: %d %s", code, raw)
	}
	// 只读第二条（第一条已撤回，不会被读到）
	res, err := st.InboxRead(ctx, wf, store.InboxReadOptions{}, time.Now())
	if err != nil || len(res.New) != 1 || res.New[0].ResponseText != "读了之后撤回" {
		t.Fatalf("已撤回的追加不应被读到: %+v %v", res, err)
	}
	code, out, raw = ntfyCall(t, srv, http.MethodPost, "/api/v1/feedbacks/queued/"+idString(read["id"])+"/revoke", nil)
	revoked = out["revoked"].(map[string]any)
	if code != http.StatusOK || revoked["delivered"] != true {
		t.Fatalf("读后撤回应成功且 delivered=true: %d %s", code, raw)
	}
	items, _ := inboxItems(t, srv, wf)
	if len(items) != 0 {
		t.Fatalf("撤回的条目不应再列出: %+v", items)
	}
	// 撤回会广播，界面据此刷新（载荷里带上是否已被读取）
	var sawRevoke bool
	for _, m := range drainSSE(ch) {
		if strings.Contains(m, "event: queued_feedback_revoked") && strings.Contains(m, `"delivered":true`) {
			sawRevoke = true
		}
	}
	if !sawRevoke {
		t.Fatal("读后撤回应广播 queued_feedback_revoked 且带 delivered")
	}
	// 重复撤回是冲突，不是成功
	if code, _, _ = ntfyCall(t, srv, http.MethodPost, "/api/v1/feedbacks/queued/"+idString(read["id"])+"/revoke", nil); code < 400 {
		t.Fatalf("重复撤回应报错，得到 %d", code)
	}
}

// #199：只被提醒过（hook / 提示行）、还没读就撤回：响应与广播带 agent_notified=true（界面据此提示「会告知忽略」），delivered 仍是 false；
// 谁都没提醒过的撤回没有 agent_notified。
func TestInboxAPI_RevokeAfterReminderCarriesAgentNotified(t *testing.T) {
	srv, st := newNtfyTestServer(t, nil)
	ctx := context.Background()
	wfReminded, wfSilent := "wf-api-revoke-notified", "wf-api-revoke-silent"
	ch := listenSSE(t, srv)

	reminded := appendText(t, srv, wfReminded, "被提醒过、没读就撤回")
	silent := appendText(t, srv, wfSilent, "谁都没提醒过就撤回")
	if p, err := st.InboxPeek(ctx, wfReminded, true, time.Now()); err != nil || !p.Remind {
		t.Fatalf("前置：hook 应已提醒: %+v %v", p, err)
	}
	drainSSE(ch)

	code, out, raw := ntfyCall(t, srv, http.MethodPost, "/api/v1/feedbacks/queued/"+idString(reminded["id"])+"/revoke", nil)
	revoked := out["revoked"].(map[string]any)
	if code != http.StatusOK || revoked["agent_notified"] != true || revoked["delivered"] == true {
		t.Fatalf("被提醒过的撤回应 agent_notified=true、delivered=false: %d %s", code, raw)
	}
	var sawRevoke bool
	for _, m := range drainSSE(ch) {
		if strings.Contains(m, "event: queued_feedback_revoked") && strings.Contains(m, `"agent_notified":true`) && strings.Contains(m, `"delivered":false`) {
			sawRevoke = true
		}
	}
	if !sawRevoke {
		t.Fatal("撤回应广播 queued_feedback_revoked 且带 agent_notified")
	}

	code, out, raw = ntfyCall(t, srv, http.MethodPost, "/api/v1/feedbacks/queued/"+idString(silent["id"])+"/revoke", nil)
	revoked = out["revoked"].(map[string]any)
	if code != http.StatusOK || revoked["agent_notified"] == true || revoked["delivered"] == true {
		t.Fatalf("谁都没提醒过的撤回不应 agent_notified: %d %s", code, raw)
	}
}

func idString(v any) string {
	if n, ok := v.(float64); ok {
		return strconv.FormatInt(int64(n), 10)
	}
	return ""
}

func TestInboxAPI_AppendBroadcastCarriesNoImages(t *testing.T) {
	srv, _ := newNtfyTestServer(t, nil)
	ch := listenSSE(t, srv)
	wf := "wf-api-sse"
	code, _, raw := ntfyCall(t, srv, http.MethodPost, "/api/v1/workflows/"+wf+"/append", map[string]any{
		"response_text": "带图追加",
		"images":        []map[string]any{{"name": "a.png", "format": "png", "data": inboxAPITestPNG}},
	})
	if code != http.StatusOK {
		t.Fatalf("追加应成功: %d %s", code, raw)
	}
	var found bool
	for _, m := range drainSSE(ch) {
		if !strings.Contains(m, "event: queued_feedback_updated") {
			continue
		}
		found = true
		if strings.Contains(m, inboxAPITestPNG) || strings.Contains(m, "base64") {
			t.Fatalf("广播不应携带图片数据: %s", m)
		}
		if !strings.Contains(m, `"workflow_id":"`+wf+`"`) {
			t.Fatalf("广播应带工作流编号: %s", m)
		}
	}
	if !found {
		t.Fatal("追加后应广播 queued_feedback_updated")
	}
}

func TestInboxAPI_ConfigPartialUpdateAndEnvOverride(t *testing.T) {
	srv, _ := newNtfyTestServer(t, nil)
	ch := listenSSE(t, srv)

	code, out, raw := ntfyCall(t, srv, http.MethodPut, "/api/v1/inbox/config", map[string]any{})
	if code != http.StatusBadRequest {
		t.Fatalf("没有任何字段应报 400: %d %s", code, raw)
	}
	code, out, raw = ntfyCall(t, srv, http.MethodPut, "/api/v1/inbox/config", map[string]any{"hooks": false})
	if code != http.StatusOK || out["hooks"] != false || out["enabled"] != true || out["receipt_hint"] != true {
		t.Fatalf("只应改动 hooks: %d %s", code, raw)
	}
	if eff := out["effective"].(map[string]any); eff["hooks"] != false || eff["enabled"] != true {
		t.Fatalf("生效值应同步: %s", raw)
	}
	var sawEvent bool
	for _, m := range drainSSE(ch) {
		if strings.Contains(m, "event: inbox_config_updated") {
			sawEvent = true
		}
	}
	if !sawEvent {
		t.Fatal("改开关后应广播 inbox_config_updated")
	}
	code, out, raw = ntfyCall(t, srv, http.MethodPut, "/api/v1/inbox/config", map[string]any{"enabled": false, "prompt_clause": false})
	if code != http.StatusOK || out["enabled"] != false || out["prompt_clause"] != false || out["hooks"] != false {
		t.Fatalf("多项修改应一起生效且保留其他项: %d %s", code, raw)
	}
	code, out, raw = ntfyCall(t, srv, http.MethodGet, "/api/v1/inbox/config", nil)
	if code != http.StatusOK || out["enabled"] != false || out["env_off"] != false {
		t.Fatalf("GET 应反映已保存的值: %d %s", code, raw)
	}
	// 类型不对的字段被拒绝
	if code, _, _ = ntfyCall(t, srv, http.MethodPut, "/api/v1/inbox/config", map[string]any{"enabled": "yes"}); code != http.StatusBadRequest {
		t.Fatalf("类型不对应报 400，得到 %d", code)
	}

	// 环境变量总闸：保存的总开关是开的，生效的却是关的，且界面能看到原因
	envSrv, _ := newNtfyTestServer(t, func(c *config.Config) { c.InboxOff = true })
	code, out, raw = ntfyCall(t, envSrv, http.MethodGet, "/api/v1/inbox/config", nil)
	if code != http.StatusOK || out["enabled"] != true || out["env_off"] != true {
		t.Fatalf("环境变量总闸应可见: %d %s", code, raw)
	}
	if eff := out["effective"].(map[string]any); eff["enabled"] != false {
		t.Fatalf("环境变量关闭时生效的总开关应为关: %s", raw)
	}
}

func TestInboxAPI_ImagesAreServedByInboxSessionID(t *testing.T) {
	srv, _ := newNtfyTestServer(t, nil)
	wf := "wf-api-img"
	code, out, raw := ntfyCall(t, srv, http.MethodPost, "/api/v1/workflows/"+wf+"/append", map[string]any{
		"response_text": "图",
		"images":        []map[string]any{{"name": "a.png", "format": "png", "data": inboxAPITestPNG}},
	})
	if code != http.StatusOK {
		t.Fatalf("追加应成功: %d %s", code, raw)
	}
	id := idString(out["queued"].(map[string]any)["id"])

	get := func(path string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		w := httptest.NewRecorder()
		srv.Engine().ServeHTTP(w, req)
		return w
	}
	w := get("/api/v1/sessions/inbox-" + id + "/images/0")
	want, _ := base64.StdEncoding.DecodeString(inboxAPITestPNG)
	if w.Code != http.StatusOK || w.Header().Get("Content-Type") != "image/png" || w.Body.String() != string(want) {
		t.Fatalf("应能按 inbox-%s 取到图片: %d %q", id, w.Code, w.Header().Get("Content-Type"))
	}
	if w := get("/api/v1/sessions/inbox-" + id + "/images/5"); w.Code != http.StatusNotFound {
		t.Fatalf("越界应 404，得到 %d", w.Code)
	}
	if w := get("/api/v1/sessions/inbox-999999/images/0"); w.Code != http.StatusNotFound {
		t.Fatalf("不存在的追加应 404，得到 %d", w.Code)
	}
	// 普通会话编号的行为不变
	if w := get("/api/v1/sessions/sess-nope/images/0"); w.Code != http.StatusNotFound {
		t.Fatalf("不存在的会话应 404，得到 %d", w.Code)
	}
}

func TestInboxAPI_LegacyQueuedListStillWorks(t *testing.T) {
	srv, _ := newNtfyTestServer(t, nil)
	wf := "wf-api-legacy"
	appendText(t, srv, wf, "旧接口也要能用")
	code, out, raw := ntfyCall(t, srv, http.MethodGet, "/api/v1/workflows/"+wf+"/queued", nil)
	if code != http.StatusOK {
		t.Fatalf("旧的 queued 接口应保留: %d %s", code, raw)
	}
	list := out["queued_feedbacks"].([]any)
	if len(list) != 1 || list[0].(map[string]any)["state"] != "queued" {
		t.Fatalf("旧接口应列出待处理的追加并带状态: %s", raw)
	}
}
