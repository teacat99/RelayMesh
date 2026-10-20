package model

import (
	"strings"
	"time"
)

// 任务（WorkItem）：独立于会话的工作清单条目。
//
// 与 Task（托管自驾的编排任务：指挥端 / 执行端、分段、报告、反馈）是两回事，互不引用：
//   - 一个任务可以关联多个会话，一个会话也可以涉及多个任务（多对多），但任务本身不依附于任何会话；
//   - 任务只回答「这件事做到哪了」：标题、类型、状态、一段可覆盖的最新进度、最近的几条进展，以及完成后的结论；
//   - 状态绑定智能体自己的开发流程（#110）：开发中 → 验证中（回归 / e2e / 热更冒烟）→ 验证通过后由智能体直接置「已完成」，
//     结论里写明怎么验证的；用户不再逐条点头确认，发现问题直接在会话里说，或在任务上点「改进需求」。

// 任务状态。主线：待办 → 进行中 → 验证中 → 已完成；「已搁置」是旁支，随时可以回到主线。
// review 这个存储值沿用旧名（数据库里已有大量记录），界面与回执里叫「验证中」。
const (
	WorkStateTodo    = "todo"
	WorkStateDoing   = "doing"
	WorkStateReview  = "review"
	WorkStateDone    = "done"
	WorkStateShelved = "shelved"
)

// WorkStates 按看板列顺序列出全部状态。
var WorkStates = []string{WorkStateTodo, WorkStateDoing, WorkStateReview, WorkStateDone, WorkStateShelved}

// 指派后的接取状态（WorkItem.ClaimState）。空串 = 没有指派。状态机本身不加新状态（todo / doing / review / done / shelved），
// 「已交付待验收」就是有协作指派的任务停在 review。
const (
	WorkClaimOffered  = "offered"  // 已派未接
	WorkClaimAccepted = "accepted" // 承接者已接取
	WorkClaimDeclined = "declined" // 承接者拒绝（保留承接者与理由，等重派）
)

// 时间线上的协作事件（WorkItemUpdate.Event）。
const (
	WorkEventAssign         = "assign"
	WorkEventReassign       = "reassign"
	WorkEventUnassign       = "unassign"
	WorkEventAccept         = "accept"
	WorkEventDecline        = "decline"
	WorkEventDeliver        = "deliver"
	WorkEventAcceptDelivery = "accept_delivery"
	WorkEventRejectDelivery = "reject_delivery"
)

// WorkClaims 是 claim 参数的合法取值（显式的接取 / 拒绝 / 交付 / 验收 / 退回）。
var WorkClaims = []string{"accept", "decline", "deliver", "accept_delivery", "reject_delivery"}

// IsCollaborative 表示任务有「协作指派」：已指派，且派发者 ≠ 承接者（人在 Web 上派的也算）。
// 只有这种任务才有「承接者交付、派发者验收」的分工；自己给自己的任务（无指派，或派发者 = 承接者）照旧由自己置完成。
func (w *WorkItem) IsCollaborative() bool {
	return w.AssigneeWorkflowID != "" && w.AssignedByWorkflowID != w.AssigneeWorkflowID
}

// 任务类型：五类固定分类 + 自由标签。分类用来回答「这是什么性质的事」，标签补充领域（例如 终端 / 设置 / 发布）。
const (
	WorkKindBug      = "bug"
	WorkKindFeature  = "feature"
	WorkKindImprove  = "improve"
	WorkKindResearch = "research"
	WorkKindOps      = "ops"
)

// WorkKinds 按展示顺序列出全部分类。
var WorkKinds = []string{WorkKindBug, WorkKindFeature, WorkKindImprove, WorkKindResearch, WorkKindOps}

// 任务阶段：这件事在「做什么」这条线上走到哪了——评估 → 方案 → 开发 → 改进。
//
// 它与另外两个维度互相独立：
//   - 类型（Kind）回答「这是什么性质的事」（缺陷 / 功能 / …），不是一条前进的线；
//   - 状态（State）回答「现在做到流程的哪一步」（待办 / 进行中 / 验证中 / …），智能体置「已完成」必须带结论；
//   - 阶段（Stage）只是一个标注，人和智能体都能改，不进状态机。空串 = 未标注（界面按类型推导一个默认值）。
const (
	WorkStageAssess  = "assess"  // 评估：搞清楚要不要做、值不值得做
	WorkStagePlan    = "plan"    // 方案：已决定做，正在定怎么做
	WorkStageDev     = "dev"     // 开发：按方案实现
	WorkStageImprove = "improve" // 改进：已交付，在迭代打磨
)

// WorkStages 按前进顺序列出全部阶段。
var WorkStages = []string{WorkStageAssess, WorkStagePlan, WorkStageDev, WorkStageImprove}

var workStageLabels = map[string]string{
	WorkStageAssess: "评估", WorkStagePlan: "方案", WorkStageDev: "开发", WorkStageImprove: "改进",
}

// WorkStageLabel 返回阶段的中文名；空串或未知值返回「未标注」。
func WorkStageLabel(stage string) string {
	if l, ok := workStageLabels[stage]; ok {
		return l
	}
	return "未标注"
}

// 操作者类型：任务的写入口只有两个，规则也只因它而不同。
const (
	WorkActorHuman = "human" // Web 界面（已登录的管理员）
	WorkActorAgent = "agent" // MCP 工具（智能体）
)

// 字段上限。进度和结论都是「摘要」而不是文档：长内容请放会话文档，这里只留能定位问题的几行。
// 描述回答「这件事到底是什么」：背景、要做什么、做到什么算完成，一小段话，创建时写一次，以后很少改。
const (
	WorkTitleMax       = 200
	WorkDescriptionMax = 1000
	WorkProgressMax    = 500
	WorkConclusionMax  = 400
	WorkNoteMax        = 500
	// WorkImproveMax 是「发起改进需求」的正文上限：它会连同「改进需求：」前缀一起写进时间线（≤ WorkNoteMax）。
	WorkImproveMax = 450
	WorkLabelMax   = 24
	WorkLabelsMax  = 8
	WorkRefMax     = 200
	WorkRefsMax    = 10
	// WorkUpdatesKeep 是每个任务保留的最近进展条数；更早的会被新条目挤掉。
	WorkUpdatesKeep = 20
	// WorkFlipMergeMinutes：同一个人在这么多分钟之内把状态改了又改回去，时间线上这两次改动互相抵消、不留记录
	// （误点后改回来的操作不算进展）；超过这个时间的反复属于真实的重开 / 重新确认，照常记录。
	WorkFlipMergeMinutes = 30
	// WorkCompactAfterDays：任务关闭（已完成 / 已搁置）超过这么多天后，进展时间线被压缩掉，只保留结论与引用。
	WorkCompactAfterDays = 90
	// WorkConclusionFallback 压缩时若没有结论，从最新进度截取多少字作为结论。
	WorkConclusionFallback = 300
)

// WorkItem 是任务本体。ID 是全局自增主键（REST 使用）；Seq 是板内的序号（智能体与界面上的 #12）。
// 单工作流的板里它就是工作流内的序号；同一项目的多个工作流共用一张板时，它们共用一套序号。
// 序号只增不复用：删掉 #12 之后新任务也是 #13，避免提交说明里的旧引用指向别的事。
type WorkItem struct {
	ID         uint   `gorm:"primaryKey;autoIncrement" json:"id"`
	WorkflowID string `gorm:"size:128;not null;index;uniqueIndex:ux_work_item_seq,priority:1" json:"workflow_id"`
	// BoardKey 是任务所在的板（#175）：项目板 "p<项目编号>"，独立板 "w:<工作流编号>"。Seq 在板内唯一、只增不复用；
	// WorkflowID 的含义是「创建它的工作流」（来源）。迁移前的历史行是空串，启动时由 reconcileBoards 补齐；
	// 部分唯一索引只约束已归板的行，旧二进制写入的空值行不会触发冲突。
	BoardKey string      `gorm:"size:160;not null;default:'';index:idx_work_items_board;uniqueIndex:ux_work_item_board_seq,priority:1,where:board_key <> ''" json:"board_key"`
	Seq      int         `gorm:"not null;uniqueIndex:ux_work_item_seq,priority:2;uniqueIndex:ux_work_item_board_seq,priority:2" json:"seq"`
	Title    string      `gorm:"size:256;not null" json:"title"`
	Kind     string      `gorm:"size:16;not null;default:'feature';index" json:"kind"`
	Labels   StringArray `gorm:"type:text" json:"labels"`
	State    string      `gorm:"size:16;not null;default:'todo';index" json:"state"`
	// Description 是任务内容的说明：这件事是什么、要做到什么程度。标题只是名字，进度只回答「现在做到哪」，
	// 都不足以让人在任务页和任务弹窗里看懂「这个任务到底指什么」；创建任务时写一次，之后很少改。
	// 只改描述不算进展：不动 updated_at、不占时间线（与调整归属同理）。
	Description string `gorm:"type:text" json:"description"`
	// Stage 是任务所处的阶段（评估 / 方案 / 开发 / 改进），空串 = 未标注；见上面的常量说明。
	Stage string `gorm:"size:16;not null;default:''" json:"stage,omitempty"`
	// Progress 是「最新进度」：每次覆盖写入，只回答现在做到哪；历史由时间线承载。
	Progress string `gorm:"type:text" json:"progress"`
	// Conclusion 是 1–3 行的结论，任务收尾时写；时间线被压缩后它是唯一保留下来的「做完了什么」。
	Conclusion string      `gorm:"type:text" json:"conclusion"`
	Refs       StringArray `gorm:"type:text" json:"refs"`
	// ParentID 指向「批次」任务，只允许一层嵌套。
	ParentID *uint `gorm:"index" json:"parent_id,omitempty"`
	// ModuleID 指向规划树上的模块或功能（work_modules）；为空表示「未归类」。
	ModuleID *uint `gorm:"index" json:"module_id,omitempty"`
	// Percent 是显式写下的完成度（0–100）；为空表示由状态 / 子任务推导，见 EffectivePercent。
	Percent         *int       `json:"percent,omitempty"`
	SourceSessionID string     `gorm:"size:128" json:"source_session_id,omitempty"`
	LastSessionID   string     `gorm:"size:128" json:"last_session_id,omitempty"`
	CreatedBy       string     `gorm:"size:16;not null;default:'human'" json:"created_by"`
	ClosedAt        *time.Time `json:"closed_at,omitempty"`
	CreatedAt       time.Time  `json:"created_at"`
	UpdatedAt       time.Time  `json:"updated_at"`

	// 指派（#175 B2，设计稿 design/shared-task-board-b2.md）：管理 Agent / 人把任务派给板上的某个工作流，
	// 承接者接取（或拒绝）、交付，派发者验收。这四个字段回答「现在由谁承接、谁派的、接没接」，
	// 全部是加列、默认空串：空 AssigneeWorkflowID = 没有指派，行为与 B2 之前完全一致。
	// AssignedByWorkflowID 为空而 AssigneeWorkflowID 非空 = 人在 Web 上派的（没有 Agent 派发者）。
	AssigneeWorkflowID   string     `gorm:"size:128;not null;default:'';index" json:"assignee_workflow_id,omitempty"`
	AssignedByWorkflowID string     `gorm:"size:128;not null;default:''" json:"assigned_by_workflow_id,omitempty"`
	ClaimState           string     `gorm:"size:16;not null;default:''" json:"claim_state,omitempty"`
	AssignedAt           *time.Time `json:"assigned_at,omitempty"`
	AcceptedAt           *time.Time `json:"accepted_at,omitempty"`

	// 以下字段不落库，由查询按需填充。
	Updates     []WorkItemUpdate  `gorm:"-" json:"updates,omitempty"`
	Sessions    []WorkItemSession `gorm:"-" json:"sessions,omitempty"`
	ChildTotal  int               `gorm:"-" json:"child_total,omitempty"`
	ChildDone   int               `gorm:"-" json:"child_done,omitempty"`
	SessionNum  int               `gorm:"-" json:"session_count,omitempty"`
	ParentTitle string            `gorm:"-" json:"parent_title,omitempty"`
	ParentSeq   int               `gorm:"-" json:"parent_seq,omitempty"`
	// ModulePath 是所属模块的完整路径（终端/历史日志）；未归类时为空。
	ModulePath string `gorm:"-" json:"module,omitempty"`
	// ProgressPct / ProgressFrom 是进度条上的取值与来源（explicit / children / state），见 EffectivePercent。
	ProgressPct  int    `gorm:"-" json:"progress_pct"`
	ProgressFrom string `gorm:"-" json:"progress_from,omitempty"`
	// SessionCtx 只在按会话查询（WorkItemFilter.SessionID）时填充：这个会话对这条任务做了什么。
	SessionCtx *WorkItemSessionCtx `gorm:"-" json:"session_ctx,omitempty"`
}

// WorkItemSessionCtx 描述「某个会话」与「某条任务」的关系，供会话页的「本对话的任务」展示。
// 数据来自任务的时间线（每个任务只保留最近 WorkUpdatesKeep 条），所以 Updates 是「时间线上还看得见的条数」。
type WorkItemSessionCtx struct {
	// Created 表示这条任务是这个会话创建的。
	Created bool `json:"created"`
	// Updates 是这个会话写在时间线上的条数（创建那一条也算）。
	Updates int `json:"updates"`
	// FromState / ToState：这个会话第一次与最近一次改变状态时的「原状态」与「新状态」；会话没有改过状态时为空。
	FromState string `json:"from_state,omitempty"`
	ToState   string `json:"to_state,omitempty"`
	// LastAt 是这个会话最近一次写时间线的时间；时间线上没有它的记录（只是被关联）时为空。
	LastAt *time.Time `json:"last_at,omitempty"`
}

// WorkItemSessionLink 是「某个会话涉及某个任务」的一条关联，连同这个会话对任务做了什么。
// 会话页按轮次（每个会话）展开本轮涉及的任务时，一次取回整个工作流的全部关联。
type WorkItemSessionLink struct {
	SessionID  string `json:"session_id"`
	WorkItemID uint   `json:"work_item_id"`
	WorkItemSessionCtx
}

// IsClosed 表示任务已离开主线（已完成或已搁置）。
func (w *WorkItem) IsClosed() bool { return IsClosedWorkState(w.State) }

// WorkItemUpdate 是时间线上的一条进展：一次状态变化、一段进度或一句备注。
type WorkItemUpdate struct {
	ID         uint      `gorm:"primaryKey;autoIncrement" json:"id"`
	WorkItemID uint      `gorm:"not null;index" json:"work_item_id"`
	ActorKind  string    `gorm:"size:16;not null" json:"actor_kind"`
	SessionID  string    `gorm:"size:128" json:"session_id,omitempty"`
	FromState  string    `gorm:"size:16" json:"from_state,omitempty"`
	ToState    string    `gorm:"size:16" json:"to_state,omitempty"`
	Text       string    `gorm:"type:text" json:"text,omitempty"`
	CreatedAt  time.Time `json:"created_at"`
	// ActorWorkflowID 是智能体写入时它自己的工作流（共享板上看「谁改的」）；人在 Web 上写入为空。
	ActorWorkflowID string `gorm:"size:128;not null;default:''" json:"actor_workflow_id,omitempty"`
	// Event 只给协作相关的动作写（见 WorkEvent* 常量）；普通进展（状态 / 进度 / 备注）留空，由 from / to / text 表达。
	Event string `gorm:"size:24;not null;default:''" json:"event,omitempty"`
}

// WorkItemSession 是任务与会话的关联（多对多）。
type WorkItemSession struct {
	WorkItemID uint      `gorm:"primaryKey" json:"-"`
	SessionID  string    `gorm:"primaryKey;size:128;index" json:"session_id"`
	CreatedAt  time.Time `json:"linked_at"`

	// 以下字段不落库，来自会话本身，方便界面直接展示。
	Title  string `gorm:"-" json:"title,omitempty"`
	Status string `gorm:"-" json:"status,omitempty"`
}

// WorkItemCounter 记录每个工作流已发出的最大序号，保证序号不复用。
// #175 起序号按板分配（WorkBoardCounter），这张旧表由新代码同步写入，只为让旧二进制回滚后仍能接着发号。
type WorkItemCounter struct {
	WorkflowID string `gorm:"primaryKey;size:128" json:"workflow_id"`
	LastSeq    int    `gorm:"not null;default:0" json:"last_seq"`
}

// WorkBoardCounter 记录每张板已发出的最大序号，保证序号不复用（#175）。
type WorkBoardCounter struct {
	BoardKey string `gorm:"primaryKey;size:160" json:"board_key"`
	LastSeq  int    `gorm:"not null;default:0" json:"last_seq"`
}

// WorkItemAlias 是改号后的旧号别名（#175）：显式合并板时，被重编的任务把旧的（工作流，序号）留在这里，
// 这样旧提交说明、文档里的 #N 仍能解析到它。迁移本身永不改号，所以平时这张表是空的。
type WorkItemAlias struct {
	OldWorkflowID string    `gorm:"primaryKey;size:128" json:"old_workflow_id"`
	OldSeq        int       `gorm:"primaryKey" json:"old_seq"`
	WorkItemID    uint      `gorm:"not null;index" json:"work_item_id"`
	CreatedAt     time.Time `json:"created_at"`
}

// IsClosedWorkState 判断状态是否已离开主线。
func IsClosedWorkState(state string) bool {
	return state == WorkStateDone || state == WorkStateShelved
}

var workStateAliases = map[string]string{
	"todo": WorkStateTodo, "pending": WorkStateTodo, "待办": WorkStateTodo, "待处理": WorkStateTodo,
	"doing": WorkStateDoing, "in_progress": WorkStateDoing, "inprogress": WorkStateDoing, "进行中": WorkStateDoing, "进行": WorkStateDoing,
	"review": WorkStateReview, "验证中": WorkStateReview, "验证": WorkStateReview, "待确认": WorkStateReview, "待验收": WorkStateReview,
	"done": WorkStateDone, "completed": WorkStateDone, "complete": WorkStateDone, "已完成": WorkStateDone, "完成": WorkStateDone,
	"shelved": WorkStateShelved, "blocked": WorkStateShelved, "paused": WorkStateShelved, "已搁置": WorkStateShelved, "搁置": WorkStateShelved,
}

// NormalizeWorkState 把状态规整成标准 id（接受中文名与常见英文别名）；无法识别时返回空串。
func NormalizeWorkState(s string) string {
	return workStateAliases[strings.ToLower(strings.TrimSpace(s))]
}

var workKindAliases = map[string]string{
	"bug": WorkKindBug, "defect": WorkKindBug, "fix": WorkKindBug, "缺陷": WorkKindBug,
	"feature": WorkKindFeature, "feat": WorkKindFeature, "功能": WorkKindFeature,
	"improve": WorkKindImprove, "improvement": WorkKindImprove, "enhancement": WorkKindImprove, "refactor": WorkKindImprove, "改进": WorkKindImprove,
	"research": WorkKindResearch, "design": WorkKindResearch, "plan": WorkKindResearch, "调研": WorkKindResearch, "方案": WorkKindResearch, "调研与方案": WorkKindResearch,
	"ops": WorkKindOps, "运维": WorkKindOps, "devops": WorkKindOps,
}

// NormalizeWorkKind 把类型规整成标准 id（接受中文名与常见英文别名）；无法识别时返回空串。
func NormalizeWorkKind(s string) string {
	return workKindAliases[strings.ToLower(strings.TrimSpace(s))]
}

var workStageAliases = map[string]string{
	"assess": WorkStageAssess, "assessment": WorkStageAssess, "evaluate": WorkStageAssess, "evaluation": WorkStageAssess, "discuss": WorkStageAssess,
	"评估": WorkStageAssess, "调研": WorkStageAssess, "讨论": WorkStageAssess, "分析": WorkStageAssess,
	"plan": WorkStagePlan, "design": WorkStagePlan, "方案": WorkStagePlan, "设计": WorkStagePlan, "规划": WorkStagePlan,
	"dev": WorkStageDev, "develop": WorkStageDev, "development": WorkStageDev, "implement": WorkStageDev, "implementation": WorkStageDev, "build": WorkStageDev,
	"开发": WorkStageDev, "实现": WorkStageDev, "实施": WorkStageDev,
	"improve": WorkStageImprove, "improvement": WorkStageImprove, "iterate": WorkStageImprove, "iteration": WorkStageImprove, "optimize": WorkStageImprove, "polish": WorkStageImprove,
	"改进": WorkStageImprove, "优化": WorkStageImprove, "迭代": WorkStageImprove, "完善": WorkStageImprove,
}

// NormalizeWorkStage 把阶段规整成标准 id（接受中文名与常见英文别名）；无法识别时返回空串。
// 空串本身也表示「未标注」，调用方需要区分「清除」与「无法识别」时先判断输入是否为空。
func NormalizeWorkStage(s string) string {
	return workStageAliases[strings.ToLower(strings.TrimSpace(s))]
}
