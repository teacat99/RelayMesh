package terminal

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/teacat99/RelayMesh/v2/internal/model"
	"github.com/teacat99/RelayMesh/v2/internal/termproto"
)

// 非交互运行是怎么退出的（#151，D-260；起因是 #169 / #171）。
//
// 能不能分辨「正确退出」和「异常退出」，分三层看，由硬到软：
//  1. 进程层：运行记录的终态（成功 / 失败 / 你取消 / 到运行时长上限 / 被拒绝 / 丢失），以及退出码与终止它的信号；
//  2. 输出层：正常结束时 stream-json 的最后一行是 result 事件（streamjson.go 在读它，用来给对话的状态一个定论）；
//  3. 错误类别层：Cursor CLI 自己把失败分成三类，并在退出前打到标准错误上——
//     RetriableError（可重试）、ActionRequiredError（要你处理）、NonRetriableError（不可重试），
//     格式是 `<类别>: [<Connect 错误码>] <消息>`，例如 `RetriableError: [resource_exhausted] Error`。
//
// 这里做第 1、3 层的判断，是个纯函数：同样的运行记录永远得到同样的结论，所以可以拿真实运行的尾部直接写成测试。
// 第 3 层依赖 CLI 的输出格式，格式变了就认不出——认不出的一律当「未知」：只通知、不自动重试。
// 第 2 层不重复做：状态非「成功」的运行本来就不是正常结束；成功的运行即使没有 result 也不会被当成异常。

// RunExitClass 是一次已终结运行的退出类别。
type RunExitClass string

const (
	// ExitOK：正常结束（运行记录的状态是成功）。
	ExitOK RunExitClass = "ok"
	// ExitCancelled：被取消（你、暂停、撤销授权……）。取消是意图，永远不自动续跑。
	ExitCancelled RunExitClass = "cancelled"
	// ExitTimedOut：到了这次运行的最长运行时间，被主机强制结束。也是意图（预设里设的上限），不自动续跑。
	ExitTimedOut RunExitClass = "timeout"
	// ExitNotRun：被拒绝 / 被闸门否决，根本没有执行。
	ExitNotRun RunExitClass = "not_run"
	// ExitLost：运行状态丢失（主机重启或掉线）：没法确认结果，只通知。
	ExitLost RunExitClass = "lost"
	// ExitRetriable：CLI 自报「可重试」（服务端限流、连接失败……）。值得退避之后自动续跑。
	ExitRetriable RunExitClass = "retriable"
	// ExitActionRequired：CLI 自报「要你处理」（登录失效、额度用尽、需要确认……）。重试没有用，要通知你。
	ExitActionRequired RunExitClass = "action_required"
	// ExitNonRetriable：CLI 自报「不可重试」。不自动续跑。
	ExitNonRetriable RunExitClass = "non_retriable"
	// ExitCrashed：进程被信号终止。SIGKILL / SIGSEGV 这类崩溃（常见于内存不足被系统杀掉）值得续跑一次；
	// SIGTERM / SIGINT 这类像是被人或系统有意结束的不续跑。
	ExitCrashed RunExitClass = "crashed"
	// ExitUnknown：退出码非 0，但输出末尾没有认得出的 CLI 错误类别。只通知，不自动续跑。
	ExitUnknown RunExitClass = "unknown"
)

// RunExit 是对一次已终结运行的分辨结果。
type RunExit struct {
	Class RunExitClass
	// Code 是 CLI 报出的 Connect 错误码（resource_exhausted、unavailable……）；没有就空。
	Code string
	// Text 是一句中文：发生了什么、属于哪一类。
	Text string
	// Raw 是错误行的原文（取自已脱敏的输出预览，压成一行、限长）；没有就空。
	Raw string
	// Retry 表示值得自动续跑。
	Retry bool
}

// IsFailure 表示这次运行是「异常退出」：失败、崩溃、认不出原因、丢失之外的取消 / 时长上限 / 没执行不算（那是意图或没发生）。
func (e RunExit) IsFailure() bool {
	switch e.Class {
	case ExitRetriable, ExitActionRequired, ExitNonRetriable, ExitCrashed, ExitUnknown:
		return true
	}
	return false
}

var (
	// cliErrRe 认 CLI 打在标准错误上的错误行：必须顶格以类别名开头，所以 stream-json 里的事件行（以 { 开头）不会误中——
	// 智能体的回复里引用这句话也只会出现在 JSON 字符串里。
	cliErrRe = regexp.MustCompile(`^(RetriableError|ActionRequiredError|NonRetriableError):\s*(?:\[([A-Za-z_]+)\]\s*)?(.*)$`)
	// ansiRe 去掉颜色等控制序列（预览已经处理过一遍，这里是第二道，防止 CLI 的彩色输出让顶格匹配落空）。
	ansiRe = regexp.MustCompile(`\x1b\[[0-9;?]*[ -/]*[@-~]`)
)

// exitScanLines 是从输出末尾往前最多看几个非空行：CLI 的错误行在退出前打印，紧挨着末尾，留一点余量给后面可能多出来的提示行。
const exitScanLines = 40

// crashSignals 是「像是崩溃」的信号：续跑一次可能就过了。其余信号（SIGTERM / SIGINT / SIGHUP / SIGQUIT / SIGPIPE……）
// 更像是被人或系统有意结束，不自动续跑。
var crashSignals = map[string]bool{
	"SIGKILL": true, "SIGSEGV": true, "SIGABRT": true, "SIGBUS": true, "SIGILL": true, "SIGFPE": true,
}

// cliCodeText 是 Connect 错误码的中文说明（CLI 用的就是 Connect 协议的 16 个码）。
var cliCodeText = map[string]string{
	"canceled":            "请求被取消",
	"cancelled":           "请求被取消",
	"unknown":             "服务端返回了未知错误",
	"invalid_argument":    "请求参数不合法",
	"deadline_exceeded":   "请求超时",
	"not_found":           "找不到对象",
	"already_exists":      "对象已经存在",
	"permission_denied":   "没有权限",
	"resource_exhausted":  "服务端限流，或额度 / 容量用尽",
	"failed_precondition": "前置条件不满足",
	"aborted":             "请求被服务端中止",
	"out_of_range":        "参数超出范围",
	"unimplemented":       "服务端不支持这个请求",
	"internal":            "服务端内部错误",
	"unavailable":         "服务暂时不可用",
	"data_loss":           "数据丢失",
	"unauthenticated":     "登录已失效",
}

// cliKindLabel 是 CLI 三类错误的中文叫法。
var cliKindLabel = map[string]string{
	"RetriableError":      "可重试",
	"ActionRequiredError": "需要你处理",
	"NonRetriableError":   "不可重试",
}

// exitSource 取运行输出的末尾预览：输出不超过开头预览的容量时整份输出都在 PreviewHead、PreviewTail 为空。
func exitSource(rec model.TerminalRun) string {
	if strings.TrimSpace(rec.PreviewTail) != "" {
		return rec.PreviewTail
	}
	return rec.PreviewHead
}

// cliErrorLine 在运行输出的末尾找 CLI 自己打印的错误行；找到时返回类别名（RetriableError……）、错误码（小写）、消息与原行。
func cliErrorLine(preview string) (kind, code, msg, raw string, ok bool) {
	lines := strings.Split(ansiRe.ReplaceAllString(preview, ""), "\n")
	seen := 0
	for i := len(lines) - 1; i >= 0 && seen < exitScanLines; i-- {
		ln := strings.TrimSpace(lines[i])
		if ln == "" {
			continue
		}
		seen++
		if m := cliErrRe.FindStringSubmatch(ln); m != nil {
			return m[1], strings.ToLower(m[2]), strings.TrimSpace(m[3]), ln, true
		}
	}
	return "", "", "", "", false
}

// noPreviousChatsText 是「最近会话」找不到旧对话时的说明：发生了什么、可以怎么办。
const noPreviousChatsText = "没有可接着的旧对话（Cursor CLI：No previous chats found.）：这个目录里还没有对话——先把预设改成「新会话」跑一次，或先在这个目录里开过一个对话"

// hasNoPreviousChats 在运行输出的末尾找 Cursor CLI 的 `No previous chats found.`（不分大小写）：整行就是这句话（容许前后标点与空白）。
func hasNoPreviousChats(preview string) bool {
	lines := strings.Split(ansiRe.ReplaceAllString(preview, ""), "\n")
	seen := 0
	for i := len(lines) - 1; i >= 0 && seen < exitScanLines; i-- {
		ln := strings.TrimSpace(lines[i])
		if ln == "" {
			continue
		}
		seen++
		if strings.EqualFold(strings.Trim(ln, " .。"), "No previous chats found") {
			return true
		}
	}
	return false
}

// cliErrorText 把 CLI 的错误类别、错误码与消息写成一句中文。消息只有「Error」这类没有信息的词时不重复。
func cliErrorText(kind, code, msg string) string {
	label := cliKindLabel[kind]
	if label == "" {
		label = kind
	}
	reason := cliCodeText[code]
	switch {
	case reason == "" && code != "":
		reason = "错误码 " + code
	case reason == "":
		reason = "没有说明原因"
	case code != "":
		reason += "（" + code + "）"
	}
	text := fmt.Sprintf("Cursor CLI 报了「%s」的错误：%s", label, reason)
	if m := strings.TrimSpace(msg); m != "" && !strings.EqualFold(m, "error") {
		text += "：" + termproto.SanitizeText(m, 80)
	}
	return text
}

// classifyRunExit 分辨一次运行是怎么退出的。传入的应是已终结的记录；还没终结的返回空类别。
func classifyRunExit(rec model.TerminalRun) RunExit {
	switch rec.State {
	case model.RunSucceeded:
		return RunExit{Class: ExitOK, Text: "正常结束"}
	case model.RunCancelled:
		return RunExit{Class: ExitCancelled, Text: "被取消"}
	case model.RunTimedOut:
		return RunExit{Class: ExitTimedOut, Text: "达到运行时长上限，被主机终止"}
	case model.RunRejected, model.RunDenied:
		return RunExit{Class: ExitNotRun, Text: "没有执行：被拒绝或被执行闸门否决"}
	case model.RunLost:
		return RunExit{Class: ExitLost, Text: "运行状态丢失（主机重启或掉线），没法确认结果"}
	case model.RunFailed:
	default:
		return RunExit{}
	}

	// 失败：先看是不是被信号终止（硬事实），再看 CLI 自己说了什么。
	if sig := strings.ToUpper(strings.TrimSpace(rec.Signal)); sig != "" {
		retry := crashSignals[sig]
		text := "进程被信号 " + sig + " 终止"
		if retry {
			text += "（像是崩溃或被系统强杀，常见于内存不足）"
		} else {
			text += "（像是被人或系统有意结束的）"
		}
		return RunExit{Class: ExitCrashed, Text: text, Retry: retry}
	}
	// 「最近会话」（--continue，#191）在目录里没有旧对话时，Cursor CLI 只打一行 `No previous chats found.` 就以退出码 1 结束，
	// 不带错误类别：单独认出来，说清「该怎么办」；它不是重试能解决的，所以归「需要你处理」、不自动续跑。
	// 只在命令确实带 --continue 时才认：同样的字样出现在别的运行里不改分类。
	if isContinueCommand(rec.Command) && hasNoPreviousChats(exitSource(rec)) {
		return RunExit{Class: ExitActionRequired, Text: noPreviousChatsText, Raw: "No previous chats found."}
	}
	if kind, code, msg, raw, ok := cliErrorLine(exitSource(rec)); ok {
		out := RunExit{Code: code, Text: cliErrorText(kind, code, msg), Raw: oneLineClip(raw, 200)}
		switch kind {
		case "RetriableError":
			out.Class, out.Retry = ExitRetriable, true
		case "ActionRequiredError":
			out.Class = ExitActionRequired
		default:
			out.Class = ExitNonRetriable
		}
		return out
	}
	code := "未知"
	if rec.ExitCode != nil {
		code = fmt.Sprintf("%d", *rec.ExitCode)
	}
	return RunExit{Class: ExitUnknown, Text: "退出码 " + code + "，输出末尾没有认得出的 Cursor CLI 错误类别"}
}

// RunFailureView 是界面上显示的「这次运行为什么失败」：只给异常退出的运行。
type RunFailureView struct {
	// Class 是 retriable / action_required / non_retriable / crashed / unknown。
	Class string `json:"class"`
	// Code 是 CLI 报出的 Connect 错误码；没有就不出现。
	Code string `json:"code,omitempty"`
	// Text 是一句中文。
	Text string `json:"text"`
	// Raw 是错误行原文（已脱敏）。
	Raw string `json:"raw,omitempty"`
	// Retry 表示这类失败值得（自动）重试。
	Retry bool `json:"retry,omitempty"`
}

// failureViewOf 把退出分辨结果变成界面用的失败说明；不是异常退出时返回 nil。
func failureViewOf(e RunExit) *RunFailureView {
	if !e.IsFailure() {
		return nil
	}
	return &RunFailureView{Class: string(e.Class), Code: e.Code, Text: e.Text, Raw: e.Raw, Retry: e.Retry}
}
