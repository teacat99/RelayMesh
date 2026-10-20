package termproto

import (
	"encoding/binary"
	"encoding/hex"
	"regexp"
)

// 模型执行（非 PTY 的「一次调用 = 一次运行」）协议。
//
// 与人工终端的 PTY 会话并存、互不干扰：运行没有 TTY、stdin 关闭，输出分 stdout / stderr 两路，
// 完整输出只落在主机本地文件里（头 + 尾截断），Hub 只收到实时片段（仅用于给人看）、
// 结束时的结果摘要与短预览，从不存输出（INV-M4）。运行进程由主机上的守护进程持有，
// Agent 重启不影响运行，结束事件在 Agent 重连后补报。
const (
	TRun         = "run"           // Hub→Agent→守护进程：启动一次运行
	TRunStarted  = "run_started"   // 守护进程→Agent→Hub：进程已启动
	TRunFailed   = "run_failed"    // 守护进程→Agent→Hub：没能启动（含本机一票否决）
	TRunEnd      = "run_end"       // 守护进程→Agent→Hub：运行结束
	TRunCancel   = "run_cancel"    // Hub→Agent→守护进程：取消运行
	TRunAck      = "run_ack"       // Hub→Agent→守护进程：结束事件已处理，可停止补报
	TRuns        = "runs"          // 守护进程→Agent→Hub：控制端接上时的运行清单（用于对账）
	TRunsSync    = "runs_sync"     // Hub→Agent→守护进程：请求重发运行清单与尚未确认的结束事件
	TRunRead     = "run_read"      // Hub→Agent→守护进程：读取已保存的输出
	TRunReadFail = "run_read_fail" // 守护进程→Agent→Hub：读取失败
)

// 二进制帧类型（延续 FrameOutput..FrameRecord 的编号）。
const (
	// FrameRunOut 是实时输出片段：[0x05][运行 ID 16B][流 u8][偏移 u64][净荷]（守护进程→Hub）。
	// 偏移是该流的累计字节位置；实时流可能因限速而跳过一段，跳过的部分仍完整记录在主机文件里。
	FrameRunOut byte = 0x05
	// FrameRunData 是 run_read 的应答：[0x06][req u32][标志 u8][流 u8][起点 u64][下一偏移 u64][总量 u64][净荷]。
	FrameRunData byte = 0x06
)

// 输出流编号。
const (
	StreamStdout byte = 1
	StreamStderr byte = 2
)

// StreamName 返回流的名称；未知编号返回空串。
func StreamName(s byte) string {
	switch s {
	case StreamStdout:
		return "stdout"
	case StreamStderr:
		return "stderr"
	}
	return ""
}

// ParseStream 把流名称解析成编号。
func ParseStream(name string) (byte, bool) {
	switch name {
	case "stdout":
		return StreamStdout, true
	case "stderr":
		return StreamStderr, true
	}
	return 0, false
}

// 运行的结束状态（RunEnd.State，守护进程上报）。
const (
	RunExited    = "exited"    // 进程自行结束（含非 0 退出码）
	RunSignaled  = "signaled"  // 被信号终止（不是本机取消或超时造成的）
	RunTimeout   = "timeout"   // 超过 timeout_sec，被终止
	RunCancelled = "cancelled" // 收到取消请求，被终止
	RunLost      = "lost"      // 守护进程重启后发现进程已不存在，结果未知
)

// 运行没能启动的原因（RunFailed.Reason）。
const (
	RunFailDenied      = "denied_by_host"   // 本机一票否决（--deny-model-exec）
	RunFailBusy        = "busy"             // 本机同时运行数达到保护上限
	RunFailBad         = "bad_request"      // 参数无效
	RunFailCwd         = "bad_cwd"          // 工作目录不存在或不是目录
	RunFailNoShell     = "no_shell"         // 找不到可用的 shell
	RunFailSpawn       = "spawn_failed"     // 进程启动失败
	RunFailUnavailable = "exec_unavailable" // 本机没有可用于保存运行输出的目录
	RunFailDuplicate   = "duplicate"        // 同一运行 ID 已存在
	RunFailHolder      = "holder_unavailable"
	RunFailDraining    = "shutting_down"
)

// 运行输出读取失败的原因（RunReadFail.Reason）。
const (
	RunReadNotFound = "not_found"
	RunReadBusy     = "busy"
	RunReadBad      = "bad_request"
	RunReadError    = "error"
	RunReadHolder   = "holder_unavailable"
)

// 运行相关的尺寸限制。
const (
	// MaxRunCommandBytes 限制命令文本长度：run 帧要能放进 64 KiB 的控制帧（含 JSON 转义膨胀），所以远低于 64 KiB。
	MaxRunCommandBytes   = 24 << 10
	MaxRunCwdRunes       = 1024
	DefaultRunTimeoutSec = 120
	MaxRunTimeoutSec     = 7 * 24 * 3600

	// RunPreviewHead / RunPreviewTail 是结束时随 run_end 上报、Hub 落库的预览大小（合并 stdout / stderr，按到达顺序）。
	RunPreviewHead = 4 << 10
	RunPreviewTail = 2 << 10
	// RunInlineMax 是 run_end 里直接带上的单路输出的上限：小输出不需要再走一次读取往返。
	RunInlineMax = 8 << 10

	// DefaultRunRead / MaxRunRead 是一次 run_read 的默认 / 最大字节数；MaxRunDataFrame 是单个应答帧的净荷上限。
	DefaultRunRead  = 64 << 10
	MaxRunRead      = 256 << 10
	MaxRunDataFrame = 32 << 10
)

// 应答帧标志位（FrameRunData）。
const (
	RunFlagEOF     byte = 1 << 0 // 已读到这一路输出的末尾，且运行已经结束
	RunFlagGap     byte = 1 << 1 // 请求的起点落在被丢弃的中间部分，已从下一段可用内容开始
	RunFlagLast    byte = 1 << 2 // 本次请求的最后一帧
	RunFlagRunning byte = 1 << 3 // 运行仍在进行（之后可能还有新的输出）
)

var runIDPattern = regexp.MustCompile(`^[0-9a-f]{32}$`)

// ValidRunID 判断运行 ID 是否为 128 位小写十六进制。
func ValidRunID(s string) bool { return runIDPattern.MatchString(s) }

// Run 请求在主机上执行一条命令（Hub→Agent→守护进程）。
//
// 没有 env 字段是刻意的：模型不能给进程注入环境变量（LD_PRELOAD 一类的风险）。
type Run struct {
	T   string `json:"t"`
	ID  string `json:"id"`
	Cmd string `json:"cmd"`
	// Shell 为空表示自动选择（Unix：bash，其次 sh；Windows：pwsh、powershell、cmd）；
	// 其余取值限定为 bash / sh / zsh / pwsh / powershell / cmd。
	Shell string `json:"shell,omitempty"`
	// Cwd 为空表示该账户的家目录。
	Cwd string `json:"cwd,omitempty"`
	// TimeoutSec 为 0 时取 DefaultRunTimeoutSec；上限 MaxRunTimeoutSec。
	TimeoutSec int `json:"timeout_sec,omitempty"`
}

// RunStarted 报告进程已启动。
type RunStarted struct {
	T       string `json:"t"`
	ID      string `json:"id"`
	PID     int    `json:"pid,omitempty"`
	Shell   string `json:"shell,omitempty"` // 实际使用的 shell（名称，不含路径）
	Cwd     string `json:"cwd,omitempty"`   // 实际工作目录
	Started int64  `json:"started"`         // 启动时间（Unix 毫秒，主机时钟）
}

// RunFailed 报告运行没能启动。
type RunFailed struct {
	T      string `json:"t"`
	ID     string `json:"id"`
	Reason string `json:"reason"`
	Detail string `json:"detail,omitempty"`
}

// RunEnd 报告运行结束。
type RunEnd struct {
	T      string `json:"t"`
	ID     string `json:"id"`
	State  string `json:"state"`
	Code   *int   `json:"code,omitempty"`
	Signal string `json:"signal,omitempty"`
	// DurMS 是运行耗时（毫秒）；OutBytes / ErrBytes 是两路输出产生的总字节数。
	DurMS    int64 `json:"dur_ms"`
	OutBytes int64 `json:"out_bytes"`
	ErrBytes int64 `json:"err_bytes"`
	// OutDropped / ErrDropped 是因「头 + 尾」截断而没有保留的中间字节数。
	OutDropped int64 `json:"out_dropped,omitempty"`
	ErrDropped int64 `json:"err_dropped,omitempty"`
	// Out / Err 在对应输出不超过 RunInlineMax 时直接给出全文；否则为空，需要用 run_read 读取。
	Out []byte `json:"out,omitempty"`
	Err []byte `json:"err,omitempty"`
	// PrevHead / PrevTail 是合并两路输出（按到达顺序）后的开头与结尾预览，仅用于列表与卡片。
	PrevHead []byte `json:"prev_head,omitempty"`
	PrevTail []byte `json:"prev_tail,omitempty"`
	Reason   string `json:"reason,omitempty"` // 补充说明（如 lost 时的原因）
}

// RunBrief 是运行清单里的一项（仍在运行的）。
type RunBrief struct {
	ID      string `json:"id"`
	PID     int    `json:"pid,omitempty"`
	Started int64  `json:"started"`
}

// Runs 是控制端接上守护进程时收到的运行清单：仍在运行的运行。
// 尚未被确认的结束事件会紧接着以独立的 run_end 帧补报（每个事件可能带有较大的内联输出，不适合塞进同一帧）。
type Runs struct {
	T       string     `json:"t"`
	Running []RunBrief `json:"running"`
}

// RunsSync 请求守护进程重发运行清单（runs）与尚未被确认的结束事件（run_end）。
// Agent 与 Hub 之间的链路断开期间产生的结束事件无处可投，重连后靠它补回；重发是幂等的。
type RunsSync struct {
	T string `json:"t"`
}

// MaxRunsPerFrame 是一个 runs 帧最多接受的条数。
const MaxRunsPerFrame = 256

// RunCancel 请求取消运行（先 TERM，宽限后 KILL，作用于整个进程组）。
type RunCancel struct {
	T  string `json:"t"`
	ID string `json:"id"`
	// Reason 是取消来源：user | model | pause | revoked | gate_closed | host_removed | token_changed | unknown_run（旧版本只有 system）。
	// 守护进程原样回报在 RunEnd.Reason 里（最长 32 个字符），Hub 把它存成运行记录的 deny_reason，界面据此解释「为什么被取消」。
	Reason string `json:"reason,omitempty"`
}

// RunAck 表示 Hub 已处理该运行的结束事件，守护进程不必再补报。
type RunAck struct {
	T  string `json:"t"`
	ID string `json:"id"`
}

// RunRead 请求读取运行已保存的输出：从某一路的 Off 起至多 Max 字节。
type RunRead struct {
	T      string `json:"t"`
	Req    uint32 `json:"req"`
	ID     string `json:"id"`
	Stream string `json:"stream"` // stdout | stderr
	Off    uint64 `json:"off,omitempty"`
	Max    int    `json:"max,omitempty"`
}

// RunReadFail 是 run_read 的失败应答。
type RunReadFail struct {
	T      string `json:"t"`
	Req    uint32 `json:"req"`
	Reason string `json:"reason"`
}

// ---- 清洗（来自 Agent 的字段一律不可信） ----

// ValidRunState 判断结束状态是否为已知值。
func ValidRunState(s string) bool {
	switch s {
	case RunExited, RunSignaled, RunTimeout, RunCancelled, RunLost:
		return true
	}
	return false
}

// Sanitized 返回净化并限长后的副本；ID 非法时 ok=false。
func (e RunEnd) Sanitized() (RunEnd, bool) {
	if !ValidRunID(e.ID) {
		return RunEnd{}, false
	}
	out := e
	out.T = TRunEnd
	if !ValidRunState(out.State) {
		out.State = RunLost
	}
	out.Signal = SanitizeText(e.Signal, 24)
	out.Reason = SanitizeText(e.Reason, 96)
	nonNeg := func(v int64) int64 {
		if v < 0 {
			return 0
		}
		return v
	}
	out.DurMS = nonNeg(e.DurMS)
	out.OutBytes, out.ErrBytes = nonNeg(e.OutBytes), nonNeg(e.ErrBytes)
	out.OutDropped, out.ErrDropped = nonNeg(e.OutDropped), nonNeg(e.ErrDropped)
	out.Out = clipBytes(e.Out, RunInlineMax)
	out.Err = clipBytes(e.Err, RunInlineMax)
	out.PrevHead = clipBytes(e.PrevHead, RunPreviewHead)
	out.PrevTail = clipBytes(e.PrevTail, RunPreviewTail)
	return out, true
}

// Sanitized 返回净化后的副本；ID 非法时 ok=false。
func (s RunStarted) Sanitized() (RunStarted, bool) {
	if !ValidRunID(s.ID) {
		return RunStarted{}, false
	}
	out := s
	out.T = TRunStarted
	out.Shell = SanitizeText(s.Shell, 32)
	out.Cwd = SanitizeText(s.Cwd, 256)
	if out.PID < 0 {
		out.PID = 0
	}
	return out, true
}

// Sanitized 返回净化后的副本；ID 非法时 ok=false。
func (f RunFailed) Sanitized() (RunFailed, bool) {
	if !ValidRunID(f.ID) {
		return RunFailed{}, false
	}
	out := f
	out.T = TRunFailed
	out.Reason = SanitizeText(f.Reason, 32)
	out.Detail = SanitizeText(f.Detail, 160)
	return out, true
}

// Sanitized 返回净化后的清单：丢弃非法项与超出上限的多余项。
func (r Runs) Sanitized() Runs {
	out := Runs{T: TRuns, Running: make([]RunBrief, 0, len(r.Running))}
	for _, b := range r.Running {
		if len(out.Running) >= MaxRunsPerFrame {
			break
		}
		if !ValidRunID(b.ID) {
			continue
		}
		if b.PID < 0 {
			b.PID = 0
		}
		out.Running = append(out.Running, b)
	}
	return out
}

func clipBytes(b []byte, limit int) []byte {
	if len(b) > limit {
		return b[:limit]
	}
	return b
}

// ---- 二进制帧编解码 ----

const (
	runIDLen      = 16
	runOutHdrLen  = 1 + runIDLen + 1 + 8
	runDataHdrLen = 1 + 4 + 1 + 1 + 8 + 8 + 8
)

// EncodeRunOut 编码实时输出片段；id 必须是合法的运行 ID（否则返回 nil）。
func EncodeRunOut(id string, stream byte, offset uint64, payload []byte) []byte {
	raw, ok := runIDBytes(id)
	if !ok {
		return nil
	}
	buf := make([]byte, runOutHdrLen+len(payload))
	buf[0] = FrameRunOut
	copy(buf[1:1+runIDLen], raw[:])
	buf[1+runIDLen] = stream
	binary.BigEndian.PutUint64(buf[2+runIDLen:runOutHdrLen], offset)
	copy(buf[runOutHdrLen:], payload)
	return buf
}

// DecodeRunOut 解码实时输出片段；返回的 payload 与 frame 共享底层数组。
func DecodeRunOut(frame []byte) (id string, stream byte, offset uint64, payload []byte, err error) {
	if len(frame) < runOutHdrLen {
		return "", 0, 0, nil, ErrShortFrame
	}
	if frame[0] != FrameRunOut {
		return "", 0, 0, nil, ErrWrongFrame
	}
	payload = frame[runOutHdrLen:]
	if len(payload) > MaxDataPayload {
		return "", 0, 0, nil, ErrPayloadTooBig
	}
	stream = frame[1+runIDLen]
	if StreamName(stream) == "" {
		return "", 0, 0, nil, ErrWrongFrame
	}
	return hex.EncodeToString(frame[1 : 1+runIDLen]), stream, binary.BigEndian.Uint64(frame[2+runIDLen : runOutHdrLen]), payload, nil
}

// RunChunk 是 FrameRunData 解码后的内容。
type RunChunk struct {
	Req    uint32
	Flags  byte
	Stream byte
	Off    uint64 // 本帧第一个字节在该路输出中的位置
	Next   uint64 // 继续读取应使用的偏移（跳过被丢弃部分后可能大于 Off+len(Data)）
	Total  uint64 // 该路输出到目前为止产生的总字节数
	Data   []byte
}

// EncodeRunData 编码 run_read 的应答帧。
func EncodeRunData(c RunChunk) []byte {
	buf := make([]byte, runDataHdrLen+len(c.Data))
	buf[0] = FrameRunData
	binary.BigEndian.PutUint32(buf[1:5], c.Req)
	buf[5] = c.Flags
	buf[6] = c.Stream
	binary.BigEndian.PutUint64(buf[7:15], c.Off)
	binary.BigEndian.PutUint64(buf[15:23], c.Next)
	binary.BigEndian.PutUint64(buf[23:31], c.Total)
	copy(buf[runDataHdrLen:], c.Data)
	return buf
}

// DecodeRunData 解码 run_read 的应答帧；返回的 Data 与 frame 共享底层数组。
func DecodeRunData(frame []byte) (RunChunk, error) {
	if len(frame) < runDataHdrLen {
		return RunChunk{}, ErrShortFrame
	}
	if frame[0] != FrameRunData {
		return RunChunk{}, ErrWrongFrame
	}
	data := frame[runDataHdrLen:]
	if len(data) > MaxRunDataFrame {
		return RunChunk{}, ErrPayloadTooBig
	}
	if StreamName(frame[6]) == "" {
		return RunChunk{}, ErrWrongFrame
	}
	return RunChunk{
		Req:    binary.BigEndian.Uint32(frame[1:5]),
		Flags:  frame[5],
		Stream: frame[6],
		Off:    binary.BigEndian.Uint64(frame[7:15]),
		Next:   binary.BigEndian.Uint64(frame[15:23]),
		Total:  binary.BigEndian.Uint64(frame[23:31]),
		Data:   data,
	}, nil
}

func runIDBytes(id string) ([runIDLen]byte, bool) {
	var out [runIDLen]byte
	if !ValidRunID(id) {
		return out, false
	}
	b, err := hex.DecodeString(id)
	if err != nil || len(b) != runIDLen {
		return out, false
	}
	copy(out[:], b)
	return out, true
}
