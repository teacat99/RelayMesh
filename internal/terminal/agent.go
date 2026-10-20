package terminal

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"log"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/teacat99/RelayMesh/v2/internal/store"
	"github.com/teacat99/RelayMesh/v2/internal/termproto"
)

// 会话相关请求的等待期限。
const (
	defaultOpenTimeout   = 10 * time.Second
	defaultAttachTimeout = 10 * time.Second

	maxPendingOpens = 4
)

var (
	errAgentClosed    = errors.New("agent connection closed")
	errOpenTimeout    = errors.New("agent did not answer in time")
	errTooManyOpens   = errors.New("too many open requests in flight")
	errSessionLimit   = errors.New("session limit reached")
	errNoSuchSession  = errors.New("no such session")
	errAgentNotActive = errors.New("agent is not enabled")
)

// openResult 是 open 请求的结果。
type openResult struct {
	info   termproto.SessionInfo
	reason string
	code   string // 守护进程给出的稳定失败码（目前只有 SSH 目标会带）；已净化前的原始值，使用前须 SanitizeSSHCode
}

// AgentOpenError 表示 Agent 拒绝新建会话。Code 是稳定的失败码（SSH 目标专用，本地 shell 与旧版本 Agent 为空）。
type AgentOpenError struct {
	Code   string
	Reason string
}

func (e *AgentOpenError) Error() string { return "agent refused to open session: " + e.Reason }

// 网关一次性操作（ssh_op）的并发与等待上限：等待期限须长于守护进程自己的操作超时（20 秒 / exec 代理 23 秒），这样超时总是由守护进程给出明确的失败码。
// 管理类操作（探测 / 登记 / 测试 / 凭据同步）很少，上限小；exec 代理承载模型执行的启动与轮询（每台主机最多 8 路并发运行、
// 轮询频繁），上限单独放宽，两类互不挤占。
const (
	maxPendingSSHOps  = 8
	maxPendingSSHExec = 64
	sshOpWait         = 30 * time.Second
	// maxSSHOpFrame 是一个 ssh_op 帧（JSON）的字节上限：Agent 的读取上限是 64 KiB，超限的帧会让 Agent 断开连接，
	// 所以发送前必须自查，留出 1 KiB 余量。
	maxSSHOpFrame = termproto.MaxControlFrame - 1<<10
	// sshOpenAllowance 是 SSH 会话在常规 open 期限之外多给的时间：网关要先出站建连、完成密钥交换与认证。
	sshOpenAllowance = 20 * time.Second
)

// sshWaitResult 是一次 ssh_op 的等待结果：gone 表示连接在应答到来之前断开。
type sshWaitResult struct {
	res  termproto.SSHResult
	gone bool
}

// agentConn 是一条 Agent→Hub 的 WebSocket 连接（已启用的在线 Agent，或待启用的等待中 Agent）。
//
// 并发约定：
//   - 读循环只有一个 goroutine（serve）；写可以来自任意 goroutine（coder/websocket 保证并发写安全）；
//   - mu 只保护内存状态，持锁期间绝不做网络 I/O；
//   - 读循环绝不阻塞在浏览器侧：输出帧只做非阻塞入队，队列满即断开该附着（见 attachment.deliver）。
type agentConn struct {
	hub    *Hub
	ws     *websocket.Conn
	code   string
	pub    ed25519.PublicKey
	meta   termproto.Meta
	ip     string
	temp   bool // 临时身份（仅内存启用状态）
	ctx    context.Context
	cancel context.CancelFunc

	closeOnce sync.Once

	mu       sync.Mutex
	enabled  bool
	closed   bool
	sessions []termproto.SessionInfo
	holder   *termproto.Holder // 最近一次守护进程状态（已净化）；nil 表示 Agent 没有报告（旧版本）
	lostSeen map[string]struct{}
	chans    map[uint32]*attachment
	nextCh   uint32
	opens    map[string]chan openResult

	recWaits map[uint32]*recordWaiter // 在途的记录读取请求
	nextRec  uint32

	runWaits   map[uint32]*runReadWaiter // 在途的运行输出读取请求
	nextRunReq uint32
	execSynced bool // 本连接是否已请求过一次运行对账（runs_sync）

	fileWaits   map[uint32]*fileWaiter // 在途的文件请求
	nextFileReq uint32

	sshWaits       map[string]chan sshWaitResult // 在途的网关一次性操作（ssh_op）
	sshExecPending int                           // 其中 exec 代理的数量（sshWaits 里其余的是管理类操作）
	credSynced     bool                          // 本连接的网关凭据是否已与 Hub 对账过（守护进程就绪后做一次）

	autoCheck     *AutoCheckView // 最近一次自动升级检查的结果；nil 表示本连接还没检查过
	autoCheckAt   time.Time      // 最近一次被受理的检查时刻，用来限制检查频率
	autoRequestAt time.Time      // 最近一次被受理的静默升级请求时刻，用来限制请求频率
	autoGrant     *AutoGrantView // 最近一次静默升级请求的裁决；nil 表示本连接还没请求过
}

func newAgentConn(h *Hub, ws *websocket.Conn, code string, pub ed25519.PublicKey, meta termproto.Meta, ip string) *agentConn {
	ctx, cancel := context.WithCancel(h.ctx)
	return &agentConn{
		hub:      h,
		ws:       ws,
		code:     code,
		pub:      pub,
		meta:     meta,
		ip:       ip,
		temp:     meta.Kind == termproto.KindTemporary,
		ctx:      ctx,
		cancel:   cancel,
		chans:    make(map[uint32]*attachment),
		opens:    make(map[string]chan openResult),
		recWaits: make(map[uint32]*recordWaiter),
		sshWaits: make(map[string]chan sshWaitResult),
		nextCh:   1,
	}
}

// write 发送一帧；失败（含超时）即关闭连接——卡死的对端不值得等待。
func (a *agentConn) write(typ websocket.MessageType, data []byte) error {
	a.mu.Lock()
	closed := a.closed
	a.mu.Unlock()
	if closed {
		return errAgentClosed
	}
	ctx, cancel := context.WithTimeout(a.ctx, writeTimeout)
	defer cancel()
	if err := a.ws.Write(ctx, typ, data); err != nil {
		a.closeWith(websocket.StatusInternalError, "write failed")
		return err
	}
	return nil
}

func (a *agentConn) writeJSON(v any) error {
	data, err := termproto.Marshal(v)
	if err != nil {
		return err
	}
	return a.write(websocket.MessageText, data)
}

func (a *agentConn) writeBinary(data []byte) error {
	return a.write(websocket.MessageBinary, data)
}

// closeWith 异步发起带关闭码的关闭握手（幂等）。reason 必须简短（≤125 字节）。
func (a *agentConn) closeWith(code websocket.StatusCode, reason string) {
	a.closeOnce.Do(func() {
		a.mu.Lock()
		a.closed = true
		a.mu.Unlock()
		go func() { _ = a.ws.Close(code, reason) }()
	})
}

func (a *agentConn) isEnabled() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.enabled && !a.closed
}

// promote 把待启用连接提升为已启用（管理员启用了该主机）。连接已关闭时返回 false。
func (a *agentConn) promote() bool {
	a.mu.Lock()
	if a.closed {
		a.mu.Unlock()
		return false
	}
	a.enabled = true
	a.mu.Unlock()
	a.ws.SetReadLimit(termproto.MaxControlFrame)
	return true
}

// serve 是连接的读循环，阻塞到连接结束；结束时完成全部清理。
func (a *agentConn) serve(onClose func()) {
	defer a.cleanup(onClose)
	go a.pingLoop()
	for {
		typ, data, err := a.ws.Read(a.ctx)
		if err != nil {
			return
		}
		a.handleFrame(typ, data)
	}
}

func (a *agentConn) pingLoop() {
	ticker := time.NewTicker(termproto.PingInterval)
	defer ticker.Stop()
	for {
		select {
		case <-a.ctx.Done():
			return
		case <-ticker.C:
			ctx, cancel := context.WithTimeout(a.ctx, termproto.PongTimeout)
			err := a.ws.Ping(ctx)
			cancel()
			if err != nil {
				a.closeWith(websocket.StatusGoingAway, "ping timeout")
				return
			}
		}
	}
}

// cleanup 连接结束后的统一收尾：失败待处理请求、断开全部浏览器附着、注销登记。
func (a *agentConn) cleanup(onClose func()) {
	a.mu.Lock()
	a.closed = true
	opens := a.opens
	a.opens = make(map[string]chan openResult)
	sshWaits := a.sshWaits
	a.sshWaits = make(map[string]chan sshWaitResult)
	a.mu.Unlock()

	// 带码关闭（若此前已调用过 closeWith 则无操作）；不能 CloseNow，否则会掐断进行中的关闭握手。
	a.closeWith(websocket.StatusNormalClosure, "")
	a.cancel()

	for _, ch := range opens {
		select {
		case ch <- openResult{reason: "agent offline"}:
		default:
		}
	}
	for _, ch := range sshWaits {
		select {
		case ch <- sshWaitResult{gone: true}:
		default:
		}
	}
	a.closeAttachments(termproto.CloseNotFound, "agent offline")
	a.hub.exec.agentGone(a)
	if onClose != nil {
		onClose()
	}
	// 该主机已经没有连接（不是被新连接顶替）：清掉它名下终端的屏幕信号，补上审批等待的结束。
	if a.hub.onlineAgent(a.code) == nil {
		a.hub.screens.observe(a, nil)
	}
}

// handleFrame 分发一帧。协议违例（不该出现的帧类型）直接断开，未知文本类型则忽略以便向前兼容。
func (a *agentConn) handleFrame(typ websocket.MessageType, data []byte) {
	if typ == websocket.MessageBinary {
		if !a.isEnabled() {
			a.closeWith(termproto.CloseInvalidHandshake, "unexpected data before approval")
			return
		}
		if ft, err := termproto.FrameType(data); err == nil {
			switch ft {
			case termproto.FrameRecord:
				a.onRecordFrame(data)
				return
			case termproto.FrameRunOut:
				a.onRunOutFrame(data)
				return
			case termproto.FrameRunData:
				a.onRunDataFrame(data)
				return
			case termproto.FrameFileData:
				a.onFileFrame(data)
				return
			}
		}
		ch, offset, payload, err := termproto.DecodeOutput(data)
		if err != nil {
			a.closeWith(termproto.CloseInvalidHandshake, "bad output frame")
			return
		}
		if att := a.attachment(ch); att != nil {
			att.deliver(offset, payload)
		}
		return
	}

	t, err := termproto.ParseType(data)
	if err != nil {
		a.closeWith(termproto.CloseInvalidHandshake, "bad control frame")
		return
	}
	if !a.isEnabled() {
		// 待启用期间 Agent 只应静默等待；任何业务帧都属于违例。
		if t == termproto.TBye {
			a.closeWith(websocket.StatusNormalClosure, "bye")
		}
		return
	}

	switch t {
	case termproto.TSessions:
		var m termproto.Sessions
		if json.Unmarshal(data, &m) == nil {
			a.setSessions(m.Sessions)
		}
	case termproto.TOpened:
		var m termproto.Opened
		if json.Unmarshal(data, &m) == nil {
			a.onOpened(m)
		}
	case termproto.TOpenFailed:
		var m termproto.OpenFailed
		if json.Unmarshal(data, &m) == nil {
			a.onOpenFailed(m)
		}
	case termproto.TAttached:
		var m termproto.Attached
		if json.Unmarshal(data, &m) == nil {
			if att := a.attachment(m.Ch); att != nil {
				att.onAttached(m)
			}
		}
	case termproto.TAttachFailed:
		var m termproto.AttachFailed
		if json.Unmarshal(data, &m) == nil {
			if att := a.attachment(m.Ch); att != nil {
				att.onAttachFailed(m)
			}
		}
	case termproto.TDetached:
		var m termproto.Detached
		if json.Unmarshal(data, &m) == nil {
			if att := a.attachment(m.Ch); att != nil {
				att.endFromAgent(m.Reason)
			}
		}
	case termproto.TExit:
		var m termproto.Exit
		if json.Unmarshal(data, &m) == nil {
			a.onExit(m)
		}
	case termproto.THolder:
		var m termproto.Holder
		if json.Unmarshal(data, &m) == nil {
			a.onHolder(m.Sanitized())
		}
	case termproto.TRecordInfo:
		var m termproto.RecordInfo
		if json.Unmarshal(data, &m) == nil {
			a.onRecordInfo(m)
		}
	case termproto.TRecordFail:
		var m termproto.RecordFail
		if json.Unmarshal(data, &m) == nil {
			a.onRecordFail(m)
		}
	case termproto.TRunStarted:
		if m, ok := decodeJSON[termproto.RunStarted](data); ok {
			if m, ok = m.Sanitized(); ok {
				a.hub.exec.onRunStarted(a, m)
			}
		}
	case termproto.TRunFailed:
		if m, ok := decodeJSON[termproto.RunFailed](data); ok {
			if m, ok = m.Sanitized(); ok {
				a.hub.exec.onRunFailed(a, m)
			}
		}
	case termproto.TRunEnd:
		if m, ok := decodeJSON[termproto.RunEnd](data); ok {
			if m, ok = m.Sanitized(); ok {
				a.hub.exec.onRunEnd(a, m)
			}
		}
	case termproto.TRuns:
		if m, ok := decodeJSON[termproto.Runs](data); ok {
			a.hub.exec.onRuns(a, m.Sanitized())
		}
	case termproto.TRunReadFail:
		if m, ok := decodeJSON[termproto.RunReadFail](data); ok {
			a.onRunReadFail(m)
		}
	case termproto.TFileRes:
		if m, ok := decodeJSON[termproto.FileRes](data); ok {
			a.onFileRes(m.Sanitized())
		}
	case termproto.TSSHResult:
		if m, ok := decodeJSON[termproto.SSHResult](data); ok {
			a.onSSHResult(m.Sanitized())
		}
	case termproto.TUpgradeStatus:
		if m, ok := decodeJSON[termproto.UpgradeStatus](data); ok {
			if m, ok = m.Sanitized(); ok {
				a.hub.onUpgradeStatus(a, m)
			}
		}
	case termproto.TAutoUpgradeCheck:
		if m, ok := decodeJSON[termproto.AutoUpgradeCheck](data); ok {
			a.hub.onAutoUpgradeCheck(a, m)
		}
	case termproto.TAutoUpgradeRequest:
		if m, ok := decodeJSON[termproto.AutoUpgradeRequest](data); ok {
			a.hub.onAutoUpgradeRequest(a, m)
		}
	case termproto.TBye:
		a.closeWith(websocket.StatusNormalClosure, "bye")
	}
}

// ---------- 会话守护进程 ----------

// HolderView 是主机上会话守护进程的状态（供界面标注「重启不中断」/「重启会中断」与解释会话为什么消失）。
type HolderView struct {
	State      string `json:"state"` // ready | unavailable
	Persistent bool   `json:"persistent"`
	Version    string `json:"version,omitempty"`
	// Rev 是守护进程的「Agent 侧代码指纹」（空 = 旧版本守护进程）。与主机 Agent 的指纹比较，就能精确知道守护进程跑的是不是旧代码，
	// 而不是靠版本号（Agent 升级后版本号总是不同，哪怕守护进程的代码一模一样）。
	Rev     string                  `json:"rev,omitempty"`
	Started int64                   `json:"started,omitempty"`
	Reason  string                  `json:"reason,omitempty"`
	Lost    []termproto.LostSession `json:"lost,omitempty"`
	// Caps 是守护进程的能力清单；DenyExec 表示本机用 --deny-model-exec 拒绝一切模型运行；Exec 是模型运行的执行环境。
	Caps     []string            `json:"caps,omitempty"`
	DenyExec bool                `json:"deny_exec,omitempty"`
	Exec     *termproto.ExecInfo `json:"exec,omitempty"`
}

// onHolder 记录守护进程状态；状态翻转与新出现的「丢失会话」写入审计。
func (a *agentConn) onHolder(m termproto.Holder) {
	a.mu.Lock()
	prev := a.holder
	a.holder = &m
	// 网关的凭据库属于守护进程的某一代：守护进程中途不可达、或换了一代（重启 / 重装，凭据库未必还是原来那份），
	// 都要在它重新就绪之后再对账一次，而不是只在这条连接上对账一回。
	if m.State != termproto.HolderReady || (prev != nil && prev.BootID != m.BootID) {
		a.credSynced = false
	}
	if a.lostSeen == nil {
		a.lostSeen = make(map[string]struct{})
	}
	var fresh []termproto.LostSession
	for _, l := range m.Lost {
		if _, seen := a.lostSeen[l.SID]; !seen {
			a.lostSeen[l.SID] = struct{}{}
			fresh = append(fresh, l)
		}
	}
	a.mu.Unlock()

	a.maybeSyncRuns()
	a.maybeSyncCreds()

	if prev == nil || prev.State != m.State || prev.Persistent != m.Persistent {
		detail := m.State + " persistent=" + strconv.FormatBool(m.Persistent)
		if m.Reason != "" {
			detail += " reason=" + m.Reason
		}
		if m.Version != "" {
			detail += " v=" + m.Version
		}
		result := ResultOK
		if m.State != termproto.HolderReady {
			result = ResultFail
		}
		a.hub.audit.recordLimited(a.ctx, a.code, auditEvent{Event: EvHolderState, Result: result, MachineCode: a.code, Detail: detail})
	}
	if len(fresh) > 0 {
		ends := make([]store.TerminalSessionEnd, 0, len(fresh))
		for _, l := range fresh {
			e := store.TerminalSessionEnd{SID: l.SID, Name: l.Name, StartedAt: l.Created, Reason: l.Reason}
			if l.At > 0 {
				e.EndedAt = time.Unix(l.At, 0)
			}
			ends = append(ends, e)
		}
		a.indexEnded(ends...)
		reasons := map[string]int{}
		for _, l := range fresh {
			reasons[l.Reason]++
		}
		parts := make([]string, 0, len(reasons))
		for r, n := range reasons {
			parts = append(parts, r+"="+strconv.Itoa(n))
		}
		sort.Strings(parts)
		a.hub.audit.recordLimited(a.ctx, a.code, auditEvent{
			Event: EvSessionsLost, Result: ResultFail, MachineCode: a.code,
			Detail: strconv.Itoa(len(fresh)) + " session(s): " + strings.Join(parts, " "),
		})
	}
}

func (a *agentConn) holderView() *HolderView {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.holder == nil {
		return nil
	}
	return &HolderView{
		State:      a.holder.State,
		Persistent: a.holder.Persistent,
		Version:    a.holder.Version,
		Rev:        a.holder.Rev,
		Started:    a.holder.Started,
		Reason:     a.holder.Reason,
		Lost:       append([]termproto.LostSession(nil), a.holder.Lost...),
		Caps:       append([]string(nil), a.holder.Caps...),
		DenyExec:   a.holder.DenyExec,
		Exec:       cloneExecInfo(a.holder.Exec),
	}
}

// dropDismissedLost 去掉管理员已经「忽略」的丢失会话提示（第六批 #28）。v 可以为 nil；Lost 是 holderView 复制出来的，原地过滤是安全的。
func (v *HolderView) dropDismissedLost(dismissed map[string]struct{}) {
	if v == nil || len(v.Lost) == 0 || len(dismissed) == 0 {
		return
	}
	kept := v.Lost[:0]
	for _, l := range v.Lost {
		if _, gone := dismissed[l.SID]; !gone {
			kept = append(kept, l)
		}
	}
	v.Lost = kept
}

func cloneExecInfo(in *termproto.ExecInfo) *termproto.ExecInfo {
	if in == nil {
		return nil
	}
	cp := *in
	return &cp
}

// ---------- 会话清单 ----------

func (a *agentConn) setSessions(in []termproto.SessionInfo) {
	clean := make([]termproto.SessionInfo, 0, len(in))
	for _, s := range in {
		if len(clean) >= termproto.MaxSessionsPerHost {
			break
		}
		if c, ok := s.Sanitized(); ok {
			clean = append(clean, c)
		}
	}
	a.mu.Lock()
	a.sessions = clean
	a.mu.Unlock()
	a.indexSessions()
	a.hub.screens.observe(a, clean)
}

// indexSessions 把当前会话清单交给索引协程。临时启用的身份不留痕：它们没有持久主机记录，也不记录终端内容。
func (a *agentConn) indexSessions() {
	if a.temp {
		return
	}
	a.hub.idx.syncList(a.code, a.sessionList())
}

// indexEnded 记录会话已结束及原因（同样不处理临时身份）。
func (a *agentConn) indexEnded(ends ...store.TerminalSessionEnd) {
	if a.temp {
		return
	}
	a.hub.idx.ended(a.code, ends)
}

func (a *agentConn) sessionList() []termproto.SessionInfo {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]termproto.SessionInfo, len(a.sessions))
	copy(out, a.sessions)
	return out
}

func (a *agentConn) findSession(sid string) (termproto.SessionInfo, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, s := range a.sessions {
		if s.SID == sid {
			return s, true
		}
	}
	return termproto.SessionInfo{}, false
}

// sessionInfo 实现 attachPeer。
func (a *agentConn) sessionInfo(sid string) (termproto.SessionInfo, bool) { return a.findSession(sid) }

func (a *agentConn) upsertSession(info termproto.SessionInfo) {
	a.mu.Lock()
	defer a.mu.Unlock()
	for i := range a.sessions {
		if a.sessions[i].SID == info.SID {
			a.sessions[i] = info
			return
		}
	}
	if len(a.sessions) < termproto.MaxSessionsPerHost {
		a.sessions = append(a.sessions, info)
	}
}

// ---------- 新建会话 ----------

// openSpec 是新建会话的参数。
type openSpec struct {
	Name      string
	Rows      uint16
	Cols      uint16
	Incognito bool
	// Compact：精简会话（小回放缓冲 + 限宽），会话守护预设启动的会话用。
	Compact bool
	IdleSec *int   // 本会话单独的空闲回收秒数（0 = 永久保持）；nil 沿用主机默认
	Cwd     string // 启动目录（必须是主机上存在的绝对路径，否则 Agent 忽略）
	// SSH 非 nil 表示这是一个经网关出站连接 SSH 目标的会话：目标只引用网关上已保存的凭据（Stored），帧里不带凭据。
	SSH *termproto.SSHTarget
}

// openSession 请求 Agent 新建会话并等待结果。
func (a *agentConn) openSession(ctx context.Context, spec openSpec) (termproto.SessionInfo, error) {
	if !a.isEnabled() {
		return termproto.SessionInfo{}, errAgentNotActive
	}
	a.mu.Lock()
	if len(a.sessions) >= termproto.MaxSessionsPerHost {
		a.mu.Unlock()
		return termproto.SessionInfo{}, errSessionLimit
	}
	if len(a.opens) >= maxPendingOpens {
		a.mu.Unlock()
		return termproto.SessionInfo{}, errTooManyOpens
	}
	req := randHex(8)
	result := make(chan openResult, 1)
	a.opens[req] = result
	a.mu.Unlock()

	defer func() {
		a.mu.Lock()
		delete(a.opens, req)
		a.mu.Unlock()
	}()

	rows, cols := termproto.ClampSize(spec.Rows, spec.Cols)
	open := termproto.Open{
		T:    termproto.TOpen,
		Req:  req,
		Name: termproto.SanitizeText(spec.Name, termproto.MaxSessionNameRunes),
		Rows: rows,
		Cols: cols,

		Incognito: spec.Incognito,
		Compact:   spec.Compact,
		Cwd:       termproto.SanitizeText(spec.Cwd, termproto.MaxCwdRunes),
		SSH:       spec.SSH,
	}
	if spec.IdleSec != nil {
		v := termproto.ClampIdle(*spec.IdleSec)
		open.IdleSec = &v
	}
	if err := a.writeJSON(open); err != nil {
		return termproto.SessionInfo{}, err
	}

	wait := a.hub.opts.OpenTimeout
	if spec.SSH != nil {
		wait += sshOpenAllowance
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case r := <-result:
		if r.reason != "" {
			code := ""
			if r.code != "" {
				code = termproto.SanitizeSSHCode(r.code)
			}
			return termproto.SessionInfo{}, &AgentOpenError{Code: code, Reason: termproto.SanitizeText(r.reason, 120)}
		}
		return r.info, nil
	case <-timer.C:
		return termproto.SessionInfo{}, errOpenTimeout
	case <-ctx.Done():
		return termproto.SessionInfo{}, ctx.Err()
	}
}

func (a *agentConn) onOpened(m termproto.Opened) {
	info, ok := m.Session.Sanitized()
	a.mu.Lock()
	ch := a.opens[m.Req]
	a.mu.Unlock()
	if ch == nil {
		return // 已超时或未知请求：忽略（Agent 侧会话仍会出现在下一次 sessions 全量更新里）
	}
	if !ok {
		select {
		case ch <- openResult{reason: "agent returned an invalid session"}:
		default:
		}
		return
	}
	a.upsertSession(info)
	a.indexSessions()
	select {
	case ch <- openResult{info: info}:
	default:
	}
}

func (a *agentConn) onOpenFailed(m termproto.OpenFailed) {
	a.mu.Lock()
	ch := a.opens[m.Req]
	a.mu.Unlock()
	if ch == nil {
		return
	}
	reason := m.Reason
	if reason == "" {
		reason = "unknown"
	}
	select {
	case ch <- openResult{reason: reason, code: m.Code}:
	default:
	}
}

// errSSHFrameTooLarge 表示一个 ssh_op 帧放不进控制帧（命令文本过长）：不发送，免得对端因超限而断开连接。
var errSSHFrameTooLarge = errors.New("ssh_op 帧超过控制帧上限")

// sshOp 请求网关执行一次性的管理类 SSH 操作（探测 / 登记 / 测试 / 凭据同步与删除）并等待结果。
// op.Target 在登记 / 修改凭据时含明文凭据：只写入这一帧，不记录、不缓存。
func (a *agentConn) sshOp(ctx context.Context, op termproto.SSHOp) (termproto.SSHResult, error) {
	return a.sshCall(ctx, op, false)
}

// sshExec 经网关在目标上执行一段脚本（命令行 + 标准输入），凭据由网关用自己保存的那份出示（target 必须是 Stored 引用）。
// 两路输出合计不超过 termproto.MaxSSHExecOut；Hub 驱动的远端作业（启动 / 轮询 / 取消 / 读取）全部走这条路。
func (a *agentConn) sshExec(ctx context.Context, target termproto.SSHTarget, cmd string, stdin []byte, maxOut int) (termproto.SSHResult, error) {
	return a.sshCall(ctx, termproto.SSHOp{Op: termproto.SSHOpExec, Target: target, Cmd: cmd, Stdin: stdin, MaxOut: maxOut}, true)
}

func (a *agentConn) sshCall(ctx context.Context, op termproto.SSHOp, exec bool) (termproto.SSHResult, error) {
	if !a.isEnabled() {
		return termproto.SSHResult{}, errAgentNotActive
	}
	req := randHex(8)
	op.T, op.Req = termproto.TSSHOp, req
	data, err := termproto.Marshal(op)
	if err != nil {
		return termproto.SSHResult{}, err
	}
	if len(data) > maxSSHOpFrame {
		return termproto.SSHResult{}, errSSHFrameTooLarge
	}

	a.mu.Lock()
	if exec {
		if a.sshExecPending >= maxPendingSSHExec {
			a.mu.Unlock()
			return termproto.SSHResult{}, errTooManyOpens
		}
		a.sshExecPending++
	} else if len(a.sshWaits)-a.sshExecPending >= maxPendingSSHOps {
		a.mu.Unlock()
		return termproto.SSHResult{}, errTooManyOpens
	}
	ch := make(chan sshWaitResult, 1)
	a.sshWaits[req] = ch
	a.mu.Unlock()
	defer func() {
		a.mu.Lock()
		delete(a.sshWaits, req)
		if exec {
			a.sshExecPending--
		}
		a.mu.Unlock()
	}()

	if err := a.write(websocket.MessageText, data); err != nil {
		return termproto.SSHResult{}, err
	}
	timer := time.NewTimer(sshOpWait)
	defer timer.Stop()
	select {
	case r := <-ch:
		if r.gone {
			return termproto.SSHResult{}, errAgentClosed
		}
		return r.res, nil
	case <-timer.C:
		return termproto.SSHResult{}, errOpenTimeout
	case <-ctx.Done():
		return termproto.SSHResult{}, ctx.Err()
	}
}

// onSSHResult 把净化后的 ssh_result 交给等待它的请求；未知 / 已超时的请求直接忽略。
func (a *agentConn) onSSHResult(m termproto.SSHResult) {
	a.mu.Lock()
	ch := a.sshWaits[m.Req]
	a.mu.Unlock()
	if ch == nil {
		return
	}
	select {
	case ch <- sshWaitResult{res: m}:
	default:
	}
}

// hasHolderCap 报告会话守护进程是否可用且声明了某项能力。
func (a *agentConn) hasHolderCap(capName string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	h := a.holder
	return h != nil && h.State == termproto.HolderReady && slices.Contains(h.Caps, capName)
}

// onExit 记录会话退出并通知该会话上的全部浏览器附着。
func (a *agentConn) onExit(m termproto.Exit) {
	if !termproto.ValidSessionID(m.SID) {
		return
	}
	a.mu.Lock()
	for i := range a.sessions {
		if a.sessions[i].SID == m.SID {
			a.sessions[i].State = termproto.SessionExited
			a.sessions[i].ExitCode = m.Code
		}
	}
	var targets []*attachment
	for _, att := range a.chans {
		if att.sid == m.SID {
			targets = append(targets, att)
		}
	}
	a.mu.Unlock()
	a.indexSessions()

	payload := termproto.BrowserExit{T: termproto.TExit, Code: m.Code, Signal: termproto.SanitizeText(m.Signal, 32)}
	for _, att := range targets {
		att.sendControl(payload)
	}
}

// ---------- 浏览器附着登记 ----------

// addAttachment 分配数据通道号（写入 att.ch）并登记附着；整个过程在同一把锁内完成。
func (a *agentConn) addAttachment(att *attachment) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return errAgentClosed
	}
	if len(a.chans) >= 4*termproto.MaxSessionsPerHost {
		return errors.New("too many attachments on this host")
	}
	att.ch = a.nextCh
	a.nextCh++
	a.chans[att.ch] = att
	return nil
}

func (a *agentConn) removeAttachment(ch uint32) {
	a.mu.Lock()
	delete(a.chans, ch)
	a.mu.Unlock()
}

func (a *agentConn) attachment(ch uint32) *attachment {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.chans[ch]
}

func (a *agentConn) closeAttachments(code websocket.StatusCode, reason string) {
	a.mu.Lock()
	list := make([]*attachment, 0, len(a.chans))
	for _, att := range a.chans {
		list = append(list, att)
	}
	a.mu.Unlock()
	for _, att := range list {
		att.end(code, reason)
	}
}

// attachedCount 返回该 Agent 上的浏览器附着数量。
func (a *agentConn) attachedCount() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.chans)
}

func logAgent(a *agentConn, format string, args ...any) {
	log.Printf("[terminal] agent %s: "+format, append([]any{a.code}, args...)...)
}
