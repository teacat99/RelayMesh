package api

import (
	"compress/gzip"
	"io"
	"net/http"
	"strings"
	"sync"

	"github.com/gin-gonic/gin"
)

type gzipWriter struct {
	gin.ResponseWriter
	writer *gzip.Writer
}

func (g *gzipWriter) WriteString(s string) (int, error) {
	g.Header().Del("Content-Length")
	return g.writer.Write([]byte(s))
}

func (g *gzipWriter) Write(data []byte) (int, error) {
	g.Header().Del("Content-Length")
	return g.writer.Write(data)
}

func (g *gzipWriter) WriteHeader(code int) {
	g.Header().Del("Content-Length")
	g.ResponseWriter.WriteHeader(code)
}

var gzipPool = sync.Pool{
	New: func() any {
		w, _ := gzip.NewWriterLevel(io.Discard, gzip.BestSpeed)
		return w
	},
}

// GzipMiddleware 为 HTTP 响应提供动态 Gzip 压缩，显著缩减大 JSON 和富文本在小带宽服务器上的网络传输体积
func GzipMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		// 1. 客户端未请求 gzip 则直通
		if !strings.Contains(c.GetHeader("Accept-Encoding"), "gzip") {
			c.Next()
			return
		}

		// 2. 严禁对 SSE 实时流进行 gzip 缓冲，否则会导致实时事件延迟挂起
		path := c.Request.URL.Path
		if strings.HasSuffix(path, "/events") || strings.HasSuffix(path, "/sse") {
			c.Next()
			return
		}

		// 3. WebSocket 协议升级跳过
		if strings.EqualFold(c.GetHeader("Upgrade"), "websocket") {
			c.Next()
			return
		}

		gz := gzipPool.Get().(*gzip.Writer)
		defer gzipPool.Put(gz)

		gz.Reset(c.Writer)
		defer func() {
			_ = gz.Close()
		}()

		c.Header("Content-Encoding", "gzip")
		c.Header("Vary", "Accept-Encoding")

		c.Writer = &gzipWriter{ResponseWriter: c.Writer, writer: gz}
		c.Next()

		// 若响应没有正文内容（如 204 No Content 或 304 Not Modified），移除 Content-Encoding
		if c.Writer.Status() == http.StatusNoContent || c.Writer.Status() == http.StatusNotModified {
			c.Header("Content-Encoding", "")
		}
	}
}
