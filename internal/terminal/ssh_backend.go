package terminal

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/teacat99/RelayMesh/v2/internal/config"
	"github.com/teacat99/RelayMesh/v2/internal/model"
	"github.com/teacat99/RelayMesh/v2/internal/secure"
	"github.com/teacat99/RelayMesh/v2/internal/sshexec"
	"github.com/teacat99/RelayMesh/v2/internal/store"
	"github.com/teacat99/RelayMesh/v2/internal/termexec"
	"github.com/teacat99/RelayMesh/v2/internal/termproto"
)

// SSH 后端（M10）：为装不了 Agent 的主机提供与 Agent 主机同一接口的第二个执行后端。
//
// 不变量：
//   - 连接地址、用户名与凭据只来自数据库；模型只能给出 target，不能指定任何地址；
//   - 凭据用独立的主密钥（RELAYMESH_SSH_SECRET_KEY）密封保存，永不出现在响应、日志、审计与事件流里；
//   - 主机密钥严格固定：只有管理员在界面上核对过指纹才会固定，之后每次连接都必须逐字节一致，
//     不一致即拒绝并置 mismatch，**绝不自动更新**；
//   - 主密钥未配置或过弱 ⇒ 整块功能关闭（已登记的 SSH 主机显示为被阻断，不报错）；
//   - 完整输出只保存在远端任务目录里，Hub 数据库不保存输出块。

const (
	sshCallTimeout   = 25 * time.Second // 单次远端调用（含建连）的上限
	sshProbeTimeout  = 12 * time.Second
	sshCooldown      = 20 * time.Second // 一次失败之后的快速失败窗口：避免对不可达的主机反复重试
	sshSpecTTL       = 30 * time.Second
	sshHealthPersist = time.Minute
	sshSweepEvery    = 6 * time.Hour
	sshRetentionDays = 7
	sshMgmtSlots     = 4
	sshReapEvery     = 30 * time.Second
)

// SSHStatus 是 SSH 后端的启用状态。
type SSHStatus struct {
	Enabled bool   `json:"enabled"`
	Reason  string `json:"reason,omitempty"` // no_secret_key | weak_secret_key
	Message string `json:"message,omitempty"`
	Max     int    `json:"max_hosts"`
}

// SSHView 是主机视图里 SSH 相关的部分：不含任何凭据。
type SSHView struct {
	Address     string     `json:"address"`
	User        string     `json:"user"`
	Auth        string     `json:"auth"`
	KeyType     string     `json:"key_type"`
	Fingerprint string     `json:"fingerprint"`
	KeyState    string     `json:"key_state,omitempty"` // mismatch：出现过密钥不一致，需要管理员重新确认指纹
	HasSecret   bool       `json:"has_secret"`
	Error       string     `json:"error,omitempty"`
	CheckedAt   *time.Time `json:"checked_at,omitempty"`
	OKAt        *time.Time `json:"ok_at,omitempty"`
	Backend     bool       `json:"backend_enabled"`
	// Gateway 非空表示经网关主机（机器码）接入：由网关出站连接目标并保存登录凭据；GatewayReady 是网关此刻是否在线且开启了网关能力。
	Gateway      string `json:"gateway,omitempty"`
	GatewayReady bool   `json:"gateway_ready,omitempty"`
	// CredOnGateway 表示登录凭据保存在网关自己的存储里（Hub 没有副本，也就不需要 SSH 主密钥）；
	// CredMissing 表示已确认网关上没有这台主机的凭据，需要管理员重新填写。
	CredOnGateway bool `json:"cred_on_gateway,omitempty"`
	CredMissing   bool `json:"cred_missing,omitempty"`
	// TermOnly 表示这台经网关接入的主机登记时探测不出 POSIX 环境，只能当人工终端用。
	TermOnly bool `json:"term_only,omitempty"`
	// GatewayDenied 表示承载这台主机的网关在本机用 --deny-model-exec 一票否决了模型执行（服务端无法更改）。
	GatewayDenied bool `json:"gateway_denied,omitempty"`
}

// SSHError 是管理类操作的可映射错误（REST 层据此给出状态码与稳定的错误码）。
type SSHError struct {
	Status  int
	Code    string
	Message string
}

func (e *SSHError) Error() string { return e.Code + ": " + e.Message }

func sshErr(status int, code, format string, args ...any) *SSHError {
	return &SSHError{Status: status, Code: code, Message: fmt.Sprintf(format, args...)}
}

// mapSSHErr 把传输层错误翻译成稳定的管理错误码。消息只含类别与（已净化的）网络原因，不含任何凭据。
func mapSSHErr(err error) *SSHError {
	var se *SSHError
	switch {
	case errors.As(err, &se):
		return se
	case errors.Is(err, sshexec.ErrHostKeyMismatch):
		return sshErr(http.StatusConflict, "ssh_host_key_mismatch", "主机密钥与已固定的不一致：已停止连接，需要核对指纹后重新确认")
	case errors.Is(err, sshexec.ErrNoHostKey):
		return sshErr(http.StatusConflict, termexec.CodeSSHUntrusted, "这台主机还没有固定的主机密钥")
	case errors.Is(err, sshexec.ErrAuthFailed):
		return sshErr(http.StatusUnprocessableEntity, "ssh_auth_failed", "SSH 认证失败：请检查用户名与凭据")
	case errors.Is(err, sshexec.ErrUnsupported):
		return sshErr(http.StatusUnprocessableEntity, "ssh_unsupported", "%s", err.Error())
	case errors.Is(err, sshexec.ErrPassphrase):
		return sshErr(http.StatusBadRequest, "ssh_passphrase_required", "私钥已加密，请同时提供口令")
	case errors.Is(err, sshexec.ErrBadCredential), errors.Is(err, sshexec.ErrBadAddress):
		return sshErr(http.StatusBadRequest, "bad_request", "%s", err.Error())
	case errors.Is(err, sshexec.ErrUnreachable), errors.Is(err, sshexec.ErrConn), errors.Is(err, sshexec.ErrTooLarge),
		errors.Is(err, sshexec.ErrProtocol), errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
		return sshErr(http.StatusBadGateway, "ssh_unreachable", "%s", unreachableText(err))
	}
	return sshErr(http.StatusInternalServerError, "internal_error", "操作失败，请查看服务端日志")
}

func unreachableText(err error) string {
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return "连接超时"
	case errors.Is(err, context.Canceled):
		return "连接已取消"
	}
	return err.Error()
}

type specEntry struct {
	spec    sshexec.Spec
	rec     model.TerminalHost
	expires time.Time
}

// routeEntry 缓存一台 SSH 主机的接入路径（承载网关的机器码，空串即 Hub 直连），避免每次门控都读库。
type routeEntry struct {
	via     string
	ok      bool // 是一台 SSH 主机
	expires time.Time
}

type hostHealth struct {
	failAt    time.Time
	okAt      time.Time
	lastErr   string
	persisted time.Time
	persistOK bool
}

// sshBackend 是 Hub 里 SSH 后端的全部状态。零值之外只有 newSSHBackend 能创建。
type sshBackend struct {
	h      *Hub
	sealer *secure.Sealer
	reason string
	pool   *sshexec.Pool

	mu       sync.Mutex
	peers    map[string]*sshPeer
	specs    map[string]specEntry
	routes   map[string]routeEntry
	health   map[string]*hostHealth
	facts    map[string]sshexec.Facts
	factsTry map[string]time.Time
	swept    map[string]time.Time
	reapAt   time.Time
	// credLost 记录已确认网关上没有登录凭据的 SSH 主机（机器码）。只在内存里：它来自网关的 cred_sync 回报与 no_credential 应答，
	// 重启之后网关重新连上时会再对账一次。
	credLost map[string]bool

	// credMu 串行化「在网关上保存凭据 → 主机记录落库」与 cred_sync 对账：登记 / 修改凭据持读锁，对账持写锁。
	// 没有它，对账可能恰好发生在凭据已保存、主机记录尚未落库之间，把刚保存的凭据当成「名单之外」回收掉。
	// 持锁的流程各自只加一次读锁（再次加读锁遇到等待中的写锁会死锁）。
	credMu sync.RWMutex

	mgmt chan struct{}
}

func newSSHBackend(h *Hub, key string) *sshBackend {
	b := &sshBackend{
		h:        h,
		pool:     &sshexec.Pool{DialTimeout: sshexec.DefaultDialTimeout},
		peers:    make(map[string]*sshPeer),
		specs:    make(map[string]specEntry),
		routes:   make(map[string]routeEntry),
		health:   make(map[string]*hostHealth),
		facts:    make(map[string]sshexec.Facts),
		factsTry: make(map[string]time.Time),
		swept:    make(map[string]time.Time),
		credLost: make(map[string]bool),
		mgmt:     make(chan struct{}, sshMgmtSlots),
	}
	switch {
	case key == "":
		b.reason = "no_secret_key"
	case config.SSHSecretKeyWeakness(key) != "":
		b.reason = "weak_secret_key"
	default:
		s, err := secure.NewSealer(key, "ssh-credentials")
		if err != nil {
			b.reason = "no_secret_key"
		} else {
			b.sealer = s
		}
	}
	return b
}

func (b *sshBackend) enabled() bool { return b != nil && b.sealer != nil }

func (b *sshBackend) status() SSHStatus {
	st := SSHStatus{Enabled: b.enabled(), Max: store.MaxSSHHosts}
	switch b.reason {
	case "no_secret_key":
		st.Reason, st.Message = b.reason, "服务端没有设置 RELAYMESH_SSH_SECRET_KEY：SSH 主机功能保持关闭（凭据必须用独立的密钥加密保存）"
	case "weak_secret_key":
		st.Reason, st.Message = b.reason, "RELAYMESH_SSH_SECRET_KEY 强度不足（长度、随机性不够或疑似占位符）：SSH 主机功能保持关闭"
	}
	return st
}

func (b *sshBackend) needEnabled() *SSHError {
	if b.enabled() {
		return nil
	}
	return sshErr(http.StatusConflict, termexec.CodeSSHOff, "%s", b.status().Message)
}

// needFor 检查某条接入路径的前置条件：直连需要 SSH 主密钥（凭据由 Hub 密封保存）；
// 经网关接入的主机凭据只在网关自己的存储里，Hub 不需要主密钥。
func (b *sshBackend) needFor(gateway string) *SSHError {
	if gateway != "" {
		return nil
	}
	return b.needEnabled()
}

// routeOf 返回一台 SSH 主机的接入路径：via 是承载网关的机器码（空串即 Hub 直连），ok=false 表示它不是（或已不是）SSH 主机。
// 结果短时缓存；invalidate / forget 会清掉它，所以改接入路径之后立即生效。
func (b *sshBackend) routeOf(code string) (via string, ok bool) {
	now := b.h.now()
	b.mu.Lock()
	if e, hit := b.routes[code]; hit && now.Before(e.expires) {
		b.mu.Unlock()
		return e.via, e.ok
	}
	b.mu.Unlock()
	rec, err := b.h.store.GetTerminalHost(b.h.ctx, code)
	if err != nil {
		return "", false // 读库失败：宁可当作不可用，也不要缓存一个错误的结论
	}
	e := routeEntry{expires: now.Add(sshSpecTTL)}
	if rec != nil && rec.IsSSH() {
		e.via, e.ok = rec.SSHGateway, true
	}
	b.mu.Lock()
	b.routes[code] = e
	b.mu.Unlock()
	return e.via, e.ok
}

// credGone 报告是否已确认网关上没有这台主机的登录凭据。
func (b *sshBackend) credGone(code string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.credLost[code]
}

func (b *sshBackend) setCredLost(code string, lost bool) {
	b.mu.Lock()
	if lost {
		b.credLost[code] = true
	} else {
		delete(b.credLost, code)
	}
	b.mu.Unlock()
}

// ---------- 执行层与闸门 ----------

// peerOf 返回某台 SSH 主机的执行层；不是 SSH 主机时返回 nil（真正的 nil 接口）。
func (b *sshBackend) peerOf(machine string) execPeer {
	if b == nil || machine == "" {
		return nil
	}
	b.mu.Lock()
	p := b.peers[machine]
	b.mu.Unlock()
	if p != nil {
		return p
	}
	rec, err := b.h.store.GetTerminalHost(b.h.ctx, machine)
	if err != nil || rec == nil || !rec.IsSSH() {
		return nil
	}
	return b.ensurePeer(machine)
}

func (b *sshBackend) ensurePeer(machine string) *sshPeer {
	b.mu.Lock()
	defer b.mu.Unlock()
	p := b.peers[machine]
	if p == nil {
		p = &sshPeer{b: b, code: machine, runs: make(map[string]*sshRun)}
		b.peers[machine] = p
	}
	return p
}

// block 是 SSH 主机特有的闸门（G2 / G5 之后）：后端是否启用、主机密钥是否可信、最近是否连不上。
// 没有 G4：SSH 主机没有「本机一票否决」，它完全由管理员在 Hub 上管理，所以默认不允许模型访问。
func (b *sshBackend) block(rec model.TerminalHost) string {
	if rec.ViaGateway() {
		return b.gatewayBlock(rec)
	}
	switch {
	case !b.enabled():
		return termexec.CodeSSHOff
	case rec.SSHKeyState == model.SSHKeyMismatch, rec.SSHHostKey == "", rec.SSHAddr == "":
		return termexec.CodeSSHUntrusted
	case b.cooling(rec.MachineCode):
		return termexec.CodeHostOffline
	}
	return ""
}

// gatewayBlock 是经网关接入的 SSH 主机的闸门：只能当终端用、主机密钥是否可信、网关上是否还有凭据、
// 网关是否在线且具备凭据库与 exec 代理能力、网关本机是否一票否决了模型执行，最后才是目标最近是否连不上。
func (b *sshBackend) gatewayBlock(rec model.TerminalHost) string {
	switch {
	case rec.SSHTermOnly:
		return termexec.CodeSSHTerminalOnly
	case rec.SSHKeyState == model.SSHKeyMismatch, rec.SSHHostKey == "", rec.SSHAddr == "":
		return termexec.CodeSSHUntrusted
	case b.credGone(rec.MachineCode):
		return termexec.CodeSSHCredMissing
	}
	ready, denied := b.h.gatewayExecState(rec.SSHGateway)
	switch {
	case !ready:
		return termexec.CodeHostOffline
	case denied:
		return termexec.CodeHostDenied
	case b.cooling(rec.MachineCode):
		return termexec.CodeHostOffline
	}
	return ""
}

func (b *sshBackend) cooling(code string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	h := b.health[code]
	return h != nil && !h.failAt.IsZero() && h.failAt.After(h.okAt) && b.h.now().Sub(h.failAt) < sshCooldown
}

// reachable 报告最近一次连接尝试没有失败（从未尝试过也算可达：第一次使用时会真正去连）。
func (b *sshBackend) reachable(rec model.TerminalHost) bool {
	return rec.SSHError == "" && !b.cooling(rec.MachineCode)
}

// activeOn 返回某台主机上进行中的运行数（用于并发保护）。
func (b *sshBackend) activeOn(code string) int {
	b.mu.Lock()
	p := b.peers[code]
	b.mu.Unlock()
	if p == nil {
		return 0
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.runs)
}

func (b *sshBackend) factsOf(code string) (sshexec.Facts, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	f, ok := b.facts[code]
	return f, ok
}

// view 生成主机视图里的 SSH 部分。
func (b *sshBackend) view(rec model.TerminalHost) *SSHView {
	v := &SSHView{
		Address: rec.SSHAddr, User: rec.SSHUser, Auth: rec.SSHAuth,
		KeyType: rec.SSHHostKeyType, Fingerprint: rec.SSHFingerprint, KeyState: rec.SSHKeyState,
		HasSecret: rec.SSHSecret != "", Error: rec.SSHError, CheckedAt: rec.SSHCheckedAt, OKAt: rec.SSHOKAt,
		Backend: b.enabled(),
		Gateway: rec.SSHGateway, GatewayReady: rec.SSHGateway != "" && b.h.gatewayReady(rec.SSHGateway),
		CredOnGateway: rec.ViaGateway(), CredMissing: rec.ViaGateway() && b.credGone(rec.MachineCode),
		TermOnly: rec.ViaGateway() && rec.SSHTermOnly,
	}
	if rec.ViaGateway() {
		_, v.GatewayDenied = b.h.gatewayExecState(rec.SSHGateway)
	}
	return v
}

// ---------- 连接信息 ----------

var errSSHHostGone = errors.New("SSH 主机已被停用或删除")

// specFor 读取一台 SSH 主机的连接信息（短时缓存，避免每次轮询都读库解密）。
// 直连的主机解开 Hub 密封保存的凭据；经网关接入的主机没有凭据（它在网关上），规格带着 Via，只能经网关使用。
func (b *sshBackend) specFor(ctx context.Context, code string) (sshexec.Spec, model.TerminalHost, error) {
	now := b.h.now()
	b.mu.Lock()
	if e, ok := b.specs[code]; ok && now.Before(e.expires) {
		b.mu.Unlock()
		return e.spec, e.rec, nil
	}
	b.mu.Unlock()

	rec, err := b.h.store.GetTerminalHost(ctx, code)
	if err != nil {
		return sshexec.Spec{}, model.TerminalHost{}, err
	}
	if rec == nil || !rec.IsSSH() || rec.Status != termproto.HostStatusEnabled {
		return sshexec.Spec{}, model.TerminalHost{}, errSSHHostGone
	}
	spec, err := b.buildSpec(*rec)
	if err != nil {
		return sshexec.Spec{}, *rec, err
	}
	b.mu.Lock()
	b.specs[code] = specEntry{spec: spec, rec: *rec, expires: now.Add(sshSpecTTL)}
	b.mu.Unlock()
	return spec, *rec, nil
}

// gatewaySpec 还原经网关接入的主机的连接规格：只有地址、用户与已固定的主机公钥——没有凭据，Via 指向承载它的网关。
func (b *sshBackend) gatewaySpec(rec model.TerminalHost) (sshexec.Spec, error) {
	if rec.SSHKeyState == model.SSHKeyMismatch {
		return sshexec.Spec{}, sshexec.ErrHostKeyMismatch
	}
	hk, err := sshexec.DecodeHostKey(rec.SSHHostKey)
	if err != nil || hk.Fingerprint != rec.SSHFingerprint {
		return sshexec.Spec{}, sshexec.ErrNoHostKey
	}
	return sshexec.Spec{Addr: rec.SSHAddr, User: rec.SSHUser, HostKey: hk, Via: rec.SSHGateway}, nil
}

func (b *sshBackend) buildSpec(rec model.TerminalHost) (sshexec.Spec, error) {
	if rec.ViaGateway() {
		return b.gatewaySpec(rec)
	}
	if !b.enabled() {
		return sshexec.Spec{}, sshErr(http.StatusConflict, termexec.CodeSSHOff, "%s", b.status().Message)
	}
	if rec.SSHKeyState == model.SSHKeyMismatch {
		return sshexec.Spec{}, sshexec.ErrHostKeyMismatch
	}
	raw, err := b.sealer.Open(rec.SSHSecret, rec.MachineCode)
	if err != nil {
		return sshexec.Spec{}, sshErr(http.StatusConflict, "ssh_secret_unreadable", "无法解开保存的凭据（RELAYMESH_SSH_SECRET_KEY 可能已更换）：请在主机页重新填写凭据")
	}
	cred, err := sshexec.ParseCredential(raw)
	if err != nil {
		return sshexec.Spec{}, sshErr(http.StatusConflict, "ssh_secret_unreadable", "保存的凭据格式无效：请在主机页重新填写凭据")
	}
	hk, err := sshexec.DecodeHostKey(rec.SSHHostKey)
	if err != nil || hk.Fingerprint != rec.SSHFingerprint {
		return sshexec.Spec{}, sshexec.ErrNoHostKey
	}
	return sshexec.Spec{Addr: rec.SSHAddr, User: rec.SSHUser, Cred: cred, HostKey: hk}, nil
}

func (b *sshBackend) invalidate(code string) {
	b.mu.Lock()
	delete(b.specs, code)
	delete(b.routes, code)
	b.mu.Unlock()
}

// forget 丢弃连接缓存与连接（凭据 / 地址 / 固定的主机密钥变化时调用）。
func (b *sshBackend) forget(code string) {
	b.invalidate(code)
	b.pool.Forget(code)
	// 连接信息变了：之前的失败记录不再代表新的配置，别让快速失败窗口挡住管理员刚修好的主机。
	b.mu.Lock()
	delete(b.health, code)
	b.mu.Unlock()
}

// call 在一台 SSH 主机上执行一段脚本，并把结果计入健康状态。经网关接入的主机（spec.Via 非空）由网关代理这次 exec。
func (b *sshBackend) call(ctx context.Context, code string, spec sshexec.Spec, cmd string, stdin []byte) (sshexec.Result, error) {
	cctx, cancel := context.WithTimeout(ctx, sshCallTimeout)
	defer cancel()
	if spec.Via != "" {
		res, err := b.callVia(cctx, code, spec, cmd, stdin)
		b.noteViaResult(code, err)
		return res, err
	}
	res, err := b.pool.Exec(cctx, code, spec, cmd, stdin, 0)
	b.noteResult(code, err)
	return res, err
}

// noteResult 更新内存里的健康状态，并在状态变化（或隔一段时间）时落库。
func (b *sshBackend) noteResult(code string, err error) {
	if errors.Is(err, context.Canceled) && b.h.ctx.Err() != nil {
		return // Hub 正在关闭，不算主机的问题
	}
	now := b.h.now()
	b.mu.Lock()
	h := b.health[code]
	if h == nil {
		h = &hostHealth{}
		b.health[code] = h
	}
	text := ""
	if err != nil {
		text = mapSSHErr(err).Message
		h.failAt, h.lastErr = now, text
	} else {
		h.okAt, h.lastErr = now, ""
	}
	changed := h.persisted.IsZero() || h.persistOK != (err == nil) || now.Sub(h.persisted) >= sshHealthPersist
	mismatch := errors.Is(err, sshexec.ErrHostKeyMismatch)
	if changed || mismatch {
		h.persisted, h.persistOK = now, err == nil
	}
	b.mu.Unlock()
	if !(changed || mismatch) {
		return
	}
	if mismatch {
		b.invalidate(code)
		log.Printf("[terminal] SSH 主机 %s 的主机密钥与已固定的不一致，已停止连接（需要管理员重新确认指纹）", code)
	}
	chk := store.SSHCheck{At: now, OK: err == nil, Err: text, KeyMismatch: mismatch}
	if serr := b.h.store.RecordSSHCheck(b.h.ctx, code, chk); serr != nil && !errors.Is(serr, store.ErrTerminalHostNotFound) {
		log.Printf("[terminal] 记录 SSH 主机 %s 的连接状态失败: %v", code, serr)
	}
}

// ---------- 周期维护 ----------

func (b *sshBackend) maintain(now time.Time) {
	if b == nil {
		return
	}
	b.mu.Lock()
	due := now.After(b.reapAt)
	if due {
		b.reapAt = now.Add(sshReapEvery)
	}
	b.mu.Unlock()
	if due {
		b.pool.Reap(now)
	}
}

// sweepIfDue 在第一次（及之后每隔一段时间）使用某台主机时，顺带清理它上面过期的任务目录。尽力而为。
func (b *sshBackend) sweepIfDue(ctx context.Context, code string, spec sshexec.Spec) {
	now := b.h.now()
	b.mu.Lock()
	last := b.swept[code]
	if !last.IsZero() && now.Sub(last) < sshSweepEvery {
		b.mu.Unlock()
		return
	}
	b.swept[code] = now
	b.mu.Unlock()
	cmd, stdin, err := sshexec.SweepCommand(sshRetentionDays)
	if err != nil {
		return
	}
	_, _ = b.call(ctx, code, spec, cmd, stdin)
}

func (b *sshBackend) shutdown() {
	if b != nil {
		b.pool.Close()
	}
}

// ---------- 管理操作（均由 REST 层在解锁之后调用）----------

func (b *sshBackend) acquireMgmt() (release func(), err *SSHError) {
	select {
	case b.mgmt <- struct{}{}:
		return func() { <-b.mgmt }, nil
	default:
		return nil, sshErr(http.StatusTooManyRequests, "ssh_busy", "正在处理其他 SSH 操作，请稍后再试")
	}
}

// SSHProbeResult 是探测到的主机密钥，供管理员核对指纹。
type SSHProbeResult struct {
	Address     string `json:"address"`
	KeyType     string `json:"key_type"`
	Fingerprint string `json:"fingerprint"`
}

func (b *sshBackend) probe(ctx context.Context, address string) (*SSHProbeResult, *sshexec.HostKey, error) {
	addr, err := sshexec.NormalizeAddr(address)
	if err != nil {
		return nil, nil, mapSSHErr(err)
	}
	pctx, cancel := context.WithTimeout(ctx, sshProbeTimeout)
	defer cancel()
	hk, err := sshexec.Probe(pctx, addr, sshProbeTimeout)
	if err != nil {
		return nil, nil, mapSSHErr(err)
	}
	return &SSHProbeResult{Address: addr, KeyType: hk.Type, Fingerprint: hk.Fingerprint}, hk, nil
}

// SSHProbe 只做密钥交换取回对方的主机密钥指纹（不发送任何凭据）。由 Hub 直接出站探测。
func (h *Hub) SSHProbe(ctx context.Context, address, actor, ip string) (*SSHProbeResult, error) {
	return h.SSHProbeVia(ctx, address, "", actor, ip)
}

// SSHProbeVia 与 SSHProbe 相同，但可以指定由网关主机（机器码）出站探测：目标只在网关的网络里才够得着时使用。
// gateway 为空即 Hub 直连。
func (h *Hub) SSHProbeVia(ctx context.Context, address, gateway, actor, ip string) (*SSHProbeResult, error) {
	b := h.ssh
	gateway, e := normGateway(gateway)
	if e != nil {
		return nil, e
	}
	if e := b.needFor(gateway); e != nil {
		return nil, e
	}
	conn, e := h.connectorFor(ctx, gateway, false)
	if e != nil {
		return nil, e
	}
	release, e := b.acquireMgmt()
	if e != nil {
		return nil, e
	}
	defer release()
	res, _, err := conn.probe(ctx, address)
	if err != nil {
		return nil, err
	}
	h.audit.record(ctx, auditEvent{Event: EvSSHProbe, Actor: actor, IP: ip, Detail: "addr=" + res.Address + " fp=" + res.Fingerprint + gatewayDetail(gateway)})
	return res, nil
}

// gatewayDetail 生成审计详情里的网关片段（直连时为空串）。
func gatewayDetail(gateway string) string {
	if gateway = strings.TrimSpace(gateway); gateway != "" {
		return " gw=" + gateway
	}
	return ""
}

// SSHAddRequest 是登记一台 SSH 主机的请求。Fingerprint 必须是管理员核对过的、探测阶段给出的指纹。
type SSHAddRequest struct {
	Remark      string `json:"remark"`
	Address     string `json:"address"`
	User        string `json:"user"`
	AuthType    string `json:"auth_type"`
	Password    string `json:"password"`
	PrivateKey  string `json:"private_key"`
	Passphrase  string `json:"passphrase"`
	Fingerprint string `json:"fingerprint"`
	// DefaultForNewModels 是这台主机对新建模型授权的默认态（allow / deny，缺省 deny），只在新增主机时生效。
	DefaultForNewModels string `json:"default_for_new_models"`
	// Gateway 非空表示经网关主机（机器码）接入：由它出站连接目标、持有人工终端会话，并把登录凭据保存在自己的存储里
	// （Hub 不保存凭据、不直连）。目标是 POSIX 环境时模型也能通过它执行。空串即 Hub 直连（凭据由 Hub 密封保存，仅供模型执行）。
	Gateway string `json:"gateway"`
}

func credentialFrom(authType, password, privateKey, passphrase string) (sshexec.Credential, error) {
	c := sshexec.Credential{Kind: strings.TrimSpace(authType)}
	switch c.Kind {
	case sshexec.AuthPassword:
		c.Password = password
	case sshexec.AuthKey:
		c.PrivateKey, c.Passphrase = privateKey, passphrase
	}
	if err := c.Validate(); err != nil {
		return sshexec.Credential{}, err
	}
	return c, nil
}

// verify 用给定的连接信息真实地连一次并探测环境：凭据不对、不是 POSIX 主机都在保存之前暴露出来。
func (b *sshBackend) verify(ctx context.Context, spec sshexec.Spec) (sshexec.Facts, time.Duration, error) {
	start := time.Now()
	vctx, cancel := context.WithTimeout(ctx, sshCallTimeout)
	defer cancel()
	c, err := sshexec.Dial(vctx, spec, sshexec.DefaultDialTimeout)
	if err != nil {
		return sshexec.Facts{}, 0, err
	}
	defer c.Close()
	cmd, stdin := sshexec.FactsCommand()
	res, err := c.Exec(vctx, cmd, stdin, 0)
	if err != nil {
		return sshexec.Facts{}, 0, err
	}
	f, err := sshexec.ParseFacts(res.Stdout)
	if err != nil {
		return f, 0, err
	}
	return f, time.Since(start), nil
}

func hostOnly(addr string) string {
	if h, _, err := net.SplitHostPort(addr); err == nil {
		return h
	}
	return addr
}

// SSHAdd 登记一台 SSH 主机：重新探测并核对指纹 → 真实连接验证凭据 → 密封保存。
// req.DefaultForNewModels 为 allow 时，新增之后把这台主机写入现有模型令牌的主机名单（返回授予结果供界面提示）。
func (h *Hub) SSHAdd(ctx context.Context, req SSHAddRequest, actor, ip string) (*HostView, *store.HostGrantResult, error) {
	b := h.ssh
	gateway, e := normGateway(req.Gateway)
	if e != nil {
		return nil, nil, e
	}
	if e := b.needFor(gateway); e != nil {
		return nil, nil, e
	}
	conn, e := h.connectorFor(ctx, gateway, true)
	if e != nil {
		return nil, nil, e
	}
	release, e := b.acquireMgmt()
	if e != nil {
		return nil, nil, e
	}
	defer release()

	if err := store.ValidateHostDefaultForNewModels(req.DefaultForNewModels); err != nil {
		return nil, nil, sshErr(http.StatusBadRequest, "bad_request", "%s", err.Error())
	}
	user := strings.TrimSpace(req.User)
	if !sshexec.ValidUser(user) {
		return nil, nil, sshErr(http.StatusBadRequest, "bad_request", "用户名无效（只允许字母、数字和 - _ . $，最长 64 个字符）")
	}
	cred, err := credentialFrom(req.AuthType, req.Password, req.PrivateKey, req.Passphrase)
	if err != nil {
		return nil, nil, mapSSHErr(err)
	}
	confirmed := strings.TrimSpace(req.Fingerprint)
	if confirmed == "" {
		return nil, nil, sshErr(http.StatusBadRequest, "bad_request", "缺少 fingerprint：必须先探测并由管理员核对主机密钥指纹")
	}
	probe, hk, err := conn.probe(ctx, req.Address)
	if err != nil {
		return nil, nil, err
	}
	if subtle.ConstantTimeCompare([]byte(confirmed), []byte(hk.Fingerprint)) != 1 {
		return nil, nil, sshErr(http.StatusConflict, "ssh_fingerprint_changed", "主机密钥指纹与您核对的不一致（对方的密钥在探测之后发生了变化），已拒绝保存")
	}
	spec := sshexec.Spec{Addr: probe.Address, User: user, Cred: cred, HostKey: hk}

	// 直连：登录验证一次即可，凭据由 Hub 密封保存。经网关：凭据保存在网关自己的存储里（以机器码为键），所以机器码要先于验证生成，
	// 验证（真实登录）成功时由网关落库；Hub 不留任何副本，也不需要 SSH 主密钥。
	var (
		out verifyOutcome
		raw []byte
	)
	if gateway == "" {
		if out, err = conn.verify(ctx, "", spec, verifyInline); err != nil {
			return nil, nil, mapSSHErr(err)
		}
		if raw, err = cred.Marshal(); err != nil {
			return nil, nil, mapSSHErr(err)
		}
	} else {
		// 「在网关上保存凭据 → 主机记录落库」不能被 cred_sync 对账打断（见 credMu）。
		b.credMu.RLock()
		defer b.credMu.RUnlock()
	}
	now := h.now()
	var rec *model.TerminalHost
	for attempt := 0; attempt < 5; attempt++ {
		code, cerr := termproto.RandomMachineCode()
		if cerr != nil {
			return nil, nil, sshErr(http.StatusInternalServerError, "internal_error", "无法生成机器码")
		}
		sealed := ""
		if gateway == "" {
			var serr error
			if sealed, serr = b.sealer.Seal(raw, code); serr != nil {
				return nil, nil, sshErr(http.StatusInternalServerError, "internal_error", "无法加密凭据")
			}
		} else {
			// 网关按机器码保存凭据，会覆盖同一个码已有的凭据：先确认这个码没人用（80 位随机码，撞车实际不会发生，但绝不能赌）。
			if ex, gerr := h.store.GetTerminalHost(ctx, code); gerr != nil {
				return nil, nil, sshErr(http.StatusInternalServerError, "internal_error", "保存失败，请查看服务端日志")
			} else if ex != nil {
				continue
			}
			if out, err = conn.verify(ctx, code, spec, verifyPersist); err != nil {
				return nil, nil, mapSSHErr(err)
			}
		}
		hostname := termproto.SanitizeText(out.Facts.Host, 128)
		if hostname == "" {
			hostname = termproto.SanitizeText(hostOnly(probe.Address), 128) // 探测不出环境信息（非 POSIX 目标）：退回到地址
		}
		rec = &model.TerminalHost{
			MachineCode: code, Kind: model.HostKindSSH, Status: termproto.HostStatusEnabled,
			Remark:   termproto.SanitizeText(req.Remark, termproto.MaxRemarkRunes),
			Hostname: hostname, OS: termproto.SanitizeText(out.Facts.OS, 64), Arch: termproto.SanitizeText(out.Facts.Arch, 32),
			LastIP: termproto.SanitizeText(hostOnly(probe.Address), 64), LastSeenAt: &now, EnabledAt: &now,
			SSHAddr: probe.Address, SSHUser: user, SSHAuth: cred.Kind, SSHSecret: sealed,
			SSHHostKeyType: hk.Type, SSHHostKey: hk.Encode(), SSHFingerprint: hk.Fingerprint,
			SSHCheckedAt: &now, SSHOKAt: &now,
			SSHGateway:          gateway,
			SSHTermOnly:         gateway != "" && !out.Posix,
			DefaultForNewModels: store.HostDefaultState(req.DefaultForNewModels),
		}
		if rec.SSHTermOnly {
			// 只能当人工终端用的主机模型用不了：不预先授予任何模型令牌，免得名单里出现永远不可用的条目。
			rec.DefaultForNewModels = model.HostDefaultDeny
		}
		cerr = h.store.CreateSSHHost(ctx, rec)
		if cerr == nil {
			break
		}
		rec = nil
		if gateway != "" && !errors.Is(cerr, store.ErrTerminalHostExists) {
			// 没登记成：回收刚保存在网关上的凭据（网关离线则留给下次对账回收）。码撞车时归属不明，不能删。
			h.dropGatewayCredAsync(gateway, code)
		}
		switch {
		case errors.Is(cerr, store.ErrTerminalHostExists):
			continue // 随机码撞车：换一个
		case errors.Is(cerr, store.ErrSSHHostLimit):
			return nil, nil, sshErr(http.StatusConflict, "ssh_limit", "SSH 主机数量已达上限（%d）", store.MaxSSHHosts)
		default:
			log.Printf("[terminal] 保存 SSH 主机失败: %v", cerr)
			return nil, nil, sshErr(http.StatusInternalServerError, "internal_error", "保存失败，请查看服务端日志")
		}
	}
	if rec == nil {
		return nil, nil, sshErr(http.StatusInternalServerError, "internal_error", "无法生成唯一的机器码")
	}
	if out.Posix {
		b.mu.Lock()
		b.facts[rec.MachineCode] = out.Facts
		b.mu.Unlock()
	}
	b.setCredLost(rec.MachineCode, false)
	detail := "addr=" + probe.Address + " user=" + user + " auth=" + cred.Kind + " fp=" + hk.Fingerprint + gatewayDetail(gateway)
	if rec.SSHTermOnly {
		detail += " term_only"
	}
	h.audit.record(ctx, auditEvent{Event: EvSSHAdd, Actor: actor, IP: ip, MachineCode: rec.MachineCode, Detail: detail})
	var grant *store.HostGrantResult
	if !rec.SSHTermOnly {
		grant = h.grantNewHostByDefault(ctx, rec, actor, ip)
	}
	view, err := h.hostView(ctx, rec.MachineCode)
	return view, grant, err
}

// SSHTestResult 是一次连接测试的结果（失败也以 200 返回，错误码与消息在字段里）。
type SSHTestResult struct {
	OK        bool   `json:"ok"`
	Code      string `json:"code,omitempty"`
	Message   string `json:"message,omitempty"`
	Hostname  string `json:"hostname,omitempty"`
	OS        string `json:"os,omitempty"`
	Arch      string `json:"arch,omitempty"`
	User      string `json:"user,omitempty"`
	Home      string `json:"home,omitempty"`
	LatencyMS int64  `json:"latency_ms,omitempty"`
}

func (b *sshBackend) loadSSH(ctx context.Context, code string) (*model.TerminalHost, error) {
	rec, err := b.h.store.GetTerminalHost(ctx, code)
	if err != nil {
		return nil, err
	}
	if rec == nil || !rec.IsSSH() {
		return nil, ErrHostNotFound
	}
	return rec, nil
}

// SSHTest 用当前保存的连接信息真实地连一次：验证主机密钥、凭据与环境，并刷新主机的自报信息。
func (h *Hub) SSHTest(ctx context.Context, code, actor, ip string) (*SSHTestResult, error) {
	b := h.ssh
	rec, err := b.loadSSH(ctx, code)
	if err != nil {
		return nil, err
	}
	if e := b.needFor(rec.SSHGateway); e != nil {
		return nil, e
	}
	release, e := b.acquireMgmt()
	if e != nil {
		return nil, e
	}
	defer release()

	spec, err := b.buildSpec(*rec)
	var out verifyOutcome
	if err == nil {
		conn, ce := h.connectorFor(ctx, rec.SSHGateway, true)
		if ce != nil {
			err = ce // 网关不可用：没有真正去连目标
		} else {
			mode := verifyInline
			if rec.ViaGateway() {
				mode = verifyStored // 经网关接入的主机：用网关里保存的凭据复测
			}
			out, err = conn.verify(ctx, code, spec, mode)
		}
	}
	res := &SSHTestResult{OK: err == nil}
	chk := store.SSHCheck{At: h.now(), OK: err == nil}
	skipRecord := false
	if err != nil {
		me := mapSSHErr(err)
		res.Code, res.Message = me.Code, me.Message
		chk.Err = me.Message
		chk.KeyMismatch = errors.Is(err, sshexec.ErrHostKeyMismatch) || me.Code == "ssh_host_key_mismatch"
		// 网关本身不可用是暂时的状态（界面用 gateway_ready 呈现），凭据不在网关上也不是目标的故障（界面单独提示「凭据缺失」）：
		// 都不能记成目标主机的连接失败。
		skipRecord = isGatewayInfraCode(me.Code) || me.Code == termexec.CodeSSHCredMissing
		if me.Code == termexec.CodeSSHCredMissing {
			b.setCredLost(code, true)
		}
	} else {
		facts := out.Facts
		res.Hostname, res.OS, res.Arch, res.User, res.Home, res.LatencyMS = facts.Host, facts.OS, facts.Arch, facts.User, facts.Home, out.Latency.Milliseconds()
		chk.Hostname, chk.OS, chk.Arch = termproto.SanitizeText(facts.Host, 128), termproto.SanitizeText(facts.OS, 64), termproto.SanitizeText(facts.Arch, 32)
		if rec.ViaGateway() {
			// 目标环境可能被重装成了别的系统：每次复测都刷新「只能当终端用」的标记。
			termOnly := !out.Posix
			chk.TermOnly = &termOnly
		}
		b.mu.Lock()
		if out.Posix {
			b.facts[code] = facts
		} else {
			delete(b.facts, code)
		}
		if hh := b.health[code]; hh != nil {
			hh.okAt, hh.failAt, hh.lastErr = h.now(), time.Time{}, ""
		}
		b.mu.Unlock()
		b.setCredLost(code, false)
	}
	if !skipRecord {
		if serr := h.store.RecordSSHCheck(ctx, code, chk); serr != nil && !errors.Is(serr, store.ErrTerminalHostNotFound) {
			log.Printf("[terminal] 记录 SSH 测试结果失败 (%s): %v", code, serr)
		}
	}
	if chk.KeyMismatch {
		b.forget(code)
	}
	b.invalidate(code)
	h.audit.record(ctx, auditEvent{Event: EvSSHTest, Actor: actor, IP: ip, MachineCode: code, Result: map[bool]string{true: ResultOK, false: ResultFail}[res.OK],
		Detail: "addr=" + rec.SSHAddr + " result=" + firstNonEmpty(res.Code, "ok")})
	return res, nil
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// SSHConnUpdate 是对已登记 SSH 主机连接信息的修改：Address / User 为空表示不改；AuthType 为空表示不改凭据。
type SSHConnUpdateRequest struct {
	Address    string `json:"address"`
	User       string `json:"user"`
	AuthType   string `json:"auth_type"`
	Password   string `json:"password"`
	PrivateKey string `json:"private_key"`
	Passphrase string `json:"passphrase"`
	// Gateway 改变接入方式：nil 表示不改；空串表示改为由 Hub 直连；机器码表示改接到那台网关主机。
	// 新的连接途径同样要先真实连通一次才会保存。
	Gateway *string `json:"gateway"`
}

// SSHUpdateConn 修改连接信息（地址 / 用户 / 凭据）。新信息先用**已固定的主机密钥**真实连一次，通过才保存：
// 因此改地址不会绕过密钥固定（连到别的机器会因密钥不一致而失败），填错凭据也不会把主机弄成连不上。
func (h *Hub) SSHUpdateConn(ctx context.Context, code string, req SSHConnUpdateRequest, actor, ip string) (*HostView, error) {
	b := h.ssh
	rec, err := b.loadSSH(ctx, code)
	if err != nil {
		return nil, err
	}
	oldGateway := rec.SSHGateway
	newGateway := oldGateway
	if req.Gateway != nil {
		ng, ge := normGateway(*req.Gateway)
		if ge != nil {
			return nil, ge
		}
		newGateway = ng
	}
	routeChanged := newGateway != oldGateway
	newCred := strings.TrimSpace(req.AuthType) != ""
	if routeChanged && !newCred {
		// 凭据只保存在各自的位置（直连在 Hub，经网关在网关主机上），换路径时不能凭空搬运：旧位置的凭据对 Hub 不可见或不该外流。
		return nil, sshErr(http.StatusBadRequest, "bad_request", "更改接入方式需要同时重新填写登录凭据（凭据保存在 Hub 或网关主机各自的存储里，不会跨位置搬运）")
	}
	if e := b.needFor(newGateway); e != nil {
		return nil, e
	}
	release, e := b.acquireMgmt()
	if e != nil {
		return nil, e
	}
	defer release()

	var spec sshexec.Spec
	if !routeChanged && !rec.ViaGateway() {
		spec, err = b.buildSpec(*rec)
		if err != nil && !errors.Is(err, sshexec.ErrHostKeyMismatch) {
			// 凭据读不出来（密封密钥更换过）时，允许用新凭据覆盖；其余情况照常报错。
			var se *SSHError
			if !(errors.As(err, &se) && se.Code == "ssh_secret_unreadable" && req.AuthType != "") {
				return nil, mapSSHErr(err)
			}
			hk, derr := sshexec.DecodeHostKey(rec.SSHHostKey)
			if derr != nil {
				return nil, mapSSHErr(derr)
			}
			spec = sshexec.Spec{Addr: rec.SSHAddr, User: rec.SSHUser, HostKey: hk}
		} else if errors.Is(err, sshexec.ErrHostKeyMismatch) {
			return nil, sshErr(http.StatusConflict, termexec.CodeSSHUntrusted, "主机密钥状态为不一致：请先核对并重新确认指纹")
		}
	} else {
		// 经网关接入的主机没有 Hub 侧的凭据；换路径时旧凭据本来就不能沿用。这里只取地址 / 用户 / 已固定的主机公钥。
		spec, err = b.gatewaySpec(*rec)
		if errors.Is(err, sshexec.ErrHostKeyMismatch) {
			return nil, sshErr(http.StatusConflict, termexec.CodeSSHUntrusted, "主机密钥状态为不一致：请先核对并重新确认指纹")
		} else if err != nil {
			return nil, mapSSHErr(err)
		}
		spec.Via = ""
	}

	upd := store.SSHConnUpdate{}
	changes := make([]string, 0, 3)
	if a := strings.TrimSpace(req.Address); a != "" {
		na, aerr := sshexec.NormalizeAddr(a)
		if aerr != nil {
			return nil, mapSSHErr(aerr)
		}
		if na != spec.Addr {
			spec.Addr, upd.Addr = na, &na
			changes = append(changes, "addr="+na)
		}
	}
	if u := strings.TrimSpace(req.User); u != "" {
		if !sshexec.ValidUser(u) {
			return nil, sshErr(http.StatusBadRequest, "bad_request", "用户名无效")
		}
		if u != spec.User {
			spec.User, upd.User = u, &u
			changes = append(changes, "user="+u)
		}
	}
	if newCred {
		cred, cerr := credentialFrom(req.AuthType, req.Password, req.PrivateKey, req.Passphrase)
		if cerr != nil {
			return nil, mapSSHErr(cerr)
		}
		kind := cred.Kind
		spec.Cred = cred
		upd.Auth = &kind
		if newGateway == "" {
			// 直连：凭据由 Hub 密封保存。
			raw, merr := cred.Marshal()
			if merr != nil {
				return nil, mapSSHErr(merr)
			}
			sealed, serr := b.sealer.Seal(raw, code)
			if serr != nil {
				return nil, sshErr(http.StatusInternalServerError, "internal_error", "无法加密凭据")
			}
			upd.Secret = &sealed
		} else {
			// 经网关：凭据由网关在真实登录成功之后保存到自己的存储里，Hub 不留副本（顺便清掉直连时留下的旧密封凭据）。
			none := ""
			upd.Secret = &none
		}
		changes = append(changes, "auth="+kind)
	}
	if routeChanged {
		upd.Gateway = &newGateway
		changes = append(changes, "gw="+firstNonEmpty(newGateway, "direct"))
	}
	if len(changes) == 0 {
		return nil, sshErr(http.StatusBadRequest, "bad_request", "没有需要修改的内容")
	}
	if newGateway == "" && spec.Cred.Kind == "" {
		return nil, sshErr(http.StatusBadRequest, "bad_request", "凭据不可用：请同时重新填写凭据")
	}
	conn, ce := h.connectorFor(ctx, newGateway, newGateway != "")
	if ce != nil {
		return nil, ce
	}
	mode := verifyInline
	if newGateway != "" {
		if newCred {
			mode = verifyPersist
			// 「在网关上保存凭据 → 主机记录落库」不能被 cred_sync 对账打断（见 credMu）。
			b.credMu.RLock()
			defer b.credMu.RUnlock()
		} else {
			mode = verifyStored // 只改地址 / 用户：沿用网关里已保存的凭据
		}
	}
	out, verr := conn.verify(ctx, code, spec, mode)
	if verr != nil {
		if me := mapSSHErr(verr); me.Code == termexec.CodeSSHCredMissing {
			b.setCredLost(code, true)
		}
		return nil, mapSSHErr(verr)
	}
	if newGateway != "" {
		termOnly := !out.Posix
		upd.TermOnly = &termOnly
	} else if rec.SSHTermOnly {
		off := false
		upd.TermOnly = &off // 改回直连：直连要求目标必须是 POSIX（verify 已保证）
	}
	if err := h.store.UpdateSSHConn(ctx, code, upd); err != nil {
		if mode == verifyPersist && routeChanged {
			h.dropGatewayCredAsync(newGateway, code) // 主机记录没改成：回收刚保存在新网关上的凭据
		}
		if errors.Is(err, store.ErrTerminalHostNotFound) {
			return nil, ErrHostNotFound
		}
		return nil, err
	}
	if oldGateway != "" && routeChanged {
		h.dropGatewayCredAsync(oldGateway, code) // 离开旧网关：清掉它那里的凭据
	}
	b.forget(code)
	b.mu.Lock()
	if out.Posix {
		b.facts[code] = out.Facts
	} else {
		delete(b.facts, code)
	}
	b.mu.Unlock()
	if newGateway != "" {
		b.setCredLost(code, false)
	}
	chk := store.SSHCheck{At: h.now(), OK: true}
	if upd.TermOnly != nil {
		chk.TermOnly = upd.TermOnly
	}
	_ = h.store.RecordSSHCheck(ctx, code, chk)
	h.audit.record(ctx, auditEvent{Event: EvSSHConn, Actor: actor, IP: ip, MachineCode: code, Detail: strings.Join(changes, " ")})
	// 目标的身份（地址 / 用户）或接入路径变了：旧路径上还开着的终端会话不该继续连着原来的对象。
	// 只换凭据不算：已经认证的会话不受凭据轮换影响，强行断开只会打断正在做的事。
	if upd.Addr != nil || upd.User != nil || upd.Gateway != nil {
		if rec.ViaGateway() {
			h.endGatewaySessions(ctx, rec.SSHGateway, code, "reconfigured", actor, ip)
		} else {
			h.endLocalSessions(ctx, code, "reconfigured", actor, ip) // Hub 直连：会话在 Hub 里
		}
	}
	return h.hostView(ctx, code)
}

// SSHRekey 在管理员核对新指纹之后，重新固定这台主机的主机密钥（主机重装 / 密钥轮换之后使用）。
func (h *Hub) SSHRekey(ctx context.Context, code, fingerprint, actor, ip string) (*HostView, error) {
	b := h.ssh
	rec, err := b.loadSSH(ctx, code)
	if err != nil {
		return nil, err
	}
	if e := b.needFor(rec.SSHGateway); e != nil {
		return nil, e
	}
	release, e := b.acquireMgmt()
	if e != nil {
		return nil, e
	}
	defer release()

	confirmed := strings.TrimSpace(fingerprint)
	if confirmed == "" {
		return nil, sshErr(http.StatusBadRequest, "bad_request", "缺少 fingerprint：必须先探测并由管理员核对新的主机密钥指纹")
	}
	conn, ce := h.connectorFor(ctx, rec.SSHGateway, false)
	if ce != nil {
		return nil, ce
	}
	probe, hk, err := conn.probe(ctx, rec.SSHAddr)
	if err != nil {
		return nil, err
	}
	if subtle.ConstantTimeCompare([]byte(confirmed), []byte(hk.Fingerprint)) != 1 {
		return nil, sshErr(http.StatusConflict, "ssh_fingerprint_changed", "主机密钥指纹与您核对的不一致，已拒绝")
	}
	empty := ""
	typ, enc, fp := hk.Type, hk.Encode(), hk.Fingerprint
	if err := h.store.UpdateSSHConn(ctx, code, store.SSHConnUpdate{HostKeyType: &typ, HostKey: &enc, Fingerprint: &fp, KeyState: &empty, Error: &empty}); err != nil {
		if errors.Is(err, store.ErrTerminalHostNotFound) {
			return nil, ErrHostNotFound
		}
		return nil, err
	}
	b.forget(code)
	h.audit.record(ctx, auditEvent{Event: EvSSHRekey, Actor: actor, IP: ip, MachineCode: code,
		Detail: "addr=" + probe.Address + " old_fp=" + rec.SSHFingerprint + " new_fp=" + hk.Fingerprint})
	return h.hostView(ctx, code)
}

// SSHProbeHost 对已登记主机的当前地址重新探测（用于「重新确认指纹」前展示新的指纹）。
func (h *Hub) SSHProbeHost(ctx context.Context, code, actor, ip string) (*SSHProbeResult, error) {
	b := h.ssh
	rec, err := b.loadSSH(ctx, code)
	if err != nil {
		return nil, err
	}
	return h.SSHProbeVia(ctx, rec.SSHAddr, rec.SSHGateway, actor, ip)
}

// sshForgetHost 在 SSH 主机被删除 / 停用时调用：丢弃缓存的连接信息。连接本身留给空闲回收，
// 这样进行中的运行仍能用已持有的连接信息把取消送达远端。
func (h *Hub) sshForgetHost(code string) {
	if h.ssh != nil {
		h.ssh.invalidate(code)
	}
}
