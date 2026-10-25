package store

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/teacat99/RelayMesh/v2/internal/model"
)

// 决策路由（#236 P2）的存储层测试：路由条件、主会话代答 / 转交、越权、先到先得、超时升级、关系变化善后、偏好。

// routedPair 建一对主会话 ← 分支（默认协作预设：开放了定夺）。
func routedPair(t *testing.T) (*Store, string, string) {
	t.Helper()
	st := newBranchStore(t)
	mkWF(t, st, "wf-main", "主会话")
	mkWF(t, st, "wf-b1", "分支一")
	mustLink(t, st, "wf-main", "wf-b1", model.BranchCreatedByHuman)
	return st, "wf-main", "wf-b1"
}

func askWith(t *testing.T, st *Store, wf, title, risk string) *model.ConfirmItem {
	t.Helper()
	item, created, err := st.CreateConfirmItem(context.Background(), ConfirmCreateInput{
		WorkflowID: wf, Title: title, Options: abOptions(), Risk: risk, AllowNote: true,
	})
	if err != nil || !created {
		t.Fatalf("CreateConfirmItem(%s,%s): created=%v err=%v", wf, title, created, err)
	}
	return item
}

func inboxOf(t *testing.T, st *Store, wf, kind string) []model.QueuedFeedback {
	t.Helper()
	var rows []model.QueuedFeedback
	if err := st.DB().Where("workflow_id = ? AND kind = ?", wf, kind).Order("id ASC").Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	return rows
}

func reloadAsk(t *testing.T, st *Store, wf, ref string) *model.ConfirmItem {
	t.Helper()
	it, err := st.GetConfirmItemByRef(context.Background(), wf, ref)
	if err != nil {
		t.Fatal(err)
	}
	return it
}

func auditCount(t *testing.T, st *Store, action string) int {
	t.Helper()
	var n int64
	if err := st.DB().Model(&model.AuditEvent{}).Where("action = ?", action).Count(&n).Error; err != nil {
		t.Fatal(err)
	}
	return int(n)
}

func TestRouting_CreateRoutesToMainAndWritesMainInboxRow(t *testing.T) {
	st, main, br := routedPair(t)
	ctx := context.Background()
	item := askWith(t, st, br, "要不要先做迁移？", "L1")
	if item.Addressee != model.ConfirmAddresseeMain(main) {
		t.Fatalf("addressee = %q，应路由给主会话", item.Addressee)
	}
	if !item.RoutedToMain() {
		t.Fatal("RoutedToMain 应为真")
	}
	rows := inboxOf(t, st, main, model.InboxKindBranch)
	if len(rows) != 1 {
		t.Fatalf("主会话收件箱应有 1 行请示，得到 %d", len(rows))
	}
	text := rows[0].ResponseText
	for _, want := range []string{"分支「分支一」（wf-b1）", "Q1", "要不要先做迁移？", "风险 L1", "a. 方案 A", "b. 方案 B", "branch(action:\"answer\"", "branch(action:\"escalate\"", "30 分钟"} {
		if !strings.Contains(text, want) {
			t.Fatalf("请示行缺 %q:\n%s", want, text)
		}
	}
	actor, _ := model.ParseMsgActor(rows[0].Actor)
	if actor.Kind != model.ActorKindBranch || actor.Workflow != br || rows[0].SenderRef != br {
		t.Fatalf("请示行的 actor / sender 不对: %+v %q", actor, rows[0].SenderRef)
	}
	asks, err := st.PendingBranchAsks(ctx, main)
	if err != nil || len(asks) != 1 || asks[0].Ref != "Q1" || asks[0].Branch != br || asks[0].BranchTitle != "分支一" {
		t.Fatalf("PendingBranchAsks: %+v %v", asks, err)
	}
	rel, _ := st.BranchOf(ctx, br)
	briefs := st.BranchBriefs(ctx, []model.WorkflowBranch{*rel})
	if len(briefs) != 1 || briefs[0].PendingAsks != 1 {
		t.Fatalf("BranchBrief.PendingAsks 应为 1: %+v", briefs)
	}
	// 视图带上路由字段
	v := model.NewConfirmItemView(reloadAsk(t, st, br, "Q1"))
	if v.Addressee != model.ConfirmAddresseeMain(main) || v.Escalation != nil || v.EscalatedAt != nil {
		t.Fatalf("视图路由字段不对: %+v", v)
	}
}

func TestRouting_NotRoutedCases(t *testing.T) {
	ctx := context.Background()
	t.Run("普通会话不路由，主会话收件箱也不会多行", func(t *testing.T) {
		st := newBranchStore(t)
		mkWF(t, st, "wf-plain", "普通")
		it := askWith(t, st, "wf-plain", "普通请示", "L1")
		if it.Addressee != "" || it.RoutedToMain() {
			t.Fatalf("普通会话的请示不应带 addressee: %q", it.Addressee)
		}
	})
	t.Run("分支没有开放定夺（仅可见预设）", func(t *testing.T) {
		st, main, br := routedPair(t)
		if _, err := st.UpdateBranchSettings(ctx, br, UpdateBranchInput{Preset: model.ExposePresetVisible}); err != nil {
			t.Fatal(err)
		}
		it := askWith(t, st, br, "请示", "L1")
		if it.Addressee != "" || len(inboxOf(t, st, main, model.InboxKindBranch)) != 0 {
			t.Fatalf("decide 关时不应路由: %q", it.Addressee)
		}
	})
	t.Run("已结束的分支不路由", func(t *testing.T) {
		st, main, br := routedPair(t)
		if _, err := st.CloseBranch(ctx, br, model.BranchCreatedByHuman); err != nil {
			t.Fatal(err)
		}
		before := len(inboxOf(t, st, main, model.InboxKindBranch))
		it := askWith(t, st, br, "请示", "L1")
		if it.Addressee != "" || len(inboxOf(t, st, main, model.InboxKindBranch)) != before {
			t.Fatalf("已结束的分支不应路由: %q", it.Addressee)
		}
	})
	t.Run("文档决策卡（created_by=doc）不路由", func(t *testing.T) {
		st, main, br := routedPair(t)
		it, _, err := st.CreateConfirmItem(ctx, ConfirmCreateInput{
			WorkflowID: br, Kind: model.ConfirmKindDecision, Title: "文档里的决策", Options: abOptions(), CreatedBy: model.ConfirmCreatedByDoc,
		})
		if err != nil {
			t.Fatal(err)
		}
		if it.Addressee != "" || len(inboxOf(t, st, main, model.InboxKindBranch)) != 0 {
			t.Fatalf("文档决策卡不应路由: %q", it.Addressee)
		}
	})
	t.Run("去重命中已有请示：不重复路由、不重复写主会话收件箱", func(t *testing.T) {
		st, main, br := routedPair(t)
		a := askWith(t, st, br, "同一个问题", "L1")
		b, created, err := st.CreateConfirmItem(ctx, ConfirmCreateInput{WorkflowID: br, Title: "同一个问题", Options: abOptions(), Risk: "L1", AllowNote: true})
		if err != nil || created || b.ID != a.ID {
			t.Fatalf("应返回已有的一条: created=%v err=%v", created, err)
		}
		if n := len(inboxOf(t, st, main, model.InboxKindBranch)); n != 1 {
			t.Fatalf("主会话收件箱应仍只有 1 行，得到 %d", n)
		}
	})
	t.Run("关系建立之前就在等的请示保持直达用户", func(t *testing.T) {
		st := newBranchStore(t)
		mkWF(t, st, "wf-main", "主会话")
		mkWF(t, st, "wf-b1", "分支一")
		early := askWith(t, st, "wf-b1", "建关系之前的请示", "L1")
		mustLink(t, st, "wf-main", "wf-b1", model.BranchCreatedByHuman)
		if got := reloadAsk(t, st, "wf-b1", "Q1"); got.Addressee != "" || early.Addressee != "" {
			t.Fatalf("既有请示不应被追溯路由: %q", got.Addressee)
		}
	})
}

func TestBranchAnswer_HappyPathAttributionAndDelivery(t *testing.T) {
	st, main, br := routedPair(t)
	ctx := context.Background()
	askWith(t, st, br, "选哪个方案？", "L1")

	it, err := st.BranchAnswer(ctx, BranchAnswerInput{Main: main, Branch: br, Ref: "Q1", OptionIDs: []string{"b"}, Note: "主会话的判断理由"})
	if err != nil {
		t.Fatal(err)
	}
	if it.Status != model.ConfirmStatusAnswered {
		t.Fatalf("状态应为 answered: %s", it.Status)
	}
	v := model.NewConfirmItemView(reloadAsk(t, st, br, "Q1"))
	if v.Answer == nil || v.Answer.By != model.ConfirmByAgent(main) || len(v.Answer.OptionIDs) != 1 || v.Answer.OptionIDs[0] != "b" || v.Answer.Note != "主会话的判断理由" {
		t.Fatalf("答复记录不对: %+v", v.Answer)
	}
	if v.Addressee != model.ConfirmAddresseeMain(main) {
		t.Fatalf("作答后 addressee 应保留（标注曾路由）: %q", v.Addressee)
	}
	rows := inboxOf(t, st, br, model.InboxKindAnswer)
	if len(rows) != 1 {
		t.Fatalf("分支收件箱应有 1 行答复，得到 %d", len(rows))
	}
	if !strings.Contains(rows[0].ResponseText, "主会话代为定夺，不是用户本人") || !strings.Contains(rows[0].ResponseText, "Q1") || !strings.Contains(rows[0].ResponseText, "方案 B") {
		t.Fatalf("答复行文案不对: %s", rows[0].ResponseText)
	}
	actor, _ := model.ParseMsgActor(rows[0].Actor)
	if actor.Kind != model.ActorKindMain || actor.Workflow != main {
		t.Fatalf("答复行应标明 actor=main: %+v", actor)
	}
	if asks, _ := st.PendingBranchAsks(ctx, main); len(asks) != 0 {
		t.Fatalf("作答后不应再有待定夺请示: %+v", asks)
	}
	if n := auditCount(t, st, AuditBranchAnswer); n != 1 {
		t.Fatalf("应有 1 条 branch.answer 审计，得到 %d", n)
	}
}

func TestBranchAnswer_PlainUserAnswerStaysHumanAndUnmarked(t *testing.T) {
	st, _, br := routedPair(t)
	ctx := context.Background()
	askWith(t, st, br, "用户自己答", "L1")
	if _, err := st.AnswerConfirmItem(ctx, br, "Q1", ConfirmAnswerInput{OptionIDs: []string{"a"}}); err != nil {
		t.Fatal(err)
	}
	v := model.NewConfirmItemView(reloadAsk(t, st, br, "Q1"))
	if v.Answer == nil || v.Answer.By != "human" {
		t.Fatalf("用户接管作答应记 human: %+v", v.Answer)
	}
	rows := inboxOf(t, st, br, model.InboxKindAnswer)
	if len(rows) != 1 || rows[0].Actor != "" || strings.Contains(rows[0].ResponseText, "主会话代为定夺") {
		t.Fatalf("用户作答的答复行不应带主会话标记: %+v", rows)
	}
}

func TestBranchAnswer_Rejections(t *testing.T) {
	ctx := context.Background()
	st, main, br := routedPair(t)
	mkWF(t, st, "wf-other", "无关会话")
	mkWF(t, st, "wf-b2", "分支二")
	mustLink(t, st, "wf-main", "wf-b2", model.BranchCreatedByHuman)
	askWith(t, st, br, "请示一", "L1")

	in := func(mutate func(*BranchAnswerInput)) error {
		x := BranchAnswerInput{Main: main, Branch: br, Ref: "Q1", OptionIDs: []string{"a"}}
		if mutate != nil {
			mutate(&x)
		}
		_, err := st.BranchAnswer(ctx, x)
		return err
	}
	wantBranchErr(t, in(func(x *BranchAnswerInput) { x.Main = "wf-other" }), ErrForbidden, "不是你的活动分支")
	wantBranchErr(t, in(func(x *BranchAnswerInput) { x.Main = br }), ErrForbidden, "不是你的活动分支")
	wantBranchErr(t, in(func(x *BranchAnswerInput) { x.Branch = "wf-b2" }), ErrNotFound, "")
	wantBranchErr(t, in(func(x *BranchAnswerInput) { x.Branch = "" }), ErrInvalidInput, "branch 必填")
	wantBranchErr(t, in(func(x *BranchAnswerInput) { x.Ref = "" }), ErrInvalidInput, "ref 必填")
	wantBranchErr(t, in(func(x *BranchAnswerInput) { x.OptionIDs = []string{"zzz"} }), ErrInvalidInput, "不属于")
	wantBranchErr(t, in(func(x *BranchAnswerInput) { x.OptionIDs = nil }), ErrInvalidInput, "必须选择")
	if it := reloadAsk(t, st, br, "Q1"); it.Status != model.ConfirmStatusPending {
		t.Fatalf("被拒绝的尝试不应改动请示: %s", it.Status)
	}

	// 两个分支都有 Q1 时不会串：主会话答 b2 的 Q1 只影响 b2
	askWith(t, st, "wf-b2", "分支二的请示", "L1")
	if err := in(func(x *BranchAnswerInput) { x.Branch = "wf-b2" }); err != nil {
		t.Fatal(err)
	}
	if reloadAsk(t, st, br, "Q1").Status != model.ConfirmStatusPending || reloadAsk(t, st, "wf-b2", "Q1").Status != model.ConfirmStatusAnswered {
		t.Fatal("按「分支 + ref」取：b2 的 Q1 已答、b1 的 Q1 仍待处理")
	}

	// 重复作答：第二次冲突
	if err := in(nil); err != nil {
		t.Fatal(err)
	}
	wantBranchErr(t, in(nil), ErrConflict, "已处理")
}

func TestBranchAnswer_NotRoutedAskIsForbidden(t *testing.T) {
	st := newBranchStore(t)
	ctx := context.Background()
	mkWF(t, st, "wf-main", "主会话")
	mkWF(t, st, "wf-b1", "分支一")
	askWith(t, st, "wf-b1", "建关系之前", "L1")
	mustLink(t, st, "wf-main", "wf-b1", model.BranchCreatedByHuman)
	_, err := st.BranchAnswer(ctx, BranchAnswerInput{Main: "wf-main", Branch: "wf-b1", Ref: "Q1", OptionIDs: []string{"a"}})
	wantBranchErr(t, err, ErrForbidden, "直接发给了用户")
}

func TestBranchAnswer_DecideTurnedOffBlocksAndEscalatesPending(t *testing.T) {
	st, main, br := routedPair(t)
	ctx := context.Background()
	askWith(t, st, br, "关掉定夺之前发的", "L1")
	_, esc, err := st.UpdateBranchSettingsFull(ctx, br, UpdateBranchInput{Preset: model.ExposePresetVisible})
	if err != nil {
		t.Fatal(err)
	}
	if len(esc) != 1 || esc[0].EscalatedAt == nil || esc[0].Addressee != "" {
		t.Fatalf("关掉定夺应立即转交已路由未答的请示: %+v", esc)
	}
	v := model.NewConfirmItemView(reloadAsk(t, st, br, "Q1"))
	if v.Escalation == nil || v.Escalation.By != model.ConfirmEscalatedBySystem || !strings.Contains(v.Escalation.Note, "定夺") {
		t.Fatalf("转交说明不对: %+v", v.Escalation)
	}
	_, err = st.BranchAnswer(ctx, BranchAnswerInput{Main: main, Branch: br, Ref: "Q1", OptionIDs: []string{"a"}})
	wantBranchErr(t, err, ErrForbidden, "没有向主会话开放")

	// 其它设置改动（decide 保持开）不转交
	st2, _, br2 := routedPair(t)
	askWith(t, st2, br2, "保持开放", "L1")
	if _, esc2, err := st2.UpdateBranchSettingsFull(ctx, br2, UpdateBranchInput{Preset: model.ExposePresetManaged}); err != nil || len(esc2) != 0 {
		t.Fatalf("decide 仍开不应转交: %v %v", esc2, err)
	}
	if got := reloadAsk(t, st2, br2, "Q1"); !got.RoutedToMain() {
		t.Fatal("仍应在等主会话")
	}
}

func TestBranchAnswer_L2GuardSwitch(t *testing.T) {
	st, main, br := routedPair(t)
	ctx := context.Background()
	if _, err := st.UpdateBranchPrefs(ctx, func(cur model.BranchPrefs) (model.BranchPrefs, error) { cur.L2Guard = true; return cur, nil }); err != nil {
		t.Fatal(err)
	}
	askWith(t, st, br, "不可逆的事", "L2")
	askWith(t, st, br, "可回滚的事", "L1")
	if rows := inboxOf(t, st, main, model.InboxKindBranch); len(rows) != 2 || !strings.Contains(rows[0].ResponseText, "兜底开关") || strings.Contains(rows[0].ResponseText, "branch(action:\"answer\"") {
		t.Fatalf("兜底开着时 L2 请示行应说明只能转交: %+v", rows)
	}
	_, err := st.BranchAnswer(ctx, BranchAnswerInput{Main: main, Branch: br, Ref: "Q1", OptionIDs: []string{"a"}})
	wantBranchErr(t, err, ErrForbidden, "兜底开关")
	if _, err := st.BranchAnswer(ctx, BranchAnswerInput{Main: main, Branch: br, Ref: "Q2", OptionIDs: []string{"a"}}); err != nil {
		t.Fatalf("L1 不受兜底限制: %v", err)
	}
	// L2 仍可转交
	if _, err := st.BranchEscalate(ctx, BranchEscalateInput{Main: main, Branch: br, Ref: "Q1", Note: "不可逆，需要你本人拍板"}); err != nil {
		t.Fatalf("L2 应能转交: %v", err)
	}
	// 关掉开关后默认放行（不分级是方案里的默认）
	if _, err := st.UpdateBranchPrefs(ctx, func(cur model.BranchPrefs) (model.BranchPrefs, error) { cur.L2Guard = false; return cur, nil }); err != nil {
		t.Fatal(err)
	}
	askWith(t, st, br, "另一件不可逆的事", "L2")
	if _, err := st.BranchAnswer(ctx, BranchAnswerInput{Main: main, Branch: br, Ref: "Q3", OptionIDs: []string{"a"}}); err != nil {
		t.Fatalf("兜底关闭时 L2 可由主会话答复: %v", err)
	}
}

func TestBranchEscalate_HappyPathAndAfterwards(t *testing.T) {
	st, main, br := routedPair(t)
	ctx := context.Background()
	askWith(t, st, br, "定夺不了的事", "L1")

	_, err := st.BranchEscalate(ctx, BranchEscalateInput{Main: main, Branch: br, Ref: "Q1", Note: "  "})
	wantBranchErr(t, err, ErrInvalidInput, "note 必填")
	_, err = st.BranchEscalate(ctx, BranchEscalateInput{Main: main, Branch: br, Ref: "Q1", Note: strings.Repeat("长", model.ConfirmEscalateNoteMax+1)})
	wantBranchErr(t, err, ErrInvalidInput, "太长")
	_, err = st.BranchEscalate(ctx, BranchEscalateInput{Main: "wf-other", Branch: br, Ref: "Q1", Note: "x"})
	wantBranchErr(t, err, ErrForbidden, "不是你的活动分支")

	it, err := st.BranchEscalate(ctx, BranchEscalateInput{Main: main, Branch: br, Ref: "Q1", Note: "涉及预算，需要你拍板"})
	if err != nil {
		t.Fatal(err)
	}
	if it.Addressee != "" || it.EscalatedAt == nil || it.Status != model.ConfirmStatusPending {
		t.Fatalf("转交后应清 addressee、记 escalated_at、仍待处理: %+v", it)
	}
	v := model.NewConfirmItemView(reloadAsk(t, st, br, "Q1"))
	if v.Escalation == nil || v.Escalation.By != model.ConfirmByAgent(main) || v.Escalation.Note != "涉及预算，需要你拍板" || v.EscalatedAt == nil {
		t.Fatalf("视图的转交信息不对: %+v", v)
	}
	if v.Addressee != "" {
		t.Fatal("转交后不应再算在等主会话")
	}
	if asks, _ := st.PendingBranchAsks(ctx, main); len(asks) != 0 {
		t.Fatalf("转交后主会话不应再有待定夺请示: %+v", asks)
	}
	// 主会话再答 / 再转交都拒绝；用户仍能直接答
	_, err = st.BranchAnswer(ctx, BranchAnswerInput{Main: main, Branch: br, Ref: "Q1", OptionIDs: []string{"a"}})
	wantBranchErr(t, err, ErrConflict, "已转交给用户")
	_, err = st.BranchEscalate(ctx, BranchEscalateInput{Main: main, Branch: br, Ref: "Q1", Note: "再转一次"})
	wantBranchErr(t, err, ErrConflict, "已转交给用户")
	if _, err := st.AnswerConfirmItem(ctx, br, "Q1", ConfirmAnswerInput{OptionIDs: []string{"a"}}); err != nil {
		t.Fatalf("用户应能直接答复转交来的请示: %v", err)
	}
	if n := auditCount(t, st, AuditBranchEscalate); n != 1 {
		t.Fatalf("应有 1 条 branch.escalate 审计，得到 %d", n)
	}
}

func TestBranchRouting_FirstComeFirstServed(t *testing.T) {
	ctx := context.Background()
	t.Run("用户先答：主会话随后得到冲突", func(t *testing.T) {
		st, main, br := routedPair(t)
		askWith(t, st, br, "抢答", "L1")
		if _, err := st.AnswerConfirmItem(ctx, br, "Q1", ConfirmAnswerInput{OptionIDs: []string{"a"}}); err != nil {
			t.Fatal(err)
		}
		_, err := st.BranchAnswer(ctx, BranchAnswerInput{Main: main, Branch: br, Ref: "Q1", OptionIDs: []string{"b"}})
		wantBranchErr(t, err, ErrConflict, "已处理")
		if v := model.NewConfirmItemView(reloadAsk(t, st, br, "Q1")); v.Answer.OptionIDs[0] != "a" || v.Answer.By != "human" {
			t.Fatalf("先到的用户答复应保持: %+v", v.Answer)
		}
	})
	t.Run("主会话先答：用户随后得到冲突", func(t *testing.T) {
		st, main, br := routedPair(t)
		askWith(t, st, br, "抢答", "L1")
		if _, err := st.BranchAnswer(ctx, BranchAnswerInput{Main: main, Branch: br, Ref: "Q1", OptionIDs: []string{"b"}}); err != nil {
			t.Fatal(err)
		}
		_, err := st.AnswerConfirmItem(ctx, br, "Q1", ConfirmAnswerInput{OptionIDs: []string{"a"}})
		wantBranchErr(t, err, ErrConflict, "已处理")
	})
}

func TestEscalateOverdueBranchAsks(t *testing.T) {
	st, main, br := routedPair(t)
	ctx := context.Background()
	mkWF(t, st, "wf-plain", "普通")
	askWith(t, st, br, "超时的", "L1")      // Q1：会超时
	askWith(t, st, br, "超时前被用户答了", "L1") // Q2
	askWith(t, st, "wf-plain", "普通请示", "L1")
	if _, err := st.AnswerConfirmItem(ctx, br, "Q2", ConfirmAnswerInput{OptionIDs: []string{"a"}}); err != nil {
		t.Fatal(err)
	}

	now := time.Now()
	// 还没到时限：什么都不动
	got, err := st.EscalateOverdueBranchAsks(ctx, now.Add(10*time.Minute), 30*time.Minute, "x", 50)
	if err != nil || len(got) != 0 {
		t.Fatalf("未超时不应升级: %v %v", got, err)
	}
	// 超时：只升级仍在等主会话的 Q1
	got, err = st.EscalateOverdueBranchAsks(ctx, now.Add(31*time.Minute), 30*time.Minute, "主会话 30 分钟没有答复，自动转交给你", 50)
	if err != nil || len(got) != 1 || got[0].Seq != 1 || got[0].WorkflowID != br {
		t.Fatalf("应只升级 Q1: %v %v", got, err)
	}
	v := model.NewConfirmItemView(reloadAsk(t, st, br, "Q1"))
	if v.Escalation == nil || v.Escalation.By != model.ConfirmEscalatedByTimeout || !strings.Contains(v.Escalation.Note, "自动转交") || v.Addressee != "" || v.EscalatedAt == nil {
		t.Fatalf("升级信息不对: %+v", v)
	}
	if plain := reloadAsk(t, st, "wf-plain", "Q1"); plain.EscalatedAt != nil {
		t.Fatal("普通会话的请示不应被动")
	}
	// 幂等：再扫一遍没有新的
	again, err := st.EscalateOverdueBranchAsks(ctx, now.Add(40*time.Minute), 30*time.Minute, "x", 50)
	if err != nil || len(again) != 0 {
		t.Fatalf("重复扫描应幂等: %v %v", again, err)
	}
	// 审计：timeout，scope 是主会话
	var ev model.AuditEvent
	if err := st.DB().Where("action = ?", AuditBranchEscalate).First(&ev).Error; err != nil {
		t.Fatal(err)
	}
	if ev.ScopeRef != main || ev.Target != br || ev.ActorRef != "branch-escalator" {
		t.Fatalf("审计主体 / 范围不对: %+v", ev)
	}
	// 主会话之后再答 → 已转交
	_, err = st.BranchAnswer(ctx, BranchAnswerInput{Main: main, Branch: br, Ref: "Q1", OptionIDs: []string{"a"}})
	wantBranchErr(t, err, ErrConflict, "已转交给用户")
}

func TestEscalateOverdueBranchAsks_LimitAndOrder(t *testing.T) {
	st, _, br := routedPair(t)
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		askWith(t, st, br, "请示"+string(rune('A'+i)), "L1")
	}
	got, err := st.EscalateOverdueBranchAsks(ctx, time.Now().Add(time.Hour), time.Minute, "x", 2)
	if err != nil || len(got) != 2 || got[0].Seq != 1 || got[1].Seq != 2 {
		t.Fatalf("limit=2 应先升级最早的两条: %v %v", got, err)
	}
	got, _ = st.EscalateOverdueBranchAsks(ctx, time.Now().Add(time.Hour), time.Minute, "x", 2)
	if len(got) != 1 || got[0].Seq != 3 {
		t.Fatalf("下一遍应升级剩下的一条: %v", got)
	}
}

func TestBranchClose_EscalatesRoutedAsks(t *testing.T) {
	st, main, br := routedPair(t)
	ctx := context.Background()
	askWith(t, st, br, "结束前的请示", "L1")
	res, err := st.CloseBranch(ctx, br, model.BranchCreatedByHuman)
	if err != nil || !res.Changed || len(res.Escalated) != 1 {
		t.Fatalf("结束分支应转交已路由未答的请示: %+v %v", res, err)
	}
	v := model.NewConfirmItemView(reloadAsk(t, st, br, "Q1"))
	if v.Escalation == nil || v.Escalation.By != model.ConfirmEscalatedBySystem || !strings.Contains(v.Escalation.Note, "分支已结束") {
		t.Fatalf("转交说明不对: %+v", v.Escalation)
	}
	if asks, _ := st.PendingBranchAsks(ctx, main); len(asks) != 0 {
		t.Fatalf("结束后主会话不应再有待定夺请示: %+v", asks)
	}
	// 幂等：再结束一次不重复转交
	res2, err := st.CloseBranch(ctx, br, model.BranchCreatedByHuman)
	if err != nil || res2.Changed || len(res2.Escalated) != 0 {
		t.Fatalf("重复结束应无动作: %+v %v", res2, err)
	}
}

func TestBranchUnlink_UnroutesPendingAsks(t *testing.T) {
	st, _, br := routedPair(t)
	ctx := context.Background()
	askWith(t, st, br, "解除前的请示", "L1")
	if _, err := st.UnlinkBranch(ctx, br, true, model.BranchCreatedByMigrate); err != nil {
		t.Fatal(err)
	}
	if !reloadAsk(t, st, br, "Q1").RoutedToMain() {
		t.Fatal("预演不应改动请示")
	}
	if _, err := st.UnlinkBranch(ctx, br, false, model.BranchCreatedByMigrate); err != nil {
		t.Fatal(err)
	}
	got := reloadAsk(t, st, br, "Q1")
	if got.RoutedToMain() || got.EscalatedAt == nil {
		t.Fatalf("解除关系后请示应转交给用户: %+v", got)
	}
}

func TestBranchPrefs_DefaultsRoundTripAndValidation(t *testing.T) {
	st := newBranchStore(t)
	ctx := context.Background()
	p, err := st.GetBranchPrefs(ctx)
	if err != nil || p.L2Guard || p.EscalateMinutes != 0 || p.EscalateAfterMinutes() != model.BranchEscalateDefaultMinutes {
		t.Fatalf("默认值不对: %+v %v", p, err)
	}
	next, err := st.UpdateBranchPrefs(ctx, func(cur model.BranchPrefs) (model.BranchPrefs, error) {
		cur.L2Guard, cur.EscalateMinutes = true, 45
		return cur, nil
	})
	if err != nil || !next.L2Guard || next.EscalateAfterMinutes() != 45 {
		t.Fatalf("更新失败: %+v %v", next, err)
	}
	if got, _ := st.GetBranchPrefs(ctx); got != next {
		t.Fatalf("读回的值不一致: %+v vs %+v", got, next)
	}
	for _, bad := range []int{1, 4, 1441, -3} {
		_, err := st.UpdateBranchPrefs(ctx, func(cur model.BranchPrefs) (model.BranchPrefs, error) { cur.EscalateMinutes = bad; return cur, nil })
		if err == nil || !strings.Contains(err.Error(), "escalate_minutes") {
			t.Fatalf("escalate_minutes=%d 应被拒绝: %v", bad, err)
		}
	}
	if got, _ := st.GetBranchPrefs(ctx); got != next {
		t.Fatalf("校验失败后库里应保持原样: %+v", got)
	}
	// 0 = 恢复默认
	if reset, err := st.UpdateBranchPrefs(ctx, func(cur model.BranchPrefs) (model.BranchPrefs, error) { cur.EscalateMinutes = 0; return cur, nil }); err != nil || reset.EscalateAfterMinutes() != model.BranchEscalateDefaultMinutes {
		t.Fatalf("0 应恢复默认: %+v %v", reset, err)
	}
	// 损坏的值按默认处理，不报错
	if err := st.DB().Save(&model.SystemSetting{Key: BranchPrefsKey, Value: "{坏的", UpdatedAt: time.Now()}).Error; err != nil {
		t.Fatal(err)
	}
	if got, err := st.GetBranchPrefs(ctx); err != nil || got.L2Guard || got.EscalateAfterMinutes() != model.BranchEscalateDefaultMinutes {
		t.Fatalf("损坏的值应按默认: %+v %v", got, err)
	}
}

func TestBranchExposeSummaryMentionsDecide(t *testing.T) {
	collab, _ := model.BranchExposePreset(model.ExposePresetCollab)
	visible, _ := model.BranchExposePreset(model.ExposePresetVisible)
	if s := collab.Summary(); !strings.Contains(s, "可定夺请示") {
		t.Fatalf("协作预设应写「可定夺请示」: %s", s)
	}
	if s := visible.Summary(); !strings.Contains(s, "不可定夺请示") {
		t.Fatalf("仅可见预设应写「不可定夺请示」: %s", s)
	}
}
