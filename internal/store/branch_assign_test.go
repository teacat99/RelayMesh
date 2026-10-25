package store

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/teacat99/RelayMesh/v2/internal/model"
)

// 主会话给分支派单（#236 P5）：任务级权限的带范围例外、主会话与分支的关系集合、结束分支时退回任务。

type assignEnv struct {
	st  *Store
	ctx context.Context
}

// newAssignEnv 造一个项目 a（目录 /home/user/a），同一张板上有：wf-main（主会话）、wf-b1 / wf-b2 / wf-b3（它的三个分支）、
// wf-other（无关的普通 Agent）、wf-mgr（项目管理 Agent）；wf-solo 在项目外（独立板），也被挂成 wf-main 的分支。
// 分支的开放范围：b1 协作（任务 = 派单）、b2 仅可见（任务 = 只读）、b3 协作、solo 协作。
func newAssignEnv(t *testing.T) *assignEnv {
	t.Helper()
	st, ctx := projTestStore(t)
	t.Cleanup(func() { _ = st.Close() })
	proj, err := st.CreateProject(ctx, ProjectInput{Name: "A", Slug: "a"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.AddProjectDir(ctx, proj.ID, "devhost", "/home/user/a", ""); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	for _, wf := range []string{"wf-main", "wf-b1", "wf-b2", "wf-b3", "wf-other", "wf-mgr"} {
		seedWorkflowDir(t, st, wf, "devhost", "/home/user/a", now)
	}
	seedWorkflowDir(t, st, "wf-solo", "devhost", "/tmp/other", now)
	visible, _ := model.BranchExposePreset(model.ExposePresetVisible)
	for branch, exp := range map[string]*model.BranchExpose{"wf-b1": nil, "wf-b2": &visible, "wf-b3": nil, "wf-solo": nil} {
		if _, err := st.LinkBranch(ctx, LinkBranchInput{ParentWorkflowID: "wf-main", BranchWorkflowID: branch, CreatedBy: model.BranchCreatedByHuman, Expose: exp, SkipRateLimit: true}); err != nil {
			t.Fatalf("LinkBranch %s: %v", branch, err)
		}
	}
	return &assignEnv{st: st, ctx: ctx}
}

// mainActor 是此刻的主会话 wf-main：关系集合现查（改了开放范围、结束了分支之后要重新取）。
func (e *assignEnv) mainActor(t *testing.T, policy WorkPolicy) WorkActor {
	t.Helper()
	sc, err := e.st.BranchScopeOf(e.ctx, "wf-main")
	if err != nil {
		t.Fatal(err)
	}
	a := agentAs("wf-main", policy)
	a.Branches = sc
	return a
}

// forbidden 要求这次更新被任务级权限拒绝，且文案含 sub。
func (e *assignEnv) forbidden(t *testing.T, id uint, p WorkItemPatch, actor WorkActor, sub string) {
	t.Helper()
	updateErr(t, e.st, id, p, actor, ErrForbidden, sub)
}

func TestBranchScopeOf(t *testing.T) {
	e := newAssignEnv(t)
	got, err := e.st.BranchScopeOf(e.ctx, "wf-main")
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]WorkBranchScope{
		"wf-b1": {Active: true, Assign: true}, "wf-b2": {Active: true, Assign: false},
		"wf-b3": {Active: true, Assign: true}, "wf-solo": {Active: true, Assign: true},
	}
	if len(got) != len(want) {
		t.Fatalf("关系集合 = %+v，期望 %+v", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Fatalf("%s = %+v，期望 %+v", k, got[k], v)
		}
	}

	// 结束的分支仍在集合里（Active 为假）：收回自己派出的指派不看关系状态；开放范围改成只读后 Assign 随之变假。
	if _, err := e.st.CloseBranch(e.ctx, "wf-b3", model.BranchCreatedByHuman); err != nil {
		t.Fatal(err)
	}
	if _, err := e.st.UpdateBranchSettings(e.ctx, "wf-b1", UpdateBranchInput{Preset: model.ExposePresetVisible}); err != nil {
		t.Fatal(err)
	}
	got, _ = e.st.BranchScopeOf(e.ctx, "wf-main")
	if got["wf-b3"] != (WorkBranchScope{Active: false, Assign: false}) || got["wf-b1"] != (WorkBranchScope{Active: true, Assign: false}) {
		t.Fatalf("结束 / 改只读之后的集合不对: %+v", got)
	}

	// 分支自己、无关的工作流、空串：没有分支，返回 nil。
	for _, wf := range []string{"wf-b1", "wf-other", "", "  "} {
		if sc, err := e.st.BranchScopeOf(e.ctx, wf); err != nil || sc != nil {
			t.Fatalf("%q 没有分支，应返回 nil: %+v %v", wf, sc, err)
		}
	}
}

// 整圈：主会话（普通 Agent，不是管理 Agent）把自己建的任务派给分支（创建时带承接者 + 先建后派两条路径）→ 分支接取 → 交付 → 主会话验收；enforce 档全程不被拒。
func TestMainAssignsOwnTaskToBranchFullLoop(t *testing.T) {
	e := newAssignEnv(t)
	main := e.mainActor(t, WorkPolicyEnforce)
	b1 := agentAs("wf-b1", WorkPolicyEnforce)

	t1 := mustCreate(t, e.st, WorkItemInput{WorkflowID: "wf-main", Title: "改接口", Assignee: "wf-b1"}, main)
	if t1.AssigneeWorkflowID != "wf-b1" || t1.AssignedByWorkflowID != "wf-main" || t1.ClaimState != model.WorkClaimOffered {
		t.Fatalf("创建时带承接者：字段不对 %+v", t1)
	}
	t2 := mustCreate(t, e.st, WorkItemInput{WorkflowID: "wf-main", Title: "写测试"}, main)
	res := mustUpdate(t, e.st, t2.ID, WorkItemPatch{Assignee: sp("wf-b1")}, main)
	if len(res.Violations) != 0 || res.Item.AssigneeWorkflowID != "wf-b1" || res.Item.AssignedByWorkflowID != "wf-main" || res.Item.Updates[0].Event != model.WorkEventAssign {
		t.Fatalf("先建后派：%+v violations=%v", res.Item, res.Violations)
	}

	for _, it := range []*model.WorkItem{t1, t2} {
		got := mustUpdate(t, e.st, it.ID, WorkItemPatch{Claim: "accept"}, b1)
		if got.Item.ClaimState != model.WorkClaimAccepted || got.Item.State != model.WorkStateDoing {
			t.Fatalf("分支接取后应是 accepted + doing: %+v", got.Item)
		}
		got = mustUpdate(t, e.st, it.ID, WorkItemPatch{Claim: "deliver", Note: "做完了，测试通过", Conclusion: sp("已完成并验证")}, b1)
		if got.Item.State != model.WorkStateReview {
			t.Fatalf("交付后应是 review: %+v", got.Item)
		}
		// 只有派发者（主会话）能验收：分支自己验收被拒。
		e.forbidden(t, it.ID, WorkItemPatch{Claim: "accept_delivery"}, b1, "验收")
		got = mustUpdate(t, e.st, it.ID, WorkItemPatch{Claim: "accept_delivery"}, main)
		if got.Item.State != model.WorkStateDone || len(got.Violations) != 0 {
			t.Fatalf("主会话验收应通过并完成: %+v violations=%v", got.Item, got.Violations)
		}
	}
}

// 例外只放行「自己建的任务 → 自己的、开放了派单的活动分支」：其余一律拒绝，文案点明哪一条没满足。
func TestMainAssignDenials(t *testing.T) {
	e := newAssignEnv(t)
	main := e.mainActor(t, WorkPolicyEnforce)
	own := mustCreate(t, e.st, WorkItemInput{WorkflowID: "wf-main", Title: "我的任务"}, main)

	// 目标不是自己的分支 / 是自己 / 是只读的分支 / 是已结束的分支。
	e.forbidden(t, own.ID, WorkItemPatch{Assignee: sp("wf-other")}, main, "「wf-other」不是你的分支")
	e.forbidden(t, own.ID, WorkItemPatch{Assignee: sp("wf-main")}, main, "「wf-main」不是你的分支")
	e.forbidden(t, own.ID, WorkItemPatch{Assignee: sp("wf-b2")}, main, "开放范围里「任务」是只读")
	if _, err := e.st.CloseBranch(e.ctx, "wf-b3", model.BranchCreatedByHuman); err != nil {
		t.Fatal(err)
	}
	main = e.mainActor(t, WorkPolicyEnforce)
	e.forbidden(t, own.ID, WorkItemPatch{Assignee: sp("wf-b3")}, main, "已经结束")
	// 创建时带承接者走另一个调用点（探针），同样拒绝。
	_, err := e.st.CreateWorkItem(e.ctx, WorkItemInput{WorkflowID: "wf-main", Title: "x", Assignee: "wf-b2"}, main)
	if ae, ok := err.(*AppError); !ok || ae.Kind != ErrForbidden || !strings.Contains(ae.Message, "只读") {
		t.Fatalf("创建时带承接者给只读分支应被拒: %v", err)
	}

	// 别的工作流创建的任务，主会话不能替它派。
	others := mustCreate(t, e.st, WorkItemInput{WorkflowID: "wf-other", Title: "别人的任务"}, agentAs("wf-other", WorkPolicyEnforce))
	e.forbidden(t, others.ID, WorkItemPatch{Assignee: sp("wf-b1")}, main, "主会话只能派自己创建的任务")

	// 管理 Agent 派出去的指派，主会话不能撤销也不能改派（不是自己派的）。
	mgr := managerAs("wf-mgr", WorkPolicyEnforce)
	mine := mustCreate(t, e.st, WorkItemInput{WorkflowID: "wf-main", Title: "管理 Agent 派的"}, main)
	mustUpdate(t, e.st, mine.ID, WorkItemPatch{Assignee: sp("wf-b1")}, mgr)
	e.forbidden(t, mine.ID, WorkItemPatch{Assignee: sp("")}, main, "现有的指派不是你派给你的分支的")
	e.forbidden(t, mine.ID, WorkItemPatch{Assignee: sp("wf-b3")}, main, "已经结束") // 目标这条先挡住
	// 管理 Agent 自己当然能改派。
	if res := mustUpdate(t, e.st, mine.ID, WorkItemPatch{Assignee: sp("wf-b2")}, mgr); res.Item.AssigneeWorkflowID != "wf-b2" {
		t.Fatalf("管理 Agent 改派: %+v", res.Item)
	}
}

// 没有分支的普通 Agent：拒绝文案与升级前逐字一致（不附带任何分支说明）；分支自己也没有分支。
func TestDenyMessageUnchangedWithoutBranches(t *testing.T) {
	e := newAssignEnv(t)
	want := denyMessage(workOpAssign, &model.WorkItem{})
	for _, wf := range []string{"wf-other", "wf-b1"} {
		a := agentAs(wf, WorkPolicyEnforce)
		it := mustCreate(t, e.st, WorkItemInput{WorkflowID: wf, Title: wf + " 的任务"}, a)
		_, err := e.st.UpdateWorkItem(e.ctx, it.ID, WorkItemPatch{Assignee: sp("wf-b3")}, a)
		ae, ok := err.(*AppError)
		if !ok || ae.Kind != ErrForbidden || ae.Message != want {
			t.Fatalf("%s 没有分支，拒绝文案应与升级前一致:\n got  %v\n want %s", wf, err, want)
		}
	}
	// 有分支的主会话，同一类拒绝文案以原文开头再补充原因。
	main := e.mainActor(t, WorkPolicyEnforce)
	own := mustCreate(t, e.st, WorkItemInput{WorkflowID: "wf-main", Title: "t"}, main)
	_, err := e.st.UpdateWorkItem(e.ctx, own.ID, WorkItemPatch{Assignee: sp("wf-other")}, main)
	if ae, ok := err.(*AppError); !ok || !strings.HasPrefix(ae.Message, want+"。") {
		t.Fatalf("有分支的主会话：文案应以原文开头 %q: %v", want, err)
	}
}

// 收回自己派出的指派不看关系状态：开放范围后来被改成只读、分支被结束，主会话仍能撤销 / 改派自己派出去的任务（但派不了新的）。
func TestMainRevokesOwnAssignmentAfterScopeChanges(t *testing.T) {
	e := newAssignEnv(t)
	main := e.mainActor(t, WorkPolicyEnforce)
	a := mustCreate(t, e.st, WorkItemInput{WorkflowID: "wf-main", Title: "A", Assignee: "wf-b1"}, main)
	b := mustCreate(t, e.st, WorkItemInput{WorkflowID: "wf-main", Title: "B", Assignee: "wf-b3"}, main)
	fresh := mustCreate(t, e.st, WorkItemInput{WorkflowID: "wf-main", Title: "新任务"}, main)

	if _, err := e.st.UpdateBranchSettings(e.ctx, "wf-b1", UpdateBranchInput{Preset: model.ExposePresetVisible}); err != nil {
		t.Fatal(err)
	}
	// 本来结束分支会把待办 / 进行中的任务退回；这里把 b3 的关系结束，但任务已交付待验收（review 不退回），再验证主会话还能处理。
	b3 := agentAs("wf-b3", WorkPolicyEnforce)
	mustUpdate(t, e.st, b.ID, WorkItemPatch{Claim: "accept"}, b3)
	mustUpdate(t, e.st, b.ID, WorkItemPatch{Claim: "deliver", Note: "交付"}, b3)
	if res, err := e.st.CloseBranch(e.ctx, "wf-b3", model.BranchCreatedByHuman); err != nil || len(res.Returned) != 0 {
		t.Fatalf("review 的任务结束分支时不退回: %v %+v", err, res)
	}
	main = e.mainActor(t, WorkPolicyEnforce)

	// 派不了新的：只读的分支、已结束的分支。
	e.forbidden(t, fresh.ID, WorkItemPatch{Assignee: sp("wf-b1")}, main, "只读")
	e.forbidden(t, fresh.ID, WorkItemPatch{Assignee: sp("wf-b3")}, main, "已经结束")
	// 收得回：撤销指派给「现在是只读」的分支的任务；review 的任务改派前要先验收或退回（状态前置条件，与角色无关）。
	res := mustUpdate(t, e.st, a.ID, WorkItemPatch{Assignee: sp("")}, main)
	if res.Item.AssigneeWorkflowID != "" || res.Item.AssignedByWorkflowID != "" || res.Item.Updates[0].Event != model.WorkEventUnassign {
		t.Fatalf("撤销应清空指派: %+v", res.Item)
	}
	res = mustUpdate(t, e.st, b.ID, WorkItemPatch{Assignee: sp("")}, main) // 已结束分支的 review 任务：撤销不看关系状态
	if res.Item.AssigneeWorkflowID != "" {
		t.Fatalf("已结束分支名下的任务也该收得回: %+v", res.Item)
	}
}

// audit 档：主会话派给自己的分支不再记违规；派给别人照旧记（执行了，但原因里带分支说明）。
func TestMainAssignAuditMode(t *testing.T) {
	e := newAssignEnv(t)
	main := e.mainActor(t, WorkPolicyAudit)
	ok := mustCreate(t, e.st, WorkItemInput{WorkflowID: "wf-main", Title: "给分支"}, main)
	if res := mustUpdate(t, e.st, ok.ID, WorkItemPatch{Assignee: sp("wf-b1")}, main); len(res.Violations) != 0 {
		t.Fatalf("派给自己的分支不该记违规: %v", res.Violations)
	}
	other := mustCreate(t, e.st, WorkItemInput{WorkflowID: "wf-main", Title: "给别人"}, main)
	res := mustUpdate(t, e.st, other.ID, WorkItemPatch{Assignee: sp("wf-other")}, main)
	if res.Item.AssigneeWorkflowID != "wf-other" {
		t.Fatalf("audit 档照常执行: %+v", res.Item)
	}
	if len(res.Violations) != 1 || !strings.Contains(res.Violations[0], "「wf-other」不是你的分支") {
		t.Fatalf("派给非分支应记一条违规并写明原因: %v", res.Violations)
	}
}

// 人、管理 Agent、off 档不受影响；独立板上的分支：权限放行，但同板检查照常拒绝，错误是 InvalidInput 而不是 Forbidden。
func TestBranchAssignDoesNotChangeOtherRoles(t *testing.T) {
	e := newAssignEnv(t)
	human := WorkActor{Kind: model.WorkActorHuman}
	main := e.mainActor(t, WorkPolicyEnforce)
	it := mustCreate(t, e.st, WorkItemInput{WorkflowID: "wf-main", Title: "t"}, main)

	// 人能派给任何同板工作流（含非分支）。
	if res := mustUpdate(t, e.st, it.ID, WorkItemPatch{Assignee: sp("wf-other")}, human); res.Item.AssigneeWorkflowID != "wf-other" {
		t.Fatalf("人派单: %+v", res.Item)
	}
	// 管理 Agent（即使它也有分支集合）能派给非分支。
	mgr := managerAs("wf-main", WorkPolicyEnforce)
	mgr.Branches = main.Branches
	if res := mustUpdate(t, e.st, it.ID, WorkItemPatch{Assignee: sp("wf-b2")}, mgr); res.Item.AssigneeWorkflowID != "wf-b2" {
		t.Fatalf("管理 Agent 派单: %+v", res.Item)
	}
	// off 档不做任何判定。
	off := e.mainActor(t, WorkPolicyOff)
	if res := mustUpdate(t, e.st, it.ID, WorkItemPatch{Assignee: sp("wf-other")}, off); res.Item.AssigneeWorkflowID != "wf-other" || len(res.Violations) != 0 {
		t.Fatalf("off 档: %+v %v", res.Item, res.Violations)
	}
	// 独立板上的分支：权限判定通过（创建时带承接者的路径先判权限、后查同板），但承接者不在这张板上，报的是 InvalidInput 而不是 Forbidden。
	_, err := e.st.CreateWorkItem(e.ctx, WorkItemInput{WorkflowID: "wf-main", Title: "t2", Assignee: "wf-solo"}, main)
	if ae, ok := err.(*AppError); !ok || ae.Kind != ErrInvalidInput || !strings.Contains(ae.Message, "不在这张任务板上") {
		t.Fatalf("独立板上的分支：应是同板检查报错: %v", err)
	}
	it2 := mustCreate(t, e.st, WorkItemInput{WorkflowID: "wf-main", Title: "t3"}, main)
	updateErr(t, e.st, it2.ID, WorkItemPatch{Assignee: sp("wf-solo")}, main, ErrInvalidInput, "不在这张任务板上")
}

// 规则矩阵：branchAssignCheck 的每一格。
func TestBranchAssignCheckTable(t *testing.T) {
	actor := WorkActor{WorkflowID: "m", Branches: map[string]WorkBranchScope{
		"b-open": {Active: true, Assign: true}, "b-read": {Active: true}, "b-closed": {}, "b-open2": {Active: true, Assign: true},
	}}
	item := func(creator, assignee, by string) *model.WorkItem {
		return &model.WorkItem{WorkflowID: creator, AssigneeWorkflowID: assignee, AssignedByWorkflowID: by}
	}
	cases := []struct {
		name          string
		before, after *model.WorkItem
		want          bool
		why           string
	}{
		{"自己的任务派给开放的分支", item("m", "", ""), item("m", "b-open", "m"), true, ""},
		{"别人的任务不能派", item("x", "", ""), item("x", "b-open", "m"), false, "主会话只能派自己创建的任务"},
		{"目标不是分支", item("m", "", ""), item("m", "x", "m"), false, "不是你的分支"},
		{"目标是自己", item("m", "", ""), item("m", "m", "m"), false, "不是你的分支"},
		{"目标只读", item("m", "", ""), item("m", "b-read", "m"), false, "只读"},
		{"目标已结束", item("m", "", ""), item("m", "b-closed", "m"), false, "已经结束"},
		{"撤销自己派给分支的指派", item("m", "b-open", "m"), item("m", "", ""), true, ""},
		{"撤销给只读分支的指派（不看目标状态）", item("m", "b-read", "m"), item("m", "", ""), true, ""},
		{"撤销给已结束分支的指派", item("m", "b-closed", "m"), item("m", "", ""), true, ""},
		{"撤销别人派的指派", item("m", "b-open", "mgr"), item("m", "", ""), false, "现有的指派不是你派给你的分支的"},
		{"撤销人派的指派（派发者为空）", item("m", "b-open", ""), item("m", "", ""), false, "现有的指派不是你派给你的分支的"},
		{"撤销给非分支的指派", item("m", "x", "m"), item("m", "", ""), false, "现有的指派不是你派给你的分支的"},
		{"改派到另一个开放的分支", item("m", "b-open", "m"), item("m", "b-open2", "m"), true, ""},
		{"改派到只读分支", item("m", "b-open", "m"), item("m", "b-read", "m"), false, "只读"},
		{"改派别人派的指派", item("m", "b-open", "mgr"), item("m", "b-open2", "m"), false, "现有的指派不是你派给你的分支的"},
		{"被拒绝后原样再派给同一个分支", item("m", "b-open", "m"), item("m", "b-open", "m"), true, ""},
		{"别人创建、主会话派给分支的任务也能撤销自己的指派", item("x", "b-open", "m"), item("x", "", ""), true, ""},
	}
	for _, c := range cases {
		got, why := actor.branchAssignCheck(c.before, c.after)
		if got != c.want || (c.why == "" && why != "") || (c.why != "" && !strings.Contains(why, c.why)) {
			t.Errorf("%s: got (%v, %q)，想要 (%v, 含 %q)", c.name, got, why, c.want, c.why)
		}
	}
	// 没有分支 / 没有工作流编号 / 入参为空：恒不放行且不给原因（文案不变）。
	for name, a := range map[string]WorkActor{"无分支": {WorkflowID: "m"}, "无编号": {Branches: actor.Branches}} {
		if ok, why := a.branchAssignCheck(item("m", "", ""), item("m", "b-open", "m")); ok || why != "" {
			t.Errorf("%s: 应 (false, \"\")，得到 (%v, %q)", name, ok, why)
		}
	}
	if ok, why := actor.branchAssignCheck(nil, item("m", "b-open", "m")); ok || why != "" {
		t.Errorf("before 为空: 应 (false, \"\")，得到 (%v, %q)", ok, why)
	}
}

// 结束分支：主会话派给它、还没交付的任务（待办 / 进行中）退回主会话；review / 已完成 / 别人派的 / 分支自己建的不动。
func TestCloseBranchReturnsAssignedTasks(t *testing.T) {
	e := newAssignEnv(t)
	main := e.mainActor(t, WorkPolicyEnforce)
	b1 := agentAs("wf-b1", WorkPolicyEnforce)
	mgr := managerAs("wf-mgr", WorkPolicyEnforce)

	mk := func(title string) *model.WorkItem {
		return mustCreate(t, e.st, WorkItemInput{WorkflowID: "wf-main", Title: title, Assignee: "wf-b1"}, main)
	}
	a := mk("A 待接取")
	b := mk("B 进行中")
	mustUpdate(t, e.st, b.ID, WorkItemPatch{Claim: "accept"}, b1)
	c := mk("C 待验收")
	mustUpdate(t, e.st, c.ID, WorkItemPatch{Claim: "accept"}, b1)
	mustUpdate(t, e.st, c.ID, WorkItemPatch{Claim: "deliver", Note: "交付"}, b1)
	d := mk("D 已完成")
	mustUpdate(t, e.st, d.ID, WorkItemPatch{Claim: "accept"}, b1)
	mustUpdate(t, e.st, d.ID, WorkItemPatch{Claim: "deliver", Note: "交付", Conclusion: sp("做完了，已验证")}, b1)
	mustUpdate(t, e.st, d.ID, WorkItemPatch{Claim: "accept_delivery"}, main)
	f := mustCreate(t, e.st, WorkItemInput{WorkflowID: "wf-mgr", Title: "F 管理 Agent 派的", Assignee: "wf-b1"}, mgr)
	g := mustCreate(t, e.st, WorkItemInput{WorkflowID: "wf-b1", Title: "G 分支自己建的"}, b1)
	h := mk("H 被拒绝的")
	mustUpdate(t, e.st, h.ID, WorkItemPatch{Claim: "decline", Note: "做不了"}, b1)

	rev := e.st.workRev.Load()
	res, err := e.st.CloseBranch(e.ctx, "wf-b1", "agent:wf-main")
	if err != nil || !res.Changed {
		t.Fatalf("结束分支: %v %+v", err, res)
	}
	if want := []int{a.Seq, b.Seq, h.Seq}; fmt.Sprint(res.Returned) != fmt.Sprint(want) {
		t.Fatalf("退回的任务 = %v，期望 %v（待办 / 进行中 / 被拒绝的待办；按创建顺序）", res.Returned, want)
	}
	if e.st.workRev.Load() == rev {
		t.Fatal("退回任务后要让 board: 缓存作废")
	}

	get := func(id uint) *model.WorkItem {
		it, err := e.st.GetWorkItem(e.ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		return it
	}
	for _, c := range []struct {
		it    *model.WorkItem
		state string
	}{{a, model.WorkStateTodo}, {b, model.WorkStateDoing}, {h, model.WorkStateTodo}} {
		got := get(c.it.ID)
		if got.AssigneeWorkflowID != "" || got.AssignedByWorkflowID != "" || got.ClaimState != "" || got.AssignedAt != nil || got.AcceptedAt != nil || got.State != c.state {
			t.Errorf("%s 应已退回主会话（无承接者、状态不变 %s）: %+v", c.it.Title, c.state, got)
		}
		u := got.Updates[0]
		if u.Event != model.WorkEventUnassign || u.ActorKind != model.WorkActorAgent || u.ActorWorkflowID != "wf-main" || !strings.Contains(u.Text, "分支结束") || !strings.Contains(u.Text, "wf-b1") {
			t.Errorf("%s 的时间线应记一条由主会话结束分支触发的退回: %+v", c.it.Title, u)
		}
		if got.WorkflowID != "wf-main" {
			t.Errorf("%s 的创建者不变: %s", c.it.Title, got.WorkflowID)
		}
	}
	for _, c := range []struct {
		name         string
		it           *model.WorkItem
		assignee, by string
		state        string
	}{
		{"C 待验收", c, "wf-b1", "wf-main", model.WorkStateReview},
		{"D 已完成", d, "wf-b1", "wf-main", model.WorkStateDone},
		{"F 管理 Agent 派的", f, "wf-b1", "wf-mgr", model.WorkStateTodo},
		{"G 分支自己建的", g, "", "", model.WorkStateTodo},
	} {
		got := get(c.it.ID)
		if got.AssigneeWorkflowID != c.assignee || got.AssignedByWorkflowID != c.by || got.State != c.state {
			t.Errorf("%s 不该被动: %+v", c.name, got)
		}
	}

	// 幂等：再结束一次什么都不退回，也不再通知。
	notices := len(inboxOf(t, e.st, "wf-b1", model.InboxKindBranch))
	res, err = e.st.CloseBranch(e.ctx, "wf-b1", "agent:wf-main")
	if err != nil || res.Changed || len(res.Returned) != 0 || len(inboxOf(t, e.st, "wf-b1", model.InboxKindBranch)) != notices {
		t.Fatalf("重复结束应是空操作: %v %+v", err, res)
	}

	// 分支收到的结束通知写明退回了哪些任务；主会话自己结束的，不给主会话发通知（结果在工具回执里）。
	rows := inboxOf(t, e.st, "wf-b1", model.InboxKindBranch)
	last := rows[len(rows)-1].ResponseText
	wantSeqs := fmt.Sprintf("#%d #%d #%d", a.Seq, b.Seq, h.Seq)
	if !strings.Contains(last, "3 个没做完的任务（"+wantSeqs+"）已退回主会话名下") || !strings.Contains(last, "已交付待验收的任务保留") {
		t.Fatalf("分支的结束通知应写明退回了哪些任务: %s", last)
	}
	for _, r := range inboxOf(t, e.st, "wf-main", model.InboxKindBranch) {
		if strings.Contains(r.ResponseText, "已退回你名下") {
			t.Fatalf("主会话自己结束的，不该再给自己发通知: %s", r.ResponseText)
		}
	}
}

// 用户结束分支：主会话收到的通知写明任务回到了自己名下；没有任务可退时两条通知与升级前逐字一致。
func TestCloseBranchNoticesByHuman(t *testing.T) {
	e := newAssignEnv(t)
	main := e.mainActor(t, WorkPolicyEnforce)
	x := mustCreate(t, e.st, WorkItemInput{WorkflowID: "wf-main", Title: "X", Assignee: "wf-b1"}, main)
	if res, err := e.st.CloseBranch(e.ctx, "wf-b1", model.BranchCreatedByHuman); err != nil || fmt.Sprint(res.Returned) != fmt.Sprint([]int{x.Seq}) {
		t.Fatalf("用户结束分支应退回任务: %v %+v", err, res)
	}
	rows := inboxOf(t, e.st, "wf-main", model.InboxKindBranch)
	if got := rows[len(rows)-1].ResponseText; !strings.Contains(got, "用户结束了分支") || !strings.Contains(got, fmt.Sprintf("1 个任务（#%d）已退回你名下", x.Seq)) {
		t.Fatalf("主会话的通知应写明退回: %s", got)
	}
	if it, _ := e.st.GetWorkItem(e.ctx, x.ID); it.Updates[0].ActorKind != model.WorkActorHuman || it.Updates[0].ActorWorkflowID != "" {
		t.Fatalf("用户结束的，时间线上的操作者是人: %+v", it.Updates[0])
	}

	// 没有任务可退：回执里 Returned 为空，两条通知与升级前一模一样。
	if res, err := e.st.CloseBranch(e.ctx, "wf-b3", model.BranchCreatedByHuman); err != nil || len(res.Returned) != 0 {
		t.Fatalf("没有任务可退: %v %+v", err, res)
	}
	rows = inboxOf(t, e.st, "wf-b3", model.InboxKindBranch)
	wantBranch := "用户结束了这个分支。请把手上的事收尾，并用 branch(action:\"say\") 向主会话汇报结果；之后不再有新任务派给你。"
	if got := rows[len(rows)-1].ResponseText; got != wantBranch {
		t.Fatalf("没有任务可退时，分支的通知应与升级前逐字一致:\n got  %s\n want %s", got, wantBranch)
	}
	rows = inboxOf(t, e.st, "wf-main", model.InboxKindBranch)
	if got := rows[len(rows)-1].ResponseText; strings.Contains(got, "退回") || !strings.Contains(got, "用户结束了分支") {
		t.Fatalf("没有任务可退时，主会话的通知不该提退回: %s", got)
	}
}

// 分支概况里的「派单」状态：开没开（开放范围里「任务」= 派单）、是否与主会话同在一张板上；已结束的分支没有这项。
func TestBranchBriefAssign(t *testing.T) {
	e := newAssignEnv(t)
	assignOf := func() map[string]*BranchAssign {
		t.Helper()
		links, err := e.st.ActiveBranchLinks(e.ctx, "wf-main")
		if err != nil {
			t.Fatal(err)
		}
		out := map[string]*BranchAssign{}
		for _, b := range e.st.BranchBriefs(e.ctx, links.AsMain) {
			out[b.BranchWorkflowID] = b.Assign
		}
		return out
	}
	want := map[string]BranchAssign{
		"wf-b1": {Open: true, BoardShared: true}, "wf-b2": {Open: false, BoardShared: true},
		"wf-b3": {Open: true, BoardShared: true}, "wf-solo": {Open: true, BoardShared: false},
	}
	got := assignOf()
	if len(got) != len(want) {
		t.Fatalf("概况数 = %d，期望 %d: %+v", len(got), len(want), got)
	}
	for wf, w := range want {
		if got[wf] == nil || *got[wf] != w {
			t.Errorf("%s 的 assign = %+v，期望 %+v", wf, got[wf], w)
		}
	}
	raw, err := json.Marshal(got["wf-b1"])
	if err != nil || string(raw) != `{"open":true,"board_shared":true}` {
		t.Fatalf("JSON 形状: %s %v", raw, err)
	}

	// 开放范围改成只读：open 随之变假；同板与否不受影响。
	if _, err := e.st.UpdateBranchSettings(e.ctx, "wf-b1", UpdateBranchInput{Preset: model.ExposePresetVisible}); err != nil {
		t.Fatal(err)
	}
	if a := assignOf()["wf-b1"]; a == nil || a.Open || !a.BoardShared {
		t.Fatalf("改只读后 open 应为假、同板不变: %+v", a)
	}

	// 已结束的分支：没有 assign 字段（JSON 里也没有键）。
	res, err := e.st.CloseBranch(e.ctx, "wf-b3", model.BranchCreatedByHuman)
	if err != nil {
		t.Fatal(err)
	}
	closed := e.st.BranchBriefs(e.ctx, []model.WorkflowBranch{res.Branch})[0]
	if closed.Assign != nil {
		t.Fatalf("已结束的分支不该有 assign: %+v", closed.Assign)
	}
	if raw, _ := json.Marshal(closed); strings.Contains(string(raw), `"assign":`) {
		t.Fatalf("已结束的分支 JSON 里不该有 assign 键: %s", raw)
	}
}
