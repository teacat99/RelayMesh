package store

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"regexp"
	"strings"
	"time"

	"github.com/teacat99/RelayMesh/v2/internal/model"
)

// 智能体在编辑器里的运行情况（D-214）：编辑器 hook 每次询问收件箱时顺带汇报「刚结束了一次工具调用」或「一轮对话结束了」，
// 以及 Cursor 的对话编号。服务端据此判断会话有没有停摆（会话守护），并在会话意外结束后用对话编号恢复。
//
// 事件时间只放内存（hook 每十几秒来一次，不值得写库）；对话编号只在变化时落库，Hub 重启后仍能用来恢复。

// 汇报事件的种类。
const (
	AgentEventTool = "tool" // 一次工具调用刚结束（说明智能体还在干活）
	AgentEventStop = "stop" // 一轮对话结束了（正常收尾、被中止或出错）
)

// 一轮对话的结束状态。其他取值一律记为 StopOther。
const (
	StopCompleted = "completed"
	StopAborted   = "aborted"
	StopError     = "error"
	StopOther     = "other"
)

// AgentEvent 是 hook 汇报的一次事件。字段都由调用方自报，入库前统一清洗（见 NormalizeAgentEvent）。
type AgentEvent struct {
	Kind           string
	Status         string
	ConversationID string
}

// AgentLink 是某个工作流的智能体最近的运行情况。
type AgentLink struct {
	ConversationID string    // Cursor 对话编号（用于重新拉起时恢复同一个对话）；未知时为空
	LastToolAt     time.Time // 最近一次工具调用结束的时间（零值 = 没汇报过）
	StopAt         time.Time // 最近一轮对话结束的时间（零值 = 没汇报过）
	StopStatus     string    // 那一轮的结束状态；StopAt 为零值时为空
}

var agentConvRe = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// NormalizeAgentEvent 清洗 hook 自报的事件：种类只认 tool / stop；状态只认 completed / aborted / error（其余记为 other，
// 只在 stop 事件里有意义）；对话编号只认字母数字、下划线、连字符，最长 64 位，格式不对就当没有。返回 ok=false 表示没有可记的内容。
func NormalizeAgentEvent(ev AgentEvent) (AgentEvent, bool) {
	ev.Kind = strings.ToLower(strings.TrimSpace(ev.Kind))
	ev.Status = strings.ToLower(strings.TrimSpace(ev.Status))
	ev.ConversationID = strings.TrimSpace(ev.ConversationID)
	if !agentConvRe.MatchString(ev.ConversationID) {
		ev.ConversationID = ""
	}
	switch ev.Kind {
	case AgentEventTool:
		ev.Status = ""
	case AgentEventStop:
		switch ev.Status {
		case StopCompleted, StopAborted, StopError:
		default:
			ev.Status = StopOther
		}
	default:
		// 没有种类（旧版 hook）：只有对话编号才值得记。
		ev.Kind, ev.Status = "", ""
		if ev.ConversationID == "" {
			return AgentEvent{}, false
		}
	}
	return ev, true
}

// agentLinkKey 是对话编号在 system_settings 里的键：工作流编号可能很长（上限 128），而键最长 64，所以取哈希。
func agentLinkKey(workflowID string) string {
	sum := sha1.Sum([]byte(workflowID))
	return "agent_link:" + hex.EncodeToString(sum[:8])
}

type agentLinkDoc struct {
	WorkflowID     string `json:"workflow_id"`
	ConversationID string `json:"conversation_id"`
}

// RecordAgentEvent 记下某个工作流的智能体刚汇报的一次事件。对话编号变了才写库，写库失败只丢掉持久化，不影响内存里的记录。
func (s *Store) RecordAgentEvent(ctx context.Context, workflowID string, ev AgentEvent, at time.Time) {
	workflowID = strings.TrimSpace(workflowID)
	if workflowID == "" {
		return
	}
	ev, ok := NormalizeAgentEvent(ev)
	if !ok {
		return
	}
	// 对话被 Hub 发起的运行认领了（见 ClaimConversation）：这个对话的活动时间与对话编号归认领者，
	// 不记到 hook 配置里写的那个工作流上，免得它的「智能体在线 / 续聊目标」被别人的对话污染（#182）。
	if owner := s.ConversationOwner(ev.ConversationID, at); owner != "" {
		workflowID = owner
	}
	a := &s.activity
	a.mu.Lock()
	if a.link == nil {
		a.link = make(map[string]AgentLink)
	}
	if len(a.link) > 2000 { // 工作流数量不会这么多；真到了就清掉陈旧的，免得无限增长
		for k, l := range a.link {
			if at.Sub(latestOf(l.LastToolAt, l.StopAt)) > InboxExpireAfter {
				delete(a.link, k)
			}
		}
	}
	l := a.link[workflowID]
	switch ev.Kind {
	case AgentEventTool:
		l.LastToolAt = at
	case AgentEventStop:
		l.StopAt, l.StopStatus = at, ev.Status
	}
	changed := false
	if ev.ConversationID != "" && ev.ConversationID != l.ConversationID {
		l.ConversationID, changed = ev.ConversationID, true
	}
	a.link[workflowID] = l
	a.mu.Unlock()

	if changed {
		raw, err := json.Marshal(agentLinkDoc{WorkflowID: workflowID, ConversationID: ev.ConversationID})
		if err == nil {
			_ = s.db.WithContext(ctx).Save(&model.SystemSetting{Key: agentLinkKey(workflowID), Value: string(raw), UpdatedAt: at}).Error
		}
	}
}

// AgentLinkOf 返回某个工作流的智能体最近的运行情况；内存里没有对话编号（比如刚重启过）时，用落库的那一份补上。
func (s *Store) AgentLinkOf(ctx context.Context, workflowID string) AgentLink {
	workflowID = strings.TrimSpace(workflowID)
	if workflowID == "" {
		return AgentLink{}
	}
	a := &s.activity
	a.mu.Lock()
	l := a.link[workflowID]
	a.mu.Unlock()
	if l.ConversationID != "" {
		return l
	}
	var rec model.SystemSetting
	if err := s.db.WithContext(ctx).Where("key = ?", agentLinkKey(workflowID)).First(&rec).Error; err != nil {
		return l // 没存过（或库暂时读不了）：就当没有对话编号
	}
	var doc agentLinkDoc
	if json.Unmarshal([]byte(rec.Value), &doc) == nil && doc.WorkflowID == workflowID && agentConvRe.MatchString(doc.ConversationID) {
		l.ConversationID = doc.ConversationID
	}
	return l
}

// dropForeignAgentLinks 清掉别的工作流上记着这个对话编号的「续聊目标」（内存与落库两处）。
// 对话被 wf 认领之前，hook 可能已经按目录里的配置把它报到了别的工作流名下，那边的链路（RecordAgentEvent 以后写的对话编号）
// 会把它当成自己的对话——之后那个工作流的预设运行、会话页「运行」就会去续聊别人的对话（#182）。
// 只清编号正好是这个对话的链路，内存里的别的字段（最近活动时间等）不动；落库的那一份只有对话编号，清掉就是删掉这一行；wf 自己的链路不动。
func (s *Store) dropForeignAgentLinks(ctx context.Context, wf, conv string) {
	if wf == "" || conv == "" {
		return
	}
	a := &s.activity
	a.mu.Lock()
	for k, l := range a.link {
		if k != wf && l.ConversationID == conv {
			l.ConversationID = ""
			a.link[k] = l
		}
	}
	a.mu.Unlock()

	var rows []model.SystemSetting
	if err := s.db.WithContext(ctx).Where("key LIKE 'agent_link:%'").Find(&rows).Error; err != nil {
		return
	}
	for _, rec := range rows {
		var doc agentLinkDoc
		if json.Unmarshal([]byte(rec.Value), &doc) != nil || doc.ConversationID != conv || doc.WorkflowID == wf {
			continue
		}
		_ = s.db.WithContext(ctx).Where("key = ?", rec.Key).Delete(&model.SystemSetting{}).Error
	}
}

func latestOf(ts ...time.Time) time.Time {
	var out time.Time
	for _, t := range ts {
		if t.After(out) {
			out = t
		}
	}
	return out
}
