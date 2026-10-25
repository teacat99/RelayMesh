package terminal

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/teacat99/RelayMesh/v2/internal/model"
	"github.com/teacat99/RelayMesh/v2/internal/sessionprompt"
	"github.com/teacat99/RelayMesh/v2/internal/store"
	"github.com/teacat99/RelayMesh/v2/internal/termexec"
	"github.com/teacat99/RelayMesh/v2/internal/termproto"
)

// 预设运行：「非交互续聊」预设（#126，store.HeadlessRun）。
//
// 把「agent -p --resume <对话> --model <模型> --force … '<提示词>'」这种手敲的命令包成启动页里的预设：
// 点「运行」，Hub 经这台主机的执行通道运行一次（不开终端、没有 TUI），结果显示在预设清单里。边界：
//   - 只在人点了「运行」时发生（需要刚刚解锁，和新建、修改预设一样）；唯一的自动触发是预设里明确打开的
//     「异常退出时自动续跑」（#151，见 auto_resume.go：默认关闭，只重试可重试的错误，有预算与熔断），它走同一条提交路径；
//   - 命令只由预设里已校验的字段拼出，所有动态部分都单引号包裹——预设不能写任意命令；
//   - 与 ExecMesh 同一条通道、同一套闸门（总闸、暂停、主机「模型访问」、主机执行模式、令牌限额）；
//     「需批准」模式下运行会停在待批准，要在 ExecMesh 里批准；
//   - 同一个预设同一时间只跑一次；对话还在运行（工作中 / 等你回答 / 等批准）时不让再续聊它，免得两边打架；
//   - 审计只记预设编号、运行编号与权限姿态，不含提示词与命令；聊天里的系统行同样不含。

const (
	// launcherRunCaller 是预设运行在运行记录里的「调用方」名字；它同时是结束时认领运行的标记。
	launcherRunCaller = "预设运行（非交互续聊）"
	// launcherThreadPrefix 是运行记录线程标签的前缀：「launcher-<预设编号>」把运行记录和预设对上。
	launcherThreadPrefix = "launcher-"
	// launcherRunGrace 是运行自身超时之外的宽限：到点后主机上报终态、Hub 落库还要一会儿。
	launcherRunGrace = 5 * time.Minute
	// launcherRunStale 是没有超时值的运行记录（旧记录）用的兜底阈值：停在「进行中」超过这么久（默认超时 + 宽限）
	// 就不再算「还在跑」。有超时值的记录按它自己的超时算（见 runStaleAfter），逐预设可调的运行时间（#141）才不会
	// 让一条 30 分钟的运行在异常残留后仍挡 7 天。
	launcherRunStale = time.Duration(headlessTimeoutSec)*time.Second + launcherRunGrace
	// launcherReplyRunes 是清单里显示的最后一条回复的长度上限。
	launcherReplyRunes = 400
	// launcherLearnActor 是「新对话已建立」系统行在聊天里的发起者标记（#176）。
	launcherLearnActor = "launcher-run"
)

// launcherRunLimitNote 是追加在默认提示词后面的时限提醒。
func launcherRunLimitNote(maxRunSec int) string {
	return "本次运行最长 " + runLimitText(maxRunSec) + "，到点会被强制结束：请在那之前先向 RelayMesh 汇报进度，没做完的写清下一步。"
}

// runLimitText 把运行时长上限（秒）写成口语：整天写「7 天」、整小时写「2 小时」，其余按分钟（「90 分钟」「1 分钟」）；
// 不足 1 分钟的写秒，不是整分钟的写「N 分 M 秒」——不取整，免得把时限说宽了。
// 时长只用在提示词与聊天系统行里，保持简短、不带小数。
func runLimitText(sec int) string {
	switch {
	case sec <= 0:
		return "未设置"
	case sec%(24*3600) == 0:
		return fmt.Sprintf("%d 天", sec/(24*3600))
	case sec%3600 == 0:
		return fmt.Sprintf("%d 小时", sec/3600)
	case sec%60 == 0:
		return fmt.Sprintf("%d 分钟", sec/60)
	case sec < 60:
		return fmt.Sprintf("%d 秒", sec)
	}
	return fmt.Sprintf("%d 分 %d 秒", sec/60, sec%60)
}

// LauncherRunPlan 是点「运行」之后返回给界面的结果：运行已经由服务端提交，浏览器不需要再做什么。
type LauncherRunPlan struct {
	Name           string `json:"name"`
	Host           string `json:"host"`
	RunID          string `json:"run_id"`
	RunState       string `json:"run_state"`
	Posture        string `json:"posture"`
	Model          string `json:"model,omitempty"`
	ConversationID string `json:"conversation_id"`
	// NewConversation 表示这次运行是新开对话（#176）：命令里没有 --resume，ConversationID 为空——
	// 对话编号要等智能体启动后从它的输出里学到（见 run_watch.go 的 learn）。
	NewConversation bool `json:"new_conversation,omitempty"`
	// AfterNew 是新会话运行之后的去向（#191，store.HeadlessAfter*）：只在 NewConversation 时有，界面据此说明「之后会怎样」。
	AfterNew string `json:"after_new,omitempty"`
	// Continue 表示这次运行是「最近会话」（#191）：命令里是 --continue，ConversationID 为空——
	// 接到哪个对话由 Cursor CLI 决定，要等它启动后从输出里看（见 run_watch.go 的 peek）。
	Continue bool `json:"continue,omitempty"`
	// MaxRunSeconds 是这次运行的最长运行时间（秒）：预设里设的值，没设就是平台上限 7 天。
	MaxRunSeconds int      `json:"max_run_seconds"`
	Warnings      []string `json:"warnings,omitempty"`
}

// LauncherRunView 是一个「非交互续聊」预设最近一次运行的现状（给启动器清单用；运行记录本身在 ExecMesh 里）。
type LauncherRunView struct {
	RunID string `json:"run_id"`
	// ThreadKey 是这条运行记录所在的 ExecMesh 线程键：界面据此链到 ExecMesh 里看完整输出（/exec/r/<键>）。
	ThreadKey string `json:"thread_key,omitempty"`
	// State 是运行记录里的状态（running / awaiting_approval / succeeded / failed / timeout / cancelled / rejected / denied / lost）。
	State string `json:"state"`
	// Active 表示还没结束（含待批准）；残留超过它自己的超时加宽限（runStaleAfter）的记录不再算进行中。
	Active     bool       `json:"active"`
	ExitCode   *int       `json:"exit_code,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
	StartedAt  *time.Time `json:"started_at,omitempty"`
	EndedAt    *time.Time `json:"ended_at,omitempty"`
	DurationMS int64      `json:"duration_ms,omitempty"`
	// TimeoutSec 是这次运行的最长运行时间（秒）：到点主机强制结束它（状态 timeout，界面写「达到运行时长上限」）。
	// 旧记录没有这个值（0 = 不知道）。
	TimeoutSec int `json:"timeout_sec,omitempty"`
	// Reply 是从输出末尾提取的最后一条回复（或没有结构化回复时的输出末行），已脱敏、限长、压成一行；提取不到是空。
	Reply string `json:"reply,omitempty"`
	// Silent 表示这次运行成功了、但期间智能体没有向 RelayMesh 发出任何消息（空转退出）。
	Silent bool `json:"silent,omitempty"`
	// Why 是运行没有正常结束（被取消 / 被拒绝 / 启动失败 / 丢失）时的原因，一句中文；有退出码的失败与正常结束没有它。
	Why string `json:"why,omitempty"`
	// Failure 是异常退出（失败 / 崩溃）时的失败说明：Cursor CLI 自己报的错误类别（可重试 / 需要你处理 / 不可重试）、错误码与一句中文，
	// 认不出时是「未知」。取消、到时长上限、成功、没执行的运行没有它（#151，见 run_exit.go）。
	Failure *RunFailureView `json:"failure,omitempty"`
	// Auto 是自动续跑的现状（只有预设的运行才有，见 auto_resume.go）：这次运行是不是自动续跑出来的、它出事之后系统怎么安排的。
	Auto *AutoResumeView `json:"auto,omitempty"`
	// By 不为空表示这次运行是项目管理 Agent 经 MCP 发起的（#179，见 agent_run.go）：写的是它的名字；人点的运行与自动续跑没有它。
	By string `json:"by,omitempty"`
	// Mode 是这次运行接对话的方式（#191，从命令判断）：resume（指定会话）/ new（新会话）/ continue（最近会话）。
	Mode string `json:"mode,omitempty"`
	// Conversation 是这次运行实际接上的对话编号（#191）：命令里有 --resume 就是它；新会话与最近会话的命令里没有编号，
	// 从输出开头的 init 事件里取（运行还没读到、或认不出时没有）。最近会话接到哪个是 Cursor CLI 的决定，在这里能看到实际结果。
	Conversation string `json:"conversation,omitempty"`
}

// ---------- 提交一次运行 ----------

// allowlistRunWarning 是「白名单」权限姿态（没带 --force）运行时的提示：提交后随运行计划返回，会话页「运行」的确认窗里也先展示。
const allowlistRunWarning = "没有带 --force：智能体只能做 CLI 配置里 permissions.allow 放行的事。要让它读写 RelayMesh，请在那里加上 Mcp(<服务器名>:*)；或把预设的权限姿态改成「放开全部命令」"

// launcherInspect 是对一条非交互续聊预设「现在能不能运行」的完整检查结果。
// 提交运行（runLauncher）与会话页底部「运行」按钮的预览（sessionLaunchers）共用它，所以两边的判断口径永远一致。
// 检查只读库、不产生运行记录，也没有别的副作用。
//
// errs 按提交时先撞到哪个的顺序排列：主机 → 上一次运行 → 对话 → 命令。提交只看第一项；预览把它们都列出来。
// 「上一次运行还没结束」排在「对话还在运行」之前：这次运行自己的输出会让对话显示成工作中，真正的原因是前者（更具体，也更有用）。
type launcherInspect struct {
	preset    store.TerminalLauncher
	run       store.HeadlessRun
	host      *model.TerminalHost
	conv      string
	newConv   bool // 新会话（#176）：命令不带 --resume；conv 为空且没有错误
	cont      bool // 最近会话（#191）：命令用 --continue，接哪个对话由 Cursor CLI 决定；conv 为空且没有错误
	cur       *store.AgentSessionView
	warnings  []string
	maxRunSec int
	command   string
	// windows 表示目标主机是 Windows（#178）：命令是 PowerShell 写法，提交时要让 Agent 用 PowerShell 执行（shell）。
	windows bool
	// shell 是 Windows 主机上执行命令的 shell（pwsh / powershell）；其他主机为空，由 Agent 按主机默认选。
	shell string
	errs  []*wakeError
}

// modeName 是这次运行接对话的方式（store.HeadlessSource*）：检查已经得出结论就用结论（新会话 / 最近会话 / 指定会话），
// 还没有（检查没通过）就按预设配置的来源。
func (in *launcherInspect) modeName() string {
	switch {
	case in.newConv:
		return store.HeadlessSourceNew
	case in.cont:
		return store.HeadlessSourceContinue
	case in.conv != "":
		return store.HeadlessSourceResume
	}
	return in.run.EffectiveSource()
}

func (in *launcherInspect) add(status int, code, msg string) {
	in.errs = append(in.errs, newWakeError(status, code, msg))
}

// inspectLauncher 检查一条预设此刻能不能运行；传入的必须已是「非交互续聊」预设（否则只报 not_headless）。
// 任何一项不满足都记进 errs，但会尽量继续往下检查，好让预览能把原因列全。
func (g *guardService) inspectLauncher(ctx context.Context, p store.TerminalLauncher, now time.Time) *launcherInspect {
	return g.inspectLauncherPrefer(ctx, p, now, "")
}

// inspectLauncherPrefer 与 inspectLauncher 相同，只是多一个「优先接着的对话」：自动续跑把刚出事的那次运行学到的对话传进来，
// 「新会话」每次都新开的预设据此重试接着它，而不是再新开一个（见 launcherPick）。
func (g *guardService) inspectLauncherPrefer(ctx context.Context, p store.TerminalLauncher, now time.Time, prefer string) *launcherInspect {
	in := &launcherInspect{preset: p}
	if !p.IsHeadless() || p.Run == nil {
		in.add(http.StatusConflict, "not_headless", "这个预设不是「非交互续聊」类型，请用「启动」打开终端")
		return in
	}
	run := *p.Run
	in.run = run

	host, herr := g.h.store.GetTerminalHost(ctx, p.Host)
	switch {
	case herr != nil || host == nil:
		in.add(http.StatusNotFound, "host_not_found", "这台主机不存在了（可能已被删除）")
	default:
		in.host = host
		in.windows = isWindowsOS(host.OS)
		a := g.h.onlineAgent(p.Host)
		offline := a == nil || !a.isEnabled()
		if offline {
			in.add(http.StatusConflict, "host_offline", "主机离线，现在没法运行")
		}
		if in.windows {
			// Windows 主机：命令是 PowerShell 写法（#178），要先确认主机上有 PowerShell；离线时没法问，预览按 powershell 算。
			if offline {
				a = nil
			}
			sh, serr := windowsShell(a)
			if serr != nil {
				in.add(http.StatusConflict, "no_powershell", serr.Error())
			}
			in.shell = sh
		}
	}

	pick := g.launcherPick(ctx, run, now, prefer)
	conv, cur, warnings, cerr := pick.Conv, pick.Cur, pick.Warnings, pick.Err
	in.conv, in.cur = conv, cur
	in.newConv = cerr == nil && pick.Mode == convNew
	in.cont = cerr == nil && pick.Mode == convContinue
	if busy, id := g.launcherRunActive(ctx, p.ID, now); busy {
		in.add(http.StatusConflict, "run_active",
			fmt.Sprintf("这个预设上一次运行还没结束（运行编号 %s），等它结束再运行；到 ExecMesh 里可以取消它", id))
	}
	if cerr != nil {
		in.errs = append(in.errs, cerr)
	} else {
		in.warnings = warnings // 因对话还在运行而拒绝时，那一路返回的提示不展示：原因本身已经说清楚了
	}

	in.maxRunSec = run.EffectiveMaxRunSeconds()
	if conv != "" || in.newConv || in.cont {
		kind := sessionprompt.KindResume // 最近会话（--continue）也是续聊：用「续聊」那类唤醒词
		if in.newConv {
			kind = sessionprompt.KindNew
		}
		prompt := renderLauncherWake(ctx, g.h.store, launcherWake{
			Kind: kind, Custom: run.Prompt, WorkflowID: run.WorkflowID, Project: p.Dir, MaxRunSec: in.maxRunSec,
		}).Text
		cmd, berr := headlessCmd{
			Bin: run.EffectiveBin(), ChatID: conv, New: in.newConv, Continue: in.cont, Dir: strings.TrimSpace(p.Dir), Posture: run.EffectivePosture(),
			Prompt: prompt, Model: run.Model, Windows: in.windows,
		}.build()
		if berr != nil {
			in.add(http.StatusConflict, "headless_command", berr.Error())
		} else {
			in.command = cmd
			if in.windows {
				if _, n := winNeutralize(prompt); n > 0 {
					in.warnings = append(in.warnings, winPromptNote(n))
				}
			}
		}
	}
	return in
}

// runLauncher 提交一次预设运行（你手动点了「运行」）。
func (g *guardService) runLauncher(ctx context.Context, presetID string, now time.Time) (LauncherRunPlan, store.TerminalLauncher, error) {
	return g.runLauncherWith(ctx, presetID, now, launcherRunOpts{})
}

// runLauncherWith 提交一次预设运行。先做全部前置检查（任何一项不满足都不会产生运行记录），再经执行通道提交。
// opts.Auto 不为空表示这是自动续跑（#151）发起的：运行说明末尾带「（自动续跑 n/m）」，聊天里的系统行也写明是系统发起的。
func (g *guardService) runLauncherWith(ctx context.Context, presetID string, now time.Time, opts launcherRunOpts) (LauncherRunPlan, store.TerminalLauncher, error) {
	var zero store.TerminalLauncher
	launchers, err := g.h.store.ListTerminalLaunchers(ctx)
	if err != nil {
		return LauncherRunPlan{}, zero, err
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
		return LauncherRunPlan{}, zero, newWakeError(http.StatusNotFound, "launcher_not_found", "这个预设已经不存在了（可能在别处删掉了）")
	case !p.IsHeadless() || p.Run == nil:
		return LauncherRunPlan{}, zero, newWakeError(http.StatusConflict, "not_headless", "这个预设不是「非交互续聊」类型，请用「启动」打开终端")
	}
	run := *p.Run

	// 同一个预设同一时刻只提交一次：检查和落库之间有一小段空窗，连点会让两次提交都通过「上一次运行还没结束」的检查。
	if !g.acquireLauncherRun(p.ID) {
		return LauncherRunPlan{}, zero, newWakeError(http.StatusConflict, "run_active", "这个预设正在提交一次运行，请稍等")
	}
	defer g.releaseLauncherRun(p.ID)

	// 全部前置检查走 inspectLauncher（与会话页「运行」按钮的预览共用，口径永远一致）；提交只看撞到的第一个原因。
	prefer := ""
	if opts.Auto != nil {
		prefer = opts.Auto.Prefer
	}
	in := g.inspectLauncherPrefer(ctx, *p, now, prefer)
	if len(in.errs) > 0 {
		return LauncherRunPlan{}, zero, in.errs[0]
	}
	conv, warnings, maxRunSec, cmd := in.conv, in.warnings, in.maxRunSec, in.command

	desc, userAgent := fmt.Sprintf("预设「%s」的非交互续聊", p.Name), "relaymesh-launcher-run"
	switch {
	case opts.Auto != nil:
		desc += autoResumeSuffix(opts.Auto.Attempt, opts.Auto.Max)
		userAgent = "relaymesh-launcher-auto"
	case opts.Agent != nil:
		desc += agentRunSuffix(opts.Agent.Label)
		userAgent = "relaymesh-launcher-agent"
	}
	afterNew := ""
	if in.newConv {
		afterNew = run.EffectiveAfterNew()
	}
	res, serr := g.h.exec.submitHeadless(ctx, headlessSubmit{
		Caller: launcherRunCaller, UserAgent: userAgent,
		Machine: p.Host, Workflow: strings.TrimSpace(run.WorkflowID), Conversation: conv, NewConversation: in.newConv, ContinueConversation: in.cont, AfterNew: afterNew,
		Cwd: strings.TrimSpace(p.Dir), Command: cmd, Shell: in.shell,
		Description: desc, Thread: launcherThreadPrefix + p.ID,
		TimeoutSec: maxRunSec,
	})
	if serr != nil {
		var te *termexec.Error
		if errors.As(serr, &te) {
			return LauncherRunPlan{}, zero, newWakeError(http.StatusConflict, te.Code, te.Message)
		}
		return LauncherRunPlan{}, zero, serr
	}

	plan := LauncherRunPlan{
		Name: p.Name, Host: p.Host, RunID: res.RunID, RunState: res.State,
		Posture: run.EffectivePosture(), Model: run.Model, ConversationID: conv, NewConversation: in.newConv, AfterNew: afterNew, Continue: in.cont, MaxRunSeconds: maxRunSec, Warnings: warnings,
	}
	if plan.Posture == store.HeadlessPostureAllowlist {
		plan.Warnings = append(plan.Warnings, allowlistRunWarning)
	}
	if res.State == model.RunAwaitingApproval {
		plan.Warnings = append(plan.Warnings, "这台主机的执行模式是「需批准」：运行已提交，要在 ExecMesh 里批准之后才会开始")
	}
	if wf := strings.TrimSpace(run.WorkflowID); wf != "" {
		switch {
		case opts.Auto != nil:
			meta, _ := json.Marshal(map[string]any{"mode": "launcher_auto", "preset": p.ID, "run_id": res.RunID, "state": "started"})
			g.addWakeLine(ctx, wf, "system", autoResumeActor, launcherAutoLineText(plan, *opts.Auto), string(meta), now)
		case opts.Agent != nil:
			meta, _ := json.Marshal(map[string]any{"mode": "launcher_agent", "preset": p.ID, "run_id": res.RunID})
			g.addWakeLine(ctx, wf, "system", agentRunLineActor, launcherAgentLineText(plan, opts.Agent.Label), string(meta), now)
		default:
			meta, _ := json.Marshal(map[string]any{"mode": "launcher_run", "preset": p.ID, "run_id": res.RunID})
			g.addWakeLine(ctx, wf, "human", "web", launcherRunLineText(plan), string(meta), now)
		}
	}
	return plan, *p, nil
}

// launcherAutoLineText 是自动续跑提交运行时聊天里的系统行：谁发起的、第几次、为什么、接着哪个对话、什么模型与权限。
// 和手动运行一样，不含提示词与命令。
func launcherAutoLineText(plan LauncherRunPlan, auto autoRunInfo) string {
	posture := "只放行白名单内的操作"
	if plan.Posture == store.HeadlessPostureForce {
		posture = "放开全部命令（--force）"
	}
	modelName := plan.Model
	if modelName == "" {
		modelName = "默认模型"
	}
	why := ""
	if r := strings.TrimSpace(auto.Reason); r != "" {
		why = "上一次运行出事的原因：" + r + " · "
	}
	return fmt.Sprintf("系统自动续跑了预设「%s」（第 %d/%d 次 · %s非交互续聊 · %s · %s · %s）",
		plan.Name, auto.Attempt, auto.Max, why, convPhrase(plan), modelName, posture)
}

// convPhrase 是聊天系统行里「这次运行接哪个对话」的说法：续聊写对话编号的前 8 位，新开对话（#176）直接写「新开对话」，
// 最近会话（#191）写明接哪个由 Cursor CLI 决定。
func convPhrase(plan LauncherRunPlan) string {
	switch {
	case plan.NewConversation:
		return "新开对话"
	case plan.Continue:
		return "接着最近的对话（由 Cursor CLI 决定）"
	}
	return "接着对话 " + shortConv(plan.ConversationID)
}

// launcherRunLineText 是提交运行时聊天里的系统行：谁、接着哪个对话、什么模型与权限。不含提示词，也不含命令。
func launcherRunLineText(plan LauncherRunPlan) string {
	return fmt.Sprintf("你手动运行了预设「%s」（%s）", plan.Name, launcherPlanSummary(plan))
}

// launcherPlanSummary 是运行系统行括号里的那一段：非交互续聊 · 接着哪个对话 · 什么模型 · 什么权限（· 最长运行多久）。
// 手动运行与项目管理 Agent 发起的运行（agent_run.go）共用，所以两种系统行描述运行的口径一致。
func launcherPlanSummary(plan LauncherRunPlan) string {
	posture := "只放行白名单内的操作"
	if plan.Posture == store.HeadlessPostureForce {
		posture = "放开全部命令（--force）"
	}
	modelName := plan.Model
	if modelName == "" {
		modelName = "默认模型"
	}
	limit := ""
	if plan.MaxRunSeconds > 0 && plan.MaxRunSeconds < headlessTimeoutSec {
		limit = " · 最长运行 " + runLimitText(plan.MaxRunSeconds) // 只在预设设了时限时写：默认的 7 天不是用户的选择，不占位置
	}
	return fmt.Sprintf("非交互续聊 · %s · %s · %s%s", convPhrase(plan), modelName, posture, limit)
}

// shortConv 取对话编号的前 8 位用来显示（编号本身不是秘密，只是太长）。
func shortConv(id string) string {
	if len(id) > 8 {
		return id[:8] + "…"
	}
	return id
}

// convMode 是一次预设运行接对话的方式：对应命令里的 `--resume <编号>` / 不带 / `--continue`（#191）。
type convMode int

const (
	// convResume：指定会话——命令带 `--resume <对话编号>`（含「跟随工作流」的存量预设）。
	convResume convMode = iota
	// convNew：新会话——命令不带 `--resume`，对话编号要从输出的 init 事件里学。
	convNew
	// convContinue：最近会话——命令带 `--continue`，由 Cursor CLI 决定接哪个对话。
	convContinue
)

// convPick 是 launcherPick 的结果：这次运行接对话的方式、对话编号（只有 convResume 才有）、已知的对话会话、提示与拒绝原因。
type convPick struct {
	Mode     convMode
	Conv     string
	Cur      *store.AgentSessionView
	Warnings []string
	Err      *wakeError
}

// launcherConversation 找出这次要接着的对话，并判断它现在能不能被续聊（#191 之前的四值形式，会话页「运行」按钮用它）。
// 「新会话 / 最近会话」没有对话编号，都按「没有编号、也没有错误」返回；需要区分它们的调用方用 launcherPick。
func (g *guardService) launcherConversation(ctx context.Context, run store.HeadlessRun, now time.Time) (string, *store.AgentSessionView, []string, *wakeError) {
	pk := g.launcherPick(ctx, run, now, "")
	return pk.Conv, pk.Cur, pk.Warnings, pk.Err
}

// launcherPick 找出这次运行接对话的方式，并判断现在能不能这样运行。
//
// 对话来源（run.EffectiveSource，#191）：
//   - 指定会话：预设里固定了对话编号就用它；否则取这个工作流里最近有动静的对话（hook 汇报的）。
//     对话还在运行（工作中且没卡住、在等你回答、在等批准）时拒绝：原进程还活着，再续聊同一个对话会和它打架；
//     卡住（很久没动静）、已结束、离线都放行——这正是需要续聊的时候；空闲只提示。
//   - 新会话：「记下编号」（存量预设的勾选）没有已知对话时新开一次，之后接着学到的对话；「不切换」「成功后切到最近会话」
//     每次运行都新开，不看工作流已有的对话。工作流里已经有智能体在运行就不新开：再开一个会和它重复接入同一个会话。
//   - 最近会话：命令用 `--continue`。Hub 不知道它会接到哪个对话，只能按这个工作流已知的对话做近似的保护，并提示可能接错。
//
// prefer 是自动续跑传来的「刚出事的那次运行学到的对话」：「新会话」每次都新开时，重试要接着它、不能再新开一个；
// 其它来源不用它（指定会话有自己的编号，最近会话重试仍是 --continue，记下编号的预设已经把它记在工作流上了）。
//
// 同时返回这个对话在 Agent 状态里的会话（没有就是 nil）：会话页「运行」（#128）要从里面取主机名与项目目录。
// 因为「对话还在运行」而拒绝时，对话编号与会话照样返回（预览要显示它），调用方看到错误就不要运行。
func (g *guardService) launcherPick(ctx context.Context, run store.HeadlessRun, now time.Time, prefer string) convPick {
	wf := strings.TrimSpace(run.WorkflowID)
	var views []store.AgentSessionView
	if wf != "" {
		v, err := g.h.store.ListAgentSessions(ctx, store.AgentSessionFilter{WorkflowID: wf, Limit: 20}, now)
		if err != nil {
			log.Printf("[terminal] 预设运行读取智能体会话失败：%v", err)
		}
		views = v
	}

	conv := run.ConversationID
	switch run.EffectiveSource() {
	case store.HeadlessSourceContinue:
		return g.pickContinue(ctx, wf, views)
	case store.HeadlessSourceNew:
		if after := run.EffectiveAfterNew(); after != store.HeadlessAfterRemember {
			if prefer == "" {
				// 每次运行都新开：不看工作流里已有的对话编号。
				if wf == "" {
					return convPick{Err: newWakeError(http.StatusConflict, "no_conversation", "「新会话」需要填工作流编号：新对话靠它接入 RelayMesh")}
				}
				if e := liveAgentError(views); e != nil {
					return convPick{Err: e}
				}
				return convPick{Mode: convNew, Warnings: []string{newConversationEveryNotice(after)}}
			}
			conv = prefer // 自动续跑：接着刚出事的那次运行学到的对话
		}
	}

	var cur *store.AgentSessionView
	for i := range views {
		if (conv != "" && views[i].ConversationID == conv) || (conv == "" && views[i].ConversationID != "") {
			cur = &views[i]
			break
		}
	}
	if conv == "" {
		switch {
		case cur != nil:
			conv = cur.ConversationID
		case wf != "":
			conv = g.h.store.AgentLinkOf(ctx, wf).ConversationID
		}
	}
	if conv == "" {
		if run.EffectiveSource() == store.HeadlessSourceNew && wf != "" {
			// 新开对话（#176）：返回「新会话、没有对话编号、也没有错误」，调用方据此知道要新开。
			// 工作流里已经有智能体在运行就不新开：再开一个会和它重复接入同一个会话。
			if e := liveAgentError(views); e != nil {
				return convPick{Err: e}
			}
			return convPick{Mode: convNew, Warnings: []string{newConversationNotice}}
		}
		return convPick{Cur: cur, Err: newWakeError(http.StatusConflict, "no_conversation",
			"还不知道要接着哪个对话（hook 没汇报过这个工作流的对话编号）：在预设里固定一个对话编号，或先让智能体在这个工作流里跑一次")}
	}
	warnings, err := conversationStateGuard(cur)
	if err != nil {
		return convPick{Mode: convResume, Conv: conv, Cur: cur, Err: err}
	}
	return convPick{Mode: convResume, Conv: conv, Cur: cur, Warnings: warnings}
}

// pickContinue 是「最近会话」（--continue）的运行前检查：Hub 在运行前不知道 Cursor CLI 会接到哪个对话，
// 只能按这个工作流已知的对话做近似的保护——已知对话正在工作 / 等你时拒绝（与指定会话同一套 conversation_live），
// 未知就放行；任何情况都带一条「可能接错」的提示。
func (g *guardService) pickContinue(ctx context.Context, wf string, views []store.AgentSessionView) convPick {
	var cur *store.AgentSessionView
	for i := range views {
		if views[i].ConversationID != "" {
			cur = &views[i]
			break
		}
	}
	pk := convPick{Mode: convContinue, Cur: cur}
	warnings, err := conversationStateGuard(cur)
	if err != nil {
		pk.Err = err
		return pk
	}
	pk.Warnings = append([]string{continueNotice}, warnings...)
	return pk
}

// conversationStateGuard 按对话此刻的状态判断能不能被续聊：在运行就拒绝，卡住 / 空闲给提示。cur 为空（不知道状态）就放行。
func conversationStateGuard(cur *store.AgentSessionView) ([]string, *wakeError) {
	if cur == nil {
		return nil, nil
	}
	var warnings []string
	switch cur.State {
	case "working":
		if cur.Stalled {
			warnings = append(warnings, "这个对话看起来已经卡住（很久没有动静），所以仍然接着它运行；如果它其实还活着，两边会打架")
		} else {
			return nil, liveConversationError("工作中")
		}
	case "waiting_you":
		return nil, liveConversationError("在等你回答")
	case "waiting_approval":
		return nil, liveConversationError("在等你批准")
	case "idle":
		warnings = append(warnings, "这个对话现在是空闲状态；如果它还开在别的窗口里，同时续聊同一个对话会和它打架")
	}
	return warnings, nil
}

func liveConversationError(state string) *wakeError {
	return newWakeError(http.StatusConflict, "conversation_live",
		fmt.Sprintf("这个对话现在还在运行（%s）：原来的进程还活着，再续聊同一个对话会和它打架。等它停下来、或者掉线之后再运行", state))
}

// newConversationNotice 是新开对话时随运行计划返回的提示（不挡运行）。
const newConversationNotice = "这个工作流还没有智能体接入：这次运行会新开一个对话；对话编号从它的输出里学到之后，再运行就是接着它，不会再新开"

// continueNotice 是「最近会话」（--continue）随运行计划返回的提示（不挡运行）：接哪个对话是 Cursor CLI 的决定。
const continueNotice = "这次运行用 --continue，接着这个目录里 Cursor CLI 认的最近对话——它由 CLI 决定，可能不是这个工作流的（同一目录有多个会话时，请改用「指定会话」）"

// newConversationEveryNotice 是「新会话」每次都新开时随运行计划返回的提示（不挡运行）；after 是「运行之后的去向」。
func newConversationEveryNotice(after string) string {
	if after == store.HeadlessAfterContinue {
		return "这个预设是「新会话 · 成功后切到最近会话」：这次运行会新开一个对话；运行成功之后，预设会自动切换成「最近会话」"
	}
	return "这个预设是「新会话 · 不切换」：每次运行都会新开一个对话，不会接着上一次的"
}

// liveAgentError 判断这个工作流里有没有智能体正在运行（工作中且没卡住、在等你回答、在等你批准）：有就返回错误——
// 新开对话要避开它（#176：防重复启用）。代码沿用 conversation_live：自动续跑等调用方对它的处理是一样的（有人在用，不抢）。
// 空闲、卡住、已结束、离线都不算在运行。
func liveAgentError(views []store.AgentSessionView) *wakeError {
	for _, v := range views {
		switch {
		case v.State == "working" && !v.Stalled:
			return liveWorkflowError("工作中")
		case v.State == "waiting_you":
			return liveWorkflowError("在等你回答")
		case v.State == "waiting_approval":
			return liveWorkflowError("在等你批准")
		}
	}
	return nil
}

func liveWorkflowError(state string) *wakeError {
	return newWakeError(http.StatusConflict, "conversation_live",
		fmt.Sprintf("这个工作流里已经有智能体在运行（%s）：不用再新开一个，再开会重复接入同一个会话。等它停下来、或者掉线之后再运行", state))
}

// latestLauncherRun 取一个预设最近一次运行记录；从没运行过返回 nil。
func (g *guardService) latestLauncherRun(ctx context.Context, presetID string) *model.TerminalRun {
	return g.latestRunBy(ctx, launcherRunCaller, launcherThreadPrefix+presetID)
}

// latestRunBy 按调用方名字与线程标签取最近一次内部非交互运行记录；从没运行过返回 nil。
func (g *guardService) latestRunBy(ctx context.Context, caller, thread string) *model.TerminalRun {
	rows, err := g.h.store.ListTerminalRuns(ctx, store.RunFilter{CredentialName: caller, Thread: thread, Limit: 1})
	if err != nil {
		log.Printf("[terminal] 读取最近一次非交互运行失败：%v", err)
		return nil
	}
	if len(rows) == 0 {
		return nil
	}
	return &rows[0]
}

// launcherRunActive 判断这个预设是不是还有一次没结束的运行；返回它的运行编号。
func (g *guardService) launcherRunActive(ctx context.Context, presetID string, now time.Time) (bool, string) {
	rec := g.latestLauncherRun(ctx, presetID)
	if rec == nil {
		return false, ""
	}
	return runActive(*rec, now), rec.RunID
}

// runStaleAfter 是一条运行记录停在「进行中」多久之后不再算「还在跑」：它自己的超时加宽限（#141：每次运行的超时可以不同）；
// 记录里没有超时值（旧记录）就用兜底阈值 launcherRunStale。
// 这只是「已经开始」的运行的阈值；还没开始（等批准）的运行见 runActive，它还要加上等批准的时间。
func runStaleAfter(rec model.TerminalRun) time.Duration {
	if rec.TimeoutSec > 0 {
		return time.Duration(rec.TimeoutSec)*time.Second + launcherRunGrace
	}
	return launcherRunStale
}

// runActive 判断一条预设运行记录是不是「还在跑」：状态没终结、且没有残留到过期。
// 时间从运行真正开始算：主机的超时从开始那刻起计，与 ExecService.maintain 的口径一致。
// 「需批准」的主机上还有两段不算在超时里的时间，都要照顾到——否则超时很短的运行（比如 1 分钟）会在批准之前，
// 或批准之后、主机报「已开始」之前就被当成残留，第二次点「运行」会再提交一个：
//   - 已批准、主机还没报「已开始」：从批准那刻算；
//   - 还在等批准：从创建时算，阈值再加上等批准的最长时间（execApprovalTTL）。
func runActive(rec model.TerminalRun, now time.Time) bool {
	if model.IsFinalRunState(rec.State) {
		return false
	}
	base, limit := rec.CreatedAt, runStaleAfter(rec)
	switch {
	case rec.StartedAt != nil && !rec.StartedAt.IsZero():
		base = *rec.StartedAt
	case rec.ApprovedAt != nil && !rec.ApprovedAt.IsZero():
		base = *rec.ApprovedAt
	case rec.State == model.RunAwaitingApproval:
		limit += execApprovalTTL
	}
	return now.Sub(base) < limit
}

// launcherSubject 是结束行里的主语：预设「X」的非交互续聊（预设已删掉时退回预设编号）。
func (g *guardService) launcherSubject(rec model.TerminalRun) string {
	id := strings.TrimPrefix(rec.Thread, launcherThreadPrefix)
	name := id
	if list, err := g.h.store.ListTerminalLaunchers(g.h.ctx); err == nil {
		for _, l := range list {
			if l.ID == id {
				name = l.Name
				break
			}
		}
	}
	return fmt.Sprintf("预设「%s」的非交互续聊", name)
}

// ---------- 最近一次运行的现状 ----------

// launcherRunViews 返回每个非交互续聊预设最近一次运行的现状（从没运行过的预设没有条目）。
func (g *guardService) launcherRunViews(ctx context.Context, now time.Time) (map[string]LauncherRunView, error) {
	launchers, err := g.h.store.ListTerminalLaunchers(ctx)
	if err != nil {
		return nil, err
	}
	state, serr := g.h.store.GetTerminalGuardState(ctx)
	if serr != nil {
		log.Printf("[terminal] 读取自动续跑状态失败（清单照常显示，不带自动续跑信息）：%v", serr)
		state = store.GuardStateDoc{}
	}
	out := map[string]LauncherRunView{}
	for _, p := range launchers {
		if !p.IsHeadless() {
			continue
		}
		rec := g.latestLauncherRun(ctx, p.ID)
		if rec == nil {
			continue
		}
		v := g.runViewOf(ctx, *rec, now)
		v.Auto = autoResumeViewOf(p, state.Presets[p.ID], *rec, g.autoResumeOK(p))
		out[p.ID] = v
	}
	return out, nil
}

// ---------- 不属于任何预设的运行（#147） ----------

// 「其他非交互运行」的来源：Hub 自己提交的非交互运行里，预设清单没有地方放的那几类。
const (
	// OtherRunSessionRun 是旧的会话页运行接口（POST /terminal/session-run）提交的运行：新页面走预设，但旧页面与脚本还在用它。
	OtherRunSessionRun = "session_run"
	// OtherRunWake 是会话守护的「非交互唤醒」提交的运行（#113）。守护在 #245 下线后不会再有新的，旧的记录仍按它归类显示。
	OtherRunWake = "wake"
	// OtherRunDeletedPreset 是所属预设已经被删除的预设运行。
	OtherRunDeletedPreset = "deleted_preset"
)

const (
	// otherRunsMax 是「其他非交互运行」最多列几条：进行中的一条不少（要能看到、能取消），结束了的补到这个数。
	otherRunsMax = 10
	// otherRunsWindow 是结束了的运行在清单里留多久。
	otherRunsWindow = 24 * time.Hour
	// otherRunsScan 是每一类来源最多回看几条运行记录；进行中的运行一定是最近的，不会落到这个范围之外。
	otherRunsScan = 100
)

// LauncherOtherRun 是一次不属于任何预设的 Hub 提交的非交互运行。
// 预设清单只认「预设自己的最近一次运行」，这类运行在那里没有位置，以前只能到 ExecMesh 里翻；
// 启动器弹窗把它们单列出来，免得「明明有会话在运行，弹窗里却什么都看不到」，进行中的还可以取消。
type LauncherOtherRun struct {
	LauncherRunView
	// Source 是这次运行的来源，见 OtherRun* 常量。
	Source string `json:"source"`
	// WorkflowID 是这次运行续聊的对话所在的工作流（可能为空）。
	WorkflowID string `json:"workflow_id,omitempty"`
	// Host 是运行所在主机的机器码；界面按它换成主机名。
	Host string `json:"host"`
	// Dir 是运行的工作目录（主机上的路径），界面据此判断它属不属于「本项目」。
	Dir string `json:"dir,omitempty"`
	// Description 是提交时写下的一句话说明（例如「会话页运行：接着对话 chat-s1 的非交互续聊」）。
	Description string `json:"description,omitempty"`
}

// runFinishedAt 是一条运行记录结束的时间：没有结束时间（直接标成终态的拒绝 / 否决）退回最后更新时间。
func runFinishedAt(rec model.TerminalRun) time.Time {
	if rec.EndedAt != nil && !rec.EndedAt.IsZero() {
		return *rec.EndedAt
	}
	if !rec.UpdatedAt.IsZero() {
		return rec.UpdatedAt
	}
	return rec.CreatedAt
}

// otherRunViews 列出 Hub 提交的、不属于任何预设的非交互运行：进行中的全部列出，结束了的只列 24 小时之内的，
// 合起来最多 10 条（进行中的不受这个数限制），新的在前。
//
// 「属于预设」只指预设还在、并且是它自己的运行（线程标签 launcher-<预设编号>）：这类运行已经在预设下面显示。
// 预设删掉之后它留下的运行就成了「其他」；会话页旧接口与非交互唤醒的运行从来不属于预设。
// 停在「进行中」却早已过了自己时限的残留记录（runActive 为否、状态又没终结）不列：它们已经不在跑了。
func (g *guardService) otherRunViews(ctx context.Context, now time.Time) ([]LauncherOtherRun, error) {
	launchers, err := g.h.store.ListTerminalLaunchers(ctx)
	if err != nil {
		return nil, err
	}
	presets := make(map[string]bool, len(launchers))
	for _, p := range launchers {
		presets[p.ID] = true
	}

	type candidate struct {
		rec    model.TerminalRun
		source string
		active bool
	}
	var cands []candidate
	activeN := 0
	for _, src := range []struct{ caller, source string }{
		{sessionRunCaller, OtherRunSessionRun},
		{headlessCallerName, OtherRunWake},
		{launcherRunCaller, OtherRunDeletedPreset},
	} {
		rows, err := g.h.store.ListTerminalRuns(ctx, store.RunFilter{CredentialName: src.caller, Limit: otherRunsScan})
		if err != nil {
			return nil, err
		}
		for _, rec := range rows {
			if src.caller == launcherRunCaller && presets[strings.TrimPrefix(rec.Thread, launcherThreadPrefix)] {
				continue
			}
			active := runActive(rec, now)
			if active {
				activeN++
			} else if !model.IsFinalRunState(rec.State) || runFinishedAt(rec).Before(now.Add(-otherRunsWindow)) {
				continue
			}
			cands = append(cands, candidate{rec: rec, source: src.source, active: active})
		}
	}
	// 三类来源合在一起，按提交的先后（主键）新的在前。
	sort.SliceStable(cands, func(i, j int) bool { return cands[i].rec.ID > cands[j].rec.ID })

	room := otherRunsMax - activeN // 结束了的还能列几条
	out := make([]LauncherOtherRun, 0, len(cands))
	for _, c := range cands {
		if !c.active {
			if room <= 0 {
				continue
			}
			room--
		}
		out = append(out, LauncherOtherRun{
			LauncherRunView: g.runViewOf(ctx, c.rec, now),
			Source:          c.source,
			WorkflowID:      strings.TrimSpace(c.rec.WorkflowID),
			Host:            c.rec.Target,
			Dir:             strings.TrimSpace(c.rec.Cwd),
			Description:     strings.TrimSpace(c.rec.Description),
		})
	}
	return out, nil
}

// runViewOf 把一条内部非交互运行记录整理成界面用的现状（预设清单、会话页底部的「运行」按钮与 session-run 接口共用）。
func (g *guardService) runViewOf(ctx context.Context, rec model.TerminalRun, now time.Time) LauncherRunView {
	v := LauncherRunView{
		RunID: rec.RunID, ThreadKey: rec.ThreadKey, State: rec.State, Active: runActive(rec, now), ExitCode: rec.ExitCode,
		CreatedAt: rec.CreatedAt, StartedAt: rec.StartedAt, EndedAt: rec.EndedAt, DurationMS: rec.DurationMS,
		TimeoutSec: rec.TimeoutSec, By: parseAgentRunSuffix(rec.Description),
		Mode: runModeOf(rec.Command), Conversation: convOfRun(rec),
	}
	if model.IsFinalRunState(rec.State) {
		// 输出不超过开头预览的容量时，整份输出都在 PreviewHead、PreviewTail 为空；更长时结尾在 PreviewTail。
		src := rec.PreviewTail
		if strings.TrimSpace(src) == "" {
			src = rec.PreviewHead
		}
		v.Reply = extractRunReply(src)
		v.Silent = g.runWasSilent(ctx, rec)
		if rec.State == model.RunCancelled || rec.State == model.RunRejected || rec.State == model.RunDenied ||
			rec.State == model.RunLost || (rec.State == model.RunFailed && rec.ExitCode == nil) {
			v.Why = failMessage(rec.DenyReason)
		}
		v.Failure = failureViewOf(classifyRunExit(rec))
		if v.Failure != nil && v.Failure.Raw != "" && v.Reply == v.Failure.Raw {
			v.Reply = "" // 失败时「最后一条回复」常常就是 CLI 的那行错误：已经在失败说明里了，不重复显示
		}
	}
	return v
}

// runModeOf 从一条非交互续聊命令判断这次运行接对话的方式：带 --continue 是最近会话，带 --resume 是指定会话，都没有是新会话（#191）。
func runModeOf(cmd string) string {
	switch {
	case isContinueCommand(cmd):
		return store.HeadlessSourceContinue
	case convFromRunCommand(cmd) != "":
		return store.HeadlessSourceResume
	}
	return store.HeadlessSourceNew
}

// extractRunReply 从运行输出的末尾预览里提取「最后一条回复」。
//
// agent 的 stream-json 输出每行一个事件，结束时有一行 {"type":"result","result":"<最终回复>",…}；
// 找不到它（失败时输出的往往是一行纯文本报错）就退回输出的最后一个纯文本行——像 JSON 的行（以 { 开头，
// 或以 } 结尾：可能是被预览窗口截断的半行事件）不当作回复。
// 预览已脱敏并去掉控制序列；这里再压成一行、限长。
func extractRunReply(preview string) string {
	lines := strings.Split(preview, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		ln := strings.TrimSpace(lines[i])
		if !strings.HasPrefix(ln, "{") {
			continue
		}
		var ev struct {
			Type   string `json:"type"`
			Result string `json:"result"`
		}
		if json.Unmarshal([]byte(ln), &ev) == nil && ev.Type == "result" && strings.TrimSpace(ev.Result) != "" {
			return oneLineClip(ev.Result, launcherReplyRunes)
		}
	}
	for i := len(lines) - 1; i >= 0; i-- {
		ln := strings.TrimSpace(lines[i])
		if ln != "" && !strings.HasPrefix(ln, "{") && !strings.HasSuffix(ln, "}") {
			return oneLineClip(ln, launcherReplyRunes)
		}
	}
	return ""
}

// oneLineClip 把文本压成一行（空白折叠）、清掉不可显示的字符，并限制长度。
func oneLineClip(s string, maxRunes int) string {
	return termproto.SanitizeText(strings.Join(strings.Fields(s), " "), maxRunes)
}

// ---------- REST ----------

func (h *Hub) handleLauncherRun(c *gin.Context) {
	ctx := c.Request.Context()
	plan, preset, err := h.guardian.runLauncher(ctx, c.Param("id"), h.now())
	if err != nil {
		var we *wakeError
		if errors.As(err, &we) {
			c.JSON(we.Status, gin.H{"error": we.Message, "code": we.Code})
			return
		}
		log.Printf("[terminal] 提交预设运行失败: %v", err)
		h.fail(c, err)
		return
	}
	h.audit.record(ctx, auditEvent{
		Event: EvLauncherRun, Actor: subjectOf(c), IP: c.ClientIP(), MachineCode: preset.Host, RunID: plan.RunID,
		Detail: fmt.Sprintf("preset=%s posture=%s", preset.ID, plan.Posture),
	})
	c.JSON(http.StatusOK, gin.H{"run": plan})
}

// handleAutoResumeCancel 取消一条还没开跑的自动续跑（路径里的运行编号是出事的那次运行）。
// 它只会让系统少做一件事，所以和查看现状一样只要登录，不需要刚刚解锁。
func (h *Hub) handleAutoResumeCancel(c *gin.Context) {
	ctx := c.Request.Context()
	preset, err := h.guardian.cancelAutoResume(ctx, c.Param("run_id"), h.now())
	if err != nil {
		var we *wakeError
		if errors.As(err, &we) {
			c.JSON(we.Status, gin.H{"error": we.Message, "code": we.Code})
			return
		}
		log.Printf("[terminal] 取消自动续跑失败: %v", err)
		h.fail(c, err)
		return
	}
	h.audit.record(ctx, auditEvent{
		Event: EvLauncherAutoStop, Actor: subjectOf(c), IP: c.ClientIP(), MachineCode: preset.Host, RunID: strings.TrimSpace(c.Param("run_id")),
		Detail: "preset=" + preset.ID,
	})
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// handleLauncherRuns 返回各非交互续聊预设最近一次运行的现状（runs），以及不属于任何预设的运行（others，#147）。
func (h *Hub) handleLauncherRuns(c *gin.Context) {
	ctx, now := c.Request.Context(), h.now()
	views, err := h.guardian.launcherRunViews(ctx, now)
	if err != nil {
		log.Printf("[terminal] 读取预设运行现状失败: %v", err)
		h.fail(c, err)
		return
	}
	others, err := h.guardian.otherRunViews(ctx, now)
	if err != nil {
		log.Printf("[terminal] 读取其他非交互运行失败: %v", err)
		h.fail(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"runs": views, "others": others})
}
