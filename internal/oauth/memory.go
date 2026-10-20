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
	ID                  string
	ClientID            string
	ClientName          string
	RedirectURI         string
	Resource            string
	Scope               string
	State               string
	CodeChallenge       string
	CodeChallengeMethod string
	CreatedAt           time.Time
}

type memoryStore struct {
	mu      sync.Mutex
	codes   map[string]*authCodeEntry
	pending map[string]*pendingRequest
}

func newMemoryStore() *memoryStore {
	return &memoryStore{
		codes:   make(map[string]*authCodeEntry),
		pending: make(map[string]*pendingRequest),
	}
}

func (m *memoryStore) putPending(p *pendingRequest) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.pending[p.ID] = p
	m.sweepLocked()
}

func (m *memoryStore) getPending(id string) (*pendingRequest, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	p, ok := m.pending[id]
	if !ok {
		return nil, false
	}
	if serviceNow().Sub(p.CreatedAt) > pendingRequestTTL {
		delete(m.pending, id)
		return nil, false
	}
	return p, true
}

func (m *memoryStore) deletePending(id string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.pending, id)
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
			delete(m.pending, k)
		}
	}
}
