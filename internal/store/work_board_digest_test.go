package store

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/teacat99/RelayMesh/v2/internal/model"
)

// 回执头 board: 行的数据（#180 B3a）：五类待办的口径、列哪几个编号、缓存与作废。

// reset 清空缓存（只给测试用：要在 5 秒有效期内用不同的「现在」重算时）。
func (c *boardDigestCache) reset() {
	c.mu.Lock()
	c.m = nil
	c.mu.Unlock()
}

func wantGroup(t *testing.T, name string, g BoardDigestGroup, total int, seqs ...int) {
	t.Helper()
	if g.Total != total {
		t.Fatalf("%s：个数应为 %d，得到 %d（%+v）", name, total, g.Total, g)
	}
	if len(g.Seqs) != len(seqs) {
		t.Fatalf("%s：编号应为 %v，得到 %v", name, seqs, g.Seqs)
	}
	for i := range seqs {
		if g.Seqs[i] != seqs[i] {
			t.Fatalf("%s：编号应为 %v，得到 %v", name, seqs, g.Seqs)
		}
	}
}

func boardDigestAt(t *testing.T, st *Store, wf string, at time.Time) *BoardDigest {
	t.Helper()
	d, err := st.BoardDigestFor(context.Background(), wf, at, 2*time.Hour)
	if err != nil {
		t.Fatalf("BoardDigestFor(%s): %v", wf, err)
	}
	return d
}

// 五类待办的完整流程：指派 → 接取 / 拒绝 / 交付 / 退回 / 重新交付 / 验收，每一步两侧的汇总都随之变化。
func TestBoardDigestCategoriesFlow(t *testing.T) {
	st, ctx, _ := boardFixture(t)
	seedWorkflowDir(t, st, "wf-3", "devhost", "/home/user/a", time.Now())
	mgr := managerAs("wf-1", WorkPolicyEnforce)
	dev := agentAs("wf-2", WorkPolicyEnforce)
	human := WorkActor{Kind: model.WorkActorHuman}
	now := time.Now()
	late := now.Add(3 * time.Hour) // 过了 2 小时阈值

	if d := boardDigestAt(t, st, "wf-1", now); !d.Empty() {
		t.Fatalf("没有任何协作指派时应为空: %+v", d)
	}
	if d := boardDigestAt(t, st, "", now); !d.Empty() {
		t.Fatalf("工作流编号为空应得到空汇总: %+v", d)
	}

	// 派发者 wf-1 一口气派 7 个给 wf-2：承接者侧列最早的 5 个。
	var items []*model.WorkItem
	for i := 0; i < 7; i++ {
		items = append(items, mustCreate(t, st, WorkItemInput{WorkflowID: "wf-1", Title: fmt.Sprintf("任务 %d", i+1), Assignee: "wf-2"}, mgr))
	}
	seq := func(i int) int { return items[i].Seq }

	d2 := boardDigestAt(t, st, "wf-2", now)
	wantGroup(t, "wf-2 待接取", d2.ToAccept, 7, seq(0), seq(1), seq(2), seq(3), seq(4))
	if d2.ToAccept.Oldest.IsZero() || !d2.ToAccept.Oldest.Equal(*items[0].AssignedAt) {
		t.Fatalf("最久的一条应是第一个派出的: %v vs %v", d2.ToAccept.Oldest, items[0].AssignedAt)
	}
	if d2.Returned.Total+d2.AwaitingReview.Total+d2.Declined.Total+d2.Stale.Total != 0 {
		t.Fatalf("承接者侧只有「待接取」: %+v", d2)
	}
	// 派发者侧：刚派出去，没到阈值时什么都没有；过了阈值才算久无人接。
	if d := boardDigestAt(t, st, "wf-1", now); !d.Empty() {
		t.Fatalf("刚派出的指派对派发者不是待办: %+v", d)
	}
	wantGroup(t, "wf-1 久无人接", boardDigestAt(t, st, "wf-1", late).Stale, 7, seq(0), seq(1), seq(2), seq(3), seq(4))

	// 接取：待接取少一个（没有等 5 秒缓存过期——任务写入使缓存作废）。
	mustUpdate(t, st, items[0].ID, WorkItemPatch{Claim: "accept"}, dev)
	wantGroup(t, "接取后 wf-2 待接取", boardDigestAt(t, st, "wf-2", now).ToAccept, 6, seq(1), seq(2), seq(3), seq(4), seq(5))
	wantGroup(t, "接取后 wf-1 久无人接", boardDigestAt(t, st, "wf-1", late).Stale, 6, seq(1), seq(2), seq(3), seq(4), seq(5))

	// 交付：派发者侧出现「待验收」，承接者侧没有任何东西指向这一条。
	mustUpdate(t, st, items[0].ID, WorkItemPatch{Claim: "deliver", Note: "第一版好了"}, dev)
	wantGroup(t, "wf-1 待验收", boardDigestAt(t, st, "wf-1", now).AwaitingReview, 1, seq(0))
	if g := boardDigestAt(t, st, "wf-2", now).Returned; g.Total != 0 {
		t.Fatalf("交付后还没被退回: %+v", g)
	}

	// 退回：派发者侧的待验收消失，承接者侧出现「被退回」，且只写普通进展不会让它消失。
	mustUpdate(t, st, items[0].ID, WorkItemPatch{Claim: "reject_delivery", Note: "漏了边界用例"}, mgr)
	if g := boardDigestAt(t, st, "wf-1", now).AwaitingReview; g.Total != 0 {
		t.Fatalf("退回后派发者侧不再待验收: %+v", g)
	}
	wantGroup(t, "wf-2 被退回", boardDigestAt(t, st, "wf-2", now).Returned, 1, seq(0))
	mustUpdate(t, st, items[0].ID, WorkItemPatch{Note: "在补边界用例"}, dev)
	wantGroup(t, "写了普通进展后仍是被退回", boardDigestAt(t, st, "wf-2", now).Returned, 1, seq(0))

	// 重新交付：被退回消失，待验收回来。
	mustUpdate(t, st, items[0].ID, WorkItemPatch{Claim: "deliver", Note: "边界用例补好了"}, dev)
	if g := boardDigestAt(t, st, "wf-2", now).Returned; g.Total != 0 {
		t.Fatalf("重新交付后不再是被退回: %+v", g)
	}
	wantGroup(t, "重新交付后 wf-1 待验收", boardDigestAt(t, st, "wf-1", now).AwaitingReview, 1, seq(0))

	// 验收：整件事结束，两侧都不再有它。
	mustUpdate(t, st, items[0].ID, WorkItemPatch{Claim: "accept_delivery", Conclusion: sp("验收通过")}, mgr)
	if g := boardDigestAt(t, st, "wf-1", now).AwaitingReview; g.Total != 0 {
		t.Fatalf("验收后不再待验收: %+v", g)
	}

	// 拒绝：承接者侧少一个待接取，派发者侧出现「被拒绝」。
	mustUpdate(t, st, items[1].ID, WorkItemPatch{Claim: "decline", Note: "手上排满了"}, dev)
	wantGroup(t, "拒绝后 wf-2 待接取", boardDigestAt(t, st, "wf-2", now).ToAccept, 5, seq(2), seq(3), seq(4), seq(5), seq(6))
	wantGroup(t, "wf-1 被拒绝", boardDigestAt(t, st, "wf-1", now).Declined, 1, seq(1))
	// 被拒绝的指派不再算久无人接。
	wantGroup(t, "拒绝后 wf-1 久无人接", boardDigestAt(t, st, "wf-1", late).Stale, 5, seq(2), seq(3), seq(4), seq(5), seq(6))

	// 搁置 = 结束：不再提醒任何一侧。
	mustUpdate(t, st, items[6].ID, WorkItemPatch{State: sp(model.WorkStateShelved), Conclusion: sp("先不做了")}, mgr)
	wantGroup(t, "搁置后 wf-2 待接取", boardDigestAt(t, st, "wf-2", now).ToAccept, 4, seq(2), seq(3), seq(4), seq(5))

	// 改派：原承接者那边的待办随之消失，新承接者那边出现。
	mustUpdate(t, st, items[5].ID, WorkItemPatch{Assignee: sp("wf-3")}, mgr)
	wantGroup(t, "改派后 wf-2 待接取", boardDigestAt(t, st, "wf-2", now).ToAccept, 3, seq(2), seq(3), seq(4))
	wantGroup(t, "改派后 wf-3 待接取", boardDigestAt(t, st, "wf-3", now).ToAccept, 1, seq(5))

	// 自己派给自己的不算协作；人在 Web 上派的任务算承接者的待办，但派发者（无工作流）一侧没有对应汇总。
	mustCreate(t, st, WorkItemInput{WorkflowID: "wf-1", Title: "自己做", Assignee: "wf-1"}, mgr)
	if d := boardDigestAt(t, st, "wf-1", now); d.ToAccept.Total != 0 || d.Declined.Total != 1 {
		t.Fatalf("自己派给自己的任务不应出现在 wf-1 的待办里: %+v", d)
	}
	byHuman := mustCreate(t, st, WorkItemInput{WorkflowID: "wf-1", Title: "用户派的", Assignee: "wf-2"}, human)
	wantGroup(t, "人派的任务算 wf-2 待接取", boardDigestAt(t, st, "wf-2", now).ToAccept, 4, seq(2), seq(3), seq(4), byHuman.Seq)

	// 数据口径与存储一致：以后写库的路径都走 Create / Update / Delete，随时可以删除任务而汇总跟着变。
	if _, err := st.DeleteWorkItem(ctx, byHuman.ID); err != nil {
		t.Fatal(err)
	}
	wantGroup(t, "删除后 wf-2 待接取", boardDigestAt(t, st, "wf-2", now).ToAccept, 3, seq(2), seq(3), seq(4))
}

// 久无人接：阈值边界取「等于即算」；承接者的工作流已不存在时单独成类。
func TestBoardDigestStaleAndGone(t *testing.T) {
	st, ctx, _ := boardFixture(t)
	seedWorkflowDir(t, st, "wf-3", "devhost", "/home/user/a", time.Now())
	mgr := managerAs("wf-1", WorkPolicyEnforce)
	a := mustCreate(t, st, WorkItemInput{WorkflowID: "wf-1", Title: "派给 wf-2", Assignee: "wf-2"}, mgr)
	b := mustCreate(t, st, WorkItemInput{WorkflowID: "wf-1", Title: "派给 wf-3", Assignee: "wf-3"}, mgr)

	threshold := 90 * time.Minute
	at := a.AssignedAt.Add(threshold)
	st.boardDigest.reset() // 边界值差 1 纳秒，要绕开 5 秒缓存才测得到
	d, err := st.BoardDigestFor(ctx, "wf-1", at.Add(-time.Nanosecond), threshold)
	if err != nil {
		t.Fatal(err)
	}
	if d.Stale.Total != 0 {
		t.Fatalf("还差 1 纳秒到阈值，谁都不应算久无人接: %+v", d.Stale)
	}
	st.boardDigest.reset()
	d, err = st.BoardDigestFor(ctx, "wf-1", at, threshold)
	if err != nil {
		t.Fatal(err)
	}
	if d.Stale.Total < 1 || d.Stale.Seqs[0] != a.Seq {
		t.Fatalf("恰好到阈值应算久无人接: %+v", d.Stale)
	}

	// wf-3 的会话全被删除：它不会再来接了，这条从「久无人接」挪到「承接者已不存在」。
	if err := st.db.Where("workflow_id = ?", "wf-3").Delete(&model.FeedbackSession{}).Error; err != nil {
		t.Fatal(err)
	}
	late := b.AssignedAt.Add(3 * time.Hour)
	d, err = st.BoardDigestFor(ctx, "wf-1", late, 2*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	wantGroup(t, "仍在的承接者", d.Stale, 1, a.Seq)
	wantGroup(t, "已不存在的承接者", d.StaleGone, 1, b.Seq)
}

// 缓存：5 秒内同一个工作流同一个阈值不重复查库；任务写入、过期、时钟回拨、换阈值都会重算；个数有上限。
func TestBoardDigestCache(t *testing.T) {
	st, ctx, _ := boardFixture(t)
	mgr := managerAs("wf-1", WorkPolicyEnforce)
	mustCreate(t, st, WorkItemInput{WorkflowID: "wf-1", Title: "一", Assignee: "wf-2"}, mgr)
	now := time.Now()
	get := func(wf string, at time.Time, stale time.Duration) *BoardDigest {
		t.Helper()
		d, err := st.BoardDigestFor(ctx, wf, at, stale)
		if err != nil {
			t.Fatal(err)
		}
		return d
	}

	d1 := get("wf-2", now, 0)
	if d1.ToAccept.Total != 1 {
		t.Fatalf("应有 1 个待接取: %+v", d1)
	}
	if get("wf-2", now.Add(time.Second), 0) != d1 {
		t.Fatal("5 秒内没有写入，应命中缓存（同一个对象）")
	}
	if get("wf-2", now.Add(time.Second), time.Hour) == d1 {
		t.Fatal("阈值不同是另一份缓存")
	}

	mustCreate(t, st, WorkItemInput{WorkflowID: "wf-1", Title: "二", Assignee: "wf-2"}, mgr)
	d2 := get("wf-2", now.Add(2*time.Second), 0)
	if d2 == d1 || d2.ToAccept.Total != 2 {
		t.Fatalf("任务写入后缓存应作废并看到新状态: %+v", d2)
	}

	if d3 := get("wf-2", now.Add(2*time.Second+boardDigestTTL), 0); d3 == d2 {
		t.Fatal("过了有效期应重算")
	}
	if d4 := get("wf-2", now, 0); d4.ToAccept.Total != 2 {
		t.Fatalf("时钟回拨（现在比缓存时间还早）也应安全重算: %+v", d4)
	}

	for i := 0; i < boardDigestCacheMax+10; i++ {
		get(fmt.Sprintf("wf-fill-%d", i), now, 0)
	}
	st.boardDigest.mu.Lock()
	n := len(st.boardDigest.m)
	st.boardDigest.mu.Unlock()
	if n > boardDigestCacheMax {
		t.Fatalf("缓存条数应有上限 %d，实际 %d", boardDigestCacheMax, n)
	}
}

// 网页侧栏徽标的数据（#180 B3b）：每个工作流的待接取个数与最早派出时间，口径与回执头「指派待你接取」一致。
func TestBoardOfferedByWorkflow(t *testing.T) {
	st, ctx, _ := boardFixture(t)
	seedWorkflowDir(t, st, "wf-3", "devhost", "/home/user/a", time.Now())
	mgr := managerAs("wf-1", WorkPolicyEnforce)
	dev := agentAs("wf-2", WorkPolicyEnforce)
	human := WorkActor{Kind: model.WorkActorHuman}

	if got, err := st.BoardOfferedByWorkflow(ctx); err != nil || len(got) != 0 {
		t.Fatalf("没有指派时应为空: %+v err=%v", got, err)
	}

	a1 := mustCreate(t, st, WorkItemInput{WorkflowID: "wf-1", Title: "派给 wf-2 一", Assignee: "wf-2"}, mgr)
	a2 := mustCreate(t, st, WorkItemInput{WorkflowID: "wf-1", Title: "派给 wf-2 二", Assignee: "wf-2"}, mgr)
	b1 := mustCreate(t, st, WorkItemInput{WorkflowID: "wf-1", Title: "派给 wf-3", Assignee: "wf-3"}, mgr)
	mustCreate(t, st, WorkItemInput{WorkflowID: "wf-1", Title: "自己做", Assignee: "wf-1"}, mgr) // 自己派给自己：不算
	b2 := mustCreate(t, st, WorkItemInput{WorkflowID: "wf-1", Title: "用户派给 wf-3", Assignee: "wf-3"}, human)

	got, err := st.BoardOfferedByWorkflow(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// 两个工作流都是 2 个：同数时按工作流编号排。
	if len(got) != 2 || got[0].WorkflowID != "wf-2" || got[0].Count != 2 || got[1].WorkflowID != "wf-3" || got[1].Count != 2 {
		t.Fatalf("汇总不符: %+v", got)
	}
	if got[0].OldestAt == nil || !got[0].OldestAt.Equal(*a1.AssignedAt) || got[1].OldestAt == nil || !got[1].OldestAt.Equal(*b1.AssignedAt) {
		t.Fatalf("最早派出时间应是各自第一个指派: %+v", got)
	}

	// 接取一个 → wf-2 剩 1 个，最早的换成后一个；拒绝掉最后一个 → wf-2 不再出现；搁置 wf-3 的一个 → 只剩 1 个。
	mustUpdate(t, st, a1.ID, WorkItemPatch{Claim: "accept"}, dev)
	got, _ = st.BoardOfferedByWorkflow(ctx)
	if len(got) != 2 || got[1].WorkflowID != "wf-2" || got[1].Count != 1 || !got[1].OldestAt.Equal(*a2.AssignedAt) {
		t.Fatalf("接取后 wf-2 应剩 1 个且最早的是第二个: %+v", got)
	}
	mustUpdate(t, st, a2.ID, WorkItemPatch{Claim: "decline", Note: "排满了"}, dev)
	mustUpdate(t, st, b1.ID, WorkItemPatch{State: sp(model.WorkStateShelved), Conclusion: sp("先不做")}, mgr)
	got, _ = st.BoardOfferedByWorkflow(ctx)
	if len(got) != 1 || got[0].WorkflowID != "wf-3" || got[0].Count != 1 || !got[0].OldestAt.Equal(*b2.AssignedAt) {
		t.Fatalf("拒绝与搁置之后只应剩 wf-3 的 1 个（用户派的）: %+v", got)
	}
}

// 派发者侧每次回执都要按 assigned_by_workflow_id 查，必须有索引（升级时由 AutoMigrate 补上，不动数据）。
func TestWorkItemAssignedByIndex(t *testing.T) {
	st, _ := projTestStore(t)
	if !st.db.Migrator().HasIndex(&model.WorkItem{}, "idx_work_items_assigned_by_workflow_id") {
		t.Fatal("work_items 应有 assigned_by_workflow_id 的索引")
	}
}
