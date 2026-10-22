package mcp

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"
)

// 请求体有上限：超大的请求体被拒绝（413），而不是被整个读进内存；正常请求不受影响。
func TestMCPRequestBodyIsBounded(t *testing.T) {
	srv := setupTestMCPServer(t)

	huge := append([]byte(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"pad":"`), bytes.Repeat([]byte("a"), MaxRequestBytes)...)
	huge = append(huge, []byte(`"}}`)...)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewReader(huge)))
	if w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("超大请求体应返回 413，得到 %d", w.Code)
	}

	// 接近上限但没超过的请求（填充放在被忽略的字段里）仍被正常处理。
	pad := bytes.Repeat([]byte("a"), MaxRequestBytes-1024)
	ok := append([]byte(`{"jsonrpc":"2.0","id":2,"method":"initialize","params":{"pad":"`), pad...)
	ok = append(ok, []byte(`"}}`)...)
	w = httptest.NewRecorder()
	srv.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewReader(ok)))
	if w.Code != http.StatusOK {
		t.Fatalf("上限以内的请求应被正常处理，得到 %d", w.Code)
	}
}

func TestTokenEqual(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"abc", "abc", true}, {"abc", "abd", false}, {"abc", "abcd", false}, {"", "x", false}, {"x", "", false},
	}
	for _, c := range cases {
		if got := tokenEqual(c.a, c.b); got != c.want {
			t.Errorf("tokenEqual(%q,%q)=%v，期望 %v", c.a, c.b, got, c.want)
		}
	}
}
