package terminal

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/teacat99/RelayMesh/v2/internal/chat"
	"github.com/teacat99/RelayMesh/v2/internal/model"
	"github.com/teacat99/RelayMesh/v2/internal/store"
	"github.com/teacat99/RelayMesh/v2/internal/termexec"
	"github.com/teacat99/RelayMesh/v2/internal/termproto"
)

// 非交互唤醒（#113 M3.4，设计稿 04 §4 形态 C）。
//
// 会话已经结束、又在界面里点了「非交互唤醒」时，Hub 经这台主机的执行通道运行一次
//
//	agent -p --resume <对话编号> --approve-mcps --trust [--force] --workspace <目录> --output-format stream-json '<唤醒词>'
//
// 它不打开终端、没有 TUI，结束后在聊天里记一条系统行。边界：
//   - 默认关闭，逐预设开启（预设里的 guard.headless）；改预设要先验证密码，所以放宽权限不会被悄悄改掉；
//   - 只在手动点击时发生（需要刚刚解锁，和普通唤醒一样）；没有任何自动触发，自动唤醒仍未授权；
//   - 复用模型执行的全部闸门：全局开关、主机的「模型访问」、暂停、主机的 auto / approve / readonly 模式
//     （approve 模式下这次运行会停在「待批准」，要你在 ExecMesh 里点批准）；
//   - 权限姿态：allowlist（默认，不带 --force，只能做 CLI 自己 permissions.allow 白名单放行的事）或 force（放开全部命令）；
//     实验 8 / 9 的结论：--approve-mcps 只批准 MCP 服务器、不批准工具调用，要让它读写 RelayMesh，
//     需要在 CLI 配置的 permissions.allow 里写 Mcp(<服务器名>:*)；
//   - 与键入唤醒共用 24 小时次数预算，但这里是硬上限（无人值守的模型调用有成本）；
//   - 只用于会话已经结束之后：会话还活着时再续聊同一个对话会和它打架。

// GuardWakeHeadless 是非交互唤醒的方式名。
const GuardWakeHeadless = "headless"

const (
	// headlessCallerName 是运行记录里的「调用方」名字；它同时是结束时认领运行的标记。
	headlessCallerName = "系统唤醒（非交互）"
	// headlessTimeoutSec 是一次非交互运行的默认超时，取平台单次运行上限（7 天）。
	// 这里的 cursor-cli 续聊是一个「会话」，不是一条短命令：原先固定 30 分钟，到点就把正在干活的会话强制结束，
	// 留下「超时 · 30m00s」告警（#113）。非交互续聊预设可以逐个设「最长运行时间」（HeadlessRun.MaxRunMinutes，#141）：
	// 设了就用它（收进 [1 分钟, 这个值]），没设、以及守护的非交互唤醒、会话页的运行，都用这个值。
	// 运行监视的寿命（run_watch）与「还在跑」的兜底阈值（launcherRunStale，只给没有超时值的旧记录用）都由它派生。
	headlessTimeoutSec = termproto.MaxRunTimeoutSec
)

var (
	headlessBinRe  = regexp.MustCompile(`^[A-Za-z0-9_./~+-]+$`)
	headlessChatRe = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
)

// GuardHeadlessView 是预设的非交互唤醒现状（只在预设开启了它时才有）。
type GuardHeadlessView struct {
	Enabled bool   `json:"enabled"`
	Posture string `json:"posture"`
	// Ok 表示现在点它能行；不行时 Note 说明原因。
	Ok   bool   `json:"ok"`
	Note string `json:"note,omitempty"`
}

// shellQuote 把任意文本变成 POSIX shell 里的一个单引号字面量。
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// headlessBinary 取非交互运行的可执行文件：优先用重新拉起命令、其次预设命令的第一个词，必须是一个朴素的路径或命令名。
func headlessBinary(p store.TerminalLauncher) (string, error) {
	cmd := ""
	if p.Guard != nil {
		cmd = strings.TrimSpace(p.Guard.ResumeCommand)
	}
	if cmd == "" {
		cmd = strings.TrimSpace(p.Command)
	}
	fields := strings.Fields(cmd)
	if len(fields) == 0 {
		return "", errors.New("预设没有配置命令，不知道该运行哪个可执行文件")
	}
	// 整条命令里每个词都必须是朴素的词（没有引号、分号、管道、重定向、变量展开、&&），第一个词也不能是 shell 内建 / 前缀命令：
	// 这样第一个词一定是可执行文件本身，而不是 cd、env、sudo 这类会改变后面含义的东西。
	for _, w := range fields {
		if !headlessWordRe.MatchString(w) {
			return "", fmt.Errorf("预设命令里的「%s」不是朴素的词（有引号、分号、管道、重定向或变量展开）；非交互唤醒只支持 agent 这样的命令名或路径加普通参数", w)
		}
	}
	if !headlessBinRe.MatchString(fields[0]) || strings.Contains(fields[0], "=") || store.IsHeadlessPrefixCmd(fields[0]) {
		return "", fmt.Errorf("预设命令的第一个词「%s」不是可执行文件本身（不能是环境变量赋值，也不能是 cd、env、sudo 这类前缀命令）", fields[0])
	}
	return fields[0], nil
}

// headlessWordRe 是预设命令里一个词允许的字符；{} 为了留住 {chat_id} 占位符。
var headlessWordRe = regexp.MustCompile(`^[A-Za-z0-9_./~+@:=,%{}-]+$`)

// headlessModelRe 是 --model 的值允许的字符；与预设校验（store.HeadlessRun）同一个范围，这里是运行前的第二道关。
var headlessModelRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/+-]{0,63}$`)

// headlessCmd 是拼非交互运行命令的全部输入。
type headlessCmd struct {
	Bin string
	// ChatID 是要接着的对话编号；New 为真（新开对话，#176）时必须为空，命令里不带 --resume。
	ChatID  string
	New     bool
	Dir     string
	Posture string
	Prompt  string
	Model   string // 可空：不带 --model
}

// buildHeadlessCommand 拼出非交互运行的一整行命令（没有 --model 的形式，守护的非交互唤醒用它）。
func buildHeadlessCommand(bin, chatID, dir, posture, prompt string) (string, error) {
	return headlessCmd{Bin: bin, ChatID: chatID, Dir: dir, Posture: posture, Prompt: prompt}.build()
}

// build 拼出非交互运行的一整行命令。所有动态部分（对话编号、目录、模型、提示词）都经过校验与单引号包裹。
func (c headlessCmd) build() (string, error) {
	switch {
	case !headlessBinRe.MatchString(c.Bin) || store.IsHeadlessPrefixCmd(c.Bin):
		return "", errors.New("可执行文件名不合法")
	case c.New && c.ChatID != "":
		return "", errors.New("新开对话不能同时指定对话编号")
	case !c.New && !headlessChatRe.MatchString(c.ChatID):
		return "", errors.New("对话编号不合法")
	case !strings.HasPrefix(c.Dir, "/") || strings.ContainsAny(c.Dir, "\x00\r\n"):
		return "", errors.New("工作目录必须是主机上的绝对路径")
	case c.Model != "" && !headlessModelRe.MatchString(c.Model):
		return "", errors.New("模型名不合法")
	case strings.TrimSpace(c.Prompt) == "" || strings.ContainsAny(c.Prompt, "\x00\r\n"):
		return "", errors.New("唤醒词为空或含换行")
	}
	parts := []string{c.Bin, "-p"}
	if !c.New {
		parts = append(parts, "--resume", c.ChatID)
	}
	parts = append(parts, "--approve-mcps", "--trust")
	if c.Model != "" {
		parts = append(parts, "--model", shellQuote(c.Model))
	}
	if c.Posture == store.HeadlessPostureForce {
		parts = append(parts, "--force")
	}
	parts = append(parts, "--workspace", shellQuote(c.Dir), "--output-format", "stream-json", shellQuote(c.Prompt))
	return strings.Join(parts, " "), nil
}

// headlessRoute 判断现在能不能非交互唤醒；不能时给出原因。
func headlessRoute(p store.TerminalLauncher, f guardFacts, hostOS string) (bool, string) {
	g := p.Guard
	switch {
	case g == nil || g.Headless == nil || !g.Headless.Enabled:
		return false, "这个预设没有开启非交互唤醒"
	case strings.Contains(strings.ToLower(hostOS), "windows"):
		return false, "暂不支持 Windows 主机"
	case !f.HostOnline:
		return false, "主机离线，现在没法唤醒"
	case f.Session != nil && !f.Session.Exited:
		return false, "会话还活着：请用「唤醒」把提示词输入进去；非交互唤醒只用于会话已经结束之后"
	case f.Link.ConversationID == "":
		return false, "还不知道智能体的对话编号（hook 没汇报过），没法接着原来的对话"
	case strings.TrimSpace(p.Dir) == "":
		return false, "预设没有配置目录，不知道在哪个工作区里运行"
	}
	if _, err := headlessBinary(p); err != nil {
		return false, err.Error()
	}
	return true, ""
}

// headlessViewOf 给 GET /guard 用的现状；预设没开启时返回 nil。
func (g *guardService) headlessViewOf(ctx context.Context, p store.TerminalLauncher, f guardFacts, ps *store.GuardPresetState, now time.Time) *GuardHeadlessView {
	if p.Guard == nil || p.Guard.Headless == nil || !p.Guard.Headless.Enabled {
		return nil
	}
	hostOS := ""
	if h, err := g.h.store.GetTerminalHost(ctx, p.Host); err == nil && h != nil {
		hostOS = h.OS
	}
	ok, note := headlessRoute(p, f, hostOS)
	if ok && ps != nil && ps.WakesSince(now.Add(-guardWakeWindow)) >= guardWakeMax {
		ok, note = false, fmt.Sprintf("24 小时内已经唤醒 %d 次，达到上限", guardWakeMax)
	}
	return &GuardHeadlessView{Enabled: true, Posture: p.Guard.Headless.EffectivePosture(), Ok: ok, Note: note}
}

// wakeHeadless 执行一次非交互唤醒：先在预算里占一个名额，再提交运行；提交失败就把名额还回去。
func (g *guardService) wakeHeadless(ctx context.Context, p *store.TerminalLauncher, f guardFacts, v GuardVerdict, now time.Time) (GuardWakePlan, store.TerminalLauncher, error) {
	var zero store.TerminalLauncher
	hostOS := ""
	if h, err := g.h.store.GetTerminalHost(ctx, p.Host); err == nil && h != nil {
		hostOS = h.OS
	}
	if ok, note := headlessRoute(*p, f, hostOS); !ok {
		code := "headless_unavailable"
		if p.Guard == nil || p.Guard.Headless == nil || !p.Guard.Headless.Enabled {
			code = "headless_off"
		}
		return GuardWakePlan{}, zero, newWakeError(http.StatusConflict, code, note)
	}
	bin, _ := headlessBinary(*p)
	posture := p.Guard.Headless.EffectivePosture()
	reason := guardWakeReason(v)

	// 占名额（硬上限）。
	n, over := 0, false
	if _, err := g.h.store.UpdateTerminalGuardState(ctx, now, func(d *store.GuardStateDoc) {
		pst := d.Preset(p.ID)
		used := pst.WakesSince(now.Add(-guardWakeWindow))
		if used >= guardWakeMax {
			over = true
			return
		}
		n = used + 1
		pst.Wakes = append(pst.Wakes, store.GuardWakeRecord{At: now, Mode: GuardWakeHeadless, Reason: reason, Episode: v.Episode})
		if pst.Permit != nil && pst.Permit.Permit == 0 {
			pst.Permit = &store.GuardWakePermitState{Permit: 1}
		}
	}); err != nil {
		return GuardWakePlan{}, zero, err
	}
	if over {
		return GuardWakePlan{}, zero, newWakeError(http.StatusConflict, "over_budget",
			fmt.Sprintf("24 小时内已经唤醒 %d 次，达到上限；非交互唤醒是无人值守的模型调用，不允许超过", guardWakeMax))
	}
	release := func() {
		_, _ = g.h.store.UpdateTerminalGuardState(ctx, now, func(d *store.GuardStateDoc) {
			pst := d.Preset(p.ID)
			for i := len(pst.Wakes) - 1; i >= 0; i-- {
				if pst.Wakes[i].At.Equal(now) && pst.Wakes[i].Mode == GuardWakeHeadless {
					pst.Wakes = append(pst.Wakes[:i], pst.Wakes[i+1:]...)
					return
				}
			}
		})
	}

	prompt := renderWakePrompt(p.Guard.WakePrompt, n, guardWakeMax, reason, p.Guard.WorkflowID)
	if strings.TrimSpace(p.Guard.WakePrompt) == "" {
		prompt += headlessRunSuffix // 默认提示词：补上「非交互运行必须把结果发到 RelayMesh」（自定义提示词不动）
	}
	cmd, err := buildHeadlessCommand(bin, f.Link.ConversationID, strings.TrimSpace(p.Dir), posture, prompt)
	if err != nil {
		release()
		return GuardWakePlan{}, zero, newWakeError(http.StatusConflict, "headless_command", err.Error())
	}
	res, err := g.h.exec.headlessRun(ctx, p.Host, strings.TrimSpace(p.Guard.WorkflowID), f.Link.ConversationID, p.Name, strings.TrimSpace(p.Dir), cmd, n)
	if err != nil {
		release()
		var te *termexec.Error
		if errors.As(err, &te) {
			return GuardWakePlan{}, zero, newWakeError(http.StatusConflict, te.Code, te.Message)
		}
		return GuardWakePlan{}, zero, err
	}

	plan := GuardWakePlan{
		Mode: GuardWakeHeadless, Name: p.Name, Host: p.Host, N: n, Max: guardWakeMax,
		RunID: res.RunID, RunState: res.State, Posture: posture,
	}
	if posture == store.HeadlessPostureAllowlist {
		plan.Warnings = append(plan.Warnings, "没有带 --force：智能体只能做 CLI 配置里 permissions.allow 放行的事。要让它读写 RelayMesh，请在那里加上 Mcp(<服务器名>:*)")
	}
	if res.State == model.RunAwaitingApproval {
		plan.Warnings = append(plan.Warnings, "这台主机的执行模式是「需批准」：运行已提交，要在 ExecMesh 里批准之后才会开始")
	}
	g.recordWakeLine(ctx, p.Guard.WorkflowID, plan, reason, now)
	return plan, *p, nil
}

// headlessRun 以「系统唤醒」的身份提交一次运行，不等它结束（立即返回运行编号）。
// 它复用 Exec 的全部闸门与落库；调用方是一个没有令牌的内部身份，只在这里构造。
func (s *ExecService) headlessRun(ctx context.Context, machineCode, workflowID, conversation, presetName, cwd, command string, n int) (*termexec.RunResult, error) {
	return s.submitHeadless(ctx, headlessSubmit{
		Caller: headlessCallerName, UserAgent: "relaymesh-headless-wake",
		Machine: machineCode, Workflow: workflowID, Conversation: conversation, Cwd: cwd, Command: command,
		Description: fmt.Sprintf("非交互唤醒「%s」（第 %d 次）", presetName, n),
	})
}

// headlessSubmit 是提交一次内部非交互运行的全部输入。
type headlessSubmit struct {
	Caller      string // 调用方名字：同时是结束时认领运行的标记
	UserAgent   string
	Machine     string
	Workflow    string
	Cwd         string
	Command     string
	Description string
	Thread      string // 线程标签：预设运行用它把运行记录和预设对上
	// Conversation 是这次运行要续聊的对话编号。给了它（且有工作流），提交成功后就开始读这次运行的输出来判定对话的状态（见 run_watch.go）。
	Conversation string
	// NewConversation 表示这是新开对话的运行（命令里没有 --resume，Conversation 为空）：对话编号要从输出的 init 事件里学到，
	// 并记成这个工作流的对话编号（见 run_watch.go 的 learn）；没有工作流就没法记，也就不监视。
	NewConversation bool
	// TimeoutSec 是这次运行的最长运行时间（秒）；没给（0 或负数）= headlessTimeoutSec（平台单次运行上限），
	// 超过上限的收进上限。预设里的值已经按分钟校验过，这里是最后一道防线。
	TimeoutSec int
}

// timeout 返回这次提交实际使用的超时（秒）。
func (in headlessSubmit) timeout() int {
	if in.TimeoutSec <= 0 || in.TimeoutSec > headlessTimeoutSec {
		return headlessTimeoutSec
	}
	return in.TimeoutSec
}

// submitHeadless 以内部身份把一条命令交给 Exec，不等它结束。闸门（总闸、暂停、主机模型访问、执行模式、令牌限额）全部照常生效。
func (s *ExecService) submitHeadless(ctx context.Context, in headlessSubmit) (*termexec.RunResult, error) {
	cred := &model.MCPCredential{
		Name:          in.Caller,
		Permissions:   model.Permissions{Terminal: true},
		TerminalHosts: []string{in.Machine},
	}
	caller := termexec.Caller{
		CredentialName: in.Caller,
		Credential:     cred,
		Permissions:    cred.Permissions,
		TokenString:    s.headlessToken(),
		Source:         "wake",
		UserAgent:      in.UserAgent,
	}
	timeout := in.timeout()
	out, err := s.Exec(ctx, caller, termexec.ExecRequest{
		Target: in.Machine, Command: in.Command, Description: in.Description, WorkflowID: in.Workflow, Thread: in.Thread,
		Cwd: in.Cwd, TimeoutSeconds: timeout, WaitSet: true, WaitSeconds: 0,
	})
	if err != nil {
		return nil, err
	}
	res, ok := out.(*termexec.RunResult)
	if !ok || res == nil {
		return nil, errors.New("提交运行失败：返回值不认识")
	}
	s.watch.start(runWatchTarget{
		RunID: res.RunID, Machine: in.Machine, WorkflowID: strings.TrimSpace(in.Workflow), Conversation: strings.TrimSpace(in.Conversation),
		Learn:   in.NewConversation,
		Timeout: time.Duration(timeout) * time.Second,
	})
	return res, nil
}

// headlessToken 是内部身份用的随机串：只为通过「令牌强度」检查，不对应任何真实凭据，也从不记录。
func (s *ExecService) headlessToken() string {
	s.headlessTokOnce.Do(func() { s.headlessTok = randHex(24) })
	return s.headlessTok
}

// headlessEndText 把一次非交互唤醒的终态写成聊天里的一句话（不含输出内容）。
func headlessEndText(rec model.TerminalRun) string { return runEndText("非交互唤醒", rec) }

// runEndText 把一次内部非交互运行的终态写成一句话；subject 是「非交互唤醒」或「预设「X」的非交互续聊」。
func runEndText(subject string, rec model.TerminalRun) string {
	secs := rec.DurationMS / 1000
	switch rec.State {
	case model.RunSucceeded:
		return fmt.Sprintf("%s已结束：成功（用时 %d 秒）。完整输出在 ExecMesh 的运行记录里", subject, secs)
	case model.RunFailed:
		code := ""
		if rec.ExitCode != nil {
			code = fmt.Sprintf("，退出码 %d", *rec.ExitCode)
		}
		// 认得出原因（Cursor CLI 自己报的错误类别、崩溃信号）就写上；认不出的不写，免得「未知」占一句话（#151，见 run_exit.go）。
		cause := ""
		if e := classifyRunExit(rec); e.IsFailure() && e.Class != ExitUnknown {
			cause = "原因：" + e.Text + "。"
		}
		return fmt.Sprintf("%s已结束：失败（用时 %d 秒%s）。%s详情在 ExecMesh 的运行记录里", subject, secs, code, cause)
	case model.RunTimedOut:
		// 到点被主机强制结束：写明是「达到运行时长上限」以及上限是多少（#141），不再含糊地说「超时」。
		limit := ""
		if rec.TimeoutSec > 0 {
			limit = "（" + runLimitText(rec.TimeoutSec) + "）"
		}
		return subject + "已结束：达到运行时长上限" + limit + "，已被终止。详情在 ExecMesh 的运行记录里"
	case model.RunCancelled:
		// 说清是谁、为什么取消的（原因码来自运行记录；旧记录没有具体原因）。
		if why := failMessage(rec.DenyReason); why != "" {
			return fmt.Sprintf("%s已结束：被取消（用时 %d 秒）。原因：%s。详情在 ExecMesh 的运行记录里", subject, secs, why)
		}
		return subject + "已结束：被取消"
	case model.RunRejected:
		return subject + "没有执行：在 ExecMesh 里被拒绝，或批准超时"
	case model.RunDenied:
		return subject + "没有执行：被执行闸门拒绝"
	}
	return subject + "已结束：运行状态丢失（主机重启或掉线）"
}

// silentRunNote 是「运行结束了，但智能体没有向 RelayMesh 发出任何消息」时追加在结束行后面的提示（空转退出，#125）。
const silentRunNote = "但这次运行期间智能体没有向 RelayMesh 发出任何消息（空转退出）：命令本身跑通了，只是没有人知道它做了什么。可以到 ExecMesh 的运行记录里看输出，或换个提示词再运行"

// runSpeakGrace 是运行结束时刻之后仍算「运行期间」的宽限：消息落库与运行结束事件到达之间有先后抖动。
const runSpeakGrace = 2 * time.Second

// agentSpoke 判断一次运行期间（from 到结束；to 为空表示还没结束）智能体有没有向这个工作流发过消息：
// 汇报 / 进度通知、提问 / 计划卡、任务进展。运行结束之后（别的会话）发的消息不算——否则事后任何一句话都会让空转显得「说过话」。
// 读不到时返回 true：宁可漏报「空转」，也不凭残缺的事实说它什么都没做。
func (g *guardService) agentSpoke(ctx context.Context, workflowID string, from time.Time, to *time.Time) bool {
	var until time.Time
	if to != nil {
		until = to.Add(runSpeakGrace)
	}
	n, err := g.h.store.CountAgentMessagesBetween(ctx, workflowID, from, until)
	if err != nil {
		log.Printf("[terminal] 统计运行期间的智能体消息失败：%v", err)
		return true
	}
	return n > 0
}

// runWasSilent 判断一次已经正常跑完的运行是不是空转：执行成功、有开始时间、有工作流，而智能体一句话也没发。
func (g *guardService) runWasSilent(ctx context.Context, rec model.TerminalRun) bool {
	wf := strings.TrimSpace(rec.WorkflowID)
	if wf == "" || rec.StartedAt == nil || rec.State != model.RunSucceeded {
		return false
	}
	return !g.agentSpoke(ctx, wf, *rec.StartedAt, rec.EndedAt)
}

// onRunEnded 是执行层每次运行终结时的内部回调：认出系统发起的非交互运行（守护的非交互唤醒、预设的非交互续聊），
// 在聊天里记一条结束的系统行；运行成功却没有向 RelayMesh 发过任何消息时，追加空转提示。
func (g *guardService) onRunEnded(rec model.TerminalRun) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[terminal] 非交互运行结束处理 panic：%v", r)
		}
	}()
	wf := strings.TrimSpace(rec.WorkflowID)
	if rec.CredentialName == launcherRunCaller {
		// 预设的运行异常退出时，看要不要自动续跑（#151）。放在结束行之后（defer），且与有没有工作流无关；它自带 panic 保护。
		defer g.autoResumeOnEnded(rec)
	}
	if wf == "" {
		return
	}
	var text, actor string
	switch rec.CredentialName {
	case headlessCallerName:
		text, actor = headlessEndText(rec), "headless-wake"
	case launcherRunCaller:
		text, actor = runEndText(g.launcherSubject(rec), rec), "launcher-run"
	case sessionRunCaller:
		text, actor = runEndText("会话页的非交互续聊", rec), "session-run"
	default:
		return
	}
	if g.runWasSilent(g.h.ctx, rec) {
		text += "。" + silentRunNote
	}
	g.addWakeLine(g.h.ctx, wf, "system", actor, text, "", g.h.now())
}

// addWakeLine 在这个工作流的聊天里记一条唤醒类系统行，并通知 Web 刷新。失败只记日志：它只是个记录，不能连累唤醒或运行本身。
func (g *guardService) addWakeLine(ctx context.Context, workflowID, actorKind, actorRef, text, meta string, now time.Time) {
	workflowID = strings.TrimSpace(workflowID)
	if workflowID == "" {
		return
	}
	if _, err := g.h.store.AddConversationEvent(ctx, model.ConversationEvent{
		ConversationID: chat.DirectID(workflowID), WorkflowID: workflowID,
		Kind: model.ConversationEventWake, ActorKind: actorKind, ActorRef: actorRef,
		Text: text, Meta: meta, CreatedAt: now,
	}); err != nil {
		log.Printf("[terminal] 记录唤醒系统行失败：%v", err)
		return
	}
	if p := g.h.wakeHook.Load(); p != nil {
		func() {
			defer func() {
				if r := recover(); r != nil {
					log.Printf("[terminal] 唤醒挂钩 panic：%v", r)
				}
			}()
			(*p)(workflowID)
		}()
	}
}
