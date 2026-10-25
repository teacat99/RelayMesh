package terminal

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/teacat99/RelayMesh/v2/internal/agentstate"
	"github.com/teacat99/RelayMesh/v2/internal/store"
	"github.com/teacat99/RelayMesh/v2/internal/termproto"
)

// Windows 主机上的非交互运行（#178）：用假 Windows 主机把「预设运行 / 启用 Agent / 会话页运行」三条路径各走一遍，
// 核对到达主机的运行（shell、工作目录、命令）与各种拒绝原因。命令本身的字符矩阵见 headless_powershell_test.go。

// addWindowsHost 在执行夹具上再接入一台 Windows 主机（假守护进程）并放开它的模型访问。
// shell 是守护进程上报的默认 shell（pwsh / powershell / cmd）；空串表示旧版守护进程、不上报执行环境。
func (f *execFixture) addWindowsHost(t *testing.T, shell string) (*fakeAgent, *hostSim) {
	t.Helper()
	meta := testMeta("win-box")
	meta.OS = "windows"
	win := f.env.enrollAgent(meta, "win")
	sim := attachSim(win)
	h := termproto.Holder{T: termproto.THolder, State: termproto.HolderReady, Persistent: true, Version: "test", Caps: []string{"policy", "drain", "lost", "exec"}}
	if shell != "" {
		h.Exec = &termproto.ExecInfo{Shell: shell, Home: `C:\Users\test`, Sep: `\`}
	}
	win.send(h)
	eventually(t, 2*time.Second, "Hub 识别 Windows 主机的 exec 能力", func() bool {
		ag := f.env.hub.onlineAgent(win.code)
		if ag == nil {
			return false
		}
		ok, _, _ := ag.execSupport()
		return ok
	})
	if res := f.env.call("POST", "/api/v1/terminal/hosts/"+win.code+"/model", map[string]any{"allow_model": true}, f.adm...); res.Status != 200 {
		t.Fatalf("放开 Windows 主机的模型访问失败: %d %s", res.Status, res.Raw)
	}
	return win, sim
}

// completeReadable 让这台假主机对每个 run 立即成功结束，输出同时能被 Hub 的运行监视读到。
func (s *hostSim) completeReadable(stdout string) {
	s.onRun = func(s *hostSim, m termproto.Run) {
		s.setOutput(m.ID, "stdout", []byte(stdout))
		s.started(m.ID)
		s.end(m.ID, termproto.RunExited, intp(0), stdout, "")
	}
}

// savePresetOn 在指定主机与目录上保存一条非交互续聊预设。
func (f *execFixture) savePresetOn(t *testing.T, id, host, dir string, run map[string]any) {
	t.Helper()
	res := f.env.putLauncher("/api/v1/terminal/launchers/"+id, map[string]any{
		"name": "Windows 续聊 " + id, "host": host, "dir": dir, "kind": "headless", "run": run,
	}, f.adm...)
	if res.Status != 200 {
		t.Fatalf("保存预设失败: %d %s", res.Status, res.Raw)
	}
}

func (f *execFixture) seeConversation(wf, conv string) {
	now := time.Now()
	f.env.store.RecordHookSeen(wf, now)
	f.env.store.RecordAgentEvent(context.Background(), wf, store.AgentEvent{Kind: store.AgentEventTool, ConversationID: conv}, now)
}

// promptLiteralOf 取命令末尾的提示词字面量（--output-format stream-json 之后的部分）。
func promptLiteralOf(t *testing.T, cmd string) string {
	t.Helper()
	const marker = "--output-format stream-json "
	i := strings.LastIndex(cmd, marker)
	if i < 0 {
		t.Fatalf("命令里没有 %q: %s", marker, cmd)
	}
	return cmd[i+len(marker):]
}

func TestLauncherRunOnWindowsHostPassesPowerShellAndExactCommand(t *testing.T) {
	cases := []struct {
		name      string
		reported  string // 守护进程上报的 shell
		wantShell string // 提交运行时指定的 shell
	}{
		{"Windows PowerShell", "powershell", "powershell"},
		{"PowerShell 7", "pwsh", "pwsh"},
		{"上报带 .exe 与大小写", "PowerShell.exe", "powershell"},
		{"旧版守护进程不上报：按 powershell 算", "", "powershell"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newExecFixture(t)
			win, wsim := f.addWindowsHost(t, c.reported)
			wsim.onRun = func(s *hostSim, m termproto.Run) {
				s.started(m.ID)
				s.end(m.ID, termproto.RunExited, intp(0), launcherResultOut, "")
			}
			f.savePresetOn(t, "pw", win.code, `C:\work\app`, map[string]any{"workflow_id": launcherWF, "model": "m-1", "posture": "force"})
			f.seeConversation(launcherWF, "chat-9")

			plan, err := f.runLauncher("pw")
			if err != nil {
				t.Fatalf("Windows 主机上的预设运行应该成功: %v", err)
			}
			// 运行经 WebSocket 异步送到假主机：runLauncher 返回时假主机未必已记下（宿主机负载高时会晚几十毫秒），先等它到，再核对计数。
			eventually(t, 2*time.Second, "Windows 主机收到运行", func() bool { return wsim.runCount() >= 1 })
			if wsim.runCount() != 1 || f.sim.runCount() != 0 {
				t.Fatalf("运行应只到达 Windows 主机: win=%d linux=%d", wsim.runCount(), f.sim.runCount())
			}
			run := wsim.lastRun()
			if run.Shell != c.wantShell {
				t.Errorf("提交运行时的 shell = %q，期望 %q", run.Shell, c.wantShell)
			}
			if run.Cwd != `C:\work\app` {
				t.Errorf("工作目录 = %q", run.Cwd)
			}
			const head = `& 'agent' -p --resume chat-9 --approve-mcps --trust --model 'm-1' --force --workspace 'C:\work\app' --output-format stream-json '`
			if !strings.HasPrefix(run.Cmd, head) || !strings.HasSuffix(run.Cmd, "'") {
				t.Fatalf("命令不是预期的 PowerShell 写法:\n%s", run.Cmd)
			}
			// 提示词字面量里不能有 cmd 会当成语法的字符（双引号换成单引号，其余换成全角）。
			lit := promptLiteralOf(t, run.Cmd)
			if strings.ContainsAny(lit, "\"%^&|<>!") {
				t.Errorf("交给主机的提示词里还有 cmd 语法字符: %s", lit)
			}
			rec := f.waitRunFinal(t, plan.RunID)
			if rec.State != "succeeded" {
				t.Errorf("运行应成功结束: %s", rec.State)
			}
		})
	}
}

func TestLauncherRunOnWindowsHostRefusals(t *testing.T) {
	t.Run("主机上只有 cmd：说明要装 PowerShell，不提交", func(t *testing.T) {
		f := newExecFixture(t)
		win, wsim := f.addWindowsHost(t, "cmd")
		f.savePresetOn(t, "pw", win.code, `C:\work\app`, map[string]any{"workflow_id": launcherWF})
		f.seeConversation(launcherWF, "chat-9")
		in := f.env.hub.guardian.inspectLauncher(context.Background(), mustLauncher(t, f, "pw"), time.Now())
		if !hasErrCode(in, "no_powershell") {
			t.Fatalf("应报 no_powershell: %+v", in.errs)
		}
		if !strings.Contains(in.errs[0].Message, "PowerShell") {
			t.Errorf("说明里应指出缺 PowerShell: %s", in.errs[0].Message)
		}
		_, err := f.runLauncher("pw")
		wantWakeCode(t, err, "no_powershell")
		if wsim.runCount() != 0 {
			t.Fatal("没有 PowerShell 不该提交运行")
		}
	})
	t.Run("Windows 主机离线：只报离线，不报缺 PowerShell", func(t *testing.T) {
		f := newExecFixture(t)
		win, _ := f.addWindowsHost(t, "cmd")
		f.savePresetOn(t, "pw", win.code, `C:\work\app`, map[string]any{"workflow_id": launcherWF})
		f.seeConversation(launcherWF, "chat-9")
		win.close()
		eventually(t, 3*time.Second, "主机离线", func() bool { return f.env.hub.onlineAgent(win.code) == nil })
		in := f.env.hub.guardian.inspectLauncher(context.Background(), mustLauncher(t, f, "pw"), time.Now())
		if !hasErrCode(in, "host_offline") || hasErrCode(in, "no_powershell") {
			t.Fatalf("离线时只该报 host_offline: %+v", in.errs)
		}
		// 离线时命令照样拼得出来（预览能看），shell 按 powershell 算。
		if !strings.HasPrefix(in.command, "& 'agent' -p --resume chat-9 ") || in.shell != "powershell" || !in.windows {
			t.Fatalf("离线时预览应仍给出 PowerShell 命令: shell=%q windows=%v cmd=%s", in.shell, in.windows, in.command)
		}
	})
	t.Run("预设的目录是 POSIX 写法：运行时报目录不对（预设保存时不知道主机系统，放行）", func(t *testing.T) {
		f := newExecFixture(t)
		win, wsim := f.addWindowsHost(t, "powershell")
		f.savePresetOn(t, "pw", win.code, "/work/app", map[string]any{"workflow_id": launcherWF})
		f.seeConversation(launcherWF, "chat-9")
		_, err := f.runLauncher("pw")
		wantWakeCode(t, err, "headless_command")
		if !strings.Contains(err.Error(), "Windows 主机上的绝对路径") {
			t.Errorf("应指出目录要写成 Windows 路径: %v", err)
		}
		if wsim.runCount() != 0 {
			t.Fatal("目录不对不该提交运行")
		}
	})
	t.Run("预设的目录是 Windows 写法但主机是 Linux：运行时报目录不对", func(t *testing.T) {
		f := newExecFixture(t)
		f.savePresetOn(t, "pl", f.a.code, `C:\work\app`, map[string]any{"workflow_id": launcherWF})
		f.seeConversation(launcherWF, "chat-9")
		_, err := f.runLauncher("pl")
		wantWakeCode(t, err, "headless_command")
		if f.sim.runCount() != 0 {
			t.Fatal("目录不对不该提交运行")
		}
	})
}

func TestLauncherRunOnWindowsHostExplainsNeutralizedPromptSymbols(t *testing.T) {
	f := newExecFixture(t)
	win, wsim := f.addWindowsHost(t, "powershell")
	wsim.completeReadable(launcherResultOut)
	f.savePresetOn(t, "pw", win.code, `C:\work\app`, map[string]any{"workflow_id": launcherWF, "prompt": `进度 50% & "收尾" | 完成!`})
	f.seeConversation(launcherWF, "chat-9")
	plan, err := f.runLauncher("pw")
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(plan.Warnings, "|")
	if !strings.Contains(joined, "已换成全角") || !strings.Contains(joined, "4 个符号") { // % & | ! 四个；双引号换成单引号不计
		t.Fatalf("应说明提示词里被换成全角的符号个数: %v", plan.Warnings)
	}
	lit := promptLiteralOf(t, wsim.lastRun().Cmd)
	if !strings.Contains(lit, "进度 50％ ＆ ''收尾'' ｜ 完成！") { // 双引号 → 单引号 → PowerShell 字面量里翻倍
		t.Fatalf("交给主机的提示词不对: %s", lit)
	}

	// 提示词里没有这些符号：没有这条提示。
	f2 := newExecFixture(t)
	win2, wsim2 := f2.addWindowsHost(t, "powershell")
	wsim2.completeReadable(launcherResultOut)
	f2.savePresetOn(t, "pw", win2.code, `C:\work\app`, map[string]any{"workflow_id": launcherWF, "prompt": "接着干"})
	f2.seeConversation(launcherWF, "chat-9")
	plan2, err := f2.runLauncher("pw")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.Join(plan2.Warnings, "|"), "全角") {
		t.Fatalf("没有被换的符号就不该提示: %v", plan2.Warnings)
	}
}

// Linux 主机上的预设运行与以前一样：命令是 POSIX 写法、不指定 shell。
func TestLauncherRunOnLinuxHostStillUsesPosixAndNoShell(t *testing.T) {
	f, id := launcherFixture(t, map[string]any{"workflow_id": launcherWF, "model": "m-1", "posture": "force"})
	f.completeWith(launcherResultOut)
	if _, err := f.runLauncher(id); err != nil {
		t.Fatal(err)
	}
	run := f.sim.lastRun()
	if run.Shell != "" || !strings.HasPrefix(run.Cmd, "agent -p --resume chat-9 --approve-mcps --trust --model 'm-1' --force --workspace '/work/app' --output-format stream-json '") {
		t.Fatalf("Linux 主机的运行变了: shell=%q cmd=%s", run.Shell, run.Cmd)
	}
}

func TestSessionEnableOnWindowsHost(t *testing.T) {
	f, wf := enableFixture(t)
	win, wsim := f.addWindowsHost(t, "powershell")
	wsim.completeReadable(newConvOut)
	login := []string{"Authorization", humanAuth(testUser)}
	url := "/api/v1/terminal/session-enable"
	body := func(extra map[string]any) map[string]any {
		m := map[string]any{"workflow_id": wf, "host": win.code, "dir": `C:\work\app`, "posture": "force"}
		for k, v := range extra {
			m[k] = v
		}
		return m
	}

	// 预览：Windows 主机 + Windows 目录 = 可以运行，命令是 PowerShell 写法。
	res := f.env.call("POST", url+"/preview", body(nil), login...)
	pv, _ := res.Body["preview"].(map[string]any)
	cmd, _ := pv["command"].(string)
	if pv["can_run"] != true || !strings.HasPrefix(cmd, `& 'agent' -p --approve-mcps --trust --force --workspace 'C:\work\app' --output-format stream-json '`) {
		t.Fatalf("Windows 主机的启用预览不对: %s", res.Raw)
	}
	if !strings.Contains(cmd, "workflow_id ''"+wf+"''") { // 内置的新开提示词里 workflow_id "…" 的双引号换成了单引号
		t.Errorf("提示词里的工作流编号应带单引号: %s", cmd)
	}
	prompt, _ := pv["prompt"].(string)
	_, n := winNeutralize(prompt)
	t.Logf("内置新开提示词里被换成全角的符号个数: %d", n)
	if has := previewHasWarning(pv, "已换成全角"); has != (n > 0) {
		t.Errorf("「已换成全角」的提示应与实际替换一致: 提示=%v 个数=%d", has, n)
	}

	// 目录与主机系统对不上、或带 cmd 语法字符：各自 bad_dir。
	for name, dir := range map[string]string{"POSIX 目录放在 Windows 主机上": "/work/app", "目录带 &": `C:\R&D`, "目录带 %": `C:\%TEMP%`, "盘符相对路径": `C:work`} {
		res = f.env.call("POST", url+"/preview", body(map[string]any{"dir": dir}), login...)
		if pv, _ = res.Body["preview"].(map[string]any); pv["can_run"] != false || !previewHasIssue(pv, "bad_dir") {
			t.Errorf("%s：应报 bad_dir: %s", name, res.Raw)
		}
	}
	res = f.env.call("POST", url+"/preview", map[string]any{"workflow_id": wf, "host": f.a.code, "dir": `C:\work\app`}, login...)
	if pv, _ = res.Body["preview"].(map[string]any); pv["can_run"] != false || !previewHasIssue(pv, "bad_dir") {
		t.Errorf("Windows 目录放在 Linux 主机上：应报 bad_dir: %s", res.Raw)
	}
	if wsim.runCount() != 0 || f.sim.runCount() != 0 || len(f.enablePresets(t)) != 0 {
		t.Fatal("预览与被拒绝的请求不该产生运行或预设")
	}

	// 真正启用：预设带 Windows 目录、运行带 powershell。
	res = f.env.call("POST", url, body(nil), f.adm...)
	if res.Status != 200 {
		t.Fatalf("Windows 主机上启用失败: %d %s", res.Status, res.Raw)
	}
	run, _ := res.Body["run"].(map[string]any)
	presets := f.enablePresets(t)
	if len(presets) != 1 || presets[0].Host != win.code || presets[0].Dir != `C:\work\app` {
		t.Fatalf("预设不对: %+v", presets)
	}
	// 同上：运行异步到达假主机，先等再核对。
	eventually(t, 2*time.Second, "Windows 主机收到运行", func() bool { return wsim.runCount() >= 1 })
	if wsim.runCount() != 1 || f.sim.runCount() != 0 {
		t.Fatalf("运行应只到达 Windows 主机: win=%d linux=%d", wsim.runCount(), f.sim.runCount())
	}
	if r := wsim.lastRun(); r.Shell != "powershell" || r.Cwd != `C:\work\app` || !strings.HasPrefix(r.Cmd, `& 'agent' -p `) {
		t.Fatalf("到达主机的运行不对: %+v", r)
	}
	f.waitRunFinal(t, run["run_id"].(string))
}

func TestSessionRunAndLaunchersOnWindowsHost(t *testing.T) {
	f := newExecFixture(t)
	win, wsim := f.addWindowsHost(t, "pwsh")
	wsim.completeReadable(launcherResultOut)
	now := time.Now()
	const wf, conv = "wf-win-run", "chat-wr1"
	// hook 汇报的目录是 Windows 路径（大小写、斜杠与预设里写的不一样）。
	f.sessionEvent(t, wf, conv, "win-box", `c:/Work/App`, "cli", agentstate.EvPrompt, now.Add(-2*time.Second))
	f.sessionEvent(t, wf, conv, "win-box", `c:/Work/App`, "cli", agentstate.EvStop, now.Add(-time.Second))

	// 会话页运行（脚本用的接口）：主机按名字对上，目录取会话上报的，命令是 PowerShell 写法。
	pv := f.sessionPreview(sessionRunRequest{WorkflowID: wf})
	if !pv.CanRun || pv.Host != win.code || pv.Dir != `c:/Work/App` || !strings.HasPrefix(pv.Command, `& 'agent' -p --resume chat-wr1 --approve-mcps --trust --workspace 'c:/Work/App' --output-format stream-json '`) {
		t.Fatalf("Windows 会话的运行预览不对: %+v", pv)
	}
	// 目录写成 POSIX 形式：Windows 主机上不行。
	wantIssue(t, f.sessionPreview(sessionRunRequest{WorkflowID: wf, Dir: "/c/work"}), "bad_dir")
	if _, err := f.runSession(sessionRunRequest{WorkflowID: wf}); err != nil {
		t.Fatalf("会话页运行应成功: %v", err)
	}
	// 运行异步到达假主机（宿主机负载高时 runSession 返回后它未必已记下）：先等，再取最后一次运行。
	eventually(t, 2*time.Second, "Windows 主机收到运行", func() bool { return wsim.runCount() >= 1 })
	if r := wsim.lastRun(); r.Shell != "pwsh" || r.Cwd != `c:/Work/App` {
		t.Fatalf("到达主机的运行不对: %+v", r)
	}
	f.waitRunFinal(t, f.env.hub.guardian.latestSessionRun(context.Background(), wf).RunID)

	// 会话页底部的「运行」视图：没有预设时给出建议（目录规整成反斜杠，名字取最后一级）；有预设时（目录大小写、斜杠不同）也认得出来。
	v, err := f.env.hub.guardian.sessionLaunchers(context.Background(), wf, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if v.Dir != `c:\Work\App` || len(v.Presets) != 0 || v.Suggest == nil || v.Suggest.Dir != `c:\Work\App` || !strings.HasSuffix(v.Suggest.Name, "App") || v.Suggest.Host != win.code {
		t.Fatalf("Windows 会话的预设建议不对: %+v suggest=%+v", v, v.Suggest)
	}
	f.savePresetOn(t, "pw", win.code, `C:\work\app`, map[string]any{"workflow_id": wf})
	v, err = f.env.hub.guardian.sessionLaunchers(context.Background(), wf, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(v.Presets) != 1 || v.Presets[0].ID != "pw" || !v.Presets[0].CanRun || !strings.HasPrefix(v.Presets[0].Command, `& 'agent' -p --resume chat-wr1 `) {
		t.Fatalf("目录写法不同的 Windows 预设应对上这个会话并可运行: %+v", v.Presets)
	}
}

func TestSessionRunWindowsDirOnLinuxHostIsRefused(t *testing.T) {
	f := sessionFixture(t)
	wantIssue(t, f.sessionPreview(sessionRunRequest{WorkflowID: sessWF, ConversationID: sessConv, Dir: `C:\work`}), "bad_dir")
	if _, err := f.runSession(sessionRunRequest{WorkflowID: sessWF, ConversationID: sessConv, Dir: `C:\work`}); err == nil {
		t.Fatal("Windows 目录放在 Linux 主机上不该运行")
	}
	if f.sim.runCount() != 0 {
		t.Fatal("不该到达主机")
	}
}

func mustLauncher(t *testing.T, f *execFixture, id string) store.TerminalLauncher {
	t.Helper()
	all, err := f.env.store.ListTerminalLaunchers(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range all {
		if l.ID == id {
			return l
		}
	}
	t.Fatalf("没有预设 %s", id)
	return store.TerminalLauncher{}
}

func hasErrCode(in *launcherInspect, code string) bool {
	for _, e := range in.errs {
		if e.Code == code {
			return true
		}
	}
	return false
}
