package holder

import (
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/teacat99/RelayMesh/v2/internal/agent/ptyhost"
	"github.com/teacat99/RelayMesh/v2/internal/termproto"
)

// 生命周期（H1）：人工终端默认永久保持；选择「空闲回收」后，只有「没人附着且没有输入」的会话才会被回收。

func idlePtr(sec int) *int { return &sec }

func (e *testEnv) tickAfter(d time.Duration) {
	e.clock.Advance(d)
	e.mgr.tick(e.clock.Now())
}

func TestPermanentSessionsAreNeverReaped(t *testing.T) {
	e := newTestEnv(t, Limits{})
	s, _ := e.open(t)
	e.tickAfter(400 * 24 * time.Hour)
	if e.mgr.Get(s.id) == nil {
		t.Fatal("默认永久保持的会话不应被回收")
	}
	if info := s.info(); info.Idle != 0 || info.IdleOwn {
		t.Fatalf("默认应报告永久保持且沿用主机默认: idle=%d own=%v", info.Idle, info.IdleOwn)
	}
}

func TestHostIdleReapsOnlyDetachedAndInputlessSessions(t *testing.T) {
	e := newTestEnv(t, Limits{})
	e.mgr.ApplyPolicy(termproto.Policy{Terminal: true, IdleSec: 3600})

	idle, _ := e.open(t)    // 从未附着、没有输入：到点回收
	watched, _ := e.open(t) // 有人附着：不回收
	typed, _ := e.open(t)   // 中途有输入：从最后一次输入重新计时
	attachOK(t, watched, &recSink{}, 1, 0)

	e.tickAfter(30 * time.Minute)
	typed.writeInput([]byte("ls\n"))
	e.tickAfter(29 * time.Minute) // 距创建 59 分钟
	for _, s := range []*Session{idle, watched, typed} {
		if e.mgr.Get(s.id) == nil {
			t.Fatalf("未到空闲时限，会话 %s 不应被回收", s.id[:8])
		}
	}

	e.tickAfter(2 * time.Minute) // 距创建 61 分钟，距最后输入 31 分钟
	eventually(t, "空闲且无输入的会话被回收", func() bool { return e.mgr.Get(idle.id) == nil })
	if e.mgr.Get(watched.id) == nil {
		t.Fatal("有人附着的会话不应被回收")
	}
	if e.mgr.Get(typed.id) == nil {
		t.Fatal("最近有输入的会话不应被回收")
	}

	e.tickAfter(30 * time.Minute) // 距最后输入 61 分钟
	eventually(t, "输入之后重新计满空闲时限才回收", func() bool { return e.mgr.Get(typed.id) == nil })
	if e.mgr.Get(watched.id) == nil {
		t.Fatal("有人附着的会话始终不应被回收")
	}
}

func TestDetachStartsTheIdleClock(t *testing.T) {
	e := newTestEnv(t, Limits{})
	e.mgr.ApplyPolicy(termproto.Policy{Terminal: true, IdleSec: 3600})
	s, _ := e.open(t)
	res := attachOK(t, s, &recSink{}, 1, 0)

	e.tickAfter(5 * time.Hour) // 一直有人看着：5 小时也不回收
	if e.mgr.Get(s.id) == nil {
		t.Fatal("有人附着期间不应计时")
	}
	res.ch.end("")
	e.tickAfter(59 * time.Minute)
	if e.mgr.Get(s.id) == nil {
		t.Fatal("断开之后需要重新计满空闲时限")
	}
	e.tickAfter(2 * time.Minute)
	eventually(t, "断开满一小时后回收", func() bool { return e.mgr.Get(s.id) == nil })
}

func TestOutputAloneDoesNotCountAsInteraction(t *testing.T) {
	e := newTestEnv(t, Limits{})
	e.mgr.ApplyPolicy(termproto.Policy{Terminal: true, IdleSec: 3600})
	s, term := e.open(t)

	for i := 0; i < 5; i++ {
		term.Emit([]byte("tick\r\n"))
		e.tickAfter(15 * time.Minute)
	}
	eventually(t, "只有输出的会话照常回收", func() bool { return e.mgr.Get(s.id) == nil })
}

func TestSessionOverrideBeatsHostDefault(t *testing.T) {
	e := newTestEnv(t, Limits{})
	e.mgr.ApplyPolicy(termproto.Policy{Terminal: true, IdleSec: 3600})
	inherit, _ := e.open(t)
	permanent, _ := e.open(t)
	twoHours, _ := e.open(t)
	permanent.setLifecycle(idlePtr(0))
	twoHours.setLifecycle(idlePtr(7200))

	if info := permanent.info(); !info.IdleOwn || info.Idle != 0 {
		t.Fatalf("单独设置为永久保持: %+v", info)
	}
	if info := twoHours.info(); !info.IdleOwn || info.Idle != 7200 {
		t.Fatalf("单独设置为 2 小时: %+v", info)
	}
	if info := inherit.info(); info.IdleOwn || info.Idle != 3600 {
		t.Fatalf("沿用主机默认应报告 3600 秒: %+v", info)
	}

	e.tickAfter(61 * time.Minute)
	eventually(t, "沿用主机默认的会话被回收", func() bool { return e.mgr.Get(inherit.id) == nil })
	if e.mgr.Get(permanent.id) == nil || e.mgr.Get(twoHours.id) == nil {
		t.Fatal("单独设置了更宽松生命周期的会话不应被回收")
	}

	e.tickAfter(61 * time.Minute)
	eventually(t, "2 小时的会话到点被回收", func() bool { return e.mgr.Get(twoHours.id) == nil })
	if e.mgr.Get(permanent.id) == nil {
		t.Fatal("永久保持的会话不应被回收")
	}

	// 改回沿用主机默认
	back, _ := e.open(t)
	back.setLifecycle(idlePtr(0))
	back.setLifecycle(nil)
	if info := back.info(); info.IdleOwn || info.Idle != 3600 {
		t.Fatalf("改回沿用主机默认: %+v", info)
	}
}

func TestLocalCeilingAlwaysWins(t *testing.T) {
	e := newTestEnv(t, Limits{DetachedTTL: 2 * time.Hour})

	// 主机默认永久保持、会话也要求永久保持：本机上限仍然生效
	s, _ := e.open(t)
	s.setLifecycle(idlePtr(0))
	if info := s.info(); info.Idle != 7200 {
		t.Fatalf("本机上限应封顶: idle=%d", info.Idle)
	}
	e.tickAfter(121 * time.Minute)
	eventually(t, "本机上限到点回收", func() bool { return e.mgr.Get(s.id) == nil })

	// 服务端策略可以更严，不能更宽
	e.mgr.ApplyPolicy(termproto.Policy{Terminal: true, IdleSec: 3600})
	tight, _ := e.open(t)
	if info := tight.info(); info.Idle != 3600 {
		t.Fatalf("策略比本机上限更严时应取更严者: idle=%d", info.Idle)
	}
	e.mgr.ApplyPolicy(termproto.Policy{Terminal: true, IdleSec: 86400})
	loose, _ := e.open(t)
	if info := loose.info(); info.Idle != 7200 {
		t.Fatalf("策略比本机上限更宽时仍以本机上限为准: idle=%d", info.Idle)
	}
}

func TestChangingLifecycleCountsAsInteraction(t *testing.T) {
	e := newTestEnv(t, Limits{})
	e.mgr.ApplyPolicy(termproto.Policy{Terminal: true, IdleSec: 3600})
	s, _ := e.open(t)

	e.tickAfter(50 * time.Minute)
	// 把永久保持改成 1 小时，不应让一个已空闲很久的会话立刻被回收
	s.setLifecycle(idlePtr(0))
	e.tickAfter(20 * time.Minute)
	s.setLifecycle(idlePtr(3600))
	e.tickAfter(30 * time.Minute)
	if e.mgr.Get(s.id) == nil {
		t.Fatal("修改生命周期本身算一次交互，应重新计时")
	}
	e.tickAfter(31 * time.Minute)
	eventually(t, "重新计满后回收", func() bool { return e.mgr.Get(s.id) == nil })
}

func TestInfoReportsLastActivity(t *testing.T) {
	e := newTestEnv(t, Limits{})
	s, _ := e.open(t)
	created := e.clock.Now().Unix()
	if got := s.info().Active; got != created {
		t.Fatalf("新会话的最后交互应为创建时刻: %d != %d", got, created)
	}

	e.clock.Advance(10 * time.Minute)
	s.writeInput([]byte("echo hi\n"))
	if got := s.info().Active; got != e.clock.Now().Unix() {
		t.Fatalf("输入应更新最后交互: %d", got)
	}

	e.clock.Advance(10 * time.Minute)
	res := attachOK(t, s, &recSink{}, 1, 0)
	e.clock.Advance(5 * time.Minute)
	if got := s.info().Active; got != e.clock.Now().Unix() {
		t.Fatalf("有人附着时最后交互即为现在: %d", got)
	}
	res.ch.end("")
	detachedAt := e.clock.Now().Unix()
	e.clock.Advance(20 * time.Minute)
	if got := s.info().Active; got != detachedAt {
		t.Fatalf("断开之后最后交互固定在断开时刻: %d != %d", got, detachedAt)
	}
}

func TestOpenWithLifecycleAndWorkingDirectory(t *testing.T) {
	e := newTestEnv(t, Limits{})
	dir := t.TempDir()

	info, err := e.mgr.OpenWith(OpenOptions{Name: "rebuild", Rows: 24, Cols: 80, IdleSec: idlePtr(7200), Cwd: dir})
	if err != nil {
		t.Fatal(err)
	}
	if !info.IdleOwn || info.Idle != 7200 {
		t.Fatalf("新建时指定的生命周期应生效: %+v", info)
	}
	terms := e.spawn.Terminals()
	if got := terms[len(terms)-1].Spec.Dir; got != filepath.Clean(dir) {
		t.Fatalf("应在指定目录启动: %q", got)
	}

	// 不存在 / 相对路径 / 文件：忽略，回落到默认目录
	file := filepath.Join(dir, "a-file")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{filepath.Join(dir, "missing"), "relative/dir", file} {
		if _, err := e.mgr.OpenWith(OpenOptions{Rows: 24, Cols: 80, Cwd: bad}); err != nil {
			t.Fatal(err)
		}
		terms = e.spawn.Terminals()
		if got := terms[len(terms)-1].Spec.Dir; got != "" {
			t.Fatalf("无效目录 %q 应被忽略，却使用了 %q", bad, got)
		}
	}
}

func TestLifecycleIsClampedToSaneRange(t *testing.T) {
	e := newTestEnv(t, Limits{})
	s, _ := e.open(t)
	s.setLifecycle(idlePtr(5)) // 过小：抬到下限，而不是立刻回收
	if got := s.info().Idle; got != termproto.MinIdleSec {
		t.Fatalf("过小的时长应抬到下限: %d", got)
	}
	s.setLifecycle(idlePtr(-3)) // 负数视为永久保持
	if got := s.info().Idle; got != 0 {
		t.Fatalf("负数应视为永久保持: %d", got)
	}
	s.setLifecycle(idlePtr(1 << 40))
	if got := s.info().Idle; got != termproto.MaxIdleSec {
		t.Fatalf("过大的时长应压到上限: %d", got)
	}
}

func TestExitedSessionsReportNoLifecycle(t *testing.T) {
	e := newTestEnv(t, Limits{})
	e.mgr.ApplyPolicy(termproto.Policy{Terminal: true, IdleSec: 3600})
	s, term := e.open(t)
	term.Exit(ptyhost.ExitStatus{Code: 0})
	eventually(t, "已退出", func() bool { return s.info().State == termproto.SessionExited })
	if info := s.info(); info.Idle != 0 || info.Active != 0 {
		t.Fatalf("已退出的会话不再有生命周期: %+v", info)
	}
}

func TestPolicyChangeOfIdleNotifiesSessionListWatchers(t *testing.T) {
	e := newTestEnv(t, Limits{})
	var n atomic.Int32
	e.mgr.SetNotifier(func() { n.Add(1) })
	s, _ := e.open(t)
	base := n.Load()

	e.mgr.ApplyPolicy(termproto.Policy{Terminal: true, IdleSec: 3600})
	if n.Load() == base {
		t.Fatal("主机默认生命周期变化后应通知会话列表更新")
	}
	if got := s.info().Idle; got != 3600 {
		t.Fatalf("已有会话报告的生效值应随主机默认变化: %d", got)
	}
	base = n.Load()
	e.mgr.ApplyPolicy(termproto.Policy{Terminal: true, IdleSec: 3600})
	if n.Load() != base {
		t.Fatal("策略没有变化时不应重复通知")
	}
}

// 精简会话（会话守护预设）：回放缓冲缩到 CompactRingSize、宽度封顶 CompactMaxCols，resize 也守这个上限；普通会话不受影响。
func TestCompactSessionLimits(t *testing.T) {
	e := newTestEnv(t, Limits{})

	info, err := e.mgr.OpenWith(OpenOptions{Name: "guarded", Rows: 40, Cols: 200, Compact: true})
	if err != nil {
		t.Fatal(err)
	}
	if info.Cols != termproto.CompactMaxCols {
		t.Fatalf("精简会话宽度应封顶 %d，实际 %d", termproto.CompactMaxCols, info.Cols)
	}
	s := e.mgr.Get(info.SID)
	if s == nil {
		t.Fatal("会话不存在")
	}
	chunk := make([]byte, 64<<10)
	// readLoop 持有 s.mu 读写环形缓冲：测试直接写缓冲时同样要加锁，否则是数据竞争。
	s.mu.Lock()
	s.ring.Write(chunk)
	kept := s.ring.End() - s.ring.Base()
	s.mu.Unlock()
	if kept != termproto.CompactRingSize {
		t.Fatalf("精简会话的缓冲应只留 %d 字节，实际 %d", termproto.CompactRingSize, kept)
	}
	s.resize(40, 300)
	if got := s.info().Cols; got != termproto.CompactMaxCols {
		t.Fatalf("resize 也应封顶: %d", got)
	}

	plain, err := e.mgr.OpenWith(OpenOptions{Name: "plain", Rows: 40, Cols: 200})
	if err != nil {
		t.Fatal(err)
	}
	if plain.Cols != 200 {
		t.Fatalf("普通会话不应被限宽: %d", plain.Cols)
	}
	ps := e.mgr.Get(plain.SID)
	if ps == nil {
		t.Fatal("会话不存在")
	}
	ps.mu.Lock()
	ps.ring.Write(chunk)
	base := ps.ring.Base()
	ps.mu.Unlock()
	if base != 0 {
		t.Fatal("普通会话缓冲不应被缩小")
	}
}
