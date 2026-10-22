//go:build e2e && !windows

package e2e

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

// 真实 SSH 服务端的兼容性：OpenSSH 与 Dropbear（Alpine 容器，shell 是 busybox ash），
// 口令与私钥两种凭据，经网关的人工终端与模型执行（含输出分页、超时、取消）。
//
// 进程内的测试 sshd 只能证明协议层；这里要证明的是远端运行脚本在真实 busybox 环境里可用
// （timeout / setsid / 进程树收集），以及各家服务端对认证方式与 PTY 的实现差异不会让网关出错。
//
// 仅在设置了 RM_E2E_REAL_SSHD=1 时运行（需要 docker 与访问 Alpine 软件源；首次会构建一个很小的镜像 rm-e2e-sshd:local）。

const sshdImage = "rm-e2e-sshd:local"

const sshdDockerfile = `FROM alpine:3.21
RUN apk add --no-cache openssh-server dropbear \
 && adduser -D -s /bin/sh e2e \
 && (grep -q '^/bin/sh$' /etc/shells || echo /bin/sh >> /etc/shells)
`

func ensureSSHDImage(t *testing.T) {
	t.Helper()
	if exec.Command("docker", "image", "inspect", sshdImage).Run() == nil {
		return
	}
	cmd := exec.Command("docker", "build", "-t", sshdImage, "-")
	cmd.Stdin = strings.NewReader(sshdDockerfile)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("构建 SSH 服务端镜像失败: %v\n%s", err, out)
	}
}

func TestRealSSHServersCompat(t *testing.T) {
	if os.Getenv("RM_E2E_REAL_SSHD") == "" {
		t.Skip("设置 RM_E2E_REAL_SSHD=1 后运行（需要 docker 与访问 Alpine 软件源）")
	}
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("需要 docker 命令")
	}
	ensureSSHDImage(t)
	for _, kind := range []string{"openssh", "dropbear"} {
		t.Run(kind, func(t *testing.T) { realSSHServerCase(t, kind) })
	}
}

func realSSHServerCase(t *testing.T, kind string) {
	l := newGWLab(t, "--ssh-gateway")

	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	sshPub, err := ssh.NewPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	authorized := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(sshPub)))
	block, err := ssh.MarshalPrivateKey(priv, "")
	if err != nil {
		t.Fatal(err)
	}
	privPEM := string(pem.EncodeToMemory(block))

	var serve string
	switch kind {
	case "openssh":
		serve = "ssh-keygen -A && exec /usr/sbin/sshd -D -e -o PasswordAuthentication=yes -o PermitRootLogin=no"
	case "dropbear":
		serve = "exec dropbear -F -E -R -p 22"
	default:
		t.Fatalf("未知的服务端: %s", kind)
	}
	setup := `echo "e2e:$E2E_PASS" | chpasswd && mkdir -p /home/e2e/.ssh && echo "$E2E_PUBKEY" > /home/e2e/.ssh/authorized_keys` +
		` && chown -R e2e:e2e /home/e2e/.ssh && chmod 700 /home/e2e/.ssh && chmod 600 /home/e2e/.ssh/authorized_keys && ` + serve

	name := "rm-e2e-" + kind + "-" + randHex(3)
	if out, err := exec.Command("docker", "run", "-d", "--name", name, "-p", "127.0.0.1::22",
		"-e", "E2E_PASS="+l.pass, "-e", "E2E_PUBKEY="+authorized, sshdImage, "sh", "-c", setup).CombinedOutput(); err != nil {
		t.Fatalf("启动 %s 容器失败: %v\n%s", kind, err, out)
	}
	t.Cleanup(func() {
		if t.Failed() {
			logs, _ := exec.Command("docker", "logs", "--tail", "30", name).CombinedOutput()
			t.Logf("---- %s 容器日志 ----\n%s", kind, logs)
		}
		_ = exec.Command("docker", "rm", "-f", name).Run()
	})
	portOut, err := exec.Command("docker", "port", name, "22/tcp").Output()
	if err != nil {
		t.Fatalf("docker port: %v", err)
	}
	addr := strings.TrimSpace(strings.Split(string(portOut), "\n")[0])

	var fp string
	waitUntil(t, 60*time.Second, kind+" 服务端就绪并能经网关探测", func() bool {
		f, st, _ := l.probe(addr)
		fp = f
		return st == 200 && f != ""
	})
	t.Logf("%s 主机密钥指纹: %s", kind, fp)

	// 错误口令被明确拒绝，不会保存任何东西
	var bad gwReply
	badBody := map[string]string{
		"remark": "bad", "address": addr, "user": "e2e", "auth_type": "password",
		"password": "wrong-" + randHex(4), "fingerprint": fp, "gateway": l.gw,
	}
	if c := l.h.api.call("POST", "/ssh/hosts", badBody, &bad); c == 200 || bad.Code != "ssh_auth_failed" {
		t.Fatalf("错误口令应被拒绝（ssh_auth_failed），实际 HTTP %d %q", c, bad.Code)
	}

	addKey := func() string {
		var out struct {
			Host struct {
				MachineCode string `json:"machine_code"`
			} `json:"host"`
			gwReply
		}
		body := map[string]string{
			"remark": kind + " key", "address": addr, "user": "e2e", "auth_type": "key",
			"private_key": privPEM, "fingerprint": fp, "gateway": l.gw,
		}
		if c := l.h.api.call("POST", "/ssh/hosts", body, &out); c != 200 || out.Host.MachineCode == "" {
			t.Fatalf("私钥登记失败: HTTP %d %s", c, out.Code)
		}
		return out.Host.MachineCode
	}

	for label, target := range map[string]string{
		"口令": l.addHost(addr, fp, kind+" password"),
		"私钥": addKey(),
	} {
		v, _ := l.h.api.host(target)
		if v.SSH == nil || v.SSH.TermOnly || !v.SSH.CredOnGateway || v.SSH.HasSecret {
			t.Fatalf("[%s] 应识别为 POSIX 主机且凭据在网关上: %+v", label, v.SSH)
		}
		exerciseRealSSHTarget(t, l, name, label, target)
	}
}

// exerciseRealSSHTarget 对一台已登记的真实 SSH 主机跑一遍人工终端与模型执行。
func exerciseRealSSHTarget(t *testing.T, l *gwLab, container, label, target string) {
	t.Helper()

	// —— 人工终端：真实 shell 的 PTY ——
	sess := l.open(target)
	tc := l.h.api.attach(l.h, l.gw, sess.SID, 0)
	tag := randHex(4)
	tc.send("echo hi-" + tag + "\r")
	tc.waitFor(line("hi-"+tag), 20*time.Second)
	tc.send("exit 4\r")
	waitUntil(t, 20*time.Second, fmt.Sprintf("[%s] 会话以退出码 4 结束", label), func() bool {
		_, list := l.h.api.sessions(l.gw)
		s, ok := sessionByID(list, sess.SID)
		return ok && s.State == "exited" && s.ExitCode != nil && *s.ExitCode == 4
	})
	tc.close()

	// —— 模型执行 ——
	m := l.modelClient(target)
	marker := "real-" + randHex(4)
	res := m.exec(target, "echo "+marker+"; echo err-"+marker+" >&2; exit 2", nil)
	if res.str("state") != "failed" || res.num("exit_code") != 2 ||
		!strings.Contains(res.obj("stdout").str("text"), marker) || !strings.Contains(res.obj("stderr").str("text"), "err-"+marker) {
		t.Fatalf("[%s] 结果 = %v", label, res)
	}
	if pwd := strings.TrimSpace(m.exec(target, "pwd", nil).obj("stdout").str("text")); pwd != "/home/e2e" {
		t.Fatalf("[%s] 默认工作目录 = %q", label, pwd)
	}
	big := m.exec(target, "seq 1 30000", nil)
	if big.str("state") != "succeeded" || big.obj("stdout").num("bytes") != 168894 {
		t.Fatalf("[%s] 大输出: %v", label, big)
	}
	next := big.obj("stdout").num("next_offset")
	page, isErr := m.run("output", big.str("run_id"), map[string]any{"stream": "stdout", "offset": next, "limit": 256 << 10})
	if isErr || page["eof"] != true || !strings.HasSuffix(page.str("text"), "\n30000\n") || int64(len(page.str("text"))) != 168894-next {
		t.Fatalf("[%s] 大输出分页: eof=%v len=%d next=%d", label, page["eof"], len(page.str("text")), next)
	}

	// 超时：远端 timeout 命令（busybox 的实现）真的结束了进程
	to := m.exec(target, "sleep 60", map[string]any{"timeout_seconds": 2, "wait_seconds": 25})
	if to.str("state") != "timeout" {
		t.Fatalf("[%s] 超时应得到 timeout: %v", label, to)
	}

	// 取消：整个进程树被结束，没有残留的 sleep
	running := m.exec(target, "sh -c 'sleep 301' & wait", map[string]any{"wait_seconds": 0})
	if running.str("state") != "running" {
		t.Fatalf("[%s] 应立即返回 running: %v", label, running)
	}
	cancelled, isErr := m.run("cancel", running.str("run_id"), map[string]any{"wait_seconds": 20})
	if isErr || cancelled.str("state") != "cancelled" {
		t.Fatalf("[%s] 取消结果: %v", label, cancelled)
	}
	waitUntil(t, 15*time.Second, fmt.Sprintf("[%s] 取消后远端没有残留的 sleep 301", label), func() bool {
		out, _ := exec.Command("docker", "exec", container, "sh", "-c", "ps | grep 'sleep 301' | grep -v grep").CombinedOutput()
		return strings.TrimSpace(string(out)) == ""
	})
}
