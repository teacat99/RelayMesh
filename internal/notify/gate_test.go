package notify

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"testing"
	"time"
)

// gateRig 记录两条通道各收到了什么，并让测试控制偏好与时钟。
type gateRig struct {
	mu        sync.Mutex
	prefs     Prefs
	loadErr   error
	clock     time.Time
	broadcast []Payload
	ntfy      []Event
	ntfyD     []Decision
	gate      *Gate
}

func newRig(t *testing.T) *gateRig {
	t.Helper()
	r := &gateRig{clock: t0, prefs: Prefs{NtfyPreview: PreviewAuto}}
	r.gate = NewGate(Options{
		Load: func(context.Context) (Prefs, error) {
			r.mu.Lock()
			defer r.mu.Unlock()
			return r.prefs, r.loadErr
		},
		Broadcast: func(typ string, data any) {
			if typ != SSEEventName {
				panic("事件名应是 notify：" + typ)
			}
			r.mu.Lock()
			r.broadcast = append(r.broadcast, data.(Payload))
			r.mu.Unlock()
		},
		Now: func() time.Time {
			r.mu.Lock()
			defer r.mu.Unlock()
			return r.clock
		},
	})
	return r
}

func (r *gateRig) publish(ev Event) Decision {
	return r.gate.Publish(ev, func(e Event, d Decision) {
		r.mu.Lock()
		r.ntfy = append(r.ntfy, e)
		r.ntfyD = append(r.ntfyD, d)
		r.mu.Unlock()
	})
}

func (r *gateRig) counts() (browser, ntfy int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.broadcast), len(r.ntfy)
}

func (r *gateRig) advance(d time.Duration) {
	r.mu.Lock()
	r.clock = r.clock.Add(d)
	r.mu.Unlock()
}

func evFull(id string, kind Kind) Event {
	return Event{ID: id, Kind: kind, Conversation: conv1to1, WorkflowID: "demo", SessionID: "s1",
		Sender: "后端组", Title: "RelayMesh", Preview: "预览正文-ABC", DeepLink: "/?wf=demo&s=s1"}
}

func TestGatePublishDeliversToBothChannels(t *testing.T) {
	r := newRig(t)
	d := r.publish(evFull("e1", KindAgentText))
	if !d.Browser || !d.Ntfy || d.Reason != ReasonDelivered {
		t.Fatalf("默认偏好下应放行：%+v", d)
	}
	b, n := r.counts()
	if b != 1 || n != 1 {
		t.Fatalf("两条通道各应收到 1 条：browser=%d ntfy=%d", b, n)
	}
	p := r.broadcast[0]
	if p.ID != "e1" || p.Kind != KindAgentText || p.Conversation != conv1to1 || p.WorkflowID != "demo" || p.SessionID != "s1" ||
		p.Sender != "后端组" || p.Title != "RelayMesh" || p.Preview != "预览正文-ABC" || p.DeepLink != "/?wf=demo&s=s1" ||
		p.Penetrate || !p.Sound || !p.Badge || !p.CreatedAt.Equal(t0) {
		t.Fatalf("SSE 载荷不对：%+v", p)
	}
	if r.ntfy[0].ID != "e1" || r.ntfyD[0].Preview != true {
		t.Fatalf("ntfy 回调应拿到规整后的事件与裁决：%+v %+v", r.ntfy[0], r.ntfyD[0])
	}
}

func TestGatePublishRespectsPrefs(t *testing.T) {
	r := newRig(t)
	r.prefs = Prefs{DND: true, NtfyPreview: PreviewAuto}

	// 免打扰：受约束类两条通道都不发；穿透类照常到达。
	if d := r.publish(evFull("a1", KindAgentText)); d.Delivered() || d.Reason != ReasonDND {
		t.Fatalf("免打扰应挡住 agent_text：%+v", d)
	}
	if b, n := r.counts(); b != 0 || n != 0 {
		t.Fatalf("被挡下的事件不应触达任何通道：browser=%d ntfy=%d", b, n)
	}
	if d := r.publish(evFull("a2", KindBlockingFeedback)); !d.Browser || !d.Ntfy || !d.Penetrate {
		t.Fatalf("穿透类不受免打扰影响：%+v", d)
	}
	if b, n := r.counts(); b != 1 || n != 1 {
		t.Fatalf("穿透类应到达两条通道：browser=%d ntfy=%d", b, n)
	}
	if !r.broadcast[0].Penetrate {
		t.Fatal("穿透类的 SSE 载荷应带 penetrate=true，浏览器据此跳过前台抑制")
	}

	// 会话静音：只挡这个会话。
	r.prefs = Prefs{NtfyPreview: PreviewAuto, Conversations: map[string]ConvPref{conv1to1: {Policy: PolicyMuted}}}
	if d := r.publish(evFull("b1", KindAskCard)); d.Delivered() || d.Reason != ReasonMuted {
		t.Fatalf("会话静音应挡住 ask_card：%+v", d)
	}
	other := evFull("b2", KindAskCard)
	other.Conversation = "wf:other"
	if d := r.publish(other); !d.Delivered() {
		t.Fatalf("其他会话不受影响：%+v", d)
	}

	// run_end 只有 ntfy 通道。
	r2 := newRig(t)
	if d := r2.publish(evFull("r1", KindRunEnd)); d.Browser || !d.Ntfy {
		t.Fatalf("run_end 只应走 ntfy：%+v", d)
	}
	if b, n := r2.counts(); b != 0 || n != 1 {
		t.Fatalf("run_end 不应发 SSE：browser=%d ntfy=%d", b, n)
	}
}

func TestGatePublishPromotesMention(t *testing.T) {
	r := newRig(t)
	r.prefs = Prefs{DND: true, NtfyPreview: PreviewAuto, Conversations: map[string]ConvPref{convGroup: {Policy: PolicyMuted}}}
	ev := evFull("m1", KindAgentText)
	ev.Conversation, ev.Mention = convGroup, true
	d := r.publish(ev)
	if d.Kind != KindMention || !d.Penetrate || !d.Browser || !d.Ntfy {
		t.Fatalf("点名应升格为 mention 并穿透：%+v", d)
	}
	if r.broadcast[0].Kind != KindMention || !r.broadcast[0].Penetrate {
		t.Fatalf("SSE 载荷里应是升格后的种类：%+v", r.broadcast[0])
	}
	if r.ntfy[0].Kind != KindMention {
		t.Fatalf("ntfy 回调拿到的事件应是升格后的种类：%+v", r.ntfy[0])
	}
}

func TestGatePublishIsIdempotent(t *testing.T) {
	r := newRig(t)
	if d := r.publish(evFull("dup", KindAgentText)); !d.Delivered() {
		t.Fatalf("首次应放行：%+v", d)
	}
	if d := r.publish(evFull("dup", KindAgentText)); d.Delivered() || d.Reason != ReasonDuplicate {
		t.Fatalf("同一个 ID 第二次应被忽略：%+v", d)
	}
	if b, n := r.counts(); b != 1 || n != 1 {
		t.Fatalf("重复发布不应再触达通道：browser=%d ntfy=%d", b, n)
	}
	// 被偏好挡下的事件同样记入幂等：之后改了偏好，同一个 ID 的重发也不会补发。
	r.prefs = Prefs{DND: true, NtfyPreview: PreviewAuto}
	r.publish(evFull("quiet", KindAgentText))
	r.prefs = Prefs{NtfyPreview: PreviewAuto}
	if d := r.publish(evFull("quiet", KindAgentText)); d.Reason != ReasonDuplicate {
		t.Fatalf("被挡下过的 ID 重发也应判重：%+v", d)
	}
}

func TestGatePublishConcurrentSameIDDeliversOnce(t *testing.T) {
	r := newRig(t)
	var wg sync.WaitGroup
	for i := 0; i < 64; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r.publish(evFull("race", KindBlockingFeedback))
		}()
	}
	wg.Wait()
	if b, n := r.counts(); b != 1 || n != 1 {
		t.Fatalf("并发发布同一个 ID 应只通知一次：browser=%d ntfy=%d", b, n)
	}
}

func TestGatePublishRejectsInvalidEvents(t *testing.T) {
	r := newRig(t)
	for name, ev := range map[string]Event{
		"没有 ID":    {Kind: KindAgentText, Conversation: conv1to1},
		"没有种类":     {ID: "x1", Conversation: conv1to1},
		"未登记的种类":   {ID: "x2", Kind: "bogus", Conversation: conv1to1},
		"ID 过长":    {ID: strings.Repeat("x", 300), Kind: KindAgentText},
		"只有空白的 ID": {ID: "   ", Kind: KindAgentText},
	} {
		if d := r.publish(ev); d.Reason != ReasonInvalid || d.Delivered() {
			t.Errorf("%s：应被丢弃：%+v", name, d)
		}
	}
	if b, n := r.counts(); b != 0 || n != 0 {
		t.Fatalf("无效事件不应触达通道：browser=%d ntfy=%d", b, n)
	}
}

func TestGateNeverNotifiesTaskUpdates(t *testing.T) {
	r := newRig(t)
	for i, k := range []Kind{KindTaskUpdate, KindSystemEvent} {
		if d := r.publish(evFull(fmt.Sprintf("n%d", i), k)); d.Delivered() || d.Reason != ReasonNever {
			t.Fatalf("%s 不应通知：%+v", k, d)
		}
	}
	if b, n := r.counts(); b != 0 || n != 0 {
		t.Fatalf("task_update / system_event 不应触达通道：browser=%d ntfy=%d", b, n)
	}
}

func TestGatePrefsLoadErrorFallsBackToDefaults(t *testing.T) {
	r := newRig(t)
	r.loadErr = errors.New("db down")
	r.prefs = Prefs{DND: true} // 读取失败时这份不会被采用
	d := r.publish(evFull("e1", KindAgentText))
	if !d.Delivered() {
		t.Fatalf("读不出偏好时应按默认偏好放行（宁可多通知）：%+v", d)
	}
	// Load 为 nil 同理。
	g := NewGate(Options{})
	if d := g.Publish(evFull("e2", KindAgentText), nil); !d.Delivered() {
		t.Fatalf("没有偏好来源时应按默认放行：%+v", d)
	}
}

func TestGateChannelsAreIndependent(t *testing.T) {
	var ntfyCalls int
	g := NewGate(Options{
		Load:      func(context.Context) (Prefs, error) { return Prefs{}, nil },
		Broadcast: func(string, any) { panic("广播炸了") },
	})
	// 广播 panic 不能传出来，也不能影响 ntfy 通道。
	d := g.Publish(evFull("e1", KindBlockingFeedback), func(Event, Decision) { ntfyCalls++ })
	if !d.Browser || ntfyCalls != 1 {
		t.Fatalf("广播出错时 ntfy 通道仍应工作：d=%+v calls=%d", d, ntfyCalls)
	}

	var broadcasts int
	g = NewGate(Options{
		Load:      func(context.Context) (Prefs, error) { return Prefs{}, nil },
		Broadcast: func(string, any) { broadcasts++ },
	})
	g.Publish(evFull("e2", KindBlockingFeedback), func(Event, Decision) { panic("ntfy 炸了") })
	if broadcasts != 1 {
		t.Fatalf("ntfy 出错时广播通道应已完成：%d", broadcasts)
	}

	// 没有 ntfy 回调 / 没有广播器都是允许的。
	g = NewGate(Options{Load: func(context.Context) (Prefs, error) { return Prefs{}, nil }})
	if d := g.Publish(evFull("e3", KindBlockingFeedback), nil); !d.Delivered() {
		t.Fatalf("没有任何通道回调时只返回裁决：%+v", d)
	}
}

func TestGateNilIsSafe(t *testing.T) {
	var g *Gate
	if d := g.Publish(evFull("e1", KindBlockingFeedback), nil); d.Delivered() {
		t.Fatalf("nil 闸不应通知：%+v", d)
	}
}

func TestGateSeenEviction(t *testing.T) {
	r := newRig(t)
	r.publish(evFull("old", KindAgentText))
	if d := r.publish(evFull("old", KindAgentText)); d.Reason != ReasonDuplicate {
		t.Fatal("窗口内应判重")
	}
	// 超过保留时间之后，同一个 ID 视为新事件（记录已过期被淘汰）。
	r.advance(seenTTL + time.Second)
	r.publish(evFull("tick", KindAgentText)) // 触发淘汰
	if d := r.publish(evFull("old", KindAgentText)); !d.Delivered() {
		t.Fatalf("过期之后应能再次通知：%+v", d)
	}

	// 容量：超过上限时淘汰最老的，最近的仍在。
	r2 := newRig(t)
	for i := 0; i < seenCap+50; i++ {
		r2.publish(evFull(fmt.Sprintf("id-%d", i), KindRunEnd))
	}
	r2.gate.mu.Lock()
	size := len(r2.gate.seen)
	r2.gate.mu.Unlock()
	if size > seenCap {
		t.Fatalf("幂等记录不应超过容量 %d，实际 %d", seenCap, size)
	}
	if d := r2.publish(evFull(fmt.Sprintf("id-%d", seenCap+49), KindRunEnd)); d.Reason != ReasonDuplicate {
		t.Fatalf("最近的 ID 仍应判重：%+v", d)
	}
	if d := r2.publish(evFull("id-0", KindRunEnd)); !d.Delivered() {
		t.Fatalf("最老的 ID 已被淘汰，应视为新事件：%+v", d)
	}
}

func TestGateNeverLogsContent(t *testing.T) {
	var buf bytes.Buffer
	old := log.Writer()
	log.SetOutput(&buf)
	defer log.SetOutput(old)

	secret := "绝密预览正文-ZXCV"
	r := newRig(t)
	r.loadErr = errors.New("db down")
	ev := evFull("e1", KindAgentText)
	ev.Preview = secret
	r.publish(ev)                                            // 偏好读取失败的日志
	r.publish(ev)                                            // 重复
	r.publish(Event{ID: "", Kind: "bogus", Preview: secret}) // 无效
	g := NewGate(Options{
		Load:      func(context.Context) (Prefs, error) { return Prefs{}, nil },
		Broadcast: func(string, any) { panic("boom") },
	})
	bad := evFull("e2", KindBlockingFeedback)
	bad.Preview = secret
	g.Publish(bad, func(Event, Decision) { panic("boom") }) // 通道 panic 的日志
	if strings.Contains(buf.String(), "ZXCV") || strings.Contains(buf.String(), "绝密") {
		t.Fatalf("日志里不应出现预览正文：\n%s", buf.String())
	}
	if buf.Len() == 0 {
		t.Fatal("测试没有走到任何日志路径")
	}
}
