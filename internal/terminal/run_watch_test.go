package terminal

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/teacat99/RelayMesh/v2/internal/agentstate"
	"github.com/teacat99/RelayMesh/v2/internal/model"
	"github.com/teacat99/RelayMesh/v2/internal/store"
	"github.com/teacat99/RelayMesh/v2/internal/termproto"
)

// 运行输出监视（#129）的集成测试：假主机按 stream-json 的节奏产出输出，Hub 读到后推进对话的状态。

const (
	watchWF   = "wf-watch"
	watchConv = "conv-watch-1"
)

// watchOut 是假主机上一次运行的 stdout：测试往里追加行，假主机按偏移served。
type watchOut struct {
	mu  sync.Mutex
	buf []byte
	f   *execFixture
	id  string
}

func (o *watchOut) add(lines ...string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	for _, l := range lines {
		o.buf = append(o.buf, l...)
		o.buf = append(o.buf, '\n')
	}
	o.f.sim.setOutput(o.id, "stdout", append([]byte(nil), o.buf...))
}

func (o *watchOut) setID(id string) {
	o.mu.Lock()
	o.id = id
	o.mu.Unlock()
}

func (o *watchOut) ident() string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.id
}

func (o *watchOut) size() int {
	o.mu.Lock()
	defer o.mu.Unlock()
	return len(o.buf)
}

// addRaw 追加不带换行的原始字节（测试半行）。
func (o *watchOut) addRaw(s string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.buf = append(o.buf, s...)
	o.f.sim.setOutput(o.id, "stdout", append([]byte(nil), o.buf...))
}

// watchFixture 在执行夹具上让对话 watchConv 在状态核心里有一个 hook 汇报过的会话（空闲），并把监视的节奏调快。
func watchFixture(t *testing.T) *execFixture {
	t.Helper()
	f := newExecFixture(t)
	f.svc.watch.timing = runWatchTiming{
		Poll: 15 * time.Millisecond, PollSlow: 15 * time.Millisecond, MinGap: 3 * time.Millisecond,
		ReadTimeout: 2 * time.Second, FinalRead: 2 * time.Second,
		Authority: 5 * time.Second, Tail: 400 * time.Millisecond, MaxAge: 30 * time.Second,
	}
	f.hookEvent(t, agentstate.EvSessionStart, "")
	f.hookEvent(t, agentstate.EvStop, agentstate.StopCompleted)
	if v := f.sessionView(t); v.State != "idle" {
		t.Fatalf("setup: session state = %q", v.State)
	}
	return f
}

// hookEvent 以编辑器 hook 的身份给这个对话报一个事件。
func (f *execFixture) hookEvent(t *testing.T, name, status string) store.AgentIngestResult {
	t.Helper()
	now := time.Now()
	res, err := f.env.store.IngestAgentEvent(context.Background(), store.AgentIngestInput{
		WorkflowID: watchWF, CredentialID: 7, HostName: "exec-host", ProjectDirectory: "/work/app", ConversationID: watchConv,
		Event: agentstate.Event{Source: agentstate.SourceHook, Name: name, Status: status, At: now, Received: now},
	})
	if err != nil {
		t.Fatalf("hook %s: %v", name, err)
	}
	return res
}

func (f *execFixture) sessionView(t *testing.T) store.AgentSessionView {
	t.Helper()
	list, err := f.env.store.ListAgentSessions(context.Background(), store.AgentSessionFilter{WorkflowID: watchWF}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range list {
		if v.ConversationID == watchConv {
			return v
		}
	}
	t.Fatalf("no session for %s in %+v", watchConv, list)
	return store.AgentSessionView{}
}

// waitView 等对话的展示状态满足条件，返回满足时的视图。
func (f *execFixture) waitView(t *testing.T, what string, ok func(v store.AgentSessionView) bool) store.AgentSessionView {
	t.Helper()
	var v store.AgentSessionView
	eventually(t, 4*time.Second, what, func() bool {
		v = f.sessionView(t)
		return ok(v)
	})
	return v
}

// submitWatched 提交一次要续聊 watchConv 的非交互运行。假主机收到后立即开始，输出由返回的 watchOut 逐步追加。
func (f *execFixture) submitWatched(t *testing.T, started bool) (*watchOut, string) {
	t.Helper()
	out := &watchOut{f: f}
	idCh := make(chan string, 1)
	f.sim.onRun = func(s *hostSim, m termproto.Run) {
		out.setID(m.ID)
		if started {
			s.started(m.ID)
		}
		idCh <- m.ID
	}
	cmd, err := headlessCmd{Bin: "agent", ChatID: watchConv, Dir: "/work/app", Posture: store.HeadlessPostureForce, Prompt: "continue"}.build()
	if err != nil {
		t.Fatal(err)
	}
	res, err := f.svc.submitHeadless(context.Background(), headlessSubmit{
		Caller: launcherRunCaller, UserAgent: "test", Machine: f.a.code, Workflow: watchWF, Conversation: watchConv,
		Cwd: "/work/app", Command: cmd, Description: "watch test",
	})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case id := <-idCh:
		if id != res.RunID {
			t.Fatalf("run id mismatch %s vs %s", id, res.RunID)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("假主机没有收到运行")
	}
	return out, res.RunID
}

// finishRun 让假主机报告运行结束。
func (o *watchOut) finishRun(state string, code int) {
	o.f.sim.endFull(termproto.RunEnd{ID: o.ident(), State: state, Code: intp(code), DurMS: 50, OutBytes: int64(o.size())})
}

func jsonLine(typ, sub string) string {
	return fmt.Sprintf(`{"type":%q,"subtype":%q,"session_id":%q}`, typ, sub, watchConv)
}

// ---------- 用例 ----------

func TestRunWatchFollowsStreamJSONFromStartToResult(t *testing.T) {
	f := watchFixture(t)
	var mu sync.Mutex
	var states []string
	f.env.hub.SetAgentStateHook(func(ev AgentStateEvent) {
		mu.Lock()
		defer mu.Unlock()
		if ev.WorkflowID != watchWF || ev.IdentityID == 0 || ev.SessionID == 0 {
			t.Errorf("bad event %+v", ev)
		}
		states = append(states, ev.State+"/"+ev.Sub)
	})

	out, runID := f.submitWatched(t, true)
	if !f.svc.watch.watching(runID) {
		t.Fatal("提交后应当开始监视")
	}

	out.add(jsonLine("system", "init"), jsonLine("thinking", "delta"))
	v := f.waitView(t, "思考中", func(v store.AgentSessionView) bool { return v.State == "working" && v.Sub == "thinking" })
	if v.StateSource != "run" {
		t.Fatalf("来源 = %q，应是 run", v.StateSource)
	}

	out.add(toolStarted("c1", "shellToolCall"))
	f.waitView(t, "工具 Shell 运行中", func(v store.AgentSessionView) bool { return v.Sub == "tool:Shell" })
	// 工具跑得再久（流里一片静默）也只是在跑，不会被怀疑在等批准。
	if v := f.sessionView(t); v.ApprovalSuspected {
		t.Fatalf("非交互运行不该怀疑等批准: %+v", v)
	}

	out.add(toolDone("c1", "shellToolCall", "success"), jsonLine("assistant", ""))
	f.waitView(t, "回复中", func(v store.AgentSessionView) bool { return v.Sub == "responding" })

	out.add(lnResultOK)
	out.finishRun(termproto.RunExited, 0)
	v = f.waitView(t, "本轮结束回到空闲", func(v store.AgentSessionView) bool { return v.State == "idle" && v.StateSource == "run" })
	if v.TurnEndedAt == nil || v.TurnStartedAt == nil {
		t.Fatalf("轮次时间不全: %+v", v)
	}
	eventually(t, 2*time.Second, "监视收尾退出", func() bool { return !f.svc.watch.watching(runID) })

	mu.Lock()
	defer mu.Unlock()
	want := []string{"working/thinking", "working/tool:Shell", "working/responding", "idle/"}
	if strings.Join(states, ",") != strings.Join(want, ",") {
		t.Fatalf("状态通知序列 = %v，期望 %v", states, want)
	}
}

func TestRunWatchHookReportsDoNotOverrideWhileTheRunLives(t *testing.T) {
	f := watchFixture(t)
	out, runID := f.submitWatched(t, true)
	out.add(jsonLine("system", "init"), toolStarted("c1", "shellToolCall"))
	f.waitView(t, "工具 Shell 运行中", func(v store.AgentSessionView) bool { return v.Sub == "tool:Shell" })

	// 非交互模式下 hook 会在运行期间乱报：思考心跳、sessionEnd、甚至 stop。都只留记录，不改状态。
	f.hookEvent(t, agentstate.EvThought, "")
	if r := f.hookEvent(t, agentstate.EvSessionEnd, "closed"); r.Change.StateChanged || r.Change.Note != agentstate.NoteRunAuthority {
		t.Fatalf("hook sessionEnd 不该改状态: %+v", r.Change)
	}
	if r := f.hookEvent(t, agentstate.EvStop, agentstate.StopCompleted); r.Change.StateChanged {
		t.Fatalf("hook stop 不该改状态: %+v", r.Change)
	}
	if v := f.sessionView(t); v.State != "working" || v.Sub != "tool:Shell" || v.StateSource != "run" {
		t.Fatalf("运行期间状态被 hook 改了: %+v", v)
	}

	// 运行结束。收尾阶段 hook 的 sessionEnd 依然不能把「本次运行结束」改成 ended。
	out.add(toolDone("c1", "shellToolCall", "success"), lnResultOK)
	out.finishRun(termproto.RunExited, 0)
	f.waitView(t, "空闲", func(v store.AgentSessionView) bool { return v.State == "idle" })
	if r := f.hookEvent(t, agentstate.EvSessionEnd, "closed"); r.Change.StateChanged {
		t.Fatalf("收尾期的 sessionEnd 不该把状态改成 ended: %+v", r.Change)
	}
	if v := f.sessionView(t); v.State != "idle" {
		t.Fatalf("收尾后状态 = %q，应保持 idle", v.State)
	}
	eventually(t, 2*time.Second, "监视收尾退出", func() bool { return !f.svc.watch.watching(runID) })

	// 收尾期（Tail）过后，hook 恢复说话：交互使用同一个对话时状态要跟着 hook 走。
	time.Sleep(450 * time.Millisecond)
	r := f.hookEvent(t, agentstate.EvPrompt, "")
	if r.Change.Note == agentstate.NoteRunAuthority || !r.Change.StateChanged {
		t.Fatalf("Tail 过后 hook 应恢复生效: %+v", r.Change)
	}
	if v := f.sessionView(t); v.State != "working" || v.StateSource != "hook" {
		t.Fatalf("hook 恢复后状态 = %+v", v)
	}
}

func TestRunWatchCancelledRunEndsAsAborted(t *testing.T) {
	f := watchFixture(t)
	out, _ := f.submitWatched(t, true)
	out.add(jsonLine("system", "init"), jsonLine("thinking", "delta"))
	f.waitView(t, "思考中", func(v store.AgentSessionView) bool { return v.State == "working" })

	// 被杀的运行没有 result：由终态补一个 stop(aborted)。
	out.finishRun(termproto.RunCancelled, -1)
	v := f.waitView(t, "被中断", func(v store.AgentSessionView) bool { return v.State == "aborted" })
	if v.StateSource != "run" {
		t.Fatalf("来源 = %q", v.StateSource)
	}
}

func TestRunWatchFailureWithoutResultEndsAsError(t *testing.T) {
	f := watchFixture(t)
	out, _ := f.submitWatched(t, true)
	out.add(jsonLine("system", "init"), toolStarted("c1", "readToolCall"))
	f.waitView(t, "工具 Read", func(v store.AgentSessionView) bool { return v.Sub == "tool:Read" })
	out.finishRun(termproto.RunExited, 3)
	f.waitView(t, "出错结束", func(v store.AgentSessionView) bool { return v.State == "error" })
}

func TestRunWatchResultErrorWinsEvenWhenExitCodeIsZero(t *testing.T) {
	f := watchFixture(t)
	out, _ := f.submitWatched(t, true)
	out.add(jsonLine("system", "init"), lnResultErr)
	out.finishRun(termproto.RunExited, 0)
	f.waitView(t, "出错结束", func(v store.AgentSessionView) bool { return v.State == "error" })
}

func TestRunWatchLeavesTheConversationAloneWhenTheRunNeverTouchedIt(t *testing.T) {
	f := watchFixture(t)
	out, runID := f.submitWatched(t, true)
	// 命令一启动就失败：输出只有一行纯文本报错，没有任何 stream-json 事件。
	out.add("agent: command not found")
	out.finishRun(termproto.RunExited, 127)
	eventually(t, 2*time.Second, "监视收尾退出", func() bool { return !f.svc.watch.watching(runID) })
	if v := f.sessionView(t); v.State != "idle" || v.StateSource != "hook" {
		t.Fatalf("没动过对话的运行不该改状态: %+v", v)
	}
	// 也没有留下运行权威：hook 立刻照常生效。
	if r := f.hookEvent(t, agentstate.EvPrompt, ""); r.Change.Note == agentstate.NoteRunAuthority {
		t.Fatalf("没动过对话的运行不该留下权威标记: %+v", r.Change)
	}
}

func TestRunWatchWaitsOutAnApprovalOrSlowStart(t *testing.T) {
	f := watchFixture(t)
	out, runID := f.submitWatched(t, false) // 主机收到了，但还没开始（没有输出可读）
	time.Sleep(120 * time.Millisecond)      // 轮询几轮，读不到输出：安静地等
	if !f.svc.watch.watching(runID) {
		t.Fatal("还没开始的运行应继续被监视")
	}
	if v := f.sessionView(t); v.State != "idle" {
		t.Fatalf("没开始的运行不该改状态: %+v", v)
	}
	f.sim.started(runID)
	out.add(jsonLine("system", "init"), jsonLine("thinking", "delta"))
	f.waitView(t, "开始后读到输出", func(v store.AgentSessionView) bool { return v.State == "working" && v.StateSource == "run" })
	out.add(lnResultOK)
	out.finishRun(termproto.RunExited, 0)
	f.waitView(t, "空闲", func(v store.AgentSessionView) bool { return v.State == "idle" })
}

func TestRunWatchHandlesLinesSplitAcrossReads(t *testing.T) {
	f := watchFixture(t)
	out, _ := f.submitWatched(t, true)
	line := toolStarted("c1", "shellToolCall")
	out.add(jsonLine("system", "init"))
	out.addRaw(line[:len(line)/2]) // 半行：这一轮读到的是残缺的 JSON
	f.waitView(t, "init 之后", func(v store.AgentSessionView) bool { return v.State == "working" })
	time.Sleep(60 * time.Millisecond)
	if v := f.sessionView(t); v.Sub == "tool:Shell" {
		t.Fatal("半行不该被当成工具开始")
	}
	out.addRaw(line[len(line)/2:] + "\n")
	f.waitView(t, "补全后认出工具开始", func(v store.AgentSessionView) bool { return v.Sub == "tool:Shell" })
	out.add(lnResultOK)
	out.finishRun(termproto.RunExited, 0)
	f.waitView(t, "空闲", func(v store.AgentSessionView) bool { return v.State == "idle" })
}

func TestRunWatchFlushesAFinalLineWithoutNewline(t *testing.T) {
	f := watchFixture(t)
	out, _ := f.submitWatched(t, true)
	out.add(jsonLine("system", "init"))
	f.waitView(t, "working", func(v store.AgentSessionView) bool { return v.State == "working" })
	out.addRaw(lnResultOK) // 最后一行没有换行就结束了
	out.finishRun(termproto.RunExited, 0)
	f.waitView(t, "空闲", func(v store.AgentSessionView) bool { return v.State == "idle" })
}

func TestRunWatchPicksUpARunThatFinishedBeforeRegistration(t *testing.T) {
	f := watchFixture(t)
	out := &watchOut{f: f}
	f.sim.onRun = func(s *hostSim, m termproto.Run) {
		out.setID(m.ID)
		s.started(m.ID)
		out.add(jsonLine("system", "init"), lnResultOK)
		s.endFull(termproto.RunEnd{ID: m.ID, State: termproto.RunExited, Code: intp(0), DurMS: 5, OutBytes: int64(out.size())})
	}
	// 不带 Conversation 提交（不自动监视），等它自己跑完，再补登记——模拟登记得比终结晚。
	cmd, _ := headlessCmd{Bin: "agent", ChatID: watchConv, Dir: "/work/app", Posture: store.HeadlessPostureForce, Prompt: "x"}.build()
	res, err := f.svc.submitHeadless(context.Background(), headlessSubmit{
		Caller: launcherRunCaller, UserAgent: "test", Machine: f.a.code, Workflow: watchWF,
		Cwd: "/work/app", Command: cmd, Description: "late",
	})
	if err != nil {
		t.Fatal(err)
	}
	f.waitRunFinal(t, res.RunID)
	f.svc.watch.start(runWatchTarget{RunID: res.RunID, Machine: f.a.code, WorkflowID: watchWF, Conversation: watchConv})
	f.waitView(t, "补登记后仍然读到整份输出并收尾", func(v store.AgentSessionView) bool { return v.State == "idle" && v.StateSource == "run" })
	eventually(t, 2*time.Second, "监视退出", func() bool { return !f.svc.watch.watching(res.RunID) })
}

func TestRunWatchRestoredRunReplaysOnlyTheCurrentState(t *testing.T) {
	f := watchFixture(t)
	var mu sync.Mutex
	changes := 0
	f.env.hub.SetAgentStateHook(func(AgentStateEvent) { mu.Lock(); changes++; mu.Unlock() })

	// 一个已经跑了一阵的运行：历史里有好几轮工具调用，眼下停在第三个工具上。
	out := &watchOut{f: f}
	f.sim.onRun = func(s *hostSim, m termproto.Run) {
		out.setID(m.ID)
		s.started(m.ID)
		out.add(jsonLine("system", "init"), jsonLine("thinking", "delta"),
			toolStarted("c1", "readToolCall"), toolDone("c1", "readToolCall", "success"),
			toolStarted("c2", "grepToolCall"), toolDone("c2", "grepToolCall", "success"),
			jsonLine("assistant", ""), toolStarted("c3", "shellToolCall"))
	}
	cmd, _ := headlessCmd{Bin: "agent", ChatID: watchConv, Dir: "/work/app", Posture: store.HeadlessPostureForce, Prompt: "x"}.build()
	res, err := f.svc.submitHeadless(context.Background(), headlessSubmit{
		Caller: launcherRunCaller, UserAgent: "test", Machine: f.a.code, Workflow: watchWF,
		Cwd: "/work/app", Command: cmd, Description: "restored",
	})
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, 2*time.Second, "假主机有了输出", func() bool { return out.ident() != "" && out.size() > 0 })

	// Hub 重启后接回：从命令里认出对话编号，只还原「现在」。
	rec := f.dbRun(t, res.RunID)
	if got := convFromRunCommand(rec.Command); got != watchConv {
		t.Fatalf("从运行命令认出的对话 = %q，应是 %q", got, watchConv)
	}
	f.svc.watch.restore(*rec)
	v := f.waitView(t, "还原成最后一个工具", func(v store.AgentSessionView) bool { return v.Sub == "tool:Shell" && v.StateSource == "run" })
	if v.State != "working" {
		t.Fatalf("还原后 = %+v", v)
	}
	mu.Lock()
	n := changes
	mu.Unlock()
	if n != 1 {
		t.Fatalf("重启接回只该通知一次状态变化，实际 %d 次", n)
	}

	// 接回之后新来的输出照常实时跟进。
	out.add(toolDone("c3", "shellToolCall", "success"), lnResultOK)
	out.finishRun(termproto.RunExited, 0)
	f.waitView(t, "接回后读到结束", func(v store.AgentSessionView) bool { return v.State == "idle" })
}

func TestRunWatchIsOptInAndIgnoresDuplicatesAndBlanks(t *testing.T) {
	f := watchFixture(t)
	w := f.svc.watch
	for name, tgt := range map[string]runWatchTarget{
		"no run":          {Machine: "m", WorkflowID: "w", Conversation: "c"},
		"no machine":      {RunID: "r", WorkflowID: "w", Conversation: "c"},
		"no workflow":     {RunID: "r", Machine: "m", Conversation: "c"},
		"no conversation": {RunID: "r", Machine: "m", WorkflowID: "w"},
	} {
		w.start(tgt)
		if w.watching(tgt.RunID) {
			t.Errorf("%s: must not start a watcher", name)
		}
	}
	// 提交时没有给对话编号的运行不监视。
	f.completeWith(lnResultOK)
	cmd, _ := headlessCmd{Bin: "agent", ChatID: watchConv, Dir: "/work/app", Posture: store.HeadlessPostureForce, Prompt: "x"}.build()
	res, err := f.svc.submitHeadless(context.Background(), headlessSubmit{
		Caller: launcherRunCaller, UserAgent: "test", Machine: f.a.code, Workflow: watchWF, Cwd: "/work/app", Command: cmd, Description: "no conv",
	})
	if err != nil {
		t.Fatal(err)
	}
	if w.watching(res.RunID) {
		t.Fatal("没有对话编号时不该监视")
	}
	f.waitRunFinal(t, res.RunID)
	if v := f.sessionView(t); v.State != "idle" || v.StateSource != "hook" {
		t.Fatalf("不监视的运行不该改状态: %+v", v)
	}
}

func TestRunWatchLimitsConcurrentWatchers(t *testing.T) {
	f := watchFixture(t)
	w := f.svc.watch
	for i := 0; i < runWatchMaxActive+10; i++ {
		w.start(runWatchTarget{RunID: fmt.Sprintf("fake-%d", i), Machine: "no-such-host", WorkflowID: "w", Conversation: "c"})
	}
	w.mu.Lock()
	n := len(w.by)
	w.mu.Unlock()
	if n > runWatchMaxActive {
		t.Fatalf("监视数 %d 超过上限 %d", n, runWatchMaxActive)
	}
}

func TestConvFromRunCommand(t *testing.T) {
	uuid := "e33e27ef-0fbb-46e4-9446-5dee56ae61fa"
	for in, want := range map[string]string{
		"agent -p --resume " + uuid + " --approve-mcps --trust":                uuid,
		"agent -p --resume=" + uuid + " --trust":                               uuid,
		"/home/u/.local/bin/agent -p --resume '" + uuid + "' --workspace '/w'": uuid,
		"agent -p --resume chat-9 --approve-mcps":                              "chat-9",
		"agent -p --trust 'say --resume nothing'":                              "nothing",
		"agent -p --approve-mcps":                                              "",
		"":                                                                     "",
		redactSecrets("agent -p --resume " + uuid + " --workspace /w 'x'"):     uuid, // 脱敏不会动对话编号
		"agent -p --resume [REDACTED] --workspace /w":                          "",
	} {
		if got := convFromRunCommand(in); got != want {
			t.Errorf("convFromRunCommand(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestStopStatusOfMapsRunStates(t *testing.T) {
	for state, want := range map[string]string{
		model.RunSucceeded: agentstate.StopCompleted,
		model.RunCancelled: agentstate.StopAborted,
		model.RunFailed:    agentstate.StopError,
		model.RunTimedOut:  agentstate.StopError,
		model.RunLost:      agentstate.StopError,
	} {
		if got := stopStatusOf(state); got != want {
			t.Errorf("stopStatusOf(%s) = %s, want %s", state, got, want)
		}
	}
}
