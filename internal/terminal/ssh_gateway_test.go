//go:build !windows

package terminal

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"log"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/teacat99/RelayMesh/v2/internal/model"
	"github.com/teacat99/RelayMesh/v2/internal/sshexec"
	"github.com/teacat99/RelayMesh/v2/internal/sshexec/sshdtest"
	"github.com/teacat99/RelayMesh/v2/internal/termexec"
	"github.com/teacat99/RelayMesh/v2/internal/termproto"
)

// 这组测试用「假网关」验证 Hub 一侧的全部行为：能力与在线校验、探测 / 登记 / 测试 / 改接 / 重新确认指纹都经网关完成、
// 登录凭据只在登记 / 重新填写那一帧里出现并由网关保存（Hub 不留副本、也不需要 SSH 主密钥）、此后的 open / exec 帧只带 stored 引用、
// 模型执行经网关代理、凭据对账与缺失提示、各种失败如何映射并落到主机状态上。
// 假网关内置一个与真实网关语义一致的凭据库（登记时落库、按机器码取用、cred_del / cred_sync），
// 并可选地把 check / exec 真实转发到进程内 SSH 服务器（见 useBackend）。
// 真实的字节通路（Hub → Agent → 守护进程 → SSH 服务器）由 e2e 目录里的进程级测试覆盖。

// gwPassword 是只存在于测试进程内的一次性口令。
const gwPassword = "gw-lab-pass-8c41d2"

const gwTargetAddr = "10.255.255.1:22" // 不可路由的地址：Hub 若绕过网关去直连，测试会立刻暴露

// gatewaySim 扮演网关上的会话守护进程：应答 ssh_op，维护一个内存里的凭据库，并记录收到的一切。
type gatewaySim struct {
	a *fakeAgent

	mu       sync.Mutex
	ops      []termproto.SSHOp // 除 cred_sync 以外的全部操作，按到达顺序
	syncs    []termproto.SSHOp // cred_sync 单独记录：它在网关就绪时自动发生，不应干扰对其余操作的精确断言
	key      *sshexec.HostKey  // 「远端服务器」当前的主机密钥
	pass     string            // 「远端服务器」接受的口令
	fail     map[string]*termproto.SSHResult
	mute     bool              // 不应答（模拟卡死的网关）
	creds    map[string]string // 网关自己的凭据库：机器码 → 口令
	notPosix bool              // check 报告「不是 POSIX 环境」
	hold     chan struct{}     // 非 nil 时，登记用的 check（persist）会等它关闭再应答
	maxOp    int               // 收到的 ssh_op 帧的最大字节数
	maxRes   int               // 发出的 ssh_result 帧的最大字节数
	srv      *sshdtest.Server  // 非 nil：check / exec 真实连到这台进程内 SSH 服务器
	pool     *sshexec.Pool
}

func newHostKey(t *testing.T) *sshexec.HostKey {
	t.Helper()
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	pk, err := ssh.NewPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	return sshexec.NewHostKey(pk)
}

func attachGatewaySim(t *testing.T, a *fakeAgent, key *sshexec.HostKey) *gatewaySim {
	g := &gatewaySim{a: a, key: key, pass: gwPassword, fail: map[string]*termproto.SSHResult{}, creds: map[string]string{}}
	g.pool = &sshexec.Pool{DialTimeout: 5 * time.Second}
	t.Cleanup(g.pool.Close)
	a.mu.Lock()
	a.extra = g.handle
	a.mu.Unlock()
	return g
}

func (g *gatewaySim) handle(t string, data []byte) {
	if t != termproto.TSSHOp {
		return
	}
	var op termproto.SSHOp
	if json.Unmarshal(data, &op) != nil {
		return
	}
	g.mu.Lock()
	if len(data) > g.maxOp {
		g.maxOp = len(data)
	}
	if op.Op == termproto.SSHOpCredSync {
		g.syncs = append(g.syncs, op)
	} else {
		g.ops = append(g.ops, op)
	}
	mute := g.mute
	forced := g.fail[op.Op]
	g.mu.Unlock()
	if mute {
		return
	}
	// 在独立的 goroutine 里应答：登记 / exec 可能要真实地联网，不能堵住假 Agent 的读循环。
	go func() {
		if forced != nil {
			r := *forced
			r.T, r.Req = termproto.TSSHResult, op.Req
			g.reply(r)
			return
		}
		g.reply(g.answer(op))
	}()
}

func (g *gatewaySim) reply(res termproto.SSHResult) {
	if raw, err := json.Marshal(res); err == nil {
		g.mu.Lock()
		if len(raw) > g.maxRes {
			g.maxRes = len(raw)
		}
		g.mu.Unlock()
	}
	g.a.send(res)
}

func (g *gatewaySim) answer(op termproto.SSHOp) termproto.SSHResult {
	res := termproto.SSHResult{T: termproto.TSSHResult, Req: op.Req}
	g.mu.Lock()
	key, pass, notPosix, srv, hold := g.key, g.pass, g.notPosix, g.srv, g.hold
	g.mu.Unlock()
	fail := func(code, msg string) termproto.SSHResult {
		res.OK, res.Code, res.Msg = false, code, msg
		return res
	}
	switch op.Op {
	case termproto.SSHOpProbe:
		res.OK, res.Addr = true, op.Target.Addr
		res.KeyType, res.HostKey, res.Fingerprint = key.Type, key.Encode(), key.Fingerprint
		return res

	case termproto.SSHOpCheck:
		if op.Target.HostKey != key.Encode() {
			return fail(termproto.SSHCodeHostKey, "host key changed")
		}
		given := op.Target.Password
		switch {
		case op.Target.Stored:
			if op.Persist {
				return fail(termproto.SSHCodeBadRequest, "persist 只用于随带凭据的检查")
			}
			g.mu.Lock()
			p, ok := g.creds[op.Target.ID]
			g.mu.Unlock()
			if !ok {
				return fail(termproto.SSHCodeNoCred, "no credential for this host")
			}
			given = p
		case op.Persist && !termproto.ValidMachineCode(op.Target.ID):
			return fail(termproto.SSHCodeBadRequest, "bad id")
		}
		if given != pass {
			return fail(termproto.SSHCodeAuth, "auth failed")
		}
		if op.Persist && hold != nil {
			<-hold
		}
		res.OK, res.Posix, res.LatencyMS = true, !notPosix, 12
		if res.Posix {
			res.Host, res.OS, res.Arch = "inner-box", "linux", "amd64"
			res.User, res.Home = op.Target.User, "/home/"+op.Target.User
			if srv != nil {
				if f, err := g.realFacts(op, given); err == nil {
					res.Host, res.OS, res.Arch, res.User, res.Home = f.Host, f.OS, f.Arch, f.User, f.Home
				}
			}
		}
		if op.Persist && !op.Target.Stored {
			g.mu.Lock()
			g.creds[op.Target.ID] = given
			g.mu.Unlock()
		}
		return res

	case termproto.SSHOpExec:
		return g.execOp(op, res)

	case termproto.SSHOpCredDel:
		g.mu.Lock()
		delete(g.creds, op.Target.ID)
		g.mu.Unlock()
		res.OK = true
		return res

	case termproto.SSHOpCredSync:
		keep := map[string]bool{}
		for _, c := range op.Keep {
			keep[c] = true
		}
		g.mu.Lock()
		for id := range g.creds {
			if !keep[id] {
				delete(g.creds, id)
				res.Dropped++
			}
		}
		for _, c := range op.Keep {
			if _, ok := g.creds[c]; ok {
				res.Have = append(res.Have, c)
			}
		}
		g.mu.Unlock()
		res.OK = true
		return res
	}
	return fail(termproto.SSHCodeBadRequest, "unknown op")
}

// spec 还原网关会用的连接规格：只认库里保存的口令，目标地址原样取自帧（与真实网关一致）。
func (g *gatewaySim) spec(op termproto.SSHOp, pass string) (sshexec.Spec, error) {
	hk, err := sshexec.DecodeHostKey(op.Target.HostKey)
	if err != nil {
		return sshexec.Spec{}, err
	}
	return sshexec.Spec{
		Addr: op.Target.Addr, User: op.Target.User, HostKey: hk,
		Cred: sshexec.Credential{Kind: sshexec.AuthPassword, Password: pass},
	}, nil
}

func (g *gatewaySim) realFacts(op termproto.SSHOp, pass string) (sshexec.Facts, error) {
	sp, err := g.spec(op, pass)
	if err != nil {
		return sshexec.Facts{}, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd, stdin := sshexec.FactsCommand()
	out, err := g.pool.Exec(ctx, "facts:"+op.Target.ID, sp, cmd, stdin, 0)
	if err != nil {
		return sshexec.Facts{}, err
	}
	return sshexec.ParseFacts(out.Stdout)
}

func (g *gatewaySim) execOp(op termproto.SSHOp, res termproto.SSHResult) termproto.SSHResult {
	fail := func(code, msg string) termproto.SSHResult {
		res.OK, res.Code, res.Msg = false, code, msg
		return res
	}
	g.mu.Lock()
	key, srv := g.key, g.srv
	pass, have := g.creds[op.Target.ID]
	g.mu.Unlock()
	switch {
	case !op.Target.Stored:
		return fail(termproto.SSHCodeBadRequest, "exec 只接受保存在网关上的凭据")
	case !have:
		return fail(termproto.SSHCodeNoCred, "no credential for this host")
	case op.Target.HostKey != key.Encode():
		return fail(termproto.SSHCodeHostKey, "host key changed")
	case srv == nil:
		return fail(termproto.SSHCodeBadRequest, "假网关没有配置真实后端")
	}
	sp, err := g.spec(op, pass)
	if err != nil {
		return fail(termproto.SSHCodeBadRequest, "bad target")
	}
	maxOut := op.MaxOut
	if maxOut <= 0 || maxOut > termproto.MaxSSHExecOut {
		maxOut = termproto.MaxSSHExecOut
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	out, err := g.pool.Exec(ctx, op.Target.ID, sp, op.Cmd, op.Stdin, maxOut)
	switch {
	case errors.Is(err, sshexec.ErrAuthFailed):
		return fail(termproto.SSHCodeAuth, "auth failed")
	case errors.Is(err, sshexec.ErrHostKeyMismatch):
		return fail(termproto.SSHCodeHostKey, "host key changed")
	case err != nil:
		return fail(termproto.SSHCodeUnreachable, "exec failed")
	case len(out.Stdout)+len(out.Stderr) > maxOut:
		return fail(termproto.SSHCodeInternal, "output too large")
	}
	res.OK, res.Stdout, res.Stderr, res.Exit = true, out.Stdout, out.Stderr, out.Exit
	return res
}

// opList 返回除 cred_sync 以外的全部操作；opsOf 按类型筛选（cred_sync 也可以）。
func (g *gatewaySim) opList() []termproto.SSHOp {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]termproto.SSHOp(nil), g.ops...)
}

func (g *gatewaySim) opsOf(kind string) []termproto.SSHOp {
	if kind == termproto.SSHOpCredSync {
		g.mu.Lock()
		defer g.mu.Unlock()
		return append([]termproto.SSHOp(nil), g.syncs...)
	}
	var out []termproto.SSHOp
	for _, o := range g.opList() {
		if o.Op == kind {
			out = append(out, o)
		}
	}
	return out
}

func (g *gatewaySim) setKey(k *sshexec.HostKey) {
	g.mu.Lock()
	g.key = k
	g.mu.Unlock()
}

func (g *gatewaySim) setNotPosix(v bool) {
	g.mu.Lock()
	g.notPosix = v
	g.mu.Unlock()
}

func (g *gatewaySim) setHold(ch chan struct{}) {
	g.mu.Lock()
	g.hold = ch
	g.mu.Unlock()
}

func (g *gatewaySim) failOp(kind string, r *termproto.SSHResult) {
	g.mu.Lock()
	if r == nil {
		delete(g.fail, kind)
	} else {
		g.fail[kind] = r
	}
	g.mu.Unlock()
}

func (g *gatewaySim) credOf(code string) (string, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	p, ok := g.creds[code]
	return p, ok
}

// dropCred 模拟网关的凭据库丢了某台主机的凭据（重装 / 数据目录被清空）。
func (g *gatewaySim) dropCred(code string) {
	g.mu.Lock()
	delete(g.creds, code)
	g.mu.Unlock()
}

func (g *gatewaySim) putCred(code, pass string) {
	g.mu.Lock()
	g.creds[code] = pass
	g.mu.Unlock()
}

func (g *gatewaySim) credCount() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return len(g.creds)
}

func (g *gatewaySim) maxFrames() (op, res int) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.maxOp, g.maxRes
}

// useBackend 让 check / exec 真实地连到进程内 SSH 服务器（key 必须与它的主机密钥一致）。
func (g *gatewaySim) useBackend(srv *sshdtest.Server) {
	g.mu.Lock()
	g.srv = srv
	g.key = sshexec.NewHostKey(srv.HostKey())
	g.mu.Unlock()
}

type gatewayFixture struct {
	env    *testEnv
	adm    []string
	gw     *fakeAgent
	sim    *gatewaySim
	key    *sshexec.HostKey
	target string           // 登记用的目标地址
	srv    *sshdtest.Server // 非 nil：真实后端
	logs   *bytes.Buffer
}

// reportCaps 让假网关向 Hub 报告会话守护进程的状态与能力。
func reportCaps(a *fakeAgent, caps ...string) {
	a.send(termproto.Holder{T: termproto.THolder, State: termproto.HolderReady, Persistent: true, Version: "test", Caps: caps})
}

// gatewayCaps 是一台完整的网关（ssh + 凭据库）声明的能力。
func gatewayCaps() []string {
	return []string{"policy", "drain", "lost", termproto.HolderCapSSH, termproto.HolderCapSSHStore}
}

func newGatewayFixture(t *testing.T, opts ...envOption) *gatewayFixture {
	t.Helper()
	logs := &bytes.Buffer{}
	prev := log.Writer()
	log.SetOutput(logs)
	t.Cleanup(func() { log.SetOutput(prev) })
	env := newTestEnv(t, append([]envOption{withExec(), withSSHKey(randHex(32))}, opts...)...)
	f := &gatewayFixture{env: env, adm: env.admin(), key: newHostKey(t), target: gwTargetAddr, logs: logs}
	f.gw, f.sim = f.newGateway(t, "gateway-box")
	return f
}

// newGatewayFixtureWithBackend 与 newGatewayFixture 相同，但目标是一台真实的进程内 SSH 服务器：模型执行会真实地跑远端作业。
func newGatewayFixtureWithBackend(t *testing.T, opts ...envOption) *gatewayFixture {
	t.Helper()
	srv := sshdtest.Start(t, sshdtest.Options{User: "tester", Password: gwPassword})
	logs := &bytes.Buffer{}
	prev := log.Writer()
	log.SetOutput(logs)
	t.Cleanup(func() { log.SetOutput(prev) })
	env := newTestEnv(t, append([]envOption{withExec(), withSSHKey(randHex(32))}, opts...)...)
	f := &gatewayFixture{env: env, adm: env.admin(), key: sshexec.NewHostKey(srv.HostKey()), target: srv.Addr, srv: srv, logs: logs}
	f.gw, f.sim = f.newGateway(t, "gateway-box")
	return f
}

// newGateway 登记一台新的网关主机：先挂接假网关的行为，再上报能力（Hub 在网关就绪时会立刻发 cred_sync，必须有人应答）。
func (f *gatewayFixture) newGateway(t *testing.T, host string) (*fakeAgent, *gatewaySim) {
	t.Helper()
	a := f.env.enrollAgent(testMeta(host), host)
	sim := attachGatewaySim(t, a, f.key)
	if f.srv != nil {
		sim.useBackend(f.srv)
	}
	reportCaps(a, gatewayCaps()...)
	eventually(t, 2*time.Second, "网关能力登记", func() bool { return f.env.hub.gatewayReady(a.code) })
	eventually(t, 3*time.Second, "网关完成首次凭据对账", func() bool { return len(sim.opsOf(termproto.SSHOpCredSync)) >= 1 })
	return a, sim
}

func (f *gatewayFixture) probe(t *testing.T, gateway string) string {
	t.Helper()
	res := f.env.call("POST", "/api/v1/terminal/ssh/probe", map[string]string{"address": f.target, "gateway": gateway}, f.adm...)
	if res.Status != 200 {
		t.Fatalf("探测失败: %d %s", res.Status, res.Raw)
	}
	fp, _ := mapOf(res.Body["probe"])["fingerprint"].(string)
	if fp == "" {
		t.Fatalf("探测结果没有指纹: %s", res.Raw)
	}
	return fp
}

func (f *gatewayFixture) addBody(fp, remark, gateway string) map[string]any {
	return map[string]any{
		"remark": remark, "address": f.target, "user": "tester",
		"auth_type": "password", "password": gwPassword, "fingerprint": fp,
		"gateway": gateway, "default_for_new_models": "allow",
	}
}

func (f *gatewayFixture) add(t *testing.T, remark string) string {
	t.Helper()
	res := f.env.call("POST", "/api/v1/terminal/ssh/hosts", f.addBody(f.probe(t, f.gw.code), remark, f.gw.code), f.adm...)
	if res.Status != 200 {
		t.Fatalf("经网关登记 SSH 主机失败: %d %s", res.Status, res.Raw)
	}
	code, _ := mapOf(res.Body["host"])["machine_code"].(string)
	if code == "" {
		t.Fatalf("响应里没有机器码: %s", res.Raw)
	}
	return code
}

// allowModel 在 Web 上允许模型访问某台主机。
func (f *gatewayFixture) allowModel(t *testing.T, code string) {
	t.Helper()
	if res := f.env.call("POST", "/api/v1/terminal/hosts/"+code+"/model", map[string]any{"allow_model": true}, f.adm...); res.Status != 200 {
		t.Fatalf("允许模型访问失败: %d %s", res.Status, res.Raw)
	}
}

func (f *gatewayFixture) hostView(t *testing.T, code string) map[string]any {
	t.Helper()
	res := f.env.call("GET", "/api/v1/terminal/hosts", nil, "Authorization", humanAuth(testUser))
	hosts, _ := res.Body["hosts"].([]any)
	for _, h := range hosts {
		if m := mapOf(h); m["machine_code"] == code {
			return m
		}
	}
	t.Fatalf("主机 %s 不在列表里: %s", code, res.Raw)
	return nil
}

func (f *gatewayFixture) openSSH(code, target string, extra map[string]any) apiResult {
	body := map[string]any{"ssh_target": target, "rows": 30, "cols": 100}
	for k, v := range extra {
		body[k] = v
	}
	return f.env.call("POST", "/api/v1/terminal/hosts/"+code+"/sessions", body, f.adm...)
}

func (f *gatewayFixture) lastOpen(t *testing.T, a *fakeAgent) termproto.Open {
	t.Helper()
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(a.opens) == 0 {
		t.Fatal("网关没有收到 open 帧")
	}
	return a.opens[len(a.opens)-1]
}

func (f *gatewayFixture) openCount(a *fakeAgent) int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.opens)
}

func (f *gatewayFixture) refuse(a *fakeAgent, code, reason string) {
	a.mu.Lock()
	a.refuseOpen, a.refuseCode = reason, code
	a.mu.Unlock()
}

func (f *gatewayFixture) caller(codes ...string) termexec.Caller {
	cred := &model.MCPCredential{
		ID: 9, Name: "cursor-gw", IsActive: true,
		Permissions:   model.Permissions{Terminal: true},
		TerminalHosts: model.StringList(codes),
	}
	return termexec.Caller{
		CredentialID: 9, CredentialName: "cursor-gw", Credential: cred, Permissions: cred.Permissions,
		TokenString: execToken, Source: "db_credential", RemoteIP: "10.0.0.5", UserAgent: "TestClient/1.0",
	}
}

func (f *gatewayFixture) noSecretsLeaked(t *testing.T, raws ...string) {
	t.Helper()
	for _, s := range append(raws, f.logs.String()) {
		if strings.Contains(s, gwPassword) {
			t.Fatalf("凭据出现在了不该出现的地方（响应 / 日志）: %.200s", s)
		}
	}
}

// ---------- 登记 / 探测 ----------

func TestSSHGatewayAddAndProbeGoThroughGateway(t *testing.T) {
	f := newGatewayFixture(t)
	code := f.add(t, "inner server")

	v := f.hostView(t, code)
	ssh := mapOf(v["ssh"])
	if v["kind"] != "ssh" || ssh["gateway"] != f.gw.code || ssh["gateway_ready"] != true || v["online"] != true {
		t.Fatalf("主机视图不对: %v", v)
	}
	// 凭据在网关上，Hub 没有副本。
	if ssh["cred_on_gateway"] != true || ssh["has_secret"] != false || ssh["cred_missing"] == true || ssh["term_only"] == true {
		t.Fatalf("凭据位置的视图字段不对: %v", ssh)
	}
	rec, _ := f.env.store.GetTerminalHost(context.Background(), code)
	if rec == nil || rec.SSHGateway != f.gw.code || rec.SSHSecret != "" || rec.SSHTermOnly || rec.SSHAuth != "password" {
		t.Fatalf("记录不对（Hub 不应保存任何凭据）: %+v", rec)
	}
	// POSIX 目标可被模型执行：新增主机的默认授权态照请求（allow）。
	if rec.DefaultForNewModels != model.HostDefaultAllow {
		t.Fatalf("默认授权态 = %q", rec.DefaultForNewModels)
	}
	if p, ok := f.sim.credOf(code); !ok || p != gwPassword {
		t.Fatal("网关应在登录成功之后把凭据保存到自己的库里（键是机器码）")
	}

	ops := f.sim.opList()
	if len(ops) != 3 || ops[0].Op != termproto.SSHOpProbe || ops[1].Op != termproto.SSHOpProbe || ops[2].Op != termproto.SSHOpCheck {
		t.Fatalf("登记应经网关探测两次并登录检查一次，实际: %+v", ops)
	}
	for _, p := range ops[:2] {
		if p.Target.Password != "" || p.Target.HostKey != "" || p.Target.Addr != f.target {
			t.Fatalf("探测帧不应带凭据或密钥: %s", p.Target)
		}
	}
	chk := ops[2]
	if !chk.Persist || chk.Target.Stored || chk.Target.ID != code {
		t.Fatalf("登记时的登录检查应带着凭据并要求网关保存（键是机器码）: persist=%v stored=%v id=%q", chk.Persist, chk.Target.Stored, chk.Target.ID)
	}
	if chk.Target.Password != gwPassword || chk.Target.Auth != "password" || chk.Target.HostKey != f.key.Encode() || chk.Target.User != "tester" || chk.Target.Addr != f.target {
		t.Fatalf("登录检查应带着凭据与已固定的主机公钥: %s", chk.Target)
	}
	f.noSecretsLeaked(t)
}

func TestSSHGatewayCapabilityAndOnlineChecks(t *testing.T) {
	f := newGatewayFixture(t)
	call := func(gateway string) apiResult {
		return f.env.call("POST", "/api/v1/terminal/ssh/probe", map[string]string{"address": f.target, "gateway": gateway}, f.adm...)
	}

	if res := call("not-a-code"); res.Status != 400 || res.str("code") != "bad_request" {
		t.Fatalf("机器码格式不对应 400: %d %s", res.Status, res.Raw)
	}
	other, _ := termproto.RandomMachineCode()
	if res := call(other); res.Status != 409 || res.str("code") != codeGatewayInvalid {
		t.Fatalf("不存在的网关: %d %s", res.Status, res.Raw)
	}
	if n := len(f.sim.opList()); n != 0 {
		t.Fatalf("被拒绝的请求不应发给网关: %d", n)
	}

	// 守护进程没有声明 SSH 网关能力：拒绝，且不把帧发出去。
	reportCaps(f.gw, "policy", "drain", "lost")
	eventually(t, 2*time.Second, "能力被撤销", func() bool { return !f.env.hub.gatewayReady(f.gw.code) })
	if res := call(f.gw.code); res.Status != 409 || res.str("code") != codeGatewayDisabled {
		t.Fatalf("没有网关能力: %d %s", res.Status, res.Raw)
	}
	// 有 ssh 能力但凭据库不可用：只做探测可以，登记（要保存凭据）被拒绝。
	reportCaps(f.gw, "policy", "drain", "lost", termproto.HolderCapSSH)
	eventually(t, 2*time.Second, "只剩 ssh 能力", func() bool { return f.env.hub.gatewayReady(f.gw.code) })
	if res := call(f.gw.code); res.Status != 200 {
		t.Fatalf("只探测不需要凭据库: %d %s", res.Status, res.Raw)
	}
	fp := f.probe(t, f.gw.code)
	if res := f.env.call("POST", "/api/v1/terminal/ssh/hosts", f.addBody(fp, "x", f.gw.code), f.adm...); res.Status != 409 || res.str("code") != codeGatewayNoStore {
		t.Fatalf("凭据库不可用时不应登记: %d %s", res.Status, res.Raw)
	}
	// 能力恢复。
	reportCaps(f.gw, gatewayCaps()...)
	eventually(t, 2*time.Second, "能力恢复", func() bool { return f.env.hub.gatewayReady(f.gw.code) })
	if res := call(f.gw.code); res.Status != 200 {
		t.Fatalf("恢复后应可探测: %d %s", res.Status, res.Raw)
	}

	// 网关离线。
	f.gw.close()
	eventually(t, 3*time.Second, "网关离线", func() bool { return f.env.hub.onlineAgent(f.gw.code) == nil })
	if res := call(f.gw.code); res.Status != 409 || res.str("code") != codeGatewayOffline {
		t.Fatalf("网关离线: %d %s", res.Status, res.Raw)
	}
}

func TestSSHGatewayOpTimeoutAndDisconnect(t *testing.T) {
	f := newGatewayFixture(t)
	f.sim.mu.Lock()
	f.sim.mute = true
	f.sim.mu.Unlock()

	// 网关在应答之前断开：请求立刻得到「离线」，而不是干等到超时。
	done := make(chan apiResult, 1)
	go func() {
		done <- f.env.call("POST", "/api/v1/terminal/ssh/probe", map[string]string{"address": f.target, "gateway": f.gw.code}, f.adm...)
	}()
	eventually(t, 3*time.Second, "网关收到操作", func() bool { return len(f.sim.opList()) == 1 })
	f.gw.close()
	select {
	case res := <-done:
		if res.Status != 409 || res.str("code") != codeGatewayOffline {
			t.Fatalf("等待期间网关断开: %d %s", res.Status, res.Raw)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("网关断开后请求没有及时返回")
	}
}

// 经网关接入的主机不需要 SSH 主密钥：凭据在网关自己的存储里，Hub 没有可加密的东西。直连仍然需要它。
func TestSSHGatewayHostsNeedNoSSHMasterKey(t *testing.T) {
	f := newGatewayFixture(t, withSSHKey(""))
	if f.env.hub.ssh.enabled() {
		t.Fatal("测试前提：Hub 没有 SSH 主密钥")
	}
	code := f.add(t, "inner server")
	if res := f.env.call("POST", "/api/v1/terminal/hosts/"+code+"/ssh/test", nil, f.adm...); res.Status != 200 || mapOf(res.Body["result"])["ok"] != true {
		t.Fatalf("没有主密钥也应能经网关复测: %d %s", res.Status, res.Raw)
	}
	if res := f.openSSH(f.gw.code, code, nil); res.Status != 200 {
		t.Fatalf("没有主密钥也应能打开终端: %d %s", res.Status, res.Raw)
	}
	// 直连登记仍被拒绝（凭据由 Hub 密封保存，需要主密钥）。
	direct := f.env.call("POST", "/api/v1/terminal/ssh/hosts", f.addBody(f.probe(t, f.gw.code), "direct", ""), f.adm...)
	if direct.Status != 409 || direct.str("code") != termexec.CodeSSHOff {
		t.Fatalf("直连需要主密钥: %d %s", direct.Status, direct.Raw)
	}
}

// ---------- 只能当终端用（探测不出 POSIX 环境） ----------

func TestSSHViaGatewayNonPosixHostIsTerminalOnly(t *testing.T) {
	f := newGatewayFixture(t)
	f.sim.setNotPosix(true)
	code := f.add(t, "switch")

	rec, _ := f.env.store.GetTerminalHost(context.Background(), code)
	if rec == nil || !rec.SSHTermOnly || rec.DefaultForNewModels != model.HostDefaultDeny {
		t.Fatalf("探测不出 POSIX 环境的主机应标成只能当终端用，且不预先授予模型: %+v", rec)
	}
	if rec.Hostname == "" {
		t.Fatalf("没有环境信息时主机名应退回到地址: %+v", rec)
	}
	if ssh := mapOf(f.hostView(t, code)["ssh"]); ssh["term_only"] != true {
		t.Fatalf("视图应标出 term_only: %v", ssh)
	}

	// 即使管理员允许了模型访问，模型也得到明确的原因。
	f.allowModel(t, code)
	svc := f.env.hub.Exec()
	_, err := svc.Exec(context.Background(), f.caller(code), termexec.ExecRequest{Target: code, Command: "id", Description: "t"})
	wantCode(t, err, termexec.CodeSSHTerminalOnly)
	tr, _ := svc.Targets(context.Background(), f.caller(code), "")
	if tg := tr.(*termexec.TargetsResult).Targets; len(tg) != 1 || tg[0].Usable || tg[0].Blocked != termexec.CodeSSHTerminalOnly {
		t.Fatalf("targets = %+v", tg)
	}
	// 人工终端照常可用。
	if res := f.openSSH(f.gw.code, code, nil); res.Status != 200 {
		t.Fatalf("只能当终端用的主机应能打开终端: %d %s", res.Status, res.Raw)
	}
	// 直接在这台 SSH 主机名下开会话：指引去网关。
	res := f.env.call("POST", "/api/v1/terminal/hosts/"+code+"/sessions", map[string]any{"rows": 24, "cols": 80}, f.adm...)
	if res.Status != 409 || res.str("code") != "ssh_via_gateway" {
		t.Fatalf("应指引去网关: %d %s", res.Status, res.Raw)
	}
}

// Hub 绝不直连经网关接入的主机：连接规格带着 Via（承载网关），没有任何凭据，底层 Dial 也拒绝它。
func TestSSHViaGatewaySpecCarriesNoCredentialAndNeverDialsDirectly(t *testing.T) {
	f := newGatewayFixture(t)
	code := f.add(t, "inner server")
	spec, rec, err := f.env.hub.ssh.specFor(context.Background(), code)
	if err != nil {
		t.Fatal(err)
	}
	if spec.Via != f.gw.code || spec.Cred.Kind != "" || spec.Cred.Password != "" || spec.Addr != f.target || spec.HostKey == nil || rec.SSHSecret != "" {
		t.Fatalf("规格不对: via=%q cred=%q addr=%q", spec.Via, spec.Cred.Kind, spec.Addr)
	}
	if _, derr := sshexec.Dial(context.Background(), spec, time.Second); !errors.Is(derr, sshexec.ErrViaGateway) {
		t.Fatalf("底层 Dial 必须拒绝经网关的规格: %v", derr)
	}
}

// ---------- 打开人工终端 ----------

func TestOpenSSHSessionViaGatewayReferencesStoredCredentialOnly(t *testing.T) {
	f := newGatewayFixture(t)
	code := f.add(t, "inner server")

	res := f.openSSH(f.gw.code, code, nil)
	if res.Status != 200 {
		t.Fatalf("打开 SSH 终端失败: %d %s", res.Status, res.Raw)
	}
	sess := mapOf(res.Body["session"])
	if sess["shell"] != "ssh" || sess["target"] != code || sess["name"] != "inner server" {
		t.Fatalf("会话信息不对（应标注 ssh、指回目标、默认用备注命名）: %v", sess)
	}
	open := f.lastOpen(t, f.gw)
	if open.SSH == nil {
		t.Fatal("open 帧里没有 SSH 目标")
	}
	tg := open.SSH
	if tg.ID != code || tg.Addr != f.target || tg.User != "tester" || tg.HostKey != f.key.Encode() || !tg.Stored {
		t.Fatalf("交给网关的目标不对: %s", tg)
	}
	if tg.Password != "" || tg.PrivateKey != "" || tg.Passphrase != "" || tg.Auth != "" {
		t.Fatalf("open 帧里不得带任何凭据: %+v", *tg)
	}
	if open.Cwd != "" {
		t.Fatalf("SSH 会话不应带本地启动目录: %q", open.Cwd)
	}

	// 响应、会话列表、审计、日志里都没有凭据；审计记下目标的机器码。
	list := f.env.call("GET", "/api/v1/terminal/hosts/"+f.gw.code+"/sessions", nil, "Authorization", humanAuth(testUser))
	audit := f.env.call("GET", "/api/v1/terminal/audit?limit=200", nil, "Authorization", humanAuth(testUser))
	if !strings.Contains(audit.Raw, "ssh:"+code) {
		t.Fatalf("审计应记下目标主机: %s", audit.Raw)
	}
	f.noSecretsLeaked(t, res.Raw, list.Raw, audit.Raw)
	if !strings.Contains(list.Raw, code) {
		t.Fatalf("网关主机的会话列表应能指回目标: %s", list.Raw)
	}
	if ok := mapOf(f.hostView(t, code)["ssh"])["ok_at"]; ok == nil {
		t.Fatal("成功打开后应刷新最近成功时间")
	}
}

func TestOpenSSHSessionFailureMapping(t *testing.T) {
	cases := []struct {
		name        string
		code        string
		status      int
		wantCode    string
		hostError   bool // 是否应记成目标主机的连接失败
		mismatched  bool
		credMissing bool
	}{
		{"主机密钥不一致", termproto.SSHCodeHostKey, 409, "ssh_host_key_mismatch", true, true, false},
		{"认证失败", termproto.SSHCodeAuth, 422, "ssh_auth_failed", true, false, false},
		{"连不上目标", termproto.SSHCodeUnreachable, 502, "ssh_unreachable", true, false, false},
		{"目标不在允许名单", termproto.SSHCodeDenied, 403, codeTargetDenied, true, false, false},
		{"网关没有开启", termproto.SSHCodeDisabled, 409, codeGatewayDisabled, false, false, false},
		{"网关繁忙", termproto.SSHCodeBusy, 429, "ssh_busy", true, false, false},
		{"网关上没有这台主机的凭据", termproto.SSHCodeNoCred, 409, termexec.CodeSSHCredMissing, false, false, true},
		{"网关的凭据库不可用", termproto.SSHCodeStore, 502, codeGatewayStore, false, false, false},
		{"旧版本守护进程（没有失败码）", "", 502, "agent_refused", true, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newGatewayFixture(t)
			code := f.add(t, "inner server")
			f.refuse(f.gw, tc.code, "dial failed: sanitized reason")
			before := f.openCount(f.gw)

			res := f.openSSH(f.gw.code, code, nil)
			if res.Status != tc.status || res.str("code") != tc.wantCode {
				t.Fatalf("期望 %d %s，得到 %d %s", tc.status, tc.wantCode, res.Status, res.Raw)
			}
			f.noSecretsLeaked(t, res.Raw)

			ssh := mapOf(f.hostView(t, code)["ssh"])
			if got := ssh["error"] != nil && ssh["error"] != ""; got != tc.hostError {
				t.Fatalf("主机连接错误状态 = %v，期望 %v（%v）", got, tc.hostError, ssh)
			}
			if got := ssh["key_state"] == "mismatch"; got != tc.mismatched {
				t.Fatalf("key_state = %v，期望 mismatch=%v", ssh["key_state"], tc.mismatched)
			}
			if got := ssh["cred_missing"] == true; got != tc.credMissing {
				t.Fatalf("cred_missing = %v，期望 %v（%v）", got, tc.credMissing, ssh)
			}
			if tc.mismatched {
				// 置位之后在闸门处直接拒绝，不再向网关发 open。
				f.refuse(f.gw, "", "")
				again := f.openSSH(f.gw.code, code, nil)
				if again.Status != 409 || again.str("code") != "ssh_host_key_mismatch" {
					t.Fatalf("mismatch 之后应直接拒绝: %d %s", again.Status, again.Raw)
				}
				if f.openCount(f.gw) != before+1 {
					t.Fatal("被拒绝的请求不应再发出 open 帧")
				}
			}
		})
	}
}

func TestOpenSSHSessionGuards(t *testing.T) {
	f := newGatewayFixture(t)
	code := f.add(t, "inner server")

	if res := f.openSSH(f.gw.code, "nope", nil); res.Status != 400 || res.str("code") != "bad_request" {
		t.Fatalf("非法机器码: %d %s", res.Status, res.Raw)
	}
	other, _ := termproto.RandomMachineCode()
	if res := f.openSSH(f.gw.code, other, nil); res.Status != 404 || res.str("code") != "host_not_found" {
		t.Fatalf("不存在的 SSH 主机: %d %s", res.Status, res.Raw)
	}
	// 目标不是 SSH 主机（就是网关自己）。
	if res := f.openSSH(f.gw.code, f.gw.code, nil); res.Status != 404 {
		t.Fatalf("目标不是 SSH 主机: %d %s", res.Status, res.Raw)
	}
	// 一台经另一网关接入的主机，不能借这台网关打开。
	gw2, _ := f.newGateway(t, "gateway-two")
	if res := f.openSSH(gw2.code, code, nil); res.Status != 409 || res.str("code") != codeNotViaGateway {
		t.Fatalf("借错网关: %d %s", res.Status, res.Raw)
	}
	if f.openCount(gw2) != 0 || f.openCount(f.gw) != 0 {
		t.Fatal("被拒绝的请求不应发出 open 帧")
	}
	// 非法的空闲时间照常被拒绝。
	if res := f.openSSH(f.gw.code, code, map[string]any{"idle_seconds": -5}); res.Status != 400 {
		t.Fatalf("非法 idle: %d %s", res.Status, res.Raw)
	}
	// 网关离线。
	f.gw.close()
	eventually(t, 3*time.Second, "网关离线", func() bool { return f.env.hub.onlineAgent(f.gw.code) == nil })
	if res := f.openSSH(f.gw.code, code, nil); res.Status != 409 || res.str("code") != codeGatewayOffline {
		t.Fatalf("网关离线: %d %s", res.Status, res.Raw)
	}
	if v := f.hostView(t, code); v["online"] != false || mapOf(v["ssh"])["gateway_ready"] == true {
		t.Fatalf("网关离线时主机视图应显示不可用: %v", v)
	}
}

// ---------- 测试 / 重新确认指纹 / 改接 ----------

func TestSSHTestAndRekeyViaGateway(t *testing.T) {
	f := newGatewayFixture(t)
	code := f.add(t, "inner server")

	test := func() map[string]any {
		res := f.env.call("POST", "/api/v1/terminal/hosts/"+code+"/ssh/test", nil, f.adm...)
		if res.Status != 200 {
			t.Fatalf("测试连接: %d %s", res.Status, res.Raw)
		}
		return mapOf(res.Body["result"])
	}
	if r := test(); r["ok"] != true || r["hostname"] != "inner-box" {
		t.Fatalf("测试应经网关成功: %v", r)
	}
	// 复测用网关里保存的凭据：帧里只有 stored 引用。
	checks := f.sim.opsOf(termproto.SSHOpCheck)
	last := checks[len(checks)-1]
	if !last.Target.Stored || last.Persist || last.Target.Password != "" || last.Target.ID != code {
		t.Fatalf("复测应引用网关里保存的凭据: %+v", last)
	}

	// 网关离线：测试给出明确的原因，但不把它记成目标主机的连接失败。
	f.gw.close()
	eventually(t, 3*time.Second, "网关离线", func() bool { return f.env.hub.onlineAgent(f.gw.code) == nil })
	if r := test(); r["ok"] != false || r["code"] != codeGatewayOffline {
		t.Fatalf("网关离线: %v", r)
	}
	if e := mapOf(f.hostView(t, code)["ssh"])["error"]; e != nil && e != "" {
		t.Fatalf("网关离线不应记成主机的连接错误: %v", e)
	}
}

func TestSSHHostKeyChangeThenRekeyViaGateway(t *testing.T) {
	f := newGatewayFixture(t)
	code := f.add(t, "inner server")

	// 远端服务器换了主机密钥：网关带着已固定的公钥登录时发现不一致，Hub 置 mismatch。
	newKey := newHostKey(t)
	f.sim.setKey(newKey)
	res := f.env.call("POST", "/api/v1/terminal/hosts/"+code+"/ssh/test", nil, f.adm...)
	if r := mapOf(res.Body["result"]); r["ok"] != false || r["code"] != "ssh_host_key_mismatch" {
		t.Fatalf("应发现密钥不一致: %s", res.Raw)
	}
	if ks := mapOf(f.hostView(t, code)["ssh"])["key_state"]; ks != "mismatch" {
		t.Fatalf("key_state = %v", ks)
	}
	if open := f.openSSH(f.gw.code, code, nil); open.Status != 409 || open.str("code") != "ssh_host_key_mismatch" {
		t.Fatalf("mismatch 之后不应能打开终端: %d %s", open.Status, open.Raw)
	}

	// 管理员核对新指纹后重新固定：经网关重新探测，指纹必须与核对的一致。
	probe := f.env.call("POST", "/api/v1/terminal/hosts/"+code+"/ssh/probe", nil, f.adm...)
	fp, _ := mapOf(probe.Body["probe"])["fingerprint"].(string)
	if probe.Status != 200 || fp != newKey.Fingerprint {
		t.Fatalf("重新探测应取回新指纹: %d %s", probe.Status, probe.Raw)
	}
	if bad := f.env.call("POST", "/api/v1/terminal/hosts/"+code+"/ssh/rekey", map[string]string{"fingerprint": f.key.Fingerprint}, f.adm...); bad.Status != 409 {
		t.Fatalf("核对的指纹与探测不一致应拒绝: %d %s", bad.Status, bad.Raw)
	}
	if ok := f.env.call("POST", "/api/v1/terminal/hosts/"+code+"/ssh/rekey", map[string]string{"fingerprint": fp}, f.adm...); ok.Status != 200 {
		t.Fatalf("重新固定: %d %s", ok.Status, ok.Raw)
	}
	if ks := mapOf(f.hostView(t, code)["ssh"])["key_state"]; ks != nil && ks != "" {
		t.Fatalf("重新固定后应清除 mismatch: %v", ks)
	}
	if open := f.openSSH(f.gw.code, code, nil); open.Status != 200 {
		t.Fatalf("重新固定后应能打开终端: %d %s", open.Status, open.Raw)
	}
	f.noSecretsLeaked(t)
}

// 凭据只保存在各自的位置，换接入路径时不能凭空搬运：必须同时重新填写凭据，新路径验证通过才保存，离开的路径上清掉旧凭据。
func TestSSHSwitchGatewayRequiresCredentialsAndMovesThem(t *testing.T) {
	f := newGatewayFixture(t)
	code := f.add(t, "inner server")
	gw2, sim2 := f.newGateway(t, "gateway-two")

	patch := func(body map[string]any) apiResult {
		return f.env.call("POST", "/api/v1/terminal/hosts/"+code+"/ssh/connection", body, f.adm...)
	}
	cred := func(m map[string]any) map[string]any {
		m["auth_type"], m["password"] = "password", gwPassword
		return m
	}
	// 没有重新填写凭据：拒绝，哪边都不动。
	if res := patch(map[string]any{"gateway": gw2.code}); res.Status != 400 || res.str("code") != "bad_request" || !strings.Contains(res.Raw, "重新填写") {
		t.Fatalf("改接必须同时重新填写凭据: %d %s", res.Status, res.Raw)
	}
	if len(sim2.opList()) != 0 {
		t.Fatal("被拒绝的改接不应惊动新网关")
	}
	// 新网关不可用：保持原样。
	other, _ := termproto.RandomMachineCode()
	if res := patch(cred(map[string]any{"gateway": other})); res.Status != 409 || res.str("code") != codeGatewayInvalid {
		t.Fatalf("无效网关: %d %s", res.Status, res.Raw)
	}
	before := len(f.sim.opList())

	// 新网关登录失败时不保存，也不碰旧网关上的凭据。
	sim2.failOp(termproto.SSHOpCheck, &termproto.SSHResult{Code: termproto.SSHCodeUnreachable, Msg: "no route"})
	if res := patch(cred(map[string]any{"gateway": gw2.code})); res.Status != 502 || res.str("code") != "ssh_unreachable" {
		t.Fatalf("改接前的真实连接失败应拒绝: %d %s", res.Status, res.Raw)
	}
	if ssh := mapOf(f.hostView(t, code)["ssh"]); ssh["gateway"] != f.gw.code {
		t.Fatalf("失败的改接不应生效: %v", ssh)
	}
	if _, ok := f.sim.credOf(code); !ok || len(f.sim.opList()) != before {
		t.Fatal("失败的改接不应动旧网关")
	}
	sim2.failOp(termproto.SSHOpCheck, nil)

	// 改接：必须经新网关真实登录并保存凭据，通过才改记录；随后旧网关上的凭据被清掉。
	if res := patch(cred(map[string]any{"gateway": gw2.code})); res.Status != 200 {
		t.Fatalf("改接: %d %s", res.Status, res.Raw)
	}
	var persisted bool
	for _, c := range sim2.opsOf(termproto.SSHOpCheck) {
		if c.Persist && c.Target.ID == code && c.Target.Password == gwPassword {
			persisted = true
		}
	}
	if !persisted {
		t.Fatal("新网关应收到带凭据并要求保存的登录检查")
	}
	if p, ok := sim2.credOf(code); !ok || p != gwPassword {
		t.Fatal("新网关应保存了凭据")
	}
	eventually(t, 3*time.Second, "旧网关上的凭据被清掉", func() bool { _, ok := f.sim.credOf(code); return !ok })
	if len(f.sim.opsOf(termproto.SSHOpCredDel)) != 1 {
		t.Fatalf("旧网关应收到一次 cred_del: %+v", f.sim.opList())
	}
	if ssh := mapOf(f.hostView(t, code)["ssh"]); ssh["gateway"] != gw2.code || ssh["cred_missing"] == true {
		t.Fatalf("改接后网关应变更: %v", ssh)
	}
	rec, _ := f.env.store.GetTerminalHost(context.Background(), code)
	if rec == nil || rec.SSHSecret != "" {
		t.Fatalf("Hub 不应保存凭据: %+v", rec)
	}
	f.noSecretsLeaked(t)
}

// 凭据缺失（网关重装 / 凭据库被清空）：复测给出明确的原因并标出「凭据缺失」，不算目标主机故障；重新填写后恢复。
func TestSSHGatewayCredentialMissingAndRestore(t *testing.T) {
	f := newGatewayFixture(t)
	code := f.add(t, "inner server")
	f.sim.dropCred(code)

	res := f.env.call("POST", "/api/v1/terminal/hosts/"+code+"/ssh/test", nil, f.adm...)
	r := mapOf(res.Body["result"])
	if res.Status != 200 || r["ok"] != false || r["code"] != termexec.CodeSSHCredMissing {
		t.Fatalf("复测应报告凭据缺失: %d %s", res.Status, res.Raw)
	}
	ssh := mapOf(f.hostView(t, code)["ssh"])
	if ssh["cred_missing"] != true || (ssh["error"] != nil && ssh["error"] != "") {
		t.Fatalf("应标出凭据缺失且不记成目标故障: %v", ssh)
	}

	// 只改地址（沿用网关里的凭据）此时失败，并提示重新填写。
	bad := f.env.call("POST", "/api/v1/terminal/hosts/"+code+"/ssh/connection", map[string]any{"address": "10.255.255.2:22"}, f.adm...)
	if bad.Status != 409 || bad.str("code") != termexec.CodeSSHCredMissing {
		t.Fatalf("没有凭据时不能只改地址: %d %s", bad.Status, bad.Raw)
	}

	// 重新填写凭据：网关登录成功后保存，缺失标记消失。
	fix := f.env.call("POST", "/api/v1/terminal/hosts/"+code+"/ssh/connection", map[string]any{"auth_type": "password", "password": gwPassword}, f.adm...)
	if fix.Status != 200 {
		t.Fatalf("重新填写凭据: %d %s", fix.Status, fix.Raw)
	}
	if _, ok := f.sim.credOf(code); !ok {
		t.Fatal("网关应重新保存凭据")
	}
	if ssh := mapOf(f.hostView(t, code)["ssh"]); ssh["cred_missing"] == true {
		t.Fatalf("缺失标记应消失: %v", ssh)
	}
	f.noSecretsLeaked(t)
}

// ---------- 删除网关 ----------

func TestDeleteGatewayInUseIsRefused(t *testing.T) {
	f := newGatewayFixture(t)
	code := f.add(t, "inner server")

	res := f.env.call("DELETE", "/api/v1/terminal/hosts/"+f.gw.code, nil, f.adm...)
	if res.Status != 409 || res.str("code") != "gateway_in_use" {
		t.Fatalf("仍有 SSH 主机经它接入时应拒绝删除: %d %s", res.Status, res.Raw)
	}
	if f.env.hub.onlineAgent(f.gw.code) == nil {
		t.Fatal("被拒绝的删除不应踢掉网关")
	}
	if del := f.env.call("DELETE", "/api/v1/terminal/hosts/"+code, nil, f.adm...); del.Status != 200 {
		t.Fatalf("删除 SSH 主机: %d %s", del.Status, del.Raw)
	}
	// 删除 SSH 主机时通知网关清掉它的凭据（尽力而为）。
	eventually(t, 3*time.Second, "网关上的凭据被清掉", func() bool { _, ok := f.sim.credOf(code); return !ok })
	if res := f.env.call("DELETE", "/api/v1/terminal/hosts/"+f.gw.code, nil, f.adm...); res.Status != 200 {
		t.Fatalf("没有依赖后应可删除: %d %s", res.Status, res.Raw)
	}
}

// ---------- 停用 / 删除 / 改接：终止目标在网关上的会话 ----------

// gwSessions 统计假网关上属于某台 SSH 主机（target 为空时统计本机 shell）的会话数。
func gwSessions(a *fakeAgent, target string) int {
	a.mu.Lock()
	defer a.mu.Unlock()
	n := 0
	for _, s := range a.sessions {
		if s.info.Target == target {
			n++
		}
	}
	return n
}

func TestEndingATargetEndsItsSessionsOnTheGateway(t *testing.T) {
	f := newGatewayFixture(t)
	a, b := f.add(t, "box-a"), f.add(t, "box-b")
	open := func(code string) {
		t.Helper()
		if res := f.openSSH(f.gw.code, code, nil); res.Status != 200 {
			t.Fatalf("打开 %s 的终端失败: %d %s", code, res.Status, res.Raw)
		}
	}
	open(a)
	open(a)
	open(b)
	// 网关自己的本机 shell：任何情况下都不该被牵连。
	if res := f.env.call("POST", "/api/v1/terminal/hosts/"+f.gw.code+"/sessions", map[string]any{"rows": 30, "cols": 100}, f.adm...); res.Status != 200 {
		t.Fatalf("网关本机会话: %d %s", res.Status, res.Raw)
	}
	eventually(t, 2*time.Second, "会话登记", func() bool { return gwSessions(f.gw, a) == 2 && gwSessions(f.gw, b) == 1 && gwSessions(f.gw, "") == 1 })

	// 停用 a：它的会话被终止，b 与本机 shell 不受影响；a 的凭据仍留在网关上（重新启用后还要用）。
	if res := f.env.call("POST", "/api/v1/terminal/hosts/"+a+"/disable", nil, f.adm...); res.Status != 200 {
		t.Fatalf("停用: %d %s", res.Status, res.Raw)
	}
	eventually(t, 2*time.Second, "停用后 a 的会话被终止", func() bool { return gwSessions(f.gw, a) == 0 })
	if gwSessions(f.gw, b) != 1 || gwSessions(f.gw, "") != 1 {
		t.Fatalf("停用 a 不应牵连别的会话: b=%d local=%d", gwSessions(f.gw, b), gwSessions(f.gw, ""))
	}
	if _, ok := f.sim.credOf(a); !ok {
		t.Fatal("停用不应删除网关上的凭据")
	}

	// 只轮换凭据（这里重新提交同一个口令）不改变目标的身份：已经认证的会话不被打断。
	if res := f.env.call("POST", "/api/v1/terminal/hosts/"+b+"/ssh/connection", map[string]any{"auth_type": "password", "password": gwPassword}, f.adm...); res.Status != 200 {
		t.Fatalf("更新凭据: %d %s", res.Status, res.Raw)
	}
	time.Sleep(150 * time.Millisecond)
	if gwSessions(f.gw, b) != 1 {
		t.Fatal("只换凭据不应终止会话")
	}

	// 改了地址：旧地址上的会话被终止（沿用网关里保存的凭据，不需要再次提交）。
	if res := f.env.call("POST", "/api/v1/terminal/hosts/"+b+"/ssh/connection", map[string]any{"address": "10.255.255.2:22"}, f.adm...); res.Status != 200 {
		t.Fatalf("改地址: %d %s", res.Status, res.Raw)
	}
	eventually(t, 2*time.Second, "改地址后 b 的会话被终止", func() bool { return gwSessions(f.gw, b) == 0 })

	// 删除：先开一个，再删。
	open(b)
	eventually(t, 2*time.Second, "新会话登记", func() bool { return gwSessions(f.gw, b) == 1 })
	if res := f.env.call("DELETE", "/api/v1/terminal/hosts/"+b, nil, f.adm...); res.Status != 200 {
		t.Fatalf("删除: %d %s", res.Status, res.Raw)
	}
	eventually(t, 2*time.Second, "删除后 b 的会话被终止", func() bool { return gwSessions(f.gw, b) == 0 })
	if gwSessions(f.gw, "") != 1 {
		t.Fatal("网关自己的会话不应被终止")
	}
}
