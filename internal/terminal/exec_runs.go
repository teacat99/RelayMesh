package terminal

import (
	"context"
	"errors"
	"log"
	"time"

	"github.com/teacat99/RelayMesh/v2/internal/model"
	"github.com/teacat99/RelayMesh/v2/internal/store"
	"github.com/teacat99/RelayMesh/v2/internal/termexec"
	"github.com/teacat99/RelayMesh/v2/internal/termproto"
)

// 运行的状态机：从 Agent 来的事件、取消 / 批准 / 暂停、重连对账与周期维护。
// 所有终态转换都走 finalize，保证一次运行只有一个终态（内存里用 final 标志，数据库里用条件更新）。

var activeRunStates = []string{model.RunAwaitingApproval, model.RunRunning}

// 系统发起的取消来源。它们同时是下发给主机的取消原因：守护进程原样回报，最终成为运行记录里的 deny_reason，
// 界面（ExecMesh、预设清单、聊天里的结束行）据此写明「为什么被取消」。
// 早期版本把这些全部写成 system，事后无从分辨；system 现在只留给读旧记录时认识，新代码不再产生它。
const (
	cancelGateClosed   = "gate_closed"   // 终端总闸关闭，进行中的运行被一并取消
	cancelHostRemoved  = "host_removed"  // 主机被停用或删除
	cancelTokenChanged = "token_changed" // 运行所用的令牌被修改、停用、删除或重新生成
	cancelUnknownRun   = "unknown_run"   // 主机上有这次运行，Hub 却不认识它（记录已不存在）
	cancelRevoked      = "revoked"       // 主机的模型授权被收回或改成只读
	cancelPause        = "pause"         // 暂停开关（全局或这台主机）
)

// isSystemCancel 判断取消来源是不是系统自己发起的、没有人为操作可查的那几种。
func isSystemCancel(by string) bool {
	switch by {
	case cancelGateClosed, cancelHostRemoved, cancelTokenChanged, cancelUnknownRun, "system":
		return true
	}
	return false
}

// ---------- 启动与维护 ----------

// start 在 Hub 启动时调用：读取持久化的暂停开关，恢复上次进程留下的未终结运行。
func (s *ExecService) start() {
	ctx := s.h.ctx
	if on, err := s.h.store.GetSystemFlag(ctx, store.SettingTerminalExecPaused); err != nil {
		log.Printf("[terminal] 读取模型执行暂停开关失败: %v", err)
	} else if on {
		s.mu.Lock()
		s.paused = true
		s.mu.Unlock()
		log.Printf("[terminal] 模型执行处于全局暂停状态（上次由管理员暂停，重启后保持）")
	}

	active, err := s.h.store.ListActiveTerminalRuns(ctx)
	if err != nil {
		log.Printf("[terminal] 恢复未终结的模型运行失败: %v", err)
		return
	}
	now := s.h.now()
	var sshRuns []*liveRun
	for _, rec := range active {
		if rec.State == model.RunAwaitingApproval {
			// 待批准的命令只存在于内存（数据库里是脱敏副本，不能用来执行），进程重启后无法继续：直接作废。
			ended := now
			_, _ = s.h.store.UpdateTerminalRunIf(ctx, rec.RunID, activeRunStates, map[string]any{
				"state": model.RunRejected, "deny_reason": "hub_restart", "ended_at": &ended,
			})
			continue
		}
		lr := &liveRun{id: rec.RunID, done: make(chan struct{}), rec: rec, started: rec.StartedAt != nil}
		s.mu.Lock()
		s.live[rec.RunID] = lr
		s.mu.Unlock()
		s.watch.restore(rec) // 系统发起的非交互运行：接回输出监视，让对话的状态不会停在「工作中」
		if rec.Backend == model.HostKindSSH {
			sshRuns = append(sshRuns, lr)
		}
	}
	if len(active) > 0 {
		log.Printf("[terminal] 已恢复 %d 个未终结的模型运行记录，等待主机重连后对账", len(active))
	}
	// SSH 主机上的运行没有「重连对账」：直接凭运行 ID 接着轮询远端任务。
	s.sshRestoreRuns(sshRuns)
}

func (s *ExecService) snapshotLive() []*liveRun {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]*liveRun, 0, len(s.live))
	for _, lr := range s.live {
		out = append(out, lr)
	}
	return out
}

// liveRunsOn 返回某台主机名下仍在进行（尚未到达终态）的运行数。
func (s *ExecService) liveRunsOn(machine string) int {
	if s == nil {
		return 0
	}
	n := 0
	for _, lr := range s.snapshotLive() {
		if lr.snapshot().Target == machine {
			n++
		}
	}
	return n
}

// maintain 每秒调用一次：批准超时、启动无应答、超期未结束的运行，对账宽限到期，以及每天一次的历史清理。
func (s *ExecService) maintain(now time.Time) {
	for _, lr := range s.snapshotLive() {
		lr.mu.Lock()
		final, state := lr.final, lr.rec.State
		expires, sentAt, started := lr.expires, lr.sentAt, lr.started
		agent := lr.peer
		base := lr.rec.CreatedAt
		if lr.rec.StartedAt != nil {
			base = *lr.rec.StartedAt
		}
		deadline := base.Add(time.Duration(lr.rec.TimeoutSec)*time.Second + 10*time.Minute)
		lr.mu.Unlock()
		if final {
			continue
		}
		switch {
		case state == model.RunAwaitingApproval && !expires.IsZero() && now.After(expires):
			if s.finalize(lr, model.RunRejected, "approval_timeout", nil) {
				s.auditRun(EvExecReject, ResultOK, lr.snapshot(), "system", "", ActorSystem, "approval_timeout")
			}
		case state == model.RunRunning && !started && !sentAt.IsZero() && now.Sub(sentAt) > execStartTimeout &&
			peerAlive(agent):
			// 连接一直在，主机却没有任何应答：放弃，并尽力让主机取消（万一它稍后才开始）。
			_ = agent.sendRunCancel(lr.id, "system")
			s.finalize(lr, model.RunFailed, "start_timeout", nil)
		case state == model.RunRunning && now.After(deadline):
			if s.finalize(lr, model.RunLost, "overdue", nil) {
				s.auditRun(EvRunLost, ResultFail, lr.snapshot(), "system", "", ActorSystem, "overdue")
			}
		}
	}

	s.mu.Lock()
	var expired []string
	for id, t := range s.lostWatch {
		if now.After(t) {
			expired = append(expired, id)
			delete(s.lostWatch, id)
		}
	}
	s.mu.Unlock()
	for _, id := range expired {
		if lr := s.liveRun(id); lr != nil {
			if s.finalize(lr, model.RunLost, "not_on_host", nil) {
				s.auditRun(EvRunLost, ResultFail, lr.snapshot(), "system", "", ActorSystem, "not_on_host")
			}
		}
	}

	if now.After(s.pruneAt) {
		s.pruneAt = now.Add(24 * time.Hour)
		if n, err := s.h.store.PruneTerminalRuns(s.h.ctx, now.Add(-execRetention)); err != nil {
			log.Printf("[terminal] 模型运行历史清理失败: %v", err)
		} else if n > 0 {
			log.Printf("[terminal] 已清理 %d 条超过 %d 天的模型运行记录", n, int(execRetention.Hours()/24))
		}
	}
}

// ---------- 终结 ----------

func finalFields(rec model.TerminalRun) map[string]any {
	return map[string]any{
		"state":          rec.State,
		"deny_reason":    rec.DenyReason,
		"exit_code":      rec.ExitCode,
		"signal":         rec.Signal,
		"ended_at":       rec.EndedAt,
		"started_at":     rec.StartedAt,
		"duration_ms":    rec.DurationMS,
		"stdout_bytes":   rec.StdoutBytes,
		"stderr_bytes":   rec.StderrBytes,
		"truncated":      rec.Truncated,
		"middle_dropped": rec.MiddleDropped,
		"preview_head":   rec.PreviewHead,
		"preview_tail":   rec.PreviewTail,
		"approved_by":    rec.ApprovedBy,
		"approved_at":    rec.ApprovedAt,
		"shell":          rec.Shell,
		"cwd":            rec.Cwd,
	}
}

// finalize 把运行置为终态：改内存 → 条件落库 → 移出活动索引 → 唤醒等待者 → 发布事件。返回是否是本次调用完成了终结。
func (s *ExecService) finalize(lr *liveRun, state, reason string, apply func(*model.TerminalRun)) bool {
	now := s.h.now()
	lr.mu.Lock()
	if lr.final {
		lr.mu.Unlock()
		return false
	}
	lr.final = true
	lr.rec.State = state
	if reason != "" {
		lr.rec.DenyReason = clipBytes(reason, 32)
	}
	lr.rec.EndedAt = &now
	if apply != nil {
		apply(&lr.rec)
	}
	if lr.rec.DurationMS == 0 && lr.rec.StartedAt != nil {
		lr.rec.DurationMS = now.Sub(*lr.rec.StartedAt).Milliseconds()
	}
	lr.frame = termproto.Run{} // 命令明文不再需要留在内存里
	rec := lr.rec
	cancelBy := lr.cancelBy // 通知挂钩要知道是谁要求取消的（空 = 没人要求）
	lr.mu.Unlock()

	if _, err := s.h.store.UpdateTerminalRunIf(s.h.ctx, rec.RunID, activeRunStates, finalFields(rec)); err != nil {
		log.Printf("[terminal] 模型运行 %s 的终态写库失败: %v", rec.RunID, err)
	}
	s.mu.Lock()
	delete(s.live, lr.id)
	delete(s.lostWatch, lr.id)
	s.mu.Unlock()
	close(lr.done)
	s.bus.publishRun(rec)
	s.runEnded(rec, cancelBy) // 最后一步：此时状态已落库、事件已发布、不持有任何锁
	return true
}

// auditRun 写一条与某次运行相关的审计；actorKind 为 model 时 actor 是令牌名。
func (s *ExecService) auditRun(event, result string, rec model.TerminalRun, actor, ip, kind, detail string) {
	s.h.audit.record(s.h.ctx, auditEvent{
		Event: event, Result: result, Actor: actor, IP: ip, MachineCode: rec.Target,
		ActorKind: kind, RunID: rec.RunID, Detail: detail,
	})
}

// ---------- 下发与 Agent 事件 ----------

// dispatch 把运行请求发给主机；发不出去就直接终结为失败。
func (s *ExecService) dispatch(lr *liveRun, a execPeer) {
	lr.mu.Lock()
	if lr.final {
		lr.mu.Unlock()
		return
	}
	frame := lr.frame
	lr.peer = a
	lr.sentAt = s.h.now()
	lr.mu.Unlock()

	if err := a.sendRun(frame); err != nil {
		reason := "send_failed"
		switch {
		case errors.Is(err, errRunFrameTooBig):
			reason = "too_large"
		case errors.Is(err, errSSHBusy):
			reason = termproto.RunFailBusy
		}
		s.finalize(lr, model.RunFailed, reason, nil)
		return
	}
	lr.mu.Lock()
	lr.frame = termproto.Run{}
	pending := lr.cancelBy
	lr.mu.Unlock()
	if pending != "" { // 发送过程中有人取消了
		_ = a.sendRunCancel(lr.id, pending)
	}
}

// adopt 为数据库里仍未终结、但内存索引里没有的运行建立内存状态（例如终态写库失败过，主机又补报了结束事件）。
func (s *ExecService) adopt(a execPeer, id string) *liveRun {
	rec, err := s.h.store.GetTerminalRun(s.h.ctx, id)
	if err != nil || rec == nil || rec.Target != a.machine() || model.IsFinalRunState(rec.State) || rec.State != model.RunRunning {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if lr, ok := s.live[id]; ok {
		return lr
	}
	lr := &liveRun{id: id, done: make(chan struct{}), rec: *rec, started: rec.StartedAt != nil}
	s.live[id] = lr
	return lr
}

// ownedRun 返回这台主机名下的进行中运行：别的主机上报的运行 ID 一律不认。
func (s *ExecService) ownedRun(a execPeer, id string) *liveRun {
	lr := s.liveRun(id)
	if lr == nil {
		lr = s.adopt(a, id)
	}
	if lr == nil {
		return nil
	}
	lr.mu.Lock()
	ok := lr.rec.Target == a.machine()
	lr.mu.Unlock()
	if !ok {
		return nil
	}
	return lr
}

func (s *ExecService) onRunStarted(a execPeer, m termproto.RunStarted) {
	lr := s.ownedRun(a, m.ID)
	if lr == nil {
		return
	}
	now := s.h.now()
	lr.mu.Lock()
	if lr.final || lr.started {
		lr.mu.Unlock()
		return
	}
	lr.started = true
	lr.rec.StartedAt = &now
	if m.Shell != "" {
		lr.rec.Shell = m.Shell
	}
	if m.Cwd != "" {
		lr.rec.Cwd = m.Cwd
	}
	rec := lr.rec
	lr.mu.Unlock()

	s.mu.Lock()
	delete(s.lostWatch, m.ID)
	s.mu.Unlock()
	_, _ = s.h.store.UpdateTerminalRunIf(s.h.ctx, m.ID, activeRunStates, map[string]any{
		"started_at": rec.StartedAt, "shell": rec.Shell, "cwd": rec.Cwd,
	})
	s.bus.publishRun(rec)
}

func (s *ExecService) onRunFailed(a execPeer, m termproto.RunFailed) {
	lr := s.ownedRun(a, m.ID)
	if lr == nil {
		return
	}
	state, reason := model.RunFailed, m.Reason
	if m.Reason == termproto.RunFailDenied {
		state, reason = model.RunDenied, termexec.CodeHostDenied
	}
	if s.finalize(lr, state, reason, nil) && state == model.RunDenied {
		s.auditRun(EvExecDenied, ResultDeny, lr.snapshot(), "system", "", ActorSystem, "reason=host_denied (agent veto)")
	}
}

// endState 把主机上报的结束状态翻译成 Hub 的运行状态。
func endState(m termproto.RunEnd) string {
	switch m.State {
	case termproto.RunExited:
		if m.Code != nil && *m.Code == 0 {
			return model.RunSucceeded
		}
		return model.RunFailed
	case termproto.RunSignaled:
		return model.RunFailed
	case termproto.RunTimeout:
		return model.RunTimedOut
	case termproto.RunCancelled:
		return model.RunCancelled
	}
	return model.RunLost
}

func (s *ExecService) onRunEnd(a execPeer, m termproto.RunEnd) {
	// 无论认不认识都确认：这是「结束事件已处理」的回执，主机收到后才停止补报。
	defer func() { _ = a.sendRunAck(m.ID) }()

	lr := s.ownedRun(a, m.ID)
	if lr == nil {
		return
	}
	end := m
	state := endState(m)
	s.finalize(lr, state, m.Reason, func(r *model.TerminalRun) {
		lr.end = &end
		r.ExitCode = m.Code
		r.Signal = m.Signal
		r.DurationMS = m.DurMS
		r.StdoutBytes, r.StderrBytes = m.OutBytes, m.ErrBytes
		r.MiddleDropped = m.OutDropped + m.ErrDropped
		r.Truncated = r.MiddleDropped > 0
		r.PreviewHead = previewText(m.PrevHead, true)
		r.PreviewTail = previewText(m.PrevTail, false)
		if state == model.RunLost {
			r.DenyReason = clipBytes(m.Reason, 32)
		}
	})
	if state == model.RunLost {
		s.auditRun(EvRunLost, ResultFail, lr.snapshot(), "system", "", ActorSystem, "reason="+m.Reason)
	}
}

// onRuns 处理主机重连后上报的「仍在运行」清单，与 Hub 记录对账。
func (s *ExecService) onRuns(a execPeer, m termproto.Runs) {
	now := s.h.now()
	onHost := make(map[string]struct{}, len(m.Running))
	for _, b := range m.Running {
		onHost[b.ID] = struct{}{}
		lr := s.ownedRun(a, b.ID)
		if lr == nil {
			// 主机上有个运行，Hub 不认识（或早已终结）：没有任何人为它负责，取消它。
			_ = a.sendRunCancel(b.ID, cancelUnknownRun)
			continue
		}
		lr.mu.Lock()
		if lr.final {
			lr.mu.Unlock()
			continue
		}
		lr.peer = a
		if !lr.started {
			lr.started = true
			t := now
			lr.rec.StartedAt = &t
		}
		cancelBy := lr.cancelBy
		target := lr.rec.Target
		lr.mu.Unlock()

		s.mu.Lock()
		delete(s.lostWatch, b.ID)
		s.mu.Unlock()

		// 断线期间管理员可能已经收回授权 / 暂停，或有人请求过取消：现在补上。
		if cancelBy == "" {
			if host, err := s.h.store.GetTerminalHost(s.h.ctx, target); err == nil {
				switch {
				case host == nil:
					cancelBy = cancelHostRemoved
				case !host.AllowModel:
					cancelBy = cancelRevoked
				case host.ModelPaused || s.isPaused():
					cancelBy = cancelPause
				}
				if cancelBy != "" {
					lr.mu.Lock()
					lr.cancelBy = cancelBy
					lr.mu.Unlock()
				}
			}
		}
		if cancelBy != "" {
			_ = a.sendRunCancel(b.ID, cancelBy)
		}
	}

	// Hub 认为在这台主机上运行、清单里却没有的：结束事件通常紧跟在清单之后到达；宽限期内没等到才判 lost。
	for _, lr := range s.snapshotLive() {
		rec := lr.snapshot()
		if rec.Target != a.machine() || rec.State != model.RunRunning {
			continue
		}
		if _, ok := onHost[rec.RunID]; ok {
			continue
		}
		s.mu.Lock()
		if _, watching := s.lostWatch[rec.RunID]; !watching {
			s.lostWatch[rec.RunID] = now.Add(execLostGrace)
		}
		s.mu.Unlock()
	}
}

// onOut 把实时输出片段转给人类事件流。片段只存在于这次转发里，不缓存、不落盘。
func (s *ExecService) onOut(a execPeer, id string, stream byte, off uint64, payload []byte) {
	lr := s.liveRun(id)
	if lr == nil {
		return
	}
	lr.mu.Lock()
	ok := lr.rec.Target == a.machine() && !lr.final
	lr.mu.Unlock()
	if !ok {
		return
	}
	s.watch.nudge(id) // 只碰一下监视协程（非阻塞）：这里跑在 Agent 连接的读协程里，不能读输出
	s.bus.publishOut(id, termproto.StreamName(stream), off, payload)
}

// agentGone 在 Agent 连接结束时调用：进行中的运行不会因此改变状态（它们由主机上的守护进程持有），重连后再对账。
func (s *ExecService) agentGone(a *agentConn) {
	if a == nil || a.code == "" {
		return
	}
	s.bus.publishHost(a.code, false)
}

// ---------- 取消 / 批准 / 暂停 ----------

// cancelRun 请求取消一次运行。待批准的直接作废；运行中的通知主机（主机离线则等重连后补发）。
func (s *ExecService) cancelRun(lr *liveRun, by string) {
	lr.mu.Lock()
	if lr.final {
		lr.mu.Unlock()
		return
	}
	state := lr.rec.State
	target := lr.rec.Target
	lr.cancelBy = by
	lr.mu.Unlock()

	// 系统发起的取消没有「操作者」可查：把原因与这次运行一起写进审计，事后才能回答「它为什么被取消」。
	if isSystemCancel(by) {
		s.auditRun(EvExecCancel, ResultOK, lr.snapshot(), "system", "", ActorSystem, "reason="+by)
	}

	if state == model.RunAwaitingApproval {
		s.finalize(lr, model.RunCancelled, "withdrawn", nil)
		return
	}
	if a := s.peerFor(target); a != nil && a.usable() {
		_ = a.sendRunCancel(lr.id, by)
	}
}

// cancelScope 取消一批运行：machine 为空表示所有主机；onlyModel 为 true 时只取消模型发起的运行。
func (s *ExecService) cancelScope(machine, by string) int {
	n := 0
	for _, lr := range s.snapshotLive() {
		rec := lr.snapshot()
		if machine != "" && rec.Target != machine {
			continue
		}
		s.cancelRun(lr, by)
		n++
	}
	return n
}

// hostGone 在主机被停用 / 删除 / 收回模型授权时调用：取消它名下的全部运行。
func (s *ExecService) hostGone(machine, by string) {
	if s == nil {
		return
	}
	s.cancelScope(machine, by)
}

// MCPCredentialChanged 在 MCP 令牌被修改、停用、删除或重新生成之后调用：它名下仍在进行的运行，
// 按新的状态已经不会被允许的，就取消（令牌可能已经泄露，进行中的命令不应该比令牌活得更久）。
// reason 为 deleted / regenerated 时无条件取消；为 updated 时按新配置重新评估。
func (s *ExecService) MCPCredentialChanged(ctx context.Context, id uint, reason string) {
	if s == nil || id == 0 {
		return
	}
	var cred *model.MCPCredential
	if reason == "updated" {
		if c, err := s.h.store.GetCredential(ctx, id); err == nil {
			cred = c
		}
	}
	now := s.h.now()
	for _, lr := range s.snapshotLive() {
		rec := lr.snapshot()
		if rec.CredentialID != id {
			continue
		}
		if cred != nil && credentialStillAllows(cred, rec.Target, now) {
			continue
		}
		s.cancelRun(lr, cancelTokenChanged)
	}
}

// credentialStillAllows 判断令牌在新状态下是否仍然允许对这台主机执行命令。
func credentialStillAllows(c *model.MCPCredential, machine string, now time.Time) bool {
	switch {
	case !c.IsActive:
		return false
	case c.ExpiresAt != nil && now.After(*c.ExpiresAt):
		return false
	case !c.Permissions.Terminal:
		return false
	case !c.AllowsTerminalHost(machine):
		return false
	case c.TerminalModeCap == model.TerminalModeReadonly:
		return false
	}
	return true
}

// gateClosed 在终端总闸关闭、连接被拆除之前调用：尽力取消所有进行中的运行。
func (s *ExecService) gateClosed() {
	if s == nil {
		return
	}
	s.cancelScope("", cancelGateClosed)
	s.bus.closeAll()
}

var (
	// ErrRunNotFound 表示运行不存在（或已被清理）。
	ErrRunNotFound = errors.New("run not found")
	// ErrRunNotAwaiting 表示运行不在待批准状态。
	ErrRunNotAwaiting = errors.New("run is not awaiting approval")
)

// Approve 由人类批准一次待批准的运行。批准时会重新检查全部闸门：授权可能在等待期间被收回或暂停。
func (s *ExecService) Approve(ctx context.Context, runID, actor, ip string) (*model.TerminalRun, error) {
	lr := s.liveRun(runID)
	if lr == nil {
		return nil, s.notLive(ctx, runID)
	}
	lr.mu.Lock()
	if lr.final || lr.rec.State != model.RunAwaitingApproval {
		lr.mu.Unlock()
		return nil, ErrRunNotAwaiting
	}
	target := lr.rec.Target
	lr.mu.Unlock()

	if e := s.g1(ctx); e != nil {
		s.finalize(lr, model.RunDenied, e.Code, nil)
		return nil, e
	}
	host, err := s.h.store.GetTerminalHost(ctx, target)
	if err != nil {
		return nil, err
	}
	a := s.peerFor(target)
	if host == nil || host.Status != termproto.HostStatusEnabled {
		s.finalize(lr, model.RunDenied, termexec.CodeHostNotFound, nil)
		return nil, execErr(termexec.CodeHostNotFound, "主机已不存在或已被停用")
	}
	if code := s.hostBlock(*host, a); code != "" {
		if code == termexec.CodeHostOffline {
			return nil, execErr(code, "%s", blockMessage(code)) // 主机离线时保留待批准状态，上线后还可以批准
		}
		s.finalize(lr, model.RunDenied, code, nil)
		return nil, execErr(code, "%s", blockMessage(code))
	}

	now := s.h.now()
	lr.mu.Lock()
	if lr.final || lr.rec.State != model.RunAwaitingApproval {
		lr.mu.Unlock()
		return nil, ErrRunNotAwaiting
	}
	lr.rec.State = model.RunRunning
	lr.rec.ApprovedBy = termproto.SanitizeText(actor, 64)
	lr.rec.ApprovedAt = &now
	lr.expires = time.Time{}
	rec := lr.rec
	lr.mu.Unlock()

	_, _ = s.h.store.UpdateTerminalRunIf(s.h.ctx, runID, []string{model.RunAwaitingApproval}, map[string]any{
		"state": model.RunRunning, "approved_by": rec.ApprovedBy, "approved_at": rec.ApprovedAt,
	})
	s.auditRun(EvExecApprove, ResultOK, rec, actor, ip, ActorHuman, "")
	s.bus.publishRun(rec)
	s.dispatch(lr, a)
	out := lr.snapshot()
	return &out, nil
}

// Reject 由人类拒绝一次待批准的运行。
func (s *ExecService) Reject(ctx context.Context, runID, actor, ip string) (*model.TerminalRun, error) {
	lr := s.liveRun(runID)
	if lr == nil {
		// 竞态：人点「拒绝」的瞬间，这条已经被拒绝（超时自动拒绝等）：结果就是想要的，原样返回。
		if rec := s.archived(ctx, runID); rec != nil && rec.State == model.RunRejected {
			return rec, nil
		}
		return nil, s.notLive(ctx, runID)
	}
	lr.mu.Lock()
	awaiting := !lr.final && lr.rec.State == model.RunAwaitingApproval
	lr.mu.Unlock()
	if !awaiting {
		return nil, ErrRunNotAwaiting
	}
	if s.finalize(lr, model.RunRejected, "rejected_by_user", nil) {
		s.auditRun(EvExecReject, ResultOK, lr.snapshot(), actor, ip, ActorHuman, "")
	}
	out := lr.snapshot()
	return &out, nil
}

// archived 取出已经不在内存里的运行记录（它已结束，只留在数据库里）；没有则返回 nil。
func (s *ExecService) archived(ctx context.Context, runID string) *model.TerminalRun {
	rec, err := s.h.store.GetTerminalRun(ctx, runID)
	if err != nil {
		return nil
	}
	return rec
}

// notLive 解释「为什么这个运行不能操作」：记录还在说明它已不在待批准 / 进行中（409），否则才是不存在（404）。
func (s *ExecService) notLive(ctx context.Context, runID string) error {
	if s.archived(ctx, runID) != nil {
		return ErrRunNotAwaiting
	}
	return ErrRunNotFound
}

// CancelByHuman 由人类取消一次进行中（或待批准）的运行。
func (s *ExecService) CancelByHuman(ctx context.Context, runID, actor, ip string) (*model.TerminalRun, error) {
	lr := s.liveRun(runID)
	if lr == nil {
		// 竞态：人点「取消」的瞬间命令刚好结束了。取消想要的结果（它不再运行）已经达到，把最终记录原样返回，
		// 这样界面上「取消并接管」之类的后续动作可以照常继续，而不是报一个让人困惑的「不存在」。
		if rec := s.archived(ctx, runID); rec != nil && model.IsFinalRunState(rec.State) {
			return rec, nil
		}
		return nil, ErrRunNotFound
	}
	s.cancelRun(lr, "user")
	s.auditRun(EvExecCancel, ResultOK, lr.snapshot(), actor, ip, ActorHuman, "")
	out := lr.snapshot()
	return &out, nil
}

// SetPaused 暂停 / 恢复模型执行。scope 为 "all"（全局）或 "host"（machine 指定主机）。
// 暂停会立刻取消该范围内所有进行中的模型运行并作废待批准项；恢复不会让它们复活。
func (s *ExecService) SetPaused(ctx context.Context, scope, machine string, paused bool, actor, ip string) error {
	event := EvExecPause
	if !paused {
		event = EvExecResume
	}
	switch scope {
	case "all":
		if err := s.h.store.SetSystemFlag(ctx, store.SettingTerminalExecPaused, paused); err != nil {
			return err
		}
		s.mu.Lock()
		s.paused = paused
		s.mu.Unlock()
		if paused {
			s.cancelScope("", "pause")
		}
		s.h.audit.record(ctx, auditEvent{Event: event, Actor: actor, IP: ip, ActorKind: ActorHuman, Detail: "scope=all"})
	case "host":
		_, changed, err := s.h.store.SetTerminalHostModel(ctx, machine, store.HostModelUpdate{ModelPaused: &paused})
		if err != nil {
			return err
		}
		if paused {
			s.cancelScope(machine, "pause")
		}
		if changed {
			s.h.audit.record(ctx, auditEvent{Event: event, Actor: actor, IP: ip, MachineCode: machine, ActorKind: ActorHuman, Detail: "scope=host"})
		}
	default:
		return errors.New("scope 只能是 all 或 host")
	}
	s.bus.publishPause(s.isPaused())
	return nil
}

// ModelSettings 修改一台主机的模型访问设置（G2 与执行模式）；收回授权时立即取消该主机上的模型运行。
func (s *ExecService) ModelSettings(ctx context.Context, machine string, upd store.HostModelUpdate, actor, ip string) (*model.TerminalHost, error) {
	host, changed, err := s.h.store.SetTerminalHostModel(ctx, machine, upd)
	if err != nil {
		return nil, err
	}
	if !changed {
		return host, nil
	}
	detail := "allow_model=" + boolStr(host.AllowModel) + " mode=" + modeLabel(host.ExecMode) + " default_new=" + store.HostDefaultState(host.DefaultForNewModels)
	s.h.audit.record(ctx, auditEvent{Event: EvHostModel, Actor: actor, IP: ip, MachineCode: machine, ActorKind: ActorHuman, Detail: detail})
	if upd.AllowModel != nil && !*upd.AllowModel {
		s.cancelScope(machine, "revoked")
	}
	if upd.ExecMode != nil && host.ExecMode == model.TerminalModeReadonly {
		s.cancelScope(machine, "revoked")
	}
	s.bus.publishHost(machine, s.peerFor(machine) != nil)
	return host, nil
}

func boolStr(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

func modeLabel(m string) string {
	if m == "" {
		return model.TerminalModeAuto
	}
	return m
}
