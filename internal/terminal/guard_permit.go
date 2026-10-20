package terminal

import (
	"fmt"
	"time"
)

// 唤醒许可（第十五批 C2）：智能体或人在任务全部完成时把许可置 0，本会话结束 / 停摆时不告警；
// 本该触发告警的那一刻消费许可并恢复为 1，同一次事件仍显示「已完成」而不重复推送。

const guardPermitMaxAge = 24 * time.Hour

const (
	GuardDone GuardState = "done" // 已标记全部完成，按约定不唤醒、不推送
)

// guardPermitFacts 是纯判断用的许可相关事实（由 guard.go 从持久状态与运行时拼出来）。
type guardPermitFacts struct {
	// Marked 表示当前许可为 0（有效标记）。
	Marked bool
	SetAt  time.Time
	// WakeSinceMark 自标记以来登记过一次唤醒。
	WakeSinceMark bool
	// OpenWorkSinceMark 自标记以来出现了新的待处理的事（未读回复、未读追加；不含进行中的任务）。
	OpenWorkSinceMark bool
	// SuppressedEpisode 消费许可时记下的事件标识：同一事件继续显示 done 而不告警。
	SuppressedEpisode string
}

// permitEffect 是纯函数 applyWakePermit 建议服务层做的持久化变更。
type permitEffect struct {
	ClearMark       bool
	Consume         bool
	SuppressEpisode string
}

func permitOpenWorkSinceMark(f guardFacts, since time.Time) bool {
	if since.IsZero() {
		return false
	}
	if fb := f.Feedback; fb != nil && fb.Status == "completed" && !fb.ConsumedByAI && fb.UpdatedAt.After(since) {
		return true
	}
	if f.InboxUnread > 0 && !f.InboxOldestUnread.IsZero() && f.InboxOldestUnread.After(since) {
		return true
	}
	if fb := f.Feedback; fb != nil && fb.DeadlineAt != nil && fb.DeadlineAt.After(since) {
		open := guardOpenWork(f)
		if open.PollLost {
			return true
		}
	}
	return false
}

func markShouldClear(f guardFacts, pf guardPermitFacts) bool {
	if !pf.Marked {
		return false
	}
	if !pf.SetAt.IsZero() && f.Now.Sub(pf.SetAt) > guardPermitMaxAge {
		return true
	}
	if pf.WakeSinceMark {
		return true
	}
	if pf.OpenWorkSinceMark || permitOpenWorkSinceMark(f, pf.SetAt) {
		return true
	}
	return false
}

// applyWakePermit 在 evaluateGuard 的原始结论上叠加唤醒许可语义。
func verdictDoneForSuppressed(raw GuardVerdict, f guardFacts, pf guardPermitFacts) (GuardVerdict, bool) {
	if pf.SuppressedEpisode == "" || raw.Episode != pf.SuppressedEpisode {
		return raw, false
	}
	switch raw.State {
	case GuardCrashed, GuardStalled, GuardEnded:
		out := raw
		out.State = GuardDone
		out.Reason = "已标记任务全部完成，按约定不唤醒"
		out.Episode = ""
		out.Cause = ""
		return out, true
	default:
		return raw, false
	}
}

func applyWakePermit(raw GuardVerdict, f guardFacts, pf guardPermitFacts) (GuardVerdict, permitEffect) {
	var eff permitEffect
	if v, ok := verdictDoneForSuppressed(raw, f, pf); ok {
		return v, eff
	}
	if !pf.Marked {
		return raw, eff
	}
	if markShouldClear(f, pf) {
		eff.ClearMark = true
		return raw, eff
	}

	trigger := raw.State == GuardCrashed || raw.State == GuardStalled || raw.State == GuardEnded
	if trigger && raw.Episode != "" {
		if pf.SuppressedEpisode == raw.Episode {
			// 已消费过：同一事件继续 done，不告警。
		} else if pf.SuppressedEpisode == "" {
			eff.Consume = true
			eff.SuppressEpisode = raw.Episode
		}
	}

	switch raw.State {
	case GuardCrashed, GuardStalled, GuardEnded, GuardIdle:
		out := raw
		out.State = GuardDone
		out.Reason = "已标记任务全部完成，按约定不唤醒"
		out.Episode = ""
		out.Cause = ""
		if out.Since != nil {
			out.Detail = append([]string(nil), out.Detail...)
			out.Detail = append(out.Detail, fmt.Sprintf("标记于 %s 前", guardAge(f.Now.Sub(pf.SetAt))))
		}
		return out, eff
	default:
		return raw, eff
	}
}
