package store

import (
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// LauncherGuard 是终端启动器预设的「会话守护」配置（D-214）。
//
// 它只描述「怎么判断、怎么唤醒」，不会让任何东西自动运行：唤醒是人在界面里点一下之后，
// 由浏览器把一句固定的提示词输入到会话里（会话已经结束时，按 ResumeCommand 重新拉起再输入）。
type LauncherGuard struct {
	// Enabled 是监测开关；关闭时保留其余配置，下次打开不用重填。
	Enabled bool `json:"enabled"`
	// WorkflowID 是这个会话里的智能体使用的 RelayMesh 工作流编号：服务端据此判断它是不是在合法地等你回复、
	// 有没有没读的回复或追加、有没有进行中的任务。留空只能监测「意外退出」，无法判断停摆。
	WorkflowID string `json:"workflow_id,omitempty"`
	// SilenceMinutes 是多久没有任何活动才算停摆；0 表示用默认值（DefaultGuardSilenceMinutes）。
	SilenceMinutes int `json:"silence_minutes,omitempty"`
	// ResumeCommand 是会话已经结束时重新拉起用的一整行命令，其中 {chat_id} 会换成智能体最近一次汇报的对话编号。
	// 留空表示只能在会话还活着时唤醒。
	ResumeCommand string `json:"resume_command,omitempty"`
	// WakePrompt 是唤醒时输入的提示词正文；留空用服务端的默认正文。支持 %n（第几次）、%max（上限）、%reason（原因）。
	// 开头的「【系统唤醒 …】」由服务端固定加上；正文里不能有 shell 特殊字符（见 GuardPromptUnsafe）。
	WakePrompt string `json:"wake_prompt,omitempty"`
	// ExitEndsSession 为 true 时启动行使用 exec 包装命令，命令退出即会话结束（默认 false）。
	ExitEndsSession bool `json:"exit_ends_session,omitempty"`
	// Headless 是「非交互唤醒」（#113 M3.4，默认关闭）：会话已经结束、你又点了「非交互唤醒」时，
	// Hub 经这台主机的执行通道运行一次 `agent -p --resume <对话编号> …`，不打开终端。
	Headless *HeadlessWake `json:"headless,omitempty"`
}

// 非交互唤醒的权限姿态。
const (
	// HeadlessPostureAllowlist（默认）：不带 --force，智能体只能做 CLI 自己的 permissions.allow 白名单放行的事
	// （例如 Mcp(relaymesh:*)、Shell(git status)）；白名单之外的 shell 命令会被拒绝。
	HeadlessPostureAllowlist = "allowlist"
	// HeadlessPostureForce：带 --force，等于放开全部命令；只在你明确接受时使用。
	HeadlessPostureForce = "force"
)

// HeadlessWake 是非交互唤醒的配置。
type HeadlessWake struct {
	Enabled bool   `json:"enabled"`
	Posture string `json:"posture,omitempty"` // allowlist | force；空 = allowlist
}

// EffectivePosture 返回实际生效的权限姿态。
func (h HeadlessWake) EffectivePosture() string {
	if h.Posture == HeadlessPostureForce {
		return HeadlessPostureForce
	}
	return HeadlessPostureAllowlist
}

// EffectiveSilence 返回实际生效的停摆判定时长。
func (g LauncherGuard) EffectiveSilence() time.Duration {
	m := g.SilenceMinutes
	if m <= 0 {
		m = DefaultGuardSilenceMinutes
	}
	return time.Duration(m) * time.Minute
}

// normalize 规整各字段：提示词是单行文本，空白折叠成一个空格；其余字段只去掉首尾空白。
func (g LauncherGuard) normalize() LauncherGuard {
	g.WorkflowID = strings.TrimSpace(g.WorkflowID)
	g.ResumeCommand = strings.TrimSpace(g.ResumeCommand)
	g.WakePrompt = strings.Join(strings.Fields(g.WakePrompt), " ")
	if g.Headless != nil {
		h := *g.Headless // 复制一份：规整不能改到调用方的数据
		h.Posture = strings.ToLower(strings.TrimSpace(h.Posture))
		if !h.Enabled && h.Posture == "" {
			g.Headless = nil
		} else {
			g.Headless = &h
		}
	}
	return g
}

// isZero 表示这份守护配置什么都没填（也没打开）；这样的配置不必落盘。
func (g LauncherGuard) isZero() bool {
	return !g.Enabled && g.WorkflowID == "" && g.SilenceMinutes == 0 && g.ResumeCommand == "" && g.WakePrompt == "" && !g.ExitEndsSession && g.Headless == nil
}

// validate 检查一份（已规整的）守护配置；错误信息面向用户，并带上出问题的字段。
func (g LauncherGuard) validate() error {
	if g.SilenceMinutes != 0 && (g.SilenceMinutes < MinGuardSilenceMinutes || g.SilenceMinutes > MaxGuardSilenceMinutes) {
		return fmt.Errorf("%w：停摆判定时长请填 %d～%d 分钟", ErrInvalidLauncher, MinGuardSilenceMinutes, MaxGuardSilenceMinutes)
	}
	if g.WorkflowID != "" {
		switch n := utf8.RuneCountInString(g.WorkflowID); {
		case n > MaxGuardWorkflowRunes:
			return fmt.Errorf("%w：工作流编号最长 %d 个字，现在是 %d 个字", ErrInvalidLauncher, MaxGuardWorkflowRunes, n)
		case hasControlRune(g.WorkflowID) || strings.IndexFunc(g.WorkflowID, unicode.IsSpace) >= 0:
			return fmt.Errorf("%w：工作流编号里不能有空格或控制字符", ErrInvalidLauncher)
		}
	}
	if g.ResumeCommand != "" {
		switch n := utf8.RuneCountInString(g.ResumeCommand); {
		case n > MaxLauncherCommandRunes:
			return fmt.Errorf("%w：重新拉起命令最长 %d 个字，现在是 %d 个字", ErrInvalidLauncher, MaxLauncherCommandRunes, n)
		case hasControlRune(g.ResumeCommand):
			return fmt.Errorf("%w：重新拉起命令只能是一行，不能有换行、制表符或其他控制字符", ErrInvalidLauncher)
		}
	}
	if g.Headless != nil {
		switch g.Headless.Posture {
		case "", HeadlessPostureAllowlist, HeadlessPostureForce:
		default:
			return fmt.Errorf("%w：非交互唤醒的权限姿态只能是 %s 或 %s", ErrInvalidLauncher, HeadlessPostureAllowlist, HeadlessPostureForce)
		}
	}
	if g.WakePrompt != "" {
		switch n := utf8.RuneCountInString(g.WakePrompt); {
		case n > MaxGuardPromptRunes:
			return fmt.Errorf("%w：唤醒提示词最长 %d 个字，现在是 %d 个字", ErrInvalidLauncher, MaxGuardPromptRunes, n)
		case hasControlRune(g.WakePrompt):
			return fmt.Errorf("%w：唤醒提示词只能是一行，不能有控制字符", ErrInvalidLauncher)
		case strings.ContainsAny(g.WakePrompt, GuardPromptUnsafe):
			return fmt.Errorf("%w：唤醒提示词里不能有 %s 这些字符（Cursor CLI 退出后会话会停在命令行，它们会被当成命令）", ErrInvalidLauncher, GuardPromptUnsafe)
		}
	}
	return nil
}

// GuardPromptUnsafe 是唤醒提示词里不允许出现的字符：它们在 shell 里有特殊含义。
//
// 提示词本该输入到智能体的对话框；但万一那时 Cursor CLI 已经退出、会话停在普通的命令行里，输入的内容会被 shell 解释。
// 所以只允许「在 shell 里只是普通文字」的字符——中文标点、字母、数字、空格、% 都没问题。
// 提示词的开头由服务端固定为「【系统唤醒 …】」，第一个词因此也不会是任何命令。
const GuardPromptUnsafe = "`$\\;&|<>(){}[]'\"!*?~#"
