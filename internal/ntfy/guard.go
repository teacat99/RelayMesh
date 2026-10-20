package ntfy

import (
	"fmt"
	"strings"
	"time"
)

// 「终端会话守护」推送（D-214）：由启动器预设拉起的智能体会话意外结束，或者停摆了，告诉你一声。
//
// 守护只负责「发现并告诉你」，不会自己去唤醒会话——推送里明说「未自动唤醒」，并指向一键唤醒的入口。
// 同一次事件只推一次，之后最多再提醒两次（间隔由守护服务掌握）；推送从不带终端内容，只有预设名、主机名与一句原因。

const (
	// 原因在推送里最多显示这么多个字符：它来自守护的判断规则，本来就是一句话，这里只是防御。
	guardReasonMaxLen = 160
	// 预设名在标题里最多显示这么多个字符。
	guardNameMaxLen = 40
)

// GuardKindAutoResumeGaveUp 是 GuardInfo.Kind 的一个取值：预设的「异常退出时自动续跑」决定不再续跑了（预算用完 / 连续没有进展 /
// 没法提交），需要你出面（#151）。守护事件（意外结束 / 停摆）的 Kind 为空。
const GuardKindAutoResumeGaveUp = "autoresume_gaveup"

// GuardInfo 是一次守护事件推送要用到的字段。纯数据，不依赖终端包：由接线处（api）把守护事件翻译成它。
type GuardInfo struct {
	// Kind 为空是守护事件（会话意外结束 / 智能体停摆）；GuardKindAutoResumeGaveUp 是「不再自动续跑」，标题与末尾说明不同。
	Kind string
	// Key 标识「同一次事件的同一条推送」：预设编号 + 事件标识 + 第几次提醒。同一个 Key 在去重窗口内只推一次。
	Key string
	// Name 是预设的名字；Machine 是机器码（主机的显示名由 Notifier 在真正发送前解析）。
	Name    string
	Machine string
	// Crashed 为真表示会话意外结束；为假表示会话还在、但智能体停摆了。
	Crashed bool
	// Reason 是一句人话的原因，如「上一轮对话因出错中断（6 小时前），之后没有新的活动」。
	Reason string
	// Since 是事件的起点（意外结束的时间 / 停摆计时的起点）；零值表示不详。
	Since time.Time
	// Reminder 为 0 表示首次通知；大于 0 表示这是同一次事件的第几次补发提醒。
	Reminder int
	// WorkflowID 是预设里填的工作流编号（用来在推送里写项目名）；可为空。
	WorkflowID string
}

// BuildGuardMessage 生成守护事件的推送内容。host 是主机的显示名（没解析出来就传空串）；
// merged 是此前因限流而没发出去的通知条数；now 用来把起点写成「已持续多久」。
func BuildGuardMessage(cfg Config, in GuardInfo, host string, merged int, now time.Time) Message {
	name := truncateRunes(oneLine(in.Name), guardNameMaxLen)
	if name == "" {
		name = "终端会话"
	}

	title, tag := fmt.Sprintf("「%s」疑似停摆", name), "hourglass_flowing_sand"
	if in.Crashed {
		title, tag = fmt.Sprintf("「%s」意外结束", name), "rotating_light"
	}
	if in.Kind == GuardKindAutoResumeGaveUp {
		title, tag = fmt.Sprintf("「%s」不再自动续跑", name), "no_entry_sign"
	}
	if in.Reminder > 0 {
		title = "（仍未处理）" + title
	}

	lines := make([]string, 0, 5)
	if r := oneLine(in.Reason); r != "" {
		lines = append(lines, truncateRunes(r, guardReasonMaxLen))
	}

	var where []string
	if h := oneLine(host); h != "" {
		where = append(where, "主机："+truncateRunes(h, hostNameMaxLen))
	}
	if p := projectLabel("", in.WorkflowID); p != "" {
		where = append(where, "项目："+p)
	}
	if len(where) > 0 {
		lines = append(lines, strings.Join(where, "　"))
	}

	if !in.Since.IsZero() && now.After(in.Since) {
		lines = append(lines, "已持续 "+formatDuration(now.Sub(in.Since)))
	}
	if in.Kind == GuardKindAutoResumeGaveUp {
		lines = append(lines, "请你看看原因再手动运行；预设里可以关掉自动续跑。")
	} else {
		lines = append(lines, "未自动唤醒；可在终端启动器里一键唤醒。")
	}
	if merged > 0 {
		lines = append(lines, mergedNote(merged))
	}

	return Message{
		Title:    title,
		Body:     strings.Join(lines, "\n"),
		Priority: cfg.Priority,
		Tags:     []string{tag},
		Click:    cfg.ClickURL,
	}
}
