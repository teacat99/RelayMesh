package terminal

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
	"github.com/gin-gonic/gin"
	"github.com/teacat99/RelayMesh/v2/internal/termproto"
)

// outQueueSize 是每个浏览器附着的待发送消息队列容量。
// Agent 侧的 256 KiB 流控窗口保证正常情况下在途数据远小于它；队列满意味着浏览器已停滞，直接断开附着。
const outQueueSize = 512

// ticketBind 是绑定在终端票据上的附着参数（服务端签发、WebSocket 握手时原样取回）。
type ticketBind struct {
	Code  string `json:"c"`
	SID   string `json:"s"`
	Since uint64 `json:"o,omitempty"`
	Rows  uint16 `json:"r,omitempty"`
	Cols  uint16 `json:"l,omitempty"`
	// Tail 是全新附着时只回放末尾多少行（0 = 整体回放），见 termproto.Attach.TailLines。
	Tail uint16 `json:"t,omitempty"`
}

type wsOut struct {
	typ         websocket.MessageType
	data        []byte
	closeAfter  bool // 哨兵：写完队列中排在它前面的消息后关闭连接
	closeCode   websocket.StatusCode
	closeReason string
}

// attachPeer 是浏览器附着背后的会话承载方：一条 Agent 连接（*agentConn），或 Hub 本地会话（*localHost，Hub 直连 SSH 主机的人工终端）。
// attachment 只通过这四个方法与承载方交互，所以两种承载方对浏览器的协议、流控与停滞判定完全一致。
type attachPeer interface {
	// addAttachment 分配数据通道号（写入 att.ch）并登记附着。
	addAttachment(att *attachment) error
	removeAttachment(ch uint32)
	// writeJSON 发送控制帧：Attach / Detach / Resize。
	writeJSON(v any) error
	// writeBinary 发送二进制帧：输入（FrameInput）与确认（FrameAck）。
	writeBinary(data []byte) error
	// sessionInfo 返回承载方当前登记的会话信息（附着方据此判断会话是否在正常录制）。
	sessionInfo(sid string) (termproto.SessionInfo, bool)
}

// attachment 是一个浏览器 WebSocket 与承载方（Agent 或 Hub 本地会话）上某个会话之间的数据通道。
type attachment struct {
	hub     *Hub
	peer    attachPeer
	code    string // 承载方对应的机器码（写审计用）
	ws      *websocket.Conn
	subject string
	ip      string
	sid     string
	ch      uint32

	out     chan wsOut
	done    chan struct{}
	endOnce sync.Once

	attachRes chan error

	sentEnd atomic.Uint64 // 已交给浏览器发送队列的最高累计偏移
	acked   atomic.Uint64 // 浏览器已确认渲染的累计偏移
	lastAck atomic.Int64  // 最近一次确认（或窗口开始积压）的 UnixNano

	// 「跟不上输出」的判定（见 termproto.LagUnacked）：satSince 是在途未确认量持续处于饱和区间的起点（UnixNano，0 = 未饱和），
	// 由 deliver / ack 两个方向的处理维护；lagOn 记录是否已告诉浏览器「正在追赶」（提醒在 deliver 路径上第一时间发，撤回与断开在写循环的定时检查里）。
	// replayEnd 是附着瞬间会话输出的累计字节数：偏移小于它的是回放（一次性、体量由服务端按 tail 裁剪），回放本身慢不算「跟不上」，
	// 只统计回放之后的实时输出积压，否则慢链路上的一次正常回放就会被当成跟不上而陷入「断开 → 重播 → 再断开」。
	replayEnd atomic.Uint64
	satSince  atomic.Int64
	lagOn     atomic.Bool
	// recOK 是「会话仍在运行且记录正常」的缓存，由写循环的定时检查刷新（最多滞后一个节拍）。deliver 路径上只读它：
	// 本地会话在会话锁内回放时会调用 deliver，那里不能再去查会话信息（同一把锁）。
	recOK atomic.Bool
	// lastInput 是浏览器最近一次送来键盘输入的时刻（UnixNano，0 = 没有）：刚敲过键就先不因跟不上而断开（termproto.LagInputGrace）。
	lastInput atomic.Int64
	// endCode 是附着被结束时的关闭码（审计里区分「因跟不上被断开」）。
	endCode atomic.Int64
}

func newAttachment(h *Hub, peer attachPeer, code string, ws *websocket.Conn, subject, ip, sid string) *attachment {
	att := &attachment{
		hub:       h,
		peer:      peer,
		code:      code,
		ws:        ws,
		subject:   subject,
		ip:        ip,
		sid:       sid,
		out:       make(chan wsOut, outQueueSize),
		done:      make(chan struct{}),
		attachRes: make(chan error, 1),
	}
	att.lastAck.Store(h.now().UnixNano())
	return att
}

// ---------- Agent → 浏览器 ----------

// enqueue 非阻塞入队。队列满只可能是浏览器停滞，此时断开附着，绝不能阻塞 Agent 的读循环。
func (att *attachment) enqueue(m wsOut) {
	select {
	case <-att.done:
		return
	default:
	}
	select {
	case att.out <- m:
	default:
		att.end(termproto.CloseStalled, "client too slow")
	}
}

func (att *attachment) sendControl(v any) {
	data, err := termproto.Marshal(v)
	if err != nil {
		return
	}
	att.enqueue(wsOut{typ: websocket.MessageText, data: data})
}

// deliver 转发 Agent 的一帧输出。
func (att *attachment) deliver(offset uint64, payload []byte) {
	end := offset + uint64(len(payload))
	window := uint64(termproto.AttachWindow / 2)
	for {
		cur := att.sentEnd.Load()
		if end <= cur {
			break
		}
		if att.sentEnd.CompareAndSwap(cur, end) {
			// 积压刚刚越过半窗口：从此刻起计算「不确认」的时长，避免空闲后第一批输出被误判为停滞。
			if acked := att.acked.Load(); cur-acked < window && end-acked >= window {
				att.lastAck.Store(att.hub.now().UnixNano())
			}
			break
		}
	}
	att.noteBacklog()
	att.enqueue(wsOut{typ: websocket.MessageBinary, data: termproto.EncodeBrowserOutput(offset, payload)})
}

// noteBacklog 根据当前在途未确认量维护「饱和」起点：达到 LagUnacked 记下起点，降到 LagClear 以下才清零（迟滞）。
func (att *attachment) noteBacklog() {
	end, acked := att.sentEnd.Load(), att.acked.Load()
	if re := att.replayEnd.Load(); acked < re {
		acked = re
	}
	var un uint64
	if end > acked {
		un = end - acked
	}
	switch {
	case un >= termproto.LagUnacked:
		att.satSince.CompareAndSwap(0, att.hub.now().UnixNano())
		att.maybeWarn()
	case un < termproto.LagClear:
		att.satSince.Store(0)
	}
}

// maybeWarn 在饱和持续满 LagWarnAfter 时第一时间（下一帧输出到来时，而不是等下一次定时检查）告诉浏览器盖上遮罩：
// 定时检查最多晚 250 毫秒，而这 250 毫秒里用户看到的正是要遮住的刷屏。只对记录正常的会话发；只发一次（lagOn）。
func (att *attachment) maybeWarn() {
	if att.lagOn.Load() {
		return
	}
	since := att.satSince.Load()
	if since == 0 || time.Duration(att.hub.now().UnixNano()-since) < termproto.LagWarnAfter || !att.recOK.Load() {
		return
	}
	if att.lagOn.CompareAndSwap(false, true) {
		att.sendControl(termproto.BrowserLag{T: termproto.TLag, On: true})
	}
}

func (att *attachment) onAttached(m termproto.Attached) {
	att.sentEnd.Store(m.From)
	att.acked.Store(m.From)
	att.lastAck.Store(att.hub.now().UnixNano())
	att.replayEnd.Store(m.End)
	att.satSince.Store(0)

	ready := termproto.BrowserReady{
		T:         termproto.TReady,
		SID:       att.sid,
		Base:      m.Base,
		End:       m.End,
		From:      m.From,
		Truncated: m.Truncated,
		Prefix:    m.Prefix,
		Rows:      m.Rows,
		Cols:      m.Cols,
		State:     m.State,
		ExitCode:  m.ExitCode,
	}
	// ready 必须先于后续输出入队：Agent 在 attached 之后才发输出帧，而两者在同一读循环内顺序处理。
	att.sendControl(ready)
	select {
	case att.attachRes <- nil:
	default:
	}
}

func (att *attachment) onAttachFailed(m termproto.AttachFailed) {
	reason := m.Reason
	if reason == "" {
		reason = "attach refused"
	}
	select {
	case att.attachRes <- errors.New(termproto.SanitizeText(reason, 120)):
	default:
	}
}

// endFromAgent 处理 Agent 主动释放通道：先把队列里已有的输出与 exit 通知发完，再关闭。
func (att *attachment) endFromAgent(reason string) {
	var code websocket.StatusCode = termproto.CloseNotFound
	switch reason {
	case "exited", "killed":
		code = termproto.CloseSessionEnded
	case "stalled":
		code = termproto.CloseStalled
	case "overrun":
		// 缓冲被越过：和「跟不上输出」同一个处理（浏览器全新附着、跳到最新），不能当成「主机不在线」慢速重试。
		code = termproto.CloseLagging
	}
	text := termproto.SanitizeText(reason, 60)
	select {
	case <-att.done:
	case att.out <- wsOut{closeAfter: true, closeCode: code, closeReason: text}:
	default:
		att.end(code, text)
	}
}

// end 立即结束附着（幂等）。可由任意 goroutine 调用，不阻塞。
func (att *attachment) end(code websocket.StatusCode, reason string) {
	att.endOnce.Do(func() {
		att.endCode.Store(int64(code))
		close(att.done)
		go func() { _ = att.ws.Close(code, reason) }()
	})
}

// ---------- 浏览器 → Agent ----------

func (att *attachment) attach(ctx context.Context, b ticketBind) error {
	if err := att.peer.addAttachment(att); err != nil {
		return err
	}
	rows, cols := termproto.ClampSize(b.Rows, b.Cols)
	if err := att.peer.writeJSON(termproto.Attach{
		T: termproto.TAttach, Ch: att.ch, SID: att.sid, Since: b.Since, Rows: rows, Cols: cols, TailLines: b.Tail,
	}); err != nil {
		att.peer.removeAttachment(att.ch)
		return err
	}
	timer := time.NewTimer(att.hub.opts.AttachTimeout)
	defer timer.Stop()
	select {
	case err := <-att.attachRes:
		if err != nil {
			att.peer.removeAttachment(att.ch)
		}
		return err
	case <-timer.C:
		att.peer.removeAttachment(att.ch)
		_ = att.peer.writeJSON(termproto.Detach{T: termproto.TDetach, Ch: att.ch})
		return errors.New("agent did not attach in time")
	case <-ctx.Done():
		att.peer.removeAttachment(att.ch)
		return ctx.Err()
	}
}

// run 阻塞到附着结束：读浏览器消息并转发，同时驱动写队列与心跳。
func (att *attachment) run() {
	go att.writeLoop()
	go att.pingLoop()
	defer att.finish()

	for {
		typ, data, err := att.ws.Read(att.hub.ctx)
		if err != nil {
			return
		}
		if typ == websocket.MessageBinary {
			payload, derr := termproto.DecodeBrowserInput(data)
			if derr != nil {
				att.end(websocket.StatusUnsupportedData, "bad input frame")
				return
			}
			if len(payload) > 0 {
				att.lastInput.Store(att.hub.now().UnixNano())
				if err := att.peer.writeBinary(termproto.EncodeInput(att.ch, payload)); err != nil {
					att.end(termproto.CloseNotFound, "agent offline")
					return
				}
			}
			continue
		}
		att.handleControl(data)
	}
}

func (att *attachment) handleControl(data []byte) {
	t, err := termproto.ParseType(data)
	if err != nil {
		return
	}
	switch t {
	case termproto.TAck:
		var m termproto.BrowserAck
		if json.Unmarshal(data, &m) != nil {
			return
		}
		limit := att.sentEnd.Load()
		off := m.Offset
		if off > limit {
			off = limit // 浏览器不可能确认尚未发给它的数据
		}
		for {
			cur := att.acked.Load()
			if off <= cur {
				return
			}
			if att.acked.CompareAndSwap(cur, off) {
				break
			}
		}
		att.lastAck.Store(att.hub.now().UnixNano())
		att.noteBacklog()
		_ = att.peer.writeBinary(termproto.EncodeAck(att.ch, off))
	case termproto.TResize:
		var m termproto.BrowserResize
		if json.Unmarshal(data, &m) != nil {
			return
		}
		rows, cols := termproto.ClampSize(m.Rows, m.Cols)
		_ = att.peer.writeJSON(termproto.Resize{T: termproto.TResize, SID: att.sid, Rows: rows, Cols: cols})
	case termproto.TPing:
		att.sendControl(termproto.Envelope{T: termproto.TPong})
	}
}

// attachTick 是附着的周期检查间隔：跟不上输出（checkLag）要在百毫秒级发现，否则用户会先看着重画滚几秒才盖上遮罩；
// 停滞（checkStall）的期限是 10 秒，同一个节拍绰绰有余。检查本身只读几个原子量，开销可以忽略。
const attachTick = 250 * time.Millisecond

func (h *Hub) attachTickInterval() time.Duration {
	if h.attachTickOverride > 0 {
		return h.attachTickOverride
	}
	return attachTick
}

func (att *attachment) writeLoop() {
	stall := time.NewTicker(att.hub.attachTickInterval())
	defer stall.Stop()
	for {
		select {
		case <-att.done:
			return
		case <-att.hub.ctx.Done():
			att.end(websocket.StatusGoingAway, "server shutting down")
			return
		case <-stall.C:
			att.checkLag()
			att.checkStall()
		case m := <-att.out:
			if m.closeAfter {
				att.end(m.closeCode, m.closeReason)
				return
			}
			ctx, cancel := context.WithTimeout(att.hub.ctx, writeTimeout)
			err := att.ws.Write(ctx, m.typ, m.data)
			cancel()
			if err != nil {
				att.end(websocket.StatusInternalError, "write failed")
				return
			}
		}
	}
}

// checkLag 处理「附着方持续跟不上输出」：饱和超过 LagWarnAfter 先告诉浏览器（遮罩），超过 LagEjectAfter 就断开并要求重新附着。
// 只对会话记录正常（rec=on）的附着生效：断开后浏览器带「全新附着 + 只回放末尾」重连，被跳过的中间输出能从记录里读到；
// 没有记录的会话保持原来的无损背压（裁掉就是丢内容）。
func (att *attachment) checkLag() {
	att.recOK.Store(att.sessionRecorded())
	since := att.satSince.Load()
	if since == 0 {
		if att.lagOn.CompareAndSwap(true, false) {
			att.sendControl(termproto.BrowserLag{T: termproto.TLag, On: false})
		}
		return
	}
	d := time.Duration(att.hub.now().UnixNano() - since)
	if d < termproto.LagWarnAfter || !att.recOK.Load() {
		return
	}
	att.maybeWarn()
	if d >= termproto.LagEjectAfter {
		if last := att.lastInput.Load(); last != 0 && time.Duration(att.hub.now().UnixNano()-last) < termproto.LagInputGrace {
			return // 用户刚敲过键：多半在处理眼前的输出，先不断开
		}
		att.end(termproto.CloseLagging, "lagging")
	}
}

// sessionRecorded 报告会话是否仍在运行且记录正常（被跳过的输出事后能从记录里读到）。
func (att *attachment) sessionRecorded() bool {
	info, ok := att.peer.sessionInfo(att.sid)
	return ok && info.Rec == "on" && info.State != termproto.SessionExited
}

func (att *attachment) checkStall() {
	unacked := att.sentEnd.Load() - att.acked.Load()
	if unacked < uint64(termproto.AttachWindow/2) {
		return
	}
	since := time.Duration(att.hub.now().UnixNano() - att.lastAck.Load())
	if since >= termproto.StallTimeout {
		att.end(termproto.CloseStalled, "client not acknowledging output")
	}
}

func (att *attachment) pingLoop() {
	ticker := time.NewTicker(termproto.PingInterval)
	defer ticker.Stop()
	for {
		select {
		case <-att.done:
			return
		case <-ticker.C:
			ctx, cancel := context.WithTimeout(att.hub.ctx, termproto.PongTimeout)
			err := att.ws.Ping(ctx)
			cancel()
			if err != nil {
				att.end(websocket.StatusGoingAway, "ping timeout")
				return
			}
		}
	}
}

// finish 是附着结束后的统一收尾：通知 Agent 释放通道、写审计、归还名额。
func (att *attachment) finish() {
	// end 幂等：此前已带关闭码结束过则无操作。这里不能 CloseNow——那会掐断正在进行的带码关闭握手，
	// 对端就收不到关闭原因。底层连接由 end 启动的 Close 协程在握手完成或 5 秒超时后回收。
	att.end(websocket.StatusNormalClosure, "")

	att.peer.removeAttachment(att.ch)
	_ = att.peer.writeJSON(termproto.Detach{T: termproto.TDetach, Ch: att.ch})
	att.hub.releaseAttachment()
	ev := auditEvent{
		Event:       EvSessionDetach,
		Actor:       att.subject,
		IP:          att.ip,
		MachineCode: att.code,
		SessionID:   att.sid,
	}
	if att.endCode.Load() == int64(termproto.CloseLagging) {
		ev.Detail = "lagging"
	}
	att.hub.audit.record(context.Background(), ev)
}

// ---------- 浏览器 WebSocket 入口 ----------

// ticketFromProtocols 从 Sec-WebSocket-Protocol 头取出形如 "tk.<票据>" 的子协议项。
// 票据放在子协议头而不是 URL：URL 会进入各级访问日志，请求头不会。
func ticketFromProtocols(h http.Header) string {
	for _, v := range h.Values("Sec-WebSocket-Protocol") {
		for _, p := range strings.Split(v, ",") {
			p = strings.TrimSpace(p)
			if strings.HasPrefix(p, termproto.TicketSubprotocolPrefix) {
				return strings.TrimPrefix(p, termproto.TicketSubprotocolPrefix)
			}
		}
	}
	return ""
}

func (h *Hub) handleBrowserWS(c *gin.Context) {
	r := c.Request
	ip := c.ClientIP()

	// 1. 总闸：实时判定，关闭即拒绝。
	if d := h.gate.Evaluate(r.Context()); !d.Open {
		h.audit.recordLimited(r.Context(), ip, auditEvent{Event: EvGateDenied, Result: ResultDeny, IP: ip, Detail: "browser ws: " + string(d.Reason)})
		c.JSON(http.StatusForbidden, gin.H{"error": "终端功能当前不可用", "code": "terminal_disabled", "reason": d.Reason})
		return
	}

	// 2. Origin：有 Origin 时必须同源或在白名单内（防跨站 WebSocket 劫持）。
	if origin := r.Header.Get("Origin"); !h.originAllowed(origin, r.Host) {
		log.Printf("[terminal] 已拒绝跨源的终端 WebSocket：Origin=%q Host=%q。若通过反向代理访问且 Host 被改写，请设置 RELAYMESH_ALLOWED_ORIGINS", sanitizeForLog(origin), sanitizeForLog(r.Host))
		c.JSON(http.StatusForbidden, gin.H{"error": "origin not allowed", "code": "origin_denied"})
		return
	}

	// 3. 一次性票据（取自子协议头）：签发时已经过登录 + 解锁令牌双重校验。
	info, ok := h.tickets.Redeem(ticketFromProtocols(r.Header), ticketPurposeTerm)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "ticket invalid or expired", "code": "ticket_invalid"})
		return
	}
	var bind ticketBind
	if err := json.Unmarshal([]byte(info.Bind), &bind); err != nil || !termproto.ValidSessionID(bind.SID) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "bad ticket"})
		return
	}

	release, tracked := h.track()
	if !tracked {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "server shutting down"})
		return
	}
	defer release()
	if !h.reserveAttachment() {
		c.JSON(http.StatusTooManyRequests, gin.H{"error": "too many terminal connections", "code": "too_many"})
		return
	}
	owned := false
	defer func() {
		if !owned {
			h.releaseAttachment()
		}
	}()

	ws, err := websocket.Accept(c.Writer, r, &websocket.AcceptOptions{
		Subprotocols:       []string{termproto.SubprotocolBrowser},
		InsecureSkipVerify: true, // Origin 已在上面按自有规则校验
		// 终端输出（尤其是全屏 / 内联重绘的程序）重复度很高：用生产会话的真实录制实测，permessage-deflate（flate BestSpeed、
		// 一条消息 32 KiB）压缩比 5~7 倍、约 9 ms/MiB。小带宽的 Hub 上打开长会话、尺寸变化后程序整段重画，传输时间几乎全是带宽，
		// 压缩让它缩短到约五分之一。只有浏览器也支持时才会启用（Safari 不支持，自动不压缩）；不足 128 字节的帧（按键回显）不压缩。
		CompressionMode: websocket.CompressionContextTakeover,
	})
	if err != nil {
		return
	}
	ws.SetReadLimit(64 << 10)

	fail := func(code websocket.StatusCode, reason string) {
		_ = ws.Close(code, reason)
	}

	// 承载方：在线的 Agent，或者 Hub 直连 SSH 主机的 Hub 本地会话（后者没有 Agent 连接，会话就在 Hub 进程里）。
	var peer attachPeer
	if agent := h.onlineAgent(bind.Code); agent != nil && agent.isEnabled() {
		if _, found := agent.findSession(bind.SID); !found {
			fail(termproto.CloseNotFound, "session not found")
			return
		}
		peer = agent
	} else if lh := h.local.peek(bind.Code); lh != nil {
		if _, found := lh.find(bind.SID); !found {
			fail(termproto.CloseNotFound, "session not found")
			return
		}
		peer = lh
	} else {
		fail(termproto.CloseNotFound, "host offline")
		return
	}

	att := newAttachment(h, peer, bind.Code, ws, info.Subject, ip, bind.SID)
	// 记录状态的初值：此刻没有持有任何会话锁，可以查；之后由定时检查刷新（回放时在会话锁内调用 deliver，那里只能读缓存）。
	att.recOK.Store(att.sessionRecorded())
	if err := att.attach(h.ctx, bind); err != nil {
		fail(termproto.CloseNotFound, termproto.SanitizeText(fmt.Sprintf("attach failed: %v", err), 100))
		return
	}

	owned = true // 名额的归还由 finish 负责
	h.audit.record(h.ctx, auditEvent{
		Event:       EvSessionAttach,
		Actor:       info.Subject,
		IP:          ip,
		MachineCode: bind.Code,
		SessionID:   bind.SID,
	})
	att.run()
}

// sanitizeForLog 去掉可能污染日志的控制字符并限长。
func sanitizeForLog(s string) string {
	return termproto.SanitizeText(s, 120)
}
