package holder

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/teacat99/RelayMesh/v2/internal/agent/runlog"
	"github.com/teacat99/RelayMesh/v2/internal/termproto"
)

// 模型执行（非交互命令运行）的实现。
//
// 与交互终端不同，运行没有 PTY：stdin 是空的，stdout 与 stderr 分开收集；输出只落在本机磁盘
// （runlog：每路「开头 + 结尾」），Hub 不保存全文。进程由守护进程持有，所以 Agent 升级 / 重启不会打断正在跑的命令。
const (
	// defaultMaxActiveRuns 是同时运行的命令数的保护性上限，只为防止失控的调用把主机的进程表耗尽。
	// 它不是策略，正常使用碰不到；碰到时返回 busy。
	defaultMaxActiveRuns = 64
	// defaultRunKillGrace 是 SIGTERM 之后等待进程自行收尾、再强制终止的宽限。
	defaultRunKillGrace = 3 * time.Second
	// runWaitDelay 是进程退出后等待输出管道排空的上限：后台子进程一直占着管道时不会把 Wait 卡死。
	runWaitDelay = 2 * time.Second
	// runTailFlush 是把运行中的结尾环写入磁盘的间隔：守护进程崩溃时磁盘上的结尾最多落后这么久。
	runTailFlush = time.Second
	// runSweepInterval 是清理过期输出的间隔。
	runSweepInterval = 10 * time.Minute
	// maxRunReads 是同时服务的 run_read 请求数。
	maxRunReads = 4
	// maxSyncEnds 是一次对账最多补报的结束事件数。
	maxSyncEnds = 128

	// 实时输出流（仅供人观看，随时可以丢弃——完整输出在磁盘上）。
	runLiveDelay = 120 * time.Millisecond // 合并这么久内到达的输出再发
	runLiveBatch = 16 << 10               // 积累到这么多立即发
	runLiveRate  = 256 << 10              // 每个运行每秒最多向外发的字节数
	runLiveBurst = 512 << 10
	runLiveQueue = 256 // 全局待发实时帧队列；满了就丢，绝不阻塞进程
	// metaCmdMax 是写进本机元信息的命令文本上限（完整命令由 Hub 保存）。
	metaCmdMax = 4 << 10
)

var (
	errBadShell       = errors.New("unsupported shell")
	errNoShell        = errors.New("no usable shell")
	errCommandTooLong = errors.New("command line too long for this platform")
)

func itoa(n int) string { return strconv.Itoa(n) }

// execSpec 描述一次运行使用的 shell 与环境。
type execSpec struct {
	Path    string
	Name    string                            // 展示名：bash / sh / zsh / pwsh / powershell / cmd
	Args    func(command string) []string     // 传给 shell 的参数（Windows 的 cmd 用 CmdLine 代替）
	CmdLine func(path, command string) string // 仅 Windows：整条命令行
	Env     []string
	Home    string
	Sep     string
}

// ExecOptions 是运行管理器的构造参数。
type ExecOptions struct {
	// Dir 是运行输出的保存目录；为空表示本机无法保存输出，运行一律失败（exec_unavailable）。
	Dir string
	// DenyExec 是「本机一票否决」：为 true 时所有 run 请求都以 denied_by_host 失败。
	DenyExec bool
	// MaxActive / KillGrace / Retention / Quota / HeadBytes / TailBytes 取默认值时填 0。
	MaxActive int
	KillGrace time.Duration
	Retention time.Duration
	Quota     int64
	HeadBytes int
	TailBytes int
	// Harden 收紧目录权限（由 Agent 提供）。
	Harden func(dir string) error
	// Resolve 解析 shell；为空时使用平台默认实现（测试里可替换）。
	Resolve func(name string) (execSpec, error)
}

// runSink 是运行事件的去向：当前接着的控制端。
type runSink interface {
	sendJSON(v any) error
	sendBinary(data []byte) error
}

// ExecManager 管理一台主机上正在进行的运行。
type ExecManager struct {
	o       ExecOptions
	store   *runlog.Store
	logf    func(string, ...any)
	now     func() time.Time
	resolve func(string) (execSpec, error)
	info    *termproto.ExecInfo

	mu       sync.Mutex
	active   map[string]*activeRun
	sink     runSink
	draining bool
	closed   bool

	reads chan struct{}
	liveQ chan []byte
	stop  chan struct{}
	done  chan struct{}

	started     atomic.Bool
	liveDropped atomic.Int64
}

// NewExecManager 创建运行管理器。存储目录打不开时记录日志并让运行以 exec_unavailable 失败，而不是让整个守护进程起不来。
func NewExecManager(o ExecOptions, logf func(string, ...any), now func() time.Time) *ExecManager {
	if logf == nil {
		logf = func(string, ...any) {}
	}
	if now == nil {
		now = time.Now
	}
	if o.MaxActive <= 0 {
		o.MaxActive = defaultMaxActiveRuns
	}
	if o.KillGrace <= 0 {
		o.KillGrace = defaultRunKillGrace
	}
	m := &ExecManager{
		o:       o,
		logf:    logf,
		now:     now,
		resolve: o.Resolve,
		active:  make(map[string]*activeRun),
		reads:   make(chan struct{}, maxRunReads),
		liveQ:   make(chan []byte, runLiveQueue),
		stop:    make(chan struct{}),
		done:    make(chan struct{}),
	}
	if m.resolve == nil {
		m.resolve = resolveExecShell
	}
	if o.Dir != "" {
		st, err := runlog.Open(runlog.Options{
			Dir: o.Dir, HeadBytes: o.HeadBytes, TailBytes: o.TailBytes, Retention: o.Retention, Quota: o.Quota,
			Now: now, Logf: logf, Harden: o.Harden,
		})
		if err != nil {
			logf("模型执行的输出目录不可用，本机将无法执行模型命令: %v", err)
		} else {
			m.store = st
		}
	}
	if spec, err := m.resolve(""); err == nil {
		m.info = &termproto.ExecInfo{Shell: spec.Name, Home: spec.Home, Sep: spec.Sep}
	} else {
		logf("找不到可用于模型执行的 shell: %v", err)
	}
	return m
}

// Denied 报告本机是否一票否决了模型执行。
func (m *ExecManager) Denied() bool { return m.o.DenyExec }

// Available 报告本机能否执行模型命令（有输出目录与 shell；不含一票否决）。
func (m *ExecManager) Available() bool { return m.store != nil && m.info != nil }

// Info 返回运行命令时的 shell / 目录约定（找不到 shell 时为 nil）。
func (m *ExecManager) Info() *termproto.ExecInfo {
	if m.info == nil {
		return nil
	}
	cp := *m.info
	return &cp
}

// Store 返回输出存储（可能为 nil）。
func (m *ExecManager) Store() *runlog.Store { return m.store }

// SetSink 设置运行事件的去向；nil 表示当前没有控制端。
func (m *ExecManager) SetSink(s runSink) {
	m.mu.Lock()
	m.sink = s
	m.mu.Unlock()
}

// Start 启动后台任务：定期写出运行中的结尾、清理过期输出、转发实时输出。
func (m *ExecManager) Start() {
	if !m.started.CompareAndSwap(false, true) {
		return
	}
	go m.loop()
	go m.liveLoop()
}

func (m *ExecManager) loop() {
	defer close(m.done)
	flush := time.NewTicker(runTailFlush)
	defer flush.Stop()
	sweep := time.NewTicker(runSweepInterval)
	defer sweep.Stop()
	if m.store != nil {
		if n := m.store.Sweep(); n > 0 {
			m.logf("已清理 %d 个过期的运行输出", n)
		}
	}
	for {
		select {
		case <-m.stop:
			return
		case <-flush.C:
			m.mu.Lock()
			list := make([]*activeRun, 0, len(m.active))
			for _, ar := range m.active {
				list = append(list, ar)
			}
			m.mu.Unlock()
			for _, ar := range list {
				if w := ar.writer(); w != nil {
					w.FlushTails()
				}
			}
		case <-sweep.C:
			if m.store != nil {
				if n := m.store.Sweep(); n > 0 {
					m.logf("已清理 %d 个过期的运行输出", n)
				}
			}
		}
	}
}

func (m *ExecManager) liveLoop() {
	for {
		select {
		case <-m.stop:
			return
		case f := <-m.liveQ:
			m.mu.Lock()
			s := m.sink
			m.mu.Unlock()
			if s != nil {
				_ = s.sendBinary(f)
			}
		}
	}
}

// ActiveCount 返回正在运行的命令数。
func (m *ExecManager) ActiveCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.active)
}

// RecoverOrphans 把上次没来得及收尾的运行标记为 lost（reason 说明为什么）。
func (m *ExecManager) RecoverOrphans(reason string) {
	if m.store == nil {
		return
	}
	for _, meta := range m.store.RecoverOrphans(reason) {
		m.logf("上次遗留的运行 %s 已不存在：%s", meta.ID[:8], reason)
	}
}

func (m *ExecManager) sendJSON(v any) {
	m.mu.Lock()
	s := m.sink
	m.mu.Unlock()
	if s != nil {
		_ = s.sendJSON(v)
	}
}

func (m *ExecManager) enqueueLive(frame []byte) {
	select {
	case m.liveQ <- frame:
	default:
		m.liveDropped.Add(int64(len(frame)))
	}
}

// ---------- 启动 ----------

func (m *ExecManager) failed(id, reason, detail string) *termproto.RunFailed {
	f := &termproto.RunFailed{T: termproto.TRunFailed, ID: id, Reason: reason, Detail: detail}
	m.sendJSON(*f)
	return f
}

// StartRun 处理一个 run 请求：成功时发出 run_started，失败时发出 run_failed（并返回它，便于测试）。
func (m *ExecManager) StartRun(req termproto.Run) *termproto.RunFailed {
	if !termproto.ValidRunID(req.ID) {
		return m.failed(req.ID, termproto.RunFailBad, "invalid run id")
	}
	if m.o.DenyExec {
		return m.failed(req.ID, termproto.RunFailDenied, "本机用 --deny-model-exec 禁止了模型执行")
	}
	if m.store == nil {
		return m.failed(req.ID, termproto.RunFailUnavailable, "本机没有可用于保存运行输出的目录")
	}
	cmd := req.Cmd
	switch {
	case strings.TrimSpace(cmd) == "":
		return m.failed(req.ID, termproto.RunFailBad, "empty command")
	case len(cmd) > termproto.MaxRunCommandBytes:
		return m.failed(req.ID, termproto.RunFailBad, "command too long")
	case strings.ContainsRune(cmd, 0) || !utf8.ValidString(cmd):
		return m.failed(req.ID, termproto.RunFailBad, "command is not valid text")
	}
	timeout := time.Duration(req.TimeoutSec) * time.Second
	if req.TimeoutSec <= 0 {
		timeout = termproto.DefaultRunTimeoutSec * time.Second
	}
	if req.TimeoutSec > termproto.MaxRunTimeoutSec {
		timeout = termproto.MaxRunTimeoutSec * time.Second
	}

	spec, err := m.resolve(req.Shell)
	switch {
	case errors.Is(err, errBadShell):
		return m.failed(req.ID, termproto.RunFailBad, "unsupported shell")
	case err != nil:
		return m.failed(req.ID, termproto.RunFailNoShell, termproto.SanitizeText(err.Error(), 200))
	}
	if err := checkCommandLength(spec, cmd); err != nil {
		return m.failed(req.ID, termproto.RunFailBad, err.Error())
	}
	cwd, ok := runDir(req.Cwd, spec)
	if !ok {
		return m.failed(req.ID, termproto.RunFailCwd, "工作目录不存在、不是目录或不是绝对路径")
	}

	ar := &activeRun{m: m, id: req.ID}
	// 初始化期间持有 ar.kmu：同时到来的取消 / 对账 / 刷新会等到进程信息就绪，而不是看到半成品。
	// 加锁顺序：ar.kmu → m.mu（任何地方都不在持有 m.mu 时去拿 ar.kmu）。
	ar.kmu.Lock()
	m.mu.Lock()
	switch {
	case m.closed, m.draining:
		m.mu.Unlock()
		ar.kmu.Unlock()
		return m.failed(req.ID, termproto.RunFailDraining, "")
	case len(m.active) >= m.o.MaxActive:
		m.mu.Unlock()
		ar.kmu.Unlock()
		return m.failed(req.ID, termproto.RunFailBusy, "本机同时运行的命令数已达保护上限")
	}
	if _, dup := m.active[req.ID]; dup {
		m.mu.Unlock()
		ar.kmu.Unlock()
		return m.failed(req.ID, termproto.RunFailDuplicate, "")
	}
	m.active[req.ID] = ar
	m.mu.Unlock()
	abort := func(reason, detail string) *termproto.RunFailed {
		m.mu.Lock()
		delete(m.active, req.ID)
		m.mu.Unlock()
		ar.kmu.Unlock()
		return m.failed(req.ID, reason, detail)
	}

	w, err := m.store.Begin(runlog.Meta{ID: req.ID, Cmd: clipText(cmd, metaCmdMax), Shell: spec.Name, Cwd: cwd})
	if err != nil {
		if errors.Is(err, runlog.ErrExists) {
			return abort(termproto.RunFailDuplicate, "")
		}
		return abort(termproto.RunFailSpawn, "无法创建输出文件: "+termproto.SanitizeText(err.Error(), 160))
	}
	ar.w = w
	ar.live = &liveEmitter{m: m, id: req.ID, tokens: runLiveBurst, last: time.Now()}

	var args []string
	if spec.Args != nil {
		args = spec.Args(cmd)
	}
	c := exec.Command(spec.Path, args...)
	c.Dir = cwd
	c.Env = spec.Env
	c.Stdout = &runWriter{ar: ar, stream: termproto.StreamStdout}
	c.Stderr = &runWriter{ar: ar, stream: termproto.StreamStderr}
	c.WaitDelay = runWaitDelay
	prepareRunCmd(c, spec, cmd)
	startedAt := m.now()
	if err := c.Start(); err != nil {
		w.Discard()
		return abort(termproto.RunFailSpawn, termproto.SanitizeText(err.Error(), 200))
	}
	ar.cmd, ar.pid, ar.startedMS = c, c.Process.Pid, startedAt.UnixMilli()
	w.SetStarted(ar.pid, spec.Name, cwd, startedAt)
	ar.timer = time.AfterFunc(timeout, func() { ar.terminate(killTimeout, "timeout") })
	pid := ar.pid
	ar.kmu.Unlock()

	m.sendJSON(termproto.RunStarted{
		T: termproto.TRunStarted, ID: req.ID, PID: pid, Shell: spec.Name, Cwd: cwd, Started: startedAt.UnixMilli(),
	})
	go ar.wait()
	return nil
}

// runDir 解析运行的工作目录：空表示账户家目录（不可用时回落到根目录 / 当前目录）；
// 否则必须是主机上存在的绝对目录。
func runDir(cwd string, spec execSpec) (string, bool) {
	if cwd == "" {
		if d := usableDir(spec.Home); d != "" {
			return d, true
		}
		if spec.Sep == "/" {
			return "/", true
		}
		return "", true
	}
	if len([]rune(cwd)) > termproto.MaxRunCwdRunes {
		return "", false
	}
	if d := usableDir(cwd); d != "" {
		return d, true
	}
	return "", false
}

func clipText(s string, n int) string {
	if len(s) <= n {
		return s
	}
	cut := n
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "…(已截断)"
}

// ---------- 单次运行 ----------

const (
	killTimeout  = "timeout"
	killCancel   = "cancel"
	killShutdown = "shutdown"
)

// activeRun 的字段分两类：初始化后不再变化的（m / id）可以直接读；
// 由 StartRun 在持有 kmu 期间才填好的（w / live / cmd / pid / startedMS），其他协程要通过 kmu 读取。
type activeRun struct {
	m  *ExecManager
	id string

	kmu       sync.Mutex
	w         *runlog.Writer
	live      *liveEmitter
	cmd       *exec.Cmd
	pid       int
	startedMS int64
	timer     *time.Timer
	kind      string
	reason    string
	grace     *time.Timer
	finished  bool
}

// writer 返回输出写入器（运行尚在初始化时会等到它就绪）。
func (ar *activeRun) writer() *runlog.Writer {
	ar.kmu.Lock()
	defer ar.kmu.Unlock()
	return ar.w
}

// brief 返回运行清单里的一项。
func (ar *activeRun) brief() termproto.RunBrief {
	ar.kmu.Lock()
	defer ar.kmu.Unlock()
	return termproto.RunBrief{ID: ar.id, PID: ar.pid, Started: ar.startedMS}
}

// terminate 终止整个进程组：先温和地请求，宽限后强制。重复调用只有第一次生效。
func (ar *activeRun) terminate(kind, reason string) {
	ar.kmu.Lock()
	if ar.kind != "" || ar.finished || ar.pid <= 0 {
		ar.kmu.Unlock()
		return
	}
	ar.kind, ar.reason = kind, reason
	pid := ar.pid
	ar.grace = time.AfterFunc(ar.m.o.KillGrace, func() { terminateTree(pid, true) })
	ar.kmu.Unlock()
	terminateTree(pid, false)
}

func (ar *activeRun) wait() {
	err := ar.cmd.Wait()
	ended := ar.m.now()

	ar.kmu.Lock()
	ar.finished = true
	if ar.grace != nil {
		ar.grace.Stop()
	}
	kind, reason, pid := ar.kind, ar.reason, ar.pid
	ar.kmu.Unlock()
	ar.timer.Stop()
	if kind != "" {
		// 被取消 / 超时的运行：确保整个进程组都不再留存（leader 已被回收，组里还有成员时 pgid 不会被复用）。
		terminateTree(pid, true)
	}

	state, code, sig, why := classifyExit(ar.cmd.ProcessState, err, kind, reason)
	res := ar.w.Finish(runlog.Finish{State: state, Code: code, Signal: sig, Reason: why, Ended: ended})
	ar.live.close()
	ar.m.finish(ar, res.Meta)
}

// classifyExit 把进程的结局归为运行状态。取消 / 超时以我们自己的判定为准（进程是被我们终止的）。
func classifyExit(ps *os.ProcessState, waitErr error, kind, reason string) (state string, code *int, signal, why string) {
	var sigName string
	exitCode := -1
	signaled := false
	if ps != nil {
		exitCode, sigName, signaled = exitDetail(ps)
	}
	switch kind {
	case killTimeout:
		return termproto.RunTimeout, nil, sigName, "timeout"
	case killCancel:
		return termproto.RunCancelled, nil, sigName, reason
	case killShutdown:
		return termproto.RunCancelled, nil, sigName, reason
	}
	switch {
	case ps == nil:
		msg := "wait_failed"
		if waitErr != nil {
			msg = "wait_failed: " + termproto.SanitizeText(waitErr.Error(), 120)
		}
		return termproto.RunLost, nil, "", msg
	case signaled:
		return termproto.RunSignaled, nil, sigName, ""
	}
	return termproto.RunExited, &exitCode, "", ""
}

// finish 在运行结束、输出落盘之后调用：移出活动集合并上报结束事件（Hub 确认之前会在重连时补报）。
func (m *ExecManager) finish(ar *activeRun, meta runlog.Meta) {
	m.mu.Lock()
	delete(m.active, ar.id)
	m.mu.Unlock()
	m.sendJSON(endFrame(meta))
}

// endFrame 由落盘的元信息构造 run_end；首次上报与补报用同一份数据，所以内容一致。
func endFrame(m runlog.Meta) termproto.RunEnd {
	return termproto.RunEnd{
		T: termproto.TRunEnd, ID: m.ID, State: m.State, Code: m.Code, Signal: m.Signal, DurMS: m.DurMS,
		OutBytes: m.Out.Total, ErrBytes: m.Err.Total, OutDropped: m.Out.Dropped(), ErrDropped: m.Err.Dropped(),
		Out: m.InlineOut, Err: m.InlineErr, PrevHead: m.PrevHead, PrevTail: m.PrevTail, Reason: m.Reason,
	}
}

// runWriter 把进程的一路输出交给存储与实时流。
type runWriter struct {
	ar     *activeRun
	stream byte
}

func (rw *runWriter) Write(p []byte) (int, error) {
	off := rw.ar.w.Write(rw.stream, p)
	rw.ar.live.emit(rw.stream, off, p)
	return len(p), nil
}

// ---------- 取消 / 确认 / 对账 / 读取 ----------

// Cancel 取消一次运行（不存在或已结束则忽略）。
func (m *ExecManager) Cancel(id, reason string) {
	m.mu.Lock()
	ar := m.active[id]
	m.mu.Unlock()
	if ar == nil {
		return
	}
	ar.terminate(killCancel, termproto.SanitizeText(reason, 32))
}

// Ack 记录 Hub 已处理该运行的结束事件。
func (m *ExecManager) Ack(id string) {
	if m.store == nil || !termproto.ValidRunID(id) {
		return
	}
	_ = m.store.MarkReported(id)
}

// Sync 向当前控制端发送运行清单，并补报尚未被确认的结束事件。幂等：Hub 对重复的结束事件以确认应答。
func (m *ExecManager) Sync() {
	m.mu.Lock()
	s := m.sink
	runs := make([]*activeRun, 0, len(m.active))
	for _, ar := range m.active {
		runs = append(runs, ar)
	}
	m.mu.Unlock()
	if s == nil {
		return
	}
	list := make([]termproto.RunBrief, 0, len(runs))
	for _, ar := range runs {
		list = append(list, ar.brief())
	}
	if len(list) > termproto.MaxRunsPerFrame {
		list = list[:termproto.MaxRunsPerFrame]
	}
	_ = s.sendJSON(termproto.Runs{T: termproto.TRuns, Running: list})
	if m.store == nil {
		return
	}
	for _, meta := range m.store.Unreported(maxSyncEnds) {
		if s.sendJSON(endFrame(meta)) != nil {
			return
		}
	}
}

// ServeRead 异步服务一次 run_read：按帧把已保存的输出送给请求方。
func (m *ExecManager) ServeRead(s runSink, r termproto.RunRead) {
	fail := func(reason string) {
		_ = s.sendJSON(termproto.RunReadFail{T: termproto.TRunReadFail, Req: r.Req, Reason: reason})
	}
	stream, ok := termproto.ParseStream(r.Stream)
	if !ok || !termproto.ValidRunID(r.ID) {
		fail(termproto.RunReadBad)
		return
	}
	if m.store == nil {
		fail(termproto.RunReadNotFound)
		return
	}
	select {
	case m.reads <- struct{}{}:
	default:
		fail(termproto.RunReadBusy)
		return
	}
	max := r.Max
	if max <= 0 {
		max = termproto.DefaultRunRead
	}
	if max > termproto.MaxRunRead {
		max = termproto.MaxRunRead
	}
	go func() {
		defer func() { <-m.reads }()
		off := r.Off
		for remaining := max; ; {
			pg, err := m.store.Read(r.ID, stream, off, min(remaining, termproto.MaxRunDataFrame))
			if err != nil {
				switch {
				case errors.Is(err, runlog.ErrNotFound):
					fail(termproto.RunReadNotFound)
				case errors.Is(err, runlog.ErrBadID):
					fail(termproto.RunReadBad)
				default:
					fail(termproto.RunReadError)
				}
				return
			}
			remaining -= len(pg.Data)
			var flags byte
			if pg.EOF {
				flags |= termproto.RunFlagEOF
			}
			if pg.Gap {
				flags |= termproto.RunFlagGap
			}
			if pg.Running {
				flags |= termproto.RunFlagRunning
			}
			last := len(pg.Data) == 0 || remaining <= 0 || pg.EOF || pg.Next >= pg.Total
			if last {
				flags |= termproto.RunFlagLast
			}
			frame := termproto.EncodeRunData(termproto.RunChunk{
				Req: r.Req, Flags: flags, Stream: stream, Off: pg.Off, Next: pg.Next, Total: pg.Total, Data: pg.Data,
			})
			if s.sendBinary(frame) != nil {
				return
			}
			if last {
				return
			}
			off = pg.Next
		}
	}()
}

// ---------- 排空与关停 ----------

// SetDraining 不再接受新的运行（排空式关停时调用）；正在进行的运行不受影响。
func (m *ExecManager) SetDraining() {
	m.mu.Lock()
	m.draining = true
	m.mu.Unlock()
}

// Shutdown 终止全部运行并停止后台任务；等待至多 timeout 让它们收尾落盘。
func (m *ExecManager) Shutdown(timeout time.Duration) {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return
	}
	m.closed = true
	list := make([]*activeRun, 0, len(m.active))
	for _, ar := range m.active {
		list = append(list, ar)
	}
	m.mu.Unlock()

	for _, ar := range list {
		ar.terminate(killShutdown, "holder_stopped")
	}
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) && m.ActiveCount() > 0 {
		time.Sleep(20 * time.Millisecond)
	}
	close(m.stop)
	if m.started.Load() {
		<-m.done
	}
}

// ---------- 实时输出 ----------

// liveEmitter 把一路路输出合并、限速后交给全局实时队列。它只服务「正在看的人」：
// 超过速率或队列满了就丢弃，从不阻塞进程；完整输出始终在磁盘上。
type liveEmitter struct {
	m  *ExecManager
	id string

	mu      sync.Mutex
	pend    [2][]byte
	pendOff [2]uint64
	timer   *time.Timer
	tokens  float64
	last    time.Time
	closed  bool
}

func streamAt(i int) byte {
	if i == 0 {
		return termproto.StreamStdout
	}
	return termproto.StreamStderr
}

func streamIndex(s byte) int {
	if s == termproto.StreamStderr {
		return 1
	}
	return 0
}

func (e *liveEmitter) emit(stream byte, off uint64, p []byte) {
	i := streamIndex(stream)
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		return
	}
	if len(e.pend[i]) == 0 {
		e.pendOff[i] = off
	}
	e.pend[i] = append(e.pend[i], p...)
	if len(e.pend[i]) >= runLiveBatch {
		e.flushLocked(i)
		return
	}
	if e.timer == nil {
		e.timer = time.AfterFunc(runLiveDelay, e.onTimer)
	}
}

func (e *liveEmitter) onTimer() {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.timer = nil
	if e.closed {
		return
	}
	e.flushLocked(0)
	e.flushLocked(1)
}

func (e *liveEmitter) flushLocked(i int) {
	data := e.pend[i]
	if len(data) == 0 {
		return
	}
	e.pend[i] = nil
	off := e.pendOff[i]
	now := time.Now()
	e.tokens += now.Sub(e.last).Seconds() * runLiveRate
	if e.tokens > runLiveBurst {
		e.tokens = runLiveBurst
	}
	e.last = now
	if e.tokens < float64(len(data)) {
		e.m.liveDropped.Add(int64(len(data)))
		return
	}
	e.tokens -= float64(len(data))
	for len(data) > 0 {
		n := min(len(data), termproto.MaxRunDataFrame)
		e.m.enqueueLive(termproto.EncodeRunOut(e.id, streamAt(i), off, data[:n]))
		off += uint64(n)
		data = data[n:]
	}
}

// close 发出尚未发送的零头并停止计时器。
func (e *liveEmitter) close() {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		return
	}
	e.flushLocked(0)
	e.flushLocked(1)
	e.closed = true
	if e.timer != nil {
		e.timer.Stop()
		e.timer = nil
	}
}

// RunsDir 返回运行输出在数据目录下的位置（与终端记录各自独立，配额与清理互不影响）。
func RunsDir(dataDir string) string { return filepath.Join(dataDir, "runs") }
