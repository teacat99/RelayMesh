package store

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// 启动器预设的类型（TerminalLauncher.Kind）。
const (
	// LauncherKindTerminal 是默认类型：点「启动」在主机上开一个终端会话并自动输入命令。
	// 旧存档没有 kind 字段，都是这种；存盘时规整成空串。
	LauncherKindTerminal = "terminal"
	// LauncherKindHeadless 是「非交互续聊」（#126）：点「运行」经主机的执行通道跑一次
	// `agent -p --resume <对话编号> …`，不开终端；运行配置见 HeadlessRun。
	LauncherKindHeadless = "headless"
)

// 非交互续聊预设的取值范围（按字符数）。
const (
	// MaxHeadlessPromptRunes 与会话 / 全局模板同一个上限（sessionprompt.TemplateMaxRunes，测试里核对）：
	// v2 内置默认比旧版长，预设里「以默认为底稿」填进去必须还能存（#232 D3，原先是 600）。
	MaxHeadlessPromptRunes = 1500
	MaxHeadlessModelRunes  = 64
	MaxHeadlessBinRunes    = 128
	// DefaultHeadlessBin 是没填可执行文件时用的命令名。
	DefaultHeadlessBin = "agent"
)

// 非交互续聊预设「最长运行时间」的取值范围（分钟，#141）。
const (
	// MinHeadlessRunMinutes 是下限：再短连智能体启动的时间都不够，没有意义。
	MinHeadlessRunMinutes = 1
	// MaxHeadlessRunMinutes 是上限，也是没填时的默认值：平台单次运行上限 7 天。
	// 它与 termproto.MaxRunTimeoutSec 同值（store 不依赖协议包，由 terminal 包的测试守住这一点）。
	MaxHeadlessRunMinutes = 7 * 24 * 60
)

// 自动续跑「成功」判定时长的取值范围（分钟，#172）。
const (
	// DefaultAutoResumeOKMinutes 是没填时的默认值：续跑出来的运行连续跑满 10 分钟，就算这次续跑成功。
	DefaultAutoResumeOKMinutes = 10
	// MinAutoResumeOKMinutes 是下限；MaxAutoResumeOKMinutes 是上限（1 天）：再长就没有「算成功」的意义了。
	MinAutoResumeOKMinutes = 1
	MaxAutoResumeOKMinutes = 24 * 60
)

var (
	// headlessBinRe 是可执行文件允许的字符：命令名，或带 / ~ 的路径；没有空白、引号、分号、变量展开。
	headlessBinRe = regexp.MustCompile(`^[A-Za-z0-9_./~+-]+$`)
	// headlessModelRe 是 --model 的值允许的字符（claude-sonnet-5-5-max、gpt-5.2、vendor/model:tag 之类）。
	headlessModelRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/+-]*$`)
)

// headlessPrefixCmds 是 shell 内建或会改变后面命令含义的前缀命令，不能当作「可执行文件」。
var headlessPrefixCmds = map[string]bool{
	"cd": true, "env": true, "export": true, "exec": true, "source": true, ".": true, "eval": true, "set": true,
	"sudo": true, "doas": true, "nohup": true, "time": true, "command": true, "builtin": true, "nice": true,
	"setsid": true, "timeout": true, "xargs": true, "sh": true, "bash": true, "zsh": true, "fish": true,
}

// IsHeadlessPrefixCmd 判断一个词是不是 shell 内建或会改变后面命令含义的前缀命令（cd、env、sudo、bash……）：
// 非交互运行的第一个词必须是可执行文件本身，不能是它们。
func IsHeadlessPrefixCmd(word string) bool { return headlessPrefixCmds[word] }

// HeadlessRun 是「非交互续聊」预设（Kind == "headless"）的运行配置。
//
// 点「运行」时，Hub 经主机的执行通道运行（与 ExecMesh 同一条通道、同一套闸门）：
//
//	<bin> -p --resume <对话编号> --approve-mcps --trust [--model <模型>] [--force] --workspace <目录> --output-format stream-json '<提示词>'
//
// 对话来源（Source，#191）三选一：指定会话 = 上面这条命令；新会话 = 命令不带 `--resume <对话编号>`，其余相同；
// 最近会话 = 命令里的 `--resume <对话编号>` 换成 `--continue`（由 Cursor CLI 接着该目录里最近的对话）。
//
// 命令只由这些已校验的字段拼出，所有动态部分都经过单引号包裹（Linux / macOS 是 bash 的单引号，Windows 主机是 PowerShell 的单引号字面量，#178）；
// 预设不能写任意命令，所以改配置不会变成任意命令执行。Windows 主机上目录与可执行文件的写法见 headless_paths.go。
type HeadlessRun struct {
	// Bin 是可执行文件（命令名或路径）；空 = DefaultHeadlessBin。
	Bin string `json:"bin,omitempty"`
	// Model 是 --model 的值；空 = 不带（用 CLI 自己的默认模型）。
	Model string `json:"model,omitempty"`
	// Posture 是权限姿态：allowlist（默认，不带 --force，只能做 CLI 白名单放行的事）或 force（带 --force，放开全部命令）。
	Posture string `json:"posture,omitempty"`
	// WorkflowID 是智能体使用的 RelayMesh 工作流编号：用来找到它最近的对话、替换提示词里的 %workflow、
	// 在聊天里记系统行、判断这次运行有没有向 RelayMesh 汇报。
	WorkflowID string `json:"workflow_id,omitempty"`
	// ConversationID 固定要接着的对话；空 = 用 hook 汇报的、这个工作流最近的 CLI 对话编号。
	ConversationID string `json:"conversation_id,omitempty"`
	// Prompt 是提示词正文；空 = 服务端默认文本。其中 %workflow 换成工作流编号。
	Prompt string `json:"prompt,omitempty"`
	// MaxRunMinutes 是一次运行最长跑多久（分钟，#141）；0 = 没设，用平台单次运行上限 7 天。
	// 到点由主机强制结束这次运行（运行状态「超时」，界面写「达到运行时长上限」）；设了的值收进 [1 分钟, 7 天]，负数被拒绝。
	// 续聊是一个会话而不是一条短命令，所以默认不设短限；想给一次运行封顶（防止它无人看管地跑太久）才需要填。
	MaxRunMinutes int `json:"max_run_minutes,omitempty"`
	// AutoResume 打开后，这个预设发起的运行如果「异常退出」且错误被 CLI 自己标成可重试（#151，D-260），Hub 会在退避之后
	// 自动再续聊一次：24 小时最多 3 次，连续 2 次没有进展就停下并通知。默认关闭——它是无人值守的模型调用，有成本。
	// 你取消的、运行时长上限到点的、成功退出的、需要你处理或认不出原因的失败，一律不会自动续跑。
	AutoResume bool `json:"auto_resume,omitempty"`
	// AutoResumeOKMinutes 是自动续跑「成功」的判定时长（分钟，#172，追加 #304）：续跑出来的运行连续跑满这么久，
	// 就算这次续跑成功，24 小时内累计的续跑次数与退避从头算，不再被它之前的几次续跑拖累；
	// 比它短就算「没有进展」（连续 2 次没有进展就熔断）。0 = 没设，用默认的 10 分钟；设了的值收进 [1 分钟, 1 天]，负数被拒绝。
	AutoResumeOKMinutes int `json:"auto_resume_ok_minutes,omitempty"`
	// Source 是对话来源（#191）：resume（指定会话）/ new（新会话）/ continue（最近会话），见 HeadlessSource*。
	// 空 = 按旧字段推导（EffectiveSource）：填了对话编号 → resume；勾了 NewConversation → new（记下编号）；都没有 → resume（跟随工作流）。
	// 存量预设不迁移：读取时推导，到用户在表单里保存一次才写入这个字段。
	Source string `json:"source,omitempty"`
	// AfterNew 是「新会话」运行之后的去向（#559）：remember（记下编号，默认）/ stay（不切换）/ continue（成功后切到最近会话），
	// 见 HeadlessAfter*。只在 Source 是 new 时有意义；规整时其它来源会把它清空。
	AfterNew string `json:"after_new,omitempty"`
	// NewConversation 是旧版的「第一次运行时新开对话」勾选（#176 第三期）。它仍然是存量预设的来源：这个预设在「还不知道要接哪个对话」
	// （预设里没有固定对话编号、这个工作流里也没有智能体接入过）时不再报错，而是新开一个对话——命令不带 --resume，提示词换成
	// 「接入工作流」版本，对话编号从这次运行输出的 init 事件里学到并记下来，之后的运行就是续聊，所以它只会新开一次。
	// 设了 Source 之后它只是 Source / AfterNew 的镜像（规整时按 new + remember 同步），让回滚到旧版本时行为基本不变；
	// 没设 Source 的存量预设仍按它推导。固定了对话编号（ConversationID）就不能再选新开：二选一。
	NewConversation bool `json:"new_conversation,omitempty"`
	// Origin 标记预设是怎么来的：空 = 手工建的；HeadlessOriginEnableAgent = 会话页「启用 Agent」自动建的（#176）。
	// 只用来在启动器清单里给它打标记、以及让「启用 Agent」再次点击时复用同一条预设，不改变运行行为。
	Origin string `json:"origin,omitempty"`
}

// HeadlessOriginEnableAgent 是 HeadlessRun.Origin 的取值：这条预设是会话页「启用 Agent」自动建的。
const HeadlessOriginEnableAgent = "enable_agent"

// 非交互续聊预设的「对话来源」（HeadlessRun.Source，#191）：这次运行接到哪个对话上，三选一。
const (
	// HeadlessSourceResume 是「指定会话」：命令带 `--resume <对话编号>`。填了对话编号就固定接这一个；
	// 留空就取这个工作流里智能体最近汇报的对话（跟随工作流，「启用 Agent」建出的预设就是这样）。
	HeadlessSourceResume = "resume"
	// HeadlessSourceNew 是「新会话」：命令不带 `--resume`，Cursor CLI 新开一个对话，Hub 从输出的 init 事件里学到它的编号。
	// 之后的运行怎么办由 HeadlessRun.AfterNew 决定。
	HeadlessSourceNew = "new"
	// HeadlessSourceContinue 是「最近会话」：命令带 `--continue`，由 Cursor CLI 接着该目录里最近动过的对话。
	// 它接到哪个对话是 CLI 的决定（编辑器里开的窗口、别的工作流的运行、手敲的命令都算），Hub 运行前并不知道。
	HeadlessSourceContinue = "continue"
)

// 「新会话」运行之后的去向（HeadlessRun.AfterNew，#559）：只在对话来源是「新会话」时有意义。
const (
	// HeadlessAfterRemember 是默认：把学到的对话编号记成工作流当前的对话，之后的运行接着它——所以只新开一次。
	HeadlessAfterRemember = "remember"
	// HeadlessAfterStay 是「不切换」：每一次运行都新开一个对话，不接着上一次的。
	HeadlessAfterStay = "stay"
	// HeadlessAfterContinue 是「切到最近会话」：运行成功之后，Hub 把这个预设的对话来源改成「最近会话」，再之后的运行用 `--continue`。
	HeadlessAfterContinue = "continue"
)

// EffectiveBin 返回实际使用的可执行文件。
func (r HeadlessRun) EffectiveBin() string {
	if r.Bin == "" {
		return DefaultHeadlessBin
	}
	return r.Bin
}

// EffectivePosture 返回实际生效的权限姿态。
func (r HeadlessRun) EffectivePosture() string {
	if r.Posture == HeadlessPostureForce {
		return HeadlessPostureForce
	}
	return HeadlessPostureAllowlist
}

// EffectiveSource 返回实际生效的对话来源（HeadlessSource*）。设了 Source 就用它；没设的存量预设按旧字段推导：
// 勾了「新开对话」→ new；其余（固定了编号，或都空 = 跟随工作流）→ resume。读取时推导，不改写存档。
// （旧版校验不允许「勾了新开」同时「固定了编号」，这样的组合根本存不进来；万一传进来，validate 会按 new 的规则拒绝它。）
func (r HeadlessRun) EffectiveSource() string {
	switch r.Source {
	case HeadlessSourceResume, HeadlessSourceNew, HeadlessSourceContinue:
		return r.Source
	}
	if r.NewConversation {
		return HeadlessSourceNew
	}
	return HeadlessSourceResume
}

// EffectiveAfterNew 返回「新会话」运行之后的去向（HeadlessAfter*）：对话来源不是「新会话」时返回空串；
// 是「新会话」但没设（旧预设，或界面没传）时就是默认的「记下编号」——与旧版的勾选行为一致。
func (r HeadlessRun) EffectiveAfterNew() string {
	if r.EffectiveSource() != HeadlessSourceNew {
		return ""
	}
	switch r.AfterNew {
	case HeadlessAfterStay, HeadlessAfterContinue:
		return r.AfterNew
	}
	return HeadlessAfterRemember
}

// EffectiveMaxRunMinutes 返回实际生效的最长运行时间（分钟）：没设就是平台单次运行上限。
func (r HeadlessRun) EffectiveMaxRunMinutes() int {
	switch {
	case r.MaxRunMinutes <= 0:
		return MaxHeadlessRunMinutes
	case r.MaxRunMinutes > MaxHeadlessRunMinutes:
		return MaxHeadlessRunMinutes
	}
	return r.MaxRunMinutes
}

// EffectiveAutoResumeOK 返回实际生效的「续跑成功」判定时长：没设就是默认的 10 分钟，超过上限的收进上限。
func (r HeadlessRun) EffectiveAutoResumeOK() time.Duration {
	m := r.AutoResumeOKMinutes
	switch {
	case m <= 0:
		m = DefaultAutoResumeOKMinutes
	case m > MaxAutoResumeOKMinutes:
		m = MaxAutoResumeOKMinutes
	}
	return time.Duration(m) * time.Minute
}

// EffectiveMaxRunSeconds 是 EffectiveMaxRunMinutes 换算成秒：交给执行通道当这次运行的超时。
func (r HeadlessRun) EffectiveMaxRunSeconds() int { return r.EffectiveMaxRunMinutes() * 60 }

// normalize 规整各字段：提示词是单行文本，空白折叠成一个空格；其余字段只去掉首尾空白，权限姿态转小写；
// 最长运行时间超过平台上限的收进上限（负数留给 validate 拒绝）。
func (r HeadlessRun) normalize() HeadlessRun {
	r.Bin = strings.TrimSpace(r.Bin)
	r.Model = strings.TrimSpace(r.Model)
	r.Posture = strings.ToLower(strings.TrimSpace(r.Posture))
	r.WorkflowID = strings.TrimSpace(r.WorkflowID)
	r.ConversationID = strings.TrimSpace(r.ConversationID)
	r.Prompt = strings.Join(strings.Fields(r.Prompt), " ")
	r.Origin = strings.ToLower(strings.TrimSpace(r.Origin))
	r.Source = strings.ToLower(strings.TrimSpace(r.Source))
	r.AfterNew = strings.ToLower(strings.TrimSpace(r.AfterNew))
	if r.Source != "" {
		// 设了对话来源：旧的「新开对话」勾选只是它的镜像（new + 记下编号 才为真），回滚到旧版本时行为基本不变；
		// 「新会话」之外的来源没有「运行之后的去向」，清空它，免得留着一个不起作用的值。
		if r.Source != HeadlessSourceNew {
			r.AfterNew = ""
		}
		r.NewConversation = r.Source == HeadlessSourceNew && r.EffectiveAfterNew() == HeadlessAfterRemember
	}
	if r.MaxRunMinutes > MaxHeadlessRunMinutes {
		r.MaxRunMinutes = MaxHeadlessRunMinutes
	}
	if r.AutoResumeOKMinutes > MaxAutoResumeOKMinutes {
		r.AutoResumeOKMinutes = MaxAutoResumeOKMinutes
	}
	return r
}

// validate 检查一份（已规整的）运行配置；错误信息面向用户，并带上出问题的字段。
func (r HeadlessRun) validate() error {
	if r.Bin != "" {
		switch {
		case utf8.RuneCountInString(r.Bin) > MaxHeadlessBinRunes:
			return fmt.Errorf("%w：可执行文件最长 %d 个字", ErrInvalidLauncher, MaxHeadlessBinRunes)
		case !ValidHeadlessBin(r.Bin):
			return fmt.Errorf("%w：可执行文件只能是命令名或路径（Linux / macOS：字母、数字和 _ . / ~ + -；Windows：agent 这样的命令名，或 C:\\…\\agent.cmd 这样的盘符 / UNC 路径，路径里不能有 \" %% ^ & | < > ! * ?），不能是 cd、env、sudo、bash 这类前缀命令，也不能是 cmd、powershell 这类 shell", ErrInvalidLauncher)
		}
	}
	if r.Model != "" {
		switch {
		case utf8.RuneCountInString(r.Model) > MaxHeadlessModelRunes:
			return fmt.Errorf("%w：模型名最长 %d 个字", ErrInvalidLauncher, MaxHeadlessModelRunes)
		case !headlessModelRe.MatchString(r.Model):
			return fmt.Errorf("%w：模型名只能含字母、数字和 . _ : / + -，并以字母或数字开头", ErrInvalidLauncher)
		}
	}
	switch r.Posture {
	case "", HeadlessPostureAllowlist, HeadlessPostureForce:
	default:
		return fmt.Errorf("%w：权限姿态只能是 %s 或 %s", ErrInvalidLauncher, HeadlessPostureAllowlist, HeadlessPostureForce)
	}
	if r.WorkflowID != "" {
		switch n := utf8.RuneCountInString(r.WorkflowID); {
		case n > MaxGuardWorkflowRunes:
			return fmt.Errorf("%w：工作流编号最长 %d 个字，现在是 %d 个字", ErrInvalidLauncher, MaxGuardWorkflowRunes, n)
		case hasControlRune(r.WorkflowID) || strings.IndexFunc(r.WorkflowID, unicode.IsSpace) >= 0:
			return fmt.Errorf("%w：工作流编号里不能有空格或控制字符", ErrInvalidLauncher)
		}
	}
	if r.ConversationID != "" && !agentConvRe.MatchString(r.ConversationID) {
		return fmt.Errorf("%w：对话编号只能含字母、数字、下划线和连字符（最长 64 位）", ErrInvalidLauncher)
	}
	switch r.Source {
	case "", HeadlessSourceResume, HeadlessSourceNew, HeadlessSourceContinue:
	default:
		return fmt.Errorf("%w：对话来源只能是新会话（%s）、最近会话（%s）或指定会话（%s）",
			ErrInvalidLauncher, HeadlessSourceNew, HeadlessSourceContinue, HeadlessSourceResume)
	}
	switch r.AfterNew {
	case "", HeadlessAfterRemember, HeadlessAfterStay, HeadlessAfterContinue:
	default:
		return fmt.Errorf("%w：「新会话」运行之后只能是记下编号（%s）、不切换（%s）或切到最近会话（%s）",
			ErrInvalidLauncher, HeadlessAfterRemember, HeadlessAfterStay, HeadlessAfterContinue)
	}
	if r.WorkflowID == "" && r.ConversationID == "" {
		return fmt.Errorf("%w：请填工作流编号（用来找到智能体最近的对话），或者直接固定一个对话编号", ErrInvalidLauncher)
	}
	switch r.EffectiveSource() {
	case HeadlessSourceNew:
		switch {
		case r.ConversationID != "":
			return fmt.Errorf("%w：固定了对话编号就不能再选「新会话」：要接着这个对话，就选「指定会话」；要新开，就清空对话编号", ErrInvalidLauncher)
		case r.WorkflowID == "":
			return fmt.Errorf("%w：「新会话」需要填工作流编号：新对话靠它接入 RelayMesh，之后的运行也靠它找到这个对话", ErrInvalidLauncher)
		}
	case HeadlessSourceContinue:
		switch {
		case r.ConversationID != "":
			return fmt.Errorf("%w：「最近会话」由 Cursor CLI 决定接哪个对话，不能再固定对话编号：要固定，就选「指定会话」；要接最近的，就清空对话编号", ErrInvalidLauncher)
		case r.WorkflowID == "":
			return fmt.Errorf("%w：「最近会话」需要填工作流编号：它用来归属这次运行、在聊天里记系统行、判断有没有向 RelayMesh 汇报", ErrInvalidLauncher)
		}
	}
	switch r.Origin {
	case "", HeadlessOriginEnableAgent:
	default:
		return fmt.Errorf("%w：不认识的预设来源", ErrInvalidLauncher)
	}
	if r.Prompt != "" {
		switch n := utf8.RuneCountInString(r.Prompt); {
		case n > MaxHeadlessPromptRunes:
			return fmt.Errorf("%w：提示词最长 %d 个字，现在是 %d 个字", ErrInvalidLauncher, MaxHeadlessPromptRunes, n)
		case hasControlRune(r.Prompt):
			return fmt.Errorf("%w：提示词只能是一行，不能有控制字符", ErrInvalidLauncher)
		}
	}
	if r.MaxRunMinutes < 0 {
		return fmt.Errorf("%w：最长运行时间不能是负数（留空 = 平台上限 7 天；要限制就填 %d 到 %d 分钟）",
			ErrInvalidLauncher, MinHeadlessRunMinutes, MaxHeadlessRunMinutes)
	}
	if r.AutoResumeOKMinutes < 0 {
		return fmt.Errorf("%w：续跑成功的判定时长不能是负数（留空 = 默认 %d 分钟；要自定义就填 %d 到 %d 分钟）",
			ErrInvalidLauncher, DefaultAutoResumeOKMinutes, MinAutoResumeOKMinutes, MaxAutoResumeOKMinutes)
	}
	return nil
}

// NormalizeHeadlessRun 规整并校验一份非交互运行配置：会话页的「运行」（#128）与预设共用这套字段规则。
// 错误信息面向用户，已去掉「终端启动预设不合法」的前缀；返回的配置总是规整后的。
func NormalizeHeadlessRun(r HeadlessRun) (HeadlessRun, error) {
	r = r.normalize()
	if err := r.validate(); err != nil {
		return r, errors.New(strings.TrimPrefix(err.Error(), ErrInvalidLauncher.Error()+"："))
	}
	return r, nil
}

// SwitchLauncherToContinue 把一条「新会话 + 运行成功后切到最近会话」的预设改成「最近会话」（#191，#559）。
// 它是「比较并交换」：在一个事务里读—判—写，只有预设还在、仍是非交互续聊、对话来源仍是 new 且 after_new 仍是 continue 时才改；
// 预设被删了、或者期间被人改过（来源 / 去向已经变了），就什么都不做——手动改动永远不被系统覆盖。
// 返回改完之后的预设；switched 表示这次是不是真的改了。
func (s *Store) SwitchLauncherToContinue(ctx context.Context, id string) (TerminalLauncher, bool, error) {
	var out TerminalLauncher
	_, err := s.updateTerminalLaunchers(ctx, func(cur []TerminalLauncher) ([]TerminalLauncher, error) {
		for i, l := range cur {
			if l.ID != id {
				continue
			}
			if !l.IsHeadless() || l.Run == nil ||
				l.Run.EffectiveSource() != HeadlessSourceNew || l.Run.EffectiveAfterNew() != HeadlessAfterContinue {
				return nil, errLauncherUnchanged
			}
			run := *l.Run // 换成新的运行配置，不改原来的
			run.Source, run.AfterNew, run.NewConversation = HeadlessSourceContinue, "", false
			l.Run = &run
			out = l
			next := append([]TerminalLauncher(nil), cur...)
			next[i] = l
			return next, nil
		}
		return nil, errLauncherUnchanged
	})
	if err != nil {
		return TerminalLauncher{}, false, err
	}
	return out, out.ID != "", nil
}

// IsHeadless 表示这是一条「非交互续聊」预设。
func (l TerminalLauncher) IsHeadless() bool { return l.Kind == LauncherKindHeadless }

// validateHeadless 检查一条（已规整的）非交互续聊预设里与类型有关的部分。
func (l TerminalLauncher) validateHeadless() error {
	switch {
	case l.Command != "":
		return fmt.Errorf("%w：非交互续聊预设不用填命令（运行的命令由下面的选项拼出来）", ErrInvalidLauncher)
	case l.Guard != nil:
		return fmt.Errorf("%w：非交互续聊预设不支持会话守护", ErrInvalidLauncher)
	case l.Dir == "":
		return fmt.Errorf("%w：请填写目录：非交互续聊要在这个目录里运行（--workspace）", ErrInvalidLauncher)
	case !strings.HasPrefix(l.Dir, "/") && !IsWindowsAbsPath(l.Dir):
		return fmt.Errorf("%w：非交互续聊的目录必须是主机上的绝对路径（Linux / macOS 以 / 开头；Windows 写盘符路径如 C:\\work，或 \\\\服务器\\共享\\… 形式）", ErrInvalidLauncher)
	case IsWindowsAbsPath(l.Dir) && WindowsPathProblem(l.Dir) != "":
		return fmt.Errorf("%w：目录%s", ErrInvalidLauncher, WindowsPathProblem(l.Dir))
	case l.Run == nil:
		return fmt.Errorf("%w：非交互续聊预设缺少运行配置", ErrInvalidLauncher)
	}
	return l.Run.validate()
}
