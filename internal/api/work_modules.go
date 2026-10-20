package api

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/teacat99/RelayMesh/v2/internal/store"
)

// 任务模块规划（WorkModule）的 REST 接口：每张任务板一棵最多两层的树（模块 → 功能）；同一个项目的工作流共用一张板。
// 登录后的管理员在任务页左侧的树上增删改排；智能体用 MCP 的 task_board(plan) 一次声明整棵树。
// 规则（命名、数量、深度、删除时任务回落）集中在 store 层。

func parseModuleID(c *gin.Context) (uint, bool) {
	n, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || n == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "无效的模块编号"})
		return 0, false
	}
	return uint(n), true
}

// broadcastWorkModules 通知前端：这张任务板的规划树（以及挂在上面的任务归属）变了，需要重新取一次。
// workflowID 是节点的创建者，boardKey 是它所在的板；同一个项目的工作流共用一张板，前端按 board_key 匹配（#175）。
func (h *APIHandler) broadcastWorkModules(workflowID, boardKey string) {
	if h.broker == nil || (workflowID == "" && boardKey == "") {
		return
	}
	h.broker.Broadcast("work_module_updated", gin.H{"workflow_id": workflowID, "board_key": boardKey})
}

// ListWorkModules 返回工作流的规划树（按树的顺序拍平，含任务汇总）与未归类任务的汇总。
//
//	GET /api/v1/work-modules?workflow_id=
func (h *APIHandler) ListWorkModules(c *gin.Context) {
	list, err := h.store.ListWorkModules(c.Request.Context(), strings.TrimSpace(c.Query("workflow_id")))
	if err != nil {
		respondWorkError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"modules": list.Modules, "unassigned": list.Unassigned})
}

type workModuleCreateRequest struct {
	WorkflowID  string `json:"workflow_id"`
	ParentID    uint   `json:"parent_id"` // 0 = 顶层模块；否则在这个模块下建功能
	Name        string `json:"name"`
	Description string `json:"description"`
}

// CreateWorkModule 新建模块或功能，排在同级最后。
func (h *APIHandler) CreateWorkModule(c *gin.Context) {
	var req workModuleCreateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "无效的请求内容"})
		return
	}
	m, err := h.store.CreateWorkModule(c.Request.Context(), req.WorkflowID, req.ParentID, req.Name, req.Description)
	if err != nil {
		respondWorkError(c, err)
		return
	}
	h.broadcastWorkModules(m.WorkflowID, m.BoardKey)
	c.JSON(http.StatusCreated, gin.H{"module": m})
}

type workModuleUpdateRequest struct {
	Name        *string `json:"name"`
	Description *string `json:"description"`
}

// UpdateWorkModule 修改模块的名称或说明；缺省的字段保持不变。
func (h *APIHandler) UpdateWorkModule(c *gin.Context) {
	id, ok := parseModuleID(c)
	if !ok {
		return
	}
	var req workModuleUpdateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "无效的请求内容"})
		return
	}
	m, err := h.store.UpdateWorkModule(c.Request.Context(), id, store.WorkModulePatch{Name: req.Name, Description: req.Description})
	if err != nil {
		respondWorkError(c, err)
		return
	}
	h.broadcastWorkModules(m.WorkflowID, m.BoardKey)
	c.JSON(http.StatusOK, gin.H{"module": m})
}

type workModuleMoveRequest struct {
	Direction string `json:"direction"` // up | down
}

// MoveWorkModule 在同级里上移或下移一格，返回最新的规划树。
func (h *APIHandler) MoveWorkModule(c *gin.Context) {
	id, ok := parseModuleID(c)
	if !ok {
		return
	}
	var req workModuleMoveRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "无效的请求内容"})
		return
	}
	delta := 0
	switch strings.ToLower(strings.TrimSpace(req.Direction)) {
	case "up":
		delta = -1
	case "down":
		delta = 1
	default:
		c.JSON(http.StatusBadRequest, gin.H{"error": "direction 只能是 up 或 down"})
		return
	}
	list, err := h.store.MoveWorkModule(c.Request.Context(), id, delta)
	if err != nil {
		respondWorkError(c, err)
		return
	}
	if len(list.Modules) > 0 {
		h.broadcastWorkModules(list.Modules[0].WorkflowID, list.Modules[0].BoardKey)
	}
	c.JSON(http.StatusOK, gin.H{"modules": list.Modules, "unassigned": list.Unassigned})
}

// DeleteWorkModule 删除模块（顶层模块连同它的功能）。任务不会被删：回到上一级，顶层模块被删时变成未归类。
func (h *APIHandler) DeleteWorkModule(c *gin.Context) {
	id, ok := parseModuleID(c)
	if !ok {
		return
	}
	res, err := h.store.DeleteWorkModule(c.Request.Context(), id)
	if err != nil {
		respondWorkError(c, err)
		return
	}
	h.broadcastWorkModules(res.Module.WorkflowID, res.Module.BoardKey)
	c.JSON(http.StatusOK, gin.H{
		"status":           "deleted",
		"path":             res.Module.Path,
		"moved_tasks":      res.MovedTasks,
		"removed_features": res.RemovedFeatures,
	})
}
