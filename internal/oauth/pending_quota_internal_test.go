package oauth

// #240 的包内单测：待决请求占的限流名额必须在它离开待决状态的每一条路径上归还，且只还一次。
// 黑盒的 HTTP 用例见 pending_quota_test.go；这里直接断言 memoryStore 与限流器的计数，
// 并覆盖黑盒观察不到的路径（管理员删除客户端、全局上限、重复归还）。

import (
	"fmt"
	"testing"
	"time"
)

// quotaFixture 是一个接好线的 memoryStore + 限流器，时间由测试控制。
type quotaFixture struct {
	t   *testing.T
	rl  *oauthRateLimiter
	m   *memoryStore
	now time.Time
	seq int
}

func newQuotaFixture(t *testing.T) *quotaFixture {
	t.Helper()
	f := &quotaFixture{t: t, rl: newOAuthRateLimiter(), m: newMemoryStore(), now: time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)}
	f.m.release = f.rl.releasePending
	SetServiceClock(func() time.Time { return f.now })
	t.Cleanup(func() { SetServiceClock(nil) })
	return f
}

// open 模拟 authorize 的两步：先申请名额，再放进待决；名额被拒时返回空编号。
func (f *quotaFixture) open(ip, clientID string) string {
	f.t.Helper()
	if !f.rl.reservePending(ip, clientID) {
		return ""
	}
	f.seq++
	id := fmt.Sprintf("req-%d", f.seq)
	f.m.putPending(&pendingRequest{ID: id, ClientID: clientID, IP: ip, CreatedAt: f.now})
	return id
}

// held 返回限流器当前记着的名额数（全局计数、按 key 的明细之和必须一致）。
func (f *quotaFixture) held() int {
	f.t.Helper()
	f.rl.mu.Lock()
	defer f.rl.mu.Unlock()
	sum := 0
	for _, n := range f.rl.pend {
		sum += n
	}
	if sum != f.rl.pendG {
		f.t.Fatalf("名额计数不一致：按 key 之和 %d，全局计数 %d", sum, f.rl.pendG)
	}
	return f.rl.pendG
}

func (f *quotaFixture) keys() int {
	f.rl.mu.Lock()
	defer f.rl.mu.Unlock()
	return len(f.rl.pend)
}

func TestReleasePendingOnlyReturnsQuotaThatIsHeld(t *testing.T) {
	rl := newOAuthRateLimiter()
	if !rl.reservePending("1.1.1.1", "c") {
		t.Fatal("首次申请应当成功")
	}
	rl.releasePending("2.2.2.2", "c") // 从没申请过的 key：不能动全局计数
	if rl.pendG != 1 {
		t.Fatalf("归还一个没占名额的 key 不应改变全局计数，实际 %d", rl.pendG)
	}
	rl.releasePending("1.1.1.1", "c")
	if rl.pendG != 0 || len(rl.pend) != 0 {
		t.Fatalf("归还后应当清零且不留空 key，实际 pendG=%d keys=%d", rl.pendG, len(rl.pend))
	}
	rl.releasePending("1.1.1.1", "c") // 重复归还：仍然是 0，不会变成负数
	if rl.pendG != 0 {
		t.Fatalf("重复归还不应产生负数，实际 %d", rl.pendG)
	}
}

func TestPendingQuotaReturnedWhenRequestsExpireBySweep(t *testing.T) {
	f := newQuotaFixture(t)
	for i := 0; i < 3; i++ {
		f.open("1.1.1.1", "c")
	}
	f.open("2.2.2.2", "c")
	if f.held() != 4 {
		t.Fatalf("放进 4 个后应占 4 个名额，实际 %d", f.held())
	}
	f.now = f.now.Add(pendingRequestTTL - time.Second)
	f.m.sweep()
	if f.held() != 4 {
		t.Fatalf("还没到有效期，不应回收，实际占 %d", f.held())
	}
	f.now = f.now.Add(2 * time.Second)
	f.m.sweep()
	if f.held() != 0 || f.keys() != 0 || len(f.m.pending) != 0 {
		t.Fatalf("过期后名额应全部归还，实际占 %d、key %d、待决 %d", f.held(), f.keys(), len(f.m.pending))
	}
}

func TestPendingQuotaReturnedWhenExpiredRequestIsTouched(t *testing.T) {
	f := newQuotaFixture(t)
	a := f.open("1.1.1.1", "c")
	b := f.open("1.1.1.1", "c")
	f.now = f.now.Add(pendingRequestTTL + time.Second)

	if _, ok := f.m.getPending(a); ok { // 授权页读取：过期的读不到，顺手回收
		t.Fatal("过期的待决请求不应被读到")
	}
	if f.held() != 1 {
		t.Fatalf("读取过期请求 a 之后应还回 1 个名额，实际还占 %d", f.held())
	}
	if _, ok := f.m.takePending(b); ok { // 决定：过期的取不到，同样回收
		t.Fatal("过期的待决请求不应被取走")
	}
	if f.held() != 0 || f.keys() != 0 {
		t.Fatalf("取过期请求 b 之后应全部归还，实际占 %d、key %d", f.held(), f.keys())
	}
}

func TestTakePendingIsExactlyOnce(t *testing.T) {
	f := newQuotaFixture(t)
	a := f.open("1.1.1.1", "c")
	f.open("1.1.1.1", "c") // 另一个请求仍然在待决中，它的名额不能被误还

	if p, ok := f.m.takePending(a); !ok || p.ID != a {
		t.Fatal("第一次应当取到")
	}
	if f.held() != 1 {
		t.Fatalf("取走一个后应还剩 1 个名额，实际 %d", f.held())
	}
	if _, ok := f.m.takePending(a); ok {
		t.Fatal("同一个请求不能被取走两次")
	}
	if f.held() != 1 {
		t.Fatalf("重复取不应再归还名额，实际 %d", f.held())
	}
}

func TestPendingQuotaReturnedWhenClientDropped(t *testing.T) {
	f := newQuotaFixture(t)
	f.open("1.1.1.1", "gone")
	f.open("2.2.2.2", "gone")
	keep := f.open("1.1.1.1", "kept")

	f.m.dropClient("gone")
	if f.held() != 1 || f.keys() != 1 {
		t.Fatalf("删除客户端后只应剩另一个客户端的 1 个名额，实际占 %d、key %d", f.held(), f.keys())
	}
	if _, ok := f.m.getPending(keep); !ok {
		t.Fatal("别的客户端的待决请求不应受影响")
	}
}

func TestGlobalPendingCapRecoversAfterExpiry(t *testing.T) {
	f := newQuotaFixture(t)
	for i := 0; i < oauthPendingGlobalMax; i++ {
		if f.open(fmt.Sprintf("10.%d.%d.%d", i/65536%256, i/256%256, i%256), "c") == "" {
			t.Fatalf("第 %d 个就被拒了，全局上限应当是 %d", i+1, oauthPendingGlobalMax)
		}
	}
	if f.open("9.9.9.9", "c") != "" {
		t.Fatal("占满全局上限后应当拒绝新的名额")
	}
	f.now = f.now.Add(pendingRequestTTL + time.Second)
	f.m.sweep()
	if f.open("9.9.9.9", "c") == "" {
		t.Fatalf("全部过期回收后应当能再申请，实际仍占 %d", f.held())
	}
}

func TestPutPendingWithoutReleaseHookDoesNotPanic(t *testing.T) {
	m := newMemoryStore() // release 为 nil：不接限流器的使用方式不能崩
	m.putPending(&pendingRequest{ID: "x", ClientID: "c", IP: "1.1.1.1", CreatedAt: time.Now()})
	if _, ok := m.takePending("x"); !ok {
		t.Fatal("应当取到")
	}
	m.putPending(&pendingRequest{ID: "y", ClientID: "c", IP: "1.1.1.1", CreatedAt: time.Now().Add(-2 * pendingRequestTTL)})
	m.sweep()
	m.dropClient("c")
}
