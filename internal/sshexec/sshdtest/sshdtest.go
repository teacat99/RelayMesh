// Package sshdtest 提供一个只用于测试的进程内 SSH 服务器：支持密码 / 键盘交互 / 公钥认证，
// 对每个 exec 请求用本机的 /bin/sh -c 真正执行，并把 stdin 转给子进程。
// 它让 SSH 后端的单元测试与真实进程 e2e 不依赖外部的 sshd，却能走完整的协议与真实的 shell 语义。
//
// 绝不要在生产代码里引用本包：它会执行客户端发来的任意命令。
package sshdtest

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"io"
	"net"
	"os"
	"os/exec"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

// Options 配置测试服务器。
type Options struct {
	User           string
	Password       string          // 非空则允许密码与键盘交互认证
	AuthorizedKeys []ssh.PublicKey // 非空则允许公钥认证
	Home           string          // 子进程的 HOME；为空则使用测试临时目录
	// KbdNoPrompt 模拟没有启用 PAM 的 OpenSSH：声明支持 keyboard-interactive，
	// 却不发出任何提问就直接拒绝（客户端因此收不到 INFO_REQUEST）。
	KbdNoPrompt   bool
	HostSigner    ssh.Signer   // 为空则生成新的 ed25519 主机密钥
	ExtraHostKeys []ssh.Signer // 额外的主机密钥（用于验证算法协商）
	// Handler 可以接管某条命令（例如模拟非 POSIX 主机）；返回 handled=false 则照常用 sh 执行。
	// 已经申请了伪终端的会话里的 exec（交互式启动 shell）不会经过 Handler：它要读输入，不能等到 EOF。
	Handler func(cmdline string, stdin []byte) (stdout, stderr []byte, code int, handled bool)
	// RejectEnv 让服务器对所有 env 请求回复失败（模拟没有配置 AcceptEnv 的 sshd）。
	RejectEnv bool
	// RejectPtyExec 让服务器对「已申请伪终端的会话」里的 exec 请求回复失败（模拟只允许 shell 请求的受限环境）。
	RejectPtyExec bool
}

// Server 是一个在 127.0.0.1 上监听的测试 SSH 服务器。
type Server struct {
	Addr string
	Home string

	t    testing.TB
	opts Options
	ln   net.Listener

	mu         sync.Mutex
	signer     ssh.Signer
	extra      []ssh.Signer
	conns      map[net.Conn]struct{}
	commands   []string
	accepted   int
	authTry    int
	dropNext   int
	closed     bool
	pty        Pty
	env        map[string]string
	shells     int
	muteKA     bool
	keepalives int
	wg         sync.WaitGroup
}

// Pty 是服务器最近一次收到的伪终端请求 / 窗口调整。
type Pty struct {
	Term       string
	Rows, Cols int
	// Modes 是 pty-req 里编码的终端模式（RFC 4254 §8：操作码 → 取值），例如 ssh.IUTF8。
	Modes map[uint8]uint32
}

// decodeModes 解码 pty-req 里的终端模式串：（1 字节操作码 + 4 字节取值）重复，遇到 0 或 ≥160 的操作码结束。
func decodeModes(b []byte) map[uint8]uint32 {
	modes := map[uint8]uint32{}
	for len(b) >= 1 {
		op := b[0]
		if op == 0 || op >= 160 || len(b) < 5 {
			break
		}
		modes[op] = uint32(b[1])<<24 | uint32(b[2])<<16 | uint32(b[3])<<8 | uint32(b[4])
		b = b[5:]
	}
	return modes
}

// NewSigner 生成一把新的 ed25519 主机 / 用户密钥。
func NewSigner(t testing.TB) ssh.Signer {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	s, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// NeedShell 在没有 /bin/sh 的环境（Windows 原生）里跳过调用它的测试。
//
// 本服务器对每个 exec 请求用 /bin/sh -c 真正执行命令；没有 POSIX shell 时，命令根本起不来（退出码 127），
// 失败的原因在测试服务器而不在被测代码。需要真实执行命令的用例开头调用它；不执行命令的用例（握手、认证、主机密钥）不必调用。
func NeedShell(t testing.TB) {
	t.Helper()
	if _, err := os.Stat("/bin/sh"); err != nil {
		t.Skip("需要 /bin/sh（POSIX 环境）：测试用的 SSH 服务器用它真正执行命令")
	}
}

// Start 启动服务器，测试结束时自动关闭。
func Start(t testing.TB, o Options) *Server {
	t.Helper()
	if o.User == "" {
		o.User = "tester"
	}
	if o.HostSigner == nil {
		o.HostSigner = NewSigner(t)
	}
	home := o.Home
	if home == "" {
		home = t.TempDir()
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{
		Addr: ln.Addr().String(), Home: home, t: t, opts: o, ln: ln,
		signer: o.HostSigner, extra: o.ExtraHostKeys, conns: map[net.Conn]struct{}{},
	}
	s.wg.Add(1)
	go s.acceptLoop()
	t.Cleanup(s.Close)
	return s
}

// HostKey 返回当前对外使用的主机公钥。
func (s *Server) HostKey() ssh.PublicKey {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.signer.PublicKey()
}

// SetHostSigner 更换主机密钥：之后的新连接使用新密钥（模拟主机被重装 / 中间人）。
func (s *Server) SetHostSigner(sg ssh.Signer) {
	s.mu.Lock()
	s.signer = sg
	s.mu.Unlock()
}

// DropConnections 关闭所有已建立的连接。
func (s *Server) DropConnections() {
	s.mu.Lock()
	conns := make([]net.Conn, 0, len(s.conns))
	for c := range s.conns {
		conns = append(conns, c)
	}
	s.mu.Unlock()
	for _, c := range conns {
		_ = c.Close()
	}
}

// DropNextExec 让接下来的 n 个 exec 请求在执行前直接断开连接（模拟网络抖动）。
func (s *Server) DropNextExec(n int) {
	s.mu.Lock()
	s.dropNext = n
	s.mu.Unlock()
}

// Commands 返回收到的所有 exec 命令行（不含 stdin）。
func (s *Server) Commands() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.commands...)
}

// Connections 返回累计接受的连接数。
func (s *Server) Connections() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.accepted
}

// AuthAttempts 返回累计收到的认证尝试次数（主机密钥不匹配时客户端不应发出任何认证）。
func (s *Server) AuthAttempts() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.authTry
}

// Close 关闭监听与所有连接。
func (s *Server) Close() {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	s.mu.Unlock()
	_ = s.ln.Close()
	s.DropConnections()
	s.wg.Wait()
}

func (s *Server) config() *ssh.ServerConfig {
	s.mu.Lock()
	signer, extra := s.signer, s.extra
	s.mu.Unlock()
	cfg := &ssh.ServerConfig{}
	if s.opts.Password != "" {
		cfg.PasswordCallback = func(c ssh.ConnMetadata, pw []byte) (*ssh.Permissions, error) {
			s.bumpAuth()
			if c.User() == s.opts.User && string(pw) == s.opts.Password {
				return nil, nil
			}
			return nil, errors.New("denied")
		}
		cfg.KeyboardInteractiveCallback = func(c ssh.ConnMetadata, ch ssh.KeyboardInteractiveChallenge) (*ssh.Permissions, error) {
			s.bumpAuth()
			if s.opts.KbdNoPrompt {
				return nil, errors.New("denied")
			}
			ans, err := ch(c.User(), "", []string{"Password: "}, []bool{false})
			if err == nil && c.User() == s.opts.User && len(ans) == 1 && ans[0] == s.opts.Password {
				return nil, nil
			}
			return nil, errors.New("denied")
		}
	}
	if len(s.opts.AuthorizedKeys) > 0 {
		cfg.PublicKeyCallback = func(c ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
			s.bumpAuth()
			if c.User() == s.opts.User {
				for _, k := range s.opts.AuthorizedKeys {
					if string(k.Marshal()) == string(key.Marshal()) {
						return nil, nil
					}
				}
			}
			return nil, errors.New("denied")
		}
	}
	cfg.AddHostKey(signer)
	for _, e := range extra {
		cfg.AddHostKey(e)
	}
	return cfg
}

func (s *Server) bumpAuth() {
	s.mu.Lock()
	s.authTry++
	s.mu.Unlock()
}

func (s *Server) acceptLoop() {
	defer s.wg.Done()
	for {
		conn, err := s.ln.Accept()
		if err != nil {
			return
		}
		s.mu.Lock()
		s.accepted++
		s.conns[conn] = struct{}{}
		s.mu.Unlock()
		s.wg.Add(1)
		go s.serve(conn)
	}
}

func (s *Server) serve(conn net.Conn) {
	defer s.wg.Done()
	defer func() {
		s.mu.Lock()
		delete(s.conns, conn)
		s.mu.Unlock()
		_ = conn.Close()
	}()
	sc, chans, reqs, err := ssh.NewServerConn(conn, s.config())
	if err != nil {
		return
	}
	defer sc.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { // 连接断开时终止还在执行的命令，避免测试清理被长命令拖住
		_ = sc.Wait()
		cancel()
	}()
	go s.globalRequests(reqs)
	var sessions sync.WaitGroup
	defer sessions.Wait()
	for nc := range chans {
		if nc.ChannelType() != "session" {
			_ = nc.Reject(ssh.UnknownChannelType, "only session")
			continue
		}
		ch, creqs, err := nc.Accept()
		if err != nil {
			continue
		}
		sessions.Add(1)
		go func() {
			defer sessions.Done()
			s.session(ctx, conn, ch, creqs)
		}()
	}
}

func (s *Server) session(ctx context.Context, conn net.Conn, ch ssh.Channel, reqs <-chan *ssh.Request) {
	defer ch.Close()
	win := &winState{}
	hasPty := false
	for req := range reqs {
		switch req.Type {
		case "pty-req":
			var p struct {
				Term                         string
				Columns, Rows, Width, Height uint32
				Modes                        string
			}
			if err := ssh.Unmarshal(req.Payload, &p); err != nil {
				_ = req.Reply(false, nil)
				continue
			}
			win.set(int(p.Rows), int(p.Columns))
			hasPty = true
			s.mu.Lock()
			s.pty = Pty{Term: p.Term, Rows: int(p.Rows), Cols: int(p.Columns), Modes: decodeModes([]byte(p.Modes))}
			s.mu.Unlock()
			_ = req.Reply(true, nil)
		case "window-change":
			var p struct{ Columns, Rows, Width, Height uint32 }
			if err := ssh.Unmarshal(req.Payload, &p); err == nil {
				win.set(int(p.Rows), int(p.Columns))
				s.mu.Lock()
				s.pty.Rows, s.pty.Cols = int(p.Rows), int(p.Columns)
				s.mu.Unlock()
			}
		case "env":
			var p struct{ Name, Value string }
			if s.opts.RejectEnv || ssh.Unmarshal(req.Payload, &p) != nil {
				_ = req.Reply(false, nil)
				continue
			}
			s.mu.Lock()
			if s.env == nil {
				s.env = map[string]string{}
			}
			s.env[p.Name] = p.Value
			s.mu.Unlock()
			_ = req.Reply(true, nil)
		case "shell":
			_ = req.Reply(true, nil)
			s.mu.Lock()
			s.shells++
			s.mu.Unlock()
			go func() {
				code := s.miniShell(ch, win)
				_, _ = ch.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{uint32(code)}))
				_ = ch.Close()
			}()
		case "exec":
			var p struct{ Command string }
			if err := ssh.Unmarshal(req.Payload, &p); err != nil {
				_ = req.Reply(false, nil)
				return
			}
			if hasPty && s.opts.RejectPtyExec {
				_ = req.Reply(false, nil)
				continue
			}
			_ = req.Reply(true, nil)
			s.mu.Lock()
			s.commands = append(s.commands, p.Command)
			drop := s.dropNext > 0
			if drop {
				s.dropNext--
			}
			s.mu.Unlock()
			if drop {
				_ = conn.Close()
				return
			}
			code := s.run(ctx, ch, p.Command, hasPty)
			_, _ = ch.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{uint32(code)}))
			return
		default:
			_ = req.Reply(false, nil)
		}
	}
}

func (s *Server) run(ctx context.Context, ch ssh.Channel, cmdline string, interactive bool) int {
	if h := s.opts.Handler; h != nil && !interactive {
		stdin, _ := io.ReadAll(ch)
		if out, errOut, code, ok := h(cmdline, stdin); ok {
			_, _ = ch.Write(out)
			_, _ = ch.Stderr().Write(errOut)
			return code
		}
		return s.exec(ctx, ch, cmdline, stdin)
	}
	return s.exec(ctx, ch, cmdline, nil)
}

func (s *Server) exec(ctx context.Context, ch ssh.Channel, cmdline string, preRead []byte) int {
	cmd := exec.CommandContext(ctx, "/bin/sh", "-c", cmdline)
	cmd.WaitDelay = time.Second
	cmd.Env = []string{"HOME=" + s.Home, "PATH=" + os.Getenv("PATH"), "USER=" + s.opts.User, "LOGNAME=" + s.opts.User}
	if preRead != nil {
		cmd.Stdin = bytesReader(preRead)
	} else {
		cmd.Stdin = ch
	}
	cmd.Stdout = ch
	cmd.Stderr = ch.Stderr()
	err := cmd.Run()
	var ee *exec.ExitError
	switch {
	case err == nil:
		return 0
	case errors.As(err, &ee):
		return ee.ExitCode()
	}
	return 127
}

type byteReader struct{ b []byte }

func bytesReader(b []byte) io.Reader { return &byteReader{b: b} }

func (r *byteReader) Read(p []byte) (int, error) {
	if len(r.b) == 0 {
		return 0, io.EOF
	}
	n := copy(p, r.b)
	r.b = r.b[n:]
	return n, nil
}
