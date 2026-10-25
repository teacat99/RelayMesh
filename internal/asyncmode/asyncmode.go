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

// Contract 是异步生效时回执里的 async: 契约（不超过 760 字，测试里卡死）。
// 它是智能体在异步模式下唯一的行为说明，所以把「不可逆操作（L2）批准前绝不执行」（INV-3）写成独立的一条：
// 用 async_ask 发 L2 确认卡、不阻断其余工作，没有答复就是不批准（#217，Q68 方案 A）。
// 上限从 600 放宽到 760，是为了写进两条工作方式（#221，用户 #859 / #834，确认项 Q73 选 a）：
// 等用户的事一律发成确认卡、不只写在汇报里（(1) 的后半句）；长耗时命令默认后台跑、等待用计时器分片（(7)）。
const Contract = `async: 异步对话已生效，工作时不要为了问话而停下来等回复。
(1) 进度、阶段结论和对用户提问的回答：用 interactive_feedback 且 wait:false 发布，发完立刻继续干活。汇报只讲进展与结论；凡是在等用户答复或拍板的事，一律发成确认卡（async_ask），不要只写在汇报里，避免被漏看。
(2) 需要用户拍板、但有默认值且可回滚的事：用 async_ask 带 default_option 提问，先按默认值做；大改动先用 async_ask（kind:"plan"，带 task_id）发计划，批准前只做只读准备。
(3) 用户的追加与答复由 hook 在工具调用之间送达；回执出现 inbox: 行或想主动确认时，调用 workflow_context(action:"inbox") 读取。
(4) 部署、推送远端、删除数据、改动凭据、不可逆迁移（L2）：用 async_ask（risk:"L2"）发确认卡，不要停下来等，先做别的；批准前绝不执行，没有答复就是不批准，相关任务置「暂缓」并写明在等 Q<N>；auth: 行已授权的推私有库、热更生产除外。
(5) 无需切换阶段（phase 可不传），只按 auth: 行的授权做事：先读收件箱，进度用 notice 汇报，做完发总结。
(6) 收尾：不用等用户答复的任务（待办/进行中/验证中）先做完并置完成；要等用户拍板才能继续的，先发确认卡（async_ask）再置「暂缓」，进展里写 Q<N>，不挡收尾。之后按 window: 行等窗口走完，收件箱为空才收尾。
(7) 长耗时命令（构建、测试、e2e、部署）默认后台启动，等待时先做不依赖结果的事；等待只用计时器分片（每片 ≤2 分钟），片间读收件箱。`

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

// ---- 严格收尾门（#232 D2）----
//
// 「做完所有非搁置任务再结束会话」：开着收尾门的会话，回复窗口只要本会话名下还有未完成（待办 / 进行中 / 验证中）
// 的任务就不走，也不能收尾；做不下去、要等用户拍板或等证据的，置「暂缓」并写明在等什么，它们不挡收尾。
// 谁的任务算「本会话名下」由存储层按指派关系判定（见 store.gateTasks），这里只管文案与数据形状。

// GateListMax 是回执里最多逐个点名的未完成任务数；更多的只给总数，避免一个积压很多的会话把每条回执撑得很长。
const GateListMax = 8

// GateViewMax 是 Web 界面里最多列出的未完成任务数（界面可以比回执长得多）。
const GateViewMax = 20

// GateTask 是挡着收尾的一个任务。
type GateTask struct {
	// ID 是任务的全局主键（REST 用，Web 上的「暂缓」快捷按钮要它）；回执里只用 Seq（#12）。
	ID    uint   `json:"id"`
	Seq   int    `json:"seq"`
	State string `json:"state"`
	Title string `json:"title"`
}

// GateNames 把挡着收尾的任务写成「#12 #15 #18 等共 11 个」：最多点名 GateListMax 个，其余只给总数。
// total 是未完成任务总数（≥ len(tasks)）；tasks 为空时返回空串。
func GateNames(tasks []GateTask, total int) string {
	n := len(tasks)
	if n > GateListMax {
		n = GateListMax
	}
	if n == 0 {
		return ""
	}
	parts := make([]string, 0, n)
	for _, t := range tasks[:n] {
		parts = append(parts, fmt.Sprintf("#%d", t.Seq))
	}
	out := strings.Join(parts, " ")
	if total > n {
		out += fmt.Sprintf(" 等共 %d 个", total)
	}
	return out
}

// GateMarker 是回执 tasks: 行末尾的收尾门提示。没有挡着的任务（或门没开 / 已放行）时返回空串。
// 它与 window: 行互补：窗口为空（从没起算或已用完）时没有 window: 行，这条仍然告诉智能体「现在还不能收尾」。
func GateMarker(tasks []GateTask, total int) string {
	if total <= 0 || len(tasks) == 0 {
		return ""
	}
	return fmt.Sprintf(" ｜ 收尾门：%d 个未完成任务挡着收尾（%s），做完置完成，要等用户拍板的先发确认卡再置暂缓", total, GateNames(tasks, total))
}

// ShelveNote 是「暂缓要写明缘由」的软提醒正文（不含任务编号）：回执 tasks: 行与 task_board overview 共用同一句话。
const ShelveNote = "等用户拍板的先发确认卡（async_ask），进展里写 Q<N>；等别的条件写明等什么、预计何时"

// ShelveMarker 是回执 tasks: 行末尾的软提醒：刚置「暂缓」、进展里却没写在等哪张确认卡 / 等什么的任务。
// 没有这样的任务时返回空串。
func ShelveMarker(tasks []GateTask, total int) string {
	if total <= 0 || len(tasks) == 0 {
		return ""
	}
	return fmt.Sprintf(" ｜ 暂缓未写缘由 %s（%s）", GateNames(tasks, total), ShelveNote)
}

// AnsweredTask 是「反向」软提醒里的一项：确认卡已经答复，挂着它的任务却还在暂缓、答复之后没人动过它。
type AnsweredTask struct {
	Seq   int    `json:"seq"`   // 任务编号（#12）
	Title string `json:"title"` // 任务标题
	Ref   string `json:"ref"`   // 已答复的确认卡（Q89）
}

// AnsweredNote 是「确认卡已答复、任务还在暂缓」的软提醒正文（不含编号）：回执 tasks: 行与 task_board overview 共用。
const AnsweredNote = "答复已在收件箱 / 确认项里：按答复接着做并置进行中，或在进展里写明为什么仍要暂缓"

// AnsweredNames 把这类任务写成「Q89→#164 Q90→#170 等共 5 个」：最多点名 GateListMax 个，其余只给总数。
func AnsweredNames(items []AnsweredTask, total int) string {
	n := len(items)
	if n > GateListMax {
		n = GateListMax
	}
	if n == 0 {
		return ""
	}
	parts := make([]string, 0, n)
	for _, it := range items[:n] {
		parts = append(parts, fmt.Sprintf("%s→#%d", it.Ref, it.Seq))
	}
	out := strings.Join(parts, " ")
	if total > n {
		out += fmt.Sprintf(" 等共 %d 个", total)
	}
	return out
}

// AnsweredMarker 是回执 tasks: 行末尾的反向软提醒：用户已经答复了确认卡，挂着它的任务却还停在「暂缓」。
// 它防的是答复被漏处理（上下文被压缩、收件箱已读已确认却没接着做）；没有这样的任务时返回空串。
func AnsweredMarker(items []AnsweredTask, total int) string {
	if total <= 0 || len(items) == 0 {
		return ""
	}
	return fmt.Sprintf(" ｜ 确认卡已答复、任务还在暂缓 %s（%s）", AnsweredNames(items, total), AnsweredNote)
}

// WindowState 是回复窗口此刻的全部事实。
type WindowState struct {
	// Until 是走动中的窗口截止时间；暂停（有任务）或没有窗口时为 nil。
	Until *time.Time
	// BankSec 是暂停时攒着的剩余秒数。
	BankSec int
	// ActiveTasks 是挡着窗口的任务数：> 0 时窗口不走。收尾门开着时是门口径的未完成任务数（待办 / 进行中 / 验证中，
	// 不按时间截断）；门关着时是老口径（进行中 / 验证中、且 3 小时内有更新）。被「本次放行」后为 0。
	ActiveTasks int
	// Strict 表示这个会话的收尾门开着（用户没有关掉）。
	Strict bool
	// Released 表示用户点了「本次放行」：窗口不再被任务挡住、照常倒计时。
	Released bool
	// Gate 是挡着收尾的任务（Strict 时有值，至多 GateViewMax 个、按编号升序）；GateTotal 是总数（≥ len(Gate)）。
	// 放行后这两项仍如实给出（界面要显示「放行中，仍有 N 个未完成」），只是不再让窗口暂停。
	Gate      []GateTask
	GateTotal int
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
	if w.Paused() && w.Strict {
		// 收尾门开着：点名挡着的任务，并把「怎样才算放下」说清楚（做完置完成；等拍板先发卡再置暂缓）。
		names := GateNames(w.Gate, w.GateTotal)
		if names == "" {
			names = fmt.Sprintf("%d 个", w.ActiveTasks)
		}
		return fmt.Sprintf("window: 回复窗口已暂停——收尾门开着，本会话还有 %d 个未完成任务挡着（%s），它们清零前窗口不走、也不能收尾。继续做完并置完成，不要为了等回复而停下来；必须等用户拍板或等外部证据的，先发确认卡（async_ask）再置「暂缓」并写明在等什么（Q<N> 或证据），暂缓的不挡窗口也不挡收尾；清零后窗口从剩余 %s 接着走。", w.ActiveTasks, names, FormatClock(time.Duration(w.BankSec)*time.Second))
	}
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
	line := fmt.Sprintf("window: 回复窗口剩余 %s。手头没有别的事时，用计时器等待 %s（AwaitShell({ block_until_ms: %d })，禁止 shell sleep），到点后读收件箱；仍无消息且窗口未到期，就按新的剩余时间再等一次；窗口到期仍无消息则走收尾。用户一回复窗口就清零，收到消息直接处理，不用再等。", FormatClock(left), FormatClock(wait), ms)
	if w.Released && w.GateTotal > 0 {
		// 用户明确放行：窗口照常倒计时，智能体要知道这是用户的决定，而不是自己的任务清零了。
		line += fmt.Sprintf("（用户已「本次放行」收尾门：虽然还有 %d 个未完成任务，窗口照常倒计时；能做完的仍请做完，其余置「暂缓」。）", w.GateTotal)
	}
	return line
}
