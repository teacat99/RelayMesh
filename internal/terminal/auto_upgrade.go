package terminal

import (
	"context"
	"fmt"
	"time"

	"github.com/teacat99/RelayMesh/v2/internal/termproto"
)

// 自动升级（Hub 侧）。方案见文档库 design/agent-auto-upgrade.md。
//
// 信任边界：
//   - 是否自动升级由主机所有者在主机本地决定（relaymesh-agent auto-upgrade enable），Hub 无法替他选，也无法远程打开；
//   - Hub 从不主动推送：它只回答 Agent 主动发来的检查（auto_upgrade_check）；
//   - Hub 一侧只有一个「总闸」（终端偏好里的 agent_auto_upgrade，默认关）：关着时一律回答 disabled；
//   - 回答只依据 Agent 握手时声明的版本 / 代码指纹 / 平台 / 模式，不取检查帧里的任何内容（帧里只有标识）。
//
// 检查这一层只有「提示」能力：回答里最多带上最新版本号，不带票据、下载地址或校验值，所以 Agent 拿不到任何可下载的东西。
//
// 静默模式（见文件后半部分的 onAutoUpgradeRequest）在检查之后多一步：Agent 自己判断「该升级了」，再发一个请求，Hub 才会
// 回一个带下载凭据的批准。没有请求就没有批准；批准里的版本必须是请求的那个，且 Agent 会自己验发布签名——所以即使
// Hub 被攻破，最坏也只是让静默主机什么都不升级，而不是运行没有签名的程序。

// autoCheckMinInterval 是同一个连接上两次检查之间的最短间隔；更频繁的检查得到 too_soon。
// 正常的 Agent 每隔几个小时才检查一次，这个间隔只是挡住失控的循环。是变量只为了让测试不必真等。
var autoCheckMinInterval = 10 * time.Second

// AutoCheckView 是一台主机最近一次自动升级检查的结果。只在内存里：Agent 重连后会重新检查。
type AutoCheckView struct {
	// At 是 Hub 受理这次检查的时刻。
	At time.Time `json:"at"`
	// Status 是 Hub 的回答（termproto.AutoOffer*）。
	Status string `json:"status"`
	// Version 是 Hub 上最新的 Agent 版本（回答 available / up_to_date 时有）。
	Version string `json:"version,omitempty"`
}

// autoCheckView 返回这个连接最近一次检查结果的副本；还没检查过则为 nil。
func (a *agentConn) autoCheckView() *AutoCheckView {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.autoCheck == nil {
		return nil
	}
	v := *a.autoCheck
	return &v
}

// AutoGrantView 是一台主机最近一次静默升级请求得到的裁决，供界面解释「它为什么没有（或已经）自动升级」。
// 只在内存里；不含凭据——批准的票据、校验值和签名只在发给 Agent 的那一帧里。
type AutoGrantView struct {
	// At 是 Hub 受理这次请求的时刻。
	At time.Time `json:"at"`
	// Status 是裁决（termproto.AutoGrant*）。
	Status string `json:"status"`
	// Version 是请求要升级到的版本。
	Version string `json:"version,omitempty"`
}

// autoGrantView 返回这个连接最近一次请求裁决的副本；还没请求过则为 nil。
func (a *agentConn) autoGrantView() *AutoGrantView {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.autoGrant == nil {
		return nil
	}
	v := *a.autoGrant
	return &v
}

// onAutoUpgradeCheck 处理 Agent 的一次检查并回答。字段不合规的检查直接忽略（正常的 Agent 不会发出）。
func (h *Hub) onAutoUpgradeCheck(a *agentConn, m termproto.AutoUpgradeCheck) {
	if m.Validate() != nil {
		return
	}
	offer := h.answerAutoCheck(h.ctx, a, h.now())
	offer.T, offer.ID = termproto.TAutoUpgradeOffer, m.ID
	_ = a.writeJSON(offer)
}

// answerAutoCheck 决定对一次检查怎么回答，并记下结果供主机视图显示。
func (h *Hub) answerAutoCheck(ctx context.Context, a *agentConn, now time.Time) termproto.AutoUpgradeOffer {
	a.mu.Lock()
	tooSoon := !a.autoCheckAt.IsZero() && now.Sub(a.autoCheckAt) < autoCheckMinInterval
	if !tooSoon {
		a.autoCheckAt = now
	}
	a.mu.Unlock()
	if tooSoon {
		// 太频繁的检查不覆盖上一次的结果：界面看到的仍是最近一次正常的回答。
		return termproto.AutoUpgradeOffer{Status: termproto.AutoOfferTooSoon}
	}

	offer := h.judgeAutoCheck(ctx, a)
	a.mu.Lock()
	a.autoCheck = &AutoCheckView{At: now, Status: offer.Status, Version: offer.Version}
	a.mu.Unlock()
	return offer
}

// judgeAutoCheck 是回答的决策：先看这台主机自己的同意，再看 Hub 的总闸，最后比较版本。
//
// 只有「能确定主机的版本更旧」（older）才回答 available：版本只升不降，不拿无法比较新旧的构建去「升级」主机
// （与 Web 一键升级里 changed / different 的处理不同——那是管理员亲手点的，这里是无人值守的）。
func (h *Hub) judgeAutoCheck(ctx context.Context, a *agentConn) termproto.AutoUpgradeOffer {
	switch a.meta.AutoUpgrade {
	case termproto.AutoUpgradeNotify, termproto.AutoUpgradeSilent:
	default:
		// off、没有声明，以及临时身份（它们不声明任何模式）。
		return termproto.AutoUpgradeOffer{Status: termproto.AutoOfferNotAllowed}
	}
	if a.temp {
		return termproto.AutoUpgradeOffer{Status: termproto.AutoOfferNotAllowed}
	}
	if !h.termPrefs(ctx).AgentAutoUpgrade {
		return termproto.AutoUpgradeOffer{Status: termproto.AutoOfferDisabled}
	}

	latest := h.dist.Version()
	if latest == "" {
		return termproto.AutoUpgradeOffer{Status: termproto.AutoOfferUnavailable}
	}
	if _, ok := h.distSHA256(a.meta.Plat); !ok {
		return termproto.AutoUpgradeOffer{Status: termproto.AutoOfferUnavailable}
	}
	if v := judgeUpgrade(a.meta.Ver, a.meta.Rev, latest, h.dist.Rev()); v.Available && v.Kind == upgradeKindOlder {
		return termproto.AutoUpgradeOffer{Status: termproto.AutoOfferAvailable, Version: latest}
	}
	return termproto.AutoUpgradeOffer{Status: termproto.AutoOfferUpToDate, Version: latest}
}

// ---------- 静默升级的请求与批准 ----------

// autoRequestMinInterval 是同一个连接上两次升级请求之间的最短间隔；更频繁的请求得到 too_soon。
// 正常的 Agent 每个周期（至少 15 分钟）最多发一次，这个间隔只是挡住失控的循环。是变量只为了让测试不必真等。
var autoRequestMinInterval = 10 * time.Second

// autoClaim 是批准一次静默升级时登记下的东西：失败时要原样撤销，成功时用来记审计。
type autoClaim struct {
	job      upgradeJob
	wasOpen  bool // 批准之前这台主机的升级窗口就已经开着（管理员为手动升级开的）：撤销时不能把它一起关掉
	sessions WebUpgradeSessions
}

// onAutoUpgradeRequest 处理静默模式 Agent 的升级请求并回答。字段不合规的请求直接忽略（正常的 Agent 不会发出）。
//
// 批准（granted）才会认领升级请求、打开升级窗口并发出下载凭据；其余都只是一个状态，什么也没有改动。
func (h *Hub) onAutoUpgradeRequest(a *agentConn, m termproto.AutoUpgradeRequest) {
	if m.Validate() != nil {
		return
	}
	now := h.now()
	g, claim := h.judgeAutoUpgrade(h.ctx, a, m, now)
	g.T, g.ID = termproto.TAutoUpgradeGrant, m.ID
	if err := a.writeJSON(g); err != nil {
		// 批准没能送达：Agent 不会去下载，撤销刚才登记的升级请求和（为它新开的）窗口，免得白占着名额。
		if claim != nil {
			h.undoAutoClaim(a.code, claim, now)
		}
		return
	}
	if g.Status != termproto.AutoGrantTooSoon {
		// 太频繁的请求不覆盖上一次的结果：界面看到的仍是最近一次正常的裁决。批准没送达的（上面已经 return）也不记。
		a.mu.Lock()
		a.autoGrant = &AutoGrantView{At: now, Status: g.Status, Version: m.Version}
		a.mu.Unlock()
	}
	if claim != nil {
		h.audit.record(h.ctx, auditEvent{
			Event: EvAutoUpgradeStart, Actor: ActorSystem, MachineCode: a.code,
			Detail: fmt.Sprintf("id=%s from=%s to=%s running=%d model=%d persistent=%t",
				claim.job.id, claim.job.from, claim.job.to, claim.sessions.Running, claim.sessions.ModelRunning, claim.sessions.Persistent),
		})
	}
}

func (h *Hub) undoAutoClaim(code string, c *autoClaim, now time.Time) {
	h.webUpgrades.abort(code, c.job.id)
	if !c.wasOpen {
		h.upgrades.close(code, now)
	}
}

// judgeAutoUpgrade 是批准的决策。顺序（越靠前的判断越便宜、越不泄露信息）：
//
//	限流 → 主机自己声明的同意（必须是 silent，且不是临时身份）→ Hub 的总闸 → 主机记录 / 分发目录
//	→ 版本（只有「确定更旧」，且必须等于请求的版本）→ 这个版本有没有发布签名 → 忙不忙 → 认领 + 开窗口 → 批准。
//
// 返回 claim 非 nil 表示批准了，调用方必须把裁决发出去；发不出去要撤销。
func (h *Hub) judgeAutoUpgrade(ctx context.Context, a *agentConn, m termproto.AutoUpgradeRequest, now time.Time) (termproto.AutoUpgradeGrant, *autoClaim) {
	no := func(status string) (termproto.AutoUpgradeGrant, *autoClaim) {
		return termproto.AutoUpgradeGrant{Status: status}, nil
	}

	a.mu.Lock()
	tooSoon := !a.autoRequestAt.IsZero() && now.Sub(a.autoRequestAt) < autoRequestMinInterval
	if !tooSoon {
		a.autoRequestAt = now
	}
	a.mu.Unlock()
	if tooSoon {
		return no(termproto.AutoGrantTooSoon)
	}

	if a.meta.AutoUpgrade != termproto.AutoUpgradeSilent || a.temp {
		return no(termproto.AutoGrantNotAllowed)
	}
	if !h.termPrefs(ctx).AgentAutoUpgrade {
		return no(termproto.AutoGrantDisabled)
	}
	if _, werr := h.upgradeTarget(ctx, a.code); werr != nil {
		if werr.code == "install_source_unavailable" {
			return no(termproto.AutoGrantUnavailable)
		}
		return no(termproto.AutoGrantNotAllowed) // 主机记录不存在 / 已停用 / 是 SSH 主机：不是一台该被升级的主机
	}

	latest := h.dist.Version()
	name, sum, ok := h.distAsset(a.meta.Plat)
	if latest == "" || !ok {
		return no(termproto.AutoGrantUnavailable)
	}
	if v := judgeUpgrade(a.meta.Ver, a.meta.Rev, latest, h.dist.Rev()); !v.Available || v.Kind != upgradeKindOlder {
		return no(termproto.AutoGrantUpToDate)
	}
	if m.Version != latest {
		return no(termproto.AutoGrantStale) // Agent 检查之后 Hub 上的版本变了：让它重新检查，不替它换目标
	}
	sig, ok := h.dist.Signature(name)
	if !ok {
		return no(termproto.AutoGrantUnsigned)
	}

	sessions := h.webUpgradeSessions(a)
	if !sessions.Persistent && sessions.needsConfirm() {
		// 会话与 Agent 同生共死：升级会结束它们，而静默升级没有管理员在场确认。
		return no(termproto.AutoGrantBusy)
	}
	job := &upgradeJob{
		id: m.ID, code: a.code, from: a.meta.Ver, to: latest, state: UpgradeJobRequested,
		origin: UpgradeOriginAuto, started: now, updated: now,
	}
	if h.webUpgrades.claim(job, now) != nil {
		return no(termproto.AutoGrantBusy)
	}
	// 升级窗口：已经开着（管理员为手动升级开的）就沿用，没开就为这次升级开一个。
	wasOpen := h.upgrades.view(a.code, now) != nil
	win := h.upgrades.open(a.code, a.meta.Ver, now, DefaultUpgradeWindow)
	return termproto.AutoUpgradeGrant{
			Status: termproto.AutoGrantGranted, Version: latest, Ticket: win.Ticket, SHA256: sum, Sig: sig,
		},
		&autoClaim{job: *job, wasOpen: wasOpen, sessions: sessions}
}
