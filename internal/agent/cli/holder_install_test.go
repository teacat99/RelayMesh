//go:build !windows

// 双服务（Agent + 会话守护进程）安装形态的测试。单服务（内嵌）形态沿用 install_test.go 的基线用例。

package cli

import (
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/teacat99/RelayMesh/v2/internal/agent/holder"
)

const (
	sysAgentUnit  = "/etc/systemd/system/relaymesh-agent.service"
	sysHolderUnit = "/etc/systemd/system/relaymesh-holder.service"
)

func autoConfig(t *testing.T, c common) (common, resolved) {
	t.Helper()
	c.holderMode = "auto"
	return testConfig(t, c)
}

func TestInstallLinuxInstallsHolderAndAgentUnits(t *testing.T) {
	h := newFakeHost(t, "linux", 0)
	c, cfg := autoConfig(t, common{maxSessions: 6})
	res, err := doInstall(testEnv(t), c, cfg, h.sys)
	if err != nil {
		t.Fatal(err)
	}
	if !res.WithHolder || res.HolderUnitPath != sysHolderUnit {
		t.Fatalf("应装了守护进程服务: %+v", res)
	}

	agentUnit := h.read(t, sysAgentUnit)
	for _, want := range []string{
		"After=network-online.target relaymesh-holder.service",
		"Wants=network-online.target relaymesh-holder.service",
		"--holder external",
		"Restart=always",
	} {
		if !strings.Contains(agentUnit, want) {
			t.Errorf("Agent 单元缺少 %q:\n%s", want, agentUnit)
		}
	}
	// 守护进程重启 / 停止不应连带 Agent（那样会话的存续又和 Agent 绑在一起了）。
	for _, bad := range []string{"Requires=", "BindsTo=", "PartOf="} {
		if strings.Contains(agentUnit, bad) {
			t.Errorf("Agent 单元不应强依赖守护进程（%s）:\n%s", bad, agentUnit)
		}
	}

	holderUnit := h.read(t, sysHolderUnit)
	for _, want := range []string{
		"Description=RelayMesh Terminal Session Holder",
		"ExecStart=/usr/local/bin/relaymesh-agent holder run",
		"--data-dir", "--max-sessions 6",
		"Restart=always", "RestartSec=2",
		"WantedBy=multi-user.target",
	} {
		if !strings.Contains(holderUnit, want) {
			t.Errorf("守护进程单元缺少 %q:\n%s", want, holderUnit)
		}
	}
	for _, bad := range []string{"--server", "--pin", "Wants=", "KillMode", "network-online"} {
		if strings.Contains(holderUnit, bad) {
			t.Errorf("守护进程单元不应包含 %q:\n%s", bad, holderUnit)
		}
	}

	want := []string{
		"systemctl daemon-reload",
		"systemctl enable relaymesh-holder.service",
		"systemctl start relaymesh-holder.service",
		"systemctl enable relaymesh-agent.service",
		"systemctl restart relaymesh-agent.service",
	}
	if strings.Join(h.cmds, "\n") != strings.Join(want, "\n") {
		t.Fatalf("命令序列不对:\n%s", strings.Join(h.cmds, "\n"))
	}
}

func TestReinstallNeverRestartsHolder(t *testing.T) {
	h := newFakeHost(t, "linux", 0)
	c, cfg := autoConfig(t, common{})
	env := testEnv(t)
	if _, err := doInstall(env, c, cfg, h.sys); err != nil {
		t.Fatal(err)
	}

	// 换服务端地址后重装：Agent 重启，守护进程只做 start（对运行中的是空操作），会话得以保留。
	c2 := c
	c2.server = "https://other.example.com:8443"
	cfg2, err := c2.resolve(env, false)
	if err != nil {
		t.Fatal(err)
	}
	cfg2.dataDir = cfg.dataDir
	h.cmds = nil
	res, err := doInstall(env, c2, cfg2, h.sys)
	if err != nil {
		t.Fatal(err)
	}
	for _, cmd := range h.cmds {
		if strings.Contains(cmd, "restart relaymesh-holder") || strings.Contains(cmd, "stop relaymesh-holder") || strings.Contains(cmd, "disable") {
			t.Fatalf("重装不得重启 / 停止守护进程（会结束会话）: %v", h.cmds)
		}
	}
	if len(res.Notes) != 0 {
		t.Fatalf("守护进程配置没变不应有提示: %v", res.Notes)
	}
	if !strings.Contains(h.read(t, sysAgentUnit), "other.example.com:8443") {
		t.Fatal("Agent 单元应更新")
	}
}

func TestReinstallWithChangedHolderConfigOnlyAdvises(t *testing.T) {
	h := newFakeHost(t, "linux", 0)
	c, cfg := autoConfig(t, common{})
	env := testEnv(t)
	if _, err := doInstall(env, c, cfg, h.sys); err != nil {
		t.Fatal(err)
	}
	c2 := c
	c2.maxSessions = 3
	cfg2, err := c2.resolve(env, false)
	if err != nil {
		t.Fatal(err)
	}
	cfg2.dataDir = cfg.dataDir
	h.cmds = nil
	res, err := doInstall(env, c2, cfg2, h.sys)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Notes) != 1 || !strings.Contains(res.Notes[0], "holder restart") {
		t.Fatalf("应提示用 holder restart 让新配置生效: %v", res.Notes)
	}
	assertCommandLineEndsWith(t, res.Notes[0], "holder restart")
	if !strings.Contains(h.read(t, sysHolderUnit), "--max-sessions 3") {
		t.Fatal("新配置应已写入，供下次启动使用")
	}
	for _, cmd := range h.cmds {
		if strings.Contains(cmd, "restart relaymesh-holder") {
			t.Fatalf("配置变化也不能自动重启守护进程: %v", h.cmds)
		}
	}
}

func TestInstallEmbeddedOverExistingHolderIsRefused(t *testing.T) {
	h := newFakeHost(t, "linux", 0)
	c, cfg := autoConfig(t, common{})
	env := testEnv(t)
	if _, err := doInstall(env, c, cfg, h.sys); err != nil {
		t.Fatal(err)
	}
	before := h.read(t, sysAgentUnit)

	c2 := c
	c2.holderMode = "embedded"
	cfg2, err := c2.resolve(env, false)
	if err != nil {
		t.Fatal(err)
	}
	cfg2.dataDir = cfg.dataDir
	h.cmds = nil
	_, err = doInstall(env, c2, cfg2, h.sys)
	if err == nil || !strings.Contains(err.Error(), "uninstall") {
		t.Fatalf("已有守护进程时改内嵌应被拒绝并指引先卸载: %v", err)
	}
	if len(h.cmds) != 0 || h.read(t, sysAgentUnit) != before || !h.exists(sysHolderUnit) {
		t.Fatal("被拒绝时不应有任何副作用")
	}
}

func TestFreshInstallFailureAfterHolderStartCleansHolderUp(t *testing.T) {
	h := newFakeHost(t, "linux", 0)
	h.runErr["systemctl restart relaymesh-agent"] = errors.New("exit status 1")
	h.runOut["systemctl restart relaymesh-agent"] = "Job for relaymesh-agent.service failed"
	c, cfg := autoConfig(t, common{})
	_, err := doInstall(testEnv(t), c, cfg, h.sys)
	if err == nil {
		t.Fatal("Agent 启动失败应报错")
	}
	if h.exists(sysAgentUnit) || h.exists(sysHolderUnit) {
		t.Fatal("全新安装失败后两份单元都应撤销")
	}
	// 新装的守护进程已经启动，里面不可能有用户的会话：停用它，免得留下一个没人管的进程。
	if !containsCmd(h.cmds, "systemctl disable --now relaymesh-holder.service") {
		t.Fatalf("应停用刚启动的守护进程: %v", h.cmds)
	}
}

func TestUpgradeFailureKeepsRunningHolder(t *testing.T) {
	h := newFakeHost(t, "linux", 0)
	c, cfg := autoConfig(t, common{})
	env := testEnv(t)
	if _, err := doInstall(env, c, cfg, h.sys); err != nil {
		t.Fatal(err)
	}
	goodAgent, goodHolder := h.read(t, sysAgentUnit), h.read(t, sysHolderUnit)

	c2 := c
	c2.server = "https://other.example.com:8443"
	c2.maxSessions = 2
	cfg2, err := c2.resolve(env, false)
	if err != nil {
		t.Fatal(err)
	}
	cfg2.dataDir = cfg.dataDir
	h.cmds = nil
	h.runErr["systemctl restart relaymesh-agent"] = errors.New("exit status 1")
	_, err = doInstall(env, c2, cfg2, h.sys)
	if err == nil {
		t.Fatal("应报错")
	}
	if h.read(t, sysAgentUnit) != goodAgent || h.read(t, sysHolderUnit) != goodHolder {
		t.Fatal("升级失败应把两份单元都恢复原样")
	}
	if containsCmd(h.cmds, "systemctl disable --now relaymesh-holder.service") {
		t.Fatalf("升级失败不能停用原本就在运行的守护进程（会结束会话）: %v", h.cmds)
	}
}

func TestInstallLinuxUserScopeWithHolder(t *testing.T) {
	h := newFakeHost(t, "linux", 1000)
	c, cfg := autoConfig(t, common{user: true})
	res, err := doInstall(testEnv(t), c, cfg, h.sys)
	if err != nil {
		t.Fatal(err)
	}
	if res.HolderUnitPath != "/home/alice/.config/systemd/user/relaymesh-holder.service" {
		t.Fatalf("用户级守护进程单元路径: %s", res.HolderUnitPath)
	}
	unit := h.read(t, res.HolderUnitPath)
	if !strings.Contains(unit, "WantedBy=default.target") || !strings.Contains(unit, "ExecStart=/home/alice/.local/bin/relaymesh-agent holder run") {
		t.Fatalf("用户级守护进程单元不对:\n%s", unit)
	}
	want := "systemctl --user daemon-reload|systemctl --user enable relaymesh-holder.service|systemctl --user start relaymesh-holder.service|" +
		"systemctl --user enable relaymesh-agent.service|systemctl --user restart relaymesh-agent.service|loginctl enable-linger alice"
	if strings.Join(h.cmds, "|") != want {
		t.Fatalf("命令序列不对: %v", h.cmds)
	}
}

func TestInstallSubcommandFlavorPutsHolderBehindSubcommand(t *testing.T) {
	h := newFakeHost(t, "linux", 0)
	c, cfg := autoConfig(t, common{})
	env := testEnv(t)
	env.Prog, env.ServiceArgs = "relaymesh agent", []string{"agent"}
	if _, err := doInstall(env, c, cfg, h.sys); err != nil {
		t.Fatal(err)
	}
	unit := h.read(t, sysHolderUnit)
	if !strings.Contains(unit, "ExecStart=/usr/local/bin/relaymesh agent holder run") {
		t.Fatalf("主程序形态下守护进程应通过 agent 子命令启动:\n%s", unit)
	}
}

func TestInstallDarwinWithHolder(t *testing.T) {
	holderPlist := "/Library/LaunchDaemons/com.relaymesh.holder.plist"
	agentPlist := "/Library/LaunchDaemons/com.relaymesh.agent.plist"

	t.Run("全新安装：先载入守护进程，再载入 Agent", func(t *testing.T) {
		h := newFakeHost(t, "darwin", 0)
		h.runErr["launchctl print"] = errors.New("exit status 113") // 守护进程尚未载入
		h.runErr["launchctl bootout"] = errors.New("exit status 113")
		c, cfg := autoConfig(t, common{})
		if _, err := doInstall(testEnv(t), c, cfg, h.sys); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(h.read(t, holderPlist), "<string>com.relaymesh.holder</string>") ||
			!strings.Contains(h.read(t, holderPlist), "<string>holder</string>") {
			t.Fatalf("守护进程 plist 不对:\n%s", h.read(t, holderPlist))
		}
		if !strings.Contains(h.read(t, agentPlist), "<string>external</string>") {
			t.Fatalf("Agent plist 应使用 --holder external:\n%s", h.read(t, agentPlist))
		}
		got := strings.Join(h.cmds, "|")
		want := "launchctl print system/com.relaymesh.holder|" +
			"launchctl bootstrap system " + holderPlist + "|launchctl enable system/com.relaymesh.holder|launchctl kickstart system/com.relaymesh.holder|" +
			"launchctl bootout system/com.relaymesh.agent|launchctl bootstrap system " + agentPlist + "|launchctl enable system/com.relaymesh.agent|launchctl kickstart -k system/com.relaymesh.agent"
		if got != want {
			t.Fatalf("命令序列不对:\n%s", strings.Join(h.cmds, "\n"))
		}
	})

	t.Run("守护进程已载入：升级时不碰它", func(t *testing.T) {
		h := newFakeHost(t, "darwin", 0) // launchctl print 成功 = 已载入
		c, cfg := autoConfig(t, common{})
		if _, err := doInstall(testEnv(t), c, cfg, h.sys); err != nil {
			t.Fatal(err)
		}
		for _, cmd := range h.cmds {
			if strings.Contains(cmd, "com.relaymesh.holder") && !strings.HasPrefix(cmd, "launchctl print") {
				t.Fatalf("已载入的守护进程不能被 bootout / kickstart（会结束会话）: %v", h.cmds)
			}
		}
	})

	t.Run("全新安装失败：卸载刚载入的守护进程并删除两份 plist", func(t *testing.T) {
		h := newFakeHost(t, "darwin", 0)
		h.runErr["launchctl print"] = errors.New("exit status 113")
		h.runErr["launchctl kickstart -k"] = errors.New("exit status 1")
		c, cfg := autoConfig(t, common{})
		if _, err := doInstall(testEnv(t), c, cfg, h.sys); err == nil {
			t.Fatal("应报错")
		}
		if h.exists(holderPlist) || h.exists(agentPlist) {
			t.Fatal("失败后不应残留 plist")
		}
		if !containsCmd(h.cmds, "launchctl bootout system/com.relaymesh.holder") {
			t.Fatalf("应卸载刚载入的守护进程: %v", h.cmds)
		}
	})

	t.Run("用户级", func(t *testing.T) {
		h := newFakeHost(t, "darwin", 501)
		h.runErr["launchctl print"] = errors.New("exit status 113")
		c, cfg := autoConfig(t, common{})
		res, err := doInstall(testEnv(t), c, cfg, h.sys)
		if err != nil {
			t.Fatal(err)
		}
		if res.HolderUnitPath != "/home/alice/Library/LaunchAgents/com.relaymesh.holder.plist" {
			t.Fatalf("路径: %s", res.HolderUnitPath)
		}
		if !strings.Contains(h.read(t, res.HolderUnitPath), "/home/alice/Library/Logs/relaymesh-holder.log") {
			t.Fatal("守护进程应有自己的日志文件")
		}
		if !containsCmd(h.cmds, "launchctl bootstrap gui/501 "+res.HolderUnitPath) {
			t.Fatalf("应使用 gui/<uid> 域: %v", h.cmds)
		}
	})
}

func TestUninstallRemovesBothUnitsAgentFirst(t *testing.T) {
	h := newFakeHost(t, "linux", 0)
	c, cfg := autoConfig(t, common{})
	env := testEnv(t)
	if _, err := doInstall(env, c, cfg, h.sys); err != nil {
		t.Fatal(err)
	}
	// 守护进程留下的状态：--purge 要一并清理。
	stateDir := holder.StateDir(cfg.dataDir)
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(stateDir+"/state.json", []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}

	h.cmds = nil
	res, err := doUninstall(env, c, true, cfg, h.sys)
	if err != nil || !res.Found {
		t.Fatalf("应找到并卸载: %v %+v", err, res)
	}
	if h.exists(sysAgentUnit) || h.exists(sysHolderUnit) {
		t.Fatal("两份单元都应删除")
	}
	want := "systemctl disable --now relaymesh-agent.service|systemctl disable --now relaymesh-holder.service|systemctl daemon-reload"
	if strings.Join(h.cmds, "|") != want {
		t.Fatalf("应先停 Agent 再停守护进程: %v", h.cmds)
	}
	if _, err := os.Stat(stateDir + "/state.json"); err == nil {
		t.Fatal("--purge 应清理守护进程的状态文件")
	}
}

func TestUninstallOnlyHolderLeftStillCleansUp(t *testing.T) {
	// 只剩守护进程单元（例如 Agent 单元被手动删了）：卸载也要能把它清掉。
	h := newFakeHost(t, "linux", 0)
	c, cfg := autoConfig(t, common{})
	env := testEnv(t)
	if _, err := doInstall(env, c, cfg, h.sys); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(h.sys.p(sysAgentUnit)); err != nil {
		t.Fatal(err)
	}
	h.cmds = nil
	res, err := doUninstall(env, c, false, cfg, h.sys)
	if err != nil || !res.Found {
		t.Fatalf("%v %+v", err, res)
	}
	if h.exists(sysHolderUnit) {
		t.Fatal("守护进程单元应被删除")
	}
	if !containsCmd(h.cmds, "systemctl disable --now relaymesh-holder.service") {
		t.Fatalf("应停用守护进程: %v", h.cmds)
	}
}

func TestReportServiceMentionsHolder(t *testing.T) {
	h := newFakeHost(t, "linux", 0)
	c, cfg := autoConfig(t, common{})
	env := testEnv(t)
	if _, err := doInstall(env, c, cfg, h.sys); err != nil {
		t.Fatal(err)
	}
	h.runOut["systemctl is-active"] = "active"
	env2 := testEnv(t)
	reportService(env2, h.sys)
	o := out(env2)
	if !strings.Contains(o, "会话守护进程") || strings.Count(o, "active") < 2 {
		t.Fatalf("状态应同时报告 Agent 与守护进程: %q", o)
	}
}

func containsCmd(cmds []string, want string) bool {
	for _, c := range cmds {
		if c == want {
			return true
		}
	}
	return false
}
