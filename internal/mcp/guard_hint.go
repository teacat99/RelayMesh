package mcp

import (
	"context"
	"strings"

	"github.com/teacat99/RelayMesh/v2/internal/terminal"
)

const guardPermitHintLine = `guard: 任务全部完成、准备结束会话前，调用 workflow_context(action:"wake_permit", permit:0)；触发一次后自动恢复为 1`

func (s *Server) guardPermitHintHeader(ctx context.Context, workflowID string) string {
	wf := strings.TrimSpace(workflowID)
	if wf == "" || s.store == nil {
		return ""
	}
	launchers, err := s.store.ListTerminalLaunchers(ctx)
	if err != nil {
		return ""
	}
	if terminal.FindGuardPresetByWorkflow(launchers, wf) == nil {
		return ""
	}
	return guardPermitHintLine
}
