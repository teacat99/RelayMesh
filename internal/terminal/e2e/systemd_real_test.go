//go:build e2e && linux

package e2e

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/teacat99/RelayMesh/v2/internal/terminal"
	"github.com/teacat99/RelayMesh/v2/internal/termproto"
)

// 真实 systemd 系统服务下的双单元演练：安装器生成的 Agent / 守护进程两个单元，
// 在真实的 systemd 里是否真的做到「Agent 升级 / 重启 / 被杀都不影响会话」。
//
// 需要：RM_E2E_SYSTEMD=1、免密 sudo、以 systemd 为 PID 1 的 Linux。会在本机装卸两个系统服务与
// /usr/local/bin/relaymesh-agent，用例结束时无论成败都会卸载并清理。

func sudo(t *testing.T, args ...string) (string, error) {
	t.Helper()
	out, err := exec.Command("sudo", append([]string{"-n"}, args...)...).CombinedOutput()
	return string(out), err
}

func mustSudo(t *testing.T, args ...string) string {
	t.Helper()
	out, err := sudo(t, args...)
	if err != nil {
		t.Fatalf("sudo %s 失败: %v\n%s", strings.Join(args, " "), err, out)
	}
	return out
}

func unitMainPID(t *testing.T, unit string) int {
	t.Helper()
	out, err := exec.Command("systemctl", "show", "-p", "MainPID", "--value", unit).Output()
	if err != nil {
		t.Fatalf("读取 %s 主进程号失败: %v", unit, err)
	}
	n, _ := strconv.Atoi(strings.TrimSpace(string(out)))
	return n
}

func unitActive(unit string) bool {
	out, _ := exec.Command("systemctl", "is-active", unit).Output()
	return strings.TrimSpace(string(out)) == "active"
}

func TestRealSystemdDoubleUnit(t *testing.T) {
	if os.Getenv("RM_E2E_SYSTEMD") != "1" {
		t.Skip("设置 RM_E2E_SYSTEMD=1 才会在本机安装 / 卸载真实的 systemd 系统服务")
	}
	hubBin, agentBin := binaries(t)
	if err := exec.Command("sudo", "-n", "true").Run(); err != nil {
		t.Skip("需要免密 sudo")
	}
	stateOut, _ := exec.Command("systemctl", "is-system-running").Output()
	if state := strings.TrimSpace(string(stateOut)); state != "running" && state != "degraded" {
		t.Skip("systemd 不是 PID 1 或未就绪: " + state)
	}

	h := startHub(t, hubBin)
	dataDir := "/var/lib/rmh-e2e-" + randHex(3)
	cfgDir := dataDir + "-cfg"
	installArgs := []string{agentBin, "install", "--server", h.agentURL(), "--pin", h.pin, "--data-dir", dataDir, "--config-dir", cfgDir}
	t.Cleanup(func() {
		_, _ = sudo(t, agentBin, "uninstall", "--purge", "--yes", "--data-dir", dataDir, "--config-dir", cfgDir)
		_, _ = sudo(t, "rm", "-rf", dataDir, cfgDir)
	})

	// 1) 安装：两个单元都应起来，Agent 单元 Wants+After 守护进程单元。
	if code := h.api.call("POST", "/discovery", map[string]any{}, nil); code != 200 {
		t.Fatalf("打开发现窗口失败: HTTP %d", code)
	}
	installOut := mustSudo(t, installArgs...)
	if !strings.Contains(installOut, "relaymesh-holder.service") {
		t.Fatalf("安装输出应提到守护进程单元:\n%s", installOut)
	}
	for _, u := range []string{"relaymesh-holder.service", "relaymesh-agent.service"} {
		waitUntil(t, 20*time.Second, u+" 处于 active", func() bool { return unitActive(u) })
	}
	unit := mustSudo(t, "cat", "/etc/systemd/system/relaymesh-agent.service")
	if !strings.Contains(unit, "Wants=network-online.target relaymesh-holder.service") || !strings.Contains(unit, "--holder external") {
		t.Fatalf("Agent 单元内容不符:\n%s", unit)
	}

	var code string
	waitUntil(t, 30*time.Second, "发现待启用主机", func() bool {
		p := h.api.hosts().Pending
		if len(p) == 1 {
			code = p[0].MachineCode
			return true
		}
		return false
	})
	if c := h.api.call("POST", "/hosts/"+code+"/enable", map[string]any{}, nil); c != 200 {
		t.Fatalf("启用主机失败: HTTP %d", c)
	}
	waitHost(t, h, code, "主机上线且守护进程就绪", func(v terminal.HostView) bool {
		return v.Online && v.Holder != nil && v.Holder.State == "ready" && v.Holder.Persistent
	})

	sess := h.api.openSession(code, "systemd-persist")
	marker := "sysd-" + randHex(4)
	tc := h.api.attach(h, code, sess.SID, 0)
	tc.send("export RM_MARK=" + marker + "; echo SH:$$:END\r")
	tc.waitFor(fmt.Sprintf("SH:%d:END", sess.PID), 10*time.Second)
	tc.close()

	assertSessionIntact := func(step string) {
		t.Helper()
		waitHost(t, h, code, step+"：主机重新上线", func(v terminal.HostView) bool {
			return v.Online && v.Holder != nil && v.Holder.State == "ready"
		})
		if !pidAlive(sess.PID) {
			t.Fatalf("%s：Shell(%d) 应继续存活", step, sess.PID)
		}
		tc := h.api.attach(h, code, sess.SID, 0)
		tc.waitFor(fmt.Sprintf("SH:%d:END", sess.PID), 10*time.Second)
		tc.send("echo VAR:$RM_MARK:END\r")
		tc.waitFor("VAR:"+marker+":END", 10*time.Second)
		tc.close()
	}

	holderPID := unitMainPID(t, "relaymesh-holder.service")
	agentPID := unitMainPID(t, "relaymesh-agent.service")

	// 2) kill -9 Agent：systemd 自动拉起，会话不受影响。
	mustSudo(t, "kill", "-9", strconv.Itoa(agentPID)) // 强杀主进程；systemd 的 Restart=always 负责拉起
	waitUntil(t, 40*time.Second, "systemd 重新拉起 Agent", func() bool {
		p := unitMainPID(t, "relaymesh-agent.service")
		return unitActive("relaymesh-agent.service") && p != 0 && p != agentPID
	})
	assertSessionIntact("kill -9 Agent")
	if unitMainPID(t, "relaymesh-holder.service") != holderPID {
		t.Fatal("Agent 被杀不应影响守护进程")
	}

	// 3) systemctl restart Agent（升级时的做法）。
	agentPID = unitMainPID(t, "relaymesh-agent.service")
	mustSudo(t, "systemctl", "restart", "relaymesh-agent.service")
	assertSessionIntact("restart Agent")
	if unitMainPID(t, "relaymesh-agent.service") == agentPID || unitMainPID(t, "relaymesh-holder.service") != holderPID {
		t.Fatal("restart Agent 应只换 Agent 进程")
	}

	// 4) 重新执行 install（升级路径）：守护进程原样保留。
	agentPID = unitMainPID(t, "relaymesh-agent.service")
	out := mustSudo(t, installArgs...)
	assertSessionIntact("重新 install")
	if unitMainPID(t, "relaymesh-holder.service") != holderPID {
		t.Fatalf("重新 install 不得重启守护进程（会结束会话）\n%s", out)
	}
	if unitMainPID(t, "relaymesh-agent.service") == agentPID {
		t.Fatal("重新 install 应重启 Agent")
	}

	// 5) 守护进程崩溃（kill -9）：systemd 拉起新的，Hub 报告 holder_exit，Agent 自动重连新守护进程。
	victim := h.api.openSession(code, "systemd-victim")
	mustSudo(t, "kill", "-9", strconv.Itoa(holderPID))
	waitUntil(t, 40*time.Second, "systemd 重新拉起守护进程", func() bool {
		p := unitMainPID(t, "relaymesh-holder.service")
		return unitActive("relaymesh-holder.service") && p != 0 && p != holderPID
	})
	v := waitHost(t, h, code, "守护进程恢复并报告崩溃丢失的会话", func(v terminal.HostView) bool {
		if v.Holder == nil || v.Holder.State != "ready" {
			return false
		}
		for _, l := range v.Holder.Lost {
			if l.SID == victim.SID {
				return true
			}
		}
		return false
	})
	for _, l := range v.Holder.Lost {
		if l.SID == victim.SID && l.Reason != termproto.LostHolderExit {
			t.Fatalf("崩溃应报告 holder_exit: %+v", l)
		}
	}

	// 6) 全部停掉后只启动 Agent：Wants= 应把守护进程一并拉起。
	mustSudo(t, "systemctl", "stop", "relaymesh-agent.service", "relaymesh-holder.service")
	if unitActive("relaymesh-holder.service") || unitActive("relaymesh-agent.service") {
		t.Fatal("stop 之后两个单元都应是 inactive")
	}
	mustSudo(t, "systemctl", "start", "relaymesh-agent.service")
	waitUntil(t, 20*time.Second, "启动 Agent 时 Wants= 拉起守护进程", func() bool { return unitActive("relaymesh-holder.service") })
	waitHost(t, h, code, "两者都起来后主机上线", func(v terminal.HostView) bool {
		return v.Online && v.Holder != nil && v.Holder.State == "ready"
	})

	// 7) 卸载：两个单元与进程都消失。
	out = mustSudo(t, agentBin, "uninstall", "--purge", "--yes", "--data-dir", dataDir, "--config-dir", cfgDir)
	for _, u := range []string{"relaymesh-agent.service", "relaymesh-holder.service"} {
		if unitActive(u) {
			t.Fatalf("卸载后 %s 不应还在运行", u)
		}
		if _, err := os.Stat("/etc/systemd/system/" + u); err == nil {
			t.Fatalf("卸载后 %s 的单元文件应被删除\n%s", u, out)
		}
	}
}
