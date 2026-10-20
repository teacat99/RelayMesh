// Package asyncmode 是「异步对话模式」的纯逻辑：有效模式的判定、降级原因、写给智能体的回执文案。
//
// 这里不碰数据库也不碰网络：Store 与 MCP / REST 层把当前状态（用户开关、hook 最近出现的时间）传进来，
// 拿回判定结果与要写进回执的行。这样判定规则与文案可以脱离其余系统穷举测试，也只有一处定义。
package asyncmode

import (
	"fmt"
	"hash/crc32"
	"strings"
	"time"
	"unicode/utf8"
)

// 模式名。
const (
	Sync  = "sync"
	Async = "async"
)

// HookFreshness 是「最近见过 hook」的有效窗口：异步模式靠 hook 把追加指令与答复在工作期间送到智能体手里，
// hook 太久没出现，就说明这条通路已经不可靠，此时继续让智能体「发了就走」会让用户的话没人听见，所以自动降级。
const HookFreshness = 60 * time.Minute

// Reason 是异步没有生效时的原因（生效时为空）。
type Reason string

const (
	ReasonNone      Reason = ""
	ReasonDisabled  Reason = "disabled"   // 用户开启过，现在关了
	ReasonNoHook    Reason = "no_hook"    // 开着，但这个工作流还没见过 hook
	ReasonHookStale Reason = "hook_stale" // 开着，但 hook 太久没出现
)

// Input 是判定所需的全部事实。
type Input struct {
	Enabled  bool      // 用户当前是否开着
	Ever     bool      // 是否曾经开启过
	HookSeen time.Time // 最近一次见到 hook 的时间；零值表示没见过
	Now      time.Time
}

// State 是判定结果。
type State struct {
	Enabled   bool   // 用户当前是否开着
	Ever      bool   // 是否曾经开启过（开着一定算开启过）
	Effective bool   // 真正生效：开着 且 hook 在线
	Reason    Reason // 没生效的原因；从未开启过的工作流为空
}

// Evaluate 判定有效模式：用户开启 且 最近一小时内见过 hook 才是异步；否则同步，并给出原因。
// 从未开启过的工作流不产生原因（Reason 为空）——它没有「降级」可言。
func Evaluate(in Input) State {
	st := State{Enabled: in.Enabled, Ever: in.Ever || in.Enabled}
	switch {
	case !in.Enabled:
		if st.Ever {
			st.Reason = ReasonDisabled
		}
	case in.HookSeen.IsZero():
		st.Reason = ReasonNoHook
	case in.Now.Sub(in.HookSeen) > HookFreshness:
		st.Reason = ReasonHookStale
	default:
		st.Effective = true
	}
	return st
}

// Mode 返回有效模式名。
func (s State) Mode() string {
	if s.Effective {
		return Async
	}
	return Sync
}

// ReasonText 是降级原因的人话（回执与界面共用）。
func ReasonText(r Reason) string {
	switch r {
	case ReasonDisabled:
		return "用户已关闭异步对话"
	case ReasonNoHook:
		return "异步对话已开启但暂未生效：这个工作流还没收到编辑器 hook 的事件，追加指令与答复无法在你工作时实时送达"
	case ReasonHookStale:
		return "异步对话已开启但暂未生效：编辑器 hook 已超过 60 分钟没有出现，追加指令与答复可能送不到你手里"
	default:
		return ""
	}
}

// Contract 是异步生效时回执里的 async: 契约（不超过 600 字，测试里卡死）。
// 它是智能体在异步模式下唯一的行为说明，所以把「不可逆操作仍然阻塞」（INV-3）写在最后一条。
const Contract = `async: 异步对话已生效，工作时不要为了问话而停下来等回复。
(1) 进度、阶段结论和对用户提问的回答：用 interactive_feedback 且 wait:false 发布，发完立刻继续干活。
(2) 需要用户拍板、但有默认值且可回滚的事：用 async_ask 带 default_option 提问，先按默认值做可回滚的部分；大改动先用 async_ask（kind:"plan"，task_id 为对应任务）发计划，批准前只做只读准备。
(3) 用户的追加与答复由 hook 在工具调用之间送达；回执出现 inbox: 行或想主动确认时，调用 workflow_context(action:"inbox") 读取。
(4) 部署、推送远端、删除数据、改动凭据、不可逆迁移（L2）仍须用不带 wait:false 的 interactive_feedback 阻塞确认，不能用 async_ask 绕过；auth: 行已授权的推私有库、热更生产除外。
(5) 无需切换阶段（phase 可不传），只按 auth: 行的授权做事：先读收件箱，进度用 notice 汇报，做完发总结。
(6) 收尾：不用等用户答复的任务（待办/进行中/验证中）先做完并置完成；要等用户拍板才能继续的置「暂缓」并写明在等什么，不挡收尾。之后按 window: 行等窗口走完，收件箱为空才收尾。`

// AuthLineMax 是 auth: 行的字数上限（含前缀、档位名与等级）。
const AuthLineMax = 300

// Profile 是回执 auth: 行需要的档位信息。
type Profile struct {
	Name  string
	Level string
	Rules string
}

// AuthLine 把档位写成回执里的 auth: 行；超过 AuthLineMax 时截断规则并以省略号收尾。
func AuthLine(p Profile) string {
	level := strings.TrimSpace(p.Level)
	if level == "" {
		level = "L1"
	}
	head := fmt.Sprintf("auth: %s [%s] ", strings.TrimSpace(p.Name), level)
	rules := strings.TrimSpace(p.Rules)
	room := AuthLineMax - utf8.RuneCountInString(head)
	if room < 1 {
		room = 1
	}
	if utf8.RuneCountInString(rules) > room {
		rules = string([]rune(rules)[:room-1]) + "…"
	}
	return head + rules
}

// ReceiptLines 返回回执头里的 mode 相关行与 auth 行。
//
//   - 没开启过异步的工作流：全部为空（INV-2，回执与升级之前逐字一致）；
//   - 异步生效：mode: async + 契约，auth 行取代阶段提示词；
//   - 开启过、但现在是同步：一行 mode: sync（原因），阶段提示词照旧。
func ReceiptLines(st State, p Profile) (mode []string, auth string) {
	if !st.Ever {
		return nil, ""
	}
	if st.Effective {
		return []string{"mode: async", Contract}, AuthLine(p)
	}
	line := "mode: sync"
	if text := ReasonText(st.Reason); text != "" {
		line += "（" + text + "）"
	}
	return []string{line}, ""
}

// Signature 把「智能体该知道的模式状态」压成一个短串：有效模式、降级原因、实际生效的档位（编号 + 内容摘要）。
// 它变了、而智能体还没被告知，就在下一次工具回执里补一段说明（见 mcp 层的 modeNoticeBlock）。
// 档位内容也算在内：用户改了档位的规则、换了默认档位、删掉了工作流选用的档位（回落到默认），智能体都该知道。
func Signature(st State, profileID uint, p Profile) string {
	if !st.Ever {
		return ""
	}
	h := crc32.ChecksumIEEE([]byte(p.Name + "\x00" + p.Level + "\x00" + p.Rules))
	return fmt.Sprintf("%s|%s|%d|%08x", st.Mode(), st.Reason, profileID, h)
}

// ---- 回复窗口（#123）----
//
// 异步对话里没有「挂起倒计时」：智能体发出消息就继续干活，用户什么时候回复不确定。收尾的依据是「回复窗口」：
// 智能体每发一条新消息，窗口累加一段（默认 5 分钟，封顶 30 分钟）；用户一回复，窗口清零；窗口走完还没有消息，
// 才可以走收尾。时间全部由服务端计算并写进回执（所有智能体同一口径），智能体不用自己算。

const (
	// DefaultWindowAdd 是智能体每发一条新消息、回复窗口增加的时长。
	DefaultWindowAdd = 5 * time.Minute
	// DefaultWaitSlice 是智能体每次用计时器连续等待的最长时间，到点后回头读收件箱。
	DefaultWaitSlice = 10 * time.Minute
	// WindowMax 是回复窗口从此刻起最多能累加到的长度，防止无限续命。
	WindowMax = 30 * time.Minute
)

// ExtendWindow 在当前截止时间上累加 add（已过期或没有窗口时从 now 起算），整体不超过 now+WindowMax。
func ExtendWindow(until *time.Time, now time.Time, add time.Duration) time.Time {
	base := now
	if until != nil && until.After(now) {
		base = *until
	}
	next := base.Add(add)
	if limit := now.Add(WindowMax); next.After(limit) {
		next = limit
	}
	return next
}

// WindowRemaining 返回窗口剩余时长；没有窗口返回 (0, false)；已到期返回 (0, true)。
func WindowRemaining(until *time.Time, now time.Time) (time.Duration, bool) {
	if until == nil {
		return 0, false
	}
	if left := until.Sub(now); left > 0 {
		return left, true
	}
	return 0, true
}

// FormatClock 把时长写成 mm:ss（超过 1 小时写成 h:mm:ss）。
func FormatClock(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	sec := int(d.Round(time.Second) / time.Second)
	if sec >= 3600 {
		return fmt.Sprintf("%d:%02d:%02d", sec/3600, sec%3600/60, sec%60)
	}
	return fmt.Sprintf("%02d:%02d", sec/60, sec%60)
}

// WindowState 是回复窗口此刻的全部事实。
type WindowState struct {
	// Until 是走动中的窗口截止时间；暂停（有任务）或没有窗口时为 nil。
	Until *time.Time
	// BankSec 是暂停时攒着的剩余秒数。
	BankSec int
	// ActiveTasks 是进行中 + 验证中的任务数：> 0 时窗口不走。
	ActiveTasks int
}

// Paused 窗口里还攒着时间、但因为有任务在做而没有在走。
func (w WindowState) Paused() bool { return w.ActiveTasks > 0 && w.Until == nil && w.BankSec > 0 }

// Empty 当前没有任何回复窗口（既不在走，也没有攒着）。
func (w WindowState) Empty() bool { return w.Until == nil && w.BankSec <= 0 }

// WindowLine 是回执里的 window: 行。没有窗口时返回空串。
//   - 有任务在做：窗口暂停，告诉智能体继续做任务，别为了等而停下来；必须等用户拍板的任务该置「暂缓」，
//     暂缓的不算「在做」，所以不挡窗口也不挡收尾（「非阻断任务做完才能结束会话」，#124）；
//   - 窗口还剩时间：手头没别的事就用计时器等 min(剩余, slice)，到点重读收件箱；仍无消息且未到期再按剩余等；
//   - 窗口已到期：最后读一轮收件箱，仍为空即可收尾。
func WindowLine(w WindowState, now time.Time, slice time.Duration) string {
	if w.Paused() {
		return fmt.Sprintf("window: 回复窗口已暂停（还有 %d 个进行中 / 验证中的任务，窗口只在手头没有任务时才走）。继续把任务做完，不要为了等回复而停下来；做不下去、必须等用户拍板的任务置「暂缓」并写明在等什么，它们不挡窗口也不挡收尾；任务清零后窗口从剩余 %s 接着走。", w.ActiveTasks, FormatClock(time.Duration(w.BankSec)*time.Second))
	}
	left, ok := WindowRemaining(w.Until, now)
	if !ok {
		return ""
	}
	if slice <= 0 {
		slice = DefaultWaitSlice
	}
	if left <= 0 {
		return "window: 回复窗口已到期且没有新消息。最后读一轮收件箱（workflow_context action:\"inbox\"）；仍为空就可以收尾，有消息则先处理，处理完不要急着结束。"
	}
	wait := left
	if wait > slice {
		wait = slice
	}
	ms := int(wait / time.Millisecond)
	return fmt.Sprintf("window: 回复窗口剩余 %s。手头没有别的事时，用计时器等待 %s（AwaitShell({ block_until_ms: %d })，禁止 shell sleep），到点后读收件箱；仍无消息且窗口未到期，就按新的剩余时间再等一次；窗口到期仍无消息则走收尾。用户一回复窗口就清零，收到消息直接处理，不用再等。", FormatClock(left), FormatClock(wait), ms)
}
