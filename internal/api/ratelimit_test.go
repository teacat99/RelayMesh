package api

import (
	"testing"
	"time"
)

func TestRateLimiter_LockoutAndUnblock(t *testing.T) {
	rl := NewRateLimiter()
	defer rl.Close()

	ip := "192.168.1.100"

	// 初始状态未被锁定
	if isLocked, _ := rl.CheckLocked(ip); isLocked {
		t.Fatalf("expected ip %s not to be locked initially", ip)
	}

	// 模拟 4 次失败（阈值为 5）
	for i := 0; i < 4; i++ {
		isLocked, _ := rl.RecordFailure(ip, 5, 10*time.Minute)
		if isLocked {
			t.Fatalf("attempt %d should not lock", i+1)
		}
	}

	// 第 5 次失败触发锁定
	isLocked, remaining := rl.RecordFailure(ip, 5, 10*time.Minute)
	if !isLocked {
		t.Fatalf("5th attempt should lock the ip")
	}
	if remaining <= 0 {
		t.Fatalf("remaining duration should be > 0, got %v", remaining)
	}

	// 再次检查确认锁定
	if isLocked, _ := rl.CheckLocked(ip); !isLocked {
		t.Fatalf("expected ip to remain locked")
	}

	// 获取锁定列表
	blockedList := rl.GetBlockedIPs()
	if len(blockedList) != 1 || blockedList[0].IP != ip {
		t.Fatalf("expected blocked list to have ip %s, got %+v", ip, blockedList)
	}

	// 手动解封
	rl.UnblockIP(ip)
	if isLocked, _ := rl.CheckLocked(ip); isLocked {
		t.Fatalf("expected ip to be unblocked")
	}
	if len(rl.GetBlockedIPs()) != 0 {
		t.Fatalf("expected blocked list to be empty after unblock")
	}

	// 测试成功重置
	rl.RecordFailure(ip, 5, 10*time.Minute)
	rl.RecordSuccess(ip)
	if isLocked, _ := rl.CheckLocked(ip); isLocked {
		t.Fatalf("expected success to reset failures")
	}
}
