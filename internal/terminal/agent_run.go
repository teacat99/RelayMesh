package terminal

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/teacat99/RelayMesh/v2/internal/model"
	"github.com/teacat99/RelayMesh/v2/internal/store"
)

// 项目管理 Agent 经 MCP 运行已有预设（#179，设计稿 design/headless-new-conversation.md §8）。
//
// 这一层只回答三个问题：谁有资格运行哪些预设、此刻能不能再运行（频率账本）、运行之后留下什么痕迹。
// 「谁是项目管理 Agent」由 MCP 一侧判定（持有 tasks 范围授权，见 acl.CanManageTasks）后经 AgentRunActor 传进来；
// 凭据的「运行预设」权限位也在 MCP 一侧把关。边界：
//   - 只能「运行」：不能建 / 改 / 删预设，不能传提示词、命令、模型或权限姿态——这些全在预设里，由人定死，也从不经过这条通道返回；
//   - 资格按任务板限定：预设的目标工作流必须在调用方所在项目的任务板上，且不是调用方自己的工作流（避免自己唤醒自己）；
//   - 提交走 guardService.runLauncherWith，与网页上点「运行」是同一条路径：总闸、暂停、主机「模型访问」、主机执行模式（「需批准」就停在待批准）、
//     令牌限额、预设单飞、「上一次运行没结束」、「对话还在运行」一个不少，没有旁路；这里只在它前面多一层频率账本；
//   - 频率账本读审计记录（EvLauncherAgentRun，只数成功提交的），所以 Hub 重启不会清零；检查与提交在同一把锁里；
//   - 审计、聊天系统行与运行记录的说明都写明「是谁发起的」，但都不含提示词与命令。

// AgentRunLimits 是频率护栏。它们是代码常量（设计稿 8.4），写进工具说明与拒绝信息；以后需要再做成设置。
type AgentRunLimits struct {
	// MaxConcurrent：每个管理 Agent 同时最多几条自己发起的运行在进行。
	MaxConcurrent int
	// MaxPerHour：每个管理 Agent 每小时最多发起几次。
	MaxPerHour int
	// MaxPerDay：所有 Agent 发起的运行 24 小时合计最多几次（熔断：两个管理 Agent 互相唤醒形成循环时由它拦住）。
	MaxPerDay int
	// PresetGap：同一预设两次由 Agent 发起的运行之间至少隔多久。
	PresetGap time.Duration
}

// AgentRunLimitsView 是回执里写给智能体看的护栏数字（与实际生效的一致），用来让它自己安排节奏。
type AgentRunLimitsView struct {
	MaxConcurrent    int `json:"max_concurrent"`
	MaxPerHour       int `json:"max_per_hour"`
	MaxPerDay        int `json:"max_per_day"`
	PresetGapSeconds int `json:"preset_gap_seconds"`
}

// AgentRunLimitsView 返回此刻生效的频率护栏。
func (h *Hub) AgentRunLimitsView() AgentRunLimitsView {
	lim := DefaultAgentRunLimits
	if h != nil && h.guardian != nil {
		lim = h.guardian.agentRunLimits()
	}
	return AgentRunLimitsView{MaxConcurrent: lim.MaxConcurrent, MaxPerHour: lim.MaxPerHour, MaxPerDay: lim.MaxPerDay, PresetGapSeconds: int(lim.PresetGap / time.Second)}
}

// DefaultAgentRunLimits 是设计稿 8.4 的默认值。
var DefaultAgentRunLimits = AgentRunLimits{MaxConcurrent: 3, MaxPerHour: 10, MaxPerDay: 30, PresetGap: 5 * time.Minute}

const (
	// agentRunLineActor 是「项目管理 Agent 运行了预设」系统行在聊天里的发起者标记。
	agentRunLineActor = "agent-run"
	// agentRunLookback 是账本回看的时间：平台单次运行最长 7 天，一条还在跑的老运行也要算进「同时在跑」。
	agentRunLookback = 7 * 24 * time.Hour
	// agentRunLabelRunes 是写进运行说明 / 聊天行里的 Agent 名字的长度上限。
	agentRunLabelRunes = 24
)

func (g *guardService) agentRunLimits() AgentRunLimits {
	if g.agentLimits != (AgentRunLimits{}) {
		return g.agentLimits
	}
	return DefaultAgentRunLimits
}

// AgentRunActor 是经 MCP 发起预设运行的调用方；MCP 一侧已经确认它是所在项目的管理 Agent。
type AgentRunActor struct {
	// IdentityID 是调用方的身份编号：频率账本按它分；审计里记它，不记目录路径。
	IdentityID uint
	// Label 是写进审计说明、运行说明与聊天系统行里的名字（身份标签）。
	Label string
	// WorkflowID 是调用方自己的工作流：它不能运行自己所在工作流的预设。
	WorkflowID string
	// ProjectID 是调用方作为管理 Agent 所在的项目：资格按这个项目的任务板限定。
	ProjectID uint
}

func (a AgentRunActor) ref() string { return "agent:" + strconv.FormatUint(uint64(a.IdentityID), 10) }

// agentRunInfo 是 runLauncherWith 里「这次运行由项目管理 Agent 发起」的标记。
type agentRunInfo struct {
	Label      string
	IdentityID uint
}

// AgentRunError 是项目管理 Agent 运行预设被拒绝的原因：Code 稳定（给程序看），Message 是一句中文并带上下一步（给智能体看）。
type AgentRunError struct {
	Code    string
	Message string
}

func (e *AgentRunError) Error() string { return e.Message }

// ErrorCode 让 MCP 一侧不必引用这个包的类型就能取到稳定的拒绝码（见 mcp.PresetRunner）。
func (e *AgentRunError) ErrorCode() string { return e.Code }

func agentErr(code, msg string) *AgentRunError { return &AgentRunError{Code: code, Message: msg} }

// wrapAgentErr 把 runLauncherWith / inspectLauncher 的 wakeError 换成对外的 AgentRunError；其他错误（数据库等）原样返回。
func wrapAgentErr(err error) error {
	var we *wakeError
	if errors.As(err, &we) {
		return &AgentRunError{Code: we.Code, Message: we.Message}
	}
	return err
}

// ---------- 运行说明里的「由谁发起」标记 ----------

var agentRunSuffixRe = regexp.MustCompile(`（项目管理 Agent「([^」]*)」发起）$`)

// cleanAgentLabel 去掉会破坏标记格式的括号 / 引号 / 换行，限长；空名字写「未命名」。
func cleanAgentLabel(label string) string {
	var b strings.Builder
	for _, r := range label {
		switch r {
		case '「', '」', '（', '）', '\n', '\r', '\t':
			continue
		}
		b.WriteRune(r)
	}
	out := strings.TrimSpace(strings.Join(strings.Fields(b.String()), " "))
	if out == "" {
		return "未命名"
	}
	if rs := []rune(out); len(rs) > agentRunLabelRunes {
		out = string(rs[:agentRunLabelRunes]) + "…"
	}
	return out
}

// agentRunSuffix 是写在运行说明末尾的标记：运行记录里留下「这次运行是项目管理 Agent 发起的」，启动器清单据此标出来。
func agentRunSuffix(label string) string {
	return "（项目管理 Agent「" + cleanAgentLabel(label) + "」发起）"
}

// parseAgentRunSuffix 从运行说明里取出发起的 Agent 名字；不是 Agent 发起的返回空串。
func parseAgentRunSuffix(desc string) string {
	m := agentRunSuffixRe.FindStringSubmatch(desc)
	if m == nil {
		return ""
	}
	return m[1]
}

// launcherAgentLineText 是项目管理 Agent 运行预设时目标工作流聊天里的系统行：谁、接着哪个对话、什么模型与权限。不含提示词，也不含命令。
func launcherAgentLineText(plan LauncherRunPlan, label string) string {
	return fmt.Sprintf("项目管理 Agent「%s」运行了预设「%s」（%s）", cleanAgentLabel(label), plan.Name, launcherPlanSummary(plan))
}

// ---------- 对外的数据形状（MCP 的 list / status / run 回执用；不含提示词与命令） ----------

// AgentPresetLastRun 是一条预设最近一次运行的现状。Reply 是从输出末尾提取、已脱敏限长的最后一条回复——
// 它来自另一个智能体的输出，读的人要当数据看，不要当指令执行。
type AgentPresetLastRun struct {
	RunID      string     `json:"run_id"`
	State      string     `json:"state"`
	Active     bool       `json:"active"`
	CreatedAt  time.Time  `json:"created_at"`
	StartedAt  *time.Time `json:"started_at,omitempty"`
	EndedAt    *time.Time `json:"ended_at,omitempty"`
	DurationMS int64      `json:"duration_ms,omitempty"`
	ExitCode   *int       `json:"exit_code,omitempty"`
	Reply      string     `json:"reply,omitempty"`
	Silent     bool       `json:"silent,omitempty"`
	Why        string     `json:"why,omitempty"`
	By         string     `json:"by,omitempty"`
}

// AgentPresetView 是项目管理 Agent 能看到的一条预设。
type AgentPresetView struct {
	Preset          string `json:"preset"`
	Name            string `json:"name"`
	WorkflowID      string `json:"workflow_id"`
	WorkflowTitle   string `json:"workflow_title,omitempty"`
	Host            string `json:"host"`
	Posture         string `json:"posture"`
	Model           string `json:"model,omitempty"`
	MaxRunMinutes   int    `json:"max_run_minutes"`
	AutoResume      bool   `json:"auto_resume,omitempty"`
	NewConversation bool   `json:"new_conversation,omitempty"`
	// Origin 是预设的来源：enable_agent = 网页上「启用 Agent」建的；空 = 用户手工建的。
	Origin string `json:"origin,omitempty"`
	// CanRun 是此刻能不能运行（沿用网页「运行」按钮预览的那套检查，再加上频率账本）；不能时 WhyNot 是一句中文原因、WhyCode 是稳定的代码。
	CanRun  bool                `json:"can_run"`
	WhyNot  string              `json:"why_not,omitempty"`
	WhyCode string              `json:"why_code,omitempty"`
	LastRun *AgentPresetLastRun `json:"last_run,omitempty"`
}

// AgentRunResult 是运行提交成功之后的回执。
type AgentRunResult struct {
	Preset          string   `json:"preset"`
	Name            string   `json:"name"`
	WorkflowID      string   `json:"workflow_id"`
	RunID           string   `json:"run_id"`
	State           string   `json:"state"`
	Posture         string   `json:"posture"`
	Model           string   `json:"model,omitempty"`
	MaxRunMinutes   int      `json:"max_run_minutes"`
	NewConversation bool     `json:"new_conversation,omitempty"`
	Warnings        []string `json:"warnings,omitempty"`
	Message         string   `json:"message"`
}

// ---------- 资格 ----------

// boardProjectOf 返回工作流现在使用的任务板所在的项目；独立板、查不到都返回 0。
func (g *guardService) boardProjectOf(ctx context.Context, workflowID string) uint {
	key, err := g.h.store.BoardKeyOf(ctx, workflowID)
	if err != nil {
		return 0
	}
	pid, _, ok := store.ParseBoardKey(key)
	if !ok {
		return 0
	}
	return pid
}

// agentEligible 把预设分成两类：actor 有资格运行的（目标工作流在 actor 所在项目的任务板上、且不是它自己的工作流），
// 以及目标就是 actor 自己的工作流的（不能运行，单列出来是为了给出更明确的拒绝原因）。别的预设一概不返回——
// 包括别的项目的、没有目标工作流的、不是非交互续聊的：对 actor 来说它们不存在。结果按名字排序。
func (g *guardService) agentEligible(ctx context.Context, actor AgentRunActor) (eligible, own []store.TerminalLauncher, err error) {
	if actor.ProjectID == 0 {
		return nil, nil, nil
	}
	launchers, err := g.h.store.ListTerminalLaunchers(ctx)
	if err != nil {
		return nil, nil, err
	}
	self := strings.TrimSpace(actor.WorkflowID)
	project := map[string]uint{} // 工作流 → 它的板所在的项目（0 = 不在任何项目的板上）
	for _, p := range launchers {
		if !p.IsHeadless() || p.Run == nil {
			continue
		}
		wf := strings.TrimSpace(p.Run.WorkflowID)
		if wf == "" {
			continue
		}
		pid, seen := project[wf]
		if !seen {
			pid = g.boardProjectOf(ctx, wf)
			project[wf] = pid
		}
		if pid == 0 || pid != actor.ProjectID {
			continue
		}
		if wf == self {
			own = append(own, p)
			continue
		}
		eligible = append(eligible, p)
	}
	byName := func(l []store.TerminalLauncher) {
		sort.Slice(l, func(i, j int) bool { return guardNameKey(l[i].Name) < guardNameKey(l[j].Name) })
	}
	byName(eligible)
	byName(own)
	return eligible, own, nil
}

// matchPreset 按编号或名字（不分大小写、空白折叠）在 list 里找预设。
func matchPreset(list []store.TerminalLauncher, ref string) *store.TerminalLauncher {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return nil
	}
	for i := range list {
		if list[i].ID == ref {
			return &list[i]
		}
	}
	key := guardNameKey(ref)
	for i := range list {
		if guardNameKey(list[i].Name) == key {
			return &list[i]
		}
	}
	return nil
}

// agentResolve 把调用方给的预设引用解析成有资格运行的预设；找不到、或者是别的项目 / 自己的，都返回带原因的 AgentRunError。
// 「找不到」与「不在你的项目里」用同一句话：不向调用方透露它看不见的预设是否存在。
func (g *guardService) agentResolve(ctx context.Context, actor AgentRunActor, ref string) (*store.TerminalLauncher, error) {
	eligible, own, err := g.agentEligible(ctx, actor)
	if err != nil {
		return nil, err
	}
	if p := matchPreset(eligible, ref); p != nil {
		return p, nil
	}
	if p := matchPreset(own, ref); p != nil {
		return nil, agentErr("own_workflow", "不能运行自己所在工作流的预设（避免自己唤醒自己）；要唤醒别的子会话，请运行目标工作流是它的预设")
	}
	return nil, agentErr("preset_not_found", "没有找到这个预设，或者它的目标工作流不在你所在项目的任务板上：用 list 看你能运行哪些")
}

// ---------- 频率账本 ----------

// agentLedger 是某一刻的账本快照：由审计记录算出（只数成功提交的）。
type agentLedger struct {
	now          time.Time
	lim          AgentRunLimits
	dayAll       int                  // 24 小时内所有 Agent 发起的运行
	hourMine     int                  // 1 小时内调用方自己发起的运行
	activeMine   int                  // 调用方自己发起、此刻还在进行的运行
	lastByPreset map[string]time.Time // 预设 → 最近一次 Agent 发起的运行（任何 Agent）
}

// presetOfAuditDetail 取审计说明里的预设编号（"preset=<id> ..."；预设编号只含字母数字与 _ -，不会含空格）。
func presetOfAuditDetail(detail string) string {
	for _, f := range strings.Fields(detail) {
		if v, ok := strings.CutPrefix(f, "preset="); ok {
			return v
		}
	}
	return ""
}

func (g *guardService) loadAgentLedger(ctx context.Context, actor AgentRunActor, now time.Time) (*agentLedger, error) {
	rows, err := g.h.store.ListTerminalAuditSince(ctx, EvLauncherAgentRun, ResultOK, now.Add(-agentRunLookback), 500)
	if err != nil {
		return nil, err
	}
	l := &agentLedger{now: now, lim: g.agentRunLimits(), lastByPreset: map[string]time.Time{}}
	me := actor.ref()
	for _, r := range rows {
		if r.Result != ResultOK {
			continue
		}
		if now.Sub(r.CreatedAt) <= 24*time.Hour {
			l.dayAll++
		}
		if id := presetOfAuditDetail(r.Detail); id != "" && r.CreatedAt.After(l.lastByPreset[id]) {
			l.lastByPreset[id] = r.CreatedAt
		}
		if r.Actor != me {
			continue
		}
		if now.Sub(r.CreatedAt) <= time.Hour {
			l.hourMine++
		}
		if r.RunID == "" {
			continue
		}
		if rec, rerr := g.h.store.GetTerminalRun(ctx, r.RunID); rerr == nil && rec != nil && runActive(*rec, now) {
			l.activeMine++
		}
	}
	return l, nil
}

// check 返回账本上第一个挡住这次运行的原因；放行返回 nil。
// 顺序：全局每天上限（与调用方无关、最要紧）→ 同一预设的间隔（最常见的「刚点过又点」）→ 同时在跑的数量 → 每小时次数。
func (l *agentLedger) check(presetID string) *AgentRunError {
	if l.lim.MaxPerDay > 0 && l.dayAll >= l.lim.MaxPerDay {
		return agentErr("agent_run_daily_cap", fmt.Sprintf(
			"所有项目管理 Agent 在过去 24 小时内合计已经运行了 %d 次预设，到了每天 %d 次的上限（防止智能体之间互相唤醒形成循环）：请告诉用户，由用户在网页上手动运行，或等明天再试",
			l.dayAll, l.lim.MaxPerDay))
	}
	if gap := l.lim.PresetGap; gap > 0 {
		if last, ok := l.lastByPreset[presetID]; ok {
			if left := gap - l.now.Sub(last); left > 0 {
				return agentErr("agent_run_preset_gap", fmt.Sprintf(
					"这个预设刚被 Agent 运行过（%s 前），同一个预设两次由 Agent 发起的运行至少隔 %s：请 %s 之后再试",
					roughDuration(l.now.Sub(last)), roughDuration(gap), roughDuration(left)))
			}
		}
	}
	if l.lim.MaxConcurrent > 0 && l.activeMine >= l.lim.MaxConcurrent {
		return agentErr("agent_run_concurrency", fmt.Sprintf(
			"你自己发起的运行已经有 %d 条还在进行，同时最多 %d 条：等其中一条结束再运行（用 status 看它们的进展）",
			l.activeMine, l.lim.MaxConcurrent))
	}
	if l.lim.MaxPerHour > 0 && l.hourMine >= l.lim.MaxPerHour {
		return agentErr("agent_run_hourly", fmt.Sprintf(
			"过去一小时你已经发起了 %d 次运行，每小时最多 %d 次：请稍后再试", l.hourMine, l.lim.MaxPerHour))
	}
	return nil
}

// roughDuration 把时长写成口语：不足 1 分钟写秒（向上取整），整分钟写分钟，其余写「N 分 M 秒」；只用在拒绝信息里。
func roughDuration(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	sec := int((d + time.Second - 1) / time.Second)
	switch {
	case sec < 60:
		return fmt.Sprintf("%d 秒", sec)
	case sec%60 == 0:
		return fmt.Sprintf("%d 分钟", sec/60)
	}
	return fmt.Sprintf("%d 分 %d 秒", sec/60, sec%60)
}

// ---------- 视图 ----------

// agentViewOf 整理一条预设给项目管理 Agent 看：沿用网页「运行」按钮预览的检查（inspectLauncher）+ 频率账本，再带上最近一次运行。
func (g *guardService) agentViewOf(ctx context.Context, p store.TerminalLauncher, led *agentLedger, now time.Time) AgentPresetView {
	run := *p.Run
	v := AgentPresetView{
		Preset: p.ID, Name: p.Name, WorkflowID: strings.TrimSpace(run.WorkflowID),
		WorkflowTitle: g.h.store.WorkflowTitleOf(ctx, run.WorkflowID),
		Posture:       run.EffectivePosture(), Model: run.Model, MaxRunMinutes: run.EffectiveMaxRunMinutes(),
		AutoResume: run.AutoResume, NewConversation: run.NewConversation, Origin: run.Origin,
	}
	if host, err := g.h.store.GetTerminalHost(ctx, p.Host); err == nil && host != nil {
		v.Host = strings.TrimSpace(host.Remark)
		if v.Host == "" {
			v.Host = host.Hostname
		}
	}
	in := g.inspectLauncher(ctx, p, now)
	switch {
	case len(in.errs) > 0:
		v.WhyNot, v.WhyCode = in.errs[0].Message, in.errs[0].Code
	case led != nil:
		if e := led.check(p.ID); e != nil {
			v.WhyNot, v.WhyCode = e.Message, e.Code
		}
	}
	v.CanRun = v.WhyNot == ""
	if rec := g.latestLauncherRun(ctx, p.ID); rec != nil {
		lv := g.runViewOf(ctx, *rec, now)
		v.LastRun = &AgentPresetLastRun{
			RunID: lv.RunID, State: lv.State, Active: lv.Active, CreatedAt: lv.CreatedAt, StartedAt: lv.StartedAt, EndedAt: lv.EndedAt,
			DurationMS: lv.DurationMS, ExitCode: lv.ExitCode, Reply: lv.Reply, Silent: lv.Silent, Why: lv.Why, By: lv.By,
		}
	}
	return v
}

// ---------- 对外入口（MCP 的 preset_run 用） ----------

var errGuardUnavailable = agentErr("terminal_unavailable", "终端功能在此服务上不可用，无法运行预设")

// AgentPresets 列出 actor 有资格运行的预设（每条带此刻能不能运行与最近一次运行）。没有资格的预设一概不出现。
func (h *Hub) AgentPresets(ctx context.Context, actor AgentRunActor) ([]AgentPresetView, error) {
	if h == nil || h.guardian == nil {
		return nil, errGuardUnavailable
	}
	g, now := h.guardian, h.now()
	eligible, _, err := g.agentEligible(ctx, actor)
	if err != nil {
		return nil, err
	}
	led, err := g.loadAgentLedger(ctx, actor, now)
	if err != nil {
		return nil, err
	}
	out := make([]AgentPresetView, 0, len(eligible))
	for _, p := range eligible {
		out = append(out, g.agentViewOf(ctx, p, led, now))
	}
	return out, nil
}

// AgentPresetStatus 看一条预设此刻的现状（用来等子会话做完）；没有资格同样被拒绝。
func (h *Hub) AgentPresetStatus(ctx context.Context, actor AgentRunActor, ref string) (*AgentPresetView, error) {
	if h == nil || h.guardian == nil {
		return nil, errGuardUnavailable
	}
	g, now := h.guardian, h.now()
	p, err := g.agentResolve(ctx, actor, ref)
	if err != nil {
		return nil, err
	}
	led, err := g.loadAgentLedger(ctx, actor, now)
	if err != nil {
		return nil, err
	}
	v := g.agentViewOf(ctx, *p, led, now)
	return &v, nil
}

// AgentRunPreset 以 actor 的名义运行一条预设。顺序：资格 → 预设此刻的状态（主机在线、上一次运行没结束、对话没在跑……）→ 频率账本 →
// 经 runLauncherWith 提交（它会把全部闸门再走一遍）→ 记审计。每一步被拒绝都记一条审计（拒绝的尝试按调用方限流，防刷屏）。
func (h *Hub) AgentRunPreset(ctx context.Context, actor AgentRunActor, ref string) (*AgentRunResult, error) {
	if h == nil || h.guardian == nil {
		return nil, errGuardUnavailable
	}
	g := h.guardian
	g.agentRunMu.Lock()
	defer g.agentRunMu.Unlock()
	now := h.now()

	deny := func(p *store.TerminalLauncher, code, detailExtra string) {
		detail := "code=" + code
		machine := ""
		if p != nil {
			detail = "preset=" + p.ID + " " + detail
			machine = p.Host
		}
		if detailExtra != "" {
			detail += " " + detailExtra
		}
		h.audit.recordLimited(ctx, actor.ref(), auditEvent{
			Event: EvLauncherAgentRun, Result: ResultDeny, Actor: actor.ref(), ActorKind: ActorModel, MachineCode: machine, Detail: detail,
		})
	}

	p, err := g.agentResolve(ctx, actor, ref)
	if err != nil {
		var ae *AgentRunError
		if errors.As(err, &ae) {
			deny(nil, ae.Code, "")
		}
		return nil, err
	}
	if in := g.inspectLauncher(ctx, *p, now); len(in.errs) > 0 {
		deny(p, in.errs[0].Code, "")
		return nil, wrapAgentErr(in.errs[0])
	}
	led, err := g.loadAgentLedger(ctx, actor, now)
	if err != nil {
		return nil, err
	}
	if e := led.check(p.ID); e != nil {
		deny(p, e.Code, "")
		return nil, e
	}

	plan, preset, err := g.runLauncherWith(ctx, p.ID, now, launcherRunOpts{Agent: &agentRunInfo{Label: actor.Label, IdentityID: actor.IdentityID}})
	if err != nil {
		var we *wakeError
		code := "submit_failed"
		if errors.As(err, &we) {
			code = we.Code
		}
		h.audit.recordLimited(ctx, actor.ref(), auditEvent{
			Event: EvLauncherAgentRun, Result: ResultFail, Actor: actor.ref(), ActorKind: ActorModel, MachineCode: p.Host,
			Detail: "preset=" + p.ID + " code=" + code,
		})
		return nil, wrapAgentErr(err)
	}
	detail := fmt.Sprintf("preset=%s workflow=%s posture=%s", preset.ID, strings.TrimSpace(p.Run.WorkflowID), plan.Posture)
	if plan.NewConversation {
		detail += " new=1"
	}
	// 成功提交的记录是账本的唯一来源，不限流：每天最多 MaxPerDay 条，本来就被上限卡住。
	h.audit.record(ctx, auditEvent{
		Event: EvLauncherAgentRun, Result: ResultOK, Actor: actor.ref(), ActorKind: ActorModel, MachineCode: preset.Host, RunID: plan.RunID,
		Detail: detail + " by=" + cleanAgentLabel(actor.Label),
	})

	msg := "已提交运行：用 status 看进展；运行结束后它的最后一条回复会出现在 last_run.reply 里。要把指令交给这个子会话，请用任务（指派）或收件箱，不要指望这个工具传话。"
	if plan.RunState == model.RunAwaitingApproval {
		msg = "已提交运行，但这台主机的执行模式是「需批准」：要用户在 ExecMesh 里批准之后才会开始（state=awaiting_approval）。" + msg
	}
	return &AgentRunResult{
		Preset: preset.ID, Name: preset.Name, WorkflowID: strings.TrimSpace(p.Run.WorkflowID), RunID: plan.RunID, State: plan.RunState,
		Posture: plan.Posture, Model: plan.Model, MaxRunMinutes: plan.MaxRunSeconds / 60, NewConversation: plan.NewConversation,
		Warnings: plan.Warnings, Message: msg,
	}, nil
}
