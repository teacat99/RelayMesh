package terminal

import (
	"context"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
	"github.com/teacat99/RelayMesh/v2/internal/model"
	"github.com/teacat99/RelayMesh/v2/internal/termproto"
)

// GET /files/dl/:ticket —— 拉取票据的下载端点。
//
// 这是整个终端体系里唯一不要求登录的读取入口，所以它的全部安全性来自票据本身：
//   - 票据是 pull_prepare 签发的一次性凭据（32 字节随机数，120 秒有效），无效的一律是同一个 404；
//   - 票据只是「某人曾经被授权过」的证据，不是授权本身：下载时把所有闸门重新核对一遍
//     （开关、令牌、主机、暂停、本机一票否决、执行模式、令牌当前的访问范围），再向 Agent 重新打开文件，
//     并要求大小仍与签发时一致（文件被替换成别的东西就拒绝）；
//   - 文件内容只流经 Hub 的内存（≤1 MiB 的读取窗口），不落盘、不进数据库；票据与路径不进访问日志（见 RedactRequestURI）。

const (
	downloadWriteStall = 60 * time.Second // 客户端停滞多久就放弃这次下载
)

func (h *Hub) mountFiles(engine *gin.Engine) {
	engine.GET("/files/dl/:ticket", h.files.handleDownload)
}

// acquireDownload 占一个下载名额：全局与每台主机各有上限。
func (s *FileService) acquireDownload(machine string) (release func(), ok bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.downloads >= fileMaxDownloads || s.perHost[machine] >= fileMaxDownloadsPerHost {
		return nil, false
	}
	s.downloads++
	s.perHost[machine]++
	return func() {
		s.mu.Lock()
		s.downloads--
		if s.perHost[machine]--; s.perHost[machine] <= 0 {
			delete(s.perHost, machine)
		}
		s.mu.Unlock()
	}, true
}

// restore 把一张刚被取走、但还没有开始服务的票据放回去（只用于「被并发上限拒绝」的情形）。
func (t *fileTickets) restore(token string, tk *fileTicket) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.now().Before(tk.Expires) {
		t.m[ticketKey(token)] = tk
	}
}

// credentialStillAllowsFiles 判断令牌在当前状态下是否仍然允许对这台主机做文件传输。
func credentialStillAllowsFiles(c *model.MCPCredential, machine string, now time.Time) bool {
	switch {
	case c == nil, !c.IsActive:
		return false
	case c.ExpiresAt != nil && now.After(*c.ExpiresAt):
		return false
	case !c.Permissions.TerminalFiles:
		return false
	case !c.AllowsTerminalHost(machine):
		return false
	case !c.TerminalLimits.HasFileScope():
		return false
	}
	return true
}

func (s *FileService) handleDownload(c *gin.Context) {
	hdr := c.Writer.Header()
	hdr.Set("Cache-Control", "no-store")
	hdr.Set("Referrer-Policy", "no-referrer")
	hdr.Set("X-Content-Type-Options", "nosniff")
	notFound := func() { c.String(http.StatusNotFound, "not found") }

	if !s.enabled {
		notFound()
		return
	}
	token := c.Param("ticket")
	tk := s.tickets.take(token)
	if tk == nil {
		notFound()
		return
	}
	release, ok := s.acquireDownload(tk.Machine)
	if !ok {
		s.tickets.restore(token, tk)
		hdr.Set("Retry-After", "2")
		c.String(http.StatusServiceUnavailable, "busy")
		return
	}
	defer release()

	ctx := c.Request.Context()
	ip := c.ClientIP()
	size, written, outcome := s.serveDownload(ctx, c, tk)
	result := ResultOK
	if outcome != "" {
		result = ResultFail
	}
	detail := "path=" + clipPath(tk.Path, fileAuditPath) + " size=" + strconv.FormatInt(size, 10) + " bytes=" + strconv.FormatInt(written, 10)
	if outcome != "" {
		detail += " outcome=" + outcome
	}
	if slices.Contains(tk.Roots, termproto.FileRootAll) {
		detail += " " + auditScopeWholeFS // 票据是在「允许整个文件系统」下签发的
	}
	s.h.audit.record(s.h.ctx, auditEvent{
		Event: EvFileDownload, Result: result, Actor: tk.Credential, IP: ip,
		MachineCode: tk.Machine, ActorKind: ActorModel, Detail: detail,
	})
}

// serveDownload 重新授权并流式输出。返回签发时的大小、已写出的字节数与失败原因（空串表示成功完成）。
func (s *FileService) serveDownload(ctx context.Context, c *gin.Context, tk *fileTicket) (size, written int64, outcome string) {
	size = tk.Size
	refuse := func(status int, why string) (int64, int64, string) {
		c.String(status, "forbidden")
		return size, 0, why
	}

	if !s.enabled || s.h.exec.g1(ctx) != nil {
		return refuse(http.StatusForbidden, "gate_closed")
	}
	cred, err := s.h.store.GetCredential(ctx, tk.CredentialID)
	if err != nil || !credentialStillAllowsFiles(cred, tk.Machine, s.h.now()) {
		return refuse(http.StatusForbidden, "credential")
	}
	rec, err := s.h.store.GetTerminalHost(ctx, tk.Machine)
	if err != nil || rec == nil {
		return refuse(http.StatusForbidden, "host")
	}
	agent := s.h.onlineAgent(tk.Machine)
	if code := s.hostBlock(*rec, agent); code != "" {
		return refuse(http.StatusForbidden, code)
	}
	if code := s.scopeBlock(cred.TerminalLimits, agent); code != "" {
		return refuse(http.StatusForbidden, code)
	}
	if effectiveMode(rec.ExecMode, cred.TerminalModeCap) != model.TerminalModeAuto {
		return refuse(http.StatusForbidden, "mode")
	}

	// 用令牌「当前」的访问范围（允许目录或「整个文件系统」）而不是签发时的快照：期间被收窄的授权立即生效。
	res, err := agent.fileCall(ctx, termproto.FileReq{
		Op: termproto.FileOpOpen, Roots: fileRoots(cred.TerminalLimits), Path: tk.Path, Max: termproto.MaxFilePull,
	}, fileCallTimeout)
	if err != nil {
		c.String(http.StatusBadGateway, "host unavailable")
		return size, 0, "transport"
	}
	if !res.OK {
		status := http.StatusBadGateway
		switch res.Code {
		case termproto.FileErrNotAllowed, termproto.FileErrDenied, termproto.FileErrBadPath:
			status = http.StatusForbidden
		case termproto.FileErrNotFound, termproto.FileErrNotFile, termproto.FileErrIsDir:
			status = http.StatusNotFound
		}
		c.String(status, "unavailable")
		return size, 0, res.Code
	}
	handle := res.Handle
	defer agent.fileClose(handle)
	if res.Size != tk.Size {
		c.String(http.StatusConflict, "the file changed since it was prepared")
		return size, 0, "changed"
	}

	hdr := c.Writer.Header()
	hdr.Set("Content-Type", "application/octet-stream")
	hdr.Set("Content-Length", strconv.FormatInt(size, 10))
	hdr.Set("Content-Disposition", contentDisposition(tk.Path))
	c.Status(http.StatusOK)
	rc := http.NewResponseController(c.Writer)
	for written < size {
		want := size - written
		if want > termproto.MaxFileRead {
			want = termproto.MaxFileRead
		}
		sl, fail, err := agent.fileRead(ctx, handle, uint64(written), int(want))
		if err != nil || fail != nil || sl == nil || len(sl.Data) == 0 || int64(len(sl.Data)) > want {
			// 响应头已经发出，没法再换成错误页：中止连接，让客户端看到长度不符。
			return size, written, "read"
		}
		_ = rc.SetWriteDeadline(s.h.now().Add(downloadWriteStall))
		if _, err := c.Writer.Write(sl.Data); err != nil {
			return size, written, "client"
		}
		written += int64(len(sl.Data))
	}
	_ = rc.SetWriteDeadline(time.Time{})
	return size, written, ""
}

// contentDisposition 由文件名生成 attachment 头：ASCII 回退名里不含引号、反斜杠、分号与控制字符，
// 非 ASCII 文件名额外给出 RFC 5987 的 filename*。
func contentDisposition(path string) string {
	name := path
	if i := strings.LastIndexAny(name, `/\`); i >= 0 {
		name = name[i+1:]
	}
	if name == "" || !utf8.ValidString(name) {
		name = "download"
	}
	var b strings.Builder
	for _, r := range name {
		switch {
		case r < 0x20 || r == 0x7f, r == '"', r == '\\', r == ';', r == '/', r == '%':
			b.WriteByte('_')
		case r > 0x7e:
			b.WriteByte('_')
		default:
			b.WriteRune(r)
		}
	}
	fallback := b.String()
	if len(fallback) > 120 {
		fallback = fallback[:120]
	}
	out := `attachment; filename="` + fallback + `"`
	if fallback != name {
		utf := name
		if len(utf) > 240 {
			utf = utf[:240]
			for !utf8.ValidString(utf) {
				utf = utf[:len(utf)-1]
			}
		}
		out += "; filename*=UTF-8''" + rfc5987Escape(utf)
	}
	return out
}

// rfc5987Escape 按 RFC 5987 的 attr-char 做百分号编码（比 url.PathEscape 更严：只放行文档规定的字符）。
func rfc5987Escape(s string) string {
	const hexdigits = "0123456789ABCDEF"
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
			b.WriteByte(c)
		case strings.IndexByte("!#$&+-.^_`|~", c) >= 0:
			b.WriteByte(c)
		default:
			b.WriteByte('%')
			b.WriteByte(hexdigits[c>>4])
			b.WriteByte(hexdigits[c&15])
		}
	}
	return b.String()
}
