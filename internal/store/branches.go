package store

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/teacat99/RelayMesh/v2/internal/model"
)

// 分支会话（#236 P1）的数据层：关系的建立 / 关闭 / 解除、主会话与分支互发消息、用户发言的抄送、会话页的分支消息流、
// 主会话读分支历史。设计见 .cursor/planning/2026-10-24-236-p1-impl-plan.md。
//
// 所有建立关系的路径（网页新建、MCP 新建、迁移命令）都经过同一个 linkBranchTx，不变量只有这一份。

const (
	// MaxActiveBranches 是一个主会话最多同时有多少个活动分支。
	MaxActiveBranches = 8
	// BranchCreatePerHour 是一个主会话每小时最多新建多少个分支（迁移命令不受限）。
	BranchCreatePerHour = 5
	// BranchSayMaxRunes 是一条分支消息的字数上限。
	BranchSayMaxRunes = 4000
	// BranchSayPerHour 是同一个发送方每小时最多发多少条分支消息（防两个 Agent 互相回复形成环路）。
	BranchSayPerHour = 60
	// BranchUnreadCap 是同一发送方发给同一个接收方、对方还没读的分支消息最多多少条。
	BranchUnreadCap = 20
	// BranchCopyRunes 是抄送给主会话的用户发言摘要的字数上限。
	BranchCopyRunes = 200
	// BranchListMax 是关系列表一次最多返回多少行。
	BranchListMax = 500
	// BranchReadDefault / BranchReadMax 是主会话读分支历史时的默认与最大轮数。
	BranchReadDefault = 10
	BranchReadMax     = 20
	// BranchSummaryRunes 是「摘要」档里每一轮正文保留的字数。
	BranchSummaryRunes = 200
	// BranchFullRunes 是「全文」档里每一轮每段正文保留的字数上限（防止一次返回过大）。
	BranchFullRunes = 8000
)

// 分支相关的审计动作（只记双方工作流编号与字数，不记正文）。
const (
	AuditBranchLink     = "branch.link"
	AuditBranchUnlink   = "branch.unlink"
	AuditBranchClose    = "branch.close"
	AuditBranchSettings = "branch.settings"
	AuditBranchSay      = "branch.say"
)

// 会话页的角色。
const (
	BranchRoleMain   = "main"
	BranchRoleBranch = "branch"
	BranchRoleNone   = "none"
)

// LinkBranchInput 是建立一条分支关系的入参。
type LinkBranchInput struct {
	ParentWorkflowID string
	BranchWorkflowID string
	// CreatedBy：human / agent:<主会话工作流编号> / migrate。
	CreatedBy string
	// Expose 为空时按 CreatedBy 取默认预设：migrate → 仅可见，其余 → 协作。
	Expose *model.BranchExpose
	// CopyUserNotes 为空时取默认值 true。
	CopyUserNotes *bool
	// SkipRateLimit 为 true 时不受「每小时新建数」限制（迁移命令）。
	SkipRateLimit bool
}

// LinkBranchResult 是建立关系的结果。
type LinkBranchResult struct {
	Branch   model.WorkflowBranch `json:"branch"`
	Created  bool                 `json:"created"`  // 新写了一行
	Reopened bool                 `json:"reopened"` // 把已关闭的关系重新打开
}

// Noop 表示关系早已存在且生效，这次什么都没改。
func (r LinkBranchResult) Noop() bool { return !r.Created && !r.Reopened }

// errBranchDryRun 让预演的事务回滚。
var errBranchDryRun = errors.New("branch dry run")

func defaultBranchExpose(createdBy string) model.BranchExpose {
	name := model.ExposePresetCollab
	if createdBy == model.BranchCreatedByMigrate {
		name = model.ExposePresetVisible
	}
	e, _ := model.BranchExposePreset(name)
	return e
}

// workflowExistsTx 判断库里有没有这个工作流的任何会话行（库里没有 workflows 表，工作流就是会话行的聚合键）。
func workflowExistsTx(tx *gorm.DB, workflowID string) (bool, error) {
	var n int64
	if err := tx.Model(&model.FeedbackSession{}).Where("workflow_id = ?", workflowID).Limit(1).Count(&n).Error; err != nil {
		return false, err
	}
	return n > 0, nil
}

// linkBranchTx 在给定事务里校验全部不变量并写入关系；网页 / MCP / 迁移命令共用。
//
// 不变量：分支 ≠ 主会话；两个工作流都已存在；深度 1（主会话不能是别人的活动分支，分支不能是别人的活动主会话）；
// 一个分支只有一个主会话；一个主会话最多 MaxActiveBranches 个活动分支；非迁移的新建每小时最多 BranchCreatePerHour 个。
// 已关闭的关系再连同一个主会话 = 重新打开；连别的主会话 = 冲突（先 branch-unlink）。幂等：关系已生效时什么都不改。
func linkBranchTx(tx *gorm.DB, in LinkBranchInput, now time.Time) (*LinkBranchResult, error) {
	parent, branch := strings.TrimSpace(in.ParentWorkflowID), strings.TrimSpace(in.BranchWorkflowID)
	if parent == "" || branch == "" {
		return nil, NewInvalidInputError("主会话与分支的工作流编号都必填")
	}
	if parent == branch {
		return nil, NewInvalidInputError("分支不能是自己的主会话")
	}
	createdBy := strings.TrimSpace(in.CreatedBy)
	if !model.ValidBranchCreatedBy(createdBy) {
		return nil, NewInvalidInputError("created_by 只能是 human、migrate 或 agent:<工作流编号>")
	}
	expose := defaultBranchExpose(createdBy)
	if in.Expose != nil {
		expose = *in.Expose
	}
	if err := expose.Validate(); err != nil {
		return nil, NewInvalidInputError(err.Error())
	}
	copyNotes := true
	if in.CopyUserNotes != nil {
		copyNotes = *in.CopyUserNotes
	}

	for _, wf := range []string{parent, branch} {
		ok, err := workflowExistsTx(tx, wf)
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, NewNotFoundError(fmt.Sprintf("工作流 %q 不存在（库里没有它的任何会话行）", wf))
		}
	}

	var cur model.WorkflowBranch
	reopening := false
	err := tx.Where("branch_workflow_id = ?", branch).First(&cur).Error
	switch {
	case err == nil:
		if cur.ParentWorkflowID != parent {
			return nil, NewConflictError(fmt.Sprintf("工作流 %q 已经是 %q 的分支（状态 %s）；要换主会话，请先 branch-unlink", branch, cur.ParentWorkflowID, cur.Status), 0)
		}
		if cur.Active() {
			return &LinkBranchResult{Branch: cur}, nil
		}
		reopening = true
	case errors.Is(err, gorm.ErrRecordNotFound):
	default:
		return nil, err
	}

	var n int64
	if err := tx.Model(&model.WorkflowBranch{}).Where("branch_workflow_id = ? AND status = ?", parent, model.BranchStatusActive).Count(&n).Error; err != nil {
		return nil, err
	}
	if n > 0 {
		return nil, NewInvalidInputError(fmt.Sprintf("主会话 %q 本身就是别人的分支；分支只允许一层（分支不能再建分支）", parent))
	}
	if err := tx.Model(&model.WorkflowBranch{}).Where("parent_workflow_id = ? AND status = ?", branch, model.BranchStatusActive).Count(&n).Error; err != nil {
		return nil, err
	}
	if n > 0 {
		return nil, NewInvalidInputError(fmt.Sprintf("工作流 %q 自己已经有分支，不能再做别人的分支（只允许一层）", branch))
	}
	if err := tx.Model(&model.WorkflowBranch{}).Where("parent_workflow_id = ? AND status = ?", parent, model.BranchStatusActive).Count(&n).Error; err != nil {
		return nil, err
	}
	if n >= MaxActiveBranches {
		return nil, NewConflictError(fmt.Sprintf("主会话 %q 已经有 %d 个活动分支（上限 %d），请先结束不用的分支", parent, n, MaxActiveBranches), 0)
	}
	if !reopening && !in.SkipRateLimit && createdBy != model.BranchCreatedByMigrate {
		if err := tx.Model(&model.WorkflowBranch{}).
			Where("parent_workflow_id = ? AND created_by <> ? AND created_at >= ?", parent, model.BranchCreatedByMigrate, now.Add(-time.Hour)).
			Count(&n).Error; err != nil {
			return nil, err
		}
		if n >= BranchCreatePerHour {
			return nil, NewConflictError(fmt.Sprintf("主会话 %q 一小时内已经新建了 %d 个分支（上限 %d），请稍后再建", parent, n, BranchCreatePerHour), 0)
		}
	}

	if reopening {
		cur.Status, cur.ClosedAt = model.BranchStatusActive, nil
		if in.Expose != nil {
			cur.Expose = expose
		}
		if in.CopyUserNotes != nil {
			cur.CopyUserNotes = copyNotes
		}
		if err := tx.Save(&cur).Error; err != nil {
			return nil, err
		}
		return &LinkBranchResult{Branch: cur, Reopened: true}, nil
	}
	row := model.WorkflowBranch{
		BranchWorkflowID: branch, ParentWorkflowID: parent, CreatedBy: createdBy, Expose: expose,
		CopyUserNotes: copyNotes, Status: model.BranchStatusActive, CreatedAt: now,
	}
	if err := tx.Create(&row).Error; err != nil {
		return nil, err
	}
	return &LinkBranchResult{Branch: row, Created: true}, nil
}

// branchAuditActor 把 created_by / 操作者翻成审计的主体。
func branchAuditActor(by string) (kind, ref, label string) {
	switch {
	case by == model.BranchCreatedByHuman:
		return "human", "web", "网页用户"
	case by == model.BranchCreatedByMigrate:
		return "system", "branch-link", "迁移命令"
	case strings.HasPrefix(by, "agent:"):
		return "system", "workflow:" + strings.TrimPrefix(by, "agent:"), "主会话 Agent"
	case by == model.ConfirmEscalatedByTimeout:
		return "system", "branch-escalator", "超时升级"
	case by == model.ConfirmEscalatedBySystem:
		return "system", "branch", "系统（关系变化）"
	}
	return "system", by, ""
}

func (s *Store) auditBranch(ctx context.Context, action, by, parent, branch string, detail map[string]any) {
	kind, ref, label := branchAuditActor(by)
	_, _ = s.AddAuditEvent(ctx, AuditInput{
		ActorKind: kind, ActorRef: ref, ActorLabel: label, Action: action,
		ScopeKind: model.ScopeWorkflow, ScopeRef: parent, Target: branch, Detail: detail,
	})
}

// PlanBranchLink 只做预演：跑一遍与 LinkBranch 完全相同的校验，然后回滚，什么都不写。
func (s *Store) PlanBranchLink(ctx context.Context, in LinkBranchInput) (*LinkBranchResult, error) {
	var res *LinkBranchResult
	err := s.WithTx(ctx, func(tx *gorm.DB) error {
		r, err := linkBranchTx(tx, in, time.Now())
		if err != nil {
			return err
		}
		res = r
		return errBranchDryRun
	})
	if err != nil && !errors.Is(err, errBranchDryRun) {
		return nil, err
	}
	return res, nil
}

// LinkBranch 建立（或重新打开）一条分支关系，并写审计。
func (s *Store) LinkBranch(ctx context.Context, in LinkBranchInput) (*LinkBranchResult, error) {
	var res *LinkBranchResult
	err := s.WithTx(ctx, func(tx *gorm.DB) error {
		r, err := linkBranchTx(tx, in, time.Now())
		res = r
		return err
	})
	if err != nil {
		return nil, err
	}
	if !res.Noop() {
		s.auditBranch(ctx, AuditBranchLink, res.Branch.CreatedBy, res.Branch.ParentWorkflowID, res.Branch.BranchWorkflowID,
			map[string]any{"created": res.Created, "reopened": res.Reopened, "expose": res.Branch.Expose.PresetName(), "expose_values": res.Branch.Expose})
	}
	return res, nil
}

// UnlinkBranch 解除一条关系（删行，不管状态）；dryRun 时只返回将被删的那一行。关系不存在返回 NotFound。
func (s *Store) UnlinkBranch(ctx context.Context, branchWorkflowID string, dryRun bool, by string) (*model.WorkflowBranch, error) {
	branch := strings.TrimSpace(branchWorkflowID)
	var cur model.WorkflowBranch
	err := s.WithTx(ctx, func(tx *gorm.DB) error {
		if err := tx.Where("branch_workflow_id = ?", branch).First(&cur).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return NewNotFoundError(fmt.Sprintf("工作流 %q 不是任何会话的分支", branch))
			}
			return err
		}
		if dryRun {
			return errBranchDryRun
		}
		// 关系没了，主会话就没有权限再答这个分支的请示：已路由未答的立即转交给用户（运维命令里没有通知通道，用户在网页上会看到）。
		if _, err := unrouteBranchAsksTx(tx, cur.BranchWorkflowID, "分支与主会话的关系已被解除，请示改由你定夺", time.Now()); err != nil {
			return err
		}
		return tx.Delete(&model.WorkflowBranch{}, cur.ID).Error
	})
	if err != nil && !errors.Is(err, errBranchDryRun) {
		return nil, err
	}
	if !dryRun {
		s.auditBranch(ctx, AuditBranchUnlink, by, cur.ParentWorkflowID, cur.BranchWorkflowID, nil)
	}
	return &cur, nil
}

// CloseBranchResult 是结束分支的结果。
type CloseBranchResult struct {
	Branch  model.WorkflowBranch `json:"branch"`
	Changed bool                 `json:"changed"` // false = 早已结束
	// Escalated 是结束时被立即转交给用户的、原本路由给主会话的请示（调用方据此发「新请示」通知与刷新界面）。
	Escalated []model.ConfirmItem `json:"-"`
	// Returned 是结束时退回主会话名下的任务序号：主会话派给这个分支、还没交付的待办 / 进行中任务（#236 P5，见 returnBranchTasksTx）。
	Returned []int `json:"returned_tasks,omitempty"`
}

// returnBranchTasksTx 结束分支时把主会话派给它、还没交付的任务（待办 / 进行中）退回主会话：撤销指派，时间线记一条，任务序号返回。
// 只退「承接者是这个分支、派发者是它的主会话」的；人派的、别的 Agent 派的不动，已交付待验收（review）的也不动——
// 那时球在主会话手里（它是派发者，本来就有验收权），退回反而丢了承接者的交付说明的归属；已完成 / 搁置的更不动。
// 退回的任务状态不变（进展与结论都留在任务上），只是不再有承接者：它们回到创建者（主会话）名下，不再卡着这个分支自己的收尾闸。
// by 与 CloseBranch 的同名参数一致：human / agent:<工作流编号>，决定时间线上的操作者。
func returnBranchTasksTx(tx *gorm.DB, parent, branch, by string, now time.Time) ([]int, error) {
	var items []model.WorkItem
	if err := tx.Where("assignee_workflow_id = ? AND assigned_by_workflow_id = ? AND state IN ?",
		branch, parent, []string{model.WorkStateTodo, model.WorkStateDoing}).Order("id ASC").Find(&items).Error; err != nil {
		return nil, err
	}
	if len(items) == 0 {
		return nil, nil
	}
	kind, wfRef := model.WorkActorHuman, ""
	if b := strings.TrimSpace(by); strings.HasPrefix(b, "agent:") {
		kind, wfRef = model.WorkActorAgent, strings.TrimSpace(strings.TrimPrefix(b, "agent:"))
	}
	seqs := make([]int, 0, len(items))
	for i := range items {
		it := &items[i]
		prev := it.AssigneeWorkflowID
		it.AssigneeWorkflowID, it.AssignedByWorkflowID, it.ClaimState = "", "", ""
		it.AssignedAt, it.AcceptedAt = nil, nil
		it.UpdatedAt = now
		if err := tx.Save(it).Error; err != nil {
			return nil, err
		}
		u := &model.WorkItemUpdate{
			WorkItemID: it.ID, ActorKind: kind, ActorWorkflowID: wfRef, CreatedAt: now,
			Event: model.WorkEventUnassign, Text: "分支结束，任务退回主会话（原承接者 " + prev + "）",
		}
		if err := appendWorkUpdate(tx, u); err != nil {
			return nil, err
		}
		seqs = append(seqs, it.Seq)
	}
	return seqs, nil
}

// branchScopeMax 是 BranchScopeOf 最多读多少条关系（含已结束的）：一个主会话同时最多 MaxActiveBranches 个活动分支，已结束的历史会越攒越多。
// 读不到的老关系只影响「收回自己很久以前派出去的指派」，那时回到普通 Agent 的规则（失败即关闭）。
const branchScopeMax = 500

// BranchScopeOf 返回一个工作流作为主会话时与各个分支（含已结束的）在任务派单上的关系，键是分支工作流编号；没有分支返回 nil（#236 P5）。
// 入口层（MCP）在进入 store 的写入之前调用，结果放进 WorkActor.Branches：store 的事务里不能再回头查库。
func (s *Store) BranchScopeOf(ctx context.Context, parent string) (map[string]WorkBranchScope, error) {
	wf := strings.TrimSpace(parent)
	if wf == "" {
		return nil, nil
	}
	var rows []model.WorkflowBranch
	if err := s.db.WithContext(ctx).Where("parent_workflow_id = ?", wf).Order("id DESC").Limit(branchScopeMax).Find(&rows).Error; err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, nil
	}
	out := make(map[string]WorkBranchScope, len(rows))
	for _, r := range rows {
		active := r.Active()
		out[r.BranchWorkflowID] = WorkBranchScope{Active: active, Assign: active && r.Expose.Tasks == model.ExposeTasksAssign}
	}
	return out, nil
}

// CloseBranch 把分支关系置为 closed（保留行，历史消息与事件仍可看）；幂等。
// by：human / agent:<工作流编号>。结束时给「不是结束者」的那一边的收件箱各写一行系统消息，让对方的 Agent 知道。
func (s *Store) CloseBranch(ctx context.Context, branchWorkflowID, by string) (*CloseBranchResult, error) {
	branch := strings.TrimSpace(branchWorkflowID)
	var res CloseBranchResult
	now := time.Now()
	err := s.WithTx(ctx, func(tx *gorm.DB) error {
		if err := tx.Where("branch_workflow_id = ?", branch).First(&res.Branch).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return NewNotFoundError(fmt.Sprintf("工作流 %q 不是任何会话的分支", branch))
			}
			return err
		}
		if !res.Branch.Active() {
			return nil
		}
		res.Branch.Status, res.Branch.ClosedAt = model.BranchStatusClosed, &now
		if err := tx.Save(&res.Branch).Error; err != nil {
			return err
		}
		// 分支结束后主会话不再有权限答它的请示：已路由未答的立即转交给用户。
		esc, err := unrouteBranchAsksTx(tx, branch, "分支已结束，请示改由你定夺", now)
		if err != nil {
			return err
		}
		res.Escalated = esc
		// 主会话派给它、还没交付的任务退回主会话名下（#236 P5）。
		if res.Returned, err = returnBranchTasksTx(tx, res.Branch.ParentWorkflowID, branch, by, now); err != nil {
			return err
		}
		res.Changed = true
		return nil
	})
	if err != nil {
		return nil, err
	}
	if res.Changed {
		var closeDetail map[string]any
		if len(res.Returned) > 0 {
			s.bumpWorkRev() // 让回执头的 board: 缓存作废：任务换了承接者
			closeDetail = map[string]any{"returned_tasks": len(res.Returned)}
		}
		s.auditBranch(ctx, AuditBranchClose, by, res.Branch.ParentWorkflowID, res.Branch.BranchWorkflowID, closeDetail)
		for _, it := range res.Escalated {
			s.auditEscalated(ctx, res.Branch.ParentWorkflowID, model.ConfirmEscalatedBySystem, it)
		}
		who := "用户"
		if strings.HasPrefix(strings.TrimSpace(by), "agent:") {
			who = "主会话 Agent"
		}
		title := s.WorkflowTitleOf(ctx, branch)
		if title == "" {
			title = branch
		}
		sys := model.MsgActor{Kind: model.ActorKindSystem, Workflow: branch}
		// 通知对方：主会话 / 用户结束了分支 → 告诉分支；（用户结束时）也告诉主会话。
		branchText := fmt.Sprintf("%s结束了这个分支。请把手上的事收尾，并用 branch(action:\"say\") 向主会话汇报结果；之后不再有新任务派给你。", who)
		parentText := fmt.Sprintf("用户结束了分支「%s」（%s）。", title, branch)
		if n := len(res.Returned); n > 0 {
			// 退回的任务要让两边都知道：分支的汇报里要写清做到哪一步，主会话要知道它们回到了自己名下。
			seqs := formatBoardSeqs(res.Returned)
			branchText += fmt.Sprintf("你名下主会话派的 %d 个没做完的任务（%s）已退回主会话名下，请在汇报里写清做到哪一步；已交付待验收的任务保留，等主会话验收。", n, seqs)
			parentText += fmt.Sprintf("它没做完的 %d 个任务（%s）已退回你名下。", n, seqs)
		}
		notices := []struct{ to, text string }{{branch, branchText}}
		if who == "用户" {
			notices = append(notices, struct{ to, text string }{res.Branch.ParentWorkflowID, parentText})
		}
		for _, n := range notices {
			_, _ = s.queueBranchRow(ctx, n.to, n.text, branch, sys, now)
		}
	}
	return &res, nil
}

// UpdateBranchInput 是改分支设置的入参：预设与显式取值二选一；CopyUserNotes 可单独改。
type UpdateBranchInput struct {
	Preset        string
	Expose        *model.BranchExpose
	CopyUserNotes *bool
}

// UpdateBranchSettings 改开放范围 / 抄送开关（只有人能调；网页）。
func (s *Store) UpdateBranchSettings(ctx context.Context, branchWorkflowID string, in UpdateBranchInput) (*model.WorkflowBranch, error) {
	cur, _, err := s.UpdateBranchSettingsFull(ctx, branchWorkflowID, in)
	return cur, err
}

// UpdateBranchSettingsFull 同 UpdateBranchSettings，并返回因此被立即转交给用户的请示：
// 「定夺」从开改成关时，原本路由给主会话、还没答的请示不能继续挂着（主会话已没有权限答），改由用户定夺（调用方据此发通知）。
func (s *Store) UpdateBranchSettingsFull(ctx context.Context, branchWorkflowID string, in UpdateBranchInput) (*model.WorkflowBranch, []model.ConfirmItem, error) {
	branch := strings.TrimSpace(branchWorkflowID)
	var cur model.WorkflowBranch
	var before model.BranchExpose
	var escalated []model.ConfirmItem
	err := s.WithTx(ctx, func(tx *gorm.DB) error {
		escalated = nil
		if err := tx.Where("branch_workflow_id = ?", branch).First(&cur).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return NewNotFoundError(fmt.Sprintf("工作流 %q 不是任何会话的分支", branch))
			}
			return err
		}
		before = cur.Expose
		wasDecide := cur.Expose.Decide
		switch {
		case strings.TrimSpace(in.Preset) != "" && in.Expose != nil:
			return NewInvalidInputError("expose_preset 与 expose 只能给一个")
		case strings.TrimSpace(in.Preset) != "":
			p, ok := model.BranchExposePreset(in.Preset)
			if !ok {
				return NewInvalidInputError("expose_preset 只能是 visible / collab / managed")
			}
			cur.Expose = p
		case in.Expose != nil:
			if err := in.Expose.Validate(); err != nil {
				return NewInvalidInputError(err.Error())
			}
			cur.Expose = *in.Expose
		}
		if in.CopyUserNotes != nil {
			cur.CopyUserNotes = *in.CopyUserNotes
		}
		if err := tx.Save(&cur).Error; err != nil {
			return err
		}
		if wasDecide && !cur.Expose.Decide {
			esc, err := unrouteBranchAsksTx(tx, branch, "「向主会话开放·定夺」已被关闭，请示改由你定夺", time.Now())
			if err != nil {
				return err
			}
			escalated = esc
		}
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	s.auditBranch(ctx, AuditBranchSettings, model.BranchCreatedByHuman, cur.ParentWorkflowID, cur.BranchWorkflowID,
		map[string]any{"expose": cur.Expose.PresetName(), "expose_values": cur.Expose, "expose_before": before, "copy_user_notes": cur.CopyUserNotes})
	for _, it := range escalated {
		s.auditEscalated(ctx, cur.ParentWorkflowID, model.ConfirmEscalatedBySystem, it)
	}
	return &cur, escalated, nil
}

// BranchLinks 是一个工作流在分支关系里的位置：它作为分支的那一条（至多一条）与它作为主会话的全部活动分支。
type BranchLinks struct {
	AsBranch *model.WorkflowBranch
	AsMain   []model.WorkflowBranch
}

// ActiveBranchLinks 一次取一个工作流的活动关系；没有任何关系的工作流返回零值（回执头据此一个字都不输出）。
func (s *Store) ActiveBranchLinks(ctx context.Context, workflowID string) (BranchLinks, error) {
	var out BranchLinks
	wf := strings.TrimSpace(workflowID)
	if wf == "" {
		return out, nil
	}
	var rows []model.WorkflowBranch
	if err := s.db.WithContext(ctx).
		Where("(branch_workflow_id = ? OR parent_workflow_id = ?) AND status = ?", wf, wf, model.BranchStatusActive).
		Order("created_at ASC, id ASC").Find(&rows).Error; err != nil {
		return out, err
	}
	for i := range rows {
		if rows[i].BranchWorkflowID == wf {
			r := rows[i]
			out.AsBranch = &r
			continue
		}
		out.AsMain = append(out.AsMain, rows[i])
	}
	return out, nil
}

// BranchOf 返回这个工作流作为分支的那一条关系（含已结束的）；不是任何分支返回 nil, nil。
func (s *Store) BranchOf(ctx context.Context, workflowID string) (*model.WorkflowBranch, error) {
	var cur model.WorkflowBranch
	err := s.db.WithContext(ctx).Where("branch_workflow_id = ?", strings.TrimSpace(workflowID)).First(&cur).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &cur, nil
}

// ListAllBranches 返回全部关系（活动 + 已结束），最新的在前，最多 BranchListMax 行。
func (s *Store) ListAllBranches(ctx context.Context) ([]model.WorkflowBranch, error) {
	var rows []model.WorkflowBranch
	if err := s.db.WithContext(ctx).Order("created_at DESC, id DESC").Limit(BranchListMax).Find(&rows).Error; err != nil {
		return nil, err
	}
	return rows, nil
}

// BranchBrief 是一条关系加上界面 / 智能体要看的概况。
type BranchBrief struct {
	model.WorkflowBranch
	Title          string     `json:"title"`
	ParentTitle    string     `json:"parent_title,omitempty"`
	Preset         string     `json:"preset,omitempty"` // 开放范围与哪个预设完全一致（visible / collab / managed）；都不一致时为空，界面显示「自定义」
	LastActivityAt *time.Time `json:"last_activity_at,omitempty"`
	Waiting        bool       `json:"waiting,omitempty"`        // 有一轮汇报在等回复
	UnreadToMain   int        `json:"unread_to_main,omitempty"` // 这个分支发给主会话、主会话还没读的消息数
	Missing        bool       `json:"missing,omitempty"`        // 这个工作流在库里已经没有任何会话行（已失效）
	PendingAsks    int        `json:"pending_asks,omitempty"`   // 路由给主会话、还在等它定夺的请示数（#236 P2）

	// 下面五项只给活动关系填（#236 P4 / P5，见 branch_status.go）：状态恒向主会话开放，不受开放范围控制。
	AgentState   string        `json:"agent_state,omitempty"`    // 分支的 Agent 此刻的状态（取值同网页状态芯片：working / idle / waiting_you / … / stalled；没有任何会话是 unknown）
	AgentStateAt *time.Time    `json:"agent_state_at,omitempty"` // 这个状态从什么时候开始
	Tasks        *BranchTasks  `json:"tasks,omitempty"`          // 名下没做完的任务数（待办 / 进行中 / 验证中），全为 0 时不出现
	Launch       *BranchLaunch `json:"launch,omitempty"`         // 启动方式：「启动」开没开、有几条预设能唤醒它
	Assign       *BranchAssign `json:"assign,omitempty"`         // 派单：「任务」开没开、主会话与分支是否在同一张项目板上（#236 P5）
}

// BranchBriefs 给一批关系补上标题、最近活动、是否在等回复、主会话未读数、是否已失效。
func (s *Store) BranchBriefs(ctx context.Context, rows []model.WorkflowBranch) []BranchBrief {
	out := make([]BranchBrief, 0, len(rows))
	titles := map[string]string{}
	title := func(wf string) string {
		if t, ok := titles[wf]; ok {
			return t
		}
		t := s.WorkflowTitleOf(ctx, wf)
		titles[wf] = t
		return t
	}
	for _, r := range rows {
		b := BranchBrief{WorkflowBranch: r, Title: title(r.BranchWorkflowID), ParentTitle: title(r.ParentWorkflowID), Preset: r.Expose.PresetName()}
		var last []time.Time
		if err := s.db.WithContext(ctx).Model(&model.FeedbackSession{}).Where("workflow_id = ?", r.BranchWorkflowID).
			Order("updated_at DESC").Limit(1).Pluck("updated_at", &last).Error; err == nil {
			if len(last) == 0 {
				b.Missing = true
			} else {
				t := last[0]
				b.LastActivityAt = &t
			}
		}
		var waiting int64
		if err := s.db.WithContext(ctx).Model(&model.FeedbackSession{}).
			Where("workflow_id = ? AND status = 'pending' AND (kind IS NULL OR kind <> ?)", r.BranchWorkflowID, model.FeedbackKindNotice).
			Limit(1).Count(&waiting).Error; err == nil {
			b.Waiting = waiting > 0
		}
		var unread int64
		if err := s.db.WithContext(ctx).Model(&model.QueuedFeedback{}).
			Where("workflow_id = ? AND kind = ? AND sender_ref = ? AND status = 'pending' AND delivered_at IS NULL", r.ParentWorkflowID, model.InboxKindBranch, r.BranchWorkflowID).
			Count(&unread).Error; err == nil {
			b.UnreadToMain = int(unread)
		}
		if r.Active() {
			var asks int64
			if err := s.db.WithContext(ctx).Model(&model.ConfirmItem{}).
				Where("workflow_id = ? AND status = ? AND addressee = ?", r.BranchWorkflowID, model.ConfirmStatusPending, model.ConfirmAddresseeMain(r.ParentWorkflowID)).
				Count(&asks).Error; err == nil {
				b.PendingAsks = int(asks)
			}
		}
		out = append(out, b)
	}
	s.fillBranchStatus(ctx, out)
	return out
}

// ---------------------------------------------------------------------------------------------
// 主会话 ↔ 分支 互发消息

// queueBranchRow 往目标工作流的收件箱写一行分支消息（不做任何校验与限额）。
func (s *Store) queueBranchRow(ctx context.Context, to, text, sender string, actor model.MsgActor, now time.Time) (*model.QueuedFeedback, error) {
	row := model.QueuedFeedback{
		WorkflowID: to, ResponseText: text, Status: "pending", Kind: model.InboxKindBranch,
		SenderRef: sender, Actor: actor.String(), CreatedAt: now, UpdatedAt: now,
	}
	if err := s.db.WithContext(ctx).Create(&row).Error; err != nil {
		return nil, err
	}
	row.State = row.DeriveState()
	return &row, nil
}

// BranchSayInput 是一条分支消息的入参。
type BranchSayInput struct {
	From string // 发送方的工作流编号（调用方自己）
	To   string // 可空：发送方是分支 → 主会话；发送方只有一个活动分支 → 那个分支
	Text string
}

// BranchSayResult 是发送结果。
type BranchSayResult struct {
	Row       model.QueuedFeedback `json:"row"`
	From      string               `json:"from_role"` // main | branch
	To        string               `json:"to"`
	ToTitle   string               `json:"to_title,omitempty"`
	Direction string               `json:"direction"` // main_to_branch | branch_to_main
}

// BranchSay 主会话 → 分支（要求开放范围的 say 开）或 分支 → 主会话（恒允许）。一律送达对方收件箱，不需要点名。
// 限额：同一发送方每小时 BranchSayPerHour 条；同一发送方发给同一接收方、对方未读的最多 BranchUnreadCap 条（防互相回复形成环路）。
func (s *Store) BranchSay(ctx context.Context, in BranchSayInput) (*BranchSayResult, error) {
	from, to := strings.TrimSpace(in.From), strings.TrimSpace(in.To)
	text := strings.TrimSpace(in.Text)
	if from == "" {
		return nil, NewInvalidInputError("workflow_id 必填")
	}
	if text == "" {
		return nil, NewInvalidInputError("text 必填：写下要告诉对方的话")
	}
	if n := len([]rune(text)); n > BranchSayMaxRunes {
		return nil, NewInvalidInputError(fmt.Sprintf("消息太长（%d 字，上限 %d 字）；请精简，或把长内容写进项目文档再在消息里引用", n, BranchSayMaxRunes))
	}
	links, err := s.ActiveBranchLinks(ctx, from)
	if err != nil {
		return nil, err
	}

	var rel *model.WorkflowBranch
	var direction string
	switch {
	case to == "" && links.AsBranch != nil:
		rel, direction, to = links.AsBranch, "branch_to_main", links.AsBranch.ParentWorkflowID
	case to == "" && len(links.AsMain) == 1:
		rel, direction, to = &links.AsMain[0], "main_to_branch", links.AsMain[0].BranchWorkflowID
	case to == "" && len(links.AsMain) > 1:
		return nil, NewInvalidInputError(fmt.Sprintf("你有 %d 个活动分支，请用 branch 参数指定要发给哪一个：%s", len(links.AsMain), s.branchChoices(ctx, links.AsMain)))
	case to == "":
		return nil, NewInvalidInputError("你既没有分支、也不是任何会话的分支，没有可以发消息的对象（branch(action:\"list\") 可以看现状）")
	default:
		if links.AsBranch != nil && links.AsBranch.ParentWorkflowID == to {
			rel, direction = links.AsBranch, "branch_to_main"
		}
		for i := range links.AsMain {
			if links.AsMain[i].BranchWorkflowID == to {
				rel, direction = &links.AsMain[i], "main_to_branch"
			}
		}
		if rel == nil {
			return nil, NewForbiddenError(fmt.Sprintf("你和 %q 之间没有生效的主会话 / 分支关系，不能发消息", to))
		}
	}
	if direction == "main_to_branch" && !rel.Expose.Say {
		return nil, NewForbiddenError(fmt.Sprintf("分支 %q 没有向主会话开放「发消息」；需要用户在该分支的会话设置里打开", to))
	}

	now := time.Now()
	var perHour, unread int64
	if err := s.db.WithContext(ctx).Model(&model.QueuedFeedback{}).
		Where("kind = ? AND sender_ref = ? AND created_at >= ?", model.InboxKindBranch, from, now.Add(-time.Hour)).Count(&perHour).Error; err != nil {
		return nil, err
	}
	if perHour >= BranchSayPerHour {
		return nil, NewConflictError(fmt.Sprintf("你一小时内已经发了 %d 条分支消息（上限 %d）。请合并成更少、更完整的消息，或等一会儿再发", perHour, BranchSayPerHour), 0)
	}
	if err := s.db.WithContext(ctx).Model(&model.QueuedFeedback{}).
		Where("workflow_id = ? AND kind = ? AND sender_ref = ? AND status = 'pending' AND delivered_at IS NULL", to, model.InboxKindBranch, from).
		Count(&unread).Error; err != nil {
		return nil, err
	}
	if unread >= BranchUnreadCap {
		return nil, NewConflictError(fmt.Sprintf("对方还有你发的 %d 条消息没读（上限 %d），先别继续发；等对方回复或读取收件箱后再发", unread, BranchUnreadCap), 0)
	}

	actorKind := model.ActorKindBranch
	if direction == "main_to_branch" {
		actorKind = model.ActorKindMain
	}
	row, err := s.queueBranchRow(ctx, to, text, from, model.MsgActor{Kind: actorKind, Workflow: from}, now)
	if err != nil {
		return nil, err
	}
	s.auditBranch(ctx, AuditBranchSay, model.BranchCreatedByAgent(from), rel.ParentWorkflowID, rel.BranchWorkflowID,
		map[string]any{"direction": direction, "runes": len([]rune(text))})
	return &BranchSayResult{Row: *row, From: actorKind, To: to, ToTitle: s.WorkflowTitleOf(ctx, to), Direction: direction}, nil
}

func (s *Store) branchChoices(ctx context.Context, rows []model.WorkflowBranch) string {
	parts := make([]string, 0, len(rows))
	for _, r := range rows {
		t := s.WorkflowTitleOf(ctx, r.BranchWorkflowID)
		if t == "" {
			parts = append(parts, r.BranchWorkflowID)
			continue
		}
		parts = append(parts, fmt.Sprintf("%s（%s）", r.BranchWorkflowID, t))
	}
	return strings.Join(parts, "、")
}

// CopyUserNoteToMain 把用户在分支会话页里的发言抄送给主会话（一行摘要，前 BranchCopyRunes 字）。
// 不是活动分支、没开抄送、或主会话那边未读的抄送已经堆到上限，都返回 nil, nil（静默跳过，不影响用户的原操作）。
func (s *Store) CopyUserNoteToMain(ctx context.Context, branchWorkflowID, text string, imageCount int) (*model.QueuedFeedback, error) {
	branch := strings.TrimSpace(branchWorkflowID)
	if branch == "" {
		return nil, nil
	}
	rel, err := s.BranchOf(ctx, branch)
	if err != nil || rel == nil || !rel.Active() || !rel.CopyUserNotes {
		return nil, err
	}
	var unread int64
	if err := s.db.WithContext(ctx).Model(&model.QueuedFeedback{}).
		Where("workflow_id = ? AND kind = ? AND sender_ref = ? AND status = 'pending' AND delivered_at IS NULL AND actor LIKE ?",
			rel.ParentWorkflowID, model.InboxKindBranch, branch, `%"kind":"human"%`).Count(&unread).Error; err != nil {
		return nil, err
	}
	if unread >= BranchUnreadCap {
		return nil, nil
	}
	summary := strings.Join(strings.Fields(text), " ")
	if summary == "" && imageCount > 0 {
		summary = fmt.Sprintf("（发了 %d 张图片）", imageCount)
	}
	if summary == "" {
		return nil, nil
	}
	if r := []rune(summary); len(r) > BranchCopyRunes {
		summary = string(r[:BranchCopyRunes]) + "…"
	}
	title := s.WorkflowTitleOf(ctx, branch)
	if title == "" {
		title = branch
	}
	line := fmt.Sprintf("用户在分支「%s」（%s）里说：%s", title, branch, summary)
	return s.queueBranchRow(ctx, rel.ParentWorkflowID, line, branch, model.MsgActor{Kind: model.ActorKindHuman, Workflow: branch}, time.Now())
}

// NotifyMainBranchCreated 在用户于网页里给某个会话建了分支之后，给主会话的收件箱写一行说明（主会话由此知道有了新分支）。
func (s *Store) NotifyMainBranchCreated(ctx context.Context, rel model.WorkflowBranch) (*model.QueuedFeedback, error) {
	title := s.WorkflowTitleOf(ctx, rel.BranchWorkflowID)
	if title == "" {
		title = rel.BranchWorkflowID
	}
	line := fmt.Sprintf("用户在网页里给本会话建了分支「%s」（%s）；开放范围：%s。分支的 Agent 接入后，可用 branch(action: \"say\") 与它互发消息。", title, rel.BranchWorkflowID, rel.Expose.Summary())
	return s.queueBranchRow(ctx, rel.ParentWorkflowID, line, rel.BranchWorkflowID, model.MsgActor{Kind: model.ActorKindSystem, Workflow: rel.BranchWorkflowID}, time.Now())
}

// ---------------------------------------------------------------------------------------------
// 会话页的分支消息流

// BranchFeedItem 是会话页时间线里的一项：一条消息，或一行事件。
type BranchFeedItem struct {
	Kind       string         `json:"kind"` // message | event
	ID         uint           `json:"id,omitempty"`
	At         time.Time      `json:"at"`
	Actor      model.MsgActor `json:"actor"`
	ActorTitle string         `json:"actor_title,omitempty"` // 发言者所在会话的标题
	Text       string         `json:"text,omitempty"`
	State      string         `json:"state,omitempty"`    // message：queued | delivered | acked | expired
	Event      string         `json:"event,omitempty"`    // event：created | closed | ask_routed（请示先送主会话定夺）| escalated（请示转交 / 升级给用户）| main_answered（主会话代答）| params_changed（主会话改了分支的参数）| params_reverted（用户撤销了主会话的修改）
	Branch     string         `json:"branch,omitempty"`   // 关联的分支工作流编号
	By         string         `json:"by,omitempty"`       // event：谁做的（human / agent:<wf> / migrate）
	Outgoing   bool           `json:"outgoing,omitempty"` // message：是本会话的智能体发出去的（收件人是 To）；否则是别人发给本会话的
	To         string         `json:"to,omitempty"`       // message：收件人的工作流编号（只有 Outgoing 时有）
	Ref        string         `json:"ref,omitempty"`      // event（ask_routed / escalated / main_answered）：请示编号 Q12
	RefTitle   string         `json:"ref_title,omitempty"`
	// event（params_changed / params_reverted）：ID 是参数事件的编号，Text 是一句人话摘要（不含常驻规则全文）。
	Field      string `json:"field,omitempty"`      // 哪个参数：resident | phase | wake_resume | wake_new | title | async
	Reverted   bool   `json:"reverted,omitempty"`   // params_changed：已被用户撤销
	Revertable bool   `json:"revertable,omitempty"` // params_changed：现在还能撤销（POST …/params-events/:id/revert）
	RevertOf   uint   `json:"revert_of,omitempty"`  // params_reverted：撤销的是哪条事件
}

// BranchFeed 是 GET /workflows/:id/branch-feed 的内容。
type BranchFeed struct {
	Role        string            `json:"role"`                   // main | branch | none
	Relation    *BranchBrief      `json:"relation,omitempty"`     // role = branch：自己作为分支的那条关系
	ParentTitle string            `json:"parent_title,omitempty"` // role = branch：主会话的标题
	Branches    []BranchBrief     `json:"branches,omitempty"`     // role = main：自己的全部分支（含已结束）
	Items       []BranchFeedItem  `json:"items"`
	Titles      map[string]string `json:"titles,omitempty"` // 工作流编号 → 标题，界面给发言者标名字用
}

// BranchFeed 取一个会话页的分支消息流。limit 为每个来源保留的最新条数（默认 100）。
// 角色：作为分支（有关系行，含已结束）= branch；有分支（含已结束）= main；都没有 = none（历史消息仍会列出）。
func (s *Store) BranchFeed(ctx context.Context, workflowID string, limit int) (*BranchFeed, error) {
	wf := strings.TrimSpace(workflowID)
	if limit <= 0 || limit > 200 {
		limit = 100
	}
	feed := &BranchFeed{Role: BranchRoleNone, Items: []BranchFeedItem{}}

	var rels []model.WorkflowBranch
	if err := s.db.WithContext(ctx).Where("branch_workflow_id = ? OR parent_workflow_id = ?", wf, wf).
		Order("created_at ASC, id ASC").Find(&rels).Error; err != nil {
		return nil, err
	}
	briefs := s.BranchBriefs(ctx, rels)
	titles := map[string]string{}
	for _, b := range briefs {
		titles[b.BranchWorkflowID] = b.Title
		titles[b.ParentWorkflowID] = b.ParentTitle
	}
	for i := range briefs {
		b := briefs[i]
		if b.BranchWorkflowID == wf {
			feed.Role, feed.Relation, feed.ParentTitle = BranchRoleBranch, &briefs[i], b.ParentTitle
			continue
		}
		if feed.Role != BranchRoleBranch {
			feed.Role = BranchRoleMain
		}
		feed.Branches = append(feed.Branches, b)
	}
	if len(titles) > 0 {
		feed.Titles = titles
	}

	// 事件行：关系的建立与结束。
	for _, b := range briefs {
		feed.Items = append(feed.Items, BranchFeedItem{
			Kind: "event", At: b.CreatedAt, Event: "created", Branch: b.BranchWorkflowID, By: b.CreatedBy,
			Actor: model.MsgActor{Kind: model.ActorKindSystem}, ActorTitle: b.Title,
		})
		if b.ClosedAt != nil {
			feed.Items = append(feed.Items, BranchFeedItem{
				Kind: "event", At: *b.ClosedAt, Event: "closed", Branch: b.BranchWorkflowID,
				Actor: model.MsgActor{Kind: model.ActorKindSystem}, ActorTitle: b.Title,
			})
		}
	}

	// 事件行：请示的转交 / 升级与主会话代答（#236 P2）。从各分支自己的确认项里读（事实只在确认项上有一份），不另存事件。
	s.appendRoutingEvents(ctx, briefs, limit, feed)
	// 事件行：主会话改了分支的参数、用户撤销（#236 P3）。事实只在 branch_param_events 一张表上。
	s.appendParamEvents(ctx, briefs, limit, feed)

	// 消息：本会话收件箱里的 branch 行（保留期内），最新 limit 条。
	var rows []model.QueuedFeedback
	if err := s.db.WithContext(ctx).Model(&model.QueuedFeedback{}).
		Omit("images").
		Where("workflow_id = ? AND kind = ? AND status <> 'revoked'"+notRoutedAskRow, wf, model.InboxKindBranch).
		Order("created_at DESC, id DESC").Limit(limit).Find(&rows).Error; err != nil {
		return nil, err
	}
	fillInboxStates(rows)
	for _, q := range rows {
		actor, _ := model.ParseMsgActor(q.Actor)
		title := titles[actor.Workflow]
		if title == "" && actor.Workflow != "" {
			title = s.WorkflowTitleOf(ctx, actor.Workflow)
			titles[actor.Workflow] = title
		}
		feed.Items = append(feed.Items, BranchFeedItem{
			Kind: "message", ID: q.ID, At: q.CreatedAt, Actor: actor, ActorTitle: title, Text: q.ResponseText, State: q.State, Branch: branchOfActor(actor, wf, rels),
		})
	}

	// 消息（发出去的）：本会话的智能体用 say 发给对方的话躺在对方的收件箱里，actor 就是本会话（main / branch）。
	// 一并列出，会话页上两个方向的话都看得到，也看得出对方读了没有（State）。
	titleOf := func(id string) string {
		if t, ok := titles[id]; ok {
			return t
		}
		t := s.WorkflowTitleOf(ctx, id)
		titles[id] = t
		return t
	}
	var sent []model.QueuedFeedback
	if err := s.db.WithContext(ctx).Model(&model.QueuedFeedback{}).
		Omit("images").
		Where("kind = ? AND status <> 'revoked' AND actor IN ?"+notRoutedAskRow, model.InboxKindBranch, []string{
			model.MsgActor{Kind: model.ActorKindMain, Workflow: wf}.String(),
			model.MsgActor{Kind: model.ActorKindBranch, Workflow: wf}.String(),
		}).
		Order("created_at DESC, id DESC").Limit(limit).Find(&sent).Error; err != nil {
		return nil, err
	}
	fillInboxStates(sent)
	for _, q := range sent {
		actor, _ := model.ParseMsgActor(q.Actor)
		branch := wf // 分支 → 主会话：归属本分支
		if actor.Kind == model.ActorKindMain {
			branch = q.WorkflowID // 主会话 → 分支：归属收件的那个分支
		}
		feed.Items = append(feed.Items, BranchFeedItem{
			Kind: "message", ID: q.ID, At: q.CreatedAt, Actor: actor, ActorTitle: titleOf(wf), Text: q.ResponseText, State: q.State,
			Branch: branch, Outgoing: true, To: q.WorkflowID,
		})
		titleOf(q.WorkflowID)
	}
	if len(titles) > 0 {
		feed.Titles = titles
	}
	sort.SliceStable(feed.Items, func(i, j int) bool { return feed.Items[i].At.Before(feed.Items[j].At) })
	if len(feed.Items) > limit {
		feed.Items = feed.Items[len(feed.Items)-limit:]
	}
	return feed, nil
}

// branchOfActor 给消息标出它属于哪个分支：发言者是分支 / 用户在分支里说的 → 那个分支；发言者是主会话 → 收件人自己（本会话就是分支）。
func branchOfActor(a model.MsgActor, self string, rels []model.WorkflowBranch) string {
	switch a.Kind {
	case model.ActorKindBranch, model.ActorKindHuman, model.ActorKindSystem:
		return a.Workflow
	case model.ActorKindMain:
		return self
	}
	return ""
}

// ---------------------------------------------------------------------------------------------
// 主会话读分支的历史

// BranchRound 是主会话能看到的分支的一轮。
type BranchRound struct {
	SessionID string    `json:"session_id"`
	At        time.Time `json:"at"`
	Kind      string    `json:"kind"`   // report（阻塞汇报）| notice（进度通知）
	Status    string    `json:"status"` // pending | completed | timeout | cancelled | archived
	Title     string    `json:"title,omitempty"`
	Summary   string    `json:"summary"`
	Reply     string    `json:"reply,omitempty"` // 用户（或回复方）的回复；摘要档只给首行
	Images    int       `json:"images,omitempty"`
}

// BranchHistory 是 branch read 的返回。
type BranchHistory struct {
	Branch string        `json:"branch"`
	Title  string        `json:"title,omitempty"`
	Depth  string        `json:"depth"` // summary | full
	Rounds []BranchRound `json:"rounds"`
}

func firstLineClip(s string, n int) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexAny(s, "\r\n"); i >= 0 {
		s = strings.TrimSpace(s[:i])
	}
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}

func ellipsisClip(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}

// ReadBranchHistory 主会话读自己某个分支最近几轮的汇报，按开放范围裁剪：off 拒绝，summary 每轮标题 + 首行，full 全文（不含图片）。
// caller 必须是该分支的主会话，且关系仍然生效。
func (s *Store) ReadBranchHistory(ctx context.Context, caller, branchWorkflowID string, limit int) (*BranchHistory, error) {
	caller, branch := strings.TrimSpace(caller), strings.TrimSpace(branchWorkflowID)
	rel, err := s.BranchOf(ctx, branch)
	if err != nil {
		return nil, err
	}
	if rel == nil || rel.ParentWorkflowID != caller {
		return nil, NewForbiddenError(fmt.Sprintf("%q 不是你的分支，不能读它的历史", branch))
	}
	if !rel.Active() {
		return nil, NewForbiddenError(fmt.Sprintf("分支 %q 已经结束，不能再读它的历史", branch))
	}
	depth := rel.Expose.History
	if depth == model.ExposeHistoryOff {
		return nil, NewForbiddenError(fmt.Sprintf("分支 %q 没有向主会话开放「读历史」；需要用户在该分支的会话设置里打开", branch))
	}
	if limit <= 0 {
		limit = BranchReadDefault
	}
	if limit > BranchReadMax {
		limit = BranchReadMax
	}
	var rows []model.FeedbackSession
	if err := s.db.WithContext(ctx).Model(&model.FeedbackSession{}).
		Where("workflow_id = ?", branch).Order("created_at DESC, id DESC").Limit(limit).Find(&rows).Error; err != nil {
		return nil, err
	}
	out := &BranchHistory{Branch: branch, Title: s.WorkflowTitleOf(ctx, branch), Depth: depth, Rounds: make([]BranchRound, 0, len(rows))}
	for i := len(rows) - 1; i >= 0; i-- {
		r := rows[i]
		kind := "report"
		if r.Kind == model.FeedbackKindNotice {
			kind = "notice"
		}
		round := BranchRound{SessionID: r.ID, At: r.CreatedAt, Kind: kind, Status: r.Status, Title: r.Title, Images: len(r.Images)}
		if depth == model.ExposeHistorySummary {
			round.Summary = firstLineClip(r.Summary, BranchSummaryRunes)
			round.Reply = firstLineClip(r.ResponseText, BranchSummaryRunes)
		} else {
			round.Summary = ellipsisClip(r.Summary, BranchFullRunes)
			round.Reply = ellipsisClip(r.ResponseText, BranchFullRunes)
		}
		out.Rounds = append(out.Rounds, round)
	}
	return out, nil
}

// notRoutedAskRow 把「路由给主会话的请示」写进主会话收件箱的那一行（origin_ref = c:Qn，正文是给智能体看的作答说明）
// 排除在分支流的消息之外：对人来说它是一行事件（见 appendRoutingEvents 的 ask_routed），不是一条「分支说的话」。
const notRoutedAskRow = " AND (origin_ref IS NULL OR origin_ref NOT LIKE 'c:%')"

// appendRoutingEvents 把这些分支里「请示送出 / 转交 / 升级给用户」与「主会话代答」的请示补成事件行（会话页分支流里一行字说明发生了什么）。
// 失败只是少几行事件，不影响分支流的其余内容。
func (s *Store) appendRoutingEvents(ctx context.Context, briefs []BranchBrief, limit int, feed *BranchFeed) {
	if len(briefs) == 0 {
		return
	}
	wfs := make([]string, 0, len(briefs))
	titles := map[string]string{}
	for _, b := range briefs {
		wfs = append(wfs, b.BranchWorkflowID)
		titles[b.BranchWorkflowID] = b.Title
	}
	var items []model.ConfirmItem
	if err := s.db.WithContext(ctx).Omit("body", "options_json").
		Where("workflow_id IN ? AND (escalated_at IS NOT NULL OR addressee <> '')", wfs).
		Order("updated_at DESC, id DESC").Limit(limit).Find(&items).Error; err != nil {
		return
	}
	for _, it := range items {
		v := model.NewConfirmItemView(&it)
		ref := model.ConfirmRef(it.Seq)
		// 曾路由给主会话（addressee 还在，或已被转交 / 升级清空但记着 escalated_at）：请示送出的那一刻是一行事件，
		// 取代原先主会话收件箱里那条给智能体看的、带工具用法的请示正文（它对人没有意义，不在分支流里当消息列出）。
		feed.Items = append(feed.Items, BranchFeedItem{
			Kind: "event", At: it.CreatedAt, Event: "ask_routed", Branch: it.WorkflowID,
			Actor: model.MsgActor{Kind: model.ActorKindSystem}, ActorTitle: titles[it.WorkflowID], Ref: ref, RefTitle: it.Title,
		})
		if it.EscalatedAt != nil && v.Escalation != nil {
			feed.Items = append(feed.Items, BranchFeedItem{
				Kind: "event", At: *it.EscalatedAt, Event: "escalated", Branch: it.WorkflowID, By: v.Escalation.By, Text: v.Escalation.Note,
				Actor: model.MsgActor{Kind: model.ActorKindSystem}, ActorTitle: titles[it.WorkflowID], Ref: ref, RefTitle: it.Title,
			})
		}
		if it.Status == model.ConfirmStatusAnswered && it.AnsweredAt != nil && v.Answer != nil && strings.HasPrefix(v.Answer.By, model.ConfirmByAgentPrefix) {
			feed.Items = append(feed.Items, BranchFeedItem{
				Kind: "event", At: *it.AnsweredAt, Event: "main_answered", Branch: it.WorkflowID, By: v.Answer.By,
				Actor: model.MsgActor{Kind: model.ActorKindSystem}, ActorTitle: titles[it.WorkflowID], Ref: ref, RefTitle: it.Title,
			})
		}
	}
}
