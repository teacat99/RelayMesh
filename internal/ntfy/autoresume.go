package ntfy

import (
	"fmt"
	"strings"
)

// 「不再自动续跑」推送（#151）：预设里打开了「异常退出时自动续跑」，系统在预算用完、连续没有进展或没法提交时决定不再自己重试，
// 需要你出面时告诉你一声。
//
// 这条通道原先叫「终端会话守护」告警（D-214），同时承载「会话意外结束 / 疑似停摆」；守护在 #245 下线后只剩这一种，
// 所以事件的字段、文案都收敛成了「不再自动续跑」。消息类型的取值仍是 KindAutoResume = "guard"（用户的推送设置里按它存开关与优先级，不能改）。
// 推送从不带终端内容，只有预设名、主机名与一句原因。

const (
	// 原因在推送里最多显示这么多个字符：它来自自动续跑的判断规则，本来就是一句话，这里只是防御。
	reasonMaxLen = 160
	// 预设名在标题里最多显示这么多个字符。
	nameMaxLen = 40
)

// AutoResumeInfo 是一次「不再自动续跑」推送要用到的字段。纯数据，不依赖终端包：由接线处（api）把终端的事件翻译成它。
type AutoResumeInfo struct {
	// Key 标识「同一次放弃的同一条推送」：预设编号 + 最后出事的运行。同一个 Key 在去重窗口内只推一次。
	Key string
	// Name 是预设的名字；Machine 是机器码（主机的显示名由 Notifier 在真正发送前解析）。
	Name    string
	Machine string
	// Reason 是一句人话的原因，如「24 小时内已经自动续跑 3 次，达到上限；这次的失败原因：……」。
	Reason string
	// WorkflowID 是预设里填的工作流编号（用来在推送里写项目名）；可为空。
	WorkflowID string
}

// BuildAutoResumeMessage 生成「不再自动续跑」的推送内容。host 是主机的显示名（没解析出来就传空串）；
// merged 是此前因限流而没发出去的通知条数。
func BuildAutoResumeMessage(cfg Config, in AutoResumeInfo, host string, merged int) Message {
	name := truncateRunes(oneLine(in.Name), nameMaxLen)
	if name == "" {
		name = "终端会话"
	}

	lines := make([]string, 0, 4)
	if r := oneLine(in.Reason); r != "" {
		lines = append(lines, truncateRunes(r, reasonMaxLen))
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

	lines = append(lines, "请你看看原因再手动运行；预设里可以关掉自动续跑。")
	if merged > 0 {
		lines = append(lines, mergedNote(merged))
	}

	return Message{
		Title:    fmt.Sprintf("「%s」不再自动续跑", name),
		Body:     strings.Join(lines, "\n"),
		Priority: cfg.msgPriority(KindAutoResume),
		Tags:     []string{"no_entry_sign"},
		Click:    cfg.ClickURL,
	}
}
