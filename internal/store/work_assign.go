package store

import (
	"context"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/teacat99/RelayMesh/v2/internal/model"
)

// 任务的指派 → 接取 → 交付 → 验收，以及任务级权限（#175 B2，设计稿 design/shared-task-board-b2.md 第 4 节）。
//
// 这个文件是这套规则的唯一真相：REST、MCP、批量写入都经 CreateWorkItem / UpdateWorkItem 走到这里，
// 入口层只负责把参数翻译过来，不各自再写一遍判定。分两层：
//
//   - 状态前置条件（永远检查，与策略无关）：接取 / 拒绝要求任务是待接取，验收 / 退回要求任务在验证中，
//     已关闭的任务不能指派……这些是数据本身的合法性；
//   - 权限（按策略检查）：谁能做——人类不受限；管理者（持有该项目任务板的授权）全部能做；
//     普通 Agent 只能写「自己的任务」（自己创建的，或指派给自己且没拒绝的）。策略来自任务所在项目的访问控制模式：
//     off 不检查，audit 照常执行并把「若启用会被拒绝」的原因记进结果，enforce 直接拒绝。
//
// 状态机不加新状态：有协作指派（派发者 ≠ 承接者）的任务停在 review，就是「已交付待验收」。

// WorkPolicy 是任务级权限的策略档位，零值 = 不检查。
type WorkPolicy int

const (
	WorkPolicyOff     WorkPolicy = iota // 不检查：人在 Web 上、项目没开访问控制、独立板
	WorkPolicyAudit                     // 违规照常执行，原因记进 WorkItemResult.Violations 交给调用层记审计
	WorkPolicyEnforce                   // 违规直接拒绝
)

// workOp 是权限判定的粒度：一次写入涉及哪几类操作，就逐类判定。
type workOp int

const (
	workOpMeta     workOp = iota // 改标题 / 类型 / 标签 / 描述 / 模块 / 父任务
	workOpProgress               // 改进展（阶段 / 进度 / 完成度 / 引用 / 结论 / 备注）与 todo / doing / review / shelved 之间的状态变化
	workOpDone                   // 置 done，或从 done 重开
	workOpAssign                 // 指派 / 改派 / 撤销指派 / 创建时带承接者
	workOpClaim                  // 接取 / 拒绝 / 交付：承接者的动作
	workOpReview                 // 验收通过 / 退回：派发者的动作
)

// workOpOrder 固定判定与提示的顺序，保证拒绝信息稳定。
var workOpOrder = []workOp{workOpMeta, workOpProgress, workOpDone, workOpAssign, workOpClaim, workOpReview}

// workRoles 是操作者相对某条任务的角色。
type workRoles struct {
	human    bool // Web 上已登录的管理员
	manager  bool // 持有该项目任务板的管理授权（由 MCP 层按授权表判定后填进 WorkActor）
	creator  bool // 任务是它的工作流创建的
	assignee bool // 任务指派给它的工作流，且没有拒绝（offered / accepted）
	assigner bool // 任务是它的工作流派的（空的派发者 = 人派的，没有 Agent 派发者）
}

func workRolesOf(a WorkActor, it *model.WorkItem) workRoles {
	r := workRoles{human: !a.isAgent(), manager: a.Manager}
	wf := strings.TrimSpace(a.WorkflowID)
	if wf == "" {
		return r
	}
	r.creator = it.WorkflowID == wf
	r.assignee = it.AssigneeWorkflowID == wf && (it.ClaimState == model.WorkClaimOffered || it.ClaimState == model.WorkClaimAccepted)
	r.assigner = it.AssigneeWorkflowID != "" && it.AssignedByWorkflowID == wf
	return r
}

// allows 判定某类操作是否被允许（设计稿 B2 第 4.2 节的「操作 × 角色」表）。
func (r workRoles) allows(op workOp, it *model.WorkItem) bool {
	if r.human || r.manager {
		return true
	}
	switch op {
	case workOpMeta:
		return r.creator
	case workOpProgress:
		return r.creator || r.assignee || r.assigner
	case workOpDone:
		if it.IsCollaborative() {
			return r.assigner // 有协作指派：完成 = 验收，只能由派发者（或管理者 / 人）
		}
		return r.creator || r.assignee
	case workOpClaim:
		return r.assignee
	case workOpReview:
		return r.assigner
	}
	return false // workOpAssign：普通 Agent 不能指派别人
}

// workWho 把工作流编号写成给人看的一小段，空串 = 人。
func workWho(wf string) string {
	if wf == "" {
		return "人（Web）"
	}
	return "工作流 " + wf
}

// denyMessage 写给智能体看：谁能做这件事、它该怎么做。
func denyMessage(op workOp, it *model.WorkItem) string {
	switch op {
	case workOpMeta:
		return "这条任务不是你的工作流创建的：标题 / 类型 / 标签 / 描述 / 模块 / 父任务只有创建者或管理 Agent 能改。需要改动请联系创建者（" + workWho(it.WorkflowID) + "）或管理 Agent"
	case workOpProgress:
		if it.AssigneeWorkflowID != "" {
			return "这条任务不是你的工作流创建的，也不由你承接（承接者：" + it.AssigneeWorkflowID + "，" + ClaimStateLabel(it.ClaimState) + "）：只能读，不能改进展或状态"
		}
		return "这条任务不是你的工作流创建的、也没有指派给你：只能读，不能改进展或状态（创建者：" + workWho(it.WorkflowID) + "）"
	case workOpDone:
		if it.IsCollaborative() {
			return "这条任务由 " + workWho(it.AssignedByWorkflowID) + " 指派给 " + workWho(it.AssigneeWorkflowID) + "：完成要由派发者（或管理 Agent / 人）验收。承接者请把状态改为 review（或 claim=deliver）并写交付说明"
		}
		return "这条任务不是你的工作流创建的、也没有指派给你：不能置为完成或重新打开（创建者：" + workWho(it.WorkflowID) + "）"
	case workOpAssign:
		return "指派 / 改派 / 撤销指派只有管理 Agent 或人能做；普通 Agent 只能写自己的任务（由用户在项目页的「任务管理」里指定管理 Agent）"
	case workOpClaim:
		who := "还没有承接者"
		if it.AssigneeWorkflowID != "" {
			who = "承接者是 " + workWho(it.AssigneeWorkflowID)
		}
		return "只有承接者能接取 / 拒绝 / 交付（" + who + "）"
	case workOpReview:
		return "验收（通过 / 退回）只有派发者、管理 Agent 或人能做（派发者：" + workWho(it.AssignedByWorkflowID) + "）"
	}
	return "没有权限"
}

// enabled 表示这个操作者要不要做任务级判定：只对智能体，且策略不是 off。
func (a WorkActor) enabled() bool {
	return a.isAgent() && a.Policy != WorkPolicyOff
}

// guard 按策略处理一类操作：允许就放行；不允许时 enforce 返回拒绝，audit 记入 res.Violations 后放行。
// it 必须是这次写入之前的任务（角色相对「改动前」判定，否则撤销指派之类的操作会把自己的权利改没）。
func (a WorkActor) guard(it *model.WorkItem, op workOp, res *WorkItemResult) error {
	if !a.enabled() {
		return nil
	}
	if workRolesOf(a, it).allows(op, it) {
		return nil
	}
	msg := denyMessage(op, it)
	if a.Policy == WorkPolicyEnforce {
		return NewForbiddenError(msg)
	}
	for _, v := range res.Violations {
		if v == msg {
			return nil
		}
	}
	res.Violations = append(res.Violations, msg)
	return nil
}

// guardAll 对一组操作逐类判定，顺序固定。
func (a WorkActor) guardAll(it *model.WorkItem, needs map[workOp]bool, res *WorkItemResult) error {
	for _, op := range workOpOrder {
		if needs[op] {
			if err := a.guard(it, op, res); err != nil {
				return err
			}
		}
	}
	return nil
}

// ---------------------------------------------------------------- claim 参数

var workClaimAliases = map[string]string{
	"accept": "accept", "接取": "accept", "接受": "accept",
	"decline": "decline", "拒绝": "decline",
	"deliver": "deliver", "交付": "deliver",
	"accept_delivery": "accept_delivery", "approve": "accept_delivery", "验收": "accept_delivery", "验收通过": "accept_delivery",
	"reject_delivery": "reject_delivery", "reject": "reject_delivery", "退回": "reject_delivery",
}

// normalizeClaim 规整 claim 参数：空串 = 没有；无法识别时报错并列出可用值。
func normalizeClaim(raw string) (string, error) {
	t := strings.ToLower(strings.TrimSpace(raw))
	if t == "" {
		return "", nil
	}
	if c, ok := workClaimAliases[t]; ok {
		return c, nil
	}
	return "", NewInvalidInputError(fmt.Sprintf("未知的 claim「%s」，可用：%s", raw, strings.Join(model.WorkClaims, " / ")))
}

// ClaimStateLabel 是接取状态的中文名；空串 = 未指派。
func ClaimStateLabel(state string) string {
	switch state {
	case model.WorkClaimOffered:
		return "待接取"
	case model.WorkClaimAccepted:
		return "已接取"
	case model.WorkClaimDeclined:
		return "已拒绝"
	}
	return "未指派"
}

// ---------------------------------------------------------------- 指派与 claim 的应用

// workEvent 是一次协作动作在时间线上的记录：name 是事件类型，text 是可读的一句话（不含备注）。
type workEvent struct {
	name string
	text string
}

// eventText 把事件的一句话和备注拼成时间线上的正文。
func eventText(ev *workEvent, note string) string {
	if note == "" {
		return ev.text
	}
	return ev.text + "：" + note
}

// workAssignCtx 是一次指派 / claim 处理需要的上下文；item 与 patch 会被改写（claim 会换算成状态变化）。
type workAssignCtx struct {
	ctx   context.Context
	tx    *gorm.DB
	item  *model.WorkItem
	patch *WorkItemPatch
	actor WorkActor
	note  string
	now   time.Time
	// board 返回任务所在的板（需要时才解析一次）。
	board func() (string, error)
}

// checkAssignee 校验承接者：必须是同一张项目板上的工作流。独立板（工作流不属于任何项目）没有可指派的对象。
func checkAssignee(ctx context.Context, tx *gorm.DB, boardKey, assignee string) error {
	if runeLen(assignee) > 128 {
		return NewInvalidInputError("承接者的工作流编号过长")
	}
	if !isProjectBoard(boardKey) {
		return NewInvalidInputError("这条任务在独立任务板上（它的工作流不属于任何项目），没有可指派的对象；先把工作流的目录归入项目")
	}
	got, err := boardKeyOn(ctx, tx, assignee)
	if err != nil {
		return err
	}
	if got != boardKey {
		return NewInvalidInputError(fmt.Sprintf("工作流「%s」不在这张任务板上（它的会话目录不属于本项目，或还没有会话），不能指派给它", assignee))
	}
	return nil
}

// assignFields 写下一次指派的字段：承接者、派发者、接取状态与时间。
// 指派给自己（管理 Agent 自己接活）直接是 accepted，不经过 offered。
func assignFields(it *model.WorkItem, assignee string, actor WorkActor, now time.Time) (self bool) {
	it.AssigneeWorkflowID = assignee
	it.AssignedByWorkflowID = ""
	if actor.isAgent() {
		it.AssignedByWorkflowID = strings.TrimSpace(actor.WorkflowID)
	}
	at := now
	it.AssignedAt = &at
	it.AcceptedAt = nil
	self = it.AssignedByWorkflowID != "" && it.AssignedByWorkflowID == assignee
	if self {
		it.ClaimState = model.WorkClaimAccepted
		acc := now
		it.AcceptedAt = &acc
	} else {
		it.ClaimState = model.WorkClaimOffered
	}
	return self
}

// applyWorkAssignment 处理 patch 里的 assignee 与 claim，返回要记在时间线上的协作事件（没有变化返回 nil）。
// 它只检查状态前置条件；谁能做由调用方在整次写入完成后统一判定（guardAll）。
func applyWorkAssignment(c workAssignCtx) (*workEvent, error) {
	claim, err := normalizeClaim(c.patch.Claim)
	if err != nil {
		return nil, err
	}
	if c.patch.Assignee != nil && claim != "" {
		return nil, NewInvalidInputError("assignee 与 claim 不能在同一次调用里一起给：先指派，承接者再用 claim 接取")
	}
	if c.patch.Assignee != nil {
		return changeAssignee(c, strings.TrimSpace(*c.patch.Assignee))
	}
	if claim != "" {
		return applyClaim(c, claim)
	}
	return nil, nil
}

func changeAssignee(c workAssignCtx, next string) (*workEvent, error) {
	it := c.item
	prev := it.AssigneeWorkflowID
	if next == prev && !(next != "" && it.ClaimState == model.WorkClaimDeclined) {
		return nil, nil // 没有变化（被拒绝后原样再派给同一个人算重派，下面处理）
	}
	if next == "" {
		it.AssigneeWorkflowID, it.AssignedByWorkflowID, it.ClaimState = "", "", ""
		it.AssignedAt, it.AcceptedAt = nil, nil
		return &workEvent{model.WorkEventUnassign, "撤销指派（原承接者 " + prev + "）"}, nil
	}
	if model.IsClosedWorkState(it.State) {
		return nil, NewInvalidInputError("已关闭（已完成 / 已搁置）的任务不能指派或改派；先把它重新打开")
	}
	if it.State == model.WorkStateReview {
		return nil, NewInvalidInputError("验证中的任务不能指派或改派：先验收通过，或退回到进行中再指派")
	}
	boardKey, err := c.board()
	if err != nil {
		return nil, err
	}
	if err := checkAssignee(c.ctx, c.tx, boardKey, next); err != nil {
		return nil, err
	}
	self := assignFields(it, next, c.actor, c.now)
	text := "指派给 " + next
	switch {
	case prev != "" && prev != next:
		text = "改派给 " + next + "（原承接者 " + prev + "）"
	case prev == next:
		text = "重新指派给 " + next
	}
	if self {
		text += "（自己承接）"
	}
	// 正在做的任务换了承接者：新承接者从待办开始（自己接手不回退）。
	if it.State == model.WorkStateDoing && !self {
		it.State = model.WorkStateTodo
		text += "，状态回到待办"
	}
	name := model.WorkEventAssign
	if prev != "" {
		name = model.WorkEventReassign
	}
	return &workEvent{name, text}, nil
}

// wantState 让 claim 换算出的状态进入 patch；调用方同时给了矛盾的 state 就报错。
func wantState(p *WorkItemPatch, claim, want string) error {
	if p.State != nil {
		if got := model.NormalizeWorkState(*p.State); got != "" && got != want {
			return NewInvalidInputError(fmt.Sprintf("claim=%s 会把状态置为 %s，与你同时给的 state=%s 矛盾：去掉 state，或换一个 claim", claim, want, got))
		}
	}
	s := want
	p.State = &s
	return nil
}

func notEmptyPtr(p *string) bool { return p != nil && strings.TrimSpace(*p) != "" }

func applyClaim(c workAssignCtx, claim string) (*workEvent, error) {
	it := c.item
	if it.AssigneeWorkflowID == "" {
		return nil, NewInvalidInputError("这条任务还没有指派给任何工作流，不能 claim=" + claim + "；先由管理 Agent 或人指派")
	}
	mustOffered := func(verb string) error {
		if it.ClaimState != model.WorkClaimOffered {
			return NewInvalidInputError(fmt.Sprintf("只有待接取的指派才能%s，这条任务的接取状态是「%s」", verb, ClaimStateLabel(it.ClaimState)))
		}
		return nil
	}
	switch claim {
	case "accept":
		if err := mustOffered("接取"); err != nil {
			return nil, err
		}
		it.ClaimState = model.WorkClaimAccepted
		at := c.now
		it.AcceptedAt = &at
		if it.State == model.WorkStateTodo {
			if err := wantState(c.patch, claim, model.WorkStateDoing); err != nil {
				return nil, err
			}
		}
		return &workEvent{model.WorkEventAccept, "接取"}, nil
	case "decline":
		if err := mustOffered("拒绝"); err != nil {
			return nil, err
		}
		if c.note == "" {
			return nil, NewInvalidInputError("拒绝接取必须在 note 里写理由（派发者要据此重派）")
		}
		it.ClaimState = model.WorkClaimDeclined
		it.AcceptedAt = nil
		return &workEvent{model.WorkEventDecline, "拒绝接取"}, nil
	case "deliver":
		if it.ClaimState != model.WorkClaimOffered && it.ClaimState != model.WorkClaimAccepted {
			return nil, NewInvalidInputError(fmt.Sprintf("只有承接中的任务才能交付，这条任务的接取状态是「%s」", ClaimStateLabel(it.ClaimState)))
		}
		if model.IsClosedWorkState(it.State) {
			return nil, NewInvalidInputError("已关闭的任务不能交付")
		}
		if c.note == "" && !notEmptyPtr(c.patch.Progress) && !notEmptyPtr(c.patch.Conclusion) {
			return nil, NewInvalidInputError("交付要写交付说明：做了什么、怎么验证的、成果在哪（note / progress / conclusion 至少给一个）")
		}
		if it.ClaimState == model.WorkClaimOffered {
			it.ClaimState = model.WorkClaimAccepted
			at := c.now
			it.AcceptedAt = &at
		}
		if err := wantState(c.patch, claim, model.WorkStateReview); err != nil {
			return nil, err
		}
		return &workEvent{model.WorkEventDeliver, "交付，待验收"}, nil
	case "accept_delivery":
		if it.State != model.WorkStateReview {
			return nil, NewInvalidInputError(fmt.Sprintf("只有验证中（review）的任务才能验收，这条任务现在是「%s」", it.State))
		}
		if err := wantState(c.patch, claim, model.WorkStateDone); err != nil {
			return nil, err
		}
		return &workEvent{model.WorkEventAcceptDelivery, "验收通过"}, nil
	case "reject_delivery":
		if it.State != model.WorkStateReview {
			return nil, NewInvalidInputError(fmt.Sprintf("只有验证中（review）的任务才能退回，这条任务现在是「%s」", it.State))
		}
		if c.actor.isAgent() && c.note == "" {
			return nil, NewInvalidInputError("退回必须在 note 里写原因（承接者要据此返工）")
		}
		if err := wantState(c.patch, claim, model.WorkStateDoing); err != nil {
			return nil, err
		}
		return &workEvent{model.WorkEventRejectDelivery, "退回"}, nil
	}
	return nil, NewInvalidInputError(fmt.Sprintf("未知的 claim「%s」，可用：%s", claim, strings.Join(model.WorkClaims, " / ")))
}

// ---------------------------------------------------------------- 隐式协作事件与判定需求

// implicitWorkEvent 在没有显式 claim 时，从状态变化里识别协作动作（设计稿 B2 第 2 节第 3 点）：
//   - 承接者把待接取的任务改成 doing / review 或写了进展 = 隐式接取；承接者把任务置 review = 交付；
//   - 派发者（管理者 / 人）把有协作指派的 review 任务置 done = 验收通过，改回 doing / todo = 退回。
//
// 只有操作者确实担着对应角色时才算协作动作，否则只是一次普通的状态变化。
// 返回的 acceptNow 表示这次要把待接取的指派置为已接取。
func implicitWorkEvent(before *model.WorkItem, next string, roles workRoles, progressTouched bool) (ev *workEvent, acceptNow bool) {
	if before.AssigneeWorkflowID == "" {
		return nil, false
	}
	prev := before.State
	if roles.assignee {
		switch {
		case before.IsCollaborative() && prev != model.WorkStateReview && next == model.WorkStateReview:
			return &workEvent{model.WorkEventDeliver, "交付，待验收"}, before.ClaimState == model.WorkClaimOffered
		case before.ClaimState == model.WorkClaimOffered && (next == model.WorkStateDoing || next == model.WorkStateReview || progressTouched):
			return &workEvent{model.WorkEventAccept, "接取（随进展隐式生效）"}, true
		}
	}
	if before.IsCollaborative() && prev == model.WorkStateReview && (roles.assigner || roles.manager || roles.human) {
		switch next {
		case model.WorkStateDone:
			return &workEvent{model.WorkEventAcceptDelivery, "验收通过"}, false
		case model.WorkStateDoing, model.WorkStateTodo:
			return &workEvent{model.WorkEventRejectDelivery, "退回"}, false
		}
	}
	return nil, false
}

// workFieldsDiff 对比改动前后：改了「元信息」类字段（标题 / 类型 / 标签 / 描述 / 模块 / 父任务），
// 还是「进展」类字段（阶段 / 引用 / 进度 / 结论 / 完成度）。字段值没变不算涉及。
func workFieldsDiff(before, after *model.WorkItem) (meta, progress bool) {
	ptrEq := func(a, b *uint) bool {
		if a == nil || b == nil {
			return a == nil && b == nil
		}
		return *a == *b
	}
	meta = before.Title != after.Title || before.Kind != after.Kind || before.Description != after.Description ||
		!sameStrings(before.Labels, after.Labels) || !ptrEq(before.ModuleID, after.ModuleID) || !ptrEq(before.ParentID, after.ParentID)
	pctEq := (before.Percent == nil && after.Percent == nil) ||
		(before.Percent != nil && after.Percent != nil && *before.Percent == *after.Percent)
	progress = before.Stage != after.Stage || !sameStrings(before.Refs, after.Refs) || before.Progress != after.Progress ||
		before.Conclusion != after.Conclusion || !pctEq
	return meta, progress
}

// workNeeds 汇总这次写入涉及哪几类操作，供 guardAll 逐类判定。
func workNeeds(before, after *model.WorkItem, note string, ev *workEvent) map[workOp]bool {
	needs := map[workOp]bool{}
	meta, progress := workFieldsDiff(before, after)
	if meta {
		needs[workOpMeta] = true
	}
	doneTouched := (before.State == model.WorkStateDone) != (after.State == model.WorkStateDone)
	if progress || note != "" || (before.State != after.State && !doneTouched) {
		needs[workOpProgress] = true
	}
	if doneTouched {
		needs[workOpDone] = true
	}
	if ev != nil {
		switch ev.name {
		case model.WorkEventAssign, model.WorkEventReassign, model.WorkEventUnassign:
			needs[workOpAssign] = true
		case model.WorkEventAccept, model.WorkEventDecline, model.WorkEventDeliver:
			needs[workOpClaim] = true
		case model.WorkEventAcceptDelivery, model.WorkEventRejectDelivery:
			needs[workOpReview] = true
		}
	}
	return needs
}
