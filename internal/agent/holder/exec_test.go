//go:build !windows

package holder

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/teacat99/RelayMesh/v2/internal/agent/runlog"
	"github.com/teacat99/RelayMesh/v2/internal/termproto"
)

// fakeSink 记录发给控制端的全部帧。
type fakeSink struct {
	mu   sync.Mutex
	text []json.RawMessage
	bins [][]byte
	cond *sync.Cond
}

func newFakeSink() *fakeSink {
	f := &fakeSink{}
	f.cond = sync.NewCond(&f.mu)
	return f
}

func (f *fakeSink) sendJSON(v any) error {
	data, err := termproto.Marshal(v)
	if err != nil {
		return err
	}
	f.mu.Lock()
	f.text = append(f.text, data)
	f.cond.Broadcast()
	f.mu.Unlock()
	return nil
}

func (f *fakeSink) sendBinary(b []byte) error {
	f.mu.Lock()
	f.bins = append(f.bins, append([]byte(nil), b...))
	f.cond.Broadcast()
	f.mu.Unlock()
	return nil
}

// types 返回已收到的文本帧类型序列。
func (f *fakeSink) types() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, raw := range f.text {
		t, _ := termproto.ParseType(raw)
		out = append(out, t)
	}
	return out
}

// wait 等到出现满足条件的文本帧并返回它；超时则让测试失败。
func (f *fakeSink) wait(t *testing.T, what string, match func(typ string, raw json.RawMessage) bool) json.RawMessage {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	f.mu.Lock()
	defer f.mu.Unlock()
	seen := 0
	for {
		for ; seen < len(f.text); seen++ {
			typ, _ := termproto.ParseType(f.text[seen])
			if match(typ, f.text[seen]) {
				return f.text[seen]
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("等待 %s 超时；已收到 %v", what, func() []string {
				var out []string
				for _, r := range f.text {
					ty, _ := termproto.ParseType(r)
					out = append(out, ty)
				}
				return out
			}())
		}
		go func() { time.Sleep(50 * time.Millisecond); f.mu.Lock(); f.cond.Broadcast(); f.mu.Unlock() }()
		f.cond.Wait()
	}
}

func (f *fakeSink) waitEnd(t *testing.T, id string) termproto.RunEnd {
	t.Helper()
	raw := f.wait(t, "run_end "+id[:8], func(typ string, raw json.RawMessage) bool {
		if typ != termproto.TRunEnd {
			return false
		}
		var e termproto.RunEnd
		return json.Unmarshal(raw, &e) == nil && e.ID == id
	})
	var e termproto.RunEnd
	if err := json.Unmarshal(raw, &e); err != nil {
		t.Fatal(err)
	}
	return e
}

func (f *fakeSink) waitFailed(t *testing.T, id string) termproto.RunFailed {
	t.Helper()
	raw := f.wait(t, "run_failed "+id[:8], func(typ string, raw json.RawMessage) bool {
		if typ != termproto.TRunFailed {
			return false
		}
		var e termproto.RunFailed
		return json.Unmarshal(raw, &e) == nil && e.ID == id
	})
	var e termproto.RunFailed
	_ = json.Unmarshal(raw, &e)
	return e
}

func testRunID(n int) string { return fmt.Sprintf("%032x", n) }

func newTestExec(t *testing.T, mod func(*ExecOptions)) (*ExecManager, *fakeSink) {
	t.Helper()
	o := ExecOptions{Dir: filepath.Join(t.TempDir(), "runs"), KillGrace: 300 * time.Millisecond}
	if mod != nil {
		mod(&o)
	}
	m := NewExecManager(o, t.Logf, nil)
	m.Start()
	t.Cleanup(func() { m.Shutdown(3 * time.Second) })
	sink := newFakeSink()
	m.SetSink(sink)
	return m, sink
}

func TestExecRunCapturesOutputAndExitCode(t *testing.T) {
	m, sink := newTestExec(t, nil)
	id := testRunID(1)
	if f := m.StartRun(termproto.Run{ID: id, Cmd: "printf 'hi'; printf 'err' 1>&2; exit 3"}); f != nil {
		t.Fatalf("启动失败: %+v", f)
	}
	end := sink.waitEnd(t, id)
	if end.State != termproto.RunExited || end.Code == nil || *end.Code != 3 {
		t.Fatalf("结局不对: %+v", end)
	}
	if string(end.Out) != "hi" || string(end.Err) != "err" || end.OutBytes != 2 || end.ErrBytes != 3 {
		t.Fatalf("输出不对: out=%q err=%q %d/%d", end.Out, end.Err, end.OutBytes, end.ErrBytes)
	}
	if !strings.Contains(string(end.PrevHead), "hi") || !strings.Contains(string(end.PrevHead), "err") {
		t.Fatalf("预览应合并两路输出: %q", end.PrevHead)
	}
	// 顺序：run_started 必须先于 run_end。
	types := sink.types()
	si, ei := -1, -1
	for i, ty := range types {
		switch ty {
		case termproto.TRunStarted:
			if si < 0 {
				si = i
			}
		case termproto.TRunEnd:
			ei = i
		}
	}
	if si < 0 || ei < 0 || si > ei {
		t.Fatalf("帧顺序不对: %v", types)
	}
}

func TestExecDeniedByHostSpawnsNothing(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "ran")
	m, sink := newTestExec(t, func(o *ExecOptions) { o.DenyExec = true })
	id := testRunID(2)
	f := m.StartRun(termproto.Run{ID: id, Cmd: "touch " + marker})
	if f == nil || f.Reason != termproto.RunFailDenied {
		t.Fatalf("应被一票否决: %+v", f)
	}
	if got := sink.waitFailed(t, id); got.Reason != termproto.RunFailDenied {
		t.Fatalf("帧不对: %+v", got)
	}
	time.Sleep(200 * time.Millisecond)
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("被否决的命令不应被执行")
	}
	if m.ActiveCount() != 0 {
		t.Fatal("不应有活动运行")
	}
	if !m.Denied() {
		t.Fatal("Denied() 应为 true")
	}
}

func TestExecUnavailableWithoutOutputDir(t *testing.T) {
	m := NewExecManager(ExecOptions{}, t.Logf, nil)
	sink := newFakeSink()
	m.SetSink(sink)
	f := m.StartRun(termproto.Run{ID: testRunID(3), Cmd: "true"})
	if f == nil || f.Reason != termproto.RunFailUnavailable {
		t.Fatalf("没有输出目录应报 exec_unavailable: %+v", f)
	}
	if m.Available() {
		t.Fatal("Available() 应为 false")
	}
}

func TestExecRejectsBadRequests(t *testing.T) {
	m, _ := newTestExec(t, nil)
	cases := []struct {
		name string
		run  termproto.Run
		want string
	}{
		{"非法 ID", termproto.Run{ID: "x", Cmd: "true"}, termproto.RunFailBad},
		{"空命令", termproto.Run{ID: testRunID(10), Cmd: "  \n"}, termproto.RunFailBad},
		{"含 NUL", termproto.Run{ID: testRunID(11), Cmd: "echo a\x00b"}, termproto.RunFailBad},
		{"非法 UTF-8", termproto.Run{ID: testRunID(12), Cmd: "echo \xff\xfe"}, termproto.RunFailBad},
		{"过长", termproto.Run{ID: testRunID(13), Cmd: strings.Repeat("a", termproto.MaxRunCommandBytes+1)}, termproto.RunFailBad},
		{"不支持的 shell", termproto.Run{ID: testRunID(14), Cmd: "true", Shell: "fish"}, termproto.RunFailBad},
		{"相对目录", termproto.Run{ID: testRunID(15), Cmd: "true", Cwd: "relative/dir"}, termproto.RunFailCwd},
		{"目录不存在", termproto.Run{ID: testRunID(16), Cmd: "true", Cwd: "/nonexistent-dir-xyz"}, termproto.RunFailCwd},
	}
	for _, c := range cases {
		f := m.StartRun(c.run)
		if f == nil || f.Reason != c.want {
			t.Errorf("%s: 期望 %s，实际 %+v", c.name, c.want, f)
		}
	}
	if m.ActiveCount() != 0 {
		t.Fatalf("失败的请求不应留下活动运行: %d", m.ActiveCount())
	}
}

func TestExecDuplicateAndBusy(t *testing.T) {
	m, sink := newTestExec(t, func(o *ExecOptions) { o.MaxActive = 2 })
	a, b, c := testRunID(20), testRunID(21), testRunID(22)
	if f := m.StartRun(termproto.Run{ID: a, Cmd: "sleep 30"}); f != nil {
		t.Fatal(f)
	}
	if f := m.StartRun(termproto.Run{ID: a, Cmd: "sleep 30"}); f == nil || f.Reason != termproto.RunFailDuplicate {
		t.Fatalf("重复 ID: %+v", f)
	}
	if f := m.StartRun(termproto.Run{ID: b, Cmd: "sleep 30"}); f != nil {
		t.Fatal(f)
	}
	if f := m.StartRun(termproto.Run{ID: c, Cmd: "true"}); f == nil || f.Reason != termproto.RunFailBusy {
		t.Fatalf("达到保护上限: %+v", f)
	}
	m.Cancel(a, "user")
	m.Cancel(b, "user")
	sink.waitEnd(t, a)
	sink.waitEnd(t, b)
}

func TestExecTimeoutKillsWholeProcessGroup(t *testing.T) {
	m, sink := newTestExec(t, nil)
	pidFile := filepath.Join(t.TempDir(), "child.pid")
	id := testRunID(30)
	start := time.Now()
	// 子进程忽略 TERM：验证宽限后会被强制终止，且孙子进程不会留存。
	cmd := fmt.Sprintf("sh -c 'trap \"\" TERM; echo $$ > %s; sleep 60' & wait", pidFile)
	if f := m.StartRun(termproto.Run{ID: id, Cmd: cmd, TimeoutSec: 1}); f != nil {
		t.Fatal(f)
	}
	end := sink.waitEnd(t, id)
	if end.State != termproto.RunTimeout || end.Reason != "timeout" {
		t.Fatalf("应判为超时: %+v", end)
	}
	if time.Since(start) > 12*time.Second {
		t.Fatalf("超时终止过慢: %v", time.Since(start))
	}
	raw, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatal(err)
	}
	pid, _ := strconv.Atoi(strings.TrimSpace(string(raw)))
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if syscall.Kill(pid, 0) != nil {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	_ = syscall.Kill(pid, syscall.SIGKILL)
	t.Fatalf("超时后孙子进程 %d 仍然存活", pid)
}

func TestExecCancel(t *testing.T) {
	m, sink := newTestExec(t, nil)
	id := testRunID(31)
	if f := m.StartRun(termproto.Run{ID: id, Cmd: "echo begin; sleep 60"}); f != nil {
		t.Fatal(f)
	}
	sink.wait(t, "run_started", func(typ string, _ json.RawMessage) bool { return typ == termproto.TRunStarted })
	time.Sleep(300 * time.Millisecond) // 让 echo 先跑出来
	m.Cancel(id, "user")
	m.Cancel(id, "user") // 重复取消无害
	end := sink.waitEnd(t, id)
	if end.State != termproto.RunCancelled || end.Reason != "user" || end.Code != nil {
		t.Fatalf("应判为取消: %+v", end)
	}
	if !strings.Contains(string(end.Out), "begin") {
		t.Fatalf("取消前的输出应当保留: %q", end.Out)
	}
	m.Cancel(testRunID(999), "user") // 不存在的运行：忽略
}

func TestExecKilledBySignalIsSignaled(t *testing.T) {
	m, sink := newTestExec(t, nil)
	id := testRunID(32)
	if f := m.StartRun(termproto.Run{ID: id, Cmd: "kill -SEGV $$"}); f != nil {
		t.Fatal(f)
	}
	end := sink.waitEnd(t, id)
	if end.State != termproto.RunSignaled || end.Signal != "SIGSEGV" || end.Code != nil {
		t.Fatalf("应判为被信号终止: %+v", end)
	}
}

func TestExecLargeOutputKeepsHeadAndTail(t *testing.T) {
	m, sink := newTestExec(t, func(o *ExecOptions) { o.HeadBytes, o.TailBytes = 4096, 2048 })
	id := testRunID(33)
	// 100000 字节：每行 "NNNNNNNN\n"（9 字节），行号递增，便于核对首尾内容。
	cmd := `i=0; while [ $i -lt 11112 ]; do printf '%08d\n' $i; i=$((i+1)); done`
	if f := m.StartRun(termproto.Run{ID: id, Cmd: cmd}); f != nil {
		t.Fatal(f)
	}
	end := sink.waitEnd(t, id)
	if end.OutBytes != 11112*9 || end.OutDropped != 11112*9-4096-2048 || end.Out != nil {
		t.Fatalf("统计不对: %+v", end)
	}
	if !strings.HasPrefix(string(end.PrevHead), "00000000\n00000001\n") || !strings.HasSuffix(string(end.PrevTail), "00011111\n") {
		t.Fatalf("预览不对: head=%q tail=%q", end.PrevHead[:20], end.PrevTail[len(end.PrevTail)-20:])
	}

	// 通过 run_read 读取：开头、缺口、结尾。
	rd := newFakeSink()
	m.ServeRead(rd, termproto.RunRead{Req: 7, ID: id, Stream: "stdout", Off: 0, Max: termproto.MaxRunRead})
	var got []byte
	var gaps int
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		rd.mu.Lock()
		frames := append([][]byte(nil), rd.bins...)
		rd.mu.Unlock()
		got, gaps = nil, 0
		last := false
		for _, fr := range frames {
			c, err := termproto.DecodeRunData(fr)
			if err != nil {
				t.Fatal(err)
			}
			if c.Req != 7 {
				t.Fatalf("req 不对: %d", c.Req)
			}
			if c.Flags&termproto.RunFlagGap != 0 {
				gaps++
			}
			got = append(got, c.Data...)
			last = c.Flags&termproto.RunFlagLast != 0
		}
		if last {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if len(got) != 4096+2048 || gaps != 1 {
		t.Fatalf("读出 %d 字节、缺口 %d 次", len(got), gaps)
	}
	if !bytes.HasPrefix(got, []byte("00000000\n")) || !bytes.HasSuffix(got, []byte("00011111\n")) {
		t.Fatal("读出的首尾内容不对")
	}
}

func TestExecReadErrors(t *testing.T) {
	m, _ := newTestExec(t, nil)
	rd := newFakeSink()
	m.ServeRead(rd, termproto.RunRead{Req: 1, ID: "bad", Stream: "stdout"})
	m.ServeRead(rd, termproto.RunRead{Req: 2, ID: testRunID(404), Stream: "stdout"})
	m.ServeRead(rd, termproto.RunRead{Req: 3, ID: testRunID(404), Stream: "weird"})
	want := map[uint32]string{1: termproto.RunReadBad, 2: termproto.RunReadNotFound, 3: termproto.RunReadBad}
	for len(want) > 0 {
		raw := rd.wait(t, "run_read_fail", func(typ string, _ json.RawMessage) bool { return typ == termproto.TRunReadFail })
		var f termproto.RunReadFail
		_ = json.Unmarshal(raw, &f)
		if want[f.Req] != f.Reason {
			t.Fatalf("req %d: %s", f.Req, f.Reason)
		}
		delete(want, f.Req)
		rd.mu.Lock()
		rd.text = rd.text[1:]
		rd.mu.Unlock()
	}
}

func TestExecSyncReplaysUnackedEnds(t *testing.T) {
	m, sink := newTestExec(t, nil)
	id1, id2 := testRunID(40), testRunID(41)
	m.SetSink(nil) // 控制端不在：结束事件无处可投
	for _, id := range []string{id1, id2} {
		if f := m.StartRun(termproto.Run{ID: id, Cmd: "echo " + id[:4]}); f != nil {
			t.Fatal(f)
		}
	}
	deadline := time.Now().Add(10 * time.Second)
	for m.ActiveCount() > 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if m.ActiveCount() != 0 {
		t.Fatal("运行没有结束")
	}

	m.SetSink(sink)
	m.Sync()
	runs := sink.wait(t, "runs", func(typ string, _ json.RawMessage) bool { return typ == termproto.TRuns })
	var rs termproto.Runs
	_ = json.Unmarshal(runs, &rs)
	if len(rs.Running) != 0 {
		t.Fatalf("没有运行中的运行: %+v", rs)
	}
	e1, e2 := sink.waitEnd(t, id1), sink.waitEnd(t, id2)
	if string(e1.Out) != id1[:4]+"\n" || string(e2.Out) != id2[:4]+"\n" || e1.State != termproto.RunExited {
		t.Fatalf("补报内容不对: %+v %+v", e1, e2)
	}

	// 确认其一后，再次对账只会补报另一个。
	m.Ack(id1)
	sink2 := newFakeSink()
	m.SetSink(sink2)
	m.Sync()
	sink2.waitEnd(t, id2)
	for _, ty := range sink2.types() {
		if ty == termproto.TRunEnd {
			var found bool
			sink2.mu.Lock()
			for _, raw := range sink2.text {
				var e termproto.RunEnd
				if json.Unmarshal(raw, &e) == nil && e.ID == id1 {
					found = true
				}
			}
			sink2.mu.Unlock()
			if found {
				t.Fatal("已确认的结束事件不应再补报")
			}
		}
	}
}

func TestExecSyncListsRunningRuns(t *testing.T) {
	m, sink := newTestExec(t, nil)
	id := testRunID(42)
	if f := m.StartRun(termproto.Run{ID: id, Cmd: "sleep 60"}); f != nil {
		t.Fatal(f)
	}
	m.Sync()
	raw := sink.wait(t, "runs", func(typ string, _ json.RawMessage) bool { return typ == termproto.TRuns })
	var rs termproto.Runs
	_ = json.Unmarshal(raw, &rs)
	if len(rs.Running) != 1 || rs.Running[0].ID != id || rs.Running[0].PID <= 0 || rs.Running[0].Started <= 0 {
		t.Fatalf("运行清单不对: %+v", rs)
	}
	m.Cancel(id, "user")
	sink.waitEnd(t, id)
}

func TestExecOrphansBecomeLostAndAreReported(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "runs")
	// 模拟上一个守护进程在运行中途消失：只有元信息与部分输出，没有收尾。
	st, err := runlog.Open(runlog.Options{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	id := testRunID(50)
	w, err := st.Begin(runlog.Meta{ID: id, Cmd: "make all", Started: time.Now().Add(-time.Minute).UnixMilli()})
	if err != nil {
		t.Fatal(err)
	}
	w.Write(termproto.StreamStdout, []byte("compiling...\n"))
	w.FlushTails()

	m := NewExecManager(ExecOptions{Dir: dir}, t.Logf, nil)
	m.RecoverOrphans(LostHostReboot)
	sink := newFakeSink()
	m.SetSink(sink)
	m.Sync()
	end := sink.waitEnd(t, id)
	if end.State != termproto.RunLost || end.Reason != LostHostReboot || string(end.Out) != "compiling...\n" {
		t.Fatalf("遗留运行应判为 lost 并保留已落盘的输出: %+v", end)
	}
}

func TestExecShutdownCancelsRunningRuns(t *testing.T) {
	m, sink := newTestExec(t, nil)
	id := testRunID(60)
	if f := m.StartRun(termproto.Run{ID: id, Cmd: "sleep 60"}); f != nil {
		t.Fatal(f)
	}
	m.Shutdown(5 * time.Second)
	end := sink.waitEnd(t, id)
	if end.State != termproto.RunCancelled || end.Reason != "holder_stopped" {
		t.Fatalf("关停应取消运行并说明原因: %+v", end)
	}
	if f := m.StartRun(termproto.Run{ID: testRunID(61), Cmd: "true"}); f == nil || f.Reason != termproto.RunFailDraining {
		t.Fatalf("关停后不再接受运行: %+v", f)
	}
}

func TestExecDrainingRefusesNewRunsButKeepsRunning(t *testing.T) {
	m, sink := newTestExec(t, nil)
	id := testRunID(62)
	if f := m.StartRun(termproto.Run{ID: id, Cmd: "sleep 1; echo done"}); f != nil {
		t.Fatal(f)
	}
	m.SetDraining()
	if f := m.StartRun(termproto.Run{ID: testRunID(63), Cmd: "true"}); f == nil || f.Reason != termproto.RunFailDraining {
		t.Fatalf("排空中不再接受新运行: %+v", f)
	}
	if end := sink.waitEnd(t, id); end.State != termproto.RunExited || string(end.Out) != "done\n" {
		t.Fatalf("排空不应打断已在运行的命令: %+v", end)
	}
}

// 回归（#73）：排空只数了交互会话，会话一归零就退出。模型命令由 ExecManager 持有、不在会话表里，
// 于是 holder restart --drain 会把还在运行的模型命令以 holder_stopped 杀掉，和「不结束运行中的工作」的承诺相反。
func TestRunDrainWaitsForModelCommands(t *testing.T) {
	dataDir := shortDataDir(t)
	opts, _ := fakeRunOptions(dataDir)
	socket, _, wait := startRun(t, opts)

	// 没有任何交互会话，只有一条模型命令在跑。
	ctl := &peer{t: t, client: dialRole(t, socket, RoleController)}
	ctl.expect(termproto.TSessions)
	marker := filepath.Join(dataDir, "run-finished")
	ctl.sendJSON(termproto.Run{T: termproto.TRun, ID: testRunID(73), Cmd: "sleep 2; touch " + marker})
	ctl.expect(termproto.TRunStarted)

	adm := dialRole(t, socket, RoleAdmin)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	// 管理接口要报出运行中的模型命令数：restart / stop 的确认与 status 都靠它。
	st, err := Status(ctx, adm)
	if err != nil {
		t.Fatal(err)
	}
	if st.Runs == nil || *st.Runs != 1 {
		t.Fatalf("status 应报告 1 条运行中的模型命令: %+v", st.Runs)
	}

	if err := SendShutdown(ctx, adm.Conn, ShutdownDrain); err != nil {
		t.Fatal(err)
	}
	exited := make(chan error, 1)
	go func() { exited <- wait() }()
	select {
	case <-exited:
		t.Fatal("还有运行中的模型命令，排空不应退出")
	case <-time.After(1200 * time.Millisecond): // 超过两个轮询间隔
	}
	// 命令自然结束 → 守护进程自动退出；命令是跑完的，不是被终止的。
	select {
	case err := <-exited:
		if err != nil {
			t.Fatalf("排空完成应干净退出: %v", err)
		}
	case <-time.After(8 * time.Second):
		t.Fatal("命令结束后守护进程应自动退出")
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("命令应当自然跑完（而不是被 holder_stopped 终止）: %v", err)
	}
}

// 对照：立即关停（restart / stop 不带 --drain）仍然会终止模型命令——排空与立即是两个不同的承诺。
func TestRunShutdownNowStillCancelsModelCommands(t *testing.T) {
	dataDir := shortDataDir(t)
	opts, _ := fakeRunOptions(dataDir)
	socket, _, wait := startRun(t, opts)

	ctl := &peer{t: t, client: dialRole(t, socket, RoleController)}
	ctl.expect(termproto.TSessions)
	ctl.sendJSON(termproto.Run{T: termproto.TRun, ID: testRunID(74), Cmd: "sleep 60"})
	ctl.expect(termproto.TRunStarted)

	adm := dialRole(t, socket, RoleAdmin)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := SendShutdown(ctx, adm.Conn, ShutdownNow); err != nil {
		t.Fatal(err)
	}
	if err := wait(); err != nil {
		t.Fatalf("立即关停应干净返回: %v", err)
	}
}

func TestExecEnvironmentIsNonInteractiveAndClean(t *testing.T) {
	t.Setenv("RELAYMESH_SECRET_FOR_TEST", "should-not-leak")
	m, sink := newTestExec(t, nil)
	id := testRunID(70)
	if f := m.StartRun(termproto.Run{ID: id, Cmd: `echo "TERM=$TERM"; echo "NO_COLOR=$NO_COLOR"; env | grep -c '^RELAYMESH_' || true; test -t 0 && echo tty || echo notty`}); f != nil {
		t.Fatal(f)
	}
	end := sink.waitEnd(t, id)
	out := string(end.Out)
	if !strings.Contains(out, "TERM=dumb\n") || !strings.Contains(out, "NO_COLOR=1\n") || !strings.Contains(out, "\n0\n") || !strings.Contains(out, "notty") {
		t.Fatalf("环境不对:\n%s", out)
	}
}

func TestExecWorkingDirectory(t *testing.T) {
	m, sink := newTestExec(t, nil)
	dir := t.TempDir()
	id := testRunID(71)
	if f := m.StartRun(termproto.Run{ID: id, Cmd: "pwd -P", Cwd: dir}); f != nil {
		t.Fatal(f)
	}
	end := sink.waitEnd(t, id)
	want, _ := filepath.EvalSymlinks(dir)
	if strings.TrimSpace(string(end.Out)) != want {
		t.Fatalf("pwd = %q，期望 %q", end.Out, want)
	}
}

func TestExecBackgroundChildDoesNotHangTheRun(t *testing.T) {
	m, sink := newTestExec(t, nil)
	id := testRunID(72)
	pidFile := filepath.Join(t.TempDir(), "bg.pid")
	// 后台子进程一直占着输出管道：运行应当在管道排空宽限之后结束，而不是被卡到超时。
	if f := m.StartRun(termproto.Run{ID: id, Cmd: fmt.Sprintf("sleep 60 & echo $! > %s; echo started", pidFile), TimeoutSec: 30}); f != nil {
		t.Fatal(f)
	}
	start := time.Now()
	end := sink.waitEnd(t, id)
	if time.Since(start) > 10*time.Second {
		t.Fatalf("被后台子进程拖住了: %v", time.Since(start))
	}
	if end.State != termproto.RunExited || end.Code == nil || *end.Code != 0 || string(end.Out) != "started\n" {
		t.Fatalf("结局不对: %+v", end)
	}
	if raw, err := os.ReadFile(pidFile); err == nil {
		if pid, _ := strconv.Atoi(strings.TrimSpace(string(raw))); pid > 0 {
			_ = syscall.Kill(pid, syscall.SIGKILL) // 清理测试遗留的后台进程
		}
	}
}

func TestExecLiveFramesAreOrderedAndBounded(t *testing.T) {
	m, sink := newTestExec(t, nil)
	id := testRunID(73)
	if f := m.StartRun(termproto.Run{ID: id, Cmd: "printf 'abc'; sleep 0.3; printf 'def'; printf 'xyz' 1>&2"}); f != nil {
		t.Fatal(f)
	}
	sink.waitEnd(t, id)
	deadline := time.Now().Add(3 * time.Second)
	var out, errb []byte
	for time.Now().Before(deadline) {
		sink.mu.Lock()
		frames := append([][]byte(nil), sink.bins...)
		sink.mu.Unlock()
		out, errb = nil, nil
		for _, fr := range frames {
			gid, stream, off, payload, err := termproto.DecodeRunOut(fr)
			if err != nil || gid != id {
				t.Fatalf("实时帧不对: %v %s", err, gid)
			}
			switch stream {
			case termproto.StreamStdout:
				if uint64(len(out)) != off {
					t.Fatalf("stdout 偏移不连续: off=%d have=%d", off, len(out))
				}
				out = append(out, payload...)
			case termproto.StreamStderr:
				if uint64(len(errb)) != off {
					t.Fatalf("stderr 偏移不连续: off=%d have=%d", off, len(errb))
				}
				errb = append(errb, payload...)
			}
		}
		if string(out) == "abcdef" && string(errb) == "xyz" {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("实时输出不完整: out=%q err=%q", out, errb)
}

func TestLiveEmitterDropsInsteadOfBlocking(t *testing.T) {
	m := NewExecManager(ExecOptions{}, t.Logf, nil)
	e := &liveEmitter{m: m, id: testRunID(80), tokens: 1024, last: time.Now()}
	chunk := bytes.Repeat([]byte("x"), runLiveBatch)
	start := time.Now()
	for i := 0; i < 2000; i++ { // 约 32 MiB，远超队列与速率
		e.emit(termproto.StreamStdout, uint64(i*len(chunk)), chunk)
	}
	if time.Since(start) > 5*time.Second {
		t.Fatalf("实时流不应拖慢输出: %v", time.Since(start))
	}
	if m.liveDropped.Load() == 0 {
		t.Fatal("超过速率与队列容量的部分应被丢弃并计数")
	}
	if len(m.liveQ) > runLiveQueue {
		t.Fatalf("队列超出上限: %d", len(m.liveQ))
	}
}

// 磁盘写不下（INV-R3）：命令照样要跑，结果照样回到 Hub；只是完整输出不落盘，只在内存里留最近的一段。
func TestExecRunsWhenOutputDiskUnavailable(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "runs")
	m, sink := newTestExec(t, func(o *ExecOptions) { o.Dir = dir })
	// 把输出目录换成同名的普通文件：之后再也建不出新运行的目录。
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dir, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	id := testRunID(40)
	if f := m.StartRun(termproto.Run{ID: id, Cmd: "printf 'df-output'; printf 'warn' 1>&2; exit 2"}); f != nil {
		t.Fatalf("磁盘写不下也应该让命令跑起来: %+v", f)
	}
	end := sink.waitEnd(t, id)
	if end.State != termproto.RunExited || end.Code == nil || *end.Code != 2 || string(end.Out) != "df-output" || string(end.Err) != "warn" {
		t.Fatalf("结果应照常带回: %+v out=%q err=%q", end, end.Out, end.Err)
	}

	// 内存里的输出仍可通过 run_read 读取。
	rd := newFakeSink()
	m.ServeRead(rd, termproto.RunRead{Req: 9, ID: id, Stream: "stdout", Off: 0, Max: 1024})
	var got []byte
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		rd.mu.Lock()
		frames := append([][]byte(nil), rd.bins...)
		rd.mu.Unlock()
		got = nil
		last := false
		for _, fr := range frames {
			c, err := termproto.DecodeRunData(fr)
			if err != nil {
				t.Fatal(err)
			}
			got = append(got, c.Data...)
			last = c.Flags&termproto.RunFlagLast != 0
		}
		if last {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if string(got) != "df-output" {
		t.Fatalf("应能读到内存里保留的输出，得到 %q", got)
	}

	// 补报：Hub 没确认前它在列表里，确认后消失。
	if un := m.Store().Unreported(10); len(un) != 1 || un[0].ID != id {
		t.Fatalf("没被确认的结束应出现在补报列表里: %+v", un)
	}
	m.Ack(id)
	if un := m.Store().Unreported(10); len(un) != 0 {
		t.Fatalf("确认后不应再补报: %+v", un)
	}
	if !m.Available() {
		t.Fatal("模型执行应仍然可用")
	}
}
