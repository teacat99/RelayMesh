package terminal

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"net/http"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gin-gonic/gin"

	"github.com/teacat99/RelayMesh/v2/internal/model"
	"github.com/teacat99/RelayMesh/v2/internal/sessionprompt"
	"github.com/teacat99/RelayMesh/v2/internal/store"
	"github.com/teacat99/RelayMesh/v2/internal/termproto"
)

// 会话页「启用 Agent」（#176 第三期）。
//
// 网页上新建的会话在智能体接入之前是「待接入」：以前只能把「启动提示词」复制给某个智能体，由它自己第一次调用时接进来。
// 现在可以直接点「启用 Agent」：在某台主机的某个目录里无人值守地新开一个 Cursor CLI 对话（agent -p …），
// 它按提示词带着这个工作流编号接入，之后这个会话就和别的智能体会话一样了。
//
// 实现是「建（或复用）一条非交互续聊预设 + 运行它」：预设打开 new_conversation、来源标记 enable_agent，
// 所以闸门（总闸、暂停、主机模型访问、执行模式、令牌限额）、单飞、对话编号学习、自动续跑、ExecMesh 里的记录与审计
// 全部沿用预设运行那一套（launcher_run.go），这里只做三件事：
//  1. 判断这个工作流现在能不能被「启用」：只有「待接入」的（会话行全是网页新建的占位）才行；
//  2. 把弹窗里的选项变成一条预设：每个工作流只有一条（编号由工作流编号派生），再次启用复用它，所以不会越点越多；
//  3. 运行失败时撤掉刚新建的预设，不留半成品。
//
// 边界：只在人点了「启用并运行」时发生（需要刚刚解锁，和新建、修改预设、运行预设一样）；智能体不能经 MCP 启用别的智能体；
// Linux / macOS / Windows 主机都支持（Windows 上命令是 PowerShell 写法，见 headless_powershell.go，#178）；
// 审计只记工作流、预设、权限姿态，不含提示词与命令。

const (
	// enablePresetPrefix 是「启用 Agent」预设编号的前缀；后面是工作流编号哈希的前 16 位十六进制，合起来不超过预设编号的 40 位上限。
	enablePresetPrefix = "ea-"
	// enableTitleRunes 是预设名里嵌入会话名的最大字符数：名字最长 40 个字，前缀与工作流尾巴要留位置。
	enableTitleRunes = 16
	// enableTailRunes 是预设名末尾的工作流编号尾巴长度：让同名会话的预设也不重名。
	enableTailRunes = 6
)

// sessionEnableRequest 是弹窗里填的选项。字段含义与非交互续聊预设（store.HeadlessRun）一致；
// 没填的用预设的默认（权限姿态没填 = 白名单，最长运行时间没填 = 平台上限）——「默认放开全部命令、默认 24 小时」是弹窗预先填好的值，
// 不是接口的默认，接口不会替调用方放宽权限。
type sessionEnableRequest struct {
	WorkflowID string `json:"workflow_id"`
	Host       string `json:"host"`
	Dir        string `json:"dir"`
	Bin        string `json:"bin"`
	Model      string `json:"model"`
	Posture    string `json:"posture"`
	// ConversationID 可选：给了就接着这个已有的 Cursor 对话（把它接到这个会话上），不给就新开对话。
	ConversationID string `json:"conversation_id"`
	Prompt         string `json:"prompt"`
	MaxRunMinutes  int    `json:"max_run_minutes"`
	AutoResume     bool   `json:"auto_resume"`
}

// SessionEnablePreset 是这个工作流已有的「启用 Agent」预设（再次启用会复用它）。
type SessionEnablePreset struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// SessionEnablePreview 是弹窗里的只读预览：不产生任何记录，也不需要解锁。
type SessionEnablePreview struct {
	WorkflowID string `json:"workflow_id"`
	Title      string `json:"title,omitempty"`
	// Pending 表示这个工作流还是「待接入」（会话行全是网页新建的占位）：只有这样的才能被启用。
	Pending bool `json:"pending"`
	// BoundDir 是网页新建这个会话时绑定的项目目录（#184；没绑定时为空）。绑定后智能体第一次汇报带的 project_directory
	// 必须与它一致（D-161），所以没填运行目录时默认用它，填了不同的目录会报 dir_mismatch。
	BoundDir string `json:"bound_dir,omitempty"`
	// Host 是要运行的主机（机器码）；没选、且没法自动选定时是空串，候选在 Hosts 里。
	Host  string           `json:"host"`
	Hosts []SessionRunHost `json:"hosts"`
	Dir   string           `json:"dir"`
	Bin   string           `json:"bin"`
	Model string           `json:"model"`
	// Posture 是生效的权限姿态（allowlist / force）。
	Posture string `json:"posture"`
	// NewConversation 表示这次会新开对话（命令里没有 --resume）；ConversationID 是续聊时要接着的对话，新开时为空。
	NewConversation bool   `json:"new_conversation"`
	ConversationID  string `json:"conversation_id"`
	// Prompt 是实际交给智能体的提示词（%workflow 已替换）；DefaultPrompt 是没填自定义提示词时的默认正文（%workflow 原样保留）。
	Prompt        string `json:"prompt"`
	DefaultPrompt string `json:"default_prompt"`
	// Command 是将要在主机上运行的完整命令（只读展示，方便核对）；拼不出来时为空。
	Command       string            `json:"command"`
	MaxRunMinutes int               `json:"max_run_minutes"`
	AutoResume    bool              `json:"auto_resume"`
	Warnings      []string          `json:"warnings"`
	Issues        []SessionRunIssue `json:"issues"`
	CanRun        bool              `json:"can_run"`
	// Preset / LastRun 在这个工作流之前启用过时有：预设（再次启用复用它）与它最近一次运行的现状。
	Preset  *SessionEnablePreset `json:"preset,omitempty"`
	LastRun *LauncherRunView     `json:"last_run,omitempty"`
}

// enablePresetID 由工作流编号派生「启用 Agent」预设的编号：同一个工作流永远是同一个编号，再次启用复用同一条预设。
func enablePresetID(workflowID string) string {
	sum := sha1.Sum([]byte(strings.TrimSpace(workflowID)))
	return enablePresetPrefix + hex.EncodeToString(sum[:8])
}

// enablePresetName 给预设起名：「启用 Agent · <会话名（截短）> · <工作流尾巴>」。名字要在全部预设里唯一，所以带上工作流编号的尾巴；
// 会话名拿不到（或含预设名不允许的字符）时只用尾巴。
func enablePresetName(title, workflowID string, withTitle bool) string {
	wf := []rune(strings.TrimSpace(workflowID))
	tail := string(wf)
	if len(wf) > enableTailRunes {
		tail = string(wf[len(wf)-enableTailRunes:])
	}
	t := strings.Join(strings.Fields(title), " ")
	if r := []rune(t); len(r) > enableTitleRunes {
		t = string(r[:enableTitleRunes-1]) + "…"
	}
	if !withTitle || t == "" {
		return "启用 Agent · " + tail
	}
	return "启用 Agent · " + t + " · " + tail
}

// sessionEnableInspect 是对「启用 Agent」此刻能不能运行的完整检查结果；预览与提交共用，口径永远一致。
type sessionEnableInspect struct {
	req      sessionEnableRequest
	title    string
	pending  bool
	boundDir string // 网页新建会话时绑定的项目目录（规整后；没绑定为空）
	hosts    []SessionRunHost
	preset   store.TerminalLauncher // 将要保存并运行的预设（选项不合法时只有部分字段）
	run      store.HeadlessRun
	in       *launcherInspect // 选项合法、主机与目录都有时才有
	errs     []*wakeError
}

func (e *sessionEnableInspect) add(status int, code, msg string) {
	e.errs = append(e.errs, newWakeError(status, code, msg))
}

// normalizeEnableRequest 去掉首尾空白、把主机码规整成规范形式（格式不对的原样留着，交给后面报错）。
func normalizeEnableRequest(req sessionEnableRequest) sessionEnableRequest {
	req.WorkflowID = strings.TrimSpace(req.WorkflowID)
	req.Host = strings.TrimSpace(req.Host)
	req.Dir = strings.TrimSpace(req.Dir)
	req.ConversationID = strings.TrimSpace(req.ConversationID)
	if n, err := termproto.NormalizeMachineCode(req.Host); err == nil && req.Host != "" {
		req.Host = n
	}
	return req
}

// inspectSessionEnable 解析目标、校验选项、拼出预设并套上所有前置条件。任何一项不满足都记进 errs，
// 但会尽量继续往下解析，好让预览把能确定的部分都显示出来。它不产生任何副作用。
func (g *guardService) inspectSessionEnable(ctx context.Context, req sessionEnableRequest, now time.Time) *sessionEnableInspect {
	e := &sessionEnableInspect{}
	req = normalizeEnableRequest(req)
	e.req = req
	if req.WorkflowID == "" {
		e.add(http.StatusBadRequest, "no_workflow", "缺少工作流编号")
		return e
	}

	// 工作流：必须存在，且还是「待接入」（会话行全是网页新建的占位）。
	sessions, err := g.h.store.GetSessionsByWorkflow(ctx, req.WorkflowID, 50)
	if err != nil {
		log.Printf("[terminal] 启用 Agent 读取工作流会话失败：%v", err)
	}
	switch {
	case len(sessions) == 0:
		e.add(http.StatusNotFound, "workflow_not_found", "没有找到这个工作流")
	default:
		e.pending = true
		for _, s := range sessions {
			if !store.IsWebPlaceholderSessionID(s.ID) {
				e.pending = false
				break
			}
		}
		e.title = strings.TrimSpace(sessions[0].Title)
		if !e.pending {
			e.add(http.StatusConflict, "not_pending", "这个工作流已经有智能体接入过了：请用会话页的「运行」接着它，不需要再启用")
		}
	}

	// 绑定目录（#184）：网页新建会话时可以绑定项目目录，绑定后智能体第一次汇报带的 project_directory 必须与它一致（D-161）。
	// 所以没填运行目录时默认用绑定的目录；填了别的目录则在下面的目录检查里提前报错，而不是等智能体起来后第一次汇报才被拒绝。
	if e.pending {
		bound, berr := g.h.store.WorkflowBoundDirectory(ctx, req.WorkflowID)
		if berr != nil {
			log.Printf("[terminal] 启用 Agent 读取工作流绑定目录失败：%v", berr)
		}
		e.boundDir = bound
		if bound != "" && req.Dir == "" {
			req.Dir = bound
			e.req.Dir = bound
		}
	}

	// 主机候选；没选主机时，只有一台可选就自动选它（在线的优先）。
	tmp := &sessionRunInspect{req: sessionRunRequest{WorkflowID: req.WorkflowID, Host: req.Host}}
	g.resolveSessionRunHost(ctx, tmp)
	e.hosts = tmp.hosts
	if req.Host == "" {
		var online []string
		for _, h := range e.hosts {
			if h.Online {
				online = append(online, h.MachineCode)
			}
		}
		switch {
		case len(online) == 1:
			req.Host = online[0]
		case len(online) == 0 && len(e.hosts) == 1:
			req.Host = e.hosts[0].MachineCode
		}
		e.req.Host = req.Host
	}

	// 运行配置：新开对话（没给对话编号）或接着给定的对话。
	run, verr := store.NormalizeHeadlessRun(store.HeadlessRun{
		Bin: req.Bin, Model: req.Model, Posture: req.Posture, WorkflowID: req.WorkflowID,
		ConversationID: req.ConversationID, NewConversation: req.ConversationID == "", Prompt: req.Prompt,
		MaxRunMinutes: req.MaxRunMinutes, AutoResume: req.AutoResume, Origin: store.HeadlessOriginEnableAgent,
	})
	e.run = run
	if verr != nil {
		e.add(http.StatusBadRequest, "bad_options", verr.Error())
	}

	// 目录与主机。目录的写法要和主机系统对得上：Windows 主机要盘符 / UNC 路径，其余主机要 / 开头；还没选定主机时只查「是不是某种绝对路径」。
	dirOK := true
	hostOS := ""
	for _, h := range e.hosts {
		if h.MachineCode == req.Host {
			hostOS = h.OS
		}
	}
	switch {
	case req.Dir == "":
		dirOK = false
		e.add(http.StatusConflict, "no_dir", "请填写要在主机的哪个目录运行（--workspace，主机上的绝对路径）")
	case !headlessDirShapeOK(req.Dir):
		dirOK = false
		e.add(http.StatusBadRequest, "bad_dir", "目录必须是主机上的绝对路径（Linux / macOS 以 / 开头；Windows 写盘符路径，如 C:\\work）")
	case utf8.RuneCountInString(req.Dir) > store.MaxLauncherDirRunes:
		dirOK = false
		e.add(http.StatusBadRequest, "bad_dir", fmt.Sprintf("目录最长 %d 个字", store.MaxLauncherDirRunes))
	case req.Host != "" && headlessDirOK(req.Dir, isWindowsOS(hostOS)) != nil:
		dirOK = false
		e.add(http.StatusBadRequest, "bad_dir", headlessDirOK(req.Dir, isWindowsOS(hostOS)).Error())
	}
	// 与网页新建会话时绑定的项目目录不一致：智能体第一次汇报会被 D-161 拒绝。不影响命令预览（dirOK 不变），只让「能不能运行」为否。
	if dirOK && e.boundDir != "" && filepath.Clean(req.Dir) != e.boundDir {
		e.add(http.StatusConflict, "dir_mismatch", fmt.Sprintf(
			"这个会话在网页里绑定了项目目录 %s：智能体第一次汇报带的目录必须与它一致，运行目录填别的路径它会被拒绝。请把运行目录改成 %s", e.boundDir, e.boundDir))
	}
	if req.Host == "" {
		e.add(http.StatusConflict, "no_host", "请选择要在哪台主机上运行")
	}

	e.preset = store.TerminalLauncher{
		ID: enablePresetID(req.WorkflowID), Name: enablePresetName(e.title, req.WorkflowID, true),
		Host: req.Host, Dir: req.Dir, Kind: store.LauncherKindHeadless, Run: &run,
	}
	if verr != nil || !dirOK || req.Host == "" {
		return e // 选项还不完整：命令拼不出来，预览把已经能确定的部分给出去
	}

	// 其余前置条件（主机在线 / 系统、上一次运行、对话、命令）与预设运行同一套检查。
	in := g.inspectLauncher(ctx, e.preset, now)
	e.in = in
	for _, w := range in.errs {
		switch w.Code {
		case "host_not_found":
			w = newWakeError(w.Status, w.Code, "选中的主机不存在、没有启用或已被删除")
		}
		e.errs = append(e.errs, w)
	}
	return e
}

// sessionEnablePreview 把检查结果整理成给弹窗的只读预览。
func (g *guardService) sessionEnablePreview(ctx context.Context, req sessionEnableRequest, now time.Time) SessionEnablePreview {
	e := g.inspectSessionEnable(ctx, req, now)
	run := e.run
	pv := SessionEnablePreview{
		WorkflowID: e.req.WorkflowID, Title: e.title, Pending: e.pending, BoundDir: e.boundDir, Host: e.req.Host, Hosts: e.hosts, Dir: e.req.Dir,
		Bin: run.EffectiveBin(), Model: run.Model, Posture: run.EffectivePosture(),
		NewConversation: run.NewConversation, ConversationID: run.ConversationID,
		MaxRunMinutes: run.EffectiveMaxRunMinutes(), AutoResume: run.AutoResume,
		CanRun: len(e.errs) == 0,
	}
	if pv.Hosts == nil {
		pv.Hosts = []SessionRunHost{}
	}
	// 提示词与「没写自己的话时会用的默认」：渲染走统一的唤醒词渲染（#232 D3），默认取分层选出的那份模板（会话模板 > 全局覆盖 > 内置）。
	wf := strings.TrimSpace(e.req.WorkflowID)
	render := func(kind sessionprompt.Kind) {
		pv.Prompt = renderLauncherWake(ctx, g.h.store, launcherWake{
			Kind: kind, Custom: run.Prompt, WorkflowID: wf, Project: e.req.Dir, MaxRunSec: run.EffectiveMaxRunSeconds(),
		}).Text
		pv.DefaultPrompt, _ = g.h.store.ResolveWakeTemplate(ctx, kind, wf, "", wf != "")
	}
	if run.ConversationID != "" {
		render(sessionprompt.KindResume)
	} else {
		render(sessionprompt.KindNew)
	}
	if e.in != nil {
		// 生效值以检查结果为准：这个工作流已经学到对话编号（上一次启用跑过）时，这次是续聊它而不是新开。
		pv.NewConversation, pv.ConversationID = e.in.newConv, e.in.conv
		if e.in.conv != "" {
			render(sessionprompt.KindResume)
		}
		pv.Command = e.in.command
		pv.Warnings = append(pv.Warnings, e.in.warnings...)
		if e.in.host != nil && e.in.host.ExecMode == model.TerminalModeApprove {
			pv.Warnings = append(pv.Warnings, "这台主机的执行模式是「需批准」：运行会先停在待批准，要到 ExecMesh 里批准之后才会开始")
		}
	}
	if run.EffectivePosture() == store.HeadlessPostureAllowlist {
		pv.Warnings = append(pv.Warnings, allowlistRunWarning)
	}
	if pv.Warnings == nil {
		pv.Warnings = []string{}
	}
	for _, w := range e.errs {
		pv.Issues = append(pv.Issues, SessionRunIssue{Code: w.Code, Message: w.Message})
	}
	if pv.Issues == nil {
		pv.Issues = []SessionRunIssue{}
	}
	// 之前启用过：预设与它最近一次运行的现状。
	id := enablePresetID(e.req.WorkflowID)
	if launchers, err := g.h.store.ListTerminalLaunchers(ctx); err == nil {
		for _, l := range launchers {
			if l.ID == id {
				pv.Preset = &SessionEnablePreset{ID: l.ID, Name: l.Name}
				break
			}
		}
	}
	if pv.Preset != nil {
		if rec := g.latestLauncherRun(ctx, id); rec != nil {
			v := g.runViewOf(ctx, *rec, now)
			pv.LastRun = &v
		}
	}
	return pv
}

// enableSessionAgent 提交一次「启用 Agent」：先做全部前置检查（任何一项不满足都不会改任何东西），
// 再保存预设并运行它；运行失败就撤掉刚新建的预设。
func (g *guardService) enableSessionAgent(ctx context.Context, req sessionEnableRequest, now time.Time) (LauncherRunPlan, store.TerminalLauncher, error) {
	var zero store.TerminalLauncher
	e := g.inspectSessionEnable(ctx, req, now)
	if len(e.errs) > 0 {
		return LauncherRunPlan{}, zero, e.errs[0]
	}

	launcher := e.preset
	_, created, err := g.h.store.UpsertTerminalLauncher(ctx, launcher)
	if err != nil && errors.Is(err, store.ErrInvalidLauncher) {
		// 预设名撞了、或会话名里有预设名不允许的字符：退回只用工作流尾巴的名字再试一次。
		launcher.Name = enablePresetName(e.title, e.req.WorkflowID, false)
		_, created, err = g.h.store.UpsertTerminalLauncher(ctx, launcher)
	}
	if err != nil {
		switch {
		case errors.Is(err, store.ErrLauncherLimit):
			return LauncherRunPlan{}, zero, newWakeError(http.StatusConflict, "launcher_limit",
				fmt.Sprintf("终端启动预设已经有 %d 条了，放不下新的：「启用 Agent」会给每个会话建一条，先到终端启动器里删掉不用的预设", store.MaxTerminalLaunchers))
		case errors.Is(err, store.ErrInvalidLauncher):
			return LauncherRunPlan{}, zero, newWakeError(http.StatusBadRequest, "bad_launcher", strings.TrimPrefix(err.Error(), store.ErrInvalidLauncher.Error()+"："))
		}
		return LauncherRunPlan{}, zero, err
	}

	plan, preset, rerr := g.runLauncher(ctx, launcher.ID, now)
	if rerr != nil {
		if created { // 不留半成品：这条预设是为这次运行新建的
			if _, _, derr := g.h.store.DeleteTerminalLauncher(ctx, launcher.ID); derr != nil && !errors.Is(derr, store.ErrLauncherNotFound) {
				log.Printf("[terminal] 启用 Agent 运行失败后撤销预设失败：%v", derr)
			}
		}
		return LauncherRunPlan{}, zero, rerr
	}
	return plan, preset, nil
}

// ---------- REST ----------

// handleSessionEnablePreview 是只读预览：不产生任何记录，不需要解锁。
func (h *Hub) handleSessionEnablePreview(c *gin.Context) {
	var req sessionEnableRequest
	if !bindJSON(c, &req) {
		return
	}
	c.JSON(http.StatusOK, gin.H{"preview": h.guardian.sessionEnablePreview(c.Request.Context(), req, h.now())})
}

// handleSessionEnable 启用 Agent：保存预设并运行（需要刚刚解锁：它会让主机运行命令）。
func (h *Hub) handleSessionEnable(c *gin.Context) {
	var req sessionEnableRequest
	if !bindJSON(c, &req) {
		return
	}
	ctx := c.Request.Context()
	plan, preset, err := h.guardian.enableSessionAgent(ctx, req, h.now())
	if err != nil {
		var we *wakeError
		if errors.As(err, &we) {
			c.JSON(we.Status, gin.H{"error": we.Message, "code": we.Code})
			return
		}
		log.Printf("[terminal] 启用 Agent 失败: %v", err)
		h.fail(c, err)
		return
	}
	h.audit.record(ctx, auditEvent{
		Event: EvSessionEnable, Actor: subjectOf(c), IP: c.ClientIP(), MachineCode: preset.Host, RunID: plan.RunID,
		Detail: fmt.Sprintf("workflow=%s preset=%s posture=%s new=%t", strings.TrimSpace(req.WorkflowID), preset.ID, plan.Posture, plan.NewConversation),
	})
	c.JSON(http.StatusOK, gin.H{"run": plan, "preset": gin.H{"id": preset.ID, "name": preset.Name}, "prefs": h.prefsView(ctx)})
}
