// Package agentstate 是 Agent 会话状态机（#113，设计稿 04 §2）：纯函数，没有数据库、没有时钟、没有全局变量。
//
//   - Apply：把一个事件（hook、MCP 调用、屏幕扫描器……）折进会话快照，得到新的快照和「这次发生了什么」；
//   - Evaluate：给定快照和「现在」，推导展示用的状态（offline / waiting_you 叠加、卡住标志）；
//   - 身份显示名（label.go）与 hook 自报字段的清洗（wire.go）。
//
// 存储、节流写库、SSE 广播都在 store / mcp 里；这里只做判断，所以可以用表驱动测试把中断序列、乱序、时钟回拨这些边界一条条钉住。
package agentstate

import "time"

// State 是会话的状态。waiting_you 与 offline 只是展示态（见 Evaluate），不会出现在 Snapshot.State 里。
type State string

const (
	StateIdle            State = "idle"             // 本轮结束，等下一条
	StateWorking         State = "working"          // 在干活（子状态见 Snapshot.Sub）
	StateWaitingApproval State = "waiting_approval" // 等人批准工具调用（只有托管终端的屏幕扫描器能确定）
	StateAborted         State = "aborted"          // 被人中断
	StateError           State = "error"            // 出错结束
	StateEnded           State = "ended"            // 会话结束（sessionEnd 或 Agent 主动 chat end）

	StateWaitingYou State = "waiting_you" // 展示态：Agent 正阻塞等人回答（叠加在 working / idle 之上）
	StateOffline    State = "offline"     // 展示态：很久没有任何信号
)

// 工作中的子状态。工具调用写成 "tool:<名>"。
const (
	SubThinking   = "thinking"
	SubResponding = "responding"
	SubToolPrefix = "tool:"
)

// Source 是事件的来源。
type Source string

const (
	SourceHook  Source = "hook"
	SourceMCP   Source = "mcp"
	SourceTUI   Source = "tui"
	SourceGuard Source = "guard"
	SourceWeb   Source = "web"
	// SourceRun 是 Hub 自己读到的非交互运行的输出（agent -p --output-format stream-json）：
	// 每个思考、工具调用、回复与结束都是确定的事件，不依赖编辑器 hook；运行期间它比 hook 更权威（见 store 的运行权威标记）。
	SourceRun Source = "run"
)

// 事件名。其中 session_start / session_end / prompt / thought / tool_start / tool_end / tool_fail / response / stop / compact
// 对应 Cursor 的 hook，chat_end 由 Hub 在 Agent 主动结束会话时产生，approval_* 由托管终端的屏幕扫描器产生（第 3 阶段）。
const (
	EvSessionStart = "session_start" // sessionStart
	EvSessionEnd   = "session_end"   // sessionEnd
	EvPrompt       = "prompt"        // beforeSubmitPrompt：一轮新对话开始
	EvThought      = "thought"       // afterAgentThought：思考心跳
	EvToolStart    = "tool_start"    // preToolUse
	EvToolEnd      = "tool_end"      // postToolUse（旧版 hook 汇报的 tool 就是它）
	EvToolFail     = "tool_fail"     // postToolUseFailure
	EvResponse     = "response"      // afterAgentResponse
	EvStop         = "stop"          // stop：一轮结束（Status 是 completed / aborted / error / other）
	EvCompact      = "compact"       // preCompact
	EvChatEnd      = "chat_end"      // Agent 主动结束（Status 是 completed / blocked / aborted / handoff）
	EvApprovalWait = "approval_wait" // 屏幕出现 Waiting for approval
	EvApprovalDone = "approval_done" // 审批等待结束
)

// stop 事件的结束状态。其他取值一律当作 StopOther。
const (
	StopCompleted = "completed"
	StopAborted   = "aborted"
	StopError     = "error"
	StopOther     = "other"
)

// 会话结束的原因（EndReason）。
const (
	EndCompleted = "completed"
	EndBlocked   = "blocked"
	EndAborted   = "aborted"
	EndHandoff   = "handoff"
	EndError     = "error"
	EndClosed    = "closed" // sessionEnd 没有给出更具体的原因
)

// Change.Note 的取值：说明这次事件为什么没有（或怎样）改变状态，写进事件的 extra 里便于排查。
const (
	NoteUnknown        = "unknown_event"
	NoteDuplicate      = "duplicate"
	NoteLate           = "late"
	NoteEndedTail      = "ended_tail"
	NoteAbortTail      = "abort_tail"
	NoteFoldedError    = "folded_error"
	NoteRevived        = "revived"
	NoteAbortOverError = "abort_over_error"
	NoteAlreadyEnded   = "already_ended"
	NoteStopOther      = "stop_other"
	NoteToolFailed     = "tool_failed"
	NoteIgnored        = "ignored"
	// NoteRunAuthority：这个对话正由 Hub 读非交互运行的输出判定状态，hook 的汇报只留记录、不改状态（由 store 标注）。
	NoteRunAuthority = "run_authority"
)

// Event 是喂给状态机的一个事件。
type Event struct {
	Source   Source
	Name     string        // Ev* 之一
	At       time.Time     // 发送端自报的时间；零值 = 没有（以接收时间为准）
	Received time.Time     // Hub 收到的时间；Apply 的时间判断以它为兜底，调用方必须填
	Tool     string        // tool_start / tool_end / tool_fail 的工具名（已清洗）
	Status   string        // stop 的结束状态，或 session_end / chat_end 的原因
	Duration time.Duration // tool_end 的耗时（只用于记录）
}

// Snapshot 是一个会话（一个 Cursor 对话）在某一时刻的状态。零值就是「还没见过」（State == ""）。
// 小写字段只在内存里：去重与节流用，Hub 重启后清零，不影响正确性。
type Snapshot struct {
	State State
	Sub   string    // 工作中的子状态：thinking / responding / tool:<名>；其他状态为空
	Since time.Time // 进入当前 State 的时间（只换子状态不重置）
	// Source 是最近一次「正常生效」的 hook / 屏幕 / 运行输出事件的来源（MCP、守护、Web 这类 Hub 侧的事件不算）：
	// 告诉前端「现在这个状态是谁说的」。空 = 没有记录（旧数据）。
	Source Source

	StartedAt time.Time
	EndedAt   time.Time
	EndReason string

	LastEventAt time.Time // 最近一次被采纳的事件的有效时间
	LastRecv    time.Time // 那次事件的接收时间（用来识别发送端时钟回拨）

	LastTool      string
	ToolStartedAt time.Time // 当前（或最近一次）工具开始的时间
	TurnStartedAt time.Time
	TurnEndedAt   time.Time

	lastKey        string
	lastKeyAt      time.Time
	lastThoughtRec time.Time
	endedRecv      time.Time // 结束事件的接收时间（识别「结束之后到达的收尾事件」，见 apply.go）
}

// Change 描述一次 Apply 的结果。
type Change struct {
	At           time.Time // 事件的有效时间（发送端时间可信就用它，否则是接收时间）
	Record       bool      // 值得写进 agent_events（重复事件、被节流的思考心跳为 false）
	StateChanged bool      // State 或 Sub 变了（要不要给前端推 SSE）
	Durable      bool      // 快照里除时间戳之外的字段变了（要写库）；只是心跳时为 false
	Note         string    // 见 Note* 常量；空串 = 事件正常生效
}

// Config 是状态机的时间参数。零值不可用，用 DefaultConfig。
type Config struct {
	DedupeWindow       time.Duration // 同一事件在这个窗口内重复出现只算一次
	ThoughtRecordEvery time.Duration // 思考心跳写进事件表的最小间隔（状态照常刷新）
	AbortFoldWindow    time.Duration // 中断之后这个窗口内补发的 stop(error) / 收尾事件并入中断序列
	EndedTail          time.Duration // 会话结束之后这个窗口内到达的收尾事件不算「重新开始」
	LateWindow         time.Duration // 比最近事件早不到这个窗口的事件算乱序迟到；更早的视为发送端时钟回拨，改用接收时间
	FutureTolerance    time.Duration // 发送端时间领先接收时间超过这个值就不可信，改用接收时间
}

// DefaultConfig 返回设计稿 04 §2.3 的默认参数。
func DefaultConfig() Config {
	return Config{
		DedupeWindow:       50 * time.Millisecond,
		ThoughtRecordEvery: 5 * time.Second,
		AbortFoldWindow:    2 * time.Second,
		EndedTail:          10 * time.Second,
		LateWindow:         5 * time.Second,
		FutureTolerance:    2 * time.Minute,
	}
}
