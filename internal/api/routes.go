package api

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"github.com/teacat99/RelayMesh/v2/internal/chat"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/teacat99/RelayMesh/v2/internal/config"
	"github.com/teacat99/RelayMesh/v2/internal/mcp"
	"github.com/teacat99/RelayMesh/v2/internal/model"
	"github.com/teacat99/RelayMesh/v2/internal/ntfy"
	"github.com/teacat99/RelayMesh/v2/internal/oauth"
	"github.com/teacat99/RelayMesh/v2/internal/store"
	"github.com/teacat99/RelayMesh/v2/internal/terminal"
)

type APIHandler struct {
	cfg       *config.Config
	store     *store.Store
	mcpServer *mcp.Server
	broker    *SSEBroker
	terminal  *terminal.Hub // 可能为 nil（终端功能整体不可用）；令牌管理里涉及终端的操作依赖它
	oauth     *oauth.Service

	ntfy      *ntfy.Notifier // ntfy 推送器（见 ntfy.go）；NewServer 里接线
	ntfyGuard ntfyGuard      // 测试推送的节流

	chat *chat.Service // 统一消息流（conversations.go）；懒创建
}

func NewAPIHandler(cfg *config.Config, st *store.Store, mcpServer *mcp.Server, broker *SSEBroker) *APIHandler {
	return &APIHandler{
		cfg:       cfg,
		store:     st,
		mcpServer: mcpServer,
		broker:    broker,
	}
}

func (h *APIHandler) GetCurrentSession(c *gin.Context) {
	projectDir := c.Query("project_directory")
	session, err := h.store.GetCurrentFeedbackSession(c.Request.Context(), projectDir)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if session == nil {
		c.JSON(http.StatusOK, gin.H{"has_session": false, "session": nil})
		return
	}
	if active, startedAt, timeoutSec := h.mcpServer.GetActiveWaiterInfo(session.ID); active {
		session.IsMCPActive = true
		session.MCPActiveAt = startedAt
		session.MCPTimeoutSec = timeoutSec
	}
	session.LastKeepaliveAt = h.mcpServer.GetLastKeepaliveTime(session.ID)
	projectSessionImages(session)
	h.attachWorkflowAggregates(c.Request.Context(), []*model.FeedbackSession{session})
	c.JSON(http.StatusOK, gin.H{"has_session": true, "session": session})
}

func (h *APIHandler) ListSessions(c *gin.Context) {
	projectDir := c.Query("project_directory")
	status := c.Query("status")
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "200"))
	brief := c.Query("brief") == "true" || c.Query("compact") == "true"
	// cover_workflows=true（只对 brief 生效）：在最新 limit 行之外，再给每个工作流补上它最新的一行（#173）。
	// 侧栏与归档库要的是「每个工作流一条」，只拿最新 N 行会话时，话痨的工作流会把较早的工作流挤出窗口。
	coverWorkflows := brief && c.Query("cover_workflows") == "true"

	var sessions []model.FeedbackSession
	var err error
	if brief {
		if coverWorkflows {
			sessions, err = h.store.ListFeedbackSessionsBriefCovering(c.Request.Context(), projectDir, status, limit)
		} else {
			sessions, err = h.store.ListFeedbackSessionsBrief(c.Request.Context(), projectDir, status, limit)
		}
	} else {
		sessions, err = h.store.ListFeedbackSessions(c.Request.Context(), projectDir, status, limit)
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	for i := range sessions {
		if active, startedAt, timeoutSec := h.mcpServer.GetActiveWaiterInfo(sessions[i].ID); active {
			sessions[i].IsMCPActive = true
			sessions[i].MCPActiveAt = startedAt
			sessions[i].MCPTimeoutSec = timeoutSec
		}
		sessions[i].LastKeepaliveAt = h.mcpServer.GetLastKeepaliveTime(sessions[i].ID)
		if !brief {
			projectSessionImages(&sessions[i])
		}
	}
	// 工作流级聚合（侧栏会话卡的会话级状态用，#159）：按工作流去重后一次查出，不逐行查询
	rows := make([]*model.FeedbackSession, len(sessions))
	for i := range sessions {
		rows[i] = &sessions[i]
	}
	h.attachWorkflowAggregates(c.Request.Context(), rows)
	c.JSON(http.StatusOK, gin.H{"sessions": sessions})
}

func (h *APIHandler) GetWorkflowSessions(c *gin.Context) {
	workflowID := strings.TrimSpace(c.Param("workflow_id"))
	if workflowID == "" {
		workflowID = strings.TrimSpace(c.Param("id"))
	}
	if workflowID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "workflow_id is required"})
		return
	}

	sessions, err := h.store.ListWorkflowFeedbackSessions(c.Request.Context(), workflowID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	// ?light=1[&tail=N]：较早的行只带首行预览（见 sessions_light.go）；不带参数时与以前逐字节一致。
	// 先轻量化再装饰：轻量行没有图片，省掉对它们的图片投影。
	if wantsLight(c) {
		lightenSessions(sessions, lightTailParam(c))
	}

	for i := range sessions {
		if active, startedAt, timeoutSec := h.mcpServer.GetActiveWaiterInfo(sessions[i].ID); active {
			sessions[i].IsMCPActive = true
			sessions[i].MCPActiveAt = startedAt
			sessions[i].MCPTimeoutSec = timeoutSec
		}
		sessions[i].LastKeepaliveAt = h.mcpServer.GetLastKeepaliveTime(sessions[i].ID)
		projectSessionImages(&sessions[i])
	}

	// 整表是读多写少的大响应：带弱 ETag，If-None-Match 命中就回 304（见 conditional.go）。
	body, etag, err := marshalSessionsPayload(sessions)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	respondConditionalJSON(c, etag, body)
}

func (h *APIHandler) GetSession(c *gin.Context) {
	id := c.Param("id")
	session, err := h.store.GetFeedbackSession(c.Request.Context(), id)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
		return
	}
	if active, startedAt, timeoutSec := h.mcpServer.GetActiveWaiterInfo(session.ID); active {
		session.IsMCPActive = true
		session.MCPActiveAt = startedAt
		session.MCPTimeoutSec = timeoutSec
	}
	session.LastKeepaliveAt = h.mcpServer.GetLastKeepaliveTime(session.ID)
	projectSessionImages(session)
	c.JSON(http.StatusOK, gin.H{"session": session})
}

// RateLimitedWriter 实现带上下文感知与平滑分块输出的流式限速器，为核心 SSE 与 API 预留生存带宽
type RateLimitedWriter struct {
	w         io.Writer
	ctx       context.Context
	limitKBps int
}

func NewRateLimitedWriter(w io.Writer, ctx context.Context, limitKBps int) *RateLimitedWriter {
	return &RateLimitedWriter{
		w:         w,
		ctx:       ctx,
		limitKBps: limitKBps,
	}
}

func (rw *RateLimitedWriter) Write(p []byte) (int, error) {
	if rw.limitKBps <= 0 {
		return rw.w.Write(p)
	}

	chunkSize := 16 * 1024 // 16KB 分片
	interval := time.Duration(float64(chunkSize) / float64(rw.limitKBps*1024) * float64(time.Second))
	n := 0

	for n < len(p) {
		select {
		case <-rw.ctx.Done():
			return n, rw.ctx.Err()
		default:
		}

		end := n + chunkSize
		if end > len(p) {
			end = len(p)
		}

		nw, err := rw.w.Write(p[n:end])
		n += nw
		if err != nil {
			return n, err
		}

		if n < len(p) && interval > 0 {
			select {
			case <-rw.ctx.Done():
				return n, rw.ctx.Err()
			case <-time.After(interval):
			}
		}
	}

	return n, nil
}

func (h *APIHandler) getImageRateLimit(ctx context.Context) int {
	settings, err := h.store.GetSettings(ctx)
	if err != nil || settings == nil {
		return 0
	}
	valAny, ok := settings["image_rate_limit_kb"]
	if !ok {
		valAny, ok = settings["imageRateLimitKB"]
	}
	if ok {
		switch val := valAny.(type) {
		case float64:
			return int(val)
		case int:
			return val
		case string:
			if i, err := strconv.Atoi(val); err == nil {
				return i
			}
		}
	}
	return 0
}

// projectSessionImages 为会话中的图片生成不可变哈希访问 URL 并剥离超大 Base64，大幅降低列表 JSON 体积
func projectSessionImages(session *model.FeedbackSession) {
	if session == nil || len(session.Images) == 0 {
		return
	}
	for i := range session.Images {
		img := &session.Images[i]
		hash := img.Hash
		if hash == "" && img.Data != "" {
			raw := strings.TrimSpace(img.Data)
			if commaIdx := strings.Index(raw, ","); commaIdx != -1 {
				raw = raw[commaIdx+1:]
			}
			sum := sha256.Sum256([]byte(raw))
			hash = hex.EncodeToString(sum[:8])
			img.Hash = hash
		}
		if img.URL == "" {
			img.URL = model.BuildSessionImageRelativeURL(session.ID, i, hash)
		}
		// 剥离 Base64 字符串，避免前端多轮历史接口传输几十兆 JSON
		img.Data = ""
	}
}

func (h *APIHandler) GetSessionImage(c *gin.Context) {
	id := c.Param("id")
	indexStr := c.Param("index")
	idx, err := strconv.Atoi(indexStr)
	if err != nil || idx < 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid image index"})
		return
	}

	var images []model.SessionImage
	queuedID, isInbox := store.ParseInboxSessionID(id)
	if isInbox {
		// 追加指令收件箱里的图片：inbox-<编号> 当作「会话编号」寻址，供智能体按直链取图。
		q, err := h.store.GetQueuedFeedback(c.Request.Context(), queuedID)
		if err != nil || q == nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "inbox message not found"})
			return
		}
		images = q.Images
	} else {
		session, err := h.store.GetFeedbackSession(c.Request.Context(), id)
		if err != nil || session == nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "session not found"})
			return
		}
		images = session.Images
	}

	if idx >= len(images) {
		c.JSON(http.StatusNotFound, gin.H{"error": "image index out of range"})
		return
	}

	img := images[idx]
	raw := strings.TrimSpace(img.Data)
	if commaIdx := strings.Index(raw, ","); commaIdx != -1 {
		raw = raw[commaIdx+1:]
	}

	if raw == "" && !isInbox {
		// 如果内存中或极端情况下已被置空，重新从数据库直接读取该行原始记录以保证绝对可用
		var rawSession model.FeedbackSession
		if err := h.store.DB().WithContext(c.Request.Context()).Where("id = ?", id).First(&rawSession).Error; err == nil {
			if idx < len(rawSession.Images) {
				img = rawSession.Images[idx]
				raw = strings.TrimSpace(img.Data)
				if commaIdx := strings.Index(raw, ","); commaIdx != -1 {
					raw = raw[commaIdx+1:]
				}
			}
		}
	}

	if raw == "" {
		c.JSON(http.StatusNotFound, gin.H{"error": "image data not available"})
		return
	}

	decoded, err := base64.StdEncoding.DecodeString(raw)
	if err != nil {
		decoded, err = base64.RawStdEncoding.DecodeString(raw)
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to decode image data"})
		return
	}

	contentType := "image/png"
	format := strings.ToLower(strings.TrimSpace(img.Format))
	switch format {
	case "jpg", "jpeg":
		contentType = "image/jpeg"
	case "webp":
		contentType = "image/webp"
	case "gif":
		contentType = "image/gif"
	case "svg", "svg+xml":
		contentType = "image/svg+xml"
	}

	// 1. 计算不可变哈希并处理 ETag 快速协商
	hash := img.Hash
	if hash == "" {
		sum := sha256.Sum256(decoded)
		hash = hex.EncodeToString(sum[:8])
	}
	eTag := fmt.Sprintf("%q", hash)
	c.Header("ETag", eTag)
	if match := c.GetHeader("If-None-Match"); match == eTag {
		c.Status(http.StatusNotModified)
		return
	}

	// 2. 注入标准不可变强缓存头，浏览器后续 100% 走磁盘缓存 (from disk cache)
	c.Header("Content-Type", contentType)
	c.Header("Cache-Control", "public, max-age=31536000, immutable")
	c.Header("Content-Length", strconv.Itoa(len(decoded)))

	// 3. 检查流式限速 (默认 0 不限速，支持 128/256/512 等档位)
	limitKB := h.getImageRateLimit(c.Request.Context())
	if limitKB > 0 {
		writer := NewRateLimitedWriter(c.Writer, c.Request.Context(), limitKB)
		_, _ = writer.Write(decoded)
	} else {
		c.Data(http.StatusOK, contentType, decoded)
	}
}

type SubmitFeedbackRequest struct {
	ResponseText string          `json:"response_text"`
	UserMessages []string        `json:"user_messages"`
	Images       []feedbackImage `json:"images"`
	// DraftWorkflowID 是草稿图片所在的工作流（图片带 draft_key 时使用）；缺省取会话所属的工作流。
	DraftWorkflowID string `json:"draft_workflow_id"`
}

// fillImageHashes 给没有哈希的图片补上内容哈希（不可变访问 URL 与 ETag 依赖它）。
func fillImageHashes(images []model.SessionImage) {
	for i := range images {
		img := &images[i]
		if img.Hash == "" && img.Data != "" {
			raw := strings.TrimSpace(img.Data)
			if commaIdx := strings.Index(raw, ","); commaIdx != -1 {
				raw = raw[commaIdx+1:]
			}
			sum := sha256.Sum256([]byte(raw))
			img.Hash = hex.EncodeToString(sum[:8])
		}
	}
}

func hasDraftKeys(images []feedbackImage) bool {
	for _, img := range images {
		if img.DraftKey != "" {
			return true
		}
	}
	return false
}

func (h *APIHandler) SubmitFeedback(c *gin.Context) {
	id := c.Param("id")
	var req SubmitFeedbackRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}

	// 草稿图片已经在粘贴时上传过：这里直接复用服务器上的那份，不再让浏览器重传一遍
	draftWorkflowID := strings.TrimSpace(req.DraftWorkflowID)
	if draftWorkflowID == "" && hasDraftKeys(req.Images) {
		if sess, err := h.store.GetFeedbackSession(c.Request.Context(), id); err == nil && sess != nil {
			draftWorkflowID = sess.WorkflowID
			if draftWorkflowID == "" {
				draftWorkflowID = sess.ID
			}
		}
	}
	images, consumed, err := h.resolveFeedbackImages(c.Request.Context(), draftWorkflowID, req.Images)
	if err != nil {
		writeResolveError(c, err)
		return
	}
	fillImageHashes(images)

	session, err := h.store.SubmitFeedback(c.Request.Context(), store.SubmitFeedbackInput{
		SessionID:    id,
		ResponseText: req.ResponseText,
		UserMessages: req.UserMessages,
		Images:       images,
	})
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	h.releaseConsumedDraftImages(c.Request.Context(), draftWorkflowID, consumed)

	// 先取快照再唤醒 MCP 等待者：等待者协程会把同一个 session 对象标记为已消费，
	// 先复制既避免数据竞争，也保证「已完成」事件先于「已消费」事件发出（后者 updated_at 更新，前端据此去重）。
	payload := h.sessionPayload(session)
	h.mcpServer.NotifySessionCompleted(session)
	h.broker.Broadcast("session_completed", payload)

	c.JSON(http.StatusOK, gin.H{"status": "ok", "session": payload})
}

func (h *APIHandler) RevokeSession(c *gin.Context) {
	id := c.Param("id")
	res, session, err := h.store.RevokeSessionFeedback(c.Request.Context(), id)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	h.broadcastSession("session_updated", session)
	c.JSON(http.StatusOK, gin.H{"status": "ok", "revoked": res, "session": session})
}

func (h *APIHandler) AppendWorkflowFeedback(c *gin.Context) {
	workflowID := c.Param("workflow_id")
	var req struct {
		ResponseText string          `json:"response_text"`
		UserMessages []string        `json:"user_messages"`
		Images       []feedbackImage `json:"images"`
		HostName     string          `json:"host_name"`
		ProjectDir   string          `json:"project_directory"`
		// DraftWorkflowID 是草稿图片所在的工作流（图片带 draft_key 时使用）；缺省取路径里的工作流。
		DraftWorkflowID string `json:"draft_workflow_id"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// 草稿图片已经在粘贴时上传过：直接复用服务器上的那份，不再让浏览器重传
	draftWorkflowID := strings.TrimSpace(req.DraftWorkflowID)
	if draftWorkflowID == "" {
		draftWorkflowID = workflowID
	}
	images, consumed, err := h.resolveFeedbackImages(c.Request.Context(), draftWorkflowID, req.Images)
	if err != nil {
		writeResolveError(c, err)
		return
	}
	fillImageHashes(images)

	d, err := h.deliverWorkflowFeedback(c.Request.Context(), workflowID, workflowFeedbackInput{
		ResponseText: req.ResponseText,
		UserMessages: req.UserMessages,
		Images:       images,
		HostName:     req.HostName,
		ProjectDir:   req.ProjectDir,
	})
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	h.releaseConsumedDraftImages(c.Request.Context(), draftWorkflowID, consumed)

	if d.Session != nil {
		c.JSON(http.StatusOK, gin.H{"status": "ok", "type": "session", "session": d.Session})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "ok", "type": "queued", "queued": d.Queued})
}

// workflowFeedbackInput 是要交给工作流里智能体的内容。
type workflowFeedbackInput struct {
	ResponseText string
	UserMessages []string
	Images       []model.SessionImage
	HostName     string
	ProjectDir   string
	// ForceInbox 为真时不并入最近一轮会话，一律存进收件箱排队（聊天里选择「作为追加」）。
	ForceInbox bool
}

// workflowDelivery 是一次交付的结果：Session 不为空表示内容成了某一轮会话的回复；否则 Queued 是收件箱里排队的那一条。
type workflowDelivery struct {
	Session *model.FeedbackSession
	Queued  *model.QueuedFeedback
}

// deliverWorkflowFeedback 把追加的内容交给工作流里的智能体，并广播：
//  1. 最近一轮会话还在等回复（pending），或已有回复但智能体还没读走（completed 且未被消费）→ 内容就是这一轮的回复；
//  2. 否则存进收件箱排队，智能体下次发起交互 / 读取收件箱时带走。
//
// 草稿图片的释放由调用方处理（只有它知道图片来自哪里）。「追加指令」「在任务上发起改进需求」都走这里，投递规则只有这一份。
func (h *APIHandler) deliverWorkflowFeedback(ctx context.Context, workflowID string, in workflowFeedbackInput) (*workflowDelivery, error) {
	latestSess, err := h.store.GetLatestWorkflowFeedbackSession(ctx, workflowID)
	if !in.ForceInbox && err == nil && latestSess != nil && (latestSess.Status == "pending" || (latestSess.Status == "completed" && !latestSess.ConsumedByAI)) {
		sess, err := h.store.SubmitFeedback(ctx, store.SubmitFeedbackInput{
			SessionID:    latestSess.ID,
			ResponseText: in.ResponseText,
			UserMessages: in.UserMessages,
			Images:       in.Images,
		})
		if err != nil {
			return nil, err
		}
		payload := h.sessionPayload(sess)
		h.mcpServer.NotifySessionCompleted(sess)
		h.broker.Broadcast("session_completed", payload)
		return &workflowDelivery{Session: payload}, nil
	}

	q, err := h.store.QueueWorkflowFeedback(ctx, store.QueueFeedbackInput{
		WorkflowID:       workflowID,
		HostName:         in.HostName,
		ProjectDirectory: in.ProjectDir,
		ResponseText:     in.ResponseText,
		UserMessages:     in.UserMessages,
		Images:           in.Images,
	})
	if err != nil {
		return nil, err
	}
	// 事件里只带标识与状态，不带图片：收到事件的页面会自己重新拉取收件箱
	h.broker.Broadcast("queued_feedback_updated", gin.H{"workflow_id": workflowID, "id": q.ID, "state": q.State})
	return &workflowDelivery{Queued: q}, nil
}

func (h *APIHandler) ListQueuedFeedbacks(c *gin.Context) {
	workflowID := c.Param("workflow_id")
	list, err := h.store.ListPendingQueuedFeedbacks(c.Request.Context(), workflowID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"queued_feedbacks": list})
}

func (h *APIHandler) RevokeQueuedFeedback(c *gin.Context) {
	idStr := c.Param("id")
	id, err := strconv.ParseUint(idStr, 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid queued feedback id"})
		return
	}

	res, err := h.store.RevokeQueuedFeedback(c.Request.Context(), uint(id))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	h.broker.Broadcast("queued_feedback_revoked", gin.H{"id": id, "workflow_id": res.WorkflowID, "delivered": res.Delivered, "agent_notified": res.AgentNotified})
	c.JSON(http.StatusOK, gin.H{"status": "ok", "revoked": res})
}

// Workflow Drafts Handlers
func (h *APIHandler) GetWorkflowDraft(c *gin.Context) {
	workflowID := c.Param("workflow_id")
	draft, err := h.store.GetWorkflowDraft(c.Request.Context(), workflowID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if draft == nil {
		c.JSON(http.StatusOK, gin.H{"draft": nil})
		return
	}
	c.JSON(http.StatusOK, gin.H{"draft": draft})
}

func (h *APIHandler) SaveWorkflowDraft(c *gin.Context) {
	workflowID := c.Param("workflow_id")
	var req struct {
		ActiveIndex int    `json:"active_index"`
		DraftsJSON  string `json:"drafts_json"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body: " + err.Error()})
		return
	}

	draft, err := h.store.SaveWorkflowDraft(c.Request.Context(), workflowID, req.ActiveIndex, req.DraftsJSON)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "ok", "draft": draft})
}

func (h *APIHandler) DeleteWorkflowDraft(c *gin.Context) {
	workflowID := c.Param("workflow_id")
	if err := h.store.DeleteWorkflowDraft(c.Request.Context(), workflowID); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	// 草稿整体销毁：它的图片也一并删除
	if _, err := h.store.DeleteAllDraftImages(c.Request.Context(), workflowID); err != nil {
		log.Printf("[draft-images] 删除草稿图片失败（workflow=%s）: %v", workflowID, err)
	}
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

func (h *APIHandler) DeleteSession(c *gin.Context) {
	id := c.Param("id")
	session, err := h.store.DeleteFeedbackSession(c.Request.Context(), id)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	h.mcpServer.NotifySessionCompleted(session)
	h.broker.Broadcast("session_deleted", map[string]any{
		"session_id":  id,
		"workflow_id": session.WorkflowID,
	})
	c.JSON(http.StatusOK, gin.H{"status": "ok", "deleted_session_id": id})
}

func (h *APIHandler) RestoreSession(c *gin.Context) {
	var session model.FeedbackSession
	if err := c.ShouldBindJSON(&session); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	restored, err := h.store.RestoreFeedbackSession(c.Request.Context(), &session)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	h.broadcastSession("session_updated", restored)
	c.JSON(http.StatusOK, gin.H{"status": "ok", "session": restored})
}

func (h *APIHandler) CancelSession(c *gin.Context) {
	id := c.Param("id")
	session, err := h.store.CancelFeedbackSession(c.Request.Context(), id)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	h.mcpServer.NotifySessionCompleted(session)
	h.broadcastSession("session_cancelled", session)

	c.JSON(http.StatusOK, gin.H{"status": "ok", "session": session})
}

func (h *APIHandler) KeepaliveSession(c *gin.Context) {
	id := c.Param("id")
	extendSec, _ := strconv.Atoi(c.DefaultQuery("extend_seconds", "300"))

	session, err := h.store.KeepaliveFeedbackSession(c.Request.Context(), id, extendSec)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	h.broadcastSession("session_keepalive", session)
	c.JSON(http.StatusOK, gin.H{"status": "ok", "session": session})
}

func (h *APIHandler) ArchiveSession(c *gin.Context) {
	id := c.Param("id")
	session, err := h.store.ArchiveFeedbackSession(c.Request.Context(), id)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	h.broadcastSession("session_archived", session)
	c.JSON(http.StatusOK, gin.H{"status": "ok", "session": session})
}

func (h *APIHandler) RenameSession(c *gin.Context) {
	id := c.Param("id")
	var req struct {
		Title string `json:"title"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.Title == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "title is required"})
		return
	}

	session, err := h.store.RenameFeedbackSession(c.Request.Context(), id, req.Title)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if active, startedAt, timeoutSec := h.mcpServer.GetActiveWaiterInfo(session.ID); active {
		session.IsMCPActive = true
		session.MCPActiveAt = startedAt
		session.MCPTimeoutSec = timeoutSec
	}
	session.LastKeepaliveAt = h.mcpServer.GetLastKeepaliveTime(session.ID)

	h.broadcastSession("session_updated", session)
	c.JSON(http.StatusOK, gin.H{"status": "ok", "session": session})
}

func (h *APIHandler) UpdateSessionPromptWait(c *gin.Context) {
	id := c.Param("id")
	var req struct {
		WaitMinutes int `json:"wait_minutes"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.WaitMinutes <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "wait_minutes is required and must be > 0"})
		return
	}

	session, err := h.store.UpdateSessionPromptWait(c.Request.Context(), id, req.WaitMinutes)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if active, startedAt, timeoutSec := h.mcpServer.GetActiveWaiterInfo(session.ID); active {
		session.IsMCPActive = true
		session.MCPActiveAt = startedAt
		session.MCPTimeoutSec = timeoutSec
	}
	session.LastKeepaliveAt = h.mcpServer.GetLastKeepaliveTime(session.ID)

	h.broadcastSession("session_updated", session)
	c.JSON(http.StatusOK, gin.H{"status": "ok", "session": session})
}

func (h *APIHandler) UpdateSessionMaxChecks(c *gin.Context) {
	id := c.Param("id")
	var req struct {
		MaxChecks int `json:"max_checks"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid max_checks"})
		return
	}

	session, err := h.store.UpdateSessionMaxChecks(c.Request.Context(), id, req.MaxChecks)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if active, startedAt, timeoutSec := h.mcpServer.GetActiveWaiterInfo(session.ID); active {
		session.IsMCPActive = true
		session.MCPActiveAt = startedAt
		session.MCPTimeoutSec = timeoutSec
	}
	session.LastKeepaliveAt = h.mcpServer.GetLastKeepaliveTime(session.ID)

	h.broadcastSession("session_updated", session)
	c.JSON(http.StatusOK, gin.H{"status": "ok", "session": session})
}

func (h *APIHandler) UpdateSessionWaitCountdown(c *gin.Context) {
	id := c.Param("id")
	// countdown_seconds（秒级，#121）优先；只给 countdown_minutes 时走旧路径，行为不变。
	var req struct {
		CountdownMinutes *int `json:"countdown_minutes"`
		CountdownSeconds *int `json:"countdown_seconds"`
	}
	if err := c.ShouldBindJSON(&req); err != nil ||
		(req.CountdownMinutes == nil && req.CountdownSeconds == nil) ||
		(req.CountdownMinutes != nil && *req.CountdownMinutes < 0) ||
		(req.CountdownSeconds != nil && (*req.CountdownSeconds < 0 || *req.CountdownSeconds > 3600)) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid countdown_seconds / countdown_minutes"})
		return
	}

	var session *model.FeedbackSession
	var err error
	if req.CountdownSeconds != nil {
		session, err = h.store.UpdateSessionWaitCountdownSeconds(c.Request.Context(), id, *req.CountdownSeconds)
	} else {
		session, err = h.store.UpdateSessionWaitCountdown(c.Request.Context(), id, *req.CountdownMinutes)
	}
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if active, startedAt, timeoutSec := h.mcpServer.GetActiveWaiterInfo(session.ID); active {
		session.IsMCPActive = true
		session.MCPActiveAt = startedAt
		session.MCPTimeoutSec = timeoutSec
	}
	session.LastKeepaliveAt = h.mcpServer.GetLastKeepaliveTime(session.ID)

	h.broadcastSession("session_updated", session)
	c.JSON(http.StatusOK, gin.H{"status": "ok", "session": session})
}

// SessionPresence 是 Web 告诉服务端「用户还在」的入口（#121 收尾宽限）：
// kind=plus 点「+1 分钟」；kind=typing 是输入时的心跳。只对正挂着的长轮询生效，没有就返回 active=false。
func (h *APIHandler) SessionPresence(c *gin.Context) {
	id := c.Param("id")
	var req struct {
		Kind string `json:"kind"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || (req.Kind != "plus" && req.Kind != "typing") {
		c.JSON(http.StatusBadRequest, gin.H{"error": "kind 必须是 plus 或 typing"})
		return
	}
	if h.mcpServer == nil {
		c.JSON(http.StatusOK, gin.H{"status": "ok", "active": false})
		return
	}
	var total int
	var ok bool
	if req.Kind == "plus" {
		total, ok = h.mcpServer.PresencePlus(id)
	} else {
		total, ok = h.mcpServer.PresenceTyping(id)
	}
	c.JSON(http.StatusOK, gin.H{"status": "ok", "active": ok, "hold_seconds": total})
}

type TranscribeAudioRequest struct {
	AudioBase64 string `json:"audio_base64"`
	MimeType    string `json:"mime_type"`
	APIURL      string `json:"api_url"`
	APIKey      string `json:"api_key"`
	Model       string `json:"model"`
	Language    string `json:"language"`
	Stream      bool   `json:"stream"`
}

func (h *APIHandler) TranscribeAudio(c *gin.Context) {
	var req TranscribeAudioRequest
	if err := c.ShouldBindJSON(&req); err != nil || req.AudioBase64 == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "audio_base64 is required"})
		return
	}

	targetURL := req.APIURL
	if strings.TrimSpace(targetURL) == "" {
		targetURL = h.cfg.ASRAPIURL
	}
	if strings.TrimSpace(targetURL) == "" {
		targetURL = "https://api.xiaomimimo.com/v1/chat/completions"
	}

	apiKey := req.APIKey
	if strings.TrimSpace(apiKey) == "" {
		apiKey = h.cfg.ASRAPIKey
	}

	cleanKey := strings.Trim(strings.TrimSpace(apiKey), "\"' \t\r\n")
	if strings.HasPrefix(cleanKey, "Bearer ") {
		cleanKey = strings.TrimSpace(strings.TrimPrefix(cleanKey, "Bearer "))
	}

	if cleanKey == "" && (strings.Contains(targetURL, "xiaomimimo.com") || strings.Contains(targetURL, "api.openai.com")) {
		c.JSON(http.StatusBadRequest, gin.H{
			"error":   "未配置语音识别 API Key",
			"details": "请在前端「设置 > 语音 ASR」中填写 Xiaomi MIMO API Key 或在服务端配置 RELAYMESH_ASR_API_KEY 环境变量",
		})
		return
	}

	modelName := req.Model
	if strings.TrimSpace(modelName) == "" {
		modelName = h.cfg.ASRModel
	}
	if strings.TrimSpace(modelName) == "" {
		modelName = "mimo-v2.5-asr"
	}

	language := req.Language
	if strings.TrimSpace(language) == "" {
		language = "auto"
	}

	mimeType := req.MimeType
	if strings.TrimSpace(mimeType) == "" {
		mimeType = "audio/wav"
	}

	// Format audio data uri
	audioDataURI := req.AudioBase64
	if !strings.HasPrefix(audioDataURI, "data:") {
		audioDataURI = fmt.Sprintf("data:%s;base64,%s", mimeType, req.AudioBase64)
	}

	// Construct upstream payload matching Xiaomi MIMO / OpenAI compatible ASR format
	upstreamReqBody := map[string]interface{}{
		"model": modelName,
		"messages": []map[string]interface{}{
			{
				"role": "user",
				"content": []map[string]interface{}{
					{
						"type": "input_audio",
						"input_audio": map[string]interface{}{
							"data": audioDataURI,
						},
					},
				},
			},
		},
		"asr_options": map[string]interface{}{
			"language": language,
		},
		"stream": req.Stream,
	}

	reqBytes, err := json.Marshal(upstreamReqBody)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to marshal payload: " + err.Error()})
		return
	}

	httpReq, err := http.NewRequestWithContext(c.Request.Context(), "POST", targetURL, bytes.NewReader(reqBytes))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to create http request: " + err.Error()})
		return
	}

	httpReq.Header.Set("Content-Type", "application/json")
	if cleanKey != "" {
		httpReq.Header.Set("api-key", cleanKey)
		httpReq.Header.Set("Authorization", "Bearer "+cleanKey)
	}

	client := &http.Client{
		Timeout: 60 * time.Second,
	}

	resp, err := client.Do(httpReq)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "upstream request failed: " + err.Error()})
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		respBody, _ := io.ReadAll(resp.Body)
		c.JSON(resp.StatusCode, gin.H{
			"error":   fmt.Sprintf("upstream returned status %d", resp.StatusCode),
			"details": string(respBody),
		})
		return
	}

	if req.Stream {
		c.Writer.Header().Set("Content-Type", "text/event-stream")
		c.Writer.Header().Set("Cache-Control", "no-cache")
		c.Writer.Header().Set("Connection", "keep-alive")
		c.Writer.Header().Set("X-Accel-Buffering", "no")
		c.Writer.WriteHeader(http.StatusOK)
		c.Writer.Flush()

		reader := bufio.NewReader(resp.Body)
		for {
			select {
			case <-c.Request.Context().Done():
				return
			default:
				line, err := reader.ReadBytes('\n')
				if len(line) > 0 {
					_, _ = c.Writer.Write(line)
					c.Writer.Flush()
				}
				if err != nil {
					return
				}
			}
		}
	} else {
		respBody, err := io.ReadAll(resp.Body)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to read upstream response: " + err.Error()})
			return
		}
		c.Data(resp.StatusCode, "application/json", respBody)
	}
}

func (h *APIHandler) UnarchiveSession(c *gin.Context) {
	id := c.Param("id")
	session, err := h.store.UnarchiveFeedbackSession(c.Request.Context(), id)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	h.broadcastSession("session_unarchived", session)
	c.JSON(http.StatusOK, gin.H{"status": "ok", "session": session})
}

// Task APIs
func (h *APIHandler) ListTasks(c *gin.Context) {
	state := c.Query("state")
	cursor := c.Query("cursor")
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "20"))
	updatesOnly := c.Query("updates_only") == "true"

	page, err := h.store.ListTasks(c.Request.Context(), h.cfg.ProjectID, state, cursor, limit, updatesOnly)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, page)
}

type CreateTaskRequest struct {
	TaskID     string            `json:"task_id,omitempty"`
	Title      string            `json:"title,omitempty"`
	Mode       string            `json:"mode,omitempty"`
	Stages     model.TaskStages  `json:"stages,omitempty"`
	Segments   []model.Segment   `json:"segments,omitempty"`
	WaitPolicy *model.WaitPolicy `json:"wait_policy,omitempty"`
}

func (h *APIHandler) CreateTask(c *gin.Context) {
	var req CreateTaskRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}

	policy := model.WaitPolicy{
		AfterMinutes:        2,
		MaxNoFeedbackChecks: 24,
		WaitInstruction:     "暂无新反馈；请等待 {minutes} 分钟后再次调用。",
	}
	if req.WaitPolicy != nil {
		if req.WaitPolicy.AfterMinutes > 0 {
			policy.AfterMinutes = req.WaitPolicy.AfterMinutes
		}
		if req.WaitPolicy.MaxNoFeedbackChecks > 0 {
			policy.MaxNoFeedbackChecks = req.WaitPolicy.MaxNoFeedbackChecks
		}
		if req.WaitPolicy.WaitInstruction != "" {
			policy.WaitInstruction = req.WaitPolicy.WaitInstruction
		}
	}

	// 默认提供至少一个基础分段
	segments := req.Segments
	if len(segments) == 0 {
		segments = []model.Segment{
			{
				Name:    "01_任务规划与目标",
				Content: "请指挥端在此分段定义任务目标、架构规范与执行约束。",
			},
		}
	}

	task, err := h.store.CreateTask(c.Request.Context(), model.CreateTaskInput{
		ProjectID:  h.cfg.ProjectID,
		TaskID:     req.TaskID,
		Title:      req.Title,
		Mode:       req.Mode,
		Stages:     req.Stages,
		Segments:   segments,
		WaitPolicy: policy,
	})
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	h.broker.Broadcast("task_update", gin.H{"task_id": task.ID})
	c.JSON(http.StatusOK, gin.H{"status": "ok", "task": task})
}

func (h *APIHandler) GetTask(c *gin.Context) {
	id := c.Param("id")
	task, err := h.store.GetTask(c.Request.Context(), h.cfg.ProjectID, id)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"task": task})
}

type UpdateTaskStagesRequest struct {
	ExpectedRevision int64            `json:"expected_revision,omitempty"`
	CurrentStageID   string           `json:"current_stage_id,omitempty"`
	Stages           model.TaskStages `json:"stages"`
}

func (h *APIHandler) UpdateTaskStages(c *gin.Context) {
	id := c.Param("id")
	var req UpdateTaskStagesRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}

	result, err := h.store.UpdateStages(c.Request.Context(), model.UpdateStagesInput{
		ProjectID:        h.cfg.ProjectID,
		TaskID:           id,
		ExpectedRevision: req.ExpectedRevision,
		CurrentStageID:   req.CurrentStageID,
		Stages:           req.Stages,
	})
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	h.broker.Broadcast("task_update", gin.H{"task_id": id})
	c.JSON(http.StatusOK, gin.H{"status": "ok", "result": result})
}

func (h *APIHandler) ReadReports(c *gin.Context) {
	id := c.Param("id")
	afterSeq, _ := strconv.ParseInt(c.DefaultQuery("after_sequence", "0"), 10, 64)
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "50"))

	reports, err := h.store.ReadReports(c.Request.Context(), h.cfg.ProjectID, id, afterSeq, limit)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, reports)
}

func (h *APIHandler) ReadTaskFeedbacks(c *gin.Context) {
	id := c.Param("id")
	afterSeq, _ := strconv.ParseInt(c.DefaultQuery("after_sequence", "0"), 10, 64)
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "100"))

	feedbacks, err := h.store.ReadFeedbacks(c.Request.Context(), h.cfg.ProjectID, id, afterSeq, limit)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"feedbacks": feedbacks})
}

type SendTaskFeedbackRequest struct {
	Body             string                `json:"body"`
	ExpectedRevision int64                 `json:"expected_revision"`
	Source           string                `json:"source,omitempty"`
	References       []model.PathReference `json:"references"`
}

func (h *APIHandler) SendTaskFeedback(c *gin.Context) {
	id := c.Param("id")
	var req SendTaskFeedbackRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}

	source := req.Source
	if source == "" {
		source = "human"
	}

	fb, err := h.store.SendFeedback(c.Request.Context(), model.SendFeedbackInput{
		ProjectID:        h.cfg.ProjectID,
		TaskID:           id,
		ExpectedRevision: req.ExpectedRevision,
		Source:           source,
		Body:             req.Body,
		References:       req.References,
	})
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	h.broker.Broadcast("task_feedback", fb)
	c.JSON(http.StatusOK, gin.H{"status": "ok", "feedback": fb})
}

type AckReportsRequest struct {
	ThroughSequence int64 `json:"through_sequence"`
}

func (h *APIHandler) AckReports(c *gin.Context) {
	id := c.Param("id")
	var req AckReportsRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}

	summary, err := h.store.AckReports(c.Request.Context(), h.cfg.ProjectID, id, req.ThroughSequence)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	h.broker.Broadcast("task_ack", summary)
	c.JSON(http.StatusOK, gin.H{"status": "ok", "summary": summary})
}

// Settings APIs
func (h *APIHandler) GetSettings(c *gin.Context) {
	settings, err := h.store.GetSettings(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"settings": settings, "version": h.cfg.Version})
}

func (h *APIHandler) UpdateSettings(c *gin.Context) {
	var payload map[string]any
	if err := c.ShouldBindJSON(&payload); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}

	if err := h.store.SaveSettings(c.Request.Context(), payload); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	h.broker.Broadcast("settings_updated", payload)
	c.JSON(http.StatusOK, gin.H{"status": "ok", "settings": payload})
}

// ─── User Norms (Skills) ───

func (h *APIHandler) ListNorms(c *gin.Context) {
	norms, err := h.store.ListUserNorms(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"norms": norms})
}

func (h *APIHandler) GetNorm(c *gin.Context) {
	name := c.Param("name")
	norm, err := h.store.GetUserNorm(c.Request.Context(), name)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"norm": norm})
}

func (h *APIHandler) CreateNorm(c *gin.Context) {
	var req model.UserNorm
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}

	if err := h.store.CreateUserNorm(c.Request.Context(), &req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	h.broker.Broadcast("norms_updated", gin.H{"name": req.Name, "action": "create"})
	c.JSON(http.StatusOK, gin.H{"status": "ok", "norm": req})
}

func (h *APIHandler) UpdateNorm(c *gin.Context) {
	name := c.Param("name")
	var body map[string]any
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}

	norm, err := h.store.UpdateUserNorm(c.Request.Context(), name, body)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	h.broker.Broadcast("norms_updated", gin.H{"name": norm.Name, "action": "update"})
	c.JSON(http.StatusOK, gin.H{"status": "ok", "norm": norm})
}

func (h *APIHandler) DeleteNorm(c *gin.Context) {
	name := c.Param("name")
	if err := h.store.DeleteUserNorm(c.Request.Context(), name); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	h.broker.Broadcast("norms_updated", gin.H{"name": name, "action": "delete"})
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

// ─── MCP Credentials ───

func (h *APIHandler) ListCredentials(c *gin.Context) {
	if h.cfg.MCPToken != "" {
		_, _ = h.store.EnsureEnvMCPCredential(c.Request.Context(), h.cfg.MCPToken)
	}

	creds, err := h.store.ListVisibleCredentials(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	// Mask tokens for list response
	type maskedCred struct {
		ID          uint              `json:"id"`
		Name        string            `json:"name"`
		Token       string            `json:"token"`
		HostName    string            `json:"host_name"`
		IsActive    bool              `json:"is_active"`
		Permissions model.Permissions `json:"permissions"`
		Note        string            `json:"note"`
		IsEnv       bool              `json:"is_env"`
		CreatedAt   string            `json:"created_at"`
		UpdatedAt   string            `json:"updated_at"`

		// 档位由权限推导（relaymesh / execmesh / hybrid / none），不落库。
		Tier string `json:"tier"`
		// 是否是能力 URL 凭据（令牌是网址的一部分，只在 /mcp/c/<令牌> 上有效）。
		Capability bool `json:"capability"`
		// 令牌强度不足的原因（空串表示合格）；带终端权限的令牌必须合格。
		TokenWeakness string `json:"token_weakness"`

		TerminalHosts   []string             `json:"terminal_hosts"`
		TerminalModeCap string               `json:"terminal_mode_cap"`
		TerminalLimits  model.TerminalLimits `json:"terminal_limits"`
		ExpiresAt       *string              `json:"expires_at"`
		AllowedCIDRs    []string             `json:"allowed_cidrs"`
		LastUsedAt      *string              `json:"last_used_at"`
		LastUsedIP      string               `json:"last_used_ip"`
	}
	formatTime := func(t *time.Time) *string {
		if t == nil {
			return nil
		}
		s := t.Format(time.RFC3339)
		return &s
	}
	nonNil := func(l model.StringList) []string {
		if l == nil {
			return []string{}
		}
		return []string(l)
	}
	result := make([]maskedCred, 0, len(creds))
	for _, cr := range creds {
		isEnv := (h.cfg.MCPToken != "" && cr.Token == h.cfg.MCPToken) || cr.Name == "MCP Token (环境变量)"
		result = append(result, maskedCred{
			ID:              cr.ID,
			Name:            cr.Name,
			Token:           store.MaskToken(cr.Token),
			HostName:        cr.HostName,
			IsActive:        cr.IsActive,
			Permissions:     cr.Permissions,
			Note:            cr.Note,
			IsEnv:           isEnv,
			CreatedAt:       cr.CreatedAt.Format(time.RFC3339),
			UpdatedAt:       cr.UpdatedAt.Format(time.RFC3339),
			Tier:            mcp.TierForPermissions(cr.Permissions),
			Capability:      cr.Capability,
			TokenWeakness:   config.MCPTokenWeakness(cr.Token),
			TerminalHosts:   nonNil(cr.TerminalHosts),
			TerminalModeCap: cr.TerminalModeCap,
			TerminalLimits:  cr.TerminalLimits,
			ExpiresAt:       formatTime(cr.ExpiresAt),
			AllowedCIDRs:    nonNil(cr.AllowedCIDRs),
			LastUsedAt:      formatTime(cr.LastUsedAt),
			LastUsedIP:      cr.LastUsedIP,
		})
	}

	c.JSON(http.StatusOK, gin.H{"credentials": result, "env_credentials": []gin.H{}})
}

func (h *APIHandler) GetCredential(c *gin.Context) {
	idStr := c.Param("id")
	id, err := strconv.ParseUint(idStr, 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid credential id"})
		return
	}
	cred, err := h.store.GetCredential(c.Request.Context(), uint(id))
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
		return
	}
	if cred.Hidden {
		c.JSON(http.StatusNotFound, gin.H{"error": "credential not found"})
		return
	}
	cred.Token = store.MaskToken(cred.Token)
	c.JSON(http.StatusOK, gin.H{"credential": cred})
}

func (h *APIHandler) CreateCredential(c *gin.Context) {
	var req model.MCPCredential
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}
	if req.Hidden || req.OAuthSource != "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "不能手动创建 OAuth 隐藏凭据"})
		return
	}

	if carriesTerminal(&req) && !h.requireTerminalUnlock(c) {
		return
	}
	// 一把新令牌直接带着「允许整个文件系统」：同样要求刚刚重新输入过密码（见 requireFreshTerminalUnlock）。
	wholeFS := model.EffectiveWholeFS(req.Permissions, req.TerminalLimits)
	if wholeFS && !h.requireFreshTerminalUnlock(c, "新建令牌「"+req.Name+"」：开启「允许整个文件系统」") {
		return
	}

	// 新建带终端能力的令牌且没有显式给出主机名单（字段缺省 / null，而不是明确的 []）时，
	// 按各主机的「对新模型默认授予」预填名单。预填的是明确的机器码条目，之后与手工配置的没有区别。
	if (req.Permissions.Terminal || req.Permissions.TerminalFiles) && !req.Capability && req.TerminalHosts == nil {
		if codes, err := h.store.DefaultAllowHostCodes(c.Request.Context()); err == nil && len(codes) > 0 {
			req.TerminalHosts = model.StringList(codes)
		}
	}

	if err := h.store.CreateCredential(c.Request.Context(), &req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	fullToken := req.Token
	if wholeFS {
		h.terminal.FilesScopeChanged(c.GetString("auth_subject"), c.ClientIP(), terminal.ResultOK, wholeFSLabel(&req)+"：开启「允许整个文件系统」（新建）")
	}
	h.broker.Broadcast("credentials_updated", gin.H{"id": req.ID, "action": "create"})
	resp := gin.H{"status": "ok", "credential": req, "token": fullToken}
	if req.Capability {
		// 完整网址只在创建时给出一次（列表里的令牌是打码的）。
		resp["capability_path"] = capabilityPathFor(fullToken)
	}
	c.JSON(http.StatusOK, resp)
}

func (h *APIHandler) UpdateCredential(c *gin.Context) {
	idStr := c.Param("id")
	id, err := strconv.ParseUint(idStr, 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid credential id"})
		return
	}

	var body map[string]any
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}
	delete(body, "id")
	delete(body, "token")
	delete(body, "created_at")

	// 授予或放宽终端能力（等同于在远端机器上执行命令）需要二次解锁；停用 / 清空主机授权等收紧操作只需登录。
	before, err := h.store.GetCredentialIfVisible(c.Request.Context(), uint(id))
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "credential not found"})
		return
	}
	if credentialUpdateNeedsUnlock(before, body) && !h.requireTerminalUnlock(c) {
		return
	}
	// 让令牌从「没有」变成「有」整个文件系统的访问：还要求刚刚重新输入过密码；关闭、收窄不需要（刹车只需登录）。
	if credentialUpdateGrantsWholeFS(before, body) && !h.requireFreshTerminalUnlock(c, wholeFSSubject(before, uint(id))) {
		return
	}

	// permissions 需特殊处理：从 map 转为 Permissions struct
	if permsRaw, ok := body["permissions"]; ok {
		permsBytes, err := json.Marshal(permsRaw)
		if err == nil {
			var perms model.Permissions
			if json.Unmarshal(permsBytes, &perms) == nil {
				body["permissions"] = perms
			}
		}
	}

	cred, err := h.store.UpdateCredential(c.Request.Context(), uint(id), body)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	cred.Token = store.MaskToken(cred.Token)

	// 「允许整个文件系统」是否生效发生了变化（开启或关闭）：写审计，开启与关闭都留痕。
	if was, now := wholeFSEffective(before), model.EffectiveWholeFS(cred.Permissions, cred.TerminalLimits); was != now {
		action := "关闭"
		if now {
			action = "开启"
		}
		h.terminal.FilesScopeChanged(c.GetString("auth_subject"), c.ClientIP(), terminal.ResultOK, wholeFSLabel(cred)+"："+action+"「允许整个文件系统」")
	}
	h.terminal.MCPCredentialChanged(cred.ID, "updated")
	h.broker.Broadcast("credentials_updated", gin.H{"id": cred.ID, "action": "update"})
	h.broker.Broadcast("session_updated", gin.H{"action": "credentials_updated"})
	c.JSON(http.StatusOK, gin.H{"status": "ok", "credential": cred})
}

func (h *APIHandler) DeleteCredential(c *gin.Context) {
	idStr := c.Param("id")
	id, err := strconv.ParseUint(idStr, 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid credential id"})
		return
	}

	targetCred, err := h.store.GetCredentialIfVisible(c.Request.Context(), uint(id))
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "credential not found"})
		return
	}
	if (h.cfg.MCPToken != "" && targetCred.Token == h.cfg.MCPToken) || targetCred.Name == "MCP Token (环境变量)" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "该凭据来自环境变量 RELAYMESH_MCP_TOKEN，不可删除；如需停用请直接点击禁用开关"})
		return
	}

	if err := h.store.DeleteCredential(c.Request.Context(), uint(id)); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	h.terminal.MCPCredentialChanged(uint(id), "deleted")
	h.broker.Broadcast("credentials_updated", gin.H{"id": id, "action": "delete"})
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

func (h *APIHandler) RegenerateCredentialToken(c *gin.Context) {
	idStr := c.Param("id")
	id, err := strconv.ParseUint(idStr, 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid credential id"})
		return
	}

	targetCred, err := h.store.GetCredentialIfVisible(c.Request.Context(), uint(id))
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "credential not found"})
		return
	}
	if (h.cfg.MCPToken != "" && targetCred.Token == h.cfg.MCPToken) || targetCred.Name == "MCP Token (环境变量)" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "该凭据由环境变量 RELAYMESH_MCP_TOKEN 驱动，不可重新生成 Token"})
		return
	}

	// 重新生成会把新的完整令牌返回给调用者；带终端能力的令牌，取走它等同于取走一把执行命令的钥匙。
	if carriesTerminal(targetCred) && !h.requireTerminalUnlock(c) {
		return
	}

	cred, fullToken, err := h.store.RegenerateCredentialToken(c.Request.Context(), uint(id))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	cred.Token = store.MaskToken(cred.Token)

	h.terminal.MCPCredentialChanged(cred.ID, "regenerated")
	h.broker.Broadcast("credentials_updated", gin.H{"id": cred.ID, "action": "regenerate"})
	resp := gin.H{"status": "ok", "credential": cred, "token": fullToken}
	if cred.Capability {
		resp["capability_path"] = capabilityPathFor(fullToken)
	}
	c.JSON(http.StatusOK, resp)
}

// capabilityPathFor 返回能力 URL 凭据的网址路径（不含协议与主机，由前端按当前访问地址补全）。
func capabilityPathFor(token string) string {
	return "/mcp/c/" + token
}

func (h *APIHandler) GetWorkflowPhase(c *gin.Context) {
	workflowID := c.Param("workflow_id")
	if workflowID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "workflow_id is required"})
		return
	}
	currentPhase, phases, err := h.store.GetWorkflowPhaseWithDefaults(c.Request.Context(), workflowID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	defaults := model.DefaultPhaseTemplate()
	defaultPrompts := make(map[string]string, len(defaults))
	for _, d := range defaults {
		if d.Prompt != "" {
			defaultPrompts[d.ID] = d.Prompt
		}
	}
	c.JSON(http.StatusOK, gin.H{
		"workflow_id":      workflowID,
		"current_phase_id": currentPhase,
		"phases":           phases,
		"default_prompts":  defaultPrompts,
	})
}

func (h *APIHandler) SetWorkflowPhase(c *gin.Context) {
	workflowID := c.Param("workflow_id")
	if workflowID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "workflow_id is required"})
		return
	}
	var body struct {
		PhaseID string            `json:"phase_id"`
		Source  string            `json:"source,omitempty"`
		Phases  []model.PhaseItem `json:"phases,omitempty"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if body.Phases != nil {
		if err := h.store.SetWorkflowPhaseConfig(c.Request.Context(), workflowID, body.Phases); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
	}

	isHumanClick := body.Source == "human"
	if body.PhaseID != "" {
		if err := h.store.SetWorkflowPhase(c.Request.Context(), workflowID, body.PhaseID, isHumanClick); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
	}

	h.broker.Broadcast("phase_changed", gin.H{"workflow_id": workflowID, "phase": model.CanonicalPhaseID(body.PhaseID)})

	currentPhase, phases, _ := h.store.GetWorkflowPhaseWithDefaults(c.Request.Context(), workflowID)
	c.JSON(http.StatusOK, gin.H{
		"workflow_id":      workflowID,
		"current_phase_id": currentPhase,
		"phases":           phases,
	})
}

func (h *APIHandler) GetWorkflowSheet(c *gin.Context) {
	workflowID := c.Param("workflow_id")
	if workflowID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "workflow_id is required"})
		return
	}
	note, err := h.store.GetNoteByKey(c.Request.Context(), workflowID, "session_doc")
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if note == nil {
		c.JSON(http.StatusOK, gin.H{
			"workflow_id": workflowID,
			"content":     "",
			"updated_at":  nil,
		})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"workflow_id": workflowID,
		"content":     note.Content,
		"updated_at":  note.UpdatedAt,
	})
}

type SaveWorkflowSheetRequest struct {
	Content string `json:"content"`
}

func (h *APIHandler) SaveWorkflowSheet(c *gin.Context) {
	workflowID := c.Param("workflow_id")
	if workflowID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "workflow_id is required"})
		return
	}
	var req SaveWorkflowSheetRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}
	note := &model.WorkflowNote{
		WorkflowID: workflowID,
		NoteKey:    "session_doc",
		Content:    req.Content,
		CreatedAt:  time.Now(),
		UpdatedAt:  time.Now(),
	}
	if err := h.store.SaveNote(c.Request.Context(), note); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	h.broker.Broadcast("workflow_sheet_updated", gin.H{"workflow_id": workflowID})
	c.JSON(http.StatusOK, gin.H{"status": "ok", "workflow_id": workflowID})
}
