package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/teacat99/RelayMesh/v2/internal/model"
	"github.com/teacat99/RelayMesh/v2/internal/sessionprompt"
)

// 唤醒词模板的存取、分层选用与「现状」现算（#232 D3，决策 D-376；渲染本身见 internal/sessionprompt）。
//
// 模板的来源分四层，从高到低：
//
//	预设自己写的正文 > 会话模板（workflow_prompts.wake_*）> 全局设置里的覆盖（settings.wakeTemplates）> 内置默认
//
// 预设正文排在最前，是为了让升级前就配了自定义正文的预设逐字不变；没写正文的预设跟着新的默认走。

// WakeLayer 是最终文字的模板来自哪一层（预览与界面据此提示「现在用的是谁的」）。
type WakeLayer string

const (
	WakeLayerPreset  WakeLayer = "preset"
	WakeLayerSession WakeLayer = "session"
	WakeLayerGlobal  WakeLayer = "global"
	WakeLayerBuiltin WakeLayer = "builtin"
	// WakeLayerDraft 只出现在预览里：界面上还没保存的草稿。
	WakeLayerDraft WakeLayer = "draft"
)

// WakeTemplates 是全局设置里的唤醒词模板覆盖（settings.wakeTemplates）；空串 = 没覆盖。
type WakeTemplates struct {
	Resume   string `json:"resume,omitempty"`
	New      string `json:"new,omitempty"`
	WebStart string `json:"webStart,omitempty"`
}

// For 返回某类唤醒词的全局覆盖。
func (w WakeTemplates) For(kind sessionprompt.Kind) string {
	switch kind {
	case sessionprompt.KindResume:
		return w.Resume
	case sessionprompt.KindNew:
		return w.New
	case sessionprompt.KindWebStart:
		return w.WebStart
	}
	return ""
}

// wakeKindName 是各类唤醒词在错误提示里的叫法。
func wakeKindName(kind sessionprompt.Kind) string {
	switch kind {
	case sessionprompt.KindResume:
		return "续聊"
	case sessionprompt.KindNew:
		return "新开对话"
	case sessionprompt.KindWebStart:
		return "网页启动提示词"
	}
	return string(kind)
}

// NormalizeWakeTemplate 把用户写的某类模板整理成存库的形态并检查：字数上限；
// 新开对话与网页启动提示词是给「还不认识这个工作流」的智能体看的，必须带 %workflow，否则它接不进来。
// 空串合法（= 清除，回到下一层）。
func NormalizeWakeTemplate(kind sessionprompt.Kind, text string) (string, error) {
	v, err := NormalizePromptText(text, sessionprompt.TemplateMaxRunes)
	if err != nil {
		return "", NewInvalidInputError(wakeKindName(kind) + "模板：" + err.Error())
	}
	if v != "" && (kind == sessionprompt.KindNew || kind == sessionprompt.KindWebStart) && !strings.Contains(v, "%workflow") {
		return "", NewInvalidInputError(wakeKindName(kind) + "模板必须包含 %workflow：智能体靠这个编号接入会话，缺了它就接不进来")
	}
	return v, nil
}

// ValidateWakeTemplates 检查要保存的全局覆盖（规则见 NormalizeWakeTemplate）。返回规整后的值。
func ValidateWakeTemplates(in WakeTemplates) (WakeTemplates, error) {
	var out WakeTemplates
	fields := []struct {
		kind sessionprompt.Kind
		in   string
		out  *string
	}{
		{sessionprompt.KindResume, in.Resume, &out.Resume}, {sessionprompt.KindNew, in.New, &out.New},
		{sessionprompt.KindWebStart, in.WebStart, &out.WebStart},
	}
	for _, f := range fields {
		v, err := NormalizeWakeTemplate(f.kind, f.in)
		if err != nil {
			return WakeTemplates{}, err
		}
		*f.out = v
	}
	return out, nil
}

// GlobalWakeTemplates 读全局设置里的模板覆盖；读不到、格式不对一律当作没有覆盖（宁可用内置默认，也不让发唤醒词失败）。
func (s *Store) GlobalWakeTemplates(ctx context.Context) WakeTemplates {
	m, err := s.GetSettings(ctx)
	if err != nil {
		return WakeTemplates{}
	}
	raw, ok := m["wakeTemplates"]
	if !ok || raw == nil {
		return WakeTemplates{}
	}
	b, err := json.Marshal(raw)
	if err != nil {
		return WakeTemplates{}
	}
	var got WakeTemplates
	if json.Unmarshal(b, &got) != nil {
		return WakeTemplates{}
	}
	clamp := func(v string) string {
		v = strings.TrimSpace(v)
		if r := []rune(v); len(r) > sessionprompt.TemplateMaxRunes {
			return string(r[:sessionprompt.TemplateMaxRunes])
		}
		return v
	}
	return WakeTemplates{Resume: clamp(got.Resume), New: clamp(got.New), WebStart: clamp(got.WebStart)}
}

// SaveWakeTemplate 保存会话自己的唤醒词模板（kind 只能是 resume / new）。空串 = 清除，回到全局 / 内置。
// 内容与现有的一致时什么也不做（changed=false）；否则写一条历史版本（每个会话每个字段保留最近 PromptRevisionKeep 个）。
// 与常驻提示词不同，模板是 Hub 发送时读的，不是智能体在回执里读的，所以不涨 ResidentRev、也不需要「已告知」。
func (s *Store) SaveWakeTemplate(ctx context.Context, workflowID string, kind sessionprompt.Kind, text, actor string, now time.Time) (model.WorkflowPrompt, bool, error) {
	wf := strings.TrimSpace(workflowID)
	if wf == "" {
		return model.WorkflowPrompt{}, false, NewInvalidInputError("workflow_id 不能为空")
	}
	var field string
	switch kind {
	case sessionprompt.KindResume:
		field = model.PromptFieldWakeResume
	case sessionprompt.KindNew:
		field = model.PromptFieldWakeNew
	default:
		return model.WorkflowPrompt{}, false, NewInvalidInputError("会话只能自定义续聊与新开对话两种唤醒词模板")
	}
	text, err := NormalizeWakeTemplate(kind, text)
	if err != nil {
		return model.WorkflowPrompt{}, false, err
	}
	actor = strings.TrimSpace(actor)
	if len(actor) > 64 {
		actor = actor[:64]
	}
	var out model.WorkflowPrompt
	changed := false
	err = s.WithTx(ctx, func(tx *gorm.DB) error {
		var row model.WorkflowPrompt
		err := tx.Where("workflow_id = ?", wf).Take(&row).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			row = model.WorkflowPrompt{WorkflowID: wf}
		} else if err != nil {
			return err
		}
		out = row
		cur := &row.WakeResume
		if kind == sessionprompt.KindNew {
			cur = &row.WakeNew
		}
		if *cur == text {
			return nil
		}
		*cur = text
		row.UpdatedAt = now
		row.UpdatedBy = actor
		if err := tx.Save(&row).Error; err != nil {
			return err
		}
		var maxRev int
		if err := tx.Model(&model.WorkflowPromptRevision{}).Where("workflow_id = ? AND field = ?", wf, field).
			Select("COALESCE(MAX(rev), 0)").Scan(&maxRev).Error; err != nil {
			return err
		}
		if err := tx.Create(&model.WorkflowPromptRevision{
			WorkflowID: wf, Field: field, Rev: maxRev + 1, Text: text, Actor: actor, CreatedAt: now,
		}).Error; err != nil {
			return err
		}
		if err := prunePromptRevisions(tx, wf, field); err != nil {
			return err
		}
		out = row
		changed = true
		return nil
	})
	if err != nil {
		return model.WorkflowPrompt{}, false, err
	}
	return out, changed, nil
}

// WakeInput 是渲染一条唤醒词要的输入。
type WakeInput struct {
	Kind sessionprompt.Kind
	// WorkflowID 可空（预设没配工作流）：为空时不算「现状」变量，默认模板也换成不点名工作流的写法。
	WorkflowID string
	// PresetBody 是预设自己写的正文（空 = 没写）；它排在所有分层的最前面，原样沿用升级前的变量语义。
	PresetBody string
	// Draft 非空时直接用它当模板（预览界面上还没保存的草稿），不走分层选用；层标成 draft。
	Draft *string
	// Base 是调用方已知的变量：目录 / 主机（预设里的）、运行时间提醒。Workflow 与「现状」由这里补齐，不必填。
	Base sessionprompt.Vars
	Now  time.Time
}

// WakeResult 是渲染结果。
type WakeResult struct {
	Text     string
	Layer    WakeLayer
	Template string // 用到的模板原文（未渲染），预览用
	Vars     sessionprompt.Vars
}

// ResolveWakeTemplate 按分层选出模板。hasWorkflow 影响内置默认的写法（见 sessionprompt.Builtin）。
// 网页启动提示词的内置默认不在这里（它由 WebWorkflowStartPrompt 生成），builtin 层返回空串，由调用方兜底。
func (s *Store) ResolveWakeTemplate(ctx context.Context, kind sessionprompt.Kind, workflowID, presetBody string, hasWorkflow bool) (string, WakeLayer) {
	if body := strings.TrimSpace(presetBody); body != "" {
		return presetBody, WakeLayerPreset
	}
	if wf := strings.TrimSpace(workflowID); wf != "" && (kind == sessionprompt.KindResume || kind == sessionprompt.KindNew) {
		if row, err := s.GetWorkflowPrompt(ctx, wf); err == nil {
			own := row.WakeResume
			if kind == sessionprompt.KindNew {
				own = row.WakeNew
			}
			if strings.TrimSpace(own) != "" {
				return own, WakeLayerSession
			}
		}
	}
	if g := s.GlobalWakeTemplates(ctx).For(kind); g != "" {
		return g, WakeLayerGlobal
	}
	return sessionprompt.Builtin(kind, hasWorkflow), WakeLayerBuiltin
}

// RenderWake 一步到位：选模板、现算「现状」变量、渲染。出错一律宽容（变量缺了显示「（暂无）」），
// 唤醒词的发送不能因为统计信息读不出来而失败。
func (s *Store) RenderWake(ctx context.Context, in WakeInput) WakeResult {
	wf := strings.TrimSpace(in.WorkflowID)
	now := in.Now
	if now.IsZero() {
		now = time.Now()
	}
	var tpl string
	var layer WakeLayer
	if in.Draft != nil {
		tpl, layer = *in.Draft, WakeLayerDraft
	} else {
		tpl, layer = s.ResolveWakeTemplate(ctx, in.Kind, wf, in.PresetBody, wf != "")
	}
	v := in.Base
	v.Workflow = wf
	if wf != "" && sessionprompt.NeedsState(tpl) {
		s.fillWakeLive(ctx, &v, wf, now)
	}
	text := sessionprompt.Render(tpl, v)
	if layer != WakeLayerPreset && (in.Kind == sessionprompt.KindResume || in.Kind == sessionprompt.KindNew) {
		// 续聊 / 新开对话的提示词是命令行里的一个参数，不能含换行：会话模板、全局覆盖与内置默认允许分行写，渲染后压成一行。
		// 预设自己的正文保存时就校验过是单行，原样不动（保持升级前的逐字相同）。
		text = sessionprompt.OneLine(text)
	}
	return WakeResult{Text: text, Layer: layer, Template: tpl, Vars: v}
}

// fillWakeLive 现算「现状」变量：标题 / 目录 / 主机、模式与授权档位、阶段、常驻规则、未读追加、未完成任务、下一步、最近交付。
// 调用方（预设）已经给了值的变量不覆盖。每一项读不出来都留空，由渲染器显示「（暂无）」。
func (s *Store) fillWakeLive(ctx context.Context, v *sessionprompt.Vars, wf string, now time.Time) {
	// 标题、目录、主机：取这个工作流最近一条会话（侧栏上看到的就是它）
	if v.Title == "" || v.Project == "" || v.Host == "" {
		var brief model.FeedbackSession
		if err := s.db.WithContext(ctx).Select("title", "host_name", "project_directory").
			Where("workflow_id = ?", wf).Order("created_at DESC").Limit(1).Take(&brief).Error; err == nil {
			if v.Title == "" {
				v.Title = brief.Title
			}
			if v.Project == "" {
				v.Project = brief.ProjectDirectory
			}
			if v.Host == "" {
				v.Host = brief.HostName
			}
		}
	}

	if info, err := s.WorkflowModeInfo(ctx, wf, now); err == nil {
		v.Mode = "同步"
		if info.State.Effective {
			v.Mode = "异步对话"
		}
		if info.Profile != nil && v.Auth == "" {
			v.Auth = strings.TrimSpace(info.Profile.Name + " " + info.Profile.Level)
		}
	}

	if cur, phases, err := s.GetWorkflowPhaseWithDefaults(ctx, wf); err == nil && cur != "" {
		v.Phase = cur
		for _, p := range phases {
			if p.ID == cur && strings.TrimSpace(p.Label) != "" {
				v.Phase = p.Label // 阶段可以在设置里改名，写用户看到的那个名字
				break
			}
		}
	}

	if row, err := s.GetWorkflowPrompt(ctx, wf); err == nil {
		v.Resident = row.Resident
	}

	if sum, err := s.InboxSummary(ctx, wf, now); err == nil {
		v.Inbox = strconv.Itoa(sum.Unread)
	}

	if tasks, total := s.gateTasks(ctx, wf, 5); total > 0 {
		var parts []string
		for _, t := range tasks {
			parts = append(parts, "#"+strconv.Itoa(t.Seq))
		}
		list := strings.Join(parts, " ")
		if total > len(tasks) {
			list += " 等"
		}
		v.Tasks = fmt.Sprintf("%d 个（%s）", total, list)
	} else {
		v.Tasks = "0 个"
	}

	if cp, err := s.GetLatestCheckpoint(ctx, wf); err == nil && cp != nil {
		var body struct {
			NextActions []string `json:"next_actions"`
		}
		if json.Unmarshal([]byte(cp.ContentJSON), &body) == nil {
			for _, a := range body.NextActions {
				if a = strings.TrimSpace(a); a != "" {
					v.Next = a
					break
				}
			}
		}
	}

	if sess, err := s.GetLatestAnchoredSession(ctx, wf, ""); err == nil && sess != nil {
		c := sess.GitCommit
		if len(c) > 7 {
			c = c[:7]
		}
		v.Anchor = strings.TrimSpace(c + " " + sess.GitSubject)
	}
}

// webStartPrompt 生成网页新建会话时给用户的启动提示词：全局设置里覆盖了就按覆盖渲染（只有编号 / 标题 / 目录三个变量可用），
// 否则用内置的 WebWorkflowStartPrompt。
func (s *Store) webStartPrompt(ctx context.Context, title, workflowID, projectDir string) string {
	if tpl := s.GlobalWakeTemplates(ctx).WebStart; tpl != "" {
		return sessionprompt.Render(tpl, sessionprompt.Vars{Workflow: workflowID, Title: title, Project: projectDir})
	}
	return WebWorkflowStartPrompt(title, workflowID, projectDir)
}
