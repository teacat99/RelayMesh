package terminal

import (
	"fmt"
	"strings"
	"time"

	"github.com/teacat99/RelayMesh/v2/internal/store"
)

// 终端会话守护（D-214）：判断一个由启动器预设拉起的智能体会话「是不是出了事」。
//
// 事故的教训是：智能体的 Cursor CLI 在一轮长对话里因网络错误中断，进程却活着、停在输入提示符上，之后 36 小时没有任何输出——
// 只盯进程退出的监控抓不到它。所以这里综合三类证据，而不是只看进程：
//   - 会话本身：还在不在、怎么结束的（退出码 / 被信号杀 / 主机重启 / 守护进程停止 / 你手动结束 / 空闲回收）；
//   - 智能体的活动：编辑器 hook 汇报的工具调用与「一轮对话怎么结束的」、智能体对 RelayMesh 的调用、等待回复时的轮询；
//   - RelayMesh 里有没有「该有人处理的事」：你已回复但没被读取、追加指令没被读取、进行中的任务。
//
// 本文件只放纯判断（evaluateGuard）：输入是调用方收集好的事实，输出是结论与理由，不碰库、不碰网络、不读时钟，
// 因此每一条判断规则都能用表驱动测试钉住。收集事实、推送、唤醒登记在 guard.go。

// GuardState 是守护对一个预设会话的判断。
type GuardState string

const (
	GuardWorking GuardState = "working" // 在干活：近期有活动（或刚收尾、还在观察）
	GuardWaiting GuardState = "waiting" // 在等你回复：合法等待，无需处理
	GuardPresent GuardState = "present" // 有人正连着，不打扰
	GuardIdle    GuardState = "idle"    // 没有待处理的事，无需处理
	GuardEnded   GuardState = "ended"   // 会话已结束，且是正常的结束（手动结束、空闲回收、命令正常退出……）
	GuardCrashed GuardState = "crashed" // 会话意外结束：需要处理
	GuardStalled GuardState = "stalled" // 会话还在，但智能体停摆了：需要处理
	GuardOffline GuardState = "offline" // 主机离线，看不到会话的情况
	GuardUnknown GuardState = "unknown" // 缺少判断依据
)

// Alerting 表示这个状态需要打扰你（推送、界面上的红点）。其余状态都是「无需处理」或「无法判断」。
func (s GuardState) Alerting() bool { return s == GuardCrashed || s == GuardStalled }

const (
	// guardErrorGrace：一轮对话以 error 收尾之后，这么久没有新的活动，就判定为中断。
	// error 是 Cursor 自己报告的「这一轮没成功」，比一般的静默更确定，所以不必等满停摆时长。
	guardErrorGrace = 5 * time.Minute
	// guardPollGrace：等待回复的轮询截止时间之后再宽限一会儿，免得刚好卡在两次轮询之间被误判。
	guardPollGrace = 2 * time.Minute
	// guardEndedFresh：会话结束超过这么久，就不再当作「刚发生的意外」去打扰。
	guardEndedFresh = 12 * time.Hour
)

// guardSession 是主机上一个存活会话的事实。
type guardSession struct {
	SID      string
	Created  time.Time
	Exited   bool
	ExitCode *int
}

// guardEnd 是一个已结束会话的事实（来自 Hub 的会话索引）。
type guardEnd struct {
	SID      string
	At       time.Time
	Reason   string
	ExitCode *int
}

// guardFeedback 是这个工作流最近一轮「交付 + 回复」的事实。
type guardFeedback struct {
	Status       string // pending / completed / cancelled / archived
	UpdatedAt    time.Time
	DeadlineAt   *time.Time
	ConsumedByAI bool
	Checks       int // 已经轮询了多少次没等到回复
	MaxChecks    int
}

// guardFacts 是判断一次所需的全部事实，由调用方（guard.go）收集。
type guardFacts struct {
	Now      time.Time
	Silence  time.Duration // 多久没有任何活动才算停摆
	HubSince time.Time     // Hub 这次启动的时间：重启之前的活动痕迹都在内存里丢了，所以从这一刻起重新计时

	HostOnline bool
	Session    *guardSession // 主机上存活的会话；nil = 没有
	Ended      *guardEnd     // 没有存活会话时，同名会话最近一次结束的记录；没有记录就是 nil
	// PresentSince 是第一次看到有人连着这个会话的时间；零值 = 现在没人连着。
	PresentSince time.Time
	// OutputAt 是服务观察到会话输出有增长的时间（零值 = 没观察到）。主机只在会话发生变化时才推送快照，
	// 所以它是个稀疏的补充信号，不能当作「一直有输出」的证据。
	OutputAt time.Time

	Link        store.AgentLink
	HookSeen    time.Time // 编辑器 hook 最近一次来询问
	AgentActive time.Time // 智能体最近一次调用 RelayMesh

	Feedback          *guardFeedback
	InboxUnread       int
	InboxOldestUnread time.Time
	DoingTasks        int
}

// GuardOpenWork 是「RelayMesh 里有没有该有人处理的事」。
type GuardOpenWork struct {
	UnreadReply bool `json:"unread_reply"` // 你已经回复了，但智能体没有读取
	PollLost    bool `json:"poll_lost"`    // 智能体在等待你回复的轮询中途没了动静
	InboxUnread int  `json:"inbox_unread"` // 没被读取的追加指令条数
	DoingTasks  int  `json:"doing_tasks"`  // 进行中的任务个数
}

// Any 表示有没有任何待处理的事。
func (w GuardOpenWork) Any() bool {
	return w.UnreadReply || w.PollLost || w.InboxUnread > 0 || w.DoingTasks > 0
}

// GuardVerdict 是一次判断的结论。
type GuardVerdict struct {
	State  GuardState `json:"state"`
	Reason string     `json:"reason"`           // 一句人话
	Detail []string   `json:"detail,omitempty"` // 判断依据，给界面的「为什么这么判断」展开用
	// Since 是进入这个状态的起点（停摆是静默的起点，意外结束是结束的时间）。
	Since *time.Time `json:"since,omitempty"`
	// Episode 标识「同一次事件」：它不变，就不会重复推送；变了才是新的事。只有需要打扰的状态才有。
	Episode string `json:"-"`
	// Cause 说明停摆是怎么发现的：error（一轮对话以出错结束）/ silence（长时间没有活动）；其他状态为空。
	Cause    string        `json:"cause,omitempty"`
	OpenWork GuardOpenWork `json:"open_work"`
}

// 停摆的发现方式（GuardVerdict.Cause）。
const (
	guardCauseError   = "error"
	guardCauseSilence = "silence"
)

func latestTime(ts ...time.Time) time.Time {
	var out time.Time
	for _, t := range ts {
		if t.After(out) {
			out = t
		}
	}
	return out
}

// guardAge 把时长写成人话（精度随量级走）。
func guardAge(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	switch mins := int(d / time.Minute); {
	case mins < 1:
		return "不到 1 分钟"
	case mins < 60:
		return fmt.Sprintf("%d 分钟", mins)
	case mins < 24*60:
		if m := mins % 60; m > 0 {
			return fmt.Sprintf("%d 小时 %d 分钟", mins/60, m)
		}
		return fmt.Sprintf("%d 小时", mins/60)
	default:
		days := mins / (24 * 60)
		if h := (mins % (24 * 60)) / 60; h > 0 {
			return fmt.Sprintf("%d 天 %d 小时", days, h)
		}
		return fmt.Sprintf("%d 天", days)
	}
}

// classifyEnd 判断一次会话结束是不是意外。原因取自 Hub 的会话索引（exit / killed / idle_ttl / policy / gone /
// host_reboot / holder_stopped / holder_exit），退出码来自进程。
func classifyEnd(reason string, code *int) (GuardState, string) {
	switch reason {
	case "killed":
		return GuardEnded, "会话被手动结束"
	case "idle_ttl":
		return GuardEnded, "会话因长时间无人操作被主机回收"
	case "policy":
		return GuardEnded, "会话被主机的策略结束"
	case "exit":
		switch {
		case code != nil && *code == 0:
			return GuardEnded, "命令正常退出（退出码 0）"
		case code != nil:
			return GuardCrashed, fmt.Sprintf("命令异常退出（退出码 %d）", *code)
		default:
			return GuardCrashed, "进程被信号终止（没有退出码）"
		}
	case "host_reboot":
		return GuardCrashed, "主机重启，会话中断"
	case "holder_stopped":
		return GuardCrashed, "主机上的守护进程被停止（维护或升级），会话中断"
	case "holder_exit":
		return GuardCrashed, "主机上的守护进程意外退出，会话中断"
	case "gone":
		return GuardCrashed, "会话从主机的会话列表里消失了（原因不明）"
	}
	return GuardCrashed, "会话结束了（原因不明）"
}

// evaluateGuard 是守护的全部判断规则。顺序就是优先级：
//
//  1. 没有存活的会话：按结束的方式分「正常结束」与「意外结束」；主机离线且没有结束记录时说「离线」。
//  2. 有人连着：不打扰（但连了超过停摆时长还一点活动没有的，当作忘了关的页面，继续往下判断）。
//  3. 一轮对话以 error 结束且之后没有新活动：中断。
//  4. 在合法地等你回复：智能体还在轮询，或轮询次数用完后停下来等你——无需处理。
//  5. 没有 hook 的任何痕迹：无法判断。
//  6. 综合「智能体最近一次活动」与「待处理的事是什么时候出现的」计时：没到停摆时长 = 在干活；到了而没有待处理的事 = 空闲；
//     到了且有待处理的事 = 停摆。
func evaluateGuard(f guardFacts) GuardVerdict {
	open := guardOpenWork(f)
	v := GuardVerdict{OpenWork: open}

	// ---- 1. 没有存活的会话 ----
	if f.Session == nil || f.Session.Exited {
		end := f.Ended
		if end == nil && f.Session != nil {
			// 会话还列在主机上、但已经退出，索引里暂时没有记录：用它自带的退出码，结束时间取现在。
			end = &guardEnd{SID: f.Session.SID, At: f.Now, Reason: "exit", ExitCode: f.Session.ExitCode}
		}
		if end == nil {
			if !f.HostOnline {
				v.State, v.Reason = GuardOffline, "主机离线，看不到会话的情况"
				return v
			}
			v.State, v.Reason = GuardUnknown, "还没有这个会话的记录（没启动过，或记录已被清理）"
			return v
		}
		state, why := classifyEnd(end.Reason, end.ExitCode)
		since := end.At
		v.Since = &since
		if state == GuardCrashed && f.Now.Sub(end.At) > guardEndedFresh {
			state, why = GuardEnded, "较早之前结束："+why
		}
		v.State, v.Reason = state, why
		if state.Alerting() {
			v.Episode = fmt.Sprintf("crashed:%s:%d", end.SID, end.At.Unix())
		}
		v.Detail = append(v.Detail, fmt.Sprintf("会话在 %s 前结束", guardAge(f.Now.Sub(end.At))))
		return v
	}

	// ---- 2. 有人连着 ----
	if !f.PresentSince.IsZero() && f.Now.Sub(f.PresentSince) < f.Silence {
		v.State, v.Reason = GuardPresent, "有人正连着这个会话"
		return v
	}

	// 智能体的各种活动痕迹。
	sessionStart := time.Time{}
	if f.Session != nil {
		sessionStart = f.Session.Created
	}
	toolOrMCP := latestTime(f.Link.LastToolAt, f.AgentActive)
	turnEnded := !f.Link.StopAt.IsZero() && !f.Link.StopAt.Before(toolOrMCP)
	hookKnown := !f.HookSeen.IsZero() || f.Link.ConversationID != "" || !f.Link.LastToolAt.IsZero() || !f.Link.StopAt.IsZero()
	lastAlive := latestTime(toolOrMCP, f.HookSeen, f.OutputAt, sessionStart, f.HubSince)
	if f.Feedback != nil && f.Feedback.Status == "pending" {
		lastAlive = latestTime(lastAlive, f.Feedback.UpdatedAt) // 最近一次轮询
	}
	if !f.PresentSince.IsZero() {
		lastAlive = latestTime(lastAlive, f.PresentSince) // 人连上来过：那一刻也算有过活动
	}

	if !f.Link.LastToolAt.IsZero() {
		v.Detail = append(v.Detail, "最近一次工具调用："+guardAge(f.Now.Sub(f.Link.LastToolAt))+"前")
	}
	if !f.AgentActive.IsZero() {
		v.Detail = append(v.Detail, "最近一次调用 RelayMesh："+guardAge(f.Now.Sub(f.AgentActive))+"前")
	}
	if turnEnded {
		v.Detail = append(v.Detail, fmt.Sprintf("一轮对话在 %s前以 %s 结束", guardAge(f.Now.Sub(f.Link.StopAt)), guardStopLabel(f.Link.StopStatus)))
	}

	// ---- 3. 出错收尾 ----
	if turnEnded && f.Link.StopStatus == store.StopError {
		since := f.Link.StopAt
		v.Since = &since
		if f.Now.Sub(since) >= guardErrorGrace {
			v.State, v.Cause = GuardStalled, guardCauseError
			v.Reason = fmt.Sprintf("上一轮对话因出错中断（%s前），之后没有新的活动", guardAge(f.Now.Sub(since)))
			v.Episode = fmt.Sprintf("stalled:error:%d", since.Unix())
			return v
		}
		v.State, v.Reason = GuardWorking, "上一轮对话刚因出错结束，正在观察是否恢复"
		return v
	}

	// ---- 4. 合法等待 ----
	pollLost := false
	if fb := f.Feedback; fb != nil && fb.Status == "pending" {
		switch {
		case fb.DeadlineAt != nil && !f.Now.After(fb.DeadlineAt.Add(guardPollGrace)):
			v.State, v.Reason = GuardWaiting, "智能体正在等你回复（仍在轮询）"
			return v
		case fb.MaxChecks > 0 && fb.Checks >= fb.MaxChecks:
			v.State, v.Reason = GuardWaiting, "智能体等了很久没有回复，已停止轮询，在等你回复"
			return v
		default:
			pollLost = true
		}
	}
	open.PollLost = pollLost
	v.OpenWork = open

	// ---- 5. 没有 hook 的任何痕迹 ----
	if !hookKnown {
		v.State = GuardUnknown
		v.Reason = "还没收到这个工作流的 hook 汇报，无法判断是否停摆（请确认项目里装了 hook、工作流编号填对了）"
		return v
	}

	// ---- 6. 综合计时 ----
	clock := lastAlive
	if t := openSince(f, open); t.After(clock) {
		clock = t
	}
	silence := f.Now.Sub(clock)
	since := clock
	if silence < f.Silence {
		v.State = GuardWorking
		if turnEnded {
			v.Reason = fmt.Sprintf("一轮对话刚在 %s前结束，正在观察", guardAge(f.Now.Sub(f.Link.StopAt)))
		} else {
			v.Reason = fmt.Sprintf("近期有活动（%s前）", guardAge(f.Now.Sub(lastAlive)))
		}
		return v
	}
	if !open.Any() {
		v.State, v.Reason = GuardIdle, "没有待处理的事（已收尾）"
		v.Since = &since
		return v
	}
	v.State, v.Cause = GuardStalled, guardCauseSilence
	v.Since = &since
	v.Reason = fmt.Sprintf("%s没有任何活动；%s", guardAge(silence), describeOpenWork(open))
	v.Episode = fmt.Sprintf("stalled:%d", since.Unix())
	return v
}

func guardStopLabel(status string) string {
	switch status {
	case store.StopCompleted:
		return "正常收尾"
	case store.StopAborted:
		return "被中止"
	case store.StopError:
		return "出错"
	}
	return "其他状态"
}

// guardOpenWork 汇总 RelayMesh 里待处理的事（轮询中断由判断过程另行补上）。
func guardOpenWork(f guardFacts) GuardOpenWork {
	w := GuardOpenWork{InboxUnread: f.InboxUnread, DoingTasks: f.DoingTasks}
	if fb := f.Feedback; fb != nil && fb.Status == "completed" && !fb.ConsumedByAI {
		w.UnreadReply = true
	}
	return w
}

// openSince 是待处理的事「出现」的时间：之后智能体才有理由做出反应，停摆计时不能早于它。
// 进行中的任务没有明确的出现时间（它们可能放了很久），返回零值。
func openSince(f guardFacts, w GuardOpenWork) time.Time {
	var t time.Time
	if w.UnreadReply && f.Feedback != nil {
		t = latestTime(t, f.Feedback.UpdatedAt)
	}
	if w.PollLost && f.Feedback != nil && f.Feedback.DeadlineAt != nil {
		t = latestTime(t, *f.Feedback.DeadlineAt)
	}
	if w.InboxUnread > 0 {
		t = latestTime(t, f.InboxOldestUnread)
	}
	return t
}

func describeOpenWork(w GuardOpenWork) string {
	var parts []string
	if w.UnreadReply {
		parts = append(parts, "你的回复还没有被读取")
	}
	if w.PollLost {
		parts = append(parts, "等待回复的轮询中途断了")
	}
	if w.InboxUnread > 0 {
		parts = append(parts, fmt.Sprintf("%d 条追加指令没有被读取", w.InboxUnread))
	}
	if w.DoingTasks > 0 {
		parts = append(parts, fmt.Sprintf("有 %d 个进行中的任务", w.DoingTasks))
	}
	return strings.Join(parts, "，")
}

// ---- 唤醒提示词 ----

const (
	// guardWakeMax 是一天内唤醒次数的参考上限：手动唤醒不受限，但超过时界面会多问一句；将来自动唤醒会硬性遵守它。
	guardWakeMax = 3
	// guardDefaultWakeBody 是默认的唤醒正文，在你手动粘贴的那句（查询最近会话，进行中就继续，已交付就沟通）上补了五件事：
	//   1. 不再断言「已触发上下文压缩」——唤醒的原因很多（进程退出、网络中断、出错停摆），原因由固定开头给出；
	//   2. 先读收件箱：你离线期间补充的话是最重要的新信息；
	//   3. 先核对现场（最近会话、进行中的任务、git 状态），不重复已完成的步骤；
	//   4. 权限沿用会话此前的授权范围（回执 auth: 行），不重新预设、不放宽——唤醒是同一场对话的延续，不是新的授权；
	//   5. 仍无法继续就说明原因并等待，不要原地重试——否则只会再被唤醒、白白烧次数。
	// 整句是一行，只含中文标点与字母数字下划线（与自定义正文同一标准）；%workflow 是这个预设里配置的工作流编号。
	guardDefaultWakeBody = "会话曾中断（可能是进程退出、网络中断或上下文被重置）。请先恢复现场：" +
		"用 RelayMesh 的 workflow_context inbox 读取工作流 %workflow 里我离线期间的追加指令，" +
		"用 list_sessions 和 task_board 查看最近会话与进行中的任务，再核对 git status 和 git log，不要重复已完成的步骤。" +
		"任务进行中就继续执行，已交付或在等我回复就通过 RelayMesh 沟通。" +
		"权限沿用本会话此前的授权范围（以回执 auth 行为准），不自行放宽，超出的先向我确认。" +
		"仍无法继续就说明原因后等待，不要重复同样的尝试。"
	// headlessRunSuffix 追加在非交互运行（守护的非交互唤醒、非交互续聊预设）的默认提示词后面（#125）：
	// 这种运行结束后没有人在终端里看输出，而「说明原因后等待」在这里恰恰等于结束本次运行——
	// 不说清楚，智能体会一句话不说就悄悄退出。自定义提示词不加（那是你自己的话）；键入终端的唤醒也不加（那里它会一直活着）。
	headlessRunSuffix = "这是一次非交互运行，没有人在终端里看你的输出，所以结果必须发到 RelayMesh：" +
		"恢复现场后先用 interactive_feedback 按回执里的异步规则发一条进度通知说明现状，结束前再发一条说明结果。" +
		"这里的等待就是结束本次运行。"
	// guardDefaultWakeBodyNoWorkflow 是预设没配工作流（或编号含 shell 特殊字符、不能写进提示词）时的版本：不点名工作流。
	guardDefaultWakeBodyNoWorkflow = "会话曾中断（可能是进程退出、网络中断或上下文被重置）。请先恢复现场：" +
		"用 RelayMesh 的 workflow_context inbox 读取我离线期间的追加指令，" +
		"用 list_sessions 和 task_board 查看最近会话与进行中的任务，再核对 git status 和 git log，不要重复已完成的步骤。" +
		"任务进行中就继续执行，已交付或在等我回复就通过 RelayMesh 沟通。" +
		"权限沿用本会话此前的授权范围（以回执 auth 行为准），不自行放宽，超出的先向我确认。" +
		"仍无法继续就说明原因后等待，不要重复同样的尝试。"
)

// guardWakeReason 给唤醒提示词里的「原因」一个很短的说法。
func guardWakeReason(v GuardVerdict) string {
	switch v.State {
	case GuardStalled:
		if v.Cause == guardCauseError {
			return "出错中断"
		}
		return "长时间无活动"
	case GuardCrashed:
		return "会话意外结束"
	case GuardEnded:
		return "会话已结束"
	}
	return "手动唤醒"
}

// renderWakePrompt 生成要输入到会话里的唤醒提示词：固定的开头（带第几次、上限、原因）+ 正文。
//
// 开头始终以「【」起头：万一提示词被输入到了一个普通的 shell 提示符（Cursor CLI 其实已经退出了），
// 第一个词也不会是任何命令。自定义正文只允许安全字符（见 store.LauncherGuard），同样不会被 shell 解释。
// 正文里的 %n、%max、%reason、%workflow 分别换成第几次、上限、原因、预设里配置的工作流编号。
// 工作流编号只校验了长度与空白，可能含 shell 特殊字符：含有时不写进提示词（%workflow 换成空串，默认正文改用不点名的版本）。
func renderWakePrompt(custom string, n, limit int, reason, workflow string) string {
	if strings.ContainsAny(workflow, store.GuardPromptUnsafe) {
		workflow = ""
	}
	body := strings.TrimSpace(custom)
	if body == "" {
		body = guardDefaultWakeBody
		if workflow == "" {
			body = guardDefaultWakeBodyNoWorkflow
		}
	}
	body = strings.NewReplacer(
		"%reason", reason, "%max", fmt.Sprint(limit), "%n", fmt.Sprint(n), "%workflow", workflow,
	).Replace(body)
	return fmt.Sprintf("【系统唤醒 %d/%d · 原因：%s】%s", n, limit, reason, body)
}
