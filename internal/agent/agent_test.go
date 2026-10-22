package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/coder/websocket"
	"github.com/teacat99/RelayMesh/v2/internal/agent/ptyhost"
	"github.com/teacat99/RelayMesh/v2/internal/termproto"
)

func mustIdentity(t *testing.T) *Identity {
	t.Helper()
	id, err := NewTemporaryIdentity()
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func newAgentFor(t *testing.T, target ServerTarget, mod func(*Options)) *Agent {
	t.Helper()
	opts := Options{
		Server:   target,
		Trust:    TrustOptions{Insecure: true},
		Identity: mustIdentity(t),
		Spawner:  &ptyhost.FakeSpawner{},
		Jitter:   func(d time.Duration) time.Duration { return d },
	}
	if mod != nil {
		mod(&opts)
	}
	a, err := New(opts)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

// Options.Rev 为空时沿用包级 Rev（main 把构建时注入的指纹赋给它）；显式给出的优先；仍为空表示非发布构建。
func TestNewRevFallsBackToPackageRev(t *testing.T) {
	target, _ := ParseServer("https://relay.example.com")
	build := func(rev string) string {
		t.Helper()
		a, err := New(Options{Server: target, Identity: mustIdentity(t), Spawner: &ptyhost.FakeSpawner{}, Rev: rev})
		if err != nil {
			t.Fatal(err)
		}
		return a.opts.Rev
	}
	prev := Rev
	t.Cleanup(func() { Rev = prev })

	Rev = ""
	if got := build(""); got != "" {
		t.Fatalf("两处都没有指纹应为空（非发布构建）: %q", got)
	}
	Rev = "0123456789ab"
	if got := build(""); got != "0123456789ab" {
		t.Fatalf("Options.Rev 为空应沿用包级 Rev: %q", got)
	}
	if got := build("72ae54bd8799"); got != "72ae54bd8799" {
		t.Fatalf("显式给出的指纹优先: %q", got)
	}
}

func TestNewValidatesOptions(t *testing.T) {
	plain, _ := ParseServer("http://10.0.0.5:18775")
	secure, _ := ParseServer("https://relay.example.com")
	id := mustIdentity(t)

	cases := []struct {
		name string
		opts Options
	}{
		{"缺少身份", Options{Server: secure}},
		{"缺少服务端地址", Options{Identity: id}},
		{"明文且没有 insecure", Options{Server: plain, Identity: id}},
		{"指纹非法", Options{Server: secure, Identity: id, Trust: TrustOptions{Pins: []string{"zzz"}}}},
	}
	for _, c := range cases {
		if a, err := New(c.opts); err == nil {
			t.Errorf("%s: 配置错误应在 New 时失败（而不是运行时无限重试），得到 %v", c.name, a)
		}
	}
	a, err := New(Options{Server: secure, Identity: id})
	if err != nil {
		t.Fatalf("合法配置应通过: %v", err)
	}
	if a.MachineCode() != id.MachineCode() {
		t.Fatal("机器码应取自身份")
	}
}

func TestRetryDelayPolicy(t *testing.T) {
	a := newAgentFor(t, ServerTarget{URL: "ws://127.0.0.1:1" + AgentWSPath, Host: "127.0.0.1:1"}, func(o *Options) {
		o.MinBackoff = time.Second
		o.MaxBackoff = 8 * time.Second
		o.SlowRetry = time.Minute
		o.DisabledRetry = 10 * time.Minute
	})

	// 网络类失败：指数退避并封顶。
	attempt := 0
	netErr := connResult{err: errors.New("connection refused")}
	want := []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second, 8 * time.Second, 8 * time.Second}
	for i, w := range want {
		if got := a.retryDelay(netErr, &attempt); got != w {
			t.Fatalf("第 %d 次退避 = %v，期望 %v", i+1, got, w)
		}
	}
	if attempt != len(want) {
		t.Fatalf("attempt 计数 = %d", attempt)
	}
	// 极端大的 attempt 不能因位移溢出而得到 0 / 负数。
	attempt = 1000
	if got := a.retryDelay(netErr, &attempt); got != 8*time.Second {
		t.Fatalf("attempt 很大时应封顶，得到 %v", got)
	}

	// 慢节奏类：握手无效 / 总闸关闭 / 发现关闭 / 被取代——不增加 attempt。
	attempt = 0
	for _, code := range []int{termproto.CloseInvalidHandshake, termproto.CloseGateClosed, termproto.CloseUnknownMachine, termproto.CloseSuperseded} {
		if got := a.retryDelay(connResult{code: websocket.StatusCode(code)}, &attempt); got != time.Minute {
			t.Errorf("code %d 应慢速重试，得到 %v", code, got)
		}
	}
	if attempt != 0 {
		t.Fatalf("慢节奏不应累计退避: %d", attempt)
	}
	if got := a.retryDelay(connResult{code: termproto.CloseDisabled}, &attempt); got != 10*time.Minute {
		t.Errorf("被停用应最慢重试，得到 %v", got)
	}
	got := a.retryDelay(connResult{code: termproto.CloseRateLimited}, &attempt)
	if got < time.Minute || got >= 2*time.Minute {
		t.Errorf("限流应在 [Slow, 2*Slow) 内随机错峰，得到 %v", got)
	}
	// 握手阶段的协议性失败（只有原因、没有错误与关闭码）：重试无意义，按慢节奏。
	if got := a.retryDelay(connResult{reason: "协议版本不兼容"}, &attempt); got != time.Minute {
		t.Errorf("协议性失败应慢速重试，得到 %v", got)
	}
}

func TestClassifyDialError(t *testing.T) {
	cases := []struct {
		status   int
		wantCode int
		wantText string
	}{
		{http.StatusForbidden, termproto.CloseGateClosed, "总闸"},
		{http.StatusTooManyRequests, termproto.CloseRateLimited, "限流"},
		{http.StatusNotFound, termproto.CloseInvalidHandshake, "版本过低"},
		{http.StatusServiceUnavailable, 0, ""}, // 服务端正在重启：当作网络错误快速重试
		{http.StatusBadGateway, 0, ""},         // 反向代理后端未就绪
	}
	for _, c := range cases {
		res := classifyDialError(&http.Response{StatusCode: c.status}, errors.New("bad handshake"))
		if int(res.code) != c.wantCode {
			t.Errorf("HTTP %d: code = %d，期望 %d", c.status, res.code, c.wantCode)
		}
		if c.wantText != "" && !strings.Contains(res.reason, c.wantText) {
			t.Errorf("HTTP %d: 原因应包含 %q，得到 %q", c.status, c.wantText, res.reason)
		}
		if c.wantCode == 0 && res.reason != "" {
			t.Errorf("HTTP %d: 不应给出协议性原因: %q", c.status, res.reason)
		}
	}
	if res := classifyDialError(nil, errors.New("dial tcp: refused")); res.code != 0 || res.err == nil {
		t.Errorf("没有响应时应保留原始错误: %+v", res)
	}
}

func TestConnResultDescribe(t *testing.T) {
	if got := (connResult{reason: "原因"}).describe(); got != "原因" {
		t.Error(got)
	}
	if got := (connResult{code: 4405}).describe(); !strings.Contains(got, "4405") {
		t.Error(got)
	}
	long := strings.Repeat("x", 1000)
	if got := (connResult{err: errors.New(long)}).describe(); utf8.RuneCountInString(got) > 420 {
		t.Errorf("错误文本应被截断，长度 %d", utf8.RuneCountInString(got))
	}

	// 网络库的层层前缀不能把真正的原因挤出显示长度。
	inner := errors.New("服务端证书指纹 sha256:abc 与 --pin 不匹配，已拒绝连接")
	wrapped := fmt.Errorf("failed to WebSocket dial: failed to send handshake request: %w",
		&url.Error{Op: "Get", URL: "https://relay.example.com" + AgentWSPath, Err: inner})
	got := (connResult{err: wrapped}).describe()
	if !strings.Contains(got, "不匹配") || strings.Contains(got, "handshake request") || strings.Contains(got, "https://") {
		t.Errorf("应只保留根因，得到 %q", got)
	}
	// 非 url.Error 的错误原样保留。
	if got := (connResult{err: errors.New("expected handshake response status code 101 but got 400")}).describe(); !strings.Contains(got, "101") {
		t.Errorf("非 url.Error 应原样保留: %q", got)
	}
}

// ---------- 握手：与一个脚本化的假 Hub 对话 ----------

func stubHub(t *testing.T, script func(ctx context.Context, ws *websocket.Conn, r *http.Request)) ServerTarget {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{
			Subprotocols:    []string{termproto.SubprotocolAgent},
			CompressionMode: websocket.CompressionDisabled,
		})
		if err != nil {
			return
		}
		defer ws.CloseNow()
		script(r.Context(), ws, r)
	}))
	t.Cleanup(srv.Close)
	target, err := ParseServer(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	return target
}

func sendJSON(t *testing.T, ctx context.Context, ws *websocket.Conn, v any) {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Error(err)
		return
	}
	if err := ws.Write(ctx, websocket.MessageText, b); err != nil {
		t.Errorf("stub 写入失败: %v", err)
	}
}

func challenge(t *testing.T, version int) (termproto.Challenge, []byte) {
	t.Helper()
	nonce, err := termproto.NewNonce()
	if err != nil {
		t.Fatal(err)
	}
	return termproto.Challenge{T: termproto.TChallenge, V: version, Nonce: termproto.EncodeKey(nonce)}, nonce
}

func connectWith(t *testing.T, a *Agent) connResult {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	a.mgr.Start()
	defer a.mgr.Shutdown(time.Second)
	done := make(chan connResult, 1)
	go func() { done <- a.connectOnce(ctx) }()
	select {
	case r := <-done:
		return r
	case <-time.After(10 * time.Second):
		t.Fatal("connectOnce 超时")
		return connResult{}
	}
}

func TestHandshakeProvesIdentityAndSendsNoCredentials(t *testing.T) {
	var (
		mu       sync.Mutex
		gotHello termproto.Hello
		headers  http.Header
		subproto string
	)
	target := stubHub(t, func(ctx context.Context, ws *websocket.Conn, r *http.Request) {
		mu.Lock()
		headers = r.Header.Clone()
		subproto = ws.Subprotocol()
		mu.Unlock()

		ch, nonce := challenge(t, termproto.ProtocolVersion)
		sendJSON(t, ctx, ws, ch)
		_, data, err := ws.Read(ctx)
		if err != nil {
			return
		}
		var h termproto.Hello
		if err := json.Unmarshal(data, &h); err != nil {
			t.Errorf("hello 不是合法 JSON: %v", err)
			return
		}
		mu.Lock()
		gotHello = h
		mu.Unlock()

		pub, err := termproto.DecodeKey(h.Pub, 32)
		if err != nil {
			t.Errorf("hello.pub 无法解码: %v", err)
			return
		}
		sig, err := termproto.DecodeKey(h.Sig, 64)
		if err != nil || !termproto.VerifyChallenge(pub, nonce, sig) {
			t.Errorf("hello 的签名必须能用公钥验证（err=%v）", err)
		}
		sendJSON(t, ctx, ws, termproto.Welcome{T: termproto.TWelcome, Status: termproto.WelcomePending, MachineCode: termproto.MachineCode(pub)})
		_ = ws.Close(websocket.StatusCode(termproto.CloseUnknownMachine), "bye")
	})

	var events []Event
	var emu sync.Mutex
	a := newAgentFor(t, target, func(o *Options) {
		o.Version = "9.9.9-test"
		o.Rev = "72ae54bd8799"
		o.OnEvent = func(ev Event) { emu.Lock(); events = append(events, ev); emu.Unlock() }
	})
	res := connectWith(t, a)

	if !res.welcomed {
		t.Fatalf("握手应已完成: %+v", res)
	}
	if int(res.code) != termproto.CloseUnknownMachine {
		t.Fatalf("应记录对端关闭码，得到 %d", res.code)
	}

	mu.Lock()
	defer mu.Unlock()
	if subproto != termproto.SubprotocolAgent {
		t.Errorf("协商的子协议 = %q", subproto)
	}
	// 通用入口的安全前提：Agent 不带任何凭据，也不带 Origin（浏览器才有 Origin，Hub 据此拒绝浏览器）。
	for _, h := range []string{"Authorization", "Cookie", "Origin"} {
		if headers.Get(h) != "" {
			t.Errorf("Agent 不应发送 %s 头", h)
		}
	}
	if !strings.HasPrefix(headers.Get("User-Agent"), "relaymesh-agent/") {
		t.Errorf("User-Agent = %q", headers.Get("User-Agent"))
	}
	if gotHello.V != termproto.ProtocolVersion || gotHello.Meta.Ver != "9.9.9-test" || gotHello.Meta.Kind != termproto.KindTemporary {
		t.Errorf("hello 内容不对: %+v", gotHello)
	}
	if gotHello.Meta.OS == "" || gotHello.Meta.Arch == "" {
		t.Errorf("hello.meta 应带系统信息: %+v", gotHello.Meta)
	}
	if gotHello.Meta.Rev != "72ae54bd8799" {
		t.Errorf("hello.meta 应带 Agent 侧代码指纹: %+v", gotHello.Meta)
	}
	emu.Lock()
	defer emu.Unlock()
	var sawPending bool
	for _, ev := range events {
		if ev.State == StatePending && ev.Detail == a.MachineCode() {
			sawPending = true
		}
		if ev.State == StateOnline {
			t.Error("pending 状态下不应报告 online")
		}
	}
	if !sawPending {
		t.Errorf("应报告 pending 并带上机器码: %+v", events)
	}
}

func TestHandshakeFailures(t *testing.T) {
	cases := []struct {
		name       string
		script     func(ctx context.Context, ws *websocket.Conn)
		wantReason string
	}{
		{
			name: "协议版本不兼容",
			script: func(ctx context.Context, ws *websocket.Conn) {
				ch, _ := challenge(t, 99)
				sendJSON(t, ctx, ws, ch)
				_, _, _ = ws.Read(ctx)
			},
			wantReason: "协议版本不兼容",
		},
		{
			name: "第一帧不是 JSON",
			script: func(ctx context.Context, ws *websocket.Conn) {
				_ = ws.Write(ctx, websocket.MessageText, []byte("hello there"))
				_, _, _ = ws.Read(ctx)
			},
			wantReason: "无法识别",
		},
		{
			name: "第一帧是二进制",
			script: func(ctx context.Context, ws *websocket.Conn) {
				_ = ws.Write(ctx, websocket.MessageBinary, []byte{1, 2, 3})
				_, _, _ = ws.Read(ctx)
			},
			wantReason: "不是文本",
		},
		{
			name: "随机数格式错误",
			script: func(ctx context.Context, ws *websocket.Conn) {
				sendJSON(t, ctx, ws, termproto.Challenge{T: termproto.TChallenge, V: termproto.ProtocolVersion, Nonce: "!!!"})
				_, _, _ = ws.Read(ctx)
			},
			wantReason: "随机数",
		},
		{
			name: "服务端算出的机器码与本机不一致",
			script: func(ctx context.Context, ws *websocket.Conn) {
				ch, _ := challenge(t, termproto.ProtocolVersion)
				sendJSON(t, ctx, ws, ch)
				if _, _, err := ws.Read(ctx); err != nil {
					return
				}
				sendJSON(t, ctx, ws, termproto.Welcome{T: termproto.TWelcome, Status: termproto.WelcomeEnabled, MachineCode: "RM-AAAA-AAAA-AAAA-AAAA"})
				_, _, _ = ws.Read(ctx)
			},
			wantReason: "不一致",
		},
		{
			name: "未知的 welcome 状态",
			script: func(ctx context.Context, ws *websocket.Conn) {
				ch, _ := challenge(t, termproto.ProtocolVersion)
				sendJSON(t, ctx, ws, ch)
				_, data, err := ws.Read(ctx)
				if err != nil {
					return
				}
				var h termproto.Hello
				_ = json.Unmarshal(data, &h)
				pub, _ := termproto.DecodeKey(h.Pub, 32)
				sendJSON(t, ctx, ws, termproto.Welcome{T: termproto.TWelcome, Status: "banana", MachineCode: termproto.MachineCode(pub)})
				_, _, _ = ws.Read(ctx)
			},
			wantReason: "未知的状态",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			target := stubHub(t, func(ctx context.Context, ws *websocket.Conn, _ *http.Request) { c.script(ctx, ws) })
			a := newAgentFor(t, target, nil)
			res := connectWith(t, a)
			if res.welcomed {
				t.Fatal("握手失败不应视为已完成")
			}
			if !strings.Contains(res.reason, c.wantReason) {
				t.Fatalf("原因应包含 %q，得到 %q（err=%v）", c.wantReason, res.reason, res.err)
			}
			// 协议性失败不能被当成网络抖动而高频重试。
			attempt := 0
			if d := a.retryDelay(res, &attempt); d < a.opts.SlowRetry/2 {
				t.Fatalf("协议性失败应慢速重试，得到 %v", d)
			}
		})
	}
}

func TestRunStopsPromptlyAndShutsDownSessions(t *testing.T) {
	// 对端永远不回应握手：Run 也必须能被 ctx 及时取消，且不残留 goroutine。
	target := stubHub(t, func(ctx context.Context, ws *websocket.Conn, _ *http.Request) {
		<-ctx.Done()
	})
	a := newAgentFor(t, target, func(o *Options) {
		o.MinBackoff = 10 * time.Millisecond
		o.MaxBackoff = 20 * time.Millisecond
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- a.Run(ctx) }()

	time.Sleep(100 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("取消后 Run 应正常返回: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Run 未在 ctx 取消后及时返回")
	}
}
