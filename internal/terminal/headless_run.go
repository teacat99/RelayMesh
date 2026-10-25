package terminal

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"regexp"
	"strings"
	"time"

	"github.com/teacat99/RelayMesh/v2/internal/model"
	"github.com/teacat99/RelayMesh/v2/internal/store"
	"github.com/teacat99/RelayMesh/v2/internal/termexec"
	"github.com/teacat99/RelayMesh/v2/internal/termproto"
)

// 非交互运行的公共部分：拼命令、提交给 Exec、结束时在聊天里记一条系统行。
//
// 预设运行（launcher_run.go）、会话页运行（session_run.go）、项目管理 Agent 运行预设（agent_run.go）、自动续跑（auto_resume.go）都走这里。
// 它原先与「会话守护的非交互唤醒」（#113）放在一起；守护连同交互式「开终端」预设在 #245 第二期下线，这里只留下共用的部分。
// 边界不变：复用模型执行的全部闸门（全局开关、主机的「模型访问」、暂停、主机的 auto / approve / readonly 模式），
// 命令里每个动态部分（对话编号、目录、模型、提示词）都经过校验与单引号包裹。

const (
	// headlessCallerName 是旧记录里的「调用方」名字：会话守护的非交互唤醒（#113）提交的运行用它，守护在 #245 下线后不会再有新的。
	// 保留常量只为认出历史运行（运行监视重启后的恢复、预设「其他运行」列表）；现在没有任何代码用它提交运行。
	headlessCallerName = "系统唤醒（非交互）"
	// headlessTimeoutSec 是一次非交互运行的默认超时，取平台单次运行上限（7 天）。
	// 这里的 cursor-cli 续聊是一个「会话」，不是一条短命令：原先固定 30 分钟，到点就把正在干活的会话强制结束，
	// 留下「超时 · 30m00s」告警（#113）。非交互续聊预设可以逐个设「最长运行时间」（HeadlessRun.MaxRunMinutes，#141）：
	// 设了就用它（收进 [1 分钟, 这个值]），没设、以及会话页的运行，都用这个值。
	// 运行监视的寿命（run_watch）与「还在跑」的兜底阈值（launcherRunStale，只给没有超时值的旧记录用）都由它派生。
	headlessTimeoutSec = termproto.MaxRunTimeoutSec
)

var (
	headlessBinRe  = regexp.MustCompile(`^[A-Za-z0-9_./~+-]+$`)
	headlessChatRe = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
)

// shellQuote 把任意文本变成 POSIX shell 里的一个单引号字面量。
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// headlessModelRe 是 --model 的值允许的字符；与预设校验（store.HeadlessRun）同一个范围，这里是运行前的第二道关。
var headlessModelRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/+-]{0,63}$`)

// headlessCmd 是拼非交互运行命令的全部输入。
type headlessCmd struct {
	Bin string
	// ChatID 是要接着的对话编号；New 为真（新开对话，#176）或 Continue 为真（最近会话，#191）时必须为空，命令里不带 --resume。
	ChatID string
	New    bool
	// Continue 为真时命令用 `--continue` 代替 `--resume <对话编号>`：由 Cursor CLI 接着该目录里最近动过的对话。
	// 与 New、ChatID 互斥。
	Continue bool
	Dir      string
	Posture  string
	Prompt   string
	Model    string // 可空：不带 --model
	// Windows 为真时按 Windows 主机拼 PowerShell 写法的命令（#178，见 headless_powershell.go）；零值（Linux / macOS）的拼法一个字节都没变。
	Windows bool
}

// build 拼出非交互运行的一整行命令。所有动态部分（对话编号、目录、模型、提示词）都经过校验与单引号包裹。
func (c headlessCmd) build() (string, error) {
	if c.Windows {
		return c.buildWindows()
	}
	switch {
	case !headlessBinRe.MatchString(c.Bin) || store.IsHeadlessPrefixCmd(c.Bin):
		return "", errors.New("可执行文件名不合法")
	case c.New && c.Continue:
		return "", errors.New("新开对话与接着最近的对话只能选一个")
	case (c.New || c.Continue) && c.ChatID != "":
		if c.Continue {
			return "", errors.New("接着最近的对话不能同时指定对话编号")
		}
		return "", errors.New("新开对话不能同时指定对话编号")
	case !c.New && !c.Continue && !headlessChatRe.MatchString(c.ChatID):
		return "", errors.New("对话编号不合法")
	case !strings.HasPrefix(c.Dir, "/") || strings.ContainsAny(c.Dir, "\x00\r\n"):
		return "", errors.New("工作目录必须是主机上的绝对路径")
	case c.Model != "" && !headlessModelRe.MatchString(c.Model):
		return "", errors.New("模型名不合法")
	case strings.TrimSpace(c.Prompt) == "" || strings.ContainsAny(c.Prompt, "\x00\r\n"):
		return "", errors.New("唤醒词为空或含换行")
	}
	parts := []string{c.Bin, "-p"}
	switch {
	case c.Continue:
		parts = append(parts, "--continue")
	case !c.New:
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

// headlessSubmit 是提交一次内部非交互运行的全部输入。
type headlessSubmit struct {
	Caller    string // 调用方名字：同时是结束时认领运行的标记
	UserAgent string
	Machine   string
	Workflow  string
	Cwd       string
	Command   string
	// Shell 是执行命令的 shell（termexec.ExecRequest.Shell）；空 = 由 Agent 按主机默认选。Windows 主机的命令是 PowerShell 写法，要写明 pwsh / powershell。
	Shell       string
	Description string
	Thread      string // 线程标签：预设运行用它把运行记录和预设对上
	// Conversation 是这次运行要续聊的对话编号。给了它（且有工作流），提交成功后就开始读这次运行的输出来判定对话的状态（见 run_watch.go）。
	Conversation string
	// NewConversation 表示这是新开对话的运行（命令里没有 --resume，Conversation 为空）：对话编号要从输出的 init 事件里学到，
	// 并记成这个工作流的对话编号（见 run_watch.go 的 learn）；没有工作流就没法记，也就不监视。
	NewConversation bool
	// AfterNew 是新开对话的运行之后的去向（store.HeadlessAfter*，#191）：只影响「新对话已建立」系统行的说法；空 = 记下编号。
	AfterNew string
	// ContinueConversation 表示这是最近会话的运行（命令里是 --continue，Conversation 为空，#191）：监视只看一眼它实际接上的对话，
	// 写一条系统行说明，不把它记成工作流的对话（见 run_watch.go 的 peek）。
	ContinueConversation bool
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
		Target: in.Machine, Command: in.Command, Shell: in.Shell, Description: in.Description, WorkflowID: in.Workflow, Thread: in.Thread,
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
		Learn: in.NewConversation, LearnAfter: in.AfterNew, Peek: in.ContinueConversation,
		Timeout: time.Duration(timeout) * time.Second,
	})
	return res, nil
}

// headlessToken 是内部身份用的随机串：只为通过「令牌强度」检查，不对应任何真实凭据，也从不记录。
func (s *ExecService) headlessToken() string {
	s.headlessTokOnce.Do(func() { s.headlessTok = randHex(24) })
	return s.headlessTok
}

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
		// 「新会话 · 成功后切到最近会话」的预设：这次运行成功了就把预设切成「最近会话」（#191）；同样放在结束行之后，自带 panic 保护。
		defer g.switchAfterNew(rec)
	}
	if wf == "" {
		return
	}
	var text, actor string
	switch rec.CredentialName {
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

// switchAfterNew 在「新会话 · 成功后切到最近会话」（after_new=continue）的预设运行成功之后，把预设的对话来源改成「最近会话」（#559，#191）。
//
// 只在这些条件下切：运行成功（失败的运行不切，下次仍是新会话）；这次运行确实是新开对话的运行（命令里既没有 --resume 也没有 --continue——
// 运行期间预设被改过的话，旧配置发起的运行不能触发新配置的切换）；预设此刻仍是 new + continue（比较并交换，见 store.SwitchLauncherToContinue：
// 手动改过的永远不被覆盖）。切完写一条聊天系统行与一条审计。自带 panic 保护——它是旁路，不能影响运行结果的记录。
func (g *guardService) switchAfterNew(rec model.TerminalRun) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[terminal] 新会话成功后切换预设 panic：%v", r)
		}
	}()
	if rec.State != model.RunSucceeded || !strings.HasPrefix(rec.Thread, launcherThreadPrefix) {
		return
	}
	if convFromRunCommand(rec.Command) != "" || isContinueCommand(rec.Command) {
		return
	}
	presetID := strings.TrimPrefix(rec.Thread, launcherThreadPrefix)
	ctx := g.h.ctx
	p, switched, err := g.h.store.SwitchLauncherToContinue(ctx, presetID)
	if err != nil {
		log.Printf("[terminal] 预设 %s 在新会话成功后切换成最近会话失败（下次仍按新会话运行）：%v", presetID, err)
		return
	}
	if !switched {
		return
	}
	g.h.audit.record(ctx, auditEvent{
		Event: EvLauncherSwitch, Actor: "system", ActorKind: ActorSystem, MachineCode: p.Host, RunID: rec.RunID,
		Detail: "preset=" + p.ID + " new->continue",
	})
	if wf := strings.TrimSpace(rec.WorkflowID); wf != "" {
		meta, _ := json.Marshal(map[string]any{"mode": "launcher_switch", "preset": p.ID, "run_id": rec.RunID})
		g.addWakeLine(ctx, wf, "system", launcherLearnActor,
			fmt.Sprintf("预设「%s」的新会话运行成功了：预设已自动切换成「最近会话」，之后的运行用 --continue 接着这个目录里最近的对话", p.Name),
			string(meta), g.h.now())
	}
}
