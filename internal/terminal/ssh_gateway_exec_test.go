//go:build !windows

package terminal

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/teacat99/RelayMesh/v2/internal/model"
	"github.com/teacat99/RelayMesh/v2/internal/termexec"
	"github.com/teacat99/RelayMesh/v2/internal/termproto"
)

// 这组测试覆盖「模型经网关在内网主机上执行命令」：Hub 驱动远端作业的每一步（启动 / 轮询 / 取消 / 读取）都被包成一次
// 经网关代理的 SSH exec，登录凭据始终留在网关自己的存储里，帧里只有 stored 引用。
// 目标是一台真实的进程内 SSH 服务器（和直连测试同一套），所以远端作业脚本是真实地跑起来的。

// reportCapsDeny 让假网关上报「本机一票否决了模型执行」（--deny-model-exec）。
func reportCapsDeny(a *fakeAgent, caps ...string) {
	a.send(termproto.Holder{T: termproto.THolder, State: termproto.HolderReady, Persistent: true, Version: "test", Caps: caps, DenyExec: true})
}

type gwExecFixture struct {
	*gatewayFixture
	code string // 已允许模型访问的 SSH 主机（经网关接入）
	svc  *ExecService
}

func newGatewayExecFixture(t *testing.T, opts ...envOption) *gwExecFixture {
	t.Helper()
	f := newGatewayFixtureWithBackend(t, opts...)
	g := &gwExecFixture{gatewayFixture: f, svc: f.env.hub.Exec()}
	g.code = f.add(t, "lab box")
	f.allowModel(t, g.code)
	return g
}

func (g *gwExecFixture) callerFor(codes ...string) termexec.Caller {
	if len(codes) == 0 {
		codes = []string{g.code}
	}
	return g.gatewayFixture.caller(codes...)
}

func (g *gwExecFixture) exec(cmd string, mut func(*termexec.ExecRequest)) (*termexec.RunResult, error) {
	req := termexec.ExecRequest{Target: g.code, Command: cmd, WaitSet: true, WaitSeconds: 25, Description: "gateway exec test"}
	if mut != nil {
		mut(&req)
	}
	res, err := g.svc.Exec(context.Background(), g.callerFor(), req)
	if err != nil {
		return nil, err
	}
	return res.(*termexec.RunResult), nil
}

func (g *gwExecFixture) mustExec(t *testing.T, cmd string, mut func(*termexec.ExecRequest)) *termexec.RunResult {
	t.Helper()
	res, err := g.exec(cmd, mut)
	if err != nil {
		t.Fatalf("exec %q: %v", cmd, err)
	}
	return res
}

func (g *gwExecFixture) output(t *testing.T, runID, stream string, off int64, limit int) *termexec.OutputPage {
	t.Helper()
	out, err := g.svc.Run(context.Background(), g.callerFor(), termexec.RunRequest{Action: "output", RunID: runID, Stream: stream, Offset: off, Limit: limit})
	if err != nil {
		t.Fatalf("读取输出: %v", err)
	}
	return out.(*termexec.OutputPage)
}

func (g *gwExecFixture) dbRun(t *testing.T, id string) *model.TerminalRun {
	t.Helper()
	rec, err := g.env.store.GetTerminalRun(context.Background(), id)
	if err != nil || rec == nil {
		t.Fatalf("读取运行记录 %s: %v %v", id, rec, err)
	}
	return rec
}

func (g *gwExecFixture) targetInfo(t *testing.T) termexec.Target {
	t.Helper()
	tr, err := g.svc.Targets(context.Background(), g.callerFor(), "")
	if err != nil {
		t.Fatal(err)
	}
	for _, tg := range tr.(*termexec.TargetsResult).Targets {
		if tg.MachineCode == g.code {
			return tg
		}
	}
	t.Fatalf("targets 里没有 %s", g.code)
	return termexec.Target{}
}

func seqText(n int) string {
	var b strings.Builder
	for i := 1; i <= n; i++ {
		b.WriteString(strconv.Itoa(i))
		b.WriteByte('\n')
	}
	return b.String()
}

// reconnect 让一台网关掉线后以同一身份重连。网关自己的持久状态（凭据库与后端）由新的假网关继承；
// report 为 true 时立刻上报能力（触发 Hub 的凭据对账）。
func (f *gatewayFixture) reconnect(t *testing.T, a *fakeAgent, sim *gatewaySim, host string, report bool) (*fakeAgent, *gatewaySim) {
	t.Helper()
	a.close()
	eventually(t, 3*time.Second, "网关离线", func() bool { return f.env.hub.onlineAgent(a.code) == nil })
	na := f.env.connectEnabled(a.priv, testMeta(host))
	ns := attachGatewaySim(t, na, f.key)
	sim.mu.Lock()
	for k, v := range sim.creds {
		ns.creds[k] = v
	}
	srv := sim.srv
	sim.mu.Unlock()
	if srv != nil {
		ns.useBackend(srv)
	}
	if report {
		reportCaps(na, gatewayCaps()...)
	}
	if a == f.gw {
		f.gw, f.sim = na, ns
	}
	return na, ns
}

// ---------- 执行 ----------

func TestSSHGatewayExecHappyPath(t *testing.T) {
	g := newGatewayExecFixture(t, withLogCapture())
	res := g.mustExec(t, "echo hello; echo oops >&2; pwd", nil)
	if res.State != model.RunSucceeded || res.ExitCode == nil || *res.ExitCode != 0 {
		t.Fatalf("结果 = %+v", res)
	}
	if res.Stdout == nil || !strings.HasPrefix(res.Stdout.Text, "hello\n") || res.Stdout.Truncated {
		t.Fatalf("stdout = %+v", res.Stdout)
	}
	if res.Stderr == nil || res.Stderr.Text != "oops\n" {
		t.Fatalf("stderr = %+v", res.Stderr)
	}
	if lines := strings.Split(res.Stdout.Text, "\n"); len(lines) < 2 || lines[1] != g.srv.Home {
		t.Fatalf("默认工作目录应恰好是家目录 %s，输出 = %q", g.srv.Home, res.Stdout.Text)
	}

	rec := g.dbRun(t, res.RunID)
	if rec.Backend != model.HostKindSSH || rec.State != model.RunSucceeded || rec.Target != g.code || rec.StartedAt == nil || rec.EndedAt == nil {
		t.Fatalf("记录 = %+v", rec)
	}

	// 经网关的每一步都是一次 exec：只带 stored 引用，没有任何凭据；命令文本只经 stdin，绝不出现在命令行上。
	execs := g.sim.opsOf(termproto.SSHOpExec)
	if len(execs) < 3 {
		t.Fatalf("启动 + 轮询 + 读取至少要 3 次 exec，实际 %d", len(execs))
	}
	for _, op := range execs {
		tg := op.Target
		if !tg.Stored || tg.ID != g.code || tg.Addr != g.target || tg.User != "tester" || tg.HostKey != g.key.Encode() {
			t.Fatalf("exec 帧的目标不对: %s", tg)
		}
		if tg.Password != "" || tg.PrivateKey != "" || tg.Passphrase != "" || tg.Auth != "" {
			t.Fatalf("exec 帧不得带凭据: %+v", tg)
		}
		if strings.Contains(op.Cmd, "echo hello") || op.MaxOut <= 0 || op.MaxOut > termproto.MaxSSHExecOut {
			t.Fatalf("exec 帧不对: maxOut=%d cmd=%.80q", op.MaxOut, op.Cmd)
		}
	}
	for _, c := range g.srv.Commands() {
		if strings.Contains(c, "hello") || strings.Contains(c, "oops") {
			t.Fatalf("命令文本泄漏到了 SSH 命令行: %q", c)
		}
	}
	if g.svc.liveRun(res.RunID) != nil {
		t.Fatal("终态后不应留在活动索引里")
	}
	// 主机视图：凭据在网关上，Hub 里没有副本。
	if p, ok := g.sim.credOf(g.code); !ok || p != gwPassword {
		t.Fatal("网关应一直保存着凭据")
	}
	tg := g.targetInfo(t)
	if tg.Backend != "ssh" || !tg.Usable || !tg.Online || tg.Blocked != "" {
		t.Fatalf("targets = %+v", tg)
	}
	g.noSecretsLeaked(t)
}

func TestSSHGatewayExecExitCodeAndTimeout(t *testing.T) {
	g := newGatewayExecFixture(t)
	res := g.mustExec(t, "echo before; exit 7", nil)
	if res.State != model.RunFailed || res.ExitCode == nil || *res.ExitCode != 7 || res.Stdout == nil || res.Stdout.Text != "before\n" {
		t.Fatalf("非零退出码: %+v", res)
	}
	start := time.Now()
	res = g.mustExec(t, "sleep 30", func(r *termexec.ExecRequest) { r.TimeoutSeconds = 2 })
	if res.State != model.RunTimedOut {
		t.Fatalf("超时应判为 timed_out: %+v", res)
	}
	if time.Since(start) > 15*time.Second {
		t.Fatalf("超时应及时生效: %v", time.Since(start))
	}
}

// 大输出：完整输出留在远端，Hub 经网关分块读取；每一帧都必须放得进 64 KiB 的控制帧。
func TestSSHGatewayExecLargeOutputRoundTripsInChunks(t *testing.T) {
	g := newGatewayExecFixture(t, withLogCapture())
	const lines = 30000
	want := seqText(lines)
	res := g.mustExec(t, fmt.Sprintf("seq 1 %d", lines), nil)
	if res.State != model.RunSucceeded || res.Stdout == nil {
		t.Fatalf("结果 = %+v", res)
	}
	if res.Stdout.Bytes != int64(len(want)) || !res.Stdout.Truncated || res.Stdout.NextOffset <= 0 || int64(len(res.Stdout.Text)) != res.Stdout.NextOffset {
		t.Fatalf("stdout = bytes %d truncated=%v next=%d textlen=%d", res.Stdout.Bytes, res.Stdout.Truncated, res.Stdout.NextOffset, len(res.Stdout.Text))
	}
	if res.Stdout.Text != want[:res.Stdout.NextOffset] {
		t.Fatal("内联的开头与实际输出不一致")
	}

	// 一次读完剩下的全部：远大于单帧上限（16 KiB 块），由 Hub 分块循环读取，内容不能有缺口或重复。
	page := g.output(t, res.RunID, "stdout", res.Stdout.NextOffset, 256<<10)
	if !page.EOF || page.TotalBytes != int64(len(want)) || page.Text != want[res.Stdout.NextOffset:] {
		t.Fatalf("剩余输出不一致: eof=%v total=%d textlen=%d wantlen=%d", page.EOF, page.TotalBytes, len(page.Text), len(want)-int(res.Stdout.NextOffset))
	}
	// 从与块边界不对齐的位置开始读一段。
	const off, limit = 12345, 100000
	mid := g.output(t, res.RunID, "stdout", off, limit)
	if mid.Text != want[off:off+limit] || mid.EOF {
		t.Fatalf("中段读取不一致: eof=%v len=%d", mid.EOF, len(mid.Text))
	}

	maxOp, maxRes := g.sim.maxFrames()
	if maxOp >= termproto.MaxControlFrame || maxRes >= termproto.MaxControlFrame {
		t.Fatalf("帧超出控制帧上限 %d：ssh_op 最大 %d，ssh_result 最大 %d", termproto.MaxControlFrame, maxOp, maxRes)
	}
	if maxRes < 20<<10 {
		t.Fatalf("测试没有真正走到分块读取（最大应答只有 %d 字节）", maxRes)
	}
	g.noSecretsLeaked(t)
}

// 命令文本达到协议上限（24 KiB）时，启动脚本连同 stdin 的 base64 膨胀也必须放得进控制帧。
func TestSSHGatewayExecAcceptsMaximumSizeCommand(t *testing.T) {
	g := newGatewayExecFixture(t)
	const prefix = "echo big-ok; : "
	cmd := prefix + strings.Repeat("a", termproto.MaxRunCommandBytes-len(prefix))
	if len(cmd) != termproto.MaxRunCommandBytes {
		t.Fatalf("测试前提：命令长度 = %d", len(cmd))
	}
	res := g.mustExec(t, cmd, nil)
	if res.State != model.RunSucceeded || res.Stdout == nil || res.Stdout.Text != "big-ok\n" {
		t.Fatalf("结果 = %+v", res)
	}
	maxOp, _ := g.sim.maxFrames()
	if maxOp >= termproto.MaxControlFrame || maxOp < 24<<10 {
		t.Fatalf("ssh_op 最大帧 = %d（上限 %d）", maxOp, termproto.MaxControlFrame)
	}
	// 非 ASCII 的最大命令（每个汉字 3 字节）同样放得进。
	zh := "echo zh-ok; : " + strings.Repeat("中", (termproto.MaxRunCommandBytes-len("echo zh-ok; : "))/3)
	if res := g.mustExec(t, zh, nil); res.State != model.RunSucceeded || res.Stdout == nil || res.Stdout.Text != "zh-ok\n" {
		t.Fatalf("非 ASCII 命令: %+v", res)
	}
}

// 多条命令并发：应答按请求号各归各的，互不串扰。
func TestSSHGatewayExecParallelRunsDoNotCrossTalk(t *testing.T) {
	g := newGatewayExecFixture(t)
	const n = 6
	results := make([]*termexec.RunResult, n)
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i], errs[i] = g.exec(fmt.Sprintf("echo run-%d; sleep 0.3; echo done-%d", i, i), nil)
		}(i)
	}
	wg.Wait()
	for i := 0; i < n; i++ {
		if errs[i] != nil {
			t.Fatalf("第 %d 条: %v", i, errs[i])
		}
		r := results[i]
		if r.State != model.RunSucceeded || r.Stdout == nil || r.Stdout.Text != fmt.Sprintf("run-%d\ndone-%d\n", i, i) {
			t.Fatalf("第 %d 条串扰或失败: %+v", i, r)
		}
	}
}

// 管理类操作与执行代理各有各的在途名额：一边占满不会拖垮另一边。
func TestSSHGatewayMgmtAndExecPendingSlotsAreIndependent(t *testing.T) {
	f := newGatewayFixture(t)
	f.sim.mu.Lock()
	f.sim.mute = true
	f.sim.mu.Unlock()
	a := f.env.hub.onlineAgent(f.gw.code)
	if a == nil {
		t.Fatal("网关不在线")
	}
	code, _ := termproto.RandomMachineCode()
	target := termproto.SSHTarget{ID: code, Addr: f.target, User: "u", HostKey: f.key.Encode(), Stored: true}
	probe := termproto.SSHOp{Op: termproto.SSHOpProbe, Target: termproto.SSHTarget{Addr: f.target}}

	var wg sync.WaitGroup
	t.Cleanup(wg.Wait)

	// 先占满管理类名额：下一个管理类请求被拒绝，执行代理照常发得出去。
	mctx, mcancel := context.WithCancel(context.Background())
	for i := 0; i < maxPendingSSHOps; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _, _ = a.sshOp(mctx, probe) }()
	}
	eventually(t, 3*time.Second, "管理类名额被占满", func() bool { return len(f.sim.opList()) == maxPendingSSHOps })
	if _, err := a.sshOp(context.Background(), probe); err != errTooManyOpens {
		t.Fatalf("管理类名额已满，应被拒绝: %v", err)
	}
	ectx, ecancel := context.WithCancel(context.Background())
	wg.Add(1)
	go func() { defer wg.Done(); _, _ = a.sshExec(ectx, target, "true", nil, 1024) }()
	eventually(t, 3*time.Second, "执行代理不受管理类名额影响", func() bool { return len(f.sim.opsOf(termproto.SSHOpExec)) == 1 })
	mcancel()
	ecancel()
	eventually(t, 3*time.Second, "名额释放", func() bool {
		a.mu.Lock()
		defer a.mu.Unlock()
		return len(a.sshWaits) == 0 && a.sshExecPending == 0
	})

	// 再占满执行代理的名额：下一个 exec 被拒绝，管理类请求照常发得出去。
	ectx2, ecancel2 := context.WithCancel(context.Background())
	for i := 0; i < maxPendingSSHExec; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _, _ = a.sshExec(ectx2, target, "true", nil, 1024) }()
	}
	eventually(t, 5*time.Second, "执行名额被占满", func() bool { return len(f.sim.opsOf(termproto.SSHOpExec)) == 1+maxPendingSSHExec })
	if _, err := a.sshExec(context.Background(), target, "true", nil, 1024); err != errTooManyOpens {
		t.Fatalf("执行名额已满，应被拒绝: %v", err)
	}
	mctx2, mcancel2 := context.WithCancel(context.Background())
	wg.Add(1)
	go func() { defer wg.Done(); _, _ = a.sshOp(mctx2, probe) }()
	eventually(t, 3*time.Second, "管理类请求不受执行名额影响", func() bool { return len(f.sim.opsOf(termproto.SSHOpProbe)) == maxPendingSSHOps+1 })
	ecancel2()
	mcancel2()
}

// ---------- 失败映射 ----------

func TestSSHGatewayExecFailureMapping(t *testing.T) {
	cases := []struct {
		name   string
		code   string
		state  string
		reason string
		check  func(t *testing.T, g *gwExecFixture)
	}{
		{"主机密钥不一致", termproto.SSHCodeHostKey, model.RunFailed, "ssh_host_key_mismatch", func(t *testing.T, g *gwExecFixture) {
			if ks := mapOf(g.hostView(t, g.code)["ssh"])["key_state"]; ks != "mismatch" {
				t.Fatalf("应置位 mismatch: %v", ks)
			}
		}},
		{"认证失败", termproto.SSHCodeAuth, model.RunFailed, "ssh_auth_failed", nil},
		{"连不上目标", termproto.SSHCodeUnreachable, model.RunFailed, "ssh_unreachable", nil},
		{"目标不在网关允许名单", termproto.SSHCodeDenied, model.RunFailed, "ssh_target_denied", func(t *testing.T, g *gwExecFixture) {
			if e := mapOf(g.hostView(t, g.code)["ssh"])["error"]; e != nil && e != "" {
				t.Fatalf("名单拒绝不应记成目标主机的连接故障: %v", e)
			}
		}},
		{"网关一票否决", termproto.SSHCodeExecDenied, model.RunDenied, termexec.CodeHostDenied, nil},
		{"网关繁忙", termproto.SSHCodeBusy, model.RunFailed, "ssh_gateway_unavailable", nil},
		{"网关没有开启网关能力", termproto.SSHCodeDisabled, model.RunFailed, "ssh_gateway_unavailable", nil},
		{"网关凭据库不可用", termproto.SSHCodeStore, model.RunFailed, "ssh_gateway_unavailable", nil},
		{"网关上没有凭据", termproto.SSHCodeNoCred, model.RunFailed, "ssh_cred_missing", func(t *testing.T, g *gwExecFixture) {
			if ssh := mapOf(g.hostView(t, g.code)["ssh"]); ssh["cred_missing"] != true {
				t.Fatalf("应标出凭据缺失: %v", ssh)
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			g := newGatewayExecFixture(t, withLogCapture())
			g.sim.failOp(termproto.SSHOpExec, &termproto.SSHResult{Code: tc.code, Msg: "simulated"})
			res := g.mustExec(t, "echo never", nil)
			if res.State != tc.state || res.Reason != tc.reason {
				t.Fatalf("期望 %s/%s，得到 %s/%s", tc.state, tc.reason, res.State, res.Reason)
			}
			if tc.check != nil {
				tc.check(t, g)
			}
			g.noSecretsLeaked(t)
		})
	}
}

// 凭据缺失：模型得到明确的 ssh_cred_missing；管理员重新填写凭据（由网关登录成功后保存）之后恢复。
func TestSSHGatewayExecCredentialMissingThenRestored(t *testing.T) {
	g := newGatewayExecFixture(t, withLogCapture())
	if res := g.mustExec(t, "echo up", nil); res.State != model.RunSucceeded {
		t.Fatalf("%+v", res)
	}
	g.sim.dropCred(g.code) // 网关重装 / 数据目录被清空

	res := g.mustExec(t, "echo lost", nil)
	if res.State != model.RunFailed || res.Reason != "ssh_cred_missing" {
		t.Fatalf("凭据丢失后的第一次执行: %+v", res)
	}
	// Hub 已经知道了：后续请求在闸门处直接拒绝，不再向网关发帧。
	before := len(g.sim.opList())
	_, err := g.exec("echo lost2", nil)
	wantCode(t, err, termexec.CodeSSHCredMissing)
	if tg := g.targetInfo(t); tg.Usable || tg.Blocked != termexec.CodeSSHCredMissing {
		t.Fatalf("targets = %+v", tg)
	}
	if len(g.sim.opList()) != before {
		t.Fatal("被闸门拒绝的请求不应再发给网关")
	}
	// 人工终端不在 Hub 这边预判：照常交给网关，网关没有凭据就会拒绝（见 TestOpenSSHSessionFailureMapping），
	// 万一凭据已经被补上，也不会被一个过期的「缺失」标记挡住。

	// 重新填写凭据。
	fix := g.env.call("POST", "/api/v1/terminal/hosts/"+g.code+"/ssh/connection", map[string]any{"auth_type": "password", "password": gwPassword}, g.adm...)
	if fix.Status != 200 {
		t.Fatalf("重新填写凭据: %d %s", fix.Status, fix.Raw)
	}
	if res := g.mustExec(t, "echo back", nil); res.State != model.RunSucceeded || res.Stdout == nil || res.Stdout.Text != "back\n" {
		t.Fatalf("恢复后: %+v", res)
	}
	if tg := g.targetInfo(t); !tg.Usable {
		t.Fatalf("恢复后 targets = %+v", tg)
	}
	g.noSecretsLeaked(t)
}

// 网关本机的一票否决（--deny-model-exec）：模型执行被拒绝并给出明确原因；人工终端不受影响；撤销之后恢复。
func TestSSHGatewayDenyExecVeto(t *testing.T) {
	g := newGatewayExecFixture(t)
	if res := g.mustExec(t, "echo ok", nil); res.State != model.RunSucceeded {
		t.Fatalf("%+v", res)
	}
	reportCapsDeny(g.gw, gatewayCaps()...)
	eventually(t, 2*time.Second, "一票否决生效", func() bool {
		_, denied := g.env.hub.gatewayExecState(g.gw.code)
		return denied
	})

	_, err := g.exec("echo vetoed", nil)
	wantCode(t, err, termexec.CodeHostDenied)
	if tg := g.targetInfo(t); tg.Usable || tg.Blocked != termexec.CodeHostDenied {
		t.Fatalf("targets = %+v", tg)
	}
	// 界面据此提示「网关本机一票否决」，服务端无法更改。
	if ssh := mapOf(g.hostView(t, g.code)["ssh"]); ssh["gateway_denied"] != true {
		t.Fatalf("视图应标出网关一票否决: %v", ssh)
	}
	// 一票否决只管模型：人工终端照常可用，Web 端的复测也照常。
	if open := g.openSSH(g.gw.code, g.code, nil); open.Status != 200 {
		t.Fatalf("人工终端不受一票否决影响: %d %s", open.Status, open.Raw)
	}
	if res := g.env.call("POST", "/api/v1/terminal/hosts/"+g.code+"/ssh/test", nil, g.adm...); res.Status != 200 || mapOf(res.Body["result"])["ok"] != true {
		t.Fatalf("复测不受影响: %d %s", res.Status, res.Raw)
	}

	// 撤销。
	reportCaps(g.gw, gatewayCaps()...)
	eventually(t, 2*time.Second, "一票否决撤销", func() bool {
		_, denied := g.env.hub.gatewayExecState(g.gw.code)
		return !denied
	})
	if res := g.mustExec(t, "echo again", nil); res.State != model.RunSucceeded {
		t.Fatalf("撤销后: %+v", res)
	}
	if ssh := mapOf(g.hostView(t, g.code)["ssh"]); ssh["gateway_denied"] == true {
		t.Fatalf("撤销后不应再标一票否决: %v", ssh)
	}
}

// 运行进行到一半，网关本机才开启一票否决：这次运行再也无法经网关观察，按「结果无法确认」收尾，并给出明确的原因。
func TestSSHGatewayVetoMidRunEndsRunAsUnconfirmed(t *testing.T) {
	g := newGatewayExecFixture(t)
	n := sshSleep(23)
	var runID string
	t.Cleanup(func() {
		// 一票否决之后 Hub 不能再经网关取消它，测试自己按进程号收拾；
		// 还要等看守脚本退出，否则它收尾写的文件会和临时目录的清理撞上（任务 #133）。
		killSleeps(t, n)
		if runID != "" {
			waitRunnerGone(t, runID, 5*time.Second)
		}
	})
	res := g.mustExec(t, fmt.Sprintf("sleep %d", n), func(r *termexec.ExecRequest) { r.WaitSeconds = 0 })
	runID = res.RunID
	waitProcs(t, n, 1, 8*time.Second, "远端进程已启动")

	reportCapsDeny(g.gw, gatewayCaps()...)
	eventually(t, 20*time.Second, "运行因网关一票否决而收尾", func() bool { return model.IsFinalRunState(g.dbRun(t, res.RunID).State) })
	rec := g.dbRun(t, res.RunID)
	if rec.State != model.RunLost || rec.DenyReason != termproto.RunFailDenied {
		t.Fatalf("记录 = state %s reason %q", rec.State, rec.DenyReason)
	}
	out, err := g.svc.Run(context.Background(), g.callerFor(), termexec.RunRequest{Action: "status", RunID: res.RunID})
	if err != nil {
		t.Fatal(err)
	}
	if r := out.(*termexec.RunResult); r.Message == "" || !strings.Contains(r.Message, "--deny-model-exec") {
		t.Fatalf("应给出明确的原因: %+v", r)
	}
}

// 网关离线：模型立刻得到 host_offline（不是漫长的超时）；网关回来之后恢复。
func TestSSHGatewayExecWhileGatewayOffline(t *testing.T) {
	g := newGatewayExecFixture(t)
	g.gw.close()
	eventually(t, 3*time.Second, "网关离线", func() bool { return g.env.hub.onlineAgent(g.gw.code) == nil })

	start := time.Now()
	_, err := g.exec("echo x", nil)
	wantCode(t, err, termexec.CodeHostOffline)
	if time.Since(start) > 2*time.Second {
		t.Fatalf("应立刻返回: %v", time.Since(start))
	}
	if tg := g.targetInfo(t); tg.Online || tg.Usable {
		t.Fatalf("targets = %+v", tg)
	}
	// 网关回来（凭据库还在）：恢复。
	gw, _ := g.reconnect(t, g.gw, g.sim, "gateway-box", true)
	eventually(t, 3*time.Second, "网关重新就绪", func() bool { return g.env.hub.gatewayReady(gw.code) })
	if res := g.mustExec(t, "echo back", nil); res.State != model.RunSucceeded || res.Stdout == nil || res.Stdout.Text != "back\n" {
		t.Fatalf("恢复后: %+v", res)
	}
}

// ---------- 生命周期 ----------

// 删除主机：先取消它名下进行中的远端作业，再通知网关删除凭据（取消要用那份凭据）。
func TestSSHGatewayDeleteHostCancelsRemoteRunBeforeDroppingCredential(t *testing.T) {
	g := newGatewayExecFixture(t)
	n := sshSleep(21)
	res := g.mustExec(t, fmt.Sprintf("sleep %d", n), func(r *termexec.ExecRequest) { r.WaitSeconds = 0 })
	waitProcs(t, n, 1, 8*time.Second, "远端进程已启动")

	del := g.env.call("DELETE", "/api/v1/terminal/hosts/"+g.code, nil, g.adm...)
	if del.Status != 200 {
		t.Fatalf("删除主机: %d %s", del.Status, del.Raw)
	}
	waitProcs(t, n, 0, 15*time.Second, "删除主机后远端进程被终止")
	eventually(t, 15*time.Second, "网关上的凭据被清掉", func() bool { _, ok := g.sim.credOf(g.code); return !ok })

	// 顺序：cred_del 必须排在最后一次 exec（取消）之后，否则取消会因为凭据已被删掉而落空。
	ops := g.sim.opList()
	del2 := -1
	for i, op := range ops {
		if op.Op == termproto.SSHOpCredDel {
			del2 = i
		}
	}
	if del2 < 0 {
		t.Fatalf("网关没有收到 cred_del: %d 个操作", len(ops))
	}
	for _, op := range ops[del2+1:] {
		if op.Op == termproto.SSHOpExec {
			t.Fatal("cred_del 之后不应再有 exec（远端取消应在它之前完成）")
		}
	}
	eventually(t, 10*time.Second, "运行进入终态", func() bool {
		rec, _ := g.env.store.GetTerminalRun(context.Background(), res.RunID)
		return rec == nil || model.IsFinalRunState(rec.State)
	})
}

// 停用主机：取消进行中的作业，但凭据留在网关上——重新启用之后还要用。
func TestSSHGatewayDisableCancelsRunAndKeepsCredential(t *testing.T) {
	g := newGatewayExecFixture(t)
	n := sshSleep(22)
	res := g.mustExec(t, fmt.Sprintf("sleep %d", n), func(r *termexec.ExecRequest) { r.WaitSeconds = 0 })
	waitProcs(t, n, 1, 8*time.Second, "远端进程已启动")

	if dis := g.env.call("POST", "/api/v1/terminal/hosts/"+g.code+"/disable", nil, g.adm...); dis.Status != 200 {
		t.Fatalf("停用主机: %d %s", dis.Status, dis.Raw)
	}
	waitProcs(t, n, 0, 15*time.Second, "停用主机后远端进程被终止")
	eventually(t, 10*time.Second, "运行进入终态", func() bool { return model.IsFinalRunState(g.dbRun(t, res.RunID).State) })
	if _, ok := g.sim.credOf(g.code); !ok {
		t.Fatal("停用不应删除网关上的凭据")
	}
	if _, err := g.exec("echo x", nil); err == nil {
		t.Fatal("停用的主机不应能执行命令")
	}
	if len(g.sim.opsOf(termproto.SSHOpCredDel)) != 0 {
		t.Fatal("停用不应向网关发 cred_del")
	}
}

// ---------- 凭据对账 ----------

func TestSSHGatewayCredSyncReconcilesOnReconnect(t *testing.T) {
	f := newGatewayFixture(t)
	a, b := f.add(t, "box-a"), f.add(t, "box-b")
	// 停用 b：对账名单必须包含停用的主机（重新启用后还要用它的凭据）。
	if dis := f.env.call("POST", "/api/v1/terminal/hosts/"+b+"/disable", nil, f.adm...); dis.Status != 200 {
		t.Fatalf("停用: %d %s", dis.Status, dis.Raw)
	}
	// 网关那边的现状：多出一份没有任何主机再使用的凭据（上次删除时网关恰好离线），并且丢了 a 的凭据。
	orphan, _ := termproto.RandomMachineCode()
	f.sim.putCred(orphan, "stale-pass")
	f.sim.dropCred(a)

	_, sim := f.reconnect(t, f.gw, f.sim, "gateway-box", true)
	eventually(t, 5*time.Second, "对账完成", func() bool { return len(sim.opsOf(termproto.SSHOpCredSync)) == 1 })

	keep := append([]string(nil), sim.opsOf(termproto.SSHOpCredSync)[0].Keep...)
	want := []string{a, b}
	slices.Sort(keep)
	slices.Sort(want)
	if !slices.Equal(keep, want) {
		t.Fatalf("对账名单 = %v，期望 %v（含停用的主机）", keep, want)
	}
	if _, ok := sim.credOf(orphan); ok {
		t.Fatal("名单之外的凭据应被网关回收")
	}
	if _, ok := sim.credOf(b); !ok {
		t.Fatal("停用主机的凭据不应被回收")
	}
	eventually(t, 3*time.Second, "缺失的凭据被标出", func() bool { return mapOf(f.hostView(t, a)["ssh"])["cred_missing"] == true })
	if mapOf(f.hostView(t, b)["ssh"])["cred_missing"] == true {
		t.Fatal("b 的凭据还在网关上，不应标成缺失")
	}
	ev := f.env.auditEvents(EvSSHCredSync)
	if len(ev) == 0 || !strings.Contains(ev[len(ev)-1].Detail, "missing=1") || !strings.Contains(ev[len(ev)-1].Detail, "dropped=1") {
		t.Fatalf("审计应记下对账结果: %+v", ev)
	}
	// 每条连接只对账一次；再次上报能力不会重复。
	reportCaps(f.gw, gatewayCaps()...)
	time.Sleep(200 * time.Millisecond)
	if n := len(sim.opsOf(termproto.SSHOpCredSync)); n != 1 {
		t.Fatalf("同一条连接不应重复对账: %d", n)
	}
}

// 凭据库跟着守护进程的这一代走：守护进程中途不可达、或换了一代（BootID 变了：重启 / 重装，凭据库未必还是原来那份），
// 重新就绪之后都要再对账；同一代守护进程反复上报则不重复。
func TestSSHGatewayCredSyncRunsAgainWhenDaemonGenerationChanges(t *testing.T) {
	f := newGatewayFixture(t)
	code := f.add(t, "box")
	count := func() int { return len(f.sim.opsOf(termproto.SSHOpCredSync)) }
	if count() != 1 {
		t.Fatalf("首次对账 = %d", count())
	}
	boot := func(id string) {
		f.gw.send(termproto.Holder{T: termproto.THolder, State: termproto.HolderReady, Persistent: true, Version: "test", BootID: id, Caps: gatewayCaps()})
	}
	boot("boot-1") // 第一次带 BootID 的上报，与之前（空）相比算换了一代
	eventually(t, 3*time.Second, "BootID 出现后重新对账", func() bool { return count() == 2 })
	boot("boot-1")
	time.Sleep(200 * time.Millisecond)
	if count() != 2 {
		t.Fatalf("同一代守护进程重复上报不应重复对账: %d", count())
	}

	// 守护进程换了一代，且新一代的凭据库是空的（例如重装）：对账之后主机被标成凭据缺失。
	f.sim.dropCred(code)
	boot("boot-2")
	eventually(t, 3*time.Second, "换代后重新对账", func() bool { return count() == 3 })
	eventually(t, 3*time.Second, "凭据缺失被发现", func() bool { return mapOf(f.hostView(t, code)["ssh"])["cred_missing"] == true })

	// 中途不可达再恢复同样会重新对账。
	f.gw.send(termproto.Holder{T: termproto.THolder, State: termproto.HolderUnavailable, Persistent: true, BootID: "boot-2", Reason: "down"})
	eventually(t, 3*time.Second, "守护进程不可达", func() bool { return !f.env.hub.gatewayReady(f.gw.code) })
	boot("boot-2")
	eventually(t, 3*time.Second, "恢复后重新对账", func() bool { return count() == 4 })
}

// 登记一台主机时，网关先保存凭据、Hub 随后才落库。这一小段空窗里，另一台网关的对账不能抢先把凭据当成多余的回收掉：
// 登记 / 修改凭据与对账互斥。
func TestSSHGatewayCredSyncWaitsForInFlightAdd(t *testing.T) {
	f := newGatewayFixture(t)
	gw2, sim2 := f.newGateway(t, "gateway-two")

	hold := make(chan struct{})
	f.sim.setHold(hold)
	fp := f.probe(t, f.gw.code)
	added := make(chan apiResult, 1)
	go func() {
		added <- f.env.call("POST", "/api/v1/terminal/ssh/hosts", f.addBody(fp, "slow box", f.gw.code), f.adm...)
	}()
	eventually(t, 3*time.Second, "登记中的登录检查已到达网关", func() bool {
		for _, c := range f.sim.opsOf(termproto.SSHOpCheck) {
			if c.Persist {
				return true
			}
		}
		return false
	})

	// 此刻第二台网关重连并上报能力：它的对账必须等登记结束。
	_, sim2 = f.reconnect(t, gw2, sim2, "gateway-two", true)
	time.Sleep(400 * time.Millisecond)
	if n := len(sim2.opsOf(termproto.SSHOpCredSync)); n != 0 {
		t.Fatalf("登记尚未结束，对账不应发出: %d", n)
	}
	close(hold)
	select {
	case res := <-added:
		if res.Status != 200 {
			t.Fatalf("登记: %d %s", res.Status, res.Raw)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("登记没有完成")
	}
	eventually(t, 5*time.Second, "登记结束后对账继续", func() bool { return len(sim2.opsOf(termproto.SSHOpCredSync)) == 1 })
	if f.sim.credCount() != 1 {
		t.Fatalf("新登记的凭据应保留在第一台网关上: %d", f.sim.credCount())
	}
}

// 对账名单取不到 / 网关没有凭据库能力时，不发空名单（空名单会让网关回收全部凭据）。
func TestSSHGatewayCredSyncNeverRunsWithoutStoreCapability(t *testing.T) {
	f := newGatewayFixture(t)
	code := f.add(t, "box")
	a, sim := f.reconnect(t, f.gw, f.sim, "gateway-box", false)
	reportCaps(a, "policy", "drain", "lost", termproto.HolderCapSSH) // 没有 ssh_store
	time.Sleep(300 * time.Millisecond)
	if n := len(sim.opsOf(termproto.SSHOpCredSync)); n != 0 {
		t.Fatalf("没有凭据库能力时不应对账: %d", n)
	}
	if _, ok := sim.credOf(code); !ok {
		t.Fatal("凭据不应被动")
	}
}
