package api

import (
	"context"

	"github.com/teacat99/RelayMesh/v2/internal/mcp"
	"github.com/teacat99/RelayMesh/v2/internal/terminal"
)

// presetRunner 把终端 Hub 的「项目管理 Agent 运行预设」能力（terminal/agent_run.go）接给 MCP 的 preset_run 工具（#179）。
// 两边各自定义类型（mcp 不引用 terminal 包，保持与 termexec 同样的解耦方式），这里只做字段搬运；规则全在 Hub 一侧。
type presetRunner struct{ hub *terminal.Hub }

var _ mcp.PresetRunner = presetRunner{}

func (p presetRunner) actor(a mcp.PresetRunActor) terminal.AgentRunActor {
	return terminal.AgentRunActor{IdentityID: a.IdentityID, Label: a.Label, WorkflowID: a.WorkflowID, ProjectID: a.ProjectID}
}

func (p presetRunner) List(ctx context.Context, a mcp.PresetRunActor) (any, any, error) {
	views, err := p.hub.AgentPresets(ctx, p.actor(a))
	if err != nil {
		return nil, nil, err
	}
	return views, p.hub.AgentRunLimitsView(), nil
}

func (p presetRunner) Status(ctx context.Context, a mcp.PresetRunActor, preset string) (any, error) {
	v, err := p.hub.AgentPresetStatus(ctx, p.actor(a), preset)
	if err != nil {
		return nil, err
	}
	return v, nil
}

func (p presetRunner) Run(ctx context.Context, a mcp.PresetRunActor, preset string) (*mcp.PresetRunOutcome, error) {
	res, err := p.hub.AgentRunPreset(ctx, p.actor(a), preset)
	if err != nil {
		return nil, err
	}
	return &mcp.PresetRunOutcome{Result: res, Preset: res.Preset, Name: res.Name, WorkflowID: res.WorkflowID, RunID: res.RunID, State: res.State, Branch: res.Branch}, nil
}
