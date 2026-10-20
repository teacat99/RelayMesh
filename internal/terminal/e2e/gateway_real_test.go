//go:build e2e && !windows

package e2e

import (
	"bytes"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/teacat99/RelayMesh/v2/internal/sshexec/sshdtest"
	"github.com/teacat99/RelayMesh/v2/internal/terminal"
	"github.com/teacat99/RelayMesh/v2/internal/termproto"
)

// SSH 网关的真实进程端到端：Hub、会话守护进程、Agent 各是独立的操作系统进程，
// SSH 目标是进程内的测试 sshd（会话里跑的是它的迷你 shell，模型执行的命令由本机的 sh 真实执行），浏览器 / 模型一侧由 termClient / MCP 客户端扮演。
//
// 要证明的是字节通路、持久性与凭据归属：
//   - SSH 连接由网关上的会话守护进程持有，所以 Agent 被强杀、Hub 重启之后，同一个 SSH 会话仍然在线、历史可回放、还能继续输入；
//   - 登录凭据只保存在网关自己的数据目录里（密封），Hub 的数据库与日志里没有；Hub 甚至没有配置 SSH 主密钥，网关主机照样可用；
//   - 模型执行经网关代理（Hub 驱动远端作业的每一步都是一次 stored 引用的 exec），Agent 强杀 / Hub 重启后进行中的运行仍能取回结果；
//   - 主机删除时网关上的凭据被清掉；网关的凭据库丢了，Hub 能发现并提示重新填写。

type gwLab struct {
	t        *testing.T
	h        *hub
	d        hostDirs
	hp       *proc
	ag       *proc
	agentBin string
	srv      *sshdtest.Server
	pass     string
	gw       string // 网关主机的机器码
	holder   []string
}

// newGWLab 启动 Hub、守护进程（带 holderArgs）与 Agent，启用网关主机并等待它就绪。
// Hub 打开了模型执行总开关，但**没有**配置 RELAYMESH_SSH_SECRET_KEY：直连的 SSH 后端因此是关闭的，网关主机不受影响。
func newGWLab(t *testing.T, holderArgs ...string) *gwLab {
	t.Helper()
	hubBin, agentBin := binaries(t)
	l := &gwLab{t: t, agentBin: agentBin, pass: "p" + randHex(10), holder: holderArgs}
	l.h = startHub(t, hubBin, execOn()...)
	l.d = newHostDirs(t)
	l.hp = startHolder(t, agentBin, l.d, holderArgs...)
	l.ag = startAgent(t, agentBin, l.h, l.d, "agent1", "--holder", "external")
	l.gw = l.h.api.enrollOnly()
	waitHost(t, l.h, l.gw, "网关主机上线且守护进程就绪", func(v terminal.HostView) bool {
		return v.Online && v.Holder != nil && v.Holder.State == "ready"
	})
	l.srv = sshdtest.Start(t, sshdtest.Options{User: "e2e", Password: l.pass})
	return l
}

func (l *gwLab) hasGatewayCap() bool {
	v, ok := l.h.api.host(l.gw)
	return ok && v.Holder != nil && slices.Contains(v.Holder.Caps, termproto.HolderCapSSH)
}

type gwReply struct {
	Code string `json:"code"`
}

// probe 经网关探测目标的主机密钥；返回指纹、HTTP 状态与错误码。
func (l *gwLab) probe(addr string) (fp string, status int, code string) {
	l.t.Helper()
	var out struct {
		Probe struct {
			Fingerprint string `json:"fingerprint"`
		} `json:"probe"`
		gwReply
	}
	status = l.h.api.call("POST", "/ssh/probe", map[string]string{"address": addr, "gateway": l.gw}, &out)
	return out.Probe.Fingerprint, status, out.Code
}

// addHost 经网关登记一台 SSH 主机，返回它的机器码。登录凭据只出现在这一次请求里：网关登录成功后把它保存在自己的库里。
func (l *gwLab) addHost(addr, fp, remark string) string {
	l.t.Helper()
	var out struct {
		Host struct {
			MachineCode string `json:"machine_code"`
		} `json:"host"`
		gwReply
	}
	body := map[string]string{
		"remark": remark, "address": addr, "user": "e2e", "auth_type": "password",
		"password": l.pass, "fingerprint": fp, "gateway": l.gw,
	}
	if c := l.h.api.call("POST", "/ssh/hosts", body, &out); c != 200 || out.Host.MachineCode == "" {
		l.t.Fatalf("经网关登记 SSH 主机失败: HTTP %d %s", c, out.Code)
	}
	return out.Host.MachineCode
}

// open 在网关上为 SSH 主机打开一个人工终端会话。
func (l *gwLab) open(target string) termproto.SessionInfo {
	l.t.Helper()
	var r struct {
		Session termproto.SessionInfo `json:"session"`
		gwReply
	}
	c := l.h.api.call("POST", "/hosts/"+l.gw+"/sessions", map[string]any{"ssh_target": target, "rows": 30, "cols": 100}, &r)
	if c != 200 || r.Session.SID == "" {
		l.t.Fatalf("打开 SSH 终端失败: HTTP %d %s", c, r.Code)
	}
	return r.Session
}

// credFile 是网关上凭据库文件的位置（<数据目录>/ssh-gateway/credentials.json）。
func (l *gwLab) credFile() string {
	return filepath.Join(l.d.data, "ssh-gateway", "credentials.json")
}

// credHolds 报告网关的凭据库文件里是否有某台 SSH 主机的条目（键是机器码，值是密封后的密文）。
func (l *gwLab) credHolds(code string) bool {
	b, err := os.ReadFile(l.credFile())
	return err == nil && bytes.Contains(b, []byte(`"`+code+`"`))
}

// modelClient 签发一把只绑定某台主机的 ExecMesh 档令牌，并先在 Web 上允许模型访问这台主机。
func (l *gwLab) modelClient(target string) *mcpClient {
	l.t.Helper()
	if c := l.h.api.call("POST", "/hosts/"+target+"/model", map[string]any{"allow_model": true}, nil); c != 200 {
		l.t.Fatalf("允许模型访问失败: HTTP %d", c)
	}
	var tok struct {
		Token string `json:"token"`
	}
	c := l.h.api.raw("POST", "/api/v1/credentials", map[string]any{
		"name": "gw-e2e-" + randHex(3), "permissions": map[string]any{"terminal": true}, "terminal_hosts": []string{target},
	}, &tok, true)
	if c != 200 || tok.Token == "" {
		l.t.Fatalf("创建令牌失败: HTTP %d", c)
	}
	return &mcpClient{t: l.t, base: l.h.base(), token: tok.Token, hc: &http.Client{Timeout: 90 * time.Second}}
}

// line 是迷你 shell 的一行「输出」：它前面是换行，从而区别于键入时的回显（回显的 echo 命令前面是空格）。
func line(s string) string { return "\n" + s + "\r\n" }

// leaks 在整套环境的所有文件里查找凭据字节串（Hub 数据库与日志、守护进程 / Agent 的数据目录与日志、会话记录）。
func (l *gwLab) leaks(needles ...string) []string {
	var found []string
	for _, root := range []string{l.h.dir, l.d.data, l.d.cfg, l.d.logs} {
		_ = filepath.WalkDir(root, func(p string, e fs.DirEntry, err error) error {
			if err != nil || e.IsDir() || e.Type()&fs.ModeSocket != 0 {
				return nil
			}
			if info, ierr := e.Info(); ierr != nil || info.Size() > 64<<20 || !info.Mode().IsRegular() {
				return nil
			}
			b, rerr := os.ReadFile(p)
			if rerr != nil {
				return nil
			}
			for _, n := range needles {
				if bytes.Contains(b, []byte(n)) {
					found = append(found, p)
				}
			}
			return nil
		})
	}
	return found
}

func TestRealSSHGatewaySessionSurvivesAgentKillAndHubRestart(t *testing.T) {
	l := newGWLab(t, "--ssh-gateway")
	if !l.hasGatewayCap() {
		t.Fatal("开启 --ssh-gateway 的守护进程应在 holder 帧里声明 ssh 能力")
	}
	fp, st, code := l.probe(l.srv.Addr)
	if st != 200 || fp == "" {
		t.Fatalf("经网关探测失败: HTTP %d %s", st, code)
	}
	target := l.addHost(l.srv.Addr, fp, "inner server")
	if !l.credHolds(target) {
		t.Fatal("登录成功之后，凭据应保存在网关自己的凭据库里")
	}

	sess := l.open(target)
	if sess.Shell != "ssh" || sess.Target != target || sess.PID != 0 {
		t.Fatalf("会话应标注 ssh 并指回目标，且没有本地进程: %+v", sess)
	}
	tc := l.h.api.attach(l.h, l.gw, sess.SID, 0)
	tc.waitFor("welcome e2e", 15*time.Second)
	tagA := randHex(4)
	tc.send("echo ping-" + tagA + "\r")
	tc.waitFor(line("ping-"+tagA), 10*time.Second)
	tc.close()

	// —— 强杀 Agent：SSH 连接由守护进程持有，不受影响 ——
	l.ag.kill9()
	if !l.hp.alive() {
		t.Fatal("Agent 被杀后守护进程不应受影响")
	}
	waitHost(t, l.h, l.gw, "Hub 感知到网关离线", func(v terminal.HostView) bool { return !v.Online })
	l.ag = startAgent(t, l.agentBin, l.h, l.d, "agent2", "--holder", "external")
	waitHost(t, l.h, l.gw, "网关重新上线", func(v terminal.HostView) bool {
		return v.Online && v.Holder != nil && v.Holder.State == "ready"
	})
	_, list := l.h.api.sessions(l.gw)
	got, ok := sessionByID(list, sess.SID)
	if !ok || got.State != "running" || got.Target != target || got.Shell != "ssh" {
		t.Fatalf("SSH 会话应原样保留: ok=%v %+v", ok, got)
	}
	tc2 := l.h.api.attach(l.h, l.gw, sess.SID, 0)
	tc2.waitFor(line("ping-"+tagA), 10*time.Second) // 回放了 Agent 被杀之前的输出
	tagB := randHex(4)
	tc2.send("echo after-kill-" + tagB + "\r")
	tc2.waitFor(line("after-kill-"+tagB), 10*time.Second)
	tc2.close()

	// —— Hub 重启 ——
	l.h.restart()
	waitHost(t, l.h, l.gw, "Hub 重启后网关重新上线", func(v terminal.HostView) bool {
		return v.Online && v.Holder != nil && v.Holder.State == "ready"
	})
	tc3 := l.h.api.attach(l.h, l.gw, sess.SID, 0)
	tc3.waitFor(line("after-kill-"+tagB), 10*time.Second)
	tagC := randHex(4)
	tc3.send("echo after-hub-" + tagC + "\r")
	tc3.waitFor(line("after-hub-"+tagC), 10*time.Second)

	// 整个过程只有一个 shell 被打开：没有因为 Agent / Hub 的变故而重连。
	if n := l.srv.Shells(); n != 1 {
		t.Fatalf("目标上应只开过 1 个 shell，实际 %d", n)
	}
	// 凭据一直留在网关上，Hub 重启不影响它。
	if !l.credHolds(target) {
		t.Fatal("Hub 重启后网关上的凭据应原样保留")
	}

	// —— 目标端结束 shell：会话以远端的退出码结束 ——
	tc3.send("exit 7\r")
	waitUntil(t, 15*time.Second, "SSH 会话以退出码 7 结束", func() bool {
		_, list := l.h.api.sessions(l.gw)
		s, ok := sessionByID(list, sess.SID)
		return ok && s.State == "exited" && s.ExitCode != nil && *s.ExitCode == 7
	})

	// —— 凭据卫生：口令只应出现在发给目标的认证里，不应以明文落在任何文件里 ——
	if found := l.leaks(l.pass); len(found) > 0 {
		t.Fatalf("凭据以明文出现在了文件里: %v", found)
	}
}

// 模型经网关在内网主机上执行命令：凭据始终在网关上，Hub 没有主密钥也没有任何副本。
func TestRealSSHGatewayModelExecUsesCredentialStoredOnGateway(t *testing.T) {
	l := newGWLab(t, "--ssh-gateway")
	fp, _, _ := l.probe(l.srv.Addr)
	target := l.addHost(l.srv.Addr, fp, "inner server")

	// Hub 没有 SSH 主密钥：直连后端是关闭的——但经网关接入的主机照常可用。
	var status struct {
		SSH struct {
			Enabled bool   `json:"enabled"`
			Reason  string `json:"reason"`
		} `json:"ssh"`
	}
	if c := l.h.api.raw("GET", "/api/v1/terminal/status", nil, &status, false); c != 200 || status.SSH.Enabled || status.SSH.Reason != "no_secret_key" {
		t.Fatalf("Hub 不应配置 SSH 主密钥: HTTP %d %+v", c, status)
	}
	var view struct {
		Hosts []terminal.HostView `json:"hosts"`
	}
	l.h.api.call("GET", "/hosts", nil, &view)
	var ssh *terminal.SSHView
	for _, h := range view.Hosts {
		if h.MachineCode == target {
			ssh = h.SSH
		}
	}
	if ssh == nil || !ssh.CredOnGateway || ssh.HasSecret || ssh.CredMissing || ssh.TermOnly || ssh.Gateway != l.gw {
		t.Fatalf("主机视图应表明凭据在网关上: %+v", ssh)
	}

	m := l.modelClient(target)
	targets, isErr := m.tool("terminal_targets", map[string]any{})
	if isErr {
		t.Fatalf("terminal_targets: %v", targets)
	}
	list, _ := targets["targets"].([]any)
	if len(list) != 1 {
		t.Fatalf("targets = %v", targets)
	}
	tg, _ := list[0].(map[string]any)
	if tg["backend"] != "ssh" || tg["usable"] != true {
		t.Fatalf("目标 = %v", tg)
	}

	marker := "gw-" + randHex(4)
	res := m.exec(target, "echo "+marker+"; echo err-"+marker+" >&2; exit 2", nil)
	if res.str("state") != "failed" || res.num("exit_code") != 2 ||
		!strings.Contains(res.obj("stdout").str("text"), marker) || !strings.Contains(res.obj("stderr").str("text"), "err-"+marker) {
		t.Fatalf("结果 = %v", res)
	}
	// 没有指定工作目录时在家目录里运行。
	if pwd := m.exec(target, "pwd", nil).obj("stdout").str("text"); strings.TrimSpace(pwd) != l.srv.Home {
		t.Fatalf("默认工作目录 = %q，应为家目录 %q", pwd, l.srv.Home)
	}
	// 大输出：完整输出留在目标上，经网关分块读取。
	big := m.exec(target, "seq 1 30000", nil)
	if big.str("state") != "succeeded" || big.obj("stdout").num("bytes") != 168894 {
		t.Fatalf("大输出: %v", big)
	}
	next := big.obj("stdout").num("next_offset")
	page, isErr := m.run("output", big.str("run_id"), map[string]any{"stream": "stdout", "offset": next, "limit": 256 << 10})
	if isErr || page["eof"] != true || !strings.HasSuffix(page.str("text"), "\n30000\n") || int64(len(page.str("text"))) != 168894-next {
		t.Fatalf("大输出分页: eof=%v len=%d next=%d", page["eof"], len(page.str("text")), next)
	}

	// 命令文本从不出现在 SSH 命令行上。
	for _, c := range l.srv.Commands() {
		if strings.Contains(c, marker) {
			t.Fatalf("命令文本泄漏到 SSH 命令行: %q", c)
		}
	}
	// 凭据卫生：Hub 的数据库与日志、网关与 Agent 的所有文件里都没有明文口令。
	if found := l.leaks(l.pass); len(found) > 0 {
		t.Fatalf("口令以明文出现在了文件里: %v", found)
	}
}

// 网关本机的一票否决（--deny-model-exec）管到经它接入的主机：模型被拒绝，人工终端照常可用。
func TestRealSSHGatewayDenyModelExecVetoCoversTargets(t *testing.T) {
	l := newGWLab(t, "--ssh-gateway", "--deny-model-exec")
	fp, _, _ := l.probe(l.srv.Addr)
	target := l.addHost(l.srv.Addr, fp, "inner server")
	m := l.modelClient(target)

	tg, _ := m.tool("terminal_targets", map[string]any{})
	list, _ := tg["targets"].([]any)
	if len(list) != 1 {
		t.Fatalf("targets: %v", tg)
	}
	one := toolRes(list[0].(map[string]any))
	if one["usable"] == true || one.str("blocked") != "host_denied" {
		t.Fatalf("被否决的主机应标明 host_denied: %v", one)
	}
	denied, isErr := m.tool("terminal_exec", map[string]any{"target": target, "command": "touch /tmp/should-not-exist-" + randHex(4)})
	if !isErr || denied.errCode() != "host_denied" {
		t.Fatalf("应被一票否决: %v", denied)
	}
	// 人工终端照常可用。
	sess := l.open(target)
	tc := l.h.api.attach(l.h, l.gw, sess.SID, 0)
	tc.waitFor("welcome e2e", 15*time.Second)
	tag := randHex(4)
	tc.send("echo human-ok-" + tag + "\r")
	tc.waitFor(line("human-ok-"+tag), 10*time.Second)
}

// 进行中的模型运行经得起网关侧的 Agent 被强杀、以及 Hub 重启：远端作业不受影响，恢复后取回结果。
func TestRealSSHGatewayModelExecSurvivesAgentKillAndHubRestart(t *testing.T) {
	l := newGWLab(t, "--ssh-gateway")
	fp, _, _ := l.probe(l.srv.Addr)
	target := l.addHost(l.srv.Addr, fp, "inner server")
	m := l.modelClient(target)

	// —— Agent 被强杀 ——
	tokA := "done-A-" + randHex(4)
	resA := m.exec(target, "echo begin-A; sleep 6; echo "+tokA, map[string]any{"wait_seconds": 0})
	idA := resA.str("run_id")
	if resA.str("state") != "running" {
		t.Fatalf("应在运行: %v", resA)
	}
	time.Sleep(1200 * time.Millisecond) // 让它真正在远端启动
	l.ag.kill9()
	waitHost(t, l.h, l.gw, "Hub 感知到网关离线", func(v terminal.HostView) bool { return !v.Online })
	l.ag = startAgent(t, l.agentBin, l.h, l.d, "agent2", "--holder", "external")
	waitHost(t, l.h, l.gw, "网关重新上线", func(v terminal.HostView) bool {
		return v.Online && v.Holder != nil && v.Holder.State == "ready"
	})
	st, isErr := m.run("status", idA, map[string]any{"wait_seconds": 40})
	if isErr || st.str("state") != "succeeded" || !strings.Contains(st.obj("stdout").str("text"), tokA) {
		t.Fatalf("Agent 重启后应取回运行结果: %v", st)
	}

	// —— Hub 重启 ——
	tokB := "done-B-" + randHex(4)
	resB := m.exec(target, "echo begin-B; sleep 6; echo "+tokB, map[string]any{"wait_seconds": 0})
	idB := resB.str("run_id")
	if resB.str("state") != "running" {
		t.Fatalf("应在运行: %v", resB)
	}
	time.Sleep(1200 * time.Millisecond)
	l.h.restart()
	m.hc = &http.Client{Timeout: 90 * time.Second}
	m.base = l.h.base()
	waitHost(t, l.h, l.gw, "Hub 重启后网关重新上线", func(v terminal.HostView) bool {
		return v.Online && v.Holder != nil && v.Holder.State == "ready"
	})
	st, isErr = m.run("status", idB, map[string]any{"wait_seconds": 40})
	if isErr || st.str("state") != "succeeded" || !strings.Contains(st.obj("stdout").str("text"), tokB) {
		t.Fatalf("Hub 重启后应接管并取回运行结果: %v", st)
	}
	page, isErr := m.run("output", idB, map[string]any{})
	if isErr || !strings.Contains(page.str("text"), "begin-B") {
		t.Fatalf("重启后输出分页: %v", page)
	}
	if !l.credHolds(target) {
		t.Fatal("凭据应一直留在网关上")
	}
}

// 删除 SSH 主机：网关上保存的凭据随之被清掉。
func TestRealSSHGatewayDeleteHostRemovesStoredCredential(t *testing.T) {
	l := newGWLab(t, "--ssh-gateway")
	fp, _, _ := l.probe(l.srv.Addr)
	keep := l.addHost(l.srv.Addr, fp, "keep")
	drop := l.addHost(l.srv.Addr, fp, "drop")
	if !l.credHolds(keep) || !l.credHolds(drop) {
		t.Fatal("两台主机的凭据都应保存在网关上")
	}
	if c := l.h.api.call("DELETE", "/hosts/"+drop, nil, nil); c != 200 {
		t.Fatalf("删除主机: HTTP %d", c)
	}
	waitUntil(t, 15*time.Second, "被删除主机的凭据从网关凭据库中清除", func() bool { return !l.credHolds(drop) })
	if !l.credHolds(keep) {
		t.Fatal("其余主机的凭据不应受影响")
	}
}

// 网关的凭据库丢了（例如换了数据卷）：Hub 在守护进程重新就绪后对账，发现凭据缺失，模型得到明确的原因；
// 管理员重新填写凭据之后恢复。
func TestRealSSHGatewayCredentialLossIsDetectedAndRecoverable(t *testing.T) {
	l := newGWLab(t, "--ssh-gateway")
	fp, _, _ := l.probe(l.srv.Addr)
	target := l.addHost(l.srv.Addr, fp, "inner server")
	m := l.modelClient(target)
	if res := m.exec(target, "echo before", nil); res.str("state") != "succeeded" {
		t.Fatalf("%v", res)
	}

	// 停掉守护进程，抹掉凭据库（保留密钥文件），再起一个新的：等同于网关换了数据卷。
	holderStop(t, l.agentBin, l.d)
	if err := os.Remove(l.credFile()); err != nil {
		t.Fatalf("抹掉凭据库: %v", err)
	}
	l.hp = startHolder(t, l.agentBin, l.d, l.holder...)
	waitHost(t, l.h, l.gw, "守护进程重新就绪", func(v terminal.HostView) bool {
		return v.Online && v.Holder != nil && v.Holder.State == "ready" && slices.Contains(v.Holder.Caps, termproto.HolderCapSSHStore)
	})
	waitHost(t, l.h, target, "Hub 发现网关上没有这台主机的凭据", func(v terminal.HostView) bool {
		return v.SSH != nil && v.SSH.CredMissing
	})
	denied, isErr := m.tool("terminal_exec", map[string]any{"target": target, "command": "echo lost"})
	if !isErr || denied.errCode() != "ssh_cred_missing" {
		t.Fatalf("模型应得到明确的凭据缺失原因: %v", denied)
	}

	// 重新填写凭据：网关登录成功后保存，缺失标记消失，模型恢复。
	if c := l.h.api.call("POST", "/hosts/"+target+"/ssh/connection", map[string]any{"auth_type": "password", "password": l.pass}, nil); c != 200 {
		t.Fatalf("重新填写凭据: HTTP %d", c)
	}
	if !l.credHolds(target) {
		t.Fatal("重新填写后，凭据应重新保存到网关上")
	}
	if v, _ := l.h.api.host(target); v.SSH == nil || v.SSH.CredMissing {
		t.Fatalf("缺失标记应消失: %+v", v.SSH)
	}
	if res := m.exec(target, "echo recovered", nil); res.str("state") != "succeeded" || !strings.Contains(res.obj("stdout").str("text"), "recovered") {
		t.Fatalf("恢复后: %v", res)
	}
}

// 目标不在网关本机的允许名单内：网关直接拒绝，且根本没有去连目标。
func TestRealSSHGatewayAllowListDeniesOtherTargets(t *testing.T) {
	l := newGWLab(t, "--ssh-gateway", "--ssh-allow", "192.0.2.0/24")
	_, st, code := l.probe(l.srv.Addr)
	if st != 403 || code != "ssh_target_denied" {
		t.Fatalf("应被允许名单拒绝: HTTP %d %s", st, code)
	}
	if n := l.srv.Connections(); n != 0 {
		t.Fatalf("被拒绝的目标不应被连接，实际连接数 %d", n)
	}
}

// 网关是 Agent 的标配能力，不带任何选项的守护进程就会声明它。
func TestRealSSHGatewayOnByDefault(t *testing.T) {
	l := newGWLab(t)
	if !l.hasGatewayCap() {
		t.Fatal("默认的守护进程应在 holder 帧里声明 ssh 能力")
	}
}

// 守护进程用 --no-ssh-gateway 关闭了网关能力：Hub 在发出任何帧之前就拒绝，目标同样没有被连接。
func TestRealSSHGatewayCanBeDisabled(t *testing.T) {
	l := newGWLab(t, "--no-ssh-gateway")
	if l.hasGatewayCap() {
		t.Fatal("带 --no-ssh-gateway 时不应声明 ssh 能力")
	}
	_, st, code := l.probe(l.srv.Addr)
	if st != 409 || code != "ssh_gateway_disabled" {
		t.Fatalf("应提示网关未开启: HTTP %d %s", st, code)
	}
	if n := l.srv.Connections(); n != 0 {
		t.Fatalf("目标不应被连接，实际连接数 %d", n)
	}
}

// 改地址不能绕过主机密钥固定：把已登记的主机改指向另一台服务器，网关带着旧公钥校验失败（用的是它自己保存的凭据），
// 对方一次认证都没有收到，库里的地址也保持不变。
func TestRealSSHGatewayAddressChangeCannotBypassHostKeyPinning(t *testing.T) {
	l := newGWLab(t, "--ssh-gateway")
	fp, st, _ := l.probe(l.srv.Addr)
	if st != 200 {
		t.Fatalf("探测失败: HTTP %d", st)
	}
	target := l.addHost(l.srv.Addr, fp, "inner server")

	other := sshdtest.Start(t, sshdtest.Options{User: "e2e", Password: l.pass})
	var out struct{ gwReply }
	c := l.h.api.call("POST", "/hosts/"+target+"/ssh/connection", map[string]any{"address": other.Addr}, &out)
	if c != 409 || out.Code != "ssh_host_key_mismatch" {
		t.Fatalf("改指向另一台服务器应因主机密钥不一致被拒绝: HTTP %d %s", c, out.Code)
	}
	if n := other.AuthAttempts(); n != 0 {
		t.Fatalf("主机密钥不一致时不应向对方发送任何认证，实际 %d 次", n)
	}
	v, _ := l.h.api.host(target)
	if v.SSH == nil || v.SSH.Address != l.srv.Addr {
		t.Fatalf("被拒绝的修改不应生效: %+v", v.SSH)
	}
}
