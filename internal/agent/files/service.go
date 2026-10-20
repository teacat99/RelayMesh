// Package files 是 Agent 侧的文件能力：stat / hash / 分块读取（拉取）/ 经网址写入（push_url）。
//
// 这里是文件能力的安全边界，Hub 只做形状校验与转发，真正的路径授权发生在这里：
//   - 路径必须落在请求携带的允许目录之内，并且用 os.Root 打开，符号链接 / .. 无法逃出允许目录；
//   - 只处理普通文件（FIFO / 设备 / 目录一律拒绝），且打开时不阻塞；
//   - 写入只发生在 push_url：先写同目录的临时文件，校验大小与摘要后再原子落位，不经符号链接写入；
//   - 网址访问受 SSRF 策略约束（见 ssrf.go）。
package files

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/teacat99/RelayMesh/v2/internal/termproto"
)

const (
	maxOpenHandles = 4
	maxHashing     = 2
	maxPushing     = 2
	handleIdle     = 30 * time.Second
	sweepEvery     = 10 * time.Second
	hashBufSize    = 1 << 20
	copyBufSize    = 64 << 10
	defaultPushTTL = 5 * time.Minute
)

// Config 是文件服务的配置。零值可用（家目录取当前账户，网址策略为严格默认）。
type Config struct {
	Home       string        // ~ 的展开目标；空则取 os.UserHomeDir
	URL        URLPolicy     // push_url 的网络策略
	Windows    *bool         // 测试覆盖路径语法；nil 按运行系统
	HandleIdle time.Duration // 句柄空闲多久后自动关闭；0 取默认
}

// Service 是 Agent 上的文件服务。并发安全。
type Service struct {
	rules pathRules
	url   URLPolicy
	idle  time.Duration

	mu      sync.Mutex
	handles map[string]*handle
	hashing int
	pushing int
	closed  bool
	stop    chan struct{}
}

type handle struct {
	f    *os.File
	size int64
	last time.Time
}

// New 创建文件服务并启动空闲句柄的回收。用完调用 Shutdown。
func New(cfg Config) *Service {
	home := cfg.Home
	if home == "" {
		home, _ = os.UserHomeDir()
	}
	win := runtime.GOOS == "windows"
	if cfg.Windows != nil {
		win = *cfg.Windows
	}
	idle := cfg.HandleIdle
	if idle <= 0 {
		idle = handleIdle
	}
	s := &Service{
		rules:   pathRules{windows: win, home: home},
		url:     cfg.URL,
		idle:    idle,
		handles: map[string]*handle{},
		stop:    make(chan struct{}),
	}
	go s.sweepLoop()
	return s
}

// Shutdown 关闭全部句柄并停止回收协程。
func (s *Service) Shutdown() {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	close(s.stop)
	hs := s.handles
	s.handles = map[string]*handle{}
	s.mu.Unlock()
	for _, h := range hs {
		_ = h.f.Close()
	}
}

func (s *Service) sweepLoop() {
	t := time.NewTicker(sweepEvery)
	defer t.Stop()
	for {
		select {
		case <-s.stop:
			return
		case now := <-t.C:
			s.sweep(now)
		}
	}
}

// sweep 关闭空闲过久的句柄（Hub 异常断开时不会发来 close）。
func (s *Service) sweep(now time.Time) {
	var dead []*os.File
	s.mu.Lock()
	for id, h := range s.handles {
		if now.Sub(h.last) > s.idle {
			dead = append(dead, h.f)
			delete(s.handles, id)
		}
	}
	s.mu.Unlock()
	for _, f := range dead {
		_ = f.Close()
	}
}

// dirRoot 是文件操作用到的那几个目录句柄方法。*os.Root 满足它——访问被限制在允许目录之内，符号链接带不出目录；
// 令牌被明确授予「整个文件系统」时用 wholeFSRoot：直接按操作系统路径访问，此时没有边界可言，符号链接照常解析
// （os.Root 会把绝对路径的符号链接一律当作逃逸，/etc/localtime 这类常见的链接就读不了）。
type dirRoot interface {
	Close() error
	Lstat(name string) (fs.FileInfo, error)
	Stat(name string) (fs.FileInfo, error)
	OpenFile(name string, flag int, perm fs.FileMode) (*os.File, error)
	Remove(name string) error
	Rename(oldname, newname string) error
	Link(oldname, newname string) error
}

var _ dirRoot = (*os.Root)(nil)

// wholeFSRoot 把相对路径接在文件系统根（或盘符根）后面直接访问。name 来自 pathRules.resolve，已是不含 `..` 的相对路径。
type wholeFSRoot struct{ base string }

var _ dirRoot = wholeFSRoot{}

func (w wholeFSRoot) path(name string) string { return filepath.Join(w.base, name) }

func (w wholeFSRoot) Close() error { return nil }

func (w wholeFSRoot) Lstat(name string) (fs.FileInfo, error) { return os.Lstat(w.path(name)) }

func (w wholeFSRoot) Stat(name string) (fs.FileInfo, error) { return os.Stat(w.path(name)) }

func (w wholeFSRoot) OpenFile(name string, flag int, perm fs.FileMode) (*os.File, error) {
	return os.OpenFile(w.path(name), flag, perm)
}

func (w wholeFSRoot) Remove(name string) error { return os.Remove(w.path(name)) }

func (w wholeFSRoot) Rename(oldname, newname string) error {
	return os.Rename(w.path(oldname), w.path(newname))
}

func (w wholeFSRoot) Link(oldname, newname string) error {
	return os.Link(w.path(oldname), w.path(newname))
}

// openRoot 按授权结果打开允许目录（「整个文件系统」时不受目录限制）。
func openRoot(rs resolved) (dirRoot, *Error) {
	if rs.Whole {
		return wholeFSRoot{base: rs.Root}, nil
	}
	root, err := os.OpenRoot(rs.Root)
	if err != nil {
		return nil, classify(err)
	}
	return root, nil
}

func (s *Service) nativeRel(rs resolved) string { return filepath.FromSlash(rs.Rel) }

// Stat 查询路径的类型 / 大小 / 权限位 / 修改时间。路径不存在不是错误，返回 Exists=false。
// 符号链接本身按链接报告（不跟随）。
func (s *Service) Stat(roots []string, p string) (termproto.FileStat, *Error) {
	rs, e := s.rules.resolve(roots, p)
	if e != nil {
		return termproto.FileStat{}, e
	}
	root, e := openRoot(rs)
	if e != nil {
		if e.Code == termproto.FileErrNotFound {
			return termproto.FileStat{Exists: false}, nil
		}
		return termproto.FileStat{}, e
	}
	defer root.Close()
	fi, err := root.Lstat(s.nativeRel(rs))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return termproto.FileStat{Exists: false}, nil
		}
		return termproto.FileStat{}, classify(err)
	}
	st := termproto.FileStat{Exists: true, MTime: fi.ModTime().Unix()}
	switch m := fi.Mode(); {
	case m&fs.ModeSymlink != 0:
		st.Type = termproto.FileTypeSymlink
	case m.IsDir():
		st.Type = termproto.FileTypeDir
	case m.IsRegular():
		st.Type, st.Size = termproto.FileTypeFile, fi.Size()
	default:
		st.Type = termproto.FileTypeOther
	}
	if !s.rules.windows {
		st.Mode = fmt.Sprintf("%04o", fi.Mode().Perm())
	}
	return st, nil
}

// openRegular 打开一个普通文件用于读取：非阻塞打开（FIFO 不会卡住），随后核对类型与大小上限。
func (s *Service) openRegular(roots []string, p string, max int64) (*os.File, int64, *Error) {
	rs, e := s.rules.resolve(roots, p)
	if e != nil {
		return nil, 0, e
	}
	if rs.Rel == "." {
		return nil, 0, fail(termproto.FileErrIsDir, "这是一个目录")
	}
	root, e := openRoot(rs)
	if e != nil {
		return nil, 0, e
	}
	defer root.Close() // 已打开的文件在 Root 关闭后仍然有效
	f, err := root.OpenFile(s.nativeRel(rs), os.O_RDONLY|openNoBlock, 0)
	if err != nil {
		return nil, 0, classify(err)
	}
	fi, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, 0, classify(err)
	}
	switch {
	case fi.IsDir():
		_ = f.Close()
		return nil, 0, fail(termproto.FileErrIsDir, "这是一个目录")
	case !fi.Mode().IsRegular():
		_ = f.Close()
		return nil, 0, fail(termproto.FileErrNotFile, "不是普通文件（管道 / 设备 / 套接字不支持）")
	case max > 0 && fi.Size() > max:
		_ = f.Close()
		return nil, 0, fail(termproto.FileErrTooLarge, fmt.Sprintf("文件大小 %d 字节，超过上限 %d 字节", fi.Size(), max))
	}
	return f, fi.Size(), nil
}

// Hash 计算普通文件的 SHA-256。耗时操作，受并发上限与 ctx 约束。
func (s *Service) Hash(ctx context.Context, roots []string, p string, max int64) (string, int64, *Error) {
	if max <= 0 || max > termproto.MaxFileHash {
		max = termproto.MaxFileHash
	}
	if !s.take(&s.hashing, maxHashing) {
		return "", 0, fail(termproto.FileErrBusy, "这台主机正在计算其它文件的摘要，请稍后重试")
	}
	defer s.release(&s.hashing)
	f, _, e := s.openRegular(roots, p, max)
	if e != nil {
		return "", 0, e
	}
	defer f.Close()
	h := sha256.New()
	buf := make([]byte, hashBufSize)
	var n int64
	for {
		if ctx.Err() != nil {
			return "", 0, fail(termproto.FileErrTimeout, "计算摘要超时")
		}
		m, err := f.Read(buf)
		if m > 0 {
			h.Write(buf[:m])
			if n += int64(m); n > max {
				return "", 0, fail(termproto.FileErrTooLarge, "文件在计算过程中变大，超过上限")
			}
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", 0, classify(err)
		}
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}

// Open 打开普通文件用于分块读取，返回句柄与文件大小。
func (s *Service) Open(roots []string, p string, max int64) (string, int64, *Error) {
	if max <= 0 || max > termproto.MaxFilePull {
		max = termproto.MaxFilePull
	}
	s.mu.Lock()
	if s.closed || len(s.handles) >= maxOpenHandles {
		s.mu.Unlock()
		return "", 0, fail(termproto.FileErrBusy, "这台主机上打开的文件太多，请稍后重试")
	}
	s.mu.Unlock()
	f, size, e := s.openRegular(roots, p, max)
	if e != nil {
		return "", 0, e
	}
	id := randHex(16)
	s.mu.Lock()
	if s.closed || len(s.handles) >= maxOpenHandles {
		s.mu.Unlock()
		_ = f.Close()
		return "", 0, fail(termproto.FileErrBusy, "这台主机上打开的文件太多，请稍后重试")
	}
	s.handles[id] = &handle{f: f, size: size, last: time.Now()}
	s.mu.Unlock()
	return id, size, nil
}

// Read 从句柄读取 [off, off+n)。返回的 eof 在读到打开时记录的文件大小时为真；
// 文件在传输中被截短时，返回的数据会少于期望，由调用方（Hub）按总大小核对并中止。
func (s *Service) Read(id string, off uint64, n int) (data []byte, eof bool, total uint64, e *Error) {
	if n <= 0 || n > termproto.MaxFileRead {
		n = termproto.MaxFileRead
	}
	s.mu.Lock()
	h := s.handles[id]
	if h != nil {
		h.last = time.Now()
	}
	s.mu.Unlock()
	if h == nil {
		return nil, false, 0, fail(termproto.FileErrBadHandle, "文件句柄不存在或已过期")
	}
	total = uint64(h.size)
	if off >= total {
		return nil, true, total, nil
	}
	if rest := total - off; uint64(n) > rest {
		n = int(rest)
	}
	buf := make([]byte, n)
	m, err := h.f.ReadAt(buf, int64(off))
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, false, total, classify(err)
	}
	buf = buf[:m]
	return buf, off+uint64(m) >= total || errors.Is(err, io.EOF), total, nil
}

// Close 关闭句柄。重复关闭与未知句柄无害。
func (s *Service) Close(id string) {
	s.mu.Lock()
	h := s.handles[id]
	delete(s.handles, id)
	s.mu.Unlock()
	if h != nil {
		_ = h.f.Close()
	}
}

// PushOpts 是 push_url 的参数。
type PushOpts struct {
	Overwrite bool          // 目标已存在时是否覆盖
	SHA256    string        // 期望摘要（可空）：不一致则丢弃下载内容
	Max       int64         // 最大下载字节数；0 取默认上限
	Timeout   time.Duration // 整体超时；0 取默认
}

// PushResult 是 push_url 的结果。
type PushResult struct {
	Size      int64
	SHA256    string
	Status    int
	Source    string // 协议://主机[:端口]，不含路径与查询串
	Redirects int
}

// PushURL 把网址的内容下载到允许目录内的目标文件：先写同目录的临时文件，校验后再落位。
func (s *Service) PushURL(ctx context.Context, roots []string, dest, rawURL string, o PushOpts) (PushResult, *Error) {
	u, perr := url.Parse(rawURL)
	if perr != nil {
		return PushResult{}, fail(termproto.FileErrURLBlocked, "网址无法解析")
	}
	if e := s.url.checkURL(u); e != nil {
		return PushResult{}, e
	}
	rs, e := s.rules.resolve(roots, dest)
	if e != nil {
		return PushResult{}, e
	}
	if rs.Rel == "." {
		return PushResult{}, fail(termproto.FileErrIsDir, "目标是允许目录本身")
	}
	max := o.Max
	if max <= 0 || max > termproto.MaxFilePush {
		max = termproto.MaxFilePush
	}
	timeout := o.Timeout
	if timeout <= 0 {
		timeout = defaultPushTTL
	}
	if !s.take(&s.pushing, maxPushing) {
		return PushResult{}, fail(termproto.FileErrBusy, "这台主机正在写入其它文件，请稍后重试")
	}
	defer s.release(&s.pushing)

	root, e := openRoot(rs)
	if e != nil {
		return PushResult{}, e
	}
	defer root.Close()
	rel := s.nativeRel(rs)

	perm := fs.FileMode(0o600) // 新文件默认仅属主可读写：下载来的内容可能带有敏感数据
	if fi, err := root.Lstat(rel); err == nil {
		switch m := fi.Mode(); {
		case fi.IsDir():
			return PushResult{}, fail(termproto.FileErrIsDir, "目标是一个目录")
		case m&fs.ModeSymlink != 0:
			return PushResult{}, fail(termproto.FileErrIsSymlink, "目标是符号链接，不经链接写入")
		case !m.IsRegular():
			return PushResult{}, fail(termproto.FileErrNotFile, "目标不是普通文件")
		case !o.Overwrite:
			return PushResult{}, fail(termproto.FileErrExists, "目标已存在（未允许覆盖）")
		default:
			perm = m.Perm()
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return PushResult{}, classify(err)
	}
	dir := filepath.Dir(rel)
	if fi, err := root.Stat(dir); err != nil {
		return PushResult{}, classify(err)
	} else if !fi.IsDir() {
		return PushResult{}, fail(termproto.FileErrNotFound, "目标的上级路径不是目录")
	}

	tmp := filepath.Join(dir, ".rmh-"+randHex(8)+".part")
	tf, err := root.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return PushResult{}, classify(err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tf.Close()
			_ = root.Remove(tmp)
		}
	}()

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	res, e := s.download(ctx, u, tf, max)
	if e != nil {
		return PushResult{}, e
	}
	if o.SHA256 != "" && !strings.EqualFold(o.SHA256, res.SHA256) {
		return PushResult{}, fail(termproto.FileErrHashMismatch, "下载内容的 SHA-256 与期望值不一致，已丢弃")
	}
	if err := tf.Chmod(perm); err != nil && !s.rules.windows {
		return PushResult{}, fail(termproto.FileErrIO, "写入失败：无法设置权限")
	}
	if err := tf.Sync(); err != nil {
		return PushResult{}, fail(termproto.FileErrIO, "写入失败：无法落盘")
	}
	if err := tf.Close(); err != nil {
		return PushResult{}, fail(termproto.FileErrIO, "写入失败：关闭文件出错")
	}
	if e := s.commit(root, tmp, rel, o.Overwrite); e != nil {
		return PushResult{}, e
	}
	committed = true
	return res, nil
}

// commit 把临时文件落到目标位置。覆盖用 rename（原子替换）；不覆盖用硬链接（目标已存在则失败，不会误覆盖），
// 文件系统不支持硬链接时退回「检查后 rename」。
func (s *Service) commit(root dirRoot, tmp, rel string, overwrite bool) *Error {
	if overwrite {
		if err := root.Rename(tmp, rel); err != nil {
			return classify(err)
		}
		return nil
	}
	err := root.Link(tmp, rel)
	switch {
	case err == nil:
		_ = root.Remove(tmp)
		return nil
	case errors.Is(err, fs.ErrExist):
		return fail(termproto.FileErrExists, "目标已存在（未允许覆盖）")
	case errors.Is(err, fs.ErrNotExist):
		return classify(err)
	}
	if _, lerr := root.Lstat(rel); lerr == nil {
		return fail(termproto.FileErrExists, "目标已存在（未允许覆盖）")
	}
	if rerr := root.Rename(tmp, rel); rerr != nil {
		return classify(rerr)
	}
	return nil
}

// download 把网址内容写入 w，返回大小 / 摘要 / 状态码 / 来源。
func (s *Service) download(ctx context.Context, u *url.URL, w io.Writer, max int64) (PushResult, *Error) {
	client := s.url.newClient(0) // 超时由 ctx 控制
	redirects := 0
	inner := client.CheckRedirect
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		redirects = len(via)
		return inner(req, via)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return PushResult{}, fail(termproto.FileErrURLBlocked, "网址无法解析")
	}
	req.Header.Set("User-Agent", "RelayMesh-Agent")
	req.Header.Set("Accept", "*/*")
	req.Header.Set("Accept-Encoding", "identity")
	resp, err := client.Do(req)
	if err != nil {
		return PushResult{}, downloadError(ctx, err)
	}
	defer resp.Body.Close()
	res := PushResult{Status: resp.StatusCode, Source: urlSource(u), Redirects: redirects}
	if resp.StatusCode != http.StatusOK {
		return res, fail(termproto.FileErrDownload, fmt.Sprintf("对方返回 HTTP %d", resp.StatusCode))
	}
	if resp.ContentLength > max {
		return res, fail(termproto.FileErrTooLarge, fmt.Sprintf("对方声明的大小 %d 字节，超过上限 %d 字节", resp.ContentLength, max))
	}
	h := sha256.New()
	buf := make([]byte, copyBufSize)
	var n int64
	for {
		m, rerr := resp.Body.Read(buf)
		if m > 0 {
			if n += int64(m); n > max {
				return res, fail(termproto.FileErrTooLarge, fmt.Sprintf("下载内容超过上限 %d 字节，已中止", max))
			}
			h.Write(buf[:m])
			if _, werr := w.Write(buf[:m]); werr != nil {
				return res, fail(termproto.FileErrIO, "写入失败："+cleanText(unwrapOS(werr), 100))
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			return res, downloadError(ctx, rerr)
		}
	}
	res.Size, res.SHA256 = n, hex.EncodeToString(h.Sum(nil))
	return res, nil
}

func unwrapOS(err error) string {
	var pe *fs.PathError
	if errors.As(err, &pe) {
		return pe.Err.Error()
	}
	return err.Error()
}

// downloadError 把网络层错误归入稳定错误码；不回传底层错误文本（可能含内部地址 / 主机名）。
func downloadError(ctx context.Context, err error) *Error {
	switch {
	case errors.Is(err, errBlocked):
		return fail(termproto.FileErrURLBlocked, blockedMsg)
	case errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded):
		return fail(termproto.FileErrTimeout, "下载超时")
	case errors.Is(err, context.Canceled):
		return fail(termproto.FileErrTimeout, "下载已取消")
	}
	return fail(termproto.FileErrDownload, "下载失败：无法连接、证书无效或连接中断")
}

func (s *Service) take(counter *int, limit int) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || *counter >= limit {
		return false
	}
	*counter++
	return true
}

func (s *Service) release(counter *int) {
	s.mu.Lock()
	*counter--
	s.mu.Unlock()
}

func randHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
