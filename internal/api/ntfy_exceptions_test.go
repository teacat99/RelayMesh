package api

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/teacat99/RelayMesh/v2/internal/agentstate"
	"github.com/teacat99/RelayMesh/v2/internal/ntfy"
	"github.com/teacat99/RelayMesh/v2/internal/store"
	"github.com/teacat99/RelayMesh/v2/internal/terminal"
)

func TestEpisodeTracker(t *testing.T) {
	var tr episodeTracker
	set := func(keys ...string) map[string]struct{} {
		m := map[string]struct{}{}
		for _, k := range keys {
			m[k] = struct{}{}
		}
		return m
	}

	// 第一遍只记录：已经存在的异常不算新事件。
	if got := tr.Observe(set("a", "b")); got != nil {
		t.Fatalf("第一遍不该有新事件：%v", got)
	}
	// 没变化 → 没有新事件。
	if got := tr.Observe(set("a", "b")); len(got) != 0 {
		t.Fatalf("没变化不该有新事件：%v", got)
	}
	// 新增 c、d（排好序）。
	if got := tr.Observe(set("a", "d", "c")); !reflect.DeepEqual(got, []string{"c", "d"}) {
		t.Fatalf("应只返回新出现的并排序：%v", got)
	}
	// b 恢复了；再次出现就是新的一段。
	tr.Observe(set("a", "c", "d"))
	if got := tr.Observe(set("a", "b", "c", "d")); !reflect.DeepEqual(got, []string{"b"}) {
		t.Fatalf("恢复后再出现应算新的一段：%v", got)
	}
	// Reset 之后重新播种。
	tr.Reset()
	if got := tr.Observe(set("x")); got != nil {
		t.Fatalf("Reset 后第一遍应只记录：%v", got)
	}
}

// excSink 是假的推送器：记下收到的异常，配置可以在测试中途改。
type excSink struct {
	mu   sync.Mutex
	cfg  ntfy.Config
	err  error
	got  []ntfy.ExceptionInfo
	cfgN int
}

func (s *excSink) NotifyException(in ntfy.ExceptionInfo) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.got = append(s.got, in)
}

func (s *excSink) EffectiveConfig(context.Context) (ntfy.Config, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cfgN++
	return s.cfg, s.err
}

func (s *excSink) set(f func(*ntfy.Config)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f(&s.cfg)
	s.cfg = s.cfg.Normalize()
}

func (s *excSink) take() []ntfy.ExceptionInfo {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := s.got
	s.got = nil
	return out
}

type excHarness struct {
	t        *testing.T
	sink     *excSink
	w        *exceptionWatcher
	now      time.Time
	views    []store.AgentSessionView
	hosts    []terminal.HostPresence
	sessionN int
	hostN    int
	failView bool
}

func newExcHarness(t *testing.T, on ...string) *excHarness {
	t.Helper()
	h := &excHarness{t: t, sink: &excSink{}, now: time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)}
	h.sink.set(func(c *ntfy.Config) {
		c.Enabled, c.Topic, c.Priority = true, "topic-1", 4
		c.PriorityByKind = map[string]int{}
		for _, k := range on {
			c.PriorityByKind[k] = 4
		}
	})
	h.w = &exceptionWatcher{
		sink: h.sink, now: func() time.Time { return h.now }, offlineSince: map[string]time.Time{},
		sessions: func(context.Context, time.Time) ([]store.AgentSessionView, error) {
			h.sessionN++
			if h.failView {
				return nil, errors.New("db down")
			}
			return h.views, nil
		},
		hosts: func(context.Context) ([]terminal.HostPresence, error) {
			h.hostN++
			return h.hosts, nil
		},
	}
	return h
}

func (h *excHarness) scan(advance time.Duration) []ntfy.ExceptionInfo {
	h.t.Helper()
	h.now = h.now.Add(advance)
	h.w.scan(context.Background())
	return h.sink.take()
}

func agentView(id uint, state agentstate.State, since time.Time) store.AgentSessionView {
	return store.AgentSessionView{
		ID: id, WorkflowID: "wf-demo", ProjectDir: "/home/me/Demo", HostName: "dev-box",
		State: string(state), Since: since, LastEventAt: since,
	}
}

func TestExceptionWatcher_NothingOnMeansNoQueries(t *testing.T) {
	h := newExcHarness(t) // 没有任何异常类被打开
	h.views = []store.AgentSessionView{agentView(1, agentstate.StateError, h.now.Add(-5*time.Minute))}
	h.hosts = []terminal.HostPresence{{MachineCode: "m1", Online: false}}
	for i := 0; i < 3; i++ {
		if got := h.scan(time.Minute); len(got) != 0 {
			t.Fatalf("没打开就不该推送：%v", got)
		}
	}
	if h.sessionN != 0 || h.hostN != 0 {
		t.Errorf("没打开的类一次库都不该查：sessions=%d hosts=%d", h.sessionN, h.hostN)
	}

	// 总开关关着时，即使某一类设了优先级也不巡检。
	h2 := newExcHarness(t, ntfy.KindAgentError)
	h2.sink.set(func(c *ntfy.Config) { c.Enabled = false })
	h2.views = h.views
	h2.scan(time.Minute)
	if h2.sessionN != 0 {
		t.Errorf("总开关关着不该巡检：%d", h2.sessionN)
	}

	// 只打开了主机掉线：不查 Agent 会话。
	h3 := newExcHarness(t, ntfy.KindHostOffline)
	h3.scan(time.Minute)
	if h3.sessionN != 0 || h3.hostN != 1 {
		t.Errorf("只该查主机：sessions=%d hosts=%d", h3.sessionN, h3.hostN)
	}
}

func TestExceptionWatcher_AgentError(t *testing.T) {
	h := newExcHarness(t, ntfy.KindAgentError)

	// 第一遍：已经存在的出错会话（10 分钟前）只记录，不推送。
	h.views = []store.AgentSessionView{agentView(1, agentstate.StateError, h.now.Add(-10*time.Minute))}
	if got := h.scan(0); len(got) != 0 {
		t.Fatalf("播种那一遍不该推送：%v", got)
	}

	// 新出错的会话：不到 1 分钟还不推（可能马上重试成功）。
	h.views = append(h.views, agentView(2, agentstate.StateError, h.now.Add(-30*time.Second)))
	if got := h.scan(0); len(got) != 0 {
		t.Fatalf("宽限期内不该推送：%v", got)
	}
	// 过了宽限期仍没恢复：推送一次。
	got := h.scan(40 * time.Second)
	if len(got) != 1 {
		t.Fatalf("应推送一条：%v", got)
	}
	e := got[0]
	if e.Kind != ntfy.KindAgentError || e.Sub != ntfy.SubAgentError || e.Key != "agent_error:2" ||
		e.Name != "Demo" || e.Host != "dev-box" || e.DeepLink != "/?wf=wf-demo" || e.Since.IsZero() {
		t.Errorf("内容不对：%+v", e)
	}
	// 同一段异常之后的每一遍都不再推。
	for i := 0; i < 3; i++ {
		if got := h.scan(30 * time.Second); len(got) != 0 {
			t.Fatalf("同一段异常不该重复推送：%v", got)
		}
	}

	// 被中断（而不是出错）用自己的细分。
	h.views = append(h.views, agentView(3, agentstate.StateAborted, h.now.Add(-2*time.Minute)))
	got = h.scan(0)
	if len(got) != 1 || got[0].Sub != ntfy.SubAgentAborted || !strings.Contains(got[0].Reason, "被中断") {
		t.Fatalf("被中断应单独成文：%v", got)
	}

	// 恢复之后在同一个对话里再出错：新的一段（Since 不同），推送器再按 Key 做 10 分钟去重。
	h.views = []store.AgentSessionView{agentView(2, agentstate.StateWorking, h.now)}
	h.scan(30 * time.Second)
	h.views = []store.AgentSessionView{agentView(2, agentstate.StateError, h.now)}
	h.scan(30 * time.Second)
	got = h.scan(40 * time.Second)
	if len(got) != 1 || got[0].Key != "agent_error:2" {
		t.Fatalf("再次出错应是新的一段：%v", got)
	}

	// 太旧的出错（超过 30 分钟）当旧事，哪怕它才刚挤进窗口。
	h.views = []store.AgentSessionView{agentView(9, agentstate.StateError, h.now.Add(-2*time.Hour))}
	if got := h.scan(30 * time.Second); len(got) != 0 {
		t.Fatalf("旧事不该推：%v", got)
	}
	// 已结束 / 空闲的会话不算。
	h.views = []store.AgentSessionView{agentView(10, agentstate.StateEnded, h.now.Add(-5*time.Minute)), agentView(11, agentstate.StateIdle, h.now.Add(-5*time.Minute))}
	if got := h.scan(30 * time.Second); len(got) != 0 {
		t.Fatalf("没出错的不该推：%v", got)
	}
}

func TestExceptionWatcher_AgentStalled(t *testing.T) {
	h := newExcHarness(t, ntfy.KindAgentStalled)

	stalled := func(id uint, reason string, since time.Time) store.AgentSessionView {
		v := agentView(id, agentstate.StateWorking, since)
		v.Stalled, v.Reason, v.QuietSeconds = true, reason, 45*60
		v.LastEventAt = h.now.Add(-45 * time.Minute)
		return v
	}
	h.views = []store.AgentSessionView{stalled(1, agentstate.ReasonQuiet, h.now.Add(-time.Hour))}
	if got := h.scan(0); len(got) != 0 {
		t.Fatalf("播种那一遍不该推送：%v", got)
	}

	// 一个新卡住的会话（静默）。
	h.views = append(h.views, stalled(2, agentstate.ReasonQuiet, h.now.Add(-50*time.Minute)))
	got := h.scan(30 * time.Second)
	if len(got) != 1 {
		t.Fatalf("应推送一条：%v", got)
	}
	e := got[0]
	if e.Kind != ntfy.KindAgentStalled || e.Key != "agent_stalled:2" || e.Name != "Demo" || !strings.Contains(e.Reason, "没有任何动静") {
		t.Errorf("内容不对：%+v", e)
	}
	if want := h.now.Add(-45 * time.Minute); e.Since.Sub(want) > time.Second || want.Sub(e.Since) > time.Second {
		t.Errorf("起点应是最近一次动静的时间：%v（现在 %v）", e.Since, h.now)
	}
	if got := h.scan(30 * time.Second); len(got) != 0 {
		t.Fatalf("同一次卡住只推一次：%v", got)
	}

	// 工具超时：起点是工具开始的时间。
	toolStart := h.now.Add(-20 * time.Minute)
	v := stalled(3, agentstate.ReasonToolOverrun, h.now.Add(-25*time.Minute))
	v.ToolStartedAt = &toolStart
	h.views = append(h.views, v)
	got = h.scan(30 * time.Second)
	if len(got) != 1 || !strings.Contains(got[0].Reason, "工具调用") || !got[0].Since.Equal(toolStart) {
		t.Fatalf("工具超时应单独成文并以工具开始为起点：%v", got)
	}

	// 托管终端冻结。
	h.views = append(h.views, stalled(4, agentstate.ReasonTerminalFrozen, h.now.Add(-10*time.Minute)))
	got = h.scan(30 * time.Second)
	if len(got) != 1 || !strings.Contains(got[0].Reason, "屏幕") || !got[0].Since.IsZero() {
		t.Fatalf("终端冻结应单独成文：%v", got)
	}

	// 会话 2 恢复后再次卡住（状态起点不同）→ 新的一段。
	h.views = []store.AgentSessionView{agentView(2, agentstate.StateWorking, h.now)}
	h.scan(30 * time.Second)
	h.views = []store.AgentSessionView{stalled(2, agentstate.ReasonQuiet, h.now.Add(-40*time.Minute))}
	if got := h.scan(30 * time.Second); len(got) != 1 {
		t.Fatalf("恢复后再次卡住应算新的一段：%v", got)
	}

	// 「可能卡住」（软标记）只在界面里提示，不推送；最近动静太旧的当旧事。
	soft := agentView(5, agentstate.StateWorking, h.now.Add(-15*time.Minute))
	soft.StalledSoft, soft.Reason = true, agentstate.ReasonQuietSoft
	old := stalled(6, agentstate.ReasonQuiet, h.now.Add(-9*time.Hour))
	old.LastEventAt = h.now.Add(-8 * time.Hour)
	h.views = []store.AgentSessionView{soft, old}
	if got := h.scan(30 * time.Second); len(got) != 0 {
		t.Fatalf("软标记与旧事都不该推：%v", got)
	}
}

func TestExceptionWatcher_HostOffline(t *testing.T) {
	h := newExcHarness(t, ntfy.KindHostOffline)

	// 第一遍：m1 已经离线（起点不详），m2 在线。不推送。
	h.hosts = []terminal.HostPresence{{MachineCode: "m1", Name: "老机器", Online: false}, {MachineCode: "m2", Name: "build-01", Online: true}}
	if got := h.scan(0); len(got) != 0 {
		t.Fatalf("播种那一遍不该推送：%v", got)
	}
	// m1 一直离线：仍然不推（它不是新事件）。
	if got := h.scan(10 * time.Minute); len(got) != 0 {
		t.Fatalf("早就离线的主机不该推：%v", got)
	}

	// m2 掉线：5 分钟内不推（抖动），满 5 分钟推一次。
	h.hosts[1].Online = false
	dropAt := h.now.Add(30 * time.Second)
	if got := h.scan(30 * time.Second); len(got) != 0 {
		t.Fatalf("刚掉线不该推：%v", got)
	}
	if got := h.scan(4 * time.Minute); len(got) != 0 {
		t.Fatalf("还没满 5 分钟不该推：%v", got)
	}
	got := h.scan(time.Minute)
	if len(got) != 1 {
		t.Fatalf("满 5 分钟应推一条：%v", got)
	}
	e := got[0]
	if e.Kind != ntfy.KindHostOffline || e.Key != "host_offline:m2" || e.Name != "build-01" || !e.Since.Equal(dropAt) {
		t.Errorf("内容不对：%+v（起点应是第一次看到离线的时间 %v）", e, dropAt)
	}
	if got := h.scan(time.Minute); len(got) != 0 {
		t.Fatalf("同一次掉线只推一次：%v", got)
	}

	// 短暂抖动：m2 回来、再离线不满 5 分钟又回来 → 不推。
	h.hosts[1].Online = true
	h.scan(30 * time.Second)
	h.hosts[1].Online = false
	h.scan(30 * time.Second)
	h.scan(2 * time.Minute)
	h.hosts[1].Online = true
	if got := h.scan(30 * time.Second); len(got) != 0 {
		t.Fatalf("抖动不该推：%v", got)
	}

	// 再次真的掉线 → 新的一段。
	h.hosts[1].Online = false
	h.scan(30 * time.Second)
	if got := h.scan(5 * time.Minute); len(got) != 1 {
		t.Fatalf("再次掉线满 5 分钟应再推一条：%v", got)
	}

	// 主机被删除 / 停用后不再出现在列表里：记录被清掉，不残留。
	h.hosts = h.hosts[:1]
	h.scan(30 * time.Second)
	if _, ok := h.w.offlineSince["m2"]; ok {
		t.Error("不在列表里的主机应清掉离线记录")
	}
}

func TestExceptionWatcher_ReseedsWhenKindTurnedBackOn(t *testing.T) {
	h := newExcHarness(t, ntfy.KindAgentError)
	h.views = []store.AgentSessionView{agentView(1, agentstate.StateError, h.now.Add(-10*time.Minute))}
	h.scan(0) // 播种

	// 关掉这一类期间，又出了一个错；再打开时那是「已经存在的」，不推送。
	h.sink.set(func(c *ntfy.Config) { c.PriorityByKind = map[string]int{} })
	h.views = append(h.views, agentView(2, agentstate.StateError, h.now.Add(time.Minute)))
	h.scan(5 * time.Minute)
	h.sink.set(func(c *ntfy.Config) { c.PriorityByKind = map[string]int{ntfy.KindAgentError: 4} })
	if got := h.scan(30 * time.Second); len(got) != 0 {
		t.Fatalf("重新打开后的第一遍应只记录：%v", got)
	}
	// 之后新出现的照常推送。
	h.views = append(h.views, agentView(3, agentstate.StateError, h.now.Add(-2*time.Minute)))
	if got := h.scan(30 * time.Second); len(got) != 1 || got[0].Key != "agent_error:3" {
		t.Fatalf("播种之后新出现的应推送：%v", got)
	}
}

func TestExceptionWatcher_SourceFailuresAreNotFatal(t *testing.T) {
	h := newExcHarness(t, ntfy.KindAgentError, ntfy.KindHostOffline)
	h.failView = true
	h.hosts = []terminal.HostPresence{{MachineCode: "m1", Online: true}}
	if got := h.scan(0); len(got) != 0 {
		t.Fatalf("读会话失败不该推送：%v", got)
	}
	// 读配置失败：这一遍整个跳过。
	h.sink.err = errors.New("db down")
	n := h.sessionN
	h.scan(time.Minute)
	if h.sessionN != n {
		t.Error("读不到配置时不该继续巡检")
	}
	// nil 的来源（没有终端子系统 / 没有库）直接跳过对应的那一类。
	bare := &exceptionWatcher{sink: h.sink, now: time.Now, offlineSince: map[string]time.Time{}}
	h.sink.err = nil
	bare.scan(context.Background())
}

func TestExceptionWatcher_SecurityHooks(t *testing.T) {
	h := newExcHarness(t, ntfy.KindSecurityAlert)
	hook := h.w.SecurityHook()

	for ev, wantSub := range map[string]string{
		terminal.EvOAuthCodeReplay:    ntfy.SubOAuthCodeReplay,
		terminal.EvOAuthRefreshReplay: ntfy.SubOAuthRefreshReplay,
		terminal.EvUnlockLocked:       ntfy.SubUnlockLocked,
	} {
		hook(terminal.SecurityAlert{Event: ev, Result: "deny", Detail: "10.9.8.7 secret-detail"})
		got := h.sink.take()
		if len(got) != 1 {
			t.Fatalf("%s：应送出一条：%v", ev, got)
		}
		e := got[0]
		if e.Kind != ntfy.KindSecurityAlert || e.Sub != wantSub || e.Key != "security:"+wantSub || e.Reason == "" {
			t.Errorf("%s：内容不对：%+v", ev, e)
		}
		// 审计详情（里面可能有 IP 等）绝不进入推送内容。
		if strings.Contains(e.Reason+e.Name+e.Host, "10.9.8.7") || strings.Contains(e.Reason, "secret-detail") {
			t.Errorf("%s：推送内容不该带审计详情：%+v", ev, e)
		}
	}

	hook(terminal.SecurityAlert{Event: "some_other_event"})
	if got := h.sink.take(); len(got) != 0 {
		t.Errorf("不认识的事件不该送出：%v", got)
	}

	h.w.LoginLockedHook()()
	got := h.sink.take()
	if len(got) != 1 || got[0].Sub != ntfy.SubLoginLocked || got[0].Key != "security:login_locked" || got[0].Kind != ntfy.KindSecurityAlert {
		t.Errorf("登录锁定应送出一条：%v", got)
	}
}

func TestStalledReason_NeverCarriesToolOrTerminalContent(t *testing.T) {
	now := time.Now()
	tool := now.Add(-time.Hour)
	for _, v := range []store.AgentSessionView{
		{Reason: agentstate.ReasonQuiet, QuietSeconds: 3000, LastTool: "Shell rm -rf /secret"},
		{Reason: agentstate.ReasonToolOverrun, ToolStartedAt: &tool, LastTool: "Shell rm -rf /secret"},
		{Reason: agentstate.ReasonTerminalFrozen, LiveWord: "Thinking secret-word"},
	} {
		reason, _ := stalledReason(v, now)
		if strings.Contains(reason, "secret") || strings.Contains(reason, "rm -rf") {
			t.Errorf("卡住原因不该带工具参数 / 终端内容：%q", reason)
		}
	}
}
