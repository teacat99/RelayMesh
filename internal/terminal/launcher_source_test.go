package terminal

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/teacat99/RelayMesh/v2/internal/agentstate"
	"github.com/teacat99/RelayMesh/v2/internal/model"
	"github.com/teacat99/RelayMesh/v2/internal/store"
	"github.com/teacat99/RelayMesh/v2/internal/termproto"
)

// 预设的对话来源三选一（#191）：新会话（成功后去向：记下编号 / 不切换 / 切到最近会话）、最近会话（--continue）、指定会话。
// 存量预设（只有旧字段）的行为由 launcher_newconv_test.go 与 launcher_run_test.go 守着，这里只测新增的语义。

// otherConvOut 是「最近会话」那次运行的输出：CLI 接上的是另一个对话（编辑器里开的那个），不是这个工作流学到的 chat-new-1。
const otherConvOut = "{\"type\":\"system\",\"subtype\":\"init\",\"session_id\":\"chat-other-9\"}\n" +
	"{\"type\":\"result\",\"subtype\":\"success\",\"is_error\":false,\"result\":\"接着了最近的对话\",\"session_id\":\"chat-other-9\"}\n"

// sourceFixture 配好一条预设 p1：工作流 wf-newconv，hook 没汇报过任何对话；run 里写对话来源（旧的 new_conversation 勾选不带）。
func sourceFixture(t *testing.T, run map[string]any) (*execFixture, string) {
	t.Helper()
	f, id := newConvFixture(t, run)
	return f, id
}

// presetRun 读出预设 p1 现在存的运行配置。
func (f *execFixture) presetRun(t *testing.T, id string) store.HeadlessRun {
	t.Helper()
	list, err := f.env.store.ListTerminalLaunchers(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range list {
		if l.ID == id && l.Run != nil {
			return *l.Run
		}
	}
	t.Fatalf("找不到预设 %s: %+v", id, list)
	return store.HeadlessRun{}
}

func TestHeadlessCmdContinueReplacesResume(t *testing.T) {
	got, err := headlessCmd{Bin: "agent", Continue: true, Dir: "/work/app", Posture: store.HeadlessPostureForce, Model: "m-1", Prompt: "接着干"}.build()
	if err != nil {
		t.Fatal(err)
	}
	want := "agent -p --continue --approve-mcps --trust --model 'm-1' --force --workspace '/work/app' --output-format stream-json '接着干'"
	if got != want {
		t.Fatalf("最近会话的命令不对：\n got %s\nwant %s", got, want)
	}
	if strings.Contains(got, "--resume") {
		t.Fatalf("最近会话不能带 --resume: %s", got)
	}
	// 与另外两种形态互斥：一次只能选一种接对话的方式。
	if _, err := (headlessCmd{Bin: "agent", Continue: true, ChatID: "chat-9", Dir: "/w", Prompt: "x"}).build(); err == nil {
		t.Fatal("最近会话同时给了对话编号，应当拒绝")
	}
	if _, err := (headlessCmd{Bin: "agent", Continue: true, New: true, Dir: "/w", Prompt: "x"}).build(); err == nil {
		t.Fatal("新开与最近会话同时选，应当拒绝")
	}
	// 另外两种形态不变。
	resume, err := headlessCmd{Bin: "agent", ChatID: "chat-9", Dir: "/work/app", Prompt: "x"}.build()
	if err != nil || !strings.HasPrefix(resume, "agent -p --resume chat-9 --approve-mcps --trust ") {
		t.Fatalf("续聊命令变了: %q %v", resume, err)
	}
	fresh, err := headlessCmd{Bin: "agent", New: true, Dir: "/work/app", Prompt: "x"}.build()
	if err != nil || strings.Contains(fresh, "--resume") || strings.Contains(fresh, "--continue") {
		t.Fatalf("新开命令变了: %q %v", fresh, err)
	}
}

func TestRunModeAndConversationHelpers(t *testing.T) {
	cont := "agent -p --continue --approve-mcps --trust --workspace '/w' --output-format stream-json 'x'"
	resume := "agent -p --resume chat-9 --approve-mcps --trust --workspace '/w' --output-format stream-json 'x'"
	fresh := "agent -p --approve-mcps --trust --workspace '/w' --output-format stream-json 'x'"
	for cmd, want := range map[string]string{cont: store.HeadlessSourceContinue, resume: store.HeadlessSourceResume, fresh: store.HeadlessSourceNew} {
		if got := runModeOf(cmd); got != want {
			t.Errorf("runModeOf(%q) = %q，想要 %q", cmd, got, want)
		}
	}
	// 提示词（单引号包着的一个词）里出现 --continue 字样不算：它前面不是空白、后面也不是。
	tricky := "agent -p --approve-mcps --trust --workspace '/w' --output-format stream-json 'please--continue-later'"
	if isContinueCommand(tricky) {
		t.Errorf("提示词里的字样不该被当成 --continue 参数: %q", tricky)
	}

	head := "{\"type\":\"system\",\"subtype\":\"init\",\"session_id\":\"chat-from-init\",\"cwd\":\"/w\"}\n{\"type\":\"assistant\"}\n"
	if got := convFromRunOutput(head); got != "chat-from-init" {
		t.Errorf("应从 init 事件取到对话编号: %q", got)
	}
	for _, none := range []string{"", "   ", "RetriableError: [unavailable] Error\n", "{\"type\":\"system\",\"subtype\":\"init\",\"session_id\":\"bad id with spaces\"}\n"} {
		if got := convFromRunOutput(none); got != "" {
			t.Errorf("认不出时应为空: %q → %q", none, got)
		}
	}
	// 命令里有 --resume 就是它；没有就看输出。
	if got := convOfRun(model.TerminalRun{Command: resume, PreviewHead: head}); got != "chat-9" {
		t.Errorf("命令里的编号优先: %q", got)
	}
	if got := convOfRun(model.TerminalRun{Command: fresh, PreviewHead: head}); got != "chat-from-init" {
		t.Errorf("新开的运行从输出取编号: %q", got)
	}
	if got := convOfRun(model.TerminalRun{Command: cont}); got != "" {
		t.Errorf("还没有输出就没有编号: %q", got)
	}
}

// 新会话 · 不切换：每一次运行都新开对话，哪怕工作流上已经记着上一次学到的对话；学到的编号照样记成工作流当前的对话。
func TestLauncherSourceNewStayOpensANewConversationEveryTime(t *testing.T) {
	f, id := sourceFixture(t, map[string]any{"source": "new", "after_new": "stay", "new_conversation": false})
	f.completeReadable(newConvOut)

	plan, err := f.runLauncher(id)
	if err != nil {
		t.Fatal(err)
	}
	if !plan.NewConversation || plan.Continue || plan.ConversationID != "" {
		t.Fatalf("第一次应新开: %+v", plan)
	}
	if !containsText(plan.Warnings, "新会话 · 不切换") {
		t.Fatalf("应提示每次都新开: %v", plan.Warnings)
	}
	eventually(t, 3*time.Second, "学到对话编号并记到工作流", func() bool {
		return f.env.store.AgentLinkOf(context.Background(), newConvWF).ConversationID == "chat-new-1"
	})
	var texts []string
	eventually(t, 3*time.Second, "聊天里有「每次都新开」的建立行", func() bool {
		texts = f.chatTexts(newConvWF)
		return containsText(texts, "新对话已建立")
	})
	if !containsText(texts, "这个预设每次运行都会新开对话，不会接着这一个") {
		t.Fatalf("建立行应说明每次都新开: %v", texts)
	}
	f.waitRunFinal(t, plan.RunID)

	// 第二次：工作流上已经有 chat-new-1，但这个预设不接它——仍然新开。
	plan2, err := f.runLauncher(id)
	if err != nil {
		t.Fatal(err)
	}
	if !plan2.NewConversation || plan2.ConversationID != "" {
		t.Fatalf("不切换的预设第二次也该新开: %+v", plan2)
	}
	eventually(t, 2*time.Second, "假主机收到第二次运行", func() bool { return f.sim.runCount() == 2 })
	if cmd := f.sim.lastRun().Cmd; strings.Contains(cmd, "--resume") || strings.Contains(cmd, "--continue") {
		t.Fatalf("第二次不该带 --resume / --continue: %s", cmd)
	}
	f.waitRunFinal(t, plan2.RunID)
	// 运行现状里能看到这次实际接上的对话（从输出的 init 事件取）。
	rv := f.env.hub.guardian.runViewOf(context.Background(), *f.dbRun(t, plan2.RunID), time.Now())
	if rv.Mode != store.HeadlessSourceNew || rv.Conversation != "chat-new-1" {
		t.Fatalf("运行现状应写明新会话与实际对话: %+v", rv)
	}
	if got := f.presetRun(t, id); got.EffectiveSource() != store.HeadlessSourceNew || got.EffectiveAfterNew() != store.HeadlessAfterStay {
		t.Fatalf("不切换的预设永远不被改写: %+v", got)
	}
}

// 新会话 · 成功后切到最近会话：第一次新开，成功后预设自动改成「最近会话」（系统行 + 审计）；之后的运行用 --continue，
// 监视只看一眼实际接上的对话、不把它记成工作流的对话。
func TestLauncherSourceNewContinueSwitchesToLatestAfterASuccessfulRun(t *testing.T) {
	f, id := sourceFixture(t, map[string]any{"source": "new", "after_new": "continue", "new_conversation": false})
	f.completeReadable(newConvOut)

	plan, err := f.runLauncher(id)
	if err != nil {
		t.Fatal(err)
	}
	if !plan.NewConversation || plan.Continue {
		t.Fatalf("第一次应新开: %+v", plan)
	}
	if !containsText(plan.Warnings, "成功后切到最近会话") {
		t.Fatalf("应提示运行成功后会切换: %v", plan.Warnings)
	}
	f.waitRunFinal(t, plan.RunID)

	eventually(t, 3*time.Second, "预设被切成最近会话", func() bool {
		return f.presetRun(t, id).EffectiveSource() == store.HeadlessSourceContinue
	})
	got := f.presetRun(t, id)
	if got.Source != store.HeadlessSourceContinue || got.AfterNew != "" || got.NewConversation {
		t.Fatalf("切换后的运行配置不对: %+v", got)
	}
	var texts []string
	// 两行要一起等：假主机瞬间跑完，监视从输出里学到对话编号写「建立行」与运行成功后写「切换行」几乎同时发生，
	// 宿主机负载高时建立行可能晚于切换行出现（#186 C1 / C2a 的整批 Go 测试里各失败过一次，单独跑 10/10 通过）。
	// 真实运行里对话编号在输出的头几行（init 事件）就到了，早于运行结束，顺序不是问题；这里断言的是「两行都有」而不是先后。
	eventually(t, 3*time.Second, "聊天里有新对话建立行与切换行", func() bool {
		texts = f.chatTexts(newConvWF)
		return containsText(texts, "已自动切换成「最近会话」") && containsText(texts, "这次运行成功之后，预设会自动切换成「最近会话」")
	})
	if !containsText(texts, "这次运行成功之后，预设会自动切换成「最近会话」") {
		t.Fatalf("建立行应预告切换: %v", texts)
	}
	if n := f.auditCount(t, EvLauncherSwitch); n != 1 {
		t.Fatalf("应有一条切换审计: %d", n)
	}
	for _, l := range f.audits(t) {
		if l.Event == EvLauncherSwitch && (l.RunID != plan.RunID || l.ActorKind != ActorSystem || l.Detail != "preset=p1 new->continue") {
			t.Fatalf("切换审计内容不对: %+v", l)
		}
	}

	// 之后的运行：--continue；CLI 这次接上了另一个对话，监视只写一条说明，不改工作流上记的对话。
	f.completeReadable(otherConvOut)
	plan2, err := f.runLauncher(id)
	if err != nil {
		t.Fatal(err)
	}
	if plan2.NewConversation || !plan2.Continue || plan2.ConversationID != "" {
		t.Fatalf("切换之后应是最近会话: %+v", plan2)
	}
	if !containsText(plan2.Warnings, "--continue") {
		t.Fatalf("应提示可能接错对话: %v", plan2.Warnings)
	}
	eventually(t, 2*time.Second, "假主机收到第二次运行", func() bool { return f.sim.runCount() == 2 })
	cmd := f.sim.lastRun().Cmd
	if !strings.HasPrefix(cmd, "agent -p --continue --approve-mcps --trust ") || strings.Contains(cmd, "--resume") {
		t.Fatalf("第二次应是 --continue 且没有 --resume: %s", cmd)
	}
	eventually(t, 3*time.Second, "聊天里有「接上了对话」的说明", func() bool {
		return containsText(f.chatTexts(newConvWF), "这次运行接上了对话 chat-oth")
	})
	f.waitRunFinal(t, plan2.RunID)
	if got := f.env.store.AgentLinkOf(context.Background(), newConvWF).ConversationID; got != "chat-new-1" {
		t.Fatalf("最近会话不该改写工作流的对话编号: %q", got)
	}
	rv := f.env.hub.guardian.runViewOf(context.Background(), *f.dbRun(t, plan2.RunID), time.Now())
	if rv.Mode != store.HeadlessSourceContinue || rv.Conversation != "chat-other-9" {
		t.Fatalf("运行现状应写明最近会话与实际接上的对话: %+v", rv)
	}
	// 已经是最近会话了：再成功不会重复切换。
	if n := f.auditCount(t, EvLauncherSwitch); n != 1 {
		t.Fatalf("不该重复切换: %d", n)
	}
}

// 切换的条件：运行失败不切；命令里带 --resume / --continue 的不是新开的运行（运行期间预设被改过）不切；
// 不是预设运行的记录不切；预设此刻不是 new + continue（手动改过）不切。
func TestSwitchAfterNewOnlyFollowsASuccessfulNewRun(t *testing.T) {
	f, id := sourceFixture(t, map[string]any{"source": "new", "after_new": "continue", "new_conversation": false})
	g := f.env.hub.guardian
	freshCmd := "agent -p --approve-mcps --trust --workspace '/work/app' --output-format stream-json 'x'"
	rec := func(mut func(*model.TerminalRun)) model.TerminalRun {
		r := model.TerminalRun{
			RunID: "run-x", State: model.RunSucceeded, Thread: launcherThreadPrefix + id, Command: freshCmd,
			CredentialName: launcherRunCaller,
		}
		mut(&r)
		return r
	}
	unchanged := func(name string) {
		t.Helper()
		if got := f.presetRun(t, id); got.Source != store.HeadlessSourceNew || got.AfterNew != store.HeadlessAfterContinue {
			t.Fatalf("%s：预设不该被改: %+v", name, got)
		}
	}
	g.switchAfterNew(rec(func(r *model.TerminalRun) { r.State = model.RunFailed }))
	unchanged("失败的运行")
	g.switchAfterNew(rec(func(r *model.TerminalRun) { r.State = model.RunCancelled }))
	unchanged("被取消的运行")
	g.switchAfterNew(rec(func(r *model.TerminalRun) { r.Command = strings.Replace(freshCmd, "-p ", "-p --resume chat-9 ", 1) }))
	unchanged("带 --resume 的运行")
	g.switchAfterNew(rec(func(r *model.TerminalRun) { r.Command = strings.Replace(freshCmd, "-p ", "-p --continue ", 1) }))
	unchanged("带 --continue 的运行")
	g.switchAfterNew(rec(func(r *model.TerminalRun) { r.Thread = "some-thread" }))
	unchanged("不是预设运行的线程")
	g.switchAfterNew(rec(func(r *model.TerminalRun) { r.Thread = launcherThreadPrefix + "missing" }))
	unchanged("预设已经不存在")

	// 手动改成「指定会话」之后，晚到的成功回调不覆盖手动改动。
	f.saveLauncher(t, id, "启用 Agent", map[string]any{"workflow_id": newConvWF, "source": "resume", "posture": "force"})
	g.switchAfterNew(rec(func(r *model.TerminalRun) {}))
	if got := f.presetRun(t, id); got.Source != store.HeadlessSourceResume {
		t.Fatalf("手动改动不该被覆盖: %+v", got)
	}
	if n := f.auditCount(t, EvLauncherSwitch); n != 0 {
		t.Fatalf("没有切换就不该有审计: %d", n)
	}

	// 条件都满足：切。
	f.saveLauncher(t, id, "启用 Agent", map[string]any{"workflow_id": newConvWF, "source": "new", "after_new": "continue", "posture": "force"})
	g.switchAfterNew(rec(func(r *model.TerminalRun) {}))
	if got := f.presetRun(t, id); got.Source != store.HeadlessSourceContinue || got.AfterNew != "" {
		t.Fatalf("成功的新开运行应触发切换: %+v", got)
	}
	if n := f.auditCount(t, EvLauncherSwitch); n != 1 {
		t.Fatalf("应有一条切换审计: %d", n)
	}
}

// 最近会话：Hub 不知道 --continue 会接到哪个对话。未知就放行并提示可能接错；工作流已知的对话正在工作就拒绝（近似保护）。
func TestLauncherSourceContinueProtectsTheKnownLiveConversation(t *testing.T) {
	f, id := sourceFixture(t, map[string]any{"source": "continue", "new_conversation": false})
	f.completeReadable(otherConvOut)
	ctx, g := context.Background(), f.env.hub.guardian

	in := g.inspectLauncher(ctx, mustPreset(t, f, id), time.Now())
	if len(in.errs) != 0 || !in.cont || in.newConv || in.conv != "" {
		t.Fatalf("没有已知对话时应放行为最近会话: %+v errs=%v", in, in.errs)
	}
	if !containsText(in.warnings, "可能不是这个工作流的") {
		t.Fatalf("应提示可能接错对话: %v", in.warnings)
	}
	if !strings.HasPrefix(in.command, "agent -p --continue --approve-mcps --trust ") {
		t.Fatalf("预览的命令应是 --continue: %s", in.command)
	}
	if in.modeName() != store.HeadlessSourceContinue {
		t.Fatalf("预览的方式应是 continue: %s", in.modeName())
	}

	// 工作流里已知的对话正在工作：拒绝，错误码与指定会话同一套。
	now := time.Now()
	ingest := func(name, status string) {
		t.Helper()
		if _, err := f.env.store.IngestAgentEvent(ctx, store.AgentIngestInput{
			WorkflowID: newConvWF, CredentialID: 7, HostName: "exec-host", ProjectDirectory: "/work/app", ConversationID: "chat-known-1", ClientKind: "cli",
			Event: agentstate.Event{Source: agentstate.SourceHook, Name: name, Status: status, At: now, Received: now},
		}); err != nil {
			t.Fatal(err)
		}
	}
	ingest(agentstate.EvPrompt, "")
	_, err := f.runLauncher(id)
	we, ok := err.(*wakeError)
	if !ok || we.Code != "conversation_live" {
		t.Fatalf("已知对话在运行时应拒绝: %v", err)
	}
	if n := f.sim.runCount(); n != 0 {
		t.Fatalf("被拒绝的运行不该到达主机: %d", n)
	}
	// 它停下来之后就放行。
	ingest(agentstate.EvStop, agentstate.StopCompleted)
	plan, err := f.runLauncher(id)
	if err != nil || !plan.Continue {
		t.Fatalf("已知对话停下来之后应能运行: %+v %v", plan, err)
	}
	f.waitRunFinal(t, plan.RunID)
}

func mustPreset(t *testing.T, f *execFixture, id string) store.TerminalLauncher {
	t.Helper()
	list, err := f.env.store.ListTerminalLaunchers(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range list {
		if l.ID == id {
			return l
		}
	}
	t.Fatalf("找不到预设 %s", id)
	return store.TerminalLauncher{}
}

// 新会话每次都新开的预设也要避开工作流里正在运行的智能体（与记下编号那一档同一条保护）。
func TestLauncherSourceNewStayRefusesALiveAgent(t *testing.T) {
	f, id := sourceFixture(t, map[string]any{"source": "new", "after_new": "stay", "new_conversation": false})
	f.completeWith(newConvOut)
	now := time.Now()
	if _, err := f.env.store.IngestAgentEvent(context.Background(), store.AgentIngestInput{
		WorkflowID: newConvWF, CredentialID: 7, HostName: "exec-host", ProjectDirectory: "/work/app",
		Event: agentstate.Event{Source: agentstate.SourceHook, Name: agentstate.EvPrompt, At: now, Received: now},
	}); err != nil {
		t.Fatal(err)
	}
	_, err := f.runLauncher(id)
	we, ok := err.(*wakeError)
	if !ok || we.Code != "conversation_live" || !strings.Contains(we.Message, "已经有智能体在运行") {
		t.Fatalf("有智能体在运行时应拒绝新开: %v", err)
	}
	if n := f.sim.runCount(); n != 0 {
		t.Fatalf("被拒绝的运行不该到达主机: %d", n)
	}
}

// 失败识别：--continue 的运行在目录里没有旧对话时，CLI 只打 `No previous chats found.`——认成「需要你处理」，不自动续跑；
// 同样的字样出现在不带 --continue 的运行里，分类不变（仍是「未知」）。
func TestClassifyRunExitNoPreviousChatsOnlyForContinue(t *testing.T) {
	exit1 := 1
	cont := model.TerminalRun{
		State: model.RunFailed, ExitCode: &exit1, PreviewHead: "No previous chats found.\n",
		Command: "agent -p --continue --approve-mcps --trust --workspace '/w' --output-format stream-json 'x'",
	}
	e := classifyRunExit(cont)
	if e.Class != ExitActionRequired || e.Retry || !strings.Contains(e.Text, "没有可接着的旧对话") || !strings.Contains(e.Text, "新会话") || e.Raw != "No previous chats found." {
		t.Fatalf("--continue 没有旧对话应归「需要你处理」: %+v", e)
	}
	if !e.IsFailure() {
		t.Fatal("它是异常退出")
	}
	// 容许大小写、结尾标点与前后空白，也容许它不在最后一行。
	for _, out := range []string{"  no previous chats found  \n", "no previous chats found\n\nstack...\n", "NO PREVIOUS CHATS FOUND.\r\n"} {
		c := cont
		c.PreviewHead = out
		if got := classifyRunExit(c); got.Class != ExitActionRequired {
			t.Errorf("%q 应认出: %+v", out, got)
		}
	}
	// 不带 --continue：同样的文字不改分类。
	resume := cont
	resume.Command = "agent -p --resume chat-9 --approve-mcps --trust --workspace '/w' --output-format stream-json 'x'"
	if got := classifyRunExit(resume); got.Class != ExitUnknown {
		t.Fatalf("没有 --continue 不该认成没有旧对话: %+v", got)
	}
	// 智能体回复里引用这句话（在 JSON 行里）不算。
	quoted := cont
	quoted.PreviewHead = "{\"type\":\"result\",\"result\":\"CLI said: No previous chats found.\"}\n"
	if got := classifyRunExit(quoted); got.Class != ExitUnknown {
		t.Fatalf("JSON 里引用这句话不该认成没有旧对话: %+v", got)
	}
	// 有信号终止仍是硬事实，排在前面。
	sig := cont
	sig.Signal = "SIGKILL"
	if got := classifyRunExit(sig); got.Class != ExitCrashed {
		t.Fatalf("信号终止优先: %+v", got)
	}
	// 失败说明里带出处理建议。
	fv := failureViewOf(e)
	if fv == nil || fv.Class != string(ExitActionRequired) || fv.Retry {
		t.Fatalf("界面的失败说明不对: %+v", fv)
	}
}

// 端到端：--continue 的运行没有旧对话 → 运行现状里是「需要你处理」，且不会被自动续跑（即使预设打开了自动续跑）。
func TestContinueWithoutPreviousChatsIsNotAutoResumed(t *testing.T) {
	f, id := autoFixture(t, map[string]any{"source": "continue"})
	ctx, g, clock := context.Background(), f.env.hub.guardian, f.env.clock
	f.sim.onRun = func(s *hostSim, m termproto.Run) {
		s.started(m.ID)
		s.end(m.ID, termproto.RunExited, intp(1), "", "No previous chats found.\n")
	}
	plan, err := f.runAt(id)
	if err != nil || !plan.Continue {
		t.Fatalf("应能提交最近会话的运行: %+v %v", plan, err)
	}
	rec := f.waitRunFinal(t, plan.RunID)
	f.waitChat(t, "聊天里有结束行", func(x []string) bool { return chatHas(x, "已结束：失败", "没有可接着的旧对话") })
	rv := g.runViewOf(ctx, *rec, clock.Now())
	if rv.Failure == nil || rv.Failure.Class != string(ExitActionRequired) || rv.Failure.Retry || rv.Mode != store.HeadlessSourceContinue {
		t.Fatalf("运行现状应是需要你处理: %+v / %+v", rv, rv.Failure)
	}
	clock.Advance(2 * time.Hour)
	g.autoResumeTick(ctx, clock.Now())
	if n := len(f.autoPreset(t, id).AutoResumes); n != 0 {
		t.Fatalf("需要你处理的失败不该排自动续跑: %+v", f.autoPreset(t, id).AutoResumes)
	}
	if f.sim.runCount() != 1 {
		t.Fatalf("不该再运行: %d", f.sim.runCount())
	}
}

// 自动续跑与对话来源（#191 §3.6）：「新会话 · 不切换」的预设首次运行出事后，重试要接着出事那次运行学到的对话，不能再新开一个；
// 学到的编号来自运行输出开头的 init 事件（不依赖工作流上记的对话——这里故意让工作流上记的是别的对话 chat-9）。
func TestAutoResumeKeepsTheConversationTheFailedNewRunLearned(t *testing.T) {
	f, id := autoFixture(t, map[string]any{"source": "new", "after_new": "stay"})
	ctx, g, clock := context.Background(), f.env.hub.guardian, f.env.clock
	// 假主机：新开的这次运行先发了 init（带对话编号 chat-failed-1）就以可重试的错误退出。不提供可读输出，所以监视学不到——
	// 重试能接对，靠的只能是运行记录里的开头预览。
	initLine := "{\"type\":\"system\",\"subtype\":\"init\",\"session_id\":\"chat-failed-1\"}\n"
	f.sim.onRun = func(s *hostSim, m termproto.Run) {
		s.started(m.ID)
		s.end(m.ID, termproto.RunExited, intp(1), initLine, autoRetriableErr)
	}

	first, err := f.runAt(id)
	if err != nil || !first.NewConversation {
		t.Fatalf("第一次应新开: %+v %v", first, err)
	}
	f.waitAutoRecords(t, id, 1)
	if got := f.env.store.AgentLinkOf(ctx, launcherWF).ConversationID; got != "chat-9" {
		t.Fatalf("前提：工作流上记的应仍是 chat-9（没学到）: %q", got)
	}

	// 到点：重试必须是 --resume chat-failed-1，而不是再新开、也不是接工作流上记的 chat-9。
	f.completeWith(launcherResultOut)
	clock.Advance(5 * time.Minute)
	g.autoResumeTick(ctx, clock.Now())
	eventually(t, autoWait, "假主机收到第二次运行", func() bool { return f.sim.runCount() == 2 })
	cmd := f.sim.lastRun().Cmd
	if !strings.HasPrefix(cmd, "agent -p --resume chat-failed-1 --approve-mcps --trust ") {
		t.Fatalf("自动续跑应接着出事那次学到的对话: %s", cmd)
	}
	f.waitRunFinal(t, f.autoPreset(t, id).AutoResumes[0].RunID)
}

// 最近会话的自动续跑：重试仍是 --continue（不挑具体对话）。
func TestAutoResumeOfAContinuePresetStaysContinue(t *testing.T) {
	f, id := autoFixture(t, map[string]any{"source": "continue"})
	ctx, g, clock := context.Background(), f.env.hub.guardian, f.env.clock
	f.failWith(autoRetriableErr)
	first, err := f.runAt(id)
	if err != nil || !first.Continue {
		t.Fatalf("第一次应是最近会话: %+v %v", first, err)
	}
	f.waitAutoRecords(t, id, 1)

	f.completeWith(launcherResultOut)
	clock.Advance(5 * time.Minute)
	g.autoResumeTick(ctx, clock.Now())
	eventually(t, autoWait, "假主机收到第二次运行", func() bool { return f.sim.runCount() == 2 })
	if cmd := f.sim.lastRun().Cmd; !strings.HasPrefix(cmd, "agent -p --continue --approve-mcps --trust ") || strings.Contains(cmd, "--resume") {
		t.Fatalf("最近会话的自动续跑应仍是 --continue: %s", cmd)
	}
	f.waitRunFinal(t, f.autoPreset(t, id).AutoResumes[0].RunID)
}

// 预设保存接口：对话来源与「运行之后的去向」经 REST 往返；旧的「新开」勾选只是镜像。
func TestLauncherSourceRoundTripsThroughThePresetAPI(t *testing.T) {
	f := newExecFixture(t)
	f.saveLauncher(t, "s1", "新会话不切换", map[string]any{"workflow_id": "wf-a", "source": "new", "after_new": "stay"})
	f.saveLauncher(t, "s2", "最近会话", map[string]any{"workflow_id": "wf-b", "source": "continue", "after_new": "stay"})
	f.saveLauncher(t, "s3", "存量", map[string]any{"workflow_id": "wf-c", "new_conversation": true})
	if got := f.presetRun(t, "s1"); got.Source != "new" || got.AfterNew != "stay" || got.NewConversation {
		t.Fatalf("s1: %+v", got)
	}
	if got := f.presetRun(t, "s2"); got.Source != "continue" || got.AfterNew != "" || got.NewConversation {
		t.Fatalf("s2 的无关去向应被清掉: %+v", got)
	}
	if got := f.presetRun(t, "s3"); got.Source != "" || got.AfterNew != "" || !got.NewConversation || got.EffectiveSource() != "new" {
		t.Fatalf("存量预设保存后仍按旧字段: %+v", got)
	}
	// 非法组合被拒绝并说明原因。
	res := f.env.putLauncher("/api/v1/terminal/launchers/bad", map[string]any{
		"name": "坏的", "host": f.a.code, "dir": "/work/app", "kind": "headless",
		"run": map[string]any{"workflow_id": "wf-x", "source": "continue", "conversation_id": "chat-1"},
	}, f.adm...)
	if res.Status != 400 || !strings.Contains(res.Raw, "不能再固定对话编号") {
		t.Fatalf("最近会话固定编号应被拒绝: %d %s", res.Status, res.Raw)
	}
}
