// Package sessionprompt 渲染「唤醒词」：把一个会话叫醒、或让一个新的智能体接上这个会话时，交给智能体的那段文字
// （#232 D3、#233，决策 D-376）。
//
// 过去这段文字有几处各自生成：非交互续聊、非交互新开对话、网页新建会话时给用户转发的启动提示词，
// 原来还有一处——键入终端的守护唤醒，它随会话守护一起下线了（#245）。
// 它们各认各的变量（有的只认 %workflow）、各带各的默认正文，所以智能体醒来后常常要先自己理解一遍现状才能开工。
// 这个包是统一的渲染器：同一套变量（只增不减，老变量含义不变；唯一的例外见 VarList）、同一套清洗规则、同一份内置默认模板；
// 「现状」类变量（未读条数、未完成任务、检查点的下一步……）由调用方在发出的那一刻现算好填进 Vars。
//
// 本包只做纯文本的渲染：不读库、不依赖任何别的业务包，所以几个调用点与预览接口可以共用，也便于单测。
package sessionprompt

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// Kind 是唤醒词的类型；不同类型的内置默认模板不同。
type Kind string

const (
	// KindResume 是非交互续聊：在主机上无人值守地继续一个已有的会话（启动器预设「非交互续聊」）。
	KindResume Kind = "resume"
	// KindNew 是非交互新开对话：全新的对话，要先带着工作流编号接入（启动器预设的「新开对话」）。
	KindNew Kind = "new"
	// KindWebStart 是网页新建会话时给用户转发的启动提示词（用户把它贴给智能体）。
	KindWebStart Kind = "web_start"
)

// Valid 判断是不是认识的类型。
func (k Kind) Valid() bool {
	switch k {
	case KindResume, KindNew, KindWebStart:
		return true
	}
	return false
}

// Label 是类型的中文名（界面与错误提示用）。
func (k Kind) Label() string {
	switch k {
	case KindResume:
		return "非交互续聊"
	case KindNew:
		return "非交互新开对话"
	case KindWebStart:
		return "网页启动提示词"
	}
	return string(k)
}

const (
	// TemplateMaxRunes 是一份模板（会话自有、全局覆盖、预设正文）的字数上限，按字符数。
	TemplateMaxRunes = 1500

	// 渲染结果的硬上限：模板限了 1500 字，但变量展开（常驻规则最长 1200 字）之后会更长；这里兜底，免得一条提示词无限变长。
	renderedMaxRunes = 6000

	// None 是取不到的「现状」变量的替换文字：唤醒不能因为某个变量取不到而被阻断。
	None = "（暂无）"
	// NotSet 是没有设置常驻规则时 %resident 的替换文字。
	NotSet = "（未设置）"

	truncatedNote = "…（已截断）"
)

// Vars 是渲染一段唤醒词用到的全部变量值。调用方负责取值；本包只做清洗与替换。
// 空字符串表示「取不到」：身份类变量（Workflow / Title / Project / Host）渲染成空，
// 状态类变量（Mode / Auth / Phase / Inbox / Tasks / Next / Anchor）渲染成「（暂无）」，Resident 渲染成「（未设置）」。
type Vars struct {
	Workflow string // %workflow 工作流编号
	Title    string // %title 工作流标题
	Project  string // %project 项目目录
	Host     string // %host 主机名
	Mode     string // %mode 异步 / 同步
	Auth     string // %auth 授权档位名
	Phase    string // %phase 当前阶段
	Resident string // %resident 会话常驻规则全文
	Inbox    string // %inbox 未读追加条数（文字，如「3」）
	Tasks    string // %tasks 未完成任务（如「3 个（#2 #3 #5）」）
	Next     string // %next 最近一次检查点的第一条「下一步」
	Anchor   string // %anchor 最近一次交付锚点（提交哈希 + 标题）

	Limit string // %limit 本次运行的最长运行时间提醒（只有预设运行才有，其他场合为空）
}

// VarInfo 描述一个变量，给界面（变量清单）与手册用。
type VarInfo struct {
	Name string `json:"name"`           // 不含 %
	Desc string `json:"desc"`           // 一句话说明
	Live bool   `json:"live,omitempty"` // 「现状」变量：发出那一刻由 Hub 现算
}

// VarList 返回全部变量，顺序即界面显示顺序。**只增不减**：老变量的名字与含义永远不变。
// 唯一的例外：%n（第几次唤醒）、%max（唤醒次数上限）、%reason（唤醒原因）三个只有键入唤醒用的变量，
// 随键入唤醒一起删了（#245）；老模板正文里残留的 %n / %max / %reason 不再替换，原样保留。
func VarList() []VarInfo {
	return []VarInfo{
		{Name: "workflow", Desc: "工作流编号"},
		{Name: "title", Desc: "工作流标题"},
		{Name: "project", Desc: "项目目录"},
		{Name: "host", Desc: "主机名"},
		{Name: "mode", Desc: "对话模式：异步 / 同步", Live: true},
		{Name: "auth", Desc: "授权档位名", Live: true},
		{Name: "phase", Desc: "当前阶段（讨论 / 方案 / 开发）", Live: true},
		{Name: "resident", Desc: "这个会话的常驻规则全文", Live: true},
		{Name: "inbox", Desc: "未读追加指令的条数", Live: true},
		{Name: "tasks", Desc: "未完成任务的个数与前几个编号", Live: true},
		{Name: "next", Desc: "最近一次检查点的第一条「下一步」", Live: true},
		{Name: "anchor", Desc: "最近一次交付锚点（提交哈希 + 标题）", Live: true},
		{Name: "limit", Desc: "本次运行的最长运行时间提醒（只有预设运行才有，其他场合为空）"},
	}
}

// SampleVars 是界面预览用的示例值：没选具体会话时（比如在全局设置里编辑模板），用它让用户看到变量大致会变成什么样。
func SampleVars() Vars {
	return Vars{
		Workflow: "wf-示例", Title: "示例会话", Project: "/home/me/project", Host: "dev-box",
		Mode: "异步对话", Auth: "标准授权 L1", Phase: "开发", Resident: "做完所有非暂缓任务再收尾",
		Inbox: "1", Tasks: "3 个（#12 #15 #20）", Next: "跑回归并推送", Anchor: "a1b2c3d 修复唤醒词",
		Limit: "本次运行最长 30 分钟，到点会被强制结束：请在那之前先向 RelayMesh 汇报进度，没做完的写清下一步。",
	}
}

// stateVars 是要查库才能知道的变量：标题 / 目录 / 主机与全部「现状」变量。工作流编号是调用方现成的。
var stateVars = []string{"title", "project", "host", "mode", "auth", "phase", "resident", "inbox", "tasks", "next", "anchor"}

// NeedsState 回答模板有没有引用要查库才能知道的变量；没有就不必为渲染付查询的代价（升级前的自定义正文大多只用编号）。
func NeedsState(tpl string) bool {
	for _, n := range stateVars {
		if strings.Contains(tpl, "%"+n) {
			return true
		}
	}
	return false
}

// matchOrder 是变量名的匹配顺序：从长到短。
// 与升级前的写法保持兼容：变量名后面不要求词边界（老正文里的「%workflow里」照样替换）。
var matchOrder = []string{
	"workflow", "resident", "project", "anchor", "inbox", "phase", "tasks", "title", "limit",
	"next", "mode", "host", "auth",
}

// 各变量值的字数上限（按字符数）。工作流编号不截断：截了就指不到那个工作流了。
const (
	maxTitleRunes   = 80
	maxProjectRunes = 200
	maxHostRunes    = 64
	maxShortRunes   = 40 // mode / auth / phase
	maxTasksRunes   = 160
	maxNextRunes    = 60
	maxLimitRunes   = 200
	maxAnchorRunes  = 100
	maxResident     = 1200
)

// Render 把模板里的 %变量 换成 v 里的值。
//
// 规则：
//   - 单遍扫描：变量值里即使出现 %xxx（比如任务标题），也不会被再次展开——标题里的内容不会变成「指令」；
//   - 不认识的 %xxx 原样保留，所以老正文里的百分号文字照常；
//   - 所有变量值先清洗（去控制字符、压缩空白、限长），Resident 之外都压成一行；
//   - 整段结果有硬上限，超出截断并注明。
func Render(tpl string, v Vars) string {
	vals := v.values()
	var b strings.Builder
	b.Grow(len(tpl) + 256)
	for i := 0; i < len(tpl); {
		j := strings.IndexByte(tpl[i:], '%')
		if j < 0 {
			b.WriteString(tpl[i:])
			break
		}
		b.WriteString(tpl[i : i+j])
		i += j
		rest := tpl[i+1:]
		matched := false
		for _, name := range matchOrder {
			if strings.HasPrefix(rest, name) {
				b.WriteString(vals[name])
				i += 1 + len(name)
				matched = true
				break
			}
		}
		if !matched {
			b.WriteByte('%')
			i++
		}
	}
	return clipFinal(strings.TrimSpace(b.String()), renderedMaxRunes)
}

// values 算出每个变量最终替换成的文字。
func (v Vars) values() map[string]string {
	one := func(s string, max int) string { return clip(oneLine(s), max) }
	status := func(s string, max int) string {
		if s = one(s, max); s != "" {
			return s
		}
		return None
	}
	resident := strings.TrimSpace(cleanControl(v.Resident))
	if resident == "" {
		resident = NotSet
	} else {
		resident = clip(resident, maxResident)
	}
	return map[string]string{
		"workflow": oneLine(v.Workflow),
		"title":    one(v.Title, maxTitleRunes),
		"project":  one(v.Project, maxProjectRunes),
		"host":     one(v.Host, maxHostRunes),
		"mode":     status(v.Mode, maxShortRunes),
		"auth":     status(v.Auth, maxShortRunes),
		"phase":    status(v.Phase, maxShortRunes),
		"resident": resident,
		"inbox":    status(v.Inbox, maxShortRunes),
		"tasks":    status(v.Tasks, maxTasksRunes),
		"next":     status(v.Next, maxNextRunes),
		"anchor":   status(v.Anchor, maxAnchorRunes),
		"limit":    one(v.Limit, maxLimitRunes),
	}
}

// cleanControl 去掉控制字符，但保留换行与制表（Resident 要保留它的分行）；回车统一成换行。
func cleanControl(s string) string {
	s = strings.ReplaceAll(strings.ReplaceAll(s, "\r\n", "\n"), "\r", "\n")
	return strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' {
			return r
		}
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, s)
}

// OneLine 把一段文字压成一行：控制字符与换行变空格，连续空白压成一个，去首尾。
// 续聊与新开对话的提示词最终是命令行里的一个参数，不能含换行（headlessCmd 会拒绝），所以渲染后统一压成一行。
func OneLine(s string) string { return oneLine(s) }

// oneLine 压成一行：控制字符与换行变空格，连续空白压成一个，去首尾。
func oneLine(s string) string {
	return strings.Join(strings.Fields(strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, s)), " ")
}

// clip 按字符数截断变量值（超出时在末尾补「…」），不会把一个汉字切成两半。max <= 0 表示不限。
func clip(s string, max int) string { return clipWith(s, max, "…") }

// clipFinal 截断整段渲染结果，末尾注明「已截断」。
func clipFinal(s string, max int) string { return clipWith(s, max, truncatedNote) }

func clipWith(s string, max int, note string) string {
	if max <= 0 || utf8.RuneCountInString(s) <= max {
		return s
	}
	rs := []rune(s)
	nr := []rune(note)
	if max <= len(nr) {
		return string(rs[:max])
	}
	return strings.TrimRight(string(rs[:max-len(nr)]), " ") + note
}
