//go:build !windows

// 这组测试让真实的 sh 经进程内 SSH 服务器执行命令，并用 ps / awk / syscall.Kill 清理残留进程，只能在类 Unix 系统上运行。
// Hub 本身可以在 Windows 上编译与运行；这里只是不让这批测试挡住 Windows 下的 go vet / 测试编译。

package terminal

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/teacat99/RelayMesh/v2/internal/model"
	"github.com/teacat99/RelayMesh/v2/internal/sshexec/sshdtest"
	"github.com/teacat99/RelayMesh/v2/internal/store"
	"github.com/teacat99/RelayMesh/v2/internal/termexec"
)

// 这些测试把 Hub 的 ExecService 接到一台真实的（进程内）SSH 服务器上：远端命令由本机的 sh 真实执行，
// 因此覆盖完整链路：REST 登记 → 主机密钥固定 → 密封保存 → 启动 / 轮询 / 取消 / 读取输出 → 重启接管。

// sshTestPass 是只存在于测试进程与测试 sshd 之间的一次性口令。
const sshTestPass = "ssh-lab-pass-3f9a71"

func withSSHKey(key string) envOption { return func(o *Options, _ *testEnv) { o.SSHSecretKey = key } }

func mapOf(v any) map[string]any {
	m, _ := v.(map[string]any)
	return m
}

type sshFixture struct {
	env  *testEnv
	srv  *sshdtest.Server
	adm  []string
	code string
	key  string
	svc  *ExecService
	logs *bytes.Buffer
}

func newSSHFixture(t *testing.T, opts ...envOption) *sshFixture {
	t.Helper()
	srv := sshdtest.Start(t, sshdtest.Options{User: "tester", Password: sshTestPass})
	key := randHex(32)
	// 日志只有传了 withLogCapture() 才收（env.logs），并且那样的测试串行；其余测试并行，不碰全局日志输出。
	env := newTestEnv(t, append([]envOption{withExec(), withSSHKey(key)}, opts...)...)
	f := &sshFixture{env: env, srv: srv, adm: env.admin(), key: key, svc: env.hub.Exec(), logs: env.logs}
	f.code = f.addHost(t, "lab box")
	res := env.call("POST", "/api/v1/terminal/hosts/"+f.code+"/model", map[string]any{"allow_model": true}, f.adm...)
	if res.Status != 200 {
		t.Fatalf("允许模型访问失败: %d %s", res.Status, res.Raw)
	}
	return f
}

func (f *sshFixture) probe(t *testing.T) string {
	t.Helper()
	res := f.env.call("POST", "/api/v1/terminal/ssh/probe", map[string]string{"address": f.srv.Addr}, f.adm...)
	if res.Status != 200 {
		t.Fatalf("探测失败: %d %s", res.Status, res.Raw)
	}
	fp, _ := mapOf(res.Body["probe"])["fingerprint"].(string)
	if fp == "" {
		t.Fatalf("探测结果没有指纹: %s", res.Raw)
	}
	return fp
}

func (f *sshFixture) addBody(fp string) map[string]string {
	return map[string]string{
		"remark": "lab box", "address": f.srv.Addr, "user": "tester",
		"auth_type": "password", "password": sshTestPass, "fingerprint": fp,
	}
}

func (f *sshFixture) addHost(t *testing.T, remark string) string {
	t.Helper()
	body := f.addBody(f.probe(t))
	body["remark"] = remark
	res := f.env.call("POST", "/api/v1/terminal/ssh/hosts", body, f.adm...)
	if res.Status != 200 {
		t.Fatalf("登记 SSH 主机失败: %d %s", res.Status, res.Raw)
	}
	code, _ := mapOf(res.Body["host"])["machine_code"].(string)
	if code == "" {
		t.Fatalf("响应里没有机器码: %s", res.Raw)
	}
	return code
}

func (f *sshFixture) caller(codes ...string) termexec.Caller {
	if len(codes) == 0 {
		codes = []string{f.code}
	}
	cred := &model.MCPCredential{
		ID: 9, Name: "cursor-ssh", IsActive: true,
		Permissions:   model.Permissions{Terminal: true},
		TerminalHosts: model.StringList(codes),
	}
	return termexec.Caller{
		CredentialID: 9, CredentialName: "cursor-ssh", Credential: cred, Permissions: cred.Permissions,
		TokenString: execToken, Source: "db_credential", RemoteIP: "10.0.0.5", UserAgent: "TestClient/1.0",
	}
}

func (f *sshFixture) exec(t *testing.T, cmd string, mut func(*termexec.ExecRequest)) (*termexec.RunResult, error) {
	t.Helper()
	req := termexec.ExecRequest{Target: f.code, Command: cmd, WaitSet: true, WaitSeconds: 25, Description: "ssh test"}
	if mut != nil {
		mut(&req)
	}
	res, err := f.svc.Exec(context.Background(), f.caller(), req)
	if err != nil {
		return nil, err
	}
	return res.(*termexec.RunResult), nil
}

func (f *sshFixture) mustExec(t *testing.T, cmd string, mut func(*termexec.ExecRequest)) *termexec.RunResult {
	t.Helper()
	res, err := f.exec(t, cmd, mut)
	if err != nil {
		t.Fatalf("exec %q: %v", cmd, err)
	}
	return res
}

func (f *sshFixture) host(t *testing.T) *model.TerminalHost {
	t.Helper()
	rec, err := f.env.store.GetTerminalHost(context.Background(), f.code)
	if err != nil || rec == nil {
		t.Fatalf("读取主机: %v %v", rec, err)
	}
	return rec
}

func (f *sshFixture) dbRun(t *testing.T, id string) *model.TerminalRun {
	t.Helper()
	rec, err := f.env.store.GetTerminalRun(context.Background(), id)
	if err != nil || rec == nil {
		t.Fatalf("读取运行记录 %s: %v %v", id, rec, err)
	}
	return rec
}

// sshSleep 返回本次测试进程独有的 sleep 秒数：避免上一次失败遗留的进程干扰计数。
func sshSleep(offset int) int { return 8000 + os.Getpid()%1000*3 + offset }

func sshProcCount(t *testing.T, n int) int {
	t.Helper()
	out, _ := exec.Command("sh", "-c", fmt.Sprintf("ps -eo args | grep -Ec '^sleep %d$' || true", n)).Output()
	c, _ := strconv.Atoi(strings.TrimSpace(string(out)))
	return c
}

// killSleeps 按进程号结束本测试起的 sleep（先用 ps 精确挑出 pid，不做模式匹配式的批量 kill）。
func killSleeps(t *testing.T, n int) {
	t.Helper()
	out, _ := exec.Command("sh", "-c", fmt.Sprintf("ps -eo pid=,args= | awk '$2==\"sleep\" && $3==\"%d\" {print $1}'", n)).Output()
	for _, f := range strings.Fields(string(out)) {
		if pid, err := strconv.Atoi(f); err == nil && pid > 1 {
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
	}
}

// waitRunnerGone 等这次运行的看守脚本（`sh <任务目录>/run.sh`）退出。杀掉 sleep 之后看守脚本还要往任务目录里写
// exit_code / finished；这时测试的临时目录被清理，会偶发「unlinkat … directory not empty」（任务 #133）。
// 在 Go 里扫 ps 的输出，而不是 grep：用 sh -c 跑 grep 时，那条命令行自己就含有要找的路径。
func waitRunnerGone(t *testing.T, runID string, within time.Duration) {
	t.Helper()
	pattern := "/runs/" + runID + "/run.sh"
	eventually(t, within, "这次运行的看守脚本已退出", func() bool {
		out, _ := exec.Command("ps", "-eo", "args").Output()
		for _, ln := range strings.Split(string(out), "\n") {
			if strings.Contains(ln, pattern) {
				return false
			}
		}
		return true
	})
}

func waitProcs(t *testing.T, n, want int, within time.Duration, what string) {
	t.Helper()
	eventually(t, within, what, func() bool { return sshProcCount(t, n) == want })
}

// ---------- 登记 ----------

func TestSSHAddFlowAndHostView(t *testing.T) {
	f := newSSHFixture(t)
	want := ssh.FingerprintSHA256(f.srv.HostKey())

	rec := f.host(t)
	if rec.Kind != model.HostKindSSH || rec.Status != "enabled" || rec.AllowModel != true {
		t.Fatalf("主机记录 = %+v", rec)
	}
	if rec.SSHFingerprint != want || rec.SSHAddr != f.srv.Addr || rec.SSHUser != "tester" || rec.SSHAuth != "password" {
		t.Fatalf("连接信息 = %+v（期望指纹 %s）", rec, want)
	}
	if !strings.HasPrefix(rec.SSHSecret, "v1:") || strings.Contains(rec.SSHSecret, sshTestPass) {
		t.Fatalf("凭据必须密封保存，得到 %q", rec.SSHSecret)
	}
	if rec.Hostname == "" || rec.OS == "" || rec.SSHOKAt == nil {
		t.Fatalf("登记时应探测并记录主机环境: %+v", rec)
	}
	if !strings.HasPrefix(rec.MachineCode, "RM-") {
		t.Fatalf("机器码 = %q", rec.MachineCode)
	}

	list := f.env.call("GET", "/api/v1/terminal/hosts", nil, "Authorization", humanAuth(testUser))
	if list.Status != 200 {
		t.Fatalf("列表: %d %s", list.Status, list.Raw)
	}
	var view map[string]any
	for _, h := range list.Body["hosts"].([]any) {
		if mapOf(h)["machine_code"] == f.code {
			view = mapOf(h)
		}
	}
	if view == nil {
		t.Fatalf("列表里没有 SSH 主机: %s", list.Raw)
	}
	s := mapOf(view["ssh"])
	if view["kind"] != "ssh" || view["online"] != true || s["fingerprint"] != want || s["has_secret"] != true || s["address"] != f.srv.Addr {
		t.Fatalf("主机视图 = %+v", view)
	}
	if strings.Contains(list.Raw, sshTestPass) || strings.Contains(list.Raw, rec.SSHSecret) {
		t.Fatal("主机列表不得包含口令或密封后的凭据")
	}

	var added *model.TerminalAuditLog
	for _, l := range f.env.auditEvents(EvSSHAdd) {
		cp := l
		added = &cp
	}
	if added == nil || added.MachineCode != f.code || !strings.Contains(added.Detail, want) || strings.Contains(added.Detail, sshTestPass) {
		t.Fatalf("ssh_host_add 审计 = %+v", added)
	}
}

func TestSSHAddRejectsBadInput(t *testing.T) {
	f := newSSHFixture(t)
	fp := f.probe(t)
	post := func(mut func(m map[string]string)) apiResult {
		body := f.addBody(fp)
		mut(body)
		return f.env.call("POST", "/api/v1/terminal/ssh/hosts", body, f.adm...)
	}
	cases := []struct {
		name   string
		mut    func(m map[string]string)
		status int
		code   string
	}{
		{"缺少指纹", func(m map[string]string) { m["fingerprint"] = "" }, 400, "bad_request"},
		{"指纹不一致", func(m map[string]string) { m["fingerprint"] = "SHA256:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA" }, 409, "ssh_fingerprint_changed"},
		{"口令错误", func(m map[string]string) { m["password"] = "definitely-wrong-pass" }, 422, "ssh_auth_failed"},
		{"用户名非法", func(m map[string]string) { m["user"] = "ro ot;id" }, 400, "bad_request"},
		{"地址非法", func(m map[string]string) { m["address"] = "bad address" }, 400, "bad_request"},
		{"认证方式非法", func(m map[string]string) { m["auth_type"] = "magic" }, 400, "bad_request"},
		{"缺少口令", func(m map[string]string) { m["password"] = "" }, 400, "bad_request"},
	}
	for _, c := range cases {
		res := post(c.mut)
		if res.Status != c.status || res.str("code") != c.code {
			t.Errorf("%s: 状态 %d code=%q（期望 %d %s）: %s", c.name, res.Status, res.str("code"), c.status, c.code, res.Raw)
		}
		if strings.Contains(res.Raw, sshTestPass) || strings.Contains(res.Raw, "definitely-wrong-pass") {
			t.Errorf("%s: 错误响应里出现了口令", c.name)
		}
	}
	hosts, _ := f.env.store.ListTerminalHosts(context.Background())
	n := 0
	for _, h := range hosts {
		if h.IsSSH() {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("失败的登记不应留下记录，SSH 主机数 = %d", n)
	}
}

func TestSSHAddKeyAuthWithPassphrase(t *testing.T) {
	pemText, signer := sshKeyPEM(t, "pass-phrase-77")
	srv := sshdtest.Start(t, sshdtest.Options{User: "tester", AuthorizedKeys: []ssh.PublicKey{signer.PublicKey()}})
	env := newTestEnv(t, withExec(), withSSHKey(randHex(32)))
	adm := env.admin()
	probe := env.call("POST", "/api/v1/terminal/ssh/probe", map[string]string{"address": srv.Addr}, adm...)
	fp, _ := mapOf(probe.Body["probe"])["fingerprint"].(string)

	body := map[string]string{"address": srv.Addr, "user": "tester", "auth_type": "key", "private_key": pemText, "fingerprint": fp}
	res := env.call("POST", "/api/v1/terminal/ssh/hosts", body, adm...)
	if res.Status != 400 || res.str("code") != "ssh_passphrase_required" {
		t.Fatalf("加密私钥缺口令应被拒绝: %d %s", res.Status, res.Raw)
	}
	body["passphrase"] = "pass-phrase-77"
	res = env.call("POST", "/api/v1/terminal/ssh/hosts", body, adm...)
	if res.Status != 200 {
		t.Fatalf("带口令的私钥应登记成功: %d %s", res.Status, res.Raw)
	}
	if strings.Contains(res.Raw, "PRIVATE KEY") || strings.Contains(res.Raw, "pass-phrase-77") {
		t.Fatal("响应不得回显私钥或口令")
	}
}

func sshKeyPEM(t *testing.T, passphrase string) (string, ssh.Signer) {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	var block *pem.Block
	if passphrase != "" {
		block, err = ssh.MarshalPrivateKeyWithPassphrase(priv, "", []byte(passphrase))
	} else {
		block, err = ssh.MarshalPrivateKey(priv, "")
	}
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(block)), signer
}

func TestSSHRESTRequiresUnlockAndOpensHubLocalTerminal(t *testing.T) {
	f := newSSHFixture(t)
	login := []string{"Authorization", humanAuth(testUser)}
	for _, p := range []struct{ method, path string }{
		{"POST", "/api/v1/terminal/ssh/probe"},
		{"POST", "/api/v1/terminal/ssh/hosts"},
		{"POST", "/api/v1/terminal/hosts/" + f.code + "/ssh/test"},
		{"POST", "/api/v1/terminal/hosts/" + f.code + "/ssh/probe"},
		{"POST", "/api/v1/terminal/hosts/" + f.code + "/ssh/connection"},
		{"POST", "/api/v1/terminal/hosts/" + f.code + "/ssh/rekey"},
	} {
		res := f.env.call(p.method, p.path, map[string]string{"address": f.srv.Addr}, login...)
		if res.Status != 403 || res.str("code") != "unlock_required" {
			t.Errorf("%s 未解锁应返回 403 unlock_required，得到 %d %s", p.path, res.Status, res.Raw)
		}
		anon := f.env.call(p.method, p.path, map[string]string{"address": f.srv.Addr})
		if anon.Status != 401 && anon.Status != 403 {
			t.Errorf("%s 匿名访问应被拒绝，得到 %d", p.path, anon.Status)
		}
	}

	// Hub 直连的 SSH 主机也有人工终端（会话持有在 Hub 本地，细节见 localterm_test.go）：未解锁一样被拒，解锁后可以打开。
	denied := f.env.call("POST", "/api/v1/terminal/hosts/"+f.code+"/sessions", map[string]any{"name": "x"}, login...)
	if denied.Status != 403 || denied.str("code") != "unlock_required" {
		t.Fatalf("未解锁不能打开终端: %d %s", denied.Status, denied.Raw)
	}
	res := f.env.call("POST", "/api/v1/terminal/hosts/"+f.code+"/sessions", map[string]any{"name": "x"}, f.adm...)
	if res.Status != 200 || mapOf(res.Body["session"])["shell"] != "ssh" {
		t.Fatalf("Hub 直连的 SSH 主机应能打开人工终端: %d %s", res.Status, res.Raw)
	}
	// 终端记录与文件传输依旧只属于装了 Agent 的主机
	rec := f.env.call("GET", "/api/v1/terminal/hosts/"+f.code+"/sessions/"+mapOf(res.Body["session"])["sid"].(string)+"/record", nil, f.adm...)
	if rec.Status != 404 && rec.Status != 409 {
		t.Fatalf("Hub 直连的终端没有记录可查: %d %s", rec.Status, rec.Raw)
	}
}

// ---------- 执行 ----------

func TestSSHExecHappyPath(t *testing.T) {
	f := newSSHFixture(t)
	res := f.mustExec(t, "echo hello; echo oops >&2; pwd", nil)
	if res.State != model.RunSucceeded || res.ExitCode == nil || *res.ExitCode != 0 {
		t.Fatalf("结果 = %+v", res)
	}
	if res.Stdout == nil || !strings.HasPrefix(res.Stdout.Text, "hello\n") || res.Stdout.Truncated {
		t.Fatalf("stdout = %+v", res.Stdout)
	}
	if res.Stderr == nil || res.Stderr.Text != "oops\n" {
		t.Fatalf("stderr = %+v", res.Stderr)
	}
	// 必须恰好是家目录：任务目录在家目录之下，用「包含」判断会放过「在任务目录里运行」的错误。
	if lines := strings.Split(res.Stdout.Text, "\n"); len(lines) < 2 || lines[1] != f.srv.Home {
		t.Fatalf("默认工作目录应恰好是家目录 %s，输出 = %q", f.srv.Home, res.Stdout.Text)
	}

	rec := f.dbRun(t, res.RunID)
	if rec.Backend != model.HostKindSSH || rec.State != model.RunSucceeded || rec.StartedAt == nil || rec.EndedAt == nil || rec.Target != f.code {
		t.Fatalf("记录 = %+v", rec)
	}
	if !strings.Contains(rec.PreviewHead, "hello") {
		t.Fatalf("预览 = %q", rec.PreviewHead)
	}

	// 命令文本只经 stdin（base64）传输：绝不出现在 SSH 的 exec 命令行里。
	for _, c := range f.srv.Commands() {
		if strings.Contains(c, "hello") || strings.Contains(c, "oops") || strings.Contains(c, "echo") {
			t.Fatalf("命令文本泄漏到 SSH 命令行: %q", c)
		}
	}
	if f.svc.liveRun(res.RunID) != nil {
		t.Fatal("终态后不应留在活动索引里")
	}
}

func TestSSHExecExitCodeCwdAndShell(t *testing.T) {
	f := newSSHFixture(t)

	res := f.mustExec(t, "echo fail >&2; exit 3", nil)
	if res.State != model.RunFailed || res.ExitCode == nil || *res.ExitCode != 3 || res.Stderr == nil || res.Stderr.Text != "fail\n" {
		t.Fatalf("非零退出码 = %+v", res)
	}

	dir := t.TempDir()
	res = f.mustExec(t, "pwd", func(r *termexec.ExecRequest) { r.Cwd = dir })
	if res.State != model.RunSucceeded || res.Stdout == nil || strings.TrimSpace(res.Stdout.Text) != dir {
		t.Fatalf("cwd = %+v", res)
	}

	res = f.mustExec(t, "pwd", func(r *termexec.ExecRequest) { r.Cwd = dir + "/不存在" })
	if res.State != model.RunFailed || res.Reason != "bad_cwd" {
		t.Fatalf("不存在的 cwd 应失败为 bad_cwd: %+v", res)
	}

	res = f.mustExec(t, "echo shell-ok", func(r *termexec.ExecRequest) { r.Shell = "sh" })
	if res.State != model.RunSucceeded || f.dbRun(t, res.RunID).Shell != "sh" {
		t.Fatalf("shell=sh: %+v shell=%q", res, f.dbRun(t, res.RunID).Shell)
	}
	auto := f.mustExec(t, "echo auto", nil)
	if sh := f.dbRun(t, auto.RunID).Shell; sh != "bash" && sh != "sh" {
		t.Fatalf("自动选择的 shell = %q", sh)
	}
	for _, bad := range []string{"cmd", "powershell", "pwsh"} {
		_, err := f.exec(t, "echo x", func(r *termexec.ExecRequest) { r.Shell = bad })
		wantCode(t, err, termexec.CodeBadRequest)
	}
}

func TestSSHExecTimeout(t *testing.T) {
	f := newSSHFixture(t)
	n := sshSleep(0)
	start := time.Now()
	res := f.mustExec(t, fmt.Sprintf("sleep %d", n), func(r *termexec.ExecRequest) { r.TimeoutSeconds = 1 })
	if res.State != model.RunTimedOut {
		t.Fatalf("超时应为 timed_out: %+v", res)
	}
	if took := time.Since(start); took > 15*time.Second {
		t.Fatalf("超时收尾太慢: %v", took)
	}
	waitProcs(t, n, 0, 8*time.Second, "超时后远端进程被清理")
}

func TestSSHExecCancel(t *testing.T) {
	f := newSSHFixture(t)
	n := sshSleep(1)
	res := f.mustExec(t, fmt.Sprintf("sleep %d", n), func(r *termexec.ExecRequest) { r.WaitSeconds = 0 })
	if res.State != model.RunRunning {
		t.Fatalf("应立即转后台: %+v", res)
	}
	waitProcs(t, n, 1, 8*time.Second, "远端进程已启动")

	out, err := f.svc.Run(context.Background(), f.caller(), termexec.RunRequest{Action: "cancel", RunID: res.RunID})
	if err != nil {
		t.Fatal(err)
	}
	got := out.(*termexec.RunResult)
	if got.State != model.RunCancelled {
		t.Fatalf("取消后的状态 = %+v", got)
	}
	waitProcs(t, n, 0, 8*time.Second, "取消后远端进程被终止")
	if rec := f.dbRun(t, res.RunID); rec.State != model.RunCancelled {
		t.Fatalf("记录 = %+v", rec)
	}
}

func TestSSHExecIgnoredTermIsKilled(t *testing.T) {
	f := newSSHFixture(t)
	n := sshSleep(2)
	res := f.mustExec(t, fmt.Sprintf("trap '' TERM; while true; do sleep %d; done", n), func(r *termexec.ExecRequest) {
		r.WaitSeconds = 0
		r.Shell = "sh"
	})
	waitProcs(t, n, 1, 8*time.Second, "远端进程已启动")
	start := time.Now()
	if _, err := f.svc.Run(context.Background(), f.caller(), termexec.RunRequest{Action: "cancel", RunID: res.RunID}); err != nil {
		t.Fatal(err)
	}
	eventually(t, 20*time.Second, "忽略 TERM 的命令最终被终止", func() bool {
		rec, _ := f.env.store.GetTerminalRun(context.Background(), res.RunID)
		return rec != nil && model.IsFinalRunState(rec.State)
	})
	if time.Since(start) > 15*time.Second {
		t.Fatalf("升级到 KILL 不应太慢: %v", time.Since(start))
	}
	waitProcs(t, n, 0, 8*time.Second, "KILL 后远端进程消失")
}

func TestSSHExecLargeOutputIsPagedFromRemote(t *testing.T) {
	f := newSSHFixture(t)
	const total = 150000
	res := f.mustExec(t, fmt.Sprintf("head -c %d /dev/zero | tr '\\0' 'x'; echo END >&2", total), nil)
	if res.State != model.RunSucceeded || res.Stdout == nil {
		t.Fatalf("结果 = %+v", res)
	}
	if res.Stdout.Bytes != total || !res.Stdout.Truncated || res.Stdout.NextOffset <= 0 || int64(len(res.Stdout.Text)) != res.Stdout.NextOffset {
		t.Fatalf("stdout = bytes %d truncated=%v next=%d textlen=%d", res.Stdout.Bytes, res.Stdout.Truncated, res.Stdout.NextOffset, len(res.Stdout.Text))
	}
	if res.Stderr == nil || res.Stderr.Text != "END\n" {
		t.Fatalf("stderr = %+v", res.Stderr)
	}

	out, err := f.svc.Run(context.Background(), f.caller(), termexec.RunRequest{
		Action: "output", RunID: res.RunID, Stream: "stdout", Offset: res.Stdout.NextOffset, Limit: 256 << 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	page := out.(*termexec.OutputPage)
	if !page.EOF || int64(len(page.Text)) != total-res.Stdout.NextOffset || page.TotalBytes != total {
		t.Fatalf("第二页 = eof=%v textlen=%d total=%d next=%d", page.EOF, len(page.Text), page.TotalBytes, page.NextOffset)
	}
	if strings.Trim(page.Text, "x") != "" {
		t.Fatal("读到的内容不是 x")
	}

	// 完整输出不入库：库里只有脱敏预览。
	rec := f.dbRun(t, res.RunID)
	if len(rec.PreviewHead) > 5<<10 || len(rec.PreviewTail) > 3<<10 {
		t.Fatalf("预览过大: head=%d tail=%d", len(rec.PreviewHead), len(rec.PreviewTail))
	}
	if rec.StdoutBytes != total {
		t.Fatalf("stdout_bytes = %d", rec.StdoutBytes)
	}
}

func TestSSHExecRedactsSecretsInStoredPreview(t *testing.T) {
	f := newSSHFixture(t)
	res := f.mustExec(t, "echo 'token=abcdef0123456789abcdef0123456789'", nil)
	rec := f.dbRun(t, res.RunID)
	if strings.Contains(rec.PreviewHead, "abcdef0123456789abcdef0123456789") || strings.Contains(rec.Command, "abcdef0123456789abcdef0123456789") {
		t.Fatalf("库里的命令与预览必须脱敏: cmd=%q preview=%q", rec.Command, rec.PreviewHead)
	}
}

func TestSSHExecIdempotencyAndTargets(t *testing.T) {
	f := newSSHFixture(t)

	tr, err := f.svc.Targets(context.Background(), f.caller(), "")
	if err != nil {
		t.Fatal(err)
	}
	targets := tr.(*termexec.TargetsResult).Targets
	if len(targets) != 1 || targets[0].Backend != "ssh" || !targets[0].Usable || !targets[0].Online || targets[0].PathSep != "/" {
		t.Fatalf("targets = %+v", targets)
	}

	first := f.mustExec(t, "echo once", func(r *termexec.ExecRequest) { r.IdempotencyKey = "same-key-1" })
	again := f.mustExec(t, "echo once", func(r *termexec.ExecRequest) { r.IdempotencyKey = "same-key-1" })
	if first.RunID != again.RunID {
		t.Fatalf("相同的幂等键应返回同一次运行: %s vs %s", first.RunID, again.RunID)
	}
}

func TestSSHExecGatesAndTargetResolution(t *testing.T) {
	f := newSSHFixture(t)

	// 模型只能用 target 指定「已登记」的主机（机器码 / 备注 / 主机名）：地址不是合法目标。
	for _, target := range []string{f.srv.Addr, "127.0.0.1", "localhost:22", "tester@" + f.srv.Addr} {
		_, err := f.exec(t, "echo x", func(r *termexec.ExecRequest) { r.Target = target })
		wantCode(t, err, termexec.CodeHostNotFound)
	}
	if res := f.mustExec(t, "echo x", func(r *termexec.ExecRequest) { r.Target = "lab box" }); res.State != model.RunSucceeded {
		t.Fatalf("按备注解析目标: %+v", res)
	}

	// G2：管理员收回模型访问。
	res := f.env.call("POST", "/api/v1/terminal/hosts/"+f.code+"/model", map[string]any{"allow_model": false}, f.adm...)
	if res.Status != 200 {
		t.Fatal(res.Raw)
	}
	_, err := f.exec(t, "echo x", nil)
	wantCode(t, err, termexec.CodeHostOff)
	res = f.env.call("POST", "/api/v1/terminal/hosts/"+f.code+"/model", map[string]any{"allow_model": true, "exec_mode": "readonly"}, f.adm...)
	if res.Status != 200 {
		t.Fatal(res.Raw)
	}
	_, err = f.exec(t, "echo x", nil)
	wantCode(t, err, termexec.CodeReadonly)
}

func TestSSHApproveModeDispatchesOnApproval(t *testing.T) {
	f := newSSHFixture(t)
	res := f.env.call("POST", "/api/v1/terminal/hosts/"+f.code+"/model", map[string]any{"allow_model": true, "exec_mode": "approve"}, f.adm...)
	if res.Status != 200 {
		t.Fatal(res.Raw)
	}
	before := f.srv.Connections()
	got := f.mustExec(t, "echo approved", func(r *termexec.ExecRequest) { r.WaitSeconds = 0 })
	if got.State != model.RunAwaitingApproval {
		t.Fatalf("应等待批准: %+v", got)
	}
	time.Sleep(300 * time.Millisecond)
	if f.srv.Connections() != before {
		t.Fatal("批准之前不得连接远端主机")
	}
	if _, err := f.svc.Approve(context.Background(), got.RunID, testUser, "127.0.0.1"); err != nil {
		t.Fatal(err)
	}
	eventually(t, 15*time.Second, "批准后命令在远端执行完毕", func() bool {
		return f.dbRun(t, got.RunID).State == model.RunSucceeded
	})
	if rec := f.dbRun(t, got.RunID); !strings.Contains(rec.PreviewHead, "approved") {
		t.Fatalf("预览 = %q", rec.PreviewHead)
	}
}

// ---------- 主机密钥固定 ----------

func TestSSHHostKeyChangeIsRefusedAndNeverRepinnedAutomatically(t *testing.T) {
	f := newSSHFixture(t)
	oldFP := ssh.FingerprintSHA256(f.srv.HostKey())
	if res := f.mustExec(t, "echo before", nil); res.State != model.RunSucceeded {
		t.Fatalf("%+v", res)
	}

	// 主机被换了密钥（重装，或者有人冒充）：所有已建立的连接断开，之后的连接看到新密钥。
	f.srv.SetHostSigner(sshdtest.NewSigner(t))
	f.srv.DropConnections()
	attempts := f.srv.AuthAttempts()

	res := f.mustExec(t, "echo after", nil)
	if res.State != model.RunFailed || res.Reason != "ssh_host_key_mismatch" || !strings.Contains(res.Message, "主机密钥") {
		t.Fatalf("密钥不一致应使运行失败并给出原因: %+v", res)
	}
	if got := f.srv.AuthAttempts(); got != attempts {
		t.Fatalf("向冒充者发出了认证尝试: %d → %d", attempts, got)
	}
	eventually(t, 3*time.Second, "主机被标记为密钥不一致", func() bool {
		return f.host(t).SSHKeyState == model.SSHKeyMismatch
	})
	if got := f.host(t).SSHFingerprint; got != oldFP {
		t.Fatalf("固定的指纹不得被自动更新: %s → %s", oldFP, got)
	}

	// 之后在闸门处直接拒绝，不再尝试连接。
	conns := f.srv.Connections()
	_, err := f.exec(t, "echo again", nil)
	wantCode(t, err, termexec.CodeSSHUntrusted)
	tr, _ := f.svc.Targets(context.Background(), f.caller(), "")
	if tg := tr.(*termexec.TargetsResult).Targets; len(tg) != 1 || tg[0].Usable || tg[0].Blocked != termexec.CodeSSHUntrusted {
		t.Fatalf("targets = %+v", tg)
	}
	if f.srv.Connections() != conns {
		t.Fatal("被拒绝的请求不应产生连接")
	}
	// 连接测试同样不会发送凭据。
	test := f.env.call("POST", "/api/v1/terminal/hosts/"+f.code+"/ssh/test", nil, f.adm...)
	if test.Status != 200 || mapOf(test.Body["result"])["ok"] != false || mapOf(test.Body["result"])["code"] != "ssh_host_key_mismatch" {
		t.Fatalf("测试结果 = %d %s", test.Status, test.Raw)
	}
	if got := f.srv.AuthAttempts(); got != attempts {
		t.Fatalf("测试不得向冒充者发送凭据: %d → %d", attempts, got)
	}

	// 重新固定：必须重新探测并由管理员核对新指纹；拿旧指纹（或错误的指纹）确认一律拒绝。
	probe := f.env.call("POST", "/api/v1/terminal/hosts/"+f.code+"/ssh/probe", nil, f.adm...)
	newFP, _ := mapOf(probe.Body["probe"])["fingerprint"].(string)
	if probe.Status != 200 || newFP == "" || newFP == oldFP {
		t.Fatalf("探测 = %d %s", probe.Status, probe.Raw)
	}
	bad := f.env.call("POST", "/api/v1/terminal/hosts/"+f.code+"/ssh/rekey", map[string]string{"fingerprint": oldFP}, f.adm...)
	if bad.Status != 409 || bad.str("code") != "ssh_fingerprint_changed" {
		t.Fatalf("旧指纹不能用来重新固定: %d %s", bad.Status, bad.Raw)
	}
	if f.host(t).SSHKeyState != model.SSHKeyMismatch {
		t.Fatal("失败的重新固定不得清除 mismatch")
	}
	ok := f.env.call("POST", "/api/v1/terminal/hosts/"+f.code+"/ssh/rekey", map[string]string{"fingerprint": newFP}, f.adm...)
	if ok.Status != 200 {
		t.Fatalf("重新固定: %d %s", ok.Status, ok.Raw)
	}
	if rec := f.host(t); rec.SSHKeyState != "" || rec.SSHFingerprint != newFP {
		t.Fatalf("重新固定后的记录 = %+v", rec)
	}
	if res := f.mustExec(t, "echo recovered", nil); res.State != model.RunSucceeded {
		t.Fatalf("重新固定后应恢复: %+v", res)
	}
	if n := len(f.env.auditEvents(EvSSHRekey)); n != 1 {
		t.Fatalf("应有一条 ssh_host_rekey 审计，得到 %d", n)
	}
}

func TestSSHUpdateConnectionVerifiesBeforeSaving(t *testing.T) {
	f := newSSHFixture(t)
	before := f.host(t)

	res := f.env.call("POST", "/api/v1/terminal/hosts/"+f.code+"/ssh/connection",
		map[string]string{"auth_type": "password", "password": "wrong-new-password"}, f.adm...)
	if res.Status != 422 || res.str("code") != "ssh_auth_failed" {
		t.Fatalf("错误的新口令应被拒绝: %d %s", res.Status, res.Raw)
	}
	if after := f.host(t); after.SSHSecret != before.SSHSecret {
		t.Fatal("验证失败时不得覆盖已保存的凭据")
	}
	if strings.Contains(res.Raw, "wrong-new-password") {
		t.Fatal("错误响应不得回显口令")
	}

	// 改到另一台机器（不同的主机密钥）：用已固定的密钥验证，所以拿不到「改地址绕过固定」的机会。
	other := sshdtest.Start(t, sshdtest.Options{User: "tester", Password: sshTestPass})
	res = f.env.call("POST", "/api/v1/terminal/hosts/"+f.code+"/ssh/connection", map[string]string{"address": other.Addr}, f.adm...)
	if res.Status != 409 || res.str("code") != "ssh_host_key_mismatch" {
		t.Fatalf("改到密钥不同的地址应被拒绝: %d %s", res.Status, res.Raw)
	}
	if after := f.host(t); after.SSHAddr != before.SSHAddr {
		t.Fatal("地址不应被修改")
	}
	if other.AuthAttempts() != 0 {
		t.Fatal("不得向密钥不符的服务器发送凭据")
	}

	res = f.env.call("POST", "/api/v1/terminal/hosts/"+f.code+"/ssh/connection", map[string]string{}, f.adm...)
	if res.Status != 400 {
		t.Fatalf("没有任何修改应返回 400: %d %s", res.Status, res.Raw)
	}
}

func TestSSHTestEndpointReportsFacts(t *testing.T) {
	f := newSSHFixture(t)
	res := f.env.call("POST", "/api/v1/terminal/hosts/"+f.code+"/ssh/test", nil, f.adm...)
	r := mapOf(res.Body["result"])
	if res.Status != 200 || r["ok"] != true || r["home"] != f.srv.Home || r["user"] == "" || r["os"] == "" {
		t.Fatalf("测试结果 = %d %s", res.Status, res.Raw)
	}
	if strings.Contains(res.Raw, sshTestPass) {
		t.Fatal("响应不得包含口令")
	}
	if n := len(f.env.auditEvents(EvSSHTest)); n != 1 {
		t.Fatalf("应有一条 ssh_host_test 审计，得到 %d", n)
	}

	unknown := f.env.call("POST", "/api/v1/terminal/hosts/RM-ABCD-ABCD-ABCD-ABCD/ssh/test", nil, f.adm...)
	if unknown.Status != 404 {
		t.Fatalf("未知主机应返回 404: %d %s", unknown.Status, unknown.Raw)
	}
}

// ---------- 主密钥 ----------

func TestSSHBackendDisabledWithoutValidKey(t *testing.T) {
	for name, key := range map[string]string{"未配置": "", "过弱": "changeme-changeme-changeme-1"} {
		t.Run(name, func(t *testing.T) {
			env := newTestEnv(t, withExec(), withSSHKey(key))
			adm := env.admin()

			st := env.call("GET", "/api/v1/terminal/status", nil, "Authorization", humanAuth(testUser))
			s := mapOf(st.Body["ssh"])
			want := "no_secret_key"
			if key != "" {
				want = "weak_secret_key"
			}
			if st.Status != 200 || s["enabled"] != false || s["reason"] != want || !strings.Contains(fmt.Sprint(s["message"]), "RELAYMESH_SSH_SECRET_KEY") {
				t.Fatalf("状态 = %d %s", st.Status, st.Raw)
			}
			for _, call := range []struct{ path string }{{"/api/v1/terminal/ssh/probe"}, {"/api/v1/terminal/ssh/hosts"}} {
				res := env.call("POST", call.path, map[string]string{"address": "127.0.0.1:2222"}, adm...)
				if res.Status != 409 || res.str("code") != termexec.CodeSSHOff {
					t.Fatalf("%s: %d %s", call.path, res.Status, res.Raw)
				}
			}

			// 库里已有的 SSH 主机（例如之前配置过密钥）在密钥缺失时只是被阻断，不会报错或崩溃。
			rec := sshHostRow("RM-5555-5555-5555-5555")
			if err := env.store.CreateSSHHost(context.Background(), rec); err != nil {
				t.Fatal(err)
			}
			cred := &model.MCPCredential{ID: 3, Name: "c", IsActive: true, Permissions: model.Permissions{Terminal: true}, TerminalHosts: model.StringList{rec.MachineCode}}
			c := termexec.Caller{CredentialID: 3, CredentialName: "c", Credential: cred, Permissions: cred.Permissions, TokenString: execToken, Source: "db_credential"}
			tr, err := env.hub.Exec().Targets(context.Background(), c, "")
			if err != nil {
				t.Fatal(err)
			}
			tg := tr.(*termexec.TargetsResult).Targets
			if len(tg) != 1 || tg[0].Usable || tg[0].Blocked != termexec.CodeSSHOff {
				t.Fatalf("targets = %+v", tg)
			}
			_, err = env.hub.Exec().Exec(context.Background(), c, termexec.ExecRequest{Target: rec.MachineCode, Command: "echo x"})
			wantCode(t, err, termexec.CodeSSHOff)
		})
	}
}

func sshHostRow(code string) *model.TerminalHost {
	return &model.TerminalHost{
		MachineCode: code, Kind: model.HostKindSSH, Status: "enabled", AllowModel: true,
		SSHAddr: "127.0.0.1:1", SSHUser: "tester", SSHAuth: "password", SSHSecret: "v1:not-a-real-seal",
		SSHHostKeyType: "ssh-ed25519", SSHHostKey: "AAAA", SSHFingerprint: "SHA256:placeholder",
	}
}

func TestSSHSealedCredentialIsBoundToItsHost(t *testing.T) {
	f := newSSHFixture(t)
	orig := f.host(t)

	// 把 A 主机的密封凭据原样复制到 B 主机：AAD 绑定了机器码，所以 B 解不开。
	other := sshHostRow("RM-6666-6666-6666-6666")
	other.SSHAddr, other.SSHSecret = orig.SSHAddr, orig.SSHSecret
	other.SSHHostKeyType, other.SSHHostKey, other.SSHFingerprint = orig.SSHHostKeyType, orig.SSHHostKey, orig.SSHFingerprint
	if err := f.env.store.CreateSSHHost(context.Background(), other); err != nil {
		t.Fatal(err)
	}
	res, err := f.svc.Exec(context.Background(), f.caller(other.MachineCode), termexec.ExecRequest{
		Target: other.MachineCode, Command: "echo stolen", WaitSet: true, WaitSeconds: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	r := res.(*termexec.RunResult)
	if r.State != model.RunFailed || r.Reason != "ssh_secret_unreadable" {
		t.Fatalf("复制来的密封凭据必须无法使用: %+v", r)
	}
	if r.Stdout != nil {
		t.Fatalf("不应有输出: %+v", r.Stdout)
	}
}

// ---------- 凭据不外泄 ----------

func TestSSHCredentialsNeverLeak(t *testing.T) {
	f := newSSHFixture(t, withLogCapture())
	if f.logs == nil { // nil 缓冲的 String() 返回 "<nil>"，日志检查会悄悄空过
		t.Fatal("夹具没有开日志捕获")
	}

	// 走一遍会产生各类输出的操作：成功、失败、测试、改连接信息的失败、主机列表、运行事件。
	f.mustExec(t, "echo ok", nil)
	f.mustExec(t, "exit 4", nil)
	f.env.call("POST", "/api/v1/terminal/hosts/"+f.code+"/ssh/test", nil, f.adm...)
	f.env.call("POST", "/api/v1/terminal/hosts/"+f.code+"/ssh/connection", map[string]string{"auth_type": "password", "password": "leak-probe-password-9"}, f.adm...)
	f.env.call("POST", "/api/v1/terminal/ssh/hosts", f.addBody("SHA256:wrong"), f.adm...)

	needles := []string{sshTestPass, "leak-probe-password-9", f.key}
	check := func(what, text string) {
		t.Helper()
		for _, n := range needles {
			if n != "" && strings.Contains(text, n) {
				t.Errorf("%s 里出现了敏感内容 %q…", what, n[:4])
			}
		}
	}

	check("Hub 日志", f.logs.String())

	audits, _ := f.env.store.ListTerminalAudit(context.Background(), 500, 0)
	raw, _ := json.Marshal(audits)
	check("审计日志", string(raw))

	runs, _ := f.env.store.ListTerminalRuns(context.Background(), store.RunFilter{Limit: 100})
	raw, _ = json.Marshal(runs)
	check("运行记录", string(raw))

	list := f.env.call("GET", "/api/v1/terminal/hosts", nil, "Authorization", humanAuth(testUser))
	check("主机列表", list.Raw)
	host := f.env.call("GET", "/api/v1/terminal/exec/runs", nil, "Authorization", humanAuth(testUser))
	check("运行列表", host.Raw)

	// 库里的凭据是密封的，且不含主密钥。
	rec := f.host(t)
	check("库里的主机记录（密封字段除外）", fmt.Sprint(rec.SSHAddr, rec.SSHUser, rec.SSHFingerprint, rec.SSHError))
	if strings.Contains(rec.SSHSecret, sshTestPass) {
		t.Error("库里不得出现明文口令")
	}
}

// ---------- 主机生命周期 ----------

func TestSSHDeletingHostCancelsRemoteRun(t *testing.T) {
	f := newSSHFixture(t)
	n := sshSleep(3)
	res := f.mustExec(t, fmt.Sprintf("sleep %d", n), func(r *termexec.ExecRequest) { r.WaitSeconds = 0 })
	waitProcs(t, n, 1, 8*time.Second, "远端进程已启动")

	del := f.env.call("DELETE", "/api/v1/terminal/hosts/"+f.code, nil, f.adm...)
	if del.Status != 200 {
		t.Fatalf("删除主机: %d %s", del.Status, del.Raw)
	}
	// 主机记录（连同凭据）已经删除，取消仍要送达远端：用的是这次运行已持有的连接信息。
	waitProcs(t, n, 0, 15*time.Second, "删除主机后远端进程被终止")
	if rec, _ := f.env.store.GetTerminalRun(context.Background(), res.RunID); rec != nil {
		t.Logf("运行记录随主机删除: %+v", rec.State)
	}
}

func TestSSHDisablingHostCancelsRemoteRun(t *testing.T) {
	f := newSSHFixture(t)
	n := sshSleep(4)
	res := f.mustExec(t, fmt.Sprintf("sleep %d", n), func(r *termexec.ExecRequest) { r.WaitSeconds = 0 })
	waitProcs(t, n, 1, 8*time.Second, "远端进程已启动")

	dis := f.env.call("POST", "/api/v1/terminal/hosts/"+f.code+"/disable", nil, f.adm...)
	if dis.Status != 200 {
		t.Fatalf("停用主机: %d %s", dis.Status, dis.Raw)
	}
	waitProcs(t, n, 0, 15*time.Second, "停用主机后远端进程被终止")
	eventually(t, 10*time.Second, "运行进入终态", func() bool {
		return model.IsFinalRunState(f.dbRun(t, res.RunID).State)
	})
	_, err := f.exec(t, "echo x", nil)
	if err == nil {
		t.Fatal("停用的主机不应能执行命令")
	}
}

func TestSSHUnreachableHostFailsFastAndRecovers(t *testing.T) {
	f := newSSHFixture(t)
	if res := f.mustExec(t, "echo up", nil); res.State != model.RunSucceeded {
		t.Fatalf("%+v", res)
	}
	f.srv.Close()

	res := f.mustExec(t, "echo down", nil)
	if res.State != model.RunFailed || res.Reason != "ssh_unreachable" {
		t.Fatalf("主机不可达: %+v", res)
	}
	// 失败之后进入快速失败窗口：不再逐个请求去等连接超时。
	start := time.Now()
	_, err := f.exec(t, "echo down2", nil)
	wantCode(t, err, termexec.CodeHostOffline)
	if time.Since(start) > 2*time.Second {
		t.Fatalf("快速失败应立刻返回: %v", time.Since(start))
	}
	view := f.env.call("GET", "/api/v1/terminal/hosts", nil, "Authorization", humanAuth(testUser))
	for _, h := range view.Body["hosts"].([]any) {
		if mapOf(h)["machine_code"] == f.code && mapOf(h)["online"] != false {
			t.Fatalf("不可达的主机应显示离线: %+v", h)
		}
	}
}

// ---------- Hub 重启接管 ----------

func TestSSHHubRestartResumesRunningCommand(t *testing.T) {
	f := newSSHFixture(t)
	res := f.mustExec(t, "echo begin; sleep 2; echo resumed-ok", func(r *termexec.ExecRequest) { r.WaitSeconds = 0 })
	eventually(t, 10*time.Second, "运行已在远端启动", func() bool { return f.dbRun(t, res.RunID).StartedAt != nil })

	// 模拟 Hub 进程退出：驱动协程随之结束，数据库里的运行仍是 running，远端任务不受影响。
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	f.env.hub.Shutdown(ctx)
	cancel()
	if got := f.dbRun(t, res.RunID).State; got != model.RunRunning {
		t.Fatalf("重启前数据库里的状态应仍是 running，得到 %s", got)
	}

	hub2, err := NewHub(Options{
		Auth: fakeAuth{f.env.creds}, Store: f.env.store, Limiter: f.env.limiter, JWTSecret: strongSecret,
		WatchInterval: 20 * time.Millisecond, ExecEnabled: true, SSHSecretKey: f.key,
	})
	if err != nil {
		t.Fatal(err)
	}
	hub2.Start()
	t.Cleanup(func() {
		c, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		hub2.Shutdown(c)
	})

	eventually(t, 20*time.Second, "新 Hub 接管并收尾", func() bool {
		return f.dbRun(t, res.RunID).State == model.RunSucceeded
	})
	rec := f.dbRun(t, res.RunID)
	if rec.ExitCode == nil || *rec.ExitCode != 0 || !strings.Contains(rec.PreviewHead, "resumed-ok") || !strings.Contains(rec.PreviewHead, "begin") {
		t.Fatalf("接管后的记录 = %+v", rec)
	}
	// 之后仍可读取完整输出。
	out, err := hub2.Exec().Run(context.Background(), f.caller(), termexec.RunRequest{Action: "status", RunID: res.RunID})
	if err != nil {
		t.Fatal(err)
	}
	if r := out.(*termexec.RunResult); r.Stdout == nil || !strings.Contains(r.Stdout.Text, "resumed-ok") {
		t.Fatalf("状态查询 = %+v", r)
	}
}

func TestSSHResumeMarksMissingRemoteJobLost(t *testing.T) {
	f := newSSHFixture(t)
	n := sshSleep(5)
	res := f.mustExec(t, fmt.Sprintf("sleep %d", n), func(r *termexec.ExecRequest) { r.WaitSeconds = 0 })
	eventually(t, 10*time.Second, "运行已在远端启动", func() bool { return f.dbRun(t, res.RunID).StartedAt != nil })
	waitProcs(t, n, 1, 8*time.Second, "远端进程已启动")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	f.env.hub.Shutdown(ctx)
	cancel()
	// 远端的任务目录被人删掉（或主机被重装）；本测试起的 sleep 按进程号清理（只动自己的进程）。
	if err := os.RemoveAll(f.srv.Home + "/.relaymesh"); err != nil {
		t.Fatal(err)
	}
	killSleeps(t, n)

	hub2, err := NewHub(Options{
		Auth: fakeAuth{f.env.creds}, Store: f.env.store, Limiter: f.env.limiter, JWTSecret: strongSecret,
		WatchInterval: 20 * time.Millisecond, ExecEnabled: true, SSHSecretKey: f.key,
	})
	if err != nil {
		t.Fatal(err)
	}
	hub2.Start()
	t.Cleanup(func() {
		c, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		hub2.Shutdown(c)
	})
	eventually(t, 15*time.Second, "找不到远端任务时判为 lost", func() bool {
		return f.dbRun(t, res.RunID).State == model.RunLost
	})
	if rec := f.dbRun(t, res.RunID); rec.DenyReason != "not_on_host" {
		t.Fatalf("原因 = %q", rec.DenyReason)
	}
}
