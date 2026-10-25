package oauth

import (
	"sync"
	"time"
)

const (
	authCodeTTL       = 60 * time.Second
	pendingRequestTTL = 10 * time.Minute
)

type authCodeEntry struct {
	Code                string
	ClientID            string
	RedirectURI         string
	Resource            string
	Scope               string
	CodeChallenge       string
	CodeChallengeMethod string
	GrantID             uint // 批准后填入
	ExpiresAt           time.Time
	Used                bool
}

type pendingRequest struct {
	ID         string
	ClientID   string
	ClientName string
	// Registered：这个客户端是本服务登记的 DCR 客户端（否则是 CIMD）；批准页的陌生来源提示按它区分。
	Registered          bool
	RedirectURI         string
	Resource            string
	Scope               string
	State               string
	CodeChallenge       string
	CodeChallengeMethod string
	CreatedAt           time.Time
	// IP 是发起这次授权请求的客户端地址：限流器按「IP+客户端」给每个待决请求记了一个名额，
	// 请求离开待决状态时要凭它把名额还回去（见 memoryStore.release）。
	IP string
}

type memoryStore struct {
	mu      sync.Mutex
	codes   map[string]*authCodeEntry
	pending map[string]*pendingRequest

	// release 在一个待决请求离开 pending 时被调用，归还它占的限流名额。
	// 待决请求只有一个出口集合：批准 / 拒绝（takePending）、过期（sweepLocked / getPending）、
	// 管理员删除客户端（dropClient）——全部经由 removePendingLocked，所以名额一定随请求归还、且只还一次。
	// 调用发生在 m.mu 持有期间，release 不得回调 memoryStore。可为 nil（单测里不需要限流时）。
	release func(ip, clientID string)
}

func newMemoryStore() *memoryStore {
	return &memoryStore{
		codes:   make(map[string]*authCodeEntry),
		pending: make(map[string]*pendingRequest),
	}
}

// removePendingLocked 把一个待决请求移出 pending 并归还它的限流名额；调用方持有 m.mu。
func (m *memoryStore) removePendingLocked(id string, p *pendingRequest) {
	delete(m.pending, id)
	if m.release != nil {
		m.release(p.IP, p.ClientID)
	}
}

func (m *memoryStore) putPending(p *pendingRequest) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.pending[p.ID] = p
	m.sweepLocked()
}

// sweep 清掉所有已过期的待决请求与授权码，并归还过期请求占的名额。
// 发起授权时要在「申请名额」之前调用：限流的判断先于放进待决，若回收只挂在放进待决那一步上，
// 名额一旦占满，后续请求都会在申请名额处被拒，永远走不到回收。
func (m *memoryStore) sweep() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sweepLocked()
}

// getPending 读取一个待决请求（授权页展示用，不改变状态）；已过期的顺手清掉并归还名额。
func (m *memoryStore) getPending(id string) (*pendingRequest, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	p, ok := m.pending[id]
	if !ok {
		return nil, false
	}
	if serviceNow().Sub(p.CreatedAt) > pendingRequestTTL {
		m.removePendingLocked(id, p)
		return nil, false
	}
	return p, true
}

// takePending 原子地取走一个待决请求（批准 / 拒绝时用）：同一个请求无论多少个决定并发到达，只有一个能取到，
// 取走的同时归还名额。过期的请求同样会被清掉并归还，但不会被取到。
func (m *memoryStore) takePending(id string) (*pendingRequest, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	p, ok := m.pending[id]
	if !ok {
		return nil, false
	}
	m.removePendingLocked(id, p)
	if serviceNow().Sub(p.CreatedAt) > pendingRequestTTL {
		return nil, false
	}
	return p, true
}

func (m *memoryStore) putCode(e *authCodeEntry) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.codes[e.Code] = e
	m.sweepLocked()
}

// redeemCode 兑换授权码。replay=true 表示该码已被使用过（重放攻击）。
func (m *memoryStore) redeemCode(code string) (entry *authCodeEntry, replay bool, ok bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, exists := m.codes[code]
	if !exists || serviceNow().After(e.ExpiresAt) {
		return nil, false, false
	}
	if e.Used {
		return e, true, false
	}
	e.Used = true
	return e, false, true
}

func (m *memoryStore) consumeCode(code string) (*authCodeEntry, bool) {
	e, _, ok := m.redeemCode(code)
	return e, ok
}

// dropClient 作废某个客户端所有进行中的授权请求与还没兑换的授权码（管理员删除它的登记时用）。
func (m *memoryStore) dropClient(clientID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for k, p := range m.pending {
		if p.ClientID == clientID {
			m.removePendingLocked(k, p)
		}
	}
	for k, e := range m.codes {
		if e.ClientID == clientID {
			delete(m.codes, k)
		}
	}
}

func (m *memoryStore) countPendingByClient(clientID string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for _, p := range m.pending {
		if p.ClientID == clientID {
			n++
		}
	}
	return n
}

func (m *memoryStore) totalPending() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.pending)
}

func (m *memoryStore) sweepLocked() {
	now := serviceNow()
	for k, e := range m.codes {
		if e.Used || now.After(e.ExpiresAt) {
			delete(m.codes, k)
		}
	}
	for k, p := range m.pending {
		if now.Sub(p.CreatedAt) > pendingRequestTTL {
			m.removePendingLocked(k, p)
		}
	}
}
