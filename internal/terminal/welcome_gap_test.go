package terminal

import (
	"sync/atomic"
	"testing"
	"time"
)

// #216：bringOnline 先把策略快照写进 welcome，之后还要写库、登记在线，推送只会发给已登记的连接。
// welcome 写出与登记完成之间发生的策略变更（这里是管理员刚把主机默认生命周期改成 24 小时）
// 既不在 welcome 里，也推不到这条新连接，它会一直拿着旧策略。
// 用 afterWelcomeHook 把变更稳定地放进这个窗口：REST 返回时库里已经是新值，但这条连接还没登记。
func TestPolicyChangedBetweenWelcomeAndRegistrationReachesTheAgent(t *testing.T) {
	e := newTestEnv(t)
	meta := testMeta("welcome-gap")
	a := e.enrollAgent(meta, "")
	hdr := e.admin()
	a.close()
	eventually(t, 2*time.Second, "旧连接已下线", func() bool { return e.hub.onlineAgent(a.code) == nil })

	var fired atomic.Int32
	hook := func(code string) {
		if fired.Add(1) != 1 {
			return
		}
		res := e.call("POST", lifecyclePath(code, ""), map[string]int{"idle_seconds": 86400}, hdr...)
		if res.Status != 200 {
			t.Errorf("窗口内改主机默认生命周期: %d %s", res.Status, res.Raw)
		}
	}
	e.hub.afterWelcomeHook.Store(&hook)

	b := e.connectEnabled(a.priv, meta)
	if b.welcome.Policy.IdleSec != 0 {
		t.Fatalf("welcome 取在变更之前，应还是永久保持: %+v", b.welcome.Policy)
	}
	eventually(t, 2*time.Second, "窗口内的策略变更补推给了新连接", func() bool {
		p, ok := lastPolicy(b)
		return ok && p.IdleSec == 86400 && p.Terminal
	})
	if n := fired.Load(); n != 1 {
		t.Fatalf("钩子应恰好触发一次: %d", n)
	}

	// 补推只有一条，且没有影响之后的正常推送：再改回永久保持，新连接照常收到。
	if res := e.call("POST", lifecyclePath(b.code, ""), map[string]int{"idle_seconds": 0}, hdr...); res.Status != 200 {
		t.Fatalf("改回永久保持: %d %s", res.Status, res.Raw)
	}
	eventually(t, 2*time.Second, "之后的推送照常送达", func() bool {
		p, ok := lastPolicy(b)
		return ok && p.IdleSec == 0
	})
	b.mu.Lock()
	n := len(b.policies)
	b.mu.Unlock()
	if n != 2 {
		t.Fatalf("新连接应恰好收到两条策略（补推一条、之后改回一条）: %d", n)
	}
}

// 窗口里什么都没变时，不该多发一条策略：补推只在策略真的不同时才发。
func TestNoCatchUpPolicyWhenNothingChangedBetweenWelcomeAndRegistration(t *testing.T) {
	e := newTestEnv(t)
	meta := testMeta("welcome-quiet")
	a := e.enrollAgent(meta, "")
	a.close()
	eventually(t, 2*time.Second, "旧连接已下线", func() bool { return e.hub.onlineAgent(a.code) == nil })

	b := e.connectEnabled(a.priv, meta)
	eventually(t, 2*time.Second, "新连接已登记在线", func() bool { return e.hub.onlineAgent(b.code) != nil })
	// 补推（如果有）紧跟在登记之后，几乎立刻就会到；这里多等一会儿，只可能漏报，不会误报。
	time.Sleep(200 * time.Millisecond)
	b.mu.Lock()
	n := len(b.policies)
	b.mu.Unlock()
	if n != 0 {
		t.Fatalf("没有策略变更时不该有补推: %d 条", n)
	}
}
