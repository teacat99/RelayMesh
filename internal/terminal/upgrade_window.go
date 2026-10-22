package terminal

import (
	"context"
	"crypto/subtle"
	"net/http"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/teacat99/RelayMesh/v2/internal/model"
	"github.com/teacat99/RelayMesh/v2/internal/termproto"
)

// 升级窗口：给「某一台」主机的 Agent 升级用的、限时的安装源访问权。
//
// 为什么需要它：Agent 的升级始终是「主机自己去取」，服务端从不向主机推送可执行内容——那等于给每台主机开一个远程代码执行面。
// 两种升级都走这条拉取通道：一是主机的主人在主机上执行一条命令（这条命令要从服务端取安装脚本与新版 Agent）；
// 二是主人在主机上明确允许后，管理员在 Web 点「一键升级」，由 Agent 自己用同一张票据去取（见 web_upgrade.go）。
// 而 /i/ 平时只在「发现窗口」开着时才放行。升级窗口就是为这一台主机单独开一小段时间：
//   - 每台主机最多一个窗口，默认 10 分钟，可延时（每次 +10 分钟，剩余时间不超过 60 分钟）、可提前结束；
//   - 票据 128 位随机，仅存于内存（服务端重启即全部关闭），常量时间比较，/i/ 对所有失败仍然只回同一个 404；
//   - 它只放行 /i/ 里本来就公开的文件（安装脚本、校验文件、各平台 Agent 二进制），不放行别的任何东西；
//   - 窗口到期、被手动结束、主机被停用 / 删除，或检测到这台主机的 Agent 已经换了版本（升级成功）时自动关闭。
const (
	// DefaultUpgradeWindow 是升级窗口的默认时长，也是每次「延时」增加的时长。
	DefaultUpgradeWindow = 10 * time.Minute
	// MaxUpgradeWindow 是窗口剩余时间的上限。
	MaxUpgradeWindow = 60 * time.Minute
)

// upgradeWin 是某台主机当前的升级窗口。
type upgradeWin struct {
	ticket string
	until  time.Time
	// baseVersion 是开窗时这台主机的 Agent 版本：之后握手时报告的版本与它不同，就认为升级已经发生。
	baseVersion string
}

// upgradeWindows 是全部升级窗口（机器码 → 窗口）。窗口数不会超过主机数，且只在管理员操作时创建。
type upgradeWindows struct {
	mu sync.Mutex
	m  map[string]*upgradeWin
}

// UpgradeWindowView 是返回给界面的升级窗口状态。Ticket 只在窗口开着时返回，界面据此拼出升级命令。
type UpgradeWindowView struct {
	Open             bool      `json:"open"`
	Ticket           string    `json:"ticket"`
	Until            time.Time `json:"until"`
	RemainingSeconds int       `json:"remaining_seconds"`
	// BaseVersion 是开窗时这台主机的 Agent 版本。
	BaseVersion string `json:"base_version,omitempty"`
}

func clampUpgradeDuration(d time.Duration) time.Duration {
	if d <= 0 {
		return DefaultUpgradeWindow
	}
	if d > MaxUpgradeWindow {
		return MaxUpgradeWindow
	}
	return d
}

func (w *upgradeWin) view(now time.Time) *UpgradeWindowView {
	rem := int(w.until.Sub(now).Seconds())
	if rem < 0 {
		rem = 0
	}
	return &UpgradeWindowView{Open: true, Ticket: w.ticket, Until: w.until, RemainingSeconds: rem, BaseVersion: w.baseVersion}
}

// live 返回未到期的窗口；已到期的留给 sweep 收尾（审计在那里记），这里按「没有窗口」处理。
func (u *upgradeWindows) live(code string, now time.Time) *upgradeWin {
	w := u.m[code]
	if w == nil || !now.Before(w.until) {
		return nil
	}
	return w
}

// open 为一台主机打开升级窗口。已经开着时保持原票据（已经复制出去的命令继续可用），并把剩余时间补到至少 dur。
func (u *upgradeWindows) open(code, baseVersion string, now time.Time, dur time.Duration) UpgradeWindowView {
	dur = clampUpgradeDuration(dur)
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.m == nil {
		u.m = make(map[string]*upgradeWin)
	}
	w := u.live(code, now)
	if w == nil {
		w = &upgradeWin{ticket: randHex(16), baseVersion: baseVersion}
		u.m[code] = w
		w.until = now.Add(dur)
		return *w.view(now)
	}
	if next := now.Add(dur); next.After(w.until) {
		w.until = next
	}
	return *w.view(now)
}

// extend 在现有剩余时间上再加 add（剩余总量不超过 MaxUpgradeWindow）。窗口没开着时返回 false。
func (u *upgradeWindows) extend(code string, now time.Time, add time.Duration) (UpgradeWindowView, bool) {
	add = clampUpgradeDuration(add)
	u.mu.Lock()
	defer u.mu.Unlock()
	w := u.live(code, now)
	if w == nil {
		return UpgradeWindowView{}, false
	}
	next := w.until.Add(add)
	if limit := now.Add(MaxUpgradeWindow); next.After(limit) {
		next = limit
	}
	w.until = next
	return *w.view(now), true
}

// close 结束一台主机的升级窗口，返回此前是否开着。
func (u *upgradeWindows) close(code string, now time.Time) bool {
	u.mu.Lock()
	defer u.mu.Unlock()
	w := u.m[code]
	delete(u.m, code)
	return w != nil && now.Before(w.until)
}

// view 返回窗口状态；没有开着的窗口时返回 nil。
func (u *upgradeWindows) view(code string, now time.Time) *UpgradeWindowView {
	u.mu.Lock()
	defer u.mu.Unlock()
	if w := u.live(code, now); w != nil {
		return w.view(now)
	}
	return nil
}

// ticketHost 判断 presented 是否是某个开着的升级窗口的票据，是则返回对应主机。
// 对每个窗口都做常量时间比较并走完全部窗口，不因票据对不对或在第几个而产生可观察的时间差。
func (u *upgradeWindows) ticketHost(presented string, now time.Time) (string, bool) {
	u.mu.Lock()
	defer u.mu.Unlock()
	host, found := "", 0
	for code, w := range u.m {
		if !now.Before(w.until) {
			continue
		}
		if subtle.ConstantTimeCompare([]byte(presented), []byte(w.ticket)) == 1 {
			host, found = code, 1
		}
	}
	return host, found == 1
}

// observe 在主机（重新）上线时调用：报告的版本与开窗时不同，说明升级已经发生，关闭窗口并返回开窗时的版本。
func (u *upgradeWindows) observe(code, version string, now time.Time) (from string, closed bool) {
	u.mu.Lock()
	defer u.mu.Unlock()
	w := u.live(code, now)
	if w == nil || version == "" || version == w.baseVersion {
		return "", false
	}
	delete(u.m, code)
	return w.baseVersion, true
}

// sweep 清掉已到期的窗口，返回它们的机器码（每个只返回一次，用于记审计）。
func (u *upgradeWindows) sweep(now time.Time) []string {
	u.mu.Lock()
	defer u.mu.Unlock()
	var out []string
	for code, w := range u.m {
		if !now.Before(w.until) {
			delete(u.m, code)
			out = append(out, code)
		}
	}
	return out
}

// ---------- Hub 侧 ----------

// 升级窗口无法开启的原因。
type upgradeWindowError struct {
	status int
	code   string
	msg    string
}

func (e *upgradeWindowError) Error() string { return e.msg }

// upgradeTarget 返回可以开升级窗口的主机当前的 Agent 版本；主机必须是已启用的、装了 Agent 的持久主机。
func (h *Hub) upgradeTarget(ctx context.Context, code string) (string, *upgradeWindowError) {
	rec, err := h.store.GetTerminalHost(ctx, code)
	if err != nil {
		return "", &upgradeWindowError{http.StatusInternalServerError, "internal_error", "操作失败，请查看服务端日志"}
	}
	switch {
	case rec == nil:
		return "", &upgradeWindowError{http.StatusNotFound, "host_not_found", "主机不存在或状态不符"}
	case rec.IsSSH():
		return "", &upgradeWindowError{http.StatusConflict, "not_agent", "这是 SSH 主机，没有需要升级的 Agent"}
	case rec.Status != termproto.HostStatusEnabled:
		return "", &upgradeWindowError{http.StatusConflict, "host_not_enabled", "只有已启用的主机才能开启升级窗口"}
	}
	if !h.dist.Available() {
		return "", &upgradeWindowError{http.StatusConflict, "install_source_unavailable",
			"这台服务端没有附带 Agent 安装文件，无法提供升级：请改用 GitHub Release 的安装命令，或使用附带了 Agent 的服务端镜像"}
	}
	return rec.AgentVersion, nil
}

// openUpgradeWindow 开启（或在已开着时延时）一台主机的升级窗口。extend 为 true 时是「延时」：在现有剩余时间上累加。
func (h *Hub) openUpgradeWindow(ctx context.Context, code, actor, ip string, dur time.Duration, extend bool) (*UpgradeWindowView, *upgradeWindowError) {
	base, werr := h.upgradeTarget(ctx, code)
	if werr != nil {
		return nil, werr
	}
	now := h.now()
	if extend {
		if v, ok := h.upgrades.extend(code, now, dur); ok {
			h.audit.record(ctx, auditEvent{Event: EvUpgradeWindow, Actor: actor, IP: ip, MachineCode: code, Detail: "extend until=" + v.Until.UTC().Format(time.RFC3339)})
			return &v, nil
		}
	}
	v := h.upgrades.open(code, base, now, dur)
	h.audit.record(ctx, auditEvent{Event: EvUpgradeWindow, Actor: actor, IP: ip, MachineCode: code, Detail: "open from=" + base + " until=" + v.Until.UTC().Format(time.RFC3339)})
	return &v, nil
}

// closeUpgradeWindow 结束一台主机的升级窗口；reason 写进审计（manual / disabled / deleted …）。
func (h *Hub) closeUpgradeWindow(ctx context.Context, code, actor, ip, reason string) bool {
	if !h.upgrades.close(code, h.now()) {
		return false
	}
	h.audit.record(ctx, auditEvent{Event: EvUpgradeWindow, Actor: actor, IP: ip, MachineCode: code, Detail: "close " + reason})
	return true
}

// upgradeSeen 在已启用的 Agent 上线时调用：版本变了就说明升级成功，自动关闭这台主机的升级窗口；
// 同时收尾这台主机上在 Web 发起的升级请求（见 web_upgrade.go）。
func (h *Hub) upgradeSeen(code string, meta termproto.Meta) {
	now := h.now()
	if from, closed := h.upgrades.observe(code, meta.Ver, now); closed {
		h.audit.record(h.ctx, auditEvent{Event: EvUpgradeWindow, Actor: ActorSystem, MachineCode: code, Detail: "close upgraded from=" + from + " to=" + meta.Ver})
	}
	if job, settled := h.webUpgrades.settleOnline(code, meta, now); settled {
		h.finishUpgradeJob(job)
	}
}

// sweepUpgradeWindows 由周期任务调用：收尾已到期的窗口。
func (h *Hub) sweepUpgradeWindows() {
	for _, code := range h.upgrades.sweep(h.now()) {
		h.audit.record(h.ctx, auditEvent{Event: EvUpgradeWindow, Actor: ActorSystem, MachineCode: code, Detail: "close expired"})
	}
}

// fillUpgrade 给主机视图填上升级提醒与窗口状态（只对装了 Agent 的持久主机）。
// latest / latestRev 是服务端自带这批 Agent 的版本与代码指纹（后者可为空）；判断规则见 judgeUpgrade。
func (h *Hub) fillUpgrade(v *HostView, latest, latestRev string, now time.Time) {
	if v.Kind != model.HostKindAgent || v.Temporary {
		return
	}
	if latest != "" {
		v.LatestAgentVersion = latest
		v.LatestAgentRev = latestRev
		verdict := judgeUpgrade(v.AgentVersion, v.AgentRev, latest, latestRev)
		v.UpgradeAvailable, v.UpgradeKind, v.UpToDateByRev = verdict.Available, verdict.Kind, verdict.SameCode
	}
	v.UpgradeWindow = h.upgrades.view(v.MachineCode, now)
	v.UpgradeJob = h.webUpgrades.view(v.MachineCode, now)
}

// ---------- REST ----------

type upgradeWindowRequest struct {
	Minutes int `json:"minutes"`
	// Extend 为 true 时是「延时」：在现有剩余时间上再加 Minutes 分钟（总剩余不超过上限）。
	Extend bool `json:"extend"`
}

func (h *Hub) writeUpgradeError(c *gin.Context, e *upgradeWindowError) {
	c.JSON(e.status, gin.H{"error": e.msg, "code": e.code})
}

// POST /hosts/:code/upgrade-window —— 开启 / 延时这台主机的升级窗口。
func (h *Hub) handleUpgradeWindowOpen(c *gin.Context) {
	code, ok := machineCodeParam(c)
	if !ok {
		return
	}
	var req upgradeWindowRequest
	if c.Request.ContentLength != 0 && !bindJSON(c, &req) {
		return
	}
	view, werr := h.openUpgradeWindow(c.Request.Context(), code, subjectOf(c), c.ClientIP(), time.Duration(req.Minutes)*time.Minute, req.Extend)
	if werr != nil {
		h.writeUpgradeError(c, werr)
		return
	}
	c.JSON(http.StatusOK, gin.H{"upgrade_window": view})
}

// DELETE /hosts/:code/upgrade-window —— 提前结束升级窗口。
func (h *Hub) handleUpgradeWindowClose(c *gin.Context) {
	code, ok := machineCodeParam(c)
	if !ok {
		return
	}
	h.closeUpgradeWindow(c.Request.Context(), code, subjectOf(c), c.ClientIP(), "manual")
	c.JSON(http.StatusOK, gin.H{"upgrade_window": nil})
}
