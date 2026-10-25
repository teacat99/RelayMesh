package terminal

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/teacat99/RelayMesh/v2/internal/agentstate"
	"github.com/teacat99/RelayMesh/v2/internal/sessionprompt"
	"github.com/teacat99/RelayMesh/v2/internal/store"
	"github.com/teacat99/RelayMesh/v2/internal/termproto"
)

// 「新开对话」（#176 第三期）：预设打开 new_conversation 后，没有对话编号时不再报错而是新开——
// 命令不带 --resume，提示词换成「接入工作流」版本；对话编号从这次运行输出的 init 事件里学到并记成工作流的对话编号，
// 之后的运行就是续聊（所以只会新开一次）。

const newConvWF = "wf-newconv"

// newConvFixture 在执行夹具上配好一条打开了「新开对话」的预设 p1：工作流 wf-newconv，hook 没汇报过任何对话。
func newConvFixture(t *testing.T, run map[string]any) (*execFixture, string) {
	t.Helper()
	f := newExecFixture(t)
	r := map[string]any{"workflow_id": newConvWF, "new_conversation": true, "posture": "force"}
	for k, v := range run {
		r[k] = v
	}
	f.saveLauncher(t, "p1", "启用 Agent", r)
	return f, "p1"
}

// newConvOut 是新开对话的运行输出：init 事件里带着 Cursor 给的对话编号，随后一条成功的终局。
const newConvOut = "{\"type\":\"system\",\"subtype\":\"init\",\"session_id\":\"chat-new-1\"}\n" +
	"{\"type\":\"result\",\"subtype\":\"success\",\"is_error\":false,\"result\":\"已接入，等待指示\",\"session_id\":\"chat-new-1\"}\n"

// completeReadable 让假主机对每个 run 立即成功结束，并且输出能被 Hub 的运行监视读到（readRun）：
// completeWith 只在结束事件里内联输出，监视读不到——而「学到对话编号」靠的正是读输出。
func (f *execFixture) completeReadable(stdout string) {
	f.sim.onRun = func(s *hostSim, m termproto.Run) {
		s.setOutput(m.ID, "stdout", []byte(stdout))
		s.started(m.ID)
		s.end(m.ID, termproto.RunExited, intp(0), stdout, "")
	}
}

func TestHeadlessCmdNewConversationOmitsResume(t *testing.T) {
	got, err := headlessCmd{Bin: "agent", New: true, Dir: "/work/app", Posture: store.HeadlessPostureForce, Model: "m-1", Prompt: "接入 wf"}.build()
	if err != nil {
		t.Fatal(err)
	}
	want := "agent -p --approve-mcps --trust --model 'm-1' --force --workspace '/work/app' --output-format stream-json '接入 wf'"
	if got != want {
		t.Fatalf("新开对话的命令不对：\n got %s\nwant %s", got, want)
	}
	if strings.Contains(got, "--resume") {
		t.Fatalf("新开对话不能带 --resume: %s", got)
	}

	// 续聊的形态不变；两种形态各自的必填项都守住。
	resume, err := headlessCmd{Bin: "agent", ChatID: "chat-9", Dir: "/work/app", Prompt: "x"}.build()
	if err != nil || !strings.HasPrefix(resume, "agent -p --resume chat-9 --approve-mcps --trust ") {
		t.Fatalf("续聊命令变了: %q %v", resume, err)
	}
	if _, err := (headlessCmd{Bin: "agent", New: true, ChatID: "chat-9", Dir: "/w", Prompt: "x"}).build(); err == nil {
		t.Fatal("新开对话同时给了对话编号，应当拒绝（二选一）")
	}
	if _, err := (headlessCmd{Bin: "agent", Dir: "/w", Prompt: "x"}).build(); err == nil {
		t.Fatal("续聊没有对话编号，应当拒绝")
	}
}

func TestStreamParserInitCarriesSessionID(t *testing.T) {
	var p streamParser
	sigs := p.Feed([]byte(lnInit + "\n" + lnThink + "\n" + lnResultOK + "\n"))
	if len(sigs) != 3 || sigs[0].Name != agentstate.EvPrompt || sigs[0].Conv != "s-1" {
		t.Fatalf("init 应带出对话编号: %+v", sigs)
	}
	if sigs[1].Conv != "" || sigs[2].Conv != "" {
		t.Fatalf("只有 init 带对话编号: %+v", sigs)
	}
	// 超长行（只保留开头嗅探）的 init 也读得到编号。
	long := `{"type":"system","subtype":"init","session_id":"s-long","cwd":"` + strings.Repeat("x", streamLineMax) + `"}` + "\n"
	if sigs := p.Feed([]byte(long)); len(sigs) != 1 || sigs[0].Conv != "s-long" {
		t.Fatalf("超长 init 行也应读到编号: %+v", sigs)
	}
}

func TestLauncherNewConversationRunsWithoutResumeAndLearnsTheConversation(t *testing.T) {
	f, id := newConvFixture(t, nil)
	f.completeReadable(newConvOut)

	plan, err := f.runLauncher(id)
	if err != nil {
		t.Fatal(err)
	}
	if !plan.NewConversation || plan.ConversationID != "" || plan.RunID == "" || plan.Posture != store.HeadlessPostureForce {
		t.Fatalf("计划不对: %+v", plan)
	}
	if !containsText(plan.Warnings, "这次运行会新开一个对话") {
		t.Fatalf("应提示这次会新开对话: %v", plan.Warnings)
	}

	eventually(t, 2*time.Second, "假主机收到运行", func() bool { return f.sim.runCount() == 1 })
	run := f.sim.lastRun()
	want := "agent -p --approve-mcps --trust --force --workspace '/work/app' --output-format stream-json '" +
		renderLauncherWake(context.Background(), f.env.store, launcherWake{
			Kind: sessionprompt.KindNew, WorkflowID: newConvWF, Project: "/work/app", MaxRunSec: headlessTimeoutSec,
		}).Text + "'"
	if run.Cmd != want {
		t.Fatalf("命令不对：\n got %s\nwant %s", run.Cmd, want)
	}
	if strings.Contains(run.Cmd, "--resume") {
		t.Fatalf("新开对话不能带 --resume: %s", run.Cmd)
	}
	for _, must := range []string{"workflow_id：" + newConvWF, "workflow_id \"" + newConvWF + "\"", "interactive_feedback", "inbox", "本次运行最长 7 天"} {
		if !strings.Contains(run.Cmd, must) {
			t.Errorf("默认的新开对话提示词里应有 %q：%s", must, run.Cmd)
		}
	}

	// 对话编号从输出的 init 事件里学到，记成这个工作流的对话编号；聊天里有「发起」与「新对话已建立」两条系统行。
	eventually(t, 3*time.Second, "学到对话编号", func() bool {
		return f.env.store.AgentLinkOf(context.Background(), newConvWF).ConversationID == "chat-new-1"
	})
	var texts []string
	eventually(t, 3*time.Second, "聊天里有发起行与建立行", func() bool {
		texts = f.chatTexts(newConvWF)
		return len(texts) >= 2 && containsText(texts, "新对话已建立")
	})
	if !strings.Contains(texts[0], "你手动运行了预设「启用 Agent」（非交互续聊 · 新开对话 · 默认模型 · 放开全部命令（--force））") {
		t.Fatalf("发起行不对: %q", texts[0])
	}
	for _, s := range texts {
		if strings.Contains(s, "agent -p") || strings.Contains(s, "没有人在终端里看你的输出") {
			t.Fatalf("系统行里不该有命令或提示词: %q", s)
		}
	}
	if !containsText(texts, "新对话已建立（对话 chat-new…）") {
		t.Fatalf("建立行应写对话编号的前 8 位: %v", texts)
	}
	f.waitRunFinal(t, plan.RunID)

	// 学到之后再运行就是续聊：命令带 --resume，不再新开。
	plan2, err := f.runLauncher(id)
	if err != nil {
		t.Fatal(err)
	}
	if plan2.NewConversation || plan2.ConversationID != "chat-new-1" {
		t.Fatalf("第二次应是续聊: %+v", plan2)
	}
	eventually(t, 2*time.Second, "假主机收到第二次运行", func() bool { return f.sim.runCount() == 2 })
	if cmd := f.sim.lastRun().Cmd; !strings.HasPrefix(cmd, "agent -p --resume chat-new-1 --approve-mcps --trust ") {
		t.Fatalf("第二次应接着学到的对话: %s", cmd)
	}
	f.waitRunFinal(t, plan2.RunID)
}

func TestLauncherNewConversationIsExplicitAndRefusesALiveAgent(t *testing.T) {
	f, id := newConvFixture(t, nil)
	f.completeWith(newConvOut)

	// 没打开「新开对话」的预设，没有对话编号照旧报错，不会悄悄新开。
	f.saveLauncher(t, "p2", "续聊", map[string]any{"workflow_id": "wf-plain"})
	if _, err := f.runLauncher("p2"); err == nil || !strings.Contains(err.Error(), "还不知道要接着哪个对话") {
		t.Fatalf("没打开新开的预设应报「不知道对话」: %v", err)
	}
	if n := f.sim.runCount(); n != 0 {
		t.Fatalf("被拒绝的运行不该到达主机: %d", n)
	}

	// 工作流里已经有智能体在运行（没汇报过对话编号的那种，比如只通过 MCP 接入的）：不新开，免得重复接入同一个会话。
	now := time.Now()
	ingest := func(name, status string) {
		t.Helper()
		if _, err := f.env.store.IngestAgentEvent(context.Background(), store.AgentIngestInput{
			WorkflowID: newConvWF, CredentialID: 7, HostName: "exec-host", ProjectDirectory: "/work/app",
			Event: agentstate.Event{Source: agentstate.SourceHook, Name: name, Status: status, At: now, Received: now},
		}); err != nil {
			t.Fatal(err)
		}
	}
	ingest(agentstate.EvPrompt, "")
	_, err := f.runLauncher(id)
	we, ok := err.(*wakeError)
	if !ok || we.Code != "conversation_live" || !strings.Contains(we.Message, "已经有智能体在运行") {
		t.Fatalf("有智能体在运行时应拒绝新开: %v", err)
	}
	if n := f.sim.runCount(); n != 0 {
		t.Fatalf("被拒绝的运行不该到达主机: %d", n)
	}

	// 它停下来（空闲）之后就不算在运行了：可以新开。
	ingest(agentstate.EvStop, agentstate.StopCompleted)
	plan, err := f.runLauncher(id)
	if err != nil || !plan.NewConversation {
		t.Fatalf("智能体空闲后应能新开: %+v %v", plan, err)
	}
	f.waitRunFinal(t, plan.RunID)
}

// 同一个预设同一时刻只提交一次：占住「正在提交」位置时，再来的提交被挡下，不会开出第二个智能体。
func TestLauncherRunSubmitIsSerialisedPerPreset(t *testing.T) {
	f, id := newConvFixture(t, nil)
	f.completeWith(newConvOut)
	g := f.env.hub.guardian

	if !g.acquireLauncherRun(id) {
		t.Fatal("第一次应占得到")
	}
	if g.acquireLauncherRun(id) {
		t.Fatal("同一个预设第二次占位应失败")
	}
	_, err := f.runLauncher(id)
	we, ok := err.(*wakeError)
	if !ok || we.Code != "run_active" || !strings.Contains(we.Message, "正在提交") {
		t.Fatalf("占着位置时再提交应被挡下: %v", err)
	}
	if n := f.sim.runCount(); n != 0 {
		t.Fatalf("被挡下的提交不该到达主机: %d", n)
	}
	g.releaseLauncherRun(id)
	plan, err := f.runLauncher(id)
	if err != nil {
		t.Fatalf("释放之后应能提交: %v", err)
	}
	f.waitRunFinal(t, plan.RunID)
	// 提交结束（成功或失败）都会释放：能再占。
	if !g.acquireLauncherRun(id) {
		t.Fatal("提交结束后占位应已释放")
	}
	g.releaseLauncherRun(id)
}

// Hub 重启后接回的新开对话运行：命令里没有 --resume，监视重新从输出历史里读到对话编号（restore 标 Learn）。
func TestRunWatchRestoredNewConversationRunLearnsFromHistory(t *testing.T) {
	f := newExecFixture(t)
	out := &watchOut{f: f}
	f.sim.onRun = func(s *hostSim, m termproto.Run) {
		out.setID(m.ID)
		s.started(m.ID)
		out.add(`{"type":"system","subtype":"init","session_id":"chat-restored"}`, jsonLine("thinking", "delta"))
	}
	cmd, err := headlessCmd{Bin: "agent", New: true, Dir: "/work/app", Posture: store.HeadlessPostureForce, Prompt: "x"}.build()
	if err != nil {
		t.Fatal(err)
	}
	// 不带 NewConversation 提交：提交时不自动监视，模拟 Hub 重启前的监视已经没了。
	res, err := f.svc.submitHeadless(context.Background(), headlessSubmit{
		Caller: launcherRunCaller, UserAgent: "test", Machine: f.a.code, Workflow: newConvWF,
		Cwd: "/work/app", Command: cmd, Description: "restored new conversation",
	})
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, 2*time.Second, "假主机有了输出", func() bool { return out.ident() != "" && out.size() > 0 })

	rec := f.dbRun(t, res.RunID)
	if got := convFromRunCommand(rec.Command); got != "" {
		t.Fatalf("新开对话的命令里没有 --resume，认不出对话编号: %q", got)
	}
	if got := f.env.store.AgentLinkOf(context.Background(), newConvWF).ConversationID; got != "" {
		t.Fatalf("restore 之前不该有对话编号: %q", got)
	}
	f.svc.watch.restore(*rec)
	eventually(t, 3*time.Second, "接回后学到对话编号", func() bool {
		return f.env.store.AgentLinkOf(context.Background(), newConvWF).ConversationID == "chat-restored"
	})
	// 接回读的是历史：「新对话已建立」那句话当时（真实场景里）已经记过，这里不重复。
	if containsText(f.chatTexts(newConvWF), "新对话已建立") {
		t.Fatalf("重启接回不该重复记「新对话已建立」: %v", f.chatTexts(newConvWF))
	}
	out.finishRun(termproto.RunExited, 0)
	eventually(t, 2*time.Second, "监视收尾退出", func() bool { return !f.svc.watch.watching(res.RunID) })
}

func containsText(texts []string, sub string) bool {
	for _, s := range texts {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}
