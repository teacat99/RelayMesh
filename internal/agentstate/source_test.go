package agentstate

import (
	"testing"
	"time"
)

// runEv 造一个来自运行输出的事件（Hub 自己读到的，没有发送端时间）。
func runEv(ms int, name, tool, status string) Event {
	return Event{Source: SourceRun, Name: name, Tool: tool, Status: status, Received: at(ms)}
}

func hookEv(ms int, name, tool, status string) Event {
	return Event{Source: SourceHook, Name: name, Tool: tool, Status: status, Received: at(ms), At: at(ms)}
}

func TestApplyRecordsStatusSource(t *testing.T) {
	cfg := DefaultConfig()
	var s Snapshot

	// 运行输出推进的状态，来源是 run。
	s, ch := Apply(s, runEv(0, EvPrompt, "", ""), cfg)
	if s.Source != SourceRun || !ch.StateChanged || !ch.Durable {
		t.Fatalf("prompt from run: source=%q changed=%v durable=%v", s.Source, ch.StateChanged, ch.Durable)
	}
	s, _ = Apply(s, runEv(1000, EvToolStart, "Shell", ""), cfg)
	s, _ = Apply(s, runEv(5000, EvToolEnd, "Shell", ""), cfg)
	s, ch = Apply(s, runEv(6000, EvStop, "", StopCompleted), cfg)
	if s.State != StateIdle || s.Source != SourceRun {
		t.Fatalf("after run stop: state=%s source=%q", s.State, s.Source)
	}

	// 之后 hook 说话（交互使用），来源回到 hook；换来源本身也要写库。
	s, ch = Apply(s, hookEv(60_000, EvPrompt, "", ""), cfg)
	if s.Source != SourceHook || !ch.Durable {
		t.Fatalf("hook prompt: source=%q durable=%v", s.Source, ch.Durable)
	}
}

func TestApplySourceIgnoresHubSideSourcesAndNonEffectiveEvents(t *testing.T) {
	cfg := DefaultConfig()
	s, _ := Apply(Snapshot{}, runEv(0, EvPrompt, "", ""), cfg)

	// MCP / 守护 / Web 是旁证，不抢「谁说的」。
	for _, src := range []Source{SourceMCP, SourceGuard, SourceWeb} {
		e := Event{Source: src, Name: EvThought, Received: at(1000)}
		s2, _ := Apply(s, e, cfg)
		if s2.Source != SourceRun {
			t.Fatalf("source %s must not take over the status source, got %q", src, s2.Source)
		}
	}

	// 重复事件（去重窗口内）与迟到事件没有作用在状态上，也不换来源。
	s, _ = Apply(s, runEv(2000, EvToolStart, "Read", ""), cfg)
	dup := hookEv(2000, EvToolStart, "Read", "")
	dup.Received = at(2000)
	if s2, ch := Apply(s, dup, cfg); ch.Note != NoteDuplicate || s2.Source != SourceRun {
		t.Fatalf("duplicate: note=%q source=%q", ch.Note, s2.Source)
	}
	late := hookEv(100, EvThought, "", "") // 比最近事件早，但不到回拨窗口：算乱序迟到
	late.Received = at(2100)
	if s2, ch := Apply(s, late, cfg); ch.Note != NoteLate || s2.Source != SourceRun {
		t.Fatalf("late: note=%q source=%q", ch.Note, s2.Source)
	}

	// 会话结束之后的收尾事件并入「结束的尾巴」，也不改来源。
	end := Event{Source: SourceHook, Name: EvSessionEnd, Status: EndClosed, Received: at(3000), At: at(3000)}
	ended, _ := Apply(s, end, cfg)
	tail, ch := Apply(ended, runEv(4000, EvThought, "", ""), cfg)
	if ch.Note != NoteEndedTail {
		t.Fatalf("tail note=%q", ch.Note)
	}
	if tail.Source != ended.Source {
		t.Fatalf("ended tail must not change the source: %q -> %q", ended.Source, tail.Source)
	}
}

func TestApplyRunSequenceMatchesStreamJSONRhythm(t *testing.T) {
	// 一次典型运行：init → 思考 → 工具开始 → 工具结束 → 回复 → result。
	cfg := DefaultConfig()
	steps := []struct {
		ev   Event
		want string
	}{
		{runEv(0, EvPrompt, "", ""), "working/thinking"},
		{runEv(500, EvThought, "", ""), "working/thinking"},
		{runEv(1000, EvToolStart, "Shell", ""), "working/tool:Shell"},
		{runEv(21_000, EvToolEnd, "Shell", ""), "working/thinking"},
		{runEv(22_000, EvResponse, "", ""), "working/responding"},
		{runEv(23_000, EvStop, "", StopCompleted), "idle"},
	}
	var s Snapshot
	for i, st := range steps {
		s, _ = Apply(s, st.ev, cfg)
		if got := stateStr(s); got != st.want {
			t.Fatalf("step %d (%s): got %s want %s", i, st.ev.Name, got, st.want)
		}
		if s.Source != SourceRun {
			t.Fatalf("step %d: source %q", i, s.Source)
		}
	}
	if s.TurnStartedAt.IsZero() || s.TurnEndedAt.IsZero() {
		t.Fatalf("turn window missing: %v..%v", s.TurnStartedAt, s.TurnEndedAt)
	}
}

func TestApplyRunPromptRevivesEndedConversation(t *testing.T) {
	// 对话在编辑器里被关掉（ended）之后，非交互续聊重新把它拉起来。
	cfg := DefaultConfig()
	s, _ := Apply(Snapshot{}, hookEv(0, EvSessionStart, "", ""), cfg)
	s, _ = Apply(s, Event{Source: SourceHook, Name: EvSessionEnd, Status: EndClosed, Received: at(1000), At: at(1000)}, cfg)
	if s.State != StateEnded {
		t.Fatalf("setup: %s", s.State)
	}
	s, ch := Apply(s, runEv(120_000, EvPrompt, "", ""), cfg)
	if s.State != StateWorking || ch.Note != NoteRevived || s.Source != SourceRun {
		t.Fatalf("revive: state=%s note=%q source=%q", s.State, ch.Note, s.Source)
	}
	if !s.EndedAt.IsZero() {
		t.Fatalf("revived session must clear EndedAt, got %v", s.EndedAt)
	}
}

func TestEvaluateRunSourceNeverSuspectsApproval(t *testing.T) {
	th := DefaultThresholds()
	now := t0.Add(time.Hour)
	mk := func(src Source, ran time.Duration) EvalInput {
		s := working("tool:Shell", now.Add(-ran-time.Second), now.Add(-ran))
		s.ToolStartedAt = now.Add(-ran)
		s.Source = src
		return EvalInput{Snap: s, Now: now}
	}

	// hook 来源：工具跑过 20 秒，纯 hook 只能怀疑它在等批准。
	if v := Evaluate(mk(SourceHook, 45*time.Second), th); !v.ApprovalSuspected || v.Reason != ReasonToolRunning {
		t.Fatalf("hook source: %+v", v)
	}
	// 运行输出来源：非交互运行没人能批准，跑得久只是在跑。
	if v := Evaluate(mk(SourceRun, 45*time.Second), th); v.ApprovalSuspected || v.Reason != "" || v.Stalled {
		t.Fatalf("run source must not suspect approval: %+v", v)
	}
	// 但超过工具类型上限照样算卡住（shell 是长工具，60 分钟）。
	if v := Evaluate(mk(SourceRun, 61*time.Minute), th); !v.Stalled || v.Reason != ReasonToolOverrun {
		t.Fatalf("run source overrun: %+v", v)
	}
}

func TestDurableEqualCoversSource(t *testing.T) {
	a := Snapshot{State: StateIdle, Since: t0, Source: SourceHook}
	b := a
	if !durableEqual(a, b) {
		t.Fatal("identical snapshots must be equal")
	}
	b.Source = SourceRun
	if durableEqual(a, b) {
		t.Fatal("a source change must count as durable")
	}
}
