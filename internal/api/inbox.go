package api

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/teacat99/RelayMesh/v2/internal/model"
	"github.com/teacat99/RelayMesh/v2/internal/store"
)

// 追加指令收件箱的 Web 接口（D-210）：
//   - GET /workflows/:workflow_id/inbox：某个工作流收件箱里的条目（每条带状态）、概况、智能体最近一次与 RelayMesh 交互的时间、编辑器 hook 最近一次查询的时间；
//   - GET / PUT /inbox/config：手动开关（总开关 + 三层发现机制各自的开关）。
//
// Cursor hook 的安装入口（install.sh / install.ps1 / 二进制下载）在 hook_install.go。
//
// 追加本身仍走 POST /workflows/:workflow_id/append，撤回仍走 POST /feedbacks/queued/:id/revoke（见 routes.go）。

// 开关请求体的上限：一个四项的小表单远用不到这么多。
const inboxMaxBody = 4 << 10

// inboxConfigView 是开关的响应：界面里保存的值，加上环境变量总闸与最终生效的结果。
type inboxConfigView struct {
	Enabled         bool `json:"enabled"`
	ReceiptHint     bool `json:"receipt_hint"`
	Hooks           bool `json:"hooks"`
	PromptClause    bool `json:"prompt_clause"`
	HookInstallHint bool `json:"hook_install_hint"`
	// EnvOff 表示服务端用 RELAYMESH_INBOX=off 强制关掉了总开关：这时界面里的总开关不起作用，只能改环境变量。
	EnvOff bool `json:"env_off"`
	// Effective 是叠加环境变量之后真正生效的开关。
	Effective store.InboxConfig `json:"effective"`
}

func (h *APIHandler) inboxConfigView(c *gin.Context) inboxConfigView {
	saved := h.store.GetInboxConfig(c.Request.Context())
	envOff := h.cfg != nil && h.cfg.InboxOff
	return inboxConfigView{
		Enabled:         saved.Enabled,
		ReceiptHint:     saved.ReceiptHint,
		Hooks:           saved.Hooks,
		PromptClause:    saved.PromptClause,
		HookInstallHint: saved.HookInstallHint,
		EnvOff:          envOff,
		Effective:       saved.Effective(envOff),
	}
}

// GetWorkflowInbox 返回一个工作流收件箱的现状。
func (h *APIHandler) GetWorkflowInbox(c *gin.Context) {
	workflowID := c.Param("workflow_id")
	ctx := c.Request.Context()
	items, err := h.store.ListInboxItems(ctx, workflowID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if items == nil {
		items = []model.QueuedFeedback{}
	}
	summary, err := h.store.InboxSummary(ctx, workflowID, time.Now())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	var lastActivity, hookSeen *time.Time
	if at, ok := h.store.AgentLastActivity(workflowID); ok {
		lastActivity = &at
	}
	hookOK := false
	if at, ok := h.store.HookLastSeen(workflowID); ok {
		hookSeen = &at
		hookOK = true
	}
	now := time.Now()
	link := h.store.AgentLinkOf(ctx, workflowID)
	sess, _ := h.store.GetLatestWorkflowFeedbackSession(ctx, workflowID)
	agentLive := store.ComputeAgentLive(link, time.Time{}, hookOK, sess, now)
	if hookOK && hookSeen != nil {
		agentLive = store.ComputeAgentLive(link, *hookSeen, true, sess, now)
	}
	body := gin.H{
		"workflow_id":            workflowID,
		"items":                  items,
		"summary":                summary,
		"agent_last_activity_at": lastActivity,
		"hook_last_seen_at":      hookSeen,
		"agent_live":             agentLive,
		"config":                 h.inboxConfigView(c),
	}
	// 异步对话的状态与 hook 芯片同源（芯片弹层里放开关），一次请求带齐；取不到时不影响收件箱本身。
	if mode, err := h.asyncModeViewFor(c, workflowID); err == nil {
		body["async_mode"] = mode
	}
	c.JSON(http.StatusOK, body)
}

// GetInboxConfig 返回收件箱的手动开关。
func (h *APIHandler) GetInboxConfig(c *gin.Context) {
	c.JSON(http.StatusOK, h.inboxConfigView(c))
}

// UpdateInboxConfig 修改手动开关；只改请求里带了的字段。
func (h *APIHandler) UpdateInboxConfig(c *gin.Context) {
	var req struct {
		Enabled         *bool `json:"enabled"`
		ReceiptHint     *bool `json:"receipt_hint"`
		Hooks           *bool `json:"hooks"`
		PromptClause    *bool `json:"prompt_clause"`
		HookInstallHint *bool `json:"hook_install_hint"`
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, inboxMaxBody)
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请求格式不对：" + err.Error()})
		return
	}
	if req.Enabled == nil && req.ReceiptHint == nil && req.Hooks == nil && req.PromptClause == nil && req.HookInstallHint == nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "没有要修改的开关"})
		return
	}
	_, err := h.store.UpdateInboxConfig(c.Request.Context(), func(cur store.InboxConfig) store.InboxConfig {
		if req.Enabled != nil {
			cur.Enabled = *req.Enabled
		}
		if req.ReceiptHint != nil {
			cur.ReceiptHint = *req.ReceiptHint
		}
		if req.Hooks != nil {
			cur.Hooks = *req.Hooks
		}
		if req.PromptClause != nil {
			cur.PromptClause = *req.PromptClause
		}
		if req.HookInstallHint != nil {
			cur.HookInstallHint = *req.HookInstallHint
		}
		return cur
	})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "保存失败"})
		return
	}
	view := h.inboxConfigView(c)
	h.broker.Broadcast("inbox_config_updated", view)
	c.JSON(http.StatusOK, view)
}
