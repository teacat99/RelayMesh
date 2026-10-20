package mcp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/teacat99/RelayMesh/v2/internal/config"
	"github.com/teacat99/RelayMesh/v2/internal/store"
)

const testLocalKey = "k-0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func newLocalProxyTestServer(t *testing.T, key string) (*Server, *store.Store) {
	t.Helper()
	cfg := &config.Config{ProjectID: "test-proxy-proj", Version: "9.9.9-test", HostName: "leader-host"}
	st, err := store.New(":memory:")
	if err != nil {
		t.Fatalf("创建内存库失败: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	srv := NewServer(cfg, st, nil)
	if key != "" {
		srv.SetLocalProxyKey(key)
	}
	return srv, st
}

// postLocal 直接对处理器发一条请求（不经过网络）；remote 是模拟的对端地址。
func postLocal(srv *Server, key, remote, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, LocalProxyPath, strings.NewReader(body))
	req.RemoteAddr = remote
	if key != "" {
		req.Header.Set(LocalProxyKeyHeader, key)
	}
	rec := httptest.NewRecorder()
	srv.ServeLocalProxy(rec, req)
	return rec
}

func TestLocalProxyDisabledWithoutKey(t *testing.T) {
	srv, _ := newLocalProxyTestServer(t, "")
	// 没设置钥匙（常驻服务 / 生产 Hub 永远如此）：不管带什么都像没有这个入口
	for _, key := range []string{"", testLocalKey, "anything"} {
		rec := postLocal(srv, key, "127.0.0.1:5555", `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("没开转发入口时应返回 404，实际 %d（key=%q）", rec.Code, key)
		}
	}
}

func TestLocalProxyRejectsMissingOrWrongKey(t *testing.T) {
	srv, _ := newLocalProxyTestServer(t, testLocalKey)
	body := `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`
	for name, key := range map[string]string{"没带钥匙": "", "钥匙不对": "k-wrong", "前缀相同": testLocalKey[:len(testLocalKey)-1]} {
		rec := postLocal(srv, key, "127.0.0.1:5555", body)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("%s：应返回 401，实际 %d", name, rec.Code)
		}
		var resp jsonRPCResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil || resp.Error == nil {
			t.Fatalf("%s：响应应是 JSON-RPC 错误：%v %s", name, err, rec.Body.String())
		}
		if strings.Contains(rec.Body.String(), testLocalKey) {
			t.Fatalf("%s：响应里不得出现真钥匙", name)
		}
	}
}

func TestLocalProxyLoopbackOnly(t *testing.T) {
	srv, _ := newLocalProxyTestServer(t, testLocalKey)
	body := `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`
	// 即使钥匙对，非回环对端也一律拒绝
	for _, remote := range []string{"192.0.2.10:4000", "10.1.2.3:80", "[2001:db8::1]:9999", "8.8.8.8:1"} {
		if rec := postLocal(srv, testLocalKey, remote, body); rec.Code != http.StatusForbidden {
			t.Fatalf("对端 %s 应被拒绝（403），实际 %d", remote, rec.Code)
		}
	}
	for _, remote := range []string{"127.0.0.1:5555", "[::1]:5555", "127.5.5.5:1"} {
		if rec := postLocal(srv, testLocalKey, remote, body); rec.Code != http.StatusOK {
			t.Fatalf("回环对端 %s 应被接受（200），实际 %d：%s", remote, rec.Code, rec.Body.String())
		}
	}
}

func TestLocalProxyServesRequestsAndNotifications(t *testing.T) {
	srv, _ := newLocalProxyTestServer(t, testLocalKey)

	rec := postLocal(srv, testLocalKey, "127.0.0.1:5555", `{"jsonrpc":"2.0","id":7,"method":"tools/list"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("tools/list 应返回 200，实际 %d：%s", rec.Code, rec.Body.String())
	}
	var resp struct {
		ID     float64 `json:"id"`
		Result struct {
			Tools []struct {
				Name string `json:"name"`
			} `json:"tools"`
		} `json:"result"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("响应不是 JSON：%v", err)
	}
	if resp.ID != 7 || len(resp.Result.Tools) == 0 {
		t.Fatalf("响应的 id / 工具列表不对：%s", rec.Body.String())
	}
	if strings.ContainsAny(strings.TrimSuffix(rec.Body.String(), "\n"), "\n") {
		t.Fatalf("响应必须是单行 JSON：%q", rec.Body.String())
	}

	// 通知：202、没有响应体
	for _, n := range []string{
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","method":"notifications/cancelled","params":{"requestId":3}}`,
	} {
		rec := postLocal(srv, testLocalKey, "127.0.0.1:5555", n)
		if rec.Code != http.StatusAccepted || rec.Body.Len() != 0 {
			t.Fatalf("通知应返回 202 且没有响应体，实际 %d %q", rec.Code, rec.Body.String())
		}
	}
}

func TestLocalProxyMethodAndMalformedBody(t *testing.T) {
	srv, _ := newLocalProxyTestServer(t, testLocalKey)

	req := httptest.NewRequest(http.MethodGet, LocalProxyPath, nil)
	req.RemoteAddr = "127.0.0.1:5555"
	req.Header.Set(LocalProxyKeyHeader, testLocalKey)
	rec := httptest.NewRecorder()
	srv.ServeLocalProxy(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET 应返回 405，实际 %d", rec.Code)
	}

	// 解析失败与 stdio 一致：回一条 -32700，而不是 HTTP 错误
	rec = postLocal(srv, testLocalKey, "127.0.0.1:5555", `{not json`)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "-32700") {
		t.Fatalf("坏 JSON 应回 -32700，实际 %d %s", rec.Code, rec.Body.String())
	}

	// 请求体超过 stdio 的单行上限（16MB）：413
	huge := strings.Repeat("a", MaxStdioMessageSize+1024)
	rec = postLocal(srv, testLocalKey, "127.0.0.1:5555", huge)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("超大请求体应返回 413，实际 %d", rec.Code)
	}
}

func TestLocalProxyRateLimitsWrongKeysOnly(t *testing.T) {
	srv, _ := newLocalProxyTestServer(t, testLocalKey)
	body := `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`

	var saw429 bool
	for i := 0; i < localProxyMaxFails+5; i++ {
		rec := postLocal(srv, "k-guess", "127.0.0.1:5555", body)
		switch rec.Code {
		case http.StatusUnauthorized:
			if saw429 {
				t.Fatalf("进入限流后不该再回 401")
			}
		case http.StatusTooManyRequests:
			saw429 = true
			if rec.Header().Get("Retry-After") == "" {
				t.Fatalf("429 应带 Retry-After")
			}
		default:
			t.Fatalf("意外的状态码 %d", rec.Code)
		}
	}
	if !saw429 {
		t.Fatalf("连续 %d 次错钥匙后应被限流（429）", localProxyMaxFails+5)
	}
	// 正确的钥匙不受限流影响（否则任何人乱试几次就能把自己人锁在门外）
	if rec := postLocal(srv, testLocalKey, "127.0.0.1:5555", body); rec.Code != http.StatusOK {
		t.Fatalf("被限流期间正确的钥匙仍应通过，实际 %d", rec.Code)
	}
}

func TestLocalProxyKeyCanBeRevoked(t *testing.T) {
	srv, _ := newLocalProxyTestServer(t, testLocalKey)
	body := `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`
	if rec := postLocal(srv, testLocalKey, "127.0.0.1:5555", body); rec.Code != http.StatusOK {
		t.Fatalf("开着时应通过，实际 %d", rec.Code)
	}
	srv.SetLocalProxyKey("") // 主进程收尾时关掉入口
	if rec := postLocal(srv, testLocalKey, "127.0.0.1:5555", body); rec.Code != http.StatusNotFound {
		t.Fatalf("关掉后应回 404，实际 %d", rec.Code)
	}
}

// 转发进程自己的主机名要带进会话；客户端断开连接要取消主进程里的调用（等价于 stdio 的 notifications/cancelled）。
func TestLocalProxyHostNameAndCancelOnDisconnect(t *testing.T) {
	srv, st := newLocalProxyTestServer(t, testLocalKey)

	handlerDone := make(chan struct{})
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer close(handlerDone)
		srv.ServeLocalProxy(w, r)
	}))
	defer ts.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	call := `{"jsonrpc":"2.0","id":11,"method":"tools/call","params":{"name":"interactive_feedback","arguments":{` +
		`"workflow_id":"wf-proxy-cancel","title":"转发取消","summary":"## 转发取消\n\n这条调用会一直等用户答复，直到连接断开。","project_directory":"/tmp/rm-proxy-test"}}}`
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, ts.URL+LocalProxyPath, strings.NewReader(call))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set(LocalProxyKeyHeader, testLocalKey)
	req.Header.Set(LocalProxyHostHeader, "window-b")
	clientDone := make(chan error, 1)
	go func() {
		resp, err := http.DefaultClient.Do(req)
		if resp != nil {
			_ = resp.Body.Close()
		}
		clientDone <- err
	}()

	// 会话建好后（主进程里这次调用正在等答复），确认它带的是转发进程的主机名
	var host string
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		sessions, err := st.GetSessionsByWorkflow(context.Background(), "wf-proxy-cancel", 5)
		if err == nil && len(sessions) > 0 {
			host = sessions[0].HostName
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if host != "window-b" {
		t.Fatalf("会话的主机名应是转发进程自报的 window-b，实际 %q", host)
	}

	select {
	case <-handlerDone:
		t.Fatalf("用户还没答复、连接也没断，调用不该提前返回")
	case <-time.After(300 * time.Millisecond):
	}

	cancel() // 转发进程收到 notifications/cancelled = 断开这次 HTTP 调用
	select {
	case <-handlerDone:
	case <-time.After(5 * time.Second):
		t.Fatalf("连接断开后，主进程里的调用应随之取消并返回")
	}
	if err := <-clientDone; err == nil {
		t.Fatalf("客户端取消后应得到错误")
	}
}

func TestLocalProxyInfoFileRoundTrip(t *testing.T) {
	dir := t.TempDir()
	info := LocalProxyInfo{Key: testLocalKey, PID: 4242, Port: 18775, Version: "9.9.9", StartedAt: "2026-10-20T00:00:00Z"}
	if err := WriteLocalProxyInfo(dir, info); err != nil {
		t.Fatalf("写接入凭证失败: %v", err)
	}
	got, err := ReadLocalProxyInfo(dir)
	if err != nil || got != info {
		t.Fatalf("读回的内容不一致：%+v %v", got, err)
	}
	if runtime.GOOS != "windows" {
		st, err := os.Stat(filepath.Join(dir, LocalProxyInfoFile))
		if err != nil {
			t.Fatal(err)
		}
		if perm := st.Mode().Perm(); perm != 0o600 {
			t.Fatalf("接入凭证文件权限应为 0600，实际 %o", perm)
		}
	}
	// 目录里不能留下临时文件
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if e.Name() != LocalProxyInfoFile {
			t.Fatalf("目录里多出了不该有的文件：%s", e.Name())
		}
	}

	// 新钥匙覆盖旧钥匙；老进程收尾时只删自己的钥匙
	newInfo := info
	newInfo.Key = "k-new-0123456789abcdef0123456789abcdef0123456789abcdef0123456789ab"
	if err := WriteLocalProxyInfo(dir, newInfo); err != nil {
		t.Fatal(err)
	}
	RemoveLocalProxyInfo(dir, info.Key)
	if got, err := ReadLocalProxyInfo(dir); err != nil || got.Key != newInfo.Key {
		t.Fatalf("老进程不该删掉新主进程的凭证：%+v %v", got, err)
	}
	RemoveLocalProxyInfo(dir, newInfo.Key)
	if _, err := ReadLocalProxyInfo(dir); !os.IsNotExist(err) {
		t.Fatalf("自己的钥匙应被删除，实际 %v", err)
	}
}

func TestLocalProxyInfoRejectsBadFiles(t *testing.T) {
	dir := t.TempDir()
	if _, err := ReadLocalProxyInfo(dir); !os.IsNotExist(err) {
		t.Fatalf("没有文件应是不存在错误，实际 %v", err)
	}
	path := filepath.Join(dir, LocalProxyInfoFile)
	for name, content := range map[string]string{"坏 JSON": "{oops", "没有钥匙": `{"pid":1}`, "空钥匙": `{"key":"  "}`} {
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := ReadLocalProxyInfo(dir); err == nil {
			t.Fatalf("%s：应读取失败", name)
		}
	}
	if err := WriteLocalProxyInfo(dir, LocalProxyInfo{}); err == nil {
		t.Fatalf("空钥匙不应被写入")
	}
}

func TestNewLocalProxyKeyIsRandom(t *testing.T) {
	a, err := NewLocalProxyKey()
	if err != nil {
		t.Fatal(err)
	}
	b, _ := NewLocalProxyKey()
	if len(a) != 64 || a == b {
		t.Fatalf("钥匙应是 64 个十六进制字符且每次不同：%q %q", a, b)
	}
}
