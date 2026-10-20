package cli

import (
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/teacat99/RelayMesh/v2/internal/agent"
	"github.com/teacat99/RelayMesh/v2/internal/agent/holder"
)

// 双任务形态：Agent 与会话守护进程各是一个计划任务、各用自己的映像文件，
// 这样升级 Agent 时可以替换 Agent 的映像，而守护进程（连同其中的终端会话）继续运行。

func winHolderCfg(t *testing.T, c common) common {
	t.Helper()
	if c.holderMode == "" {
		c.holderMode = "auto"
	}
	if c.server == "" {
		c.server = "https://relay.example.com"
	}
	return c
}

func argsOf(t *testing.T, xml string) (command string, args []string) {
	t.Helper()
	doc := parseTaskXML(t, xml)
	return doc.Exec.Command, parseWindowsArgs(doc.Exec.Args)
}

func TestInstallWindowsWithHolderCreatesTwoTasks(t *testing.T) {
	f := newFakeWin(t, false)
	res, err := doInstallWindows(testEnv(t), winHolderCfg(t, common{user: true, maxSessions: 5}), f.w)
	if err != nil {
		t.Fatal(err)
	}
	binDir := filepath.Join(f.w.localApp, "Programs", "RelayMesh Agent")
	dataDir := filepath.Join(f.w.localApp, "RelayMesh", "Agent")
	if !res.WithHolder || res.HolderTaskName != "RelayMeshHolder-HOST_alice" ||
		res.HolderTaskBinPath != filepath.Join(binDir, "relaymesh-agent-holderw.exe") ||
		res.HolderLogPath != filepath.Join(dataDir, "agent-holder.log") {
		t.Fatalf("守护进程的任务名 / 映像 / 日志不对: %+v", res)
	}
	// 守护进程有自己的无窗口映像：和 Agent 的映像是两个文件，这才能在不碰它的前提下替换 Agent。
	hb, err := os.ReadFile(res.HolderTaskBinPath)
	if err != nil {
		t.Fatal(err)
	}
	if binary.LittleEndian.Uint16(hb[0x80+4+20+68:]) != peSubsystemGUI {
		t.Fatal("守护进程映像应是无窗口版")
	}
	if !res.Started || !res.HolderStarted {
		t.Fatalf("两个进程都应启动: %+v", res)
	}

	// 顺序：先停旧 Agent；守护进程先创建并启动，Agent 随后（Agent 起来时守护进程已经在监听）。
	var verbs []string
	for _, c := range f.cmds {
		if strings.HasPrefix(c, "schtasks ") {
			fs := strings.Fields(c)
			verbs = append(verbs, fs[1]+":"+fs[3])
		}
	}
	want := []string{
		"/End:RelayMeshAgent-HOST_alice",
		"/Create:RelayMeshHolder-HOST_alice",
		"/Run:RelayMeshHolder-HOST_alice",
		"/Create:RelayMeshAgent-HOST_alice",
		"/Run:RelayMeshAgent-HOST_alice",
	}
	if strings.Join(verbs, " ") != strings.Join(want, " ") {
		t.Fatalf("schtasks 调用顺序不对:\n%s", strings.Join(f.cmds, "\n"))
	}

	holderCmd, holderArgs := argsOf(t, f.xmls[0])
	if holderCmd != res.HolderTaskBinPath {
		t.Errorf("守护进程任务应启动它自己的映像: %q", holderCmd)
	}
	hj := strings.Join(holderArgs, "\x00")
	for _, w := range []string{"holder\x00run", "--data-dir\x00" + dataDir, "--max-sessions\x005", "--log-file\x00" + res.HolderLogPath} {
		if !strings.Contains(hj, w) {
			t.Errorf("守护进程任务参数缺少 %q: %q", strings.ReplaceAll(w, "\x00", " "), holderArgs)
		}
	}
	for _, bad := range []string{"--server", "--pin", "https://"} {
		if strings.Contains(hj, bad) {
			t.Errorf("守护进程任务不应带服务端相关参数 %q: %q", bad, holderArgs)
		}
	}

	agentCmd, agentArgs := argsOf(t, f.xmls[1])
	if agentCmd != res.TaskBinPath {
		t.Errorf("Agent 任务应启动 Agent 映像: %q", agentCmd)
	}
	aj := strings.Join(agentArgs, "\x00")
	for _, w := range []string{"run", "--server\x00https://relay.example.com", "--holder\x00external"} {
		if !strings.Contains(aj, w) {
			t.Errorf("Agent 任务参数缺少 %q: %q", strings.ReplaceAll(w, "\x00", " "), agentArgs)
		}
	}
	if doc := parseTaskXML(t, f.xmls[0]); doc.Principal.LogonType != "InteractiveToken" || len(doc.Logon) != 1 {
		t.Errorf("用户级守护进程任务应为登录触发 + 交互令牌: %+v", doc)
	}
}

func TestInstallWindowsSystemScopeHolderRunsAsSystem(t *testing.T) {
	f := newFakeWin(t, true)
	res, err := doInstallWindows(testEnv(t), winHolderCfg(t, common{}), f.w)
	if err != nil {
		t.Fatal(err)
	}
	if res.HolderTaskName != "RelayMeshHolder" {
		t.Fatalf("系统级守护进程任务名: %+v", res)
	}
	if !strings.HasPrefix(res.HolderTaskBinPath, filepath.Join(f.w.progFiles, "RelayMesh Agent")) {
		t.Fatalf("守护进程映像必须放在 Program Files：它以 SYSTEM 运行，放在可写目录就是提权通道: %s", res.HolderTaskBinPath)
	}
	doc := parseTaskXML(t, f.xmls[0])
	if doc.Principal.User != "S-1-5-18" || len(doc.Boot) != 1 {
		t.Errorf("系统级守护进程应为 SYSTEM + 开机触发: %+v", doc)
	}
}

func TestInstallWindowsEmbeddedStaysSingleTask(t *testing.T) {
	f := newFakeWin(t, false)
	res, err := doInstallWindows(testEnv(t), winCfg(t, common{user: true}), f.w)
	if err != nil {
		t.Fatal(err)
	}
	if res.WithHolder || len(f.xmls) != 1 {
		t.Fatalf("内嵌模式只应有一个任务: %+v xmls=%d", res, len(f.xmls))
	}
	_, args := argsOf(t, f.xmls[0])
	if !strings.Contains(strings.Join(args, "\x00"), "--holder\x00embedded") {
		t.Errorf("内嵌模式的 Agent 任务应明确写出 --holder embedded: %q", args)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(res.BinPath), "relaymesh-agent-holderw.exe")); err == nil {
		t.Fatal("内嵌模式不应安装守护进程映像")
	}
}

func TestInstallWindowsReinstallKeepsRunningHolder(t *testing.T) {
	f := newFakeWin(t, false)
	c := winHolderCfg(t, common{user: true})
	env := testEnv(t)
	res, err := doInstallWindows(env, c, f.w)
	if err != nil {
		t.Fatal(err)
	}
	holderBefore, err := os.ReadFile(res.HolderTaskBinPath)
	if err != nil {
		t.Fatal(err)
	}

	// 新版本可执行文件；守护进程仍在运行（Windows 上运行中的映像文件不可替换，也不该被打断）。
	if err := os.WriteFile(f.w.exe, append(makePE(0x20b, peSubsystemCUI), []byte("v2")...), 0o755); err != nil {
		t.Fatal(err)
	}
	f.cmds, f.xmls = nil, nil
	res2, err := doInstallWindows(env, c, f.w)
	if err != nil {
		t.Fatal(err)
	}
	for _, cmd := range f.cmds {
		if strings.HasPrefix(cmd, "schtasks /End") && strings.Contains(cmd, "RelayMeshHolder") {
			t.Fatalf("重装不得结束守护进程任务（会结束会话）: %v", f.cmds)
		}
		if strings.HasPrefix(cmd, "schtasks /Run") && strings.Contains(cmd, "RelayMeshHolder") {
			t.Fatalf("守护进程已在运行，不必再启动: %v", f.cmds)
		}
	}
	holderAfter, err := os.ReadFile(res2.HolderTaskBinPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(holderAfter) != string(holderBefore) {
		t.Fatal("运行中的守护进程映像应保持原样")
	}
	agentNew, err := os.ReadFile(res2.TaskBinPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(string(agentNew), "v2") {
		t.Fatal("Agent 映像应换成新版本")
	}
	if len(res2.Notes) != 1 || !strings.Contains(res2.Notes[0], "holder restart") {
		t.Fatalf("应提示用 holder restart 换上新版本: %v", res2.Notes)
	}
	assertCommandLineEndsWith(t, res2.Notes[0], "holder restart")
	if res2.HolderStarted {
		t.Fatal("没有重新启动守护进程，不应报告 HolderStarted")
	}
	if !res2.Started {
		t.Fatal("Agent 应被重新启动")
	}
}

func TestInstallWindowsStoppedHolderGetsNewImage(t *testing.T) {
	f := newFakeWin(t, false)
	c := winHolderCfg(t, common{user: true})
	env := testEnv(t)
	res, err := doInstallWindows(env, c, f.w)
	if err != nil {
		t.Fatal(err)
	}
	f.running[res.HolderTaskBinPath] = false // 守护进程已经不在运行
	if err := os.WriteFile(f.w.exe, append(makePE(0x20b, peSubsystemCUI), []byte("v2")...), 0o755); err != nil {
		t.Fatal(err)
	}
	res2, err := doInstallWindows(env, c, f.w)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(res2.HolderTaskBinPath)
	if !strings.HasSuffix(string(b), "v2") || !res2.HolderStarted || len(res2.Notes) != 0 {
		t.Fatalf("没有在运行的守护进程应换上新映像并启动: started=%v notes=%v", res2.HolderStarted, res2.Notes)
	}
}

func TestInstallWindowsHolderTaskFailureIsReported(t *testing.T) {
	f := newFakeWin(t, false)
	f.failOn["schtasks /Run /TN RelayMeshHolder"] = errors.New("exit status 1")
	f.outOn["schtasks /Run /TN RelayMeshHolder"] = "错误: 拒绝访问。"
	_, err := doInstallWindows(testEnv(t), winHolderCfg(t, common{user: true}), f.w)
	if err == nil || !strings.Contains(err.Error(), "守护进程") || !strings.Contains(err.Error(), "拒绝访问") {
		t.Fatalf("守护进程启动失败应如实报告并带上原始输出: %v", err)
	}
}

func TestUninstallWindowsRemovesBothTasksAgentFirst(t *testing.T) {
	f := newFakeWin(t, false)
	env := testEnv(t)
	c := winHolderCfg(t, common{user: true})
	res, err := doInstallWindows(env, c, f.w)
	if err != nil {
		t.Fatal(err)
	}
	f.cmds = nil
	ur, err := doUninstallWindows(env, c, false, resolved{}, f.w)
	if err != nil || !ur.Found {
		t.Fatalf("应找到并卸载: %v %+v", err, ur)
	}
	var seq []string
	for _, cmd := range f.cmds {
		fs := strings.Fields(cmd)
		seq = append(seq, fs[1]+":"+fs[3])
	}
	// 最后一条查询是「远程卸载留下的一次性任务」是否存在（普通的手动卸载里它不存在，只查询不删除）
	want := "/Query:RelayMeshAgent-HOST_alice /End:RelayMeshAgent-HOST_alice /Delete:RelayMeshAgent-HOST_alice " +
		"/Query:RelayMeshHolder-HOST_alice /End:RelayMeshHolder-HOST_alice /Delete:RelayMeshHolder-HOST_alice " +
		"/Query:RelayMeshAgent-HOST_alice-Uninstall"
	if strings.Join(seq, " ") != want {
		t.Fatalf("应先卸 Agent 任务，再卸守护进程任务:\n%s", strings.Join(f.cmds, "\n"))
	}
	for _, p := range []string{res.BinPath, res.TaskBinPath, res.HolderTaskBinPath} {
		if _, err := os.Stat(p); err == nil {
			t.Errorf("应已删除 %s", p)
		}
	}
}

func TestUninstallWindowsOrphanHolderProcessIsStopped(t *testing.T) {
	// 守护进程任务已被手动删掉但进程还在：卸载仍要结束它，否则映像文件删不掉。
	f := newFakeWin(t, false)
	env := testEnv(t)
	c := winHolderCfg(t, common{user: true})
	res, err := doInstallWindows(env, c, f.w)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.w.run("schtasks", "/Delete", "/TN", res.HolderTaskName, "/F"); err != nil {
		t.Fatal(err)
	}
	f.cmds = nil
	if _, err := doUninstallWindows(env, c, false, resolved{}, f.w); err != nil {
		t.Fatal(err)
	}
	if f.running[res.HolderTaskBinPath] {
		t.Fatal("残留的守护进程应被结束")
	}
	if _, err := os.Stat(res.HolderTaskBinPath); err == nil {
		t.Fatal("守护进程映像应被删除")
	}
}

func TestUninstallWindowsPurgeRemovesHolderState(t *testing.T) {
	f := newFakeWin(t, false)
	env := testEnv(t)
	c := winHolderCfg(t, common{user: true})
	res, err := doInstallWindows(env, c, f.w)
	if err != nil {
		t.Fatal(err)
	}
	stateDir := holder.StateDir(res.DataDir)
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stateDir, "state.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(res.HolderLogPath, []byte("log"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := doUninstallWindows(env, c, true, resolved{}, f.w); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{filepath.Join(stateDir, "state.json"), res.HolderLogPath} {
		if _, err := os.Stat(p); err == nil {
			t.Errorf("--purge 应清理 %s", p)
		}
	}
	if _, err := agent.LoadIdentity(res.DataDir); err == nil {
		t.Fatal("--purge 应删除身份")
	}
}

func TestReportWindowsMentionsHolder(t *testing.T) {
	f := newFakeWin(t, false)
	env := testEnv(t)
	if _, err := doInstallWindows(env, winHolderCfg(t, common{user: true}), f.w); err != nil {
		t.Fatal(err)
	}
	env2 := testEnv(t)
	reportWindows(env2, f.w)
	o := out(env2)
	if !strings.Contains(o, "RelayMeshAgent-HOST_alice") || !strings.Contains(o, "会话守护进程") ||
		!strings.Contains(o, "RelayMeshHolder-HOST_alice") || strings.Count(o, "运行中") < 2 {
		t.Fatalf("状态应同时报告 Agent 与守护进程任务: %q", o)
	}
}
