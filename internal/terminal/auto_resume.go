package terminal

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/teacat99/RelayMesh/v2/internal/model"
	"github.com/teacat99/RelayMesh/v2/internal/store"
)

// 非交互运行异常退出之后的自动续跑（#151，D-260；起因是 #169 / #171）。
//
// 预设里打开了「异常退出时自动续跑」，这个预设发起的运行如果以「可重试」的错误退出（分辨规则见 run_exit.go），
// Hub 在退避之后用同一个预设再续聊一次。边界：
//   - 默认关闭，只有预设里明确打开才会发生；只认预设发起的运行，会话页的运行都不在内；
//   - 只重试 Cursor CLI 自己标成「可重试」的错误与崩溃类信号。你取消的、到了运行时长上限的、需要你处理的、
//     不可重试的、认不出原因的……一律不重试（只在聊天里写明为什么没有续跑）；
//   - 24 小时内最多自动续跑 3 次（硬上限：先占名额再提交，提交失败还回去）；
//     连续 2 次续跑都在 10 分钟内又失败（没有进展）就熔断，停下来通知你；
//   - 「成功」清零（#172，追加 #304）：续跑出来的运行连续跑满 10 分钟（预设里可调，AutoResumeOKMinutes），就算这次续跑成功，
//     24 小时内累计的续跑次数与退避从它之后重新算——否则长期无人看管的任务里，隔几小时断一次、每次续跑都成功，
//     攒满 3 次就再也续不了了。所以预算真正限制的是「没来得及跑出进展的续跑」，成功的不占名额；
//   - 退避 5 分钟、20 分钟、60 分钟（按 24 小时内已经续跑的次数）；到点之前你随时可以取消，你手动运行一次也会取代这次计划；
//   - 到点时重新检查一切：预设还在、开关还开着、出事的那次运行仍是最近一次、对话没有被别人接手、主机在线；
//     条件不满足就作废，不会硬跑；
//   - 提交走预设运行的同一条路径（runLauncherWith），总闸、暂停、主机「模型访问」与执行模式、令牌限额全部照常生效，没有旁路；
//   - 登记（排期 / 开跑 / 作废 / 放弃）写在预设的自动续跑登记里（system_settings 的 terminal_guard_state，名字是历史遗留），只含运行编号、次数、时间与一句原因，不含任何输出内容；
//     Hub 重启不丢排期，错过时机太久的排期会作废。

const (
	// autoResumeMax 是 24 小时内最多自动续跑几次（窗口见 autoResumeWindow）。
	autoResumeMax = 3
	// autoResumeWindow 是自动续跑预算的滚动窗口：这段时间内已经开跑的次数才占当前的「最多几次」（原先与会话守护的唤醒预算共用一个 24 小时窗口）。
	autoResumeWindow = 24 * time.Hour
	// autoResumeOKDefault 是「续跑成功」的默认判定时长：自动续跑出来的运行连续跑满这么久，就算这次续跑成功（24 小时内的次数清零）；
	// 活得比这短就算「没有进展」。预设里可以改（store.HeadlessRun.AutoResumeOKMinutes），默认值与 store.DefaultAutoResumeOKMinutes 一致。
	autoResumeOKDefault = store.DefaultAutoResumeOKMinutes * time.Minute
	// autoResumeBreak：连续这么多次「没有进展」就熔断。
	autoResumeBreak = 2
	// autoResumeMaxLate：排定的开跑时间过去这么久还没跑成（主机一直离线、Hub 停了很久……）就作废。
	autoResumeMaxLate = 2 * time.Hour
	// autoResumeSettle：对话在出事的运行结束之后这么久才有新动静，就说明有人接手了。
	// 出事的运行自己的收尾（读尾巴、给对话一个定论）也会产生动静，所以要留一点余量。
	autoResumeSettle = 2 * time.Minute

	// autoResumeActor 是聊天系统行里的来源标记。
	autoResumeActor = "launcher-auto"
	// autoResumeBusyWhy 是「到点时发现对话又在运行」的作废原因：不去和另一边打架。
	autoResumeBusyWhy = "对话或上一次运行还在进行（有人接手了），不去打架"
)

// defaultAutoResumeBackoff 是退避：第 n 次续跑之前等多久（n 超过长度就用最后一项）。
var defaultAutoResumeBackoff = []time.Duration{5 * time.Minute, 20 * time.Minute, time.Hour}

// autoResumeDelay 返回已经自动续跑 used 次之后，下一次续跑要等多久。
func autoResumeDelay(backoff []time.Duration, used int) time.Duration {
	if len(backoff) == 0 {
		backoff = defaultAutoResumeBackoff
	}
	if used < 0 {
		used = 0
	}
	if used >= len(backoff) {
		used = len(backoff) - 1
	}
	return backoff[used]
}

// ParseAutoResumeBackoff 解析 RELAYMESH_TERMINAL_AUTORESUME_BACKOFF_SEC：逗号分隔的秒数（如 "3,6,9"）。
// 只供实验与端到端测试缩短退避；不合法的项（不是正整数、超过一天）整个丢掉，全不合法返回 nil（用默认退避）。
func ParseAutoResumeBackoff(raw string) []time.Duration {
	var out []time.Duration
	for _, part := range strings.Split(raw, ",") {
		n, err := strconv.Atoi(strings.TrimSpace(part))
		if err != nil || n <= 0 || n > 24*3600 {
			continue
		}
		out = append(out, time.Duration(n)*time.Second)
	}
	return out
}

// autoRunInfo 标明一次预设运行是自动续跑发起的。
type autoRunInfo struct {
	Attempt int    // 这是 24 小时内第几次自动续跑
	Max     int    // 24 小时内最多几次
	Reason  string // 为什么续跑（上一次运行出事的原因，一句中文）
	// Prefer 是上一次运行（出事的那次）实际接上的对话编号，没有就空（#191）：「新会话」每次都新开的预设重试时接着它，
	// 而不是再新开一个对话；其它来源不用它（见 launcherPick）。
	Prefer string
}

// launcherRunOpts 是 runLauncherWith 的可选项；零值就是「你手动点了运行」。
// Auto 与 Agent 至多一个：自动续跑（#151）是系统发起的，Agent 是项目管理 Agent 经 MCP 发起的（#179，见 agent_run.go）。
type launcherRunOpts struct {
	Auto  *autoRunInfo
	Agent *agentRunInfo
}

// autoResumeSuffix 是写在运行说明末尾的标记：运行记录里留下「这次运行是自动续跑出来的」，界面据此显示次数。
func autoResumeSuffix(attempt, max int) string {
	return fmt.Sprintf("（自动续跑 %d/%d）", attempt, max)
}

var autoResumeSuffixRe = regexp.MustCompile(`（自动续跑 (\d+)/(\d+)）$`)

// parseAutoResumeSuffix 从运行说明里取出「自动续跑 n/m」；不是自动续跑出来的返回 0, 0。
func parseAutoResumeSuffix(desc string) (attempt, max int) {
	m := autoResumeSuffixRe.FindStringSubmatch(strings.TrimSpace(desc))
	if m == nil {
		return 0, 0
	}
	attempt, _ = strconv.Atoi(m[1])
	max, _ = strconv.Atoi(m[2])
	return attempt, max
}

// ---------- 通知 ----------

// AutoResumeEvent 是自动续跑要告诉你的事件。现在只有一种：系统决定不再自动续跑了（预算用完、连续没有进展、没法提交），
// 这时需要你出面。排期、开跑、作废都只写在聊天里，不推送：每次运行的结束本来就有「运行结束」推送。
// 纯事实：推不推、怎么写由接收方决定。
type AutoResumeEvent struct {
	PresetID   string
	Name       string
	Machine    string
	WorkflowID string
	// FromRun 是最后出事的那次运行（通知的去重键）。
	FromRun string
	// Reason 是它出事的原因，Why 是不再自动续跑的原因。
	Reason string
	Why    string
}

// SetAutoResumeHook 接入自动续跑的通知；启动时调用一次，传 nil 表示摘掉。
// fn 在判断协程里被同步调用，必须立即返回，不能在里面做网络请求；fn 里的 panic 会被截住。
func (h *Hub) SetAutoResumeHook(fn func(AutoResumeEvent)) {
	if h == nil || h.guardian == nil {
		return
	}
	if fn == nil {
		h.guardian.autoHook.Store(nil)
		return
	}
	h.guardian.autoHook.Store(&fn)
}

func (g *guardService) fireAutoResume(ev AutoResumeEvent) {
	fn := g.autoHook.Load()
	if fn == nil {
		return
	}
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[terminal] 自动续跑通知挂钩出错（自动续跑本身不受影响）：%v", r)
		}
	}()
	(*fn)(ev)
}

// ---------- 排期 ----------

// planAutoResume 在 ps（事务里的预设自动续跑登记）上登记「这次运行出事了，要不要续跑」的决定，返回登记的那条；
// 同一次运行已经登记过就什么都不做（created 为 false）。
//
// 决定顺序：先给「出事的运行如果本身是自动续跑出来的」标上有没有进展；连续没有进展就熔断；24 小时预算用完就放弃；
// 否则排期（退避取决于 24 小时内已经续跑了几次）。熔断与预算在排期时就判断，好让你立刻知道，而不是到点才发现。
func planAutoResume(ps *store.GuardPresetState, rec model.TerminalRun, exit RunExit, now time.Time, backoff []time.Duration, ok time.Duration) (store.AutoResumeRecord, bool) {
	if _, dup := ps.AutoResumeFrom(rec.RunID); dup {
		return store.AutoResumeRecord{}, false
	}
	if ok <= 0 {
		ok = autoResumeOKDefault
	}
	// 出事的运行如果本身是自动续跑出来的：连续跑满判定时长就是「续跑成功」（次数清零，下面算预算时已经看不到它与它之前的登记），
	// 不到就是「没有进展」（连续几次就熔断）。调用方在这之前已经走过一遍认定（settleAutoResume，会写聊天行），这里再认定一次是幂等的，
	// 只为让这个函数自己就能给出完整的决定。
	for i := len(ps.AutoResumes) - 1; i >= 0; i-- {
		r := &ps.AutoResumes[i]
		if r.RunID != rec.RunID || r.State != store.AutoResumeStarted {
			continue
		}
		if time.Duration(rec.DurationMS)*time.Millisecond >= ok {
			r.Settled, r.Quick = true, false
		} else if !r.Settled {
			r.Quick = true
		}
		break
	}
	used := ps.AutoResumeStartedSince(now.Add(-autoResumeWindow))
	r := store.AutoResumeRecord{
		FromRun: rec.RunID, Attempt: used + 1, Class: string(exit.Class), Code: exit.Code, Reason: exit.Text, PlannedAt: now,
	}
	switch {
	case ps.AutoResumeTrailingQuick() >= autoResumeBreak:
		r.State = store.AutoResumeGaveUp
		r.Why = fmt.Sprintf("连续 %d 次自动续跑都在 %s 内又失败了，没有进展", autoResumeBreak, runLimitText(int(ok/time.Second)))
	case used >= autoResumeMax:
		r.State = store.AutoResumeGaveUp
		r.Why = autoResumeBudgetWhy(ok)
	default:
		r.State = store.AutoResumePending
		r.Due = now.Add(autoResumeDelay(backoff, used))
	}
	ps.AutoResumes = append(ps.AutoResumes, r)
	return r, true
}

// autoResumeBackoff 返回生效的退避（测试与实验可以经 Options 缩短，生产用默认）。
func (g *guardService) autoResumeBackoff() []time.Duration {
	if b := g.h.opts.AutoResumeBackoff; len(b) > 0 {
		return b
	}
	return defaultAutoResumeBackoff
}

// autoResumeOK 返回这个预设生效的「续跑成功」判定时长：实验与测试可以经 Options 缩短（RELAYMESH_TERMINAL_AUTORESUME_OK_SEC），
// 否则取预设里的设置，没设就是默认的 10 分钟。
func (g *guardService) autoResumeOK(p store.TerminalLauncher) time.Duration {
	if d := g.h.opts.AutoResumeOK; d > 0 {
		return d
	}
	if p.Run != nil {
		return p.Run.EffectiveAutoResumeOK()
	}
	return autoResumeOKDefault
}

// autoResumeBudgetWhy 是「24 小时预算用完」的放弃原因：顺便说明怎样才算「成功」让计数清零，免得人以为长时间跑着的续跑也占名额。
func autoResumeBudgetWhy(ok time.Duration) string {
	return fmt.Sprintf("24 小时内已经自动续跑 %d 次，达到上限（续跑出来的运行连续跑满 %s，就算成功、次数会清零）", autoResumeMax, runLimitText(int(ok/time.Second)))
}

// runLasted 是一次预设运行实际跑了多久：已经终结的取记录里的时长；还在跑的从真正开始那刻算到 now；
// 状态没终结却已经是残留（主机早就不在了）、或还没开始的算 0——不能把「卡在进行中」的记录当成跑得很久。
func runLasted(rec model.TerminalRun, now time.Time) time.Duration {
	if model.IsFinalRunState(rec.State) {
		return time.Duration(rec.DurationMS) * time.Millisecond
	}
	if !runActive(rec, now) || rec.StartedAt == nil || rec.StartedAt.IsZero() {
		return 0
	}
	return now.Sub(*rec.StartedAt)
}

// settleAutoResume 在「开跑出了 runID 的那次续跑」连续跑满判定时长时，认定这次续跑成功：24 小时内累计的续跑次数与退避从它之后重新算，
// 并在聊天里写一行说明（只写一次）。ran 是这次运行已经跑了多久。runID 不是自动续跑出来的、没跑满、早就认定过，都什么也不做。
// 先读后写：绝大多数运行（手动运行、跑得再久的非续跑）根本不是续跑出来的，不该为它们每次都写登记。
func (g *guardService) settleAutoResume(ctx context.Context, p store.TerminalLauncher, runID string, ran time.Duration, now time.Time) bool {
	ok := g.autoResumeOK(p)
	if runID == "" || ran < ok {
		return false
	}
	state, err := g.h.store.GetTerminalGuardState(ctx)
	if err != nil {
		log.Printf("[terminal] 自动续跑读取状态失败：%v", err)
		return false
	}
	if r, found := state.Preset(p.ID).AutoResumeStarting(runID); !found || r.State != store.AutoResumeStarted || r.Settled {
		return false
	}
	changed := false
	if _, err := g.h.store.UpdateTerminalGuardState(ctx, now, func(d *store.GuardStateDoc) {
		changed = d.Preset(p.ID).SettleAutoResume(runID)
	}); err != nil {
		log.Printf("[terminal] 自动续跑认定成功保存失败（下一轮再试）：%v", err)
		return false
	}
	if changed && p.Run != nil {
		g.autoResumeLine(ctx, p.Run.WorkflowID, p.ID, runID, "settled", now, fmt.Sprintf(
			"预设「%s」自动续跑出来的运行已经连续跑满 %s，算这次续跑成功：24 小时内累计的续跑次数与退避从头算",
			p.Name, runLimitText(int(ok/time.Second))))
	}
	return changed
}

// claimAutoResumeRun 处理「运行在登记里写上它的运行编号之前就结束了」（#187）：续跑的提交是分开的几步——占名额、提交运行、写聊天行、
// 写运行编号（autoResumeOne）——运行真的很快就结束、或这几步被负载拖慢时，结束的处理（autoResumeOnEnded）会先到，按编号找不到登记：
// 跑满了判定时长的续跑认定不了成功，没有进展的续跑记不上「没有进展」，排期还把它当成占了名额。运行说明末尾带「自动续跑 n/m」，
// 据此把那条已开跑、还没有编号的登记认领过来；提交方随后写入的是同一个编号，所以与先后顺序无关。
// 不是续跑出来的运行（绝大多数）与编号早已写上的（常见情形）在这里就返回，不写登记。
func (g *guardService) claimAutoResumeRun(ctx context.Context, presetID string, rec model.TerminalRun, now time.Time) {
	attempt, _ := parseAutoResumeSuffix(rec.Description)
	if attempt <= 0 || rec.RunID == "" {
		return
	}
	state, err := g.h.store.GetTerminalGuardState(ctx)
	if err != nil {
		log.Printf("[terminal] 自动续跑读取状态失败：%v", err)
		return
	}
	if _, found := state.Preset(presetID).AutoResumeStarting(rec.RunID); found {
		return
	}
	if _, err := g.h.store.UpdateTerminalGuardState(ctx, now, func(d *store.GuardStateDoc) {
		d.Preset(presetID).ClaimAutoResumeRun(rec.RunID, attempt)
	}); err != nil {
		log.Printf("[terminal] 自动续跑认领运行编号保存失败：%v", err)
	}
}

// presetByRun 找出这次运行所属的预设；运行不是预设发起的、或预设已经删掉了，返回 nil。
func (g *guardService) presetByRun(ctx context.Context, rec model.TerminalRun) *store.TerminalLauncher {
	if rec.CredentialName != launcherRunCaller || !strings.HasPrefix(rec.Thread, launcherThreadPrefix) {
		return nil
	}
	id := strings.TrimPrefix(rec.Thread, launcherThreadPrefix)
	list, err := g.h.store.ListTerminalLaunchers(ctx)
	if err != nil {
		log.Printf("[terminal] 自动续跑读取预设失败：%v", err)
		return nil
	}
	for i := range list {
		if list[i].ID == id {
			return &list[i]
		}
	}
	return nil
}

// autoResumeOnEnded 在预设的一次运行终结时被调用（onRunEnded）：打开了自动续跑的预设，运行异常退出，就登记并写聊天系统行。
// 失败只记日志：它是个附带的安排，不能连累运行本身的收尾。
func (g *guardService) autoResumeOnEnded(rec model.TerminalRun) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[terminal] 自动续跑登记 panic：%v", r)
		}
	}()
	ctx, now := g.h.ctx, g.h.now()
	p := g.presetByRun(ctx, rec)
	if p == nil || !p.IsHeadless() || p.Run == nil {
		return
	}
	// 运行可能在登记里写上它的运行编号之前就结束了：先把那条还没有编号的登记认领过来，下面的认定与排期才找得到它。
	g.claimAutoResumeRun(ctx, p.ID, rec, now)
	// 续跑出来的运行连续跑满判定时长：这次续跑算成功（不论它后来怎么结束：失败、正常退出还是被你取消），次数清零。
	// 放在「要不要再续跑」之前：下面排期时就能看到清零之后的预算。
	g.settleAutoResume(ctx, *p, rec.RunID, time.Duration(rec.DurationMS)*time.Millisecond, now)
	if rec.State != model.RunFailed || !p.Run.AutoResume {
		return
	}
	wf := strings.TrimSpace(p.Run.WorkflowID)
	exit := classifyRunExit(rec)
	if !exit.Retry {
		g.autoResumeLine(ctx, wf, p.ID, rec.RunID, store.AutoResumeSkipped, now,
			fmt.Sprintf("没有自动续跑预设「%s」：%s。这类失败重试没有用，请你处理后手动运行", p.Name, exit.Text))
		return
	}

	var (
		created bool
		got     store.AutoResumeRecord
	)
	if _, err := g.h.store.UpdateTerminalGuardState(ctx, now, func(d *store.GuardStateDoc) {
		got, created = planAutoResume(d.Preset(p.ID), rec, exit, now, g.autoResumeBackoff(), g.autoResumeOK(*p))
	}); err != nil {
		log.Printf("[terminal] 自动续跑登记保存失败：%v", err)
		return
	}
	if !created {
		return
	}
	switch got.State {
	case store.AutoResumePending:
		g.autoResumeLine(ctx, wf, p.ID, rec.RunID, got.State, now, fmt.Sprintf(
			"预设「%s」的运行异常退出：%s。系统计划 %s自动续跑（第 %d/%d 次）；到点之前可以在启动页取消，你手动运行一次也会取代这次计划",
			p.Name, exit.Text, autoResumeWhen(got.Due.Sub(now)), got.Attempt, autoResumeMax))
	case store.AutoResumeGaveUp:
		g.autoResumeGaveUp(ctx, *p, got, now)
	}
}

// autoResumeWhen 把一段等待写成口语：「5 分钟后」。
func autoResumeWhen(d time.Duration) string {
	return runLimitText(int(d.Round(time.Second)/time.Second)) + "后"
}

// autoResumeLine 在聊天里记一条自动续跑相关的系统行。
func (g *guardService) autoResumeLine(ctx context.Context, workflowID, presetID, fromRun, state string, now time.Time, text string) {
	if strings.TrimSpace(workflowID) == "" {
		return
	}
	meta, _ := json.Marshal(map[string]any{"mode": "launcher_auto", "preset": presetID, "run_id": fromRun, "state": state})
	g.addWakeLine(ctx, workflowID, "system", autoResumeActor, text, string(meta), now)
}

// autoResumeGaveUp 处理「系统决定不再自动续跑」：聊天里写明原因，并通知你（需要你出面了）。
func (g *guardService) autoResumeGaveUp(ctx context.Context, p store.TerminalLauncher, r store.AutoResumeRecord, now time.Time) {
	wf := ""
	if p.Run != nil {
		wf = strings.TrimSpace(p.Run.WorkflowID)
	}
	g.autoResumeLine(ctx, wf, p.ID, r.FromRun, r.State, now,
		fmt.Sprintf("不再自动续跑预设「%s」：%s。请你看看再手动运行；这次的失败原因：%s", p.Name, r.Why, r.Reason))
	g.fireAutoResume(AutoResumeEvent{
		PresetID: p.ID, Name: p.Name, Machine: p.Host, WorkflowID: wf, FromRun: r.FromRun, Reason: r.Reason, Why: r.Why,
	})
}

// setAutoResume 在一次事务里改一条登记（按出事的运行编号找最近的一条），返回改完之后的样子；找不到返回 false。
// mutate 返回 false 表示不改（条件不满足），此时也返回 false。
func (g *guardService) setAutoResume(ctx context.Context, presetID, fromRun string, now time.Time, mutate func(*store.AutoResumeRecord) bool) (store.AutoResumeRecord, bool) {
	var (
		out store.AutoResumeRecord
		ok  bool
	)
	if _, err := g.h.store.UpdateTerminalGuardState(ctx, now, func(d *store.GuardStateDoc) {
		ps := d.Preset(presetID)
		for i := len(ps.AutoResumes) - 1; i >= 0; i-- {
			if ps.AutoResumes[i].FromRun != fromRun {
				continue
			}
			if mutate(&ps.AutoResumes[i]) {
				out, ok = ps.AutoResumes[i], true
			}
			return
		}
	}); err != nil {
		log.Printf("[terminal] 自动续跑登记更新失败：%v", err)
		return store.AutoResumeRecord{}, false
	}
	return out, ok
}

// ---------- 到点开跑 ----------

// autoResumeTick 检查所有待开跑的自动续跑：到点的重新核对条件，通过就提交。运行服务的周期循环每一轮调用一次。
func (g *guardService) autoResumeTick(ctx context.Context, now time.Time) {
	if !g.h.Enabled(ctx) {
		return // 总闸关着：主机都连不上，更不该排着自动续跑
	}
	state, err := g.h.store.GetTerminalGuardState(ctx)
	if err != nil {
		log.Printf("[terminal] 自动续跑读取状态失败：%v", err)
		return
	}
	g.autoResumeSettleTick(ctx, state, now)
	type cand struct {
		presetID string
		rec      store.AutoResumeRecord
	}
	var cands []cand
	for id, ps := range state.Presets {
		for _, r := range ps.AutoResumes {
			if r.State == store.AutoResumePending {
				cands = append(cands, cand{presetID: id, rec: r})
			}
		}
	}
	if len(cands) == 0 {
		return
	}
	sort.Slice(cands, func(i, j int) bool { return cands[i].rec.Due.Before(cands[j].rec.Due) })
	launchers, err := g.h.store.ListTerminalLaunchers(ctx)
	if err != nil {
		log.Printf("[terminal] 自动续跑读取预设失败：%v", err)
		return
	}
	byID := make(map[string]*store.TerminalLauncher, len(launchers))
	for i := range launchers {
		byID[launchers[i].ID] = &launchers[i]
	}
	for _, c := range cands {
		g.autoResumeOne(ctx, byID[c.presetID], c.presetID, c.rec, now)
	}
}

// autoResumeSettleTick 认定「还在跑、却已经连续跑满判定时长」的续跑成功：不必等它结束（它可能一直跑下去）。
// 运行结束时的处理（autoResumeOnEnded）同样会认定；这里让认定更早，也兜住「结束的回调没赶上」的情形（例如 Hub 在运行期间重启）。
// 只看 24 小时窗口内、已开跑且还没认定的登记：更早的不占当前预算，认不认定都没有影响。
func (g *guardService) autoResumeSettleTick(ctx context.Context, state store.GuardStateDoc, now time.Time) {
	type cand struct{ presetID, runID string }
	var cands []cand
	since := now.Add(-autoResumeWindow)
	for id, ps := range state.Presets {
		for _, r := range ps.AutoResumes {
			if r.State == store.AutoResumeStarted && !r.Settled && r.RunID != "" && r.StartedAt.After(since) {
				cands = append(cands, cand{presetID: id, runID: r.RunID})
			}
		}
	}
	if len(cands) == 0 {
		return
	}
	launchers, err := g.h.store.ListTerminalLaunchers(ctx)
	if err != nil {
		log.Printf("[terminal] 自动续跑读取预设失败：%v", err)
		return
	}
	byID := make(map[string]*store.TerminalLauncher, len(launchers))
	for i := range launchers {
		byID[launchers[i].ID] = &launchers[i]
	}
	for _, c := range cands {
		p := byID[c.presetID]
		if p == nil || p.Run == nil {
			continue
		}
		run, err := g.h.store.GetTerminalRun(ctx, c.runID)
		if err != nil || run == nil {
			continue
		}
		g.settleAutoResume(ctx, *p, c.runID, runLasted(*run, now), now)
	}
}

// autoResumeAbandon 把一条待开跑的登记作废（跳过）并在聊天里写明原因。
func (g *guardService) autoResumeAbandon(ctx context.Context, p *store.TerminalLauncher, presetID string, r store.AutoResumeRecord, state, why string, now time.Time) {
	if _, ok := g.setAutoResume(ctx, presetID, r.FromRun, now, func(x *store.AutoResumeRecord) bool {
		if x.State != store.AutoResumePending {
			return false // 你在这期间取消了，或已经处理过
		}
		x.State, x.Why = state, why
		return true
	}); !ok || p == nil || p.Run == nil {
		return
	}
	g.autoResumeLine(ctx, p.Run.WorkflowID, p.ID, r.FromRun, state, now,
		fmt.Sprintf("取消了预设「%s」的自动续跑计划：%s", p.Name, why))
}

// autoResumeOne 处理一条待开跑的登记：没到点就等；到点了重新核对条件，通过就占预算并提交，不通过就作废或放弃。
func (g *guardService) autoResumeOne(ctx context.Context, p *store.TerminalLauncher, presetID string, r store.AutoResumeRecord, now time.Time) {
	switch {
	case p == nil:
		g.autoResumeAbandon(ctx, nil, presetID, r, store.AutoResumeSkipped, "预设已经不存在了", now)
		return
	case !p.IsHeadless() || p.Run == nil:
		g.autoResumeAbandon(ctx, p, presetID, r, store.AutoResumeSkipped, "预设已经不是「非交互续聊」类型", now)
		return
	case !p.Run.AutoResume:
		g.autoResumeAbandon(ctx, p, presetID, r, store.AutoResumeSkipped, "预设里已经关掉了自动续跑", now)
		return
	case now.Before(r.Due):
		return
	case now.After(r.Due.Add(autoResumeMaxLate)):
		g.autoResumeAbandon(ctx, p, presetID, r, store.AutoResumeSkipped,
			fmt.Sprintf("错过了续跑的时机（排定时间已经过去超过 %s）", runLimitText(int(autoResumeMaxLate/time.Second))), now)
		return
	}
	latest := g.latestLauncherRun(ctx, p.ID)
	if latest == nil || latest.RunID != r.FromRun {
		g.autoResumeAbandon(ctx, p, presetID, r, store.AutoResumeSkipped, "你已经重新运行过这个预设了，这次计划作废", now)
		return
	}

	// 「新会话」每次都新开的预设：重试要接着出事的那次运行学到的对话，不能再新开一个（#191，见 launcherPick）。
	prefer := convOfRun(*latest)

	// 能不能运行：与手动运行同一套检查，只是这里按原因分流——主机离线就继续等，对话被占用 / 被接手就作废，别的算放弃。
	in := g.inspectLauncherPrefer(ctx, *p, now, prefer)
	if len(in.errs) > 0 {
		e := in.errs[0]
		switch e.Code {
		case "host_offline":
			return // 主机还没回来：下一轮再看，直到错过时机
		case "run_active", "conversation_live":
			g.autoResumeAbandon(ctx, p, presetID, r, store.AutoResumeSkipped, autoResumeBusyWhy, now)
		default:
			g.autoResumeFailed(ctx, *p, presetID, r, e.Message, now)
		}
		return
	}
	if in.cur != nil && ranAfter(in.cur.LastEventAt, runFinishedAt(*latest).Add(autoResumeSettle)) {
		g.autoResumeAbandon(ctx, p, presetID, r, store.AutoResumeSkipped, "对话在出事之后又有了新动静，说明有人接手了", now)
		return
	}

	// 占名额（硬上限）：占不到就放弃；占到了才提交。
	var (
		attempt int
		over    bool
		gone    bool
	)
	if _, err := g.h.store.UpdateTerminalGuardState(ctx, now, func(d *store.GuardStateDoc) {
		ps := d.Preset(presetID)
		used := ps.AutoResumeStartedSince(now.Add(-autoResumeWindow))
		for i := len(ps.AutoResumes) - 1; i >= 0; i-- {
			x := &ps.AutoResumes[i]
			if x.FromRun != r.FromRun {
				continue
			}
			if x.State != store.AutoResumePending {
				gone = true // 你在这期间取消了
				return
			}
			if used >= autoResumeMax {
				over = true
				x.State = store.AutoResumeGaveUp
				x.Why = autoResumeBudgetWhy(g.autoResumeOK(*p))
				return
			}
			attempt = used + 1
			x.State, x.Attempt, x.StartedAt = store.AutoResumeStarted, attempt, now
			return
		}
		gone = true
	}); err != nil {
		log.Printf("[terminal] 自动续跑占名额失败（下一轮再试）：%v", err)
		return
	}
	switch {
	case gone:
		return
	case over:
		if got, ok := g.autoResumeCurrent(ctx, presetID, r.FromRun); ok {
			g.autoResumeGaveUp(ctx, *p, got, now)
		}
		return
	}

	plan, _, err := g.runLauncherWith(ctx, p.ID, now, launcherRunOpts{Auto: &autoRunInfo{Attempt: attempt, Max: autoResumeMax, Reason: r.Reason, Prefer: prefer}})
	if err != nil {
		g.autoResumeRelease(ctx, *p, presetID, r, err, now)
		return
	}
	g.setAutoResume(ctx, presetID, r.FromRun, now, func(x *store.AutoResumeRecord) bool {
		x.RunID = plan.RunID
		return true
	})
	g.h.audit.record(ctx, auditEvent{
		Event: EvLauncherAutoRun, Actor: "system", ActorKind: ActorSystem, MachineCode: p.Host, RunID: plan.RunID,
		Detail: fmt.Sprintf("preset=%s attempt=%d/%d", p.ID, attempt, autoResumeMax),
	})
}

// ranAfter 判断 t 是不是明显晚于 limit；t 为零值（不知道）不算。
func ranAfter(t, limit time.Time) bool { return !t.IsZero() && t.After(limit) }

// autoResumeCurrent 读出一条登记现在的样子。
func (g *guardService) autoResumeCurrent(ctx context.Context, presetID, fromRun string) (store.AutoResumeRecord, bool) {
	state, err := g.h.store.GetTerminalGuardState(ctx)
	if err != nil {
		return store.AutoResumeRecord{}, false
	}
	return state.Preset(presetID).AutoResumeFrom(fromRun)
}

// autoResumeFailed 把一条待开跑的登记标成「放弃」（没法续跑，要你出面），聊天里写明并通知你。
func (g *guardService) autoResumeFailed(ctx context.Context, p store.TerminalLauncher, presetID string, r store.AutoResumeRecord, why string, now time.Time) {
	got, ok := g.setAutoResume(ctx, presetID, r.FromRun, now, func(x *store.AutoResumeRecord) bool {
		if x.State != store.AutoResumePending && x.State != store.AutoResumeStarted {
			return false
		}
		x.State, x.Why = store.AutoResumeGaveUp, why
		x.RunID, x.StartedAt = "", time.Time{}
		return true
	})
	if ok {
		g.autoResumeGaveUp(ctx, p, got, now)
	}
}

// autoResumeRelease 处理「名额占了、提交却失败」：把名额还回去。主机刚好掉线就回到待开跑继续等；
// 对话被占用就作废；其余（被闸门拒绝、命令构造失败……）放弃并通知。
func (g *guardService) autoResumeRelease(ctx context.Context, p store.TerminalLauncher, presetID string, r store.AutoResumeRecord, cause error, now time.Time) {
	var we *wakeError
	code, msg := "", cause.Error()
	if errors.As(cause, &we) {
		code, msg = we.Code, we.Message
	}
	switch code {
	case "host_offline":
		g.setAutoResume(ctx, presetID, r.FromRun, now, func(x *store.AutoResumeRecord) bool {
			x.State, x.StartedAt, x.RunID = store.AutoResumePending, time.Time{}, ""
			return true
		})
	case "run_active", "conversation_live":
		g.setAutoResume(ctx, presetID, r.FromRun, now, func(x *store.AutoResumeRecord) bool {
			x.State, x.Why, x.StartedAt, x.RunID = store.AutoResumeSkipped, autoResumeBusyWhy, time.Time{}, ""
			return true
		})
		if p.Run != nil {
			g.autoResumeLine(ctx, p.Run.WorkflowID, p.ID, r.FromRun, store.AutoResumeSkipped, now,
				fmt.Sprintf("取消了预设「%s」的自动续跑计划：%s", p.Name, autoResumeBusyWhy))
		}
	default:
		g.autoResumeFailed(ctx, p, presetID, r, msg, now)
	}
}

// cancelAutoResume 取消一条还没开跑的自动续跑（按出事的运行编号找）。已经开跑、已经作废的不能取消。
func (g *guardService) cancelAutoResume(ctx context.Context, fromRun string, now time.Time) (store.TerminalLauncher, error) {
	var zero store.TerminalLauncher
	fromRun = strings.TrimSpace(fromRun)
	if fromRun == "" {
		return zero, newWakeError(http.StatusBadRequest, "bad_request", "缺少运行编号")
	}
	state, err := g.h.store.GetTerminalGuardState(ctx)
	if err != nil {
		return zero, err
	}
	presetID := ""
	for id, ps := range state.Presets {
		if r, ok := ps.AutoResumeFrom(fromRun); ok && r.State == store.AutoResumePending {
			presetID = id
			break
		}
	}
	if presetID == "" {
		return zero, newWakeError(http.StatusConflict, "not_pending", "这次自动续跑已经不在排期里了（可能已经开跑、作废，或你取消过）")
	}
	_, ok := g.setAutoResume(ctx, presetID, fromRun, now, func(x *store.AutoResumeRecord) bool {
		if x.State != store.AutoResumePending {
			return false
		}
		x.State, x.Why = store.AutoResumeStopped, "你取消了这次自动续跑"
		return true
	})
	if !ok {
		return zero, newWakeError(http.StatusConflict, "not_pending", "这次自动续跑已经不在排期里了（可能刚刚开跑了）")
	}
	list, lerr := g.h.store.ListTerminalLaunchers(ctx)
	if lerr != nil {
		return zero, nil // 已经取消了；只是拿不到预设做聊天行
	}
	for _, l := range list {
		if l.ID == presetID {
			if l.Run != nil {
				g.autoResumeLine(ctx, l.Run.WorkflowID, l.ID, fromRun, store.AutoResumeStopped, now,
					fmt.Sprintf("你取消了预设「%s」的自动续跑计划", l.Name))
			}
			return l, nil
		}
	}
	return zero, nil
}

// ---------- 界面数据 ----------

// AutoResumeView 是一次预设运行相关的自动续跑现状：这次运行是不是自动续跑出来的、它出事之后系统怎么安排的。
type AutoResumeView struct {
	// Enabled 表示预设里开着自动续跑。
	Enabled bool `json:"enabled"`
	// Max 是 24 小时内最多自动续跑几次。
	Max int `json:"max"`
	// OKMinutes 是「续跑成功」的判定时长（分钟，不满 1 分钟的按 1 计；#172）：续跑出来的运行连续跑满这么久，24 小时内累计的次数就清零。
	OKMinutes int `json:"ok_minutes,omitempty"`
	// Settled 表示这次运行本身是自动续跑出来的、而且已经连续跑满判定时长：算续跑成功，次数已经清零。
	Settled bool `json:"settled,omitempty"`
	// Attempt 大于 0 表示这次运行本身是自动续跑出来的：24 小时内的第几次。
	Attempt int `json:"attempt,omitempty"`
	// State 是这次运行出事之后系统的安排：pending / started / stopped / skipped / gaveup；没有安排就空。
	State string `json:"state,omitempty"`
	// NextAttempt 是安排里这次续跑排第几次。
	NextAttempt int `json:"next_attempt,omitempty"`
	// Due 是待开跑（pending）的开跑时间。
	Due *time.Time `json:"due,omitempty"`
	// Why 是作废 / 放弃 / 取消的原因。
	Why string `json:"why,omitempty"`
	// NextRunID 是续跑出来的新运行（started）。
	NextRunID string `json:"next_run_id,omitempty"`
}

// autoResumeViewOf 整理一次预设运行的自动续跑现状；预设没开、这次运行也与自动续跑无关时返回 nil。
func autoResumeViewOf(p store.TerminalLauncher, ps *store.GuardPresetState, rec model.TerminalRun, ok time.Duration) *AutoResumeView {
	v := &AutoResumeView{Enabled: p.Run != nil && p.Run.AutoResume, Max: autoResumeMax, OKMinutes: max(int(ok/time.Minute), 1)}
	v.Attempt, _ = parseAutoResumeSuffix(rec.Description)
	if r, found := ps.AutoResumeStarting(rec.RunID); found {
		v.Settled = r.Settled
	}
	if r, ok := ps.AutoResumeFrom(rec.RunID); ok {
		v.State, v.Why, v.NextAttempt = r.State, r.Why, r.Attempt
		switch r.State {
		case store.AutoResumePending:
			due := r.Due
			v.Due = &due
		case store.AutoResumeStarted:
			v.NextRunID = r.RunID
		}
	}
	if !v.Enabled && v.Attempt == 0 && v.State == "" {
		return nil
	}
	return v
}
