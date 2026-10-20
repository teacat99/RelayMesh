package api

import (
	"errors"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/teacat99/RelayMesh/v2/internal/chat"
	"github.com/teacat99/RelayMesh/v2/internal/model"
	"github.com/teacat99/RelayMesh/v2/internal/store"
)

// 群聊 REST（#117，设计稿 03）。所有写操作都是你（登录用户）做的：建群、拉人、移人、改设置、归档都只有这里的入口，
// MCP 的 chat 工具里没有——「建群 / 拉人只有人能做」靠入口本身守住，而不是靠参数校验。

// groupError 把群聊规则错误翻成 HTTP 状态；不是群聊错误时返回 false。
func writeGroupError(c *gin.Context, err error) bool {
	var ge *store.GroupError
	if !errors.As(err, &ge) {
		return false
	}
	status := http.StatusBadRequest
	switch ge.Code {
	case "not_member":
		status = http.StatusForbidden
	case "rate_limited":
		status = http.StatusTooManyRequests
	case "too_large":
		status = http.StatusRequestEntityTooLarge
	case "group_archived", "hop_limit":
		status = http.StatusConflict
	}
	c.JSON(status, gin.H{"error": ge.Message, "code": ge.Code})
	return true
}

func writeGroupAPIError(c *gin.Context, err error) {
	if writeGroupError(c, err) {
		return
	}
	var ae *store.AppError
	if errors.As(err, &ae) {
		switch ae.Kind {
		case store.ErrNotFound:
			c.JSON(http.StatusNotFound, gin.H{"error": ae.Message})
			return
		case store.ErrInvalidInput:
			c.JSON(http.StatusBadRequest, gin.H{"error": ae.Message})
			return
		case store.ErrConflict:
			c.JSON(http.StatusConflict, gin.H{"error": ae.Message})
			return
		}
	}
	c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
}

func (h *APIHandler) touchGroup(groupID string) {
	if h.broker != nil {
		h.broker.Broadcast("conversation_changed", gin.H{"group_id": groupID})
	}
}

// ListGroups GET /api/v1/groups?archived=1
func (h *APIHandler) ListGroups(c *gin.Context) {
	list, err := h.chatService().GroupSummaries(c.Request.Context(), c.Query("archived") == "1")
	if err != nil {
		writeGroupAPIError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"groups": list})
}

// GroupCandidate 是建群时可以选的工作流。
type GroupCandidate struct {
	WorkflowID string `json:"workflow_id"`
	Name       string `json:"name"`
	ProjectDir string `json:"project_directory,omitempty"`
	HostName   string `json:"host_name,omitempty"`
	State      string `json:"state,omitempty"`
	// Eligible 表示它处于聊天模式（G-1）；不满足时 Reason 说明为什么不能加入。
	Eligible bool   `json:"eligible"`
	Reason   string `json:"reason,omitempty"`
}

// groupEligibility 判断一个工作流能不能进群：必须处于聊天模式（异步开启且 hook 已验证）。
func (h *APIHandler) groupEligibility(c *gin.Context, workflowID string) (bool, string) {
	info, err := h.store.WorkflowModeInfo(c.Request.Context(), workflowID, time.Now())
	if err != nil {
		return false, err.Error()
	}
	if info.State.Effective {
		return true, ""
	}
	if !info.State.Enabled {
		return false, "这个工作流没有开启异步对话（聊天）模式"
	}
	return false, "这个工作流还没有装好 hook 或 hook 已过期，聊天模式没有生效"
}

// GroupCandidates GET /api/v1/groups/candidates：列出可以拉进群的工作流（最近有动静的在前）。
func (h *APIHandler) GroupCandidates(c *gin.Context) {
	ctx := c.Request.Context()
	views, err := h.store.ListAgentSessions(ctx, store.AgentSessionFilter{Limit: 200}, time.Now())
	if err != nil {
		writeGroupAPIError(c, err)
		return
	}
	seen := map[string]bool{}
	out := []GroupCandidate{}
	for _, v := range views {
		wf := strings.TrimSpace(v.WorkflowID)
		if wf == "" || seen[wf] {
			continue
		}
		seen[wf] = true
		ok, reason := h.groupEligibility(c, wf)
		out = append(out, GroupCandidate{WorkflowID: wf, Name: v.IdentityLabel, ProjectDir: v.ProjectDir, HostName: v.HostName, State: v.State, Eligible: ok, Reason: reason})
	}
	// 只发过汇报、没有 Agent 状态会话的工作流（旧版 hook / 纯 MCP 接入）也要能选：名字退回项目目录名。
	briefs, err := h.store.RecentWorkflows(ctx, 200)
	if err != nil {
		writeGroupAPIError(c, err)
		return
	}
	for _, b := range briefs {
		wf := strings.TrimSpace(b.WorkflowID)
		if wf == "" || seen[wf] {
			continue
		}
		seen[wf] = true
		ok, reason := h.groupEligibility(c, wf)
		name := filepath.Base(strings.TrimRight(b.ProjectDir, "/"))
		if name == "." || name == "/" || name == "" {
			name = wf
		}
		out = append(out, GroupCandidate{WorkflowID: wf, Name: name, ProjectDir: b.ProjectDir, HostName: b.HostName, Eligible: ok, Reason: reason})
	}
	c.JSON(http.StatusOK, gin.H{"candidates": out})
}

// CreateGroup POST /api/v1/groups  {title, project_id?, workflow_ids[]}
func (h *APIHandler) CreateGroup(c *gin.Context) {
	var req struct {
		Title       string   `json:"title"`
		ProjectID   string   `json:"project_id"`
		WorkflowIDs []string `json:"workflow_ids"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	for _, wf := range req.WorkflowIDs {
		if ok, reason := h.groupEligibility(c, strings.TrimSpace(wf)); !ok {
			c.JSON(http.StatusBadRequest, gin.H{"error": "无法加入 " + wf + "：" + reason, "code": "not_chat_mode"})
			return
		}
	}
	g, err := h.store.CreateGroup(c.Request.Context(), store.CreateGroupInput{Title: req.Title, ProjectID: req.ProjectID, CreatedBy: store.GroupHumanRef, Members: req.WorkflowIDs})
	if err != nil {
		writeGroupAPIError(c, err)
		return
	}
	h.touchGroup(g.ID)
	c.JSON(http.StatusOK, gin.H{"status": "ok", "group": g})
}

// GetGroup GET /api/v1/groups/:id
func (h *APIHandler) GetGroup(c *gin.Context) {
	info, err := h.chatService().GroupInfoOf(c.Request.Context(), c.Param("id"))
	if err != nil {
		writeGroupAPIError(c, err)
		return
	}
	c.JSON(http.StatusOK, info)
}

// UpdateGroup PUT /api/v1/groups/:id  {title?, project_id?, archived?}
func (h *APIHandler) UpdateGroup(c *gin.Context) {
	var req struct {
		Title     *string `json:"title"`
		ProjectID *string `json:"project_id"`
		Archived  *bool   `json:"archived"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	g, err := h.store.UpdateGroup(c.Request.Context(), c.Param("id"), store.UpdateGroupInput{Title: req.Title, ProjectID: req.ProjectID, Archived: req.Archived})
	if err != nil {
		writeGroupAPIError(c, err)
		return
	}
	h.touchGroup(g.ID)
	c.JSON(http.StatusOK, gin.H{"status": "ok", "group": g})
}

// AddGroupMembers POST /api/v1/groups/:id/members  {workflow_ids[]}
func (h *APIHandler) AddGroupMembers(c *gin.Context) {
	var req struct {
		WorkflowIDs []string `json:"workflow_ids"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	for _, wf := range req.WorkflowIDs {
		if ok, reason := h.groupEligibility(c, strings.TrimSpace(wf)); !ok {
			c.JSON(http.StatusBadRequest, gin.H{"error": "无法加入 " + wf + "：" + reason, "code": "not_chat_mode"})
			return
		}
	}
	if err := h.store.AddGroupMembers(c.Request.Context(), c.Param("id"), store.GroupHumanRef, req.WorkflowIDs); err != nil {
		writeGroupAPIError(c, err)
		return
	}
	h.touchGroup(c.Param("id"))
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

// RemoveGroupMember DELETE /api/v1/groups/:id/members/:ref
func (h *APIHandler) RemoveGroupMember(c *gin.Context) {
	if err := h.store.RemoveGroupMember(c.Request.Context(), c.Param("id"), c.Param("ref")); err != nil {
		writeGroupAPIError(c, err)
		return
	}
	h.touchGroup(c.Param("id"))
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

// SetGroupMemberPolicy PUT /api/v1/groups/:id/members/:ref/policy  {push_policy?, muted_until?, clear_mute?}
func (h *APIHandler) SetGroupMemberPolicy(c *gin.Context) {
	var req struct {
		PushPolicy string `json:"push_policy"`
		MutedUntil string `json:"muted_until"`
		ClearMute  bool   `json:"clear_mute"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	var until *time.Time
	if s := strings.TrimSpace(req.MutedUntil); s != "" {
		t, err := time.Parse(time.RFC3339, s)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "muted_until 需要 RFC3339 时间"})
			return
		}
		until = &t
	}
	if err := h.store.SetGroupMemberPolicy(c.Request.Context(), c.Param("id"), c.Param("ref"), req.PushPolicy, until, req.ClearMute); err != nil {
		writeGroupAPIError(c, err)
		return
	}
	h.touchGroup(c.Param("id"))
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

// ---- /conversations/:id/* 的群分支 ----

func (h *APIHandler) listGroupMessages(c *gin.Context) {
	limit, _ := strconv.Atoi(c.Query("limit"))
	page, err := h.chatService().ListGroup(c.Request.Context(), c.Param("id"), chat.ListParams{
		Limit: limit, Before: c.Query("before"), After: c.Query("after"), Around: c.Query("around"), Query: c.Query("q"),
	})
	if err != nil {
		writeGroupAPIError(c, err)
		return
	}
	c.JSON(http.StatusOK, page)
}

func (h *APIHandler) markGroupRead(c *gin.Context) {
	var req struct {
		Cursor string `json:"cursor"`
	}
	_ = c.ShouldBindJSON(&req)
	st, err := h.chatService().MarkGroupRead(c.Request.Context(), c.Param("id"), req.Cursor)
	if err != nil {
		writeGroupAPIError(c, err)
		return
	}
	h.touchGroup(c.Param("id"))
	c.JSON(http.StatusOK, gin.H{"status": "ok", "unread": st.Unread})
}

func (h *APIHandler) sendGroupMessage(c *gin.Context) {
	var req struct {
		Text     string   `json:"text"`
		Mentions []string `json:"mentions"`
		ReplyTo  string   `json:"reply_to"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if strings.TrimSpace(req.Text) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "消息不能为空"})
		return
	}
	// 人发的话不能「点名你自己」。@ 的目标必须是群里的在职成员（由存储层校验）。
	mentions := make([]string, 0, len(req.Mentions))
	for _, m := range req.Mentions {
		if m = strings.TrimSpace(m); m != "" && m != model.ChatMentionHuman {
			mentions = append(mentions, m)
		}
	}
	msg, res, err := h.chatService().PostHuman(c.Request.Context(), c.Param("id"), req.Text, mentions, req.ReplyTo)
	if err != nil {
		writeGroupAPIError(c, err)
		return
	}
	h.touchGroup(c.Param("id"))
	c.JSON(http.StatusOK, gin.H{"status": "ok", "message": msg, "delivered_to": res.Recipients})
}
