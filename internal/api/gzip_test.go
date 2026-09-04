package api

import (
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestGzipMiddleware(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(GzipMiddleware())

	largeBody := strings.Repeat("RelayMesh payload content testing gzip compression ratio. ", 50)

	r.GET("/test/json", func(c *gin.Context) {
		c.String(http.StatusOK, largeBody)
	})

	r.GET("/api/v1/events", func(c *gin.Context) {
		c.String(http.StatusOK, "event: ping\ndata: {}\n\n")
	})

	// 1. 客户端携带 Accept-Encoding: gzip -> 应当返回 gzip 压缩
	req := httptest.NewRequest(http.MethodGet, "/test/json", nil)
	req.Header.Set("Accept-Encoding", "gzip, deflate")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d", w.Code)
	}
	if w.Header().Get("Content-Encoding") != "gzip" {
		t.Fatalf("expected Content-Encoding: gzip, got %q", w.Header().Get("Content-Encoding"))
	}

	// 验证解压后内容正确
	gzReader, err := gzip.NewReader(w.Body)
	if err != nil {
		t.Fatalf("failed to create gzip reader: %v", err)
	}
	defer gzReader.Close()
	decompressed, err := io.ReadAll(gzReader)
	if err != nil {
		t.Fatalf("failed to read decompressed body: %v", err)
	}
	if string(decompressed) != largeBody {
		t.Fatalf("decompressed content does not match original payload")
	}

	// 2. 客户端不携带 gzip -> 应当返回未压缩内容
	reqNoGzip := httptest.NewRequest(http.MethodGet, "/test/json", nil)
	wNoGzip := httptest.NewRecorder()
	r.ServeHTTP(wNoGzip, reqNoGzip)

	if wNoGzip.Header().Get("Content-Encoding") != "" {
		t.Fatalf("expected empty Content-Encoding, got %q", wNoGzip.Header().Get("Content-Encoding"))
	}
	if wNoGzip.Body.String() != largeBody {
		t.Fatalf("expected uncompressed raw body")
	}

	// 3. SSE 路由 -> 即使携带 Accept-Encoding: gzip 也严禁压缩
	reqSSE := httptest.NewRequest(http.MethodGet, "/api/v1/events", nil)
	reqSSE.Header.Set("Accept-Encoding", "gzip")
	wSSE := httptest.NewRecorder()
	r.ServeHTTP(wSSE, reqSSE)

	if wSSE.Header().Get("Content-Encoding") != "" {
		t.Fatalf("expected SSE endpoint to skip gzip compression, got %q", wSSE.Header().Get("Content-Encoding"))
	}
}
