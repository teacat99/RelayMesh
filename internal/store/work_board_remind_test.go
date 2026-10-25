package store

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/teacat99/RelayMesh/v2/internal/model"
)

// 「指派久无人接」的一次性提醒（#180 B3d）：什么时候算到点、只提醒一次、重新派出后再有一次、不动任务本身。

const staleTwoHours = 2 * time.Hour

// reload 重新读一遍任务（含不对外返回的 offer_reminded_at）。
func reload(t *testing.T, st *Store, id uint) model.WorkItem {
	t.Helper()
	var it model.WorkItem
	if err := st.db.First(&it, id).Error; err != nil {
		t.Fatal(err)
	}
	return it
}

func claim(t *testing.T, st *Store, ctx context.Context, now time.Time, max int) []BoardStaleOffer {
	t.Helper()
	got, err := st.ClaimStaleOffers(ctx, now, staleTwoHours, max)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

// seedBoardWorkflows 让这些工作流的会话目录落在 boardFixture 的那张任务板上（否则不能被指派）。
func seedBoardWorkflows(t *testing.T, st *Store, wfs ...string) {
	t.Helper()
	for _, wf := range wfs {
		seedWorkflowDir(t, st, wf, "devhost", "/home/user/a", time.Now())
	}
}

// setAssignedAt 把任务的派出时间改成确定的值，让「到点」的判断不依赖测试跑得多快。
func setAssignedAt(t *testing.T, st *Store, id uint, at time.Time) {
	t.Helper()
	if err := st.db.Model(&model.WorkItem{}).Where("id = ?", id).UpdateColumn("assigned_at", at).Error; err != nil {
		t.Fatal(err)
	}
}

// 到点才提醒（「等于即算」，差 1 纳秒不算）；提醒一次之后同一轮不再出现；按承接者成组；标题只在单个指派时带。
func TestClaimStaleOffersDueAndOnce(t *testing.T) {
	st, ctx, _ := boardFixture(t)
	seedBoardWorkflows(t, st, "wf-3")
	mgr := managerAs("wf-1", WorkPolicyEnforce)

	// 先在另一块（独立的）任务板上垫一个任务：让数据库编号与板内序号错开。只有一块板、从头建任务时两者恰好相等，
	// 提醒深链用错了编号（#N 当成数据库编号）就测不出来。
	seedWorkflowDir(t, st, "wf-solo", "devhost", "/home/user/solo", time.Now())
	mustCreate(t, st, WorkItemInput{WorkflowID: "wf-solo", Title: "垫一个"}, managerAs("wf-solo", WorkPolicyEnforce))

	a1 := mustCreate(t, st, WorkItemInput{WorkflowID: "wf-1", Title: "派给 wf-2 一", Assignee: "wf-2"}, mgr)
	a2 := mustCreate(t, st, WorkItemInput{WorkflowID: "wf-1", Title: "派给 wf-2 二", Assignee: "wf-2"}, mgr)
	b1 := mustCreate(t, st, WorkItemInput{WorkflowID: "wf-1", Title: "派给 wf-3", Assignee: "wf-3"}, mgr)
	if uint(b1.Seq) == b1.ID {
		t.Fatalf("测试前提：数据库编号应与板内序号错开: id=%d seq=%d", b1.ID, b1.Seq)
	}
	base := time.Now().Add(-10 * time.Hour).Truncate(time.Second)
	setAssignedAt(t, st, a1.ID, base)
	setAssignedAt(t, st, a2.ID, base)
	setAssignedAt(t, st, b1.ID, base.Add(time.Minute))

	if got := claim(t, st, ctx, base.Add(staleTwoHours-time.Nanosecond), 0); len(got) != 0 {
		t.Fatalf("差 1 纳秒不算到点: %+v", got)
	}
	// 到点：只有最早派出的 wf-2 那两个（b1 晚了一分钟）；两个编号、不带标题。
	got := claim(t, st, ctx, base.Add(staleTwoHours), 0)
	if len(got) != 1 || got[0].AssigneeWorkflowID != "wf-2" || fmt.Sprint(got[0].Seqs) != fmt.Sprint([]int{a1.Seq, a2.Seq}) || got[0].Title != "" || got[0].ID != 0 {
		t.Fatalf("到点的应只有 wf-2 的两个，列出编号且不带标题、不带数据库编号: %+v", got)
	}
	if !got[0].OldestAt.Equal(base) {
		t.Fatalf("OldestAt 应是最早的派出时间: %v vs %v", got[0].OldestAt, base)
	}
	// 一分钟后 b1 到点：单个指派带标题；wf-2 的两个不再出现。
	got = claim(t, st, ctx, base.Add(staleTwoHours+time.Minute), 0)
	if len(got) != 1 || got[0].AssigneeWorkflowID != "wf-3" || fmt.Sprint(got[0].Seqs) != fmt.Sprint([]int{b1.Seq}) || got[0].Title != "派给 wf-3" {
		t.Fatalf("接着到点的是 wf-3 的那个，带标题: %+v", got)
	}
	if got[0].ID != b1.ID {
		t.Fatalf("单个指派要带它的数据库编号（任务页深链用），而不是板内序号: got %d want id %d (seq %d)", got[0].ID, b1.ID, b1.Seq)
	}
	// 同一轮不再出现，之后多久都不会。
	for _, d := range []time.Duration{staleTwoHours + time.Minute, 100 * time.Hour} {
		if again := claim(t, st, ctx, base.Add(d), 0); len(again) != 0 {
			t.Fatalf("已提醒的不该再出现（%v）: %+v", d, again)
		}
	}
	if it := reload(t, st, a1.ID); it.OfferRemindedAt == nil || !it.OfferRemindedAt.Equal(base.Add(staleTwoHours)) {
		t.Fatalf("标记应记下提醒时刻: %v", it.OfferRemindedAt)
	}
}

// 不算「久无人接」的：已接取、已拒绝、已搁置 / 已完成、自己派给自己、没有指派；人派的算。
func TestClaimStaleOffersOnlyOpenOffers(t *testing.T) {
	st, ctx, _ := boardFixture(t)
	seedBoardWorkflows(t, st, "wf-3")
	mgr := managerAs("wf-1", WorkPolicyEnforce)
	dev := agentAs("wf-2", WorkPolicyEnforce)
	human := WorkActor{Kind: model.WorkActorHuman}

	accepted := mustCreate(t, st, WorkItemInput{WorkflowID: "wf-1", Title: "已接取", Assignee: "wf-2"}, mgr)
	mustUpdate(t, st, accepted.ID, WorkItemPatch{Claim: "accept"}, dev)
	declined := mustCreate(t, st, WorkItemInput{WorkflowID: "wf-1", Title: "已拒绝", Assignee: "wf-2"}, mgr)
	mustUpdate(t, st, declined.ID, WorkItemPatch{Claim: "decline", Note: "排满了"}, dev)
	shelved := mustCreate(t, st, WorkItemInput{WorkflowID: "wf-1", Title: "已搁置", Assignee: "wf-2"}, mgr)
	mustUpdate(t, st, shelved.ID, WorkItemPatch{State: sp(model.WorkStateShelved), Conclusion: sp("先不做")}, mgr)
	mustCreate(t, st, WorkItemInput{WorkflowID: "wf-1", Title: "自己做", Assignee: "wf-1"}, mgr)
	mustCreate(t, st, WorkItemInput{WorkflowID: "wf-1", Title: "没有指派"}, mgr)
	byHuman := mustCreate(t, st, WorkItemInput{WorkflowID: "wf-1", Title: "用户派的", Assignee: "wf-3"}, human)

	got := claim(t, st, ctx, time.Now().Add(100*time.Hour), 0)
	if len(got) != 1 || got[0].AssigneeWorkflowID != "wf-3" || fmt.Sprint(got[0].Seqs) != fmt.Sprint([]int{byHuman.Seq}) {
		t.Fatalf("只有用户派给 wf-3 的那个还在待接取: %+v", got)
	}
}

// 重新派出（改派、拒绝后再派）后，这一轮又有一次提醒；还没到新的阈值时不会提前提醒。
func TestClaimStaleOffersReOfferRemindsAgain(t *testing.T) {
	st, ctx, _ := boardFixture(t)
	seedBoardWorkflows(t, st, "wf-3")
	mgr := managerAs("wf-1", WorkPolicyEnforce)

	it := mustCreate(t, st, WorkItemInput{WorkflowID: "wf-1", Title: "会被改派", Assignee: "wf-2"}, mgr)
	// 第一轮发生在「过去」：这样第一次提醒的时刻早于下面真实发生的改派，与线上的先后一致。
	base := time.Now().Add(-10 * time.Hour).Truncate(time.Second)
	setAssignedAt(t, st, it.ID, base)
	if got := claim(t, st, ctx, base.Add(staleTwoHours), 0); len(got) != 1 || got[0].AssigneeWorkflowID != "wf-2" {
		t.Fatalf("第一轮应提醒 wf-2: %+v", got)
	}

	// 在第一次提醒之后改派给 wf-3：assigned_at 变新，这一轮没提醒过。
	reassigned := mustUpdate(t, st, it.ID, WorkItemPatch{Assignee: sp("wf-3")}, mgr).Item
	if reassigned.AssignedAt == nil || !reassigned.AssignedAt.After(base) {
		t.Fatalf("改派应刷新派出时间: %v vs %v", reassigned.AssignedAt, base)
	}
	if got := claim(t, st, ctx, reassigned.AssignedAt.Add(staleTwoHours-time.Second), 0); len(got) != 0 {
		t.Fatalf("新一轮还没到点，不该提醒: %+v", got)
	}
	got := claim(t, st, ctx, reassigned.AssignedAt.Add(staleTwoHours), 0)
	if len(got) != 1 || got[0].AssigneeWorkflowID != "wf-3" || got[0].Title != "会被改派" {
		t.Fatalf("改派后的新一轮应提醒 wf-3: %+v", got)
	}
	if again := claim(t, st, ctx, reassigned.AssignedAt.Add(50*time.Hour), 0); len(again) != 0 {
		t.Fatalf("新一轮也只提醒一次: %+v", again)
	}
}

// 一遍最多处理 maxGroups 组，没被选中的整组留着，下一遍再来（不会被「打了标记却没发出」吞掉）。
func TestClaimStaleOffersMaxGroups(t *testing.T) {
	st, ctx, _ := boardFixture(t)
	seedBoardWorkflows(t, st, "wf-3", "wf-4", "wf-5", "wf-6", "wf-7", "wf-8")
	mgr := managerAs("wf-1", WorkPolicyEnforce)
	var last time.Time
	for i := 2; i <= 8; i++ {
		it := mustCreate(t, st, WorkItemInput{WorkflowID: "wf-1", Title: fmt.Sprintf("给 wf-%d", i), Assignee: fmt.Sprintf("wf-%d", i)}, mgr)
		last = *it.AssignedAt
	}
	now := last.Add(staleTwoHours)

	// max <= 0 用默认上限（5），不是「不限」。
	first := claim(t, st, ctx, now, 0)
	if len(first) != BoardStaleMaxGroupsPerScan || BoardStaleMaxGroupsPerScan != 5 {
		t.Fatalf("第一遍应只处理默认的 5 组: %d", len(first))
	}
	second := claim(t, st, ctx, now, 5)
	if len(second) != 2 {
		t.Fatalf("第二遍应补上剩下的 2 组: %d", len(second))
	}
	seen := map[string]bool{}
	for _, g := range append(first, second...) {
		if seen[g.AssigneeWorkflowID] {
			t.Fatalf("同一个承接者不该被提醒两次: %s", g.AssigneeWorkflowID)
		}
		seen[g.AssigneeWorkflowID] = true
	}
	if len(seen) != 7 {
		t.Fatalf("七个承接者都应被提醒到: %v", seen)
	}
	if third := claim(t, st, ctx, now, 5); len(third) != 0 {
		t.Fatalf("没有剩余了: %+v", third)
	}
}

// 打标记不是任务的变化：不动 updated_at、不进时间线、不让任务清单的缓存版本前进。
func TestClaimStaleOffersLeavesTaskUntouched(t *testing.T) {
	st, ctx, _ := boardFixture(t)
	mgr := managerAs("wf-1", WorkPolicyEnforce)
	it := mustCreate(t, st, WorkItemInput{WorkflowID: "wf-1", Title: "安静", Assignee: "wf-2"}, mgr)

	before := reload(t, st, it.ID)
	var updatesBefore int64
	st.db.Model(&model.WorkItemUpdate{}).Where("work_item_id = ?", it.ID).Count(&updatesBefore)
	revBefore := st.workRev.Load()

	if got := claim(t, st, ctx, it.AssignedAt.Add(staleTwoHours), 0); len(got) != 1 {
		t.Fatalf("应提醒一次: %+v", got)
	}

	after := reload(t, st, it.ID)
	if !after.UpdatedAt.Equal(before.UpdatedAt) {
		t.Fatalf("updated_at 不该变: %v → %v", before.UpdatedAt, after.UpdatedAt)
	}
	var updatesAfter int64
	st.db.Model(&model.WorkItemUpdate{}).Where("work_item_id = ?", it.ID).Count(&updatesAfter)
	if updatesAfter != updatesBefore {
		t.Fatalf("时间线不该多出记录: %d → %d", updatesBefore, updatesAfter)
	}
	if st.workRev.Load() != revBefore {
		t.Fatalf("任务清单的缓存版本不该前进: %d → %d", revBefore, st.workRev.Load())
	}
}

// 升级：旧库没有 offer_reminded_at 这一列，重新打开时由 AutoMigrate 补上（可空，不动数据）；
// 升级前就存在的久无人接指派因此还没提醒过，升级后第一遍扫描会提醒一次（这是设计，写在 D-386 里）。
func TestMigrationAddsOfferRemindedAt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")
	st, err := New(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		"ALTER TABLE work_items DROP COLUMN offer_reminded_at",
		"INSERT INTO work_items (workflow_id, board_key, seq, title, kind, state, assignee_workflow_id, assigned_by_workflow_id, claim_state, assigned_at, created_at, updated_at) " +
			"VALUES ('wf-2', 'w:wf-2', 1, '老指派', 'feature', 'todo', 'wf-2', 'wf-1', 'offered', datetime('now', '-3 days'), datetime('now', '-3 days'), datetime('now', '-3 days'))",
	} {
		if err := st.db.Exec(q).Error; err != nil {
			t.Fatalf("还原旧表结构失败（%s）: %v", q, err)
		}
	}
	sqlDB, _ := st.db.DB()
	_ = sqlDB.Close()

	st2, err := New(path) // 重新打开 = 升级
	if err != nil {
		t.Fatalf("升级失败: %v", err)
	}
	if !st2.db.Migrator().HasColumn(&model.WorkItem{}, "offer_reminded_at") {
		t.Fatal("升级后应补上 offer_reminded_at 列")
	}
	ctx := context.Background()
	got := claim(t, st2, ctx, time.Now(), 0)
	if len(got) != 1 || got[0].AssigneeWorkflowID != "wf-2" || got[0].Title != "老指派" {
		t.Fatalf("升级前就存在的久无人接指派，升级后第一遍应提醒一次: %+v", got)
	}
	if again := claim(t, st2, ctx, time.Now(), 0); len(again) != 0 {
		t.Fatalf("之后不再提醒: %+v", again)
	}
	// 「重启」：标记在库里，重新打开后仍然记得，不会重复提醒。
	sqlDB2, _ := st2.db.DB()
	_ = sqlDB2.Close()
	st3, err := New(path)
	if err != nil {
		t.Fatal(err)
	}
	if again := claim(t, st3, ctx, time.Now().Add(100*time.Hour), 0); len(again) != 0 {
		t.Fatalf("重启后不该重复提醒: %+v", again)
	}
}

// 没有任何指派时什么都不返回、不报错；没有派出时间的（异常数据）无从算起，跳过。
func TestClaimStaleOffersEmptyAndMissingTime(t *testing.T) {
	st, ctx, _ := boardFixture(t)
	if got := claim(t, st, ctx, time.Now().Add(time.Hour), 0); len(got) != 0 {
		t.Fatalf("空板不该有提醒: %+v", got)
	}
	mgr := managerAs("wf-1", WorkPolicyEnforce)
	it := mustCreate(t, st, WorkItemInput{WorkflowID: "wf-1", Title: "没有派出时间", Assignee: "wf-2"}, mgr)
	if err := st.db.Model(&model.WorkItem{}).Where("id = ?", it.ID).UpdateColumn("assigned_at", nil).Error; err != nil {
		t.Fatal(err)
	}
	if got := claim(t, st, ctx, time.Now().Add(1000*time.Hour), 0); len(got) != 0 {
		t.Fatalf("没有派出时间的不提醒: %+v", got)
	}
}
