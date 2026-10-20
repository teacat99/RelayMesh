package terminal

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"log"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
	"github.com/teacat99/RelayMesh/v2/internal/installer"
	"github.com/teacat99/RelayMesh/v2/internal/secure"
	"github.com/teacat99/RelayMesh/v2/internal/store"
	"github.com/teacat99/RelayMesh/v2/internal/termproto"
)

// Authenticator 是 Hub 需要的身份能力，由 api.AuthHandler 实现（通过接口避免 terminal 反向依赖 api）。
type Authenticator interface {
	CredentialSource
	// VerifyHumanJWT 只接受「人类登录」签发的 JWT，MCP 凭据一律返回 false。
	VerifyHumanJWT(token string) (subject string, ok bool)
	// IsMCPToken 判断 token 是否是一把有效的 MCP 令牌（环境变量令牌或凭据库令牌）。
	// 终端 REST 只服务人类登录会话：拿 MCP 令牌来访问会得到明确的 403，而不是含糊的“需要登录”。
	IsMCPToken(token string) bool
}

// Limiter 是解锁尝试限流所需的最小接口（api.RateLimiter 满足）。
type Limiter interface {
	CheckLocked(key string) (bool, time.Duration)
	RecordFailure(key string, maxAttempts int, lockout time.Duration) (bool, time.Duration)
	RecordSuccess(key string)
}

// Options 是 Hub 的依赖与配置。
type Options struct {
	Auth    Authenticator
	Store   *store.Store
	Limiter Limiter

	// JWTSecret 只用于启动时判定强度（总闸条件之一），Hub 不会保存或使用它签发任何内容。
	JWTSecret string
	Mode      Mode
	// DiscoveryAlways 为 true 时发现窗口常开（RELAYMESH_TERMINAL_DISCOVERY=always）。
	DiscoveryAlways bool
	// AllowedOrigins 是除「同源」之外额外允许发起终端 WebSocket 的 Origin（完整形式，如 https://relay.example.com）。
	AllowedOrigins []string

	// 终端记录的服务端策略（RELAYMESH_TERMINAL_RECORD*）：下发给每台主机的 Agent，只能让本机配置更严格。
	// RecordOff 全局关闭录制；其余三项在大于 0 时给保留天数、单会话 MiB、整机 MiB 设上限。
	RecordOff           bool
	RecordRetentionDays int
	RecordSessionMB     int
	RecordHostMB        int

	// ExecEnabled 是 G1：RELAYMESH_TERMINAL_EXEC=on 才允许模型经 MCP 在已授权的主机上执行命令（默认关闭）。
	ExecEnabled bool

	// FilesEnabled 是文件能力的服务端开关：RELAYMESH_TERMINAL_FILES=on 才允许模型经 MCP 查看 / 拉取 / 写入已授权主机上
	// 令牌允许目录内的文件（默认关闭；还要求 ExecEnabled，因为所有闸门都复用模型执行这一套）。
	FilesEnabled bool

	// AgentDistDir 是 Agent 二进制的分发目录（RELAYMESH_AGENT_DIST_DIR 或自动探测的结果，见 installer.ResolveDir）。
	// 为空或目录里没有 Agent 时，服务端不提供安装源，Web 回退为 GitHub Release 来源的安装命令。
	AgentDistDir string

	// SSHSecretKey 是 SSH 后端保存凭据所用的主密钥（RELAYMESH_SSH_SECRET_KEY）；缺失或过弱时 SSH 主机功能整体关闭。
	// 它独立于 JWT 密钥：Hub 只用它加密 / 解密保存的 SSH 凭据，不会用于签发或校验任何其他内容。
	SSHSecretKey string

	// 以下字段主要用于测试，零值取生产默认值。
	Now            func() time.Time
	WatchInterval  time.Duration // 总闸轮询间隔，默认 2 秒
	UnlockIdle     time.Duration // 解锁令牌空闲失效，默认 15 分钟
	UnlockAbsolute time.Duration // 解锁令牌绝对失效，默认 8 小时
	// HandshakeTimeout 是 Agent 完成 hello 的期限，默认 termproto.HandshakeTimeout（10 秒）。
	HandshakeTimeout time.Duration
	// OpenTimeout / AttachTimeout 是等待 Agent 应答新建会话 / 附着的期限，默认 10 秒。
	OpenTimeout   time.Duration
	AttachTimeout time.Duration
	// GuardTickEvery 是会话守护与自动续跑检查的间隔，默认 1 分钟；AutoResumeBackoff 是自动续跑的退避（第 n 次续跑前等多久），
	// 默认 5 分钟 / 20 分钟 / 60 分钟。二者只用来让实验与端到端测试不必真等几分钟（RELAYMESH_TERMINAL_GUARD_TICK_SEC /
	// RELAYMESH_TERMINAL_AUTORESUME_BACKOFF_SEC），生产不设。
	GuardTickEvery    time.Duration
	AutoResumeBackoff []time.Duration
	// AutoResumeOK 非零时覆盖预设里的「续跑成功」判定时长（默认 10 分钟，#172），同样只供实验与端到端测试
	// （RELAYMESH_TERMINAL_AUTORESUME_OK_SEC）：让「跑满判定时长」在秒级发生；生产不设，按预设里的分钟数。
	AutoResumeOK time.Duration
}

// 解锁失败限流：连续失败次数与锁定时长固定，不受登录防爆破开关影响（终端是高危能力）。
const (
	unlockMaxFailures = 5
	unlockLockout     = 15 * time.Minute

	ticketPurposeTerm = "term"
	ticketTTL         = 30 * time.Second

	writeTimeout = 10 * time.Second

	maxAttachmentsPerHub = 64
)

// Hub 是终端功能的服务端中枢。
type Hub struct {
	opts Options
	now  func() time.Time

	// wakeHook 在一次手动唤醒登记之后被调用（参数是工作流编号）：聊天里新增了一条系统行，需要通知 Web 刷新。
	wakeHook atomic.Pointer[func(workflowID string)]

	gate    *Gate
	store   *store.Store
	audit   *auditor
	limiter Limiter
	tickets *secure.TicketStore
	unlocks *secure.UnlockStore
	// freshNanos 覆盖「刚刚重新输入过密码」的窗口（纳秒）；0 表示用 FreshUnlockWindow。见 SetFreshUnlockWindow。
	freshNanos atomic.Int64
	// attachTick 是附着周期检查的间隔，0 表示用 attachTick 常量；只有测试会改（把它拉长，让定时检查不插手，单独验证事件路径）。
	attachTickOverride time.Duration

	disc    discovery
	pending *pendingList
	temps   *tempHosts

	// ctx 是 Hub 的根上下文，只在 Shutdown 的最后一步取消：连接协程的读写都挂在它下面，
	// 过早取消会让 coder/websocket 直接掐断底层连接，导致对端收不到带关闭码的优雅关闭。
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup // 连接协程（Agent / 浏览器入口）

	// bgCtx 只管后台循环（总闸监视、周期清理），Shutdown 一开始就取消，避免它们与关闭流程互相干扰。
	bgCtx    context.Context
	bgCancel context.CancelFunc
	bgWG     sync.WaitGroup

	mu          sync.RWMutex
	agents      map[string]*agentConn // 机器码 → 已启用且在线的 Agent 连接
	allowed     map[string]struct{}   // 额外允许的 Origin（小写 scheme://host）
	attachTotal int
	closing     bool

	startOnce sync.Once

	pre *preAuthLimiter
	idx *sessionIndexer
	// guardian 是终端会话守护（D-214）：判断启动器预设拉起的智能体会话有没有意外结束 / 停摆，并生成一键唤醒的计划。
	guardian *guardService
	// screens 消费托管终端的屏幕信号（M3.2）：审批等待、实时 token、上下文占用，见 screen.go。
	screens *screenService

	exec  *ExecService // 模型执行（MCP terminal_exec 等）
	files *FileService // 模型文件能力（MCP terminal_files）
	ssh   *sshBackend  // SSH 后端：为装不了 Agent 的主机提供同一套执行接口
	local *localEngine // Hub 本地会话：Hub 直连 SSH 主机的人工终端（见 localterm.go）

	recAudit recordAuditGate // 记录读取审计的去重（同一人对同一会话的连续读取只记一条）
	prefs    prefsCache      // 终端偏好（宽度保持、终端限高）的内存副本，见 prefs.go

	tlsPin  string // 内置 HTTPS 叶子证书指纹（可选，展示给 Agent 安装命令）
	tlsSPKI string // 内置 HTTPS 叶子证书的公钥指纹（curl --pinnedpubkey 格式，自签名证书下校验安装文件的下载）
	tlsPort int    // 内置 HTTPS 端口（可选，展示给 Agent 安装命令）
	tlsMu   sync.RWMutex

	// 服务端自带的 Agent 安装源（/i/:ticket/:name）。票据跟随发现窗口的纪元换发，见 installer_rest.go。
	dist           *installer.Dist
	installTickets installTickets
	installLimit   *installLimiter
	installSem     chan struct{}

	// upgrades 是各主机的 Agent 升级窗口（内存态）：窗口开着时，/i/ 也接受该窗口的票据。见 upgrade_window.go。
	upgrades upgradeWindows
	// webUpgrades 是各主机最近一次「Web 一键升级」请求的进展（内存态）。见 web_upgrade.go。
	webUpgrades upgradeJobs
}

// NewHub 创建 Hub。必须调用 Start 才会启动后台任务（总闸监视、到期清理），调用 Shutdown 释放。
func NewHub(opts Options) (*Hub, error) {
	if opts.Auth == nil {
		return nil, errors.New("terminal: Auth is required")
	}
	if opts.Store == nil {
		return nil, errors.New("terminal: Store is required")
	}
	if opts.Limiter == nil {
		return nil, errors.New("terminal: Limiter is required")
	}
	if opts.Mode == "" {
		opts.Mode = ModeAuto
	}
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	if opts.WatchInterval <= 0 {
		opts.WatchInterval = 2 * time.Second
	}
	if opts.HandshakeTimeout <= 0 {
		opts.HandshakeTimeout = termproto.HandshakeTimeout
	}
	if opts.OpenTimeout <= 0 {
		opts.OpenTimeout = defaultOpenTimeout
	}
	if opts.AttachTimeout <= 0 {
		opts.AttachTimeout = defaultAttachTimeout
	}
	ctx, cancel := context.WithCancel(context.Background())
	bgCtx, bgCancel := context.WithCancel(ctx)

	h := &Hub{
		opts:     opts,
		now:      now,
		gate:     NewGate(opts.Mode, opts.JWTSecret, opts.Auth),
		store:    opts.Store,
		audit:    newAuditor(opts.Store, now),
		limiter:  opts.Limiter,
		tickets:  secure.NewTicketStore(256),
		unlocks:  secure.NewUnlockStore(opts.UnlockIdle, opts.UnlockAbsolute, 256),
		pending:  newPendingList(termproto.MaxPending, now),
		temps:    newTempHosts(),
		ctx:      ctx,
		cancel:   cancel,
		bgCtx:    bgCtx,
		bgCancel: bgCancel,
		agents:   make(map[string]*agentConn),
		allowed:  make(map[string]struct{}),
		pre:      newPreAuthLimiter(now),

		dist:         installer.NewDist(opts.AgentDistDir),
		installLimit: newInstallLimiter(now),
		installSem:   make(chan struct{}, installMaxTransfers),
	}
	h.idx = newSessionIndexer(h)
	h.guardian = newGuardService(h)
	h.screens = newScreenService(h)
	h.ssh = newSSHBackend(h, opts.SSHSecretKey)
	h.local = newLocalEngine(h)
	h.exec = newExecService(h, opts.ExecEnabled)
	h.files = newFileService(h, opts.FilesEnabled && opts.ExecEnabled)
	h.disc.always = opts.DiscoveryAlways
	h.tickets.SetClock(now)
	h.unlocks.SetClock(now)

	for _, raw := range opts.AllowedOrigins {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		u, err := url.Parse(raw)
		if err != nil || u.Scheme == "" || u.Host == "" {
			log.Printf("[terminal] 忽略无法解析的 RELAYMESH_ALLOWED_ORIGINS 条目: %q（应为 https://host[:port] 形式）", raw)
			continue
		}
		h.allowed[strings.ToLower(u.Scheme+"://"+u.Host)] = struct{}{}
	}
	return h, nil
}

// Start 启动后台任务：总闸监视（状态翻转时立刻拆除连接）与周期性清理。重复调用无副作用。
func (h *Hub) Start() {
	h.startOnce.Do(func() {
		h.exec.start()
		h.bgWG.Add(5)
		go func() {
			defer h.bgWG.Done()
			h.idx.run(h.bgCtx)
		}()
		go func() {
			defer h.bgWG.Done()
			h.guardian.run(h.bgCtx)
		}()
		go func() {
			defer h.bgWG.Done()
			h.screens.run(h.bgCtx)
		}()
		go func() {
			defer h.bgWG.Done()
			h.gate.Watch(h.bgCtx, h.opts.WatchInterval, h.onGateChange)
		}()
		go func() {
			defer h.bgWG.Done()
			h.maintenanceLoop()
		}()
	})
}

// Shutdown 优雅关闭，顺序很重要：
//  1. 标记关闭（拒绝新连接）并停止后台循环；
//  2. 对全部 Agent 与浏览器连接发起带 1001 的关闭握手，等待它们自然结束（受 ctx 限制）；
//  3. 最后才取消根上下文，强制回收仍未结束的连接。
func (h *Hub) Shutdown(ctx context.Context) {
	h.mu.Lock()
	h.closing = true
	h.mu.Unlock()

	h.bgCancel()
	h.bgWG.Wait()
	h.exec.watch.shutdown() // 输出监视协程跟着 bgCtx 一起退出，这里等它们收尾
	h.exec.bus.closeAll()

	h.closeAllConnections(websocket.StatusGoingAway, "server shutting down")

	done := make(chan struct{})
	go func() {
		h.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-ctx.Done():
	}
	h.cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
	}
	h.local.shutdown() // Hub 本地会话随进程结束：先断开附着并关闭 SSH 连接，再关掉 exec 代理缓存的连接
	h.ssh.shutdown()
}

// Enabled 返回总闸当前是否开启（实时判定）。
func (h *Hub) Enabled(ctx context.Context) bool {
	return h.gate.Evaluate(ctx).Open
}

// SetTLSPin 设置内置 HTTPS 叶子证书指纹（"sha256:<hex>"），供「安装 Agent」命令展示 --pin 参数。
func (h *Hub) SetTLSPin(pin string) {
	h.tlsMu.Lock()
	h.tlsPin = pin
	h.tlsMu.Unlock()
}

func (h *Hub) currentTLSPin() string {
	h.tlsMu.RLock()
	defer h.tlsMu.RUnlock()
	return h.tlsPin
}

// SetTLSSPKI 设置内置 HTTPS 叶子证书的公钥指纹（"sha256//<base64>"，即 curl --pinnedpubkey 的参数格式），
// 供自签名证书场景下校验「从服务端下载安装文件」这一步：Agent 自己用证书指纹（SetTLSPin）校验连接，下载则用公钥指纹。
func (h *Hub) SetTLSSPKI(pin string) {
	h.tlsMu.Lock()
	h.tlsSPKI = pin
	h.tlsMu.Unlock()
}

func (h *Hub) currentTLSSPKI() string {
	h.tlsMu.RLock()
	defer h.tlsMu.RUnlock()
	return h.tlsSPKI
}

// SetHTTPSPort 设置内置 HTTPS 监听端口，供「安装 Agent」命令拼出正确的服务端地址。
func (h *Hub) SetHTTPSPort(port int) {
	h.tlsMu.Lock()
	h.tlsPort = port
	h.tlsMu.Unlock()
}

func (h *Hub) currentHTTPSPort() int {
	h.tlsMu.RLock()
	defer h.tlsMu.RUnlock()
	return h.tlsPort
}

// CredentialsChanged 在登录凭据被修改或重置后调用：撤销全部解锁令牌与票据，并断开所有浏览器终端连接。
// Agent 连接是机器身份，不受管理员密码影响；若密码被清空，总闸监视会随后拆除 Agent 连接。
func (h *Hub) CredentialsChanged(actor, ip string) {
	h.unlocks.RevokeAll()
	h.tickets.RevokeAll()
	h.exec.bus.closeAll()
	h.closeAllAttachments(termproto.CloseGateClosed, "credentials changed")
	h.audit.record(context.Background(), auditEvent{Event: EvCredentialsChange, Actor: actor, IP: ip})
	h.gate.Observe(h.ctx, h.onGateChange)
}

// CredentialRevealed 记录一次「查看令牌」尝试（在设置页重新验证 Web 密码后读出某个 MCP 令牌）。
// result 取 ok / fail / deny；detail 只含令牌编号与名称，令牌与密码从不进入审计。
// 失败与被锁定是持有登录态的攻击者可以反复触发的高频事件，按来源合并限流；成功则逐条落盘，因为每一条都是一次真实的泄露面。
// RecordOAuthAudit 记录 OAuth 协议事件（event 为 oauth_* 常量名；detail 不得含机密）。
func (h *Hub) RecordOAuthAudit(event, result, detail string) {
	if h == nil || h.audit == nil {
		return
	}
	h.audit.record(context.Background(), auditEvent{Event: event, Result: result, Actor: ActorSystem, Detail: detail})
}

func (h *Hub) CredentialRevealed(actor, ip, result, detail string) {
	e := auditEvent{Event: EvCredentialReveal, Result: result, Actor: actor, IP: ip, Detail: detail}
	if result == ResultOK {
		h.audit.record(context.Background(), e)
		return
	}
	h.audit.recordLimited(context.Background(), ip, e)
}

// FilesScopeChanged 记录「允许整个文件系统」的变化：result=ok 表示开启 / 关闭已生效，deny 表示因没有刚刚重新验证
// 密码而被拒。detail 只含令牌编号、名称与动作。开启与关闭都逐条落盘（每一条都改变了授权边界）；被拒绝的尝试
// 是持有登录态的攻击者可以反复触发的，按来源合并限流。nil 安全：没有终端子系统时什么也不做。
func (h *Hub) FilesScopeChanged(actor, ip, result, detail string) {
	if h == nil || h.audit == nil {
		return
	}
	e := auditEvent{Event: EvFilesScope, Result: result, Actor: actor, IP: ip, Detail: detail}
	if result == ResultOK {
		h.audit.record(context.Background(), e)
		return
	}
	h.audit.recordLimited(context.Background(), ip, e)
}

// originAllowed 判断浏览器 WebSocket 的 Origin 是否被允许：无 Origin（非浏览器客户端）放行，
// 因为真正的鉴权在一次性票据上，跨站网页一定会带 Origin；有 Origin 时要求同源或在白名单内。
func (h *Hub) originAllowed(origin, host string) bool {
	if origin == "" {
		return true
	}
	u, err := url.Parse(origin)
	if err != nil || u.Host == "" {
		return false
	}
	if strings.EqualFold(u.Host, host) {
		return true
	}
	h.mu.RLock()
	_, ok := h.allowed[strings.ToLower(u.Scheme+"://"+u.Host)]
	h.mu.RUnlock()
	return ok
}

// isClosing 返回 Hub 是否已经进入关闭流程（Shutdown 一开始就置位）。
func (h *Hub) isClosing() bool {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.closing
}

// onGateChange 在总闸状态翻转时由监视协程调用。
func (h *Hub) onGateChange(prev, cur Decision) {
	// Hub 正在关闭：关闭流程自己会有序收尾（带 1001 的关闭握手）。这时的总闸翻转多半是关闭打断了判定，
	// 不能走「总闸关闭」的拆除——它会取消主机上由守护进程持有、本应跨 Hub 重启存活的模型运行。
	// 真正的总闸关闭（清空密码等）在这一刻碰巧撞上关闭也无妨：新的 Hub 启动后每次握手 / 请求都会实时判定，仍然是关闭的。
	if h.isClosing() {
		log.Printf("[terminal] 总闸状态在 Hub 关闭过程中变化（%s → %s），不做拆除：进行中的模型运行由主机上的守护进程继续持有", prev.Reason, cur.Reason)
		return
	}
	h.audit.record(context.Background(), auditEvent{
		Event:  EvGateChanged,
		Result: map[bool]string{true: ResultOK, false: ResultDeny}[cur.Open],
		Actor:  "system",
		Detail: string(prev.Reason) + " → " + string(cur.Reason),
	})
	if prev.Open && !cur.Open {
		log.Printf("[terminal] 总闸关闭（%s），正在断开全部终端连接并撤销令牌", cur.Reason)
		h.teardownForGate(cur.Reason)
		return
	}
	if !prev.Open && cur.Open {
		log.Printf("[terminal] 总闸重新开启")
	}
}

// teardownForGate 总闸关闭后的收尾：撤销凭据、停止发现、通知并断开全部连接。
func (h *Hub) teardownForGate(reason Reason) {
	h.exec.gateClosed() // 趁连接还在，先让主机取消进行中的模型运行
	h.unlocks.RevokeAll()
	h.tickets.RevokeAll()
	h.disc.close(h.now())
	h.closeAllAttachments(termproto.CloseGateClosed, "terminal disabled")
	// Hub 本地会话没有「在主机上继续运行」这回事：终端能力整体停用时，Hub 不应继续替人开着 SSH 连接。
	h.local.endAll()

	for _, e := range h.pending.drain() {
		if e.agent != nil {
			e.agent.closeWith(termproto.CloseGateClosed, "terminal disabled")
		}
	}

	policy := termproto.PolicyMsg{T: termproto.TPolicy, Policy: termproto.Policy{Terminal: false, Reason: string(reason)}}
	for _, a := range h.snapshotAgents() {
		_ = a.writeJSON(policy)
		a.closeWith(termproto.CloseGateClosed, "terminal disabled")
	}
}

// closeAllConnections 关闭全部 Agent（含待启用）与浏览器连接。
func (h *Hub) closeAllConnections(code websocket.StatusCode, reason string) {
	h.closeAllAttachments(code, reason)
	for _, e := range h.pending.drain() {
		if e.agent != nil {
			e.agent.closeWith(code, reason)
		}
	}
	for _, a := range h.snapshotAgents() {
		a.closeWith(code, reason)
	}
}

func (h *Hub) closeAllAttachments(code websocket.StatusCode, reason string) {
	for _, a := range h.snapshotAgents() {
		a.closeAttachments(code, reason)
	}
	h.local.closeAttachments(code, reason)
}

func (h *Hub) snapshotAgents() []*agentConn {
	h.mu.RLock()
	defer h.mu.RUnlock()
	out := make([]*agentConn, 0, len(h.agents))
	for _, a := range h.agents {
		out = append(out, a)
	}
	return out
}

func (h *Hub) onlineAgent(code string) *agentConn {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.agents[code]
}

// registerAgent 登记在线 Agent，返回被取代的旧连接（同一机器码重连）。
func (h *Hub) registerAgent(a *agentConn) (old *agentConn) {
	h.mu.Lock()
	defer h.mu.Unlock()
	old = h.agents[a.code]
	h.agents[a.code] = a
	return old
}

func (h *Hub) unregisterAgent(a *agentConn) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.agents[a.code] == a {
		delete(h.agents, a.code)
	}
}

// reserveAttachment 预占一个浏览器附着名额（全局上限，防止资源被单用户耗尽）。
func (h *Hub) reserveAttachment() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closing || h.attachTotal >= maxAttachmentsPerHub {
		return false
	}
	h.attachTotal++
	return true
}

func (h *Hub) releaseAttachment() {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.attachTotal > 0 {
		h.attachTotal--
	}
}

// track 登记一个处理中的连接协程，使 Shutdown 能等待其结束；Hub 已进入关闭流程时返回 false。
func (h *Hub) track() (release func(), ok bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closing {
		return nil, false
	}
	h.wg.Add(1)
	return h.wg.Done, true
}

// maintenanceLoop 每秒执行：发现窗口到期收尾、离线待启用条目过期、临时主机过期、审计修剪。
func (h *Hub) maintenanceLoop() {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-h.bgCtx.Done():
			return
		case <-ticker.C:
			h.maintain()
		}
	}
}

func (h *Hub) maintain() {
	now := h.now()
	if h.disc.takeExpired(now) {
		h.dropPending("discovery window closed")
		h.audit.record(h.ctx, auditEvent{Event: EvDiscoveryClose, Actor: "system", Detail: "expired"})
	}
	h.sweepUpgradeWindows()
	h.webUpgrades.sweep(now)
	h.pending.expireOffline(termproto.PendingOfflineTTL)
	for _, code := range h.temps.gc(now) {
		if a := h.onlineAgent(code); a != nil {
			_ = a.writeJSON(termproto.PolicyMsg{T: termproto.TPolicy, Policy: termproto.Policy{Terminal: false, Reason: "temporary_expired"}})
			a.closeWith(termproto.CloseDisabled, "temporary enablement expired")
		}
	}
	h.audit.maybePrune(h.ctx)
	h.exec.maintain(now)
	h.ssh.maintain(now)
	h.local.maintain(now)
}

// dropPending 清空待启用列表并以「未知主机」关闭这些连接（Agent 随后按退避重试）。
func (h *Hub) dropPending(reason string) {
	for _, e := range h.pending.drain() {
		if e.agent != nil {
			e.agent.closeWith(termproto.CloseUnknownMachine, reason)
		}
	}
}

// randHex 生成 n 字节随机数的十六进制串。
func randHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		// crypto/rand 失败属于系统级故障，不能退化为可预测值。
		panic("terminal: crypto/rand failed: " + err.Error())
	}
	return hex.EncodeToString(b)
}
