package api

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/teacat99/RelayMesh/v2/internal/store"
)

// Agent 身份与状态的 Web 接口（#113，设计稿 04 §5）：
//   - GET /identities：身份清单（显示名、客户端类型与版本、最近一次 hook / MCP / 查询的时间）；
//   - PUT /identities/:id：改名（空串恢复用目录名）。项目约定里局部修改都用 PUT（CORS 只放行 GET / POST / PUT / DELETE），所以不用 PATCH；
//   - GET /agent-sessions：会话清单与当前状态，可按 workflow_id / identity_id 过滤。
//
// 状态由 hook 汇报与 MCP 调用推进（见 internal/mcp/agent_state.go），变化时 SSE 推 agent_state 事件，前端按事件里的 workflow_id 重新取一次即可；
// 「停摆 / 离线 / 等你」这类随时间推导的状态是取数时才算出来的，所以前端也要隔一阵重新取一次。

// 改名请求体的上限：一个字段的小表单。
const agentIdentityMaxBody = 2 << 10

func parseAgentIdentityID(c *gin.Context) (uint, bool) {
	n, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || n == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "无效的身份编号"})
		return 0, false
	}
	return uint(n), true
}

// ListAgentIdentities 返回全部 Agent 身份。
//
//	GET /api/v1/identities
func (h *APIHandler) ListAgentIdentities(c *gin.Context) {
	list, err := h.store.ListAgentIdentities(c.Request.Context())
	if err != nil {
		respondWorkError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"identities": list, "now": time.Now()})
}

// UpdateAgentIdentity 给身份改名。
//
//	PUT /api/v1/identities/:id   {"name": "后端组"}
func (h *APIHandler) UpdateAgentIdentity(c *gin.Context) {
	id, ok := parseAgentIdentityID(c)
	if !ok {
		return
	}
	var req struct {
		Name *string `json:"name"`
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, agentIdentityMaxBody)
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "无效的请求内容"})
		return
	}
	if req.Name == nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "缺少要修改的字段 name"})
		return
	}
	view, err := h.store.RenameAgentIdentity(c.Request.Context(), id, *req.Name, "web")
	if err != nil {
		respondWorkError(c, err)
		return
	}
	if h.broker != nil {
		h.broker.Broadcast("agent_identity_updated", gin.H{"identity_id": view.ID})
	}
	c.JSON(http.StatusOK, gin.H{"identity": view})
}

// ListAgentSessions 返回 Agent 会话与当前状态，最近有动静的在前。
//
//	GET /api/v1/agent-sessions?workflow_id=&identity_id=&limit=
func (h *APIHandler) ListAgentSessions(c *gin.Context) {
	f := store.AgentSessionFilter{WorkflowID: strings.TrimSpace(c.Query("workflow_id"))}
	if v := strings.TrimSpace(c.Query("identity_id")); v != "" {
		n, err := strconv.ParseUint(v, 10, 64)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "无效的身份编号"})
			return
		}
		f.IdentityID = uint(n)
	}
	if v := strings.TrimSpace(c.Query("limit")); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "无效的 limit"})
			return
		}
		f.Limit = n
	}
	now := time.Now()
	list, err := h.store.ListAgentSessions(c.Request.Context(), f, now)
	if err != nil {
		respondWorkError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"sessions": list, "now": now})
}
