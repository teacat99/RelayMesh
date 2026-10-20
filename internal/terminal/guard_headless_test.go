package terminal

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/teacat99/RelayMesh/v2/internal/model"
	"github.com/teacat99/RelayMesh/v2/internal/store"
	"github.com/teacat99/RelayMesh/v2/internal/termproto"
)

// ---------- 纯函数 ----------

func TestHeadlessShellQuote(t *testing.T) {
	cases := map[string]string{
		"plain":       "'plain'",
		"a b":         "'a b'",
		"it's":        `'it'\''s'`,
		"$(rm -rf)":   "'$(rm -rf)'",
		"`x`;&|<>":    "'`x`;&|<>'",
		"":            "''",
		"中文【唤醒】":      "'中文【唤醒】'",
		"a'; touch b": `'a'\''; touch b'`,
	}
	for in, want := range cases {
		if got := shellQuote(in); got != want {
			t.Errorf("shellQuote(%q) = %q，期望 %q", in, got, want)
		}
	}
}

func TestHeadlessBinary(t *testing.T) {
	mk := func(cmd, resume string) store.TerminalLauncher {
		return store.TerminalLauncher{Command: cmd, Guard: &store.LauncherGuard{ResumeCommand: resume}}
	}
	ok := map[string]store.TerminalLauncher{
		"agent":                  mk("agent", ""),
		"/root/.local/bin/agent": mk("/root/.local/bin/agent --foo", ""),
		"~/bin/agent":            mk("x", "~/bin/agent --resume {chat_id}"),
	}
	for want, p := range ok {
		got, err := headlessBinary(p)
		if err != nil || got != want {
			t.Errorf("headlessBinary = %q, %v；期望 %q", got, err, want)
		}
	}
	bad := []store.TerminalLauncher{
		mk("", ""),
		mk("CURSOR_CONFIG_DIR=/x agent", ""),
		mk("cd /x && agent", ""),
		mk(`"agent" -x`, ""),
		mk("agent;rm", "agent;rm"),
	}
	for _, p := range bad {
		if got, err := headlessBinary(p); err == nil {
			t.Errorf("不该接受 %+v，得到 %q", p, got)
		}
	}
}

func TestBuildHeadlessCommand(t *testing.T) {
	prompt := "【系统唤醒 1/3 · 原因：手动唤醒】请继续"
	got, err := buildHeadlessCommand("agent", "chat-9_x", "/work/app", store.HeadlessPostureAllowlist, prompt)
	if err != nil {
		t.Fatal(err)
	}
	want := "agent -p --resume chat-9_x --approve-mcps --trust --workspace '/work/app' --output-format stream-json '【系统唤醒 1/3 · 原因：手动唤醒】请继续'"
	if got != want {
		t.Fatalf("命令不对：\n got %s\nwant %s", got, want)
	}
	if strings.Contains(got, "--force") {
		t.Fatal("allowlist 姿态不能带 --force")
	}
	forced, _ := buildHeadlessCommand("agent", "c1", "/w", store.HeadlessPostureForce, prompt)
	if !strings.Contains(forced, " --trust --force --workspace ") {
		t.Fatalf("force 姿态应带 --force: %s", forced)
	}
	for name, args := range map[string][5]string{
		"二进制含空格": {"a b", "c1", "/w", "allowlist", "p"},
		"对话编号注入": {"agent", "c1; rm -rf /", "/w", "allowlist", "p"},
		"目录不是绝对": {"agent", "c1", "w", "allowlist", "p"},
		"目录含换行":  {"agent", "c1", "/w\nx", "allowlist", "p"},
		"提示词为空":  {"agent", "c1", "/w", "allowlist", " "},
		"提示词换行":  {"agent", "c1", "/w", "allowlist", "a\nb"},
	} {
		if _, err := buildHeadlessCommand(args[0], args[1], args[2], args[3], args[4]); err == nil {
			t.Errorf("%s：应该拒绝", name)
		}
	}
	// 提示词里的单引号（正常不会有，守住最后一道）不能逃出引号。
	q, _ := buildHeadlessCommand("agent", "c1", "/w", "allowlist", "x'; touch /tmp/pwn; echo '")
	if !strings.Contains(q, `'x'\''; touch /tmp/pwn; echo '\'''`) {
		t.Fatalf("单引号没有被转义: %s", q)
	}
}

func TestHeadlessRoute(t *testing.T) {
	p := store.TerminalLauncher{Dir: "/w", Command: "agent", Guard: &store.LauncherGuard{Headless: &store.HeadlessWake{Enabled: true}}}
	live := guardFacts{HostOnline: true, Link: store.AgentLink{ConversationID: "c1"}}
	cases := []struct {
		name string
		mut  func(*store.TerminalLauncher, *guardFacts, *string)
		want string
	}{
		{"都满足", func(*store.TerminalLauncher, *guardFacts, *string) {}, ""},
		{"没开启", func(p *store.TerminalLauncher, _ *guardFacts, _ *string) { p.Guard = &store.LauncherGuard{} }, "没有开启"},
		{"Windows", func(_ *store.TerminalLauncher, _ *guardFacts, os *string) { *os = "Windows 11" }, "Windows"},
		{"主机离线", func(_ *store.TerminalLauncher, f *guardFacts, _ *string) { f.HostOnline = false }, "离线"},
		{"会话还活着", func(_ *store.TerminalLauncher, f *guardFacts, _ *string) { f.Session = &guardSession{SID: "s"} }, "会话还活着"},
		{"不知道对话编号", func(_ *store.TerminalLauncher, f *guardFacts, _ *string) { f.Link.ConversationID = "" }, "对话编号"},
		{"没有目录", func(p *store.TerminalLauncher, _ *guardFacts, _ *string) { p.Dir = "" }, "目录"},
		{"命令不朴素", func(p *store.TerminalLauncher, _ *guardFacts, _ *string) { p.Command = "cd /x && agent" }, "朴素"},
	}
	for _, c := range cases {
		pp, ff, hostOS := p, live, "Linux"
		g := *p.Guard
		pp.Guard = &g
		c.mut(&pp, &ff, &hostOS)
		ok, note := headlessRoute(pp, ff, hostOS)
		if c.want == "" {
			if !ok {
				t.Errorf("%s：应可用，得到 %q", c.name, note)
			}
			continue
		}
		if ok || !strings.Contains(note, c.want) {
			t.Errorf("%s：应不可用且原因含 %q，得到 ok=%v %q", c.name, c.want, ok, note)
		}
	}
}

func TestHeadlessEndTextCoversEveryTerminalState(t *testing.T) {
	zero := 0
	two := 2
	for state, want := range map[string]string{
		model.RunSucceeded: "成功",
		model.RunFailed:    "失败",
		model.RunTimedOut:  "达到运行时长上限",
		model.RunCancelled: "取消",
		model.RunRejected:  "拒绝",
		model.RunDenied:    "闸门",
		model.RunLost:      "丢失",
	} {
		got := headlessEndText(model.TerminalRun{State: state, DurationMS: 12_000, ExitCode: &zero})
		if !strings.Contains(got, want) {
			t.Errorf("%s 的文字 %q 应含 %q", state, got, want)
		}
	}
	if got := headlessEndText(model.TerminalRun{State: model.RunFailed, ExitCode: &two}); !strings.Contains(got, "退出码 2") {
		t.Errorf("失败应带退出码: %q", got)
	}
	if got := headlessEndText(model.TerminalRun{State: model.RunSucceeded, DurationMS: 12_345}); !strings.Contains(got, "12 秒") {
		t.Errorf("应带用时: %q", got)
	}
	// 到点被强制结束：写「达到运行时长上限」并带上这次运行自己的上限（#141）；旧记录没有超时值就不写括号。
	for timeoutSec, want := range map[int]string{
		1800:                       "达到运行时长上限（30 分钟），已被终止",
		2 * 3600:                   "达到运行时长上限（2 小时），已被终止",
		termproto.MaxRunTimeoutSec: "达到运行时长上限（7 天），已被终止",
		0:                          "达到运行时长上限，已被终止",
	} {
		got := runEndText("预设「续聊」的非交互续聊", model.TerminalRun{State: model.RunTimedOut, TimeoutSec: timeoutSec})
		if !strings.Contains(got, want) || strings.Contains(got, "超时被终止") {
			t.Errorf("TimeoutSec=%d 的结束行 %q 应含 %q", timeoutSec, got, want)
		}
	}
}

// ---------- 经 Exec 通道的集成 ----------

const headlessWF = "wf-headless"

// headlessFixture 在执行夹具上配好一个带非交互唤醒的预设，并让工作流 hook 报过对话编号。
func headlessFixture(t *testing.T, headless map[string]any) (*execFixture, string) {
	t.Helper()
	f := newExecFixture(t)
	guard := map[string]any{"enabled": true, "workflow_id": headlessWF}
	if headless != nil {
		guard["headless"] = headless
	}
	res := f.env.call("PUT", "/api/v1/terminal/launchers/p1", map[string]any{
		"name": "Cursor CLI", "host": f.a.code, "dir": "/work/app", "command": "agent", "guard": guard,
	}, f.adm...)
	if res.Status != 200 {
		t.Fatalf("保存预设失败: %d %s", res.Status, res.Raw)
	}
	now := time.Now()
	f.env.store.RecordHookSeen(headlessWF, now)
	f.env.store.RecordAgentEvent(context.Background(), headlessWF, store.AgentEvent{Kind: store.AgentEventTool, ConversationID: "chat-9"}, now)
	return f, "p1"
}

func (f *execFixture) wakeHeadless(t *testing.T, id string) (GuardWakePlan, error) {
	t.Helper()
	plan, _, err := f.env.hub.guardian.wake(context.Background(), id, GuardWakeHeadless, time.Now())
	return plan, err
}

func wantWakeCode(t *testing.T, err error, code string) {
	t.Helper()
	we, ok := err.(*wakeError)
	if !ok || we.Code != code {
		t.Fatalf("期望唤醒错误码 %q，得到 %v", code, err)
	}
}

func TestHeadlessWakeRunsOnceThroughTheExecChannelAndLeavesChatLines(t *testing.T) {
	f, id := headlessFixture(t, map[string]any{"enabled": true})
	f.completeWith("{\"type\":\"result\"}\n")

	plan, err := f.wakeHeadless(t, id)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Mode != GuardWakeHeadless || plan.RunID == "" || plan.N != 1 || plan.Max != 3 || plan.Posture != store.HeadlessPostureAllowlist {
		t.Fatalf("计划不对: %+v", plan)
	}
	if plan.Prompt != "" || plan.Command != "" {
		t.Fatalf("服务端直接执行，计划里不该带提示词与命令: %+v", plan)
	}
	if len(plan.Warnings) == 0 || !strings.Contains(plan.Warnings[0], "Mcp(") {
		t.Fatalf("allowlist 姿态应提示白名单的前提: %v", plan.Warnings)
	}

	eventually(t, 2*time.Second, "假主机收到运行", func() bool { return f.sim.runCount() == 1 })
	run := f.sim.lastRun()
	wantPrefix := "agent -p --resume chat-9 --approve-mcps --trust --workspace '/work/app' --output-format stream-json '【系统唤醒 1/3 · 原因：手动唤醒】"
	if !strings.HasPrefix(run.Cmd, wantPrefix) || strings.Contains(run.Cmd, "--force") {
		t.Fatalf("命令不对: %s", run.Cmd)
	}
	if run.Cwd != "/work/app" {
		t.Fatalf("应在预设目录里运行: %q", run.Cwd)
	}

	rec := f.dbRun(t, plan.RunID)
	if rec.CredentialName != headlessCallerName || rec.WorkflowID != headlessWF || !strings.Contains(rec.Description, "非交互唤醒") {
		t.Fatalf("运行记录的归属不对: %+v", rec)
	}

	// 登记了一次唤醒；聊天里先后有「发起」与「结束」两条系统行，都不含提示词与命令。
	st, _ := f.env.store.GetTerminalGuardState(context.Background())
	if ps := st.Presets[id]; ps == nil || len(ps.Wakes) != 1 || ps.Wakes[0].Mode != GuardWakeHeadless {
		t.Fatalf("应登记一次 headless 唤醒: %+v", ps)
	}
	var texts []string
	eventually(t, 3*time.Second, "聊天里出现结束行", func() bool {
		evs, _ := f.env.store.StreamEvents(context.Background(), "wf:"+headlessWF, store.StreamRange{Dir: store.StreamOlder, Limit: 10})
		texts = texts[:0]
		for _, e := range evs {
			texts = append(texts, e.Text)
		}
		return len(texts) == 2
	})
	joined := strings.Join(texts, "\n")
	if !strings.Contains(joined, "你手动发起了非交互唤醒（第 1/3 次") || !strings.Contains(joined, "非交互唤醒已结束：成功") {
		t.Fatalf("系统行不对: %q", joined)
	}
	if strings.Contains(joined, "agent -p") || strings.Contains(joined, "系统唤醒 1/3") || strings.Contains(joined, "chat-9") {
		t.Fatalf("系统行里不该有命令、提示词或对话编号: %q", joined)
	}
}

func TestHeadlessWakeForcePostureAddsForceAndGuardViewShowsState(t *testing.T) {
	f, id := headlessFixture(t, map[string]any{"enabled": true, "posture": "force"})
	f.completeWith("")

	view, err := f.env.hub.GuardView(context.Background())
	if err != nil || len(view.Items) != 1 {
		t.Fatalf("GuardView: %v %+v", err, view)
	}
	h := view.Items[0].Headless
	if h == nil || !h.Enabled || !h.Ok || h.Posture != store.HeadlessPostureForce {
		t.Fatalf("现状应是可用的 force: %+v", h)
	}

	if _, err := f.wakeHeadless(t, id); err != nil {
		t.Fatal(err)
	}
	eventually(t, 2*time.Second, "假主机收到运行", func() bool { return f.sim.runCount() == 1 })
	if !strings.Contains(f.sim.lastRun().Cmd, " --trust --force --workspace ") {
		t.Fatalf("force 姿态应带 --force: %s", f.sim.lastRun().Cmd)
	}
}

func TestHeadlessWakeRefusals(t *testing.T) {
	t.Run("没开启", func(t *testing.T) {
		f, id := headlessFixture(t, nil)
		_, err := f.wakeHeadless(t, id)
		wantWakeCode(t, err, "headless_off")
		if view, _ := f.env.hub.GuardView(context.Background()); view.Items[0].Headless != nil {
			t.Fatalf("没开启时视图里不该有 headless: %+v", view.Items[0].Headless)
		}
	})
	t.Run("会话还活着", func(t *testing.T) {
		f, id := headlessFixture(t, map[string]any{"enabled": true})
		ag := f.env.hub.onlineAgent(f.a.code)
		ag.mu.Lock()
		ag.sessions = []termproto.SessionInfo{{SID: sidOf('a'), Name: "Cursor CLI", State: termproto.SessionRunning, Created: time.Now().Unix()}}
		ag.mu.Unlock()
		_, err := f.wakeHeadless(t, id)
		wantWakeCode(t, err, "headless_unavailable")
		if f.sim.runCount() != 0 {
			t.Fatal("不该提交运行")
		}
	})
	t.Run("不知道对话编号", func(t *testing.T) {
		f := newExecFixture(t)
		f.env.call("PUT", "/api/v1/terminal/launchers/p1", map[string]any{
			"name": "Cursor CLI", "host": f.a.code, "dir": "/work/app", "command": "agent",
			"guard": map[string]any{"enabled": true, "workflow_id": "wf-nochat", "headless": map[string]any{"enabled": true}},
		}, f.adm...)
		_, err := f.wakeHeadless(t, "p1")
		wantWakeCode(t, err, "headless_unavailable")
	})
	t.Run("次数用完是硬上限，不占名额也不提交", func(t *testing.T) {
		f, id := headlessFixture(t, map[string]any{"enabled": true})
		f.completeWith("")
		for i := 0; i < guardWakeMax; i++ {
			if _, err := f.wakeHeadless(t, id); err != nil {
				t.Fatalf("第 %d 次: %v", i+1, err)
			}
		}
		_, err := f.wakeHeadless(t, id)
		wantWakeCode(t, err, "over_budget")
		st, _ := f.env.store.GetTerminalGuardState(context.Background())
		if n := len(st.Presets[id].Wakes); n != guardWakeMax {
			t.Fatalf("被拒绝的这次不该登记: %d", n)
		}
		eventually(t, 2*time.Second, "三次运行都到了主机", func() bool { return f.sim.runCount() == guardWakeMax })
		view, _ := f.env.hub.GuardView(context.Background())
		if h := view.Items[0].Headless; h == nil || h.Ok || !strings.Contains(h.Note, "上限") {
			t.Fatalf("视图应标出达到上限: %+v", h)
		}
	})
	t.Run("执行闸门拒绝时名额要还回去", func(t *testing.T) {
		f, id := headlessFixture(t, map[string]any{"enabled": true})
		f.setModel(t, map[string]any{"allow_model": false})
		_, err := f.wakeHeadless(t, id)
		if err == nil {
			t.Fatal("主机没开模型访问，应被拒绝")
		}
		if _, ok := err.(*wakeError); !ok {
			t.Fatalf("应转换成面向界面的唤醒错误: %T %v", err, err)
		}
		st, _ := f.env.store.GetTerminalGuardState(context.Background())
		if ps := st.Presets[id]; ps != nil && len(ps.Wakes) != 0 {
			t.Fatalf("提交失败不该占名额: %+v", ps.Wakes)
		}
		if f.sim.runCount() != 0 {
			t.Fatal("不该有运行到达主机")
		}
	})
	t.Run("需批准模式：运行停在待批准", func(t *testing.T) {
		f, id := headlessFixture(t, map[string]any{"enabled": true})
		f.setModel(t, map[string]any{"allow_model": true, "exec_mode": "approve"})
		plan, err := f.wakeHeadless(t, id)
		if err != nil {
			t.Fatal(err)
		}
		if plan.RunState != model.RunAwaitingApproval {
			t.Fatalf("应停在待批准: %+v", plan)
		}
		if f.sim.runCount() != 0 {
			t.Fatal("批准之前不该到达主机")
		}
		joined := strings.Join(plan.Warnings, "|")
		if !strings.Contains(joined, "需批准") {
			t.Fatalf("应提示要去批准: %v", plan.Warnings)
		}
	})
}
