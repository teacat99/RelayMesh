package store

import (
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
	MaxHeadlessPromptRunes = 600
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
// 点「运行」时，Hub 经主机的执行通道运行（与 ExecMesh、会话守护的非交互唤醒同一条通道、同一套闸门）：
//
//	<bin> -p --resume <对话编号> --approve-mcps --trust [--model <模型>] [--force] --workspace <目录> --output-format stream-json '<提示词>'
//
// 新开对话（NewConversation 且还不知道对话编号）时命令不带 `--resume <对话编号>`，其余相同。
//
// 命令只由这些已校验的字段拼出，所有动态部分都经过单引号包裹；预设不能写任意命令，所以改配置不会变成任意命令执行。
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
	// NewConversation 打开后，这个预设在「还不知道要接哪个对话」（预设里没有固定对话编号、这个工作流里也没有智能体接入过）时
	// 不再报错，而是新开一个对话（#176 第三期）：命令不带 --resume，提示词换成「接入工作流」版本，
	// 对话编号从这次运行输出的 init 事件里学到并记下来——之后的运行就是续聊，所以它只会新开一次。
	// 默认关：不打开它，没有对话编号的预设照旧报错，不会悄悄新开。固定了对话编号（ConversationID）就不能再选新开：二选一。
	NewConversation bool `json:"new_conversation,omitempty"`
	// Origin 标记预设是怎么来的：空 = 手工建的；HeadlessOriginEnableAgent = 会话页「启用 Agent」自动建的（#176）。
	// 只用来在启动器清单里给它打标记、以及让「启用 Agent」再次点击时复用同一条预设，不改变运行行为。
	Origin string `json:"origin,omitempty"`
}

// HeadlessOriginEnableAgent 是 HeadlessRun.Origin 的取值：这条预设是会话页「启用 Agent」自动建的。
const HeadlessOriginEnableAgent = "enable_agent"

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
		case !headlessBinRe.MatchString(r.Bin) || IsHeadlessPrefixCmd(r.Bin):
			return fmt.Errorf("%w：可执行文件只能是命令名或路径（字母、数字和 _ . / ~ + -），不能是 cd、env、sudo、bash 这类前缀命令或 shell", ErrInvalidLauncher)
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
	if r.WorkflowID == "" && r.ConversationID == "" {
		return fmt.Errorf("%w：请填工作流编号（用来找到智能体最近的对话），或者直接固定一个对话编号", ErrInvalidLauncher)
	}
	if r.NewConversation {
		switch {
		case r.ConversationID != "":
			return fmt.Errorf("%w：固定了对话编号就不能再选「新开对话」：要接着这个对话，就关掉新开；要新开，就清空对话编号", ErrInvalidLauncher)
		case r.WorkflowID == "":
			return fmt.Errorf("%w：「新开对话」需要填工作流编号：新对话靠它接入 RelayMesh，之后的运行也靠它找到这个对话", ErrInvalidLauncher)
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
	case !strings.HasPrefix(l.Dir, "/"):
		return fmt.Errorf("%w：非交互续聊的目录必须是主机上的绝对路径（以 / 开头）", ErrInvalidLauncher)
	case l.Run == nil:
		return fmt.Errorf("%w：非交互续聊预设缺少运行配置", ErrInvalidLauncher)
	}
	return l.Run.validate()
}
