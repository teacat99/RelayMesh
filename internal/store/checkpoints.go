package store

import (
	"context"

	"github.com/teacat99/RelayMesh/internal/model"
	"gorm.io/gorm"
)

func (s *Store) SaveCheckpoint(ctx context.Context, cp *model.WorkflowCheckpoint) error {
	return s.WithTx(ctx, func(tx *gorm.DB) error {
		var maxRev int
		tx.Model(&model.WorkflowCheckpoint{}).
			Where("workflow_id = ?", cp.WorkflowID).
			Select("COALESCE(MAX(revision), 0)").
			Scan(&maxRev)
		cp.Revision = maxRev + 1
		if cp.Status == "" {
			cp.Status = "submitted"
		}
		return tx.Create(cp).Error
	})
}

func (s *Store) GetLatestCheckpoint(ctx context.Context, workflowID string) (*model.WorkflowCheckpoint, error) {
	var cp model.WorkflowCheckpoint
	err := s.db.WithContext(ctx).
		Where("workflow_id = ?", workflowID).
		Order("revision DESC").
		First(&cp).Error
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, nil
		}
		return nil, err
	}
	return &cp, nil
}

func (s *Store) GetCheckpointByRevision(ctx context.Context, workflowID string, revision int) (*model.WorkflowCheckpoint, error) {
	var cp model.WorkflowCheckpoint
	err := s.db.WithContext(ctx).
		Where("workflow_id = ? AND revision = ?", workflowID, revision).
		First(&cp).Error
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, nil
		}
		return nil, err
	}
	return &cp, nil
}

func (s *Store) ListCheckpoints(ctx context.Context, workflowID string, limit int) ([]model.WorkflowCheckpoint, error) {
	if limit <= 0 || limit > 50 {
		limit = 20
	}
	var cps []model.WorkflowCheckpoint
	err := s.db.WithContext(ctx).
		Where("workflow_id = ?", workflowID).
		Order("revision DESC").
		Limit(limit).
		Find(&cps).Error
	return cps, err
}

func (s *Store) UpdateCheckpointStatus(ctx context.Context, workflowID string, revision int, status string) error {
	return s.db.WithContext(ctx).
		Model(&model.WorkflowCheckpoint{}).
		Where("workflow_id = ? AND revision = ?", workflowID, revision).
		Update("status", status).Error
}

func (s *Store) SupersedeOldCheckpoints(ctx context.Context, workflowID string, currentRevision int) error {
	return s.db.WithContext(ctx).
		Model(&model.WorkflowCheckpoint{}).
		Where("workflow_id = ? AND revision < ? AND status NOT IN ('superseded', 'accepted')", workflowID, currentRevision).
		Update("status", "superseded").Error
}
