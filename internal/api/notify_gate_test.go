package api

import (
	"bytes"
	"context"
	"encoding/json"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/teacat99/RelayMesh/v2/internal/model"
	"github.com/teacat99/RelayMesh/v2/internal/notify"
	"github.com/teacat99/RelayMesh/v2/internal/ntfy"
	"github.com/teacat99/RelayMesh/v2/internal/store"
	"github.com/teacat99/RelayMesh/v2/internal/terminal"
)

// 统一通知闸的接线（#115）：各个来源经 MCP / 终端钩子进来，浏览器通道（SSE notify）与 ntfy 通道按偏好放行或抑制。
// 决策规则本身由 notify 包的决策表覆盖，这里验证「来源 → 事件 → 通道」这条链路在真实装配下成立，
// 以及老三类渠道（阻塞待答 / 运行结束 / 守护告警）在默认偏好下行为不变。

type gateRig struct {
	t      *testing.T
	srv    *Server
	st     *store.Store
	fake   *fakeNtfy
	events chan string
}

// newGateRig 装配真实的服务、假的 ntfy 与 SSE 订阅者；聚合与冷却的时间单位缩到毫秒级。
func newGateRig(t *testing.T) *gateRig {
	t.Helper()
	srv, st := newNtfyTestServer(t, nil)
	fake := newFakeNtfy(t)
	if _, err := st.UpdateNtfyConfig(context.Background(), func(c ntfy.Config) ntfy.Config {
		c.Enabled, c.Server, c.Topic, c.Token = true, fake.URL, "rm-gate", "tk_gate_SECRET"
		c.ClickURL = "https://rm.example.com/"
		c.RunNotify, c.RunMinSeconds = true, 60
		return c
	}); err != nil {
		t.Fatal(err)
	}
	srv.ntfy.SetTiming(20*time.Millisecond, 10*time.Millisecond)
	return &gateRig{t: t, srv: srv, st: st, fake: fake, events: listenSSE(t, srv)}
}

func (g *gateRig) mcp(tool string, args map[string]any) *httptest.ResponseRecorder {
	g.t.Helper()
	body, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "tools/call",
		"params": map[string]any{"name": tool, "arguments": args},
	})
	w := httptest.NewRecorder()
	g.srv.Engine().ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/mcp?token=tok-ntfy", bytes.NewReader(body)))
	return w
}

func (g *gateRig) enableAsync(wf string) {
	g.t.Helper()
	now := time.Now()
	g.st.RecordHookSeen(wf, now)
	on := true
	if _, err := g.st.UpdateWorkflowMode(context.Background(), wf, store.ModePatch{Enabled: &on}, now); err != nil {
		g.t.Fatalf("开启异步失败：%v", err)
	}
}

func (g *gateRig) setPrefs(body map[string]any) {
	g.t.Helper()
	if code, _, raw := ntfyCall(g.t, g.srv, http.MethodPut, "/api/v1/notify/prefs", body); code != http.StatusOK {
		g.t.Fatalf("设置通知偏好失败：%d %s", code, raw)
	}
}

func (g *gateRig) setConversation(conv, policy string) {
	g.t.Helper()
	if code, _, raw := ntfyCall(g.t, g.srv, http.MethodPut, "/api/v1/notify/conversations", map[string]any{"conversation": conv, "policy": policy}); code != http.StatusOK {
		g.t.Fatalf("设置会话策略失败：%d %s", code, raw)
	}
	drainSSE(g.events) // 偏好更新自己会广播一条 notify_prefs_updated，与本测试无关
}

// notifies 取出（并清空）目前收到的 SSE notify 事件。
func (g *gateRig) notifies() []notify.Payload {
	var out []notify.Payload
	for _, m := range drainSSE(g.events) {
		if !strings.HasPrefix(m, "event: "+notify.SSEEventName+"\n") {
			continue
		}
		for _, line := range strings.Split(m, "\n") {
			if strings.HasPrefix(line, "data: ") {
				var p notify.Payload
				if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &p); err != nil {
					g.t.Fatalf("notify 事件的载荷不是合法 JSON：%v\n%s", err, line)
				}
				out = append(out, p)
			}
		}
	}
	return out
}

// waitNotifies 等到至少收到 n 条 notify 事件。
func (g *gateRig) waitNotifies(n int) []notify.Payload {
	g.t.Helper()
	var all []notify.Payload
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		all = append(all, g.notifies()...)
		if len(all) >= n {
			return all
		}
		time.Sleep(10 * time.Millisecond)
	}
	g.t.Fatalf("等待 %d 条 notify 事件超时，实际 %d 条：%+v", n, len(all), all)
	return nil
}

// expectQuiet 断言一小会儿之内两条通道都没有任何动静。
func (g *gateRig) expectQuiet(what string) {
	g.t.Helper()
	before := len(g.fake.snapshot())
	time.Sleep(350 * time.Millisecond)
	if got := g.notifies(); len(got) != 0 {
		g.t.Fatalf("%s：浏览器通道不应有事件：%+v", what, got)
	}
	if after := len(g.fake.snapshot()); after != before {
		g.t.Fatalf("%s：ntfy 通道不应有推送（%d → %d）", what, before, after)
	}
}

func (g *gateRig) blockingCall(wf, title, summary string) chan *httptest.ResponseRecorder {
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		done <- g.mcp("interactive_feedback", map[string]any{
			"workflow_id": wf, "project_directory": "/work/Demo", "title": title, "summary": summary,
		})
	}()
	return done
}

func waitDone(t *testing.T, done chan *httptest.ResponseRecorder) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("interactive_feedback 没有按短等待时长返回")
	}
}

// 阻塞待答是穿透类：免打扰 + 会话静音下两条通道照常到达；ntfy 走的仍是老的构造器（标题、优先级、不带摘要）。
func TestNotifyGate_BlockingFeedbackPenetratesAndLegacyPushIsUnchanged(t *testing.T) {
	g := newGateRig(t)
	g.setPrefs(map[string]any{"dnd": true})
	g.setConversation("wf:wf-blk", "muted")

	done := g.blockingCall("wf-blk", "请确认方案", "## 结论\n方案已就绪")
	got := g.waitNotifies(1)
	if len(got) != 1 {
		t.Fatalf("应只有一条 notify：%+v", got)
	}
	p := got[0]
	if p.Kind != notify.KindBlockingFeedback || !p.Penetrate || !p.Sound || !p.Badge ||
		p.WorkflowID != "wf-blk" || p.SessionID == "" || p.Conversation != "wf:wf-blk" ||
		p.Preview != "请确认方案" || p.Title != "Demo" || !strings.HasPrefix(p.ID, "blk:") {
		t.Fatalf("notify 载荷不对：%+v", p)
	}
	u, err := url.Parse(p.DeepLink)
	if err != nil || u.Path != "/" || u.Query().Get("wf") != "wf-blk" || u.Query().Get("s") != p.SessionID {
		t.Fatalf("深链应指向这个工作流与会话：%q", p.DeepLink)
	}

	posts := g.fake.waitFor(t, 1)
	b := posts[0].Body
	msg, _ := b["message"].(string)
	if b["title"] != "智能体在等你回复" || b["priority"] != float64(4) || b["click"] != "https://rm.example.com/" {
		t.Fatalf("老渠道的推送应保持原样（固定标题、配置优先级、固定点击地址）：%+v", b)
	}
	if !strings.Contains(msg, "请确认方案") || !strings.Contains(msg, "项目：Demo") || strings.Contains(msg, "方案已就绪") {
		t.Fatalf("推送正文应含标题与项目、不含摘要：%q", msg)
	}
	waitDone(t, done)
}

// notice（interactive_feedback 的 wait:false）成为 agent_text：浏览器通道立即，ntfy 通道按新格式聚合后推送。
func TestNotifyGate_NoticeBecomesAgentText(t *testing.T) {
	g := newGateRig(t)
	g.enableAsync("wf-notice")

	w := g.mcp("interactive_feedback", map[string]any{
		"workflow_id": "wf-notice", "project_directory": "/work/Demo", "wait": false,
		"title": "第三步完成", "summary": "## 进展\n构建与测试都通过了",
	})
	if w.Code != http.StatusOK {
		t.Fatalf("notice 发布失败：%d %s", w.Code, w.Body.String())
	}

	got := g.waitNotifies(1)
	p := got[0]
	if p.Kind != notify.KindAgentText || p.Penetrate || !p.Sound || p.WorkflowID != "wf-notice" || p.SessionID == "" ||
		p.Preview != "第三步完成：进展 构建与测试都通过了" || p.Title != "Demo" || !strings.HasPrefix(p.ID, "txt:") {
		t.Fatalf("agent_text 载荷不对：%+v", p)
	}

	posts := g.fake.waitFor(t, 1)
	b := posts[0].Body
	if b["title"] != "Demo" || b["message"] != "第三步完成：进展 构建与测试都通过了" || b["priority"] != float64(3) {
		t.Fatalf("新消息类的推送格式不对：%+v", b)
	}
	click, _ := b["click"].(string)
	if !strings.HasPrefix(click, "https://rm.example.com/?") || !strings.Contains(click, "wf=wf-notice") || !strings.Contains(click, "s="+p.SessionID) {
		t.Fatalf("点击地址应是对外地址加深链：%q", click)
	}
}

// 免打扰与会话静音都能让 notice 安静；解除之后恢复，其他会话不受影响。
func TestNotifyGate_DNDAndMuteSilenceNotices(t *testing.T) {
	g := newGateRig(t)
	g.enableAsync("wf-quiet")
	g.enableAsync("wf-other")
	notice := func(wf, title string) {
		t.Helper()
		if w := g.mcp("interactive_feedback", map[string]any{
			"workflow_id": wf, "project_directory": "/work/Demo", "wait": false, "title": title, "summary": "内容",
		}); w.Code != http.StatusOK {
			t.Fatalf("notice 发布失败：%d %s", w.Code, w.Body.String())
		}
	}

	g.setPrefs(map[string]any{"dnd": true})
	notice("wf-quiet", "免打扰中")
	g.expectQuiet("免打扰")

	g.setPrefs(map[string]any{"dnd": false})
	g.setConversation("wf:wf-quiet", "muted")
	notice("wf-quiet", "静音中")
	g.expectQuiet("会话静音")

	// 其他会话不受影响。
	notice("wf-other", "别的会话")
	if got := g.waitNotifies(1); got[0].WorkflowID != "wf-other" {
		t.Fatalf("其他会话应照常通知：%+v", got)
	}
	g.fake.waitFor(t, 1)

	// 恢复默认之后，这个会话又会通知。
	if code, _, raw := ntfyCall(t, g.srv, http.MethodDelete, "/api/v1/notify/conversations?conversation=wf:wf-quiet", nil); code != http.StatusOK {
		t.Fatalf("恢复默认失败：%d %s", code, raw)
	}
	drainSSE(g.events)
	notice("wf-quiet", "解除之后")
	if got := g.waitNotifies(1); got[0].WorkflowID != "wf-quiet" || !strings.Contains(got[0].Preview, "解除之后") {
		t.Fatalf("解除静音后应恢复通知：%+v", got)
	}
}

// 「隐藏预览」只影响发往第三方 ntfy 的报文，浏览器（已登录的网页）仍然看得到预览。
func TestNotifyGate_NtfyPreviewModeOnlyAffectsNtfy(t *testing.T) {
	g := newGateRig(t)
	g.enableAsync("wf-priv")
	g.setPrefs(map[string]any{"ntfy_preview": "never"})

	g.mcp("interactive_feedback", map[string]any{
		"workflow_id": "wf-priv", "project_directory": "/work/Demo", "wait": false,
		"title": "机密进展", "summary": "内部代号-ZXCV 已上线",
	})
	got := g.waitNotifies(1)
	if !strings.Contains(got[0].Preview, "内部代号-ZXCV") {
		t.Fatalf("浏览器通道应带预览：%+v", got[0])
	}
	posts := g.fake.waitFor(t, 1)
	raw, _ := json.Marshal(posts[0].Body)
	if strings.Contains(string(raw), "ZXCV") || strings.Contains(string(raw), "机密进展") {
		t.Fatalf("隐藏预览时 ntfy 报文不应含正文：%s", raw)
	}
	if posts[0].Body["message"] != "有新消息" {
		t.Fatalf("隐藏预览时只写「有新消息」：%+v", posts[0].Body)
	}
}

// async_ask 新建的确认卡成为 ask_card；同一问题重复提不会再通知；免打扰时安静。
func TestNotifyGate_AsyncAskBecomesAskCard(t *testing.T) {
	g := newGateRig(t)
	ask := func(wf, title string) map[string]any {
		t.Helper()
		w := g.mcp("async_ask", map[string]any{
			"workflow_id": wf, "title": title, "risk": "L1",
			"options": []map[string]any{{"label": "先做 A"}, {"label": "先做 B", "recommended": true}},
		})
		if w.Code != http.StatusOK {
			t.Fatalf("async_ask 失败：%d %s", w.Code, w.Body.String())
		}
		return nil
	}

	ask("wf-ask", "先做 A 还是先做 B？")
	got := g.waitNotifies(1)
	p := got[0]
	if p.Kind != notify.KindAskCard || p.Penetrate || p.Preview != "先做 A 还是先做 B？" || p.WorkflowID != "wf-ask" || p.ID != "ask:wf-ask:Q1" {
		t.Fatalf("ask_card 载荷不对：%+v", p)
	}
	posts := g.fake.waitFor(t, 1)
	if posts[0].Body["message"] != "先做 A 还是先做 B？" || posts[0].Body["priority"] != float64(3) {
		t.Fatalf("ask_card 的 ntfy 报文不对：%+v", posts[0].Body)
	}

	// 同一问题（标题与选项相同）仍在待处理：没有新建，不再通知。
	ask("wf-ask", "先做 A 还是先做 B？")
	g.expectQuiet("重复提问")

	// 免打扰：新的问题也安静。
	g.setPrefs(map[string]any{"dnd": true})
	drainSSE(g.events)
	ask("wf-ask", "另一个问题？")
	g.expectQuiet("免打扰下的 async_ask")
}

// 运行结束只有 ntfy 通道（没有浏览器通知），免打扰能让它安静；默认偏好下与升级前一致。
func TestNotifyGate_RunEndIsNtfyOnlyAndRespectsDND(t *testing.T) {
	g := newGateRig(t)
	hook := g.srv.notifier.RunEndHook()

	hook(longRun("r-1", nil))
	posts := g.fake.waitFor(t, 1)
	if posts[0].Body["title"] != "模型命令失败（退出码 2）" || posts[0].Body["priority"] != float64(4) {
		t.Fatalf("运行结束的推送应保持原样：%+v", posts[0].Body)
	}
	if got := g.notifies(); len(got) != 0 {
		t.Fatalf("运行结束没有浏览器通道：%+v", got)
	}

	g.setPrefs(map[string]any{"dnd": true})
	hook(longRun("r-2", nil))
	g.expectQuiet("免打扰下的运行结束")
}

// 「不再自动续跑」（#151）也是穿透类：预算用完 / 连续没进展时系统不会再自己重试，免打扰下也要让你知道；
// ntfy 的标题与说明是它自己的，不能说成「疑似停摆」；同一次放弃只通知一次。
func TestNotifyGate_AutoResumeGaveUpPenetratesWithItsOwnWording(t *testing.T) {
	g := newGateRig(t)
	g.setPrefs(map[string]any{"dnd": true})

	ev := terminal.AutoResumeEvent{
		PresetID: "p1", FromRun: "run-9", Name: "夜间回归", Machine: "RM-AAAA", WorkflowID: "wf-auto",
		Reason: "Cursor CLI 报了「可重试」的错误：服务端限流", Why: "连续 2 次自动续跑都在 10 分钟内又失败了，没有进展",
	}
	g.srv.notifier.AutoResumeHook()(ev)
	got := g.waitNotifies(1)
	p := got[0]
	if p.Kind != notify.KindGuardAlert || !p.Penetrate || p.Title != "夜间回归" || p.ID != "arz:p1:run-9" || p.WorkflowID != "wf-auto" || p.DeepLink != "/exec" ||
		!strings.HasPrefix(p.Preview, "不再自动续跑：连续 2 次") {
		t.Fatalf("自动续跑放弃的载荷不对：%+v", p)
	}
	posts := g.fake.waitFor(t, 1)
	if posts[0].Body["title"] != "「夜间回归」不再自动续跑" {
		t.Fatalf("ntfy 标题不对：%+v", posts[0].Body)
	}
	msg, _ := posts[0].Body["message"].(string)
	if !strings.Contains(msg, "连续 2 次") || !strings.Contains(msg, "这次的失败原因：Cursor CLI 报了「可重试」的错误") || strings.Contains(msg, "一键唤醒") {
		t.Fatalf("ntfy 正文不对：%q", msg)
	}

	// 同一次放弃（同一个预设、同一次出事的运行）重复交给闸只通知一次；缺预设或运行编号的事件不通知。
	g.srv.notifier.AutoResumeHook()(ev)
	g.srv.notifier.AutoResumeHook()(terminal.AutoResumeEvent{Name: "x"})
	g.expectQuiet("重复或无效的放弃事件")
}

// 同一个会话重复交给闸只通知一次（两条通道都是）。
func TestNotifyGate_DuplicateRequestNotifiesOnce(t *testing.T) {
	g := newGateRig(t)
	sess := &model.FeedbackSession{ID: "s-dup", WorkflowID: "wf-dup", Title: "重复", ProjectDirectory: "/work/Demo"}
	g.srv.notifier.NotifyRequest(sess)
	g.srv.notifier.NotifyRequest(sess)
	if got := g.waitNotifies(1); len(got) != 1 {
		t.Fatalf("应只有一条 notify：%+v", got)
	}
	g.fake.waitFor(t, 1)
	time.Sleep(300 * time.Millisecond)
	if extra := g.notifies(); len(extra) != 0 {
		t.Fatalf("重复发布不应再广播：%+v", extra)
	}
	if n := len(g.fake.snapshot()); n != 1 {
		t.Fatalf("重复发布不应再推送 ntfy：%d", n)
	}
	// nil / 缺 ID 的会话不会 panic、也不通知。
	g.srv.notifier.NotifyRequest(nil)
	g.srv.notifier.NotifyRequest(&model.FeedbackSession{})
	g.expectQuiet("无效会话")
}

// 隐私：日志里既没有 ntfy 令牌，也没有预览正文（它们会经过闸、聚合与投递的全部环节）。
func TestNotifyGate_LogsContainNeitherTokenNorPreview(t *testing.T) {
	var buf bytes.Buffer
	old := log.Writer()
	log.SetOutput(&buf)
	defer log.SetOutput(old)

	g := newGateRig(t)
	g.enableAsync("wf-log")
	g.mcp("interactive_feedback", map[string]any{
		"workflow_id": "wf-log", "project_directory": "/work/Demo", "wait": false,
		"title": "日志检查", "summary": "绝密预览正文-LOGSCAN",
	})
	g.waitNotifies(1)
	g.fake.waitFor(t, 1)
	// 再走一遍几条会写日志的路径：偏好更新、重复发布、队列之外的无效事件。
	g.setPrefs(map[string]any{"dnd": true})
	g.setPrefs(map[string]any{"dnd": false})
	g.srv.notifier.NotifyNotice(&model.FeedbackSession{ID: "s-x", WorkflowID: "wf-log", Summary: "绝密预览正文-LOGSCAN"})
	g.srv.notifier.NotifyNotice(&model.FeedbackSession{ID: "s-x", WorkflowID: "wf-log", Summary: "绝密预览正文-LOGSCAN"})
	time.Sleep(300 * time.Millisecond)

	logs := buf.String()
	for _, banned := range []string{"LOGSCAN", "绝密", "tk_gate_SECRET", "rm-gate"} {
		if strings.Contains(logs, banned) {
			t.Fatalf("日志里不应出现 %q：\n%s", banned, logs)
		}
	}
}

// 群聊（#117）：群默认只在被 @ 时通知。Agent 在群里发言走通知闸——没 @你 的普通发言被默认策略拦下，
// 群设成「全部」后是 agent_text；@你 升格为穿透的 mention（免打扰也拦不住）；会话键是群编号，对某个群静音不影响 1:1。
func TestNotifyGate_GroupMessagesAndMentions(t *testing.T) {
	g := newGateRig(t)
	g.enableAsync("wf-ga")
	g.enableAsync("wf-gb")
	grp, err := g.st.CreateGroup(context.Background(), store.CreateGroupInput{Title: "联调群", CreatedBy: store.GroupHumanRef, Members: []string{"wf-ga", "wf-gb"}})
	if err != nil {
		t.Fatal(err)
	}
	send := func(mentions ...string) {
		t.Helper()
		w := g.mcp("chat", map[string]any{"workflow_id": "wf-ga", "project_directory": "/work/Demo", "action": "send", "conversation_id": grp.ID, "text": "接口改好了", "mentions": mentions})
		if w.Code != http.StatusOK || strings.Contains(w.Body.String(), `"isError":true`) {
			t.Fatalf("群里发言失败：%d %s", w.Code, w.Body.String())
		}
	}

	// 1. 默认策略：没点名你的发言安静
	send()
	g.expectQuiet("群默认只在被 @ 时通知")

	// 2. 群设成「全部」：普通发言是 agent_text，会话键是群编号，标题是群名，深链进群页
	g.setConversation(grp.ID, "all")
	send()
	p := g.waitNotifies(1)[0]
	if p.Kind != notify.KindAgentText || p.Penetrate || p.Conversation != grp.ID || p.Title != "联调群" || !strings.HasPrefix(p.DeepLink, "/group/") || !strings.Contains(p.DeepLink, strings.TrimPrefix(grp.ID, "grp:")) {
		t.Fatalf("群文本载荷不对：%+v", p)
	}
	g.fake.waitFor(t, 1)

	// 3. 免打扰下：普通群文本被拦；@你 升格为穿透的 mention
	g.setPrefs(map[string]any{"dnd": true})
	drainSSE(g.events)
	send()
	g.expectQuiet("免打扰下的普通群文本")
	before := len(g.fake.snapshot())
	send(model.ChatMentionHuman)
	got := g.waitNotifies(1)
	if got[0].Kind != notify.KindMention || !got[0].Penetrate || got[0].Conversation != grp.ID {
		t.Fatalf("@你 应升格为穿透的 mention：%+v", got[0])
	}
	g.fake.waitFor(t, before+1)

	// 4. 关掉免打扰、把这个群静音：@wf-gb（没点名你）安静
	g.setPrefs(map[string]any{"dnd": false})
	g.setConversation(grp.ID, "muted")
	send("wf-gb")
	g.expectQuiet("静音的群里没点名你的发言")
}

// 托管终端等你批准（M3.2）：approval_waiting 穿透免打扰，点击直达终端页；提醒带序号不被当作重复；结束不通知。
func TestNotifyGate_ApprovalWaitingPenetratesAndRemindersAreDistinct(t *testing.T) {
	g := newGateRig(t)
	g.setPrefs(map[string]any{"dnd": true})
	var mu sync.Mutex
	var states []map[string]any
	g.srv.notifier.broadcast = func(typ string, data any) {
		if typ == "agent_state" {
			mu.Lock()
			states = append(states, data.(map[string]any))
			mu.Unlock()
		}
	}
	hook := g.srv.notifier.ScreenHook()
	start := terminal.ScreenEvent{
		PresetID: "p1", Name: "夜间回归", Machine: "RM-AAAA", WorkflowID: "wf-apr", Approval: true, Episode: "p1:aaaaaaaa:100",
		IdentityID: 3, SessionID: 9, State: "waiting_approval", Since: time.Now(),
	}
	hook(start)
	p := g.waitNotifies(1)[0]
	if p.Kind != notify.KindApprovalWaiting || !p.Penetrate || p.Preview != "终端在等你批准" || p.ID != "apr:p1:aaaaaaaa:100" ||
		p.WorkflowID != "wf-apr" || p.DeepLink != "/exec" || p.Title == "" {
		t.Fatalf("approval_waiting 载荷不对：%+v", p)
	}
	mu.Lock()
	if len(states) != 1 || states[0]["state"] != "waiting_approval" || states[0]["session_id"] != uint(9) {
		t.Fatalf("状态变化应推给浏览器：%+v", states)
	}
	mu.Unlock()

	g.fake.waitFor(t, 1) // 先等 ntfy 通道把第一条推完，后面的「安静」断言才有基线
	// 同一次事件重复交给闸：不重复通知。
	hook(start)
	g.expectQuiet("重复的审批事件")

	// 提醒：通知 ID 带序号，闸放行；文案不同。
	rem := start
	rem.IdentityID, rem.Reminder = 0, 1
	hook(rem)
	p = g.waitNotifies(1)[0]
	if p.ID != "apr:p1:aaaaaaaa:100:r1" || p.Preview != "终端仍在等你批准" {
		t.Fatalf("提醒载荷不对：%+v", p)
	}

	g.fake.waitFor(t, 2)
	// 结束：只推状态，不通知。
	end := start
	end.Approval, end.State = false, "working"
	hook(end)
	g.expectQuiet("审批结束")
}
