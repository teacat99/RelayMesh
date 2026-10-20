package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/teacat99/RelayMesh/v2/internal/asyncmode"
	"github.com/teacat99/RelayMesh/v2/internal/model"
)

// 重启类测试用 openFileStore（jwtsecret_test.go）打开 / 重新打开落盘的库：:memory: 库关掉就没了。

func savedHookSeen(t *testing.T, st *Store, wf string) (time.Time, bool) {
	t.Helper()
	var rows []model.WorkflowHookSeen
	if err := st.db.Where("workflow_id = ?", wf).Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	if len(rows) == 0 {
		return time.Time{}, false
	}
	return rows[0].SeenAt, true
}

// #103：Hub 重启（含热更新）后，开着异步的工作流不必等 hook 再来一次才恢复。
// 这是用户可感知的那一条：重启前有效，重启后立刻仍然有效，而不是先按 no_hook 降级成同步。
func TestHookSeenSurvivesRestart(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "hook.db")
	now := time.Now()

	st1 := openFileStore(t, path)
	st1.RecordHookSeen("wf-a", now)
	if _, err := st1.UpdateWorkflowMode(ctx, "wf-a", ModePatch{Enabled: boolPtr(true)}, now); err != nil {
		t.Fatal(err)
	}
	if info, _ := st1.WorkflowModeInfo(ctx, "wf-a", now); !info.State.Effective {
		t.Fatalf("重启前应有效: %+v", info.State)
	}
	if err := st1.Close(); err != nil {
		t.Fatal(err)
	}

	st2 := openFileStore(t, path) // 重启：内存是空的，只有库里的东西
	at, ok := st2.HookLastSeen("wf-a")
	if !ok || !at.Equal(now) {
		t.Fatalf("重启后应恢复 hook 时间: ok=%v at=%v want=%v", ok, at, now)
	}
	later := now.Add(5 * time.Second)
	info, err := st2.WorkflowModeInfo(ctx, "wf-a", later)
	if err != nil {
		t.Fatal(err)
	}
	if !info.State.Effective || info.State.Reason != asyncmode.ReasonNone {
		t.Fatalf("重启后异步应继续有效，而不是按 no_hook 降级: %+v", info.State)
	}
	if info.HookSeen.IsZero() {
		t.Fatalf("ModeInfo.HookSeen 应有值（网页的「能否开启」与 hook 状态都读它）: %+v", info)
	}
	// 没见过 hook 的工作流不会凭空出现痕迹
	if _, ok := st2.HookLastSeen("wf-never"); ok {
		t.Fatalf("没见过的工作流不应有 hook 时间")
	}
}

// 恢复出来的只是重启前内存里本来就有的那个时间，没有额外宽限：停机久了再起来，照旧按 hook_stale 降级
// （不是 no_hook——对界面与回执来说「hook 太久没出现」比「从没装过」更准确）。
func TestRestoredHookSeenStillExpires(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "hook.db")
	t0 := time.Now().Add(-3 * time.Hour)

	st1 := openFileStore(t, path)
	st1.RecordHookSeen("wf-a", t0)
	if _, err := st1.UpdateWorkflowMode(ctx, "wf-a", ModePatch{Enabled: boolPtr(true)}, t0); err != nil {
		t.Fatal(err)
	}
	_ = st1.Close()

	st2 := openFileStore(t, path)
	info, err := st2.WorkflowModeInfo(ctx, "wf-a", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if info.State.Effective || !info.State.Enabled || info.State.Reason != asyncmode.ReasonHookStale {
		t.Fatalf("3 小时前的 hook 恢复后应是 hook_stale，不能因为重启而变新鲜: %+v", info.State)
	}
	// 「见过 hook」这件事也跟着恢复：用户可以先开着、等 hook 回来自动生效（UpdateWorkflowMode 的契约）
	if _, err := st2.UpdateWorkflowMode(ctx, "wf-a", ModePatch{Enabled: boolPtr(false)}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := st2.UpdateWorkflowMode(ctx, "wf-a", ModePatch{Enabled: boolPtr(true)}, time.Now()); err != nil {
		t.Fatalf("见过 hook 的工作流重启后应仍允许开启: %v", err)
	}
}

// 落库有节流：hook 每十几秒来一次，不值得次次写库；但内存里的值始终跟着最新一次走。
func TestHookSeenPersistIsThrottled(t *testing.T) {
	st := openFileStore(t, filepath.Join(t.TempDir(), "hook.db"))
	t0 := time.Date(2026, 10, 15, 12, 0, 0, 0, time.Local)

	st.RecordHookSeen("wf-t", t0)
	if got, ok := savedHookSeen(t, st, "wf-t"); !ok || !got.Equal(t0) {
		t.Fatalf("第一次应立刻落库: ok=%v got=%v", ok, got)
	}
	st.RecordHookSeen("wf-t", t0.Add(10*time.Second))
	st.RecordHookSeen("wf-t", t0.Add(HookSeenPersistEvery-time.Second))
	if got, _ := savedHookSeen(t, st, "wf-t"); !got.Equal(t0) {
		t.Fatalf("节流间隔内不应再写库: got=%v want=%v", got, t0)
	}
	if mem, _ := st.HookLastSeen("wf-t"); !mem.Equal(t0.Add(HookSeenPersistEvery - time.Second)) {
		t.Fatalf("内存里的值不受节流影响，应是最新一次: %v", mem)
	}
	st.RecordHookSeen("wf-t", t0.Add(HookSeenPersistEvery))
	if got, _ := savedHookSeen(t, st, "wf-t"); !got.Equal(t0.Add(HookSeenPersistEvery)) {
		t.Fatalf("到了间隔应再次落库: got=%v", got)
	}
	// 空工作流编号不落库、不报错
	st.RecordHookSeen("", t0)
	var n int64
	st.db.Model(&model.WorkflowHookSeen{}).Count(&n)
	if n != 1 {
		t.Fatalf("库里应只有 wf-t 一行: %d", n)
	}
}

// 落库值最多比真实值旧一个节流间隔：重启恢复出来的值只会偏旧，不会偏新。
func TestRestoredHookSeenNeverNewerThanReal(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hook.db")
	t0 := time.Date(2026, 10, 15, 12, 0, 0, 0, time.Local)
	st1 := openFileStore(t, path)
	var real time.Time
	for i := 0; i < 20; i++ { // 每 15 秒一次，共 5 分钟，和真实 hook 节奏一致
		real = t0.Add(time.Duration(i) * 15 * time.Second)
		st1.RecordHookSeen("wf-a", real)
	}
	_ = st1.Close()

	st2 := openFileStore(t, path)
	got, ok := st2.HookLastSeen("wf-a")
	if !ok {
		t.Fatal("应恢复")
	}
	if got.After(real) {
		t.Fatalf("恢复值不能比真实值新: got=%v real=%v", got, real)
	}
	if real.Sub(got) > HookSeenPersistEvery {
		t.Fatalf("恢复值最多比真实值旧一个节流间隔: 差 %v", real.Sub(got))
	}
}

// 启动时的恢复不覆盖内存里更新的值（正常启动时内存是空的，这条守的是以后有人在别处提前写了内存）。
func TestRestoreHookSeenDoesNotClobberNewerMemory(t *testing.T) {
	ctx := context.Background()
	st := openFileStore(t, filepath.Join(t.TempDir(), "hook.db"))
	t1 := time.Date(2026, 10, 15, 12, 0, 0, 0, time.Local)
	st.RecordHookSeen("wf-a", t1)                     // 落库 t1
	st.RecordHookSeen("wf-a", t1.Add(10*time.Second)) // 内存更新，库里仍是 t1
	if err := st.RestoreHookSeen(ctx); err != nil {
		t.Fatal(err)
	}
	if got, _ := st.HookLastSeen("wf-a"); !got.Equal(t1.Add(10 * time.Second)) {
		t.Fatalf("恢复不应把内存里更新的值倒退回库里的旧值: %v", got)
	}
}

// 太久以前的记录在启动时清掉（没有恢复意义，也免得表无限增长）；近的恢复。
func TestRestoreHookSeenPrunesAncientRows(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hook.db")
	st1 := openFileStore(t, path)
	now := time.Now()
	for wf, at := range map[string]time.Time{
		"wf-ancient": now.Add(-hookSeenKeep - 24*time.Hour),
		"wf-recent":  now.Add(-time.Hour),
	} {
		if err := st1.db.Create(&model.WorkflowHookSeen{WorkflowID: wf, SeenAt: at.In(time.Local)}).Error; err != nil {
			t.Fatal(err)
		}
	}
	_ = st1.Close()

	st2 := openFileStore(t, path)
	if _, ok := st2.HookLastSeen("wf-ancient"); ok {
		t.Fatalf("超过保留期的记录不应恢复")
	}
	if _, ok := savedHookSeen(t, st2, "wf-ancient"); ok {
		t.Fatalf("超过保留期的记录应在启动时被清掉")
	}
	if at, ok := st2.HookLastSeen("wf-recent"); !ok || now.Sub(at) > 2*time.Hour {
		t.Fatalf("近的记录应恢复: ok=%v at=%v", ok, at)
	}
}

// 恢复后新的 hook 事件照常工作：节流基线来自库里的值，不会因为刚恢复就立刻重复写，也不会永远不写。
func TestHookSeenKeepsPersistingAfterRestore(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hook.db")
	t0 := time.Date(2026, 10, 15, 12, 0, 0, 0, time.Local)
	st1 := openFileStore(t, path)
	st1.RecordHookSeen("wf-a", t0)
	_ = st1.Close()

	st2 := openFileStore(t, path)
	st2.RecordHookSeen("wf-a", t0.Add(20*time.Second)) // 基线是 t0：间隔内不写
	if got, _ := savedHookSeen(t, st2, "wf-a"); !got.Equal(t0) {
		t.Fatalf("恢复后间隔内不应写库: %v", got)
	}
	st2.RecordHookSeen("wf-a", t0.Add(2*time.Minute))
	if got, _ := savedHookSeen(t, st2, "wf-a"); !got.Equal(t0.Add(2 * time.Minute)) {
		t.Fatalf("恢复后超过间隔应写库: %v", got)
	}
}
