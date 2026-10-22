package terminal

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/teacat99/RelayMesh/v2/internal/termproto"
)

// 终端记录读取（Hub 侧）：向 Agent 请求主机本地记录的内容，原样流给人类浏览器，Hub 不落盘。

var (
	errRecordNotFound = errors.New("record not found on host")
	errRecordBusy     = errors.New("host is busy reading records")
	errRecordTimeout  = errors.New("host did not answer in time")
	errRecordTooMany  = errors.New("too many record requests in flight")
	errRecordProtocol = errors.New("host sent an invalid record response")
)

// maxPendingRecordReqs 是单个 Agent 连接上同时在途的记录请求数上限。
const maxPendingRecordReqs = 4

// 记录读取的期限（变量而非常量，仅为让测试能缩短等待）。
var (
	// recordReadTimeout 是一次读取从发出请求到收齐全部帧的总期限。
	recordReadTimeout = 20 * time.Second
	// recordIdleTimeout 是两帧之间的最长间隔。
	recordIdleTimeout = 10 * time.Second
)

type recordEvent struct {
	chunk *termproto.RecordChunk
	info  json.RawMessage
	fail  string
}

type recordWaiter struct {
	ch chan recordEvent
}

// RecordPage 是一次记录读取的结果：若干完整事件的原始编码与续读游标。
type RecordPage struct {
	Data    []byte
	Seg     uint32
	Off     uint32
	NextSeg uint32
	NextOff uint32
	EOF     bool
	Skipped bool
}

func (a *agentConn) registerRecordReq(frames int) (uint32, *recordWaiter, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(a.recWaits) >= maxPendingRecordReqs {
		return 0, nil, errRecordTooMany
	}
	a.nextRec++
	if a.nextRec == 0 {
		a.nextRec = 1
	}
	req := a.nextRec
	w := &recordWaiter{ch: make(chan recordEvent, frames)}
	a.recWaits[req] = w
	return req, w, nil
}

func (a *agentConn) unregisterRecordReq(req uint32) {
	a.mu.Lock()
	delete(a.recWaits, req)
	a.mu.Unlock()
}

func (a *agentConn) recordWaiter(req uint32) *recordWaiter {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.recWaits[req]
}

// deliver 非阻塞投递：读循环绝不能等任何一个浏览器请求。通道按期望帧数预留了容量，
// 装不下说明 Agent 发来了超出请求的数据，直接丢弃，请求会因收不到 Last 帧而超时。
func (w *recordWaiter) deliver(ev recordEvent) {
	select {
	case w.ch <- ev:
	default:
	}
}

// onRecordFrame 处理 Agent 发来的记录内容帧（读循环里调用）。
func (a *agentConn) onRecordFrame(data []byte) {
	c, err := termproto.DecodeRecord(data)
	if err != nil {
		a.closeWith(termproto.CloseInvalidHandshake, "bad record frame")
		return
	}
	w := a.recordWaiter(c.Req)
	if w == nil {
		return // 请求已超时或已取消
	}
	cp := c
	cp.Data = append([]byte(nil), c.Data...)
	w.deliver(recordEvent{chunk: &cp})
}

func (a *agentConn) onRecordInfo(m termproto.RecordInfo) {
	if w := a.recordWaiter(m.Req); w != nil {
		w.deliver(recordEvent{info: m.Info})
	}
}

func (a *agentConn) onRecordFail(m termproto.RecordFail) {
	if w := a.recordWaiter(m.Req); w != nil {
		reason := m.Reason
		if reason == "" {
			reason = termproto.RecReasonError
		}
		w.deliver(recordEvent{fail: reason})
	}
}

func recordFailError(reason string) error {
	switch reason {
	case termproto.RecReasonNotFound:
		return errRecordNotFound
	case termproto.RecReasonBusy:
		return errRecordBusy
	default:
		return errRecordProtocol
	}
}

// readRecord 读取会话记录的一段内容：从 (seg, off) 起至多 max 字节（Agent 可能按帧略有出入）。
func (a *agentConn) readRecord(ctx context.Context, sid string, seg, off uint32, max int) (*RecordPage, error) {
	if !a.isEnabled() {
		return nil, errAgentNotActive
	}
	if max <= 0 {
		max = termproto.DefaultRecordRead
	}
	if max > termproto.MaxRecordRead {
		max = termproto.MaxRecordRead
	}
	req, w, err := a.registerRecordReq(max/termproto.MaxRecordFrameData + 4)
	if err != nil {
		return nil, err
	}
	defer a.unregisterRecordReq(req)

	if err := a.writeJSON(termproto.RecordRead{T: termproto.TRecordRead, Req: req, SID: sid, Seg: seg, Off: off, Max: max}); err != nil {
		return nil, err
	}

	page := &RecordPage{}
	first := true
	total := time.NewTimer(recordReadTimeout)
	defer total.Stop()
	idle := time.NewTimer(recordIdleTimeout)
	defer idle.Stop()
	for {
		select {
		case ev := <-w.ch:
			if ev.fail != "" {
				return nil, recordFailError(ev.fail)
			}
			c := ev.chunk
			if c == nil {
				return nil, errRecordProtocol
			}
			if first {
				page.Seg, page.Off = c.Seg, c.Off
				page.Skipped = c.Flags&termproto.RecFlagSkipped != 0
				first = false
			}
			page.Data = append(page.Data, c.Data...)
			page.NextSeg, page.NextOff = c.NextSeg, c.NextOff
			page.EOF = c.Flags&termproto.RecFlagEOF != 0
			if len(page.Data) > max+termproto.MaxRecordFrameData {
				return nil, errRecordProtocol
			}
			if c.Flags&termproto.RecFlagLast != 0 {
				return page, nil
			}
			if !idle.Stop() {
				select {
				case <-idle.C:
				default:
				}
			}
			idle.Reset(recordIdleTimeout)
		case <-idle.C:
			return nil, errRecordTimeout
		case <-total.C:
			return nil, errRecordTimeout
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-a.ctx.Done():
			return nil, errAgentClosed
		}
	}
}

// statRecord 读取会话记录的索引信息（主机端 record.Info 的 JSON，不含内容）。
func (a *agentConn) statRecord(ctx context.Context, sid string) (json.RawMessage, error) {
	if !a.isEnabled() {
		return nil, errAgentNotActive
	}
	req, w, err := a.registerRecordReq(2)
	if err != nil {
		return nil, err
	}
	defer a.unregisterRecordReq(req)
	if err := a.writeJSON(termproto.RecordStat{T: termproto.TRecordStat, Req: req, SID: sid}); err != nil {
		return nil, err
	}
	timer := time.NewTimer(recordIdleTimeout)
	defer timer.Stop()
	select {
	case ev := <-w.ch:
		if ev.fail != "" {
			return nil, recordFailError(ev.fail)
		}
		if len(ev.info) == 0 {
			return nil, errRecordProtocol
		}
		return ev.info, nil
	case <-timer.C:
		return nil, errRecordTimeout
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-a.ctx.Done():
		return nil, errAgentClosed
	}
}
