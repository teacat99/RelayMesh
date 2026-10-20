package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"

	"github.com/teacat99/RelayMesh/v2/internal/model"
	"github.com/teacat99/RelayMesh/v2/internal/store"
)

// task_board：任务清单的「看板」入口，给智能体三件 workflow_context(task_*) 做不好的事：
//
//   - overview：一次看清全局（各状态数量、模块规划树与进度、正在做 / 验证中的任务）。清单为空时返回一份
//     手把手的用法而不是空列表——空清单恰恰是最需要提示的时候；
//   - plan：一次声明整棵模块规划树（幂等、原子）。任务只能归入已存在的模块路径，所以标注永远统一；
//   - batch：一次调用成批新建 / 更新任务，逐条回执，单条失败不拖累其余；还可以带上 modules，先规划再建任务。
//
// 规则（状态流转、「已完成」必须带结论、长度上限、模块与进度校验）全部由 store 层执行，这里只做参数翻译与回执整形。

const (
	taskBoardMaxOps       = 30
	taskBoardProgressMax  = 160 // 列表 / 概览里 progress 的截断长度（完整内容用 task_get）
	taskBoardDescMax      = 120 // 列表 / 概览 / 回执里 description 的截断长度（全文用 task_get）
	taskBoardFocusMax     = 10
	taskBoardTodoPreview  = 5
	taskBoardAssignedMax  = 10 // 概览里「指派给你 / 等你验收」各最多列几条（其余用 list assignee=me 看）
	taskBoardUnassignedIn = "未归类"
)

// looseInt 容忍模型把整数写成字符串：12、"12"、"#12"、"60%"、12.0 都能读成整数。
type looseInt int

func (f *looseInt) UnmarshalJSON(b []byte) error {
	s := strings.TrimSpace(string(b))
	if s == "null" {
		return nil
	}
	if strings.HasPrefix(s, `"`) {
		var str string
		if err := json.Unmarshal(b, &str); err != nil {
			return err
		}
		s = strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(strings.TrimSpace(str), "#"), "%"))
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		fl, ferr := strconv.ParseFloat(s, 64)
		if ferr != nil || fl != math.Trunc(fl) || math.Abs(fl) > 1e9 {
			return fmt.Errorf("需要整数，收到 %s", string(b))
		}
		n = int(fl)
	}
	*f = looseInt(n)
	return nil
}

func looseIntPtr(f *looseInt) *int {
	if f == nil {
		return nil
	}
	v := int(*f)
	return &v
}

// taskBoardNode 是 plan / batch 里的一个规划节点：顶层是模块，features 里是它的功能。
type taskBoardNode struct {
	Name        string          `json:"name"`
	Description *string         `json:"description,omitempty"`
	Rename      string          `json:"rename,omitempty"`
	Remove      bool            `json:"remove,omitempty"`
	Features    []taskBoardNode `json:"features,omitempty"`
}

// taskBoardOp 是 batch 里的一条操作：没有 task_id / task_ids 是新建，有则是更新。
type taskBoardOp struct {
	TaskID       *looseInt  `json:"task_id,omitempty"`
	TaskIDs      []looseInt `json:"task_ids,omitempty"`
	Title        string     `json:"title,omitempty"`
	Kind         string     `json:"kind,omitempty"`
	Stage        *string    `json:"stage,omitempty"` // assess / plan / dev / improve；更新时传 "" 清除（回到未标注）
	State        string     `json:"state,omitempty"`
	Module       *string    `json:"module,omitempty"` // 模块路径；更新时传 "" 取消归类
	Percent      *looseInt  `json:"percent,omitempty"`
	Description  *string    `json:"description,omitempty"` // 任务内容说明；更新时传 "" 清除
	Progress     *string    `json:"progress,omitempty"`
	Conclusion   *string    `json:"conclusion,omitempty"`
	Note         string     `json:"note,omitempty"`
	Labels       *[]string  `json:"labels,omitempty"`
	Refs         *[]string  `json:"refs,omitempty"`
	ParentTaskID *looseInt  `json:"parent_task_id,omitempty"`
	// 共享任务板的协作（#175 B2）：Assignee 指派（"" 撤销），Claim 是 accept / decline / deliver / accept_delivery / reject_delivery。
	Assignee *string `json:"assignee,omitempty"`
	Claim    string  `json:"claim,omitempty"`
}

type taskBoardArgs struct {
	Action        string          `json:"action,omitempty"`
	WorkflowID    string          `json:"workflow_id"`
	SessionID     string          `json:"session_id,omitempty"`
	Module        string          `json:"module,omitempty"`
	States        []string        `json:"states,omitempty"`
	Kind          string          `json:"kind,omitempty"`
	Query         string          `json:"query,omitempty"`
	Sort          string          `json:"sort,omitempty"`
	Limit         looseInt        `json:"limit,omitempty"`
	IncludeClosed bool            `json:"include_closed,omitempty"`
	Modules       []taskBoardNode `json:"modules,omitempty"`
	Reorder       bool            `json:"reorder,omitempty"`
	Ops           []taskBoardOp   `json:"ops,omitempty"`
	Assignee      string          `json:"assignee,omitempty"` // list：只要指派给这个工作流的（me / none / 工作流编号）
}

// taskBoardHowTo 是空清单时 overview 回给智能体的用法：每一步都带着可以直接照抄的参数。
var taskBoardHowTo = []string{
	`1) 先想清楚怎么分模块，再动手：模块 = 这个项目里稳定的功能域 / 子系统（看一眼目录结构、README、设计文档），再加 2–3 条横切的工作线（如「发布与运维」「质量与测试」「文档与待办」）；功能 = 模块里的具体能力。不要按「第 N 轮 / 第 N 批」、日期、会话、负责人或阶段（评估、开发、测试）划分，也不要建「其他 / 杂项」——这些几周后就会失效，清单会变成流水账；`,
	`2) action=plan 一次定义 3–8 个模块（可带功能），例如 modules=[{"name":"终端","features":[{"name":"历史日志"},{"name":"输入法"}]},{"name":"任务"},{"name":"发布与运维"}]；`,
	`3) action=batch 一次建好任务，module 填已有路径，例如 ops=[{"title":"历史日志平铺","description":"终端历史日志按会话平铺展示，不再逐条折叠；做到打开会话即可滚动查看全部历史。","module":"终端/历史日志","kind":"feature","state":"doing"}]（也可以在同一次调用里带上 modules：先规划、再建任务）；每个任务都写 description（这件事是什么、做到什么算完成，一小段话，用户在 Web 任务页和弹窗里靠它看懂任务），标题只是名字；一个任务 = 一件做完能验证的事，同一件事的评估 → 方案 → 开发不要拆成几个任务；`,
	`4) 推进时用 batch 成批更新：{"task_ids":[1,2,3],"state":"review"}（review = 验证中），或 {"task_id":4,"progress":"做到哪、下一步","percent":60}；验证通过后置 done 要带 conclusion：{"task_id":4,"state":"done","conclusion":"做了什么、怎么验证的"}；`,
	`5) 状态绑定你自己的开发流程：做完先置 review（验证中：回归 / e2e / 冒烟在跑），验证全部通过后由你自己置 done，conclusion 必填（没写结论的 done 会被拒绝，新建任务也不能直接是 done）；用户在 Web「任务」页实时看着这份清单，发现问题可以退回或发起改进需求；`,
	`6) 之后每次开工、上下文被重置，先 overview：新任务在已有路径里找归属，确实没有合适的功能再用 plan 追加；少于 3 步的小事不用建任务。`,
}

// taskBoardMaintain 是清单已经有结构时 overview 附带的维护守则：首次规划之后最容易走样的是「后续使用」，
// 所以每次回到清单（开工、上下文重置）都把最要紧的三条再说一遍。
var taskBoardMaintain = []string{
	"新任务先在上面的模块树里找归属，module 写完整路径（如 终端/历史日志）；确实没有合适的功能，才用 plan 追加一个功能，不要因为一次对话就新建模块。",
	"模块是项目稳定的功能域，不是轮次 / 日期 / 会话 / 阶段；用户一次提的多件事各归各的功能域，不要按「这一轮」打包成父任务。",
	"同一件事的评估 → 方案 → 开发是同一个任务在推进（state / stage / progress），不要拆成几个任务；做完写 conclusion 并推进到 review。",
}

func (s *Server) handleTaskBoard(ctx context.Context, raw json.RawMessage) (any, error) {
	var args taskBoardArgs
	if err := json.Unmarshal(raw, &args); err != nil {
		return nil, fmt.Errorf("invalid arguments: %w", err)
	}
	args.WorkflowID = strings.TrimSpace(args.WorkflowID)
	if args.WorkflowID == "" {
		return nil, fmt.Errorf("workflow_id is required for task_board (use the workflow_id from the receipt header)")
	}
	action := strings.ToLower(strings.TrimSpace(args.Action))
	if action == "" {
		action = "overview"
	}
	switch action {
	case "overview":
		return s.taskBoardOverview(ctx, args)
	case "list":
		return s.taskBoardList(ctx, args)
	case "plan":
		return s.taskBoardPlan(ctx, args)
	case "batch":
		return s.taskBoardBatch(ctx, args)
	}
	return nil, fmt.Errorf("unknown action %q for task_board; valid actions: overview (default), list, plan, batch", args.Action)
}

// notifyWorkBoard 通知前端重新取数：规划树变了（modules）和 / 或有一批任务变了（items，不带具体编号）。
func (s *Server) notifyWorkBoard(workflowID string, modules, items bool) {
	if s.onUpdate == nil {
		return
	}
	// 同一个项目的工作流共用一张任务板：事件带上 board_key，前端据此判断是不是自己正在看的那张板（#175）。
	boardKey, _ := s.store.BoardKeyOf(context.Background(), workflowID)
	if modules {
		s.onUpdate("work_module_updated", map[string]any{"workflow_id": workflowID, "board_key": boardKey})
	}
	if items {
		s.onUpdate("work_item_updated", map[string]any{"workflow_id": workflowID, "board_key": boardKey, "bulk": true})
	}
}

// workErrMessage 取业务错误里写给智能体看的那句话；其余错误笼统报告。
func workErrMessage(err error) string {
	var ae *store.AppError
	if errors.As(err, &ae) {
		return ae.Message
	}
	return "操作失败"
}

func trimTo(s string, max int) string {
	s = strings.TrimSpace(s)
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max]) + "…"
}

// openWorkStates 是「未结束」的三个状态。
var openWorkStates = []string{model.WorkStateTodo, model.WorkStateDoing, model.WorkStateReview}

func countsJSON(counts map[string]int) map[string]int {
	out := map[string]int{}
	total := 0
	for _, st := range model.WorkStates {
		out[st] = counts[st]
		total += counts[st]
	}
	out["open"] = counts[model.WorkStateTodo] + counts[model.WorkStateDoing] + counts[model.WorkStateReview]
	out["total"] = total
	return out
}

// slimTask 是列表 / 概览里的任务形状：够定位进度就行，完整内容用 task_get。
func slimTask(it *model.WorkItem, viewer string) map[string]any {
	m := map[string]any{
		"ref":        fmt.Sprintf("#%d", it.Seq),
		"title":      it.Title,
		"kind":       it.Kind,
		"state":      it.State,
		"percent":    it.ProgressPct,
		"updated_at": it.UpdatedAt.Format("2006-01-02 15:04"),
	}
	if it.Stage != "" {
		m["stage"] = it.Stage
	}
	if it.ModulePath != "" {
		m["module"] = it.ModulePath
	}
	if d := trimTo(it.Description, taskBoardDescMax); d != "" {
		m["description"] = d
	}
	if p := trimTo(it.Progress, taskBoardProgressMax); p != "" {
		m["progress"] = p
	}
	if it.ParentID != nil && it.ParentSeq > 0 {
		m["parent"] = fmt.Sprintf("#%d", it.ParentSeq)
	}
	if it.ChildTotal > 0 {
		m["children"] = fmt.Sprintf("%d/%d", it.ChildDone, it.ChildTotal)
	}
	// 共享板：任务不是调用方创建时带创建者；已指派时带承接者 / 接取状态 / 派发者（没有指派的任务形状不变）。
	if viewer != "" && it.WorkflowID != "" && it.WorkflowID != viewer {
		m["workflow"] = it.WorkflowID
	}
	if it.AssigneeWorkflowID != "" {
		m["assignee"] = it.AssigneeWorkflowID
		m["claim"] = it.ClaimState
		by := it.AssignedByWorkflowID
		if by == "" {
			by = "human"
		}
		m["assigned_by"] = by
	}
	return m
}

func slimTasks(items []model.WorkItem, viewer string) []map[string]any {
	out := make([]map[string]any, 0, len(items))
	for i := range items {
		out = append(out, slimTask(&items[i], viewer))
	}
	return out
}

// moduleTreeJSON 把拍平的规划树还原成「模块 → features」两层结构，带任务汇总。
func moduleTreeJSON(list *store.WorkModuleList) []map[string]any {
	out := make([]map[string]any, 0)
	var cur map[string]any
	for _, m := range list.Modules {
		node := map[string]any{"path": m.Path, "open": m.Stats.Open(), "done": m.Stats.Done, "total": m.Stats.Total, "percent": m.Stats.Percent}
		if m.Description != "" {
			node["description"] = m.Description
		}
		if m.Depth <= 1 {
			out = append(out, node)
			cur = node
			continue
		}
		if cur != nil {
			feats, _ := cur["features"].([]map[string]any)
			cur["features"] = append(feats, node)
		}
	}
	return out
}

func toPlanNodes(in []taskBoardNode) []store.WorkPlanNode {
	out := make([]store.WorkPlanNode, 0, len(in))
	for _, n := range in {
		out = append(out, store.WorkPlanNode{
			Name: n.Name, Description: n.Description, Rename: n.Rename, Remove: n.Remove, Features: toPlanNodes(n.Features),
		})
	}
	return out
}

func planResultJSON(r *store.WorkPlanResult) map[string]any {
	out := map[string]any{"changed": r.Changed()}
	if len(r.Created) > 0 {
		out["created"] = r.Created
	}
	if len(r.Renamed) > 0 {
		out["renamed"] = r.Renamed
	}
	if len(r.Updated) > 0 {
		out["updated"] = r.Updated
	}
	if len(r.Removed) > 0 {
		out["removed"] = r.Removed
	}
	if r.Reordered {
		out["reordered"] = true
	}
	if r.Unchanged > 0 {
		out["unchanged"] = r.Unchanged
	}
	return out
}

// ---------------------------------------------------------------- overview

func (s *Server) taskBoardOverview(ctx context.Context, args taskBoardArgs) (any, error) {
	wf := args.WorkflowID
	digest, err := s.store.WorkDigestFor(ctx, wf)
	if err != nil {
		return nil, workToolError("task_board overview", err)
	}
	if digest.Total == 0 && digest.Modules == 0 {
		return map[string]any{
			"workflow_id": wf,
			"empty":       true,
			"counts":      countsJSON(digest.Counts),
			"message":     "这个工作流还没有任务清单（也没有模块规划）。清单是给你自己与用户共用的进度看板：用户在 Web「任务」页能实时看到。按下面的步骤建立：",
			"how_to":      taskBoardHowTo,
		}, nil
	}
	list, err := s.store.ListWorkModules(ctx, wf)
	if err != nil {
		return nil, workToolError("task_board overview", err)
	}
	out := map[string]any{
		"workflow_id": wf,
		"empty":       false,
		"counts":      countsJSON(digest.Counts),
		"modules":     moduleTreeJSON(list),
	}
	if list.Unassigned.Total > 0 {
		out["unassigned"] = map[string]int{"open": list.Unassigned.Open(), "total": list.Unassigned.Total}
	}
	focus, err := s.store.ListWorkItems(ctx, store.WorkItemFilter{WorkflowID: wf, States: []string{model.WorkStateDoing, model.WorkStateReview}, Limit: 50})
	if err != nil {
		return nil, workToolError("task_board overview", err)
	}
	if len(focus) > taskBoardFocusMax {
		focus = focus[:taskBoardFocusMax]
	}
	out["focus"] = slimTasks(focus, wf)
	todo, err := s.store.ListWorkItems(ctx, store.WorkItemFilter{WorkflowID: wf, States: []string{model.WorkStateTodo}, Limit: taskBoardTodoPreview})
	if err != nil {
		return nil, workToolError("task_board overview", err)
	}
	if len(todo) > 0 {
		out["todo_preview"] = slimTasks(todo, wf)
		if n := digest.Counts[model.WorkStateTodo]; n > len(todo) {
			out["todo_more"] = n - len(todo)
		}
	}
	s.addCollaboration(ctx, wf, out)
	switch {
	case digest.Modules == 0:
		out["hint"] = `还没有模块规划，任务都在「未归类」。先想清楚怎么分：模块 = 项目稳定的功能域 / 子系统（外加发布运维、质量测试、文档待办这类横切工作线），不按轮次、日期、会话、阶段分；然后用 action=plan 建 3–8 个模块，再用 batch 把任务成批归类，例如 ops=[{"task_ids":[1,2,3],"module":"终端"}]`
	case digest.Total == 0:
		out["hint"] = `已有模块规划但还没有任务：用 action=batch 一次建好任务，module 填上面的路径`
	}
	if digest.Modules > 0 {
		// 清单已经有结构：把维护守则和结构上的毛病一起交给智能体，让后续使用不走样。
		out["maintain"] = taskBoardMaintain
		if warnings := boardStructureWarnings(list); len(warnings) > 0 {
			out["warnings"] = warnings
		}
	}
	return out, nil
}

// addCollaboration 在概览里补「指派给你 / 你派出的 / 等你验收」（#175 B2）：只在共享板上真的有协作指派时才出现，
// 单工作流的板与没有指派的板，概览与以前完全一致。自己给自己的任务不算（那是你自己的活）。
func (s *Server) addCollaboration(ctx context.Context, wf string, out map[string]any) {
	if mine, err := s.store.ListWorkItems(ctx, store.WorkItemFilter{WorkflowID: wf, Assignee: wf, States: openWorkStates, Limit: 100}); err == nil {
		var toMe []model.WorkItem
		offered := 0
		for _, it := range mine {
			if !it.IsCollaborative() || it.ClaimState == model.WorkClaimDeclined {
				continue
			}
			toMe = append(toMe, it)
			if it.ClaimState == model.WorkClaimOffered {
				offered++
			}
		}
		if len(toMe) > 0 {
			sort.SliceStable(toMe, func(i, j int) bool { // 待接取优先，其次进行中 > 验证中 > 待办
				oi, oj := toMe[i].ClaimState == model.WorkClaimOffered, toMe[j].ClaimState == model.WorkClaimOffered
				if oi != oj {
					return oi
				}
				return workListRank[toMe[i].State] < workListRank[toMe[j].State]
			})
			more := len(toMe) - taskBoardAssignedMax
			if more > 0 {
				toMe = toMe[:taskBoardAssignedMax]
				out["assigned_to_you_more"] = more
			}
			out["assigned_to_you"] = slimTasks(toMe, wf)
			if offered > 0 {
				out["assigned_to_you_hint"] = fmt.Sprintf("有 %d 个新指派待接取：task_update {task_id, claim:\"accept\"} 接取（todo → doing），或 claim:\"decline\" + note 写理由拒绝；直接把状态改成 doing 也算接取", offered)
			}
		}
	}
	dispatched, err := s.store.ListWorkItems(ctx, store.WorkItemFilter{WorkflowID: wf, AssignedBy: wf, States: openWorkStates, Limit: 200})
	if err != nil {
		return
	}
	counts := map[string]int{}
	var awaiting []model.WorkItem
	for _, it := range dispatched {
		if !it.IsCollaborative() {
			continue
		}
		counts[it.ClaimState]++
		if it.State == model.WorkStateReview {
			awaiting = append(awaiting, it)
		}
	}
	if len(counts) == 0 {
		return
	}
	summary := map[string]int{}
	for k, v := range counts {
		summary[k] = v
	}
	summary["awaiting_acceptance"] = len(awaiting)
	out["assigned_by_you"] = summary
	if len(awaiting) > 0 {
		if len(awaiting) > taskBoardAssignedMax {
			awaiting = awaiting[:taskBoardAssignedMax]
		}
		out["awaiting_your_acceptance"] = slimTasks(awaiting, wf)
		out["awaiting_your_acceptance_hint"] = "这些是你派出、承接者已交付待验收的任务：task_update {task_id, claim:\"accept_delivery\"}（done，需结论）验收，或 claim:\"reject_delivery\" + note 写原因退回"
	}
}

// ---------------------------------------------------------------- list

func (s *Server) taskBoardList(ctx context.Context, args taskBoardArgs) (any, error) {
	wf := args.WorkflowID
	states := args.States
	if len(states) == 0 {
		if args.IncludeClosed {
			states = model.WorkStates
		} else {
			states = openWorkStates
		}
	}
	f := store.WorkItemFilter{WorkflowID: wf, States: states, Query: args.Query, Limit: 500}
	if args.Kind != "" {
		f.Kinds = []string{args.Kind}
	}
	if strings.TrimSpace(args.Assignee) != "" {
		f.Assignee = assigneeFilter(args.Assignee, wf)
	}
	moduleLabel := strings.TrimSpace(args.Module)
	if moduleLabel != "" {
		switch strings.ToLower(moduleLabel) {
		case taskBoardUnassignedIn, "unassigned", "(unassigned)", "none":
			f.Unassigned = true
		default:
			m, err := s.store.ResolveWorkModule(ctx, wf, moduleLabel)
			if err != nil {
				return nil, workToolError("task_board list", err)
			}
			f.ModuleID = m.ID
			moduleLabel = m.Path
		}
	}
	sortMode := strings.ToLower(strings.TrimSpace(args.Sort))
	if sortMode != "recent" {
		sortMode = "plan"
	}
	items, err := s.store.ListWorkItems(ctx, f)
	if err != nil {
		return nil, workToolError("task_board list", err)
	}
	if sortMode == "recent" {
		sort.SliceStable(items, func(i, j int) bool { return items[i].UpdatedAt.After(items[j].UpdatedAt) })
	} else {
		order := map[uint]int{}
		if list, err := s.store.ListWorkModules(ctx, wf); err == nil {
			for i, m := range list.Modules {
				order[m.ID] = i
			}
		}
		pos := func(it *model.WorkItem) int {
			if it.ModuleID == nil {
				return math.MaxInt32 // 未归类排最后
			}
			if p, ok := order[*it.ModuleID]; ok {
				return p
			}
			return math.MaxInt32
		}
		sort.SliceStable(items, func(i, j int) bool {
			pi, pj := pos(&items[i]), pos(&items[j])
			if pi != pj {
				return pi < pj
			}
			ri, rj := workListRank[items[i].State], workListRank[items[j].State]
			if ri != rj {
				return ri < rj
			}
			return items[i].UpdatedAt.After(items[j].UpdatedAt)
		})
	}
	limit := int(args.Limit)
	if limit <= 0 {
		limit = workListDefaultLimit
	}
	if limit > workListMaxLimit {
		limit = workListMaxLimit
	}
	total := len(items)
	if len(items) > limit {
		items = items[:limit]
	}
	counts, _ := s.store.CountBoardWorkItemsByState(ctx, wf) // 与清单同口径：整张任务板
	out := map[string]any{"tasks": slimTasks(items, wf), "count": len(items), "total": total, "sort": sortMode, "counts_by_state": countsJSON(counts)}
	if moduleLabel != "" {
		out["module"] = moduleLabel
	}
	if total > len(items) {
		out["truncated"] = true
		out["hint"] = fmt.Sprintf("只返回前 %d 个，共 %d 个；用 module / states / kind / query 缩小范围，或调大 limit（最多 %d）", len(items), total, workListMaxLimit)
	}
	return out, nil
}

// ---------------------------------------------------------------- plan

func (s *Server) taskBoardPlan(ctx context.Context, args taskBoardArgs) (any, error) {
	wf := args.WorkflowID
	if len(args.Modules) == 0 {
		return nil, fmt.Errorf(`modules is required for task_board plan, e.g. modules=[{"name":"终端","description":"可选","features":[{"name":"历史日志"}]}]`)
	}
	res, err := s.store.ApplyWorkPlan(ctx, wf, toPlanNodes(args.Modules), args.Reorder)
	if err != nil {
		return nil, workToolError("task_board plan（整份计划都没有生效）", err)
	}
	if res.Changed() {
		s.notifyWorkBoard(wf, true, false)
	}
	list, err := s.store.ListWorkModules(ctx, wf)
	if err != nil {
		return nil, workToolError("task_board plan", err)
	}
	out := planResultJSON(res)
	if res.Changed() {
		out["status"] = "applied"
	} else {
		out["status"] = "unchanged"
	}
	if warnings := newNodeWarnings(res.Created); len(warnings) > 0 {
		out["warnings"] = warnings
	}
	out["tree"] = moduleTreeJSON(list)
	return out, nil
}

// ---------------------------------------------------------------- batch

// moduleResolver 在一次 batch 里缓存模块路径的解析结果；同一个写错的路径只完整报错一次，免得清单每条都带一长串路径。
type moduleResolver struct {
	s      *Server
	ctx    context.Context
	wf     string
	cache  map[string]*model.WorkModule
	failed map[string]bool
}

func (r *moduleResolver) resolve(ref string) (*model.WorkModule, error) {
	key := strings.TrimSpace(ref)
	if m, ok := r.cache[key]; ok {
		return m, nil
	}
	if r.failed[key] {
		return nil, store.NewInvalidInputError(fmt.Sprintf("模块「%s」不存在（同上，已有路径见前面的报错；也可以先调用 action=overview 查看）", key))
	}
	m, err := r.s.store.ResolveWorkModule(r.ctx, r.wf, key)
	if err != nil {
		r.failed[key] = true
		return nil, err
	}
	r.cache[key] = m
	return m, nil
}

// moduleID 把 op.Module 翻成模块主键：nil = 不动；"" = 取消归类；其余按路径解析。
func (r *moduleResolver) moduleID(ref *string) (*uint, error) {
	if ref == nil {
		return nil, nil
	}
	var id uint
	if strings.TrimSpace(*ref) != "" {
		m, err := r.resolve(*ref)
		if err != nil {
			return nil, err
		}
		id = m.ID
	}
	return &id, nil
}

func (s *Server) findSameOpenTask(ctx context.Context, wf, title string, moduleID uint) *model.WorkItem {
	items, err := s.store.ListWorkItems(ctx, store.WorkItemFilter{WorkflowID: wf, Query: title, States: openWorkStates, Limit: 50})
	if err != nil {
		return nil
	}
	for i := range items {
		it := &items[i]
		if !strings.EqualFold(strings.TrimSpace(it.Title), title) {
			continue
		}
		var mid uint
		if it.ModuleID != nil {
			mid = *it.ModuleID
		}
		if mid == moduleID {
			return it
		}
	}
	return nil
}

func batchEntry(i int, it *model.WorkItem, action string) map[string]any {
	e := map[string]any{"i": i, "ok": true, "action": action, "ref": fmt.Sprintf("#%d", it.Seq), "state": it.State, "percent": it.ProgressPct}
	if action == "created" || action == "exists" {
		e["title"] = trimTo(it.Title, 60)
	}
	if it.Stage != "" {
		e["stage"] = it.Stage
	}
	if it.ModulePath != "" {
		e["module"] = it.ModulePath
	}
	if it.AssigneeWorkflowID != "" {
		e["assignee"] = it.AssigneeWorkflowID
		e["claim"] = it.ClaimState
	}
	return e
}

func batchFailure(i int, ref string, err error) map[string]any {
	e := map[string]any{"i": i, "ok": false, "error": workErrMessage(err)}
	if ref != "" {
		e["ref"] = ref
	}
	return e
}

func (s *Server) taskBoardBatch(ctx context.Context, args taskBoardArgs) (any, error) {
	wf := args.WorkflowID
	if len(args.Ops) == 0 && len(args.Modules) == 0 {
		return nil, fmt.Errorf(`ops is required for task_board batch, e.g. ops=[{"title":"新任务","module":"终端"},{"task_ids":[1,2],"state":"review"}]`)
	}
	if len(args.Ops) > taskBoardMaxOps {
		return nil, fmt.Errorf("一次最多 %d 个操作，收到 %d 个；请分批调用", taskBoardMaxOps, len(args.Ops))
	}
	out := map[string]any{}
	modulesChanged := false
	var planWarnings []string
	// 先规划：整份计划原子生效，失败时一条任务也不会动。
	if len(args.Modules) > 0 {
		res, err := s.store.ApplyWorkPlan(ctx, wf, toPlanNodes(args.Modules), args.Reorder)
		if err != nil {
			return nil, workToolError("task_board batch（modules 规划失败，ops 都没有执行）", err)
		}
		modulesChanged = res.Changed()
		out["plan"] = planResultJSON(res)
		planWarnings = newNodeWarnings(res.Created)
	}

	pol := s.newWorkPolicy(ctx, wf, args.SessionID)
	actor := pol.actor
	var policyHints []string // audit 档下被记了违规的写入：在回执里提醒一次（不阻止），去重
	addHint := func(res *store.WorkItemResult) {
		if h := pol.hint(res); h != "" {
			for _, old := range policyHints {
				if old == h {
					return
				}
			}
			policyHints = append(policyHints, h)
		}
	}
	resolver := &moduleResolver{s: s, ctx: ctx, wf: wf, cache: map[string]*model.WorkModule{}, failed: map[string]bool{}}
	results := make([]map[string]any, 0, len(args.Ops))
	var created, updated, unchanged, exists, failed int
	itemsChanged := false
	// 结构体检要用的两项：新建任务里没写 module 的数量，以及以轮次 / 批次开头的标题。
	createdNoModule := 0
	var roundTitles []string

	for i, op := range args.Ops {
		ids := make([]int, 0, len(op.TaskIDs)+1)
		if op.TaskID != nil {
			ids = append(ids, int(*op.TaskID))
		}
		for _, id := range op.TaskIDs {
			ids = append(ids, int(id))
		}
		percent := looseIntPtr(op.Percent)

		mid, err := resolver.moduleID(op.Module)
		if err != nil {
			results = append(results, batchFailure(i, "", err))
			failed++
			continue
		}
		var parentSeq int
		if op.ParentTaskID != nil {
			parentSeq = int(*op.ParentTaskID)
		}

		if len(ids) == 0 { // 新建
			title := strings.TrimSpace(op.Title)
			if title == "" {
				results = append(results, batchFailure(i, "", store.NewInvalidInputError("新建任务需要 title（要更新已有任务请给 task_id 或 task_ids）")))
				failed++
				continue
			}
			parent, err := s.parentIDBySeq(ctx, wf, parentSeq)
			if err != nil {
				results = append(results, batchFailure(i, "", err))
				failed++
				continue
			}
			var moduleID uint
			if mid != nil {
				moduleID = *mid
			}
			if dup := s.findSameOpenTask(ctx, wf, title, moduleID); dup != nil {
				e := batchEntry(i, dup, "exists")
				e["note"] = "同模块下已有同名的未结束任务，没有重复创建；要修改它请用 task_id"
				results = append(results, e)
				exists++
				continue
			}
			in := store.WorkItemInput{WorkflowID: wf, Title: title, Kind: op.Kind, State: op.State, Note: op.Note, ParentID: parent, ModuleID: moduleID, Percent: percent}
			if op.Stage != nil {
				in.Stage = *op.Stage
			}
			if op.Labels != nil {
				in.Labels = *op.Labels
			}
			if op.Refs != nil {
				in.Refs = *op.Refs
			}
			if op.Description != nil {
				in.Description = *op.Description
			}
			if op.Progress != nil {
				in.Progress = *op.Progress
			}
			if op.Assignee != nil {
				in.Assignee = *op.Assignee
			}
			res, err := s.store.CreateWorkItem(ctx, in, actor)
			pol.note(ctx, "task_board", "batch", 0, res, err)
			if err != nil {
				results = append(results, batchFailure(i, "", err))
				failed++
				continue
			}
			addHint(res)
			e := batchEntry(i, res.Item, "created")
			results = append(results, e)
			created++
			itemsChanged = true
			if moduleID == 0 {
				createdNoModule++
			}
			if isRoundTitle(title) {
				roundTitles = append(roundTitles, trimTo(title, 24))
			}
			continue
		}

		// 更新：同一条操作可以对多个任务套用同一个改动。
		if len(ids) > 1 && strings.TrimSpace(op.Title) != "" {
			results = append(results, batchFailure(i, "", store.NewInvalidInputError("title 只能用于单个任务的改名，不能同时套用到多个任务")))
			failed++
			continue
		}
		patch := store.WorkItemPatch{Note: op.Note, Labels: op.Labels, Refs: op.Refs, Description: op.Description, Progress: op.Progress, Conclusion: op.Conclusion, Percent: percent, ModuleID: mid, Stage: op.Stage, Assignee: op.Assignee, Claim: op.Claim}
		if t := strings.TrimSpace(op.Title); t != "" {
			patch.Title = &t
		}
		if op.Kind != "" {
			k := op.Kind
			patch.Kind = &k
		}
		if op.State != "" {
			st := op.State
			patch.State = &st
		}
		if op.ParentTaskID != nil {
			pid, err := s.parentIDBySeq(ctx, wf, parentSeq)
			if err != nil {
				results = append(results, batchFailure(i, "", err))
				failed++
				continue
			}
			patch.ParentID = &pid
		}
		for _, seq := range ids {
			ref := fmt.Sprintf("#%d", seq)
			cur, err := s.store.GetWorkItemBySeq(ctx, wf, seq)
			if err != nil {
				results = append(results, batchFailure(i, ref, err))
				failed++
				continue
			}
			res, err := s.store.UpdateWorkItem(ctx, cur.ID, patch, actor)
			pol.note(ctx, "task_board", "batch", seq, res, err)
			if err != nil {
				results = append(results, batchFailure(i, ref, err))
				failed++
				continue
			}
			addHint(res)
			action := "unchanged"
			if res.Changed {
				action = "updated"
				updated++
				itemsChanged = true
			} else {
				unchanged++
			}
			e := batchEntry(i, res.Item, action)
			results = append(results, e)
		}
	}

	if modulesChanged || itemsChanged {
		s.notifyWorkBoard(wf, modulesChanged, itemsChanged)
	}
	status := "ok"
	switch {
	case failed > 0 && created+updated+unchanged+exists == 0 && len(args.Ops) > 0:
		status = "failed"
	case failed > 0:
		status = "partial"
	}
	out["status"] = status
	out["summary"] = map[string]int{"created": created, "updated": updated, "unchanged": unchanged, "exists": exists, "failed": failed}
	out["results"] = results
	if warnings := append(planWarnings, batchWarnings(createdNoModule, roundTitles)...); len(warnings) > 0 {
		out["warnings"] = warnings
	}
	if len(policyHints) > 0 {
		out["policy_hint"] = strings.Join(policyHints, " ｜ ")
	}
	return out, nil
}

// ---------------------------------------------------------------- 回执头里的任务概况

// taskDigestHeader 生成回执头里的 tasks 行：让上下文被重置、或根本没读过工具说明的模型，
// 一眼知道这个工作流有没有任务清单、做到哪了、该用哪个工具。清单为空时它就是那句提醒。
func (s *Server) taskDigestHeader(ctx context.Context, workflowID string) string {
	d, err := s.store.WorkDigestFor(ctx, workflowID)
	if err != nil || d == nil {
		return ""
	}
	switch {
	case d.Total == 0 && d.Modules == 0:
		return "tasks: 本工作流还没有任务清单——多步（≥3 步）或跨会话的工作，请先调用 task_board（action=overview 看用法 → plan 按项目功能域建模块，不按轮次 / 阶段分 → batch 建任务），用户在 Web「任务」页能实时看到"
	case d.Total == 0:
		return fmt.Sprintf("tasks: 已规划 %d 个模块，还没有任务（task_board action=batch 一次建好）", d.Modules)
	}
	var parts []string
	for _, p := range []struct{ state, label string }{
		{model.WorkStateDoing, "进行中"}, {model.WorkStateReview, "验证中"}, {model.WorkStateTodo, "待办"},
	} {
		if n := d.Counts[p.state]; n > 0 {
			parts = append(parts, fmt.Sprintf("%s %d", p.label, n))
		}
	}
	line := "tasks: "
	if len(parts) == 0 {
		line += "无未结束任务"
	} else {
		line += strings.Join(parts, " · ")
	}
	line += fmt.Sprintf("（共 %d，已完成 %d）", d.Total, d.Counts[model.WorkStateDone])
	if it := d.Latest; it != nil {
		stateLabel := map[string]string{model.WorkStateDoing: "进行中", model.WorkStateReview: "验证中", model.WorkStateTodo: "待办"}[it.State]
		last := fmt.Sprintf(" ｜ 最近 #%d %s %d%% %s", it.Seq, stateLabel, it.ProgressPct, trimTo(it.Title, 24))
		if it.ModulePath != "" {
			last += "〔" + it.ModulePath + "〕"
		}
		line += last
	}
	// 结构上的提醒：回执头每次都会被读到，是后续使用时最便宜的纠偏位置。
	switch {
	case d.Modules == 0 && d.Total > 0:
		line += " ｜ 还没有模块规划：先 task_board action=plan 按项目功能域建 3–8 个模块，再 batch 归类"
	case d.Unassigned > 0:
		line += fmt.Sprintf(" ｜ 未归类 %d 条：用 task_board batch 归入模块", d.Unassigned)
	}
	return line + " ｜ task_board 查看 / 成批更新"
}
