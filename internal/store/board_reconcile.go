package store

import (
	"context"
	"fmt"
	"log"
	"sort"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/teacat99/RelayMesh/v2/internal/model"
)

// 启动时给历史任务补板（#175 迁移）。
//
// 原则：**迁移永不改号**。有项目的工作流归项目板，没有项目的归独立板；若归项目板会与板内已有的序号或规划节点路径冲突，
// 这个工作流留在自己的独立板，等用户看过预览后显式合并（设计稿第 8 节第 5 点）。
// 整个过程幂等：已经归板的行不会再动，重复执行结果不变；旧二进制在回滚期间写入的空值行会在下次启动时补齐。
//
// 规划（planBoardReconcile）与执行（applyBoardReconcile）分开：规划只读，所以 dry-run 工具看到什么，启动时就会做什么。

// boardData 是迁移 / 预演用的存量快照（只取需要的列）。
type boardData struct {
	items         []model.WorkItem
	counters      []model.WorkItemCounter
	boardCounters []model.WorkBoardCounter
	modules       []model.WorkModule
}

// loadBoardData 读存量快照。迁移前的库没有 board_key 列和 work_board_counters 表，这里按有无区分，让预演对旧库同样可用。
func loadBoardData(ctx context.Context, db *gorm.DB) (*boardData, error) {
	d := &boardData{}
	mig := db.Migrator()
	itemCols := []string{"id", "workflow_id", "seq", "title", "created_at", "parent_id", "module_id"}
	if mig.HasColumn("work_items", "board_key") {
		itemCols = append(itemCols, "board_key")
	}
	if err := db.WithContext(ctx).Select(itemCols).Order("id ASC").Find(&d.items).Error; err != nil {
		return nil, fmt.Errorf("读取任务失败: %w", err)
	}
	if err := db.WithContext(ctx).Find(&d.counters).Error; err != nil {
		return nil, fmt.Errorf("读取序号计数失败: %w", err)
	}
	if mig.HasTable("work_board_counters") {
		if err := db.WithContext(ctx).Find(&d.boardCounters).Error; err != nil {
			return nil, fmt.Errorf("读取板序号计数失败: %w", err)
		}
	}
	modCols := []string{"id", "workflow_id", "parent_id", "name"}
	if mig.HasColumn("work_modules", "board_key") {
		modCols = append(modCols, "board_key")
	}
	if err := db.WithContext(ctx).Table("work_modules").Select(modCols).Order("id ASC").Find(&d.modules).Error; err != nil {
		return nil, fmt.Errorf("读取规划节点失败: %w", err)
	}
	return d, nil
}

// BoardAssignment 是迁移里一个工作流的归板决定。
type BoardAssignment struct {
	WorkflowID string `json:"workflow_id"`
	BoardKey   string `json:"board_key"`
	Items      int    `json:"items"`
	Modules    int    `json:"modules"`
	// Reason 说明依据：粘性（已有归板的任务 / 节点）、目录归属，或冲突回退。
	Reason string `json:"reason"`
	// Fallback 为 true 表示它本该落到 WantedKey，因序号或规划节点路径冲突回退到自己的独立板。
	Fallback  bool     `json:"fallback,omitempty"`
	WantedKey string   `json:"wanted_key,omitempty"`
	Conflicts []string `json:"conflicts,omitempty"`
}

// BoardCounterPlan 是迁移后某张板的序号计数；只列需要写（没有记录或要调大）的。
type BoardCounterPlan struct {
	BoardKey string `json:"board_key"`
	LastSeq  int    `json:"last_seq"`
	// Existing 是现有 work_board_counters 里的值，没有记录为 0。
	Existing int `json:"existing"`
}

// BoardReconcilePlan 是给历史任务补板的完整计划。
type BoardReconcilePlan struct {
	// Placed 是已经归板的任务数（board_key 非空）；Unplaced / UnplacedModules 是这次要归板的任务数与规划节点数。
	Placed          int                `json:"placed"`
	Unplaced        int                `json:"unplaced"`
	UnplacedModules int                `json:"unplaced_modules"`
	Assignments     []BoardAssignment  `json:"assignments"`
	Counters        []BoardCounterPlan `json:"counters"`
	Warnings        []string           `json:"warnings"`
}

// Changed 表示这份计划是否会写库。
func (p *BoardReconcilePlan) Changed() bool {
	return len(p.Assignments) > 0 || len(p.Counters) > 0
}

// PlanBoardReconcile 只读地算出启动迁移会做什么（供 dry-run 工具使用）。
func PlanBoardReconcile(ctx context.Context, db *gorm.DB) (*BoardReconcilePlan, error) {
	d, err := loadBoardData(ctx, db)
	if err != nil {
		return nil, err
	}
	return planBoardReconcile(ctx, db, d)
}

func planBoardReconcile(ctx context.Context, db *gorm.DB, d *boardData) (*BoardReconcilePlan, error) {
	plan := &BoardReconcilePlan{}

	byModuleID := make(map[uint]*model.WorkModule, len(d.modules))
	for i := range d.modules {
		byModuleID[d.modules[i].ID] = &d.modules[i]
	}
	modulePaths := boardModulePaths(d.modules, byModuleID)

	// 已归板的占用情况：板内用了哪些序号、哪些规划路径；以及各工作流已经在哪张板（粘性）。
	placedSeq := map[string]map[int]bool{}
	placedPath := map[string]map[string]bool{}
	mark := func(board string, seq int, path string) {
		if seq != 0 {
			if placedSeq[board] == nil {
				placedSeq[board] = map[int]bool{}
			}
			placedSeq[board][seq] = true
		}
		if path != "" {
			if placedPath[board] == nil {
				placedPath[board] = map[string]bool{}
			}
			placedPath[board][strings.ToLower(path)] = true
		}
	}
	itemBoard := map[string]string{} // 工作流 → 最近一条已归板任务所在的板（id 升序遍历，后写覆盖前写）
	modBoard := map[string]string{}  // 工作流 → 最近一个已归板节点所在的板
	unItems := map[string][]*model.WorkItem{}
	unMods := map[string][]*model.WorkModule{}
	firstAt := map[string]time.Time{}
	for i := range d.items {
		it := &d.items[i]
		if it.BoardKey != "" {
			plan.Placed++
			itemBoard[it.WorkflowID] = it.BoardKey
			mark(it.BoardKey, it.Seq, "")
			continue
		}
		plan.Unplaced++
		unItems[it.WorkflowID] = append(unItems[it.WorkflowID], it)
		if t, ok := firstAt[it.WorkflowID]; !ok || it.CreatedAt.Before(t) {
			firstAt[it.WorkflowID] = it.CreatedAt
		}
	}
	for i := range d.modules {
		m := &d.modules[i]
		if m.BoardKey != "" {
			modBoard[m.WorkflowID] = m.BoardKey
			mark(m.BoardKey, 0, modulePaths[m.ID])
			continue
		}
		plan.UnplacedModules++
		unMods[m.WorkflowID] = append(unMods[m.WorkflowID], m)
	}

	// 逐个工作流决定归哪张板；更早的先定，后来者遇到冲突才回退。
	wfSet := map[string]bool{}
	for wf := range unItems {
		wfSet[wf] = true
	}
	for wf := range unMods {
		wfSet[wf] = true
	}
	wfs := make([]string, 0, len(wfSet))
	for wf := range wfSet {
		wfs = append(wfs, wf)
	}
	sort.SliceStable(wfs, func(i, j int) bool {
		ti, oki := firstAt[wfs[i]]
		tj, okj := firstAt[wfs[j]]
		if oki != okj {
			return oki // 没有任务（只有规划节点）的排最后
		}
		if oki && !ti.Equal(tj) {
			return ti.Before(tj)
		}
		return wfs[i] < wfs[j]
	})

	finalBoard := map[string]string{} // 工作流 → 它的任务最终所在的板（含已归板的）
	for wf, b := range itemBoard {
		finalBoard[wf] = b
	}
	for _, wf := range wfs {
		var target, reason string
		switch {
		case itemBoard[wf] != "":
			target, reason = itemBoard[wf], "已有任务在该板（粘性）"
		case modBoard[wf] != "":
			target, reason = modBoard[wf], "已有规划节点在该板（粘性）"
		default:
			info, err := resolveBoardOn(ctx, db, wf)
			if err != nil {
				return nil, fmt.Errorf("解析工作流 %s 所属的板失败: %w", wf, err)
			}
			target, reason = info.Key, info.Reason
		}
		a := BoardAssignment{WorkflowID: wf, BoardKey: target, Items: len(unItems[wf]), Modules: len(unMods[wf]), Reason: reason}
		if target != WorkflowBoardKey(wf) {
			var conflicts []string
			for _, it := range unItems[wf] {
				if placedSeq[target][it.Seq] {
					conflicts = append(conflicts, fmt.Sprintf("任务 #%d 的序号在板 %s 里已被占用", it.Seq, target))
				}
			}
			for _, m := range unMods[wf] {
				if placedPath[target][strings.ToLower(modulePaths[m.ID])] {
					conflicts = append(conflicts, fmt.Sprintf("规划节点「%s」在板 %s 里已有同路径节点", modulePaths[m.ID], target))
				}
			}
			if len(conflicts) > 0 {
				a.WantedKey, a.Fallback, a.Conflicts = target, true, conflicts
				a.BoardKey = WorkflowBoardKey(wf)
				a.Reason = "与板内已有内容冲突，留在自己的独立板（迁移不改号，等显式合并）"
				plan.Warnings = append(plan.Warnings, fmt.Sprintf("工作流 %s 本应归板 %s，但有 %d 处冲突，已回退到独立板 %s", wf, target, len(conflicts), a.BoardKey))
			}
		}
		for _, it := range unItems[wf] {
			mark(a.BoardKey, it.Seq, "")
		}
		for _, m := range unMods[wf] {
			mark(a.BoardKey, 0, modulePaths[m.ID])
		}
		if a.Items > 0 {
			finalBoard[wf] = a.BoardKey
		}
		plan.Assignments = append(plan.Assignments, a)
	}

	// 没有待归板的行时什么都不做（与 reconcileBoards 的快速路径一致）：之后的板计数由发号时按需补齐。
	if plan.Unplaced == 0 && plan.UnplacedModules == 0 {
		return plan, nil
	}

	// 板的序号计数：板内最大序号、板内各工作流旧计数的最大值、现有板计数，三者取大；只列需要写的。
	boardMax := map[string]int{}
	itemsBoard := func(it *model.WorkItem) string {
		if it.BoardKey != "" {
			return it.BoardKey
		}
		return finalBoard[it.WorkflowID]
	}
	for i := range d.items {
		it := &d.items[i]
		if b := itemsBoard(it); b != "" && it.Seq > boardMax[b] {
			boardMax[b] = it.Seq
		}
	}
	for _, c := range d.counters {
		b := finalBoard[c.WorkflowID]
		if b == "" {
			// 任务删光、只剩计数的工作流：它若会落在独立板，计数要带过去，否则新任务会复用已发过的号。
			info, err := resolveBoardOn(ctx, db, c.WorkflowID)
			if err != nil {
				return nil, fmt.Errorf("解析工作流 %s 所属的板失败: %w", c.WorkflowID, err)
			}
			if isProjectBoard(info.Key) {
				continue
			}
			b = info.Key
		}
		if c.LastSeq > boardMax[b] {
			boardMax[b] = c.LastSeq
		}
	}
	existing := map[string]int{}
	for _, bc := range d.boardCounters {
		existing[bc.BoardKey] = bc.LastSeq
	}
	keys := make([]string, 0, len(boardMax))
	for k := range boardMax {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if boardMax[k] > existing[k] {
			plan.Counters = append(plan.Counters, BoardCounterPlan{BoardKey: k, LastSeq: boardMax[k], Existing: existing[k]})
		}
	}
	return plan, nil
}

// applyBoardReconcile 执行计划：给任务和规划节点写 board_key，写 / 调大板计数。必须在事务里调用。
func applyBoardReconcile(tx *gorm.DB, plan *BoardReconcilePlan) error {
	for _, a := range plan.Assignments {
		if err := tx.Model(&model.WorkItem{}).Where("workflow_id = ? AND board_key = ''", a.WorkflowID).
			UpdateColumn("board_key", a.BoardKey).Error; err != nil {
			return fmt.Errorf("任务归板失败（%s）: %w", a.WorkflowID, err)
		}
		if err := tx.Model(&model.WorkModule{}).Where("workflow_id = ? AND board_key = ''", a.WorkflowID).
			UpdateColumn("board_key", a.BoardKey).Error; err != nil {
			return fmt.Errorf("规划节点归板失败（%s）: %w", a.WorkflowID, err)
		}
	}
	for _, c := range plan.Counters {
		if err := tx.Exec(`INSERT INTO work_board_counters(board_key, last_seq) VALUES(?, ?)
			ON CONFLICT(board_key) DO UPDATE SET last_seq = MAX(last_seq, excluded.last_seq)`, c.BoardKey, c.LastSeq).Error; err != nil {
			return fmt.Errorf("写板序号计数失败（%s）: %w", c.BoardKey, err)
		}
	}
	return nil
}

// reconcileBoards 给没有 board_key 的历史任务与规划节点补齐板，并保证板序号计数不落后；幂等。
// 没有未归板的行时只做两次计数查询，不读全表。
func (s *Store) reconcileBoards(ctx context.Context) (*BoardReconcilePlan, error) {
	var items, mods int64
	if err := s.db.WithContext(ctx).Model(&model.WorkItem{}).Where("board_key = ''").Count(&items).Error; err != nil {
		return nil, err
	}
	if err := s.db.WithContext(ctx).Model(&model.WorkModule{}).Where("board_key = ''").Count(&mods).Error; err != nil {
		return nil, err
	}
	if items == 0 && mods == 0 {
		return &BoardReconcilePlan{}, nil
	}
	var plan *BoardReconcilePlan
	err := s.WithTx(ctx, func(tx *gorm.DB) error {
		d, err := loadBoardData(ctx, tx)
		if err != nil {
			return err
		}
		if plan, err = planBoardReconcile(ctx, tx, d); err != nil {
			return err
		}
		return applyBoardReconcile(tx, plan)
	})
	if err != nil {
		return nil, err
	}
	return plan, nil
}

// reconcileBoardsAtStartup 在启动时跑 reconcileBoards，失败只记日志（不阻断启动）；有动作时打一行摘要。
func (s *Store) reconcileBoardsAtStartup(ctx context.Context) {
	plan, err := s.reconcileBoards(ctx)
	if err != nil {
		log.Printf("[board] 任务归板失败（任务暂时看不到所属的板，请排查后重启）: %v", err)
		return
	}
	if !plan.Changed() {
		return
	}
	fallbacks := 0
	for _, a := range plan.Assignments {
		if a.Fallback {
			fallbacks++
		}
	}
	log.Printf("[board] 任务归板完成：任务 %d 条 / 规划节点 %d 个，涉及工作流 %d 个，冲突回退独立板 %d 个，调整板计数 %d 张",
		plan.Unplaced, plan.UnplacedModules, len(plan.Assignments), fallbacks, len(plan.Counters))
	for _, w := range plan.Warnings {
		log.Printf("[board] %s", w)
	}
}
