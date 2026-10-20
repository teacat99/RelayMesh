package terminal

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/teacat99/RelayMesh/v2/internal/model"
	"github.com/teacat99/RelayMesh/v2/internal/termproto"
)

// 模型运行结束的通知挂钩（给 ntfy 之类的外部提醒用）。
//
// 终端这一侧只报告事实：每次终结恰好一条，带上谁要求过取消、终态原因的中文说明；
// 「要不要推送」是接收方按自己的配置决定的，这里不做判断。

type endEvents struct {
	mu  sync.Mutex
	got []RunEndEvent
}

func (e *endEvents) hook(ev RunEndEvent) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.got = append(e.got, ev)
}

func (e *endEvents) count() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return len(e.got)
}

func (e *endEvents) all() []RunEndEvent {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]RunEndEvent(nil), e.got...)
}

func (e *endEvents) only(t *testing.T) RunEndEvent {
	t.Helper()
	eventually(t, 2*time.Second, "收到运行结束事件", func() bool { return e.count() >= 1 })
	time.Sleep(100 * time.Millisecond) // 给「多发了一条」留出暴露的时间
	got := e.all()
	if len(got) != 1 {
		t.Fatalf("每次终结应恰好通知一次，实际 %d 次：%+v", len(got), got)
	}
	return got[0]
}

func longEnd(id, state string, code *int, reason string) termproto.RunEnd {
	return termproto.RunEnd{ID: id, State: state, Code: code, Reason: reason, DurMS: 90_000}
}

func TestRunEndHookReportsFinishedRun(t *testing.T) {
	f := newExecFixture(t)
	ev := &endEvents{}
	f.env.hub.SetRunEndHook(ev.hook)
	f.sim.onRun = func(s *hostSim, m termproto.Run) {
		s.started(m.ID)
		s.endFull(longEnd(m.ID, termproto.RunExited, intp(0), ""))
	}

	req := waitSet(5)
	req.Target, req.Command, req.Description, req.Thread = "build box", "make build", "全量构建", "demo"
	res, err := f.exec(t, f.caller(nil), req)
	if err != nil {
		t.Fatal(err)
	}

	got := ev.only(t)
	if got.Run.RunID != res.RunID || got.Run.State != model.RunSucceeded {
		t.Fatalf("事件指向了别的运行或状态不对：%+v", got.Run)
	}
	if got.Run.Target != f.a.code || got.Run.Description != "全量构建" || got.Run.Command != "make build" {
		t.Errorf("事件应带上主机、说明、命令：%+v", got.Run)
	}
	if got.Run.DurationMS != 90_000 || got.Run.ExitCode == nil || *got.Run.ExitCode != 0 {
		t.Errorf("事件应带上时长与退出码：%+v", got.Run)
	}
	if got.CancelBy != "" || got.Why != "" {
		t.Errorf("正常结束没有取消来源，也没有原因说明：%+v", got)
	}
}

func TestRunEndHookReportsFailureAndTimeout(t *testing.T) {
	f := newExecFixture(t)
	ev := &endEvents{}
	f.env.hub.SetRunEndHook(ev.hook)
	f.sim.onRun = func(s *hostSim, m termproto.Run) {
		s.started(m.ID)
		if m.Cmd == "false" {
			s.endFull(longEnd(m.ID, termproto.RunExited, intp(2), ""))
			return
		}
		s.endFull(longEnd(m.ID, termproto.RunTimeout, nil, ""))
	}

	for _, cmd := range []string{"false", "sleep 99999"} {
		req := waitSet(5)
		req.Target, req.Command, req.Thread = "build box", cmd, "t-"+cmd[:3]
		if _, err := f.exec(t, f.caller(nil), req); err != nil {
			t.Fatal(err)
		}
	}
	eventually(t, 2*time.Second, "两条事件", func() bool { return ev.count() == 2 })
	byCmd := map[string]RunEndEvent{}
	for _, e := range ev.all() {
		byCmd[e.Run.Command] = e
	}
	if e := byCmd["false"]; e.Run.State != model.RunFailed || e.Run.ExitCode == nil || *e.Run.ExitCode != 2 {
		t.Errorf("失败的运行应带退出码：%+v", e.Run)
	}
	if e := byCmd["sleep 99999"]; e.Run.State != model.RunTimedOut {
		t.Errorf("超时的运行状态不对：%+v", e.Run)
	}
}

// 你（在网页上）取消的运行：事件里要写明取消来源，接收方据此决定不再打扰你。
func TestRunEndHookCarriesWhoCancelled(t *testing.T) {
	f := newExecFixture(t)
	ev := &endEvents{}
	f.env.hub.SetRunEndHook(ev.hook)
	f.sim.onRun = func(s *hostSim, m termproto.Run) { s.started(m.ID) } // 一直在跑

	req := waitSet(1)
	req.Target, req.Command, req.Thread = "build box", "sleep 600", "demo"
	res, err := f.exec(t, f.caller(nil), req)
	if err != nil {
		t.Fatal(err)
	}
	if res.State != model.RunRunning {
		t.Fatalf("应还在运行：%+v", res)
	}
	if got := ev.count(); got != 0 {
		t.Fatalf("还没结束，不应通知：%d", got)
	}

	if _, err := f.svc.CancelByHuman(context.Background(), res.RunID, "admin", "127.0.0.1"); err != nil {
		t.Fatal(err)
	}
	eventually(t, 2*time.Second, "主机收到取消请求", func() bool { return f.sim.cancelCount() == 1 })
	f.sim.endFull(longEnd(res.RunID, termproto.RunCancelled, nil, ""))

	got := ev.only(t)
	if got.Run.State != model.RunCancelled || got.CancelBy != "user" {
		t.Fatalf("应报告「被用户取消」：%+v", got)
	}
}

// 没人要求取消、却被中断（主机上的守护进程停了）：取消来源为空，并带上原因的中文说明。
func TestRunEndHookHostSideInterruptHasNoCancelSource(t *testing.T) {
	f := newExecFixture(t)
	ev := &endEvents{}
	f.env.hub.SetRunEndHook(ev.hook)
	f.sim.onRun = func(s *hostSim, m termproto.Run) { s.started(m.ID) }

	req := waitSet(1)
	req.Target, req.Command, req.Thread = "build box", "sleep 600", "demo"
	res, err := f.exec(t, f.caller(nil), req)
	if err != nil {
		t.Fatal(err)
	}
	f.sim.endFull(longEnd(res.RunID, termproto.RunCancelled, nil, "holder_stopped"))

	got := ev.only(t)
	if got.Run.State != model.RunCancelled || got.CancelBy != "" {
		t.Fatalf("主机侧中断没有取消来源：%+v", got)
	}
	if want := failMessage("holder_stopped"); want == "" || got.Why != want {
		t.Errorf("应带上原因的中文说明 %q，实际 %q", want, got.Why)
	}
}

// 主机在连接中断后重发结束事件，或同一个结束事件被处理两次：只通知一次。
func TestRunEndHookFiresOnceEvenIfEndIsReplayed(t *testing.T) {
	f := newExecFixture(t)
	ev := &endEvents{}
	f.env.hub.SetRunEndHook(ev.hook)
	f.sim.onRun = func(s *hostSim, m termproto.Run) {
		s.started(m.ID)
		s.endFull(longEnd(m.ID, termproto.RunExited, intp(0), ""))
	}
	req := waitSet(5)
	req.Target, req.Command, req.Thread = "build box", "make", "demo"
	res, err := f.exec(t, f.caller(nil), req)
	if err != nil {
		t.Fatal(err)
	}
	ev.only(t)

	f.sim.endFull(longEnd(res.RunID, termproto.RunExited, intp(0), "")) // 重放
	f.sim.endFull(longEnd(res.RunID, termproto.RunLost, nil, "host_reboot"))
	time.Sleep(200 * time.Millisecond)
	if got := ev.count(); got != 1 {
		t.Fatalf("重放的结束事件不应再次通知，实际共 %d 次", got)
	}
}

// 挂钩出了任何岔子，运行的收尾都不受影响：状态照常落库、结果照常返回，下一次运行也照常通知。
func TestRunEndHookPanicDoesNotBreakFinalize(t *testing.T) {
	f := newExecFixture(t)
	var calls int
	var mu sync.Mutex
	f.env.hub.SetRunEndHook(func(RunEndEvent) {
		mu.Lock()
		calls++
		mu.Unlock()
		panic("挂钩坏了")
	})
	f.completeWith("ok\n")

	for i := 0; i < 2; i++ {
		req := waitSet(5)
		req.Target, req.Command, req.Thread = "build box", "echo ok", "demo"
		res, err := f.exec(t, f.caller(nil), req)
		if err != nil {
			t.Fatalf("挂钩 panic 不应让 exec 失败：%v", err)
		}
		if res.State != model.RunSucceeded {
			t.Fatalf("结果状态不对：%+v", res)
		}
		if rec := f.dbRun(t, res.RunID); rec.State != model.RunSucceeded || rec.EndedAt == nil {
			t.Fatalf("终态应已落库：%+v", rec)
		}
		if f.svc.liveRun(res.RunID) != nil {
			t.Fatalf("终态后不应留在活动索引里")
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if calls != 2 {
		t.Fatalf("两次运行都应调用挂钩，实际 %d 次", calls)
	}
}

// 挂钩里回头读库、查活动索引不能死锁：终结时不能一边持有运行 / 服务的锁一边调用挂钩。
func TestRunEndHookMayCallBackIntoService(t *testing.T) {
	f := newExecFixture(t)
	done := make(chan struct{})
	f.env.hub.SetRunEndHook(func(ev RunEndEvent) {
		defer close(done)
		_ = f.svc.liveRun(ev.Run.RunID)
		_, _ = f.env.store.GetTerminalRun(context.Background(), ev.Run.RunID)
		f.svc.snapshotLive()
	})
	f.completeWith("ok\n")

	req := waitSet(5)
	req.Target, req.Command, req.Thread = "build box", "echo ok", "demo"
	if _, err := f.exec(t, f.caller(nil), req); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("挂钩里回调服务卡住了（疑似在持锁时调用挂钩）")
	}
}

// 没有接线（默认）与摘掉挂钩（传 nil）都是合法的。
func TestRunEndHookNilIsHarmless(t *testing.T) {
	f := newExecFixture(t)
	f.completeWith("ok\n")
	ev := &endEvents{}
	f.env.hub.SetRunEndHook(ev.hook)
	f.env.hub.SetRunEndHook(nil)

	req := waitSet(5)
	req.Target, req.Command, req.Thread = "build box", "echo ok", "demo"
	if _, err := f.exec(t, f.caller(nil), req); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	if ev.count() != 0 {
		t.Fatal("摘掉挂钩后不应再通知")
	}
	var nilHub *Hub
	nilHub.SetRunEndHook(ev.hook) // 不能 panic
}

func TestHostDisplayNameExported(t *testing.T) {
	if got := HostDisplayName(model.TerminalHost{Remark: "构建机", Hostname: "ip-10-0-0-5"}); got != "构建机" {
		t.Errorf("有备注时用备注：%q", got)
	}
	if got := HostDisplayName(model.TerminalHost{Hostname: "ip-10-0-0-5"}); got != "ip-10-0-0-5" {
		t.Errorf("没有备注时用主机名：%q", got)
	}
}
