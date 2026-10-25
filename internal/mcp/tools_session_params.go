package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/teacat99/RelayMesh/v2/internal/model"
	"github.com/teacat99/RelayMesh/v2/internal/store"
)

// 会话参数（#236 P3）的 MCP 侧：session_params 工具，以及回执头里 branch: 行后面的 params: 行。
//
// 权限与取值规则都在 store.ApplySessionParams / GetSessionParams（调用者是谁取自 workflow_id，与 branch 工具同一口径）；
// 这里只做参数、目标分支的解析与呈现。与分支无关的工作流调用它得到「没有分支关系」的说明，不报错（INV-9）。

// looseObject 容忍模型把对象写成 JSON 字符串（部分客户端会把嵌套对象序列化成字符串再传）。
type looseObject map[string]any

func (o *looseObject) UnmarshalJSON(b []byte) error {
	trimmed := strings.TrimSpace(string(b))
	if trimmed == "" || trimmed == "null" {
		return nil
	}
	if strings.HasPrefix(trimmed, `"`) {
		var inner string
		if err := json.Unmarshal(b, &inner); err != nil {
			return err
		}
		if strings.TrimSpace(inner) == "" {
			return nil
		}
		b = []byte(inner)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		return fmt.Errorf("patch 需要是对象（如 {\"phase\": \"dev\"}）: %w", err)
	}
	*o = m
	return nil
}

type sessionParamsArgs struct {
	Action     string      `json:"action,omitempty"`
	WorkflowID string      `json:"workflow_id"`
	Target     string      `json:"target,omitempty"`
	Patch      looseObject `json:"patch,omitempty"`
}

func (s *Server) handleSessionParams(ctx context.Context, args json.RawMessage) (any, error) {
	var a sessionParamsArgs
	if err := json.Unmarshal(args, &a); err != nil {
		return nil, fmt.Errorf("参数无效: %w", err)
	}
	wf := strings.TrimSpace(a.WorkflowID)
	if wf == "" {
		return nil, fmt.Errorf("workflow_id 必填（用回执头里的 workflow_id）")
	}
	switch action := strings.ToLower(strings.TrimSpace(a.Action)); action {
	case "", "get":
		return s.sessionParamsGet(ctx, wf, a)
	case "set":
		return s.sessionParamsSet(ctx, wf, a)
	default:
		return nil, fmt.Errorf("未知 action：%s（可用 get / set）", action)
	}
}

// noBranchRelation 是没有任何分支关系的工作流得到的说明（不报错）。
func noBranchRelation() map[string]any {
	return map[string]any{
		"ok":   false,
		"role": "none",
		"note": "这个工作流没有分支关系：session_params 只用于读写自己分支的会话参数。branch(action: \"create\", title: ...) 可以把一块工作拆成一个分支会话。",
	}
}

// resolveParamsTarget 决定操作哪个分支。none 表示调用者没有任何活动的分支关系（返回说明，不报错）。
// 分支一侧不带 target：get 读自己的；set 也解析成自己，由 store 给出「分支不能改自己的参数」的明确拒绝。
func (s *Server) resolveParamsTarget(ctx context.Context, wf, target string) (branch string, none bool, err error) {
	links, err := s.store.ActiveBranchLinks(ctx, wf)
	if err != nil {
		return "", false, err
	}
	if links.AsBranch == nil && len(links.AsMain) == 0 {
		return "", true, nil
	}
	if target = strings.TrimSpace(target); target != "" {
		return target, false, nil
	}
	switch {
	case links.AsBranch != nil:
		return wf, false, nil
	case len(links.AsMain) == 1:
		return links.AsMain[0].BranchWorkflowID, false, nil
	}
	names := make([]string, 0, len(links.AsMain))
	for _, b := range links.AsMain {
		names = append(names, s.branchWorkflowLabel(ctx, b.BranchWorkflowID))
	}
	return "", false, store.NewInvalidInputError(fmt.Sprintf("你有 %d 个活动分支，请用 target 指定是哪一个：%s", len(links.AsMain), strings.Join(names, "、")))
}

// paramsLevelMeaning 是「改参数」各档的含义，写给智能体看。
func paramsLevelMeaning(level string) string {
	switch level {
	case model.ExposeParamsResident:
		return "可改：常驻规则（resident）。"
	case model.ExposeParamsResidentPhase:
		return "可改：常驻规则（resident）与阶段（phase）。"
	case model.ExposeParamsAll:
		return "可改：常驻规则（resident）、阶段（phase）、续聊唤醒词（wake_resume）、新开对话唤醒词（wake_new）、标题（title）、异步开关（async，只能关，或恢复用户开启过的）。"
	}
	return "没有向主会话开放「改参数」：主会话不能改这个分支的任何参数。"
}

const paramsNeverOpen = "授权档位、凭据、目录与主机、终端访问、开放范围本身永不开放，也不能替分支第一次开启异步对话。"

func paramEventView(ev model.BranchParamEvent) map[string]any {
	by := "user"
	if ev.ByAgent() {
		by = "main"
	}
	out := map[string]any{
		"id": ev.ID, "at": ev.CreatedAt.Format(time.RFC3339), "field": ev.Field, "by": by, "summary": ev.Summary,
	}
	if ev.RevertedAt != nil {
		out["reverted"] = true
	}
	if ev.IsRevert() {
		out["revert_of"] = ev.RevertOf
	}
	return out
}

func (s *Server) sessionParamsGet(ctx context.Context, wf string, a sessionParamsArgs) (any, error) {
	branch, none, err := s.resolveParamsTarget(ctx, wf, a.Target)
	if err != nil {
		return nil, err
	}
	if none {
		return noBranchRelation(), nil
	}
	view, err := s.store.GetSessionParams(ctx, wf, branch, time.Now())
	if err != nil {
		return nil, err
	}
	out := map[string]any{
		"role":          view.Role,
		"branch":        view.Branch,
		"branch_title":  view.Title,
		"level":         view.Level,
		"level_meaning": paramsLevelMeaning(view.Level),
		"editable":      view.Editable,
	}
	if view.Role == store.BranchRoleBranch {
		out["main"] = map[string]any{"workflow_id": view.Parent, "title": s.store.WorkflowTitleOf(ctx, view.Parent)}
		out["note"] = "这是你自己的会话参数。只有用户，以及用户开放了「改参数」的主会话能改它们，你不能改自己的参数；主会话的修改会在你下一次回执的 params: 行里告知。" + paramsNeverOpen
	} else if view.Values == nil {
		out["note"] = fmt.Sprintf("分支 %s 没有向主会话开放「改参数」，所以这里看不到它的参数值，也改不了；需要用户在 Web 里该分支的会话设置里打开。", view.Branch)
	} else {
		out["how_to_set"] = "session_params(action: \"set\", target, patch: {…})；只能改 editable 里的字段，整条要么全写要么都不写。" + paramsNeverOpen
	}
	if view.Values != nil {
		writer := s.store.ResidentWriter(ctx, view.Branch)
		by := ""
		switch {
		case view.Values.Resident.Rev == 0:
		case strings.HasPrefix(writer, "agent:"):
			by = "main"
		default:
			by = "user"
		}
		out["values"] = map[string]any{
			"resident": map[string]any{
				"text": clipResident(view.Values.Resident.Text), "rev": view.Values.Resident.Rev, "set": view.Values.Resident.Text != "", "written_by": by,
			},
			"phase":       view.Values.Phase,
			"wake_resume": view.Values.WakeResume,
			"wake_new":    view.Values.WakeNew,
			"title":       view.Values.Title,
			"async": map[string]any{
				"enabled": view.Values.Async.Enabled, "ever_enabled": view.Values.Async.EverEnabled,
				"hook_seen": view.Values.Async.HookSeen, "effective": view.Values.Async.Effective,
			},
		}
		if evs, err := s.store.ListBranchParamEvents(ctx, view.Branch, 5); err == nil && len(evs) > 0 {
			rows := make([]map[string]any, 0, len(evs))
			for _, ev := range evs {
				rows = append(rows, paramEventView(ev))
			}
			out["recent_changes"] = rows
		}
	}
	return out, nil
}

func (s *Server) sessionParamsSet(ctx context.Context, wf string, a sessionParamsArgs) (any, error) {
	branch, none, err := s.resolveParamsTarget(ctx, wf, a.Target)
	if err != nil {
		return nil, err
	}
	if none {
		return noBranchRelation(), nil
	}
	patch, err := store.ParseSessionParamsPatch(a.Patch)
	if err != nil {
		return nil, err
	}
	res, err := s.store.ApplySessionParams(ctx, wf, branch, patch, time.Now())
	if res != nil && len(res.Applied) > 0 {
		s.afterParamsApplied(ctx, res)
	}
	if err != nil {
		var ae *store.AppError
		if res != nil && len(res.Applied) > 0 && !errors.As(err, &ae) {
			// 半途出错（数据库）：已经写入的部分不能当作没发生，连同错误一起如实交回。
			out := sessionParamsResult(res)
			out["ok"] = false
			out["error"] = err.Error()
			return out, nil
		}
		return nil, err
	}
	return sessionParamsResult(res), nil
}

// afterParamsApplied 在有字段写入之后通知界面：branch_changed(params) 让会话页的事件行、分支面板与侧栏刷新；
// 字段本身在网页上的现有展示（常驻规则 / 唤醒词弹窗、阶段滑块、标题、异步开关）也各发它们原本的刷新事件（见 store.ParamEffects）。
func (s *Server) afterParamsApplied(ctx context.Context, res *store.ApplyParamsResult) {
	fields := make([]string, 0, len(res.Applied))
	ids := make([]uint, 0, len(res.Applied))
	for _, ev := range res.Applied {
		fields = append(fields, ev.Field)
		ids = append(ids, ev.ID)
	}
	s.emitBranchEvent(branchChangedEvent, map[string]any{
		"parent": res.Parent, "branch": res.Branch, "change": "params", "fields": fields, "events": ids,
	})
	for _, ef := range s.store.ParamEffects(ctx, res.Branch, res.Applied) {
		s.emitBranchEvent(ef.Event, ef.Payload)
	}
}

func sessionParamsResult(res *store.ApplyParamsResult) map[string]any {
	applied := make([]map[string]any, 0, len(res.Applied))
	for _, ev := range res.Applied {
		row := map[string]any{"field": ev.Field, "summary": ev.Summary, "event_id": ev.ID}
		if ev.Field == model.ParamFieldResident {
			row["rev"] = ev.Rev
		}
		applied = append(applied, row)
	}
	out := map[string]any{"ok": true, "branch": res.Branch, "level": res.Level, "applied": applied}
	if len(res.Unchanged) > 0 {
		out["unchanged"] = res.Unchanged
	}
	switch {
	case len(res.Applied) == 0:
		out["note"] = "没有任何字段需要改：这些字段现在的取值与你给的一致。"
	default:
		out["note"] = "已写入。分支会在下一次回执的 params: 行里被告知（并标明是主会话代用户改的，不是用户本人的指示，它的 auth: 授权不会因此放宽）；用户能在 Web 里看到每一项并撤销。建议用 branch(action: \"say\") 告诉它为什么改。"
	}
	return out
}

// ---- 回执头里的 params: 行 ----

// branchParamsShow 是 params: 行里最多列出的条数；更早的写成「另有 N 项」。
const branchParamsShow = 5

// formatBranchParamsLine 把还没告诉分支的参数事件（旧的在前）写成回执头里的一行；没有事件返回空串。
// 只列最近 branchParamsShow 条；声明「主会话的修改是代用户改的、不是用户本人的指示、不放宽 auth:」。
// 事件摘要不含换行（标题在写入时已限定一行），所以不会冒充回执头里别的 key。
func formatBranchParamsLine(events []model.BranchParamEvent) string {
	if len(events) == 0 {
		return ""
	}
	shown := events
	hidden := 0
	if len(shown) > branchParamsShow {
		hidden = len(shown) - branchParamsShow
		shown = shown[hidden:]
	}
	parts := make([]string, 0, len(shown))
	for _, ev := range shown {
		if ev.ByAgent() {
			parts = append(parts, "主会话："+ev.Summary)
		} else {
			parts = append(parts, ev.Summary)
		}
	}
	line := "params: 你的会话参数有变化——" + strings.Join(parts, "；")
	if hidden > 0 {
		line += fmt.Sprintf("（另有 %d 项更早的变化，session_params(action: \"get\") 的 recent_changes 里能看到）", hidden)
	}
	line += "。"
	for _, ev := range events {
		if ev.ByAgent() {
			line += "主会话的修改是它经用户开放的「改参数」代用户改的，不是用户本人的指示；你的 auth: 授权不会因此放宽；用户可在 Web 里撤销。"
			break
		}
	}
	return line
}

// branchParamsLine 返回分支回执头里 branch: 行后面的 params: 行，并把这些事件记为已告知（一条变化只告知一次）。
// 没有未告知的事件返回空串，回执一个字都不多。读库失败一律降级为不带（不报错、不阻断回执）。
func (s *Server) branchParamsLine(ctx context.Context, b *model.WorkflowBranch) string {
	events, err := s.store.UntoldBranchParamEvents(ctx, b.BranchWorkflowID)
	if err != nil || len(events) == 0 {
		return ""
	}
	line := formatBranchParamsLine(events)
	ids := make([]uint, 0, len(events))
	for _, ev := range events {
		ids = append(ids, ev.ID)
	}
	if err := s.store.MarkBranchParamEventsTold(ctx, ids, time.Now()); err == nil {
		s.emitBranchEvent(branchChangedEvent, map[string]any{"parent": b.ParentWorkflowID, "branch": b.BranchWorkflowID, "change": "params_told"})
	}
	return line
}
