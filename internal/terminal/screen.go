package terminal

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/teacat99/RelayMesh/v2/internal/agentstate"
	"github.com/teacat99/RelayMesh/v2/internal/store"
	"github.com/teacat99/RelayMesh/v2/internal/termproto"
	"github.com/teacat99/RelayMesh/v2/internal/termstate"
)

// 托管终端屏幕信号的 Hub 侧消费者（#113 M3.2，设计稿 04 §2.6）。
//
// Agent 守护进程把 PTY 输出交给 termstate.Scanner，识别出 cursor-agent TUI 的状态行 / 审批提示 / 页脚后，
// 把结果放在会话清单的 SessionInfo.Screen 里推给 Hub（只在识别结果变化或动画中每 10 秒一次）。这里负责：
//
//  1. 按启动器预设把终端会话对应到工作流（预设的 Guard.WorkflowID）；
//  2. 过版本闸门（cursor 版本不在已验证清单里就只展示「未识别」，不产生任何审批 / 卡住判断）；
//  3. 把信号存进 store 的屏幕信号表（状态卡、卡住判断读它），审批等待的开始与结束折成 approval_wait / approval_done
//     状态机事件，并通过 ScreenHook 通知上层发 approval_waiting 通知与 SSE。
//
// 它只读不写终端：绝不往终端里输入任何东西。

// ScreenEvent 是托管终端屏幕上出现的值得上层关心的变化（目前只有审批等待的开始 / 结束）。
type ScreenEvent struct {
	PresetID   string
	Name       string // 预设名
	Machine    string // 主机码
	WorkflowID string
	// Approval 为 true 表示刚进入审批等待，false 表示审批等待结束（批准、跳过或中断）。
	Approval bool
	// Episode 标识同一次审批等待（预设 + 会话 + 开始时间），用来给通知去重。
	Episode string
	// Reminder 为 0 表示首次；大于 0 表示同一次审批等待没人处理，第几次提醒（通知 ID 带上它，闸才不会当作重复丢掉）。
	Reminder int

	// 状态机记录结果：IdentityID 为 0 表示没有可归属的 Agent 会话（还没有 hook 事件），此时只有通知，没有状态变化。
	IdentityID uint
	SessionID  uint
	State      string
	Sub        string
	Source     string // 现在这个状态是谁说的（hook / tui / run）
	Since      time.Time
}

type screenTrack struct {
	workflow   string
	presetID   string
	name       string
	approval   bool
	episode    string
	approvalAt time.Time
	reminders  int
}

type screenService struct {
	h    *Hub
	hook atomic.Pointer[func(ScreenEvent)]

	mu        sync.Mutex
	tracked   map[string]*screenTrack // 主机码 + "|" + sid
	launchers []store.TerminalLauncher
	loadedAt  time.Time
}

// screenLauncherTTL 是启动器预设列表的缓存时间：预设很少改，而会话清单可能每 10 秒来一次。
const screenLauncherTTL = 30 * time.Second

func newScreenService(h *Hub) *screenService {
	return &screenService{h: h, tracked: map[string]*screenTrack{}}
}

// SetScreenHook 接入屏幕事件通知；启动时调用一次，传 nil 表示摘掉。
// fn 在清单处理协程里被同步调用，必须立即返回，不能做网络请求。
func (h *Hub) SetScreenHook(fn func(ScreenEvent)) {
	if h == nil || h.screens == nil {
		return
	}
	if fn == nil {
		h.screens.hook.Store(nil)
		return
	}
	h.screens.hook.Store(&fn)
}

func (s *screenService) fire(ev ScreenEvent) {
	p := s.hook.Load()
	if p == nil {
		return
	}
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[terminal] 屏幕事件挂钩 panic：%v", r)
		}
	}()
	(*p)(ev)
}

// presetFor 找一个会话对应的启动器预设：同一主机、同名（与会话守护的认法一致），并且配置了工作流。
func (s *screenService) presetFor(ctx context.Context, host, sessionName string, now time.Time) *store.TerminalLauncher {
	s.mu.Lock()
	if now.Sub(s.loadedAt) >= screenLauncherTTL || s.launchers == nil {
		list, err := s.h.store.ListTerminalLaunchers(ctx)
		if err != nil {
			s.mu.Unlock()
			return nil
		}
		s.launchers, s.loadedAt = list, now
	}
	list := s.launchers
	s.mu.Unlock()
	key := guardNameKey(sessionName)
	if key == "" {
		return nil
	}
	for i := range list {
		p := &list[i]
		if p.Host != host || p.Guard == nil || strings.TrimSpace(p.Guard.WorkflowID) == "" {
			continue
		}
		if guardNameKey(p.Name) == key {
			return p
		}
	}
	return nil
}

// observe 处理一台主机最新的会话清单。a 是该主机的连接，list 是清洗过的清单。
func (s *screenService) observe(a *agentConn, list []termproto.SessionInfo) {
	if s == nil || a == nil || a.temp || s.h == nil || s.h.store == nil {
		return
	}
	ctx := s.h.bgCtx
	if ctx == nil {
		ctx = context.Background()
	}
	now := s.h.now()
	host := a.code
	seen := map[string]bool{}

	for i := range list {
		info := &list[i]
		if info.State != termproto.SessionRunning || info.Screen == nil {
			continue
		}
		p := s.presetFor(ctx, host, info.Name, now)
		if p == nil {
			continue
		}
		wf := strings.TrimSpace(p.Guard.WorkflowID)
		key := host + "|" + info.SID
		seen[key] = true

		version := s.h.store.AgentCursorVersion(ctx, wf)
		sc := info.Screen
		ch := s.h.store.SetAgentScreen(wf, store.AgentScreenInput{
			Verified: termstate.TUIVersionVerified(version), Recognized: true, CursorVersion: version,
			Word: sc.Word, Tokens: sc.Tokens, CtxPct: sc.CtxPct,
			Animating: sc.Animating, Approval: sc.Approval, Cancelled: sc.Cancelled, QuietSeconds: sc.Quiet,
		}, now)

		s.mu.Lock()
		tr := s.tracked[key]
		if tr == nil {
			tr = &screenTrack{workflow: wf, presetID: p.ID, name: p.Name}
			s.tracked[key] = tr
		}
		tr.workflow, tr.presetID, tr.name = wf, p.ID, p.Name
		if ch.ApprovalStarted {
			tr.approval, tr.approvalAt, tr.reminders = true, now, 0
			tr.episode = fmt.Sprintf("%s:%s:%d", p.ID, info.SID[:8], now.Unix())
		}
		episode := tr.episode
		if ch.ApprovalEnded {
			tr.approval = false
		}
		s.mu.Unlock()

		switch {
		case ch.ApprovalStarted:
			s.emit(ctx, ScreenEvent{PresetID: p.ID, Name: p.Name, Machine: host, WorkflowID: wf, Approval: true, Episode: episode}, agentstate.EvApprovalWait, now)
		case ch.ApprovalEnded:
			s.emit(ctx, ScreenEvent{PresetID: p.ID, Name: p.Name, Machine: host, WorkflowID: wf, Approval: false, Episode: episode}, agentstate.EvApprovalDone, now)
		}
	}

	// 这台主机上之前跟踪过、这次清单里没有（会话退出了、守护进程不再报屏幕、预设被删）的：清掉信号；
	// 若当时正在等审批，要补一个「结束」，否则状态会永远停在 waiting_approval。
	prefix := host + "|"
	var gone []*screenTrack
	s.mu.Lock()
	for key, tr := range s.tracked {
		if strings.HasPrefix(key, prefix) && !seen[key] {
			gone = append(gone, tr)
			delete(s.tracked, key)
		}
	}
	s.mu.Unlock()
	for _, tr := range gone {
		if s.h.store.ClearAgentScreen(tr.workflow) && tr.approval {
			s.emit(ctx, ScreenEvent{PresetID: tr.presetID, Name: tr.name, Machine: host, WorkflowID: tr.workflow, Approval: false, Episode: tr.episode}, agentstate.EvApprovalDone, now)
		}
	}
}

// emit 把审批等待的开始 / 结束折进状态机（尽力而为：没有可归属的会话就只通知），再交给上层挂钩。
func (s *screenService) emit(ctx context.Context, ev ScreenEvent, name string, now time.Time) {
	res, err := s.h.store.IngestAgentEvent(ctx, store.AgentIngestInput{
		WorkflowID: ev.WorkflowID, Latest: true,
		Event: agentstate.Event{Source: agentstate.SourceTUI, Name: name, Received: now},
	})
	if err != nil {
		log.Printf("[terminal] 记录屏幕事件失败：%v", err)
	}
	if res.IdentityID != 0 {
		ev.IdentityID, ev.SessionID = res.IdentityID, res.SessionID
		ev.State, ev.Sub, ev.Since = string(res.Snapshot.State), res.Snapshot.Sub, res.Snapshot.Since
		ev.Source = string(res.Snapshot.Source)
	}
	s.fire(ev)
}

// 审批等待没人处理时的提醒：等了 screenRemindAfter[0] 后第一次，之后按 screenRemindAfter[1] 再一次，到此为止。
var screenRemindAfter = []time.Duration{5 * time.Minute, 30 * time.Minute}

const screenRemindEvery = 30 * time.Second

// run 周期性检查「还在等批准」的会话并补发提醒。审批画面本身没有输出，守护进程不会再推送，所以必须由 Hub 自己计时。
func (s *screenService) run(ctx context.Context) {
	t := time.NewTicker(screenRemindEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.remind(s.h.now())
		}
	}
}

// remind 对每个仍在审批等待、且到了提醒时间的会话发一条提醒事件。
func (s *screenService) remind(now time.Time) {
	var due []ScreenEvent
	s.mu.Lock()
	for key, tr := range s.tracked {
		if !tr.approval || tr.reminders >= len(screenRemindAfter) {
			continue
		}
		if now.Sub(tr.approvalAt) < screenRemindAfter[tr.reminders] {
			continue
		}
		tr.reminders++
		host := key[:strings.IndexByte(key, '|')]
		due = append(due, ScreenEvent{
			PresetID: tr.presetID, Name: tr.name, Machine: host, WorkflowID: tr.workflow,
			Approval: true, Episode: tr.episode, Reminder: tr.reminders,
		})
	}
	s.mu.Unlock()
	for _, ev := range due {
		s.fire(ev)
	}
}
