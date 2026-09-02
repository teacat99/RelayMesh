package mcp

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/teacat99/RelayMesh/internal/model"
	"github.com/teacat99/RelayMesh/internal/store"
)

type reportArguments struct {
	Action                       string                `json:"action"`
	TaskID                       string                `json:"task_id"`
	KnownTaskRevision            int64                 `json:"known_task_revision,omitempty"`
	AcknowledgedTaskRevision     int64                 `json:"acknowledged_task_revision,omitempty"`
	AfterFeedbackSequence        int64                 `json:"after_feedback_sequence,omitempty"`
	AcknowledgedFeedbackSequence int64                 `json:"acknowledged_feedback_sequence,omitempty"`
	Kind                         string                `json:"kind,omitempty"`
	Body                         string                `json:"body,omitempty"`
	References                   []model.PathReference `json:"references,omitempty"`
	IdempotencyKey               string                `json:"idempotency_key,omitempty"`
}

func (s *Server) handleReportProgress(ctx context.Context, raw json.RawMessage) (any, error) {
	var args reportArguments
	if err := json.Unmarshal(raw, &args); err != nil {
		return nil, fmt.Errorf("invalid arguments: %w", err)
	}

	projectID := s.cfg.ProjectID

	// 更新执行端活跃心跳
	if args.TaskID != "" {
		_ = s.store.UpdateTaskHeartbeat(ctx, projectID, args.TaskID, "executor", "mcp-executor")
	}

	switch args.Action {
	case "sync":
		result, err := s.store.Sync(ctx, model.SyncInput{
			ProjectID:                    projectID,
			TaskID:                       args.TaskID,
			KnownTaskRevision:            args.KnownTaskRevision,
			AcknowledgedTaskRevision:     args.AcknowledgedTaskRevision,
			AfterFeedbackSequence:        args.AfterFeedbackSequence,
			AcknowledgedFeedbackSequence: args.AcknowledgedFeedbackSequence,
		})
		return withInstruction(result, "请按返回的分段内容与指引执行；MCP 不直接管理本地系统进程。"), err

	case "report":
		result, err := s.store.AddReport(ctx, model.AddReportInput{
			ProjectID:                    projectID,
			TaskID:                       args.TaskID,
			AcknowledgedTaskRevision:     args.AcknowledgedTaskRevision,
			AcknowledgedFeedbackSequence: args.AcknowledgedFeedbackSequence,
			Kind:                         args.Kind,
			Body:                         args.Body,
			References:                   args.References,
			IdempotencyKey:               args.IdempotencyKey,
		})
		s.notifyTaskUpdate(args.TaskID)
		return result, err

	case "check_feedback":
		result, err := s.store.CheckFeedback(ctx, model.CheckFeedbackInput{
			ProjectID:                    projectID,
			TaskID:                       args.TaskID,
			AfterFeedbackSequence:        args.AfterFeedbackSequence,
			AcknowledgedTaskRevision:     args.AcknowledgedTaskRevision,
			AcknowledgedFeedbackSequence: args.AcknowledgedFeedbackSequence,
			IdempotencyKey:               args.IdempotencyKey,
		})
		return result, err

	default:
		return nil, store.NewInvalidInputError(fmt.Sprintf("unknown report_progress action %q", args.Action))
	}
}
