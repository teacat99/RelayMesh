package terminal

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/teacat99/RelayMesh/v2/internal/store"
)

// 会话守护的界面接口（D-214）：
//   - GET  /guard            登录即可读：每个配置了守护的预设现在是什么状态、唤醒过几次、能怎么唤醒；
//   - POST /guard/:id/wake   要求刚刚解锁：生成一份「唤醒计划」并登记这次唤醒。计划由浏览器执行——
//     它会把一句提示词输入到会话里，等于人在键盘上敲，所以和打开终端一样要先验证密码。
//
// 服务端不会自己唤醒任何会话；返回的计划里没有任何命令的输出，审计只记预设编号、方式与第几次。

// 唤醒方式。
const (
	GuardWakeNone     = "none"     // 现在没法唤醒
	GuardWakeType     = "type"     // 把提示词输入到存活的会话里
	GuardWakeRelaunch = "relaunch" // 会话已经结束：新开一个会话，重新拉起智能体并带上提示词
)

// GuardWakeView 是最近一次唤醒的概况。
type GuardWakeView struct {
	At       time.Time `json:"at"`
	Mode     string    `json:"mode"`
	Reason   string    `json:"reason"`
	Progress string    `json:"progress"` // progress（之后有了动静）| none（观察窗口过完仍没有）| pending（还在观察）
}

// GuardItem 是一个预设的守护概况。
type GuardItem struct {
	PresetID       string       `json:"preset_id"`
	Name           string       `json:"name"`
	Host           string       `json:"host"`
	Enabled        bool         `json:"enabled"`
	WorkflowID     string       `json:"workflow_id,omitempty"`
	SilenceMinutes int          `json:"silence_minutes"`
	Verdict        GuardVerdict `json:"verdict"`
	// WakesInWindow 是滚动 24 小时内已经唤醒的次数；NoProgressStreak 是最近连续几次唤醒没有任何效果。
	WakesInWindow    int `json:"wakes_24h"`
	NoProgressStreak int `json:"no_progress_streak"`
	// PermitMarked 表示已标记「任务全部完成」（唤醒许可为 0）。
	PermitMarked bool           `json:"permit_marked,omitempty"`
	LastWake     *GuardWakeView `json:"last_wake,omitempty"`
	// WakeMode 是现在点「唤醒」会怎么做；none 时 WakeNote 说明原因。
	WakeMode string `json:"wake_mode"`
	WakeNote string `json:"wake_note,omitempty"`
	// ChatKnown 表示已经知道智能体的对话编号（hook 汇报过），重新拉起时能接着原来的对话。
	ChatKnown bool `json:"chat_known"`
	// Headless 是非交互唤醒的现状；预设没有开启它时没有这一项。
	Headless *GuardHeadlessView `json:"headless,omitempty"`
}

// GuardView 是 GET /guard 的返回。
type GuardView struct {
	Items           []GuardItem `json:"items"`
	WakeMax         int         `json:"wake_max"`
	WakeWindowHours int         `json:"wake_window_hours"`
	ProgressMinutes int         `json:"progress_minutes"`
	At              time.Time   `json:"at"`
}

// view 汇总所有配置了守护的预设现在的状态。
func (g *guardService) view(ctx context.Context, now time.Time) (GuardView, error) {
	out := GuardView{
		Items:           []GuardItem{},
		WakeMax:         guardWakeMax,
		WakeWindowHours: int(guardWakeWindow / time.Hour),
		ProgressMinutes: int(guardProgressWindow / time.Minute),
		At:              now,
	}
	launchers, err := g.h.store.ListTerminalLaunchers(ctx)
	if err != nil {
		return out, err
	}
	state, err := g.h.store.GetTerminalGuardState(ctx)
	if err != nil {
		return out, err
	}
	rows := guardRows{}
	for _, p := range launchers {
		if p.Guard == nil {
			continue
		}
		f := g.facts(ctx, p, now, rows)
		ps := state.Presets[p.ID]
		v, _ := g.verdictOf(p, f, ps)
		item := GuardItem{
			PresetID: p.ID, Name: p.Name, Host: p.Host,
			Enabled:        p.Guard.Enabled,
			WorkflowID:     strings.TrimSpace(p.Guard.WorkflowID),
			SilenceMinutes: int(p.Guard.EffectiveSilence() / time.Minute),
			Verdict:        v,
			ChatKnown:      f.Link.ConversationID != "",
		}
		item.WakeMode, item.WakeNote = guardWakeRoute(f, "")
		item.Headless = g.headlessViewOf(ctx, p, f, ps, now)

		if ps != nil {
			item.WakesInWindow = ps.WakesSince(now.Add(-guardWakeWindow))
			if ps.Permit != nil && ps.Permit.Permit == 0 && !ps.Permit.SetAt.IsZero() {
				item.PermitMarked = true
			}
			if w, ok := ps.LastWake(); ok {
				// 最近一次的结论可能还没被周期判断补上，这里按当下的事实现算；观察窗口内还没结论的这一次不计入连续次数，
				// 但前面已经有结论的那几次照常数——它们仍然说明「唤醒在白费力气」。
				prog := guardSettleProgress(w, f, now)
				settled := append([]store.GuardWakeRecord(nil), ps.Wakes...)
				if last := len(settled) - 1; prog == "" {
					settled = settled[:last]
				} else {
					settled[last].Progress = prog
				}
				item.NoProgressStreak = (&store.GuardPresetState{Wakes: settled}).TrailingNoProgress()
				if prog == "" {
					prog = "pending"
				}
				item.LastWake = &GuardWakeView{At: w.At, Mode: w.Mode, Reason: w.Reason, Progress: prog}
			}
		}
		out.Items = append(out.Items, item)
	}
	return out, nil
}

// guardWakeRoute 决定唤醒走哪条路。preferred 为空表示自动选：会话还活着就输入，否则重新拉起。
// 返回 none 时第二个值说明为什么。
func guardWakeRoute(f guardFacts, preferred string) (mode, note string) {
	live := f.HostOnline && f.Session != nil && !f.Session.Exited
	switch preferred {
	case "":
		switch {
		case live:
			return GuardWakeType, ""
		case f.HostOnline:
			return GuardWakeRelaunch, ""
		}
		return GuardWakeNone, "主机离线，现在没法唤醒"
	case GuardWakeType:
		if live {
			return GuardWakeType, ""
		}
		return GuardWakeNone, "没有存活的会话可以输入提示词"
	case GuardWakeRelaunch:
		if f.HostOnline {
			return GuardWakeRelaunch, ""
		}
		return GuardWakeNone, "主机离线，现在没法唤醒"
	}
	return GuardWakeNone, "不认识的唤醒方式"
}

// GuardWakePlan 是一次唤醒要执行的内容，由浏览器照着做。
type GuardWakePlan struct {
	Mode string `json:"mode"` // type | relaunch
	Name string `json:"name"` // 预设名
	Host string `json:"host"` // 机器码
	// SessionSID 是 type 方式要连上去输入的存活会话。
	SessionSID string `json:"session_sid,omitempty"`
	// Prompt 是要输入的提示词（含固定的「【系统唤醒 …】」开头）；它不含任何 shell 特殊字符。
	Prompt string `json:"prompt"`
	// Command、Dir、Resume 只用于 relaunch：在 Dir 里新开会话并运行 Command；Resume 表示接着原来的对话（否则是新对话）。
	// AppendPrompt 表示可以把 Prompt 放进引号、追加在 Command 后面一起启动；否则只启动命令，提示词要另行发给它。
	Command      string `json:"command,omitempty"`
	Dir          string `json:"dir,omitempty"`
	Resume       bool   `json:"resume,omitempty"`
	AppendPrompt bool   `json:"append_prompt,omitempty"`
	// N 是这次是滚动 24 小时内的第几次唤醒（含本次），Max 是参考上限；OverBudget 表示这次已经超过上限。
	N          int      `json:"n"`
	Max        int      `json:"max"`
	OverBudget bool     `json:"over_budget"`
	Warnings   []string `json:"warnings,omitempty"`
	// RunID、RunState、Posture 只用于 headless（非交互唤醒）：已经提交的运行、它当前的状态与权限姿态。
	// 这种方式由服务端直接执行，浏览器不需要再做什么，只提示去哪里看运行。
	RunID    string `json:"run_id,omitempty"`
	RunState string `json:"run_state,omitempty"`
	Posture  string `json:"posture,omitempty"`
}

// wakeError 是唤醒失败的原因，面向界面。
type wakeError struct {
	Status  int
	Code    string
	Message string
}

func (e *wakeError) Error() string { return e.Message }

func newWakeError(status int, code, msg string) *wakeError {
	return &wakeError{Status: status, Code: code, Message: msg}
}

// guardAppendable 判断能不能把提示词直接追加在这条命令后面（作为它的最后一个参数）。
// 带管道、重定向、命令替换或以 & ; \ 结尾的命令，追加之后意思就变了。
func guardAppendable(command string) bool {
	c := strings.TrimSpace(command)
	if c == "" || strings.ContainsAny(c, "|<>`") || strings.Contains(c, "$(") {
		return false
	}
	return !strings.HasSuffix(c, "&") && !strings.HasSuffix(c, ";") && !strings.HasSuffix(c, `\`)
}

// guardWakeCommand 决定重新拉起时运行什么：配了重新拉起命令就用它（有 {chat_id} 时换成最近汇报的对话编号），
// 没配、或者要用编号却不知道时，退回预设自己的命令（新开一个对话）。
func guardWakeCommand(p store.TerminalLauncher, chatID string) (command string, resume bool, warnings []string, err error) {
	if rc := strings.TrimSpace(p.Guard.ResumeCommand); rc != "" {
		switch {
		case !strings.Contains(rc, "{chat_id}"):
			return rc, true, nil, nil
		case chatID != "":
			return strings.ReplaceAll(rc, "{chat_id}", chatID), true, nil, nil
		}
		warnings = append(warnings, "还不知道这个智能体的对话编号（hook 没汇报过），重新拉起命令用不了；改为新开一个对话，之前对话的上下文不会带过来，靠提示词让它从 RelayMesh 接上")
	} else {
		warnings = append(warnings, "预设没有配置重新拉起命令，将新开一个对话；之前对话的上下文不会带过来，靠提示词让它从 RelayMesh 接上")
	}
	cmd := strings.TrimSpace(p.Command)
	if cmd == "" {
		return "", false, nil, newWakeError(http.StatusConflict, "no_command", "这个预设没有配置命令，没法重新拉起；请先在预设里填上启动命令（或重新拉起命令）")
	}
	return cmd, false, warnings, nil
}

// wake 生成一份唤醒计划并登记这次唤醒。preferred 为空表示自动选择方式。
func (g *guardService) wake(ctx context.Context, presetID, preferred string, now time.Time) (GuardWakePlan, store.TerminalLauncher, error) {
	var zero store.TerminalLauncher
	launchers, err := g.h.store.ListTerminalLaunchers(ctx)
	if err != nil {
		return GuardWakePlan{}, zero, err
	}
	var p *store.TerminalLauncher
	for i := range launchers {
		if launchers[i].ID == presetID {
			p = &launchers[i]
			break
		}
	}
	switch {
	case p == nil:
		return GuardWakePlan{}, zero, newWakeError(http.StatusNotFound, "launcher_not_found", "这个预设已经不存在了（可能在别处删掉了）")
	case p.Guard == nil:
		return GuardWakePlan{}, zero, newWakeError(http.StatusConflict, "guard_not_set", "这个预设还没有配置会话守护：先在预设里设置工作流编号与重新拉起命令")
	case preferred != "" && preferred != GuardWakeType && preferred != GuardWakeRelaunch && preferred != GuardWakeHeadless:
		return GuardWakePlan{}, zero, newWakeError(http.StatusBadRequest, "bad_mode", "不认识的唤醒方式")
	}

	state, _ := g.h.store.GetTerminalGuardState(ctx)
	var ps *store.GuardPresetState
	if state.Presets != nil {
		ps = state.Presets[p.ID]
	}
	f := g.facts(ctx, *p, now, guardRows{})
	v, _ := g.verdictOf(*p, f, ps)
	if preferred == GuardWakeHeadless {
		return g.wakeHeadless(ctx, p, f, v, now)
	}
	mode, note := guardWakeRoute(f, preferred)
	if mode == GuardWakeNone {
		code := "wake_unavailable"
		if !f.HostOnline {
			code = "host_offline"
		}
		return GuardWakePlan{}, zero, newWakeError(http.StatusConflict, code, note)
	}

	plan := GuardWakePlan{Mode: mode, Name: p.Name, Host: p.Host, Max: guardWakeMax}
	switch mode {
	case GuardWakeType:
		plan.SessionSID = f.Session.SID
	case GuardWakeRelaunch:
		cmd, resume, warns, err := guardWakeCommand(*p, f.Link.ConversationID)
		if err != nil {
			return GuardWakePlan{}, zero, err
		}
		plan.Command, plan.Resume, plan.Warnings, plan.Dir = cmd, resume, warns, p.Dir
		plan.AppendPrompt = guardAppendable(cmd)
		if !plan.AppendPrompt {
			plan.Warnings = append(plan.Warnings, "命令里有管道、重定向或结尾符号，没法安全地把提示词追加进去：只会启动命令，提示词需要你自己发给它")
		}
	}

	reason := guardWakeReason(v)
	if _, err := g.h.store.UpdateTerminalGuardState(ctx, now, func(d *store.GuardStateDoc) {
		pst := d.Preset(p.ID)
		used := pst.WakesSince(now.Add(-guardWakeWindow))
		plan.N, plan.OverBudget = used+1, used >= guardWakeMax
		pst.Wakes = append(pst.Wakes, store.GuardWakeRecord{At: now, Mode: mode, Reason: reason, Episode: v.Episode})
		// 手动唤醒会清除「任务全部完成」标记。
		if pst.Permit != nil && pst.Permit.Permit == 0 {
			pst.Permit = &store.GuardWakePermitState{Permit: 1}
		}
	}); err != nil {
		return GuardWakePlan{}, zero, err
	}
	plan.Prompt = renderWakePrompt(p.Guard.WakePrompt, plan.N, guardWakeMax, reason, p.Guard.WorkflowID)
	g.recordWakeLine(ctx, p.Guard.WorkflowID, plan, reason, now)
	return plan, *p, nil
}

// wakeLineText 是聊天里「系统行」的文字：谁、第几次、怎么唤醒。不含提示词，也不含命令。
func wakeLineText(plan GuardWakePlan, reason string) string {
	if plan.Mode == GuardWakeHeadless {
		posture := "只放行白名单内的操作"
		if plan.Posture == store.HeadlessPostureForce {
			posture = "放开全部命令（--force）"
		}
		return fmt.Sprintf("你手动发起了非交互唤醒（第 %d/%d 次 · 接着原对话运行一次 · %s · 原因：%s）", plan.N, plan.Max, posture, reason)
	}
	how := "键入提示词"
	if plan.Mode == GuardWakeRelaunch {
		how = "重新拉起，新开对话"
		if plan.Resume {
			how = "重新拉起，接着原对话"
		}
	}
	s := fmt.Sprintf("你手动唤醒了智能体（第 %d/%d 次 · %s · 原因：%s）", plan.N, plan.Max, how, reason)
	if plan.OverBudget {
		s += "，已超过 24 小时参考次数"
	}
	return s
}

// recordWakeLine 在这个工作流的聊天里记一条系统行，并通知 Web 刷新。失败只记日志：唤醒本身已经登记，不能因此回滚。
func (g *guardService) recordWakeLine(ctx context.Context, workflowID string, plan GuardWakePlan, reason string, now time.Time) {
	workflowID = strings.TrimSpace(workflowID)
	if workflowID == "" {
		return
	}
	meta, _ := json.Marshal(map[string]any{"mode": plan.Mode, "n": plan.N, "max": plan.Max, "reason": reason, "over_budget": plan.OverBudget})
	g.addWakeLine(ctx, workflowID, "human", "web", wakeLineText(plan, reason), string(meta), now)
}

// SetWakeHook 接入「聊天里多了一条唤醒系统行」的通知；启动时调用一次，传 nil 表示摘掉。fn 必须立即返回。
func (h *Hub) SetWakeHook(fn func(workflowID string)) {
	if h == nil {
		return
	}
	if fn == nil {
		h.wakeHook.Store(nil)
		return
	}
	h.wakeHook.Store(&fn)
}

// ---------- REST ----------

// GuardView 返回守护汇总（与 GET /terminal/guard 相同，供其他 REST 只读引用）。
func (h *Hub) GuardView(ctx context.Context) (GuardView, error) {
	return h.guardian.view(ctx, h.now())
}

func (h *Hub) handleGuardList(c *gin.Context) {
	view, err := h.GuardView(c.Request.Context())
	if err != nil {
		log.Printf("[terminal] 读取会话守护状态失败: %v", err)
		h.fail(c, err)
		return
	}
	c.JSON(http.StatusOK, view)
}

type guardWakeRequest struct {
	// Mode 可选：type 或 relaunch；不填由服务端按会话是否还活着自动选。
	Mode string `json:"mode"`
}

func (h *Hub) handleGuardWake(c *gin.Context) {
	var req guardWakeRequest
	if !bindJSON(c, &req) {
		return
	}
	ctx := c.Request.Context()
	plan, preset, err := h.guardian.wake(ctx, c.Param("id"), strings.TrimSpace(req.Mode), h.now())
	if err != nil {
		var we *wakeError
		if errors.As(err, &we) {
			c.JSON(we.Status, gin.H{"error": we.Message, "code": we.Code})
			return
		}
		log.Printf("[terminal] 生成唤醒计划失败: %v", err)
		h.fail(c, err)
		return
	}
	h.audit.record(ctx, auditEvent{
		Event: EvGuardWake, Actor: subjectOf(c), IP: c.ClientIP(), MachineCode: preset.Host,
		Detail: fmt.Sprintf("preset=%s mode=%s n=%d", preset.ID, plan.Mode, plan.N),
	})
	c.JSON(http.StatusOK, gin.H{"plan": plan})
}

type guardPermitRequest struct {
	Permit int    `json:"permit"`
	Note   string `json:"note,omitempty"`
}

func (h *Hub) handleGuardPermit(c *gin.Context) {
	var req guardPermitRequest
	if !bindJSON(c, &req) {
		return
	}
	ctx := c.Request.Context()
	now := h.now()
	permitState, err := h.guardian.SetWakePermit(ctx, c.Param("id"), req.Permit, "human", req.Note, now)
	if err != nil {
		var we *wakeError
		if errors.As(err, &we) {
			c.JSON(we.Status, gin.H{"error": we.Message, "code": we.Code})
			return
		}
		log.Printf("[terminal] 设置唤醒许可失败: %v", err)
		h.fail(c, err)
		return
	}
	h.audit.record(ctx, auditEvent{
		Event: EvGuardPermit, Actor: subjectOf(c), IP: c.ClientIP(),
		Detail: fmt.Sprintf("preset=%s permit=%d by=human", c.Param("id"), req.Permit),
	})
	c.JSON(http.StatusOK, gin.H{"permit": permitState})
}
