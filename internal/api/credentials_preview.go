package api

import (
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
	"github.com/teacat99/RelayMesh/v2/internal/mcp"
	"github.com/teacat99/RelayMesh/v2/internal/model"
)

// PreviewCredentialTools 返回「具有给定权限的令牌将暴露哪些工具、收到哪一版指引」。
// 权限页在保存之前用它做预览：展示内容由服务端按真实逻辑算出，前端不重复实现一份。
func (h *APIHandler) PreviewCredentialTools(c *gin.Context) {
	var req struct {
		Permissions model.Permissions `json:"permissions"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}

	req.Permissions = req.Permissions.Normalized()
	tools := mcp.GetToolDefinitionsForPermissions(req.Permissions)
	items := make([]gin.H, 0, len(tools))
	for _, tool := range tools {
		items = append(items, gin.H{
			"name":    tool.Name,
			"summary": firstLineSummary(tool.Description, 120),
		})
	}

	tier := mcp.TierForPermissions(req.Permissions)
	instructions := "none"
	switch tier {
	case mcp.TierRelayMesh:
		instructions = "governance"
	case mcp.TierExecMesh:
		instructions = "terminal"
	case mcp.TierHybrid:
		instructions = "governance+terminal"
	}

	c.JSON(http.StatusOK, gin.H{
		"tier":         tier,
		"tools":        items,
		"instructions": instructions,
		// 混合令牌调用 terminal_exec 时必须带 workflow_id 与 description；ExecMesh 档不需要。
		"terminal_requires_workflow": tier == mcp.TierHybrid && req.Permissions.Terminal,
		// 开启「ExecMesh 兼容命名」时，终端工具以 ExecMesh 的名字出现（targets_list / exec / job_* …）。
		"terminal_compat": req.Permissions.Terminal && req.Permissions.TerminalCompat,
	})
}

func firstLineSummary(text string, maxRunes int) string {
	text = strings.TrimSpace(text)
	if idx := strings.IndexAny(text, "\r\n"); idx >= 0 {
		text = strings.TrimSpace(text[:idx])
	}
	if utf8.RuneCountInString(text) <= maxRunes {
		return text
	}
	runes := []rune(text)
	return string(runes[:maxRunes]) + "…"
}
