package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"

	"gorm.io/gorm"

	"github.com/teacat99/RelayMesh/v2/internal/model"
)

// 会话级常驻提示词的存取（#232，模型见 model.WorkflowPrompt）。
// 写入口只有 Web 登录态（internal/api/workflow_prompts.go）；MCP 侧只读（回执头的 resident: 行、workflow_context 的 resident_get）。

// NormalizePromptText 把用户写的提示词整理成存库的形态：统一换行为 \n、去掉除换行与制表符外的控制字符、去掉首尾空白，
// 并检查字数上限（按字符计）。空串合法，表示清除。
func NormalizePromptText(s string, max int) (string, error) {
	s = strings.ReplaceAll(strings.ReplaceAll(s, "\r\n", "\n"), "\r", "\n")
	s = strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' {
			return r
		}
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, s)
	s = strings.TrimSpace(s)
	if n := len([]rune(s)); n > max {
		return "", NewInvalidInputError(fmt.Sprintf("最多 %d 个字，现在是 %d 个字，请删减后再保存", max, n))
	}
	return s, nil
}

// GetWorkflowPrompt 读取某个工作流的提示词设置；没有配置过时返回只有 WorkflowID 的零值（Rev 为 0）。
// 热路径：每条回执头都会问一次，没配置过的工作流只多一次主键查询。
func (s *Store) GetWorkflowPrompt(ctx context.Context, workflowID string) (model.WorkflowPrompt, error) {
	wf := strings.TrimSpace(workflowID)
	row := model.WorkflowPrompt{WorkflowID: wf}
	if wf == "" {
		return row, nil
	}
	var got model.WorkflowPrompt
	err := s.db.WithContext(ctx).Where("workflow_id = ?", wf).Take(&got).Error
	switch {
	case err == nil:
		return got, nil
	case errors.Is(err, gorm.ErrRecordNotFound):
		return row, nil
	default:
		return row, err
	}
}

// SaveResidentPrompt 保存常驻提示词。内容与现有的一致时什么也不做（changed=false，不涨版本）；
// 否则版本号加一并写一条历史版本（每个会话保留最近 model.PromptRevisionKeep 个）。
// 空串表示清除：同样涨版本，让智能体下一次回执知道「常驻指令已清除」。从没配置过的会话保存空串仍是什么也不做。
func (s *Store) SaveResidentPrompt(ctx context.Context, workflowID, text, actor string, now time.Time) (model.WorkflowPrompt, bool, error) {
	wf := strings.TrimSpace(workflowID)
	if wf == "" {
		return model.WorkflowPrompt{}, false, NewInvalidInputError("workflow_id 不能为空")
	}
	text, err := NormalizePromptText(text, model.ResidentPromptMax)
	if err != nil {
		return model.WorkflowPrompt{}, false, err
	}
	actor = strings.TrimSpace(actor)
	if len(actor) > 64 {
		actor = actor[:64]
	}
	var out model.WorkflowPrompt
	changed := false
	err = s.WithTx(ctx, func(tx *gorm.DB) error {
		var row model.WorkflowPrompt
		err := tx.Where("workflow_id = ?", wf).Take(&row).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			row = model.WorkflowPrompt{WorkflowID: wf}
		} else if err != nil {
			return err
		}
		out = row
		if row.Resident == text {
			return nil
		}
		row.Resident = text
		row.ResidentRev++
		row.UpdatedAt = now
		row.UpdatedBy = actor
		if err := tx.Save(&row).Error; err != nil {
			return err
		}
		rev := model.WorkflowPromptRevision{
			WorkflowID: wf, Field: model.PromptFieldResident, Rev: row.ResidentRev,
			Text: text, Actor: actor, CreatedAt: now,
		}
		if err := tx.Create(&rev).Error; err != nil {
			return err
		}
		if err := prunePromptRevisions(tx, wf, model.PromptFieldResident); err != nil {
			return err
		}
		out = row
		changed = true
		return nil
	})
	if err != nil {
		return model.WorkflowPrompt{}, false, err
	}
	return out, changed, nil
}

// prunePromptRevisions 只保留某个会话某个字段最近 PromptRevisionKeep 个版本。
func prunePromptRevisions(tx *gorm.DB, wf, field string) error {
	var keep []uint
	if err := tx.Model(&model.WorkflowPromptRevision{}).
		Where("workflow_id = ? AND field = ?", wf, field).
		Order("id DESC").Limit(model.PromptRevisionKeep).Pluck("id", &keep).Error; err != nil {
		return err
	}
	if len(keep) < model.PromptRevisionKeep {
		return nil
	}
	return tx.Where("workflow_id = ? AND field = ? AND id NOT IN ?", wf, field, keep).
		Delete(&model.WorkflowPromptRevision{}).Error
}

// MarkResidentTold 记下「智能体已经见到常驻提示词的第 rev 版」；只往前走，不会把已告知的版本改小。
func (s *Store) MarkResidentTold(ctx context.Context, workflowID string, rev int) error {
	wf := strings.TrimSpace(workflowID)
	if wf == "" || rev <= 0 {
		return nil
	}
	return s.db.WithContext(ctx).Model(&model.WorkflowPrompt{}).
		Where("workflow_id = ? AND resident_told_rev < ?", wf, rev).
		Update("resident_told_rev", rev).Error
}

// ListPromptRevisions 返回某个会话某个字段的历史版本，新的在前，最多 PromptRevisionKeep 个。
func (s *Store) ListPromptRevisions(ctx context.Context, workflowID, field string) ([]model.WorkflowPromptRevision, error) {
	var list []model.WorkflowPromptRevision
	err := s.db.WithContext(ctx).
		Where("workflow_id = ? AND field = ?", strings.TrimSpace(workflowID), field).
		Order("id DESC").Limit(model.PromptRevisionKeep).Find(&list).Error
	return list, err
}

// RestoreResidentRevision 把某个历史版本的文字存成新的当前版本（版本号继续往前走，不改写历史）。
func (s *Store) RestoreResidentRevision(ctx context.Context, workflowID string, revisionID uint, actor string, now time.Time) (model.WorkflowPrompt, bool, error) {
	wf := strings.TrimSpace(workflowID)
	var rev model.WorkflowPromptRevision
	err := s.db.WithContext(ctx).
		Where("id = ? AND workflow_id = ? AND field = ?", revisionID, wf, model.PromptFieldResident).Take(&rev).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return model.WorkflowPrompt{}, false, NewNotFoundError("没有这个历史版本")
	}
	if err != nil {
		return model.WorkflowPrompt{}, false, err
	}
	return s.SaveResidentPrompt(ctx, wf, rev.Text, actor, now)
}
