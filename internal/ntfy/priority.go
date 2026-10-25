package ntfy

import "strings"

// 「各类消息的优先级」表（#235）。
//
// 推送的优先级过去由四套规则各管一摊：等你回复、守护告警、命令失败、测试推送用设置里唯一的那个全局优先级；
// 模型命令顺利完成最多按 3；点名、提问卡、Agent 文本等「新消息类」在代码里写死 3 / 4，根本不看设置。
// 现在把它们收成一张表：每一类消息有一个稳定的标识（Kind*）和一组内置默认——内置默认逐项复刻升级前的行为——
// 用户可以在配置的 priority_by_kind 里逐类覆盖：改成 1~5，或者设成 0 表示这一类不推送。
// 没有覆盖的类型永远走内置默认，所以升级不会改变任何人已有的推送，也不会因为以后调整内置默认而冻结在旧值上。

// 消息类型的稳定标识：它们存进配置（priority_by_kind 的键）、出现在接口里，只能新增，不要改名。
const (
	// 需要你处理。
	KindRequest      = "request"          // 智能体在等你回复（interactive_feedback 阻塞）
	KindMention      = "mention"          // 群里 @ 了你（或 @all）
	KindApproval     = "approval_waiting" // 托管终端在等你批准
	KindAskCard      = "ask_card"         // 新的待确认提问 / 方案 / 授权卡
	KindAgentText    = "agent_text"       // Agent 主动发来的文本消息
	KindAutoResume   = "guard"            // 不再自动续跑（#151）。取值沿用历史名 guard（原「终端会话守护告警」，#245 下线）：推送设置里按它存开关与优先级
	KindGroupCooling = "group_cooling"    // 群里 Agent 之间的消息过密，已暂停互相投递

	// 模型命令（ExecMesh）运行结束；总开关与最短运行时长另在「运行结束推送」里设。
	KindRunTrouble = "run_trouble" // 失败 / 超时 / 结果丢失 / 被主机中断
	KindRunOK      = "run_ok"      // 顺利完成

	// 异常：默认都不推送，打开之前不会多出任何一条推送。
	KindAgentError    = "agent_error"    // Agent 出错结束 / 被中断
	KindAgentStalled  = "agent_stalled"  // Agent 疑似卡住
	KindHostOffline   = "host_offline"   // ExecMesh 主机掉线
	KindSecurityAlert = "security_alert" // 安全告警
	KindBoardStale    = "board_stale"    // 共享任务板上有指派派出很久没人接（一次性提醒，#180 B3d）
)

// 表里的分组：只影响设置页怎么排版。
const (
	GroupNeedYou   = "need_you"
	GroupRun       = "run"
	GroupException = "exception"
)

// KindInfo 描述表里的一行：它是什么、内置默认是什么。运行时取值与设置页展示用的是同一份，
// 界面里不另存默认值，免得两边悄悄漂移。
type KindInfo struct {
	ID    string `json:"id"`
	Group string `json:"group"`
	// Label 是一句话的名字；Desc 说明它什么时候会推送。
	Label string `json:"label"`
	Desc  string `json:"desc"`
	// DefaultOn 为假表示这一类默认不推送（异常类），此时 DefaultPriority 没有意义。
	DefaultOn bool `json:"default_on"`
	// DefaultPriority 是内置默认优先级 1~5；0 表示跟随「默认优先级」（Config.Priority）。
	DefaultPriority int `json:"default_priority"`
	// CapNormal 为真表示跟随默认优先级时最多按 3（顺利完成的长任务不是紧急事）。
	CapNormal bool `json:"cap_normal,omitempty"`
	// SuggestedPriority 是默认不推送的类型被打开时，界面建议用的优先级。
	SuggestedPriority int `json:"suggested_priority,omitempty"`
}

// 表的顺序就是设置页的排列顺序。
var kindTable = []KindInfo{
	{ID: KindRequest, Group: GroupNeedYou, Label: "智能体在等你回复",
		Desc:      "智能体停下来等你答复时推送（受「延迟推送」约束：你在网页上先处理掉就不再推）。",
		DefaultOn: true},
	{ID: KindMention, Group: GroupNeedYou, Label: "群里 @ 了你",
		Desc:      "群聊里有人或 Agent @ 了你，或者 @all。",
		DefaultOn: true, DefaultPriority: 4},
	{ID: KindApproval, Group: GroupNeedYou, Label: "终端在等你批准",
		Desc:      "托管终端里的 Agent 停在「等待批准」，等了很久还没人处理时会再提醒。",
		DefaultOn: true, DefaultPriority: 4},
	{ID: KindAskCard, Group: GroupNeedYou, Label: "新的待确认提问",
		Desc:      "Agent 发来的确认卡（含方案卡、授权卡），需要你拍板。",
		DefaultOn: true, DefaultPriority: 3},
	{ID: KindAgentText, Group: GroupNeedYou, Label: "Agent 发来的文本消息",
		Desc:      "Agent 主动发来的进展或通知，不需要你回复；同一个会话里的多条会合并成一条。",
		DefaultOn: true, DefaultPriority: 3},
	{ID: KindAutoResume, Group: GroupNeedYou, Label: "不再自动续跑",
		Desc:      "预设里打开了「异常退出时自动续跑」，但预算用完、连续没有进展或没法提交，系统不会再自己重试了。",
		DefaultOn: true},
	{ID: KindGroupCooling, Group: GroupNeedYou, Label: "群聊过密，已暂停投递",
		Desc:      "群里 Agent 之间的消息太密，系统已暂停互相投递，提醒你它们在空转。",
		DefaultOn: true, DefaultPriority: 4},

	{ID: KindRunTrouble, Group: GroupRun, Label: "模型命令出状况",
		Desc:      "模型经 ExecMesh 发起的命令失败、超时、结果丢失或被主机中断（需要先打开「运行结束推送」）。",
		DefaultOn: true},
	{ID: KindRunOK, Group: GroupRun, Label: "模型命令顺利完成",
		Desc:      "命令成功跑完。设成「不推送」就只在出状况时通知你；默认最多按 3，不紧急。",
		DefaultOn: true, CapNormal: true},

	{ID: KindAgentError, Group: GroupException, Label: "Agent 出错或被中断",
		Desc:              "Agent 的状态变成「出错」或「已中断」；同一个对话 10 分钟内只推一次。",
		SuggestedPriority: 4},
	{ID: KindAgentStalled, Group: GroupException, Label: "Agent 疑似卡住",
		Desc:              "Agent 长时间没有任何动静；只有确定卡住才推，「可能卡住」只在界面里提示；每次卡住只推一次。",
		SuggestedPriority: 4},
	{ID: KindHostOffline, Group: GroupException, Label: "ExecMesh 主机掉线",
		Desc:              "已启用的主机断线超过 5 分钟还没回来；短暂的抖动不推。",
		SuggestedPriority: 4},
	{ID: KindSecurityAlert, Group: GroupException, Label: "安全告警",
		Desc:              "OAuth 授权码或刷新令牌被重放使用、解锁尝试过多被临时锁定；10 分钟内最多推一条，期间的合并计数。",
		SuggestedPriority: 5},
	{ID: KindBoardStale, Group: GroupException, Label: "指派久无人接",
		Desc:              "共享任务板上有指派派出后超过设定时长（默认 2 小时，在「外观与通知」里改）还没人接取；每个指派只提醒一次，同一个承接者的合成一条。网页里的提醒不受这一项影响，也在那里单独开关。",
		SuggestedPriority: 3},
}

var kindIndex = func() map[string]KindInfo {
	m := make(map[string]KindInfo, len(kindTable))
	for _, k := range kindTable {
		m[k.ID] = k
	}
	return m
}()

// Kinds 返回全部消息类型（按设置页的排列顺序）；返回的是副本，调用方随便改。
func Kinds() []KindInfo {
	return append([]KindInfo(nil), kindTable...)
}

// KnownKind 报告 id 是不是已登记的消息类型。
func KnownKind(id string) bool {
	_, ok := kindIndex[id]
	return ok
}

// KindLabel 返回消息类型的名字；没登记的原样返回。
func KindLabel(id string) string {
	if k, ok := kindIndex[id]; ok {
		return k.Label
	}
	return id
}

// PriorityName 是 ntfy 优先级的中文名（ntfy 自己叫 min / low / default / high / max）。
func PriorityName(p int) string {
	switch p {
	case 1:
		return "最低"
	case 2:
		return "较低"
	case 3:
		return "默认"
	case 4:
		return "较高"
	case 5:
		return "紧急"
	}
	return "默认"
}

// defaultPriority 是「默认优先级」：配置里的 Priority，没有或越界时用出厂值。
func (c Config) defaultPriority() int {
	if c.Priority < 1 || c.Priority > 5 {
		return DefaultPriority
	}
	return c.Priority
}

// PriorityFor 返回这一类消息此刻要用的优先级；on 为假表示这一类不推送。
// 取值顺序：priority_by_kind 里设了的（0 = 不推送，1~5 = 这个优先级）→ 该类的内置默认；
// 没登记的类型一律跟随默认优先级、不拦（它们不在表里，没法被关掉）。
func (c Config) PriorityFor(kind string) (priority int, on bool) {
	info, known := kindIndex[kind]
	if !known {
		return c.defaultPriority(), true
	}
	if v, set := c.PriorityByKind[kind]; set {
		if v < 1 {
			return 0, false
		}
		if v > 5 {
			v = 5
		}
		return v, true
	}
	if !info.DefaultOn {
		return 0, false
	}
	return c.builtinPriority(info), true
}

// builtinPriority 是内置默认：写死的用写死值；跟随默认优先级的跟随（顺利完成的命令最多 3）。
func (c Config) builtinPriority(info KindInfo) int {
	if info.DefaultPriority > 0 {
		return info.DefaultPriority
	}
	p := c.defaultPriority()
	if info.CapNormal && p > 3 {
		p = 3
	}
	return p
}

// TestPriorityFor 是「试发这一类」用的优先级：设成 1~5 的用它；没设、或设成不推送的，用内置默认
// （默认不推送的异常类用界面建议的值）——让人在打开一类推送之前，就能在手机上试出它的效果。
func (c Config) TestPriorityFor(kind string) int {
	info, known := kindIndex[kind]
	if !known {
		return c.defaultPriority()
	}
	if p, on := c.PriorityFor(kind); on {
		return p
	}
	if v := c.PriorityByKind[kind]; v >= 1 {
		return v
	}
	if !info.DefaultOn && info.SuggestedPriority > 0 {
		return info.SuggestedPriority
	}
	return c.builtinPriority(info)
}

// msgPriority 是各个构造器取优先级的入口：被设成不推送的类型，调用方本应在发送前就挡掉（见 Notifier 各 handle*），
// 走到这里说明是测试或防御路径，退回试发用的取值，保证报文里永远是合法的 1~5。
func (c Config) msgPriority(kind string) int {
	if p, on := c.PriorityFor(kind); on {
		return p
	}
	return c.TestPriorityFor(kind)
}

// kindEnabled 报告这一类消息是否要推送。
func (c Config) kindEnabled(kind string) bool {
	_, on := c.PriorityFor(kind)
	return on
}

// normalizePriorityByKind 复制覆盖表并丢掉没登记的类型（旧版本不认识的键、手写进库里的错键），空表归一成 nil。
func normalizePriorityByKind(in map[string]int) map[string]int {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]int, len(in))
	for k, v := range in {
		k = strings.TrimSpace(k)
		if KnownKind(k) {
			out[k] = v
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// copyPriorityByKind 返回覆盖表的副本；nil 也返回非 nil 的空表（对外视图里序列化成 {} 而不是 null）。
func copyPriorityByKind(in map[string]int) map[string]int {
	out := make(map[string]int, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

// eventPushKind 把统一通知闸的种类翻译成表里的消息类型；闸里没有对应行的返回 false。
func eventPushKind(gateKind string) (string, bool) {
	switch gateKind {
	case "mention":
		return KindMention, true
	case "approval_waiting":
		return KindApproval, true
	case "ask_card":
		return KindAskCard, true
	case "agent_text":
		return KindAgentText, true
	case "guard_alert":
		// 「不再自动续跑」走 NotifyAutoResume（KindAutoResume）；走新消息类入口的 guard_alert 只有「群聊过密」。
		return KindGroupCooling, true
	case "board_stale":
		return KindBoardStale, true
	}
	return "", false
}
