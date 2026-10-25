package terminal

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/teacat99/RelayMesh/v2/internal/agentstate"
	"github.com/teacat99/RelayMesh/v2/internal/model"
	"github.com/teacat99/RelayMesh/v2/internal/sessionprompt"
	"github.com/teacat99/RelayMesh/v2/internal/store"
	"github.com/teacat99/RelayMesh/v2/internal/termproto"
)

// ---------- 纯函数 ----------

func TestHeadlessCmdModelPostureAndGuards(t *testing.T) {
	base := headlessCmd{Bin: "agent", ChatID: "chat-9", Dir: "/work/app", Posture: store.HeadlessPostureAllowlist, Prompt: "请继续"}

	got, err := base.build()
	if err != nil {
		t.Fatal(err)
	}
	want := "agent -p --resume chat-9 --approve-mcps --trust --workspace '/work/app' --output-format stream-json '请继续'"
	if got != want {
		t.Fatalf("没有模型的命令不对：\n got %s\nwant %s", got, want)
	}

	withModel := base
	withModel.Model, withModel.Posture = "claude-sonnet-5-5-max", store.HeadlessPostureForce
	got, err = withModel.build()
	if err != nil {
		t.Fatal(err)
	}
	want = "agent -p --resume chat-9 --approve-mcps --trust --model 'claude-sonnet-5-5-max' --force --workspace '/work/app' --output-format stream-json '请继续'"
	if got != want {
		t.Fatalf("带模型与 --force 的命令不对：\n got %s\nwant %s", got, want)
	}

	for name, mut := range map[string]func(c *headlessCmd){
		"模型有空格":    func(c *headlessCmd) { c.Model = "a b" },
		"模型注入":     func(c *headlessCmd) { c.Model = "x'; touch /tmp/pwn; echo '" },
		"模型以连字符开头": func(c *headlessCmd) { c.Model = "--force" },
		"前缀命令":     func(c *headlessCmd) { c.Bin = "sudo" },
		"shell":    func(c *headlessCmd) { c.Bin = "bash" },
	} {
		c := base
		mut(&c)
		if cmd, err := c.build(); err == nil {
			t.Errorf("%s：应该拒绝，得到 %s", name, cmd)
		}
	}
}

func TestRenderLauncherPrompt(t *testing.T) {
	st := newWakeStore(t)
	ctx := context.Background()
	render := func(custom, wf string) string {
		return renderLauncherWake(ctx, st, launcherWake{Kind: sessionprompt.KindResume, Custom: custom, WorkflowID: wf}).Text
	}
	def := render("", "wf-1")
	for _, need := range []string{"wf-1", "没有人在终端里看你的输出", "interactive_feedback", "结束前都要向 RelayMesh 发一条消息", "结束本次运行", "确认卡"} {
		if !strings.Contains(def, need) {
			t.Errorf("默认提示词应含 %q：%s", need, def)
		}
	}
	if strings.Contains(def, "%") || strings.ContainsAny(def, "\r\n") {
		t.Errorf("默认提示词不该留占位符或换行: %q", def)
	}
	if noWf := render("", ""); !strings.Contains(noWf, "读取我离线期间的追加指令") || strings.Contains(noWf, "%") || strings.Contains(noWf, "工作流 （") {
		t.Errorf("没有工作流编号时默认提示词不点名：%s", noWf)
	}
	if got := render("  看看 %workflow 的收件箱  ", "wf-2"); got != "看看 wf-2 的收件箱" {
		t.Errorf("自定义提示词应替换 %%workflow 并去掉首尾空白: %q", got)
	}
	// 工作流编号里的 shell 特殊字符不会成为问题：提示词最终是单引号包裹的一个参数。
	cmd, err := headlessCmd{Bin: "agent", ChatID: "c1", Dir: "/w", Prompt: render("读 %workflow", "a$(touch x)'b")}.build()
	if err != nil || !strings.Contains(cmd, `'读 a$(touch x)'\''b'`) {
		t.Errorf("特殊字符应被单引号完整包住: %v %s", err, cmd)
	}
}

// 表单里展示的三个默认正文（「以默认为底稿」填进去就要能存）：一行、不超过预设里正文的字数上限、变量原样保留；
// 字数上限与会话 / 全局模板同一个（TemplateMaxRunes）。
func TestExposedPromptDefaultsFitPresetLimits(t *testing.T) {
	if store.MaxHeadlessPromptRunes != sessionprompt.TemplateMaxRunes {
		t.Fatalf("预设正文上限（%d）应与模板上限 %d 一致", store.MaxHeadlessPromptRunes, sessionprompt.TemplateMaxRunes)
	}
	cases := map[string]struct {
		text string
		max  int
	}{
		"续聊":   {sessionprompt.OneLine(sessionprompt.Builtin(sessionprompt.KindResume, true)), store.MaxHeadlessPromptRunes},
		"新开对话": {sessionprompt.OneLine(sessionprompt.Builtin(sessionprompt.KindNew, true)), store.MaxHeadlessPromptRunes},
	}
	for name, c := range cases {
		if n := len([]rune(c.text)); n == 0 || n > c.max {
			t.Errorf("%s 的默认正文 %d 字，应在 1～%d 之间", name, n, c.max)
		}
		if strings.ContainsAny(c.text, "\r\n\t") || !strings.Contains(c.text, "%workflow") {
			t.Errorf("%s 的默认正文应是一行并保留 %%workflow: %q", name, c.text)
		}
	}
}

func TestExtractRunReply(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"取结果事件", "{\"type\":\"system\"}\n{\"type\":\"assistant\",\"message\":{}}\n{\"type\":\"result\",\"is_error\":false,\"result\":\"已恢复现场。\\n收件箱为空。\",\"session_id\":\"c\"}\n", "已恢复现场。 收件箱为空。"},
		{"取最后一个结果事件", "{\"type\":\"result\",\"result\":\"第一次\"}\n{\"type\":\"result\",\"result\":\"第二次\"}\n", "第二次"},
		{"没有结果事件：退回最后一个纯文本行", "{\"type\":\"system\"}\nError: chat not found\n", "Error: chat not found"},
		{"被截断的半行事件不当作回复", "…text\",\"session_id\":\"c\"}\n", ""},
		{"空输出", "", ""},
		{"结果为空：退回纯文本行", "{\"type\":\"result\",\"result\":\"  \"}\nwarn: x\n", "warn: x"},
	}
	for _, c := range cases {
		if got := extractRunReply(c.in); got != c.want {
			t.Errorf("%s：得到 %q，期望 %q", c.name, got, c.want)
		}
	}
	long := "{\"type\":\"result\",\"result\":\"" + strings.Repeat("长", launcherReplyRunes+50) + "\"}"
	if got := extractRunReply(long); len([]rune(got)) > launcherReplyRunes {
		t.Errorf("回复应限长 %d 字，得到 %d 字", launcherReplyRunes, len([]rune(got)))
	}
}

// ---------- 经 Exec 通道的集成 ----------

const launcherWF = "wf-launch"

// launcherFixture 在执行夹具上配好非交互续聊预设 p1，并让工作流 hook 报过对话编号 chat-9。
func launcherFixture(t *testing.T, run map[string]any) (*execFixture, string) {
	t.Helper()
	f := newExecFixture(t)
	f.saveLauncher(t, "p1", "续聊", run)
	now := time.Now()
	f.env.store.RecordHookSeen(launcherWF, now)
	f.env.store.RecordAgentEvent(context.Background(), launcherWF, store.AgentEvent{Kind: store.AgentEventTool, ConversationID: "chat-9"}, now)
	return f, "p1"
}

func (f *execFixture) saveLauncher(t *testing.T, id, name string, run map[string]any) apiResult {
	t.Helper()
	res := f.env.putLauncher("/api/v1/terminal/launchers/"+id, map[string]any{
		"name": name, "host": f.a.code, "dir": "/work/app", "kind": "headless", "run": run,
	}, f.adm...)
	if res.Status != 200 {
		t.Fatalf("保存预设失败: %d %s", res.Status, res.Raw)
	}
	return res
}

func (f *execFixture) runLauncher(id string) (LauncherRunPlan, error) {
	plan, _, err := f.env.hub.guardian.runLauncher(context.Background(), id, time.Now())
	return plan, err
}

func (f *execFixture) waitRunFinal(t *testing.T, runID string) *model.TerminalRun {
	t.Helper()
	var rec *model.TerminalRun
	eventually(t, 3*time.Second, "运行进入终态", func() bool {
		rec = f.dbRun(t, runID)
		return model.IsFinalRunState(rec.State)
	})
	return rec
}

func (f *execFixture) chatTexts(wf string) []string {
	evs, _ := f.env.store.StreamEvents(context.Background(), "wf:"+wf, store.StreamRange{Dir: store.StreamOlder, Limit: 20})
	var out []string
	for i := len(evs) - 1; i >= 0; i-- { // 旧的在前
		out = append(out, evs[i].Text)
	}
	return out
}

const launcherResultOut = "{\"type\":\"system\",\"subtype\":\"init\"}\n" +
	"{\"type\":\"result\",\"subtype\":\"success\",\"is_error\":false,\"result\":\"已恢复现场：收件箱为空。\",\"session_id\":\"chat-9\"}\n"

func TestLauncherRunSubmitsExactCommandAndLeavesChatLines(t *testing.T) {
	f, id := launcherFixture(t, map[string]any{"workflow_id": launcherWF, "model": "claude-sonnet-5-5-max", "posture": "force"})
	f.completeWith(launcherResultOut)

	plan, err := f.runLauncher(id)
	if err != nil {
		t.Fatal(err)
	}
	if plan.RunID == "" || plan.Name != "续聊" || plan.Posture != store.HeadlessPostureForce || plan.Model != "claude-sonnet-5-5-max" || plan.ConversationID != "chat-9" {
		t.Fatalf("计划不对: %+v", plan)
	}
	if len(plan.Warnings) != 0 {
		t.Fatalf("force 姿态没有白名单提示: %v", plan.Warnings)
	}

	eventually(t, 2*time.Second, "假主机收到运行", func() bool { return f.sim.runCount() == 1 })
	run := f.sim.lastRun()
	want := "agent -p --resume chat-9 --approve-mcps --trust --model 'claude-sonnet-5-5-max' --force --workspace '/work/app' --output-format stream-json '" +
		renderLauncherWake(context.Background(), f.env.store, launcherWake{
			Kind: sessionprompt.KindResume, WorkflowID: launcherWF, Project: "/work/app", MaxRunSec: headlessTimeoutSec,
		}).Text + "'"
	if run.Cmd != want {
		t.Fatalf("命令不对：\n got %s\nwant %s", run.Cmd, want)
	}
	if run.Cwd != "/work/app" {
		t.Fatalf("应在预设目录里运行: %q", run.Cwd)
	}
	// 预设没设最长运行时间：默认平台上限 7 天，下发给主机的超时与默认提示词里的提醒一致。
	if plan.MaxRunSeconds != headlessTimeoutSec || run.TimeoutSec != headlessTimeoutSec || !strings.Contains(run.Cmd, "本次运行最长 7 天") {
		t.Fatalf("没设最长运行时间时应是 7 天：plan=%d host=%d", plan.MaxRunSeconds, run.TimeoutSec)
	}

	rec := f.waitRunFinal(t, plan.RunID)
	if rec.CredentialName != launcherRunCaller || rec.Thread != launcherThreadPrefix+id || rec.WorkflowID != launcherWF ||
		!strings.Contains(rec.Description, "预设「续聊」") || rec.State != model.RunSucceeded {
		t.Fatalf("运行记录的归属不对: %+v", rec)
	}

	// 聊天里先后有「发起」与「结束」两条系统行：不含命令与提示词；没有智能体消息，结束行带空转提示。
	var texts []string
	eventually(t, 3*time.Second, "聊天里出现结束行", func() bool {
		texts = f.chatTexts(launcherWF)
		return len(texts) == 2
	})
	if !strings.Contains(texts[0], "你手动运行了预设「续聊」（非交互续聊 · 接着对话 chat-9 · claude-sonnet-5-5-max · 放开全部命令（--force））") {
		t.Fatalf("发起行不对: %q", texts[0])
	}
	if strings.Contains(texts[0], "最长运行") {
		t.Fatalf("没设时限的预设，发起行不该写「最长运行」（默认的 7 天不是用户的选择）: %q", texts[0])
	}
	if !strings.Contains(texts[1], "预设「续聊」的非交互续聊已结束：成功") || !strings.Contains(texts[1], "空转退出") {
		t.Fatalf("结束行不对: %q", texts[1])
	}
	for _, s := range texts {
		if strings.Contains(s, "agent -p") || strings.Contains(s, "没有人在终端里看你的输出") || strings.Contains(s, "--force") && !strings.Contains(s, "放开全部命令") {
			t.Fatalf("系统行里不该有命令或提示词: %q", s)
		}
	}
}

// 预设里设了「最长运行时间」（#141）：这个值原样成为这次运行的超时（下发给主机、记在运行记录里），
// 默认提示词提醒智能体这个时限，聊天里的发起行写明它；到点被主机结束时，结束行与运行现状都写「达到运行时长上限」。
func TestLauncherRunHonoursThePresetsMaxRunTime(t *testing.T) {
	f, id := launcherFixture(t, map[string]any{"workflow_id": launcherWF, "max_run_minutes": 30})
	f.sim.onRun = func(s *hostSim, m termproto.Run) {
		s.started(m.ID)
		s.end(m.ID, termproto.RunTimeout, nil, "", "")
	}
	plan, err := f.runLauncher(id)
	if err != nil {
		t.Fatal(err)
	}
	if plan.MaxRunSeconds != 1800 {
		t.Fatalf("计划里的最长运行时间 = %d 秒，应是 1800", plan.MaxRunSeconds)
	}
	eventually(t, 2*time.Second, "假主机收到运行", func() bool { return f.sim.runCount() == 1 })
	run := f.sim.lastRun()
	if run.TimeoutSec != 1800 {
		t.Fatalf("下发给主机的超时 = %d 秒，应是 1800", run.TimeoutSec)
	}
	if !strings.HasSuffix(run.Cmd, "本次运行最长 30 分钟，到点会被强制结束：请在那之前先向 RelayMesh 汇报进度，没做完的写清下一步。'") {
		t.Fatalf("默认提示词后面应补上 30 分钟的时限提醒: %s", run.Cmd)
	}

	rec := f.waitRunFinal(t, plan.RunID)
	if rec.State != model.RunTimedOut || rec.TimeoutSec != 1800 {
		t.Fatalf("运行记录应是超时终态并带着它自己的超时: %+v", rec)
	}
	var texts []string
	eventually(t, 3*time.Second, "聊天里出现结束行", func() bool {
		texts = f.chatTexts(launcherWF)
		return len(texts) == 2
	})
	if !strings.Contains(texts[0], "最长运行 30 分钟") {
		t.Fatalf("发起行应写明设了的时限: %q", texts[0])
	}
	if !strings.Contains(texts[1], "已结束：达到运行时长上限（30 分钟），已被终止") || strings.Contains(texts[1], "超时被终止") {
		t.Fatalf("结束行应写「达到运行时长上限」与上限: %q", texts[1])
	}
	views, _ := f.env.hub.guardian.launcherRunViews(context.Background(), time.Now())
	if v := views[id]; v.State != model.RunTimedOut || v.TimeoutSec != 1800 || v.Active {
		t.Fatalf("现状应带这次运行的超时并已不在进行: %+v", v)
	}
}

// 自定义提示词不被加任何东西，但超时照样按预设的最长运行时间下发。
func TestLauncherRunCustomPromptIsUntouchedButStillGetsTheLimit(t *testing.T) {
	f, id := launcherFixture(t, map[string]any{"workflow_id": launcherWF, "prompt": "请继续 %workflow 的工作", "max_run_minutes": 90})
	f.completeWith("")
	plan, err := f.runLauncher(id)
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, 2*time.Second, "假主机收到运行", func() bool { return f.sim.runCount() == 1 })
	run := f.sim.lastRun()
	if !strings.HasSuffix(run.Cmd, "'请继续 "+launcherWF+" 的工作'") || strings.Contains(run.Cmd, "本次运行最长") {
		t.Fatalf("自定义提示词不该被加时限提醒: %s", run.Cmd)
	}
	if run.TimeoutSec != 5400 || plan.MaxRunSeconds != 5400 {
		t.Fatalf("超时应按预设的 90 分钟下发: host=%d plan=%d", run.TimeoutSec, plan.MaxRunSeconds)
	}
	f.waitRunFinal(t, plan.RunID)
}

// 预设运行的记录残留（运行已经不在主机上、终态事件也没来）：Agent 重连后的对账把它收尾成「状态丢失」，
// 预设随即可以再运行——兜底阈值（超时 + 宽限）不是唯一的出路（#141 第二步：重连对账）。
func TestLauncherRunLeftoverRecordIsReconciledWhenTheAgentReconnects(t *testing.T) {
	f := newExecFixture(t, withFakeClock())
	f.saveLauncher(t, "p1", "续聊", map[string]any{"workflow_id": launcherWF, "max_run_minutes": 30})
	now := time.Now()
	f.env.store.RecordHookSeen(launcherWF, now)
	f.env.store.RecordAgentEvent(context.Background(), launcherWF, store.AgentEvent{Kind: store.AgentEventTool, ConversationID: "chat-9"}, now)
	f.sim.onRun = func(s *hostSim, m termproto.Run) { s.started(m.ID) } // 开始了，但一直不结束

	first, err := f.runLauncher("p1")
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, 2*time.Second, "主机确认运行已开始", func() bool {
		lr := f.svc.liveRun(first.RunID)
		return lr != nil && lr.snapshot().StartedAt != nil
	})
	if _, err := f.runLauncher("p1"); err == nil {
		t.Fatal("运行还在进行时不该能再运行一次")
	} else {
		wantWakeCode(t, err, "run_active")
	}

	// 主机（Agent）重连，清单里已经没有这次运行：宽限期内先不判，过了宽限仍没有结束事件就收尾成 lost。
	f.sim.a.send(termproto.Runs{T: termproto.TRuns})
	eventually(t, 2*time.Second, "对账登记宽限", func() bool {
		f.svc.mu.Lock()
		defer f.svc.mu.Unlock()
		_, ok := f.svc.lostWatch[first.RunID]
		return ok
	})
	f.svc.maintain(f.env.clock.Now())
	if rec := f.dbRun(t, first.RunID); rec.State != model.RunRunning {
		t.Fatalf("宽限期内不该判丢失: %s", rec.State)
	}
	f.env.clock.Advance(execLostGrace + time.Second)
	f.svc.maintain(f.env.clock.Now())
	if rec := f.dbRun(t, first.RunID); rec.State != model.RunLost || rec.DenyReason != "not_on_host" {
		t.Fatalf("对账后应收尾为 lost / not_on_host: %+v", rec)
	}
	views, _ := f.env.hub.guardian.launcherRunViews(context.Background(), time.Now())
	if v := views["p1"]; v.State != model.RunLost || v.Active || v.TimeoutSec != 1800 {
		t.Fatalf("对账后现状应是已丢失、不在进行: %+v", v)
	}
	second, err := f.runLauncher("p1")
	if err != nil {
		t.Fatalf("对账收尾之后应能再运行: %v", err)
	}
	if second.RunID == first.RunID {
		t.Fatal("应是一次新的运行")
	}
}

func TestLauncherRunViewShowsReplyFailureAndSilence(t *testing.T) {
	f, id := launcherFixture(t, map[string]any{"workflow_id": launcherWF})
	ctx := context.Background()
	hub := f.env.hub

	if views, err := hub.guardian.launcherRunViews(ctx, time.Now()); err != nil || len(views) != 0 {
		t.Fatalf("从没运行过的预设没有条目: %+v %v", views, err)
	}

	f.completeWith(launcherResultOut)
	plan, err := f.runLauncher(id)
	if err != nil {
		t.Fatal(err)
	}
	f.waitRunFinal(t, plan.RunID)
	views, _ := hub.guardian.launcherRunViews(ctx, time.Now())
	v, ok := views[id]
	if !ok || v.RunID != plan.RunID || v.State != model.RunSucceeded || v.Active || v.Reply != "已恢复现场：收件箱为空。" || !v.Silent {
		t.Fatalf("成功且空转的现状不对: %+v", v)
	}
	if v.ExitCode == nil || *v.ExitCode != 0 {
		t.Fatalf("应带退出码 0: %+v", v)
	}
	if rec := f.dbRun(t, plan.RunID); v.ThreadKey == "" || v.ThreadKey != rec.ThreadKey {
		t.Fatalf("应带 ExecMesh 线程键（界面据此链到完整输出）: %+v", v)
	}

	// 运行结束之后（超过宽限）别的会话发的消息不算这次运行说过话：把这次运行的时间窗挪到一分钟前，再发一条消息。
	ended := time.Now().Add(-time.Minute)
	started := ended.Add(-30 * time.Second)
	if ok, err := f.env.store.UpdateTerminalRun(ctx, plan.RunID, map[string]any{"started_at": started, "ended_at": ended}); err != nil || !ok {
		t.Fatalf("改运行记录的时间窗: %v %v", ok, err)
	}
	if _, err := f.env.store.CreateNoticeSession(ctx, store.CreateSessionInput{WorkflowID: launcherWF, ProjectDirectory: "/work/app", Summary: "事后别的会话说的话"}); err != nil {
		t.Fatal(err)
	}
	views, _ = hub.guardian.launcherRunViews(ctx, time.Now())
	if !views[id].Silent {
		t.Fatalf("运行结束之后才发的消息不该让这次运行显得「说过话」: %+v", views[id])
	}
	// 窗口里的消息才算：把时间窗挪回去盖住这条消息。
	if ok, err := f.env.store.UpdateTerminalRun(ctx, plan.RunID, map[string]any{"ended_at": time.Now().Add(time.Second)}); err != nil || !ok {
		t.Fatalf("改运行记录的时间窗: %v %v", ok, err)
	}
	views, _ = hub.guardian.launcherRunViews(ctx, time.Now())
	if views[id].Silent {
		t.Fatalf("运行期间发过消息，不该算空转: %+v", views[id])
	}

	// 失败的运行：状态 failed、退出码 2、回复退回输出里的纯文本报错。
	f.sim.onRun = func(s *hostSim, m termproto.Run) {
		s.started(m.ID)
		s.end(m.ID, termproto.RunExited, intp(2), "", "Error: chat not found\n")
	}
	plan2, err := f.runLauncher(id)
	if err != nil {
		t.Fatal(err)
	}
	f.waitRunFinal(t, plan2.RunID)
	views, _ = hub.guardian.launcherRunViews(ctx, time.Now())
	v = views[id]
	if v.RunID != plan2.RunID || v.State != model.RunFailed || v.ExitCode == nil || *v.ExitCode != 2 || v.Reply != "Error: chat not found" || v.Silent {
		t.Fatalf("失败的现状不对: %+v", v)
	}
	eventually(t, 3*time.Second, "失败的结束行", func() bool {
		for _, s := range f.chatTexts(launcherWF) {
			if strings.Contains(s, "预设「续聊」的非交互续聊已结束：失败") && strings.Contains(s, "退出码 2") && !strings.Contains(s, "空转") {
				return true
			}
		}
		return false
	})
}

func TestLauncherRunRefusals(t *testing.T) {
	t.Run("预设不存在", func(t *testing.T) {
		f, _ := launcherFixture(t, map[string]any{"workflow_id": launcherWF})
		_, err := f.runLauncher("nope")
		wantWakeCode(t, err, "launcher_not_found")
	})
	t.Run("终端预设不能运行", func(t *testing.T) {
		f, _ := launcherFixture(t, map[string]any{"workflow_id": launcherWF})
		res := f.env.putLauncher("/api/v1/terminal/launchers/t1", map[string]any{"name": "终端", "host": f.a.code, "dir": "/work/app", "command": "agent"}, f.adm...)
		if res.Status != 200 {
			t.Fatalf("保存终端预设: %d %s", res.Status, res.Raw)
		}
		_, err := f.runLauncher("t1")
		wantWakeCode(t, err, "not_headless")
		if f.sim.runCount() != 0 {
			t.Fatal("不该提交运行")
		}
	})
	t.Run("不知道要接着哪个对话", func(t *testing.T) {
		f := newExecFixture(t)
		f.saveLauncher(t, "p1", "续聊", map[string]any{"workflow_id": "wf-nochat"})
		_, err := f.runLauncher("p1")
		wantWakeCode(t, err, "no_conversation")
		if f.sim.runCount() != 0 {
			t.Fatal("不该提交运行")
		}
	})
	t.Run("对话还在运行：工作中", func(t *testing.T) {
		f, id := launcherFixture(t, map[string]any{"workflow_id": launcherWF})
		_, err := f.env.store.IngestAgentEvent(context.Background(), store.AgentIngestInput{
			WorkflowID: launcherWF, CredentialID: 1, HostName: "h", ProjectDirectory: "/work/app", ConversationID: "chat-9", ClientKind: "cli",
			Event: agentstate.Event{Source: agentstate.SourceHook, Name: agentstate.EvPrompt, Received: time.Now()},
		})
		if err != nil {
			t.Fatal(err)
		}
		_, err = f.runLauncher(id)
		wantWakeCode(t, err, "conversation_live")
		if f.sim.runCount() != 0 {
			t.Fatal("对话还在运行，不该提交运行")
		}
	})
	ingest := func(t *testing.T, f *execFixture, name string, at time.Time) {
		t.Helper()
		_, err := f.env.store.IngestAgentEvent(context.Background(), store.AgentIngestInput{
			WorkflowID: launcherWF, CredentialID: 1, HostName: "h", ProjectDirectory: "/work/app", ConversationID: "chat-9", ClientKind: "cli",
			Event: agentstate.Event{Source: agentstate.SourceHook, Name: name, Received: at, Status: "completed"},
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	t.Run("空闲的对话：放行并提示", func(t *testing.T) {
		f, id := launcherFixture(t, map[string]any{"workflow_id": launcherWF, "posture": "force"})
		f.completeWith("")
		now := time.Now()
		ingest(t, f, agentstate.EvPrompt, now.Add(-2*time.Second))
		ingest(t, f, agentstate.EvStop, now.Add(-time.Second))
		plan, err := f.runLauncher(id)
		if err != nil {
			t.Fatalf("空闲的对话应放行: %v", err)
		}
		if len(plan.Warnings) != 1 || !strings.Contains(plan.Warnings[0], "空闲") {
			t.Fatalf("应提示对话是空闲状态: %v", plan.Warnings)
		}
	})
	t.Run("很久没有动静的对话（卡住或离线）：放行", func(t *testing.T) {
		f, id := launcherFixture(t, map[string]any{"workflow_id": launcherWF, "posture": "force"})
		f.completeWith("")
		ingest(t, f, agentstate.EvPrompt, time.Now())
		// 三个小时之后：原来「工作中」的对话要么已经被判成卡住、要么已经离线——这正是需要续聊它的时候。
		if _, _, err := f.env.hub.guardian.runLauncher(context.Background(), id, time.Now().Add(3*time.Hour)); err != nil {
			t.Fatalf("卡住 / 离线的对话应放行: %v", err)
		}
	})
	t.Run("执行闸门拒绝：主机没开模型访问", func(t *testing.T) {
		f, id := launcherFixture(t, map[string]any{"workflow_id": launcherWF})
		f.setModel(t, map[string]any{"allow_model": false})
		_, err := f.runLauncher(id)
		if _, ok := err.(*wakeError); !ok {
			t.Fatalf("应转换成面向界面的错误: %T %v", err, err)
		}
		if f.sim.runCount() != 0 {
			t.Fatal("不该有运行到达主机")
		}
		if views, _ := f.env.hub.guardian.launcherRunViews(context.Background(), time.Now()); len(views) != 0 {
			t.Fatalf("被闸门拒绝的运行不属于预设的「最近一次」（拒绝由审计记录）：%+v", views)
		}
	})
	t.Run("需批准模式：运行停在待批准并提示", func(t *testing.T) {
		f, id := launcherFixture(t, map[string]any{"workflow_id": launcherWF})
		f.setModel(t, map[string]any{"allow_model": true, "exec_mode": "approve"})
		plan, err := f.runLauncher(id)
		if err != nil {
			t.Fatal(err)
		}
		if plan.RunState != model.RunAwaitingApproval || f.sim.runCount() != 0 {
			t.Fatalf("应停在待批准且不到达主机: %+v runs=%d", plan, f.sim.runCount())
		}
		joined := strings.Join(plan.Warnings, "|")
		if !strings.Contains(joined, "需批准") || !strings.Contains(joined, "Mcp(") {
			t.Fatalf("应提示要去批准、以及 allowlist 的前提: %v", plan.Warnings)
		}
		views, _ := f.env.hub.guardian.launcherRunViews(context.Background(), time.Now())
		if v := views[id]; v.State != model.RunAwaitingApproval || !v.Active || v.Reply != "" {
			t.Fatalf("待批准的现状不对: %+v", v)
		}
	})
}

func TestLauncherRunIsSingleFlightPerPreset(t *testing.T) {
	f, id := launcherFixture(t, map[string]any{"workflow_id": launcherWF})
	// 没有 completeWith：假主机收到运行后不结束它，运行一直停在 running。
	first, err := f.runLauncher(id)
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, 2*time.Second, "假主机收到运行", func() bool { return f.sim.runCount() == 1 })

	_, err = f.runLauncher(id)
	wantWakeCode(t, err, "run_active")
	if !strings.Contains(err.Error(), first.RunID) {
		t.Fatalf("错误信息应带上还在跑的运行编号: %v", err)
	}
	if f.sim.runCount() != 1 {
		t.Fatalf("第二次不该到达主机: %d", f.sim.runCount())
	}
	views, _ := f.env.hub.guardian.launcherRunViews(context.Background(), time.Now())
	if v := views[id]; !v.Active || v.State != model.RunRunning || v.Reply != "" || v.Silent {
		t.Fatalf("进行中的现状不对: %+v", v)
	}

	// 记录残留超过宽限（异常残留）不再挡路。
	old := time.Now().Add(-launcherRunStale - time.Minute)
	if ok, err := f.env.store.UpdateTerminalRun(context.Background(), first.RunID, map[string]any{"created_at": old}); err != nil || !ok {
		t.Fatalf("改旧运行记录: %v %v", ok, err)
	}
	f.completeWith(launcherResultOut)
	if _, err := f.runLauncher(id); err != nil {
		t.Fatalf("残留的旧记录不该挡住新的运行: %v", err)
	}
}

// 这个预设自己的运行还在跑时，运行输出会让对话显示成「工作中」：拒绝的原因要说「上一次运行还没结束」（更具体），
// 而不是「对话还在运行」；预览里两条都列出来，run_active 排在前面。
func TestLauncherRunActiveOutranksLiveConversation(t *testing.T) {
	f, id := launcherFixture(t, map[string]any{"workflow_id": launcherWF})
	first, err := f.runLauncher(id) // 没有 completeWith：假主机收到运行后一直不结束
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, 2*time.Second, "假主机收到运行", func() bool { return f.sim.runCount() == 1 })
	if _, err := f.env.store.IngestAgentEvent(context.Background(), store.AgentIngestInput{
		WorkflowID: launcherWF, CredentialID: 1, HostName: "h", ProjectDirectory: "/work/app", ConversationID: "chat-9", ClientKind: "cli",
		Event: agentstate.Event{Source: agentstate.SourceRun, Name: agentstate.EvPrompt, Received: time.Now()},
	}); err != nil {
		t.Fatal(err)
	}

	_, err = f.runLauncher(id)
	wantWakeCode(t, err, "run_active")
	if !strings.Contains(err.Error(), first.RunID) {
		t.Fatalf("错误信息应带上还在跑的运行编号: %v", err)
	}
	launchers, _ := f.env.store.ListTerminalLaunchers(context.Background())
	in := f.env.hub.guardian.inspectLauncher(context.Background(), launchers[0], time.Now())
	var codes []string
	for _, e := range in.errs {
		codes = append(codes, e.Code)
	}
	if strings.Join(codes, ",") != "run_active,conversation_live" {
		t.Fatalf("预览里两条原因都要有，且 run_active 在前: %v", codes)
	}
	if f.sim.runCount() != 1 {
		t.Fatalf("被拒绝的请求不该到达主机: %d", f.sim.runCount())
	}
}

func TestLauncherRunPinnedConversationWorksWithoutWorkflow(t *testing.T) {
	f := newExecFixture(t)
	f.saveLauncher(t, "p1", "固定对话", map[string]any{"conversation_id": "e33e27ef-0fbb-46e4-9446-5dee56ae61fa", "prompt": "请继续 %workflow 的工作"})
	f.completeWith("")

	plan, err := f.runLauncher("p1")
	if err != nil {
		t.Fatal(err)
	}
	if plan.ConversationID != "e33e27ef-0fbb-46e4-9446-5dee56ae61fa" || len(plan.Warnings) == 0 || !strings.Contains(plan.Warnings[0], "Mcp(") {
		t.Fatalf("计划不对（allowlist 默认姿态要有白名单提示）: %+v", plan)
	}
	eventually(t, 2*time.Second, "假主机收到运行", func() bool { return f.sim.runCount() == 1 })
	want := "agent -p --resume e33e27ef-0fbb-46e4-9446-5dee56ae61fa --approve-mcps --trust --workspace '/work/app' --output-format stream-json '请继续  的工作'"
	if got := f.sim.lastRun().Cmd; got != want {
		t.Fatalf("命令不对：\n got %s\nwant %s", got, want)
	}
	rec := f.waitRunFinal(t, plan.RunID)
	if rec.WorkflowID != "" {
		t.Fatalf("没配工作流时运行记录不带工作流: %+v", rec)
	}
}

func TestLauncherRunRESTNeedsUnlockAndAuditsWithoutPromptOrCommand(t *testing.T) {
	f, id := launcherFixture(t, map[string]any{"workflow_id": launcherWF, "model": "m1"})
	f.completeWith(launcherResultOut)
	path := "/api/v1/terminal/launchers/" + id + "/run"

	if res := f.env.call("POST", path, nil); res.Status != 401 {
		t.Fatalf("没登录应 401: %d %s", res.Status, res.Raw)
	}
	if res := f.env.call("POST", path, nil, "Authorization", humanAuth(testUser)); res.Status == 200 {
		t.Fatalf("只登录没解锁不该能运行（运行 = 让主机上的 agent 动手）: %d %s", res.Status, res.Raw)
	}
	if f.sim.runCount() != 0 {
		t.Fatal("被拒绝的请求不该到达主机")
	}

	res := f.env.call("POST", path, nil, f.adm...)
	if res.Status != 200 {
		t.Fatalf("运行失败: %d %s", res.Status, res.Raw)
	}
	run, _ := res.Body["run"].(map[string]any)
	if run["run_id"] == "" || run["conversation_id"] != "chat-9" || run["posture"] != "allowlist" || run["model"] != "m1" {
		t.Fatalf("响应不对: %s", res.Raw)
	}
	if bad := f.env.call("POST", "/api/v1/terminal/launchers/nope/run", nil, f.adm...); bad.Status != 404 || bad.Body["code"] != "launcher_not_found" {
		t.Fatalf("不存在的预设应 404: %d %s", bad.Status, bad.Raw)
	}

	logs := f.env.auditEvents(EvLauncherRun)
	if len(logs) != 1 || logs[0].RunID != run["run_id"] || !strings.Contains(logs[0].Detail, "preset=p1") || !strings.Contains(logs[0].Detail, "posture=allowlist") {
		t.Fatalf("审计应记一条 launcher_run（预设编号、姿态、运行编号）: %+v", logs)
	}
	for _, l := range logs {
		if strings.Contains(l.Detail, "agent") || strings.Contains(l.Detail, "没有人在终端") || strings.Contains(l.Detail, "chat-9") {
			t.Fatalf("审计不该含命令、提示词或对话编号: %q", l.Detail)
		}
	}

	// GET /launchers/runs：登录即可读（不需要解锁），带最近一次运行的现状。
	f.waitRunFinal(t, run["run_id"].(string))
	got := f.env.call("GET", "/api/v1/terminal/launchers/runs", nil, "Authorization", humanAuth(testUser))
	runs, _ := got.Body["runs"].(map[string]any)
	cur, _ := runs[id].(map[string]any)
	if got.Status != 200 || cur["state"] != model.RunSucceeded || cur["reply"] != "已恢复现场：收件箱为空。" || cur["silent"] != true {
		t.Fatalf("现状接口不对: %d %s", got.Status, got.Raw)
	}
}

func TestLauncherSaveViaRESTValidatesKindAndRoundTrips(t *testing.T) {
	f := newExecFixture(t)
	save := func(body map[string]any) apiResult {
		body["name"], body["host"] = "续聊", f.a.code
		return f.env.putLauncher("/api/v1/terminal/launchers/p1", body, f.adm...)
	}
	for name, body := range map[string]map[string]any{
		"不认识的类型":     {"kind": "cron", "dir": "/w"},
		"缺运行配置":      {"kind": "headless", "dir": "/w"},
		"目录不是绝对":     {"kind": "headless", "dir": "w", "run": map[string]any{"workflow_id": "wf"}},
		"前缀命令":       {"kind": "headless", "dir": "/w", "run": map[string]any{"workflow_id": "wf", "bin": "sudo"}},
		"终端预设带了运行配置": {"dir": "/w", "run": map[string]any{"workflow_id": "wf"}},
		"最长运行时间是负数":  {"kind": "headless", "dir": "/w", "run": map[string]any{"workflow_id": "wf", "max_run_minutes": -1}},
	} {
		if res := save(body); res.Status != 400 || res.Body["code"] != "bad_launcher" {
			t.Errorf("%s：应 400 bad_launcher，得到 %d %s", name, res.Status, res.Raw)
		}
	}
	res := save(map[string]any{"kind": "headless", "dir": "/w", "run": map[string]any{"workflow_id": "wf", "model": "m1", "posture": "force", "prompt": "继续   %workflow"}})
	if res.Status != 200 {
		t.Fatalf("合法的续聊预设应保存成功: %d %s", res.Status, res.Raw)
	}
	prefs, _ := res.Body["prefs"].(map[string]any)
	list, _ := prefs["launchers"].([]any)
	if len(list) != 1 {
		t.Fatalf("prefs 应带 1 条预设: %s", res.Raw)
	}
	l, _ := list[0].(map[string]any)
	run, _ := l["run"].(map[string]any)
	if l["kind"] != "headless" || run["model"] != "m1" || run["posture"] != "force" || run["prompt"] != "继续 %workflow" {
		t.Fatalf("类型与运行配置应原样（规整后）回到界面: %s", res.Raw)
	}
	if _, has := run["max_run_minutes"]; has {
		t.Fatalf("没设最长运行时间的预设不该带这个字段（旧存档与界面都按「没设」读）: %s", res.Raw)
	}

	// 最长运行时间（#141）：设了的原样回来；超过平台上限（7 天）的收进上限。
	for in, want := range map[int]float64{45: 45, 1: 1, store.MaxHeadlessRunMinutes: store.MaxHeadlessRunMinutes, store.MaxHeadlessRunMinutes + 500: store.MaxHeadlessRunMinutes} {
		res := save(map[string]any{"kind": "headless", "dir": "/w", "run": map[string]any{"workflow_id": "wf", "max_run_minutes": in}})
		if res.Status != 200 {
			t.Fatalf("max_run_minutes=%d 应保存成功: %d %s", in, res.Status, res.Raw)
		}
		prefs, _ := res.Body["prefs"].(map[string]any)
		list, _ := prefs["launchers"].([]any)
		l, _ := list[0].(map[string]any)
		run, _ := l["run"].(map[string]any)
		if run["max_run_minutes"] != want {
			t.Fatalf("max_run_minutes=%d 回到界面应是 %v: %s", in, want, res.Raw)
		}
	}
}
