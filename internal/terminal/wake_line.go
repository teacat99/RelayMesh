package terminal

import (
	"context"
	"log"
	"strings"
	"time"

	"github.com/teacat99/RelayMesh/v2/internal/chat"
	"github.com/teacat99/RelayMesh/v2/internal/model"
)

// 运行类操作的两个公共零件：失败原因（面向界面）与「聊天里记一条系统行」。
// 它们原先住在会话守护的 REST 与非交互唤醒文件里（D-214 / #113）；守护在 #245 第二期下线后单独放在这里，
// 预设运行、会话页运行、启用 Agent、自动续跑、运行结束处理都在用。

// wakeError 是运行类操作失败的原因，面向界面：带 HTTP 状态与错误码。
type wakeError struct {
	Status  int
	Code    string
	Message string
}

func (e *wakeError) Error() string { return e.Message }

func newWakeError(status int, code, msg string) *wakeError {
	return &wakeError{Status: status, Code: code, Message: msg}
}

// SetWakeHook 接入「聊天里多了一条系统行」的通知；启动时调用一次，传 nil 表示摘掉。fn 必须立即返回。
func (h *Hub) SetWakeHook(fn func(workflowID string)) {
	if h == nil {
		return
	}
	if fn == nil {
		h.wakeHook.Store(nil)
		return
	}
	h.wakeHook.Store(&fn)
}

// addWakeLine 在这个工作流的聊天里记一条唤醒类系统行，并通知 Web 刷新。失败只记日志：它只是个记录，不能连累唤醒或运行本身。
func (g *guardService) addWakeLine(ctx context.Context, workflowID, actorKind, actorRef, text, meta string, now time.Time) {
	workflowID = strings.TrimSpace(workflowID)
	if workflowID == "" {
		return
	}
	if _, err := g.h.store.AddConversationEvent(ctx, model.ConversationEvent{
		ConversationID: chat.DirectID(workflowID), WorkflowID: workflowID,
		Kind: model.ConversationEventWake, ActorKind: actorKind, ActorRef: actorRef,
		Text: text, Meta: meta, CreatedAt: now,
	}); err != nil {
		log.Printf("[terminal] 记录唤醒系统行失败：%v", err)
		return
	}
	if p := g.h.wakeHook.Load(); p != nil {
		func() {
			defer func() {
				if r := recover(); r != nil {
					log.Printf("[terminal] 唤醒挂钩 panic：%v", r)
				}
			}()
			(*p)(workflowID)
		}()
	}
}
