//go:build e2e && linux

package e2e

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/teacat99/RelayMesh/v2/internal/agent"
	"github.com/teacat99/RelayMesh/v2/internal/terminal"
	"github.com/teacat99/RelayMesh/v2/internal/termproto"
)

// 真实 systemd 系统服务下的 Web 一键升级（方案见 .cursor/planning/2026-10-08-agent-web-upgrade-plan.md）。
//
// 与「升级窗口」那条演练不同，这里的升级由 Hub 下发请求、Agent 自己下载并交接给升级进程，
// 而交接靠的是 systemd-run 起一个脱离 Agent cgroup 的瞬态单元——只有真实的 systemd 才能验证它。
// 因此这个用例需要：RM_E2E_SYSTEMD=1、免密 sudo、以 systemd 为 PID 1 的 Linux；会在本机装卸两个系统服务与
// /usr/local/bin/relaymesh-agent。请只在一次性的容器里跑（scripts/e2e-systemd/run.sh 会准备好）。
//
// 要验证的承诺：
//  1. 主机所有者没有允许时，请求被拒绝；允许后，Agent 在几秒内向 Hub 声明能力；
//  2. 有活动会话时，没有确认的请求被拒绝（并带回会话摘要）；确认后升级完成，程序、版本、请求状态与主机上的结果记录都对；
//  3. 升级只重启 Agent：会话守护进程的进程号不变，里面的 Shell 原样存活（升级不打断正在运行的命令）；
//  4. 已经是服务端自带的版本时不再升级；
//  5. 新版本通过了自检却起不来：升级进程在超时后自动还原旧版本，请求以「已还原」收场，会话同样不受影响。

// stageDist 往分发目录里放一个 Agent 程序，并写好 VERSION。先写临时文件再改名，Hub 永远不会读到半截文件。
func stageDist(t *testing.T, dist, asset, version string, content []byte) {
	t.Helper()
	tmp := filepath.Join(dist, ".stage-"+randHex(3))
	if err := os.WriteFile(tmp, content, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(tmp, filepath.Join(dist, asset)); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dist, "VERSION"), []byte(version+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// waitHostFor 与 waitHost 一样，但超时由调用方决定（等自动还原要一两分钟）。
func waitHostFor(t *testing.T, h *hub, code string, timeout time.Duration, what string, cond func(terminal.HostView) bool) terminal.HostView {
	t.Helper()
	var got terminal.HostView
	waitUntil(t, timeout, what, func() bool {
		v, ok := h.api.host(code)
		if ok && cond(v) {
			got = v
			return true
		}
		return false
	})
	return got
}

func installedVersion(t *testing.T, bin string) string {
	t.Helper()
	out, err := exec.Command(bin, "version").CombinedOutput()
	if err != nil {
		t.Fatalf("%s version 失败: %v\n%s", bin, err, out)
	}
	return strings.TrimSpace(string(out))
}

func TestRealSystemdWebUpgrade(t *testing.T) {
	if os.Getenv("RM_E2E_SYSTEMD") != "1" {
		t.Skip("设置 RM_E2E_SYSTEMD=1 才会在本机安装 / 卸载真实的 systemd 系统服务（用 scripts/e2e-systemd/run.sh 在一次性容器里跑）")
	}
	hubBin, _ := binaries(t)
	oldBin, newBin := os.Getenv("RM_E2E_AGENT_OLD"), os.Getenv("RM_E2E_AGENT_NEW")
	if oldBin == "" || newBin == "" {
		t.Skip("设置 RM_E2E_AGENT_OLD / RM_E2E_AGENT_NEW 为两个不同版本的 relaymesh-agent 后运行")
	}
	if err := exec.Command("sudo", "-n", "true").Run(); err != nil {
		t.Skip("需要免密 sudo")
	}
	if _, err := exec.LookPath("systemd-run"); err != nil {
		t.Skip("需要 systemd-run")
	}
	stateOut, _ := exec.Command("systemctl", "is-system-running").Output()
	if state := strings.TrimSpace(string(stateOut)); state != "running" && state != "degraded" {
		t.Skip("systemd 不是 PID 1 或未就绪: " + state)
	}
	const oldVer, newVer, badVer = "v1.6.0", "v1.7.0", "v1.8.0-bad"
	const installed = "/usr/local/bin/relaymesh-agent"
	asset := "relaymesh-agent-linux-" + runtime.GOARCH

	// 服务端自带的分发目录：先放好「新版本」。
	dist := t.TempDir()
	newBytes, err := os.ReadFile(newBin)
	if err != nil {
		t.Fatal(err)
	}
	stageDist(t, dist, asset, newVer, newBytes)
	h := startHub(t, hubBin, "RELAYMESH_AGENT_DIST_DIR="+dist)

	dataDir := "/var/lib/rmh-e2e-" + randHex(3)
	cfgDir := dataDir + "-cfg"
	t.Cleanup(func() {
		if t.Failed() {
			out, _ := exec.Command("journalctl", "-u", "relaymesh-agent.service", "-n", "40", "--no-pager").CombinedOutput()
			t.Logf("---- relaymesh-agent.service 日志 ----\n%s", out)
			if b, err := os.ReadFile(filepath.Join(dataDir, "upgrade", "last.json")); err == nil {
				t.Logf("---- 主机上的升级结果记录 ----\n%s", b)
			}
		}
		bin := installed
		if _, err := os.Stat(bin); err != nil {
			bin = oldBin
		}
		_, _ = sudo(t, bin, "uninstall", "--purge", "--yes", "--data-dir", dataDir, "--config-dir", cfgDir)
		_, _ = sudo(t, "rm", "-rf", dataDir, cfgDir)
	})

	// —— 1. 安装旧版本（系统服务，守护进程独立单元），启用主机 ——
	if c := h.api.call("POST", "/discovery", map[string]any{}, nil); c != 200 {
		t.Fatalf("打开发现窗口失败: HTTP %d", c)
	}
	mustSudo(t, oldBin, "install", "--server", h.agentURL(), "--pin", h.pin, "--data-dir", dataDir, "--config-dir", cfgDir)
	for _, u := range []string{"relaymesh-holder.service", "relaymesh-agent.service"} {
		waitUntil(t, 20*time.Second, u+" 处于 active", func() bool { return unitActive(u) })
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
	v := waitHost(t, h, code, "旧版本主机上线、守护进程就绪、声明了一键升级能力", func(v terminal.HostView) bool {
		return v.Online && v.Holder != nil && v.Holder.State == "ready" && v.Holder.Persistent && v.WebUpgrade != ""
	})
	if v.AgentVersion != oldVer {
		t.Fatalf("前提：主机应报告旧版本 %s，实际 %q", oldVer, v.AgentVersion)
	}
	if v.WebUpgrade != "off" {
		t.Fatalf("主机所有者还没有允许，Agent 应声明 off，实际 %q", v.WebUpgrade)
	}
	if !v.UpgradeAvailable || v.LatestAgentVersion != newVer {
		t.Fatalf("旧版本主机应被提醒升级: available=%v latest=%q", v.UpgradeAvailable, v.LatestAgentVersion)
	}

	var rejected struct {
		Code     string                       `json:"code"`
		Sessions *terminal.WebUpgradeSessions `json:"sessions"`
	}
	post := func(body any, out any) int { return h.api.call("POST", "/hosts/"+code+"/web-upgrade", body, out) }

	// —— 2. 主机所有者没有允许：请求被拒绝，界面据此提示去主机上开启 ——
	if c := post(nil, &rejected); c != 409 || rejected.Code != "not_allowed" {
		t.Fatalf("没有允许时应被拒绝: HTTP %d code=%q", c, rejected.Code)
	}

	// 主机所有者在本机允许：开关只能在主机上改，Agent 盯着它，几秒内重新握手并向 Hub 声明 on。
	mustSudo(t, installed, "web-upgrade", "on", "--data-dir", dataDir)
	waitHost(t, h, code, "Agent 向 Hub 声明已允许从 Web 升级", func(v terminal.HostView) bool {
		return v.Online && v.WebUpgrade == "on" && v.Holder != nil && v.Holder.State == "ready"
	})

	// 一个正在运行的 Shell 会话，带着只在这个 Shell 里存在的变量。
	sess := h.api.openSession(code, "web-upgrade-e2e")
	marker := "kept-" + randHex(4)
	tc := h.api.attach(h, code, sess.SID, 0)
	tc.send("export RM_MARK=" + marker + "; echo SH:$$:END\r")
	tc.waitFor(fmt.Sprintf("SH:%d:END", sess.PID), 10*time.Second)
	tc.close()
	holderPID := unitMainPID(t, "relaymesh-holder.service")
	agentPID := unitMainPID(t, "relaymesh-agent.service")

	assertSessionIntact := func(step string) {
		t.Helper()
		waitHost(t, h, code, step+"：主机重新上线", func(v terminal.HostView) bool {
			return v.Online && v.Holder != nil && v.Holder.State == "ready"
		})
		if !pidAlive(sess.PID) {
			t.Fatalf("%s：Shell(%d) 应继续存活", step, sess.PID)
		}
		if now := unitMainPID(t, "relaymesh-holder.service"); now != holderPID {
			t.Fatalf("%s：会话守护进程不应被重启（进程号 %d → %d）", step, holderPID, now)
		}
		tc := h.api.attach(h, code, sess.SID, 0)
		defer tc.close()
		tc.waitFor(fmt.Sprintf("SH:%d:END", sess.PID), 10*time.Second)
		tc.send("echo VAR:$RM_MARK:END\r")
		tc.waitFor("VAR:"+marker+":END", 10*time.Second) // Shell 变量还在：是升级之前的那个 Shell
	}

	// —— 3. 有活动会话：没有确认的请求被拒绝，并带回会话摘要；被拒绝的请求不留下记录 ——
	rejected.Sessions = nil
	if c := post(nil, &rejected); c != 409 || rejected.Code != "sessions_active" || rejected.Sessions == nil {
		t.Fatalf("有活动会话时应要求确认: HTTP %d code=%q sessions=%+v", c, rejected.Code, rejected.Sessions)
	}
	if rejected.Sessions.Running < 1 || !rejected.Sessions.Persistent {
		t.Fatalf("会话摘要应说明有运行中的会话、且由守护进程持有: %+v", rejected.Sessions)
	}
	if hv, _ := h.api.host(code); hv.UpgradeJob != nil {
		t.Fatalf("被拒绝的请求不应留下升级记录: %+v", hv.UpgradeJob)
	}

	// —— 4. 确认后升级：Hub 下发 → Agent 下载校验 → systemd-run 交接 → 新版本上线 ——
	var accepted struct {
		UpgradeJob *terminal.UpgradeJobView `json:"upgrade_job"`
	}
	if c := post(map[string]any{"confirm_sessions": true}, &accepted); c != 200 || accepted.UpgradeJob == nil {
		t.Fatalf("确认后应接受请求: HTTP %d", c)
	}
	if j := accepted.UpgradeJob; j.From != oldVer || j.To != newVer {
		t.Fatalf("请求应是 %s → %s: %+v", oldVer, newVer, j)
	}
	got := waitHostFor(t, h, code, 120*time.Second, "升级完成：主机带着新版本回来、请求以 done 收场", func(v terminal.HostView) bool {
		return v.Online && v.AgentVersion == newVer && v.UpgradeJob != nil && v.UpgradeJob.State == terminal.UpgradeJobDone
	})
	if got.UpgradeAvailable {
		t.Fatal("升级后不应再提醒升级")
	}
	// 成功的结果记录由升级进程在确认新版本连上之后才写下（Hub 靠版本变化就知道完成了；记录随 Agent 下一次握手带给 Hub），
	// 所以这里直接看主机上的文件。
	waitUntil(t, 20*time.Second, "主机上留下了升级成功的记录", func() bool {
		n := agent.ReadUpgradeNote(dataDir)
		return n != nil && n.Outcome == termproto.UpgradeOutcomeOK && n.From == oldVer && n.To == newVer
	})
	if out := installedVersion(t, installed); !strings.Contains(out, newVer) {
		t.Fatalf("已安装的程序应是新版本，实际 %q", out)
	}
	if now := unitMainPID(t, "relaymesh-agent.service"); now == agentPID || !unitActive("relaymesh-agent.service") {
		t.Fatalf("Agent 应已被重启并在运行（进程号 %d → %d）", agentPID, now)
	}
	assertSessionIntact("升级后")
	// 瞬态的升级单元用的是 --collect：结束后不留痕迹。
	waitUntil(t, 20*time.Second, "瞬态升级单元已被回收", func() bool {
		out, _ := exec.Command("systemctl", "list-units", "--all", "--no-legend", "relaymesh-agent-upgrade-*").Output()
		return strings.TrimSpace(string(out)) == ""
	})
	// 升级完成后开关仍然保持（升级不替主机所有者改主意）。
	if got.WebUpgrade != "on" {
		t.Fatalf("升级后允许开关应保持 on，实际 %q", got.WebUpgrade)
	}

	// —— 5. 已经是服务端自带的版本：不再升级 ——
	rejected.Code = ""
	if c := post(map[string]any{"confirm_sessions": true}, &rejected); c != 409 || rejected.Code != "up_to_date" {
		t.Fatalf("已是最新时应拒绝: HTTP %d code=%q", c, rejected.Code)
	}

	// —— 6. 坏版本：能通过 version 自检，装上之后却起不来 → 超时自动还原旧版本 ——
	bad := "#!/bin/sh\nif [ \"$1\" = version ]; then echo 'RelayMesh Agent " + badVer + "'; exit 0; fi\necho 'boom: cannot start' >&2\nexit 1\n"
	stageDist(t, dist, asset, badVer, []byte(bad))
	waitHost(t, h, code, "Hub 看到更新的自带版本", func(v terminal.HostView) bool { return v.UpgradeAvailable && v.LatestAgentVersion == badVer })
	holderBefore := unitMainPID(t, "relaymesh-holder.service")
	if c := post(map[string]any{"confirm_sessions": true}, &accepted); c != 200 || accepted.UpgradeJob == nil {
		t.Fatalf("发起坏版本升级失败: HTTP %d", c)
	}
	failed := waitHostFor(t, h, code, 240*time.Second, "新版本起不来：自动还原，请求以 failed（已还原）收场", func(v terminal.HostView) bool {
		return v.Online && v.AgentVersion == newVer && v.UpgradeJob != nil && v.UpgradeJob.State == terminal.UpgradeJobFailed
	})
	if !failed.UpgradeJob.RolledBack {
		t.Fatalf("请求应标明已还原: %+v", failed.UpgradeJob)
	}
	if failed.LastUpgrade == nil || failed.LastUpgrade.Outcome != termproto.UpgradeOutcomeRolledBack {
		t.Fatalf("主机上的升级结果记录应是已还原: %+v", failed.LastUpgrade)
	}
	if out := installedVersion(t, installed); !strings.Contains(out, newVer) {
		t.Fatalf("还原后已安装的程序应仍是 %s，实际 %q", newVer, out)
	}
	if !unitActive("relaymesh-agent.service") {
		t.Fatal("还原后 Agent 服务应在运行")
	}
	if now := unitMainPID(t, "relaymesh-holder.service"); now != holderBefore {
		t.Fatalf("坏版本折腾期间守护进程不应被重启（进程号 %d → %d）", holderBefore, now)
	}
	assertSessionIntact("还原后")

	// —— 7. 卸载：两个单元与进程都消失 ——
	out := mustSudo(t, installed, "uninstall", "--purge", "--yes", "--data-dir", dataDir, "--config-dir", cfgDir)
	for _, u := range []string{"relaymesh-agent.service", "relaymesh-holder.service"} {
		if unitActive(u) {
			t.Fatalf("卸载后 %s 不应还在运行", u)
		}
		if _, err := os.Stat("/etc/systemd/system/" + u); err == nil {
			t.Fatalf("卸载后 %s 的单元文件应被删除\n%s", u, out)
		}
	}
}
