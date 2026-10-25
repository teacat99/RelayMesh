package holder

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/teacat99/RelayMesh/v2/internal/agent/ptyhost"
	"github.com/teacat99/RelayMesh/v2/internal/sshexec"
	"github.com/teacat99/RelayMesh/v2/internal/sshexec/sshdtest"
	"github.com/teacat99/RelayMesh/v2/internal/termproto"
)

const (
	gwPassword = "gw-pass-SECRET-7Qk"
	gwTarget   = "RM-ABCD-EFGH-JKMN-PQRS"
	gwTarget2  = "RM-ZZZZ-EFGH-JKMN-PQRS"
)

// logSink 收集守护进程的全部日志，用来断言凭据不会被打印。
type logSink struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (l *logSink) logf(format string, args ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	fmt.Fprintf(&l.buf, format+"\n", args...)
}

func (l *logSink) text() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.String()
}

// gwOpts 补上一个独立的凭据库目录（测试结束自动清理）。
func gwOpts(t *testing.T, o SSHOptions) *SSHOptions {
	t.Helper()
	if o.CredDir == "" {
		o.CredDir = t.TempDir()
	}
	return &o
}

func newSSHEnv(t *testing.T, ssh *SSHOptions) (*hostEnv, *logSink) {
	t.Helper()
	return newSSHEnvExec(t, ssh, nil)
}

// newSSHEnvExec 与 newSSHEnv 相同，另可指定本机的模型执行选项（用来测试一票否决）。
func newSSHEnvExec(t *testing.T, ssh *SSHOptions, exec *ExecOptions) (*hostEnv, *logSink) {
	t.Helper()
	sink := &logSink{}
	spawn := &ptyhost.FakeSpawner{}
	h, err := NewHost(HostOptions{
		Manager: ManagerOptions{
			Spawner: spawn,
			Shell: func(rows, cols uint16) (ptyhost.Spec, error) {
				return ptyhost.Spec{Path: "/bin/fakeshell", Rows: rows, Cols: cols}, nil
			},
			SSH:  ssh,
			Logf: sink.logf,
		},
		Exec:    exec,
		Version: "1.0-test",
		Logf:    sink.logf,
	})
	if err != nil {
		t.Fatal(err)
	}
	h.Start()
	t.Cleanup(func() { h.Close(false) })
	return &hostEnv{host: h, spawn: spawn}, sink
}

// gwSpec 是登记 / 修改凭据时随带明文凭据的目标（只出现在 check 里）。
func gwSpec(srv *sshdtest.Server) termproto.SSHTarget {
	return termproto.SSHTarget{
		ID: gwTarget, Addr: srv.Addr, User: "tester", Auth: sshexec.AuthPassword, Password: gwPassword,
		HostKey: sshexec.NewHostKey(srv.HostKey()).Encode(),
	}
}

// ref 把随带凭据的目标换成「引用网关上已保存凭据」的目标：不再携带任何凭据。
func ref(tg termproto.SSHTarget) termproto.SSHTarget {
	return termproto.SSHTarget{ID: tg.ID, Addr: tg.Addr, User: tg.User, HostKey: tg.HostKey, Stored: true}
}

func gwServer(t *testing.T) *sshdtest.Server {
	return sshdtest.Start(t, sshdtest.Options{User: "tester", Password: gwPassword})
}

// result 读取下一个 ssh_result。
func (p *peer) result() termproto.SSHResult {
	p.t.Helper()
	var res termproto.SSHResult
	if err := json.Unmarshal(p.expect(termproto.TSSHResult), &res); err != nil {
		p.t.Fatal(err)
	}
	return res
}

// store 经 check + persist 把凭据保存到网关，返回结果。
func (p *peer) store(req string, tg termproto.SSHTarget) termproto.SSHResult {
	p.t.Helper()
	p.sendJSON(termproto.SSHOp{T: termproto.TSSHOp, Req: req, Op: termproto.SSHOpCheck, Target: tg, Persist: true})
	return p.result()
}

// mustStore 保存凭据并要求成功。
func (p *peer) mustStore(req string, tg termproto.SSHTarget) termproto.SSHResult {
	p.t.Helper()
	res := p.store(req, tg)
	if !res.OK {
		p.t.Fatalf("保存凭据失败: %+v", res)
	}
	return res
}

// execOp 经网关在目标上执行一段脚本（sh -s + stdin），返回结果。
func (p *peer) execOp(req string, tg termproto.SSHTarget, script string, maxOut int) termproto.SSHResult {
	p.t.Helper()
	p.sendJSON(termproto.SSHOp{T: termproto.TSSHOp, Req: req, Op: termproto.SSHOpExec, Target: tg,
		Cmd: sshexec.RemoteShell, Stdin: []byte(script), MaxOut: maxOut})
	return p.result()
}

// openSSH 发出一个带 SSH 目标的 open，返回 opened 或 open_failed 的原始帧。
func (p *peer) openSSH(req string, tg termproto.SSHTarget) (opened *termproto.Opened, failed *termproto.OpenFailed) {
	p.t.Helper()
	p.sendJSON(termproto.Open{T: termproto.TOpen, Req: req, Name: "prod", Rows: 24, Cols: 80, SSH: &tg})
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		mt, data, err := p.next(time.Until(deadline))
		if err != nil {
			p.t.Fatalf("等待 opened / open_failed 时连接出错: %v", err)
		}
		if mt != MsgText {
			continue
		}
		switch typ, _ := termproto.ParseType(data); typ {
		case termproto.TOpened:
			var m termproto.Opened
			if json.Unmarshal(data, &m) == nil && m.Req == req {
				return &m, nil
			}
		case termproto.TOpenFailed:
			var m termproto.OpenFailed
			if json.Unmarshal(data, &m) == nil && m.Req == req {
				return nil, &m
			}
		}
	}
	p.t.Fatal("等待 opened / open_failed 超时")
	return nil, nil
}

// readUntil 读取某通道的输出直到累计内容包含 sub。
func (p *peer) readUntil(ch uint32, sub string) string {
	p.t.Helper()
	var acc strings.Builder
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		mt, data, err := p.next(time.Until(deadline))
		if err != nil {
			p.t.Fatalf("等待输出 %q 时连接出错: %v（已收到 %q）", sub, err, acc.String())
		}
		if mt != MsgBinary {
			continue
		}
		c, _, payload, derr := termproto.DecodeOutput(data)
		if derr != nil || c != ch {
			continue
		}
		acc.Write(payload)
		if strings.Contains(acc.String(), sub) {
			return acc.String()
		}
	}
	p.t.Fatalf("等待输出 %q 超时，已收到 %q", sub, acc.String())
	return ""
}

func TestSSHGatewayAdvertisesCapabilityOnlyWhenEnabled(t *testing.T) {
	on, _ := newSSHEnv(t, gwOpts(t, SSHOptions{}))
	hello := on.host.Hello()
	if !hasCap(hello.Caps, CapSSH) || !hasCap(hello.Caps, CapSSHStore) {
		t.Fatalf("开启网关且凭据库可用时应声明 ssh 与 ssh_store: %v", hello.Caps)
	}
	off, _ := newSSHEnv(t, nil)
	if hello := off.host.Hello(); hasCap(hello.Caps, CapSSH) || hasCap(hello.Caps, CapSSHStore) {
		t.Fatalf("未开启网关不应声明 ssh 能力: %v", hello.Caps)
	}
}

// 凭据库打不开（没有目录 / 目录不可写）时，网关仍然开启，但不宣告 ssh_store：Hub 不会把主机登记到它上面。
func TestSSHGatewayWithoutUsableStoreDoesNotAdvertiseStore(t *testing.T) {
	noDir, logs := newSSHEnv(t, &SSHOptions{})
	hello := noDir.host.Hello()
	if !hasCap(hello.Caps, CapSSH) || hasCap(hello.Caps, CapSSHStore) {
		t.Fatalf("没有凭据库目录：应有 ssh、没有 ssh_store: %v", hello.Caps)
	}
	if !strings.Contains(logs.text(), "凭据库不可用") {
		t.Fatalf("应记录凭据库不可用的原因: %q", logs.text())
	}

	file := filepath.Join(t.TempDir(), "occupied")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	bad, _ := newSSHEnv(t, &SSHOptions{CredDir: filepath.Join(file, "sub")}) // 父路径是个文件
	if hello := bad.host.Hello(); hasCap(hello.Caps, CapSSHStore) {
		t.Fatalf("目录不可用：不应声明 ssh_store: %v", hello.Caps)
	}

	srv := gwServer(t)
	p := noDir.dial(t, RoleController)
	if res := p.store("s1", gwSpec(srv)); res.OK || res.Code != termproto.SSHCodeStore {
		t.Fatalf("没有凭据库时保存应返回 cred_store: %+v", res)
	}
	if n := srv.AuthAttempts(); n != 0 {
		t.Fatalf("没有地方保存凭据时不应去登录目标: %d 次认证", n)
	}
	if _, failed := p.openSSH("r1", ref(gwSpec(srv))); failed == nil || failed.Code != termproto.SSHCodeStore {
		t.Fatalf("没有凭据库时开会话应返回 cred_store: %+v", failed)
	}
}

func hasCap(caps []string, c string) bool {
	for _, x := range caps {
		if x == c {
			return true
		}
	}
	return false
}

func TestSSHSessionEndToEnd(t *testing.T) {
	srv := gwServer(t)
	env, _ := newSSHEnv(t, gwOpts(t, SSHOptions{}))
	p := env.dial(t, RoleController)
	p.mustStore("s1", gwSpec(srv))

	opened, failed := p.openSSH("r1", ref(gwSpec(srv)))
	if failed != nil {
		t.Fatalf("开会话失败: %+v", failed)
	}
	info := opened.Session
	if info.Shell != "ssh" || info.Target != gwTarget || info.PID != 0 || info.State != termproto.SessionRunning {
		t.Fatalf("会话信息不对: %+v", info)
	}
	p.attach(1, info.SID, 0)
	p.readUntil(1, "welcome tester")

	p.sendBinary(termproto.EncodeInput(1, []byte("echo ping-pong\r")))
	p.readUntil(1, "ping-pong")

	p.sendJSON(termproto.Resize{T: termproto.TResize, SID: info.SID, Rows: 40, Cols: 120})
	waitFor(t, "服务器看到新的窗口大小", func() bool { pt := srv.Pty(); return pt.Rows == 40 && pt.Cols == 120 })

	p.sendBinary(termproto.EncodeInput(1, []byte("exit 7\r")))
	var ex termproto.Exit
	if err := json.Unmarshal(p.expect(termproto.TExit), &ex); err != nil {
		t.Fatal(err)
	}
	if ex.Code == nil || *ex.Code != 7 {
		t.Fatalf("退出码应为 7: %+v", ex)
	}
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("等待超时：%s", what)
}

func TestSSHGatewayOffRejectsTargetsAndOps(t *testing.T) {
	srv := gwServer(t)
	env, _ := newSSHEnv(t, nil)
	p := env.dial(t, RoleController)

	_, failed := p.openSSH("r1", ref(gwSpec(srv)))
	if failed == nil || failed.Code != termproto.SSHCodeDisabled {
		t.Fatalf("未开启网关必须拒绝: %+v", failed)
	}
	if n := srv.Connections(); n != 0 {
		t.Fatalf("拒绝时不应去连接目标，实际连接数 %d", n)
	}

	for i, op := range []termproto.SSHOp{
		{Op: termproto.SSHOpProbe, Target: gwSpec(srv)},
		{Op: termproto.SSHOpCheck, Target: gwSpec(srv), Persist: true},
		{Op: termproto.SSHOpExec, Target: ref(gwSpec(srv)), Cmd: "sh -s", Stdin: []byte("true")},
		{Op: termproto.SSHOpCredDel, Target: termproto.SSHTarget{ID: gwTarget}},
		{Op: termproto.SSHOpCredSync, Keep: []string{gwTarget}},
	} {
		op.T, op.Req = termproto.TSSHOp, fmt.Sprintf("o%d", i)
		p.sendJSON(op)
		res := p.result()
		if res.OK || res.Code != termproto.SSHCodeDisabled || res.Req != op.Req {
			t.Fatalf("ssh_op(%s) 应被拒绝: %+v", op.Op, res)
		}
	}
	if n := srv.Connections(); n != 0 {
		t.Fatalf("拒绝时不应去连接目标，实际连接数 %d", n)
	}
}

// 主机密钥不一致：必须在发送任何认证信息之前中止（凭据绝不能交给未经确认的机器）。
func TestSSHHostKeyMismatchSendsNoAuth(t *testing.T) {
	srv := gwServer(t)
	env, _ := newSSHEnv(t, gwOpts(t, SSHOptions{}))
	p := env.dial(t, RoleController)
	spec := gwSpec(srv)
	p.mustStore("s1", spec)
	srv.SetHostSigner(sshdtest.NewSigner(t)) // 主机被重装 / 中间人
	before := srv.AuthAttempts()

	_, failed := p.openSSH("r1", ref(spec))
	if failed == nil || failed.Code != termproto.SSHCodeHostKey {
		t.Fatalf("应报告主机密钥不一致: %+v", failed)
	}
	if res := p.execOp("x1", ref(spec), "true", 0); res.OK || res.Code != termproto.SSHCodeHostKey {
		t.Fatalf("exec 同样必须拒绝: %+v", res)
	}
	if n := srv.AuthAttempts(); n != before {
		t.Fatalf("主机密钥不一致时不能发出任何认证，新增了 %d 次", n-before)
	}
}

// 登记 / 修改凭据时的各种失败：都在 check 阶段暴露，并且什么都不会保存。
func TestSSHCheckErrorsAreCodedAndStoreNothing(t *testing.T) {
	srv := gwServer(t)
	opts := gwOpts(t, SSHOptions{})
	env, _ := newSSHEnv(t, opts)
	p := env.dial(t, RoleController)

	cases := []struct {
		name string
		mod  func(*termproto.SSHTarget)
		code string
	}{
		{"密码错误", func(t *termproto.SSHTarget) { t.Password = "nope" }, termproto.SSHCodeAuth},
		{"没有固定主机密钥", func(t *termproto.SSHTarget) { t.HostKey = "" }, termproto.SSHCodeNoHostKey},
		{"用户名非法", func(t *termproto.SSHTarget) { t.User = "a b" }, termproto.SSHCodeBadRequest},
		{"地址非法", func(t *termproto.SSHTarget) { t.Addr = "user@host" }, termproto.SSHCodeBadRequest},
		{"认证方式非法", func(t *termproto.SSHTarget) { t.Auth = "magic" }, termproto.SSHCodeBadRequest},
		{"私钥为空", func(t *termproto.SSHTarget) { t.Auth, t.Password, t.PrivateKey = sshexec.AuthKey, "", "" }, termproto.SSHCodeBadRequest},
		{"主机标识非法", func(t *termproto.SSHTarget) { t.ID = "not-a-code" }, termproto.SSHCodeBadRequest},
	}
	for i, c := range cases {
		tg := gwSpec(srv)
		c.mod(&tg)
		res := p.store(fmt.Sprintf("e%d", i), tg)
		if res.OK || res.Code != c.code {
			t.Errorf("%s: 期望 %s，实际 %+v", c.name, c.code, res)
		}
	}
	// 一条都没有保存：此后引用它只会得到 no_credential。
	if _, failed := p.openSSH("r1", ref(gwSpec(srv))); failed == nil || failed.Code != termproto.SSHCodeNoCred {
		t.Fatalf("失败的检查不应留下凭据: %+v", failed)
	}
}

// 引用网关上的凭据时的各种失败。
func TestSSHStoredTargetErrors(t *testing.T) {
	srv := gwServer(t)
	env, _ := newSSHEnv(t, gwOpts(t, SSHOptions{}))
	p := env.dial(t, RoleController)
	p.mustStore("s1", gwSpec(srv))

	// 没有保存过凭据的主机。
	other := ref(gwSpec(srv))
	other.ID = gwTarget2
	if _, failed := p.openSSH("a", other); failed == nil || failed.Code != termproto.SSHCodeNoCred {
		t.Errorf("没有保存凭据应返回 no_credential: %+v", failed)
	}
	if res := p.execOp("b", other, "true", 0); res.OK || res.Code != termproto.SSHCodeNoCred {
		t.Errorf("exec 没有凭据应返回 no_credential: %+v", res)
	}
	// stored 与随带凭据互斥。
	both := ref(gwSpec(srv))
	both.Password = "x"
	if _, failed := p.openSSH("c", both); failed == nil || failed.Code != termproto.SSHCodeBadRequest {
		t.Errorf("stored 同时带凭据应被拒绝: %+v", failed)
	}
	// 终端会话与 exec 都不接受随带凭据的目标：凭据只能经 check 保存到网关，再引用。
	if _, failed := p.openSSH("d", gwSpec(srv)); failed == nil || failed.Code != termproto.SSHCodeBadRequest {
		t.Errorf("open 不接受随带凭据的目标: %+v", failed)
	}
	if res := p.execOp("e", gwSpec(srv), "true", 0); res.OK || res.Code != termproto.SSHCodeBadRequest {
		t.Errorf("exec 不接受随带凭据的目标: %+v", res)
	}
	// 主机标识非法。
	bad := ref(gwSpec(srv))
	bad.ID = "nope"
	if _, failed := p.openSSH("f", bad); failed == nil || failed.Code != termproto.SSHCodeBadRequest {
		t.Errorf("非法主机标识应被拒绝: %+v", failed)
	}
	// persist 只用于随带凭据的检查。
	p.sendJSON(termproto.SSHOp{T: termproto.TSSHOp, Req: "g", Op: termproto.SSHOpCheck, Target: ref(gwSpec(srv)), Persist: true})
	if res := p.result(); res.OK || res.Code != termproto.SSHCodeBadRequest {
		t.Errorf("stored + persist 应被拒绝: %+v", res)
	}
}

func TestSSHUnreachableTarget(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close() // 端口已关闭：连接被拒绝
	env, _ := newSSHEnv(t, gwOpts(t, SSHOptions{DialTimeout: time.Second}))
	p := env.dial(t, RoleController)
	srv := gwServer(t)
	p.mustStore("s1", gwSpec(srv))
	tg := ref(gwSpec(srv))
	tg.Addr = addr
	_, failed := p.openSSH("r1", tg)
	if failed == nil || failed.Code != termproto.SSHCodeUnreachable {
		t.Fatalf("应报告不可达: %+v", failed)
	}
}

// 凭据只用于建连：无论成功还是失败，都不会出现在日志、应答、会话信息里，也不会以明文落盘。
func TestSSHCredentialsNeverLeak(t *testing.T) {
	srv := gwServer(t)
	opts := gwOpts(t, SSHOptions{})
	env, logs := newSSHEnv(t, opts)
	p := env.dial(t, RoleController)

	var frames bytes.Buffer
	tap := func(v any) { raw, _ := json.Marshal(v); frames.Write(raw); frames.WriteByte('\n') }

	bad := gwSpec(srv)
	bad.Password = gwPassword + "-wrong"
	tap(p.store("s0", bad))

	tap(p.mustStore("s1", gwSpec(srv)))
	opened, failed := p.openSSH("r2", ref(gwSpec(srv)))
	if failed != nil {
		t.Fatalf("保存后的引用应当成功: %+v", failed)
	}
	tap(opened)
	tap(p.execOp("x1", ref(gwSpec(srv)), "echo hello", 0))
	tap(env.host.Manager().Snapshot())

	for _, secret := range []string{gwPassword, gwPassword + "-wrong"} {
		if strings.Contains(frames.String(), secret) {
			t.Fatalf("应答 / 会话信息泄露了凭据: %s", frames.String())
		}
		if strings.Contains(logs.text(), secret) {
			t.Fatalf("日志泄露了凭据: %s", logs.text())
		}
		err := filepath.WalkDir(opts.CredDir, func(path string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return err
			}
			raw, rerr := os.ReadFile(path)
			if rerr != nil {
				return rerr
			}
			if strings.Contains(string(raw), secret) {
				t.Errorf("落盘文件 %s 含明文凭据", path)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}

func TestSSHOpProbeAndCheck(t *testing.T) {
	sshdtest.NeedShell(t) // 测试用 SSH 服务器要用 /bin/sh 真正执行命令；Windows 原生没有，跳过
	srv := gwServer(t)
	env, _ := newSSHEnv(t, gwOpts(t, SSHOptions{}))
	p := env.dial(t, RoleController)
	want := sshexec.NewHostKey(srv.HostKey())

	// probe 不需要凭据，也不需要已固定的主机密钥。
	p.sendJSON(termproto.SSHOp{T: termproto.TSSHOp, Req: "p1", Op: termproto.SSHOpProbe, Target: termproto.SSHTarget{Addr: srv.Addr}})
	probe := p.result()
	if !probe.OK || probe.Fingerprint != want.Fingerprint || probe.HostKey != want.Encode() || probe.KeyType != want.Type {
		t.Fatalf("probe 结果不对: %+v 期望指纹 %s", probe, want.Fingerprint)
	}
	if srv.AuthAttempts() != 0 {
		t.Fatalf("probe 不应发送任何认证信息")
	}

	// check（不落库）用固定的主机密钥与随带的凭据真实登录，并报告目标是否是 POSIX 环境。
	p.sendJSON(termproto.SSHOp{T: termproto.TSSHOp, Req: "c1", Op: termproto.SSHOpCheck, Target: gwSpec(srv)})
	chk := p.result()
	if !chk.OK || chk.Req != "c1" || chk.LatencyMS < 0 || !chk.Posix || chk.User == "" || chk.Host == "" {
		t.Fatalf("check 应成功并识别 POSIX 环境: %+v", chk)
	}
	if _, failed := p.openSSH("r1", ref(gwSpec(srv))); failed == nil || failed.Code != termproto.SSHCodeNoCred {
		t.Fatalf("不落库的 check 不应保存凭据: %+v", failed)
	}

	// 错误的凭据 / 缺少主机密钥 / 未知操作。
	wrong := gwSpec(srv)
	wrong.Password = "nope"
	noKey := gwSpec(srv)
	noKey.HostKey = ""
	for i, c := range []struct {
		op     termproto.SSHOp
		expect string
	}{
		{termproto.SSHOp{Op: termproto.SSHOpCheck, Target: wrong}, termproto.SSHCodeAuth},
		{termproto.SSHOp{Op: termproto.SSHOpCheck, Target: noKey}, termproto.SSHCodeNoHostKey},
		{termproto.SSHOp{Op: "rm -rf", Target: gwSpec(srv)}, termproto.SSHCodeBadRequest},
		{termproto.SSHOp{Op: termproto.SSHOpProbe, Target: termproto.SSHTarget{Addr: "bad addr"}}, termproto.SSHCodeBadRequest},
	} {
		c.op.T, c.op.Req = termproto.TSSHOp, "x"+string(rune('0'+i))
		p.sendJSON(c.op)
		if r := p.result(); r.OK || r.Code != c.expect {
			t.Errorf("用例 %d: 期望 %s，实际 %+v", i, c.expect, r)
		}
	}
}

// 非 POSIX 目标（不允许 exec 的设备）：check 仍然成功（终端可用），但 Posix=false，Hub 据此把它标为「仅终端」。
func TestSSHCheckReportsNonPosixTarget(t *testing.T) {
	srv := sshdtest.Start(t, sshdtest.Options{User: "tester", Password: gwPassword,
		Handler: func(cmdline string, stdin []byte) ([]byte, []byte, int, bool) {
			return nil, []byte("% Unknown command\n"), 1, true
		}})
	env, _ := newSSHEnv(t, gwOpts(t, SSHOptions{}))
	p := env.dial(t, RoleController)
	res := p.mustStore("s1", gwSpec(srv))
	if res.Posix {
		t.Fatalf("不能解析环境信息的目标不应标为 POSIX: %+v", res)
	}
	if opened, failed := p.openSSH("r1", ref(gwSpec(srv))); failed != nil || opened == nil {
		t.Fatalf("仅终端的目标仍应能开会话: %+v", failed)
	}
}

// 凭据只在真实登录成功后才落库：填错的密码不会覆盖原来可用的凭据；改成新的正确凭据后旧连接作废。
func TestSSHPersistOnlyAfterSuccessfulLogin(t *testing.T) {
	sshdtest.NeedShell(t) // 测试用 SSH 服务器要用 /bin/sh 真正执行命令；Windows 原生没有，跳过
	srv := gwServer(t)
	env, _ := newSSHEnv(t, gwOpts(t, SSHOptions{}))
	p := env.dial(t, RoleController)

	wrong := gwSpec(srv)
	wrong.Password = "nope"
	if res := p.store("s0", wrong); res.OK || res.Code != termproto.SSHCodeAuth {
		t.Fatalf("错误密码应登录失败: %+v", res)
	}
	p.mustStore("s1", gwSpec(srv))
	if res := p.store("s2", wrong); res.OK {
		t.Fatalf("错误密码不应成功: %+v", res)
	}
	// 原来可用的凭据没有被覆盖。
	if res := p.execOp("x1", ref(gwSpec(srv)), "echo still-works", 0); !res.OK || !strings.Contains(string(res.Stdout), "still-works") {
		t.Fatalf("原凭据应保持可用: %+v", res)
	}
}

func TestSSHExecProxy(t *testing.T) {
	sshdtest.NeedShell(t) // 测试用 SSH 服务器要用 /bin/sh 真正执行命令；Windows 原生没有，跳过
	srv := gwServer(t)
	env, _ := newSSHEnv(t, gwOpts(t, SSHOptions{}))
	p := env.dial(t, RoleController)
	spec := gwSpec(srv)
	p.mustStore("s1", spec)

	res := p.execOp("x1", ref(spec), "echo out-line\necho err-line >&2\nexit 3\n", 0)
	if !res.OK || res.Exit != 3 || string(res.Stdout) != "out-line\n" || string(res.Stderr) != "err-line\n" {
		t.Fatalf("exec 结果不对: %+v", res)
	}
	// stdin 原样传给远端脚本。
	if res := p.execOp("x2", ref(spec), "cat >/dev/null; printf '%s' \"$HOME\"\n", 0); !res.OK {
		t.Fatalf("exec 失败: %+v", res)
	}
	// 输出超过上限：明确失败，而不是截断后假装成功。
	big := p.execOp("x3", ref(spec), "head -c 5000 /dev/zero | tr '\\0' a\n", 1000)
	if big.OK || big.Code != termproto.SSHCodeInternal {
		t.Fatalf("输出超限应失败: %+v", big)
	}
	// 上限是两路合计：各自都没超、合起来超了同样要失败（应答帧必须放得进控制帧）。
	both := p.execOp("x3b", ref(spec), "head -c 600 /dev/zero | tr '\\0' a; head -c 600 /dev/zero | tr '\\0' b >&2\n", 1000)
	if both.OK || both.Code != termproto.SSHCodeInternal {
		t.Fatalf("两路合计超限应失败: %+v", both)
	}
	if ok := p.execOp("x3c", ref(spec), "head -c 400 /dev/zero | tr '\\0' a; head -c 500 /dev/zero | tr '\\0' b >&2\n", 1000); !ok.OK || len(ok.Stdout)+len(ok.Stderr) != 900 {
		t.Fatalf("合计未超限应成功: %+v", ok)
	}
	// 命令行本身必须有效。
	for i, cmd := range []string{"", strings.Repeat("x", maxSSHExecCmd+1), "a\x00b"} {
		p.sendJSON(termproto.SSHOp{T: termproto.TSSHOp, Req: fmt.Sprintf("c%d", i), Op: termproto.SSHOpExec, Target: ref(spec), Cmd: cmd})
		if r := p.result(); r.OK || r.Code != termproto.SSHCodeBadRequest {
			t.Errorf("无效命令 %d 应被拒绝: %+v", i, r)
		}
	}
	// 同一台主机的连续 exec 复用同一条 SSH 连接。
	if n := srv.Connections(); n != 1 {
		// 登记时的 check 用了一条独立连接，exec 复用池里的一条：最多 2 条。
		if n > 2 {
			t.Fatalf("连续 exec 不应每次新建连接: %d", n)
		}
	}
}

// exec 代理有独立于建连的并发上限，超出立即返回 busy。
func TestSSHExecConcurrencyIsBounded(t *testing.T) {
	sshdtest.NeedShell(t) // 测试用 SSH 服务器要用 /bin/sh 真正执行命令；Windows 原生没有，跳过
	srv := gwServer(t)
	env, _ := newSSHEnv(t, gwOpts(t, SSHOptions{ExecConcurrent: 1}))
	p := env.dial(t, RoleController)
	spec := gwSpec(srv)
	p.mustStore("s1", spec)

	p.sendJSON(termproto.SSHOp{T: termproto.TSSHOp, Req: "slow", Op: termproto.SSHOpExec, Target: ref(spec),
		Cmd: sshexec.RemoteShell, Stdin: []byte("sleep 1\n")})
	time.Sleep(200 * time.Millisecond)
	if res := p.execOp("second", ref(spec), "true", 0); res.OK || res.Code != termproto.SSHCodeBusy {
		t.Fatalf("超出 exec 并发上限应立即 busy: %+v", res)
	}
	if res := p.result(); !res.OK || res.Req != "slow" {
		t.Fatalf("第一个 exec 应正常完成: %+v", res)
	}
}

// 本机的一票否决（--deny-model-exec）：经网关执行命令同样被拒绝，且不会去连接目标。
func TestSSHExecIsVetoedByLocalDeny(t *testing.T) {
	srv := gwServer(t)
	env, _ := newSSHEnvExec(t, gwOpts(t, SSHOptions{}), &ExecOptions{Dir: t.TempDir(), DenyExec: true})
	p := env.dial(t, RoleController)
	spec := gwSpec(srv)
	p.mustStore("s1", spec) // 登记 / 终端不受一票否决影响（人工操作）
	before := srv.Connections()

	res := p.execOp("x1", ref(spec), "echo should-not-run", 0)
	if res.OK || res.Code != termproto.SSHCodeExecDenied {
		t.Fatalf("被一票否决时 exec 必须失败: %+v", res)
	}
	if n := srv.Connections(); n != before {
		t.Fatalf("被否决的 exec 不应去连接目标: 新增 %d 条连接", n-before)
	}
	if opened, failed := p.openSSH("r1", ref(spec)); failed != nil || opened == nil {
		t.Fatalf("一票否决不影响人工终端: %+v", failed)
	}
}

func TestSSHCredDelAndSync(t *testing.T) {
	srv := gwServer(t)
	env, _ := newSSHEnv(t, gwOpts(t, SSHOptions{}))
	p := env.dial(t, RoleController)
	a, b := gwSpec(srv), gwSpec(srv)
	b.ID = gwTarget2
	p.mustStore("s1", a)
	p.mustStore("s2", b)

	// 同步：只保留 a；b 被回收，并回报实际持有的清单。
	p.sendJSON(termproto.SSHOp{T: termproto.TSSHOp, Req: "y1", Op: termproto.SSHOpCredSync, Keep: []string{gwTarget, "garbage", "RM-AAAA-AAAA-AAAA-AAAA"}})
	sync := p.result()
	if !sync.OK || len(sync.Have) != 1 || sync.Have[0] != gwTarget || sync.Dropped != 1 {
		t.Fatalf("同步结果不对: %+v", sync)
	}
	if res := p.execOp("x1", ref(b), "true", 0); res.OK || res.Code != termproto.SSHCodeNoCred {
		t.Fatalf("被回收的主机应没有凭据: %+v", res)
	}
	if res := p.execOp("x2", ref(a), "true", 0); !res.OK {
		t.Fatalf("保留的主机仍应可用: %+v", res)
	}

	// 删除：此后不能再用，连接池里的旧连接也不会被继续使用（凭据世代已变）。
	p.sendJSON(termproto.SSHOp{T: termproto.TSSHOp, Req: "d1", Op: termproto.SSHOpCredDel, Target: termproto.SSHTarget{ID: gwTarget}})
	if res := p.result(); !res.OK {
		t.Fatalf("删除应成功: %+v", res)
	}
	if res := p.execOp("x3", ref(a), "true", 0); res.OK || res.Code != termproto.SSHCodeNoCred {
		t.Fatalf("删除后不应再能使用: %+v", res)
	}
	// 删除不存在的凭据也是成功（幂等）。
	p.sendJSON(termproto.SSHOp{T: termproto.TSSHOp, Req: "d2", Op: termproto.SSHOpCredDel, Target: termproto.SSHTarget{ID: gwTarget}})
	if res := p.result(); !res.OK {
		t.Fatalf("重复删除应幂等: %+v", res)
	}
	p.sendJSON(termproto.SSHOp{T: termproto.TSSHOp, Req: "d3", Op: termproto.SSHOpCredDel, Target: termproto.SSHTarget{ID: "bad"}})
	if res := p.result(); res.OK || res.Code != termproto.SSHCodeBadRequest {
		t.Fatalf("非法标识应被拒绝: %+v", res)
	}
}

// 重启后凭据仍在（守护进程升级 / 重启不丢凭据），并且换了进程也能继续使用。
func TestSSHCredentialsSurviveRestart(t *testing.T) {
	sshdtest.NeedShell(t) // 测试用 SSH 服务器要用 /bin/sh 真正执行命令；Windows 原生没有，跳过
	srv := gwServer(t)
	dir := t.TempDir()
	first, _ := newSSHEnv(t, &SSHOptions{CredDir: dir})
	p := first.dial(t, RoleController)
	spec := gwSpec(srv)
	p.mustStore("s1", spec)
	first.host.Close(false)

	second, _ := newSSHEnv(t, &SSHOptions{CredDir: dir})
	p2 := second.dial(t, RoleController)
	if res := p2.execOp("x1", ref(spec), "echo after-restart", 0); !res.OK || !strings.Contains(string(res.Stdout), "after-restart") {
		t.Fatalf("重启后凭据应仍可用: %+v", res)
	}
}

func TestSSHConnectionLossEndsSessionWithNotice(t *testing.T) {
	srv := gwServer(t)
	env, _ := newSSHEnv(t, gwOpts(t, SSHOptions{}))
	p := env.dial(t, RoleController)
	p.mustStore("s1", gwSpec(srv))
	opened, failed := p.openSSH("r1", ref(gwSpec(srv)))
	if failed != nil {
		t.Fatalf("开会话失败: %+v", failed)
	}
	p.attach(1, opened.Session.SID, 0)
	p.readUntil(1, "welcome tester")

	srv.DropConnections()

	p.readUntil(1, "SSH 连接已断开")
	p.expect(termproto.TExit)
}

// 建连要经过网络，不能占住控制连接：SSH 目标卡住时，普通会话照常即时开出。
func TestSSHOpenDoesNotBlockLocalSessions(t *testing.T) {
	hang, err := net.Listen("tcp", "127.0.0.1:0") // 接受连接但永不回应：握手会一直等到超时
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = hang.Close() })
	go func() {
		for {
			c, err := hang.Accept()
			if err != nil {
				return
			}
			t.Cleanup(func() { _ = c.Close() })
		}
	}()
	env, _ := newSSHEnv(t, gwOpts(t, SSHOptions{DialTimeout: 1500 * time.Millisecond}))
	p := env.dial(t, RoleController)

	srv := gwServer(t)
	p.mustStore("s1", gwSpec(srv))
	tg := ref(gwSpec(srv))
	tg.Addr = hang.Addr().String()
	p.sendJSON(termproto.Open{T: termproto.TOpen, Req: "slow", Rows: 24, Cols: 80, SSH: &tg})

	start := time.Now()
	info := p.open("local") // 普通会话
	if info.SID == "" {
		t.Fatal("普通会话应当开出")
	}
	if d := time.Since(start); d > time.Second {
		t.Fatalf("普通会话被卡住的 SSH 建连拖慢了 %v", d)
	}
	var failed termproto.OpenFailed
	if err := json.Unmarshal(p.expect(termproto.TOpenFailed), &failed); err != nil {
		t.Fatal(err)
	}
	if failed.Req != "slow" || failed.Code != termproto.SSHCodeUnreachable {
		t.Fatalf("卡住的建连最终应报告不可达: %+v", failed)
	}
}

// 同时进行的建连数有上限，超出立即返回 busy，而不是排队堆积。
func TestSSHConcurrentDialsAreBounded(t *testing.T) {
	hang, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = hang.Close() })
	go func() {
		for {
			c, err := hang.Accept()
			if err != nil {
				return
			}
			t.Cleanup(func() { _ = c.Close() })
		}
	}()
	env, _ := newSSHEnv(t, gwOpts(t, SSHOptions{DialTimeout: 1200 * time.Millisecond, MaxConcurrent: 1}))
	p := env.dial(t, RoleController)
	srv := gwServer(t)
	p.mustStore("s1", gwSpec(srv))
	slow := ref(gwSpec(srv))
	slow.Addr = hang.Addr().String()

	p.sendJSON(termproto.Open{T: termproto.TOpen, Req: "first", Rows: 24, Cols: 80, SSH: &slow})
	time.Sleep(150 * time.Millisecond)
	_, failed := p.openSSH("second", ref(gwSpec(srv)))
	if failed == nil || failed.Code != termproto.SSHCodeBusy {
		t.Fatalf("超出并发上限应立即返回 busy: %+v", failed)
	}
}

func TestSSHAllowListBlocksTargets(t *testing.T) {
	srv := gwServer(t) // 127.0.0.1
	env, _ := newSSHEnv(t, gwOpts(t, SSHOptions{Allow: []string{"10.0.0.0/8", "*.corp.example"}}))
	p := env.dial(t, RoleController)

	// 允许名单先于一切：不在名单内的目标既不能登记（check），也不能探测、执行或开会话。
	if res := p.store("s1", gwSpec(srv)); res.OK || res.Code != termproto.SSHCodeDenied {
		t.Fatalf("不在允许名单内不能登记: %+v", res)
	}
	_, failed := p.openSSH("r1", ref(gwSpec(srv)))
	if failed == nil || failed.Code != termproto.SSHCodeDenied {
		t.Fatalf("不在允许名单内必须拒绝: %+v", failed)
	}
	if res := p.execOp("x1", ref(gwSpec(srv)), "true", 0); res.OK || res.Code != termproto.SSHCodeDenied {
		t.Fatalf("exec 同样受允许名单约束: %+v", res)
	}
	p.sendJSON(termproto.SSHOp{T: termproto.TSSHOp, Req: "o1", Op: termproto.SSHOpProbe, Target: termproto.SSHTarget{Addr: srv.Addr}})
	if res := p.result(); res.OK || res.Code != termproto.SSHCodeDenied {
		t.Fatalf("probe 同样受允许名单约束: %+v", res)
	}
	if n := srv.Connections(); n != 0 {
		t.Fatalf("被拒绝的目标不应被连接，实际连接数 %d", n)
	}

	allowed, _ := newSSHEnv(t, gwOpts(t, SSHOptions{Allow: []string{"127.0.0.0/8"}}))
	p2 := allowed.dial(t, RoleController)
	p2.mustStore("s2", gwSpec(srv))
	if _, failed := p2.openSSH("r2", ref(gwSpec(srv))); failed != nil {
		t.Fatalf("在允许名单内应当放行: %+v", failed)
	}
}

func TestParseAndMatchAllowList(t *testing.T) {
	if al, err := parseAllowList(nil); err != nil || al != nil {
		t.Fatalf("空名单应为不限制: %v %v", al, err)
	}
	if al, err := parseAllowList([]string{" ", ""}); err != nil || al != nil {
		t.Fatalf("全是空白也应为不限制: %v %v", al, err)
	}
	for _, bad := range []string{"10.0.0.0/33", "bad host", "a/b/c", "user@host", "[abc"} {
		if _, err := parseAllowList([]string{bad}); err == nil {
			t.Errorf("%q 应被拒绝", bad)
		}
	}
	al, err := parseAllowList([]string{"10.1.0.0/16", "192.168.1.9", "fd00::/8", "*.corp.example", "jump.lab"})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for _, ok := range []string{"10.1.2.3:22", "192.168.1.9:2222", "[fd00::5]:22", "db.corp.example:22", "jump.lab:22", "DB.Corp.Example:22"} {
		if err := al.permits(ctx, ok); err != nil {
			t.Errorf("%s 应放行: %v", ok, err)
		}
	}
	for _, bad := range []string{"10.2.0.1:22", "192.168.1.10:22", "[fe80::1]:22", "evil.example:22", "corp.example:22", "x.jump.lab:22"} {
		if err := al.permits(ctx, bad); err == nil {
			t.Errorf("%s 应被拒绝", bad)
		}
	}
	// 主机名解析出的每个 IP 都必须在允许的网段内。
	loop, _ := parseAllowList([]string{"127.0.0.0/8", "::1"})
	if err := loop.permits(ctx, "localhost:22"); err != nil {
		t.Errorf("localhost 解析到的地址都在名单内，应放行: %v", err)
	}
	v4only, _ := parseAllowList([]string{"127.0.0.0/8"})
	if _, derr := net.LookupHost("localhost"); derr == nil {
		addrs, _ := net.LookupIP("localhost")
		hasV6 := false
		for _, a := range addrs {
			if a.To4() == nil {
				hasV6 = true
			}
		}
		if hasV6 && v4only.permits(ctx, "localhost:22") == nil {
			t.Errorf("localhost 同时解析到名单外的 IPv6 地址时应拒绝")
		}
	}
	var nilList *allowList
	if err := nilList.permits(ctx, "anything:22"); err != nil {
		t.Errorf("nil 名单应放行: %v", err)
	}
}

func TestSSHOptionsValidate(t *testing.T) {
	if err := (&SSHOptions{Allow: []string{"10.0.0.0/8", "*.lan"}}).Validate(); err != nil {
		t.Fatal(err)
	}
	if err := (&SSHOptions{Allow: []string{"10.0.0.0/99"}}).Validate(); err == nil {
		t.Fatal("非法 CIDR 应校验失败")
	}
	// 名单无效时 Manager 宁可保持关闭，也不能退化成不限制。
	m := NewManager(ManagerOptions{SSH: &SSHOptions{Allow: []string{"10.0.0.0/99"}}})
	if m.SSHEnabled() {
		t.Fatal("允许名单无效时网关必须保持关闭")
	}
}

// 凭据世代变化后连接池的键随之变化：旧凭据建立的连接不会被新请求复用。
func TestSSHPoolKeyChangesWithCredentialEpoch(t *testing.T) {
	g, err := newSSHGateway(&SSHOptions{CredDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(g.shutdown)
	sp := sshexec.Spec{Addr: "10.0.0.1:22", User: "root", HostKey: sshexec.NewHostKey(sshdtest.NewSigner(t).PublicKey())}
	k1 := g.poolKey(gwTarget, sp)
	if k1 != g.poolKey(gwTarget, sp) {
		t.Fatal("同一世代的键应稳定")
	}
	g.bumpEpoch(gwTarget)
	if k2 := g.poolKey(gwTarget, sp); k2 == k1 {
		t.Fatal("凭据被改写 / 删除后键必须变化")
	}
	if g.poolKey(gwTarget2, sp) == g.poolKey(gwTarget, sp) {
		t.Fatal("不同主机的键必须不同")
	}
}
