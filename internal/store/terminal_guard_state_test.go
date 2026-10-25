package store

import (
	"context"
	"fmt"
	"strings"
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

func TestGuardStateRoundTrip(t *testing.T) {
	st := newMemStore(t)
	ctx := context.Background()
	now := time.Date(2026, 10, 12, 9, 0, 0, 0, time.UTC)

	out, err := st.UpdateTerminalGuardState(ctx, now, func(d *GuardStateDoc) {
		p := d.Preset("p1")
		p.AutoResumes = append(p.AutoResumes, arStarted("run-1", "run-2", 3*time.Hour, now, false), arStarted("run-2", "run-3", 30*time.Minute, now, true))
	})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if out.Presets["p1"] == nil || len(out.Presets["p1"].AutoResumes) != 2 {
		t.Fatalf("写入结果应包含两条登记: %+v", out)
	}
	got, err := st.GetTerminalGuardState(ctx)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	p := got.Presets["p1"]
	if p == nil || len(p.AutoResumes) != 2 || p.AutoResumes[1].RunID != "run-3" || !p.AutoResumes[1].Quick {
		t.Fatalf("重读应一致: %+v", p)
	}
}

func TestGuardStateDropsExpiredAndEmptyEntries(t *testing.T) {
	st := newMemStore(t)
	ctx := context.Background()
	now := time.Date(2026, 10, 12, 9, 0, 0, 0, time.UTC)

	out, err := st.UpdateTerminalGuardState(ctx, now, func(d *GuardStateDoc) {
		d.Preset("busy").AutoResumes = []AutoResumeRecord{arStarted("run-1", "run-2", time.Hour, now, false)}
		d.Preset("stale").AutoResumes = []AutoResumeRecord{arStarted("run-8", "run-9", AutoResumeKeep+time.Hour, now, false)}
		d.Preset("blank") // 空条目不应被保存
		d.Presets["nil"] = nil
	})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if out.Presets["busy"] == nil {
		t.Fatalf("有登记的预设应保留: %+v", out.Presets)
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

// 会话守护下线（#245 第二期）之前存下的文档里还有 notified_episode / wakes / permit 这些字段：读得出（只认自动续跑），
// 下一次写入就丢掉它们；整份文档里只剩这些旧字段的预设按空条目清掉。回滚到旧版本也读得出新写的文档（旧字段本来就是可选的）。
func TestGuardStateIgnoresAndDropsLegacyGuardFields(t *testing.T) {
	st := newMemStore(t)
	ctx := context.Background()
	now := time.Date(2026, 10, 12, 9, 0, 0, 0, time.UTC)
	legacy := `{"presets":{` +
		`"p1":{"notified_episode":"stalled:100","notified_at":"2026-10-12T08:00:00Z","reminders":1,` +
		`"wakes":[{"at":"2026-10-12T07:00:00Z","mode":"type","reason":"长时间无活动"}],` +
		`"permit":{"permit":0,"set_by":"agent"},` +
		`"auto_resumes":[{"from_run":"run-1","attempt":1,"planned_at":"2026-10-12T08:30:00Z","state":"started","run_id":"run-2","started_at":"2026-10-12T08:31:00Z"}]},` +
		`"p2":{"notified_episode":"crashed:s1:1","wakes":[{"at":"2026-10-12T07:00:00Z","mode":"relaunch"}]}}}`
	if err := st.db.Save(&model.SystemSetting{Key: TerminalGuardStateKey, Value: legacy, UpdatedAt: now}).Error; err != nil {
		t.Fatalf("seed: %v", err)
	}
	doc, err := st.GetTerminalGuardState(ctx)
	if err != nil || doc.Presets["p1"] == nil || len(doc.Presets["p1"].AutoResumes) != 1 || doc.Presets["p1"].AutoResumes[0].RunID != "run-2" {
		t.Fatalf("旧文档里的自动续跑登记应读得出: %+v err=%v", doc, err)
	}
	// 任何一次写入都会把旧字段丢掉。
	if _, err := st.UpdateTerminalGuardState(ctx, now, func(d *GuardStateDoc) {}); err != nil {
		t.Fatalf("update: %v", err)
	}
	var rec model.SystemSetting
	if err := st.db.Where("key = ?", TerminalGuardStateKey).First(&rec).Error; err != nil {
		t.Fatal(err)
	}
	for _, gone := range []string{"notified_episode", "wakes", "permit", "reminders", "p2"} {
		if strings.Contains(rec.Value, gone) {
			t.Fatalf("写入之后不该再留着 %q: %s", gone, rec.Value)
		}
	}
	if !strings.Contains(rec.Value, "run-2") {
		t.Fatalf("自动续跑登记必须保留: %s", rec.Value)
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
	if _, err := st.UpdateTerminalGuardState(ctx, now, func(d *GuardStateDoc) {
		d.Preset("p").AutoResumes = []AutoResumeRecord{arStarted("run-1", "run-2", time.Hour, now, false)}
	}); err != nil {
		t.Fatalf("损坏后写入应成功: %v", err)
	}
	doc, _ = st.GetTerminalGuardState(ctx)
	if doc.Presets["p"] == nil || len(doc.Presets["p"].AutoResumes) != 1 {
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
