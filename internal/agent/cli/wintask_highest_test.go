package cli

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// 第三档安装范围「当前用户 + 最高权限」（--user --highest）：
// 身份与环境仍是该用户（任务名 / 数据目录 / 可执行文件位置 / 登录触发器都与用户级一致），
// 只把任务的运行级别从 LeastPrivilege 提到 HighestAvailable——登录时由任务计划程序直接给出完整的管理员令牌，
// 既不弹 UAC，也不需要保存密码。创建这种任务本身需要管理员令牌。

func TestRenderTaskXMLUserHighest(t *testing.T) {
	doc := parseTaskXML(t, renderTaskXML(taskSpec{
		User:      `WINHOST\alice`,
		Highest:   true,
		Command:   "c",
		Arguments: "a",
		Start:     time.Now(),
	}))
	p := doc.Principal
	if p.User != `WINHOST\alice` || p.LogonType != "InteractiveToken" || p.RunLevel != "HighestAvailable" {
		t.Errorf("应为该用户 + 交互令牌 + 最高权限（而不是 SYSTEM）: %+v", p)
	}
	if len(doc.Logon) != 1 || doc.Logon[0].User != `WINHOST\alice` || len(doc.Boot) != 0 {
		t.Errorf("最高权限的用户级任务仍应只在该用户登录时触发: logon=%v boot=%v", doc.Logon, doc.Boot)
	}
	if len(doc.Time) != 1 || doc.Time[0].Interval != "PT5M" {
		t.Errorf("看门狗触发器不应丢: %+v", doc.Time)
	}
}

func TestRenderTaskXMLHighestDoesNotChangeSystemScope(t *testing.T) {
	// Highest 只描述「用户级任务用什么令牌」；系统级任务本来就是 SYSTEM + 最高权限，不应受它影响（更不能带出用户名）
	doc := parseTaskXML(t, renderTaskXML(taskSpec{System: true, Highest: true, User: `X\ignored`, Command: "c", Start: time.Now()}))
	if doc.Principal.User != "S-1-5-18" || doc.Principal.RunLevel != "HighestAvailable" || len(doc.Boot) != 1 || len(doc.Logon) != 0 {
		t.Errorf("系统级任务不受 Highest 影响: %+v", doc)
	}
}

func TestRenderTaskXMLUserStaysLeastPrivilegeWithoutHighest(t *testing.T) {
	doc := parseTaskXML(t, renderTaskXML(taskSpec{User: `X\u`, Command: "c", Start: time.Now()}))
	if doc.Principal.RunLevel != "LeastPrivilege" {
		t.Errorf("不指定 Highest 时必须保持最低权限（默认行为不能悄悄变）: %+v", doc.Principal)
	}
}

func TestInstallWindowsHighestNeedsElevation(t *testing.T) {
	f := newFakeWin(t, false)
	_, err := doInstallWindows(testEnv(t), winHolderCfg(t, common{user: true, highest: true}), f.w)
	if !errors.Is(err, errWindowsNeedsAdminHighest) || !strings.Contains(err.Error(), "管理员") {
		t.Fatalf("没有管理员令牌时应明确提示，而不是创建到一半才失败: %v", err)
	}
	if len(f.cmds) != 0 {
		t.Fatalf("失败时不应有任何副作用: %v", f.cmds)
	}
	if _, statErr := os.Stat(filepath.Join(f.w.localApp, "RelayMesh")); statErr == nil {
		t.Fatal("失败时不应创建身份目录")
	}
}

func TestInstallWindowsHighestWithoutUserIsRejected(t *testing.T) {
	f := newFakeWin(t, true)
	_, err := doInstallWindows(testEnv(t), winHolderCfg(t, common{highest: true}), f.w)
	if err == nil || !strings.Contains(err.Error(), "--user") {
		t.Fatalf("--highest 单独使用应被拒绝并指向 --user: %v", err)
	}
	if len(f.cmds) != 0 || len(f.xmls) != 0 {
		t.Fatalf("被拒绝时不应有任何副作用: cmds=%v", f.cmds)
	}
}

func TestInstallWindowsHighestCreatesBothTasksAtHighestRunLevel(t *testing.T) {
	f := newFakeWin(t, true)
	res, err := doInstallWindows(testEnv(t), winHolderCfg(t, common{user: true, highest: true}), f.w)
	if err != nil {
		t.Fatal(err)
	}
	if !res.User || !res.Highest || res.TaskName != "RelayMeshAgent-HOST_alice" || res.HolderTaskName != "RelayMeshHolder-HOST_alice" {
		t.Fatalf("应是用户级任务名 + 最高权限标记: %+v", res)
	}
	// 位置与普通用户级完全一致：不能因为「最高权限」就把可执行文件放进别的目录
	if !strings.HasPrefix(res.BinPath, f.w.localApp) || !strings.HasPrefix(res.DataDir, f.w.localApp) {
		t.Fatalf("可执行文件与数据目录仍应在该用户的目录下: bin=%s data=%s", res.BinPath, res.DataDir)
	}
	if len(f.xmls) != 2 {
		t.Fatalf("守护进程与 Agent 各一个任务: %d", len(f.xmls))
	}
	for i, x := range f.xmls {
		doc := parseTaskXML(t, x)
		p := doc.Principal
		if p.User != `HOST\alice` || p.LogonType != "InteractiveToken" || p.RunLevel != "HighestAvailable" {
			t.Errorf("第 %d 个任务应为该用户 + 交互令牌 + 最高权限: %+v", i, p)
		}
		if len(doc.Logon) != 1 || len(doc.Boot) != 0 {
			t.Errorf("第 %d 个任务应只有登录触发器: logon=%d boot=%d", i, len(doc.Logon), len(doc.Boot))
		}
		if !strings.Contains(doc.Desc, "最高权限") {
			t.Errorf("第 %d 个任务的描述应写明最高权限（在任务计划程序里能一眼看出）: %q", i, doc.Desc)
		}
	}
	_, args := argsOf(t, f.xmls[1])
	if !strings.Contains(strings.Join(args, "\x00"), "--data-dir\x00"+res.DataDir) {
		t.Errorf("任务参数必须显式写入数据目录，保证任务用的身份就是安装时打印的那个: %q", args)
	}
}

func TestInstallWindowsPlainUserStaysLeastPrivilegeEvenWhenElevated(t *testing.T) {
	// 管理员为自己装用户级任务、没写 --highest：行为与之前一致（最低权限），不能因为 shell 已提升就默认提权
	f := newFakeWin(t, true)
	res, err := doInstallWindows(testEnv(t), winHolderCfg(t, common{user: true}), f.w)
	if err != nil {
		t.Fatal(err)
	}
	if res.Highest {
		t.Fatalf("未指定 --highest 不应标记为最高权限: %+v", res)
	}
	for i, x := range f.xmls {
		if doc := parseTaskXML(t, x); doc.Principal.RunLevel != "LeastPrivilege" || strings.Contains(doc.Desc, "最高权限") {
			t.Errorf("第 %d 个任务应保持最低权限: %+v desc=%q", i, doc.Principal, doc.Desc)
		}
	}
}

func TestCheckHighest(t *testing.T) {
	cases := []struct {
		name    string
		c       common
		goos    string
		wantErr string // 空：应当通过
	}{
		{"未指定", common{}, "linux", ""},
		{"未指定但带 --user", common{user: true}, "darwin", ""},
		{"Windows 用户级", common{user: true, highest: true}, "windows", ""},
		{"Windows 系统级", common{highest: true}, "windows", "--user"},
		{"Linux", common{user: true, highest: true}, "linux", "Windows"},
		{"macOS", common{user: true, highest: true}, "darwin", "Windows"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := tc.c
			err := c.checkHighest(tc.goos)
			switch {
			case tc.wantErr == "" && err != nil:
				t.Fatalf("不应报错: %v", err)
			case tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)):
				t.Fatalf("应报错并包含 %q: %v", tc.wantErr, err)
			}
		})
	}
}

func TestInstallRejectsHighestOutsideWindows(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("在 Windows 上这是合法组合，并且会真实安装")
	}
	code, env := runCLI(t, []string{"install", "--user", "--highest", "--server", "https://x.example.com", "--data-dir", t.TempDir()}, nil)
	if code != 2 || !strings.Contains(errOut(env), "--highest") || !strings.Contains(errOut(env), "Windows") {
		t.Fatalf("非 Windows 上应在任何副作用之前拒绝: code=%d err=%q", code, errOut(env))
	}
}

func TestUsageMentionsHighest(t *testing.T) {
	code, env := runCLI(t, []string{"help"}, nil)
	if code != 0 || !strings.Contains(out(env), "--highest") {
		t.Fatalf("用法里应介绍 --highest: code=%d", code)
	}
}

func TestReportWindowsLabelsHighestUserTask(t *testing.T) {
	f := newFakeWin(t, true)
	if _, err := doInstallWindows(testEnv(t), winCfg(t, common{user: true, highest: true}), f.w); err != nil {
		t.Fatal(err)
	}
	env := testEnv(t)
	reportWindows(env, f.w)
	if out := env.Stdout.(*syncBuffer).String(); !strings.Contains(out, "用户级任务（最高权限）") {
		t.Fatalf("状态里应标出最高权限: %q", out)
	}

	plain := newFakeWin(t, false)
	if _, err := doInstallWindows(testEnv(t), winCfg(t, common{user: true}), plain.w); err != nil {
		t.Fatal(err)
	}
	env2 := testEnv(t)
	reportWindows(env2, plain.w)
	if out := env2.Stdout.(*syncBuffer).String(); strings.Contains(out, "最高权限") || !strings.Contains(out, "用户级任务") {
		t.Fatalf("普通用户级任务不应带最高权限标注: %q", out)
	}
}

func TestReportWindowsIgnoresUnreadableTaskDefinition(t *testing.T) {
	// 读不到任务定义（权限等原因）只是少一个标注，不能影响状态输出本身
	f := newFakeWin(t, true)
	if _, err := doInstallWindows(testEnv(t), winCfg(t, common{user: true, highest: true}), f.w); err != nil {
		t.Fatal(err)
	}
	f.failOn["schtasks /Query /TN RelayMeshAgent-HOST_alice /XML"] = errors.New("exit status 1")
	env := testEnv(t)
	reportWindows(env, f.w)
	out := env.Stdout.(*syncBuffer).String()
	if !strings.Contains(out, "用户级任务") || strings.Contains(out, "最高权限") || !strings.Contains(out, "PID 4242") {
		t.Fatalf("读不到定义时仍应正常报告、只是不带标注: %q", out)
	}
}

func TestUninstallWindowsHighestTaskNeedsAdminHint(t *testing.T) {
	// 以最高权限安装（管理员 PowerShell），随后在普通 PowerShell 里卸载：schtasks /Delete 会被拒绝，
	// 错误里必须告诉用户「以管理员身份重试」，而不是只丢一句「拒绝访问」。
	f := newFakeWin(t, true)
	env := testEnv(t)
	if _, err := doInstallWindows(env, winCfg(t, common{user: true, highest: true}), f.w); err != nil {
		t.Fatal(err)
	}
	f.w.elevated = false
	f.failOn["schtasks /Delete"] = errors.New("exit status 1")
	f.outOn["schtasks /Delete"] = "错误: 拒绝访问。"
	_, err := doUninstallWindows(env, common{user: true}, false, resolved{}, f.w)
	if err == nil || !strings.Contains(err.Error(), "拒绝访问") || !strings.Contains(err.Error(), "管理员身份运行") {
		t.Fatalf("应保留原始输出并给出管理员提示: %v", err)
	}

	// 管理员 shell 里删除失败是另一回事：不应再误导用户去「以管理员重试」
	g := newFakeWin(t, true)
	if _, err := doInstallWindows(env, winCfg(t, common{user: true, highest: true}), g.w); err != nil {
		t.Fatal(err)
	}
	g.failOn["schtasks /Delete"] = errors.New("exit status 1")
	g.outOn["schtasks /Delete"] = "错误: 某种其他失败。"
	_, err = doUninstallWindows(env, common{user: true}, false, resolved{}, g.w)
	if err == nil || strings.Contains(err.Error(), "管理员身份运行") {
		t.Fatalf("已是管理员时不应再给出管理员提示: %v", err)
	}
}

func TestInstallWindowsCreateDeniedHintsAboutExistingHighestTask(t *testing.T) {
	// 同名任务此前是以最高权限装的，普通 PowerShell 里覆盖安装会被拒绝：错误里提示用管理员重试
	f := newFakeWin(t, false)
	f.failOn["schtasks /Create"] = errors.New("exit status 1")
	f.outOn["schtasks /Create"] = "错误: 拒绝访问。"
	_, err := doInstallWindows(testEnv(t), winCfg(t, common{user: true}), f.w)
	if err == nil || !strings.Contains(err.Error(), "拒绝访问") || !strings.Contains(err.Error(), "最高权限") {
		t.Fatalf("应提示可能是此前以最高权限安装的任务: %v", err)
	}

	g := newFakeWin(t, true)
	g.failOn["schtasks /Create"] = errors.New("exit status 1")
	g.outOn["schtasks /Create"] = "错误: 拒绝访问。"
	_, err = doInstallWindows(testEnv(t), winCfg(t, common{user: true}), g.w)
	if err == nil || strings.Contains(err.Error(), "最高权限") {
		t.Fatalf("管理员 shell 里不应给出这条提示: %v", err)
	}
}
