package ntfy

import (
	"fmt"
	"strings"
	"time"

	"github.com/teacat99/RelayMesh/v2/internal/model"
)

// 「模型运行结束」推送：模型经 ExecMesh 发起的命令跑完了（或出了状况），告诉你一声。
//
// 它解决的是长任务的问题：模型把一条要跑几个小时的命令丢到主机上就去等了，你不想一直盯着网页。
// 推送只回答三件事——哪台主机上的什么事、结局如何、用了多久；**从不带命令的输出**（输出可能含密钥，
// 也不该经过第三方服务），命令本身也只在你打开「附带摘要」时给第一行。

const (
	// 打开「附带摘要」时，命令只取第一行的这么多个字符。
	commandExcerptLen = 100
	// 主机名在推送里最多显示这么多个字符。
	hostNameMaxLen = 40
)

// RunInfo 是一次模型运行结束时推送要用到的字段。
// 取快照而不是持有运行记录：推送在别的协程里异步完成。它是纯数据，不依赖终端包——
// 由接线处（api）把终端的运行结束事件翻译成它，ntfy 因此始终是只依赖标准库与 model 的叶子包。
type RunInfo struct {
	RunID string
	// State 是运行的终态，取值见 model.Run*。
	State string
	// Machine 是机器码；主机的显示名由接线处在真正发送前解析（见 Options.HostName），免得短命令也去查库。
	Machine string
	// Description 是模型写的一句话说明；Command 是脱敏后的命令文本。
	Description string
	Command     string
	ExitCode    *int
	Signal      string
	// Why 是终态原因的中文说明（如「主机重启，进程已丢失」），没有就空着。
	Why string
	// Duration 是命令实际运行的时长（不含等批准的时间）；没跑起来的运行为 0。
	Duration         time.Duration
	WorkflowID       string
	ProjectDirectory string
	// CancelBy 是请求取消这次运行的来源（user / pause / revoked / system / model）；空表示没有任何人要求取消。
	CancelBy string
}

// WorthNotifying 判断这次运行结束要不要推送（总开关、主题等由 Notifier 另行检查）。
//
// 推送：成功、失败、超时、结果丢失，以及「没人要求取消、却被中断」（典型是主机上的守护进程停了或重启，
// 长任务因此被杀——这正是离开电脑的人最需要知道的）。
// 不推送：你或模型自己取消的（你已经知道了）；被拒绝或被闸门拒绝的（根本没运行）；运行得比门槛更短的。
func (in RunInfo) WorthNotifying(cfg Config) bool {
	if !cfg.RunNotify {
		return false
	}
	switch in.State {
	case model.RunSucceeded, model.RunFailed, model.RunTimedOut, model.RunLost:
	case model.RunCancelled:
		if in.CancelBy != "" {
			return false
		}
	default:
		return false
	}
	return in.Duration >= time.Duration(cfg.RunMinSeconds)*time.Second
}

// BuildRunMessage 生成「模型运行结束」的推送内容。
// host 是主机的显示名（没解析出来就传空串，推送里干脆不写主机，也不会拿机器码凑数）；
// merged 是此前因限流而没发出去的通知条数，大于 0 时在末尾说明一下。
func BuildRunMessage(cfg Config, in RunInfo, host string, merged int) Message {
	title, tag := runOutcome(in)
	lines := make([]string, 0, 6)

	if d := oneLine(in.Description); d != "" {
		lines = append(lines, truncateRunes(d, titleLineMaxLen))
	}
	if cfg.IncludeSummary {
		if c := firstCommandLine(in.Command); c != "" {
			lines = append(lines, "命令："+c)
		}
	}

	var where []string
	if h := oneLine(host); h != "" {
		where = append(where, "主机："+truncateRunes(h, hostNameMaxLen))
	}
	if p := projectLabel(in.ProjectDirectory, in.WorkflowID); p != "" {
		where = append(where, "项目："+p)
	}
	if len(where) > 0 {
		lines = append(lines, strings.Join(where, "　"))
	}

	lines = append(lines, "用时 "+formatDuration(in.Duration))
	if why := oneLine(in.Why); why != "" {
		lines = append(lines, "原因："+truncateRunes(why, titleLineMaxLen))
	}
	if merged > 0 {
		lines = append(lines, mergedNote(merged))
	}

	return Message{
		Title:    title,
		Body:     strings.Join(lines, "\n"),
		Priority: cfg.msgPriority(runKind(in.State)),
		Tags:     []string{tag},
		Click:    cfg.ClickURL,
	}
}

// runOutcome 给出结局对应的标题与 ntfy 标签（标签在手机上显示成表情）。
func runOutcome(in RunInfo) (title, tag string) {
	switch in.State {
	case model.RunSucceeded:
		return "模型命令已完成", "white_check_mark"
	case model.RunFailed:
		switch {
		case in.ExitCode != nil:
			return fmt.Sprintf("模型命令失败（退出码 %d）", *in.ExitCode), "x"
		case strings.TrimSpace(in.Signal) != "":
			return fmt.Sprintf("模型命令被信号终止（%s）", truncateRunes(oneLine(in.Signal), 16)), "x"
		}
		return "模型命令失败", "x"
	case model.RunTimedOut:
		return "模型命令超时，已被终止", "hourglass_flowing_sand"
	case model.RunLost:
		return "模型命令的结果已丢失", "warning"
	case model.RunCancelled:
		return "模型命令被中断", "warning"
	}
	return "模型命令已结束", "information_source"
}

// runKind 把运行的终态归到表里的消息类型：顺利完成是 KindRunOK，其余（失败 / 超时 / 丢失 / 被中断）都是 KindRunTrouble。
func runKind(state string) string {
	if state == model.RunSucceeded {
		return KindRunOK
	}
	return KindRunTrouble
}

// runPriority：出了状况的跟随默认优先级；顺利完成的最多按 3 提醒——长任务成功了不是紧急事。
// 两者都可以在「各类消息的优先级」里单独改（见 priority.go）。
func runPriority(cfg Config, state string) int {
	return cfg.msgPriority(runKind(state))
}

// firstCommandLine 取命令的第一个非空行，折叠空白并截短。
func firstCommandLine(cmd string) string {
	for _, line := range strings.Split(cmd, "\n") {
		if l := oneLine(line); l != "" {
			return truncateRunes(l, commandExcerptLen)
		}
	}
	return ""
}

// formatDuration 把时长写成人话：精度随量级走（小时以上不再报秒），不到 1 秒统一说「不到 1 秒」。
func formatDuration(d time.Duration) string {
	if d < time.Second {
		return "不到 1 秒"
	}
	secs := int64(d / time.Second)
	days, rest := secs/86400, secs%86400
	hours, rest := rest/3600, rest%3600
	mins, s := rest/60, rest%60
	switch {
	case days > 0:
		if hours > 0 {
			return fmt.Sprintf("%d 天 %d 小时", days, hours)
		}
		return fmt.Sprintf("%d 天", days)
	case hours > 0:
		if mins > 0 {
			return fmt.Sprintf("%d 小时 %d 分", hours, mins)
		}
		return fmt.Sprintf("%d 小时", hours)
	case mins > 0:
		if s > 0 {
			return fmt.Sprintf("%d 分 %d 秒", mins, s)
		}
		return fmt.Sprintf("%d 分钟", mins)
	}
	return fmt.Sprintf("%d 秒", s)
}
