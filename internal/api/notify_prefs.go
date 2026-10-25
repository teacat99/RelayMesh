package api

import (
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/teacat99/RelayMesh/v2/internal/notify"
)

// 通知偏好的 Web 接口（#115，设计稿 07 §5）。全局偏好与会话级设置都存在 system_settings 的同一个键里：
//   - GET    /notify/prefs：当前偏好（免打扰、ntfy 预览模式、指派久无人接的提醒开关与阈值、会话级设置；
//     另带阈值的生效值 board_stale_after_minutes 与出厂默认 board_stale_default_minutes，见 notifyPrefsView）；
//   - PUT    /notify/prefs {dnd?, ntfy_preview?, board_stale_off?, board_stale_minutes?}：局部修改全局偏好（项目约定里局部修改用 PUT，不用 PATCH）；
//   - PUT    /notify/conversations {conversation, policy, muted_until?}：设置一个会话的策略（与默认相同即等于恢复默认）；
//   - DELETE /notify/conversations?conversation=：恢复该会话的默认策略。
//
// 每次修改后 SSE 推 notify_prefs_updated，其他标签页 / 设备重新取一次即可立即同步免打扰状态。
// 本机的声音与桌面通知开关是浏览器自己的偏好（localStorage），不在这里。

// 请求体上限：这些都是很小的表单。
const notifyPrefsMaxBody = 4 << 10

// notifyConversationBody 是设置会话策略的请求体。
type notifyConversationBody struct {
	Conversation string     `json:"conversation"`
	Policy       string     `json:"policy"`
	MutedUntil   *time.Time `json:"muted_until"`
}

// notifyPrefsView 是接口给出的偏好：存下来的偏好原样平铺，外加「指派久无人接」阈值的生效值与出厂默认。
// 没改过阈值时库里存的是 0（跟着默认走），而界面要显示「2 小时」、网页行的变红时间也要和回执头、提醒用同一个数——
// 默认值只在后端定义一处，前端从这里读，不再抄一份（抄了就会在以后调整默认值时悄悄漂移）。
type notifyPrefsView struct {
	notify.Prefs
	BoardStaleAfterMinutes   int `json:"board_stale_after_minutes"`
	BoardStaleDefaultMinutes int `json:"board_stale_default_minutes"`
}

func newNotifyPrefsView(p notify.Prefs) notifyPrefsView {
	return notifyPrefsView{
		Prefs:                    p,
		BoardStaleAfterMinutes:   p.BoardStaleAfterMinutes(),
		BoardStaleDefaultMinutes: notify.BoardStaleDefaultMinutes,
	}
}

// GetNotifyPrefs 返回当前通知偏好。
func (h *APIHandler) GetNotifyPrefs(c *gin.Context) {
	prefs, err := h.store.GetNotifyPrefs(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "读取通知偏好失败"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"prefs": newNotifyPrefsView(prefs), "now": time.Now()})
}

// UpdateNotifyPrefs 局部修改全局偏好：只改请求体里出现的字段。
func (h *APIHandler) UpdateNotifyPrefs(c *gin.Context) {
	var req struct {
		DND         *bool   `json:"dnd"`
		NtfyPreview *string `json:"ntfy_preview"`
		// 「指派久无人接」（#180 B3d）：board_stale_off 关掉提醒；board_stale_minutes 是阈值（30~1440 分钟，0 = 恢复默认）。
		BoardStaleOff     *bool `json:"board_stale_off"`
		BoardStaleMinutes *int  `json:"board_stale_minutes"`
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, notifyPrefsMaxBody)
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请求内容不是有效的 JSON"})
		return
	}
	if req.DND == nil && req.NtfyPreview == nil && req.BoardStaleOff == nil && req.BoardStaleMinutes == nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "没有要修改的字段（dnd / ntfy_preview / board_stale_off / board_stale_minutes）"})
		return
	}
	if req.NtfyPreview != nil && !notify.PreviewMode(*req.NtfyPreview).Valid() {
		c.JSON(http.StatusBadRequest, gin.H{"error": "ntfy_preview 只能是 auto / always / never"})
		return
	}
	// 阈值越界时明确拒绝，不悄悄收到边界上：用户点的是 5 分钟，保存下来却是 30 分钟，会让人以为设置没生效。
	if req.BoardStaleMinutes != nil && !notify.ValidBoardStaleMinutes(*req.BoardStaleMinutes) {
		c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("board_stale_minutes 应在 %d~%d 分钟之间（0 表示恢复默认 %d 分钟）",
			notify.BoardStaleMinMinutes, notify.BoardStaleMaxMinutes, notify.BoardStaleDefaultMinutes)})
		return
	}
	prefs, err := h.store.UpdateNotifyPrefs(c.Request.Context(), func(cur notify.Prefs) (notify.Prefs, error) {
		if req.DND != nil {
			cur.DND = *req.DND
		}
		if req.NtfyPreview != nil {
			cur.NtfyPreview = notify.PreviewMode(*req.NtfyPreview)
		}
		if req.BoardStaleOff != nil {
			cur.BoardStaleOff = *req.BoardStaleOff
		}
		if req.BoardStaleMinutes != nil {
			cur.BoardStaleMinutes = *req.BoardStaleMinutes
		}
		return cur, nil
	})
	if !h.finishNotifyPrefsUpdate(c, prefs, err) {
		return
	}
	log.Printf("[notify] 通知偏好已更新：免打扰=%v 预览模式=%s 指派久无人接提醒=%v 阈值=%d 分钟",
		prefs.DND, prefs.NtfyPreview, !prefs.BoardStaleOff, prefs.BoardStaleAfterMinutes())
}

// SetNotifyConversation 设置一个会话的通知策略。
func (h *APIHandler) SetNotifyConversation(c *gin.Context) {
	var req notifyConversationBody
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, notifyPrefsMaxBody)
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请求内容不是有效的 JSON"})
		return
	}
	conv := strings.TrimSpace(req.Conversation)
	if !notify.ValidConversation(conv) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "conversation 无效：应为 wf:<工作流> 或 grp:<群>"})
		return
	}
	policy := notify.Policy(req.Policy)
	if !policy.Valid() {
		c.JSON(http.StatusBadRequest, gin.H{"error": "policy 只能是 all / mentions / muted"})
		return
	}
	if req.MutedUntil != nil {
		if policy != notify.PolicyMuted {
			c.JSON(http.StatusBadRequest, gin.H{"error": "muted_until 只能和 policy=muted 一起使用"})
			return
		}
		if !req.MutedUntil.After(time.Now()) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "muted_until 必须晚于现在"})
			return
		}
	}
	prefs, err := h.store.UpdateNotifyPrefs(c.Request.Context(), func(cur notify.Prefs) (notify.Prefs, error) {
		next := cur
		next.Conversations = make(map[string]notify.ConvPref, len(cur.Conversations)+1)
		for k, v := range cur.Conversations {
			next.Conversations[k] = v
		}
		next.Conversations[conv] = notify.ConvPref{Policy: policy, MutedUntil: req.MutedUntil}
		return next, nil
	})
	if !h.finishNotifyPrefsUpdate(c, prefs, err) {
		return
	}
	log.Printf("[notify] 会话通知策略已更新：%s → %s", conv, policy)
}

// ResetNotifyConversation 恢复一个会话的默认策略（没有设置过也算成功）。
func (h *APIHandler) ResetNotifyConversation(c *gin.Context) {
	conv := strings.TrimSpace(c.Query("conversation"))
	if !notify.ValidConversation(conv) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "conversation 无效：应为 wf:<工作流> 或 grp:<群>"})
		return
	}
	prefs, err := h.store.UpdateNotifyPrefs(c.Request.Context(), func(cur notify.Prefs) (notify.Prefs, error) {
		next := cur
		next.Conversations = make(map[string]notify.ConvPref, len(cur.Conversations))
		for k, v := range cur.Conversations {
			if k != conv {
				next.Conversations[k] = v
			}
		}
		return next, nil
	})
	if !h.finishNotifyPrefsUpdate(c, prefs, err) {
		return
	}
	log.Printf("[notify] 会话通知策略已恢复默认：%s", conv)
}

// finishNotifyPrefsUpdate 统一收尾：错误转成响应（校验类 400，其余 500），成功则广播并返回新偏好。返回 true 表示成功。
func (h *APIHandler) finishNotifyPrefsUpdate(c *gin.Context, prefs notify.Prefs, err error) bool {
	if err != nil {
		if errors.Is(err, notify.ErrInvalidPrefs) {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return false
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "保存通知偏好失败"})
		return false
	}
	if h.broker != nil {
		h.broker.Broadcast("notify_prefs_updated", gin.H{"at": time.Now()})
	}
	c.JSON(http.StatusOK, gin.H{"prefs": newNotifyPrefsView(prefs), "now": time.Now()})
	return true
}
