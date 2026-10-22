package ntfy

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// recorder 记录收到的每个请求，供断言。
type recorder struct {
	mu    sync.Mutex
	calls []recorded
}

type recorded struct {
	Method string
	Path   string
	Header http.Header
	Body   publishBody
	Raw    string
}

func (r *recorder) add(req *http.Request) recorded {
	body, _ := io.ReadAll(req.Body)
	rec := recorded{Method: req.Method, Path: req.URL.Path, Header: req.Header.Clone(), Raw: string(body)}
	_ = json.Unmarshal([]byte(rec.Raw), &rec.Body)
	r.mu.Lock()
	r.calls = append(r.calls, rec)
	r.mu.Unlock()
	return rec
}

func (r *recorder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.calls)
}

func (r *recorder) last() recorded {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls[len(r.calls)-1]
}

// fastSender 是不等待的发送器：重试之间只隔一毫秒。
func fastSender(retries int) *Sender {
	s := NewSender("RelayMesh-test")
	// 测试要与开发者机器上的代理环境变量无关：直连本地的测试服务。
	s.client.Transport.(*http.Transport).Proxy = nil
	s.Backoff = make([]time.Duration, retries)
	for i := range s.Backoff {
		s.Backoff[i] = time.Millisecond
	}
	return s
}

func testCfg(server string) Config {
	return Config{Enabled: true, Server: server, Topic: "topic-1", Priority: 4}.Normalize()
}

func TestSend_PublishesJSONWithAuth(t *testing.T) {
	rec := &recorder{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.add(r)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"x"}`))
	}))
	defer srv.Close()

	cfg := testCfg(srv.URL)
	cfg.Token = "tk_secret"
	res, err := fastSender(0).Send(context.Background(), cfg, Message{
		Title: "智能体在等你回复", Body: "第十批：方案确认\n项目：RelayMesh", Priority: 4, Tags: []string{"speech_balloon"}, Click: "https://rm.example.com/",
	})
	if err != nil {
		t.Fatalf("推送失败：%v", err)
	}
	if res.Status != 200 || res.Attempts != 1 {
		t.Errorf("结果不对：%+v", res)
	}

	got := rec.last()
	if got.Method != http.MethodPost || got.Path != "/" {
		t.Errorf("应向服务根地址 POST：%s %s", got.Method, got.Path)
	}
	if ct := got.Header.Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type 不对：%q", ct)
	}
	if got.Header.Get("Authorization") != "Bearer tk_secret" {
		t.Errorf("应带 Bearer 令牌：%q", got.Header.Get("Authorization"))
	}
	if got.Header.Get("User-Agent") != "RelayMesh-test" {
		t.Errorf("User-Agent 不对：%q", got.Header.Get("User-Agent"))
	}
	b := got.Body
	if b.Topic != "topic-1" || b.Title != "智能体在等你回复" || b.Message != "第十批：方案确认\n项目：RelayMesh" ||
		b.Priority != 4 || len(b.Tags) != 1 || b.Tags[0] != "speech_balloon" || b.Click != "https://rm.example.com/" {
		t.Errorf("请求体内容不对（中文应原样往返）：%+v", b)
	}
	// 主题只在 JSON 里，URL 与请求头里都不应出现——它在公共服务上相当于密码。
	if strings.Contains(got.Path, "topic-1") {
		t.Error("主题不应出现在 URL 里")
	}
}

func TestSend_BasicAuthAndNoAuth(t *testing.T) {
	rec := &recorder{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { rec.add(r) }))
	defer srv.Close()

	cfg := testCfg(srv.URL)
	if _, err := fastSender(0).Send(context.Background(), cfg, Message{Body: "x"}); err != nil {
		t.Fatal(err)
	}
	if got := rec.last().Header.Get("Authorization"); got != "" {
		t.Errorf("没配认证时不应带 Authorization：%q", got)
	}

	cfg.Username, cfg.Password = "bob", "pw"
	if _, err := fastSender(0).Send(context.Background(), cfg, Message{Body: "x"}); err != nil {
		t.Fatal(err)
	}
	if got := rec.last().Header.Get("Authorization"); got != "Basic Ym9iOnB3" {
		t.Errorf("应走 Basic：%q", got)
	}
}

func TestSend_KeepsServerPathPrefix(t *testing.T) {
	rec := &recorder{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { rec.add(r) }))
	defer srv.Close()
	cfg := testCfg(srv.URL + "/ntfy")
	if _, err := fastSender(0).Send(context.Background(), cfg, Message{Body: "x"}); err != nil {
		t.Fatal(err)
	}
	if got := rec.last().Path; got != "/ntfy/" {
		t.Errorf("带路径前缀的服务应发往 /ntfy/：%q", got)
	}
}

func TestSend_AuthFailureNotRetriedAndScrubbed(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.WriteHeader(http.StatusForbidden)
		// 恶意或有 bug 的服务把收到的令牌回显在错误里：展示给用户之前必须抹掉。
		_, _ = w.Write([]byte(`{"code":40301,"http":403,"error":"forbidden: bad token tk_secret\u0007 / pass hunter2"}`))
	}))
	defer srv.Close()

	cfg := testCfg(srv.URL)
	cfg.Token = "tk_secret"
	cfg.Password = "hunter2"
	cfg.Username = "bob"
	_, err := fastSender(2).Send(context.Background(), cfg, Message{Body: "x"})
	se, ok := err.(*SendError)
	if !ok {
		t.Fatalf("应返回 *SendError：%T %v", err, err)
	}
	if se.Status != 403 || se.Retryable {
		t.Errorf("403 不应重试：%+v", se)
	}
	if atomic.LoadInt32(&hits) != 1 {
		t.Errorf("认证失败只应请求一次，实际 %d 次", hits)
	}
	if !strings.Contains(se.Msg, "认证失败") {
		t.Errorf("应说明是认证问题：%s", se.Msg)
	}
	for _, secret := range []string{"tk_secret", "hunter2"} {
		if strings.Contains(se.Msg, secret) {
			t.Fatalf("错误信息泄露了凭据 %q：%s", secret, se.Msg)
		}
	}
	if strings.ContainsRune(se.Msg, '\u0007') {
		t.Errorf("错误信息里不应有控制字符：%q", se.Msg)
	}
}

func TestSend_RetriesServerErrorsThenSucceeds(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&hits, 1) < 3 {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	res, err := fastSender(2).Send(context.Background(), testCfg(srv.URL), Message{Body: "x"})
	if err != nil {
		t.Fatalf("两次 502 之后应成功：%v", err)
	}
	if res.Attempts != 3 {
		t.Errorf("应尝试 3 次：%+v", res)
	}
}

func TestSend_GivesUpAfterRetries(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	res, err := fastSender(2).Send(context.Background(), testCfg(srv.URL), Message{Body: "x"})
	se, _ := err.(*SendError)
	if se == nil || !se.Retryable || se.Status != 503 {
		t.Fatalf("应以可重试的 503 失败：%v", err)
	}
	if res.Attempts != 3 || atomic.LoadInt32(&hits) != 3 {
		t.Errorf("1 次 + 2 次重试 = 3 次：%+v hits=%d", res, hits)
	}
}

func TestSend_RateLimitedHonorsRetryAfter(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&hits, 1) == 1 {
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	start := time.Now()
	res, err := fastSender(1).Send(context.Background(), testCfg(srv.URL), Message{Body: "x"})
	if err != nil || res.Attempts != 2 {
		t.Fatalf("429 之后应重试成功：%v %+v", err, res)
	}
	if time.Since(start) < 900*time.Millisecond {
		t.Errorf("应按 Retry-After 等够 1 秒，只等了 %s", time.Since(start))
	}
}

func TestSend_RedirectIsNotFollowed(t *testing.T) {
	var targetHits int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&targetHits, 1)
	}))
	defer target.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+"/", http.StatusMovedPermanently)
	}))
	defer srv.Close()

	cfg := testCfg(srv.URL)
	cfg.Token = "tk_secret"
	_, err := fastSender(2).Send(context.Background(), cfg, Message{Body: "x"})
	se, _ := err.(*SendError)
	if se == nil || se.Status != 301 || se.Retryable {
		t.Fatalf("重定向应按失败处理且不重试：%v", err)
	}
	if !strings.Contains(se.Msg, "重定向") || !strings.Contains(se.Msg, "https://") {
		t.Errorf("应提示改用最终地址：%s", se.Msg)
	}
	if atomic.LoadInt32(&targetHits) != 0 {
		t.Error("不应跟随重定向（认证头会被带去别处）")
	}
}

func TestSend_HTMLErrorPageNotShown(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte("<html><body><h1>404 Not Found</h1>nginx</body></html>"))
	}))
	defer srv.Close()

	_, err := fastSender(0).Send(context.Background(), testCfg(srv.URL), Message{Body: "x"})
	se, _ := err.(*SendError)
	if se == nil || se.Status != 404 {
		t.Fatalf("应是 404：%v", err)
	}
	if strings.Contains(se.Msg, "<html>") || strings.Contains(se.Msg, "nginx") {
		t.Errorf("不是 ntfy 形状的响应体不应展示：%s", se.Msg)
	}
	if !strings.Contains(se.Msg, "服务地址") {
		t.Errorf("应提示检查服务地址：%s", se.Msg)
	}
}

func TestSend_ShowsNtfyErrorDetail(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"code":40001,"http":400,"error":"topic invalid"}`))
	}))
	defer srv.Close()
	_, err := fastSender(0).Send(context.Background(), testCfg(srv.URL), Message{Body: "x"})
	if err == nil || !strings.Contains(err.Error(), "topic invalid") {
		t.Fatalf("应带上 ntfy 返回的说明：%v", err)
	}
}

func TestSend_TimeoutIsRetryable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(300 * time.Millisecond)
	}))
	defer srv.Close()

	s := fastSender(1)
	s.client.Timeout = 80 * time.Millisecond
	res, err := s.Send(context.Background(), testCfg(srv.URL), Message{Body: "x"})
	se, _ := err.(*SendError)
	if se == nil || !se.Retryable || !strings.Contains(se.Msg, "超时") {
		t.Fatalf("应是可重试的超时：%v", err)
	}
	if res.Attempts != 2 {
		t.Errorf("应重试一次：%+v", res)
	}
}

func TestSend_ConnectionRefused(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	url := srv.URL
	srv.Close() // 关掉之后端口无人监听

	_, err := fastSender(1).Send(context.Background(), testCfg(url), Message{Body: "x"})
	se, _ := err.(*SendError)
	if se == nil || !se.Retryable || !strings.Contains(se.Msg, "连接 ntfy 服务失败") {
		t.Fatalf("应是可重试的连接失败：%v", err)
	}
	// 给用户看的文字里不应带完整的请求 URL（里面有服务地址的路径等）。
	if strings.Contains(se.Msg, `Post "`) {
		t.Errorf("不应带出请求 URL：%s", se.Msg)
	}
}

func TestSend_UntrustedCertificate(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()

	s := fastSender(2)
	_, err := s.Send(context.Background(), testCfg(srv.URL), Message{Body: "x"})
	se, _ := err.(*SendError)
	if se == nil || se.Retryable || !strings.Contains(se.Msg, "TLS 证书") {
		t.Fatalf("自签名证书应以不可重试的 TLS 错误失败：%v", err)
	}
}

func TestSend_BlocksLinkLocalAndUnspecified(t *testing.T) {
	for _, target := range []string{"http://169.254.169.254", "http://169.254.1.1:8080", "http://0.0.0.0:9"} {
		t.Run(target, func(t *testing.T) {
			s := fastSender(2)
			s.client.Timeout = 2 * time.Second
			res, err := s.Send(context.Background(), testCfg(target), Message{Body: "x"})
			se, _ := err.(*SendError)
			if se == nil || se.Retryable || !strings.Contains(se.Msg, "被禁止") {
				t.Fatalf("应以不可重试的「被禁止的地址」失败：%v", err)
			}
			if res.Attempts != 1 {
				t.Errorf("被禁止的地址不应重试：%+v", res)
			}
		})
	}
}

func TestBlockedAddr(t *testing.T) {
	cases := map[string]bool{
		"169.254.169.254":        true, // 云厂商元数据
		"169.254.0.1":            true, // 链路本地
		"fe80::1":                true, // IPv6 链路本地
		"::ffff:169.254.169.254": true, // 映射成 IPv6 的 IPv4 元数据地址
		"0.0.0.0":                true,
		"::":                     true,
		"224.0.0.1":              true, // 组播
		"ff02::1":                true,
		"127.0.0.1":              false, // 本机：自建 ntfy 常见
		"::1":                    false,
		"10.1.2.3":               false, // 内网
		"192.168.1.5":            false,
		"172.17.0.2":             false, // Docker 网络
		"198.18.0.206":           false, // fake-ip 代理地址
		"8.8.8.8":                false,
	}
	for ip, want := range cases {
		if got := blockedAddr(netip.MustParseAddr(ip)); got != want {
			t.Errorf("%s 的判断应为 %v，实际 %v", ip, want, got)
		}
	}
}

func TestSend_ContextCancelStopsRetries(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	s := NewSender("t")
	s.Backoff = []time.Duration{5 * time.Second, 5 * time.Second}
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(60 * time.Millisecond); cancel() }()

	start := time.Now()
	_, err := s.Send(ctx, testCfg(srv.URL), Message{Body: "x"})
	if err == nil {
		t.Fatal("应以失败告终")
	}
	if time.Since(start) > 2*time.Second {
		t.Errorf("取消后应立刻放弃重试，却等了 %s", time.Since(start))
	}
	if atomic.LoadInt32(&hits) != 1 {
		t.Errorf("取消后不应再请求：%d", hits)
	}
}
