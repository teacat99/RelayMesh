package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"gorm.io/gorm"

	"github.com/teacat99/RelayMesh/v2/internal/confirm"
	"github.com/teacat99/RelayMesh/v2/internal/model"
)

// 决策路由（#236 P2）：分支里智能体发的请示（async_ask 的 ask / plan），分支开放了「定夺」时先送主会话；
// 主会话用 branch(action:"answer") 代为定夺、用 branch(action:"escalate") 转交用户，超时没人答复自动升级给用户。
// 设计与取舍见 .cursor/planning/2026-10-24-236-p2-impl-plan.md。路由状态只放在确认项自己的三列里（addressee / escalated_at /
// escalation_json），卡片视图、MCP get / list、计数都读同一份事实。

// 审计动作（只记双方工作流编号、请示编号与风险档，不记正文）。
const (
	AuditBranchAnswer   = "branch.answer"
	AuditBranchEscalate = "branch.escalate"
)

// BranchPrefsKey 是分支全局偏好在 system_settings 表里的键，值是 JSON（见 model.BranchPrefs）。
// 只新增键、不改表结构；旧版本二进制不认识这个键，会直接忽略它。
const BranchPrefsKey = "branch.prefs"

// GetBranchPrefs 读取分支全局偏好；从未设置过、或存下来的值已损坏时返回出厂默认（兜底开关关、30 分钟升级）。
func (s *Store) GetBranchPrefs(ctx context.Context) (model.BranchPrefs, error) {
	var rec model.SystemSetting
	err := s.db.WithContext(ctx).Where("key = ?", BranchPrefsKey).First(&rec).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return model.BranchPrefs{}, nil
		}
		return model.BranchPrefs{}, err
	}
	return decodeBranchPrefs(rec.Value), nil
}

// UpdateBranchPrefs 在一个事务里「读—改—写」：mutate 拿到当前偏好、返回新偏好（或返回错误拒绝这次修改）。
// 新偏好校验不过返回包装了 model.ErrInvalidBranchPrefs 的错误，库里保持原样。
func (s *Store) UpdateBranchPrefs(ctx context.Context, mutate func(cur model.BranchPrefs) (model.BranchPrefs, error)) (model.BranchPrefs, error) {
	var out model.BranchPrefs
	err := s.WithTx(ctx, func(tx *gorm.DB) error {
		cur := model.BranchPrefs{}
		var rec model.SystemSetting
		if err := tx.Where("key = ?", BranchPrefsKey).First(&rec).Error; err == nil {
			cur = decodeBranchPrefs(rec.Value)
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		next, err := mutate(cur)
		if err != nil {
			return err
		}
		if err := next.Validate(); err != nil {
			return err
		}
		raw, err := json.Marshal(next)
		if err != nil {
			return err
		}
		if err := tx.Save(&model.SystemSetting{Key: BranchPrefsKey, Value: string(raw), UpdatedAt: time.Now()}).Error; err != nil {
			return err
		}
		out = next
		return nil
	})
	return out, err
}

// decodeBranchPrefs 容忍损坏的值：解析失败按出厂默认处理（兜底开关关 = 平台不替用户设限，是方案里的默认）。
func decodeBranchPrefs(raw string) model.BranchPrefs {
	var p model.BranchPrefs
	if json.Unmarshal([]byte(raw), &p) != nil {
		return model.BranchPrefs{}
	}
	return p
}

// branchRoute 是建立请示时预读的、写给主会话那行文案要用的东西。是否真的路由不看它，看事务里重验后的关系。
type branchRoute struct {
	branchTitle string
	minutes     int
	l2Guard     bool
}

// prepareBranchRoute 预读：这个工作流是活动分支且开放了「定夺」才返回非 nil（读不到任何东西一律当作不路由，请示照常直达用户）。
func (s *Store) prepareBranchRoute(ctx context.Context, workflowID string) *branchRoute {
	rel, err := s.BranchOf(ctx, workflowID)
	if err != nil || rel == nil || !rel.Active() || !rel.Expose.Decide {
		return nil
	}
	prefs, _ := s.GetBranchPrefs(ctx)
	return &branchRoute{branchTitle: s.WorkflowTitleOf(ctx, workflowID), minutes: prefs.EscalateAfterMinutes(), l2Guard: prefs.L2Guard}
}

// activeDecideBranchTx 事务里取这个工作流作为分支的活动关系，且开放了「定夺」；否则返回 nil, nil。
func activeDecideBranchTx(tx *gorm.DB, workflowID string) (*model.WorkflowBranch, error) {
	var rel model.WorkflowBranch
	err := tx.Where("branch_workflow_id = ? AND status = ?", workflowID, model.BranchStatusActive).First(&rel).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !rel.Expose.Decide {
		return nil, nil
	}
	return &rel, nil
}

// routedAskTx 校验「调用者是这个分支的活动主会话、分支开放了定夺、这条请示还在等主会话」，返回关系与请示。
// 请示的编号只在各自的工作流内唯一，所以一律按「分支工作流 + ref」取，并核对 addressee，两个分支同时有 Q1 也不会串。
func routedAskTx(tx *gorm.DB, main, branch, ref string) (*model.WorkflowBranch, *model.ConfirmItem, error) {
	main, branch, ref = strings.TrimSpace(main), strings.TrimSpace(branch), strings.TrimSpace(ref)
	if main == "" {
		return nil, nil, NewInvalidInputError("workflow_id 必填")
	}
	if branch == "" {
		return nil, nil, NewInvalidInputError("branch 必填：分支的工作流编号（branch(action:\"list\") 可以看你的分支）")
	}
	if ref == "" {
		return nil, nil, NewInvalidInputError("ref 必填：请示的编号，如 Q12（回执头 / 收件箱里的请示都带着）")
	}
	var rel model.WorkflowBranch
	err := tx.Where("branch_workflow_id = ? AND parent_workflow_id = ? AND status = ?", branch, main, model.BranchStatusActive).First(&rel).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil, NewForbiddenError(fmt.Sprintf("%q 不是你的活动分支，不能处理它的请示（branch(action:\"list\") 可以看你的分支）", branch))
	}
	if err != nil {
		return nil, nil, err
	}
	if !rel.Expose.Decide {
		return nil, nil, NewForbiddenError(fmt.Sprintf("分支 %q 没有向主会话开放「定夺」；需要用户在该分支的会话设置里打开", branch))
	}
	item, err := getConfirmItem(tx, branch, ref)
	if err != nil {
		return nil, nil, err
	}
	refText := model.ConfirmRef(item.Seq)
	if item.Status != model.ConfirmStatusPending {
		return nil, nil, NewConflictError(fmt.Sprintf("%s 已处理（%s）：可能用户已经直接答复了，不需要你再定夺", refText, item.Status), 0)
	}
	switch {
	case model.ConfirmAddresseeMainOf(item.Addressee) == main:
		return &rel, item, nil
	case item.EscalatedAt != nil:
		return nil, nil, NewConflictError(fmt.Sprintf("%s 已转交给用户，不能再由主会话作答", refText), 0)
	case strings.TrimSpace(item.Addressee) == "":
		return nil, nil, NewForbiddenError(fmt.Sprintf("%s 不是路由给主会话的请示（它直接发给了用户）", refText))
	default:
		return nil, nil, NewForbiddenError(fmt.Sprintf("%s 不是路由给你的请示", refText))
	}
}

// escalateConfirmTx 把一条路由给主会话、仍待处理的请示转交给用户：清 addressee、记 escalated_at 与来源 / 说明。
// 条件更新：同一条被并发作答或已转交时影响行数为 0，返回冲突（调用方按「已被处理」处理）。
func escalateConfirmTx(tx *gorm.DB, item *model.ConfirmItem, by, note string, now time.Time) error {
	raw, err := json.Marshal(model.ConfirmEscalation{By: by, Note: note})
	if err != nil {
		return err
	}
	res := tx.Model(&model.ConfirmItem{}).
		Where("id = ? AND status = ? AND addressee LIKE ?", item.ID, model.ConfirmStatusPending, model.ConfirmAddresseeMainPrefix+"%").
		Updates(map[string]any{"addressee": "", "escalated_at": now, "escalation_json": string(raw), "updated_at": now})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return NewConflictError(fmt.Sprintf("%s 已被处理，不能再转交", model.ConfirmRef(item.Seq)), 0)
	}
	item.Addressee, item.EscalatedAt, item.EscalationJSON, item.UpdatedAt = "", &now, string(raw), now
	return nil
}

// unrouteBranchAsksTx 把一个分支所有「已路由、未答」的请示立即转交给用户（来源 system）：关系结束、或「定夺」被关掉时调用，
// 免得请示挂在一个主会话已经没有权限答的地方，只能干等超时。返回被转交的请示（调用方负责发通知）。
func unrouteBranchAsksTx(tx *gorm.DB, branchWorkflowID, note string, now time.Time) ([]model.ConfirmItem, error) {
	var rows []model.ConfirmItem
	if err := tx.Where("workflow_id = ? AND status = ? AND addressee LIKE ?", branchWorkflowID, model.ConfirmStatusPending,
		model.ConfirmAddresseeMainPrefix+"%").Order("created_at ASC, id ASC").Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]model.ConfirmItem, 0, len(rows))
	for i := range rows {
		if err := escalateConfirmTx(tx, &rows[i], model.ConfirmEscalatedBySystem, note, now); err != nil {
			if isConflict(err) {
				continue
			}
			return nil, err
		}
		out = append(out, rows[i])
	}
	return out, nil
}

// BranchAnswerInput 是主会话代答的入参。
type BranchAnswerInput struct {
	Main      string // 调用者（主会话）的工作流编号
	Branch    string // 分支的工作流编号
	Ref       string // 请示编号（Q12）
	OptionIDs []string
	Note      string
	Custom    string
}

// BranchAnswer 主会话代为定夺分支里的一条请示。校验见 routedAskTx；平台兜底开关开着时 L2 请示一律拒绝（只能转交）；
// 答复内容与确认卡同一套校验；By=agent:<主会话>；答复行进分支收件箱并带上 actor=main；先到先得（用户先答则返回冲突）。
func (s *Store) BranchAnswer(ctx context.Context, in BranchAnswerInput) (*model.ConfirmItem, error) {
	prefs, err := s.GetBranchPrefs(ctx)
	if err != nil {
		return nil, err
	}
	var out *model.ConfirmItem
	var rel *model.WorkflowBranch
	err = s.WithTx(ctx, func(tx *gorm.DB) error {
		r, item, err := routedAskTx(tx, in.Main, in.Branch, in.Ref)
		if err != nil {
			return err
		}
		if prefs.L2Guard && item.Risk == model.ConfirmRiskL2 {
			return NewForbiddenError(fmt.Sprintf("平台开着「L2 请示不由主会话代答」的兜底开关：%s 的风险档是 L2，请用 branch(action:\"escalate\") 转交给用户", model.ConfirmRef(item.Seq)))
		}
		note, custom, err := validateConfirmAnswer(item, in.OptionIDs, in.Note, in.Custom)
		if err != nil {
			return err
		}
		now := time.Now()
		if err := answerConfirmTx(tx, item, in.OptionIDs, note, custom, model.ConfirmByAgent(strings.TrimSpace(in.Main)), now); err != nil {
			return err
		}
		actor := model.MsgActor{Kind: model.ActorKindMain, Workflow: strings.TrimSpace(in.Main)}.String()
		if err := queueAnswerMessageActorTx(tx, item.WorkflowID, confirm.FormatMainAnswerLine(item, in.OptionIDs, note, custom), confirmOriginRef(item.Seq), actor, now); err != nil {
			return err
		}
		out, rel = item, r
		return nil
	})
	if err != nil {
		return nil, err
	}
	s.auditBranch(ctx, AuditBranchAnswer, model.BranchCreatedByAgent(rel.ParentWorkflowID), rel.ParentWorkflowID, rel.BranchWorkflowID,
		map[string]any{"ref": model.ConfirmRef(out.Seq), "risk": out.Risk})
	return out, nil
}

// BranchEscalateInput 是主会话转交的入参。
type BranchEscalateInput struct {
	Main   string
	Branch string
	Ref    string
	Note   string // 必填：用户靠它看懂为什么转给他
}

// BranchEscalate 主会话把分支里一条请示转交给用户：校验同 BranchAnswer（不看兜底开关）；addressee 清空、记 escalated_at 与说明。
func (s *Store) BranchEscalate(ctx context.Context, in BranchEscalateInput) (*model.ConfirmItem, error) {
	note := strings.TrimSpace(in.Note)
	if note == "" {
		return nil, NewInvalidInputError("note 必填：写下为什么转给用户（用户靠它看懂这件事）")
	}
	if n := utf8.RuneCountInString(note); n > model.ConfirmEscalateNoteMax {
		return nil, NewInvalidInputError(fmt.Sprintf("note 太长（%d 字，上限 %d 字）", n, model.ConfirmEscalateNoteMax))
	}
	var out *model.ConfirmItem
	var rel *model.WorkflowBranch
	err := s.WithTx(ctx, func(tx *gorm.DB) error {
		r, item, err := routedAskTx(tx, in.Main, in.Branch, in.Ref)
		if err != nil {
			return err
		}
		if err := escalateConfirmTx(tx, item, model.ConfirmByAgent(strings.TrimSpace(in.Main)), note, time.Now()); err != nil {
			return err
		}
		out, rel = item, r
		return nil
	})
	if err != nil {
		return nil, err
	}
	s.auditBranch(ctx, AuditBranchEscalate, model.BranchCreatedByAgent(rel.ParentWorkflowID), rel.ParentWorkflowID, rel.BranchWorkflowID,
		map[string]any{"ref": model.ConfirmRef(out.Seq), "risk": out.Risk, "by": "agent"})
	return out, nil
}

// EscalateOverdueBranchAsks 把「路由给主会话、仍待处理、创建超过 after」的请示升级给用户（来源 timeout），最多处理 limit 条；
// 返回被升级的请示（调用方负责发通知）。幂等：已升级 / 已答复的不会再被选中；重启不丢（时间戳在库里）。
func (s *Store) EscalateOverdueBranchAsks(ctx context.Context, now time.Time, after time.Duration, note string, limit int) ([]model.ConfirmItem, error) {
	if limit <= 0 {
		limit = 50
	}
	var out []model.ConfirmItem
	var parents []string
	err := s.WithTx(ctx, func(tx *gorm.DB) error {
		out, parents = out[:0], parents[:0] // 事务重试时不带上一次的残留
		var rows []model.ConfirmItem
		if err := tx.Where("status = ? AND addressee LIKE ? AND created_at <= ?", model.ConfirmStatusPending,
			model.ConfirmAddresseeMainPrefix+"%", now.Add(-after)).Order("created_at ASC, id ASC").Limit(limit).Find(&rows).Error; err != nil {
			return err
		}
		for i := range rows {
			parent := model.ConfirmAddresseeMainOf(rows[i].Addressee) // 升级会清空 addressee，先记下来给审计用
			if err := escalateConfirmTx(tx, &rows[i], model.ConfirmEscalatedByTimeout, note, now); err != nil {
				if isConflict(err) {
					continue // 刚被用户答复 / 被主会话转交，跳过
				}
				return err
			}
			out = append(out, rows[i])
			parents = append(parents, parent)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	for i := range out {
		s.auditEscalated(ctx, parents[i], model.ConfirmEscalatedByTimeout, out[i])
	}
	return out, nil
}

// auditEscalated 给一条被转交 / 升级的请示写审计（by：agent:<wf> / timeout / system；parent 是当时的主会话）。
func (s *Store) auditEscalated(ctx context.Context, parent, by string, item model.ConfirmItem) {
	s.auditBranch(ctx, AuditBranchEscalate, by, parent, item.WorkflowID,
		map[string]any{"ref": model.ConfirmRef(item.Seq), "risk": item.Risk, "by": strings.SplitN(by, ":", 2)[0]})
}

// isConflict 判断是不是「已被处理」类的冲突错误（条件更新没命中）。
func isConflict(err error) bool {
	var ae *AppError
	return errors.As(err, &ae) && ae.Kind == ErrConflict
}

// PendingBranchAsk 是主会话眼里一条在等它定夺的请示。
type PendingBranchAsk struct {
	Branch      string    `json:"branch"`
	BranchTitle string    `json:"branch_title,omitempty"`
	Ref         string    `json:"ref"`
	Title       string    `json:"title"`
	Risk        string    `json:"risk"`
	CreatedAt   time.Time `json:"created_at"`
}

// PendingBranchAsksMax 是回执头 / branch list 里一次最多列出的待定夺请示条数。
const PendingBranchAsksMax = 50

// PendingBranchAsks 列出路由给这个主会话、仍待处理的请示（最早的在前）。没有时返回空切片（回执头据此一个字都不输出）。
func (s *Store) PendingBranchAsks(ctx context.Context, mainWorkflowID string) ([]PendingBranchAsk, error) {
	wf := strings.TrimSpace(mainWorkflowID)
	if wf == "" {
		return nil, nil
	}
	var rows []model.ConfirmItem
	if err := s.db.WithContext(ctx).Omit("body", "options_json", "answer_json", "escalation_json").
		Where("status = ? AND addressee = ?", model.ConfirmStatusPending, model.ConfirmAddresseeMain(wf)).
		Order("created_at ASC, id ASC").Limit(PendingBranchAsksMax).Find(&rows).Error; err != nil {
		return nil, err
	}
	titles := map[string]string{}
	out := make([]PendingBranchAsk, 0, len(rows))
	for _, r := range rows {
		t, ok := titles[r.WorkflowID]
		if !ok {
			t = s.WorkflowTitleOf(ctx, r.WorkflowID)
			titles[r.WorkflowID] = t
		}
		out = append(out, PendingBranchAsk{Branch: r.WorkflowID, BranchTitle: t, Ref: model.ConfirmRef(r.Seq), Title: r.Title, Risk: r.Risk, CreatedAt: r.CreatedAt})
	}
	return out, nil
}
