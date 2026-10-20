package terminal

import (
	"crypto/ed25519"
	"errors"
	"sort"
	"sync"
	"time"

	"github.com/teacat99/RelayMesh/v2/internal/termproto"
)

// ---------- 发现窗口 ----------

// discovery 是「发现窗口」：只有窗口开启期间，未知机器码的 Agent 才能进入待启用列表。
// 这是通用入口的核心防护：窗口之外，任何未知连接在读到第一帧后立即被拒绝。
type discovery struct {
	mu     sync.Mutex
	always bool
	until  time.Time
	// epoch 在窗口每次「从关闭到开启」时加一：绑定窗口的凭据（安装票据）据此判断自己属于哪一次窗口，
	// 窗口关闭后再开启不会让旧凭据复活。窗口开着时再次点击「开启」只是顺延时间，纪元不变。
	epoch uint64
}

const (
	// DefaultDiscoveryWindow 是管理员打开发现窗口时的默认时长。
	DefaultDiscoveryWindow = 10 * time.Minute
	// MaxDiscoveryWindow 是发现窗口的时长上限。
	MaxDiscoveryWindow = 60 * time.Minute
)

// open 打开窗口并返回到期时间；常开模式下无操作。
func (d *discovery) open(now time.Time, dur time.Duration) time.Time {
	if dur <= 0 {
		dur = DefaultDiscoveryWindow
	}
	if dur > MaxDiscoveryWindow {
		dur = MaxDiscoveryWindow
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.always {
		return time.Time{}
	}
	if !now.Before(d.until) {
		d.epoch++
	}
	d.until = now.Add(dur)
	return d.until
}

// extend 在窗口开着时把到期时间再往后顺延 add（总剩余时间不超过 MaxDiscoveryWindow），返回新的到期时间。
// 与 open 的区别：open 是把到期时间重置为「从现在起 dur」，剩余 9 分钟时点「开启 10 分钟」只多出 1 分钟；
// extend 是在现有剩余上累加，所以「续期」每次都实打实多出 add。纪元不变，已经发出去的安装票据继续有效。
// 窗口没开着（或常开）时返回 false，调用方应改走 open。
func (d *discovery) extend(now time.Time, add time.Duration) (time.Time, bool) {
	if add <= 0 {
		add = DefaultDiscoveryWindow
	}
	if add > MaxDiscoveryWindow {
		add = MaxDiscoveryWindow
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.always || !now.Before(d.until) {
		return time.Time{}, false
	}
	next := d.until.Add(add)
	if limit := now.Add(MaxDiscoveryWindow); next.After(limit) {
		next = limit
	}
	d.until = next
	return next, true
}

// close 立即关闭窗口，返回此前是否处于开启状态。
func (d *discovery) close(now time.Time) (wasOpen bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.always {
		return false
	}
	wasOpen = now.Before(d.until)
	d.until = time.Time{}
	return wasOpen
}

// active 判断当前是否接受未知主机。
func (d *discovery) active(now time.Time) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.always || now.Before(d.until)
}

// state 返回展示用状态。
func (d *discovery) state(now time.Time) (active, always bool, until time.Time) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.always {
		return true, true, time.Time{}
	}
	if now.Before(d.until) {
		return true, false, d.until
	}
	return false, false, time.Time{}
}

// snapshot 返回窗口是否开启、是否常开、到期时间与当前纪元（一次加锁取得一致的快照）。
func (d *discovery) snapshot(now time.Time) (active, always bool, until time.Time, epoch uint64) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.always {
		return true, true, time.Time{}, 0
	}
	if now.Before(d.until) {
		return true, false, d.until, d.epoch
	}
	return false, false, time.Time{}, d.epoch
}

// takeExpired 窗口刚好到期时返回 true，且只返回一次（用于触发「到期收尾」）。
func (d *discovery) takeExpired(now time.Time) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.always || d.until.IsZero() || now.Before(d.until) {
		return false
	}
	d.until = time.Time{}
	return true
}

// ---------- 待启用列表（仅内存） ----------

var (
	errPendingFull  = errors.New("pending list is full")
	errPendingPerIP = errors.New("too many pending hosts from this address")
)

// MaxPendingPerIP 限制同一来源地址占用的待启用条目数，防止单一来源把列表灌满。
const MaxPendingPerIP = 8

// pendingEntry 是一个等待管理员处理的未知主机。所有自报字段都已净化。
type pendingEntry struct {
	Code      string
	PubKey    ed25519.PublicKey
	Meta      termproto.Meta
	IP        string
	FirstSeen time.Time
	LastSeen  time.Time
	OfflineAt time.Time // 零值表示当前连接仍在线

	agent *agentConn
}

// pendingList 保存待启用主机。条目只存在于内存：匿名来源不能往数据库里写任何东西。
type pendingList struct {
	mu    sync.Mutex
	items map[string]*pendingEntry
	max   int
	now   func() time.Time
}

func newPendingList(max int, now func() time.Time) *pendingList {
	return &pendingList{items: make(map[string]*pendingEntry), max: max, now: now}
}

// add 登记（或刷新）一个待启用主机，返回被它顶替的旧连接（同一机器码重连）。
// 列表满时，优先淘汰最久的离线条目；没有可淘汰的条目时拒绝新来者。
func (p *pendingList) add(e *pendingEntry) (created bool, superseded *agentConn, err error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	now := p.now()
	if old, ok := p.items[e.Code]; ok {
		superseded = old.agent
		old.agent = e.agent
		old.Meta = e.Meta
		old.IP = e.IP
		old.LastSeen = now
		old.OfflineAt = time.Time{}
		return false, superseded, nil
	}

	perIP := 0
	for _, it := range p.items {
		if it.IP == e.IP {
			perIP++
		}
	}
	if perIP >= MaxPendingPerIP {
		return false, nil, errPendingPerIP
	}

	if len(p.items) >= p.max {
		var victim *pendingEntry
		for _, it := range p.items {
			if it.OfflineAt.IsZero() {
				continue
			}
			if victim == nil || it.OfflineAt.Before(victim.OfflineAt) {
				victim = it
			}
		}
		if victim == nil {
			return false, nil, errPendingFull
		}
		delete(p.items, victim.Code)
	}

	e.FirstSeen, e.LastSeen, e.OfflineAt = now, now, time.Time{}
	p.items[e.Code] = e
	return true, nil, nil
}

// get 返回条目的快照（不含内部连接引用的所有权）。
func (p *pendingList) get(code string) (*pendingEntry, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	e, ok := p.items[code]
	if !ok {
		return nil, false
	}
	cp := *e
	return &cp, true
}

// take 原子地取出并移除条目：并发的「启用」与「忽略」只有先到者能拿到。
func (p *pendingList) take(code string) (*pendingEntry, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	e, ok := p.items[code]
	if ok {
		delete(p.items, code)
	}
	return e, ok
}

// restore 把 take 出的条目放回（持久化失败时回滚）。
func (p *pendingList) restore(e *pendingEntry) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if _, exists := p.items[e.Code]; !exists {
		p.items[e.Code] = e
	}
}

// markOffline 在连接断开时调用；仅当条目当前绑定的仍是该连接才标记，避免误伤已重连的新连接。
func (p *pendingList) markOffline(code string, a *agentConn) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if e, ok := p.items[code]; ok && e.agent == a {
		e.agent = nil
		e.OfflineAt = p.now()
		e.LastSeen = e.OfflineAt
	}
}

// expireOffline 移除离线超过 ttl 的条目。
func (p *pendingList) expireOffline(ttl time.Duration) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	now := p.now()
	n := 0
	for code, e := range p.items {
		if !e.OfflineAt.IsZero() && now.Sub(e.OfflineAt) >= ttl {
			delete(p.items, code)
			n++
		}
	}
	return n
}

// drain 清空列表并返回全部条目（发现窗口关闭 / 总闸关闭时调用）。
func (p *pendingList) drain() []*pendingEntry {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]*pendingEntry, 0, len(p.items))
	for _, e := range p.items {
		out = append(out, e)
	}
	p.items = make(map[string]*pendingEntry)
	return out
}

// snapshot 按首次出现时间排序返回快照。
func (p *pendingList) snapshot() []pendingEntry {
	p.mu.Lock()
	out := make([]pendingEntry, 0, len(p.items))
	for _, e := range p.items {
		cp := *e
		out = append(out, cp)
	}
	p.mu.Unlock()
	sort.Slice(out, func(i, j int) bool { return out[i].FirstSeen.Before(out[j].FirstSeen) })
	return out
}

// ---------- 临时主机（仅内存） ----------

const (
	// TempHostMaxAge 是临时启用的最长有效期（自启用起）。
	TempHostMaxAge = 24 * time.Hour
	// TempHostIdle 是临时主机断线后仍保留启用状态的宽限期（应对网络抖动）。
	TempHostIdle = 10 * time.Minute
)

// tempHost 是被「临时启用」的主机：Agent 以临时身份运行（身份只存在于进程内存），
// 服务端同样只在内存里记住它的启用状态，进程结束或超时后自然消失，不在数据库留下任何痕迹。
type tempHost struct {
	Code      string
	PubKey    ed25519.PublicKey
	Meta      termproto.Meta
	Remark    string
	IP        string
	EnabledAt time.Time
	LastSeen  time.Time
	Online    bool
}

type tempHosts struct {
	mu    sync.Mutex
	items map[string]*tempHost
}

func newTempHosts() *tempHosts {
	return &tempHosts{items: make(map[string]*tempHost)}
}

func (t *tempHosts) add(h *tempHost) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.items[h.Code] = h
}

// lookup 返回仍有效的临时主机（公钥需一致）。过期的条目会被顺手清除。
func (t *tempHosts) lookup(code string, pub ed25519.PublicKey, now time.Time) (*tempHost, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	h, ok := t.items[code]
	if !ok {
		return nil, false
	}
	if t.expiredLocked(h, now) || !pub.Equal(h.PubKey) {
		if t.expiredLocked(h, now) {
			delete(t.items, code)
		}
		return nil, false
	}
	cp := *h
	return &cp, true
}

func (t *tempHosts) expiredLocked(h *tempHost, now time.Time) bool {
	if now.Sub(h.EnabledAt) >= TempHostMaxAge {
		return true
	}
	return !h.Online && now.Sub(h.LastSeen) >= TempHostIdle
}

// setOnline 更新在线状态与最近可见时间；返回条目是否存在。
func (t *tempHosts) setOnline(code string, online bool, meta *termproto.Meta, ip string, now time.Time) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	h, ok := t.items[code]
	if !ok {
		return false
	}
	h.Online = online
	h.LastSeen = now
	if meta != nil {
		h.Meta = *meta
	}
	if ip != "" {
		h.IP = ip
	}
	return true
}

func (t *tempHosts) setRemark(code, remark string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	h, ok := t.items[code]
	if ok {
		h.Remark = remark
	}
	return ok
}

func (t *tempHosts) remove(code string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	_, ok := t.items[code]
	delete(t.items, code)
	return ok
}

// gc 清除过期条目，返回被清除的机器码（调用方据此断开仍在线的连接）。
func (t *tempHosts) gc(now time.Time) []string {
	t.mu.Lock()
	defer t.mu.Unlock()
	var removed []string
	for code, h := range t.items {
		if t.expiredLocked(h, now) {
			delete(t.items, code)
			removed = append(removed, code)
		}
	}
	return removed
}

func (t *tempHosts) snapshot(now time.Time) []tempHost {
	t.mu.Lock()
	out := make([]tempHost, 0, len(t.items))
	for _, h := range t.items {
		if !t.expiredLocked(h, now) {
			out = append(out, *h)
		}
	}
	t.mu.Unlock()
	sort.Slice(out, func(i, j int) bool { return out[i].EnabledAt.Before(out[j].EnabledAt) })
	return out
}
