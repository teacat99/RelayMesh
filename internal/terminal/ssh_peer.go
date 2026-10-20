package terminal

import (
	"context"
	"errors"
	"log"
	"strconv"
	"sync"
	"time"

	"github.com/teacat99/RelayMesh/v2/internal/sshexec"
	"github.com/teacat99/RelayMesh/v2/internal/termexec"
	"github.com/teacat99/RelayMesh/v2/internal/termproto"
)

// SSH 主机的执行层：在远端启动一个脱离连接的后台任务（任务目录里保存命令、输出与退出码），
// 然后用短连接轮询取回进展。连接断开不会影响远端任务；Hub 重启后可以凭运行 ID 接着轮询。
//
// 与 Agent 主机共用同一套状态机：这里只把进展翻译成与 Agent 相同的 termproto 帧，交给 ExecService.onRun*。

const (
	sshMaxRunsPerHost = 8
	sshLiveWindow     = 32 << 10 // 每次轮询、每一路最多取回的实时输出；积压更多时实时视图向前跳过
	// 经网关代理的 exec 应答（两路输出合计 + 脚本自身的头部）必须放得进 termproto.MaxSSHExecOut（40 KiB）：
	// 轮询结果里每一路数据是 base64（膨胀 4/3），12 KiB 原始数据 → 16 KiB 文本，两路合计约 32 KiB，留出余量给状态行。
	sshGatewayLiveWindow = 12 << 10
	// 读取输出时经网关每次最多取回的原始字节数（base64 后约 21.4 KiB）；更大的读取在 readSegment 里分块循环。
	sshGatewayReadChunk = 16 << 10
	sshMaxRunOutput     = 256 << 20
	sshPollFirst        = 200 * time.Millisecond
	sshPollMax          = 1500 * time.Millisecond
	sshPollFail         = 3 * time.Second
	sshTimeoutGrace     = 5 * time.Second
	sshCancelGrace      = 30 * time.Second
	sshLostPolls        = 2
	sshFactsEvery       = time.Minute
)

var errSSHBusy = errors.New("ssh host busy")

type sshPeer struct {
	b    *sshBackend
	code string

	mu   sync.Mutex
	runs map[string]*sshRun
}

type sshRun struct {
	id       string
	cancelCh chan string
	timeout  time.Duration
	base     time.Time // 起算时间：Hub 侧超时兜底以此为准
	resume   bool
	spec     sshexec.Spec // 最近一次取得的连接信息：主机被删除 / 停用之后仍可用来把取消送达远端
	haveSpec bool
}

var _ execPeer = (*sshPeer)(nil)

func (p *sshPeer) machine() string       { return p.code }
func (p *sshPeer) gone() <-chan struct{} { return nil }

// usable 报告这台 SSH 主机此刻是否具备执行条件：直连需要 SSH 主密钥有效；经网关接入则需要网关在线且具备 ssh 与凭据库能力
// （不需要主密钥——凭据在网关上）。
func (p *sshPeer) usable() bool {
	via, ok := p.b.routeOf(p.code)
	if !ok {
		return false
	}
	if via == "" {
		return p.b.enabled()
	}
	ready, _ := p.b.h.gatewayExecState(via)
	return ready
}

func (p *sshPeer) execSupport() (supported, denied bool, info *termproto.ExecInfo) {
	via, ok := p.b.routeOf(p.code)
	if !ok {
		return false, false, nil
	}
	if via == "" {
		if !p.b.enabled() {
			return false, false, nil
		}
	} else {
		ready, veto := p.b.h.gatewayExecState(via)
		if !ready {
			return false, false, nil
		}
		denied = veto
	}
	info = &termproto.ExecInfo{Shell: "bash", Sep: "/"}
	if f, ok := p.b.factsOf(p.code); ok {
		info.Home = f.Home
		if !f.Bash {
			info.Shell = "sh"
		}
	} else {
		p.b.refreshFactsAsync(p.code)
	}
	return true, denied, info
}

func (p *sshPeer) sendRunAck(string) error { return nil }

func (p *sshPeer) running(id string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	_, ok := p.runs[id]
	return ok
}

func (p *sshPeer) sendRunCancel(id, reason string) error {
	p.mu.Lock()
	sr := p.runs[id]
	p.mu.Unlock()
	if sr == nil {
		return nil
	}
	select {
	case sr.cancelCh <- reason:
	default: // 已有一个取消请求在排队
	}
	return nil
}

func (p *sshPeer) sendRun(r termproto.Run) error {
	b := p.b
	if !p.usable() {
		return errors.New("ssh backend unavailable")
	}
	if !sshexec.ValidRunID(r.ID) || !sshexec.ValidRemoteShell(r.Shell) {
		return errors.New("invalid ssh run request")
	}
	release, ok := b.h.track()
	if !ok {
		return errors.New("hub is shutting down")
	}
	sr := &sshRun{id: r.ID, cancelCh: make(chan string, 1), timeout: time.Duration(r.TimeoutSec) * time.Second, base: b.h.now()}
	p.mu.Lock()
	if _, dup := p.runs[r.ID]; dup {
		p.mu.Unlock()
		release()
		return errors.New("duplicate run id")
	}
	if len(p.runs) >= sshMaxRunsPerHost {
		p.mu.Unlock()
		release()
		return errSSHBusy
	}
	p.runs[r.ID] = sr
	p.mu.Unlock()
	go func() {
		defer release()
		defer p.finish(sr)
		p.drive(sr, &r)
	}()
	return nil
}

func (p *sshPeer) finish(sr *sshRun) {
	p.mu.Lock()
	delete(p.runs, sr.id)
	p.mu.Unlock()
}

// resume 在 Hub 重启之后接管上次留下的进行中运行：凭运行 ID 接着轮询远端任务。
func (b *sshBackend) resume(lr *liveRun) {
	rec := lr.snapshot()
	release, ok := b.h.track()
	if !ok {
		return
	}
	p := b.ensurePeer(rec.Target)
	base := rec.CreatedAt
	if rec.StartedAt != nil {
		base = *rec.StartedAt
	}
	sr := &sshRun{id: rec.RunID, cancelCh: make(chan string, 1), timeout: time.Duration(rec.TimeoutSec) * time.Second, base: base, resume: true}
	p.mu.Lock()
	p.runs[sr.id] = sr
	p.mu.Unlock()
	lr.mu.Lock()
	lr.peer = p
	lr.mu.Unlock()
	go func() {
		defer release()
		defer p.finish(sr)
		p.drive(sr, nil)
	}()
}

// refreshFactsAsync 在不知道主机环境（Hub 刚重启）时，后台探测一次，供之后的 terminal_targets 展示家目录等信息。
func (b *sshBackend) refreshFactsAsync(code string) {
	now := b.h.now()
	b.mu.Lock()
	if last, ok := b.factsTry[code]; ok && now.Sub(last) < sshFactsEvery {
		b.mu.Unlock()
		return
	}
	b.factsTry[code] = now
	b.mu.Unlock()
	release, ok := b.h.track()
	if !ok {
		return
	}
	go func() {
		defer release()
		ctx, cancel := context.WithTimeout(b.h.bgCtx, sshCallTimeout)
		defer cancel()
		spec, _, err := b.specFor(ctx, code)
		if err != nil {
			return
		}
		cmd, stdin := sshexec.FactsCommand()
		res, err := b.call(ctx, code, spec, cmd, stdin)
		if err != nil {
			return
		}
		if f, err := sshexec.ParseFacts(res.Stdout); err == nil {
			b.mu.Lock()
			b.facts[code] = f
			b.mu.Unlock()
		}
	}()
}

// ---------- 读取输出 ----------

func (p *sshPeer) readRun(ctx context.Context, id, stream string, off uint64, max int) (*RunPage, error) {
	if !sshexec.ValidRunID(id) {
		return nil, errRunReadNotFound
	}
	spec, _, err := p.b.specFor(ctx, p.code)
	if err != nil {
		return nil, err
	}
	rr, err := p.readSegment(ctx, spec, id, stream, int64(off), max)
	if err != nil {
		return nil, err
	}
	next := off + uint64(len(rr.Data))
	return &RunPage{
		Data: rr.Data, Off: off, Next: next, Total: uint64(rr.Size),
		EOF: next >= uint64(rr.Size), Running: p.running(id),
	}, nil
}

// readSegment 读取一路输出从 off 开始的至多 max 字节。经网关代理的 exec 应答有总量上限（termproto.MaxSSHExecOut），
// 所以大于 sshGatewayReadChunk 的读取在这里分块循环——调用方看到的仍是一次读取。
func (p *sshPeer) readSegment(ctx context.Context, spec sshexec.Spec, id, stream string, off int64, max int) (sshexec.ReadResult, error) {
	chunk := max
	if spec.Via != "" {
		chunk = min(max, sshGatewayReadChunk)
	}
	var acc sshexec.ReadResult
	for {
		want := min(chunk, max-len(acc.Data))
		rr, err := p.readOnce(ctx, spec, id, stream, off+int64(len(acc.Data)), want)
		if err != nil {
			return rr, err
		}
		if len(acc.Data) == 0 {
			acc = rr
		} else {
			acc.Data = append(acc.Data, rr.Data...)
			acc.Size = rr.Size
		}
		// 读满、读到末尾、或远端没有再给数据（输出还在增长也不会无限循环）：结束。
		if len(acc.Data) >= max || len(rr.Data) == 0 || off+int64(len(acc.Data)) >= rr.Size {
			return acc, nil
		}
	}
}

func (p *sshPeer) readOnce(ctx context.Context, spec sshexec.Spec, id, stream string, off int64, max int) (sshexec.ReadResult, error) {
	cmd, stdin, err := sshexec.ReadCommand(sshexec.ReadSpec{RunID: id, Stream: stream, Off: off, Max: max})
	if err != nil {
		return sshexec.ReadResult{}, err
	}
	res, err := p.b.call(ctx, p.code, spec, cmd, stdin)
	if err != nil {
		return sshexec.ReadResult{}, err
	}
	rr, err := sshexec.ParseRead(res.Stdout)
	if err != nil {
		return rr, err
	}
	if rr.Gone {
		return rr, errRunReadNotFound
	}
	if rr.Problem != "" {
		return rr, sshexec.ErrProtocol
	}
	return rr, nil
}

// ---------- 驱动一次运行 ----------

// streamTrack 记录一路输出的开头与结尾：运行结束时据此生成预览与内联全文，免得再向远端读取。
type streamTrack struct {
	head    []byte
	tail    []byte
	tailEnd int64
}

func (t *streamTrack) add(off int64, data []byte) {
	if len(data) == 0 {
		return
	}
	if off == int64(len(t.head)) && len(t.head) <= termproto.RunInlineMax {
		room := termproto.RunInlineMax + 1 - len(t.head)
		t.head = append(t.head, data[:min(room, len(data))]...)
	}
	if off == t.tailEnd {
		t.tail = append(t.tail, data...)
	} else {
		t.tail = append([]byte(nil), data...)
	}
	if len(t.tail) > termproto.RunPreviewTail {
		t.tail = append([]byte(nil), t.tail[len(t.tail)-termproto.RunPreviewTail:]...)
	}
	t.tailEnd = off + int64(len(data))
}

type driveState struct {
	out, err     streamTrack
	outOff       int64
	errOff       int64
	cancelBy     string
	cancelPend   bool
	cancelAt     time.Time
	timedOut     bool
	capHit       bool
	deadPolls    int
	fails        int
	cancelFailed int
}

func sshFailReason(err error) string {
	var se *SSHError
	switch {
	case errors.Is(err, errSSHHostGone):
		return "ssh_host_removed"
	case errors.Is(err, sshexec.ErrHostKeyMismatch):
		return "ssh_host_key_mismatch"
	case errors.Is(err, sshexec.ErrNoHostKey):
		return "ssh_untrusted"
	case errors.Is(err, sshexec.ErrAuthFailed):
		return "ssh_auth_failed"
	case errors.Is(err, sshexec.ErrUnsupported):
		return "ssh_unsupported"
	case errors.As(err, &se) && se.Code == "ssh_secret_unreadable":
		return "ssh_secret_unreadable"
	case errors.As(err, &se) && se.Code == termexec.CodeSSHOff:
		return "ssh_unavailable"
	case errors.As(err, &se) && se.Code == termexec.CodeSSHCredMissing:
		return "ssh_cred_missing"
	case errors.As(err, &se) && se.Code == codeExecDenied:
		return termproto.RunFailDenied
	case errors.As(err, &se) && se.Code == codeTargetDenied:
		return "ssh_target_denied"
	case errors.As(err, &se) && se.Code == codeRequestTooLarge:
		return "too_large"
	case errors.As(err, &se) && (isGatewayInfraCode(se.Code) || se.Code == "ssh_busy"):
		return "ssh_gateway_unavailable"
	}
	return "ssh_unreachable"
}

// permanentSSH 报告这类错误重试没有意义，需要管理员介入。
func permanentSSH(err error) bool {
	switch sshFailReason(err) {
	case "ssh_host_key_mismatch", "ssh_untrusted", "ssh_secret_unreadable", "ssh_unavailable", "ssh_host_removed", "ssh_unsupported",
		"ssh_cred_missing", "ssh_target_denied", termproto.RunFailDenied, "too_large":
		return true
	}
	return false
}

// specFor 取这次运行用的连接信息；主机被删除 / 停用之后退回到本次运行已持有的那份（只用于取消与收尾）。
func (p *sshPeer) specFor(ctx context.Context, sr *sshRun) (sshexec.Spec, error) {
	spec, _, err := p.b.specFor(ctx, p.code)
	if err == nil {
		sr.spec, sr.haveSpec = spec, true
		return spec, nil
	}
	if errors.Is(err, errSSHHostGone) && sr.haveSpec {
		return sr.spec, nil
	}
	return sshexec.Spec{}, err
}

func (p *sshPeer) end(id string, e termproto.RunEnd) {
	e.T, e.ID = termproto.TRunEnd, id
	p.b.h.exec.onRunEnd(p, e)
}

func (p *sshPeer) lost(id, reason string, dur int64) {
	p.end(id, termproto.RunEnd{State: termproto.RunLost, Reason: reason, DurMS: dur})
}

func (p *sshPeer) drive(sr *sshRun, run *termproto.Run) {
	b := p.b
	ex := b.h.exec
	ctx := b.h.bgCtx
	id := sr.id
	st := &driveState{}

	spec, err := p.specFor(ctx, sr)
	if err != nil {
		if run != nil {
			ex.onRunFailed(p, termproto.RunFailed{T: termproto.TRunFailed, ID: id, Reason: sshFailReason(err)})
		} else {
			p.lost(id, sshFailReason(err), 0)
		}
		return
	}

	if run != nil {
		if !p.launch(ctx, sr, spec, run, st) {
			return
		}
	}

	wait := sshPollFirst
	resumed := run == nil
	quiet := resumed       // 接管：第一次轮询取回的是历史输出，不再作为实时片段转发
	needStarted := resumed // 接管：确认主机上确实有这个任务之后补上「已启动」
	timer := time.NewTimer(wait)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return // Hub 正在关闭：远端任务继续运行，重启之后由 resume 接管
		case reason := <-sr.cancelCh:
			if st.cancelBy == "" {
				st.cancelBy, st.cancelPend, st.cancelAt = reason, true, b.h.now()
			}
		case <-timer.C:
		}
		now := b.h.now()

		if sp, serr := p.specFor(ctx, sr); serr == nil {
			spec = sp
		} else if permanentSSH(serr) && !sr.haveSpec {
			p.lost(id, sshFailReason(serr), p.durMS(sr))
			return
		}

		if st.cancelBy == "" && sr.timeout > 0 && now.After(sr.base.Add(sr.timeout+sshTimeoutGrace)) {
			// 远端的 timeout 包装没有生效（主机没有 timeout 命令，或命令忽略了 TERM）：由 Hub 兜底。
			st.cancelBy, st.cancelPend, st.cancelAt, st.timedOut = "timeout", true, now, true
		}
		if st.cancelPend {
			if p.cancelRemote(ctx, spec, id) {
				st.cancelPend = false
			} else if st.cancelFailed++; st.cancelFailed > 20 {
				st.cancelPend = false
			}
		}

		pr, perr := p.poll(ctx, spec, id, st.outOff, st.errOff)
		if perr != nil {
			if ctx.Err() != nil {
				return
			}
			st.fails++
			if errors.Is(perr, sshexec.ErrHostKeyMismatch) || errors.Is(perr, sshexec.ErrNoHostKey) {
				// 主机密钥与固定值不一致：可能是中间人，立刻停止，不再尝试连接。
				p.lost(id, "ssh_host_key_mismatch", p.durMS(sr))
				return
			}
			if reason := sshFailReason(perr); spec.Via != "" && permanentSSH(perr) {
				// 经网关接入的主机：凭据已不在网关上 / 网关本机禁止了这类连接——再轮询也不会好转，远端任务无法再观察，按「无法确认」收尾。
				p.lost(id, reason, p.durMS(sr))
				return
			}
			wait = sshPollFail
			timer.Reset(wait)
			continue
		}
		st.fails = 0
		if pr.Gone {
			p.lost(id, "not_on_host", p.durMS(sr))
			return
		}
		if pr.Problem != "" {
			wait = sshPollFail
			timer.Reset(wait)
			continue
		}

		if len(pr.Out) > 0 {
			if !quiet {
				ex.onOut(p, id, termproto.StreamStdout, uint64(st.outOff), pr.Out)
			}
			st.out.add(st.outOff, pr.Out)
			st.outOff += int64(len(pr.Out))
		}
		if len(pr.Err) > 0 {
			if !quiet {
				ex.onOut(p, id, termproto.StreamStderr, uint64(st.errOff), pr.Err)
			}
			st.err.add(st.errOff, pr.Err)
			st.errOff += int64(len(pr.Err))
		}
		quiet = false
		// 积压超过窗口：实时视图向前跳过（完整输出始终在远端，之后可按偏移读取）。
		win := liveWindow(spec)
		if pr.OutSize-st.outOff > win {
			st.outOff = pr.OutSize - win
		}
		if pr.ErrSize-st.errOff > win {
			st.errOff = pr.ErrSize - win
		}

		if needStarted {
			needStarted = false
			ex.onRunStarted(p, termproto.RunStarted{T: termproto.TRunStarted, ID: id, PID: pr.PID, Started: sr.base.UnixMilli()})
		}

		if pr.ExitCode != nil {
			p.complete(ctx, sr, spec, st, pr)
			return
		}
		if pr.PID > 0 && !pr.Alive {
			if st.deadPolls++; st.deadPolls >= sshLostPolls {
				if st.cancelBy != "" && !st.timedOut {
					p.end(id, termproto.RunEnd{State: termproto.RunCancelled, DurMS: p.durMS(sr), OutBytes: pr.OutSize, ErrBytes: pr.ErrSize})
				} else {
					p.lost(id, "proc_gone", p.durMS(sr))
				}
				return
			}
		} else {
			st.deadPolls = 0
		}
		if st.cancelBy != "" && !st.cancelPend && now.Sub(st.cancelAt) > sshCancelGrace+sshTimeoutGrace {
			p.lost(id, "overdue", p.durMS(sr))
			return
		}
		if pr.OutSize+pr.ErrSize > sshMaxRunOutput && st.cancelBy == "" {
			st.cancelBy, st.cancelPend, st.cancelAt, st.capHit = "system", true, now, true
		}

		wait = min(wait*3/2, sshPollMax)
		timer.Reset(wait)
	}
}

func (p *sshPeer) durMS(sr *sshRun) int64 { return p.b.h.now().Sub(sr.base).Milliseconds() }

// launch 在远端启动任务。成功返回 true；失败时已经向状态机报告了结果。
func (p *sshPeer) launch(ctx context.Context, sr *sshRun, spec sshexec.Spec, run *termproto.Run, st *driveState) bool {
	b := p.b
	ex := b.h.exec
	id := sr.id
	fail := func(reason string) bool {
		ex.onRunFailed(p, termproto.RunFailed{T: termproto.TRunFailed, ID: id, Reason: reason})
		return false
	}
	cmd, stdin, err := sshexec.LaunchCommand(sshexec.LaunchSpec{
		RunID: id, Command: run.Cmd, Shell: run.Shell, Cwd: run.Cwd, TimeoutSec: run.TimeoutSec,
	})
	run.Cmd = "" // 命令明文只在启动脚本里，不再留在内存
	if err != nil {
		return fail(termproto.RunFailBad)
	}
	var res sshexec.Result
	for attempt := 0; ; attempt++ {
		res, err = b.call(ctx, p.code, spec, cmd, stdin)
		// 启动脚本是幂等的（重放只会得到 DUP 与原进程号），连接中途断开时重试一次是安全的。
		if err != nil && attempt == 0 && errors.Is(err, sshexec.ErrConn) && ctx.Err() == nil {
			continue
		}
		break
	}
	if err != nil {
		if ctx.Err() != nil {
			return false
		}
		return fail(sshFailReason(err))
	}
	lr, err := sshexec.ParseLaunch(res.Stdout)
	if err != nil {
		return fail("ssh_unreachable")
	}
	if lr.Err != "" {
		switch lr.Err {
		case sshexec.LaunchErrCwd:
			return fail(termproto.RunFailCwd)
		case sshexec.LaunchErrSpawn:
			return fail(termproto.RunFailSpawn)
		}
		return fail(termproto.RunFailUnavailable)
	}
	sr.base = b.h.now()
	cwd := run.Cwd
	if cwd == "" {
		if f, ok := b.factsOf(p.code); ok {
			cwd = f.Home
		}
	}
	ex.onRunStarted(p, termproto.RunStarted{T: termproto.TRunStarted, ID: id, PID: lr.PID, Shell: lr.Shell, Cwd: cwd, Started: sr.base.UnixMilli()})
	if rel, ok := b.h.track(); ok {
		go func() {
			defer rel()
			b.sweepIfDue(ctx, p.code, spec)
		}()
	}
	return true
}

// liveWindow 是每次轮询每一路最多取回的实时输出：经网关代理时受应答帧大小限制，窗口更小。
func liveWindow(spec sshexec.Spec) int64 {
	if spec.Via != "" {
		return sshGatewayLiveWindow
	}
	return sshLiveWindow
}

func (p *sshPeer) poll(ctx context.Context, spec sshexec.Spec, id string, outOff, errOff int64) (sshexec.PollResult, error) {
	cmd, stdin, err := sshexec.PollCommand(sshexec.PollSpec{RunID: id, OutOff: outOff, ErrOff: errOff, MaxBytes: int(liveWindow(spec))})
	if err != nil {
		return sshexec.PollResult{}, err
	}
	res, err := p.b.call(ctx, p.code, spec, cmd, stdin)
	if err != nil {
		return sshexec.PollResult{}, err
	}
	return sshexec.ParsePoll(res.Stdout)
}

// cancelRemote 让远端终止任务（先 TERM，宽限后 KILL，作用于整个会话）。返回是否已送达。
func (p *sshPeer) cancelRemote(ctx context.Context, spec sshexec.Spec, id string) bool {
	cmd, stdin, err := sshexec.CancelCommand(id)
	if err != nil {
		return true
	}
	res, err := p.b.call(ctx, p.code, spec, cmd, stdin)
	if err != nil {
		return false
	}
	_, perr := sshexec.ParseCancel(res.Stdout)
	return perr == nil
}

func sigName(n int) string {
	switch n {
	case 1:
		return "SIGHUP"
	case 2:
		return "SIGINT"
	case 3:
		return "SIGQUIT"
	case 4:
		return "SIGILL"
	case 6:
		return "SIGABRT"
	case 8:
		return "SIGFPE"
	case 9:
		return "SIGKILL"
	case 10:
		return "SIGBUS"
	case 11:
		return "SIGSEGV"
	case 13:
		return "SIGPIPE"
	case 14:
		return "SIGALRM"
	case 15:
		return "SIGTERM"
	}
	return "SIG" + strconv.Itoa(n)
}

// classify 把远端退出码翻译成与 Agent 相同的结束状态。
func (p *sshPeer) classify(sr *sshRun, st *driveState, code int) (state string, codePtr *int, signal, reason string) {
	switch {
	case st.capHit:
		return termproto.RunSignaled, nil, "SIGKILL", "output_limit"
	case st.timedOut:
		return termproto.RunTimeout, nil, "", ""
	case st.cancelBy != "" && code != 0:
		// 带上 Hub 自己下发的取消原因（与 Agent 主机的守护进程原样回报同一个口径）：事后才回答得了「它为什么被取消」。
		return termproto.RunCancelled, nil, "", termproto.SanitizeText(st.cancelBy, 32)
	case code == 124 && sr.timeout > 0:
		return termproto.RunTimeout, nil, "", "" // GNU timeout 的约定
	case code > 128 && code < 160:
		return termproto.RunSignaled, nil, sigName(code - 128), ""
	}
	c := code
	return termproto.RunExited, &c, "", ""
}

// complete 在远端任务结束之后收集结果：内联全文（小输出）、开头与结尾预览，然后交给状态机收尾。
func (p *sshPeer) complete(ctx context.Context, sr *sshRun, spec sshexec.Spec, st *driveState, pr sshexec.PollResult) {
	state, codePtr, signal, reason := p.classify(sr, st, *pr.ExitCode)

	var outHead, outTail, errHead, errTail []byte
	var wg sync.WaitGroup
	gather := func(stream string, t *streamTrack, size int64, head, tail *[]byte) {
		defer wg.Done()
		*head, *tail = p.collect(ctx, spec, sr.id, stream, t, size)
	}
	wg.Add(2)
	go gather("stdout", &st.out, pr.OutSize, &outHead, &outTail)
	go gather("stderr", &st.err, pr.ErrSize, &errHead, &errTail)
	wg.Wait()

	end := termproto.RunEnd{
		State: state, Code: codePtr, Signal: signal, Reason: reason, DurMS: p.durMS(sr),
		OutBytes: pr.OutSize, ErrBytes: pr.ErrSize,
		PrevHead: previewMerge(outHead, errHead, termproto.RunPreviewHead, true),
		PrevTail: previewMerge(outTail, errTail, termproto.RunPreviewTail, false),
	}
	if pr.OutSize > 0 && pr.OutSize <= termproto.RunInlineMax && int64(len(outHead)) == pr.OutSize {
		end.Out = outHead
	}
	if pr.ErrSize > 0 && pr.ErrSize <= termproto.RunInlineMax && int64(len(errHead)) == pr.ErrSize {
		end.Err = errHead
	}
	p.end(sr.id, end)
}

// collect 取回一路输出的开头（小输出即全文）与结尾。优先用轮询过程中已经见过的内容，不够才向远端读取。
func (p *sshPeer) collect(ctx context.Context, spec sshexec.Spec, id, stream string, t *streamTrack, size int64) (head, tail []byte) {
	if size <= 0 {
		return nil, nil
	}
	headNeed := int64(termproto.RunPreviewHead)
	if size <= termproto.RunInlineMax {
		headNeed = size
	}
	headNeed = min(headNeed, size)
	if int64(len(t.head)) >= headNeed {
		head = t.head[:headNeed]
	} else if rr, err := p.readSegment(ctx, spec, id, stream, 0, int(headNeed)); err == nil {
		head = rr.Data
	}
	switch {
	case size <= termproto.RunInlineMax:
		tail = head[max(0, len(head)-termproto.RunPreviewTail):]
	case t.tailEnd == size && int64(len(t.tail)) >= min(size, termproto.RunPreviewTail):
		tail = t.tail
	default:
		off := max(0, size-termproto.RunPreviewTail)
		if rr, err := p.readSegment(ctx, spec, id, stream, off, int(size-off)); err == nil {
			tail = rr.Data
		}
	}
	return head, tail
}

// previewMerge 把两路输出合成一份预览：head 时 stdout 在前、stderr 接在其后；tail 时同样的顺序取最后 limit 字节
// （即 stderr 的末尾总在最后）。SSH 的两路输出分开保存，无法还原它们交错的先后顺序。
func previewMerge(a, b []byte, limit int, head bool) []byte {
	if len(a)+len(b) == 0 {
		return nil
	}
	all := make([]byte, 0, len(a)+len(b))
	all = append(all, a...)
	all = append(all, b...)
	if len(all) <= limit {
		return all
	}
	if head {
		return all[:limit]
	}
	return all[len(all)-limit:]
}

// ---------- 与 ExecService 的衔接 ----------

// sshRestoreRuns 在 Hub 启动时接管上次进程留下的、属于 SSH 主机的进行中运行。
func (s *ExecService) sshRestoreRuns(lrs []*liveRun) {
	for _, lr := range lrs {
		// 经网关接入的主机不需要 SSH 主密钥（凭据在网关上）；网关此刻是否在线也无所谓——接管之后会一直重试轮询，直到它重新连上。
		// 主机已不存在（ok=false）也接管：drive 的第一步 specFor 会立即把它收尾为 ssh_host_removed。
		if via, ok := s.h.ssh.routeOf(lr.snapshot().Target); !ok || via != "" || s.h.ssh.enabled() {
			s.h.ssh.resume(lr)
			continue
		}
		// 直连主机而后端关闭（密钥缺失）：无法再观察远端，交给周期维护按「超期」收尾。
		log.Printf("[terminal] SSH 后端未启用，运行 %s 暂时无法接管（超期后将标记为丢失）", lr.id)
	}
}
