package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/teacat99/RelayMesh/v2/internal/ntfy"
)

// ntfy 推送设置：独立于 /settings（全局设置会整体返回给浏览器），因为这里有访问令牌 / 密码——
// 凭据只许写、不许读，响应里只告诉界面「是否已设置」。

// 请求体上限：一张表单远小于这个数，超大的请求直接拒绝。
const ntfyMaxBody = 32 << 10

// 测试推送的最短间隔：它会真的向外发请求，不能被当成放大器反复点。
const ntfyTestInterval = 2 * time.Second

// ntfyGuard 保存测试推送的节流状态。
type ntfyGuard struct {
	mu       sync.Mutex
	lastTest time.Time
}

func (g *ntfyGuard) allowTest(now time.Time) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if now.Sub(g.lastTest) < ntfyTestInterval {
		return false
	}
	g.lastTest = now
	return true
}

// ntfyView 是 GET / PUT 的响应。
type ntfyView struct {
	Config ntfy.PublicConfig `json:"config"`
	Status ntfy.Status       `json:"status"`
}

// SetNtfy 接入 ntfy 推送器；启动时调用一次。
func (h *APIHandler) SetNtfy(n *ntfy.Notifier) { h.ntfy = n }

func (h *APIHandler) ntfyUnavailable(c *gin.Context) bool {
	if h.ntfy == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "ntfy 推送功能未启用"})
		return true
	}
	return false
}

// GetNtfy 返回当前配置（不含凭据）与最近的推送情况。
func (h *APIHandler) GetNtfy(c *gin.Context) {
	if h.ntfyUnavailable(c) {
		return
	}
	cfg, err := h.store.GetNtfyConfig(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "读取 ntfy 配置失败"})
		return
	}
	c.JSON(http.StatusOK, h.ntfyViewOf(cfg))
}

// UpdateNtfy 保存整张表单；凭据留空表示保持原值。
func (h *APIHandler) UpdateNtfy(c *gin.Context) {
	if h.ntfyUnavailable(c) {
		return
	}
	var upd ntfy.Update
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, ntfyMaxBody)
	if err := c.ShouldBindJSON(&upd); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请求内容不是有效的 JSON"})
		return
	}

	cfg, err := h.store.UpdateNtfyConfig(c.Request.Context(), upd.ApplyTo)
	if err != nil {
		if errors.Is(err, ntfy.ErrInvalidConfig) {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "保存 ntfy 配置失败"})
		return
	}
	// 日志只记「谁开了 / 关了、连哪台服务」，不记主题和任何凭据（主题在公共服务上相当于密码）。
	log.Printf("[ntfy] 推送配置已更新：启用=%v 服务=%s", cfg.Enabled, ntfyHost(cfg.Server))
	c.JSON(http.StatusOK, h.ntfyViewOf(cfg))
}

// TestNtfy 立刻发一条测试推送。请求体里的 draft（可选）是尚未保存的表单，叠加在已保存的配置上试发；
// 没有 draft 就用已保存的配置。不论推送成败，接口本身都返回 200，结果在 ok / error 里。
func (h *APIHandler) TestNtfy(c *gin.Context) {
	if h.ntfyUnavailable(c) {
		return
	}
	var req struct {
		Draft *ntfy.Update `json:"draft"`
	}
	raw, err := io.ReadAll(http.MaxBytesReader(c.Writer, c.Request.Body, ntfyMaxBody))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请求内容过大或读取失败"})
		return
	}
	// 允许没有请求体（直接测已保存的配置）；有请求体但不是合法 JSON 才算错。
	if len(bytes.TrimSpace(raw)) > 0 {
		if err := json.Unmarshal(raw, &req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "请求内容不是有效的 JSON"})
			return
		}
	}

	cur, err := h.store.GetNtfyConfig(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "读取 ntfy 配置失败"})
		return
	}
	cfg := cur
	if req.Draft != nil {
		cfg = req.Draft.ApplyTo(cur)
	}
	if err := cfg.Validate(true); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if !h.ntfyGuard.allowTest(time.Now()) {
		c.JSON(http.StatusTooManyRequests, gin.H{"error": "测试过于频繁，请稍等几秒再试"})
		return
	}

	res, sendErr := h.ntfy.SendTest(c.Request.Context(), cfg)
	out := gin.H{
		"ok":          sendErr == nil,
		"attempts":    res.Attempts,
		"took_ms":     res.Took.Milliseconds(),
		"http_status": res.Status,
		"status":      h.ntfy.Status(),
	}
	if sendErr != nil {
		out["error"] = sendErr.Error()
	}
	c.JSON(http.StatusOK, out)
}

func (h *APIHandler) ntfyViewOf(cfg ntfy.Config) ntfyView {
	return ntfyView{Config: cfg.Public(h.cfg.NtfyToken), Status: h.ntfy.Status()}
}

// ntfyHost 只取主机名用于日志。
func ntfyHost(server string) string {
	if u, err := url.Parse(server); err == nil && u.Host != "" {
		return u.Host
	}
	return "?"
}
