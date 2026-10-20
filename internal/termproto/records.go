package termproto

import (
	"encoding/binary"
	"encoding/json"
)

// 终端记录（客户端侧）读取协议：Hub 向 Agent 请求某个会话已记录的终端显示内容。
// 内容只在人类读取时按页经 Agent→Hub 流出，Hub 不落盘（INV-M4）。
const (
	TRecordRead = "record_read" // Hub→Agent：读取一段记录内容
	TRecordStat = "record_stat" // Hub→Agent：读取记录索引
	TRecordInfo = "record_info" // Agent→Hub：索引应答
	TRecordFail = "record_fail" // Agent→Hub：请求失败
)

// FrameRecord 是 Agent→Hub 的记录内容帧（一次 record_read 应答由一到多帧组成）：
// [0x04][req u32][flags u8][seg u32][off u32][next_seg u32][next_off u32][事件字节]
// 事件字节与主机上分段文件里的编码完全一致（kind | dt uvarint | len uvarint | 净荷）。
const FrameRecord byte = 0x04

// 记录帧标志位。
const (
	RecFlagEOF     byte = 1 << 0 // 已读到记录末尾（会话仍在运行时之后可能还有新内容）
	RecFlagSkipped byte = 1 << 1 // 请求的起点已被清理，数据从最早保留的分段开头开始
	RecFlagLast    byte = 1 << 2 // 本次请求的最后一帧
)

// 记录读取的尺寸上限。
const (
	MaxRecordFrameData = 48 << 10 // 单帧事件字节上限（须小于 MaxControlFrame）
	MaxRecordRead      = 1 << 20  // 单次请求返回的字节上限
	DefaultRecordRead  = 256 << 10
	MaxRecordInfoJSON  = 56 << 10
)

// 录制状态（SessionInfo.Rec）。
const (
	RecOn        = "on"        // 正在录制
	RecPaused    = "paused"    // 暂停（磁盘不足或服务端策略临时关闭）
	RecError     = "error"     // 写盘失败，重试中
	RecOff       = "off"       // 服务端策略关闭了录制
	RecVeto      = "veto"      // 本机一票否决（--no-record）
	RecIncognito = "incognito" // 无痕会话
)

// 记录请求失败原因（RecordFail.Reason）。
const (
	RecReasonNotFound = "not_found" // 没有该会话的记录（无痕、已过期或已被清理）
	RecReasonBusy     = "busy"      // Agent 正忙于其他读取
	RecReasonBad      = "bad_request"
	RecReasonError    = "error"
)

// RecordRead 请求读取一段记录内容：从 (Seg, Off) 起至多 Max 字节；Seg 为 0 表示从最早保留的分段开始。
type RecordRead struct {
	T   string `json:"t"`
	Req uint32 `json:"req"`
	SID string `json:"sid"`
	Seg uint32 `json:"seg,omitempty"`
	Off uint32 `json:"off,omitempty"`
	Max int    `json:"max,omitempty"`
}

// RecordStat 请求读取会话记录的索引信息。
type RecordStat struct {
	T   string `json:"t"`
	Req uint32 `json:"req"`
	SID string `json:"sid"`
}

// RecordInfo 是索引应答；Info 是主机端 record.Info 的 JSON（不含任何终端内容）。
type RecordInfo struct {
	T    string          `json:"t"`
	Req  uint32          `json:"req"`
	SID  string          `json:"sid"`
	Info json.RawMessage `json:"info"`
}

// RecordFail 是记录请求的失败应答。
type RecordFail struct {
	T      string `json:"t"`
	Req    uint32 `json:"req"`
	Reason string `json:"reason"`
}

// RecordChunk 是 FrameRecord 解码后的内容。
type RecordChunk struct {
	Req     uint32
	Flags   byte
	Seg     uint32
	Off     uint32
	NextSeg uint32
	NextOff uint32
	Data    []byte
}

const recordHeaderLen = 1 + 4 + 1 + 4*4

// EncodeRecord 编码记录内容帧。
func EncodeRecord(c RecordChunk) []byte {
	buf := make([]byte, recordHeaderLen+len(c.Data))
	buf[0] = FrameRecord
	binary.BigEndian.PutUint32(buf[1:5], c.Req)
	buf[5] = c.Flags
	binary.BigEndian.PutUint32(buf[6:10], c.Seg)
	binary.BigEndian.PutUint32(buf[10:14], c.Off)
	binary.BigEndian.PutUint32(buf[14:18], c.NextSeg)
	binary.BigEndian.PutUint32(buf[18:22], c.NextOff)
	copy(buf[recordHeaderLen:], c.Data)
	return buf
}

// DecodeRecord 解码记录内容帧；返回的 Data 与 frame 共享底层数组。
func DecodeRecord(frame []byte) (RecordChunk, error) {
	if len(frame) < recordHeaderLen {
		return RecordChunk{}, ErrShortFrame
	}
	if frame[0] != FrameRecord {
		return RecordChunk{}, ErrWrongFrame
	}
	data := frame[recordHeaderLen:]
	if len(data) > MaxRecordFrameData {
		return RecordChunk{}, ErrPayloadTooBig
	}
	return RecordChunk{
		Req:     binary.BigEndian.Uint32(frame[1:5]),
		Flags:   frame[5],
		Seg:     binary.BigEndian.Uint32(frame[6:10]),
		Off:     binary.BigEndian.Uint32(frame[10:14]),
		NextSeg: binary.BigEndian.Uint32(frame[14:18]),
		NextOff: binary.BigEndian.Uint32(frame[18:22]),
		Data:    data,
	}, nil
}
