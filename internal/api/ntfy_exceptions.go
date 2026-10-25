package api

import (
	"context"
	"fmt"
	"log"
	"sort"
	"sync"
	"time"

	"github.com/teacat99/RelayMesh/v2/internal/agentstate"
	"github.com/teacat99/RelayMesh/v2/internal/ntfy"
	"github.com/teacat99/RelayMesh/v2/internal/store"
	"github.com/teacat99/RelayMesh/v2/internal/terminal"
)

// 「异常」推送的发现端（#235）：把 Agent 出错 / 被中断、Agent 疑似卡住、ExecMesh 主机掉线、安全告警
// 这四类事实翻译成 ntfy.ExceptionInfo 交给推送器。
//
// 分工见 internal/ntfy/exception.go：这里负责「什么时候算异常、同一段异常只报一次」，推送器负责这一类开没开、
// 去重与限流、拼文案。前三类是「状态」（此刻有哪些对象处在异常里），由一个后台巡检器定时扫一遍，
// 用 episodeTracker 找出「新出现」的那些；安全告警是「事件」，由审计挂钩与登录锁定挂钩直接送来。
//
// 巡检只在对应的那一类被打开时才查库（总开关关着、或这一类设成不推送时，一次都不查），
// 并且打开之后的第一遍只记录、不推送——已经存在的异常不算新事件，免得刚打开开关或刚热更完就被一串旧事刷屏。

const (
	// 启动后先等这么久再巡检第一遍：让主机的 Agent 先重连、Agent 状态先恢复，不把「刚重启」当成掉线。
	exceptionFirstDelay = time.Minute
	exceptionScanEvery  = 30 * time.Second
	exceptionScanLimit  = 20 * time.Second

	// Agent 出错 / 被中断之后，这么久还没恢复才推送：中断之后马上接着发话（手动停一下再继续）、
	// 报错之后马上重试成功，都不值得打扰你。超过 agentErrorMaxAge 的算旧事，不再当新消息推。
	agentErrorGrace  = time.Minute
	agentErrorMaxAge = 30 * time.Minute
	// 最近一次动静早于这个时间的会话，即使被判成「卡住」也是旧事（比如会话行刚被挤进最近 200 条的窗口）。
	agentStallMaxIdle = 6 * time.Hour
	// 主机掉线超过这么久才推送：网络抖动、主机重启、Agent 升级重连都在几分钟内回来。
	hostOfflineGrace = 5 * time.Minute
	// 每遍最多看多少个 Agent 会话（按最近动静排序，和界面的 Agent 状态列表同一个口径）。
	exceptionAgentLimit = 200
)

// episodeTracker 把「此刻有哪些对象处在异常里」的周期性快照，翻译成「哪些异常是新出现的」。
// 对象恢复之后从集合里消失，再次出现就是一段新的异常——「每次卡住只推一次，恢复后重新计」就是这么来的。
type episodeTracker struct {
	seeded bool
	active map[string]struct{}
}

// Observe 接收此刻处在异常里的集合，返回其中上一遍没有的（排好序）。第一遍只记录、返回空：
// 刚启动或刚打开推送时已经存在的异常不算新事件。
func (t *episodeTracker) Observe(bad map[string]struct{}) []string {
	prev := t.active
	t.active = bad
	if !t.seeded {
		t.seeded = true
		return nil
	}
	var fresh []string
	for k := range bad {
		if _, was := prev[k]; !was {
			fresh = append(fresh, k)
		}
	}
	sort.Strings(fresh)
	return fresh
}

// Reset 回到未播种状态：这一类被关掉之后再打开，要重新播种。
func (t *episodeTracker) Reset() {
	t.seeded = false
	t.active = nil
}

// exceptionSink 是巡检器对推送器的全部依赖（便于测试时换成假的）。*ntfy.Notifier 满足它。
type exceptionSink interface {
	NotifyException(ntfy.ExceptionInfo)
	EffectiveConfig(ctx context.Context) (ntfy.Config, error)
}

type exceptionWatcher struct {
	sink     exceptionSink
	sessions func(ctx context.Context, now time.Time) ([]store.AgentSessionView, error)
	hosts    func(ctx context.Context) ([]terminal.HostPresence, error)
	now      func() time.Time

	mu           sync.Mutex // 保护下面几项（每遍巡检持有）
	errEps       episodeTracker
	stallEps     episodeTracker
	hostEps      episodeTracker
	offlineSince map[string]time.Time

	lifeMu sync.Mutex
	cancel context.CancelFunc
}

// newExceptionWatcher 接线：st 提供 Agent 会话，hub 提供主机在线情况（都可以为 nil，对应的那类就不巡检）。
func newExceptionWatcher(sink exceptionSink, st *store.Store, hub *terminal.Hub) *exceptionWatcher {
	w := &exceptionWatcher{sink: sink, now: time.Now, offlineSince: make(map[string]time.Time)}
	if st != nil {
		w.sessions = func(ctx context.Context, now time.Time) ([]store.AgentSessionView, error) {
			return st.ListAgentSessions(ctx, store.AgentSessionFilter{Limit: exceptionAgentLimit}, now)
		}
	}
	if hub != nil {
		w.hosts = hub.HostPresence
	}
	return w
}

// start 启动后台巡检；重复调用无副作用。stop 让它退出。
func (w *exceptionWatcher) start() {
	w.lifeMu.Lock()
	defer w.lifeMu.Unlock()
	if w.cancel != nil {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	w.cancel = cancel
	go func() {
		timer := time.NewTimer(exceptionFirstDelay)
		defer timer.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-timer.C:
			}
			w.safeScan(ctx)
			timer.Reset(exceptionScanEvery)
		}
	}()
}

func (w *exceptionWatcher) stop() {
	w.lifeMu.Lock()
	defer w.lifeMu.Unlock()
	if w.cancel != nil {
		w.cancel()
		w.cancel = nil
	}
}

// safeScan 保证巡检里任何意外都不会带走进程或这个协程。
func (w *exceptionWatcher) safeScan(parent context.Context) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[ntfy] 异常巡检发生内部错误：%v", r)
		}
	}()
	ctx, cancel := context.WithTimeout(parent, exceptionScanLimit)
	defer cancel()
	w.scan(ctx)
}

// scan 巡检一遍。这一类没打开就不查库，并让它回到未播种状态。
func (w *exceptionWatcher) scan(ctx context.Context) {
	cfg, err := w.sink.EffectiveConfig(ctx)
	if err != nil {
		return
	}
	live := cfg.Enabled && cfg.Topic != ""
	want := func(kind string) bool {
		if !live {
			return false
		}
		_, on := cfg.PriorityFor(kind)
		return on
	}
	wantErr, wantStall, wantHost := want(ntfy.KindAgentError), want(ntfy.KindAgentStalled), want(ntfy.KindHostOffline)

	w.mu.Lock()
	defer w.mu.Unlock()
	if !wantErr {
		w.errEps.Reset()
	}
	if !wantStall {
		w.stallEps.Reset()
	}
	if !wantHost {
		w.hostEps.Reset()
		w.offlineSince = make(map[string]time.Time)
	}
	now := w.now()

	if (wantErr || wantStall) && w.sessions != nil {
		if views, err := w.sessions(ctx, now); err != nil {
			log.Printf("[ntfy] 异常巡检读取 Agent 会话失败：%v", err)
		} else {
			w.scanAgents(views, now, wantErr, wantStall)
		}
	}
	if wantHost && w.hosts != nil {
		if hosts, err := w.hosts(ctx); err != nil {
			log.Printf("[ntfy] 异常巡检读取主机状态失败：%v", err)
		} else {
			w.scanHosts(hosts, now)
		}
	}
}

// scanAgents 找出新出现的「出错 / 被中断」与「卡住」。episode 键带上状态的起点：同一个对话里再出一次错是新的一段。
func (w *exceptionWatcher) scanAgents(views []store.AgentSessionView, now time.Time, wantErr, wantStall bool) {
	errBad, stallBad := map[string]struct{}{}, map[string]struct{}{}
	info := map[string]ntfy.ExceptionInfo{}

	for _, v := range views {
		name := projectTitle(v.ProjectDir, v.WorkflowID)
		switch {
		case wantErr && (v.State == string(agentstate.StateError) || v.State == string(agentstate.StateAborted)):
			age := now.Sub(v.Since)
			if age < agentErrorGrace || age > agentErrorMaxAge {
				continue
			}
			sub, reason := ntfy.SubAgentError, "上一轮对话因出错中断，之后没有恢复"
			if v.State == string(agentstate.StateAborted) {
				sub, reason = ntfy.SubAgentAborted, "上一轮对话被中断，之后没有恢复"
			}
			ep := fmt.Sprintf("agent_error:%d:%d", v.ID, v.Since.Unix())
			errBad[ep] = struct{}{}
			info[ep] = ntfy.ExceptionInfo{
				Kind: ntfy.KindAgentError, Sub: sub,
				// 推送器按这个 Key 做「同一个对话 10 分钟内只推一次」；不带起点，所以反复出错也不会刷屏。
				Key:  fmt.Sprintf("agent_error:%d", v.ID),
				Name: name, Host: v.HostName, Reason: reason, Since: v.Since, DeepLink: deepLinkTo(v.WorkflowID, ""),
			}
		case wantStall && v.Stalled:
			if now.Sub(v.LastEventAt) > agentStallMaxIdle {
				continue
			}
			reason, since := stalledReason(v, now)
			ep := fmt.Sprintf("agent_stalled:%d:%d", v.ID, v.Since.Unix())
			stallBad[ep] = struct{}{}
			info[ep] = ntfy.ExceptionInfo{
				Kind: ntfy.KindAgentStalled, Key: fmt.Sprintf("agent_stalled:%d", v.ID),
				Name: name, Host: v.HostName, Reason: reason, Since: since, DeepLink: deepLinkTo(v.WorkflowID, ""),
			}
		}
	}

	if wantErr {
		for _, ep := range w.errEps.Observe(errBad) {
			w.sink.NotifyException(info[ep])
		}
	}
	if wantStall {
		for _, ep := range w.stallEps.Observe(stallBad) {
			w.sink.NotifyException(info[ep])
		}
	}
}

// stalledReason 把状态判断给出的卡住原因翻成一句人话，并给出起点（不详时为零值）。
// 文案只描述现象，不含终端内容、工具参数与任何凭据。
func stalledReason(v store.AgentSessionView, now time.Time) (string, time.Time) {
	switch v.Reason {
	case agentstate.ReasonToolOverrun:
		since := time.Time{}
		if v.ToolStartedAt != nil {
			since = *v.ToolStartedAt
		}
		return "一次工具调用运行得比正常时间长得多", since
	case agentstate.ReasonTerminalFrozen:
		return "托管终端的屏幕没有任何输出", time.Time{}
	}
	// 默认（静默超过阈值）：起点取最近一次动静的时间。
	return "长时间没有任何动静", now.Add(-time.Duration(v.QuietSeconds) * time.Second)
}

// scanHosts 找出新出现的「主机掉线」：从在线变成离线、并且超过 hostOfflineGrace 还没回来。
// 数据库里的「最近上线」只在连接建立时更新，掉线时刻只能由巡检自己记：第一次看到某台主机离线的时间就是起点
// （误差不超过一个巡检间隔）。第一遍巡检时已经离线的主机起点不详，当作早就掉线，不算新事件。
func (w *exceptionWatcher) scanHosts(hosts []terminal.HostPresence, now time.Time) {
	bad := map[string]struct{}{}
	info := map[string]ntfy.ExceptionInfo{}
	present := make(map[string]struct{}, len(hosts))

	for _, p := range hosts {
		present[p.MachineCode] = struct{}{}
		if p.Online {
			delete(w.offlineSince, p.MachineCode)
			continue
		}
		since, known := w.offlineSince[p.MachineCode]
		if !known {
			since = now
			if !w.hostEps.seeded {
				since = now.Add(-hostOfflineGrace)
			}
			w.offlineSince[p.MachineCode] = since
		}
		if now.Sub(since) < hostOfflineGrace {
			continue
		}
		key := "host_offline:" + p.MachineCode
		bad[key] = struct{}{}
		info[key] = ntfy.ExceptionInfo{
			Kind: ntfy.KindHostOffline, Key: key, Name: p.Name,
			Reason: "这台主机上的 Agent 已经断开连接", Since: since, DeepLink: "/exec",
		}
	}
	// 被删除 / 停用的主机不再出现在列表里：清掉它的记录。
	for code := range w.offlineSince {
		if _, ok := present[code]; !ok {
			delete(w.offlineSince, code)
		}
	}
	for _, key := range w.hostEps.Observe(bad) {
		w.sink.NotifyException(info[key])
	}
}

// securityAlertText 把审计里的高危事件翻成推送里的细分与一句原因。原因只描述发生了什么：
// 不带来源 IP、不带审计详情、不带任何令牌（这些只在控制台的审计日志里）。
func securityAlertText(event string) (sub, reason string) {
	switch event {
	case terminal.EvOAuthCodeReplay:
		return ntfy.SubOAuthCodeReplay, "同一个 OAuth 授权码被重复兑换，可能被人截获了。"
	case terminal.EvOAuthRefreshReplay:
		return ntfy.SubOAuthRefreshReplay, "一个 OAuth 刷新令牌被重复使用，令牌可能已经泄露。"
	case terminal.EvUnlockLocked:
		return ntfy.SubUnlockLocked, "终端解锁连续多次密码错误，已临时锁定。"
	}
	return "", ""
}

// SecurityHook 返回装到终端 Hub 上的安全告警挂钩（审计里的重放与解锁锁定）。
// 同一种事件 10 分钟内最多推一条，期间重复发生的次数合并进下一条（由推送器按 Key 去重并计数）。
func (w *exceptionWatcher) SecurityHook() func(terminal.SecurityAlert) {
	return func(a terminal.SecurityAlert) {
		sub, reason := securityAlertText(a.Event)
		if sub == "" {
			return
		}
		w.notifySecurity(sub, reason)
	}
}

// LoginLockedHook 返回装到登录处理上的挂钩：连续登录失败触发了 IP 锁定。
func (w *exceptionWatcher) LoginLockedHook() func() {
	return func() {
		w.notifySecurity(ntfy.SubLoginLocked, "登录连续多次失败，来源已被临时锁定。")
	}
}

func (w *exceptionWatcher) notifySecurity(sub, reason string) {
	w.sink.NotifyException(ntfy.ExceptionInfo{
		Kind: ntfy.KindSecurityAlert, Sub: sub, Key: "security:" + sub,
		Reason: reason, Since: w.now(), DeepLink: "/exec",
	})
}
