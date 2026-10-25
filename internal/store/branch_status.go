package store

import (
	"context"
	"database/sql"
	"sort"
	"strings"
	"time"

	"github.com/teacat99/RelayMesh/v2/internal/model"
)

// 分支概况里的「状态」几项（#236 P4 / P5）：Agent 状态、名下任务数、启动方式，以及派单（「任务」开没开、是否同在一张项目板上）。
//
// 它们和「最近活动 / 待答数」一样恒向主会话开放，不受向主会话开放的范围控制：主会话要靠它们判断「分支此刻在干什么、要不要唤醒或介入」。
// 全部是现有数据的只读汇总（状态跟踪器、任务板、启动预设），没有新增表与列；只给活动关系填，已结束的关系不填。

// BranchAgentStateUnknown 是分支还没有任何智能体会话（hook 从未汇报过）时的状态。
const BranchAgentStateUnknown = "unknown"

// BranchAgentStateStalled 是分支的智能体在工作却久无动静（卡住）时的状态：与网页状态芯片的优先级一致，卡住盖过其他展示态。
const BranchAgentStateStalled = "stalled"

// branchStateEvalMax 是一次 BranchBriefs 最多给多少个活动分支算 Agent 状态：全部关系的列表可以有几百行，
// 每个分支的状态要查一遍会话与身份，超出的不填（界面本来也只在主会话 / 分支各自的面板里用，每个主会话最多 MaxActiveBranches 个）。
const branchStateEvalMax = 50

// BranchTasks 是分支名下还没做完的任务数，口径与收尾门（gateWhere）一致：待办 / 进行中数承接者、验证中数验收者。
// 三项全为 0 时整个字段不输出。
type BranchTasks struct {
	Todo   int `json:"todo,omitempty"`
	Doing  int `json:"doing,omitempty"`
	Review int `json:"review,omitempty"`
}

func (t BranchTasks) total() int { return t.Todo + t.Doing + t.Review }

// BranchLaunch 是分支的「启动方式」：用户有没有在开放范围里打开「启动」，以及有几条非交互续聊预设能唤醒它。
// 「此刻能不能运行」（主机在线、上一次运行有没有结束……）不在这里：它随时在变，要看 preset_run 的 list / status，或网页「运行」窗里的检查。
type BranchLaunch struct {
	Open    bool   `json:"open"`
	Presets int    `json:"presets"`
	Preset  string `json:"preset,omitempty"` // 按名字排在最前的那条预设的编号
	Name    string `json:"name,omitempty"`
}

// BranchAssign 是主会话能不能把任务派给这个分支（#236 P5）：用户有没有在开放范围里打开「任务」= 派单，
// 以及两边是不是在同一张项目板上——派单要求承接者与任务在同一张项目板，不同板时指派会被拒绝，原因要讲清楚。
// 板读不出来时整个字段不输出（不假装「不在同一张板上」）。
type BranchAssign struct {
	Open        bool `json:"open"`
	BoardShared bool `json:"board_shared"`
}

// branchAssignOf 算一行的派单概况；cache 是「工作流 → 任务板」，同一个主会话的几行共用一次查询。
func (s *Store) branchAssignOf(ctx context.Context, b *BranchBrief, cache map[string]string) *BranchAssign {
	key := func(wf string) (string, bool) {
		if k, ok := cache[wf]; ok {
			return k, true
		}
		k, err := s.BoardKeyOf(ctx, wf)
		if err != nil {
			return "", false
		}
		cache[wf] = k
		return k, true
	}
	pk, okParent := key(b.ParentWorkflowID)
	bk, okBranch := key(b.BranchWorkflowID)
	if !okParent || !okBranch {
		return nil
	}
	return &BranchAssign{Open: b.Expose.Tasks == model.ExposeTasksAssign, BoardShared: pk == bk && isProjectBoard(pk)}
}

// openTaskCounts 数一个工作流名下没做完的任务，按状态分。
func (s *Store) openTaskCounts(ctx context.Context, wf string) (BranchTasks, error) {
	type row struct {
		State string
		N     int
	}
	var rows []row
	if err := s.db.WithContext(ctx).Model(&model.WorkItem{}).Where(gateWhere, sql.Named("wf", wf)).
		Select("state, COUNT(*) AS n").Group("state").Scan(&rows).Error; err != nil {
		return BranchTasks{}, err
	}
	var t BranchTasks
	for _, r := range rows {
		switch r.State {
		case model.WorkStateTodo:
			t.Todo = r.N
		case model.WorkStateDoing:
			t.Doing = r.N
		case model.WorkStateReview:
			t.Review = r.N
		}
	}
	return t, nil
}

// pickBranchSession 从按最近动静排序的会话里挑出代表这个工作流的那一个，规则与网页 pickSession 一致：
// 最近有动静的活动会话（没结束、没离线）；都不活动就取没结束里最近的；再不行取最近一个。
func pickBranchSession(list []AgentSessionView) *AgentSessionView {
	if len(list) == 0 {
		return nil
	}
	for i := range list {
		if st := list[i].State; st != "ended" && st != "offline" && st != "" {
			return &list[i]
		}
	}
	for i := range list {
		if st := list[i].State; st != "ended" && st != "" {
			return &list[i]
		}
	}
	return &list[0]
}

// branchAgentState 取一个工作流此刻的 Agent 状态与它开始的时间：没有会话是 unknown；查询出错也按 unknown（状态是辅助信息，不该让整个概况失败）。
func (s *Store) branchAgentState(ctx context.Context, wf string, now time.Time) (string, *time.Time) {
	list, err := s.ListAgentSessions(ctx, AgentSessionFilter{WorkflowID: wf, Limit: 20}, now)
	if err != nil {
		return BranchAgentStateUnknown, nil
	}
	p := pickBranchSession(list)
	if p == nil {
		return BranchAgentStateUnknown, nil
	}
	state := p.State
	if p.Stalled {
		state = BranchAgentStateStalled
	}
	if state == "" {
		state = BranchAgentStateUnknown
	}
	return state, tptr(p.Since)
}

// launchPresetsByWorkflow 把非交互续聊预设按目标工作流归类（每个工作流里按名字排序）。读不出来返回 nil：调用方据此不输出 launch，
// 而不是输出「0 条预设」——后者是在说一件没查证过的事。
func (s *Store) launchPresetsByWorkflow(ctx context.Context) map[string][]TerminalLauncher {
	all, err := s.ListTerminalLaunchers(ctx)
	if err != nil {
		return nil
	}
	out := map[string][]TerminalLauncher{}
	for _, p := range all {
		if !p.IsHeadless() || p.Run == nil {
			continue
		}
		if wf := strings.TrimSpace(p.Run.WorkflowID); wf != "" {
			out[wf] = append(out[wf], p)
		}
	}
	for _, l := range out {
		sort.SliceStable(l, func(i, j int) bool { return strings.ToLower(l[i].Name) < strings.ToLower(l[j].Name) })
	}
	return out
}

// fillBranchStatus 给活动关系补上 Agent 状态、名下任务数与启动方式（见本文件开头的说明）。已结束的关系一律不填。
func (s *Store) fillBranchStatus(ctx context.Context, briefs []BranchBrief) {
	var active []int
	for i := range briefs {
		if briefs[i].Active() {
			active = append(active, i)
		}
	}
	if len(active) == 0 {
		return
	}
	now := time.Now()
	presets := s.launchPresetsByWorkflow(ctx)
	boards := map[string]string{}
	for n, i := range active {
		b := &briefs[i]
		wf := b.BranchWorkflowID
		if n < branchStateEvalMax {
			b.AgentState, b.AgentStateAt = s.branchAgentState(ctx, wf, now)
			b.Assign = s.branchAssignOf(ctx, b, boards)
		}
		if t, err := s.openTaskCounts(ctx, wf); err == nil && t.total() > 0 {
			b.Tasks = &t
		}
		if presets != nil {
			l := &BranchLaunch{Open: b.Expose.Launch, Presets: len(presets[wf])}
			if l.Presets > 0 {
				l.Preset, l.Name = presets[wf][0].ID, presets[wf][0].Name
			}
			b.Launch = l
		}
	}
}
