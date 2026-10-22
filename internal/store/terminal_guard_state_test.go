package store

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/teacat99/RelayMesh/v2/internal/model"
)

func TestGuardStateEmptyWhenNeverSaved(t *testing.T) {
	st := newMemStore(t)
	doc, err := st.GetTerminalGuardState(context.Background())
	if err != nil || doc.Presets == nil || len(doc.Presets) != 0 {
		t.Fatalf("从未保存过应返回空文档（Presets 非 nil）: %+v err=%v", doc, err)
	}
}

func TestGuardStateRoundTripAndAccessors(t *testing.T) {
	st := newMemStore(t)
	ctx := context.Background()
	now := time.Date(2026, 10, 12, 9, 0, 0, 0, time.UTC)

	out, err := st.UpdateTerminalGuardState(ctx, now, func(d *GuardStateDoc) {
		p := d.Preset("p1")
		p.NotifiedEpisode = "stalled:100"
		p.NotifiedAt = now
		p.Reminders = 1
		p.Wakes = append(p.Wakes,
			GuardWakeRecord{At: now.Add(-3 * time.Hour), Mode: "type", Reason: "长时间无活动", Episode: "stalled:100"},
			GuardWakeRecord{At: now.Add(-30 * time.Minute), Mode: "relaunch", Reason: "会话意外结束"},
		)
	})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if out.Presets["p1"] == nil || len(out.Presets["p1"].Wakes) != 2 {
		t.Fatalf("写入结果应包含两条唤醒记录: %+v", out)
	}

	got, err := st.GetTerminalGuardState(ctx)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	p := got.Presets["p1"]
	if p == nil || p.NotifiedEpisode != "stalled:100" || p.Reminders != 1 || !p.NotifiedAt.Equal(now) {
		t.Fatalf("重读应一致: %+v", p)
	}
	if n := p.WakesSince(now.Add(-24 * time.Hour)); n != 2 {
		t.Fatalf("24 小时内应有 2 次唤醒，得到 %d", n)
	}
	if n := p.WakesSince(now.Add(-time.Hour)); n != 1 {
		t.Fatalf("1 小时内应有 1 次唤醒，得到 %d", n)
	}
	last, ok := p.LastWake()
	if !ok || last.Mode != "relaunch" {
		t.Fatalf("最近一次应是重新拉起: %+v ok=%v", last, ok)
	}
	var nilState *GuardPresetState
	if _, ok := nilState.LastWake(); ok || nilState.WakesSince(now) != 0 {
		t.Fatalf("nil 状态应安全地返回零值")
	}
}

func TestGuardStateTrailingNoProgress(t *testing.T) {
	mk := func(progress ...string) *GuardPresetState {
		p := &GuardPresetState{}
		for _, pr := range progress {
			p.Wakes = append(p.Wakes, GuardWakeRecord{Mode: "type", Progress: pr})
		}
		return p
	}
	cases := []struct {
		name string
		p    *GuardPresetState
		want int
	}{
		{"nil", nil, 0},
		{"没有唤醒", mk(), 0},
		{"最近一次有动静", mk(GuardProgressNone, GuardProgressYes), 0},
		{"最近一次还没结论", mk(GuardProgressNone, ""), 0},
		{"最近一次没动静", mk(GuardProgressYes, GuardProgressNone), 1},
		{"连续两次没动静", mk(GuardProgressYes, GuardProgressNone, GuardProgressNone), 2},
		{"全部没动静", mk(GuardProgressNone, GuardProgressNone, GuardProgressNone), 3},
	}
	for _, tc := range cases {
		if got := tc.p.TrailingNoProgress(); got != tc.want {
			t.Errorf("%s: 期望 %d，得到 %d", tc.name, tc.want, got)
		}
	}
}

func TestGuardStateTrimsOldAndExcessWakesAndDropsEmpty(t *testing.T) {
	st := newMemStore(t)
	ctx := context.Background()
	now := time.Date(2026, 10, 12, 9, 0, 0, 0, time.UTC)

	out, err := st.UpdateTerminalGuardState(ctx, now, func(d *GuardStateDoc) {
		p := d.Preset("busy")
		for i := 0; i < GuardWakeKeepMax+7; i++ {
			p.Wakes = append(p.Wakes, GuardWakeRecord{At: now.Add(-time.Duration(GuardWakeKeepMax+7-i) * time.Minute), Mode: "type", Reason: fmt.Sprintf("r%d", i)})
		}
		old := d.Preset("stale")
		old.Wakes = []GuardWakeRecord{{At: now.Add(-GuardWakeKeep - time.Hour), Mode: "type"}}
		d.Preset("blank") // 空条目不应被保存
		d.Presets["nil"] = nil
	})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	busy := out.Presets["busy"]
	if busy == nil || len(busy.Wakes) != GuardWakeKeepMax {
		t.Fatalf("超量应只留最近 %d 条: %+v", GuardWakeKeepMax, busy)
	}
	if busy.Wakes[len(busy.Wakes)-1].Reason != fmt.Sprintf("r%d", GuardWakeKeepMax+6) {
		t.Fatalf("应保留最新的，丢掉最旧的: 最后一条是 %q", busy.Wakes[len(busy.Wakes)-1].Reason)
	}
	for _, id := range []string{"stale", "blank", "nil"} {
		if _, ok := out.Presets[id]; ok {
			t.Fatalf("预设 %q 应被清掉（过期 / 空 / nil）", id)
		}
	}

	reread, _ := st.GetTerminalGuardState(ctx)
	if len(reread.Presets) != 1 || reread.Presets["busy"] == nil {
		t.Fatalf("落盘的也应只剩 busy: %+v", reread.Presets)
	}
}

func TestGuardStateKeepsNotifiedEpisodeWithoutWakes(t *testing.T) {
	st := newMemStore(t)
	ctx := context.Background()
	now := time.Date(2026, 10, 12, 9, 0, 0, 0, time.UTC)
	if _, err := st.UpdateTerminalGuardState(ctx, now, func(d *GuardStateDoc) {
		d.Preset("p").NotifiedEpisode = "crashed:s1:1"
	}); err != nil {
		t.Fatalf("update: %v", err)
	}
	// 之后别的预设的写入不能把它清掉。
	if _, err := st.UpdateTerminalGuardState(ctx, now.Add(time.Minute), func(d *GuardStateDoc) {
		d.Preset("q").Reminders = 1
	}); err != nil {
		t.Fatalf("update: %v", err)
	}
	got, _ := st.GetTerminalGuardState(ctx)
	if got.Presets["p"] == nil || got.Presets["p"].NotifiedEpisode != "crashed:s1:1" {
		t.Fatalf("已推送的事件标识应被保留: %+v", got.Presets["p"])
	}
}

func TestGuardStateBrokenDocumentIsTreatedAsEmpty(t *testing.T) {
	st := newMemStore(t)
	ctx := context.Background()
	now := time.Date(2026, 10, 12, 9, 0, 0, 0, time.UTC)
	if err := st.db.Save(&model.SystemSetting{Key: TerminalGuardStateKey, Value: "{not json", UpdatedAt: now}).Error; err != nil {
		t.Fatalf("seed: %v", err)
	}
	doc, err := st.GetTerminalGuardState(ctx)
	if err != nil || doc.Presets == nil || len(doc.Presets) != 0 {
		t.Fatalf("损坏的存档应按空处理: %+v err=%v", doc, err)
	}
	// 损坏之后还能正常写。
	if _, err := st.UpdateTerminalGuardState(ctx, now, func(d *GuardStateDoc) { d.Preset("p").Reminders = 2 }); err != nil {
		t.Fatalf("损坏后写入应成功: %v", err)
	}
	doc, _ = st.GetTerminalGuardState(ctx)
	if doc.Presets["p"] == nil || doc.Presets["p"].Reminders != 2 {
		t.Fatalf("写入后应能读回: %+v", doc)
	}
}

// ---------- 自动续跑登记（#151） ----------

func arStarted(from, run string, startedAgo time.Duration, now time.Time, quick bool) AutoResumeRecord {
	return AutoResumeRecord{FromRun: from, RunID: run, State: AutoResumeStarted, PlannedAt: now.Add(-startedAgo), StartedAt: now.Add(-startedAgo), Quick: quick}
}

func TestGuardStateAutoResumeAccessors(t *testing.T) {
	now := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	p := &GuardPresetState{AutoResumes: []AutoResumeRecord{
		arStarted("r1", "r2", 30*time.Hour, now, true),
		arStarted("r2", "r3", 5*time.Hour, now, false),
		{FromRun: "r3", State: AutoResumePending, Attempt: 2, PlannedAt: now, Due: now.Add(time.Minute)},
		{FromRun: "r9", State: AutoResumeGaveUp, Why: "达到上限", PlannedAt: now},
	}}
	if n := p.AutoResumeStartedSince(now.Add(-24 * time.Hour)); n != 1 {
		t.Fatalf("24 小时内开跑的应只有 1 次（30 小时前的不算，放弃的不算）: %d", n)
	}
	if n := p.AutoResumeStartedSince(now.Add(-48 * time.Hour)); n != 2 {
		t.Fatalf("48 小时内开跑的应有 2 次: %d", n)
	}
	if r, ok := p.AutoResumeFrom("r3"); !ok || r.State != AutoResumePending || r.Attempt != 2 {
		t.Fatalf("按出事的运行找: %+v %v", r, ok)
	}
	if _, ok := p.AutoResumeFrom("nope"); ok {
		t.Fatal("没有的运行不该找到")
	}
	if r, ok := p.AutoResumeStarting("r3"); !ok || r.FromRun != "r2" {
		t.Fatalf("r3 是由 r2 出事之后续跑出来的: %+v %v", r, ok)
	}
	if _, ok := p.AutoResumeStarting(""); ok {
		t.Fatal("空编号不该找到")
	}
	var nilState *GuardPresetState
	if nilState.AutoResumeStartedSince(now) != 0 || nilState.AutoResumeTrailingQuick() != 0 {
		t.Fatal("nil 状态应安全地返回零值")
	}
	if _, ok := nilState.AutoResumeFrom("r1"); ok {
		t.Fatal("nil 状态不该找到")
	}
}

func TestGuardStateAutoResumeTrailingQuick(t *testing.T) {
	now := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name string
		recs []AutoResumeRecord
		want int
	}{
		{"没有登记", nil, 0},
		{"末尾是待开跑", []AutoResumeRecord{arStarted("a", "b", time.Hour, now, true), {FromRun: "b", State: AutoResumePending}}, 0},
		{"一次没进展", []AutoResumeRecord{arStarted("a", "b", time.Hour, now, true)}, 1},
		{"一次有进展", []AutoResumeRecord{arStarted("a", "b", time.Hour, now, false)}, 0},
		{"连续两次没进展（链连着）", []AutoResumeRecord{arStarted("a", "b", 2*time.Hour, now, true), arStarted("b", "c", time.Hour, now, true)}, 2},
		{"中间有一次有进展：只数末尾那段", []AutoResumeRecord{
			arStarted("a", "b", 3*time.Hour, now, true), arStarted("b", "c", 2*time.Hour, now, false), arStarted("c", "d", time.Hour, now, true),
		}, 1},
		{"链断了（你手动运行过，出事的运行对不上）", []AutoResumeRecord{arStarted("a", "b", 2*time.Hour, now, true), arStarted("x", "y", time.Hour, now, true)}, 1},
		{"末尾是放弃：新的一串从头算", []AutoResumeRecord{
			arStarted("a", "b", 2*time.Hour, now, true), arStarted("b", "c", time.Hour, now, true), {FromRun: "c", State: AutoResumeGaveUp},
		}, 0},
	}
	for _, c := range cases {
		if got := (&GuardPresetState{AutoResumes: c.recs}).AutoResumeTrailingQuick(); got != c.want {
			t.Errorf("%s: 期望 %d，得到 %d", c.name, c.want, got)
		}
	}
}

// #172：成功的续跑（Settled）让预算用量清零——从最近一次成功的续跑之后才开始数，它自己和它之前的都不算。
func TestGuardStateAutoResumeStartedSinceStartsOverAfterASettledResume(t *testing.T) {
	now := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	since := now.Add(-24 * time.Hour)
	settled := func(r AutoResumeRecord) AutoResumeRecord { r.Settled = true; return r }
	cases := []struct {
		name string
		recs []AutoResumeRecord
		want int
	}{
		{"没有成功的：老样子", []AutoResumeRecord{arStarted("a", "b", 20*time.Hour, now, false), arStarted("b", "c", time.Hour, now, true)}, 2},
		{"最近一次成功：清零", []AutoResumeRecord{
			arStarted("a", "b", 20*time.Hour, now, false), arStarted("b", "c", 10*time.Hour, now, false), settled(arStarted("c", "d", time.Hour, now, false)),
		}, 0},
		{"成功之后又续跑了 2 次：只数这 2 次", []AutoResumeRecord{
			arStarted("a", "b", 20*time.Hour, now, false), settled(arStarted("b", "c", 10*time.Hour, now, false)),
			arStarted("c", "d", 5*time.Hour, now, true), arStarted("d", "e", time.Hour, now, true),
		}, 2},
		{"以最近一次成功为界（更早的成功不影响）", []AutoResumeRecord{
			settled(arStarted("a", "b", 20*time.Hour, now, false)), arStarted("b", "c", 15*time.Hour, now, true),
			settled(arStarted("c", "d", 10*time.Hour, now, false)), arStarted("d", "e", 2*time.Hour, now, true),
		}, 1},
		{"成功的那次本身滑出了 24 小时窗口：照常数窗口里的", []AutoResumeRecord{
			settled(arStarted("a", "b", 30*time.Hour, now, false)), arStarted("b", "c", 5*time.Hour, now, true),
		}, 1},
		{"没有开跑的登记（放弃 / 待开跑）带着 Settled 也不改变计数", []AutoResumeRecord{
			arStarted("a", "b", 5*time.Hour, now, true), {FromRun: "b", State: AutoResumeGaveUp, Settled: true, StartedAt: now.Add(-time.Hour)},
		}, 1},
	}
	for _, c := range cases {
		if got := (&GuardPresetState{AutoResumes: c.recs}).AutoResumeStartedSince(since); got != c.want {
			t.Errorf("%s: 期望 %d，得到 %d", c.name, c.want, got)
		}
	}
}

// 成功认定之后，连续没进展的计数也从头算：认定会去掉 Quick，「末尾那段没进展」自然断开。
func TestGuardStateSettleAutoResumeEndsTheQuickStreak(t *testing.T) {
	now := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	p := &GuardPresetState{AutoResumes: []AutoResumeRecord{
		arStarted("a", "b", 3*time.Hour, now, true), arStarted("b", "c", 2*time.Hour, now, true),
	}}
	if n := p.AutoResumeTrailingQuick(); n != 2 {
		t.Fatalf("认定之前连续 2 次没进展: %d", n)
	}
	if !p.SettleAutoResume("c") {
		t.Fatal("c 是已开跑的续跑，应认定")
	}
	if r := p.AutoResumes[1]; !r.Settled || r.Quick {
		t.Fatalf("认定应标 Settled 并去掉 Quick: %+v", r)
	}
	if n := p.AutoResumeTrailingQuick(); n != 0 {
		t.Fatalf("认定之后末尾不是「没进展」: %d", n)
	}
	// Settled 随登记落库、读回来仍在（新字段向后兼容：旧存档没有它，读出来是 false）。
	st := newMemStore(t)
	ctx := context.Background()
	if _, err := st.UpdateTerminalGuardState(ctx, now, func(d *GuardStateDoc) { d.Preset("p1").AutoResumes = p.AutoResumes }); err != nil {
		t.Fatal(err)
	}
	doc, err := st.GetTerminalGuardState(ctx)
	if err != nil {
		t.Fatal(err)
	}
	got := doc.Presets["p1"].AutoResumes
	if len(got) != 2 || got[0].Settled || !got[1].Settled {
		t.Fatalf("Settled 应随登记保存: %+v", got)
	}
}

func TestGuardStatePrunesAutoResumesButKeepsPendingOnes(t *testing.T) {
	st := newMemStore(t)
	ctx := context.Background()
	now := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)

	out, err := st.UpdateTerminalGuardState(ctx, now, func(d *GuardStateDoc) {
		p := d.Preset("busy")
		p.AutoResumes = append(p.AutoResumes,
			AutoResumeRecord{FromRun: "old", State: AutoResumeSkipped, PlannedAt: now.Add(-AutoResumeKeep - time.Hour)},
			AutoResumeRecord{FromRun: "old-pending", State: AutoResumePending, PlannedAt: now.Add(-AutoResumeKeep - time.Hour), Due: now.Add(-time.Hour)},
		)
		for i := 0; i < AutoResumeKeepMax+5; i++ {
			p.AutoResumes = append(p.AutoResumes, AutoResumeRecord{FromRun: fmt.Sprintf("r%d", i), State: AutoResumeSkipped, PlannedAt: now.Add(-time.Duration(AutoResumeKeepMax+5-i) * time.Minute)})
		}
		stale := d.Preset("stale")
		stale.AutoResumes = []AutoResumeRecord{{FromRun: "x", State: AutoResumeStopped, PlannedAt: now.Add(-AutoResumeKeep - time.Hour)}}
	})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	busy := out.Presets["busy"]
	if busy == nil || len(busy.AutoResumes) != AutoResumeKeepMax {
		t.Fatalf("超量应只留最近 %d 条: %+v", AutoResumeKeepMax, busy)
	}
	if busy.AutoResumes[len(busy.AutoResumes)-1].FromRun != fmt.Sprintf("r%d", AutoResumeKeepMax+4) {
		t.Fatalf("应保留最新的: 最后一条是 %q", busy.AutoResumes[len(busy.AutoResumes)-1].FromRun)
	}
	for _, r := range busy.AutoResumes {
		if r.FromRun == "old" {
			t.Fatal("过期的非待开跑登记应被清掉")
		}
	}
	if _, ok := out.Presets["stale"]; ok {
		t.Fatal("只有过期登记的预设应被清掉")
	}

	// 待开跑的登记不受时间限制（到点没跑成会被标成跳过）：单独验证它不会被按时间清掉。
	out, err = st.UpdateTerminalGuardState(ctx, now, func(d *GuardStateDoc) {
		d.Presets = map[string]*GuardPresetState{}
		d.Preset("p").AutoResumes = []AutoResumeRecord{{FromRun: "old-pending", State: AutoResumePending, PlannedAt: now.Add(-AutoResumeKeep - time.Hour), Due: now.Add(-time.Hour)}}
	})
	if err != nil {
		t.Fatal(err)
	}
	if p := out.Presets["p"]; p == nil || len(p.AutoResumes) != 1 {
		t.Fatalf("待开跑的登记不该被按时间清掉: %+v", p)
	}
	reread, _ := st.GetTerminalGuardState(ctx)
	if p := reread.Presets["p"]; p == nil || p.AutoResumes[0].State != AutoResumePending || !p.AutoResumes[0].Due.Equal(now.Add(-time.Hour)) {
		t.Fatalf("落盘后应一致: %+v", p)
	}
}
