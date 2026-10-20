package cli

import (
	"errors"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// 远程卸载的 Agent 端：判断「我是不是装成服务运行的」，以及如何把卸载进程放到自己的进程树之外启动。

type fakeSelfOps struct {
	env       map[string]string
	euid      int
	missing   map[string]bool // lookPath 找不到的命令
	exe       string
	units     map[bool]bool // 范围（user）→ 单元文件是否存在
	runErr    error
	runOut    string
	ran       [][]string
	startedAt time.Time
}

func newFakeSelfOps() *fakeSelfOps {
	return &fakeSelfOps{
		env:       map[string]string{"INVOCATION_ID": "abc123"},
		euid:      0,
		missing:   map[string]bool{},
		exe:       "/usr/local/bin/relaymesh-agent",
		units:     map[bool]bool{false: true},
		startedAt: time.Date(2026, 10, 2, 9, 30, 0, 0, time.UTC),
	}
}

func (f *fakeSelfOps) ops() selfOps {
	return selfOps{
		getenv:  func(k string) string { return f.env[k] },
		geteuid: func() int { return f.euid },
		lookPath: func(name string) (string, error) {
			if f.missing[name] {
				return "", errors.New("not found")
			}
			return "/usr/bin/" + name, nil
		},
		executable: func() (string, error) { return f.exe, nil },
		run: func(name string, args ...string) (string, error) {
			f.ran = append(f.ran, append([]string{name}, args...))
			return f.runOut, f.runErr
		},
		now:           func() time.Time { return f.startedAt },
		unitInstalled: func(user bool) bool { return f.units[user] },
	}
}

func TestLinuxSelfUninstallerNeedsEverySignOfBeingAService(t *testing.T) {
	cfg := resolved{dataDir: "/var/lib/relaymesh-agent"}
	cases := []struct {
		name string
		mod  func(*fakeSelfOps)
		want bool
	}{
		{"systemd 管理的系统级服务", func(*fakeSelfOps) {}, true},
		{"没有 INVOCATION_ID（前台运行 / 容器里）", func(f *fakeSelfOps) { delete(f.env, "INVOCATION_ID") }, false},
		{"有 INVOCATION_ID 但服务单元文件不存在（从某个服务里打开的终端里手动运行）", func(f *fakeSelfOps) { f.units[false] = false }, false},
		{"没有 systemd-run", func(f *fakeSelfOps) { f.missing["systemd-run"] = true }, false},
		{"普通用户：看的是用户级单元，系统级单元存在不算", func(f *fakeSelfOps) { f.euid = 1000 }, false},
		{"普通用户且用户级单元存在", func(f *fakeSelfOps) { f.euid = 1000; f.units = map[bool]bool{true: true} }, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeSelfOps()
			tc.mod(f)
			got := linuxSelfUninstaller(f.ops(), testEnv(t), cfg) != nil
			if got != tc.want {
				t.Fatalf("能力 = %v，期望 %v", got, tc.want)
			}
		})
	}
}

func TestLinuxSelfUninstallerRunsUninstallOutsideTheServiceCgroup(t *testing.T) {
	f := newFakeSelfOps()
	cfg := resolved{dataDir: "/var/lib/relaymesh-agent", cfgDir: "/etc/relaymesh-agent"}
	fn := linuxSelfUninstaller(f.ops(), testEnv(t), cfg)
	if fn == nil {
		t.Fatal("应具备能力")
	}
	if err := fn(true); err != nil {
		t.Fatal(err)
	}
	if len(f.ran) != 1 || f.ran[0][0] != "systemd-run" {
		t.Fatalf("应只调用一次 systemd-run: %v", f.ran)
	}
	got := strings.Join(f.ran[0][1:], " ")
	for _, want := range []string{
		"--collect",
		"--unit=relaymesh-agent-uninstall-" + strconv.FormatInt(f.startedAt.Unix(), 10),
		"-- /usr/local/bin/relaymesh-agent uninstall --yes --purge --data-dir /var/lib/relaymesh-agent --config-dir /etc/relaymesh-agent",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("systemd-run 参数缺少 %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "--user") {
		t.Errorf("系统级服务不应带 --user: %s", got)
	}

	// 不 purge：不带 --purge
	f.ran = nil
	if err := fn(false); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.Join(f.ran[0], " "), "--purge") {
		t.Errorf("purge=false 时不应带 --purge: %v", f.ran[0])
	}
}

func TestLinuxSelfUninstallerUserScope(t *testing.T) {
	f := newFakeSelfOps()
	f.euid = 1000
	f.units = map[bool]bool{true: true}
	f.exe = "/home/alice/.local/bin/relaymesh-agent"
	fn := linuxSelfUninstaller(f.ops(), testEnv(t), resolved{dataDir: "/home/alice/.local/share/relaymesh-agent"})
	if fn == nil {
		t.Fatal("应具备能力")
	}
	if err := fn(false); err != nil {
		t.Fatal(err)
	}
	args := f.ran[0]
	// systemd-run 的 --user 在最前（连接用户管理器），uninstall 的 --user 在命令里（卸载用户级服务）
	if args[1] != "--user" || !strings.Contains(strings.Join(args, " "), "uninstall --yes --user") {
		t.Errorf("用户级应同时带 systemd-run --user 与 uninstall --user: %v", args)
	}
}

func TestLinuxSelfUninstallerTrimsDeletedSuffixAndKeepsServicePrefix(t *testing.T) {
	// 升级替换过二进制后 /proc/self/exe 会带 " (deleted)"；主程序子命令形态的服务要带上前缀参数
	f := newFakeSelfOps()
	f.exe = "/usr/local/bin/relaymesh (deleted)"
	env := testEnv(t)
	env.ServiceArgs = []string{"agent"}
	fn := linuxSelfUninstaller(f.ops(), env, resolved{})
	if err := fn(false); err != nil {
		t.Fatal(err)
	}
	got := strings.Join(f.ran[0], " ")
	if !strings.Contains(got, "-- /usr/local/bin/relaymesh agent uninstall --yes") || strings.Contains(got, "(deleted)") {
		t.Errorf("命令不对: %s", got)
	}
}

func TestLinuxSelfUninstallerReportsStartFailure(t *testing.T) {
	f := newFakeSelfOps()
	f.runErr = errors.New("exit status 1")
	f.runOut = "Failed to connect to bus"
	fn := linuxSelfUninstaller(f.ops(), testEnv(t), resolved{})
	err := fn(true)
	if err == nil || !strings.Contains(err.Error(), "Failed to connect to bus") {
		t.Fatalf("启动失败应带上 systemd-run 的输出: %v", err)
	}
}

func TestUninstallArgsAreExplicitAboutDirectories(t *testing.T) {
	got := strings.Join(uninstallArgs(false, false, resolved{}), " ")
	if got != "uninstall --yes" {
		t.Fatalf("没有目录时不应带目录参数: %q", got)
	}
	got = strings.Join(uninstallArgs(true, true, resolved{dataDir: "/d", cfgDir: "/c"}), " ")
	if got != "uninstall --yes --user --purge --data-dir /d --config-dir /c" {
		t.Fatalf("got %q", got)
	}
}

// ---------- Windows ----------

// installedAs 在假的 Windows 上装好 Agent，并把「当前进程」设为已安装的无窗口版（即由计划任务启动的那个 Agent）。
func installedAs(t *testing.T, elevated bool, c common) (*fakeWin, winInstallResult) {
	t.Helper()
	f := newFakeWin(t, elevated)
	res, err := doInstallWindows(testEnv(t), winCfg(t, c), f.w)
	if err != nil {
		t.Fatal(err)
	}
	f.w.exe = res.TaskBinPath // 当前进程就是计划任务启动的无窗口版
	f.cmds, f.xmls = nil, nil
	return f, res
}

func TestWindowsSelfUninstallerOnlyForInstalledAgent(t *testing.T) {
	f, _ := installedAs(t, false, common{user: true})
	if windowsSelfUninstaller(f.w, testEnv(t), resolved{}) == nil {
		t.Fatal("从安装目录里的无窗口版运行，应具备卸载能力")
	}
	// 从下载目录直接运行（没安装 / 不是计划任务启动的那个文件）：不声明
	g := newFakeWin(t, false)
	if windowsSelfUninstaller(g.w, testEnv(t), resolved{}) != nil {
		t.Fatal("没有安装时不应声明卸载能力")
	}
	// 控制台版（用户手动在终端里运行 relaymesh-agent.exe run）也不是计划任务启动的
	f2, res2 := installedAs(t, false, common{user: true})
	f2.w.exe = res2.BinPath
	if windowsSelfUninstaller(f2.w, testEnv(t), resolved{}) != nil {
		t.Fatal("手动运行的控制台版不应声明卸载能力")
	}
}

func TestWindowsSelfUninstallerCreatesOneShotTaskAndRunsIt(t *testing.T) {
	f, res := installedAs(t, false, common{user: true})
	cfg := resolved{dataDir: res.DataDir, cfgDir: filepath.Join(f.w.localApp, "RelayMesh", "Config")}
	fn := windowsSelfUninstaller(f.w, testEnv(t), cfg)
	if fn == nil {
		t.Fatal("应具备能力")
	}
	if err := fn(true); err != nil {
		t.Fatal(err)
	}
	var verbs []string
	for _, c := range f.cmds {
		if strings.HasPrefix(c, "schtasks ") {
			fs := strings.Fields(c)
			verbs = append(verbs, fs[1]+":"+fs[3])
		}
	}
	oneShot := "RelayMeshAgent-HOST_alice-Uninstall"
	if strings.Join(verbs, " ") != "/Query:RelayMeshAgent-HOST_alice /Create:"+oneShot+" /Run:"+oneShot {
		// 第一条 /Query 来自「是否是最高权限」的判断
		t.Fatalf("schtasks 调用顺序不对: %v", f.cmds)
	}
	doc := parseTaskXML(t, f.xmls[0])
	if len(doc.Logon)+len(doc.Boot)+len(doc.Time) != 0 {
		t.Errorf("一次性任务不应有任何触发器（否则会被重复触发）: %+v", doc)
	}
	if doc.Principal.User != `HOST\alice` || doc.Principal.RunLevel != "LeastPrivilege" {
		t.Errorf("用户级 Agent 的卸载任务应以同一用户、最低权限运行: %+v", doc.Principal)
	}
	if doc.Exec.Command != res.TaskBinPath {
		t.Errorf("应运行已安装的无窗口版（不弹控制台窗口）: %q", doc.Exec.Command)
	}
	joined := strings.Join(parseWindowsArgs(doc.Exec.Args), " ")
	for _, want := range []string{"uninstall --yes --user --purge", "--data-dir " + res.DataDir, "--config-dir " + cfg.cfgDir} {
		if !strings.Contains(joined, want) {
			t.Errorf("卸载任务参数缺少 %q: %q", want, joined)
		}
	}
}

func TestWindowsSelfUninstallerSystemAndHighestScopes(t *testing.T) {
	// 系统级：SYSTEM 运行
	f, _ := installedAs(t, true, common{})
	if err := windowsSelfUninstaller(f.w, testEnv(t), resolved{})(false); err != nil {
		t.Fatal(err)
	}
	doc := parseTaskXML(t, f.xmls[0])
	if doc.Principal.User != "S-1-5-18" || doc.Principal.RunLevel != "HighestAvailable" {
		t.Errorf("系统级的卸载任务应以 SYSTEM 运行: %+v", doc.Principal)
	}
	if args := strings.Join(parseWindowsArgs(doc.Exec.Args), " "); strings.Contains(args, "--user") || strings.Contains(args, "--purge") {
		t.Errorf("系统级 + 不 purge 不应带 --user / --purge: %q", args)
	}

	// 用户级 + 最高权限：卸载任务同样要以最高权限，否则普通令牌删不掉最高权限的任务
	h, _ := installedAs(t, true, common{user: true, highest: true})
	if err := windowsSelfUninstaller(h.w, testEnv(t), resolved{})(false); err != nil {
		t.Fatal(err)
	}
	if doc := parseTaskXML(t, h.xmls[0]); doc.Principal.RunLevel != "HighestAvailable" || doc.Principal.User != `HOST\alice` {
		t.Errorf("最高权限的用户级 Agent，卸载任务也应是该用户 + 最高权限: %+v", doc.Principal)
	}
}

func TestWindowsSelfUninstallerCleansUpWhenRunFails(t *testing.T) {
	f, _ := installedAs(t, false, common{user: true})
	f.failOn["schtasks /Run"] = errors.New("exit status 1")
	f.outOn["schtasks /Run"] = "错误: 拒绝访问。"
	err := windowsSelfUninstaller(f.w, testEnv(t), resolved{})(false)
	if err == nil || !strings.Contains(err.Error(), "拒绝访问") {
		t.Fatalf("应带上 schtasks 的输出: %v", err)
	}
	last := f.cmds[len(f.cmds)-1]
	if !strings.HasPrefix(last, "schtasks /Delete /TN RelayMeshAgent-HOST_alice-Uninstall") {
		t.Fatalf("启动失败时应删掉刚创建的一次性任务，别留下垃圾: %v", f.cmds)
	}
}

func TestWindowsSelfUninstallerCreateFailureLeavesAgentAlone(t *testing.T) {
	f, _ := installedAs(t, false, common{user: true})
	f.failOn["schtasks /Create"] = errors.New("exit status 1")
	f.outOn["schtasks /Create"] = "错误: 拒绝访问。"
	if err := windowsSelfUninstaller(f.w, testEnv(t), resolved{})(false); err == nil {
		t.Fatal("创建失败应报错")
	}
	for _, c := range f.cmds {
		if strings.HasPrefix(c, "schtasks /End") || strings.HasPrefix(c, "schtasks /Delete /TN RelayMeshAgent-HOST_alice ") {
			t.Fatalf("失败时绝不能动 Agent 自己的任务: %v", f.cmds)
		}
	}
}

func TestUninstallWindowsRemovesOneShotUninstallTask(t *testing.T) {
	// 卸载进程就运行在一次性任务里：卸载流程要把这个任务的定义也删掉（运行中的实例不受影响）
	f, res := installedAs(t, false, common{user: true})
	if err := windowsSelfUninstaller(f.w, testEnv(t), resolved{dataDir: res.DataDir})(false); err != nil {
		t.Fatal(err)
	}
	f.cmds = nil
	ur, err := doUninstallWindows(testEnv(t), winCfg(t, common{user: true}), false, resolved{}, f.w)
	if err != nil || !ur.Found {
		t.Fatalf("found=%v err=%v", ur.Found, err)
	}
	deleted := false
	for _, c := range f.cmds {
		if strings.HasPrefix(c, "schtasks /Delete /TN RelayMeshAgent-HOST_alice-Uninstall") {
			deleted = true
		}
	}
	if !deleted {
		t.Fatalf("应删除一次性卸载任务: %v", f.cmds)
	}
	// 已经没有它时（普通的手动卸载）：只做查询，不报错也不乱删
	g := newFakeWin(t, false)
	env := testEnv(t)
	if _, err := doInstallWindows(env, winCfg(t, common{user: true}), g.w); err != nil {
		t.Fatal(err)
	}
	g.cmds = nil
	if _, err := doUninstallWindows(env, winCfg(t, common{user: true}), false, resolved{}, g.w); err != nil {
		t.Fatal(err)
	}
	for _, c := range g.cmds {
		if strings.Contains(c, "-Uninstall") && strings.HasPrefix(c, "schtasks /Delete") {
			t.Fatalf("没有一次性任务时不应尝试删除: %v", g.cmds)
		}
	}
}

func TestSameWinPath(t *testing.T) {
	a := filepath.Join("C:", "Program Files", "RelayMesh Agent", "relaymesh-agentw.exe")
	if !sameWinPath(a, strings.ToUpper(a)) || !sameWinPath(a, filepath.Join(filepath.Dir(a), ".", filepath.Base(a))) {
		t.Error("Windows 路径应不区分大小写，并先做规范化")
	}
	if sameWinPath("", "") || sameWinPath(a, "") || sameWinPath(a, a+".old") {
		t.Error("空路径永远不相等，不同文件更不相等")
	}
}
