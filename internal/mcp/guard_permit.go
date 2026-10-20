package mcp

import (
	"context"
	"fmt"
	"strings"

	"github.com/teacat99/RelayMesh/v2/internal/terminal"
)

func (s *Server) handleWakePermit(ctx context.Context, args workflowContextArgs) (any, error) {
	if args.WorkflowID == "" {
		return nil, fmt.Errorf("workflow_id is required for wake_permit")
	}
	if !args.Permit.Set {
		return nil, fmt.Errorf("permit is required for wake_permit (0 or 1)")
	}
	if args.Permit.V != 0 && args.Permit.V != 1 {
		return nil, fmt.Errorf("permit must be 0 or 1")
	}
	note := strings.TrimSpace(args.Note)
	if len([]rune(note)) > 200 {
		return nil, fmt.Errorf("note must be at most 200 characters")
	}
	launchers, err := s.store.ListTerminalLaunchers(ctx)
	if err != nil {
		return nil, err
	}
	preset := terminal.FindGuardPresetByWorkflow(launchers, args.WorkflowID)
	if preset == nil {
		return map[string]any{
			"status":      "skipped",
			"workflow_id": args.WorkflowID,
			"message":     "没有绑定守护，无需设置",
		}, nil
	}
	if s.termGuard == nil {
		return nil, fmt.Errorf("terminal hub not available")
	}
	permit, err := s.termGuard.SetGuardWakePermit(ctx, preset.ID, args.Permit.V, "agent", note)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"status":      "ok",
		"workflow_id": args.WorkflowID,
		"preset_id":   preset.ID,
		"permit":      permit,
	}, nil
}
