package terminal

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"path"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gin-gonic/gin"

	"github.com/teacat99/RelayMesh/v2/internal/model"
	"github.com/teacat99/RelayMesh/v2/internal/store"
	"github.com/teacat99/RelayMesh/v2/internal/termproto"
)

// 会话页底部「运行」按钮（#140）：这个会话在哪台主机、哪个目录，有哪些「对应」的非交互续聊预设，此刻能不能运行；
// 没有对应预设时，给一份预填好的「为这个会话创建续聊预设」建议。
//
// 「对应」同时满足三条（缺一条就不算）：
//   - 主机：预设的主机是会话所在的主机。会话上报的主机名按 terminal_exec 同一口径（matchHosts）对上主机清单；
//     同名有多台时，预设钉的那一台算；会话没有上报主机名时不按主机筛（否则永远对不上）；
//   - 目录：预设的目录与会话上报的项目目录相同——`agent --resume` 要在对话原来的目录里才找得到它，所以不做「在子目录下」的宽松匹配；
//   - 对话：预设跟的就是这个会话——预设里的工作流是这个会话的工作流，或预设里固定的对话编号是这个会话的对话。
//     没有这一条，同一台主机、同一个目录里「别的工作流的续聊预设」也会冒出来，点了就接着别人的对话运行。
//
// 视图只读：不产生运行记录，也不需要解锁。真正运行仍走 POST /launchers/:id/run（需要刚刚解锁），
// 并由服务端再检查一次（inspectLauncher，与这里是同一份判断）。

// SessionLauncherView 是一条与会话对应的非交互续聊预设：确认窗要展示的内容，以及此刻能不能运行。
type SessionLauncherView struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Host      string `json:"host"`
	HostTitle string `json:"host_title,omitempty"`
	Dir       string `json:"dir"`
	// ConversationID 是这次运行实际要接着的对话（预设里固定的，或工作流里最近一个有动静的）；还不知道是空。
	ConversationID string `json:"conversation_id,omitempty"`
	Model          string `json:"model,omitempty"`
	Posture        string `json:"posture"`
	// MaxRunSeconds 是这次运行的最长运行时间（秒）。
	MaxRunSeconds int `json:"max_run_seconds"`
	// Command 是拼出的整行命令（只读预览；缺对话编号时为空）。
	Command  string            `json:"command,omitempty"`
	Issues   []SessionRunIssue `json:"issues,omitempty"`
	Warnings []string          `json:"warnings,omitempty"`
	CanRun   bool              `json:"can_run"`
	// Busy 表示现在不能运行，是因为对话还在运行或这个预设上一次运行没结束（run_active / conversation_live）：
	// 界面在这种时候不显示「运行」按钮（状态块已经在说它正在工作），而不是显示一个灰掉的按钮。
	Busy bool `json:"busy"`
	// LastRun 是这个预设最近一次运行的现状（没运行过是 null）。
	LastRun *LauncherRunView `json:"last_run"`
}

// SessionLauncherSuggest 是没有对应预设时的一份预填：启动器「本项目」标签顶部据此给「为这个会话创建续聊预设」。
type SessionLauncherSuggest struct {
	Name       string `json:"name"`
	Host       string `json:"host,omitempty"`
	HostTitle  string `json:"host_title,omitempty"`
	Dir        string `json:"dir"`
	WorkflowID string `json:"workflow_id"`
	Posture    string `json:"posture"`
	// NeedsHost 表示主机没法唯一确定（会话没上报主机名，或有多台同名主机）：界面要让人先选一台，不能一键创建。
	NeedsHost bool `json:"needs_host,omitempty"`
}

// SessionLaunchersView 是 GET /launchers/for-session 的返回。
type SessionLaunchersView struct {
	WorkflowID     string `json:"workflow_id"`
	ConversationID string `json:"conversation_id,omitempty"`
	// SessionHost 是会话上报的主机名；Host 是对上主机清单后唯一确定的机器码（没法唯一确定时为空）。
	SessionHost string `json:"session_host,omitempty"`
	Host        string `json:"host,omitempty"`
	// Hosts 是名字与会话上报的主机一致的主机（机器码）：启动器「本项目」标签按它筛预设；空表示不按主机筛。
	Hosts []string `json:"hosts"`
	// Dir 是会话上报的项目目录（主机上的绝对路径）；没有或不是绝对路径时为空。
	Dir     string                `json:"dir,omitempty"`
	Presets []SessionLauncherView `json:"presets"`
	// Suggest 只在没有对应预设、又足以建议时给出；建议不出来时 Reason 说明为什么。
	Suggest *SessionLauncherSuggest `json:"suggest,omitempty"`
	Reason  string                  `json:"reason,omitempty"`
}

// sameDir 判断两个主机上的绝对路径是不是同一个目录（容忍多余的斜杠与 . / ..）；不是绝对路径一律不算。
func sameDir(a, b string) bool {
	a, b = strings.TrimSpace(a), strings.TrimSpace(b)
	if !strings.HasPrefix(a, "/") || !strings.HasPrefix(b, "/") {
		return false
	}
	return path.Clean(a) == path.Clean(b)
}

// followsSession 判断预设跟的是不是这个会话：工作流相同，或固定的对话编号就是这个会话的对话。
func followsSession(run store.HeadlessRun, workflowID, conversationID string) bool {
	if strings.TrimSpace(run.WorkflowID) == workflowID {
		return true
	}
	return conversationID != "" && strings.TrimSpace(run.ConversationID) == conversationID
}

// sessionLaunchers 整理一个会话页的「运行」视图。
func (g *guardService) sessionLaunchers(ctx context.Context, workflowID string, now time.Time) (SessionLaunchersView, error) {
	wf := strings.TrimSpace(workflowID)
	v := SessionLaunchersView{WorkflowID: wf, Hosts: []string{}, Presets: []SessionLauncherView{}}
	if wf == "" {
		return v, newWakeError(http.StatusBadRequest, "no_workflow", "缺少工作流编号")
	}
	launchers, err := g.h.store.ListTerminalLaunchers(ctx)
	if err != nil {
		return v, err
	}

	// 目标（对话 / 目录 / 主机）直接用会话页「运行」的解析：同一个口径、没有副作用。
	in := g.inspectSessionRun(ctx, sessionRunRequest{WorkflowID: wf}, now)
	v.ConversationID = in.conv
	sessionHost := in.sessionHost // 客户端没报主机名时 store 填的占位值也算「没上报」；还没有状态会话时用工作流上记录的主机（#182）
	v.SessionHost = sessionHost
	if strings.HasPrefix(in.dir, "/") {
		v.Dir = path.Clean(in.dir)
	}
	// 名字对得上的主机；只有恰好一台时才算「唯一确定」（同名多台要人选；in.host 可能是其中在线的那一台，这里不采用）。
	named := map[string]bool{}
	for _, h := range in.hosts {
		if h.Matched {
			named[h.MachineCode] = true
			v.Hosts = append(v.Hosts, h.MachineCode)
		}
	}
	if len(v.Hosts) == 1 {
		v.Host = v.Hosts[0]
	}

	for _, p := range launchers {
		if !p.IsHeadless() || p.Run == nil || v.Dir == "" || !sameDir(p.Dir, v.Dir) {
			continue
		}
		if sessionHost != "" && !named[p.Host] {
			continue
		}
		if !followsSession(*p.Run, wf, in.conv) {
			continue
		}
		v.Presets = append(v.Presets, g.sessionLauncherOf(ctx, p, now))
	}
	sort.SliceStable(v.Presets, func(i, j int) bool {
		a, b := v.Presets[i], v.Presets[j]
		if a.CanRun != b.CanRun {
			return a.CanRun
		}
		return strings.ToLower(a.Name) < strings.ToLower(b.Name)
	})

	if len(v.Presets) == 0 {
		g.suggestSessionLauncher(in, &v, launchers)
	}
	return v, nil
}

// sessionLauncherOf 把一条对应预设整理成界面用的视图（套用与提交运行完全相同的前置检查）。
func (g *guardService) sessionLauncherOf(ctx context.Context, p store.TerminalLauncher, now time.Time) SessionLauncherView {
	pin := g.inspectLauncher(ctx, p, now)
	it := SessionLauncherView{
		ID: p.ID, Name: p.Name, Host: p.Host, HostTitle: p.Host, Dir: p.Dir, ConversationID: pin.conv,
		Model: pin.run.Model, Posture: pin.run.EffectivePosture(), MaxRunSeconds: pin.maxRunSec,
		Command: pin.command, Warnings: pin.warnings, CanRun: len(pin.errs) == 0,
	}
	if pin.host != nil {
		it.HostTitle = hostDisplayName(*pin.host)
		if pin.host.ExecMode == model.TerminalModeApprove {
			it.Warnings = append(it.Warnings, "这台主机的执行模式是「需批准」：运行会先停在待批准，要到 ExecMesh 里批准之后才会开始")
		}
	}
	if it.Posture == store.HeadlessPostureAllowlist {
		it.Warnings = append(it.Warnings, allowlistRunWarning)
	}
	for _, e := range pin.errs {
		it.Issues = append(it.Issues, SessionRunIssue{Code: e.Code, Message: e.Message})
		if e.Code == "run_active" || e.Code == "conversation_live" {
			it.Busy = true
		}
	}
	if rec := g.latestLauncherRun(ctx, p.ID); rec != nil {
		rv := g.runViewOf(ctx, *rec, now)
		it.LastRun = &rv
	}
	return it
}

// suggestSessionLauncher 在没有对应预设时给一份建议；建议不出来就写明原因。
func (g *guardService) suggestSessionLauncher(in *sessionRunInspect, v *SessionLaunchersView, launchers []store.TerminalLauncher) {
	// 原因文字都在这里写：会话页运行接口（session-run）的提示是写给接口调用方的（让请求带上 dir / conversation_id），不适合直接放进预设界面。
	switch {
	case len(launchers) >= store.MaxTerminalLaunchers:
		v.Reason = fmt.Sprintf("启动预设已经有 %d 个（到上限了）：先删掉一个不用的，再为这个会话创建续聊预设", store.MaxTerminalLaunchers)
		return
	case v.Dir == "":
		if d := strings.TrimSpace(in.dir); d != "" {
			v.Reason = fmt.Sprintf("会话上报的项目目录 %s 不是主机上的绝对路径，不知道续聊该在哪个目录运行", termproto.SanitizeText(d, 200))
		} else {
			v.Reason = "不知道这个会话在主机的哪个目录（会话没有上报项目目录）"
		}
		return
	}
	s := &SessionLauncherSuggest{
		Name: uniqueLauncherName(launchers, "续聊 · "+launcherDirBase(v.Dir)), Dir: v.Dir, WorkflowID: v.WorkflowID,
		Posture: store.HeadlessPostureAllowlist,
	}
	switch {
	case v.Host != "":
		s.Host, s.HostTitle = v.Host, v.Host
		for _, h := range in.hosts {
			if h.MachineCode == v.Host {
				s.HostTitle = displayNameOf(h.Remark, h.Hostname)
			}
		}
	case v.SessionHost == "", len(v.Hosts) > 1:
		s.NeedsHost = true // 会话没上报主机名，或有多台同名主机：要人选
	default:
		// 会话上报了主机名，主机清单里却没有对得上的已启用主机：在它上面运行不了，建议也就没意义。
		v.Reason = fmt.Sprintf("主机清单里没有备注或主机名为 %s 的已启用主机：先把那台机器接入终端，或给已接入的主机写一个同名备注（名字对上了，会话才认得出它在哪台主机）", termproto.SanitizeText(v.SessionHost, 80))
		return
	}
	v.Suggest = s
}

// launcherDirBase 取目录最后一段当作项目名；根目录或取不到时用「会话」。
func launcherDirBase(dir string) string {
	b := path.Base(path.Clean(dir))
	if b == "/" || b == "." || b == "" {
		return "会话"
	}
	return b
}

// uniqueLauncherName 在已有预设名字之外（不分大小写）给出一个合法的新名字：超长截断，重名加序号。
func uniqueLauncherName(launchers []store.TerminalLauncher, want string) string {
	want = strings.Join(strings.Fields(termproto.SanitizeText(want, 0)), " ")
	clip := func(s string, n int) string {
		if utf8.RuneCountInString(s) <= n {
			return s
		}
		return string([]rune(s)[:n])
	}
	used := map[string]bool{}
	for _, l := range launchers {
		used[strings.ToLower(l.Name)] = true
	}
	base := clip(want, store.MaxLauncherNameRunes)
	if !used[strings.ToLower(base)] {
		return base
	}
	for i := 2; ; i++ {
		suffix := fmt.Sprintf(" %d", i)
		name := clip(want, store.MaxLauncherNameRunes-utf8.RuneCountInString(suffix)) + suffix
		if !used[strings.ToLower(name)] {
			return name
		}
	}
}

func (h *Hub) handleSessionLaunchers(c *gin.Context) {
	v, err := h.guardian.sessionLaunchers(c.Request.Context(), c.Query("workflow_id"), h.now())
	if err != nil {
		var we *wakeError
		if errors.As(err, &we) {
			c.JSON(we.Status, gin.H{"error": we.Message, "code": we.Code})
			return
		}
		log.Printf("[terminal] 读取会话对应的续聊预设失败: %v", err)
		h.fail(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"session": v})
}
