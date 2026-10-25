package terminal

import (
	"context"
	"encoding/json"
	"log"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/teacat99/RelayMesh/v2/internal/agentstate"
	"github.com/teacat99/RelayMesh/v2/internal/model"
	"github.com/teacat99/RelayMesh/v2/internal/store"
)

// 非交互运行的输出监视（#129）。
//
// RelayMesh 自己用 `agent -p --output-format stream-json` 续聊一个对话时（预设的非交互续聊、会话页运行），
// 输出里的每个思考、工具调用、回复与终局都是确定的事件——比编辑器 hook 准：hook 在非交互模式下不报 stop、
// 恢复对话不报 sessionStart，智能体停了 hook 也不知道。所以运行期间由 Hub 读这份输出来判定这个对话的状态
// （来源记为 run），hook 的汇报只留记录、不改状态（store 的「运行权威」标记）。
//
// 设计要点：
//   - 轮询为主：从已读偏移起经执行通道的 readRun 取输出。实时帧（RunOut）只用来「碰一下」让轮询提前——
//     实时帧是有损的（批处理、限速、队列丢弃），而且 onOut 跑在 Agent 连接的读协程里，不能在那里读输出（会和读协程互等）。
//   - 判定权在这里，结束的定论在 onRunEnded：运行终结时补读尾巴，再按终态补一个 stop（输出里没有 result 才补）。
//   - 只有运行真的动过这个对话（读到过输出信号）才改状态：启动就失败的运行（命令不存在、没权限）不碰对话。
//   - 容忍一切：读失败（运行还没开始、主机忙、断线）下一轮再试；输出有缺口就重新对齐；监视协程崩了，权威标记到期自动失效。

// AgentStateEvent 是 Hub 自己推进了某个 Agent 会话的状态之后对外报告的变化（Web 据此重新取状态）。
type AgentStateEvent struct {
	WorkflowID string
	IdentityID uint
	SessionID  uint
	State      string
	Sub        string
	Source     string // 现在这个状态是谁说的（hook / tui / run）
	Since      time.Time
}

// SetAgentStateHook 接入「Hub 推进了 Agent 会话状态」的通知；启动时调用一次，传 nil 表示摘掉。
// fn 在监视协程里同步调用，必须立即返回；里面的 panic 会被截住。
func (h *Hub) SetAgentStateHook(fn func(AgentStateEvent)) {
	if h == nil || h.exec == nil || h.exec.watch == nil {
		return
	}
	if fn == nil {
		h.exec.watch.hook.Store(nil)
		return
	}
	h.exec.watch.hook.Store(&fn)
}

// runWatchTiming 是监视的时间参数（测试里会调小）。
type runWatchTiming struct {
	Poll        time.Duration // Agent 主机的轮询间隔
	PollSlow    time.Duration // SSH 主机的轮询间隔：每次读取都是一次远程命令，慢一点
	MinGap      time.Duration // 两次读取之间的最小间隔：实时帧再密也不会超过
	ReadTimeout time.Duration // 一次读取的时限
	FinalRead   time.Duration // 运行终结后补读尾巴的总时限
	Authority   time.Duration // 运行权威标记的有效期：每轮续期，协程死了最多再挡 hook 这么久
	Tail        time.Duration // 运行结束后 hook 的收尾汇报（sessionEnd 等）还会陆续到：标记再保留一会儿
	MaxAge      time.Duration // 一个监视的最长寿命（运行本身有超时，这里再留余量）；各运行按自己的超时 + Slack 再收短
	Slack       time.Duration // 运行自身超时之外再留的余量：监视寿命 = min(MaxAge, 运行超时 + Slack)
}

// runWatchSlack 是监视寿命在运行自身超时之外留的余量，由两段组成：
//   - 等批准：「需批准」的主机上运行要先等批准（最长 execApprovalTTL），批准之后才开始、才开始计超时，
//     而监视是提交时就开始的——不留这一段，超时很短的运行（比如 1 分钟）会在批准之前就被撤掉监视；
//   - 收尾：运行到点被主机结束后，终态事件与收尾读取还要一会儿（5 分钟）。
const runWatchSlack = execApprovalTTL + 5*time.Minute

func defaultRunWatchTiming() runWatchTiming {
	return runWatchTiming{
		Poll: 2 * time.Second, PollSlow: 5 * time.Second, MinGap: 600 * time.Millisecond,
		ReadTimeout: 10 * time.Second, FinalRead: 8 * time.Second,
		Authority: 45 * time.Second, Tail: 10 * time.Second,
		MaxAge: time.Duration(headlessTimeoutSec)*time.Second + runWatchSlack, Slack: runWatchSlack,
	}
}

// lifetime 是这次运行的监视最长活多久：运行有自己的超时（逐预设可调，#141）就按它加余量，但不超过 MaxAge；
// 没有超时信息（旧记录）就用 MaxAge。运行的终态事件到了监视会自行收尾，这个寿命只是防止协程泄漏的兜底。
func (t runWatchTiming) lifetime(runTimeout time.Duration) time.Duration {
	if runTimeout <= 0 {
		return t.MaxAge
	}
	if d := runTimeout + t.Slack; d < t.MaxAge {
		return d
	}
	return t.MaxAge
}

const (
	runWatchPages     = 8 // 一轮最多读几页，防止一次占太久；剩下的下一轮接着读
	runWatchPageBytes = 64 << 10
	runWatchMaxActive = 64 // 同时监视的运行数上限
)

// runWatchTarget 是被监视运行要记到哪个对话上。
type runWatchTarget struct {
	RunID        string
	Machine      string
	WorkflowID   string
	Conversation string
	// Learn 表示这是新开对话的运行（命令里没有 --resume，Conversation 为空，#176）：对话编号要从输出的 init 事件里学到——
	// 学到之后记成这个工作流的对话编号（之后的运行就是续聊），监视也从此按这个对话判定状态；学到之前读到的信号一律不记。
	Learn bool
	// LearnAfter 是新开对话的运行（Learn）之后的去向（store.HeadlessAfter*，#191）：只决定「新对话已建立」那句系统行怎么说；
	// 空 = 记下编号（旧行为）。每次都新开的预设里，学到的编号照样记成工作流当前的对话，让 Web 显示与状态监视有依据。
	LearnAfter string
	// Peek 表示这是「最近会话」（--continue，#191）的运行：接到哪个对话是 Cursor CLI 的决定，Conversation 为空。
	// 监视只从输出的 init 事件里看一眼它实际接上的对话，写一条系统行说明（运行记录里能看出这次接上了谁），然后收工——
	// **不**把它记成工作流的对话、也不按它判定状态或认领：它可能是编辑器里的别的对话，不能凭它认定「这是这个工作流的对话」。
	Peek bool
	// Timeout 是这次运行自己的超时（0 = 不知道）：监视的寿命据此收短（见 runWatchTiming.lifetime）。
	Timeout time.Duration
	// Restored 表示这是 Hub 重启后接回来的运行：它的输出里已经有一段历史，第一次读完只需要还原「现在的状态」，
	// 不要把历史事件整串重放（那会在事件流水里重复记一遍）。
	Restored bool
}

// runWatch 是一次运行的监视状态。除 wake / end 两个通道外，其余字段只由监视协程自己读写。
type runWatch struct {
	t    runWatchTarget
	wake chan struct{}          // 容量 1：实时输出到达时碰一下
	end  chan model.TerminalRun // 容量 1：运行终结时由 onRunEnded 交来终态记录

	parser  streamParser
	off     uint64
	touched bool // 读到过至少一个信号：运行真的动过这个对话
	stopped bool // 已经产生过 stop 信号（输出里的 result）
	replay  bool // 还在读历史（重启接回的运行）：只记下最后一个信号，读到头再还原
	pending *runSignal
	logged  bool

	claimLogged bool // 认领对话失败只记一次日志（见 claim）
	peeked      bool // 最近会话（Peek）：已经看到实际接上的对话并写了系统行，监视可以收工
}

// runWatcher 管理所有在监视的运行。
type runWatcher struct {
	h      *Hub
	timing runWatchTiming
	hook   atomic.Pointer[func(AgentStateEvent)]

	mu     sync.Mutex
	by     map[string]*runWatch
	closed bool
	wg     sync.WaitGroup
}

func newRunWatcher(h *Hub) *runWatcher {
	return &runWatcher{h: h, timing: defaultRunWatchTiming(), by: make(map[string]*runWatch)}
}

// start 开始监视一次运行。重复登记、缺字段、已达上限、已在关闭都静默忽略（监视是旁路，不能影响运行本身）。
func (r *runWatcher) start(t runWatchTarget) {
	if r == nil || t.RunID == "" || t.Machine == "" || t.WorkflowID == "" || (t.Conversation == "" && !t.Learn && !t.Peek) {
		return
	}
	r.mu.Lock()
	if r.closed || r.by[t.RunID] != nil {
		r.mu.Unlock()
		return
	}
	if len(r.by) >= runWatchMaxActive {
		r.mu.Unlock()
		log.Printf("[terminal] 运行输出监视已达上限 %d，运行 %s 不再监视", runWatchMaxActive, t.RunID)
		return
	}
	rw := &runWatch{t: t, wake: make(chan struct{}, 1), end: make(chan model.TerminalRun, 1), replay: t.Restored}
	r.by[t.RunID] = rw
	r.wg.Add(1)
	r.mu.Unlock()
	go r.loop(rw)
}

// shutdown 等监视协程退出（调用前根上下文已取消）。之后不再接受新的监视。
func (r *runWatcher) shutdown() {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.closed = true
	r.mu.Unlock()
	r.wg.Wait()
}

func (r *runWatcher) forget(rw *runWatch) {
	r.mu.Lock()
	if r.by[rw.t.RunID] == rw {
		delete(r.by, rw.t.RunID)
	}
	r.mu.Unlock()
}

// watching 报告某次运行此刻是否在被监视（测试用）。
func (r *runWatcher) watching(id string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.by[id] != nil
}

// nudge 在实时输出到达时被调用（Agent 连接的读协程里）：只做一次非阻塞的通道发送。
func (r *runWatcher) nudge(id string) {
	if r == nil {
		return
	}
	r.mu.Lock()
	rw := r.by[id]
	r.mu.Unlock()
	if rw == nil {
		return
	}
	select {
	case rw.wake <- struct{}{}:
	default:
	}
}

// ended 在运行终结时被调用（finalize 之后）：把终态记录交给监视协程，由它补读尾巴并给出定论。非阻塞。
func (r *runWatcher) ended(rec model.TerminalRun) {
	if r == nil {
		return
	}
	r.mu.Lock()
	rw := r.by[rec.RunID]
	r.mu.Unlock()
	if rw == nil {
		return
	}
	select {
	case rw.end <- rec:
	default:
	}
}

// ---------- 监视循环 ----------

func (r *runWatcher) loop(rw *runWatch) {
	defer r.wg.Done()
	defer r.forget(rw)
	ctx := r.h.bgCtx
	maxAge := time.NewTimer(r.timing.lifetime(rw.t.Timeout))
	defer maxAge.Stop()

	for {
		// 运行已经终结（登记得比终结晚，或者终结通知没有送到）：直接收尾。
		if rec, ok := r.finalRecord(ctx, rw.t.RunID); ok {
			r.finish(ctx, rw, rec)
			return
		}
		polled := r.h.now()
		r.poll(ctx, rw)
		if rw.peeked {
			return // 最近会话：看到实际接上的对话就够了，不再监视这次运行的输出
		}

		timer := time.NewTimer(r.pollEvery(rw))
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case rec := <-rw.end:
			timer.Stop()
			r.finish(ctx, rw, rec)
			return
		case <-maxAge.C:
			timer.Stop()
			if rw.touched {
				r.authority(rw, r.h.now().Add(r.timing.Tail))
			}
			return
		case <-rw.wake:
			timer.Stop()
			// 实时帧到了：提前读，但两次读取之间至少隔 MinGap。
			if d := r.timing.MinGap - r.h.now().Sub(polled); d > 0 {
				gap := time.NewTimer(d)
				select {
				case <-ctx.Done():
					gap.Stop()
					return
				case rec := <-rw.end:
					gap.Stop()
					r.finish(ctx, rw, rec)
					return
				case <-gap.C:
				}
			}
		case <-timer.C:
		}
	}
}

func (r *runWatcher) pollEvery(rw *runWatch) time.Duration {
	if _, isAgent := r.h.exec.peerFor(rw.t.Machine).(*agentConn); isAgent {
		return r.timing.Poll
	}
	return r.timing.PollSlow
}

// finalRecord 在运行已不在活动索引里、且数据库里是终态时返回终态记录。
func (r *runWatcher) finalRecord(ctx context.Context, id string) (model.TerminalRun, bool) {
	if r.h.exec.liveRun(id) != nil {
		return model.TerminalRun{}, false
	}
	rec, err := r.h.store.GetTerminalRun(ctx, id)
	if err != nil || rec == nil || !model.IsFinalRunState(rec.State) {
		return model.TerminalRun{}, false
	}
	return *rec, true
}

// poll 读一轮新输出并把其中的信号折进状态。读不了（运行还没开始、主机忙、断线）就静默返回，下一轮再试。
// 返回是否读到了数据。
func (r *runWatcher) poll(ctx context.Context, rw *runWatch) bool {
	peer := r.h.exec.peerFor(rw.t.Machine)
	if peer == nil || !peerAlive(peer) || !peer.usable() {
		return false
	}
	got := false
	caughtUp := false
	for i := 0; i < runWatchPages; i++ {
		rctx, cancel := context.WithTimeout(ctx, r.timing.ReadTimeout)
		page, err := peer.readRun(rctx, rw.t.RunID, "stdout", rw.off, runWatchPageBytes)
		cancel()
		if err != nil || page == nil {
			return got
		}
		if page.Gap {
			rw.parser.Resync() // 中间一段被丢掉了：半行作废，重新对齐
		}
		progressed := page.Next > rw.off
		if len(page.Data) > 0 {
			got = true
			r.consume(rw, page.Data)
		}
		if page.Next > rw.off {
			rw.off = page.Next
		}
		if !progressed || page.Next >= page.Total {
			caughtUp = true
			break
		}
	}
	if caughtUp && rw.replay {
		r.endReplay(rw)
	}
	if rw.touched {
		r.authority(rw, r.h.now().Add(r.timing.Authority))
	}
	return got
}

// consume 解析一段输出，把产生的信号折进状态。
func (r *runWatcher) consume(rw *runWatch, data []byte) {
	r.signals(rw, rw.parser.Feed(data))
}

func (r *runWatcher) signals(rw *runWatch, sigs []runSignal) {
	sigs = collapseSignals(sigs)
	if len(sigs) == 0 {
		return
	}
	if rw.t.Peek {
		for _, sig := range sigs {
			if sig.Conv != "" && r.peek(rw, sig.Conv) {
				break
			}
		}
		return // 最近会话不记状态信号（见 runWatchTarget.Peek）
	}
	if rw.t.Conversation == "" {
		// 新开对话的运行：对话编号只能从输出里学。init 是输出的第一行，它之前不会有别的信号；
		// 还没学到就什么都不记（不知道记到哪个对话上），也不算「动过这个对话」。
		for _, sig := range sigs {
			if sig.Conv != "" && r.learn(rw, sig.Conv) {
				break
			}
		}
		if rw.t.Conversation == "" {
			return
		}
	}
	first := !rw.touched
	rw.touched = true
	if first {
		r.authority(rw, r.h.now().Add(r.timing.Authority)) // 从读到第一个信号起，这个对话由运行输出说了算
	}
	now := r.h.now()
	var (
		last    AgentStateEvent
		changed bool
	)
	for i, sig := range sigs {
		if sig.Name == agentstate.EvStop {
			rw.stopped = true
		}
		if rw.replay {
			s := sig
			rw.pending = &s // 历史只留最后一个，读到头再还原
			continue
		}
		// 同一批里的信号错开 1 毫秒，保证先后顺序在状态机里有定论。
		if ev, ok := r.emit(rw, sig, now.Add(time.Duration(i)*time.Millisecond)); ok {
			last, changed = ev, true
		}
	}
	// 一批信号只通知一次（带批末的状态）：Web 收到通知会重新取一遍当前状态，中间态没有人看得见。
	if changed {
		r.notify(last)
	}
}

// endReplay 在重启接回的运行读完历史后，把最后一个信号作为「现在的状态」记下。
func (r *runWatcher) endReplay(rw *runWatch) {
	rw.replay = false
	if rw.pending == nil {
		return
	}
	sig := *rw.pending
	rw.pending = nil
	if ev, ok := r.emit(rw, sig, r.h.now()); ok {
		r.notify(ev)
	}
}

// emit 把一个信号作为运行输出来源的事件记到对话的会话上。状态变了（或出现了新会话）返回要通知 Web 的事件。
func (r *runWatcher) emit(rw *runWatch, sig runSignal, at time.Time) (AgentStateEvent, bool) {
	res, err := r.h.store.IngestAgentEvent(r.h.bgCtx, store.AgentIngestInput{
		WorkflowID: rw.t.WorkflowID, ConversationID: rw.t.Conversation, Conversation: true,
		Event: agentstate.Event{
			Source: agentstate.SourceRun, Name: sig.Name, Tool: sig.Tool, Status: sig.Status, Received: at,
		},
	})
	if err != nil && !rw.logged {
		rw.logged = true
		log.Printf("[terminal] 运行 %s 的状态事件写库失败（内存里的状态照常推进）：%v", rw.t.RunID, err)
	}
	if res.IdentityID == 0 || !(res.Change.StateChanged || res.NewSession) {
		return AgentStateEvent{}, false
	}
	return AgentStateEvent{
		WorkflowID: rw.t.WorkflowID, IdentityID: res.IdentityID, SessionID: res.SessionID,
		State: string(res.Snapshot.State), Sub: res.Snapshot.Sub, Source: string(res.Snapshot.Source), Since: res.Snapshot.Since,
	}, true
}

// notify 把状态变化交给接入的挂钩（没有接线就什么都不做）。
func (r *runWatcher) notify(ev AgentStateEvent) {
	p := r.hook.Load()
	if p == nil {
		return
	}
	defer func() {
		if rec := recover(); rec != nil {
			log.Printf("[terminal] 状态变化通知挂钩 panic：%v", rec)
		}
	}()
	(*p)(ev)
}

// learn 在新开对话的运行输出里学到对话编号（#176）：记成这个工作流的对话编号——之后会话页「运行」与预设运行
// 都能接着它，所以新开只会发生一次——并让监视从此按这个对话判定状态。
// 编号格式不对（不是 Cursor 的会话编号）就不学，返回 false，下一个带编号的信号再试。
func (r *runWatcher) learn(rw *runWatch, conv string) bool {
	ev, ok := store.NormalizeAgentEvent(store.AgentEvent{ConversationID: conv})
	if !ok || ev.ConversationID == "" {
		return false
	}
	rw.t.Conversation = ev.ConversationID
	now := r.h.now()
	r.h.store.RecordAgentEvent(r.h.bgCtx, rw.t.WorkflowID, store.AgentEvent{ConversationID: ev.ConversationID}, now)
	if !rw.replay { // 重启接回的运行读的是历史：这句话当时已经记过了
		meta, _ := json.Marshal(map[string]any{"mode": "launcher_conversation", "run_id": rw.t.RunID})
		r.h.guardian.addWakeLine(r.h.bgCtx, rw.t.WorkflowID, "system", launcherLearnActor,
			learnLineText(ev.ConversationID, rw.t.LearnAfter), string(meta), now)
	}
	return true
}

// learnLineText 是「新对话已建立」的系统行：after 是「新会话」运行之后的去向（空 = 记下编号）。
func learnLineText(conv, after string) string {
	head := "新对话已建立（对话 " + shortConv(conv) + "）："
	switch after {
	case store.HeadlessAfterStay:
		return head + "这个预设每次运行都会新开对话，不会接着这一个"
	case store.HeadlessAfterContinue:
		return head + "这次运行成功之后，预设会自动切换成「最近会话」"
	}
	return head + "之后的运行都会接着它，不会再新开"
}

// peek 在「最近会话」（--continue）的运行输出里看一眼它实际接上的对话（#191）：写一条系统行说明，仅此而已——
// 不记成工作流的对话、不判定状态、不认领（见 runWatchTarget.Peek）。编号格式不对就不认，返回 false，下一个带编号的信号再试。
func (r *runWatcher) peek(rw *runWatch, conv string) bool {
	ev, ok := store.NormalizeAgentEvent(store.AgentEvent{ConversationID: conv})
	if !ok || ev.ConversationID == "" {
		return false
	}
	rw.peeked = true
	if rw.replay { // 重启接回的运行读的是历史：这句话当时已经记过了
		return true
	}
	meta, _ := json.Marshal(map[string]any{"mode": "launcher_continue", "run_id": rw.t.RunID, "conversation": ev.ConversationID})
	r.h.guardian.addWakeLine(r.h.bgCtx, rw.t.WorkflowID, "system", launcherLearnActor,
		"这次运行接上了对话 "+shortConv(ev.ConversationID)+"（最近会话：由 Cursor CLI 决定，不一定是这个工作流的）", string(meta), r.h.now())
	return true
}

// authority 标记（或续期）这个对话由运行输出说了算，直到 until；同时认领这个对话（见 claim）。
func (r *runWatcher) authority(rw *runWatch, until time.Time) {
	r.h.store.SetAgentRunAuthority(rw.t.WorkflowID, rw.t.Conversation, until)
	r.claim(rw, until)
}

// claim 认领这次运行跑的对话（#182）：运行是 Hub 替 rw.t.WorkflowID 发起的，对话归它——
// 而对话里 hook 汇报带的工作流来自项目目录里的 .cursor/relaymesh.json，同一个仓库的多个工作树共用一份配置时，
// 那里写的常常是另一个工作流：状态会话会被建到别的工作流名下，之后的 hook 事件又被运行权威挡掉、没人把它纠正回来
// （这正是「启动预设首次运行后没有绑定到会话」的现象）。
//
// 认领与运行权威同生共死（同一个 until，每轮续期）：只有真的动过这个对话的运行才认领，启动就失败的运行不碰它。
// 认领会把已经建在别处的会话与旧链路搬回来；有会话挪动时通知两边的网页重新取一遍（没有 session_id 的事件 = 整体刷新）。
func (r *runWatcher) claim(rw *runWatch, until time.Time) {
	wf, conv := rw.t.WorkflowID, rw.t.Conversation
	if wf == "" || conv == "" {
		return
	}
	from, err := r.h.store.ClaimConversation(r.h.bgCtx, wf, conv, until, r.h.now())
	if err != nil && !rw.claimLogged {
		rw.claimLogged = true
		log.Printf("[terminal] 运行 %s 认领对话 %s 时搬动已有会话失败（认领照常生效，之后每个状态事件会再试着纠正）：%v", rw.t.RunID, shortConv(conv), err)
	}
	if len(from) == 0 {
		return
	}
	r.notify(AgentStateEvent{WorkflowID: wf})
	for _, other := range from {
		r.notify(AgentStateEvent{WorkflowID: other})
	}
}

// ---------- 终结 ----------

// finish 在运行终结后收尾：补读还没读到的输出，再按终态给出定论。
// 它在监视协程里运行——不能在连接的读协程里读输出（读取要等读协程转交数据帧，会互等）。
func (r *runWatcher) finish(ctx context.Context, rw *runWatch, rec model.TerminalRun) {
	r.drain(ctx, rw)
	if rw.replay {
		r.endReplay(rw)
	}
	r.signals(rw, rw.parser.Flush())
	if !rw.touched {
		return // 运行没有动过这个对话（启动就失败了）：不改状态，也不留权威标记
	}
	now := r.h.now()
	if !rw.stopped {
		// 输出里没有 result（被杀、超时、崩了、主机失联）：按终态补一个 stop。
		if ev, ok := r.emit(rw, runSignal{Name: agentstate.EvStop, Status: stopStatusOf(rec.State)}, now); ok {
			r.notify(ev)
		}
		rw.stopped = true
	}
	// hook 的收尾汇报（sessionEnd 等）随后才到：标记再保留一会儿，别让它们把「本次运行结束」改成 ended。
	r.authority(rw, now.Add(r.timing.Tail))
}

// drain 把终结前来不及读的输出读完（有总时限；主机不可达就算了）。
func (r *runWatcher) drain(ctx context.Context, rw *runWatch) {
	dctx, cancel := context.WithTimeout(ctx, r.timing.FinalRead)
	defer cancel()
	for i := 0; i < runWatchPages*4 && dctx.Err() == nil; i++ {
		before := rw.off
		r.poll(dctx, rw)
		if rw.off == before {
			return
		}
	}
}

// stopStatusOf 把运行终态映射成状态机 stop 事件的结束状态。
func stopStatusOf(state string) string {
	switch state {
	case model.RunSucceeded:
		return agentstate.StopCompleted
	case model.RunCancelled:
		return agentstate.StopAborted
	}
	return agentstate.StopError // 失败、超时、失联……
}

// ---------- Hub 重启后接回运行 ----------

var resumeArgRe = regexp.MustCompile(`--resume[ =]'?([A-Za-z0-9_-]{1,64})'?`)

// continueArgRe 认命令里的 `--continue` 参数（最近会话，#191）：整词匹配，提示词里（单引号包着、前面不是空白）的同样字样不会误中——
// 提示词是单个单引号包裹的词，里面的 --continue 前面是别的字符或空格；这里要求它是命令里独立的一个词，所以只在提示词恰好以 " --continue " 出现时才会误判，
// 而提示词里的空白已被折叠成单个空格，这种误判理论上存在，代价只是多一次「peek」监视（不改任何状态）。
var continueArgRe = regexp.MustCompile(`(?:^|\s)--continue(?:\s|$)`)

// isContinueCommand 判断一条非交互续聊命令是不是「最近会话」（--continue）。
func isContinueCommand(cmd string) bool { return continueArgRe.MatchString(cmd) }

// convFromRunCommand 从非交互续聊命令里取出被续聊的对话编号（命令是脱敏副本，--resume 参数不含密钥，会保留）。
func convFromRunCommand(cmd string) string {
	m := resumeArgRe.FindStringSubmatch(cmd)
	if m == nil {
		return ""
	}
	return strings.TrimSpace(m[1])
}

// convFromRunOutput 从一次运行输出的开头预览里取出它实际接上的对话编号（stream-json 的 init 事件里的 session_id，#191）；认不出返回空。
// 开头预览是已脱敏的副本，对话编号不受影响；预览被截断时 init 是第一行，不会丢。
func convFromRunOutput(head string) string {
	if strings.TrimSpace(head) == "" {
		return ""
	}
	var p streamParser
	sigs := append(p.Feed([]byte(head)), p.Flush()...)
	for _, sig := range sigs {
		if sig.Conv == "" {
			continue
		}
		if ev, ok := store.NormalizeAgentEvent(store.AgentEvent{ConversationID: sig.Conv}); ok && ev.ConversationID != "" {
			return ev.ConversationID
		}
	}
	return ""
}

// convOfRun 是一次运行实际接着的对话编号：命令里有 --resume 就是它；新会话 / 最近会话（命令里没有编号）从输出里的 init 事件取；认不出返回空。
func convOfRun(rec model.TerminalRun) string {
	if c := convFromRunCommand(rec.Command); c != "" {
		return c
	}
	return convFromRunOutput(rec.PreviewHead)
}

// restore 在 Hub 重启、恢复未终结的运行记录后，为其中系统发起的非交互运行重新接上监视。
func (r *runWatcher) restore(rec model.TerminalRun) {
	switch rec.CredentialName {
	case headlessCallerName, launcherRunCaller, sessionRunCaller:
	default:
		return
	}
	conv := convFromRunCommand(rec.Command)
	cont := conv == "" && isContinueCommand(rec.Command)
	r.start(runWatchTarget{
		RunID: rec.RunID, Machine: rec.Target, WorkflowID: strings.TrimSpace(rec.WorkflowID),
		// 命令里有 --continue 的是最近会话的运行（#191）：只看一眼实际接上的对话；
		// 都没有 --resume 与 --continue 的是新开对话的运行（#176）：对话编号重新从输出的 init 事件里读（历史输出整段重放）。
		Conversation: conv, Learn: conv == "" && !cont, Peek: cont, Restored: true,
		Timeout: time.Duration(rec.TimeoutSec) * time.Second,
	})
}
