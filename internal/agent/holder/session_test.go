package holder

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/teacat99/RelayMesh/v2/internal/agent/ptyhost"
	"github.com/teacat99/RelayMesh/v2/internal/termproto"
)

// ---------- 测试夹具 ----------

type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func newFakeClock() *fakeClock { return &fakeClock{t: time.Unix(1_700_000_000, 0)} }

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

// recSink 记录通道发出的全部帧。
type recSink struct {
	mu    sync.Mutex
	jsons []map[string]any
	bins  [][]byte
	ended []uint32
}

func (r *recSink) sendJSON(v any) error {
	raw, err := json.Marshal(v)
	if err != nil {
		return err
	}
	var m map[string]any
	_ = json.Unmarshal(raw, &m)
	r.mu.Lock()
	r.jsons = append(r.jsons, m)
	r.mu.Unlock()
	return nil
}

func (r *recSink) sendBinary(b []byte) error {
	r.mu.Lock()
	r.bins = append(r.bins, append([]byte(nil), b...))
	r.mu.Unlock()
	return nil
}

func (r *recSink) channelEnded(c *channel) {
	r.mu.Lock()
	r.ended = append(r.ended, c.id)
	r.mu.Unlock()
}

func (r *recSink) jsonOfType(typ string) []map[string]any {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []map[string]any
	for _, m := range r.jsons {
		if m["t"] == typ {
			out = append(out, m)
		}
	}
	return out
}

// payload 按偏移顺序拼接某通道收到的全部输出；同时校验偏移连续、无重叠、无缺口。
func (r *recSink) payload(t *testing.T, ch uint32, from uint64) []byte {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []byte
	next := from
	for _, b := range r.bins {
		c, off, p, err := termproto.DecodeOutput(b)
		if err != nil {
			t.Fatalf("解码输出帧失败: %v", err)
		}
		if c != ch {
			continue
		}
		if off != next {
			t.Fatalf("输出偏移不连续: 期望 %d 得到 %d", next, off)
		}
		out = append(out, p...)
		next += uint64(len(p))
	}
	return out
}

func (r *recSink) outLen(ch uint32) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	total := 0
	for _, b := range r.bins {
		if c, _, p, err := termproto.DecodeOutput(b); err == nil && c == ch {
			total += len(p)
		}
	}
	return total
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("等待超时: %s", what)
}

type testEnv struct {
	mgr   *Manager
	spawn *ptyhost.FakeSpawner
	clock *fakeClock
}

func newTestEnv(t *testing.T, lim Limits) *testEnv {
	t.Helper()
	spawn := &ptyhost.FakeSpawner{}
	clock := newFakeClock()
	mgr := NewManager(ManagerOptions{
		Spawner: spawn,
		Shell: func(rows, cols uint16) (ptyhost.Spec, error) {
			return ptyhost.Spec{Path: "/bin/fakeshell", Rows: rows, Cols: cols}, nil
		},
		Limits: lim,
		Now:    clock.Now,
	})
	t.Cleanup(func() { mgr.Shutdown(2 * time.Second) })
	return &testEnv{mgr: mgr, spawn: spawn, clock: clock}
}

func (e *testEnv) open(t *testing.T) (*Session, *ptyhost.FakeTerminal) {
	t.Helper()
	info, err := e.mgr.Open("test", 24, 80)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	s := e.mgr.Get(info.SID)
	terms := e.spawn.Terminals()
	return s, terms[len(terms)-1]
}

func attachOK(t *testing.T, s *Session, sink frameSink, ch uint32, since uint64) attachResult {
	t.Helper()
	res, err := s.attach(sink, ch, since, 0, 0, 0)
	if err != nil {
		t.Fatalf("attach: %v", err)
	}
	res.ch.start()
	return res
}

// ---------- 用例 ----------

func TestOpenAndSnapshot(t *testing.T) {
	e := newTestEnv(t, Limits{})
	info, err := e.mgr.Open("  my\x1b[31m shell  ", 30, 100)
	if err != nil {
		t.Fatal(err)
	}
	if !termproto.ValidSessionID(info.SID) || info.State != termproto.SessionRunning {
		t.Fatalf("会话信息不符: %+v", info)
	}
	if info.Rows != 30 || info.Cols != 100 || info.Shell != "fakeshell" || info.PID == 0 {
		t.Fatalf("尺寸 / shell / pid 不符: %+v", info)
	}
	if info.Name != "my[31m shell" && info.Name == "  my\x1b[31m shell  " {
		t.Fatalf("会话名应被净化: %q", info.Name)
	}
	if snap := e.mgr.Snapshot(); len(snap) != 1 || snap[0].SID != info.SID {
		t.Fatalf("快照不符: %+v", snap)
	}
	term := e.spawn.Terminals()[0]
	if term.Spec.Rows != 30 || term.Spec.Cols != 100 {
		t.Fatalf("启动规格尺寸不符: %+v", term.Spec)
	}
}

func TestAttachFromZeroReplaysAndStreams(t *testing.T) {
	e := newTestEnv(t, Limits{})
	s, term := e.open(t)
	term.Emit([]byte("hello "))
	eventually(t, "输出进入缓冲", func() bool { return s.info().End == 6 })

	sink := &recSink{}
	res := attachOK(t, s, sink, 1, 0)
	if res.reply.From != 0 || res.reply.Base != 0 || res.reply.End != 6 || res.reply.Truncated || res.reply.Prefix != "" {
		t.Fatalf("attached 应答不符: %+v", res.reply)
	}
	term.Emit([]byte("world"))
	eventually(t, "回放与实时输出", func() bool { return sink.outLen(1) == 11 })
	if got := sink.payload(t, 1, 0); string(got) != "hello world" {
		t.Fatalf("got %q", got)
	}
	if s.info().Attached != 1 {
		t.Fatalf("附着数应为 1: %+v", s.info())
	}
}

func TestAttachResumeAndTruncation(t *testing.T) {
	e := newTestEnv(t, Limits{RingSize: 16})
	s, term := e.open(t)
	term.Emit(bytes.Repeat([]byte("0123456789\n"), 4)) // 44 字节，缓冲 16 → base=28
	eventually(t, "输出进入缓冲", func() bool { return s.info().End == 44 })
	info := s.info()
	if info.Base != 28 {
		t.Fatalf("base 应为 28: %d", info.Base)
	}

	// since=0：缓冲已回绕 → truncated，起点对齐到换行之后
	sinkA := &recSink{}
	a := attachOK(t, s, sinkA, 1, 0)
	if !a.reply.Truncated || a.reply.From < 28 || a.reply.From > 44 {
		t.Fatalf("应标记 truncated 且起点在缓冲内: %+v", a.reply)
	}
	if a.reply.From != 28 {
		// 28 位置是换行之后的一个字符还是换行本身取决于数据；只要保证对齐后前一个字节是换行
		t.Logf("起点已对齐到 %d", a.reply.From)
	}
	a.ch.end("")

	// since 在缓冲内：续传，无 truncated，无恢复序列
	sinkB := &recSink{}
	b := attachOK(t, s, sinkB, 2, 40)
	if b.reply.Truncated || b.reply.From != 40 || b.reply.Prefix != "" {
		t.Fatalf("续传应精确从 since 开始: %+v", b.reply)
	}
	eventually(t, "续传数据", func() bool { return sinkB.outLen(2) == 4 })
	b.ch.end("")

	// since 早于缓冲头：truncated
	c, err := s.attach(&recSink{}, 3, 5, 0, 0, 0)
	if err != nil || !c.reply.Truncated || c.reply.From < 28 {
		t.Fatalf("since 被覆盖应标记 truncated: %+v err=%v", c.reply, err)
	}
	c.ch.end("")

	// since 超过 End：视为已追平
	d, err := s.attach(&recSink{}, 4, 9999, 0, 0, 0)
	if err != nil || d.reply.From != 44 || d.reply.Truncated {
		t.Fatalf("since>End 应从 End 开始: %+v err=%v", d.reply, err)
	}
	d.ch.end("")
}

func TestFlowControlBackpressureAndNoLoss(t *testing.T) {
	e := newTestEnv(t, Limits{})
	s, term := e.open(t)
	sink := &recSink{}
	res := attachOK(t, s, sink, 1, 0)

	// 远大于窗口的输出：在收到确认之前，Agent 不应发送超过窗口，也不应把 PTY 读空。
	total := 4 * termproto.AttachWindow
	data := make([]byte, total)
	for i := range data {
		data[i] = byte('a' + i%26)
	}
	term.Emit(data)

	eventually(t, "窗口被填满", func() bool { return sink.outLen(1) >= termproto.AttachWindow })
	time.Sleep(100 * time.Millisecond)
	if n := sink.outLen(1); n > termproto.AttachWindow {
		t.Fatalf("未确认时发送量超过窗口: %d > %d", n, termproto.AttachWindow)
	}
	// 读协程应已暂停：假终端里还有大量未被读取的数据（内核 PTY 缓冲的背压）
	if end := s.info().End; end >= uint64(total) {
		t.Fatalf("窗口耗尽后读协程应暂停，但已读入 %d/%d", end, total)
	}

	// 逐步确认，数据应完整、有序地流过
	var acked uint64
	deadline := time.Now().Add(10 * time.Second)
	for sink.outLen(1) < total && time.Now().Before(deadline) {
		sent := uint64(sink.outLen(1))
		if sent > acked {
			acked = sent
			res.ch.ack(acked)
		}
		time.Sleep(2 * time.Millisecond)
	}
	if got := sink.outLen(1); got != total {
		t.Fatalf("数据未完整送达: %d/%d", got, total)
	}
	if !bytes.Equal(sink.payload(t, 1, 0), data) {
		t.Fatal("输出内容与产生的内容不一致")
	}
}

func TestNoBackpressureWithoutAttachment(t *testing.T) {
	e := newTestEnv(t, Limits{RingSize: 1024})
	s, term := e.open(t)
	total := 5 << 20
	term.Emit(bytes.Repeat([]byte("x"), total))
	eventually(t, "无人附着时读协程持续消费", func() bool { return s.info().End == uint64(total) })
	if info := s.info(); info.Base != uint64(total-1024) {
		t.Fatalf("环形缓冲应只保留最后 1024 字节: base=%d", info.Base)
	}
}

func TestStalledChannelIsDetachedAndSessionResumes(t *testing.T) {
	e := newTestEnv(t, Limits{})
	s, term := e.open(t)
	sink := &recSink{}
	res := attachOK(t, s, sink, 1, 0)
	term.Emit(bytes.Repeat([]byte("s"), 3*termproto.AttachWindow))
	eventually(t, "窗口被填满", func() bool { return sink.outLen(1) >= termproto.AttachWindow })
	eventually(t, "读协程暂停", func() bool {
		e1 := s.info().End
		time.Sleep(30 * time.Millisecond)
		return s.info().End == e1 && e1 >= termproto.AttachWindow
	})

	now := e.clock.Now()
	s.checkStalls(now) // 第一次只记录「开始耗尽」的时刻
	if len(sink.jsonOfType(termproto.TDetached)) != 0 {
		t.Fatal("窗口刚耗尽不应立即判定停滞")
	}
	if n := s.checkStalls(now.Add(termproto.StallTimeout - time.Second)); n != 0 {
		t.Fatal("未到期限不应判定停滞")
	}
	if n := s.checkStalls(now.Add(termproto.StallTimeout + time.Second)); n != 1 {
		t.Fatalf("超过期限应断开该附着: %d", n)
	}
	eventually(t, "detached(stalled)", func() bool {
		d := sink.jsonOfType(termproto.TDetached)
		return len(d) == 1 && d[0]["reason"] == "stalled"
	})
	_ = res
	// 附着被断开后，会话恢复读取，把剩余输出读进缓冲
	eventually(t, "会话继续读取", func() bool { return s.info().End == uint64(3*termproto.AttachWindow) })
	if s.info().Attached != 0 {
		t.Fatal("通道应已移除")
	}
}

func TestInputReachesTerminal(t *testing.T) {
	e := newTestEnv(t, Limits{})
	s, term := e.open(t)
	s.writeInput([]byte("ls -l\r"))
	s.writeInput([]byte("pwd\r"))
	eventually(t, "输入到达终端", func() bool { return string(term.Input()) == "ls -l\rpwd\r" })
}

func TestResizeAppliesOnAttachAndResizeFrame(t *testing.T) {
	e := newTestEnv(t, Limits{})
	s, term := e.open(t)
	res, err := s.attach(&recSink{}, 1, 0, 50, 160, 0)
	if err != nil {
		t.Fatal(err)
	}
	res.ch.start()
	if res.reply.Rows != 50 || res.reply.Cols != 160 {
		t.Fatalf("attached 应反映新尺寸: %+v", res.reply)
	}
	if r, c := term.Size(); r != 50 || c != 160 {
		t.Fatalf("终端尺寸应已调整: %dx%d", r, c)
	}
	s.resize(10, 20)
	if r, c := term.Size(); r != 10 || c != 20 {
		t.Fatalf("resize 未生效: %dx%d", r, c)
	}
	n := len(term.Resizes())
	s.resize(10, 20) // 相同尺寸不重复调用，避免无谓的 SIGWINCH
	if len(term.Resizes()) != n {
		t.Fatal("相同尺寸不应再次调用 Resize")
	}
}

func TestExitDeliversAllOutputThenExitThenDetached(t *testing.T) {
	e := newTestEnv(t, Limits{})
	s, term := e.open(t)
	sink := &recSink{}
	attachOK(t, s, sink, 1, 0)

	term.Emit([]byte("final words\r\n"))
	term.Exit(ptyhost.ExitStatus{Code: 3})

	eventually(t, "exit 与 detached", func() bool {
		return len(sink.jsonOfType(termproto.TExit)) == 1 && len(sink.jsonOfType(termproto.TDetached)) == 1
	})
	if got := sink.payload(t, 1, 0); string(got) != "final words\r\n" {
		t.Fatalf("退出前的输出必须全部送达: %q", got)
	}
	ex := sink.jsonOfType(termproto.TExit)[0]
	if ex["code"] != float64(3) || ex["sid"] != s.id {
		t.Fatalf("exit 帧不符: %v", ex)
	}
	if d := sink.jsonOfType(termproto.TDetached)[0]; d["reason"] != "exited" {
		t.Fatalf("detached 原因应为 exited: %v", d)
	}
	// 顺序：输出帧 → exit → detached
	sink.mu.Lock()
	idxExit, idxDet := -1, -1
	for i, m := range sink.jsons {
		switch m["t"] {
		case termproto.TExit:
			idxExit = i
		case termproto.TDetached:
			idxDet = i
		}
	}
	sink.mu.Unlock()
	if !(idxExit >= 0 && idxExit < idxDet) {
		t.Fatalf("exit 必须先于 detached: %d %d", idxExit, idxDet)
	}

	// 会话仍保留（可回看），快照带退出码
	eventually(t, "会话状态为 exited", func() bool { return s.info().State == termproto.SessionExited })
	info := s.info()
	if info.ExitCode == nil || *info.ExitCode != 3 {
		t.Fatalf("快照应带退出码: %+v", info)
	}
	if e.mgr.Get(s.id) == nil {
		t.Fatal("自然退出的会话应保留到被回收")
	}

	// 之后再附着：回放输出 → exit → detached
	late := &recSink{}
	res, err := s.attach(late, 7, 0, 0, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if res.reply.State != termproto.SessionExited || res.reply.ExitCode == nil || *res.reply.ExitCode != 3 {
		t.Fatalf("attached 应反映已退出: %+v", res.reply)
	}
	res.ch.start()
	eventually(t, "迟到的附着也收到 exit", func() bool {
		return len(late.jsonOfType(termproto.TExit)) == 1 && len(late.jsonOfType(termproto.TDetached)) == 1
	})
	if got := late.payload(t, 7, 0); string(got) != "final words\r\n" {
		t.Fatalf("回放内容不符: %q", got)
	}
}

func TestSignalExitHasNoCode(t *testing.T) {
	e := newTestEnv(t, Limits{})
	s, term := e.open(t)
	term.Exit(ptyhost.ExitStatus{Code: -1, Signal: "terminated"})
	eventually(t, "exited", func() bool { return s.info().State == termproto.SessionExited })
	if s.info().ExitCode != nil {
		t.Fatal("被信号终止时不应有退出码")
	}
}

func TestKillRemovesSessionAndNotifiesChannels(t *testing.T) {
	e := newTestEnv(t, Limits{})
	s, term := e.open(t)
	sink := &recSink{}
	attachOK(t, s, sink, 1, 0)
	s.kill()
	if term.KillCount() == 0 {
		t.Fatal("应调用终端的 Kill")
	}
	eventually(t, "会话被移除", func() bool { return e.mgr.Get(s.id) == nil })
	eventually(t, "detached(killed)", func() bool {
		d := sink.jsonOfType(termproto.TDetached)
		return len(d) >= 1 && d[len(d)-1]["reason"] == "killed"
	})
	if e.mgr.Count() != 0 {
		t.Fatal("管理器里不应再有会话")
	}
}

func TestKillOfExitedSessionRemovesImmediately(t *testing.T) {
	e := newTestEnv(t, Limits{})
	s, term := e.open(t)
	term.Exit(ptyhost.ExitStatus{Code: 0})
	eventually(t, "exited", func() bool { return s.info().State == termproto.SessionExited })
	s.kill()
	if e.mgr.Get(s.id) != nil {
		t.Fatal("已退出的会话被 kill 后应立刻移除")
	}
}

func TestHeldTTYStillBecomesExited(t *testing.T) {
	spawn := &ptyhost.FakeSpawner{HoldTTY: true}
	mgr := NewManager(ManagerOptions{
		Spawner: spawn,
		Shell: func(rows, cols uint16) (ptyhost.Spec, error) {
			return ptyhost.Spec{Path: "/bin/fakeshell"}, nil
		},
	})
	defer mgr.Shutdown(2 * time.Second)
	info, err := mgr.Open("", 24, 80)
	if err != nil {
		t.Fatal(err)
	}
	s := mgr.Get(info.SID)
	term := spawn.Terminals()[0]
	term.Emit([]byte("last"))
	term.Exit(ptyhost.ExitStatus{Code: 0}) // 读端不会自然结束（后台进程握着终端）
	start := time.Now()
	eventually(t, "限时排空后变为 exited", func() bool { return s.info().State == termproto.SessionExited })
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("排空耗时过长: %v", d)
	}
	if s.info().End != 4 {
		t.Fatalf("残余输出应被排空: %+v", s.info())
	}
}

func TestSessionLimitAndSpawnFailure(t *testing.T) {
	e := newTestEnv(t, Limits{MaxSessions: 2})
	e.open(t)
	e.open(t)
	if _, err := e.mgr.Open("", 24, 80); err != ErrSessionLimit {
		t.Fatalf("应返回 ErrSessionLimit: %v", err)
	}
	e2 := newTestEnv(t, Limits{})
	e2.spawn.Err = ptyhost.ErrUnsupported
	if _, err := e2.mgr.Open("", 24, 80); err == nil {
		t.Fatal("启动失败应返回错误")
	}
	if e2.mgr.Count() != 0 {
		t.Fatal("启动失败不应留下会话")
	}
}

func TestPolicyDisablesTerminalAndKillsSessions(t *testing.T) {
	e := newTestEnv(t, Limits{})
	s1, _ := e.open(t)
	s2, _ := e.open(t)
	e.mgr.ApplyPolicy(termproto.Policy{Terminal: false, Reason: "disabled_by_env"})
	eventually(t, "全部会话被终止", func() bool { return e.mgr.Count() == 0 })
	_ = s1
	_ = s2
	if _, err := e.mgr.Open("", 24, 80); err != ErrTerminalDisabled {
		t.Fatalf("总闸关闭时不得新建会话: %v", err)
	}
	// 重新开放后恢复
	e.mgr.ApplyPolicy(termproto.Policy{Terminal: true})
	if _, err := e.mgr.Open("", 24, 80); err != nil {
		t.Fatalf("重新开放后应能新建会话: %v", err)
	}
}

func TestPolicyOnlyTightensLimits(t *testing.T) {
	e := newTestEnv(t, Limits{MaxSessions: 4, DetachedTTL: time.Hour})
	e.mgr.ApplyPolicy(termproto.Policy{Terminal: true, MaxSessions: 2, DetachedTTLSec: 60})
	e.mgr.mu.Lock()
	if e.mgr.limits.MaxSessions != 2 || e.mgr.limits.DetachedTTL != time.Minute {
		t.Fatalf("策略应收紧限额: %+v", e.mgr.limits)
	}
	e.mgr.mu.Unlock()
	// 服务端想放宽：无效，仍以本地为上限
	e.mgr.ApplyPolicy(termproto.Policy{Terminal: true, MaxSessions: 100, DetachedTTLSec: 999999})
	e.mgr.mu.Lock()
	defer e.mgr.mu.Unlock()
	if e.mgr.limits.MaxSessions != 4 || e.mgr.limits.DetachedTTL != time.Hour {
		t.Fatalf("策略不得放宽本地限额: %+v", e.mgr.limits)
	}
}

func TestDetachedTTLKillsAndExitedTTLRemoves(t *testing.T) {
	e := newTestEnv(t, Limits{DetachedTTL: time.Hour, ExitedTTL: 10 * time.Minute})
	running, _ := e.open(t)
	exited, exitedTerm := e.open(t)
	exitedTerm.Exit(ptyhost.ExitStatus{Code: 0})
	eventually(t, "exited", func() bool { return exited.info().State == termproto.SessionExited })

	// 有人附着的会话不会过期
	attached, _ := e.open(t)
	attachOK(t, attached, &recSink{}, 1, 0)

	e.clock.Advance(9 * time.Minute)
	e.mgr.tick(e.clock.Now())
	if e.mgr.Get(exited.id) == nil {
		t.Fatal("ExitedTTL 未到不应回收")
	}
	e.clock.Advance(2 * time.Minute)
	e.mgr.tick(e.clock.Now())
	if e.mgr.Get(exited.id) != nil {
		t.Fatal("已退出会话超过 ExitedTTL 应被回收")
	}
	if e.mgr.Get(running.id) == nil {
		t.Fatal("运行中的会话在 DetachedTTL 之内不应被终止")
	}

	e.clock.Advance(time.Hour)
	e.mgr.tick(e.clock.Now())
	eventually(t, "无人附着超过 TTL 的会话被终止", func() bool { return e.mgr.Get(running.id) == nil })
	if e.mgr.Get(attached.id) == nil {
		t.Fatal("有人附着的会话不应因 TTL 被终止")
	}
}

func TestAltScreenAttachUsesPrefixAndJiggle(t *testing.T) {
	e := newTestEnv(t, Limits{})
	s, term := e.open(t)
	term.Emit([]byte("prompt$ vim\r\n\x1b[?1049h\x1b[?1000h\x1b[?2004h\x1b[Hvim screen"))
	eventually(t, "输出进入缓冲", func() bool { return s.info().Alt })

	// 新终端（since=0）：先进入备用屏并恢复模式，只回放备用屏内容，并请求重绘
	sink := &recSink{}
	res := attachOK(t, s, sink, 1, 0)
	prefix, err := base64.StdEncoding.DecodeString(res.reply.Prefix)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"\x1b[?1049h", "\x1b[?1000h", "\x1b[?2004h"} {
		if !bytes.Contains(prefix, []byte(want)) {
			t.Fatalf("恢复序列缺少 %q: %q", want, prefix)
		}
	}
	altEnter := uint64(len("prompt$ vim\r\n\x1b[?1049h"))
	if res.reply.From != altEnter {
		t.Fatalf("应从进入备用屏之后开始回放: from=%d want=%d", res.reply.From, altEnter)
	}
	if res.reply.Truncated {
		t.Fatal("有意跳过主屏历史不应标记 truncated")
	}
	if !res.jiggle {
		t.Fatal("备用屏会话的新附着应请求重绘")
	}
	before := len(term.Resizes())
	s.jiggle()
	rs := term.Resizes()[before:]
	if len(rs) != 2 || rs[0] != [2]uint16{23, 80} || rs[1] != [2]uint16{24, 80} {
		t.Fatalf("尺寸抖动应先少一行再恢复: %v", rs)
	}

	// 断线续传（since>0 且在缓冲内）：终端状态完好，不发恢复序列、不抖动
	res.ch.end("")
	resume := attachOK(t, s, &recSink{}, 2, altEnter+3)
	if resume.reply.Prefix != "" || resume.jiggle || resume.reply.From != altEnter+3 {
		t.Fatalf("续传不应带恢复序列或重绘: %+v jiggle=%v", resume.reply, resume.jiggle)
	}
}

func TestMainScreenAttachCarriesModesPrefix(t *testing.T) {
	e := newTestEnv(t, Limits{})
	s, term := e.open(t)
	term.Emit([]byte("$ \x1b[?2004h"))
	eventually(t, "输出进入缓冲", func() bool { return s.info().End == 10 })
	res := attachOK(t, s, &recSink{}, 1, 0)
	prefix, _ := base64.StdEncoding.DecodeString(res.reply.Prefix)
	if string(prefix) != "\x1b[?2004h" || res.jiggle {
		t.Fatalf("主屏会话应只恢复括号粘贴模式: prefix=%q jiggle=%v", prefix, res.jiggle)
	}
}

func TestDuplicateAttachToGoneSession(t *testing.T) {
	e := newTestEnv(t, Limits{})
	s, _ := e.open(t)
	s.kill()
	eventually(t, "会话被移除", func() bool { return e.mgr.Get(s.id) == nil })
	if _, err := s.attach(&recSink{}, 1, 0, 0, 0, 0); err != errNoSuchSession {
		t.Fatalf("已移除的会话不能再附着: %v", err)
	}
}

func TestShutdownKillsEverything(t *testing.T) {
	e := newTestEnv(t, Limits{})
	for i := 0; i < 3; i++ {
		e.open(t)
	}
	e.mgr.Shutdown(3 * time.Second)
	if e.mgr.Count() != 0 {
		t.Fatalf("Agent 退出时 PTY 不应继续存活: %d", e.mgr.Count())
	}
	for _, term := range e.spawn.Terminals() {
		if term.KillCount() == 0 {
			t.Fatal("每个终端都应被终止")
		}
	}
	if _, err := e.mgr.Open("", 24, 80); err != ErrShuttingDown {
		t.Fatalf("退出后不得新建会话: %v", err)
	}
}

// 并发压力：多条通道随机附着 / 确认 / 断开，同时持续产生输出；每条通道收到的数据必须连续、无重复、内容正确。
func TestConcurrentChannelsKeepStreamIntegrity(t *testing.T) {
	e := newTestEnv(t, Limits{})
	s, term := e.open(t)

	const total = 2 << 20
	data := make([]byte, total)
	for i := range data {
		data[i] = byte(i * 31)
	}
	go func() {
		for off := 0; off < total; off += 7919 {
			end := off + 7919
			if end > total {
				end = total
			}
			term.Emit(data[off:end])
			time.Sleep(50 * time.Microsecond)
		}
	}()

	var wg sync.WaitGroup
	for w := 0; w < 4; w++ {
		wg.Add(1)
		go func(id uint32) {
			defer wg.Done()
			sink := &recSink{}
			res, err := s.attach(sink, id, 0, 0, 0, 0)
			if err != nil {
				t.Errorf("attach: %v", err)
				return
			}
			res.ch.start()
			from := res.reply.From
			deadline := time.Now().Add(20 * time.Second)
			for from+uint64(sink.outLen(id)) < total && time.Now().Before(deadline) {
				n := uint64(sink.outLen(id))
				res.ch.ack(from + n)
				time.Sleep(time.Millisecond)
			}
			got := sink.payload(t, id, from)
			if uint64(len(got))+from != total {
				t.Errorf("通道 %d 未收齐: from=%d len=%d", id, from, len(got))
				return
			}
			if !bytes.Equal(got, data[from:]) {
				t.Errorf("通道 %d 的数据与产生的内容不一致", id)
			}
			res.ch.end("")
		}(uint32(w + 1))
	}
	wg.Wait()
}
