package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/teacat99/RelayMesh/v2/internal/config"
	"github.com/teacat99/RelayMesh/v2/internal/ntfy"
	"github.com/teacat99/RelayMesh/v2/internal/store"
)

// fakeNtfy 是一个假的 ntfy 服务：记录收到的发布请求。
type fakeNtfy struct {
	*httptest.Server
	mu    sync.Mutex
	posts []fakeNtfyPost
	// status 非 0 时用这个状态码应答（模拟认证失败等）。
	status int
}

type fakeNtfyPost struct {
	Auth string
	Body map[string]any
}

func newFakeNtfy(t *testing.T) *fakeNtfy {
	t.Helper()
	f := &fakeNtfy{}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		f.mu.Lock()
		f.posts = append(f.posts, fakeNtfyPost{Auth: r.Header.Get("Authorization"), Body: body})
		status := f.status
		f.mu.Unlock()
		if status != 0 {
			w.WriteHeader(status)
			_, _ = w.Write([]byte(`{"error":"denied"}`))
			return
		}
		_, _ = w.Write([]byte(`{"id":"1"}`))
	}))
	t.Cleanup(f.Close)
	return f
}

func (f *fakeNtfy) snapshot() []fakeNtfyPost {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]fakeNtfyPost(nil), f.posts...)
}

func (f *fakeNtfy) waitFor(t *testing.T, n int) []fakeNtfyPost {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if got := f.snapshot(); len(got) >= n {
			return got
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("等待假 ntfy 收到 %d 条推送超时，实际 %d 条", n, len(f.snapshot()))
	return nil
}

func ntfyCall(t *testing.T, srv *Server, method, path string, body any) (int, map[string]any, string) {
	t.Helper()
	var rd io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		rd = bytes.NewReader(raw)
	}
	req := httptest.NewRequest(method, path, rd)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	w := httptest.NewRecorder()
	srv.Engine().ServeHTTP(w, req)
	var out map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	return w.Code, out, w.Body.String()
}

func newNtfyTestServer(t *testing.T, mutate func(*config.Config)) (*Server, *store.Store) {
	t.Helper()
	cfg := &config.Config{ProjectID: "ntfy-proj", JWTSecret: "ntfy-test-secret", MCPToken: "tok-ntfy", FeedbackTimeoutSeconds: 2}
	if mutate != nil {
		mutate(cfg)
	}
	st, err := store.New(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	srv := NewServer(cfg, st, nil)
	t.Cleanup(func() { srv.Shutdown(context.Background()) })
	return srv, st
}

func TestNtfyAPI_DefaultsAndWriteOnlySecrets(t *testing.T) {
	srv, st := newNtfyTestServer(t, nil)

	code, out, raw := ntfyCall(t, srv, http.MethodGet, "/api/v1/notify/ntfy", nil)
	if code != http.StatusOK {
		t.Fatalf("GET 应返回 200：%d %s", code, raw)
	}
	cfg := out["config"].(map[string]any)
	if cfg["enabled"] != false || cfg["server"] != ntfy.DefaultServer || cfg["token_set"] != false || cfg["password_set"] != false {
		t.Fatalf("默认配置不对：%s", raw)
	}

	// 保存带凭据的配置：响应与之后的读取都不能带出凭据本身。
	body := map[string]any{
		"enabled": true, "server": "https://ntfy.example.com", "topic": "rm-topic-1", "priority": 4,
		"token": "tk_TOPSECRETTOKEN", "username": "bob", "password": "TOPSECRETPASS", "click_url": "https://rm.example.com/",
	}
	code, out, raw = ntfyCall(t, srv, http.MethodPut, "/api/v1/notify/ntfy", body)
	if code != http.StatusOK {
		t.Fatalf("PUT 应返回 200：%d %s", code, raw)
	}
	for _, secret := range []string{"TOPSECRETTOKEN", "TOPSECRETPASS"} {
		if strings.Contains(raw, secret) {
			t.Fatalf("PUT 响应泄露了凭据 %q：%s", secret, raw)
		}
	}
	cfg = out["config"].(map[string]any)
	if cfg["token_set"] != true || cfg["password_set"] != true || cfg["username"] != "bob" || cfg["topic"] != "rm-topic-1" {
		t.Fatalf("应只告诉界面「已设置」：%s", raw)
	}
	_, _, raw = ntfyCall(t, srv, http.MethodGet, "/api/v1/notify/ntfy", nil)
	if strings.Contains(raw, "TOPSECRET") {
		t.Fatalf("GET 响应泄露了凭据：%s", raw)
	}
	// 全局设置接口也不能带出。
	_, _, raw = ntfyCall(t, srv, http.MethodGet, "/api/v1/settings", nil)
	if strings.Contains(raw, "TOPSECRET") || strings.Contains(raw, "rm-topic-1") {
		t.Fatalf("全局设置里不应出现 ntfy 配置：%s", raw)
	}

	// 凭据留空 = 保持原值；库里的真实值没变。
	body = map[string]any{"enabled": true, "server": "https://ntfy.example.com", "topic": "rm-topic-2", "priority": 5, "username": "bob"}
	if code, _, raw = ntfyCall(t, srv, http.MethodPut, "/api/v1/notify/ntfy", body); code != http.StatusOK {
		t.Fatalf("第二次 PUT 失败：%d %s", code, raw)
	}
	saved, _ := st.GetNtfyConfig(context.Background())
	if saved.Token != "tk_TOPSECRETTOKEN" || saved.Password != "TOPSECRETPASS" || saved.Topic != "rm-topic-2" || saved.Priority != 5 {
		t.Fatalf("凭据留空应保持原值、其余字段整体替换：%+v", saved)
	}

	// clear_token / clear_password 才会清掉。只清密码、留着用户名是不完整的认证，会被拒绝；一起清则通过。
	body["clear_token"], body["clear_password"] = true, true
	if code, _, raw = ntfyCall(t, srv, http.MethodPut, "/api/v1/notify/ntfy", body); code != http.StatusBadRequest || !strings.Contains(raw, "同时填写") {
		t.Fatalf("只清密码不清用户名应被拒绝：%d %s", code, raw)
	}
	body["username"] = ""
	code, out, raw = ntfyCall(t, srv, http.MethodPut, "/api/v1/notify/ntfy", body)
	if code != http.StatusOK {
		t.Fatalf("同时清掉用户名与密码应成功：%d %s", code, raw)
	}
	cfg = out["config"].(map[string]any)
	if cfg["token_set"] != false || cfg["password_set"] != false || cfg["username"] != "" {
		t.Fatalf("清除后应显示未设置：%s", raw)
	}
}

func TestNtfyAPI_EnvTokenIsReportedButNeverReturned(t *testing.T) {
	srv, _ := newNtfyTestServer(t, func(c *config.Config) { c.NtfyToken = "tk_FROMENVSECRET" })
	_, out, raw := ntfyCall(t, srv, http.MethodGet, "/api/v1/notify/ntfy", nil)
	cfg := out["config"].(map[string]any)
	if cfg["token_set"] != true || cfg["token_from_env"] != true {
		t.Fatalf("环境变量提供令牌时应如实告知界面：%s", raw)
	}
	if strings.Contains(raw, "FROMENVSECRET") {
		t.Fatalf("泄露了环境变量里的令牌：%s", raw)
	}
}

func TestNtfyAPI_ValidationErrors(t *testing.T) {
	srv, st := newNtfyTestServer(t, nil)
	good := map[string]any{"enabled": true, "server": "https://ntfy.sh", "topic": "good-topic", "priority": 4}
	if code, _, raw := ntfyCall(t, srv, http.MethodPut, "/api/v1/notify/ntfy", good); code != http.StatusOK {
		t.Fatalf("初始配置应保存成功：%d %s", code, raw)
	}

	bad := []struct {
		name string
		body map[string]any
		want string
	}{
		{"启用时主题为空", map[string]any{"enabled": true, "server": "https://ntfy.sh", "topic": ""}, "主题不能为空"},
		{"主题含非法字符", map[string]any{"enabled": true, "server": "https://ntfy.sh", "topic": "a b/c"}, "字母、数字"},
		{"服务地址不是 http", map[string]any{"enabled": true, "server": "file:///etc/passwd", "topic": "t"}, "http://"},
		{"服务地址带用户信息", map[string]any{"enabled": true, "server": "https://u:p@ntfy.sh", "topic": "t"}, "认证项"},
		{"优先级越界", map[string]any{"enabled": true, "server": "https://ntfy.sh", "topic": "t", "priority": 9}, "优先级"},
		{"延迟越界", map[string]any{"enabled": true, "server": "https://ntfy.sh", "topic": "t", "delay_seconds": 99999}, "延迟"},
		{"令牌带换行", map[string]any{"enabled": true, "server": "https://ntfy.sh", "topic": "t", "token": "tk_a\r\nX-Evil: 1"}, "令牌"},
	}
	for _, tc := range bad {
		t.Run(tc.name, func(t *testing.T) {
			code, out, raw := ntfyCall(t, srv, http.MethodPut, "/api/v1/notify/ntfy", tc.body)
			if code != http.StatusBadRequest {
				t.Fatalf("应返回 400：%d %s", code, raw)
			}
			if msg, _ := out["error"].(string); !strings.Contains(msg, tc.want) {
				t.Fatalf("错误信息应含 %q：%s", tc.want, raw)
			}
		})
	}
	saved, _ := st.GetNtfyConfig(context.Background())
	if saved.Topic != "good-topic" {
		t.Fatalf("校验失败不应改动已保存的配置：%+v", saved)
	}

	// 不是 JSON、或过大的请求体。
	req := httptest.NewRequest(http.MethodPut, "/api/v1/notify/ntfy", strings.NewReader("not json"))
	w := httptest.NewRecorder()
	srv.Engine().ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("非 JSON 应返回 400：%d", w.Code)
	}
	huge := strings.NewReader(`{"topic":"` + strings.Repeat("a", 64<<10) + `"}`)
	w = httptest.NewRecorder()
	srv.Engine().ServeHTTP(w, httptest.NewRequest(http.MethodPut, "/api/v1/notify/ntfy", huge))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("过大的请求体应被拒绝：%d", w.Code)
	}
}

func TestNtfyAPI_RequiresLogin(t *testing.T) {
	srv, _ := newNtfyTestServer(t, func(c *config.Config) {
		c.WebUsername, c.WebPassword = "admin", "strong_password_999"
	})
	for _, tc := range []struct{ method, path string }{
		{http.MethodGet, "/api/v1/notify/ntfy"},
		{http.MethodPut, "/api/v1/notify/ntfy"},
		{http.MethodPost, "/api/v1/notify/ntfy/test"},
	} {
		req := httptest.NewRequest(tc.method, tc.path, strings.NewReader(`{}`))
		w := httptest.NewRecorder()
		srv.Engine().ServeHTTP(w, req)
		if w.Code != http.StatusUnauthorized {
			t.Errorf("%s %s 未登录应返回 401，实际 %d", tc.method, tc.path, w.Code)
		}
	}
}

func TestNtfyAPI_TestEndpoint(t *testing.T) {
	fake := newFakeNtfy(t)
	srv, _ := newNtfyTestServer(t, nil)

	// 还没配置主题：400，不发出任何请求。
	code, out, raw := ntfyCall(t, srv, http.MethodPost, "/api/v1/notify/ntfy/test", nil)
	if code != http.StatusBadRequest || !strings.Contains(raw, "主题") {
		t.Fatalf("没配主题时应返回 400：%d %s", code, raw)
	}
	_ = out

	// 用未保存的草稿试发（草稿里没有令牌，保存过的令牌会被沿用）。
	save := map[string]any{"enabled": false, "server": fake.URL, "topic": "saved-topic", "priority": 4, "token": "tk_saved"}
	if code, _, raw = ntfyCall(t, srv, http.MethodPut, "/api/v1/notify/ntfy", save); code != http.StatusOK {
		t.Fatalf("保存失败：%d %s", code, raw)
	}
	draft := map[string]any{"draft": map[string]any{"enabled": false, "server": fake.URL, "topic": "draft-topic", "priority": 3}}
	code, out, raw = ntfyCall(t, srv, http.MethodPost, "/api/v1/notify/ntfy/test", draft)
	if code != http.StatusOK || out["ok"] != true {
		t.Fatalf("测试推送应成功：%d %s", code, raw)
	}
	posts := fake.waitFor(t, 1)
	if posts[0].Body["topic"] != "draft-topic" || posts[0].Auth != "Bearer tk_saved" || posts[0].Body["title"] != "RelayMesh 测试推送" {
		t.Fatalf("应使用草稿里的主题、沿用已保存的令牌：%+v", posts[0])
	}
	if strings.Contains(raw, "tk_saved") {
		t.Fatalf("测试结果泄露了令牌：%s", raw)
	}
	if status, _ := out["status"].(map[string]any); status == nil || status["last_ok"] != true || status["last_test"] != true {
		t.Fatalf("应记下「最近一次」是测试推送：%s", raw)
	}

	// 间隔太短被节流。
	if code, _, raw = ntfyCall(t, srv, http.MethodPost, "/api/v1/notify/ntfy/test", nil); code != http.StatusTooManyRequests {
		t.Fatalf("连续测试应被节流：%d %s", code, raw)
	}
	srv.handler.ntfyGuard.mu.Lock()
	srv.handler.ntfyGuard.lastTest = time.Now().Add(-time.Minute)
	srv.handler.ntfyGuard.mu.Unlock()

	// 服务拒绝：接口仍是 200，结果里 ok=false 并给出可读的原因；原因里没有令牌。
	fake.mu.Lock()
	fake.status = http.StatusForbidden
	fake.mu.Unlock()
	code, out, raw = ntfyCall(t, srv, http.MethodPost, "/api/v1/notify/ntfy/test", nil)
	if code != http.StatusOK || out["ok"] != false || out["http_status"] != float64(403) {
		t.Fatalf("被拒绝时应返回 200 且 ok=false：%d %s", code, raw)
	}
	if msg, _ := out["error"].(string); !strings.Contains(msg, "认证失败") || strings.Contains(raw, "tk_saved") {
		t.Fatalf("错误应可读且不含令牌：%s", raw)
	}
}

// 端到端：智能体经 MCP 发起 interactive_feedback → 手机上的 ntfy 收到推送。
func TestNtfyAPI_NewRequestIsPushed(t *testing.T) {
	fake := newFakeNtfy(t)
	srv, st := newNtfyTestServer(t, nil)
	if _, err := st.UpdateNtfyConfig(context.Background(), func(c ntfy.Config) ntfy.Config {
		c.Enabled, c.Server, c.Topic, c.Token = true, fake.URL, "rm-e2e", "tk_e2e"
		c.ClickURL = "https://rm.example.com/"
		return c
	}); err != nil {
		t.Fatal(err)
	}

	call := func(args map[string]any) *httptest.ResponseRecorder {
		body, _ := json.Marshal(map[string]any{
			"jsonrpc": "2.0", "id": 1, "method": "tools/call",
			"params": map[string]any{"name": "interactive_feedback", "arguments": args},
		})
		w := httptest.NewRecorder()
		srv.Engine().ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/mcp?token=tok-ntfy", bytes.NewReader(body)))
		return w
	}

	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		done <- call(map[string]any{
			"workflow_id": "wf-push", "project_directory": "/work/Demo", "title": "请确认方案", "summary": "## 结论\n方案已就绪",
		})
	}()

	posts := fake.waitFor(t, 1)
	p := posts[0]
	msg, _ := p.Body["message"].(string)
	if p.Body["topic"] != "rm-e2e" || p.Auth != "Bearer tk_e2e" || p.Body["click"] != "https://rm.example.com/" {
		t.Fatalf("推送的主题 / 认证 / 点击地址不对：%+v", p)
	}
	if !strings.Contains(msg, "请确认方案") || !strings.Contains(msg, "项目：Demo") {
		t.Fatalf("推送正文应含会话标题与项目：%q", msg)
	}
	if strings.Contains(msg, "方案已就绪") {
		t.Fatalf("默认不应把摘要发给第三方服务：%q", msg)
	}
	select {
	case w := <-done:
		if w.Code != http.StatusOK {
			t.Fatalf("MCP 调用本身应正常结束：%d", w.Code)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("interactive_feedback 没有按短等待时长返回")
	}

	// 对照：已经有人提前追加了回复 → 请求秒回，不会有人在「等」，也就不该推送。
	code, _, raw := ntfyCall(t, srv, http.MethodPost, "/api/v1/workflows/wf-quick/append", map[string]any{"response_text": "先说一句"})
	if code != http.StatusOK {
		t.Fatalf("追加失败：%d %s", code, raw)
	}
	before := len(fake.snapshot())
	w := call(map[string]any{"workflow_id": "wf-quick", "project_directory": "/work/Demo", "title": "秒回", "summary": "s"})
	if w.Code != http.StatusOK {
		t.Fatalf("MCP 调用失败：%d", w.Code)
	}
	time.Sleep(200 * time.Millisecond)
	if after := len(fake.snapshot()); after != before {
		t.Fatalf("已被提前追加秒回的请求不应推送（推送数 %d → %d）", before, after)
	}

	// 关掉总开关后不再推送。
	if _, err := st.UpdateNtfyConfig(context.Background(), func(c ntfy.Config) ntfy.Config { c.Enabled = false; return c }); err != nil {
		t.Fatal(err)
	}
	call(map[string]any{"workflow_id": "wf-off", "project_directory": "/work/Demo", "title": "关掉之后", "summary": "s"})
	time.Sleep(200 * time.Millisecond)
	if after := len(fake.snapshot()); after != before {
		t.Fatalf("总开关关闭后不应推送：%d → %d", before, after)
	}
}

// 延迟推送走真实的「是否仍在等待」复核（数据库里的会话状态）：1 秒内被处理掉的请求不推送，没处理的到点推送。
func TestNtfyAPI_DelayedPushChecksRealSessionState(t *testing.T) {
	fake := newFakeNtfy(t)
	srv, st := newNtfyTestServer(t, func(c *config.Config) { c.FeedbackTimeoutSeconds = 4 })
	if _, err := st.UpdateNtfyConfig(context.Background(), func(c ntfy.Config) ntfy.Config {
		c.Enabled, c.Server, c.Topic, c.DelaySeconds = true, fake.URL, "rm-delay", 1
		return c
	}); err != nil {
		t.Fatal(err)
	}

	call := func(workflow, title string) {
		body, _ := json.Marshal(map[string]any{
			"jsonrpc": "2.0", "id": 1, "method": "tools/call",
			"params": map[string]any{"name": "interactive_feedback", "arguments": map[string]any{
				"workflow_id": workflow, "project_directory": "/work/Demo", "title": title, "summary": "s",
			}},
		})
		srv.Engine().ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/mcp?token=tok-ntfy", bytes.NewReader(body)))
	}
	pendingSession := func(workflow string) string {
		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) {
			if s, err := st.GetLatestWorkflowFeedbackSession(context.Background(), workflow); err == nil && s != nil && s.Status == "pending" {
				return s.ID
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatalf("没有等到 %s 的待回复会话", workflow)
		return ""
	}

	// 1) 延迟期间被人处理掉（提交了回复）：到点后不再推送。
	go call("wf-answered", "很快就被回复")
	id := pendingSession("wf-answered")
	if _, err := st.SubmitFeedback(context.Background(), store.SubmitFeedbackInput{SessionID: id, ResponseText: "好的"}); err != nil {
		t.Fatal(err)
	}
	// 2) 没人处理：延迟到点后推送。
	go call("wf-ignored", "一直没人理")
	pendingSession("wf-ignored")

	posts := fake.waitFor(t, 1)
	time.Sleep(300 * time.Millisecond) // 留出时间，确认被处理掉的那条没有迟到的推送
	posts = fake.snapshot()
	if len(posts) != 1 {
		t.Fatalf("应只推送没人处理的那一条，实际 %d 条：%+v", len(posts), posts)
	}
	if msg, _ := posts[0].Body["message"].(string); !strings.Contains(msg, "一直没人理") {
		t.Fatalf("推送的应是没人处理的请求：%q", msg)
	}
}
