package store

import (
	"time"

	"github.com/teacat99/RelayMesh/v2/internal/model"
)

// AgentLiveState 是顶栏存活图标的结论。
type AgentLiveState string

const (
	AgentLiveRunning AgentLiveState = "running"
	AgentLiveIdle    AgentLiveState = "idle"
	AgentLiveQuiet   AgentLiveState = "quiet"
	AgentLiveUnknown AgentLiveState = "unknown"
)

// AgentLiveView 供 GET /workflows/:id/inbox 与前端使用。
type AgentLiveView struct {
	State  AgentLiveState `json:"state"`
	Reason string         `json:"reason,omitempty"`
	At     *time.Time     `json:"at,omitempty"`
}

const (
	agentLiveHookStale  = 24 * time.Hour
	agentLiveToolWindow = 90 * time.Second
	agentLiveQuietHook  = 3 * time.Minute
)

// ComputeAgentLive 根据 hook 痕迹、智能体汇报与会话状态推断顶栏图标档位（纯函数）。
func ComputeAgentLive(link AgentLink, hookAt time.Time, hookOK bool, sess *model.FeedbackSession, now time.Time) AgentLiveView {
	if !hookOK || now.Sub(hookAt) > agentLiveHookStale {
		return AgentLiveView{State: AgentLiveUnknown, Reason: "尚未收到编辑器 hook 的查询（或已超过 24 小时）"}
	}

	executing := sess != nil && sess.Status == "completed" && sess.ConsumedByAI

	lastEvent := latestOf(link.LastToolAt, link.StopAt)
	if !link.StopAt.IsZero() && link.StopAt.Equal(lastEvent) && link.StopStatus == StopCompleted {
		at := link.StopAt
		return AgentLiveView{State: AgentLiveIdle, Reason: "最近一轮对话已正常收尾", At: &at}
	}

	if !link.LastToolAt.IsZero() && now.Sub(link.LastToolAt) <= agentLiveToolWindow {
		if link.StopAt.Before(link.LastToolAt) || link.StopStatus != StopCompleted {
			at := link.LastToolAt
			return AgentLiveView{State: AgentLiveRunning, Reason: "90 秒内有工具调用结束", At: &at}
		}
	}

	shouldWork := executing
	if !link.LastToolAt.IsZero() && now.Sub(link.LastToolAt) <= agentLiveToolWindow {
		if link.StopAt.Before(link.LastToolAt) || link.StopStatus != StopCompleted {
			shouldWork = true
		}
	}
	if shouldWork && now.Sub(hookAt) >= agentLiveQuietHook {
		at := hookAt
		return AgentLiveView{State: AgentLiveQuiet, Reason: "智能体应在执行，但超过 3 分钟没有 hook 事件", At: &at}
	}

	if executing {
		at := hookAt
		return AgentLiveView{State: AgentLiveRunning, Reason: "智能体已取走回复，正在执行", At: &at}
	}

	return AgentLiveView{State: AgentLiveUnknown, Reason: "hook 已安装，但最近没有可判定的活动"}
}
