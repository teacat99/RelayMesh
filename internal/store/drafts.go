package store

import (
	"context"
	"time"

	"github.com/teacat99/RelayMesh/internal/model"
	"gorm.io/gorm"
)

// GetWorkflowDraft 获取工作流对应的草稿数据，若不存在返回 nil, nil
func (s *Store) GetWorkflowDraft(ctx context.Context, workflowID string) (*model.WorkflowDraft, error) {
	var draft model.WorkflowDraft
	err := s.db.WithContext(ctx).Where("workflow_id = ?", workflowID).First(&draft).Error
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, nil
		}
		return nil, err
	}
	return &draft, nil
}

// SaveWorkflowDraft 保存或更新工作流草稿数据
func (s *Store) SaveWorkflowDraft(ctx context.Context, workflowID string, activeIndex int, draftsJSON string) (*model.WorkflowDraft, error) {
	draft := model.WorkflowDraft{
		WorkflowID:  workflowID,
		ActiveIndex: activeIndex,
		DraftsJSON:  draftsJSON,
		UpdatedAt:   time.Now(),
	}

	err := s.WithTx(ctx, func(tx *gorm.DB) error {
		return tx.Save(&draft).Error
	})
	if err != nil {
		return nil, err
	}
	return &draft, nil
}

// DeleteWorkflowDraft 删除工作流草稿数据
func (s *Store) DeleteWorkflowDraft(ctx context.Context, workflowID string) error {
	return s.WithTx(ctx, func(tx *gorm.DB) error {
		return tx.Where("workflow_id = ?", workflowID).Delete(&model.WorkflowDraft{}).Error
	})
}
