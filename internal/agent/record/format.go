// Package record 实现终端会话的**客户端侧**记录：终端上显示什么就记录什么。
//
// 设计要点：
//   - 记录只写在运行终端的主机本地（Agent 数据目录），服务端数据库与审计日志永不保存终端内容（INV-M4）；
//   - 记录的是 PTY 的输出字节流（包含 shell 对键入内容的回显），外加窗口尺寸变化与时间标记；
//     没有独立的按键流，也不做口令屏蔽——关闭回显的口令输入自然不会出现，显示出来的一切都会被记录；
//   - 写盘走异步队列，任何磁盘问题（写满、权限、变慢）都只会让记录出现「缺口」，绝不影响终端本身；
//   - 文件按 1 MiB 分段，封存后的分段在后台压缩；单会话滚动配额、整机配额、保留期与低磁盘保护均可配置。
//
// 文件布局（目录 0700、文件 0600）：
//
//	<Dir>/sessions/<sid>/meta.json        会话元数据（创建时写入，结束时更新）
//	<Dir>/sessions/<sid>/index.jsonl      追加式日志：分段起止、压缩、清理与缺口
//	<Dir>/sessions/<sid>/seg-000001.rec   原始分段（封存后被压缩为 .rec.gz）
package record

import (
	"encoding/binary"
	"encoding/json"
	"errors"
)

const (
	// segMagic 是每个分段文件的开头 4 字节。
	segMagic = "RMR1"
	// MaxEventPayload 是单个事件净荷的上限；更长的输出会被切成多个事件。
	MaxEventPayload = 32 << 10
	// minPageBytes 是单页读取的下限：必须容得下最大的一个事件。
	minPageBytes = MaxEventPayload + 64
	// MaxPageBytes 是单次读取返回的字节上限。
	MaxPageBytes = 48 << 10
)

// 事件类别。
const (
	KindOutput byte = 1 // 终端显示的输出字节（含 shell 对键入内容的回显）
	KindResize byte = 2 // 窗口尺寸变化：rows u16 BE + cols u16 BE
	KindMark   byte = 3 // 标记：JSON（见 Mark）
)

// 标记类型（Mark.T）。
const (
	MarkSeg = "seg" // 分段起点：带绝对时间与当前窗口尺寸
	MarkGap = "gap" // 缺口：这一段时间内的输出没有被记录
	MarkEnd = "end" // 会话结束
)

// 缺口原因（Mark.Reason / 会话状态）。
const (
	GapBacklog    = "backlog"     // 写盘跟不上，队列溢出
	GapDiskLow    = "disk_low"    // 磁盘剩余空间低于保护线
	GapWriteError = "write_error" // 写文件失败
	GapPolicyOff  = "policy_off"  // 服务端策略临时关闭了录制
)

// Mark 是 KindMark 事件的 JSON 净荷。
type Mark struct {
	T      string `json:"t"`
	Seq    uint32 `json:"seq,omitempty"`    // seg：分段序号
	T0     int64  `json:"t0,omitempty"`     // seg：分段起点的绝对时间（Unix 毫秒）
	Rows   uint16 `json:"rows,omitempty"`   // seg：此刻的窗口尺寸
	Cols   uint16 `json:"cols,omitempty"`   // seg：此刻的窗口尺寸
	Reason string `json:"reason,omitempty"` // gap：原因；end：结束原因
	Bytes  int64  `json:"bytes,omitempty"`  // gap：丢失的输出字节数（尽力统计）
	Code   *int   `json:"code,omitempty"`   // end：退出码
	Signal string `json:"signal,omitempty"` // end：终止信号
}

// Event 是解码后的一个事件。
type Event struct {
	Kind    byte
	DT      uint64 // 距上一个事件的毫秒数（分段内第一个事件为 0）
	Payload []byte
}

// AppendEvent 把一个事件编码追加到 dst：kind(1) | dt uvarint | len uvarint | 净荷。
func AppendEvent(dst []byte, kind byte, dtMS uint64, payload []byte) []byte {
	dst = append(dst, kind)
	dst = binary.AppendUvarint(dst, dtMS)
	dst = binary.AppendUvarint(dst, uint64(len(payload)))
	return append(dst, payload...)
}

var errShortEvent = errors.New("record: incomplete event")

// DecodeEvent 从 data 开头解出一个完整事件，返回事件与消耗的字节数。
// 数据不足一个完整事件时返回 errShortEvent（写入中的分段尾部属于正常情况）。
func DecodeEvent(data []byte) (Event, int, error) {
	if len(data) < 3 {
		return Event{}, 0, errShortEvent
	}
	kind := data[0]
	pos := 1
	dt, n := binary.Uvarint(data[pos:])
	if n == 0 {
		return Event{}, 0, errShortEvent
	}
	if n < 0 {
		return Event{}, 0, errors.New("record: bad event time")
	}
	pos += n
	ln, n := binary.Uvarint(data[pos:])
	if n == 0 {
		return Event{}, 0, errShortEvent
	}
	if n < 0 || ln > MaxEventPayload {
		return Event{}, 0, errors.New("record: bad event length")
	}
	pos += n
	if uint64(len(data)-pos) < ln {
		return Event{}, 0, errShortEvent
	}
	return Event{Kind: kind, DT: dt, Payload: data[pos : pos+int(ln)]}, pos + int(ln), nil
}

// DecodeEvents 尽可能多地解出完整事件，返回事件列表与消耗的字节数；遇到损坏数据返回错误。
func DecodeEvents(data []byte) ([]Event, int, error) {
	var out []Event
	used := 0
	for used < len(data) {
		ev, n, err := DecodeEvent(data[used:])
		if err != nil {
			if errors.Is(err, errShortEvent) {
				return out, used, nil
			}
			return out, used, err
		}
		out = append(out, ev)
		used += n
	}
	return out, used, nil
}

func encodeResize(rows, cols uint16) []byte {
	b := make([]byte, 4)
	binary.BigEndian.PutUint16(b[0:], rows)
	binary.BigEndian.PutUint16(b[2:], cols)
	return b
}

// DecodeResize 解出 KindResize 事件的净荷。
func DecodeResize(p []byte) (rows, cols uint16, ok bool) {
	if len(p) != 4 {
		return 0, 0, false
	}
	return binary.BigEndian.Uint16(p[0:]), binary.BigEndian.Uint16(p[2:]), true
}

func encodeMark(m Mark) []byte {
	b, _ := json.Marshal(m)
	return b
}

// DecodeMark 解出 KindMark 事件的净荷。
func DecodeMark(p []byte) (Mark, bool) {
	var m Mark
	if json.Unmarshal(p, &m) != nil || m.T == "" {
		return Mark{}, false
	}
	return m, true
}
