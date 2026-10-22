package terminal

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/teacat99/RelayMesh/v2/internal/store"
	"github.com/teacat99/RelayMesh/v2/internal/termproto"
)

// 会话页「启用 Agent」（#176 第三期）：待接入会话的一键启用 = 建（或复用）一条打开了「新开对话」的预设并运行它。

// enableFixture 在执行夹具上建一个网页新建的「待接入」会话；假主机对每次运行立即成功结束，输出里带着新对话的编号。
func enableFixture(t *testing.T) (*execFixture, string) {
	t.Helper()
	return enableFixtureWith(t, store.CreateWebWorkflowInput{Title: "支付 重构"})
}

// enableFixtureWith 与 enableFixture 一样，但可以指定网页新建会话时的选项（比如绑定项目目录，#184）。
func enableFixtureWith(t *testing.T, in store.CreateWebWorkflowInput) (*execFixture, string) {
	t.Helper()
	f := newExecFixture(t)
	f.completeReadable(newConvOut)
	res, err := f.env.store.CreateWebWorkflow(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	return f, res.Session.WorkflowID
}

func (f *execFixture) enablePresets(t *testing.T) []store.TerminalLauncher {
	t.Helper()
	all, err := f.env.store.ListTerminalLaunchers(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var out []store.TerminalLauncher
	for _, l := range all {
		if l.Run != nil && l.Run.Origin == store.HeadlessOriginEnableAgent {
			out = append(out, l)
		}
	}
	return out
}

func TestSessionEnablePreviewIsReadOnlyAndExplainsWhatIsMissing(t *testing.T) {
	f, wf := enableFixture(t)
	login := []string{"Authorization", humanAuth(testUser)}
	url := "/api/v1/terminal/session-enable/preview"

	if res := f.env.call("POST", url, map[string]any{"workflow_id": wf}); res.Status != 401 {
		t.Fatalf("没登录应 401: %d %s", res.Status, res.Raw)
	}

	// 只有一台主机：自动选它；目录没填：不能运行，原因写明。
	res := f.env.call("POST", url, map[string]any{"workflow_id": wf}, login...)
	pv, _ := res.Body["preview"].(map[string]any)
	if res.Status != 200 || pv["pending"] != true || pv["host"] != f.a.code || pv["can_run"] != false || pv["new_conversation"] != true || pv["title"] != "支付 重构" {
		t.Fatalf("预览不对: %d %s", res.Status, res.Raw)
	}
	if !previewHasIssue(pv, "no_dir") {
		t.Fatalf("没填目录应报 no_dir: %s", res.Raw)
	}

	// 填了目录：可以运行；默认姿态是白名单（接口不替调用方放宽权限），命令不带 --resume，也不带 --force。
	res = f.env.call("POST", url, map[string]any{"workflow_id": wf, "dir": "/work/app"}, login...)
	pv, _ = res.Body["preview"].(map[string]any)
	cmd, _ := pv["command"].(string)
	if pv["can_run"] != true || pv["posture"] != "allowlist" || strings.Contains(cmd, "--resume") || strings.Contains(cmd, "--force") ||
		!strings.Contains(cmd, "--workspace '/work/app'") || !strings.Contains(cmd, wf) {
		t.Fatalf("预览不对: %s", res.Raw)
	}
	if !previewHasWarning(pv, "没有带 --force") || !previewHasWarning(pv, "会新开一个对话") {
		t.Fatalf("应提示白名单与新开对话: %s", res.Raw)
	}

	// 选「放开全部命令」：命令里有 --force；最长运行时间按给的值（24 小时）。
	res = f.env.call("POST", url, map[string]any{"workflow_id": wf, "dir": "/work/app", "posture": "force", "max_run_minutes": 1440}, login...)
	pv, _ = res.Body["preview"].(map[string]any)
	cmd, _ = pv["command"].(string)
	if pv["posture"] != "force" || !strings.Contains(cmd, " --force ") || pv["max_run_minutes"] != float64(1440) || !strings.Contains(cmd, "本次运行最长 1 天") {
		t.Fatalf("force 预览不对: %s", res.Raw)
	}

	// 主机不对、系统不支持、选项不合法：各自有明确原因。
	res = f.env.call("POST", url, map[string]any{"workflow_id": wf, "dir": "/work/app", "model": "a b"}, login...)
	if pv, _ = res.Body["preview"].(map[string]any); pv["can_run"] != false || !previewHasIssue(pv, "bad_options") {
		t.Fatalf("模型名不合法应报 bad_options: %s", res.Raw)
	}
	res = f.env.call("POST", url, map[string]any{"workflow_id": wf, "dir": "work/app"}, login...)
	if pv, _ = res.Body["preview"].(map[string]any); !previewHasIssue(pv, "bad_dir") {
		t.Fatalf("相对目录应报 bad_dir: %s", res.Raw)
	}
	res = f.env.call("POST", url, map[string]any{"workflow_id": "wf-nope", "dir": "/work/app"}, login...)
	if pv, _ = res.Body["preview"].(map[string]any); pv["can_run"] != false || !previewHasIssue(pv, "workflow_not_found") {
		t.Fatalf("不存在的工作流应报 workflow_not_found: %s", res.Raw)
	}
	res = f.env.call("POST", url, map[string]any{"dir": "/work/app"}, login...)
	if pv, _ = res.Body["preview"].(map[string]any); !previewHasIssue(pv, "no_workflow") {
		t.Fatalf("缺工作流编号应报 no_workflow: %s", res.Raw)
	}

	// 预览不产生任何东西。
	if f.sim.runCount() != 0 || len(f.enablePresets(t)) != 0 {
		t.Fatalf("预览不该提交运行或建预设: runs=%d presets=%d", f.sim.runCount(), len(f.enablePresets(t)))
	}
}

func TestSessionEnableNeedsUnlockCreatesPresetRunsAndAuditsWithoutPromptOrCommand(t *testing.T) {
	f, wf := enableFixture(t)
	login := []string{"Authorization", humanAuth(testUser)}
	url := "/api/v1/terminal/session-enable"
	body := map[string]any{"workflow_id": wf, "dir": "/work/app", "posture": "force", "max_run_minutes": 1440, "model": "claude-sonnet-5-5-max"}

	if res := f.env.call("POST", url, body); res.Status != 401 {
		t.Fatalf("没登录应 401: %d %s", res.Status, res.Raw)
	}
	if res := f.env.call("POST", url, body, login...); res.Status == 200 {
		t.Fatalf("只登录没解锁不该能启用（启用 = 让主机上的 agent 动手）: %d %s", res.Status, res.Raw)
	}
	if f.sim.runCount() != 0 || len(f.enablePresets(t)) != 0 {
		t.Fatal("被拒绝的请求不该建预设或到达主机")
	}
	// 选项不全 / 不合法：400 或 409，不建预设、不运行。
	if bad := f.env.call("POST", url, map[string]any{"workflow_id": wf}, f.adm...); bad.Status != 409 || bad.Body["code"] != "no_dir" {
		t.Fatalf("没填目录应 409 no_dir: %d %s", bad.Status, bad.Raw)
	}
	if bad := f.env.call("POST", url, map[string]any{"dir": "/work/app"}, f.adm...); bad.Status != 400 || bad.Body["code"] != "no_workflow" {
		t.Fatalf("缺工作流应 400: %d %s", bad.Status, bad.Raw)
	}
	if f.sim.runCount() != 0 || len(f.enablePresets(t)) != 0 {
		t.Fatal("被拒绝的请求不该建预设或到达主机")
	}

	res := f.env.call("POST", url, body, f.adm...)
	if res.Status != 200 {
		t.Fatalf("启用失败: %d %s", res.Status, res.Raw)
	}
	run, _ := res.Body["run"].(map[string]any)
	preset, _ := res.Body["preset"].(map[string]any)
	if run["run_id"] == "" || run["new_conversation"] != true || run["posture"] != "force" || preset["id"] != enablePresetID(wf) ||
		!strings.HasPrefix(preset["name"].(string), "启用 Agent · 支付 重构 · ") {
		t.Fatalf("响应不对: %s", res.Raw)
	}
	if _, ok := res.Body["prefs"]; !ok {
		t.Fatalf("响应应带上最新的预设清单（界面据此刷新）: %s", res.Raw)
	}
	runID, _ := run["run_id"].(string)

	// 预设：来源标记、新开对话、姿态、24 小时。
	presets := f.enablePresets(t)
	if len(presets) != 1 {
		t.Fatalf("应建出一条预设: %+v", presets)
	}
	p := presets[0]
	if p.ID != enablePresetID(wf) || p.Kind != store.LauncherKindHeadless || p.Host != f.a.code || p.Dir != "/work/app" ||
		!p.Run.NewConversation || p.Run.WorkflowID != wf || p.Run.Posture != store.HeadlessPostureForce || p.Run.MaxRunMinutes != 1440 || p.Run.AutoResume {
		t.Fatalf("预设不对: %+v %+v", p, p.Run)
	}

	// 命令：没有 --resume，带 --force 与模型，提示词是新开对话版本，超时是 24 小时。
	eventually(t, 2*time.Second, "假主机收到运行", func() bool { return f.sim.runCount() == 1 })
	hostRun := f.sim.lastRun()
	if strings.Contains(hostRun.Cmd, "--resume") || !strings.Contains(hostRun.Cmd, " --force ") || !strings.Contains(hostRun.Cmd, "--model 'claude-sonnet-5-5-max'") ||
		!strings.Contains(hostRun.Cmd, "workflow_id \""+wf+"\"") || hostRun.TimeoutSec != 1440*60 {
		t.Fatalf("命令不对: %+v", hostRun)
	}
	f.waitRunFinal(t, runID)

	// 审计：只记工作流、预设、姿态与是否新开，不含命令、提示词、模型。
	logs := f.env.auditEvents(EvSessionEnable)
	if len(logs) != 1 || logs[0].RunID != runID || logs[0].MachineCode != f.a.code ||
		!strings.Contains(logs[0].Detail, "workflow="+wf) || !strings.Contains(logs[0].Detail, "preset="+enablePresetID(wf)) ||
		!strings.Contains(logs[0].Detail, "posture=force") || !strings.Contains(logs[0].Detail, "new=true") {
		t.Fatalf("审计应记一条 session_enable: %+v", logs)
	}
	for _, l := range logs {
		if strings.Contains(l.Detail, "agent -p") || strings.Contains(l.Detail, "接入") || strings.Contains(l.Detail, "claude") {
			t.Fatalf("审计不该含命令、提示词或模型: %q", l.Detail)
		}
	}

	// 对话编号学到了：再启用同一个会话复用同一条预设，并且是接着它（续聊），不会再新开。
	eventually(t, 3*time.Second, "学到对话编号", func() bool {
		return f.env.store.AgentLinkOf(context.Background(), wf).ConversationID == "chat-new-1"
	})
	res = f.env.call("POST", "/api/v1/terminal/session-enable/preview", map[string]any{"workflow_id": wf, "dir": "/work/app", "posture": "force"}, login...)
	pv, _ := res.Body["preview"].(map[string]any)
	pp, _ := pv["preset"].(map[string]any)
	if pv["new_conversation"] != false || pv["conversation_id"] != "chat-new-1" || pp["id"] != enablePresetID(wf) || pv["last_run"] == nil ||
		!strings.Contains(pv["command"].(string), "--resume chat-new-1") {
		t.Fatalf("再启用的预览应是续聊、带上已有的预设与上次运行: %s", res.Raw)
	}
	res = f.env.call("POST", url, body, f.adm...)
	if res.Status != 200 {
		t.Fatalf("再次启用失败: %d %s", res.Status, res.Raw)
	}
	run, _ = res.Body["run"].(map[string]any)
	if run["new_conversation"] == true || run["conversation_id"] != "chat-new-1" || len(f.enablePresets(t)) != 1 {
		t.Fatalf("再次启用应复用预设并续聊: %s presets=%d", res.Raw, len(f.enablePresets(t)))
	}
	f.waitRunFinal(t, run["run_id"].(string))
}

func TestSessionEnableOnlyForPendingWorkflowsAndRollsBackOnFailure(t *testing.T) {
	f, wf := enableFixture(t)
	body := map[string]any{"workflow_id": wf, "dir": "/work/app"}
	url := "/api/v1/terminal/session-enable"

	// 运行被拒绝（这里是主机离线）：刚新建的预设要撤掉，不留半成品。
	f.a.close()
	eventually(t, 2*time.Second, "主机离线", func() bool { return f.env.hub.onlineAgent(f.a.code) == nil })
	res := f.env.call("POST", url, body, f.adm...)
	if res.Status != 409 || res.Body["code"] != "host_offline" {
		t.Fatalf("主机离线应 409 host_offline: %d %s", res.Status, res.Raw)
	}
	if n := len(f.enablePresets(t)); n != 0 {
		t.Fatalf("运行失败应撤掉新建的预设，实际还剩 %d 条", n)
	}

	// 智能体已经接入（有了非占位的会话行）：不能再启用，请用「运行」续聊。
	if _, err := f.env.store.CreateNoticeSession(context.Background(), store.CreateSessionInput{WorkflowID: wf, ProjectDirectory: "/work/app", Summary: "已接入"}); err != nil {
		t.Fatal(err)
	}
	res = f.env.call("POST", url, body, f.adm...)
	if res.Status != 409 || res.Body["code"] != "not_pending" || !strings.Contains(res.Body["error"].(string), "「运行」") {
		t.Fatalf("已接入的工作流应 409 not_pending: %d %s", res.Status, res.Raw)
	}
	if f.sim.runCount() != 0 || len(f.enablePresets(t)) != 0 {
		t.Fatal("被拒绝的请求不该建预设或到达主机")
	}
}

func TestSessionEnableRefusals(t *testing.T) {
	url := "/api/v1/terminal/session-enable"
	body := func(wf string, extra map[string]any) map[string]any {
		m := map[string]any{"workflow_id": wf, "dir": "/work/app"}
		for k, v := range extra {
			m[k] = v
		}
		return m
	}

	t.Run("Windows 主机", func(t *testing.T) {
		f, wf := enableFixture(t)
		meta := testMeta("win-box")
		meta.OS = "windows"
		win := f.env.enrollAgent(meta, "windows")
		login := []string{"Authorization", humanAuth(testUser)}
		res := f.env.call("POST", url+"/preview", body(wf, map[string]any{"host": win.code}), login...)
		pv, _ := res.Body["preview"].(map[string]any)
		if pv["can_run"] != false || !previewHasIssue(pv, "unsupported_os") || !strings.Contains(res.Raw, "开终端启动") {
			t.Fatalf("Windows 主机应报 unsupported_os 并指向「开终端启动」: %s", res.Raw)
		}
		res = f.env.call("POST", url, body(wf, map[string]any{"host": win.code}), f.adm...)
		if res.Status != 409 || res.Body["code"] != "unsupported_os" {
			t.Fatalf("Windows 主机启用应 409: %d %s", res.Status, res.Raw)
		}
		if len(f.enablePresets(t)) != 0 || f.sim.runCount() != 0 {
			t.Fatal("被拒绝的请求不该建预设或到达主机")
		}
		// 两台主机都在、没指定：不替用户猜，让用户选。
		res = f.env.call("POST", url, body(wf, nil), f.adm...)
		if res.Status != 409 || res.Body["code"] != "no_host" {
			t.Fatalf("多台主机没选应 409 no_host: %d %s", res.Status, res.Raw)
		}
	})

	t.Run("执行闸门拒绝：主机没开模型访问", func(t *testing.T) {
		f, wf := enableFixture(t)
		f.setModel(t, map[string]any{"allow_model": false})
		res := f.env.call("POST", url, body(wf, nil), f.adm...)
		if res.Status != 409 && res.Status != 403 {
			t.Fatalf("闸门拒绝应是 4xx: %d %s", res.Status, res.Raw)
		}
		if n := len(f.enablePresets(t)); n != 0 || f.sim.runCount() != 0 {
			t.Fatalf("被闸门拒绝不该留下预设或运行: presets=%d runs=%d", n, f.sim.runCount())
		}
	})

	t.Run("预设已满", func(t *testing.T) {
		f, wf := enableFixture(t)
		ctx := context.Background()
		for i := 0; i < store.MaxTerminalLaunchers; i++ {
			l := store.TerminalLauncher{ID: "fill-" + string(rune('a'+i)), Name: "占位" + string(rune('A'+i)), Host: f.a.code, Dir: "/work/app"}
			if _, _, err := f.env.store.UpsertTerminalLauncher(ctx, l); err != nil {
				t.Fatal(err)
			}
		}
		res := f.env.call("POST", url, body(wf, nil), f.adm...)
		if res.Status != 409 || res.Body["code"] != "launcher_limit" || !strings.Contains(res.Body["error"].(string), "删掉不用的预设") {
			t.Fatalf("预设满了应 409 launcher_limit: %d %s", res.Status, res.Raw)
		}
		if f.sim.runCount() != 0 {
			t.Fatal("被拒绝的请求不该到达主机")
		}
	})

	t.Run("上一次运行还没结束", func(t *testing.T) {
		f, wf := enableFixture(t)
		f.sim.onRun = func(s *hostSim, m termproto.Run) { s.started(m.ID) } // 一直在跑
		if res := f.env.call("POST", url, body(wf, nil), f.adm...); res.Status != 200 {
			t.Fatalf("第一次应成功: %d %s", res.Status, res.Raw)
		}
		res := f.env.call("POST", url, body(wf, nil), f.adm...)
		if res.Status != 409 || res.Body["code"] != "run_active" {
			t.Fatalf("上一次还在跑应 409 run_active（不会开出第二个智能体）: %d %s", res.Status, res.Raw)
		}
		if n := f.sim.runCount(); n != 1 || len(f.enablePresets(t)) != 1 {
			t.Fatalf("只该有一次运行、一条预设: runs=%d presets=%d", n, len(f.enablePresets(t)))
		}
	})
}

// #184：网页新建会话时绑定了项目目录 → 「启用 Agent」没填运行目录就默认用它，填了不同的目录提前报 dir_mismatch；
// 否则智能体起来后第一次汇报带的目录与绑定目录不一致，会被 D-161（项目所有权）静默拒绝。没绑定目录的会话与以前完全一样。
func TestSessionEnableBoundDirectory(t *testing.T) {
	url := "/api/v1/terminal/session-enable"
	login := []string{"Authorization", humanAuth(testUser)}
	preview := func(f *execFixture, wf string, extra map[string]any) (map[string]any, string) {
		t.Helper()
		m := map[string]any{"workflow_id": wf}
		for k, v := range extra {
			m[k] = v
		}
		res := f.env.call("POST", url+"/preview", m, login...)
		pv, _ := res.Body["preview"].(map[string]any)
		if res.Status != 200 || pv == nil {
			t.Fatalf("预览应 200: %d %s", res.Status, res.Raw)
		}
		return pv, res.Raw
	}

	t.Run("没填运行目录：默认用绑定的目录", func(t *testing.T) {
		f, wf := enableFixtureWith(t, store.CreateWebWorkflowInput{Title: "绑定目录", ProjectDirectory: "/work/app/"})
		pv, raw := preview(f, wf, nil)
		cmd, _ := pv["command"].(string)
		if pv["bound_dir"] != "/work/app" || pv["dir"] != "/work/app" || pv["can_run"] != true || previewHasIssue(pv, "no_dir") ||
			!strings.Contains(cmd, "--workspace '/work/app'") {
			t.Fatalf("没填目录应默认用绑定目录（规整后无尾部斜杠）并可运行: %s", raw)
		}
		// 写成等价形式（带尾部斜杠）：规整后相等，不算不一致。
		pv, raw = preview(f, wf, map[string]any{"dir": "/work/app/"})
		if pv["can_run"] != true || previewHasIssue(pv, "dir_mismatch") {
			t.Fatalf("等价写法不该报不一致: %s", raw)
		}
	})

	t.Run("填了别的目录：提前报 dir_mismatch，不建预设也不到达主机", func(t *testing.T) {
		f, wf := enableFixtureWith(t, store.CreateWebWorkflowInput{Title: "绑定目录", ProjectDirectory: "/work/app"})
		pv, raw := preview(f, wf, map[string]any{"dir": "/work/other"})
		cmd, _ := pv["command"].(string)
		if pv["can_run"] != false || !previewHasIssue(pv, "dir_mismatch") || !strings.Contains(raw, "绑定了项目目录 /work/app") {
			t.Fatalf("目录不一致应报 dir_mismatch 并点出绑定的目录: %s", raw)
		}
		if !strings.Contains(cmd, "--workspace '/work/other'") {
			t.Fatalf("目录不一致不该挡住命令预览（用户要能看到自己填的命令）: %s", raw)
		}
		res := f.env.call("POST", url, map[string]any{"workflow_id": wf, "dir": "/work/other"}, f.adm...)
		if res.Status != 409 || res.Body["code"] != "dir_mismatch" {
			t.Fatalf("提交时目录不一致应 409 dir_mismatch: %d %s", res.Status, res.Raw)
		}
		if len(f.enablePresets(t)) != 0 || f.sim.runCount() != 0 {
			t.Fatal("被拒绝的请求不该建预设或到达主机")
		}
		// 不填目录再提交：用绑定目录运行，预设里的目录就是它。
		res = f.env.call("POST", url, map[string]any{"workflow_id": wf}, f.adm...)
		if res.Status != 200 {
			t.Fatalf("不填目录应按绑定目录运行成功: %d %s", res.Status, res.Raw)
		}
		if ps := f.enablePresets(t); len(ps) != 1 || ps[0].Dir != "/work/app" {
			t.Fatalf("预设的目录应是绑定目录: %+v", ps)
		}
	})

	t.Run("没绑定目录：和以前一样", func(t *testing.T) {
		f, wf := enableFixture(t)
		pv, raw := preview(f, wf, nil)
		if _, has := pv["bound_dir"]; has || !previewHasIssue(pv, "no_dir") || previewHasIssue(pv, "dir_mismatch") {
			t.Fatalf("没绑定目录时不该有 bound_dir，目录仍是必填: %s", raw)
		}
		pv, raw = preview(f, wf, map[string]any{"dir": "/anywhere"})
		if pv["can_run"] != true || previewHasIssue(pv, "dir_mismatch") {
			t.Fatalf("没绑定目录时填什么绝对路径都行: %s", raw)
		}
	})
}

func previewHasIssue(pv map[string]any, code string) bool {
	issues, _ := pv["issues"].([]any)
	for _, i := range issues {
		if m, ok := i.(map[string]any); ok && m["code"] == code {
			return true
		}
	}
	return false
}

func previewHasWarning(pv map[string]any, sub string) bool {
	ws, _ := pv["warnings"].([]any)
	for _, w := range ws {
		if s, ok := w.(string); ok && strings.Contains(s, sub) {
			return true
		}
	}
	return false
}
