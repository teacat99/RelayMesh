package store

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/teacat99/RelayMesh/v2/internal/model"
	"gorm.io/gorm"
)

// 任务的模块规划（WorkModule）。一个工作流一棵最多两层的树：模块 → 功能。
//
//   - 名称同级不区分大小写不重名，不含路径分隔符；任务只接受「已存在的路径」，所以标注永远统一；
//   - 删除模块不删任务：任务回到上一级（顶层模块则变成未归类）；
//   - 智能体用 ApplyWorkPlan 一次声明整棵树：幂等、整体成功或整体失败；默认只追加不重排，reorder=true 才按给定顺序排。

// WorkModulePatch 是修改模块的参数：指针为 nil 的字段保持不变。
type WorkModulePatch struct {
	Name        *string
	Description *string
}

// WorkPlanNode 是一次「规划」调用里的一个节点（模块或功能）。
type WorkPlanNode struct {
	Name        string
	Description *string // nil = 不改说明
	Rename      string  // 非空 = 把 Name 改成它
	Remove      bool    // 移除这个节点（必须没有任务也没有剩余的功能）
	Features    []WorkPlanNode
}

// WorkPlanResult 是规划调用的结果摘要（节点都用完整路径表示）。
type WorkPlanResult struct {
	Created   []string `json:"created,omitempty"`
	Renamed   []string `json:"renamed,omitempty"`
	Updated   []string `json:"updated,omitempty"`
	Removed   []string `json:"removed,omitempty"`
	Unchanged int      `json:"unchanged,omitempty"`
	// Reordered 表示 reorder=true 时同级顺序确实发生了变化。
	Reordered bool `json:"reordered,omitempty"`
}

// Changed 表示这次规划是否真的改动了规划树（新建 / 改名 / 改说明 / 移除 / 顺序变化）。
func (r *WorkPlanResult) Changed() bool {
	return len(r.Created)+len(r.Renamed)+len(r.Updated)+len(r.Removed) > 0 || r.Reordered
}

// WorkModuleList 是一个工作流的规划树（按树的顺序拍平）与「未归类」的任务汇总。
type WorkModuleList struct {
	Modules    []model.WorkModule    `json:"modules"`
	Unassigned model.WorkModuleStats `json:"unassigned"`
}

// WorkModuleDeleteResult 是删除模块的结果。
type WorkModuleDeleteResult struct {
	Module          model.WorkModule `json:"module"`
	MovedTasks      int              `json:"moved_tasks"`
	RemovedFeatures int              `json:"removed_features"`
}

// WorkDigest 是工作流任务清单的一句话概况，供回执头与概览使用。
type WorkDigest struct {
	Counts  map[string]int
	Total   int
	Modules int
	// Unassigned 是还没有归入任何模块的未结束任务数（只数叶子任务，与模块汇总的口径一致）。
	Unassigned int
	// Latest 是最近更新的未结束任务（没有时为 nil）。
	Latest *model.WorkItem
}

// moduleNode / moduleForest 是规划树在内存里的形态，所有结构性规则都在它上面执行。
type moduleNode struct {
	m        model.WorkModule
	children []*moduleNode
}

type moduleForest struct {
	// board 是这棵树所在的任务板（board_key）；新节点写在这张板上。
	board string
	tops  []*moduleNode
	byID  map[uint]*moduleNode
	// pendingOrder 记录规划调用里各组同级「按计划给出的顺序」，reorder=true 时在末尾统一重排。
	pendingOrder []orderGroup
}

// orderGroup 是一组同级节点按计划给出的顺序。
type orderGroup struct {
	parent    *moduleNode
	mentioned []*moduleNode
}

func (n *moduleNode) isTop() bool { return n.m.ParentID == nil }

// loadModuleForest 读一张任务板的规划树：同一个项目的所有工作流共用一棵树（#175）。
func loadModuleForest(tx *gorm.DB, boardKey string) (*moduleForest, error) {
	var rows []model.WorkModule
	if err := tx.Where("board_key = ?", boardKey).Order("sort ASC, id ASC").Find(&rows).Error; err != nil {
		return nil, err
	}
	f := &moduleForest{board: boardKey, byID: make(map[uint]*moduleNode, len(rows))}
	for i := range rows {
		f.byID[rows[i].ID] = &moduleNode{m: rows[i]}
	}
	for i := range rows {
		n := f.byID[rows[i].ID]
		if n.m.ParentID == nil {
			f.tops = append(f.tops, n)
			continue
		}
		if p, ok := f.byID[*n.m.ParentID]; ok && p.isTop() {
			p.children = append(p.children, n)
		}
		// 找不到父节点的游离功能不会出现在树里（删除模块时功能一并删除，正常不会产生）。
	}
	return f, nil
}

func (f *moduleForest) siblings(parent *moduleNode) []*moduleNode {
	if parent == nil {
		return f.tops
	}
	return parent.children
}

func (f *moduleForest) setSiblings(parent *moduleNode, list []*moduleNode) {
	if parent == nil {
		f.tops = list
		return
	}
	parent.children = list
}

func findSibling(list []*moduleNode, name string) *moduleNode {
	for _, n := range list {
		if strings.EqualFold(n.m.Name, name) {
			return n
		}
	}
	return nil
}

func (f *moduleForest) pathOf(n *moduleNode) string {
	if n.m.ParentID != nil {
		if p, ok := f.byID[*n.m.ParentID]; ok {
			return model.JoinModulePath(p.m.Name, n.m.Name)
		}
	}
	return n.m.Name
}

// flat 按树的顺序拍平（顶层模块在前，紧跟它的功能），并填好 Path / Depth。
func (f *moduleForest) flat() []model.WorkModule {
	out := make([]model.WorkModule, 0, len(f.byID))
	for _, top := range f.tops {
		m := top.m
		m.Path, m.Depth = m.Name, 1
		out = append(out, m)
		for _, c := range top.children {
			cm := c.m
			cm.Path, cm.Depth = model.JoinModulePath(top.m.Name, c.m.Name), 2
			out = append(out, cm)
		}
	}
	return out
}

func (f *moduleForest) paths() []string {
	flat := f.flat()
	out := make([]string, 0, len(flat))
	for _, m := range flat {
		out = append(out, m.Path)
	}
	return out
}

// resolve 把用户 / 智能体给的路径解析成节点：先按完整路径精确匹配（不区分大小写）；
// 只给一段且顶层没有同名模块时，若全树只有一个叫这个名字的功能，也接受（省得每次写全路径）。
func (f *moduleForest) resolve(ref string) (*moduleNode, error) {
	segs := model.SplitModulePath(ref)
	if len(segs) == 0 {
		return nil, NewInvalidInputError("模块路径不能为空")
	}
	if len(segs) > model.WorkModuleDepthMax {
		return nil, NewInvalidInputError(fmt.Sprintf("模块路径最多 %d 层（模块/功能），收到「%s」", model.WorkModuleDepthMax, ref))
	}
	top := findSibling(f.tops, segs[0])
	if len(segs) == 2 {
		if top != nil {
			if c := findSibling(top.children, segs[1]); c != nil {
				return c, nil
			}
		}
		return nil, f.notFoundError(ref)
	}
	if top != nil {
		return top, nil
	}
	var hit []*moduleNode
	for _, t := range f.tops {
		if c := findSibling(t.children, segs[0]); c != nil {
			hit = append(hit, c)
		}
	}
	switch len(hit) {
	case 1:
		return hit[0], nil
	case 0:
		return nil, f.notFoundError(ref)
	}
	cands := make([]string, 0, len(hit))
	for _, h := range hit {
		cands = append(cands, f.pathOf(h))
	}
	return nil, NewInvalidInputError(fmt.Sprintf("「%s」有歧义，请写完整路径：%s", ref, strings.Join(cands, "、")))
}

func (f *moduleForest) notFoundError(ref string) error {
	paths := f.paths()
	if len(paths) == 0 {
		return NewInvalidInputError(fmt.Sprintf("模块「%s」不存在：这个工作流还没有模块规划。先用 task_board action=plan 建立（例如 modules=[{name:\"终端\",features:[{name:\"历史日志\"}]}]），再标注任务", ref))
	}
	const show = 40
	list := strings.Join(paths, "、")
	if len(paths) > show {
		list = strings.Join(paths[:show], "、") + fmt.Sprintf("……（共 %d 个）", len(paths))
	}
	return NewInvalidInputError(fmt.Sprintf("模块「%s」不存在。已有：%s。先在已有路径里找最贴近的；确实没有对应的功能，再用 task_board action=plan 追加（按项目功能域命名，不要按轮次 / 日期 / 会话 / 阶段），不要随手写新名字", ref, list))
}

// add 在 parent 下追加一个节点（parent 为 nil = 顶层模块），执行名称、数量与深度规则。
// workflowID 是创建者所在的工作流（只记录来历，节点属于 f.board 这张板）。
func (f *moduleForest) add(tx *gorm.DB, workflowID string, parent *moduleNode, name, desc string) (*moduleNode, error) {
	if parent != nil && !parent.isTop() {
		return nil, NewInvalidInputError(fmt.Sprintf("规划树最多 %d 层（模块/功能）：「%s」是功能，不能再往下建", model.WorkModuleDepthMax, f.pathOf(parent)))
	}
	sibs := f.siblings(parent)
	if findSibling(sibs, name) != nil {
		where := "模块"
		if parent != nil {
			where = fmt.Sprintf("「%s」下的功能", parent.m.Name)
		}
		return nil, NewInvalidInputError(fmt.Sprintf("已经有叫「%s」的%s（名称同级不区分大小写，不能重名）", name, where))
	}
	if parent == nil && len(sibs) >= model.WorkModulesMax {
		return nil, NewInvalidInputError(fmt.Sprintf("一张任务板最多 %d 个模块；请合并或删除不用的模块", model.WorkModulesMax))
	}
	if parent != nil && len(sibs) >= model.WorkFeaturesMax {
		return nil, NewInvalidInputError(fmt.Sprintf("一个模块下最多 %d 个功能；请拆分模块或删除不用的功能", model.WorkFeaturesMax))
	}
	m := model.WorkModule{WorkflowID: workflowID, BoardKey: f.board, Name: name, Description: desc, Sort: len(sibs)}
	if parent != nil {
		pid := parent.m.ID
		m.ParentID = &pid
	}
	if err := tx.Create(&m).Error; err != nil {
		return nil, err
	}
	n := &moduleNode{m: m}
	f.byID[m.ID] = n
	f.setSiblings(parent, append(sibs, n))
	return n, nil
}

// renameCheck 校验把 n 改名为 name 是否合法（同级不重名）。
func (f *moduleForest) renameCheck(n *moduleNode, name string) error {
	var parent *moduleNode
	if n.m.ParentID != nil {
		parent = f.byID[*n.m.ParentID]
	}
	if dup := findSibling(f.siblings(parent), name); dup != nil && dup != n {
		return NewInvalidInputError(fmt.Sprintf("同一层已经有叫「%s」的节点（名称同级不区分大小写，不能重名）", name))
	}
	return nil
}

// normalize 把有变化的同级顺序写回库：每一组同级的 sort 规整成 0..n-1。
func (f *moduleForest) normalize(tx *gorm.DB) error {
	write := func(list []*moduleNode) error {
		for i, n := range list {
			if n.m.Sort == i {
				continue
			}
			if err := tx.Model(&model.WorkModule{}).Where("id = ?", n.m.ID).UpdateColumn("sort", i).Error; err != nil {
				return err
			}
			n.m.Sort = i
		}
		return nil
	}
	if err := write(f.tops); err != nil {
		return err
	}
	for _, t := range f.tops {
		if err := write(t.children); err != nil {
			return err
		}
	}
	return nil
}

func validateModuleName(name string) (string, error) {
	n := strings.TrimSpace(name)
	if n == "" {
		return "", NewInvalidInputError("模块名不能为空")
	}
	if runeLen(n) > model.WorkModuleNameMax {
		return "", NewInvalidInputError(fmt.Sprintf("模块名最长 %d 个字，现在是 %d 个字（「%s…」）", model.WorkModuleNameMax, runeLen(n), truncateRunes(n, 12)))
	}
	if strings.ContainsAny(n, "/>＞／›→\r\n\t") {
		return "", NewInvalidInputError(fmt.Sprintf("模块名「%s」不能包含 / > → 这类路径分隔符，也不能换行", n))
	}
	return n, nil
}

func validateModuleDesc(desc string) (string, error) {
	d := strings.TrimSpace(desc)
	if err := validateWorkText("模块说明", d, model.WorkModuleDescMax); err != nil {
		return "", err
	}
	return d, nil
}

// workModuleStats 给拍平后的规划树填上任务汇总，并返回「未归类」的汇总。
// 只统计叶子任务（没有子任务的任务）；顶层模块包含它下面各功能的任务。
func (s *Store) workModuleStats(ctx context.Context, boardKey string, mods []model.WorkModule) (model.WorkModuleStats, error) {
	var items []model.WorkItem
	if err := s.db.WithContext(ctx).Select("id", "parent_id", "module_id", "state", "percent").
		Where("board_key = ?", boardKey).Find(&items).Error; err != nil {
		return model.WorkModuleStats{}, err
	}
	hasKids := map[uint]bool{}
	for _, it := range items {
		if it.ParentID != nil {
			hasKids[*it.ParentID] = true
		}
	}
	parentOf := make(map[uint]*uint, len(mods))
	idx := make(map[uint]int, len(mods))
	for i, m := range mods {
		parentOf[m.ID] = m.ParentID
		idx[m.ID] = i
	}
	type acc struct {
		st  model.WorkModuleStats
		sum int
		n   int
	}
	accs := make(map[uint]*acc, len(mods))
	var unassigned acc
	feed := func(a *acc, state string, pct int) {
		a.st.Total++
		switch state {
		case model.WorkStateTodo:
			a.st.Todo++
		case model.WorkStateDoing:
			a.st.Doing++
		case model.WorkStateReview:
			a.st.Review++
		case model.WorkStateDone:
			a.st.Done++
		case model.WorkStateShelved:
			a.st.Shelved++
		}
		if state != model.WorkStateShelved {
			a.sum += pct
			a.n++
		}
	}
	for _, it := range items {
		if hasKids[it.ID] {
			continue
		}
		pct, _ := model.EffectivePercent(it.State, it.Percent, 0, 0)
		if it.ModuleID == nil {
			feed(&unassigned, it.State, pct)
			continue
		}
		if _, ok := idx[*it.ModuleID]; !ok {
			feed(&unassigned, it.State, pct) // 指向已不存在的模块：按未归类计
			continue
		}
		targets := []uint{*it.ModuleID}
		if p := parentOf[*it.ModuleID]; p != nil {
			targets = append(targets, *p)
		}
		for _, id := range targets {
			a := accs[id]
			if a == nil {
				a = &acc{}
				accs[id] = a
			}
			feed(a, it.State, pct)
		}
	}
	finish := func(a *acc) model.WorkModuleStats {
		st := a.st
		if a.n > 0 {
			st.Percent = (a.sum + a.n/2) / a.n
		}
		return st
	}
	for i := range mods {
		if a := accs[mods[i].ID]; a != nil {
			mods[i].Stats = finish(a)
		}
	}
	return finish(&unassigned), nil
}

// ListWorkModules 返回工作流所在任务板的规划树（按树的顺序拍平，含任务汇总）与未归类的汇总。
func (s *Store) ListWorkModules(ctx context.Context, workflowID string) (*WorkModuleList, error) {
	workflowID = strings.TrimSpace(workflowID)
	if workflowID == "" {
		return nil, NewInvalidInputError("workflow_id 不能为空")
	}
	boardKey, err := boardKeyOn(ctx, s.db, workflowID)
	if err != nil {
		return nil, err
	}
	return s.listWorkModulesOnBoard(ctx, boardKey)
}

func (s *Store) listWorkModulesOnBoard(ctx context.Context, boardKey string) (*WorkModuleList, error) {
	f, err := loadModuleForest(s.db.WithContext(ctx), boardKey)
	if err != nil {
		return nil, err
	}
	mods := f.flat()
	unassigned, err := s.workModuleStats(ctx, boardKey, mods)
	if err != nil {
		return nil, err
	}
	return &WorkModuleList{Modules: mods, Unassigned: unassigned}, nil
}

// moduleBoard 返回模块所在的板：已归板的直接用自己的 board_key；没归板的（旧版本回滚期间写入）按创建者解析。
func moduleBoard(ctx context.Context, db *gorm.DB, m *model.WorkModule) (string, error) {
	if m.BoardKey != "" {
		return m.BoardKey, nil
	}
	return boardKeyOn(ctx, db, m.WorkflowID)
}

// GetWorkModule 读取单个模块（含完整路径，不含任务汇总）。
func (s *Store) GetWorkModule(ctx context.Context, id uint) (*model.WorkModule, error) {
	var m model.WorkModule
	if err := s.db.WithContext(ctx).First(&m, id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, NewNotFoundError("模块不存在")
		}
		return nil, err
	}
	boardKey, err := moduleBoard(ctx, s.db, &m)
	if err != nil {
		return nil, err
	}
	f, err := loadModuleForest(s.db.WithContext(ctx), boardKey)
	if err != nil {
		return nil, err
	}
	if n, ok := f.byID[m.ID]; ok {
		m.Path = f.pathOf(n)
		m.Depth = 1
		if !n.isTop() {
			m.Depth = 2
		}
	}
	return &m, nil
}

// ResolveWorkModule 按路径解析模块（见 moduleForest.resolve）。找不到时错误里带着已有的路径清单。
func (s *Store) ResolveWorkModule(ctx context.Context, workflowID, ref string) (*model.WorkModule, error) {
	boardKey, err := boardKeyOn(ctx, s.db, strings.TrimSpace(workflowID))
	if err != nil {
		return nil, err
	}
	f, err := loadModuleForest(s.db.WithContext(ctx), boardKey)
	if err != nil {
		return nil, err
	}
	n, err := f.resolve(ref)
	if err != nil {
		return nil, err
	}
	m := n.m
	m.Path = f.pathOf(n)
	m.Depth = 1
	if !n.isTop() {
		m.Depth = 2
	}
	return &m, nil
}

// CreateWorkModule 在规划树上新建一个模块（parentID=0）或功能（parentID=某个顶层模块），排在同级最后。
func (s *Store) CreateWorkModule(ctx context.Context, workflowID string, parentID uint, name, description string) (*model.WorkModule, error) {
	workflowID = strings.TrimSpace(workflowID)
	if workflowID == "" {
		return nil, NewInvalidInputError("workflow_id 不能为空")
	}
	if runeLen(workflowID) > 128 {
		return nil, NewInvalidInputError("workflow_id 过长")
	}
	n, err := validateModuleName(name)
	if err != nil {
		return nil, err
	}
	desc, err := validateModuleDesc(description)
	if err != nil {
		return nil, err
	}
	var id uint
	err = s.WithTx(ctx, func(tx *gorm.DB) error {
		boardKey, err := boardKeyOn(ctx, tx, workflowID)
		if err != nil {
			return err
		}
		f, err := loadModuleForest(tx, boardKey)
		if err != nil {
			return err
		}
		var parent *moduleNode
		if parentID != 0 {
			p, ok := f.byID[parentID]
			if !ok {
				return NewNotFoundError("上级模块不存在，或不在这张任务板上")
			}
			parent = p
		}
		node, err := f.add(tx, workflowID, parent, n, desc)
		if err != nil {
			return err
		}
		id = node.m.ID
		return nil
	})
	if err != nil {
		return nil, err
	}
	return s.GetWorkModule(ctx, id)
}

// UpdateWorkModule 修改模块的名称或说明（不动位置与归属）。
func (s *Store) UpdateWorkModule(ctx context.Context, id uint, patch WorkModulePatch) (*model.WorkModule, error) {
	err := s.WithTx(ctx, func(tx *gorm.DB) error {
		var cur model.WorkModule
		if err := tx.First(&cur, id).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return NewNotFoundError("模块不存在")
			}
			return err
		}
		boardKey, err := moduleBoard(ctx, tx, &cur)
		if err != nil {
			return err
		}
		f, err := loadModuleForest(tx, boardKey)
		if err != nil {
			return err
		}
		node, ok := f.byID[id]
		if !ok {
			return NewNotFoundError("模块不在规划树里（上级已被删除）")
		}
		changed := false
		if patch.Name != nil {
			name, err := validateModuleName(*patch.Name)
			if err != nil {
				return err
			}
			if name != node.m.Name {
				if err := f.renameCheck(node, name); err != nil {
					return err
				}
				node.m.Name, changed = name, true
			}
		}
		if patch.Description != nil {
			d, err := validateModuleDesc(*patch.Description)
			if err != nil {
				return err
			}
			if d != node.m.Description {
				node.m.Description, changed = d, true
			}
		}
		if !changed {
			return nil
		}
		return tx.Model(&model.WorkModule{}).Where("id = ?", id).
			Updates(map[string]any{"name": node.m.Name, "description": node.m.Description}).Error
	})
	if err != nil {
		return nil, err
	}
	return s.GetWorkModule(ctx, id)
}

// MoveWorkModule 在同级里上移（delta<0）或下移（delta>0）一格；已在边界时什么也不做。返回最新的规划树。
func (s *Store) MoveWorkModule(ctx context.Context, id uint, delta int) (*WorkModuleList, error) {
	var boardKey string
	err := s.WithTx(ctx, func(tx *gorm.DB) error {
		var cur model.WorkModule
		if err := tx.First(&cur, id).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return NewNotFoundError("模块不存在")
			}
			return err
		}
		var err error
		if boardKey, err = moduleBoard(ctx, tx, &cur); err != nil {
			return err
		}
		f, err := loadModuleForest(tx, boardKey)
		if err != nil {
			return err
		}
		node, ok := f.byID[id]
		if !ok {
			return NewNotFoundError("模块不在规划树里（上级已被删除）")
		}
		var parent *moduleNode
		if node.m.ParentID != nil {
			parent = f.byID[*node.m.ParentID]
		}
		sibs := f.siblings(parent)
		i := -1
		for k, n := range sibs {
			if n == node {
				i = k
				break
			}
		}
		j := i
		switch {
		case delta < 0:
			j = i - 1
		case delta > 0:
			j = i + 1
		}
		if i < 0 || j < 0 || j >= len(sibs) || j == i {
			return nil
		}
		sibs[i], sibs[j] = sibs[j], sibs[i]
		return f.normalize(tx)
	})
	if err != nil {
		return nil, err
	}
	return s.listWorkModulesOnBoard(ctx, boardKey)
}

// DeleteWorkModule 删除模块（顶层模块连同它的功能）。任务不会被删：
// 属于被删节点的任务回到它的上一级；顶层模块被删时变成未归类。
func (s *Store) DeleteWorkModule(ctx context.Context, id uint) (*WorkModuleDeleteResult, error) {
	out := &WorkModuleDeleteResult{}
	err := s.WithTx(ctx, func(tx *gorm.DB) error {
		var cur model.WorkModule
		if err := tx.First(&cur, id).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return NewNotFoundError("模块不存在")
			}
			return err
		}
		boardKey, err := moduleBoard(ctx, tx, &cur)
		if err != nil {
			return err
		}
		f, err := loadModuleForest(tx, boardKey)
		if err != nil {
			return err
		}
		node, ok := f.byID[id]
		if !ok {
			// 游离节点（上级已不在）：直接清掉，任务按未归类处理。
			node = &moduleNode{m: cur}
		}
		out.Module = node.m
		out.Module.Path = f.pathOf(node)
		ids := []uint{id}
		for _, c := range node.children {
			ids = append(ids, c.m.ID)
		}
		out.RemovedFeatures = len(node.children)

		var fallback any // nil → NULL（未归类）
		if node.m.ParentID != nil {
			if _, ok := f.byID[*node.m.ParentID]; ok {
				fallback = *node.m.ParentID
			}
		}
		res := tx.Model(&model.WorkItem{}).Where("module_id IN ?", ids).UpdateColumn("module_id", fallback)
		if res.Error != nil {
			return res.Error
		}
		out.MovedTasks = int(res.RowsAffected)
		if err := tx.Where("id IN ?", ids).Delete(&model.WorkModule{}).Error; err != nil {
			return err
		}
		// 把被删节点从内存树里摘掉，再规整同级顺序。
		for _, did := range ids {
			delete(f.byID, did)
		}
		if node.isTop() {
			kept := make([]*moduleNode, 0, len(f.tops))
			for _, t := range f.tops {
				if t.m.ID != id {
					kept = append(kept, t)
				}
			}
			f.tops = kept
		} else if p, ok := f.byID[*node.m.ParentID]; ok {
			kept := make([]*moduleNode, 0, len(p.children))
			for _, c := range p.children {
				if c.m.ID != id {
					kept = append(kept, c)
				}
			}
			p.children = kept
		}
		return f.normalize(tx)
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// planPath 返回计划节点的展示路径。
func planPath(parent *moduleNode, name string) string {
	if parent == nil {
		return name
	}
	return model.JoinModulePath(parent.m.Name, name)
}

// applyPlanNode 把一个计划节点落到规划树上，返回落地后的节点（被移除或不存在时为 nil）。
func (f *moduleForest) applyPlanNode(tx *gorm.DB, workflowID string, parent *moduleNode, pn WorkPlanNode, res *WorkPlanResult) (*moduleNode, error) {
	name, err := validateModuleName(pn.Name)
	if err != nil {
		return nil, err
	}
	label := planPath(parent, name)
	existing := findSibling(f.siblings(parent), name)

	if pn.Remove {
		if existing == nil {
			res.Unchanged++
			return nil, nil
		}
		for _, cpn := range pn.Features {
			if _, err := f.applyPlanNode(tx, workflowID, existing, cpn, res); err != nil {
				return nil, err
			}
		}
		if len(existing.children) > 0 {
			return nil, NewInvalidInputError(fmt.Sprintf("「%s」下还有 %d 个功能，不能直接移除；先把这些功能也标 remove", label, len(existing.children)))
		}
		var n int64
		if err := tx.Model(&model.WorkItem{}).Where("module_id = ?", existing.m.ID).Count(&n).Error; err != nil {
			return nil, err
		}
		if n > 0 {
			return nil, NewInvalidInputError(fmt.Sprintf("「%s」下还有 %d 个任务，不能直接移除；先用 batch 把它们改到别的模块", label, n))
		}
		if err := tx.Delete(&model.WorkModule{}, existing.m.ID).Error; err != nil {
			return nil, err
		}
		delete(f.byID, existing.m.ID)
		kept := make([]*moduleNode, 0, len(f.siblings(parent)))
		for _, sb := range f.siblings(parent) {
			if sb != existing {
				kept = append(kept, sb)
			}
		}
		f.setSiblings(parent, kept)
		res.Removed = append(res.Removed, label)
		return nil, nil
	}

	var desc string
	if pn.Description != nil {
		if desc, err = validateModuleDesc(*pn.Description); err != nil {
			return nil, err
		}
	}

	node := existing
	touched := false // 这个节点本身是否发生了可见变化（新建 / 改名 / 改说明）
	if newName := strings.TrimSpace(pn.Rename); newName != "" {
		newName, err = validateModuleName(newName)
		if err != nil {
			return nil, err
		}
		switch {
		case existing != nil && existing.m.Name != newName:
			if err := f.renameCheck(existing, newName); err != nil {
				return nil, err
			}
			if err := tx.Model(&model.WorkModule{}).Where("id = ?", existing.m.ID).Update("name", newName).Error; err != nil {
				return nil, err
			}
			res.Renamed = append(res.Renamed, label+"→"+newName)
			existing.m.Name = newName
			label = planPath(parent, newName)
			touched = true
		case existing == nil:
			dup := findSibling(f.siblings(parent), newName)
			if dup == nil {
				return nil, NewInvalidInputError(fmt.Sprintf("找不到要改名的「%s」，也没有叫「%s」的节点", label, newName))
			}
			node = dup // 已经改过名：重复提交同一份计划时保持幂等
			label = planPath(parent, newName)
		}
	}

	created := false
	if node == nil {
		if node, err = f.add(tx, workflowID, parent, name, desc); err != nil {
			return nil, err
		}
		res.Created = append(res.Created, label)
		created, touched = true, true
	}
	if pn.Description != nil && !created && desc != node.m.Description {
		if err := tx.Model(&model.WorkModule{}).Where("id = ?", node.m.ID).Update("description", desc).Error; err != nil {
			return nil, err
		}
		node.m.Description = desc
		res.Updated = append(res.Updated, label)
		touched = true
	}
	if !touched {
		res.Unchanged++
	}

	if len(pn.Features) > 0 {
		if parent != nil {
			return nil, NewInvalidInputError(fmt.Sprintf("「%s」是功能，下面不能再有子级（最多 %d 层：模块/功能）", label, model.WorkModuleDepthMax))
		}
		var mentioned []*moduleNode
		for _, cpn := range pn.Features {
			c, err := f.applyPlanNode(tx, workflowID, node, cpn, res)
			if err != nil {
				return nil, err
			}
			if c != nil {
				mentioned = append(mentioned, c)
			}
		}
		f.pendingOrder = append(f.pendingOrder, orderGroup{parent: node, mentioned: mentioned})
	}
	return node, nil
}

// ApplyWorkPlan 把一份规划声明落到工作流所在任务板的规划树上（同一个项目的工作流共用一棵树）：
//   - 不存在的节点新建（追加在同级末尾），已存在的保持原位，所以重复提交同一份计划是幂等的；
//   - 带 rename 的节点改名，带 remove 的节点移除（必须没有任务也没有剩余功能），description 不为空时更新说明；
//   - reorder=true 时，计划里列出的同级节点按给定顺序排到前面（没列出的保持原有相对顺序，跟在后面）；
//   - 整个调用在一个事务里：任何一个节点不合法，整份计划都不生效。
func (s *Store) ApplyWorkPlan(ctx context.Context, workflowID string, plan []WorkPlanNode, reorder bool) (*WorkPlanResult, error) {
	workflowID = strings.TrimSpace(workflowID)
	if workflowID == "" {
		return nil, NewInvalidInputError("workflow_id 不能为空")
	}
	if runeLen(workflowID) > 128 {
		return nil, NewInvalidInputError("workflow_id 过长")
	}
	if len(plan) == 0 {
		return nil, NewInvalidInputError("modules 不能为空：至少给出一个模块，例如 [{name:\"终端\",features:[{name:\"历史日志\"}]}]")
	}
	res := &WorkPlanResult{}
	err := s.WithTx(ctx, func(tx *gorm.DB) error {
		*res = WorkPlanResult{}
		boardKey, err := boardKeyOn(ctx, tx, workflowID)
		if err != nil {
			return err
		}
		f, err := loadModuleForest(tx, boardKey)
		if err != nil {
			return err
		}
		var mentionedTops []*moduleNode
		for _, pn := range plan {
			n, err := f.applyPlanNode(tx, workflowID, nil, pn, res)
			if err != nil {
				return err
			}
			if n != nil {
				mentionedTops = append(mentionedTops, n)
			}
		}
		if reorder {
			if f.reorder(nil, mentionedTops) {
				res.Reordered = true
			}
			for _, g := range f.pendingOrder {
				if f.reorder(g.parent, g.mentioned) {
					res.Reordered = true
				}
			}
		}
		f.pendingOrder = nil
		return f.normalize(tx)
	})
	if err != nil {
		return nil, err
	}
	return res, nil
}

// reorder 让 mentioned（按给定顺序）排到同级最前面，其余保持原有相对顺序；返回顺序是否真的变了。
func (f *moduleForest) reorder(parent *moduleNode, mentioned []*moduleNode) bool {
	if len(mentioned) == 0 {
		return false
	}
	sibs := f.siblings(parent)
	in := make(map[*moduleNode]bool, len(mentioned))
	out := make([]*moduleNode, 0, len(sibs))
	for _, n := range mentioned {
		if !in[n] {
			in[n] = true
			out = append(out, n)
		}
	}
	for _, n := range sibs {
		if !in[n] {
			out = append(out, n)
		}
	}
	changed := false
	for i := range out {
		if out[i] != sibs[i] {
			changed = true
			break
		}
	}
	f.setSiblings(parent, out)
	return changed
}

// workModulePaths 返回一批任务板里 模块 ID → 完整路径 的映射，给任务列表补 ModulePath 用。
func (s *Store) workModulePaths(ctx context.Context, boardKeys []string) (map[uint]string, error) {
	out := map[uint]string{}
	if len(boardKeys) == 0 {
		return out, nil
	}
	var rows []model.WorkModule
	if err := s.db.WithContext(ctx).Select("id", "board_key", "parent_id", "name").
		Where("board_key IN ?", boardKeys).Find(&rows).Error; err != nil {
		return nil, err
	}
	byID := make(map[uint]model.WorkModule, len(rows))
	for _, r := range rows {
		byID[r.ID] = r
	}
	for _, r := range rows {
		if r.ParentID == nil {
			out[r.ID] = r.Name
			continue
		}
		if p, ok := byID[*r.ParentID]; ok {
			out[r.ID] = model.JoinModulePath(p.Name, r.Name)
		}
	}
	return out, nil
}

// checkWorkModule 校验任务要归入的模块：必须存在并且和任务在同一张任务板上。
func checkWorkModule(tx *gorm.DB, boardKey string, moduleID uint) error {
	if moduleID == 0 {
		return nil
	}
	var m model.WorkModule
	if err := tx.Select("id", "board_key").First(&m, moduleID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return NewInvalidInputError("模块不存在（可能刚被删除），请刷新规划后重试")
		}
		return err
	}
	if m.BoardKey != boardKey {
		if isProjectBoard(boardKey) {
			return NewInvalidInputError("模块必须属于任务所在的任务板（同一个项目）")
		}
		return NewInvalidInputError("模块必须属于任务所在的工作流")
	}
	return nil
}

// validateWorkPercent 校验显式进度：0–100，或 -1 表示清除（回到由状态 / 子任务推导）。
func validateWorkPercent(p int) error {
	if p < -1 || p > 100 {
		return NewInvalidInputError(fmt.Sprintf("percent 必须在 0–100 之间（-1 表示清除，回到由状态推导），收到 %d", p))
	}
	return nil
}

// WorkModuleScope 返回一个模块及其下属功能的 ID（任务列表按模块筛选时包含功能里的任务）。
func (s *Store) WorkModuleScope(ctx context.Context, moduleID uint) ([]uint, error) {
	ids := []uint{moduleID}
	var kids []uint
	if err := s.db.WithContext(ctx).Model(&model.WorkModule{}).Where("parent_id = ?", moduleID).Pluck("id", &kids).Error; err != nil {
		return nil, err
	}
	return append(ids, kids...), nil
}

// CountWorkModules 统计工作流所在任务板的模块 / 功能节点数。
func (s *Store) CountWorkModules(ctx context.Context, workflowID string) (int, error) {
	boardKey, err := boardKeyOn(ctx, s.db, workflowID)
	if err != nil {
		return 0, err
	}
	var n int64
	if err := s.db.WithContext(ctx).Model(&model.WorkModule{}).Where("board_key = ?", boardKey).Count(&n).Error; err != nil {
		return 0, err
	}
	return int(n), nil
}

// WorkDigestFor 汇总工作流所在任务板的概况：各状态数量、规划节点数与最近更新的未结束任务。
// 同一个项目的工作流共用一张板，所以概况是整张板的；单工作流的板与按工作流统计完全一致。
func (s *Store) WorkDigestFor(ctx context.Context, workflowID string) (*WorkDigest, error) {
	boardKey, err := boardKeyOn(ctx, s.db, workflowID)
	if err != nil {
		return nil, err
	}
	counts, err := s.countItemsByStateOnBoard(ctx, boardKey)
	if err != nil {
		return nil, err
	}
	d := &WorkDigest{Counts: counts}
	for _, n := range counts {
		d.Total += n
	}
	var modules int64
	if err := s.db.WithContext(ctx).Model(&model.WorkModule{}).Where("board_key = ?", boardKey).Count(&modules).Error; err != nil {
		return nil, err
	}
	d.Modules = int(modules)
	if d.Total > 0 {
		var n int64
		// 叶子任务（没有子任务的）里，未结束且没有模块的；批次任务本身不重复计。
		if err := s.db.WithContext(ctx).Model(&model.WorkItem{}).
			Where("board_key = ? AND state NOT IN ('done','shelved') AND (module_id IS NULL OR module_id = 0)", boardKey).
			Where("NOT EXISTS (SELECT 1 FROM work_items c WHERE c.parent_id = work_items.id)").
			Count(&n).Error; err != nil {
			return nil, err
		}
		d.Unassigned = int(n)
	}
	if d.Counts[model.WorkStateTodo]+d.Counts[model.WorkStateDoing]+d.Counts[model.WorkStateReview] > 0 {
		var it model.WorkItem
		err := s.db.WithContext(ctx).Where("board_key = ? AND state NOT IN ('done','shelved')", boardKey).
			Order("updated_at DESC, id DESC").First(&it).Error
		switch {
		case err == nil:
			items := []model.WorkItem{it}
			if err := s.annotateWorkItems(ctx, items); err == nil {
				it = items[0]
			}
			d.Latest = &it
		case errors.Is(err, gorm.ErrRecordNotFound):
		default:
			return nil, err
		}
	}
	return d, nil
}
