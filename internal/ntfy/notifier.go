package ntfy

import (
	"context"
	"log"
	"sync"
	"time"

	"github.com/teacat99/RelayMesh/v2/internal/model"
)

const (
	queueSize    = 64
	workerCount  = 2
	maxScheduled = 200 // 同时在等待延迟到期的推送上限，防止异常情况下无限堆积

	// 限流：一分钟内最多发这么多条，超出的合并到下一条里说明。
	// 正常使用远到不了这个数；它保护的是「某个智能体在死循环里疯狂发请求」时你的手机和 ntfy 主题。
	rateWindow       = time.Minute
	rateMaxPerWindow = 20

	// 同一个会话在这段时间内只推送一次：同一条请求可能经好几条路径被通知到。
	seenTTL = 10 * time.Minute

	// 一次推送（含重试）的总时限。
	deliverTimeout = 40 * time.Second
)

// Options 是 Notifier 的依赖。
type Options struct {
	// Load 读取已保存的配置；必填。每条事件现读一次（主键查询，很便宜），所以界面上一保存就立即生效。
	Load func(ctx context.Context) (Config, error)
	// StillPending 供延迟推送在真正发出前复核：这条请求是否仍在等人回复。nil 表示总是需要推送。
	StillPending func(ctx context.Context, sessionID string) bool
	// HostName 把机器码解析成主机的显示名，只在「运行结束」真要推送时才调用（不推送的短命令不会去查库）。
	// nil 或返回空串都表示「不写主机」——推送里宁可少一行，也不拿机器码凑数。
	HostName func(ctx context.Context, machineCode string) string
	// EnvToken 是环境变量里的访问令牌，非空时盖过已保存的令牌。
	EnvToken string
	// Sender 为 nil 时用默认发送器。
	Sender *Sender
}

// hostLookupTimeout 是解析一次主机名最多等多久：它只是推送里的一行字，不值得拖住工作协程。
const hostLookupTimeout = 3 * time.Second

// Status 是自进程启动以来的推送情况（只在内存里，重启清零）。
type Status struct {
	LastAttemptAt  *time.Time `json:"last_attempt_at,omitempty"`
	LastOK         bool       `json:"last_ok"`
	LastTest       bool       `json:"last_test"` // 最近一次是不是「测试推送」
	LastHTTPStatus int        `json:"last_http_status,omitempty"`
	LastError      string     `json:"last_error,omitempty"`
	Sent           int        `json:"sent"`
	Failed         int        `json:"failed"`
	// Dropped 是被限流合并或因队列已满而没发出的条数。
	Dropped int `json:"dropped"`
}

// job 是队列里的一条事件：新反馈请求（info）、模型运行结束（run 非 nil）、「不再自动续跑」（autoResume 非 nil）、
// 新消息类事件（event 非 nil，见 event.go）、一批聚合到期的新消息（flush 非 nil）或异常（exception 非 nil，见 exception.go）。
type job struct {
	info       RequestInfo
	run        *RunInfo
	autoResume *AutoResumeInfo
	event      *EventInfo
	flush      *flushItem
	exception  *ExceptionInfo
	delayed    bool // 已经等过延迟，这次是到期后的复核与发送（只对请求有意义）
}

// Notifier 把「新反馈请求」「模型运行结束」「不再自动续跑」与新消息类事件异步推送到 ntfy。
// NotifyRequest / NotifyRun / NotifyAutoResume / NotifyEvent 永远不会阻塞调用方，推送失败或 ntfy 变慢也不会影响智能体的请求，更不会影响命令运行的收尾与自动续跑的下一轮判断。
type Notifier struct {
	opts   Options
	sender *Sender

	queue  chan job
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	mu         sync.Mutex
	started    bool
	seen       map[string]time.Time
	sentAt     []time.Time
	suppressed int
	scheduled  int
	status     Status

	// 新消息类的会话聚合（见 event.go）：等待到期的批次，以及每个会话上次推送的时间。
	batches   map[string]*eventBatch
	lastFlush map[string]time.Time

	// 异常（见 exception.go）：每个 Key 在去重窗口里被挡掉的重复次数，下一条同 Key 的推送会说明。
	excRepeats map[string]int

	// 以下三项只为测试存在：缩短延迟的单位、缩短聚合与冷却的单位、替换时钟。
	delayUnit time.Duration
	mergeUnit time.Duration
	now       func() time.Time
}

// New 创建并启动 Notifier。
func New(opts Options) *Notifier {
	ctx, cancel := context.WithCancel(context.Background())
	n := &Notifier{
		opts:      opts,
		sender:    opts.Sender,
		queue:     make(chan job, queueSize),
		ctx:       ctx,
		cancel:    cancel,
		seen:      make(map[string]time.Time),
		batches:   make(map[string]*eventBatch),
		lastFlush: make(map[string]time.Time),

		excRepeats: make(map[string]int),
		delayUnit:  time.Second,
		mergeUnit:  time.Second,
		now:        time.Now,
	}
	if n.sender == nil {
		n.sender = NewSender("RelayMesh")
	}
	return n
}

// ensureStarted 在第一次真正需要时才启动工作协程：没有用 ntfy 的部署（以及大量只创建服务、不推送的测试）里一条协程都不会多。
func (n *Notifier) ensureStarted() {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.started || n.ctx.Err() != nil {
		return
	}
	n.started = true
	n.wg.Add(workerCount)
	for i := 0; i < workerCount; i++ {
		go n.worker()
	}
}

// SetTiming 调整延迟推送与会话聚合的时间单位（默认都是 1 秒：「延迟 N 秒」= N×delayUnit，聚合 5 与冷却 20 同理）。
// 只供测试使用，必须在第一条事件之前调用；生产代码不要碰它。
func (n *Notifier) SetTiming(delayUnit, mergeUnit time.Duration) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if delayUnit > 0 {
		n.delayUnit = delayUnit
	}
	if mergeUnit > 0 {
		n.mergeUnit = mergeUnit
	}
}

// Stop 让后台协程退出（取消进行中的推送）；最多等 3 秒。可重复调用。
func (n *Notifier) Stop() {
	if n == nil {
		return
	}
	n.mu.Lock()
	n.cancel() // 取消之后 ensureStarted 不会再启动协程，下面的 Wait 因此不会与 Add 竞争
	n.stopBatches()
	n.mu.Unlock()
	done := make(chan struct{})
	go func() { n.wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
	}
}

// NotifyRequest 登记一条「智能体在等你回复」的事件。立即返回；会话内容当场复制，调用方之后随便改它。
func (n *Notifier) NotifyRequest(s *model.FeedbackSession) {
	if n == nil || s == nil || s.ID == "" {
		return
	}
	n.submit(job{info: InfoFromSession(s)})
}

// NotifyRun 登记一条「模型运行结束」的事件。立即返回；事件当场复制，调用方之后随便改它。
// 要不要推送由配置决定（见 RunInfo.WorthNotifying），这里只挡掉一定不会推送的那一大批。
func (n *Notifier) NotifyRun(in RunInfo) {
	if n == nil || in.RunID == "" {
		return
	}
	// 门槛最低 1 秒，而绝大多数命令比这短得多：不为它们排队，更不为它们读配置。
	if in.Duration < time.Second {
		return
	}
	if in.ExitCode != nil {
		code := *in.ExitCode
		in.ExitCode = &code
	}
	n.submit(job{run: &in})
}

// NotifyAutoResume 登记一条「不再自动续跑」事件（#151）。立即返回；事件当场复制。
// 「同一次放弃只推一次」由自动续跑的登记保证，这里的去重（按 Key）只是进程内的第二道保险。
func (n *Notifier) NotifyAutoResume(in AutoResumeInfo) {
	if n == nil || in.Key == "" {
		return
	}
	n.submit(job{autoResume: &in})
}

func (n *Notifier) submit(j job) {
	select {
	case <-n.ctx.Done():
		return
	default:
	}
	n.ensureStarted()
	select {
	case n.queue <- j:
	default:
		n.mu.Lock()
		n.status.Dropped++
		n.mu.Unlock()
		log.Printf("[ntfy] 推送队列已满，丢弃一条事件")
	}
}

// Status 返回当前的推送情况快照。
func (n *Notifier) Status() Status {
	n.mu.Lock()
	defer n.mu.Unlock()
	st := n.status
	if st.LastAttemptAt != nil {
		t := *st.LastAttemptAt
		st.LastAttemptAt = &t
	}
	return st
}

// EffectiveConfig 读取已保存的配置，并让环境变量里的令牌盖过去。
func (n *Notifier) EffectiveConfig(ctx context.Context) (Config, error) {
	cfg, err := n.opts.Load(ctx)
	if err != nil {
		return Config{}, err
	}
	return cfg.Normalize().WithEnvToken(n.opts.EnvToken), nil
}

// SendTest 用给定配置立刻发一条测试推送（不排队、不限流、不看总开关），并记入「最近一次推送」。
// 只试一次、不重试：人在界面上等着看结果，要的是马上知道通不通，而不是后台悄悄再试几轮。
func (n *Notifier) SendTest(ctx context.Context, cfg Config) (Result, error) {
	res, _, err := n.SendKindTest(ctx, cfg, "")
	return res, err
}

// SendKindTest 是「试发这一类」：kind 为空（或不认识）就是普通测试推送，否则按这一类此刻的优先级发一条，
// 正文写明是哪一类、几级。返回实际用的优先级，界面据此告诉用户。其余与 SendTest 相同（不排队、不限流、不看总开关）。
func (n *Notifier) SendKindTest(ctx context.Context, cfg Config, kind string) (Result, int, error) {
	cfg = cfg.Normalize().WithEnvToken(n.opts.EnvToken)
	ctx, cancel := context.WithTimeout(ctx, requestTimeout+2*time.Second)
	defer cancel()
	once := *n.sender
	once.Backoff = nil
	msg, priority := BuildKindTestMessage(cfg, kind, n.now())
	res, err := once.Send(ctx, cfg, msg)
	n.record(res, err, true)
	return res, priority, err
}

func (n *Notifier) worker() {
	defer n.wg.Done()
	for {
		select {
		case <-n.ctx.Done():
			return
		case j := <-n.queue:
			n.safeHandle(j)
		}
	}
}

// safeHandle 保证任何一条事件上的意外（哪怕是 bug）都不会带走进程或这个工作协程。
func (n *Notifier) safeHandle(j job) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[ntfy] 处理事件时发生内部错误：%v", r)
		}
	}()
	n.handle(j)
}

func (n *Notifier) handle(j job) {
	cfg, err := n.EffectiveConfig(n.ctx)
	if err != nil {
		log.Printf("[ntfy] 读取推送配置失败：%v", err)
		return
	}
	if !cfg.Enabled || cfg.Topic == "" || cfg.Validate(true) != nil {
		return
	}
	if j.run != nil {
		n.handleRun(cfg, *j.run)
		return
	}
	if j.autoResume != nil {
		n.handleAutoResume(cfg, *j.autoResume)
		return
	}
	if j.event != nil {
		n.handleEvent(cfg, *j.event)
		return
	}
	if j.flush != nil {
		n.handleFlush(cfg, *j.flush)
		return
	}
	if j.exception != nil {
		n.handleException(cfg, *j.exception)
		return
	}

	// 「智能体在等你回复」在表里设成了不推送：不去重、不排延迟（延迟到期后重新进来时也会在这里再挡一次）。
	if !cfg.kindEnabled(KindRequest) {
		return
	}
	if !j.delayed {
		if !n.firstSight(j.info.SessionID) {
			return
		}
		if cfg.DelaySeconds > 0 {
			n.schedule(j, time.Duration(cfg.DelaySeconds)*n.delayUnit)
			return
		}
	} else if n.opts.StillPending != nil && !n.opts.StillPending(n.ctx, j.info.SessionID) {
		// 等待期间你已经在网页上处理掉了（或请求已超时 / 被取消）：不再打扰。
		return
	}

	merged, ok := n.allow()
	if !ok {
		return
	}
	n.deliver(cfg, BuildRequestMessage(cfg, j.info, merged), merged)
}

// handleRun 处理一条「模型运行结束」：先按配置判断值不值得推，再去重、限流，最后才解析主机名并发送。
// 运行结束没有延迟——延迟是给请求用的（你在网页上处理掉就不必再推），运行结束没有「处理掉」这回事。
func (n *Notifier) handleRun(cfg Config, in RunInfo) {
	if !in.WorthNotifying(cfg) {
		return
	}
	// 「运行结束推送」是总开关；表里再按结局分开：顺利完成与出状况各有各的优先级，也可以单独不推。
	if !cfg.kindEnabled(runKind(in.State)) {
		return
	}
	if !n.firstSight("run:" + in.RunID) {
		return
	}
	merged, ok := n.allow()
	if !ok {
		return
	}
	n.deliver(cfg, BuildRunMessage(cfg, in, n.hostName(in.Machine), merged), merged)
}

// handleAutoResume 处理一条「不再自动续跑」事件：能走到这里的都是自动续跑已经判定「该打扰你」的事件（预设里打开了自动续跑），
// 所以只受消息类型开关、总开关与主题约束（见 handle）；去重、限流之后解析主机名并发送。
func (n *Notifier) handleAutoResume(cfg Config, in AutoResumeInfo) {
	if !cfg.kindEnabled(KindAutoResume) {
		return
	}
	if !n.firstSight("guard:" + in.Key) {
		return
	}
	merged, ok := n.allow()
	if !ok {
		return
	}
	n.deliver(cfg, BuildAutoResumeMessage(cfg, in, n.hostName(in.Machine), merged), merged)
}

// deliver 发送一条已经生成好的消息并记录结果。merged 是这条消息替此前被合并的通知做了说明的条数：
// 没发出去就还给计数，留给下一条。
func (n *Notifier) deliver(cfg Config, msg Message, merged int) {
	ctx, cancel := context.WithTimeout(n.ctx, deliverTimeout)
	defer cancel()
	res, err := n.sender.Send(ctx, cfg, msg)
	n.record(res, err, false)
	if err != nil {
		if merged > 0 {
			n.mu.Lock()
			n.suppressed += merged
			n.mu.Unlock()
		}
		log.Printf("[ntfy] 推送失败：%v", err)
	}
}

// hostName 解析主机的显示名。解析出任何岔子都当作「没有」：推送照发，只是少一行主机。
func (n *Notifier) hostName(machine string) (name string) {
	if n.opts.HostName == nil || machine == "" {
		return ""
	}
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[ntfy] 解析主机名时发生内部错误：%v", r)
			name = ""
		}
	}()
	ctx, cancel := context.WithTimeout(n.ctx, hostLookupTimeout)
	defer cancel()
	return n.opts.HostName(ctx, machine)
}

// firstSight 在同一个会话第一次出现时返回 true，同时清理过期记录。
func (n *Notifier) firstSight(id string) bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	now := n.now()
	for k, t := range n.seen {
		if now.Sub(t) > seenTTL {
			delete(n.seen, k)
		}
	}
	if _, dup := n.seen[id]; dup {
		return false
	}
	n.seen[id] = now
	return true
}

// schedule 在 d 之后把事件以「已延迟」的身份重新送回队列。
func (n *Notifier) schedule(j job, d time.Duration) {
	n.mu.Lock()
	if n.scheduled >= maxScheduled {
		n.status.Dropped++
		n.mu.Unlock()
		log.Printf("[ntfy] 等待延迟到期的推送过多，丢弃一条事件")
		return
	}
	n.scheduled++
	n.mu.Unlock()

	j.delayed = true
	time.AfterFunc(d, func() {
		n.mu.Lock()
		n.scheduled--
		n.mu.Unlock()
		n.submit(j)
	})
}

// allow 是限流：窗口内还有额度就占用一格，并返回此前被合并的条数（交给这条消息去说明）；没额度就记一笔合并。
func (n *Notifier) allow() (merged int, ok bool) {
	n.mu.Lock()
	defer n.mu.Unlock()
	now := n.now()
	kept := n.sentAt[:0]
	for _, t := range n.sentAt {
		if now.Sub(t) < rateWindow {
			kept = append(kept, t)
		}
	}
	n.sentAt = kept
	if len(n.sentAt) >= rateMaxPerWindow {
		n.suppressed++
		n.status.Dropped++
		return 0, false
	}
	n.sentAt = append(n.sentAt, now)
	merged, n.suppressed = n.suppressed, 0
	return merged, true
}

func (n *Notifier) record(res Result, err error, test bool) {
	n.mu.Lock()
	defer n.mu.Unlock()
	t := n.now()
	n.status.LastAttemptAt = &t
	n.status.LastTest = test
	n.status.LastHTTPStatus = res.Status
	if err == nil {
		n.status.LastOK = true
		n.status.LastError = ""
		if !test {
			n.status.Sent++
		}
		return
	}
	n.status.LastOK = false
	n.status.LastError = err.Error()
	if !test {
		n.status.Failed++
	}
}
