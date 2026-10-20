package agentstate

import (
	"strings"
	"time"
)

// 为了在内存里识别「会话结束之后到达的收尾事件」，快照里多记了结束事件的接收时间（见 Snapshot 的小写字段）。
// 它跨的是两个时钟（Hub 产生的 chat_end 用接收时间，hook 事件用发送端时间），所以收尾判断用接收时间之差，不用有效时间之差。

func knownEvent(name string) bool {
	switch name {
	case EvSessionStart, EvSessionEnd, EvPrompt, EvThought, EvToolStart, EvToolEnd, EvToolFail,
		EvResponse, EvStop, EvCompact, EvChatEnd, EvApprovalWait, EvApprovalDone:
		return true
	}
	return false
}

// NormalizeStopStatus 把 stop 的结束状态收敛到 completed / aborted / error / other。
func NormalizeStopStatus(s string) string {
	switch v := strings.ToLower(strings.TrimSpace(s)); v {
	case StopCompleted, StopAborted, StopError:
		return v
	}
	return StopOther
}

// NormalizeEndReason 把会话结束的原因收敛到已知取值；认不出就用 fallback。
func NormalizeEndReason(s, fallback string) string {
	switch v := strings.ToLower(strings.TrimSpace(s)); v {
	case EndCompleted, EndBlocked, EndAborted, EndHandoff, EndError, EndClosed:
		return v
	}
	return fallback
}

func absDuration(d time.Duration) time.Duration {
	if d < 0 {
		return -d
	}
	return d
}

func recvOf(e Event, t time.Time) time.Time {
	switch {
	case !e.Received.IsZero():
		return e.Received
	case !e.At.IsZero():
		return e.At
	}
	return t
}

// resolveTime 决定事件的有效时间，并判断它是不是乱序到达的迟到事件。
//   - 没有发送端时间（Hub 自己产生的事件）：用接收时间，不判迟到；
//   - 发送端时间比接收时间领先太多：不可信，用接收时间；
//   - 比最近一个被采纳的事件早：早不到 LateWindow 的是乱序（迟到，只记录不改状态）；
//     更早的说明发送端时钟回拨了，用接收时间当作「现在」。
func resolveTime(s Snapshot, e Event, c Config) (time.Time, bool) {
	recv := e.Received
	if recv.IsZero() {
		recv = e.At
	}
	if e.At.IsZero() {
		return recv, false
	}
	at := e.At
	if at.After(recv.Add(c.FutureTolerance)) {
		return recv, false
	}
	if !s.LastEventAt.IsZero() && at.Before(s.LastEventAt) {
		if at.Before(s.LastEventAt.Add(-c.LateWindow)) && !recv.Before(s.LastRecv) {
			return recv, false
		}
		return at, true
	}
	return at, false
}

func setState(s *Snapshot, st State, sub string, t time.Time) {
	if s.State != st {
		s.Since = t
	}
	s.State, s.Sub = st, sub
}

// startTurn 记下一轮新对话的开始。
func startTurn(s *Snapshot, t time.Time) {
	s.TurnStartedAt, s.TurnEndedAt = t, time.Time{}
}

// startTurnIfNew：从「没在干活」的状态被活动事件拉起来（旧版 hook 没有 beforeSubmitPrompt），也算一轮新对话的开始。
func startTurnIfNew(s *Snapshot, t time.Time) {
	switch s.State {
	case "", StateIdle, StateAborted, StateError, StateEnded:
		startTurn(s, t)
	}
}

func reviveSession(s *Snapshot) {
	s.EndedAt, s.EndReason, s.endedRecv = time.Time{}, "", time.Time{}
}

func endSession(s *Snapshot, t, recv time.Time, reason string) {
	s.State, s.Sub, s.Since = StateEnded, "", t
	s.EndedAt, s.EndReason, s.endedRecv = t, reason, recv
	if s.TurnStartedAt.After(s.TurnEndedAt) || s.TurnEndedAt.IsZero() {
		s.TurnEndedAt = t // 没收尾的那一轮一并结束
	}
}

// statusSource 报告这个来源说的是不是「智能体在干什么」。MCP 调用、守护与 Web 操作是 Hub 侧的旁证，不算状态的出处。
func statusSource(src Source) bool {
	switch src {
	case SourceHook, SourceTUI, SourceRun:
		return true
	}
	return false
}

// tookEffect 报告一次 Apply 的结果是不是真的作用在了状态上（而不是被去重、判成迟到或并入收尾尾巴）。
func tookEffect(note string) bool {
	switch note {
	case "", NoteRevived, NoteStopOther, NoteToolFailed, NoteAbortOverError:
		return true
	}
	return false
}

// durableEqual 比较「需要写库」的字段（不含心跳性质的时间戳）。
func durableEqual(a, b Snapshot) bool {
	return a.State == b.State && a.Sub == b.Sub && a.Since.Equal(b.Since) && a.Source == b.Source &&
		a.StartedAt.Equal(b.StartedAt) && a.EndedAt.Equal(b.EndedAt) && a.EndReason == b.EndReason &&
		a.LastTool == b.LastTool && a.ToolStartedAt.Equal(b.ToolStartedAt) &&
		a.TurnStartedAt.Equal(b.TurnStartedAt) && a.TurnEndedAt.Equal(b.TurnEndedAt)
}

// Apply 把一个事件折进会话快照，返回新快照与这次的结果。纯函数：同样的输入永远得到同样的输出。
func Apply(s Snapshot, e Event, c Config) (Snapshot, Change) {
	if !knownEvent(e.Name) {
		return s, Change{Note: NoteUnknown}
	}
	t, late := resolveTime(s, e, c)
	if t.IsZero() {
		return s, Change{Note: NoteUnknown}
	}
	recv := recvOf(e, t)

	status := ""
	switch e.Name {
	case EvStop:
		status = NormalizeStopStatus(e.Status)
	case EvSessionEnd:
		status = NormalizeEndReason(e.Status, EndClosed)
	case EvChatEnd:
		status = NormalizeEndReason(e.Status, EndCompleted)
	}

	// 去重：同一事件（名字 + 工具 + 状态）在极短窗口内重复，只算一次（交互模式下 afterAgentThought 会重复）。
	key := e.Name + "|" + e.Tool + "|" + status
	if key == s.lastKey && !s.lastKeyAt.IsZero() && absDuration(t.Sub(s.lastKeyAt)) < c.DedupeWindow {
		return s, Change{At: t, Note: NoteDuplicate}
	}
	s.lastKey, s.lastKeyAt = key, t

	// 乱序特例：中断序列里补发的 stop(error) 先到了，随后才收到更早发生的 stop(aborted)——以 aborted 为准。
	abortOverError := e.Name == EvStop && status == StopAborted && s.State == StateError &&
		absDuration(t.Sub(s.Since)) <= c.AbortFoldWindow
	if late && !abortOverError {
		return s, Change{At: t, Record: true, Note: NoteLate}
	}

	before := s
	ch := Change{At: t, Record: true}
	if !(abortOverError && t.Before(s.LastEventAt)) {
		s.LastEventAt, s.LastRecv = t, recv
	}

	switch e.Name {
	case EvSessionStart:
		if s.State == "" || s.State == StateEnded || s.StartedAt.IsZero() {
			s.StartedAt = t
			reviveSession(&s)
			s.TurnStartedAt, s.TurnEndedAt = time.Time{}, time.Time{}
			s.LastTool, s.ToolStartedAt = "", time.Time{}
		}
		setState(&s, StateIdle, "", t)

	case EvSessionEnd, EvChatEnd:
		if s.State == StateEnded {
			ch.Note = NoteAlreadyEnded
			break
		}
		endSession(&s, t, recv, status)

	case EvPrompt:
		if s.State == StateEnded {
			reviveSession(&s)
			ch.Note = NoteRevived
		}
		startTurn(&s, t)
		setState(&s, StateWorking, SubThinking, t)

	case EvThought, EvToolStart, EvToolEnd, EvResponse:
		applyActivity(&s, e, t, recv, c, &ch)

	case EvToolFail:
		switch s.State {
		case StateEnded:
			ch.Note = NoteEndedTail
		case StateAborted, StateError:
			ch.Note = NoteAbortTail
		case StateWorking:
			if strings.HasPrefix(s.Sub, SubToolPrefix) {
				s.Sub = SubThinking
			}
			ch.Note = NoteToolFailed
		default:
			ch.Note = NoteIgnored
		}

	case EvStop:
		applyStop(&s, status, t, abortOverError, c, &ch)

	case EvCompact:
		if s.State == StateEnded {
			ch.Note = NoteEndedTail
		}

	case EvApprovalWait:
		switch s.State {
		case "", StateIdle, StateWorking:
			startTurnIfNew(&s, t)
			setState(&s, StateWaitingApproval, "", t)
		default:
			ch.Note = NoteIgnored
		}

	case EvApprovalDone:
		if s.State == StateWaitingApproval {
			setState(&s, StateWorking, SubThinking, t)
		} else {
			ch.Note = NoteIgnored
		}
	}

	ch.StateChanged = before.State != s.State || before.Sub != s.Sub
	// 记下「现在这个状态是谁说的」：只认真正说明运行情况的来源，且事件要真的生效（重复、迟到、收尾尾巴都不算）。
	if statusSource(e.Source) && tookEffect(ch.Note) {
		s.Source = e.Source
	}
	ch.Durable = !durableEqual(before, s)

	// 思考心跳很密：状态照常刷新，但写进事件表的间隔不小于 ThoughtRecordEvery（状态变了的那一次除外）。
	if e.Name == EvThought {
		if !ch.StateChanged && !s.lastThoughtRec.IsZero() && t.Sub(s.lastThoughtRec) < c.ThoughtRecordEvery {
			ch.Record = false
		} else {
			s.lastThoughtRec = t
		}
	}
	return s, ch
}

// applyActivity 处理「说明 Agent 在干活」的事件：思考、工具开始 / 结束、回复。
func applyActivity(s *Snapshot, e Event, t, recv time.Time, c Config, ch *Change) {
	switch s.State {
	case StateEnded:
		// 会话结束之后到达的收尾事件（比如 chat end 那次工具调用自己的 postToolUse）不算重新开始；
		// 隔了一会儿才来的活动才说明对话被接着聊了。
		gap := t.Sub(s.EndedAt)
		if !s.endedRecv.IsZero() {
			gap = recv.Sub(s.endedRecv)
		}
		if gap <= c.EndedTail {
			ch.Note = NoteEndedTail
			return
		}
		reviveSession(s)
		ch.Note = NoteRevived
	case StateAborted, StateError:
		// 中断序列的尾巴（afterShellExecution、补发的工具失败等）不算重新开始。
		if t.Sub(s.Since) <= c.AbortFoldWindow {
			ch.Note = NoteAbortTail
			return
		}
	}

	sub := SubThinking
	switch e.Name {
	case EvToolStart:
		tool := e.Tool
		if tool == "" {
			tool = "unknown"
		}
		sub = SubToolPrefix + tool
		s.LastTool, s.ToolStartedAt = tool, t
	case EvToolEnd:
		if e.Tool != "" {
			s.LastTool = e.Tool
		}
	case EvResponse:
		sub = SubResponding
	}
	if s.State != StateWorking {
		startTurnIfNew(s, t)
	}
	setState(s, StateWorking, sub, t)
}

// applyStop 处理一轮结束。
func applyStop(s *Snapshot, status string, t time.Time, abortOverError bool, c Config, ch *Change) {
	if s.State == StateEnded {
		ch.Note = NoteEndedTail // 会话已经结束，迟到的 stop 只记录
		return
	}
	switch status {
	case StopCompleted:
		s.TurnEndedAt = t
		setState(s, StateIdle, "", t)
	case StopAborted:
		s.TurnEndedAt = t
		setState(s, StateAborted, "", t)
		if abortOverError {
			ch.Note = NoteAbortOverError
		}
	case StopError:
		// 中断序列会补发一个 stop(error)（实验 §6）：紧跟在 aborted 之后的那一个并入中断，不当成出错。
		if s.State == StateAborted && absDuration(t.Sub(s.Since)) <= c.AbortFoldWindow {
			ch.Note = NoteFoldedError
			return
		}
		s.TurnEndedAt = t
		setState(s, StateError, "", t)
	default:
		s.TurnEndedAt = t
		setState(s, StateIdle, "", t)
		ch.Note = NoteStopOther
	}
}
