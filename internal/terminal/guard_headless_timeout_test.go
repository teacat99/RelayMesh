package terminal

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/teacat99/RelayMesh/v2/internal/model"
	"github.com/teacat99/RelayMesh/v2/internal/store"
	"github.com/teacat99/RelayMesh/v2/internal/termproto"
)

// 非交互运行的超时不能回到「几十分钟」：cursor-cli 续聊是一个会话，到点强制结束会把正在干活的会话打断（#113）。
// 同时锁住两处由它派生的阈值：运行监视的寿命不能短于运行本身，「还在跑」的兜底阈值要随它一起变。
func TestHeadlessTimeoutIsLongEnoughForASession(t *testing.T) {
	if headlessTimeoutSec < 24*3600 {
		t.Fatalf("非交互运行的超时过短：%d 秒（会话会在到点时被强制结束）", headlessTimeoutSec)
	}
	if headlessTimeoutSec > termproto.MaxRunTimeoutSec {
		t.Fatalf("非交互运行的超时 %d 秒超过平台单次运行上限 %d 秒，会被收进上限", headlessTimeoutSec, termproto.MaxRunTimeoutSec)
	}
	run := time.Duration(headlessTimeoutSec) * time.Second
	if got := defaultRunWatchTiming().MaxAge; got < run {
		t.Fatalf("运行监视的寿命 %v 短于运行本身的超时 %v：运行还没结束就停止监视了", got, run)
	}
	if want := run + 5*time.Minute; launcherRunStale != want {
		t.Fatalf("launcherRunStale 应为运行超时加 5 分钟：got %v want %v", launcherRunStale, want)
	}
}

// 预设「最长运行时间」的上限与平台单次运行上限同值：store 不依赖协议包，靠这条守住两边不漂移（#141）。
func TestHeadlessRunMinutesCapMatchesThePlatformCap(t *testing.T) {
	if got := store.MaxHeadlessRunMinutes * 60; got != termproto.MaxRunTimeoutSec {
		t.Fatalf("预设最长运行时间上限 %d 分钟 = %d 秒，应与平台单次运行上限 %d 秒一致", store.MaxHeadlessRunMinutes, got, termproto.MaxRunTimeoutSec)
	}
	if got := (store.HeadlessRun{}).EffectiveMaxRunSeconds(); got != headlessTimeoutSec {
		t.Fatalf("没设最长运行时间时应用默认超时 %d 秒，得到 %d", headlessTimeoutSec, got)
	}
}

// 提交时的超时：没给（0 / 负数）用默认（平台上限），给了就用，超过上限的收进上限。
func TestHeadlessSubmitTimeoutDefaultsAndClamps(t *testing.T) {
	for in, want := range map[int]int{
		0:                      headlessTimeoutSec,
		-5:                     headlessTimeoutSec,
		1:                      1,
		90:                     90,
		1800:                   1800,
		headlessTimeoutSec:     headlessTimeoutSec,
		headlessTimeoutSec + 1: headlessTimeoutSec,
	} {
		if got := (headlessSubmit{TimeoutSec: in}).timeout(); got != want {
			t.Errorf("TimeoutSec=%d：实际超时 = %d，期望 %d", in, got, want)
		}
	}
}

// 「还在跑」的兜底阈值按每次运行自己的超时算（#141）：30 分钟的运行残留后不会像 7 天的那样挡一周；
// 从运行真正开始的时刻算（待批准的从创建算，并且再加上等批准的最长时间）；终态永远不算；没有超时值的旧记录用兜底阈值。
func TestRunActiveFollowsEachRunsOwnTimeout(t *testing.T) {
	now := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	ago := func(d time.Duration) time.Time { return now.Add(-d) }
	ptr := func(tm time.Time) *time.Time { return &tm }
	minute := time.Minute
	for _, c := range []struct {
		name string
		rec  model.TerminalRun
		want bool
	}{
		{"30 分钟的运行，开始 20 分钟：还在跑", model.TerminalRun{State: model.RunRunning, TimeoutSec: 1800, CreatedAt: ago(20 * minute), StartedAt: ptr(ago(20 * minute))}, true},
		{"30 分钟的运行，开始 34 分钟（还在宽限内）：还在跑", model.TerminalRun{State: model.RunRunning, TimeoutSec: 1800, CreatedAt: ago(34 * minute), StartedAt: ptr(ago(34 * minute))}, true},
		{"30 分钟的运行，开始 36 分钟（超过超时 + 5 分钟宽限）：残留，不算", model.TerminalRun{State: model.RunRunning, TimeoutSec: 1800, CreatedAt: ago(36 * minute), StartedAt: ptr(ago(36 * minute))}, false},
		{"7 天的运行，开始 6 天：还在跑", model.TerminalRun{State: model.RunRunning, TimeoutSec: headlessTimeoutSec, CreatedAt: ago(6 * 24 * time.Hour), StartedAt: ptr(ago(6 * 24 * time.Hour))}, true},
		{"7 天的运行，开始 7 天 6 分钟：残留，不算", model.TerminalRun{State: model.RunRunning, TimeoutSec: headlessTimeoutSec, CreatedAt: ago(7*24*time.Hour + 6*minute), StartedAt: ptr(ago(7*24*time.Hour + 6*minute))}, false},
		{"待批准（没开始）从创建时算：10 分钟前创建的 30 分钟运行还在", model.TerminalRun{State: model.RunAwaitingApproval, TimeoutSec: 1800, CreatedAt: ago(10 * minute)}, true},
		// 等批准最长 execApprovalTTL（10 分钟），批准之后才开始计超时：1 分钟的运行在批准前不能被当成残留，
		// 否则等了 8 分钟的第二次点击会再提交一个（#141 里发现的、短超时带来的新情形）。
		{"待批准的 1 分钟运行，创建 8 分钟：还在等批准，不算残留", model.TerminalRun{State: model.RunAwaitingApproval, TimeoutSec: 60, CreatedAt: ago(8 * minute)}, true},
		{"待批准的 1 分钟运行，创建 15 分钟（等批准 10 + 超时 1 + 宽限 5 = 16 分钟内）：还在", model.TerminalRun{State: model.RunAwaitingApproval, TimeoutSec: 60, CreatedAt: ago(15 * minute)}, true},
		{"待批准的 1 分钟运行，创建 17 分钟：残留", model.TerminalRun{State: model.RunAwaitingApproval, TimeoutSec: 60, CreatedAt: ago(17 * minute)}, false},
		{"待批准（没开始）超过等批准 + 超时 + 宽限：残留", model.TerminalRun{State: model.RunAwaitingApproval, TimeoutSec: 1800, CreatedAt: ago(46 * minute)}, false},
		// 批准之后、主机报「已开始」之前：从批准那刻算（创建早就过了 1 分钟 + 宽限，但批准才 1 分钟）。
		{"1 分钟运行，创建 9 分钟前、1 分钟前刚批准、主机还没报开始：还在", model.TerminalRun{State: model.RunRunning, TimeoutSec: 60, CreatedAt: ago(9 * minute), ApprovedAt: ptr(ago(minute))}, true},
		{"1 分钟运行，批准 7 分钟前、主机一直没报开始：残留", model.TerminalRun{State: model.RunRunning, TimeoutSec: 60, CreatedAt: ago(9 * minute), ApprovedAt: ptr(ago(7 * minute))}, false},
		{"待批准一阵后才开始：从开始时算（创建 38 分钟前、8 分钟前才批准开始），还在跑", model.TerminalRun{State: model.RunRunning, TimeoutSec: 1800, CreatedAt: ago(38 * minute), StartedAt: ptr(ago(8 * minute))}, true},
		{"终态永远不算进行中", model.TerminalRun{State: model.RunTimedOut, TimeoutSec: 1800, CreatedAt: ago(1 * minute), StartedAt: ptr(ago(1 * minute))}, false},
		{"没有超时值的旧记录：6 天 23 小时仍在兜底阈值内", model.TerminalRun{State: model.RunRunning, CreatedAt: ago(7*24*time.Hour - time.Hour)}, true},
		{"没有超时值的旧记录：超过兜底阈值", model.TerminalRun{State: model.RunRunning, CreatedAt: ago(launcherRunStale + minute)}, false},
		{"没有超时值、有开始时间的旧记录：从开始时算，超过兜底阈值", model.TerminalRun{State: model.RunRunning, CreatedAt: ago(launcherRunStale + 2*minute), StartedAt: ptr(ago(launcherRunStale + minute))}, false},
	} {
		if got := runActive(c.rec, now); got != c.want {
			t.Errorf("%s：runActive = %v，期望 %v", c.name, got, c.want)
		}
	}
	if got, want := runStaleAfter(model.TerminalRun{TimeoutSec: 600}), 10*time.Minute+launcherRunGrace; got != want {
		t.Errorf("有超时值的记录：runStaleAfter = %v，期望 %v", got, want)
	}
	if got := runStaleAfter(model.TerminalRun{}); got != launcherRunStale {
		t.Errorf("没有超时值的记录应用兜底阈值：%v", got)
	}
}

// 监视的寿命：运行有自己的超时就按它加余量（不超过全局 MaxAge），没有就用 MaxAge。
func TestRunWatchLifetimeFollowsTheRunsOwnTimeout(t *testing.T) {
	d := defaultRunWatchTiming()
	for name, c := range map[string]struct{ timeout, want time.Duration }{
		"30 分钟的运行":          {30 * time.Minute, 30*time.Minute + d.Slack},
		"1 分钟的运行":           {time.Minute, time.Minute + d.Slack},
		"7 天的运行（默认）":        {time.Duration(headlessTimeoutSec) * time.Second, d.MaxAge},
		"没有超时信息（旧记录）":       {0, d.MaxAge},
		"超过平台上限的值收进 MaxAge": {30 * 24 * time.Hour, d.MaxAge},
	} {
		if got := d.lifetime(c.timeout); got != c.want {
			t.Errorf("%s：lifetime(%v) = %v，期望 %v", name, c.timeout, got, c.want)
		}
	}
	// 余量要盖得住等批准：超时很短的运行在「需批准」的主机上要先等批准（最长 execApprovalTTL），监视不能在那之前就撤掉。
	if d.Slack < execApprovalTTL+time.Minute {
		t.Errorf("监视余量 %v 盖不住等批准（%v）加收尾", d.Slack, execApprovalTTL)
	}
	if d.MaxAge < time.Duration(headlessTimeoutSec)*time.Second {
		t.Fatalf("默认 MaxAge %v 短于默认运行超时", d.MaxAge)
	}
}

// 监视协程真的按运行自己的超时收尾：运行没有终态事件时（事件丢了），超时 + 余量之后协程退出，不会留到全局 MaxAge。
func TestRunWatchStopsAtTheRunsOwnTimeoutPlusSlack(t *testing.T) {
	f := watchFixture(t)
	f.svc.watch.timing.MaxAge = time.Hour // 全局寿命很长：能让监视退出的只有这次运行自己的超时
	f.svc.watch.timing.Slack = 50 * time.Millisecond
	f.sim.onRun = func(s *hostSim, m termproto.Run) { s.started(m.ID) }

	cmd, err := headlessCmd{Bin: "agent", ChatID: watchConv, Dir: "/work/app", Posture: store.HeadlessPostureForce, Prompt: "x"}.build()
	if err != nil {
		t.Fatal(err)
	}
	res, err := f.svc.submitHeadless(context.Background(), headlessSubmit{
		Caller: launcherRunCaller, UserAgent: "test", Machine: f.a.code, Workflow: watchWF, Conversation: watchConv,
		Cwd: "/work/app", Command: cmd, Description: "limit", TimeoutSec: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if rec := f.dbRun(t, res.RunID); rec.TimeoutSec != 1 {
		t.Fatalf("运行记录应带这次运行自己的超时：%d", rec.TimeoutSec)
	}
	if !f.svc.watch.watching(res.RunID) {
		t.Fatal("提交后应当开始监视")
	}
	f.svc.watch.mu.Lock()
	got := f.svc.watch.by[res.RunID].t.Timeout
	f.svc.watch.mu.Unlock()
	if got != time.Second {
		t.Fatalf("监视目标应带运行自己的超时：%v", got)
	}
	eventually(t, 3*time.Second, "监视在 1 秒超时 + 余量后退出", func() bool { return !f.svc.watch.watching(res.RunID) })
}

// Hub 重启后接回的监视同样带着运行自己的超时（来自运行记录）。
func TestRunWatchRestoreCarriesTheRunsOwnTimeout(t *testing.T) {
	f := watchFixture(t)
	f.sim.onRun = func(s *hostSim, m termproto.Run) { s.started(m.ID) }
	cmd, _ := headlessCmd{Bin: "agent", ChatID: watchConv, Dir: "/work/app", Posture: store.HeadlessPostureForce, Prompt: "x"}.build()
	// 不给 Conversation：提交时不登记监视，模拟「Hub 刚重启、监视还没接回」。
	res, err := f.svc.submitHeadless(context.Background(), headlessSubmit{
		Caller: launcherRunCaller, UserAgent: "test", Machine: f.a.code, Workflow: watchWF,
		Cwd: "/work/app", Command: cmd, Description: "restore-limit", TimeoutSec: 1800,
	})
	if err != nil {
		t.Fatal(err)
	}
	rec := f.dbRun(t, res.RunID)
	if rec.TimeoutSec != 1800 || model.IsFinalRunState(rec.State) {
		t.Fatalf("运行记录应带 30 分钟超时且仍在进行：%+v", rec)
	}
	if f.svc.watch.watching(res.RunID) {
		t.Fatal("没给对话编号时提交不该登记监视")
	}

	f.svc.watch.restore(*rec)
	f.svc.watch.mu.Lock()
	rw := f.svc.watch.by[res.RunID]
	var got time.Duration
	if rw != nil {
		got = rw.t.Timeout
	}
	f.svc.watch.mu.Unlock()
	if rw == nil {
		t.Fatal("接回应当登记监视")
	}
	if got != 1800*time.Second {
		t.Fatalf("接回的监视应带记录里的超时 30 分钟：%v", got)
	}
	f.sim.endFull(termproto.RunEnd{ID: res.RunID, State: termproto.RunExited, Code: intp(0), DurMS: 10})
	eventually(t, 3*time.Second, "运行结束后监视收尾", func() bool { return !f.svc.watch.watching(res.RunID) })
}

func TestRunLimitText(t *testing.T) {
	for in, want := range map[int]string{
		0:                          "未设置",
		-1:                         "未设置",
		30:                         "30 秒",
		60:                         "1 分钟",
		90:                         "1 分 30 秒",
		1800:                       "30 分钟",
		3600:                       "1 小时",
		5400:                       "90 分钟",
		2 * 3600:                   "2 小时",
		24 * 3600:                  "1 天",
		36 * 3600:                  "36 小时",
		termproto.MaxRunTimeoutSec: "7 天",
	} {
		if got := runLimitText(in); got != want {
			t.Errorf("runLimitText(%d) = %q，期望 %q", in, got, want)
		}
	}
}

// 默认提示词后面补一句本次运行的最长时间；自定义提示词一个字不加；没有时限（<=0）也不加。
func TestRenderLauncherPromptForAddsTheLimitNoteToTheDefaultOnly(t *testing.T) {
	base := renderLauncherPrompt("", "wf-1")
	got := renderLauncherPromptFor("", "wf-1", 1800)
	if !strings.HasPrefix(got, base+" ") || !strings.HasSuffix(got, "本次运行最长 30 分钟，到点会被强制结束：请在那之前先向 RelayMesh 汇报进度，没做完的写清下一步。") {
		t.Fatalf("默认提示词应补时限提醒：%q", got)
	}
	if strings.ContainsAny(got, "\r\n") || strings.Contains(got, "%workflow") {
		t.Fatalf("提示词不该有换行或占位符：%q", got)
	}
	if def := renderLauncherPromptFor("   ", "wf-1", headlessTimeoutSec); !strings.Contains(def, "本次运行最长 7 天") {
		t.Fatalf("没设时限的默认提示词也写明平台上限：%q", def)
	}
	if custom := renderLauncherPromptFor("  看看 %workflow 的收件箱  ", "wf-2", 1800); custom != "看看 wf-2 的收件箱" {
		t.Fatalf("自定义提示词不该被加东西：%q", custom)
	}
	if none := renderLauncherPromptFor("", "wf-1", 0); none != base {
		t.Fatalf("没有时限信息就不补：%q", none)
	}
	// 经命令拼装器后仍是单引号包裹的一个参数，提醒里的标点不会破坏它。
	cmd, err := headlessCmd{Bin: "agent", ChatID: "c1", Dir: "/w", Prompt: got}.build()
	if err != nil || !strings.HasSuffix(cmd, "没做完的写清下一步。'") {
		t.Fatalf("命令拼装不对：%v %s", err, cmd)
	}
}
