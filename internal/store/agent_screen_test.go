package store

import (
	"context"
	"testing"
	"time"

	"github.com/teacat99/RelayMesh/v2/internal/agentstate"
)

func screenIn(mod func(*AgentScreenInput)) AgentScreenInput {
	in := AgentScreenInput{Verified: true, Recognized: true, CursorVersion: "2026.10.12-d47ca18", Tokens: -1, CtxPct: -1, QuietSeconds: 0}
	if mod != nil {
		mod(&in)
	}
	return in
}

func TestSetAgentScreen_ApprovalTransitions(t *testing.T) {
	st := newMemStore(t)
	now := agentBase
	if ch := st.SetAgentScreen("wf-a", screenIn(func(i *AgentScreenInput) { i.Animating = true; i.Word = "Running" }), now); ch.ApprovalStarted || ch.ApprovalEnded {
		t.Fatalf("no approval yet: %+v", ch)
	}
	ch := st.SetAgentScreen("wf-a", screenIn(func(i *AgentScreenInput) { i.Approval = true; i.QuietSeconds = 3 }), now.Add(time.Second))
	if !ch.ApprovalStarted || !ch.Approval {
		t.Fatalf("approval should start: %+v", ch)
	}
	// 重复上报同一状态：不再算「刚开始」。
	if ch = st.SetAgentScreen("wf-a", screenIn(func(i *AgentScreenInput) { i.Approval = true; i.QuietSeconds = 20 }), now.Add(20*time.Second)); ch.ApprovalStarted || !ch.Approval {
		t.Fatalf("repeat report: %+v", ch)
	}
	if ch = st.SetAgentScreen("wf-a", screenIn(func(i *AgentScreenInput) { i.Animating = true; i.Word = "Running" }), now.Add(30*time.Second)); !ch.ApprovalEnded || ch.Approval {
		t.Fatalf("approval should end: %+v", ch)
	}
}

func TestSetAgentScreen_UnverifiedVersionNeverReportsApproval(t *testing.T) {
	st := newMemStore(t)
	ch := st.SetAgentScreen("wf-a", screenIn(func(i *AgentScreenInput) {
		i.Verified = false
		i.Approval = true
		i.Animating = true
		i.Word = "Working"
		i.Tokens = 50
		i.CtxPct = 5
	}), agentBase)
	if ch.ApprovalStarted || ch.Approval {
		t.Fatalf("unverified screen must not raise approval: %+v", ch)
	}
	v := st.agentScreenOf("wf-a", agentBase.Add(time.Second))
	if v == nil || v.TUI != "unrecognized" || v.Approval || v.Tokens != -1 || v.CtxPct != -1 || v.Word != "" {
		t.Fatalf("unverified view should be degraded: %+v", v)
	}
}

func TestAgentScreenOf_TTLAndAnimationStale(t *testing.T) {
	st := newMemStore(t)
	st.SetAgentScreen("wf-a", screenIn(func(i *AgentScreenInput) { i.Animating = true; i.Word = "Thinking"; i.Tokens = 40; i.CtxPct = 12.5 }), agentBase)
	v := st.agentScreenOf("wf-a", agentBase.Add(5*time.Second))
	if v == nil || v.TUI != "ok" || !v.Animating || v.Word != "Thinking" || v.Tokens != 40 || v.TokensSrc != "tui" || v.CtxPct != 12.5 {
		t.Fatalf("fresh view: %+v", v)
	}
	// 动画中的上报超过 90 秒没有刷新：守护进程可能断了，不能再说「正在动画」。
	v = st.agentScreenOf("wf-a", agentBase.Add(2*time.Minute))
	if v == nil || v.Animating || v.Word != "" || v.signal.Animating {
		t.Fatalf("stale animation must be dropped: %+v", v)
	}
	if v.Tokens != 40 {
		t.Fatalf("last known tokens should still be shown: %+v", v)
	}
	// 很久之后整体过期。
	if st.agentScreenOf("wf-a", agentBase.Add(31*time.Minute)) != nil {
		t.Fatal("expired signal must disappear")
	}
	if st.agentScreenOf("other", agentBase) != nil {
		t.Fatal("unknown workflow has no screen")
	}
}

func TestSetAgentScreen_TokensSinceTracksChanges(t *testing.T) {
	st := newMemStore(t)
	st.SetAgentScreen("wf-a", screenIn(func(i *AgentScreenInput) { i.Animating = true; i.Tokens = 10 }), agentBase)
	st.SetAgentScreen("wf-a", screenIn(func(i *AgentScreenInput) { i.Animating = true; i.Tokens = 10 }), agentBase.Add(10*time.Second))
	st.SetAgentScreen("wf-a", screenIn(func(i *AgentScreenInput) { i.Animating = true; i.Tokens = 10 }), agentBase.Add(20*time.Second))
	v := st.agentScreenOf("wf-a", agentBase.Add(21*time.Second))
	if !v.signal.TokensSince.Equal(agentBase) {
		t.Fatalf("unchanged tokens keep the original since: %v", v.signal.TokensSince)
	}
	st.SetAgentScreen("wf-a", screenIn(func(i *AgentScreenInput) { i.Animating = true; i.Tokens = 25 }), agentBase.Add(30*time.Second))
	v = st.agentScreenOf("wf-a", agentBase.Add(31*time.Second))
	if !v.signal.TokensSince.Equal(agentBase.Add(30 * time.Second)) {
		t.Fatalf("changed tokens reset since: %v", v.signal.TokensSince)
	}
}

func TestClearAgentScreen(t *testing.T) {
	st := newMemStore(t)
	st.SetAgentScreen("wf-a", screenIn(func(i *AgentScreenInput) { i.Approval = true }), agentBase)
	if !st.ClearAgentScreen("wf-a") {
		t.Fatal("should report it was waiting for approval")
	}
	if st.ClearAgentScreen("wf-a") || st.agentScreenOf("wf-a", agentBase) != nil {
		t.Fatal("already cleared")
	}
}

func TestIngestLatest_AttachesToTheHookSession(t *testing.T) {
	st := newMemStore(t)
	ctx := context.Background()
	mustIngest(t, st, agentIn(agentEv(0, agentstate.EvSessionStart)))
	mustIngest(t, st, agentIn(agentEv(100, agentstate.EvPrompt)))

	t1 := agentBase.Add(time.Second)
	res, err := st.IngestAgentEvent(ctx, AgentIngestInput{
		WorkflowID: "wf-a", Latest: true,
		Event: agentstate.Event{Source: agentstate.SourceTUI, Name: agentstate.EvApprovalWait, Received: t1},
	})
	if err != nil || res.IdentityID == 0 || res.Snapshot.State != agentstate.StateWaitingApproval {
		t.Fatalf("approval_wait should land on the hook session: %+v err=%v", res, err)
	}
	views, _ := st.ListAgentSessions(ctx, AgentSessionFilter{WorkflowID: "wf-a"}, t1.Add(time.Second))
	if len(views) != 1 || views[0].State != string(agentstate.StateWaitingApproval) || views[0].ConversationID != "conv-1" {
		t.Fatalf("exactly one session in waiting_approval expected: %+v", views)
	}

	res, _ = st.IngestAgentEvent(ctx, AgentIngestInput{
		WorkflowID: "wf-a", Latest: true,
		Event: agentstate.Event{Source: agentstate.SourceTUI, Name: agentstate.EvApprovalDone, Received: t1.Add(5 * time.Second)},
	})
	if res.Snapshot.State != agentstate.StateWorking {
		t.Fatalf("approval_done should resume working: %+v", res.Snapshot)
	}
}

func TestIngestLatest_NoSessionRecordsNothing(t *testing.T) {
	st := newMemStore(t)
	res, err := st.IngestAgentEvent(context.Background(), AgentIngestInput{
		WorkflowID: "wf-none", Latest: true,
		Event: agentstate.Event{Source: agentstate.SourceTUI, Name: agentstate.EvApprovalWait, Received: agentBase},
	})
	if err != nil || res.IdentityID != 0 || res.SessionID != 0 {
		t.Fatalf("must not invent an identity: %+v err=%v", res, err)
	}
}

func TestIngestLatest_SkipsEndedSessionsAndRestoresFromDB(t *testing.T) {
	st := newMemStore(t)
	ctx := context.Background()
	mustIngest(t, st, agentIn(agentEv(0, agentstate.EvPrompt)))
	st.agent = agentStateMem{} // 模拟重启：内存清空，库还在
	res, err := st.IngestAgentEvent(ctx, AgentIngestInput{
		WorkflowID: "wf-a", Latest: true,
		Event: agentstate.Event{Source: agentstate.SourceTUI, Name: agentstate.EvApprovalWait, Received: agentBase.Add(time.Second)},
	})
	if err != nil || res.IdentityID == 0 || res.NewSession {
		t.Fatalf("should restore the session from the database: %+v err=%v", res, err)
	}
	end := agentEv(2000, agentstate.EvSessionEnd)
	end.Status = agentstate.EndCompleted
	mustIngest(t, st, agentIn(end))
	res, _ = st.IngestAgentEvent(ctx, AgentIngestInput{
		WorkflowID: "wf-a", Latest: true,
		Event: agentstate.Event{Source: agentstate.SourceTUI, Name: agentstate.EvApprovalWait, Received: agentBase.Add(3 * time.Second)},
	})
	if res.IdentityID != 0 {
		t.Fatalf("an ended session must not receive screen events: %+v", res)
	}
}

func TestListAgentSessions_ScreenSignalDrivesStallAndFields(t *testing.T) {
	st := newMemStore(t)
	ctx := context.Background()
	mustIngest(t, st, agentIn(agentEv(0, agentstate.EvPrompt)))
	base := agentBase

	// 冻结：状态是 working/thinking，屏幕不动 5 分钟，hook 静默也就 5 分钟（纯 hook 规则不会报）。
	st.SetAgentScreen("wf-a", screenIn(func(i *AgentScreenInput) { i.Tokens = 120; i.CtxPct = 33; i.Animating = false; i.QuietSeconds = 2 }), base.Add(10*time.Second))
	v, _ := st.ListAgentSessions(ctx, AgentSessionFilter{}, base.Add(5*time.Minute))
	if len(v) != 1 || !v[0].Stalled || v[0].Reason != agentstate.ReasonTerminalFrozen {
		t.Fatalf("frozen terminal should be stalled: %+v", v)
	}
	if v[0].TUI != "ok" || v[0].TokensLive == nil || *v[0].TokensLive != 120 || v[0].TokensSource != "tui" || v[0].CtxPct == nil || *v[0].CtxPct != 33 {
		t.Fatalf("screen fields missing: %+v", v[0])
	}

	// 版本未验证：不采信，字段只剩 unrecognized，卡住判断退回 hook 规则（5 分钟不算卡住）。
	st.SetAgentScreen("wf-a", screenIn(func(i *AgentScreenInput) { i.Verified = false }), base.Add(10*time.Second))
	v, _ = st.ListAgentSessions(ctx, AgentSessionFilter{}, base.Add(5*time.Minute))
	if v[0].Stalled || v[0].StalledSoft || v[0].TUI != "unrecognized" || v[0].TokensLive != nil || v[0].CtxPct != nil {
		t.Fatalf("unverified screen must be inert: %+v", v[0])
	}

	// 没有信号：没有 tui 字段。
	st.ClearAgentScreen("wf-a")
	v, _ = st.ListAgentSessions(ctx, AgentSessionFilter{}, base.Add(5*time.Minute))
	if v[0].TUI != "" || v[0].TokensLive != nil {
		t.Fatalf("no screen: %+v", v[0])
	}
}
