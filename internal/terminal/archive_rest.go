package terminal

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// ---------- 侧栏归档（D-200） ----------
//
// 归档只改变侧栏默认显示哪些条目，不结束会话、不动记录与审计，也不改变模型线程里的内容。
// 它和置顶、改名一样属于「整理自己的清单」，只需要登录，不需要解锁令牌。
// 已归档的 key 随主机列表一起返回（见 handleListHosts），所以多台设备在下一次轮询时自然同步。

// archiveMaxKeysPerCall 限制一次请求能整理的条目数，足够「全部归档」这类批量操作，又不会被拿来灌库。
const archiveMaxKeysPerCall = 200

// archiveKeyMaxLen 与 model.TerminalArchive 的列宽一致。
const archiveKeyMaxLen = 160

type archiveRequest struct {
	// Keys 是侧栏条目的 key：「机器码:会话 ID」或「r:线程键」。
	Keys []string `json:"keys"`
	// Archived 为 true 归档，为 false 取消归档。
	Archived bool `json:"archived"`
}

// validArchiveKey 只放行侧栏 key 会用到的字符；不要求对应的条目此刻存在
// （主机离线、历史被清理后，归档记录仍然有意义，也便于「撤销」竞态时幂等）。
func validArchiveKey(k string) bool {
	if k == "" || len(k) > archiveKeyMaxLen {
		return false
	}
	for i := 0; i < len(k); i++ {
		c := k[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case c == ':', c == '-', c == '_', c == '.':
		default:
			return false
		}
	}
	return true
}

func (h *Hub) handleArchive(c *gin.Context) {
	var req archiveRequest
	if !bindJSON(c, &req) {
		return
	}
	if len(req.Keys) == 0 || len(req.Keys) > archiveMaxKeysPerCall {
		c.JSON(http.StatusBadRequest, gin.H{"error": "一次需要整理 1～200 个条目", "code": "bad_archive_keys"})
		return
	}
	seen := make(map[string]struct{}, len(req.Keys))
	keys := make([]string, 0, len(req.Keys))
	for _, k := range req.Keys {
		if !validArchiveKey(k) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "条目标识格式不正确", "code": "bad_archive_keys"})
			return
		}
		if _, dup := seen[k]; dup {
			continue
		}
		seen[k] = struct{}{}
		keys = append(keys, k)
	}
	changed, err := h.store.SetTerminalArchived(c.Request.Context(), keys, req.Archived, h.now())
	if err != nil {
		h.fail(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "changed": changed})
}
