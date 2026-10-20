package holder

import (
	"encoding/base64"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"time"

	"github.com/teacat99/RelayMesh/v2/internal/agent/ptyhost"
	"github.com/teacat99/RelayMesh/v2/internal/agent/record"
	"github.com/teacat99/RelayMesh/v2/internal/termproto"
	"github.com/teacat99/RelayMesh/v2/internal/termstate"
)

const (
	// drainGrace 是子进程退出之后继续读取残余输出的最长时间。
	// 后台进程可能仍握着终端，所以读端不一定会自然结束，必须有界。
	drainGrace = 300 * time.Millisecond
	// inputQueueFrames 是每个会话待写入终端的输入帧队列长度（每帧至多 16 KiB）。
	inputQueueFrames = 256
)

var (
	errNoSuchSession = errors.New("no such session")
	errChannelInUse  = errors.New("channel already in use")
)

// exitInfo 记录进程的退出情况。
type exitInfo struct {
	code   *int
	signal string
}

// Session 是 Agent 上的一个终端会话：一个伪终端 + 其中运行的进程 + 回放缓冲。
//
// 生命周期与任何网络连接无关（INV-P1）：只有进程自身退出、被 kill、TTL 到期或 Agent 进程退出才会结束。
// 所有可变状态由 mu 保护；cond 用于唤醒等待输出 / 确认 / 状态变化的 goroutine。
// 持有 mu 时绝不做网络 I/O，也绝不调用可能再次加锁的方法。
type Session struct {
	id      string
	mgr     *Manager
	term    ptyhost.Terminal
	created time.Time
	shell   string
	pid     int
	target  string // 经 SSH 网关的会话：Hub 侧那台 SSH 主机的机器码；本机 shell 会话为空

	mu         sync.Mutex
	cond       *sync.Cond
	ring       *Ring
	modes      *ModeTracker
	scan       *termstate.Scanner
	screenKey  string    // 上一次推送给 Hub 的屏幕识别摘要（见 screenChanged）
	screenAt   time.Time // 上一次推送的时间
	name       string
	compact    bool // 精简会话：宽度不超过 CompactMaxCols（见 clampCols）
	rows, cols uint16
	state      string
	exit       exitInfo
	exitedAt   time.Time
	lastDetach time.Time // 附着数降为 0 的时刻；从未附着过则为创建时刻；有人附着时无意义
	lastInput  time.Time // 最后一次收到输入的时刻（空闲回收的「交互」之一）；从未有过则为零值
	idleSet    bool      // 本会话单独设置过生命周期（否则沿用主机默认）
	idleSec    int       // idleSet 时的空闲回收秒数；0 = 永久保持
	chans      map[*channel]struct{}
	draining   bool // 进程已退出：不再施加背压，尽快排空
	killed     bool
	removed    bool   // 已从管理器中移除
	why        string // 被终止的原因（killed / idle_ttl / policy / holder_stopped），写入记录的结束原因

	// 终端显示内容的本地记录。rec 为 nil 表示本会话没有被录制，recState 说明原因（veto / off / incognito / error）。
	rec      *record.Writer
	recState string

	inputQ   chan []byte
	readDone chan struct{}
	stop     chan struct{}
	stopOnce sync.Once
}

func newSession(m *Manager, id, name string, term ptyhost.Terminal, rows, cols uint16, shell string, ringSize int) *Session {
	s := &Session{
		id:         id,
		mgr:        m,
		term:       term,
		created:    m.now(),
		shell:      filepath.Base(shell),
		pid:        term.Pid(),
		ring:       NewRing(ringSize),
		modes:      NewModeTracker(),
		scan:       termstate.NewScanner(),
		name:       name,
		rows:       rows,
		cols:       cols,
		state:      termproto.SessionRunning,
		chans:      make(map[*channel]struct{}),
		inputQ:     make(chan []byte, inputQueueFrames),
		readDone:   make(chan struct{}),
		stop:       make(chan struct{}),
		lastDetach: m.now(),
	}
	s.cond = sync.NewCond(&s.mu)
	return s
}

// start 启动会话的后台协程。
func (s *Session) start() {
	go s.readLoop()
	go s.inputLoop()
	go s.monitor()
}

// ---------- 输出：PTY → 环形缓冲 ----------

// windowFullLocked 报告是否有附着方的在途数据已达窗口上限：此时暂停读取 PTY，
// 让内核 PTY 缓冲形成天然背压（例如 `cat` 大文件不会把浏览器撑爆，Ctrl-C 也能及时生效）。
// 没有附着方时永不阻塞：环形缓冲直接覆盖旧数据（INV-P3）。
func (s *Session) windowFullLocked() bool {
	if s.draining {
		return false
	}
	end := s.ring.End()
	for c := range s.chans {
		if end-c.acked >= termproto.AttachWindow {
			return true
		}
	}
	return false
}

// waitWindow 阻塞到可以继续读取 PTY；会话被回收时返回 false。
func (s *Session) waitWindow() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for !s.removed && s.windowFullLocked() {
		s.cond.Wait()
	}
	return !s.removed
}

func (s *Session) readLoop() {
	defer close(s.readDone)
	buf := make([]byte, termproto.MaxDataPayload)
	for {
		if !s.waitWindow() {
			return
		}
		n, err := s.term.Read(buf)
		if n > 0 {
			s.mu.Lock()
			s.modes.Feed(s.ring.End(), buf[:n])
			s.ring.Write(buf[:n])
			s.scan.Feed(s.mgr.now(), buf[:n])
			s.mu.Unlock()
			s.cond.Broadcast()
			s.rec.Output(buf[:n]) // 非阻塞：记录写盘慢或出错都不会影响终端
		}
		if err != nil {
			return
		}
	}
}

// monitor 等待进程退出，有界排空残余输出后把会话标记为 exited。
func (s *Session) monitor() {
	st := s.term.Wait()

	s.mu.Lock()
	s.draining = true
	s.mu.Unlock()
	s.cond.Broadcast()

	if err := s.term.SetReadDeadline(time.Now().Add(drainGrace)); err != nil {
		// 平台不支持读期限（Windows ConPTY）：超时后关闭终端来解除阻塞的 Read。
		time.AfterFunc(drainGrace, func() { _ = s.term.Close() })
	}
	<-s.readDone
	_ = s.term.Close()

	var code *int
	if st.Code >= 0 {
		c := st.Code
		code = &c
	}
	s.mu.Lock()
	s.state = termproto.SessionExited
	s.exit = exitInfo{code: code, signal: st.Signal}
	s.exitedAt = s.mgr.now()
	if len(s.chans) == 0 {
		s.lastDetach = s.exitedAt
	}
	killed := s.killed
	why := s.why
	s.mu.Unlock()
	s.cond.Broadcast()
	s.endRecord(code, st.Signal, killed, why)
	s.mgr.sessionExited(s, killed)
}

// endRecord 封存本会话的记录并写入结束原因。
func (s *Session) endRecord(code *int, signal string, killed bool, why string) {
	reason := "exit"
	if killed {
		reason = why
		if reason == "" {
			reason = "killed"
		}
	}
	s.rec.End(code, signal, reason)
}

// ---------- 输入：浏览器 → PTY ----------

// writeInput 把输入交给写协程；队列满（子进程长时间不读输入）时丢弃新输入，绝不阻塞连接的读循环。
func (s *Session) writeInput(p []byte) {
	if len(p) == 0 {
		return
	}
	s.mu.Lock()
	running := s.state == termproto.SessionRunning && !s.removed
	if running {
		s.lastInput = s.mgr.now()
	}
	s.mu.Unlock()
	if !running {
		return
	}
	cp := append([]byte(nil), p...)
	select {
	case s.inputQ <- cp:
	default:
	}
}

func (s *Session) inputLoop() {
	for {
		select {
		case <-s.stop:
			return
		case p := <-s.inputQ:
			if _, err := s.term.Write(p); err != nil {
				// 终端已关闭或进程已退出：丢弃后续输入即可，状态迁移由 monitor 负责。
				continue
			}
		}
	}
}

// ---------- 控制 ----------

// clampCols 精简会话把宽度限制在 CompactMaxCols 以内；普通会话原样返回。
func (s *Session) clampCols(cols uint16) uint16 {
	if s.compact && cols > termproto.CompactMaxCols {
		return termproto.CompactMaxCols
	}
	return cols
}

func (s *Session) resize(rows, cols uint16) {
	rows, cols = termproto.ClampSize(rows, cols)
	cols = s.clampCols(cols)
	s.mu.Lock()
	if s.state != termproto.SessionRunning || (rows == s.rows && cols == s.cols) {
		s.mu.Unlock()
		return
	}
	s.rows, s.cols = rows, cols
	s.mu.Unlock()
	_ = s.term.Resize(rows, cols)
	s.rec.Resize(rows, cols)
	s.mgr.changed()
}

func (s *Session) size() (uint16, uint16) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.rows, s.cols
}

// jiggle 通过「尺寸抖动」（少一行再恢复）触发 SIGWINCH，让刚被重新附着的全屏程序整屏重绘。
func (s *Session) jiggle() {
	rows, cols := s.size()
	switch {
	case rows > 1:
		_ = s.term.Resize(rows-1, cols)
	case cols > 1:
		_ = s.term.Resize(rows, cols-1)
	default:
		return
	}
	select {
	case <-time.After(25 * time.Millisecond):
	case <-s.stop:
		return
	}
	_ = s.term.Resize(rows, cols)
}

func (s *Session) rename(name string) {
	name = termproto.SanitizeText(name, termproto.MaxSessionNameRunes)
	s.mu.Lock()
	s.name = name
	s.mu.Unlock()
	s.rec.SetName(name)
	s.mgr.changed()
}

// lastActivityLocked 返回最后一次交互的时刻：最后一次输入与最后一次断开中较晚者。调用方持有 s.mu。
func (s *Session) lastActivityLocked() time.Time {
	if s.lastInput.After(s.lastDetach) {
		return s.lastInput
	}
	return s.lastDetach
}

// setLifecycle 修改本会话的生命周期：nil 表示改回沿用主机默认，0 表示永久保持。
// 修改本身算一次交互：把空闲时限从「永久」改成「1 小时」不应让已经空闲很久的会话立刻被回收。
func (s *Session) setLifecycle(idle *int) {
	s.mu.Lock()
	if s.removed {
		s.mu.Unlock()
		return
	}
	if idle == nil {
		s.idleSet, s.idleSec = false, 0
	} else {
		s.idleSet, s.idleSec = true, termproto.ClampIdle(*idle)
	}
	s.lastInput = s.mgr.now()
	s.mu.Unlock()
	s.mgr.changed()
}

// kill 终止会话。运行中：挂断进程，随后由 monitor 收尾并从管理器移除；已退出：直接移除。
func (s *Session) kill() { s.killFor("killed") }

// killFor 终止会话并记下原因（写入记录的结束原因）；首个原因生效。
func (s *Session) killFor(why string) {
	s.mu.Lock()
	s.killed = true
	if s.why == "" {
		s.why = why
	}
	exited := s.state == termproto.SessionExited
	s.mu.Unlock()
	if exited {
		s.mgr.remove(s)
		return
	}
	s.term.Kill()
}

// teardown 结束会话的后台协程并关闭终端（会话已从管理器移除之后调用）。幂等。
func (s *Session) teardown() {
	s.stopOnce.Do(func() {
		s.mu.Lock()
		s.removed = true
		var open []*channel
		for c := range s.chans {
			open = append(open, c)
		}
		s.mu.Unlock()
		close(s.stop)
		s.cond.Broadcast()
		s.term.Kill()
		for _, c := range open {
			c.end("killed")
		}
		_ = s.term.Close()
	})
}

// ---------- 快照 ----------

func (s *Session) info() termproto.SessionInfo {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.infoLocked()
}

func (s *Session) infoLocked() termproto.SessionInfo {
	alt, _ := s.modes.Alt()
	info := termproto.SessionInfo{
		SID:      s.id,
		Name:     s.name,
		Rows:     s.rows,
		Cols:     s.cols,
		Base:     s.ring.Base(),
		End:      s.ring.End(),
		State:    s.state,
		Created:  s.created.Unix(),
		Attached: len(s.chans),
		Shell:    s.shell,
		PID:      s.pid,
		Target:   s.target,
		Alt:      alt && s.state == termproto.SessionRunning,
	}
	if s.state == termproto.SessionRunning {
		now := s.mgr.now()
		info.Screen = s.scan.Wire(now)
		cfg := s.mgr.idle()
		info.Idle = int(s.effectiveIdleLocked(cfg.ceiling, cfg.hostIdle) / time.Second)
		info.IdleOwn = s.idleSet
		if len(s.chans) > 0 {
			info.Active = now.Unix()
		} else {
			info.Active = s.lastActivityLocked().Unix()
		}
	}
	if s.state == termproto.SessionExited {
		info.ExitCode = s.exit.code
	}
	if s.rec == nil {
		info.Rec = s.recState
		return info
	}
	st := s.rec.Stats()
	info.Rec = st.State
	info.RecBytes = st.Raw
	if st.FirstMS > 0 {
		info.RecFirst = st.FirstMS / 1000
	}
	if st.LastMS > 0 {
		info.RecLast = st.LastMS / 1000
	}
	info.RecGap = st.Gap
	return info
}

// ---------- 附着 ----------

// attachResult 是附着的结果：应答帧、数据通道，以及附着完成后是否需要触发重绘。
type attachResult struct {
	reply  termproto.Attached
	ch     *channel
	jiggle bool
}

// attach 把一条数据通道附着到会话，并按 since 计算回放起点与模式恢复序列。
//
//   - since 早于缓冲头：从头开始并标记 truncated（缺口必须显式，INV-P2）；
//   - since == 0（新终端，没有任何状态）：回放缓冲，先恢复 DEC 私有模式；
//     会话正处于备用屏（vim / htop / tmux）时只回放自进入备用屏以来的内容，并在附着后触发一次重绘；
//     主屏上缓冲已经很大、会话记录正常录制、浏览器又给了 tailLines 时，只回放末尾这么多行（更早的由浏览器按需读记录）；
//   - since > 0 且仍在缓冲内（断线续传）：从 since 续传，终端状态完好，不发恢复序列。
func (s *Session) attach(sink frameSink, chID uint32, since uint64, rows, cols uint16, tailLines uint16) (attachResult, error) {
	s.mu.Lock()
	if s.removed {
		s.mu.Unlock()
		return attachResult{}, errNoSuchSession
	}

	cols = s.clampCols(cols)
	var resizeTo *[2]uint16
	if s.state == termproto.SessionRunning && rows > 0 && cols > 0 && (rows != s.rows || cols != s.cols) {
		s.rows, s.cols = rows, cols
		resizeTo = &[2]uint16{rows, cols}
	}

	base, end := s.ring.Base(), s.ring.End()
	alt, altEnter := s.modes.Alt()
	running := s.state == termproto.SessionRunning

	var from uint64
	truncated := false
	switch {
	case since > end:
		from = end
	case since < base:
		from = base
		truncated = true
	default:
		from = since
	}

	var prefix []byte
	jiggle := false
	if since == 0 || truncated {
		if alt && running {
			if altEnter >= base {
				from, truncated = altEnter, false // 有意跳过主屏历史，不算「被覆盖」
			} else {
				from, truncated = s.ring.AlignForReplay(base), true
			}
			prefix = s.modes.Prefix(true)
			jiggle = true
		} else {
			// 只有全新附着才裁剪（落后太多的重连，truncated，仍整体回放），并且只有会话记录正在正常录制时才裁剪：
			// 被跳过的更早输出得确实能从记录里读到，否则裁掉就是丢内容。
			tail := 0
			if since == 0 && s.rec != nil && s.rec.Stats().State == record.StateOn {
				tail = min(int(tailLines), termproto.MaxTailLines)
			}
			var trimmed bool
			from, trimmed = s.ring.ReplayStart(from, tail, termproto.TailMinBytes)
			if trimmed {
				truncated = false // 有意跳过（浏览器可从记录里按需读到），不算「被覆盖」
			}
			prefix = s.modes.Prefix(false)
		}
	}

	c := &channel{
		sess:  s,
		sink:  sink,
		id:    chID,
		sent:  from,
		acked: from,
		done:  make(chan struct{}),
	}
	s.chans[c] = struct{}{}

	reply := termproto.Attached{
		T:         termproto.TAttached,
		Ch:        chID,
		SID:       s.id,
		Base:      base,
		End:       end,
		From:      from,
		Truncated: truncated,
		Rows:      s.rows,
		Cols:      s.cols,
		State:     s.state,
	}
	if len(prefix) > 0 {
		reply.Prefix = base64.StdEncoding.EncodeToString(prefix)
	}
	if s.state == termproto.SessionExited {
		reply.ExitCode = s.exit.code
	}
	s.mu.Unlock()

	if resizeTo != nil {
		_ = s.term.Resize(resizeTo[0], resizeTo[1])
	}
	s.mgr.changed()
	return attachResult{reply: reply, ch: c, jiggle: jiggle}, nil
}

// checkStalls 检查附着方是否长时间不确认：窗口耗尽且 StallTimeout 内没有任何确认进展的通道被断开，
// 避免一个卡死的浏览器拖住会话的输出。返回被断开的通道数。
func (s *Session) checkStalls(now time.Time) int {
	var stalled []*channel
	s.mu.Lock()
	end := s.ring.End()
	for c := range s.chans {
		if end-c.acked < termproto.AttachWindow {
			c.fullSince = time.Time{}
			continue
		}
		if c.fullSince.IsZero() {
			c.fullSince = now
			continue
		}
		if now.Sub(c.fullSince) >= termproto.StallTimeout {
			stalled = append(stalled, c)
		}
	}
	s.mu.Unlock()
	for _, c := range stalled {
		c.end("stalled")
	}
	return len(stalled)
}

// screenRefreshEvery 是「屏幕仍在动画」时重复推送识别结果的最短间隔：状态词没变也要让 Hub 看到 token 计数在走。
const screenRefreshEvery = 10 * time.Second

// screenChanged 判断屏幕识别结果相对上一次推送是否值得再推一次：摘要（状态词、审批、中断、动画、上下文整数百分比、是否认出）
// 变了，或者仍在动画且距上次推送已超过 screenRefreshEvery。返回 true 时已把摘要记为「已推送」。
// 摘要故意不含 token 数与字节速率，否则动画期间每秒都会推。
func (s *Session) screenChanged(now time.Time) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state != termproto.SessionRunning {
		return false
	}
	sc := s.scan.Wire(now)
	key := ""
	if sc != nil {
		key = fmt.Sprintf("%s|%t|%t|%t|%d", sc.Word, sc.Approval, sc.Cancelled, sc.Animating, int(sc.CtxPct))
	}
	if key == s.screenKey && !(sc != nil && sc.Animating && now.Sub(s.screenAt) >= screenRefreshEvery) {
		return false
	}
	s.screenKey, s.screenAt = key, now
	return true
}
