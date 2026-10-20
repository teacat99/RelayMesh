package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/teacat99/RelayMesh/v2/internal/acl"
	"github.com/teacat99/RelayMesh/v2/internal/config"
	"github.com/teacat99/RelayMesh/v2/internal/model"
	"github.com/teacat99/RelayMesh/v2/internal/store"
)

// preset_run：项目管理 Agent 运行已有的「非交互续聊」预设（#179，设计稿 design/headless-new-conversation.md §8）。
//
// 两道门缺一不可，而且都在这里（MCP 一侧）把关：
//  1. 凭据带「运行预设」权限位（Permissions.Launcher；没有它，这个工具连 tools/list 里都没有，dispatchTool 也先拒绝）；
//  2. 调用方是它所在项目的管理 Agent（持有 tasks 范围授权，acl.CanManageTasks）。管理者的判定与项目的访问控制档位无关——
//     项目在 off 档、没有开 acl，用户在「任务管理」里勾选的 Agent 仍然是管理 Agent。
//
// 其余的一切（预设的资格、全部执行闸门、频率账本、审计、聊天系统行）在 Hub 一侧（terminal.Hub.AgentRunPreset），
// 这里只做协议适配与项目审计。身份是调用方自报的（workflow_id + 目录），不是安全边界；
// 真正的约束是凭据位（人授予、随时能关）与频率护栏——文档里如实写明。

// PresetRunActor 是经 preset_run 发起操作的调用方；MCP 一侧已经确认它是 ProjectID 这个项目的管理 Agent。
type PresetRunActor struct {
	IdentityID uint
	Label      string
	WorkflowID string
	ProjectID  uint
}

// PresetRunOutcome 是运行提交成功后的结果：Result 原样放进回执，其余字段给 MCP 一侧写项目审计用（不含提示词与命令）。
type PresetRunOutcome struct {
	Result     any
	Preset     string
	Name       string
	WorkflowID string
	RunID      string
	State      string
}

// PresetRunner 是 MCP 运行预设所需的终端 Hub 能力。错误可以实现 ErrorCode() string 以带上稳定的拒绝码。
type PresetRunner interface {
	// List 列出 actor 有资格运行的预设，presets / limits 原样放进回执。
	List(ctx context.Context, actor PresetRunActor) (presets any, limits any, err error)
	// Status 看一条预设此刻的现状。
	Status(ctx context.Context, actor PresetRunActor, preset string) (any, error)
	// Run 运行一条预设。
	Run(ctx context.Context, actor PresetRunActor, preset string) (*PresetRunOutcome, error)
}

// SetPresetRunner 注入运行预设的能力；终端功能不可用时不注入，工具返回明确的错误。
func (s *Server) SetPresetRunner(r PresetRunner) {
	s.termMu.Lock()
	s.presetRunner = r
	s.termMu.Unlock()
}

func (s *Server) presetRunnerOf() PresetRunner {
	s.termMu.RLock()
	defer s.termMu.RUnlock()
	return s.presetRunner
}

// presetRunError 是 MCP 一侧（调用方资格）的拒绝：Code 稳定，Message 是一句带下一步的中文。
type presetRunError struct {
	Code    string
	Message string
}

func (e *presetRunError) Error() string     { return e.Message }
func (e *presetRunError) ErrorCode() string { return e.Code }

// presetRunFail 把带稳定错误码的拒绝以 JSON 呈现并标记 isError（形状与终端工具一致：{"error":{"code","message"}}）；其他错误原样返回。
func presetRunFail(err error) (any, bool, error) {
	var ce interface{ ErrorCode() string }
	if errors.As(err, &ce) {
		payload := map[string]any{"error": map[string]string{"code": ce.ErrorCode(), "message": err.Error()}}
		b, _ := json.MarshalIndent(payload, "", "  ")
		return toolCallResult{Content: []mcpContentItem{{Type: "text", Text: string(b)}}, IsError: true}, true, nil
	}
	return nil, true, err
}

// presetErrCode 取错误里的稳定拒绝码；不带码的（数据库故障之类的内部错误）返回空串。
func presetErrCode(err error) string {
	var ce interface{ ErrorCode() string }
	if errors.As(err, &ce) {
		return ce.ErrorCode()
	}
	return ""
}

// presetCaller 是通过两道门之后的调用方。
type presetCaller struct {
	actor  PresetRunActor
	proj   *model.Project
	caller *aclCaller
}

// presetCallerOf 判定调用方：认出身份 → 找到它的工作流所在项目的任务板 → 确认它是这个项目的管理 Agent。
// 拒绝时返回 *presetRunError；认出了项目的拒绝（不是管理 Agent）同时记进项目审计。
func (s *Server) presetCallerOf(ctx context.Context, credCtx *CredentialContext, wf, dir, host string) (*presetCaller, *presetRunError) {
	wf = strings.TrimSpace(wf)
	if wf == "" {
		return nil, &presetRunError{"workflow_id_required", "preset_run 需要 workflow_id（你自己的工作流编号）：服务端用它认出你是谁、你管理哪个项目"}
	}
	var cred uint
	host = strings.TrimSpace(host)
	if credCtx != nil {
		cred = credCtx.CredentialID
		if host == "" {
			host = credCtx.HostName
		}
	}
	id, asserted := s.store.ResolveCallerIdentity(ctx, wf, cred, host, dir, time.Now())
	if id == 0 {
		return nil, &presetRunError{"identity_unknown", "认不出你的身份：这个工作流还没有带目录的汇报会话。先用 interactive_feedback 或 task_board 汇报 / 操作一次，或者在调用里带上 project_directory 与 host_name"}
	}
	notInProject := &presetRunError{"not_in_project", "你的工作流不在任何项目的任务板上，没有可管理的项目：运行预设只对项目管理 Agent 开放"}
	key, err := s.store.BoardKeyOf(ctx, wf)
	if err != nil {
		return nil, notInProject
	}
	pid, _, ok := store.ParseBoardKey(key)
	if !ok || pid == 0 {
		return nil, notInProject
	}
	proj, err := s.store.GetProject(ctx, pid)
	if err != nil || proj == nil || proj.ArchivedAt != nil {
		return nil, notInProject
	}
	label := s.store.IdentityLabelOf(ctx, id)
	pc := &presetCaller{
		actor:  PresetRunActor{IdentityID: id, Label: label, WorkflowID: wf, ProjectID: proj.ID},
		proj:   proj,
		caller: &aclCaller{IdentityID: id, Asserted: asserted, Label: label, Project: proj},
	}
	grants, _ := s.store.ActiveGrantsForCaller(ctx, proj.ID, id, wf)
	if !acl.CanManageTasks(id, proj.ID, grants).Allow {
		s.presetRunAudit(ctx, pc, model.AuditDeny, "preset:*", map[string]any{"code": "not_project_manager"})
		return nil, &presetRunError{"not_project_manager", "只有项目管理 Agent 才能运行预设：请用户在项目页「任务管理」里把你勾选为管理 Agent（持有任务管理授权）"}
	}
	return pc, nil
}

// presetRunAudit 把一次运行（或被拒绝的运行）记进项目审计：范围 tasks、动作 launcher.run。只记预设、工作流、运行编号与拒绝码，不记提示词与命令。
func (s *Server) presetRunAudit(ctx context.Context, pc *presetCaller, result, target string, detail map[string]any) {
	if pc == nil || pc.proj == nil {
		return
	}
	var dedupe time.Duration
	if result == model.AuditDeny {
		// 被拒绝的尝试：同一个预设、同一个原因 30 分钟内只记一条（避免循环重试刷屏）；不同的预设或不同的原因各记各的。
		dedupe = store.AuditCrossReadDedupe
	}
	s.aclAuditIn(ctx, pc.proj, pc.caller, pc.actor.WorkflowID, model.AuditLauncherRun, result, model.ScopeTasks,
		strconv.FormatUint(uint64(pc.proj.ID), 10), target, dedupe, true, detail)
}

func clampRunes(s string, n int) string {
	if rs := []rune(s); len(rs) > n {
		return string(rs[:n]) + "…"
	}
	return s
}

func (s *Server) handlePresetRun(ctx context.Context, credCtx *CredentialContext, raw json.RawMessage) (any, bool, error) {
	runner := s.presetRunnerOf()
	if runner == nil {
		return presetRunFail(&presetRunError{"terminal_unavailable", "终端功能在此服务上不可用，无法运行预设"})
	}
	// 令牌强度：与终端工具同样的运行时兜底（创建 / 启用时服务端已校验过一遍；这里防数据库被手工改动、或令牌后来被判定为弱）。
	if credCtx == nil || config.MCPTokenWeakness(credCtx.TokenString) != "" {
		w := "没有令牌"
		if credCtx != nil {
			w = config.MCPTokenWeakness(credCtx.TokenString)
		}
		return presetRunFail(&presetRunError{"weak_token", "令牌强度不足以代表运行预设（" + w + "），请在 Web 的权限页重新生成令牌"})
	}
	var args struct {
		Action           string `json:"action"`
		Preset           string `json:"preset"`
		WorkflowID       string `json:"workflow_id"`
		ProjectDirectory string `json:"project_directory"`
		HostName         string `json:"host_name"`
	}
	if err := decodeToolArgs(raw, &args); err != nil {
		return presetRunFail(&presetRunError{"bad_request", err.Error()})
	}
	action := strings.ToLower(strings.TrimSpace(args.Action))
	switch action {
	case "":
		action = "list"
	case "list", "status", "run":
	default:
		return presetRunFail(&presetRunError{"bad_action", "action 只能是 list / status / run，得到 " + strconv.Quote(clampRunes(action, 30))})
	}
	preset := strings.TrimSpace(args.Preset)
	if action != "list" && preset == "" {
		return presetRunFail(&presetRunError{"preset_required", "status / run 需要 preset（预设的编号或名字）：先用 list 看你能运行哪些"})
	}

	pc, gateErr := s.presetCallerOf(ctx, credCtx, args.WorkflowID, args.ProjectDirectory, args.HostName)
	if gateErr != nil {
		return presetRunFail(gateErr)
	}

	switch action {
	case "list":
		presets, limits, err := runner.List(ctx, pc.actor)
		if err != nil {
			return presetRunFail(err)
		}
		return map[string]any{
			"project": map[string]any{"id": pc.proj.ID, "name": pc.proj.Name},
			"presets": presets,
			"limits":  limits,
			"hint":    "run 只会按预设里定死的内容运行；要告诉子会话做什么，请先指派任务或写它的收件箱，再运行预设唤醒它。",
		}, false, nil
	case "status":
		view, err := runner.Status(ctx, pc.actor, preset)
		if err != nil {
			return presetRunFail(err)
		}
		return view, false, nil
	}

	out, err := runner.Run(ctx, pc.actor, preset)
	if err != nil {
		// 只有业务上的拒绝（带码）记审计；内部错误原样报告给调用方，不伪装成拒绝。
		if code := presetErrCode(err); code != "" && code != "terminal_unavailable" {
			s.presetRunAudit(ctx, pc, model.AuditDeny, "preset:"+clampRunes(preset, 60), map[string]any{"code": code})
		}
		return presetRunFail(err)
	}
	s.presetRunAudit(ctx, pc, model.AuditAllow, "preset:"+out.Preset, map[string]any{
		"preset": out.Preset, "name": out.Name, "workflow_id": out.WorkflowID, "run_id": out.RunID, "state": out.State,
	})
	return out.Result, false, nil
}
