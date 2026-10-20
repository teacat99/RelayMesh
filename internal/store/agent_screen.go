package store

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/teacat99/RelayMesh/v2/internal/agentstate"
	"github.com/teacat99/RelayMesh/v2/internal/model"
)

// 托管终端的屏幕信号（#113 M3.2）：Hub 从 Agent 守护进程上报的会话清单里取出屏幕扫描结果，按工作流存在内存里，
// 展示（状态卡的 token / 上下文 %）和状态判断（审批、卡住）都读这一份。它是易失的：重启后清零，下一次上报就恢复，
// 所以不落库；落库的只有 approval_wait / approval_done 这类状态机事件（走 IngestAgentEvent）。

const (
	// agentScreenTTL：上报超过这么久没有更新就不再采信。托管终端空闲时守护进程不会重复推送，所以这里给得很宽；
	// 动画中的会话每 10 秒刷新一次，超过 agentScreenAnimStale 没刷新说明守护进程断了，也不能再当作「正在动画」。
	agentScreenTTL       = 30 * time.Minute
	agentScreenAnimStale = 90 * time.Second
	// agentScreenMax：内存里最多保留多少个工作流的屏幕信号，防止异常膨胀。
	agentScreenMax = 512
)

// AgentScreenInput 是一次屏幕上报（已按版本闸门判断过 Verified）。
type AgentScreenInput struct {
	Verified      bool   // cursor 版本在已验证清单里；false 时只展示「未识别」，不参与任何判断
	Recognized    bool   // 守护进程认出了 TUI（Screen 非空）
	CursorVersion string // 判断 Verified 所用的版本，仅展示
	Word          string
	Tokens        int     // -1 = 没读到
	CtxPct        float64 // -1 = 没读到
	Animating     bool
	Approval      bool
	Cancelled     bool
	QuietSeconds  int // 上报时距最后一次输出的秒数；-1 = 不知道
}

type agentScreenEntry struct {
	in          AgentScreenInput
	at          time.Time
	tokensSince time.Time
	tokens      int
}

type agentScreenMem struct {
	mu sync.Mutex
	m  map[string]*agentScreenEntry
}

// AgentScreenChange 描述一次上报带来的变化，供调用方决定要不要产生状态事件与通知。
type AgentScreenChange struct {
	ApprovalStarted bool // 刚进入审批等待
	ApprovalEnded   bool // 审批等待刚结束（批准、跳过或中断）
	Approval        bool // 当前是否在审批等待
}

// SetAgentScreen 登记一个工作流的最新屏幕信号。
func (s *Store) SetAgentScreen(workflowID string, in AgentScreenInput, now time.Time) AgentScreenChange {
	wf := strings.TrimSpace(workflowID)
	var ch AgentScreenChange
	if wf == "" {
		return ch
	}
	if !in.Verified {
		// 版本不在清单里：屏幕写法没有验证过，不采信审批 / 计数，只留一条「未识别」供展示。
		in.Approval, in.Animating, in.Cancelled, in.Word, in.Tokens, in.CtxPct = false, false, false, "", -1, -1
	}
	m := &s.screens
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.m == nil {
		m.m = make(map[string]*agentScreenEntry)
	}
	prev := m.m[wf]
	e := &agentScreenEntry{in: in, at: now, tokensSince: now, tokens: in.Tokens}
	wasApproval := false
	if prev != nil {
		wasApproval = prev.in.Approval && now.Sub(prev.at) < agentScreenTTL
		if prev.tokens == in.Tokens {
			e.tokensSince = prev.tokensSince
		}
	}
	if len(m.m) >= agentScreenMax && prev == nil {
		for k, v := range m.m { // 满了：丢掉最旧的一个
			if now.Sub(v.at) > agentScreenTTL {
				delete(m.m, k)
			}
		}
		if len(m.m) >= agentScreenMax {
			return ch
		}
	}
	m.m[wf] = e
	ch.Approval = in.Approval
	ch.ApprovalStarted = in.Approval && !wasApproval
	ch.ApprovalEnded = !in.Approval && wasApproval
	return ch
}

// ClearAgentScreen 在托管终端消失（会话退出、主机离线）时清掉信号。返回之前是否在审批等待。
func (s *Store) ClearAgentScreen(workflowID string) (wasApproval bool) {
	wf := strings.TrimSpace(workflowID)
	m := &s.screens
	m.mu.Lock()
	defer m.mu.Unlock()
	if e := m.m[wf]; e != nil {
		wasApproval = e.in.Approval
		delete(m.m, wf)
	}
	return wasApproval
}

// AgentScreenView 是对外展示的屏幕信号。
type AgentScreenView struct {
	TUI        string  // "ok"（版本已验证）| "unrecognized"（版本未验证或画面认不出）
	Word       string  // 状态行动词，仅在动画中有值
	Tokens     int     // -1 = 没读到
	TokensSrc  string  // 有 Tokens 时为 "tui"
	CtxPct     float64 // -1 = 没读到
	Approval   bool
	Animating  bool
	ReceivedAt time.Time
	signal     agentstate.ScreenSignal // 给 Evaluate 用；TUI 不是 ok 时为零值（Verified=false）
}

// agentScreenOf 取一个工作流现在可用的屏幕信号；没有或已过期返回 nil。
func (s *Store) agentScreenOf(workflowID string, now time.Time) *AgentScreenView {
	m := &s.screens
	m.mu.Lock()
	e := m.m[strings.TrimSpace(workflowID)]
	var cp agentScreenEntry
	if e != nil {
		cp = *e
	}
	m.mu.Unlock()
	if e == nil || now.Sub(cp.at) >= agentScreenTTL {
		return nil
	}
	v := &AgentScreenView{Tokens: -1, CtxPct: -1, ReceivedAt: cp.at}
	if !cp.in.Verified || !cp.in.Recognized {
		v.TUI = "unrecognized"
		return v
	}
	v.TUI = "ok"
	age := now.Sub(cp.at)
	animating := cp.in.Animating && age < agentScreenAnimStale
	quiet := time.Duration(-1)
	if cp.in.QuietSeconds >= 0 {
		quiet = time.Duration(cp.in.QuietSeconds)*time.Second + age
	}
	v.Approval, v.Animating = cp.in.Approval, animating
	if animating {
		v.Word = cp.in.Word
	}
	if cp.in.Tokens >= 0 {
		v.Tokens, v.TokensSrc = cp.in.Tokens, "tui"
	}
	v.CtxPct = cp.in.CtxPct
	v.signal = agentstate.ScreenSignal{
		Verified: true, Animating: animating, Approval: cp.in.Approval,
		Tokens: cp.in.Tokens, TokensSince: cp.tokensSince, QuietFor: quiet, CtxPct: cp.in.CtxPct,
	}
	return v
}

// AgentCursorVersion 返回工作流最近一个 Agent 会话报告的 cursor 版本（屏幕扫描器的版本闸门要用）；不知道返回空串。
// 先看内存（hook 事件刚推进过的会话），再退到库里最近一行有版本的会话。
func (s *Store) AgentCursorVersion(ctx context.Context, workflowID string) string {
	wf := strings.TrimSpace(workflowID)
	if wf == "" {
		return ""
	}
	m := &s.agent
	m.mu.Lock()
	var (
		best    time.Time
		version string
	)
	for _, e := range m.sessions {
		if e.workflow == wf && e.version != "" && (version == "" || e.snap.LastEventAt.After(best)) {
			best, version = e.snap.LastEventAt, e.version
		}
	}
	m.mu.Unlock()
	if version != "" {
		return version
	}
	var row model.AgentSession
	err := s.db.WithContext(ctx).Select("cursor_version").
		Where("workflow_id = ? AND cursor_version <> ''", wf).
		Order("last_event_at DESC, id DESC").Limit(1).Take(&row).Error
	if err != nil {
		return ""
	}
	return row.CursorVersion
}
