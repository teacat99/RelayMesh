package terminal

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/teacat99/RelayMesh/v2/internal/agentstate"
	"github.com/teacat99/RelayMesh/v2/internal/store"
	"github.com/teacat99/RelayMesh/v2/internal/termproto"
)

// 运行认领对话（#182）：Hub 发起的运行清楚自己替哪个工作流跑、跑的是哪个对话；hook 汇报带的工作流却来自项目目录里的
// .cursor/relaymesh.json——同一个仓库的多个工作树共用一份配置时，写的常常是另一个工作流。
// 现场：「启用 Agent」的新对话一开始，hook 就把状态会话建到了别的工作流名下；运行读到 init 学会了对话编号，
// 却没有把对话认回来，之后 hook 的事件又被运行权威挡掉，会话就永远卡在错的工作流里，会话页「运行」也找不到预设。

const (
	claimHookWF = "wf-hook"      // hook 配置里写的工作流（不是发起运行的那个）
	claimConvID = "chat-claim-1" // 新对话
)

// claimFixture 是执行夹具加上调快的监视节奏（与 watchFixture 相同）；不预先建任何会话。
func claimFixture(t *testing.T) *execFixture {
	t.Helper()
	f := newExecFixture(t)
	f.svc.watch.timing = runWatchTiming{
		Poll: 15 * time.Millisecond, PollSlow: 15 * time.Millisecond, MinGap: 3 * time.Millisecond,
		ReadTimeout: 2 * time.Second, FinalRead: 2 * time.Second,
		Authority: 5 * time.Second, Tail: 400 * time.Millisecond, MaxAge: 30 * time.Second,
	}
	return f
}

// hookUnder 以编辑器 hook 的身份给对话 conv 报一个事件，工作流取 wf（模拟 .cursor/relaymesh.json 里写的那个）。
func (f *execFixture) hookUnder(t *testing.T, wf, conv, name, status string) store.AgentIngestResult {
	t.Helper()
	now := time.Now()
	res, err := f.env.store.IngestAgentEvent(context.Background(), store.AgentIngestInput{
		WorkflowID: wf, CredentialID: 7, HostName: "exec-host", ProjectDirectory: "/work/app", ConversationID: conv,
		Event: agentstate.Event{Source: agentstate.SourceHook, Name: name, Status: status, At: now, Received: now},
	})
	if err != nil {
		t.Fatalf("hook %s: %v", name, err)
	}
	return res
}

func (f *execFixture) sessionsUnder(t *testing.T, wf string) []store.AgentSessionView {
	t.Helper()
	list, err := f.env.store.ListAgentSessions(context.Background(), store.AgentSessionFilter{WorkflowID: wf}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	return list
}

// claimOutLines 是新开对话的运行输出里的 init 行与一条思考，对话编号都是 claimConvID。
var claimOutLines = []string{
	fmt.Sprintf(`{"type":"system","subtype":"init","session_id":%q}`, claimConvID),
	fmt.Sprintf(`{"type":"thinking","subtype":"delta","session_id":%q}`, claimConvID),
}

// stateEventLog 记下 Hub 通知 Web 的所有 agent_state 事件。
type stateEventLog struct {
	mu  sync.Mutex
	evs []AgentStateEvent
}

func (l *stateEventLog) add(ev AgentStateEvent) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.evs = append(l.evs, ev)
}

// refreshes 返回对 wf 的「整体刷新」通知条数（没有 session_id 的事件，Web 收到会重新取一遍）。
func (l *stateEventLog) refreshes(wf string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	n := 0
	for _, ev := range l.evs {
		if ev.WorkflowID == wf && ev.SessionID == 0 {
			n++
		}
	}
	return n
}

func TestRunWatchClaimsALearnedConversationFromAWronglyBoundHook(t *testing.T) {
	f := claimFixture(t)
	ctx := context.Background()
	events := &stateEventLog{}
	f.env.hub.SetAgentStateHook(events.add)

	// 现场：新对话一开始，hook 就按目录配置把它报到了别的工作流名下——状态会话建在那里，旧链路的对话编号也被它写走了。
	f.hookUnder(t, claimHookWF, claimConvID, agentstate.EvSessionStart, "")
	f.hookUnder(t, claimHookWF, claimConvID, agentstate.EvPrompt, "")
	f.env.store.RecordAgentEvent(ctx, claimHookWF, store.AgentEvent{Kind: store.AgentEventTool, ConversationID: claimConvID}, time.Now())
	if len(f.sessionsUnder(t, claimHookWF)) != 1 || len(f.sessionsUnder(t, newConvWF)) != 0 {
		t.Fatal("setup: the session must start under the hook's workflow")
	}
	if got := f.env.store.AgentLinkOf(ctx, claimHookWF).ConversationID; got != claimConvID {
		t.Fatalf("setup: the hook's workflow link = %q", got)
	}

	// 发起运行（新开对话）：输出里的 init 带着同一个对话编号。
	out := &watchOut{f: f}
	f.sim.onRun = func(s *hostSim, m termproto.Run) {
		out.setID(m.ID)
		s.started(m.ID)
	}
	cmd, err := headlessCmd{Bin: "agent", New: true, Dir: "/work/app", Posture: store.HeadlessPostureForce, Prompt: "接入"}.build()
	if err != nil {
		t.Fatal(err)
	}
	res, err := f.svc.submitHeadless(ctx, headlessSubmit{
		Caller: launcherRunCaller, UserAgent: "test", Machine: f.a.code, Workflow: newConvWF,
		Cwd: "/work/app", Command: cmd, Description: "claim test", NewConversation: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, 3*time.Second, "假主机收到运行", func() bool { return out.ident() != "" })
	out.add(claimOutLines...)

	// 运行读到 init：认领对话——会话搬回发起运行的工作流，状态由运行说了算。
	var moved store.AgentSessionView
	eventually(t, 4*time.Second, "会话归到发起运行的工作流并显示运行的状态", func() bool {
		list := f.sessionsUnder(t, newConvWF)
		if len(list) != 1 {
			return false
		}
		moved = list[0]
		return moved.ConversationID == claimConvID && moved.State == "working" && moved.Sub == "thinking" && moved.StateSource == "run"
	})
	if got := f.sessionsUnder(t, claimHookWF); len(got) != 0 {
		t.Fatalf("hook 配置里的工作流名下不该留下幽灵会话: %+v", got)
	}
	// 旧链路：对话编号归发起运行的工作流，别人名下被写走的那份清掉（否则它的预设会去续聊别人的对话）。
	if got := f.env.store.AgentLinkOf(ctx, newConvWF).ConversationID; got != claimConvID {
		t.Fatalf("发起运行的工作流应记着学到的对话: %q", got)
	}
	if got := f.env.store.AgentLinkOf(ctx, claimHookWF).ConversationID; got != "" {
		t.Fatalf("hook 配置里的工作流不该留着别人的对话编号: %q", got)
	}
	// 网页：两边都要重新取一遍（有会话挪动时发的、没有 session_id 的通知）。
	eventually(t, 2*time.Second, "两边的网页都收到整体刷新通知", func() bool {
		return events.refreshes(newConvWF) >= 1 && events.refreshes(claimHookWF) >= 1
	})

	// 运行期间 hook 仍按旧配置汇报：只留记录，不改状态，更不会把会话改回去。
	r := f.hookUnder(t, claimHookWF, claimConvID, agentstate.EvThought, "")
	if r.Change.StateChanged || r.Change.Note != agentstate.NoteRunAuthority {
		t.Fatalf("运行期间 hook 的汇报应只留记录: %+v", r.Change)
	}
	if len(f.sessionsUnder(t, newConvWF)) != 1 || len(f.sessionsUnder(t, claimHookWF)) != 0 {
		t.Fatal("hook 的汇报不该把会话改回别的工作流")
	}

	out.add(lnResultOK)
	out.finishRun(termproto.RunExited, 0)
	eventually(t, 4*time.Second, "本轮结束回到空闲", func() bool {
		list := f.sessionsUnder(t, newConvWF)
		return len(list) == 1 && list[0].State == "idle" && list[0].StateSource == "run"
	})
	eventually(t, 2*time.Second, "监视收尾退出", func() bool { return !f.svc.watch.watching(res.RunID) })
}

// Hub 重启后接回运行（部署新版本就是一次重启）：监视从输出历史里重新学到对话、重新认领——已经卡在错误工作流里的会话自己归位。
func TestRunWatchRestoredRunHealsAWronglyBoundSession(t *testing.T) {
	f := claimFixture(t)
	ctx := context.Background()
	f.hookUnder(t, claimHookWF, claimConvID, agentstate.EvSessionStart, "")
	f.hookUnder(t, claimHookWF, claimConvID, agentstate.EvPrompt, "")

	out := &watchOut{f: f}
	f.sim.onRun = func(s *hostSim, m termproto.Run) {
		out.setID(m.ID)
		s.started(m.ID)
		out.add(claimOutLines...)
	}
	cmd, err := headlessCmd{Bin: "agent", New: true, Dir: "/work/app", Posture: store.HeadlessPostureForce, Prompt: "x"}.build()
	if err != nil {
		t.Fatal(err)
	}
	// 不带 NewConversation 提交：提交时不自动监视，模拟 Hub 重启前的监视已经没了。
	res, err := f.svc.submitHeadless(ctx, headlessSubmit{
		Caller: launcherRunCaller, UserAgent: "test", Machine: f.a.code, Workflow: newConvWF,
		Cwd: "/work/app", Command: cmd, Description: "restored claim",
	})
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, 2*time.Second, "假主机有了输出", func() bool { return out.ident() != "" && out.size() > 0 })
	if len(f.sessionsUnder(t, newConvWF)) != 0 || len(f.sessionsUnder(t, claimHookWF)) != 1 {
		t.Fatal("setup: the wrongly bound session must still be under the hook's workflow before the run is re-attached")
	}

	f.svc.watch.restore(*f.dbRun(t, res.RunID))
	eventually(t, 4*time.Second, "接回后会话归位并显示运行的状态", func() bool {
		list := f.sessionsUnder(t, newConvWF)
		return len(list) == 1 && list[0].ConversationID == claimConvID && list[0].State == "working" && list[0].StateSource == "run"
	})
	if got := f.sessionsUnder(t, claimHookWF); len(got) != 0 {
		t.Fatalf("接回后错误工作流名下不该再有这个会话: %+v", got)
	}
	out.finishRun(termproto.RunExited, 0)
	eventually(t, 2*time.Second, "监视收尾退出", func() bool { return !f.svc.watch.watching(res.RunID) })
}

// 认领与运行权威同生共死：运行收尾（Tail 过后）hook 恢复按自己带的工作流记——交互使用同一个对话时由 hook 说了算。
func TestRunWatchClaimEndsWithTheRun(t *testing.T) {
	f := claimFixture(t)
	f.hookUnder(t, newConvWF, claimConvID, agentstate.EvSessionStart, "")
	out := &watchOut{f: f}
	f.sim.onRun = func(s *hostSim, m termproto.Run) {
		out.setID(m.ID)
		s.started(m.ID)
	}
	cmd, err := headlessCmd{Bin: "agent", ChatID: claimConvID, Dir: "/work/app", Posture: store.HeadlessPostureForce, Prompt: "x"}.build()
	if err != nil {
		t.Fatal(err)
	}
	res, err := f.svc.submitHeadless(context.Background(), headlessSubmit{
		Caller: launcherRunCaller, UserAgent: "test", Machine: f.a.code, Workflow: newConvWF, Conversation: claimConvID,
		Cwd: "/work/app", Command: cmd, Description: "claim expires",
	})
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, 3*time.Second, "假主机收到运行", func() bool { return out.ident() != "" })
	out.add(claimOutLines...)
	eventually(t, 3*time.Second, "运行动过对话后被认领", func() bool {
		return f.env.store.ConversationOwner(claimConvID, time.Now()) == newConvWF
	})
	out.add(lnResultOK)
	out.finishRun(termproto.RunExited, 0)
	eventually(t, 2*time.Second, "监视收尾退出", func() bool { return !f.svc.watch.watching(res.RunID) })

	eventually(t, 3*time.Second, "收尾期过后认领失效", func() bool {
		return f.env.store.ConversationOwner(claimConvID, time.Now()) == ""
	})
}

// 启动就失败的运行（没有动过这个对话）不认领：它没有资格改对话的归属。
func TestRunWatchDoesNotClaimWhenTheRunNeverTouchedTheConversation(t *testing.T) {
	f := claimFixture(t)
	f.hookUnder(t, claimHookWF, claimConvID, agentstate.EvSessionStart, "")
	out := &watchOut{f: f}
	f.sim.onRun = func(s *hostSim, m termproto.Run) {
		out.setID(m.ID)
		s.started(m.ID)
	}
	cmd, err := headlessCmd{Bin: "agent", ChatID: claimConvID, Dir: "/work/app", Posture: store.HeadlessPostureForce, Prompt: "x"}.build()
	if err != nil {
		t.Fatal(err)
	}
	res, err := f.svc.submitHeadless(context.Background(), headlessSubmit{
		Caller: launcherRunCaller, UserAgent: "test", Machine: f.a.code, Workflow: newConvWF, Conversation: claimConvID,
		Cwd: "/work/app", Command: cmd, Description: "never touched",
	})
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, 3*time.Second, "假主机收到运行", func() bool { return out.ident() != "" })
	// 没有任何输出信号就结束了（命令不存在、没权限之类）：输出只有一行纯文本报错。
	out.add("agent: command not found")
	out.finishRun(termproto.RunExited, 127)
	eventually(t, 3*time.Second, "监视收尾退出", func() bool { return !f.svc.watch.watching(res.RunID) })

	if got := f.env.store.ConversationOwner(claimConvID, time.Now()); got != "" {
		t.Fatalf("没动过对话的运行不该认领它: %q", got)
	}
	if len(f.sessionsUnder(t, claimHookWF)) != 1 || len(f.sessionsUnder(t, newConvWF)) != 0 {
		t.Fatal("没动过对话的运行不该挪动任何会话")
	}
}

// 会话页「运行」找预设：工作流还没有智能体状态会话时（hook 没汇报过、汇报被记到了别处），目录与主机用工作流上记录的——
// 智能体在反馈会话里自报的那一份——预设里绑定的工作流本来就是这个会话。
func TestSessionLaunchersFallBackToTheWorkflowBindingWithoutAnAgentSession(t *testing.T) {
	const wf = "wf-bound"
	f := newExecFixture(t)
	if _, err := f.env.store.CreateFeedbackSession(context.Background(), store.CreateSessionInput{
		SessionID: "s-bound", WorkflowID: wf, EnvHostName: "exec-host", ProjectDirectory: "/work/app", Title: "t", Summary: "s",
	}); err != nil {
		t.Fatal(err)
	}
	enable := map[string]any{"workflow_id": wf, "new_conversation": true, "posture": "force"}
	f.saveLauncherAt(t, "p1", "启用 Agent", f.a.code, "/work/app", enable)
	f.saveLauncherAt(t, "p2", "别的工作流", f.a.code, "/work/app", map[string]any{"workflow_id": "wf-other-bound", "new_conversation": true, "posture": "force"})
	f.saveLauncherAt(t, "p3", "别的目录", f.a.code, "/work/elsewhere", enable)

	v := f.sessionLaunchers(t, wf)
	wantIDs(t, v, "p1")
	if v.Dir != "/work/app" || v.SessionHost != "exec-host" || v.Host != f.a.code {
		t.Fatalf("目标应取自工作流上记录的主机与目录: %+v", v)
	}

	// 会话页「运行」的预览用同一份兜底：不再说「不知道在哪个目录」。
	pv := f.sessionPreview(sessionRunRequest{WorkflowID: wf, ConversationID: "chat-bound"})
	for _, i := range pv.Issues {
		if i.Code == "no_dir" {
			t.Fatalf("有记录的目录时不该报 no_dir: %+v", pv.Issues)
		}
	}
	if pv.Dir != "/work/app" || pv.SessionHost != "exec-host" {
		t.Fatalf("预览的目录与主机不对: %+v", pv)
	}
	// 请求自己指定了别的目录：以请求为准，也不采用那条记录的主机。
	pv = f.sessionPreview(sessionRunRequest{WorkflowID: wf, ConversationID: "chat-bound", Dir: "/work/elsewhere"})
	if pv.Dir != "/work/elsewhere" || pv.SessionHost != "" {
		t.Fatalf("请求指定的目录优先，且不采用记录里的主机: %+v", pv)
	}
}

func TestSessionLaunchersWithoutAnyRecordStillSayWhy(t *testing.T) {
	f := newExecFixture(t)
	f.saveLauncherAt(t, "p1", "启用 Agent", f.a.code, "/work/app", map[string]any{"workflow_id": "wf-nothing", "new_conversation": true, "posture": "force"})
	v := f.sessionLaunchers(t, "wf-nothing")
	wantIDs(t, v)
	if !strings.Contains(v.Reason, "没有上报项目目录") {
		t.Fatalf("没有任何记录时仍应说明原因: %+v", v)
	}
}

// 兜底得来的主机也要参与筛选：记录里的主机对不上预设的主机，就不是这个会话的预设。
func TestSessionLaunchersBindingHostStillFiltersPresets(t *testing.T) {
	const wf = "wf-bound-host"
	f := newExecFixture(t)
	if _, err := f.env.store.CreateFeedbackSession(context.Background(), store.CreateSessionInput{
		SessionID: "s-bound-host", WorkflowID: wf, EnvHostName: "some-other-box", ProjectDirectory: "/work/app", Title: "t", Summary: "s",
	}); err != nil {
		t.Fatal(err)
	}
	f.saveLauncherAt(t, "p1", "启用 Agent", f.a.code, "/work/app", map[string]any{"workflow_id": wf, "new_conversation": true, "posture": "force"})
	v := f.sessionLaunchers(t, wf)
	wantIDs(t, v)
	if v.SessionHost != "some-other-box" {
		t.Fatalf("记录里的主机名应当原样带出: %+v", v)
	}
}
