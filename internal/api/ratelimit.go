package api

import (
	"sync"
	"time"
)

type IPAttempt struct {
	FailedCount  int
	LastFailedAt time.Time
	LockedUntil  time.Time
}

type BlockedIPInfo struct {
	IP             string    `json:"ip"`
	FailedCount    int       `json:"failed_count"`
	LastFailedAt   time.Time `json:"last_failed_at"`
	LockedUntil    time.Time `json:"locked_until"`
	RemainingSecs  int64     `json:"remaining_seconds"`
}

type RateLimiter struct {
	mu      sync.RWMutex
	records map[string]*IPAttempt
	stopCh  chan struct{}
}

func NewRateLimiter() *RateLimiter {
	rl := &RateLimiter{
		records: make(map[string]*IPAttempt),
		stopCh:  make(chan struct{}),
	}
	// 启动定期自动垃圾回收协程（每 5 分钟清理过期记录）
	go rl.startCleanupRoutine()
	return rl
}

func (rl *RateLimiter) Close() {
	close(rl.stopCh)
}

func (rl *RateLimiter) startCleanupRoutine() {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			rl.cleanup()
		case <-rl.stopCh:
			return
		}
	}
}

func (rl *RateLimiter) cleanup() {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	now := time.Now()
	for ip, record := range rl.records {
		// 如果未被锁定，且距离最后一次失败超过 30 分钟，清理记录
		// 如果已被锁定，且锁定期已过，清理记录
		if record.LockedUntil.IsZero() {
			if now.Sub(record.LastFailedAt) > 30*time.Minute {
				delete(rl.records, ip)
			}
		} else if now.After(record.LockedUntil) {
			delete(rl.records, ip)
		}
	}
}

// CheckLocked 检查该 IP 是否处于封禁锁定期
func (rl *RateLimiter) CheckLocked(ip string) (isLocked bool, remaining time.Duration) {
	rl.mu.RLock()
	defer rl.mu.RUnlock()

	record, exists := rl.records[ip]
	if !exists {
		return false, 0
	}

	now := time.Now()
	if !record.LockedUntil.IsZero() && now.Before(record.LockedUntil) {
		return true, record.LockedUntil.Sub(now)
	}

	return false, 0
}

// RecordFailure 记录一次登录失败，若达到阈值则自动封禁
func (rl *RateLimiter) RecordFailure(ip string, maxAttempts int, lockoutDuration time.Duration) (isLocked bool, remaining time.Duration) {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	now := time.Now()
	record, exists := rl.records[ip]
	if !exists {
		record = &IPAttempt{
			FailedCount:  0,
			LastFailedAt: now,
		}
		rl.records[ip] = record
	}

	// 如果距离上次失败已超过 15 分钟，重置计数器
	if !record.LastFailedAt.IsZero() && now.Sub(record.LastFailedAt) > 15*time.Minute && record.LockedUntil.IsZero() {
		record.FailedCount = 0
	}

	record.FailedCount++
	record.LastFailedAt = now

	if maxAttempts > 0 && record.FailedCount >= maxAttempts {
		record.LockedUntil = now.Add(lockoutDuration)
		return true, lockoutDuration
	}

	return false, 0
}

// RecordSuccess 登录成功，清除该 IP 的所有失败记录
func (rl *RateLimiter) RecordSuccess(ip string) {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	delete(rl.records, ip)
}

// GetBlockedIPs 获取当前所有处于封禁状态的 IP 列表
func (rl *RateLimiter) GetBlockedIPs() []BlockedIPInfo {
	rl.mu.RLock()
	defer rl.mu.RUnlock()

	now := time.Now()
	var list []BlockedIPInfo
	for ip, record := range rl.records {
		if !record.LockedUntil.IsZero() && now.Before(record.LockedUntil) {
			remaining := int64(record.LockedUntil.Sub(now).Seconds())
			list = append(list, BlockedIPInfo{
				IP:            ip,
				FailedCount:   record.FailedCount,
				LastFailedAt:  record.LastFailedAt,
				LockedUntil:   record.LockedUntil,
				RemainingSecs: remaining,
			})
		}
	}
	return list
}

// UnblockIP 手动解封特定 IP
func (rl *RateLimiter) UnblockIP(ip string) {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	delete(rl.records, ip)
}

// ClearAll 手动清空所有封禁记录
func (rl *RateLimiter) ClearAll() {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	rl.records = make(map[string]*IPAttempt)
}
