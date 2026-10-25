package model

import (
	"strings"
	"time"
)

// 主会话经 session_params 改分支「会话参数」的事件（#236 P3）。
//
// 这张表同时服务三处：分支下一次回执里的 params: 行（未告知的事件，只告知一次）、会话页的事件行、
// 分支面板里「主会话最近改过什么」与撤销。权限与审计只在 store.ApplySessionParams 一处判定。

// 会话参数里能被主会话改的字段名；常驻规则与唤醒词沿用 workflow_prompt_revisions 里的字段名，保持同一套叫法。
const (
	ParamFieldResident   = PromptFieldResident
	ParamFieldPhase      = "phase"
	ParamFieldWakeResume = PromptFieldWakeResume
	ParamFieldWakeNew    = PromptFieldWakeNew
	ParamFieldTitle      = "title"
	ParamFieldAsync      = "async"
)

// BranchParamFields 是 session_params 能写的全部字段，也是 get 展示与 set 写入的固定顺序（开放档位从低到高）。
var BranchParamFields = []string{
	ParamFieldResident, ParamFieldPhase, ParamFieldWakeResume, ParamFieldWakeNew, ParamFieldTitle, ParamFieldAsync,
}

const (
	// BranchParamEventKeep 是每个分支保留的事件条数，超出的按时间删旧。
	BranchParamEventKeep = 200
	// BranchParamRateLimit 是每个分支每小时最多接受的主会话参数写入数（按字段计）：防止失控的主会话刷屏。
	BranchParamRateLimit = 30
	// BranchParamTitleMax 是主会话给分支改的标题的最大字数（按字符计）。
	BranchParamTitleMax = 80
	// BranchParamSummaryMax 是事件摘要的最大字数（按字符计）。
	BranchParamSummaryMax = 240
	// BranchParamByHuman 是由用户本人（网页登录态）产生的事件的操作者；主会话写入记成 agent:<主会话工作流编号>。
	BranchParamByHuman = "human"
)

// ExposeParamsRank 把「改参数」的四档换算成可比较的等级：off 0、resident 1、resident_phase 2、all 3；
// 不认识的取值一律按 off（0）处理——权限判定宁可偏紧。
func ExposeParamsRank(level string) int {
	switch level {
	case ExposeParamsResident:
		return 1
	case ExposeParamsResidentPhase:
		return 2
	case ExposeParamsAll:
		return 3
	}
	return 0
}

// ParamFieldMinLevel 返回改某个字段至少需要的「改参数」档位；不是可写字段返回空串。
//
// 常驻规则 ≥ resident；阶段 ≥ resident_phase；标题、两类唤醒词与异步开关只在 all。
// 授权档位、凭据、目录与主机、终端永不在这里（见 store.neverOpenParamKeys）。
func ParamFieldMinLevel(field string) string {
	switch field {
	case ParamFieldResident:
		return ExposeParamsResident
	case ParamFieldPhase:
		return ExposeParamsResidentPhase
	case ParamFieldWakeResume, ParamFieldWakeNew, ParamFieldTitle, ParamFieldAsync:
		return ExposeParamsAll
	}
	return ""
}

// ParamFieldAllowed 判断某个开放档位能不能改这个字段。
func ParamFieldAllowed(level, field string) bool {
	need := ParamFieldMinLevel(field)
	return need != "" && ExposeParamsRank(level) >= ExposeParamsRank(need)
}

// ParamFieldsEditable 返回某个开放档位下能改的字段（按 BranchParamFields 的顺序）。
func ParamFieldsEditable(level string) []string {
	var out []string
	for _, f := range BranchParamFields {
		if ParamFieldAllowed(level, f) {
			out = append(out, f)
		}
	}
	return out
}

// ParamFieldLabel 是字段在界面与回执里的叫法。
func ParamFieldLabel(field string) string {
	switch field {
	case ParamFieldResident:
		return "常驻规则"
	case ParamFieldPhase:
		return "阶段"
	case ParamFieldWakeResume:
		return "续聊唤醒词"
	case ParamFieldWakeNew:
		return "新开对话唤醒词"
	case ParamFieldTitle:
		return "标题"
	case ParamFieldAsync:
		return "异步对话"
	}
	return field
}

// ExposeWidens 判断把开放范围从 from 改成 to 是不是「放宽」：
// 「改参数」从低档调到更高的一档（任何一档非 off 的上调），或把「启动」从关打开。
// 放宽要终端解锁（密码再验证），收紧只需登录；对其它几项（读历史、发消息、定夺、任务）不设这道关。
func ExposeWidens(from, to BranchExpose) bool {
	if ExposeParamsRank(to.Params) > ExposeParamsRank(from.Params) {
		return true
	}
	return to.Launch && !from.Launch
}

// ClampExposeWiden 把 to 里「放宽」的部分压回 from 的取值，其余各项照 to：返回压回之后的开放范围与被压回的项名
// （"params" / "launch"）。用于「终端不可用、没法验证是本人在点」时仍然让其它设置生效，而不放宽授权。
// from 里不认识的「改参数」档位按 off 处理（宁紧勿松）。to 本身没有放宽时原样返回。
func ClampExposeWiden(from, to BranchExpose) (BranchExpose, []string) {
	out := to
	var clamped []string
	if ExposeParamsRank(to.Params) > ExposeParamsRank(from.Params) {
		out.Params = from.Params
		if ExposeParamsRank(out.Params) == 0 {
			out.Params = ExposeParamsOff
		}
		clamped = append(clamped, "params")
	}
	if to.Launch && !from.Launch {
		out.Launch = false
		clamped = append(clamped, "launch")
	}
	return out, clamped
}

// BranchParamEvent 是一次参数写入（或一次撤销）。库里的 ParentWorkflowID / BranchWorkflowID 记事件发生时的关系两端。
type BranchParamEvent struct {
	ID               uint      `gorm:"primaryKey" json:"id"`
	CreatedAt        time.Time `gorm:"index" json:"created_at"`
	ParentWorkflowID string    `gorm:"size:128;not null;index" json:"parent_workflow_id"`
	BranchWorkflowID string    `gorm:"size:128;not null;index" json:"branch_workflow_id"`
	// Field 是 ParamField* 之一。
	Field string `gorm:"size:16;not null" json:"field"`
	// By 是写入者：agent:<主会话工作流编号> 或 human（用户撤销）。库里的列叫 actor：by 是 SQL 关键字，裸写进查询里容易出事。
	By string `gorm:"column:actor;size:160;not null" json:"by"`
	// Summary 是一句人话（「常驻规则 rev 3」「阶段 讨论 → 开发」），不含常驻 / 唤醒词的全文。
	Summary string `gorm:"size:240;not null" json:"summary"`
	// OldValue / NewValue 是撤销用的旧值与新值全文（常驻规则 ≤1200 字、唤醒词 ≤1500 字，其余很短；异步为 on / off）。
	OldValue string `gorm:"type:text" json:"old_value,omitempty"`
	NewValue string `gorm:"type:text" json:"new_value,omitempty"`
	// Rev 是常驻规则写入后的版本号；其它字段为 0。
	Rev int `gorm:"not null;default:0" json:"rev,omitempty"`
	// ToldAt 是分支的回执 params: 行已经带过这条事件的时间；空 = 还没告诉。
	ToldAt *time.Time `gorm:"index" json:"told_at,omitempty"`
	// RevertedAt / RevertedBy 标记这条事件已被用户撤销；撤销本身另记一条事件，用 RevertOf 指回原事件。
	RevertedAt *time.Time `json:"reverted_at,omitempty"`
	RevertedBy string     `gorm:"size:64" json:"reverted_by,omitempty"`
	RevertOf   uint       `gorm:"index" json:"revert_of,omitempty"`
}

// ByAgent 表示这条事件由主会话的智能体写入。
func (e BranchParamEvent) ByAgent() bool { return strings.HasPrefix(e.By, "agent:") }

// IsRevert 表示这条事件是用户撤销产生的。
func (e BranchParamEvent) IsRevert() bool { return e.RevertOf != 0 }
