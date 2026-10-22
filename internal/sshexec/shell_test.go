package sshexec

import (
	"bytes"
	"context"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/teacat99/RelayMesh/v2/internal/sshexec/sshdtest"
)

// shellReader 在后台持续读取 Shell 的输出，测试里按子串等待。
type shellReader struct {
	mu   sync.Mutex
	buf  bytes.Buffer
	done chan struct{}
	err  error
}

func readShell(sh *Shell) *shellReader {
	r := &shellReader{done: make(chan struct{})}
	go func() {
		defer close(r.done)
		tmp := make([]byte, 4096)
		for {
			n, err := sh.Read(tmp)
			if n > 0 {
				r.mu.Lock()
				r.buf.Write(tmp[:n])
				r.mu.Unlock()
			}
			if err != nil {
				r.err = err
				return
			}
		}
	}()
	return r
}

func (r *shellReader) text() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.buf.String()
}

func (r *shellReader) waitFor(t *testing.T, sub string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(r.text(), sub) {
			return
		}
		select {
		case <-r.done:
			if strings.Contains(r.text(), sub) {
				return
			}
			t.Fatalf("输出提前结束，没有等到 %q：%q", sub, r.text())
		case <-time.After(10 * time.Millisecond):
		}
	}
	t.Fatalf("等待 %q 超时，当前输出：%q", sub, r.text())
}

func (r *shellReader) waitEOF(t *testing.T) {
	t.Helper()
	select {
	case <-r.done:
	case <-time.After(5 * time.Second):
		t.Fatalf("读取没有结束，当前输出：%q", r.text())
	}
	if r.err != io.EOF {
		t.Fatalf("结束时应为 io.EOF，实际 %v", r.err)
	}
}

func openTestShell(t *testing.T, srv *sshdtest.Server, opts ShellOptions) *Shell {
	t.Helper()
	c, err := Dial(context.Background(), passwordSpec(srv, "tester"), time.Second*5)
	if err != nil {
		t.Fatal(err)
	}
	sh, err := c.OpenShell(opts)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sh.Close() })
	return sh
}

func newShellServer(t *testing.T) *sshdtest.Server {
	return sshdtest.Start(t, sshdtest.Options{User: "tester", Password: testPassword})
}

func TestShellInputOutputAndExitCode(t *testing.T) {
	srv := newShellServer(t)
	sh := openTestShell(t, srv, ShellOptions{Rows: 30, Cols: 100})
	rd := readShell(sh)

	rd.waitFor(t, "welcome tester")
	if _, err := sh.Write([]byte("echo hello world\r")); err != nil {
		t.Fatal(err)
	}
	rd.waitFor(t, "hello world")
	if _, err := sh.Write([]byte("exit 3\r")); err != nil {
		t.Fatal(err)
	}
	ex := sh.Wait()
	if ex.Code != 3 || ex.Lost || ex.Signal != "" {
		t.Fatalf("退出结果不对: %+v", ex)
	}
	rd.waitEOF(t)
	if strings.Contains(rd.text(), "SSH 连接已断开") {
		t.Fatalf("正常退出不应出现断开提示: %q", rd.text())
	}
	if p := srv.Pty(); p.Term != "xterm-256color" || p.Rows != 30 || p.Cols != 100 {
		t.Fatalf("伪终端请求不对: %+v", p)
	}
}

func TestShellResize(t *testing.T) {
	srv := newShellServer(t)
	sh := openTestShell(t, srv, ShellOptions{})
	rd := readShell(sh)
	rd.waitFor(t, "$ ")
	if p := srv.Pty(); p.Rows != 24 || p.Cols != 80 {
		t.Fatalf("默认尺寸应为 24×80: %+v", p)
	}
	if err := sh.Resize(41, 132); err != nil {
		t.Fatal(err)
	}
	// window-change 没有应答：反复询问，直到服务器那边看到新尺寸。
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		_, _ = sh.Write([]byte("size\r"))
		time.Sleep(50 * time.Millisecond)
		if strings.Contains(rd.text(), "41 132") {
			return
		}
	}
	t.Fatalf("窗口调整没有生效: %q", rd.text())
}

func TestShellLargeOutputIsDeliveredInOrder(t *testing.T) {
	srv := newShellServer(t)
	sh := openTestShell(t, srv, ShellOptions{})
	rd := readShell(sh)
	rd.waitFor(t, "$ ")
	_, _ = sh.Write([]byte("big 4000\r"))
	rd.waitFor(t, "line-3999")
	out := rd.text()
	last := -1
	for i := 0; i < 4000; i++ {
		idx := strings.Index(out, "line-"+itoa(i)+"\r\n")
		if idx < 0 || idx < last {
			t.Fatalf("第 %d 行缺失或乱序", i)
		}
		last = idx
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [12]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}

// 连接被对端掐断：不是远端 shell 自己退出，所以要标记 Lost，并在输出里留下可见的原因。
func TestShellConnectionLostLeavesNotice(t *testing.T) {
	srv := newShellServer(t)
	sh := openTestShell(t, srv, ShellOptions{})
	rd := readShell(sh)
	rd.waitFor(t, "$ ")

	srv.DropConnections()

	ex := sh.Wait()
	if !ex.Lost || ex.Code != -1 || ex.Reason == "" {
		t.Fatalf("应判定为连接中断: %+v", ex)
	}
	rd.waitEOF(t)
	if !strings.Contains(rd.text(), "SSH 连接已断开：") {
		t.Fatalf("输出里应有断开提示: %q", rd.text())
	}
	if n := strings.Count(rd.text(), "SSH 连接已断开"); n != 1 {
		t.Fatalf("断开提示应恰好出现一次，实际 %d 次", n)
	}
}

// NAT 静默丢包：连接没有关闭，但对端不再应答。保活连续无应答之后要主动判定断开。
func TestShellKeepAliveDetectsSilentLoss(t *testing.T) {
	srv := newShellServer(t)
	sh := openTestShell(t, srv, ShellOptions{
		KeepAliveEvery: 40 * time.Millisecond, KeepAliveMisses: 2, KeepAliveReplyWait: 60 * time.Millisecond,
	})
	rd := readShell(sh)
	rd.waitFor(t, "$ ")

	srv.MuteKeepalive(true)

	done := make(chan ShellExit, 1)
	go func() { done <- sh.Wait() }()
	select {
	case ex := <-done:
		if !ex.Lost || !strings.Contains(ex.Reason, "保活") {
			t.Fatalf("应因保活无应答判定中断: %+v", ex)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("保活没有发现静默断开的连接")
	}
	rd.waitEOF(t)
	if !strings.Contains(rd.text(), "保活连续") {
		t.Fatalf("断开提示里应说明原因: %q", rd.text())
	}
}

// 正常应答的保活不会把健康的连接误判为断开。
func TestShellKeepAliveKeepsHealthyConnection(t *testing.T) {
	srv := newShellServer(t)
	sh := openTestShell(t, srv, ShellOptions{
		KeepAliveEvery: 30 * time.Millisecond, KeepAliveMisses: 2, KeepAliveReplyWait: 200 * time.Millisecond,
	})
	rd := readShell(sh)
	rd.waitFor(t, "$ ")

	deadline := time.Now().Add(3 * time.Second)
	for srv.Keepalives() < 5 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if srv.Keepalives() < 5 {
		t.Fatalf("保活请求没有按期发出: %d", srv.Keepalives())
	}
	select {
	case <-sh.done:
		t.Fatalf("健康连接被误判为断开: %+v", sh.exit)
	default:
	}
	_, _ = sh.Write([]byte("echo still-alive\r"))
	rd.waitFor(t, "still-alive")
}

// 本地主动结束不算「连接中断」，也不会留下断开提示。
func TestShellKillIsNotConnectionLoss(t *testing.T) {
	old := killGrace
	killGrace = 100 * time.Millisecond
	t.Cleanup(func() { killGrace = old })

	srv := newShellServer(t)
	sh := openTestShell(t, srv, ShellOptions{})
	rd := readShell(sh)
	rd.waitFor(t, "$ ")

	sh.Kill()

	done := make(chan ShellExit, 1)
	go func() { done <- sh.Wait() }()
	select {
	case ex := <-done:
		// 退出码取决于远端 shell 收到挂断后怎么退出（测试服务器会以 0 退出）；关键是不能被当成连接中断。
		if ex.Lost {
			t.Fatalf("主动结束不应被判定为连接中断: %+v", ex)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Kill 之后会话没有结束")
	}
	rd.waitEOF(t)
	if strings.Contains(rd.text(), "SSH 连接已断开") {
		t.Fatalf("主动结束不应出现断开提示: %q", rd.text())
	}
}

func TestShellCloseIsIdempotentAndUnblocksRead(t *testing.T) {
	srv := newShellServer(t)
	sh := openTestShell(t, srv, ShellOptions{})
	rd := readShell(sh)
	rd.waitFor(t, "$ ")
	_ = sh.Close()
	_ = sh.Close()
	select {
	case <-rd.done:
	case <-time.After(5 * time.Second):
		t.Fatal("Close 之后 Read 没有被解除阻塞")
	}
}

// ---------- 字符集协商（中文输入） ----------

func TestParseLocaleProbe(t *testing.T) {
	cases := []struct {
		name, out, charmap, utf8 string
	}{
		{"默认已是 UTF-8", "RM_CHARMAP UTF-8\n", "UTF-8", ""},
		{"默认是 ASCII，找到可用的 UTF-8", "RM_CHARMAP ANSI_X3.4-1968\nRM_UTF8 C.UTF-8\n", "ANSI_X3.4-1968", "C.UTF-8"},
		{"没有找到可用的 UTF-8", "RM_CHARMAP ANSI_X3.4-1968\n", "ANSI_X3.4-1968", ""},
		{"忽略登录横幅等无关输出", "Welcome!\nlast login...\nRM_CHARMAP ISO-8859-1\nRM_UTF8 en_US.utf8\n", "ISO-8859-1", "en_US.utf8"},
		{"locale 名里有 shell 元字符时丢弃", "RM_CHARMAP ASCII\nRM_UTF8 C.UTF-8;reboot\n", "ASCII", ""},
		{"locale 名带空格时丢弃", "RM_CHARMAP ASCII\nRM_UTF8 a b\n", "ASCII", ""},
		{"空输出", "", "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cm, loc := ParseLocaleProbe([]byte(c.out))
			if cm != c.charmap || loc != c.utf8 {
				t.Fatalf("得到 (%q, %q)，期望 (%q, %q)", cm, loc, c.charmap, c.utf8)
			}
		})
	}
}

// probeHandler 伪造对方主机对字符集探测脚本的应答；其他命令照常执行。
func probeHandler(reply string, code int) func(string, []byte) ([]byte, []byte, int, bool) {
	return func(cmdline string, stdin []byte) ([]byte, []byte, int, bool) {
		if cmdline == RemoteShell && bytes.Contains(stdin, []byte("RM_CHARMAP")) {
			return []byte(reply), nil, code, true
		}
		return nil, nil, 0, false
	}
}

func shellServerWith(t *testing.T, o sshdtest.Options) *sshdtest.Server {
	t.Helper()
	o.User, o.Password = "tester", testPassword
	return sshdtest.Start(t, o)
}

func TestShellRequestsIUTF8(t *testing.T) {
	srv := newShellServer(t)
	openTestShell(t, srv, ShellOptions{NoLocaleProbe: true})
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && srv.Pty().Term == "" {
		time.Sleep(10 * time.Millisecond)
	}
	if v, ok := srv.Pty().Modes[ssh.IUTF8]; !ok || v != 1 {
		t.Fatalf("伪终端请求应带 IUTF8=1，实际模式: %v", srv.Pty().Modes)
	}
	if srv.Pty().Modes[ssh.ECHO] != 1 {
		t.Fatalf("ECHO 不能丢: %v", srv.Pty().Modes)
	}
}

// 对方默认字符集不是 UTF-8：下发 LC_CTYPE（只影响字符类别），不动 LANG。
func TestShellSendsUTF8LocaleWhenRemoteDefaultIsNotUTF8(t *testing.T) {
	srv := shellServerWith(t, sshdtest.Options{Handler: probeHandler("RM_CHARMAP ANSI_X3.4-1968\nRM_UTF8 C.UTF-8\n", 0)})
	sh := openTestShell(t, srv, ShellOptions{})
	rd := readShell(sh)
	rd.waitFor(t, "welcome tester")
	env := srv.Env()
	if env["LC_CTYPE"] != "C.UTF-8" {
		t.Fatalf("应下发 LC_CTYPE=C.UTF-8，实际 %v", env)
	}
	if _, ok := env["LANG"]; ok {
		t.Fatalf("LC_CTYPE 被接受后不应再动 LANG: %v", env)
	}
}

func TestShellLeavesEnvAloneWhenRemoteAlreadyUTF8(t *testing.T) {
	srv := shellServerWith(t, sshdtest.Options{Handler: probeHandler("RM_CHARMAP UTF-8\n", 0)})
	sh := openTestShell(t, srv, ShellOptions{})
	readShell(sh).waitFor(t, "welcome tester")
	if env := srv.Env(); len(env) != 0 {
		t.Fatalf("对方默认已是 UTF-8，不应下发任何环境变量: %v", env)
	}
}

// 探测失败（非 POSIX 主机、没有 sh）不能影响打开终端，也不能下发任何东西。
func TestShellProbeFailureIsHarmless(t *testing.T) {
	srv := shellServerWith(t, sshdtest.Options{Handler: probeHandler("'sh' is not recognized as an internal or external command\n", 1)})
	sh := openTestShell(t, srv, ShellOptions{})
	readShell(sh).waitFor(t, "welcome tester")
	if env := srv.Env(); len(env) != 0 {
		t.Fatalf("探测失败时不应下发环境变量: %v", env)
	}
}

func TestShellNoLocaleProbeOption(t *testing.T) {
	srv := shellServerWith(t, sshdtest.Options{Handler: probeHandler("RM_CHARMAP ASCII\nRM_UTF8 C.UTF-8\n", 0)})
	sh := openTestShell(t, srv, ShellOptions{NoLocaleProbe: true})
	readShell(sh).waitFor(t, "welcome tester")
	for _, c := range srv.Commands() {
		if c == RemoteShell {
			t.Fatalf("NoLocaleProbe 时不应执行探测: %v", srv.Commands())
		}
	}
	if env := srv.Env(); len(env) != 0 {
		t.Fatalf("NoLocaleProbe 时不应下发环境变量: %v", env)
	}
}

// sshd 没有配置 AcceptEnv（Alpine、Arch 的默认配置）：LC_CTYPE 与 LANG 都被拒绝，
// 改用「带环境启动登录 shell」的兜底方式，对方的 shell 最终仍然运行在 UTF-8 下。
func TestShellFallsBackToBootstrapWhenEnvIsRejected(t *testing.T) {
	srv := shellServerWith(t, sshdtest.Options{
		RejectEnv: true,
		Handler:   probeHandler("RM_CHARMAP ANSI_X3.4-1968\nRM_UTF8 C.UTF-8\n", 0),
	})
	sh := openTestShell(t, srv, ShellOptions{})
	rd := readShell(sh)
	if _, err := sh.Write([]byte("echo \"LC=$LC_CTYPE\"\nexit 0\n")); err != nil {
		t.Fatal(err)
	}
	rd.waitFor(t, "LC=C.UTF-8")
	want := `exec env LC_CTYPE=C.UTF-8 "${SHELL:-/bin/sh}" -l`
	found := false
	for _, c := range srv.Commands() {
		found = found || c == want
	}
	if !found {
		t.Fatalf("应发出兜底启动命令 %q，实际命令: %q", want, srv.Commands())
	}
	if n := srv.Shells(); n != 0 {
		t.Fatalf("兜底方式不应再发 shell 请求，实际 %d", n)
	}
}

// 兜底方式不可用（对方连 exec 都拒绝）时，仍要回到普通的 shell 请求，终端必须能打开。
func TestShellBootstrapFailureStillOpensPlainShell(t *testing.T) {
	srv := shellServerWith(t, sshdtest.Options{
		RejectEnv:     true,
		RejectPtyExec: true,
		Handler:       probeHandler("RM_CHARMAP ANSI_X3.4-1968\nRM_UTF8 C.UTF-8\n", 0),
	})
	sh := openTestShell(t, srv, ShellOptions{})
	readShell(sh).waitFor(t, "welcome tester")
	if n := srv.Shells(); n != 1 {
		t.Fatalf("应回退到普通 shell 请求，实际 %d", n)
	}
}
