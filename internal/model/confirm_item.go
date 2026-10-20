package model

import (
	"encoding/json"
	"strconv"
	"strings"
	"time"
)

// 确认项（ConfirmItem）：智能体的异步提问、计划确认、文档决策卡等「需要人确认」的事项的统一载体。
// 序号 Seq 按工作流单调递增且永不复用，界面与收件箱里写成 Q12；与任务 #12 是两套编号。
//
// 状态机：pending → answered（用户作答）/ dismissed（用户放弃）/ withdrawn（来源撤回：智能体撤回、围栏被删、文档归档）/
// expired（文档决策卡的选项变了，旧答复作废）。除 pending 外都是终态；终态不会回到 pending，
// 需要重新确认时（例如围栏被重新加回）会新建一条（新的 Seq）。

const (
	ConfirmKindAsk      = "ask"      // 智能体异步提问（async_ask）
	ConfirmKindPlan     = "plan"     // 计划确认：绑定任务，批准后再动手
	ConfirmKindDecision = "decision" // 文档里的 ```decision 围栏

	ConfirmStatusPending   = "pending"
	ConfirmStatusAnswered  = "answered"
	ConfirmStatusDismissed = "dismissed"
	ConfirmStatusWithdrawn = "withdrawn"
	ConfirmStatusExpired   = "expired"

	// 风险等级：L0 只读 / 可随时回滚；L1 可回滚的改动；L2 不可逆或高风险（只能阻塞式确认，批量入口不处理）。
	ConfirmRiskL0 = "L0"
	ConfirmRiskL1 = "L1"
	ConfirmRiskL2 = "L2"

	ConfirmCreatedByAgent = "agent"
	ConfirmCreatedByDoc   = "doc"

	ConfirmTitleMax   = 200
	ConfirmBodyMax    = 20000
	ConfirmNoteMax    = 2000
	ConfirmCustomMax  = 2000 // 自定义答复（#183）的字数上限，与备注同一口径
	ConfirmBatchMax   = 50
	ConfirmOptionsMin = 2
	ConfirmOptionsMax = 8

	// 每个工作流每小时最多通过 async_ask 创建的条数；超过时提示合并提问。
	ConfirmCreateRateHour = 30
	// 同一时刻待处理超过这个数，async_ask 的返回里会提示合并提问。
	ConfirmPendingWarn = 10

	// 收件箱里确认项答复的消息种类（QueuedFeedback.Kind）。
	InboxKindAnswer = "answer"
)

// ConfirmOption 是确认项里的一个可选项。
type ConfirmOption struct {
	ID          string `json:"id"`
	Label       string `json:"label"`
	Detail      string `json:"detail,omitempty"`
	Recommended bool   `json:"recommended,omitempty"`
}

// ConfirmAnswer 记录一次作答（选项 id 列表 + 可选备注 + 可选的自定义答复 + 作答人来源）。
//
// Custom 是用户觉得「预设选项都不合适」时自己写的答复（#183）：单选时与选项互斥（OptionIDs 为空），
// 多选时可以与选中的选项并存；备注 Note 仍然是对所选内容的补充，两者互不替代。
// 老答复没有这个字段（JSON 里缺省即为空），界面与智能体都按「没有自定义答复」处理。
type ConfirmAnswer struct {
	OptionIDs []string `json:"options"`
	Note      string   `json:"note,omitempty"`
	Custom    string   `json:"custom,omitempty"`
	By        string   `json:"by"` // 目前只有 human（Web 登录用户）
}

type ConfirmItem struct {
	ID         uint   `gorm:"primaryKey" json:"id"`
	WorkflowID string `gorm:"size:128;not null;uniqueIndex:idx_confirm_wf_seq,priority:1;index:idx_confirm_wf_created,priority:1" json:"workflow_id"`
	Seq        int    `gorm:"not null;uniqueIndex:idx_confirm_wf_seq,priority:2" json:"seq"`

	Kind  string `gorm:"size:16;not null;index" json:"kind"`
	Title string `gorm:"size:200;not null" json:"title"`
	Body  string `gorm:"type:text" json:"body,omitempty"`

	OptionsJSON   string `gorm:"type:text;not null" json:"-"`
	Multi         bool   `gorm:"not null;default:false" json:"multi"`
	AllowNote     bool   `gorm:"not null;default:false" json:"allow_note"`
	DefaultOption string `gorm:"size:64" json:"default_option,omitempty"`
	Risk          string `gorm:"size:4;not null;default:'L1';index" json:"risk"`

	TaskSeq   int    `gorm:"not null;default:0;index" json:"task_seq,omitempty"`
	DocID     uint   `gorm:"not null;default:0;index" json:"doc_id,omitempty"`
	DocKey    string `gorm:"size:64;index" json:"doc_key,omitempty"`
	Anchor    string `gorm:"size:200" json:"anchor,omitempty"`
	OptionsFP string `gorm:"size:64;index" json:"options_fp,omitempty"` // 选项指纹：文档决策卡选项是否变化的判据

	Status      string     `gorm:"size:16;not null;default:'pending';index" json:"status"`
	AnswerJSON  string     `gorm:"type:text" json:"-"`
	AnsweredAt  *time.Time `json:"answered_at,omitempty"`
	DeliveredAt *time.Time `json:"delivered_at,omitempty"` // 答复被智能体读到的时间（预留；当前由收件箱状态承载）

	CreatedBy string    `gorm:"size:16;not null" json:"created_by"`
	CreatedAt time.Time `gorm:"index:idx_confirm_wf_created,priority:2" json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// ConfirmItemCounter 保证工作流内确认项序号不复用（即使中间的项被删除）。
type ConfirmItemCounter struct {
	WorkflowID string `gorm:"primaryKey;size:128" json:"workflow_id"`
	LastSeq    int    `gorm:"not null;default:0" json:"last_seq"`
}

// ConfirmItemView 是对外（REST / MCP）的确认项视图：选项与答复已解码，绑定的文档带路径与标题。
type ConfirmItemView struct {
	ID            uint            `json:"id"`
	WorkflowID    string          `json:"workflow_id"`
	Seq           int             `json:"seq"`
	Ref           string          `json:"ref"`
	Kind          string          `json:"kind"`
	Title         string          `json:"title"`
	Body          string          `json:"body,omitempty"`
	Options       []ConfirmOption `json:"options"`
	Multi         bool            `json:"multi"`
	AllowNote     bool            `json:"allow_note"`
	DefaultOption string          `json:"default_option,omitempty"`
	Risk          string          `json:"risk"`
	TaskSeq       int             `json:"task_seq,omitempty"`
	DocID         uint            `json:"doc_id,omitempty"`
	DocKey        string          `json:"doc_key,omitempty"`
	DocPath       string          `json:"doc_path,omitempty"`
	DocTitle      string          `json:"doc_title,omitempty"`
	Anchor        string          `json:"anchor,omitempty"`
	Status        string          `json:"status"`
	Answer        *ConfirmAnswer  `json:"answer,omitempty"`
	AnsweredAt    *time.Time      `json:"answered_at,omitempty"`
	CreatedBy     string          `json:"created_by"`
	CreatedAt     time.Time       `json:"created_at"`
	UpdatedAt     time.Time       `json:"updated_at"`
}

// NewConfirmItemView 把库里的行转成对外视图（选项 JSON 损坏时返回空选项，不让一行脏数据拖垮整个列表）。
func NewConfirmItemView(item *ConfirmItem) ConfirmItemView {
	v := ConfirmItemView{
		ID: item.ID, WorkflowID: item.WorkflowID, Seq: item.Seq, Ref: ConfirmRef(item.Seq),
		Kind: item.Kind, Title: item.Title, Body: item.Body,
		Multi: item.Multi, AllowNote: item.AllowNote, DefaultOption: item.DefaultOption, Risk: item.Risk,
		TaskSeq: item.TaskSeq, DocID: item.DocID, DocKey: item.DocKey, Anchor: item.Anchor,
		Status: item.Status, AnsweredAt: item.AnsweredAt,
		CreatedBy: item.CreatedBy, CreatedAt: item.CreatedAt, UpdatedAt: item.UpdatedAt,
		Options: []ConfirmOption{},
	}
	if strings.TrimSpace(item.OptionsJSON) != "" {
		var opts []ConfirmOption
		if json.Unmarshal([]byte(item.OptionsJSON), &opts) == nil && opts != nil {
			v.Options = opts
		}
	}
	if strings.TrimSpace(item.AnswerJSON) != "" {
		var a ConfirmAnswer
		if json.Unmarshal([]byte(item.AnswerJSON), &a) == nil {
			v.Answer = &a
		}
	}
	return v
}

// NormalizeConfirmRisk 把任意输入规整成 L0 / L1 / L2；不认识的按 L1（中间档）处理。
func NormalizeConfirmRisk(s string) string {
	switch strings.ToUpper(strings.TrimSpace(s)) {
	case ConfirmRiskL0:
		return ConfirmRiskL0
	case ConfirmRiskL2:
		return ConfirmRiskL2
	default:
		return ConfirmRiskL1
	}
}

// ConfirmRef 把序号写成界面与收件箱里用的 Q12；序号非法时返回空串。
func ConfirmRef(seq int) string {
	if seq <= 0 {
		return ""
	}
	return "Q" + strconv.Itoa(seq)
}
