package store

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/teacat99/RelayMesh/internal/model"
	"gorm.io/gorm"
)

func (s *Store) CreateTask(ctx context.Context, input model.CreateTaskInput) (*model.Task, error) {
	if input.ProjectID == "" {
		return nil, NewInvalidInputError("project_id is required")
	}
	if err := ValidateSegments(input.Segments); err != nil {
		return nil, NewInvalidInputError(err.Error())
	}

	taskID := input.TaskID
	if taskID == "" {
		taskID = "task-" + uuid.New().String()[:8]
	}

	mode := input.Mode
	if mode == "" {
		mode = "managed_autopilot"
	}

	task := &model.Task{
		ID:                           taskID,
		ProjectID:                    input.ProjectID,
		Title:                        input.Title,
		Mode:                         mode,
		State:                        "active",
		Stages:                       input.Stages,
		Revision:                     1,
		ReportSequence:               0,
		FeedbackSequence:             0,
		AcknowledgedTaskRevision:     0,
		AcknowledgedFeedbackSequence: 0,
		AcknowledgedReportSequence:   0,
		WaitPolicy:                   input.WaitPolicy,
		CreatedAt:                    time.Now(),
		UpdatedAt:                    time.Now(),
	}

	err := s.WithTx(ctx, func(tx *gorm.DB) error {
		var existing model.Task
		if err := tx.Where("id = ?", taskID).First(&existing).Error; err == nil {
			return NewConflictError(fmt.Sprintf("task %q already exists", taskID), existing.Revision)
		}

		if err := tx.Create(task).Error; err != nil {
			return err
		}

		for i, seg := range input.Segments {
			segment := model.Segment{
				TaskID:          taskID,
				Name:            seg.Name,
				Content:         seg.Content,
				Position:        i,
				UpdatedRevision: 1,
				CreatedAt:       time.Now(),
				UpdatedAt:       time.Now(),
			}
			if err := tx.Create(&segment).Error; err != nil {
				return err
			}
		}
		return nil
	})

	if err != nil {
		return nil, err
	}

	return s.GetTask(ctx, input.ProjectID, taskID)
}

func (s *Store) UpdateTask(ctx context.Context, input model.UpdateTaskInput) (*model.MutationResult, error) {
	if input.TaskID == "" {
		return nil, NewInvalidInputError("task_id is required")
	}

	var result model.MutationResult

	err := s.WithTx(ctx, func(tx *gorm.DB) error {
		var task model.Task
		if err := tx.Where("id = ? AND project_id = ?", input.TaskID, input.ProjectID).First(&task).Error; err != nil {
			if err == gorm.ErrRecordNotFound {
				return NewNotFoundError(fmt.Sprintf("task %q not found", input.TaskID))
			}
			return err
		}

		if task.State == "closed" {
			return NewTaskClosedError(fmt.Sprintf("task %q is closed", input.TaskID))
		}

		if input.ExpectedRevision > 0 && task.Revision != input.ExpectedRevision {
			return NewConflictError(fmt.Sprintf("revision conflict for task %q: expected %d, got %d", input.TaskID, input.ExpectedRevision, task.Revision), task.Revision)
		}

		var segment model.Segment
		err := tx.Where("task_id = ? AND name = ?", input.TaskID, input.Segment).First(&segment).Error
		if err != nil && err != gorm.ErrRecordNotFound {
			return err
		}

		var newContent string
		if input.Mode == "patch" {
			if err == gorm.ErrRecordNotFound {
				return NewNotFoundError(fmt.Sprintf("segment %q not found for patch", input.Segment))
			}
			if !strings.Contains(segment.Content, input.OldText) {
				return NewConflictError(fmt.Sprintf("old_text not found in segment %q", input.Segment), task.Revision)
			}
			newContent = strings.Replace(segment.Content, input.OldText, input.NewText, 1)
		} else {
			// replace / upsert
			newContent = input.NewText
		}

		if len(newContent) > model.MaxSegmentBytes {
			return NewInvalidInputError(fmt.Sprintf("segment %q exceeds max bytes (%d > %d)", input.Segment, len(newContent), model.MaxSegmentBytes))
		}

		nextRev := task.Revision + 1
		task.Revision = nextRev
		task.UpdatedAt = time.Now()

		if err := tx.Save(&task).Error; err != nil {
			return err
		}

		if segment.ID == 0 {
			var count int64
			tx.Model(&model.Segment{}).Where("task_id = ?", input.TaskID).Count(&count)
			newSeg := model.Segment{
				TaskID:          input.TaskID,
				Name:            input.Segment,
				Content:         newContent,
				Position:        int(count),
				UpdatedRevision: nextRev,
				CreatedAt:       time.Now(),
				UpdatedAt:       time.Now(),
			}
			if err := tx.Create(&newSeg).Error; err != nil {
				return err
			}
		} else {
			segment.Content = newContent
			segment.UpdatedRevision = nextRev
			segment.UpdatedAt = time.Now()
			if err := tx.Save(&segment).Error; err != nil {
				return err
			}
		}

		result = model.MutationResult{
			TaskID:   input.TaskID,
			Revision: nextRev,
			State:    task.State,
		}
		return nil
	})

	if err != nil {
		return nil, err
	}
	return &result, nil
}

func (s *Store) SetWaitPolicy(ctx context.Context, input model.SetWaitPolicyInput) (*model.MutationResult, error) {
	var result model.MutationResult

	err := s.WithTx(ctx, func(tx *gorm.DB) error {
		var task model.Task
		if err := tx.Where("id = ? AND project_id = ?", input.TaskID, input.ProjectID).First(&task).Error; err != nil {
			if err == gorm.ErrRecordNotFound {
				return NewNotFoundError(fmt.Sprintf("task %q not found", input.TaskID))
			}
			return err
		}

		if task.State == "closed" {
			return NewTaskClosedError(fmt.Sprintf("task %q is closed", input.TaskID))
		}

		if input.ExpectedRevision > 0 && task.Revision != input.ExpectedRevision {
			return NewConflictError(fmt.Sprintf("revision conflict for task %q: expected %d, got %d", input.TaskID, input.ExpectedRevision, task.Revision), task.Revision)
		}

		task.WaitPolicy = input.WaitPolicy
		task.Revision++
		task.UpdatedAt = time.Now()

		if err := tx.Save(&task).Error; err != nil {
			return err
		}

		result = model.MutationResult{
			TaskID:   input.TaskID,
			Revision: task.Revision,
			State:    task.State,
		}
		return nil
	})

	if err != nil {
		return nil, err
	}
	return &result, nil
}

func (s *Store) SendFeedback(ctx context.Context, input model.SendFeedbackInput) (*model.Feedback, error) {
	if err := ValidatePathReferences(input.References); err != nil {
		return nil, NewInvalidInputError(err.Error())
	}

	var feedback model.Feedback

	err := s.WithTx(ctx, func(tx *gorm.DB) error {
		var task model.Task
		if err := tx.Where("id = ? AND project_id = ?", input.TaskID, input.ProjectID).First(&task).Error; err != nil {
			if err == gorm.ErrRecordNotFound {
				return NewNotFoundError(fmt.Sprintf("task %q not found", input.TaskID))
			}
			return err
		}

		if task.State == "closed" {
			return NewTaskClosedError(fmt.Sprintf("task %q is closed", input.TaskID))
		}

		if input.IdempotencyKey != "" {
			var existing model.Feedback
			if err := tx.Where("task_id = ? AND idempotency_key = ?", input.TaskID, input.IdempotencyKey).First(&existing).Error; err == nil {
				feedback = existing
				return nil
			}
		}

		nextSeq := task.FeedbackSequence + 1
		task.FeedbackSequence = nextSeq
		task.UpdatedAt = time.Now()

		if err := tx.Save(&task).Error; err != nil {
			return err
		}

		source := input.Source
		if source == "" {
			source = "human"
		}

		feedback = model.Feedback{
			TaskID:         input.TaskID,
			Sequence:       nextSeq,
			TaskRevision:   task.Revision,
			Source:         source,
			Body:           input.Body,
			References:     input.References,
			IdempotencyKey: input.IdempotencyKey,
			CreatedAt:      time.Now(),
		}

		return tx.Create(&feedback).Error
	})

	if err != nil {
		return nil, err
	}
	return &feedback, nil
}

func (s *Store) UpdateStages(ctx context.Context, input model.UpdateStagesInput) (*model.MutationResult, error) {
	if input.TaskID == "" {
		return nil, NewInvalidInputError("task_id is required")
	}

	var result model.MutationResult

	err := s.WithTx(ctx, func(tx *gorm.DB) error {
		var task model.Task
		if err := tx.Where("id = ? AND project_id = ?", input.TaskID, input.ProjectID).First(&task).Error; err != nil {
			if err == gorm.ErrRecordNotFound {
				return NewNotFoundError(fmt.Sprintf("task %q not found", input.TaskID))
			}
			return err
		}

		if task.State == "closed" {
			return NewTaskClosedError(fmt.Sprintf("task %q is closed", input.TaskID))
		}

		if input.ExpectedRevision > 0 && task.Revision != input.ExpectedRevision {
			return NewConflictError(fmt.Sprintf("revision conflict for task %q: expected %d, got %d", input.TaskID, input.ExpectedRevision, task.Revision), task.Revision)
		}

		task.Stages = input.Stages
		if input.CurrentStageID != "" {
			task.CurrentStageID = input.CurrentStageID
		}
		task.Revision++
		task.UpdatedAt = time.Now()

		if err := tx.Save(&task).Error; err != nil {
			return err
		}

		result = model.MutationResult{
			TaskID:   input.TaskID,
			Revision: task.Revision,
			State:    task.State,
		}
		return nil
	})

	if err != nil {
		return nil, err
	}
	return &result, nil
}

func (s *Store) UpdateTaskHeartbeat(ctx context.Context, projectID, taskID, role, sessionID string) error {
	now := time.Now()
	updates := map[string]interface{}{
		"updated_at": now,
	}
	if role == "commander" {
		updates["commander_session_id"] = sessionID
		updates["commander_heartbeat_at"] = now
	} else if role == "executor" {
		updates["executor_session_id"] = sessionID
		updates["executor_heartbeat_at"] = now
	}
	return s.db.WithContext(ctx).Model(&model.Task{}).
		Where("id = ? AND project_id = ?", taskID, projectID).
		Updates(updates).Error
}

func (s *Store) AddReport(ctx context.Context, input model.AddReportInput) (*model.ReportResult, error) {
	if err := ValidatePathReferences(input.References); err != nil {
		return nil, NewInvalidInputError(err.Error())
	}

	var result model.ReportResult

	err := s.WithTx(ctx, func(tx *gorm.DB) error {
		var task model.Task
		if err := tx.Where("id = ? AND project_id = ?", input.TaskID, input.ProjectID).First(&task).Error; err != nil {
			if err == gorm.ErrRecordNotFound {
				return NewNotFoundError(fmt.Sprintf("task %q not found", input.TaskID))
			}
			return err
		}

		if task.State == "closed" {
			return NewTaskClosedError(fmt.Sprintf("task %q is closed", input.TaskID))
		}

		if input.AcknowledgedTaskRevision > task.AcknowledgedTaskRevision {
			task.AcknowledgedTaskRevision = input.AcknowledgedTaskRevision
		}
		if input.AcknowledgedFeedbackSequence > task.AcknowledgedFeedbackSequence {
			task.AcknowledgedFeedbackSequence = input.AcknowledgedFeedbackSequence
		}

		var report model.Report
		if input.IdempotencyKey != "" {
			var existing model.Report
			if err := tx.Where("task_id = ? AND idempotency_key = ?", input.TaskID, input.IdempotencyKey).First(&existing).Error; err == nil {
				report = existing
			}
		}

		if report.ID == 0 {
			nextSeq := task.ReportSequence + 1
			task.ReportSequence = nextSeq
			task.UpdatedAt = time.Now()

			report = model.Report{
				TaskID:                   input.TaskID,
				Sequence:                 nextSeq,
				AcknowledgedTaskRevision: input.AcknowledgedTaskRevision,
				Kind:                     input.Kind,
				Body:                     input.Body,
				References:               input.References,
				IdempotencyKey:           input.IdempotencyKey,
				CreatedAt:                time.Now(),
			}

			if err := tx.Create(&report).Error; err != nil {
				return err
			}
		}

		if err := tx.Save(&task).Error; err != nil {
			return err
		}

		// Fetch unread feedback if any
		var newFeedbacks []model.Feedback
		tx.Where("task_id = ? AND sequence > ?", input.TaskID, input.AcknowledgedFeedbackSequence).Order("sequence ASC").Find(&newFeedbacks)

		instruction := task.WaitPolicy.WaitInstruction
		if instruction == "" {
			instruction = fmt.Sprintf("请等待 %d 分钟后再次调用 check_feedback 检查反馈。", task.WaitPolicy.AfterMinutes)
		} else {
			instruction = strings.ReplaceAll(instruction, "{minutes}", fmt.Sprintf("%d", task.WaitPolicy.AfterMinutes))
		}

		result = model.ReportResult{
			Report:           report,
			FeedbackSequence: task.FeedbackSequence,
			Feedback:         newFeedbacks,
			Wait: model.WaitStatus{
				State:                "waiting",
				SourceReportSequence: report.Sequence,
				NoFeedbackChecks:     0,
				MaxChecks:            task.WaitPolicy.MaxNoFeedbackChecks,
				AfterMinutes:         task.WaitPolicy.AfterMinutes,
				Instruction:          instruction,
			},
		}
		return nil
	})

	if err != nil {
		return nil, err
	}
	return &result, nil
}

func (s *Store) Sync(ctx context.Context, input model.SyncInput) (*model.SyncResult, error) {
	var result model.SyncResult

	err := s.WithTx(ctx, func(tx *gorm.DB) error {
		var task model.Task
		if err := tx.Where("id = ? AND project_id = ?", input.TaskID, input.ProjectID).First(&task).Error; err != nil {
			if err == gorm.ErrRecordNotFound {
				return NewNotFoundError(fmt.Sprintf("task %q not found", input.TaskID))
			}
			return err
		}

		if input.AcknowledgedTaskRevision > task.AcknowledgedTaskRevision {
			task.AcknowledgedTaskRevision = input.AcknowledgedTaskRevision
		}
		if input.AcknowledgedFeedbackSequence > task.AcknowledgedFeedbackSequence {
			task.AcknowledgedFeedbackSequence = input.AcknowledgedFeedbackSequence
		}
		tx.Save(&task)

		var segments []model.Segment
		if input.KnownTaskRevision < task.Revision {
			tx.Where("task_id = ?", input.TaskID).Order("position ASC").Find(&segments)
		}

		var feedbacks []model.Feedback
		tx.Where("task_id = ? AND sequence > ?", input.TaskID, input.AfterFeedbackSequence).Order("sequence ASC").Find(&feedbacks)

		result = model.SyncResult{
			TaskID:                       task.ID,
			State:                        task.State,
			Revision:                     task.Revision,
			ReportSequence:               task.ReportSequence,
			FeedbackSequence:             task.FeedbackSequence,
			AcknowledgedTaskRevision:     task.AcknowledgedTaskRevision,
			AcknowledgedFeedbackSequence: task.AcknowledgedFeedbackSequence,
			Segments:                     segments,
			Feedback:                     feedbacks,
		}
		return nil
	})

	if err != nil {
		return nil, err
	}
	return &result, nil
}

func (s *Store) CheckFeedback(ctx context.Context, input model.CheckFeedbackInput) (*model.CheckFeedbackResult, error) {
	var result model.CheckFeedbackResult

	err := s.WithTx(ctx, func(tx *gorm.DB) error {
		var task model.Task
		if err := tx.Where("id = ? AND project_id = ?", input.TaskID, input.ProjectID).First(&task).Error; err != nil {
			if err == gorm.ErrRecordNotFound {
				return NewNotFoundError(fmt.Sprintf("task %q not found", input.TaskID))
			}
			return err
		}

		if input.AcknowledgedTaskRevision > task.AcknowledgedTaskRevision {
			task.AcknowledgedTaskRevision = input.AcknowledgedTaskRevision
		}
		if input.AcknowledgedFeedbackSequence > task.AcknowledgedFeedbackSequence {
			task.AcknowledgedFeedbackSequence = input.AcknowledgedFeedbackSequence
		}
		tx.Save(&task)

		var feedbacks []model.Feedback
		tx.Where("task_id = ? AND sequence > ?", input.TaskID, input.AfterFeedbackSequence).Order("sequence ASC").Find(&feedbacks)

		instruction := task.WaitPolicy.WaitInstruction
		if len(feedbacks) == 0 {
			if instruction == "" {
				instruction = fmt.Sprintf("暂无新反馈；请等待 %d 分钟后再次调用。", task.WaitPolicy.AfterMinutes)
			} else {
				instruction = strings.ReplaceAll(instruction, "{minutes}", fmt.Sprintf("%d", task.WaitPolicy.AfterMinutes))
			}
		} else {
			instruction = "已接收到新反馈；请阅读后继续推进任务。"
		}

		result = model.CheckFeedbackResult{
			Feedback: feedbacks,
			Wait: model.WaitStatus{
				State:            "waiting",
				NoFeedbackChecks: 0,
				MaxChecks:        task.WaitPolicy.MaxNoFeedbackChecks,
				AfterMinutes:     task.WaitPolicy.AfterMinutes,
				Instruction:      instruction,
			},
		}
		return nil
	})

	if err != nil {
		return nil, err
	}
	return &result, nil
}

func (s *Store) GetTask(ctx context.Context, projectID, taskID string) (*model.Task, error) {
	var task model.Task
	err := s.db.WithContext(ctx).Preload("Segments", func(db *gorm.DB) *gorm.DB {
		return db.Order("position ASC")
	}).Where("id = ? AND project_id = ?", taskID, projectID).First(&task).Error

	if err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, NewNotFoundError(fmt.Sprintf("task %q not found", taskID))
		}
		return nil, err
	}
	return &task, nil
}

func (s *Store) ListTasks(ctx context.Context, projectID, state, cursor string, limit int, updatesOnly bool) (*model.TaskPage, error) {
	if limit <= 0 || limit > model.MaxPageSize {
		limit = 20
	}

	query := s.db.WithContext(ctx).Model(&model.Task{}).Where("project_id = ?", projectID)
	if state != "" {
		query = query.Where("state = ?", state)
	}
	if updatesOnly {
		query = query.Where("report_sequence > acknowledged_report_sequence")
	}
	if cursor != "" {
		query = query.Where("id > ?", cursor)
	}

	var tasks []model.Task
	if err := query.Order("id ASC").Limit(limit + 1).Find(&tasks).Error; err != nil {
		return nil, err
	}

	var nextCursor string
	if len(tasks) > limit {
		nextCursor = tasks[limit].ID
		tasks = tasks[:limit]
	}

	summaries := make([]model.TaskSummary, len(tasks))
	for i, t := range tasks {
		unread := t.ReportSequence - t.AcknowledgedReportSequence
		if unread < 0 {
			unread = 0
		}
		summaries[i] = model.TaskSummary{
			ID:                         t.ID,
			ProjectID:                  t.ProjectID,
			Title:                      t.Title,
			Mode:                       t.Mode,
			State:                      t.State,
			CurrentStageID:             t.CurrentStageID,
			Stages:                     t.Stages,
			CommanderSessionID:         t.CommanderSessionID,
			ExecutorSessionID:          t.ExecutorSessionID,
			Revision:                   t.Revision,
			ReportSequence:             t.ReportSequence,
			FeedbackSequence:           t.FeedbackSequence,
			AcknowledgedReportSequence: t.AcknowledgedReportSequence,
			UnreadReportCount:          unread,
			UpdatedAt:                  t.UpdatedAt,
		}
	}

	return &model.TaskPage{
		Tasks:      summaries,
		NextCursor: nextCursor,
	}, nil
}

func (s *Store) ReadReports(ctx context.Context, projectID, taskID string, afterSequence int64, limit int) (*model.ReportPage, error) {
	if limit <= 0 || limit > model.MaxPageSize {
		limit = 50
	}

	var reports []model.Report
	err := s.db.WithContext(ctx).
		Where("task_id = ? AND sequence > ?", taskID, afterSequence).
		Order("sequence ASC").
		Limit(limit + 1).
		Find(&reports).Error

	if err != nil {
		return nil, err
	}

	var nextSeq int64 = 0
	hasMore := false
	if len(reports) > limit {
		hasMore = true
		nextSeq = reports[limit-1].Sequence
		reports = reports[:limit]
	} else if len(reports) > 0 {
		nextSeq = reports[len(reports)-1].Sequence
	}

	return &model.ReportPage{
		Reports:      reports,
		NextSequence: nextSeq,
		HasMore:      hasMore,
	}, nil
}

func (s *Store) AckReports(ctx context.Context, projectID, taskID string, throughSequence int64) (*model.AckSummary, error) {
	var summary model.AckSummary

	err := s.WithTx(ctx, func(tx *gorm.DB) error {
		var task model.Task
		if err := tx.Where("id = ? AND project_id = ?", taskID, projectID).First(&task).Error; err != nil {
			if err == gorm.ErrRecordNotFound {
				return NewNotFoundError(fmt.Sprintf("task %q not found", taskID))
			}
			return err
		}

		if throughSequence > task.AcknowledgedReportSequence {
			if throughSequence > task.ReportSequence {
				throughSequence = task.ReportSequence
			}
			task.AcknowledgedReportSequence = throughSequence
			task.UpdatedAt = time.Now()
			if err := tx.Save(&task).Error; err != nil {
				return err
			}
		}

		unread := task.ReportSequence - task.AcknowledgedReportSequence
		if unread < 0 {
			unread = 0
		}

		summary = model.AckSummary{
			TaskID:                     task.ID,
			AcknowledgedReportSequence: task.AcknowledgedReportSequence,
			UnreadReportCount:          unread,
		}
		return nil
	})

	if err != nil {
		return nil, err
	}
	return &summary, nil
}

func (s *Store) ReadFeedbacks(ctx context.Context, projectID, taskID string, afterSequence int64, limit int) ([]model.Feedback, error) {
	if limit <= 0 || limit > model.MaxPageSize {
		limit = 100
	}

	var feedbacks []model.Feedback
	err := s.db.WithContext(ctx).
		Where("task_id = ? AND sequence > ?", taskID, afterSequence).
		Order("sequence ASC").
		Limit(limit).
		Find(&feedbacks).Error

	if err != nil {
		return nil, err
	}
	return feedbacks, nil
}

func (s *Store) CloseTask(ctx context.Context, projectID, taskID string, expectedRevision int64, idempotencyKey string) (*model.MutationResult, error) {
	var result model.MutationResult

	err := s.WithTx(ctx, func(tx *gorm.DB) error {
		var task model.Task
		if err := tx.Where("id = ? AND project_id = ?", taskID, projectID).First(&task).Error; err != nil {
			if err == gorm.ErrRecordNotFound {
				return NewNotFoundError(fmt.Sprintf("task %q not found", taskID))
			}
			return err
		}

		if expectedRevision > 0 && task.Revision != expectedRevision {
			return NewConflictError(fmt.Sprintf("revision conflict for task %q: expected %d, got %d", taskID, expectedRevision, task.Revision), task.Revision)
		}

		task.State = "closed"
		task.Revision++
		task.UpdatedAt = time.Now()

		if err := tx.Save(&task).Error; err != nil {
			return err
		}

		result = model.MutationResult{
			TaskID:   task.ID,
			Revision: task.Revision,
			State:    task.State,
		}
		return nil
	})

	if err != nil {
		return nil, err
	}
	return &result, nil
}
