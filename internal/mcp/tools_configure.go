package mcp

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/teacat99/RelayMesh/internal/model"
	"github.com/teacat99/RelayMesh/internal/store"
)

type configureArguments struct {
	Action                string                `json:"action"`
	TaskID                string                `json:"task_id,omitempty"`
	Title                 string                `json:"title,omitempty"`
	Mode                  string                `json:"mode,omitempty"`
	CurrentStageID        string                `json:"current_stage_id,omitempty"`
	Stages                model.TaskStages      `json:"stages,omitempty"`
	Segments              []model.Segment       `json:"segments,omitempty"`
	WaitPolicy            *model.WaitPolicy     `json:"wait_policy,omitempty"`
	ExpectedRevision      int64                 `json:"expected_revision,omitempty"`
	Segment               string                `json:"segment,omitempty"`
	OldText               string                `json:"old_text,omitempty"`
	NewText               string                `json:"new_text,omitempty"`
	Source                string                `json:"source,omitempty"`
	Body                  string                `json:"body,omitempty"`
	References            []model.PathReference `json:"references,omitempty"`
	IdempotencyKey        string                `json:"idempotency_key,omitempty"`
	State                 string                `json:"state,omitempty"`
	Cursor                string                `json:"cursor,omitempty"`
	Limit                 int                   `json:"limit,omitempty"`
	AfterReportSequence   int64                 `json:"after_report_sequence,omitempty"`
	ThroughReportSequence int64                 `json:"through_report_sequence,omitempty"`
}

func (s *Server) handleConfigureTask(ctx context.Context, raw json.RawMessage) (any, error) {
	var args configureArguments
	if err := json.Unmarshal(raw, &args); err != nil {
		return nil, fmt.Errorf("invalid arguments: %w", err)
	}

	projectID := s.cfg.ProjectID

	// 更新指挥端活跃心跳
	if args.TaskID != "" {
		_ = s.store.UpdateTaskHeartbeat(ctx, projectID, args.TaskID, "commander", "mcp-commander")
	}

	switch args.Action {
	case "create":
		policy := s.defaultWaitPolicy()
		if args.WaitPolicy != nil {
			policy = mergeWaitPolicy(policy, *args.WaitPolicy)
		}
		task, err := s.store.CreateTask(ctx, model.CreateTaskInput{
			ProjectID:      projectID,
			TaskID:         args.TaskID,
			Title:          args.Title,
			Mode:           args.Mode,
			Stages:         args.Stages,
			Segments:       args.Segments,
			WaitPolicy:     policy,
			IdempotencyKey: args.IdempotencyKey,
		})
		s.notifyTaskUpdate(args.TaskID)
		return withInstruction(task, "托管任务通信已创建；指挥端可继续更新阶段大盘/分段工单，或等待执行端同步。"), err

	case "update":
		result, err := s.store.UpdateTask(ctx, model.UpdateTaskInput{
			ProjectID:        projectID,
			TaskID:           args.TaskID,
			ExpectedRevision: args.ExpectedRevision,
			Mode:             args.Mode,
			Segment:          args.Segment,
			OldText:          args.OldText,
			NewText:          args.NewText,
			IdempotencyKey:   args.IdempotencyKey,
		})
		s.notifyTaskUpdate(args.TaskID)
		return withInstruction(result, "任务分段已更新；执行端下次同步会收到新的 revision。"), err

	case "update_stages":
		result, err := s.store.UpdateStages(ctx, model.UpdateStagesInput{
			ProjectID:        projectID,
			TaskID:           args.TaskID,
			ExpectedRevision: args.ExpectedRevision,
			CurrentStageID:   args.CurrentStageID,
			Stages:           args.Stages,
			IdempotencyKey:   args.IdempotencyKey,
		})
		s.notifyTaskUpdate(args.TaskID)
		return withInstruction(result, "阶段大盘进度已更新；前端展示台与执行端已同步最新阶段。"), err

	case "set_wait_policy":
		if args.WaitPolicy == nil {
			return nil, store.NewInvalidInputError("wait_policy is required")
		}
		policy := mergeWaitPolicy(s.defaultWaitPolicy(), *args.WaitPolicy)
		result, err := s.store.SetWaitPolicy(ctx, model.SetWaitPolicyInput{
			ProjectID:        projectID,
			TaskID:           args.TaskID,
			ExpectedRevision: args.ExpectedRevision,
			WaitPolicy:       policy,
			IdempotencyKey:   args.IdempotencyKey,
		})
		s.notifyTaskUpdate(args.TaskID)
		return withInstruction(result, "等待提示策略已更新；它只影响后续通信提示，不管理真实进程。"), err

	case "send_feedback":
		src := args.Source
		if src == "" {
			src = "commander"
		}
		feedback, err := s.store.SendFeedback(ctx, model.SendFeedbackInput{
			ProjectID:        projectID,
			TaskID:           args.TaskID,
			ExpectedRevision: args.ExpectedRevision,
			Source:           src,
			Body:             args.Body,
			References:       args.References,
			IdempotencyKey:   args.IdempotencyKey,
		})
		s.notifyTaskUpdate(args.TaskID)
		return withInstruction(feedback, "反馈已保存；执行端同步后可继续推进。"), err

	case "get":
		task, err := s.store.GetTask(ctx, projectID, args.TaskID)
		return withInstruction(task, "读取不会标记报告已处理；处理报告后请显式调用 ack_reports。"), err

	case "list":
		page, err := s.store.ListTasks(ctx, projectID, args.State, args.Cursor, args.Limit, false)
		return withInstruction(page, "如 next_cursor 非空，请原样传回 cursor 获取下一页。"), err

	case "list_updates":
		page, err := s.store.ListTasks(ctx, projectID, args.State, args.Cursor, args.Limit, true)
		return withInstruction(page, "只读取有未确认报告的任务；读取详情后仍需显式 ack_reports。"), err

	case "read_reports":
		page, err := s.store.ReadReports(ctx, projectID, args.TaskID, args.AfterReportSequence, args.Limit)
		return withInstruction(page, "报告读取不会自动确认；处理完成后调用 ack_reports。"), err

	case "ack_reports":
		summary, err := s.store.AckReports(ctx, projectID, args.TaskID, args.ThroughReportSequence)
		s.notifyTaskUpdate(args.TaskID)
		return withInstruction(summary, "报告确认游标已按实际存在的 sequence 单调推进。"), err

	case "close":
		result, err := s.store.CloseTask(ctx, projectID, args.TaskID, args.ExpectedRevision, args.IdempotencyKey)
		s.notifyTaskUpdate(args.TaskID)
		return withInstruction(result, "任务通信已关闭；历史仍可读取，但后续修改和报告会被拒绝。"), err

	default:
		return nil, store.NewInvalidInputError(fmt.Sprintf("unknown configure_task action %q", args.Action))
	}
}

func (s *Server) defaultWaitPolicy() model.WaitPolicy {
	return model.WaitPolicy{
		AfterMinutes:         s.cfg.WaitAfterMinutes,
		MaxNoFeedbackChecks:  s.cfg.MaxNoFeedbackChecks,
		WaitInstruction:      s.cfg.WaitInstruction,
		ExhaustedInstruction: s.cfg.WaitExhaustedInstruction,
	}
}

func mergeWaitPolicy(base, override model.WaitPolicy) model.WaitPolicy {
	if override.AfterMinutes != 0 {
		base.AfterMinutes = override.AfterMinutes
	}
	if override.MaxNoFeedbackChecks != 0 {
		base.MaxNoFeedbackChecks = override.MaxNoFeedbackChecks
	}
	if override.WaitInstruction != "" {
		base.WaitInstruction = override.WaitInstruction
	}
	if override.ExhaustedInstruction != "" {
		base.ExhaustedInstruction = override.ExhaustedInstruction
	}
	return base
}

func withInstruction(value any, instruction string) map[string]any {
	return map[string]any{"data": value, "next_instruction": instruction}
}
