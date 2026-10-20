package api

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/teacat99/RelayMesh/v2/internal/config"
	"github.com/teacat99/RelayMesh/v2/internal/store"
)

func TestResolveTrustedProxies(t *testing.T) {
	withDefaults := append(append([]string{}, defaultTrustedProxies...), "203.0.113.0/24")

	cases := []struct {
		name     string
		raw      string
		wantMode string
		wantList []string
		wantErr  bool
	}{
		{name: "未配置使用默认网段", raw: "", wantMode: trustedProxyModeDefault, wantList: defaultTrustedProxies},
		{name: "只有分隔符视为未配置", raw: "  , ; ", wantMode: trustedProxyModeDefault, wantList: defaultTrustedProxies},
		{name: "none 不信任任何代理", raw: "none", wantMode: trustedProxyModeNone, wantList: nil},
		{name: "none 不区分大小写与空白", raw: " NONE ", wantMode: trustedProxyModeNone, wantList: nil},
		{name: "星号恢复信任全部", raw: "*", wantMode: trustedProxyModeAll, wantList: []string{"0.0.0.0/0", "::/0"}},
		{name: "自定义 IP 与 CIDR", raw: "203.0.113.7, 198.51.100.0/24", wantMode: trustedProxyModeCustom, wantList: []string{"203.0.113.7", "198.51.100.0/24"}},
		{name: "CIDR 规范化为网络地址", raw: "10.1.2.3/8", wantMode: trustedProxyModeCustom, wantList: []string{"10.0.0.0/8"}},
		{name: "default 关键字展开并可追加", raw: "default,203.0.113.0/24", wantMode: trustedProxyModeCustom, wantList: withDefaults},
		{name: "重复条目去重", raw: "10.0.0.0/8,10.0.0.0/8", wantMode: trustedProxyModeCustom, wantList: []string{"10.0.0.0/8"}},
		{name: "IPv6", raw: "2001:db8::/32 ::1", wantMode: trustedProxyModeCustom, wantList: []string{"2001:db8::/32", "::1"}},
		{name: "无效 IP 报错", raw: "not-an-ip", wantErr: true},
		{name: "无效 CIDR 报错", raw: "10.0.0.0/99", wantErr: true},
		{name: "none 不能与其他条目混用", raw: "none,10.0.0.0/8", wantErr: true},
		{name: "星号不能与其他条目混用", raw: "*,10.0.0.0/8", wantErr: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, mode, err := ResolveTrustedProxies(tc.raw)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("期望报错，实际得到 mode=%q list=%v", mode, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("不应报错: %v", err)
			}
			if mode != tc.wantMode {
				t.Fatalf("mode = %q，期望 %q", mode, tc.wantMode)
			}
			if len(tc.wantList) == 0 {
				if len(got) != 0 {
					t.Fatalf("期望空列表，实际 %v", got)
				}
				return
			}
			if !reflect.DeepEqual(got, tc.wantList) {
				t.Fatalf("list = %v，期望 %v", got, tc.wantList)
			}
		})
	}
}

func TestResolveTrustedProxies_ReturnsCopyOfDefaults(t *testing.T) {
	first, _, _ := ResolveTrustedProxies("")
	first[0] = "0.0.0.0/0"
	second, _, _ := ResolveTrustedProxies("")
	if second[0] != defaultTrustedProxies[0] || defaultTrustedProxies[0] == "0.0.0.0/0" {
		t.Fatalf("修改返回值不应污染默认网段: %v", second)
	}
}

// clientIPFor 用给定的可信代理配置构造 Gin 引擎，模拟一次来自 remoteAddr、携带 headers 的请求，返回 c.ClientIP()。
func clientIPFor(t *testing.T, rawProxies, remoteAddr string, headers map[string]string) string {
	t.Helper()
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	ConfigureTrustedProxies(engine, rawProxies)
	engine.GET("/ip", func(c *gin.Context) { c.String(http.StatusOK, c.ClientIP()) })

	req := httptest.NewRequest(http.MethodGet, "/ip", nil)
	req.RemoteAddr = remoteAddr
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	engine.ServeHTTP(w, req)
	return w.Body.String()
}

func TestConfigureTrustedProxies_ClientIP(t *testing.T) {
	cases := []struct {
		name    string
		proxies string
		remote  string
		headers map[string]string
		want    string
	}{
		{
			name:    "默认：公网直连伪造 X-Forwarded-For 被忽略",
			remote:  "203.0.113.9:4000",
			headers: map[string]string{"X-Forwarded-For": "1.2.3.4"},
			want:    "203.0.113.9",
		},
		{
			name:    "默认：公网直连伪造 X-Real-IP 被忽略",
			remote:  "203.0.113.9:4000",
			headers: map[string]string{"X-Real-IP": "1.2.3.4"},
			want:    "203.0.113.9",
		},
		{
			name:    "默认：私网反代追加真实 IP，客户端伪造的最左项被忽略",
			remote:  "172.31.0.1:5555",
			headers: map[string]string{"X-Forwarded-For": "6.6.6.6, 8.8.8.8"},
			want:    "8.8.8.8",
		},
		{
			name:    "默认：多级内部反代自右向左跳过可信跳",
			remote:  "10.0.0.2:5555",
			headers: map[string]string{"X-Forwarded-For": "8.8.8.8, 10.0.0.9"},
			want:    "8.8.8.8",
		},
		{
			name:   "默认：回环对端且无转发头取对端地址",
			remote: "127.0.0.1:1",
			want:   "127.0.0.1",
		},
		{
			name:    "默认：IPv4 映射的 IPv6 私网对端同样可信",
			remote:  "[::ffff:172.31.0.1]:1234",
			headers: map[string]string{"X-Forwarded-For": "8.8.4.4"},
			want:    "8.8.4.4",
		},
		{
			name:    "默认：可信对端携带无法解析的转发头时回退为对端地址",
			remote:  "172.31.0.1:5555",
			headers: map[string]string{"X-Forwarded-For": "garbage"},
			want:    "172.31.0.1",
		},
		{
			name:    "none：即便私网对端也不采信转发头",
			proxies: "none",
			remote:  "172.31.0.1:5555",
			headers: map[string]string{"X-Forwarded-For": "8.8.8.8"},
			want:    "172.31.0.1",
		},
		{
			name:    "星号：恢复旧行为，公网客户端可伪造（最左项）",
			proxies: "*",
			remote:  "203.0.113.9:4000",
			headers: map[string]string{"X-Forwarded-For": "1.2.3.4, 5.6.7.8"},
			want:    "1.2.3.4",
		},
		{
			name:    "自定义：显式列出的 CDN 网段被采信",
			proxies: "203.0.113.0/24",
			remote:  "203.0.113.9:4000",
			headers: map[string]string{"X-Forwarded-For": "8.8.8.8"},
			want:    "8.8.8.8",
		},
		{
			name:    "自定义：显式列表替换默认网段，私网对端不再可信",
			proxies: "203.0.113.0/24",
			remote:  "172.31.0.1:5555",
			headers: map[string]string{"X-Forwarded-For": "8.8.8.8"},
			want:    "172.31.0.1",
		},
		{
			name:    "配置无效：回退默认而不是信任全部（私网仍可信）",
			proxies: "garbage",
			remote:  "172.31.0.1:5555",
			headers: map[string]string{"X-Forwarded-For": "8.8.8.8"},
			want:    "8.8.8.8",
		},
		{
			name:    "配置无效：回退默认而不是信任全部（公网伪造仍被忽略）",
			proxies: "garbage",
			remote:  "203.0.113.9:4000",
			headers: map[string]string{"X-Forwarded-For": "1.2.3.4"},
			want:    "203.0.113.9",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := clientIPFor(t, tc.proxies, tc.remote, tc.headers); got != tc.want {
				t.Fatalf("ClientIP() = %q，期望 %q", got, tc.want)
			}
		})
	}
}

// 回归测试：登录反爆破以 c.ClientIP() 为键。修复前 Gin 信任全部代理，攻击者每次更换 X-Forwarded-For
// 即可让每次失败都记在不同「IP」名下，从而永远不会被锁定。
func TestLoginLockout_NotBypassedByForgedForwardedFor(t *testing.T) {
	cfg := &config.Config{
		ProjectID:   "test-proj",
		WebUsername: "admin",
		WebPassword: "correct-password-123",
		JWTSecret:   "test-jwt-secret-0123456789abcdef0123456789abcdef",
	}
	st, err := store.New(":memory:")
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	srv := NewServer(cfg, st, nil)

	attempt := func(remoteAddr, forwardedFor string) int {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login",
			bytes.NewBufferString(`{"username":"admin","password":"wrong-password"}`))
		req.Header.Set("Content-Type", "application/json")
		req.RemoteAddr = remoteAddr
		if forwardedFor != "" {
			req.Header.Set("X-Forwarded-For", forwardedFor)
		}
		w := httptest.NewRecorder()
		srv.Engine().ServeHTTP(w, req)
		return w.Code
	}

	// 1. 公网直连客户端每次伪造不同的 X-Forwarded-For：仍按其真实地址累计，第 5 次失败触发锁定。
	for i := 1; i <= 4; i++ {
		if code := attempt("203.0.113.50:40000", fmt.Sprintf("198.18.0.%d", i)); code != http.StatusUnauthorized {
			t.Fatalf("直连第 %d 次失败应返回 401，实际 %d", i, code)
		}
	}
	if code := attempt("203.0.113.50:40000", "198.18.0.5"); code != http.StatusTooManyRequests {
		t.Fatalf("直连第 5 次失败应触发锁定 429，实际 %d（X-Forwarded-For 被采信，反爆破被绕过）", code)
	}
	if code := attempt("203.0.113.50:40000", "198.18.0.99"); code != http.StatusTooManyRequests {
		t.Fatalf("锁定期间更换伪造头仍应返回 429，实际 %d", code)
	}

	// 2. 私网反代（OpenResty 追加真实客户端 IP）：伪造的最左项被忽略，锁定按真实客户端计算，
	//    并且不会连坐同一反代后的其他真实客户端。
	for i := 1; i <= 4; i++ {
		if code := attempt("172.31.0.1:50000", fmt.Sprintf("198.18.1.%d, 198.51.100.20", i)); code != http.StatusUnauthorized {
			t.Fatalf("反代后第 %d 次失败应返回 401，实际 %d", i, code)
		}
	}
	if code := attempt("172.31.0.1:50000", "198.18.1.9, 198.51.100.20"); code != http.StatusTooManyRequests {
		t.Fatalf("反代后同一真实客户端第 5 次失败应锁定 429，实际 %d", code)
	}
	if code := attempt("172.31.0.1:50000", "198.51.100.21"); code != http.StatusUnauthorized {
		t.Fatalf("同一反代后的另一真实客户端不应被连坐锁定，实际 %d", code)
	}
}
