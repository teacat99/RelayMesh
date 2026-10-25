package store

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/teacat99/RelayMesh/v2/internal/model"
	"github.com/teacat99/RelayMesh/v2/internal/sessionprompt"
)

// 主会话经 session_params 改分支的「会话参数」（#236 P3）。
//
// 权限只在这里判定：调用者是谁取自 MCP 会话本身（工作流编号来自回执头），不是请求里自己填的东西；
// 判定依据是调用者与目标之间的「活动关系」和那条关系上的开放范围（params 四档），每次现查现判，
// 用户把档位调回去之后主会话手里的旧结果立刻失效。
//
// 写入顺序执行：底层各字段（常驻规则 / 唤醒词 / 阶段 / 标题 / 异步）本来就是各自的事务，而 WithTx 的锁不可重入，
// 没法包成一个大事务，所以先把全部校验过完（权限、键、取值、async 的特殊规则、限频），任何一项不过整条拒绝、一个字段也不写；
// 全部通过后依次写，中途数据库出错时返回已写入的部分，每个已写入的字段都有自己的事件与审计。

// 分支会话参数相关的审计动作（只记字段与摘要，不记常驻规则 / 唤醒词的全文）。
const (
	AuditBranchParamsSet    = "branch.params_set"
	AuditBranchParamsRevert = "branch.params_revert"
)

// SessionParamsPatch 是主会话要改的参数：只改非 nil 的字段。
type SessionParamsPatch struct {
	Resident   *string // 常驻规则；空串 = 清除
	Phase      *string // discuss / plan / dev（旧编号会换算）
	WakeResume *string // 续聊唤醒词；空串 = 清除，回到全局 / 内置
	WakeNew    *string // 新开对话唤醒词；非空必须带 %workflow
	Title      *string // 标题（一行）
	Async      *bool   // 异步对话开关：只能关，或恢复用户开启过的
}

// Fields 返回这次要改的字段，顺序固定（与 model.BranchParamFields 一致）。
func (p SessionParamsPatch) Fields() []string {
	var out []string
	if p.Resident != nil {
		out = append(out, model.ParamFieldResident)
	}
	if p.Phase != nil {
		out = append(out, model.ParamFieldPhase)
	}
	if p.WakeResume != nil {
		out = append(out, model.ParamFieldWakeResume)
	}
	if p.WakeNew != nil {
		out = append(out, model.ParamFieldWakeNew)
	}
	if p.Title != nil {
		out = append(out, model.ParamFieldTitle)
	}
	if p.Async != nil {
		out = append(out, model.ParamFieldAsync)
	}
	return out
}

// neverOpenParamKeys 是永不对主会话开放的键（规整后的写法）：授权档位、凭据、目录与主机、终端、开放范围与启动本身。
// 它们被点名拒绝，而不是当成「不认识的键」，好让智能体明白这是边界而不是拼写错误。
var neverOpenParamKeys = map[string]bool{
	"profile": true, "profile_id": true, "auth": true, "auth_profile": true, "authorization": true, "authorization_profile": true,
	"credential": true, "credentials": true, "token": true, "secret": true, "password": true,
	"directory": true, "project_directory": true, "dir": true, "cwd": true,
	"host": true, "host_id": true, "host_name": true,
	"terminal": true, "terminal_exec": true,
	"launch": true, "expose": true, "expose_preset": true, "params": true,
	"parent": true, "parent_workflow_id": true, "workflow_id": true,
	"strict_wrapup": true,
}

func normParamKey(k string) string {
	return strings.NewReplacer("-", "_", " ", "_").Replace(strings.ToLower(strings.TrimSpace(k)))
}

// ParseSessionParamsPatch 把 MCP 传来的 patch 对象翻成 SessionParamsPatch：只做键与类型层面的检查，
// 取值规则（字数、阶段枚举、%workflow…）与权限在 ApplySessionParams 里统一判定。
func ParseSessionParamsPatch(raw map[string]any) (SessionParamsPatch, error) {
	var p SessionParamsPatch
	if len(raw) == 0 {
		return p, NewInvalidInputError("patch 不能为空：至少给一个要改的字段（" + strings.Join(model.BranchParamFields, "、") + "）")
	}
	keys := make([]string, 0, len(raw))
	for k := range raw {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		nk := normParamKey(k)
		if neverOpenParamKeys[nk] {
			return SessionParamsPatch{}, NewForbiddenError(fmt.Sprintf("%q 永不对主会话开放：授权档位、凭据、目录与主机、终端、开放范围与启动都只有用户本人能改", k))
		}
		v := raw[k]
		switch nk {
		case model.ParamFieldResident, model.ParamFieldPhase, model.ParamFieldWakeResume, model.ParamFieldWakeNew, model.ParamFieldTitle:
			s, ok := v.(string)
			if !ok {
				return SessionParamsPatch{}, NewInvalidInputError(fmt.Sprintf("%s 必须是字符串（想清除就给空串）", k))
			}
			switch nk {
			case model.ParamFieldResident:
				p.Resident = &s
			case model.ParamFieldPhase:
				p.Phase = &s
			case model.ParamFieldWakeResume:
				p.WakeResume = &s
			case model.ParamFieldWakeNew:
				p.WakeNew = &s
			case model.ParamFieldTitle:
				p.Title = &s
			}
		case model.ParamFieldAsync:
			b, ok := parseParamBool(v)
			if !ok {
				return SessionParamsPatch{}, NewInvalidInputError(fmt.Sprintf("%s 必须是 true / false（或 on / off）", k))
			}
			p.Async = &b
		default:
			return SessionParamsPatch{}, NewInvalidInputError(fmt.Sprintf(
				"不认识的参数 %q；session_params 只认：%s（等待参数落在每一轮汇报上，不是会话参数）", k, strings.Join(model.BranchParamFields, "、")))
		}
	}
	return p, nil
}

func parseParamBool(v any) (bool, bool) {
	switch x := v.(type) {
	case bool:
		return x, true
	case string:
		switch strings.ToLower(strings.TrimSpace(x)) {
		case "true", "on":
			return true, true
		case "false", "off":
			return false, true
		}
	}
	return false, false
}

// ---- 权限 ----

// paramsAccess 是调用者对目标分支的参数权限判定结果。
type paramsAccess struct {
	Rel   model.WorkflowBranch
	Role  string // BranchRoleMain / BranchRoleBranch
	Level string // 关系上「改参数」的档位
}

// resolveParamsAccess 判定 caller 与 target 的关系：target 必须有一条仍然生效的分支关系，caller 是它的主会话或它自己。
// 其他一律拒绝；关系已结束 / 被解除，主会话不能再读写（历史事件保留）。
func (s *Store) resolveParamsAccess(ctx context.Context, caller, target string) (*paramsAccess, error) {
	caller, target = strings.TrimSpace(caller), strings.TrimSpace(target)
	if target == "" {
		return nil, NewInvalidInputError("缺少要操作的分支（target）")
	}
	rel, err := s.BranchOf(ctx, target)
	if err != nil {
		return nil, err
	}
	if rel == nil {
		return nil, NewForbiddenError(fmt.Sprintf("%q 不是任何主会话的分支，不能读写它的参数", target))
	}
	role := ""
	switch {
	case caller != "" && caller == target:
		role = BranchRoleBranch
	case caller != "" && caller == rel.ParentWorkflowID:
		role = BranchRoleMain
	default:
		return nil, NewForbiddenError(fmt.Sprintf("%q 不是你的分支，不能读写它的参数", target))
	}
	if !rel.Active() {
		return nil, NewForbiddenError(fmt.Sprintf("分支 %q 已经结束（关系已关闭或解除），不能再读写它的参数", target))
	}
	return &paramsAccess{Rel: *rel, Role: role, Level: rel.Expose.Params}, nil
}

// ---- 读 ----

// SessionParamValues 是分支各个会话参数的当前值（不含授权档位、凭据、目录与主机——那些永不经这条路径读写）。
type SessionParamValues struct {
	Resident   ResidentParam `json:"resident"`
	Phase      string        `json:"phase"`
	WakeResume string        `json:"wake_resume"` // 会话自己的模板；空串 = 没设，用全局 / 内置默认
	WakeNew    string        `json:"wake_new"`
	Title      string        `json:"title"`
	Async      AsyncParam    `json:"async"`
}

// ResidentParam 是常驻规则的当前值与来源。
type ResidentParam struct {
	Text      string     `json:"text"`
	Rev       int        `json:"rev"`
	UpdatedBy string     `json:"updated_by,omitempty"` // 最近一次保存常驻规则的操作者（用户登录名或 agent:<主会话>）
	UpdatedAt *time.Time `json:"updated_at,omitempty"`
}

// AsyncParam 是异步对话开关的当前状态（不含授权档位）。
type AsyncParam struct {
	Enabled     bool `json:"enabled"`      // 用户 / 主会话设的开关
	EverEnabled bool `json:"ever_enabled"` // 用户是否开启过（主会话只能恢复开启过的）
	HookSeen    bool `json:"hook_seen"`    // 是否见过这个分支的编辑器 hook（开启的前提）
	Effective   bool `json:"effective"`    // 此刻是不是有效的异步模式（hook 过期会降级）
}

// SessionParamsView 是 session_params(get) 的结果。
type SessionParamsView struct {
	Branch string `json:"branch"`
	Parent string `json:"parent"`
	Title  string `json:"title,omitempty"`
	Role   string `json:"role"`  // main | branch
	Level  string `json:"level"` // 「改参数」档位：off / resident / resident_phase / all
	// Editable 是调用者现在能改的字段；分支本人与 off 档恒为空。
	Editable []string `json:"editable"`
	// Values 在主会话读 off 档的分支时为空（常驻规则全文可能含用户写给分支的私货）；分支读自己的参数恒有。
	Values *SessionParamValues `json:"values,omitempty"`
}

// paramCurrent 是一次性读出的各字段当前值。
type paramCurrent struct {
	prompt   model.WorkflowPrompt
	phase    string
	title    string
	mode     ModeInfo
	hookSeen bool
}

func (s *Store) readParamCurrent(ctx context.Context, branch string, now time.Time) (paramCurrent, error) {
	var cur paramCurrent
	var err error
	if cur.prompt, err = s.GetWorkflowPrompt(ctx, branch); err != nil {
		return cur, err
	}
	if cur.phase, _, err = s.GetWorkflowPhaseWithDefaults(ctx, branch); err != nil {
		return cur, err
	}
	cur.title = s.WorkflowTitleOf(ctx, branch)
	if cur.mode, err = s.WorkflowModeInfo(ctx, branch, now); err != nil {
		return cur, err
	}
	_, cur.hookSeen = s.HookLastSeen(branch)
	return cur, nil
}

func (c paramCurrent) values() *SessionParamValues {
	v := &SessionParamValues{
		Resident:   ResidentParam{Text: c.prompt.Resident, Rev: c.prompt.ResidentRev, UpdatedBy: c.prompt.UpdatedBy},
		Phase:      c.phase,
		WakeResume: c.prompt.WakeResume,
		WakeNew:    c.prompt.WakeNew,
		Title:      c.title,
		Async: AsyncParam{
			Enabled: c.mode.Row.AsyncEnabled, EverEnabled: c.mode.Row.EverEnabled,
			HookSeen: c.hookSeen, Effective: c.mode.State.Effective,
		},
	}
	if !c.prompt.UpdatedAt.IsZero() {
		at := c.prompt.UpdatedAt
		v.Resident.UpdatedAt = &at
	}
	return v
}

// current 返回某个字段的当前值（与事件里的 old_value / new_value 同一口径，撤销时拿来比对）。
func (c paramCurrent) current(field string) string {
	switch field {
	case model.ParamFieldResident:
		return c.prompt.Resident
	case model.ParamFieldWakeResume:
		return c.prompt.WakeResume
	case model.ParamFieldWakeNew:
		return c.prompt.WakeNew
	case model.ParamFieldPhase:
		return c.phase
	case model.ParamFieldTitle:
		return c.title
	case model.ParamFieldAsync:
		return onOff(c.mode.Row.AsyncEnabled)
	}
	return ""
}

func onOff(b bool) string {
	if b {
		return "on"
	}
	return "off"
}

// GetSessionParams 是 session_params(get)：主会话读自己某个分支的参数，分支读自己的。
// 主会话读 off 档的分支只拿到开放范围与说明，不返回参数值。
func (s *Store) GetSessionParams(ctx context.Context, caller, target string, now time.Time) (*SessionParamsView, error) {
	acc, err := s.resolveParamsAccess(ctx, caller, target)
	if err != nil {
		return nil, err
	}
	branch := acc.Rel.BranchWorkflowID
	view := &SessionParamsView{
		Branch: branch, Parent: acc.Rel.ParentWorkflowID, Title: s.WorkflowTitleOf(ctx, branch),
		Role: acc.Role, Level: acc.Level, Editable: []string{},
	}
	if acc.Role == BranchRoleMain {
		if f := model.ParamFieldsEditable(acc.Level); len(f) > 0 {
			view.Editable = f
		}
		if model.ExposeParamsRank(acc.Level) == 0 {
			return view, nil
		}
	}
	cur, err := s.readParamCurrent(ctx, branch, now)
	if err != nil {
		return nil, err
	}
	view.Values = cur.values()
	return view, nil
}

// ResidentWriter 返回某个会话「当前这一版常驻规则」的写入者（最近一条常驻规则历史版本的操作者）；没有历史返回空串。
// 回执头据此判断来源：agent:<主会话> = 主会话代写，其它 = 用户本人。
// 不用 workflow_prompts.updated_by：它在保存唤醒词时也会被覆盖，会把「用户写的常驻规则」误标成别人写的。
func (s *Store) ResidentWriter(ctx context.Context, workflowID string) string {
	var actors []string
	if err := s.db.WithContext(ctx).Model(&model.WorkflowPromptRevision{}).
		Where("workflow_id = ? AND field = ?", strings.TrimSpace(workflowID), model.PromptFieldResident).
		Order("id DESC").Limit(1).Pluck("actor", &actors).Error; err != nil || len(actors) == 0 {
		return ""
	}
	return actors[0]
}

// ---- 写 ----

// ApplyParamsResult 是 session_params(set) 的结果。出错时也会返回已经写入的部分（Applied）。
type ApplyParamsResult struct {
	Branch    string                   `json:"branch"`
	Parent    string                   `json:"parent"`
	Level     string                   `json:"level"`
	Applied   []model.BranchParamEvent `json:"applied"`
	Unchanged []string                 `json:"unchanged,omitempty"` // 与现有取值一致、没写的字段
}

type paramPlan struct {
	field   string
	oldVal  string
	newVal  string
	changed bool
}

// invalidWithField 给取值校验的报错前面加上字段名，让主会话知道是哪一项不过。
func invalidWithField(field string, err error) error {
	var ae *AppError
	if errors.As(err, &ae) && ae.Kind == ErrInvalidInput {
		return NewInvalidInputError(field + "：" + ae.Message)
	}
	return err
}

// planSessionParams 校验取值并算出每个字段的旧值 / 新值 / 是否有变化。任何一项不过都返回错误，此时还没有写过任何东西。
func (s *Store) planSessionParams(ctx context.Context, branch string, patch SessionParamsPatch, cur paramCurrent) ([]paramPlan, error) {
	var plans []paramPlan
	for _, f := range patch.Fields() {
		pl := paramPlan{field: f, oldVal: cur.current(f)}
		switch f {
		case model.ParamFieldResident:
			v, err := NormalizePromptText(*patch.Resident, model.ResidentPromptMax)
			if err != nil {
				return nil, invalidWithField(f, err)
			}
			pl.newVal = v
		case model.ParamFieldWakeResume:
			v, err := NormalizeWakeTemplate(sessionprompt.KindResume, *patch.WakeResume)
			if err != nil {
				return nil, invalidWithField(f, err)
			}
			pl.newVal = v
		case model.ParamFieldWakeNew:
			v, err := NormalizeWakeTemplate(sessionprompt.KindNew, *patch.WakeNew)
			if err != nil {
				return nil, invalidWithField(f, err)
			}
			pl.newVal = v
		case model.ParamFieldPhase:
			v, err := NormalizeWebPhase(*patch.Phase)
			if err != nil || v == "" {
				return nil, NewInvalidInputError("phase：只能是 discuss / plan / dev")
			}
			pl.newVal = v
		case model.ParamFieldTitle:
			v, err := NormalizePromptText(*patch.Title, model.BranchParamTitleMax)
			if err != nil {
				return nil, invalidWithField(f, err)
			}
			v = strings.TrimSpace(strings.ReplaceAll(v, "\t", " "))
			if v == "" {
				return nil, NewInvalidInputError("title：不能为空")
			}
			if strings.Contains(v, "\n") {
				return nil, NewInvalidInputError("title：只能是一行")
			}
			pl.newVal = v
		case model.ParamFieldAsync:
			pl.newVal = onOff(*patch.Async)
			if pl.newVal == "on" && pl.oldVal == "off" {
				// 第一次开启（等于启用授权档位的行为）只有用户本人能做；主会话只能恢复用户开启过的，且仍受「见过 hook」限制。
				if !cur.mode.Row.EverEnabled {
					return nil, NewForbiddenError("async：这个分支的异步对话从没被用户开启过。第一次开启（等同启用授权档位的行为）只有用户本人能做；主会话只能关闭它，或恢复用户开启过的")
				}
				if !cur.hookSeen {
					return nil, NewConflictError("async：还没有收到这个分支的编辑器 hook 事件，不能开启异步对话（先让用户在编辑器里装好 hook，并让分支调用一次工具）", 0)
				}
			}
		}
		pl.changed = pl.newVal != pl.oldVal
		plans = append(plans, pl)
	}
	return plans, nil
}

// ApplySessionParams 是 session_params(set)：校验—写—事件—审计。caller 必须是 target 的主会话，关系仍然生效，
// 且开放范围的「改参数」档位够改每一个要改的字段。先校验后写：任何一项不过，整条拒绝、一个字段也不写。
func (s *Store) ApplySessionParams(ctx context.Context, caller, target string, patch SessionParamsPatch, now time.Time) (*ApplyParamsResult, error) {
	s.paramsMu.Lock()
	defer s.paramsMu.Unlock()

	acc, err := s.resolveParamsAccess(ctx, caller, target)
	if err != nil {
		return nil, err
	}
	branch, parent, level := acc.Rel.BranchWorkflowID, acc.Rel.ParentWorkflowID, acc.Level
	if acc.Role != BranchRoleMain {
		return nil, NewForbiddenError("分支不能改自己的参数：只有用户，以及用户开放了「改参数」的主会话能改")
	}
	fields := patch.Fields()
	if len(fields) == 0 {
		return nil, NewInvalidInputError("patch 里没有任何要改的字段；可改的有：" + strings.Join(model.BranchParamFields, "、"))
	}
	if model.ExposeParamsRank(level) == 0 {
		return nil, NewForbiddenError(fmt.Sprintf("分支 %q 没有向主会话开放「改参数」；需要用户在该分支的会话设置里打开", branch))
	}
	var denied []string
	for _, f := range fields {
		if !model.ParamFieldAllowed(level, f) {
			denied = append(denied, fmt.Sprintf("%s（至少需要 %s 档）", f, model.ParamFieldMinLevel(f)))
		}
	}
	if len(denied) > 0 {
		return nil, NewForbiddenError(fmt.Sprintf("这个分支「改参数」现在开放到 %s 档，改不了：%s。整条没有写入；需要用户把开放范围调高", level, strings.Join(denied, "、")))
	}

	cur, err := s.readParamCurrent(ctx, branch, now)
	if err != nil {
		return nil, err
	}
	plans, err := s.planSessionParams(ctx, branch, patch, cur)
	if err != nil {
		return nil, err
	}
	changed := 0
	for _, p := range plans {
		if p.changed {
			changed++
		}
	}
	if changed > 0 {
		var n int64
		if err := s.db.WithContext(ctx).Model(&model.BranchParamEvent{}).
			Where("branch_workflow_id = ? AND actor LIKE ? AND revert_of = 0 AND created_at >= ?", branch, "agent:%", now.Add(-time.Hour)).
			Count(&n).Error; err != nil {
			return nil, err
		}
		if int(n)+changed > model.BranchParamRateLimit {
			return nil, NewConflictError(fmt.Sprintf("这个分支一小时内已经接受了 %d 次主会话的参数写入（上限 %d），这次还要写 %d 项。请合并成更少的修改，或等一会儿再改；读取不受影响", n, model.BranchParamRateLimit, changed), 0)
		}
	}

	actor := "agent:" + strings.TrimSpace(caller)
	res := &ApplyParamsResult{Branch: branch, Parent: parent, Level: level, Applied: []model.BranchParamEvent{}}
	for _, pl := range plans {
		if !pl.changed {
			res.Unchanged = append(res.Unchanged, pl.field)
			continue
		}
		rev, wrote, err := s.writeParamField(ctx, branch, pl.field, pl.newVal, actor, now)
		if err != nil {
			return res, paramWriteErr(pl.field, len(res.Applied), err)
		}
		if !wrote { // 校验之后、写入之前被别处改成了同一个值：当作没变
			res.Unchanged = append(res.Unchanged, pl.field)
			continue
		}
		ev := model.BranchParamEvent{
			CreatedAt: now, ParentWorkflowID: parent, BranchWorkflowID: branch, Field: pl.field, By: actor,
			Summary:  truncRunes(paramEventSummary(pl.field, pl.oldVal, pl.newVal, rev), model.BranchParamSummaryMax),
			OldValue: pl.oldVal, NewValue: pl.newVal, Rev: rev,
		}
		if err := s.recordParamEvent(ctx, &ev); err != nil {
			return res, fmt.Errorf("「%s」已经写入，但记录事件失败（本次此前已写入 %d 项，见返回的 applied）：%w", model.ParamFieldLabel(pl.field), len(res.Applied), err)
		}
		res.Applied = append(res.Applied, ev)
		s.auditBranch(ctx, AuditBranchParamsSet, actor, parent, branch,
			map[string]any{"field": pl.field, "event_id": ev.ID, "summary": ev.Summary, "rev": rev})
	}
	return res, nil
}

// paramWriteErr 给「写某个字段时出错」补上上下文：业务错误（冲突 / 不合法…）原样返回，MCP 层会把它翻成人话；
// 其它（数据库）错误说明此前已经写了几项，调用方据此知道是半途而废。
func paramWriteErr(field string, applied int, err error) error {
	var ae *AppError
	if errors.As(err, &ae) {
		return err
	}
	return fmt.Errorf("写入「%s」时出错（本次此前已写入 %d 项，见返回的 applied）：%w", model.ParamFieldLabel(field), applied, err)
}

// writeParamField 把一个字段写成新值。返回常驻规则写入后的版本号（其它字段为 0）与是否真的写了。
func (s *Store) writeParamField(ctx context.Context, branch, field, val, actor string, now time.Time) (rev int, wrote bool, err error) {
	switch field {
	case model.ParamFieldResident:
		row, ch, err := s.SaveResidentPrompt(ctx, branch, val, actor, now)
		return row.ResidentRev, ch, err
	case model.ParamFieldWakeResume:
		_, ch, err := s.SaveWakeTemplate(ctx, branch, sessionprompt.KindResume, val, actor, now)
		return 0, ch, err
	case model.ParamFieldWakeNew:
		_, ch, err := s.SaveWakeTemplate(ctx, branch, sessionprompt.KindNew, val, actor, now)
		return 0, ch, err
	case model.ParamFieldPhase:
		// 不当成用户点击：不改「用户偏好的阶段」。
		return 0, true, s.SetWorkflowPhase(ctx, branch, val, false)
	case model.ParamFieldTitle:
		_, err := s.RenameFeedbackSession(ctx, branch, val)
		return 0, true, err
	case model.ParamFieldAsync:
		on := val == "on"
		_, err := s.UpdateWorkflowMode(ctx, branch, ModePatch{Enabled: &on}, now)
		return 0, true, err
	}
	return 0, false, NewInvalidInputError(fmt.Sprintf("%q 不是可写的会话参数", field))
}

func phaseLabelOf(id string) string {
	switch id {
	case model.PhaseDiscuss:
		return "讨论"
	case model.PhasePlan:
		return "方案"
	case model.PhaseDev:
		return "开发"
	}
	return id
}

// paramEventSummary 是一句人话，不含常驻规则 / 唤醒词全文。
func paramEventSummary(field, oldVal, newVal string, rev int) string {
	label := model.ParamFieldLabel(field)
	switch field {
	case model.ParamFieldResident:
		if newVal == "" {
			return fmt.Sprintf("%s已清除（rev %d）", label, rev)
		}
		return fmt.Sprintf("%s更新为 rev %d（%d 字）", label, rev, len([]rune(newVal)))
	case model.ParamFieldWakeResume, model.ParamFieldWakeNew:
		if newVal == "" {
			return label + "已清除（回到全局 / 内置默认）"
		}
		return fmt.Sprintf("%s已更新（%d 字）", label, len([]rune(newVal)))
	case model.ParamFieldPhase:
		return fmt.Sprintf("%s %s → %s", label, phaseLabelOf(oldVal), phaseLabelOf(newVal))
	case model.ParamFieldTitle:
		return fmt.Sprintf("%s「%s」→「%s」", label, ellipsisClip(oldVal, 30), ellipsisClip(newVal, 30))
	case model.ParamFieldAsync:
		if newVal == "on" {
			return label + "已恢复开启"
		}
		return label + "已关闭"
	}
	return label
}

// recordParamEvent 写一条事件，并把这个分支的事件裁到最近 BranchParamEventKeep 条。
func (s *Store) recordParamEvent(ctx context.Context, ev *model.BranchParamEvent) error {
	db := s.db.WithContext(ctx)
	if err := db.Create(ev).Error; err != nil {
		return err
	}
	var keep []uint
	if err := db.Model(&model.BranchParamEvent{}).Where("branch_workflow_id = ?", ev.BranchWorkflowID).
		Order("id DESC").Limit(model.BranchParamEventKeep).Pluck("id", &keep).Error; err != nil {
		return err
	}
	if len(keep) < model.BranchParamEventKeep {
		return nil
	}
	return db.Where("branch_workflow_id = ? AND id NOT IN ?", ev.BranchWorkflowID, keep).Delete(&model.BranchParamEvent{}).Error
}

// ---- 界面刷新 ----

// ParamEffect 是一次参数写入（或撤销）之后要广播给网页的一条事件。
type ParamEffect struct {
	Event   string
	Payload any // 事件 session_updated 时是 *model.FeedbackSession（调用方要走各自的载荷规整，不能原样广播）
}

// ParamEffects 给出一次参数写入 / 撤销之后要广播的现有事件：常驻规则与唤醒词弹窗、阶段滑块、会话标题、异步开关
// 本来就靠这些事件刷新，主会话（session_params）或用户撤销改了它们之后同样要发，否则开着那个分支页面的人要手动刷新才看得到。
// evs 里的 NewValue 是写入之后的值；按字段去重（常驻规则与两个唤醒词共用 workflow_prompts_updated），顺序固定。
// 读会话失败时跳过标题那一条（界面下次拉取也会更新），不影响其它事件。
func (s *Store) ParamEffects(ctx context.Context, branch string, evs []model.BranchParamEvent) []ParamEffect {
	branch = strings.TrimSpace(branch)
	byField := make(map[string]model.BranchParamEvent, len(evs))
	for _, ev := range evs {
		byField[ev.Field] = ev
	}
	var out []ParamEffect
	promptsDone := false
	for _, f := range model.BranchParamFields {
		ev, ok := byField[f]
		if !ok {
			continue
		}
		switch f {
		case model.ParamFieldResident, model.ParamFieldWakeResume, model.ParamFieldWakeNew:
			if !promptsDone {
				promptsDone = true
				out = append(out, ParamEffect{Event: "workflow_prompts_updated", Payload: map[string]string{"workflow_id": branch}})
			}
		case model.ParamFieldPhase:
			out = append(out, ParamEffect{Event: "phase_changed", Payload: map[string]string{"workflow_id": branch, "phase": ev.NewValue}})
		case model.ParamFieldTitle:
			if rows, err := s.GetSessionsByWorkflow(ctx, branch, 1); err == nil && len(rows) > 0 {
				sess := rows[0]
				out = append(out, ParamEffect{Event: "session_updated", Payload: &sess})
			}
		case model.ParamFieldAsync:
			out = append(out, ParamEffect{Event: "workflow_mode_updated", Payload: map[string]string{"workflow_id": branch}})
		}
	}
	return out
}

// ---- 事件 ----

// ListBranchParamEvents 返回某个分支的参数事件，新的在前；limit 默认 50、最多 BranchParamEventKeep。
func (s *Store) ListBranchParamEvents(ctx context.Context, branch string, limit int) ([]model.BranchParamEvent, error) {
	if limit <= 0 {
		limit = 50
	}
	if limit > model.BranchParamEventKeep {
		limit = model.BranchParamEventKeep
	}
	var rows []model.BranchParamEvent
	err := s.db.WithContext(ctx).Where("branch_workflow_id = ?", strings.TrimSpace(branch)).
		Order("id DESC").Limit(limit).Find(&rows).Error
	return rows, err
}

// GetBranchParamEvent 取某个分支的一条事件；不属于这个分支按不存在处理。
func (s *Store) GetBranchParamEvent(ctx context.Context, branch string, id uint) (*model.BranchParamEvent, error) {
	var ev model.BranchParamEvent
	err := s.db.WithContext(ctx).Where("id = ? AND branch_workflow_id = ?", id, strings.TrimSpace(branch)).First(&ev).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, NewNotFoundError("没有这条参数事件")
	}
	if err != nil {
		return nil, err
	}
	return &ev, nil
}

// UntoldBranchParamEvents 返回还没告诉分支的事件，旧的在前（回执的 params: 行据此生成，告知后用 MarkBranchParamEventsTold 标记）。
func (s *Store) UntoldBranchParamEvents(ctx context.Context, branch string) ([]model.BranchParamEvent, error) {
	var rows []model.BranchParamEvent
	err := s.db.WithContext(ctx).Where("branch_workflow_id = ? AND told_at IS NULL", strings.TrimSpace(branch)).
		Order("id ASC").Find(&rows).Error
	return rows, err
}

// MarkBranchParamEventsTold 记下「这些事件已经告诉了分支」；已标记过的不改。
func (s *Store) MarkBranchParamEventsTold(ctx context.Context, ids []uint, now time.Time) error {
	if len(ids) == 0 {
		return nil
	}
	return s.db.WithContext(ctx).Model(&model.BranchParamEvent{}).
		Where("id IN ? AND told_at IS NULL", ids).Update("told_at", now).Error
}

// ---- 撤销 ----

// BranchParamRevert 是一次撤销的结果。
type BranchParamRevert struct {
	Reverted model.BranchParamEvent `json:"reverted"` // 被撤销的原事件（已带 reverted_at）
	Event    model.BranchParamEvent `json:"event"`    // 撤销本身产生的新事件
}

// revertStaticVerdict 是「能不能撤销」的前一半：只看事件本身与同字段有没有更新的主会话写入，不读当前值。
// laterWrite 为真表示同一字段之后还有更新的、未被撤销的主会话写入。撤销本身（RevertBranchParamEvent）与界面列表上「撤销」
// 按钮的显隐（ListBranchParamEventViews）共用这一份规则，避免两边各写一份而漂移。
func revertStaticVerdict(ev model.BranchParamEvent, laterWrite bool) error {
	switch {
	case ev.IsRevert():
		return NewInvalidInputError("这是一条撤销记录，不能再撤销")
	case !ev.ByAgent():
		return NewInvalidInputError("只能撤销主会话写入的修改")
	case ev.RevertedAt != nil:
		return NewConflictError("这条修改已经撤销过了", 0)
	case laterWrite:
		return NewConflictError(fmt.Sprintf("主会话之后又改过「%s」，这条已经不是最新的修改，不能撤销", model.ParamFieldLabel(ev.Field)), 0)
	}
	return nil
}

// revertValueVerdict 是后一半：当前值必须仍是这次写入的值，并且旧值要能还原回去。
func revertValueVerdict(ev model.BranchParamEvent, cur paramCurrent) error {
	switch {
	case cur.current(ev.Field) != ev.NewValue:
		return NewConflictError(fmt.Sprintf("「%s」之后又被改过（当前值已经不是这次写入的值），不能撤销", model.ParamFieldLabel(ev.Field)), 0)
	case ev.Field == model.ParamFieldTitle && ev.OldValue == "":
		return NewInvalidInputError("这次修改之前分支没有标题，无法撤销成「无标题」")
	}
	return nil
}

// revertCandidate 判断一条事件是不是「值得问能不能撤销」的：主会话写入的、不是撤销记录、还没被撤销。
func revertCandidate(ev model.BranchParamEvent) bool {
	return !ev.IsRevert() && ev.ByAgent() && ev.RevertedAt == nil
}

// BranchParamEventView 是给界面的一条事件：事件本身，加上「现在能不能撤销」。
type BranchParamEventView struct {
	model.BranchParamEvent
	Revertable bool `json:"revertable"`
	// RevertBlocked 是主会话写入的、还没撤销的事件现在不能撤销的原因（之后又改过 / 当前值已变…），能撤销或不适用时为空。
	RevertBlocked string `json:"revert_blocked,omitempty"`
}

// appendParamEvents 把参数事件（主会话经「改参数」写入、用户撤销）作为事件行放进会话页的消息流。
// 主会话看得到它每个分支的，分支看得到自己的；每个分支最多取最近 limit 条。读失败的分支跳过，不影响消息流本身。
func (s *Store) appendParamEvents(ctx context.Context, briefs []BranchBrief, limit int, feed *BranchFeed) {
	now := time.Now()
	for _, b := range briefs {
		views, err := s.ListBranchParamEventViews(ctx, b.BranchWorkflowID, limit, now)
		if err != nil {
			continue
		}
		for _, v := range views {
			name := "params_changed"
			if v.IsRevert() {
				name = "params_reverted"
			}
			feed.Items = append(feed.Items, BranchFeedItem{
				Kind: "event", ID: v.ID, At: v.CreatedAt, Event: name, Branch: b.BranchWorkflowID, By: v.By,
				Actor: model.MsgActor{Kind: model.ActorKindSystem}, ActorTitle: b.Title,
				Text: v.Summary, Field: v.Field, Reverted: v.RevertedAt != nil, Revertable: v.Revertable, RevertOf: v.RevertOf,
			})
		}
	}
}

// ListBranchParamEventViews 返回某个分支最近的参数事件（新的在前），每条带「现在能不能撤销」。
// 只有存在可撤销候选时才读一次当前值；读不到当前值时跳过「当前值是否仍是它写入的值」这一条（宁可多显示一个按钮，
// 点下去再得到明确的冲突说明，也不因为读失败把按钮藏掉）。
func (s *Store) ListBranchParamEventViews(ctx context.Context, branch string, limit int, now time.Time) ([]BranchParamEventView, error) {
	evs, err := s.ListBranchParamEvents(ctx, branch, limit)
	if err != nil {
		return nil, err
	}
	views := make([]BranchParamEventView, len(evs))
	var cur *paramCurrent
	for i := range evs {
		views[i].BranchParamEvent = evs[i]
		if cur == nil && revertCandidate(evs[i]) {
			if c, err := s.readParamCurrent(ctx, strings.TrimSpace(branch), now); err == nil {
				cur = &c
			}
		}
	}
	for i := range evs {
		if !revertCandidate(evs[i]) {
			continue
		}
		later := false
		for j := 0; j < i && !later; j++ { // evs 新的在前：下标更小的都比它新
			later = evs[j].Field == evs[i].Field && evs[j].RevertOf == 0 && evs[j].RevertedAt == nil
		}
		err := revertStaticVerdict(evs[i], later)
		if err == nil && cur != nil {
			err = revertValueVerdict(evs[i], *cur)
		}
		if err == nil {
			views[i].Revertable = true
			continue
		}
		var ae *AppError
		if errors.As(err, &ae) {
			views[i].RevertBlocked = ae.Message
		}
	}
	return views, nil
}

// RevertBranchParamEvent 撤销主会话的一次参数写入（只有人能调，网页登录态；权限在 API 层）。
// 只在「这条事件是该字段最新一条未撤销的主会话写入」且「当前值仍等于它写入的值」时成功，否则返回冲突并写明原因；
// 撤销与关系状态无关（分支被结束后事件仍在，撤销仍可用）。常驻规则 / 唤醒词用 Save… 存成新版本（历史不改写）。
// 撤销本身记一条 by=human 的事件，下一次分支回执的 params: 行会告诉它。
func (s *Store) RevertBranchParamEvent(ctx context.Context, branch string, eventID uint, actor string, now time.Time) (*BranchParamRevert, error) {
	s.paramsMu.Lock()
	defer s.paramsMu.Unlock()

	branch = strings.TrimSpace(branch)
	actor = truncRunes(strings.TrimSpace(actor), 64)
	if actor == "" {
		actor = model.BranchParamByHuman
	}
	ev, err := s.GetBranchParamEvent(ctx, branch, eventID)
	if err != nil {
		return nil, err
	}
	// 能不能撤销的规则在 revertStaticVerdict / revertValueVerdict 里，列表上「撤销」按钮的显隐（ListBranchParamEventViews）共用它们。
	var later int64
	if !ev.IsRevert() && ev.ByAgent() && ev.RevertedAt == nil {
		if err := s.db.WithContext(ctx).Model(&model.BranchParamEvent{}).
			Where("branch_workflow_id = ? AND field = ? AND id > ? AND revert_of = 0 AND reverted_at IS NULL", branch, ev.Field, ev.ID).
			Count(&later).Error; err != nil {
			return nil, err
		}
	}
	if err := revertStaticVerdict(*ev, later > 0); err != nil {
		return nil, err
	}
	cur, err := s.readParamCurrent(ctx, branch, now)
	if err != nil {
		return nil, err
	}
	if err := revertValueVerdict(*ev, cur); err != nil {
		return nil, err
	}
	rev, _, err := s.writeParamField(ctx, branch, ev.Field, ev.OldValue, actor, now)
	if err != nil {
		return nil, err
	}
	out := &BranchParamRevert{}
	if err := s.db.WithContext(ctx).Model(&model.BranchParamEvent{}).Where("id = ?", ev.ID).
		Updates(map[string]any{"reverted_at": now, "reverted_by": actor}).Error; err != nil {
		return nil, fmt.Errorf("已把「%s」改回旧值，但标记原事件失败：%w", model.ParamFieldLabel(ev.Field), err)
	}
	ev.RevertedAt, ev.RevertedBy = &now, actor
	out.Reverted = *ev
	out.Event = model.BranchParamEvent{
		CreatedAt: now, ParentWorkflowID: ev.ParentWorkflowID, BranchWorkflowID: branch, Field: ev.Field, By: model.BranchParamByHuman,
		Summary:  truncRunes(fmt.Sprintf("用户撤销了主会话对「%s」的修改（%s）", model.ParamFieldLabel(ev.Field), ev.Summary), model.BranchParamSummaryMax),
		OldValue: ev.NewValue, NewValue: ev.OldValue, Rev: rev, RevertOf: ev.ID,
	}
	if err := s.recordParamEvent(ctx, &out.Event); err != nil {
		return nil, fmt.Errorf("已把「%s」改回旧值，但记录撤销事件失败：%w", model.ParamFieldLabel(ev.Field), err)
	}
	s.auditBranch(ctx, AuditBranchParamsRevert, model.BranchCreatedByHuman, ev.ParentWorkflowID, branch,
		map[string]any{"field": ev.Field, "event_id": out.Event.ID, "reverted_event": ev.ID, "summary": out.Event.Summary, "by": actor})
	return out, nil
}
