package terminal

import (
	"context"
	"errors"
	"log"
	"net/http"
	"slices"
	"strconv"
	"time"

	"github.com/teacat99/RelayMesh/v2/internal/sshexec"
	"github.com/teacat99/RelayMesh/v2/internal/termexec"
	"github.com/teacat99/RelayMesh/v2/internal/termproto"
)

// 经网关接入的 SSH 主机的执行代理与凭据治理（网关 v2 的 Hub 侧）。
//
//   - 执行：Hub 驱动远端作业的每一步（启动 / 轮询 / 取消 / 读取）都是一次经网关代理的 SSH exec（callVia）。
//     网关出示它自己保存的凭据；Hub 只送去地址、用户与已固定的主机公钥（stored 引用，帧里没有凭据）。
//   - 对账：网关（重新）就绪时 Hub 发 cred_sync，列出仍然需要的主机；网关回收名单之外的凭据，并回报自己实际持有哪些，
//     没有的就标成「凭据缺失」（界面提示管理员重新填写，模型得到明确的 ssh_cred_missing）。
//   - 清理：主机被删除 / 改接别的网关 / 登记失败时，best-effort 通知网关删除凭据；通知不到的由下一次对账回收。

const (
	credSyncTimeout = 30 * time.Second
	credDelTimeout  = 15 * time.Second
	// credDropRunWait 是删除主机时等待它名下进行中的运行收尾（远端作业被取消）的上限，之后才删网关上的凭据。
	credDropRunWait = 60 * time.Second
)

// gatewayExec 报告网关的 SSH exec 代理能力：ready = 守护进程可用且声明了 ssh 与 ssh_store；
// denied = 网关本机用 --deny-model-exec 一票否决了模型执行。
func (a *agentConn) gatewayExec() (ready, denied bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	h := a.holder
	if h == nil || h.State != termproto.HolderReady {
		return false, false
	}
	ready = slices.Contains(h.Caps, termproto.HolderCapSSH) && slices.Contains(h.Caps, termproto.HolderCapSSHStore)
	return ready, h.DenyExec
}

// gatewayExecState 是 gatewayExec 的按机器码版本；网关离线时两者都为 false。
func (h *Hub) gatewayExecState(code string) (ready, denied bool) {
	a := h.onlineAgent(code)
	if a == nil || !a.isEnabled() {
		return false, false
	}
	return a.gatewayExec()
}

// callVia 经网关在目标上执行一段脚本。凭据由网关出示；一票否决与能力缺失在发出之前就拒绝，省一次往返。
func (b *sshBackend) callVia(ctx context.Context, code string, spec sshexec.Spec, cmd string, stdin []byte) (sshexec.Result, error) {
	a := b.h.onlineAgent(spec.Via)
	if a == nil || !a.isEnabled() {
		return sshexec.Result{}, gatewayTransportErr(errAgentNotActive)
	}
	ready, denied := a.gatewayExec()
	switch {
	case !ready:
		return sshexec.Result{}, sshErr(http.StatusConflict, codeGatewayDisabled, "网关主机的会话守护进程不可用，或没有开启 SSH 网关 / 凭据库能力")
	case denied:
		return sshexec.Result{}, gatewayResultErr(termproto.SSHResult{Code: termproto.SSHCodeExecDenied})
	}
	target, err := storedTarget(code, spec)
	if err != nil {
		return sshexec.Result{}, err
	}
	res, err := a.sshExec(ctx, target, cmd, stdin, termproto.MaxSSHExecOut)
	if err != nil {
		return sshexec.Result{}, gatewayTransportErr(err)
	}
	if !res.OK {
		return sshexec.Result{}, gatewayExecErr(res)
	}
	return sshexec.Result{Stdout: res.Stdout, Stderr: res.Stderr, Exit: res.Exit}, nil
}

// noteViaResult 记录经网关执行的结果。只有「目标侧」的结果才计入主机的健康状态：网关离线 / 忙 / 一票否决 / 请求太大都不是目标的故障，
// 凭据不在网关上则单独标记。
func (b *sshBackend) noteViaResult(code string, err error) {
	var se *SSHError
	if errors.As(err, &se) {
		switch {
		case se.Code == termexec.CodeSSHCredMissing:
			b.setCredLost(code, true)
			return
		case isGatewayInfraCode(se.Code), se.Code == "ssh_busy", se.Code == codeExecDenied,
			se.Code == codeTargetDenied, se.Code == codeRequestTooLarge:
			return
		}
	}
	if err == nil {
		b.setCredLost(code, false)
	}
	b.noteResult(code, err)
}

// ---------- 对账与清理 ----------

// maybeSyncCreds 在网关的会话守护进程就绪且具备凭据库能力之后，每条连接对账一次凭据。
func (a *agentConn) maybeSyncCreds() {
	if !a.hasHolderCap(termproto.HolderCapSSHStore) {
		return
	}
	a.mu.Lock()
	if a.credSynced {
		a.mu.Unlock()
		return
	}
	a.credSynced = true
	a.mu.Unlock()
	h := a.hub
	release, ok := h.track()
	if !ok {
		return
	}
	go func() {
		defer release()
		h.syncGatewayCreds(a)
	}()
}

// syncGatewayCreds 把「仍然需要凭据的主机」名单发给网关，并据回报标出缺失的主机。
//
// 安全性：名单必须完整——漏掉的主机会被网关回收凭据。所以读库失败时一律放弃本次对账（绝不发空名单），名单包含该网关下
// 所有状态的主机（停用的也算：重新启用后要能继续使用）；与登记 / 修改凭据互斥（credMu），避免回收掉刚保存、尚未落库的凭据。
func (h *Hub) syncGatewayCreds(a *agentConn) {
	b := h.ssh
	b.credMu.Lock()
	defer b.credMu.Unlock()
	if !a.isEnabled() || !a.hasHolderCap(termproto.HolderCapSSHStore) {
		return
	}
	codes, err := h.store.ListSSHHostCodesViaGateway(h.bgCtx, a.code)
	if err != nil {
		log.Printf("[terminal] 网关 %s 的凭据对账已放弃（读取主机名单失败）: %v", a.code, err)
		return
	}
	if len(codes) > termproto.MaxSSHKeep {
		log.Printf("[terminal] 网关 %s 的凭据对账已放弃（主机数 %d 超过协议上限 %d）", a.code, len(codes), termproto.MaxSSHKeep)
		return
	}
	ctx, cancel := context.WithTimeout(h.bgCtx, credSyncTimeout)
	defer cancel()
	res, err := a.sshOp(ctx, termproto.SSHOp{Op: termproto.SSHOpCredSync, Keep: codes})
	if err != nil {
		log.Printf("[terminal] 网关 %s 的凭据对账失败: %v", a.code, err)
		a.mu.Lock()
		a.credSynced = false // 没对上账：下一次守护进程状态上报时再试
		a.mu.Unlock()
		return
	}
	if !res.OK {
		log.Printf("[terminal] 网关 %s 的凭据对账被拒绝: code=%s", a.code, res.Code)
		return
	}
	have := make(map[string]struct{}, len(res.Have))
	for _, c := range res.Have {
		have[c] = struct{}{}
	}
	missing := 0
	for _, c := range codes {
		_, ok := have[c]
		b.setCredLost(c, !ok)
		if !ok {
			missing++
		}
	}
	if res.Dropped > 0 || missing > 0 {
		h.audit.record(h.bgCtx, auditEvent{
			Event: EvSSHCredSync, MachineCode: a.code, Result: ResultOK,
			Detail: "hosts=" + strconv.Itoa(len(codes)) + " missing=" + strconv.Itoa(missing) + " dropped=" + strconv.Itoa(res.Dropped),
		})
	}
}

// dropGatewayCredAsync 通知网关删除某台 SSH 主机的凭据（主机被删除 / 改接别处 / 登记没成功）。尽力而为：
// 网关离线或失败时凭据会留在那里，直到下一次对账（名单里没有它）被回收。
func (h *Hub) dropGatewayCredAsync(gateway, code string) {
	if gateway == "" || !termproto.ValidMachineCode(code) {
		return
	}
	release, ok := h.track()
	if !ok {
		return
	}
	go func() {
		defer release()
		h.dropGatewayCred(gateway, code)
	}()
}

// dropGatewayCredWhenIdle 与 dropGatewayCredAsync 相同，但先等这台主机名下进行中的运行收尾再删凭据：
// 取消远端作业要用网关上保存的那份凭据，抢在取消之前删掉它，远端进程就成了没人管的孤儿。
// 等待有上限（运行因故迟迟不收尾时不让凭据永远留着）；Hub 关闭时放弃，留待下一次对账回收。
func (h *Hub) dropGatewayCredWhenIdle(gateway, code string) {
	if gateway == "" || !termproto.ValidMachineCode(code) {
		return
	}
	release, ok := h.track()
	if !ok {
		return
	}
	go func() {
		defer release()
		deadline := time.NewTimer(credDropRunWait)
		defer deadline.Stop()
		tick := time.NewTicker(100 * time.Millisecond)
		defer tick.Stop()
		for h.exec.liveRunsOn(code) > 0 {
			select {
			case <-h.bgCtx.Done():
				return
			case <-deadline.C:
				log.Printf("[terminal] 主机 %s 名下仍有运行未收尾，不再等待，直接通知网关 %s 删除凭据", code, gateway)
				h.dropGatewayCred(gateway, code)
				return
			case <-tick.C:
			}
		}
		h.dropGatewayCred(gateway, code)
	}()
}

func (h *Hub) dropGatewayCred(gateway, code string) {
	a := h.onlineAgent(gateway)
	if a == nil || !a.isEnabled() || !a.hasHolderCap(termproto.HolderCapSSHStore) {
		return
	}
	ctx, cancel := context.WithTimeout(h.bgCtx, credDelTimeout)
	defer cancel()
	res, err := a.sshOp(ctx, termproto.SSHOp{Op: termproto.SSHOpCredDel, Target: termproto.SSHTarget{ID: code}})
	if err != nil || !res.OK {
		log.Printf("[terminal] 通知网关 %s 删除主机 %s 的凭据未成功（留待下次对账回收）", gateway, code)
	}
}
