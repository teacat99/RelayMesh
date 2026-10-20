package holder

import (
	"bytes"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/teacat99/RelayMesh/v2/internal/agent/ptyhost"
	"github.com/teacat99/RelayMesh/v2/internal/agent/record"
	"github.com/teacat99/RelayMesh/v2/internal/termproto"
)

type recEnv struct {
	*testEnv
	store *record.Store
}

func newRecEnv(t *testing.T, ro record.Options, lim Limits) *recEnv {
	t.Helper()
	if ro.Dir == "" {
		ro.Dir = t.TempDir()
	}
	ro.MinFree = -1
	ro.NoCompress = true
	ro.FlushEvery = 10 * time.Millisecond
	ro.GCInterval = time.Hour
	store, err := record.Open(ro)
	if err != nil {
		t.Fatal(err)
	}
	spawn := &ptyhost.FakeSpawner{}
	clock := newFakeClock()
	mgr := NewManager(ManagerOptions{
		Spawner: spawn,
		Shell: func(rows, cols uint16) (ptyhost.Spec, error) {
			return ptyhost.Spec{Path: "/bin/fakeshell", Rows: rows, Cols: cols}, nil
		},
		Limits: lim,
		Now:    clock.Now,
		Record: store,
	})
	t.Cleanup(func() {
		mgr.Shutdown(2 * time.Second)
		store.Close()
	})
	return &recEnv{testEnv: &testEnv{mgr: mgr, spawn: spawn, clock: clock}, store: store}
}

func recordedOutput(t *testing.T, st *record.Store, sid string) (string, []record.Event) {
	t.Helper()
	var out bytes.Buffer
	var evs []record.Event
	var seg, off uint32
	for i := 0; i < 1000; i++ {
		pg, err := st.Read(sid, seg, off, record.MaxPageBytes)
		if err != nil {
			t.Fatalf("Read: %v", err)
		}
		es, _, err := record.DecodeEvents(pg.Data)
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range es {
			if e.Kind == record.KindOutput {
				out.Write(e.Payload)
			}
		}
		evs = append(evs, es...)
		if pg.EOF {
			break
		}
		seg, off = pg.NextSeg, pg.NextOff
	}
	return out.String(), evs
}

func TestSessionRecordsDisplayedOutputAndLifecycle(t *testing.T) {
	e := newRecEnv(t, record.Options{}, Limits{})
	s, term := e.open(t)

	if info := s.info(); info.Rec != termproto.RecOn {
		t.Fatalf("Rec = %q, want on", info.Rec)
	}
	term.Emit([]byte("$ echo hi\r\nhi\r\n"))
	s.resize(40, 120)
	term.Emit([]byte("$ "))
	eventually(t, "记录字节数出现在会话信息里", func() bool {
		i := s.info()
		return i.RecBytes > 0 && i.RecLast > 0 && i.RecFirst > 0
	})

	s.rename("renamed")
	term.Exit(ptyhost.ExitStatus{Code: 2})
	eventually(t, "记录已收尾", func() bool {
		info, err := e.store.Info(s.id)
		return err == nil && info.EndedMS > 0
	})
	info, _ := e.store.Info(s.id)
	if info.EndedReason != "exit" || info.ExitCode == nil || *info.ExitCode != 2 || info.Name != "renamed" || info.Shell != "fakeshell" {
		t.Fatalf("record info = %+v", info)
	}
	out, evs := recordedOutput(t, e.store, s.id)
	if out != "$ echo hi\r\nhi\r\n$ " {
		t.Fatalf("recorded output = %q", out)
	}
	sawResize := false
	for _, ev := range evs {
		if ev.Kind == record.KindResize {
			r, c, _ := record.DecodeResize(ev.Payload)
			sawResize = sawResize || (r == 40 && c == 120)
		}
	}
	if !sawResize {
		t.Fatal("resize not recorded")
	}
}

func TestIncognitoVetoAndPolicyOffLeaveNoRecord(t *testing.T) {
	e := newRecEnv(t, record.Options{}, Limits{})
	info, err := e.mgr.OpenWith(OpenOptions{Name: "private", Rows: 24, Cols: 80, Incognito: true})
	if err != nil {
		t.Fatal(err)
	}
	if info.Rec != termproto.RecIncognito {
		t.Fatalf("incognito Rec = %q", info.Rec)
	}
	term := e.spawn.Terminals()[0]
	term.Emit([]byte("top secret\r\n"))
	eventually(t, "输出进入缓冲", func() bool { return e.mgr.Get(info.SID).info().End > 0 })
	if _, err := e.store.Info(info.SID); err == nil {
		t.Fatal("incognito session must leave no record")
	}

	e.mgr.ApplyPolicy(termproto.Policy{Terminal: true, RecOff: true})
	off, err := e.mgr.Open("after-policy", 24, 80)
	if err != nil {
		t.Fatal(err)
	}
	if off.Rec != termproto.RecOff {
		t.Fatalf("policy-off Rec = %q", off.Rec)
	}
	if _, err := e.store.Info(off.SID); err == nil {
		t.Fatal("session opened under policy off must leave no record")
	}

	v := newRecEnv(t, record.Options{Disabled: true}, Limits{})
	vinfo, err := v.mgr.Open("vetoed", 24, 80)
	if err != nil {
		t.Fatal(err)
	}
	if vinfo.Rec != termproto.RecVeto {
		t.Fatalf("veto Rec = %q", vinfo.Rec)
	}
	// 服务端无法覆盖本机否决：策略里不关闭录制，也不会把它打开。
	v.mgr.ApplyPolicy(termproto.Policy{Terminal: true})
	again, _ := v.mgr.Open("vetoed-again", 24, 80)
	if again.Rec != termproto.RecVeto {
		t.Fatalf("veto must survive policy updates, Rec = %q", again.Rec)
	}
	if got := v.store.List(); len(got) != 0 {
		t.Fatalf("veto left %d records", len(got))
	}
}

func TestRecordEndReasons(t *testing.T) {
	e := newRecEnv(t, record.Options{}, Limits{DetachedTTL: time.Minute})

	endReason := func(sid string) string {
		t.Helper()
		var reason string
		eventually(t, "记录结束", func() bool {
			info, err := e.store.Info(sid)
			if err != nil || info.EndedMS == 0 {
				return false
			}
			reason = info.EndedReason
			return true
		})
		return reason
	}

	// 用户 kill
	s1, _ := e.open(t)
	s1.kill()
	if got := endReason(s1.id); got != "killed" {
		t.Fatalf("kill → %q", got)
	}

	// 无人附着超过保留期限
	s2, _ := e.open(t)
	e.clock.Advance(2 * time.Minute)
	e.mgr.tick(e.clock.Now())
	if got := endReason(s2.id); got != "idle_ttl" {
		t.Fatalf("ttl → %q", got)
	}

	// 终端总闸关闭
	s3, _ := e.open(t)
	e.mgr.ApplyPolicy(termproto.Policy{Terminal: false, Reason: "gate"})
	if got := endReason(s3.id); got != "policy" {
		t.Fatalf("policy → %q", got)
	}

	// 守护进程被停止
	e.mgr.ApplyPolicy(termproto.Policy{Terminal: true})
	s4, _ := e.open(t)
	e.mgr.Shutdown(2 * time.Second)
	if got := endReason(s4.id); got != "holder_stopped" {
		t.Fatalf("shutdown → %q", got)
	}
}

// 真实 PTY：键入的命令因 shell / 内核的回显而出现在记录里，关闭回显的口令输入不会出现。
// 记录的是「终端上显示什么」，没有独立的按键流，也不做口令屏蔽（AC-P14）。
func TestRealPTYRecordsEchoedInputButNotHiddenPassword(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("使用 POSIX shell 与 stty")
	}
	const script = `printf 'user@host:~$ '
IFS= read -r cmd
printf 'ran:%s\r\n' "$cmd"
stty -echo
printf '[sudo] password for user: '
IFS= read -r pw
stty echo
printf '\r\nlen:%s\r\n' "${#pw}"
`
	store, err := record.Open(record.Options{
		Dir: t.TempDir(), MinFree: -1, NoCompress: true, FlushEvery: 10 * time.Millisecond, GCInterval: time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	mgr := NewManager(ManagerOptions{
		Spawner: ptyhost.NewSpawner(),
		Shell: func(rows, cols uint16) (ptyhost.Spec, error) {
			return ptyhost.Spec{Path: "/bin/sh", Args: []string{"-c", script}, Rows: rows, Cols: cols}, nil
		},
		Record: store,
	})
	defer mgr.Shutdown(3 * time.Second)

	info, err := mgr.Open("pty", 24, 80)
	if err != nil {
		t.Fatal(err)
	}
	s := mgr.Get(info.SID)
	waitRecorded := func(substr string) {
		t.Helper()
		eventually(t, "记录里出现 "+substr, func() bool {
			out, _ := recordedOutput(t, store, info.SID)
			return strings.Contains(out, substr)
		})
	}

	waitRecorded("user@host:~$ ")
	s.writeInput([]byte("ls -la /tmp\r"))
	waitRecorded("ran:ls -la /tmp")
	waitRecorded("password for user: ")

	const secret = "S3cr3t-Passw0rd!"
	s.writeInput([]byte(secret + "\r"))
	waitRecorded("len:16")

	eventually(t, "记录已收尾", func() bool {
		i, err := store.Info(info.SID)
		return err == nil && i.EndedMS > 0
	})
	out, _ := recordedOutput(t, store, info.SID)
	if !strings.Contains(out, "ls -la /tmp") {
		t.Fatalf("typed command must appear (it is displayed via echo): %q", out)
	}
	if strings.Contains(out, secret) || strings.Contains(out, "S3cr3t") {
		t.Fatalf("hidden password leaked into the record: %q", out)
	}
	// 没有回显的口令不出现，但终端上显示的一切都在：命令的回显（输入的 ls -la /tmp）与 shell 的输出各出现一次以上。
	if strings.Count(out, "ls -la /tmp") < 2 {
		t.Fatalf("expected echo and shell output of the command: %q", out)
	}
}

// 服务端下发的行数上限（Policy.RecMaxLines）要进到记录引擎；本机配置的上限不会被更松的策略放宽。
func TestApplyPolicyForwardsRecordLineCap(t *testing.T) {
	e := newRecEnv(t, record.Options{}, Limits{})
	if got := e.store.SessionLineLimit(); got != 0 {
		t.Fatalf("默认应不限制: %d", got)
	}
	e.mgr.ApplyPolicy(termproto.Policy{Terminal: true, RecMaxLines: 12345})
	if got := e.store.SessionLineLimit(); got != 12345 {
		t.Fatalf("策略里的行数上限应生效: %d", got)
	}
	e.mgr.ApplyPolicy(termproto.Policy{Terminal: true})
	if got := e.store.SessionLineLimit(); got != 0 {
		t.Fatalf("撤销后应回到不限制: %d", got)
	}

	local := newRecEnv(t, record.Options{SessionLines: 8000}, Limits{})
	local.mgr.ApplyPolicy(termproto.Policy{Terminal: true, RecMaxLines: 50000})
	if got := local.store.SessionLineLimit(); got != 8000 {
		t.Fatalf("更松的策略不能放宽本机上限: %d", got)
	}
	local.mgr.ApplyPolicy(termproto.Policy{Terminal: true, RecMaxLines: 6000})
	if got := local.store.SessionLineLimit(); got != 6000 {
		t.Fatalf("更严的策略应生效: %d", got)
	}
}

func TestDefaultCapsDeclareRecordLineCap(t *testing.T) {
	if !HasCap(DefaultCaps(), CapRecLines) {
		t.Fatalf("守护进程应声明 %q 能力: %v", CapRecLines, DefaultCaps())
	}
}
