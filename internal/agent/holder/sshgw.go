package holder

import (
	"context"
	"errors"
	"fmt"
	"net"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/teacat99/RelayMesh/v2/internal/agent/ptyhost"
	"github.com/teacat99/RelayMesh/v2/internal/agent/sshcred"
	"github.com/teacat99/RelayMesh/v2/internal/sshexec"
	"github.com/teacat99/RelayMesh/v2/internal/termproto"
)

// SSH 网关：本机用 --ssh-gateway 显式开启后，会话守护进程可以替 Hub 去连接 SSH 目标并持有这条连接。
//
// 不变量：
//   - 没有开启（Manager.ssh == nil）就拒绝一切 SSH 目标与 ssh_op；
//   - 主机公钥必须随请求给出，连接时逐字节比对，不做首次信任；probe 是唯一不带凭据、也不要求固定的操作；
//   - 目标的登录凭据只保存在这台网关自己的密封凭据库里（sshcred）：只有 check（登记 / 改凭据）会带着明文凭据进来，
//     验证登录成功之后才落库；此后的 open / exec 都只引用主机码（stored），Hub 不再保管也不再发送凭据；
//   - 凭据不写入会话信息 / 记录 / 日志，错误文本也只描述类别；
//   - 建连数、ssh_op 数与 exec 代理数各有并发上限，超过立即返回 busy，不排队堆积；
//   - 本机可以用允许名单收紧能被当作跳板的目标范围；用 --deny-model-exec 一票否决模型执行（ssh exec 同样受约束）。

// SSHOptions 是 SSH 网关的本机选项；非 nil 即表示已开启网关能力。
type SSHOptions struct {
	// Allow 是允许连接的目标：CIDR（10.0.0.0/8）、单个 IP、或主机名（可含 * 通配，如 *.corp.example）。
	// 为空表示不限制。
	Allow []string
	// DialTimeout 是单次建连（含握手与认证）的上限；<=0 取默认。
	DialTimeout time.Duration
	// MaxConcurrent 是同时进行的建连 / 管理类 ssh_op 数上限；<=0 取默认。
	MaxConcurrent int
	// ExecConcurrent 是同时进行的 exec 代理数上限（Hub 经网关管理远端作业会很频繁）；<=0 取默认。
	ExecConcurrent int
	// CredDir 是凭据库目录（通常是 <数据目录>/ssh-gateway）。为空表示没有可用的凭据库：网关无法保存凭据，
	// 也就不会宣告「可托管凭据」，Hub 不会把主机登记到它上面。
	CredDir string
	// CredKey 是外置的凭据库密封密钥（RELAYMESH_AGENT_SSH_CRED_KEY，至少 16 个字符）；为空则使用目录里自动生成的 key 文件。
	CredKey string
	// Harden 收紧凭据库目录的访问权限（Windows 上设置 ACL）；nil 表示只依赖文件权限。
	Harden func(dir string) error
}

const (
	defaultSSHDialTimeout = 12 * time.Second
	defaultSSHConcurrent  = 4
	defaultSSHExecConc    = 32
	sshOpTimeout          = 20 * time.Second
	// sshExecTimeout 比 Hub 单次调用的上限（25 秒）略短，这样超时的是网关而不是 Hub 的等待，错误信息更准确。
	sshExecTimeout = 23 * time.Second
	sshShellName   = "ssh"
	// maxSSHExecCmd 是 exec 代理的命令行长度上限：Hub 只会发 "sh -s" 一类的短命令，脚本走 stdin。
	maxSSHExecCmd = 512
)

// SSHOpenError 是 SSH 会话 / 操作失败的结构化错误：Code 是稳定的 termproto.SSHCode*，Msg 只含类别说明。
type SSHOpenError struct {
	Code string
	Msg  string
}

func (e *SSHOpenError) Error() string { return e.Msg }

func sshFail(code, format string, args ...any) *SSHOpenError {
	return &SSHOpenError{Code: code, Msg: fmt.Sprintf(format, args...)}
}

// sshGateway 是 Manager 里 SSH 网关的状态。
type sshGateway struct {
	allow   *allowList
	dial    time.Duration
	sem     chan struct{} // 建连与管理类 ssh_op
	execSem chan struct{} // exec 代理（高频、短小，与管理类互不挤占）
	pool    *sshexec.Pool

	store    *sshcred.Store // nil 表示凭据库不可用（storeErr 说明原因）
	storeErr error

	// epochs 记录每台主机凭据的「世代」：凭据被改写 / 删除后加一，连接池的键包含它，旧凭据建立的连接随之作废。
	mu     sync.Mutex
	epochs map[string]uint64
}

func newSSHGateway(o *SSHOptions) (*sshGateway, error) {
	al, err := parseAllowList(o.Allow)
	if err != nil {
		return nil, err
	}
	dial, conc, execConc := o.DialTimeout, o.MaxConcurrent, o.ExecConcurrent
	if dial <= 0 {
		dial = defaultSSHDialTimeout
	}
	if conc <= 0 {
		conc = defaultSSHConcurrent
	}
	if execConc <= 0 {
		execConc = defaultSSHExecConc
	}
	g := &sshGateway{
		allow: al, dial: dial,
		sem:     make(chan struct{}, conc),
		execSem: make(chan struct{}, execConc),
		pool:    &sshexec.Pool{DialTimeout: dial},
		epochs:  map[string]uint64{},
	}
	if o.CredDir == "" {
		g.storeErr = errors.New("没有指定凭据库目录")
	} else if st, serr := sshcred.Open(sshcred.Options{Dir: o.CredDir, Key: o.CredKey, Harden: o.Harden}); serr != nil {
		g.storeErr = serr
	} else {
		g.store = st
	}
	return g, nil
}

// storeReady 报告凭据库是否可用：不可用时网关不能托管凭据，不会向 Hub 宣告 ssh_store 能力。
func (g *sshGateway) storeReady() bool { return g.store != nil }

// maintain 周期性回收空闲连接。
func (g *sshGateway) maintain(now time.Time) { g.pool.Reap(now) }

// shutdown 关闭连接池里的全部连接。
func (g *sshGateway) shutdown() { g.pool.Close() }

// bumpEpoch 让某台主机已缓存的连接全部作废（凭据被改写或删除时调用）。
func (g *sshGateway) bumpEpoch(id string) {
	g.mu.Lock()
	g.epochs[id]++
	g.mu.Unlock()
}

// poolKey 是连接池键：主机码 + 凭据世代 + 目标地址 / 用户 / 固定的主机密钥，任何一项变化都不会复用旧连接。
func (g *sshGateway) poolKey(id string, sp sshexec.Spec) string {
	g.mu.Lock()
	ep := g.epochs[id]
	g.mu.Unlock()
	return id + "|" + strconv.FormatUint(ep, 10) + "|" + sp.Addr + "|" + sp.User + "|" + sp.HostKey.Fingerprint
}

// acquire 占用一个建连名额；已满则立即失败。
func (g *sshGateway) acquire() (release func(), err *SSHOpenError) {
	select {
	case g.sem <- struct{}{}:
		return func() { <-g.sem }, nil
	default:
		return nil, sshFail(termproto.SSHCodeBusy, "网关正在处理太多 SSH 连接，请稍后再试")
	}
}

// acquireExec 占用一个 exec 代理名额；已满则立即失败。
func (g *sshGateway) acquireExec() (release func(), err *SSHOpenError) {
	select {
	case g.execSem <- struct{}{}:
		return func() { <-g.execSem }, nil
	default:
		return nil, sshFail(termproto.SSHCodeBusy, "网关正在处理太多 SSH 命令，请稍后再试")
	}
}

// sshCode 把传输层错误翻译成稳定的错误码与只含类别的说明。
func sshCode(err error) (code, msg string) {
	var oe *SSHOpenError
	switch {
	case errors.As(err, &oe):
		return oe.Code, oe.Msg
	case errors.Is(err, sshexec.ErrHostKeyMismatch):
		return termproto.SSHCodeHostKey, "主机密钥与已固定的不一致：已停止连接，需要核对指纹后重新确认"
	case errors.Is(err, sshexec.ErrNoHostKey):
		return termproto.SSHCodeNoHostKey, "这台主机还没有固定的主机密钥，拒绝连接"
	case errors.Is(err, sshexec.ErrAuthFailed):
		return termproto.SSHCodeAuth, "SSH 认证失败：请检查用户名与凭据"
	case errors.Is(err, sshexec.ErrPassphrase):
		return termproto.SSHCodePassphrase, "私钥已加密，请同时提供口令"
	case errors.Is(err, sshexec.ErrBadCredential), errors.Is(err, sshexec.ErrBadAddress):
		return termproto.SSHCodeBadRequest, err.Error()
	case errors.Is(err, sshexec.ErrUnsupported):
		return termproto.SSHCodeUnsupported, err.Error()
	case errors.Is(err, sshexec.ErrTooLarge):
		return termproto.SSHCodeInternal, "远端返回的数据超过了网关的上限"
	case errors.Is(err, context.DeadlineExceeded):
		return termproto.SSHCodeUnreachable, "连接超时"
	case errors.Is(err, context.Canceled):
		return termproto.SSHCodeUnreachable, "连接已取消"
	case errors.Is(err, sshexec.ErrUnreachable), errors.Is(err, sshexec.ErrConn), errors.Is(err, sshexec.ErrProtocol):
		return termproto.SSHCodeUnreachable, err.Error()
	}
	return termproto.SSHCodeInternal, "操作失败"
}

func asSSHError(err error) *SSHOpenError {
	if err == nil {
		return nil
	}
	var oe *SSHOpenError
	if errors.As(err, &oe) {
		return oe
	}
	code, msg := sshCode(err)
	return &SSHOpenError{Code: code, Msg: msg}
}

// credSource 说明 spec 允许从哪里取得登录凭据。
type credSource int

const (
	credNone   credSource = iota // 不需要凭据（probe：只校验地址）
	credStored                   // 只认网关凭据库里的（open / exec）
	credInline                   // 凭据库里的（stored）或请求里随带的（check：登记 / 修改凭据时的验证）
)

// loadStored 从凭据库取出某台主机的登录凭据。
func (g *sshGateway) loadStored(id string) (sshexec.Credential, error) {
	if !termproto.ValidMachineCode(id) {
		return sshexec.Credential{}, sshFail(termproto.SSHCodeBadRequest, "SSH 主机标识无效")
	}
	if g.store == nil {
		return sshexec.Credential{}, sshFail(termproto.SSHCodeStore, "网关的凭据库不可用：%v", g.storeErr)
	}
	cred, err := g.store.Get(id)
	switch {
	case err == nil:
		return cred, nil
	case errors.Is(err, sshcred.ErrNotFound):
		return sshexec.Credential{}, sshFail(termproto.SSHCodeNoCred, "这台网关上没有该主机的凭据，请在主机设置里重新填写登录凭据")
	case errors.Is(err, sshcred.ErrUnreadable):
		return sshexec.Credential{}, sshFail(termproto.SSHCodeNoCred, "网关上保存的凭据无法解开（密钥已变更？），请重新填写登录凭据")
	}
	return sshexec.Credential{}, sshFail(termproto.SSHCodeStore, "读取网关凭据库失败")
}

// spec 校验目标并还原成连接规格。src 决定凭据来源：见 credSource。
func (g *sshGateway) spec(ctx context.Context, t *termproto.SSHTarget, src credSource) (sshexec.Spec, error) {
	addr, err := sshexec.NormalizeAddr(t.Addr)
	if err != nil {
		return sshexec.Spec{}, err
	}
	if err := g.allow.permits(ctx, addr); err != nil {
		return sshexec.Spec{}, err
	}
	sp := sshexec.Spec{Addr: addr, User: t.User}
	if src == credNone {
		return sp, nil
	}
	if !sshexec.ValidUser(t.User) {
		return sshexec.Spec{}, sshFail(termproto.SSHCodeBadRequest, "用户名无效")
	}
	inline := t.Password != "" || t.PrivateKey != "" || t.Passphrase != ""
	var cred sshexec.Credential
	switch {
	case t.Stored:
		if inline { // 协议上 stored 与随带凭据互斥：同时出现说明对端实现有误，宁可拒绝
			return sshexec.Spec{}, sshFail(termproto.SSHCodeBadRequest, "stored 目标不应同时携带凭据")
		}
		if cred, err = g.loadStored(t.ID); err != nil {
			return sshexec.Spec{}, err
		}
	case src == credInline:
		cred = sshexec.Credential{Kind: t.Auth, Password: t.Password, PrivateKey: t.PrivateKey, Passphrase: t.Passphrase}
		if err := cred.Validate(); err != nil {
			return sshexec.Spec{}, err
		}
	default:
		return sshexec.Spec{}, sshFail(termproto.SSHCodeBadRequest, "该操作只接受保存在网关上的凭据（stored）")
	}
	hk, err := sshexec.DecodeHostKey(t.HostKey)
	if err != nil {
		return sshexec.Spec{}, sshexec.ErrNoHostKey
	}
	sp.Cred, sp.HostKey = cred, hk
	return sp, nil
}

// openTerminal 连接目标、申请伪终端并启动 shell，返回可以交给会话的终端。阻塞到建连完成（或失败）。
// 终端会话是独占的长连接，不走连接池；凭据只接受已保存在网关上的（stored）。
func (g *sshGateway) openTerminal(t *termproto.SSHTarget, rows, cols uint16) (ptyhost.Terminal, error) {
	release, ferr := g.acquire()
	if ferr != nil {
		return nil, ferr
	}
	defer release()
	ctx, cancel := context.WithTimeout(context.Background(), g.dial+5*time.Second)
	defer cancel()
	sp, err := g.spec(ctx, t, credStored)
	if err != nil {
		return nil, asSSHError(err)
	}
	c, err := sshexec.Dial(ctx, sp, g.dial)
	if err != nil {
		return nil, asSSHError(err)
	}
	sh, err := c.OpenShell(sshexec.ShellOptions{Rows: rows, Cols: cols})
	if err != nil {
		return nil, asSSHError(err)
	}
	return &sshTerm{sh: sh}, nil
}

// storeFail 把凭据库的错误翻译成只含类别的说明（路径、系统错误文本不外传）。
func storeFail(err error) *SSHOpenError {
	switch {
	case errors.Is(err, sshcred.ErrFull):
		return sshFail(termproto.SSHCodeStore, "网关上保存的凭据已达上限（%d 条），请先清理不用的 SSH 主机", sshcred.MaxItems)
	case errors.Is(err, sshcred.ErrBadID):
		return sshFail(termproto.SSHCodeBadRequest, "SSH 主机标识无效")
	}
	return sshFail(termproto.SSHCodeStore, "读写网关凭据库失败")
}

// requireStore 保证凭据库可用，否则给出带原因的 cred_store 错误。
func (g *sshGateway) requireStore() *SSHOpenError {
	if g.store == nil {
		return sshFail(termproto.SSHCodeStore, "网关的凭据库不可用：%v", g.storeErr)
	}
	return nil
}

// op 执行一次性的 ssh_op，结果总是一个 ssh_result（失败也不返回 error）。
func (g *sshGateway) op(m termproto.SSHOp) termproto.SSHResult {
	res := termproto.SSHResult{T: termproto.TSSHResult, Req: m.Req}
	fail := func(err error) termproto.SSHResult {
		oe := asSSHError(err)
		res.OK, res.Code, res.Msg = false, oe.Code, oe.Msg
		return res
	}
	acquire, timeout := g.acquire, sshOpTimeout
	if m.Op == termproto.SSHOpExec {
		acquire, timeout = g.acquireExec, sshExecTimeout
	}
	release, ferr := acquire()
	if ferr != nil {
		return fail(ferr)
	}
	defer release()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	switch m.Op {
	case termproto.SSHOpProbe:
		sp, err := g.spec(ctx, &m.Target, credNone)
		if err != nil {
			return fail(err)
		}
		hk, err := sshexec.Probe(ctx, sp.Addr, g.dial)
		if err != nil {
			return fail(err)
		}
		res.OK, res.Addr, res.KeyType, res.HostKey, res.Fingerprint = true, sp.Addr, hk.Type, hk.Encode(), hk.Fingerprint
		return res

	case termproto.SSHOpCheck:
		if m.Persist {
			// 只有「随带凭据」的检查才需要落库；引用已保存凭据（stored）的检查没有新东西可存。
			if m.Target.Stored {
				return fail(sshFail(termproto.SSHCodeBadRequest, "persist 只用于随带凭据的检查"))
			}
			if !termproto.ValidMachineCode(m.Target.ID) {
				return fail(sshFail(termproto.SSHCodeBadRequest, "SSH 主机标识无效"))
			}
			if serr := g.requireStore(); serr != nil {
				return fail(serr)
			}
		}
		sp, err := g.spec(ctx, &m.Target, credInline)
		if err != nil {
			return fail(err)
		}
		start := time.Now()
		c, err := sshexec.Dial(ctx, sp, g.dial)
		if err != nil {
			return fail(err)
		}
		defer c.Close()
		res.OK, res.Addr = true, sp.Addr
		// 环境信息只是锦上添花：有些目标（网络设备、受限 shell）不允许 exec，但终端照常可用，所以失败不算错误。
		// 能解析出 POSIX 环境信息才说明目标可以跑远端作业脚本（模型执行）；否则这台主机只能当终端用。
		cmd, stdin := sshexec.FactsCommand()
		if out, err := c.Exec(ctx, cmd, stdin, 0); err == nil {
			if f, perr := sshexec.ParseFacts(out.Stdout); perr == nil {
				res.Posix = true
				res.Host, res.OS, res.Arch, res.User, res.Home = f.Host, f.OS, f.Arch, f.User, f.Home
			}
		}
		res.LatencyMS = time.Since(start).Milliseconds()
		// 凭据只在真实登录成功之后才落库：填错的密码绝不会替换掉原来可用的凭据。
		if m.Persist {
			if err := g.store.Put(m.Target.ID, sp.Cred); err != nil {
				return fail(storeFail(err))
			}
			g.bumpEpoch(m.Target.ID)
		}
		return res

	case termproto.SSHOpExec:
		// 通用的 SSH exec 代理：Hub 把远端作业脚本（sh -s + stdin）经网关送到目标，凭据由网关自己出示。
		if m.Cmd == "" || len(m.Cmd) > maxSSHExecCmd || strings.ContainsRune(m.Cmd, 0) {
			return fail(sshFail(termproto.SSHCodeBadRequest, "命令无效"))
		}
		sp, err := g.spec(ctx, &m.Target, credStored)
		if err != nil {
			return fail(err)
		}
		maxOut := m.MaxOut
		if maxOut <= 0 || maxOut > termproto.MaxSSHExecOut {
			maxOut = termproto.MaxSSHExecOut
		}
		out, err := g.pool.Exec(ctx, g.poolKey(m.Target.ID, sp), sp, m.Cmd, m.Stdin, maxOut)
		if err != nil {
			return fail(err)
		}
		// maxOut 是两路输出合计的上限（单路上限已由连接层保证）：应答帧必须放得进 64 KiB 的控制帧，超限的帧会让 Hub 断开连接。
		if len(out.Stdout)+len(out.Stderr) > maxOut {
			return fail(sshexec.ErrTooLarge)
		}
		res.OK, res.Stdout, res.Stderr, res.Exit = true, out.Stdout, out.Stderr, out.Exit
		return res

	case termproto.SSHOpCredDel:
		if !termproto.ValidMachineCode(m.Target.ID) {
			return fail(sshFail(termproto.SSHCodeBadRequest, "SSH 主机标识无效"))
		}
		if serr := g.requireStore(); serr != nil {
			return fail(serr)
		}
		if _, err := g.store.Delete(m.Target.ID); err != nil {
			return fail(storeFail(err))
		}
		g.bumpEpoch(m.Target.ID)
		res.OK = true
		return res

	case termproto.SSHOpCredSync:
		// Hub 在网关（重新）就绪时发来仍然需要的主机清单：网关回收清单之外的凭据（主机已删除却没来得及通知），
		// 并回报自己实际持有哪些，Hub 据此标出「凭据缺失」的主机。
		if serr := g.requireStore(); serr != nil {
			return fail(serr)
		}
		have, dropped, err := g.store.Retain(m.Keep)
		if err != nil {
			return fail(storeFail(err))
		}
		for _, id := range dropped {
			g.bumpEpoch(id)
		}
		res.OK, res.Have, res.Dropped = true, have, len(dropped)
		return res
	}
	return fail(sshFail(termproto.SSHCodeBadRequest, "未知的 SSH 操作"))
}

// ---------- 终端适配 ----------

// sshTerm 把 sshexec.Shell 适配成会话使用的 ptyhost.Terminal。
type sshTerm struct{ sh *sshexec.Shell }

func (t *sshTerm) Read(p []byte) (int, error)  { return t.sh.Read(p) }
func (t *sshTerm) Write(p []byte) (int, error) { return t.sh.Write(p) }
func (t *sshTerm) Resize(rows, cols uint16) error {
	return t.sh.Resize(rows, cols)
}
func (t *sshTerm) Pid() int { return 0 }
func (t *sshTerm) Wait() ptyhost.ExitStatus {
	ex := t.sh.Wait()
	return ptyhost.ExitStatus{Code: ex.Code, Signal: ex.Signal}
}
func (t *sshTerm) Kill()                           { t.sh.Kill() }
func (t *sshTerm) SetReadDeadline(time.Time) error { return ptyhost.ErrUnsupported }
func (t *sshTerm) Close() error                    { return t.sh.Close() }

// ---------- 允许名单 ----------

// allowList 是网关本机配置的目标范围。nil 表示不限制。
type allowList struct {
	nets  []*net.IPNet
	names []string // 小写的主机名模式
}

func parseAllowList(entries []string) (*allowList, error) {
	var al allowList
	for _, raw := range entries {
		e := strings.ToLower(strings.TrimSpace(raw))
		if e == "" {
			continue
		}
		switch {
		case strings.Contains(e, "/"):
			_, n, err := net.ParseCIDR(e)
			if err != nil {
				return nil, fmt.Errorf("--ssh-allow 中的 %q 不是有效的 CIDR", raw)
			}
			al.nets = append(al.nets, n)
		case net.ParseIP(e) != nil:
			ip := net.ParseIP(e)
			bits := 128
			if ip4 := ip.To4(); ip4 != nil {
				ip, bits = ip4, 32
			}
			al.nets = append(al.nets, &net.IPNet{IP: ip, Mask: net.CIDRMask(bits, bits)})
		default:
			if _, err := path.Match(e, "x"); err != nil || strings.ContainsAny(e, " \t/@:") {
				return nil, fmt.Errorf("--ssh-allow 中的 %q 不是有效的主机名模式", raw)
			}
			al.names = append(al.names, e)
		}
	}
	if len(al.nets) == 0 && len(al.names) == 0 {
		return nil, nil
	}
	return &al, nil
}

func (a *allowList) contains(ip net.IP) bool {
	for _, n := range a.nets {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

// permits 判断 host:port 形式的目标是否在允许范围内。nil 名单一律放行。
// 目标是主机名时：先按名字模式匹配；否则解析出全部 IP，必须每一个都落在允许的网段里。
func (a *allowList) permits(ctx context.Context, addr string) error {
	if a == nil {
		return nil
	}
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		host = addr
	}
	host = strings.ToLower(strings.Trim(host, "[]"))
	deny := sshFail(termproto.SSHCodeDenied, "目标不在这台网关的允许名单内（--ssh-allow）")
	if ip := net.ParseIP(host); ip != nil {
		if a.contains(ip) {
			return nil
		}
		return deny
	}
	for _, p := range a.names {
		if ok, _ := path.Match(p, host); ok {
			return nil
		}
	}
	if len(a.nets) == 0 {
		return deny
	}
	rctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	ips, err := net.DefaultResolver.LookupIPAddr(rctx, host)
	if err != nil || len(ips) == 0 {
		return deny
	}
	for _, ia := range ips {
		if !a.contains(ia.IP) {
			return deny
		}
	}
	return nil
}
