// Package secure 提供与业务无关的短时凭据原语：一次性票据与解锁令牌。
//
// 设计要点：
//   - 明文凭据只在签发时返回一次，服务端内存里只保存其 SHA-256 摘要，进程内存被读取也无法还原出可用凭据；
//   - 票据单次有效（兑换即销毁）、带用途与绑定对象，防止被挪用到别的端点或别的会话；
//   - 容量有上限，过期项在签发 / 兑换时惰性回收，不依赖后台协程，也不会被灌爆内存；
//   - 时间源可注入，便于确定性测试。
package secure

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"sync"
	"time"
)

// ErrTooManyTickets 表示未过期的票据数量已达上限，调用方应返回 429 并稍后重试。
var ErrTooManyTickets = errors.New("too many outstanding tickets")

// credentialBytes 是明文凭据的随机字节数（256 位）。
const credentialBytes = 32

// NewCredential 生成一个 URL 安全的随机凭据明文（base64url，无填充）。
func NewCredential() (string, error) {
	buf := make([]byte, credentialBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

func digest(credential string) [sha256.Size]byte {
	return sha256.Sum256([]byte(credential))
}

// TicketInfo 是兑换成功后返回的票据元数据。
type TicketInfo struct {
	Purpose string    // 用途，兑换时必须一致，例如 "sse" / "term"
	Subject string    // 签发时的主体（登录账号）
	Bind    string    // 绑定对象，例如会话 ID；不需要时为空
	Expires time.Time // 过期时间
}

// TicketStore 是内存版一次性票据库。零值不可用，请使用 NewTicketStore。
type TicketStore struct {
	mu      sync.Mutex
	items   map[[sha256.Size]byte]TicketInfo
	max     int
	nowFunc func() time.Time
}

// NewTicketStore 创建票据库；max<=0 时取 1024。
func NewTicketStore(max int) *TicketStore {
	if max <= 0 {
		max = 1024
	}
	return &TicketStore{
		items:   make(map[[sha256.Size]byte]TicketInfo),
		max:     max,
		nowFunc: time.Now,
	}
}

// SetClock 仅供测试注入时间源。
func (s *TicketStore) SetClock(now func() time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nowFunc = now
}

// Issue 签发一张票据，返回明文（仅此一次可见）。
func (s *TicketStore) Issue(purpose, subject, bind string, ttl time.Duration) (string, error) {
	if ttl <= 0 {
		return "", errors.New("ticket ttl must be positive")
	}
	plain, err := NewCredential()
	if err != nil {
		return "", err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	now := s.nowFunc()
	s.purgeLocked(now)
	if len(s.items) >= s.max {
		return "", ErrTooManyTickets
	}
	s.items[digest(plain)] = TicketInfo{
		Purpose: purpose,
		Subject: subject,
		Bind:    bind,
		Expires: now.Add(ttl),
	}
	return plain, nil
}

// Redeem 兑换票据：存在、未过期且用途一致则销毁并返回元数据。
// 用途不一致时不销毁（持有者没有过错，且攻击者无法借此消耗他人的票据）。
func (s *TicketStore) Redeem(ticket, purpose string) (TicketInfo, bool) {
	if ticket == "" {
		return TicketInfo{}, false
	}
	key := digest(ticket)

	s.mu.Lock()
	defer s.mu.Unlock()

	now := s.nowFunc()
	info, ok := s.items[key]
	if !ok {
		return TicketInfo{}, false
	}
	if !now.Before(info.Expires) {
		delete(s.items, key)
		return TicketInfo{}, false
	}
	if info.Purpose != purpose {
		return TicketInfo{}, false
	}
	delete(s.items, key)
	return info, true
}

// Len 返回当前票据数（含尚未被回收的过期项），仅供测试与监控。
func (s *TicketStore) Len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.items)
}

// RevokeSubject 作废某个主体的全部票据（改密、登出等场景）。
func (s *TicketStore) RevokeSubject(subject string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for k, v := range s.items {
		if v.Subject == subject {
			delete(s.items, k)
		}
	}
}

// RevokeAll 作废全部票据。
func (s *TicketStore) RevokeAll() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.items = make(map[[sha256.Size]byte]TicketInfo)
}

func (s *TicketStore) purgeLocked(now time.Time) {
	for k, v := range s.items {
		if !now.Before(v.Expires) {
			delete(s.items, k)
		}
	}
}
