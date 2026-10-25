package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/teacat99/RelayMesh/v2/internal/asyncmode"
	"github.com/teacat99/RelayMesh/v2/internal/model"
)

// 严格收尾门（#232 D2，D-375）：谁的任务挡谁的收尾、门开 / 门关 / 放行三种口径下窗口怎么走、暂缓缘由的软提醒。

// putGateTask 直接写一行任务：门的判定只看 workflow_id / state / 指派三元组，不必走完整的看板与指派流程。
func putGateTask(t *testing.T, st *Store, wf string, seq int, state, assignee, by, claim string) {
	t.Helper()
	it := &model.WorkItem{
		WorkflowID: wf, Seq: seq, Title: fmt.Sprintf("任务%d", seq), State: state, CreatedBy: model.WorkActorAgent,
		AssigneeWorkflowID: assignee, AssignedByWorkflowID: by, ClaimState: claim,
	}
	if err := st.db.Create(it).Error; err != nil {
		t.Fatalf("写任务失败: %v", err)
	}
}

func gateSeqs(st *Store, wf string) []int {
	tasks, _ := st.gateTasks(context.Background(), wf, asyncmode.GateViewMax)
	out := make([]int, 0, len(tasks))
	for _, g := range tasks {
		out = append(out, g.Seq)
	}
	return out
}

func sameInts(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// 归属矩阵：同一张板上 A、B 两个会话，逐种状态 × 指派关系检查「谁被挡着」。
// 关键不变量：任何一个任务在任一时刻只挡一方（或不挡任何 Agent 会话），所以不会出现两个会话互相等对方而死锁。
func TestGateTasksOwnershipMatrix(t *testing.T) {
	cases := []struct {
		name                string
		creator, state      string
		assignee, by, claim string
		blocksA, blocksB    bool
	}{
		{"自己的待办", "A", "todo", "", "", "", true, false},
		{"自己的进行中", "A", "doing", "", "", "", true, false},
		{"自己的验证中", "A", "review", "", "", "", true, false},
		{"已完成不挡", "A", "done", "", "", "", false, false},
		{"已搁置不挡", "A", "shelved", "", "", "", false, false},
		{"别人的待办不挡我", "B", "todo", "", "", "", false, true},

		{"A 派给 B 待接取：B 要接", "A", "todo", "B", "A", model.WorkClaimOffered, false, true},
		{"A 派给 B 已接取进行中：B 在做", "A", "doing", "B", "A", model.WorkClaimAccepted, false, true},
		{"A 派给 B 已交付待验收：A 要验收", "A", "review", "B", "A", model.WorkClaimAccepted, true, false},
		{"B 拒绝了 A 的指派：回到 A 手里等重派", "A", "todo", "B", "A", model.WorkClaimDeclined, true, false},

		{"人派给 B 待接取", "A", "todo", "B", "", model.WorkClaimOffered, false, true},
		{"人派给 B 已交付：没有 Agent 验收，不挡任何会话", "A", "review", "B", "", model.WorkClaimAccepted, false, false},
		{"人派给 B 被 B 拒绝：等人重派，不挡任何会话", "A", "todo", "B", "", model.WorkClaimDeclined, false, false},

		{"自己派给自己的验证中：自己验", "A", "review", "A", "A", model.WorkClaimAccepted, true, false},
		{"自己派给自己的进行中", "A", "doing", "A", "A", model.WorkClaimAccepted, true, false},
		{"B 创建后派给 A（B 派）待接取：A 要接", "B", "todo", "A", "B", model.WorkClaimOffered, true, false},
		{"B 创建后派给 A 已交付：B 要验收", "B", "review", "A", "B", model.WorkClaimAccepted, false, true},
	}
	st := setupTestStore(t)
	for i, tc := range cases {
		putGateTask(t, st, tc.creator, 100+i, tc.state, tc.assignee, tc.by, tc.claim)
	}
	for i, tc := range cases {
		seq := 100 + i
		gotA := false
		for _, s := range gateSeqs(st, "A") {
			gotA = gotA || s == seq
		}
		gotB := false
		for _, s := range gateSeqs(st, "B") {
			gotB = gotB || s == seq
		}
		if gotA != tc.blocksA || gotB != tc.blocksB {
			t.Errorf("%s：挡 A=%v 挡 B=%v，期望 A=%v B=%v", tc.name, gotA, gotB, tc.blocksA, tc.blocksB)
		}
		if gotA && gotB {
			t.Errorf("%s：同一个任务不能同时挡住两个会话（会互相等待）", tc.name)
		}
	}
}

// 门口径不按时间截断：十天没动的待办 / 验证中仍然挡着；老口径（3 小时）不挡。
func TestGateTasksNotTimeBounded(t *testing.T) {
	st := setupTestStore(t)
	putGateTask(t, st, "A", 1, "todo", "", "", "")
	putGateTask(t, st, "A", 2, "review", "", "", "")
	old := time.Now().Add(-10 * 24 * time.Hour)
	if err := st.db.Model(&model.WorkItem{}).Where("workflow_id = ?", "A").UpdateColumn("updated_at", old).Error; err != nil {
		t.Fatal(err)
	}
	if got := gateSeqs(st, "A"); !sameInts(got, []int{1, 2}) {
		t.Fatalf("十天没动的待办 / 验证中也该挡着: %v", got)
	}
	if n := st.activeTaskCount(context.Background(), "A"); n != 0 {
		t.Fatalf("老口径只数 3 小时内有更新的进行中 / 验证中，这里应为 0，实际 %d", n)
	}
}

func TestGateTasksOrderLimitAndTotal(t *testing.T) {
	st := setupTestStore(t)
	for _, seq := range []int{9, 3, 7, 1, 5} {
		putGateTask(t, st, "A", seq, "todo", "", "", "")
	}
	tasks, total := st.gateTasks(context.Background(), "A", 3)
	if total != 5 || len(tasks) != 3 || tasks[0].Seq != 1 || tasks[1].Seq != 3 || tasks[2].Seq != 5 {
		t.Fatalf("应按编号升序取前 3 个、总数 5：%+v total=%d", tasks, total)
	}
	if tasks[0].Title != "任务1" || tasks[0].State != "todo" {
		t.Fatalf("应带标题与状态：%+v", tasks[0])
	}
	if got, n := st.gateTasks(context.Background(), "nobody", 3); got != nil || n != 0 {
		t.Fatalf("没有任务应返回空: %v %d", got, n)
	}
}

// asyncOn 给 wf 开启异步（先记 hook 再开关），返回此刻。
func asyncOn(t *testing.T, st *Store, wf string) time.Time {
	t.Helper()
	now := time.Now()
	st.RecordHookSeen(wf, now)
	on := true
	if _, err := st.UpdateWorkflowMode(context.Background(), wf, ModePatch{Enabled: &on}, now); err != nil {
		t.Fatalf("开启异步失败: %v", err)
	}
	return now
}

func setStrict(t *testing.T, st *Store, wf string, on bool) {
	t.Helper()
	if _, err := st.UpdateWorkflowMode(context.Background(), wf, ModePatch{StrictWrapup: &on}, time.Now()); err != nil {
		t.Fatalf("改收尾门开关失败: %v", err)
	}
}

// 默认就是严格口径：一个待办（老口径根本不数）就让窗口暂停，清零后接着走；窗口状态里带着挡着的任务。
func TestReconcileStrictGateHoldsWindowForTodo(t *testing.T) {
	st := setupTestStore(t)
	ctx := context.Background()
	const wf = "wf-gate"
	now := asyncOn(t, st, wf)
	putGateTask(t, st, wf, 1, "todo", "", "", "")
	putGateTask(t, st, wf, 2, "shelved", "", "", "")

	w, err := st.ExtendReplyWindow(ctx, wf, 5*time.Minute, now)
	if err != nil {
		t.Fatal(err)
	}
	if w.Until != nil || w.BankSec != 300 || w.ActiveTasks != 1 || !w.Strict || w.GateTotal != 1 || len(w.Gate) != 1 || w.Gate[0].Seq != 1 {
		t.Fatalf("有一个待办时累加的时间应攒进暂存、窗口不走：%+v", w)
	}
	cur, changed, err := st.ReconcileReplyWindow(ctx, wf, now.Add(time.Minute))
	if err != nil || changed || cur.Until != nil || cur.BankSec != 300 || !cur.Paused() {
		t.Fatalf("对齐后仍应暂停：%+v changed=%v err=%v", cur, changed, err)
	}

	// 待办做完 → 窗口从攒着的 300 秒接着走
	if err := st.db.Model(&model.WorkItem{}).Where("workflow_id = ? AND seq = ?", wf, 1).Update("state", "done").Error; err != nil {
		t.Fatal(err)
	}
	later := now.Add(2 * time.Minute)
	cur, changed, err = st.ReconcileReplyWindow(ctx, wf, later)
	if err != nil || !changed || cur.Until == nil || cur.BankSec != 0 || !cur.Until.Equal(later.Add(300*time.Second)) {
		t.Fatalf("任务清零后窗口应接着走 300 秒：%+v changed=%v err=%v", cur, changed, err)
	}
}

// 门关着：回到老口径——待办与超过 3 小时没动的验证中都不挡；进行中且 3 小时内有更新的才挡。
func TestReconcileStrictOffUsesLegacyCount(t *testing.T) {
	st := setupTestStore(t)
	ctx := context.Background()
	const wf = "wf-gate-off"
	now := asyncOn(t, st, wf)
	setStrict(t, st, wf, false)
	putGateTask(t, st, wf, 1, "todo", "", "", "")
	putGateTask(t, st, wf, 2, "review", "", "", "")
	if err := st.db.Model(&model.WorkItem{}).Where("workflow_id = ? AND seq = ?", wf, 2).UpdateColumn("updated_at", now.Add(-5*time.Hour)).Error; err != nil {
		t.Fatal(err)
	}
	w, err := st.ExtendReplyWindow(ctx, wf, 5*time.Minute, now)
	if err != nil || w.Until == nil || w.ActiveTasks != 0 || w.Strict || w.GateTotal != 0 || len(w.Gate) != 0 {
		t.Fatalf("门关着时待办与久未更新的验证中都不该挡窗口：%+v %v", w, err)
	}
	putGateTask(t, st, wf, 3, "doing", "", "", "")
	cur, changed, err := st.ReconcileReplyWindow(ctx, wf, now.Add(time.Second))
	if err != nil || !changed || cur.Until != nil || cur.ActiveTasks != 1 || !cur.Paused() {
		t.Fatalf("新鲜的进行中任务老口径下仍应暂停窗口：%+v changed=%v err=%v", cur, changed, err)
	}
	// 重新打开门：同样的任务现在按严格口径数（待办 + 进行中 + 久未更新的验证中 = 3）
	setStrict(t, st, wf, true)
	cur, _, err = st.ReconcileReplyWindow(ctx, wf, now.Add(2*time.Second))
	if err != nil || cur.ActiveTasks != 3 || !cur.Strict || cur.GateTotal != 3 {
		t.Fatalf("重新开门后应按严格口径数 3 个：%+v %v", cur, err)
	}
}

// 本次放行：窗口立刻恢复倒计时、任务一个不动；用户回复即复位；窗口走完重新起算也复位；没有窗口 / 门已关时拒绝。
func TestReleaseGateLifecycle(t *testing.T) {
	st := setupTestStore(t)
	ctx := context.Background()
	const wf = "wf-gate-release"
	now := asyncOn(t, st, wf)

	if _, err := st.ReleaseGate(ctx, wf, now); err == nil || !strings.Contains(err.Error(), "没有回复窗口") {
		t.Fatalf("没有窗口时放行应被拒绝并说明原因: %v", err)
	}
	var ce *AppError
	if _, err := st.ReleaseGate(ctx, wf, now); !errors.As(err, &ce) || ce.Kind != ErrConflict {
		t.Fatalf("应是冲突错误: %T %v", err, err)
	}
	if _, err := st.ReleaseGate(ctx, "nope", now); err == nil {
		t.Fatal("没有模式行应报错")
	}

	putGateTask(t, st, wf, 1, "todo", "", "", "")
	putGateTask(t, st, wf, 2, "review", "", "", "")
	if _, err := st.ExtendReplyWindow(ctx, wf, 5*time.Minute, now); err != nil {
		t.Fatal(err)
	}
	w, err := st.ReleaseGate(ctx, wf, now.Add(time.Minute))
	if err != nil || w.Until == nil || w.BankSec != 0 || w.ActiveTasks != 0 || !w.Released || w.GateTotal != 2 {
		t.Fatalf("放行后窗口应立刻接着走、仍如实带着 2 个未完成任务：%+v %v", w, err)
	}
	if left := w.Until.Sub(now.Add(time.Minute)); left != 300*time.Second {
		t.Fatalf("攒着的 300 秒应整段接着走，实际 %v", left)
	}
	var open int64
	st.db.Model(&model.WorkItem{}).Where("workflow_id = ? AND state IN ?", wf, []string{"todo", "review"}).Count(&open)
	if open != 2 {
		t.Fatalf("放行不该动任务：%d", open)
	}
	// 放行期间智能体再发消息：窗口在原有基础上累加、仍然走，不会被再次暂停
	w, err = st.ExtendReplyWindow(ctx, wf, 5*time.Minute, now.Add(2*time.Minute))
	if err != nil || w.Until == nil || w.ActiveTasks != 0 || !w.Released {
		t.Fatalf("放行期间累加窗口不该被重新暂停：%+v %v", w, err)
	}
	g, err := st.WorkflowGate(ctx, wf)
	if err != nil || !g.Strict || !g.Released || g.Total != 2 || len(g.Tasks) != 2 {
		t.Fatalf("门状态应显示已放行且仍列出任务：%+v %v", g, err)
	}

	// 用户回复：窗口清零、放行复位
	if err := st.ClearReplyWindow(ctx, wf); err != nil {
		t.Fatal(err)
	}
	if g, _ = st.WorkflowGate(ctx, wf); g.Released {
		t.Fatal("用户回复后放行应复位")
	}
	w, _ = st.ExtendReplyWindow(ctx, wf, 5*time.Minute, now.Add(3*time.Minute))
	if w.Until != nil || w.BankSec != 300 || w.ActiveTasks != 2 {
		t.Fatalf("复位后新一轮窗口应重新被任务挡住：%+v", w)
	}

	// 放行后窗口自己走完（没有回复）：下一次重新起算时放行复位
	if _, err := st.ReleaseGate(ctx, wf, now.Add(4*time.Minute)); err != nil {
		t.Fatal(err)
	}
	expired := now.Add(30 * time.Minute)
	w, _, _ = st.ReconcileReplyWindow(ctx, wf, expired)
	if w.Until == nil || w.Until.After(expired) {
		t.Fatalf("窗口应已走完：%+v", w)
	}
	w, err = st.ExtendReplyWindow(ctx, wf, 5*time.Minute, expired)
	if err != nil || w.Released || w.Until != nil || w.ActiveTasks != 2 {
		t.Fatalf("窗口走完后重新起算应回到被任务挡住：%+v %v", w, err)
	}

	// 门关着时放行没有意义
	setStrict(t, st, wf, false)
	if _, err := st.ReleaseGate(ctx, wf, expired); err == nil || !strings.Contains(err.Error(), "已经关闭") {
		t.Fatalf("门关着时放行应被拒绝: %v", err)
	}
}

// 改开关会让上一轮的放行作废（口径变了，放行不该悄悄带过去）。
func TestStrictToggleClearsRelease(t *testing.T) {
	st := setupTestStore(t)
	ctx := context.Background()
	const wf = "wf-gate-toggle"
	now := asyncOn(t, st, wf)
	putGateTask(t, st, wf, 1, "todo", "", "", "")
	_, _ = st.ExtendReplyWindow(ctx, wf, 5*time.Minute, now)
	if _, err := st.ReleaseGate(ctx, wf, now); err != nil {
		t.Fatal(err)
	}
	setStrict(t, st, wf, false)
	setStrict(t, st, wf, true)
	g, err := st.WorkflowGate(ctx, wf)
	if err != nil || g.Released || !g.Strict {
		t.Fatalf("开关一变放行应作废：%+v %v", g, err)
	}
}

// 没有开过异步的会话没有模式行：门状态全为零值，不报错。
func TestWorkflowGateWithoutModeRow(t *testing.T) {
	st := setupTestStore(t)
	putGateTask(t, st, "wf-never", 1, "todo", "", "", "")
	g, err := st.WorkflowGate(context.Background(), "wf-never")
	if err != nil || g.Strict || g.Total != 0 || len(g.Tasks) != 0 {
		t.Fatalf("没有模式行不该有门：%+v %v", g, err)
	}
}

// 暂缓缘由的软提醒：只提醒最近 24 小时内置的、没写 Q<N> / 「等」/ 结论的暂缓；别人的暂缓不算。
func TestShelvedUnexplained(t *testing.T) {
	st := setupTestStore(t)
	ctx := context.Background()
	now := time.Now()
	mk := func(wf string, seq int, progress, conclusion string, refs []string, age time.Duration) {
		it := &model.WorkItem{WorkflowID: wf, Seq: seq, Title: fmt.Sprintf("任务%d", seq), State: "shelved", CreatedBy: model.WorkActorAgent,
			Progress: progress, Conclusion: conclusion, Refs: refs}
		if err := st.db.Create(it).Error; err != nil {
			t.Fatal(err)
		}
		if err := st.db.Model(&model.WorkItem{}).Where("id = ?", it.ID).UpdateColumn("updated_at", now.Add(-age)).Error; err != nil {
			t.Fatal(err)
		}
	}
	mk("A", 1, "", "", nil, time.Hour)                 // 什么都没写：提醒
	mk("A", 2, "等 Q83 的答复", "", nil, time.Hour)        // 写了卡号：不提醒
	mk("A", 3, "先放一放", "", []string{"Q91"}, time.Hour) // 卡号在引用里：不提醒
	mk("A", 4, "等采样器结束", "", nil, time.Hour)           // 写明了在等什么：不提醒
	mk("A", 5, "不做了", "", nil, time.Hour)              // 没写缘由：提醒
	mk("A", 6, "不做了", "已被 #7 取代", nil, time.Hour)      // 有结论（收尾性搁置）：不提醒
	mk("A", 7, "", "", nil, 30*time.Hour)              // 24 小时前置的：不翻旧账
	mk("B", 1, "", "", nil, time.Hour)                 // 别的会话的：不算我的
	if err := st.db.Create(&model.WorkItem{WorkflowID: "B", Seq: 2, Title: "B 派给 A 的暂缓", State: "shelved", CreatedBy: model.WorkActorAgent,
		AssigneeWorkflowID: "A", AssignedByWorkflowID: "B", ClaimState: model.WorkClaimAccepted}).Error; err != nil {
		t.Fatal(err)
	}
	tasks, total := st.ShelvedUnexplained(ctx, "A", now, 8)
	var seqs []int
	for _, g := range tasks {
		seqs = append(seqs, g.Seq)
	}
	// A 自己的 #1 #5，加上别人派给 A 的那条（它的 seq 是 B 板上的 2）
	if total != 3 || !sameInts(seqs, []int{1, 2, 5}) {
		t.Fatalf("应提醒 #1 #2（B 派来的）#5：%v total=%d", seqs, total)
	}
	if tasks, total := st.ShelvedUnexplained(ctx, "A", now, 1); total != 3 || len(tasks) != 1 {
		t.Fatalf("limit 只截断点名，不改总数: %v %d", tasks, total)
	}
	if _, total := st.ShelvedUnexplained(ctx, "", now, 8); total != 0 {
		t.Fatal("空工作流不该有提醒")
	}
}

func TestShelveExplained(t *testing.T) {
	cases := []struct {
		name string
		it   model.WorkItem
		want bool
	}{
		{"空", model.WorkItem{}, false},
		{"进展里有卡号", model.WorkItem{Progress: "等用户在 Q102 拍板"}, true},
		{"引用里有卡号", model.WorkItem{Refs: model.StringArray{"Q5"}}, true},
		{"写了等什么", model.WorkItem{Progress: "等 18:38 之后热更"}, true},
		{"只有结论", model.WorkItem{Conclusion: "改由 #9 承接"}, true},
		{"进展没有缘由", model.WorkItem{Progress: "先放放"}, false},
		{"小写 q 不是卡号", model.WorkItem{Progress: "q12"}, false},
	}
	for _, tc := range cases {
		if got := ShelveExplained(tc.it); got != tc.want {
			t.Errorf("%s: got %v want %v", tc.name, got, tc.want)
		}
	}
}

// 反向提醒：确认卡已答复、任务还在暂缓、答复之后没人动过它。
func TestShelvedAfterAnswer(t *testing.T) {
	st := setupTestStore(t)
	ctx := context.Background()
	now := time.Now()
	answeredAt := now.Add(-2 * time.Hour)

	card := func(wf string, seq, taskSeq int, status string, at time.Time) {
		c := &model.ConfirmItem{WorkflowID: wf, Seq: seq, Kind: model.ConfirmKindAsk, Title: fmt.Sprintf("卡%d", seq),
			OptionsJSON: "[]", Risk: model.ConfirmRiskL1, TaskSeq: taskSeq, Status: status, CreatedBy: model.ConfirmCreatedByAgent}
		if status == model.ConfirmStatusAnswered {
			a := at
			c.AnsweredAt = &a
		}
		if err := st.db.Create(c).Error; err != nil {
			t.Fatal(err)
		}
	}
	task := func(wf string, seq int, state, progress string, refs []string, updatedAgo time.Duration) {
		it := &model.WorkItem{WorkflowID: wf, Seq: seq, Title: fmt.Sprintf("任务%d", seq), State: state, CreatedBy: model.WorkActorAgent,
			Progress: progress, Refs: refs}
		if err := st.db.Create(it).Error; err != nil {
			t.Fatal(err)
		}
		if err := st.db.Model(&model.WorkItem{}).Where("id = ?", it.ID).UpdateColumn("updated_at", now.Add(-updatedAgo)).Error; err != nil {
			t.Fatal(err)
		}
	}

	card("A", 5, 0, model.ConfirmStatusAnswered, answeredAt)               // 已答复
	card("A", 6, 0, model.ConfirmStatusPending, time.Time{})               // 还没答复
	card("A", 7, 0, model.ConfirmStatusDismissed, time.Time{})             // 驳回：不是一次决定
	card("A", 8, 11, model.ConfirmStatusAnswered, answeredAt)              // 计划卡，task_seq 指向 #11
	card("A", 9, 0, model.ConfirmStatusAnswered, now.Add(-8*24*time.Hour)) // 8 天前：超出回看范围
	card("B", 5, 0, model.ConfirmStatusAnswered, answeredAt)               // 别的会话同号的卡不能串

	task("A", 1, "shelved", "等 Q5 的答复", nil, 3*time.Hour)        // 答复前就暂缓、之后没动过：提醒
	task("A", 2, "shelved", "等 Q5 的答复", nil, time.Hour)          // 答复之后已经更新过：当作已处理
	task("A", 3, "shelved", "等 Q6 的答复", nil, 3*time.Hour)        // 卡还没答复：不提醒
	task("A", 4, "shelved", "等 Q7", nil, 3*time.Hour)            // 卡被驳回：不提醒
	task("A", 10, "shelved", "先放着", []string{"Q5"}, 3*time.Hour) // 卡号在引用里：提醒
	task("A", 11, "shelved", "", nil, 3*time.Hour)               // 没写卡号，靠计划卡 task_seq 关联：提醒
	task("A", 12, "doing", "等 Q5", nil, 3*time.Hour)             // 不是暂缓：不提醒
	task("A", 13, "shelved", "等 Q9 的答复", nil, 3*time.Hour)       // 卡 8 天前答复：不提醒
	task("A", 14, "shelved", "见 FAQ5", nil, 3*time.Hour)         // FAQ5 不是卡号
	task("A", 15, "shelved", "等 Q5 和 Q8", nil, 3*time.Hour)      // 两张都答复了：只算一个任务，点名最近答复的
	task("B", 1, "shelved", "等 Q5", nil, 3*time.Hour)            // 别的会话的任务，它自己的 Q5 也已答复：只提醒 B 自己

	items, total := st.ShelvedAfterAnswer(ctx, "A", now, 8)
	var seqs []int
	refs := map[int]string{}
	for _, it := range items {
		seqs = append(seqs, it.Seq)
		refs[it.Seq] = it.Ref
	}
	if total != 4 || !sameInts(seqs, []int{1, 10, 11, 15}) {
		t.Fatalf("应提醒 #1 #10 #11 #15：%v total=%d", seqs, total)
	}
	if refs[1] != "Q5" || refs[10] != "Q5" || refs[11] != "Q8" {
		t.Fatalf("点名的卡号不对：%v", refs)
	}
	if refs[15] != "Q5" && refs[15] != "Q8" {
		t.Fatalf("#15 应点名它挂着的已答复卡：%v", refs)
	}
	// B 会话自己的任务：只看 B 自己的卡与任务
	if items, total := st.ShelvedAfterAnswer(ctx, "B", now, 8); total != 1 || len(items) != 1 || items[0].Seq != 1 || items[0].Ref != "Q5" {
		t.Fatalf("B 应只有自己的 #1：%v %d", items, total)
	}
	if items, total := st.ShelvedAfterAnswer(ctx, "A", now, 2); total != 4 || len(items) != 2 {
		t.Fatalf("limit 只截断点名，不改总数: %v %d", items, total)
	}
	if _, total := st.ShelvedAfterAnswer(ctx, "", now, 8); total != 0 {
		t.Fatal("空工作流不该有提醒")
	}

	// 任务被更新（比如置成进行中后又放回暂缓，或补了进展）后提醒消失
	if err := st.db.Model(&model.WorkItem{}).Where("workflow_id = ? AND seq = ?", "A", 1).UpdateColumn("updated_at", now).Error; err != nil {
		t.Fatal(err)
	}
	if _, total := st.ShelvedAfterAnswer(ctx, "A", now, 8); total != 3 {
		t.Fatalf("更新过的任务不该再被点名：total=%d", total)
	}
	// 别人派给 A 的暂缓任务也归 A（按承接者）
	if err := st.db.Create(&model.WorkItem{WorkflowID: "C", Seq: 1, Title: "C 派给 A", State: "shelved", CreatedBy: model.WorkActorAgent,
		Progress: "等 Q5", AssigneeWorkflowID: "A", AssignedByWorkflowID: "C", ClaimState: model.WorkClaimAccepted,
		UpdatedAt: now.Add(-3 * time.Hour)}).Error; err != nil {
		t.Fatal(err)
	}
	if _, total := st.ShelvedAfterAnswer(ctx, "A", now, 8); total != 4 {
		t.Fatalf("派给 A 的暂缓任务也应算 A 的：total=%d", total)
	}
}
