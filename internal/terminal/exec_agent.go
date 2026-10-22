package terminal

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"time"

	"github.com/coder/websocket"
	"github.com/teacat99/RelayMesh/v2/internal/termproto"
)

// agentConn 的模型运行支持：向主机发送 run / run_cancel / run_ack / runs_sync，并按请求读取运行输出（run_read）。
// 运行本身的状态机在 ExecService；这里只做「线上收发」。

var (
	errRunReadNotFound = errors.New("run output not found on host")
	errRunReadBusy     = errors.New("host is busy reading run output")
	errRunReadTimeout  = errors.New("host did not answer in time")
	errRunReadTooMany  = errors.New("too many run reads in flight")
	errRunReadProtocol = errors.New("host sent an invalid run read response")
	errRunFrameTooBig  = errors.New("run request does not fit in a control frame")
)

// maxPendingRunReads 是单个 Agent 连接上同时在途的运行输出读取数（与守护进程的并发读取上限一致）。
const maxPendingRunReads = 4

// runFrameLimit 是 run 帧序列化后的字节上限：留出余量，确保不会被 Agent 一侧 64 KiB 的读取上限拒收。
const runFrameLimit = termproto.MaxControlFrame - 4<<10

// 运行输出读取的期限（变量而非常量，仅为让测试能缩短等待）。
var (
	runReadTimeout     = 20 * time.Second
	runReadIdleTimeout = 10 * time.Second
)

type runReadEvent struct {
	chunk *termproto.RunChunk
	fail  string
}

type runReadWaiter struct {
	ch chan runReadEvent
}

// deliver 非阻塞投递：读循环绝不能等任何一个请求方。
func (w *runReadWaiter) deliver(ev runReadEvent) {
	select {
	case w.ch <- ev:
	default:
	}
}

// RunPage 是一次运行输出读取的结果。
type RunPage struct {
	Data    []byte
	Off     uint64 // 第一个字节在该路输出中的位置
	Next    uint64 // 继续读取应使用的偏移
	Total   uint64 // 该路输出到目前为止产生的总字节数
	Gap     bool
	EOF     bool
	Running bool
}

func (a *agentConn) registerRunRead(frames int) (uint32, *runReadWaiter, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.runWaits == nil {
		a.runWaits = make(map[uint32]*runReadWaiter)
	}
	if len(a.runWaits) >= maxPendingRunReads {
		return 0, nil, errRunReadTooMany
	}
	a.nextRunReq++
	if a.nextRunReq == 0 {
		a.nextRunReq = 1
	}
	req := a.nextRunReq
	w := &runReadWaiter{ch: make(chan runReadEvent, frames)}
	a.runWaits[req] = w
	return req, w, nil
}

func (a *agentConn) unregisterRunRead(req uint32) {
	a.mu.Lock()
	delete(a.runWaits, req)
	a.mu.Unlock()
}

func (a *agentConn) runReadWaiter(req uint32) *runReadWaiter {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.runWaits[req]
}

// onRunDataFrame 处理 run_read 的应答帧（读循环里调用）。
func (a *agentConn) onRunDataFrame(data []byte) {
	c, err := termproto.DecodeRunData(data)
	if err != nil {
		a.closeWith(termproto.CloseInvalidHandshake, "bad run data frame")
		return
	}
	w := a.runReadWaiter(c.Req)
	if w == nil {
		return // 请求已超时或已取消
	}
	cp := c
	cp.Data = append([]byte(nil), c.Data...)
	w.deliver(runReadEvent{chunk: &cp})
}

func (a *agentConn) onRunReadFail(m termproto.RunReadFail) {
	if w := a.runReadWaiter(m.Req); w != nil {
		reason := m.Reason
		if reason == "" {
			reason = termproto.RunReadError
		}
		w.deliver(runReadEvent{fail: reason})
	}
}

// onRunOutFrame 处理实时输出片段：只转给人类事件流，从不落盘。
func (a *agentConn) onRunOutFrame(data []byte) {
	id, stream, off, payload, err := termproto.DecodeRunOut(data)
	if err != nil {
		a.closeWith(termproto.CloseInvalidHandshake, "bad run output frame")
		return
	}
	a.hub.exec.onOut(a, id, stream, off, payload)
}

func runReadFailError(reason string) error {
	switch reason {
	case termproto.RunReadNotFound:
		return errRunReadNotFound
	case termproto.RunReadBusy:
		return errRunReadBusy
	default:
		return errRunReadProtocol
	}
}

// readRun 读取运行已保存的输出：从某一路的 off 起至多 max 字节。
func (a *agentConn) readRun(ctx context.Context, id, stream string, off uint64, max int) (*RunPage, error) {
	if !a.isEnabled() {
		return nil, errAgentNotActive
	}
	if max <= 0 {
		max = termproto.DefaultRunRead
	}
	if max > termproto.MaxRunRead {
		max = termproto.MaxRunRead
	}
	req, w, err := a.registerRunRead(max/termproto.MaxRunDataFrame + 4)
	if err != nil {
		return nil, err
	}
	defer a.unregisterRunRead(req)

	if err := a.writeJSON(termproto.RunRead{T: termproto.TRunRead, Req: req, ID: id, Stream: stream, Off: off, Max: max}); err != nil {
		return nil, err
	}

	page := &RunPage{}
	first := true
	total := time.NewTimer(runReadTimeout)
	defer total.Stop()
	idle := time.NewTimer(runReadIdleTimeout)
	defer idle.Stop()
	for {
		select {
		case ev := <-w.ch:
			if ev.fail != "" {
				return nil, runReadFailError(ev.fail)
			}
			c := ev.chunk
			if c == nil {
				return nil, errRunReadProtocol
			}
			if first {
				page.Off = c.Off
				first = false
			}
			page.Data = append(page.Data, c.Data...)
			page.Next, page.Total = c.Next, c.Total
			page.Gap = page.Gap || c.Flags&termproto.RunFlagGap != 0
			page.EOF = c.Flags&termproto.RunFlagEOF != 0
			page.Running = c.Flags&termproto.RunFlagRunning != 0
			if len(page.Data) > max+termproto.MaxRunDataFrame {
				return nil, errRunReadProtocol
			}
			if c.Flags&termproto.RunFlagLast != 0 {
				return page, nil
			}
			if !idle.Stop() {
				select {
				case <-idle.C:
				default:
				}
			}
			idle.Reset(runReadIdleTimeout)
		case <-idle.C:
			return nil, errRunReadTimeout
		case <-total.C:
			return nil, errRunReadTimeout
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-a.ctx.Done():
			return nil, errAgentClosed
		}
	}
}

// ---- 发送 ----

// sendRun 发出一次运行请求。序列化后超过控制帧安全上限时拒绝发送（不会触发连接级错误）。
func (a *agentConn) sendRun(r termproto.Run) error {
	r.T = termproto.TRun
	data, err := termproto.Marshal(r)
	if err != nil {
		return err
	}
	if len(data) > runFrameLimit {
		return errRunFrameTooBig
	}
	return a.write(websocket.MessageText, data)
}

func (a *agentConn) sendRunCancel(id, reason string) error {
	return a.writeJSON(termproto.RunCancel{T: termproto.TRunCancel, ID: id, Reason: reason})
}

func (a *agentConn) sendRunAck(id string) error {
	return a.writeJSON(termproto.RunAck{T: termproto.TRunAck, ID: id})
}

func (a *agentConn) sendRunsSync() error {
	return a.writeJSON(termproto.RunsSync{T: termproto.TRunsSync})
}

// execSupport 报告这台主机的模型执行能力（来自守护进程最近一次上报）：
// supported = 守护进程可用且具备 exec 能力；denied = 本机用 --deny-model-exec 一票否决；info = 执行环境。
func (a *agentConn) execSupport() (supported, denied bool, info *termproto.ExecInfo) {
	a.mu.Lock()
	defer a.mu.Unlock()
	h := a.holder
	if h == nil || h.State != termproto.HolderReady {
		return false, false, nil
	}
	if !slices.Contains(h.Caps, "exec") {
		return false, false, nil
	}
	if h.Exec != nil {
		cp := *h.Exec
		info = &cp
	}
	return true, h.DenyExec, info
}

// decodeJSON 是 handleFrame 里重复出现的「解析失败就忽略」小工具。
func decodeJSON[T any](data []byte) (T, bool) {
	var v T
	if err := json.Unmarshal(data, &v); err != nil {
		return v, false
	}
	return v, true
}

// maybeSyncRuns 在守护进程上报「可用且具备 exec 能力」后，每条连接只请求一次运行对账（runs_sync）：
// 守护进程会重发运行清单与尚未被确认的结束事件，Hub 据此补齐 Agent 离线期间发生的事。重发是幂等的。
func (a *agentConn) maybeSyncRuns() {
	supported, _, _ := a.execSupport()
	if !supported {
		return
	}
	a.mu.Lock()
	if a.execSynced {
		a.mu.Unlock()
		return
	}
	a.execSynced = true
	a.mu.Unlock()
	_ = a.sendRunsSync()
}
