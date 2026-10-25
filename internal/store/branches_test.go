package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"gorm.io/gorm"

	"github.com/teacat99/RelayMesh/v2/internal/model"
)

func newBranchStore(t *testing.T) *Store {
	t.Helper()
	st, err := New(":memory:")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

// mkWF 建一个只有一行已完成会话的工作流（库里没有 workflows 表，工作流就是会话行的聚合键）。
func mkWF(t *testing.T, st *Store, wf, title string) {
	t.Helper()
	now := time.Now()
	row := model.FeedbackSession{
		ID: "s-" + wf, WorkflowID: wf, Title: title, Summary: "汇报：" + title, Status: "completed",
		UserMessages: model.StringArray{}, Images: model.SessionImages{}, CreatedAt: now, UpdatedAt: now,
	}
	if err := st.DB().Create(&row).Error; err != nil {
		t.Fatalf("create workflow %s: %v", wf, err)
	}
}

func mustLink(t *testing.T, st *Store, parent, branch, by string) *LinkBranchResult {
	t.Helper()
	res, err := st.LinkBranch(context.Background(), LinkBranchInput{ParentWorkflowID: parent, BranchWorkflowID: branch, CreatedBy: by})
	if err != nil {
		t.Fatalf("LinkBranch(%s <- %s): %v", parent, branch, err)
	}
	return res
}

func wantBranchErr(t *testing.T, err error, kind ErrorKind, contains string) {
	t.Helper()
	var ae *AppError
	if !errors.As(err, &ae) {
		t.Fatalf("期望 %s 类错误，得到 %v", kind, err)
	}
	if ae.Kind != kind {
		t.Fatalf("期望 %s，得到 %s（%s）", kind, ae.Kind, ae.Message)
	}
	if contains != "" && !strings.Contains(ae.Message, contains) {
		t.Fatalf("错误信息应含 %q: %s", contains, ae.Message)
	}
}

func countBranchRows(t *testing.T, st *Store) int64 {
	t.Helper()
	var n int64
	if err := st.DB().Model(&model.WorkflowBranch{}).Count(&n).Error; err != nil {
		t.Fatal(err)
	}
	return n
}

func TestBranchLink_BasicDefaultsAndIdempotence(t *testing.T) {
	st := newBranchStore(t)
	mkWF(t, st, "wf-main", "主会话")
	mkWF(t, st, "wf-b1", "分支一")

	res := mustLink(t, st, "wf-main", "wf-b1", model.BranchCreatedByHuman)
	if !res.Created || res.Reopened || res.Noop() {
		t.Fatalf("第一次应是新建: %+v", res)
	}
	if res.Branch.Status != model.BranchStatusActive || res.Branch.ID == 0 {
		t.Fatalf("应写入一条生效的关系: %+v", res.Branch)
	}
	if got := res.Branch.Expose.PresetName(); got != model.ExposePresetCollab {
		t.Fatalf("人建的默认应是协作预设，得到 %q", got)
	}
	if !res.Branch.CopyUserNotes {
		t.Fatalf("抄送默认应开")
	}

	again := mustLink(t, st, "wf-main", "wf-b1", model.BranchCreatedByHuman)
	if !again.Noop() || again.Branch.ID != res.Branch.ID {
		t.Fatalf("重复建立应是空操作且返回同一行: %+v", again)
	}
	if n := countBranchRows(t, st); n != 1 {
		t.Fatalf("幂等：应只有 1 行，得到 %d", n)
	}
}

func TestBranchLink_MigrateDefaultsToVisible(t *testing.T) {
	st := newBranchStore(t)
	mkWF(t, st, "wf-main", "主会话")
	mkWF(t, st, "wf-old", "老会话")
	res := mustLink(t, st, "wf-main", "wf-old", model.BranchCreatedByMigrate)
	if got := res.Branch.Expose.PresetName(); got != model.ExposePresetVisible {
		t.Fatalf("迁移默认应是仅可见，得到 %q", got)
	}
	if res.Branch.Expose.Say || res.Branch.Expose.Decide {
		t.Fatalf("仅可见不应开放发消息 / 定夺: %+v", res.Branch.Expose)
	}
}

func TestBranchLink_CopyUserNotesFalseSurvivesGormDefault(t *testing.T) {
	st := newBranchStore(t)
	mkWF(t, st, "wf-main", "主会话")
	mkWF(t, st, "wf-b1", "分支一")
	off := false
	if _, err := st.LinkBranch(context.Background(), LinkBranchInput{ParentWorkflowID: "wf-main", BranchWorkflowID: "wf-b1", CreatedBy: model.BranchCreatedByHuman, CopyUserNotes: &off}); err != nil {
		t.Fatal(err)
	}
	var row model.WorkflowBranch
	if err := st.DB().Where("branch_workflow_id = ?", "wf-b1").First(&row).Error; err != nil {
		t.Fatal(err)
	}
	if row.CopyUserNotes {
		t.Fatalf("显式关闭抄送应能存进库（GORM 零值遇到 default 标签会被换成默认值）")
	}
}

func TestBranchLink_RejectsInvalid(t *testing.T) {
	st := newBranchStore(t)
	ctx := context.Background()
	mkWF(t, st, "wf-main", "主会话")
	mkWF(t, st, "wf-b1", "分支一")

	_, err := st.LinkBranch(ctx, LinkBranchInput{ParentWorkflowID: "wf-main", BranchWorkflowID: "wf-main", CreatedBy: model.BranchCreatedByHuman})
	wantBranchErr(t, err, ErrInvalidInput, "自己的主会话")
	_, err = st.LinkBranch(ctx, LinkBranchInput{ParentWorkflowID: "", BranchWorkflowID: "wf-b1", CreatedBy: model.BranchCreatedByHuman})
	wantBranchErr(t, err, ErrInvalidInput, "必填")
	_, err = st.LinkBranch(ctx, LinkBranchInput{ParentWorkflowID: "wf-main", BranchWorkflowID: "wf-ghost", CreatedBy: model.BranchCreatedByHuman})
	wantBranchErr(t, err, ErrNotFound, "wf-ghost")
	_, err = st.LinkBranch(ctx, LinkBranchInput{ParentWorkflowID: "wf-ghost", BranchWorkflowID: "wf-b1", CreatedBy: model.BranchCreatedByHuman})
	wantBranchErr(t, err, ErrNotFound, "wf-ghost")
	_, err = st.LinkBranch(ctx, LinkBranchInput{ParentWorkflowID: "wf-main", BranchWorkflowID: "wf-b1", CreatedBy: "robot"})
	wantBranchErr(t, err, ErrInvalidInput, "created_by")
	bad := model.BranchExpose{History: "everything", Params: model.ExposeParamsOff, Tasks: model.ExposeTasksRead}
	_, err = st.LinkBranch(ctx, LinkBranchInput{ParentWorkflowID: "wf-main", BranchWorkflowID: "wf-b1", CreatedBy: model.BranchCreatedByHuman, Expose: &bad})
	wantBranchErr(t, err, ErrInvalidInput, "history")
	if n := countBranchRows(t, st); n != 0 {
		t.Fatalf("被拒绝的请求不应写任何行，得到 %d", n)
	}
}

func TestBranchLink_DepthAndSingleParent(t *testing.T) {
	st := newBranchStore(t)
	ctx := context.Background()
	for _, wf := range []string{"wf-a", "wf-b", "wf-c", "wf-d"} {
		mkWF(t, st, wf, wf)
	}
	mustLink(t, st, "wf-a", "wf-b", model.BranchCreatedByHuman)

	// 分支不能再建分支
	_, err := st.LinkBranch(ctx, LinkBranchInput{ParentWorkflowID: "wf-b", BranchWorkflowID: "wf-c", CreatedBy: model.BranchCreatedByHuman})
	wantBranchErr(t, err, ErrInvalidInput, "只允许一层")
	// 已经有分支的会话不能再做别人的分支
	_, err = st.LinkBranch(ctx, LinkBranchInput{ParentWorkflowID: "wf-c", BranchWorkflowID: "wf-a", CreatedBy: model.BranchCreatedByHuman})
	wantBranchErr(t, err, ErrInvalidInput, "只允许一层")
	// 一个分支只有一个主会话
	_, err = st.LinkBranch(ctx, LinkBranchInput{ParentWorkflowID: "wf-c", BranchWorkflowID: "wf-b", CreatedBy: model.BranchCreatedByHuman})
	wantBranchErr(t, err, ErrConflict, "已经是")
	// 反向（把主会话当作它自己分支的分支）也是深度违规：先撞上「主会话本身就是别人的分支」
	_, err = st.LinkBranch(ctx, LinkBranchInput{ParentWorkflowID: "wf-b", BranchWorkflowID: "wf-a", CreatedBy: model.BranchCreatedByHuman})
	wantBranchErr(t, err, ErrInvalidInput, "只允许一层")
	if n := countBranchRows(t, st); n != 1 {
		t.Fatalf("所有违规都不应留下行，得到 %d", n)
	}
}

func TestBranchLink_ClosedBranchDoesNotCountForDepth(t *testing.T) {
	st := newBranchStore(t)
	ctx := context.Background()
	for _, wf := range []string{"wf-a", "wf-b", "wf-c"} {
		mkWF(t, st, wf, wf)
	}
	mustLink(t, st, "wf-a", "wf-b", model.BranchCreatedByHuman)
	if _, err := st.CloseBranch(ctx, "wf-b", model.BranchCreatedByHuman); err != nil {
		t.Fatal(err)
	}
	// wf-b 已结束：不再是活动分支，所以它可以做别人的主会话；wf-a 也不再是活动的主会话。
	mustLink(t, st, "wf-b", "wf-c", model.BranchCreatedByHuman)
}

func TestBranchLink_ActiveLimitAndHourlyLimit(t *testing.T) {
	st := newBranchStore(t)
	ctx := context.Background()
	mkWF(t, st, "wf-main", "主会话")
	for i := 0; i < MaxActiveBranches+2; i++ {
		mkWF(t, st, fmt.Sprintf("wf-m%d", i), fmt.Sprintf("迁移%d", i))
	}
	// 迁移命令不受每小时限制，但活动分支上限照样适用。
	for i := 0; i < MaxActiveBranches; i++ {
		mustLink(t, st, "wf-main", fmt.Sprintf("wf-m%d", i), model.BranchCreatedByMigrate)
	}
	_, err := st.LinkBranch(ctx, LinkBranchInput{ParentWorkflowID: "wf-main", BranchWorkflowID: "wf-m8", CreatedBy: model.BranchCreatedByMigrate})
	wantBranchErr(t, err, ErrConflict, "活动分支")

	// 每小时限制：另一个主会话，人连续建 5 个可以，第 6 个被拒；迁移建的不计入。
	mkWF(t, st, "wf-main2", "主会话二")
	for i := 0; i < BranchCreatePerHour; i++ {
		wf := fmt.Sprintf("wf-h%d", i)
		mkWF(t, st, wf, wf)
		mustLink(t, st, "wf-main2", wf, model.BranchCreatedByHuman)
	}
	mkWF(t, st, "wf-h-extra", "多出来的")
	_, err = st.LinkBranch(ctx, LinkBranchInput{ParentWorkflowID: "wf-main2", BranchWorkflowID: "wf-h-extra", CreatedBy: model.BranchCreatedByHuman})
	wantBranchErr(t, err, ErrConflict, "一小时内")
	// SkipRateLimit 放行
	if _, err := st.LinkBranch(ctx, LinkBranchInput{ParentWorkflowID: "wf-main2", BranchWorkflowID: "wf-h-extra", CreatedBy: model.BranchCreatedByHuman, SkipRateLimit: true}); err != nil {
		t.Fatalf("SkipRateLimit 应放行: %v", err)
	}
}

func TestBranchLink_HourlyLimitWindowSlides(t *testing.T) {
	st := newBranchStore(t)
	ctx := context.Background()
	mkWF(t, st, "wf-main", "主会话")
	for i := 0; i < BranchCreatePerHour; i++ {
		wf := fmt.Sprintf("wf-h%d", i)
		mkWF(t, st, wf, wf)
		mustLink(t, st, "wf-main", wf, model.BranchCreatedByHuman)
	}
	// 把这 5 条都挪到两小时之前：窗口里就没有了，再建应该放行。
	if err := st.DB().Model(&model.WorkflowBranch{}).Where("1 = 1").Update("created_at", time.Now().Add(-2*time.Hour)).Error; err != nil {
		t.Fatal(err)
	}
	mkWF(t, st, "wf-new", "新的")
	if _, err := st.LinkBranch(ctx, LinkBranchInput{ParentWorkflowID: "wf-main", BranchWorkflowID: "wf-new", CreatedBy: model.BranchCreatedByHuman}); err != nil {
		t.Fatalf("两小时前的新建不应再计入限额: %v", err)
	}
}

func TestBranchLink_ReopenAfterCloseAndConflictWithOtherParent(t *testing.T) {
	st := newBranchStore(t)
	ctx := context.Background()
	for _, wf := range []string{"wf-a", "wf-b", "wf-c"} {
		mkWF(t, st, wf, wf)
	}
	first := mustLink(t, st, "wf-a", "wf-b", model.BranchCreatedByHuman)
	if _, err := st.CloseBranch(ctx, "wf-b", model.BranchCreatedByHuman); err != nil {
		t.Fatal(err)
	}
	// 已结束的关系连别的主会话 = 冲突，连同一个 = 重新打开
	_, err := st.LinkBranch(ctx, LinkBranchInput{ParentWorkflowID: "wf-c", BranchWorkflowID: "wf-b", CreatedBy: model.BranchCreatedByHuman})
	wantBranchErr(t, err, ErrConflict, "branch-unlink")
	re := mustLink(t, st, "wf-a", "wf-b", model.BranchCreatedByHuman)
	if !re.Reopened || re.Created || re.Branch.ID != first.Branch.ID || !re.Branch.Active() || re.Branch.ClosedAt != nil {
		t.Fatalf("应重新打开同一行: %+v", re)
	}
	if n := countBranchRows(t, st); n != 1 {
		t.Fatalf("重新打开不应多出行，得到 %d", n)
	}
}

func TestBranchLink_DryRunWritesNothingAndMatchesApply(t *testing.T) {
	st := newBranchStore(t)
	ctx := context.Background()
	mkWF(t, st, "wf-main", "主会话")
	mkWF(t, st, "wf-b1", "分支一")
	in := LinkBranchInput{ParentWorkflowID: "wf-main", BranchWorkflowID: "wf-b1", CreatedBy: model.BranchCreatedByMigrate}

	plan, err := st.PlanBranchLink(ctx, in)
	if err != nil || plan == nil || !plan.Created {
		t.Fatalf("预演应报告将新建: %+v %v", plan, err)
	}
	if n := countBranchRows(t, st); n != 0 {
		t.Fatalf("预演不应写入，得到 %d 行", n)
	}
	// 预演用的是同一套校验：违规的请求预演就失败
	_, err = st.PlanBranchLink(ctx, LinkBranchInput{ParentWorkflowID: "wf-main", BranchWorkflowID: "wf-main", CreatedBy: model.BranchCreatedByMigrate})
	wantBranchErr(t, err, ErrInvalidInput, "")
	applied, err := st.LinkBranch(ctx, in)
	if err != nil || !applied.Created {
		t.Fatalf("应用: %+v %v", applied, err)
	}
	plan2, err := st.PlanBranchLink(ctx, in)
	if err != nil || !plan2.Noop() {
		t.Fatalf("已存在的关系预演应是空操作: %+v %v", plan2, err)
	}
}

func TestBranchLink_AtomicRollbackOnTxFailure(t *testing.T) {
	st := newBranchStore(t)
	mkWF(t, st, "wf-main", "主会话")
	mkWF(t, st, "wf-b1", "分支一")
	sentinel := errors.New("boom")
	err := st.WithTx(context.Background(), func(tx *gorm.DB) error {
		if _, err := linkBranchTx(tx, LinkBranchInput{ParentWorkflowID: "wf-main", BranchWorkflowID: "wf-b1", CreatedBy: model.BranchCreatedByHuman}, time.Now()); err != nil {
			return err
		}
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("事务应回滚并返回原错误: %v", err)
	}
	if n := countBranchRows(t, st); n != 0 {
		t.Fatalf("事务回滚后不应有关系行，得到 %d", n)
	}
}

func TestBranchClose_IdempotentAndNotifiesTheOtherSide(t *testing.T) {
	st := newBranchStore(t)
	ctx := context.Background()
	mkWF(t, st, "wf-main", "主会话")
	mkWF(t, st, "wf-b1", "分支一")
	mustLink(t, st, "wf-main", "wf-b1", model.BranchCreatedByHuman)

	// 用户在网页结束：两边的收件箱各一条系统通知
	res, err := st.CloseBranch(ctx, "wf-b1", model.BranchCreatedByHuman)
	if err != nil || !res.Changed || res.Branch.Status != model.BranchStatusClosed || res.Branch.ClosedAt == nil {
		t.Fatalf("应结束: %+v %v", res, err)
	}
	var toBranch, toMain []model.QueuedFeedback
	st.DB().Where("workflow_id = ? AND kind = ?", "wf-b1", model.InboxKindBranch).Find(&toBranch)
	st.DB().Where("workflow_id = ? AND kind = ?", "wf-main", model.InboxKindBranch).Find(&toMain)
	if len(toBranch) != 1 || len(toMain) != 1 {
		t.Fatalf("用户结束时双方都应收到通知: branch=%d main=%d", len(toBranch), len(toMain))
	}
	if a, ok := model.ParseMsgActor(toBranch[0].Actor); !ok || a.Kind != model.ActorKindSystem {
		t.Fatalf("通知应是系统发言者: %+v", toBranch[0])
	}

	again, err := st.CloseBranch(ctx, "wf-b1", model.BranchCreatedByHuman)
	if err != nil || again.Changed {
		t.Fatalf("重复结束应是空操作: %+v %v", again, err)
	}
	var n int64
	st.DB().Model(&model.QueuedFeedback{}).Where("kind = ?", model.InboxKindBranch).Count(&n)
	if n != 2 {
		t.Fatalf("重复结束不应再发通知，通知总数应仍是 2，得到 %d", n)
	}

	_, err = st.CloseBranch(ctx, "wf-nope", model.BranchCreatedByHuman)
	wantBranchErr(t, err, ErrNotFound, "")
}

func TestBranchClose_ByMainAgentOnlyNotifiesBranch(t *testing.T) {
	st := newBranchStore(t)
	ctx := context.Background()
	mkWF(t, st, "wf-main", "主会话")
	mkWF(t, st, "wf-b1", "分支一")
	mustLink(t, st, "wf-main", "wf-b1", model.BranchCreatedByHuman)
	if _, err := st.CloseBranch(ctx, "wf-b1", model.BranchCreatedByAgent("wf-main")); err != nil {
		t.Fatal(err)
	}
	var mainN, branchN int64
	st.DB().Model(&model.QueuedFeedback{}).Where("workflow_id = ? AND kind = ?", "wf-main", model.InboxKindBranch).Count(&mainN)
	st.DB().Model(&model.QueuedFeedback{}).Where("workflow_id = ? AND kind = ?", "wf-b1", model.InboxKindBranch).Count(&branchN)
	if mainN != 0 || branchN != 1 {
		t.Fatalf("主会话自己结束的，只应通知分支: main=%d branch=%d", mainN, branchN)
	}
}

func TestBranchUnlink_PreviewKeepsRowApplyDeletesAndIsAudited(t *testing.T) {
	st := newBranchStore(t)
	ctx := context.Background()
	mkWF(t, st, "wf-main", "主会话")
	mkWF(t, st, "wf-b1", "分支一")
	mustLink(t, st, "wf-main", "wf-b1", model.BranchCreatedByHuman)

	row, err := st.UnlinkBranch(ctx, "wf-b1", true, model.BranchCreatedByMigrate)
	if err != nil || row.BranchWorkflowID != "wf-b1" {
		t.Fatalf("预览应返回将被删的行: %+v %v", row, err)
	}
	if n := countBranchRows(t, st); n != 1 {
		t.Fatalf("预览不应删除，得到 %d", n)
	}
	if _, err := st.UnlinkBranch(ctx, "wf-b1", false, model.BranchCreatedByMigrate); err != nil {
		t.Fatal(err)
	}
	if n := countBranchRows(t, st); n != 0 {
		t.Fatalf("应用后应删除，得到 %d", n)
	}
	var audits []model.AuditEvent
	st.DB().Where("action IN ?", []string{AuditBranchLink, AuditBranchUnlink}).Order("id ASC").Find(&audits)
	if len(audits) != 2 || audits[0].Action != AuditBranchLink || audits[1].Action != AuditBranchUnlink {
		t.Fatalf("应有 link 与 unlink 两条审计（预览不记）: %+v", audits)
	}
	_, err = st.UnlinkBranch(ctx, "wf-b1", false, model.BranchCreatedByMigrate)
	wantBranchErr(t, err, ErrNotFound, "")
}

func TestBranchSay_BranchToMainAlwaysAllowedMainToBranchNeedsSay(t *testing.T) {
	st := newBranchStore(t)
	ctx := context.Background()
	mkWF(t, st, "wf-main", "主会话")
	mkWF(t, st, "wf-b1", "分支一")
	visible, _ := model.BranchExposePreset(model.ExposePresetVisible)
	if _, err := st.LinkBranch(ctx, LinkBranchInput{ParentWorkflowID: "wf-main", BranchWorkflowID: "wf-b1", CreatedBy: model.BranchCreatedByHuman, Expose: &visible}); err != nil {
		t.Fatal(err)
	}

	// 分支 → 主会话：不需要 to，恒允许
	r, err := st.BranchSay(ctx, BranchSayInput{From: "wf-b1", Text: "  已完成登录页  "})
	if err != nil {
		t.Fatalf("分支应能给主会话发消息: %v", err)
	}
	if r.Direction != "branch_to_main" || r.To != "wf-main" || r.Row.WorkflowID != "wf-main" || r.Row.Kind != model.InboxKindBranch || r.Row.ResponseText != "已完成登录页" {
		t.Fatalf("送达结果不对: %+v", r)
	}
	if a, _ := model.ParseMsgActor(r.Row.Actor); a.Kind != model.ActorKindBranch || a.Workflow != "wf-b1" || r.Row.SenderRef != "wf-b1" {
		t.Fatalf("发言者应是分支: %+v", r.Row)
	}

	// 主会话 → 分支：仅可见预设没开 say → 拒绝
	_, err = st.BranchSay(ctx, BranchSayInput{From: "wf-main", Text: "请改一下"})
	wantBranchErr(t, err, ErrForbidden, "发消息")

	// 打开 say 之后允许；只有一个分支时不需要指定 to
	collab, _ := model.BranchExposePreset(model.ExposePresetCollab)
	if _, err := st.UpdateBranchSettings(ctx, "wf-b1", UpdateBranchInput{Expose: &collab}); err != nil {
		t.Fatal(err)
	}
	r2, err := st.BranchSay(ctx, BranchSayInput{From: "wf-main", Text: "请改一下"})
	if err != nil || r2.Direction != "main_to_branch" || r2.To != "wf-b1" {
		t.Fatalf("主会话应能给唯一的分支发消息: %+v %v", r2, err)
	}
	if a, _ := model.ParseMsgActor(r2.Row.Actor); a.Kind != model.ActorKindMain || a.Workflow != "wf-main" {
		t.Fatalf("发言者应是主会话: %+v", r2.Row)
	}
}

func TestBranchSay_Validation(t *testing.T) {
	st := newBranchStore(t)
	ctx := context.Background()
	for _, wf := range []string{"wf-main", "wf-b1", "wf-b2", "wf-x"} {
		mkWF(t, st, wf, wf)
	}
	mustLink(t, st, "wf-main", "wf-b1", model.BranchCreatedByHuman)
	mustLink(t, st, "wf-main", "wf-b2", model.BranchCreatedByHuman)

	_, err := st.BranchSay(ctx, BranchSayInput{From: "wf-main", Text: "   "})
	wantBranchErr(t, err, ErrInvalidInput, "text")
	_, err = st.BranchSay(ctx, BranchSayInput{From: "wf-main", Text: strings.Repeat("字", BranchSayMaxRunes+1), To: "wf-b1"})
	wantBranchErr(t, err, ErrInvalidInput, "太长")
	// 两个分支不指定 to → 要求指定，并列出可选项
	_, err = st.BranchSay(ctx, BranchSayInput{From: "wf-main", Text: "hi"})
	wantBranchErr(t, err, ErrInvalidInput, "wf-b2")
	// 和自己没有关系的工作流既不能发也不能收
	_, err = st.BranchSay(ctx, BranchSayInput{From: "wf-x", Text: "hi"})
	wantBranchErr(t, err, ErrInvalidInput, "没有可以发消息的对象")
	_, err = st.BranchSay(ctx, BranchSayInput{From: "wf-x", To: "wf-b1", Text: "hi"})
	wantBranchErr(t, err, ErrForbidden, "没有生效的")
	_, err = st.BranchSay(ctx, BranchSayInput{From: "wf-b1", To: "wf-b2", Text: "hi"})
	wantBranchErr(t, err, ErrForbidden, "没有生效的")
	// 指定 to 的主会话 → 分支
	if _, err := st.BranchSay(ctx, BranchSayInput{From: "wf-main", To: "wf-b2", Text: "hi"}); err != nil {
		t.Fatalf("指定 to 应成功: %v", err)
	}
	// 分支结束之后不能再发
	if _, err := st.CloseBranch(ctx, "wf-b2", model.BranchCreatedByHuman); err != nil {
		t.Fatal(err)
	}
	_, err = st.BranchSay(ctx, BranchSayInput{From: "wf-main", To: "wf-b2", Text: "hi"})
	wantBranchErr(t, err, ErrForbidden, "没有生效的")
}

func TestBranchSay_AntiLoopLimits(t *testing.T) {
	st := newBranchStore(t)
	ctx := context.Background()
	mkWF(t, st, "wf-main", "主会话")
	mkWF(t, st, "wf-b1", "分支一")
	mustLink(t, st, "wf-main", "wf-b1", model.BranchCreatedByHuman)

	// 对方未读堆到上限后拒绝
	for i := 0; i < BranchUnreadCap; i++ {
		if _, err := st.BranchSay(ctx, BranchSayInput{From: "wf-b1", Text: fmt.Sprintf("第 %d 条", i)}); err != nil {
			t.Fatalf("第 %d 条应成功: %v", i, err)
		}
	}
	_, err := st.BranchSay(ctx, BranchSayInput{From: "wf-b1", Text: "再来一条"})
	wantBranchErr(t, err, ErrConflict, "没读")
	// 对方读掉之后又能发
	if _, err := st.InboxRead(ctx, "wf-main", InboxReadOptions{}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := st.BranchSay(ctx, BranchSayInput{From: "wf-b1", Text: "读完了再发"}); err != nil {
		t.Fatalf("对方读过之后应能继续发: %v", err)
	}

	// 每小时上限：把上面的发送记录确认掉，再手工补满到上限
	if err := st.DB().Model(&model.QueuedFeedback{}).Where("kind = ?", model.InboxKindBranch).Update("status", "consumed").Error; err != nil {
		t.Fatal(err)
	}
	var already int64
	st.DB().Model(&model.QueuedFeedback{}).Where("kind = ? AND sender_ref = ?", model.InboxKindBranch, "wf-b1").Count(&already)
	for i := int(already); i < BranchSayPerHour; i++ {
		if _, err := st.queueBranchRow(ctx, "wf-main", "补", "wf-b1", model.MsgActor{Kind: model.ActorKindBranch, Workflow: "wf-b1"}, time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	st.DB().Model(&model.QueuedFeedback{}).Where("kind = ?", model.InboxKindBranch).Update("status", "consumed")
	_, err = st.BranchSay(ctx, BranchSayInput{From: "wf-b1", Text: "超过每小时上限"})
	wantBranchErr(t, err, ErrConflict, "一小时内")
	// 一小时之外的不计
	st.DB().Model(&model.QueuedFeedback{}).Where("kind = ?", model.InboxKindBranch).Update("created_at", time.Now().Add(-2*time.Hour))
	if _, err := st.BranchSay(ctx, BranchSayInput{From: "wf-b1", Text: "窗口滑过之后可以发"}); err != nil {
		t.Fatalf("两小时前的发送不应再计入: %v", err)
	}
}

func TestBranchInbox_SystemKindSemantics(t *testing.T) {
	st := newBranchStore(t)
	ctx := context.Background()
	mkWF(t, st, "wf-main", "主会话")
	mkWF(t, st, "wf-b1", "分支一")
	mustLink(t, st, "wf-main", "wf-b1", model.BranchCreatedByHuman)

	// 一条追加指令 + 两条分支消息
	app := queueText(t, st, "wf-main", "用户追加")
	for i := 0; i < 2; i++ {
		if _, err := st.BranchSay(ctx, BranchSayInput{From: "wf-b1", Text: fmt.Sprintf("分支汇报 %d", i)}); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now()
	sum, err := st.InboxSummary(ctx, "wf-main", now)
	if err != nil || sum.Unread != 3 || sum.UnreadBranch != 2 {
		t.Fatalf("概况应是 3 未读、其中 2 条分支消息: %+v %v", sum, err)
	}
	if sum.LatestPreview != "用户追加" {
		t.Fatalf("预览应取最新一条非分支消息的未读，得到 %q", sum.LatestPreview)
	}

	// 追加列表（网页）不含分支消息
	list, err := st.ListInboxItems(ctx, "wf-main")
	if err != nil || len(list) != 1 || list[0].ID != app.ID {
		t.Fatalf("追加列表应只有那条用户追加: %+v %v", list, err)
	}

	// 分支消息不能撤回
	var br model.QueuedFeedback
	st.DB().Where("workflow_id = ? AND kind = ?", "wf-main", model.InboxKindBranch).First(&br)
	if _, err := st.RevokeQueuedFeedback(ctx, br.ID); err == nil {
		t.Fatal("分支消息不应能被撤回")
	}

	// 创建新一轮会话的事务里取走收件箱：只并入追加，不并入分支消息
	var merged inboxMerge
	if err := st.WithTx(ctx, func(tx *gorm.DB) error {
		m, err := consumeInboxTx(tx, "wf-main", "s-new", now)
		merged = m
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if merged.Count != 1 || !strings.Contains(merged.Text, "用户追加") || strings.Contains(merged.Text, "分支汇报") {
		t.Fatalf("秒回只应并入用户追加: %+v", merged)
	}
	var stillPending int64
	st.DB().Model(&model.QueuedFeedback{}).Where("workflow_id = ? AND kind = ? AND status = 'pending'", "wf-main", model.InboxKindBranch).Count(&stillPending)
	if stillPending != 2 {
		t.Fatalf("分支消息应仍在收件箱里等 Agent 读取，得到 %d 条待读", stillPending)
	}

	// 主动读取能读到全文、带发言者
	res, err := st.InboxRead(ctx, "wf-main", InboxReadOptions{}, now)
	if err != nil {
		t.Fatal(err)
	}
	branchRead := 0
	for _, q := range res.New {
		if q.Kind == model.InboxKindBranch {
			branchRead++
			if q.Actor == "" || q.DeliveredAt == nil {
				t.Fatalf("读到的分支消息应带发言者并记为已送达: %+v", q)
			}
		}
	}
	if branchRead != 2 {
		t.Fatalf("应读到 2 条分支消息，得到 %d: %+v", branchRead, res.New)
	}
}

func TestBranchInbox_DoesNotCountTowardUserAppendCap(t *testing.T) {
	st := newBranchStore(t)
	ctx := context.Background()
	mkWF(t, st, "wf-main", "主会话")
	mkWF(t, st, "wf-b1", "分支一")
	mustLink(t, st, "wf-main", "wf-b1", model.BranchCreatedByHuman)
	for i := 0; i < 5; i++ {
		if _, err := st.BranchSay(ctx, BranchSayInput{From: "wf-b1", Text: fmt.Sprintf("m%d", i)}); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < InboxMaxPending; i++ {
		if _, err := st.QueueWorkflowFeedback(ctx, QueueFeedbackInput{WorkflowID: "wf-main", ResponseText: fmt.Sprintf("追加 %d", i)}); err != nil {
			t.Fatalf("分支消息不应占用户追加的上限（第 %d 条失败）: %v", i, err)
		}
	}
}

func TestBranchInbox_ActorOmittedForOrdinaryRows(t *testing.T) {
	st := newBranchStore(t)
	row := queueText(t, st, "wf-plain", "普通追加")
	b, err := json.Marshal(rowByID(t, st, row.ID))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), `"actor"`) {
		t.Fatalf("不是分支消息的行，响应里不应出现 actor 字段（保持与旧版本逐字节一致）: %s", b)
	}
}

func TestCopyUserNoteToMain(t *testing.T) {
	st := newBranchStore(t)
	ctx := context.Background()
	mkWF(t, st, "wf-main", "主会话")
	mkWF(t, st, "wf-b1", "分支一")
	mkWF(t, st, "wf-b2", "分支二")
	mustLink(t, st, "wf-main", "wf-b1", model.BranchCreatedByHuman)
	off := false
	if _, err := st.LinkBranch(ctx, LinkBranchInput{ParentWorkflowID: "wf-main", BranchWorkflowID: "wf-b2", CreatedBy: model.BranchCreatedByHuman, CopyUserNotes: &off}); err != nil {
		t.Fatal(err)
	}

	long := strings.Repeat("长", BranchCopyRunes+50)
	row, err := st.CopyUserNoteToMain(ctx, "wf-b1", "  先做登录页\n再做注册页 "+long, 0)
	if err != nil || row == nil {
		t.Fatalf("应抄送: %+v %v", row, err)
	}
	if row.WorkflowID != "wf-main" || row.Kind != model.InboxKindBranch || row.SenderRef != "wf-b1" {
		t.Fatalf("应写进主会话的收件箱: %+v", row)
	}
	if a, _ := model.ParseMsgActor(row.Actor); a.Kind != model.ActorKindHuman || a.Workflow != "wf-b1" {
		t.Fatalf("发言者应是用户（在 wf-b1 里）: %+v", row.Actor)
	}
	if !strings.Contains(row.ResponseText, "分支一") || !strings.Contains(row.ResponseText, "先做登录页 再做注册页") || !strings.HasSuffix(row.ResponseText, "…") {
		t.Fatalf("应带分支标题、折叠空白并截断: %q", row.ResponseText)
	}
	if n := len([]rune(row.ResponseText)); n > BranchCopyRunes+80 {
		t.Fatalf("摘要应被截断，得到 %d 字", n)
	}

	// 只有图片
	imgRow, err := st.CopyUserNoteToMain(ctx, "wf-b1", "", 2)
	if err != nil || imgRow == nil || !strings.Contains(imgRow.ResponseText, "2 张图片") {
		t.Fatalf("只有图片也应抄送一行: %+v %v", imgRow, err)
	}
	// 空内容不抄送
	if r, err := st.CopyUserNoteToMain(ctx, "wf-b1", "   ", 0); err != nil || r != nil {
		t.Fatalf("空内容不应抄送: %+v %v", r, err)
	}
	// 关了抄送 / 不是分支 / 已结束：静默跳过
	if r, err := st.CopyUserNoteToMain(ctx, "wf-b2", "不该抄送", 0); err != nil || r != nil {
		t.Fatalf("关闭抄送的分支不应抄送: %+v %v", r, err)
	}
	if r, err := st.CopyUserNoteToMain(ctx, "wf-main", "我是主会话", 0); err != nil || r != nil {
		t.Fatalf("不是分支不应抄送: %+v %v", r, err)
	}
	if _, err := st.CloseBranch(ctx, "wf-b1", model.BranchCreatedByHuman); err != nil {
		t.Fatal(err)
	}
	if r, err := st.CopyUserNoteToMain(ctx, "wf-b1", "结束后不再抄送", 0); err != nil || r != nil {
		t.Fatalf("已结束的分支不应抄送: %+v %v", r, err)
	}
}

func TestCopyUserNoteToMain_UnreadCap(t *testing.T) {
	st := newBranchStore(t)
	ctx := context.Background()
	mkWF(t, st, "wf-main", "主会话")
	mkWF(t, st, "wf-b1", "分支一")
	mustLink(t, st, "wf-main", "wf-b1", model.BranchCreatedByHuman)
	for i := 0; i < BranchUnreadCap+5; i++ {
		if _, err := st.CopyUserNoteToMain(ctx, "wf-b1", fmt.Sprintf("说 %d", i), 0); err != nil {
			t.Fatal(err)
		}
	}
	var n int64
	st.DB().Model(&model.QueuedFeedback{}).Where("workflow_id = ? AND kind = ?", "wf-main", model.InboxKindBranch).Count(&n)
	if n != BranchUnreadCap {
		t.Fatalf("主会话未读的抄送应封顶在 %d，得到 %d", BranchUnreadCap, n)
	}
}

func TestBranchFeed_RolesItemsAndTitles(t *testing.T) {
	st := newBranchStore(t)
	ctx := context.Background()
	mkWF(t, st, "wf-main", "主会话")
	mkWF(t, st, "wf-b1", "分支一")
	mkWF(t, st, "wf-lonely", "孤立")
	mustLink(t, st, "wf-main", "wf-b1", model.BranchCreatedByHuman)
	if _, err := st.BranchSay(ctx, BranchSayInput{From: "wf-b1", Text: "分支说话"}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.BranchSay(ctx, BranchSayInput{From: "wf-main", Text: "主会话回话"}); err != nil {
		t.Fatal(err)
	}

	mainFeed, err := st.BranchFeed(ctx, "wf-main", 0)
	if err != nil {
		t.Fatal(err)
	}
	if mainFeed.Role != BranchRoleMain || len(mainFeed.Branches) != 1 || mainFeed.Branches[0].Title != "分支一" || mainFeed.Branches[0].UnreadToMain != 1 {
		t.Fatalf("主会话视角不对: %+v", mainFeed)
	}
	if mainFeed.Branches[0].Preset != model.ExposePresetCollab {
		t.Fatalf("简报应带预设名（网页 / MCP 新建默认协作），界面靠它显示「协作」: %+v", mainFeed.Branches[0])
	}
	// 两个方向的话都在：建立事件 → 分支说的（收到）→ 主会话回的（发出）
	if len(mainFeed.Items) != 3 || mainFeed.Items[0].Kind != "event" || mainFeed.Items[0].Event != "created" || mainFeed.Items[1].Kind != "message" || mainFeed.Items[1].Text != "分支说话" {
		t.Fatalf("主会话的时间线应是「建立事件 + 分支说的话 + 主会话自己回的话」: %+v", mainFeed.Items)
	}
	if mainFeed.Items[1].Branch != "wf-b1" || mainFeed.Items[1].ActorTitle != "分支一" || mainFeed.Titles["wf-b1"] != "分支一" || mainFeed.Items[1].Outgoing || mainFeed.Items[1].To != "" {
		t.Fatalf("收到的消息应标出所属分支与发言者标题，且不是发出的: %+v titles=%v", mainFeed.Items[1], mainFeed.Titles)
	}
	if out := mainFeed.Items[2]; !out.Outgoing || out.To != "wf-b1" || out.Branch != "wf-b1" || out.Text != "主会话回话" ||
		out.Actor.Kind != model.ActorKindMain || out.Actor.Workflow != "wf-main" || out.ActorTitle != "主会话" || out.State != "queued" {
		t.Fatalf("主会话发给分支的话应标「发出」、收件人与归属分支、发言者是自己、对方还没读: %+v", out)
	}

	branchFeed, err := st.BranchFeed(ctx, "wf-b1", 0)
	if err != nil {
		t.Fatal(err)
	}
	if branchFeed.Role != BranchRoleBranch || branchFeed.Relation == nil || branchFeed.ParentTitle != "主会话" {
		t.Fatalf("分支视角不对: %+v", branchFeed)
	}
	var sawMain, sawOwn bool
	for _, it := range branchFeed.Items {
		if it.Kind == "message" && it.Text == "主会话回话" {
			sawMain = true
			if it.Actor.Kind != model.ActorKindMain || it.Branch != "wf-b1" || it.Outgoing {
				t.Fatalf("主会话的话应标 main、归属到本分支、对分支来说是收到的: %+v", it)
			}
		}
		if it.Kind == "message" && it.Text == "分支说话" {
			sawOwn = true
			if it.Actor.Kind != model.ActorKindBranch || it.Actor.Workflow != "wf-b1" || it.Branch != "wf-b1" || !it.Outgoing || it.To != "wf-main" {
				t.Fatalf("分支自己发给主会话的话应标 branch、发出、收件人是主会话: %+v", it)
			}
		}
	}
	if !sawMain || !sawOwn {
		t.Fatalf("分支视角应看到主会话发来的消息，也看到自己发出去的话: %+v", branchFeed.Items)
	}

	none, err := st.BranchFeed(ctx, "wf-lonely", 0)
	if err != nil || none.Role != BranchRoleNone || len(none.Items) != 0 || none.Items == nil {
		t.Fatalf("没有关系的工作流：角色 none、items 为空数组（不是 null）: %+v %v", none, err)
	}

	// 结束之后事件行与角色仍在
	if _, err := st.CloseBranch(ctx, "wf-b1", model.BranchCreatedByHuman); err != nil {
		t.Fatal(err)
	}
	after, _ := st.BranchFeed(ctx, "wf-main", 0)
	if after.Role != BranchRoleMain {
		t.Fatalf("结束之后主会话视角仍应是 main: %+v", after)
	}
	var closed bool
	for _, it := range after.Items {
		if it.Kind == "event" && it.Event == "closed" {
			closed = true
		}
	}
	if !closed {
		t.Fatalf("应有结束事件: %+v", after.Items)
	}
}

// 用户在分支页里说的话被抄送到主会话：对主会话是「收到的」用户抄送；对分支不是「发出的消息」（用户的话本来就在分支自己的页面里）。
func TestBranchFeed_UserCopyIsNotOutgoing(t *testing.T) {
	st := newBranchStore(t)
	ctx := context.Background()
	mkWF(t, st, "wf-main", "主会话")
	mkWF(t, st, "wf-b1", "分支一")
	mustLink(t, st, "wf-main", "wf-b1", model.BranchCreatedByHuman)
	if r, err := st.CopyUserNoteToMain(ctx, "wf-b1", "先做登录页", 0); err != nil || r == nil {
		t.Fatalf("应抄送: %+v %v", r, err)
	}

	mainFeed, err := st.BranchFeed(ctx, "wf-main", 0)
	if err != nil {
		t.Fatal(err)
	}
	var seen int
	for _, it := range mainFeed.Items {
		if it.Kind != "message" {
			continue
		}
		seen++
		if it.Actor.Kind != model.ActorKindHuman || it.Outgoing || it.Branch != "wf-b1" || !strings.Contains(it.Text, "先做登录页") {
			t.Fatalf("主会话应把它当作收到的用户抄送: %+v", it)
		}
	}
	if seen != 1 {
		t.Fatalf("主会话应恰有一条消息: %+v", mainFeed.Items)
	}

	branchFeed, err := st.BranchFeed(ctx, "wf-b1", 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, it := range branchFeed.Items {
		if it.Kind == "message" {
			t.Fatalf("用户的话不应变成分支「发出」的消息: %+v", it)
		}
	}
}

func TestBranchFeed_OrderedByTimeAndLimited(t *testing.T) {
	st := newBranchStore(t)
	ctx := context.Background()
	mkWF(t, st, "wf-main", "主会话")
	mkWF(t, st, "wf-b1", "分支一")
	mustLink(t, st, "wf-main", "wf-b1", model.BranchCreatedByHuman)
	base := time.Now().Add(time.Hour)
	for i := 0; i < 6; i++ {
		if _, err := st.queueBranchRow(ctx, "wf-main", fmt.Sprintf("m%d", i), "wf-b1", model.MsgActor{Kind: model.ActorKindBranch, Workflow: "wf-b1"}, base.Add(time.Duration(i)*time.Minute)); err != nil {
			t.Fatal(err)
		}
	}
	feed, err := st.BranchFeed(ctx, "wf-main", 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(feed.Items) != 3 || feed.Items[0].Text != "m3" || feed.Items[2].Text != "m5" {
		t.Fatalf("应保留最新 3 项并按时间升序: %+v", feed.Items)
	}
}

func TestReadBranchHistory_DepthAndPermissions(t *testing.T) {
	st := newBranchStore(t)
	ctx := context.Background()
	mkWF(t, st, "wf-main", "主会话")
	mkWF(t, st, "wf-b1", "分支一")
	mkWF(t, st, "wf-other", "别人")
	mustLink(t, st, "wf-main", "wf-b1", model.BranchCreatedByHuman)
	// 给分支补两轮：一轮阻塞汇报（多行 + 回复）、一轮进度通知
	now := time.Now()
	long := "第一行摘要\n" + strings.Repeat("详", 300)
	for i, row := range []model.FeedbackSession{
		{ID: "s-r1", WorkflowID: "wf-b1", Title: "登录页", Summary: long, ResponseText: "好的\n继续", Status: "completed", CreatedAt: now.Add(time.Minute)},
		{ID: "s-r2", WorkflowID: "wf-b1", Kind: model.FeedbackKindNotice, Title: "进度", Summary: "做完了注册页", Status: "completed", CreatedAt: now.Add(2 * time.Minute)},
	} {
		row.UserMessages, row.Images, row.UpdatedAt = model.StringArray{}, model.SessionImages{}, row.CreatedAt
		if err := st.DB().Create(&row).Error; err != nil {
			t.Fatalf("row %d: %v", i, err)
		}
	}

	// 摘要档：每轮只给首行（≤200 字），升序
	h, err := st.ReadBranchHistory(ctx, "wf-main", "wf-b1", 0)
	if err != nil || h.Depth != model.ExposeHistoryFull {
		t.Fatalf("协作预设默认读全文: %+v %v", h, err)
	}
	visible, _ := model.BranchExposePreset(model.ExposePresetVisible)
	if _, err := st.UpdateBranchSettings(ctx, "wf-b1", UpdateBranchInput{Expose: &visible}); err != nil {
		t.Fatal(err)
	}
	h, err = st.ReadBranchHistory(ctx, "wf-main", "wf-b1", 10)
	if err != nil || h.Depth != model.ExposeHistorySummary {
		t.Fatalf("仅可见预设应是摘要档: %+v %v", h, err)
	}
	if len(h.Rounds) != 3 {
		t.Fatalf("应有 3 轮（建库时的 1 轮 + 2 轮）: %d", len(h.Rounds))
	}
	var r1 *BranchRound
	for i := range h.Rounds {
		if h.Rounds[i].SessionID == "s-r1" {
			r1 = &h.Rounds[i]
		}
	}
	if r1 == nil || r1.Summary != "第一行摘要" || r1.Reply != "好的" || r1.Kind != "report" {
		t.Fatalf("摘要档只给首行: %+v", r1)
	}
	for i := 1; i < len(h.Rounds); i++ {
		if h.Rounds[i].At.Before(h.Rounds[i-1].At) {
			t.Fatalf("应按时间升序: %+v", h.Rounds)
		}
	}
	notice := false
	for _, r := range h.Rounds {
		if r.SessionID == "s-r2" && r.Kind == "notice" {
			notice = true
		}
	}
	if !notice {
		t.Fatalf("进度通知应标 notice: %+v", h.Rounds)
	}

	// 全文档：给全文，但每段不超过上限
	full, _ := model.BranchExposePreset(model.ExposePresetCollab)
	if _, err := st.UpdateBranchSettings(ctx, "wf-b1", UpdateBranchInput{Expose: &full}); err != nil {
		t.Fatal(err)
	}
	h, _ = st.ReadBranchHistory(ctx, "wf-main", "wf-b1", 10)
	for _, r := range h.Rounds {
		if r.SessionID == "s-r1" && (!strings.Contains(r.Summary, "第一行摘要\n") || r.Reply != "好的\n继续") {
			t.Fatalf("全文档应给全文: %+v", r)
		}
	}

	// off：拒绝
	off := model.BranchExpose{History: model.ExposeHistoryOff, Params: model.ExposeParamsOff, Tasks: model.ExposeTasksRead}
	if _, err := st.UpdateBranchSettings(ctx, "wf-b1", UpdateBranchInput{Expose: &off}); err != nil {
		t.Fatal(err)
	}
	_, err = st.ReadBranchHistory(ctx, "wf-main", "wf-b1", 10)
	wantBranchErr(t, err, ErrForbidden, "读历史")
	// 不是自己的分支
	_, err = st.ReadBranchHistory(ctx, "wf-other", "wf-b1", 10)
	wantBranchErr(t, err, ErrForbidden, "不是你的分支")
	_, err = st.ReadBranchHistory(ctx, "wf-main", "wf-nope", 10)
	wantBranchErr(t, err, ErrForbidden, "不是你的分支")
	// 已结束
	if _, err := st.UpdateBranchSettings(ctx, "wf-b1", UpdateBranchInput{Expose: &full}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CloseBranch(ctx, "wf-b1", model.BranchCreatedByHuman); err != nil {
		t.Fatal(err)
	}
	_, err = st.ReadBranchHistory(ctx, "wf-main", "wf-b1", 10)
	wantBranchErr(t, err, ErrForbidden, "已经结束")
}

func TestUpdateBranchSettings_PresetExposeConflictAndAudit(t *testing.T) {
	st := newBranchStore(t)
	ctx := context.Background()
	mkWF(t, st, "wf-main", "主会话")
	mkWF(t, st, "wf-b1", "分支一")
	mustLink(t, st, "wf-main", "wf-b1", model.BranchCreatedByHuman)

	row, err := st.UpdateBranchSettings(ctx, "wf-b1", UpdateBranchInput{Preset: "managed"})
	if err != nil || row.Expose.PresetName() != model.ExposePresetManaged {
		t.Fatalf("应切到托管预设: %+v %v", row, err)
	}
	off := false
	row, err = st.UpdateBranchSettings(ctx, "wf-b1", UpdateBranchInput{CopyUserNotes: &off})
	if err != nil || row.CopyUserNotes || row.Expose.PresetName() != model.ExposePresetManaged {
		t.Fatalf("只改抄送不应动开放范围: %+v %v", row, err)
	}
	custom := model.BranchExpose{History: model.ExposeHistorySummary, Say: true, Params: model.ExposeParamsOff, Tasks: model.ExposeTasksRead}
	row, err = st.UpdateBranchSettings(ctx, "wf-b1", UpdateBranchInput{Expose: &custom})
	if err != nil || row.Expose.PresetName() != "" {
		t.Fatalf("自定义组合没有预设名: %+v %v", row, err)
	}
	_, err = st.UpdateBranchSettings(ctx, "wf-b1", UpdateBranchInput{Preset: "collab", Expose: &custom})
	wantBranchErr(t, err, ErrInvalidInput, "只能给一个")
	_, err = st.UpdateBranchSettings(ctx, "wf-b1", UpdateBranchInput{Preset: "turbo"})
	wantBranchErr(t, err, ErrInvalidInput, "visible")
	_, err = st.UpdateBranchSettings(ctx, "wf-nope", UpdateBranchInput{Preset: "visible"})
	wantBranchErr(t, err, ErrNotFound, "")

	var n int64
	st.DB().Model(&model.AuditEvent{}).Where("action = ?", AuditBranchSettings).Count(&n)
	if n != 3 {
		t.Fatalf("三次成功的修改应各记一条审计，得到 %d", n)
	}
}

func TestBranchAudit_NeverRecordsBodies(t *testing.T) {
	st := newBranchStore(t)
	ctx := context.Background()
	mkWF(t, st, "wf-main", "主会话")
	mkWF(t, st, "wf-b1", "分支一")
	mustLink(t, st, "wf-main", "wf-b1", model.BranchCreatedByHuman)
	secret := "机密正文-不该进审计"
	if _, err := st.BranchSay(ctx, BranchSayInput{From: "wf-b1", Text: secret}); err != nil {
		t.Fatal(err)
	}
	var evs []model.AuditEvent
	st.DB().Where("action = ?", AuditBranchSay).Find(&evs)
	if len(evs) != 1 {
		t.Fatalf("应有 1 条 branch.say 审计: %d", len(evs))
	}
	if strings.Contains(evs[0].Detail, secret) || strings.Contains(evs[0].Target, secret) {
		t.Fatalf("审计不得记正文: %+v", evs[0])
	}
	if !strings.Contains(evs[0].Detail, `"runes"`) {
		t.Fatalf("审计应记字数: %s", evs[0].Detail)
	}
}

func TestCreateWebWorkflow_AsBranchIsAtomic(t *testing.T) {
	st := newBranchStore(t)
	ctx := context.Background()
	mkWF(t, st, "wf-main", "主会话")

	// 主会话不存在 → 整个创建回滚：不留占位会话、不留标题
	_, err := st.CreateWebWorkflow(ctx, CreateWebWorkflowInput{Title: "不该留下", WorkflowID: "wf-web-ghost", Branch: &CreateWebBranchSpec{ParentWorkflowID: "wf-ghost"}})
	wantBranchErr(t, err, ErrNotFound, "wf-ghost")
	var n int64
	st.DB().Model(&model.FeedbackSession{}).Where("workflow_id = ?", "wf-web-ghost").Count(&n)
	if n != 0 {
		t.Fatalf("关系校验失败应整体回滚，却留下 %d 行会话", n)
	}
	st.DB().Model(&model.SystemSetting{}).Where("key = ?", "wf_custom_title:wf-web-ghost").Count(&n)
	if n != 0 {
		t.Fatalf("标题设置也应回滚")
	}

	// 预设与显式取值不能同时给；未知预设被拒；都在落库之前
	custom := model.BranchExpose{History: model.ExposeHistoryFull, Params: model.ExposeParamsOff, Tasks: model.ExposeTasksRead}
	_, err = st.CreateWebWorkflow(ctx, CreateWebWorkflowInput{Title: "x1", WorkflowID: "wf-web-x1", Branch: &CreateWebBranchSpec{ParentWorkflowID: "wf-main", ExposePreset: "collab", Expose: &custom}})
	wantBranchErr(t, err, ErrInvalidInput, "只能给一个")
	_, err = st.CreateWebWorkflow(ctx, CreateWebWorkflowInput{Title: "x2", WorkflowID: "wf-web-x2", Branch: &CreateWebBranchSpec{ParentWorkflowID: "wf-main", ExposePreset: "turbo"}})
	wantBranchErr(t, err, ErrInvalidInput, "visible")

	// 成功：占位会话、标题、关系同事务，主会话收件箱有一条系统说明，审计有 link
	res, err := st.CreateWebWorkflow(ctx, CreateWebWorkflowInput{Title: "登录页分支", WorkflowID: "wf-web-ok", Branch: &CreateWebBranchSpec{ParentWorkflowID: "wf-main", ExposePreset: "visible"}})
	if err != nil || res.Branch == nil {
		t.Fatalf("应创建成功: %+v %v", res, err)
	}
	if res.Branch.ParentWorkflowID != "wf-main" || res.Branch.BranchWorkflowID != "wf-web-ok" || res.Branch.CreatedBy != model.BranchCreatedByHuman || res.Branch.Expose.PresetName() != model.ExposePresetVisible {
		t.Fatalf("关系不对: %+v", res.Branch)
	}
	var notes []model.QueuedFeedback
	st.DB().Where("workflow_id = ? AND kind = ?", "wf-main", model.InboxKindBranch).Find(&notes)
	if len(notes) != 1 || !strings.Contains(notes[0].ResponseText, "登录页分支") || !strings.Contains(notes[0].ResponseText, "wf-web-ok") {
		t.Fatalf("主会话应收到新分支的说明: %+v", notes)
	}
	st.DB().Model(&model.AuditEvent{}).Where("action = ? AND target = ?", AuditBranchLink, "wf-web-ok").Count(&n)
	if n != 1 {
		t.Fatalf("应记一条 branch.link 审计，得到 %d", n)
	}

	// 不带 Branch 的创建行为不变：没有关系行、没有通知
	before := countBranchRows(t, st)
	plain, err := st.CreateWebWorkflow(ctx, CreateWebWorkflowInput{Title: "普通会话", WorkflowID: "wf-web-plain"})
	if err != nil || plain.Branch != nil {
		t.Fatalf("普通创建: %+v %v", plain, err)
	}
	if countBranchRows(t, st) != before {
		t.Fatalf("普通创建不应写关系行")
	}
}

func TestBranchBriefs_WaitingMissingAndUnread(t *testing.T) {
	st := newBranchStore(t)
	ctx := context.Background()
	mkWF(t, st, "wf-main", "主会话")
	mkWF(t, st, "wf-b1", "分支一")
	mustLink(t, st, "wf-main", "wf-b1", model.BranchCreatedByHuman)

	rows, _ := st.ListAllBranches(ctx)
	b := st.BranchBriefs(ctx, rows)[0]
	if b.Waiting || b.Missing || b.Title != "分支一" || b.ParentTitle != "主会话" || b.LastActivityAt == nil {
		t.Fatalf("初始概况不对: %+v", b)
	}
	// 一轮阻塞汇报在等回复
	now := time.Now()
	pending := model.FeedbackSession{ID: "s-wait", WorkflowID: "wf-b1", Title: "等回复", Summary: "请定夺", Status: "pending", UserMessages: model.StringArray{}, Images: model.SessionImages{}, CreatedAt: now, UpdatedAt: now}
	if err := st.DB().Create(&pending).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := st.BranchSay(ctx, BranchSayInput{From: "wf-b1", Text: "有事"}); err != nil {
		t.Fatal(err)
	}
	b = st.BranchBriefs(ctx, rows)[0]
	if !b.Waiting || b.UnreadToMain != 1 {
		t.Fatalf("应显示在等回复、主会话有 1 条未读: %+v", b)
	}
	// 分支的会话行都没了 → 标记为已失效
	if err := st.DB().Where("workflow_id = ?", "wf-b1").Delete(&model.FeedbackSession{}).Error; err != nil {
		t.Fatal(err)
	}
	b = st.BranchBriefs(ctx, rows)[0]
	if !b.Missing || b.LastActivityAt != nil {
		t.Fatalf("会话行消失应标 Missing: %+v", b)
	}
}

func TestActiveBranchLinks(t *testing.T) {
	st := newBranchStore(t)
	ctx := context.Background()
	for _, wf := range []string{"wf-main", "wf-b1", "wf-b2", "wf-x"} {
		mkWF(t, st, wf, wf)
	}
	mustLink(t, st, "wf-main", "wf-b1", model.BranchCreatedByHuman)
	mustLink(t, st, "wf-main", "wf-b2", model.BranchCreatedByHuman)
	if _, err := st.CloseBranch(ctx, "wf-b2", model.BranchCreatedByHuman); err != nil {
		t.Fatal(err)
	}
	l, err := st.ActiveBranchLinks(ctx, "wf-main")
	if err != nil || l.AsBranch != nil || len(l.AsMain) != 1 || l.AsMain[0].BranchWorkflowID != "wf-b1" {
		t.Fatalf("主会话只应看到生效的分支: %+v %v", l, err)
	}
	l, _ = st.ActiveBranchLinks(ctx, "wf-b1")
	if l.AsBranch == nil || l.AsBranch.ParentWorkflowID != "wf-main" || len(l.AsMain) != 0 {
		t.Fatalf("分支视角不对: %+v", l)
	}
	l, _ = st.ActiveBranchLinks(ctx, "wf-b2")
	if l.AsBranch != nil || len(l.AsMain) != 0 {
		t.Fatalf("已结束的分支不应出现在活动关系里: %+v", l)
	}
	l, _ = st.ActiveBranchLinks(ctx, "wf-x")
	if l.AsBranch != nil || len(l.AsMain) != 0 {
		t.Fatalf("没有关系的工作流应是零值（回执头据此一个字都不输出）: %+v", l)
	}
	l, err = st.ActiveBranchLinks(ctx, "")
	if err != nil || l.AsBranch != nil || len(l.AsMain) != 0 {
		t.Fatalf("空编号应是零值: %+v %v", l, err)
	}
}

func TestBranchExpose_ValueScanRoundTripAndPresets(t *testing.T) {
	for _, name := range []string{model.ExposePresetVisible, model.ExposePresetCollab, model.ExposePresetManaged} {
		p, ok := model.BranchExposePreset(name)
		if !ok || p.Validate() != nil || p.PresetName() != name {
			t.Fatalf("预设 %s 应自洽: %+v ok=%v err=%v", name, p, ok, p.Validate())
		}
		v, err := p.Value()
		if err != nil {
			t.Fatal(err)
		}
		var back model.BranchExpose
		if err := back.Scan(v); err != nil || back != p {
			t.Fatalf("Value/Scan 应往返一致: %+v vs %+v (%v)", back, p, err)
		}
	}
	var e model.BranchExpose
	if err := e.Scan(nil); err != nil || e != (model.BranchExpose{}) {
		t.Fatalf("nil 应扫成零值: %+v %v", e, err)
	}
	if _, ok := model.BranchExposePreset("nope"); ok {
		t.Fatal("未知预设应返回 false")
	}
	a := model.MsgActor{Kind: model.ActorKindBranch, Workflow: "wf-x"}
	if got, ok := model.ParseMsgActor(a.String()); !ok || got != a {
		t.Fatalf("发言者往返: %+v %v", got, ok)
	}
	if _, ok := model.ParseMsgActor("not json"); ok {
		t.Fatal("坏数据应返回 false")
	}
	if (model.MsgActor{}).String() != "" {
		t.Fatal("空发言者应序列化成空串")
	}
}
