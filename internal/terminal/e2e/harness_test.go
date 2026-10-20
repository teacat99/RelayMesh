//go:build e2e && !windows

// 真实进程端到端：Hub、会话守护进程、Agent 各是一个独立的操作系统进程，浏览器一侧由本文件里的
// WebSocket 客户端扮演（与真实浏览器使用同一套票据 + 二进制帧协议）。
//
// 运行方式见 scripts/e2e-holder/run.sh。没有设置 RM_E2E_HUB / RM_E2E_AGENT 时整组用例会被跳过，
// 所以日常 `go test ./...` 不受影响（本文件只在 -tags e2e 时才会被编译）。
//
// 凭据全部是每次运行随机生成的一次性值，只存在于子进程环境与本进程内存里，不写盘、不打印。

package e2e

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/teacat99/RelayMesh/v2/internal/terminal"
	"github.com/teacat99/RelayMesh/v2/internal/termproto"
)

// ---------- 二进制与进程 ----------

func binaries(t *testing.T) (hubBin, agentBin string) {
	t.Helper()
	hubBin, agentBin = os.Getenv("RM_E2E_HUB"), os.Getenv("RM_E2E_AGENT")
	if hubBin == "" || agentBin == "" {
		t.Skip("设置 RM_E2E_HUB / RM_E2E_AGENT 为已构建的 relaymesh 与 relaymesh-agent 后运行（见 scripts/e2e-holder/run.sh）")
	}
	return hubBin, agentBin
}

// proc 是一个受测子进程：独立进程组、输出落到日志文件，失败时把日志尾部打印出来。
type proc struct {
	t    *testing.T
	name string
	cmd  *exec.Cmd
	done chan struct{}
	log  string
}

// isolatedEnv 组装受测子进程的环境：继承当前环境，再兜底把 Agent 的数据 / 配置目录指到该进程专属的临时目录，最后才是调用方给的变量。
//
// 为什么要兜底：以 root 运行时，Agent 与会话守护进程的默认数据目录是 /var/lib/relaymesh-agent，完全不理会 HOME / XDG_*。
// 只要哪个用例漏传 --data-dir，进程就会连上这台机器上真实在用的守护进程——改写真实会话的记录、把真实 Agent 挤下线
// （真实事故，见 .cursor/decisions.md D-213）。命令行里的 --data-dir / --config-dir 优先于环境变量，所以显式传参的用例不受影响。
func isolatedEnv(dir string, extra []string) []string {
	env := append(os.Environ(),
		"RELAYMESH_AGENT_DATA_DIR="+filepath.Join(dir, "agent-data"),
		"RELAYMESH_AGENT_CONFIG_DIR="+filepath.Join(dir, "agent-config"),
	)
	return append(env, extra...)
}

func startProc(t *testing.T, name, bin string, env []string, args ...string) *proc {
	t.Helper()
	logPath := filepath.Join(t.TempDir(), name+".log")
	lf, err := os.Create(logPath)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(bin, args...)
	cmd.Env = isolatedEnv(filepath.Dir(logPath), env)
	cmd.Stdout, cmd.Stderr = lf, lf
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatalf("启动 %s 失败: %v", name, err)
	}
	p := &proc{t: t, name: name, cmd: cmd, done: make(chan struct{}), log: logPath}
	go func() {
		_ = cmd.Wait()
		_ = lf.Close()
		close(p.done)
	}()
	t.Cleanup(func() {
		p.kill9()
		if t.Failed() {
			t.Logf("---- %s 日志尾部 ----\n%s", name, tail(logPath, 40))
		}
	})
	return p
}

func tail(path string, lines int) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return err.Error()
	}
	parts := strings.Split(strings.TrimRight(string(b), "\n"), "\n")
	if len(parts) > lines {
		parts = parts[len(parts)-lines:]
	}
	return strings.Join(parts, "\n")
}

func (p *proc) pid() int { return p.cmd.Process.Pid }

func (p *proc) alive() bool {
	select {
	case <-p.done:
		return false
	default:
		return true
	}
}

// kill9 用 SIGKILL 结束进程（模拟崩溃 / 被强杀），并等它真正退出。
func (p *proc) kill9() {
	if !p.alive() {
		return
	}
	_ = syscall.Kill(p.pid(), syscall.SIGKILL)
	<-p.done
}

// term 发 SIGTERM 请求优雅退出，超时再强杀。
func (p *proc) term(timeout time.Duration) {
	if !p.alive() {
		return
	}
	_ = syscall.Kill(p.pid(), syscall.SIGTERM)
	select {
	case <-p.done:
	case <-time.After(timeout):
		p.kill9()
	}
}

// pidAlive 判断任意进程是否存活（排除僵尸）。
func pidAlive(pid int) bool {
	// EPERM 说明进程存在但属于别的用户（例如 root 服务里的 Shell），同样算存活。
	if pid <= 0 {
		return false
	}
	if err := syscall.Kill(pid, 0); err != nil && err != syscall.EPERM {
		return false
	}
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return true
	}
	s := string(b)
	if i := strings.LastIndex(s, ") "); i >= 0 && i+2 < len(s) {
		return s[i+2] != 'Z'
	}
	return true
}

func waitUntil(t *testing.T, timeout time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("等待超时（%s）：%s", timeout, what)
}

func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

func randHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// ---------- Hub ----------

type hub struct {
	t         *testing.T
	bin       string
	dir       string
	env       []string
	httpPort  int
	httpsPort int
	user      string
	pass      string
	pin       string
	proc      *proc
	api       *api
}

func writeSelfSigned(t *testing.T, dir string) (certPath, keyPath, pin string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: "127.0.0.1"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	certPath, keyPath = filepath.Join(dir, "cert.pem"), filepath.Join(dir, "key.pem")
	if err := os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), 0o600); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(der)
	return certPath, keyPath, "sha256:" + hex.EncodeToString(sum[:])
}

func startHub(t *testing.T, bin string, extraEnv ...string) *hub {
	t.Helper()
	dir := t.TempDir()
	cert, key, pin := writeSelfSigned(t, dir)
	h := &hub{
		t: t, bin: bin, dir: dir,
		httpPort: freePort(t), httpsPort: freePort(t),
		user: "e2e", pass: randHex(12), pin: pin,
	}
	h.env = []string{
		"RELAYMESH_DB_PATH=" + filepath.Join(dir, "relaymesh.db"),
		"RELAYMESH_HOST=127.0.0.1",
		fmt.Sprintf("RELAYMESH_PORT=%d", h.httpPort),
		fmt.Sprintf("RELAYMESH_HTTPS_PORT=%d", h.httpsPort),
		"RELAYMESH_TLS_CERT=" + cert,
		"RELAYMESH_TLS_KEY=" + key,
		"RELAYMESH_WEB_USERNAME=" + h.user,
		"RELAYMESH_WEB_PASSWORD=" + h.pass,
		"RELAYMESH_JWT_SECRET=" + randHex(32),
		"RELAYMESH_MCP_TOKEN=" + randHex(24),
		"RELAYMESH_OPEN_BROWSER=false",
		"GIN_MODE=release",
	}
	h.env = append(h.env, extraEnv...) // 重复的键以后出现者为准
	h.start()
	return h
}

func (h *hub) base() string { return fmt.Sprintf("http://127.0.0.1:%d", h.httpPort) }

func (h *hub) start() {
	h.t.Helper()
	h.proc = startProc(h.t, "hub", h.bin, h.env)
	waitUntil(h.t, 20*time.Second, "Hub 开始监听", func() bool {
		if !h.proc.alive() {
			h.t.Fatalf("Hub 提前退出")
		}
		resp, err := http.Get(h.base() + "/")
		if err != nil {
			return false
		}
		_ = resp.Body.Close()
		return true
	})
	h.api = newAPI(h.t, h.base(), h.user, h.pass)
}

// restart 先让 Hub 优雅退出再用同一份数据重新启动。
func (h *hub) restart() {
	h.t.Helper()
	h.proc.term(10 * time.Second)
	h.start()
}

func (h *hub) agentURL() string { return fmt.Sprintf("https://127.0.0.1:%d", h.httpsPort) }

// ---------- REST ----------

type api struct {
	t      *testing.T
	base   string
	jwt    string
	unlock string
	pass   string
	hc     *http.Client
}

func newAPI(t *testing.T, base, user, pass string) *api {
	t.Helper()
	a := &api{t: t, base: base, pass: pass, hc: &http.Client{Timeout: 15 * time.Second}}
	var login struct {
		Token string `json:"token"`
	}
	if code := a.raw("POST", "/api/v1/auth/login", map[string]string{"username": user, "password": pass}, &login, false); code != 200 || login.Token == "" {
		t.Fatalf("登录失败: HTTP %d", code)
	}
	a.jwt = login.Token
	var un struct {
		Token string `json:"token"`
	}
	if code := a.raw("POST", "/api/v1/terminal/unlock", map[string]string{"password": pass}, &un, false); code != 200 || un.Token == "" {
		t.Fatalf("解锁终端失败: HTTP %d", code)
	}
	a.unlock = un.Token
	return a
}

// reunlock 重新输入一次密码换一枚新的解锁令牌：开启「允许整个文件系统」要求令牌是刚刚才验证出来的。
func (a *api) reunlock() {
	a.t.Helper()
	var un struct {
		Token string `json:"token"`
	}
	if code := a.raw("POST", "/api/v1/terminal/unlock", map[string]string{"password": a.pass}, &un, false); code != 200 || un.Token == "" {
		a.t.Fatalf("重新解锁终端失败: HTTP %d", code)
	}
	a.unlock = un.Token
}

func (a *api) raw(method, path string, body, out any, unlocked bool) int {
	a.t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, a.base+path, rd)
	if err != nil {
		a.t.Fatal(err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if a.jwt != "" {
		req.Header.Set("Authorization", "Bearer "+a.jwt)
	}
	if unlocked && a.unlock != "" {
		req.Header.Set("X-Terminal-Unlock", a.unlock)
	}
	resp, err := a.hc.Do(req)
	if err != nil {
		return -1
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if out != nil && len(data) > 0 {
		_ = json.Unmarshal(data, out)
	}
	return resp.StatusCode
}

func (a *api) call(method, path string, body, out any) int {
	a.t.Helper()
	return a.raw(method, "/api/v1/terminal"+path, body, out, true)
}

type hostsResp struct {
	Hosts   []terminal.HostView    `json:"hosts"`
	Pending []terminal.PendingView `json:"pending"`
}

func (a *api) hosts() hostsResp {
	a.t.Helper()
	var r hostsResp
	if code := a.call("GET", "/hosts", nil, &r); code != 200 {
		a.t.Fatalf("列出主机失败: HTTP %d", code)
	}
	return r
}

// host 返回指定机器码的主机视图。
func (a *api) host(code string) (terminal.HostView, bool) {
	for _, h := range a.hosts().Hosts {
		if h.MachineCode == code {
			return h, true
		}
	}
	return terminal.HostView{}, false
}

// enrollOnly 打开发现窗口，等待唯一一台待启用主机出现并启用它，返回机器码。
func (a *api) enrollOnly() string {
	a.t.Helper()
	if code := a.call("POST", "/discovery", map[string]any{}, nil); code != 200 {
		a.t.Fatalf("打开发现窗口失败: HTTP %d", code)
	}
	var code string
	waitUntil(a.t, 30*time.Second, "发现待启用主机", func() bool {
		p := a.hosts().Pending
		if len(p) == 1 {
			code = p[0].MachineCode
			return true
		}
		return false
	})
	if c := a.call("POST", "/hosts/"+code+"/enable", map[string]any{}, nil); c != 200 {
		a.t.Fatalf("启用主机失败: HTTP %d", c)
	}
	return code
}

func (a *api) openSession(code, name string) termproto.SessionInfo {
	a.t.Helper()
	var r struct {
		Session termproto.SessionInfo `json:"session"`
	}
	if c := a.call("POST", "/hosts/"+code+"/sessions", map[string]any{"name": name, "rows": 30, "cols": 100}, &r); c != 200 || r.Session.SID == "" {
		a.t.Fatalf("开会话失败: HTTP %d", c)
	}
	return r.Session
}

func (a *api) sessions(code string) (online bool, list []termproto.SessionInfo) {
	a.t.Helper()
	var r struct {
		Online   bool                    `json:"online"`
		Sessions []termproto.SessionInfo `json:"sessions"`
	}
	if c := a.call("GET", "/hosts/"+code+"/sessions", nil, &r); c != 200 {
		a.t.Fatalf("列会话失败: HTTP %d", c)
	}
	return r.Online, r.Sessions
}

// ---------- 扮演浏览器的终端客户端 ----------

type termClient struct {
	t      *testing.T
	ws     *websocket.Conn
	cancel context.CancelFunc
	mu     sync.Mutex
	buf    bytes.Buffer
	end    uint64
	text   []string // 文本控制帧（ready / exit / closed …）
	done   chan struct{}
}

func (a *api) attach(h *hub, code, sid string, since uint64) *termClient {
	a.t.Helper()
	var tk struct {
		Ticket string `json:"ticket"`
		WSPath string `json:"ws_path"`
	}
	if c := a.call("POST", "/hosts/"+code+"/sessions/"+sid+"/ticket", map[string]any{"since": since, "rows": 30, "cols": 100}, &tk); c != 200 {
		a.t.Fatalf("签发票据失败: HTTP %d", c)
	}
	ctx, cancel := context.WithCancel(context.Background())
	ws, _, err := websocket.Dial(ctx, fmt.Sprintf("ws://127.0.0.1:%d%s", h.httpPort, tk.WSPath), &websocket.DialOptions{
		Subprotocols: []string{termproto.SubprotocolBrowser, termproto.TicketSubprotocolPrefix + tk.Ticket},
	})
	if err != nil {
		cancel()
		a.t.Fatalf("连接终端 WebSocket 失败: %v", err)
	}
	ws.SetReadLimit(1 << 20)
	tc := &termClient{t: a.t, ws: ws, cancel: cancel, done: make(chan struct{})}
	go tc.readLoop(ctx)
	a.t.Cleanup(tc.close)
	return tc
}

func (tc *termClient) readLoop(ctx context.Context) {
	defer close(tc.done)
	for {
		typ, data, err := tc.ws.Read(ctx)
		if err != nil {
			return
		}
		if typ == websocket.MessageBinary {
			off, payload, derr := termproto.DecodeBrowserOutput(data)
			if derr != nil {
				continue
			}
			tc.mu.Lock()
			tc.buf.Write(payload)
			tc.end = off + uint64(len(payload))
			end := tc.end
			tc.mu.Unlock()
			ack, _ := json.Marshal(termproto.BrowserAck{T: termproto.TAck, Offset: end})
			_ = tc.ws.Write(ctx, websocket.MessageText, ack)
			continue
		}
		tc.mu.Lock()
		tc.text = append(tc.text, string(data))
		tc.mu.Unlock()
	}
}

func (tc *termClient) send(s string) {
	tc.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := tc.ws.Write(ctx, websocket.MessageBinary, termproto.EncodeBrowserInput([]byte(s))); err != nil {
		tc.t.Fatalf("发送输入失败: %v", err)
	}
}

func (tc *termClient) output() string {
	tc.mu.Lock()
	defer tc.mu.Unlock()
	return tc.buf.String()
}

func (tc *termClient) waitFor(sub string, timeout time.Duration) {
	tc.t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if strings.Contains(tc.output(), sub) {
			return
		}
		select {
		case <-tc.done:
			if strings.Contains(tc.output(), sub) {
				return
			}
			tc.t.Fatalf("连接已断开，仍未看到 %q；当前输出尾部: %q", sub, lastN(tc.output(), 300))
		case <-time.After(30 * time.Millisecond):
		}
	}
	tc.t.Fatalf("等待输出 %q 超时；当前输出尾部: %q", sub, lastN(tc.output(), 300))
}

func lastN(s string, n int) string {
	if len(s) > n {
		return s[len(s)-n:]
	}
	return s
}

func (tc *termClient) close() {
	tc.cancel()
	_ = tc.ws.CloseNow()
}

// ---------- Agent / 守护进程 ----------

// dirs 为一台测试主机准备一组短路径目录（AF_UNIX 路径有长度上限，t.TempDir() 可能超限）。
type hostDirs struct {
	data string
	cfg  string
	logs string
}

func newHostDirs(t *testing.T) hostDirs {
	t.Helper()
	root, err := os.MkdirTemp("", "rme")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	d := hostDirs{data: filepath.Join(root, "d"), cfg: filepath.Join(root, "c"), logs: filepath.Join(root, "l")}
	for _, p := range []string{d.data, d.cfg, d.logs} {
		if err := os.MkdirAll(p, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	// 清理按后进先出执行：这个钩子在删除目录之前运行，失败时把各进程的日志文件尾部打印出来。
	t.Cleanup(func() {
		if !t.Failed() {
			return
		}
		entries, _ := os.ReadDir(d.logs)
		for _, e := range entries {
			t.Logf("---- %s ----\n%s", e.Name(), tail(filepath.Join(d.logs, e.Name()), 25))
		}
	})
	return d
}

func startHolder(t *testing.T, agentBin string, d hostDirs, extra ...string) *proc {
	t.Helper()
	args := append([]string{"holder", "run", "--data-dir", d.data, "--log-file", filepath.Join(d.logs, "holder.log")}, extra...)
	p := startProc(t, "holder", agentBin, nil, args...)
	waitUntil(t, 15*time.Second, "守护进程开始监听", func() bool {
		if !p.alive() {
			t.Fatalf("守护进程提前退出")
		}
		out, err := exec.Command(agentBin, "holder", "status", "--data-dir", d.data).CombinedOutput()
		return err == nil && strings.Contains(string(out), "进程号")
	})
	return p
}

func startAgent(t *testing.T, agentBin string, h *hub, d hostDirs, name string, extra ...string) *proc {
	t.Helper()
	args := []string{"run", "--server", h.agentURL(), "--pin", h.pin, "--data-dir", d.data, "--config-dir", d.cfg,
		"--log-file", filepath.Join(d.logs, name+".log")}
	return startProc(t, name, agentBin, nil, append(args, extra...)...)
}

func holderStatus(agentBin string, d hostDirs) (string, error) {
	out, err := exec.Command(agentBin, "holder", "status", "--data-dir", d.data).CombinedOutput()
	return string(out), err
}

func holderStop(t *testing.T, agentBin string, d hostDirs) {
	t.Helper()
	if out, err := exec.Command(agentBin, "holder", "stop", "--data-dir", d.data, "--yes").CombinedOutput(); err != nil {
		t.Fatalf("holder stop 失败: %v\n%s", err, out)
	}
}
