//go:build e2e && !windows

package e2e

import (
	"bytes"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/teacat99/RelayMesh/v2/internal/sshexec/sshdtest"
)

// SSH 后端的真实进程端到端：Hub 是独立的操作系统进程（含重启与强杀），模型一侧用真实的 MCP HTTP 调用，
// 远端是进程内的测试 sshd（命令由本机的 sh 真实执行）。

type sshLab struct {
	t    *testing.T
	h    *hub
	srv  *sshdtest.Server
	pass string
	key  string
	code string
	mcp  *mcpClient
}

func newSSHLab(t *testing.T, withKey bool) *sshLab {
	t.Helper()
	hubBin, _ := binaries(t)
	l := &sshLab{t: t, pass: "p" + randHex(10), key: randHex(32)}
	env := execOn()
	if withKey {
		env = append(env, "RELAYMESH_SSH_SECRET_KEY="+l.key)
	}
	l.h = startHub(t, hubBin, env...)
	l.srv = sshdtest.Start(t, sshdtest.Options{User: "e2e", Password: l.pass})
	return l
}

func (l *sshLab) probe() string {
	l.t.Helper()
	var out struct {
		Probe struct {
			Fingerprint string `json:"fingerprint"`
		} `json:"probe"`
	}
	if c := l.h.api.call("POST", "/ssh/probe", map[string]string{"address": l.srv.Addr}, &out); c != 200 || out.Probe.Fingerprint == "" {
		l.t.Fatalf("探测失败: HTTP %d", c)
	}
	return out.Probe.Fingerprint
}

// addHost 走完整的两步登记，并允许模型访问、签发一把绑定该主机的令牌。
func (l *sshLab) addHost() {
	l.t.Helper()
	var out struct {
		Host struct {
			MachineCode string `json:"machine_code"`
		} `json:"host"`
	}
	body := map[string]string{
		"remark": "ssh e2e", "address": l.srv.Addr, "user": "e2e", "auth_type": "password",
		"password": l.pass, "fingerprint": l.probe(),
	}
	if c := l.h.api.call("POST", "/ssh/hosts", body, &out); c != 200 || out.Host.MachineCode == "" {
		l.t.Fatalf("登记 SSH 主机失败: HTTP %d", c)
	}
	l.code = out.Host.MachineCode
	if c := l.h.api.call("POST", "/hosts/"+l.code+"/model", map[string]any{"allow_model": true}, nil); c != 200 {
		l.t.Fatalf("允许模型访问失败: HTTP %d", c)
	}
	var tok struct {
		Token string `json:"token"`
	}
	c := l.h.api.raw("POST", "/api/v1/credentials", map[string]any{
		"name": "ssh-e2e", "permissions": map[string]any{"terminal": true}, "terminal_hosts": []string{l.code},
	}, &tok, true)
	if c != 200 || tok.Token == "" {
		l.t.Fatalf("创建令牌失败: HTTP %d", c)
	}
	l.mcp = &mcpClient{t: l.t, base: l.h.base(), token: tok.Token, hc: &http.Client{Timeout: 90 * time.Second}}
}

// leaks 在 Hub 的数据库文件（含 WAL）与日志里查找敏感字节串。
func (l *sshLab) leaks(needles ...string) []string {
	files, _ := filepath.Glob(filepath.Join(l.h.dir, "relaymesh.db*"))
	files = append(files, l.h.proc.log)
	var found []string
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		for _, n := range needles {
			if bytes.Contains(b, []byte(n)) {
				found = append(found, filepath.Base(f))
			}
		}
	}
	return found
}

func TestRealSSHExecBasicsAndCredentialHygiene(t *testing.T) {
	l := newSSHLab(t, true)
	l.addHost()
	m := l.mcp

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

	marker := "ssh-" + randHex(4)
	res := m.exec(l.code, "echo "+marker+"; echo err-"+marker+" >&2; exit 2", nil)
	if res.str("state") != "failed" || res.num("exit_code") != 2 ||
		!strings.Contains(res.obj("stdout").str("text"), marker) || !strings.Contains(res.obj("stderr").str("text"), "err-"+marker) {
		t.Fatalf("结果 = %v", res)
	}

	// 没有指定工作目录时在家目录里运行（而不是会被定期清理的任务目录）。
	if pwd := m.exec(l.code, "pwd", nil).obj("stdout").str("text"); strings.TrimSpace(pwd) != l.srv.Home {
		t.Fatalf("默认工作目录 = %q，应为家目录 %q", pwd, l.srv.Home)
	}

	// 命令文本从不出现在 SSH 命令行里。
	for _, c := range l.srv.Commands() {
		if strings.Contains(c, marker) {
			t.Fatalf("命令文本泄漏到 SSH 命令行: %q", c)
		}
	}
	// 凭据只以密封形式存在：数据库（含 WAL）与日志里没有明文口令，也没有主密钥。
	if where := l.leaks(l.pass, l.key); len(where) > 0 {
		t.Fatalf("在 %v 里发现了明文口令或主密钥", where)
	}
	// 主机列表不回显任何凭据。
	var raw struct {
		Hosts []map[string]any `json:"hosts"`
	}
	l.h.api.call("GET", "/hosts", nil, &raw)
	if len(raw.Hosts) != 1 || raw.Hosts[0]["kind"] != "ssh" {
		t.Fatalf("主机列表 = %v", raw.Hosts)
	}
	if s := fmt.Sprint(raw.Hosts[0]); strings.Contains(s, l.pass) || strings.Contains(s, "v1:") {
		t.Fatalf("主机视图泄漏了凭据: %s", s)
	}
}

// Hub 优雅重启与被强杀之后，进行中的 SSH 运行都能被接管并取回结果。
func TestRealSSHExecSurvivesHubRestartAndKill(t *testing.T) {
	l := newSSHLab(t, true)
	l.addHost()
	m := l.mcp

	for name, restart := range map[string]func(){
		"优雅重启": func() { l.h.restart() },
		"强杀":   func() { l.h.proc.kill9(); l.h.start() },
	} {
		token := "done-" + randHex(4)
		res := m.exec(l.code, fmt.Sprintf("echo begin-%s; sleep 4; echo %s", token, token), map[string]any{"wait_seconds": 0})
		id := res.str("run_id")
		if res.str("state") != "running" {
			t.Fatalf("%s：应在运行: %v", name, res)
		}
		time.Sleep(800 * time.Millisecond) // 让它真正在远端启动
		restart()
		m.hc = &http.Client{Timeout: 90 * time.Second}
		m.base = l.h.base()

		st, isErr := m.run("status", id, map[string]any{"wait_seconds": 30})
		if isErr || st.str("state") != "succeeded" || !strings.Contains(st.obj("stdout").str("text"), token) {
			t.Fatalf("%s：重启后应取回运行结果: %v", name, st)
		}
		page, isErr := m.run("output", id, map[string]any{})
		if isErr || !strings.Contains(page.str("text"), "begin-"+token) {
			t.Fatalf("%s：重启后输出分页: %v", name, page)
		}
	}
}

func TestRealSSHHostKeyPinningRefusesImpostor(t *testing.T) {
	l := newSSHLab(t, true)
	l.addHost()
	m := l.mcp
	if res := m.exec(l.code, "echo ok", nil); res.str("state") != "succeeded" {
		t.Fatalf("%v", res)
	}

	oldFP := l.probe()
	l.srv.SetHostSigner(sshdtest.NewSigner(t))
	l.srv.DropConnections()
	attempts := l.srv.AuthAttempts()

	res := m.exec(l.code, "echo impostor", nil)
	if res.str("state") != "failed" || res.str("reason") != "ssh_host_key_mismatch" {
		t.Fatalf("密钥不一致应使运行失败: %v", res)
	}
	if got := l.srv.AuthAttempts(); got != attempts {
		t.Fatalf("向冒充者发出了认证尝试: %d → %d", attempts, got)
	}
	out, isErr := m.tool("terminal_exec", map[string]any{"target": l.code, "command": "echo again"})
	if !isErr || out.errCode() != "ssh_untrusted" {
		t.Fatalf("之后应在闸门处被拒绝: %v", out)
	}

	var probe struct {
		Probe struct {
			Fingerprint string `json:"fingerprint"`
		} `json:"probe"`
	}
	if c := l.h.api.call("POST", "/hosts/"+l.code+"/ssh/probe", nil, &probe); c != 200 || probe.Probe.Fingerprint == oldFP {
		t.Fatalf("探测新指纹: HTTP %d fp=%s", c, probe.Probe.Fingerprint)
	}
	if c := l.h.api.call("POST", "/hosts/"+l.code+"/ssh/rekey", map[string]string{"fingerprint": oldFP}, nil); c != 409 {
		t.Fatalf("旧指纹不能用来重新固定: HTTP %d", c)
	}
	if c := l.h.api.call("POST", "/hosts/"+l.code+"/ssh/rekey", map[string]string{"fingerprint": probe.Probe.Fingerprint}, nil); c != 200 {
		t.Fatalf("重新固定: HTTP %d", c)
	}
	if res := m.exec(l.code, "echo recovered", nil); res.str("state") != "succeeded" {
		t.Fatalf("重新固定后应恢复: %v", res)
	}
}

func TestRealSSHDisabledWithoutSecretKey(t *testing.T) {
	l := newSSHLab(t, false)
	var st struct {
		SSH struct {
			Enabled bool   `json:"enabled"`
			Reason  string `json:"reason"`
		} `json:"ssh"`
	}
	if c := l.h.api.raw("GET", "/api/v1/terminal/status", nil, &st, false); c != 200 || st.SSH.Enabled || st.SSH.Reason != "no_secret_key" {
		t.Fatalf("状态 = HTTP %d %+v", c, st)
	}
	if c := l.h.api.call("POST", "/ssh/probe", map[string]string{"address": l.srv.Addr}, nil); c != 409 {
		t.Fatalf("没有主密钥时探测应被拒绝: HTTP %d", c)
	}
	if l.srv.Connections() != 0 {
		t.Fatal("功能关闭时不得连接任何主机")
	}
}
