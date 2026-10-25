package ntfy

import (
	"fmt"
	"log"
	"net/url"
	"strings"
	"time"
)

// 「新消息类」通知（agent_text / ask_card / mention / approval_waiting）的推送，由统一通知闸（internal/notify）放行后交来。
//
// 与老三类（新反馈请求 / 运行结束 / 守护告警）不同，它们按设计稿 07 §7 的格式推送：
//   - 标题「会话名 · 发送者」，正文是预览（≤ 120 字）或「有新消息」；优先级取「各类消息的优先级」表里这一类的值
//     （内置默认与升级前一致：穿透类 4、其余 3，见 priority.go），表里设成不推送的类型直接不推；点击打开深链；
//   - 同一个会话里非穿透类的多条消息先聚合一小会儿再推一条（单条原文、多条「N 条新消息」），
//     同一会话相邻两次推送至少隔一段冷却；穿透类立即推送、不聚合；
//   - 全局限流（每分钟 20 条）与老三类共用同一份额度。
//
// 老三类的构造器与行为一个字没动。

const (
	// 非穿透类先聚合这么多秒再推一条；同一会话相邻两次推送至少隔这么多秒。
	eventCoalesceSec = 5
	eventCooldownSec = 20

	// 同时在等待聚合的会话数上限，防止异常情况下无限堆积。
	maxBatches = 200
	// lastFlush 记录的保留时间：超过冷却很久的记录没有意义，顺手清掉。
	lastFlushTTL = 10 * time.Minute

	eventPreviewMax = 120
	eventTitleMax   = 80

	// 优先级：穿透类 = 4（high），其余 = 3（default）。
	eventPriorityHigh   = 4
	eventPriorityNormal = 3
)

// EventInfo 是新消息类推送要用到的字段。纯数据：由接线处（api）把通知闸的事件翻译成它，
// ntfy 因此仍然只依赖标准库与 model。
type EventInfo struct {
	// ID 是事件的唯一标识；同一个 ID 在去重窗口内只推一次。
	ID string
	// Kind 是种类：agent_text / ask_card / mention / approval_waiting；决定没有预览时的那句话与图标。
	Kind string
	// Conversation 是聚合键：同一个会话的非穿透类消息合并成一条。留空表示不参与聚合。
	Conversation string
	// Title 是会话名；Sender 是发送者（与会话名相同时只写一个）。
	Title  string
	Sender string
	// Preview 是预览正文（已折成一行并截断）；ShowPreview 为假时报文里不会出现它。
	Preview     string
	ShowPreview bool
	// Penetrate 为真表示穿透类：优先级 4、立即推送、不聚合。
	Penetrate bool
	// DeepLink 是站内路径（以 / 开头），拼到「点击打开的地址」后面。
	DeepLink string
}

// eventBatch 是一个会话里正在等待聚合的非穿透类消息。
type eventBatch struct {
	count int
	last  EventInfo
	timer *time.Timer
}

// flushItem 是聚合到期后交给工作协程发送的一批。
type flushItem struct {
	info  EventInfo
	count int
}

// NotifyEvent 登记一条新消息类事件。立即返回；事件当场复制，调用方之后随便改它。
func (n *Notifier) NotifyEvent(in EventInfo) {
	if n == nil || in.ID == "" {
		return
	}
	n.submit(job{event: &in})
}

// handleEvent 处理一条新消息类事件：去重之后，穿透类立即限流并发送，其余进入会话聚合。
func (n *Notifier) handleEvent(cfg Config, in EventInfo) {
	// 这一类在表里设成了不推送：连去重记录和限流额度都不占（以后再打开时，之前被挡掉的事件不会被当成「已经推过」）。
	if kind, ok := eventPushKind(in.Kind); ok && !cfg.kindEnabled(kind) {
		return
	}
	if !n.firstSight("evt:" + in.ID) {
		return
	}
	if in.Penetrate {
		merged, ok := n.allow()
		if !ok {
			return
		}
		n.deliver(cfg, BuildEventMessage(cfg, in, 1, merged), merged)
		return
	}
	n.addToBatch(in)
}

// addToBatch 把一条非穿透类事件并入它所在会话的聚合批次；会话里没有批次时新开一个并安排到期发送。
// 批次到期的时间 = max(聚合时长, 距上次推送满冷却的时间)：突发的一串消息只推一条，持续不断的消息也不会让同一会话刷屏。
func (n *Notifier) addToBatch(in EventInfo) {
	key := in.Conversation
	if key == "" {
		key = "id:" + in.ID
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.ctx.Err() != nil {
		return
	}
	if b := n.batches[key]; b != nil {
		b.count++
		b.last = in
		return
	}
	if len(n.batches) >= maxBatches {
		n.status.Dropped++
		log.Printf("[ntfy] 等待聚合的会话过多，丢弃一条事件")
		return
	}

	now := n.now()
	for k, t := range n.lastFlush {
		if now.Sub(t) > lastFlushTTL {
			delete(n.lastFlush, k)
		}
	}
	wait := time.Duration(eventCoalesceSec) * n.mergeUnit
	if last, ok := n.lastFlush[key]; ok {
		if rest := time.Duration(eventCooldownSec)*n.mergeUnit - now.Sub(last); rest > wait {
			wait = rest
		}
	}
	b := &eventBatch{count: 1, last: in}
	b.timer = time.AfterFunc(wait, func() { n.flushBatch(key) })
	n.batches[key] = b
}

// flushBatch 在批次到期时被定时器调用：取出批次、记下推送时间，再交回队列由工作协程发送。
func (n *Notifier) flushBatch(key string) {
	n.mu.Lock()
	b := n.batches[key]
	delete(n.batches, key)
	if b != nil {
		n.lastFlush[key] = n.now()
	}
	n.mu.Unlock()
	if b == nil {
		return
	}
	n.submit(job{flush: &flushItem{info: b.last, count: b.count}})
}

// stopBatches 取消所有等待中的聚合定时器（Stop 时调用；此时 ctx 已取消，不会再有新批次）。
func (n *Notifier) stopBatches() {
	for key, b := range n.batches {
		b.timer.Stop()
		delete(n.batches, key)
	}
}

// handleFlush 发送一批聚合后的消息。
func (n *Notifier) handleFlush(cfg Config, f flushItem) {
	// 聚合的几秒里设置可能改过：到期发送前再看一次这一类还推不推。
	if kind, ok := eventPushKind(f.info.Kind); ok && !cfg.kindEnabled(kind) {
		return
	}
	merged, ok := n.allow()
	if !ok {
		return
	}
	n.deliver(cfg, BuildEventMessage(cfg, f.info, f.count, merged), merged)
}

// BuildEventMessage 生成新消息类的推送内容。count 是这一条代表的消息数（聚合后大于 1）；
// merged 是此前因全局限流而没发出去的通知条数，大于 0 时在末尾说明一下。
func BuildEventMessage(cfg Config, in EventInfo, count, merged int) Message {
	lines := []string{eventBody(in, count)}
	if merged > 0 {
		lines = append(lines, mergedNote(merged))
	}
	// 优先级取「各类消息的优先级」表里这一类的值；闸里没有对应行的种类退回老规则（穿透类 4，其余 3）。
	priority := eventPriorityNormal
	if in.Penetrate {
		priority = eventPriorityHigh
	}
	if kind, ok := eventPushKind(in.Kind); ok {
		priority = cfg.msgPriority(kind)
	}
	return Message{
		Title:    eventTitle(in),
		Body:     strings.Join(lines, "\n"),
		Priority: priority,
		Tags:     []string{eventTag(in.Kind)},
		Click:    eventClick(cfg.ClickURL, in.DeepLink),
	}
}

// eventTitle 是「会话名 · 发送者」；发送者缺失或与会话名相同时只写会话名，两者都没有时写 RelayMesh。
func eventTitle(in EventInfo) string {
	name := clipTotal(oneLine(in.Title), eventTitleMax)
	if name == "" {
		name = "RelayMesh"
	}
	sender := clipTotal(oneLine(in.Sender), 40)
	if sender != "" && !strings.EqualFold(sender, name) {
		name += " · " + sender
	}
	return name
}

// eventBody 是正文：能带预览就写预览，否则只写「有新消息」这一类的一句话；聚合后写「N 条新消息」。
func eventBody(in EventInfo, count int) string {
	preview := ""
	if in.ShowPreview {
		preview = clipTotal(oneLine(in.Preview), eventPreviewMax)
	}
	if count > 1 {
		s := fmt.Sprintf("%d 条新消息", count)
		if preview != "" {
			s += "\n最新：" + preview
		}
		return s
	}
	if preview != "" {
		return preview
	}
	switch in.Kind {
	case "ask_card":
		return "有新的待确认提问"
	case "mention":
		return "有人 @ 了你"
	case "approval_waiting":
		return "终端在等你批准"
	case "board_stale":
		return "有指派很久没人接"
	}
	return "有新消息"
}

func eventTag(kind string) string {
	switch kind {
	case "ask_card":
		return "question"
	case "mention":
		return "bell"
	case "approval_waiting":
		return "warning"
	case "board_stale":
		return "hourglass_flowing_sand"
	}
	return "speech_balloon"
}

// eventClick 把深链拼到「点击打开的地址」后面；没有配置地址时点击没有动作。
func eventClick(base, link string) string {
	base = strings.TrimSpace(base)
	if base == "" {
		return ""
	}
	if link == "" || link[0] != '/' {
		return base
	}
	u, err := url.Parse(base)
	if err != nil {
		return base
	}
	path, query := link, ""
	if i := strings.IndexByte(link, '?'); i >= 0 {
		path, query = link[:i], link[i+1:]
	}
	u.Path = strings.TrimRight(u.Path, "/") + path
	u.RawPath = ""
	switch {
	case query == "":
	case u.RawQuery == "":
		u.RawQuery = query
	default:
		u.RawQuery += "&" + query
	}
	return u.String()
}

// clipTotal 按字符数截断，省略号计入上限（总长不超过 max）。设计稿要求预览 ≤ 120 字，是含省略号的总长。
func clipTotal(s string, max int) string {
	r := []rune(s)
	if max <= 0 || len(r) <= max {
		return s
	}
	return strings.TrimSpace(string(r[:max-1])) + "…"
}
