package record

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

func testSID(n int) string { return fmt.Sprintf("%032x", n) }

// 后台清理只能整理已结束的会话。「没结束又不在 live 里」的目录要么是 Begin 刚建出、写协程正在追加
// 分段的新会话（此时压缩并删掉原文件会丢数据），要么是等着 Recover 收尾的崩溃遗留（Recover 要改写
// meta.json，临时文件被误删会让收尾失败）。
func TestGCLeavesUnendedSessionsAlone(t *testing.T) {
	st := newTestStore(t, func(o *Options) { o.NoCompress = false })
	sid := testSID(91)
	dir := st.SessionDir(sid)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := writeMeta(dir, Meta{SID: sid}); err != nil { // 未结束
		t.Fatal(err)
	}
	rec := filepath.Join(dir, segName(1))
	metaTmp := filepath.Join(dir, metaFile+".tmp")
	for _, p := range []string{rec, metaTmp} {
		if err := os.WriteFile(p, bytes.Repeat([]byte("x"), 512), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	st.gcOnce()
	time.Sleep(300 * time.Millisecond) // 留足时间让（本不该发生的）压缩任务跑完
	for _, p := range []string{rec, metaTmp} {
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("未结束的会话被后台清理动了: %v", err)
		}
	}
	if _, err := os.Stat(rec + ".gz"); err == nil {
		t.Fatal("未结束会话的原始分段被压缩了")
	}

	// 结束之后同样的目录应当被整理：原始分段压缩掉、原文件删除。
	if err := writeMeta(dir, Meta{SID: sid, EndedMS: time.Now().UnixMilli(), EndedReason: "exit"}); err != nil {
		t.Fatal(err)
	}
	st.gcOnce()
	deadline := time.Now().Add(5 * time.Second)
	for {
		_, gzErr := os.Stat(rec + ".gz")
		_, recErr := os.Stat(rec)
		if gzErr == nil && os.IsNotExist(recErr) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("已结束的会话没有被整理: gz err=%v, rec err=%v", gzErr, recErr)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func newTestStore(t *testing.T, mut func(*Options)) *Store {
	t.Helper()
	o := Options{
		Dir:        t.TempDir(),
		MinFree:    -1,
		FlushEvery: 20 * time.Millisecond,
		NoCompress: true,
		GCInterval: time.Hour,
	}
	if mut != nil {
		mut(&o)
	}
	s, err := Open(o)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(s.Close)
	return s
}

// readAll 从头读完一个会话的记录，返回全部事件与输出字节的拼接。
func readAll(t *testing.T, s *Store, sid string) ([]Event, []byte) {
	t.Helper()
	var evs []Event
	var out []byte
	var seg, off uint32
	for i := 0; i < 100000; i++ {
		pg, err := s.Read(sid, seg, off, MaxPageBytes)
		if err != nil {
			t.Fatalf("Read(%d,%d): %v", seg, off, err)
		}
		es, used, err := DecodeEvents(pg.Data)
		if err != nil || used != len(pg.Data) {
			t.Fatalf("decode page: used=%d len=%d err=%v", used, len(pg.Data), err)
		}
		for _, e := range es {
			if e.Kind == KindOutput {
				out = append(out, e.Payload...)
			}
		}
		evs = append(evs, es...)
		if pg.EOF {
			return evs, out
		}
		if pg.NextSeg == seg && pg.NextOff == off && len(pg.Data) == 0 {
			t.Fatalf("no progress at (%d,%d)", seg, off)
		}
		seg, off = pg.NextSeg, pg.NextOff
	}
	t.Fatal("too many pages")
	return nil, nil
}

func marksOf(evs []Event) []Mark {
	var out []Mark
	for _, e := range evs {
		if e.Kind == KindMark {
			if m, ok := DecodeMark(e.Payload); ok {
				out = append(out, m)
			}
		}
	}
	return out
}

func TestEventRoundTrip(t *testing.T) {
	var buf []byte
	buf = AppendEvent(buf, KindOutput, 0, []byte("hello"))
	buf = AppendEvent(buf, KindResize, 1500, encodeResize(30, 100))
	buf = AppendEvent(buf, KindMark, 7, encodeMark(Mark{T: MarkGap, Reason: GapDiskLow, Bytes: 9}))

	evs, used, err := DecodeEvents(buf)
	if err != nil || used != len(buf) || len(evs) != 3 {
		t.Fatalf("decode: n=%d used=%d/%d err=%v", len(evs), used, len(buf), err)
	}
	if evs[0].Kind != KindOutput || string(evs[0].Payload) != "hello" || evs[0].DT != 0 {
		t.Fatalf("event 0: %+v", evs[0])
	}
	if r, c, ok := DecodeResize(evs[1].Payload); !ok || r != 30 || c != 100 || evs[1].DT != 1500 {
		t.Fatalf("event 1: %+v", evs[1])
	}
	if m, ok := DecodeMark(evs[2].Payload); !ok || m.T != MarkGap || m.Bytes != 9 {
		t.Fatalf("event 2: %+v", evs[2])
	}

	// 截断的尾部事件不是错误：只返回前面完整的部分。
	for cut := 1; cut < len(buf); cut++ {
		evs, used, err := DecodeEvents(buf[:cut])
		if err != nil {
			t.Fatalf("cut %d: %v", cut, err)
		}
		if used > cut || len(evs) > 3 {
			t.Fatalf("cut %d: used=%d n=%d", cut, used, len(evs))
		}
	}
	// 声明的长度超过上限属于损坏数据。
	bad := AppendEvent(nil, KindOutput, 0, make([]byte, 10))
	bad[2] = 0xff
	bad[3] = 0xff
	bad[4] = 0x7f
	if _, _, err := DecodeEvent(bad); err == nil || errors.Is(err, errShortEvent) {
		t.Fatalf("oversized length should be corruption, got %v", err)
	}
}

func TestWriterBasicRecordsDisplayedOutput(t *testing.T) {
	s := newTestStore(t, nil)
	sid := testSID(1)
	w, state := s.Begin(Meta{SID: sid, Name: "demo", Shell: "bash", Rows: 24, Cols: 80})
	if w == nil || state != StateOn {
		t.Fatalf("Begin: %v %q", w, state)
	}
	w.Output([]byte("$ ls\r\n"))
	w.Resize(40, 120)
	w.Output([]byte("file-a  file-b\r\n"))
	code := 3
	w.End(&code, "", "exit")

	evs, out := readAll(t, s, sid)
	if string(out) != "$ ls\r\nfile-a  file-b\r\n" {
		t.Fatalf("output = %q", out)
	}
	var sawResize bool
	for _, e := range evs {
		if e.Kind == KindResize {
			if r, c, _ := DecodeResize(e.Payload); r == 40 && c == 120 {
				sawResize = true
			}
		}
	}
	if !sawResize {
		t.Fatal("resize event missing")
	}
	marks := marksOf(evs)
	if len(marks) < 2 || marks[0].T != MarkSeg || marks[0].Rows != 24 || marks[0].Cols != 80 || marks[0].Seq != 1 {
		t.Fatalf("marks = %+v", marks)
	}
	last := marks[len(marks)-1]
	if last.T != MarkEnd || last.Code == nil || *last.Code != 3 || last.Reason != "exit" {
		t.Fatalf("end mark = %+v", last)
	}

	info, err := s.Info(sid)
	if err != nil {
		t.Fatal(err)
	}
	if info.Name != "demo" || info.Shell != "bash" || info.EndedMS == 0 || info.EndedReason != "exit" ||
		info.ExitCode == nil || *info.ExitCode != 3 || info.Gap || info.Head || len(info.Segments) != 1 {
		t.Fatalf("info = %+v", info)
	}
	if info.RawBytes <= 0 || info.FirstMS == 0 || info.LastMS < info.FirstMS {
		t.Fatalf("info stats = %+v", info)
	}
}

func TestPermissionBits(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX 权限位在 Windows 上不适用（由 ACL 加固）")
	}
	s := newTestStore(t, nil)
	sid := testSID(2)
	w, _ := s.Begin(Meta{SID: sid})
	w.Output([]byte("secret-looking output"))
	w.End(nil, "", "exit")

	check := func(path string, want os.FileMode) {
		t.Helper()
		fi, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if got := fi.Mode().Perm(); got != want {
			t.Fatalf("%s mode = %o, want %o", path, got, want)
		}
	}
	check(s.o.Dir, 0o700)
	check(s.root, 0o700)
	check(s.SessionDir(sid), 0o700)
	ents, _ := os.ReadDir(s.SessionDir(sid))
	if len(ents) == 0 {
		t.Fatal("no files written")
	}
	for _, e := range ents {
		check(filepath.Join(s.SessionDir(sid), e.Name()), 0o600)
	}
}

func TestSegmentsRollAndCompress(t *testing.T) {
	s := newTestStore(t, func(o *Options) {
		o.SegmentBytes = 8 << 10
		o.NoCompress = false
	})
	sid := testSID(3)
	w, _ := s.Begin(Meta{SID: sid, Rows: 24, Cols: 80})
	var want bytes.Buffer
	for i := 0; i < 400; i++ {
		line := fmt.Sprintf("line %04d %s\r\n", i, strings.Repeat("x", 90))
		want.WriteString(line)
		w.Output([]byte(line))
	}
	w.End(nil, "", "exit")

	deadline := time.Now().Add(10 * time.Second)
	for {
		ents, _ := os.ReadDir(s.SessionDir(sid))
		raw := 0
		for _, e := range ents {
			if strings.HasSuffix(e.Name(), ".rec") {
				raw++
			}
		}
		if raw == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("%d raw segments never compressed", raw)
		}
		time.Sleep(20 * time.Millisecond)
	}

	info, err := s.Info(sid)
	if err != nil {
		t.Fatal(err)
	}
	if len(info.Segments) < 4 {
		t.Fatalf("segments = %d", len(info.Segments))
	}
	for _, sg := range info.Segments {
		if !sg.Gz || sg.Raw <= 0 || sg.Disk <= 0 || sg.Disk >= sg.Raw {
			t.Fatalf("segment %+v not compressed", sg)
		}
		if sg.Rows != 24 || sg.Cols != 80 || sg.T0 == 0 {
			t.Fatalf("segment meta %+v", sg)
		}
	}
	if info.DiskBytes >= info.RawBytes {
		t.Fatalf("disk %d >= raw %d", info.DiskBytes, info.RawBytes)
	}
	_, out := readAll(t, s, sid)
	if !bytes.Equal(out, want.Bytes()) {
		t.Fatalf("content mismatch after compression: got %d bytes want %d", len(out), want.Len())
	}
}

func TestSessionQuotaRollsAndMarksGap(t *testing.T) {
	s := newTestStore(t, func(o *Options) {
		o.SegmentBytes = 8 << 10
		o.SessionBytes = 40 << 10
	})
	sid := testSID(4)
	w, _ := s.Begin(Meta{SID: sid})
	var all bytes.Buffer
	for i := 0; i < 600; i++ {
		line := fmt.Sprintf("row %05d %s\r\n", i, strings.Repeat("y", 100))
		all.WriteString(line)
		w.Output([]byte(line))
	}
	w.End(nil, "", "exit")

	info, err := s.Info(sid)
	if err != nil {
		t.Fatal(err)
	}
	if !info.Gap || !info.Head || info.DroppedBytes <= 0 {
		t.Fatalf("expected truncated history: %+v", info)
	}
	if info.RawBytes > 40<<10 {
		t.Fatalf("retained %d bytes exceeds quota", info.RawBytes)
	}
	if info.Segments[0].Seq <= 1 {
		t.Fatalf("oldest segment should have been dropped: %+v", info.Segments)
	}
	for _, sg := range info.Segments {
		if _, err := os.Stat(filepath.Join(s.SessionDir(sid), segName(sg.Seq))); err != nil {
			t.Fatalf("listed segment %d missing: %v", sg.Seq, err)
		}
	}
	evs, out := readAll(t, s, sid)
	if len(out) == 0 || !bytes.HasSuffix(all.Bytes(), out) {
		t.Fatalf("retained output must be a suffix of the whole output (got %d bytes)", len(out))
	}
	if marks := marksOf(evs); marks[0].T != MarkSeg || marks[0].Seq != info.Segments[0].Seq {
		t.Fatalf("first mark %+v", marks[0])
	}
	// 读取一个早已被清理的起点：从最早保留的分段开始，并标记 Skipped。
	pg, err := s.Read(sid, 1, 0, MaxPageBytes)
	if err != nil || !pg.Skipped || pg.Seg != info.Segments[0].Seq {
		t.Fatalf("skipped read: %+v err=%v", pg, err)
	}
}

func TestJournalCompactionKeepsInfoCorrect(t *testing.T) {
	s := newTestStore(t, func(o *Options) {
		o.SegmentBytes = minSegmentBytes
		o.SessionBytes = 3 * minSegmentBytes
	})
	sid := testSID(5)
	w, _ := s.Begin(Meta{SID: sid})
	chunk := bytes.Repeat([]byte("z"), 3000)
	for i := 0; i < 4000; i++ {
		w.Output(chunk)
		if i%50 == 0 {
			w.Flush()
		}
	}
	w.End(nil, "", "exit")
	fi, err := os.Stat(filepath.Join(s.SessionDir(sid), journalFile))
	if err != nil {
		t.Fatal(err)
	}
	if fi.Size() > 2*journalCompactAt {
		t.Fatalf("journal grew to %d bytes", fi.Size())
	}
	info, err := s.Info(sid)
	if err != nil {
		t.Fatal(err)
	}
	if !info.Gap || info.DroppedBytes <= 0 || !info.Head {
		t.Fatalf("info after compaction: %+v", info)
	}
	if info.RawBytes > 3*minSegmentBytes {
		t.Fatalf("raw %d exceeds quota", info.RawBytes)
	}
}

// fakeClock 是并发安全的假时钟：Open 之后后台清理协程马上跑第一轮 gcOnce、写协程也会读 Now，
// 测试里推进时间必须加锁——裸指针写会在 -race 下偶发数据竞争（2026-10-20 高负载下复现过
// TestRetentionRemovesExpiredSessions 的 race 报告）。
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func newFakeClock() *fakeClock { return &fakeClock{t: time.Now()} }

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

func TestHostQuotaRemovesOldestEndedFirst(t *testing.T) {
	clock := newFakeClock()
	s := newTestStore(t, func(o *Options) {
		o.SegmentBytes = minSegmentBytes
		o.HostBytes = 50 << 10
		o.Now = clock.Now
	})
	write := func(n int, kb int) string {
		sid := testSID(n)
		w, _ := s.Begin(Meta{SID: sid})
		w.Output(bytes.Repeat([]byte("h"), kb<<10))
		return sid
	}
	a := write(10, 20)
	s.liveWriter(a).End(nil, "", "exit")
	clock.Advance(time.Minute)
	b := write(11, 20)
	s.liveWriter(b).End(nil, "", "exit")
	clock.Advance(time.Minute)
	c := write(12, 20) // 仍在录制
	s.liveWriter(c).Flush()

	s.enforceHost()
	if _, err := s.Info(a); !errors.Is(err, ErrNotFound) {
		t.Fatalf("oldest ended session should be removed, err=%v", err)
	}
	if _, err := s.Info(b); err != nil {
		t.Fatalf("newer ended session must survive: %v", err)
	}
	if _, err := s.Info(c); err != nil {
		t.Fatalf("live session must survive: %v", err)
	}
	s.liveWriter(c).End(nil, "", "exit")
}

func TestRetentionRemovesExpiredSessions(t *testing.T) {
	clock := newFakeClock()
	s := newTestStore(t, func(o *Options) {
		o.Retention = 24 * time.Hour
		o.Now = clock.Now
	})
	old, fresh := testSID(20), testSID(21)
	w, _ := s.Begin(Meta{SID: old})
	w.Output([]byte("old"))
	w.End(nil, "", "exit")
	clock.Advance(20 * time.Hour)
	w, _ = s.Begin(Meta{SID: fresh})
	w.Output([]byte("fresh"))
	w.End(nil, "", "exit")

	clock.Advance(5 * time.Hour) // old 已结束 25 小时，fresh 5 小时
	s.gcOnce()
	if _, err := s.Info(old); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expired session should be removed: %v", err)
	}
	if _, err := s.Info(fresh); err != nil {
		t.Fatalf("fresh session must stay: %v", err)
	}

	// 服务端策略只能缩短保留期。
	s.SetPolicy(Policy{RetentionDays: 1})
	if got := s.retention(); got != 24*time.Hour {
		t.Fatalf("retention = %v", got)
	}
	s.SetPolicy(Policy{RetentionDays: 365})
	if got := s.retention(); got != 24*time.Hour {
		t.Fatalf("policy must not loosen retention: %v", got)
	}
}

func TestLowDiskPausesRecordingAndLeavesGap(t *testing.T) {
	var mu sync.Mutex
	free := uint64(10 << 30)
	s := newTestStore(t, func(o *Options) {
		o.MinFree = 1 << 30
		o.FreeSpace = func(string) (uint64, error) {
			mu.Lock()
			defer mu.Unlock()
			return free, nil
		}
	})
	setFree := func(v uint64) { mu.Lock(); free = v; mu.Unlock() }

	sid := testSID(30)
	w, _ := s.Begin(Meta{SID: sid})
	w.Output([]byte("before\r\n"))
	w.Flush()

	setFree(100 << 20)
	waitFor(t, func() bool { return w.Stats().State == StatePaused }, "pause on low disk")
	if st := w.Stats(); st.Reason != GapDiskLow {
		t.Fatalf("stats = %+v", st)
	}
	w.Output([]byte("lost-while-paused\r\n"))
	w.Flush()

	setFree(10 << 30)
	waitFor(t, func() bool { return w.Stats().State == StateOn }, "resume after disk recovers")
	w.Output([]byte("after\r\n"))
	w.End(nil, "", "exit")

	evs, out := readAll(t, s, sid)
	if string(out) != "before\r\nafter\r\n" {
		t.Fatalf("output = %q", out)
	}
	var gap *Mark
	for _, m := range marksOf(evs) {
		if m.T == MarkGap {
			m := m
			gap = &m
		}
	}
	if gap == nil || gap.Reason != GapDiskLow || gap.Bytes != int64(len("lost-while-paused\r\n")) {
		t.Fatalf("gap mark = %+v", gap)
	}
	if info, _ := s.Info(sid); !info.Gap {
		t.Fatalf("info should report gap: %+v", info)
	}
}

func TestPolicyOffPausesAndResumes(t *testing.T) {
	s := newTestStore(t, nil)
	sid := testSID(31)
	w, _ := s.Begin(Meta{SID: sid})
	w.Output([]byte("one\r\n"))
	w.Flush()

	s.SetPolicy(Policy{Off: true})
	waitFor(t, func() bool { return w.Stats().State == StatePaused && w.Stats().Reason == GapPolicyOff }, "policy pause")
	w.Output([]byte("hidden\r\n"))
	w.Flush()

	// 策略关闭期间新建的会话根本不录制。
	if nw, state := s.Begin(Meta{SID: testSID(32)}); nw != nil || state != StateOff {
		t.Fatalf("Begin under policy off: %v %q", nw, state)
	}
	if _, err := os.Stat(s.SessionDir(testSID(32))); !os.IsNotExist(err) {
		t.Fatalf("no directory expected for unrecorded session: %v", err)
	}

	s.SetPolicy(Policy{})
	waitFor(t, func() bool { return w.Stats().State == StateOn }, "policy resume")
	w.Output([]byte("two\r\n"))
	w.End(nil, "", "exit")

	evs, out := readAll(t, s, sid)
	if string(out) != "one\r\ntwo\r\n" {
		t.Fatalf("output = %q", out)
	}
	found := false
	for _, m := range marksOf(evs) {
		found = found || (m.T == MarkGap && m.Reason == GapPolicyOff)
	}
	if !found {
		t.Fatal("gap(policy_off) mark missing")
	}
}

func TestVetoIncognitoLeaveNoTrace(t *testing.T) {
	vetoed := newTestStore(t, func(o *Options) { o.Disabled = true })
	if w, state := vetoed.Begin(Meta{SID: testSID(40)}); w != nil || state != StateVeto {
		t.Fatalf("veto: %v %q", w, state)
	}
	s := newTestStore(t, nil)
	if w, state := s.Begin(Meta{SID: testSID(41), Name: "private", Incognito: true}); w != nil || state != StateIncognito {
		t.Fatalf("incognito: %v %q", w, state)
	}
	for _, st := range []*Store{vetoed, s} {
		ents, _ := os.ReadDir(st.root)
		if len(ents) != 0 {
			t.Fatalf("expected no session dirs, got %d", len(ents))
		}
	}
	// nil Writer 上的调用都必须是安全的无操作。
	var nw *Writer
	nw.Output([]byte("x"))
	nw.Resize(1, 1)
	nw.Flush()
	nw.SetName("n")
	nw.End(nil, "", "exit")
	if nw.Stats().State != "" {
		t.Fatal("nil writer stats should be empty")
	}
}

func TestBacklogOverflowNeverBlocksAndMarksGap(t *testing.T) {
	gate := make(chan struct{})
	var once sync.Once
	s := newTestStore(t, func(o *Options) {
		o.MinFree = 1
		o.QueueBytes = 64 << 10
		o.FreeSpace = func(string) (uint64, error) {
			once.Do(func() { <-gate }) // 第一次开分段时卡住写协程，模拟极慢的磁盘
			return 1 << 40, nil
		}
	})
	sid := testSID(50)
	w, _ := s.Begin(Meta{SID: sid})
	chunk := bytes.Repeat([]byte("b"), 8<<10)
	start := time.Now()
	for i := 0; i < 200; i++ {
		w.Output(chunk) // 1.6 MiB 远超队列上限，但调用方不能被拖慢
	}
	if d := time.Since(start); d > time.Second {
		t.Fatalf("producer was blocked for %v", d)
	}
	close(gate)
	waitFor(t, func() bool { return w.queued.Load() == 0 }, "queue drained")
	w.Output([]byte("tail\r\n"))
	w.End(nil, "", "exit")

	evs, out := readAll(t, s, sid)
	if !bytes.HasSuffix(out, []byte("tail\r\n")) {
		t.Fatalf("tail missing: %d bytes", len(out))
	}
	var gap *Mark
	for _, m := range marksOf(evs) {
		if m.T == MarkGap && m.Reason == GapBacklog {
			m := m
			gap = &m
		}
	}
	if gap == nil || gap.Bytes <= 0 {
		t.Fatalf("backlog gap mark missing: %+v", gap)
	}
	if st := w.Stats(); st.Dropped <= 0 || !st.Gap {
		t.Fatalf("stats = %+v", st)
	}
}

func TestWriteFailureNeverBlocksAndRecovers(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("需要非特权 POSIX 用户才能用目录权限制造写失败")
	}
	s := newTestStore(t, func(o *Options) { o.SegmentBytes = minSegmentBytes })
	sid := testSID(60)
	w, _ := s.Begin(Meta{SID: sid})
	w.Output([]byte("first\r\n"))
	w.Flush()

	dir := s.SessionDir(sid)
	if err := os.Chmod(dir, 0o500); err != nil { // 只读目录：无法创建新分段
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

	start := time.Now()
	chunk := bytes.Repeat([]byte("e"), 2<<10)
	for i := 0; i < 20; i++ { // 足以让当前分段滚动并尝试创建下一个
		w.Output(chunk)
	}
	w.Flush()
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("write failure leaked into the producer path (%v)", d)
	}
	waitFor(t, func() bool { return w.Stats().State == StateError }, "error state after write failure")

	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool {
		w.Output([]byte("."))
		return w.Stats().State == StateOn
	}, "recovery after permissions are restored")
	w.Output([]byte("recovered\r\n"))
	w.End(nil, "", "exit")

	evs, out := readAll(t, s, sid)
	if !bytes.HasPrefix(out, []byte("first\r\n")) || !bytes.HasSuffix(out, []byte("recovered\r\n")) {
		t.Fatalf("unexpected output (%d bytes)", len(out))
	}
	found := false
	for _, m := range marksOf(evs) {
		found = found || (m.T == MarkGap && m.Reason == GapWriteError)
	}
	if !found {
		t.Fatal("gap(write_error) mark missing")
	}
}

func TestReadPagesAcrossSegmentsAndLiveTail(t *testing.T) {
	s := newTestStore(t, func(o *Options) { o.SegmentBytes = 8 << 10 })
	sid := testSID(70)
	w, _ := s.Begin(Meta{SID: sid})
	var want bytes.Buffer
	for i := 0; i < 200; i++ {
		line := fmt.Sprintf("p%04d %s\r\n", i, strings.Repeat("q", 200))
		want.WriteString(line)
		w.Output([]byte(line))
	}
	// 会话仍在运行：读取应当包含已入队的全部内容，且游标可续读。
	_, out := readAll(t, s, sid)
	if !bytes.Equal(out, want.Bytes()) {
		t.Fatalf("live read mismatch: %d vs %d", len(out), want.Len())
	}
	pg, err := s.Read(sid, 0, 0, MaxPageBytes)
	if err != nil {
		t.Fatal(err)
	}
	if pg.EOF || len(pg.Data) == 0 || len(pg.Data) > MaxPageBytes {
		t.Fatalf("first page: eof=%v len=%d", pg.EOF, len(pg.Data))
	}

	// 续读：先读到末尾，之后追加的内容通过同一个游标取回。
	var seg, off uint32
	for {
		p, err := s.Read(sid, seg, off, MaxPageBytes)
		if err != nil {
			t.Fatal(err)
		}
		seg, off = p.NextSeg, p.NextOff
		if p.EOF {
			break
		}
	}
	w.Output([]byte("LATE\r\n"))
	p, err := s.Read(sid, seg, off, MaxPageBytes)
	if err != nil {
		t.Fatal(err)
	}
	es, _, _ := DecodeEvents(p.Data)
	if len(es) != 1 || string(es[0].Payload) != "LATE\r\n" {
		t.Fatalf("tail read = %+v", es)
	}
	w.End(nil, "", "exit")
}

func TestInvalidSIDIsRejected(t *testing.T) {
	s := newTestStore(t, nil)
	for _, sid := range []string{"", "../etc", "..", testSID(1)[:31], strings.ToUpper(testSID(255)), testSID(1) + "/x"} {
		if _, err := s.Info(sid); !errors.Is(err, ErrNotFound) {
			t.Fatalf("Info(%q) err = %v", sid, err)
		}
		if _, err := s.Read(sid, 0, 0, 100); !errors.Is(err, ErrNotFound) {
			t.Fatalf("Read(%q) err = %v", sid, err)
		}
		if err := s.Delete(sid); !errors.Is(err, ErrNotFound) {
			t.Fatalf("Delete(%q) err = %v", sid, err)
		}
	}
}

func TestRecoverMarksUnfinishedSessionsEnded(t *testing.T) {
	src := newTestStore(t, nil)
	sid := testSID(80)
	w, _ := src.Begin(Meta{SID: sid, Name: "crashed"})
	w.Output([]byte("last words\r\n"))
	w.Flush()

	// 模拟进程被杀：把磁盘状态原样拷到新目录，由一个全新的 Store 接手。
	dst := t.TempDir()
	copyDir(t, src.o.Dir, dst)
	s2, err := Open(Options{Dir: dst, MinFree: -1, NoCompress: true, RecoverReason: "host_reboot", GCInterval: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	info, err := s2.Info(sid)
	if err != nil {
		t.Fatal(err)
	}
	if info.EndedMS == 0 || info.EndedReason != "host_reboot" || info.Name != "crashed" {
		t.Fatalf("recovered info = %+v", info)
	}
	_, out := readAll(t, s2, sid)
	if string(out) != "last words\r\n" {
		t.Fatalf("recovered output = %q", out)
	}
	w.End(nil, "", "exit")
}

func TestRecoverUsesPerSessionReason(t *testing.T) {
	src := newTestStore(t, nil)
	a, b := testSID(81), testSID(82)
	for _, sid := range []string{a, b} {
		w, _ := src.Begin(Meta{SID: sid})
		w.Output([]byte("x"))
		w.Flush()
	}
	dst := t.TempDir()
	copyDir(t, src.o.Dir, dst)
	s2, err := Open(Options{Dir: dst, MinFree: -1, NoCompress: true, DeferRecover: true, GCInterval: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	if info, _ := s2.Info(a); info == nil || info.EndedMS != 0 {
		t.Fatalf("DeferRecover must not touch sessions yet: %+v", info)
	}
	s2.Recover(func(sid string) string {
		if sid == a {
			return "host_reboot"
		}
		return ""
	})
	if info, _ := s2.Info(a); info.EndedReason != "host_reboot" {
		t.Fatalf("a = %+v", info)
	}
	if info, _ := s2.Info(b); info.EndedReason != "holder_exit" {
		t.Fatalf("b = %+v", info)
	}
	for _, sid := range []string{a, b} {
		src.liveWriter(sid).End(nil, "", "exit")
	}
}

func TestDeleteRefusesLiveSessions(t *testing.T) {
	s := newTestStore(t, nil)
	sid := testSID(90)
	w, _ := s.Begin(Meta{SID: sid})
	w.Output([]byte("x"))
	if err := s.Delete(sid); err == nil {
		t.Fatal("deleting a live session must fail")
	}
	w.End(nil, "", "exit")
	if err := s.Delete(sid); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Info(sid); !errors.Is(err, ErrNotFound) {
		t.Fatalf("after delete: %v", err)
	}
}

// End 返回之后，会话必须已经不在「正在录制」表里：调用方（Delete、主机配额清理）紧接着就会看这张表。
// 写协程曾在通知 End 的调用方之后才把自己摘掉，窗口只有几微秒，高负载下 TestDeleteRefusesLiveSessions
// 偶发失败；这里循环很多轮放大窗口，改回旧顺序时会失败。
func TestEndReturnsAfterSessionLeavesLiveSet(t *testing.T) {
	s := newTestStore(t, nil)
	for i := 0; i < 400; i++ {
		sid := testSID(1000 + i)
		w, st := s.Begin(Meta{SID: sid})
		if st != StateOn {
			t.Fatalf("第 %d 轮：Begin 状态 = %q", i, st)
		}
		w.Output([]byte("x"))
		w.End(nil, "", "exit")
		if s.isLive(sid) {
			t.Fatalf("第 %d 轮：End 返回后会话仍在「正在录制」表里", i)
		}
		if err := s.Delete(sid); err != nil {
			t.Fatalf("第 %d 轮：End 之后 Delete 失败: %v", i, err)
		}
	}
}

func TestCloseFinishesLiveSessions(t *testing.T) {
	s, err := Open(Options{Dir: t.TempDir(), MinFree: -1, NoCompress: true, GCInterval: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	sid := testSID(91)
	w, _ := s.Begin(Meta{SID: sid})
	w.Output([]byte("bye\r\n"))
	s.Close()
	info, err := readInfo(s.SessionDir(sid), sid)
	if err != nil {
		t.Fatal(err)
	}
	if info.EndedMS == 0 || info.EndedReason != "holder_stopped" {
		t.Fatalf("info = %+v", info)
	}
	if st := w.Stats(); st.Raw == 0 {
		t.Fatalf("stats = %+v", st)
	}
}

func TestConcurrentProducersKeepEventsIntact(t *testing.T) {
	s := newTestStore(t, func(o *Options) { o.SegmentBytes = 16 << 10 })
	sid := testSID(95)
	w, _ := s.Begin(Meta{SID: sid})
	var wg sync.WaitGroup
	for g := 0; g < 4; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < 200; i++ {
				w.Output([]byte(fmt.Sprintf("[g%d-%03d]", g, i)))
				if i%40 == 0 {
					w.Resize(uint16(20+g), uint16(80+i%40))
				}
			}
		}(g)
	}
	wg.Wait()
	w.End(nil, "", "exit")
	_, out := readAll(t, s, sid)
	for g := 0; g < 4; g++ {
		for i := 0; i < 200; i++ {
			if !bytes.Contains(out, []byte(fmt.Sprintf("[g%d-%03d]", g, i))) {
				t.Fatalf("missing chunk g%d-%03d", g, i)
			}
		}
	}
}

func waitFor(t *testing.T, cond func() bool, what string) {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timeout waiting for: %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func copyDir(t *testing.T, src, dst string) {
	t.Helper()
	err := filepath.Walk(src, func(p string, fi os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		target := filepath.Join(dst, rel)
		if fi.IsDir() {
			return os.MkdirAll(target, 0o700)
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(target, b, 0o600)
	})
	if err != nil {
		t.Fatal(err)
	}
}
