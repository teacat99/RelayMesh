package terminal

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
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

// 会话页「运行」（#128）：不先建预设，直接对这个会话做一次「非交互续聊」。
//
// #140 起 Web 不再用这组接口：会话页底部的「运行」按钮改为运行与会话对应的终端预设（session_launchers.go），
// 这里的接口保留给脚本与旧前端；会话与主机的解析（resolveSessionRunHost、sessionRunInspect）仍被预设视图共用。
//
// 它与预设运行（launcher_run.go，§8.7）是同一条通道、同一套闸门（总闸、暂停、主机「模型访问」、主机执行模式、令牌限额；
// 「需批准」模式下运行会停在待批准）、同一个命令拼装器，差别只在「目标从哪来」：
//   - 对话编号：这个工作流里 hook 汇报的对话（可手动指定）；
//   - 目录：会话身份里的项目目录（可改）；
//   - 主机：会话上报的主机名（host_name）对上主机清单里主机的名字（与 terminal_exec 同口径：机器码 → 备注 / 显示名 → 自报主机名别名）；对不上或有多台时由调用方在请求里指定 host；
//   - 提示词、模型、权限姿态、可执行文件：请求里可调，字段规则与预设共用（store.NormalizeHeadlessRun）。
//
// 边界与预设一样：
//   - 只在人点了「运行」时发生（需要刚刚解锁）；预览是只读的，不产生运行记录，也不需要解锁；
//   - 命令只由已校验的字段拼出，所有动态部分都单引号包裹；
//   - 同一个工作流同一时刻只跑一次；对话还在运行（工作中 / 等你回答 / 等批准）时不让再续聊它；
//   - 审计只记工作流与权限姿态，不含提示词与命令；聊天里的系统行同样不含。

const (
	// sessionRunCaller 是会话页运行在运行记录里的「调用方」名字；它同时是结束时认领运行的标记。
	sessionRunCaller = "会话页运行（非交互续聊）"
	// sessionThreadPrefix 是运行记录线程标签的前缀。线程标签最长 64 个字，而工作流编号可以更长，
	// 所以标签里放的是工作流编号的短哈希：同一个工作流永远得到同一个标签。
	sessionThreadPrefix = "session-run-"
)

// sessionThreadFor 返回某个工作流的会话页运行所用的线程标签。
func sessionThreadFor(workflowID string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(workflowID)))
	return sessionThreadPrefix + hex.EncodeToString(sum[:6])
}

// sessionRunRequest 是预览与运行共用的请求：除工作流编号外都可以不填（不填就按会话里已知的来）。
type sessionRunRequest struct {
	WorkflowID string `json:"workflow_id"`
	// ConversationID 空 = 这个工作流里最近一个有对话编号的会话。
	ConversationID string `json:"conversation_id"`
	// Host 是机器码；空 = 按会话上报的主机名匹配。
	Host string `json:"host"`
	// Dir 空 = 会话身份里的项目目录。
	Dir string `json:"dir"`
	// Bin / Model / Posture / Prompt 同预设的运行选项；空 = 默认值（agent / 不带 --model / allowlist / 默认提示词）。
	Bin     string `json:"bin"`
	Model   string `json:"model"`
	Posture string `json:"posture"`
	Prompt  string `json:"prompt"`
}

// SessionRunHost 是面板里「在哪台主机上运行」的一个候选。
type SessionRunHost struct {
	MachineCode string `json:"machine_code"`
	Hostname    string `json:"hostname"`
	Remark      string `json:"remark,omitempty"`
	OS          string `json:"os,omitempty"`
	Online      bool   `json:"online"`
	// Matched 表示这台主机的名字（备注，没有备注时是主机名；主机自报的主机名作别名）与会话上报的一致，口径同 terminal_exec 的 matchHosts。
	Matched bool `json:"matched"`
}

// SessionRunIssue 是此刻点「运行」会被拒绝的一个原因（预览里列出，真正点运行时服务端再判断一次，口径相同）。
type SessionRunIssue struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// SessionRunPreview 是面板打开、或改动选项时的只读预览：会在哪台主机、哪个目录、接着哪个对话运行，拼出来的命令是什么，
// 现在能不能点「运行」、不能的原因。
type SessionRunPreview struct {
	WorkflowID     string `json:"workflow_id"`
	ConversationID string `json:"conversation_id,omitempty"`
	// ClientKind 是这个对话来自哪个客户端（cli / ide）；IDE 里的对话 `agent --resume` 多半找不到。
	ClientKind string `json:"client_kind,omitempty"`
	// SessionHost 是会话上报的主机名。
	SessionHost string `json:"session_host,omitempty"`
	// Host 是选中的机器码（没选中是空）。
	Host  string           `json:"host,omitempty"`
	Hosts []SessionRunHost `json:"hosts"`
	Dir   string           `json:"dir,omitempty"`
	Bin   string           `json:"bin"`
	Model string           `json:"model,omitempty"`
	// Posture 是权限姿态：allowlist | force。
	Posture string `json:"posture"`
	// Prompt 是实际交给 agent 的提示词（%workflow 已替换）；DefaultPrompt 是不改动时用的默认提示词。
	Prompt        string `json:"prompt"`
	DefaultPrompt string `json:"default_prompt"`
	// Command 是拼出的整行命令（只读预览；缺对话编号或目录时为空）。
	Command  string            `json:"command,omitempty"`
	Issues   []SessionRunIssue `json:"issues,omitempty"`
	Warnings []string          `json:"warnings,omitempty"`
	CanRun   bool              `json:"can_run"`
	// LastRun 是这个工作流最近一次会话页运行的现状（没有运行过是 null）。
	LastRun *LauncherRunView `json:"last_run"`
}

// sessionRunInspect 是对一次请求的完整检查结果：预览直接展示它，运行在没有问题时才继续提交。
type sessionRunInspect struct {
	req      sessionRunRequest
	run      store.HeadlessRun // 规整后的运行配置（工作流与对话编号已填好）
	conv     string
	cur      *store.AgentSessionView
	host     *model.TerminalHost
	hosts    []SessionRunHost
	dir      string
	prompt   string
	command  string
	warnings []string
	errs     []*wakeError
	// windows / shell：选中的主机是 Windows 时命令是 PowerShell 写法（#178），shell 是 Agent 要用的 pwsh / powershell；其他主机为空。
	windows bool
	shell   string

	// sessionHost 是会话上报的主机名（占位值与没上报都是空）。工作流还没有智能体状态会话时（hook 还没汇报过，
	// 或汇报被记到了别的工作流名下），取工作流上记录的主机——见 inspectSessionRun 的目录与主机兜底（#182）。
	sessionHost string
}

func (in *sessionRunInspect) add(status int, code, msg string) {
	in.errs = append(in.errs, newWakeError(status, code, msg))
}

// inspectSessionRun 检查一次会话页运行：解析目标、校验选项、拼命令、套上所有前置条件。任何一项不满足都记进 errs，
// 但会尽量继续往下解析，好让预览把能确定的部分都显示出来。它不产生任何副作用。
func (g *guardService) inspectSessionRun(ctx context.Context, req sessionRunRequest, now time.Time) *sessionRunInspect {
	in := &sessionRunInspect{}
	req.WorkflowID = strings.TrimSpace(req.WorkflowID)
	req.ConversationID = strings.TrimSpace(req.ConversationID)
	req.Host = strings.TrimSpace(req.Host)
	req.Dir = strings.TrimSpace(req.Dir)
	in.req = req
	if req.WorkflowID == "" {
		in.add(http.StatusBadRequest, "no_workflow", "缺少工作流编号")
		return in
	}
	if n, err := termproto.NormalizeMachineCode(req.Host); err == nil && req.Host != "" {
		in.req.Host = n
	}

	run, verr := store.NormalizeHeadlessRun(store.HeadlessRun{
		Bin: req.Bin, Model: req.Model, Posture: req.Posture, WorkflowID: req.WorkflowID,
		ConversationID: req.ConversationID, Prompt: req.Prompt,
	})
	in.run = run
	if verr != nil {
		// 选项本身不合法：命令拼不出来，但主机与目录还是能解析，预览照样给。
		in.add(http.StatusBadRequest, "bad_options", verr.Error())
	}

	// 对话与会话。
	conv, cur, warnings, cerr := g.launcherConversation(ctx, run, now)
	in.conv, in.cur, in.warnings = conv, cur, append(in.warnings, warnings...)
	if cerr != nil {
		if cerr.Code == "no_conversation" {
			cerr = newWakeError(cerr.Status, cerr.Code,
				"还不知道要接着哪个对话（hook 没汇报过这个工作流的对话编号）：请求里带上 conversation_id，或先让智能体在这个工作流里跑一次")
		}
		in.errs = append(in.errs, cerr)
	}

	// 目录：没指定就用会话身份里的项目目录。
	in.dir = req.Dir
	in.sessionHost = reportedHostName(cur)
	if in.dir == "" && cur != nil {
		in.dir = strings.TrimSpace(cur.ProjectDir)
	}
	if cur == nil {
		// 这个工作流还没有智能体状态会话（hook 还没汇报过，或汇报被记到了别的工作流名下）：目录与主机用工作流上记录的——
		// 智能体在反馈会话里自报的最近一个（状态核心给汇报找身份时用的也是这一份），而不是干脆说「不知道」（#182）。
		bindHost, bindDir := g.h.store.WorkflowBinding(ctx, req.WorkflowID)
		if in.dir == "" {
			in.dir = bindDir
		}
		if strings.EqualFold(bindHost, placeholderHostName) {
			bindHost = ""
		}
		if sameDir(bindDir, in.dir) {
			in.sessionHost = bindHost // 主机与目录是同一份记录里的一对：请求自己指定了别的目录，就不采用这条记录的主机
		}
	}
	switch {
	case in.dir == "":
		in.add(http.StatusConflict, "no_dir", "不知道要在主机的哪个目录运行（会话没有上报项目目录）：请求里带上 dir（主机上的绝对路径）")
	case !headlessDirShapeOK(in.dir):
		in.add(http.StatusBadRequest, "bad_dir", "目录必须是主机上的绝对路径")
	}

	// 主机。
	g.resolveSessionRunHost(ctx, in)
	// 目录的写法要和主机系统对得上（Windows 主机要盘符 / UNC 路径，其余主机要 / 开头）；上面只能先查「是不是某种绝对路径」。
	if in.host != nil && in.dir != "" && headlessDirShapeOK(in.dir) {
		if derr := headlessDirOK(in.dir, in.windows); derr != nil {
			in.add(http.StatusBadRequest, "bad_dir", derr.Error())
		}
	}

	// 同一个工作流上一次会话页运行还没结束。它排在所有原因的最前面：上一次运行正在跑时，
	// 对话多半也显示「工作中」，但「上一次运行还没结束」才是更准确、也更有用的说法。
	if busy, id := g.sessionRunActive(ctx, req.WorkflowID, now); busy {
		in.errs = append([]*wakeError{newWakeError(http.StatusConflict, "run_active",
			fmt.Sprintf("这个工作流上一次运行还没结束（运行编号 %s），等它结束再运行；到 ExecMesh 里可以取消它", id))}, in.errs...)
	}

	// 提示词与命令。选项不合法时不拼命令（规整后的字段可能是空的）。
	in.prompt = renderLauncherWake(ctx, g.h.store, launcherWake{
		Kind: sessionprompt.KindResume, Custom: run.Prompt, WorkflowID: req.WorkflowID, Project: in.dir, NoLimitNote: true,
	}).Text
	if verr == nil && in.conv != "" && headlessDirOK(in.dir, in.windows) == nil {
		cmd, berr := headlessCmd{
			Bin: run.EffectiveBin(), ChatID: in.conv, Dir: in.dir, Posture: run.EffectivePosture(),
			Prompt: in.prompt, Model: run.Model, Windows: in.windows,
		}.build()
		if berr != nil {
			in.add(http.StatusConflict, "headless_command", berr.Error())
		} else {
			in.command = cmd
			if in.windows {
				if _, n := winNeutralize(in.prompt); n > 0 {
					in.warnings = append(in.warnings, winPromptNote(n))
				}
			}
		}
	}

	// 提示（不挡运行）。
	if run.EffectivePosture() == store.HeadlessPostureAllowlist {
		in.warnings = append(in.warnings, "没有带 --force：智能体只能做 CLI 配置里 permissions.allow 放行的事。要让它读写 RelayMesh，请在那里加上 Mcp(<服务器名>:*)；或把权限姿态改成「放开全部命令」")
	}
	if cur != nil && cur.ClientKind == "ide" {
		in.warnings = append(in.warnings, "这个对话来自编辑器（IDE）：非交互续聊只能接着命令行（CLI）里的对话，多半找不到它")
	}
	if in.host != nil && in.host.ExecMode == model.TerminalModeApprove {
		in.warnings = append(in.warnings, "这台主机的执行模式是「需批准」：运行会先停在待批准，要到 ExecMesh 里批准之后才会开始")
	}
	return in
}

// placeholderHostName 是 store 在客户端没有上报主机名时填的占位值（store/sessions.go 的 P5 兜底）：它不是任何一台主机的名字。
const placeholderHostName = "hosts"

// reportedHostName 取会话上报的主机名；没有会话、或是占位值（客户端其实没报）都按「没上报」处理，返回空。
func reportedHostName(cur *store.AgentSessionView) string {
	if cur == nil {
		return ""
	}
	n := strings.TrimSpace(cur.HostName)
	if strings.EqualFold(n, placeholderHostName) {
		return ""
	}
	return n
}

// resolveSessionRunHost 选出运行的主机，并整理出面板里的候选清单（已启用的主机；名字与会话上报的主机一致的排在前面，在线的优先）。
//
// 「名字一致」与 terminal_exec 解析 target 用同一个口径（matchHosts）：机器码 → 显示名（备注，没有备注时是主机名）→ 主机名别名。
// 会话上报的主机名（编辑器 hook 里的 hostname）常常是短名（devhost），而 ExecMesh 主机自报的是全名（devhost.example.com），
// 管理员又习惯给主机写一个短备注——只比自报主机名会把这样的主机判成「清单里没有」（#139）。仍然不猜：短名不会去前缀匹配全名。
func (g *guardService) resolveSessionRunHost(ctx context.Context, in *sessionRunInspect) {
	hosts, err := g.h.store.ListTerminalHosts(ctx)
	if err != nil {
		log.Printf("[terminal] 会话页运行读取主机清单失败：%v", err)
	}
	sessionHost := in.sessionHost
	enabled := make([]model.TerminalHost, 0, len(hosts))
	for _, h := range hosts {
		if h.Status == termproto.HostStatusEnabled {
			enabled = append(enabled, h)
		}
	}
	matchedCode := map[string]bool{}
	if sessionHost != "" {
		for _, i := range matchHosts(enabled, sessionHost) {
			matchedCode[enabled[i].MachineCode] = true
		}
	}
	byCode := map[string]*model.TerminalHost{}
	for i := range enabled {
		h := &enabled[i]
		byCode[h.MachineCode] = h
		a := g.h.onlineAgent(h.MachineCode)
		in.hosts = append(in.hosts, SessionRunHost{
			MachineCode: h.MachineCode, Hostname: h.Hostname, Remark: h.Remark, OS: h.OS,
			Online:  a != nil && a.isEnabled(),
			Matched: matchedCode[h.MachineCode],
		})
	}
	sort.SliceStable(in.hosts, func(i, j int) bool {
		a, b := in.hosts[i], in.hosts[j]
		if a.Matched != b.Matched {
			return a.Matched
		}
		if a.Online != b.Online {
			return a.Online
		}
		return a.MachineCode < b.MachineCode
	})

	var pick *model.TerminalHost
	if in.req.Host != "" {
		if h, ok := byCode[in.req.Host]; ok {
			pick = h
		} else {
			in.add(http.StatusNotFound, "host_not_found", "选中的主机不存在、没有启用或已被删除")
			return
		}
	} else {
		var matched, online []string
		for _, c := range in.hosts {
			if !c.Matched {
				continue
			}
			matched = append(matched, c.MachineCode)
			if c.Online {
				online = append(online, c.MachineCode)
			}
		}
		switch {
		case len(matched) == 1:
			pick = byCode[matched[0]]
		case len(matched) > 1 && len(online) == 1:
			pick = byCode[online[0]]
		case len(matched) > 1:
			in.add(http.StatusConflict, "host_ambiguous", fmt.Sprintf("有 %d 台主机的名字都是 %s：请选择要在哪一台上运行", len(matched), sessionHost))
			return
		case sessionHost == "":
			in.add(http.StatusConflict, "host_unknown", "会话没有上报主机名：请选择要在哪台主机上运行")
			return
		default:
			in.add(http.StatusConflict, "host_not_found",
				fmt.Sprintf("主机清单里没有备注或主机名为 %s 的已启用主机：先把那台机器接入终端，或给已接入的主机写一个同名备注", sessionHost))
			return
		}
	}
	in.host = pick
	in.windows = isWindowsOS(pick.OS)
	a := g.h.onlineAgent(pick.MachineCode)
	if a == nil || !a.isEnabled() {
		in.add(http.StatusConflict, "host_offline", "主机离线，现在没法运行")
		a = nil
	}
	if in.windows {
		// Windows 主机：命令是 PowerShell 写法（#178），要先确认主机上有 PowerShell；离线时没法问，预览按 powershell 算。
		sh, serr := windowsShell(a)
		if serr != nil {
			in.add(http.StatusConflict, "no_powershell", serr.Error())
		}
		in.shell = sh
	}
}

// sessionRunActive 判断这个工作流是不是还有一次没结束的会话页运行；返回它的运行编号。
func (g *guardService) sessionRunActive(ctx context.Context, workflowID string, now time.Time) (bool, string) {
	rec := g.latestSessionRun(ctx, workflowID)
	if rec == nil {
		return false, ""
	}
	return runActive(*rec, now), rec.RunID
}

// latestSessionRun 取这个工作流最近一次会话页运行记录；从没运行过返回 nil。
func (g *guardService) latestSessionRun(ctx context.Context, workflowID string) *model.TerminalRun {
	wf := strings.TrimSpace(workflowID)
	rec := g.latestRunBy(ctx, sessionRunCaller, sessionThreadFor(wf))
	if rec == nil || strings.TrimSpace(rec.WorkflowID) != wf { // 标签是哈希：再对一次工作流编号，排除极小概率的碰撞
		return nil
	}
	return rec
}

// sessionRunView 返回这个工作流最近一次会话页运行的现状；从没运行过返回 nil。
func (g *guardService) sessionRunView(ctx context.Context, workflowID string, now time.Time) *LauncherRunView {
	rec := g.latestSessionRun(ctx, workflowID)
	if rec == nil {
		return nil
	}
	v := g.runViewOf(ctx, *rec, now)
	return &v
}

// sessionRunPreview 把检查结果整理成给面板的只读预览。
func (g *guardService) sessionRunPreview(ctx context.Context, req sessionRunRequest, now time.Time) SessionRunPreview {
	in := g.inspectSessionRun(ctx, req, now)
	pv := SessionRunPreview{
		WorkflowID: in.req.WorkflowID, ConversationID: in.conv, Host: "", Hosts: in.hosts, Dir: in.dir,
		Bin: in.run.EffectiveBin(), Model: in.run.Model, Posture: in.run.EffectivePosture(),
		Prompt: in.prompt, DefaultPrompt: renderLauncherWake(ctx, g.h.store, launcherWake{
			Kind: sessionprompt.KindResume, WorkflowID: in.req.WorkflowID, Project: in.dir, NoLimitNote: true,
		}).Text,
		Command: in.command, Warnings: in.warnings, CanRun: len(in.errs) == 0,
	}
	if pv.Hosts == nil {
		pv.Hosts = []SessionRunHost{}
	}
	if in.host != nil {
		pv.Host = in.host.MachineCode
	}
	pv.SessionHost = in.sessionHost // 没有状态会话时是工作流上记录的主机（见 inspectSessionRun）
	if in.cur != nil {
		pv.ClientKind = in.cur.ClientKind
	}
	for _, e := range in.errs {
		pv.Issues = append(pv.Issues, SessionRunIssue{Code: e.Code, Message: e.Message})
	}
	if in.req.WorkflowID != "" {
		pv.LastRun = g.sessionRunView(ctx, in.req.WorkflowID, now)
	}
	return pv
}

// acquireSessionRun 占住这个工作流的「正在提交」位置：同一时刻只允许一个提交，防止连点或两个窗口同时点造成两次运行。
func (g *guardService) acquireSessionRun(workflowID string) bool {
	g.sessionRunMu.Lock()
	defer g.sessionRunMu.Unlock()
	if g.sessionRunning == nil {
		g.sessionRunning = map[string]bool{}
	}
	if g.sessionRunning[workflowID] {
		return false
	}
	g.sessionRunning[workflowID] = true
	return true
}

func (g *guardService) releaseSessionRun(workflowID string) {
	g.sessionRunMu.Lock()
	defer g.sessionRunMu.Unlock()
	delete(g.sessionRunning, workflowID)
}

// acquireLauncherRun 占住这个预设的「正在提交」位置（#176）：同一时刻只允许一个提交，防止连点、两个窗口同时点、
// 或自动续跑与手动运行撞在一起造成两次运行；占不到返回 false。
func (g *guardService) acquireLauncherRun(presetID string) bool {
	g.sessionRunMu.Lock()
	defer g.sessionRunMu.Unlock()
	if g.launcherRunning == nil {
		g.launcherRunning = map[string]bool{}
	}
	if g.launcherRunning[presetID] {
		return false
	}
	g.launcherRunning[presetID] = true
	return true
}

func (g *guardService) releaseLauncherRun(presetID string) {
	g.sessionRunMu.Lock()
	defer g.sessionRunMu.Unlock()
	delete(g.launcherRunning, presetID)
}

// runSession 提交一次会话页运行：先做全部前置检查（任何一项不满足都不会产生运行记录），再经执行通道提交。
func (g *guardService) runSession(ctx context.Context, req sessionRunRequest, now time.Time) (LauncherRunPlan, *model.TerminalHost, error) {
	wf := strings.TrimSpace(req.WorkflowID)
	if wf != "" {
		if !g.acquireSessionRun(wf) {
			return LauncherRunPlan{}, nil, newWakeError(http.StatusConflict, "run_active", "这个工作流正在提交一次运行，请稍等")
		}
		defer g.releaseSessionRun(wf)
	}
	in := g.inspectSessionRun(ctx, req, now)
	if len(in.errs) > 0 {
		return LauncherRunPlan{}, nil, in.errs[0]
	}
	res, serr := g.h.exec.submitHeadless(ctx, headlessSubmit{
		Caller: sessionRunCaller, UserAgent: "relaymesh-session-run",
		Machine: in.host.MachineCode, Workflow: wf, Conversation: in.conv, Cwd: in.dir, Command: in.command, Shell: in.shell,
		Description: fmt.Sprintf("会话页运行：接着对话 %s 的非交互续聊", shortConv(in.conv)), Thread: sessionThreadFor(wf),
	})
	if serr != nil {
		var te *termexec.Error
		if errors.As(serr, &te) {
			return LauncherRunPlan{}, nil, newWakeError(http.StatusConflict, te.Code, te.Message)
		}
		return LauncherRunPlan{}, nil, serr
	}
	plan := LauncherRunPlan{
		Name: "会话页运行", Host: in.host.MachineCode, RunID: res.RunID, RunState: res.State,
		Posture: in.run.EffectivePosture(), Model: in.run.Model, ConversationID: in.conv, Warnings: in.warnings,
	}
	if res.State == model.RunAwaitingApproval {
		// 预览阶段已经提示过「需批准」；这里按真实状态再确认一次（执行模式可能刚被改过）。
		if !containsApproveNote(plan.Warnings) {
			plan.Warnings = append(plan.Warnings, "这台主机的执行模式是「需批准」：运行已提交，要在 ExecMesh 里批准之后才会开始")
		}
	}
	meta, _ := json.Marshal(map[string]any{"mode": "session_run", "run_id": res.RunID})
	g.addWakeLine(ctx, wf, "human", "web", sessionRunLineText(plan), string(meta), now)
	return plan, in.host, nil
}

func containsApproveNote(ws []string) bool {
	for _, w := range ws {
		if strings.Contains(w, "需批准") {
			return true
		}
	}
	return false
}

// sessionRunLineText 是提交运行时聊天里的系统行：谁、接着哪个对话、什么模型与权限。不含提示词，也不含命令。
func sessionRunLineText(plan LauncherRunPlan) string {
	posture := "只放行白名单内的操作"
	if plan.Posture == store.HeadlessPostureForce {
		posture = "放开全部命令（--force）"
	}
	modelName := plan.Model
	if modelName == "" {
		modelName = "默认模型"
	}
	return fmt.Sprintf("你在会话页运行了非交互续聊（接着对话 %s · %s · %s）", shortConv(plan.ConversationID), modelName, posture)
}

// ---------- REST ----------

// handleSessionRunPreview 是只读预览：不产生运行记录，不需要解锁。
func (h *Hub) handleSessionRunPreview(c *gin.Context) {
	var req sessionRunRequest
	if !bindJSON(c, &req) {
		return
	}
	c.JSON(http.StatusOK, gin.H{"preview": h.guardian.sessionRunPreview(c.Request.Context(), req, h.now())})
}

// handleSessionRunStatus 返回这个工作流最近一次会话页运行的现状（从没运行过是 null）；面板在运行进行中时轮询它。
func (h *Hub) handleSessionRunStatus(c *gin.Context) {
	wf := strings.TrimSpace(c.Query("workflow_id"))
	if wf == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "缺少工作流编号", "code": "no_workflow"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"run": h.guardian.sessionRunView(c.Request.Context(), wf, h.now())})
}

// handleSessionRun 提交一次会话页运行（需要刚刚解锁：它会让主机运行命令）。
func (h *Hub) handleSessionRun(c *gin.Context) {
	var req sessionRunRequest
	if !bindJSON(c, &req) {
		return
	}
	ctx := c.Request.Context()
	plan, host, err := h.guardian.runSession(ctx, req, h.now())
	if err != nil {
		var we *wakeError
		if errors.As(err, &we) {
			c.JSON(we.Status, gin.H{"error": we.Message, "code": we.Code})
			return
		}
		log.Printf("[terminal] 提交会话页运行失败: %v", err)
		h.fail(c, err)
		return
	}
	h.audit.record(ctx, auditEvent{
		Event: EvSessionRun, Actor: subjectOf(c), IP: c.ClientIP(), MachineCode: host.MachineCode, RunID: plan.RunID,
		Detail: fmt.Sprintf("workflow=%s posture=%s", strings.TrimSpace(req.WorkflowID), plan.Posture),
	})
	c.JSON(http.StatusOK, gin.H{"run": plan})
}
