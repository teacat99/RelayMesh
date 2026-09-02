package store

import (
	"context"
	"time"

	"github.com/teacat99/RelayMesh/internal/model"
	"gorm.io/gorm"
)

func (s *Store) SaveNote(ctx context.Context, note *model.WorkflowNote) error {
	if note.NoteKey != "" && note.WorkflowID != "" {
		var existing model.WorkflowNote
		err := s.db.WithContext(ctx).
			Where("workflow_id = ? AND note_key = ?", note.WorkflowID, note.NoteKey).
			First(&existing).Error
		if err == nil {
			return s.db.WithContext(ctx).
				Model(&existing).
				Updates(map[string]any{
					"content":    note.Content,
					"session_id": note.SessionID,
					"updated_at": time.Now(),
				}).Error
		}
	}
	return s.db.WithContext(ctx).Create(note).Error
}

func (s *Store) GetNote(ctx context.Context, id uint) (*model.WorkflowNote, error) {
	var note model.WorkflowNote
	err := s.db.WithContext(ctx).First(&note, id).Error
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, nil
		}
		return nil, err
	}
	return &note, nil
}

func (s *Store) GetNoteByKey(ctx context.Context, workflowID, noteKey string) (*model.WorkflowNote, error) {
	var note model.WorkflowNote
	err := s.db.WithContext(ctx).
		Where("workflow_id = ? AND note_key = ?", workflowID, noteKey).
		First(&note).Error
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, nil
		}
		return nil, err
	}
	return &note, nil
}

func (s *Store) ListNotes(ctx context.Context, workflowID string, sessionID string, limit int) ([]model.WorkflowNote, error) {
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	q := s.db.WithContext(ctx)
	if workflowID != "" {
		q = q.Where("workflow_id = ?", workflowID)
	}
	if sessionID != "" {
		q = q.Where("session_id = ?", sessionID)
	}
	var notes []model.WorkflowNote
	err := q.Order("updated_at DESC").Limit(limit).Find(&notes).Error
	return notes, err
}

func (s *Store) UpdateNote(ctx context.Context, id uint, content string) error {
	return s.db.WithContext(ctx).
		Model(&model.WorkflowNote{}).
		Where("id = ?", id).
		Updates(map[string]any{
			"content":    content,
			"updated_at": time.Now(),
		}).Error
}

func (s *Store) DeleteNote(ctx context.Context, id uint) error {
	return s.db.WithContext(ctx).Delete(&model.WorkflowNote{}, id).Error
}

func (s *Store) ListWorkflowSummaries(ctx context.Context, limit int) ([]map[string]any, error) {
	if limit <= 0 || limit > 50 {
		limit = 20
	}
	var results []struct {
		WorkflowID string
		Sessions   int64
		LastActive time.Time
	}

	err := s.db.WithContext(ctx).
		Model(&model.FeedbackSession{}).
		Select("workflow_id, COUNT(DISTINCT session_id) as sessions, MAX(updated_at) as last_active").
		Where("workflow_id != ''").
		Group("workflow_id").
		Order("last_active DESC").
		Limit(limit).
		Find(&results).Error

	if err != nil {
		return nil, err
	}

	summaries := make([]map[string]any, 0, len(results))
	for _, r := range results {
		summary := map[string]any{
			"workflow_id":   r.WorkflowID,
			"session_count": r.Sessions,
			"last_active":   r.LastActive,
		}

		cp, _ := s.GetLatestCheckpoint(ctx, r.WorkflowID)
		if cp != nil {
			summary["latest_checkpoint_revision"] = cp.Revision
			summary["latest_checkpoint_status"] = cp.Status
			summary["latest_checkpoint_at"] = cp.CreatedAt
		}

		phase, _, _ := s.GetWorkflowPhaseWithDefaults(ctx, r.WorkflowID)
		if phase != "" {
			summary["current_phase"] = phase
		}

		summaries = append(summaries, summary)
	}
	return summaries, nil
}
