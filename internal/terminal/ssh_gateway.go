package terminal

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/teacat99/RelayMesh/v2/internal/model"
	"github.com/teacat99/RelayMesh/v2/internal/sshexec"
	"github.com/teacat99/RelayMesh/v2/internal/store"
	"github.com/teacat99/RelayMesh/v2/internal/termexec"
	"github.com/teacat99/RelayMesh/v2/internal/termproto"
)

// SSH 网关接入：让「没有安装 Agent、Hub 也够不着」的 SSH 主机同时提供人工终端与模型执行。
//
// 网关是一台本机用 --ssh-gateway 开启了该能力的普通 Agent 主机。登记 / 测试 / 重新确认指纹时由网关出站去连目标
// （ssh_op）；打开终端时在 open 帧里带上目标，由网关上的会话守护进程建连并持有会话——所以浏览器刷新、Hub 重启都不会中断它；
// 模型执行时 Hub 驱动远端作业（启动 / 轮询 / 取消 / 读取），每一步都是一次经网关代理的 SSH exec（ssh_op exec）。
//
// 不变量：
//   - 登录凭据只保存在网关自己的密封凭据库里（Agent 数据目录，见 internal/agent/sshcred）：Hub 一份副本都不留，也不需要
//     RELAYMESH_SSH_SECRET_KEY。凭据只在「登记 / 重新填写」那一帧里临时出现，网关真实登录成功之后才落库；
//     此后的 open / exec 帧只带 stored 引用，网关按机器码取用；
//   - Hub 绝不直连经网关接入的主机（sshexec.Spec.Via 非空时 Dial 直接拒绝）；
//   - 主机密钥照常严格固定：探测是唯一不校验密钥的操作，其余一律带着已固定的公钥，不一致即拒绝并置 mismatch；
//   - 网关本机拥有一票否决（--deny-model-exec）与目标允许名单（--ssh-allow）：前者拒绝一切模型执行，后者限制凭据能用在哪些地址。

// 网关相关的稳定错误码（REST 层）。
const (
	codeGatewayInvalid  = "ssh_gateway_invalid"  // 指定的网关主机不存在、已停用或不是装了 Agent 的主机
	codeGatewayOffline  = "ssh_gateway_offline"  // 网关主机当前不在线
	codeGatewayDisabled = "ssh_gateway_disabled" // 网关主机没有开启 SSH 网关能力
	codeGatewayNoStore  = "ssh_gateway_no_store" // 网关主机的凭据库不可用（旧版本，或数据目录不可写）
	codeGatewayStore    = "ssh_gateway_store"    // 网关主机读写自己的凭据库失败
	codeGatewayTimeout  = "ssh_gateway_timeout"  // 网关主机没有在规定时间内应答
	codeGatewayError    = "ssh_gateway_error"    // 网关主机返回了无法理解的结果
	codeTargetDenied    = "ssh_target_denied"    // 目标不在网关本机的允许名单内
	codeExecDenied      = "ssh_exec_denied"      // 网关本机一票否决了模型执行（--deny-model-exec）
	codeRequestTooLarge = "ssh_request_too_large"
	codeNotViaGateway   = "ssh_wrong_gateway" // 这台 SSH 主机不是经这台网关接入的
)

// verifyMode 说明 verify 用哪里的凭据，以及要不要让网关把凭据保存下来。
type verifyMode int

const (
	verifyInline  verifyMode = iota // 用 spec.Cred 登录（直连总是这样）
	verifyPersist                   // 经网关：用 spec.Cred 登录，成功之后由网关以机器码为键保存（登记 / 重新填写凭据）
	verifyStored                    // 经网关：用网关里已保存的凭据登录（复测 / 只改地址或用户）
)

// verifyOutcome 是一次真实登录的结果。
type verifyOutcome struct {
	Facts   sshexec.Facts
	Latency time.Duration
	// Posix 表示目标是受支持的 POSIX 环境（有 sh 与 base64），能被模型执行。直连要求目标必须是 POSIX（否则 verify 直接失败），
	// 所以直连恒为 true；经网关接入的主机则允许不是：它仍可以当人工终端用。
	Posix bool
}

// sshConnector 是「用一份连接信息真实地连一次目标」的两条途径：Hub 直连，或经网关主机代连。
// 管理类操作（探测 / 登记 / 测试 / 改连接 / 重新确认指纹）都只依赖这个接口，所以两种接入方式走同一套校验。
type sshConnector interface {
	// probe 只做密钥交换取回对方的主机公钥，不发送任何认证信息。
	probe(ctx context.Context, address string) (*SSHProbeResult, *sshexec.HostKey, error)
	// verify 用固定的主机公钥与凭据真实登录一次并探测环境。id 是这台 SSH 主机的机器码（网关按它保存 / 取用凭据）。
	verify(ctx context.Context, id string, spec sshexec.Spec, mode verifyMode) (verifyOutcome, error)
}

// directConnector 由 Hub 自己出站连接目标。
type directConnector struct{ b *sshBackend }

func (d directConnector) probe(ctx context.Context, address string) (*SSHProbeResult, *sshexec.HostKey, error) {
	return d.b.probe(ctx, address)
}

func (d directConnector) verify(ctx context.Context, _ string, spec sshexec.Spec, mode verifyMode) (verifyOutcome, error) {
	if mode != verifyInline {
		return verifyOutcome{}, sshErr(http.StatusInternalServerError, "internal_error", "直连不使用网关保存的凭据")
	}
	facts, took, err := d.b.verify(ctx, spec)
	if err != nil {
		return verifyOutcome{}, err
	}
	return verifyOutcome{Facts: facts, Latency: took, Posix: true}, nil
}

// gatewayConnector 让网关主机上的会话守护进程出站连接目标。
type gatewayConnector struct {
	a    *agentConn
	code string // 网关的机器码（写审计用）
}

func (g gatewayConnector) probe(ctx context.Context, address string) (*SSHProbeResult, *sshexec.HostKey, error) {
	addr, err := sshexec.NormalizeAddr(address)
	if err != nil {
		return nil, nil, mapSSHErr(err)
	}
	res, err := g.a.sshOp(ctx, termproto.SSHOp{Op: termproto.SSHOpProbe, Target: termproto.SSHTarget{Addr: addr}})
	if err != nil {
		return nil, nil, gatewayTransportErr(err)
	}
	if !res.OK {
		return nil, nil, gatewayResultErr(res)
	}
	hk, derr := sshexec.DecodeHostKey(res.HostKey)
	if derr != nil || hk.Fingerprint != res.Fingerprint {
		return nil, nil, sshErr(http.StatusBadGateway, codeGatewayError, "网关主机返回的主机密钥无效")
	}
	return &SSHProbeResult{Address: addr, KeyType: hk.Type, Fingerprint: hk.Fingerprint}, hk, nil
}

func (g gatewayConnector) verify(ctx context.Context, id string, spec sshexec.Spec, mode verifyMode) (verifyOutcome, error) {
	var t termproto.SSHTarget
	var err error
	if mode == verifyStored {
		t, err = storedTarget(id, spec)
	} else {
		t, err = inlineTarget(id, spec)
	}
	if err != nil {
		return verifyOutcome{}, err
	}
	res, err := g.a.sshOp(ctx, termproto.SSHOp{Op: termproto.SSHOpCheck, Target: t, Persist: mode == verifyPersist})
	t.Password, t.PrivateKey, t.Passphrase = "", "", ""
	if err != nil {
		return verifyOutcome{}, gatewayTransportErr(err)
	}
	if !res.OK {
		return verifyOutcome{}, gatewayResultErr(res)
	}
	out := verifyOutcome{
		Facts:   sshexec.Facts{Host: res.Host, OS: res.OS, Arch: res.Arch, User: res.User, Home: res.Home},
		Latency: time.Duration(res.LatencyMS) * time.Millisecond,
		Posix:   res.Posix,
	}
	return out, nil
}

// inlineTarget 把连接信息（含凭据）折算成发给网关的目标：只用于登记 / 重新填写凭据时的 check——凭据随这一帧临时出现，
// 网关真实登录成功之后才落入自己的凭据库。id 是这台 SSH 主机的机器码（网关按它保存凭据）。
func inlineTarget(id string, spec sshexec.Spec) (termproto.SSHTarget, error) {
	t, err := storedTarget(id, spec)
	if err != nil {
		return t, err
	}
	t.Stored = false
	t.Auth = spec.Cred.Kind
	switch spec.Cred.Kind {
	case sshexec.AuthPassword:
		t.Password = spec.Cred.Password
	case sshexec.AuthKey:
		t.PrivateKey, t.Passphrase = spec.Cred.PrivateKey, spec.Cred.Passphrase
	}
	return t, nil
}

// storedTarget 折算成引用网关已保存凭据的目标：帧里不带任何凭据，网关按机器码取用。open / exec / 复测都用它。
func storedTarget(id string, spec sshexec.Spec) (termproto.SSHTarget, error) {
	if spec.HostKey == nil {
		return termproto.SSHTarget{}, sshexec.ErrNoHostKey
	}
	return termproto.SSHTarget{ID: id, Addr: spec.Addr, User: spec.User, HostKey: spec.HostKey.Encode(), Stored: true}, nil
}

// gatewayTransportErr 翻译「请求没能送达网关 / 没等到应答」这类错误。
func gatewayTransportErr(err error) *SSHError {
	switch {
	case errors.Is(err, errAgentNotActive), errors.Is(err, errAgentClosed):
		return sshErr(http.StatusConflict, codeGatewayOffline, "网关主机当前不在线")
	case errors.Is(err, errOpenTimeout):
		return sshErr(http.StatusGatewayTimeout, codeGatewayTimeout, "网关主机没有在规定时间内应答")
	case errors.Is(err, errTooManyOpens):
		return sshErr(http.StatusTooManyRequests, "ssh_busy", "网关主机正在处理其他 SSH 操作，请稍后再试")
	case errors.Is(err, errSSHFrameTooLarge):
		return sshErr(http.StatusRequestEntityTooLarge, codeRequestTooLarge, "请求太大，无法经网关下发")
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return sshErr(http.StatusBadGateway, "ssh_unreachable", "%s", unreachableText(err))
	}
	return sshErr(http.StatusBadGateway, codeGatewayError, "网关主机没有返回有效的结果")
}

// gatewayResultErr 把网关返回的失败码翻译成与直连一致的管理错误码：同一类问题，界面上给出同一种提示。
func gatewayResultErr(res termproto.SSHResult) *SSHError {
	switch res.Code {
	case termproto.SSHCodeDisabled:
		return sshErr(http.StatusConflict, codeGatewayDisabled, "这台主机没有开启 SSH 网关（需要用 --ssh-gateway 运行 Agent / 会话守护进程）")
	case termproto.SSHCodeDenied:
		return sshErr(http.StatusForbidden, codeTargetDenied, "目标不在网关主机的允许名单内（--ssh-allow）")
	case termproto.SSHCodeBadRequest:
		return sshErr(http.StatusBadRequest, "bad_request", "%s", firstNonEmpty(res.Msg, "连接信息无效"))
	case termproto.SSHCodeUnreachable:
		return sshErr(http.StatusBadGateway, "ssh_unreachable", "从网关主机连接目标失败：%s", firstNonEmpty(res.Msg, "网络不通或服务未运行"))
	case termproto.SSHCodeAuth:
		return sshErr(http.StatusUnprocessableEntity, "ssh_auth_failed", "SSH 认证失败：请检查用户名与凭据")
	case termproto.SSHCodeHostKey:
		return sshErr(http.StatusConflict, "ssh_host_key_mismatch", "主机密钥与已固定的不一致：已停止连接，需要核对指纹后重新确认")
	case termproto.SSHCodeNoHostKey:
		return sshErr(http.StatusConflict, termexec.CodeSSHUntrusted, "这台主机还没有固定的主机密钥")
	case termproto.SSHCodePassphrase:
		return sshErr(http.StatusBadRequest, "ssh_passphrase_required", "私钥已加密，请同时提供口令")
	case termproto.SSHCodeUnsupported:
		return sshErr(http.StatusUnprocessableEntity, "ssh_unsupported", "%s", firstNonEmpty(res.Msg, "目标不是受支持的 POSIX 环境"))
	case termproto.SSHCodeBusy:
		return sshErr(http.StatusTooManyRequests, "ssh_busy", "网关主机正在处理其他 SSH 操作，请稍后再试")
	case termproto.SSHCodeNoCred:
		return sshErr(http.StatusConflict, termexec.CodeSSHCredMissing, "网关主机上没有这台 SSH 主机的登录凭据（凭据只保存在网关自己的存储里）：请在主机设置里重新填写凭据")
	case termproto.SSHCodeExecDenied:
		return sshErr(http.StatusForbidden, codeExecDenied, "网关主机在本机策略（--deny-model-exec）下拒绝了模型执行")
	case termproto.SSHCodeStore:
		return sshErr(http.StatusBadGateway, codeGatewayStore, "网关主机的凭据库不可用：%s", firstNonEmpty(res.Msg, "读写失败"))
	}
	return sshErr(http.StatusBadGateway, codeGatewayError, "网关主机没有返回有效的结果")
}

// gatewayExecErr 翻译经网关代理的 exec 失败：目标侧的问题还原成与直连相同的哨兵错误（执行层据此决定重试 / 停止 / 标记主机密钥不一致），
// 网关侧的问题保持为带稳定错误码的 *SSHError。
func gatewayExecErr(res termproto.SSHResult) error {
	switch res.Code {
	case termproto.SSHCodeHostKey:
		return sshexec.ErrHostKeyMismatch
	case termproto.SSHCodeNoHostKey:
		return sshexec.ErrNoHostKey
	case termproto.SSHCodeAuth:
		return sshexec.ErrAuthFailed
	case termproto.SSHCodeUnsupported:
		return sshexec.ErrUnsupported
	case termproto.SSHCodeUnreachable:
		return fmt.Errorf("%w：%s", sshexec.ErrUnreachable, firstNonEmpty(res.Msg, "网络不通或服务未运行"))
	}
	return gatewayResultErr(res)
}

// isGatewayInfraCode 判断错误码是否属于「网关本身的状态」而非目标主机的问题：
// 这类失败不应被记成目标主机的连接失败（网关离线是暂时的，界面用 gateway_ready 单独呈现它）。
func isGatewayInfraCode(code string) bool {
	return strings.HasPrefix(code, "ssh_gateway_")
}

// gatewayReady 报告一台主机此刻是否可以充当网关：已启用、在线，且会话守护进程声明了 SSH 网关能力。
func (h *Hub) gatewayReady(code string) bool {
	a := h.onlineAgent(code)
	return a != nil && a.isEnabled() && a.hasHolderCap(termproto.HolderCapSSH)
}

// normGateway 规整请求里的网关机器码：空串表示不经网关（Hub 直连）；非空必须是合法的机器码（统一成规范写法后再保存与比较）。
func normGateway(raw string) (string, *SSHError) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", nil
	}
	code, err := termproto.NormalizeMachineCode(raw)
	if err != nil {
		return "", sshErr(http.StatusBadRequest, "bad_request", "网关主机的机器码格式不正确")
	}
	return code, nil
}

// gatewayFor 校验并返回网关主机的在线连接。失败时的错误码让界面能区分「选错了主机」「不在线」「没开启网关」「凭据库不可用」。
// needStore 为 true 时（登记 / 修改凭据、复测、打开终端、执行——凡是要保存或取用凭据的操作）还要求网关的凭据库可用；
// 只做主机密钥探测不需要。
func (h *Hub) gatewayFor(ctx context.Context, code string, needStore bool) (*agentConn, *SSHError) {
	code, e := normGateway(code)
	if e != nil || code == "" {
		return nil, sshErr(http.StatusBadRequest, "bad_request", "网关主机的机器码格式不正确")
	}
	rec, err := h.store.GetTerminalHost(ctx, code)
	if err != nil {
		return nil, sshErr(http.StatusInternalServerError, "internal_error", "操作失败，请查看服务端日志")
	}
	if rec == nil || rec.IsSSH() || rec.Status != termproto.HostStatusEnabled {
		return nil, sshErr(http.StatusConflict, codeGatewayInvalid, "网关主机不存在、已停用，或不是装了 Agent 的主机")
	}
	a := h.onlineAgent(code)
	if a == nil || !a.isEnabled() {
		return nil, sshErr(http.StatusConflict, codeGatewayOffline, "网关主机当前不在线")
	}
	if !a.hasHolderCap(termproto.HolderCapSSH) {
		return nil, sshErr(http.StatusConflict, codeGatewayDisabled, "这台主机没有开启 SSH 网关（需要用 --ssh-gateway 运行 Agent / 会话守护进程，并确认守护进程可用）")
	}
	if needStore && !a.hasHolderCap(termproto.HolderCapSSHStore) {
		return nil, sshErr(http.StatusConflict, codeGatewayNoStore, "这台网关主机的凭据库不可用（Agent 版本过旧，或数据目录不可写）：登录凭据只保存在网关自己的存储里，请升级 Agent 并确认数据目录可写")
	}
	return a, nil
}

// connectorFor 按接入方式选择连接途径：gateway 为空即 Hub 直连。needStore 同 gatewayFor（直连忽略它）。
func (h *Hub) connectorFor(ctx context.Context, gateway string, needStore bool) (sshConnector, *SSHError) {
	if gateway == "" {
		return directConnector{b: h.ssh}, nil
	}
	a, e := h.gatewayFor(ctx, gateway, needStore)
	if e != nil {
		return nil, e
	}
	return gatewayConnector{a: a, code: gateway}, nil
}

// endGatewaySessions 终止网关主机上属于某台 SSH 主机的全部终端会话：停用 / 删除这台 SSH 主机，
// 或者改了它的地址 / 用户 / 接入路径之后调用。会话运行在网关上，不会随 SSH 主机的记录一起消失；
// 不处理的话，管理员以为已经切断的连接其实还开着。返回终止的会话数。
func (h *Hub) endGatewaySessions(ctx context.Context, gateway, target, reason, actor, ip string) int {
	a := h.onlineAgent(gateway)
	if a == nil {
		return 0 // 网关离线：它那边的会话由网关自己的生命周期管，Hub 这边无从下手
	}
	n := 0
	for _, s := range a.sessionList() {
		if s.Target != target || s.State == termproto.SessionExited {
			continue
		}
		if err := a.writeJSON(termproto.Kill{T: termproto.TKill, SID: s.SID}); err != nil {
			break
		}
		a.indexEnded(store.TerminalSessionEnd{SID: s.SID, Reason: "killed"})
		h.audit.record(ctx, auditEvent{
			Event: EvSessionKill, Actor: actor, IP: ip, MachineCode: gateway, SessionID: s.SID,
			Detail: "ssh_target=" + target + " " + reason,
		})
		n++
	}
	return n
}

// ErrGatewayInUse 表示一台主机仍被 SSH 主机当作网关：先删除或改接它们，才能删除这台主机。
type ErrGatewayInUse struct{ Count int64 }

func (e *ErrGatewayInUse) Error() string { return "gateway still in use" }

// ---------- 人工终端：在网关上打开 SSH 会话 ----------

// openSSHSession 在网关主机（code）上为一台经它接入的 SSH 主机打开人工终端会话。
// 会话由网关上的会话守护进程持有，列在网关主机名下（SessionInfo.Target 指回 SSH 主机）。
func (h *Hub) openSSHSession(c *gin.Context, code string, req openSessionRequest) {
	ctx := c.Request.Context()
	targetCode, nerr := termproto.NormalizeMachineCode(strings.TrimSpace(req.SSHTarget))
	if nerr != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "ssh_target 不是有效的机器码", "code": "bad_request"})
		return
	}
	b := h.ssh
	rec, err := h.store.GetTerminalHost(ctx, targetCode)
	if err != nil {
		h.fail(c, err)
		return
	}
	if rec == nil || !rec.IsSSH() || rec.Status != termproto.HostStatusEnabled {
		c.JSON(http.StatusNotFound, gin.H{"error": "SSH 主机不存在或已停用", "code": "host_not_found"})
		return
	}
	if rec.SSHGateway != code {
		c.JSON(http.StatusConflict, gin.H{"error": "这台 SSH 主机不是经这台主机接入的", "code": codeNotViaGateway})
		return
	}
	if req.IdleSeconds != nil && !validIdleSeconds(*req.IdleSeconds) {
		badIdleResponse(c)
		return
	}
	a, e := h.gatewayFor(ctx, code, true)
	if e != nil {
		h.sshFail(c, e)
		return
	}
	spec, err := b.buildSpec(*rec)
	if err != nil {
		h.sshFail(c, mapSSHErr(err))
		return
	}
	target, err := storedTarget(rec.MachineCode, spec)
	if err != nil {
		h.sshFail(c, mapSSHErr(err))
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		name = firstNonEmpty(strings.TrimSpace(rec.Remark), termproto.SSHLabel(rec.SSHUser, rec.SSHAddr))
	}
	info, err := a.openSession(ctx, openSpec{
		Name: name, Rows: req.Rows, Cols: req.Cols, Incognito: req.Incognito, IdleSec: req.IdleSeconds, SSH: &target,
	})
	if err != nil {
		h.failOpenSSH(c, code, rec, err)
		return
	}
	b.setCredLost(rec.MachineCode, false)
	_ = h.store.RecordSSHCheck(ctx, rec.MachineCode, store.SSHCheck{At: h.now(), OK: true})
	detail := "ssh:" + rec.MachineCode
	if req.Incognito {
		detail += " incognito"
	}
	h.audit.record(ctx, auditEvent{
		Event: EvSessionOpen, Actor: subjectOf(c), IP: c.ClientIP(), MachineCode: code, SessionID: info.SID, Detail: detail,
	})
	c.JSON(http.StatusOK, gin.H{"session": info})
}

// failOpenSSH 把打开 SSH 会话的失败写成响应；主机密钥不一致时顺带置 mismatch（只升不降，之后需要管理员重新确认指纹）。
func (h *Hub) failOpenSSH(c *gin.Context, gateway string, rec *model.TerminalHost, err error) {
	ctx := c.Request.Context()
	var oe *AgentOpenError
	if !errors.As(err, &oe) {
		switch {
		case errors.Is(err, errSessionLimit):
			c.JSON(http.StatusConflict, gin.H{"error": "该主机的会话数已达上限", "code": "session_limit"})
		case errors.Is(err, errTooManyOpens):
			c.JSON(http.StatusTooManyRequests, gin.H{"error": "操作过于频繁", "code": "busy"})
		case errors.Is(err, errOpenTimeout):
			c.JSON(http.StatusGatewayTimeout, gin.H{"error": "网关主机没有在规定时间内响应", "code": codeGatewayTimeout})
		case errors.Is(err, errAgentClosed), errors.Is(err, errAgentNotActive):
			c.JSON(http.StatusConflict, gin.H{"error": "网关主机当前不在线", "code": codeGatewayOffline})
		default:
			c.JSON(http.StatusBadGateway, gin.H{"error": "网关主机没有返回有效的结果", "code": codeGatewayError})
		}
		return
	}
	se := gatewayResultErr(termproto.SSHResult{Code: oe.Code, Msg: oe.Reason})
	if oe.Code == "" {
		// 旧版本守护进程不认识 SSH 目标，或失败发生在建连之前：没有稳定的码，只给出（已净化的）原因。
		se = sshErr(http.StatusBadGateway, "agent_refused", "%s", oe.Error())
	}
	switch {
	case oe.Code == termproto.SSHCodeHostKey:
		_ = h.store.RecordSSHCheck(ctx, rec.MachineCode, store.SSHCheck{At: h.now(), Err: se.Message, KeyMismatch: true})
		h.ssh.forget(rec.MachineCode)
	case oe.Code == termproto.SSHCodeNoCred:
		// 凭据不在网关上不是目标主机的故障：记下「缺凭据」让界面提示重新填写，而不是把主机标成连接失败。
		h.ssh.setCredLost(rec.MachineCode, true)
	case !isGatewayInfraCode(se.Code):
		_ = h.store.RecordSSHCheck(ctx, rec.MachineCode, store.SSHCheck{At: h.now(), Err: se.Message})
	}
	h.audit.record(ctx, auditEvent{
		Event: EvSessionOpen, Actor: subjectOf(c), IP: c.ClientIP(), MachineCode: gateway, Result: ResultFail,
		Detail: "ssh:" + rec.MachineCode + " code=" + firstNonEmpty(oe.Code, "none"),
	})
	c.JSON(se.Status, gin.H{"error": se.Message, "code": se.Code})
}
