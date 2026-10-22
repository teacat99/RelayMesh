package store

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/teacat99/RelayMesh/v2/internal/model"
	"gorm.io/gorm"
)

type CreateSessionInput struct {
	SessionID          string `json:"session_id,omitempty"`
	WorkflowID         string `json:"workflow_id,omitempty"`
	CredentialID       uint   `json:"credential_id,omitempty"`
	EnvHostName        string `json:"env_host_name,omitempty"`
	CredentialHostName string `json:"credential_host_name,omitempty"`
	ProjectDirectory   string `json:"project_directory"`
	Title              string `json:"title,omitempty"`
	Summary            string `json:"summary"`
	TimeoutSeconds     int    `json:"timeout_seconds,omitempty"`
	// 交付锚点（可选）：调用方已校验并规整；为空表示本轮没有锚点。
	GitCommit  string `json:"git_commit,omitempty"`
	GitSubject string `json:"git_subject,omitempty"`
}

type SubmitFeedbackInput struct {
	SessionID    string               `json:"session_id"`
	ResponseText string               `json:"response_text"`
	UserMessages []string             `json:"user_messages,omitempty"`
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

	// 严守设计规范（D-139 / D-153 / D-164）：任何 Session 必须且只能隶属于唯一的 Workflow。
	// 若调用方未显式提供 workflow_id，实施同项目目录/同凭据自愈继承机制，彻底避免因上下文压缩遗漏参数而乱建临时分支：
	workflowID := strings.TrimSpace(input.WorkflowID)
	projDir := strings.TrimSpace(input.ProjectDirectory)
	if projDir == "." {
		projDir = ""
	}

	// D-164: 孤儿新 Workflow ID 智能模糊吸附与纠偏
	// 如果调用方传入了一个在库中不存在的 workflow_id，检查是否是当前项目既有活跃工作流的常见变体（例如缺失后缀、前缀、连字符变体等）
	if workflowID != "" && projDir != "" {
		var count int64
		_ = s.db.WithContext(ctx).Model(&model.FeedbackSession{}).Where("workflow_id = ?", workflowID).Count(&count).Error
		if count == 0 {
			var activeWorkflows []string
			_ = s.db.WithContext(ctx).Model(&model.FeedbackSession{}).
				Where("project_directory = ? AND workflow_id != '' AND status != 'archived'", projDir).
				Distinct("workflow_id").
				Pluck("workflow_id", &activeWorkflows).Error

			normInput := strings.ToLower(strings.ReplaceAll(workflowID, "_", "-"))
			cleanInput := strings.TrimPrefix(normInput, "wf-")

			for _, existingWf := range activeWorkflows {
				normExisting := strings.ToLower(strings.ReplaceAll(existingWf, "_", "-"))
				cleanExisting := strings.TrimPrefix(normExisting, "wf-")

				// 1. 完全去前缀后一致 (如 wf-relaymesh 与 relaymesh)
				// 2. 包含或前缀匹配 (如 relaymesh 匹配 relaymesh-continuation，且长度足够避免单字母误伤)
				// 3. 去除所有标点连字符后完全一致
				if cleanInput == cleanExisting ||
					(len(cleanInput) >= 5 && (strings.HasPrefix(cleanExisting, cleanInput) || strings.Contains(cleanExisting, cleanInput))) ||
					strings.ReplaceAll(cleanInput, "-", "") == strings.ReplaceAll(cleanExisting, "-", "") {
					workflowID = existingWf
					break
				}
			}
		}
	}

	if workflowID == "" {
		// 1. 优先基于项目目录寻找该项目最近活跃或未归档的既有工作流
		if projDir != "" {
			var recentSess model.FeedbackSession
			err := s.db.WithContext(ctx).
				Where("project_directory = ? AND workflow_id != '' AND status != 'archived'", projDir).
				Order("updated_at desc, id desc").
				First(&recentSess).Error
			if err != nil && projDir != "." {
				if absDir, aErr := filepath.Abs(projDir); aErr == nil && absDir != projDir {
					_ = s.db.WithContext(ctx).
						Where("project_directory = ? AND workflow_id != '' AND status != 'archived'", absDir).
						Order("updated_at desc, id desc").
						First(&recentSess).Error
				}
			}
			if recentSess.WorkflowID != "" {
				workflowID = recentSess.WorkflowID
			}
		}

		// 2. 二级自愈：若同项目未匹配到但有凭据 Token 绑定，在同凭据下查找最近未归档工作流
		if workflowID == "" && input.CredentialID > 0 {
			var recentSess model.FeedbackSession
			if err := s.db.WithContext(ctx).
				Where("credential_id = ? AND workflow_id != '' AND status != 'archived'", input.CredentialID).
				Order("updated_at desc, id desc").
				First(&recentSess).Error; err == nil && recentSess.WorkflowID != "" {
				workflowID = recentSess.WorkflowID
			}
		}

		// 3. 兜底保护：若全无历史工作流（全新项目开天辟地第一轮），方才派生标准工作流标识（例：wf-20260908-e61b8709）
		if workflowID == "" {
			cleanSessID := strings.TrimPrefix(sessionID, "sess-")
			workflowID = fmt.Sprintf("wf-%s-%s", now.Format("20060102"), cleanSessID)
		}
	}
	input.WorkflowID = workflowID

	var session model.FeedbackSession

	err := s.WithTx(ctx, func(tx *gorm.DB) error {
		// D-161: 工作流项目所有权强绑定校验
		// 若该 workflow_id 已存在历史会话，比对绑定的项目路径，严禁跨项目交叉写入或覆写
		if input.WorkflowID != "" {
			var existingWfSess model.FeedbackSession
			if err := tx.Where("workflow_id = ?", input.WorkflowID).Order("id asc").First(&existingWfSess).Error; err == nil {
				origDir := filepath.Clean(strings.TrimSpace(existingWfSess.ProjectDirectory))
				currDir := filepath.Clean(strings.TrimSpace(input.ProjectDirectory))
				if origDir != "" && currDir != "" && origDir != "." && currDir != "." && origDir != currDir {
					return NewConflictError(fmt.Sprintf("Workflow ID %q 已被项目 %q 占用，当前项目 %q 无法交叉写入！请分配独立的 workflow_id 或留空由系统自动派生。", input.WorkflowID, origDir, currDir), 0)
				}
			}
		}

		// 如果该工作流已存在用户自定义固定的标题，优先锁定使用用户自定义标题
		effectiveTitle := input.Title
		if input.WorkflowID != "" {
			var customTitleSetting model.SystemSetting
			if err := tx.Where("key = ?", "wf_custom_title:"+input.WorkflowID).First(&customTitleSetting).Error; err == nil && customTitleSetting.Value != "" {
				effectiveTitle = customTitleSetting.Value
			}
		}

		// If workflow_id provided, check if there is an existing pending session for this workflow
		// D-159: 强制 ORDER BY created_at DESC 杜绝正序查出陈旧残留记录
		if input.WorkflowID != "" {
			var existing model.FeedbackSession
			if err := tx.Where("workflow_id = ? AND status = 'pending'", input.WorkflowID).Order("created_at desc, id desc").First(&existing).Error; err == nil {
				// D-161: 仅在项目目录一致时才允许覆写已有 pending
				origDir := filepath.Clean(strings.TrimSpace(existing.ProjectDirectory))
				currDir := filepath.Clean(strings.TrimSpace(input.ProjectDirectory))
				if origDir != "" && currDir != "" && origDir != "." && currDir != "." && origDir != currDir {
					return NewConflictError(fmt.Sprintf("无法覆写 pending 会话：Workflow ID %q 属于项目 %q，当前项目 %q 无权覆写", input.WorkflowID, origDir, currDir), 0)
				}

				existing.Summary = input.Summary
				if effectiveTitle != "" {
					existing.Title = effectiveTitle
				}
				if input.ProjectDirectory != "" {
					existing.ProjectDirectory = input.ProjectDirectory
				}
				existing.TimeoutSeconds = timeoutSec
				existing.DeadlineAt = &deadline
				existing.UpdatedAt = now
				// 交付锚点：新一次调用带了锚点就以它为准；没带则保留原来的（漏传只是不更新，不清空）。
				if input.GitCommit != "" {
					existing.GitCommit = input.GitCommit
					existing.GitSubject = input.GitSubject
				}
				if err := tx.Save(&existing).Error; err != nil {
					return err
				}
				session = existing

				// D-159: 自动将该工作流下所有历史完成未消费轮次打标为已读
				_ = tx.Model(&model.FeedbackSession{}).
					Where("workflow_id = ? AND status = 'completed' AND consumed_by_ai = false", input.WorkflowID).
					Updates(map[string]any{
						"consumed_by_ai": true,
						"consumed_at":    now,
					}).Error

				return nil
			}
		}

		// 只有在 workflow_id 为空或者指定了新的独立单次会话时才复用历史 pending；
		// 若指定了 workflow_id 且当前没有 pending 轮次，必须新建一条 session（作为该工作流的新一轮对话），绝不就地覆盖已 completed 的历史轮次！

		// 全局系统配置作为基线默认值
		globalSettings, _ := s.GetGlobalAppSettingsWithDB(ctx, tx)

		// host_name 优先级链（服务端自主决定，防止 AI 伪装）:
		// P1: 客户端专属/凭据绑定 > P2: Web UI 全局设置 > P3: 环境变量 > P4: Workflow 继承 > P5: 默认 "hosts"
		hostName := ""
		if input.CredentialHostName != "" {
			hostName = input.CredentialHostName
		}
		if hostName == "" && globalSettings != nil && globalSettings.HostName != "" {
			hostName = globalSettings.HostName
		}
		if hostName == "" && input.EnvHostName != "" {
			hostName = input.EnvHostName
		}
		promptWaitMin := 2
		waitCountdownMin := 2
		waitCountdownSec := -1
		maxChecks := 24
		if globalSettings != nil {
			if globalSettings.PromptWaitMinutes > 0 {
				promptWaitMin = globalSettings.PromptWaitMinutes
			}
			if globalSettings.DefaultWaitCountdownMinutes >= 0 {
				waitCountdownMin = globalSettings.DefaultWaitCountdownMinutes
			}
			if globalSettings.DefaultWaitCountdownSeconds >= 0 {
				waitCountdownSec = globalSettings.DefaultWaitCountdownSeconds
			}
			if globalSettings.MaxNoFeedbackChecks > 0 {
				maxChecks = globalSettings.MaxNoFeedbackChecks
			}
		}

		var credIDPtr *uint
		if input.CredentialID > 0 {
			credIDPtr = &input.CredentialID
		}

		// 若指定了 WorkflowID，检查是否存在历史轮次以继承用户此前调整的配置
		if input.WorkflowID != "" {
			var lastSess model.FeedbackSession
			if err := tx.Where("workflow_id = ?", input.WorkflowID).Order("created_at DESC").First(&lastSess).Error; err == nil {
				if credIDPtr == nil && lastSess.CredentialID != nil {
					credIDPtr = lastSess.CredentialID
				}
				// P4: Workflow 继承（仅当 P1-P3 均为空时）
				if hostName == "" && lastSess.HostName != "" {
					hostName = lastSess.HostName
				}
				if lastSess.PromptWaitMinutes > 0 {
					promptWaitMin = lastSess.PromptWaitMinutes
				}
				if lastSess.WaitCountdownMinutes >= 0 {
					waitCountdownMin = lastSess.WaitCountdownMinutes
				}
				if lastSess.WaitCountdownSeconds >= 0 {
					waitCountdownSec = lastSess.WaitCountdownSeconds
				} else {
					// 上一轮没有设过秒级值（旧数据 / 刚在分钟档里调过）：不要让全局的秒级默认盖过它的分钟值
					waitCountdownSec = -1
				}
				if lastSess.MaxNoFeedbackChecks >= 0 {
					maxChecks = lastSess.MaxNoFeedbackChecks
				}
			}
		}
		// P5: 兜底默认值
		if hostName == "" {
			hostName = "hosts"
		}

		// 取走收件箱里用户提前追加的内容（秒回直取机制）：排队中的全部按时间顺序合并成一次秒回；
		// 已被智能体读取但没确认的，随这次交互一并确认（见 inbox.go）。
		inbox, err := consumeInboxTx(tx, input.WorkflowID, sessionID, now)
		if err != nil {
			return err
		}
		hasQueued := inbox.Count > 0

		sessionStatus := "pending"
		consumedByAI := false
		respText := ""
		userMsgs := model.StringArray{}
		imgs := model.SessionImages{}
		var respondedAt, consumedAt *time.Time

		if hasQueued {
			taken := now // 秒回：智能体此刻创建本轮就同时取走了这些追加
			consumedAt = &taken
			sessionStatus = "completed"
			consumedByAI = true
			respText = inbox.Text
			// 「秒回」里的内容是用户此前就敲好的：回复时间取其中最早一条的创建时间，而不是这一轮被创建的此刻。
			first := inbox.First
			if first.IsZero() || first.After(now) {
				first = now
			}
			respondedAt = &first
			if inbox.Messages != nil {
				userMsgs = inbox.Messages
			}
			if inbox.Images != nil {
				imgs = inbox.Images
			}
		}

		session = model.FeedbackSession{
			ID:                   sessionID,
			WorkflowID:           input.WorkflowID,
			CredentialID:         credIDPtr,
			HostName:             hostName,
			ProjectDirectory:     input.ProjectDirectory,
			Title:                effectiveTitle,
			Summary:              input.Summary,
			Status:               sessionStatus,
			ResponseText:         respText,
			UserMessages:         userMsgs,
			Images:               imgs,
			ConsumedByAI:         consumedByAI,
			ConsumedAt:           consumedAt,
			RespondedAt:          respondedAt,
			TimeoutSeconds:       timeoutSec,
			NoFeedbackChecks:     0,
			MaxNoFeedbackChecks:  maxChecks,
			PromptWaitMinutes:    promptWaitMin,
			WaitCountdownMinutes: waitCountdownMin,
			WaitCountdownSeconds: waitCountdownSec,
			DeadlineAt:           &deadline,
			GitCommit:            input.GitCommit,
			GitSubject:           input.GitSubject,
			CreatedAt:            now,
			UpdatedAt:            now,
		}

		if err := tx.Create(&session).Error; err != nil {
			return err
		}
		// GORM 创建时会把零值换成列默认值（空回执上限 0=不限 → 24，秒级倒计时 0=秒回 → -1，分钟 0 → 2），
		// 但这几个字段里 0 是用户明确选的有效值，要原样写回，否则「不限」「秒回」下一轮就丢了。
		zeroFix := map[string]any{}
		if maxChecks == 0 {
			zeroFix["max_no_feedback_checks"] = 0
			session.MaxNoFeedbackChecks = 0
		}
		if waitCountdownSec == 0 {
			zeroFix["wait_countdown_seconds"] = 0
			session.WaitCountdownSeconds = 0
		}
		if waitCountdownMin == 0 {
			zeroFix["wait_countdown_minutes"] = 0
			session.WaitCountdownMinutes = 0
		}
		if len(zeroFix) > 0 {
			if err := tx.Model(&model.FeedbackSession{}).Where("id = ?", session.ID).Updates(zeroFix).Error; err != nil {
				return err
			}
		}

		// D-159: AI 发起新轮次交互，自动将本工作流所有历史已完成但未消费的轮次批量打标为已消费
		if input.WorkflowID != "" {
			_ = tx.Model(&model.FeedbackSession{}).
				Where("workflow_id = ? AND id != ? AND status = 'completed' AND consumed_by_ai = false", input.WorkflowID, session.ID).
				Updates(map[string]any{
					"consumed_by_ai": true,
					"consumed_at":    now,
				}).Error
		}

		return nil
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

// notNoticeSQL 排除异步模式下的 notice 进度气泡：库里旧会话的 kind 是 NULL、新的常规会话是空串，两者都算常规汇报。
const notNoticeSQL = "COALESCE(kind, '') <> 'notice'"

// GetLatestWorkflowFeedbackSession 自动查找某工作流下最近的汇报会话（优先 pending 活跃轮次，其次最新轮次）。
// notice 进度气泡不是「一轮对话」——它没有回复框、发布即完成——所以不在这里的候选之内：
// 守护、continue_feedback_session、Agent 存活判定都靠它找「智能体到底在等谁」，被 notice 顶替就会误判。
func (s *Store) GetLatestWorkflowFeedbackSession(ctx context.Context, workflowID string) (*model.FeedbackSession, error) {
	var session model.FeedbackSession
	// 1. 优先匹配处于 pending 状态的活跃会话
	if err := s.db.WithContext(ctx).Where("workflow_id = ? AND status = 'pending' AND "+notNoticeSQL, workflowID).Order("created_at DESC").First(&session).Error; err == nil {
		return &session, nil
	}
	// 2. 若无 pending，回退查找该工作流最新创建的会话
	if err := s.db.WithContext(ctx).Where("workflow_id = ? AND "+notNoticeSQL, workflowID).Order("created_at DESC").First(&session).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, NewNotFoundError(fmt.Sprintf("no session found for workflow_id %q", workflowID))
		}
		return nil, err
	}
	return &session, nil
}

func (s *Store) GetSessionsByWorkflow(ctx context.Context, workflowID string, limit int) ([]model.FeedbackSession, error) {
	if limit <= 0 || limit > 50 {
		limit = 10
	}
	var sessions []model.FeedbackSession
	err := s.db.WithContext(ctx).
		Where("workflow_id = ?", workflowID).
		Order("created_at DESC").
		Limit(limit).
		Find(&sessions).Error
	return sessions, err
}

// GetFeedbackRoundsByWorkflow 与 GetSessionsByWorkflow 相同，但不含 notice 进度气泡：
// 检查点的「之后又多了几轮」「之后的用户反馈」按真正的汇报—回复轮次数，进度通知再多也不该让检查点显得过期。
func (s *Store) GetFeedbackRoundsByWorkflow(ctx context.Context, workflowID string, limit int) ([]model.FeedbackSession, error) {
	if limit <= 0 || limit > 50 {
		limit = 10
	}
	var sessions []model.FeedbackSession
	err := s.db.WithContext(ctx).
		Where("workflow_id = ? AND "+notNoticeSQL, workflowID).
		Order("created_at DESC").
		Limit(limit).
		Find(&sessions).Error
	return sessions, err
}

// CreateNoticeSession 发布一条不阻塞的进度 / 回答气泡（异步对话模式下 interactive_feedback 的 wait:false）。
//
// 它与 CreateFeedbackSession 的区别，也是它不复用后者的原因：
//   - 不取走收件箱里的追加（那是给「等待中的汇报」的秒回，notice 没有在等谁，取走就等于吞掉用户的话）；
//   - 不覆写已有的 pending 汇报，也不把历史上已完成但未消费的轮次批量标成已消费；
//   - 一建就是 completed + 已被智能体消费 + 没有回复时间：界面上只有 AI 气泡，没有回复框。
//
// workflow_id 必须给定且已被调用方核实（异步模式是按工作流开的，不存在「自动派生新工作流」的情形）。
func (s *Store) CreateNoticeSession(ctx context.Context, input CreateSessionInput) (*model.FeedbackSession, error) {
	if strings.TrimSpace(input.Summary) == "" {
		return nil, NewInvalidInputError("summary is required and cannot be empty")
	}
	workflowID := strings.TrimSpace(input.WorkflowID)
	if workflowID == "" {
		return nil, NewInvalidInputError("workflow_id is required for a notice")
	}
	projDir := strings.TrimSpace(input.ProjectDirectory)
	if projDir == "." {
		projDir = ""
	}

	now := time.Now()
	sessionID := input.SessionID
	if sessionID == "" {
		sessionID = "sess-" + uuid.New().String()[:8]
	}

	var session model.FeedbackSession
	err := s.WithTx(ctx, func(tx *gorm.DB) error {
		// 与汇报一致的项目所有权校验（D-161）：同一个 workflow_id 不能被另一个项目交叉写入。
		var first model.FeedbackSession
		if err := tx.Where("workflow_id = ?", workflowID).Order("id asc").First(&first).Error; err == nil {
			origDir := filepath.Clean(strings.TrimSpace(first.ProjectDirectory))
			currDir := filepath.Clean(projDir)
			if origDir != "" && currDir != "" && origDir != "." && currDir != "." && origDir != currDir {
				return NewConflictError(fmt.Sprintf("Workflow ID %q 已被项目 %q 占用，当前项目 %q 无法交叉写入！", workflowID, origDir, currDir), 0)
			}
		}

		// 用户给工作流定过标题的，沿用用户的标题。
		title := input.Title
		var customTitle model.SystemSetting
		if err := tx.Where("key = ?", "wf_custom_title:"+workflowID).First(&customTitle).Error; err == nil && customTitle.Value != "" {
			title = customTitle.Value
		}

		// host_name 的优先级与汇报一致：凭据绑定 > 全局设置 > 环境变量 > 工作流继承 > 默认。
		globalSettings, _ := s.GetGlobalAppSettingsWithDB(ctx, tx)
		hostName := input.CredentialHostName
		if hostName == "" && globalSettings != nil {
			hostName = globalSettings.HostName
		}
		if hostName == "" {
			hostName = input.EnvHostName
		}
		var credIDPtr *uint
		if input.CredentialID > 0 {
			credIDPtr = &input.CredentialID
		}
		var last model.FeedbackSession
		if err := tx.Where("workflow_id = ?", workflowID).Order("created_at DESC").First(&last).Error; err == nil {
			if hostName == "" {
				hostName = last.HostName
			}
			if credIDPtr == nil {
				credIDPtr = last.CredentialID
			}
			if projDir == "" {
				projDir = last.ProjectDirectory
			}
		}
		if hostName == "" {
			hostName = "hosts"
		}

		session = model.FeedbackSession{
			ID:               sessionID,
			WorkflowID:       workflowID,
			Kind:             model.FeedbackKindNotice,
			CredentialID:     credIDPtr,
			HostName:         hostName,
			ProjectDirectory: projDir,
			Title:            title,
			Summary:          input.Summary,
			Status:           "completed",
			UserMessages:     model.StringArray{},
			Images:           model.SessionImages{},
			ConsumedByAI:     true,
			ConsumedAt:       &now,
			GitCommit:        input.GitCommit,
			GitSubject:       input.GitSubject,
			CreatedAt:        now,
			UpdatedAt:        now,
		}
		return tx.Create(&session).Error
	})
	if err != nil {
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
			now := time.Now()
			session.RespondedAt = &now
			session.UpdatedAt = now
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
			now := time.Now()
			if session.RespondedAt == nil {
				// 历史行（没有这一列）：合并追加时才补上，保留第一次回复的时间不动。
				session.RespondedAt = &now
			}
			session.UpdatedAt = now
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
	SessionID    string               `json:"session_id,omitempty"`
	WorkflowID   string               `json:"workflow_id,omitempty"`
	QueuedID     uint                 `json:"queued_id,omitempty"`
	ResponseText string               `json:"response_text"`
	UserMessages []string             `json:"user_messages"`
	Images       []model.SessionImage `json:"images"`
	// Delivered：撤回的是一条智能体已经读取过的追加（收件箱），会在它下次读取时被告知忽略。
	Delivered bool `json:"delivered,omitempty"`
	// AgentNotified：撤回时智能体已经知道这条追加存在（读取过全文，或被 hook / 提示行提醒过），它会被告知忽略这一条（#199）。
	// 比 Delivered 宽：只被提醒过、还没读的也算；界面据此提示「已撤回，会告知忽略」。
	AgentNotified bool `json:"agent_notified,omitempty"`
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

		// D-159: 仅允许撤回本工作流最新轮次，历史轮次已终态冻结，严禁撤回复活
		if session.WorkflowID != "" {
			var latestSess model.FeedbackSession
			if err := tx.Where("workflow_id = ?", session.WorkflowID).Order("created_at DESC, rowid DESC").First(&latestSess).Error; err == nil {
				if latestSess.ID != session.ID {
					return NewConflictError(fmt.Sprintf("session %q 不是当前工作流最新轮次，已终态冻结不可撤回", sessionID), 0)
				}
			}
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
		session.ConsumedAt = nil
		session.RespondedAt = nil
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

func (s *Store) MarkSessionConsumedByAI(ctx context.Context, sessionID string, consumedAt ...time.Time) error {
	t := time.Now()
	if len(consumedAt) > 0 && !consumedAt[0].IsZero() {
		t = consumedAt[0]
	}
	return s.WithTx(ctx, func(tx *gorm.DB) error {
		return tx.Model(&model.FeedbackSession{}).Where("id = ?", sessionID).Updates(map[string]any{
			"consumed_by_ai": true,
			"consumed_at":    t,
		}).Error
	})
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
	return s.listFeedbackSessions(ctx, projectDir, status, limit, false)
}

// ListFeedbackRoundSessions 与 ListFeedbackSessions 相同，但在 SQL 里排除「进度通知」（kind=notice）。
// 通知不是「轮」；若取完 limit 条再在内存里过滤，每发一条通知就会把一轮更早的汇报挤出窗口，
// 回执里 recent_workflows 的「N 轮」会随通知越变越少（#109）。
func (s *Store) ListFeedbackRoundSessions(ctx context.Context, projectDir, status string, limit int) ([]model.FeedbackSession, error) {
	return s.listFeedbackSessions(ctx, projectDir, status, limit, true)
}

func (s *Store) listFeedbackSessions(ctx context.Context, projectDir, status string, limit int, excludeNotices bool) ([]model.FeedbackSession, error) {
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
	if excludeNotices {
		query = query.Where(notNoticeSQL)
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

// briefSessionColumns 是轻量会话列表读的列：不含 summary / response_text / user_messages / images 等大体积正文。
// 「最新 N 行」与「每个工作流补一行」两条查询共用同一份，免得其中一条漏列。
var briefSessionColumns = []string{
	"id", "workflow_id", "host_name", "project_directory", "title", "status", "consumed_by_ai",
	"timeout_seconds", "no_feedback_checks", "max_no_feedback_checks", "prompt_wait_minutes",
	"wait_countdown_minutes", "wait_countdown_seconds", "deadline_at", "git_commit", "git_subject",
	"created_at", "updated_at",
}

// briefCoverExtraMax 是「每个工作流至少一行」最多额外补进来的行数（在最新 limit 行之外）。
// 一个工作流补一行，所以它也是侧栏最多能多看到的工作流个数；真实环境的工作流很少超过几百个，
// 上限只是防极端数据把响应撑大。
const briefCoverExtraMax = 500

// briefSessionWindow 取轻量会话列表的「最新 limit 行」（按创建时间倒序，跨所有工作流），不注入自定义标题。
func (s *Store) briefSessionWindow(ctx context.Context, projectDir, status string, limit int) ([]model.FeedbackSession, error) {
	if limit <= 0 || limit > 1000 {
		limit = 200
	}

	var sessions []model.FeedbackSession
	query := s.db.WithContext(ctx).Model(&model.FeedbackSession{}).Select(briefSessionColumns)

	if projectDir != "" && projectDir != "." {
		query = query.Where("project_directory = ?", projectDir)
	}
	if status != "" {
		query = query.Where("status = ?", status)
	}

	if err := query.Order("created_at DESC").Limit(limit).Find(&sessions).Error; err != nil {
		return nil, err
	}
	return sessions, nil
}

// applyCustomWorkflowTitles 注入工作流自定义锁定的固定标题，确保展示名称始终一致（查不到设置时原样返回，不报错）。
func (s *Store) applyCustomWorkflowTitles(ctx context.Context, sessions []model.FeedbackSession) {
	if len(sessions) == 0 {
		return
	}
	var customTitleSettings []model.SystemSetting
	if err := s.db.WithContext(ctx).Where("key LIKE 'wf_custom_title:%'").Find(&customTitleSettings).Error; err != nil || len(customTitleSettings) == 0 {
		return
	}
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

// WorkflowTitleOf 是工作流在界面上显示的名字：用户锁定的自定义标题优先，没有就取最近一条带标题的会话的标题；都没有返回空串。
// 只读、两次主键 / 索引查询；给 MCP 的 preset_run 这类要「用人话指一个工作流」的地方用（#179）。
func (s *Store) WorkflowTitleOf(ctx context.Context, workflowID string) string {
	wf := strings.TrimSpace(workflowID)
	if wf == "" {
		return ""
	}
	var custom []string
	if err := s.db.WithContext(ctx).Model(&model.SystemSetting{}).Where("key = ?", "wf_custom_title:"+wf).Limit(1).Pluck("value", &custom).Error; err == nil {
		if len(custom) > 0 && strings.TrimSpace(custom[0]) != "" {
			return strings.TrimSpace(custom[0])
		}
	}
	var titles []string
	if err := s.db.WithContext(ctx).Model(&model.FeedbackSession{}).Where("workflow_id = ? AND title <> ''", wf).
		Order("created_at DESC").Limit(1).Pluck("title", &titles).Error; err != nil || len(titles) == 0 {
		return ""
	}
	return strings.TrimSpace(titles[0])
}

// ListFeedbackSessionsBrief 获取轻量会话列表（通过字段投影排除大体积正文 summary、response_text、user_messages 和 images，专供小带宽场景下的列表加载）
func (s *Store) ListFeedbackSessionsBrief(ctx context.Context, projectDir, status string, limit int) ([]model.FeedbackSession, error) {
	sessions, err := s.briefSessionWindow(ctx, projectDir, status, limit)
	if err != nil {
		return nil, err
	}
	s.applyCustomWorkflowTitles(ctx, sessions)
	return sessions, nil
}

// ListFeedbackSessionsBriefCovering 在轻量列表「最新 limit 行」之外，再把每个工作流里最新的一行补进来（#173）。
//
// 为什么要补：最新 limit 行是按「会话行」数的，一个工作流跑一百轮就是一百行；话痨的工作流多了，
// 窗口里只剩它们，较早的工作流（哪怕没归档、没删除）一行都不在窗口里，侧栏与归档库就凭空少了它们。
// 侧栏和归档库要的是「每个工作流一条」，所以窗口之外的工作流各补它最新的一行（同样遵守项目目录与状态过滤）。
//
// 约定：
//   - 结果按创建时间倒序，与 ListFeedbackSessionsBrief 同序，调用方不必改排序；
//   - 补的行数最多 briefCoverExtraMax，取最近有动静的那些工作流；
//   - 没有工作流编号的老会话行不补（它们没有「工作流」可言，维持只看最新窗口的老行为）；
//   - 不改 limit 的含义：窗口仍是最新 limit 行，补进来的只是窗口之外的各工作流各一行。
func (s *Store) ListFeedbackSessionsBriefCovering(ctx context.Context, projectDir, status string, limit int) ([]model.FeedbackSession, error) {
	sessions, err := s.briefSessionWindow(ctx, projectDir, status, limit)
	if err != nil {
		return nil, err
	}
	covered := make(map[string]struct{}, len(sessions))
	seen := make(map[string]struct{}, len(sessions))
	for i := range sessions {
		seen[sessions[i].ID] = struct{}{}
		if sessions[i].WorkflowID != "" {
			covered[sessions[i].WorkflowID] = struct{}{}
		}
	}

	// 每个工作流里最新一行的创建时间：分组只读 workflow_id 与 created_at 两列，走复合索引 idx_fs_wf_created，不翻大正文。
	latest := s.db.WithContext(ctx).Model(&model.FeedbackSession{}).
		Select("workflow_id, MAX(created_at) AS latest_at").
		Where("workflow_id <> ''")
	if projectDir != "" && projectDir != "." {
		latest = latest.Where("project_directory = ?", projectDir)
	}
	if status != "" {
		latest = latest.Where("status = ?", status)
	}
	latest = latest.Group("workflow_id")

	qualified := make([]string, len(briefSessionColumns))
	for i, c := range briefSessionColumns {
		qualified[i] = "feedback_sessions." + c
	}
	rows := s.db.WithContext(ctx).Model(&model.FeedbackSession{}).
		Select(qualified).
		Joins("JOIN (?) AS lw ON lw.workflow_id = feedback_sessions.workflow_id AND lw.latest_at = feedback_sessions.created_at", latest)
	if projectDir != "" && projectDir != "." {
		rows = rows.Where("feedback_sessions.project_directory = ?", projectDir)
	}
	if status != "" {
		rows = rows.Where("feedback_sessions.status = ?", status)
	}
	// 已被窗口覆盖的工作流，它们最新的一行一定也在窗口里（比窗口里任何一行都新或相等），
	// 所以在「按创建时间倒序」的结果里它们必然排在最前面：多取 len(covered) 行，去重后正好剩下最多 briefCoverExtraMax 个窗口外的工作流。
	var latestRows []model.FeedbackSession
	if err := rows.Order("feedback_sessions.created_at DESC").Limit(len(covered) + briefCoverExtraMax).Find(&latestRows).Error; err != nil {
		return nil, err
	}

	added := 0
	for i := range latestRows {
		row := latestRows[i]
		if _, ok := covered[row.WorkflowID]; ok {
			continue // 这个工作流在最新窗口里已经有了
		}
		if _, ok := seen[row.ID]; ok {
			continue
		}
		covered[row.WorkflowID] = struct{}{} // 同一工作流里创建时间并列的两行（极端情况）也只留一行
		seen[row.ID] = struct{}{}
		sessions = append(sessions, row)
		added++
		if added >= briefCoverExtraMax {
			break
		}
	}
	if added > 0 {
		sort.SliceStable(sessions, func(a, b int) bool { return sessions[a].CreatedAt.After(sessions[b].CreatedAt) })
	}
	s.applyCustomWorkflowTitles(ctx, sessions)
	return sessions, nil
}

// CountFeedbackSessionsByWorkflow 数每个工作流在库里一共有多少行会话（含已归档），键是工作流编号，没有行的工作流不出现在结果里。
// 侧栏「N 轮」徽标要的是整个工作流的总数，而不是列表里恰好带出的行数；按工作流分组一次查出（走 idx_fs_wf_created 覆盖索引，不翻正文）。
func (s *Store) CountFeedbackSessionsByWorkflow(ctx context.Context, workflowIDs []string) (map[string]int, error) {
	out := make(map[string]int, len(workflowIDs))
	// 分批：SQLite 的绑定参数个数有上限，宁可多分几批（与 CountPendingConfirmsByWorkflow 同一做法）。
	const batch = 400
	for start := 0; start < len(workflowIDs); start += batch {
		end := start + batch
		if end > len(workflowIDs) {
			end = len(workflowIDs)
		}
		var rows []struct {
			WorkflowID string
			N          int64
		}
		err := s.db.WithContext(ctx).Model(&model.FeedbackSession{}).
			Select("workflow_id AS workflow_id, COUNT(*) AS n").
			Where("workflow_id IN ?", workflowIDs[start:end]).
			Group("workflow_id").Scan(&rows).Error
		if err != nil {
			return nil, err
		}
		for _, r := range rows {
			out[r.WorkflowID] = int(r.N)
		}
	}
	return out, nil
}

// ListWorkflowFeedbackSessions 获取指定工作流的所有完整会话（按 created_at ASC 升序，包含完整图文正文，按需呈现对话流）
func (s *Store) ListWorkflowFeedbackSessions(ctx context.Context, workflowID string) ([]model.FeedbackSession, error) {
	var sessions []model.FeedbackSession
	err := s.db.WithContext(ctx).Model(&model.FeedbackSession{}).
		Where("workflow_id = ? OR id = ?", workflowID, workflowID).
		Order("created_at ASC").
		Find(&sessions).Error
	if err != nil {
		return nil, err
	}

	// 注入工作流自定义标题
	var customTitle model.SystemSetting
	if err := s.db.WithContext(ctx).Where("key = ?", "wf_custom_title:"+workflowID).First(&customTitle).Error; err == nil && customTitle.Value != "" {
		for i := range sessions {
			sessions[i].Title = customTitle.Value
		}
	}

	return sessions, nil
}

// ListWorkflowSessionBodies 按 id 取一批会话的完整正文（#169 第二期「正文按需」）：
// 只读正文相关的列（summary / response_text / user_messages / images）与定位用的 id / 时间戳，不读其余列。
// 范围限定在这个工作流里（与 ListWorkflowFeedbackSessions 同一个口径：workflow_id 等于它，或会话 id 就是它），
// 所以拿别的工作流的 id 来问只会得到空结果，不会越界读到别处的正文。结果按创建时间升序；不存在的 id 直接略过。
func (s *Store) ListWorkflowSessionBodies(ctx context.Context, workflowID string, ids []string) ([]model.FeedbackSession, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	var rows []model.FeedbackSession
	err := s.db.WithContext(ctx).Model(&model.FeedbackSession{}).
		Select("id", "workflow_id", "summary", "response_text", "user_messages", "images", "created_at", "updated_at").
		Where("(workflow_id = ? OR id = ?) AND id IN ?", workflowID, workflowID, ids).
		Order("created_at ASC").
		Find(&rows).Error
	if err != nil {
		return nil, err
	}
	return rows, nil
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

		// D-159: 仅允许最新轮次 keepalive，历史轮次已终态冻结，严禁复活或保活
		if session.WorkflowID != "" {
			var latestSess model.FeedbackSession
			if err := tx.Where("workflow_id = ?", session.WorkflowID).Order("created_at DESC, rowid DESC").First(&latestSess).Error; err == nil {
				if latestSess.ID != session.ID {
					return NewConflictError(fmt.Sprintf("session %q 是历史轮次，已终态冻结，严禁复活或保活", sessionID), 0)
				}
			}
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

// UpdateSessionWaitCountdownSeconds 按秒设置挂起倒计时（秒回 0 / 30 / 60 …）。
// 同步写入分钟列（向上取整），旧界面和旧二进制读到的仍然是一个合理的值。
func (s *Store) UpdateSessionWaitCountdownSeconds(ctx context.Context, idOrWorkflow string, seconds int) (*model.FeedbackSession, error) {
	if seconds < 0 {
		return nil, fmt.Errorf("countdown_seconds 必须 >= 0")
	}
	minutes := (seconds + 59) / 60
	var session model.FeedbackSession
	err := s.WithTx(ctx, func(tx *gorm.DB) error {
		now := time.Now()
		wf := ""
		if err := tx.Where("id = ?", idOrWorkflow).First(&session).Error; err == nil {
			wf = session.WorkflowID
		} else if err := tx.Where("workflow_id = ?", idOrWorkflow).Order("created_at DESC").First(&session).Error; err == nil {
			wf = idOrWorkflow
		} else {
			return NewNotFoundError(fmt.Sprintf("session or workflow %q not found", idOrWorkflow))
		}
		upd := map[string]interface{}{"wait_countdown_seconds": seconds, "wait_countdown_minutes": minutes, "updated_at": now}
		var q *gorm.DB
		if wf != "" {
			q = tx.Model(&model.FeedbackSession{}).Where("workflow_id = ?", wf)
		} else {
			q = tx.Model(&model.FeedbackSession{}).Where("id = ?", session.ID)
		}
		if err := q.Updates(upd).Error; err != nil {
			return err
		}
		session.WaitCountdownSeconds = seconds
		session.WaitCountdownMinutes = minutes
		session.UpdatedAt = now
		return nil
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
						"wait_countdown_seconds": -1,
						"updated_at":             now,
					})
			}
			session.WaitCountdownMinutes = countdownMinutes
			session.WaitCountdownSeconds = -1
			session.UpdatedAt = now
			return tx.Save(&session).Error
		}

		if err := tx.Where("workflow_id = ?", idOrWorkflow).Order("created_at DESC").First(&session).Error; err == nil {
			if err := tx.Model(&model.FeedbackSession{}).
				Where("workflow_id = ?", idOrWorkflow).
				Updates(map[string]interface{}{
					"wait_countdown_minutes": countdownMinutes,
					"wait_countdown_seconds": -1,
					"updated_at":             now,
				}).Error; err != nil {
				return err
			}
			session.WaitCountdownMinutes = countdownMinutes
			session.WaitCountdownSeconds = -1
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

// DeleteFeedbackSession 物理删除会话记录，维护工作流状态并级联清理（D-159）
func (s *Store) DeleteFeedbackSession(ctx context.Context, sessionID string) (*model.FeedbackSession, error) {
	var session model.FeedbackSession
	err := s.WithTx(ctx, func(tx *gorm.DB) error {
		if err := tx.Where("id = ?", sessionID).First(&session).Error; err != nil {
			if err == gorm.ErrRecordNotFound {
				return NewNotFoundError(fmt.Sprintf("session %q not found", sessionID))
			}
			return err
		}

		if err := tx.Delete(&session).Error; err != nil {
			return err
		}

		// 检查该工作流下是否还有剩余会话
		if session.WorkflowID != "" {
			var remainingCount int64
			_ = tx.Model(&model.FeedbackSession{}).Where("workflow_id = ?", session.WorkflowID).Count(&remainingCount).Error
			if remainingCount == 0 {
				// 若已全部清空，清理该工作流的元数据
				_ = tx.Where("key = ?", "wf_custom_title:"+session.WorkflowID).Delete(&model.SystemSetting{}).Error
				_ = tx.Where("workflow_id = ?", session.WorkflowID).Delete(&model.WorkflowPhaseState{}).Error
				_ = tx.Where("workflow_id = ?", session.WorkflowID).Delete(&model.WorkflowCheckpoint{}).Error
				_ = tx.Where("workflow_id = ?", session.WorkflowID).Delete(&model.WorkflowNote{}).Error
				_ = tx.Where("workflow_id = ?", session.WorkflowID).Delete(&model.WorkflowDraft{}).Error
				_ = tx.Where("workflow_id = ?", session.WorkflowID).Delete(&model.DraftImage{}).Error
			}
		}

		return nil
	})

	if err != nil {
		return nil, err
	}
	return &session, nil
}

// MarkWorkflowHistorySessionsConsumed 自动将某工作流下所有已完成但未被消费的历史轮次批量打标为已消费（D-159）
func (s *Store) MarkWorkflowHistorySessionsConsumed(ctx context.Context, workflowID string) error {
	workflowID = strings.TrimSpace(workflowID)
	if workflowID == "" {
		return nil
	}
	now := time.Now()
	return s.WithTx(ctx, func(tx *gorm.DB) error {
		return tx.Model(&model.FeedbackSession{}).
			Where("workflow_id = ? AND status = 'completed' AND consumed_by_ai = false", workflowID).
			Updates(map[string]any{
				"consumed_by_ai": true,
				"consumed_at":    now,
			}).Error
	})
}

// RestoreFeedbackSession 复原被删除的会话（支持撤销删除）
func (s *Store) RestoreFeedbackSession(ctx context.Context, session *model.FeedbackSession) (*model.FeedbackSession, error) {
	if session == nil || session.ID == "" {
		return nil, NewInvalidInputError("invalid session data to restore")
	}
	err := s.WithTx(ctx, func(tx *gorm.DB) error {
		return tx.Save(session).Error
	})
	if err != nil {
		return nil, err
	}
	return session, nil
}

// GetLatestAnchoredSession 返回某工作流里最近一次带交付锚点的会话（只读取轻量列）。
// excludeSessionID 非空时跳过该会话（用于「与上一次比较」）。没有任何锚点时返回 (nil, nil)。
func (s *Store) GetLatestAnchoredSession(ctx context.Context, workflowID, excludeSessionID string) (*model.FeedbackSession, error) {
	workflowID = strings.TrimSpace(workflowID)
	if workflowID == "" {
		return nil, nil
	}
	q := s.db.WithContext(ctx).
		Select("id", "workflow_id", "git_commit", "git_subject", "created_at").
		Where("workflow_id = ? AND git_commit IS NOT NULL AND git_commit != ''", workflowID)
	if excludeSessionID != "" {
		q = q.Where("id != ?", excludeSessionID)
	}
	var sess model.FeedbackSession
	if err := q.Order("created_at DESC").Limit(1).Take(&sess).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return &sess, nil
}

// FindAnchoredSessionByCommit 在某工作流里按哈希找到带该锚点的最近一次会话。
// 哈希允许长短不一（互为前缀即视为同一提交），用于把胶囊的基准关联回具体交付。
func (s *Store) FindAnchoredSessionByCommit(ctx context.Context, workflowID, commit string) (*model.FeedbackSession, error) {
	workflowID = strings.TrimSpace(workflowID)
	commit = strings.ToLower(strings.TrimSpace(commit))
	if workflowID == "" || commit == "" {
		return nil, nil
	}
	var rows []model.FeedbackSession
	if err := s.db.WithContext(ctx).
		Select("id", "workflow_id", "git_commit", "git_subject", "created_at").
		Where("workflow_id = ? AND git_commit IS NOT NULL AND git_commit != ''", workflowID).
		Order("created_at DESC").Limit(500).Find(&rows).Error; err != nil {
		return nil, err
	}
	for i := range rows {
		got := strings.ToLower(rows[i].GitCommit)
		if got == commit || strings.HasPrefix(got, commit) || strings.HasPrefix(commit, got) {
			return &rows[i], nil
		}
	}
	return nil, nil
}

// CountRecentNotices 统计某个工作流自 since 以来发布的进度通知条数，给发布端做频率上限用。
func (s *Store) CountRecentNotices(ctx context.Context, workflowID string, since time.Time) (int, error) {
	var n int64
	err := s.db.WithContext(ctx).Model(&model.FeedbackSession{}).
		Where("workflow_id = ? AND kind = ? AND created_at >= ?", strings.TrimSpace(workflowID), model.FeedbackKindNotice, since).
		Count(&n).Error
	return int(n), err
}
