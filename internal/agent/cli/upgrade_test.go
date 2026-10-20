//go:build !windows

// upgrade 的测试沿用安装测试的假根目录与假命令执行器：先真的 doInstall 装一遍，再升级，
// 这样「升级没有改动服务配置、没有碰守护进程」是对着真实生成的单元文件验证的。

package cli

import (
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

const (
	oldAgentBody = "#!/bin/sh\necho agent v1\n"
	newAgentBody = "#!/bin/sh\necho agent v2\n"
	sysAgentBin  = "/usr/local/bin/relaymesh-agent"
)

// installedHost 造一台已经装好服务（Agent + 会话守护进程）的假主机，并清空命令记录。
func installedHost(t *testing.T, goos string, euid int, c common) (*fakeHost, Env) {
	t.Helper()
	h := newFakeHost(t, goos, euid)
	h.sys.sleep = func(time.Duration) {}
	cc, cfg := autoConfig(t, c)
	env := testEnv(t)
	if _, err := doInstall(env, cc, cfg, h.sys); err != nil {
		t.Fatal(err)
	}
	h.cmds = nil
	return h, env
}

// useNewAgent 让「当前正在运行的程序」变成新版本（升级时从它复制）。
func useNewAgent(t *testing.T, h *fakeHost) {
	t.Helper()
	if err := os.WriteFile(h.sys.exe, []byte(newAgentBody), 0o755); err != nil {
		t.Fatal(err)
	}
}

func TestUpgradeLinuxSystemReplacesBinaryAndRestartsOnlyTheAgent(t *testing.T) {
	h, env := installedHost(t, "linux", 0, common{})
	if got := h.read(t, sysAgentBin); got != oldAgentBody {
		t.Fatalf("前提：安装后应是旧版本: %q", got)
	}
	agentUnit, holderUnit := h.read(t, sysAgentUnit), h.read(t, sysHolderUnit)
	useNewAgent(t, h)
	h.runOut["systemctl is-active"] = "active"

	res, err := doUpgrade(env, h.sys, false)
	if err != nil {
		t.Fatal(err)
	}
	if got := h.read(t, sysAgentBin); got != newAgentBody {
		t.Fatalf("可执行文件应换成新版本: %q", got)
	}
	if st, _ := os.Stat(h.sys.p(sysAgentBin)); st.Mode().Perm() != 0o755 {
		t.Fatalf("权限应为 755: %o", st.Mode().Perm())
	}
	// 服务配置一个字节都不能变：升级不重写 --server / --data-dir 等安装时的配置。
	if h.read(t, sysAgentUnit) != agentUnit || h.read(t, sysHolderUnit) != holderUnit {
		t.Fatal("升级不应改动任何服务配置")
	}
	// 只重启 Agent；守护进程里有正在运行的会话，绝不能动。
	want := "systemctl restart relaymesh-agent.service\nsystemctl is-active relaymesh-agent.service"
	if strings.Join(h.cmds, "\n") != want {
		t.Fatalf("命令序列不对:\n%s", strings.Join(h.cmds, "\n"))
	}
	for _, c := range h.cmds {
		if strings.Contains(c, "holder") || strings.Contains(c, "daemon-reload") {
			t.Fatalf("不应触碰守护进程或重载配置: %s", c)
		}
	}
	if !res.HolderUnit || res.User || res.BinPath != sysAgentBin || !strings.Contains(res.State, "active") || len(res.Notes) != 0 {
		t.Fatalf("结果不对: %+v", res)
	}
	// 残留：同目录不应留下临时文件。
	entries, _ := os.ReadDir(h.sys.p("/usr/local/bin"))
	for _, e := range entries {
		if e.Name() != "relaymesh-agent" {
			t.Errorf("不应留下临时文件: %s", e.Name())
		}
	}
}

func TestUpgradeLinuxUserScope(t *testing.T) {
	h, env := installedHost(t, "linux", 1000, common{user: true})
	useNewAgent(t, h)
	h.runOut["systemctl --user is-active"] = "active"

	// 没有加 --user 的普通用户同样能找到自己的用户级安装。
	res, err := doUpgrade(env, h.sys, false)
	if err != nil {
		t.Fatal(err)
	}
	bin := "/home/alice/.local/bin/relaymesh-agent"
	if got := h.read(t, bin); got != newAgentBody || !res.User || res.BinPath != bin {
		t.Fatalf("用户级升级结果不对: %q %+v", got, res)
	}
	want := "systemctl --user restart relaymesh-agent.service\nsystemctl --user is-active relaymesh-agent.service"
	if strings.Join(h.cmds, "\n") != want {
		t.Fatalf("命令序列不对:\n%s", strings.Join(h.cmds, "\n"))
	}
}

func TestUpgradeNonRootSeesSystemInstallAndAsksForRoot(t *testing.T) {
	h, env := installedHost(t, "linux", 0, common{})
	h.sys.euid = 1000 // 之后换成普通用户来执行
	useNewAgent(t, h)

	_, err := doUpgrade(env, h.sys, false)
	if !errors.Is(err, errUpgradeNeedRoot) {
		t.Fatalf("应明确提示需要管理员权限: %v", err)
	}
	if got := h.read(t, sysAgentBin); got != oldAgentBody {
		t.Fatal("被拒绝时不能改动已安装的程序")
	}
	if len(h.cmds) != 0 {
		t.Fatalf("被拒绝时不应执行任何命令: %v", h.cmds)
	}
}

func TestUpgradeUserFlagDoesNotTouchSystemInstall(t *testing.T) {
	h, env := installedHost(t, "linux", 0, common{})
	useNewAgent(t, h)
	if _, err := doUpgrade(env, h.sys, true); !errors.Is(err, errNothingToUpgrade) {
		t.Fatalf("--user 只看用户级，没装就该说没装: %v", err)
	}
	if got := h.read(t, sysAgentBin); got != oldAgentBody || len(h.cmds) != 0 {
		t.Fatal("不应改动系统级安装")
	}
}

func TestUpgradeWhenNothingIsInstalled(t *testing.T) {
	h := newFakeHost(t, "linux", 0)
	_, err := doUpgrade(testEnv(t), h.sys, false)
	if !errors.Is(err, errNothingToUpgrade) {
		t.Fatalf("没装服务时应拒绝并指向 install: %v", err)
	}
	if h.exists(sysAgentBin) || len(h.cmds) != 0 {
		t.Fatal("没装服务时不应复制任何文件或执行命令")
	}
}

func TestUpgradeRefusesWhenUnitPointsElsewhere(t *testing.T) {
	h, env := installedHost(t, "linux", 0, common{})
	unit := strings.ReplaceAll(h.read(t, sysAgentUnit), sysAgentBin, "/opt/elsewhere/relaymesh-agent")
	if err := os.WriteFile(h.sys.p(sysAgentUnit), []byte(unit), 0o644); err != nil {
		t.Fatal(err)
	}
	useNewAgent(t, h)
	_, err := doUpgrade(env, h.sys, false)
	if err == nil || !strings.Contains(err.Error(), "无法确定该替换哪个文件") {
		t.Fatalf("服务配置指向别处时应拒绝: %v", err)
	}
	if got := h.read(t, sysAgentBin); got != oldAgentBody || len(h.cmds) != 0 {
		t.Fatal("拒绝时不应有任何改动")
	}
}

func TestUpgradeRefusesToUpgradeFromTheInstalledBinaryItself(t *testing.T) {
	h, env := installedHost(t, "linux", 0, common{})
	h.sys.exe = h.sys.p(sysAgentBin) // 运行的就是已安装的那个
	_, err := doUpgrade(env, h.sys, false)
	if err == nil || !strings.Contains(err.Error(), "已安装的程序本身") {
		t.Fatalf("应提示先获取新版本: %v", err)
	}
	if len(h.cmds) != 0 {
		t.Fatalf("不应重启服务: %v", h.cmds)
	}
}

func TestUpgradeReportsRestartFailureAndKeepsTheNewBinary(t *testing.T) {
	h, env := installedHost(t, "linux", 0, common{})
	useNewAgent(t, h)
	h.runErr["systemctl restart"] = errors.New("exit status 1")
	h.runOut["systemctl restart"] = "Failed to restart relaymesh-agent.service: Unit not found."

	_, err := doUpgrade(env, h.sys, false)
	if err == nil || !strings.Contains(err.Error(), "已经换成新版本") || !strings.Contains(err.Error(), "Unit not found") {
		t.Fatalf("重启失败应带上原始输出并说明程序已换: %v", err)
	}
	if got := h.read(t, sysAgentBin); got != newAgentBody {
		t.Fatal("重启失败不回滚程序（重试升级即可）")
	}
}

func TestUpgradeFlagsAServiceThatIsNotRunningAfterwards(t *testing.T) {
	h, env := installedHost(t, "linux", 0, common{})
	useNewAgent(t, h)
	h.runOut["systemctl is-active"] = "failed"
	res, err := doUpgrade(env, h.sys, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Notes) != 1 || !strings.Contains(res.Notes[0], "journalctl -u relaymesh-agent.service") {
		t.Fatalf("重启后没在运行应给出查看日志的提示: %+v", res.Notes)
	}
}

func TestUpgradeDarwinRestartsOnlyTheAgentJob(t *testing.T) {
	h, env := installedHost(t, "darwin", 0, common{})
	useNewAgent(t, h)
	res, err := doUpgrade(env, h.sys, false)
	if err != nil {
		t.Fatal(err)
	}
	if got := h.read(t, sysAgentBin); got != newAgentBody || res.State != "" {
		t.Fatalf("macOS 升级结果不对: %q %+v", got, res)
	}
	plist := "/Library/LaunchDaemons/" + launchdLabel + ".plist"
	want := strings.Join([]string{
		"launchctl bootout system/" + launchdLabel,
		"launchctl bootstrap system " + plist,
		"launchctl enable system/" + launchdLabel,
		"launchctl kickstart -k system/" + launchdLabel,
	}, "\n")
	if strings.Join(h.cmds, "\n") != want {
		t.Fatalf("launchctl 序列不对:\n%s", strings.Join(h.cmds, "\n"))
	}
	for _, c := range h.cmds {
		if strings.Contains(c, launchdHolderLabel) {
			t.Fatalf("不应触碰守护进程任务: %s", c)
		}
	}
}

func TestUpgradeUnsupportedOS(t *testing.T) {
	h := newFakeHost(t, "freebsd", 0)
	if _, err := doUpgrade(testEnv(t), h.sys, false); !errors.Is(err, errNoSystem) {
		t.Fatalf("不支持的系统应明确说明: %v", err)
	}
}
