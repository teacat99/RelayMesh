// Package notify 是统一通知闸的核心：所有「要不要打扰你」的判断都在这里做一次。
//
// 本包只有纯逻辑（事件、偏好、决策）与一个很薄的 Gate（见 gate.go）：它不依赖数据库、不依赖 ntfy，
// 也不关心浏览器怎么呈现——「值不值得通知你」由服务端决定，「在这台设备上怎么弹」由浏览器决定。
// 设计依据：#111 设计稿 07-notifications。
package notify

import (
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// Kind 是通知的种类，决定它是否穿透免打扰 / 会话静音，以及走哪些通道。
type Kind string

const (
	// 穿透类：没有你就停在那里，或点名找你，不受任何免打扰 / 静音影响。
	KindBlockingFeedback Kind = "blocking_feedback" // interactive_feedback 阻塞等你
	KindMention          Kind = "mention"           // 群里 @你 / @all（N-2：@all 也穿透）
	KindApprovalWaiting  Kind = "approval_waiting"  // 托管终端等你批准
	KindGuardAlert       Kind = "guard_alert"       // 不再自动续跑（#151）/ 群聊过密；取值沿用历史名（原「终端会话守护告警」）

	// 受约束类：免打扰与会话策略都能让它安静。
	KindAgentText Kind = "agent_text" // Agent 主动发的文本：notice、chat send、群文本
	KindAskCard   Kind = "ask_card"   // async_ask 带默认值的提问 / 授权卡
	KindRunEnd    Kind = "run_end"    // ExecMesh 运行结束（只有 ntfy 通道）
	// KindBoardStale 是共享任务板上「指派派出后很久没人接」的一次性提醒（#180 B3d）：给用户看的，不属于任何会话。
	// 受免打扰约束；ntfy 通道默认不推（在「各类消息的优先级」里打开才推）。
	KindBoardStale Kind = "board_stale"

	// 永不通知：任务 / 进展更新与系统行只在界面里体现。
	KindTaskUpdate  Kind = "task_update"
	KindSystemEvent Kind = "system_event"
)

// kindSpec 描述一个 kind 的固有性质（与偏好无关）。
type kindSpec struct {
	penetrate bool // 穿透免打扰与会话静音
	never     bool // 任何情况下都不通知
	browser   bool // 有浏览器通道
	ntfy      bool // 有 ntfy 通道
}

var kindSpecs = map[Kind]kindSpec{
	KindBlockingFeedback: {penetrate: true, browser: true, ntfy: true},
	KindMention:          {penetrate: true, browser: true, ntfy: true},
	KindApprovalWaiting:  {penetrate: true, browser: true, ntfy: true},
	KindGuardAlert:       {penetrate: true, browser: true, ntfy: true},
	KindAgentText:        {browser: true, ntfy: true},
	KindAskCard:          {browser: true, ntfy: true},
	KindRunEnd:           {ntfy: true},
	KindBoardStale:       {browser: true, ntfy: true},
	KindTaskUpdate:       {never: true},
	KindSystemEvent:      {never: true},
}

// Known 报告 k 是不是已登记的种类。
func (k Kind) Known() bool {
	_, ok := kindSpecs[k]
	return ok
}

// Penetrates 报告 k 是否属于穿透类。
func (k Kind) Penetrates() bool { return kindSpecs[k].penetrate }

// 各字段的长度上限（按字符数）。
const (
	// MaxPreviewRunes 是预览的最大字数（设计 §7：≤ 120 字）。
	MaxPreviewRunes = 120
	maxTitleRunes   = 80
	maxSenderRunes  = 40
	maxIDBytes      = 200
	maxLinkBytes    = 512
)

// Event 是一条要交给闸的通知。ID 全局唯一，是幂等键：同一个 ID 至多通知一次。
type Event struct {
	ID           string    `json:"id"`
	Kind         Kind      `json:"kind"`
	Conversation string    `json:"conversation"`            // 1:1 = "wf:<workflow_id>"，群 = "grp:<id>"
	WorkflowID   string    `json:"workflow_id,omitempty"`   // 深链 / 回执定位用
	SessionID    string    `json:"session_id,omitempty"`    // 深链定位到具体那条
	Sender       string    `json:"sender,omitempty"`        // 发送者显示名（Agent 身份标签）
	Title        string    `json:"title,omitempty"`         // 会话名（项目名 / 群名）
	Mention      bool      `json:"mention,omitempty"`       // 这条消息点名了你（对 agent_text 生效，等同 mention）
	Preview      string    `json:"preview,omitempty"`       // 预览正文（已折成一行并截断）
	DeepLink     string    `json:"deep_link,omitempty"`     // 站内路径（以 / 开头），点击通知时打开
	CrossProject bool      `json:"cross_project,omitempty"` // 来自跨项目的群：ntfy 默认隐藏预览
	CreatedAt    time.Time `json:"created_at"`
}

// ConversationOf 返回 1:1 会话（按工作流）的会话 id。
func ConversationOf(workflowID string) string { return "wf:" + strings.TrimSpace(workflowID) }

// Normalize 返回规整后的事件：文本字段折成一行并按字符数截断，深链只接受站内路径，缺省字段补齐。
// 它不改 ID 与 Kind（它们由调用方保证，见 Valid）。
func (e Event) Normalize(now time.Time) Event {
	e.ID = strings.TrimSpace(e.ID)
	e.WorkflowID = strings.TrimSpace(e.WorkflowID)
	e.SessionID = strings.TrimSpace(e.SessionID)
	e.Conversation = strings.TrimSpace(e.Conversation)
	if e.Conversation == "" && e.WorkflowID != "" {
		e.Conversation = ConversationOf(e.WorkflowID)
	}
	e.Sender = clipRunes(oneLine(e.Sender), maxSenderRunes)
	e.Title = clipRunes(oneLine(e.Title), maxTitleRunes)
	e.Preview = clipRunes(oneLine(e.Preview), MaxPreviewRunes)
	e.DeepLink = cleanDeepLink(e.DeepLink)
	if e.CreatedAt.IsZero() {
		e.CreatedAt = now
	}
	return e
}

// Valid 检查事件是否可以进闸：必须有 ID 与已登记的 Kind。
func (e Event) Valid() bool {
	return e.ID != "" && len(e.ID) <= maxIDBytes && e.Kind.Known()
}

// oneLine 把任意空白（含换行）折成单个空格。
func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

// clipRunes 按字符数截断；超长时末尾用省略号标记（省略号计入上限）。
func clipRunes(s string, max int) string {
	if max <= 0 || utf8.RuneCountInString(s) <= max {
		return s
	}
	r := []rune(s)
	return strings.TrimSpace(string(r[:max-1])) + "…"
}

// cleanDeepLink 只接受站内路径：以单个 / 开头，不含空白与控制字符，长度有限。
// 协议相对地址（//host）与带协议的地址一律丢弃——深链最终会被拼到对外地址后面，不能让它指向别处。
func cleanDeepLink(s string) string {
	s = strings.TrimSpace(s)
	if s == "" || len(s) > maxLinkBytes || s[0] != '/' || strings.HasPrefix(s, "//") || strings.HasPrefix(s, `/\`) {
		return ""
	}
	for _, r := range s {
		if unicode.IsSpace(r) || unicode.IsControl(r) {
			return ""
		}
	}
	return s
}
