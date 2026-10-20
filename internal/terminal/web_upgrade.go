package terminal

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/teacat99/RelayMesh/v2/internal/termproto"
)

// Web 一键升级（Hub 侧）。方案与信任模型见 .cursor/planning/2026-10-08-agent-web-upgrade-plan.md。
//
// 拉取式：Hub 不向主机推送任何可执行内容。管理员（已登录且已解锁）点「一键升级」后，Hub 只做三件事：
//  1. 确认这台主机的 Agent 在线，并且声明了「主机所有者已在本机允许从 Web 升级」（Meta.WebUpgrade == "on"）——
//     这个开关只能在主机上改，Hub 无法替主人选择；
//  2. 为它开（或沿用）这台主机的升级窗口，那是 Agent 取新版本所用的、限时的、只放行 /i/ 里公开文件的票据；
//  3. 用 upgrade 帧把「目标版本 + 窗口票据 + 这个平台的 SHA-256」告诉它。
//
// 之后 Agent 自己下载、校验、验证新程序能运行，再交给一个独立于 Agent 进程树的升级进程完成替换与重启；
// 新版本连不上服务端时，升级进程会自动还原旧版本。会话由独立的会话守护进程持有，升级过程不碰它。
//
// 这里只记录「请求的进展」，不参与升级本身：进展来自 Agent 的 upgrade_status 帧，最终结果以
// 「主机带着不同的版本重新上线」为准（upgradeSeen）。状态只在内存里，Hub 重启即丢失——升级是否完成
// 仍然能从主机重新上线时的版本看出来，不会因此出错。

// 升级请求的状态。
const (
	UpgradeJobRequested   = "requested"   // 已发出请求，等待 Agent 应答
	UpgradeJobDownloading = "downloading" // Agent 已接受，正在下载并校验
	UpgradeJobHandoff     = "handoff"     // 已交给升级进程；Agent 即将被重启，随后以新版本重新上线
	UpgradeJobDone        = "done"        // 主机带着新版本重新上线了
	UpgradeJobFailed      = "failed"      // 被拒绝、失败，或新版本没有连上服务端而被还原
)

const (
	// webUpgradeStall：请求这么久没有任何进展，界面把它标成「停滞」并提示去主机上看日志。
	webUpgradeStall = 5 * time.Minute
	// webUpgradeExpire：请求这么久没有进展就被当作已放弃，可以重新发起（与 Agent 交接后的占用时长一致）。
	webUpgradeExpire = 10 * time.Minute
	// 请求在内存里的保留时长（自最后一次进展起算），界面据此显示「刚刚发生了什么」：
	// 完成的只需短暂保留（版本已经更新，提示一下即可）；失败的原因值得在管理员重新打开页面时仍能看到；
	// 一直没有结果的请求（Hub 看不到任何进展）保留一小时，之后连「停滞」的提示一起清掉。
	webUpgradeKeepDone     = 10 * time.Minute
	webUpgradeKeepFailed   = 24 * time.Hour
	webUpgradeKeepInflight = time.Hour
	// webUpgradeSkew：比较 Agent 记录的时间与 Hub 发起请求的时间时容许的时钟偏差。
	webUpgradeSkew = 30 * time.Second
)

// 升级请求被拒绝的稳定代码（REST 的 code 字段；界面据此给出对应的说明）。
const (
	webUpgradeNotOnline    = "not_online"        // Agent 不在线
	webUpgradeNotSupported = "not_supported"     // Agent 不支持（旧版本 / 前台运行 / 系统不受支持）
	webUpgradeNotAllowed   = "not_allowed"       // 主机所有者没有在本机允许
	webUpgradeNoTarget     = "no_target_version" // 服务端不知道自带的 Agent 是哪个版本
	webUpgradeUpToDate     = "up_to_date"        // 已是服务端自带的版本（或更新）
	webUpgradeNoAsset      = "no_asset"          // 服务端没有适用于这台主机平台的 Agent 程序
	webUpgradeSessionsBusy = "sessions_active"   // 有正在运行的会话 / 模型运行，需要管理员确认
	webUpgradeInProgress   = "in_progress"       // 已经有一次升级在进行
	webUpgradeSendFailed   = "send_failed"       // 请求没能送达 Agent
)

// UpgradeOriginAuto 标记由主机自己发起的静默自动升级（缺省 / 空表示管理员在 Web 上点的一键升级）。
// 两种来源共用这套请求状态机（认领 / 推进 / 上线结算 / 回滚识别），只在审计事件与界面文案上区分。
const UpgradeOriginAuto = "auto"

// upgradeJob 是对某台主机发起的一次 Web 升级请求。
type upgradeJob struct {
	id       string
	code     string
	from, to string
	state    string
	// origin 是请求的来源：空 = 管理员在 Web 上点的；UpgradeOriginAuto = 主机自己发起的静默自动升级。
	origin string
	// failCode / detail：失败（被拒绝、失败、被还原）时的稳定代码与说明。
	failCode, detail string
	rolledBack       bool
	started, updated time.Time
}

func (j *upgradeJob) terminal() bool {
	return j.state == UpgradeJobDone || j.state == UpgradeJobFailed
}

// rank 给未结束的状态排序，进展只允许向前（防止乱序帧把状态倒回去）。
func upgradeJobRank(state string) int {
	switch state {
	case UpgradeJobRequested:
		return 0
	case UpgradeJobDownloading:
		return 1
	case UpgradeJobHandoff:
		return 2
	}
	return 3
}

// UpgradeJobView 是返回给界面的升级请求状态。
type UpgradeJobView struct {
	ID    string `json:"id"`
	State string `json:"state"`
	// Origin 为 "auto" 表示这次升级是主机自己发起的静默自动升级；缺省是管理员在 Web 上点的。
	Origin string `json:"origin,omitempty"`
	// Stalled 表示请求超过一段时间没有任何进展（仅对未结束的请求）。
	Stalled bool   `json:"stalled,omitempty"`
	From    string `json:"from"`
	To      string `json:"to"`
	// Code / Detail 在失败时说明原因；RolledBack 表示新版本没有连上服务端，已经自动还原成旧版本。
	Code       string    `json:"code,omitempty"`
	Detail     string    `json:"detail,omitempty"`
	RolledBack bool      `json:"rolled_back,omitempty"`
	StartedAt  time.Time `json:"started_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

func (j *upgradeJob) view(now time.Time) *UpgradeJobView {
	return &UpgradeJobView{
		ID: j.id, State: j.state, Origin: j.origin, From: j.from, To: j.to,
		Stalled:    !j.terminal() && now.Sub(j.updated) >= webUpgradeStall,
		Code:       j.failCode,
		Detail:     j.detail,
		RolledBack: j.rolledBack,
		StartedAt:  j.started,
		UpdatedAt:  j.updated,
	}
}

// upgradeJobs 是各主机最近一次的升级请求（机器码 → 请求）。每台主机只保留一个。
type upgradeJobs struct {
	mu sync.Mutex
	m  map[string]*upgradeJob
}

// busy 返回这台主机上未结束、且没有过期的请求（副本）；没有则返回 nil。
func (u *upgradeJobs) busy(code string, now time.Time) *upgradeJob {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.busyLocked(code, now)
}

func (u *upgradeJobs) busyLocked(code string, now time.Time) *upgradeJob {
	cur := u.m[code]
	if cur == nil || cur.terminal() || now.Sub(cur.updated) >= webUpgradeExpire {
		return nil
	}
	c := *cur
	return &c
}

// claim 登记一次新请求。这台主机上已有未结束的请求时不登记，返回那一个（调用方回 in_progress）；
// 检查与登记在同一把锁里完成，同时到达的两次点击只会有一个成功。
func (u *upgradeJobs) claim(j *upgradeJob, now time.Time) (existing *upgradeJob) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if cur := u.busyLocked(j.code, now); cur != nil {
		return cur
	}
	if u.m == nil {
		u.m = make(map[string]*upgradeJob)
	}
	u.m[j.code] = j
	return nil
}

// abort 撤销刚登记、但请求没能发出去的请求。
func (u *upgradeJobs) abort(code, id string) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if cur := u.m[code]; cur != nil && cur.id == id {
		delete(u.m, code)
	}
}

// advance 按 Agent 的进展报告推进请求；请求不存在、编号对不上或已结束时什么也不做。返回推进后的副本。
func (u *upgradeJobs) advance(code, id string, now time.Time, apply func(*upgradeJob)) (upgradeJob, bool) {
	u.mu.Lock()
	defer u.mu.Unlock()
	j := u.m[code]
	if j == nil || j.id != id || j.terminal() {
		return upgradeJob{}, false
	}
	before := j.state
	apply(j)
	if j.state == before && !j.terminal() {
		return *j, false
	}
	j.updated = now
	return *j, true
}

// settleOnline 在这台主机的 Agent（重新）上线时判定未结束的请求：
//   - 报告的版本与发起请求时不同 → 升级完成（done）；
//   - 版本没变，但 Agent 带回了「发起请求之后」的失败 / 回滚记录 → 失败（failed），并说明是否已还原；
//   - 其余情况（例如网络闪断后以同一版本重连）不改变状态。
func (u *upgradeJobs) settleOnline(code string, meta termproto.Meta, now time.Time) (upgradeJob, bool) {
	u.mu.Lock()
	defer u.mu.Unlock()
	j := u.m[code]
	if j == nil || j.terminal() {
		return upgradeJob{}, false
	}
	note := meta.LastUpgrade
	switch {
	case meta.Ver != "" && meta.Ver != j.from:
		j.state, j.to = UpgradeJobDone, meta.Ver
	case note != nil && note.Outcome != termproto.UpgradeOutcomeOK && note.At >= j.started.Add(-webUpgradeSkew).Unix():
		j.state = UpgradeJobFailed
		j.rolledBack = note.Outcome == termproto.UpgradeOutcomeRolledBack
		// 失败码沿用 upgrade_status 的写法：已还原单独成码，升级进程没能完成替换 / 重启记为 apply。
		j.failCode = termproto.UpgradeCodeApply
		if j.rolledBack {
			j.failCode = termproto.UpgradeOutcomeRolledBack
		}
		j.detail = note.Detail
	default:
		return upgradeJob{}, false
	}
	j.updated = now
	return *j, true
}

func (u *upgradeJobs) view(code string, now time.Time) *UpgradeJobView {
	u.mu.Lock()
	defer u.mu.Unlock()
	if j := u.m[code]; j != nil {
		return j.view(now)
	}
	return nil
}

func (u *upgradeJobs) drop(code string) {
	u.mu.Lock()
	defer u.mu.Unlock()
	delete(u.m, code)
}

// sweep 清掉保留期已过的请求（保留时长按状态区分，见 webUpgradeKeep*），防止内存里无限累积。
func (u *upgradeJobs) sweep(now time.Time) {
	u.mu.Lock()
	defer u.mu.Unlock()
	for code, j := range u.m {
		keep := webUpgradeKeepInflight
		switch j.state {
		case UpgradeJobDone:
			keep = webUpgradeKeepDone
		case UpgradeJobFailed:
			keep = webUpgradeKeepFailed
		}
		if now.Sub(j.updated) >= keep {
			delete(u.m, code)
		}
	}
}

// WebUpgradeSessions 汇总一台主机上「升级会影响到的东西」，供界面在确认前提示，也是服务端要求确认的依据。
type WebUpgradeSessions struct {
	Running      int `json:"running"`       // 正在运行的会话数
	Attached     int `json:"attached"`      // 其中此刻有人正在查看的
	Alt          int `json:"alt"`           // 其中正在运行全屏程序（如 vim）的
	ModelRunning int `json:"model_running"` // 进行中的模型运行数
	// Persistent 表示会话由独立的守护进程持有：升级只重启 Agent，会话不受影响；
	// 为 false 时会话与 Agent 同生共死，升级会结束它们。
	Persistent bool `json:"persistent"`
}

// needsConfirm 判断是否存在需要管理员确认才可继续的东西。
// 会话由守护进程持有时升级不会打断它们，但管理员仍应知道这一点，所以同样要确认——宁可多问一次。
func (s WebUpgradeSessions) needsConfirm() bool { return s.Running > 0 || s.ModelRunning > 0 }

func (h *Hub) webUpgradeSessions(a *agentConn) WebUpgradeSessions {
	var s WebUpgradeSessions
	for _, si := range a.sessionList() {
		if si.State != termproto.SessionRunning {
			continue
		}
		s.Running++
		if si.Attached > 0 {
			s.Attached++
		}
		if si.Alt {
			s.Alt++
		}
	}
	s.ModelRunning = h.exec.ActiveOnHost(a.code)
	if hv := a.holderView(); hv != nil {
		s.Persistent = hv.Persistent
	}
	return s
}

// webUpgradeError 是升级请求被拒绝的原因。
type webUpgradeError struct {
	status   int
	code     string
	msg      string
	sessions *WebUpgradeSessions // 仅 sessions_active
	job      *UpgradeJobView     // 仅 in_progress：正在进行的那一次
}

func (e *webUpgradeError) Error() string { return e.msg }

func newWebUpgradeError(status int, code, msg string) *webUpgradeError {
	return &webUpgradeError{status: status, code: code, msg: msg}
}

// distAsset 在分发目录里找出适用于 plat（如 linux-amd64）的 Agent 程序，返回它的文件名与 SHA-256。
func (h *Hub) distAsset(plat string) (name, sum string, ok bool) {
	for _, as := range h.dist.List() {
		if as.Platform() == plat {
			name = as.Name
			break
		}
	}
	if name == "" {
		return "", "", false
	}
	sums, err := h.dist.Checksums()
	if err != nil {
		return "", "", false
	}
	for _, line := range strings.Split(string(sums), "\n") {
		sum, file, found := strings.Cut(line, "  ")
		if found && file == name && len(sum) == 64 {
			return name, sum, true
		}
	}
	return "", "", false
}

// distSHA256 返回适用于 plat 的 Agent 程序的 SHA-256。
func (h *Hub) distSHA256(plat string) (string, bool) {
	_, sum, ok := h.distAsset(plat)
	return sum, ok
}

// requestWebUpgrade 受理一次「一键升级」：全部前提成立才会向 Agent 发出请求，任何一条不成立都不会改动主机上的任何东西。
func (h *Hub) requestWebUpgrade(ctx context.Context, code, actor, ip string, confirmSessions bool) (*UpgradeJobView, *webUpgradeError) {
	if _, werr := h.upgradeTarget(ctx, code); werr != nil {
		return nil, newWebUpgradeError(werr.status, werr.code, werr.msg)
	}
	a := h.onlineAgent(code)
	if a == nil || !a.isEnabled() || a.temp {
		return nil, newWebUpgradeError(http.StatusConflict, webUpgradeNotOnline, "这台主机的 Agent 当前不在线：一键升级需要 Agent 在线")
	}
	switch a.meta.WebUpgrade {
	case termproto.WebUpgradeOn:
	case termproto.WebUpgradeOff:
		return nil, newWebUpgradeError(http.StatusConflict, webUpgradeNotAllowed,
			"主机所有者没有允许从 Web 升级。需要时请在这台主机上运行 relaymesh-agent web-upgrade on，或改用升级窗口里的命令")
	default:
		return nil, newWebUpgradeError(http.StatusConflict, webUpgradeNotSupported,
			"这台主机上的 Agent 不支持从 Web 升级（版本较旧、以前台方式运行，或系统暂不受支持），请改用升级窗口里的命令")
	}

	now := h.now()
	if cur := h.webUpgrades.busy(code, now); cur != nil {
		e := newWebUpgradeError(http.StatusConflict, webUpgradeInProgress, "这台主机上已经有一次升级在进行")
		e.job = cur.view(now)
		return nil, e
	}

	from, latest := a.meta.Ver, h.dist.Version()
	if latest == "" {
		return nil, newWebUpgradeError(http.StatusConflict, webUpgradeNoTarget, "服务端没有记录自带 Agent 的版本（缺少 VERSION 文件），无法判断该升到哪个版本")
	}
	if verdict := judgeUpgrade(from, a.meta.Rev, latest, h.dist.Rev()); !verdict.Available {
		msg := "这台主机的 Agent 已经是服务端自带的版本（或更新）"
		if verdict.SameCode {
			msg = "这台主机的 Agent 与服务端自带的 Agent 代码完全一致（版本号不同只是构建号不同），不需要升级"
		}
		return nil, newWebUpgradeError(http.StatusConflict, webUpgradeUpToDate, msg)
	}
	sum, ok := h.distSHA256(a.meta.Plat)
	if !ok {
		return nil, newWebUpgradeError(http.StatusConflict, webUpgradeNoAsset,
			fmt.Sprintf("服务端自带的 Agent 里没有适用于这台主机（%s）的程序", platformLabel(a.meta.Plat)))
	}

	sessions := h.webUpgradeSessions(a)
	if sessions.needsConfirm() && !confirmSessions {
		e := newWebUpgradeError(http.StatusConflict, webUpgradeSessionsBusy, "这台主机上还有正在运行的会话或模型运行，需要确认后才会升级")
		e.sessions = &sessions
		return nil, e
	}

	job := &upgradeJob{id: randHex(16), code: code, from: from, to: latest, state: UpgradeJobRequested, started: now, updated: now}
	if cur := h.webUpgrades.claim(job, now); cur != nil {
		e := newWebUpgradeError(http.StatusConflict, webUpgradeInProgress, "这台主机上已经有一次升级在进行")
		e.job = cur.view(now)
		return nil, e
	}

	// 升级窗口：已经开着（管理员为手动升级开的）就沿用，没开就为这次升级开一个。
	wasOpen := h.upgrades.view(code, now) != nil
	win := h.upgrades.open(code, from, now, DefaultUpgradeWindow)
	frame := termproto.Upgrade{T: termproto.TUpgrade, ID: job.id, Version: latest, Ticket: win.Ticket, SHA256: sum}
	if err := a.writeJSON(frame); err != nil {
		h.webUpgrades.abort(code, job.id)
		if !wasOpen {
			h.upgrades.close(code, now)
		}
		return nil, newWebUpgradeError(http.StatusBadGateway, webUpgradeSendFailed, "升级请求没能送达这台主机的 Agent，请稍后重试")
	}
	h.audit.record(ctx, auditEvent{
		Event: EvWebUpgrade, Actor: actor, IP: ip, MachineCode: code,
		Detail: fmt.Sprintf("request id=%s from=%s to=%s running=%d model=%d confirmed=%t", job.id, from, latest, sessions.Running, sessions.ModelRunning, confirmSessions),
	})
	return job.view(now), nil
}

func platformLabel(plat string) string {
	if plat == "" {
		return "平台未报告"
	}
	return plat
}

// onUpgradeStatus 处理 Agent 的升级进展报告。编号对不上的（Hub 重启过、或别的请求）一律忽略。
func (h *Hub) onUpgradeStatus(a *agentConn, m termproto.UpgradeStatus) {
	now := h.now()
	j, changed := h.webUpgrades.advance(a.code, m.ID, now, func(j *upgradeJob) {
		switch m.Stage {
		case termproto.UpgradeStageDownloading, termproto.UpgradeStageHandoff:
			next := UpgradeJobDownloading
			if m.Stage == termproto.UpgradeStageHandoff {
				next = UpgradeJobHandoff
			}
			if upgradeJobRank(next) > upgradeJobRank(j.state) {
				j.state = next
			}
		default: // refused / failed
			j.state, j.failCode, j.detail = UpgradeJobFailed, m.Code, m.Detail
		}
	})
	if !changed {
		return
	}
	switch j.state {
	case UpgradeJobHandoff:
		// 静默自动升级在批准时已经记了 start；交接这一步只给管理员发起的升级单独记一条。
		if j.origin != UpgradeOriginAuto {
			h.audit.record(h.ctx, auditEvent{Event: EvWebUpgrade, Actor: ActorSystem, MachineCode: a.code, Detail: "handoff id=" + j.id + " to=" + j.to})
		}
	case UpgradeJobFailed:
		h.finishUpgradeJob(j)
	}
}

// finishUpgradeJob 在请求结束（成功或失败）时记审计；失败时顺手结束升级窗口——它的使命已经结束，没必要继续开着。
func (h *Hub) finishUpgradeJob(j upgradeJob) {
	auto := j.origin == UpgradeOriginAuto
	if j.state == UpgradeJobFailed {
		ev, detail := EvWebUpgrade, fmt.Sprintf("failed id=%s code=%s rolled_back=%t %s", j.id, j.failCode, j.rolledBack, j.detail)
		if auto {
			// 自动升级的审计只记「是哪一次、升到哪、为什么」；已还原的单独成一类，便于一眼看出新版本有问题。
			ev = EvAutoUpgradeFail
			if j.rolledBack {
				ev = EvAutoUpgradeRollback
			}
			detail = fmt.Sprintf("id=%s from=%s to=%s code=%s %s", j.id, j.from, j.to, j.failCode, j.detail)
		}
		h.audit.record(h.ctx, auditEvent{Event: ev, Actor: ActorSystem, MachineCode: j.code, Result: ResultFail, Detail: detail})
		h.closeUpgradeWindow(h.ctx, j.code, ActorSystem, "", "upgrade failed")
		return
	}
	ev, detail := EvWebUpgrade, fmt.Sprintf("done id=%s from=%s to=%s", j.id, j.from, j.to)
	if auto {
		ev, detail = EvAutoUpgradeOK, fmt.Sprintf("id=%s from=%s to=%s", j.id, j.from, j.to)
	}
	h.audit.record(h.ctx, auditEvent{Event: ev, Actor: ActorSystem, MachineCode: j.code, Result: ResultOK, Detail: detail})
}

// ---------- REST ----------

type webUpgradeRequest struct {
	// ConfirmSessions 表示管理员已经看过「还有正在运行的会话」的提示并确认继续。
	ConfirmSessions bool `json:"confirm_sessions"`
}

// POST /hosts/:code/web-upgrade —— 请这台主机的 Agent 自己升级到服务端自带的版本。
func (h *Hub) handleWebUpgrade(c *gin.Context) {
	code, ok := machineCodeParam(c)
	if !ok {
		return
	}
	var req webUpgradeRequest
	if c.Request.ContentLength != 0 && !bindJSON(c, &req) {
		return
	}
	job, werr := h.requestWebUpgrade(c.Request.Context(), code, subjectOf(c), c.ClientIP(), req.ConfirmSessions)
	if werr != nil {
		body := gin.H{"error": werr.msg, "code": werr.code}
		if werr.sessions != nil {
			body["sessions"] = werr.sessions
		}
		if werr.job != nil {
			body["upgrade_job"] = werr.job
		}
		c.JSON(werr.status, body)
		return
	}
	c.JSON(http.StatusOK, gin.H{"upgrade_job": job})
}
