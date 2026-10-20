package sshexec

import (
	"context"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
)

// 交互式 shell 会话（人工 SSH 终端）。与 Exec 不同，这里申请伪终端并启动对方的登录 shell，
// 输入输出是原始字节流，窗口大小可随时调整，并周期性发送保活请求以发现静默断开的连接。

// 保活的默认参数：每 30 秒一次，连续 3 次没有应答就视为连接已断开。
const (
	DefaultKeepAliveEvery  = 30 * time.Second
	DefaultKeepAliveMisses = 3
	keepAliveReplyWait     = 15 * time.Second
	defaultTerm            = "xterm-256color"
)

// killGrace 是 Kill 之后等待远端自行结束的宽限期，超过就直接关闭连接（测试里会缩短它）。
var killGrace = 2 * time.Second

// ShellOptions 是 OpenShell 的参数。零值使用默认值。
type ShellOptions struct {
	Term string
	Rows uint16
	Cols uint16
	// KeepAliveEvery <= 0 使用默认值；KeepAliveMisses <= 0 使用默认值。
	KeepAliveEvery  time.Duration
	KeepAliveMisses int
	// KeepAliveReplyWait <= 0 使用默认值（单次保活请求等待应答的上限）。
	KeepAliveReplyWait time.Duration
	// NoLocaleProbe 为 true 时不探测、也不下发字符集（默认会探测，见 localeProbeScript）。
	NoLocaleProbe bool
}

// ---------- 字符集协商 ----------
//
// 浏览器与 xterm.js 发来的永远是 UTF-8，而对方 shell 用什么字符集取决于它自己的 locale。
// 如果对方登录 shell 运行在 C / POSIX 下（最小化的 Debian、很多云主机镜像都是这样），bash 的 readline 会把
// UTF-8 的高位字节当 Meta 键：中文输入表现为乱跳、没反应，或者一行 \344\275\240 的八进制转义。
// 普通 ssh 客户端会把本机的 LANG / LC_* 带过去（SendEnv），Hub 没有「本机」，所以要自己探测并下发。

// localeProbeScript 经 `sh -s` 从 stdin 读入，在对方主机上探测：默认字符集是不是 UTF-8；
// 不是的话，有没有一个确实能得到 UTF-8 的 locale。输出只有 RM_ 开头的行。
// 注意它不能带 scriptPrelude 里的 LC_ALL=C：这里要看的正是对方的真实默认值。
const localeProbeScript = `c=$(locale charmap 2>/dev/null)
echo "RM_CHARMAP $c"
if [ "$c" != "UTF-8" ]; then
  for l in C.UTF-8 C.utf8 en_US.UTF-8 en_US.utf8; do
    if [ "$(LC_ALL=$l locale charmap 2>/dev/null)" = "UTF-8" ]; then echo "RM_UTF8 $l"; exit 0; fi
  done
  for l in $(locale -a 2>/dev/null | grep -i 'utf-\{0,1\}8' | head -20); do
    if [ "$(LC_ALL=$l locale charmap 2>/dev/null)" = "UTF-8" ]; then echo "RM_UTF8 $l"; exit 0; fi
  done
fi
exit 0
`

// localeProbeTimeout 是探测的总时限：对方响应慢或不是 POSIX 环境时宁可放弃，也不能拖住打开终端。
const localeProbeTimeout = 8 * time.Second

// localeNameRe 限制 locale 名只能是安全字符：它会被放进环境变量，兜底方案里还会出现在命令行上。
var localeNameRe = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.@-]{0,39}$`)

// ParseLocaleProbe 解析探测输出：对方当前的字符集，以及一个可用的 UTF-8 locale 名（没有则为空）。
func ParseLocaleProbe(out []byte) (charmap, utf8 string) {
	for _, line := range rmLines(out) {
		switch key, val := splitRM(line); key {
		case "CHARMAP":
			charmap = val
		case "UTF8":
			if localeNameRe.MatchString(val) {
				utf8 = val
			}
		}
	}
	return charmap, utf8
}

// utf8Hint 返回需要下发给对方 shell 的 UTF-8 locale 名。
// 对方默认已是 UTF-8、探测失败（非 POSIX 主机如 Windows）、找不到可用的 locale 时都返回空：此时什么都不改。
func (c *Client) utf8Hint() string {
	ctx, cancel := context.WithTimeout(context.Background(), localeProbeTimeout)
	defer cancel()
	res, err := c.Exec(ctx, RemoteShell, []byte(localeProbeScript), 4096)
	if err != nil || res.Exit != 0 {
		return ""
	}
	charmap, loc := ParseLocaleProbe(res.Stdout)
	if strings.EqualFold(charmap, "UTF-8") {
		return ""
	}
	return loc
}

// utf8Bootstrap 是对方拒绝环境变量时的兜底：不走 shell 请求，改为让登录 shell 在带上 locale 的环境里启动。
// loc 已通过 localeNameRe 校验，可以安全拼进命令。
func utf8Bootstrap(loc string) string {
	return `exec env LC_CTYPE=` + loc + ` "${SHELL:-/bin/sh}" -l`
}

// ShellExit 描述一个 shell 会话是如何结束的。
type ShellExit struct {
	// Code 是远端 shell 的退出码；被信号终止或连接丢失时为 -1。
	Code int
	// Signal 是远端报告的终止信号名（没有则为空）。
	Signal string
	// Lost 为 true 表示不是远端自己退出，而是连接中断（网络断开、保活无应答、被本地关闭除外）。
	Lost bool
	// Reason 在 Lost 时给出已净化的简短原因。
	Reason string
}

// Shell 是一个已启动的交互式 shell。它拥有底层的 SSH 连接：使用者读完输出后必须调用 Close（会一并关闭连接）。
type Shell struct {
	client *Client
	sess   *ssh.Session
	stdin  io.WriteCloser
	out    io.Reader

	waitOnce  sync.Once
	done      chan struct{}
	exit      ShellExit
	closed    chan struct{}
	closeOnce sync.Once
	stopKA    chan struct{}

	mu      sync.Mutex
	killed  bool
	kaLost  string // 保活判定连接已断开时的原因
	noticed bool   // 已经把「连接中断」提示送出
	pending []byte // 尚未送出的提示字节
}

// OpenShell 在已认证的连接上申请伪终端并启动登录 shell。成功后 Shell 接管这条连接；失败时连接已被关闭。
func (c *Client) OpenShell(opts ShellOptions) (*Shell, error) {
	term := opts.Term
	if term == "" {
		term = defaultTerm
	}
	rows, cols := int(opts.Rows), int(opts.Cols)
	if rows <= 0 {
		rows = 24
	}
	if cols <= 0 {
		cols = 80
	}
	fail := func(err error) (*Shell, error) {
		_ = c.Close()
		return nil, err
	}
	// 先探测对方的字符集（独立的 exec 会话，失败一律当作「不需要处理」）。
	hint := ""
	if !opts.NoLocaleProbe {
		hint = c.utf8Hint()
	}
	sess, err := c.c.NewSession()
	if err != nil {
		return fail(fmt.Errorf("%w：%v", ErrConn, err))
	}
	// IUTF8（RFC 8160）让对方的行规程按 UTF-8 字符擦除；不认识这个模式的服务器会直接忽略它。
	modes := ssh.TerminalModes{ssh.ECHO: 1, ssh.IUTF8: 1, ssh.TTY_OP_ISPEED: 14400, ssh.TTY_OP_OSPEED: 14400}
	if err := sess.RequestPty(term, rows, cols, modes); err != nil {
		_ = sess.Close()
		return fail(fmt.Errorf("%w：服务器拒绝分配终端（%v）", ErrConn, err))
	}
	// 对方默认不是 UTF-8：优先只下发字符类别 LC_CTYPE，其次 LANG；sshd 没有开 AcceptEnv 时两者都会被拒绝，
	// 这时改用「带环境启动登录 shell」的兜底方式。
	bootstrap := ""
	if hint != "" && sess.Setenv("LC_CTYPE", hint) != nil && sess.Setenv("LANG", hint) != nil {
		bootstrap = utf8Bootstrap(hint)
	}
	stdin, err := sess.StdinPipe()
	if err != nil {
		_ = sess.Close()
		return fail(fmt.Errorf("%w：%v", ErrConn, err))
	}
	out, err := sess.StdoutPipe()
	if err != nil {
		_ = sess.Close()
		return fail(fmt.Errorf("%w：%v", ErrConn, err))
	}
	started := bootstrap != "" && sess.Start(bootstrap) == nil
	if !started {
		if err := sess.Shell(); err != nil {
			_ = sess.Close()
			return fail(fmt.Errorf("%w：服务器拒绝启动 shell（%v）", ErrConn, err))
		}
	}
	s := &Shell{client: c, sess: sess, stdin: stdin, out: out,
		done: make(chan struct{}), closed: make(chan struct{}), stopKA: make(chan struct{})}
	go s.waitLoop()
	every, misses, wait := opts.KeepAliveEvery, opts.KeepAliveMisses, opts.KeepAliveReplyWait
	if every <= 0 {
		every = DefaultKeepAliveEvery
	}
	if misses <= 0 {
		misses = DefaultKeepAliveMisses
	}
	if wait <= 0 {
		wait = keepAliveReplyWait
	}
	go s.keepAlive(every, misses, wait)
	return s, nil
}

// waitLoop 等待远端 shell 结束并记录结果。
func (s *Shell) waitLoop() {
	err := s.sess.Wait()
	s.mu.Lock()
	killed, kaLost := s.killed, s.kaLost
	s.mu.Unlock()
	ex := ShellExit{}
	var ee *ssh.ExitError
	var em *ssh.ExitMissingError
	switch {
	case err == nil:
	case errors.As(err, &ee):
		ex.Code = ee.ExitStatus()
		if sig := ee.Signal(); sig != "" {
			ex.Signal, ex.Code = sig, -1
		}
	case killed:
		ex.Code = -1 // 本地主动结束：不算「连接中断」
	default:
		ex.Code, ex.Lost = -1, true
		switch {
		case kaLost != "":
			ex.Reason = kaLost
		case errors.As(err, &em):
			ex.Reason = "连接已关闭，没有收到退出状态"
		default:
			ex.Reason = "连接中断"
		}
	}
	s.exit = ex
	close(s.done)
	close(s.stopKA)
	// 不在这里关闭连接：缓冲区里可能还有没读完的尾部输出，由使用者在读完之后调用 Close。
}

// keepAlive 周期性发送保活请求；连续 misses 次没有应答就判定连接已断开并关闭它。
func (s *Shell) keepAlive(every time.Duration, misses int, wait time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	missed := 0
	for {
		select {
		case <-s.stopKA:
			return
		case <-s.closed:
			return
		case <-t.C:
		}
		replied := make(chan struct{})
		go func() {
			_, _, _ = s.client.c.SendRequest("keepalive@openssh.com", true, nil)
			close(replied)
		}()
		timer := time.NewTimer(wait)
		select {
		case <-replied:
			timer.Stop()
			missed = 0
		case <-timer.C:
			missed++
			if missed >= misses {
				s.mu.Lock()
				s.kaLost = fmt.Sprintf("保活连续 %d 次无应答", missed)
				s.mu.Unlock()
				_ = s.client.Close()
				return
			}
		case <-s.stopKA:
			timer.Stop()
			return
		case <-s.closed:
			timer.Stop()
			return
		}
	}
}

// Read 读取远端输出（伪终端已把 stderr 合并进来）。远端结束后返回 io.EOF；
// 如果结束原因是连接中断，会先把一行可见的提示作为输出送出，让回放里也留下原因。
func (s *Shell) Read(p []byte) (int, error) {
	s.mu.Lock()
	if len(s.pending) > 0 {
		n := copy(p, s.pending)
		s.pending = s.pending[n:]
		s.mu.Unlock()
		return n, nil
	}
	noticed := s.noticed
	s.mu.Unlock()
	if noticed {
		return 0, io.EOF
	}
	n, err := s.out.Read(p)
	if n > 0 {
		return n, nil
	}
	if err == nil {
		return 0, nil
	}
	// 输出结束：等结果落定（有界），连接中断时补一行提示。
	select {
	case <-s.done:
	case <-time.After(2 * time.Second):
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.noticed = true
	if s.exit.Lost {
		msg := []byte("\r\n\x1b[31m[RelayMesh] SSH 连接已断开：" + s.exit.Reason + "\x1b[0m\r\n")
		n := copy(p, msg)
		s.pending = msg[n:]
		return n, nil
	}
	return 0, io.EOF
}

// Write 向远端写入输入。
func (s *Shell) Write(p []byte) (int, error) { return s.stdin.Write(p) }

// Resize 通知远端窗口大小变化。
func (s *Shell) Resize(rows, cols uint16) error {
	return s.sess.WindowChange(int(rows), int(cols))
}

// Wait 阻塞到会话结束并返回结果；可重复调用。
func (s *Shell) Wait() ShellExit {
	<-s.done
	return s.exit
}

// Kill 请求结束会话：先发 SIGHUP 并关闭输入，宽限期过后仍未结束则直接关闭连接。不阻塞。
func (s *Shell) Kill() {
	s.mu.Lock()
	s.killed = true
	s.mu.Unlock()
	grace := killGrace
	go func() {
		_ = s.sess.Signal(ssh.SIGHUP)
		_ = s.stdin.Close()
		select {
		case <-s.done:
		case <-time.After(grace):
			s.Close()
		}
	}()
}

// Close 关闭会话与底层连接（幂等），并解除阻塞中的 Read / Write。
func (s *Shell) Close() error {
	s.closeOnce.Do(func() {
		close(s.closed)
		_ = s.sess.Close()
		_ = s.client.Close()
	})
	return nil
}
