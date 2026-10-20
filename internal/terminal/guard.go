package terminal

import (
	"context"
	"errors"
	"log"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/teacat99/RelayMesh/v2/internal/model"
	"github.com/teacat99/RelayMesh/v2/internal/store"
	"github.com/teacat99/RelayMesh/v2/internal/termproto"
)

// 终端会话守护的服务层（D-214）：把「预设 → 会话 → 事实」收集起来交给 evaluateGuard 判断，
// 周期性地检查并在出事时通知，同时为「一键唤醒」生成要执行的计划。判断规则本身在 guard_eval.go。
//
// 它只做两件事：发现并告诉你（通知），以及你点一下就帮你把唤醒提示词送进去（计划由浏览器执行）。
// 它**不会**自己去唤醒任何会话——自动唤醒要等观察一段时间的误报率之后再决定（评估稿 D-214）。

const (
	// guardFirstDelay：Hub 启动后等这么久才开始第一轮判断，让主机的 Agent 先重连、报上会话清单。
	guardFirstDelay = 20 * time.Second
	// guardTickEvery：两轮判断的间隔。停摆以「分钟」为单位，没必要更勤。
	guardTickEvery = time.Minute
	// guardRemindEvery、guardMaxReminders：同一次事件没人处理时，隔多久补发一次提醒、最多补发几次。
	guardRemindEvery  = 6 * time.Hour
	guardMaxReminders = 2
	// guardWakeWindow：「一天最多唤醒几次」的滚动窗口。
	guardWakeWindow = 24 * time.Hour
	// guardProgressWindow：唤醒之后观察这么久，智能体仍没有任何新动静，就记为「没有效果」。
	guardProgressWindow = 10 * time.Minute
	// guardIndexRows：从会话索引里最多看多少行来找「这个预设最近的会话」。
	guardIndexRows = 200
)

// GuardAlert 是守护要告知你的一次事件。纯事实：推不推、怎么写由接收方（ntfy）决定。
type GuardAlert struct {
	PresetID   string
	Name       string
	Machine    string
	WorkflowID string
	State      GuardState // crashed | stalled
	Reason     string
	Since      time.Time // 事件的起点；零值 = 不详
	Episode    string
	// Reminder 为 0 表示首次通知；大于 0 表示同一次事件的第几次补发提醒。
	Reminder int
}

// SetGuardHook 接入守护事件通知；启动时调用一次，传 nil 表示摘掉。
// fn 在守护的判断协程里被同步调用（此时事件标识已落库），必须立即返回，不能在里面做网络请求；fn 里的 panic 会被截住。
func (h *Hub) SetGuardHook(fn func(GuardAlert)) {
	if h == nil || h.guardian == nil {
		return
	}
	if fn == nil {
		h.guardian.hook.Store(nil)
		return
	}
	h.guardian.hook.Store(&fn)
}

// guardRuntime 是一个预设会话的内存观察：主机只在会话发生变化时才推送快照，
// 所以这里只能在「两次看到的快照之间」比较，得到「有人连着多久了」「输出有没有增长」。
type guardRuntime struct {
	sid         string
	lastEnd     uint64
	outputAt    time.Time
	attachSince time.Time
}

type guardService struct {
	h        *Hub
	hook     atomic.Pointer[func(GuardAlert)]
	autoHook atomic.Pointer[func(AutoResumeEvent)] // 自动续跑放弃时的通知（#151），见 auto_resume.go
	since    atomic.Int64                          // 本次启动的时间（UnixNano）：此前的活动痕迹都在内存里丢了，从这一刻起重新计时
	// tickEvery 是两轮判断的间隔：默认 guardTickEvery；只有实验与端到端测试会经 Options.GuardTickEvery 缩短它。
	tickEvery time.Duration

	mu sync.Mutex
	rt map[string]*guardRuntime

	// 会话页「运行」（#128）正在提交的工作流：同一个工作流同一时刻只允许一个提交（见 session_run.go）。
	sessionRunMu   sync.Mutex
	sessionRunning map[string]bool
	// 预设运行正在提交的预设（#176）：同一个预设同一时刻只允许一个提交（共用 sessionRunMu）。
	// 「上一次运行还没结束」的检查看的是已落库的运行记录，两个几乎同时到达的提交都能在记录落库前通过它；
	// 对续聊来说后面的会被主机上的对话冲突挡住，但新开对话没有这道保险，会开出两个智能体——所以提交要串行。
	launcherRunning map[string]bool

	// 项目管理 Agent 经 MCP 运行预设（#179，见 agent_run.go）：频率账本的检查与提交放在同一把锁里，
	// 两个几乎同时到达的调用不会同时通过「每小时 / 每天 / 同时在跑」的上限。
	agentRunMu sync.Mutex
	// agentLimits 覆盖频率默认值（零值 = 用 defaultAgentRunLimits）；只有测试会改它。
	agentLimits AgentRunLimits
}

func newGuardService(h *Hub) *guardService {
	g := &guardService{h: h, rt: map[string]*guardRuntime{}, sessionRunning: map[string]bool{}, tickEvery: guardTickEvery}
	if h.opts.GuardTickEvery > 0 {
		g.tickEvery = h.opts.GuardTickEvery
	}
	g.since.Store(h.now().UnixNano())
	return g
}

// guardNameKey 是「按名字认会话」的键：与启动器一致（启动时新建会话用的就是预设的名字），不分大小写、空白折叠。
func guardNameKey(s string) string {
	return strings.ToLower(strings.Join(strings.Fields(s), " "))
}

func isNotFound(err error) bool {
	var ae *store.AppError
	return errors.As(err, &ae) && ae.Kind == store.ErrNotFound
}

// ---------- 周期判断 ----------

// run 是守护的后台循环：直到 ctx 取消。
func (g *guardService) run(ctx context.Context) {
	first := guardFirstDelay
	if g.tickEvery < first {
		first = g.tickEvery // 间隔被缩短（实验 / 测试）时，第一轮也别再多等
	}
	timer := time.NewTimer(first)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		g.safeTick(ctx)
		timer.Reset(g.tickEvery)
	}
}

// safeTick 跑一轮守护判断，再跑一轮自动续跑检查；两件事互不牵连，任何一个出意外都只记日志、下一轮继续。
func (g *guardService) safeTick(ctx context.Context) {
	func() {
		defer func() {
			if r := recover(); r != nil {
				log.Printf("[terminal] 会话守护的判断出错（下一轮继续）：%v", r)
			}
		}()
		g.tick(ctx, g.h.now())
	}()
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[terminal] 自动续跑的检查出错（下一轮继续）：%v", r)
		}
	}()
	g.autoResumeTick(ctx, g.h.now())
}

// guardAlertDue 判断这一轮要不要通知：同一次事件（事件标识不变）只通知一次，没人处理的话每隔
// guardRemindEvery 补发一次、最多 guardMaxReminders 次；通知之后你已经唤醒过的，就不再提醒（你在处理了）。
func guardAlertDue(v GuardVerdict, ps *store.GuardPresetState, now time.Time) (send bool, reminder int) {
	if !v.State.Alerting() || v.Episode == "" {
		return false, 0
	}
	if ps == nil || ps.NotifiedEpisode != v.Episode {
		return true, 0
	}
	if ps.Reminders >= guardMaxReminders || now.Sub(ps.NotifiedAt) < guardRemindEvery {
		return false, 0
	}
	if w, ok := ps.LastWake(); ok && w.At.After(ps.NotifiedAt) {
		return false, 0
	}
	return true, ps.Reminders + 1
}

// guardSettleProgress 判断最近一次唤醒有没有效果：之后智能体有了新动静就是「有」；观察窗口过完仍没有就是「没有」；
// 还在窗口内、也没动静就没有结论（返回空串）。
func guardSettleProgress(w store.GuardWakeRecord, f guardFacts, now time.Time) string {
	if w.Progress != "" {
		return w.Progress
	}
	if guardActiveAfter(f, w.At) {
		return store.GuardProgressYes
	}
	if now.Sub(w.At) >= guardProgressWindow {
		return store.GuardProgressNone
	}
	return ""
}

// guardActiveAfter：智能体在 at 之后有没有新动静（工具调用、一轮对话收尾、调用 RelayMesh）。
// 注意不含会话输出与人的输入：唤醒本身就会让终端有输入和回显，那不算智能体的反应。
func guardActiveAfter(f guardFacts, at time.Time) bool {
	return f.Link.LastToolAt.After(at) || f.Link.StopAt.After(at) || f.AgentActive.After(at)
}

type guardRows map[string][]model.TerminalSession

// tick 做一轮判断：对每个打开了守护的预设收集事实、下结论，必要时通知，并补上唤醒有没有效果的结论。
func (g *guardService) tick(ctx context.Context, now time.Time) {
	if !g.h.Enabled(ctx) {
		return // 总闸关着：主机都连不上，判断出来的「离线」没有意义，更不该据此打扰你
	}
	launchers, err := g.h.store.ListTerminalLaunchers(ctx)
	if err != nil {
		log.Printf("[terminal] 会话守护读取预设失败：%v", err)
		return
	}
	var guarded []store.TerminalLauncher
	known := make(map[string]struct{}, len(launchers))
	for _, l := range launchers {
		known[l.ID] = struct{}{}
		if l.Guard != nil && l.Guard.Enabled {
			guarded = append(guarded, l)
		}
	}
	g.forgetExcept(guarded)

	state, err := g.h.store.GetTerminalGuardState(ctx)
	if err != nil {
		log.Printf("[terminal] 会话守护读取状态失败：%v", err)
		return
	}

	var (
		alerts  []GuardAlert
		changes []func(*store.GuardStateDoc)
		rows    = guardRows{}
	)
	for _, p := range guarded {
		f := g.facts(ctx, p, now, rows)
		ps := state.Presets[p.ID]
		v, peff := g.verdictOf(p, f, ps)
		if peff.ClearMark || peff.Consume {
			id, eff := p.ID, peff
			changes = append(changes, func(d *store.GuardStateDoc) {
				st := d.Preset(id)
				g.applyPermitEffect(st, eff, now)
			})
		}

		if ps != nil {
			if w, ok := ps.LastWake(); ok && w.Progress == "" {
				if res := guardSettleProgress(w, f, now); res != "" {
					id, at := p.ID, w.At
					changes = append(changes, func(d *store.GuardStateDoc) {
						wakes := d.Preset(id).Wakes
						for i := len(wakes) - 1; i >= 0; i-- {
							if wakes[i].At.Equal(at) {
								wakes[i].Progress = res
								break
							}
						}
					})
				}
			}
		}

		if send, reminder := guardAlertDue(v, ps, now); send {
			a := GuardAlert{
				PresetID: p.ID, Name: p.Name, Machine: p.Host, WorkflowID: strings.TrimSpace(p.Guard.WorkflowID),
				State: v.State, Reason: v.Reason, Episode: v.Episode, Reminder: reminder,
			}
			if v.Since != nil {
				a.Since = *v.Since
			}
			alerts = append(alerts, a)
			id, episode := p.ID, v.Episode
			changes = append(changes, func(d *store.GuardStateDoc) {
				st := d.Preset(id)
				if reminder == 0 {
					st.NotifiedEpisode, st.Reminders = episode, 0
				} else {
					st.Reminders = reminder
				}
				st.NotifiedAt = now
			})
		}
	}
	// 预设已经删掉了，它的状态也不必留着。
	for id := range state.Presets {
		if _, ok := known[id]; !ok {
			gone := id
			changes = append(changes, func(d *store.GuardStateDoc) { delete(d.Presets, gone) })
		}
	}
	if len(changes) == 0 {
		return
	}
	if _, err := g.h.store.UpdateTerminalGuardState(ctx, now, func(d *store.GuardStateDoc) {
		for _, apply := range changes {
			apply(d)
		}
	}); err != nil {
		// 状态没落库就不通知：否则每一轮都会为同一件事再通知一次。
		log.Printf("[terminal] 会话守护保存状态失败（本轮不通知）：%v", err)
		return
	}
	for _, a := range alerts {
		g.fire(a)
	}
}

// fire 把事件交给挂钩；挂钩里的任何意外都不能影响守护。
func (g *guardService) fire(a GuardAlert) {
	fn := g.hook.Load()
	if fn == nil {
		return
	}
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[terminal] 会话守护通知挂钩出错（守护本身不受影响）：%v", r)
		}
	}()
	(*fn)(a)
}

// forgetExcept 丢掉不再受守护的预设的内存观察。
func (g *guardService) forgetExcept(keep []store.TerminalLauncher) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if len(g.rt) == 0 {
		return
	}
	alive := make(map[string]struct{}, len(keep))
	for _, l := range keep {
		alive[l.ID] = struct{}{}
	}
	for id := range g.rt {
		if _, ok := alive[id]; !ok {
			delete(g.rt, id)
		}
	}
}

// ---------- 收集事实 ----------

func (g *guardService) rowsOf(ctx context.Context, cache guardRows, host string) []model.TerminalSession {
	if r, ok := cache[host]; ok {
		return r
	}
	r, err := g.h.store.ListTerminalSessions(ctx, host, false, guardIndexRows)
	if err != nil {
		// 读不到索引就当作「没有记录」：判断结果是 unknown，不会据此打扰你。
		log.Printf("[terminal] 会话守护读取会话索引失败：%v", err)
		r = nil
	}
	cache[host] = r
	return r
}

func newerSession(a, b termproto.SessionInfo) bool {
	return a.Created > b.Created || (a.Created == b.Created && a.SID > b.SID)
}

// observe 更新并返回一个预设会话的内存观察；info 为 nil 表示现在没有存活的会话（观察清零）。
func (g *guardService) observe(presetID string, info *termproto.SessionInfo, now time.Time) (presentSince, outputAt time.Time) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if info == nil {
		delete(g.rt, presetID)
		return time.Time{}, time.Time{}
	}
	r := g.rt[presetID]
	if r == nil || r.sid != info.SID {
		// 第一次看到（或换了新会话）：记下当前的输出量作为基线，不把它当作「刚有输出」。
		r = &guardRuntime{sid: info.SID, lastEnd: info.End}
		g.rt[presetID] = r
	} else if info.End != r.lastEnd {
		r.lastEnd, r.outputAt = info.End, now
	}
	if info.Active > 0 {
		// 主机上最近一次交互（有输入，或有人连着）：时间取主机时钟，不能晚于现在。
		if t := time.Unix(info.Active, 0); !t.After(now) && t.After(r.outputAt) {
			r.outputAt = t
		}
	}
	if info.Attached > 0 {
		if r.attachSince.IsZero() {
			r.attachSince = now
		}
	} else {
		r.attachSince = time.Time{}
	}
	return r.attachSince, r.outputAt
}

// facts 收集判断一个预设所需的全部事实。读库出错的那部分当作「没有」：宁可判成 unknown，也不凭残缺的事实打扰你。
func (g *guardService) facts(ctx context.Context, p store.TerminalLauncher, now time.Time, cache guardRows) guardFacts {
	gd := p.Guard
	f := guardFacts{
		Now:      now,
		Silence:  gd.EffectiveSilence(),
		HubSince: time.Unix(0, g.since.Load()),
	}

	// ---- 会话本身 ----
	key := guardNameKey(p.Name)
	var infos []termproto.SessionInfo
	if a := g.h.onlineAgent(p.Host); a != nil && a.isEnabled() {
		f.HostOnline = true
		infos = a.sessionList()
	}
	var live, listedExit *termproto.SessionInfo
	for i := range infos {
		s := &infos[i]
		if guardNameKey(s.Name) != key {
			continue
		}
		if s.State == termproto.SessionRunning {
			if live == nil || newerSession(*s, *live) {
				live = s
			}
		} else if listedExit == nil || newerSession(*s, *listedExit) {
			listedExit = s
		}
	}
	if live != nil {
		created := time.Unix(live.Created, 0)
		if created.After(now) {
			created = now // 主机的时钟比 Hub 快：别让「会话创建时间」落在未来
		}
		f.Session = &guardSession{SID: live.SID, Created: created}
		f.PresentSince, f.OutputAt = g.observe(p.ID, live, now)
	} else {
		g.observe(p.ID, nil, now)
		f.Ended = g.lastEnd(ctx, p, key, listedExit, cache)
	}

	// ---- 智能体与 RelayMesh 里的事 ----
	if wf := strings.TrimSpace(gd.WorkflowID); wf != "" {
		st := g.h.store
		f.Link = st.AgentLinkOf(ctx, wf)
		f.HookSeen, _ = st.HookLastSeen(wf)
		f.AgentActive, _ = st.AgentLastActivity(wf)
		if fb, err := st.GetLatestWorkflowFeedbackSession(ctx, wf); err == nil && fb != nil {
			f.Feedback = &guardFeedback{
				Status: fb.Status, UpdatedAt: fb.UpdatedAt, DeadlineAt: fb.DeadlineAt,
				ConsumedByAI: fb.ConsumedByAI, Checks: fb.NoFeedbackChecks, MaxChecks: fb.MaxNoFeedbackChecks,
			}
		} else if err != nil && !isNotFound(err) {
			log.Printf("[terminal] 会话守护读取反馈会话失败：%v", err)
		}
		if sum, err := st.InboxSummary(ctx, wf, now); err == nil {
			f.InboxUnread = sum.Unread
			if sum.OldestUnreadAt != nil {
				f.InboxOldestUnread = *sum.OldestUnreadAt
			}
		}
		if counts, err := st.CountWorkItemsByState(ctx, wf); err == nil {
			f.DoingTasks = counts[model.WorkStateDoing]
		}
	}
	return f
}

// lastEnd 找这个预设「最近一个会话」的结束记录；没有存活的会话时才会调用。
// 返回 nil 表示没有可判断的记录：从没启动过、记录已被清理、主机离线而最近的会话还在运行，
// 或者主机刚报告会话退出而索引还没来得及记下（几秒内就会有，下一轮再判断）。
func (g *guardService) lastEnd(ctx context.Context, p store.TerminalLauncher, key string, listedExit *termproto.SessionInfo, cache guardRows) *guardEnd {
	rows := g.rowsOf(ctx, cache, p.Host)
	var newest *model.TerminalSession
	for i := range rows {
		if guardNameKey(rows[i].Name) == key {
			newest = &rows[i]
			break
		}
	}
	if newest == nil || newest.EndedAt == nil {
		return nil
	}
	if listedExit != nil && listedExit.SID != newest.SID && listedExit.Created > newest.StartedAt {
		// 主机列表里有个比索引里最新的还要新的已退出会话，它的结束记录还没落进索引：等下一轮。
		return nil
	}
	return &guardEnd{SID: newest.SID, At: *newest.EndedAt, Reason: newest.EndedReason, ExitCode: newest.ExitCode}
}

func (g *guardService) permitFacts(f guardFacts, ps *store.GuardPresetState) guardPermitFacts {
	var pf guardPermitFacts
	if ps == nil || ps.Permit == nil {
		return pf
	}
	pf.SuppressedEpisode = ps.Permit.SuppressedEpisode
	if ps.Permit.Permit != 0 {
		return pf
	}
	pf.Marked = !ps.Permit.SetAt.IsZero()
	pf.SetAt = ps.Permit.SetAt
	if pf.Marked && ps.Wakes != nil {
		for _, w := range ps.Wakes {
			if w.At.After(pf.SetAt) {
				pf.WakeSinceMark = true
				break
			}
		}
	}
	pf.OpenWorkSinceMark = permitOpenWorkSinceMark(f, pf.SetAt)
	return pf
}

func (g *guardService) applyPermitEffect(st *store.GuardPresetState, eff permitEffect, now time.Time) {
	if st.Permit == nil {
		st.Permit = &store.GuardWakePermitState{Permit: 1}
	}
	if eff.ClearMark {
		st.Permit = &store.GuardWakePermitState{Permit: 1}
		return
	}
	if eff.Consume {
		st.Permit.Permit = 1
		st.Permit.SuppressedEpisode = eff.SuppressEpisode
		st.Permit.Note = ""
	}
}

// verdictOf 在 evaluateGuard 的结论上补一点只有这一层才知道的说明，并叠加唤醒许可。
func (g *guardService) verdictOf(p store.TerminalLauncher, f guardFacts, ps *store.GuardPresetState) (GuardVerdict, permitEffect) {
	v := evaluateGuard(f)
	if v.State == GuardUnknown && f.Session != nil && strings.TrimSpace(p.Guard.WorkflowID) == "" {
		v.Reason = "没有填工作流编号，只能监测会话是否意外结束"
	}
	if p.Guard != nil && p.Guard.ExitEndsSession {
		v.Detail = append(v.Detail, "启动器用 exec 启动，命令退出即会话结束")
	}
	pf := g.permitFacts(f, ps)
	return applyWakePermit(v, f, pf)
}

// SetWakePermit 设置预设的唤醒许可（0 = 标记全部完成，1 = 取消标记）。
func (g *guardService) SetWakePermit(ctx context.Context, presetID string, permit int, by, note string, now time.Time) (*store.GuardWakePermitState, error) {
	if permit != 0 && permit != 1 {
		return nil, newWakeError(http.StatusBadRequest, "bad_permit", "permit 只能是 0 或 1")
	}
	note = strings.TrimSpace(note)
	if len([]rune(note)) > 200 {
		return nil, newWakeError(http.StatusBadRequest, "note_too_long", "备注最长 200 个字")
	}
	launchers, err := g.h.store.ListTerminalLaunchers(ctx)
	if err != nil {
		return nil, err
	}
	var p *store.TerminalLauncher
	for i := range launchers {
		if launchers[i].ID == presetID {
			p = &launchers[i]
			break
		}
	}
	if p == nil {
		return nil, newWakeError(http.StatusNotFound, "launcher_not_found", "这个预设已经不存在了")
	}
	if p.Guard == nil || !p.Guard.Enabled {
		return nil, newWakeError(http.StatusConflict, "guard_not_enabled", "这个预设没有开启会话守护")
	}
	var out *store.GuardWakePermitState
	_, err = g.h.store.UpdateTerminalGuardState(ctx, now, func(d *store.GuardStateDoc) {
		st := d.Preset(presetID)
		if permit == 0 {
			st.Permit = &store.GuardWakePermitState{Permit: 0, SetAt: now, SetBy: by, Note: note}
		} else {
			st.Permit = &store.GuardWakePermitState{Permit: 1}
		}
		out = st.Permit
	})
	return out, err
}

// FindGuardPresetByWorkflow 返回绑定该工作流且开启守护的第一个预设。
func FindGuardPresetByWorkflow(launchers []store.TerminalLauncher, workflowID string) *store.TerminalLauncher {
	wf := strings.TrimSpace(workflowID)
	for i := range launchers {
		l := &launchers[i]
		if l.Guard != nil && l.Guard.Enabled && strings.TrimSpace(l.Guard.WorkflowID) == wf {
			return l
		}
	}
	return nil
}
