package holder

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/teacat99/RelayMesh/v2/internal/agent/ptyhost"
	"github.com/teacat99/RelayMesh/v2/internal/agent/record"
	"github.com/teacat99/RelayMesh/v2/internal/termproto"
)

// Limits 是 Agent 强制执行的限额；服务端策略只能把它们调得更严（INV-P4）。
type Limits struct {
	MaxSessions int // 每主机会话数上限
	RingSize    int // 每会话回放缓冲字节数
	// DetachedTTL 是本机允许的空闲回收上限：会话的生命周期（主机默认或会话单独设置）不得比它更宽松。
	// 0 表示本机不设上限，会话可以永久保持。服务端策略只能把它调得更短。
	DetachedTTL time.Duration
	ExitedTTL   time.Duration // 已退出会话的保留时长（自退出或最近一次断开起算）
}

// DefaultLimits 返回默认限额。
func DefaultLimits() Limits {
	return Limits{
		MaxSessions: termproto.MaxSessionsPerHost,
		RingSize:    termproto.RingSize,
		DetachedTTL: 0,
		ExitedTTL:   30 * time.Minute,
	}
}

var (
	// ErrTerminalDisabled 表示终端功能被策略关闭（总闸关闭）。
	ErrTerminalDisabled = errors.New("terminal is disabled by server policy")
	// ErrSessionLimit 表示已达会话数上限。
	ErrSessionLimit = errors.New("session limit reached")
	// ErrShuttingDown 表示守护进程（或内嵌模式下的 Agent）正在退出。
	ErrShuttingDown = errors.New("holder is shutting down")
	// ErrDraining 表示守护进程正在排空：不再接受新会话，等现有会话自然结束后退出。
	ErrDraining = errors.New("holder is draining, no new sessions")
)

// ShellFunc 构造在新会话里运行的 shell 的启动规格。
type ShellFunc func(rows, cols uint16) (ptyhost.Spec, error)

// ManagerOptions 是 Manager 的构造参数。
type ManagerOptions struct {
	Spawner ptyhost.Spawner // 默认 ptyhost.NewSpawner()
	Shell   ShellFunc       // 默认 DefaultShell
	Limits  Limits          // 零值字段回落到 DefaultLimits
	// Record 是终端显示内容的本地记录存储；nil 表示不提供录制能力（会话信息里不带录制状态）。
	Record *record.Store
	// RecordFailed 表示记录存储本该启用却打不开：会话照常提供，但信息里标注录制出错。
	RecordFailed bool
	// SSH 非 nil 表示本机开启了 SSH 网关（--ssh-gateway）：会话守护进程可以替 Hub 去连接 SSH 目标。
	SSH  *SSHOptions
	Logf func(format string, args ...any)
	Now  func() time.Time
}

// idleConfig 是空闲回收的生效配置：ceiling 是本机上限（0 = 不设），hostIdle 是主机默认秒数（0 = 永久保持）。
type idleConfig struct {
	ceiling  time.Duration
	hostIdle int
}

// Manager 管理一台主机上的全部会话。它独立于任何网络连接：连接断开、重连都不影响会话存续。
// 外置模式下它运行在守护进程（holder）里，因此也独立于 Agent 进程；内嵌模式下它随 Agent 进程存亡。
type Manager struct {
	spawner ptyhost.Spawner
	shell   ShellFunc
	rec     *record.Store
	recFail bool
	ssh     *sshGateway // 非 nil 表示本机开启了 SSH 网关
	now     func() time.Time
	logf    func(string, ...any)

	mu       sync.Mutex
	local    Limits // 本地默认（启动时确定）
	limits   Limits // 生效限额 = min(本地, 服务端策略)
	hostIdle int    // 主机默认的空闲回收秒数（服务端策略下发）；0 = 永久保持
	// idleCfg 是生效的空闲回收配置的只读快照。会话在持有自己的锁时读取它（生成会话信息），
	// 所以不能再去取管理器的锁；每次策略变化时整体替换。
	idleCfg  atomic.Pointer[idleConfig]
	allowed  bool // 服务端策略是否允许终端；握手前默认允许，随策略下发更新
	sessions map[string]*Session
	order    []*Session
	notify   func()
	observer func()
	closed   bool
	draining bool

	loopStop chan struct{}
	loopDone chan struct{}
	started  bool
}

// NewManager 创建会话管理器。
func NewManager(opts ManagerOptions) *Manager {
	if opts.Spawner == nil {
		opts.Spawner = ptyhost.NewSpawner()
	}
	if opts.Shell == nil {
		opts.Shell = DefaultShell
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.Logf == nil {
		opts.Logf = func(string, ...any) {}
	}
	lim := opts.Limits
	def := DefaultLimits()
	if lim.MaxSessions <= 0 {
		lim.MaxSessions = def.MaxSessions
	}
	if lim.RingSize <= 0 {
		lim.RingSize = def.RingSize
	}
	if lim.DetachedTTL < 0 {
		lim.DetachedTTL = def.DetachedTTL
	}
	if lim.ExitedTTL <= 0 {
		lim.ExitedTTL = def.ExitedTTL
	}
	m := &Manager{
		spawner:  opts.Spawner,
		shell:    opts.Shell,
		rec:      opts.Record,
		recFail:  opts.RecordFailed,
		now:      opts.Now,
		logf:     opts.Logf,
		local:    lim,
		limits:   lim,
		allowed:  true,
		sessions: make(map[string]*Session),
		loopStop: make(chan struct{}),
		loopDone: make(chan struct{}),
	}
	m.idleCfg.Store(&idleConfig{ceiling: lim.DetachedTTL})
	if opts.SSH != nil {
		// 允许名单有误时宁可关闭网关，也不能悄悄退化成「不限制」。命令行入口会先校验，所以正常不会走到这里。
		if g, err := newSSHGateway(opts.SSH); err != nil {
			m.logf("SSH 网关配置无效，已保持关闭: %v", err)
		} else {
			m.ssh = g
			if g.storeErr != nil {
				// 网关仍可用于已有的探测，但无法托管凭据，也就不会宣告 ssh_store：Hub 不会把主机登记到它上面。
				m.logf("SSH 网关的凭据库不可用，暂时不能托管凭据: %v", g.storeErr)
			}
		}
	}
	return m
}

// SSHEnabled 报告本机是否开启了 SSH 网关。
func (m *Manager) SSHEnabled() bool { return m.ssh != nil }

// SSHStoreReady 报告网关的凭据库是否可用（可托管目标的登录凭据）。
func (m *Manager) SSHStoreReady() bool { return m.ssh != nil && m.ssh.storeReady() }

// SSHOp 执行一次性的 SSH 操作（探测主机密钥 / 真实登录检查）。网关未开启时返回 gateway_disabled。
func (m *Manager) SSHOp(op termproto.SSHOp) termproto.SSHResult {
	if m.ssh == nil {
		return termproto.SSHResult{T: termproto.TSSHResult, Req: op.Req, Code: termproto.SSHCodeDisabled,
			Msg: "这台主机没有开启 SSH 网关（--ssh-gateway）"}
	}
	return m.ssh.op(op)
}

// Validate 校验网关选项（目前是允许名单的格式），供命令行在启动前给出明确的错误。
func (o *SSHOptions) Validate() error {
	_, err := parseAllowList(o.Allow)
	return err
}

// idle 返回当前生效的空闲回收配置。
func (m *Manager) idle() idleConfig {
	if c := m.idleCfg.Load(); c != nil {
		return *c
	}
	return idleConfig{}
}

// Start 启动周期任务（停滞检查与过期回收）。重复调用无副作用。
func (m *Manager) Start() {
	m.mu.Lock()
	if m.started || m.closed {
		m.mu.Unlock()
		return
	}
	m.started = true
	m.mu.Unlock()
	go m.loop()
}

// SetNotifier 注册「会话集合或状态发生变化」的回调（用于向 Hub 推送会话清单）。
// 回调在不持有任何锁的情况下被调用，必须快速返回。
func (m *Manager) SetNotifier(fn func()) {
	m.mu.Lock()
	m.notify = fn
	m.mu.Unlock()
}

// SetObserver 注册第二个「会话集合或状态发生变化」的回调，供守护进程持久化会话索引。
// 与 SetNotifier 相互独立：控制端换人不会影响它。回调同样在不持有任何锁的情况下被调用。
func (m *Manager) SetObserver(fn func()) {
	m.mu.Lock()
	m.observer = fn
	m.mu.Unlock()
}

func (m *Manager) changed() {
	m.mu.Lock()
	fn, obs := m.notify, m.observer
	m.mu.Unlock()
	if fn != nil {
		fn()
	}
	if obs != nil {
		obs()
	}
}

// SetDraining 进入排空状态：此后不再接受新会话。不可逆。
func (m *Manager) SetDraining() {
	m.mu.Lock()
	m.draining = true
	m.mu.Unlock()
}

// RunningCount 返回仍在运行的会话数（不含已退出但尚未回收的）。
func (m *Manager) RunningCount() int {
	m.mu.Lock()
	list := append([]*Session(nil), m.order...)
	m.mu.Unlock()
	n := 0
	for _, s := range list {
		s.mu.Lock()
		if s.state == termproto.SessionRunning && !s.removed {
			n++
		}
		s.mu.Unlock()
	}
	return n
}

// Open 新建会话。
func (m *Manager) Open(name string, rows, cols uint16) (termproto.SessionInfo, error) {
	return m.OpenWith(OpenOptions{Name: name, Rows: rows, Cols: cols})
}

// OpenOptions 是新建会话的参数。
type OpenOptions struct {
	Name       string
	Rows, Cols uint16
	// Incognito 为 true 时本会话不录制，主机上不留任何记录。
	Incognito bool
	// Compact 为 true 时是精简会话：回放缓冲 CompactRingSize、宽度不超过 CompactMaxCols。
	Compact bool
	// IdleSec 是本会话单独设置的空闲回收秒数（0 = 永久保持）；nil 沿用主机默认。
	IdleSec *int
	// Cwd 是新会话的启动目录；不是主机上存在的绝对目录时忽略（回落到默认目录）。
	Cwd string
	// SSH 非空表示这是经 SSH 网关的会话（Cwd 对它没有意义）。只接受引用本机凭据库的目标（Stored），凭据由网关自己取用，不会被保存到会话里。
	SSH *termproto.SSHTarget
}

// OpenWith 新建一个会话。
func (m *Manager) OpenWith(oo OpenOptions) (termproto.SessionInfo, error) {
	rows, cols := termproto.ClampSize(oo.Rows, oo.Cols)
	if oo.Compact && cols > termproto.CompactMaxCols {
		cols = termproto.CompactMaxCols
	}
	name := termproto.SanitizeText(oo.Name, termproto.MaxSessionNameRunes)

	m.mu.Lock()
	err := m.admitLocked()
	limits := m.limits
	m.mu.Unlock()
	if err != nil {
		return termproto.SessionInfo{}, err
	}

	var (
		term      ptyhost.Terminal
		shellPath string
		target    string
	)
	if oo.SSH != nil {
		// 经 SSH 网关：不启动本机 shell，而是替 Hub 去连接目标。网关没开启时一律拒绝（即使 Hub 发来了目标）。
		if m.ssh == nil {
			return termproto.SessionInfo{}, sshFail(termproto.SSHCodeDisabled, "这台主机没有开启 SSH 网关（--ssh-gateway）")
		}
		t, err := m.ssh.openTerminal(oo.SSH, rows, cols)
		if err != nil {
			return termproto.SessionInfo{}, err
		}
		term, shellPath, target = t, sshShellName, oo.SSH.ID
	} else {
		spec, err := m.shell(rows, cols)
		if err != nil {
			return termproto.SessionInfo{}, fmt.Errorf("prepare shell: %w", err)
		}
		spec.Rows, spec.Cols = rows, cols
		if dir := usableDir(oo.Cwd); dir != "" {
			spec.Dir = dir
		}
		t, err := m.spawner.Spawn(spec)
		if err != nil {
			return termproto.SessionInfo{}, fmt.Errorf("start shell: %w", err)
		}
		term, shellPath = t, spec.Path
	}
	id, err := newSessionID()
	if err != nil {
		term.Kill()
		_ = term.Close()
		return termproto.SessionInfo{}, err
	}
	ringSize := limits.RingSize
	if oo.Compact && ringSize > termproto.CompactRingSize {
		ringSize = termproto.CompactRingSize
	}
	s := newSession(m, id, name, term, rows, cols, shellPath, ringSize)
	s.compact = oo.Compact
	if termproto.ValidMachineCode(target) {
		s.target = target
	}
	if oo.IdleSec != nil {
		s.idleSet, s.idleSec = true, termproto.ClampIdle(*oo.IdleSec)
	}
	switch {
	case m.rec != nil:
		s.rec, s.recState = m.rec.Begin(record.Meta{
			SID: id, Name: name, Shell: filepath.Base(shellPath), Rows: rows, Cols: cols, Incognito: oo.Incognito,
		})
	case m.recFail:
		s.recState = record.StateError
	}

	m.mu.Lock()
	if err := m.admitLocked(); err != nil { // 准备期间策略或状态可能已变化
		m.mu.Unlock()
		term.Kill()
		_ = term.Close()
		if s.rec != nil { // 会话没有真正创建：不留记录
			s.rec.End(nil, "", "aborted")
			_ = m.rec.Delete(id)
		}
		return termproto.SessionInfo{}, err
	}
	m.sessions[id] = s
	m.order = append(m.order, s)
	m.mu.Unlock()

	s.start()
	m.changed()
	return s.info(), nil
}

func (m *Manager) admitLocked() error {
	switch {
	case m.closed:
		return ErrShuttingDown
	case m.draining:
		return ErrDraining
	case !m.allowed:
		return ErrTerminalDisabled
	case len(m.sessions) >= m.limits.MaxSessions:
		return ErrSessionLimit
	}
	return nil
}

// Get 按 ID 查找会话。
func (m *Manager) Get(sid string) *Session {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.sessions[sid]
}

// Snapshot 返回全部会话的当前信息（按创建顺序）。
func (m *Manager) Snapshot() []termproto.SessionInfo {
	m.mu.Lock()
	list := append([]*Session(nil), m.order...)
	m.mu.Unlock()
	out := make([]termproto.SessionInfo, 0, len(list))
	for _, s := range list {
		out = append(out, s.info())
	}
	return out
}

// Count 返回当前会话数（含已退出但尚未回收的）。
func (m *Manager) Count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.sessions)
}

// ApplyPolicy 应用服务端下发的策略：取本地默认与策略中更严的一方；终端被关闭时终止全部会话。
func (m *Manager) ApplyPolicy(p termproto.Policy) {
	m.mu.Lock()
	m.allowed = p.Terminal
	eff := m.local
	if p.MaxSessions > 0 && p.MaxSessions < eff.MaxSessions {
		eff.MaxSessions = p.MaxSessions
	}
	if p.DetachedTTLSec > 0 {
		// 旧版 Hub 下发的固定「断开多久回收」：与本机上限取更短的一方。
		if ttl := time.Duration(p.DetachedTTLSec) * time.Second; eff.DetachedTTL == 0 || ttl < eff.DetachedTTL {
			eff.DetachedTTL = ttl
		}
	}
	m.limits = eff
	idleChanged := m.hostIdle != termproto.ClampIdle(p.IdleSec) || m.idle().ceiling != eff.DetachedTTL
	m.hostIdle = termproto.ClampIdle(p.IdleSec)
	m.idleCfg.Store(&idleConfig{ceiling: eff.DetachedTTL, hostIdle: m.hostIdle})
	var victims []*Session
	if !p.Terminal {
		victims = append(victims, m.order...)
	}
	m.mu.Unlock()

	if m.rec != nil {
		m.rec.SetPolicy(record.Policy{
			Off:           p.RecOff,
			RetentionDays: p.RecRetentionDays,
			SessionBytes:  int64(p.RecSessionMB) << 20,
			SessionLines:  int64(p.RecMaxLines),
			HostBytes:     int64(p.RecHostMB) << 20,
		})
	}

	if len(victims) > 0 {
		m.logf("终端总闸已关闭（%s），终止全部 %d 个会话", termproto.SanitizeText(p.Reason, 40), len(victims))
	}
	for _, s := range victims {
		s.killFor("policy")
	}
	if idleChanged {
		m.changed() // 每个会话报告的生效生命周期变了：让 Hub 的会话列表尽快跟上
	}
}

// KillAll 终止全部会话。
func (m *Manager) KillAll() {
	m.mu.Lock()
	list := append([]*Session(nil), m.order...)
	m.mu.Unlock()
	for _, s := range list {
		s.kill()
	}
}

// remove 把会话移出管理器并回收其资源。
func (m *Manager) remove(s *Session) {
	m.mu.Lock()
	if _, ok := m.sessions[s.id]; !ok {
		m.mu.Unlock()
		return
	}
	delete(m.sessions, s.id)
	for i, o := range m.order {
		if o == s {
			m.order = append(m.order[:i], m.order[i+1:]...)
			break
		}
	}
	m.mu.Unlock()
	s.teardown()
	m.changed()
}

// sessionExited 由会话的 monitor 在进程退出并排空输出后调用。
func (m *Manager) sessionExited(s *Session, killed bool) {
	if killed {
		m.remove(s)
		return
	}
	m.changed()
}

// Shutdown 终止全部会话并停止周期任务（守护进程被有意停止、或内嵌模式下 Agent 退出前调用）。
func (m *Manager) Shutdown(timeout time.Duration) {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return
	}
	m.closed = true
	started := m.started
	list := append([]*Session(nil), m.order...)
	m.mu.Unlock()

	if started {
		close(m.loopStop)
		<-m.loopDone
	}
	if m.ssh != nil {
		m.ssh.shutdown() // 关掉 exec 代理缓存的连接；SSH 终端会话随下面的会话回收一并结束
	}
	for _, s := range list {
		s.killFor("holder_stopped")
	}
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if m.Count() == 0 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	for _, s := range list { // 仍未退出的会话强制回收
		m.remove(s)
	}
}

// loop 周期性检查附着方停滞与会话过期。
func (m *Manager) loop() {
	defer close(m.loopDone)
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-m.loopStop:
			return
		case <-ticker.C:
			m.tick(m.now())
		}
	}
}

func (m *Manager) tick(now time.Time) {
	m.mu.Lock()
	list := append([]*Session(nil), m.order...)
	lim := m.limits
	hostIdle := m.hostIdle
	m.mu.Unlock()
	screenDirty := false
	for _, s := range list {
		if s.screenChanged(now) {
			screenDirty = true
		}
		if s.checkStalls(now) > 0 {
			m.logf("会话 %s 有附着方长时间不确认输出，已断开该附着", s.id[:8])
		}
		switch s.expiry(now, lim, hostIdle) {
		case expireKill:
			m.logf("会话 %s 长时间没有交互，已按生命周期设置回收", s.id[:8])
			s.killFor("idle_ttl")
		case expireRemove:
			m.remove(s)
		}
	}
	if screenDirty {
		m.changed() // 屏幕识别结果变了：让 Hub 的会话清单尽快跟上（状态卡 / 审批提醒）
	}
	if m.ssh != nil {
		m.ssh.maintain(now)
	}
}

type expiry int

const (
	expireNone expiry = iota
	expireKill
	expireRemove
)

// effectiveIdle 返回会话生效的空闲回收时长；0 表示永久保持。
// 会话单独设置的值优先于主机默认；本机上限（lim.DetachedTTL）永远是天花板，设置得再宽松也不能超过它。
// 调用方持有 s.mu。
func (s *Session) effectiveIdleLocked(ceiling time.Duration, hostIdle int) time.Duration {
	sec := hostIdle
	if s.idleSet {
		sec = s.idleSec
	}
	idle := time.Duration(sec) * time.Second
	if ceiling > 0 && (idle == 0 || idle > ceiling) {
		idle = ceiling
	}
	return idle
}

// expiry 判定会话是否已过期：
//   - 运行中：没有人附着，并且自「最后一次输入」与「最后一次断开」中较晚者起，已超过生效的空闲回收时长 → 终止
//     （永久保持的会话从不因此被回收）；
//   - 已退出：超过 ExitedTTL → 回收。
//
// 「交互」只指输入与附着：输出本身不算，否则后台持续输出的任务永远不会被回收。
func (s *Session) expiry(now time.Time, lim Limits, hostIdle int) expiry {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.chans) > 0 || s.removed {
		return expireNone
	}
	switch s.state {
	case termproto.SessionRunning:
		if idle := s.effectiveIdleLocked(lim.DetachedTTL, hostIdle); idle > 0 && now.Sub(s.lastActivityLocked()) >= idle {
			return expireKill
		}
	case termproto.SessionExited:
		ref := s.exitedAt
		if s.lastDetach.After(ref) {
			ref = s.lastDetach
		}
		if lim.ExitedTTL > 0 && now.Sub(ref) >= lim.ExitedTTL {
			return expireRemove
		}
	}
	return expireNone
}

// usableDir 返回 dir 当它是主机上存在的绝对目录；否则返回空串。
func usableDir(dir string) string {
	if dir == "" || !filepath.IsAbs(dir) {
		return ""
	}
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		return ""
	}
	return filepath.Clean(dir)
}

func newSessionID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}
