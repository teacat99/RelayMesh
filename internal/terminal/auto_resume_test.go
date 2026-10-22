package terminal

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/teacat99/RelayMesh/v2/internal/agentstate"
	"github.com/teacat99/RelayMesh/v2/internal/model"
	"github.com/teacat99/RelayMesh/v2/internal/store"
	"github.com/teacat99/RelayMesh/v2/internal/termproto"
)

// ---------- 纯函数 ----------

func TestAutoResumeDelayWalksTheBackoffAndSticksToTheLast(t *testing.T) {
	b := []time.Duration{5 * time.Minute, 20 * time.Minute, time.Hour}
	for used, want := range map[int]time.Duration{-1: 5 * time.Minute, 0: 5 * time.Minute, 1: 20 * time.Minute, 2: time.Hour, 3: time.Hour, 9: time.Hour} {
		if got := autoResumeDelay(b, used); got != want {
			t.Errorf("已续跑 %d 次：等 %v，应是 %v", used, got, want)
		}
	}
	if got := autoResumeDelay(nil, 1); got != 20*time.Minute {
		t.Errorf("没配退避用默认：%v", got)
	}
}

func TestParseAutoResumeBackoffKeepsOnlySaneItems(t *testing.T) {
	cases := map[string][]time.Duration{
		"3,6,9":         {3 * time.Second, 6 * time.Second, 9 * time.Second},
		" 2 , x ,0,-4 ": {2 * time.Second},
		"86401":         nil,
		"86400":         {24 * time.Hour},
		"":              nil,
		"abc":           nil,
	}
	for in, want := range cases {
		got := ParseAutoResumeBackoff(in)
		if len(got) != len(want) {
			t.Errorf("%q：%v，应是 %v", in, got, want)
			continue
		}
		for i := range got {
			if got[i] != want[i] {
				t.Errorf("%q：%v，应是 %v", in, got, want)
			}
		}
	}
}

func TestAutoResumeSuffixRoundTrips(t *testing.T) {
	d := "预设「续聊」的非交互续聊" + autoResumeSuffix(2, 3)
	if a, m := parseAutoResumeSuffix(d); a != 2 || m != 3 {
		t.Fatalf("解析：%d/%d", a, m)
	}
	for _, plain := range []string{"预设「续聊」的非交互续聊", "", "（自动续跑 x/3）", "（自动续跑 1/3）后面还有字"} {
		if a, m := parseAutoResumeSuffix(plain); a != 0 || m != 0 {
			t.Errorf("%q 不是自动续跑出来的：%d/%d", plain, a, m)
		}
	}
}

func failedRec(id string, dur time.Duration) model.TerminalRun {
	return model.TerminalRun{RunID: id, State: model.RunFailed, DurationMS: dur.Milliseconds()}
}

var retriableExit = RunExit{Class: ExitRetriable, Code: "resource_exhausted", Text: "可重试的错误", Retry: true}

func TestPlanAutoResumeSchedulesWithBackoffByUsage(t *testing.T) {
	now := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	b := []time.Duration{5 * time.Minute, 20 * time.Minute, time.Hour}

	ps := &store.GuardPresetState{}
	r, created := planAutoResume(ps, failedRec("run-1", time.Hour), retriableExit, now, b, autoResumeOKDefault)
	if !created || r.State != store.AutoResumePending || r.Attempt != 1 || !r.Due.Equal(now.Add(5*time.Minute)) || r.FromRun != "run-1" || r.Code != "resource_exhausted" {
		t.Fatalf("第一次出事：%+v", r)
	}
	if len(ps.AutoResumes) != 1 {
		t.Fatalf("应登记一条: %+v", ps.AutoResumes)
	}

	// 同一次运行不重复登记。
	if _, again := planAutoResume(ps, failedRec("run-1", time.Hour), retriableExit, now, b, autoResumeOKDefault); again || len(ps.AutoResumes) != 1 {
		t.Fatalf("同一次运行不该重复登记: %+v", ps.AutoResumes)
	}

	// 24 小时内已经续跑过一次（那次续跑还没有被认定成功）：现在出事的是你手动运行的 run-9，第二次排期的退避是 20 分钟，次数是第 2 次。
	ps = &store.GuardPresetState{AutoResumes: []store.AutoResumeRecord{
		{FromRun: "run-0", RunID: "run-1", State: store.AutoResumeStarted, StartedAt: now.Add(-3 * time.Hour)},
	}}
	r, _ = planAutoResume(ps, failedRec("run-9", 3*time.Hour), retriableExit, now, b, autoResumeOKDefault)
	if r.State != store.AutoResumePending || r.Attempt != 2 || !r.Due.Equal(now.Add(20*time.Minute)) {
		t.Fatalf("已续跑一次之后：%+v", r)
	}
	if ps.AutoResumes[0].Quick || ps.AutoResumes[0].Settled {
		t.Fatal("出事的不是那次续跑出来的运行，不该动已有登记")
	}
}

func TestPlanAutoResumeBreaksAfterTwoQuickFailuresInARow(t *testing.T) {
	now := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	b := []time.Duration{5 * time.Minute, 20 * time.Minute, time.Hour}
	ps := &store.GuardPresetState{}

	// 手动运行 run-1 出事 → 排期；续跑出 run-2。
	planAutoResume(ps, failedRec("run-1", time.Hour), retriableExit, now, b, autoResumeOKDefault)
	ps.AutoResumes[0].State, ps.AutoResumes[0].RunID, ps.AutoResumes[0].StartedAt = store.AutoResumeStarted, "run-2", now

	// run-2 一分钟就又失败了：标上没有进展，但只有一次，还可以再续跑一次。
	r, _ := planAutoResume(ps, failedRec("run-2", time.Minute), retriableExit, now, b, autoResumeOKDefault)
	if !ps.AutoResumes[0].Quick || r.State != store.AutoResumePending || r.Attempt != 2 || !r.Due.Equal(now.Add(20*time.Minute)) {
		t.Fatalf("第一次没有进展不该熔断：%+v / %+v", r, ps.AutoResumes[0])
	}
	ps.AutoResumes[1].State, ps.AutoResumes[1].RunID, ps.AutoResumes[1].StartedAt = store.AutoResumeStarted, "run-3", now

	// run-3 也很快失败：连续两次没有进展，熔断。
	r, _ = planAutoResume(ps, failedRec("run-3", 2*time.Minute), retriableExit, now, b, autoResumeOKDefault)
	if r.State != store.AutoResumeGaveUp || !strings.Contains(r.Why, "连续 2 次") || !r.Due.IsZero() {
		t.Fatalf("应熔断: %+v", r)
	}
	// 熔断之后的新一串（比如你手动运行又出事）从头算：登记的末尾不再是「续跑出来的没进展」。
	r, _ = planAutoResume(ps, failedRec("run-9", time.Hour), retriableExit, now, b, autoResumeOKDefault)
	if r.State != store.AutoResumePending {
		t.Fatalf("熔断之后你手动运行又出事，应重新排期（预算另算）: %+v", r)
	}
}

func TestPlanAutoResumeProgressBetweenFailuresKeepsTheChainAlive(t *testing.T) {
	now := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	b := []time.Duration{time.Minute}
	ps := &store.GuardPresetState{AutoResumes: []store.AutoResumeRecord{
		{FromRun: "run-1", RunID: "run-2", State: store.AutoResumeStarted, StartedAt: now.Add(-4 * time.Hour), Quick: true},
		{FromRun: "run-2", RunID: "run-3", State: store.AutoResumeStarted, StartedAt: now.Add(-3 * time.Hour)},
	}}
	// run-3 跑了 40 分钟才失败：有进展，链上最近一次不是 Quick，不熔断。
	r, _ := planAutoResume(ps, failedRec("run-3", 40*time.Minute), retriableExit, now, b, autoResumeOKDefault)
	if r.State != store.AutoResumePending || ps.AutoResumes[1].Quick {
		t.Fatalf("有进展就不该熔断: %+v / %+v", r, ps.AutoResumes)
	}
}

func TestPlanAutoResumeStopsAtTheDailyBudget(t *testing.T) {
	now := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	started := func(from, run string, ago time.Duration) store.AutoResumeRecord {
		return store.AutoResumeRecord{FromRun: from, RunID: run, State: store.AutoResumeStarted, StartedAt: now.Add(-ago)}
	}
	// 24 小时内续跑了 3 次，现在出事的是你手动运行的（不是续跑出来的）：没有哪次续跑被认定成功，预算用完。
	ps := &store.GuardPresetState{AutoResumes: []store.AutoResumeRecord{
		started("a", "b", 20*time.Hour), started("b", "c", 10*time.Hour), started("c", "d", time.Hour),
	}}
	r, _ := planAutoResume(ps, failedRec("manual-1", 3*time.Hour), retriableExit, now, nil, autoResumeOKDefault)
	if r.State != store.AutoResumeGaveUp || !strings.Contains(r.Why, "24 小时内已经自动续跑 3 次") || !strings.Contains(r.Why, "连续跑满 10 分钟，就算成功") {
		t.Fatalf("预算用完应放弃，并说明怎样才算成功: %+v", r)
	}
	// 最早的一次滑出 24 小时窗口之后，名额又有了。
	ps = &store.GuardPresetState{AutoResumes: []store.AutoResumeRecord{
		started("a", "b", 25*time.Hour), started("b", "c", 10*time.Hour), started("c", "d", time.Hour),
	}}
	if r, _ := planAutoResume(ps, failedRec("manual-1", 3*time.Hour), retriableExit, now, nil, autoResumeOKDefault); r.State != store.AutoResumePending || r.Attempt != 3 {
		t.Fatalf("窗口滑过之后应有名额，这是 24 小时内第 3 次: %+v", r)
	}
}

// #172：续跑出来的运行连续跑满判定时长就算成功，24 小时内累计的次数清零——不再被它之前的几次续跑拖累。
func TestPlanAutoResumeSuccessfulResumeClearsTheBudget(t *testing.T) {
	now := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	b := []time.Duration{5 * time.Minute, 20 * time.Minute, time.Hour}
	started := func(from, run string, ago time.Duration) store.AutoResumeRecord {
		return store.AutoResumeRecord{FromRun: from, RunID: run, State: store.AutoResumeStarted, StartedAt: now.Add(-ago)}
	}
	chain := func() *store.GuardPresetState {
		return &store.GuardPresetState{AutoResumes: []store.AutoResumeRecord{
			started("a", "b", 20*time.Hour), started("b", "c", 10*time.Hour), started("c", "d", time.Hour),
		}}
	}

	// 第 3 次续跑出来的 d 跑了 3 小时才又失败：这次续跑成功，名额清零——重新排期，是「第 1 次」，退避回到第一档 5 分钟。
	ps := chain()
	r, _ := planAutoResume(ps, failedRec("d", 3*time.Hour), retriableExit, now, b, autoResumeOKDefault)
	if r.State != store.AutoResumePending || r.Attempt != 1 || !r.Due.Equal(now.Add(5*time.Minute)) {
		t.Fatalf("跑了 3 小时的续跑算成功，次数清零，退避回到 5 分钟: %+v", r)
	}
	if !ps.AutoResumes[2].Settled || ps.AutoResumes[2].Quick || ps.AutoResumes[0].Settled || ps.AutoResumes[1].Settled {
		t.Fatalf("只认定出事的那次续跑（d）: %+v", ps.AutoResumes)
	}
	if n := ps.AutoResumeStartedSince(now.Add(-guardWakeWindow)); n != 0 {
		t.Fatalf("认定之后预算用量应是 0: %d", n)
	}

	// 恰好跑满判定时长也算成功；差一毫秒就是「没有进展」，不清零（预算照旧用完）。
	ps = chain()
	if r, _ := planAutoResume(ps, failedRec("d", autoResumeOKDefault), retriableExit, now, b, autoResumeOKDefault); r.State != store.AutoResumePending || r.Attempt != 1 {
		t.Fatalf("恰好 10 分钟算成功: %+v", r)
	}
	ps = chain()
	r, _ = planAutoResume(ps, failedRec("d", autoResumeOKDefault-time.Millisecond), retriableExit, now, b, autoResumeOKDefault)
	if r.State != store.AutoResumeGaveUp || !ps.AutoResumes[2].Quick || ps.AutoResumes[2].Settled {
		t.Fatalf("差一点就是没有进展，不清零: %+v / %+v", r, ps.AutoResumes[2])
	}

	// 判定时长可以自定义：设成 30 分钟，跑了 20 分钟还不算成功，跑了 45 分钟才算。
	ps = chain()
	if r, _ := planAutoResume(ps, failedRec("d", 20*time.Minute), retriableExit, now, b, 30*time.Minute); r.State != store.AutoResumeGaveUp {
		t.Fatalf("30 分钟的标准下 20 分钟没有进展: %+v", r)
	}
	ps = chain()
	if r, _ := planAutoResume(ps, failedRec("d", 45*time.Minute), retriableExit, now, b, 30*time.Minute); r.State != store.AutoResumePending || r.Attempt != 1 {
		t.Fatalf("30 分钟的标准下 45 分钟算成功: %+v", r)
	}

	// 清零是从成功的那次往后重新算：成功之后又续跑了 3 次（这回都没进展），预算照样会用完。
	ps = chain()
	planAutoResume(ps, failedRec("d", 3*time.Hour), retriableExit, now, b, autoResumeOKDefault)
	ps.AutoResumes = append(ps.AutoResumes,
		started("e1", "e2", 50*time.Minute), started("e2", "e3", 40*time.Minute), started("e3", "e4", 30*time.Minute),
	)
	// e4 是个新的出事运行（不是任何一次续跑出来的），之前这一串里 3 次都已经占了名额。
	if r, _ := planAutoResume(ps, failedRec("manual-9", time.Hour), retriableExit, now, b, autoResumeOKDefault); r.State != store.AutoResumeGaveUp {
		t.Fatalf("成功之后重新累计满 3 次，应放弃: %+v", r)
	}
}

// 认定是幂等的：同一次续跑只认定一次，不是续跑出来的运行、没有开跑的登记都不会被误认定。
func TestSettleAutoResumeOnlyTouchesAStartedResumeOnce(t *testing.T) {
	ps := &store.GuardPresetState{AutoResumes: []store.AutoResumeRecord{
		{FromRun: "a", RunID: "b", State: store.AutoResumeStarted, Quick: true},
		{FromRun: "b", State: store.AutoResumePending},
	}}
	if ps.SettleAutoResume("zzz") || ps.SettleAutoResume("") {
		t.Fatal("不是续跑出来的运行不该被认定")
	}
	if !ps.SettleAutoResume("b") || !ps.AutoResumes[0].Settled || ps.AutoResumes[0].Quick {
		t.Fatalf("应认定并去掉 Quick: %+v", ps.AutoResumes[0])
	}
	if ps.SettleAutoResume("b") {
		t.Fatal("第二次认定应返回 false（调用方据此只写一次聊天行）")
	}
	var none *store.GuardPresetState
	if none.SettleAutoResume("b") {
		t.Fatal("空状态不该 panic 也不该有改动")
	}
}

// #187：结束的处理先于「写运行编号」时，按运行说明里的次数把已开跑、还没有编号的那条登记认领过来；认领是幂等的，也不会认错。
func TestClaimAutoResumeRun(t *testing.T) {
	var none *store.GuardPresetState
	if none.ClaimAutoResumeRun("r9", 1) {
		t.Fatal("空状态不该 panic 也不该有改动")
	}
	mk := func() *store.GuardPresetState {
		return &store.GuardPresetState{AutoResumes: []store.AutoResumeRecord{
			{FromRun: "a", RunID: "a-next", State: store.AutoResumeStarted, Attempt: 1},
			{FromRun: "c", State: store.AutoResumeStarted, Attempt: 2}, // 已开跑，还没有编号
			{FromRun: "b", State: store.AutoResumePending, Attempt: 2}, // 排着队，还没开跑（排在后面，认领不能被它拐走）
		}}
	}
	ps := mk()
	for _, c := range []struct {
		runID   string
		attempt int
		why     string
	}{
		{"", 2, "没有运行编号"},
		{"r9", 0, "次数不合法"},
		{"r9", -1, "次数不合法"},
		{"r9", 3, "次数对不上"},
		{"r9", 1, "次数 1 的那条已经有编号了"},
	} {
		if ps.ClaimAutoResumeRun(c.runID, c.attempt) {
			t.Fatalf("%s：不该认领: %+v", c.why, ps.AutoResumes)
		}
	}
	if ps.AutoResumes[0].RunID != "a-next" || ps.AutoResumes[1].RunID != "" || ps.AutoResumes[2].RunID != "" {
		t.Fatalf("不该认领时什么也不改: %+v", ps.AutoResumes)
	}
	if !ps.ClaimAutoResumeRun("r9", 2) || ps.AutoResumes[1].RunID != "r9" || ps.AutoResumes[2].RunID != "" {
		t.Fatalf("应认领已开跑、没有编号、次数相同的那条（排着队的不认领）: %+v", ps.AutoResumes)
	}
	if ps.ClaimAutoResumeRun("r9", 2) {
		t.Fatal("同一个编号第二次认领应返回 false（幂等）")
	}
	if ps.ClaimAutoResumeRun("r10", 2) {
		t.Fatal("已经没有「已开跑、没有编号」的登记了：不该认领")
	}

	// 已经有登记带着这个编号：即使还有别的没编号的同次数登记，也不再认领（一个编号只属于一条登记）。
	ps = &store.GuardPresetState{AutoResumes: []store.AutoResumeRecord{
		{FromRun: "x", RunID: "r5", State: store.AutoResumeStarted, Attempt: 2},
		{FromRun: "y", State: store.AutoResumeStarted, Attempt: 2},
	}}
	if ps.ClaimAutoResumeRun("r5", 2) || ps.AutoResumes[1].RunID != "" {
		t.Fatalf("编号已经属于另一条登记: %+v", ps.AutoResumes)
	}
	// 多条同次数的没编号登记（不该出现，防御）：认领最近的一条。
	ps = &store.GuardPresetState{AutoResumes: []store.AutoResumeRecord{
		{FromRun: "x", State: store.AutoResumeStarted, Attempt: 2},
		{FromRun: "y", State: store.AutoResumeStarted, Attempt: 2},
	}}
	if !ps.ClaimAutoResumeRun("r6", 2) || ps.AutoResumes[1].RunID != "r6" || ps.AutoResumes[0].RunID != "" {
		t.Fatalf("应认领最近的一条: %+v", ps.AutoResumes)
	}
}

func TestAutoResumeViewOf(t *testing.T) {
	now := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	on := store.TerminalLauncher{Run: &store.HeadlessRun{AutoResume: true}}
	off := store.TerminalLauncher{Run: &store.HeadlessRun{}}
	plain := model.TerminalRun{RunID: "r1", Description: "预设「续聊」的非交互续聊"}

	if v := autoResumeViewOf(off, nil, plain, autoResumeOKDefault); v != nil {
		t.Fatalf("没开、也与自动续跑无关：不该有 Auto: %+v", v)
	}
	if v := autoResumeViewOf(on, nil, plain, autoResumeOKDefault); v == nil || !v.Enabled || v.Max != autoResumeMax || v.State != "" || v.Attempt != 0 || v.OKMinutes != 10 || v.Settled {
		t.Fatalf("开着但还没有安排（成功判定默认 10 分钟）: %+v", v)
	}
	// 判定时长按分钟给界面：不满 1 分钟（实验用的秒级覆盖）按 1 计，不会写成 0。
	if v := autoResumeViewOf(on, nil, plain, 30*time.Second); v.OKMinutes != 1 {
		t.Fatalf("不满 1 分钟按 1 计: %+v", v)
	}
	if v := autoResumeViewOf(on, nil, plain, 45*time.Minute); v.OKMinutes != 45 {
		t.Fatalf("自定义 45 分钟: %+v", v)
	}
	auto := model.TerminalRun{RunID: "r2", Description: "预设「续聊」的非交互续聊" + autoResumeSuffix(2, 3)}
	if v := autoResumeViewOf(off, nil, auto, autoResumeOKDefault); v == nil || v.Enabled || v.Attempt != 2 {
		t.Fatalf("关掉之后，之前自动续跑出来的运行仍要标出次数: %+v", v)
	}
	ps := &store.GuardPresetState{AutoResumes: []store.AutoResumeRecord{
		{FromRun: "r1", State: store.AutoResumePending, Attempt: 2, Due: now.Add(time.Minute)},
	}}
	if v := autoResumeViewOf(on, ps, plain, autoResumeOKDefault); v.State != store.AutoResumePending || v.NextAttempt != 2 || v.Due == nil || !v.Due.Equal(now.Add(time.Minute)) {
		t.Fatalf("待开跑: %+v", v)
	}
	ps.AutoResumes[0].State, ps.AutoResumes[0].RunID, ps.AutoResumes[0].Why = store.AutoResumeStarted, "r7", ""
	if v := autoResumeViewOf(on, ps, plain, autoResumeOKDefault); v.State != store.AutoResumeStarted || v.NextRunID != "r7" || v.Due != nil {
		t.Fatalf("已开跑: %+v", v)
	}
	ps.AutoResumes[0].State, ps.AutoResumes[0].Why = store.AutoResumeGaveUp, "达到上限"
	if v := autoResumeViewOf(on, ps, plain, autoResumeOKDefault); v.State != store.AutoResumeGaveUp || v.Why != "达到上限" {
		t.Fatalf("放弃: %+v", v)
	}

	// 这次运行本身是自动续跑出来的，并且已经认定成功：界面据此写「次数已清零」。
	resumed := model.TerminalRun{RunID: "r7", Description: "预设「续聊」的非交互续聊" + autoResumeSuffix(1, 3)}
	ps = &store.GuardPresetState{AutoResumes: []store.AutoResumeRecord{{FromRun: "r1", RunID: "r7", State: store.AutoResumeStarted}}}
	if v := autoResumeViewOf(on, ps, resumed, autoResumeOKDefault); v.Settled || v.Attempt != 1 {
		t.Fatalf("还没跑满判定时长: %+v", v)
	}
	ps.SettleAutoResume("r7")
	if v := autoResumeViewOf(on, ps, resumed, autoResumeOKDefault); !v.Settled || v.Attempt != 1 {
		t.Fatalf("已经认定成功: %+v", v)
	}
}

// ---------- 经 Exec 通道的集成 ----------

const autoRetriableErr = "RetriableError: [resource_exhausted] Error\n"

// autoWait 是等异步处理（运行结束的登记、聊天行、假主机收到运行……）完成的上限。它只是兜底——正常几十毫秒就好，不是时间断言：
// 机器被别的任务占满、整个测试拿不到几个时间片时（#187），3 秒这类贴着正常耗时的上限会误报，所以留足余量；真卡住了只是晚几秒报错。
const autoWait = 15 * time.Second

// autoFixture 在执行夹具上配好打开了自动续跑的预设 p1（假时钟、退避 5 分钟 / 20 分钟 / 1 小时），并让工作流 hook 报过对话编号 chat-9。
func autoFixture(t *testing.T, run map[string]any) (*execFixture, string) {
	t.Helper()
	f := newExecFixture(t, withFakeClock(), func(o *Options, _ *testEnv) {
		o.AutoResumeBackoff = []time.Duration{5 * time.Minute, 20 * time.Minute, time.Hour}
	})
	if run == nil {
		run = map[string]any{}
	}
	run["workflow_id"] = launcherWF
	if _, set := run["auto_resume"]; !set {
		run["auto_resume"] = true
	}
	f.saveLauncher(t, "p1", "续聊", run)
	now := f.env.clock.Now()
	f.env.store.RecordHookSeen(launcherWF, now)
	f.env.store.RecordAgentEvent(context.Background(), launcherWF, store.AgentEvent{Kind: store.AgentEventTool, ConversationID: "chat-9"}, now)
	return f, "p1"
}

// failWith 让主机上的每次运行都以退出码 1 结束，标准错误里是给定的内容。
func (f *execFixture) failWith(stderr string) {
	f.sim.onRun = func(s *hostSim, m termproto.Run) {
		s.started(m.ID)
		s.end(m.ID, termproto.RunExited, intp(1), "", stderr)
	}
}

// runAt 按假时钟当前时间手动运行预设。
func (f *execFixture) runAt(id string) (LauncherRunPlan, error) {
	plan, _, err := f.env.hub.guardian.runLauncher(context.Background(), id, f.env.clock.Now())
	return plan, err
}

func (f *execFixture) autoPreset(t *testing.T, id string) *store.GuardPresetState {
	t.Helper()
	st, err := f.env.store.GetTerminalGuardState(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if ps := st.Presets[id]; ps != nil {
		return ps
	}
	return &store.GuardPresetState{}
}

// waitAutoRecords 等预设的自动续跑登记至少有 n 条（运行终结的处理是异步的）。
func (f *execFixture) waitAutoRecords(t *testing.T, id string, n int) []store.AutoResumeRecord {
	t.Helper()
	var recs []store.AutoResumeRecord
	eventually(t, autoWait, "出现自动续跑登记", func() bool {
		recs = f.autoPreset(t, id).AutoResumes
		return len(recs) >= n
	})
	return recs
}

func (f *execFixture) waitChat(t *testing.T, what string, match func(texts []string) bool) []string {
	t.Helper()
	var texts []string
	eventually(t, autoWait, what, func() bool {
		texts = f.chatTexts(launcherWF)
		return match(texts)
	})
	return texts
}

func chatHas(texts []string, parts ...string) bool {
	for _, s := range texts {
		ok := true
		for _, p := range parts {
			if !strings.Contains(s, p) {
				ok = false
				break
			}
		}
		if ok {
			return true
		}
	}
	return false
}

// autoEvents 收集自动续跑的通知。
type autoEvents struct {
	mu  sync.Mutex
	got []AutoResumeEvent
}

func (a *autoEvents) hook(ev AutoResumeEvent) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.got = append(a.got, ev)
}

func (a *autoEvents) list() []AutoResumeEvent {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]AutoResumeEvent(nil), a.got...)
}

func (f *execFixture) ingestConv(t *testing.T, name string, at time.Time) {
	t.Helper()
	_, err := f.env.store.IngestAgentEvent(context.Background(), store.AgentIngestInput{
		WorkflowID: launcherWF, CredentialID: 1, HostName: "h", ProjectDirectory: "/work/app", ConversationID: "chat-9", ClientKind: "cli",
		Event: agentstate.Event{Source: agentstate.SourceHook, Name: name, Received: at, Status: "completed"},
	})
	if err != nil {
		t.Fatal(err)
	}
}

// 主线：可重试的失败 → 排期并在聊天里说明；没到点不动；到点用同一条命令续跑一次并标明次数；续跑成功就到此为止。
func TestAutoResumePlansARetriableFailureAndRunsItOnce(t *testing.T) {
	f, id := autoFixture(t, nil)
	ctx, g, clock := context.Background(), f.env.hub.guardian, f.env.clock
	var ev autoEvents
	f.env.hub.SetAutoResumeHook(ev.hook)
	t0 := clock.Now()
	f.failWith(autoRetriableErr)

	first, err := f.runAt(id)
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, autoWait, "假主机收到运行", func() bool { return f.sim.runCount() == 1 })
	firstCmd := f.sim.lastRun().Cmd
	r := f.waitAutoRecords(t, id, 1)[0]
	if r.FromRun != first.RunID || r.State != store.AutoResumePending || r.Attempt != 1 || r.Class != "retriable" ||
		r.Code != "resource_exhausted" || !r.Due.Equal(t0.Add(5*time.Minute)) || !r.PlannedAt.Equal(t0) {
		t.Fatalf("排期不对: %+v", r)
	}
	texts := f.waitChat(t, "聊天里有结束行与排期行", func(x []string) bool { return chatHas(x, "系统计划 5 分钟后自动续跑") })
	if !chatHas(texts, "已结束：失败", "退出码 1", "原因：Cursor CLI 报了「可重试」的错误：服务端限流", "resource_exhausted") {
		t.Fatalf("结束行应写明失败原因: %q", texts)
	}
	if !chatHas(texts, "预设「续聊」的运行异常退出", "第 1/3 次", "到点之前可以在启动页取消") {
		t.Fatalf("排期行不对: %q", texts)
	}

	// 没到点：什么都不做。
	g.autoResumeTick(ctx, t0.Add(4*time.Minute))
	if f.sim.runCount() != 1 || f.autoPreset(t, id).AutoResumes[0].State != store.AutoResumePending {
		t.Fatalf("没到点不该续跑: runs=%d %+v", f.sim.runCount(), f.autoPreset(t, id).AutoResumes)
	}

	// 到点：续跑一次，命令与手动运行完全一样（同一条提交路径）。
	f.completeWith(launcherResultOut)
	clock.Advance(5 * time.Minute)
	g.autoResumeTick(ctx, clock.Now())
	eventually(t, autoWait, "假主机收到第二次运行", func() bool { return f.sim.runCount() == 2 })
	if cmd := f.sim.lastRun().Cmd; cmd != firstCmd {
		t.Fatalf("自动续跑的命令应与手动运行完全一样：\n got %s\nwant %s", cmd, firstCmd)
	}
	r = f.autoPreset(t, id).AutoResumes[0]
	if r.State != store.AutoResumeStarted || r.RunID == "" || r.Attempt != 1 || !r.StartedAt.Equal(clock.Now()) {
		t.Fatalf("开跑后的登记不对: %+v", r)
	}
	second := f.dbRun(t, r.RunID)
	if second.CredentialName != launcherRunCaller || second.Thread != launcherThreadPrefix+id ||
		!strings.HasSuffix(second.Description, "（自动续跑 1/3）") || !strings.Contains(second.Description, "预设「续聊」") {
		t.Fatalf("自动续跑出来的运行记录不对: %+v", second)
	}

	// 续跑成功：到此为止，没有新的排期，也没有放弃通知。
	f.waitChat(t, "第二次运行的结束行", func(x []string) bool { return chatHas(x, "已结束：成功") })
	f.waitRunFinal(t, second.RunID)
	texts = f.chatTexts(launcherWF)
	if !chatHas(texts, "系统自动续跑了预设「续聊」（第 1/3 次", "上一次运行出事的原因：Cursor CLI 报了「可重试」的错误", "接着对话 chat-9") {
		t.Fatalf("应有系统自动续跑行: %q", texts)
	}
	for _, s := range texts {
		if strings.Contains(s, "agent -p") || strings.Contains(s, "没有人在终端里看你的输出") {
			t.Fatalf("系统行里不该有命令或提示词: %q", s)
		}
	}
	if n := len(f.autoPreset(t, id).AutoResumes); n != 1 {
		t.Fatalf("成功的运行不该再登记: %d", n)
	}
	if got := ev.list(); len(got) != 0 {
		t.Fatalf("没有放弃就不该通知: %+v", got)
	}

	// 现状：最新一次运行是自动续跑出来的第 1 次；出事的那次运行显示「已续跑」并指向它。
	views, _ := g.launcherRunViews(ctx, clock.Now())
	if v := views[id]; v.RunID != second.RunID || v.Auto == nil || !v.Auto.Enabled || v.Auto.Attempt != 1 || v.Auto.Max != 3 || v.State != model.RunSucceeded {
		t.Fatalf("最新一次运行的现状: %+v / %+v", v, v.Auto)
	}
	launchers, _ := f.env.store.ListTerminalLaunchers(ctx)
	firstRec := f.dbRun(t, first.RunID)
	if v := autoResumeViewOf(launchers[0], f.autoPreset(t, id), *firstRec, autoResumeOKDefault); v.State != store.AutoResumeStarted || v.NextRunID != second.RunID {
		t.Fatalf("出事的那次运行应指向续跑出来的运行: %+v", v)
	}
	fv := g.runViewOf(ctx, *firstRec, clock.Now())
	if fv.Failure == nil || fv.Failure.Class != "retriable" || !fv.Failure.Retry || fv.Failure.Code != "resource_exhausted" || fv.Reply != "" {
		t.Fatalf("失败说明应在运行现状里，且不重复显示那行错误: %+v / %+v", fv, fv.Failure)
	}

	// 审计：系统发起的这次运行有一条记录，只有预设编号与次数，不含命令。
	if n := f.auditCount(t, EvLauncherAutoRun); n != 1 {
		t.Fatalf("应有一条自动续跑审计: %d", n)
	}
	for _, l := range f.audits(t) {
		if l.Event == EvLauncherAutoRun && (l.Detail != "preset=p1 attempt=1/3" || l.RunID != second.RunID || l.ActorKind != ActorSystem) {
			t.Fatalf("审计内容不对: %+v", l)
		}
	}
}

// 熔断：续跑出来的运行连续两次很快又失败 → 不再续跑、通知你；预算没有用完也一样。
func TestAutoResumeBreaksAfterConsecutiveQuickFailuresAndNotifies(t *testing.T) {
	f, id := autoFixture(t, nil)
	ctx, g, clock := context.Background(), f.env.hub.guardian, f.env.clock
	var ev autoEvents
	f.env.hub.SetAutoResumeHook(ev.hook)
	f.failWith(autoRetriableErr)

	if _, err := f.runAt(id); err != nil {
		t.Fatal(err)
	}
	f.waitAutoRecords(t, id, 1)

	clock.Advance(5 * time.Minute)
	g.autoResumeTick(ctx, clock.Now()) // 第 1 次续跑 → 很快又失败 → 排第 2 次（退避 20 分钟）
	recs := f.waitAutoRecords(t, id, 2)
	if recs[0].State != store.AutoResumeStarted || !recs[0].Quick || recs[1].State != store.AutoResumePending || recs[1].Attempt != 2 ||
		!recs[1].Due.Equal(clock.Now().Add(20*time.Minute)) || recs[1].FromRun != recs[0].RunID {
		t.Fatalf("第一次没有进展：应排第 2 次、退避 20 分钟: %+v", recs)
	}

	clock.Advance(20 * time.Minute)
	g.autoResumeTick(ctx, clock.Now()) // 第 2 次续跑 → 又很快失败 → 熔断
	recs = f.waitAutoRecords(t, id, 3)
	if recs[1].State != store.AutoResumeStarted || !recs[1].Quick || recs[2].State != store.AutoResumeGaveUp || !strings.Contains(recs[2].Why, "连续 2 次") {
		t.Fatalf("应熔断: %+v", recs)
	}
	if f.sim.runCount() != 3 {
		t.Fatalf("1 次手动 + 2 次自动 = 3 次运行，实际 %d", f.sim.runCount())
	}
	// 熔断的登记先落库、通知回调随后才发出（运行结束的处理在另一个协程里）：登记一出现就立刻数通知，
	// 在 -race 或主机忙的时候会抢在回调前面读到空列表，所以要等它（TestAutoResumeGivesUpWhenTheDailyBudgetIsSpent 同样这么等）。
	eventually(t, autoWait, "放弃要通知一次", func() bool { return len(ev.list()) >= 1 })
	got := ev.list()
	if len(got) != 1 || got[0].PresetID != id || got[0].FromRun != recs[2].FromRun || !strings.Contains(got[0].Why, "连续 2 次") ||
		!strings.Contains(got[0].Reason, "可重试") || got[0].Name != "续聊" || got[0].WorkflowID != launcherWF {
		t.Fatalf("放弃要通知一次: %+v", got)
	}
	f.waitChat(t, "聊天里写明不再续跑", func(x []string) bool { return chatHas(x, "不再自动续跑预设「续聊」", "连续 2 次") })

	// 之后的检查什么都不会再做。
	clock.Advance(3 * time.Hour)
	g.autoResumeTick(ctx, clock.Now())
	if f.sim.runCount() != 3 || len(ev.list()) != 1 {
		t.Fatalf("熔断之后不该再动: runs=%d events=%d", f.sim.runCount(), len(ev.list()))
	}
	// 熔断那次放弃的登记不占预算：真正开跑的只有 2 次（都还在 24 小时窗口里）。
	if n := f.autoPreset(t, id).AutoResumeStartedSince(clock.Now().Add(-guardWakeWindow)); n != 2 {
		t.Fatalf("预算用量应是 2: %d", n)
	}
}

// 预算：24 小时内已经自动续跑 3 次，再出事就不排期了，直接放弃并通知。
func TestAutoResumeGivesUpWhenTheDailyBudgetIsSpent(t *testing.T) {
	f, id := autoFixture(t, nil)
	ctx, clock := context.Background(), f.env.clock
	var ev autoEvents
	f.env.hub.SetAutoResumeHook(ev.hook)
	now := clock.Now()
	if _, err := f.env.store.UpdateTerminalGuardState(ctx, now, func(d *store.GuardStateDoc) {
		ps := d.Preset(id)
		for i, from := range []string{"x1", "x2", "x3"} {
			ps.AutoResumes = append(ps.AutoResumes, store.AutoResumeRecord{
				FromRun: from, RunID: from + "-next", State: store.AutoResumeStarted, PlannedAt: now.Add(-time.Duration(i+1) * time.Hour),
				StartedAt: now.Add(-time.Duration(i+1) * time.Hour),
			})
		}
	}); err != nil {
		t.Fatal(err)
	}
	f.failWith(autoRetriableErr)
	first, err := f.runAt(id)
	if err != nil {
		t.Fatal(err)
	}
	recs := f.waitAutoRecords(t, id, 4)
	last := recs[3]
	if last.FromRun != first.RunID || last.State != store.AutoResumeGaveUp || !strings.Contains(last.Why, "24 小时内已经自动续跑 3 次") || !last.Due.IsZero() {
		t.Fatalf("预算用完应直接放弃: %+v", last)
	}
	eventually(t, autoWait, "放弃通知", func() bool { return len(ev.list()) == 1 })
	f.waitChat(t, "聊天里写明达到上限", func(x []string) bool { return chatHas(x, "不再自动续跑预设「续聊」", "达到上限") })
	if f.sim.runCount() != 1 {
		t.Fatalf("不该再续跑: %d", f.sim.runCount())
	}
}

// 不可重试 / 要你处理 / 认不出的失败：不排期；聊天里写明为什么没续跑。预设没开自动续跑时更是什么都不登记。
func TestAutoResumeIgnoresFailuresThatRetryingCannotFix(t *testing.T) {
	cases := []struct {
		name, stderr, want string
	}{
		{"要你处理", "ActionRequiredError: [unauthenticated] Please log in\n", "Cursor CLI 报了「需要你处理」的错误"},
		{"不可重试", "NonRetriableError: [invalid_argument] bad model\n", "Cursor CLI 报了「不可重试」的错误"},
		{"认不出", "Error: something odd\n", "输出末尾没有认得出的 Cursor CLI 错误类别"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f, id := autoFixture(t, nil)
			ctx, g, clock := context.Background(), f.env.hub.guardian, f.env.clock
			var ev autoEvents
			f.env.hub.SetAutoResumeHook(ev.hook)
			f.failWith(c.stderr)
			first, err := f.runAt(id)
			if err != nil {
				t.Fatal(err)
			}
			f.waitChat(t, "说明没有自动续跑", func(x []string) bool {
				return chatHas(x, "没有自动续跑预设「续聊」", c.want, "重试没有用")
			})
			if n := len(f.autoPreset(t, id).AutoResumes); n != 0 {
				t.Fatalf("不该登记: %+v", f.autoPreset(t, id).AutoResumes)
			}
			clock.Advance(3 * time.Hour)
			g.autoResumeTick(ctx, clock.Now())
			if f.sim.runCount() != 1 || len(ev.list()) != 0 {
				t.Fatalf("不该续跑也不该通知: runs=%d events=%v", f.sim.runCount(), ev.list())
			}
			rec := f.dbRun(t, first.RunID)
			if v := g.runViewOf(ctx, *rec, clock.Now()); v.Failure == nil || v.Failure.Retry {
				t.Fatalf("失败说明里这类失败不该是「值得重试」: %+v", v.Failure)
			}
		})
	}
}

func TestAutoResumeStaysOffUnlessThePresetOptsIn(t *testing.T) {
	f, id := autoFixture(t, map[string]any{"auto_resume": false})
	ctx, g, clock := context.Background(), f.env.hub.guardian, f.env.clock
	f.failWith(autoRetriableErr)
	first, err := f.runAt(id)
	if err != nil {
		t.Fatal(err)
	}
	// 结束行照样写明失败原因（对谁都有用），但不登记、不说要自动续跑。
	texts := f.waitChat(t, "结束行", func(x []string) bool {
		return chatHas(x, "已结束：失败", "原因：Cursor CLI 报了「可重试」的错误")
	})
	if chatHas(texts, "自动续跑") {
		t.Fatalf("没打开自动续跑，聊天里不该出现它: %q", texts)
	}
	if len(f.autoPreset(t, id).AutoResumes) != 0 {
		t.Fatalf("没打开就不该登记: %+v", f.autoPreset(t, id).AutoResumes)
	}
	clock.Advance(3 * time.Hour)
	g.autoResumeTick(ctx, clock.Now())
	if f.sim.runCount() != 1 {
		t.Fatalf("没打开就不该续跑: %d", f.sim.runCount())
	}
	views, _ := g.launcherRunViews(ctx, clock.Now())
	if v := views[id]; v.RunID != first.RunID || v.Auto != nil || v.Failure == nil || v.Failure.Class != "retriable" {
		t.Fatalf("没打开：现状不带 Auto，但仍说明失败原因: %+v / %+v", v, v.Auto)
	}
}

// 取消、到运行时长上限、成功的运行都不是异常退出：不登记。
func TestAutoResumeIgnoresIntentionalAndSuccessfulEnds(t *testing.T) {
	f, id := autoFixture(t, nil)
	ctx, g, clock := context.Background(), f.env.hub.guardian, f.env.clock

	f.sim.onRun = func(s *hostSim, m termproto.Run) {
		s.started(m.ID)
		s.end(m.ID, termproto.RunTimeout, nil, "", autoRetriableErr)
	}
	p1, err := f.runAt(id)
	if err != nil {
		t.Fatal(err)
	}
	if rec := f.waitRunFinal(t, p1.RunID); rec.State != model.RunTimedOut {
		t.Fatalf("应是运行时长上限: %s", rec.State)
	}
	f.completeWith(launcherResultOut)
	p2, err := f.runAt(id)
	if err != nil {
		t.Fatal(err)
	}
	f.waitRunFinal(t, p2.RunID)
	f.waitChat(t, "两次运行都有结束行", func(x []string) bool {
		return chatHas(x, "已结束：达到运行时长上限") && chatHas(x, "已结束：成功")
	})
	clock.Advance(3 * time.Hour)
	g.autoResumeTick(ctx, clock.Now())
	if n := len(f.autoPreset(t, id).AutoResumes); n != 0 || f.sim.runCount() != 2 {
		t.Fatalf("不该登记也不该续跑: records=%d runs=%d", n, f.sim.runCount())
	}
}

// 崩溃类信号（SIGKILL）值得续跑；有意结束的信号（SIGTERM）不续跑。
func TestAutoResumeRetriesCrashSignalsButNotDeliberateOnes(t *testing.T) {
	for _, c := range []struct {
		sig  string
		plan bool
	}{{"SIGKILL", true}, {"SIGTERM", false}} {
		t.Run(c.sig, func(t *testing.T) {
			f, id := autoFixture(t, nil)
			f.sim.onRun = func(s *hostSim, m termproto.Run) {
				s.started(m.ID)
				s.endFull(termproto.RunEnd{ID: m.ID, State: termproto.RunSignaled, Signal: c.sig, DurMS: 5})
			}
			if _, err := f.runAt(id); err != nil {
				t.Fatal(err)
			}
			if c.plan {
				r := f.waitAutoRecords(t, id, 1)[0]
				if r.State != store.AutoResumePending || r.Class != "crashed" {
					t.Fatalf("崩溃应排期: %+v", r)
				}
				return
			}
			f.waitChat(t, "说明没有自动续跑", func(x []string) bool { return chatHas(x, "没有自动续跑预设「续聊」", "信号 SIGTERM") })
			if n := len(f.autoPreset(t, id).AutoResumes); n != 0 {
				t.Fatalf("有意结束的信号不该登记: %d", n)
			}
		})
	}
}

// 取消：REST 只要登录；取消之后到点什么都不做；再取消返回 409；审计只记预设编号。
func TestAutoResumeCancelViaREST(t *testing.T) {
	f, id := autoFixture(t, nil)
	ctx, g, clock := context.Background(), f.env.hub.guardian, f.env.clock
	f.failWith(autoRetriableErr)
	first, err := f.runAt(id)
	if err != nil {
		t.Fatal(err)
	}
	f.waitAutoRecords(t, id, 1)
	path := "/api/v1/terminal/launchers/runs/" + first.RunID + "/auto/cancel"

	if res := f.env.call("POST", path, nil); res.Status != http.StatusUnauthorized {
		t.Fatalf("未登录不能取消: %d %s", res.Status, res.Raw)
	}
	// 只要登录，不需要刚刚解锁。
	res := f.env.call("POST", path, nil, "Authorization", humanAuth(testUser))
	if res.Status != http.StatusOK {
		t.Fatalf("取消失败: %d %s", res.Status, res.Raw)
	}
	r := f.autoPreset(t, id).AutoResumes[0]
	if r.State != store.AutoResumeStopped || !strings.Contains(r.Why, "你取消了") {
		t.Fatalf("应记为已取消: %+v", r)
	}
	f.waitChat(t, "聊天里写明取消", func(x []string) bool { return chatHas(x, "你取消了预设「续聊」的自动续跑计划") })
	if n := f.auditCount(t, EvLauncherAutoStop); n != 1 {
		t.Fatalf("应有一条取消审计: %d", n)
	}
	for _, l := range f.audits(t) {
		if l.Event == EvLauncherAutoStop && (l.RunID != first.RunID || l.Detail != "preset=p1") {
			t.Fatalf("取消审计内容不对: %+v", l)
		}
	}

	// 再取消：已经不在排期里。
	res = f.env.call("POST", path, nil, "Authorization", humanAuth(testUser))
	if res.Status != http.StatusConflict || res.str("code") != "not_pending" {
		t.Fatalf("重复取消应 409: %d %s", res.Status, res.Raw)
	}
	if res = f.env.call("POST", "/api/v1/terminal/launchers/runs/nope/auto/cancel", nil, "Authorization", humanAuth(testUser)); res.Status != http.StatusConflict {
		t.Fatalf("没有排期的运行 409: %d %s", res.Status, res.Raw)
	}

	// 到点：不会续跑。
	clock.Advance(10 * time.Minute)
	g.autoResumeTick(ctx, clock.Now())
	if f.sim.runCount() != 1 {
		t.Fatalf("取消之后不该续跑: %d", f.sim.runCount())
	}
}

// 到点时重新核对：你已经重新运行过 / 预设删了 / 开关关了 / 有人接手了 / 对话在运行 → 作废，不硬跑。
func TestAutoResumeIsVoidedWhenTheWorldChangedBeforeItIsDue(t *testing.T) {
	type scenario struct {
		name  string
		setup func(t *testing.T, f *execFixture, id string)
		why   string
	}
	for _, c := range []scenario{
		{"你已经重新运行过", func(t *testing.T, f *execFixture, id string) {
			f.completeWith(launcherResultOut)
			p, err := f.runAt(id)
			if err != nil {
				t.Fatal(err)
			}
			f.waitRunFinal(t, p.RunID)
		}, "你已经重新运行过这个预设了"},
		{"预设里关掉了自动续跑", func(t *testing.T, f *execFixture, id string) {
			f.saveLauncher(t, id, "续聊", map[string]any{"workflow_id": launcherWF})
		}, "预设里已经关掉了自动续跑"},
		{"预设被删除", func(t *testing.T, f *execFixture, id string) {
			if res := f.env.call("DELETE", "/api/v1/terminal/launchers/"+id, nil, f.adm...); res.Status != 200 {
				t.Fatalf("删除预设: %d %s", res.Status, res.Raw)
			}
		}, "预设已经不存在了"},
		{"有人接手了（对话在出事之后又有了新动静）", func(t *testing.T, f *execFixture, id string) {
			at := f.env.clock.Now().Add(3 * time.Minute)
			f.ingestConv(t, agentstate.EvPrompt, at)
			f.ingestConv(t, agentstate.EvStop, at.Add(time.Second))
		}, "说明有人接手了"},
		{"对话又在运行", func(t *testing.T, f *execFixture, id string) {
			f.ingestConv(t, agentstate.EvPrompt, f.env.clock.Now().Add(4*time.Minute+30*time.Second))
		}, autoResumeBusyWhy},
	} {
		t.Run(c.name, func(t *testing.T) {
			f, id := autoFixture(t, nil)
			ctx, g, clock := context.Background(), f.env.hub.guardian, f.env.clock
			f.failWith(autoRetriableErr)
			first, err := f.runAt(id)
			if err != nil {
				t.Fatal(err)
			}
			f.waitAutoRecords(t, id, 1)
			f.waitRunFinal(t, first.RunID)
			c.setup(t, f, id)
			runsBefore := f.sim.runCount()

			clock.Advance(5 * time.Minute)
			g.autoResumeTick(ctx, clock.Now())
			r := f.autoPreset(t, id).AutoResumes[0]
			if r.State != store.AutoResumeSkipped || !strings.Contains(r.Why, c.why) {
				t.Fatalf("应作废（%s）: %+v", c.why, r)
			}
			if f.sim.runCount() != runsBefore {
				t.Fatalf("作废之后不该再提交运行: %d → %d", runsBefore, f.sim.runCount())
			}
		})
	}
}

// 主机离线：继续等（不作废、不占预算）；等得太久（超过排定时间 2 小时）才作废。
func TestAutoResumeWaitsForTheHostThenGivesUpOnALateStart(t *testing.T) {
	f, id := autoFixture(t, nil)
	ctx, g, clock := context.Background(), f.env.hub.guardian, f.env.clock
	f.failWith(autoRetriableErr)
	first, err := f.runAt(id)
	if err != nil {
		t.Fatal(err)
	}
	r0 := f.waitAutoRecords(t, id, 1)[0]
	f.waitRunFinal(t, first.RunID)
	f.a.close()
	eventually(t, autoWait, "主机离线", func() bool { return f.env.hub.onlineAgent(f.a.code) == nil })

	clock.Advance(30 * time.Minute)
	g.autoResumeTick(ctx, clock.Now())
	r := f.autoPreset(t, id).AutoResumes[0]
	if r.State != store.AutoResumePending || !r.StartedAt.IsZero() {
		t.Fatalf("主机离线应继续等，不占预算: %+v", r)
	}
	if n := f.autoPreset(t, id).AutoResumeStartedSince(clock.Now().Add(-guardWakeWindow)); n != 0 {
		t.Fatalf("没开跑不该占预算: %d", n)
	}

	g.autoResumeTick(ctx, r0.Due.Add(autoResumeMaxLate+time.Second))
	r = f.autoPreset(t, id).AutoResumes[0]
	if r.State != store.AutoResumeSkipped || !strings.Contains(r.Why, "错过了续跑的时机") {
		t.Fatalf("等得太久应作废: %+v", r)
	}
}

// 提交被闸门拒绝（主机没开模型访问）：占的名额还回去，放弃并通知。
func TestAutoResumeReleasesTheBudgetWhenTheGateRefuses(t *testing.T) {
	f, id := autoFixture(t, nil)
	ctx, g, clock := context.Background(), f.env.hub.guardian, f.env.clock
	var ev autoEvents
	f.env.hub.SetAutoResumeHook(ev.hook)
	f.failWith(autoRetriableErr)
	first, err := f.runAt(id)
	if err != nil {
		t.Fatal(err)
	}
	f.waitAutoRecords(t, id, 1)
	f.waitRunFinal(t, first.RunID)
	f.setModel(t, map[string]any{"allow_model": false})

	clock.Advance(5 * time.Minute)
	g.autoResumeTick(ctx, clock.Now())
	r := f.autoPreset(t, id).AutoResumes[0]
	if r.State != store.AutoResumeGaveUp || r.Why == "" || r.RunID != "" || !r.StartedAt.IsZero() {
		t.Fatalf("被闸门拒绝应放弃并还回名额: %+v", r)
	}
	if n := f.autoPreset(t, id).AutoResumeStartedSince(clock.Now().Add(-guardWakeWindow)); n != 0 {
		t.Fatalf("名额应还回去: %d", n)
	}
	if f.sim.runCount() != 1 || len(ev.list()) != 1 {
		t.Fatalf("不该有运行到达主机，且要通知一次: runs=%d events=%d", f.sim.runCount(), len(ev.list()))
	}
	f.waitChat(t, "聊天里写明放弃", func(x []string) bool { return chatHas(x, "不再自动续跑预设「续聊」") })
}

// 一份排期只会开跑一次：同一轮、或紧接着的下一轮检查，都不会为它再提交第二次运行（登记在守护状态里，由占名额的事务把关）。
func TestAutoResumeNeverStartsTwiceFromOnePlan(t *testing.T) {
	f, id := autoFixture(t, nil)
	ctx, g, clock := context.Background(), f.env.hub.guardian, f.env.clock
	f.failWith(autoRetriableErr)
	first, err := f.runAt(id)
	if err != nil {
		t.Fatal(err)
	}
	f.waitAutoRecords(t, id, 1)
	f.waitRunFinal(t, first.RunID)
	f.completeWith(launcherResultOut)

	clock.Advance(6 * time.Minute)
	g.autoResumeTick(ctx, clock.Now())
	g.autoResumeTick(ctx, clock.Now())
	g.autoResumeTick(ctx, clock.Now().Add(time.Minute))
	eventually(t, autoWait, "假主机收到续跑", func() bool { return f.sim.runCount() == 2 })
	r := f.autoPreset(t, id).AutoResumes[0]
	f.waitRunFinal(t, r.RunID)
	g.autoResumeTick(ctx, clock.Now().Add(2*time.Minute))
	if f.sim.runCount() != 2 || len(f.autoPreset(t, id).AutoResumes) != 1 {
		t.Fatalf("一份排期只该开跑一次: runs=%d %+v", f.sim.runCount(), f.autoPreset(t, id).AutoResumes)
	}
}

// 预设保存：auto_resume 随预设一起存取。
func TestAutoResumeFlagRoundTripsThroughThePresetAPI(t *testing.T) {
	f, id := autoFixture(t, nil)
	launchers, err := f.env.store.ListTerminalLaunchers(context.Background())
	if err != nil || len(launchers) != 1 || launchers[0].Run == nil || !launchers[0].Run.AutoResume {
		t.Fatalf("auto_resume 应随预设保存: %+v %v", launchers, err)
	}
	f.saveLauncher(t, id, "续聊", map[string]any{"workflow_id": launcherWF})
	launchers, _ = f.env.store.ListTerminalLaunchers(context.Background())
	if launchers[0].Run.AutoResume {
		t.Fatal("没写 auto_resume 就是关闭")
	}
}

// #172：「续跑成功」的判定时长随预设保存；没写就是默认的 10 分钟；超过上限的收进上限；负数被拒绝。
func TestAutoResumeOKMinutesRoundTripsAndIsValidated(t *testing.T) {
	f, id := autoFixture(t, map[string]any{"auto_resume_ok_minutes": 45})
	launchers, err := f.env.store.ListTerminalLaunchers(context.Background())
	if err != nil || len(launchers) != 1 || launchers[0].Run == nil || launchers[0].Run.AutoResumeOKMinutes != 45 ||
		launchers[0].Run.EffectiveAutoResumeOK() != 45*time.Minute {
		t.Fatalf("auto_resume_ok_minutes 应随预设保存: %+v %v", launchers, err)
	}
	if got := f.env.hub.guardian.autoResumeOK(launchers[0]); got != 45*time.Minute {
		t.Fatalf("生效的判定时长应是预设里的 45 分钟: %v", got)
	}

	f.saveLauncher(t, id, "续聊", map[string]any{"workflow_id": launcherWF, "auto_resume": true})
	launchers, _ = f.env.store.ListTerminalLaunchers(context.Background())
	if launchers[0].Run.AutoResumeOKMinutes != 0 || launchers[0].Run.EffectiveAutoResumeOK() != 10*time.Minute {
		t.Fatalf("没写就是默认 10 分钟: %+v", launchers[0].Run)
	}

	f.saveLauncher(t, id, "续聊", map[string]any{"workflow_id": launcherWF, "auto_resume": true, "auto_resume_ok_minutes": 99999})
	launchers, _ = f.env.store.ListTerminalLaunchers(context.Background())
	if launchers[0].Run.AutoResumeOKMinutes != store.MaxAutoResumeOKMinutes {
		t.Fatalf("超过上限的收进上限（1 天）: %d", launchers[0].Run.AutoResumeOKMinutes)
	}

	res := f.env.call("PUT", "/api/v1/terminal/launchers/"+id, map[string]any{
		"name": "续聊", "host": f.a.code, "dir": "/work/app", "kind": "headless",
		"run": map[string]any{"workflow_id": launcherWF, "auto_resume": true, "auto_resume_ok_minutes": -3},
	}, f.adm...)
	if res.Status != http.StatusBadRequest || !strings.Contains(res.Raw, "续跑成功的判定时长不能是负数") {
		t.Fatalf("负数应被拒绝并说明: %d %s", res.Status, res.Raw)
	}
}

// 自定义的判定时长也能压过默认值（实验用的秒级覆盖更优先）。
func TestAutoResumeOKOverrideBeatsThePreset(t *testing.T) {
	f, _ := autoFixture(t, map[string]any{"auto_resume_ok_minutes": 45})
	launchers, _ := f.env.store.ListTerminalLaunchers(context.Background())
	g := f.env.hub.guardian
	if got := g.autoResumeOK(launchers[0]); got != 45*time.Minute {
		t.Fatalf("没有覆盖时取预设: %v", got)
	}
	g.h.opts.AutoResumeOK = 3 * time.Second
	if got := g.autoResumeOK(launchers[0]); got != 3*time.Second {
		t.Fatalf("覆盖优先: %v", got)
	}
	if got := g.autoResumeOK(store.TerminalLauncher{}); got != 3*time.Second {
		t.Fatalf("没有运行配置时覆盖也生效: %v", got)
	}
	g.h.opts.AutoResumeOK = 0
	if got := g.autoResumeOK(store.TerminalLauncher{}); got != autoResumeOKDefault {
		t.Fatalf("没有运行配置、没有覆盖：默认值: %v", got)
	}
}

// failAfter 让主机上的运行以退出码 1 结束，标准错误是可重试的错误；durs 依次是第 1、2、3…次运行报告的耗时，用完后沿用最后一个。
func (f *execFixture) failAfter(durs ...time.Duration) *atomic.Int32 {
	var n atomic.Int32
	f.sim.onRun = func(s *hostSim, m termproto.Run) {
		i := int(n.Add(1)) - 1
		if i >= len(durs) {
			i = len(durs) - 1
		}
		s.started(m.ID)
		s.endFull(termproto.RunEnd{
			ID: m.ID, State: termproto.RunExited, Code: intp(1), DurMS: durs[i].Milliseconds(),
			ErrBytes: int64(len(autoRetriableErr)), Err: []byte(autoRetriableErr), PrevHead: []byte(autoRetriableErr),
		})
	}
	return &n
}

// #172 主线：之前已经自动续跑过 2 次（还没被认定成功），这回续跑出来的运行连续跑满 11 分钟才又失败——
// 这次续跑算成功，累计清零：不再是「24 小时内已经续跑 3 次，达到上限」，而是重新排第 1 次、退避回到 5 分钟，下一次续跑照常开跑。
func TestAutoResumeKeepsGoingAfterAResumeRanLongEnough(t *testing.T) {
	f, id := autoFixture(t, nil)
	ctx, g, clock := context.Background(), f.env.hub.guardian, f.env.clock
	var ev autoEvents
	f.env.hub.SetAutoResumeHook(ev.hook)
	now := clock.Now()
	if _, err := f.env.store.UpdateTerminalGuardState(ctx, now, func(d *store.GuardStateDoc) {
		ps := d.Preset(id)
		for i, from := range []string{"x1", "x2"} {
			ps.AutoResumes = append(ps.AutoResumes, store.AutoResumeRecord{
				FromRun: from, RunID: from + "-next", State: store.AutoResumeStarted,
				PlannedAt: now.Add(-time.Duration(3-i) * time.Hour), StartedAt: now.Add(-time.Duration(3-i) * time.Hour),
			})
		}
	}); err != nil {
		t.Fatal(err)
	}
	// 第 1 次（你手动运行）很快失败；之后每一次续跑出来的运行都跑了 11 分钟才失败。
	f.failAfter(12*time.Millisecond, 11*time.Minute)

	first, err := f.runAt(id)
	if err != nil {
		t.Fatal(err)
	}
	recs := f.waitAutoRecords(t, id, 3)
	if recs[2].FromRun != first.RunID || recs[2].State != store.AutoResumePending || recs[2].Attempt != 3 || !recs[2].Due.Equal(now.Add(time.Hour)) {
		t.Fatalf("已续跑 2 次：这是第 3 次、退避 1 小时: %+v", recs[2])
	}

	clock.Advance(time.Hour)
	g.autoResumeTick(ctx, clock.Now()) // 第 3 次续跑开跑，跑了 11 分钟才失败：认定成功、次数清零，再排期
	recs = f.waitAutoRecords(t, id, 4)
	if recs[2].State != store.AutoResumeStarted || !recs[2].Settled || recs[2].Quick {
		t.Fatalf("跑满 10 分钟的续跑应认定成功: %+v", recs[2])
	}
	if recs[3].FromRun != recs[2].RunID || recs[3].State != store.AutoResumePending || recs[3].Attempt != 1 || !recs[3].Due.Equal(clock.Now().Add(5*time.Minute)) {
		t.Fatalf("清零之后重新排第 1 次、退避回到 5 分钟（而不是放弃）: %+v", recs[3])
	}
	f.waitChat(t, "聊天里写明续跑成功、次数清零", func(x []string) bool {
		return chatHas(x, "连续跑满 10 分钟", "续跑成功", "次数与退避从头算")
	})
	f.waitChat(t, "聊天里写明重新排第 1 次", func(x []string) bool { return chatHas(x, "系统计划 5 分钟后自动续跑", "第 1/3 次") })
	if got := ev.list(); len(got) != 0 {
		t.Fatalf("没有放弃，不该通知: %+v", got)
	}

	clock.Advance(5 * time.Minute)
	g.autoResumeTick(ctx, clock.Now()) // 清零之后的第 1 次续跑照常开跑，运行说明里标「自动续跑 1/3」
	eventually(t, autoWait, "假主机收到第 3 次运行（1 次手动 + 2 次真正开跑的续跑；之前那 2 次是预先写进去的登记）", func() bool { return f.sim.runCount() == 3 })
	recs = f.waitAutoRecords(t, id, 5)
	last := f.dbRun(t, recs[3].RunID)
	if recs[3].State != store.AutoResumeStarted || recs[3].Attempt != 1 || !strings.HasSuffix(last.Description, autoResumeSuffix(1, autoResumeMax)) {
		t.Fatalf("清零后的续跑应标 1/3: %+v / %q", recs[3], last.Description)
	}
}

// #187：提交续跑与「在登记里写上运行编号」之间隔着一次写聊天行和一次事务，运行可能在这之前就结束了（负载高时这段空窗不短）。
// 结束的处理不能依赖「登记里已经有运行编号」：它要靠运行说明末尾的「自动续跑 n/m」把那条已开跑、还没有编号的登记认领回来，
// 否则这次续跑既没被认定成功（跑满了也不清零）、也没被记成「没有进展」，下一次排期还把它当成占了名额而放弃。
// 这里直接以「结束先到」的顺序调用结束的处理，不靠计时，结果是确定的。
func TestAutoResumeEndedBeforeTheRunIDWasWrittenStillSettles(t *testing.T) {
	f, id := autoFixture(t, nil)
	ctx, g, clock := context.Background(), f.env.hub.guardian, f.env.clock
	now := clock.Now()
	if _, err := f.env.store.UpdateTerminalGuardState(ctx, now, func(d *store.GuardStateDoc) {
		ps := d.Preset(id)
		for i, from := range []string{"x1", "x2"} {
			ps.AutoResumes = append(ps.AutoResumes, store.AutoResumeRecord{
				FromRun: from, RunID: from + "-next", State: store.AutoResumeStarted, Attempt: i + 1,
				PlannedAt: now.Add(-time.Duration(3-i) * time.Hour), StartedAt: now.Add(-time.Duration(3-i) * time.Hour),
			})
		}
		// 第 3 次续跑刚占了名额、运行已经提交，登记里还没有运行编号。
		ps.AutoResumes = append(ps.AutoResumes, store.AutoResumeRecord{
			FromRun: "first", Attempt: 3, State: store.AutoResumeStarted, PlannedAt: now.Add(-time.Hour), Due: now, StartedAt: now,
		})
	}); err != nil {
		t.Fatal(err)
	}

	ended := model.TerminalRun{
		RunID: "resumed-run", CredentialName: launcherRunCaller, Thread: launcherThreadPrefix + id,
		State: model.RunFailed, ExitCode: intp(1), DurationMS: (11 * time.Minute).Milliseconds(),
		Description: "预设「续聊」的非交互续聊" + autoResumeSuffix(3, autoResumeMax), PreviewHead: autoRetriableErr,
	}
	g.autoResumeOnEnded(ended)

	recs := f.autoPreset(t, id).AutoResumes
	if len(recs) != 4 {
		t.Fatalf("应有 4 条登记（3 条续跑 + 清零之后重新排的那条）: %+v", recs)
	}
	if recs[2].RunID != ended.RunID || recs[2].State != store.AutoResumeStarted || !recs[2].Settled || recs[2].Quick {
		t.Fatalf("结束先到：还没有编号的那条登记应被认领，并因跑满 10 分钟认定成功（后面跟着的登记一起列出）: %+v", recs[2:])
	}
	if recs[3].FromRun != ended.RunID || recs[3].State != store.AutoResumePending || recs[3].Attempt != 1 || !recs[3].Due.Equal(now.Add(5*time.Minute)) {
		t.Fatalf("清零之后重新排第 1 次、退避回到 5 分钟（而不是放弃）: %+v", recs[3])
	}
	f.waitChat(t, "聊天里写明续跑成功", func(x []string) bool { return chatHas(x, "连续跑满 10 分钟", "续跑成功") })

	// 提交方随后补写同一个编号：幂等，不改变认领的结果。
	g.setAutoResume(ctx, id, "first", now, func(x *store.AutoResumeRecord) bool { x.RunID = ended.RunID; return true })
	if again := f.autoPreset(t, id).AutoResumes; len(again) != 4 || again[2].RunID != ended.RunID || !again[2].Settled {
		t.Fatalf("补写同一个编号不该改动结果: %+v", again)
	}
}

// 认领只按「自动续跑 n/m」：手动运行结束时，不会把别的、还没有编号的登记认领过去；次数对不上的续跑运行也不认领。
func TestAutoResumeEndedRunDoesNotClaimTheWrongRecord(t *testing.T) {
	f, id := autoFixture(t, nil)
	ctx, g, clock := context.Background(), f.env.hub.guardian, f.env.clock
	now := clock.Now()
	if _, err := f.env.store.UpdateTerminalGuardState(ctx, now, func(d *store.GuardStateDoc) {
		d.Preset(id).AutoResumes = append(d.Preset(id).AutoResumes, store.AutoResumeRecord{
			FromRun: "first", Attempt: 2, State: store.AutoResumeStarted, PlannedAt: now.Add(-time.Hour), Due: now, StartedAt: now,
		})
	}); err != nil {
		t.Fatal(err)
	}
	base := model.TerminalRun{
		CredentialName: launcherRunCaller, Thread: launcherThreadPrefix + id,
		State: model.RunFailed, ExitCode: intp(1), DurationMS: 12, PreviewHead: autoRetriableErr,
	}

	manual := base
	manual.RunID, manual.Description = "manual-run", "预设「续聊」的非交互续聊"
	g.autoResumeOnEnded(manual)
	wrongAttempt := base
	wrongAttempt.RunID, wrongAttempt.Description = "other-run", "预设「续聊」的非交互续聊"+autoResumeSuffix(3, autoResumeMax)
	g.autoResumeOnEnded(wrongAttempt)

	recs := f.autoPreset(t, id).AutoResumes
	if recs[0].RunID != "" || recs[0].Settled || recs[0].Quick {
		t.Fatalf("没带次数的手动运行、次数对不上的续跑运行，都不该认领那条还没有编号的登记: %+v", recs[0])
	}
	if len(recs) != 3 || recs[1].FromRun != "manual-run" || recs[2].FromRun != "other-run" {
		t.Fatalf("它们各自照常登记（各排一条）: %+v", recs)
	}
}

// 没跑满判定时长的续跑不会清零：2 次没有进展的续跑之后，预算与熔断照旧起作用（成功清零只对「跑出进展的」续跑生效）。
func TestAutoResumeDoesNotClearTheBudgetForAResumeThatDidNotRunLongEnough(t *testing.T) {
	f, id := autoFixture(t, nil)
	ctx, g, clock := context.Background(), f.env.hub.guardian, f.env.clock
	now := clock.Now()
	if _, err := f.env.store.UpdateTerminalGuardState(ctx, now, func(d *store.GuardStateDoc) {
		ps := d.Preset(id)
		for i, from := range []string{"x1", "x2"} {
			ps.AutoResumes = append(ps.AutoResumes, store.AutoResumeRecord{
				FromRun: from, RunID: from + "-next", State: store.AutoResumeStarted,
				PlannedAt: now.Add(-time.Duration(3-i) * time.Hour), StartedAt: now.Add(-time.Duration(3-i) * time.Hour),
			})
		}
	}); err != nil {
		t.Fatal(err)
	}
	f.failAfter(12*time.Millisecond, 9*time.Minute+59*time.Second) // 差一秒跑满 10 分钟

	if _, err := f.runAt(id); err != nil {
		t.Fatal(err)
	}
	f.waitAutoRecords(t, id, 3)
	clock.Advance(time.Hour)
	g.autoResumeTick(ctx, clock.Now()) // 第 3 次续跑：9 分 59 秒就失败 → 没有进展，不认定；预算已经是 3 次 → 放弃
	recs := f.waitAutoRecords(t, id, 4)
	if recs[2].Settled || !recs[2].Quick || recs[3].State != store.AutoResumeGaveUp || !strings.Contains(recs[3].Why, "24 小时内已经自动续跑 3 次") {
		t.Fatalf("差一点跑满不能清零，预算用完就放弃: %+v", recs)
	}
}

// 续跑出来的运行还在跑、已经连续跑满判定时长：不必等它结束就认定成功（只写一次聊天行）；没跑满的、不在跑的都不动。
func TestAutoResumeTickSettlesARunThatIsStillGoing(t *testing.T) {
	f, id := autoFixture(t, nil)
	ctx, g, clock := context.Background(), f.env.hub.guardian, f.env.clock
	f.failAfter(12 * time.Millisecond)
	if _, err := f.runAt(id); err != nil {
		t.Fatal(err)
	}
	f.waitAutoRecords(t, id, 1)
	// 第 2 次运行（续跑出来的）开始后一直不结束。
	f.sim.onRun = func(s *hostSim, m termproto.Run) { s.started(m.ID) }
	clock.Advance(5 * time.Minute)
	g.autoResumeTick(ctx, clock.Now())
	recs := f.waitAutoRecords(t, id, 1)
	eventually(t, autoWait, "续跑登记已开跑并带运行编号", func() bool {
		recs = f.autoPreset(t, id).AutoResumes
		return len(recs) >= 1 && recs[0].State == store.AutoResumeStarted && recs[0].RunID != ""
	})
	var run *model.TerminalRun
	eventually(t, autoWait, "续跑出来的运行已经开始", func() bool {
		run = f.dbRun(t, recs[0].RunID)
		return run.StartedAt != nil && !run.StartedAt.IsZero() && !model.IsFinalRunState(run.State)
	})

	started := *run.StartedAt
	g.autoResumeTick(ctx, started.Add(9*time.Minute)) // 才 9 分钟：不动
	if f.autoPreset(t, id).AutoResumes[0].Settled {
		t.Fatal("没跑满判定时长不该认定")
	}
	g.autoResumeTick(ctx, started.Add(10*time.Minute+time.Second)) // 跑满了：认定成功，聊天里写一行
	if !f.autoPreset(t, id).AutoResumes[0].Settled {
		t.Fatalf("还在跑、已跑满判定时长，应认定成功: %+v", f.autoPreset(t, id).AutoResumes[0])
	}
	f.waitChat(t, "聊天里写明续跑成功", func(x []string) bool { return chatHas(x, "连续跑满 10 分钟", "续跑成功") })
	g.autoResumeTick(ctx, started.Add(11*time.Minute)) // 再检查不会重复写
	n := 0
	for _, text := range f.chatTexts(launcherWF) {
		if strings.Contains(text, "续跑成功") {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("续跑成功的聊天行只该写一次: %d", n)
	}
}

// 手动运行、不是续跑出来的运行，跑得再久也不会改动守护状态（不为它们每次都写一遍）。
func TestAutoResumeSettleIgnoresRunsThatAreNotResumes(t *testing.T) {
	f, id := autoFixture(t, nil)
	ctx, g, clock := context.Background(), f.env.hub.guardian, f.env.clock
	launchers, _ := f.env.store.ListTerminalLaunchers(ctx)
	if g.settleAutoResume(ctx, launchers[0], "manual-run-1", 3*time.Hour, clock.Now()) {
		t.Fatal("不是续跑出来的运行不该被认定")
	}
	if len(f.autoPreset(t, id).AutoResumes) != 0 {
		t.Fatalf("不该留下任何登记: %+v", f.autoPreset(t, id).AutoResumes)
	}
}
