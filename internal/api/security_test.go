package api

import (
	"bytes"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

func TestResolveCSPMode(t *testing.T) {
	cases := []struct {
		in      string
		want    string
		warning bool
	}{
		{"", CSPModeEnforce, false},
		{"enforce", CSPModeEnforce, false},
		{" ENFORCE ", CSPModeEnforce, false},
		{"on", CSPModeEnforce, false},
		{"off", CSPModeOff, false},
		{"false", CSPModeOff, false},
		{"report", CSPModeReport, false},
		{"report-only", CSPModeReport, false},
		{"strict-ish", CSPModeEnforce, true},
	}
	for _, tc := range cases {
		got, warn := ResolveCSPMode(tc.in)
		if got != tc.want {
			t.Errorf("ResolveCSPMode(%q) = %q，期望 %q", tc.in, got, tc.want)
		}
		if (warn != "") != tc.warning {
			t.Errorf("ResolveCSPMode(%q) 警告=%q，期望有警告=%v", tc.in, warn, tc.warning)
		}
	}
}

func cspDirective(policy, name string) string {
	for _, part := range strings.Split(policy, ";") {
		part = strings.TrimSpace(part)
		if part == name || strings.HasPrefix(part, name+" ") {
			return part
		}
	}
	return ""
}

func TestBuildCSPIsStrictForScripts(t *testing.T) {
	policy := BuildCSP("relay.example.com:18776")

	script := cspDirective(policy, "script-src")
	if script != "script-src 'self'" {
		t.Fatalf("script-src 必须只有 'self'，实际为 %q", script)
	}
	if strings.Contains(script, "unsafe-inline") || strings.Contains(script, "unsafe-eval") {
		t.Fatalf("script-src 不得放开内联 / eval：%q", script)
	}
	for _, want := range []string{"object-src 'none'", "base-uri 'self'", "form-action 'self'", "frame-ancestors 'self'"} {
		if cspDirective(policy, strings.Fields(want)[0]) != want {
			t.Errorf("缺少指令 %q，策略=%s", want, policy)
		}
	}
	connect := cspDirective(policy, "connect-src")
	for _, want := range []string{"'self'", "ws://relay.example.com:18776", "wss://relay.example.com:18776"} {
		if !strings.Contains(connect, want) {
			t.Errorf("connect-src 应包含 %q，实际 %q", want, connect)
		}
	}
}

// 页面的脚本、样式、字体、媒体、Worker 都只来自本站：除 img-src（Markdown 允许引用外部图片，图片无法执行脚本）
// 与 connect-src（当前主机自己的 ws / wss）外，任何指令里都不能出现带主机名的来源。
// 以前 style-src / font-src 放行过 Google Fonts，而页面其实只有几处行内 code 用得上其中一个字体，后来改用系统字体并收紧了策略。
func TestBuildCSPNoThirdPartyOrigins(t *testing.T) {
	policy := BuildCSP("relay.example.com:18776")
	for _, name := range []string{"default-src", "script-src", "style-src", "font-src", "media-src", "worker-src", "object-src", "base-uri", "form-action", "frame-ancestors"} {
		directive := cspDirective(policy, name)
		if directive == "" {
			t.Errorf("缺少指令 %s，策略=%s", name, policy)
			continue
		}
		for _, src := range strings.Fields(directive)[1:] {
			if strings.Contains(src, "://") || strings.HasPrefix(src, "http:") || strings.HasPrefix(src, "https:") || strings.HasPrefix(src, "//") || strings.HasPrefix(src, "*") {
				t.Errorf("%s 不应放行第三方来源 %q（页面所有资源都由本服务提供）：%s", name, src, directive)
			}
		}
	}
	if strings.Contains(policy, "googleapis") || strings.Contains(policy, "gstatic") {
		t.Errorf("策略里不应再有 Google Fonts：%s", policy)
	}
}

func TestBuildCSPRejectsInjectedHost(t *testing.T) {
	hostile := []string{
		"evil.com; script-src *",
		"a.com\r\nX-Injected: 1",
		"a.com 'unsafe-inline'",
		"*.evil.com",
		"a.com:99999999",
		"",
	}
	for _, host := range hostile {
		policy := BuildCSP(host)
		if strings.Contains(policy, "evil") || strings.Contains(policy, "X-Injected") || strings.ContainsAny(policy, "\r\n") {
			t.Errorf("Host %q 被注入进了策略：%s", host, policy)
		}
		if cspDirective(policy, "script-src") != "script-src 'self'" {
			t.Errorf("Host %q 改变了 script-src：%s", host, policy)
		}
		if cspDirective(policy, "connect-src") != "connect-src 'self'" {
			t.Errorf("非法 Host %q 应只保留 'self'，实际 %q", host, cspDirective(policy, "connect-src"))
		}
	}
	if !strings.Contains(BuildCSP("[::1]:18775"), "ws://[::1]:18775") {
		t.Error("IPv6 字面量主机应被接受")
	}
}

func newSecurityTestEngine(mode string) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(SecurityHeadersMiddleware(mode))
	ok := func(c *gin.Context) { c.String(http.StatusOK, "ok") }
	r.GET("/", ok)
	r.GET("/assets/app.js", ok)
	r.GET("/api/v1/auth/status", ok)
	r.POST("/mcp", ok)
	r.GET("/sse/events", ok)
	return r
}

func TestSecurityHeadersMiddleware(t *testing.T) {
	do := func(mode, method, path string) http.Header {
		req := httptest.NewRequest(method, path, nil)
		req.Host = "relay.example.com"
		rec := httptest.NewRecorder()
		newSecurityTestEngine(mode).ServeHTTP(rec, req)
		return rec.Header()
	}

	for _, path := range []string{"/", "/assets/app.js"} {
		h := do(CSPModeEnforce, http.MethodGet, path)
		if h.Get("Content-Security-Policy") == "" {
			t.Errorf("%s 应下发 CSP", path)
		}
		if h.Get("Content-Security-Policy-Report-Only") != "" {
			t.Errorf("%s enforce 模式不应下发 Report-Only", path)
		}
	}
	for _, path := range []string{"/api/v1/auth/status", "/sse/events"} {
		h := do(CSPModeEnforce, http.MethodGet, path)
		if h.Get("Content-Security-Policy") != "" {
			t.Errorf("%s 不应下发 CSP", path)
		}
		if h.Get("X-Content-Type-Options") != "nosniff" {
			t.Errorf("%s 仍应下发基础安全头", path)
		}
	}
	if h := do(CSPModeEnforce, http.MethodPost, "/mcp"); h.Get("Content-Security-Policy") != "" {
		t.Error("MCP 不应下发 CSP")
	}

	h := do(CSPModeReport, http.MethodGet, "/")
	if h.Get("Content-Security-Policy-Report-Only") == "" || h.Get("Content-Security-Policy") != "" {
		t.Errorf("report 模式应只下发 Report-Only：%v", h)
	}
	h = do(CSPModeOff, http.MethodGet, "/")
	if h.Get("Content-Security-Policy") != "" || h.Get("Content-Security-Policy-Report-Only") != "" {
		t.Errorf("off 模式不应下发任何 CSP：%v", h)
	}
	if h.Get("X-Content-Type-Options") != "nosniff" || h.Get("X-Frame-Options") != "SAMEORIGIN" || h.Get("Referrer-Policy") != "no-referrer" {
		t.Errorf("off 模式仍应保留基础安全头：%v", h)
	}
	if !strings.Contains(h.Get("Permissions-Policy"), "microphone=(self)") {
		t.Errorf("语音输入依赖麦克风权限：%q", h.Get("Permissions-Policy"))
	}
}

func TestRedactRequestURI(t *testing.T) {
	cases := []struct{ in, want string }{
		{"/api/v1/sessions?limit=200&brief=true", "/api/v1/sessions?limit=200&brief=true"},
		{"/api/v1/events?token=eyJhbGciOi.secret.sig", "/api/v1/events?token=***"},
		{"/api/v1/events?a=1&token=abc&b=2", "/api/v1/events?a=1&token=***&b=2"},
		{"/api/v1/terminal/ws?ticket=abcdef", "/api/v1/terminal/ws?ticket=***"},
		{"/x?Token=abc", "/x?Token=***"},
		{"/x?to%6Ben=abc", "/x?to%6Ben=***"},
		{"/x?access_token=a&api_key=b&password=c&key=d", "/x?access_token=***&api_key=***&password=***&key=***"},
		{"/x?flag&token=abc", "/x?flag&token=***"},
		{"/mcp?token=abc123", "/mcp?token=***"},
		{"/mcp/token/abc123", "/mcp/token/***"},
		{"/mcp/abc123", "/mcp/***"},
		{"/mcp/c/rmc-abc123", "/mcp/c/***"},
		{"/mcp/c/rmc-abc123/", "/mcp/c/***"},
		{"/mcp/c/rmc-abc123?hostname=x&token=y", "/mcp/c/***?hostname=x&token=***"},
		{"/mcp", "/mcp"},
		{"/mcp/", "/mcp/"},
		{"/files/dl/AbCdEf0123456789-_AbCdEf0123456789-_AbCdEf0", "/files/dl/***"},
		{"/files/dl/AbCdEf0123456789-_AbCdEf0123456789-_AbCdEf0?x=1&token=y", "/files/dl/***?x=1&token=***"},
		{"/files/dl/", "/files/dl/"},
		{"/files/other", "/files/other"},
		{"/i/0123456789abcdef0123456789abcdef/install.sh", "/i/***/install.sh"},
		{"/i/0123456789abcdef0123456789abcdef/relaymesh-agent-linux-amd64?x=1&token=y", "/i/***/relaymesh-agent-linux-amd64?x=1&token=***"},
		{"/i/0123456789abcdef0123456789abcdef", "/i/***"},
		{"/i/", "/i/"},
		{"/index.html", "/index.html"},
		{"/icons/a.svg", "/icons/a.svg"},
		{"/", "/"},
		{"/x?", "/x?"},
	}
	for _, tc := range cases {
		if got := RedactRequestURI(tc.in); got != tc.want {
			t.Errorf("RedactRequestURI(%q) = %q，期望 %q", tc.in, got, tc.want)
		}
	}
}

func TestRedactingLogFormatterHidesCredentials(t *testing.T) {
	line := RedactingLogFormatter(gin.LogFormatterParams{
		TimeStamp:  time.Date(2026, 10, 1, 3, 4, 5, 0, time.UTC),
		StatusCode: 200,
		Latency:    1500 * time.Microsecond,
		ClientIP:   "203.0.113.9",
		Method:     "GET",
		Path:       "/api/v1/events?token=SUPERSECRETJWT",
	})
	if strings.Contains(line, "SUPERSECRETJWT") {
		t.Fatalf("访问日志泄露了令牌：%s", line)
	}
	for _, want := range []string{"[GIN]", "200", "203.0.113.9", "GET", "token=***"} {
		if !strings.Contains(line, want) {
			t.Errorf("日志缺少 %q：%s", want, line)
		}
	}
}

// 能力 URL 的令牌是网址的一部分：访问日志与 panic 恢复日志都不能原样记录它。
func TestCapabilityURLNeverAppearsInLogs(t *testing.T) {
	const secret = "rmc-0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

	line := RedactingLogFormatter(gin.LogFormatterParams{
		TimeStamp: time.Now(), StatusCode: 200, Latency: time.Millisecond, ClientIP: "203.0.113.9",
		Method: "POST", Path: "/mcp/c/" + secret,
	})
	if strings.Contains(line, secret) || !strings.Contains(line, "/mcp/c/***") {
		t.Fatalf("访问日志泄露了能力 URL：%s", line)
	}

	// panic 恢复日志：gin 默认的恢复中间件会把完整请求行打进日志，这里必须是脱敏的。
	var buf bytes.Buffer
	prev := log.Writer()
	log.SetOutput(&buf)
	defer log.SetOutput(prev)

	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(RedactingRecovery())
	r.POST("/mcp/c/:cap", func(c *gin.Context) { panic("boom") })
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/mcp/c/"+secret+"?token="+secret, nil)
	req.Header.Set("Authorization", "Bearer "+secret)
	r.ServeHTTP(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("panic 应转成 500: %d", w.Code)
	}
	out := buf.String()
	if strings.Contains(out, secret) {
		t.Fatalf("panic 恢复日志泄露了能力 URL：%s", out)
	}
	if !strings.Contains(out, "boom") || !strings.Contains(out, "/mcp/c/***") {
		t.Fatalf("panic 恢复日志应记录原因与脱敏后的请求行：%s", out)
	}
}
