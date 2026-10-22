package cli

import (
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 用假的 Windows 验证升级：先真的 doInstallWindows 装一遍，再升级——
// 「任务定义没被重写、守护进程没被动」是对着安装产生的真实文件与命令记录验证的。

// newerExe 让「当前正在运行的程序」变成带标记的新版本。
func newerExe(t *testing.T, f *fakeWin) {
	t.Helper()
	if err := os.WriteFile(f.w.exe, append(makePE(0x20b, peSubsystemCUI), []byte("v2")...), 0o755); err != nil {
		t.Fatal(err)
	}
}

func readFile(t *testing.T, p string) []byte {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func schtasksVerbs(cmds []string) []string {
	var verbs []string
	for _, c := range cmds {
		if strings.HasPrefix(c, "schtasks ") {
			verbs = append(verbs, strings.Fields(c)[1])
		}
	}
	return verbs
}

func TestUpgradeWindowsUserScopeReplacesImagesWithoutRewritingTasks(t *testing.T) {
	f := newFakeWin(t, false)
	env := testEnv(t)
	if _, err := doInstallWindows(env, winCfg(t, common{user: true}), f.w); err != nil {
		t.Fatal(err)
	}
	created := len(f.xmls)
	f.cmds = nil
	newerExe(t, f)

	res, err := doUpgradeWindows(env, f.w, false)
	if err != nil {
		t.Fatal(err)
	}
	if !res.User || res.TaskName != "RelayMeshAgent-HOST_alice" || !res.Started {
		t.Fatalf("结果不对: %+v", res)
	}
	con, gui := readFile(t, res.BinPath), readFile(t, res.TaskBinPath)
	if !strings.HasSuffix(string(con), "v2") || !strings.HasSuffix(string(gui), "v2") {
		t.Fatal("控制台版与无窗口版都应换成新版本")
	}
	if binary.LittleEndian.Uint16(gui[0x80+4+20+68:]) != peSubsystemGUI {
		t.Fatal("计划任务启动的应是无窗口版")
	}
	// 任务定义不能被重写（服务端地址、数据目录等保持原样）；顺序：先停、再启动。
	if len(f.xmls) != created {
		t.Fatal("升级不应重新创建计划任务")
	}
	verbs := strings.Join(schtasksVerbs(f.cmds), ",")
	if strings.Contains(verbs, "/Create") || strings.Contains(verbs, "/Delete") {
		t.Fatalf("升级不应创建或删除任务: %s", verbs)
	}
	if !strings.HasSuffix(verbs, "/End,/Run") {
		t.Fatalf("应先停再启动: %s", verbs)
	}
}

func TestUpgradeWindowsSystemScopeWhenElevated(t *testing.T) {
	f := newFakeWin(t, true)
	env := testEnv(t)
	if _, err := doInstallWindows(env, winCfg(t, common{}), f.w); err != nil {
		t.Fatal(err)
	}
	newerExe(t, f)
	res, err := doUpgradeWindows(env, f.w, false)
	if err != nil {
		t.Fatal(err)
	}
	if res.User || res.TaskName != "RelayMeshAgent" || !strings.HasPrefix(res.BinPath, filepath.Join(f.w.progFiles, "RelayMesh Agent")) {
		t.Fatalf("管理员应升级系统级安装: %+v", res)
	}
}

func TestUpgradeWindowsNonElevatedSeesSystemInstallAndAsksForAdmin(t *testing.T) {
	f := newFakeWin(t, true)
	env := testEnv(t)
	res0, err := doInstallWindows(env, winCfg(t, common{}), f.w)
	if err != nil {
		t.Fatal(err)
	}
	f.w.elevated = false
	f.cmds = nil
	newerExe(t, f)

	_, err = doUpgradeWindows(env, f.w, false)
	if !errors.Is(err, errWinUpgradeNeedsAdmin) {
		t.Fatalf("应提示需要管理员: %v", err)
	}
	if strings.Contains(strings.Join(schtasksVerbs(f.cmds), ","), "/End") {
		t.Fatal("被拒绝时不应停任何任务")
	}
	if strings.HasSuffix(string(readFile(t, res0.BinPath)), "v2") {
		t.Fatal("被拒绝时不应替换程序")
	}
}

func TestUpgradeWindowsWhenNothingIsInstalled(t *testing.T) {
	f := newFakeWin(t, true)
	if _, err := doUpgradeWindows(testEnv(t), f.w, false); !errors.Is(err, errWinNothingToUpgrade) {
		t.Fatalf("没装任务时应指向 install: %v", err)
	}
	if _, err := doUpgradeWindows(testEnv(t), f.w, true); !errors.Is(err, errWinNothingToUpgrade) {
		t.Fatalf("--user 同样: %v", err)
	}
	for _, c := range f.cmds {
		if strings.Contains(c, "/End") || strings.Contains(c, "/Run") {
			t.Fatalf("没装任务时不应停 / 启: %s", c)
		}
	}
}

func TestUpgradeWindowsKeepsARunningHolder(t *testing.T) {
	f := newFakeWin(t, false)
	env := testEnv(t)
	inst, err := doInstallWindows(env, winHolderCfg(t, common{user: true}), f.w)
	if err != nil {
		t.Fatal(err)
	}
	holderBefore := readFile(t, inst.HolderTaskBinPath)
	f.cmds = nil
	newerExe(t, f)

	res, err := doUpgradeWindows(env, f.w, false)
	if err != nil {
		t.Fatal(err)
	}
	if res.HolderState != "kept" || len(res.Notes) != 1 || !strings.Contains(res.Notes[0], "holder restart") {
		t.Fatalf("运行中的守护进程应保持不动并给出提示: %+v", res)
	}
	// 回归：PowerShell 里照抄「`…holder restart`」会把结尾的反引号一起带走，得到「未知的 holder 子命令 "restart`"」
	assertCommandLineEndsWith(t, res.Notes[0], "holder restart")
	if string(readFile(t, inst.HolderTaskBinPath)) != string(holderBefore) {
		t.Fatal("守护进程的映像不能被替换（它正持有终端会话）")
	}
	for _, c := range f.cmds {
		if strings.Contains(c, winHolderTaskName) {
			t.Fatalf("不应碰守护进程的任务: %s", c)
		}
	}
	if !strings.HasSuffix(string(readFile(t, res.TaskBinPath)), "v2") {
		t.Fatal("Agent 自己的映像应换成新版本")
	}
}

func TestUpgradeWindowsRefreshesAStoppedHolder(t *testing.T) {
	f := newFakeWin(t, false)
	env := testEnv(t)
	inst, err := doInstallWindows(env, winHolderCfg(t, common{user: true}), f.w)
	if err != nil {
		t.Fatal(err)
	}
	f.running[inst.HolderTaskBinPath] = false // 守护进程此刻没在运行
	newerExe(t, f)

	res, err := doUpgradeWindows(env, f.w, false)
	if err != nil {
		t.Fatal(err)
	}
	if res.HolderState != "refreshed" || !strings.HasSuffix(string(readFile(t, inst.HolderTaskBinPath)), "v2") {
		t.Fatalf("没在运行的守护进程应换上新版本的程序: %+v", res)
	}
}

func TestUpgradeWindowsFailsIfTheOldInstanceWontExit(t *testing.T) {
	f := newFakeWin(t, false)
	env := testEnv(t)
	inst, err := doInstallWindows(env, winCfg(t, common{user: true}), f.w)
	if err != nil {
		t.Fatal(err)
	}
	f.w.procs = func(image string) []int {
		if image == inst.TaskBinPath {
			return []int{4242}
		}
		return nil
	}
	f.failOn["schtasks /End"] = errors.New("exit status 1")
	f.outOn["schtasks /End"] = "错误: 拒绝访问。"
	newerExe(t, f)

	_, err = doUpgradeWindows(env, f.w, false)
	if err == nil || !strings.Contains(err.Error(), "没有退出") || !strings.Contains(err.Error(), "拒绝访问") {
		t.Fatalf("应说明旧进程没退出并带上 schtasks 的输出: %v", err)
	}
	if strings.HasSuffix(string(readFile(t, inst.BinPath)), "v2") {
		t.Fatal("旧进程没退出时不应替换程序")
	}
}

func TestUpgradeWindowsRefusesToUpgradeFromTheInstalledImage(t *testing.T) {
	f := newFakeWin(t, false)
	env := testEnv(t)
	inst, err := doInstallWindows(env, winCfg(t, common{user: true}), f.w)
	if err != nil {
		t.Fatal(err)
	}
	f.cmds = nil
	f.w.exe = strings.ToUpper(inst.BinPath) // Windows 路径不分大小写
	_, err = doUpgradeWindows(env, f.w, false)
	if err == nil || !strings.Contains(err.Error(), "已安装的程序本身") {
		t.Fatalf("应提示先获取新版本: %v", err)
	}
	if strings.Contains(strings.Join(schtasksVerbs(f.cmds), ","), "/End") {
		t.Fatal("拒绝时不应停任务")
	}
}

func TestUpgradeWindowsStartFailureStillReportsTheReplacedBinary(t *testing.T) {
	f := newFakeWin(t, false)
	env := testEnv(t)
	if _, err := doInstallWindows(env, winCfg(t, common{user: true}), f.w); err != nil {
		t.Fatal(err)
	}
	f.failOn["schtasks /Run"] = errors.New("exit status 1")
	f.outOn["schtasks /Run"] = "错误: 系统找不到指定的文件。"
	newerExe(t, f)
	_, err := doUpgradeWindows(env, f.w, false)
	if err == nil || !strings.Contains(err.Error(), "已经换成新版本") || !strings.Contains(err.Error(), "系统找不到") {
		t.Fatalf("启动失败应说明程序已换并带上输出: %v", err)
	}
}
