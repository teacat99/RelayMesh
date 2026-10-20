package terminal

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// strongSecret 仅用于测试：随机形态、长度与字符种类都合格。
const strongSecret = "k3J9xQ2vL7mZ0pR4tY8wB1nC5dF6gH3a"

type fakeCreds struct {
	mu   sync.Mutex
	user string
	pass string
	err  error
}

func (f *fakeCreds) EffectiveCredentials(ctx context.Context) (string, string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.user, f.pass, f.err
}

func (f *fakeCreds) set(user, pass string, err error) {
	f.mu.Lock()
	f.user, f.pass, f.err = user, pass, err
	f.mu.Unlock()
}

func TestResolveMode(t *testing.T) {
	cases := []struct {
		in      string
		want    Mode
		warning bool
	}{
		{"", ModeAuto, false},
		{"auto", ModeAuto, false},
		{" ON ", ModeAuto, false},
		{"true", ModeAuto, false},
		{"1", ModeAuto, false},
		{"off", ModeOff, false},
		{"FALSE", ModeOff, false},
		{"0", ModeOff, false},
		{"disabled", ModeOff, false},
		{"enable-please", ModeOff, true}, // 写错时保持关闭，并给出提示
		{"maybe", ModeOff, true},
	}
	for _, c := range cases {
		got, warn := ResolveMode(c.in)
		if got != c.want || (warn != "") != c.warning {
			t.Errorf("ResolveMode(%q) = (%q, %q)，期望 (%q, 提示=%v)", c.in, got, warn, c.want, c.warning)
		}
	}
}

func TestResolveDiscovery(t *testing.T) {
	cases := []struct {
		in      string
		always  bool
		warning bool
	}{
		{"", false, false},
		{"window", false, false},
		{"always", true, false},
		{"TRUE", true, false},
		{"garbage", false, true}, // 写错时回落到更安全的窗口模式
	}
	for _, c := range cases {
		got, warn := ResolveDiscovery(c.in)
		if got != c.always || (warn != "") != c.warning {
			t.Errorf("ResolveDiscovery(%q) = (%v, %q)", c.in, got, warn)
		}
	}
}

func TestGateEvaluateMatrix(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name   string
		mode   Mode
		secret string
		user   string
		pass   string
		err    error
		want   Decision
	}{
		{"全部满足", ModeAuto, strongSecret, "admin", "p", nil, Decision{true, ReasonOK}},
		{"环境强制关闭优先", ModeOff, strongSecret, "admin", "p", nil, Decision{false, ReasonDisabledByEnv}},
		{"弱密钥", ModeAuto, "short", "admin", "p", nil, Decision{false, ReasonWeakJWTSecret}},
		{"内置默认密钥", ModeAuto, "relaymesh-secret-key-change-in-production", "admin", "p", nil, Decision{false, ReasonWeakJWTSecret}},
		{"未设密码", ModeAuto, strongSecret, "admin", "", nil, Decision{false, ReasonNoPassword}},
		{"读取凭据失败按关闭处理", ModeAuto, strongSecret, "", "", errors.New("db down"), Decision{false, ReasonInternal}},
	}
	for _, c := range cases {
		g := NewGate(c.mode, c.secret, &fakeCreds{user: c.user, pass: c.pass, err: c.err})
		if got := g.Evaluate(ctx); got != c.want {
			t.Errorf("%s: 得到 %+v，期望 %+v", c.name, got, c.want)
		}
	}
	if got := NewGate(ModeAuto, strongSecret, nil).Evaluate(ctx); got.Open || got.Reason != ReasonInternal {
		t.Errorf("缺少凭据源必须 fail-closed: %+v", got)
	}
}

func TestGateWatchReportsFlipsOnly(t *testing.T) {
	creds := &fakeCreds{user: "admin", pass: "pw"}
	g := NewGate(ModeAuto, strongSecret, creds)

	type flip struct{ prev, cur Decision }
	flips := make(chan flip, 8)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		g.Watch(ctx, 10*time.Millisecond, func(prev, cur Decision) { flips <- flip{prev, cur} })
	}()

	// 基线不触发回调
	select {
	case f := <-flips:
		t.Fatalf("首次判定只记录基线，不应回调: %+v", f)
	case <-time.After(60 * time.Millisecond):
	}

	creds.set("admin", "", nil) // 管理员清空了密码
	select {
	case f := <-flips:
		if !f.prev.Open || f.cur.Open || f.cur.Reason != ReasonNoPassword {
			t.Fatalf("翻转内容不符: %+v", f)
		}
	case <-time.After(time.Second):
		t.Fatal("密码被清空后应在一个轮询周期内回调")
	}

	// 状态不变不重复回调
	select {
	case f := <-flips:
		t.Fatalf("状态未变不应再回调: %+v", f)
	case <-time.After(60 * time.Millisecond):
	}

	creds.set("admin", "pw", nil)
	select {
	case f := <-flips:
		if f.prev.Open || !f.cur.Open {
			t.Fatalf("恢复后应报告重新开启: %+v", f)
		}
	case <-time.After(time.Second):
		t.Fatal("密码恢复后应回调")
	}

	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("ctx 结束后 Watch 必须返回")
	}
}

func TestGateObserveTriggersImmediately(t *testing.T) {
	creds := &fakeCreds{user: "admin", pass: "pw"}
	g := NewGate(ModeAuto, strongSecret, creds)
	ctx := context.Background()
	var calls atomic.Int32
	g.Observe(ctx, func(prev, cur Decision) { calls.Add(1) }) // 基线
	creds.set("admin", "", nil)
	g.Observe(ctx, func(prev, cur Decision) { calls.Add(1) })
	if calls.Load() != 1 {
		t.Fatalf("Observe 应只在翻转时回调一次，实际 %d", calls.Load())
	}
}
