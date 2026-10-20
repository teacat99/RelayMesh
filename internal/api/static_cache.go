package api

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"io/fs"
	"net/http"
	"regexp"
	"strings"
)

// 内嵌前端资源的缓存头（#169）。
//
// embed.FS 里的文件没有修改时间，http.FileServer 因此既不发 Last-Modified 也不发 ETag，也没有 Cache-Control：
// 浏览器没有任何办法复用上一次下载的前端，每次打开页面都要把整套脚本与样式（压缩后约 620 KB）重新下一遍。
// 生产实测回访（"热"）与首访（"冷"）的首屏一样慢，1 Mbps 下光是下载脚本就要 5 秒。
//
//   - /assets/ 下文件名带内容哈希的产物（Vite 输出，形如 index-Cqxweu7V.js）：内容永远不会变，
//     缓存一年并标 immutable，回访时浏览器不再发任何请求；
//   - 其余（index.html、theme-init.js、favicon、没有哈希的样式等）：no-cache + 弱 ETag（内容哈希），
//     每次打开问一下、没变回 304（没有正文），发新版后立即生效，不会卡在旧页面上。
//
// 弱 ETag 与 gzip 中间件共存：压缩与否内容语义一致，If-None-Match 按弱比较命中即 304。

// hashedAssetName 匹配 Vite 的产物命名：assets/<名字>-<8 位 base64url 哈希>.<扩展名>。
// 哈希本身可能以 "-" 或 "_" 开头 / 含有这两个字符（如 ExecLayout--kcfqpyX.js），所以用「最后 8 个字符前面是 -」来切。
var hashedAssetName = regexp.MustCompile(`^assets/.+-([A-Za-z0-9_-]{8})\.[A-Za-z0-9]+$`)

// staticETagMaxBytes 是给无哈希文件算 ETag 时愿意读的上限：这类文件（index.html、theme-init.js、favicon 等）都很小，
// 超过上限就只发 no-cache（每次完整下载，正确但不省流量），避免每个请求都去哈希一个大文件。
const staticETagMaxBytes = 1 << 20

// isHashedAsset 判断 name（相对 fsys 根、不带前导斜杠）是不是文件名里带内容哈希的 /assets/ 产物。
// 哈希段必须含数字或大写字母：Vite 的哈希几乎一定满足，而 assets/some-fontname.woff2 这类恰好以 8 个小写字母结尾的
// 无哈希文件不能被当成「永不变」。
func isHashedAsset(name string) bool {
	m := hashedAssetName.FindStringSubmatch(name)
	if m == nil {
		return false
	}
	return strings.ContainsAny(m[1], "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZ")
}

// staticCacheHeaders 在交给 http.FileServer 之前调用，给即将返回的静态文件补缓存头。
// name 是 fsys 里的相对路径（SPA 兜底页传 "index.html"）。http.FileServer 看到响应头里已有 ETag 时会自己处理 If-None-Match。
func staticCacheHeaders(h http.Header, fsys fs.FS, name string) {
	if isHashedAsset(name) {
		h.Set("Cache-Control", "public, max-age=31536000, immutable")
		return
	}
	h.Set("Cache-Control", "no-cache")
	if etag := staticETag(fsys, name); etag != "" {
		h.Set("ETag", etag)
	}
}

// staticETag 返回文件内容的弱 ETag；读不到、是目录或超过上限时返回空串。
// 每次现算而不缓存：内嵌文件总共只有几 KB，哈希的开销可以忽略，而 os.DirFS（开发时直接读磁盘）下缓存会过期。
func staticETag(fsys fs.FS, name string) string {
	f, err := fsys.Open(name)
	if err != nil {
		return ""
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || info.IsDir() || info.Size() > staticETagMaxBytes {
		return ""
	}
	sum := sha256.New()
	if _, err := io.Copy(sum, io.LimitReader(f, staticETagMaxBytes+1)); err != nil {
		return ""
	}
	return `W/"` + hex.EncodeToString(sum.Sum(nil)[:16]) + `"`
}
