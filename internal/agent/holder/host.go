package holder

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/teacat99/RelayMesh/v2/internal/agent/record"
	"github.com/teacat99/RelayMesh/v2/internal/termproto"
)

const (
	// handshakeTimeout 是客户端连上之后发出 ipc_open 的期限。
	handshakeTimeout = 5 * time.Second
	// adminIdleTimeout 是管理连接的最长存活时间（命令行用，一次性）。
	adminIdleTimeout = time.Minute
	// byeTimeout 是发送告别帧的最长等待。
	byeTimeout = 2 * time.Second
	// shutdownGrace 是终止全部会话时等待它们退出的最长时间。
	shutdownGrace = 3 * time.Second
)

var (
	// ErrHostClosed 表示 Host 已关闭，不再接受连接。
	ErrHostClosed = errors.New("holder host is closed")
	errBadOpen    = errors.New("holder ipc: bad open frame")
)

// HostOptions 是 Host 的构造参数。
type HostOptions struct {
	Manager ManagerOptions
	// Version 是守护进程（内嵌模式下是 Agent）自己的版本，经 ipc_hello 报告。
	Version string
	// Rev 是守护进程二进制的「Agent 侧代码指纹」（见 termproto.ValidAgentRev），经 ipc_hello 报告；空表示非发布构建。
	Rev string
	// Persistent 为 true 表示这是外置守护进程：会话能挺过 Agent 的重启。
	Persistent bool
	// StateDir 非空时把会话索引与策略持久化到该目录（仅外置守护进程使用）。
	StateDir string
	// Record 非 nil 时启用终端记录（终端上显示什么就记录什么，只写在本机）。Dir 必填。
	Record *record.Options
	// Exec 非 nil 时启用模型执行（非交互命令运行）。其中 DenyExec 是本机一票否决。
	Exec *ExecOptions
	Logf func(string, ...any)
	Now  func() time.Time
}

// Host 是「会话守护」的核心：持有 Manager，接受控制端连接，并（外置模式下）把状态落盘。
//
// 外置守护进程与 Agent 内嵌模式共用它：区别只在于连接的来源（本地套接字或进程内管道）
// 与是否持久化。因此两种模式走的是同一条代码路径，内嵌模式的测试同时也在验证外置模式。
type Host struct {
	mgr        *Manager
	rec        *record.Store
	exec       *ExecManager
	version    string
	rev        string
	persistent bool
	logf       func(string, ...any)
	now        func() time.Time

	bootID   string
	bootTime int64
	started  time.Time
	state    *stateStore

	mu       sync.Mutex
	active   *ctrl
	lost     []Lost
	policy   *termproto.Policy
	closed   bool
	draining bool

	stop     chan struct{}
	shutdown chan string

	// 状态落盘：saveMu 串行化所有写入，保证 Close 写下的最终状态（clean 与否）不会被随后到来的周期 / 事件保存覆盖。
	saveMu       sync.Mutex
	lastSave     time.Time
	savedRunning int // 最近一次落盘时的运行中会话数：会话变多（新开）时要立刻保存
	finalized    bool
	saveQueued   atomic.Bool
}

// NewHost 创建 Host。外置模式下会读取上一次的状态，据此算出「已丢失的会话」并立即写入新的状态。
func NewHost(opts HostOptions) (*Host, error) {
	if opts.Logf == nil {
		opts.Logf = func(string, ...any) {}
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.Manager.Logf == nil {
		opts.Manager.Logf = opts.Logf
	}
	if opts.Manager.Now == nil {
		opts.Manager.Now = opts.Now
	}
	var rec *record.Store
	if opts.Record != nil {
		ro := *opts.Record
		ro.DeferRecover = true
		if ro.Logf == nil {
			ro.Logf = opts.Logf
		}
		if ro.Now == nil {
			ro.Now = opts.Now
		}
		var err error
		if rec, err = record.Open(ro); err != nil {
			// 记录不可用不应让终端不可用：照常提供会话，并在会话信息里如实标注录制出错。
			opts.Logf("终端记录不可用，会话将不被录制: %v", err)
			opts.Manager.RecordFailed = true
		}
	}
	opts.Manager.Record = rec
	h := &Host{
		rec:        rec,
		mgr:        NewManager(opts.Manager),
		version:    opts.Version,
		rev:        opts.Rev,
		persistent: opts.Persistent,
		logf:       opts.Logf,
		now:        opts.Now,
		started:    opts.Now(),
		stop:       make(chan struct{}),
		shutdown:   make(chan string, 1),
	}
	h.bootID, h.bootTime = bootIdentity()
	if opts.Exec != nil {
		h.exec = NewExecManager(*opts.Exec, opts.Logf, opts.Now)
	}
	// 上次没来得及收尾的运行为什么不存在了。内嵌模式没有持久化的状态，唯一的可能是上一个 Agent 进程退出了。
	orphanWhy := "agent_exit"

	if opts.Persistent && opts.StateDir != "" {
		h.state = newStateStore(opts.StateDir)
		old, err := h.state.load()
		if err != nil {
			return nil, err
		}
		orphanWhy = LostHolderExit
		if old != nil {
			orphanWhy = lostReason(old, h.bootID, h.bootTime)
		}
		h.lost = computeLost(old, h.bootID, h.bootTime, h.now())
		if old != nil && old.Policy != nil {
			p := *old.Policy
			h.policy = &p
			h.mgr.ApplyPolicy(p)
		}
		for _, l := range h.lost {
			h.logf("上次遗留的会话 %s（%s）已不存在：%s", shortSID(l.SID), l.Name, DescribeLost(l.Reason))
		}
		h.mgr.SetObserver(h.scheduleSave)
		h.saveNow(false)
	}
	if h.exec != nil {
		h.exec.RecoverOrphans(orphanWhy)
	}
	if rec != nil {
		// 上次没来得及收尾的记录：用丢失原因（主机重启 / 守护进程被停止 / 守护进程崩溃）给它们补上结束信息。
		reasons := make(map[string]string, len(h.lost))
		for _, l := range h.lost {
			reasons[l.SID] = string(l.Reason)
		}
		rec.Recover(func(sid string) string { return reasons[sid] })
	}
	return h, nil
}

// Records 返回终端记录存储（未启用记录时为 nil）。
func (h *Host) Records() *record.Store { return h.rec }

func shortSID(sid string) string {
	if len(sid) > 8 {
		return sid[:8]
	}
	return sid
}

// Manager 返回会话管理器（测试与内嵌模式观察用）。
func (h *Host) Manager() *Manager { return h.mgr }

// Persistent 报告会话是否能挺过 Agent 的重启。
func (h *Host) Persistent() bool { return h.persistent }

// Start 启动周期任务（会话停滞检查与过期回收、状态刷新）。
func (h *Host) Start() {
	h.mgr.Start()
	if h.exec != nil {
		h.exec.Start()
	}
	if h.state != nil {
		go h.refreshLoop()
	}
}

func (h *Host) refreshLoop() {
	t := time.NewTicker(stateRefreshInterval)
	defer t.Stop()
	for {
		select {
		case <-h.stop:
			return
		case <-t.C:
			h.saveNow(false)
		}
	}
}

// Hello 返回当前的 ipc_hello 内容。
func (h *Host) Hello() IPCHello {
	h.mu.Lock()
	lost := append([]Lost(nil), h.lost...)
	h.mu.Unlock()
	hello := IPCHello{
		T:          TIPCHello,
		V:          IPCVersion,
		Holder:     h.version,
		Rev:        h.rev,
		BootID:     h.bootID,
		PID:        os.Getpid(),
		Started:    h.started.Unix(),
		Persistent: h.persistent,
		Caps:       DefaultCaps(),
		Lost:       lost,
	}
	if h.exec != nil {
		// 被本机否决时也声明 exec 能力：Hub 发来 run 能得到明确的 denied_by_host，而不是石沉大海。
		if h.exec.Available() || h.exec.Denied() {
			hello.Caps = append(hello.Caps, CapExec)
		}
		hello.DenyExec = h.exec.Denied()
		hello.Exec = h.exec.Info()
	}
	if h.mgr.SSHEnabled() {
		hello.Caps = append(hello.Caps, CapSSH)
		if h.mgr.SSHStoreReady() {
			hello.Caps = append(hello.Caps, CapSSHStore)
		}
	}
	return hello
}

// Exec 返回模型执行管理器（未启用时为 nil）。
func (h *Host) Exec() *ExecManager { return h.exec }

// RunningWork 返回仍在运行的交互会话数与模型命令数。
// 两者分属 Manager 与 ExecManager：模型命令不在会话表里，所以「还有没有人在用这个守护进程」必须两边都看——
// 排空式关停要等这两类都归零，只数会话会在命令还在跑时就把它们终止（holder_stopped）。
func (h *Host) RunningWork() (sessions, runs int) {
	sessions = h.mgr.RunningCount()
	if h.exec != nil {
		runs = h.exec.ActiveCount()
	}
	return sessions, runs
}

// ApplyPolicy 应用并（外置模式下）持久化策略：Hub 离线期间守护进程仍按这份策略计时与回收。
func (h *Host) ApplyPolicy(p termproto.Policy) {
	h.mu.Lock()
	cp := p
	h.policy = &cp
	h.mu.Unlock()
	h.mgr.ApplyPolicy(p)
	h.scheduleSave()
}

// RequestShutdown 请求关停（仅外置守护进程响应）。立即关停由运行循环执行；排空则先停止接受新会话。
func (h *Host) RequestShutdown(mode string) {
	if !h.persistent {
		return
	}
	switch mode {
	case ShutdownNow:
	case ShutdownDrain:
		h.mu.Lock()
		h.draining = true
		h.mu.Unlock()
		h.mgr.SetDraining()
		if h.exec != nil {
			h.exec.SetDraining()
		}
	default:
		return
	}
	select {
	case h.shutdown <- mode:
	default:
		// 已经有一个待处理的请求；立即关停应当能「升级」排空，所以把旧的挤掉。
		if mode == ShutdownNow {
			select {
			case <-h.shutdown:
			default:
			}
			select {
			case h.shutdown <- mode:
			default:
			}
		}
	}
}

// ShutdownRequests 返回关停请求通道（运行循环消费）。
func (h *Host) ShutdownRequests() <-chan string { return h.shutdown }

// Draining 报告是否处于排空状态。
func (h *Host) Draining() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.draining
}

// Close 终止全部会话并释放资源。clean 表示这是有意的停止：先把会话索引连同 clean 标记落盘（再终止），
// 下次启动时才能告诉用户「守护进程被停止」而不是「意外退出」。幂等。
func (h *Host) Close(clean bool) {
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return
	}
	h.closed = true
	active := h.active
	h.mu.Unlock()

	close(h.stop)
	h.finalSave(clean)
	if active != nil {
		active.bye(ByeShutdown)
	}
	execDone := make(chan struct{})
	go func() {
		defer close(execDone)
		if h.exec != nil {
			h.exec.Shutdown(shutdownGrace)
		}
	}()
	h.mgr.Shutdown(shutdownGrace)
	<-execDone
	if h.rec != nil {
		h.rec.Close()
	}
}

// ---------- 状态持久化 ----------

func (h *Host) snapshotState(clean bool) *persistedState {
	h.mu.Lock()
	st := &persistedState{
		BootID:   h.bootID,
		BootTime: h.bootTime,
		PID:      os.Getpid(),
		Started:  h.started.Unix(),
		Clean:    clean,
		Lost:     append([]Lost(nil), h.lost...),
		Sessions: []persistedSession{},
	}
	if h.policy != nil {
		p := *h.policy
		st.Policy = &p
	}
	h.mu.Unlock()
	for _, info := range h.mgr.Snapshot() {
		if info.State != termproto.SessionRunning {
			continue
		}
		st.Sessions = append(st.Sessions, persistedSession{
			SID:     info.SID,
			Name:    info.Name,
			Shell:   info.Shell,
			PID:     info.PID,
			Created: info.Created,
			Cwd:     processCwd(info.PID),
		})
	}
	return st
}

// saveNow 保存当前状态；Close 写下最终状态之后不再生效。
func (h *Host) saveNow(clean bool) {
	if h.state == nil {
		return
	}
	h.saveMu.Lock()
	defer h.saveMu.Unlock()
	if h.finalized {
		return
	}
	h.lastSave = time.Now()
	st := h.snapshotState(clean)
	h.savedRunning = len(st.Sessions)
	if err := h.state.save(st); err != nil {
		h.logf("保存守护进程状态失败: %v", err)
	}
}

// finalSave 写下最终状态并封口：之后的周期 / 事件保存都不会再覆盖它。
func (h *Host) finalSave(clean bool) {
	if h.state == nil {
		return
	}
	h.saveMu.Lock()
	defer h.saveMu.Unlock()
	h.finalized = true
	if err := h.state.save(h.snapshotState(clean)); err != nil {
		h.logf("保存守护进程状态失败: %v", err)
	}
}

// scheduleSave 在会话集合变化时落盘：
//   - 新开了会话（运行中的数量比上次落盘时多）或长时间没存过：立即保存。崩溃时「有哪些会话」要能如实报告，
//     而新开会话是低频事件，立即写一个很小的文件的代价可以忽略；
//   - 其余变化（会话结束、改名……）合并成一次后沿保存，避免高频变化时反复写盘。
func (h *Host) scheduleSave() {
	if h.state == nil {
		return
	}
	running := h.mgr.RunningCount()
	h.saveMu.Lock()
	since := time.Since(h.lastSave)
	if running > h.savedRunning || h.lastSave.IsZero() || since >= stateSaveDebounce {
		h.lastSave = time.Now()
		h.saveMu.Unlock()
		go h.saveNow(false) // 观察者回调在 Manager 的锁之外被调用，但保存要读 Manager 快照：异步更稳妥
		return
	}
	h.saveMu.Unlock()
	if !h.saveQueued.CompareAndSwap(false, true) {
		return
	}
	time.AfterFunc(stateSaveDebounce-since, func() {
		h.saveQueued.Store(false)
		select {
		case <-h.stop:
			return
		default:
		}
		h.saveNow(false)
	})
}

// ---------- 连接服务 ----------

// Serve 在一条新连接上完成握手并服务它，直到连接结束。控制端：同一时刻只有一个，新来的取代旧的；
// 管理端：只能查询与请求关停。
func (h *Host) Serve(ctx context.Context, conn Conn) error {
	defer conn.Close()

	hctx, cancel := context.WithTimeout(ctx, handshakeTimeout)
	typ, data, err := conn.Read(hctx)
	cancel()
	if err != nil {
		return err
	}
	var open IPCOpen
	if typ != MsgText || json.Unmarshal(data, &open) != nil || open.T != TIPCOpen {
		sendBye(conn, ByeBadRequest)
		return errBadOpen
	}
	if open.V != IPCVersion {
		sendBye(conn, ByeVersion)
		return errBadOpen
	}
	h.mu.Lock()
	closed := h.closed
	h.mu.Unlock()
	if closed {
		sendBye(conn, ByeShutdown)
		return ErrHostClosed
	}

	switch open.Role {
	case RoleController:
		return h.serveController(ctx, conn)
	case RoleAdmin:
		return h.serveAdmin(ctx, conn)
	}
	sendBye(conn, ByeBadRequest)
	return errBadOpen
}

func (h *Host) sendHello(ctx context.Context, conn Conn) error {
	raw, err := json.Marshal(h.Hello())
	if err != nil {
		return err
	}
	wctx, cancel := context.WithTimeout(ctx, writeTimeout)
	defer cancel()
	return conn.Write(wctx, MsgText, raw)
}

func (h *Host) serveController(ctx context.Context, conn Conn) error {
	cctx, cancel := context.WithCancel(ctx)
	defer cancel()

	if err := h.sendHello(cctx, conn); err != nil {
		return err
	}
	c := newCtrl(cctx, h, conn)

	h.mu.Lock()
	old := h.active
	h.active = c
	h.mu.Unlock()
	if old != nil {
		h.logf("新的控制端取代了旧的控制端")
		old.supersede()
	}
	h.mgr.SetNotifier(c.requestSessions)
	if h.exec != nil {
		h.exec.SetSink(c)
	}
	defer func() {
		h.mu.Lock()
		if h.active == c {
			h.active = nil
			h.mgr.SetNotifier(nil)
			if h.exec != nil {
				h.exec.SetSink(nil)
			}
		}
		h.mu.Unlock()
		c.closeChannels()
	}()

	c.sendSessions()
	if h.exec != nil {
		h.exec.Sync() // 控制端（重新）接上：对账运行清单，并补报断开期间产生的、尚未被确认的结束事件
	}
	go c.pingLoop()
	return c.serve()
}

func (h *Host) serveAdmin(ctx context.Context, conn Conn) error {
	actx, cancel := context.WithTimeout(ctx, adminIdleTimeout)
	defer cancel()
	if err := h.sendHello(actx, conn); err != nil {
		return err
	}
	for {
		typ, data, err := conn.Read(actx)
		if err != nil {
			return err
		}
		if typ != MsgText {
			continue
		}
		t, err := termproto.ParseType(data)
		if err != nil {
			continue
		}
		switch t {
		case TIPCStatus:
			h.mu.Lock()
			hasCtrl := h.active != nil
			draining := h.draining
			h.mu.Unlock()
			_, runs := h.RunningWork()
			raw, err := json.Marshal(IPCStatusReply{
				T:          TIPCStatusReply,
				Hello:      h.Hello(),
				Sessions:   h.mgr.Snapshot(),
				Controller: hasCtrl,
				Draining:   draining,
				Runs:       &runs,
			})
			if err != nil {
				return err
			}
			wctx, wcancel := context.WithTimeout(actx, writeTimeout)
			err = conn.Write(wctx, MsgText, raw)
			wcancel()
			if err != nil {
				return err
			}
		case TIPCShutdown:
			var m IPCShutdown
			if json.Unmarshal(data, &m) == nil {
				h.RequestShutdown(m.Mode)
			}
			sendBye(conn, ByeShutdown)
			return nil
		}
	}
}

func sendBye(conn Conn, reason string) {
	raw, err := json.Marshal(IPCBye{T: TIPCBye, Reason: reason})
	if err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), byeTimeout)
	defer cancel()
	_ = conn.Write(ctx, MsgText, raw)
}

// bye 向控制端告别并关闭连接（守护进程关停时用）。
func (l *ctrl) bye(reason string) {
	l.closeChannels()
	sendBye(l.conn, reason)
	_ = l.conn.Close()
}

// supersede 在新的控制端到来时结束本连接：先静默清场通道（不发 detached，新控制端会重新对账），再告知原因。
func (l *ctrl) supersede() { l.bye(ByeSuperseded) }
