package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/gin-gonic/gin"
	"github.com/teacat99/RelayMesh/v2/internal/config"
	"github.com/teacat99/RelayMesh/v2/internal/store"
)

func TestIsHashedAsset(t *testing.T) {
	cases := map[string]bool{
		"assets/index-Cqxweu7V.js":                                              true,
		"assets/index-BEn7UiWT.css":                                             true,
		"assets/vendor-misc-C3dvP_DW.js":                                        true, // 哈希里带下划线
		"assets/ExecLayout--kcfqpyX.js":                                         true, // 哈希以 "-" 开头
		"assets/TerminalKeybar.vue_vue_type_script_setup_true_lang-B37h2whd.js": true,
		"assets/globals.css":                                                    false, // 没有哈希
		"assets/some-fontname.woff2":                                            false, // 恰好以 8 个小写字母结尾：没有数字 / 大写，不能当成「永不变」
		"index.html":                                                            false,
		"theme-init.js":                                                         false,
		"favicon.svg":                                                           false,
		"licenses/foo-AbCd1234.txt":                                             false, // 不在 assets/ 下
		"assets/index-Cqxweu7V.js.map":                                          false, // 多段扩展名：保守处理
		"assets/short-Ab1.js":                                                   false, // 哈希段不够 8 位
		"assets":                                                                false,
		"":                                                                      false,
		"assets/dir-Cqxweu7V/../../etc/passwd":                                  false, // 路径里带 .. 的畸形写法不会被 http.FileServer 放行，这里只要不被当成哈希产物即可
	}
	for name, want := range cases {
		if got := isHashedAsset(name); got != want {
			t.Errorf("isHashedAsset(%q) = %v, want %v", name, got, want)
		}
	}
}

func TestStaticETag(t *testing.T) {
	a := fstest.MapFS{"index.html": {Data: []byte("<title>a</title>")}}
	b := fstest.MapFS{"index.html": {Data: []byte("<title>b</title>")}}
	ea, eb := staticETag(a, "index.html"), staticETag(b, "index.html")
	if !strings.HasPrefix(ea, `W/"`) || !strings.HasSuffix(ea, `"`) {
		t.Fatalf("应是弱 ETag: %q", ea)
	}
	if ea == eb {
		t.Fatalf("内容不同 ETag 必须不同: %q", ea)
	}
	if ea != staticETag(a, "index.html") {
		t.Fatalf("同样内容 ETag 必须稳定")
	}
	big := fstest.MapFS{"big.bin": {Data: make([]byte, staticETagMaxBytes+1)}, "d/x": {Data: []byte("x")}}
	if got := staticETag(big, "big.bin"); got != "" {
		t.Fatalf("超过上限不算 ETag: %q", got)
	}
	if got := staticETag(big, "d"); got != "" {
		t.Fatalf("目录不算 ETag: %q", got)
	}
	if got := staticETag(big, "missing"); got != "" {
		t.Fatalf("不存在的文件不算 ETag: %q", got)
	}
	// 超过上限：仍然是 no-cache（正确但不省流量），只是没有 ETag
	h := http.Header{}
	staticCacheHeaders(h, big, "big.bin")
	if h.Get("Cache-Control") != "no-cache" || h.Get("ETag") != "" {
		t.Fatalf("大文件应只有 no-cache: %v", h)
	}
}

// 内嵌前端经完整引擎（含 gzip 中间件与 SPA 兜底）返回的缓存头：
// 带哈希的产物缓存一年且 immutable；页面与无哈希文件 no-cache + 弱 ETag，If-None-Match 命中回 304（无正文、无 Content-Encoding）。
func TestEmbeddedFrontendCacheHeaders(t *testing.T) {
	gin.SetMode(gin.TestMode)
	st, err := store.New(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	fsys := fstest.MapFS{
		"index.html":                    {Data: []byte("<!doctype html><title>RelayMesh</title>")},
		"theme-init.js":                 {Data: []byte("document.documentElement.dataset.theme='dark'")},
		"assets/index-Cqxweu7V.js":      {Data: []byte("console.log('app')")},
		"assets/ExecLayout--kcfqpyX.js": {Data: []byte("console.log('exec')")},
		"assets/globals.css":            {Data: []byte("body{margin:0}")},
		"assets/some-fontname.woff2":    {Data: []byte("font")},
	}
	srv := NewServer(config.LoadWithMode("stdio"), st, fsys)
	get := func(path string, hdr ...string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, path, nil)
		for i := 0; i+1 < len(hdr); i += 2 {
			req.Header.Set(hdr[i], hdr[i+1])
		}
		w := httptest.NewRecorder()
		srv.Engine().ServeHTTP(w, req)
		return w
	}

	const immutable = "public, max-age=31536000, immutable"
	for _, p := range []string{"/assets/index-Cqxweu7V.js", "/assets/ExecLayout--kcfqpyX.js"} {
		for _, enc := range []string{"", "gzip"} {
			w := get(p, "Accept-Encoding", enc)
			if w.Code != http.StatusOK {
				t.Fatalf("%s enc=%q: %d", p, enc, w.Code)
			}
			if cc := w.Header().Get("Cache-Control"); cc != immutable {
				t.Fatalf("%s enc=%q: 带哈希的产物应缓存一年且 immutable: %q", p, enc, cc)
			}
		}
	}

	// 页面本身（根路径与任意 SPA 路由）：no-cache + 同一个 ETag；命中回 304
	root := get("/")
	if root.Code != http.StatusOK || !strings.Contains(root.Body.String(), "RelayMesh") {
		t.Fatalf("根路径应返回 index.html: %d %q", root.Code, root.Body.String())
	}
	etag := root.Header().Get("ETag")
	if !strings.HasPrefix(etag, `W/"`) || root.Header().Get("Cache-Control") != "no-cache" {
		t.Fatalf("index.html 应 no-cache + 弱 ETag: cc=%q etag=%q", root.Header().Get("Cache-Control"), etag)
	}
	spa := get("/dashboard/anything")
	if spa.Code != http.StatusOK || spa.Header().Get("ETag") != etag || spa.Header().Get("Cache-Control") != "no-cache" {
		t.Fatalf("SPA 兜底页应与 index.html 同缓存头: code=%d cc=%q etag=%q want %q", spa.Code, spa.Header().Get("Cache-Control"), spa.Header().Get("ETag"), etag)
	}
	for _, enc := range []string{"", "gzip"} {
		hit := get("/", "If-None-Match", etag, "Accept-Encoding", enc)
		if hit.Code != http.StatusNotModified || hit.Body.Len() != 0 {
			t.Fatalf("enc=%q: If-None-Match 命中应 304 且无正文: %d %q", enc, hit.Code, hit.Body.String())
		}
		if hit.Header().Get("Content-Encoding") != "" {
			t.Fatalf("enc=%q: 304 不应带 Content-Encoding: %q", enc, hit.Header().Get("Content-Encoding"))
		}
		if hit.Header().Get("ETag") != etag {
			t.Fatalf("enc=%q: 304 应带回 ETag: %q", enc, hit.Header().Get("ETag"))
		}
	}
	if miss := get("/", "If-None-Match", `W/"deadbeef"`); miss.Code != http.StatusOK || miss.Body.Len() == 0 {
		t.Fatalf("别的 ETag 不应命中: %d", miss.Code)
	}

	// 无哈希的文件：no-cache + ETag，不能被当成 immutable
	for _, p := range []string{"/theme-init.js", "/assets/globals.css", "/assets/some-fontname.woff2"} {
		w := get(p)
		if w.Code != http.StatusOK {
			t.Fatalf("%s: %d", p, w.Code)
		}
		if cc := w.Header().Get("Cache-Control"); cc != "no-cache" {
			t.Fatalf("%s 应 no-cache: %q", p, cc)
		}
		tag := w.Header().Get("ETag")
		if !strings.HasPrefix(tag, `W/"`) {
			t.Fatalf("%s 应带弱 ETag: %q", p, tag)
		}
		if hit := get(p, "If-None-Match", tag); hit.Code != http.StatusNotModified {
			t.Fatalf("%s 命中应 304: %d", p, hit.Code)
		}
	}

	// API 的 404 不受影响（不会被缓存头污染）
	api := get("/api/v1/definitely-missing")
	if api.Code != http.StatusNotFound || strings.Contains(api.Header().Get("Cache-Control"), "immutable") {
		t.Fatalf("API 404 不应带静态缓存头: %d %q", api.Code, api.Header().Get("Cache-Control"))
	}
}
