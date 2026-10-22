package terminal

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/teacat99/RelayMesh/v2/internal/store"
)

var guardT0 = time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC)

func ago(d time.Duration) time.Time { return guardT0.Add(-d) }

func ptrTime(t time.Time) *time.Time { return &t }

func ptrInt(i int) *int { return &i }

// baseFacts 是一个「健康」的起点：主机在线，会话存活，hook 有过汇报，5 分钟前有过工具调用，没有任何待处理的事。
func baseFacts() guardFacts {
	return guardFacts{
		Now:        guardT0,
		Silence:    30 * time.Minute,
		HubSince:   ago(100 * time.Hour),
		HostOnline: true,
		Session:    &guardSession{SID: "sid-1", Created: ago(10 * time.Hour)},
		Link:       store.AgentLink{ConversationID: "conv-1", LastToolAt: ago(5 * time.Minute)},
		HookSeen:   ago(5 * time.Minute),
	}
}

func TestEvaluateGuard(t *testing.T) {
	cases := []struct {
		name    string
		mut     func(*guardFacts)
		state   GuardState
		reason  string // Reason 里应包含的片段（空 = 不检查）
		episode string // Episode 的前缀；alerting 状态必须非空，其余必须为空
	}{
		// ---- 没有存活会话 ----
		{"主机离线且没有记录", func(f *guardFacts) { f.Session, f.HostOnline = nil, false }, GuardOffline, "离线", ""},
		{"主机在线但没有记录", func(f *guardFacts) { f.Session = nil }, GuardUnknown, "还没有这个会话的记录", ""},
		{"手动结束", func(f *guardFacts) {
			f.Session, f.Ended = nil, &guardEnd{SID: "s", At: ago(time.Hour), Reason: "killed"}
		}, GuardEnded, "手动结束", ""},
		{"空闲回收", func(f *guardFacts) {
			f.Session, f.Ended = nil, &guardEnd{SID: "s", At: ago(time.Hour), Reason: "idle_ttl"}
		}, GuardEnded, "回收", ""},
		{"策略结束", func(f *guardFacts) {
			f.Session, f.Ended = nil, &guardEnd{SID: "s", At: ago(time.Hour), Reason: "policy"}
		}, GuardEnded, "策略", ""},
		{"命令正常退出", func(f *guardFacts) {
			f.Session, f.Ended = nil, &guardEnd{SID: "s", At: ago(time.Hour), Reason: "exit", ExitCode: ptrInt(0)}
		}, GuardEnded, "退出码 0", ""},
		{"命令异常退出", func(f *guardFacts) {
			f.Session, f.Ended = nil, &guardEnd{SID: "s", At: ago(time.Hour), Reason: "exit", ExitCode: ptrInt(2)}
		}, GuardCrashed, "退出码 2", "crashed:s:"},
		{"被信号终止", func(f *guardFacts) { f.Session, f.Ended = nil, &guardEnd{SID: "s", At: ago(time.Hour), Reason: "exit"} }, GuardCrashed, "信号", "crashed:s:"},
		{"主机重启", func(f *guardFacts) {
			f.Session, f.Ended = nil, &guardEnd{SID: "s", At: ago(time.Hour), Reason: "host_reboot"}
		}, GuardCrashed, "主机重启", "crashed:s:"},
		{"守护进程被停止", func(f *guardFacts) {
			f.Session, f.Ended = nil, &guardEnd{SID: "s", At: ago(time.Hour), Reason: "holder_stopped"}
		}, GuardCrashed, "被停止", "crashed:s:"},
		{"守护进程意外退出", func(f *guardFacts) {
			f.Session, f.Ended = nil, &guardEnd{SID: "s", At: ago(time.Hour), Reason: "holder_exit"}
		}, GuardCrashed, "意外退出", "crashed:s:"},
		{"会话凭空消失", func(f *guardFacts) { f.Session, f.Ended = nil, &guardEnd{SID: "s", At: ago(time.Hour), Reason: "gone"} }, GuardCrashed, "消失", "crashed:s:"},
		{"不认识的原因按意外处理", func(f *guardFacts) {
			f.Session, f.Ended = nil, &guardEnd{SID: "s", At: ago(time.Hour), Reason: "weird"}
		}, GuardCrashed, "原因不明", "crashed:s:"},
		{"意外结束超过 12 小时不再打扰", func(f *guardFacts) {
			f.Session, f.Ended = nil, &guardEnd{SID: "s", At: ago(13 * time.Hour), Reason: "host_reboot"}
		}, GuardEnded, "较早之前结束", ""},
		{"主机离线不掩盖已知的意外结束", func(f *guardFacts) {
			f.HostOnline, f.Session, f.Ended = false, nil, &guardEnd{SID: "s", At: ago(time.Hour), Reason: "host_reboot"}
		}, GuardCrashed, "主机重启", "crashed:s:"},
		{"会话还列着但已退出（索引没有记录）", func(f *guardFacts) { f.Session = &guardSession{SID: "s2", Exited: true, ExitCode: ptrInt(1)} }, GuardCrashed, "退出码 1", "crashed:s2:"},
		{"会话还列着已退出码 0", func(f *guardFacts) { f.Session = &guardSession{SID: "s2", Exited: true, ExitCode: ptrInt(0)} }, GuardEnded, "退出码 0", ""},

		// ---- 有人在场 ----
		{"有人连着", func(f *guardFacts) { f.PresentSince = ago(5 * time.Minute) }, GuardPresent, "有人", ""},
		{"忘了关的页面不算在场（没有待处理的事 → 空闲）", func(f *guardFacts) {
			f.PresentSince = ago(2 * time.Hour)
			f.Link.LastToolAt, f.HookSeen = ago(3*time.Hour), ago(3*time.Hour)
		}, GuardIdle, "没有待处理", ""},
		{"忘了关的页面不算在场（有进行中的任务 → 停摆）", func(f *guardFacts) {
			f.PresentSince = ago(2 * time.Hour)
			f.Link.LastToolAt, f.HookSeen = ago(3*time.Hour), ago(3*time.Hour)
			f.DoingTasks = 1
		}, GuardStalled, "进行中的任务", "stalled:"},

		// ---- 出错收尾 ----
		{"出错收尾超过宽限 → 停摆", func(f *guardFacts) {
			f.Link = store.AgentLink{ConversationID: "c", LastToolAt: ago(time.Hour), StopAt: ago(10 * time.Minute), StopStatus: store.StopError}
		}, GuardStalled, "出错中断", "stalled:error:"},
		{"出错收尾还在宽限内 → 观察", func(f *guardFacts) {
			f.Link = store.AgentLink{ConversationID: "c", LastToolAt: ago(time.Hour), StopAt: ago(2 * time.Minute), StopStatus: store.StopError}
		}, GuardWorking, "刚因出错结束", ""},
		{"出错之后又有工具调用 → 恢复了", func(f *guardFacts) {
			f.Link = store.AgentLink{ConversationID: "c", LastToolAt: ago(time.Minute), StopAt: ago(10 * time.Minute), StopStatus: store.StopError}
		}, GuardWorking, "近期有活动", ""},
		{"出错之后智能体又调用了 RelayMesh → 恢复了", func(f *guardFacts) {
			f.Link = store.AgentLink{ConversationID: "c", LastToolAt: ago(time.Hour), StopAt: ago(10 * time.Minute), StopStatus: store.StopError}
			f.AgentActive = ago(time.Minute)
		}, GuardWorking, "近期有活动", ""},
		{"被中止的收尾不走出错通道（无待处理 → 观察期后空闲）", func(f *guardFacts) {
			f.Link = store.AgentLink{ConversationID: "c", LastToolAt: ago(2 * time.Hour), StopAt: ago(time.Hour), StopStatus: store.StopAborted}
			f.HookSeen = ago(time.Hour)
		}, GuardIdle, "没有待处理", ""},

		// ---- 合法等待 ----
		{"等待回复：轮询中", func(f *guardFacts) {
			f.Feedback = &guardFeedback{Status: "pending", UpdatedAt: ago(time.Minute), DeadlineAt: ptrTime(guardT0.Add(4 * time.Minute)), MaxChecks: 24}
		}, GuardWaiting, "仍在轮询", ""},
		{"等待回复：截止刚过，仍在宽限内", func(f *guardFacts) {
			f.Feedback = &guardFeedback{Status: "pending", UpdatedAt: ago(8 * time.Minute), DeadlineAt: ptrTime(ago(time.Minute)), MaxChecks: 24}
		}, GuardWaiting, "仍在轮询", ""},
		{"等待回复：轮询次数用完后停下来等你（即使没有 hook 痕迹）", func(f *guardFacts) {
			f.Link, f.HookSeen = store.AgentLink{}, time.Time{}
			f.Feedback = &guardFeedback{Status: "pending", UpdatedAt: ago(5 * time.Hour), DeadlineAt: ptrTime(ago(4 * time.Hour)), Checks: 24, MaxChecks: 24}
		}, GuardWaiting, "停止轮询", ""},
		{"等待回复：轮询中途断了且很久没有活动 → 停摆", func(f *guardFacts) {
			f.Link.LastToolAt, f.HookSeen = ago(2*time.Hour), ago(2*time.Hour)
			f.Feedback = &guardFeedback{Status: "pending", UpdatedAt: ago(time.Hour), DeadlineAt: ptrTime(ago(50 * time.Minute)), Checks: 3, MaxChecks: 24}
		}, GuardStalled, "轮询中途断了", "stalled:"},
		{"等待回复：轮询刚断，还在观察期 → 在干活", func(f *guardFacts) {
			f.Link.LastToolAt, f.HookSeen = ago(12*time.Minute), ago(12*time.Minute)
			f.Feedback = &guardFeedback{Status: "pending", UpdatedAt: ago(15 * time.Minute), DeadlineAt: ptrTime(ago(10 * time.Minute)), Checks: 3, MaxChecks: 24}
		}, GuardWorking, "", ""},

		// ---- 没有 hook 痕迹 ----
		{"没有 hook 的任何痕迹 → 无法判断", func(f *guardFacts) { f.Link, f.HookSeen = store.AgentLink{}, time.Time{} }, GuardUnknown, "无法判断", ""},
		{"只有落库的对话编号也算 hook 来过", func(f *guardFacts) {
			f.Link, f.HookSeen = store.AgentLink{ConversationID: "c"}, time.Time{}
			f.Session.Created = ago(5 * time.Minute)
		}, GuardWorking, "", ""},

		// ---- 综合计时 ----
		{"近期有工具调用 → 在干活", func(f *guardFacts) { f.DoingTasks = 2 }, GuardWorking, "近期有活动", ""},
		{"静默超过时长且有进行中的任务 → 停摆", func(f *guardFacts) {
			f.Link.LastToolAt, f.HookSeen = ago(45*time.Minute), ago(45*time.Minute)
			f.DoingTasks = 2
		}, GuardStalled, "进行中的任务", "stalled:"},
		{"静默超过时长但没有待处理的事 → 空闲", func(f *guardFacts) {
			f.Link.LastToolAt, f.HookSeen = ago(45*time.Minute), ago(45*time.Minute)
		}, GuardIdle, "没有待处理", ""},
		{"你的回复没被读取且超过时长 → 停摆（计时从回复时起）", func(f *guardFacts) {
			f.Link.LastToolAt, f.HookSeen = ago(3*time.Hour), ago(3*time.Hour)
			f.Feedback = &guardFeedback{Status: "completed", UpdatedAt: ago(40 * time.Minute), ConsumedByAI: false}
		}, GuardStalled, "你的回复还没有被读取", "stalled:"},
		{"你刚回复，智能体有时间反应 → 在干活", func(f *guardFacts) {
			f.Link.LastToolAt, f.HookSeen = ago(3*time.Hour), ago(3*time.Hour)
			f.Feedback = &guardFeedback{Status: "completed", UpdatedAt: ago(5 * time.Minute), ConsumedByAI: false}
		}, GuardWorking, "", ""},
		{"回复已被读取且没有别的事 → 空闲", func(f *guardFacts) {
			f.Link.LastToolAt, f.HookSeen = ago(3*time.Hour), ago(3*time.Hour)
			f.Feedback = &guardFeedback{Status: "completed", UpdatedAt: ago(3 * time.Hour), ConsumedByAI: true}
		}, GuardIdle, "", ""},
		{"追加指令没被读取 → 停摆", func(f *guardFacts) {
			f.Link.LastToolAt, f.HookSeen = ago(3*time.Hour), ago(3*time.Hour)
			f.InboxUnread, f.InboxOldestUnread = 2, ago(50*time.Minute)
		}, GuardStalled, "2 条追加指令", "stalled:"},
		{"一轮对话刚正常结束且还有待处理的事 → 观察", func(f *guardFacts) {
			f.Link = store.AgentLink{ConversationID: "c", LastToolAt: ago(20 * time.Minute), StopAt: ago(3 * time.Minute), StopStatus: store.StopCompleted}
			f.HookSeen = ago(3 * time.Minute)
			f.DoingTasks = 1
		}, GuardWorking, "刚在", ""},
		{"一轮对话结束之后智能体又有 RelayMesh 交互 → 不算已结束", func(f *guardFacts) {
			f.Link = store.AgentLink{ConversationID: "c", LastToolAt: ago(50 * time.Minute), StopAt: ago(40 * time.Minute), StopStatus: store.StopCompleted}
			f.AgentActive, f.HookSeen = ago(2*time.Minute), ago(40*time.Minute)
			f.DoingTasks = 1
		}, GuardWorking, "近期有活动", ""},
		{"Hub 刚重启：从重启起重新计时 → 在干活", func(f *guardFacts) {
			f.Link.LastToolAt, f.HookSeen = ago(3*time.Hour), ago(3*time.Hour)
			f.HubSince = ago(10 * time.Minute)
			f.DoingTasks = 1
		}, GuardWorking, "", ""},
		{"Hub 重启超过时长仍没有活动 → 停摆", func(f *guardFacts) {
			f.Link.LastToolAt, f.HookSeen = ago(3*time.Hour), ago(3*time.Hour)
			f.HubSince = ago(31 * time.Minute)
			f.DoingTasks = 1
		}, GuardStalled, "", "stalled:"},
		{"观察到会话有新输出 → 在干活", func(f *guardFacts) {
			f.Link.LastToolAt, f.HookSeen = ago(3*time.Hour), ago(3*time.Hour)
			f.OutputAt = ago(2 * time.Minute)
			f.DoingTasks = 1
		}, GuardWorking, "", ""},
		{"会话刚创建 → 在干活", func(f *guardFacts) {
			f.Link.LastToolAt, f.HookSeen = ago(3*time.Hour), ago(3*time.Hour)
			f.Session.Created = ago(5 * time.Minute)
			f.DoingTasks = 1
		}, GuardWorking, "", ""},
	}
	for _, c := range cases {
		f := baseFacts()
		c.mut(&f)
		v := evaluateGuard(f)
		if v.State != c.state {
			t.Fatalf("%s：状态应为 %s，得到 %s（%s）\n依据：%v", c.name, c.state, v.State, v.Reason, v.Detail)
		}
		if c.reason != "" && !strings.Contains(v.Reason, c.reason) {
			t.Fatalf("%s：理由应包含「%s」，得到「%s」", c.name, c.reason, v.Reason)
		}
		if v.Reason == "" {
			t.Fatalf("%s：理由不能为空", c.name)
		}
		if c.episode == "" {
			if v.Episode != "" {
				t.Fatalf("%s：不需要打扰的状态不应有 Episode: %q", c.name, v.Episode)
			}
		} else if !strings.HasPrefix(v.Episode, c.episode) {
			t.Fatalf("%s：Episode 应以 %q 开头，得到 %q", c.name, c.episode, v.Episode)
		}
		if v.State.Alerting() != (v.Episode != "") {
			t.Fatalf("%s：需要打扰的状态必须有 Episode（反之亦然）: %s %q", c.name, v.State, v.Episode)
		}
	}
}

// 同一次停摆，Episode 不随时间变化：否则每个周期都会当成新的事件再推送一次。
func TestEvaluateGuardEpisodeIsStableWhileStalled(t *testing.T) {
	f := baseFacts()
	f.Link.LastToolAt, f.HookSeen = ago(45*time.Minute), ago(45*time.Minute)
	f.DoingTasks = 1
	first := evaluateGuard(f)
	if first.State != GuardStalled || first.Since == nil {
		t.Fatalf("应判为停摆: %+v", first)
	}
	if !first.Since.Equal(ago(45 * time.Minute)) {
		t.Fatalf("停摆的起点应是最近一次活动的时间: %v", first.Since)
	}
	for _, later := range []time.Duration{time.Minute, time.Hour, 30 * time.Hour} {
		f.Now = guardT0.Add(later)
		v := evaluateGuard(f)
		if v.State != GuardStalled || v.Episode != first.Episode {
			t.Fatalf("%v 之后 Episode 应不变: %q vs %q (%s)", later, v.Episode, first.Episode, v.State)
		}
	}
	// 智能体有了新活动再停摆，是新的一次。
	f.Now = guardT0.Add(5 * time.Hour)
	f.Link.LastToolAt = guardT0.Add(time.Hour)
	f.HookSeen = f.Link.LastToolAt
	if v := evaluateGuard(f); v.State != GuardStalled || v.Episode == first.Episode {
		t.Fatalf("新一轮的停摆应有新的 Episode: %q vs %q", v.Episode, first.Episode)
	}
}

func TestEvaluateGuardOpenWorkIsReported(t *testing.T) {
	f := baseFacts()
	f.DoingTasks, f.InboxUnread = 2, 3
	f.Feedback = &guardFeedback{Status: "completed", UpdatedAt: ago(time.Minute)}
	w := evaluateGuard(f).OpenWork
	if !w.UnreadReply || w.DoingTasks != 2 || w.InboxUnread != 3 || w.PollLost || !w.Any() {
		t.Fatalf("待处理的事汇总不对: %+v", w)
	}
	if (GuardOpenWork{}).Any() {
		t.Fatal("什么都没有时 Any 应为 false")
	}
	// 轮询中断也要出现在结论的汇总里。
	f = baseFacts()
	f.Link.LastToolAt, f.HookSeen = ago(2*time.Hour), ago(2*time.Hour)
	f.Feedback = &guardFeedback{Status: "pending", UpdatedAt: ago(time.Hour), DeadlineAt: ptrTime(ago(50 * time.Minute)), Checks: 2, MaxChecks: 24}
	if w = evaluateGuard(f).OpenWork; !w.PollLost || !w.Any() {
		t.Fatalf("轮询中断应计入待处理: %+v", w)
	}
}

func TestEvaluateGuardCauseAndDetail(t *testing.T) {
	f := baseFacts()
	f.Link = store.AgentLink{ConversationID: "c", LastToolAt: ago(time.Hour), StopAt: ago(10 * time.Minute), StopStatus: store.StopError}
	v := evaluateGuard(f)
	if v.Cause != guardCauseError {
		t.Fatalf("出错收尾的停摆 Cause 应为 error: %q", v.Cause)
	}
	if joined := strings.Join(v.Detail, "\n"); !strings.Contains(joined, "出错") || !strings.Contains(joined, "最近一次工具调用") {
		t.Fatalf("依据里应说明收尾方式与最近一次工具调用: %v", v.Detail)
	}
	f = baseFacts()
	f.Link.LastToolAt, f.HookSeen = ago(45*time.Minute), ago(45*time.Minute)
	f.DoingTasks = 1
	if v = evaluateGuard(f); v.Cause != guardCauseSilence {
		t.Fatalf("静默造成的停摆 Cause 应为 silence: %q", v.Cause)
	}
	if v = evaluateGuard(baseFacts()); v.Cause != "" {
		t.Fatalf("非停摆状态不应有 Cause: %q", v.Cause)
	}
}

func TestGuardAge(t *testing.T) {
	cases := map[time.Duration]string{
		-time.Minute:                    "不到 1 分钟",
		0:                               "不到 1 分钟",
		59 * time.Second:                "不到 1 分钟",
		time.Minute:                     "1 分钟",
		59 * time.Minute:                "59 分钟",
		time.Hour:                       "1 小时",
		time.Hour + 5*time.Minute:       "1 小时 5 分钟",
		23*time.Hour + 59*time.Minute:   "23 小时 59 分钟",
		24 * time.Hour:                  "1 天",
		26*time.Hour + 30*time.Minute:   "1 天 2 小时",
		3*24*time.Hour + 10*time.Minute: "3 天",
	}
	for d, want := range cases {
		if got := guardAge(d); got != want {
			t.Fatalf("guardAge(%v) = %q，应为 %q", d, got, want)
		}
	}
}

func TestRenderWakePrompt(t *testing.T) {
	def := renderWakePrompt("", 2, 3, "出错中断", "relaymesh-continuation")
	if !strings.HasPrefix(def, "【系统唤醒 2/3 · 原因：出错中断】") {
		t.Fatalf("开头不对: %q", def)
	}
	// 默认正文要点：读收件箱（点名工作流）、核对现场、继续或沟通、权限沿用会话此前的授权范围、无法继续就说明原因而不是重试。
	for _, want := range []string{
		"RelayMesh", "workflow_context inbox", "工作流 relaymesh-continuation 里", "list_sessions", "task_board",
		"git status", "不要重复已完成的步骤", "继续执行", "沿用本会话此前的授权范围", "auth 行为准", "不要重复同样的尝试",
	} {
		if !strings.Contains(def, want) {
			t.Fatalf("默认唤醒提示词缺少 %q: %q", want, def)
		}
	}
	if strings.Contains(def, "部署、删库") {
		t.Fatalf("唤醒提示词不该重新预设一套授权范围（用户 #80：应继承会话此前的授权）: %q", def)
	}
	if strings.Contains(def, "%") {
		t.Fatalf("占位符应全部替换: %q", def)
	}
	// 不再断言「已触发上下文压缩」：唤醒的原因很多，原因由开头给出。
	if strings.Contains(def, "已触发") {
		t.Fatalf("默认正文不应断言发生了什么: %q", def)
	}
	// 整句是一行（输入到对话框时回车就是发送），并且不能长到难以阅读。
	if strings.ContainsAny(def, "\r\n\t") {
		t.Fatalf("提示词必须是一行: %q", def)
	}
	if n := utf8.RuneCountInString(def); n > 320 {
		t.Fatalf("默认提示词不应太长（%d 个字）：%q", n, def)
	}

	// 没配工作流 / 工作流编号含 shell 特殊字符：不点名工作流，其余不变。
	for _, wf := range []string{"", "wf;rm", "a$b", "x'y"} {
		got := renderWakePrompt("", 1, 3, "手动唤醒", wf)
		if strings.Contains(got, "工作流 ") || (wf != "" && strings.Contains(got, wf)) {
			t.Fatalf("工作流 %q 不应写进提示词: %q", wf, got)
		}
		if !strings.Contains(got, "workflow_context inbox") || !strings.Contains(got, "沿用本会话此前的授权范围") {
			t.Fatalf("不点名工作流时正文其余部分应保持: %q", got)
		}
	}

	custom := renderWakePrompt("  第 %n 次，共 %max 次，因为 %reason，请继续  ", 1, 3, "会话意外结束", "wf")
	if custom != "【系统唤醒 1/3 · 原因：会话意外结束】第 1 次，共 3 次，因为 会话意外结束，请继续" {
		t.Fatalf("自定义正文的占位符没替换对: %q", custom)
	}
	if got := renderWakePrompt("去查 %workflow 的收件箱", 1, 3, "手动唤醒", "wf-9"); !strings.HasSuffix(got, "去查 wf-9 的收件箱") {
		t.Fatalf("自定义正文里的 %%workflow 应换成工作流编号: %q", got)
	}
	if got := renderWakePrompt("去查 %workflow 的收件箱", 1, 3, "手动唤醒", "a;b"); !strings.HasSuffix(got, "去查  的收件箱") {
		t.Fatalf("不安全的工作流编号换成空串: %q", got)
	}
	// 无论正文是什么，第一个字符始终是「【」：输入到普通命令行时，第一个词不会是任何命令。
	for _, body := range []string{"", "yes", "rm -rf", "ls"} {
		if got := renderWakePrompt(body, 1, 3, "手动唤醒", ""); !strings.HasPrefix(got, "【") {
			t.Fatalf("正文 %q 时开头应固定为【: %q", body, got)
		}
	}
	// 默认正文里不能有 shell 特殊字符（与自定义正文同一标准）；有编号与没编号两个版本都查。
	for name, body := range map[string]string{"点名工作流": guardDefaultWakeBody, "不点名": guardDefaultWakeBodyNoWorkflow} {
		if strings.ContainsAny(body, store.GuardPromptUnsafe) {
			t.Fatalf("默认唤醒正文（%s）含 shell 特殊字符: %q", name, body)
		}
		if strings.ContainsAny(body, "\r\n\t") {
			t.Fatalf("默认唤醒正文（%s）必须是一行", name)
		}
	}
	for _, r := range []string{"出错中断", "长时间无活动", "会话意外结束", "会话已结束", "手动唤醒"} {
		if strings.ContainsAny(r, store.GuardPromptUnsafe) {
			t.Fatalf("原因 %q 含 shell 特殊字符", r)
		}
	}
}

func TestGuardWakeReason(t *testing.T) {
	cases := []struct {
		v    GuardVerdict
		want string
	}{
		{GuardVerdict{State: GuardStalled, Cause: guardCauseError}, "出错中断"},
		{GuardVerdict{State: GuardStalled, Cause: guardCauseSilence}, "长时间无活动"},
		{GuardVerdict{State: GuardCrashed}, "会话意外结束"},
		{GuardVerdict{State: GuardEnded}, "会话已结束"},
		{GuardVerdict{State: GuardWorking}, "手动唤醒"},
		{GuardVerdict{State: GuardIdle}, "手动唤醒"},
	}
	for _, c := range cases {
		if got := guardWakeReason(c.v); got != c.want {
			t.Fatalf("%+v 的原因应为 %q，得到 %q", c.v, c.want, got)
		}
	}
}

func TestGuardStateAlerting(t *testing.T) {
	for _, s := range []GuardState{GuardWorking, GuardWaiting, GuardPresent, GuardIdle, GuardEnded, GuardDone, GuardOffline, GuardUnknown} {
		if s.Alerting() {
			t.Fatalf("%s 不应打扰", s)
		}
	}
	for _, s := range []GuardState{GuardCrashed, GuardStalled} {
		if !s.Alerting() {
			t.Fatalf("%s 应该打扰", s)
		}
	}
}
