package store

import (
	"context"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/teacat99/RelayMesh/v2/internal/model"
)

// 回执头里的 board: 行的数据（#180 B3a，方案稿 .cursor/planning/2026-10-22-180-shared-board-b3-plan.md）。
//
// 共享任务板上，承接者只有主动调 task_board overview 才知道有新指派，派发者也只有调 overview 才知道对方交付了；
// 生产实测「指派 → 接取」的等待中位数约 102 分钟。回执头每次都会被读到，是让「醒着但不知道有活」的会话看到待办的最便宜位置。
//
// 状态式，不是一次性提示：只要下面五类里的事还没处理，每一次回执都带，处理掉就自然消失（一次性的提示会被上下文压缩
// 或一时忽略吃掉，而这几项都是一次调用就能办掉的待办）。所以不需要新表、新列——只给 assigned_by_workflow_id 补了一个索引。
//
//	承接者侧  ToAccept        指派给我、还没接取（不含自己派给自己的）
//	承接者侧  Returned        我交付的任务被退回、还没有重新交付（任务现在是 todo / doing，时间线上最近一次协作事件是「退回」）
//	派发者侧  AwaitingReview  我派出的任务已交付，等我验收（协作任务停在 review）
//	派发者侧  Declined        我派出的指派被拒绝，等我改派
//	派发者侧  Stale / StaleGone  我派出的指派超过阈值还没人接；承接者的工作流已不存在的单独算（不会再有人接了）
//
// 口径与 task_board overview 里的 assigned_to_you / awaiting_your_acceptance 一致：只算未结束（todo / doing / review）的协作任务。

const (
	// BoardDigestMaxSeqs 是每一类最多列出的任务编号个数。
	BoardDigestMaxSeqs = 5
	// BoardDigestStaleDefault 是指派派出后多久没人接算「久无人接」的默认值（B3d 起可以在设置里改）。
	BoardDigestStaleDefault = 2 * time.Hour

	boardDigestTTL      = 5 * time.Second // 进程内缓存的有效期；任务一变更就整体作废（见 workRev）
	boardDigestCacheMax = 256             // 缓存的工作流个数上限；超过就整张清掉重来
	boardDigestScanMax  = 500             // 每一侧最多扫描的指派数（板上同时未结束的协作任务远小于它）
)

// BoardDigestGroup 是一类待办：个数、最多 BoardDigestMaxSeqs 个任务编号（板内序号，按派出时间从早到晚）、
// 其中最早一条的派出时间（零值 = 没有可比的时间）。
type BoardDigestGroup struct {
	Total  int
	Seqs   []int
	Oldest time.Time
}

func (g *BoardDigestGroup) add(seq int, assignedAt *time.Time) {
	g.Total++
	if len(g.Seqs) < BoardDigestMaxSeqs {
		g.Seqs = append(g.Seqs, seq)
	}
	if assignedAt != nil && !assignedAt.IsZero() && (g.Oldest.IsZero() || assignedAt.Before(g.Oldest)) {
		g.Oldest = *assignedAt
	}
}

// BoardDigest 是某个工作流在共享任务板上「该处理的协作待办」。返回值可能来自缓存，调用方只读、不要改。
type BoardDigest struct {
	ToAccept       BoardDigestGroup
	Returned       BoardDigestGroup
	AwaitingReview BoardDigestGroup
	Declined       BoardDigestGroup
	Stale          BoardDigestGroup
	StaleGone      BoardDigestGroup
}

// Empty 表示没有任何待办——回执里就不出现 board: 行，与升级前逐字一致。
func (d *BoardDigest) Empty() bool {
	return d == nil || d.ToAccept.Total+d.Returned.Total+d.AwaitingReview.Total+d.Declined.Total+d.Stale.Total+d.StaleGone.Total == 0
}

// boardDigestCache 是回执头的进程内缓存：回执是高频路径，同一个工作流 5 秒内不重复查库；
// 任务一变更（workRev 前进）整体作废，所以接取 / 拒绝 / 交付 / 验收之后的下一次回执一定看得到新状态。
type boardDigestCache struct {
	mu sync.Mutex
	m  map[string]boardDigestEntry
}

type boardDigestEntry struct {
	rev uint64
	at  time.Time
	d   *BoardDigest
}

func (c *boardDigestCache) get(key string, rev uint64, now time.Time) (*BoardDigest, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.m[key]
	if !ok || e.rev != rev {
		return nil, false
	}
	if age := now.Sub(e.at); age < 0 || age >= boardDigestTTL {
		return nil, false
	}
	return e.d, true
}

func (c *boardDigestCache) put(key string, rev uint64, now time.Time, d *BoardDigest) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.m == nil || len(c.m) >= boardDigestCacheMax {
		c.m = make(map[string]boardDigestEntry, 16)
	}
	c.m[key] = boardDigestEntry{rev: rev, at: now, d: d}
}

// bumpWorkRev 在任务写入返回时调用（CreateWorkItem / UpdateWorkItem / DeleteWorkItem 的 defer），
// 让所有缓存的 board: 行作废。写入失败或没有变化时多作废一次是无害的。
func (s *Store) bumpWorkRev() { s.workRev.Add(1) }

// BoardDigestFor 返回工作流 workflowID 在共享任务板上该处理的协作待办。staleAfter ≤ 0 用默认值。
// 工作流编号为空、或板上没有任何协作指派时返回空的汇总（不是错误）。
func (s *Store) BoardDigestFor(ctx context.Context, workflowID string, now time.Time, staleAfter time.Duration) (*BoardDigest, error) {
	wf := strings.TrimSpace(workflowID)
	if wf == "" {
		return &BoardDigest{}, nil
	}
	if staleAfter <= 0 {
		staleAfter = BoardDigestStaleDefault
	}
	key := wf + "|" + staleAfter.String()
	// 先读版本再查库：查库期间有写入，缓存的版本就落后，下一次自然重算。
	rev := s.workRev.Load()
	if d, ok := s.boardDigest.get(key, rev, now); ok {
		return d, nil
	}
	d, err := s.computeBoardDigest(ctx, wf, now, staleAfter)
	if err != nil {
		return nil, err
	}
	s.boardDigest.put(key, rev, now, d)
	return d, nil
}

// digestOpenStates 是「未结束」的状态；已完成 / 已搁置的指派不再提醒。
var digestOpenStates = []string{model.WorkStateTodo, model.WorkStateDoing, model.WorkStateReview}

// sortByAssigned 按派出时间从早到晚排（没有时间的排最前），同一时刻按编号，保证「列哪 5 个」稳定。
func sortByAssigned(items []model.WorkItem) {
	at := func(it *model.WorkItem) time.Time {
		if it.AssignedAt == nil {
			return time.Time{}
		}
		return *it.AssignedAt
	}
	sort.SliceStable(items, func(i, j int) bool {
		ai, aj := at(&items[i]), at(&items[j])
		if !ai.Equal(aj) {
			return ai.Before(aj)
		}
		return items[i].Seq < items[j].Seq
	})
}

func (s *Store) computeBoardDigest(ctx context.Context, wf string, now time.Time, staleAfter time.Duration) (*BoardDigest, error) {
	d := &BoardDigest{}
	db := s.db.WithContext(ctx)

	// 承接者侧：指派给我、派发者不是我（自己派给自己的由自己做，不算协作）、没有拒绝、没有结束。
	var mine []model.WorkItem
	if err := db.Select("id", "seq", "state", "claim_state", "assigned_at").
		Where("assignee_workflow_id = ? AND assigned_by_workflow_id <> ? AND claim_state IN ? AND state IN ?",
			wf, wf, []string{model.WorkClaimOffered, model.WorkClaimAccepted}, digestOpenStates).
		Order("id ASC").Limit(boardDigestScanMax).Find(&mine).Error; err != nil {
		return nil, err
	}
	sortByAssigned(mine)
	var maybeReturned []uint
	for i := range mine {
		it := &mine[i]
		switch {
		case it.ClaimState == model.WorkClaimOffered:
			d.ToAccept.add(it.Seq, it.AssignedAt)
		case it.State == model.WorkStateTodo || it.State == model.WorkStateDoing:
			maybeReturned = append(maybeReturned, it.ID) // 已接取、正在做：是不是被退回过，要看时间线
		}
	}
	if len(maybeReturned) > 0 {
		returned, err := s.returnedAmong(ctx, maybeReturned)
		if err != nil {
			return nil, err
		}
		for i := range mine {
			if returned[mine[i].ID] {
				d.Returned.add(mine[i].Seq, mine[i].AssignedAt)
			}
		}
	}

	// 派发者侧：我派出的（派发者是我，承接者不是我）。
	var sent []model.WorkItem
	if err := db.Select("id", "seq", "state", "claim_state", "assignee_workflow_id", "assigned_at").
		Where("assigned_by_workflow_id = ? AND assignee_workflow_id <> '' AND assignee_workflow_id <> ? AND state IN ?",
			wf, wf, digestOpenStates).
		Order("id ASC").Limit(boardDigestScanMax).Find(&sent).Error; err != nil {
		return nil, err
	}
	sortByAssigned(sent)
	var stale []*model.WorkItem
	for i := range sent {
		it := &sent[i]
		switch {
		case it.ClaimState == model.WorkClaimDeclined:
			d.Declined.add(it.Seq, it.AssignedAt)
		case it.State == model.WorkStateReview:
			d.AwaitingReview.add(it.Seq, it.AssignedAt)
		case it.ClaimState == model.WorkClaimOffered && it.AssignedAt != nil && now.Sub(*it.AssignedAt) >= staleAfter:
			stale = append(stale, it)
		}
	}
	if len(stale) > 0 {
		ids := make([]string, 0, len(stale))
		seen := map[string]bool{}
		for _, it := range stale {
			if !seen[it.AssigneeWorkflowID] {
				seen[it.AssigneeWorkflowID] = true
				ids = append(ids, it.AssigneeWorkflowID)
			}
		}
		alive, err := s.workflowsWithSessions(ctx, ids)
		if err != nil {
			return nil, err
		}
		for _, it := range stale {
			if alive[it.AssigneeWorkflowID] {
				d.Stale.add(it.Seq, it.AssignedAt)
			} else {
				d.StaleGone.add(it.Seq, it.AssignedAt)
			}
		}
	}
	return d, nil
}

// BoardOffered 是某个工作流手上「指派给它、还没接取」的协作任务：个数与最早一条的派出时间。
// 网页侧栏会话卡的「N 项待接取」徽标用（#180 B3b）；口径与回执头 board: 行的「指派待你接取」一致。
type BoardOffered struct {
	WorkflowID string     `json:"workflow_id"`
	Count      int        `json:"count"`
	OldestAt   *time.Time `json:"oldest_at,omitempty"`
}

// BoardOfferedByWorkflow 一次查出所有工作流各自的待接取个数与最早派出时间（只返回个数大于 0 的），个数多的在前。
// 待接取的指派在任何时刻都很少（生产现状个位数），一条查询取出、在 Go 里汇总；不用 SQL 聚合是为了让时间列按类型解析。
func (s *Store) BoardOfferedByWorkflow(ctx context.Context) ([]BoardOffered, error) {
	var rows []model.WorkItem
	if err := s.db.WithContext(ctx).Select("assignee_workflow_id", "assigned_at").
		Where("claim_state = ? AND assignee_workflow_id <> '' AND assigned_by_workflow_id <> assignee_workflow_id AND state IN ?",
			model.WorkClaimOffered, digestOpenStates).
		Find(&rows).Error; err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, nil
	}
	byWf := map[string]*BoardOffered{}
	order := make([]string, 0, 4)
	for i := range rows {
		wf := rows[i].AssigneeWorkflowID
		o, ok := byWf[wf]
		if !ok {
			o = &BoardOffered{WorkflowID: wf}
			byWf[wf] = o
			order = append(order, wf)
		}
		o.Count++
		if at := rows[i].AssignedAt; at != nil && !at.IsZero() && (o.OldestAt == nil || at.Before(*o.OldestAt)) {
			t := *at
			o.OldestAt = &t
		}
	}
	out := make([]BoardOffered, 0, len(order))
	for _, wf := range order {
		out = append(out, *byWf[wf])
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].WorkflowID < out[j].WorkflowID
	})
	return out, nil
}

// returnedAmong 在这些任务里找出「时间线上最近一次协作事件是退回」的：交付被退回后，承接者重做、再交付（或被改派 / 撤销 / 拒绝）
// 都会让最近的协作事件变成别的，退回的提醒随之消失。时间线每个任务只保留最近的几十条，退回之后又写了很多条普通进展时
// 这条事件可能已被挤掉——那种情形提醒自然不再出现，属于软提醒的可接受边界。
func (s *Store) returnedAmong(ctx context.Context, ids []uint) (map[uint]bool, error) {
	var evs []model.WorkItemUpdate
	if err := s.db.WithContext(ctx).Select("id", "work_item_id", "event").
		Where("work_item_id IN ? AND event <> ''", ids).Order("id DESC").Find(&evs).Error; err != nil {
		return nil, err
	}
	seen := make(map[uint]bool, len(ids))
	out := map[uint]bool{}
	for _, e := range evs {
		if seen[e.WorkItemID] {
			continue
		}
		seen[e.WorkItemID] = true
		if e.Event == model.WorkEventRejectDelivery {
			out[e.WorkItemID] = true
		}
	}
	return out, nil
}

// workflowsWithSessions 返回这些工作流里「还有会话」的——没有任何会话的工作流视为已不存在（被删除了）。
func (s *Store) workflowsWithSessions(ctx context.Context, ids []string) (map[string]bool, error) {
	var have []string
	if err := s.db.WithContext(ctx).Model(&model.FeedbackSession{}).Distinct().
		Where("workflow_id IN ?", ids).Pluck("workflow_id", &have).Error; err != nil {
		return nil, err
	}
	out := make(map[string]bool, len(have))
	for _, w := range have {
		out[w] = true
	}
	return out, nil
}
