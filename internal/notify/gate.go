package notify

import (
	"context"
	"log"
	"sync"
	"time"
)

// SSEEventName 是通知闸向浏览器广播的 SSE 事件名；载荷见 Payload。
const SSEEventName = "notify"

// 幂等记录的容量与保留时间：同一个 Event.ID 在这段时间内至多通知一次。
const (
	seenTTL = 10 * time.Minute
	seenCap = 1024
)

// 闸自己给出的两种「没有通知」的原因（决策函数里的原因见 decide.go）。
const (
	ReasonInvalid   Reason = "invalid"   // 事件缺 ID 或种类未登记
	ReasonDuplicate Reason = "duplicate" // 同一个 ID 已经处理过
)

// Payload 是 SSE notify 事件的载荷：事件本身加上闸的裁决。
// Kind 是实际生效的种类（点名了你的 agent_text 已升格为 mention）。
type Payload struct {
	ID           string    `json:"id"`
	Kind         Kind      `json:"kind"`
	Conversation string    `json:"conversation"`
	WorkflowID   string    `json:"workflow_id,omitempty"`
	SessionID    string    `json:"session_id,omitempty"`
	Sender       string    `json:"sender,omitempty"`
	Title        string    `json:"title,omitempty"`
	Preview      string    `json:"preview,omitempty"`
	DeepLink     string    `json:"deep_link,omitempty"`
	Mention      bool      `json:"mention,omitempty"`
	CreatedAt    time.Time `json:"created_at"`
	Penetrate    bool      `json:"penetrate"` // 穿透类：浏览器侧也不做前台抑制
	Sound        bool      `json:"sound"`
	Badge        bool      `json:"badge"`
}

// PayloadOf 组装 SSE 载荷。
func PayloadOf(ev Event, d Decision) Payload {
	return Payload{
		ID: ev.ID, Kind: d.Kind, Conversation: ev.Conversation, WorkflowID: ev.WorkflowID, SessionID: ev.SessionID,
		Sender: ev.Sender, Title: ev.Title, Preview: ev.Preview, DeepLink: ev.DeepLink, Mention: ev.Mention,
		CreatedAt: ev.CreatedAt, Penetrate: d.Penetrate, Sound: d.Sound, Badge: d.Badge,
	}
}

// Options 是 Gate 的依赖。
type Options struct {
	// Load 读取当前偏好；必填。每条事件现读一次（主键查询，很便宜），偏好一保存就立即生效。
	// 读取失败时按出厂默认决策——宁可多通知，也不因为读不出偏好就把关键通知吞掉。
	Load func(ctx context.Context) (Prefs, error)
	// Broadcast 把 SSE 事件推给所有浏览器；nil 表示不发浏览器通道。
	Broadcast func(eventType string, data any)
	// Now 只为测试存在：替换时钟。
	Now func() time.Time
}

// Gate 是统一通知闸：Publish 一条事件，它做幂等、按偏好决策，再分发到浏览器（SSE）与 ntfy 两条互不影响的通道。
// 任何一条通道出岔子（包括 panic）都不会传到调用方——通知永远不能拖垮发出它的那次工具调用。
type Gate struct {
	opts Options

	mu    sync.Mutex
	seen  map[string]time.Time
	order []seenEntry // 按到达顺序，用于淘汰
}

type seenEntry struct {
	id string
	at time.Time
}

// NewGate 创建通知闸。
func NewGate(opts Options) *Gate {
	if opts.Now == nil {
		opts.Now = time.Now
	}
	return &Gate{opts: opts, seen: make(map[string]time.Time)}
}

// Publish 处理一条事件并返回裁决。ntfy 是 ntfy 通道的投递回调：只有裁决要求推 ntfy 时才会被调用，
// 调用方在里面把事件交给 ntfy 推送器（它必须立即返回）；传 nil 表示这个来源没有 ntfy 通道。
// 内容（预览等）不会写进日志，日志里只有种类与原因。
func (g *Gate) Publish(ev Event, ntfy func(Event, Decision)) Decision {
	if g == nil {
		return Decision{Reason: ReasonInvalid}
	}
	now := g.opts.Now()
	ev = ev.Normalize(now)
	if !ev.Valid() {
		log.Printf("[notify] 丢弃无效事件：kind=%q", ev.Kind)
		return Decision{Kind: ev.Kind, Reason: ReasonInvalid}
	}
	if !g.firstSight(ev.ID, now) {
		return Decision{Kind: EffectiveKind(ev), Reason: ReasonDuplicate}
	}

	prefs := g.prefs(now)
	d := Decide(ev, prefs, now)

	if d.Browser && g.opts.Broadcast != nil {
		g.safely("广播", func() { g.opts.Broadcast(SSEEventName, PayloadOf(ev, d)) })
	}
	if d.Ntfy && ntfy != nil {
		// ntfy 回调拿到的是已升格的种类（点名的 agent_text 此时是 mention），投递细节随种类走。
		ev.Kind = d.Kind
		g.safely("ntfy", func() { ntfy(ev, d) })
	}
	return d
}

// prefs 读取偏好并规整；读不出来就用出厂默认。
func (g *Gate) prefs(now time.Time) Prefs {
	if g.opts.Load == nil {
		return Prefs{}.Normalize(now)
	}
	p, err := g.opts.Load(context.Background())
	if err != nil {
		log.Printf("[notify] 读取通知偏好失败，按默认偏好决策：%v", err)
		return Prefs{}.Normalize(now)
	}
	return p.Normalize(now)
}

// firstSight 在这个 ID 第一次出现时返回 true；同时淘汰过期与超出容量的记录。
func (g *Gate) firstSight(id string, now time.Time) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	for len(g.order) > 0 && (now.Sub(g.order[0].at) > seenTTL || len(g.order) >= seenCap) {
		old := g.order[0]
		g.order = g.order[1:]
		if g.seen[old.id].Equal(old.at) {
			delete(g.seen, old.id)
		}
	}
	if _, dup := g.seen[id]; dup {
		return false
	}
	g.seen[id] = now
	g.order = append(g.order, seenEntry{id: id, at: now})
	return true
}

// safely 把回调里的 panic 挡在闸内。
func (g *Gate) safely(what string, f func()) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[notify] %s通道发生内部错误：%v", what, r)
		}
	}()
	f()
}
