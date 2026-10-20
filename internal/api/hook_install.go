package api

import (
	"io"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/teacat99/RelayMesh/v2/internal/cursorhook"
	"github.com/teacat99/RelayMesh/v2/internal/installer"
)

const (
	hookInstallRatePerMinute = 120
	hookInstallMaxTransfers  = 8
)

type hookInstallLimiter struct {
	mu   sync.Mutex
	rate map[string]int
	win  map[string]time.Time
}

func (l *hookInstallLimiter) allow(ip string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.rate == nil {
		l.rate = map[string]int{}
		l.win = map[string]time.Time{}
	}
	if len(l.rate) > 4096 {
		l.rate = map[string]int{}
		l.win = map[string]time.Time{}
	}
	start, ok := l.win[ip]
	if !ok || now.Sub(start) >= time.Minute {
		l.win[ip], l.rate[ip] = now, 0
	}
	l.rate[ip]++
	return l.rate[ip] <= hookInstallRatePerMinute
}

var hookLimiter hookInstallLimiter

func (h *APIHandler) hookDist() *installer.Dist {
	dir := installer.ResolveDir("")
	if h.cfg != nil {
		dir = installer.ResolveDir(h.cfg.AgentDistDir)
	}
	return installer.NewDist(dir)
}

// hubVersion 是本 Hub 的版本号；写进安装脚本，让没有 agent-dist 的部署回退到 GitHub Release 时取到同版本的 hook。
func (h *APIHandler) hubVersion() string {
	if h.cfg != nil {
		return h.cfg.Version
	}
	return ""
}

func (h *APIHandler) ServeHookInstallSh(c *gin.Context) {
	if !hookLimiter.allow(c.ClientIP(), time.Now()) {
		c.Status(http.StatusTooManyRequests)
		return
	}
	c.Header("Cache-Control", "no-store")
	c.Data(http.StatusOK, "text/plain; charset=utf-8", cursorhook.RenderInstallSh(h.hubVersion()))
}

func (h *APIHandler) ServeHookInstallPs1(c *gin.Context) {
	if !hookLimiter.allow(c.ClientIP(), time.Now()) {
		c.Status(http.StatusTooManyRequests)
		return
	}
	c.Header("Cache-Control", "no-store")
	c.Data(http.StatusOK, "text/plain; charset=utf-8", cursorhook.RenderInstallPs1(h.hubVersion()))
}

func (h *APIHandler) ServeHookBinary(c *gin.Context) {
	name := c.Param("name")
	if name == installer.ChecksumsName {
		h.serveHookChecksums(c)
		return
	}
	if !installer.ValidHookAssetName(name) {
		c.Status(http.StatusNotFound)
		return
	}
	if !hookLimiter.allow(c.ClientIP(), time.Now()) {
		c.Status(http.StatusTooManyRequests)
		return
	}
	d := h.hookDist()
	f, st, err := d.OpenHook(name)
	if err != nil {
		c.Status(http.StatusNotFound)
		return
	}
	defer f.Close()
	c.Header("Cache-Control", "no-store")
	c.Header("Content-Length", strconv.FormatInt(st.Size(), 10))
	c.Header("Content-Type", "application/octet-stream")
	_, _ = io.Copy(c.Writer, f)
}

func (h *APIHandler) serveHookChecksums(c *gin.Context) {
	if !hookLimiter.allow(c.ClientIP(), time.Now()) {
		c.Status(http.StatusTooManyRequests)
		return
	}
	b, err := h.hookDist().HookChecksums()
	if err != nil {
		c.Status(http.StatusNotFound)
		return
	}
	c.Header("Cache-Control", "no-store")
	c.Data(http.StatusOK, "text/plain; charset=utf-8", b)
}
