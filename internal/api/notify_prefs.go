package api

import (
	"errors"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/teacat99/RelayMesh/v2/internal/notify"
)

// 通知偏好的 Web 接口（#115，设计稿 07 §5）。全局偏好与会话级设置都存在 system_settings 的同一个键里：
//   - GET    /notify/prefs：当前偏好（免打扰、ntfy 预览模式、会话级设置）；
//   - PUT    /notify/prefs {dnd?, ntfy_preview?}：局部修改全局偏好（项目约定里局部修改用 PUT，不用 PATCH）；
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

// GetNotifyPrefs 返回当前通知偏好。
func (h *APIHandler) GetNotifyPrefs(c *gin.Context) {
	prefs, err := h.store.GetNotifyPrefs(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "读取通知偏好失败"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"prefs": prefs, "now": time.Now()})
}

// UpdateNotifyPrefs 局部修改全局偏好：只改请求体里出现的字段。
func (h *APIHandler) UpdateNotifyPrefs(c *gin.Context) {
	var req struct {
		DND         *bool   `json:"dnd"`
		NtfyPreview *string `json:"ntfy_preview"`
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, notifyPrefsMaxBody)
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请求内容不是有效的 JSON"})
		return
	}
	if req.DND == nil && req.NtfyPreview == nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "没有要修改的字段（dnd / ntfy_preview）"})
		return
	}
	if req.NtfyPreview != nil && !notify.PreviewMode(*req.NtfyPreview).Valid() {
		c.JSON(http.StatusBadRequest, gin.H{"error": "ntfy_preview 只能是 auto / always / never"})
		return
	}
	prefs, err := h.store.UpdateNotifyPrefs(c.Request.Context(), func(cur notify.Prefs) (notify.Prefs, error) {
		if req.DND != nil {
			cur.DND = *req.DND
		}
		if req.NtfyPreview != nil {
			cur.NtfyPreview = notify.PreviewMode(*req.NtfyPreview)
		}
		return cur, nil
	})
	if !h.finishNotifyPrefsUpdate(c, prefs, err) {
		return
	}
	log.Printf("[notify] 通知偏好已更新：免打扰=%v 预览模式=%s", prefs.DND, prefs.NtfyPreview)
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
	c.JSON(http.StatusOK, gin.H{"prefs": prefs, "now": time.Now()})
	return true
}
