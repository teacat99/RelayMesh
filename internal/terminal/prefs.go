package terminal

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"sync"

	"github.com/gin-gonic/gin"

	"github.com/teacat99/RelayMesh/v2/internal/sessionprompt"
	"github.com/teacat99/RelayMesh/v2/internal/store"
	"github.com/teacat99/RelayMesh/v2/internal/termproto"
)

// 终端偏好（宽度保持、终端限高）。它们保存在 Hub（system_settings），所有浏览器共享：
//   - 宽度保持只影响浏览器怎么报告终端尺寸，登录即可修改；
//   - 终端限高会让主机丢弃已记录的旧输出，属于数据保留策略：必须刚刚重新输入过密码、写审计，并立刻下发给在线主机。

// prefsCache 是偏好在内存里的副本：握手与策略下发都要读它，不必每次查库。
type prefsCache struct {
	mu     sync.RWMutex
	loaded bool
	v      store.TerminalPrefs
}

// termPrefs 返回当前的终端偏好。读库失败时按默认值（全部关闭）处理且不缓存，下次再试。
func (h *Hub) termPrefs(ctx context.Context) store.TerminalPrefs {
	h.prefs.mu.RLock()
	if h.prefs.loaded {
		v := h.prefs.v
		h.prefs.mu.RUnlock()
		return v
	}
	h.prefs.mu.RUnlock()
	v, err := h.store.GetTerminalPrefs(ctx)
	if err != nil {
		log.Printf("[terminal] 读取终端偏好失败（按默认值处理）: %v", err)
		return store.TerminalPrefs{}
	}
	h.prefs.mu.Lock()
	h.prefs.v, h.prefs.loaded = v, true
	h.prefs.mu.Unlock()
	return v
}

func (h *Hub) cachePrefs(v store.TerminalPrefs) {
	h.prefs.mu.Lock()
	h.prefs.v, h.prefs.loaded = v, true
	h.prefs.mu.Unlock()
}

// PrefsLimits 是各项偏好的取值范围，界面据此校验输入。
type PrefsLimits struct {
	FixedColsMin      int `json:"fixed_cols_min"`
	FixedColsMax      int `json:"fixed_cols_max"`
	RecordMaxLinesMin int `json:"record_max_lines_min"`
	RecordMaxLinesMax int `json:"record_max_lines_max"`
	// 终端启动器预设：数量与各字段的最大长度（按字符数）。
	LauncherMax        int `json:"launcher_max"`
	LauncherNameMax    int `json:"launcher_name_max"`
	LauncherDirMax     int `json:"launcher_dir_max"`
	LauncherCommandMax int `json:"launcher_command_max"`
	// 「非交互续聊」预设（#126）：各文本字段的上限（按字符数）与默认提示词（%workflow 原样保留）。
	// 旧版 Hub 没有这几项——界面据此判断这台 Hub 认不认识这种预设。
	LauncherRunPromptMax     int    `json:"launcher_run_prompt_max"`
	LauncherRunModelMax      int    `json:"launcher_run_model_max"`
	LauncherRunBinMax        int    `json:"launcher_run_bin_max"`
	LauncherRunPromptDefault string `json:"launcher_run_prompt_default"`
	// LauncherRunNewPromptDefault 是「新开对话」（#176）没写自定义提示词时服务端用的默认正文（%workflow 原样保留）；
	// 旧版 Hub 没有它——界面据此判断这台 Hub 认不认识「新开对话」与「启用 Agent」。
	LauncherRunNewPromptDefault string `json:"launcher_run_new_prompt_default"`
	// LauncherRunMaxMinutes 是预设「最长运行时间」（#141）的上限，也是没填时的默认值（分钟）：平台单次运行上限 7 天；下限固定 1 分钟。
	// 旧版 Hub 没有这一项——界面据此决定要不要显示这个输入框。
	LauncherRunMaxMinutes int `json:"launcher_run_max_minutes"`
}

// RecordLinesCoverage 说明「终端限高」对在线主机的覆盖情况：升级只替换 Agent，不会重启会话守护进程，
// 而按行数丢弃是守护进程里的记录引擎做的，所以旧的守护进程会照旧只按字节配额滚动。
type RecordLinesCoverage struct {
	// Online 是当前在线且已启用的主机数；Unsupported 是其中守护进程还不支持按行数限高的主机数。
	Online      int `json:"online"`
	Unsupported int `json:"unsupported"`
}

// PrefsView 是返回给界面的终端偏好。
type PrefsView struct {
	FixedCols      int         `json:"fixed_cols"`
	RecordMaxLines int         `json:"record_max_lines"`
	Limits         PrefsLimits `json:"limits"`
	// AgentAutoUpgrade 是「Agent 自动升级」总闸（默认关）。旧版 Hub 没有这一项——界面据此判断这台 Hub 认不认识自动升级。
	AgentAutoUpgrade bool `json:"agent_auto_upgrade"`
	// RecordLines 仅在设了行数上限时返回。
	RecordLines *RecordLinesCoverage `json:"record_lines,omitempty"`
	// Launchers 是终端启动器的预设（始终是数组，没有预设时为空数组）。它们另存一个键，随偏好一起返回，界面一次读取就够。
	Launchers []store.TerminalLauncher `json:"launchers"`
}

// prefsView 汇总当前偏好与取值范围。
func (h *Hub) prefsView(ctx context.Context) *PrefsView {
	p := h.termPrefs(ctx)
	launchers, err := h.store.ListTerminalLaunchers(ctx)
	if err != nil {
		// 读不到预设不该让整个状态接口失败（总闸、解锁状态都在里面）：按「没有预设」处理，下次轮询再试。
		log.Printf("[terminal] 读取终端启动预设失败（按没有预设处理）: %v", err)
		launchers = []store.TerminalLauncher{}
	}
	v := &PrefsView{
		FixedCols:        p.FixedCols,
		RecordMaxLines:   p.RecordMaxLines,
		AgentAutoUpgrade: p.AgentAutoUpgrade,
		Launchers:        launchers,
		Limits: PrefsLimits{
			FixedColsMin: store.MinFixedCols, FixedColsMax: store.MaxFixedCols,
			RecordMaxLinesMin: store.MinRecordMaxLines, RecordMaxLinesMax: store.MaxRecordMaxLines,
			LauncherMax: store.MaxTerminalLaunchers, LauncherNameMax: store.MaxLauncherNameRunes,
			LauncherDirMax: store.MaxLauncherDirRunes, LauncherCommandMax: store.MaxLauncherCommandRunes,
			// 两个默认正文都是内置模板（带 %变量原样保留）压成一行：预设里的正文必须是一行，且不得超过上面的字数上限，
			// 这样「以默认为底稿」填进去一定能存。会话模板与全局覆盖排在内置默认前面，见 store.ResolveWakeTemplate。
			LauncherRunPromptMax: store.MaxHeadlessPromptRunes, LauncherRunModelMax: store.MaxHeadlessModelRunes,
			LauncherRunBinMax:           store.MaxHeadlessBinRunes,
			LauncherRunPromptDefault:    sessionprompt.OneLine(sessionprompt.Builtin(sessionprompt.KindResume, true)),
			LauncherRunNewPromptDefault: sessionprompt.OneLine(sessionprompt.Builtin(sessionprompt.KindNew, true)),
			LauncherRunMaxMinutes:       store.MaxHeadlessRunMinutes,
		},
	}
	if p.RecordMaxLines > 0 {
		cov := &RecordLinesCoverage{}
		for _, a := range h.snapshotAgents() {
			if !a.isEnabled() {
				continue
			}
			cov.Online++
			if !a.hasHolderCap(termproto.HolderCapRecLines) {
				cov.Unsupported++
			}
		}
		v.RecordLines = cov
	}
	return v
}

type displayPrefsRequest struct {
	FixedCols int `json:"fixed_cols"`
}

// handlePrefsDisplay 修改「宽度保持」：0 关闭；否则固定为该列数。只改浏览器怎么上报尺寸，不涉及任何数据，登录即可。
func (h *Hub) handlePrefsDisplay(c *gin.Context) {
	var req displayPrefsRequest
	if !bindJSON(c, &req) {
		return
	}
	ctx := c.Request.Context()
	p, err := h.store.UpdateTerminalPrefs(ctx, func(p *store.TerminalPrefs) { p.FixedCols = req.FixedCols })
	if err != nil {
		h.failPrefs(c, err)
		return
	}
	h.cachePrefs(p)
	c.JSON(http.StatusOK, gin.H{"prefs": h.prefsView(ctx)})
}

type retentionPrefsRequest struct {
	RecordMaxLines int `json:"record_max_lines"`
}

// handlePrefsRetention 修改「终端限高」：0 不按行数限制；否则每个会话的记录至少保留最近这么多行，更早的滚动丢弃。
// 它会让主机丢弃已记录的旧内容，所以放在需要刚刚解锁的路由组里，并写审计、立刻下发给全部在线主机。
func (h *Hub) handlePrefsRetention(c *gin.Context) {
	var req retentionPrefsRequest
	if !bindJSON(c, &req) {
		return
	}
	ctx := c.Request.Context()
	before := h.termPrefs(ctx)
	p, err := h.store.UpdateTerminalPrefs(ctx, func(p *store.TerminalPrefs) { p.RecordMaxLines = req.RecordMaxLines })
	if err != nil {
		h.failPrefs(c, err)
		return
	}
	h.cachePrefs(p)
	if p.RecordMaxLines != before.RecordMaxLines {
		h.audit.record(ctx, auditEvent{
			Event: EvRecordPolicy, Actor: subjectOf(c), IP: c.ClientIP(),
			Detail: fmt.Sprintf("max_lines=%d", p.RecordMaxLines),
		})
		h.pushPolicyToAll(ctx)
	}
	c.JSON(http.StatusOK, gin.H{"prefs": h.prefsView(ctx)})
}

type autoUpgradePrefsRequest struct {
	// Enabled 用指针：缺省不能被当成「关闭」（一个空请求体不该悄悄改动总闸）。
	Enabled *bool `json:"enabled"`
}

// handlePrefsAutoUpgrade 打开 / 关闭「Agent 自动升级」总闸（默认关）。
// 打开它不会让任何主机自动升级——每台主机仍要由它的所有者在本机同意（relaymesh-agent auto-upgrade enable）；
// 但它放开了「Hub 回答主机的版本检查」，所以和其他会改变主机行为的设置一样，放在需要刚刚解锁的路由组里并写审计。
func (h *Hub) handlePrefsAutoUpgrade(c *gin.Context) {
	var req autoUpgradePrefsRequest
	if !bindJSON(c, &req) {
		return
	}
	if req.Enabled == nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "缺少 enabled", "code": "bad_prefs"})
		return
	}
	ctx := c.Request.Context()
	before := h.termPrefs(ctx)
	p, err := h.store.UpdateTerminalPrefs(ctx, func(p *store.TerminalPrefs) { p.AgentAutoUpgrade = *req.Enabled })
	if err != nil {
		h.failPrefs(c, err)
		return
	}
	h.cachePrefs(p)
	if p.AgentAutoUpgrade != before.AgentAutoUpgrade {
		h.audit.record(ctx, auditEvent{
			Event: EvAutoUpgradeSwitch, Actor: subjectOf(c), IP: c.ClientIP(),
			Detail: fmt.Sprintf("enabled=%t", p.AgentAutoUpgrade),
		})
	}
	c.JSON(http.StatusOK, gin.H{"prefs": h.prefsView(ctx)})
}

// pushPolicyToAll 把最新的记录策略下发给所有在线且已启用的主机（离线的主机下次握手时会拿到）。
func (h *Hub) pushPolicyToAll(ctx context.Context) {
	for _, a := range h.snapshotAgents() {
		if !a.isEnabled() {
			continue
		}
		_ = a.writeJSON(termproto.PolicyMsg{T: termproto.TPolicy, Policy: h.policyFor(ctx, a.code)})
	}
}

func (h *Hub) failPrefs(c *gin.Context, err error) {
	if errors.Is(err, store.ErrInvalidTerminalPrefs) {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error(), "code": "bad_prefs"})
		return
	}
	log.Printf("[terminal] 保存终端偏好失败: %v", err)
	h.fail(c, err)
}
