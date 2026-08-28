package api

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/teacat99/RelayMesh/internal/config"
	"github.com/teacat99/RelayMesh/internal/mcp"
	"github.com/teacat99/RelayMesh/internal/model"
	"github.com/teacat99/RelayMesh/internal/store"
)

type APIHandler struct {
	cfg       *config.Config
	store     *store.Store
	mcpServer *mcp.Server
	broker    *SSEBroker
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
	c.JSON(http.StatusOK, gin.H{"has_session": true, "session": session})
}

func (h *APIHandler) ListSessions(c *gin.Context) {
	projectDir := c.Query("project_directory")
	status := c.Query("status")
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "200"))

	sessions, err := h.store.ListFeedbackSessions(c.Request.Context(), projectDir, status, limit)
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
	}
	c.JSON(http.StatusOK, gin.H{"sessions": sessions})
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
	c.JSON(http.StatusOK, gin.H{"session": session})
}

type SubmitFeedbackRequest struct {
	ResponseText string              `json:"response_text"`
	UserMessages []string            `json:"user_messages"`
	Images       []model.SessionImage `json:"images"`
}

func (h *APIHandler) SubmitFeedback(c *gin.Context) {
	id := c.Param("id")
	var req SubmitFeedbackRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}

	session, err := h.store.SubmitFeedback(c.Request.Context(), store.SubmitFeedbackInput{
		SessionID:    id,
		ResponseText: req.ResponseText,
		UserMessages: req.UserMessages,
		Images:       req.Images,
	})
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// Wake up MCP waiter & broadcast SSE
	h.mcpServer.NotifySessionCompleted(session)
	h.broker.Broadcast("session_completed", session)

	c.JSON(http.StatusOK, gin.H{"status": "ok", "session": session})
}

func (h *APIHandler) RevokeSession(c *gin.Context) {
	id := c.Param("id")
	res, session, err := h.store.RevokeSessionFeedback(c.Request.Context(), id)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	h.broker.Broadcast("session_updated", session)
	c.JSON(http.StatusOK, gin.H{"status": "ok", "revoked": res, "session": session})
}

func (h *APIHandler) AppendWorkflowFeedback(c *gin.Context) {
	workflowID := c.Param("workflow_id")
	var req struct {
		ResponseText string              `json:"response_text"`
		UserMessages []string            `json:"user_messages"`
		Images       []model.SessionImage `json:"images"`
		HostName     string              `json:"host_name"`
		ProjectDir   string              `json:"project_directory"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// 1. 优先检查该 workflow 是否有处于 pending 或 未被 AI 消费的 completed 会话
	latestSess, err := h.store.GetLatestWorkflowFeedbackSession(c.Request.Context(), workflowID)
	if err == nil && latestSess != nil && (latestSess.Status == "pending" || (latestSess.Status == "completed" && !latestSess.ConsumedByAI)) {
		sess, err := h.store.SubmitFeedback(c.Request.Context(), store.SubmitFeedbackInput{
			SessionID:    latestSess.ID,
			ResponseText: req.ResponseText,
			UserMessages: req.UserMessages,
			Images:       req.Images,
		})
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		h.mcpServer.NotifySessionCompleted(sess)
		h.broker.Broadcast("session_completed", sess)
		c.JSON(http.StatusOK, gin.H{"status": "ok", "type": "session", "session": sess})
		return
	}

	// 2. 若当前无未消费会话，存入 QueuedFeedback 等待 AI 下次发起交互直接秒回
	q, err := h.store.QueueWorkflowFeedback(c.Request.Context(), store.QueueFeedbackInput{
		WorkflowID:       workflowID,
		HostName:         req.HostName,
		ProjectDirectory: req.ProjectDir,
		ResponseText:     req.ResponseText,
		UserMessages:     req.UserMessages,
		Images:           req.Images,
	})
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	h.broker.Broadcast("queued_feedback_updated", q)
	c.JSON(http.StatusOK, gin.H{"status": "ok", "type": "queued", "queued": q})
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

	h.broker.Broadcast("queued_feedback_revoked", gin.H{"id": id, "workflow_id": res.WorkflowID})
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
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

func (h *APIHandler) CancelSession(c *gin.Context) {
	id := c.Param("id")
	session, err := h.store.CancelFeedbackSession(c.Request.Context(), id)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	h.mcpServer.NotifySessionCompleted(session)
	h.broker.Broadcast("session_cancelled", session)

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

	h.broker.Broadcast("session_keepalive", session)
	c.JSON(http.StatusOK, gin.H{"status": "ok", "session": session})
}

func (h *APIHandler) ArchiveSession(c *gin.Context) {
	id := c.Param("id")
	session, err := h.store.ArchiveFeedbackSession(c.Request.Context(), id)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	h.broker.Broadcast("session_archived", session)
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

	h.broker.Broadcast("session_updated", session)
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

	h.broker.Broadcast("session_updated", session)
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

	h.broker.Broadcast("session_updated", session)
	c.JSON(http.StatusOK, gin.H{"status": "ok", "session": session})
}

func (h *APIHandler) UpdateSessionWaitCountdown(c *gin.Context) {
	id := c.Param("id")
	var req struct {
		CountdownMinutes int `json:"countdown_minutes"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.CountdownMinutes < 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid countdown_minutes"})
		return
	}

	session, err := h.store.UpdateSessionWaitCountdown(c.Request.Context(), id, req.CountdownMinutes)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	h.broker.Broadcast("session_updated", session)
	c.JSON(http.StatusOK, gin.H{"status": "ok", "session": session})
}

func (h *APIHandler) UpdateSessionUserPresence(c *gin.Context) {
	id := c.Param("id")
	var req struct {
		UserPresence string `json:"user_presence"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.UserPresence == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "user_presence is required"})
		return
	}

	session, err := h.store.UpdateSessionUserPresence(c.Request.Context(), id, req.UserPresence)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	h.broker.Broadcast("session_updated", session)
	c.JSON(http.StatusOK, gin.H{"status": "ok", "session": session})
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

	h.broker.Broadcast("session_unarchived", session)
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

func (h *APIHandler) GetTask(c *gin.Context) {
	id := c.Param("id")
	task, err := h.store.GetTask(c.Request.Context(), h.cfg.ProjectID, id)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"task": task})
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

type SendTaskFeedbackRequest struct {
	Body             string                `json:"body"`
	ExpectedRevision int64                 `json:"expected_revision"`
	References       []model.PathReference `json:"references"`
}

func (h *APIHandler) SendTaskFeedback(c *gin.Context) {
	id := c.Param("id")
	var req SendTaskFeedbackRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}

	fb, err := h.store.SendFeedback(c.Request.Context(), model.SendFeedbackInput{
		ProjectID:        h.cfg.ProjectID,
		TaskID:           id,
		ExpectedRevision: req.ExpectedRevision,
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
	c.JSON(http.StatusOK, gin.H{"settings": settings})
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
