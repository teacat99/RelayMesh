package terminal

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/gin-gonic/gin"
	"github.com/teacat99/RelayMesh/v2/internal/store"
	"github.com/teacat99/RelayMesh/v2/internal/termproto"
)

// ---------- 预认证限流 ----------

// 预认证阶段（尚未证明身份的连接）的速率上限：每个来源地址每分钟允许新建的握手数。
// 并发上限见 termproto.MaxPreAuthConns / MaxPreAuthPerIP。
const preAuthRatePerMinute = 60

type rateWindow struct {
	start time.Time
	n     int
}

// preAuthLimiter 限制未认证连接对服务端资源的占用：全局与单 IP 并发、单 IP 新建速率。
// 通用入口对任何人开放，这是它不被廉价 DDoS 的第一道防线（第二道是 8 KiB 首帧上限与 10 秒握手期限）。
type preAuthLimiter struct {
	mu    sync.Mutex
	now   func() time.Time
	total int
	perIP map[string]int
	rate  map[string]*rateWindow
}

func newPreAuthLimiter(now func() time.Time) *preAuthLimiter {
	return &preAuthLimiter{now: now, perIP: make(map[string]int), rate: make(map[string]*rateWindow)}
}

// acquire 尝试占用一个预认证名额；任一上限被触及即拒绝且不占用。
func (l *preAuthLimiter) acquire(ip string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := l.now()
	if len(l.rate) > 4096 {
		for k, w := range l.rate {
			if now.Sub(w.start) >= time.Minute {
				delete(l.rate, k)
			}
		}
	}
	w := l.rate[ip]
	if w == nil || now.Sub(w.start) >= time.Minute {
		w = &rateWindow{start: now}
		l.rate[ip] = w
	}
	if w.n >= preAuthRatePerMinute || l.total >= termproto.MaxPreAuthConns || l.perIP[ip] >= termproto.MaxPreAuthPerIP {
		return false
	}
	w.n++
	l.total++
	l.perIP[ip]++
	return true
}

func (l *preAuthLimiter) release(ip string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.total > 0 {
		l.total--
	}
	if n := l.perIP[ip]; n <= 1 {
		delete(l.perIP, ip)
	} else {
		l.perIP[ip] = n - 1
	}
}

// ---------- Agent 入口 ----------

var errHandshake = errors.New("invalid handshake")

// closeAsync 异步关闭连接：握手失败的对端可能永远不回应，不能让处理协程（和预认证名额）为此被占住。
func closeAsync(ws *websocket.Conn, code websocket.StatusCode, reason string) {
	go func() { _ = ws.Close(code, reason) }()
}

func (h *Hub) policy() termproto.Policy {
	// 不再下发固定的「断开多久回收」：生命周期由主机默认（Policy.IdleSec，policyFor 填入）与会话各自的设置决定。
	return termproto.Policy{
		Terminal:    true,
		MaxSessions: termproto.MaxSessionsPerHost,
	}
}

// policyFor 是下发给某台主机 Agent 的完整策略：基础策略 + 记录策略。
// 记录策略只会让主机本机配置更严格：全局关闭、保留天数与大小上限、以及该主机被管理员单独关闭记录。
func (h *Hub) policyFor(ctx context.Context, code string) termproto.Policy {
	p := h.policy()
	p.RecOff = h.opts.RecordOff
	p.RecRetentionDays = h.opts.RecordRetentionDays
	p.RecSessionMB = h.opts.RecordSessionMB
	p.RecHostMB = h.opts.RecordHostMB
	p.RecMaxLines = h.termPrefs(ctx).RecordMaxLines
	if rec, err := h.store.GetTerminalHost(ctx, code); err == nil && rec != nil {
		if rec.RecOff {
			p.RecOff = true
		}
		p.IdleSec = termproto.ClampIdle(rec.IdleSec)
	}
	return p
}

// handleAgentWS 是 Agent 的通用入口 GET /api/v1/terminal/agent/ws。
//
// 该入口不要求任何事先配置的凭据（机器身份由私钥签名证明），因此所有防护都在「证明身份之前」就位：
// 总闸 → 拒绝任何带 Origin 的请求（浏览器页面不得冒充 Agent）→ 预认证并发/速率上限 → 8 KiB 首帧 → 10 秒握手期限 →
// 挑战应答验签 → 按主机状态分流（已启用 / 临时启用 / 发现窗口内的待启用 / 其余一律拒绝）。
func (h *Hub) handleAgentWS(c *gin.Context) {
	r := c.Request
	ip := c.ClientIP()

	if !h.gate.Evaluate(r.Context()).Open {
		c.AbortWithStatus(http.StatusForbidden)
		return
	}
	if r.Header.Get("Origin") != "" {
		c.AbortWithStatus(http.StatusForbidden)
		return
	}
	release, tracked := h.track()
	if !tracked {
		c.AbortWithStatus(http.StatusServiceUnavailable)
		return
	}
	defer release()

	if !h.pre.acquire(ip) {
		c.AbortWithStatus(http.StatusTooManyRequests)
		return
	}
	slotHeld := true
	releaseSlot := func() {
		if slotHeld {
			slotHeld = false
			h.pre.release(ip)
		}
	}
	defer releaseSlot()

	ws, err := websocket.Accept(c.Writer, r, &websocket.AcceptOptions{
		Subprotocols:       []string{termproto.SubprotocolAgent},
		InsecureSkipVerify: true, // 上面已拒绝任何带 Origin 的请求
		// 与浏览器那条链路同理（见 attach.go）：Agent 把会话输出推给 Hub，小带宽 / 上行有限时压缩能缩短到约五分之一。
		// 由 Agent 在拨号时提出，旧版 Agent 不提就不会启用，所以新旧版本互相兼容。读取上限按解压后的字节数算，
		// 认证前对端发来的压缩帧同样受 MaxPreAuthFrame 约束（库里 limitReader 包在解压器外面），不会有解压炸弹。
		CompressionMode: websocket.CompressionContextTakeover,
	})
	if err != nil {
		return
	}
	ws.SetReadLimit(termproto.MaxPreAuthFrame)

	pub, meta, nonceOK := h.challengeAndHello(ws)
	if !nonceOK {
		h.audit.recordLimited(h.ctx, ip, auditEvent{Event: EvAgentHandshakeBad, Result: ResultFail, IP: ip})
		closeAsync(ws, termproto.CloseInvalidHandshake, "invalid handshake")
		return
	}

	code := termproto.MachineCode(pub)
	pubB64 := termproto.EncodeKey(pub)
	now := h.now()

	rec, err := h.store.GetTerminalHost(h.ctx, code)
	if err != nil {
		log.Printf("[terminal] 读取主机记录失败: %v", err)
		closeAsync(ws, websocket.StatusInternalError, "internal error")
		return
	}

	switch {
	case rec != nil && rec.PublicKey != pubB64:
		// 机器码由公钥派生，同一机器码对应不同公钥只可能是数据损坏或恶意构造：一律拒绝。
		h.audit.recordLimited(h.ctx, code, auditEvent{Event: EvAgentHandshakeBad, Result: ResultDeny, IP: ip, MachineCode: code, Detail: "public key mismatch"})
		closeAsync(ws, termproto.CloseDisabled, "rejected")
	case rec != nil && rec.Status == termproto.HostStatusEnabled:
		h.bringOnline(ws, code, pub, meta, ip, releaseSlot)
	case rec != nil:
		closeAsync(ws, termproto.CloseDisabled, "host disabled")
	default:
		if _, ok := h.temps.lookup(code, pub, now); ok {
			h.bringOnline(ws, code, pub, meta, ip, releaseSlot)
			return
		}
		if !h.disc.active(now) {
			closeAsync(ws, termproto.CloseUnknownMachine, "unknown machine")
			return
		}
		h.enterPending(ws, code, pub, meta, ip, releaseSlot)
	}
}

// challengeAndHello 发出挑战并读取、验证 Hello。返回的 meta 已净化。
func (h *Hub) challengeAndHello(ws *websocket.Conn) (ed25519.PublicKey, termproto.Meta, bool) {
	nonce, err := termproto.NewNonce()
	if err != nil {
		return nil, termproto.Meta{}, false
	}
	chal, err := termproto.Marshal(termproto.Challenge{T: termproto.TChallenge, V: termproto.ProtocolVersion, Nonce: termproto.EncodeKey(nonce)})
	if err != nil {
		return nil, termproto.Meta{}, false
	}

	ctx, cancel := context.WithTimeout(h.ctx, h.opts.HandshakeTimeout)
	defer cancel()
	if err := ws.Write(ctx, websocket.MessageText, chal); err != nil {
		return nil, termproto.Meta{}, false
	}
	typ, data, err := ws.Read(ctx)
	if err != nil || typ != websocket.MessageText {
		return nil, termproto.Meta{}, false
	}
	var hello termproto.Hello
	if json.Unmarshal(data, &hello) != nil || hello.T != termproto.THello {
		return nil, termproto.Meta{}, false
	}
	pub, sig, err := hello.Decode()
	if err != nil {
		return nil, termproto.Meta{}, false
	}
	if !termproto.VerifyChallenge(pub, nonce, sig) {
		return nil, termproto.Meta{}, false
	}
	return pub, hello.Meta.Sanitized(), true
}

// bringOnline 让一个已启用（持久或临时）的 Agent 上线并进入读循环。
func (h *Hub) bringOnline(ws *websocket.Conn, code string, pub ed25519.PublicKey, meta termproto.Meta, ip string, releaseSlot func()) {
	a := newAgentConn(h, ws, code, pub, meta, ip)
	a.enabled = true
	onClose := func() {
		h.unregisterAgent(a)
		if a.temp {
			h.temps.setOnline(code, false, nil, "", h.now())
		}
	}

	welcome := termproto.Welcome{T: termproto.TWelcome, Status: termproto.WelcomeEnabled, MachineCode: code, Policy: h.policyFor(h.ctx, code)}
	if err := a.writeJSON(welcome); err != nil {
		a.cleanup(onClose)
		return
	}
	ws.SetReadLimit(termproto.MaxControlFrame)
	releaseSlot()

	now := h.now()
	if a.temp {
		if !h.temps.setOnline(code, true, &meta, ip, now) {
			// 临时启用在握手与上线之间过期：按未知主机处理。
			a.closeWith(termproto.CloseUnknownMachine, "temporary enablement expired")
			a.cleanup(onClose)
			return
		}
	} else if err := h.store.TouchTerminalHost(h.ctx, code, store.TerminalHostSeen{
		Hostname: meta.Host, OS: meta.OS, Arch: meta.Arch, AgentVersion: meta.Ver, AgentRev: meta.Rev, Privilege: meta.Priv, IP: ip, At: now,
	}); err != nil && !errors.Is(err, store.ErrTerminalHostNotFound) {
		log.Printf("[terminal] 刷新主机在线信息失败 (%s): %v", code, err)
	}
	if !a.temp {
		// 升级窗口开着时，主机带着不同的版本回来了：升级已经完成，窗口随之关闭；
		// 在 Web 上发起的升级请求也在这里收尾（版本变了 → 完成；版本没变但带回了回滚 / 失败记录 → 失败）。
		h.upgradeSeen(code, meta)
	}

	if old := h.registerAgent(a); old != nil {
		old.closeWith(termproto.CloseSuperseded, "superseded by a newer connection")
	}
	// 注册之后再复核一次：若 Hub 此刻已开始关闭或总闸已翻转，不让这个连接漏网。
	if h.isGateClosedNow() {
		a.closeWith(termproto.CloseGateClosed, "terminal disabled")
	}
	h.audit.recordLimited(h.ctx, code, auditEvent{Event: EvAgentConnected, IP: ip, MachineCode: code, Detail: meta.Host})
	a.serve(onClose)
}

// enterPending 让发现窗口内的未知 Agent 进入待启用列表，并保持连接等待管理员处理。
func (h *Hub) enterPending(ws *websocket.Conn, code string, pub ed25519.PublicKey, meta termproto.Meta, ip string, releaseSlot func()) {
	a := newAgentConn(h, ws, code, pub, meta, ip)
	entry := &pendingEntry{Code: code, PubKey: pub, Meta: meta, IP: ip, agent: a}
	created, superseded, err := h.pending.add(entry)
	if err != nil {
		closeAsync(ws, termproto.CloseRateLimited, "pending list full")
		return
	}
	if superseded != nil {
		superseded.closeWith(termproto.CloseSuperseded, "superseded by a newer connection")
	}
	onClose := func() {
		h.pending.markOffline(code, a)
		h.unregisterAgent(a) // 连接在被启用后才断开时，它已被登记为在线 Agent
		if a.temp {
			h.temps.setOnline(code, false, nil, "", h.now())
		}
	}

	welcome := termproto.Welcome{T: termproto.TWelcome, Status: termproto.WelcomePending, MachineCode: code, Policy: termproto.Policy{Terminal: true}}
	if err := a.writeJSON(welcome); err != nil {
		a.cleanup(onClose)
		return
	}
	releaseSlot()
	if created {
		h.audit.recordLimited(h.ctx, ip, auditEvent{Event: EvHostPending, IP: ip, MachineCode: code, Detail: meta.Host})
	}
	a.serve(onClose)
}

func (h *Hub) isGateClosedNow() bool {
	return !h.gate.Evaluate(h.ctx).Open
}
