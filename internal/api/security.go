package api

import (
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"runtime/debug"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

// 安全响应头与访问日志脱敏（S0.2 / S0.4）。
//
// 背景：Web 控制台的 Markdown 内容来自 AI 客户端，属于不可信输入。前端已用 DOMPurify 做白名单净化，
// 这里再用浏览器侧的 CSP 兜底——即使净化被绕过，也不能执行内联脚本或连接到任意来源。

const (
	// CSPModeOff 关闭 CSP 头（仅用于排查兼容问题）；其余安全头仍然下发。
	CSPModeOff = "off"
	// CSPModeReport 只上报不拦截（Content-Security-Policy-Report-Only），用于灰度观察。
	CSPModeReport = "report"
	// CSPModeEnforce 强制执行（默认）。
	CSPModeEnforce = "enforce"
)

// ResolveCSPMode 规范化 RELAYMESH_CSP。空值与非法值都回落到 enforce，并返回提示文案（非法时）。
func ResolveCSPMode(raw string) (mode string, warning string) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "":
		return CSPModeEnforce, ""
	case CSPModeOff, "false", "0", "no", "disabled":
		return CSPModeOff, ""
	case CSPModeReport, "report-only":
		return CSPModeReport, ""
	case CSPModeEnforce, "on", "true", "1", "yes":
		return CSPModeEnforce, ""
	default:
		return CSPModeEnforce, fmt.Sprintf("RELAYMESH_CSP=%q 无法识别（可选 off / report / enforce），已按 enforce 处理", raw)
	}
}

// hostForCSPRe 限制可以写进 CSP 的 Host 字符集，杜绝通过伪造 Host 头向策略里注入指令。
var hostForCSPRe = regexp.MustCompile(`^[A-Za-z0-9.\-]+(:[0-9]{1,5})?$|^\[[0-9A-Fa-f:.]+\](:[0-9]{1,5})?$`)

// BuildCSP 生成内容安全策略。
//
//   - script-src 仅 'self'：前端构建产物没有内联脚本（首屏主题脚本已外置为 /theme-init.js）。
//   - style-src 保留 'unsafe-inline'：Vue / 组件库会在运行时写入 style，CSP 对样式注入的风险远低于脚本。
//   - 脚本、样式、字体等子资源只放行本站：页面不向任何第三方站点取资源（首屏不被外链阻塞，内网 / 离线可用，也不向第三方泄露访问者 IP）。
//     以前放行过 Google Fonts，实际上只有几处行内 code 用到其中一个字体；现在 Noto Sans Mono 的拉丁子集随应用自带（同源，font-src 'self' 已覆盖）。
//   - connect-src 除 'self' 外显式列出当前主机的 ws/wss：部分浏览器的 'self' 不覆盖 WebSocket（终端依赖它）。
//   - img-src 放开 http/https：Markdown 里允许引用外部图片；图片无法执行脚本。
func BuildCSP(host string) string {
	connect := []string{"'self'"}
	host = strings.TrimSpace(host)
	if host != "" && len(host) <= 255 && hostForCSPRe.MatchString(host) {
		connect = append(connect, "ws://"+host, "wss://"+host)
	}
	directives := []string{
		"default-src 'self'",
		"script-src 'self'",
		"style-src 'self' 'unsafe-inline'",
		"font-src 'self' data:",
		"img-src 'self' data: blob: http: https:",
		"media-src 'self' blob: data:",
		"connect-src " + strings.Join(connect, " "),
		"worker-src 'self' blob:",
		"object-src 'none'",
		"base-uri 'self'",
		"form-action 'self'",
		"frame-ancestors 'self'",
	}
	return strings.Join(directives, "; ")
}

// cspApplies 只对页面壳与静态资源下发 CSP；JSON API、MCP 与 SSE 不需要（也避免每个响应多出几百字节）。
func cspApplies(path string) bool {
	return !(strings.HasPrefix(path, "/api/") || strings.HasPrefix(path, "/mcp") || strings.HasPrefix(path, "/sse/"))
}

// SecurityHeadersMiddleware 下发基础安全响应头，并按模式附带 CSP。
func SecurityHeadersMiddleware(mode string) gin.HandlerFunc {
	return func(c *gin.Context) {
		h := c.Writer.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("X-Frame-Options", "SAMEORIGIN")
		h.Set("Permissions-Policy", "camera=(), geolocation=(), payment=(), usb=(), microphone=(self)")

		if mode != CSPModeOff && cspApplies(c.Request.URL.Path) {
			policy := BuildCSP(c.Request.Host)
			if mode == CSPModeReport {
				h.Set("Content-Security-Policy-Report-Only", policy)
			} else {
				h.Set("Content-Security-Policy", policy)
			}
		}
		c.Next()
	}
}

// ---- 访问日志脱敏 ----

// sensitiveQueryKeys 是会出现在 URL 里的凭据类查询参数。
var sensitiveQueryKeys = map[string]struct{}{
	"token":        {},
	"ticket":       {},
	"access_token": {},
	"key":          {},
	"api_key":      {},
	"password":     {},
}

// RedactRequestURI 把访问日志里的凭据脱敏：
//   - 查询参数 token / ticket / access_token / key / api_key / password 的值
//   - MCP 路径中内嵌的令牌：/mcp/token/<令牌>、/mcp/<令牌> 与能力 URL /mcp/c/<令牌>
//   - 文件拉取票据：/files/dl/<票据>
//   - Agent 安装源的窗口票据：/i/<票据>/<文件名>
func RedactRequestURI(uri string) string {
	path, rawQuery, hasQuery := strings.Cut(uri, "?")

	if strings.HasPrefix(path, "/mcp/") {
		rest := strings.Trim(strings.TrimPrefix(path, "/mcp/"), "/")
		switch {
		case strings.HasPrefix(rest, "token/"):
			path = "/mcp/token/***"
		case strings.HasPrefix(rest, "c/"):
			// 能力 URL：令牌就是网址的一部分，绝不能原样进入日志。
			path = "/mcp/c/***"
		case rest != "" && !strings.Contains(rest, "/"):
			path = "/mcp/***"
		}
	}
	if rest, ok := strings.CutPrefix(path, "/files/dl/"); ok && rest != "" {
		// 文件拉取票据：持有它就能下载一次文件，同样绝不能原样进入日志。
		path = "/files/dl/***"
	}
	if rest, ok := strings.CutPrefix(path, "/i/"); ok && rest != "" {
		// Agent 安装源的窗口票据：文件名保留（排查问题要用），票据脱敏。
		if _, name, hasName := strings.Cut(rest, "/"); hasName {
			path = "/i/***/" + name
		} else {
			path = "/i/***"
		}
	}
	if !hasQuery {
		return path
	}
	if rawQuery == "" {
		return path + "?"
	}

	parts := strings.Split(rawQuery, "&")
	for i, part := range parts {
		key, _, hasValue := strings.Cut(part, "=")
		if !hasValue {
			continue
		}
		decoded, err := url.QueryUnescape(key)
		if err != nil {
			decoded = key
		}
		if _, sensitive := sensitiveQueryKeys[strings.ToLower(decoded)]; sensitive {
			parts[i] = key + "=***"
		}
	}
	return path + "?" + strings.Join(parts, "&")
}

// RedactingLogFormatter 与 Gin 默认日志格式一致，只是对请求 URI 做脱敏。
func RedactingLogFormatter(param gin.LogFormatterParams) string {
	latency := param.Latency
	if latency > time.Minute {
		latency = latency.Truncate(time.Second)
	}
	return fmt.Sprintf("[GIN] %v | %3d | %13v | %15s | %-7s %#v\n%s",
		param.TimeStamp.Format("2006/01/02 - 15:04:05"),
		param.StatusCode,
		latency,
		param.ClientIP,
		param.Method,
		RedactRequestURI(param.Path),
		param.ErrorMessage,
	)
}

// RedactingRecovery 与 gin.Recovery 一样把 panic 转成 500，但日志里的请求行经过脱敏：
// gin 默认的恢复中间件会把完整的请求行（含 /mcp/c/<令牌> 这样的网址）连同请求头一起打进日志。
func RedactingRecovery() gin.HandlerFunc {
	return func(c *gin.Context) {
		defer func() {
			rec := recover()
			if rec == nil {
				return
			}
			if rec == http.ErrAbortHandler {
				panic(rec)
			}
			// 客户端已断开的写错误不是服务端缺陷，不打印栈。
			if err, ok := rec.(error); ok {
				var opErr *net.OpError
				if errors.As(err, &opErr) {
					var sysErr *os.SyscallError
					if errors.As(opErr.Err, &sysErr) {
						msg := strings.ToLower(sysErr.Error())
						if strings.Contains(msg, "broken pipe") || strings.Contains(msg, "connection reset by peer") {
							_ = c.Error(err)
							c.Abort()
							return
						}
					}
				}
			}
			log.Printf("[Recovery] panic recovered: %v | %s %s\n%s", rec, c.Request.Method, RedactRequestURI(c.Request.URL.RequestURI()), debug.Stack())
			c.AbortWithStatus(http.StatusInternalServerError)
		}()
		c.Next()
	}
}
