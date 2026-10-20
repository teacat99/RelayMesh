package agentstate

import (
	"strings"
	"time"
)

// Thresholds 是「离线」与「卡住」判定的阈值（设计稿 04 §2.4，纯 hook 来源的那一列）。
// 核心结论（实验 §8）：token 不变本身不是证据——工具运行与审批等待期间计数器恒定；
// 所以这里只用「事件静默多久」与「工具跑了多久」，托管终端的屏幕信号第 3 阶段再叠加。
type Thresholds struct {
	Offline        time.Duration // 没有任何信号这么久 → offline（已结束的不算）
	ThinkSoft      time.Duration // 思考 / 回复中静默这么久 → 软标记「可能卡住」（不推送）
	ThinkStalled   time.Duration // 静默这么久 → stalled（沿用会话守护的 30 分钟）
	ToolQuiet      time.Duration // 工具跑超过这个时间 → 标「工具执行中 / 可能在等批准」（不报警）
	ToolMax        time.Duration // 一般工具的上限，超过 → stalled
	LongToolMax    time.Duration // 长工具（shell、子代理）的上限
	LongTools      []string      // 长工具的名字（小写，按前缀匹配）
	ApprovalRemind time.Duration // 等批准超过这么久 → 提醒

	// 托管终端（屏幕信号可信）时的两条规则，设计稿 04 §2.4：
	TUITokenFlat time.Duration // 思考 / 回复中，token 计数不变且屏幕仍在动画这么久 → 软标记
	TUIFrozen    time.Duration // 思考 / 回复中，屏幕没有任何字节（且不是审批）这么久 → stalled（终端冻结）
}

// DefaultThresholds 返回设计稿的默认值。
func DefaultThresholds() Thresholds {
	return Thresholds{
		Offline:        2 * time.Hour,
		ThinkSoft:      10 * time.Minute,
		ThinkStalled:   30 * time.Minute,
		ToolQuiet:      20 * time.Second,
		ToolMax:        10 * time.Minute,
		LongToolMax:    60 * time.Minute,
		LongTools:      []string{"shell", "awaitshell", "task", "bash", "terminal"},
		ApprovalRemind: 5 * time.Minute,
		TUITokenFlat:   45 * time.Second,
		TUIFrozen:      3 * time.Minute,
	}
}

// 卡住 / 异常的原因代码（前端按代码配文案）。
const (
	ReasonQuietSoft       = "quiet_soft"       // 思考 / 回复中静默超过软阈值
	ReasonQuiet           = "quiet"            // 静默超过硬阈值
	ReasonToolRunning     = "tool_running"     // 工具执行了一阵子：可能在等批准（不报警）
	ReasonToolOverrun     = "tool_overrun"     // 工具超过类型上限
	ReasonApprovalOverdue = "approval_overdue" // 等批准太久
	ReasonTokensFlat      = "tokens_flat"      // 托管终端：token 计数停了但屏幕还在动（软标记）
	ReasonTerminalFrozen  = "terminal_frozen"  // 托管终端：屏幕没有任何输出（硬卡住）
)

// ScreenSignal 是托管终端屏幕扫描器给状态判断的输入（M3.2）。只有 Verified（cursor 版本在已验证清单里）时才会被采信；
// 未验证时调用方应传 nil 或 Verified=false，判断退回纯 hook 规则，不产生任何屏幕相关的告警。
type ScreenSignal struct {
	Verified    bool
	Animating   bool          // 最近 2 秒仍有输出字节
	Approval    bool          // 屏幕停在 Waiting for approval
	Tokens      int           // 状态行里的实时 token 数；-1 = 没读到
	TokensSince time.Time     // 计数最近一次变化的时间（Hub 记录，零值 = 不知道）
	QuietFor    time.Duration // 屏幕距最后一次输出多久；<0 = 不知道
	CtxPct      float64       // 上下文占用百分比；<0 = 没读到
}

// EvalInput 是 Evaluate 的输入。
type EvalInput struct {
	Snap            Snapshot
	Now             time.Time
	LastMCPAt       time.Time     // MCP 侧最近一次活动（零值 = 不知道）
	WaitingYou      bool          // Agent 正阻塞等人回答（调用方从待答复的汇报推导）
	HasLiveTerminal bool          // 有活的托管终端（第 3 阶段；有的话不判 offline）
	Screen          *ScreenSignal // 托管终端的屏幕信号；nil = 没有托管终端或没有可用信号
}

// View 是展示用的状态：基础状态叠加了 waiting_you / offline，以及卡住标志。
type View struct {
	State    State  // 展示态：working / idle / waiting_approval / aborted / error / ended / waiting_you / offline；没见过的会话为空
	Base     State  // 快照里的状态
	Sub      string // 子状态（working 时才有）
	Since    time.Time
	QuietFor time.Duration // 距最近一次信号（hook 事件或 MCP 调用）过了多久

	WaitingYou        bool
	Stalled           bool // 硬卡住（可以告警）
	StalledSoft       bool // 软标记：可能卡住（只显示，不推送）
	ApprovalSuspected bool // 疑似在等批准（纯 hook 只能怀疑）
	ApprovalOverdue   bool
	Reason            string // Reason* 之一；没有异常为空
}

func latest(a, b time.Time) time.Time {
	if b.After(a) {
		return b
	}
	return a
}

func (th Thresholds) isLongTool(name string) bool {
	n := strings.ToLower(strings.TrimSpace(name))
	if n == "" {
		return false
	}
	for _, p := range th.LongTools {
		if strings.HasPrefix(n, p) {
			return true
		}
	}
	return false
}

// Evaluate 推导展示状态。纯函数，所以「现在」由调用方给。
func Evaluate(in EvalInput, th Thresholds) View {
	s := in.Snap
	v := View{State: s.State, Base: s.State, Sub: s.Sub, Since: s.Since}
	if s.State == "" {
		return v
	}
	last := latest(s.LastEventAt, in.LastMCPAt)
	if last.IsZero() {
		last = s.Since
	}
	quiet := in.Now.Sub(last)
	if quiet < 0 {
		quiet = 0
	}
	v.QuietFor = quiet

	if s.State == StateEnded { // 已结束的会话不判卡住，也不算离线
		return v
	}
	// 阻塞等人回答：Agent 没卡住，是人在拖；但太久没有信号（Agent 都不再轮询了）就不再冒充「在等」。
	if in.WaitingYou && quiet < th.Offline {
		switch s.State {
		case StateWorking, StateIdle, StateWaitingApproval:
			v.State, v.WaitingYou = StateWaitingYou, true
			return v
		}
	}
	if quiet >= th.Offline && !in.HasLiveTerminal {
		v.State = StateOffline
		return v
	}

	screen := in.Screen
	if screen != nil && !screen.Verified {
		screen = nil
	}
	switch s.State {
	case StateWorking:
		if strings.HasPrefix(s.Sub, SubToolPrefix) {
			start := s.ToolStartedAt
			if start.IsZero() {
				start = s.Since
			}
			ran := in.Now.Sub(start)
			limit := th.ToolMax
			if th.isLongTool(strings.TrimPrefix(s.Sub, SubToolPrefix)) {
				limit = th.LongToolMax
			}
			switch {
			case ran > limit:
				v.Stalled, v.Reason = true, ReasonToolOverrun
			case ran > th.ToolQuiet && screen == nil && s.Source != SourceRun:
				// 有可信屏幕信号时「是不是在等批准」是确定的（approval_wait 事件），不再靠猜。
				// 非交互运行里没有人能批准：需要批准的调用会被直接拒绝，所以工具跑得久只是在跑，不是「可能在等批准」。
				v.ApprovalSuspected, v.Reason = true, ReasonToolRunning
			}
			break
		}
		if screen != nil {
			// 屏幕信号可信时，思考 / 回复中的卡住只看屏幕（托管终端一列的规则），不再套用「hook 静默多久」。
			if reason, hard := screenStall(*screen, in.Now, th); reason != "" {
				v.Reason = reason
				v.Stalled, v.StalledSoft = hard, !hard
			}
			break
		}
		switch {
		case quiet >= th.ThinkStalled:
			v.Stalled, v.Reason = true, ReasonQuiet
		case quiet >= th.ThinkSoft:
			v.StalledSoft, v.Reason = true, ReasonQuietSoft
		}
	case StateWaitingApproval:
		if in.Now.Sub(s.Since) >= th.ApprovalRemind {
			v.ApprovalOverdue, v.Reason = true, ReasonApprovalOverdue
		}
	}
	return v
}

// screenStall 按托管终端一列的规则判断思考 / 回复中的卡住：终端冻结（硬）优先于 token 不变（软）。
// 核心结论（实验 §8）：token 不变本身不是证据，必须同时「屏幕仍在动画」；审批等待时屏幕本来就零字节，不算冻结。
func screenStall(sc ScreenSignal, now time.Time, th Thresholds) (reason string, hard bool) {
	if sc.Approval {
		return "", false
	}
	if th.TUIFrozen > 0 && !sc.Animating && sc.QuietFor >= th.TUIFrozen {
		return ReasonTerminalFrozen, true
	}
	if th.TUITokenFlat > 0 && sc.Animating && sc.Tokens >= 0 && !sc.TokensSince.IsZero() && now.Sub(sc.TokensSince) >= th.TUITokenFlat {
		return ReasonTokensFlat, false
	}
	return "", false
}
