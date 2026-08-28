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

type CreateSessionInput struct {
	SessionID        string              `json:"session_id,omitempty"`
	WorkflowID       string              `json:"workflow_id,omitempty"`
	HostName         string              `json:"host_name,omitempty"`
	ProjectDirectory string              `json:"project_directory"`
	Title            string              `json:"title,omitempty"`
	Summary          string              `json:"summary"`
	UserPresence     string              `json:"user_presence,omitempty"`
	TimeoutSeconds   int                 `json:"timeout_seconds,omitempty"`
}

type SubmitFeedbackInput struct {
	SessionID    string              `json:"session_id"`
	ResponseText string              `json:"response_text"`
	UserMessages []string            `json:"user_messages,omitempty"`
	Images       []model.SessionImage `json:"images,omitempty"`
}

func (s *Store) CreateFeedbackSession(ctx context.Context, input CreateSessionInput) (*model.FeedbackSession, error) {
	if input.Summary == "" {
		return nil, NewInvalidInputError("summary is required and cannot be empty")
	}

	sessionID := input.SessionID
	if sessionID == "" {
		sessionID = "sess-" + uuid.New().String()[:8]
	}

	timeoutSec := input.TimeoutSeconds
	if timeoutSec <= 0 {
		timeoutSec = 120
	}

	now := time.Now()
	deadline := now.Add(time.Duration(timeoutSec) * time.Second)

	var session model.FeedbackSession

	err := s.WithTx(ctx, func(tx *gorm.DB) error {
		// 如果该工作流已存在用户自定义固定的标题，优先锁定使用用户自定义标题
		effectiveTitle := input.Title
		if input.WorkflowID != "" {
			var customTitleSetting model.SystemSetting
			if err := tx.Where("key = ?", "wf_custom_title:"+input.WorkflowID).First(&customTitleSetting).Error; err == nil && customTitleSetting.Value != "" {
				effectiveTitle = customTitleSetting.Value
			}
		}

		// If workflow_id provided, check if there is an existing pending session for this workflow
		if input.WorkflowID != "" {
			var existing model.FeedbackSession
			if err := tx.Where("workflow_id = ? AND status = 'pending'", input.WorkflowID).First(&existing).Error; err == nil {
				existing.Summary = input.Summary
				if effectiveTitle != "" {
					existing.Title = effectiveTitle
				}
				if input.HostName != "" {
					existing.HostName = input.HostName
				}
				if input.ProjectDirectory != "" {
					existing.ProjectDirectory = input.ProjectDirectory
				}
				existing.TimeoutSeconds = timeoutSec
				existing.DeadlineAt = &deadline
				existing.UpdatedAt = now
				if err := tx.Save(&existing).Error; err != nil {
					return err
				}
				session = existing
				return nil
			}
		}

		// 只有在 workflow_id 为空或者指定了新的独立单次会话时才复用历史 pending；
		// 若指定了 workflow_id 且当前没有 pending 轮次，必须新建一条 session（作为该工作流的新一轮对话），绝不就地覆盖已 completed 的历史轮次！

		// 全局系统配置作为基线默认值
		globalSettings, _ := s.GetGlobalAppSettingsWithDB(ctx, tx)

		presence := input.UserPresence
		if presence == "" {
			if globalSettings != nil && globalSettings.UserPresence != "" {
				presence = globalSettings.UserPresence
			} else {
				presence = "online"
			}
		}
		hostName := input.HostName
		if hostName == "" && globalSettings != nil && globalSettings.HostName != "" {
			hostName = globalSettings.HostName
		}
		promptWaitMin := 2
		waitCountdownMin := 2
		maxChecks := 24
		if globalSettings != nil {
			if globalSettings.PromptWaitMinutes > 0 {
				promptWaitMin = globalSettings.PromptWaitMinutes
			}
			if globalSettings.DefaultWaitCountdownMinutes >= 0 {
				waitCountdownMin = globalSettings.DefaultWaitCountdownMinutes
			}
			if globalSettings.MaxNoFeedbackChecks > 0 {
				maxChecks = globalSettings.MaxNoFeedbackChecks
			}
		}

		// 若指定了 WorkflowID，检查是否存在历史轮次以继承用户此前调整的配置
		if input.WorkflowID != "" {
			var lastSess model.FeedbackSession
			if err := tx.Where("workflow_id = ?", input.WorkflowID).Order("created_at DESC").First(&lastSess).Error; err == nil {
				if input.UserPresence == "" && lastSess.UserPresence != "" {
					presence = lastSess.UserPresence
				}
				if input.HostName == "" && lastSess.HostName != "" {
					hostName = lastSess.HostName
				}
				if lastSess.PromptWaitMinutes > 0 {
					promptWaitMin = lastSess.PromptWaitMinutes
				}
				if lastSess.WaitCountdownMinutes >= 0 {
					waitCountdownMin = lastSess.WaitCountdownMinutes
				}
				if lastSess.MaxNoFeedbackChecks >= 0 {
					maxChecks = lastSess.MaxNoFeedbackChecks
				}
			}
		}

		// 检查是否有提前暂存的待处理用户反馈 (QueuedFeedback - 提前秒回直取机制)
		var queuedFeedback model.QueuedFeedback
		hasQueued := false
		if input.WorkflowID != "" {
			if err := tx.Where("workflow_id = ? AND status = 'pending'", input.WorkflowID).Order("created_at ASC").First(&queuedFeedback).Error; err == nil {
				hasQueued = true
			}
		}

		sessionStatus := "pending"
		consumedByAI := false
		respText := ""
		userMsgs := model.StringArray{}
		imgs := model.SessionImages{}

		if hasQueued {
			sessionStatus = "completed"
			consumedByAI = true
			respText = queuedFeedback.ResponseText
			userMsgs = queuedFeedback.UserMessages
			imgs = queuedFeedback.Images
			queuedFeedback.Status = "consumed"
			queuedFeedback.UpdatedAt = now
			_ = tx.Save(&queuedFeedback).Error
		}

		session = model.FeedbackSession{
			ID:                   sessionID,
			WorkflowID:           input.WorkflowID,
			HostName:             hostName,
			ProjectDirectory:     input.ProjectDirectory,
			Title:                effectiveTitle,
			Summary:              input.Summary,
			Status:               sessionStatus,
			UserPresence:         presence,
			ResponseText:         respText,
			UserMessages:         userMsgs,
			Images:               imgs,
			ConsumedByAI:         consumedByAI,
			TimeoutSeconds:       timeoutSec,
			NoFeedbackChecks:     0,
			MaxNoFeedbackChecks:  maxChecks,
			PromptWaitMinutes:    promptWaitMin,
			WaitCountdownMinutes: waitCountdownMin,
			DeadlineAt:           &deadline,
			CreatedAt:            now,
			UpdatedAt:            now,
		}

		return tx.Create(&session).Error
	})

	if err != nil {
		return nil, err
	}
	return &session, nil
}

func (s *Store) GetFeedbackSession(ctx context.Context, sessionID string) (*model.FeedbackSession, error) {
	var session model.FeedbackSession
	if err := s.db.WithContext(ctx).Where("id = ?", sessionID).First(&session).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, NewNotFoundError(fmt.Sprintf("session %q not found", sessionID))
		}
		return nil, err
	}
	return &session, nil
}

// GetLatestWorkflowFeedbackSession 自动查找某工作流下最近的会话（优先 pending 活跃轮次，其次最新轮次）
func (s *Store) GetLatestWorkflowFeedbackSession(ctx context.Context, workflowID string) (*model.FeedbackSession, error) {
	var session model.FeedbackSession
	// 1. 优先匹配处于 pending 状态的活跃会话
	if err := s.db.WithContext(ctx).Where("workflow_id = ? AND status = 'pending'", workflowID).Order("created_at DESC").First(&session).Error; err == nil {
		return &session, nil
	}
	// 2. 若无 pending，回退查找该工作流最新创建的会话
	if err := s.db.WithContext(ctx).Where("workflow_id = ?", workflowID).Order("created_at DESC").First(&session).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, NewNotFoundError(fmt.Sprintf("no session found for workflow_id %q", workflowID))
		}
		return nil, err
	}
	return &session, nil
}

func (s *Store) GetCurrentFeedbackSession(ctx context.Context, projectDir string) (*model.FeedbackSession, error) {
	var session model.FeedbackSession
	query := s.db.WithContext(ctx).Where("status = 'pending'")
	if projectDir != "" && projectDir != "." {
		query = query.Where("project_directory = ?", projectDir)
	}
	if err := query.Order("created_at DESC").First(&session).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, nil
		}
		return nil, err
	}
	return &session, nil
}

func (s *Store) SubmitFeedback(ctx context.Context, input SubmitFeedbackInput) (*model.FeedbackSession, error) {
	var session model.FeedbackSession

	err := s.WithTx(ctx, func(tx *gorm.DB) error {
		if err := tx.Where("id = ?", input.SessionID).First(&session).Error; err != nil {
			if err == gorm.ErrRecordNotFound {
				return NewNotFoundError(fmt.Sprintf("session %q not found", input.SessionID))
			}
			return err
		}

		// 1. 若会话仍处于 pending 状态，执行初次提交
		if session.Status == "pending" {
			session.Status = "completed"
			session.ConsumedByAI = false
			session.ResponseText = input.ResponseText
			if len(input.UserMessages) > 0 {
				session.UserMessages = model.StringArray(input.UserMessages)
			}
			if len(input.Images) > 0 {
				session.Images = model.SessionImages(input.Images)
			}
			session.UpdatedAt = time.Now()
			return tx.Save(&session).Error
		}

		// 2. 若会话已 completed 且 AI 尚未提取消费 (ConsumedByAI == false)，自动合并追加！
		if session.Status == "completed" && !session.ConsumedByAI {
			if strings.TrimSpace(input.ResponseText) != "" {
				if session.ResponseText == "" {
					session.ResponseText = input.ResponseText
				} else {
					session.ResponseText = session.ResponseText + "\n" + input.ResponseText
				}
			}
			if len(input.UserMessages) > 0 {
				session.UserMessages = append(session.UserMessages, input.UserMessages...)
			}
			if len(input.Images) > 0 {
				session.Images = append(session.Images, input.Images...)
			}
			session.UpdatedAt = time.Now()
			return tx.Save(&session).Error
		}

		return NewConflictError(fmt.Sprintf("session %q is already %s and consumed", input.SessionID, session.Status), 0)
	})

	if err != nil {
		return nil, err
	}
	return &session, nil
}

type RevokedFeedbackResult struct {
	SessionID    string              `json:"session_id,omitempty"`
	WorkflowID   string              `json:"workflow_id,omitempty"`
	QueuedID     uint                `json:"queued_id,omitempty"`
	ResponseText string              `json:"response_text"`
	UserMessages []string            `json:"user_messages"`
	Images       []model.SessionImage `json:"images"`
}

func (s *Store) RevokeSessionFeedback(ctx context.Context, sessionID string) (*RevokedFeedbackResult, *model.FeedbackSession, error) {
	var session model.FeedbackSession
	var res RevokedFeedbackResult

	err := s.WithTx(ctx, func(tx *gorm.DB) error {
		if err := tx.Where("id = ?", sessionID).First(&session).Error; err != nil {
			if err == gorm.ErrRecordNotFound {
				return NewNotFoundError(fmt.Sprintf("session %q not found", sessionID))
			}
			return err
		}

		if session.Status != "completed" || session.ConsumedByAI {
			return NewConflictError(fmt.Sprintf("session %q cannot be revoked (status: %s, consumed: %v)", sessionID, session.Status, session.ConsumedByAI), 0)
		}

		res = RevokedFeedbackResult{
			SessionID:    session.ID,
			WorkflowID:   session.WorkflowID,
			ResponseText: session.ResponseText,
			UserMessages: []string(session.UserMessages),
			Images:       []model.SessionImage(session.Images),
		}

		session.Status = "pending"
		session.ConsumedByAI = false
		session.ResponseText = ""
		session.UserMessages = model.StringArray{}
		session.Images = model.SessionImages{}
		session.UpdatedAt = time.Now()

		return tx.Save(&session).Error
	})

	if err != nil {
		return nil, nil, err
	}
	return &res, &session, nil
}

func (s *Store) MarkSessionConsumedByAI(ctx context.Context, sessionID string) error {
	return s.WithTx(ctx, func(tx *gorm.DB) error {
		return tx.Model(&model.FeedbackSession{}).Where("id = ?", sessionID).Update("consumed_by_ai", true).Error
	})
}

type QueueFeedbackInput struct {
	WorkflowID       string              `json:"workflow_id"`
	HostName         string              `json:"host_name,omitempty"`
	ProjectDirectory string              `json:"project_directory,omitempty"`
	ResponseText     string              `json:"response_text"`
	UserMessages     []string            `json:"user_messages,omitempty"`
	Images           []model.SessionImage `json:"images,omitempty"`
}

func (s *Store) QueueWorkflowFeedback(ctx context.Context, input QueueFeedbackInput) (*model.QueuedFeedback, error) {
	if input.WorkflowID == "" {
		return nil, NewInvalidInputError("workflow_id is required")
	}

	var q model.QueuedFeedback
	now := time.Now()

	err := s.WithTx(ctx, func(tx *gorm.DB) error {
		var existing model.QueuedFeedback
		if err := tx.Where("workflow_id = ? AND status = 'pending'", input.WorkflowID).First(&existing).Error; err == nil {
			if strings.TrimSpace(input.ResponseText) != "" {
				if existing.ResponseText == "" {
					existing.ResponseText = input.ResponseText
				} else {
					existing.ResponseText = existing.ResponseText + "\n" + input.ResponseText
				}
			}
			if len(input.UserMessages) > 0 {
				existing.UserMessages = append(existing.UserMessages, input.UserMessages...)
			}
			if len(input.Images) > 0 {
				existing.Images = append(existing.Images, input.Images...)
			}
			if input.HostName != "" {
				existing.HostName = input.HostName
			}
			if input.ProjectDirectory != "" {
				existing.ProjectDirectory = input.ProjectDirectory
			}
			existing.UpdatedAt = now
			if err := tx.Save(&existing).Error; err != nil {
				return err
			}
			q = existing
			return nil
		}

		q = model.QueuedFeedback{
			WorkflowID:       input.WorkflowID,
			HostName:         input.HostName,
			ProjectDirectory: input.ProjectDirectory,
			ResponseText:     input.ResponseText,
			UserMessages:     model.StringArray(input.UserMessages),
			Images:           model.SessionImages(input.Images),
			Status:           "pending",
			CreatedAt:        now,
			UpdatedAt:        now,
		}
		return tx.Create(&q).Error
	})

	if err != nil {
		return nil, err
	}
	return &q, nil
}

func (s *Store) RevokeQueuedFeedback(ctx context.Context, queuedID uint) (*RevokedFeedbackResult, error) {
	var q model.QueuedFeedback
	var res RevokedFeedbackResult

	err := s.WithTx(ctx, func(tx *gorm.DB) error {
		if err := tx.Where("id = ?", queuedID).First(&q).Error; err != nil {
			if err == gorm.ErrRecordNotFound {
				return NewNotFoundError(fmt.Sprintf("queued feedback %d not found", queuedID))
			}
			return err
		}

		if q.Status != "pending" {
			return NewConflictError(fmt.Sprintf("queued feedback %d cannot be revoked (status: %s)", queuedID, q.Status), 0)
		}

		res = RevokedFeedbackResult{
			WorkflowID:   q.WorkflowID,
			QueuedID:     q.ID,
			ResponseText: q.ResponseText,
			UserMessages: []string(q.UserMessages),
			Images:       []model.SessionImage(q.Images),
		}

		q.Status = "revoked"
		q.UpdatedAt = time.Now()
		return tx.Save(&q).Error
	})

	if err != nil {
		return nil, err
	}
	return &res, nil
}

func (s *Store) ListPendingQueuedFeedbacks(ctx context.Context, workflowID string) ([]model.QueuedFeedback, error) {
	var list []model.QueuedFeedback
	query := s.db.WithContext(ctx).Where("status = 'pending'")
	if workflowID != "" {
		query = query.Where("workflow_id = ?", workflowID)
	}
	if err := query.Order("created_at ASC").Find(&list).Error; err != nil {
		return nil, err
	}
	return list, nil
}

func (s *Store) CancelFeedbackSession(ctx context.Context, idOrWorkflow string) (*model.FeedbackSession, error) {
	var session model.FeedbackSession

	err := s.WithTx(ctx, func(tx *gorm.DB) error {
		now := time.Now()
		if err := tx.Where("id = ?", idOrWorkflow).First(&session).Error; err == nil {
			if session.Status == "pending" {
				session.Status = "cancelled"
				session.UpdatedAt = now
				if err := tx.Save(&session).Error; err != nil {
					return err
				}
			}
			return nil
		}

		if err := tx.Where("workflow_id = ? AND status = 'pending'", idOrWorkflow).First(&session).Error; err == nil {
			session.Status = "cancelled"
			session.UpdatedAt = now
			return tx.Save(&session).Error
		}

		return NewNotFoundError(fmt.Sprintf("session or workflow %q not found", idOrWorkflow))
	})

	if err != nil {
		return nil, err
	}
	return &session, nil
}

func (s *Store) ListFeedbackSessions(ctx context.Context, projectDir, status string, limit int) ([]model.FeedbackSession, error) {
	if limit <= 0 || limit > 1000 {
		limit = 200
	}

	var sessions []model.FeedbackSession
	query := s.db.WithContext(ctx).Model(&model.FeedbackSession{})
	if projectDir != "" && projectDir != "." {
		query = query.Where("project_directory = ?", projectDir)
	}
	if status != "" {
		query = query.Where("status = ?", status)
	}

	if err := query.Order("created_at DESC").Limit(limit).Find(&sessions).Error; err != nil {
		return nil, err
	}

	// 注入工作流自定义锁定的固定标题，确保展示名称始终一致
	var customTitleSettings []model.SystemSetting
	if err := s.db.WithContext(ctx).Where("key LIKE 'wf_custom_title:%'").Find(&customTitleSettings).Error; err == nil && len(customTitleSettings) > 0 {
		titleMap := make(map[string]string)
		for _, set := range customTitleSettings {
			wId := strings.TrimPrefix(set.Key, "wf_custom_title:")
			titleMap[wId] = set.Value
		}
		for i := range sessions {
			wId := sessions[i].WorkflowID
			if wId == "" {
				wId = sessions[i].ID
			}
			if cTitle, ok := titleMap[wId]; ok && cTitle != "" {
				sessions[i].Title = cTitle
			}
		}
	}

	return sessions, nil
}

func (s *Store) KeepaliveFeedbackSession(ctx context.Context, sessionID string, extendSec int) (*model.FeedbackSession, error) {
	var session model.FeedbackSession

	err := s.WithTx(ctx, func(tx *gorm.DB) error {
		if err := tx.Where("id = ?", sessionID).First(&session).Error; err != nil {
			if err == gorm.ErrRecordNotFound {
				return NewNotFoundError(fmt.Sprintf("session %q not found", sessionID))
			}
			return err
		}

		if session.Status != "pending" {
			return NewConflictError(fmt.Sprintf("session %q is already %s", sessionID, session.Status), 0)
		}

		if extendSec <= 0 {
			extendSec = 300
		}

		now := time.Now()
		newDeadline := now.Add(time.Duration(extendSec) * time.Second)
		session.DeadlineAt = &newDeadline
		session.TimeoutSeconds += extendSec
		session.NoFeedbackChecks++
		session.UpdatedAt = now

		return tx.Save(&session).Error
	})

	if err != nil {
		return nil, err
	}
	return &session, nil
}

func (s *Store) ArchiveFeedbackSession(ctx context.Context, idOrWorkflow string) (*model.FeedbackSession, error) {
	var session model.FeedbackSession
	err := s.WithTx(ctx, func(tx *gorm.DB) error {
		now := time.Now()
		// 1. 优先按 session_id 查找
		if err := tx.Where("id = ?", idOrWorkflow).First(&session).Error; err == nil {
			if session.WorkflowID != "" {
				// 级联归档该工作流下所有轮次
				if err := tx.Model(&model.FeedbackSession{}).
					Where("workflow_id = ? AND status != 'archived'", session.WorkflowID).
					Updates(map[string]interface{}{
						"status":     "archived",
						"updated_at": now,
					}).Error; err != nil {
					return err
				}
			} else {
				session.Status = "archived"
				session.UpdatedAt = now
				if err := tx.Save(&session).Error; err != nil {
					return err
				}
			}
			return tx.Where("id = ?", session.ID).First(&session).Error
		}

		// 2. 按 workflow_id 查找并批量归档
		var count int64
		if err := tx.Model(&model.FeedbackSession{}).Where("workflow_id = ?", idOrWorkflow).Count(&count).Error; err != nil {
			return err
		}
		if count == 0 {
			return NewNotFoundError(fmt.Sprintf("session or workflow %q not found", idOrWorkflow))
		}

		if err := tx.Model(&model.FeedbackSession{}).
			Where("workflow_id = ? AND status != 'archived'", idOrWorkflow).
			Updates(map[string]interface{}{
				"status":     "archived",
				"updated_at": now,
			}).Error; err != nil {
			return err
		}

		return tx.Where("workflow_id = ?", idOrWorkflow).Order("created_at DESC").First(&session).Error
	})
	if err != nil {
		return nil, err
	}
	return &session, nil
}

func (s *Store) RenameFeedbackSession(ctx context.Context, idOrWorkflow, newTitle string) (*model.FeedbackSession, error) {
	var session model.FeedbackSession
	err := s.WithTx(ctx, func(tx *gorm.DB) error {
		now := time.Now()
		var targetWorkflowID string

		if err := tx.Where("id = ?", idOrWorkflow).First(&session).Error; err == nil {
			targetWorkflowID = session.WorkflowID
			if targetWorkflowID == "" {
				targetWorkflowID = session.ID
			}
			if session.WorkflowID != "" {
				if err := tx.Model(&model.FeedbackSession{}).
					Where("workflow_id = ?", session.WorkflowID).
					Updates(map[string]interface{}{
						"title":      newTitle,
						"updated_at": now,
					}).Error; err != nil {
					return err
				}
			} else {
				session.Title = newTitle
				session.UpdatedAt = now
				if err := tx.Save(&session).Error; err != nil {
					return err
				}
			}
		} else {
			var count int64
			if err := tx.Model(&model.FeedbackSession{}).Where("workflow_id = ?", idOrWorkflow).Count(&count).Error; err != nil {
				return err
			}
			if count == 0 {
				return NewNotFoundError(fmt.Sprintf("session or workflow %q not found", idOrWorkflow))
			}
			targetWorkflowID = idOrWorkflow
			if err := tx.Model(&model.FeedbackSession{}).
				Where("workflow_id = ?", idOrWorkflow).
				Updates(map[string]interface{}{
					"title":      newTitle,
					"updated_at": now,
				}).Error; err != nil {
				return err
			}
			if err := tx.Where("workflow_id = ?", idOrWorkflow).Order("created_at DESC").First(&session).Error; err != nil {
				return err
			}
		}

		// 持久化保存该 Workflow 的自定义固定标题，确保未来新轮次不会被 AI 的 title 覆盖
		if targetWorkflowID != "" {
			setting := model.SystemSetting{
				Key:       "wf_custom_title:" + targetWorkflowID,
				Value:     newTitle,
				UpdatedAt: now,
			}
			if err := tx.Save(&setting).Error; err != nil {
				return err
			}
		}

		session.Title = newTitle
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &session, nil
}

func (s *Store) UpdateSessionPromptWait(ctx context.Context, idOrWorkflow string, waitMinutes int) (*model.FeedbackSession, error) {
	var session model.FeedbackSession
	err := s.WithTx(ctx, func(tx *gorm.DB) error {
		now := time.Now()
		if err := tx.Where("id = ?", idOrWorkflow).First(&session).Error; err == nil {
			if session.WorkflowID != "" {
				_ = tx.Model(&model.FeedbackSession{}).
					Where("workflow_id = ?", session.WorkflowID).
					Updates(map[string]interface{}{
						"prompt_wait_minutes": waitMinutes,
						"updated_at":          now,
					})
			}
			session.PromptWaitMinutes = waitMinutes
			session.UpdatedAt = now
			return tx.Save(&session).Error
		}

		if err := tx.Where("workflow_id = ?", idOrWorkflow).Order("created_at DESC").First(&session).Error; err == nil {
			if err := tx.Model(&model.FeedbackSession{}).
				Where("workflow_id = ?", idOrWorkflow).
				Updates(map[string]interface{}{
					"prompt_wait_minutes": waitMinutes,
					"updated_at":          now,
				}).Error; err != nil {
				return err
			}
			session.PromptWaitMinutes = waitMinutes
			session.UpdatedAt = now
			return nil
		}

		return NewNotFoundError(fmt.Sprintf("session or workflow %q not found", idOrWorkflow))
	})
	if err != nil {
		return nil, err
	}
	return &session, nil
}

func (s *Store) UpdateSessionMaxChecks(ctx context.Context, idOrWorkflow string, maxChecks int) (*model.FeedbackSession, error) {
	var session model.FeedbackSession
	err := s.WithTx(ctx, func(tx *gorm.DB) error {
		now := time.Now()
		if err := tx.Where("id = ?", idOrWorkflow).First(&session).Error; err == nil {
			if session.WorkflowID != "" {
				_ = tx.Model(&model.FeedbackSession{}).
					Where("workflow_id = ?", session.WorkflowID).
					Updates(map[string]interface{}{
						"max_no_feedback_checks": maxChecks,
						"updated_at":             now,
					})
			}
			session.MaxNoFeedbackChecks = maxChecks
			session.UpdatedAt = now
			return tx.Save(&session).Error
		}

		if err := tx.Where("workflow_id = ?", idOrWorkflow).Order("created_at DESC").First(&session).Error; err == nil {
			if err := tx.Model(&model.FeedbackSession{}).
				Where("workflow_id = ?", idOrWorkflow).
				Updates(map[string]interface{}{
					"max_no_feedback_checks": maxChecks,
					"updated_at":             now,
				}).Error; err != nil {
				return err
			}
			session.MaxNoFeedbackChecks = maxChecks
			session.UpdatedAt = now
			return nil
		}

		return NewNotFoundError(fmt.Sprintf("session or workflow %q not found", idOrWorkflow))
	})
	if err != nil {
		return nil, err
	}
	return &session, nil
}

func (s *Store) UpdateSessionWaitCountdown(ctx context.Context, idOrWorkflow string, countdownMinutes int) (*model.FeedbackSession, error) {
	var session model.FeedbackSession
	err := s.WithTx(ctx, func(tx *gorm.DB) error {
		now := time.Now()
		if err := tx.Where("id = ?", idOrWorkflow).First(&session).Error; err == nil {
			if session.WorkflowID != "" {
				_ = tx.Model(&model.FeedbackSession{}).
					Where("workflow_id = ?", session.WorkflowID).
					Updates(map[string]interface{}{
						"wait_countdown_minutes": countdownMinutes,
						"updated_at":             now,
					})
			}
			session.WaitCountdownMinutes = countdownMinutes
			session.UpdatedAt = now
			return tx.Save(&session).Error
		}

		if err := tx.Where("workflow_id = ?", idOrWorkflow).Order("created_at DESC").First(&session).Error; err == nil {
			if err := tx.Model(&model.FeedbackSession{}).
				Where("workflow_id = ?", idOrWorkflow).
				Updates(map[string]interface{}{
					"wait_countdown_minutes": countdownMinutes,
					"updated_at":             now,
				}).Error; err != nil {
				return err
			}
			session.WaitCountdownMinutes = countdownMinutes
			session.UpdatedAt = now
			return nil
		}

		return NewNotFoundError(fmt.Sprintf("session or workflow %q not found", idOrWorkflow))
	})
	if err != nil {
		return nil, err
	}
	return &session, nil
}

func (s *Store) UpdateSessionUserPresence(ctx context.Context, idOrWorkflow string, presence string) (*model.FeedbackSession, error) {
	var session model.FeedbackSession
	if presence == "" {
		presence = "online"
	}
	err := s.WithTx(ctx, func(tx *gorm.DB) error {
		now := time.Now()
		if err := tx.Where("id = ?", idOrWorkflow).First(&session).Error; err == nil {
			if session.WorkflowID != "" {
				_ = tx.Model(&model.FeedbackSession{}).
					Where("workflow_id = ?", session.WorkflowID).
					Updates(map[string]interface{}{
						"user_presence": presence,
						"updated_at":    now,
					})
			}
			session.UserPresence = presence
			session.UpdatedAt = now
			return tx.Save(&session).Error
		}

		if err := tx.Where("workflow_id = ?", idOrWorkflow).Order("created_at DESC").First(&session).Error; err == nil {
			if err := tx.Model(&model.FeedbackSession{}).
				Where("workflow_id = ?", idOrWorkflow).
				Updates(map[string]interface{}{
					"user_presence": presence,
					"updated_at":    now,
				}).Error; err != nil {
				return err
			}
			session.UserPresence = presence
			session.UpdatedAt = now
			return nil
		}

		return NewNotFoundError(fmt.Sprintf("session or workflow %q not found", idOrWorkflow))
	})
	if err != nil {
		return nil, err
	}
	return &session, nil
}

func (s *Store) UnarchiveFeedbackSession(ctx context.Context, idOrWorkflow string) (*model.FeedbackSession, error) {
	var session model.FeedbackSession
	err := s.WithTx(ctx, func(tx *gorm.DB) error {
		now := time.Now()
		// 1. 优先按 session_id 查找
		if err := tx.Where("id = ?", idOrWorkflow).First(&session).Error; err == nil {
			if session.WorkflowID != "" {
				// 级联取消归档该工作流下所有已归档轮次
				if err := tx.Model(&model.FeedbackSession{}).
					Where("workflow_id = ? AND status = 'archived'", session.WorkflowID).
					Updates(map[string]interface{}{
						"status":     "completed",
						"updated_at": now,
					}).Error; err != nil {
					return err
				}
			} else {
				session.Status = "completed"
				session.UpdatedAt = now
				if err := tx.Save(&session).Error; err != nil {
					return err
				}
			}
			return tx.Where("id = ?", session.ID).First(&session).Error
		}

		// 2. 按 workflow_id 查找并批量取消归档
		var count int64
		if err := tx.Model(&model.FeedbackSession{}).Where("workflow_id = ?", idOrWorkflow).Count(&count).Error; err != nil {
			return err
		}
		if count == 0 {
			return NewNotFoundError(fmt.Sprintf("session or workflow %q not found", idOrWorkflow))
		}

		if err := tx.Model(&model.FeedbackSession{}).
			Where("workflow_id = ? AND status = 'archived'", idOrWorkflow).
			Updates(map[string]interface{}{
				"status":     "completed",
				"updated_at": now,
			}).Error; err != nil {
			return err
		}

		return tx.Where("workflow_id = ?", idOrWorkflow).Order("created_at DESC").First(&session).Error
	})
	if err != nil {
		return nil, err
	}
	return &session, nil
}
