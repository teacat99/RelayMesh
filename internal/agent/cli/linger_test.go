//go:build !windows

package cli

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// lingerHost 在 fakeHost 上模拟 loginctl 的真实效果：enable-linger 在 /var/lib/systemd/linger 下建同名文件，disable-linger 删掉它。
func lingerHost(t *testing.T, euid int) *fakeHost {
	t.Helper()
	h := newFakeHost(t, "linux", euid)
	file := func(name string) string { return h.sys.p(filepath.Join(lingerDir, name)) }
	h.hook = func(line string) {
		switch {
		case strings.HasPrefix(line, "loginctl enable-linger "):
			name := strings.TrimPrefix(line, "loginctl enable-linger ")
			_ = os.MkdirAll(filepath.Dir(file(name)), 0o755)
			_ = os.WriteFile(file(name), nil, 0o644)
		case strings.HasPrefix(line, "loginctl disable-linger "):
			_ = os.Remove(file(strings.TrimPrefix(line, "loginctl disable-linger ")))
		}
	}
	return h
}

func (h *fakeHost) lingerOn(name string) bool { return h.sys.lingerOn(name) }

func (h *fakeHost) countCmds(prefix string) int {
	n := 0
	for _, c := range h.cmds {
		if strings.HasPrefix(c, prefix) {
			n++
		}
	}
	return n
}

func TestInstallUserScopeEnablesLinger(t *testing.T) {
	h := lingerHost(t, 1000)
	c, cfg := testConfig(t, common{user: true})
	res, err := doInstall(testEnv(t), c, cfg, h.sys)
	if err != nil {
		t.Fatal(err)
	}
	if res.Linger != lingerEnabled || res.LingerUser != "alice" {
		t.Fatalf("应由安装器开启 linger: %+v", res)
	}
	if !h.lingerOn("alice") {
		t.Fatal("linger 应已开启")
	}
	if last := h.cmds[len(h.cmds)-1]; last != "loginctl enable-linger alice" {
		t.Fatalf("linger 必须排在服务启动之后，最后一条命令是 %q", last)
	}
	if b, err := os.ReadFile(filepath.Join(cfg.dataDir, lingerMarkerFile)); err != nil || strings.TrimSpace(string(b)) != "alice" {
		t.Fatalf("应留下「由安装器开启」的记号: %v %q", err, b)
	}
}

func TestInstallLingerAlreadyOnIsLeftAlone(t *testing.T) {
	h := lingerHost(t, 1000)
	if err := os.MkdirAll(h.sys.p(lingerDir), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(h.sys.p(filepath.Join(lingerDir, "alice")), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	c, cfg := testConfig(t, common{user: true})
	res, err := doInstall(testEnv(t), c, cfg, h.sys)
	if err != nil {
		t.Fatal(err)
	}
	if res.Linger != lingerAlready {
		t.Fatalf("本来就开着：应报告 already，得到 %q", res.Linger)
	}
	if h.countCmds("loginctl") != 0 {
		t.Fatalf("已经开着就不该再执行 loginctl: %v", h.cmds)
	}
	if _, err := os.Stat(filepath.Join(cfg.dataDir, lingerMarkerFile)); err == nil {
		t.Fatal("不是安装器开的，不能留下记号（否则卸载时会误关别人开的 linger）")
	}
}

// 没有权限等原因开启失败：安装本身必须成功（服务已经装好并在运行），只是如实告知并给出手动命令。
func TestInstallLingerFailureDoesNotFailInstall(t *testing.T) {
	h := lingerHost(t, 1000)
	h.runErr["loginctl enable-linger"] = errors.New("exit status 1")
	h.runOut["loginctl enable-linger"] = "Failed to set linger: Interactive authentication required.\nsecond line"
	c, cfg := testConfig(t, common{user: true})
	res, err := doInstall(testEnv(t), c, cfg, h.sys)
	if err != nil {
		t.Fatalf("linger 失败不应让安装失败: %v", err)
	}
	if res.Linger != lingerFailed || !strings.Contains(res.LingerDetail, "Interactive authentication required") || strings.Contains(res.LingerDetail, "second line") {
		t.Fatalf("应记录失败原因（只取首行）: %+v", res)
	}
	if _, err := os.Stat(filepath.Join(cfg.dataDir, lingerMarkerFile)); err == nil {
		t.Fatal("没开成功就不能留下记号")
	}
	if !h.exists("/home/alice/.config/systemd/user/relaymesh-agent.service") {
		t.Fatal("服务应已安装")
	}

	env := testEnv(t)
	reportLinger(env, res)
	out := env.Stdout.(*syncBuffer).String()
	for _, want := range []string{"没能自动开启 linger", "Interactive authentication required", "loginctl enable-linger alice", "sudo"} {
		if !strings.Contains(out, want) {
			t.Errorf("提示里应包含 %q:\n%s", want, out)
		}
	}
}

func TestInstallNoLingerFlag(t *testing.T) {
	h := lingerHost(t, 1000)
	c, cfg := testConfig(t, common{user: true, noLinger: true})
	res, err := doInstall(testEnv(t), c, cfg, h.sys)
	if err != nil {
		t.Fatal(err)
	}
	if res.Linger != lingerSkipped || h.countCmds("loginctl") != 0 || h.lingerOn("alice") {
		t.Fatalf("--no-linger 不该碰 linger: %+v %v", res, h.cmds)
	}
	env := testEnv(t)
	reportLinger(env, res)
	if out := env.Stdout.(*syncBuffer).String(); !strings.Contains(out, "--no-linger") || !strings.Contains(out, "loginctl enable-linger alice") {
		t.Fatalf("应说明是按 --no-linger 跳过，并给出手动命令:\n%s", out)
	}
}

// 系统级服务本来就不依赖登录会话；macOS 没有 linger 的概念。
func TestInstallSystemScopeAndDarwinIgnoreLinger(t *testing.T) {
	h := lingerHost(t, 0)
	c, cfg := testConfig(t, common{})
	res, err := doInstall(testEnv(t), c, cfg, h.sys)
	if err != nil {
		t.Fatal(err)
	}
	if res.Linger != lingerNA || h.countCmds("loginctl") != 0 {
		t.Fatalf("系统级安装不该碰 linger: %+v %v", res, h.cmds)
	}

	d := newFakeHost(t, "darwin", 501)
	c, cfg = testConfig(t, common{user: true})
	res, err = doInstall(testEnv(t), c, cfg, d.sys)
	if err != nil {
		t.Fatal(err)
	}
	if res.Linger != lingerNA || d.countCmds("loginctl") != 0 {
		t.Fatalf("macOS 不适用 linger: %+v %v", res, d.cmds)
	}
}

// 安装失败并回滚时，不能留下对 linger 的改动（linger 排在所有服务命令之后）。
func TestInstallFailureNeverTouchesLinger(t *testing.T) {
	h := lingerHost(t, 1000)
	h.runErr["systemctl --user enable"] = errors.New("exit status 1")
	c, cfg := testConfig(t, common{user: true})
	if _, err := doInstall(testEnv(t), c, cfg, h.sys); err == nil {
		t.Fatal("服务命令失败时应失败")
	}
	if h.countCmds("loginctl") != 0 || h.lingerOn("alice") {
		t.Fatalf("失败的安装不能动 linger: %v", h.cmds)
	}
}

// 升级 / 重装：第二次安装发现 linger 已开（是我们上次开的），要保持「由安装器开启」的身份，卸载时仍会关闭。
func TestReinstallKeepsLingerOwnership(t *testing.T) {
	h := lingerHost(t, 1000)
	c, cfg := testConfig(t, common{user: true})
	env := testEnv(t)
	if _, err := doInstall(env, c, cfg, h.sys); err != nil {
		t.Fatal(err)
	}
	h.cmds = nil
	res, err := doInstall(env, c, cfg, h.sys)
	if err != nil {
		t.Fatal(err)
	}
	if res.Linger != lingerEnabled {
		t.Fatalf("重装应仍视为由安装器开启: %q", res.Linger)
	}
	if h.countCmds("loginctl") != 0 {
		t.Fatalf("已经开着，重装不必再执行 loginctl: %v", h.cmds)
	}
	if _, err := os.Stat(filepath.Join(cfg.dataDir, lingerMarkerFile)); err != nil {
		t.Fatal("记号必须保留")
	}
}

func TestUninstallDisablesLingerLast(t *testing.T) {
	h := lingerHost(t, 1000)
	c, cfg := testConfig(t, common{user: true})
	env := testEnv(t)
	if _, err := doInstall(env, c, cfg, h.sys); err != nil {
		t.Fatal(err)
	}
	h.cmds = nil
	res, err := doUninstall(env, c, true, cfg, h.sys)
	if err != nil {
		t.Fatal(err)
	}
	if !res.LingerReleased || res.LingerNote != "" {
		t.Fatalf("应关闭由安装器开启的 linger: %+v", res)
	}
	if h.lingerOn("alice") {
		t.Fatal("linger 应已关闭")
	}
	// 关闭 linger 后用户管理器可能立刻停止并结束本进程（远程卸载时本进程就在它名下），所以必须是最后一条命令。
	if last := h.cmds[len(h.cmds)-1]; last != "loginctl disable-linger alice" {
		t.Fatalf("disable-linger 必须排在最后，得到 %v", h.cmds)
	}
	if got := strings.Join(h.cmds, "|"); !strings.Contains(got, "daemon-reload|loginctl disable-linger alice") {
		t.Fatalf("命令序列不对: %s", got)
	}
	// 记号不能让 --purge 之后的数据目录清不空
	if _, err := os.Stat(cfg.dataDir); err == nil {
		t.Fatalf("--purge 后数据目录应被清空移除（记号文件不能残留）: %s", cfg.dataDir)
	}
}

func TestUninstallLeavesForeignLingerAlone(t *testing.T) {
	h := lingerHost(t, 1000)
	if err := os.MkdirAll(h.sys.p(lingerDir), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(h.sys.p(filepath.Join(lingerDir, "alice")), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	c, cfg := testConfig(t, common{user: true})
	env := testEnv(t)
	if _, err := doInstall(env, c, cfg, h.sys); err != nil {
		t.Fatal(err)
	}
	h.cmds = nil
	res, err := doUninstall(env, c, false, cfg, h.sys)
	if err != nil {
		t.Fatal(err)
	}
	if res.LingerReleased || h.countCmds("loginctl") != 0 || !h.lingerOn("alice") {
		t.Fatalf("不是安装器开的 linger 卸载时必须原样保留: %+v %v", res, h.cmds)
	}
}

func TestUninstallLingerDisableFailureIsReported(t *testing.T) {
	h := lingerHost(t, 1000)
	c, cfg := testConfig(t, common{user: true})
	env := testEnv(t)
	if _, err := doInstall(env, c, cfg, h.sys); err != nil {
		t.Fatal(err)
	}
	h.runErr["loginctl disable-linger"] = errors.New("exit status 1")
	h.runOut["loginctl disable-linger"] = "Failed to unset linger: Access denied"
	res, err := doUninstall(env, c, false, cfg, h.sys)
	if err != nil {
		t.Fatalf("关闭 linger 失败不应让卸载失败: %v", err)
	}
	if res.LingerReleased || !strings.Contains(res.LingerNote, "Access denied") || !strings.Contains(res.LingerNote, "sudo loginctl disable-linger alice") {
		t.Fatalf("应如实报告并给出手动命令: %+v", res)
	}
	if _, err := os.Stat(filepath.Join(cfg.dataDir, lingerMarkerFile)); err == nil {
		t.Fatal("记号无论成败都应清掉，不留清不掉的文件")
	}
}

func TestUninstallSystemScopeNeverTouchesLinger(t *testing.T) {
	h := lingerHost(t, 0)
	c, cfg := testConfig(t, common{})
	env := testEnv(t)
	if _, err := doInstall(env, c, cfg, h.sys); err != nil {
		t.Fatal(err)
	}
	// 即使数据目录里恰好有个同名记号（比如手动拷贝），系统级卸载也不该去关任何人的 linger
	if err := os.WriteFile(filepath.Join(cfg.dataDir, lingerMarkerFile), []byte("alice\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	h.cmds = nil
	if _, err := doUninstall(env, c, false, cfg, h.sys); err != nil {
		t.Fatal(err)
	}
	if h.countCmds("loginctl") != 0 {
		t.Fatalf("系统级卸载不该碰 linger: %v", h.cmds)
	}
}

// 远程卸载的命令行必须带上 --user 与数据目录：卸载进程靠它们找到服务与 linger 记号。
func TestRemoteUninstallArgsCarryWhatLingerNeeds(t *testing.T) {
	cfg := resolved{dataDir: "/home/alice/.local/share/relaymesh-agent", cfgDir: "/home/alice/.config/relaymesh-agent"}
	got := strings.Join(uninstallArgs(true, true, cfg), " ")
	for _, want := range []string{"uninstall --yes", "--user", "--purge", "--data-dir /home/alice/.local/share/relaymesh-agent"} {
		if !strings.Contains(got, want) {
			t.Errorf("卸载进程的命令行应包含 %q: %s", want, got)
		}
	}
}

func TestReportLingerMessages(t *testing.T) {
	cases := []struct {
		res  installResult
		want []string
	}{
		{installResult{Linger: lingerEnabled, LingerUser: "alice"}, []string{"已为用户 alice 开启 linger", "卸载时会一并关闭"}},
		{installResult{Linger: lingerAlready}, []string{"linger 已处于开启状态"}},
		{installResult{Linger: lingerFailed}, []string{"没能自动开启 linger", "原因未知", "loginctl enable-linger $USER"}},
	}
	for _, tc := range cases {
		env := testEnv(t)
		reportLinger(env, tc.res)
		out := env.Stdout.(*syncBuffer).String()
		for _, want := range tc.want {
			if !strings.Contains(out, want) {
				t.Errorf("%s 的提示应包含 %q:\n%s", tc.res.Linger, want, out)
			}
		}
	}
	env := testEnv(t)
	reportLinger(env, installResult{})
	if out := env.Stdout.(*syncBuffer).String(); out != "" {
		t.Errorf("不适用时不输出任何内容: %q", out)
	}
}
