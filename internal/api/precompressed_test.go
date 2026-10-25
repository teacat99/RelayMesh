package api

import (
	"bytes"
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/gin-gonic/gin"
	"github.com/teacat99/RelayMesh/v2/internal/config"
	"github.com/teacat99/RelayMesh/v2/internal/store"
)

func TestAcceptsBrotli(t *testing.T) {
	cases := []struct {
		name   string
		values []string
		want   bool
	}{
		{"Chrome 的写法", []string{"gzip, deflate, br, zstd"}, true},
		{"只有 br", []string{"br"}, true},
		{"大小写不敏感", []string{"GZIP, BR"}, true},
		{"q 值为正", []string{"gzip, br;q=0.5"}, true},
		{"q=1 带空格", []string{"gzip, br; q=1.0"}, true},
		{"多个头值", []string{"gzip", "br"}, true},
		{"没有 br", []string{"gzip, deflate"}, false},
		{"空", nil, false},
		{"空串", []string{""}, false},
		{"q=0 明确拒绝", []string{"gzip, br;q=0"}, false},
		{"q=0.000 明确拒绝", []string{"br;q=0.000, gzip"}, false},
		{"q 值写坏了按不接受", []string{"br;q=abc"}, false},
		{"通配符不算", []string{"*"}, false},
		{"名字只是前缀不算", []string{"brotli, gzip"}, false},
		{"identity", []string{"identity"}, false},
	}
	for _, c := range cases {
		if got := acceptsBrotli(c.values); got != c.want {
			t.Errorf("%s: acceptsBrotli(%q) = %v，期望 %v", c.name, c.values, got, c.want)
		}
	}
}

// 经完整引擎（含 gzip 中间件与 SPA 兜底）：命中预压缩时发 .br；其余情况与以前完全一致。
func TestPrecompressedAssets(t *testing.T) {
	gin.SetMode(gin.TestMode)
	st, err := store.New(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	// .br 的内容在这里只是占位字节（服务端不解码它，原样发出去）；浏览器端的解码正确性由前端构建的自检与 web/dist_test.go 保证。
	jsRaw := strings.Repeat("console.log('relaymesh');\n", 200)
	brJS := []byte("BR-JS-PAYLOAD\x00\x01\x02")
	brCSS := []byte("BR-CSS-PAYLOAD")
	fsys := fstest.MapFS{
		"index.html":                   {Data: []byte("<!doctype html><title>RelayMesh</title>")},
		"assets/index-Cqxweu7V.js":     {Data: []byte(jsRaw)},
		"assets/index-Cqxweu7V.js.br":  {Data: brJS},
		"assets/index-Cqxweu7V.css":    {Data: []byte("body{margin:0}")},
		"assets/index-Cqxweu7V.css.br": {Data: brCSS},
		// 有哈希但没有 .br（例如小于下限没压）
		"assets/small-Abcdef12.js": {Data: []byte(jsRaw)},
		// 没有哈希的文件即使旁边有 .br 也不能走预压缩（不是 immutable，内容会变）
		"assets/globals.css":    {Data: []byte("body{margin:0}")},
		"assets/globals.css.br": {Data: []byte("STALE")},
		// 只剩孤儿 .br：原文件不存在
		"assets/orphan-Zyxwvu98.js.br": {Data: []byte("ORPHAN")},
	}
	srv := NewServer(config.LoadWithMode("stdio"), st, fsys)
	do := func(method, path string, hdr ...string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, path, nil)
		for i := 0; i+1 < len(hdr); i += 2 {
			req.Header.Set(hdr[i], hdr[i+1])
		}
		w := httptest.NewRecorder()
		srv.Engine().ServeHTTP(w, req)
		return w
	}
	const immutable = "public, max-age=31536000, immutable"
	hasVary := func(w *httptest.ResponseRecorder) bool {
		for _, v := range w.Header().Values("Vary") {
			if strings.Contains(strings.ToLower(v), "accept-encoding") {
				return true
			}
		}
		return false
	}

	t.Run("接受 br：发 .br，类型取原扩展名，缓存头与 Vary 齐全", func(t *testing.T) {
		for _, tc := range []struct {
			path, ctype string
			body        []byte
		}{
			{"/assets/index-Cqxweu7V.js", "javascript", brJS},
			{"/assets/index-Cqxweu7V.css", "text/css", brCSS},
		} {
			w := do(http.MethodGet, tc.path, "Accept-Encoding", "gzip, deflate, br, zstd")
			if w.Code != http.StatusOK {
				t.Fatalf("%s: %d", tc.path, w.Code)
			}
			if got := w.Header().Get("Content-Encoding"); got != "br" {
				t.Fatalf("%s: Content-Encoding = %q，期望 br", tc.path, got)
			}
			if !strings.Contains(w.Header().Get("Content-Type"), tc.ctype) {
				t.Fatalf("%s: Content-Type = %q，应取原文件的类型（含 %s）", tc.path, w.Header().Get("Content-Type"), tc.ctype)
			}
			if cc := w.Header().Get("Cache-Control"); cc != immutable {
				t.Fatalf("%s: Cache-Control = %q", tc.path, cc)
			}
			if !hasVary(w) {
				t.Fatalf("%s: 缺 Vary: Accept-Encoding: %v", tc.path, w.Header().Values("Vary"))
			}
			if !bytes.Equal(w.Body.Bytes(), tc.body) {
				t.Fatalf("%s: 正文应是 .br 文件原样: %q", tc.path, w.Body.Bytes())
			}
			if w.Header().Get("Content-Length") != strconv.Itoa(len(tc.body)) {
				t.Fatalf("%s: Content-Length = %q，期望 %d", tc.path, w.Header().Get("Content-Length"), len(tc.body))
			}
		}
	})

	t.Run("HEAD：头一样、没有正文", func(t *testing.T) {
		w := do(http.MethodHead, "/assets/index-Cqxweu7V.js", "Accept-Encoding", "br")
		if w.Code != http.StatusOK || w.Header().Get("Content-Encoding") != "br" || w.Body.Len() != 0 {
			t.Fatalf("HEAD: code=%d enc=%q body=%d", w.Code, w.Header().Get("Content-Encoding"), w.Body.Len())
		}
		if w.Header().Get("Content-Length") != strconv.Itoa(len(brJS)) {
			t.Fatalf("HEAD Content-Length = %q", w.Header().Get("Content-Length"))
		}
	})

	t.Run("Range：按已编码的字节取范围，不能带整体长度", func(t *testing.T) {
		w := do(http.MethodGet, "/assets/index-Cqxweu7V.js", "Accept-Encoding", "br", "Range", "bytes=0-3")
		if w.Code != http.StatusPartialContent || w.Header().Get("Content-Encoding") != "br" {
			t.Fatalf("Range: code=%d enc=%q", w.Code, w.Header().Get("Content-Encoding"))
		}
		if got := w.Header().Get("Content-Range"); got != "bytes 0-3/"+strconv.Itoa(len(brJS)) {
			t.Fatalf("Content-Range = %q", got)
		}
		if !bytes.Equal(w.Body.Bytes(), brJS[:4]) {
			t.Fatalf("Range 正文 = %q，期望 %q", w.Body.Bytes(), brJS[:4])
		}
		if cl := w.Header().Get("Content-Length"); cl != "" && cl != "4" {
			t.Fatalf("Range 响应的 Content-Length 不能是整体长度: %q", cl)
		}
	})

	t.Run("只接受 gzip：走原来的动态 gzip，解开是原文件", func(t *testing.T) {
		w := do(http.MethodGet, "/assets/index-Cqxweu7V.js", "Accept-Encoding", "gzip, deflate")
		if w.Header().Get("Content-Encoding") != "gzip" {
			t.Fatalf("Content-Encoding = %q，期望 gzip", w.Header().Get("Content-Encoding"))
		}
		zr, err := gzip.NewReader(w.Body)
		if err != nil {
			t.Fatal(err)
		}
		got, _ := io.ReadAll(zr)
		if string(got) != jsRaw {
			t.Fatal("gzip 解开后应是原文件")
		}
		if cc := w.Header().Get("Cache-Control"); cc != immutable {
			t.Fatalf("Cache-Control = %q", cc)
		}
	})

	t.Run("什么编码都不接受：原文件", func(t *testing.T) {
		w := do(http.MethodGet, "/assets/index-Cqxweu7V.js")
		if w.Header().Get("Content-Encoding") != "" || w.Body.String() != jsRaw {
			t.Fatalf("应返回原文件: enc=%q len=%d", w.Header().Get("Content-Encoding"), w.Body.Len())
		}
	})

	t.Run("br;q=0 明确拒绝：不发 br", func(t *testing.T) {
		w := do(http.MethodGet, "/assets/index-Cqxweu7V.js", "Accept-Encoding", "gzip, br;q=0")
		if w.Header().Get("Content-Encoding") != "gzip" {
			t.Fatalf("Content-Encoding = %q，期望 gzip", w.Header().Get("Content-Encoding"))
		}
	})

	t.Run("没有 .br 的带哈希产物：照旧（br 客户端拿到的不是 br）", func(t *testing.T) {
		w := do(http.MethodGet, "/assets/small-Abcdef12.js", "Accept-Encoding", "br")
		if w.Code != http.StatusOK || w.Header().Get("Content-Encoding") == "br" {
			t.Fatalf("没有 .br 不能声称 br: code=%d enc=%q", w.Code, w.Header().Get("Content-Encoding"))
		}
		if w.Header().Get("Content-Encoding") == "" && w.Body.String() != jsRaw {
			t.Fatal("没压缩时应是原文件")
		}
	})

	t.Run("没有哈希的文件：旁边有 .br 也不用", func(t *testing.T) {
		w := do(http.MethodGet, "/assets/globals.css", "Accept-Encoding", "br")
		if w.Header().Get("Content-Encoding") == "br" || strings.Contains(w.Body.String(), "STALE") {
			t.Fatalf("无哈希文件不能走预压缩: enc=%q", w.Header().Get("Content-Encoding"))
		}
		if cc := w.Header().Get("Cache-Control"); cc != "no-cache" {
			t.Fatalf("无哈希文件应 no-cache: %q", cc)
		}
	})

	t.Run("孤儿 .br（原文件不存在）：不当作内容发出去", func(t *testing.T) {
		w := do(http.MethodGet, "/assets/orphan-Zyxwvu98.js", "Accept-Encoding", "br")
		if w.Header().Get("Content-Encoding") == "br" || strings.Contains(w.Body.String(), "ORPHAN") {
			t.Fatalf("孤儿 .br 被当成内容发出: enc=%q body=%q", w.Header().Get("Content-Encoding"), w.Body.String())
		}
	})

	t.Run("POST 等其它方法不受影响", func(t *testing.T) {
		w := do(http.MethodPost, "/assets/index-Cqxweu7V.js", "Accept-Encoding", "br")
		if w.Header().Get("Content-Encoding") == "br" {
			t.Fatal("POST 不应走预压缩")
		}
	})

	t.Run("页面与接口不受影响", func(t *testing.T) {
		w := do(http.MethodGet, "/", "Accept-Encoding", "gzip, br")
		if w.Code != http.StatusOK || w.Header().Get("Content-Encoding") == "br" {
			t.Fatalf("index.html 不预压缩: code=%d enc=%q", w.Code, w.Header().Get("Content-Encoding"))
		}
		api := do(http.MethodGet, "/api/v1/auth/status", "Accept-Encoding", "gzip, br")
		if api.Header().Get("Content-Encoding") == "br" {
			t.Fatal("接口不应出现 br")
		}
	})
}
