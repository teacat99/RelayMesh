// Package agent 实现 RelayMesh 终端 Agent：运行在被管理主机上的常驻进程，
// 主动连接 Hub（服务端）的通用入口，按需在本机的伪终端里运行 shell，并把它安全地中继给 Web 浏览器。
//
// 关键性质：
//   - 伪终端由「会话守护进程」（internal/agent/holder）持有：外置模式下它是独立进程，Agent 升级、重启乃至崩溃都不会
//     打断会话；内嵌模式（Windows 临时启动、`--holder=embedded`）下它与 Agent 同进程，会话随进程结束，且会如实标注；
//   - 伪终端的寿命只取决于守护进程自身、显式 kill 与 TTL，绝不取决于网络连接（INV-P1）；
//   - 身份是 Ed25519 密钥，机器码由公钥派生；服务端只存公钥，没有任何可冒充主机的秘密；
//   - Agent 必须验证服务端（系统 CA / --pin 指纹 / TOFU），失败即关闭，绝不降级为明文；
//   - 限额由守护进程自己强制执行，服务端策略只能收紧。
package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"net/http"
	"net/url"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
	"github.com/teacat99/RelayMesh/v2/internal/agent/files"
	"github.com/teacat99/RelayMesh/v2/internal/agent/holder"
	"github.com/teacat99/RelayMesh/v2/internal/agent/ptyhost"
	"github.com/teacat99/RelayMesh/v2/internal/agent/record"
	"github.com/teacat99/RelayMesh/v2/internal/termproto"
)

// Version 是 Agent 的版本号，独立二进制构建时通过 -ldflags "-X github.com/teacat99/RelayMesh/v2/internal/agent.Version=…" 注入。
var Version = "dev"

// Rev 是这个二进制的「Agent 侧代码指纹」（见 internal/agentrev、termproto.ValidAgentRev），构建时通过
// -ldflags "-X main.rev=…" 注入后由 main 赋值；空表示非发布构建，此时服务端回退到版本号比较。
var Rev = ""

// State 是 Agent 与 Hub 之间的连接状态（供命令行输出与测试观察）。
type State string

const (
	StateConnecting State = "connecting" // 正在拨号 / 握手
	StatePending    State = "pending"    // 已连上，等待管理员在 Web 上启用
	StateOnline     State = "online"     // 已启用，可以提供终端
	StateRetrying   State = "retrying"   // 连接失败或断开，等待重试
)

// Event 描述一次状态变化。
type Event struct {
	State  State
	Detail string
	// Code 是对端的 WebSocket 关闭码（没有则为 0）。
	Code int
	// Retry 在 StateRetrying 时给出下次重试前的等待时间。
	Retry time.Duration
}

// Options 是 Agent 的运行参数。
type Options struct {
	Server   ServerTarget
	Trust    TrustOptions
	Identity *Identity
	// Version 自报的 Agent 版本；空则使用包级 Version。
	Version string
	// Rev 自报的 Agent 侧代码指纹；空则使用包级 Rev（仍为空表示非发布构建）。
	Rev    string
	Limits Limits
	Logf   func(format string, args ...any)
	// OnEvent 在状态变化时被同步调用，必须快速返回。
	OnEvent func(Event)

	// Holder 决定会话由谁持有，零值为内嵌（见 HolderMode）。
	Holder HolderMode
	// HolderSocket 是外置守护进程的本地套接字路径（external / auto 必填）。
	HolderSocket string

	// Record 是终端记录（终端上显示什么就记录什么，只写在本机）的配置。Record.Dir 为空则不提供记录与历史读取。
	// 内嵌模式下由 Agent 自己写入该目录；外置模式下由守护进程写入，Agent 只读。
	// Record.Disabled 是本机的一票否决（--no-record）。
	Record record.Options

	// Exec 是模型执行（非交互命令运行）的配置。Exec.DenyExec 是本机的一票否决（--deny-model-exec）：
	// 内嵌模式下由 Agent 自己执行；外置模式下命令由守护进程执行，但 Agent 同样会拦下被否决的请求，
	// 所以无论在哪一侧设置，否决都生效。Exec.Dir 为空表示本机不能执行模型命令。
	Exec holder.ExecOptions

	// Files 是文件能力（模型经 MCP 查看 / 拉取 / 写入文件）的本机选项。文件能力同样受 Exec.DenyExec 一票否决。
	Files FileOptions

	// SSH 非 nil 表示本机开启了 SSH 网关（--ssh-gateway）。内嵌模式下由 Agent 自己的会话宿主承担；
	// 外置模式下是守护进程（holder run --ssh-gateway）去连接 SSH 目标，Agent 只转发帧，这里的值不起作用。
	SSH *holder.SSHOptions

	// SelfUninstall 非 nil 表示这个 Agent 是以常驻服务运行、并且知道如何卸载自己：
	// 管理员删除主机并勾选「同时卸载设备端」时，Hub 会发来 uninstall 帧，Agent 调用它在本机另起一个与自身进程树无关的卸载进程
	// （purge 对应 --purge）。返回 nil 表示卸载进程已成功启动（卸载本身随后异步完成，会停止本 Agent）；
	// 返回错误则什么都没有发生，Agent 继续正常运行。前台运行 / 临时身份 / 暂不支持的系统保持 nil。
	SelfUninstall func(purge bool) error

	// SelfUpgrade 非 nil 表示这个 Agent 是以常驻服务运行、知道如何把升级交给一个与自身进程树无关的升级进程：
	// 握手时声明「Web 升级」能力（所有者在本机允许了才是 on），收到 Hub 的 upgrade 帧后下载、校验并交接。
	// 前台运行 / 临时身份 / 暂不支持的系统保持 nil，不声明能力。见 webupgrade.go。
	SelfUpgrade *SelfUpgrade

	// 以下字段供测试注入。
	Spawner ptyhost.Spawner
	Shell   ShellFunc

	// 重试节奏（零值取默认）：网络类错误指数退避 MinBackoff→MaxBackoff；
	// 握手无效 / 总闸关闭 / 发现关闭等取 SlowRetry；被停用取 DisabledRetry。
	MinBackoff    time.Duration
	MaxBackoff    time.Duration
	SlowRetry     time.Duration
	DisabledRetry time.Duration
	// Jitter 给等待时间加抖动；默认 ±20%。
	Jitter func(time.Duration) time.Duration
}

// Agent 是终端 Agent 的运行实例。
type Agent struct {
	opts Options
	logf func(string, ...any)
	code string

	// uninstallUntil 非零表示已经接受了卸载请求：在这个时刻（Unix 纳秒）之前不再重连 Hub（见 uninstallGrace）。
	uninstallUntil atomic.Int64

	// upg 处理 Hub 的升级请求（nil 表示不支持）；它的「进行中」状态要跨越 WebSocket 重连，所以放在 Agent 上而不是每条连接上。
	upg *upgrader
	// auto 非 nil 才会向 Hub 做自动升级检查（见 Options.SelfUpgrade.AutoMode）；是否真的检查还要看本机所有者选的模式。
	auto *autoChecker
	// regate 标记「因为本地的升级开关（Web 一键升级 / 自动升级）变了而主动断开重连」，用来给日志一个明白的原因。
	regate atomic.Bool

	// src 是当前使用的守护进程来源；auto 模式在 Run 开始时才确定。
	src holderSource
	// host 与 mgr 只在内嵌模式下存在（会话与 Agent 同进程）。
	host *holder.Host
	mgr  *Manager
}

// New 校验参数并创建 Agent。
func New(opts Options) (*Agent, error) {
	if opts.Identity == nil {
		return nil, errors.New("缺少身份")
	}
	if opts.Server.URL == "" {
		return nil, errors.New("缺少服务端地址")
	}
	if err := opts.Trust.Validate(opts.Server); err != nil {
		return nil, err
	}
	if opts.Logf == nil {
		opts.Logf = func(string, ...any) {}
	}
	if opts.Version == "" {
		opts.Version = Version
	}
	if opts.Rev == "" {
		opts.Rev = Rev
	}
	if opts.MinBackoff <= 0 {
		opts.MinBackoff = time.Second
	}
	if opts.MaxBackoff <= 0 {
		opts.MaxBackoff = time.Minute
	}
	if opts.SlowRetry <= 0 {
		opts.SlowRetry = time.Minute
	}
	if opts.DisabledRetry <= 0 {
		// 管理员重新启用后，主机要等 Agent 下一次重试才会上线：间隔太长会让「启用」看起来没有生效。
		// 被停用期间的每次重试只是一次握手（不入审计、不占会话），一分钟一次的成本可以忽略。
		opts.DisabledRetry = time.Minute
	}
	if opts.Jitter == nil {
		opts.Jitter = func(d time.Duration) time.Duration {
			return time.Duration(float64(d) * (0.8 + 0.4*rand.Float64()))
		}
	}
	a := &Agent{opts: opts, logf: opts.Logf, code: opts.Identity.MachineCode()}
	if opts.SelfUpgrade != nil && opts.Identity.Kind != termproto.KindTemporary {
		a.upg = newUpgrader(opts.SelfUpgrade, opts.Version, opts.Server, opts.Trust, opts.Logf)
		if opts.SelfUpgrade.AutoMode != nil {
			a.auto = newAutoChecker(opts.SelfUpgrade, opts.Version, opts.Logf)
			a.auto.upg = a.upg // 静默升级与 Web 一键升级复用同一条链路，共用「进行中」名额
		}
	}
	switch opts.Holder {
	case "", HolderEmbedded:
		if err := a.useEmbedded(); err != nil {
			return nil, err
		}
	case HolderExternal:
		if opts.HolderSocket == "" {
			return nil, errors.New("--holder=external 需要守护进程的套接字路径")
		}
		a.src = &socketSource{path: opts.HolderSocket, version: opts.Version}
	case HolderAuto:
		if opts.HolderSocket == "" {
			return nil, errors.New("--holder=auto 需要守护进程的套接字路径")
		}
	default:
		return nil, fmt.Errorf("未知的守护进程模式 %q", opts.Holder)
	}
	return a, nil
}

// useEmbedded 在进程内创建会话宿主（不具持久性：会话随本进程结束）。
func (a *Agent) useEmbedded() error {
	hopts := holder.HostOptions{
		Manager: holder.ManagerOptions{
			Spawner: a.opts.Spawner,
			Shell:   a.opts.Shell,
			Limits:  a.opts.Limits,
			Logf:    a.opts.Logf,
			SSH:     a.opts.SSH,
		},
		Version:    a.opts.Version,
		Rev:        a.opts.Rev,
		Persistent: false,
	}
	if a.opts.Record.Dir != "" {
		ro := a.opts.Record
		hopts.Record = &ro
	}
	if a.opts.Exec.Dir != "" || a.opts.Exec.DenyExec {
		eo := a.opts.Exec
		hopts.Exec = &eo
	}
	host, err := holder.NewHost(hopts)
	if err != nil {
		return err
	}
	a.host = host
	a.mgr = host.Manager()
	a.src = &embeddedSource{host: host, version: a.opts.Version}
	return nil
}

// resolveHolder 在 auto 模式下决定使用外置还是内嵌：外置守护进程能连上（含握手）就用它，否则退回内嵌。
func (a *Agent) resolveHolder(ctx context.Context) error {
	if a.src != nil {
		return nil
	}
	// 探测用管理端身份，不会取代真正的控制连接（例如另一个正在运行的 Agent）。
	pctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	conn, err := holder.Dial(pctx, a.opts.HolderSocket)
	if err == nil {
		var cli *holder.Client
		if cli, err = holder.Handshake(pctx, conn, holder.RoleAdmin, a.opts.Version); err == nil {
			_ = cli.Conn.Close()
			a.logf("检测到会话守护进程（版本 %s），终端会话将由它持有，Agent 重启不会中断会话", cli.Hello.Holder)
			a.src = &socketSource{path: a.opts.HolderSocket, version: a.opts.Version}
			return nil
		}
		_ = conn.Close()
	}
	a.logf("未检测到可用的会话守护进程（%v），使用内嵌模式：终端会话将随本进程结束", err)
	return a.useEmbedded()
}

// recordSource 返回读取本机终端记录的来源。
func (a *Agent) recordSource() recordSource {
	if a.host != nil {
		if st := a.host.Records(); st != nil {
			return st
		}
	}
	if a.opts.Record.Dir != "" {
		return record.NewReader(a.opts.Record.Dir)
	}
	return nil
}

// MachineCode 返回本机的机器码。
func (a *Agent) MachineCode() string { return a.code }

// SessionsOutliveAgent 报告会话是否由外置守护进程持有（Agent 退出后仍在运行）。auto 模式在 Run 开始后才有确定答案。
func (a *Agent) SessionsOutliveAgent() bool { return a.src != nil && a.host == nil }

// Manager 返回内嵌模式下的会话管理器（主要供测试观察；外置守护进程模式下为 nil）。
func (a *Agent) Manager() *Manager { return a.mgr }

func (a *Agent) emit(ev Event) {
	if a.opts.OnEvent != nil {
		a.opts.OnEvent(ev)
	}
}

// Run 保持与 Hub 的连接直到 ctx 被取消：断线自动重连，会话在重连期间继续运行。
// 内嵌模式下返回时已终止全部会话（PTY 的寿命不超过 Agent 进程）；外置守护进程模式下会话由守护进程继续持有。
func (a *Agent) Run(ctx context.Context) error {
	if err := a.resolveHolder(ctx); err != nil {
		return err
	}
	if a.host != nil {
		a.host.Start()
		defer a.host.Close(false)
	}

	attempt := 0
	for ctx.Err() == nil {
		a.emit(Event{State: StateConnecting})
		res := a.connectOnce(ctx)
		if ctx.Err() != nil {
			break
		}
		if res.welcomed {
			attempt = 0
		}
		delay := a.retryDelay(res, &attempt)
		if hold := a.uninstallHoldoff(); hold > delay {
			delay = hold
		}
		msg := res.describe()
		a.logf("%s；%s 后重试", msg, fmtDelay(delay))
		a.emit(Event{State: StateRetrying, Detail: msg, Code: int(res.code), Retry: delay})
		select {
		case <-ctx.Done():
		case <-time.After(delay):
		}
	}
	return nil
}

// uninstallGrace 是 Agent 接受卸载请求后暂停重连的时间。卸载进程会很快把本 Agent 停掉；
// 这期间重新连上 Hub 只会让一台刚被删除的主机又在发现列表里冒出来一次。
// 超过这个时间还没被停掉，说明卸载没有成功，恢复正常重连（并且日志里有卸载进程的失败信息）。
const uninstallGrace = 3 * time.Minute

// uninstallHoldoff 返回距离「可以恢复重连」还要等多久；没有接受过卸载请求或已过期则不大于 0。
func (a *Agent) uninstallHoldoff() time.Duration {
	until := a.uninstallUntil.Load()
	if until == 0 {
		return 0
	}
	return time.Until(time.Unix(0, until))
}

// fmtDelay 把等待时间格式化成人类友好的短文本（不把 400ms 显示成「0s」）。
func fmtDelay(d time.Duration) string {
	if d < 10*time.Second {
		return d.Round(100 * time.Millisecond).String()
	}
	return d.Round(time.Second).String()
}

// ---------- 单次连接 ----------

type connResult struct {
	code     websocket.StatusCode // 对端关闭码；没有则为 0
	err      error
	welcomed bool // 握手已完成（收到 welcome）
	reason   string
	// reconnect 表示这次断开是 Agent 自己决定的（不是故障），应马上重连而不是按故障退避。
	reconnect bool
}

func (r connResult) describe() string {
	switch {
	case r.reason != "":
		return r.reason
	case r.code > 0:
		return describeClose(r.code)
	case r.err != nil:
		return "连接失败: " + termproto.SanitizeText(rootCause(r.err), 400)
	}
	return "连接已断开"
}

// describeClose 把服务端的关闭码翻译成管理员能直接照做的提示。
// Agent 的日志往往是排查「主机为什么没出现 / 为什么离线」时唯一的线索，只给数字太不友好。
func describeClose(code websocket.StatusCode) string {
	base := fmt.Sprintf("服务端关闭了连接（%d）", int(code))
	switch int(code) {
	case int(websocket.StatusGoingAway):
		return base + "：服务端正在重启或关闭"
	case termproto.CloseInvalidHandshake:
		return base + "：握手无效，多为 Agent 与服务端版本不兼容"
	case termproto.CloseGateClosed:
		return base + "：服务端的终端功能已关闭（例如 Web 尚未设置登录密码）"
	case termproto.CloseUnknownMachine:
		return base + "：服务端还不认识这台主机——请在 Web 终端页点「接入主机」开启发现窗口，Agent 会自动重试"
	case termproto.CloseDisabled:
		return base + "：这台主机已被停用或忽略——在 Web 上重新启用后，Agent 会自动重试"
	case termproto.CloseSuperseded:
		return base + "：同一机器码在别处建立了新连接（是否有两个 Agent 共用了同一份身份？）"
	case termproto.CloseRateLimited:
		return base + "：服务端限流，稍后重试"
	}
	return base
}

// rootCause 去掉网络库层层包装的前缀（"failed to WebSocket dial: … Get \"https://…\": "），
// 只保留真正有用的原因——例如证书指纹不匹配的说明，否则它会被冗长的前缀挤出显示长度。
func rootCause(err error) string {
	var ue *url.Error
	if errors.As(err, &ue) && ue.Err != nil {
		return ue.Err.Error()
	}
	return err.Error()
}

func (a *Agent) connectOnce(ctx context.Context) connResult {
	client, err := a.opts.Trust.HTTPClient(a.opts.Server)
	if err != nil {
		return connResult{err: err}
	}

	dialCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	ws, resp, err := websocket.Dial(dialCtx, a.opts.Server.URL, &websocket.DialOptions{
		HTTPClient:   client,
		Subprotocols: []string{termproto.SubprotocolAgent},
		// 会话输出重复度高，压缩比实测 5~7 倍、约 9 ms/MiB；Hub 不支持（旧版）时不会协商成功，自动不压缩。
		CompressionMode: websocket.CompressionContextTakeover,
		HTTPHeader:      http.Header{"User-Agent": []string{"relaymesh-agent/" + a.opts.Version}},
	})
	cancel()
	if err != nil {
		return classifyDialError(resp, err)
	}
	ws.SetReadLimit(64 << 10)

	lctx, lcancel := context.WithCancel(ctx)
	defer lcancel()

	// 握手里声明的「允许从 Web 升级」与「自动升级」状态在这里一次取定：声明与稍后监视开关变化用的是同一个值，
	// 所以握手期间开关变了也不会漏掉（监视会发现它与已声明的不同）。
	var gate upgradeGates
	if a.upg != nil {
		gate = a.opts.SelfUpgrade.gates()
	}
	w, res := a.handshake(lctx, ws, gate)
	if res != nil {
		closeQuietly(ws, websocket.StatusGoingAway, "handshake failed")
		return *res
	}

	enabled := w.Status == termproto.WelcomeEnabled
	l := newRelay(lctx, ws, a.src, enabled, a.logf)
	l.setRecords(a.recordSource())
	l.denyExec = a.opts.Exec.DenyExec
	if a.opts.SelfUninstall != nil && a.opts.Identity.Kind != termproto.KindTemporary {
		l.selfUninstall = a.opts.SelfUninstall
		l.onUninstalling = func() { a.uninstallUntil.Store(time.Now().Add(uninstallGrace).UnixNano()) }
	}
	l.upgrader = a.upg
	l.auto = a.auto
	if su := a.opts.SelfUpgrade; a.upg != nil && su.Connected != nil {
		// 握手成功就留下「上线标记」：升级进程靠它确认新版本真的连上了 Hub，没连上就回滚。
		su.Connected(a.opts.Version)
	}
	// 文件服务随这条 Hub 连接存在：连接断开时打开的文件句柄一并关闭。
	fileSvc := files.New(files.Config{URL: files.URLPolicy{AllowPrivate: a.opts.Files.AllowPrivateURLs}})
	l.setFiles(fileSvc)
	defer fileSvc.Shutdown()
	// 自动升级检查只在连接被启用之后开始（待启用期间 Hub 不回答任何业务帧）；节奏在这里取一份副本，
	// 不让检查协程去读会被测试改写的包级变量。
	timing := autoCheckDefaults
	var autoOnce sync.Once
	startAutoCheck := func() {
		if a.auto != nil {
			autoOnce.Do(func() { go a.auto.run(lctx, timing, l.sendJSON) })
		}
	}
	l.onOnline = func() {
		a.logf("已被启用，终端可用（机器码 %s）", a.code)
		a.emit(Event{State: StateOnline})
		startAutoCheck()
	}
	defer l.close()

	if enabled {
		l.setPolicy(w.Policy)
		l.start()
		startAutoCheck()
		a.logf("已连接并上线（机器码 %s）", a.code)
		a.emit(Event{State: StateOnline})
	} else {
		a.logf("已连接，等待管理员在 Web 端「接入主机」中启用。本机机器码: %s（请与网页上显示的逐位核对）", a.code)
		a.emit(Event{State: StatePending, Detail: a.code})
	}

	go l.pingLoop()

	if a.upg != nil {
		// 主机所有者随时可以用 `web-upgrade on/off`、`auto-upgrade enable/disable` 改开关：变了就主动重连，让 Hub 马上看到新的能力。
		go a.watchUpgradeGate(lctx, upgradeGateInterval, gate, func() {
			a.regate.Store(true)
			lcancel()
		})
	}

	serveDone := make(chan struct{})
	var code websocket.StatusCode
	var serveErr error
	go func() {
		code, serveErr = l.serve()
		close(serveDone)
	}()

	select {
	case <-serveDone:
		lcancel()
		closeQuietly(ws, websocket.StatusNormalClosure, "")
		if a.regate.Swap(false) {
			return connResult{welcomed: true, reconnect: true, reason: "本机的升级开关（允许从 Web 升级 / 自动升级）变了，重新连接以告知服务端"}
		}
		return connResult{code: normalizeCode(code), err: serveErr, welcomed: true}
	case <-ctx.Done():
		// 优雅退出：告知对端，随后关闭（带关闭握手，最长等待 5 秒）。
		_ = l.sendJSON(termproto.Bye{T: termproto.TBye, Reason: "agent shutting down"})
		_ = ws.Close(websocket.StatusGoingAway, "agent shutting down")
		lcancel()
		<-serveDone
		return connResult{err: ctx.Err(), welcomed: true}
	}
}

func normalizeCode(c websocket.StatusCode) websocket.StatusCode {
	if c < 0 {
		return 0
	}
	return c
}

func closeQuietly(ws *websocket.Conn, code websocket.StatusCode, reason string) {
	go func() { _ = ws.Close(code, reason) }()
}

// handshake 完成「挑战 → 应答 → welcome」。失败时返回非 nil 的结果描述。
// gate 是要在 hello 里声明的两个本地开关：「允许从 Web 升级」与「自动升级」（没有升级能力时都为空）。
func (a *Agent) handshake(ctx context.Context, ws *websocket.Conn, gate upgradeGates) (termproto.Welcome, *connResult) {
	fail := func(reason string, err error) (termproto.Welcome, *connResult) {
		var code websocket.StatusCode
		if err != nil {
			code = websocket.CloseStatus(err)
		}
		return termproto.Welcome{}, &connResult{code: normalizeCode(code), err: err, reason: reason}
	}

	hctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	typ, data, err := ws.Read(hctx)
	if err != nil {
		return fail("", err)
	}
	if typ != websocket.MessageText {
		return fail("握手失败：服务端的第一帧不是文本挑战", nil)
	}
	var ch termproto.Challenge
	if json.Unmarshal(data, &ch) != nil || ch.T != termproto.TChallenge {
		return fail("握手失败：无法识别服务端的挑战", nil)
	}
	if ch.V != termproto.ProtocolVersion {
		return fail(fmt.Sprintf("握手失败：协议版本不兼容（服务端 v%d，本 Agent v%d），请升级 Agent 或服务端", ch.V, termproto.ProtocolVersion), nil)
	}
	nonce, err := termproto.DecodeKey(ch.Nonce, termproto.NonceSize)
	if err != nil {
		return fail("握手失败：挑战随机数格式不对", nil)
	}

	id := a.opts.Identity
	meta := HostMeta(a.opts.Version, id.Kind)
	meta.Rev = a.opts.Rev
	// 只有常驻服务才可能被「同时卸载」：临时身份没有安装过任何东西。
	meta.Uninstall = a.opts.SelfUninstall != nil && id.Kind != termproto.KindTemporary
	if a.upg != nil {
		meta.WebUpgrade = gate.web
		meta.AutoUpgrade = gate.auto
		meta.AutoWindow = gate.window
		if a.opts.SelfUpgrade.Note != nil {
			meta.LastUpgrade = a.opts.SelfUpgrade.Note()
		}
	}
	hello := termproto.Hello{
		T:    termproto.THello,
		V:    termproto.ProtocolVersion,
		Pub:  termproto.EncodeKey(id.Pub),
		Sig:  termproto.EncodeKey(termproto.SignChallenge(id.Priv, nonce)),
		Meta: meta,
	}
	raw, err := termproto.Marshal(hello)
	if err != nil {
		return fail("", err)
	}
	if err := ws.Write(hctx, websocket.MessageText, raw); err != nil {
		return fail("", err)
	}

	typ, data, err = ws.Read(hctx)
	if err != nil {
		return fail("", err)
	}
	var w termproto.Welcome
	if typ != websocket.MessageText || json.Unmarshal(data, &w) != nil || w.T != termproto.TWelcome {
		return fail("握手失败：无法识别服务端的 welcome", nil)
	}
	if w.MachineCode != a.code {
		return fail(fmt.Sprintf("握手失败：服务端算出的机器码与本机不一致（本机 %s），疑似协议版本不匹配", a.code), nil)
	}
	if w.Status != termproto.WelcomeEnabled && w.Status != termproto.WelcomePending {
		return fail("握手失败：服务端返回了未知的状态", nil)
	}
	return w, nil
}

// classifyDialError 把拨号阶段的失败翻译成重试节奏与可读的原因。
func classifyDialError(resp *http.Response, err error) connResult {
	res := connResult{err: err}
	if resp == nil {
		return res
	}
	switch resp.StatusCode {
	case http.StatusForbidden:
		res.code = termproto.CloseGateClosed
		res.reason = "服务端拒绝了 Agent 入口（终端总闸关闭——例如 Web 未设置密码——或被反向代理规则拦截）"
	case http.StatusTooManyRequests:
		res.code = termproto.CloseRateLimited
		res.reason = "服务端限流，稍后重试"
	case http.StatusNotFound:
		res.code = termproto.CloseInvalidHandshake
		res.reason = "服务端没有终端入口（版本过低，或地址不是 RelayMesh 服务端）"
	}
	return res
}

// retryDelay 按失败类型决定下次重试前的等待时间。
func (a *Agent) retryDelay(res connResult, attempt *int) time.Duration {
	o := a.opts
	if res.reconnect {
		return o.Jitter(o.MinBackoff)
	}
	switch int(res.code) {
	case termproto.CloseInvalidHandshake, termproto.CloseGateClosed, termproto.CloseUnknownMachine, termproto.CloseSuperseded:
		return o.Jitter(o.SlowRetry)
	case termproto.CloseDisabled:
		return o.Jitter(o.DisabledRetry)
	case termproto.CloseRateLimited:
		return o.Jitter(o.SlowRetry + time.Duration(rand.Int64N(int64(o.SlowRetry))))
	}
	if res.reason != "" && res.code == 0 && res.err == nil {
		// 握手阶段的协议性失败（版本不兼容等）：重试没有意义，按慢节奏。
		return o.Jitter(o.SlowRetry)
	}
	d := o.MinBackoff << min(*attempt, 16)
	if d > o.MaxBackoff || d <= 0 {
		d = o.MaxBackoff
	}
	*attempt++
	return o.Jitter(d)
}
