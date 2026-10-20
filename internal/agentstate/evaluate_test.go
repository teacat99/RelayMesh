package agentstate

import (
	"testing"
	"time"
)

func working(sub string, since, lastEvent time.Time) Snapshot {
	return Snapshot{State: StateWorking, Sub: sub, Since: since, LastEventAt: lastEvent, StartedAt: since}
}

func TestEvaluateTable(t *testing.T) {
	th := DefaultThresholds()
	now := t0.Add(10 * time.Hour)
	ago := func(d time.Duration) time.Time { return now.Add(-d) }

	cases := []struct {
		name string
		in   EvalInput
		want View // 只比较关心的字段
	}{
		{"never_seen", EvalInput{Snap: Snapshot{}, Now: now}, View{}},
		{"idle_recent", EvalInput{Snap: Snapshot{State: StateIdle, Since: ago(time.Minute), LastEventAt: ago(time.Minute)}, Now: now},
			View{State: StateIdle, Base: StateIdle}},
		{"idle_quiet_but_not_offline", EvalInput{Snap: Snapshot{State: StateIdle, LastEventAt: ago(time.Hour + 59*time.Minute)}, Now: now},
			View{State: StateIdle, Base: StateIdle}},
		{"idle_offline_after_2h", EvalInput{Snap: Snapshot{State: StateIdle, LastEventAt: ago(2 * time.Hour)}, Now: now},
			View{State: StateOffline, Base: StateIdle}},
		{"idle_with_live_terminal_is_not_offline", EvalInput{Snap: Snapshot{State: StateIdle, LastEventAt: ago(5 * time.Hour)}, Now: now, HasLiveTerminal: true},
			View{State: StateIdle, Base: StateIdle}},
		{"ended_is_never_offline", EvalInput{Snap: Snapshot{State: StateEnded, LastEventAt: ago(72 * time.Hour)}, Now: now},
			View{State: StateEnded, Base: StateEnded}},
		{"ended_ignores_waiting_you", EvalInput{Snap: Snapshot{State: StateEnded, LastEventAt: ago(time.Minute)}, Now: now, WaitingYou: true},
			View{State: StateEnded, Base: StateEnded}},

		{"thinking_just_under_soft", EvalInput{Snap: working(SubThinking, ago(time.Hour), ago(9*time.Minute+59*time.Second)), Now: now},
			View{State: StateWorking, Base: StateWorking, Sub: SubThinking}},
		{"thinking_soft_at_10m", EvalInput{Snap: working(SubThinking, ago(time.Hour), ago(10*time.Minute)), Now: now},
			View{State: StateWorking, Base: StateWorking, Sub: SubThinking, StalledSoft: true, Reason: ReasonQuietSoft}},
		{"thinking_soft_just_under_hard", EvalInput{Snap: working(SubThinking, ago(time.Hour), ago(29*time.Minute+59*time.Second)), Now: now},
			View{State: StateWorking, Base: StateWorking, Sub: SubThinking, StalledSoft: true, Reason: ReasonQuietSoft}},
		{"thinking_hard_at_30m", EvalInput{Snap: working(SubThinking, ago(2*time.Hour), ago(30*time.Minute)), Now: now},
			View{State: StateWorking, Base: StateWorking, Sub: SubThinking, Stalled: true, Reason: ReasonQuiet}},
		{"responding_hard_at_30m", EvalInput{Snap: working(SubResponding, ago(2*time.Hour), ago(31*time.Minute)), Now: now},
			View{State: StateWorking, Base: StateWorking, Sub: SubResponding, Stalled: true, Reason: ReasonQuiet}},
		{"mcp_activity_resets_quiet", EvalInput{Snap: working(SubThinking, ago(2*time.Hour), ago(40*time.Minute)), Now: now, LastMCPAt: ago(time.Minute)},
			View{State: StateWorking, Base: StateWorking, Sub: SubThinking}},
		{"stale_mcp_does_not_hide_quiet", EvalInput{Snap: working(SubThinking, ago(2*time.Hour), ago(40*time.Minute)), Now: now, LastMCPAt: ago(50 * time.Minute)},
			View{State: StateWorking, Base: StateWorking, Sub: SubThinking, Stalled: true, Reason: ReasonQuiet}},

		{"tool_just_started", func() EvalInput {
			s := working("tool:Read", ago(time.Minute), ago(19*time.Second))
			s.ToolStartedAt = ago(19 * time.Second)
			return EvalInput{Snap: s, Now: now}
		}(), View{State: StateWorking, Base: StateWorking, Sub: "tool:Read"}},
		{"tool_running_suspects_approval", func() EvalInput {
			s := working("tool:Read", ago(time.Minute), ago(21*time.Second))
			s.ToolStartedAt = ago(21 * time.Second)
			return EvalInput{Snap: s, Now: now}
		}(), View{State: StateWorking, Base: StateWorking, Sub: "tool:Read", ApprovalSuspected: true, Reason: ReasonToolRunning}},
		{"tool_overrun_regular_tool", func() EvalInput {
			s := working("tool:Read", ago(time.Hour), ago(11*time.Minute))
			s.ToolStartedAt = ago(10*time.Minute + time.Second)
			return EvalInput{Snap: s, Now: now}
		}(), View{State: StateWorking, Base: StateWorking, Sub: "tool:Read", Stalled: true, Reason: ReasonToolOverrun}},
		{"shell_gets_the_long_limit", func() EvalInput {
			s := working("tool:Shell", ago(2*time.Hour), ago(59*time.Minute))
			s.ToolStartedAt = ago(59 * time.Minute)
			return EvalInput{Snap: s, Now: now}
		}(), View{State: StateWorking, Base: StateWorking, Sub: "tool:Shell", ApprovalSuspected: true, Reason: ReasonToolRunning}},
		{"shell_overrun", func() EvalInput {
			s := working("tool:Shell", ago(2*time.Hour), ago(61*time.Minute))
			s.ToolStartedAt = ago(61 * time.Minute)
			return EvalInput{Snap: s, Now: now, LastMCPAt: ago(time.Minute)}
		}(), View{State: StateWorking, Base: StateWorking, Sub: "tool:Shell", Stalled: true, Reason: ReasonToolOverrun}},
		{"task_subagent_is_a_long_tool", func() EvalInput {
			s := working("tool:Task", ago(time.Hour), ago(30*time.Minute))
			s.ToolStartedAt = ago(30 * time.Minute)
			return EvalInput{Snap: s, Now: now}
		}(), View{State: StateWorking, Base: StateWorking, Sub: "tool:Task", ApprovalSuspected: true, Reason: ReasonToolRunning}},
		{"tool_without_start_time_falls_back_to_since", func() EvalInput {
			s := working("tool:Read", ago(11*time.Minute), ago(11*time.Minute))
			return EvalInput{Snap: s, Now: now}
		}(), View{State: StateWorking, Base: StateWorking, Sub: "tool:Read", Stalled: true, Reason: ReasonToolOverrun}},

		{"waiting_you_overlays_working", EvalInput{Snap: working(SubThinking, ago(2*time.Hour), ago(40*time.Minute)), Now: now, WaitingYou: true, LastMCPAt: ago(time.Minute)},
			View{State: StateWaitingYou, Base: StateWorking, Sub: SubThinking, WaitingYou: true}},
		{"waiting_you_suppresses_tool_overrun", func() EvalInput {
			s := working("tool:mcp_interactive_feedback", ago(3*time.Hour), ago(30*time.Minute))
			s.ToolStartedAt = ago(3 * time.Hour)
			return EvalInput{Snap: s, Now: now, WaitingYou: true, LastMCPAt: ago(time.Minute)}
		}(), View{State: StateWaitingYou, Base: StateWorking, Sub: "tool:mcp_interactive_feedback", WaitingYou: true}},
		{"waiting_you_overlays_idle", EvalInput{Snap: Snapshot{State: StateIdle, LastEventAt: ago(time.Minute)}, Now: now, WaitingYou: true},
			View{State: StateWaitingYou, Base: StateIdle, WaitingYou: true}},
		{"waiting_you_does_not_overlay_aborted", EvalInput{Snap: Snapshot{State: StateAborted, LastEventAt: ago(time.Minute)}, Now: now, WaitingYou: true},
			View{State: StateAborted, Base: StateAborted}},
		{"waiting_you_gives_way_to_offline", EvalInput{Snap: Snapshot{State: StateIdle, LastEventAt: ago(3 * time.Hour)}, Now: now, WaitingYou: true},
			View{State: StateOffline, Base: StateIdle}},

		{"waiting_approval_fresh", EvalInput{Snap: Snapshot{State: StateWaitingApproval, Since: ago(time.Minute), LastEventAt: ago(time.Minute)}, Now: now},
			View{State: StateWaitingApproval, Base: StateWaitingApproval}},
		{"waiting_approval_overdue", EvalInput{Snap: Snapshot{State: StateWaitingApproval, Since: ago(5 * time.Minute), LastEventAt: ago(5 * time.Minute)}, Now: now},
			View{State: StateWaitingApproval, Base: StateWaitingApproval, ApprovalOverdue: true, Reason: ReasonApprovalOverdue}},
		{"aborted_never_stalled", EvalInput{Snap: Snapshot{State: StateAborted, LastEventAt: ago(90 * time.Minute)}, Now: now},
			View{State: StateAborted, Base: StateAborted}},
		{"error_never_stalled", EvalInput{Snap: Snapshot{State: StateError, LastEventAt: ago(90 * time.Minute)}, Now: now},
			View{State: StateError, Base: StateError}},
		{"future_last_event_is_clamped", EvalInput{Snap: working(SubThinking, now, now.Add(time.Minute)), Now: now},
			View{State: StateWorking, Base: StateWorking, Sub: SubThinking}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Evaluate(tc.in, th)
			if got.State != tc.want.State || got.Base != tc.want.Base || got.Sub != tc.want.Sub ||
				got.WaitingYou != tc.want.WaitingYou || got.Stalled != tc.want.Stalled ||
				got.StalledSoft != tc.want.StalledSoft || got.ApprovalSuspected != tc.want.ApprovalSuspected ||
				got.ApprovalOverdue != tc.want.ApprovalOverdue || got.Reason != tc.want.Reason {
				t.Fatalf("got  %+v\nwant %+v", got, tc.want)
			}
			if got.QuietFor < 0 {
				t.Fatalf("QuietFor negative: %v", got.QuietFor)
			}
		})
	}
}

func TestEvaluateQuietForUsesNewestSignal(t *testing.T) {
	th := DefaultThresholds()
	now := t0.Add(time.Hour)
	v := Evaluate(EvalInput{
		Snap:      working(SubThinking, now.Add(-time.Hour), now.Add(-20*time.Minute)),
		Now:       now,
		LastMCPAt: now.Add(-3 * time.Minute),
	}, th)
	if v.QuietFor != 3*time.Minute {
		t.Fatalf("QuietFor = %v, want 3m", v.QuietFor)
	}
}

// 阈值可配：同一个快照换一套阈值，判断结果跟着变。
func TestEvaluateHonoursCustomThresholds(t *testing.T) {
	th := DefaultThresholds()
	th.ThinkSoft = time.Minute
	th.ThinkStalled = 2 * time.Minute
	now := t0.Add(time.Hour)
	s := working(SubThinking, now.Add(-time.Hour), now.Add(-90*time.Second))
	if v := Evaluate(EvalInput{Snap: s, Now: now}, th); !v.StalledSoft || v.Stalled {
		t.Fatalf("custom soft threshold ignored: %+v", v)
	}
	th.ThinkStalled = time.Minute + 30*time.Second
	if v := Evaluate(EvalInput{Snap: s, Now: now}, th); !v.Stalled {
		t.Fatalf("custom hard threshold ignored: %+v", v)
	}
}

func TestEvaluateScreenSignal(t *testing.T) {
	th := DefaultThresholds()
	now := t0.Add(10 * time.Hour)
	ago := func(d time.Duration) time.Time { return now.Add(-d) }
	thinking := working(SubThinking, ago(time.Hour), ago(40*time.Minute)) // 纯 hook 规则下已经是硬卡住

	cases := []struct {
		name string
		sc   *ScreenSignal
		snap Snapshot
		want View
	}{
		{"no_screen_falls_back_to_hook_rules", nil, thinking,
			View{Stalled: true, Reason: ReasonQuiet}},
		{"unverified_screen_is_ignored", &ScreenSignal{Verified: false, Animating: true, Tokens: 5, TokensSince: ago(time.Hour)}, thinking,
			View{Stalled: true, Reason: ReasonQuiet}},
		{"verified_animating_tokens_moving_is_healthy", &ScreenSignal{Verified: true, Animating: true, Tokens: 80, TokensSince: ago(10 * time.Second), QuietFor: 0}, thinking,
			View{}},
		{"tokens_flat_but_animating_is_soft", &ScreenSignal{Verified: true, Animating: true, Tokens: 80, TokensSince: ago(45 * time.Second)}, thinking,
			View{StalledSoft: true, Reason: ReasonTokensFlat}},
		{"tokens_flat_just_under_threshold", &ScreenSignal{Verified: true, Animating: true, Tokens: 80, TokensSince: ago(44 * time.Second)}, thinking,
			View{}},
		{"tokens_unknown_never_flags", &ScreenSignal{Verified: true, Animating: true, Tokens: -1, TokensSince: ago(time.Hour)}, thinking,
			View{}},
		{"tokens_flat_without_animation_is_not_the_soft_rule", &ScreenSignal{Verified: true, Animating: false, Tokens: 80, TokensSince: ago(time.Hour), QuietFor: 30 * time.Second}, thinking,
			View{}},
		{"frozen_after_3m_is_hard", &ScreenSignal{Verified: true, Animating: false, Tokens: 80, TokensSince: ago(time.Hour), QuietFor: 3 * time.Minute}, thinking,
			View{Stalled: true, Reason: ReasonTerminalFrozen}},
		{"frozen_just_under_3m", &ScreenSignal{Verified: true, Animating: false, QuietFor: 3*time.Minute - time.Second}, thinking,
			View{}},
		{"frozen_unknown_quiet_never_flags", &ScreenSignal{Verified: true, Animating: false, QuietFor: -1}, thinking,
			View{}},
		{"approval_screen_is_not_frozen", &ScreenSignal{Verified: true, Animating: false, Approval: true, QuietFor: time.Hour}, thinking,
			View{}},
		{"responding_uses_same_rules", &ScreenSignal{Verified: true, Animating: false, QuietFor: 5 * time.Minute},
			working(SubResponding, ago(time.Hour), ago(2*time.Minute)),
			View{Stalled: true, Reason: ReasonTerminalFrozen}},
		{"healthy_screen_overrides_hook_quiet_soft", &ScreenSignal{Verified: true, Animating: true, Tokens: 10, TokensSince: ago(time.Second)},
			working(SubThinking, ago(time.Hour), ago(15*time.Minute)),
			View{}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := Evaluate(EvalInput{Snap: c.snap, Now: now, Screen: c.sc, HasLiveTerminal: c.sc != nil}, th)
			if got.Stalled != c.want.Stalled || got.StalledSoft != c.want.StalledSoft || got.Reason != c.want.Reason {
				t.Fatalf("got stalled=%v soft=%v reason=%q, want stalled=%v soft=%v reason=%q",
					got.Stalled, got.StalledSoft, got.Reason, c.want.Stalled, c.want.StalledSoft, c.want.Reason)
			}
		})
	}
}

func TestEvaluateScreenSignalTools(t *testing.T) {
	th := DefaultThresholds()
	now := t0.Add(10 * time.Hour)
	ago := func(d time.Duration) time.Time { return now.Add(-d) }
	s := working("tool:Shell", ago(time.Minute), ago(30*time.Second))
	s.ToolStartedAt = ago(30 * time.Second)

	// 纯 hook：工具跑了 20 秒以上只能「疑似在等批准」。
	if got := Evaluate(EvalInput{Snap: s, Now: now}, th); !got.ApprovalSuspected {
		t.Fatalf("hook-only should suspect approval: %+v", got)
	}
	// 屏幕可信：是不是在等批准是确定的，不再猜。
	sc := &ScreenSignal{Verified: true, Animating: true, Tokens: 10, TokensSince: ago(time.Second)}
	if got := Evaluate(EvalInput{Snap: s, Now: now, Screen: sc, HasLiveTerminal: true}, th); got.ApprovalSuspected || got.Reason != "" {
		t.Fatalf("verified screen should not guess: %+v", got)
	}
	// 屏幕不可信：回到猜测。
	sc.Verified = false
	if got := Evaluate(EvalInput{Snap: s, Now: now, Screen: sc}, th); !got.ApprovalSuspected {
		t.Fatalf("unverified screen should fall back to suspicion: %+v", got)
	}
}
