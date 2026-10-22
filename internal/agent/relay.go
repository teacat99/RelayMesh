package agent

import (
	"context"
	"encoding/json"
	"errors"
	"math/rand/v2"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
	"github.com/teacat99/RelayMesh/v2/internal/agent/files"
	"github.com/teacat99/RelayMesh/v2/internal/agent/holder"
	"github.com/teacat99/RelayMesh/v2/internal/termproto"
)

const (
	// writeTimeout 是向 Hub / 守护进程写一帧的最长时间；超时意味着对端卡死，连接随之被放弃。
	writeTimeout = 15 * time.Second

	holderMinBackoff = 500 * time.Millisecond
	holderMaxBackoff = 15 * time.Second
	// holderSlowRetry 是被取代 / 版本不兼容这类「重试也没用」情形下的重试间隔。
	holderSlowRetry = 30 * time.Second
)

// holderSource 提供到会话守护进程的控制连接。
type holderSource interface {
	// Dial 建立一条控制端连接并完成握手。
	Dial(ctx context.Context) (*holder.Client, error)
}

// relay 是一条已完成握手的 Agent→Hub 连接上的帧中继：Hub 与会话守护进程之间近乎透明的帧代理。
//
// 它只负责这一条 Hub 连接：连接断开后 relay 随之结束并放下与守护进程的控制连接，
// 会话由守护进程持有，继续运行。守护进程暂时不可达时 relay 在后台重连，Hub 连接保持不动，
// 并把「不可用」如实告知 Hub，而不是静默退化。
type relay struct {
	ws   *websocket.Conn
	src  holderSource
	ctx  context.Context
	logf func(string, ...any)

	enabled  atomic.Bool // 待启用期间为 false，收到 approved 后为 true
	onOnline func()
	closed   atomic.Bool
	started  sync.Once

	records recordSource // 本机终端记录的来源；nil 表示不提供
	recSem  chan struct{}
	// denyExec 是 Agent 自己的一票否决（--deny-model-exec）：为 true 时不向守护进程转发任何 run 请求，
	// 也在上报给 Hub 的 holder 帧里声明被否决——即使外置守护进程本身没有设置它。
	denyExec bool
	// selfUninstall 非 nil 才响应 Hub 的卸载请求（见 Options.SelfUninstall）；onUninstalling 在卸载进程成功启动后调用。
	selfUninstall  func(purge bool) error
	onUninstalling func()
	// upgrader 非 nil 才响应 Hub 的升级请求（见 Options.SelfUpgrade）。
	upgrader *upgrader
	// auto 非 nil 才处理 Hub 对自动升级检查的回答；检查本身由 Agent 在这条连接上另起的循环发出。
	auto *autoChecker
	// holderKnown / holderDeny 记录守护进程报告的本机策略：holderDeny 为 true 表示守护进程上设置了一票否决。
	// 守护进程断开后保持最近一次的值（不会因为守护进程挂了就放开）；从未见过守护进程时 holderKnown 为 false。
	holderKnown atomic.Bool
	holderDeny  atomic.Bool

	// files 是文件能力（stat / hash / 拉取 / push_url），fileSem 限制同时受理的文件请求数。
	files   *files.Service
	fileSem chan struct{}

	mu     sync.Mutex
	hc     *holder.Client // 当前守护进程连接；nil 表示不可用
	policy *termproto.Policy
	chans  map[uint32]struct{} // 已向守护进程转发 attach、尚未结束的通道
}

func newRelay(ctx context.Context, ws *websocket.Conn, src holderSource, enabled bool, logf func(string, ...any)) *relay {
	r := &relay{ws: ws, src: src, ctx: ctx, logf: logf, chans: make(map[uint32]struct{}),
		recSem: make(chan struct{}, maxConcurrentRecordReads), fileSem: make(chan struct{}, maxConcurrentFileReqs)}
	r.enabled.Store(enabled)
	return r
}

// ---------- 向 Hub 发送 ----------

func (r *relay) write(typ websocket.MessageType, data []byte) error {
	if r.closed.Load() {
		return context.Canceled
	}
	ctx, cancel := context.WithTimeout(r.ctx, writeTimeout)
	defer cancel()
	return r.ws.Write(ctx, typ, data)
}

func (r *relay) sendJSON(v any) error {
	data, err := termproto.Marshal(v)
	if err != nil {
		return err
	}
	return r.write(websocket.MessageText, data)
}

// ---------- 向守护进程发送 ----------

func (r *relay) holderConn() *holder.Client {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.hc
}

// forward 把一帧原样转发给守护进程；守护进程不可用时返回 false。
func (r *relay) forward(typ holder.MsgType, data []byte) bool {
	hc := r.holderConn()
	if hc == nil {
		return false
	}
	ctx, cancel := context.WithTimeout(r.ctx, writeTimeout)
	defer cancel()
	if err := hc.Conn.Write(ctx, typ, data); err != nil {
		_ = hc.Conn.Close() // 半条消息无法继续使用；holderLoop 会发现并重连
		return false
	}
	return true
}

// ---------- 状态 ----------

// start 启动与守护进程的连接维护（仅在已启用之后；待启用的主机不该去占用守护进程的控制权）。
func (r *relay) start() {
	r.started.Do(func() { go r.holderLoop() })
}

func (r *relay) setPolicy(p termproto.Policy) {
	r.mu.Lock()
	cp := p
	r.policy = &cp
	r.mu.Unlock()
	r.pushPolicy()
}

func (r *relay) pushPolicy() {
	r.mu.Lock()
	p := r.policy
	r.mu.Unlock()
	if p == nil {
		return
	}
	data, err := termproto.Marshal(termproto.PolicyMsg{T: termproto.TPolicy, Policy: *p})
	if err != nil {
		return
	}
	r.forward(holder.MsgText, data)
}

func (r *relay) track(ch uint32) {
	r.mu.Lock()
	r.chans[ch] = struct{}{}
	r.mu.Unlock()
}

func (r *relay) untrack(ch uint32) {
	r.mu.Lock()
	delete(r.chans, ch)
	r.mu.Unlock()
}

// ---------- 读循环（Hub → 守护进程） ----------

// serve 阻塞处理 Hub 发来的帧，直到连接结束。返回对端的关闭码（没有则为 -1）与错误。
func (r *relay) serve() (websocket.StatusCode, error) {
	for {
		typ, data, err := r.ws.Read(r.ctx)
		if err != nil {
			return websocket.CloseStatus(err), err
		}
		if typ == websocket.MessageBinary {
			r.handleBinary(data)
			continue
		}
		if code, stop := r.handleText(data); stop {
			return code, nil
		}
	}
}

func (r *relay) handleBinary(data []byte) {
	if !r.enabled.Load() {
		return
	}
	typ, err := termproto.FrameType(data)
	if err != nil {
		return
	}
	if typ == termproto.FrameInput || typ == termproto.FrameAck {
		r.forward(holder.MsgBinary, data)
	}
}

// handleText 处理一条控制帧。返回 stop=true 表示应当结束连接（附带关闭码）。
func (r *relay) handleText(data []byte) (code websocket.StatusCode, stop bool) {
	t, err := termproto.ParseType(data)
	if err != nil {
		return 0, false
	}

	if !r.enabled.Load() {
		// 待启用期间只接受 approved / rejected；其余帧（包括策略）在启用之前没有意义。
		switch t {
		case termproto.TApproved:
			var m termproto.Approved
			if json.Unmarshal(data, &m) != nil {
				return 0, false
			}
			r.enabled.Store(true)
			r.setPolicy(m.Policy)
			r.start()
			if r.onOnline != nil {
				r.onOnline()
			}
		case termproto.TRejected:
			return termproto.CloseDisabled, true
		}
		return 0, false
	}

	switch t {
	case termproto.TOpen:
		var m termproto.Open
		if json.Unmarshal(data, &m) != nil {
			return 0, false
		}
		if !r.forward(holder.MsgText, data) {
			_ = r.sendJSON(termproto.OpenFailed{T: termproto.TOpenFailed, Req: m.Req, Reason: "session holder unavailable"})
		}
	case termproto.TSSHOp:
		// SSH 网关的一次性操作（探测 / 检查）：由会话守护进程执行；本机没有开启网关时守护进程自己会拒绝。
		// 登记 / 修改凭据的帧里含明文凭据，只做转发，绝不记录。
		var m struct {
			Req string `json:"req"`
			Op  string `json:"op"`
		}
		if json.Unmarshal(data, &m) != nil {
			return 0, false
		}
		// 经网关代理执行命令同样是「模型执行」：本机一票否决（--deny-model-exec）一并生效。
		if m.Op == termproto.SSHOpExec && r.denyExec {
			_ = r.sendJSON(termproto.SSHResult{T: termproto.TSSHResult, Req: m.Req,
				Code: termproto.SSHCodeExecDenied, Msg: "本机用 --deny-model-exec 禁止了模型执行"})
			return 0, false
		}
		if !r.forward(holder.MsgText, data) {
			_ = r.sendJSON(termproto.SSHResult{T: termproto.TSSHResult, Req: m.Req, Code: termproto.SSHCodeInternal, Msg: "会话守护进程不可用"})
		}
	case termproto.TAttach:
		var m termproto.Attach
		if json.Unmarshal(data, &m) != nil {
			return 0, false
		}
		r.track(m.Ch)
		if !r.forward(holder.MsgText, data) {
			r.untrack(m.Ch)
			_ = r.sendJSON(termproto.AttachFailed{T: termproto.TAttachFailed, Ch: m.Ch, SID: m.SID, Reason: "session holder unavailable"})
		}
	case termproto.TDetach:
		var m termproto.Detach
		if json.Unmarshal(data, &m) != nil {
			return 0, false
		}
		r.untrack(m.Ch)
		r.forward(holder.MsgText, data)
	case termproto.TResize, termproto.TKill, termproto.TRename, termproto.TLifecycle:
		r.forward(holder.MsgText, data)
	case termproto.TRun:
		var m termproto.Run
		if json.Unmarshal(data, &m) != nil {
			return 0, false
		}
		if r.denyExec {
			_ = r.sendJSON(termproto.RunFailed{T: termproto.TRunFailed, ID: m.ID, Reason: termproto.RunFailDenied, Detail: "本机用 --deny-model-exec 禁止了模型执行"})
			return 0, false
		}
		if !r.forward(holder.MsgText, data) {
			_ = r.sendJSON(termproto.RunFailed{T: termproto.TRunFailed, ID: m.ID, Reason: termproto.RunFailHolder})
		}
	case termproto.TRunCancel, termproto.TRunAck, termproto.TRunsSync:
		r.forward(holder.MsgText, data)
	case termproto.TRunRead:
		var m termproto.RunRead
		if json.Unmarshal(data, &m) != nil {
			return 0, false
		}
		if !r.forward(holder.MsgText, data) {
			_ = r.sendJSON(termproto.RunReadFail{T: termproto.TRunReadFail, Req: m.Req, Reason: termproto.RunReadHolder})
		}
	case termproto.TRecordRead:
		var m termproto.RecordRead
		if json.Unmarshal(data, &m) == nil {
			r.handleRecordRead(m)
		}
	case termproto.TRecordStat:
		var m termproto.RecordStat
		if json.Unmarshal(data, &m) == nil {
			r.handleRecordStat(m)
		}
	case termproto.TFileReq:
		var m termproto.FileReq
		if json.Unmarshal(data, &m) == nil {
			r.handleFileReq(m)
		}
	case termproto.TPolicy:
		var m termproto.PolicyMsg
		if json.Unmarshal(data, &m) == nil {
			r.setPolicy(m.Policy)
		}
	case termproto.TUninstall:
		var m termproto.Uninstall
		if json.Unmarshal(data, &m) == nil {
			r.handleUninstall(m)
		}
	case termproto.TUpgrade:
		var m termproto.Upgrade
		if json.Unmarshal(data, &m) == nil {
			r.handleUpgrade(m)
		}
	case termproto.TAutoUpgradeOffer:
		var m termproto.AutoUpgradeOffer
		if r.auto != nil && json.Unmarshal(data, &m) == nil {
			if o, ok := m.Sanitized(); ok {
				r.auto.deliver(o)
			}
		}
	case termproto.TAutoUpgradeGrant:
		var m termproto.AutoUpgradeGrant
		if r.auto != nil && json.Unmarshal(data, &m) == nil {
			if g, ok := m.Sanitized(); ok {
				r.auto.deliverGrant(g)
			}
		}
	}
	return 0, false
}

// handleUpgrade 响应 Hub 的升级请求：管理员在 Web 上点了「一键升级」。只在已启用的连接上处理。
// 下载要花几秒到几分钟，放进单独的 goroutine，不阻塞这条连接上的其他帧。
func (r *relay) handleUpgrade(m termproto.Upgrade) {
	report := func(st termproto.UpgradeStatus) { _ = r.sendJSON(st) }
	if r.upgrader == nil {
		r.logf("收到升级请求，但这个 Agent 不是以可升级的常驻服务运行，已拒绝")
		report(termproto.UpgradeStatus{T: termproto.TUpgradeStatus, ID: m.ID, Stage: termproto.UpgradeStageRefused,
			Code: termproto.UpgradeCodeNotSupported, Detail: "这个 Agent 不是以可升级的常驻服务运行"})
		return
	}
	go r.upgrader.run(r.ctx, m, report)
}

// handleUninstall 响应 Hub 的卸载请求：管理员在 Web 上删除这台主机并勾选了「同时卸载设备端」。
// 只在已启用的连接上处理（待启用期间 handleText 根本不会走到这里）。没有声明能力就忽略——Hub 本来也不会发。
func (r *relay) handleUninstall(m termproto.Uninstall) {
	if r.selfUninstall == nil {
		r.logf("收到卸载请求，但这个 Agent 不是以可卸载的常驻服务运行，已忽略")
		return
	}
	r.logf("收到管理员的卸载请求（这台主机已在 Web 上被删除），正在卸载本机的 Agent 服务…")
	if err := r.selfUninstall(m.Purge); err != nil {
		r.logf("卸载没有开始: %v；可以在本机手动执行：relaymesh-agent uninstall", err)
		return
	}
	if r.onUninstalling != nil {
		r.onUninstalling()
	}
	r.logf("卸载进程已启动，本 Agent 很快会被停止；终端录像与命令输出仍保留在本机的数据目录里")
}

// ---------- 守护进程连接的维护 ----------

func (r *relay) holderLoop() {
	backoff := holderMinBackoff
	announced := false // 已经向 Hub 报告过「不可用」
	for r.ctx.Err() == nil {
		hc, err := r.src.Dial(r.ctx)
		if err != nil {
			if r.ctx.Err() != nil {
				return
			}
			reason, wait := classifyHolderError(err)
			if !announced {
				r.logf("会话守护进程不可用: %v", err)
				_ = r.sendJSON(termproto.Holder{T: termproto.THolder, State: termproto.HolderUnavailable, Reason: reason})
				announced = true
			}
			if wait == 0 {
				wait = backoff
				backoff = min(backoff*2, holderMaxBackoff)
			}
			r.sleep(withJitter(wait))
			continue
		}
		backoff = holderMinBackoff
		announced = false

		r.attachHolder(hc)
		err = r.pump(hc)
		r.detachHolder(hc)
		if r.ctx.Err() != nil {
			return
		}
		var bye *holder.ByeError
		switch {
		case errors.As(err, &bye) && bye.Reason == holder.ByeSuperseded:
			r.logf("会话守护进程的控制连接被另一个 Agent 实例取代（是否启动了两个 Agent？）")
			_ = r.sendJSON(termproto.Holder{T: termproto.THolder, State: termproto.HolderUnavailable, Reason: termproto.HolderReasonSuperseded})
			announced = true
			r.sleep(withJitter(holderSlowRetry))
		case errors.As(err, &bye) && bye.Reason == holder.ByeShutdown:
			r.logf("会话守护进程正在关停")
			_ = r.sendJSON(termproto.Holder{T: termproto.THolder, State: termproto.HolderUnavailable, Reason: termproto.HolderReasonUnreachable})
			announced = true
			r.sleep(withJitter(backoff))
		default:
			r.logf("与会话守护进程的连接已断开: %v", err)
			_ = r.sendJSON(termproto.Holder{T: termproto.THolder, State: termproto.HolderUnavailable, Reason: termproto.HolderReasonUnreachable})
			announced = true
			r.sleep(withJitter(holderMinBackoff))
		}
	}
}

func classifyHolderError(err error) (reason string, wait time.Duration) {
	var bye *holder.ByeError
	switch {
	case errors.As(err, &bye) && bye.Reason == holder.ByeVersion:
		return termproto.HolderReasonVersion, holderSlowRetry
	case errors.As(err, &bye) && bye.Reason == holder.ByeSuperseded:
		return termproto.HolderReasonSuperseded, holderSlowRetry
	}
	return termproto.HolderReasonUnreachable, 0
}

func withJitter(d time.Duration) time.Duration {
	return time.Duration(float64(d) * (0.8 + 0.4*rand.Float64()))
}

func (r *relay) sleep(d time.Duration) {
	select {
	case <-r.ctx.Done():
	case <-time.After(d):
	}
}

// attachHolder 登记新的守护进程连接，向 Hub 报告其状态与（若有）重启后丢失的会话，并补发最近一次策略。
func (r *relay) attachHolder(hc *holder.Client) {
	r.mu.Lock()
	r.hc = hc
	r.mu.Unlock()

	h := hc.Hello
	r.holderDeny.Store(h.DenyExec)
	r.holderKnown.Store(true)
	r.logf("已连接会话守护进程（版本 %s，%s）", h.Holder, persistenceLabel(h.Persistent))
	msg, dropped := holderReadyFrame(h)
	if r.denyExec {
		msg.DenyExec = true
	}
	if dropped > 0 {
		r.logf("丢失会话记录过多，本次只上报最近的 %d 条（另有 %d 条未上报）", len(msg.Lost), dropped)
	}
	_ = r.sendJSON(msg)
	r.pushPolicy()
	go r.holderPing(hc)
}

// maxHolderFrame 是 holder 帧序列化后允许的最大字节数：Hub 对在线连接的读上限是 64 KiB，
// 超过会被直接断开，所以这里留足余量。
const maxHolderFrame = 40 << 10

// holderReadyFrame 把守护进程的 hello 转成上报给 Hub 的 holder 帧。
// 全部文本先净化并限长；丢失会话按时间从新到旧保留，直到帧装不下为止。返回被舍弃的条数。
func holderReadyFrame(h holder.IPCHello) (termproto.Holder, int) {
	msg := termproto.Holder{
		T:          termproto.THolder,
		State:      termproto.HolderReady,
		Persistent: h.Persistent,
		Version:    h.Holder,
		Rev:        h.Rev,
		BootID:     h.BootID,
		Started:    h.Started,
		Caps:       h.Caps,
		DenyExec:   h.DenyExec,
		Exec:       h.Exec,
	}
	for _, l := range h.Lost {
		msg.Lost = append(msg.Lost, termproto.LostSession{
			SID: l.SID, Name: l.Name, Cwd: l.Cwd, Created: l.Created, Reason: l.Reason, At: l.At,
		})
	}
	msg = msg.Sanitized()
	total := len(msg.Lost)
	sort.SliceStable(msg.Lost, func(i, j int) bool { return msg.Lost[i].At > msg.Lost[j].At })
	for len(msg.Lost) > 0 {
		if data, err := termproto.Marshal(msg); err == nil && len(data) <= maxHolderFrame {
			break
		}
		msg.Lost = msg.Lost[:len(msg.Lost)-1]
	}
	return msg, total - len(msg.Lost)
}

func persistenceLabel(persistent bool) string {
	if persistent {
		return "会话可挺过 Agent 重启"
	}
	return "内嵌模式：会话随 Agent 进程结束"
}

// detachHolder 放下守护进程连接，并对尚未结束的通道补发 detached，让 Hub 知道它们不再有数据。
func (r *relay) detachHolder(hc *holder.Client) {
	_ = hc.Conn.Close()
	r.mu.Lock()
	if r.hc == hc {
		r.hc = nil
	}
	left := make([]uint32, 0, len(r.chans))
	for ch := range r.chans {
		left = append(left, ch)
	}
	r.chans = make(map[uint32]struct{})
	r.mu.Unlock()
	for _, ch := range left {
		_ = r.sendJSON(termproto.Detached{T: termproto.TDetached, Ch: ch, Reason: "holder_unavailable"})
	}
}

func (r *relay) holderPing(hc *holder.Client) {
	t := time.NewTicker(termproto.PingInterval)
	defer t.Stop()
	for {
		select {
		case <-r.ctx.Done():
			return
		case <-t.C:
			if r.holderConn() != hc {
				return
			}
			ctx, cancel := context.WithTimeout(r.ctx, termproto.PongTimeout)
			err := hc.Conn.Ping(ctx)
			cancel()
			if err != nil {
				_ = hc.Conn.Close()
				return
			}
		}
	}
}

// pump 把守护进程发来的帧转发给 Hub，直到这条本地连接结束。
func (r *relay) pump(hc *holder.Client) error {
	for {
		typ, data, err := hc.Conn.Read(r.ctx)
		if err != nil {
			return err
		}
		if typ == holder.MsgBinary {
			if r.enabled.Load() {
				if err := r.write(websocket.MessageBinary, data); err != nil && r.ctx.Err() != nil {
					return r.ctx.Err()
				}
			}
			continue
		}
		t, perr := termproto.ParseType(data)
		if perr != nil {
			continue
		}
		switch t {
		case holder.TIPCBye:
			var b holder.IPCBye
			_ = json.Unmarshal(data, &b)
			return &holder.ByeError{Reason: b.Reason}
		case termproto.TDetached, termproto.TAttachFailed:
			var m struct {
				Ch uint32 `json:"ch"`
			}
			if json.Unmarshal(data, &m) == nil {
				r.untrack(m.Ch)
			}
			r.forwardToHub(data)
		case termproto.TSessions, termproto.TOpened, termproto.TOpenFailed, termproto.TAttached, termproto.TExit, termproto.TSSHResult:
			r.forwardToHub(data)
		case termproto.TRunStarted, termproto.TRunFailed, termproto.TRunEnd, termproto.TRuns, termproto.TRunReadFail:
			r.forwardToHub(data)
		}
	}
}

func (r *relay) forwardToHub(data []byte) {
	if !r.enabled.Load() {
		return
	}
	_ = r.write(websocket.MessageText, data)
}

// close 放下守护进程连接（会话继续运行）。可重复调用。
func (r *relay) close() {
	r.closed.Store(true)
	r.mu.Lock()
	hc := r.hc
	r.hc = nil
	r.mu.Unlock()
	if hc != nil {
		_ = hc.Conn.Close()
	}
}

// pingLoop 周期性发送 WebSocket ping；超时无 pong 视为断链（上下文取消会关闭连接，读循环随之退出）。
func (r *relay) pingLoop() {
	ticker := time.NewTicker(termproto.PingInterval)
	defer ticker.Stop()
	for {
		select {
		case <-r.ctx.Done():
			return
		case <-ticker.C:
			ctx, cancel := context.WithTimeout(r.ctx, termproto.PongTimeout)
			err := r.ws.Ping(ctx)
			cancel()
			if err != nil {
				return
			}
		}
	}
}
