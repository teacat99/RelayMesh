package terminal

import (
	"bytes"
	"crypto/subtle"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/teacat99/RelayMesh/v2/internal/buildinfo"
	"github.com/teacat99/RelayMesh/v2/internal/installer"
)

// GET /i/:ticket/:name —— 服务端自带的 Agent 安装源（安装脚本、Agent 二进制与 checksums.txt）。
//
// 这是整个终端体系里第二个不要求登录的读取入口（另一个是 /files/dl/:ticket），所以安全性来自下面几层：
//   - 票据绑定发现窗口：它在窗口开启时才有效，窗口关闭 / 到期后立即作废，下一次开窗换发新票据；
//     票据 128 位随机、常量时间比较，所有失败（票据不对、窗口没开、总闸关闭、文件不存在、名字不在白名单）都是同一个 404，
//     无法借响应差异探测任何状态；
//   - 文件名白名单：只允许 install.sh / install.ps1 / checksums.txt 与 Agent 二进制的固定命名，目录里的其他文件永远不会被发出；
//   - 限速与并发上限：每个来源地址每分钟 installRatePerMinute 次、全局同时最多 installMaxTransfers 个传输；
//   - Cache-Control: no-store，脚本里带有票据，不允许任何中间层缓存；票据不进访问日志（见 api.RedactRequestURI）。
//
// 票据只证明「管理员此刻想接入新主机」，下载内容本身并不机密（公开发布的二进制）。真正保护 Hub 的是
// Agent 握手：机器码未经管理员核对启用之前，连上来也不会获得任何权限。
const (
	installRatePerMinute = 60
	installMaxTransfers  = 8
)

// EvInstallServe 记录服务端分发了安装文件（或拒绝了一次带错误票据的请求）。
const EvInstallServe = "install_serve"

func (h *Hub) mountInstaller(engine *gin.Engine) {
	engine.GET("/i/:ticket/:name", h.handleInstallAsset)
	engine.HEAD("/i/:ticket/:name", h.handleInstallAsset)
}

// ---------- 票据 ----------

// installTickets 保存「当前发现窗口」的安装票据。票据按窗口纪元（discovery.epoch）换发：
// 窗口关闭后再开启，纪元加一，旧票据立刻失效；常开模式纪元恒为 0，票据在进程生命周期内不变。
type installTickets struct {
	mu    sync.Mutex
	have  bool
	epoch uint64
	token string
}

// forEpoch 返回给定窗口纪元对应的票据；纪元变化时换发新票据。
func (t *installTickets) forEpoch(epoch uint64) string {
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.have || t.epoch != epoch {
		t.token, t.epoch, t.have = randHex(16), epoch, true
	}
	return t.token
}

// valid 常量时间比较：presented 是否是给定窗口纪元的票据。
func (t *installTickets) valid(epoch uint64, presented string) bool {
	want := t.forEpoch(epoch)
	return subtle.ConstantTimeCompare([]byte(presented), []byte(want)) == 1
}

// currentInstallTicket 返回当前有效的安装票据；发现窗口没有开启时返回空串。
func (h *Hub) currentInstallTicket() string {
	active, _, _, epoch := h.disc.snapshot(h.now())
	if !active {
		return ""
	}
	return h.installTickets.forEpoch(epoch)
}

// ---------- 限速 ----------

// installLimiter 是 /i/ 的来源地址限速：每个地址每分钟 installRatePerMinute 次请求（无论成功与否都计数）。
type installLimiter struct {
	mu   sync.Mutex
	now  func() time.Time
	rate map[string]*rateWindow
}

func newInstallLimiter(now func() time.Time) *installLimiter {
	return &installLimiter{now: now, rate: make(map[string]*rateWindow)}
}

func (l *installLimiter) allow(ip string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	if len(l.rate) > 4096 {
		for k, w := range l.rate {
			if now.Sub(w.start) >= time.Minute {
				delete(l.rate, k)
			}
		}
	}
	w := l.rate[ip]
	if w == nil || now.Sub(w.start) >= time.Minute {
		w = &rateWindow{start: now}
		l.rate[ip] = w
	}
	if w.n >= installRatePerMinute {
		return false
	}
	w.n++
	return true
}

// ---------- 状态 ----------

// InstallStatus 是 /status 里的「安装源」部分。
type InstallStatus struct {
	// Available 表示分发目录里至少有一个 Agent 二进制：为 false 时前端应回退为 GitHub Release 来源的命令。
	Available bool `json:"available"`
	// Ticket 是当前发现窗口的安装票据，只在窗口开启期间返回。
	Ticket string `json:"ticket,omitempty"`
	// Platforms 是可分发的平台（如 linux-amd64）。
	Platforms []string `json:"platforms,omitempty"`
	// SPKIPin 是内置 HTTPS 叶子证书的公钥指纹（curl --pinnedpubkey 格式），用于自签名证书场景下校验下载。
	SPKIPin string `json:"spki_pin,omitempty"`
	// Version 是这个 Hub 对应的 GitHub Release 标签（vX.Y.Z 或 vX.Y.Z-rc.N）；开发版与未知版本为空。
	// 不论有没有安装源都返回：没有安装源（单二进制、go run）时，前端回退到 GitHub 来源的命令，必须据此把脚本地址与
	// RELAYMESH_VERSION 固定成这个版本——脚本默认取「最新正式版」，预览期间那里没有 Agent，命令会失败。
	// 值经 buildinfo.ReleaseTag 重新拼出，只含数字、点、连字符、小写字母与前缀 v。
	Version string `json:"version,omitempty"`
}

func (h *Hub) installStatus() *InstallStatus {
	assets := h.dist.List()
	st := &InstallStatus{Available: len(assets) > 0, Version: buildinfo.ReleaseTag(h.version)}
	if !st.Available {
		return st
	}
	for _, a := range assets {
		st.Platforms = append(st.Platforms, a.Platform())
	}
	st.SPKIPin = h.currentTLSSPKI()
	st.Ticket = h.currentInstallTicket()
	return st
}

// ---------- 下载 ----------

func (h *Hub) handleInstallAsset(c *gin.Context) {
	hdr := c.Writer.Header()
	hdr.Set("Cache-Control", "no-store")
	hdr.Set("Referrer-Policy", "no-referrer")
	hdr.Set("X-Content-Type-Options", "nosniff")
	notFound := func() { c.String(http.StatusNotFound, "not found") }

	ip := c.ClientIP()
	name := c.Param("name")

	if !h.installLimit.allow(ip) {
		hdr.Set("Retry-After", "30")
		c.String(http.StatusTooManyRequests, "too many requests")
		return
	}

	// 先做不碰数据库的判断：票据匹配（发现窗口的票据，或某台主机升级窗口的票据）、名字合法。任何一项不满足都是同一个 404。
	now := h.now()
	active, _, _, epoch := h.disc.snapshot(now)
	validName := installer.IsScript(name) || name == installer.ChecksumsName || installer.ValidAssetName(name)
	discOK := active && h.installTickets.valid(epoch, c.Param("ticket"))
	upgradeHost, upgradeOK := h.upgrades.ticketHost(c.Param("ticket"), now)
	if !validName || !(discOK || upgradeOK) {
		// 拒绝事件与成功事件使用不同的限流键：来路不明的探测不能把真正的安装记录挤掉。
		h.audit.recordLimited(h.ctx, "deny|"+ip, auditEvent{
			Event: EvInstallServe, Result: ResultDeny, Actor: "installer-client", IP: ip,
			Detail: "name=" + clipName(name),
		})
		notFound()
		return
	}
	if !h.gate.Evaluate(c.Request.Context()).Open {
		notFound()
		return
	}
	// 用升级窗口的票据取文件时，审计里注明是哪台主机的升级窗口（发现窗口的票据不带这一项）。
	via := ""
	if !discOK {
		via = " window=upgrade host=" + upgradeHost
	}

	switch {
	case installer.IsScript(name):
		h.serveInstallScript(c, name, ip, via)
	case name == installer.ChecksumsName:
		sums, err := h.dist.Checksums()
		if err != nil {
			notFound()
			return
		}
		hdr.Set("Content-Type", "text/plain; charset=utf-8")
		http.ServeContent(c.Writer, c.Request, name, time.Time{}, bytes.NewReader(sums))
		h.recordInstallServe(ip, name, int64(len(sums)), via)
	default:
		h.serveInstallBinary(c, name, ip, via)
	}
}

func (h *Hub) serveInstallScript(c *gin.Context, name, ip, via string) {
	body, err := installer.Render(name, installer.Prelude{
		Ticket:  c.Param("ticket"),
		SPKIPin: h.currentTLSSPKI(),
	})
	if err != nil {
		c.String(http.StatusInternalServerError, "script unavailable")
		return
	}
	c.Writer.Header().Set("Content-Type", installer.ContentType(name))
	http.ServeContent(c.Writer, c.Request, name, time.Time{}, bytes.NewReader(body))
	h.recordInstallServe(ip, name, int64(len(body)), via)
}

func (h *Hub) serveInstallBinary(c *gin.Context, name, ip, via string) {
	select {
	case h.installSem <- struct{}{}:
		defer func() { <-h.installSem }()
	default:
		c.Writer.Header().Set("Retry-After", "2")
		c.String(http.StatusServiceUnavailable, "busy")
		return
	}
	f, st, err := h.dist.Open(name)
	if err != nil {
		c.String(http.StatusNotFound, "not found")
		return
	}
	defer f.Close()
	hdr := c.Writer.Header()
	hdr.Set("Content-Type", installer.ContentType(name))
	// name 已通过白名单，只含字母、数字与连字符、点，直接放进 filename 是安全的。
	hdr.Set("Content-Disposition", `attachment; filename="`+name+`"`)
	http.ServeContent(c.Writer, c.Request, name, st.ModTime(), f)
	h.recordInstallServe(ip, name, st.Size(), via)
}

func (h *Hub) recordInstallServe(ip, name string, size int64, via string) {
	h.audit.recordLimited(h.ctx, ip, auditEvent{
		Event: EvInstallServe, Actor: "installer-client", IP: ip,
		Detail: "name=" + name + " size=" + strconv.FormatInt(size, 10) + via,
	})
}

// clipName 截断审计里的文件名：被拒绝的请求里 name 是攻击者可控的。
func clipName(name string) string {
	name = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return '_'
		}
		return r
	}, name)
	if len(name) > 64 {
		name = name[:64]
	}
	return name
}
