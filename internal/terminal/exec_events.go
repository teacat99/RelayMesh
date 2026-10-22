package terminal

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/teacat99/RelayMesh/v2/internal/model"
)

// 人类专用的模型运行事件流（INV-M1/M3）：
//   - 只接受人类登录的 JWT（走与终端 REST 相同的 guard），MCP 令牌拿不到——模型看不到别的模型在做什么；
//   - 与全站 SSE 完全独立：运行事件里有命令摘要与实时输出，不能广播给所有订阅者；
//   - 每个订阅者一个有界队列：状态事件放不下就断开该订阅（客户端重连后重新拉取快照），
//     实时输出片段放不下就直接丢弃（片段只是「给人看的预览」，完整输出始终可经 output 读取）。

const (
	execMaxSubscribers = 8
	execSubQueue       = 512
	// execOutQueue 是每个订阅者队列里实时输出片段最多占用的位置。片段单个可达 32 KiB，
	// 不加这条限制时，一个停住的查看者在输出洪水下最多会让 Hub 多占 16 MiB（8 个就是 128 MiB）；
	// 现在最多 4 MiB。其余位置留给状态事件，所以洪水不会把状态事件挤掉。
	execOutQueue     = 128
	execStreamMaxAge = time.Hour
	execStreamPing   = 15 * time.Second
)

// execEvent 是事件流上的一条事件。
type execEvent struct {
	Type string `json:"type"` // run | out | pause | host

	Run *model.TerminalRun `json:"run,omitempty"`

	RunID  string `json:"run_id,omitempty"`
	Stream string `json:"stream,omitempty"`
	Off    uint64 `json:"off,omitempty"`
	Data   []byte `json:"data,omitempty"` // 实时输出片段（JSON 里为 base64）

	Paused  *bool  `json:"paused,omitempty"`
	Machine string `json:"machine_code,omitempty"`
	Online  *bool  `json:"online,omitempty"`
}

type execSub struct {
	ch   chan execEvent
	once sync.Once
}

func (s *execSub) close() { s.once.Do(func() { close(s.ch) }) }

type execBus struct {
	mu   sync.Mutex
	subs map[*execSub]struct{}
}

func newExecBus() *execBus { return &execBus{subs: make(map[*execSub]struct{})} }

func (b *execBus) subscribe() (*execSub, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.subs) >= execMaxSubscribers {
		return nil, false
	}
	s := &execSub{ch: make(chan execEvent, execSubQueue)}
	b.subs[s] = struct{}{}
	return s, true
}

func (b *execBus) unsubscribe(s *execSub) {
	b.mu.Lock()
	delete(b.subs, s)
	b.mu.Unlock()
	s.close()
}

func (b *execBus) count() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.subs)
}

// publish 发布状态类事件：订阅者跟不上就断开它。
func (b *execBus) publish(ev execEvent) {
	b.mu.Lock()
	var slow []*execSub
	for s := range b.subs {
		select {
		case s.ch <- ev:
		default:
			slow = append(slow, s)
		}
	}
	for _, s := range slow {
		delete(b.subs, s)
	}
	b.mu.Unlock()
	for _, s := range slow {
		s.close()
	}
}

// publishOut 发布实时输出片段：尽力而为，放不下就丢。
func (b *execBus) publishOut(id, stream string, off uint64, data []byte) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.subs) == 0 {
		return
	}
	cp := append([]byte(nil), data...)
	for s := range b.subs {
		if len(s.ch) >= execOutQueue {
			continue
		}
		select {
		case s.ch <- execEvent{Type: "out", RunID: id, Stream: stream, Off: off, Data: cp}:
		default:
		}
	}
}

func (b *execBus) publishRun(rec model.TerminalRun) {
	cp := rec
	b.publish(execEvent{Type: "run", Run: &cp})
}

func (b *execBus) publishPause(paused bool) {
	b.publish(execEvent{Type: "pause", Paused: &paused})
}

func (b *execBus) publishHost(machine string, online bool) {
	b.publish(execEvent{Type: "host", Machine: machine, Online: &online})
}

// closeAll 断开全部订阅者（登录凭据变更、总闸关闭、Hub 关闭时调用）。
func (b *execBus) closeAll() {
	b.mu.Lock()
	subs := make([]*execSub, 0, len(b.subs))
	for s := range b.subs {
		subs = append(subs, s)
	}
	b.subs = make(map[*execSub]struct{})
	b.mu.Unlock()
	for _, s := range subs {
		s.close()
	}
}

// handleExecEvents 提供 GET /exec/events：SSE 格式（event: / data:），浏览器用 fetch 流式读取（EventSource 无法带 Authorization 头）。
func (h *Hub) handleExecEvents(c *gin.Context) {
	sub, ok := h.exec.bus.subscribe()
	if !ok {
		c.JSON(http.StatusTooManyRequests, gin.H{"error": "事件订阅数已达上限，请关闭其他页面后重试"})
		return
	}
	defer h.exec.bus.unsubscribe(sub)

	w := c.Writer
	rc := http.NewResponseController(w)
	_ = rc.SetWriteDeadline(time.Time{}) // 长连接：取消服务器的整体写超时
	hd := w.Header()
	hd.Set("Content-Type", "text/event-stream")
	hd.Set("Cache-Control", "no-cache, no-transform")
	hd.Set("X-Accel-Buffering", "no")
	hd.Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)

	// 实时输出片段等同于读取完整输出：需要解锁（与 GET /exec/runs/:id/output 一致）。
	// 事件流本身只要登录；解锁令牌随请求头带来，每个片段都重新核对（过期、被主动上锁后立即停止下发）。
	subject, unlockTok := subjectOf(c), c.GetHeader(unlockHeader)
	outAllowed := func() bool {
		if unlockTok == "" {
			return false
		}
		_, ok := h.unlocks.Peek(unlockTok, subject)
		return ok
	}
	send := func(ev execEvent) bool {
		if ev.Type == "out" && !outAllowed() {
			return true // 丢弃：不是失败，状态事件照常下发
		}
		data, err := json.Marshal(ev)
		if err != nil {
			return true
		}
		if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", ev.Type, data); err != nil {
			return false
		}
		return rc.Flush() == nil
	}

	paused := h.exec.isPaused()
	if !send(execEvent{Type: "pause", Paused: &paused}) {
		return
	}

	ping := time.NewTicker(execStreamPing)
	defer ping.Stop()
	maxAge := time.NewTimer(execStreamMaxAge)
	defer maxAge.Stop()
	for {
		select {
		case ev, open := <-sub.ch:
			if !open {
				return
			}
			if !send(ev) {
				return
			}
		case <-ping.C:
			if _, err := fmt.Fprint(w, ": ping\n\n"); err != nil || rc.Flush() != nil {
				return
			}
		case <-maxAge.C:
			return // 到期让客户端用新的令牌重连
		case <-c.Request.Context().Done():
			return
		case <-h.bgCtx.Done():
			return
		}
	}
}
