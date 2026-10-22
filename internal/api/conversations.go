package api

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/teacat99/RelayMesh/v2/internal/chat"
	"github.com/teacat99/RelayMesh/v2/internal/store"
)

// 统一消息流 REST（#116）：把一个工作流 1:1 会话的汇报、你的回复、追加、确认卡、任务进展合成一条按时间排序的消息流。
// 鉴权与 /api/v1 其余接口一致（登录 JWT）；会话编号形如 wf:<workflow_id>。

// chatService 返回消息流服务（懒创建，测试里直接构造 APIHandler 也能用）。
func (h *APIHandler) chatService() *chat.Service {
	if h.chat == nil {
		h.chat = chat.NewService(h.store)
	}
	return h.chat
}

// conversationWorkflow 解析路径里的会话编号，只支持 1:1（wf:）会话。
func conversationWorkflow(c *gin.Context) (string, bool) {
	wf, ok := chat.ParseDirectID(c.Param("id"))
	if !ok || strings.TrimSpace(wf) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "会话编号无效（应形如 wf:<workflow_id>）"})
		return "", false
	}
	return wf, true
}

func writeChatError(c *gin.Context, err error) {
	var ae *store.AppError
	if errors.As(err, &ae) && ae.Kind == store.ErrInvalidInput {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
}

// ListConversationMessages GET /api/v1/conversations/:id/messages?limit=&before=&after=&around=&q=&kinds=a,b
func (h *APIHandler) ListConversationMessages(c *gin.Context) {
	if chat.IsGroupID(c.Param("id")) {
		h.listGroupMessages(c)
		return
	}
	wf, ok := conversationWorkflow(c)
	if !ok {
		return
	}
	limit, _ := strconv.Atoi(c.Query("limit"))
	var kinds []string
	if raw := strings.TrimSpace(c.Query("kinds")); raw != "" {
		kinds = strings.Split(raw, ",")
	}
	page, err := h.chatService().List(c.Request.Context(), wf, chat.ListParams{
		Limit: limit, Before: c.Query("before"), After: c.Query("after"), Around: c.Query("around"),
		Query: c.Query("q"), Kinds: kinds,
	})
	if err != nil {
		writeChatError(c, err)
		return
	}
	c.JSON(http.StatusOK, page)
}

// MarkConversationRead POST /api/v1/conversations/:id/read  {cursor?}
func (h *APIHandler) MarkConversationRead(c *gin.Context) {
	if chat.IsGroupID(c.Param("id")) {
		h.markGroupRead(c)
		return
	}
	wf, ok := conversationWorkflow(c)
	if !ok {
		return
	}
	var req struct {
		Cursor string `json:"cursor"`
	}
	_ = c.ShouldBindJSON(&req) // 允许空体：表示「读到现在」
	st, err := h.chatService().MarkRead(c.Request.Context(), wf, req.Cursor)
	if err != nil {
		writeChatError(c, err)
		return
	}
	// 已读游标是跨设备共享的：通知其他页面刷新未读徽标。
	if h.broker != nil {
		h.broker.Broadcast("conversation_changed", gin.H{"workflow_id": wf, "read": true})
	}
	c.JSON(http.StatusOK, gin.H{"status": "ok", "read_ms": st.ReadMs, "unread": st.Unread})
}

// ConversationSummaries GET /api/v1/conversation-summaries?ids=wf:a,wf:b（或 workflow_ids=a,b）
func (h *APIHandler) ConversationSummaries(c *gin.Context) {
	var wfs []string
	for _, id := range strings.Split(c.Query("ids"), ",") {
		if wf, ok := chat.ParseDirectID(strings.TrimSpace(id)); ok {
			wfs = append(wfs, wf)
		}
	}
	for _, wf := range strings.Split(c.Query("workflow_ids"), ",") {
		if wf = strings.TrimSpace(wf); wf != "" {
			wfs = append(wfs, wf)
		}
	}
	if len(wfs) > 200 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "一次最多查询 200 个会话"})
		return
	}
	list, err := h.chatService().Summaries(c.Request.Context(), wfs)
	if err != nil {
		writeChatError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"summaries": list})
}

// SendConversationMessage POST /api/v1/conversations/:id/messages
// 请求体：{text, images?, reply_to?, target?, draft_workflow_id?, host_name?, project_directory?}。
// reply_to 形如 s:<session_id>：正文前加「【回复 <8 位哈希>】」；target 为 auto（默认，沿用投递规则）或 inbox（强制进收件箱）。
func (h *APIHandler) SendConversationMessage(c *gin.Context) {
	if chat.IsGroupID(c.Param("id")) {
		h.sendGroupMessage(c)
		return
	}
	wf, ok := conversationWorkflow(c)
	if !ok {
		return
	}
	var req struct {
		Text            string          `json:"text"`
		Images          []feedbackImage `json:"images"`
		ReplyTo         string          `json:"reply_to"`
		Target          string          `json:"target"`
		DraftWorkflowID string          `json:"draft_workflow_id"`
		HostName        string          `json:"host_name"`
		ProjectDir      string          `json:"project_directory"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	text := strings.TrimSpace(req.Text)
	if text == "" && len(req.Images) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "消息不能为空"})
		return
	}
	target := strings.TrimSpace(req.Target)
	if target != "" && target != "auto" && target != "inbox" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "target 只能是 auto 或 inbox"})
		return
	}
	if ref := strings.TrimSpace(req.ReplyTo); ref != "" {
		hash, ok := chat.ReplyHash(ref)
		if !ok {
			c.JSON(http.StatusBadRequest, gin.H{"error": "reply_to 无效（应形如 s:<session_id>）"})
			return
		}
		text = "【回复 " + hash + "】 " + text
	}

	ctx := c.Request.Context()
	draftWF := strings.TrimSpace(req.DraftWorkflowID)
	if draftWF == "" {
		draftWF = wf
	}
	images, consumed, err := h.resolveFeedbackImages(ctx, draftWF, req.Images)
	if err != nil {
		writeResolveError(c, err)
		return
	}
	fillImageHashes(images)

	d, err := h.deliverWorkflowFeedback(ctx, wf, workflowFeedbackInput{
		ResponseText: text, Images: images, HostName: req.HostName, ProjectDir: req.ProjectDir,
		ForceInbox: target == "inbox",
	})
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	h.releaseConsumedDraftImages(ctx, draftWF, consumed)

	msg, err := h.chatService().Delivered(ctx, wf, d.Session, d.Queued)
	if err != nil {
		writeChatError(c, err)
		return
	}
	// 你发消息 = 你看过这个会话：把已读游标推到现在，别让自己发的话触发未读。
	_, _ = h.chatService().MarkRead(ctx, wf, "")
	c.JSON(http.StatusOK, gin.H{"status": "ok", "message": msg, "delivered_as": deliveredAs(d)})
}

func deliveredAs(d *workflowDelivery) string {
	if d.Session != nil {
		return "round_reply"
	}
	return "inbox"
}
