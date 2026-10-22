package api

import (
	"context"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/teacat99/RelayMesh/v2/internal/mcp"
	"github.com/teacat99/RelayMesh/v2/internal/model"
	"github.com/teacat99/RelayMesh/v2/internal/notify"
	"github.com/teacat99/RelayMesh/v2/internal/ntfy"
	"github.com/teacat99/RelayMesh/v2/internal/store"
	"github.com/teacat99/RelayMesh/v2/internal/terminal"
)

// 统一通知闸的接线层（#115，设计稿 07 §3）。
//
// 各个通知来源（MCP 的阻塞待答 / notice / async_ask、终端的运行结束 / 守护告警）只交出自己的事实，
// 这里把它们翻译成 notify.Event，交给闸按偏好决策；放行之后，浏览器通道由闸直接发 SSE notify，
// ntfy 通道由这里的回调交给 ntfy.Notifier。notify 与 ntfy 两个包互不依赖，翻译与接线都在这一层。
//
// 老三类（阻塞待答 / 运行结束 / 守护告警）的 ntfy 通道仍然走它们原来的入口（NotifyRequest / NotifyRun / NotifyGuard），
// 构造器、延迟与复核、去重与限流一个字没动；新来源（notice / async_ask）走 NotifyEvent。

// 查名字（发送者标签、项目名）的数据库查询最多等这么久：它只是通知里的一个字段，不值得拖住工具调用。
const notifyLookupTimeout = 2 * time.Second

type gateNotifier struct {
	gate  *notify.Gate
	ntfy  *ntfy.Notifier
	store *store.Store
	// broadcast 把 Web 事件推给所有连接的浏览器（SSE）；nil 时屏幕事件只发通知、不推状态变化。
	broadcast func(eventType string, data any)
}

func newGateNotifier(gate *notify.Gate, n *ntfy.Notifier, st *store.Store) *gateNotifier {
	return &gateNotifier{gate: gate, ntfy: n, store: st}
}

// NotifyRequest 是阻塞待答（有人在等你）：穿透类。实现 mcp.RequestNotifier。
func (g *gateNotifier) NotifyRequest(s *model.FeedbackSession) {
	if g == nil || s == nil || s.ID == "" {
		return
	}
	snap := *s // 会话对象随后还会被修改；老的 ntfy 入口自己也会再复制一次
	ev := notify.Event{
		ID:           "blk:" + s.ID,
		Kind:         notify.KindBlockingFeedback,
		Conversation: conversationOf(s.WorkflowID),
		WorkflowID:   s.WorkflowID,
		SessionID:    s.ID,
		Title:        projectTitle(s.ProjectDirectory, s.WorkflowID),
		Preview:      s.Title, // 与升级前的浏览器通知一致：正文是这条请求的标题
		DeepLink:     deepLinkTo(s.WorkflowID, s.ID),
	}
	g.gate.Publish(ev, func(notify.Event, notify.Decision) { g.ntfy.NotifyRequest(&snap) })
}

// NotifyNotice 是非阻塞的 notice 文本（interactive_feedback 的 wait:false）：agent_text，受免打扰与会话静音约束。
// 实现 mcp.EventNotifier。
func (g *gateNotifier) NotifyNotice(s *model.FeedbackSession) {
	if g == nil || s == nil || s.ID == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), notifyLookupTimeout)
	defer cancel()
	title, sender := g.names(ctx, s.WorkflowID, s.ProjectDirectory)
	ev := notify.Event{
		ID:           "txt:" + s.ID,
		Kind:         notify.KindAgentText,
		Conversation: conversationOf(s.WorkflowID),
		WorkflowID:   s.WorkflowID,
		SessionID:    s.ID,
		Sender:       sender,
		Title:        title,
		Preview:      noticePreview(s.Title, s.Summary),
		DeepLink:     deepLinkTo(s.WorkflowID, s.ID),
	}
	g.gate.Publish(ev, g.pushEvent)
}

// NotifyAsk 是 async_ask 新建的确认卡：ask_card，受免打扰与会话静音约束。实现 mcp.EventNotifier。
func (g *gateNotifier) NotifyAsk(item *model.ConfirmItem) {
	if g == nil || item == nil || item.WorkflowID == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), notifyLookupTimeout)
	defer cancel()
	title, sender := g.names(ctx, item.WorkflowID, "")
	preview := strings.TrimSpace(item.Title)
	if preview == "" {
		preview = notify.Excerpt(item.Body)
	}
	ev := notify.Event{
		ID:           "ask:" + item.WorkflowID + ":" + model.ConfirmRef(item.Seq),
		Kind:         notify.KindAskCard,
		Conversation: conversationOf(item.WorkflowID),
		WorkflowID:   item.WorkflowID,
		Sender:       sender,
		Title:        title,
		Preview:      preview,
		DeepLink:     deepLinkTo(item.WorkflowID, ""),
	}
	g.gate.Publish(ev, g.pushEvent)
}

// NotifyGroup 是群里 Agent 的发言（实现 mcp.GroupNotifier）：文本按 agent_text（受免打扰与群静音约束），
// 点名了你（或 @all）时升格为 mention（穿透）；冷却被触发时发一条穿透的告警，告诉你 Agent 们在空转。
func (g *gateNotifier) NotifyGroup(n mcp.GroupNotice) {
	if g == nil || strings.TrimSpace(n.GroupID) == "" {
		return
	}
	if n.CoolingStarted {
		g.gate.Publish(notify.Event{
			ID:           "grd:cool:" + n.GroupID + ":" + n.MessageID,
			Kind:         notify.KindGuardAlert,
			Conversation: n.GroupID,
			Title:        n.GroupTitle,
			Preview:      "群里 Agent 之间的消息太密，已暂停互相投递",
			DeepLink:     groupDeepLink(n.GroupID),
		}, g.pushEvent)
	}
	g.gate.Publish(notify.Event{
		ID:           "grp:" + n.GroupID + ":" + n.MessageID,
		Kind:         notify.KindAgentText,
		Conversation: n.GroupID,
		Mention:      n.MentionsHuman,
		Sender:       n.SenderLabel,
		Title:        n.GroupTitle,
		Preview:      notify.Excerpt(n.Text),
		DeepLink:     groupDeepLink(n.GroupID),
	}, g.pushEvent)
}

// groupDeepLink 打开群聊页。
func groupDeepLink(groupID string) string { return "/group/" + url.PathEscape(groupID) }

// RunEndHook 返回装到终端 Hub 上的「运行结束」挂钩：run_end，只有 ntfy 通道。
func (g *gateNotifier) RunEndHook() func(terminal.RunEndEvent) {
	return func(ev terminal.RunEndEvent) {
		in := runInfoFromEvent(ev)
		// 与 ntfy 自己的预筛一致：不到 1 秒的运行（绝大多数命令）既不推送，也不值得读一次偏好。
		if in.RunID == "" || in.Duration < time.Second {
			return
		}
		g.gate.Publish(notify.Event{
			ID:           "run:" + in.RunID,
			Kind:         notify.KindRunEnd,
			Conversation: conversationOf(in.WorkflowID),
			WorkflowID:   in.WorkflowID,
			Title:        projectTitle(in.ProjectDirectory, in.WorkflowID),
			Preview:      in.Description,
		}, func(notify.Event, notify.Decision) { g.ntfy.NotifyRun(in) })
	}
}

// GuardHook 返回装到终端 Hub 上的「守护告警」挂钩：guard_alert，穿透类。
func (g *gateNotifier) GuardHook() func(terminal.GuardAlert) {
	return func(a terminal.GuardAlert) {
		in := guardInfoFromAlert(a)
		if in.Key == "" {
			return
		}
		what := "疑似停摆"
		if in.Crashed {
			what = "意外结束"
		}
		reason := strings.TrimSpace(in.Reason)
		preview := what
		if reason != "" {
			preview += "：" + reason
		}
		g.gate.Publish(notify.Event{
			ID:           "grd:" + in.Key,
			Kind:         notify.KindGuardAlert,
			Conversation: conversationOf(in.WorkflowID),
			WorkflowID:   in.WorkflowID,
			Title:        strings.TrimSpace(in.Name),
			Preview:      preview,
			DeepLink:     "/exec",
		}, func(notify.Event, notify.Decision) { g.ntfy.NotifyGuard(in) })
	}
}

// AutoResumeHook 返回装到终端 Hub 上的「自动续跑放弃」挂钩（#151）：guard_alert，穿透类——预算用完、连续没有进展或没法提交时，
// 系统不会再自己重试了，需要你出面。排期与开跑只写在聊天里，不推送（每次运行结束本来就有 run_end 推送）。
func (g *gateNotifier) AutoResumeHook() func(terminal.AutoResumeEvent) {
	return func(ev terminal.AutoResumeEvent) {
		in := guardInfoFromAutoResume(ev)
		if ev.PresetID == "" || ev.FromRun == "" {
			return
		}
		preview := "不再自动续跑：" + strings.TrimSpace(ev.Why)
		g.gate.Publish(notify.Event{
			ID:           "arz:" + ev.PresetID + ":" + ev.FromRun,
			Kind:         notify.KindGuardAlert,
			Conversation: conversationOf(ev.WorkflowID),
			WorkflowID:   ev.WorkflowID,
			Title:        strings.TrimSpace(ev.Name),
			Preview:      preview,
			DeepLink:     "/exec",
		}, func(notify.Event, notify.Decision) { g.ntfy.NotifyGuard(in) })
	}
}

// ScreenHook 返回装到终端 Hub 上的「托管终端屏幕事件」挂钩（M3.2）：
// 进入审批等待时发一条 approval_waiting（穿透类，点击直达终端页）；等了很久没人处理时 Hub 会再发提醒事件，
// 提醒的通知 ID 带上序号，闸才不会把它当作重复丢掉。审批结束不通知，只把状态变化推给浏览器。
func (g *gateNotifier) ScreenHook() func(terminal.ScreenEvent) {
	return func(ev terminal.ScreenEvent) {
		if ev.IdentityID != 0 && g.broadcast != nil {
			g.broadcast(agentStateEventName, map[string]any{
				"workflow_id": ev.WorkflowID, "identity_id": ev.IdentityID, "session_id": ev.SessionID,
				"state": ev.State, "sub": ev.Sub, "source": ev.Source, "since": ev.Since,
			})
		}
		if !ev.Approval {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), notifyLookupTimeout)
		defer cancel()
		title, sender := g.names(ctx, ev.WorkflowID, "")
		if title == "" || title == strings.TrimSpace(ev.WorkflowID) {
			title = strings.TrimSpace(ev.Name)
		}
		id := "apr:" + ev.Episode
		preview := "终端在等你批准"
		if ev.Reminder > 0 {
			id += ":r" + strconv.Itoa(ev.Reminder)
			preview = "终端仍在等你批准"
		}
		g.gate.Publish(notify.Event{
			ID:           id,
			Kind:         notify.KindApprovalWaiting,
			Conversation: conversationOf(ev.WorkflowID),
			WorkflowID:   ev.WorkflowID,
			Sender:       sender,
			Title:        title,
			Preview:      preview,
			DeepLink:     "/exec",
		}, g.pushEvent)
	}
}

// agentStateEventName 与 mcp 包广播状态变化用的事件名一致（前端订阅同一个）。
const agentStateEventName = "agent_state"

// pushEvent 把放行的新消息类事件交给 ntfy（预览能不能带由闸按偏好算好，随裁决一起来）。
func (g *gateNotifier) pushEvent(ev notify.Event, d notify.Decision) {
	g.ntfy.NotifyEvent(ntfy.EventInfo{
		ID:           ev.ID,
		Kind:         string(ev.Kind),
		Conversation: ev.Conversation,
		Title:        ev.Title,
		Sender:       ev.Sender,
		Preview:      ev.Preview,
		ShowPreview:  d.Preview,
		Penetrate:    d.Penetrate,
		DeepLink:     ev.DeepLink,
	})
}

// names 返回通知里的「会话名」与「发送者」：会话名取项目目录的最后一段（没有就用工作流编号），
// 发送者取这个工作流最近一个 Agent 会话的身份标签（设计稿 04 的 identityLabel）。查不到的字段留空——通知宁可少一个字段。
func (g *gateNotifier) names(ctx context.Context, workflowID, projectDir string) (title, sender string) {
	if g.store != nil && workflowID != "" {
		if list, err := g.store.ListAgentSessions(ctx, store.AgentSessionFilter{WorkflowID: workflowID, Limit: 1}, time.Now()); err == nil && len(list) > 0 {
			sender = list[0].IdentityLabel
			if strings.TrimSpace(projectDir) == "" {
				projectDir = list[0].ProjectDir
			}
		}
		if strings.TrimSpace(projectDir) == "" {
			if sess, err := g.store.GetLatestWorkflowFeedbackSession(ctx, workflowID); err == nil && sess != nil {
				projectDir = sess.ProjectDirectory
			}
		}
	}
	return projectTitle(projectDir, workflowID), sender
}

// conversationOf 返回 1:1 会话 id；没有工作流时返回空串（空串表示「不属于任何会话」，不受会话级偏好约束）。
func conversationOf(workflowID string) string {
	if strings.TrimSpace(workflowID) == "" {
		return ""
	}
	return notify.ConversationOf(workflowID)
}

// projectTitle 取项目目录的最后一段（兼容 / 与 \）；目录为空时退回工作流编号。
func projectTitle(projectDirectory, workflowID string) string {
	dir := strings.TrimRight(strings.TrimSpace(projectDirectory), `/\`)
	if i := strings.LastIndexAny(dir, `/\`); i >= 0 {
		dir = dir[i+1:]
	}
	if dir == "" {
		dir = strings.TrimSpace(workflowID)
	}
	return dir
}

// deepLinkTo 生成站内深链：按工作流（可选再按会话）打开主界面里对应的会话。
func deepLinkTo(workflowID, sessionID string) string {
	q := url.Values{}
	if wf := strings.TrimSpace(workflowID); wf != "" {
		q.Set("wf", wf)
	}
	if sid := strings.TrimSpace(sessionID); sid != "" {
		q.Set("s", sid)
	}
	if len(q) == 0 {
		return "/"
	}
	return "/?" + q.Encode()
}

// noticePreview 组合 notice 的预览：有标题时是「标题：正文开头」，否则只有正文开头；两者都没有就是空串。
func noticePreview(title, summary string) string {
	title = strings.TrimSpace(title)
	body := notify.Excerpt(summary)
	switch {
	case title != "" && body != "":
		return title + "：" + body
	case title != "":
		return title
	}
	return body
}
