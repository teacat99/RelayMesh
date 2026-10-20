package secure

import (
	"crypto/sha256"
	"errors"
	"sync"
	"time"
)

// 解锁令牌的默认有效期：空闲 15 分钟、绝对 8 小时（每次成功使用顺延空闲期，但不会超过绝对期限）。
const (
	DefaultUnlockIdle     = 15 * time.Minute
	DefaultUnlockAbsolute = 8 * time.Hour
)

// ErrTooManyUnlocks 表示同时存在的解锁令牌数量已达上限。
var ErrTooManyUnlocks = errors.New("too many active unlock tokens")

type unlockEntry struct {
	subject  string
	created  time.Time
	lastUsed time.Time
}

// UnlockStore 保存「二次解锁」令牌：管理员在已登录的基础上再次输入密码后获得，
// 用于启用 / 停用主机、开会话等高风险操作。只存摘要，空闲与绝对两道期限。
type UnlockStore struct {
	mu       sync.Mutex
	items    map[[sha256.Size]byte]*unlockEntry
	idle     time.Duration
	absolute time.Duration
	max      int
	nowFunc  func() time.Time
}

// NewUnlockStore 创建解锁令牌库；参数 <=0 时使用默认值（空闲 15 分钟 / 绝对 8 小时 / 最多 256 个）。
func NewUnlockStore(idle, absolute time.Duration, max int) *UnlockStore {
	if idle <= 0 {
		idle = DefaultUnlockIdle
	}
	if absolute <= 0 {
		absolute = DefaultUnlockAbsolute
	}
	if max <= 0 {
		max = 256
	}
	return &UnlockStore{
		items:    make(map[[sha256.Size]byte]*unlockEntry),
		idle:     idle,
		absolute: absolute,
		max:      max,
		nowFunc:  time.Now,
	}
}

// SetClock 仅供测试注入时间源。
func (s *UnlockStore) SetClock(now func() time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nowFunc = now
}

// IdleTTL 返回空闲有效期，供接口告知前端。
func (s *UnlockStore) IdleTTL() time.Duration { return s.idle }

// AbsoluteTTL 返回绝对有效期。
func (s *UnlockStore) AbsoluteTTL() time.Duration { return s.absolute }

// Issue 为主体签发解锁令牌，返回明文与绝对过期时间。
func (s *UnlockStore) Issue(subject string) (string, time.Time, error) {
	plain, err := NewCredential()
	if err != nil {
		return "", time.Time{}, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	now := s.nowFunc()
	s.purgeLocked(now)
	if len(s.items) >= s.max {
		return "", time.Time{}, ErrTooManyUnlocks
	}
	s.items[digest(plain)] = &unlockEntry{subject: subject, created: now, lastUsed: now}
	return plain, now.Add(s.absolute), nil
}

// Validate 校验令牌归属于 subject 且未过期；通过则顺延空闲期。
func (s *UnlockStore) Validate(token, subject string) bool {
	if token == "" {
		return false
	}
	key := digest(token)

	s.mu.Lock()
	defer s.mu.Unlock()

	entry, ok := s.items[key]
	if !ok {
		return false
	}
	now := s.nowFunc()
	if s.expired(entry, now) {
		delete(s.items, key)
		return false
	}
	if entry.subject != subject {
		return false
	}
	entry.lastUsed = now
	return true
}

// ValidateFresh 在 Validate 的基础上要求令牌是「刚刚」换取的（创建于 maxAge 之内），也就是管理员不久之前才输入过密码。
// 用于授予式的高风险操作（例如开启「允许整个文件系统」）：已经解锁了几个小时的令牌不能替这类操作代为确认。
// 不满足时不顺延空闲期——被拒绝的请求不该让令牌更长寿。maxAge<=0 一律不通过。
func (s *UnlockStore) ValidateFresh(token, subject string, maxAge time.Duration) bool {
	if token == "" || maxAge <= 0 {
		return false
	}
	key := digest(token)

	s.mu.Lock()
	defer s.mu.Unlock()

	entry, ok := s.items[key]
	if !ok {
		return false
	}
	now := s.nowFunc()
	if s.expired(entry, now) {
		delete(s.items, key)
		return false
	}
	if entry.subject != subject || now.Sub(entry.created) > maxAge {
		return false
	}
	entry.lastUsed = now
	return true
}

// Peek 与 Validate 判定规则相同，但不顺延空闲期：用于状态轮询等「只读查看」场景，
// 避免界面每隔几秒刷新一次状态就让解锁令牌永远不过期。返回距离空闲失效 / 绝对失效中较早者的剩余时间。
func (s *UnlockStore) Peek(token, subject string) (remaining time.Duration, ok bool) {
	if token == "" {
		return 0, false
	}
	key := digest(token)

	s.mu.Lock()
	defer s.mu.Unlock()

	entry, found := s.items[key]
	if !found {
		return 0, false
	}
	now := s.nowFunc()
	if s.expired(entry, now) {
		delete(s.items, key)
		return 0, false
	}
	if entry.subject != subject {
		return 0, false
	}
	idleLeft := entry.lastUsed.Add(s.idle).Sub(now)
	absLeft := entry.created.Add(s.absolute).Sub(now)
	if absLeft < idleLeft {
		idleLeft = absLeft
	}
	return idleLeft, true
}

// Revoke 作废单个令牌（主动「上锁」）。
func (s *UnlockStore) Revoke(token string) {
	if token == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.items, digest(token))
}

// RevokeAll 作废全部令牌：总闸关闭、账号或密码被修改时必须调用。
func (s *UnlockStore) RevokeAll() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.items = make(map[[sha256.Size]byte]*unlockEntry)
}

// Len 返回当前令牌数（含尚未回收的过期项），仅供测试与监控。
func (s *UnlockStore) Len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.items)
}

func (s *UnlockStore) expired(e *unlockEntry, now time.Time) bool {
	return !now.Before(e.created.Add(s.absolute)) || !now.Before(e.lastUsed.Add(s.idle))
}

func (s *UnlockStore) purgeLocked(now time.Time) {
	for k, v := range s.items {
		if s.expired(v, now) {
			delete(s.items, k)
		}
	}
}
