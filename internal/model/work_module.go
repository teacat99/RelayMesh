package model

import (
	"strings"
	"time"
)

// 任务的「模块规划」：每个工作流（项目线程）有一棵最多两层的规划树——模块 → 功能。
//
// 任务只回答「这件事做到哪了」，模块规划回答「这件事属于哪块」：给任务标上统一的模块路径（终端/历史日志），
// 界面左侧的树、列表的分组顺序、智能体的批量标注都以它为准。模块名只接受已存在的路径，所以不会出现
// 「终端 / Terminal / 终端会话」三种写法并存的情况。
//
// 规则（由 store 层统一执行）：
//   - 深度 ≤ 2；名称一行、不含 `/`、同级不区分大小写不重名；
//   - 每个工作流 ≤ WorkModulesMax 个顶层模块，每个模块下 ≤ WorkFeaturesMax 个功能；
//   - 删除模块不删任务：任务回到被删节点的上一级（顶层模块则变成「未归类」），被删模块的功能一并删除。

const (
	WorkModuleNameMax = 40
	WorkModuleDescMax = 200
	// WorkModulesMax 每个工作流最多的顶层模块数。
	WorkModulesMax = 30
	// WorkFeaturesMax 每个顶层模块下最多的功能数。
	WorkFeaturesMax = 30
	// WorkModuleDepthMax 规划树的最大深度（模块 + 功能）。
	WorkModuleDepthMax = 2
	// WorkModulePathSep 模块路径的分隔符。
	WorkModulePathSep = "/"
)

// WorkModule 是规划树上的一个节点。ParentID 为空的是顶层「模块」，否则是它下面的「功能」。
type WorkModule struct {
	ID         uint   `gorm:"primaryKey;autoIncrement" json:"id"`
	WorkflowID string `gorm:"size:128;not null;index" json:"workflow_id"`
	// BoardKey 是规划树所在的板（#175），同一张板里的各工作流共用一棵树；WorkflowID 是创建它的工作流。
	BoardKey    string `gorm:"size:160;not null;default:'';index" json:"board_key"`
	ParentID    *uint  `gorm:"index" json:"parent_id,omitempty"`
	Name        string `gorm:"size:128;not null" json:"name"`
	Description string `gorm:"size:512" json:"description"`
	// Sort 是同级内的顺序（小的在前）；每次写入后会被规整成 0..n-1。
	Sort      int       `gorm:"not null;default:0" json:"sort"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`

	// 以下字段不落库，由查询按需填充。
	Path  string          `gorm:"-" json:"path"`
	Depth int             `gorm:"-" json:"depth"`
	Stats WorkModuleStats `gorm:"-" json:"stats"`
}

// WorkModuleStats 是节点上任务的汇总（顶层模块包含它下面各功能的任务）。
// 只统计「叶子任务」（没有子任务的任务），批次任务本身不重复计入。
type WorkModuleStats struct {
	Total   int `json:"total"`
	Todo    int `json:"todo"`
	Doing   int `json:"doing"`
	Review  int `json:"review"`
	Done    int `json:"done"`
	Shelved int `json:"shelved"`
	// Percent 是未搁置任务有效进度（见 EffectivePercent）的平均值；没有任务时为 0。
	Percent int `json:"percent"`
}

// Open 是尚未结束的任务数（待办 + 进行中 + 验证中）。
func (s WorkModuleStats) Open() int { return s.Todo + s.Doing + s.Review }

// JoinModulePath 把模块名与功能名拼成路径。
func JoinModulePath(parts ...string) string { return strings.Join(parts, WorkModulePathSep) }

// SplitModulePath 把用户 / 智能体给的路径拆成各段：按 `/` 分段，去掉每段两端的空白与空段。
// 同时容忍 `>`、`＞`、`／` 这几种常见写法。
func SplitModulePath(path string) []string {
	r := strings.NewReplacer(">", WorkModulePathSep, "＞", WorkModulePathSep, "／", WorkModulePathSep, "›", WorkModulePathSep, "→", WorkModulePathSep)
	raw := strings.Split(r.Replace(path), WorkModulePathSep)
	out := make([]string, 0, len(raw))
	for _, seg := range raw {
		if s := strings.TrimSpace(seg); s != "" {
			out = append(out, s)
		}
	}
	return out
}

// EffectivePercent 是任务进度条上的取值（0–100）：
//
//	已完成 = 100；显式写过 percent 的用它；有子任务的按完成比例；否则由状态推导（待办 0 / 进行中 50 / 验证中 90 / 已搁置 0）。
//
// derived 说明数值的来源：explicit（显式）/ children（子任务比例）/ state（状态推导，只适合画条、不适合当数字展示）。
func EffectivePercent(state string, percent *int, childTotal, childDone int) (value int, derived string) {
	switch {
	case state == WorkStateDone:
		return 100, "state"
	case percent != nil:
		return ClampPercent(*percent), "explicit"
	case childTotal > 0:
		return ClampPercent((childDone*100 + childTotal/2) / childTotal), "children"
	}
	switch state {
	case WorkStateDoing:
		return 50, "state"
	case WorkStateReview:
		return 90, "state"
	}
	return 0, "state"
}

// ClampPercent 把进度限制在 0–100。
func ClampPercent(v int) int {
	if v < 0 {
		return 0
	}
	if v > 100 {
		return 100
	}
	return v
}
