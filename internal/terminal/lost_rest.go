package terminal

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/teacat99/RelayMesh/v2/internal/termproto"
)

// ---------- 主机页「会话已不存在」提示的忽略（第六批 #28） ----------
//
// 这条提示来自主机上会话守护进程的上报，守护进程每次连接都会再报一遍，所以「删掉它」由 Hub 记住（见
// model.TerminalLostDismissal）。忽略只是不再提醒：不删除会话记录、审计与历史入口，不要求主机在线，不要求 Agent 升级。
// 和归档、置顶一样属于「整理自己的清单」，只需要登录，不需要解锁令牌。

type lostDismissRequest struct {
	// SIDs 是要忽略的会话 ID（主机页每条提示一个）。
	SIDs []string `json:"sids"`
	// All 为 true 时忽略这台主机当前显示的全部提示（「全部清除」）；与 SIDs 同时给出时两者取并集。
	All bool `json:"all"`
}

func (h *Hub) handleDismissLost(c *gin.Context) {
	code, ok := machineCodeParam(c)
	if !ok {
		return
	}
	var req lostDismissRequest
	if !bindJSON(c, &req) {
		return
	}
	if !req.All && len(req.SIDs) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "需要指定要忽略的会话，或选择全部清除", "code": "bad_lost_sids"})
		return
	}
	if len(req.SIDs) > termproto.MaxLostSessions {
		c.JSON(http.StatusBadRequest, gin.H{"error": "一次最多忽略 64 条提示", "code": "bad_lost_sids"})
		return
	}
	ctx := c.Request.Context()
	if rec, err := h.store.GetTerminalHost(ctx, code); err != nil {
		h.fail(c, err)
		return
	} else if rec == nil {
		h.fail(c, ErrHostNotFound)
		return
	}

	seen := make(map[string]struct{}, len(req.SIDs))
	sids := make([]string, 0, len(req.SIDs))
	add := func(sid string) {
		if _, dup := seen[sid]; dup {
			return
		}
		seen[sid] = struct{}{}
		sids = append(sids, sid)
	}
	for _, sid := range req.SIDs {
		if !termproto.ValidSessionID(sid) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "会话标识格式不正确", "code": "bad_lost_sids"})
			return
		}
		add(sid)
	}
	if req.All {
		// 主机离线时界面不会显示这些提示（提示随在线的守护进程上报而来），没有可清的内容。
		if a := h.onlineAgent(code); a != nil {
			for _, l := range a.holderView().lostList() {
				add(l.SID)
			}
		}
	}
	if len(sids) == 0 {
		c.JSON(http.StatusOK, gin.H{"ok": true, "changed": 0})
		return
	}
	changed, err := h.store.DismissTerminalLost(ctx, code, sids, h.now())
	if err != nil {
		h.fail(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "changed": changed})
}

// lostList 返回守护进程报告的丢失会话（v 为 nil 时为空）。
func (v *HolderView) lostList() []termproto.LostSession {
	if v == nil {
		return nil
	}
	return v.Lost
}
