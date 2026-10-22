package terminal

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/teacat99/RelayMesh/v2/internal/model"
	"github.com/teacat99/RelayMesh/v2/internal/termexec"
	"github.com/teacat99/RelayMesh/v2/internal/termproto"
)

// 把一次运行的状态与输出整理成返回给模型的结果，并实现 terminal_run（status / output / cancel）。

func failMessage(reason string) string {
	switch reason {
	case termexec.CodeHostDenied:
		return blockMessage(termexec.CodeHostDenied)
	case termproto.RunFailBusy:
		return "主机上同时运行的命令过多，请稍后重试"
	case termproto.RunFailBad:
		return "主机认为请求参数无效"
	case termproto.RunFailCwd:
		return "工作目录不存在或不是目录"
	case termproto.RunFailNoShell:
		return "主机上找不到可用的 shell"
	case termproto.RunFailSpawn:
		return "命令无法启动"
	case termproto.RunFailUnavailable:
		return "主机没有可用于保存运行输出的目录，不支持模型执行"
	case termproto.RunFailHolder:
		return "主机上的会话守护进程不可用"
	case termproto.RunFailDraining:
		return "主机正在关停"
	case "send_failed":
		return "无法把命令发送给主机（连接已断开）"
	case "too_large":
		return "命令太大，无法放进一次下发"
	case "start_timeout":
		return "主机长时间没有应答，已放弃这次运行"
	case "approval_timeout":
		return "等待用户批准超时（10 分钟），命令没有执行"
	case "rejected_by_user":
		return "用户拒绝了这条命令，没有执行"
	case "withdrawn":
		return "命令在批准前被撤回，没有执行"
	case "hub_restart":
		return "服务重启，待批准的命令已作废"
	case "not_on_host":
		return "主机重连后已找不到这次运行，结果无法确认"
	case "overdue":
		return "运行超过预期时间仍无结果，结果无法确认"
	case "host_reboot":
		return "主机重启，进程已丢失"
	case "holder_exit", "holder_stopped":
		return "主机上的会话守护进程已退出，进程已丢失"
	// 取消原因（运行状态是 cancelled 时 deny_reason 里的取消来源）。
	case "user":
		return "你在 ExecMesh 里取消了这次运行"
	case "model":
		return "模型自己取消了这次运行"
	case cancelPause:
		return "模型执行被暂停（全局或这台主机），进行中的运行被取消"
	case cancelRevoked:
		return "这台主机的模型授权被收回（或改成了只读），进行中的运行被取消"
	case cancelGateClosed:
		return "终端总闸关闭（服务端的终端功能被整体停用），进行中的运行被一并取消"
	case cancelHostRemoved:
		return "这台主机被停用或删除，进行中的运行被取消"
	case cancelTokenChanged:
		return "这次运行所用的令牌被修改、停用、删除或重新生成，进行中的运行被取消"
	case cancelUnknownRun:
		return "服务端不认识这次运行（记录已不存在），主机上的进程被取消"
	case "system":
		return "被系统取消（旧版本统一记成 system：总闸关闭、主机停用、令牌变更等都会这样，当时没有记下具体是哪一种）"
	case "ssh_unreachable":
		return "无法连接到 SSH 主机（网络不通或服务未运行）"
	case "ssh_auth_failed":
		return "SSH 认证失败：管理员需要在 Web 主机页更新凭据"
	case "ssh_host_key_mismatch", "ssh_untrusted":
		return blockMessage(termexec.CodeSSHUntrusted)
	case "ssh_secret_unreadable":
		return "无法解开保存的 SSH 凭据（RELAYMESH_SSH_SECRET_KEY 可能已更换）：管理员需要重新填写凭据"
	case "ssh_cred_missing":
		return blockMessage(termexec.CodeSSHCredMissing)
	case termproto.RunFailDenied:
		// 运行中途网关本机才开启了一票否决：远端作业已经无法再经网关观察。
		return "承载这台 SSH 主机的网关在本机策略（--deny-model-exec）下拒绝了模型执行，这次运行的结果无法再确认"
	case "ssh_gateway_unavailable":
		return "承载这台 SSH 主机的网关当前不可用（离线、没有开启 SSH 网关能力或没有及时应答）"
	case "ssh_target_denied":
		return "目标不在网关主机的允许名单内（网关本机的 --ssh-allow 限制）"
	case "ssh_unavailable":
		return blockMessage(termexec.CodeSSHOff)
	case "ssh_unsupported":
		return "这台主机不是受支持的 POSIX 环境（需要 sh 与 base64）"
	case "ssh_host_removed":
		return "SSH 主机已被停用或删除，无法再确认这次运行的结果"
	case "proc_gone":
		return "远端进程已经消失却没有留下退出码（可能被外部终止，或主机重启），结果无法确认"
	case "output_limit":
		return "输出超过 256 MiB 的上限，命令已被终止"
	}
	return ""
}

func stateHint(rec model.TerminalRun) string {
	switch rec.State {
	case model.RunRunning:
		return "命令仍在运行：用 terminal_run {action:\"status\", run_id, wait_seconds} 等待，{action:\"output\"} 读取已产生的输出，{action:\"cancel\"} 取消。"
	case model.RunAwaitingApproval:
		return "命令正在等待用户在 Web（ExecMesh 模式）里批准；不要重复提交，用 terminal_run {action:\"status\", wait_seconds} 查询。"
	}
	return ""
}

// resultFor 生成返回给模型的结果。end 非空（刚结束）时优先使用其中随附的输出；否则只给元数据与预览。
func (s *ExecService) resultFor(ctx context.Context, rec model.TerminalRun, end *termproto.RunEnd, maxOut int) *termexec.RunResult {
	r := &termexec.RunResult{
		RunID:  rec.RunID,
		State:  rec.State,
		Target: rec.Target,
		Thread: rec.Thread,
		Mode:   rec.PolicyMode,
	}
	// 开始时间：已开始用实际开始时间；仍在等待批准（或刚被拒绝）时用提交时间。
	if rec.StartedAt != nil {
		r.StartedAt = rec.StartedAt.UTC().Format(time.RFC3339)
	} else if !rec.CreatedAt.IsZero() {
		r.StartedAt = rec.CreatedAt.UTC().Format(time.RFC3339)
	}
	if rec.EndedAt != nil {
		r.FinishedAt = rec.EndedAt.UTC().Format(time.RFC3339)
	}
	switch rec.State {
	case model.RunRunning, model.RunAwaitingApproval:
		r.Hint = stateHint(rec)
		if rec.State == model.RunRunning {
			if a := s.peerFor(rec.Target); a == nil || !a.usable() {
				r.Warnings = append(r.Warnings, "主机当前离线：命令仍由主机上的守护进程持有，主机重连后会自动对账")
			}
		}
		return r
	case model.RunDenied, model.RunRejected:
		r.Reason = rec.DenyReason
		r.Message = failMessage(rec.DenyReason)
		return r
	}

	r.ExitCode = rec.ExitCode
	r.Signal = rec.Signal
	r.DurationMS = rec.DurationMS
	if rec.ExitCode == nil && (rec.State == model.RunFailed || rec.State == model.RunLost) && rec.DenyReason != "" {
		r.Reason = rec.DenyReason
		r.Message = failMessage(rec.DenyReason)
	}
	if rec.State == model.RunCancelled && rec.DenyReason != "" {
		// 被取消的运行也说明是谁、为什么取消的：模型据此判断要不要重新提交。
		r.Reason = rec.DenyReason
		r.Message = failMessage(rec.DenyReason)
	}
	if rec.State == model.RunLost && r.Message == "" {
		r.Message = "结果无法确认"
	}
	if rec.State == model.RunTimedOut {
		r.Message = "命令超过 timeout_seconds 被终止"
	}
	// 刚结束的运行：输出随结束事件附带（小输出全文，大输出只带开头，其余向主机读取）。
	// 事后查询（status / 幂等重放）：没有随附内容，一律向主机读取同样形状的结果；主机读不到时才退回库里的脱敏预览。
	a := s.peerFor(rec.Target)
	var outInline, errInline []byte
	outBytes, errBytes := rec.StdoutBytes, rec.StderrBytes
	var outDropped, errDropped int64
	if end != nil {
		outBytes, errBytes = end.OutBytes, end.ErrBytes
		outInline, errInline = end.Out, end.Err
		outDropped, errDropped = end.OutDropped, end.ErrDropped
	}
	r.Stdout = s.streamOutput(ctx, a, rec.RunID, "stdout", outBytes, outDropped, outInline, maxOut)
	r.Stderr = s.streamOutput(ctx, a, rec.RunID, "stderr", errBytes, errDropped, errInline, maxOut)
	unreadable := (r.Stdout != nil && r.Stdout.Text == "" && r.Stdout.Bytes > 0) || (r.Stderr != nil && r.Stderr.Text == "" && r.Stderr.Bytes > 0)
	if unreadable {
		r.Preview = &termexec.Preview{Head: rec.PreviewHead, Tail: rec.PreviewTail}
		r.Warnings = append(r.Warnings, "无法从主机读取完整输出（主机离线或输出已被清理），这里只给出脱敏预览")
		r.Hint = "主机恢复在线后，可用 terminal_run {action:\"output\", run_id, stream, offset} 分页读取完整输出。"
	}
	if r.Stdout != nil || r.Stderr != nil {
		r.Notice = termexec.Notice
	}
	if (r.Stdout != nil && r.Stdout.Truncated) || (r.Stderr != nil && r.Stderr.Truncated) {
		r.Hint = "输出被截断：用 terminal_run {action:\"output\", run_id, stream, offset: next_offset} 继续读取。"
	}
	return r
}

// streamOutput 生成某一路输出的返回内容：小输出直接用随结束事件附带的全文，大输出向主机读取开头 + 末尾一小段。
func (s *ExecService) streamOutput(ctx context.Context, a execPeer, runID, stream string, total, dropped int64, inline []byte, maxOut int) *termexec.Output {
	if total <= 0 {
		return nil
	}
	out := &termexec.Output{Bytes: total, DroppedBytes: dropped}
	if int64(len(inline)) == total && dropped == 0 {
		data := inline
		if len(data) > maxOut {
			kept, _ := trimIncompleteTail(data[:maxOut])
			data = kept
			out.Truncated = true
			out.NextOffset = int64(len(kept))
			out.Tail = cleanOutput(trimLeadingContinuation(inline[max(len(inline)-execTailBytes, len(kept)):]))
		}
		out.Text = cleanOutput(data)
		return out
	}
	if a == nil || !a.usable() {
		return out // 读不到：调用方会补充预览与提示
	}
	rctx, cancel := context.WithTimeout(ctx, runReadTimeout)
	defer cancel()
	page, err := a.readRun(rctx, runID, stream, 0, maxOut)
	if err != nil {
		return out
	}
	data := page.Data
	next := page.Next
	if !page.EOF || next < uint64(total) {
		kept, cut := trimIncompleteTail(data)
		data, next = kept, next-uint64(cut)
	}
	out.Text = cleanOutput(data)
	if int64(next) < total {
		out.Truncated = true
		out.NextOffset = int64(next)
		tailOff := uint64(total) - execTailBytes
		if total < execTailBytes || tailOff < next {
			tailOff = next
		}
		if tailOff < uint64(total) {
			if tp, err := a.readRun(rctx, runID, stream, tailOff, int(uint64(total)-tailOff)); err == nil {
				out.Tail = cleanOutput(trimLeadingContinuation(tp.Data))
			}
		}
	}
	return out
}

// ---------- terminal_run ----------

func runNotFound() *termexec.Error {
	return execErr(termexec.CodeNotFound, "找不到这个 run_id（或它不属于此令牌）")
}

// ownedRecord 返回此令牌创建的运行：优先取内存里的最新状态，其次查库；别人的运行与不存在的运行表现一致。
func (s *ExecService) ownedRecord(ctx context.Context, c termexec.Caller, id string) (model.TerminalRun, *liveRun, *termexec.Error) {
	if lr := s.liveRun(id); lr != nil {
		rec := lr.snapshot()
		if rec.CredentialID != c.CredentialID {
			return model.TerminalRun{}, nil, runNotFound()
		}
		return rec, lr, nil
	}
	rec, err := s.h.store.GetTerminalRun(ctx, id)
	if err != nil {
		return model.TerminalRun{}, nil, execErr(termexec.CodeInternal, "读取运行记录失败")
	}
	if rec == nil || rec.CredentialID != c.CredentialID {
		return model.TerminalRun{}, nil, runNotFound()
	}
	return *rec, nil, nil
}

func (s *ExecService) Run(ctx context.Context, c termexec.Caller, req termexec.RunRequest) (any, error) {
	if e := s.prelude(ctx, c, "run"); e != nil {
		return nil, e
	}
	id := strings.ToLower(strings.TrimSpace(req.RunID))
	if !termproto.ValidRunID(id) {
		return nil, execErr(termexec.CodeBadRequest, "run_id 必须是 32 位十六进制字符串")
	}
	rec, lr, terr := s.ownedRecord(ctx, c, id)
	if terr != nil {
		return nil, terr
	}
	maxOut := execDefaultOutput
	if lim := c.Credential.TerminalLimits.MaxOutputBytes; lim > 0 {
		maxOut = max(min(maxOut, lim), execMinOutput)
	}

	switch strings.ToLower(strings.TrimSpace(req.Action)) {
	case "", "status":
		wait := clampInt(req.WaitSeconds, 0, execMaxWait)
		if lim := c.Credential.TerminalLimits.MaxWaitSeconds; lim > 0 && wait > lim {
			wait = lim
		}
		if lr != nil && wait > 0 {
			s.waitDone(ctx, lr, time.Duration(wait)*time.Second)
			rec = lr.snapshot()
		}
		var end *termproto.RunEnd
		if lr != nil {
			end = lr.endFrame()
		}
		return s.resultFor(ctx, rec, end, maxOut), nil

	case "cancel":
		if model.IsFinalRunState(rec.State) || lr == nil {
			res := s.resultFor(ctx, rec, nil, maxOut)
			res.Message = "运行已经结束，无需取消"
			return res, nil
		}
		s.cancelRun(lr, "model")
		s.auditRun(EvExecCancel, ResultOK, rec, c.CredentialName, c.RemoteIP, ActorModel, "")
		s.waitDone(ctx, lr, execCancelWait)
		return s.resultFor(ctx, lr.snapshot(), lr.endFrame(), maxOut), nil

	case "output":
		return s.outputPage(ctx, c, rec, req, maxOut)
	}
	return nil, execErr(termexec.CodeBadRequest, "action 只能是 status / output / cancel")
}

// outputPage 读取运行的完整输出的一页（按字节偏移分页，从不经过 Hub 的数据库）。
func (s *ExecService) outputPage(ctx context.Context, c termexec.Caller, rec model.TerminalRun, req termexec.RunRequest, maxOut int) (any, error) {
	stream := strings.ToLower(strings.TrimSpace(req.Stream))
	if stream == "" {
		stream = "stdout"
	}
	if _, ok := termproto.ParseStream(stream); !ok {
		return nil, execErr(termexec.CodeBadRequest, "stream 只能是 stdout 或 stderr")
	}
	if req.Offset < 0 || req.Limit < 0 {
		return nil, execErr(termexec.CodeBadRequest, "offset / limit 不能为负数")
	}
	if rec.State == model.RunAwaitingApproval || rec.State == model.RunDenied || rec.State == model.RunRejected {
		return nil, execErr(termexec.CodeNotReady, "这次运行没有产生输出（状态：%s）", rec.State)
	}
	// 读取输出同样要求当前仍然被授权：G3 白名单与 G2 的主机授权在读取时重新检查。
	host, err := s.h.store.GetTerminalHost(ctx, rec.Target)
	if err != nil {
		return nil, execErr(termexec.CodeInternal, "读取主机信息失败")
	}
	if host == nil || !c.Credential.AllowsTerminalHost(rec.Target) {
		return nil, s.deny(c, "run", rec.Target, termexec.CodeNotAllowed, "此令牌已无权访问这台主机")
	}
	if !host.AllowModel {
		return nil, s.deny(c, "run", rec.Target, termexec.CodeHostOff, blockMessage(termexec.CodeHostOff))
	}
	a := s.peerFor(rec.Target)
	if a == nil || !a.usable() {
		return nil, execErr(termexec.CodeOutputGone, "主机当前离线，无法读取输出；主机上线后可再次读取")
	}
	limit := req.Limit
	if limit == 0 {
		limit = maxOut
	}
	limit = clampInt(limit, execMinOutput, execMaxOutput)
	if lim := c.Credential.TerminalLimits.MaxOutputBytes; lim > 0 && limit > lim {
		limit = max(lim, execMinOutput)
	}

	rctx, cancel := context.WithTimeout(ctx, runReadTimeout)
	defer cancel()
	page, err := a.readRun(rctx, rec.RunID, stream, uint64(req.Offset), limit)
	if err != nil {
		switch {
		case errors.Is(err, errRunReadNotFound):
			return nil, execErr(termexec.CodeOutputGone, "主机上已找不到这次运行的输出（可能已过保留期被清理）")
		case errors.Is(err, errRunReadBusy):
			return nil, execErr(termexec.CodeBusy, "主机正忙于读取其他输出，请稍后重试")
		}
		return nil, execErr(termexec.CodeOutputGone, "读取输出失败：%v", err)
	}
	data, next := page.Data, page.Next
	if !(page.EOF && next >= page.Total) {
		kept, cut := trimIncompleteTail(data)
		data, next = kept, next-uint64(cut)
	}
	return &termexec.OutputPage{
		RunID:      rec.RunID,
		Stream:     stream,
		Offset:     int64(page.Off),
		NextOffset: int64(next),
		TotalBytes: int64(page.Total),
		Text:       cleanOutput(data),
		EOF:        page.EOF && next >= page.Total,
		Running:    page.Running,
		Gap:        page.Gap,
		Notice:     termexec.Notice,
	}, nil
}
