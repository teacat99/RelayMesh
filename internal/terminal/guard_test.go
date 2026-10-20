package terminal

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/teacat99/RelayMesh/v2/internal/store"
	"github.com/teacat99/RelayMesh/v2/internal/termproto"
)

// ---------- 纯判断 ----------

func TestGuardAlertDue(t *testing.T) {
	now := time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC)
	stalled := GuardVerdict{State: GuardStalled, Episode: "stalled:100"}
	notified := func(ep string, reminders int, ago time.Duration) *store.GuardPresetState {
		return &store.GuardPresetState{NotifiedEpisode: ep, Reminders: reminders, NotifiedAt: now.Add(-ago)}
	}
	cases := []struct {
		name     string
		v        GuardVerdict
		ps       *store.GuardPresetState
		wantSend bool
		wantRem  int
	}{
		{"不是需要处理的状态", GuardVerdict{State: GuardWorking, Episode: "x"}, nil, false, 0},
		{"没有事件标识", GuardVerdict{State: GuardStalled}, nil, false, 0},
		{"第一次：没有任何记录", stalled, nil, true, 0},
		{"第一次：记录的是另一次事件", stalled, notified("stalled:50", 2, time.Hour), true, 0},
		{"同一次事件，刚通知过", stalled, notified("stalled:100", 0, time.Hour), false, 0},
		{"同一次事件，隔了 6 小时：补发第 1 次", stalled, notified("stalled:100", 0, 6*time.Hour), true, 1},
		{"补发过 1 次，又隔了 6 小时：补发第 2 次", stalled, notified("stalled:100", 1, 6*time.Hour), true, 2},
		{"补发满 2 次就不再发", stalled, notified("stalled:100", 2, 24*time.Hour), false, 0},
		{"补发过 1 次，间隔不够", stalled, notified("stalled:100", 1, 5*time.Hour), false, 0},
		{"意外结束同样适用", GuardVerdict{State: GuardCrashed, Episode: "crashed:s:1"}, nil, true, 0},
	}
	for _, tc := range cases {
		send, rem := guardAlertDue(tc.v, tc.ps, now)
		if send != tc.wantSend || rem != tc.wantRem {
			t.Errorf("%s: 期望 (%v, %d)，得到 (%v, %d)", tc.name, tc.wantSend, tc.wantRem, send, rem)
		}
	}

	if send, _ := guardAlertDue(GuardVerdict{State: GuardDone, Episode: "crashed:s:1"}, nil, now); send {
		t.Error("done 状态不应触发 guardAlertDue")
	}

	// 通知之后你已经唤醒过：认为你在处理，不再补发提醒。
	woke := notified("stalled:100", 0, 7*time.Hour)
	woke.Wakes = []store.GuardWakeRecord{{At: now.Add(-time.Hour), Mode: "type"}}
	if send, _ := guardAlertDue(stalled, woke, now); send {
		t.Error("通知之后唤醒过，不该再补发提醒")
	}
	// 唤醒发生在通知之前：与这次提醒无关。
	earlier := notified("stalled:100", 0, 7*time.Hour)
	earlier.Wakes = []store.GuardWakeRecord{{At: now.Add(-9 * time.Hour), Mode: "type"}}
	if send, rem := guardAlertDue(stalled, earlier, now); !send || rem != 1 {
		t.Errorf("通知之前的唤醒不影响提醒: %v %d", send, rem)
	}
}

func TestGuardSettleProgress(t *testing.T) {
	at := time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC)
	w := func(progress string) store.GuardWakeRecord { return store.GuardWakeRecord{At: at, Progress: progress} }
	idle := guardFacts{}
	tool := guardFacts{Link: store.AgentLink{LastToolAt: at.Add(2 * time.Minute)}}
	stop := guardFacts{Link: store.AgentLink{StopAt: at.Add(time.Minute)}}
	mcp := guardFacts{AgentActive: at.Add(30 * time.Second)}
	old := guardFacts{Link: store.AgentLink{LastToolAt: at.Add(-time.Minute), StopAt: at.Add(-time.Second)}, AgentActive: at.Add(-time.Hour)}

	cases := []struct {
		name string
		w    store.GuardWakeRecord
		f    guardFacts
		now  time.Time
		want string
	}{
		{"已有结论就沿用", w(store.GuardProgressNone), tool, at.Add(time.Hour), store.GuardProgressNone},
		{"窗口内还没动静：没有结论", w(""), idle, at.Add(5 * time.Minute), ""},
		{"窗口内有工具调用", w(""), tool, at.Add(5 * time.Minute), store.GuardProgressYes},
		{"一轮对话收尾也算动静", w(""), stop, at.Add(5 * time.Minute), store.GuardProgressYes},
		{"调用 RelayMesh 也算动静", w(""), mcp, at.Add(5 * time.Minute), store.GuardProgressYes},
		{"窗口过完仍没有动静", w(""), idle, at.Add(guardProgressWindow), store.GuardProgressNone},
		{"唤醒之前的痕迹不算", w(""), old, at.Add(time.Hour), store.GuardProgressNone},
		{"窗口过完才出现的动静，只要出现了就算", w(""), tool, at.Add(time.Hour), store.GuardProgressYes},
	}
	for _, tc := range cases {
		if got := guardSettleProgress(tc.w, tc.f, tc.now); got != tc.want {
			t.Errorf("%s: 期望 %q，得到 %q", tc.name, tc.want, got)
		}
	}
	// 终端上的输出与人的输入不算智能体的反应：唤醒本身就会让终端有回显。
	echo := guardFacts{OutputAt: at.Add(time.Second)}
	if guardActiveAfter(echo, at) {
		t.Error("终端输出不该被当作智能体的动静")
	}
}

func TestGuardWakeRoute(t *testing.T) {
	live := guardFacts{HostOnline: true, Session: &guardSession{SID: "s"}}
	ended := guardFacts{HostOnline: true}
	offline := guardFacts{}
	exited := guardFacts{HostOnline: true, Session: &guardSession{SID: "s", Exited: true}}
	cases := []struct {
		name      string
		f         guardFacts
		preferred string
		want      string
	}{
		{"自动：会话还活着就输入", live, "", GuardWakeType},
		{"自动：会话没了就重新拉起", ended, "", GuardWakeRelaunch},
		{"自动：列表里是已退出的会话也重新拉起", exited, "", GuardWakeRelaunch},
		{"自动：主机离线没法唤醒", offline, "", GuardWakeNone},
		{"指定输入：会话活着", live, GuardWakeType, GuardWakeType},
		{"指定输入：没有存活的会话", ended, GuardWakeType, GuardWakeNone},
		{"指定重新拉起：会话还活着也可以（新开一个）", live, GuardWakeRelaunch, GuardWakeRelaunch},
		{"指定重新拉起：主机离线", offline, GuardWakeRelaunch, GuardWakeNone},
		{"不认识的方式", live, "teleport", GuardWakeNone},
	}
	for _, tc := range cases {
		got, note := guardWakeRoute(tc.f, tc.preferred)
		if got != tc.want {
			t.Errorf("%s: 期望 %s，得到 %s（%s）", tc.name, tc.want, got, note)
		}
		if got == GuardWakeNone && note == "" {
			t.Errorf("%s: 没法唤醒时必须说明原因", tc.name)
		}
	}
}

func TestGuardAppendable(t *testing.T) {
	for cmd, want := range map[string]bool{
		"agent":                               true,
		"agent --force --model x":             true,
		"cd /x && agent":                      true,
		"agent --resume abc":                  true,
		"":                                    false,
		"   ":                                 false,
		"agent | tee run.log":                 false,
		"agent > out.txt":                     false,
		"agent < in.txt":                      false,
		"agent &":                             false,
		"agent;":                              false,
		"agent \\":                            false,
		"FOO=`date` agent":                    false,
		"FOO=$(date) agent":                   false,
		"agent --model 'a b'":                 true,
		"agent --resume abc 2>&1":             false,
		"agent --message 'a > b'":             false, // 引号里的符号也宁可保守地拒绝
		"agent --model claude-sonnet-5-5-max": true,
	} {
		if got := guardAppendable(cmd); got != want {
			t.Errorf("guardAppendable(%q) = %v，期望 %v", cmd, got, want)
		}
	}
}

func TestGuardWakeCommand(t *testing.T) {
	p := func(cmd, resume string) store.TerminalLauncher {
		return store.TerminalLauncher{ID: "p", Command: cmd, Guard: &store.LauncherGuard{ResumeCommand: resume}}
	}

	t.Run("用对话编号接着原来的对话", func(t *testing.T) {
		cmd, resume, warns, err := guardWakeCommand(p("agent", "agent --resume {chat_id} --force"), "chat-1")
		if err != nil || cmd != "agent --resume chat-1 --force" || !resume || len(warns) != 0 {
			t.Fatalf("got %q %v %v %v", cmd, resume, warns, err)
		}
	})
	t.Run("重新拉起命令里没有占位符：原样用", func(t *testing.T) {
		cmd, resume, warns, err := guardWakeCommand(p("agent", "agent --continue"), "")
		if err != nil || cmd != "agent --continue" || !resume || len(warns) != 0 {
			t.Fatalf("got %q %v %v %v", cmd, resume, warns, err)
		}
	})
	t.Run("需要编号却不知道：退回预设命令并提醒", func(t *testing.T) {
		cmd, resume, warns, err := guardWakeCommand(p("agent --force", "agent --resume {chat_id}"), "")
		if err != nil || cmd != "agent --force" || resume || len(warns) != 1 || !strings.Contains(warns[0], "对话编号") {
			t.Fatalf("got %q %v %v %v", cmd, resume, warns, err)
		}
	})
	t.Run("没配重新拉起命令：新开对话并提醒", func(t *testing.T) {
		cmd, resume, warns, err := guardWakeCommand(p("agent", ""), "chat-1")
		if err != nil || cmd != "agent" || resume || len(warns) != 1 || !strings.Contains(warns[0], "新开一个对话") {
			t.Fatalf("got %q %v %v %v", cmd, resume, warns, err)
		}
	})
	t.Run("什么命令都没有：报错", func(t *testing.T) {
		_, _, _, err := guardWakeCommand(p("", ""), "chat-1")
		var we *wakeError
		if !asWakeError(err, &we) || we.Code != "no_command" {
			t.Fatalf("应报 no_command: %v", err)
		}
	})
}

func asWakeError(err error, out **wakeError) bool {
	we, ok := err.(*wakeError)
	if ok {
		*out = we
	}
	return ok
}

func TestGuardNameKey(t *testing.T) {
	for in, want := range map[string]string{
		"Cursor CLI":     "cursor cli",
		"  cursor   cli": "cursor cli",
		"CURSOR\tCLI":    "cursor cli",
		"":               "",
	} {
		if got := guardNameKey(in); got != want {
			t.Errorf("guardNameKey(%q) = %q，期望 %q", in, got, want)
		}
	}
}

// ---------- 内存观察 ----------

func TestGuardObserveTracksAttachAndOutput(t *testing.T) {
	e := newTestEnv(t, withFakeClock())
	g := e.hub.guardian
	t0 := e.clock.Now()
	info := termproto.SessionInfo{SID: sidOf('a'), Name: "x", End: 100}

	// 第一次看到：当前输出量只是基线，不算「刚有输出」；没人连着。
	if present, out := g.observe("p", &info, t0); !present.IsZero() || !out.IsZero() {
		t.Fatalf("第一次观察不该有结论: %v %v", present, out)
	}
	// 输出量变了：算有输出。
	info.End = 150
	if _, out := g.observe("p", &info, t0.Add(time.Minute)); !out.Equal(t0.Add(time.Minute)) {
		t.Fatalf("输出增长应记下时间: %v", out)
	}
	// 没变：保持原来的时间。
	if _, out := g.observe("p", &info, t0.Add(2*time.Minute)); !out.Equal(t0.Add(time.Minute)) {
		t.Fatalf("输出没变不该刷新: %v", out)
	}
	// 有人连上：记下第一次看到的时间，之后保持。
	info.Attached = 1
	present, _ := g.observe("p", &info, t0.Add(3*time.Minute))
	if !present.Equal(t0.Add(3 * time.Minute)) {
		t.Fatalf("应记下有人连着的起点: %v", present)
	}
	if p, _ := g.observe("p", &info, t0.Add(10*time.Minute)); !p.Equal(present) {
		t.Fatalf("起点应保持不变: %v", p)
	}
	// 断开：清零。
	info.Attached = 0
	if p, _ := g.observe("p", &info, t0.Add(11*time.Minute)); !p.IsZero() {
		t.Fatalf("断开后应清零: %v", p)
	}
	// 主机上的「最近交互」时间：取它，但不能晚于现在（主机时钟快）。
	info.Active = t0.Add(20 * time.Minute).Unix()
	if _, out := g.observe("p", &info, t0.Add(30*time.Minute)); !out.Equal(time.Unix(info.Active, 0)) {
		t.Fatalf("应取主机报告的最近交互时间: %v", out)
	}
	info.Active = t0.Add(5 * time.Hour).Unix()
	if _, out := g.observe("p", &info, t0.Add(31*time.Minute)); out.After(t0.Add(31 * time.Minute)) {
		t.Fatalf("未来的时间不能被采信: %v", out)
	}
	// 换了新会话：基线重来。
	other := termproto.SessionInfo{SID: sidOf('b'), End: 5}
	if p, out := g.observe("p", &other, t0.Add(32*time.Minute)); !p.IsZero() || !out.IsZero() {
		t.Fatalf("换会话应重新观察: %v %v", p, out)
	}
	// 没有存活的会话：清零。
	g.observe("p", nil, t0.Add(33*time.Minute))
	g.mu.Lock()
	_, kept := g.rt["p"]
	g.mu.Unlock()
	if kept {
		t.Fatal("没有存活的会话时不该保留观察")
	}
}

// ---------- 服务（接真实的 Hub 与存储） ----------

type alertSink struct {
	mu   sync.Mutex
	list []GuardAlert
}

func (s *alertSink) hook(a GuardAlert) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.list = append(s.list, a)
}

func (s *alertSink) all() []GuardAlert {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]GuardAlert(nil), s.list...)
}

func sidOf(c byte) string { return strings.Repeat(string(c), 32) }

const guardPresetName = "Cursor CLI"

type guardEnv struct {
	*testEnv
	alerts *alertSink
	agent  *agentConn
}

func newGuardEnv(t *testing.T, opts ...envOption) *guardEnv {
	t.Helper()
	e := newTestEnv(t, append([]envOption{withFakeClock()}, opts...)...)
	g := &guardEnv{testEnv: e, alerts: &alertSink{}}
	e.hub.SetGuardHook(g.alerts.hook)
	return g
}

// savePreset 经 REST 保存一个带守护配置的预设（走和界面一样的校验）。
func (g *guardEnv) savePreset(id, name string, guard map[string]any) apiResult {
	g.t.Helper()
	body := map[string]any{"name": name, "host": testLauncherHost, "dir": "/home/user/RelayMesh", "command": "agent"}
	if guard != nil {
		body["guard"] = guard
	}
	res := g.call("PUT", "/api/v1/terminal/launchers/"+id, body, g.admin()...)
	if res.Status != 200 {
		g.t.Fatalf("保存预设失败: %d %s", res.Status, res.Raw)
	}
	return res
}

// online 让主机上线并报告这些会话。
func (g *guardEnv) online(sessions ...termproto.SessionInfo) {
	g.t.Helper()
	a := &agentConn{hub: g.hub, code: testLauncherHost, enabled: true, sessions: sessions}
	g.hub.registerAgent(a)
	g.t.Cleanup(func() { g.hub.unregisterAgent(a) })
	g.agent = a
}

func (g *guardEnv) setSessions(sessions ...termproto.SessionInfo) {
	g.agent.mu.Lock()
	g.agent.sessions = sessions
	g.agent.mu.Unlock()
}

func (g *guardEnv) offline() {
	g.hub.unregisterAgent(g.agent)
}

func (g *guardEnv) liveSession(sid string, created time.Time) termproto.SessionInfo {
	return termproto.SessionInfo{SID: sid, Name: guardPresetName, State: termproto.SessionRunning, Created: created.Unix()}
}

// index 往会话索引里写一行；ended 为真时同时标记「此刻」已退出（带退出码）。
func (g *guardEnv) index(sid, name string, started time.Time, exited bool, code *int) {
	g.t.Helper()
	_, err := g.store.SyncTerminalSessions(context.Background(), testLauncherHost, []store.TerminalSessionSeen{
		{SID: sid, Name: name, StartedAt: started.Unix(), Exited: exited, ExitCode: code},
	}, g.clock.Now())
	if err != nil {
		g.t.Fatalf("写会话索引: %v", err)
	}
}

func (g *guardEnv) tick() { g.hub.guardian.tick(context.Background(), g.clock.Now()) }

// stalledByError 让工作流 wf 汇报：一轮对话在 at 时以出错收尾（之前有过工具调用）。
func (g *guardEnv) stalledByError(wf, chat string, at time.Time) {
	ctx := context.Background()
	g.store.RecordHookSeen(wf, at)
	g.store.RecordAgentEvent(ctx, wf, store.AgentEvent{Kind: store.AgentEventTool, ConversationID: chat}, at.Add(-time.Minute))
	g.store.RecordAgentEvent(ctx, wf, store.AgentEvent{Kind: store.AgentEventStop, Status: store.StopError, ConversationID: chat}, at)
}

func (g *guardEnv) guardState() store.GuardStateDoc {
	g.t.Helper()
	doc, err := g.store.GetTerminalGuardState(context.Background())
	if err != nil {
		g.t.Fatalf("读守护状态: %v", err)
	}
	return doc
}

func TestGuardTickNotifiesUnexpectedEndOnceAndSurvivesRestart(t *testing.T) {
	g := newGuardEnv(t)
	g.savePreset("p1", guardPresetName, map[string]any{"enabled": true, "workflow_id": "wf-a"})
	g.online()
	code := 137
	g.index(sidOf('a'), guardPresetName, g.clock.Now().Add(-time.Hour), true, &code)

	g.clock.Advance(time.Minute)
	g.tick()
	got := g.alerts.all()
	if len(got) != 1 {
		t.Fatalf("会话异常退出应通知 1 次: %+v", got)
	}
	a := got[0]
	if a.State != GuardCrashed || a.Reminder != 0 || a.PresetID != "p1" || a.Name != guardPresetName ||
		a.Machine != testLauncherHost || a.WorkflowID != "wf-a" || !strings.Contains(a.Reason, "137") || a.Since.IsZero() {
		t.Fatalf("通知内容不对: %+v", a)
	}

	// 同一次事件：之后的每一轮都不再通知。
	for i := 0; i < 5; i++ {
		g.clock.Advance(time.Minute)
		g.tick()
	}
	if n := len(g.alerts.all()); n != 1 {
		t.Fatalf("同一次事件不该重复通知: %d", n)
	}

	// Hub 重启（内存里的东西都没了）：已通知的事件标识在库里，不会再通知一遍。
	// 另起一个没有任何内存状态的 guardService 来跑后面的轮次，而不是改写 g.hub.guardian：Hub 的后台协程启动时会读这个字段，
	// -race 或主机忙时它可能还没读到，改写就是一次数据竞争（测试自己造出来的，产品代码里没有人改写它）。
	restarted := newGuardService(g.hub)
	notify := g.alerts.hook
	restarted.hook.Store(&notify)
	tick := func() { restarted.tick(context.Background(), g.clock.Now()) }
	g.clock.Advance(time.Minute)
	tick()
	if n := len(g.alerts.all()); n != 1 {
		t.Fatalf("重启之后不该为同一次事件再通知: %d", n)
	}
	if st := g.guardState().Presets["p1"]; st == nil || st.NotifiedEpisode == "" {
		t.Fatalf("应持久化已通知的事件: %+v", st)
	}

	// 一次新的意外结束是另一件事：再通知。
	g.index(sidOf('b'), guardPresetName, g.clock.Now(), false, nil)
	g.setSessions(g.liveSession(sidOf('b'), g.clock.Now()))
	tick()
	g.setSessions()
	g.clock.Advance(time.Minute)
	g.index(sidOf('b'), guardPresetName, g.clock.Now().Add(-time.Minute), true, &code)
	g.clock.Advance(time.Minute)
	tick()
	if n := len(g.alerts.all()); n != 2 {
		t.Fatalf("新的意外结束应再通知一次: %d", n)
	}
}

func TestGuardTickDoesNotAlertForNormalEndsOfflineHostOrIndexLag(t *testing.T) {
	g := newGuardEnv(t)
	g.savePreset("p1", guardPresetName, map[string]any{"enabled": true, "workflow_id": "wf-a"})
	g.online()
	zero := 0

	// 命令正常退出（退出码 0）：不是意外。
	g.index(sidOf('a'), guardPresetName, g.clock.Now().Add(-time.Hour), true, &zero)
	g.clock.Advance(time.Minute)
	g.tick()
	if n := len(g.alerts.all()); n != 0 {
		t.Fatalf("正常退出不该通知: %+v", g.alerts.all())
	}

	// 手动结束：不是意外。
	g.index(sidOf('b'), guardPresetName, g.clock.Now(), false, nil)
	if err := g.store.RecordTerminalSessionEnds(context.Background(), testLauncherHost,
		[]store.TerminalSessionEnd{{SID: sidOf('b'), Reason: "killed", EndedAt: g.clock.Now()}}, g.clock.Now()); err != nil {
		t.Fatal(err)
	}
	g.clock.Advance(time.Minute)
	g.tick()
	if n := len(g.alerts.all()); n != 0 {
		t.Fatalf("手动结束不该通知: %+v", g.alerts.all())
	}

	// 主机刚报告一个已退出的会话（比索引里最新的还新）、索引还没来得及记下：等下一轮，不凭残缺的事实通知。
	g.online(termproto.SessionInfo{SID: sidOf('d'), Name: guardPresetName, State: termproto.SessionExited, Created: g.clock.Now().Unix() + 5})
	g.tick()
	if n := len(g.alerts.all()); n != 0 {
		t.Fatalf("索引没跟上时不该通知: %+v", g.alerts.all())
	}

	// 主机离线、最近的会话在索引里还是运行中：看不到，不能当作意外。
	g.index(sidOf('c'), guardPresetName, g.clock.Now(), false, nil)
	g.offline()
	g.clock.Advance(time.Minute)
	g.tick()
	if n := len(g.alerts.all()); n != 0 {
		t.Fatalf("主机离线不该通知: %+v", g.alerts.all())
	}

	// 主机回来、索引也跟上之后，那次异常退出才通知。
	g.online(termproto.SessionInfo{SID: sidOf('d'), Name: guardPresetName, State: termproto.SessionExited, Created: g.clock.Now().Unix() + 5})
	code := 1
	g.index(sidOf('d'), guardPresetName, g.clock.Now().Add(5*time.Second), true, &code)
	g.tick()
	if n := len(g.alerts.all()); n != 1 {
		t.Fatalf("索引跟上之后应通知: %+v", g.alerts.all())
	}
}

func TestGuardTickStalledAfterErrorRemindsTwiceThenStops(t *testing.T) {
	g := newGuardEnv(t)
	g.savePreset("p1", guardPresetName, map[string]any{"enabled": true, "workflow_id": "wf-b"})
	start := g.clock.Now()
	g.online(g.liveSession(sidOf('a'), start))
	g.clock.Advance(time.Minute)
	stopAt := g.clock.Now()
	g.stalledByError("wf-b", "chat-b", stopAt)

	// 刚出错收尾：观察期内不通知。
	g.clock.Advance(3 * time.Minute)
	g.tick()
	if n := len(g.alerts.all()); n != 0 {
		t.Fatalf("观察期内不该通知: %+v", g.alerts.all())
	}

	// 5 分钟没有新活动：停摆，通知一次。
	g.clock.Advance(3 * time.Minute)
	g.tick()
	got := g.alerts.all()
	if len(got) != 1 || got[0].State != GuardStalled || got[0].Reminder != 0 || !strings.Contains(got[0].Reason, "出错") {
		t.Fatalf("应通知一次停摆: %+v", got)
	}
	if !got[0].Since.Equal(stopAt) {
		t.Fatalf("起点应是出错收尾的时间: %v 期望 %v", got[0].Since, stopAt)
	}

	g.clock.Advance(time.Hour)
	g.tick()
	if n := len(g.alerts.all()); n != 1 {
		t.Fatalf("同一次停摆一小时后不该重复通知: %d", n)
	}

	// 6 小时后补发第 1 次，再 6 小时补发第 2 次，之后不再发。
	for want := 2; want <= 3; want++ {
		g.clock.Advance(guardRemindEvery)
		g.tick()
		list := g.alerts.all()
		if len(list) != want || list[want-1].Reminder != want-1 {
			t.Fatalf("应补发第 %d 次提醒: %+v", want-1, list)
		}
	}
	g.clock.Advance(guardRemindEvery)
	g.tick()
	g.clock.Advance(guardRemindEvery)
	g.tick()
	if n := len(g.alerts.all()); n != 3 {
		t.Fatalf("提醒最多补发 2 次，共 3 条: %d", n)
	}
}

func TestGuardTickStaysQuietWhilePresentThenAlertsWhenTheTabIsForgotten(t *testing.T) {
	g := newGuardEnv(t)
	g.savePreset("p1", guardPresetName, map[string]any{"enabled": true, "workflow_id": "wf-c", "silence_minutes": 30})
	s := g.liveSession(sidOf('a'), g.clock.Now())
	s.Attached = 1
	g.online(s)
	g.clock.Advance(time.Minute)
	g.stalledByError("wf-c", "chat-c", g.clock.Now())

	// 有人连着：即使上一轮出错中断了，也不打扰（你正看着它）。
	g.clock.Advance(10 * time.Minute)
	g.tick()
	g.clock.Advance(10 * time.Minute)
	g.tick()
	if n := len(g.alerts.all()); n != 0 {
		t.Fatalf("有人连着时不该通知: %+v", g.alerts.all())
	}
	// 连着的页面一直没人管（超过停摆时长）：当作忘了关，照常判断。
	g.clock.Advance(21 * time.Minute)
	g.tick()
	if n := len(g.alerts.all()); n != 1 {
		t.Fatalf("忘了关的页面不该一直压住通知: %+v", g.alerts.all())
	}
}

func TestGuardTickIgnoresDisabledPresetsAndPresetsWithoutGuard(t *testing.T) {
	g := newGuardEnv(t)
	g.savePreset("off", "Disabled", map[string]any{"enabled": false, "workflow_id": "wf-x"})
	g.savePreset("none", "Plain", nil)
	g.online()
	code := 2
	g.index(sidOf('a'), "Disabled", g.clock.Now().Add(-time.Hour), true, &code)
	g.index(sidOf('b'), "Plain", g.clock.Now().Add(-time.Hour), true, &code)
	g.clock.Advance(time.Minute)
	g.tick()
	if n := len(g.alerts.all()); n != 0 {
		t.Fatalf("没打开守护的预设不该通知: %+v", g.alerts.all())
	}
}

func TestGuardTickSkipsWhenTheTerminalGateIsClosed(t *testing.T) {
	g := newGuardEnv(t, withMode(ModeOff))
	if _, _, err := g.store.UpsertTerminalLauncher(context.Background(), store.TerminalLauncher{
		ID: "p1", Name: guardPresetName, Host: testLauncherHost, Command: "agent",
		Guard: &store.LauncherGuard{Enabled: true, WorkflowID: "wf-a"},
	}); err != nil {
		t.Fatal(err)
	}
	g.online()
	code := 9
	g.index(sidOf('a'), guardPresetName, g.clock.Now().Add(-time.Hour), true, &code)
	g.clock.Advance(time.Minute)
	g.tick()
	if n := len(g.alerts.all()); n != 0 {
		t.Fatalf("总闸关闭时守护不该工作: %+v", g.alerts.all())
	}
}

func TestGuardTickPrunesStateOfDeletedPresets(t *testing.T) {
	g := newGuardEnv(t)
	g.savePreset("p1", guardPresetName, map[string]any{"enabled": true, "workflow_id": "wf-a"})
	g.online()
	code := 1
	g.index(sidOf('a'), guardPresetName, g.clock.Now().Add(-time.Hour), true, &code)
	g.clock.Advance(time.Minute)
	g.tick()
	if g.guardState().Presets["p1"] == nil {
		t.Fatal("通知之后应有这个预设的状态")
	}
	res := g.call("DELETE", "/api/v1/terminal/launchers/p1", nil, g.admin()...)
	if res.Status != 200 {
		t.Fatalf("删除预设: %d %s", res.Status, res.Raw)
	}
	g.clock.Advance(time.Minute)
	g.tick()
	if st := g.guardState().Presets["p1"]; st != nil {
		t.Fatalf("预设删除后状态也应清掉: %+v", st)
	}
}

func TestGuardTickSurvivesAMissingOrMisbehavingHook(t *testing.T) {
	g := newGuardEnv(t)
	g.savePreset("p1", guardPresetName, map[string]any{"enabled": true, "workflow_id": "wf-d"})
	g.online()
	code := 3
	crash := func(sid byte) {
		g.index(sidOf(sid), guardPresetName, g.clock.Now().Add(-time.Hour), true, &code)
		g.clock.Advance(time.Minute)
	}

	// 没有接线：什么都不会发生，也不会报错（事件标识照常记下）。
	g.hub.SetGuardHook(nil)
	crash('a')
	g.tick()

	// 挂钩里 panic：被截住，守护照常继续。
	g.hub.SetGuardHook(func(GuardAlert) { panic("boom") })
	crash('b')
	g.hub.guardian.safeTick(context.Background())

	// 恢复之后：前面两次已经记过了，不会补发；新的事件照常通知。
	g.hub.SetGuardHook(g.alerts.hook)
	g.tick()
	if n := len(g.alerts.all()); n != 0 {
		t.Fatalf("已经通知过（记过）的事件不该再通知: %+v", g.alerts.all())
	}
	crash('c')
	g.tick()
	if n := len(g.alerts.all()); n != 1 {
		t.Fatalf("新的事件应正常通知: %+v", g.alerts.all())
	}
}

func TestGuardTickSettlesWhetherAWakeHadAnyEffect(t *testing.T) {
	g := newGuardEnv(t)
	g.savePreset("p1", guardPresetName, map[string]any{"enabled": true, "workflow_id": "wf-e", "resume_command": "agent --resume {chat_id}"})
	g.online(g.liveSession(sidOf('a'), g.clock.Now()))
	g.clock.Advance(time.Minute)
	g.stalledByError("wf-e", "chat-e", g.clock.Now())
	g.clock.Advance(10 * time.Minute)

	res := g.call("POST", "/api/v1/terminal/guard/p1/wake", map[string]string{}, g.admin()...)
	if res.Status != 200 {
		t.Fatalf("唤醒: %d %s", res.Status, res.Raw)
	}
	wokeAt := g.clock.Now()

	// 观察窗口内：还没有结论。
	g.clock.Advance(2 * time.Minute)
	g.tick()
	if w := g.guardState().Presets["p1"].Wakes; len(w) != 1 || w[0].Progress != "" {
		t.Fatalf("窗口内没动静时不该有结论: %+v", w)
	}
	// 智能体有了动静：记为有效。
	g.store.RecordAgentEvent(context.Background(), "wf-e", store.AgentEvent{Kind: store.AgentEventTool}, wokeAt.Add(3*time.Minute))
	g.clock.Advance(time.Minute)
	g.tick()
	if w := g.guardState().Presets["p1"].Wakes; w[0].Progress != store.GuardProgressYes {
		t.Fatalf("有动静应记为有效: %+v", w)
	}

	// 再唤醒一次，这回一直没动静：窗口过完记为没有效果。
	g.clock.Advance(time.Hour)
	if res := g.call("POST", "/api/v1/terminal/guard/p1/wake", map[string]string{}, g.admin()...); res.Status != 200 {
		t.Fatalf("再唤醒: %d %s", res.Status, res.Raw)
	}
	g.clock.Advance(guardProgressWindow + time.Minute)
	g.tick()
	w := g.guardState().Presets["p1"].Wakes
	if len(w) != 2 || w[1].Progress != store.GuardProgressNone {
		t.Fatalf("窗口过完仍没动静应记为没有效果: %+v", w)
	}
	// 已经有结论的不会被改写。
	g.store.RecordAgentEvent(context.Background(), "wf-e", store.AgentEvent{Kind: store.AgentEventTool}, g.clock.Now())
	g.clock.Advance(time.Minute)
	g.tick()
	if w := g.guardState().Presets["p1"].Wakes; w[1].Progress != store.GuardProgressNone || w[0].Progress != store.GuardProgressYes {
		t.Fatalf("已有结论不该被改写: %+v", w)
	}
}

func TestGuardVerdictWithoutWorkflowExplainsTheLimit(t *testing.T) {
	g := newGuardEnv(t)
	g.savePreset("p1", guardPresetName, map[string]any{"enabled": true})
	g.online(g.liveSession(sidOf('a'), g.clock.Now()))
	g.clock.Advance(time.Minute)
	res := g.call("GET", "/api/v1/terminal/guard", nil, "Authorization", humanAuth(testUser))
	if res.Status != 200 {
		t.Fatalf("GET /guard: %d %s", res.Status, res.Raw)
	}
	items, _ := res.Body["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("应有 1 项: %s", res.Raw)
	}
	v, _ := items[0].(map[string]any)["verdict"].(map[string]any)
	if v["state"] != "unknown" || !strings.Contains(v["reason"].(string), "没有填工作流编号") {
		t.Fatalf("没填工作流编号时应说明只能监测意外结束: %v", v)
	}
	// 只监测意外结束：会话没了就照样能发现。
	code := 137
	g.setSessions()
	g.index(sidOf('a'), guardPresetName, g.clock.Now().Add(-time.Hour), true, &code)
	g.clock.Advance(time.Minute)
	g.tick()
	if n := len(g.alerts.all()); n != 1 || g.alerts.all()[0].WorkflowID != "" {
		t.Fatalf("没有工作流编号也该能发现意外结束: %+v", g.alerts.all())
	}
}
