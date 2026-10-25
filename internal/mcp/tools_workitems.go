package mcp

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/teacat99/RelayMesh/v2/internal/model"
	"github.com/teacat99/RelayMesh/v2/internal/store"
)

// workflow_context 的 task_* 动作：智能体维护独立于会话的任务清单。
//
// 智能体用工作流内的序号（#12 → task_id=12）引用任务；规则（「已完成」必须带结论、进度与结论的长度上限、
// 引用与标签的数量上限、父任务只许一层）全部由 store 层执行，这里只做参数翻译与回执整形。

const (
	workListDefaultLimit = 30
	workListMaxLimit     = 100
)

// workToolError 把 store 的业务错误原样交给智能体（信息是写给它看的：缺什么、超了多少），其余错误笼统报告。
func workToolError(action string, err error) error {
	var ae *store.AppError
	if errors.As(err, &ae) {
		return fmt.Errorf("%s: %s", action, ae.Message)
	}
	return fmt.Errorf("%s 失败", action)
}

func (s *Server) notifyWorkItem(item *model.WorkItem, deleted bool) {
	if s.onUpdate != nil && item != nil {
		s.onUpdate("work_item_updated", map[string]any{"id": item.ID, "workflow_id": item.WorkflowID, "board_key": item.BoardKey, "deleted": deleted})
	}
}

// compactTask 是回执里的任务形状：只放智能体决策需要的字段，内部主键不外露（用 task_id 引用）。
func compactTask(it *model.WorkItem) map[string]any {
	m := map[string]any{
		"task_id":    it.Seq,
		"ref":        fmt.Sprintf("#%d", it.Seq),
		"title":      it.Title,
		"kind":       it.Kind,
		"state":      it.State,
		"percent":    it.ProgressPct, // 进度条上的值：显式 percent，否则由子任务 / 状态推导
		"labels":     it.Labels,
		"progress":   it.Progress,
		"conclusion": it.Conclusion,
		"refs":       it.Refs,
		"created_by": it.CreatedBy,
		"created_at": it.CreatedAt,
		"updated_at": it.UpdatedAt,
	}
	// 描述在列表 / 回执里只给开头几十个字（够认出是哪件事），全文用 task_get。
	if d := trimTo(it.Description, taskBoardDescMax); d != "" {
		m["description"] = d
	}
	if it.Stage != "" {
		m["stage"] = it.Stage
	}
	if it.ModulePath != "" {
		m["module"] = it.ModulePath
	}
	if it.ClosedAt != nil {
		m["closed_at"] = *it.ClosedAt
	}
	if it.ParentID != nil && it.ParentSeq > 0 {
		m["parent_task_id"] = it.ParentSeq
	}
	if it.ChildTotal > 0 {
		m["children"] = map[string]int{"total": it.ChildTotal, "done": it.ChildDone}
	}
	if it.SessionNum > 0 {
		m["session_count"] = it.SessionNum
	}
	// 共享任务板的协作（#175 B2）：只在已指派时才带，没有指派的任务形状与以前完全一致。
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

// compactTaskFor 在 compactTask 的基础上补「创建者」：任务不是 viewer（调用方自己的工作流）创建时多一个 workflow，
// 单工作流的板上永远等于 viewer，所以不会多出任何字段。
func compactTaskFor(it *model.WorkItem, viewer string) map[string]any {
	m := compactTask(it)
	if viewer != "" && it.WorkflowID != "" && it.WorkflowID != viewer {
		m["workflow"] = it.WorkflowID
	}
	return m
}

func detailedTask(it *model.WorkItem, viewer string) map[string]any {
	m := compactTaskFor(it, viewer)
	if it.Description != "" {
		m["description"] = it.Description // task_get 给全文
	}
	if it.AssignedAt != nil {
		m["assigned_at"] = *it.AssignedAt
	}
	if it.AcceptedAt != nil {
		m["accepted_at"] = *it.AcceptedAt
	}
	timeline := make([]map[string]any, 0, len(it.Updates))
	for _, u := range it.Updates {
		e := map[string]any{"at": u.CreatedAt, "by": u.ActorKind}
		// 操作者的工作流：只在不是调用方自己时才标出来（自己的操作不必自报家门，单工作流的板上时间线形状不变）。
		if u.ActorWorkflowID != "" && u.ActorWorkflowID != viewer {
			e["workflow"] = u.ActorWorkflowID
		}
		if u.Event != "" {
			e["event"] = u.Event
		}
		if u.FromState != "" || u.ToState != "" {
			e["from"], e["to"] = u.FromState, u.ToState
		}
		if u.Text != "" {
			e["text"] = u.Text
		}
		if u.SessionID != "" {
			e["session_id"] = u.SessionID
		}
		timeline = append(timeline, e)
	}
	m["timeline"] = timeline
	sessions := make([]map[string]any, 0, len(it.Sessions))
	for _, ls := range it.Sessions {
		e := map[string]any{"session_id": ls.SessionID}
		if ls.Title != "" {
			e["title"] = ls.Title
		}
		sessions = append(sessions, e)
	}
	m["sessions"] = sessions
	if it.SourceSessionID != "" {
		m["source_session_id"] = it.SourceSessionID
	}
	return m
}

// parentIDBySeq 把智能体给的父任务序号换成内部主键；序号为 0 表示没有父任务。
func (s *Server) parentIDBySeq(ctx context.Context, workflowID string, seq int) (uint, error) {
	if seq <= 0 {
		return 0, nil
	}
	p, err := s.store.GetWorkItemBySeq(ctx, workflowID, seq)
	if err != nil {
		return 0, err
	}
	return p.ID, nil
}

// assigneeFilter 把 task_list / task_board list 的 assignee 过滤值换算成 store 能懂的：me = 调用方自己，none = 未指派。
func assigneeFilter(v, self string) string {
	switch t := strings.TrimSpace(v); strings.ToLower(t) {
	case "me", "self":
		return self
	case "none", "unassigned", "未指派":
		return "none"
	default:
		return t
	}
}

func (s *Server) handleTaskCreate(ctx context.Context, args workflowContextArgs) (any, error) {
	if strings.TrimSpace(args.WorkflowID) == "" {
		return nil, fmt.Errorf("workflow_id is required for task_create")
	}
	if strings.TrimSpace(args.Title) == "" {
		return nil, fmt.Errorf("title is required for task_create")
	}
	var parentSeq int
	if args.ParentTaskID != nil {
		parentSeq = *args.ParentTaskID
	}
	parent, err := s.parentIDBySeq(ctx, args.WorkflowID, parentSeq)
	if err != nil {
		return nil, workToolError("task_create", err)
	}
	in := store.WorkItemInput{
		WorkflowID: args.WorkflowID, Title: args.Title, Kind: args.Kind, State: args.State,
		Note: args.Note, ParentID: parent, Percent: looseIntPtr(args.Percent),
	}
	if args.Stage != nil {
		in.Stage = *args.Stage
	}
	if args.Module != nil && strings.TrimSpace(*args.Module) != "" {
		m, err := s.store.ResolveWorkModule(ctx, args.WorkflowID, *args.Module)
		if err != nil {
			return nil, workToolError("task_create", err)
		}
		in.ModuleID = m.ID
	}
	if args.Labels != nil {
		in.Labels = *args.Labels
	}
	if args.Refs != nil {
		in.Refs = *args.Refs
	}
	if args.Description != nil {
		in.Description = *args.Description
	}
	if args.Progress != nil {
		in.Progress = *args.Progress
	}
	if args.Assignee != nil {
		in.Assignee = *args.Assignee
	}
	pol := s.newWorkPolicy(ctx, args.WorkflowID, args.SessionID)
	res, err := s.store.CreateWorkItem(ctx, in, pol.actor)
	pol.note(ctx, "workflow_context", "task_create", 0, res, err)
	if err != nil {
		return nil, workToolError("task_create", err)
	}
	s.notifyWorkItem(res.Item, false)
	s.postBoardLines(ctx, res)
	out := map[string]any{"status": "created", "task": compactTaskFor(res.Item, args.WorkflowID)}
	if h := pol.hint(res); h != "" {
		out["policy_hint"] = h
	}
	if h := s.branchAssignHint(ctx, args.WorkflowID, res); h != "" {
		out["branch_hint"] = h
	}
	if strings.TrimSpace(args.SessionID) != "" {
		out["session_linked"] = res.SessionLinked
	}
	return out, nil
}

func (s *Server) handleTaskUpdate(ctx context.Context, args workflowContextArgs) (any, error) {
	if strings.TrimSpace(args.WorkflowID) == "" {
		return nil, fmt.Errorf("workflow_id is required for task_update")
	}
	if args.TaskID <= 0 {
		return nil, fmt.Errorf("task_id is required for task_update (the number after # in the task list)")
	}
	cur, err := s.store.GetWorkItemBySeq(ctx, args.WorkflowID, args.TaskID)
	if err != nil {
		return nil, workToolError("task_update", err)
	}
	patch := store.WorkItemPatch{Note: args.Note, Refs: args.Refs, Labels: args.Labels, Description: args.Description, Progress: args.Progress, Conclusion: args.Conclusion, Percent: looseIntPtr(args.Percent), Stage: args.Stage, Assignee: args.Assignee, Claim: args.Claim}
	if args.Module != nil {
		var mid uint // "" = 取消归类
		if strings.TrimSpace(*args.Module) != "" {
			m, err := s.store.ResolveWorkModule(ctx, args.WorkflowID, *args.Module)
			if err != nil {
				return nil, workToolError("task_update", err)
			}
			mid = m.ID
		}
		patch.ModuleID = &mid
	}
	if args.Title != "" {
		t := args.Title
		patch.Title = &t
	}
	if args.Kind != "" {
		k := args.Kind
		patch.Kind = &k
	}
	if args.State != "" {
		st := args.State
		patch.State = &st
	}
	if args.ParentTaskID != nil {
		pid, err := s.parentIDBySeq(ctx, args.WorkflowID, *args.ParentTaskID)
		if err != nil {
			return nil, workToolError("task_update", err)
		}
		patch.ParentID = &pid
	}
	pol := s.newWorkPolicy(ctx, args.WorkflowID, args.SessionID)
	res, err := s.store.UpdateWorkItem(ctx, cur.ID, patch, pol.actor)
	pol.note(ctx, "workflow_context", "task_update", args.TaskID, res, err)
	if err != nil {
		return nil, workToolError("task_update", err)
	}
	status := "unchanged"
	if res.Changed {
		status = "updated"
		s.notifyWorkItem(res.Item, false)
		s.postBoardLines(ctx, res)
	}
	out := map[string]any{"status": status, "task": compactTaskFor(res.Item, args.WorkflowID)}
	if h := pol.hint(res); h != "" {
		out["policy_hint"] = h
	}
	if h := s.branchAssignHint(ctx, args.WorkflowID, res); h != "" {
		out["branch_hint"] = h
	}
	if strings.TrimSpace(args.SessionID) != "" {
		out["session_linked"] = res.SessionLinked
	}
	return out, nil
}

func (s *Server) handleTaskGet(ctx context.Context, args workflowContextArgs) (any, error) {
	if strings.TrimSpace(args.WorkflowID) == "" {
		return nil, fmt.Errorf("workflow_id is required for task_get")
	}
	if args.TaskID <= 0 {
		return nil, fmt.Errorf("task_id is required for task_get")
	}
	it, err := s.store.GetWorkItemBySeq(ctx, args.WorkflowID, args.TaskID)
	if err != nil {
		return nil, workToolError("task_get", err)
	}
	return map[string]any{"task": detailedTask(it, args.WorkflowID)}, nil
}

// workListRank 决定 task_list 的排序：正在做的最前，其次验证中，再是待办；已关闭的靠后。
var workListRank = map[string]int{
	model.WorkStateDoing: 0, model.WorkStateReview: 1, model.WorkStateTodo: 2, model.WorkStateShelved: 3, model.WorkStateDone: 4,
}

func (s *Server) handleTaskList(ctx context.Context, args workflowContextArgs) (any, error) {
	if strings.TrimSpace(args.WorkflowID) == "" {
		return nil, fmt.Errorf("workflow_id is required for task_list")
	}
	states := args.States
	if len(states) == 0 {
		if args.IncludeClosed {
			states = model.WorkStates
		} else {
			states = []string{model.WorkStateTodo, model.WorkStateDoing, model.WorkStateReview}
		}
	}
	f := store.WorkItemFilter{WorkflowID: args.WorkflowID, States: states, Query: args.Query, Limit: 500}
	if args.Kind != "" {
		f.Kinds = []string{args.Kind}
	}
	if args.Assignee != nil {
		f.Assignee = assigneeFilter(*args.Assignee, args.WorkflowID)
	}
	items, err := s.store.ListWorkItems(ctx, f)
	if err != nil {
		return nil, workToolError("task_list", err)
	}
	sort.SliceStable(items, func(i, j int) bool {
		ri, rj := workListRank[items[i].State], workListRank[items[j].State]
		if ri != rj {
			return ri < rj
		}
		return items[i].UpdatedAt.After(items[j].UpdatedAt)
	})
	limit := args.Limit
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
	tasks := make([]map[string]any, 0, len(items))
	for i := range items {
		tasks = append(tasks, compactTaskFor(&items[i], args.WorkflowID))
	}
	counts, _ := s.store.CountBoardWorkItemsByState(ctx, args.WorkflowID) // 与清单同口径：整张任务板
	out := map[string]any{"tasks": tasks, "count": len(tasks), "total": total, "counts_by_state": counts}
	if total > len(tasks) {
		out["truncated"] = true
		out["hint"] = fmt.Sprintf("只返回前 %d 个，共 %d 个；用 states / kind / query 缩小范围，或调大 limit（最多 %d）", len(tasks), total, workListMaxLimit)
	}
	return out, nil
}
