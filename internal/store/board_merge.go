package store

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/teacat99/RelayMesh/v2/internal/model"
)

// ---- 并入预演（只读） ----
//
// 「把各工作流并入各自目录归属的板，序号会怎样」的预览：保留较早的工作流的序号，后来者只重编冲突的那几条。
// 它回答的是显式合并（设计稿第 8 节第 5 点）会改哪些号，**不是**启动迁移做的事——启动迁移（reconcileBoards）永不改号，
// 遇到冲突的工作流让它留在自己的独立板。dry-run 工具两份都打印。

// BoardMergeWorkflow 是规划里的一个工作流：它有多少任务、落在哪张板、依据是什么。
type BoardMergeWorkflow struct {
	WorkflowID string `json:"workflow_id"`
	Tasks      int    `json:"tasks"`
	MaxSeq     int    `json:"max_seq"`
	// Counter 是旧计数表 work_item_counters 里的值；HasCounter 表示有没有这条记录。
	Counter     int       `json:"counter"`
	HasCounter  bool      `json:"has_counter"`
	Modules     int       `json:"modules"`
	FirstTaskAt time.Time `json:"first_task_at,omitempty"`
	BoardKey    string    `json:"board_key"`
	ProjectSlug string    `json:"project_slug,omitempty"`
	Host        string    `json:"host,omitempty"`
	Dir         string    `json:"dir,omitempty"`
	Reason      string    `json:"reason"`
}

// BoardMergeBoard 是规划出的一张板。
type BoardMergeBoard struct {
	Key         string `json:"key"`
	ProjectID   uint   `json:"project_id,omitempty"`
	ProjectSlug string `json:"project_slug,omitempty"`
	ACLMode     string `json:"acl_mode,omitempty"`
	// Workflows 按「谁更早」排序（有任务的最早创建时间，其次编号）：排在前面的保留自己的序号。
	Workflows []string `json:"workflows"`
	Tasks     int      `json:"tasks"`
	// Counter 是迁移后 work_board_counters.last_seq：板内最大序号与各工作流旧计数的最大值。
	Counter int `json:"counter"`
	// Renumbered 是要改号的任务数；DupModules 是会被合并掉的重复规划节点数。
	Renumbered int `json:"renumbered"`
	DupModules int `json:"dup_modules"`
}

// BoardMergeItemRef 指向一条任务。
type BoardMergeItemRef struct {
	ItemID     uint   `json:"item_id"`
	WorkflowID string `json:"workflow_id"`
	Title      string `json:"title"`
}

// BoardMergeCollision 是同一张板里两个以上工作流用了同一个序号。
type BoardMergeCollision struct {
	BoardKey string              `json:"board_key"`
	Seq      int                 `json:"seq"`
	Items    []BoardMergeItemRef `json:"items"`
}

// BoardMergeRenumber 是一条改号动作：只重编冲突的那几条，别的序号原样保留；旧号写进别名表。
type BoardMergeRenumber struct {
	BoardKey   string `json:"board_key"`
	WorkflowID string `json:"workflow_id"`
	ItemID     uint   `json:"item_id"`
	OldSeq     int    `json:"old_seq"`
	NewSeq     int    `json:"new_seq"`
}

// BoardMergeModuleDup 是同一张板里路径相同的规划节点（会合并成一个）。
type BoardMergeModuleDup struct {
	BoardKey  string   `json:"board_key"`
	Path      string   `json:"path"`
	Workflows []string `json:"workflows"`
	ModuleIDs []uint   `json:"module_ids"`
	// Tasks 是挂在这些节点上的任务数。
	Tasks int `json:"tasks"`
}

// BoardMergePlan 是并入预演的完整结果。
type BoardMergePlan struct {
	Items      int                   `json:"items"`
	Workflows  []BoardMergeWorkflow  `json:"workflows"`
	Boards     []BoardMergeBoard     `json:"boards"`
	Collisions []BoardMergeCollision `json:"collisions"`
	Renumbers  []BoardMergeRenumber  `json:"renumbers"`
	ModuleDups []BoardMergeModuleDup `json:"module_dups"`
	// Warnings 是不阻塞但值得人看一眼的发现（解析不到目录、跨工作流的父子 / 模块引用、计数落后等）。
	Warnings []string `json:"warnings"`
}

// boardMergeWF 是规划时每个工作流的累加器。
type boardMergeWF struct {
	items []model.WorkItem
	first time.Time
}

// PlanBoardMerge 读库算出「若把每个工作流并入它目录归属的板，序号有没有冲突、要改哪些号」，不写任何表。
// 只用到旧列（不看 board_key），所以对迁移前后的库都可用；它按目录归属动态解析，不考虑任务已经落在哪张板。
func PlanBoardMerge(ctx context.Context, db *gorm.DB) (*BoardMergePlan, error) {
	s := &Store{db: db}
	plan := &BoardMergePlan{}

	d, err := loadBoardData(ctx, db)
	if err != nil {
		return nil, err
	}
	items, counters, modules := d.items, d.counters, d.modules
	plan.Items = len(items)

	// 每个工作流的任务 / 计数 / 节点数。
	wfs := map[string]*boardMergeWF{}
	get := func(wf string) *boardMergeWF {
		a := wfs[wf]
		if a == nil {
			a = &boardMergeWF{}
			wfs[wf] = a
		}
		return a
	}
	itemByID := make(map[uint]*model.WorkItem, len(items))
	for i := range items {
		it := &items[i]
		a := get(it.WorkflowID)
		a.items = append(a.items, *it)
		if a.first.IsZero() || it.CreatedAt.Before(a.first) {
			a.first = it.CreatedAt
		}
		itemByID[it.ID] = it
	}
	for _, a := range wfs {
		sort.SliceStable(a.items, func(i, j int) bool { return a.items[i].Seq < a.items[j].Seq })
	}
	counterOf := map[string]int{}
	for _, c := range counters {
		counterOf[c.WorkflowID] = c.LastSeq
		get(c.WorkflowID)
	}
	moduleByID := make(map[uint]*model.WorkModule, len(modules))
	moduleCount := map[string]int{}
	for i := range modules {
		m := &modules[i]
		moduleByID[m.ID] = m
		moduleCount[m.WorkflowID]++
		get(m.WorkflowID)
	}

	// 解析每个工作流的板。
	ids := make([]string, 0, len(wfs))
	for wf := range wfs {
		ids = append(ids, wf)
	}
	sort.Strings(ids)
	type boardAgg struct {
		info BoardInfo
		wfs  []string
	}
	boards := map[string]*boardAgg{}
	boardOfWF := map[string]string{}
	for _, wf := range ids {
		a := wfs[wf]
		info, err := s.resolveBoard(ctx, wf)
		if err != nil {
			return nil, fmt.Errorf("解析工作流 %s 所属的板失败: %w", wf, err)
		}
		row := BoardMergeWorkflow{
			WorkflowID: wf, Tasks: len(a.items), Modules: moduleCount[wf], FirstTaskAt: a.first,
			BoardKey: info.Key, Host: info.Host, Dir: info.Dir, Reason: info.Reason,
		}
		if info.Project != nil {
			row.ProjectSlug = info.Project.Slug
		}
		for _, it := range a.items {
			if it.Seq > row.MaxSeq {
				row.MaxSeq = it.Seq
			}
		}
		if c, ok := counterOf[wf]; ok {
			row.Counter, row.HasCounter = c, true
			if c < row.MaxSeq {
				plan.Warnings = append(plan.Warnings, fmt.Sprintf("工作流 %s 的旧计数 %d 落后于最大序号 %d（旧代码靠 MAX(seq) 兜底，迁移会取较大者）", wf, c, row.MaxSeq))
			}
		} else if len(a.items) > 0 {
			plan.Warnings = append(plan.Warnings, fmt.Sprintf("工作流 %s 有 %d 条任务但没有旧计数记录（迁移按最大序号 %d 补）", wf, len(a.items), row.MaxSeq))
		}
		if info.Dir == "" && len(a.items) > 0 {
			plan.Warnings = append(plan.Warnings, fmt.Sprintf("工作流 %s 有任务却没有带目录的汇报会话，只能落独立板", wf))
		}
		plan.Workflows = append(plan.Workflows, row)
		b := boards[info.Key]
		if b == nil {
			b = &boardAgg{info: info}
			boards[info.Key] = b
		}
		b.wfs = append(b.wfs, wf)
		boardOfWF[wf] = info.Key
	}

	// 跨工作流的引用（存储层本来就禁止，库里出现说明历史数据有问题）。
	for _, it := range items {
		if it.ParentID != nil {
			if p := itemByID[*it.ParentID]; p == nil {
				plan.Warnings = append(plan.Warnings, fmt.Sprintf("任务 id=%d（%s #%d）的父任务 id=%d 不存在", it.ID, it.WorkflowID, it.Seq, *it.ParentID))
			} else if p.WorkflowID != it.WorkflowID {
				plan.Warnings = append(plan.Warnings, fmt.Sprintf("任务 id=%d（%s #%d）的父任务属于别的工作流 %s", it.ID, it.WorkflowID, it.Seq, p.WorkflowID))
			}
		}
		if it.ModuleID != nil {
			if m := moduleByID[*it.ModuleID]; m == nil {
				plan.Warnings = append(plan.Warnings, fmt.Sprintf("任务 id=%d（%s #%d）的模块 id=%d 不存在", it.ID, it.WorkflowID, it.Seq, *it.ModuleID))
			} else if m.WorkflowID != it.WorkflowID {
				plan.Warnings = append(plan.Warnings, fmt.Sprintf("任务 id=%d（%s #%d）的模块属于别的工作流 %s", it.ID, it.WorkflowID, it.Seq, m.WorkflowID))
			}
		}
	}

	// 逐板规划：序号冲突与改号、规划节点合并、计数。
	keys := make([]string, 0, len(boards))
	for k := range boards {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	modulePaths := boardModulePaths(modules, moduleByID)
	for _, key := range keys {
		b := boards[key]
		// 谁更早：有任务的最早创建时间，没有任务的排最后；再按编号，保证结果稳定。
		order := append([]string(nil), b.wfs...)
		sort.SliceStable(order, func(i, j int) bool {
			ai, aj := wfs[order[i]], wfs[order[j]]
			zi, zj := ai.first.IsZero(), aj.first.IsZero()
			if zi != zj {
				return !zi
			}
			if !ai.first.Equal(aj.first) {
				return ai.first.Before(aj.first)
			}
			return order[i] < order[j]
		})
		pb := BoardMergeBoard{Key: key, Workflows: order}
		if b.info.Project != nil {
			pb.ProjectID, pb.ProjectSlug, pb.ACLMode = b.info.Project.ID, b.info.Project.Slug, b.info.Project.ACLMode
		}

		boardMax := 0
		for _, wf := range order {
			for _, it := range wfs[wf].items {
				pb.Tasks++
				if it.Seq > boardMax {
					boardMax = it.Seq
				}
			}
			if c := counterOf[wf]; c > boardMax {
				boardMax = c
			}
		}

		// 序号冲突：同一个序号出现在两个以上工作流里。
		type ref struct {
			wf string
			it model.WorkItem
		}
		bySeq := map[int][]ref{}
		for _, wf := range order {
			for _, it := range wfs[wf].items {
				bySeq[it.Seq] = append(bySeq[it.Seq], ref{wf: wf, it: it})
			}
		}
		seqs := make([]int, 0, len(bySeq))
		for seq, rs := range bySeq {
			if len(rs) > 1 {
				seqs = append(seqs, seq)
			}
		}
		sort.Ints(seqs)
		for _, seq := range seqs {
			col := BoardMergeCollision{BoardKey: key, Seq: seq}
			for _, r := range bySeq[seq] {
				col.Items = append(col.Items, BoardMergeItemRef{ItemID: r.it.ID, WorkflowID: r.wf, Title: r.it.Title})
			}
			plan.Collisions = append(plan.Collisions, col)
		}

		// 改号：排在最前的工作流保留全部序号；后来者只重编冲突的那几条，按原顺序接在板内最大序号之后。
		occupied := map[int]bool{}
		next := boardMax
		for i, wf := range order {
			for _, it := range wfs[wf].items {
				if i == 0 || !occupied[it.Seq] {
					occupied[it.Seq] = true
					continue
				}
				next++
				occupied[next] = true
				plan.Renumbers = append(plan.Renumbers, BoardMergeRenumber{
					BoardKey: key, WorkflowID: wf, ItemID: it.ID, OldSeq: it.Seq, NewSeq: next,
				})
				pb.Renumbered++
			}
		}
		pb.Counter = boardMax
		if next > pb.Counter {
			pb.Counter = next
		}

		// 规划节点：同一张板里路径相同的合并为一个。
		groups := map[string][]*model.WorkModule{}
		for i := range modules {
			m := &modules[i]
			if boardOfWF[m.WorkflowID] != key {
				continue
			}
			p := modulePaths[m.ID]
			groups[p] = append(groups[p], m)
		}
		paths := make([]string, 0, len(groups))
		for p, ms := range groups {
			if len(ms) > 1 {
				paths = append(paths, p)
			}
		}
		sort.Strings(paths)
		for _, p := range paths {
			dup := BoardMergeModuleDup{BoardKey: key, Path: p}
			seen := map[string]bool{}
			idset := map[uint]bool{}
			for _, m := range groups[p] {
				dup.ModuleIDs = append(dup.ModuleIDs, m.ID)
				idset[m.ID] = true
				if !seen[m.WorkflowID] {
					seen[m.WorkflowID] = true
					dup.Workflows = append(dup.Workflows, m.WorkflowID)
				}
			}
			sort.Strings(dup.Workflows)
			for _, wf := range order {
				for _, it := range wfs[wf].items {
					if it.ModuleID != nil && idset[*it.ModuleID] {
						dup.Tasks++
					}
				}
			}
			pb.DupModules += len(groups[p]) - 1
			plan.ModuleDups = append(plan.ModuleDups, dup)
		}
		plan.Boards = append(plan.Boards, pb)
	}
	return plan, nil
}

// boardModulePaths 算出每个规划节点的完整路径（顶层 / 功能，用 / 连接）；父链断裂或成环时只取能走到的部分。
func boardModulePaths(modules []model.WorkModule, byID map[uint]*model.WorkModule) map[uint]string {
	out := make(map[uint]string, len(modules))
	for i := range modules {
		m := &modules[i]
		parts := []string{m.Name}
		cur := m
		for depth := 0; cur.ParentID != nil && depth < 8; depth++ {
			p := byID[*cur.ParentID]
			if p == nil {
				break
			}
			parts = append([]string{p.Name}, parts...)
			cur = p
		}
		out[m.ID] = strings.Join(parts, model.WorkModulePathSep)
	}
	return out
}
