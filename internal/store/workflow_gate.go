package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/teacat99/RelayMesh/v2/internal/asyncmode"
	"github.com/teacat99/RelayMesh/v2/internal/model"
)

// 严格收尾门（#232 D2，决策 D-375）。
//
// 用户的要求是「做完所有非搁置任务再结束会话」。老口径只数「进行中 / 验证中、且最近 3 小时有更新」的任务，
// 于是待办没人管、挂着不动的验证中任务三小时后就不挡了。收尾门把口径收紧成「本会话名下还有多少个没做完的任务」：
//   - 范围：待办 / 进行中 / 验证中，不按时间截断；已完成 / 已搁置不算（暂缓就是「放下」的正规出口）；
//   - 「本会话名下」按指派关系判定，避免两个会话互相等对方而死锁（见 gateWhere 的逐条注释与单测矩阵）；
//   - 门关着（StrictWrapupOff）时回到老口径；放行（GateReleased）时窗口不再被挡，但界面仍能看到被放行的任务。

// gateWhere 是「挡着 @wf 这个会话收尾的任务」的查询条件。三类状态、四种归属：
//
//	待办 / 进行中：谁该动手谁挡着——
//	  · 没有指派：创建它的会话；
//	  · 已指派（含已派未接、已接取）：承接者；
//	  · 承接者拒绝（declined）：回到派发者手里等重派，此时挡着的是派发者而不是拒绝的人；
//	验证中（review）：谁该验收谁挡着——
//	  · 没有指派 / 自己派给自己：自己；
//	  · 协作指派（派发者 ≠ 承接者）：已交付待验收，挡着的是派发者；人在 Web 上派的（派发者为空）没有 Agent 要验收，不挡任何会话。
//
// 整个条件外面再包一层括号，免得与调用方追加的条件组合时被 OR 截断。
const gateWhere = `(
	(state IN ('todo','doing') AND (
		(assignee_workflow_id = '' AND workflow_id = @wf)
		OR (assignee_workflow_id = @wf AND claim_state <> 'declined')
		OR (claim_state = 'declined' AND assigned_by_workflow_id = @wf)
	))
	OR (state = 'review' AND (
		(assignee_workflow_id = '' AND workflow_id = @wf)
		OR (assignee_workflow_id = @wf AND assigned_by_workflow_id = @wf)
		OR (assignee_workflow_id <> '' AND assignee_workflow_id <> assigned_by_workflow_id AND assigned_by_workflow_id = @wf)
	))
)`

// gateTasks 返回挡着 wf 收尾的任务：total 是总数，tasks 是按编号升序的前 limit 个。
// 出错时偏向放行（返回 0）：查询失败不该把窗口永远钉住，与老口径出错的处理一致。
func (s *Store) gateTasks(ctx context.Context, wf string, limit int) (tasks []asyncmode.GateTask, total int) {
	q := func() *gorm.DB {
		return s.db.WithContext(ctx).Model(&model.WorkItem{}).Where(gateWhere, sql.Named("wf", wf))
	}
	var n int64
	if err := q().Count(&n).Error; err != nil || n == 0 {
		return nil, 0
	}
	total = int(n)
	var rows []model.WorkItem
	if err := q().Select("id", "seq", "state", "title").Order("seq ASC, id ASC").Limit(limit).Find(&rows).Error; err != nil {
		return nil, total
	}
	tasks = make([]asyncmode.GateTask, 0, len(rows))
	for _, r := range rows {
		tasks = append(tasks, asyncmode.GateTask{ID: r.ID, Seq: r.Seq, State: r.State, Title: r.Title})
	}
	return tasks, total
}

// windowLoad 是回复窗口对齐要用的「任务负载」：收尾门开着用门口径，关着用老口径；放行后不再挡。
type windowLoad struct {
	Active   int // 此刻挡着窗口的任务数（放行后为 0）
	Strict   bool
	Released bool
	Gate     []asyncmode.GateTask
	Total    int // 门口径的未完成任务总数（门关着时为 0）
}

func (s *Store) windowLoad(ctx context.Context, wf string, row model.WorkflowMode) windowLoad {
	l := windowLoad{Strict: !row.StrictWrapupOff, Released: row.GateReleased}
	if l.Strict {
		l.Gate, l.Total = s.gateTasks(ctx, wf, asyncmode.GateViewMax)
		l.Active = l.Total
	} else {
		l.Active = s.activeTaskCount(ctx, wf)
		l.Released = false // 门关着没有「放行」一说
	}
	if l.Released {
		l.Active = 0
	}
	return l
}

// apply 把任务负载写进窗口状态。
func (l windowLoad) apply(w *ReplyWindow) {
	w.ActiveTasks = l.Active
	w.Strict = l.Strict
	w.Released = l.Released
	w.Gate = l.Gate
	w.GateTotal = l.Total
}

// GateInfo 是某个会话此刻的收尾门状态（Web 弹层与回执 tasks: 行用）。
type GateInfo struct {
	// Strict 门开着（用户没关）；Released 本轮已被用户放行。
	Strict   bool
	Released bool
	// Total 是挡着收尾的任务总数，Tasks 是其中按编号升序的前 asyncmode.GateViewMax 个。
	Total int
	Tasks []asyncmode.GateTask
}

// WorkflowGate 读取某个会话的收尾门状态。没有模式行（从未开启过异步）时全为零值：没有门可言。
func (s *Store) WorkflowGate(ctx context.Context, workflowID string) (GateInfo, error) {
	wf := strings.TrimSpace(workflowID)
	var g GateInfo
	if wf == "" {
		return g, nil
	}
	var row model.WorkflowMode
	if err := s.db.WithContext(ctx).Where("workflow_id = ?", wf).Take(&row).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return g, nil
		}
		return g, err
	}
	g.Strict = !row.StrictWrapupOff
	if !g.Strict {
		return g, nil
	}
	g.Released = row.GateReleased
	g.Tasks, g.Total = s.gateTasks(ctx, wf, asyncmode.GateViewMax)
	return g, nil
}

// ReleaseGate 「本次放行」：这一轮回复窗口不再被任务挡住，立刻恢复倒计时；任务本身一概不动。
// 下一次用户回复、或窗口走完后重新起算时自动复位。没有窗口在等（或门已关）时放行没有意义，返回冲突并说明。
func (s *Store) ReleaseGate(ctx context.Context, workflowID string, now time.Time) (ReplyWindow, error) {
	wf := strings.TrimSpace(workflowID)
	if wf == "" {
		return ReplyWindow{}, NewInvalidInputError("workflow_id 不能为空")
	}
	var row model.WorkflowMode
	if err := s.db.WithContext(ctx).Where("workflow_id = ?", wf).Take(&row).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ReplyWindow{}, NewNotFoundError("这个会话还没有开启过异步对话，没有收尾门可放行")
		}
		return ReplyWindow{}, err
	}
	if row.StrictWrapupOff {
		return ReplyWindow{}, NewConflictError("这个会话的收尾门已经关闭，不需要放行", 0)
	}
	if row.ReplyWindowUntil == nil && row.ReplyWindowBankSec <= 0 {
		return ReplyWindow{}, NewConflictError("现在没有回复窗口在等，不需要放行", 0)
	}
	if !row.GateReleased {
		if err := s.db.WithContext(ctx).Model(&model.WorkflowMode{}).Where("workflow_id = ?", wf).
			Update("gate_released", true).Error; err != nil {
			return ReplyWindow{}, err
		}
	}
	w, _, err := s.ReconcileReplyWindow(ctx, wf, now) // 窗口攒着的剩余时间立刻接着走
	return w, err
}

// ---- 暂缓要写明缘由（#232 D2 的软提醒）----
//
// 收尾门把「暂缓」变成放下任务的正规出口，也就多了一条偷懒的路：把做不完的统统置暂缓、什么也不写。
// 用户（Q84）的要求是「需要我决策的全部使用确认卡」，所以刚置暂缓的任务，进展里应当写明在等哪张确认卡（Q<N>）
// 或在等什么别的条件；没写的，回执 tasks: 行与 task_board overview 会温和地提醒一次。只是提醒，不拦截。

// ShelveNagWindow 是「刚暂缓」的时间范围：只提醒最近这段时间内置的暂缓，历史上早已搁置的任务不翻旧账。
const ShelveNagWindow = 24 * time.Hour

// shelvedWhere 是「@wf 名下的暂缓任务」：没有指派归创建者，已指派归承接者（与 gateWhere 的待办 / 进行中同口径）。
const shelvedWhere = `(state = 'shelved' AND (
	(assignee_workflow_id = '' AND workflow_id = @wf)
	OR assignee_workflow_id = @wf
))`

var shelveReasonRe = regexp.MustCompile(`Q\d+`)

// ShelveExplained 判断一个暂缓任务有没有写明缘由：进展 / 引用里带确认卡编号（Q<N>），或写了「等…」，
// 或已经有结论（说明是收尾性的搁置，不是在等谁）。
func ShelveExplained(it model.WorkItem) bool {
	if strings.TrimSpace(it.Conclusion) != "" {
		return true
	}
	text := it.Progress + "\n" + strings.Join(it.Refs, "\n")
	return shelveReasonRe.MatchString(text) || strings.Contains(text, "等")
}

// ShelvedUnexplained 返回 wf 名下、最近 ShelveNagWindow 内置为暂缓、却没写明缘由的任务（按编号升序，最多 limit 个），
// total 是总数。查询出错时返回空（提醒是软的，查不出来就不提醒）。
func (s *Store) ShelvedUnexplained(ctx context.Context, workflowID string, now time.Time, limit int) (tasks []asyncmode.GateTask, total int) {
	wf := strings.TrimSpace(workflowID)
	if wf == "" {
		return nil, 0
	}
	var rows []model.WorkItem
	if err := s.db.WithContext(ctx).Model(&model.WorkItem{}).
		Where(shelvedWhere, sql.Named("wf", wf)).
		Where("updated_at >= ?", now.Add(-ShelveNagWindow)).
		Order("seq ASC, id ASC").Limit(200).Find(&rows).Error; err != nil {
		return nil, 0
	}
	for _, r := range rows {
		if ShelveExplained(r) {
			continue
		}
		total++
		if len(tasks) < limit {
			tasks = append(tasks, asyncmode.GateTask{ID: r.ID, Seq: r.Seq, State: r.State, Title: r.Title})
		}
	}
	return tasks, total
}

// ---- 反向提醒：确认卡已答复、任务还在暂缓（#232 D2）----
//
// 「先发确认卡再置暂缓」的另一半是「答复来了要接着做」。答复会经 hook 与收件箱送达，但上下文被压缩、
// 收件箱读过确认过却没接着做的情况仍会发生，所以服务端再兜一层：最近被答复的确认卡，如果挂着它的任务
// 还停在「暂缓」、而且答复之后没人动过这个任务（更新时间不晚于作答时间），就点名一次。只是提醒，不拦截；
// 任务一被更新（置进行中，或在进展里写明为什么仍要暂缓）就自动消失。

// AnsweredNagWindow 是反向提醒的回看范围：只看最近这段时间内被答复的确认卡，更早的答复还没人处理，多半是有意搁置。
const AnsweredNagWindow = 7 * 24 * time.Hour

// answeredRefRe 取任务进展 / 引用里的确认卡编号 Q<N>（词边界：不把 FAQ12 之类当成卡号）。
var answeredRefRe = regexp.MustCompile(`\bQ(\d+)\b`)

// ShelvedAfterAnswer 返回 wf 名下「所挂确认卡已被答复、答复后没人动过、却还在暂缓」的任务（按任务编号升序，最多 limit 个），
// total 是总数。任务与确认卡的关联有两种：确认卡的 task_seq 指向该任务（计划卡），或任务的进展 / 引用里写了卡号 Q<N>。
// 一个任务挂着多张已答复的卡时，取最近答复的那一张。查询出错时返回空（提醒是软的，查不出来就不提醒）。
func (s *Store) ShelvedAfterAnswer(ctx context.Context, workflowID string, now time.Time, limit int) (items []asyncmode.AnsweredTask, total int) {
	wf := strings.TrimSpace(workflowID)
	if wf == "" {
		return nil, 0
	}
	var cards []model.ConfirmItem
	if err := s.db.WithContext(ctx).Select("seq", "task_seq", "answered_at").
		Where("workflow_id = ? AND status = ? AND answered_at IS NOT NULL AND answered_at >= ?",
			wf, model.ConfirmStatusAnswered, now.Add(-AnsweredNagWindow)).
		Order("seq ASC").Limit(200).Find(&cards).Error; err != nil || len(cards) == 0 {
		return nil, 0
	}
	cardBySeq := make(map[int]model.ConfirmItem, len(cards))
	cardsByTask := make(map[int][]model.ConfirmItem)
	for _, c := range cards {
		cardBySeq[c.Seq] = c
		if c.TaskSeq > 0 {
			cardsByTask[c.TaskSeq] = append(cardsByTask[c.TaskSeq], c)
		}
	}
	var rows []model.WorkItem
	if err := s.db.WithContext(ctx).Model(&model.WorkItem{}).
		Where(shelvedWhere, sql.Named("wf", wf)).
		Order("seq ASC, id ASC").Limit(200).Find(&rows).Error; err != nil {
		return nil, 0
	}
	for _, r := range rows {
		var best *model.ConfirmItem
		consider := func(c model.ConfirmItem) {
			if c.AnsweredAt == nil || r.UpdatedAt.After(*c.AnsweredAt) {
				return // 答复之后任务已经被动过：当作已处理
			}
			if best == nil || c.AnsweredAt.After(*best.AnsweredAt) {
				cc := c
				best = &cc
			}
		}
		for _, c := range cardsByTask[r.Seq] {
			consider(c)
		}
		text := r.Progress + "\n" + strings.Join(r.Refs, "\n")
		for _, m := range answeredRefRe.FindAllStringSubmatch(text, -1) {
			if n, err := strconv.Atoi(m[1]); err == nil {
				if c, ok := cardBySeq[n]; ok {
					consider(c)
				}
			}
		}
		if best == nil {
			continue
		}
		total++
		if len(items) < limit {
			items = append(items, asyncmode.AnsweredTask{Seq: r.Seq, Title: r.Title, Ref: fmt.Sprintf("Q%d", best.Seq)})
		}
	}
	return items, total
}
