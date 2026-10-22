package ntfy

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode"
)

// Message 是一条要推送的内容。
type Message struct {
	Title    string
	Body     string
	Priority int
	Tags     []string
	Click    string
}

// SendError 描述一次推送为什么失败：Msg 面向用户且已抹掉凭据；Retryable 说明值不值得再试一次。
type SendError struct {
	// Status 是 HTTP 状态码；还没连上服务时为 0。
	Status     int
	Msg        string
	Retryable  bool
	RetryAfter time.Duration
}

func (e *SendError) Error() string { return e.Msg }

// Result 是一次（含重试的）推送的结果摘要。
type Result struct {
	Status   int
	Attempts int
	Took     time.Duration
}

// Sender 负责一次推送的 HTTP 细节。零值不可用，请用 NewSender。
type Sender struct {
	client *http.Client
	// Backoff 是相邻两次尝试之间的等待；长度决定最多重试几次（总尝试数 = len(Backoff)+1）。
	Backoff   []time.Duration
	userAgent string
}

// 单次请求的超时：连接 + 发送 + 收到响应头。ntfy 的发布接口应当在一两秒内返回。
const requestTimeout = 12 * time.Second

// 响应体只读这么多，足够解析出 ntfy 的错误说明，又防止对端拿一个超大响应拖垮内存。
const maxResponseBytes = 4 << 10

var errBlockedAddr = errors.New("目标地址被禁止")

// NewSender 创建默认的发送器：最多重试 2 次（1 秒、4 秒后），不跟随重定向，拒绝链路本地等危险地址。
func NewSender(userAgent string) *Sender {
	if userAgent == "" {
		userAgent = "RelayMesh"
	}
	dialer := &net.Dialer{Timeout: 8 * time.Second, KeepAlive: 30 * time.Second, Control: guardControl}
	tr := &http.Transport{
		// 尊重 HTTPS_PROXY 等环境变量：Hub 在公司网络里往往只能经代理出网。
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           dialer.DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          4,
		MaxIdleConnsPerHost:   2,
		IdleConnTimeout:       60 * time.Second,
		TLSHandshakeTimeout:   8 * time.Second,
		ResponseHeaderTimeout: 10 * time.Second,
	}
	return &Sender{
		client: &http.Client{
			Transport: tr,
			Timeout:   requestTimeout,
			// 不跟随重定向：POST 被 301/302 改成 GET 会悄悄丢掉消息，也避免把认证头带去别的地方。
			// 重定向按失败处理，并提示用户把服务地址改成最终地址。
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
		Backoff:   []time.Duration{1 * time.Second, 4 * time.Second},
		userAgent: userAgent,
	}
}

// guardControl 在建立连接的那一刻检查解析后的真实地址（因此域名重绑定也绕不过去）：
// 拒绝链路本地（含各家云厂商的元数据服务 169.254.169.254）、未指定地址与组播。
// 回环和内网地址放行——自建的 ntfy 通常就在内网或同一个 Docker 网络里。
func guardControl(_, address string, _ syscall.RawConn) error {
	ap, err := netip.ParseAddrPort(address)
	if err != nil || blockedAddr(ap.Addr()) {
		return errBlockedAddr
	}
	return nil
}

func blockedAddr(a netip.Addr) bool {
	a = a.Unmap()
	return a.IsLinkLocalUnicast() || a.IsLinkLocalMulticast() || a.IsMulticast() || a.IsUnspecified()
}

// publishBody 是 ntfy「JSON 发布」的请求体：主题放在 JSON 里、向服务根地址 POST。
// 用 JSON 而不是请求头发布有两个好处：中文标题不必做 RFC 2047 编码，主题也不会出现在 URL / 日志里。
type publishBody struct {
	Topic    string   `json:"topic"`
	Message  string   `json:"message"`
	Title    string   `json:"title,omitempty"`
	Priority int      `json:"priority,omitempty"`
	Tags     []string `json:"tags,omitempty"`
	Click    string   `json:"click,omitempty"`
}

// Send 推送一条消息；失败时返回 *SendError。可重试的失败（网络错误、5xx、429）按 Backoff 重试。
func (s *Sender) Send(ctx context.Context, cfg Config, msg Message) (Result, error) {
	start := time.Now()
	raw, err := json.Marshal(publishBody{
		Topic:    cfg.Topic,
		Message:  msg.Body,
		Title:    msg.Title,
		Priority: msg.Priority,
		Tags:     msg.Tags,
		Click:    msg.Click,
	})
	if err != nil {
		return Result{}, &SendError{Msg: "无法生成推送内容"}
	}

	var res Result
	for attempt := 0; ; attempt++ {
		res.Attempts = attempt + 1
		status, serr := s.post(ctx, cfg, raw)
		res.Status = status
		if serr == nil {
			res.Took = time.Since(start)
			return res, nil
		}
		if !serr.Retryable || attempt >= len(s.Backoff) || ctx.Err() != nil {
			res.Took = time.Since(start)
			return res, serr
		}
		wait := s.Backoff[attempt]
		// 对端说了多久之后再来（429 的 Retry-After），在合理范围内照做。
		if serr.RetryAfter > wait && serr.RetryAfter <= 15*time.Second {
			wait = serr.RetryAfter
		}
		select {
		case <-time.After(wait):
		case <-ctx.Done():
			res.Took = time.Since(start)
			return res, serr
		}
	}
}

// post 发一次请求，不重试。
func (s *Sender) post(ctx context.Context, cfg Config, body []byte) (int, *SendError) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, cfg.Server+"/", bytes.NewReader(body))
	if err != nil {
		return 0, &SendError{Msg: "服务地址无法用于发送请求"}
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", s.userAgent)
	if auth := cfg.authHeader(); auth != "" {
		req.Header.Set("Authorization", auth)
	}

	resp, err := s.client.Do(req)
	if err != nil {
		return 0, classifyNetworkError(err, cfg)
	}
	defer resp.Body.Close()
	snippet, _ := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	// 把连接读空，便于复用；超出上限的部分直接丢弃。
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return resp.StatusCode, nil
	}
	return resp.StatusCode, classifyStatus(resp, snippet, cfg)
}

func classifyStatus(resp *http.Response, snippet []byte, cfg Config) *SendError {
	code := resp.StatusCode
	detail := ntfyErrorText(snippet, cfg)
	withDetail := func(msg string) string {
		if detail == "" {
			return msg
		}
		return msg + "（服务端说明：" + detail + "）"
	}
	e := &SendError{Status: code}
	switch {
	case code >= 300 && code < 400:
		e.Msg = fmt.Sprintf("服务返回了重定向（HTTP %d）：请把服务地址改成最终地址，例如把 http:// 改成 https://", code)
	case code == http.StatusUnauthorized || code == http.StatusForbidden:
		e.Msg = withDetail(fmt.Sprintf("认证失败（HTTP %d）：请检查访问令牌或用户名密码，以及它们对这个主题是否有发布权限", code))
	case code == http.StatusNotFound:
		e.Msg = withDetail("找不到发布接口（HTTP 404）：请检查服务地址是否正确（路径前缀、是否真的指向 ntfy 服务）")
	case code == http.StatusMethodNotAllowed:
		e.Msg = withDetail("服务不接受发布请求（HTTP 405）：服务地址可能不是 ntfy，或 ntfy 版本太旧、不支持 JSON 发布")
	case code == http.StatusRequestEntityTooLarge:
		e.Msg = withDetail("消息太大，被服务拒绝（HTTP 413）")
	case code == http.StatusTooManyRequests:
		e.Msg = withDetail("推送太频繁，被 ntfy 服务限流（HTTP 429）")
		e.Retryable = true
		if secs, err := strconv.Atoi(strings.TrimSpace(resp.Header.Get("Retry-After"))); err == nil && secs > 0 {
			e.RetryAfter = time.Duration(secs) * time.Second
		}
	case code >= 500:
		e.Msg = withDetail(fmt.Sprintf("ntfy 服务暂时不可用（HTTP %d）", code))
		e.Retryable = true
	default:
		e.Msg = withDetail(fmt.Sprintf("ntfy 拒绝了这次推送（HTTP %d）", code))
	}
	return e
}

func classifyNetworkError(err error, cfg Config) *SendError {
	// 取出最里层的原因，避免把完整的请求 URL 之类带进给用户看的文字。
	cause := err
	var ue interface{ Unwrap() error }
	if errors.As(err, &ue) {
		if inner := ue.Unwrap(); inner != nil {
			cause = inner
		}
	}
	text := scrub(cause.Error(), cfg)

	var (
		unknownAuth x509.UnknownAuthorityError
		hostErr     x509.HostnameError
		certInvalid x509.CertificateInvalidError
		certVerify  *tls.CertificateVerificationError
		dnsErr      *net.DNSError
	)
	switch {
	case errors.Is(err, errBlockedAddr):
		return &SendError{Msg: "服务地址解析到了被禁止的地址（链路本地 / 云厂商元数据地址 / 组播），已拒绝发送"}
	case errors.As(err, &unknownAuth), errors.As(err, &hostErr), errors.As(err, &certInvalid), errors.As(err, &certVerify):
		return &SendError{Msg: "TLS 证书校验失败：服务使用了不受信任或与域名不符的证书。请换用受信任的证书，或在内网用 http:// 地址（" + text + "）"}
	case errors.Is(err, context.DeadlineExceeded), isTimeout(err):
		return &SendError{Msg: "连接 ntfy 服务超时：请检查服务地址与网络（Hub 所在机器要能访问它）", Retryable: true}
	case errors.Is(err, context.Canceled):
		return &SendError{Msg: "推送被取消（服务正在关闭）"}
	case errors.As(err, &dnsErr):
		return &SendError{Msg: "无法解析服务地址的主机名：" + text, Retryable: true}
	default:
		return &SendError{Msg: "连接 ntfy 服务失败：" + text, Retryable: true}
	}
}

func isTimeout(err error) bool {
	var ne net.Error
	return errors.As(err, &ne) && ne.Timeout()
}

// ntfyErrorText 取出 ntfy 错误响应（形如 {"code":40301,"http":403,"error":"forbidden"}）里的说明；
// 不是这种形状（例如反向代理的 HTML 错误页）就什么都不取，免得把乱七八糟的东西展示给用户。
func ntfyErrorText(snippet []byte, cfg Config) string {
	var parsed struct {
		Error string `json:"error"`
	}
	if json.Unmarshal(snippet, &parsed) != nil {
		return ""
	}
	return truncateRunes(scrub(strings.TrimSpace(parsed.Error), cfg), 160)
}

// scrub 把凭据从文字里抹掉，并去掉控制字符：错误文字要展示给用户、写进日志，任何情况下都不能带出令牌或密码。
func scrub(text string, cfg Config) string {
	for _, secret := range cfg.secrets() {
		text = strings.ReplaceAll(text, secret, "<已隐藏>")
	}
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, text)
}

func basicAuth(user, pass string) string {
	return base64.StdEncoding.EncodeToString([]byte(user + ":" + pass))
}

// truncateRunes 按字符（而不是字节）截断，保证不会把一个汉字劈成两半。
func truncateRunes(s string, max int) string {
	if max <= 0 {
		return ""
	}
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max]) + "…"
}
