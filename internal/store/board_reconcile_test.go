package store

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/teacat99/RelayMesh/v2/internal/model"
)

// 启动时给历史任务补板的测试（#175 B1a）：迁移永不改号，冲突回退独立板，幂等，旧库可升级。

// itemBoards 返回工作流所有任务所在的板（去重、排序）。
func itemBoards(t *testing.T, st *Store, wf string) string {
	t.Helper()
	var keys []string
	if err := st.db.Model(&model.WorkItem{}).Where("workflow_id = ?", wf).Distinct().Order("board_key").Pluck("board_key", &keys).Error; err != nil {
		t.Fatal(err)
	}
	return strings.Join(keys, ",")
}

func countUnplaced(t *testing.T, st *Store) int64 {
	t.Helper()
	var n, m int64
	st.db.Model(&model.WorkItem{}).Where("board_key = ''").Count(&n)
	st.db.Model(&model.WorkModule{}).Where("board_key = ''").Count(&m)
	return n + m
}

func boardCounter(t *testing.T, st *Store, key string) int {
	t.Helper()
	var c model.WorkBoardCounter
	if err := st.db.Where("board_key = ?", key).First(&c).Error; err != nil {
		t.Fatalf("板 %s 没有计数记录: %v", key, err)
	}
	return c.LastSeq
}

// 项目里的工作流归项目板，目录外的归独立板；任务序号一个都不改；板计数取最大序号与旧计数的较大者；第二次什么都不做。
func TestReconcileAssignsBoardsAndCounters(t *testing.T) {
	st, ctx, a := boardFixture(t)
	legacyTasks(t, st, "wf-1", 0, 1, 2, 3)
	legacyTasks(t, st, "wf-2", 0, 4, 5)
	legacyTasks(t, st, "wf-solo", 0, 1, 2)
	legacyTasks(t, st, "wf-nodir", 0, 1)
	legacyModule(t, st, "wf-1", 0, "后端")
	// wf-3 没有任务，但旧计数是 9（任务删光了），它在项目目录里：项目板不受它影响（项目板计数只取板内的）。
	seedWorkflowDir(t, st, "wf-3", "devhost", "/home/user/a", time.Now())
	if err := st.db.Create(&model.WorkItemCounter{WorkflowID: "wf-3", LastSeq: 9}).Error; err != nil {
		t.Fatal(err)
	}
	// wf-4 没有任务，旧计数是 6，在项目外：它的独立板要带着这个计数，否则新任务会复用已发过的号。
	seedWorkflowDir(t, st, "wf-4", "devhost", "/tmp/other", time.Now())
	if err := st.db.Create(&model.WorkItemCounter{WorkflowID: "wf-4", LastSeq: 6}).Error; err != nil {
		t.Fatal(err)
	}
	pkey := ProjectBoardKey(a.ID)

	plan, err := st.reconcileBoards(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Unplaced != 8 || plan.UnplacedModules != 1 || len(plan.Assignments) != 4 {
		t.Fatalf("计划规模不对: %+v", plan)
	}
	for _, as := range plan.Assignments {
		if as.Fallback {
			t.Errorf("不冲突的工作流不应回退: %+v", as)
		}
	}
	for wf, want := range map[string]string{"wf-1": pkey, "wf-2": pkey, "wf-solo": "w:wf-solo", "wf-nodir": "w:wf-nodir"} {
		if got := itemBoards(t, st, wf); got != want {
			t.Errorf("%s 的任务应在 %s，实际 %s", wf, want, got)
		}
	}
	var modBoard string
	st.db.Model(&model.WorkModule{}).Select("board_key").Scan(&modBoard)
	if modBoard != pkey {
		t.Errorf("规划节点应跟着 wf-1 归项目板，实际 %q", modBoard)
	}
	if n := countUnplaced(t, st); n != 0 {
		t.Fatalf("归板后不应再有未归板的行: %d", n)
	}
	// 一个号都没改。
	var seqs []int
	st.db.Model(&model.WorkItem{}).Where("workflow_id = ?", "wf-2").Order("seq").Pluck("seq", &seqs)
	if len(seqs) != 2 || seqs[0] != 4 || seqs[1] != 5 {
		t.Fatalf("迁移不应改号: %v", seqs)
	}
	if got := boardCounter(t, st, pkey); got != 5 {
		t.Errorf("项目板计数应是 5（板内最大序号），实际 %d", got)
	}
	if got := boardCounter(t, st, "w:wf-solo"); got != 2 {
		t.Errorf("wf-solo 板计数应是 2，实际 %d", got)
	}
	if got := boardCounter(t, st, "w:wf-4"); got != 6 {
		t.Errorf("只剩旧计数的独立板要带着计数 6，实际 %d", got)
	}

	// 幂等：第二次不改任何东西。
	again, err := st.reconcileBoards(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if again.Changed() {
		t.Fatalf("第二次不应再有动作: %+v", again)
	}
	full, err := PlanBoardReconcile(ctx, st.db)
	if err != nil {
		t.Fatal(err)
	}
	if full.Changed() || full.Unplaced != 0 || full.Placed != 8 {
		t.Fatalf("归板后的完整预演应无动作: %+v", full)
	}

	// 归板后新任务按板发号：项目板接着 #6、#7，独立板接着自己的计数。
	if it := boardTask(t, st, "wf-1", "新任务"); it.Seq != 6 || it.BoardKey != pkey {
		t.Errorf("wf-1 新任务应是项目板 #6: seq=%d board=%s", it.Seq, it.BoardKey)
	}
	if it := boardTask(t, st, "wf-2", "新任务"); it.Seq != 7 {
		t.Errorf("wf-2 新任务应是项目板 #7: seq=%d", it.Seq)
	}
	if it := boardTask(t, st, "wf-solo", "新任务"); it.Seq != 3 || it.BoardKey != "w:wf-solo" {
		t.Errorf("wf-solo 新任务应是 #3: seq=%d board=%s", it.Seq, it.BoardKey)
	}
	if it := boardTask(t, st, "wf-4", "新任务"); it.Seq != 7 {
		t.Errorf("wf-4 的号不能复用旧计数发过的号，应是 #7: seq=%d", it.Seq)
	}
}

// 归项目板会和板内已有序号冲突：后来者留在自己的独立板，序号保持原样，给警告；更早的工作流不受影响。
func TestReconcileSeqConflictFallsBackToIndependentBoard(t *testing.T) {
	st, ctx, a := boardFixture(t)
	legacyTasks(t, st, "wf-1", 0, 1, 2, 3)
	legacyTasks(t, st, "wf-2", 0, 2, 3, 4)
	now := time.Now()
	setTasksCreatedAt(t, st, "wf-1", now.Add(-2*time.Hour))
	setTasksCreatedAt(t, st, "wf-2", now.Add(-time.Hour))

	plan, err := st.reconcileBoards(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got := itemBoards(t, st, "wf-1"); got != ProjectBoardKey(a.ID) {
		t.Fatalf("更早的 wf-1 应占项目板: %s", got)
	}
	if got := itemBoards(t, st, "wf-2"); got != "w:wf-2" {
		t.Fatalf("冲突的 wf-2 应回退到独立板: %s", got)
	}
	var seqs []int
	st.db.Model(&model.WorkItem{}).Where("workflow_id = ?", "wf-2").Order("seq").Pluck("seq", &seqs)
	if len(seqs) != 3 || seqs[0] != 2 || seqs[2] != 4 {
		t.Fatalf("回退不改号: %v", seqs)
	}
	var fb *BoardAssignment
	for i := range plan.Assignments {
		if plan.Assignments[i].Fallback {
			fb = &plan.Assignments[i]
		}
	}
	if fb == nil || fb.WorkflowID != "wf-2" || fb.WantedKey != ProjectBoardKey(a.ID) || len(fb.Conflicts) != 2 {
		t.Fatalf("应记录 wf-2 回退及 2 处冲突: %+v", fb)
	}
	if len(plan.Warnings) != 1 || !strings.Contains(plan.Warnings[0], "wf-2") {
		t.Errorf("应给回退警告: %v", plan.Warnings)
	}
	if got := boardCounter(t, st, "w:wf-2"); got != 4 {
		t.Errorf("独立板计数应是 4: %d", got)
	}
	// 之后 wf-2 的新任务留在独立板，接着 #5。
	if it := boardTask(t, st, "wf-2", "后续"); it.BoardKey != "w:wf-2" || it.Seq != 5 {
		t.Errorf("回退后的新任务应在独立板 #5: %+v", it)
	}
}

// 序号不冲突但规划节点路径重复：迁移不合并节点，后来者同样回退独立板（节点与任务一起走）。
func TestReconcileModulePathConflictFallsBack(t *testing.T) {
	st, ctx, a := boardFixture(t)
	m1 := legacyModule(t, st, "wf-1", 0, "后端")
	m2 := legacyModule(t, st, "wf-2", 0, "后端") // 同路径（旧版本里两个工作流各有一棵树，重名不冲突）
	legacyTasks(t, st, "wf-1", m1.ID, 1, 2)
	legacyTasks(t, st, "wf-2", m2.ID, 3, 4)
	now := time.Now()
	setTasksCreatedAt(t, st, "wf-1", now.Add(-2*time.Hour))
	setTasksCreatedAt(t, st, "wf-2", now.Add(-time.Hour))

	plan, err := st.reconcileBoards(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if itemBoards(t, st, "wf-1") != ProjectBoardKey(a.ID) || itemBoards(t, st, "wf-2") != "w:wf-2" {
		t.Fatalf("路径冲突应让后来者回退: wf-1=%s wf-2=%s", itemBoards(t, st, "wf-1"), itemBoards(t, st, "wf-2"))
	}
	var board string
	st.db.Model(&model.WorkModule{}).Where("id = ?", m2.ID).Select("board_key").Scan(&board)
	if board != "w:wf-2" {
		t.Fatalf("规划节点应跟着工作流回退: %s", board)
	}
	if len(plan.Warnings) != 1 || !strings.Contains(plan.Warnings[0], "wf-2") {
		t.Errorf("应给回退警告: %v", plan.Warnings)
	}
	// wf-2 看到的是自己的规划树（只有自己的「后端」），wf-1 看到项目板的。
	l2, err := st.ListWorkModules(ctx, "wf-2")
	if err != nil || len(l2.Modules) != 1 || l2.Modules[0].ID != m2.ID {
		t.Fatalf("wf-2 应只看到自己的节点: %+v err=%v", l2, err)
	}
	l1, err := st.ListWorkModules(ctx, "wf-1")
	if err != nil || len(l1.Modules) != 1 || l1.Modules[0].ID != m1.ID {
		t.Fatalf("wf-1 应只看到项目板的节点: %+v err=%v", l1, err)
	}
}

// 已有任务在某张板的工作流，目录之后挪走了，旧版本回滚期间写入的游离行仍归到原来的板（粘性），不跟着目录走。
func TestReconcileStrayRowsFollowStickyBoard(t *testing.T) {
	st, ctx, a := boardFixture(t)
	first := boardTask(t, st, "wf-1", "在项目里建的")
	if first.BoardKey != ProjectBoardKey(a.ID) {
		t.Fatalf("前提：任务应在项目板: %s", first.BoardKey)
	}
	// 之后 wf-1 的最近会话挪到项目外；回滚到旧版本期间它又写了一条没有 board_key 的任务。
	seedWorkflowDir(t, st, "wf-1", "devhost", "/tmp/other", time.Now().Add(time.Minute))
	legacyTasks(t, st, "wf-1", 0, 2)
	if got, err := st.BoardKeyOf(ctx, "wf-1"); err != nil || got != ProjectBoardKey(a.ID) {
		t.Fatalf("目录挪走后仍应粘在项目板: %q %v", got, err)
	}

	plan, err := st.reconcileBoards(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got := itemBoards(t, st, "wf-1"); got != ProjectBoardKey(a.ID) {
		t.Fatalf("游离行应归原板: %s", got)
	}
	if len(plan.Assignments) != 1 || !strings.Contains(plan.Assignments[0].Reason, "粘性") || plan.Assignments[0].Fallback {
		t.Fatalf("应按粘性归板: %+v", plan.Assignments)
	}
	if got := boardCounter(t, st, ProjectBoardKey(a.ID)); got != 2 {
		t.Errorf("板计数应跟上游离行的序号: %d", got)
	}
}

// 真正的升级路径：旧库没有 board_key 列和两张新表，打开时 AutoMigrate 补上，启动迁移把存量归板；只读预演在旧库上同样可用。
func TestReconcileUpgradesOldSchemaOnOpen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "relaymesh.db")
	st, err := New(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	a, err := st.CreateProject(ctx, ProjectInput{Name: "A", Slug: "a"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.AddProjectDir(ctx, a.ID, "devhost", "/home/user/a", ""); err != nil {
		t.Fatal(err)
	}
	seedWorkflowDir(t, st, "wf-1", "devhost", "/home/user/a", time.Now())
	seedWorkflowDir(t, st, "wf-solo", "devhost", "/tmp/other", time.Now())
	m := legacyModule(t, st, "wf-1", 0, "后端")
	legacyTasks(t, st, "wf-1", m.ID, 1, 2)
	legacyTasks(t, st, "wf-solo", 0, 1)

	// 退回旧表结构：删掉带 board_key 的索引、列和两张新表。
	var idx []string
	if err := st.db.Raw("SELECT name FROM sqlite_master WHERE type = 'index' AND sql LIKE '%board_key%'").Scan(&idx).Error; err != nil {
		t.Fatal(err)
	}
	for _, name := range idx {
		if err := st.db.Exec("DROP INDEX IF EXISTS " + name).Error; err != nil {
			t.Fatal(err)
		}
	}
	for _, q := range []string{
		"ALTER TABLE work_items DROP COLUMN board_key",
		"ALTER TABLE work_modules DROP COLUMN board_key",
		"DROP TABLE work_board_counters",
		"DROP TABLE work_item_aliases",
	} {
		if err := st.db.Exec(q).Error; err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	// 只读预演：旧库上能跑，不写任何东西。
	ro, err := gorm.Open(sqlite.Open("file:"+path+"?mode=ro"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	plan, err := PlanBoardReconcile(ctx, ro)
	if err != nil {
		t.Fatalf("旧库上的预演失败: %v", err)
	}
	if plan.Unplaced != 3 || plan.UnplacedModules != 1 || len(plan.Assignments) != 2 {
		t.Fatalf("旧库预演规模不对: %+v", plan)
	}
	if sqlDB, err := ro.DB(); err == nil {
		_ = sqlDB.Close()
	}

	// 重新打开：补列、补表、归板。
	st2, err := New(path)
	if err != nil {
		t.Fatalf("旧库升级失败: %v", err)
	}
	defer st2.Close()
	if n := countUnplaced(t, st2); n != 0 {
		t.Fatalf("升级后不应有未归板的行: %d", n)
	}
	if got := itemBoards(t, st2, "wf-1"); got != ProjectBoardKey(a.ID) {
		t.Errorf("wf-1 应归项目板: %s", got)
	}
	if got := itemBoards(t, st2, "wf-solo"); got != "w:wf-solo" {
		t.Errorf("wf-solo 应归独立板: %s", got)
	}
	if got := boardCounter(t, st2, ProjectBoardKey(a.ID)); got != 2 {
		t.Errorf("项目板计数: %d", got)
	}
	it, err := st2.GetWorkItemBySeq(ctx, "wf-1", 2)
	if err != nil || it.ModulePath != "后端" {
		t.Fatalf("升级后按序号读任务应带模块路径: %+v %v", it, err)
	}
}

// 归板只补 board_key：序号、标题、创建 / 更新时间一个都不变。更新时间一变，任务页「最近更新」的排序会在迁移后整体跳变，
// 所以用 UpdateColumn（不触发自动更新时间）补列；这条用例把它钉住（迁移演练 -mode rehearse 在真实数据上做同样的核对）。
func TestReconcileKeepsSeqTitlesAndTimestamps(t *testing.T) {
	st, ctx, _ := boardFixture(t)
	ids1 := legacyTasks(t, st, "wf-1", 0, 1, 2)
	ids2 := legacyTasks(t, st, "wf-solo", 0, 1)
	m := legacyModule(t, st, "wf-1", 0, "后端")

	created := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	updated := time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)
	for _, id := range append(ids1, ids2...) {
		if err := st.db.Model(&model.WorkItem{}).Where("id = ?", id).
			UpdateColumns(map[string]any{"created_at": created, "updated_at": updated}).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := st.db.Model(&model.WorkModule{}).Where("id = ?", m.ID).
		UpdateColumns(map[string]any{"created_at": created, "updated_at": updated}).Error; err != nil {
		t.Fatal(err)
	}
	var before []model.WorkItem
	if err := st.db.Order("id").Find(&before).Error; err != nil {
		t.Fatal(err)
	}

	if _, err := st.reconcileBoards(ctx); err != nil {
		t.Fatal(err)
	}
	if n := countUnplaced(t, st); n != 0 {
		t.Fatalf("应全部归板: %d", n)
	}

	var after []model.WorkItem
	if err := st.db.Order("id").Find(&after).Error; err != nil {
		t.Fatal(err)
	}
	if len(after) != len(before) {
		t.Fatalf("任务数变了: %d → %d", len(before), len(after))
	}
	for i := range before {
		b, a := before[i], after[i]
		if a.WorkflowID != b.WorkflowID || a.Seq != b.Seq || a.Title != b.Title || a.State != b.State {
			t.Errorf("任务 %d 的序号 / 标题 / 状态被改了: %+v → %+v", b.ID, b, a)
		}
		if !a.CreatedAt.Equal(b.CreatedAt) || !a.UpdatedAt.Equal(b.UpdatedAt) {
			t.Errorf("任务 %d 的时间被改了：创建 %v → %v，更新 %v → %v", b.ID, b.CreatedAt, a.CreatedAt, b.UpdatedAt, a.UpdatedAt)
		}
		if a.BoardKey == "" {
			t.Errorf("任务 %d 没有归板", b.ID)
		}
	}
	var gotMod model.WorkModule
	if err := st.db.First(&gotMod, m.ID).Error; err != nil {
		t.Fatal(err)
	}
	if !gotMod.UpdatedAt.Equal(updated) || !gotMod.CreatedAt.Equal(created) || gotMod.BoardKey == "" {
		t.Errorf("规划节点的时间被改了或没归板: %+v", gotMod)
	}
}
