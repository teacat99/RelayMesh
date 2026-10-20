package files

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/teacat99/RelayMesh/v2/internal/termproto"
)

type pushEnv struct {
	t       *testing.T
	root    string
	outside string
	s       *Service
	roots   []string
}

func newPushEnv(t *testing.T, policy URLPolicy) *pushEnv {
	t.Helper()
	root, outside := layout(t)
	return &pushEnv{t: t, root: root, outside: outside, roots: []string{root}, s: newTestService(t, Config{URL: policy})}
}

func (e *pushEnv) push(destRel, rawURL string, o PushOpts) (PushResult, *Error) {
	e.t.Helper()
	return e.s.PushURL(context.Background(), e.roots, filepath.Join(e.root, destRel), rawURL, o)
}

// names 列出目录里的文件名（用来确认没有残留的临时文件）。
func names(t *testing.T, dir string) []string {
	t.Helper()
	ents, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range ents {
		out = append(out, e.Name())
	}
	sort.Strings(out)
	return out
}

func allowLocal() URLPolicy { return URLPolicy{AllowPrivate: true} }

func serve(t *testing.T, h http.HandlerFunc) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv
}

func body(content string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(content)) }
}

func sum(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

func TestPushURLHappyPath(t *testing.T) {
	srv := serve(t, body("payload-data"))
	e := newPushEnv(t, allowLocal())
	res, err := e.push("sub/out.bin", srv.URL+"/path/file?sig=SECRETSIG", PushOpts{})
	wantOK(t, err)
	if res.Size != 12 || res.SHA256 != sum("payload-data") || res.Status != 200 || res.Redirects != 0 {
		t.Fatalf("result = %+v", res)
	}
	if strings.Contains(res.Source, "SECRETSIG") || strings.Contains(res.Source, "/path") || res.Source != "http://"+srv.Listener.Addr().String() {
		t.Fatalf("Source 不应包含路径与查询串: %q", res.Source)
	}
	got, rerr := os.ReadFile(filepath.Join(e.root, "sub", "out.bin"))
	if rerr != nil || string(got) != "payload-data" {
		t.Fatalf("file = %q, %v", got, rerr)
	}
	if n := names(t, filepath.Join(e.root, "sub")); len(n) != 1 || n[0] != "out.bin" {
		t.Fatalf("目录里不应有残留的临时文件: %v", n)
	}
	if fi, _ := os.Stat(filepath.Join(e.root, "sub", "out.bin")); fi != nil && !isWindows() && fi.Mode().Perm() != 0o600 {
		t.Fatalf("新文件应仅属主可读写，实际 %v", fi.Mode().Perm())
	}
}

func isWindows() bool { return runtime.GOOS == "windows" }

func TestPushURLOverwriteSemantics(t *testing.T) {
	srv := serve(t, body("new"))
	e := newPushEnv(t, allowLocal())
	target := filepath.Join(e.root, "t.sh")
	if err := os.WriteFile(target, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	_, err := e.push("t.sh", srv.URL, PushOpts{})
	wantCode(t, err, termproto.FileErrExists)
	if b, _ := os.ReadFile(target); string(b) != "old" {
		t.Fatalf("未允许覆盖时不应改动原文件: %q", b)
	}
	for _, x := range names(t, e.root) {
		if strings.HasPrefix(x, ".rmh-") {
			t.Fatalf("残留临时文件: %v", names(t, e.root))
		}
	}
	_, err = e.push("t.sh", srv.URL, PushOpts{Overwrite: true})
	wantOK(t, err)
	if b, _ := os.ReadFile(target); string(b) != "new" {
		t.Fatalf("覆盖后内容 = %q", b)
	}
	if fi, _ := os.Stat(target); fi != nil && !isWindows() && fi.Mode().Perm() != 0o755 {
		t.Fatalf("覆盖应保留原权限位，实际 %v", fi.Mode().Perm())
	}
}

func TestPushURLDestinationChecks(t *testing.T) {
	srv := serve(t, body("data"))
	e := newPushEnv(t, allowLocal())
	_, err := e.push("sub", srv.URL, PushOpts{Overwrite: true})
	wantCode(t, err, termproto.FileErrIsDir)
	_, err = e.s.PushURL(context.Background(), e.roots, e.root, srv.URL, PushOpts{Overwrite: true})
	wantCode(t, err, termproto.FileErrIsDir)
	_, err = e.push("missing/dir/out", srv.URL, PushOpts{})
	wantCode(t, err, termproto.FileErrNotFound)
	_, err = e.s.PushURL(context.Background(), e.roots, filepath.Join(e.outside, "x"), srv.URL, PushOpts{})
	wantCode(t, err, termproto.FileErrNotAllowed)
	_, err = e.s.PushURL(context.Background(), e.roots, filepath.Join(e.root, "..", "outside", "x"), srv.URL, PushOpts{})
	wantCode(t, err, termproto.FileErrNotAllowed)
	_, err = e.s.PushURL(context.Background(), e.roots, "relative/x", srv.URL, PushOpts{})
	wantCode(t, err, termproto.FileErrBadPath)
	if n := names(t, e.outside); len(n) != 0 {
		t.Fatalf("允许目录之外不应出现文件: %v", n)
	}
}

func TestPushURLNeverWritesThroughSymlinks(t *testing.T) {
	skipWindows(t, "创建符号链接需要特权")
	srv := serve(t, body("PWNED"))
	e := newPushEnv(t, allowLocal())
	victim := filepath.Join(e.outside, "victim.txt")
	if err := os.WriteFile(victim, []byte("original"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustSymlink(t, victim, filepath.Join(e.root, "filelink"))
	mustSymlink(t, e.outside, filepath.Join(e.root, "dirlink"))
	mustSymlink(t, "sub", filepath.Join(e.root, "inlink"))

	_, err := e.push("filelink", srv.URL, PushOpts{Overwrite: true})
	wantCode(t, err, termproto.FileErrIsSymlink)
	_, err = e.push("filelink", srv.URL, PushOpts{})
	wantCode(t, err, termproto.FileErrIsSymlink)
	_, err = e.push("dirlink/new.txt", srv.URL, PushOpts{})
	wantCode(t, err, termproto.FileErrNotAllowed)
	_, err = e.push("dirlink/victim.txt", srv.URL, PushOpts{Overwrite: true})
	wantCode(t, err, termproto.FileErrNotAllowed)
	if b, _ := os.ReadFile(victim); string(b) != "original" {
		t.Fatalf("目录外的文件被改动了: %q", b)
	}
	if n := names(t, e.outside); len(n) != 1 {
		t.Fatalf("目录外出现了多余的文件: %v", n)
	}
	// 经过指向允许目录内部的相对链接写入是合法的，文件落在链接指向的目录里
	_, err = e.push("inlink/ok.txt", srv.URL, PushOpts{})
	wantOK(t, err)
	if b, _ := os.ReadFile(filepath.Join(e.root, "sub", "ok.txt")); string(b) != "PWNED" {
		t.Fatalf("内部链接写入失败: %q", b)
	}
}

func TestPushURLSizeLimits(t *testing.T) {
	big := strings.Repeat("x", 1000)
	declared := serve(t, body(big))
	e := newPushEnv(t, allowLocal())
	_, err := e.push("a.bin", declared.URL, PushOpts{Max: 999})
	wantCode(t, err, termproto.FileErrTooLarge)

	// 不声明长度（chunked）的响应：靠流式计数拦住
	stream := serve(t, func(w http.ResponseWriter, r *http.Request) {
		f := w.(http.Flusher)
		for i := 0; i < 50; i++ {
			_, _ = w.Write([]byte(strings.Repeat("y", 100)))
			f.Flush()
		}
	})
	_, err = e.push("b.bin", stream.URL, PushOpts{Max: 4000})
	wantCode(t, err, termproto.FileErrTooLarge)
	if n := names(t, e.root); len(n) != 1 || n[0] != "sub" {
		t.Fatalf("超限的下载不应留下文件: %v", n)
	}
	// 恰好等于上限可以通过
	res, err := e.push("c.bin", declared.URL, PushOpts{Max: 1000})
	wantOK(t, err)
	if res.Size != 1000 {
		t.Fatalf("size = %d", res.Size)
	}
}

func TestPushURLHashVerification(t *testing.T) {
	srv := serve(t, body("verify-me"))
	e := newPushEnv(t, allowLocal())
	_, err := e.push("a", srv.URL, PushOpts{SHA256: strings.Repeat("0", 64)})
	wantCode(t, err, termproto.FileErrHashMismatch)
	if n := names(t, e.root); len(n) != 1 || n[0] != "sub" {
		t.Fatalf("摘要不符不应留下文件或临时文件: %v", n)
	}
	_, err = e.push("a", srv.URL, PushOpts{SHA256: strings.ToUpper(sum("verify-me"))})
	wantOK(t, err)
}

func TestPushURLHTTPFailures(t *testing.T) {
	e := newPushEnv(t, allowLocal())
	notFound := serve(t, func(w http.ResponseWriter, r *http.Request) { http.NotFound(w, r) })
	_, err := e.push("a", notFound.URL, PushOpts{})
	wantCode(t, err, termproto.FileErrDownload)
	if !strings.Contains(err.Msg, "404") {
		t.Fatalf("应带上状态码: %s", err.Msg)
	}

	slow := serve(t, func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(3 * time.Second):
		}
	})
	_, err = e.push("b", slow.URL, PushOpts{Timeout: 200 * time.Millisecond})
	wantCode(t, err, termproto.FileErrTimeout)

	// 对方声明了长度却中途断开：不能当成功落盘
	cut := serve(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "1000")
		_, _ = w.Write([]byte("short"))
		if hj, ok := w.(http.Hijacker); ok {
			if c, _, herr := hj.Hijack(); herr == nil {
				_ = c.Close()
			}
		}
	})
	_, err = e.push("c", cut.URL, PushOpts{})
	wantCode(t, err, termproto.FileErrDownload)

	for _, n := range names(t, e.root) {
		if n != "sub" {
			t.Fatalf("失败的下载不应留下文件: %v", names(t, e.root))
		}
	}

	// 连接被拒绝：错误里不能带出地址（避免泄露内部网络信息）
	dead := httptest.NewServer(http.NotFoundHandler())
	deadURL, deadAddr := dead.URL, dead.Listener.Addr().String()
	dead.Close()
	_, err = e.push("d", deadURL, PushOpts{})
	wantCode(t, err, termproto.FileErrDownload)
	if strings.Contains(err.Msg, "127.0.0.1") || strings.Contains(err.Msg, deadAddr) {
		t.Fatalf("错误信息泄露了地址: %s", err.Msg)
	}
}

func TestPushURLRejectsBadAndPrivateURLs(t *testing.T) {
	lookup := fakeLookup(map[string][]string{"localhost": {"127.0.0.1", "::1"}, "internal.example": {"10.1.2.3"}})
	e := newPushEnv(t, URLPolicy{Lookup: lookup.lookup, Dial: failDial(t)})
	for _, raw := range []string{
		"file:///etc/passwd", "ftp://example.com/x", "gopher://example.com/", "javascript:alert(1)", "data:text/plain,hi",
		"http://user:pass@example.com/", "https://user@example.com/", "http:///nohost", "http://",
		"http://127.0.0.1/", "http://127.0.0.1:8080/x", "http://[::1]/", "http://[::ffff:7f00:1]/", "http://[::ffff:127.0.0.1]/",
		"http://169.254.169.254/latest/meta-data/", "http://10.0.0.1/", "http://192.168.1.1/", "http://172.16.0.1/",
		"http://0.0.0.0/", "http://100.64.0.1/", "http://[fe80::1]/", "http://[fd00::1]/", "http://224.0.0.1/",
		"http://localhost/", "http://localhost:3000/", "http://internal.example/",
		"http://example.com:0/", "http://example.com:70000/", "http://example.com:abc/", "not a url",
	} {
		_, err := e.push("out", raw, PushOpts{})
		if err == nil {
			t.Errorf("%q 应被拒绝", raw)
			continue
		}
		if err.Code != termproto.FileErrURLBlocked {
			t.Errorf("%q: 错误码 = %s，期望 url_blocked（%s）", raw, err.Code, err.Msg)
		}
	}
	if n := names(t, e.root); len(n) != 1 {
		t.Fatalf("被拒绝的网址不应产生文件: %v", n)
	}
}

// fakeLookup 是可计数的假解析器：未登记的主机名解析失败。
type fakeResolver struct {
	mu    sync.Mutex
	table map[string][]string
	calls map[string]int
}

func fakeLookup(table map[string][]string) *fakeResolver {
	return &fakeResolver{table: table, calls: map[string]int{}}
}

func (f *fakeResolver) lookup(ctx context.Context, host string) ([]netip.Addr, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls[host]++
	ips, ok := f.table[host]
	if !ok {
		return nil, errors.New("no such host")
	}
	var out []netip.Addr
	for _, s := range ips {
		a, err := netip.ParseAddr(s)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, nil
}

func failDial(t *testing.T) func(ctx context.Context, network, addr string) (net.Conn, error) {
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		t.Errorf("不应拨号，但收到拨号请求: %s", addr)
		return nil, errors.New("unexpected dial")
	}
}

// dialRecorder 记录被要求拨号的地址，并把连接转到本地测试服务器。
type dialRecorder struct {
	mu     sync.Mutex
	addrs  []string
	target string
}

func (d *dialRecorder) dial(ctx context.Context, network, addr string) (net.Conn, error) {
	d.mu.Lock()
	d.addrs = append(d.addrs, addr)
	d.mu.Unlock()
	var nd net.Dialer
	return nd.DialContext(ctx, network, d.target)
}

func (d *dialRecorder) dialed() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]string(nil), d.addrs...)
}

func TestPushURLDialsValidatedAddressNotHostname(t *testing.T) {
	srv := serve(t, body("pinned"))
	res := fakeLookup(map[string][]string{
		"public.test": {"93.184.216.34"},
		"mixed.test":  {"93.184.216.34", "10.0.0.5"},
		"meta.test":   {"169.254.169.254"},
		"v6priv.test": {"fd00::1"},
		"mapped.test": {"::ffff:127.0.0.1"},
	})
	rec := &dialRecorder{target: srv.Listener.Addr().String()}
	e := newPushEnv(t, URLPolicy{Lookup: res.lookup, Dial: rec.dial})

	_, err := e.push("ok", "http://public.test/file", PushOpts{})
	wantOK(t, err)
	if got := rec.dialed(); len(got) != 1 || got[0] != "93.184.216.34:80" {
		t.Fatalf("应直接拨号到已校验的 IP，实际 %v", got)
	}
	if res.calls["public.test"] != 1 {
		t.Fatalf("同一主机名只应解析一次（防 DNS 重绑定），实际 %d 次", res.calls["public.test"])
	}
	before := len(rec.dialed())
	for _, host := range []string{"mixed.test", "meta.test", "v6priv.test", "mapped.test", "nxdomain.test"} {
		_, err := e.push("bad-"+host, "http://"+host+"/x", PushOpts{})
		wantCode(t, err, termproto.FileErrURLBlocked)
		if err.Msg != blockedMsg {
			t.Fatalf("%s: 「无法解析」与「非公网」必须是同一条消息，实际 %q", host, err.Msg)
		}
	}
	if after := len(rec.dialed()); after != before {
		t.Fatalf("被拒绝的主机不应产生任何拨号: %v", rec.dialed())
	}
	// 非默认端口与 https 默认端口体现在拨号地址里
	_, err = e.push("p", "http://public.test:8080/x", PushOpts{})
	wantOK(t, err)
	if got := rec.dialed(); got[len(got)-1] != "93.184.216.34:8080" {
		t.Fatalf("端口应保留: %v", got)
	}
}

func TestPushURLRedirectsAreRevalidated(t *testing.T) {
	srv := serve(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/ok":
			_, _ = w.Write([]byte("final"))
		case r.URL.Path == "/to-public":
			http.Redirect(w, r, "http://public2.test/ok", http.StatusFound)
		case r.URL.Path == "/to-inner":
			http.Redirect(w, r, "http://inner.test/secret", http.StatusFound)
		case r.URL.Path == "/to-literal":
			http.Redirect(w, r, "http://127.0.0.1:1/", http.StatusMovedPermanently)
		case r.URL.Path == "/to-meta":
			http.Redirect(w, r, "http://169.254.169.254/latest/meta-data/", http.StatusTemporaryRedirect)
		case r.URL.Path == "/to-file":
			http.Redirect(w, r, "file:///etc/passwd", http.StatusFound)
		case r.URL.Path == "/to-userinfo":
			http.Redirect(w, r, "http://u:p@public2.test/ok", http.StatusFound)
		case strings.HasPrefix(r.URL.Path, "/chain/"):
			var n int
			_, _ = fmt.Sscanf(r.URL.Path, "/chain/%d", &n)
			http.Redirect(w, r, fmt.Sprintf("/chain/%d", n+1), http.StatusFound)
		default:
			http.NotFound(w, r)
		}
	})
	res := fakeLookup(map[string][]string{
		"public.test":  {"93.184.216.34"},
		"public2.test": {"93.184.216.35"},
		"inner.test":   {"10.0.0.5"},
	})
	rec := &dialRecorder{target: srv.Listener.Addr().String()}
	e := newPushEnv(t, URLPolicy{Lookup: res.lookup, Dial: rec.dial})

	r, err := e.push("followed", "http://public.test/to-public", PushOpts{})
	wantOK(t, err)
	if r.Redirects != 1 || r.Size != 5 {
		t.Fatalf("result = %+v", r)
	}
	if got := rec.dialed(); len(got) != 2 || got[0] != "93.184.216.34:80" || got[1] != "93.184.216.35:80" {
		t.Fatalf("两跳都应拨号到各自已校验的 IP: %v", got)
	}
	for _, path := range []string{"/to-inner", "/to-literal", "/to-meta", "/to-file", "/to-userinfo"} {
		before := len(rec.dialed())
		_, err := e.push("x"+strings.ReplaceAll(path, "/", "_"), "http://public.test"+path, PushOpts{})
		wantCode(t, err, termproto.FileErrURLBlocked)
		for _, a := range rec.dialed()[before:] {
			if !strings.HasPrefix(a, "93.") {
				t.Fatalf("%s: 重定向后不应拨号到非公网地址，实际 %v", path, rec.dialed()[before:])
			}
		}
	}
	// 无限重定向有上限，且不被误认为 SSRF
	before := len(rec.dialed())
	_, err = e.push("chain", "http://public.test/chain/0", PushOpts{})
	wantCode(t, err, termproto.FileErrDownload)
	if used := len(rec.dialed()) - before; used > maxRedirects+1 {
		t.Fatalf("重定向次数超过上限: %d", used)
	}
	for _, n := range names(t, e.root) {
		if n != "sub" && n != "followed" {
			t.Fatalf("失败的下载不应留下文件: %v", names(t, e.root))
		}
	}
}

func TestPushURLIgnoresProxyEnvironment(t *testing.T) {
	srv := serve(t, body("direct"))
	t.Setenv("HTTP_PROXY", "http://127.0.0.1:9")
	t.Setenv("http_proxy", "http://127.0.0.1:9")
	t.Setenv("HTTPS_PROXY", "http://127.0.0.1:9")
	t.Setenv("NO_PROXY", "")
	res := fakeLookup(map[string][]string{"public.test": {"93.184.216.34"}})
	rec := &dialRecorder{target: srv.Listener.Addr().String()}
	e := newPushEnv(t, URLPolicy{Lookup: res.lookup, Dial: rec.dial})
	_, err := e.push("o", "http://public.test/x", PushOpts{})
	wantOK(t, err)
	if got := rec.dialed(); len(got) != 1 || got[0] != "93.184.216.34:80" {
		t.Fatalf("代理环境变量不应生效，实际拨号 %v", got)
	}
}

func TestControlHookAndDialContext(t *testing.T) {
	p := URLPolicy{}
	for addr, wantBlocked := range map[string]bool{
		"93.184.216.34:80": false, "[2606:4700:4700::1111]:443": false,
		"127.0.0.1:80": true, "[::1]:80": true, "10.0.0.1:22": true, "169.254.169.254:80": true,
		"example.com:80": true, "garbage": true, "[::ffff:127.0.0.1]:80": true,
	} {
		err := p.control("tcp", addr, nil)
		if (err != nil) != wantBlocked {
			t.Errorf("control(%s) = %v，期望 blocked=%v", addr, err, wantBlocked)
		}
	}
	if err := (URLPolicy{AllowPrivate: true}).control("tcp", "127.0.0.1:80", nil); err != nil {
		t.Errorf("AllowPrivate 应放行回环: %v", err)
	}
	// 即使绕过 checkURL 直接拨号，传输层也拒绝回环 / 私网
	for _, addr := range []string{"127.0.0.1:80", "[::1]:80", "10.0.0.1:80"} {
		if _, err := p.dialContext(context.Background(), "tcp", addr); !errors.Is(err, errBlocked) {
			t.Errorf("dialContext(%s) = %v，期望 errBlocked", addr, err)
		}
	}
}

func TestPushURLConcurrencyLimit(t *testing.T) {
	var mu sync.Mutex
	started := 0
	release := make(chan struct{})
	srv := serve(t, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		started++
		mu.Unlock()
		<-release
		_, _ = w.Write([]byte("x"))
	})
	e := newPushEnv(t, allowLocal())
	var wg sync.WaitGroup
	for i := 0; i < maxPushing; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := e.push(fmt.Sprintf("c%d", i), srv.URL, PushOpts{})
			if err != nil {
				t.Errorf("并发写入 %d 失败: %v", i, err)
			}
		}(i)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		mu.Lock()
		n := started
		mu.Unlock()
		if n >= maxPushing {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("并发请求没有按时到达服务器")
		}
		time.Sleep(10 * time.Millisecond)
	}
	_, err := e.push("over", srv.URL, PushOpts{})
	wantCode(t, err, termproto.FileErrBusy)
	close(release)
	wg.Wait()
	_, err = e.push("after", srv.URL, PushOpts{}) // 释放之后可以再写
	wantOK(t, err)
}
