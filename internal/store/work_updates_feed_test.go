package store

import (
	"context"
	"testing"
	"time"
)

// 会话进展卡的数据源：只含智能体写的更新、按提交时间升序、带任务序号与标题，且每次更新各占一条（不是每个任务一条）。
func TestListWorkflowUpdatesIsPerEventAgentOnlyAndOrdered(t *testing.T) {
	st := setupTestStore(t)
	ctx := context.Background()

	a := mustCreateWork(t, st, "wf-feed", "甲任务", nil)
	b := mustCreateWork(t, st, "wf-feed", "乙任务", nil)
	other := mustCreateWork(t, st, "wf-other", "别的工作流", nil)

	// 甲：智能体连续更新三次（状态 + 两次进度）；乙：智能体一次；人在网页上改了一次甲（不应出现）
	steps := []struct {
		id    uint
		patch WorkItemPatch
		who   WorkActor
	}{
		{a.ID, WorkItemPatch{State: ptr("doing"), Progress: ptr("开工")}, agent},
		{b.ID, WorkItemPatch{State: ptr("doing")}, agent},
		{a.ID, WorkItemPatch{Progress: ptr("做到一半")}, agent},
		{a.ID, WorkItemPatch{Percent: ptr(80), Note: "人点了一下"}, human},
		{a.ID, WorkItemPatch{State: ptr("review"), Conclusion: ptr("做完了"), Progress: ptr("收尾")}, agent},
		{other.ID, WorkItemPatch{State: ptr("doing")}, agent},
	}
	for i, s := range steps {
		if _, err := st.UpdateWorkItem(ctx, s.id, s.patch, s.who); err != nil {
			t.Fatalf("第 %d 步更新失败: %v", i, err)
		}
		time.Sleep(2 * time.Millisecond) // 保证提交时间严格递增
	}

	got, err := st.ListWorkflowUpdates(ctx, "wf-feed", time.Time{}, 0)
	if err != nil {
		t.Fatalf("读取进展失败: %v", err)
	}
	// 甲 3 次（doing / 进度 / review）+ 乙 1 次 = 4 条智能体更新；创建记录是人写的，不算；人的备注与别的工作流不算
	var seqs []int
	for _, u := range got {
		if u.ActorKind != "agent" {
			t.Fatalf("只应有智能体的更新，却出现了 %+v", u)
		}
		seqs = append(seqs, u.Seq)
	}
	want := []int{a.Seq, b.Seq, a.Seq, a.Seq}
	if len(seqs) != len(want) {
		t.Fatalf("应有 %d 条进展，实际 %d：%+v", len(want), len(seqs), got)
	}
	for i := range want {
		if seqs[i] != want[i] {
			t.Fatalf("按提交时间升序应为 %v，实际 %v", want, seqs)
		}
	}
	for i := 1; i < len(got); i++ {
		if got[i].CreatedAt.Before(got[i-1].CreatedAt) {
			t.Fatalf("时间必须升序：%v 在 %v 之后", got[i].CreatedAt, got[i-1].CreatedAt)
		}
	}
	if got[0].Title != "甲任务" || got[0].ToState != "doing" || got[0].FromState != "todo" {
		t.Fatalf("第一条应是甲任务 todo→doing：%+v", got[0])
	}
	if last := got[len(got)-1]; last.ToState != "review" || last.State != "review" {
		t.Fatalf("最后一条应是甲任务提交验收：%+v", last)
	}

	// since 之后只剩更晚的；limit 取最新的 N 条且仍是升序
	cut := got[1].CreatedAt
	later, err := st.ListWorkflowUpdates(ctx, "wf-feed", cut, 0)
	if err != nil || len(later) != 2 {
		t.Fatalf("since 之后应剩 2 条（err=%v）：%+v", err, later)
	}
	tail, err := st.ListWorkflowUpdates(ctx, "wf-feed", time.Time{}, 2)
	if err != nil || len(tail) != 2 || tail[0].ID != got[2].ID || tail[1].ID != got[3].ID {
		t.Fatalf("limit=2 应取最新两条并保持升序（err=%v）：%+v", err, tail)
	}

	none, err := st.ListWorkflowUpdates(ctx, "wf-nope", time.Time{}, 0)
	if err != nil || len(none) != 0 {
		t.Fatalf("没有任务的工作流应为空：%v %+v", err, none)
	}
}
