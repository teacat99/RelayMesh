package api

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/teacat99/RelayMesh/v2/internal/chat"
)

// conversationEvents 是会让聊天消息流变化的 SSE 事件。
var conversationEvents = map[string]bool{
	"session_update":          true,
	"session_cancelled":       true,
	"session_archived":        true,
	"session_unarchived":      true,
	"session_completed":       true,
	"session_updated":         true,
	"session_deleted":         true,
	"queued_feedback_updated": true,
	"queued_feedback_revoked": true,
	"confirm_updated":         true,
	"work_item_updated":       true,
	"workflow_mode_updated":   true,
	"conversation_changed":    true,
}

// conversationDebounce 合并同一个工作流的连发事件（一次答复会连着触发 confirm、inbox、session 好几个）。
const conversationDebounce = 150 * time.Millisecond

// touchConversation 从事件载荷里取工作流编号，防抖后广播 conversation_updated。
// 事件里只带编号与时间：页面收到后自己重新拉最新一页并按消息编号合并。
func (b *SSEBroker) touchConversation(payload string) {
	var p struct {
		WorkflowID string `json:"workflow_id"`
		SessionID  string `json:"session_id"`
		GroupID    string `json:"group_id"`
	}
	if json.Unmarshal([]byte(payload), &p) != nil {
		return
	}
	if gid := strings.TrimSpace(p.GroupID); gid != "" {
		b.scheduleTouchKey(gid, map[string]any{"conversation_id": gid, "group_id": gid})
		return
	}
	wf := strings.TrimSpace(p.WorkflowID)
	if wf == "" {
		// MCP 侧的 session_update 只带会话编号：查库要在锁外做（Broadcast 调用时还持有客户端锁）。
		if id := strings.TrimSpace(p.SessionID); id != "" && b.sessionWorkflow != nil {
			go func() {
				if wf := strings.TrimSpace(b.sessionWorkflow(id)); wf != "" {
					b.scheduleTouch(wf)
				}
			}()
		}
		return
	}
	b.scheduleTouch(wf)
}

// scheduleTouch 为一个工作流排一次防抖后的 conversation_updated。
func (b *SSEBroker) scheduleTouch(wf string) {
	b.scheduleTouchKey(wf, map[string]any{"conversation_id": chat.DirectID(wf), "workflow_id": wf})
}

// scheduleTouchKey 为一个会话排一次防抖后的 conversation_updated；key 是防抖键（工作流编号或群编号），fields 是事件载荷。
func (b *SSEBroker) scheduleTouchKey(key string, fields map[string]any) {
	wf := key
	b.touchMu.Lock()
	defer b.touchMu.Unlock()
	if b.touch == nil {
		b.touch = map[string]*time.Timer{}
	}
	if _, pending := b.touch[wf]; pending {
		return // 已经排了一次，到点会带上最新状态
	}
	b.touch[wf] = time.AfterFunc(conversationDebounce, func() {
		b.touchMu.Lock()
		delete(b.touch, wf)
		b.touchMu.Unlock()
		select {
		case <-b.done:
			return
		default:
		}
		payload := map[string]any{"at": time.Now().UnixMilli()}
		for k, v := range fields {
			payload[k] = v
		}
		b.Broadcast("conversation_updated", payload)
	})
}
