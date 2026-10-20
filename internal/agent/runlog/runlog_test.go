package runlog

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/teacat99/RelayMesh/v2/internal/termproto"
)

func newID(n int) string { return fmt.Sprintf("%032x", n) }

func openStore(t *testing.T, o Options) *Store {
	t.Helper()
	if o.Dir == "" {
		o.Dir = filepath.Join(t.TempDir(), "runs")
	}
	s, err := Open(o)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func begin(t *testing.T, s *Store, n int) *Writer {
	t.Helper()
	w, err := s.Begin(Meta{ID: newID(n), Cmd: "echo hi", Started: time.Now().UnixMilli()})
	if err != nil {
		t.Fatal(err)
	}
	// 没跑完的写入器还握着文件句柄：Windows 上它会让临时目录删不掉，所以测试结束时一并放掉。
	t.Cleanup(w.abort)
	return w
}

func readAll(t *testing.T, s *Store, id string, stream byte, page int) (data []byte, gaps int, last Page) {
	t.Helper()
	var off uint64
	for i := 0; i < 10000; i++ {
		pg, err := s.Read(id, stream, off, page)
		if err != nil {
			t.Fatal(err)
		}
		if pg.Gap {
			gaps++
		}
		data = append(data, pg.Data...)
		last = pg
		if pg.EOF || (pg.Running && pg.Next >= pg.Total) {
			return data, gaps, last
		}
		if pg.Next == off && len(pg.Data) == 0 {
			t.Fatalf("读取没有进展: %+v", pg)
		}
		off = pg.Next
	}
	t.Fatal("读取不收敛")
	return nil, 0, last
}

func TestSmallOutputRoundTripAndInline(t *testing.T) {
	s := openStore(t, Options{})
	w := begin(t, s, 1)
	if off := w.Write(termproto.StreamStdout, []byte("hello ")); off != 0 {
		t.Fatalf("首段偏移 = %d", off)
	}
	if off := w.Write(termproto.StreamStdout, []byte("world\n")); off != 6 {
		t.Fatalf("次段偏移 = %d", off)
	}
	w.Write(termproto.StreamStderr, []byte("oops"))
	code := 0
	res := w.Finish(Finish{State: termproto.RunExited, Code: &code, Ended: time.Now()})

	if string(res.Out) != "hello world\n" || string(res.Err) != "oops" {
		t.Fatalf("内联输出不对: %q %q", res.Out, res.Err)
	}
	if string(res.PrevHead) != "hello world\noops" || len(res.PrevTail) != 0 {
		t.Fatalf("预览不对: head=%q tail=%q", res.PrevHead, res.PrevTail)
	}
	if res.Meta.Out.Total != 12 || res.Meta.Err.Total != 4 || res.Meta.State != termproto.RunExited {
		t.Fatalf("元信息不对: %+v", res.Meta)
	}
	got, gaps, last := readAll(t, s, w.ID(), termproto.StreamStdout, 5)
	if string(got) != "hello world\n" || gaps != 0 || !last.EOF {
		t.Fatalf("读出 %q gaps=%d last=%+v", got, gaps, last)
	}
	if m, err := s.Meta(w.ID()); err != nil || m.State != termproto.RunExited || m.Code == nil || *m.Code != 0 {
		t.Fatalf("Meta() = %+v %v", m, err)
	}
}

func TestHeadTailTruncationLiveAndFinished(t *testing.T) {
	s := openStore(t, Options{HeadBytes: 100, TailBytes: 50, PreviewCap: [2]int{20, 10}})
	w := begin(t, s, 2)

	var all bytes.Buffer
	for i := 0; i < 40; i++ { // 40 * 25 = 1000 字节
		line := fmt.Sprintf("line-%04d-%s\n", i, strings.Repeat("x", 14))
		all.WriteString(line)
		w.Write(termproto.StreamStdout, []byte(line))
	}
	if all.Len() != 1000 {
		t.Fatalf("测试数据长度 = %d", all.Len())
	}
	want := all.Bytes()

	check := func(label string, running bool) {
		t.Helper()
		var data []byte
		var gaps int
		var off uint64
		for i := 0; i < 100; i++ {
			pg, err := s.Read(w.ID(), termproto.StreamStdout, off, 30)
			if err != nil {
				t.Fatalf("%s: %v", label, err)
			}
			if pg.Total != 1000 {
				t.Fatalf("%s: Total = %d", label, pg.Total)
			}
			if pg.Running != running {
				t.Fatalf("%s: Running = %v", label, pg.Running)
			}
			if pg.Gap {
				gaps++
			}
			data = append(data, pg.Data...)
			if pg.Next >= pg.Total {
				if pg.EOF == running {
					t.Fatalf("%s: EOF = %v，运行中 = %v", label, pg.EOF, running)
				}
				break
			}
			off = pg.Next
		}
		if gaps != 1 {
			t.Fatalf("%s: 缺口次数 = %d", label, gaps)
		}
		if !bytes.Equal(data[:100], want[:100]) {
			t.Fatalf("%s: 开头区域不一致", label)
		}
		if !bytes.Equal(data[100:], want[950:]) {
			t.Fatalf("%s: 结尾区域不一致: %q", label, data[100:])
		}
	}
	check("运行中", true)

	// 起点落在被丢弃部分时直接从结尾区域开始，并标记缺口。
	pg, err := s.Read(w.ID(), termproto.StreamStdout, 500, 1000)
	if err != nil || !pg.Gap || pg.Off != 950 || !bytes.Equal(pg.Data, want[950:]) {
		t.Fatalf("缺口读取: %+v err=%v", pg, err)
	}
	// 超出末尾：空页。
	pg, _ = s.Read(w.ID(), termproto.StreamStdout, 5000, 10)
	if len(pg.Data) != 0 || pg.Next != 1000 || pg.EOF {
		t.Fatalf("越界读取（运行中）: %+v", pg)
	}

	res := w.Finish(Finish{State: termproto.RunExited, Ended: time.Now()})
	if res.Meta.Out.Dropped() != 850 {
		t.Fatalf("被丢弃字节 = %d", res.Meta.Out.Dropped())
	}
	// 磁盘上只保留了开头与结尾，但总量不超过内联上限时，随 run_end 上报的是全文。
	if !bytes.Equal(res.Out, want) {
		t.Fatalf("总量不超过内联上限时应给出全文: %d 字节", len(res.Out))
	}
	if !bytes.Equal(res.PrevHead, want[:20]) || !bytes.Equal(res.PrevTail, want[990:]) {
		t.Fatalf("预览不对: head=%q tail=%q", res.PrevHead, res.PrevTail)
	}
	check("已结束", false)
}

func TestInlineLimitBoundary(t *testing.T) {
	s := openStore(t, Options{})
	w := begin(t, s, 3)
	exact := bytes.Repeat([]byte("a"), termproto.RunInlineMax)
	w.Write(termproto.StreamStdout, exact)
	w.Write(termproto.StreamStderr, append(exact, 'b'))
	res := w.Finish(Finish{State: termproto.RunExited, Ended: time.Now()})
	if !bytes.Equal(res.Out, exact) {
		t.Errorf("恰好等于上限的输出应内联（%d 字节）", len(res.Out))
	}
	if res.Err != nil {
		t.Errorf("超出上限一个字节就不应内联（%d 字节）", len(res.Err))
	}
}

func TestConcurrentWritersKeepTotals(t *testing.T) {
	s := openStore(t, Options{HeadBytes: 1 << 10, TailBytes: 1 << 10})
	w := begin(t, s, 4)
	var wg sync.WaitGroup
	chunk := bytes.Repeat([]byte("z"), 97)
	for _, st := range []byte{termproto.StreamStdout, termproto.StreamStderr} {
		wg.Add(1)
		go func(st byte) {
			defer wg.Done()
			for i := 0; i < 500; i++ {
				w.Write(st, chunk)
			}
		}(st)
	}
	wg.Wait()
	o, e := w.Totals()
	if o != 500*97 || e != 500*97 {
		t.Fatalf("总量 = %d / %d", o, e)
	}
	res := w.Finish(Finish{State: termproto.RunExited, Ended: time.Now()})
	if res.Meta.Out.Total != 500*97 || res.Meta.Err.Total != 500*97 {
		t.Fatalf("元信息总量 = %+v", res.Meta)
	}
}

func TestBeginRejectsDuplicateAndBadID(t *testing.T) {
	s := openStore(t, Options{})
	begin(t, s, 5)
	if _, err := s.Begin(Meta{ID: newID(5)}); err != ErrExists {
		t.Fatalf("重复 ID: %v", err)
	}
	if _, err := s.Begin(Meta{ID: "../../etc"}); err != ErrBadID {
		t.Fatalf("非法 ID: %v", err)
	}
	if _, err := s.Read("nope", termproto.StreamStdout, 0, 10); err != ErrBadID {
		t.Fatalf("读取非法 ID: %v", err)
	}
	if _, err := s.Read(newID(6), termproto.StreamStdout, 0, 10); err != ErrNotFound {
		t.Fatalf("读取不存在的运行: %v", err)
	}
	if _, err := s.Read(newID(5), 9, 0, 10); err != ErrBadID {
		t.Fatalf("读取未知流: %v", err)
	}
}

func TestRecoverOrphansMarksLostAndKeepsOutput(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "runs")
	s1 := openStore(t, Options{Dir: dir, HeadBytes: 10, TailBytes: 8})
	w := begin(t, s1, 7)
	w.SetStarted(1234, "bash", "/tmp", time.Now().Add(-time.Second))
	w.Write(termproto.StreamStdout, []byte("0123456789ABCDEFGHIJ")) // 头 10 + 尾 8（丢 2）
	w.FlushTails()
	// 模拟守护进程崩溃：不调用 Finish，直接用新的 Store 打开同一目录。
	s2 := openStore(t, Options{Dir: dir, HeadBytes: 10, TailBytes: 8})
	lost := s2.RecoverOrphans("holder_exit")
	if len(lost) != 1 || lost[0].State != termproto.RunLost || lost[0].Reason != "holder_exit" || lost[0].PID != 1234 {
		t.Fatalf("遗留运行 = %+v", lost)
	}
	if lost[0].Out.Head != 10 || lost[0].Out.Tail != 8 {
		t.Fatalf("按文件估计的区域 = %+v", lost[0].Out)
	}
	got, gaps, last := readAll(t, s2, newID(7), termproto.StreamStdout, 100)
	if string(got) != "0123456789CDEFGHIJ" || gaps != 1 || !last.EOF {
		t.Fatalf("读出 %q gaps=%d last=%+v", got, gaps, last)
	}
	if again := s2.RecoverOrphans("holder_exit"); len(again) != 0 {
		t.Fatalf("已标记的运行不应再次被标记: %+v", again)
	}
}

func TestUnreportedAndMarkReported(t *testing.T) {
	s := openStore(t, Options{})
	a, b := begin(t, s, 8), begin(t, s, 9)
	now := time.Now()
	a.Finish(Finish{State: termproto.RunExited, Ended: now.Add(-time.Minute)})
	b.Finish(Finish{State: termproto.RunTimeout, Ended: now})
	begin(t, s, 10) // 仍在运行：不会出现在清单里

	list := s.Unreported(10)
	if len(list) != 2 || list[0].ID != newID(8) || list[1].ID != newID(9) {
		t.Fatalf("未确认清单（应按结束时间从旧到新）= %+v", list)
	}
	if got := s.Unreported(1); len(got) != 1 || got[0].ID != newID(8) {
		t.Fatalf("limit 未生效: %+v", got)
	}
	if err := s.MarkReported(newID(8)); err != nil {
		t.Fatal(err)
	}
	if list := s.Unreported(10); len(list) != 1 || list[0].ID != newID(9) {
		t.Fatalf("确认后清单 = %+v", list)
	}
	if err := s.MarkReported(newID(10)); err != nil {
		t.Fatalf("运行中的运行确认应当无害: %v", err)
	}
	if err := s.MarkReported(newID(99)); err != ErrNotFound {
		t.Fatalf("确认不存在的运行: %v", err)
	}
}

func TestSweepRetentionAndQuota(t *testing.T) {
	now := time.Now()
	clock := now
	s := openStore(t, Options{Retention: 24 * time.Hour, Now: func() time.Time { return clock }})

	finish := func(n int, ended time.Time, size int) {
		w := begin(t, s, n)
		w.Write(termproto.StreamStdout, bytes.Repeat([]byte("q"), size))
		w.Finish(Finish{State: termproto.RunExited, Ended: ended})
	}
	finish(20, now.Add(-48*time.Hour), 100) // 过期
	finish(21, now.Add(-3*time.Hour), 1500)
	finish(22, now.Add(-2*time.Hour), 1500)
	finish(23, now.Add(-1*time.Hour), 1500)
	live := begin(t, s, 24) // 运行中的永远不删
	live.Write(termproto.StreamStdout, bytes.Repeat([]byte("l"), 100))

	// 配额按实际占用推算：刚好容得下 22、23 与运行中的 24，容不下再多一个 21。
	s.o.Quota = dirSize(s.dir(newID(22))) + dirSize(s.dir(newID(23))) + dirSize(s.dir(newID(24))) + 64

	removed := s.Sweep()
	if removed < 2 {
		t.Fatalf("应至少清掉过期的一个与超配额的最旧一个，实际 %d", removed)
	}
	for n, want := range map[int]bool{20: false, 21: false, 22: true, 23: true, 24: true} {
		_, err := os.Stat(s.dir(newID(n)))
		if (err == nil) != want {
			t.Errorf("运行 %d 存在 = %v，期望 %v", n, err == nil, want)
		}
	}
}

func TestDirectoryPermissions(t *testing.T) {
	if os.PathSeparator == '\\' {
		t.Skip("Windows 使用 ACL")
	}
	s := openStore(t, Options{})
	w := begin(t, s, 30)
	w.Write(termproto.StreamStdout, []byte("x"))
	w.Finish(Finish{State: termproto.RunExited, Ended: time.Now()})
	for _, p := range []string{s.root, s.dir(newID(30)), filepath.Join(s.dir(newID(30)), "stdout.log"), filepath.Join(s.dir(newID(30)), metaFile)} {
		fi, err := os.Stat(p)
		if err != nil {
			t.Fatal(err)
		}
		if fi.Mode().Perm()&0o077 != 0 {
			t.Errorf("%s 权限过宽: %v", p, fi.Mode().Perm())
		}
	}
}

func TestRingKeepsLatestBytes(t *testing.T) {
	r := newRing(5)
	r.write([]byte("ab"))
	r.write([]byte("cd"))
	if string(r.bytes()) != "abcd" {
		t.Fatalf("未满: %q", r.bytes())
	}
	r.write([]byte("efg"))
	if string(r.bytes()) != "cdefg" {
		t.Fatalf("回绕: %q", r.bytes())
	}
	r.write([]byte("0123456789"))
	if string(r.bytes()) != "56789" {
		t.Fatalf("超长写入: %q", r.bytes())
	}
	r.write([]byte("A"))
	if got := string(r.slice(1, 3)); got != "789" {
		t.Fatalf("slice = %q", got)
	}
	if r.slice(9, 3) != nil || r.slice(0, 0) != nil {
		t.Fatal("越界 slice 应为 nil")
	}
}

// breakDisk 让存储目录再也建不出新运行：把目录换成同名的普通文件（不需要 root，也不依赖具体的文件系统）。
func breakDisk(t *testing.T, s *Store) {
	t.Helper()
	if err := os.RemoveAll(s.root); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(s.root, []byte("not a dir"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestBeginFallsBackToMemoryWhenDiskUnavailable(t *testing.T) {
	var logs []string
	s := openStore(t, Options{TailBytes: 64, Logf: func(f string, a ...any) { logs = append(logs, fmt.Sprintf(f, a...)) }})
	breakDisk(t, s)

	w, err := s.Begin(Meta{ID: newID(1), Cmd: "df -h"})
	if err != nil {
		t.Fatalf("磁盘写不下时也应该让命令跑起来，却返回了 %v", err)
	}
	if !w.mem {
		t.Fatal("应退到只在内存里保留")
	}
	if len(logs) == 0 || !strings.Contains(logs[0], "只在内存里") {
		t.Fatalf("退化时应留下一条日志: %v", logs)
	}
	w.SetStarted(1234, "sh", "/", time.Now())

	// 运行中：输出超过结尾环，只留最近的 64 字节，其余记为被丢弃。
	big := strings.Repeat("0123456789", 20) // 200 字节
	w.Write(termproto.StreamStdout, []byte(big))
	pg, err := s.Read(newID(1), termproto.StreamStdout, 0, 4096)
	if err != nil {
		t.Fatal(err)
	}
	if !pg.Gap || pg.Total != 200 || !pg.Running || string(pg.Data) != big[200-64:] {
		t.Fatalf("运行中应只能读到最近 64 字节并标出缺口: %+v", pg)
	}
	if m, err := s.Meta(newID(1)); err != nil || m.State != StateRunning || m.Out.Total != 200 || m.Out.Head != 0 || m.Out.Dropped() != 136 {
		t.Fatalf("运行中的元信息不对: %+v %v", m, err)
	}

	// 结束：登记在内存里，仍能读、能查、能补报，且磁盘上没有任何东西。
	code := 0
	res := w.Finish(Finish{State: termproto.RunExited, Code: &code, Ended: time.Now()})
	if string(res.Out) != big {
		t.Fatalf("不超过内联上限（%d 字节）的输出应整段内联带回，即使结尾环只留了 64 字节: %q", termproto.RunInlineMax, res.Out)
	}
	m, err := s.Meta(newID(1))
	if err != nil || m.State != termproto.RunExited || m.Reported {
		t.Fatalf("结束后的元信息不对: %+v %v", m, err)
	}
	pg, err = s.Read(newID(1), termproto.StreamStdout, 0, 4096)
	if err != nil || !pg.EOF || pg.Running || string(pg.Data) != big[200-64:] {
		t.Fatalf("结束后应读到结尾并到达 EOF: %+v %v", pg, err)
	}
	if un := s.Unreported(10); len(un) != 1 || un[0].ID != newID(1) {
		t.Fatalf("没被确认的内存运行应出现在补报列表里: %+v", un)
	}
	if err := s.MarkReported(newID(1)); err != nil {
		t.Fatal(err)
	}
	if un := s.Unreported(10); len(un) != 0 {
		t.Fatalf("确认之后不应再补报: %+v", un)
	}
	if fi, err := os.Stat(s.root); err != nil || fi.IsDir() {
		t.Fatalf("不应在磁盘上创建任何目录: %v", err)
	}
	if _, err := s.Begin(Meta{ID: newID(1)}); err != ErrExists {
		t.Fatalf("相同 ID 应被拒绝: %v", err)
	}
}

func TestMemoryRunsAreBoundedAndInlineSmallOutput(t *testing.T) {
	s := openStore(t, Options{})
	breakDisk(t, s)
	for i := 1; i <= maxMemDone+4; i++ {
		w, err := s.Begin(Meta{ID: newID(i), Cmd: "echo"})
		if err != nil {
			t.Fatal(err)
		}
		w.Write(termproto.StreamStdout, []byte("small\n"))
		code := 0
		res := w.Finish(Finish{State: termproto.RunExited, Code: &code, Ended: time.Now()})
		if string(res.Out) != "small\n" {
			t.Fatalf("小输出应内联带回: %q", res.Out)
		}
	}
	if _, err := s.Meta(newID(1)); err == nil {
		t.Fatal("最旧的内存运行应已被挤掉")
	}
	if _, err := s.Meta(newID(maxMemDone + 4)); err != nil {
		t.Fatalf("最新的内存运行应还在: %v", err)
	}
	s.mu.Lock()
	n := len(s.memDone)
	s.mu.Unlock()
	if n != maxMemDone {
		t.Fatalf("内存里最多保留 %d 个，实际 %d", maxMemDone, n)
	}
}

func TestReclaimRemovesOldestConfirmedRunsOnly(t *testing.T) {
	s := openStore(t, Options{})
	finish := func(n int, reported bool, ended time.Time) {
		w := begin(t, s, n)
		w.Write(termproto.StreamStdout, []byte("x"))
		code := 0
		w.Finish(Finish{State: termproto.RunExited, Code: &code, Ended: ended})
		if reported {
			if err := s.MarkReported(newID(n)); err != nil {
				t.Fatal(err)
			}
		}
	}
	now := time.Now()
	finish(1, true, now.Add(-4*time.Hour))
	finish(2, true, now.Add(-3*time.Hour))
	finish(3, true, now.Add(-2*time.Hour))
	finish(4, false, now.Add(-5*time.Hour)) // 最旧，但 Hub 还没确认：不能动
	finish(5, true, now.Add(-1*time.Hour))
	running := begin(t, s, 6) // 运行中：不能动
	running.Write(termproto.StreamStdout, []byte("y"))

	// 4 个已确认 → 删最旧的 2 个（1 和 2）。
	if n := s.reclaim(); n != 2 {
		t.Fatalf("应删除一半已确认的运行，删了 %d", n)
	}
	for _, c := range []struct {
		n    int
		want bool
	}{{1, false}, {2, false}, {3, true}, {4, true}, {5, true}, {6, true}} {
		_, err := os.Stat(s.dir(newID(c.n)))
		if (err == nil) != c.want {
			t.Fatalf("运行 %d 的目录存在=%v，期望 %v", c.n, err == nil, c.want)
		}
	}
	if n := (&Store{root: filepath.Join(t.TempDir(), "none"), live: map[string]*Writer{}}).reclaim(); n != 0 {
		t.Fatalf("没有可回收的运行时应返回 0: %d", n)
	}
}

// 磁盘写满时「已确认」的标记写不进 meta.json：内存里记着，本进程内不再补报，空间紧张时也允许回收。
func TestAckedInMemoryWhenMetaCannotBeWritten(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("需要非特权 POSIX 用户才能用目录权限制造写失败")
	}
	s := openStore(t, Options{})
	w := begin(t, s, 1)
	w.Write(termproto.StreamStdout, []byte("x"))
	code := 0
	w.Finish(Finish{State: termproto.RunExited, Code: &code, Ended: time.Now()})

	dir := s.dir(newID(1))
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	if err := s.MarkReported(newID(1)); err == nil {
		t.Fatal("目录不可写时标记应失败")
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if un := s.Unreported(10); len(un) != 0 {
		t.Fatalf("已在内存里确认的运行不应再补报: %+v", un)
	}
	if n := s.reclaim(); n != 1 {
		t.Fatalf("已在内存里确认的运行应允许回收，回收了 %d", n)
	}
	if s.isAcked(newID(1)) {
		t.Fatal("回收之后内存里的标记也应清掉")
	}
}

// 结束时最终的元信息写不进磁盘（磁盘写满）：盘上的 meta.json 还停在「运行中」，
// 但状态、读取、补报、回收都以内存里的为准，不能把一个已经结束的运行当成永远在跑。
func TestFinishKeepsStateInMemoryWhenMetaCannotBeWritten(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("需要非特权 POSIX 用户才能用目录权限制造写失败")
	}
	s := openStore(t, Options{TailBytes: 8, HeadBytes: 8})
	w := begin(t, s, 1)
	w.SetStarted(99, "sh", "/", time.Now())
	w.Write(termproto.StreamStdout, []byte("0123456789ABCDEFGHIJ")) // 头 8 字节在文件里，结尾环留最后 8 字节

	dir := s.dir(newID(1))
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	code := 0
	w.Finish(Finish{State: termproto.RunExited, Code: &code, Ended: time.Now()})

	m, err := s.Meta(newID(1))
	if err != nil || m.State != termproto.RunExited {
		t.Fatalf("状态应以内存里的为准（已结束），得到 %+v %v", m, err)
	}
	data, gaps, last := readAll(t, s, newID(1), termproto.StreamStdout, 4096)
	if string(data) != "01234567CDEFGHIJ" || gaps != 1 || last.Running || !last.EOF {
		t.Fatalf("应读到头 8 字节 + 缺口 + 尾 8 字节: %q gaps=%d %+v", data, gaps, last)
	}
	if un := s.Unreported(10); len(un) != 1 || un[0].State != termproto.RunExited {
		t.Fatalf("结束事件还没被确认，应在补报列表里且状态是已结束: %+v", un)
	}
	if err := s.MarkReported(newID(1)); err != nil {
		t.Fatalf("内存里登记的运行确认应成功: %v", err)
	}
	if un := s.Unreported(10); len(un) != 0 {
		t.Fatalf("确认后不应再补报: %+v", un)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if n := s.reclaim(); n != 1 {
		t.Fatalf("已结束且已确认的运行应可回收，回收了 %d", n)
	}
}
