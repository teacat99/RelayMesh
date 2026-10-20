package store

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/teacat99/RelayMesh/v2/internal/model"
)

// 描述回答「这件事到底是什么」：创建时写一次，之后很少改。只改描述不算进展，不能把旧任务顶到「最近进展」前面，
// 也不能占时间线（与调整归属同理）。
func TestWorkItemDescriptionLifecycle(t *testing.T) {
	st := setupTestStore(t)
	ctx := context.Background()

	it := mustCreateWork(t, st, "w", "写描述", func(in *WorkItemInput) {
		in.Description = "  终端历史日志按需加载。\n做到：打开会话只拉最后一屏。  "
	})
	if it.Description != "终端历史日志按需加载。\n做到：打开会话只拉最后一屏。" {
		t.Fatalf("描述应去掉首尾空白并保留换行: %q", it.Description)
	}

	// 只改描述：写库、报告有变化，但不动 updated_at、不占时间线。
	before := it.UpdatedAt
	time.Sleep(5 * time.Millisecond)
	r, err := st.UpdateWorkItem(ctx, it.ID, WorkItemPatch{Description: ptr("改写后的描述")}, human)
	if err != nil {
		t.Fatal(err)
	}
	if !r.Changed || r.Item.Description != "改写后的描述" {
		t.Fatalf("改描述应报告变化并写库: changed=%v desc=%q", r.Changed, r.Item.Description)
	}
	if !r.Item.UpdatedAt.Equal(before) {
		t.Fatalf("只改描述不应动 updated_at: %v → %v", before, r.Item.UpdatedAt)
	}
	if len(r.Item.Updates) != 1 {
		t.Fatalf("只改描述不应占时间线，实际 %d 条", len(r.Item.Updates))
	}

	// 同样的描述再来一次：无变化。
	r, err = st.UpdateWorkItem(ctx, it.ID, WorkItemPatch{Description: ptr("  改写后的描述 ")}, human)
	if err != nil {
		t.Fatal(err)
	}
	if r.Changed {
		t.Fatal("内容相同（忽略首尾空白）的描述不算变化")
	}

	// 描述与别的真实变化一起提交：描述随之保存，updated_at 照常刷新，时间线按进度记一条。
	time.Sleep(5 * time.Millisecond)
	r, err = st.UpdateWorkItem(ctx, it.ID, WorkItemPatch{Description: ptr("跟着进度一起改"), Progress: ptr("做到一半")}, agent)
	if err != nil {
		t.Fatal(err)
	}
	if r.Item.Description != "跟着进度一起改" || !r.Item.UpdatedAt.After(before) || len(r.Item.Updates) != 2 {
		t.Fatalf("描述应随同次更新保存: desc=%q updated=%v timeline=%d", r.Item.Description, r.Item.UpdatedAt, len(r.Item.Updates))
	}

	// 传空串 = 清除；不传 = 保持。
	if _, err = st.UpdateWorkItem(ctx, it.ID, WorkItemPatch{Title: ptr("改个标题")}, human); err != nil {
		t.Fatal(err)
	}
	got, _ := st.GetWorkItem(ctx, it.ID)
	if got.Description != "跟着进度一起改" {
		t.Fatalf("不传描述应保持原样: %q", got.Description)
	}
	r, err = st.UpdateWorkItem(ctx, it.ID, WorkItemPatch{Description: ptr("")}, human)
	if err != nil || !r.Changed || r.Item.Description != "" {
		t.Fatalf("空串应清除描述: err=%v changed=%v desc=%q", err, r.Changed, r.Item.Description)
	}

	// 只调整归属 + 只改描述一起：都是整理动作，仍然不动 updated_at。
	mod, err := st.CreateWorkModule(ctx, "w", 0, "终端", "")
	if err != nil {
		t.Fatal(err)
	}
	stable, _ := st.GetWorkItem(ctx, it.ID)
	time.Sleep(5 * time.Millisecond)
	r, err = st.UpdateWorkItem(ctx, it.ID, WorkItemPatch{Description: ptr("整理时顺手补的说明"), ModuleID: ptr(mod.ID)}, human)
	if err != nil {
		t.Fatal(err)
	}
	if !r.Changed || r.Item.Description != "整理时顺手补的说明" || r.Item.ModuleID == nil || *r.Item.ModuleID != mod.ID {
		t.Fatalf("归属与描述应同时写入: %+v", r.Item)
	}
	if !r.Item.UpdatedAt.Equal(stable.UpdatedAt) {
		t.Fatalf("整理动作（归属 + 描述）不应动 updated_at: %v → %v", stable.UpdatedAt, r.Item.UpdatedAt)
	}
}

func TestWorkItemDescriptionLimitAndSearch(t *testing.T) {
	st := setupTestStore(t)
	ctx := context.Background()

	atLimit := strings.Repeat("字", model.WorkDescriptionMax)
	if _, err := st.CreateWorkItem(ctx, WorkItemInput{WorkflowID: "w", Title: "刚好上限", Description: atLimit}, human); err != nil {
		t.Fatalf("恰好 %d 个字应该允许: %v", model.WorkDescriptionMax, err)
	}
	tooLong := atLimit + "多"
	_, err := st.CreateWorkItem(ctx, WorkItemInput{WorkflowID: "w", Title: "超长", Description: tooLong}, human)
	wantInvalid(t, err, "描述最长")
	it := mustCreateWork(t, st, "w", "更新超长", nil)
	_, err = st.UpdateWorkItem(ctx, it.ID, WorkItemPatch{Description: ptr(tooLong)}, human)
	wantInvalid(t, err, "描述最长")

	// 描述参与文本搜索：标题、标签、进度、结论里都没有的词，靠描述找到。
	mustCreateWork(t, st, "w", "无关的标题", func(in *WorkItemInput) { in.Description = "把冷数据归档到对象存储，保留 90 天索引" })
	mustCreateWork(t, st, "w", "另一件事", nil)
	items, err := st.ListWorkItems(ctx, WorkItemFilter{WorkflowID: "w", Query: "对象存储"})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].Title != "无关的标题" {
		t.Fatalf("搜索应能命中描述: %+v", items)
	}
}

// 状态误点后马上改回来：一来一回互相抵消，时间线上不留痕迹；其它情形一律照常记录。
func TestWorkItemStateFlipIsCancelled(t *testing.T) {
	st := setupTestStore(t)
	ctx := context.Background()

	timeline := func(t *testing.T, id uint) []model.WorkItemUpdate {
		t.Helper()
		got, err := st.GetWorkItem(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		return got.Updates // 新的在前
	}
	move := func(t *testing.T, id uint, patch WorkItemPatch, actor WorkActor) {
		t.Helper()
		if _, err := st.UpdateWorkItem(ctx, id, patch, actor); err != nil {
			t.Fatal(err)
		}
	}
	state := func(s string) WorkItemPatch { return WorkItemPatch{State: ptr(s)} }

	t.Run("来回点不累积", func(t *testing.T) {
		it := mustCreateWork(t, st, "w", "来回点", func(in *WorkItemInput) { in.State = "review" })
		move(t, it.ID, state("doing"), human)
		if n := len(timeline(t, it.ID)); n != 2 {
			t.Fatalf("第一次改状态应记录一条: %d", n)
		}
		move(t, it.ID, state("review"), human)
		ups := timeline(t, it.ID)
		if len(ups) != 1 || ups[0].FromState != "" || ups[0].ToState != "review" {
			t.Fatalf("改回原状态应与上一条抵消，只剩创建记录: %+v", ups)
		}
		for i := 0; i < 5; i++ {
			move(t, it.ID, state("doing"), human)
			move(t, it.ID, state("review"), human)
		}
		if n := len(timeline(t, it.ID)); n != 1 {
			t.Fatalf("反复来回点也不应累积记录: %d", n)
		}
		got, _ := st.GetWorkItem(ctx, it.ID)
		if got.State != "review" {
			t.Fatalf("状态本身必须真的改回去: %s", got.State)
		}
	})

	t.Run("误点已完成再改回：不留记录且重新打开", func(t *testing.T) {
		it := mustCreateWork(t, st, "w", "误点完成", func(in *WorkItemInput) { in.State = "review" })
		move(t, it.ID, state("done"), human)
		if got, _ := st.GetWorkItem(ctx, it.ID); got.ClosedAt == nil {
			t.Fatal("置为已完成应写 closed_at")
		}
		move(t, it.ID, state("review"), human)
		got, _ := st.GetWorkItem(ctx, it.ID)
		if got.ClosedAt != nil || got.State != "review" || len(got.Updates) != 1 {
			t.Fatalf("改回验证中应清掉 closed_at 且不留记录: state=%s closed=%v timeline=%d", got.State, got.ClosedAt, len(got.Updates))
		}
	})

	t.Run("向前推进不合并", func(t *testing.T) {
		it := mustCreateWork(t, st, "w", "向前推进", nil)
		move(t, it.ID, state("doing"), human)
		move(t, it.ID, state("review"), human)
		if n := len(timeline(t, it.ID)); n != 3 {
			t.Fatalf("todo→doing→review 每一步都是里程碑，应记 3 条（含创建）: %d", n)
		}
	})

	t.Run("链条末端改回只撤最后一步", func(t *testing.T) {
		it := mustCreateWork(t, st, "w", "链条", nil)
		move(t, it.ID, state("doing"), human)
		move(t, it.ID, state("review"), human)
		move(t, it.ID, state("doing"), human) // 与上一条（doing→review）互为反向：撤掉它
		ups := timeline(t, it.ID)
		if len(ups) != 2 || ups[0].ToState != "doing" || ups[0].FromState != "todo" {
			t.Fatalf("应剩 创建 + todo→doing: %+v", ups)
		}
	})

	t.Run("带文字的记录不合并", func(t *testing.T) {
		it := mustCreateWork(t, st, "w", "带备注", nil)
		move(t, it.ID, WorkItemPatch{State: ptr("doing"), Note: "先开工，等方案确认"}, human)
		move(t, it.ID, state("todo"), human) // 上一条带文字：保留
		if n := len(timeline(t, it.ID)); n != 3 {
			t.Fatalf("上一条带备注，改回来不应撤掉它: %d", n)
		}
		it2 := mustCreateWork(t, st, "w", "改回时带备注", nil)
		move(t, it2.ID, state("doing"), human)
		move(t, it2.ID, WorkItemPatch{State: ptr("todo"), Note: "方向不对，先放回去"}, human)
		if n := len(timeline(t, it2.ID)); n != 3 {
			t.Fatalf("改回时写了备注，是有信息量的记录，不应合并: %d", n)
		}
	})

	t.Run("不同操作者不合并", func(t *testing.T) {
		it := mustCreateWork(t, st, "w", "两个人", nil)
		move(t, it.ID, state("doing"), human)
		move(t, it.ID, state("todo"), agent)
		if n := len(timeline(t, it.ID)); n != 3 {
			t.Fatalf("人改、智能体改回是两个独立的决定，应各记一条: %d", n)
		}
	})

	t.Run("同时改了阶段不合并", func(t *testing.T) {
		it := mustCreateWork(t, st, "w", "带阶段", nil)
		move(t, it.ID, state("doing"), human)
		move(t, it.ID, WorkItemPatch{State: ptr("todo"), Stage: ptr("dev")}, human)
		if n := len(timeline(t, it.ID)); n != 3 {
			t.Fatalf("这一次还改了阶段，不应当作纯粹的误点: %d", n)
		}
	})

	t.Run("创建记录不会被撤", func(t *testing.T) {
		it := mustCreateWork(t, st, "w", "创建即进行中", func(in *WorkItemInput) { in.State = "doing" })
		move(t, it.ID, state("todo"), human)
		ups := timeline(t, it.ID)
		if len(ups) != 2 || ups[1].FromState != "" || ups[1].ToState != "doing" {
			t.Fatalf("创建记录必须保留: %+v", ups)
		}
	})

	t.Run("时间窗口", func(t *testing.T) {
		backdate := func(t *testing.T, id uint, ago time.Duration) {
			t.Helper()
			ups := timeline(t, id)
			if err := st.db.Model(&model.WorkItemUpdate{}).Where("id = ?", ups[0].ID).
				Update("created_at", time.Now().Add(-ago)).Error; err != nil {
				t.Fatal(err)
			}
		}
		window := time.Duration(model.WorkFlipMergeMinutes) * time.Minute

		inside := mustCreateWork(t, st, "w", "窗口内", nil)
		move(t, inside.ID, state("doing"), human)
		backdate(t, inside.ID, window-time.Minute)
		move(t, inside.ID, state("todo"), human)
		if n := len(timeline(t, inside.ID)); n != 1 {
			t.Fatalf("窗口内改回应抵消: %d", n)
		}

		outside := mustCreateWork(t, st, "w", "窗口外", nil)
		move(t, outside.ID, state("doing"), human)
		backdate(t, outside.ID, window+time.Minute)
		move(t, outside.ID, state("todo"), human)
		if n := len(timeline(t, outside.ID)); n != 3 {
			t.Fatalf("超过窗口的反复是真实的重开，应照常记录: %d", n)
		}
	})
}
