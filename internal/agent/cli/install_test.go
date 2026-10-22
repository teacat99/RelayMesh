//go:build !windows

// 这些测试用假的根目录与假的命令执行器模拟 Linux / macOS 的服务安装，依赖 Unix 的路径与文件权限语义，
// 所以只在非 Windows 上运行；Windows 的安装流程由 wintask_test.go 覆盖。

package cli

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/teacat99/RelayMesh/v2/internal/agent"
)

type fakeHost struct {
	sys  sysOps
	cmds []string
	// runErr 可按命令前缀注入失败。
	runErr map[string]error
	// runOut 可按命令前缀注入输出。
	runOut map[string]string
	// hook 在每条命令「成功」之后调用，用来模拟命令对系统造成的真实效果（例如 loginctl 创建 linger 文件）。
	hook func(line string)
}

func newFakeHost(t *testing.T, goos string, euid int) *fakeHost {
	t.Helper()
	root := t.TempDir()
	exe := filepath.Join(t.TempDir(), "agent-src")
	if err := os.WriteFile(exe, []byte("#!/bin/sh\necho agent v1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "run", "systemd", "system"), 0o755); err != nil {
		t.Fatal(err)
	}
	h := &fakeHost{runErr: map[string]error{}, runOut: map[string]string{}}
	h.sys = sysOps{
		goos: goos, euid: euid, home: "/home/alice", root: root, exe: exe, userName: "alice",
		run: func(name string, args ...string) (string, error) {
			line := strings.TrimSpace(name + " " + strings.Join(args, " "))
			h.cmds = append(h.cmds, line)
			for prefix, err := range h.runErr {
				if strings.HasPrefix(line, prefix) {
					return h.runOut[prefix], err
				}
			}
			for prefix, out := range h.runOut {
				if strings.HasPrefix(line, prefix) {
					return out, nil
				}
			}
			if h.hook != nil {
				h.hook(line)
			}
			return "", nil
		},
	}
	return h
}

func (h *fakeHost) read(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(h.sys.p(path))
	if err != nil {
		t.Fatalf("读取 %s 失败: %v", path, err)
	}
	return string(b)
}

func (h *fakeHost) exists(path string) bool {
	_, err := os.Stat(h.sys.p(path))
	return err == nil
}

func TestInstallLinuxSystem(t *testing.T) {
	h := newFakeHost(t, "linux", 0)
	pin := "sha256:" + strings.Repeat("ab", 32)
	c, cfg := testConfig(t, common{pins: stringList{pin}})
	env := testEnv(t)

	res, err := doInstall(env, c, cfg, h.sys)
	if err != nil {
		t.Fatal(err)
	}

	// 二进制被复制到系统目录，内容一致，且可执行。
	bin := "/usr/local/bin/relaymesh-agent"
	if res.BinPath != bin {
		t.Fatalf("BinPath = %s", res.BinPath)
	}
	if got, want := h.read(t, bin), "#!/bin/sh\necho agent v1\n"; got != want {
		t.Fatalf("复制的二进制内容不对: %q", got)
	}
	if st, _ := os.Stat(h.sys.p(bin)); st.Mode().Perm() != 0o755 {
		t.Fatalf("二进制权限 = %o", st.Mode().Perm())
	}

	// 单元文件：路径、权限、内容。
	unitPath := "/etc/systemd/system/relaymesh-agent.service"
	unit := h.read(t, unitPath)
	for _, want := range []string{
		"ExecStart=" + bin + " run --server https://relay.example.com --pin " + pin,
		"--data-dir", // 测试里显式给了目录，应被固化
	} {
		if !strings.Contains(unit, want) {
			t.Errorf("单元缺少 %q:\n%s", want, unit)
		}
	}
	if st, _ := os.Stat(h.sys.p(unitPath)); st.Mode().Perm() != 0o644 {
		t.Errorf("单元权限 = %o", st.Mode().Perm())
	}

	// 机器码在安装时就已固定，并与服务将读取的身份一致。
	id, err := agent.LoadIdentity(cfg.dataDir)
	if err != nil || id.MachineCode() != res.MachineCode {
		t.Fatalf("身份与安装结果不一致: %v %s vs %s", err, id.MachineCode(), res.MachineCode)
	}

	want := []string{
		"systemctl daemon-reload",
		"systemctl enable relaymesh-agent.service",
		"systemctl restart relaymesh-agent.service",
	}
	if strings.Join(h.cmds, "\n") != strings.Join(want, "\n") {
		t.Fatalf("命令序列不对:\n%s", strings.Join(h.cmds, "\n"))
	}
}

func TestInstallOmitsDefaultDirsFromUnit(t *testing.T) {
	// 没有显式指定目录时，不能把安装者当前解析出的路径固化进单元：运行时按「有效用户」决定默认目录。
	h := newFakeHost(t, "linux", 0)
	c := common{server: "https://relay.example.com"}
	cfg, err := c.resolve(testEnv(t), false)
	if err != nil {
		t.Fatal(err)
	}
	cfg.dataDir = t.TempDir() // 避免写到真实的 /var/lib
	if _, err := doInstall(testEnv(t), c, cfg, h.sys); err != nil {
		t.Fatal(err)
	}
	unit := h.read(t, "/etc/systemd/system/relaymesh-agent.service")
	if strings.Contains(unit, "--data-dir") || strings.Contains(unit, "--config-dir") {
		t.Fatalf("未显式指定时不应固化目录:\n%s", unit)
	}
}

func TestInstallIsIdempotentAndUpdates(t *testing.T) {
	h := newFakeHost(t, "linux", 0)
	c, cfg := testConfig(t, common{})
	env := testEnv(t)
	if _, err := doInstall(env, c, cfg, h.sys); err != nil {
		t.Fatal(err)
	}
	first := h.read(t, "/etc/systemd/system/relaymesh-agent.service")

	// 升级二进制并改服务端地址后重装：配置与二进制都被更新，机器码不变。
	if err := os.WriteFile(h.sys.exe, []byte("#!/bin/sh\necho agent v2\n"), 0o755); err != nil {
		t.Fatal(err)
	}
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
	second := h.read(t, "/etc/systemd/system/relaymesh-agent.service")
	if first == second || !strings.Contains(second, "other.example.com:8443") {
		t.Fatalf("重装应更新单元:\n%s", second)
	}
	if got := h.read(t, "/usr/local/bin/relaymesh-agent"); !strings.Contains(got, "v2") {
		t.Fatalf("重装应更新二进制: %q", got)
	}
	id, _ := agent.LoadIdentity(cfg.dataDir)
	if id.MachineCode() != res.MachineCode {
		t.Fatal("重装不应改变机器码")
	}
	if len(h.cmds) != 3 || h.cmds[2] != "systemctl restart relaymesh-agent.service" {
		t.Fatalf("重装应重启服务以应用新配置: %v", h.cmds)
	}
	// 目录里不能残留临时文件。
	entries, _ := os.ReadDir(h.sys.p("/etc/systemd/system"))
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".tmp-") {
			t.Errorf("残留临时文件: %s", e.Name())
		}
	}
}

func TestInstallLinuxNonRootNeedsUserFlag(t *testing.T) {
	h := newFakeHost(t, "linux", 1000)
	c, cfg := testConfig(t, common{})
	_, err := doInstall(testEnv(t), c, cfg, h.sys)
	if !errors.Is(err, errNeedRoot) {
		t.Fatalf("非 root 且没有 --user 应提示需要权限，得到 %v", err)
	}
	if len(h.cmds) != 0 || h.exists("/usr/local/bin/relaymesh-agent") {
		t.Fatal("失败时不应有任何副作用")
	}
}

func TestInstallLinuxUserScope(t *testing.T) {
	h := newFakeHost(t, "linux", 1000)
	c, cfg := testConfig(t, common{user: true})
	res, err := doInstall(testEnv(t), c, cfg, h.sys)
	if err != nil {
		t.Fatal(err)
	}
	if !res.User {
		t.Fatal("应为用户级安装")
	}
	unit := h.read(t, "/home/alice/.config/systemd/user/relaymesh-agent.service")
	if !strings.Contains(unit, "WantedBy=default.target") || !strings.Contains(unit, "ExecStart=/home/alice/.local/bin/relaymesh-agent run") {
		t.Fatalf("用户级单元不对:\n%s", unit)
	}
	// 服务起来之后，最后才顺手开启 linger（见 linger_test.go）。
	want := "systemctl --user daemon-reload|systemctl --user enable relaymesh-agent.service|systemctl --user restart relaymesh-agent.service|loginctl enable-linger alice"
	if strings.Join(h.cmds, "|") != want {
		t.Fatalf("命令序列不对: %v", h.cmds)
	}
	if !h.exists("/home/alice/.local/bin/relaymesh-agent") {
		t.Fatal("二进制应装到用户目录")
	}
}

func TestInstallFailsWithoutSystemd(t *testing.T) {
	h := newFakeHost(t, "linux", 0)
	if err := os.RemoveAll(h.sys.p("/run/systemd/system")); err != nil {
		t.Fatal(err)
	}
	c, cfg := testConfig(t, common{})
	_, err := doInstall(testEnv(t), c, cfg, h.sys)
	if err == nil || !strings.Contains(err.Error(), "systemd") {
		t.Fatalf("应明确提示没有 systemd，得到 %v", err)
	}
	if h.exists("/usr/local/bin/relaymesh-agent") || len(h.cmds) != 0 {
		t.Fatal("失败时不应有任何副作用")
	}
}

func TestInstallRejectsTemporaryIdentity(t *testing.T) {
	h := newFakeHost(t, "linux", 0)
	c, cfg := testConfig(t, common{temp: true})
	if _, err := doInstall(testEnv(t), c, cfg, h.sys); err == nil {
		t.Fatal("临时身份不能安装为常驻服务")
	}
}

func TestInstallReportsSystemctlFailure(t *testing.T) {
	h := newFakeHost(t, "linux", 0)
	h.runErr["systemctl enable"] = errors.New("exit status 1")
	h.runOut["systemctl enable"] = "Failed to enable unit: Access denied"
	c, cfg := testConfig(t, common{})
	_, err := doInstall(testEnv(t), c, cfg, h.sys)
	if err == nil || !strings.Contains(err.Error(), "Access denied") || !strings.Contains(err.Error(), "systemctl enable") {
		t.Fatalf("应带上命令与其输出，得到 %v", err)
	}
}

func TestInstallRollsBackFreshUnitOnFailure(t *testing.T) {
	// 真实环境里复现过：WSL 的用户级 systemd 总线不可达，daemon-reload 失败，
	// 此前会留下一份没人管的单元文件。现在必须撤销，并给出可操作的提示。
	h := newFakeHost(t, "linux", 1000)
	h.runErr["systemctl --user daemon-reload"] = errors.New("exit status 1")
	h.runOut["systemctl --user daemon-reload"] = "Failed to connect to bus: No such file or directory"
	c, cfg := testConfig(t, common{user: true})
	_, err := doInstall(testEnv(t), c, cfg, h.sys)
	if err == nil {
		t.Fatal("服务管理器不可达时应失败")
	}
	for _, want := range []string{"Failed to connect to bus", "loginctl enable-linger", "relaymesh-agent run", "已撤销"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("错误信息应包含 %q:\n%v", want, err)
		}
	}
	if h.exists("/home/alice/.config/systemd/user/relaymesh-agent.service") {
		t.Fatal("失败后不应残留单元文件")
	}
	if !h.exists("/home/alice/.local/bin/relaymesh-agent") {
		t.Fatal("可执行文件应保留，方便修复环境后直接重试")
	}
	if len(h.cmds) != 1 {
		t.Fatalf("第一条命令就失败时不应再触发回滚命令: %v", h.cmds)
	}
}

func TestInstallRestoresPreviousUnitOnFailure(t *testing.T) {
	h := newFakeHost(t, "linux", 0)
	c, cfg := testConfig(t, common{})
	env := testEnv(t)
	if _, err := doInstall(env, c, cfg, h.sys); err != nil {
		t.Fatal(err)
	}
	unitPath := "/etc/systemd/system/relaymesh-agent.service"
	good := h.read(t, unitPath)

	c2 := c
	c2.server = "https://other.example.com:8443"
	cfg2, err := c2.resolve(env, false)
	if err != nil {
		t.Fatal(err)
	}
	cfg2.dataDir = cfg.dataDir
	h.cmds = nil
	h.runErr["systemctl restart"] = errors.New("exit status 1")
	h.runOut["systemctl restart"] = "Job for relaymesh-agent.service failed"
	_, err = doInstall(env, c2, cfg2, h.sys)
	if err == nil || !strings.Contains(err.Error(), "已恢复安装前的服务配置") {
		t.Fatalf("应说明已恢复旧配置: %v", err)
	}
	if got := h.read(t, unitPath); got != good {
		t.Fatalf("失败后应恢复原有单元:\n%s", got)
	}
	reloads := 0
	for _, c := range h.cmds {
		if c == "systemctl daemon-reload" {
			reloads++
		}
	}
	if reloads != 2 {
		t.Fatalf("回滚后应让 systemd 重新读取旧单元（共 2 次 daemon-reload）: %v", h.cmds)
	}
	entries, _ := os.ReadDir(h.sys.p("/etc/systemd/system"))
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".tmp-") {
			t.Errorf("残留临时文件: %s", e.Name())
		}
	}
}

func TestInstallDarwinRollsBackOnFailure(t *testing.T) {
	plist := "/Library/LaunchDaemons/com.relaymesh.agent.plist"
	t.Run("全新安装失败：卸载新任务并删除 plist", func(t *testing.T) {
		h := newFakeHost(t, "darwin", 0)
		h.runErr["launchctl kickstart"] = errors.New("exit status 1")
		c, cfg := testConfig(t, common{})
		if _, err := doInstall(testEnv(t), c, cfg, h.sys); err == nil {
			t.Fatal("kickstart 失败应报错")
		}
		if h.exists(plist) {
			t.Fatal("失败后不应残留 plist")
		}
		if got := strings.Join(h.cmds, "|"); !strings.HasSuffix(got, "launchctl kickstart -k system/com.relaymesh.agent|launchctl bootout system/com.relaymesh.agent") {
			t.Fatalf("应在失败后卸载刚载入的任务: %v", h.cmds)
		}
	})
	t.Run("升级失败：恢复旧 plist 并重新载入旧任务", func(t *testing.T) {
		h := newFakeHost(t, "darwin", 0)
		c, cfg := testConfig(t, common{})
		env := testEnv(t)
		if _, err := doInstall(env, c, cfg, h.sys); err != nil {
			t.Fatal(err)
		}
		good := h.read(t, plist)
		c2 := c
		c2.server = "https://other.example.com:8443"
		cfg2, err := c2.resolve(env, false)
		if err != nil {
			t.Fatal(err)
		}
		cfg2.dataDir = cfg.dataDir
		h.cmds = nil
		h.runErr["launchctl bootstrap system /Library/LaunchDaemons"] = errors.New("exit status 5")
		if _, err := doInstall(env, c2, cfg2, h.sys); err == nil {
			t.Fatal("bootstrap 失败应报错")
		}
		if got := h.read(t, plist); got != good {
			t.Fatalf("应恢复旧 plist:\n%s", got)
		}
		// 第 0 步 bootout 已卸掉旧任务，回滚必须把它重新载入（bootstrap 本身又会失败，因为注入了故障——这里只看是否尝试）。
		last := h.cmds[len(h.cmds)-1]
		if !strings.HasPrefix(last, "launchctl bootstrap system ") {
			t.Fatalf("应尝试重新载入旧任务: %v", h.cmds)
		}
	})
}

func TestDescribeSystemdState(t *testing.T) {
	cases := map[string]string{
		"active\n":    "active（运行中）",
		"failed":      "failed（已失败）",
		"":            "未知",
		"weird-state": "weird-state",
	}
	for in, want := range cases {
		if got := describeSystemdState(in); got != want {
			t.Errorf("describeSystemdState(%q) = %q，期望 %q", in, got, want)
		}
	}
	if got := describeSystemdState("Failed to connect to bus: No such file or directory"); !strings.Contains(got, "无法确定") {
		t.Errorf("总线不可达不应被当成服务状态: %q", got)
	}
}

func TestServiceFailureHintOnlyForUserBusProblems(t *testing.T) {
	if serviceFailureHint("relaymesh-agent", "linux", true, "Failed to connect to bus: x") == "" {
		t.Error("用户级 + 总线不可达应给提示")
	}
	if serviceFailureHint("relaymesh-agent", "linux", false, "Failed to connect to bus: x") != "" {
		t.Error("系统级安装不该给用户总线的提示")
	}
	if serviceFailureHint("relaymesh-agent", "linux", true, "Access denied") != "" {
		t.Error("无关的失败不该附带提示")
	}
	if serviceFailureHint("relaymesh-agent", "darwin", true, "Failed to connect to bus") != "" {
		t.Error("macOS 不适用")
	}
}

func TestInstallUsesSubcommandFlavor(t *testing.T) {
	// 主程序的 `relaymesh agent` 形态：服务运行的是 relaymesh 二进制的 agent 子命令。
	h := newFakeHost(t, "linux", 0)
	c, cfg := testConfig(t, common{})
	env := testEnv(t)
	env.Prog, env.ServiceArgs = "relaymesh agent", []string{"agent"}
	res, err := doInstall(env, c, cfg, h.sys)
	if err != nil {
		t.Fatal(err)
	}
	if res.BinPath != "/usr/local/bin/relaymesh" {
		t.Fatalf("BinPath = %s", res.BinPath)
	}
	unit := h.read(t, "/etc/systemd/system/relaymesh-agent.service")
	if !strings.Contains(unit, "ExecStart=/usr/local/bin/relaymesh agent run --server") {
		t.Fatalf("ExecStart 应包含 agent 子命令:\n%s", unit)
	}
}

func TestInstallDarwin(t *testing.T) {
	t.Run("系统级 LaunchDaemon", func(t *testing.T) {
		h := newFakeHost(t, "darwin", 0)
		h.runErr["launchctl bootout"] = errors.New("exit status 113") // 尚未加载属正常
		c, cfg := testConfig(t, common{})
		if _, err := doInstall(testEnv(t), c, cfg, h.sys); err != nil {
			t.Fatalf("bootout 失败（服务尚未加载）应被忽略: %v", err)
		}
		plist := h.read(t, "/Library/LaunchDaemons/com.relaymesh.agent.plist")
		if !strings.Contains(plist, "<string>/usr/local/bin/relaymesh-agent</string>") || !strings.Contains(plist, "/var/log/relaymesh-agent.log") {
			t.Fatalf("plist 不对:\n%s", plist)
		}
		want := "launchctl bootout system/com.relaymesh.agent|launchctl bootstrap system /Library/LaunchDaemons/com.relaymesh.agent.plist|launchctl enable system/com.relaymesh.agent|launchctl kickstart -k system/com.relaymesh.agent"
		if strings.Join(h.cmds, "|") != want {
			t.Fatalf("命令序列不对:\n%s", strings.Join(h.cmds, "\n"))
		}
	})
	t.Run("非 root 自动装为用户级 LaunchAgent", func(t *testing.T) {
		h := newFakeHost(t, "darwin", 501)
		c, cfg := testConfig(t, common{})
		res, err := doInstall(testEnv(t), c, cfg, h.sys)
		if err != nil {
			t.Fatal(err)
		}
		if !res.User {
			t.Fatal("非 root 应装为用户级")
		}
		plist := h.read(t, "/home/alice/Library/LaunchAgents/com.relaymesh.agent.plist")
		if !strings.Contains(plist, "/home/alice/Library/Logs/relaymesh-agent.log") {
			t.Fatalf("plist 不对:\n%s", plist)
		}
		if h.cmds[1] != "launchctl bootstrap gui/501 /home/alice/Library/LaunchAgents/com.relaymesh.agent.plist" {
			t.Fatalf("应使用 gui/<uid> 域: %v", h.cmds)
		}
	})
}

func TestInstallUnsupportedOS(t *testing.T) {
	h := newFakeHost(t, "freebsd", 0)
	c, cfg := testConfig(t, common{})
	if _, err := doInstall(testEnv(t), c, cfg, h.sys); !errors.Is(err, errNoSystem) {
		t.Fatalf("应提示不支持，得到 %v", err)
	}
}

func TestUninstall(t *testing.T) {
	h := newFakeHost(t, "linux", 0)
	c, cfg := testConfig(t, common{})
	env := testEnv(t)
	if _, err := doInstall(env, c, cfg, h.sys); err != nil {
		t.Fatal(err)
	}
	id, _ := agent.LoadIdentity(cfg.dataDir)
	knownPath := filepath.Join(cfg.cfgDir, "known_servers")
	if err := os.WriteFile(knownPath, []byte("relay.example.com:443 sha256:aa\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	h.cmds = nil
	res, err := doUninstall(env, c, false, cfg, h.sys)
	if err != nil || !res.Found {
		t.Fatalf("应找到并卸载: %v %+v", err, res)
	}
	if h.exists("/etc/systemd/system/relaymesh-agent.service") || h.exists("/usr/local/bin/relaymesh-agent") {
		t.Fatal("单元与二进制应被删除")
	}
	if strings.Join(h.cmds, "|") != "systemctl disable --now relaymesh-agent.service|systemctl daemon-reload" {
		t.Fatalf("命令序列不对: %v", h.cmds)
	}
	// 默认保留身份：重装后机器码不变。
	if again, err := agent.LoadIdentity(cfg.dataDir); err != nil || again.MachineCode() != id.MachineCode() {
		t.Fatal("卸载默认应保留身份")
	}
	if _, err := os.Stat(knownPath); err != nil {
		t.Fatal("卸载默认应保留已信任服务器记录")
	}

	// --purge 才删除身份与信任记录。
	if _, err := doInstall(env, c, cfg, h.sys); err != nil {
		t.Fatal(err)
	}
	if _, err := doUninstall(env, c, true, cfg, h.sys); err != nil {
		t.Fatal(err)
	}
	if _, err := agent.LoadIdentity(cfg.dataDir); !errors.Is(err, agent.ErrNoIdentity) {
		t.Fatalf("--purge 后身份应被删除: %v", err)
	}
	if _, err := os.Stat(knownPath); err == nil {
		t.Fatal("--purge 后信任记录应被删除")
	}
	// 清空之后不留空目录。
	if _, err := os.Stat(cfg.dataDir); err == nil {
		t.Fatalf("--purge 后清空的数据目录应被移除: %s", cfg.dataDir)
	}
}

// 数据目录里还有别的文件时（例如用户把 --data-dir 指向了自己的目录），--purge 只删 Agent 自己的文件，目录与其他文件原样保留。
func TestUninstallPurgeKeepsForeignFiles(t *testing.T) {
	h := newFakeHost(t, "linux", 0)
	c, cfg := testConfig(t, common{})
	env := testEnv(t)
	if _, err := doInstall(env, c, cfg, h.sys); err != nil {
		t.Fatal(err)
	}
	foreign := filepath.Join(cfg.dataDir, "notes.txt")
	if err := os.WriteFile(foreign, []byte("keep me"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := doUninstall(env, c, true, cfg, h.sys); err != nil {
		t.Fatal(err)
	}
	if _, err := agent.LoadIdentity(cfg.dataDir); !errors.Is(err, agent.ErrNoIdentity) {
		t.Fatalf("身份应被删除: %v", err)
	}
	if b, err := os.ReadFile(foreign); err != nil || string(b) != "keep me" {
		t.Fatalf("目录里不属于 Agent 的文件必须原样保留: %v %q", err, b)
	}
}

func TestUninstallWhenNothingInstalled(t *testing.T) {
	h := newFakeHost(t, "linux", 0)
	c, cfg := testConfig(t, common{})
	res, err := doUninstall(testEnv(t), c, false, cfg, h.sys)
	if err != nil {
		t.Fatal(err)
	}
	if res.Found || len(h.cmds) != 0 {
		t.Fatalf("没有安装时不应执行任何命令: %+v %v", res, h.cmds)
	}
}

func TestUninstallToleratesStopFailure(t *testing.T) {
	h := newFakeHost(t, "linux", 0)
	c, cfg := testConfig(t, common{})
	env := testEnv(t)
	if _, err := doInstall(env, c, cfg, h.sys); err != nil {
		t.Fatal(err)
	}
	h.runErr["systemctl disable"] = errors.New("unit not loaded")
	if _, err := doUninstall(env, c, false, cfg, h.sys); err != nil {
		t.Fatalf("停止失败不应阻止清理: %v", err)
	}
	if h.exists("/etc/systemd/system/relaymesh-agent.service") {
		t.Fatal("单元文件应仍被删除")
	}
}

func TestInstallBinaryNeverClobbersItself(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "bin")
	if err := os.WriteFile(f, []byte("precious"), 0o755); err != nil {
		t.Fatal(err)
	}
	// 源与目标是同一个文件：若直接截断再复制，会把自己清空。
	if err := installBinary(f, f); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(f); string(b) != "precious" {
		t.Fatalf("自我复制不应破坏文件: %q", b)
	}
	// 目标是指向源的符号链接：同样不能破坏。
	link := filepath.Join(dir, "link")
	if err := os.Symlink(f, link); err != nil {
		t.Fatal(err)
	}
	if err := installBinary(f, link); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(f); string(b) != "precious" {
		t.Fatalf("经符号链接复制不应破坏源: %q", b)
	}
}

func TestReportService(t *testing.T) {
	h := newFakeHost(t, "linux", 0)
	env := testEnv(t)
	reportService(env, h.sys)
	if out := env.Stdout.(*syncBuffer).String(); !strings.Contains(out, "未安装") {
		t.Fatalf("没有服务时应提示未安装: %q", out)
	}

	c, cfg := testConfig(t, common{})
	if _, err := doInstall(env, c, cfg, h.sys); err != nil {
		t.Fatal(err)
	}
	h.runOut["systemctl is-active"] = "active"
	env2 := testEnv(t)
	reportService(env2, h.sys)
	out := env2.Stdout.(*syncBuffer).String()
	if !strings.Contains(out, "系统服务") || !strings.Contains(out, "active") {
		t.Fatalf("应报告系统服务已安装且 active: %q", out)
	}
}
