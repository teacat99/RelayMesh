package terminal

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/teacat99/RelayMesh/v2/internal/agentstate"
	"github.com/teacat99/RelayMesh/v2/internal/store"
	"github.com/teacat99/RelayMesh/v2/internal/termproto"
)

// 托管终端屏幕信号的 Hub 侧消费者（#113 M3.2）。

type screenSink struct {
	mu   sync.Mutex
	list []ScreenEvent
}

func (s *screenSink) hook(ev ScreenEvent) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.list = append(s.list, ev)
}

func (s *screenSink) all() []ScreenEvent {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]ScreenEvent(nil), s.list...)
}

const verifiedCursor = "2026.10.12-d47ca18"

// screenEnv 是一台在线主机 + 一个带守护配置的预设（工作流 wf-a），工作流里已有一次带版本号的 hook 事件。
func newScreenEnv(t *testing.T, cursorVersion string) (*guardEnv, *screenSink) {
	t.Helper()
	g := newGuardEnv(t)
	sink := &screenSink{}
	g.hub.SetScreenHook(sink.hook)
	g.savePreset("p1", guardPresetName, map[string]any{"enabled": true, "workflow_id": "wf-a"})
	g.online()
	now := g.clock.Now()
	_, err := g.store.IngestAgentEvent(context.Background(), store.AgentIngestInput{
		WorkflowID: "wf-a", CredentialID: 1, HostName: "devhost", ProjectDirectory: "/home/user/RelayMesh",
		ConversationID: "conv-1", ClientKind: "cli", CursorVersion: cursorVersion,
		Event: agentstate.Event{Source: agentstate.SourceHook, Name: agentstate.EvPrompt, At: now, Received: now},
	})
	if err != nil {
		t.Fatal(err)
	}
	return g, sink
}

func (g *guardEnv) report(sessions ...termproto.SessionInfo) {
	g.t.Helper()
	g.agent.setSessions(sessions)
}

func screenSession(sid string, sc *termproto.Screen) termproto.SessionInfo {
	return termproto.SessionInfo{SID: sid, Name: guardPresetName, State: termproto.SessionRunning, Created: 1_700_000_000, Screen: sc}
}

func animating(word string, tokens int) *termproto.Screen {
	return &termproto.Screen{Word: word, Tokens: tokens, CtxPct: 8, Animating: true, BPS: 300, Quiet: 0}
}

func approving() *termproto.Screen {
	return &termproto.Screen{Tokens: -1, CtxPct: 8, Approval: true, Quiet: 4}
}

func (g *guardEnv) sessionView(t *testing.T) store.AgentSessionView {
	t.Helper()
	v, err := g.store.ListAgentSessions(context.Background(), store.AgentSessionFilter{WorkflowID: "wf-a"}, g.clock.Now())
	if err != nil || len(v) != 1 {
		t.Fatalf("ListAgentSessions: %+v err=%v", v, err)
	}
	return v[0]
}

func TestScreen_ApprovalLifecycle(t *testing.T) {
	g, sink := newScreenEnv(t, verifiedCursor)
	sid := sidOf('a')

	g.report(screenSession(sid, animating("Running", 100)))
	if n := len(sink.all()); n != 0 {
		t.Fatalf("an animating screen is not an event: %+v", sink.all())
	}
	v := g.sessionView(t)
	if v.TUI != "ok" || v.TokensLive == nil || *v.TokensLive != 100 || v.LiveWord != "Running" {
		t.Fatalf("live fields missing: %+v", v)
	}

	g.clock.Advance(2 * time.Second)
	g.report(screenSession(sid, approving()))
	evs := sink.all()
	if len(evs) != 1 || !evs[0].Approval || evs[0].WorkflowID != "wf-a" || evs[0].Name != guardPresetName || evs[0].Episode == "" {
		t.Fatalf("approval start event: %+v", evs)
	}
	if evs[0].IdentityID == 0 || evs[0].State != string(agentstate.StateWaitingApproval) {
		t.Fatalf("event should carry the recorded state: %+v", evs[0])
	}
	if v = g.sessionView(t); v.State != string(agentstate.StateWaitingApproval) {
		t.Fatalf("session should show waiting_approval: %+v", v)
	}

	// 同一次审批等待的重复上报不再产生事件。
	g.clock.Advance(20 * time.Second)
	g.report(screenSession(sid, approving()))
	if n := len(sink.all()); n != 1 {
		t.Fatalf("repeat report produced %d events", n)
	}

	g.clock.Advance(time.Second)
	g.report(screenSession(sid, animating("Running", 120)))
	evs = sink.all()
	if len(evs) != 2 || evs[1].Approval || evs[1].Episode != evs[0].Episode {
		t.Fatalf("approval end event: %+v", evs)
	}
	if v = g.sessionView(t); v.State != string(agentstate.StateWorking) {
		t.Fatalf("session should resume working: %+v", v)
	}
}

func TestScreen_SessionGoneWhileWaitingEndsApproval(t *testing.T) {
	g, sink := newScreenEnv(t, verifiedCursor)
	sid := sidOf('b')
	g.report(screenSession(sid, approving()))
	g.clock.Advance(time.Second)
	g.report() // 会话没了（退出 / 被回收）
	evs := sink.all()
	if len(evs) != 2 || !evs[0].Approval || evs[1].Approval {
		t.Fatalf("a vanished session must close the approval: %+v", evs)
	}
	if v := g.sessionView(t); v.State == string(agentstate.StateWaitingApproval) {
		t.Fatalf("state must not stay in waiting_approval: %+v", v)
	}
	if v := g.sessionView(t); v.TUI != "" {
		t.Fatalf("screen signal should be cleared: %+v", v)
	}
}

func TestScreen_HostOfflineClearsSignal(t *testing.T) {
	g, sink := newScreenEnv(t, verifiedCursor)
	g.report(screenSession(sidOf('c'), approving()))
	g.offline()
	g.hub.screens.observe(g.agent, nil) // cleanup 在主机断线后做的事
	evs := sink.all()
	if len(evs) != 2 || evs[1].Approval {
		t.Fatalf("offline must close the approval: %+v", evs)
	}
}

func TestScreen_UnverifiedVersionIsInert(t *testing.T) {
	for _, ver := range []string{"", "2027.01.01-0000000"} {
		g, sink := newScreenEnv(t, ver)
		g.report(screenSession(sidOf('d'), approving()))
		if n := len(sink.all()); n != 0 {
			t.Fatalf("version %q: unverified TUI must not raise approval: %+v", ver, sink.all())
		}
		v := g.sessionView(t)
		if v.TUI != "unrecognized" || v.TokensLive != nil || v.State == string(agentstate.StateWaitingApproval) {
			t.Fatalf("version %q: should degrade to unrecognized: %+v", ver, v)
		}
	}
}

func TestScreen_PresetWithoutWorkflowOrOtherNameIsIgnored(t *testing.T) {
	g, sink := newScreenEnv(t, verifiedCursor)
	other := screenSession(sidOf('e'), approving())
	other.Name = "some other terminal"
	g.report(other)
	if n := len(sink.all()); n != 0 {
		t.Fatalf("a session that matches no preset must be ignored: %+v", sink.all())
	}
	if v := g.sessionView(t); v.TUI != "" {
		t.Fatalf("no signal expected: %+v", v)
	}
}

func TestScreen_OldAgentWithoutScreenFieldIsQuiet(t *testing.T) {
	g, sink := newScreenEnv(t, verifiedCursor)
	g.report(screenSession(sidOf('f'), nil))
	if len(sink.all()) != 0 {
		t.Fatalf("old agents never report Screen: %+v", sink.all())
	}
	if v := g.sessionView(t); v.TUI != "" || v.State == string(agentstate.StateWaitingApproval) {
		t.Fatalf("unexpected: %+v", v)
	}
}

func TestScreen_UnknownVersionWithoutHookIsInert(t *testing.T) {
	g := newGuardEnv(t)
	sink := &screenSink{}
	g.hub.SetScreenHook(sink.hook)
	g.savePreset("p1", guardPresetName, map[string]any{"enabled": true, "workflow_id": "wf-solo"})
	g.online()
	// 工作流没有任何 hook 事件：版本未知，屏幕信号不被采信（不通知）。
	g.report(screenSession(sidOf('9'), approving()))
	if n := len(sink.all()); n != 0 {
		t.Fatalf("unknown version → inert: %+v", sink.all())
	}
}

func TestScreen_HookPanicIsContained(t *testing.T) {
	g, _ := newScreenEnv(t, verifiedCursor)
	g.hub.SetScreenHook(func(ScreenEvent) { panic("boom") })
	g.report(screenSession(sidOf('7'), approving())) // 不能把清单处理协程带崩
	if !strings.Contains(string(g.sessionView(t).State), "approval") {
		t.Fatalf("state should still be recorded: %+v", g.sessionView(t))
	}
}

func TestScreen_RemindersAreSparseAndStopWhenResolved(t *testing.T) {
	g, sink := newScreenEnv(t, verifiedCursor)
	sid := sidOf('8')
	g.report(screenSession(sid, approving()))
	start := g.clock.Now()
	remind := func(after time.Duration) { g.hub.screens.remind(start.Add(after)) }
	count := func() (reminders int) {
		for _, ev := range sink.all() {
			if ev.Reminder > 0 {
				reminders++
			}
		}
		return
	}

	remind(4*time.Minute + 59*time.Second)
	if count() != 0 {
		t.Fatal("too early for the first reminder")
	}
	remind(5 * time.Minute)
	remind(5*time.Minute + 30*time.Second) // 同一轮窗口内不重复
	if count() != 1 {
		t.Fatalf("expected exactly one reminder at 5 minutes, got %d", count())
	}
	remind(29 * time.Minute)
	if count() != 1 {
		t.Fatal("second reminder must wait until 30 minutes")
	}
	remind(30 * time.Minute)
	remind(10 * time.Hour)
	if count() != 2 {
		t.Fatalf("reminders are capped at two, got %d", count())
	}
	for _, ev := range sink.all() {
		if ev.Reminder > 0 && (!ev.Approval || ev.Episode == "" || ev.WorkflowID != "wf-a") {
			t.Fatalf("bad reminder event: %+v", ev)
		}
	}

	// 处理掉之后不再提醒；下一次审批等待重新计数。
	g.clock.Advance(time.Hour)
	g.report(screenSession(sid, animating("Running", 5)))
	before := len(sink.all())
	g.hub.screens.remind(g.clock.Now().Add(10 * time.Hour))
	if len(sink.all()) != before {
		t.Fatal("no reminders once the approval is over")
	}
}
