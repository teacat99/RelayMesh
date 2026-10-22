package agentstate

import (
	"math/rand"
	"testing"
	"time"
)

var t0 = time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)

// step 是一条事件序列里的一步：事件本身 + 对这一步结果的断言。
type step struct {
	name   string
	atMs   int // 发送端时间（相对 t0）；noAt 时忽略
	recvMs int // 接收时间（相对 t0）
	tool   string
	status string
	noAt   bool // Hub 自己产生的事件，没有发送端时间
	noTime bool // 发送端和接收时间都没有

	want       string // 期望的「状态/子状态」（checkState 为 true 时才检查，空串 = 还没有状态）
	checkState bool
	checkNote  bool
	note       string
	checkRec   bool
	rec        bool
	checkDur   bool
	dur        bool
	checkChg   bool
	chg        bool
}

func ev(ms int, name string) step { return step{name: name, atMs: ms, recvMs: ms} }

func (s step) withTool(t string) step       { s.tool = t; return s }
func (s step) withStatus(st string) step    { s.status = st; return s }
func (s step) recv(ms int) step             { s.recvMs = ms; return s }
func (s step) hub() step                    { s.noAt = true; return s }
func (s step) bare() step                   { s.noTime = true; return s }
func (s step) wantState(w string) step      { s.checkState, s.want = true, w; return s }
func (s step) wantNote(n string) step       { s.checkNote, s.note = true, n; return s }
func (s step) wantRecord(b bool) step       { s.checkRec, s.rec = true, b; return s }
func (s step) wantDurable(b bool) step      { s.checkDur, s.dur = true, b; return s }
func (s step) wantStateChanged(b bool) step { s.checkChg, s.chg = true, b; return s }

func (s step) event() Event {
	e := Event{Source: SourceHook, Name: s.name, Tool: s.tool, Status: s.status}
	if s.noTime {
		return e
	}
	e.Received = t0.Add(time.Duration(s.recvMs) * time.Millisecond)
	if !s.noAt {
		e.At = t0.Add(time.Duration(s.atMs) * time.Millisecond)
	}
	return e
}

func stateStr(s Snapshot) string {
	if s.Sub != "" {
		return string(s.State) + "/" + s.Sub
	}
	return string(s.State)
}

func at(ms int) time.Time { return t0.Add(time.Duration(ms) * time.Millisecond) }

type applyCase struct {
	name  string
	steps []step
	check func(t *testing.T, s Snapshot)
}

func applyCases() []applyCase {
	return []applyCase{
		{name: "normal_turn", steps: []step{
			ev(0, EvSessionStart).wantState("idle"),
			ev(100, EvPrompt).wantState("working/thinking"),
			ev(200, EvThought).wantState("working/thinking"),
			ev(300, EvToolStart).withTool("Read").wantState("working/tool:Read"),
			ev(400, EvToolEnd).withTool("Read").wantState("working/thinking"),
			ev(500, EvResponse).wantState("working/responding"),
			ev(600, EvStop).withStatus("completed").wantState("idle"),
		}, check: func(t *testing.T, s Snapshot) {
			if !s.StartedAt.Equal(at(0)) || !s.TurnStartedAt.Equal(at(100)) || !s.TurnEndedAt.Equal(at(600)) {
				t.Fatalf("times started=%v turnStart=%v turnEnd=%v", s.StartedAt, s.TurnStartedAt, s.TurnEndedAt)
			}
			if s.LastTool != "Read" {
				t.Fatalf("LastTool = %q", s.LastTool)
			}
		}},
		{name: "v1_hook_tool_then_stop", steps: []step{
			ev(0, EvToolEnd).wantState("working/thinking"),
			ev(1000, EvToolEnd).wantState("working/thinking"),
			ev(2000, EvStop).withStatus("completed").wantState("idle"),
		}, check: func(t *testing.T, s Snapshot) {
			if !s.TurnStartedAt.Equal(at(0)) || !s.TurnEndedAt.Equal(at(2000)) || !s.StartedAt.IsZero() {
				t.Fatalf("turnStart=%v turnEnd=%v started=%v", s.TurnStartedAt, s.TurnEndedAt, s.StartedAt)
			}
		}},
		{name: "v1_new_turn_after_idle", steps: []step{
			ev(0, EvStop).withStatus("completed").wantState("idle"),
			ev(5000, EvToolEnd).wantState("working/thinking"),
		}, check: func(t *testing.T, s Snapshot) {
			if !s.TurnStartedAt.Equal(at(5000)) || !s.TurnEndedAt.IsZero() {
				t.Fatalf("turnStart=%v turnEnd=%v", s.TurnStartedAt, s.TurnEndedAt)
			}
		}},
		{name: "abort_sequence_folds_error", steps: []step{
			ev(0, EvPrompt).wantState("working/thinking"),
			ev(100, EvToolStart).withTool("Shell").wantState("working/tool:Shell"),
			ev(200, EvStop).withStatus("aborted").wantState("aborted"),
			ev(250, EvToolFail).withTool("Shell").wantState("aborted").wantNote(NoteAbortTail),
			ev(300, EvStop).withStatus("error").wantState("aborted").wantNote(NoteFoldedError).wantDurable(false),
		}},
		{name: "error_after_fold_window_is_error", steps: []step{
			ev(0, EvStop).withStatus("aborted").wantState("aborted"),
			ev(3000, EvStop).withStatus("error").wantState("error"),
		}},
		{name: "plain_error", steps: []step{
			ev(0, EvPrompt),
			ev(500, EvStop).withStatus("error").wantState("error"),
		}},
		{name: "reordered_error_before_aborted", steps: []step{
			ev(0, EvPrompt),
			ev(100, EvToolStart).withTool("Shell"),
			ev(300, EvStop).withStatus("error").recv(310).wantState("error"),
			ev(200, EvStop).withStatus("aborted").recv(320).wantState("aborted").wantNote(NoteAbortOverError),
		}, check: func(t *testing.T, s Snapshot) {
			if !s.LastEventAt.Equal(at(300)) {
				t.Fatalf("LastEventAt moved back: %v", s.LastEventAt)
			}
		}},
		{name: "duplicate_thought_merged", steps: []step{
			ev(0, EvPrompt),
			ev(100, EvThought).wantRecord(true),
			ev(120, EvThought).wantNote(NoteDuplicate).wantRecord(false),
		}},
		{name: "thought_record_throttled", steps: []step{
			ev(0, EvPrompt),
			ev(1000, EvThought).wantRecord(true),
			ev(2000, EvThought).wantRecord(false).wantState("working/thinking"),
			ev(3000, EvThought).wantRecord(false),
			ev(6100, EvThought).wantRecord(true),
		}},
		{name: "thought_that_changes_state_is_recorded", steps: []step{
			ev(0, EvPrompt),
			ev(1000, EvThought).wantRecord(true),
			ev(1100, EvToolStart).withTool("Read").wantState("working/tool:Read"),
			ev(1200, EvThought).wantRecord(true).wantState("working/thinking").wantStateChanged(true),
		}},
		{name: "different_tools_not_deduped", steps: []step{
			ev(0, EvToolStart).withTool("Read").wantState("working/tool:Read"),
			ev(10, EvToolStart).withTool("Grep").wantState("working/tool:Grep").wantNote(""),
		}},
		{name: "identical_stop_deduped", steps: []step{
			ev(0, EvStop).withStatus("completed").wantState("idle"),
			ev(30, EvStop).withStatus("completed").wantNote(NoteDuplicate).wantRecord(false),
		}},
		{name: "tool_end_without_start", steps: []step{
			ev(0, EvPrompt),
			ev(100, EvToolEnd).withTool("Read").wantState("working/thinking"),
		}},
		{name: "tool_fail_while_tool_running", steps: []step{
			ev(0, EvPrompt),
			ev(100, EvToolStart).withTool("Shell").wantState("working/tool:Shell"),
			ev(200, EvToolFail).withTool("Shell").wantState("working/thinking").wantNote(NoteToolFailed),
		}},
		{name: "tool_fail_when_idle_ignored", steps: []step{
			ev(0, EvStop).withStatus("completed").wantState("idle"),
			ev(1000, EvToolFail).wantState("idle").wantNote(NoteIgnored).wantDurable(false),
		}},
		{name: "tool_fail_keeps_responding", steps: []step{
			ev(0, EvResponse).wantState("working/responding"),
			ev(500, EvToolFail).wantState("working/responding"),
		}},
		{name: "session_end_then_stop_tail", steps: []step{
			ev(0, EvPrompt),
			ev(1000, EvSessionEnd).withStatus("completed").wantState("ended"),
			ev(1100, EvStop).withStatus("completed").wantState("ended").wantNote(NoteEndedTail),
		}, check: func(t *testing.T, s Snapshot) {
			if s.EndReason != EndCompleted || !s.EndedAt.Equal(at(1000)) {
				t.Fatalf("end reason=%q at=%v", s.EndReason, s.EndedAt)
			}
		}},
		{name: "ended_tail_activity_ignored", steps: []step{
			ev(0, EvSessionEnd).wantState("ended"),
			ev(5000, EvToolEnd).wantState("ended").wantNote(NoteEndedTail),
		}},
		{name: "ended_then_late_activity_revives", steps: []step{
			ev(0, EvSessionStart),
			ev(1, EvSessionEnd).withStatus("completed").wantState("ended"),
			ev(20000, EvToolEnd).wantState("working/thinking").wantNote(NoteRevived),
		}, check: func(t *testing.T, s Snapshot) {
			if !s.EndedAt.IsZero() || s.EndReason != "" || !s.StartedAt.Equal(at(0)) {
				t.Fatalf("revived fields ended=%v reason=%q started=%v", s.EndedAt, s.EndReason, s.StartedAt)
			}
			if !s.TurnStartedAt.Equal(at(20000)) {
				t.Fatalf("turnStart=%v", s.TurnStartedAt)
			}
		}},
		{name: "ended_prompt_revives_immediately", steps: []step{
			ev(0, EvSessionEnd).wantState("ended"),
			ev(1000, EvPrompt).wantState("working/thinking").wantNote(NoteRevived),
		}},
		{name: "ended_session_start_is_a_new_lifecycle", steps: []step{
			ev(0, EvSessionEnd).wantState("ended"),
			ev(1000, EvSessionStart).wantState("idle"),
		}, check: func(t *testing.T, s Snapshot) {
			if !s.StartedAt.Equal(at(1000)) || !s.EndedAt.IsZero() {
				t.Fatalf("started=%v ended=%v", s.StartedAt, s.EndedAt)
			}
		}},
		{name: "chat_end_then_its_own_tool_end_and_stop", steps: []step{
			ev(0, EvPrompt),
			ev(1000, EvChatEnd).hub().withStatus("completed").wantState("ended"),
			ev(1050, EvToolEnd).wantState("ended").wantNote(NoteEndedTail),
			ev(1200, EvResponse).wantState("ended").wantNote(NoteEndedTail),
			ev(1300, EvStop).withStatus("completed").wantState("ended"),
		}, check: func(t *testing.T, s Snapshot) {
			if s.EndReason != EndCompleted {
				t.Fatalf("end reason = %q", s.EndReason)
			}
		}},
		{name: "chat_end_with_skewed_hook_clock_is_still_a_tail", steps: []step{
			ev(0, EvPrompt),
			// Hub 用接收时间记结束；hook 机器的时钟比 Hub 快 40 秒，收尾事件的发送端时间看起来「很晚」，但接收时间紧跟在结束之后
			ev(1000, EvChatEnd).hub().withStatus("completed").wantState("ended"),
			ev(41100, EvToolEnd).recv(1100).wantState("ended").wantNote(NoteEndedTail),
		}},
		{name: "chat_end_blocked_reason", steps: []step{
			ev(0, EvPrompt),
			ev(1000, EvChatEnd).hub().withStatus("blocked").wantState("ended"),
		}, check: func(t *testing.T, s Snapshot) {
			if s.EndReason != EndBlocked {
				t.Fatalf("end reason = %q", s.EndReason)
			}
		}},
		{name: "chat_end_unknown_reason_defaults_completed", steps: []step{
			ev(0, EvChatEnd).hub().withStatus("whatever").wantState("ended"),
		}, check: func(t *testing.T, s Snapshot) {
			if s.EndReason != EndCompleted {
				t.Fatalf("end reason = %q", s.EndReason)
			}
		}},
		{name: "session_end_unknown_reason_defaults_closed", steps: []step{
			ev(0, EvSessionEnd).withStatus("window_close").wantState("ended"),
		}, check: func(t *testing.T, s Snapshot) {
			if s.EndReason != EndClosed {
				t.Fatalf("end reason = %q", s.EndReason)
			}
		}},
		{name: "late_event_ignored", steps: []step{
			ev(1000, EvPrompt),
			ev(2000, EvToolStart).withTool("Read").wantState("working/tool:Read"),
			ev(1500, EvToolEnd).withTool("Grep").recv(2100).wantState("working/tool:Read").wantNote(NoteLate).wantRecord(true).wantDurable(false),
		}},
		{name: "clock_rewind_uses_receive_time", steps: []step{
			ev(0, EvPrompt),
			ev(10000, EvToolStart).withTool("Read").wantState("working/tool:Read"),
			// 发送端时钟回拨了一个小时：时间戳比最近事件早很多，但接收时间在后，当作「现在」
			ev(-3600000, EvToolEnd).recv(11000).wantState("working/thinking"),
		}, check: func(t *testing.T, s Snapshot) {
			if !s.LastEventAt.Equal(at(11000)) {
				t.Fatalf("LastEventAt = %v, want receive time", s.LastEventAt)
			}
		}},
		{name: "future_timestamp_clamped_to_receive_time", steps: []step{
			ev(3600000, EvPrompt).recv(0).wantState("working/thinking"),
		}, check: func(t *testing.T, s Snapshot) {
			if !s.LastEventAt.Equal(at(0)) {
				t.Fatalf("LastEventAt = %v", s.LastEventAt)
			}
		}},
		{name: "slightly_ahead_clock_is_trusted", steps: []step{
			ev(30000, EvPrompt).recv(0).wantState("working/thinking"),
		}, check: func(t *testing.T, s Snapshot) {
			if !s.LastEventAt.Equal(at(30000)) {
				t.Fatalf("LastEventAt = %v", s.LastEventAt)
			}
		}},
		{name: "no_sender_time_uses_receive_time", steps: []step{
			ev(0, EvPrompt).hub().recv(500).wantState("working/thinking"),
		}, check: func(t *testing.T, s Snapshot) {
			if !s.LastEventAt.Equal(at(500)) {
				t.Fatalf("LastEventAt = %v", s.LastEventAt)
			}
		}},
		{name: "no_times_at_all_is_unknown", steps: []step{
			ev(0, EvPrompt).bare().wantState("").wantNote(NoteUnknown).wantRecord(false),
		}},
		{name: "approval_wait_and_done", steps: []step{
			ev(0, EvPrompt),
			ev(1000, EvApprovalWait).wantState("waiting_approval"),
			ev(5000, EvApprovalDone).wantState("working/thinking"),
		}},
		{name: "approval_wait_from_idle_starts_turn", steps: []step{
			ev(0, EvStop).withStatus("completed").wantState("idle"),
			ev(1000, EvApprovalWait).wantState("waiting_approval"),
		}, check: func(t *testing.T, s Snapshot) {
			if !s.TurnStartedAt.Equal(at(1000)) {
				t.Fatalf("turnStart = %v", s.TurnStartedAt)
			}
		}},
		{name: "approval_wait_ignored_when_ended", steps: []step{
			ev(0, EvSessionEnd).wantState("ended"),
			ev(1000, EvApprovalWait).wantState("ended").wantNote(NoteIgnored),
		}},
		{name: "approval_done_without_wait_ignored", steps: []step{
			ev(0, EvPrompt),
			ev(1000, EvApprovalDone).wantState("working/thinking").wantNote(NoteIgnored),
		}},
		{name: "tool_end_from_waiting_approval_resumes_same_turn", steps: []step{
			ev(0, EvPrompt),
			ev(1000, EvApprovalWait).wantState("waiting_approval"),
			ev(9000, EvToolEnd).withTool("Shell").wantState("working/thinking"),
		}, check: func(t *testing.T, s Snapshot) {
			if !s.TurnStartedAt.Equal(at(0)) {
				t.Fatalf("turnStart moved to %v", s.TurnStartedAt)
			}
		}},
		{name: "waiting_approval_then_stop", steps: []step{
			ev(0, EvPrompt),
			ev(1000, EvApprovalWait),
			ev(3000, EvStop).withStatus("completed").wantState("idle"),
		}},
		{name: "compact_is_heartbeat_only", steps: []step{
			ev(0, EvPrompt).wantState("working/thinking"),
			ev(100, EvCompact).wantState("working/thinking").wantNote("").wantRecord(true).wantDurable(false).wantStateChanged(false),
		}},
		{name: "compact_when_ended_is_tail", steps: []step{
			ev(0, EvSessionEnd),
			ev(500, EvCompact).wantState("ended").wantNote(NoteEndedTail),
		}},
		{name: "prompt_resets_turn_times", steps: []step{
			ev(0, EvPrompt),
			ev(1000, EvStop).withStatus("completed"),
			ev(2000, EvPrompt).wantState("working/thinking"),
		}, check: func(t *testing.T, s Snapshot) {
			if !s.TurnStartedAt.Equal(at(2000)) || !s.TurnEndedAt.IsZero() {
				t.Fatalf("turnStart=%v turnEnd=%v", s.TurnStartedAt, s.TurnEndedAt)
			}
		}},
		{name: "session_start_twice_keeps_started_at", steps: []step{
			ev(0, EvSessionStart).wantState("idle"),
			ev(1000, EvSessionStart).wantState("idle"),
		}, check: func(t *testing.T, s Snapshot) {
			if !s.StartedAt.Equal(at(0)) {
				t.Fatalf("StartedAt = %v", s.StartedAt)
			}
		}},
		{name: "unknown_event_name", steps: []step{
			ev(0, EvPrompt).wantState("working/thinking"),
			ev(100, "bogus").wantState("working/thinking").wantNote(NoteUnknown).wantRecord(false),
		}},
		{name: "stop_other_goes_idle", steps: []step{
			ev(0, EvPrompt),
			ev(100, EvStop).withStatus("weird").wantState("idle").wantNote(NoteStopOther),
		}},
		{name: "thought_from_idle_revives_working", steps: []step{
			ev(0, EvStop).withStatus("completed").wantState("idle"),
			ev(1000, EvThought).wantState("working/thinking"),
		}},
		{name: "response_after_tool", steps: []step{
			ev(0, EvToolStart).withTool("Read"),
			ev(100, EvResponse).wantState("working/responding"),
		}},
		{name: "aborted_then_prompt_resumes", steps: []step{
			ev(0, EvStop).withStatus("aborted").wantState("aborted"),
			ev(10000, EvPrompt).wantState("working/thinking"),
		}},
		{name: "aborted_tail_activity_ignored_then_resumes", steps: []step{
			ev(0, EvStop).withStatus("aborted").wantState("aborted"),
			ev(1000, EvThought).wantState("aborted").wantNote(NoteAbortTail),
			ev(3000, EvThought).wantState("working/thinking"),
		}},
		{name: "error_then_tool_after_window", steps: []step{
			ev(0, EvStop).withStatus("error").wantState("error"),
			ev(3000, EvToolStart).withTool("Read").wantState("working/tool:Read"),
		}},
		{name: "same_millisecond_events_not_late", steps: []step{
			ev(100, EvPrompt).wantState("working/thinking"),
			ev(100, EvThought).wantNote(""),
			ev(100, EvToolStart).withTool("Read").wantState("working/tool:Read"),
		}},
		{name: "stop_aborted_when_ended_is_tail", steps: []step{
			ev(0, EvSessionEnd).wantState("ended"),
			ev(1000, EvStop).withStatus("aborted").wantState("ended").wantNote(NoteEndedTail),
		}},
		{name: "event_before_end_is_late", steps: []step{
			ev(1000, EvSessionEnd).wantState("ended"),
			ev(900, EvToolEnd).recv(1050).wantState("ended").wantNote(NoteLate),
		}},
		{name: "session_end_twice", steps: []step{
			ev(0, EvSessionEnd).wantState("ended"),
			ev(1000, EvSessionEnd).wantState("ended").wantNote(NoteAlreadyEnded),
		}, check: func(t *testing.T, s Snapshot) {
			if !s.EndedAt.Equal(at(0)) {
				t.Fatalf("EndedAt rewritten: %v", s.EndedAt)
			}
		}},
		{name: "completed_after_abort_goes_idle", steps: []step{
			ev(0, EvStop).withStatus("aborted"),
			ev(5000, EvStop).withStatus("completed").wantState("idle"),
		}},
		{name: "tool_start_records_tool_and_time", steps: []step{
			ev(0, EvPrompt),
			ev(700, EvToolStart).withTool("Shell").wantState("working/tool:Shell").wantStateChanged(true).wantDurable(true),
		}, check: func(t *testing.T, s Snapshot) {
			if s.LastTool != "Shell" || !s.ToolStartedAt.Equal(at(700)) {
				t.Fatalf("LastTool=%q ToolStartedAt=%v", s.LastTool, s.ToolStartedAt)
			}
		}},
		{name: "tool_start_without_name", steps: []step{
			ev(0, EvToolStart).wantState("working/tool:unknown"),
		}},
		{name: "session_end_closes_open_turn", steps: []step{
			ev(0, EvPrompt),
			ev(1000, EvSessionEnd).wantState("ended"),
		}, check: func(t *testing.T, s Snapshot) {
			if !s.TurnEndedAt.Equal(at(1000)) {
				t.Fatalf("TurnEndedAt = %v", s.TurnEndedAt)
			}
		}},
		{name: "repeat_working_is_not_durable", steps: []step{
			ev(0, EvPrompt),
			ev(6000, EvThought).wantDurable(false).wantStateChanged(false),
		}},
		{name: "since_only_moves_on_state_change", steps: []step{
			ev(0, EvPrompt),
			ev(1000, EvToolStart).withTool("Read"),
			ev(2000, EvToolEnd),
			ev(3000, EvResponse).wantState("working/responding"),
		}, check: func(t *testing.T, s Snapshot) {
			if !s.Since.Equal(at(0)) {
				t.Fatalf("Since moved on sub-state change: %v", s.Since)
			}
		}},
		{name: "stop_completed_clears_sub", steps: []step{
			ev(0, EvToolStart).withTool("Read").wantState("working/tool:Read"),
			ev(1000, EvStop).withStatus("completed").wantState("idle"),
		}, check: func(t *testing.T, s Snapshot) {
			if s.Sub != "" || !s.Since.Equal(at(1000)) {
				t.Fatalf("sub=%q since=%v", s.Sub, s.Since)
			}
		}},
	}
}

func TestApplySequences(t *testing.T) {
	cases := applyCases()
	if len(cases) < 40 {
		t.Fatalf("设计稿要求至少 40 条事件序列，现在只有 %d 条", len(cases))
	}
	cfg := DefaultConfig()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var s Snapshot
			for i, st := range tc.steps {
				var ch Change
				s, ch = Apply(s, st.event(), cfg)
				if st.checkState {
					if got := stateStr(s); got != st.want {
						t.Fatalf("step %d (%s): state = %q, want %q (note=%q)", i, st.name, got, st.want, ch.Note)
					}
				}
				if st.checkNote && ch.Note != st.note {
					t.Fatalf("step %d (%s): note = %q, want %q", i, st.name, ch.Note, st.note)
				}
				if st.checkRec && ch.Record != st.rec {
					t.Fatalf("step %d (%s): record = %v, want %v", i, st.name, ch.Record, st.rec)
				}
				if st.checkDur && ch.Durable != st.dur {
					t.Fatalf("step %d (%s): durable = %v, want %v", i, st.name, ch.Durable, st.dur)
				}
				if st.checkChg && ch.StateChanged != st.chg {
					t.Fatalf("step %d (%s): stateChanged = %v, want %v", i, st.name, ch.StateChanged, st.chg)
				}
			}
			if tc.check != nil {
				tc.check(t, s)
			}
		})
	}
}

// 没有任何时间信息的事件不能改动快照。
func TestApplyWithoutTimesLeavesSnapshotUntouched(t *testing.T) {
	cfg := DefaultConfig()
	s, _ := Apply(Snapshot{}, ev(0, EvPrompt).event(), cfg)
	before := s
	after, ch := Apply(s, Event{Source: SourceHook, Name: EvStop, Status: "completed"}, cfg)
	if after != before || ch.Note != NoteUnknown || ch.Record {
		t.Fatalf("snapshot changed or recorded: %+v %+v", ch, after)
	}
}

// Apply 是纯函数：同样的输入得到同样的输出，也不会改动传进来的快照。
func TestApplyIsDeterministic(t *testing.T) {
	cfg := DefaultConfig()
	s, _ := Apply(Snapshot{}, ev(0, EvPrompt).event(), cfg)
	e := ev(500, EvToolStart).withTool("Read").event()
	a1, c1 := Apply(s, e, cfg)
	a2, c2 := Apply(s, e, cfg)
	if a1 != a2 || c1 != c2 {
		t.Fatalf("Apply is not deterministic: %+v vs %+v", a1, a2)
	}
	if stateStr(s) != "working/thinking" {
		t.Fatalf("input snapshot mutated: %s", stateStr(s))
	}
}

// 随机事件流不会 panic，状态永远落在合法集合里，已结束的会话不会被收尾事件复活。
func TestApplyRandomStreamsKeepInvariants(t *testing.T) {
	cfg := DefaultConfig()
	names := []string{EvSessionStart, EvSessionEnd, EvPrompt, EvThought, EvToolStart, EvToolEnd, EvToolFail,
		EvResponse, EvStop, EvCompact, EvChatEnd, EvApprovalWait, EvApprovalDone, "bogus"}
	statuses := []string{"", "completed", "aborted", "error", "blocked", "weird"}
	valid := map[State]bool{"": true, StateIdle: true, StateWorking: true, StateWaitingApproval: true,
		StateAborted: true, StateError: true, StateEnded: true}
	rng := rand.New(rand.NewSource(20261014))
	for n := 0; n < 3000; n++ {
		var s Snapshot
		clock := 0
		for i := 0; i < 40; i++ {
			clock += rng.Intn(3000)
			e := Event{Source: SourceHook, Name: names[rng.Intn(len(names))], Status: statuses[rng.Intn(len(statuses))],
				Received: at(clock)}
			if rng.Intn(5) != 0 {
				e.At = at(clock + rng.Intn(4000) - 2000)
			}
			if rng.Intn(3) == 0 {
				e.Tool = []string{"Shell", "Read", "Task"}[rng.Intn(3)]
			}
			prev := s
			var ch Change
			s, ch = Apply(s, e, cfg)
			if !valid[s.State] {
				t.Fatalf("invalid state %q after %+v", s.State, e)
			}
			if s.State != StateWorking && s.Sub != "" {
				t.Fatalf("state %q carries sub %q", s.State, s.Sub)
			}
			if s.State == StateWorking && s.Sub == "" {
				t.Fatalf("working without sub after %+v", e)
			}
			if prev.State == StateEnded && s.State != StateEnded {
				switch e.Name {
				case EvSessionStart, EvPrompt, EvThought, EvToolStart, EvToolEnd, EvResponse:
				default:
					t.Fatalf("ended revived by %s", e.Name)
				}
			}
			if ch.Note == NoteDuplicate && ch.Record {
				t.Fatalf("duplicate recorded")
			}
			if s.State == StateEnded && s.EndedAt.IsZero() {
				t.Fatalf("ended without EndedAt")
			}
			if s.State != StateEnded && !s.EndedAt.IsZero() {
				t.Fatalf("EndedAt set while %s", s.State)
			}
		}
	}
}
