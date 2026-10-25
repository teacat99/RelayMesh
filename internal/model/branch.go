package model

import (
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// 分支会话（#236）：一个工作流可以是另一个工作流（主会话）的分支。
//
// 分支是**独立的会话**（自己的 workflow_id、凭据与 Agent），关系只是「谁是谁的主会话」加一份「向主会话开放多少」。
// 设计与分期见 .cursor/planning/2026-10-23-236-branch-sessions-plan.md 与 2026-10-24-236-p1-impl-plan.md。

// 分支关系的状态。
const (
	BranchStatusActive = "active"
	BranchStatusClosed = "closed"
)

// 分支关系的创建者（created_by）：human 网页用户；agent:<主会话的工作流编号> 主会话经 MCP 建的；migrate 迁移命令。
const (
	BranchCreatedByHuman   = "human"
	BranchCreatedByMigrate = "migrate"
	branchCreatedByAgent   = "agent:"
)

// BranchCreatedByAgent 拼出「某个主会话 Agent 创建」的 created_by。
func BranchCreatedByAgent(workflowID string) string { return branchCreatedByAgent + workflowID }

// ValidBranchCreatedBy 判断 created_by 是不是三种形态之一。
func ValidBranchCreatedBy(v string) bool {
	switch {
	case v == BranchCreatedByHuman, v == BranchCreatedByMigrate:
		return true
	case strings.HasPrefix(v, branchCreatedByAgent) && len(v) > len(branchCreatedByAgent):
		return true
	}
	return false
}

// InboxKindBranch 是主会话与分支之间互发消息（以及用户在分支里的发言抄送给主会话）在收件箱里的 kind。
// 和确认项答复、群聊 @ 提醒一样属于「系统投递」：不是用户追加的话，不占用户的待读上限，不并入 interactive_feedback 的秒回，用户不能撤回。
const InboxKindBranch = "branch"

// 消息发言者的种类（queued_feedbacks.actor 里的 kind）。
const (
	ActorKindMain   = "main"   // 主会话 Agent
	ActorKindBranch = "branch" // 分支 Agent
	ActorKindHuman  = "human"  // 用户本人（在另一个会话页里说的，被抄送过来）
	ActorKindSystem = "system" // 系统（分支已创建 / 已结束等）
)

// MsgActor 是一条消息的发言者：紧凑 JSON 存在 queued_feedbacks.actor 里；空 = 和以前一样（用户 / 本会话 Agent）。
type MsgActor struct {
	Kind     string `json:"kind"`
	Workflow string `json:"workflow,omitempty"`
}

// String 是入库的紧凑 JSON；空发言者返回空串。
func (a MsgActor) String() string {
	if strings.TrimSpace(a.Kind) == "" {
		return ""
	}
	b, err := json.Marshal(a)
	if err != nil {
		return ""
	}
	return string(b)
}

// ParseMsgActor 解析库里的 actor 文本；空串或不合法返回 false。
func ParseMsgActor(s string) (MsgActor, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return MsgActor{}, false
	}
	var a MsgActor
	if json.Unmarshal([]byte(s), &a) != nil || strings.TrimSpace(a.Kind) == "" {
		return MsgActor{}, false
	}
	return a, true
}

// 「向主会话开放多少」各项的取值。
const (
	ExposeHistoryOff     = "off"
	ExposeHistorySummary = "summary"
	ExposeHistoryFull    = "full"

	ExposeParamsOff           = "off"
	ExposeParamsResident      = "resident"
	ExposeParamsResidentPhase = "resident_phase"
	ExposeParamsAll           = "all"

	ExposeTasksRead   = "read"
	ExposeTasksAssign = "assign"
)

// 开放范围的三个一键预设。
const (
	ExposePresetVisible = "visible" // 仅可见
	ExposePresetCollab  = "collab"  // 协作
	ExposePresetManaged = "managed" // 托管
)

// BranchExpose 是分支向主会话开放的范围（只有人能改）。状态（Agent 状态 / 最近活动 / 待答数 / 任务数）恒开，不在这里。
// 一次定义全部可配的六项；各项由哪一期执行见 2026-10-24-236-p1-impl-plan.md 第 3 节（P1 只执行 History 与 Say）。
type BranchExpose struct {
	History string `json:"history"` // off | summary | full：branch read 能看到的汇报深度
	Decide  bool   `json:"decide"`  // P2：分支的请示能否由主会话定夺
	Say     bool   `json:"say"`     // 主会话能否给分支发消息（分支给主会话发消息恒允许）
	Params  string `json:"params"`  // P3：off | resident | resident_phase | all
	Launch  bool   `json:"launch"`  // P4：主会话能否启动 / 唤醒分支
	Tasks   string `json:"tasks"`   // P5：read | assign（主会话能否把自己建的任务指派给这个分支；同板才有意义）
}

// BranchExposePreset 返回预设对应的开放范围。
func BranchExposePreset(name string) (BranchExpose, bool) {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case ExposePresetVisible:
		return BranchExpose{History: ExposeHistorySummary, Params: ExposeParamsOff, Tasks: ExposeTasksRead}, true
	case ExposePresetCollab:
		return BranchExpose{History: ExposeHistoryFull, Decide: true, Say: true, Params: ExposeParamsResident, Tasks: ExposeTasksAssign}, true
	case ExposePresetManaged:
		return BranchExpose{History: ExposeHistoryFull, Decide: true, Say: true, Params: ExposeParamsAll, Launch: true, Tasks: ExposeTasksAssign}, true
	}
	return BranchExpose{}, false
}

// Validate 检查各项取值都在枚举里。
func (e BranchExpose) Validate() error {
	switch e.History {
	case ExposeHistoryOff, ExposeHistorySummary, ExposeHistoryFull:
	default:
		return fmt.Errorf("history 只能是 off / summary / full，收到 %q", e.History)
	}
	switch e.Params {
	case ExposeParamsOff, ExposeParamsResident, ExposeParamsResidentPhase, ExposeParamsAll:
	default:
		return fmt.Errorf("params 只能是 off / resident / resident_phase / all，收到 %q", e.Params)
	}
	switch e.Tasks {
	case ExposeTasksRead, ExposeTasksAssign:
	default:
		return fmt.Errorf("tasks 只能是 read / assign，收到 %q", e.Tasks)
	}
	return nil
}

// PresetName 返回与当前取值完全一致的预设名；都不一致返回空串（界面显示「自定义」）。
func (e BranchExpose) PresetName() string {
	for _, n := range []string{ExposePresetVisible, ExposePresetCollab, ExposePresetManaged} {
		if p, _ := BranchExposePreset(n); p == e {
			return n
		}
	}
	return ""
}

// ParamsSummary 是「改参数」这一项在摘要里的写法（P3 起生效）：写明主会话能改哪些，不认识的取值按「不可改」写（宁紧勿松）。
func ParamsSummary(level string) string {
	switch level {
	case ExposeParamsResident:
		return "可改参数：常驻规则"
	case ExposeParamsResidentPhase:
		return "可改参数：常驻规则与阶段"
	case ExposeParamsAll:
		return "可改参数：常驻规则、阶段、标题、唤醒词、异步开关"
	}
	return "不可改参数"
}

// Summary 是给人 / 智能体看的一行摘要，只列当前版本真正生效的几项：读历史、发消息、定夺请示（P2 起）、改参数（P3 起）、启动（P4 起）、派任务（P5 起）。
// 一项在预设里有取值、但对应的能力还没实现时不能写进摘要：写出来等于告诉智能体「你可以做」，它会照着去做然后撞墙。
// 任务这一项（P5 之前就是这种情况）从 P5 起生效，所以现在写进来；不认识的取值按「不可派任务」写（宁紧勿松）。
func (e BranchExpose) Summary() string {
	history := map[string]string{ExposeHistoryOff: "不可读历史", ExposeHistorySummary: "读历史：摘要", ExposeHistoryFull: "读历史：全文"}[e.History]
	if history == "" {
		history = "读历史：" + e.History
	}
	say := "不可发消息"
	if e.Say {
		say = "可发消息"
	}
	decide := "不可定夺请示"
	if e.Decide {
		decide = "可定夺请示"
	}
	launch := "不可启动"
	if e.Launch {
		launch = "可启动"
	}
	return history + "，" + say + "，" + decide + "，" + ParamsSummary(e.Params) + "，" + launch + "，" + TasksSummary(e.Tasks)
}

// TasksSummary 是「派任务」这一项在摘要里的写法（P5 起生效）：主会话能不能把自己建的任务指派给这个分支。
// 看任务不在这里：同一张任务板上的工作流本来就读得到彼此的任务。不认识的取值按「不可派任务」写。
func TasksSummary(level string) string {
	if level == ExposeTasksAssign {
		return "可派任务"
	}
	return "不可派任务"
}

// 分支的全局偏好（system_settings 的 branch.prefs 键，JSON；#236 P2）。
const (
	// BranchEscalateDefaultMinutes 是路由给主会话的请示超过多久没人答复就自动升级给用户（分钟）。
	BranchEscalateDefaultMinutes = 30
	BranchEscalateMinMinutes     = 5
	BranchEscalateMaxMinutes     = 1440
)

// BranchPrefs 是分支的全局偏好。零值 = 出厂默认：兜底开关关、30 分钟升级。
type BranchPrefs struct {
	// L2Guard 为真时，风险档为 L2 的请示不允许主会话代答（只能转交用户）；默认关（平台不对决策分级，见总方案 D4）。
	L2Guard bool `json:"l2_guard,omitempty"`
	// EscalateMinutes 是升级时限（5~1440 分钟）；0 = 没设置过，按默认 30。
	EscalateMinutes int `json:"escalate_minutes,omitempty"`
}

// ErrInvalidBranchPrefs 标记偏好校验不过（接口层据此回 400）。
var ErrInvalidBranchPrefs = errors.New("分支偏好不合法")

// Validate 校验取值：升级时限只能是 0（默认）或 5~1440 分钟。
func (p BranchPrefs) Validate() error {
	if p.EscalateMinutes != 0 && (p.EscalateMinutes < BranchEscalateMinMinutes || p.EscalateMinutes > BranchEscalateMaxMinutes) {
		return fmt.Errorf("%w：escalate_minutes 只能是 0（恢复默认 %d）或 %d~%d 分钟", ErrInvalidBranchPrefs,
			BranchEscalateDefaultMinutes, BranchEscalateMinMinutes, BranchEscalateMaxMinutes)
	}
	return nil
}

// EscalateAfterMinutes 返回实际生效的升级时限（分钟）：没设置过 / 越界（旧值损坏）都按默认处理。
func (p BranchPrefs) EscalateAfterMinutes() int {
	if p.EscalateMinutes < BranchEscalateMinMinutes || p.EscalateMinutes > BranchEscalateMaxMinutes {
		return BranchEscalateDefaultMinutes
	}
	return p.EscalateMinutes
}

// Value / Scan 让它在 SQLite 里存成一列 JSON 文本。
func (e BranchExpose) Value() (driver.Value, error) { return json.Marshal(e) }

func (e *BranchExpose) Scan(value interface{}) error {
	if value == nil {
		*e = BranchExpose{}
		return nil
	}
	var raw []byte
	switch v := value.(type) {
	case []byte:
		raw = v
	case string:
		raw = []byte(v)
	default:
		return errors.New("type assertion to []byte or string failed")
	}
	if len(strings.TrimSpace(string(raw))) == 0 {
		*e = BranchExpose{}
		return nil
	}
	return json.Unmarshal(raw, e)
}

// WorkflowBranch 是一条「分支 → 主会话」的关系。库里没有 workflows 表，所以关系直接记两个工作流编号。
// CopyUserNotes 故意不写 default 标签：GORM 创建时会把零值换成列默认值，「关闭抄送」就存不进去了。
type WorkflowBranch struct {
	ID               uint         `gorm:"primaryKey" json:"id"`
	BranchWorkflowID string       `gorm:"size:128;not null;uniqueIndex" json:"branch_workflow_id"`
	ParentWorkflowID string       `gorm:"size:128;not null;index" json:"parent_workflow_id"`
	CreatedBy        string       `gorm:"size:160;not null" json:"created_by"`
	Expose           BranchExpose `gorm:"type:text" json:"expose"`
	CopyUserNotes    bool         `gorm:"not null" json:"copy_user_notes"`
	Status           string       `gorm:"size:16;not null;index" json:"status"`
	CreatedAt        time.Time    `json:"created_at"`
	ClosedAt         *time.Time   `json:"closed_at,omitempty"`
}

// Active 表示关系仍然生效。
func (b WorkflowBranch) Active() bool { return b.Status == BranchStatusActive }
