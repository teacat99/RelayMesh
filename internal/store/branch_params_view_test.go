package store

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/teacat99/RelayMesh/v2/internal/model"
)

// 参数事件的界面视图（#236 P3 S3）：列表上「撤销」按钮的显隐与真正撤销共用同一份规则，会话页消息流里带参数事件行。

func viewByID(views []BranchParamEventView, id uint) BranchParamEventView {
	for _, v := range views {
		if v.ID == id {
			return v
		}
	}
	return BranchParamEventView{}
}

// mustApplyOne 经主会话写一项参数，返回产生的那一条事件。
func mustApplyOne(t *testing.T, st *Store, branch string, p SessionParamsPatch) model.BranchParamEvent {
	t.Helper()
	res, err := st.ApplySessionParams(context.Background(), "wf-main", branch, p, time.Now())
	if err != nil || len(res.Applied) != 1 {
		t.Fatalf("前提失败: %+v %v", res, err)
	}
	return res.Applied[0]
}

// 对每种局面：列表上标「能撤销」的，真去撤销必须成功；标「不能」的，真去撤销必须以同一句原因失败。
// 先验不能的、后撤能的——撤销会改变局面，反过来会让别的事件的判定变化。
func TestListBranchParamEventViews_RevertableMirrorsRevert(t *testing.T) {
	ctx := context.Background()
	now := time.Now()

	scenarios := []struct {
		name   string
		branch string
		setup  func(t *testing.T, st *Store) // 造出各种事件
	}{
		{"单条写入可撤销", "wf-b1", func(t *testing.T, st *Store) {
			mustApplyOne(t, st, "wf-b1", SessionParamsPatch{Resident: strp("规则甲")})
		}},
		{"同字段两次写入：只有最新的可撤销", "wf-b1", func(t *testing.T, st *Store) {
			mustApplyOne(t, st, "wf-b1", SessionParamsPatch{Resident: strp("规则甲")})
			mustApplyOne(t, st, "wf-b1", SessionParamsPatch{Resident: strp("规则乙")})
		}},
		{"用户随后又改过：不可撤销", "wf-b1", func(t *testing.T, st *Store) {
			mustApplyOne(t, st, "wf-b1", SessionParamsPatch{Resident: strp("主会话写的")})
			if _, _, err := st.SaveResidentPrompt(ctx, "wf-b1", "用户又改了", "alice", now); err != nil {
				t.Fatal(err)
			}
		}},
		{"不同字段互不影响", "wf-b1", func(t *testing.T, st *Store) {
			mustApplyOne(t, st, "wf-b1", SessionParamsPatch{Title: strp("标题甲")})
			mustApplyOne(t, st, "wf-b1", SessionParamsPatch{Resident: strp("规则丙")})
			mustApplyOne(t, st, "wf-b1", SessionParamsPatch{Phase: strp("plan")})
		}},
		{"原先没有标题：不可撤销成无标题", "wf-b3", func(t *testing.T, st *Store) {
			mkWF(t, st, "wf-b3", "")
			mustLink(t, st, "wf-main", "wf-b3", model.BranchCreatedByHuman)
			setParamsTier(t, st, "wf-b3", model.ExposeParamsAll)
			mustApplyOne(t, st, "wf-b3", SessionParamsPatch{Title: strp("起个名")})
		}},
		{"撤销链：撤掉最新的，前一条才可撤；已撤销的与撤销记录不是候选", "wf-b1", func(t *testing.T, st *Store) {
			mustApplyOne(t, st, "wf-b1", SessionParamsPatch{Resident: strp("规则甲")})
			b := mustApplyOne(t, st, "wf-b1", SessionParamsPatch{Resident: strp("规则乙")})
			if _, err := st.RevertBranchParamEvent(ctx, "wf-b1", b.ID, "alice", now); err != nil {
				t.Fatal(err)
			}
		}},
		{"关系结束后仍可撤销", "wf-b1", func(t *testing.T, st *Store) {
			mustApplyOne(t, st, "wf-b1", SessionParamsPatch{Resident: strp("规则甲")})
			if _, err := st.CloseBranch(ctx, "wf-b1", model.BranchCreatedByHuman); err != nil {
				t.Fatal(err)
			}
		}},
	}

	for _, sc := range scenarios {
		sc := sc
		t.Run(sc.name, func(t *testing.T) {
			st := paramsFixture(t, model.ExposeParamsAll)
			sc.setup(t, st)
			views, err := st.ListBranchParamEventViews(ctx, sc.branch, 50, now)
			if err != nil {
				t.Fatal(err)
			}
			if len(views) == 0 {
				t.Fatal("应有事件")
			}
			var able, blocked []BranchParamEventView
			for _, v := range views {
				if !revertCandidate(v.BranchParamEvent) {
					if v.Revertable || v.RevertBlocked != "" {
						t.Fatalf("非候选（用户的 / 撤销记录 / 已撤销）不该有撤销标记: %+v", v)
					}
					continue
				}
				if v.Revertable {
					if v.RevertBlocked != "" {
						t.Fatalf("能撤销的不该带原因: %+v", v)
					}
					able = append(able, v)
				} else {
					blocked = append(blocked, v)
				}
			}
			for _, v := range blocked {
				if v.RevertBlocked == "" {
					t.Fatalf("不能撤销的候选应写明原因: %+v", v)
				}
				_, err := st.RevertBranchParamEvent(ctx, sc.branch, v.ID, "alice", now)
				var ae *AppError
				if err == nil || !errors.As(err, &ae) || ae.Message != v.RevertBlocked {
					t.Fatalf("列表说不能撤销（%q），真去撤销应以同一句原因失败: %v", v.RevertBlocked, err)
				}
			}
			for _, v := range able {
				if _, err := st.RevertBranchParamEvent(ctx, sc.branch, v.ID, "alice", now); err != nil {
					t.Fatalf("列表说能撤销（%s），真去撤销却失败: %v", v.Summary, err)
				}
			}
		})
	}
}

// 具体期望：避免「全都不能撤销」也能让上面的镜像测试通过。
func TestListBranchParamEventViews_ConcreteVerdicts(t *testing.T) {
	ctx := context.Background()
	now := time.Now()

	t.Run("同字段：只有最新的可撤销；撤掉最新的之后前一条可撤销", func(t *testing.T) {
		st := paramsFixture(t, model.ExposeParamsAll)
		first := mustApplyOne(t, st, "wf-b1", SessionParamsPatch{Resident: strp("规则甲")})
		second := mustApplyOne(t, st, "wf-b1", SessionParamsPatch{Resident: strp("规则乙")})
		views, _ := st.ListBranchParamEventViews(ctx, "wf-b1", 50, now)
		if v := viewByID(views, second.ID); !v.Revertable {
			t.Fatalf("最新的写入应可撤销: %+v", v)
		}
		if v := viewByID(views, first.ID); v.Revertable || !strings.Contains(v.RevertBlocked, "不是最新的修改") {
			t.Fatalf("较早的写入应不可撤销并写明原因: %+v", v)
		}
		if _, err := st.RevertBranchParamEvent(ctx, "wf-b1", second.ID, "alice", now); err != nil {
			t.Fatal(err)
		}
		views, _ = st.ListBranchParamEventViews(ctx, "wf-b1", 50, now)
		if v := viewByID(views, first.ID); !v.Revertable {
			t.Fatalf("撤掉最新的之后，前一条应成为可撤销: %+v", v)
		}
		if v := viewByID(views, second.ID); v.Revertable || v.RevertedAt == nil {
			t.Fatalf("已撤销的应带 reverted_at 且不再可撤销: %+v", v)
		}
		// 顺序：新的在前；撤销记录排在被撤销的写入之前。
		if len(views) != 3 || views[0].RevertOf != second.ID || views[1].ID != second.ID || views[2].ID != first.ID {
			t.Fatalf("列表应新的在前: %+v", views)
		}
	})

	t.Run("用户随后又改过：当前值已变，不可撤销", func(t *testing.T) {
		st := paramsFixture(t, model.ExposeParamsAll)
		ev := mustApplyOne(t, st, "wf-b1", SessionParamsPatch{Resident: strp("主会话写的")})
		if _, _, err := st.SaveResidentPrompt(ctx, "wf-b1", "用户又改了", "alice", now); err != nil {
			t.Fatal(err)
		}
		views, _ := st.ListBranchParamEventViews(ctx, "wf-b1", 50, now)
		if v := viewByID(views, ev.ID); v.Revertable || !strings.Contains(v.RevertBlocked, "之后又被改过") {
			t.Fatalf("用户改过之后不该再显示撤销: %+v", v)
		}
	})

	t.Run("不同字段互不影响", func(t *testing.T) {
		st := paramsFixture(t, model.ExposeParamsAll)
		a := mustApplyOne(t, st, "wf-b1", SessionParamsPatch{Title: strp("标题甲")})
		b := mustApplyOne(t, st, "wf-b1", SessionParamsPatch{Resident: strp("规则丙")})
		c := mustApplyOne(t, st, "wf-b1", SessionParamsPatch{Phase: strp("plan")})
		views, _ := st.ListBranchParamEventViews(ctx, "wf-b1", 50, now)
		for _, id := range []uint{a.ID, b.ID, c.ID} {
			if v := viewByID(views, id); !v.Revertable {
				t.Fatalf("不同字段的写入各自可撤销: %+v", v)
			}
		}
	})

	t.Run("撤销记录与用户的记录不是候选：没有撤销标记，也不给原因", func(t *testing.T) {
		st := paramsFixture(t, model.ExposeParamsAll)
		ev := mustApplyOne(t, st, "wf-b1", SessionParamsPatch{Title: strp("标题甲")})
		out, err := st.RevertBranchParamEvent(ctx, "wf-b1", ev.ID, "alice", now)
		if err != nil {
			t.Fatal(err)
		}
		// 手写两行：一行用户写的修改；一行「主会话写的、但带 revert_of」（不该出现，但库里真有这样的行时也不能当成可撤销）。
		human := model.BranchParamEvent{CreatedAt: now, ParentWorkflowID: "wf-main", BranchWorkflowID: "wf-b1", Field: model.ParamFieldPhase,
			By: model.BranchParamByHuman, Summary: "手写的", OldValue: "discuss", NewValue: "dev"}
		odd := model.BranchParamEvent{CreatedAt: now, ParentWorkflowID: "wf-main", BranchWorkflowID: "wf-b1", Field: model.ParamFieldPhase,
			By: "agent:wf-main", Summary: "怪行", OldValue: "discuss", NewValue: "dev", RevertOf: ev.ID}
		for _, row := range []*model.BranchParamEvent{&human, &odd} {
			if err := st.DB().Create(row).Error; err != nil {
				t.Fatal(err)
			}
		}
		views, _ := st.ListBranchParamEventViews(ctx, "wf-b1", 50, now)
		for _, id := range []uint{out.Event.ID, human.ID, odd.ID, ev.ID} {
			if v := viewByID(views, id); v.ID == 0 || v.Revertable || v.RevertBlocked != "" {
				t.Fatalf("事件 %d 不该有撤销标记: %+v", id, v)
			}
		}
	})

	t.Run("原先没有标题：不可撤销成无标题", func(t *testing.T) {
		st := paramsFixture(t, model.ExposeParamsAll)
		mkWF(t, st, "wf-b3", "")
		mustLink(t, st, "wf-main", "wf-b3", model.BranchCreatedByHuman)
		setParamsTier(t, st, "wf-b3", model.ExposeParamsAll)
		ev := mustApplyOne(t, st, "wf-b3", SessionParamsPatch{Title: strp("起个名")})
		views, _ := st.ListBranchParamEventViews(ctx, "wf-b3", 50, now)
		if v := viewByID(views, ev.ID); v.Revertable || !strings.Contains(v.RevertBlocked, "无标题") {
			t.Fatalf("没有旧标题可还原: %+v", v)
		}
	})
}

// 没有事件的分支：空列表、不读当前值也不报错；limit 生效。
func TestListBranchParamEventViews_EmptyAndLimit(t *testing.T) {
	ctx := context.Background()
	now := time.Now()
	st := paramsFixture(t, model.ExposeParamsAll)
	views, err := st.ListBranchParamEventViews(ctx, "wf-b1", 10, now)
	if err != nil || len(views) != 0 {
		t.Fatalf("没有事件应是空: %v %v", views, err)
	}
	if views, err = st.ListBranchParamEventViews(ctx, "wf-nobody", 10, now); err != nil || len(views) != 0 {
		t.Fatalf("不存在的分支也只是空: %v %v", views, err)
	}
	for _, r := range []string{"规则甲", "规则乙", "规则丙"} {
		mustApplyOne(t, st, "wf-b1", SessionParamsPatch{Resident: strp(r)})
	}
	if views, _ = st.ListBranchParamEventViews(ctx, "wf-b1", 2, now); len(views) != 2 || !views[0].Revertable || views[1].Revertable {
		t.Fatalf("limit=2 应只返回最新两条，且只有最新的可撤销: %+v", views)
	}
}

// 会话页消息流：参数事件作为事件行出现在主会话与分支两边的流里，别的会话看不到；没有事件就没有这类行（INV-2）。
func TestBranchFeed_ParamEventRows(t *testing.T) {
	ctx := context.Background()
	now := time.Now()
	st := paramsFixture(t, model.ExposeParamsAll)

	rows := func(wf string) []BranchFeedItem {
		t.Helper()
		feed, err := st.BranchFeed(ctx, wf, 100)
		if err != nil {
			t.Fatal(err)
		}
		var out []BranchFeedItem
		for _, it := range feed.Items {
			if it.Event == "params_changed" || it.Event == "params_reverted" {
				out = append(out, it)
			}
		}
		return out
	}
	for _, wf := range []string{"wf-main", "wf-b1", "wf-other"} {
		if got := rows(wf); len(got) != 0 {
			t.Fatalf("还没有参数事件时 %s 的消息流不该有这类行: %+v", wf, got)
		}
	}

	residentEv := mustApplyOne(t, st, "wf-b1", SessionParamsPatch{Resident: strp("规则甲")})
	mustApplyOne(t, st, "wf-b1", SessionParamsPatch{Title: strp("新标题")})
	if _, err := st.RevertBranchParamEvent(ctx, "wf-b1", residentEv.ID, "alice", now); err != nil {
		t.Fatal(err)
	}

	for _, wf := range []string{"wf-main", "wf-b1"} {
		got := rows(wf)
		if len(got) != 3 {
			t.Fatalf("%s 应看到 2 条修改 + 1 条撤销: %+v", wf, got)
		}
		var changed, reverted int
		for _, it := range got {
			if it.Kind != "event" || it.Branch != "wf-b1" || it.Text == "" || it.Field == "" || it.ID == 0 || it.At.IsZero() {
				t.Fatalf("事件行字段不全: %+v", it)
			}
			switch it.Event {
			case "params_changed":
				changed++
				if it.By != "agent:wf-main" {
					t.Fatalf("修改行应记 agent:wf-main: %+v", it)
				}
				if it.ID == residentEv.ID && (!it.Reverted || it.Revertable) {
					t.Fatalf("已撤销的常驻规则修改应带 reverted、不再 revertable: %+v", it)
				}
				if it.Field == model.ParamFieldTitle && (it.Reverted || !it.Revertable) {
					t.Fatalf("标题修改还没撤销、应可撤销: %+v", it)
				}
			case "params_reverted":
				reverted++
				if it.By != model.BranchParamByHuman || it.RevertOf != residentEv.ID || it.Revertable {
					t.Fatalf("撤销行不对: %+v", it)
				}
			}
		}
		if changed != 2 || reverted != 1 {
			t.Fatalf("%s 修改 %d 撤销 %d", wf, changed, reverted)
		}
	}
	if got := rows("wf-other"); len(got) != 0 {
		t.Fatalf("无关会话不该看到别人分支的参数事件: %+v", got)
	}

	// 摘要里不含常驻规则全文（只有版本与字数）。
	for _, it := range rows("wf-main") {
		if it.Field == model.ParamFieldResident && strings.Contains(it.Text, "规则甲") {
			t.Fatalf("事件行不该带常驻规则全文: %q", it.Text)
		}
	}
}

// 参数写入 / 撤销之后要广播的现有事件：按字段映射、常驻规则与唤醒词合并成一条、顺序固定、标题带会话对象。
func TestParamEffects(t *testing.T) {
	st := paramsFixture(t, model.ExposeParamsAll)
	ctx := context.Background()
	now := time.Now()

	res, err := st.ApplySessionParams(ctx, "wf-main", "wf-b1", SessionParamsPatch{
		Resident: strp("规则一"), Phase: strp("dev"), WakeResume: strp("续聊"), WakeNew: strp("新开 %workflow"), Title: strp("新标题"),
	}, now)
	if err != nil || len(res.Applied) != 5 {
		t.Fatalf("主会话一次写五项: %+v %v", res, err)
	}

	type brief struct{ event, key, val string }
	describe := func(effects []ParamEffect) []brief {
		var out []brief
		for _, ef := range effects {
			b := brief{event: ef.Event}
			switch p := ef.Payload.(type) {
			case map[string]string:
				b.key, b.val = p["workflow_id"], p["phase"]
			case *model.FeedbackSession:
				b.key, b.val = p.WorkflowID, p.Title
			default:
				t.Fatalf("事件 %s 的载荷类型不对: %T", ef.Event, ef.Payload)
			}
			out = append(out, b)
		}
		return out
	}

	got := describe(st.ParamEffects(ctx, "wf-b1", res.Applied))
	want := []brief{
		{"workflow_prompts_updated", "wf-b1", ""}, // 常驻规则 + 两个唤醒词只发一条
		{"phase_changed", "wf-b1", "dev"},
		{"session_updated", "wf-b1", "新标题"},
	}
	if len(got) != len(want) {
		t.Fatalf("事件条数不对: %+v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("第 %d 条 = %+v，期望 %+v", i, got[i], want[i])
		}
	}

	t.Run("只有唤醒词也发提示词事件；异步发 workflow_mode_updated", func(t *testing.T) {
		evs := []model.BranchParamEvent{
			{Field: model.ParamFieldAsync, NewValue: "off"},
			{Field: model.ParamFieldWakeNew, NewValue: "x"},
		}
		got := describe(st.ParamEffects(ctx, "wf-b1", evs))
		if len(got) != 2 || got[0].event != "workflow_prompts_updated" || got[1].event != "workflow_mode_updated" {
			t.Fatalf("按固定字段顺序（提示词在前、异步在后）: %+v", got)
		}
	})
	t.Run("没有事件 / 不认识的字段什么都不发", func(t *testing.T) {
		if out := st.ParamEffects(ctx, "wf-b1", nil); len(out) != 0 {
			t.Fatalf("空输入应无事件: %+v", out)
		}
		if out := st.ParamEffects(ctx, "wf-b1", []model.BranchParamEvent{{Field: "credentials", NewValue: "x"}}); len(out) != 0 {
			t.Fatalf("不认识的字段不发事件: %+v", out)
		}
	})
	t.Run("读不到会话时跳过标题，其它事件照发", func(t *testing.T) {
		evs := []model.BranchParamEvent{{Field: model.ParamFieldTitle, NewValue: "x"}, {Field: model.ParamFieldPhase, NewValue: "plan"}}
		got := describe(st.ParamEffects(ctx, "wf-nowhere", evs))
		if len(got) != 1 || got[0].event != "phase_changed" || got[0].val != "plan" {
			t.Fatalf("没有会话行时只剩阶段事件: %+v", got)
		}
	})
	t.Run("撤销产生的事件同样适用（NewValue 是改回去的旧值）", func(t *testing.T) {
		rev, err := st.RevertBranchParamEvent(ctx, "wf-b1", res.Applied[1].ID, "alice", now) // phase
		if err != nil {
			t.Fatal(err)
		}
		got := describe(st.ParamEffects(ctx, "wf-b1", []model.BranchParamEvent{rev.Event}))
		if len(got) != 1 || got[0].event != "phase_changed" || got[0].val != rev.Event.NewValue || got[0].val == "dev" {
			t.Fatalf("撤销阶段应广播改回去的阶段（不是 dev）: %+v / %+v", got, rev.Event)
		}
	})
}
