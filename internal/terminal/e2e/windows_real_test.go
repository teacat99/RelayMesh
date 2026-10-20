//go:build e2e && !windows

package e2e

import (
	"bytes"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/teacat99/RelayMesh/v2/internal/sshexec/sshdtest"
	"github.com/teacat99/RelayMesh/v2/internal/terminal"
	"github.com/teacat99/RelayMesh/v2/internal/termproto"
)

// Windows 真机验证：在 WSL 里驱动宿主 Windows 上的**真实** Agent 进程（经 WSL 互操作启动 .exe）。
//
//   - Hub 与 SSH 测试目标在 WSL 里，Windows 经 localhost 转发访问它们（127.0.0.1 在两边指向同一处）；
//   - Windows 上的 Agent 以 `--ssh-gateway --ssh-allow 127.0.0.1` 运行，数据目录是专用的实验目录，
//     不碰用户已安装的 Agent 与它的计划任务 / 服务（进程映像名也与之不同，清理时只按这个名字结束）；
//   - 要证明的是：Windows 上的本地终端与模型执行、网关登录目标的口令 / 私钥凭据、密封凭据库的目录权限（ACL）与持久性、
//     以及把 Windows 自带的 OpenSSH 服务当作目标（非 POSIX 的 cmd.exe，人工终端可用、模型执行被明确拒绝）。
//
// 运行方式（需要先把 Windows 版 Agent 复制成一个不叫 relaymesh-agent*.exe 的文件）：
//
//	GOOS=windows go build -o /tmp/rmlab-agent.exe ./cmd/relaymesh-agent && cp /tmp/rmlab-agent.exe /mnt/c/Users/<你>/AppData/Local/Temp/rmlab/
//	RM_E2E_WIN_AGENT=/mnt/c/Users/<你>/AppData/Local/Temp/rmlab/rmlab-agent.exe bash scripts/e2e-holder/run.sh -run TestWin

const winSystem32 = "/mnt/c/Windows/System32"

type winLab struct {
	t       *testing.T
	h       *hub
	exe     string // Agent 在 WSL 里的路径（/mnt/c/...）
	image   string // 进程映像名，taskkill 只按它结束
	root    string // 本次运行的实验目录（WSL 视角）
	dataWin string // 数据目录（Windows 视角）
	cfgWin  string
	logWin  string
	cmd     *exec.Cmd
	gw      string
}

func winPath(t *testing.T, p string) string {
	t.Helper()
	out, err := exec.Command("wslpath", "-w", p).Output()
	if err != nil {
		t.Fatalf("wslpath -w %s: %v", p, err)
	}
	return strings.TrimSpace(string(out))
}

func newWinLab(t *testing.T) *winLab {
	t.Helper()
	exe := os.Getenv("RM_E2E_WIN_AGENT")
	if exe == "" {
		t.Skip("设置 RM_E2E_WIN_AGENT 为 Windows 版 Agent 在 WSL 里的路径（/mnt/c/...）后运行")
	}
	image := filepath.Base(exe)
	if strings.HasPrefix(strings.ToLower(image), "relaymesh-agent") {
		t.Fatalf("为避免误伤已安装的 Agent，实验用的可执行文件不能叫 relaymesh-agent*.exe: %s", image)
	}
	hubBin, _ := binaries(t)
	w := &winLab{t: t, exe: exe, image: image}
	w.root = filepath.Join(filepath.Dir(exe), "run-"+randHex(4))
	for _, d := range []string{"data", "cfg", "log"} {
		if err := os.MkdirAll(filepath.Join(w.root, d), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	w.dataWin = winPath(t, filepath.Join(w.root, "data"))
	w.cfgWin = winPath(t, filepath.Join(w.root, "cfg"))
	w.logWin = winPath(t, filepath.Join(w.root, "log", "agent.log"))
	w.h = startHub(t, hubBin, execOn()...)
	t.Cleanup(func() {
		if t.Failed() {
			t.Logf("---- Windows Agent 日志 ----\n%s\n---- 进程输出 ----\n%s",
				tail(filepath.Join(w.root, "log", "agent.log"), 40), tail(filepath.Join(w.root, "stdout.log"), 20))
		}
		w.kill()
		_ = os.RemoveAll(w.root)
	})
	return w
}

func (w *winLab) start(extra ...string) {
	w.t.Helper()
	args := []string{"run", "--server", w.h.agentURL(), "--pin", w.h.pin, "--data-dir", w.dataWin, "--config-dir", w.cfgWin,
		"--log-file", w.logWin, "--ssh-gateway", "--ssh-allow", "127.0.0.1"}
	cmd := exec.Command(w.exe, append(args, extra...)...)
	cmd.Dir = "/mnt/c/Windows" // 互操作不支持 UNC 形式的工作目录
	out, err := os.OpenFile(filepath.Join(w.root, "stdout.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		w.t.Fatal(err)
	}
	cmd.Stdout, cmd.Stderr = out, out
	if err := cmd.Start(); err != nil {
		w.t.Fatalf("启动 Windows Agent 失败: %v", err)
	}
	w.cmd = cmd
}

// kill 结束实验用的 Agent（含它拉起的子进程树）；只按实验专用的映像名，不会碰已安装的 relaymesh-agent*。
func (w *winLab) kill() {
	_ = exec.Command(filepath.Join(winSystem32, "taskkill.exe"), "/F", "/T", "/IM", w.image).Run()
	if w.cmd == nil {
		return
	}
	done := make(chan struct{})
	go func(c *exec.Cmd) { _ = c.Wait(); close(done) }(w.cmd)
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		_ = w.cmd.Process.Kill()
	}
	w.cmd = nil
}

func (w *winLab) gatewayReady(v terminal.HostView) bool {
	return v.Online && v.Holder != nil && v.Holder.State == "ready" &&
		slices.Contains(v.Holder.Caps, termproto.HolderCapSSH) && slices.Contains(v.Holder.Caps, termproto.HolderCapSSHStore)
}

// enrollGateway 启用唯一一台待启用主机（Windows Agent）并等待它作为网关就绪。
func (w *winLab) enrollGateway() {
	w.t.Helper()
	w.gw = w.h.api.enrollOnly()
	waitHost(w.t, w.h, w.gw, "Windows 上的网关就绪", w.gatewayReady)
}

func (w *winLab) credFile() string {
	return filepath.Join(w.root, "data", "ssh-gateway", "credentials.json")
}

func (w *winLab) credHolds(code string) bool {
	b, err := os.ReadFile(w.credFile())
	return err == nil && bytes.Contains(b, []byte(`"`+code+`"`))
}

// leaks 在 Hub 与 Windows 实验目录的所有文件里查找凭据字节串。
func (w *winLab) leaks(needles ...string) []string {
	var found []string
	for _, root := range []string{w.h.dir, w.root} {
		_ = filepath.WalkDir(root, func(p string, e fs.DirEntry, err error) error {
			if err != nil || e.IsDir() || !e.Type().IsRegular() {
				return nil
			}
			if info, ierr := e.Info(); ierr != nil || info.Size() > 64<<20 {
				return nil
			}
			b, rerr := os.ReadFile(p)
			if rerr != nil {
				return nil
			}
			for _, n := range needles {
				if n != "" && bytes.Contains(b, []byte(n)) {
					found = append(found, p)
				}
			}
			return nil
		})
	}
	return found
}

// aclIdentities 列出某个路径上的访问控制项主体（PowerShell 的 Get-Acl，UTF-8 输出）。
func (w *winLab) aclIdentities(wslPath string) []string {
	w.t.Helper()
	script := "[Console]::OutputEncoding=[Text.Encoding]::UTF8; (Get-Acl -LiteralPath '" + winPath(w.t, wslPath) +
		"').Access | ForEach-Object { $_.IdentityReference.Value }"
	cmd := exec.Command(filepath.Join(winSystem32, "WindowsPowerShell/v1.0/powershell.exe"), "-NoProfile", "-NonInteractive", "-Command", script)
	cmd.Dir = "/mnt/c/Windows"
	out, err := cmd.Output()
	if err != nil {
		w.t.Fatalf("读取 ACL 失败: %v", err)
	}
	var ids []string
	for _, l := range strings.Split(string(out), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			ids = append(ids, l)
		}
	}
	return ids
}

// assertPrivate 要求路径上没有向宽泛主体（Everyone / Users / Authenticated Users）开放的访问控制项。
func (w *winLab) assertPrivate(rel string) {
	w.t.Helper()
	p := filepath.Join(w.root, rel)
	ids := w.aclIdentities(p)
	if len(ids) == 0 {
		w.t.Fatalf("%s 没有读到任何访问控制项", rel)
	}
	w.t.Logf("%s 的访问控制项主体: %v", rel, ids)
	for _, id := range ids {
		l := strings.ToLower(id)
		if l == "everyone" || strings.HasSuffix(l, `\users`) || strings.HasSuffix(l, `\authenticated users`) {
			w.t.Fatalf("%s 向过宽的主体开放了访问: %s（全部: %v）", rel, id, ids)
		}
	}
}

// waitReachable 等到 Windows 一侧能连上 WSL 里的 host:port。
// WSL 的 localhost 转发是事后检测新监听端口的，刚启动的服务大约要一秒才能从 Windows 访问到。
func (w *winLab) waitReachable(hostport string) {
	w.t.Helper()
	host, port, ok := strings.Cut(hostport, ":")
	if !ok {
		w.t.Fatalf("地址格式不对: %s", hostport)
	}
	script := "try { $c = New-Object Net.Sockets.TcpClient; $c.Connect('" + host + "', " + port + "); $c.Close(); exit 0 } catch { exit 1 }"
	ps := filepath.Join(winSystem32, "WindowsPowerShell/v1.0/powershell.exe")
	waitUntil(w.t, 40*time.Second, "Windows 一侧能连上 "+hostport, func() bool {
		cmd := exec.Command(ps, "-NoProfile", "-NonInteractive", "-Command", script)
		cmd.Dir = "/mnt/c/Windows"
		return cmd.Run() == nil
	})
}

func winUserName(t *testing.T) string {
	t.Helper()
	if u := os.Getenv("RM_E2E_WIN_SSH_USER"); u != "" {
		return u
	}
	cmd := exec.Command(filepath.Join(winSystem32, "cmd.exe"), "/c", "echo %USERNAME%")
	cmd.Dir = "/mnt/c/Windows"
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("读取 Windows 用户名失败: %v", err)
	}
	return strings.TrimSpace(string(out))
}

// 本地终端与模型执行（Windows 上的 ConPTY、PowerShell、编码），再经 Windows 上的网关用口令登录 WSL 里的 SSH 目标；
// 重启 Agent 之后凭据仍在、无需重新填写；密封凭据库的目录权限不向宽泛主体开放。
func TestWinGatewayLocalTerminalPasswordTargetAndPersistence(t *testing.T) {
	w := newWinLab(t)
	w.start()
	w.enrollGateway()

	// —— 本地人工终端（ConPTY + PowerShell）——
	sess := w.h.api.openSession(w.gw, "win-local")
	tc := w.h.api.attach(w.h, w.gw, sess.SID, 0)
	tag := randHex(4)
	// PSReadLine 会用控制序列重绘键入的那一行，输出行前面不一定有换行：拼接出结果，使键入的回显里不含连续的结果串。
	tc.send("Write-Output ('win-' + '" + tag + "')\r")
	tc.waitFor("win-"+tag+"\r\n", 30*time.Second)
	tc.send("exit 5\r")
	waitUntil(t, 20*time.Second, "本地会话以退出码 5 结束", func() bool {
		_, list := w.h.api.sessions(w.gw)
		s, ok := sessionByID(list, sess.SID)
		return ok && s.State == "exited" && s.ExitCode != nil && *s.ExitCode == 5
	})
	tc.close()

	// —— 本机的模型执行（PowerShell，UTF-8 往返）——
	l := &gwLab{t: t, h: w.h, gw: w.gw}
	m := l.modelClient(w.gw)
	res := m.exec(w.gw, "Write-Output 'hello-"+tag+"'; Write-Output '中文-"+tag+"'", nil)
	out := res.obj("stdout").str("text")
	if res.str("state") != "succeeded" || !strings.Contains(out, "hello-"+tag) || !strings.Contains(out, "中文-"+tag) {
		t.Fatalf("Windows 本机执行结果不对: %v", res)
	}
	if r := m.exec(w.gw, "exit 3", nil); r.str("state") != "failed" || r.num("exit_code") != 3 {
		t.Fatalf("退出码应原样返回: %v", r)
	}

	// —— 经 Windows 网关登录 WSL 里的 SSH 目标（口令）——
	l.pass = "p" + randHex(10)
	l.srv = sshdtest.Start(t, sshdtest.Options{User: "e2e", Password: l.pass})
	w.waitReachable(l.srv.Addr)
	fp, st, code := l.probe(l.srv.Addr)
	if st != 200 || fp == "" {
		t.Fatalf("经 Windows 网关探测失败: HTTP %d %s", st, code)
	}
	target := l.addHost(l.srv.Addr, fp, "wsl inner")
	if !w.credHolds(target) {
		t.Fatal("登录成功之后，凭据应保存在 Windows 网关的凭据库里")
	}
	ss := l.open(target)
	tc2 := w.h.api.attach(w.h, w.gw, ss.SID, 0)
	tc2.waitFor("welcome e2e", 20*time.Second)
	tc2.send("echo via-win-" + tag + "\r")
	tc2.waitFor(line("via-win-"+tag), 15*time.Second)
	tc2.close()
	tm := l.modelClient(target)
	marker := "gwwin-" + randHex(4)
	if r := tm.exec(target, "echo "+marker, nil); r.str("state") != "succeeded" || !strings.Contains(r.obj("stdout").str("text"), marker) {
		t.Fatalf("经 Windows 网关执行失败: %v", r)
	}

	// —— 凭据卫生与权限 ——
	if found := w.leaks(l.pass); len(found) > 0 {
		t.Fatalf("口令以明文出现在了文件里: %v", found)
	}
	w.assertPrivate(filepath.Join("data", "ssh-gateway"))
	w.assertPrivate(filepath.Join("data", "ssh-gateway", "credentials.json"))
	w.assertPrivate(filepath.Join("data", "ssh-gateway", "key"))

	// —— 重启 Windows Agent：凭据仍在，无需重新填写 ——
	w.kill()
	waitHost(t, w.h, w.gw, "Hub 感知到网关离线", func(v terminal.HostView) bool { return !v.Online })
	w.start()
	waitHost(t, w.h, w.gw, "重启后的网关重新就绪", w.gatewayReady)
	marker2 := "gwwin2-" + randHex(4)
	if r := tm.exec(target, "echo "+marker2, nil); r.str("state") != "succeeded" || !strings.Contains(r.obj("stdout").str("text"), marker2) {
		t.Fatalf("重启 Windows Agent 后应无需重新填写凭据就能执行: %v", r)
	}

	// —— 删除主机：Windows 网关上的凭据被清掉 ——
	if c := w.h.api.call("DELETE", "/hosts/"+target, nil, nil); c != 200 {
		t.Fatalf("删除主机: HTTP %d", c)
	}
	waitUntil(t, 30*time.Second, "被删除主机的凭据从 Windows 网关的凭据库中清除", func() bool { return !w.credHolds(target) })
}

// 把 Windows 自带的 OpenSSH 服务当作目标（Windows 本机的 127.0.0.1:22），用私钥登录。
// 它的默认 shell 是 cmd.exe，不是 POSIX 环境：登记时被识别为「只能当人工终端用」，人工终端可用，模型执行被明确拒绝。
//
// 私钥取自 RM_E2E_WIN_SSH_KEY（默认 ~/.ssh/id_ed25519），只在本进程内存里使用，经 API 交给实验网关密封保存，
// 测试结束时实验目录被删除；测试还会确认它没有以明文出现在任何文件里。
func TestWinGatewayToWindowsOpenSSHWithKey(t *testing.T) {
	keyPath := os.Getenv("RM_E2E_WIN_SSH_KEY")
	if keyPath == "" {
		keyPath = filepath.Join(os.Getenv("HOME"), ".ssh", "id_ed25519")
	}
	pemBytes, err := os.ReadFile(keyPath)
	if err != nil {
		t.Skipf("没有可用的私钥 %s: %v", keyPath, err)
	}
	if _, err := ssh.ParseRawPrivateKey(pemBytes); err != nil {
		t.Skipf("私钥不能直接使用（可能有口令）: %v", err)
	}
	w := newWinLab(t)
	w.start()
	w.enrollGateway()
	l := &gwLab{t: t, h: w.h, gw: w.gw}

	const addr = "127.0.0.1:22"
	fp, st, code := l.probe(addr)
	if st != 200 || fp == "" {
		t.Fatalf("探测 Windows OpenSSH 失败: HTTP %d %s", st, code)
	}
	t.Logf("Windows OpenSSH 的主机密钥指纹: %s", fp)
	var added struct {
		Host struct {
			MachineCode string `json:"machine_code"`
		} `json:"host"`
		gwReply
	}
	body := map[string]string{
		"remark": "windows openssh", "address": addr, "user": winUserName(t), "auth_type": "key",
		"private_key": string(pemBytes), "fingerprint": fp, "gateway": w.gw,
	}
	if c := w.h.api.call("POST", "/ssh/hosts", body, &added); c != 200 || added.Host.MachineCode == "" {
		t.Fatalf("经 Windows 网关登记 Windows OpenSSH 失败（私钥登录）: HTTP %d %s", c, added.Code)
	}
	target := added.Host.MachineCode
	if !w.credHolds(target) {
		t.Fatal("私钥应密封保存在 Windows 网关的凭据库里")
	}
	v, _ := w.h.api.host(target)
	if v.SSH == nil || !v.SSH.CredOnGateway || v.SSH.HasSecret || v.SSH.CredMissing {
		t.Fatalf("主机视图应表明凭据在网关上: %+v", v.SSH)
	}
	t.Logf("Windows OpenSSH 目标: term_only=%v key_type=%s", v.SSH.TermOnly, v.SSH.KeyType)

	// 人工终端：cmd.exe 的交互会话
	sess := l.open(target)
	tc := w.h.api.attach(w.h, w.gw, sess.SID, 0)
	tag := randHex(4)
	// 拼接出结果：键入的回显里不含连续的结果串（cmd.exe 会把键入的内容原样回显）。
	// （同一行里 %A% 在 set 执行之前就被展开，所以分三次输入。）
	tc.send("set A=winssh\r")
	tc.send("set B=" + tag + "\r")
	tc.send("echo %A%-%B%\r")
	tc.waitFor("winssh-"+tag, 30*time.Second) // ConPTY 重绘时结果行后面跟的是光标定位序列，不一定是换行
	tc.send("exit\r")
	waitUntil(t, 30*time.Second, "Windows OpenSSH 会话结束", func() bool {
		_, list := w.h.api.sessions(w.gw)
		s, ok := sessionByID(list, sess.SID)
		return ok && s.State == "exited"
	})
	tc.close()

	// 模型执行：非 POSIX 目标被明确拒绝（而不是含糊地失败）
	if v.SSH.TermOnly {
		_ = w.h.api.call("POST", "/hosts/"+target+"/model", map[string]any{"allow_model": true}, nil)
		m := l.modelClient(target)
		_, isErr := m.tool("terminal_exec", map[string]any{"target": target, "command": "echo x"})
		if !isErr {
			t.Fatal("非 POSIX 的 Windows OpenSSH 目标不应允许模型执行")
		}
	}

	// 私钥不应以明文出现在任何文件里（取密钥正文中段的一段作为特征串）
	var body64 []string
	for _, ln := range strings.Split(string(pemBytes), "\n") {
		if ln = strings.TrimSpace(ln); ln != "" && !strings.HasPrefix(ln, "-----") {
			body64 = append(body64, ln)
		}
	}
	if len(body64) >= 2 {
		if found := w.leaks(body64[len(body64)/2]); len(found) > 0 {
			t.Fatalf("私钥以明文出现在了文件里: %v", found)
		}
	}
	w.assertPrivate(filepath.Join("data", "ssh-gateway", "credentials.json"))
}
