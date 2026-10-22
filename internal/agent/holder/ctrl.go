package holder

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"github.com/teacat99/RelayMesh/v2/internal/termproto"
)

const (
	// writeTimeout 是向控制端写一帧的最长时间；超时意味着对端卡死，连接随之被关闭。
	writeTimeout = 15 * time.Second
	// sessionsDebounce 合并短时间内连续发生的会话变化，只推送一次清单。
	sessionsDebounce = 150 * time.Millisecond
	// ipcPingInterval / ipcPongTimeout 是本地连接的心跳：控制端进程卡住（未退出）时，守护进程据此放弃它。
	ipcPingInterval = termproto.PingInterval
	ipcPongTimeout  = termproto.PongTimeout
)

// ctrl 是守护进程这一侧、一条控制端连接上的协议处理器。
// 它只负责这一条连接：连接断开后 ctrl 随之结束，会话（Session）仍由 Manager 持有并继续运行。
// 协议与 Agent↔Hub 的数据面一致，所以 Agent 才能近乎透明地转发。
type ctrl struct {
	host *Host
	mgr  *Manager
	conn Conn
	ctx  context.Context
	logf func(string, ...any)

	mu    sync.Mutex
	chans map[uint32]*channel

	closed    atomic.Bool
	sessDirty atomic.Bool
}

func newCtrl(ctx context.Context, h *Host, conn Conn) *ctrl {
	return &ctrl{
		host:  h,
		mgr:   h.mgr,
		conn:  conn,
		ctx:   ctx,
		logf:  h.logf,
		chans: make(map[uint32]*channel),
	}
}

// ---------- 发送 ----------

func (l *ctrl) write(typ MsgType, data []byte) error {
	if l.closed.Load() {
		return context.Canceled
	}
	ctx, cancel := context.WithTimeout(l.ctx, writeTimeout)
	defer cancel()
	return l.conn.Write(ctx, typ, data)
}

func (l *ctrl) sendJSON(v any) error {
	data, err := termproto.Marshal(v)
	if err != nil {
		return err
	}
	return l.write(MsgText, data)
}

func (l *ctrl) sendBinary(data []byte) error { return l.write(MsgBinary, data) }

func (l *ctrl) channelEnded(c *channel) {
	l.mu.Lock()
	if l.chans[c.id] == c {
		delete(l.chans, c.id)
	}
	l.mu.Unlock()
}

// sendSessions 推送会话清单的全量快照。
func (l *ctrl) sendSessions() {
	if l.closed.Load() {
		return
	}
	_ = l.sendJSON(termproto.Sessions{T: termproto.TSessions, Sessions: l.mgr.Snapshot()})
}

// requestSessions 请求推送会话清单；短时间内的多次请求合并为一次。可在任意 goroutine 调用。
func (l *ctrl) requestSessions() {
	if l.closed.Load() || !l.sessDirty.CompareAndSwap(false, true) {
		return
	}
	time.AfterFunc(sessionsDebounce, func() {
		l.sessDirty.Store(false)
		l.sendSessions()
	})
}

// ---------- 读循环 ----------

// serve 阻塞处理控制端发来的帧，直到连接结束。
func (l *ctrl) serve() error {
	for {
		typ, data, err := l.conn.Read(l.ctx)
		if err != nil {
			return err
		}
		if typ == MsgBinary {
			l.handleBinary(data)
			continue
		}
		l.handleText(data)
	}
}

func (l *ctrl) handleBinary(data []byte) {
	typ, err := termproto.FrameType(data)
	if err != nil {
		return
	}
	switch typ {
	case termproto.FrameInput:
		ch, payload, err := termproto.DecodeInput(data)
		if err != nil {
			return
		}
		if c := l.channel(ch); c != nil {
			c.sess.writeInput(payload)
		}
	case termproto.FrameAck:
		ch, off, err := termproto.DecodeAck(data)
		if err != nil {
			return
		}
		if c := l.channel(ch); c != nil {
			c.ack(off)
		}
	}
}

func (l *ctrl) channel(id uint32) *channel {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.chans[id]
}

// handleText 处理一条控制帧。
func (l *ctrl) handleText(data []byte) {
	t, err := termproto.ParseType(data)
	if err != nil {
		return
	}
	switch t {
	case termproto.TOpen:
		var m termproto.Open
		if json.Unmarshal(data, &m) == nil {
			l.onOpen(m)
		}
	case termproto.TSSHOp:
		var m termproto.SSHOp
		if json.Unmarshal(data, &m) == nil {
			go l.onSSHOp(m) // 同样要经过网络，不占读循环
		}
	case termproto.TAttach:
		var m termproto.Attach
		if json.Unmarshal(data, &m) == nil {
			l.onAttach(m)
		}
	case termproto.TDetach:
		var m termproto.Detach
		if json.Unmarshal(data, &m) == nil {
			if c := l.channel(m.Ch); c != nil {
				c.end("") // 控制端主动释放，无需再通知它
			}
		}
	case termproto.TResize:
		var m termproto.Resize
		if json.Unmarshal(data, &m) == nil {
			if s := l.mgr.Get(m.SID); s != nil {
				s.resize(m.Rows, m.Cols)
			}
		}
	case termproto.TKill:
		var m termproto.Kill
		if json.Unmarshal(data, &m) == nil {
			if s := l.mgr.Get(m.SID); s != nil {
				s.kill()
			}
		}
	case termproto.TRename:
		var m termproto.Rename
		if json.Unmarshal(data, &m) == nil {
			if s := l.mgr.Get(m.SID); s != nil {
				s.rename(m.Name)
			}
		}
	case termproto.TLifecycle:
		var m termproto.SetLifecycle
		if json.Unmarshal(data, &m) == nil {
			if s := l.mgr.Get(m.SID); s != nil {
				s.setLifecycle(m.IdleSec)
			}
		}
	case termproto.TPolicy:
		var m termproto.PolicyMsg
		if json.Unmarshal(data, &m) == nil {
			l.host.ApplyPolicy(m.Policy)
		}
	case termproto.TRun, termproto.TRunCancel, termproto.TRunAck, termproto.TRunRead, termproto.TRunsSync:
		l.handleRun(t, data)
	case TIPCList:
		l.sendSessions()
	case TIPCDetachAll:
		l.endAllChannels()
	case TIPCShutdown:
		var m IPCShutdown
		if json.Unmarshal(data, &m) == nil {
			l.host.RequestShutdown(m.Mode)
		}
	}
}

func (l *ctrl) onOpen(m termproto.Open) {
	if m.SSH != nil {
		// 建连要经过网络（最长十几秒），不能占着控制连接的读循环：输入、确认与其他会话的帧都要继续流动。
		go l.openSession(m)
		return
	}
	l.openSession(m)
}

func (l *ctrl) openSession(m termproto.Open) {
	info, err := l.mgr.OpenWith(OpenOptions{
		Name: m.Name, Rows: m.Rows, Cols: m.Cols, Incognito: m.Incognito, Compact: m.Compact, IdleSec: m.IdleSec, Cwd: m.Cwd, SSH: m.SSH,
	})
	if err != nil {
		// 日志与应答里只出现类别说明：SSH 目标含凭据，绝不能连同请求一起被打印。
		fail := termproto.OpenFailed{T: termproto.TOpenFailed, Req: m.Req, Reason: err.Error()}
		var oe *SSHOpenError
		if errors.As(err, &oe) {
			fail.Code, fail.Reason = oe.Code, oe.Msg
		}
		l.logf("新建会话失败: %s", fail.Reason)
		_ = l.sendJSON(fail)
		return
	}
	_ = l.sendJSON(termproto.Opened{T: termproto.TOpened, Req: m.Req, Session: info})
}

// onSSHOp 执行 Hub 发来的一次性 SSH 操作（登记 / 测试 / 重新确认指纹 / 凭据同步 / exec 代理），结果总会以 ssh_result 应答。
func (l *ctrl) onSSHOp(m termproto.SSHOp) {
	// 本机的一票否决（--deny-model-exec）：经网关向其他主机执行命令同样是「模型执行」，必须一并拦下。
	// Agent 的转发层也有同样的检查；这里是守护进程独立运行时的最后一道防线。
	if m.Op == termproto.SSHOpExec && l.host.exec != nil && l.host.exec.Denied() {
		l.logf("SSH 操作 exec 被本机否决（--deny-model-exec）")
		_ = l.sendJSON(termproto.SSHResult{T: termproto.TSSHResult, Req: m.Req,
			Code: termproto.SSHCodeExecDenied, Msg: "本机用 --deny-model-exec 禁止了模型执行"})
		return
	}
	res := l.mgr.SSHOp(m)
	if !res.OK {
		l.logf("SSH 操作 %s 失败: %s", m.Op, res.Code)
	}
	_ = l.sendJSON(res)
}

func (l *ctrl) onAttach(m termproto.Attach) {
	fail := func(reason string) {
		_ = l.sendJSON(termproto.AttachFailed{T: termproto.TAttachFailed, Ch: m.Ch, SID: m.SID, Reason: reason})
	}
	if !termproto.ValidSessionID(m.SID) {
		fail("bad session id")
		return
	}
	s := l.mgr.Get(m.SID)
	if s == nil {
		fail(errNoSuchSession.Error())
		return
	}
	if l.channel(m.Ch) != nil {
		fail(errChannelInUse.Error())
		return
	}
	res, err := s.attach(l, m.Ch, m.Since, m.Rows, m.Cols, m.TailLines)
	if err != nil {
		fail(err.Error())
		return
	}
	// 先登记通道（之后到达的输入 / 确认才能路由），再写 attached，最后才启动发送——输出帧必须排在应答之后。
	l.mu.Lock()
	l.chans[m.Ch] = res.ch
	l.mu.Unlock()
	if err := l.sendJSON(res.reply); err != nil {
		res.ch.end("")
		return
	}
	res.ch.start()
	if res.jiggle {
		go s.jiggle()
	}
}

// endAllChannels 静默结束本连接上的全部数据通道（会话继续运行）。
// 控制端与 Hub 的连接断开时用它清场，而不必断开与守护进程的连接。
func (l *ctrl) endAllChannels() {
	l.mu.Lock()
	list := make([]*channel, 0, len(l.chans))
	for _, c := range l.chans {
		list = append(list, c)
	}
	l.mu.Unlock()
	for _, c := range list {
		c.end("")
	}
}

// closeChannels 在连接结束时静默结束全部通道；会话本身继续运行。
func (l *ctrl) closeChannels() {
	l.closed.Store(true)
	l.endAllChannels()
}

// pingLoop 周期性发送本地心跳；超时无应答视为控制端卡死，关闭连接让读循环退出。
func (l *ctrl) pingLoop() {
	ticker := time.NewTicker(ipcPingInterval)
	defer ticker.Stop()
	for {
		select {
		case <-l.ctx.Done():
			return
		case <-ticker.C:
			ctx, cancel := context.WithTimeout(l.ctx, ipcPongTimeout)
			err := l.conn.Ping(ctx)
			cancel()
			if err != nil {
				_ = l.conn.Close()
				return
			}
		}
	}
}
