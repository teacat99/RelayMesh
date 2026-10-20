package agent

import (
	"encoding/json"
	"errors"

	"github.com/coder/websocket"
	"github.com/teacat99/RelayMesh/v2/internal/agent/record"
	"github.com/teacat99/RelayMesh/v2/internal/termproto"
)

// maxConcurrentRecordReads 是同时进行的记录读取数。读取只碰磁盘、不碰终端，但仍要有界，
// 避免一个浏览器的大量并发请求拖慢本机。
const maxConcurrentRecordReads = 2

// recordSource 是 Agent 读取本机终端记录的来源：内嵌模式下是进程内的存储，
// 外置模式下是守护进程写下的目录的只读视图（守护进程停止时历史记录仍可读取）。
type recordSource interface {
	Info(sid string) (*record.Info, error)
	Read(sid string, seg, off uint32, max int) (*record.Page, error)
}

// setRecords 设置记录来源；必须在 relay 开始处理帧之前调用。
func (r *relay) setRecords(rs recordSource) { r.records = rs }

func (r *relay) failRecord(req uint32, reason string) {
	_ = r.sendJSON(termproto.RecordFail{T: termproto.TRecordFail, Req: req, Reason: reason})
}

// handleRecordRead 受理一次记录读取请求：在独立协程里读盘，绝不阻塞 Hub 连接的读循环。
func (r *relay) handleRecordRead(m termproto.RecordRead) {
	if !record.ValidSID(m.SID) {
		r.failRecord(m.Req, termproto.RecReasonBad)
		return
	}
	select {
	case r.recSem <- struct{}{}:
	default:
		r.failRecord(m.Req, termproto.RecReasonBusy)
		return
	}
	go func() {
		defer func() { <-r.recSem }()
		r.serveRecordRead(m)
	}()
}

func (r *relay) serveRecordRead(m termproto.RecordRead) {
	rs := r.records
	if rs == nil {
		r.failRecord(m.Req, termproto.RecReasonNotFound)
		return
	}
	limit := m.Max
	if limit <= 0 {
		limit = termproto.DefaultRecordRead
	}
	if limit > termproto.MaxRecordRead {
		limit = termproto.MaxRecordRead
	}
	seg, off := m.Seg, m.Off
	sent := 0
	first := true
	for {
		if r.closed.Load() || r.ctx.Err() != nil {
			return
		}
		pg, err := rs.Read(m.SID, seg, off, termproto.MaxRecordFrameData)
		if err != nil {
			if errors.Is(err, record.ErrNotFound) {
				r.failRecord(m.Req, termproto.RecReasonNotFound)
			} else {
				r.logf("读取终端记录失败（会话 %s）: %v", m.SID[:8], err)
				r.failRecord(m.Req, termproto.RecReasonError)
			}
			return
		}
		var flags byte
		if pg.EOF {
			flags |= termproto.RecFlagEOF
		}
		if pg.Skipped && first {
			flags |= termproto.RecFlagSkipped
		}
		sent += len(pg.Data)
		last := pg.EOF || sent >= limit || len(pg.Data) == 0
		if last {
			flags |= termproto.RecFlagLast
		}
		frame := termproto.EncodeRecord(termproto.RecordChunk{
			Req: m.Req, Flags: flags, Seg: pg.Seg, Off: pg.Off, NextSeg: pg.NextSeg, NextOff: pg.NextOff, Data: pg.Data,
		})
		if err := r.write(websocket.MessageBinary, frame); err != nil {
			return
		}
		if last {
			return
		}
		seg, off, first = pg.NextSeg, pg.NextOff, false
	}
}

// handleRecordStat 返回会话记录的索引信息（不含终端内容）。
func (r *relay) handleRecordStat(m termproto.RecordStat) {
	if !record.ValidSID(m.SID) {
		r.failRecord(m.Req, termproto.RecReasonBad)
		return
	}
	select {
	case r.recSem <- struct{}{}:
	default:
		r.failRecord(m.Req, termproto.RecReasonBusy)
		return
	}
	go func() {
		defer func() { <-r.recSem }()
		rs := r.records
		if rs == nil {
			r.failRecord(m.Req, termproto.RecReasonNotFound)
			return
		}
		info, err := rs.Info(m.SID)
		if err != nil {
			if errors.Is(err, record.ErrNotFound) {
				r.failRecord(m.Req, termproto.RecReasonNotFound)
			} else {
				r.failRecord(m.Req, termproto.RecReasonError)
			}
			return
		}
		raw := marshalInfo(info)
		_ = r.sendJSON(termproto.RecordInfo{T: termproto.TRecordInfo, Req: m.Req, SID: m.SID, Info: raw})
	}()
}

// marshalInfo 序列化索引信息；分段很多导致超过帧上限时只保留首尾各一部分（中间的省略）。
func marshalInfo(info *record.Info) json.RawMessage {
	raw, _ := json.Marshal(info)
	if len(raw) <= termproto.MaxRecordInfoJSON {
		return raw
	}
	thin := *info
	for keep := 128; keep >= 8; keep /= 2 {
		if len(info.Segments) <= 2*keep {
			continue
		}
		thin.Segments = append(append([]record.SegInfo(nil), info.Segments[:keep]...), info.Segments[len(info.Segments)-keep:]...)
		raw, _ = json.Marshal(&thin)
		if len(raw) <= termproto.MaxRecordInfoJSON {
			return raw
		}
	}
	thin.Segments = nil
	raw, _ = json.Marshal(&thin)
	return raw
}
