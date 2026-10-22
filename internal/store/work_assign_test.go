package store

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/teacat99/RelayMesh/v2/internal/model"
)

// 任务的指派 → 接取 → 交付 → 验收与任务级权限（#175 B2）：规则表 + 完整流程 + 策略三档。

func agentAs(wf string, policy WorkPolicy) WorkActor {
	return WorkActor{Kind: model.WorkActorAgent, WorkflowID: wf, Policy: policy}
}

func managerAs(wf string, policy WorkPolicy) WorkActor {
	a := agentAs(wf, policy)
	a.Manager = true
	return a
}

func sp(s string) *string { return &s }

func mustCreate(t *testing.T, st *Store, in WorkItemInput, actor WorkActor) *model.WorkItem {
	t.Helper()
	res, err := st.CreateWorkItem(context.Background(), in, actor)
	if err != nil {
		t.Fatalf("建任务失败: %v", err)
	}
	return res.Item
}

func mustUpdate(t *testing.T, st *Store, id uint, p WorkItemPatch, actor WorkActor) *WorkItemResult {
	t.Helper()
	res, err := st.UpdateWorkItem(context.Background(), id, p, actor)
	if err != nil {
		t.Fatalf("更新任务失败: %v", err)
	}
	return res
}

func updateErr(t *testing.T, st *Store, id uint, p WorkItemPatch, actor WorkActor, wantKind ErrorKind, wantSub string) {
	t.Helper()
	_, err := st.UpdateWorkItem(context.Background(), id, p, actor)
	if err == nil {
		t.Fatalf("应当被拒绝（%s，含「%s」），却成功了", wantKind, wantSub)
	}
	ae, ok := err.(*AppError)
	if !ok || ae.Kind != wantKind || !strings.Contains(ae.Message, wantSub) {
		t.Fatalf("错误不符：想要 %s 含「%s」，得到 %v", wantKind, wantSub, err)
	}
}

// 规则表：角色 × 操作，逐格核对设计稿 B2 第 4.2 节。
func TestWorkRolesAllowsTable(t *testing.T) {
	collab := &model.WorkItem{WorkflowID: "c", AssigneeWorkflowID: "a", AssignedByWorkflowID: "s", ClaimState: model.WorkClaimAccepted}
	offered := &model.WorkItem{WorkflowID: "c", AssigneeWorkflowID: "a", AssignedByWorkflowID: "s", ClaimState: model.WorkClaimOffered}
	declined := &model.WorkItem{WorkflowID: "c", AssigneeWorkflowID: "a", AssignedByWorkflowID: "s", ClaimState: model.WorkClaimDeclined}
	byHuman := &model.WorkItem{WorkflowID: "c", AssigneeWorkflowID: "a", AssignedByWorkflowID: "", ClaimState: model.WorkClaimAccepted}
	self := &model.WorkItem{WorkflowID: "c", AssigneeWorkflowID: "s", AssignedByWorkflowID: "s", ClaimState: model.WorkClaimAccepted}
	plain := &model.WorkItem{WorkflowID: "c"}

	type row struct {
		name string
		it   *model.WorkItem
		who  WorkActor
		// 按 workOpOrder：meta progress done assign claim review
		want [6]bool
	}
	human := WorkActor{Kind: model.WorkActorHuman}
	manager := managerAs("m", WorkPolicyEnforce)
	rows := []row{
		{"协作任务·创建者", collab, agentAs("c", WorkPolicyEnforce), [6]bool{true, true, false, false, false, false}},
		{"协作任务·承接者", collab, agentAs("a", WorkPolicyEnforce), [6]bool{false, true, false, false, true, false}},
		{"协作任务·派发者", collab, agentAs("s", WorkPolicyEnforce), [6]bool{false, true, true, false, false, true}},
		{"协作任务·其他人", collab, agentAs("o", WorkPolicyEnforce), [6]bool{}},
		{"协作任务·人类", collab, human, [6]bool{true, true, true, true, true, true}},
		{"协作任务·管理者", collab, manager, [6]bool{true, true, true, true, true, true}},
		{"待接取·承接者可接取", offered, agentAs("a", WorkPolicyEnforce), [6]bool{false, true, false, false, true, false}},
		{"已拒绝·原承接者不再是承接者", declined, agentAs("a", WorkPolicyEnforce), [6]bool{}},
		{"已拒绝·创建者仍能写自己的", declined, agentAs("c", WorkPolicyEnforce), [6]bool{true, true, false, false, false, false}},
		{"人派的·承接者不能完成", byHuman, agentAs("a", WorkPolicyEnforce), [6]bool{false, true, false, false, true, false}},
		{"人派的·创建者不是派发者", byHuman, agentAs("c", WorkPolicyEnforce), [6]bool{true, true, false, false, false, false}},
		{"自己接活·承接者能完成", self, agentAs("s", WorkPolicyEnforce), [6]bool{false, true, true, false, true, true}},
		{"自己接活·创建者能完成", self, agentAs("c", WorkPolicyEnforce), [6]bool{true, true, true, false, false, false}},
		{"自己接活·其他人", self, agentAs("o", WorkPolicyEnforce), [6]bool{}},
		{"无指派·创建者", plain, agentAs("c", WorkPolicyEnforce), [6]bool{true, true, true, false, false, false}},
		{"无指派·其他人", plain, agentAs("o", WorkPolicyEnforce), [6]bool{}},
		{"无指派·管理者", plain, manager, [6]bool{true, true, true, true, true, true}},
	}
	for _, r := range rows {
		roles := workRolesOf(r.who, r.it)
		for i, op := range workOpOrder {
			if got := roles.allows(op, r.it); got != r.want[i] {
				t.Errorf("%s：操作 #%d 应为 %v，得到 %v", r.name, i, r.want[i], got)
			}
		}
	}
}

func TestIsCollaborative(t *testing.T) {
	cases := []struct {
		assignee, by string
		want         bool
	}{
		{"", "", false}, {"a", "s", true}, {"a", "", true}, {"s", "s", false},
	}
	for _, c := range cases {
		it := &model.WorkItem{AssigneeWorkflowID: c.assignee, AssignedByWorkflowID: c.by}
		if got := it.IsCollaborative(); got != c.want {
			t.Errorf("assignee=%q by=%q 应为 %v", c.assignee, c.by, c.want)
		}
	}
}

// 完整路径：管理者建任务并指派 → 承接者接取 → 进展 → 交付 → 派发者验收。
func TestAssignAcceptDeliverAcceptFlow(t *testing.T) {
	st, ctx, _ := boardFixture(t)
	mgr := managerAs("wf-1", WorkPolicyEnforce)
	dev := agentAs("wf-2", WorkPolicyEnforce)

	it := mustCreate(t, st, WorkItemInput{WorkflowID: "wf-1", Title: "接口改造", Assignee: "wf-2"}, mgr)
	if it.AssigneeWorkflowID != "wf-2" || it.AssignedByWorkflowID != "wf-1" || it.ClaimState != model.WorkClaimOffered ||
		it.AssignedAt == nil || it.AcceptedAt != nil || it.State != model.WorkStateTodo {
		t.Fatalf("创建时指派的字段不对: %+v", it)
	}
	got, _ := st.GetWorkItem(ctx, it.ID)
	if len(got.Updates) != 1 || got.Updates[0].Event != model.WorkEventAssign || got.Updates[0].ActorWorkflowID != "wf-1" ||
		!strings.Contains(got.Updates[0].Text, "指派给 wf-2") {
		t.Fatalf("创建那条时间线应是 assign 事件: %+v", got.Updates)
	}

	// 接取：offered → accepted，todo 自动转 doing。
	res := mustUpdate(t, st, it.ID, WorkItemPatch{Claim: "accept"}, dev)
	if res.Item.ClaimState != model.WorkClaimAccepted || res.Item.State != model.WorkStateDoing || res.Item.AcceptedAt == nil {
		t.Fatalf("接取后应是 accepted + doing: %+v", res.Item)
	}
	if u := res.Item.Updates[0]; u.Event != model.WorkEventAccept || u.ActorWorkflowID != "wf-2" || u.FromState != model.WorkStateTodo || u.ToState != model.WorkStateDoing {
		t.Fatalf("接取的时间线不对: %+v", u)
	}
	// 重复接取：状态前置条件不满足，说明当前接取状态。
	updateErr(t, st, it.ID, WorkItemPatch{Claim: "accept"}, dev, ErrInvalidInput, "已接取")

	// 进展：承接者能改进度，但不能改标题。
	mustUpdate(t, st, it.ID, WorkItemPatch{Progress: sp("做了一半"), Note: "前半段完成"}, dev)
	updateErr(t, st, it.ID, WorkItemPatch{Title: sp("偷偷改名")}, dev, ErrForbidden, "创建者")
	// 承接者不能自己验收：直接置 done 被拒，提示走交付。
	updateErr(t, st, it.ID, WorkItemPatch{State: sp("done"), Conclusion: sp("我自己验收")}, dev, ErrForbidden, "验收")

	// 交付：没有交付说明不行；有了就是 review + deliver 事件。
	updateErr(t, st, it.ID, WorkItemPatch{Claim: "deliver"}, dev, ErrInvalidInput, "交付说明")
	res = mustUpdate(t, st, it.ID, WorkItemPatch{Claim: "deliver", Note: "已提交 PR，e2e 全过", Conclusion: sp("接口改造完成，e2e 通过")}, dev)
	if res.Item.State != model.WorkStateReview || res.Item.Updates[0].Event != model.WorkEventDeliver {
		t.Fatalf("交付后应是 review + deliver: %+v", res.Item)
	}
	// 承接者不能验收；创建者（非派发者）也不能；派发者（管理者）验收通过。
	updateErr(t, st, it.ID, WorkItemPatch{Claim: "accept_delivery"}, dev, ErrForbidden, "验收")
	updateErr(t, st, it.ID, WorkItemPatch{State: sp("done")}, agentAs("o", WorkPolicyEnforce), ErrForbidden, "验收")
	res = mustUpdate(t, st, it.ID, WorkItemPatch{Claim: "accept_delivery"}, mgr)
	if res.Item.State != model.WorkStateDone || res.Item.ClosedAt == nil || res.Item.Updates[0].Event != model.WorkEventAcceptDelivery {
		t.Fatalf("验收通过后应是 done + accept_delivery: %+v", res.Item)
	}
	if len(res.Violations) != 0 {
		t.Fatalf("enforce 下不应有 Violations: %v", res.Violations)
	}
}

// 退回路径：验收不通过 → 回到 doing，必须写原因。
func TestRejectDeliveryNeedsReason(t *testing.T) {
	st, _, _ := boardFixture(t)
	mgr := managerAs("wf-1", WorkPolicyEnforce)
	dev := agentAs("wf-2", WorkPolicyEnforce)
	it := mustCreate(t, st, WorkItemInput{WorkflowID: "wf-1", Title: "T", Assignee: "wf-2"}, mgr)
	mustUpdate(t, st, it.ID, WorkItemPatch{Claim: "accept"}, dev)
	mustUpdate(t, st, it.ID, WorkItemPatch{Claim: "deliver", Note: "好了"}, dev)

	updateErr(t, st, it.ID, WorkItemPatch{Claim: "reject_delivery"}, mgr, ErrInvalidInput, "原因")
	res := mustUpdate(t, st, it.ID, WorkItemPatch{Claim: "reject_delivery", Note: "漏了边界用例"}, mgr)
	if res.Item.State != model.WorkStateDoing || res.Item.ClaimState != model.WorkClaimAccepted || res.Item.Updates[0].Event != model.WorkEventRejectDelivery {
		t.Fatalf("退回后应回到 doing 且仍是已接取: %+v", res.Item)
	}
	// 不在验证中的任务不能验收 / 退回。
	updateErr(t, st, it.ID, WorkItemPatch{Claim: "accept_delivery"}, mgr, ErrInvalidInput, "验证中")
}

// 拒绝：必须写理由；拒绝后原承接者不再有写权限；原样重派给同一个人回到待接取。
func TestDeclineAndReoffer(t *testing.T) {
	st, _, _ := boardFixture(t)
	mgr := managerAs("wf-1", WorkPolicyEnforce)
	dev := agentAs("wf-2", WorkPolicyEnforce)
	it := mustCreate(t, st, WorkItemInput{WorkflowID: "wf-1", Title: "T", Assignee: "wf-2"}, mgr)

	updateErr(t, st, it.ID, WorkItemPatch{Claim: "decline"}, dev, ErrInvalidInput, "理由")
	res := mustUpdate(t, st, it.ID, WorkItemPatch{Claim: "decline", Note: "手上排满了"}, dev)
	if res.Item.ClaimState != model.WorkClaimDeclined || res.Item.AssigneeWorkflowID != "wf-2" || res.Item.State != model.WorkStateTodo {
		t.Fatalf("拒绝后应保留承接者、状态不变: %+v", res.Item)
	}
	if u := res.Item.Updates[0]; u.Event != model.WorkEventDecline || !strings.Contains(u.Text, "手上排满了") {
		t.Fatalf("拒绝的时间线应带理由: %+v", u)
	}
	// 已拒绝：不能再接取，也不再有写权限。
	updateErr(t, st, it.ID, WorkItemPatch{Claim: "accept"}, dev, ErrInvalidInput, "已拒绝")
	updateErr(t, st, it.ID, WorkItemPatch{Progress: sp("偷做")}, dev, ErrForbidden, "只能读")
	// 原样重派给同一个人：回到待接取。
	res = mustUpdate(t, st, it.ID, WorkItemPatch{Assignee: sp("wf-2")}, mgr)
	if res.Item.ClaimState != model.WorkClaimOffered || res.Item.Updates[0].Event != model.WorkEventReassign {
		t.Fatalf("重派后应回到待接取: %+v", res.Item)
	}
	// 只有待接取才能拒绝：接取之后不能再拒绝（要换人由管理者改派）。
	mustUpdate(t, st, it.ID, WorkItemPatch{Claim: "accept"}, dev)
	updateErr(t, st, it.ID, WorkItemPatch{Claim: "decline", Note: "又不想做了"}, dev, ErrInvalidInput, "已接取")
}

// 指派的状态前置条件与承接者校验。
func TestAssignRules(t *testing.T) {
	st, _, _ := boardFixture(t)
	mgr := managerAs("wf-1", WorkPolicyEnforce)
	it := mustCreate(t, st, WorkItemInput{WorkflowID: "wf-1", Title: "T"}, mgr)

	// 承接者必须是同一张项目板上的工作流：项目外的、没有会话的都不行。
	for _, bad := range []string{"wf-solo", "wf-nodir", "never-seen"} {
		updateErr(t, st, it.ID, WorkItemPatch{Assignee: sp(bad)}, mgr, ErrInvalidInput, "不在这张任务板上")
	}
	// 独立板上的任务没有可指派的对象。
	solo := mustCreate(t, st, WorkItemInput{WorkflowID: "wf-solo", Title: "独"}, WorkActor{Kind: model.WorkActorHuman})
	updateErr(t, st, solo.ID, WorkItemPatch{Assignee: sp("wf-2")}, WorkActor{Kind: model.WorkActorHuman}, ErrInvalidInput, "独立任务板")
	// assignee 与 claim 不能同时给；claim 不能凭空用；未知 claim 报错并列出可用值。
	updateErr(t, st, it.ID, WorkItemPatch{Assignee: sp("wf-2"), Claim: "accept"}, mgr, ErrInvalidInput, "不能在同一次调用里一起给")
	updateErr(t, st, it.ID, WorkItemPatch{Claim: "accept"}, mgr, ErrInvalidInput, "还没有指派")
	updateErr(t, st, it.ID, WorkItemPatch{Claim: "bogus"}, mgr, ErrInvalidInput, "可用")

	// 指派给自己 = 直接 accepted，不经过 offered；状态不动。
	res := mustUpdate(t, st, it.ID, WorkItemPatch{Assignee: sp("wf-1")}, mgr)
	if res.Item.ClaimState != model.WorkClaimAccepted || res.Item.AcceptedAt == nil || res.Item.State != model.WorkStateTodo {
		t.Fatalf("自己承接应是 accepted: %+v", res.Item)
	}
	if res.Item.IsCollaborative() {
		t.Fatalf("自己给自己的任务不算协作指派: %+v", res.Item)
	}
	// 自己给自己的任务：自己能直接完成（保持现有规则）。
	done := mustUpdate(t, st, it.ID, WorkItemPatch{State: sp("done"), Conclusion: sp("自己做完自己验证")}, mgr)
	if done.Item.State != model.WorkStateDone {
		t.Fatalf("自己给自己的任务应能直接完成: %+v", done.Item)
	}
	// 已关闭的任务不能指派或改派；重新打开之后可以。
	updateErr(t, st, it.ID, WorkItemPatch{Assignee: sp("wf-2")}, mgr, ErrInvalidInput, "已关闭")
	mustUpdate(t, st, it.ID, WorkItemPatch{State: sp("doing")}, mgr)

	// 改派：doing 的任务换承接者回到待办；撤销清空全部指派字段；验证中的任务不能指派。
	res = mustUpdate(t, st, it.ID, WorkItemPatch{Assignee: sp("wf-2")}, mgr)
	if res.Item.ClaimState != model.WorkClaimOffered || res.Item.State != model.WorkStateTodo || res.Item.Updates[0].Event != model.WorkEventReassign {
		t.Fatalf("改派给别人应回到待办 + offered: %+v", res.Item)
	}
	res = mustUpdate(t, st, it.ID, WorkItemPatch{Assignee: sp("")}, mgr)
	if res.Item.AssigneeWorkflowID != "" || res.Item.AssignedByWorkflowID != "" || res.Item.ClaimState != "" || res.Item.AssignedAt != nil ||
		res.Item.Updates[0].Event != model.WorkEventUnassign {
		t.Fatalf("撤销应清空指派字段: %+v", res.Item)
	}
	mustUpdate(t, st, it.ID, WorkItemPatch{State: sp("review")}, mgr)
	updateErr(t, st, it.ID, WorkItemPatch{Assignee: sp("wf-2")}, mgr, ErrInvalidInput, "验证中")
	// 没有变化的指派是空操作。
	mustUpdate(t, st, it.ID, WorkItemPatch{State: sp("todo")}, mgr)
	mustUpdate(t, st, it.ID, WorkItemPatch{Assignee: sp("wf-2")}, mgr)
	if res := mustUpdate(t, st, it.ID, WorkItemPatch{Assignee: sp("wf-2")}, mgr); res.Changed {
		t.Fatal("重复指派给同一个待接取的承接者应是空操作")
	}
}

// 隐式协作事件：承接者直接改状态也算接取 / 交付；派发者改状态也算验收 / 退回。
func TestImplicitClaimEvents(t *testing.T) {
	st, _, _ := boardFixture(t)
	mgr := managerAs("wf-1", WorkPolicyEnforce)
	dev := agentAs("wf-2", WorkPolicyEnforce)

	it := mustCreate(t, st, WorkItemInput{WorkflowID: "wf-1", Title: "T", Assignee: "wf-2"}, mgr)
	// 承接者直接把状态改成 doing = 隐式接取。
	res := mustUpdate(t, st, it.ID, WorkItemPatch{State: sp("doing")}, dev)
	if res.Item.ClaimState != model.WorkClaimAccepted || res.Item.Updates[0].Event != model.WorkEventAccept {
		t.Fatalf("改成 doing 应隐式接取: %+v", res.Item)
	}
	// 把状态改成 review = 交付。
	res = mustUpdate(t, st, it.ID, WorkItemPatch{State: sp("review"), Progress: sp("做完了")}, dev)
	if res.Item.Updates[0].Event != model.WorkEventDeliver {
		t.Fatalf("改成 review 应是交付: %+v", res.Item.Updates[0])
	}
	// 派发者把 review 改回 doing = 退回；再交付、再置 done = 验收。
	res = mustUpdate(t, st, it.ID, WorkItemPatch{State: sp("doing"), Note: "再看看边界"}, mgr)
	if res.Item.Updates[0].Event != model.WorkEventRejectDelivery {
		t.Fatalf("派发者改回 doing 应是退回: %+v", res.Item.Updates[0])
	}
	mustUpdate(t, st, it.ID, WorkItemPatch{State: sp("review")}, dev)
	res = mustUpdate(t, st, it.ID, WorkItemPatch{State: sp("done"), Conclusion: sp("改造完成，回归通过")}, mgr)
	if res.Item.Updates[0].Event != model.WorkEventAcceptDelivery {
		t.Fatalf("派发者置 done 应是验收通过: %+v", res.Item.Updates[0])
	}

	// 待接取的任务，承接者只写进展（不改状态）也算隐式接取。
	it2 := mustCreate(t, st, WorkItemInput{WorkflowID: "wf-1", Title: "T2", Assignee: "wf-2"}, mgr)
	res = mustUpdate(t, st, it2.ID, WorkItemPatch{Progress: sp("先看了代码")}, dev)
	if res.Item.ClaimState != model.WorkClaimAccepted || res.Item.Updates[0].Event != model.WorkEventAccept {
		t.Fatalf("写进展应隐式接取: %+v", res.Item)
	}
}

// 人在 Web 上派的任务：没有 Agent 派发者；承接者交付后只有人 / 管理者能验收。
func TestHumanAssignsTask(t *testing.T) {
	st, _, _ := boardFixture(t)
	human := WorkActor{Kind: model.WorkActorHuman}
	dev := agentAs("wf-2", WorkPolicyEnforce)
	it := mustCreate(t, st, WorkItemInput{WorkflowID: "wf-1", Title: "人派的"}, human)

	res := mustUpdate(t, st, it.ID, WorkItemPatch{Assignee: sp("wf-2"), Note: "麻烦你做"}, human)
	if res.Item.AssigneeWorkflowID != "wf-2" || res.Item.AssignedByWorkflowID != "" || res.Item.ClaimState != model.WorkClaimOffered || !res.Item.IsCollaborative() {
		t.Fatalf("人派的任务应没有 Agent 派发者且算协作指派: %+v", res.Item)
	}
	if u := res.Item.Updates[0]; u.ActorKind != model.WorkActorHuman || u.ActorWorkflowID != "" || !strings.Contains(u.Text, "麻烦你做") {
		t.Fatalf("时间线应记人 + 备注: %+v", u)
	}
	mustUpdate(t, st, it.ID, WorkItemPatch{Claim: "accept"}, dev)
	mustUpdate(t, st, it.ID, WorkItemPatch{Claim: "deliver", Note: "做好了"}, dev)
	updateErr(t, st, it.ID, WorkItemPatch{State: sp("done"), Conclusion: sp("我验收")}, dev, ErrForbidden, "验收")
	// 创建任务的 wf-1（不是派发者、不是管理者）也不能验收。
	updateErr(t, st, it.ID, WorkItemPatch{State: sp("done"), Conclusion: sp("我验收")}, agentAs("wf-1", WorkPolicyEnforce), ErrForbidden, "验收")
	res = mustUpdate(t, st, it.ID, WorkItemPatch{State: sp("done")}, human)
	if res.Item.State != model.WorkStateDone || res.Item.Updates[0].Event != model.WorkEventAcceptDelivery {
		t.Fatalf("人验收通过应记 accept_delivery: %+v", res.Item)
	}
}

// 创建者不是派发者时：改回 doing 只是普通状态变化（没有协作事件），完成仍不行。
func TestCreatorWithoutAssignerRole(t *testing.T) {
	st, _, _ := boardFixture(t)
	mgr := managerAs("wf-1", WorkPolicyEnforce)
	dev := agentAs("wf-2", WorkPolicyEnforce)
	author := agentAs("wf-3", WorkPolicyEnforce)
	// wf-3 要在项目板上：给它一个会话目录。
	seedWorkflowDir(t, st, "wf-3", "devhost", "/home/user/a", time.Now())

	it := mustCreate(t, st, WorkItemInput{WorkflowID: "wf-3", Title: "作者的任务"}, author)
	mustUpdate(t, st, it.ID, WorkItemPatch{Assignee: sp("wf-2")}, mgr) // 管理者把别人创建的任务派出去
	mustUpdate(t, st, it.ID, WorkItemPatch{Claim: "accept"}, dev)
	mustUpdate(t, st, it.ID, WorkItemPatch{State: sp("review")}, dev)
	res := mustUpdate(t, st, it.ID, WorkItemPatch{State: sp("doing"), Note: "我看还差一点"}, author)
	if res.Item.Updates[0].Event != "" {
		t.Fatalf("创建者不是派发者，改回 doing 不应记协作事件: %+v", res.Item.Updates[0])
	}
	mustUpdate(t, st, it.ID, WorkItemPatch{State: sp("review")}, dev)
	updateErr(t, st, it.ID, WorkItemPatch{State: sp("done"), Conclusion: sp("我来验收")}, author, ErrForbidden, "验收")
	// 创建者仍能改自己任务的标题。
	mustUpdate(t, st, it.ID, WorkItemPatch{Title: sp("作者改名")}, author)
}

// 策略三档：enforce 拒绝；audit 照常执行并记 Violations；off 与人类不检查。
func TestWorkPolicyModes(t *testing.T) {
	st, _, _ := boardFixture(t)
	owner := mustCreate(t, st, WorkItemInput{WorkflowID: "wf-1", Title: "wf-1 的任务"}, agentAs("wf-1", WorkPolicyEnforce))

	// enforce：别人改它的任务被拒；指派被拒（普通 Agent 不能指派）。
	other := agentAs("wf-2", WorkPolicyEnforce)
	updateErr(t, st, owner.ID, WorkItemPatch{Progress: sp("乱写")}, other, ErrForbidden, "只能读")
	updateErr(t, st, owner.ID, WorkItemPatch{Assignee: sp("wf-2")}, agentAs("wf-1", WorkPolicyEnforce), ErrForbidden, "管理 Agent")
	if _, err := st.CreateWorkItem(context.Background(), WorkItemInput{WorkflowID: "wf-2", Title: "越权派活", Assignee: "wf-1"}, other); err == nil {
		t.Fatal("普通 Agent 创建时带承接者应被拒绝")
	}

	// audit：照常执行，Violations 里记「若启用会被拒绝」。
	audit := agentAs("wf-2", WorkPolicyAudit)
	res := mustUpdate(t, st, owner.ID, WorkItemPatch{Progress: sp("审计模式下乱写"), Title: sp("审计改名")}, audit)
	if !res.Changed || res.Item.Title != "审计改名" || res.Item.Progress != "审计模式下乱写" {
		t.Fatalf("audit 下应照常执行: %+v", res.Item)
	}
	if len(res.Violations) != 2 {
		t.Fatalf("应记 2 条违规（元信息 + 进展）: %v", res.Violations)
	}

	// off 与人类：不检查，也没有 Violations。
	for _, a := range []WorkActor{agentAs("wf-2", WorkPolicyOff), {Kind: model.WorkActorHuman}} {
		res := mustUpdate(t, st, owner.ID, WorkItemPatch{Progress: sp("不检查 " + a.Kind)}, a)
		if len(res.Violations) != 0 {
			t.Fatalf("%s 不应有违规记录: %v", a.Kind, res.Violations)
		}
	}
	// 字段值没变不算涉及：别人把同样的标题再发一遍不触发判定。
	cur, _ := st.GetWorkItem(context.Background(), owner.ID)
	if _, err := st.UpdateWorkItem(context.Background(), owner.ID, WorkItemPatch{Title: sp(cur.Title)}, other); err != nil {
		t.Fatalf("没改任何值的写入不应被拒绝: %v", err)
	}
}

// 管理者能编辑所有任务、验收他人的交付；其他人的任务只读。
func TestManagerEditsEverything(t *testing.T) {
	st, _, _ := boardFixture(t)
	it := mustCreate(t, st, WorkItemInput{WorkflowID: "wf-2", Title: "别人的"}, agentAs("wf-2", WorkPolicyEnforce))
	mgr := managerAs("wf-1", WorkPolicyEnforce)
	res := mustUpdate(t, st, it.ID, WorkItemPatch{Title: sp("管理者改名"), Progress: sp("管理者写进展")}, mgr)
	if res.Item.Title != "管理者改名" {
		t.Fatalf("管理者应能改别人的任务: %+v", res.Item)
	}
	if res.Item.Updates[0].ActorWorkflowID != "wf-1" {
		t.Fatalf("时间线应记操作者的工作流: %+v", res.Item.Updates[0])
	}
}

// 时间线：智能体写入记工作流，人写入为空。
func TestTimelineActorWorkflow(t *testing.T) {
	st, ctx, _ := boardFixture(t)
	it := mustCreate(t, st, WorkItemInput{WorkflowID: "wf-1", Title: "T"}, agentAs("wf-1", WorkPolicyOff))
	mustUpdate(t, st, it.ID, WorkItemPatch{Note: "人写的一句"}, WorkActor{Kind: model.WorkActorHuman})
	got, _ := st.GetWorkItem(ctx, it.ID)
	if len(got.Updates) != 2 || got.Updates[0].ActorWorkflowID != "" || got.Updates[1].ActorWorkflowID != "wf-1" {
		t.Fatalf("时间线操作者不对: %+v", got.Updates)
	}
}

// 列表过滤：按承接者 / 未指派 / 派发者。
func TestListFilterByAssignee(t *testing.T) {
	st, ctx, _ := boardFixture(t)
	mgr := managerAs("wf-1", WorkPolicyOff)
	a := mustCreate(t, st, WorkItemInput{WorkflowID: "wf-1", Title: "派给 wf-2", Assignee: "wf-2"}, mgr)
	mustCreate(t, st, WorkItemInput{WorkflowID: "wf-1", Title: "没派"}, mgr)
	mustCreate(t, st, WorkItemInput{WorkflowID: "wf-1", Title: "自己接", Assignee: "wf-1"}, mgr)

	list := func(f WorkItemFilter) []model.WorkItem {
		f.WorkflowID = "wf-1"
		items, err := st.ListWorkItems(ctx, f)
		if err != nil {
			t.Fatal(err)
		}
		return items
	}
	if got := list(WorkItemFilter{Assignee: "wf-2"}); len(got) != 1 || got[0].ID != a.ID {
		t.Fatalf("按承接者过滤: %+v", got)
	}
	if got := list(WorkItemFilter{Assignee: "none"}); len(got) != 1 || got[0].Title != "没派" {
		t.Fatalf("未指派过滤: %+v", got)
	}
	if got := list(WorkItemFilter{AssignedBy: "wf-1"}); len(got) != 2 {
		t.Fatalf("派发者过滤应含派给别人与自己接的 2 条: %+v", got)
	}
	if got := list(WorkItemFilter{}); len(got) != 3 {
		t.Fatalf("不过滤应是 3 条: %d", len(got))
	}
}

// 升级：B2 之前的库（没有指派列与时间线的 actor / event 列）打开后自动加列，旧行取默认值，新写入照常。
func TestMigrationAddsAssignmentColumns(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")
	st, err := New(path)
	if err != nil {
		t.Fatal(err)
	}
	// 把新列删掉，还原成 B2 之前的表结构，再塞一条旧行。
	for _, q := range []string{
		"DROP INDEX IF EXISTS idx_work_items_assignee_workflow_id",
		"ALTER TABLE work_items DROP COLUMN assignee_workflow_id",
		"ALTER TABLE work_items DROP COLUMN assigned_by_workflow_id",
		"ALTER TABLE work_items DROP COLUMN claim_state",
		"ALTER TABLE work_items DROP COLUMN assigned_at",
		"ALTER TABLE work_items DROP COLUMN accepted_at",
		"ALTER TABLE work_item_updates DROP COLUMN actor_workflow_id",
		"ALTER TABLE work_item_updates DROP COLUMN event",
		"INSERT INTO work_items (workflow_id, board_key, seq, title, kind, state, created_at, updated_at) VALUES ('legacy', 'w:legacy', 1, '老任务', 'feature', 'doing', datetime('now'), datetime('now'))",
		"INSERT INTO work_item_updates (work_item_id, actor_kind, to_state, text, created_at) VALUES (1, 'agent', 'doing', '老进展', datetime('now'))",
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
	ctx := context.Background()
	got, err := st2.GetWorkItemBySeq(ctx, "legacy", 1)
	if err != nil || got.Title != "老任务" || got.AssigneeWorkflowID != "" || got.ClaimState != "" || got.AssignedAt != nil || got.IsCollaborative() {
		t.Fatalf("旧行应取默认值: %+v err=%v", got, err)
	}
	if len(got.Updates) != 1 || got.Updates[0].Text != "老进展" || got.Updates[0].Event != "" || got.Updates[0].ActorWorkflowID != "" {
		t.Fatalf("旧时间线应取默认值: %+v", got.Updates)
	}
	// 新写入照常：指派、接取。
	if _, err := st2.CreateWorkItem(ctx, WorkItemInput{WorkflowID: "legacy", Title: "新任务"}, WorkActor{Kind: model.WorkActorHuman}); err != nil {
		t.Fatalf("升级后建任务失败: %v", err)
	}
	if res, err := st2.UpdateWorkItem(ctx, got.ID, WorkItemPatch{Note: "升级后还能写"}, agentAs("legacy", WorkPolicyOff)); err != nil || res.Item.Updates[0].ActorWorkflowID != "legacy" {
		t.Fatalf("升级后更新旧任务失败: %+v err=%v", res, err)
	}
}

// 旧数据兼容：没有任何指派字段的任务，行为与 B2 之前完全一致（普通 Agent 在 off 策略下可改、可完成）。
func TestUnassignedTasksUnchangedBehavior(t *testing.T) {
	st, _, _ := boardFixture(t)
	dev := agentAs("wf-1", WorkPolicyOff)
	it := mustCreate(t, st, WorkItemInput{WorkflowID: "wf-1", Title: "老样子"}, dev)
	mustUpdate(t, st, it.ID, WorkItemPatch{State: sp("review")}, dev)
	res := mustUpdate(t, st, it.ID, WorkItemPatch{State: sp("done"), Conclusion: sp("验证过了")}, dev)
	if res.Item.State != model.WorkStateDone || res.Item.Updates[0].Event != "" {
		t.Fatalf("无指派的任务不应有协作事件: %+v", res.Item.Updates[0])
	}
}

// 详情里「指派」下拉的候选（#175 B2b）：项目板 = 项目里的全部工作流 + 已在板上建过任务的；独立板没有候选。
func TestBoardWorkflowIDs(t *testing.T) {
	st, ctx, a := boardFixture(t)
	board := ProjectBoardKey(a.ID)

	// 还没有任何任务：项目里的两个工作流都是候选，项目外的不是。
	ids, err := st.BoardWorkflowIDs(ctx, board)
	if err != nil || strings.Join(ids, ",") != "wf-1,wf-2" {
		t.Fatalf("项目板候选应是 wf-1,wf-2: %v err=%v", ids, err)
	}

	// 会话目录后来被移出项目、但已在板上建过任务的工作流仍然是候选（粘性归板，checkAssignee 也认）。
	boardTask(t, st, "wf-1", "甲")
	seedWorkflowDir(t, st, "wf-1", "devhost", "/tmp/moved-away", time.Now().Add(time.Minute))
	ids, err = st.BoardWorkflowIDs(ctx, board)
	if err != nil || strings.Join(ids, ",") != "wf-1,wf-2" {
		t.Fatalf("建过任务的 wf-1 即使目录被移走也应在候选里: %v err=%v", ids, err)
	}
	if _, err := st.CreateWorkItem(ctx, WorkItemInput{WorkflowID: "wf-2", Title: "乙", Assignee: "wf-1"}, WorkActor{Kind: model.WorkActorHuman}); err != nil {
		t.Fatalf("候选里的工作流应能被指派: %v", err)
	}

	// 独立板与非法 key：没有可指派的对象。
	for _, key := range []string{WorkflowBoardKey("wf-solo"), "garbage", ""} {
		got, err := st.BoardWorkflowIDs(ctx, key)
		if err != nil || len(got) != 0 {
			t.Fatalf("%q 不应有候选: %v err=%v", key, got, err)
		}
	}
}
