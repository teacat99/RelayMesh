package terminal

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
	"github.com/teacat99/RelayMesh/v2/internal/sshexec"
	"github.com/teacat99/RelayMesh/v2/internal/termproto"
	"github.com/teacat99/RelayMesh/v2/internal/termstate"
)

// Hub 本地会话：Hub 直连的 SSH 主机的人工终端。
//
// 经网关接入的 SSH 主机，终端会话由网关主机上的会话守护进程持有（持久、有记录）。Hub 直连的 SSH 主机没有这样的守护进程：
// 由 Hub 自己出站建立 SSH 连接、申请伪终端，并把这个会话持有在 Hub 进程里——环形缓冲、按偏移续传、备用屏恢复、
// 流控与停滞判定都与会话守护进程一致（环形缓冲与模式跟踪器共用 internal/termstate；浏览器一侧复用同一个 attachment，
// 协议完全相同），所以刷新页面、换设备都能接着用，界面上也没有第二套终端。
//
// 与守护进程持有的会话相比，有意保留的差别：
//   - 不记录终端内容、也不落盘：会话只存在于 Hub 的内存里，Hub 重启即结束（SSH 连接随进程一起关闭）；
//   - 同一台主机最多 maxLocalSessionsPerHost 个会话，全部直连主机合计最多 maxLocalSessionsTotal 个；
//   - 登录凭据仍加密保存在 Hub（RELAYMESH_SSH_SECRET_KEY），只在建立连接的那一刻解开，不进入会话对象。
//
// 锁顺序：localEngine.mu → localHost.mu → localSession.mu。任何持锁区间内都不会调用可能阻塞的 SSH / WebSocket 操作；
// attachment 的 deliver / sendControl / endFromAgent 都是非阻塞入队，可以在持锁时调用。
const (
	maxLocalSessionsPerHost = 8
	maxLocalSessionsTotal   = 32

	// localExitedKeep 是已退出、且没有人附着的会话在清单里的保留时长，过后自动清出（人也可以随时手动清除）。
	localExitedKeep = time.Hour
	// localMaintainEvery 是空闲回收与过期清理的检查间隔。
	localMaintainEvery = 15 * time.Second
	// localInputQueue 是每个会话待写入远端的输入队列长度；远端长时间不读输入时丢弃新输入，绝不阻塞浏览器连接的读循环。
	localInputQueue = 256
)

var errLocalClosed = errors.New("terminal: local session engine closed")

// localEngine 管理 Hub 直连 SSH 主机上的人工终端会话。
type localEngine struct {
	h *Hub

	mu     sync.Mutex
	hosts  map[string]*localHost
	total  int // 已占用的会话名额（含正在建立的）
	closed bool
	last   time.Time // 上一次维护的时刻
}

func newLocalEngine(h *Hub) *localEngine {
	return &localEngine{h: h, hosts: make(map[string]*localHost)}
}

// host 返回某台直连 SSH 主机（机器码）的会话集合，不存在则创建。
func (e *localEngine) host(code string) *localHost {
	e.mu.Lock()
	defer e.mu.Unlock()
	lh := e.hosts[code]
	if lh == nil {
		lh = &localHost{e: e, code: code, sessions: make(map[string]*localSession), chans: make(map[uint32]*localChan)}
		e.hosts[code] = lh
	}
	return lh
}

// peek 返回已存在的会话集合；这台主机从没开过本地会话时返回 nil。
func (e *localEngine) peek(code string) *localHost {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.hosts[code]
}

func (e *localEngine) snapshotHosts() []*localHost {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]*localHost, 0, len(e.hosts))
	for _, lh := range e.hosts {
		out = append(out, lh)
	}
	return out
}

func (e *localEngine) reserve() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		return errLocalClosed
	}
	if e.total >= maxLocalSessionsTotal {
		return errSessionLimit
	}
	e.total++
	return nil
}

func (e *localEngine) release() {
	e.mu.Lock()
	if e.total > 0 {
		e.total--
	}
	e.mu.Unlock()
}

func (e *localEngine) isClosed() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.closed
}

// list 返回一台主机上的本地会话清单（按创建先后）；没有则为空切片。
func (e *localEngine) list(code string) []termproto.SessionInfo {
	if lh := e.peek(code); lh != nil {
		return lh.list()
	}
	return []termproto.SessionInfo{}
}

// endHost 终止一台主机上的全部本地会话（停用 / 删除 / 改了连接信息之后调用），返回涉及的会话数。
func (e *localEngine) endHost(code string) int {
	lh := e.peek(code)
	if lh == nil {
		return 0
	}
	n := 0
	for _, s := range lh.snapshot() {
		s.kill()
		n++
	}
	return n
}

// endAll 终止全部本地会话（总闸关闭时调用：终端能力整体停用，Hub 不应继续替人开着 SSH 连接）。
func (e *localEngine) endAll() {
	for _, lh := range e.snapshotHosts() {
		for _, s := range lh.snapshot() {
			s.kill()
		}
	}
}

// closeAttachments 断开全部浏览器附着（会话本身不受影响）。
func (e *localEngine) closeAttachments(code websocket.StatusCode, reason string) {
	for _, lh := range e.snapshotHosts() {
		lh.closeAttachments(code, reason)
	}
}

// maintain 执行空闲回收与过期清理；由 Hub 的维护循环每秒调用，内部自行限频。
func (e *localEngine) maintain(now time.Time) {
	e.mu.Lock()
	if e.closed || now.Sub(e.last) < localMaintainEvery {
		e.mu.Unlock()
		return
	}
	e.last = now
	e.mu.Unlock()
	for _, lh := range e.snapshotHosts() {
		for _, s := range lh.snapshot() {
			s.maintain(now)
		}
	}
}

// shutdown 关闭引擎：立即断开全部浏览器附着并结束全部会话（SSH 连接随之关闭）。
func (e *localEngine) shutdown() {
	e.mu.Lock()
	e.closed = true
	hosts := make([]*localHost, 0, len(e.hosts))
	for _, lh := range e.hosts {
		hosts = append(hosts, lh)
	}
	e.mu.Unlock()
	for _, lh := range hosts {
		lh.closeAttachments(websocket.StatusGoingAway, "server shutting down")
		for _, s := range lh.snapshot() {
			lh.remove(s)
		}
	}
}

// ---------- 主机级 ----------

// localChan 是一条浏览器数据通道：附着登记时分配通道号，收到 Attach 帧后才绑定到具体会话。
type localChan struct {
	att *attachment
	s   *localSession
}

// localHost 是一台直连 SSH 主机上的本地会话集合，同时实现 attachPeer——浏览器附着把它当作「一条 Agent 连接」使用。
type localHost struct {
	e    *localEngine
	code string

	// hostIdle 是这台主机上新会话默认的空闲回收秒数（0 = 永久保持），来自主机记录，修改后由 setHostIdle 同步。
	hostIdle atomic.Int64

	mu       sync.Mutex
	sessions map[string]*localSession
	order    []string // 会话 ID，按创建先后
	chans    map[uint32]*localChan
	nextCh   uint32
	opening  int // 正在建立（已占名额、尚未登记）的会话数
}

func (lh *localHost) setHostIdle(sec int) { lh.hostIdle.Store(int64(termproto.ClampIdle(sec))) }

func (lh *localHost) snapshot() []*localSession {
	lh.mu.Lock()
	defer lh.mu.Unlock()
	out := make([]*localSession, 0, len(lh.order))
	for _, sid := range lh.order {
		if s := lh.sessions[sid]; s != nil {
			out = append(out, s)
		}
	}
	return out
}

func (lh *localHost) get(sid string) *localSession {
	lh.mu.Lock()
	defer lh.mu.Unlock()
	return lh.sessions[sid]
}

// list 返回会话清单（按创建先后）。
func (lh *localHost) list() []termproto.SessionInfo {
	ss := lh.snapshot()
	out := make([]termproto.SessionInfo, 0, len(ss))
	for _, s := range ss {
		out = append(out, s.info())
	}
	return out
}

func (lh *localHost) find(sid string) (termproto.SessionInfo, bool) {
	s := lh.get(sid)
	if s == nil {
		return termproto.SessionInfo{}, false
	}
	return s.info(), true
}

// sessionInfo 实现 attachPeer。
func (lh *localHost) sessionInfo(sid string) (termproto.SessionInfo, bool) { return lh.find(sid) }

func (lh *localHost) reserve() error {
	lh.mu.Lock()
	if len(lh.sessions)+lh.opening >= maxLocalSessionsPerHost {
		lh.mu.Unlock()
		return errSessionLimit
	}
	lh.opening++
	lh.mu.Unlock()
	if err := lh.e.reserve(); err != nil {
		lh.mu.Lock()
		lh.opening--
		lh.mu.Unlock()
		return err
	}
	return nil
}

// localOpen 是新建本地会话的参数。
type localOpen struct {
	Name string
	Rows uint16
	Cols uint16
	// IdleSec 为 nil 沿用主机默认；0 永久保持。
	IdleSec *int
	// Dial 建立一条已认证的 SSH 连接；成功后连接的所有权交给会话（会话结束时关闭）。
	Dial func(ctx context.Context) (*sshexec.Client, error)
}

// open 建立 SSH 连接、申请伪终端并登记一个新会话。
func (lh *localHost) open(ctx context.Context, o localOpen) (termproto.SessionInfo, error) {
	if err := lh.reserve(); err != nil {
		return termproto.SessionInfo{}, err
	}
	committed := false
	defer func() {
		if !committed {
			lh.mu.Lock()
			lh.opening--
			lh.mu.Unlock()
			lh.e.release()
		}
	}()

	cl, err := o.Dial(ctx)
	if err != nil {
		return termproto.SessionInfo{}, err
	}
	rows, cols := termproto.ClampSize(o.Rows, o.Cols)
	sh, err := cl.OpenShell(sshexec.ShellOptions{Rows: rows, Cols: cols}) // 失败时已关闭连接
	if err != nil {
		return termproto.SessionInfo{}, err
	}
	sid, err := newLocalSessionID()
	if err != nil {
		_ = sh.Close()
		return termproto.SessionInfo{}, err
	}

	now := lh.e.h.now()
	s := &localSession{
		lh:        lh,
		sid:       sid,
		name:      termproto.SanitizeText(o.Name, termproto.MaxSessionNameRunes),
		created:   now,
		rows:      rows,
		cols:      cols,
		state:     termproto.SessionRunning,
		ring:      termstate.NewRing(termproto.RingSize),
		modes:     termstate.NewModeTracker(),
		atts:      make(map[*attachment]struct{}),
		lastInput: now, lastDetach: now,
		shell:  sh,
		inputQ: make(chan []byte, localInputQueue),
		stop:   make(chan struct{}),
	}
	if o.IdleSec != nil {
		s.idleSet, s.idleSec = true, termproto.ClampIdle(*o.IdleSec)
	}

	lh.mu.Lock()
	if lh.e.isClosed() {
		lh.mu.Unlock()
		_ = sh.Close()
		return termproto.SessionInfo{}, errLocalClosed
	}
	lh.sessions[sid] = s
	lh.order = append(lh.order, sid)
	lh.opening--
	lh.mu.Unlock()
	committed = true

	go s.readLoop()
	go s.inputLoop()
	return s.info(), nil
}

// remove 把会话从清单里清出并释放它占用的资源（幂等）。
func (lh *localHost) remove(s *localSession) {
	lh.mu.Lock()
	if _, ok := lh.sessions[s.sid]; !ok {
		lh.mu.Unlock()
		return
	}
	delete(lh.sessions, s.sid)
	for i, sid := range lh.order {
		if sid == s.sid {
			lh.order = append(lh.order[:i], lh.order[i+1:]...)
			break
		}
	}
	lh.mu.Unlock()
	lh.e.release()
	s.teardown()
}

func (lh *localHost) closeAttachments(code websocket.StatusCode, reason string) {
	lh.mu.Lock()
	list := make([]*attachment, 0, len(lh.chans))
	for _, c := range lh.chans {
		list = append(list, c.att)
	}
	lh.mu.Unlock()
	for _, att := range list {
		att.end(code, reason)
	}
}

// ---------- attachPeer：浏览器附着把这里当作一条 Agent 连接 ----------

func (lh *localHost) addAttachment(att *attachment) error {
	lh.mu.Lock()
	defer lh.mu.Unlock()
	if len(lh.chans) >= 4*termproto.MaxSessionsPerHost {
		return errors.New("too many attachments on this host")
	}
	att.ch = lh.nextCh
	lh.nextCh++
	lh.chans[att.ch] = &localChan{att: att}
	return nil
}

// removeAttachment 摘掉通道，并让它所附着的会话同步忘掉这个附着。
//
// 附着收尾时的顺序是「先 removeAttachment、再发 Detach」：对 Agent 来说通道表只是 Hub 一侧的记账，
// Detach 由 Agent 进程自己按通道号处理；本地会话没有那一层，通道摘掉之后 Detach 再也找不到会话，
// 所以会话侧的摘除必须在这里完成，否则附着数永远不归零、空闲回收也永远不会开始计时。
func (lh *localHost) removeAttachment(ch uint32) {
	lh.mu.Lock()
	var s *localSession
	var att *attachment
	if c := lh.chans[ch]; c != nil {
		s, att = c.s, c.att
	}
	delete(lh.chans, ch)
	lh.mu.Unlock()
	if s != nil {
		s.detach(att)
	}
}

// writeJSON 处理浏览器附着发来的控制帧：Attach（同步应答）、Detach、Resize。
func (lh *localHost) writeJSON(v any) error {
	switch m := v.(type) {
	case termproto.Attach:
		return lh.attach(m)
	case termproto.Detach:
		lh.detach(m.Ch)
	case termproto.Resize:
		if s := lh.get(m.SID); s != nil {
			s.resize(m.Rows, m.Cols)
		}
	}
	return nil
}

// writeBinary 处理输入帧；确认帧不需要处理——附着自己记录了确认进度，输出循环按它判断窗口是否已满。
func (lh *localHost) writeBinary(data []byte) error {
	typ, err := termproto.FrameType(data)
	if err != nil || typ != termproto.FrameInput {
		return nil
	}
	ch, payload, err := termproto.DecodeInput(data)
	if err != nil {
		return nil
	}
	lh.mu.Lock()
	c := lh.chans[ch]
	lh.mu.Unlock()
	if c == nil || c.s == nil {
		return errors.New("no such channel")
	}
	c.s.writeInput(payload)
	return nil
}

func (lh *localHost) attach(m termproto.Attach) error {
	lh.mu.Lock()
	c := lh.chans[m.Ch]
	s := lh.sessions[m.SID]
	if c != nil && s != nil {
		c.s = s
	}
	lh.mu.Unlock()
	if c == nil {
		return errors.New("no such channel")
	}
	if s == nil || !s.attach(c.att, m.Since, m.Rows, m.Cols) {
		c.att.onAttachFailed(termproto.AttachFailed{T: termproto.TAttachFailed, Ch: m.Ch, SID: m.SID, Reason: "session not found"})
	}
	return nil
}

func (lh *localHost) detach(ch uint32) {
	lh.mu.Lock()
	var s *localSession
	var att *attachment
	if c := lh.chans[ch]; c != nil {
		s, att = c.s, c.att
	}
	lh.mu.Unlock()
	if s != nil {
		s.detach(att)
	}
}

// ---------- 会话 ----------

// localSession 是 Hub 持有的一个 SSH 人工终端会话。
type localSession struct {
	lh      *localHost
	sid     string
	created time.Time

	shell  *sshexec.Shell
	inputQ chan []byte

	stop     chan struct{}
	stopOnce sync.Once

	mu        sync.Mutex
	name      string
	rows      uint16
	cols      uint16
	state     string
	exit      *int
	exitedAt  time.Time
	ring      *termstate.Ring
	modes     *termstate.ModeTracker
	atts      map[*attachment]struct{}
	idleSet   bool
	idleSec   int
	lastInput time.Time
	// lastDetach 是最近一次「附着数降为 0」的时刻；有人附着时空闲计时不会走。
	lastDetach time.Time
	killed     bool
	removed    bool
}

func newLocalSessionID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

func (s *localSession) now() time.Time { return s.lh.e.h.now() }

func (s *localSession) info() termproto.SessionInfo {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.infoLocked()
}

func (s *localSession) infoLocked() termproto.SessionInfo {
	alt, _ := s.modes.Alt()
	info := termproto.SessionInfo{
		SID:      s.sid,
		Name:     s.name,
		Rows:     s.rows,
		Cols:     s.cols,
		Base:     s.ring.Base(),
		End:      s.ring.End(),
		State:    s.state,
		Created:  s.created.Unix(),
		Attached: len(s.atts),
		Shell:    "ssh",
		Alt:      alt && s.state == termproto.SessionRunning,
		// Hub 本地会话不记录终端内容（见文件头说明）。
		Rec: termproto.RecOff,
	}
	if s.state == termproto.SessionRunning {
		info.Idle = int(s.effectiveIdleLocked() / time.Second)
		info.IdleOwn = s.idleSet
		if len(s.atts) > 0 {
			info.Active = s.now().Unix()
		} else {
			info.Active = s.lastActivityLocked().Unix()
		}
	} else {
		info.ExitCode = s.exit
	}
	return info
}

func (s *localSession) effectiveIdleLocked() time.Duration {
	if s.idleSet {
		return time.Duration(s.idleSec) * time.Second
	}
	return time.Duration(s.lh.hostIdle.Load()) * time.Second
}

func (s *localSession) lastActivityLocked() time.Time {
	if s.lastInput.After(s.lastDetach) {
		return s.lastInput
	}
	return s.lastDetach
}

func (s *localSession) attList() []*attachment {
	out := make([]*attachment, 0, len(s.atts))
	for a := range s.atts {
		out = append(out, a)
	}
	return out
}

// ---------- 输出：SSH → 环形缓冲 → 浏览器 ----------

// windowFull 报告是否有附着的在途字节已经用满流控窗口。先读确认进度再读已发送进度：
// 两者都只增不减，且确认不会超过已发送，这个顺序保证差值不会下溢。
func (s *localSession) windowFull() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for a := range s.atts {
		acked := a.acked.Load()
		sent := a.sentEnd.Load()
		if sent > acked && sent-acked >= termproto.AttachWindow {
			return true
		}
	}
	return false
}

// waitWindow 阻塞到可以继续读取 SSH 输出；会话被清出时返回 false。
// 停滞的附着会被它自己的停滞检查断开并从 atts 里摘掉，所以这里不会永远等下去。
func (s *localSession) waitWindow() bool {
	for {
		s.mu.Lock()
		removed := s.removed
		s.mu.Unlock()
		if removed {
			return false
		}
		if !s.windowFull() {
			return true
		}
		select {
		case <-s.stop:
			return false
		case <-time.After(5 * time.Millisecond):
		}
	}
}

func (s *localSession) readLoop() {
	buf := make([]byte, termproto.MaxDataPayload)
	for {
		if !s.waitWindow() {
			return
		}
		n, err := s.shell.Read(buf)
		if n > 0 {
			s.emit(buf[:n])
		}
		if err != nil {
			break
		}
	}
	s.finish()
}

// emit 把一段输出写入环形缓冲并转发给全部附着。取附着快照与写缓冲在同一把锁内完成，
// 与 attach 的「回放 + 登记」互斥，所以新附着既不会漏掉也不会重复这一段。
func (s *localSession) emit(p []byte) {
	s.mu.Lock()
	off := s.ring.End()
	s.modes.Feed(off, p)
	s.ring.Write(p)
	atts := s.attList()
	s.mu.Unlock()
	for _, a := range atts {
		a.deliver(off, p)
	}
}

// finish 在 SSH 会话结束后收尾：记录结束状态，通知仍附着的浏览器；被终止的会话随即清出清单。
func (s *localSession) finish() {
	ex := s.shell.Wait()
	_ = s.shell.Close()

	var code *int
	if ex.Code >= 0 {
		c := ex.Code
		code = &c
	}
	s.mu.Lock()
	if s.removed {
		s.mu.Unlock()
		return
	}
	s.state = termproto.SessionExited
	s.exit = code
	s.exitedAt = s.now()
	if len(s.atts) == 0 {
		s.lastDetach = s.exitedAt
	}
	killed := s.killed
	atts := s.attList()
	s.mu.Unlock()

	frame := termproto.BrowserExit{T: termproto.TExit, Code: code, Signal: termproto.SanitizeText(ex.Signal, 32)}
	reason := "exited"
	if killed {
		reason = "killed"
	}
	for _, a := range atts {
		a.sendControl(frame)
		a.endFromAgent(reason)
	}
	if killed {
		s.lh.remove(s)
	}
}

// ---------- 附着 ----------

// attach 把一条浏览器数据通道附着到会话，并按 since 计算回放起点与模式恢复序列（语义与会话守护进程一致）：
//   - since 早于缓冲头：从头开始并标记 truncated（缺口必须显式）；
//   - since == 0（新终端，没有任何状态）：回放缓冲，先恢复 DEC 私有模式；
//     会话正处于备用屏（vim / htop / tmux）时只回放自进入备用屏以来的内容，并在附着后触发一次重绘；
//   - since > 0 且仍在缓冲内（断线续传）：从 since 续传，终端状态完好，不发恢复序列。
//
// 回放在持有会话锁时入队，所以回放与随后的实时输出严格有序。返回 false 表示会话已被清出。
// Hub 本地会话没有会话记录：更早的输出只剩这块缓冲，所以不做「只回放末尾若干行」的裁剪（那是 Agent 会话的优化）。
func (s *localSession) attach(att *attachment, since uint64, rows, cols uint16) bool {
	s.mu.Lock()
	if s.removed {
		s.mu.Unlock()
		return false
	}
	running := s.state == termproto.SessionRunning
	var resizeTo *[2]uint16
	if running && rows > 0 && cols > 0 && (rows != s.rows || cols != s.cols) {
		s.rows, s.cols = rows, cols
		resizeTo = &[2]uint16{rows, cols}
	}

	base, end := s.ring.Base(), s.ring.End()
	alt, altEnter := s.modes.Alt()

	var from uint64
	truncated := false
	switch {
	case since > end:
		from = end
	case since < base:
		from, truncated = base, true
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
			from = s.ring.AlignForReplay(from)
			prefix = s.modes.Prefix(false)
		}
	}

	reply := termproto.Attached{
		T: termproto.TAttached, Ch: att.ch, SID: s.sid,
		Base: base, End: end, From: from, Truncated: truncated,
		Rows: s.rows, Cols: s.cols, State: s.state,
	}
	if len(prefix) > 0 {
		reply.Prefix = base64.StdEncoding.EncodeToString(prefix)
	}
	if !running {
		reply.ExitCode = s.exit
	}
	s.atts[att] = struct{}{}

	// ready 必须先于后续输出入队；回放与实时输出都在会话锁下入队，不会交错。
	att.onAttached(reply)
	buf := make([]byte, termproto.MaxDataPayload)
	for off := from; off < end; {
		n, err := s.ring.Read(off, buf)
		if err != nil || n == 0 {
			break
		}
		att.deliver(off, buf[:n]) // deliver 会复制净荷，buf 可以复用
		off += uint64(n)
	}
	exitFrame := termproto.BrowserExit{T: termproto.TExit, Code: s.exit}
	s.mu.Unlock()

	if resizeTo != nil {
		_ = s.shell.Resize(resizeTo[0], resizeTo[1])
	}
	if jiggle {
		go s.jiggle()
	}
	if !running {
		// 已经结束的会话：补发退出通知后关闭这条通道，浏览器据此显示「已结束」。
		att.sendControl(exitFrame)
		att.endFromAgent("exited")
	}
	return true
}

func (s *localSession) detach(att *attachment) {
	s.mu.Lock()
	if _, ok := s.atts[att]; ok {
		delete(s.atts, att)
		if len(s.atts) == 0 {
			s.lastDetach = s.now()
		}
	}
	s.mu.Unlock()
}

// jiggle 通过「尺寸抖动」（少一行再恢复）触发 SIGWINCH，让刚被重新附着的全屏程序整屏重绘。
func (s *localSession) jiggle() {
	s.mu.Lock()
	rows, cols := s.rows, s.cols
	s.mu.Unlock()
	switch {
	case rows > 1:
		_ = s.shell.Resize(rows-1, cols)
	case cols > 1:
		_ = s.shell.Resize(rows, cols-1)
	default:
		return
	}
	select {
	case <-time.After(25 * time.Millisecond):
	case <-s.stop:
		return
	}
	_ = s.shell.Resize(rows, cols)
}

// ---------- 输入与控制 ----------

// writeInput 把输入交给写协程；队列满（远端长时间不读输入）时丢弃新输入，绝不阻塞浏览器连接的读循环。
func (s *localSession) writeInput(p []byte) {
	if len(p) == 0 {
		return
	}
	s.mu.Lock()
	running := s.state == termproto.SessionRunning && !s.removed
	if running {
		s.lastInput = s.now()
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

func (s *localSession) inputLoop() {
	for {
		select {
		case <-s.stop:
			return
		case p := <-s.inputQ:
			// 连接已断开或会话已结束：丢弃输入即可，状态迁移由 finish 负责。
			_, _ = s.shell.Write(p)
		}
	}
}

func (s *localSession) resize(rows, cols uint16) {
	rows, cols = termproto.ClampSize(rows, cols)
	s.mu.Lock()
	if s.state != termproto.SessionRunning || (rows == s.rows && cols == s.cols) {
		s.mu.Unlock()
		return
	}
	s.rows, s.cols = rows, cols
	s.mu.Unlock()
	_ = s.shell.Resize(rows, cols)
}

func (s *localSession) rename(name string) {
	name = termproto.SanitizeText(name, termproto.MaxSessionNameRunes)
	s.mu.Lock()
	s.name = name
	s.mu.Unlock()
}

// setLifecycle 修改本会话的空闲回收时长：nil 表示改回沿用主机默认，0 表示永久保持。
// 修改本身算一次交互：把「永久」改成「1 小时」不应让已经空闲很久的会话立刻被回收。
// 返回 false 表示会话已经结束，不能再改。
func (s *localSession) setLifecycle(idle *int) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.removed || s.state != termproto.SessionRunning {
		return false
	}
	if idle == nil {
		s.idleSet, s.idleSec = false, 0
	} else {
		s.idleSet, s.idleSec = true, termproto.ClampIdle(*idle)
	}
	s.lastInput = s.now()
	return true
}

// kill 终止会话：运行中的挂断远端 shell，由 finish 收尾并清出清单；已退出的直接清出。
func (s *localSession) kill() {
	s.mu.Lock()
	s.killed = true
	exited := s.state == termproto.SessionExited
	s.mu.Unlock()
	if exited {
		s.lh.remove(s)
		return
	}
	s.shell.Kill()
}

// maintain 执行空闲回收（没人附着、没有输入、超过空闲时限）与已退出会话的过期清理。
func (s *localSession) maintain(now time.Time) {
	s.mu.Lock()
	removed, state := s.removed, s.state
	idle := s.effectiveIdleLocked()
	quiet := len(s.atts) == 0
	last, exitedAt := s.lastActivityLocked(), s.exitedAt
	s.mu.Unlock()
	switch {
	case removed || !quiet:
	case state == termproto.SessionRunning && idle > 0 && now.Sub(last) >= idle:
		s.kill()
	case state == termproto.SessionExited && now.Sub(exitedAt) >= localExitedKeep:
		s.lh.remove(s)
	}
}

// teardown 结束会话的后台协程并关闭 SSH 连接（会话已从清单清出之后调用，幂等）。
// 不处理浏览器附着：它们要么已被 finish 优雅通知，要么由引擎关闭时统一断开。
func (s *localSession) teardown() {
	s.stopOnce.Do(func() {
		s.mu.Lock()
		s.removed = true
		s.mu.Unlock()
		close(s.stop)
		s.shell.Kill()
		_ = s.shell.Close()
	})
}
