package api

import (
	"bytes"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

// 前端产物的 Brotli 预压缩（#186）。
//
// 前端构建（frontend/build.precompress.ts）在 /assets 下的每个 .js / .css 旁写一个同名加 .br 的文件（质量 11，只在构建时压一次，随 web/dist 一起提交并内嵌）。
// 浏览器的 Accept-Encoding 带 br 时，这里直接把 .br 当作响应正文发出去：比请求时的动态 gzip（gzip.go，级别 4）小约 19%，
// 而且不占 CPU。首屏 4 个脚本 + 1 个样式合计少下约 86 KB，生产带宽约 1～3 Mbps 时首访快约 0.2～0.7 秒。
//
// 只处理 GET / HEAD 请求的「带哈希的 /assets 产物」（isHashedAsset，缓存一年且 immutable）：
//   - 没有 .br、客户端不接受 br（Chrome 只在 HTTPS 下才声明 br）、不是带哈希的产物：原样放行，走后面的动态 gzip 与静态文件处理，行为与以前一致；
//   - 命中时响应带 Content-Encoding: br 与 Vary: Accept-Encoding（缓存按编码分开存），Content-Type 取原文件的扩展名（不是 .br）。
//
// 必须挂在 GzipMiddleware 之前：命中后直接结束请求，不会被二次压缩。
func PrecompressedAssets(fsys fs.FS) gin.HandlerFunc {
	return func(c *gin.Context) {
		if fsys == nil || (c.Request.Method != http.MethodGet && c.Request.Method != http.MethodHead) {
			c.Next()
			return
		}
		urlPath := c.Request.URL.Path
		if !strings.HasPrefix(urlPath, "/assets/") {
			c.Next()
			return
		}
		name := strings.TrimPrefix(urlPath, "/")
		if !isHashedAsset(name) || !acceptsBrotli(c.Request.Header.Values("Accept-Encoding")) {
			c.Next()
			return
		}
		// 原文件必须真的存在：只剩孤儿 .br 时不能把它当成这个地址的内容发出去
		if info, err := fs.Stat(fsys, name); err != nil || info.IsDir() {
			c.Next()
			return
		}
		br, err := fsys.Open(name + ".br")
		if err != nil {
			c.Next()
			return
		}
		defer br.Close()
		brInfo, err := br.Stat()
		if err != nil || brInfo.IsDir() {
			c.Next()
			return
		}
		body, ok := br.(io.ReadSeeker)
		if !ok {
			data, err := io.ReadAll(br)
			if err != nil {
				c.Next()
				return
			}
			body = bytes.NewReader(data)
		}

		h := c.Writer.Header()
		staticCacheHeaders(h, fsys, name) // 带哈希：缓存一年且 immutable
		if ct := mime.TypeByExtension(path.Ext(name)); ct != "" {
			h.Set("Content-Type", ct)
		}
		h.Set("Content-Encoding", "br")
		h.Add("Vary", "Accept-Encoding")
		// ServeContent 负责 HEAD 与 Range（对已编码的正文按字节范围，与 Content-Encoding 一致），但只要响应头里有 Content-Encoding，
		// 标准库就不再自动写 Content-Length（它无法确认外层是否还会压缩）。这里的正文就是成品，长度确定，自己写；
		// 带 Range 的请求由 ServeContent 按范围算，不能预设整体长度。
		if c.Request.Header.Get("Range") == "" {
			h.Set("Content-Length", strconv.FormatInt(brInfo.Size(), 10))
		}
		http.ServeContent(c.Writer, c.Request, name, time.Time{}, body)
		c.Abort()
	}
}

// acceptsBrotli 判断 Accept-Encoding 是否声明接受 br：逐个编码取出，名字（不分大小写）是 br 且 q 大于 0。
// 通配符 * 不算（浏览器都显式写出 br；通配符放行 br 容易在不认识它的中间层上出事）。
func acceptsBrotli(values []string) bool {
	for _, v := range values {
		for _, part := range strings.Split(v, ",") {
			coding, params, _ := strings.Cut(strings.TrimSpace(part), ";")
			if !strings.EqualFold(strings.TrimSpace(coding), "br") {
				continue
			}
			q := 1.0
			for _, p := range strings.Split(params, ";") {
				k, val, ok := strings.Cut(strings.TrimSpace(p), "=")
				if !ok || !strings.EqualFold(strings.TrimSpace(k), "q") {
					continue
				}
				f, err := strconv.ParseFloat(strings.TrimSpace(val), 64)
				if err != nil {
					f = 0 // 写坏了的 q 值按「不接受」处理
				}
				q = f
			}
			if q > 0 {
				return true
			}
		}
	}
	return false
}
