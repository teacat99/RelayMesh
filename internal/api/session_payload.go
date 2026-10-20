package api

import (
	"time"

	"github.com/teacat99/RelayMesh/v2/internal/model"
)

// sessionLiveSource 提供只存在于内存中的会话实时状态：MCP 等待者与最近一次保活响应时间。
// *mcp.Server 满足该接口；抽成接口是为了让载荷规整逻辑可以脱离完整 MCP 服务单测。
type sessionLiveSource interface {
	GetActiveWaiterInfo(sessionID string) (active bool, startedAt *time.Time, timeoutSec int)
	GetLastKeepaliveTime(sessionID string) *time.Time
}

// sessionEventPayload 生成 SSE 会话类事件（session_updated / session_completed 等）的载荷。
//
// 约定：载荷与 GET /sessions/:id 返回的同一会话等价——补齐 MCP 实时字段、图片只带引用不带 Base64。
// 前端据此可以直接对本地缓存打补丁，而不必在每个事件后整条重拉列表来「纠正」实时字段。
//
// 始终在副本上操作：调用方传入的 session 可能同时被 MCP 等待者协程读写（例如提交反馈后唤醒等待者并将其标记为已消费），
// 直接改动原对象既会污染接口响应，也会与等待者产生数据竞争。
func sessionEventPayload(live sessionLiveSource, session *model.FeedbackSession) *model.FeedbackSession {
	if session == nil {
		return nil
	}

	out := *session
	if len(session.Images) > 0 {
		out.Images = make(model.SessionImages, len(session.Images))
		copy(out.Images, session.Images)
	}

	// 实时字段只认内存中的真实状态，绝不沿用入参上可能残留的值。
	out.IsMCPActive = false
	out.MCPActiveAt = nil
	out.MCPTimeoutSec = 0
	out.LastKeepaliveAt = nil
	if live != nil {
		if active, startedAt, timeoutSec := live.GetActiveWaiterInfo(out.ID); active {
			out.IsMCPActive = true
			out.MCPActiveAt = startedAt
			out.MCPTimeoutSec = timeoutSec
		}
		out.LastKeepaliveAt = live.GetLastKeepaliveTime(out.ID)
	}

	projectSessionImages(&out)
	return &out
}

// liveSource 返回当前处理器可用的实时状态来源；mcpServer 为空时返回 nil 接口，避免「带类型的 nil 指针」被误判为非空。
func (h *APIHandler) liveSource() sessionLiveSource {
	if h.mcpServer == nil {
		return nil
	}
	return h.mcpServer
}

// sessionPayload 是 sessionEventPayload 在 APIHandler 上的便捷封装。
func (h *APIHandler) sessionPayload(session *model.FeedbackSession) *model.FeedbackSession {
	return sessionEventPayload(h.liveSource(), session)
}

// broadcastSession 统一广播携带完整会话的 SSE 事件，保证所有会话事件载荷语义一致。
func (h *APIHandler) broadcastSession(event string, session *model.FeedbackSession) {
	h.broker.Broadcast(event, h.sessionPayload(session))
}
