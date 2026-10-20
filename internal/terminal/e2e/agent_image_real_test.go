//go:build e2e && !windows

package e2e

import (
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/teacat99/RelayMesh/v2/internal/sshexec/sshdtest"
	"github.com/teacat99/RelayMesh/v2/internal/terminal"
	"github.com/teacat99/RelayMesh/v2/internal/termproto"
)

// Agent 专用镜像（Dockerfile.agent）的真实通路验证：容器里跑的就是要发布的那个镜像，
// 以默认命令（run；SSH 网关是 Agent 的标配能力、默认开启）加环境变量启动，Hub 与 SSH 目标都在宿主机上。
//
// 要证明的是：
//   - 镜像以默认命令就是一个可用的网关：发现 → 启用 → 声明 ssh 与凭据库能力；
//   - 经它完成「探测 → 登记（凭据只交给网关）→ 人工终端 → 模型执行」；
//   - 允许名单（RELAYMESH_AGENT_SSH_ALLOW）之外的目标被网关本机拒绝；
//   - 数据卷里有密封的凭据库、没有明文口令，文件属于容器里的非 root 用户；
//   - 重建容器（同一个数据卷）后机器码不变、凭据仍在网关上，无需重新填写就能继续执行。
//
// 仅在设置了 RM_E2E_AGENT_IMAGE（镜像名）时运行，例如：
//
//	docker build -f Dockerfile.agent -t relaymesh-agent:local .
//	RM_E2E_AGENT_IMAGE=relaymesh-agent:local bash scripts/e2e-holder/run.sh -run AgentImage
func TestRealAgentImageGateway(t *testing.T) {
	image := os.Getenv("RM_E2E_AGENT_IMAGE")
	if image == "" {
		t.Skip("设置 RM_E2E_AGENT_IMAGE 为已构建的 Agent 专用镜像后运行")
	}
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("需要 docker 命令")
	}
	hubBin, _ := binaries(t)
	h := startHub(t, hubBin, execOn()...) // 没有配置 SSH 主密钥：网关主机不受影响

	docker := func(args ...string) (string, error) {
		out, err := exec.Command("docker", args...).CombinedOutput()
		return string(out), err
	}
	name := "rm-e2e-agent-" + randHex(4)
	vol := name + "-data"
	if out, err := docker("volume", "create", vol); err != nil {
		t.Fatalf("创建数据卷失败: %v\n%s", err, out)
	}
	t.Cleanup(func() {
		if t.Failed() {
			logs, _ := docker("logs", "--tail", "40", name)
			t.Logf("---- 容器日志 ----\n%s", logs)
		}
		_, _ = docker("rm", "-f", name)
		_, _ = docker("volume", "rm", "-f", vol)
	})
	// 默认命令（不传 command）：镜像自己的 CMD 就是 run，网关默认开启。
	start := func() {
		t.Helper()
		out, err := docker("run", "-d", "--name", name, "--network", "host",
			"-e", "RELAYMESH_AGENT_SERVER="+h.agentURL(),
			"-e", "RELAYMESH_AGENT_PIN="+h.pin,
			"-e", "RELAYMESH_AGENT_SSH_ALLOW=127.0.0.0/8",
			"-v", vol+":/data", image)
		if err != nil {
			t.Fatalf("启动容器失败: %v\n%s", err, out)
		}
	}
	start()

	gw := h.api.enrollOnly()
	ready := func(v terminal.HostView) bool {
		return v.Online && v.Holder != nil && v.Holder.State == "ready" &&
			slices.Contains(v.Holder.Caps, termproto.HolderCapSSH) && slices.Contains(v.Holder.Caps, termproto.HolderCapSSHStore)
	}
	waitHost(t, h, gw, "镜像里的网关上线并声明 ssh 与凭据库能力", ready)

	pass := "p" + randHex(10)
	srv := sshdtest.Start(t, sshdtest.Options{User: "e2e", Password: pass})
	l := &gwLab{t: t, h: h, srv: srv, pass: pass, gw: gw}

	fp, st, code := l.probe(srv.Addr)
	if st != 200 || fp == "" {
		t.Fatalf("经镜像里的网关探测失败: HTTP %d %s", st, code)
	}
	// 允许名单之外的目标由网关本机拒绝（10.255.255.1 不在 127.0.0.0/8 内，也不会有任何出站连接）。
	if _, st, code := l.probe("10.255.255.1:22"); st != 403 || code != "ssh_target_denied" {
		t.Fatalf("名单之外的目标应被网关拒绝（403 ssh_target_denied），实际 HTTP %d %s", st, code)
	}
	target := l.addHost(srv.Addr, fp, "image inner")

	// 人工终端
	sess := l.open(target)
	tc := h.api.attach(h, gw, sess.SID, 0)
	tc.waitFor("welcome e2e", 15*time.Second)
	tag := randHex(4)
	tc.send("echo ping-" + tag + "\r")
	tc.waitFor(line("ping-"+tag), 10*time.Second)
	tc.close()

	// 模型执行：凭据在网关上，Hub 没有副本
	m := l.modelClient(target)
	marker := "img-" + randHex(4)
	if res := m.exec(target, "echo "+marker, nil); res.str("state") != "succeeded" || !strings.Contains(res.obj("stdout").str("text"), marker) {
		t.Fatalf("经镜像网关执行失败: %v", res)
	}

	// 数据卷：密封凭据库与密钥在容器的非 root 用户名下，没有明文口令
	if uid, err := docker("exec", name, "id", "-u"); err != nil || strings.TrimSpace(uid) != "10001" {
		t.Fatalf("容器应以 uid 10001 运行: %q %v", uid, err)
	}
	copyData := func() string {
		dst := t.TempDir()
		if out, err := docker("cp", name+":/data/.", dst); err != nil {
			t.Fatalf("取出数据卷内容失败: %v\n%s", err, out)
		}
		return dst
	}
	dataDir := copyData()
	cred, err := os.ReadFile(filepath.Join(dataDir, "ssh-gateway", "credentials.json"))
	if err != nil || !strings.Contains(string(cred), `"`+target+`"`) {
		t.Fatalf("网关的凭据库里应有这台主机的条目: %v", err)
	}
	if fi, err := os.Stat(filepath.Join(dataDir, "ssh-gateway", "key")); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("密封密钥文件应存在且仅所有者可读写: %v %v", fi, err)
	}
	assertNoPlain := func(root string) {
		t.Helper()
		_ = filepath.WalkDir(root, func(p string, e fs.DirEntry, err error) error {
			if err != nil || e.IsDir() || !e.Type().IsRegular() {
				return nil
			}
			if b, rerr := os.ReadFile(p); rerr == nil && strings.Contains(string(b), pass) {
				t.Fatalf("口令以明文出现在了数据卷里: %s", p)
			}
			return nil
		})
	}
	assertNoPlain(dataDir)

	// 重建容器（同一个数据卷）：机器码不变，凭据仍在网关上
	if out, err := docker("rm", "-f", name); err != nil {
		t.Fatalf("删除容器失败: %v\n%s", err, out)
	}
	waitHost(t, h, gw, "旧容器移除后 Hub 感知到网关离线", func(v terminal.HostView) bool { return !v.Online })
	start()
	waitHost(t, h, gw, "重建后的网关用同一个机器码重新上线", ready)
	marker2 := "img2-" + randHex(4)
	if res := m.exec(target, "echo "+marker2, nil); res.str("state") != "succeeded" || !strings.Contains(res.obj("stdout").str("text"), marker2) {
		t.Fatalf("重建容器后应无需重新填写凭据就能执行: %v", res)
	}
	if hosts := h.api.hosts(); len(hosts.Pending) != 0 {
		t.Fatalf("重建后不应出现新的待启用主机（机器码应保持不变）: %+v", hosts.Pending)
	}
	assertNoPlain(copyData())
}
