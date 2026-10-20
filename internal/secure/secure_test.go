package secure

import (
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func newFakeClock() *fakeClock { return &fakeClock{t: time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)} }

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

func TestNewCredentialIsRandomAndURLSafe(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 64; i++ {
		c, err := NewCredential()
		if err != nil {
			t.Fatal(err)
		}
		if len(c) != 43 { // 32 字节的 base64url 无填充
			t.Fatalf("凭据长度应为 43，实际 %d", len(c))
		}
		if strings.ContainsAny(c, "+/=") {
			t.Fatalf("凭据必须是 URL 安全字符: %q", c)
		}
		if seen[c] {
			t.Fatalf("随机凭据出现重复: %q", c)
		}
		seen[c] = true
	}
}

func TestTicketSingleUse(t *testing.T) {
	clk := newFakeClock()
	s := NewTicketStore(8)
	s.SetClock(clk.Now)

	tk, err := s.Issue("sse", "admin", "", 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	info, ok := s.Redeem(tk, "sse")
	if !ok || info.Subject != "admin" || info.Purpose != "sse" {
		t.Fatalf("首次兑换应成功: %+v ok=%v", info, ok)
	}
	if _, ok := s.Redeem(tk, "sse"); ok {
		t.Fatal("票据必须单次有效，第二次兑换应失败")
	}
	if s.Len() != 0 {
		t.Fatalf("兑换后不应残留: %d", s.Len())
	}
}

func TestTicketExpires(t *testing.T) {
	clk := newFakeClock()
	s := NewTicketStore(8)
	s.SetClock(clk.Now)

	tk, _ := s.Issue("term", "admin", "sess-1", 30*time.Second)
	clk.Advance(29 * time.Second)
	if _, ok := s.Redeem(tk, "term"); !ok {
		t.Fatal("29 秒内应可兑换")
	}

	tk2, _ := s.Issue("term", "admin", "sess-1", 30*time.Second)
	clk.Advance(30 * time.Second)
	if _, ok := s.Redeem(tk2, "term"); ok {
		t.Fatal("恰好到期（含边界）应失效")
	}
	if s.Len() != 0 {
		t.Fatal("过期票据在兑换时应被清除")
	}
}

func TestTicketPurposeMismatchDoesNotConsume(t *testing.T) {
	s := NewTicketStore(8)
	tk, _ := s.Issue("sse", "admin", "", time.Minute)

	if _, ok := s.Redeem(tk, "term"); ok {
		t.Fatal("用途不一致必须失败")
	}
	if _, ok := s.Redeem(tk, "sse"); !ok {
		t.Fatal("用途不一致的尝试不应消耗票据")
	}
}

func TestTicketBindIsReturned(t *testing.T) {
	s := NewTicketStore(8)
	tk, _ := s.Issue("term", "admin", "host=RM-X;sid=abc", time.Minute)
	info, ok := s.Redeem(tk, "term")
	if !ok || info.Bind != "host=RM-X;sid=abc" {
		t.Fatalf("绑定对象应原样返回: %+v", info)
	}
}

func TestTicketRejectsGarbage(t *testing.T) {
	s := NewTicketStore(8)
	_, _ = s.Issue("sse", "admin", "", time.Minute)
	for _, bad := range []string{"", "x", strings.Repeat("A", 43), "../../etc/passwd"} {
		if _, ok := s.Redeem(bad, "sse"); ok {
			t.Fatalf("随机串不应兑换成功: %q", bad)
		}
	}
}

func TestTicketCapacityAndPurge(t *testing.T) {
	clk := newFakeClock()
	s := NewTicketStore(3)
	s.SetClock(clk.Now)

	for i := 0; i < 3; i++ {
		if _, err := s.Issue("sse", "admin", "", 10*time.Second); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.Issue("sse", "admin", "", 10*time.Second); !errors.Is(err, ErrTooManyTickets) {
		t.Fatalf("达到上限应返回 ErrTooManyTickets: %v", err)
	}
	clk.Advance(11 * time.Second)
	if _, err := s.Issue("sse", "admin", "", 10*time.Second); err != nil {
		t.Fatalf("过期项应在签发时回收，释放名额: %v", err)
	}
}

func TestTicketRejectsNonPositiveTTL(t *testing.T) {
	s := NewTicketStore(1)
	if _, err := s.Issue("sse", "admin", "", 0); err == nil {
		t.Fatal("ttl<=0 必须被拒绝")
	}
}

func TestTicketRevokeSubject(t *testing.T) {
	s := NewTicketStore(8)
	a, _ := s.Issue("sse", "alice", "", time.Minute)
	b, _ := s.Issue("sse", "bob", "", time.Minute)
	s.RevokeSubject("alice")
	if _, ok := s.Redeem(a, "sse"); ok {
		t.Fatal("alice 的票据应已作废")
	}
	if _, ok := s.Redeem(b, "sse"); !ok {
		t.Fatal("bob 的票据不应受影响")
	}
	c, _ := s.Issue("sse", "bob", "", time.Minute)
	s.RevokeAll()
	if _, ok := s.Redeem(c, "sse"); ok {
		t.Fatal("RevokeAll 后应全部失效")
	}
}

func TestTicketConcurrentRedeemExactlyOnce(t *testing.T) {
	s := NewTicketStore(8)
	tk, _ := s.Issue("term", "admin", "", time.Minute)

	var wins atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 64; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, ok := s.Redeem(tk, "term"); ok {
				wins.Add(1)
			}
		}()
	}
	wg.Wait()
	if wins.Load() != 1 {
		t.Fatalf("并发兑换必须恰好成功一次，实际 %d", wins.Load())
	}
}

func TestUnlockIdleAndAbsoluteExpiry(t *testing.T) {
	clk := newFakeClock()
	s := NewUnlockStore(15*time.Minute, 8*time.Hour, 8)
	s.SetClock(clk.Now)

	tok, exp, err := s.Issue("admin")
	if err != nil {
		t.Fatal(err)
	}
	if want := clk.Now().Add(8 * time.Hour); !exp.Equal(want) {
		t.Fatalf("绝对过期时间应为 %v，实际 %v", want, exp)
	}

	// 每 10 分钟使用一次：空闲期被顺延，直到绝对期限才失效
	for i := 0; i < 47; i++ {
		clk.Advance(10 * time.Minute)
		if !s.Validate(tok, "admin") {
			t.Fatalf("第 %d 次（已过 %v）应仍有效", i+1, time.Duration(i+1)*10*time.Minute)
		}
	}
	clk.Advance(10 * time.Minute) // 累计 8 小时 10 分钟，超过绝对期限
	if s.Validate(tok, "admin") {
		t.Fatal("超过绝对期限必须失效，即使一直在使用")
	}
}

func TestUnlockIdleTimeout(t *testing.T) {
	clk := newFakeClock()
	s := NewUnlockStore(15*time.Minute, 8*time.Hour, 8)
	s.SetClock(clk.Now)

	tok, _, _ := s.Issue("admin")
	clk.Advance(14 * time.Minute)
	if !s.Validate(tok, "admin") {
		t.Fatal("14 分钟内应有效")
	}
	clk.Advance(15 * time.Minute)
	if s.Validate(tok, "admin") {
		t.Fatal("空闲满 15 分钟应失效")
	}
	if s.Len() != 0 {
		t.Fatal("失效令牌应被清除")
	}
}

// ValidateFresh：要求令牌是「刚刚」换取的。用得再勤、空闲期再新，只要创建时间超过 maxAge 就不通过；
// 通过与否都不影响普通的 Validate（令牌本身仍然有效）。
func TestUnlockValidateFresh(t *testing.T) {
	clk := newFakeClock()
	s := NewUnlockStore(15*time.Minute, 8*time.Hour, 8)
	s.SetClock(clk.Now)
	const window = 2 * time.Minute

	tok, _, _ := s.Issue("admin")
	if !s.ValidateFresh(tok, "admin", window) {
		t.Fatal("刚签发的令牌应通过新鲜度检查")
	}
	clk.Advance(90 * time.Second)
	if !s.ValidateFresh(tok, "admin", window) {
		t.Fatal("90 秒内应仍然新鲜")
	}
	clk.Advance(31 * time.Second) // 距离创建 121 秒，而空闲期因上面的使用被顺延，令牌本身仍有效
	if s.ValidateFresh(tok, "admin", window) {
		t.Fatal("创建超过 2 分钟的令牌不得通过新鲜度检查，哪怕一直在使用")
	}
	if !s.Validate(tok, "admin") {
		t.Fatal("新鲜度不足不应让令牌本身失效（普通解锁仍然成立）")
	}

	// 主体绑定、垃圾输入与非法窗口
	fresh, _, _ := s.Issue("admin")
	if s.ValidateFresh(fresh, "mallory", window) {
		t.Fatal("新鲜度检查同样要核对主体")
	}
	for _, bad := range []string{"", "nope"} {
		if s.ValidateFresh(bad, "admin", window) {
			t.Fatalf("不应通过: %q", bad)
		}
	}
	for _, w := range []time.Duration{0, -time.Second} {
		if s.ValidateFresh(fresh, "admin", w) {
			t.Fatalf("窗口 %v 不合法，一律不通过", w)
		}
	}
	// 过期的令牌（空闲期用尽）不新鲜，也被清除
	clk.Advance(20 * time.Minute)
	if s.ValidateFresh(fresh, "admin", time.Hour) {
		t.Fatal("已过空闲期的令牌不应通过")
	}
}

// 被拒绝的新鲜度检查不应顺延空闲期：它不是一次成功的使用。
func TestUnlockValidateFreshFailureDoesNotExtendIdle(t *testing.T) {
	clk := newFakeClock()
	s := NewUnlockStore(10*time.Minute, time.Hour, 8)
	s.SetClock(clk.Now)
	tok, _, _ := s.Issue("admin")

	clk.Advance(6 * time.Minute)
	if s.ValidateFresh(tok, "admin", time.Minute) {
		t.Fatal("6 分钟前的令牌不应满足 1 分钟的新鲜度")
	}
	clk.Advance(5 * time.Minute) // 距最初签发 11 分钟：若失败的检查顺延了空闲期，此刻仍有效
	if s.Validate(tok, "admin") {
		t.Fatal("失败的新鲜度检查不得顺延空闲期")
	}
}

func TestUnlockSubjectBinding(t *testing.T) {
	s := NewUnlockStore(0, 0, 0)
	tok, _, _ := s.Issue("alice")
	if s.Validate(tok, "bob") {
		t.Fatal("令牌不得被其他主体使用")
	}
	if !s.Validate(tok, "alice") {
		t.Fatal("归属主体应可使用（主体不符不应销毁令牌）")
	}
}

func TestUnlockRevoke(t *testing.T) {
	s := NewUnlockStore(0, 0, 0)
	a, _, _ := s.Issue("admin")
	b, _, _ := s.Issue("admin")
	s.Revoke(a)
	if s.Validate(a, "admin") {
		t.Fatal("已撤销令牌应失效")
	}
	if !s.Validate(b, "admin") {
		t.Fatal("撤销 a 不应影响 b")
	}
	s.RevokeAll()
	if s.Validate(b, "admin") {
		t.Fatal("RevokeAll 后全部失效")
	}
	s.Revoke("") // 空串不应 panic
}

func TestUnlockRejectsGarbageAndEmpty(t *testing.T) {
	s := NewUnlockStore(0, 0, 0)
	_, _, _ = s.Issue("admin")
	for _, bad := range []string{"", "nope", strings.Repeat("Z", 43)} {
		if s.Validate(bad, "admin") {
			t.Fatalf("不应通过: %q", bad)
		}
	}
}

func TestUnlockCapacity(t *testing.T) {
	clk := newFakeClock()
	s := NewUnlockStore(time.Minute, time.Hour, 2)
	s.SetClock(clk.Now)
	_, _, _ = s.Issue("admin")
	_, _, _ = s.Issue("admin")
	if _, _, err := s.Issue("admin"); !errors.Is(err, ErrTooManyUnlocks) {
		t.Fatalf("应返回 ErrTooManyUnlocks: %v", err)
	}
	clk.Advance(2 * time.Minute)
	if _, _, err := s.Issue("admin"); err != nil {
		t.Fatalf("空闲过期项回收后应能再签发: %v", err)
	}
}

func TestUnlockDefaults(t *testing.T) {
	s := NewUnlockStore(0, 0, 0)
	if s.IdleTTL() != DefaultUnlockIdle || s.AbsoluteTTL() != DefaultUnlockAbsolute {
		t.Fatalf("默认期限不符: idle=%v abs=%v", s.IdleTTL(), s.AbsoluteTTL())
	}
}

func TestUnlockPeekDoesNotExtendIdle(t *testing.T) {
	clock := newFakeClock()
	s := NewUnlockStore(10*time.Minute, time.Hour, 8)
	s.SetClock(clock.Now)

	token, _, err := s.Issue("alice")
	if err != nil {
		t.Fatal(err)
	}

	clock.Advance(6 * time.Minute)
	left, ok := s.Peek(token, "alice")
	if !ok || left != 4*time.Minute {
		t.Fatalf("Peek 应返回剩余 4 分钟: ok=%v left=%v", ok, left)
	}
	// Peek 不得顺延：再过 5 分钟就已超过最初的 10 分钟空闲期
	clock.Advance(5 * time.Minute)
	if _, ok := s.Peek(token, "alice"); ok {
		t.Fatal("Peek 不应顺延空闲期，令牌此刻应已过期")
	}

	// 对照：Validate 会顺延
	token2, _, _ := s.Issue("alice")
	clock.Advance(6 * time.Minute)
	if !s.Validate(token2, "alice") {
		t.Fatal("Validate 应通过")
	}
	clock.Advance(6 * time.Minute)
	if _, ok := s.Peek(token2, "alice"); !ok {
		t.Fatal("Validate 顺延后令牌应仍有效")
	}

	// 主体不匹配
	if _, ok := s.Peek(token2, "bob"); ok {
		t.Fatal("主体不匹配必须失败")
	}
	// 绝对期限优先于空闲期限
	token3, _, _ := s.Issue("alice")
	for i := 0; i < 7; i++ {
		clock.Advance(9 * time.Minute)
		if !s.Validate(token3, "alice") {
			break
		}
	}
	if left, ok := s.Peek(token3, "alice"); ok && left > time.Hour {
		t.Fatalf("剩余时间不得超过绝对期限: %v", left)
	}
}
