package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// startHubForForwarder 起一个带转发入口与公开状态接口的假 Hub（真实的 mcp.Server），返回转发端、服务端与数据目录。
func startHubForForwarder(t *testing.T, key string) (*LocalForwarder, *Server, *httptest.Server, string) {
	t.Helper()
	srv, _ := newLocalProxyTestServer(t, key)
	mux := http.NewServeMux()
	mux.HandleFunc(LocalProxyPath, srv.ServeLocalProxy)
	mux.HandleFunc("/api/v1/auth/status", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"run_mode":"stdio","version":"9.9.9-test","project_id":"test-proxy-proj"}`))
	})
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)

	dir := t.TempDir()
	if key != "" {
		if err := WriteLocalProxyInfo(dir, LocalProxyInfo{Key: key, PID: 1, Port: 1, Version: "9.9.9-test"}); err != nil {
			t.Fatal(err)
		}
	}
	return NewLocalForwarder(ts.URL, dir, "window-b"), srv, ts, dir
}

func rawMsg(line string) StdioMessage {
	var req jsonRPCRequest
	_ = json.Unmarshal([]byte(line), &req)
	return StdioMessage{Raw: []byte(line), Method: req.Method, ID: req.ParsedID(), IsNotification: !req.HasID() || strings.HasPrefix(req.Method, "notifications/")}
}

func TestForwarderRoundTrip(t *testing.T) {
	fw, _, _, _ := startHubForForwarder(t, testLocalKey)

	info, err := fw.Probe(context.Background())
	if err != nil || info.RunMode != "stdio" || info.Version != "9.9.9-test" {
		t.Fatalf("Probe 结果不对：%+v %v", info, err)
	}
	if err := fw.CheckKey(); err != nil {
		t.Fatalf("钥匙应可读：%v", err)
	}

	resp, err := fw.Forward(context.Background(), rawMsg(`{"jsonrpc":"2.0","id":"abc","method":"tools/list"}`))
	if err != nil {
		t.Fatalf("转发请求失败：%v", err)
	}
	var parsed struct {
		ID     string `json:"id"`
		Result struct {
			Tools []any `json:"tools"`
		} `json:"result"`
	}
	if err := json.Unmarshal(resp, &parsed); err != nil || parsed.ID != "abc" || len(parsed.Result.Tools) == 0 {
		t.Fatalf("响应不对：%v %s", err, resp)
	}

	if resp, err := fw.Forward(context.Background(), rawMsg(`{"jsonrpc":"2.0","method":"notifications/initialized"}`)); err != nil || resp != nil {
		t.Fatalf("通知应返回 (nil, nil)，实际 %q %v", resp, err)
	}
}

func TestForwarderRereadsKeyAfterLeaderChange(t *testing.T) {
	fw, srv, _, dir := startHubForForwarder(t, testLocalKey)
	list := rawMsg(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
	if _, err := fw.Forward(context.Background(), list); err != nil {
		t.Fatalf("首次转发失败：%v", err)
	}

	// 主进程换人：新钥匙写进文件，旧钥匙作废
	newKey := "k-rotated-0123456789abcdef0123456789abcdef0123456789abcdef01234567"
	srv.SetLocalProxyKey(newKey)
	if err := WriteLocalProxyInfo(dir, LocalProxyInfo{Key: newKey, PID: 2, Port: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := fw.Forward(context.Background(), list); err != nil {
		t.Fatalf("换钥匙后应自动重读文件并成功：%v", err)
	}
}

func TestForwarderKeyRejectedWhenFileIsStale(t *testing.T) {
	fw, srv, _, _ := startHubForForwarder(t, testLocalKey)
	srv.SetLocalProxyKey("k-someone-else-0123456789abcdef0123456789abcdef0123456789abcdef")
	_, err := fw.Forward(context.Background(), rawMsg(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
	if !errors.Is(err, ErrKeyRejected) {
		t.Fatalf("文件里的钥匙对不上时应返回 ErrKeyRejected，实际 %v", err)
	}
}

func TestForwarderNoKeyFile(t *testing.T) {
	fw := NewLocalForwarder("http://127.0.0.1:1", t.TempDir(), "")
	if err := fw.CheckKey(); !errors.Is(err, ErrNoKey) {
		t.Fatalf("没有接入凭证文件应返回 ErrNoKey，实际 %v", err)
	}
	if _, err := fw.Forward(context.Background(), rawMsg(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)); !errors.Is(err, ErrNoKey) {
		t.Fatalf("转发时同样应返回 ErrNoKey，实际 %v", err)
	}
}

func TestForwarderUnreachable(t *testing.T) {
	fw, _, ts, _ := startHubForForwarder(t, testLocalKey)
	ts.Close() // 主进程没了：端口没人听
	_, err := fw.Forward(context.Background(), rawMsg(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
	if !errors.Is(err, ErrHubUnreachable) {
		t.Fatalf("端口没人听应返回 ErrHubUnreachable（请求一定没发出去），实际 %v", err)
	}
	if _, err := fw.Probe(context.Background()); !errors.Is(err, ErrHubUnreachable) {
		t.Fatalf("Probe 也应返回 ErrHubUnreachable，实际 %v", err)
	}
}

func TestForwarderHubLostMidFlight(t *testing.T) {
	var accepted sync.WaitGroup
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	accepted.Add(1)
	go func() {
		defer accepted.Done()
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		// 把请求读完（头部），然后不回响应直接断开：模拟主进程在处理中途退出
		_, _ = bufio.NewReader(conn).ReadString('\n')
		time.Sleep(50 * time.Millisecond)
		_ = conn.Close()
	}()

	dir := t.TempDir()
	if err := WriteLocalProxyInfo(dir, LocalProxyInfo{Key: testLocalKey}); err != nil {
		t.Fatal(err)
	}
	fw := NewLocalForwarder("http://"+ln.Addr().String(), dir, "")
	_, err = fw.Forward(context.Background(), rawMsg(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{}}`))
	if !errors.Is(err, ErrHubLost) {
		t.Fatalf("请求发出后连接断了应返回 ErrHubLost（不能自动重发），实际 %v", err)
	}
	if errors.Is(err, ErrHubUnreachable) {
		t.Fatalf("发出后断开不应被当作「没发出去」")
	}
	accepted.Wait()
}

func TestForwarderNotStdioHub(t *testing.T) {
	// 常驻服务 / 老版本：没有转发入口，/mcp-local 一律 404
	srv, _ := newLocalProxyTestServer(t, "")
	ts := httptest.NewServer(http.HandlerFunc(srv.ServeLocalProxy))
	defer ts.Close()
	dir := t.TempDir()
	if err := WriteLocalProxyInfo(dir, LocalProxyInfo{Key: testLocalKey}); err != nil {
		t.Fatal(err)
	}
	fw := NewLocalForwarder(ts.URL, dir, "")
	if _, err := fw.Forward(context.Background(), rawMsg(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)); !errors.Is(err, ErrNotStdioHub) {
		t.Fatalf("404 应返回 ErrNotStdioHub，实际 %v", err)
	}
}

func TestForwarderProbeNotRelayMesh(t *testing.T) {
	for name, handler := range map[string]http.HandlerFunc{
		"返回 404":   func(w http.ResponseWriter, r *http.Request) { http.NotFound(w, r) },
		"返回非 JSON": func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("hello")) },
	} {
		ts := httptest.NewServer(handler)
		fw := NewLocalForwarder(ts.URL, t.TempDir(), "")
		if _, err := fw.Probe(context.Background()); !errors.Is(err, ErrNotStdioHub) {
			t.Fatalf("%s：应返回 ErrNotStdioHub，实际 %v", name, err)
		}
		ts.Close()
	}
}

func TestForwarderCancelClosesTheRequest(t *testing.T) {
	blocked := make(chan struct{})
	released := make(chan struct{})
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 与真实的 ServeLocalProxy 一致：先把请求体读完。net/http 只有读完请求体才会开始监视连接，
		// 客户端断开时请求上下文才会被取消。
		_, _ = io.Copy(io.Discard, r.Body)
		close(blocked)
		<-r.Context().Done() // 主进程那头：请求上下文随连接断开而取消
		close(released)
	}))
	defer ts.Close()
	dir := t.TempDir()
	if err := WriteLocalProxyInfo(dir, LocalProxyInfo{Key: testLocalKey}); err != nil {
		t.Fatal(err)
	}
	fw := NewLocalForwarder(ts.URL, dir, "")

	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		_, err := fw.Forward(ctx, rawMsg(`{"jsonrpc":"2.0","id":9,"method":"tools/call","params":{}}`))
		result <- err
	}()
	<-blocked
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("取消后应返回 context.Canceled，实际 %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatalf("取消后转发应立刻返回")
	}
	select {
	case <-released:
	case <-time.After(3 * time.Second):
		t.Fatalf("主进程那头应感知到连接断开")
	}
}

func TestForwarderSendsHostNameAndKeyHeaders(t *testing.T) {
	var gotHost, gotKey, gotType, gotBody string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHost = r.Header.Get(LocalProxyHostHeader)
		gotKey = r.Header.Get(LocalProxyKeyHeader)
		gotType = r.Header.Get("Content-Type")
		buf := new(strings.Builder)
		_, _ = bufio.NewReader(r.Body).WriteTo(buf)
		gotBody = buf.String()
		w.WriteHeader(http.StatusAccepted)
	}))
	defer ts.Close()
	dir := t.TempDir()
	if err := WriteLocalProxyInfo(dir, LocalProxyInfo{Key: testLocalKey}); err != nil {
		t.Fatal(err)
	}
	fw := NewLocalForwarder(ts.URL, dir, "window-b")
	line := `{"jsonrpc":"2.0","method":"notifications/initialized"}`
	if _, err := fw.Forward(context.Background(), rawMsg(line)); err != nil {
		t.Fatal(err)
	}
	if gotHost != "window-b" || gotKey != testLocalKey || gotType != "application/json" || gotBody != line {
		t.Fatalf("请求头 / 请求体不对：host=%q type=%q body=%q（钥匙是否带上：%v）", gotHost, gotType, gotBody, gotKey == testLocalKey)
	}
}
