package store

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/teacat99/RelayMesh/v2/internal/model"
	"gorm.io/gorm"
)

// 任务（WorkItem）的存取。规则集中在这里，REST 与 MCP 两个入口只负责把参数翻译过来：
//
//   - 序号 Seq 按工作流单调递增、不复用；
//   - 状态绑定智能体自己的开发流程（#110）：待办 → 进行中（开发）→ 验证中（跑回归 / e2e / 热更冒烟）→ 已完成。
//     验证全部通过后由智能体直接置「已完成」，不再逐条等用户点头；代价是必须写结论（做了什么、怎么验证的），
//     没有结论的「已完成」不收——结论就是验证过的证据，也是任务关闭后留下的东西。人在 Web 上可以置任何状态；
//   - 每次有意义的写入都会在时间线上留一条，时间线只保留最近 WorkUpdatesKeep 条；
//   - 关闭超过 WorkCompactAfterDays 天的任务，时间线被压缩掉，只保留标题 / 类型 / 标签 / 日期 / 结论 / 引用 / 关联会话。

// WorkActor 描述一次写入是谁做的、发生在哪个会话里。
type WorkActor struct {
	Kind      string // model.WorkActorHuman 或 model.WorkActorAgent
	SessionID string // 可选
	// 以下三项只对智能体有意义（#175 B2，见 work_assign.go）：人在 Web 上写入恒不做任务级判定。
	WorkflowID string     // 智能体自己的工作流（MCP 的 workflow_id）；用来判定「是不是自己的任务」，也记在时间线上
	Manager    bool       // 持有该项目任务板的管理授权（由 MCP 层按授权表判定后填入）
	Policy     WorkPolicy // 任务级权限策略；零值 = 不检查
}

func (a WorkActor) isAgent() bool { return a.Kind == model.WorkActorAgent }

// workflowRef 是写进时间线的操作者工作流：只有智能体有，人在 Web 上写入为空。
func (a WorkActor) workflowRef() string {
	if !a.isAgent() {
		return ""
	}
	return strings.TrimSpace(a.WorkflowID)
}

func (a WorkActor) kind() string {
	if a.Kind == model.WorkActorAgent {
		return model.WorkActorAgent
	}
	return model.WorkActorHuman
}

// WorkItemInput 是新建任务的参数。
type WorkItemInput struct {
	WorkflowID string
	Title      string
	Kind       string // 空 = feature
	Stage      string // 空 = 未标注
	State      string // 空 = todo
	Labels     []string
	Refs       []string
	// Description 是任务内容的说明（这件事是什么、做到什么算完成）；标题和进度都说不清时靠它。
	Description string
	Progress    string
	Note        string
	ParentID    uint // 0 = 没有父任务
	ModuleID    uint // 0 = 未归类
	Percent     *int // nil = 由状态 / 子任务推导；0–100 = 显式完成度
	// Assignee 非空 = 创建时同时指派给这个工作流（管理 Agent / 人；必须是同一张项目板上的工作流）。
	Assignee string
}

// WorkItemPatch 是更新任务的参数：指针为 nil 的字段保持不变。
type WorkItemPatch struct {
	Title  *string
	Kind   *string
	Stage  *string // 指向空串 = 清除（回到未标注）
	State  *string
	Labels *[]string
	Refs   *[]string
	// Description 指向空串 = 清除；只改描述不动 updated_at、不写时间线（见 model.WorkItem.Description）。
	Description *string
	Progress    *string
	Conclusion  *string
	ParentID    *uint // 指向 0 表示解除父任务
	ModuleID    *uint // 指向 0 表示取消归类
	Percent     *int  // -1 表示清除显式完成度（回到由状态推导）
	Note        string
	// Assignee 指向工作流编号 = 指派 / 改派；指向空串 = 撤销指派；nil = 不变（#175 B2）。
	Assignee *string
	// Claim 是显式的协作动作：accept / decline / deliver / accept_delivery / reject_delivery；空 = 无。
	// 与 Assignee 不能同时给。承接者直接改状态也能隐式表达接取 / 交付，见 work_assign.go。
	Claim string
}

// WorkItemResult 是一次写入的结果。
type WorkItemResult struct {
	Item *model.WorkItem
	// Changed 为 false 表示这次调用没有产生任何变化（没有写库）。
	Changed bool
	// SessionLinked 表示本次传入的会话已与任务关联（会话不存在时为 false）。
	SessionLinked bool
	// Violations 只在策略为 audit 时有值：这次写入照常执行了，但若启用强制会被拒绝，每条是一句原因，
	// 调用层据此写审计（would_deny）。
	Violations []string
}

// WorkItemFilter 是列表的筛选条件。
type WorkItemFilter struct {
	WorkflowID string
	States     []string
	Kinds      []string
	Stages     []string // 阶段；包含空串或 "none" 表示「未标注」
	SessionID  string   // 非空：只要与这个会话关联的任务，并附上这个会话对每条任务做了什么（SessionCtx）
	Query      string
	ParentID   *uint
	TopLevel   bool // 只要顶层任务（没有父任务的）
	ModuleID   uint // >0：只要这个模块（含它下面各功能）里的任务
	Unassigned bool // 只要未归类的任务（优先于 ModuleID）
	ClosedDays int  // >0：已关闭的任务只保留最近 N 天内关闭的
	Limit      int  // 0 = 默认 500
	// Assignee：工作流编号 = 只要指派给它的任务；"none" = 只要没有指派的任务；空 = 不过滤（#175 B2）。
	Assignee string
	// AssignedBy：只要这个工作流派出的任务（空 = 不过滤）。
	AssignedBy string
}

// WorkflowStat 是某个工作流下任务的概况，供界面做工作流筛选。
//
// 同一个项目的工作流共用一张任务板（#175）：每个有任务的工作流各占一条，数字是整张板的；
// BoardKey / Members 说明它在哪张板、板上有哪些工作流（只有一个时省略），界面可据此去重。
type WorkflowStat struct {
	WorkflowID string   `json:"workflow_id"`
	BoardKey   string   `json:"board_key,omitempty"`
	Members    []string `json:"workflows,omitempty"`
	// BoardName 是项目板所属项目的名称，只在多个工作流共用这张板时才带（界面给共享板起名用）；
	// 独立板与只有一个工作流的板省略，界面照旧用会话标题 / 工作流编号。
	BoardName string `json:"board_name,omitempty"`
	Total     int    `json:"total"`
	Active    int    `json:"active"`
	// Doing / Review 是会话页头「任务」入口上的计数：进行中的数量，以及验证中的数量（都是智能体手上的活，不需要用户处理）。
	Doing     int       `json:"doing"`
	Review    int       `json:"review"`
	UpdatedAt time.Time `json:"updated_at"`
}

const workItemListDefaultLimit = 500

// WorkItemListLimit 是任务清单一次最多返回多少条：调用方没指定（或指定得不合理、超过 1000）就取默认的 500。
// 清单按最近更新从新到旧取前这么多条；REST 拿它判断清单是不是被截断了（顶到上限 = 更早更新的可能被略去），
// 这样页面能如实说明，而不是悄悄少显示。
func WorkItemListLimit(requested int) int {
	if requested <= 0 || requested > 1000 {
		return workItemListDefaultLimit
	}
	return requested
}

func runeLen(s string) int { return utf8.RuneCountInString(s) }

// WorkStateLabel 返回状态的中文名（与界面一致）；未知值原样返回。
func WorkStateLabel(state string) string {
	switch state {
	case model.WorkStateTodo:
		return "待办"
	case model.WorkStateDoing:
		return "进行中"
	case model.WorkStateReview:
		return "验证中"
	case model.WorkStateDone:
		return "已完成"
	case model.WorkStateShelved:
		return "已搁置"
	}
	return state
}

func truncateRunes(s string, n int) string {
	if runeLen(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n])
}

func validateWorkText(field, value string, max int) error {
	if n := runeLen(value); n > max {
		return NewInvalidInputError(fmt.Sprintf("%s最长 %d 个字，现在是 %d 个字；长内容请放进会话文档，这里只写能定位问题的几行", field, max, n))
	}
	return nil
}

// NormalizeWorkLabels 去空白、去重（不区分大小写）并校验数量与长度。
func NormalizeWorkLabels(in []string) ([]string, error) {
	out := make([]string, 0, len(in))
	seen := map[string]bool{}
	for _, raw := range in {
		l := strings.TrimSpace(raw)
		if l == "" {
			continue
		}
		if strings.ContainsAny(l, "\r\n") {
			return nil, NewInvalidInputError("标签不能包含换行")
		}
		if runeLen(l) > model.WorkLabelMax {
			return nil, NewInvalidInputError(fmt.Sprintf("标签「%s」超过 %d 个字", truncateRunes(l, 12), model.WorkLabelMax))
		}
		key := strings.ToLower(l)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, l)
	}
	if len(out) > model.WorkLabelsMax {
		return nil, NewInvalidInputError(fmt.Sprintf("一个任务最多 %d 个标签", model.WorkLabelsMax))
	}
	return out, nil
}

// NormalizeWorkRefs 去空白、去重并校验数量与长度。引用是自由文本：提交（commit:abc1234）、决策（D-201）、文件路径、文档链接都可以。
func NormalizeWorkRefs(in []string) ([]string, error) {
	out := make([]string, 0, len(in))
	seen := map[string]bool{}
	for _, raw := range in {
		r := strings.TrimSpace(raw)
		if r == "" {
			continue
		}
		if strings.ContainsAny(r, "\r\n") {
			return nil, NewInvalidInputError("引用不能包含换行")
		}
		if runeLen(r) > model.WorkRefMax {
			return nil, NewInvalidInputError(fmt.Sprintf("引用「%s…」超过 %d 个字", truncateRunes(r, 16), model.WorkRefMax))
		}
		if seen[r] {
			continue
		}
		seen[r] = true
		out = append(out, r)
	}
	if len(out) > model.WorkRefsMax {
		return nil, NewInvalidInputError(fmt.Sprintf("一个任务最多 %d 条引用", model.WorkRefsMax))
	}
	return out, nil
}

func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// nextBoardSeq 在事务里为任务板分配下一个序号（#175）。计数器单独存放，删除任务不会让序号回退。
//
// 序号在板内唯一：同一个项目里的多个工作流共用一套 #N，所以一个工作流看到的 #12 和另一个工作流看到的 #12 是同一件事。
// 兼容两件旧事：
//   - 旧的 (workflow_id, seq) 唯一索引仍在，别处已经给同一个创建者用过的号要跳过；
//   - 旧二进制（回滚时）仍按创建者的旧计数 work_item_counters 发号，这里写穿，保证它不会倒回已经发过的号。
func nextBoardSeq(tx *gorm.DB, boardKey, workflowID string) (int, error) {
	var c model.WorkBoardCounter
	err := tx.Where("board_key = ?", boardKey).First(&c).Error
	switch {
	case err == nil:
	case errors.Is(err, gorm.ErrRecordNotFound):
		// 板计数器出现之前就已有任务（手工导入、旧版本回滚期间写入的行）：从现有最大序号接着往下发；
		// 还没归板的旧行（board_key 为空）按创建者算进来，避免和它们撞号。
		var max int
		if e := tx.Model(&model.WorkItem{}).
			Where("board_key = ? OR (board_key = '' AND workflow_id = ?)", boardKey, workflowID).
			Select("COALESCE(MAX(seq), 0)").Scan(&max).Error; e != nil {
			return 0, e
		}
		// 独立板沿用该工作流旧的计数器：任务删光之后序号也不回退。
		if _, wf, ok := ParseBoardKey(boardKey); ok && wf != "" {
			var old model.WorkItemCounter
			switch e := tx.Where("workflow_id = ?", wf).First(&old).Error; {
			case e == nil:
				if old.LastSeq > max {
					max = old.LastSeq
				}
			case !errors.Is(e, gorm.ErrRecordNotFound):
				return 0, e
			}
		}
		c = model.WorkBoardCounter{BoardKey: boardKey, LastSeq: max}
	default:
		return 0, err
	}
	seq := c.LastSeq
	for {
		seq++
		var n int64
		if e := tx.Model(&model.WorkItem{}).Where("workflow_id = ? AND seq = ?", workflowID, seq).Count(&n).Error; e != nil {
			return 0, e
		}
		if n == 0 {
			break
		}
	}
	c.LastSeq = seq
	if err := tx.Save(&c).Error; err != nil {
		return 0, err
	}
	if err := tx.Exec(`INSERT INTO work_item_counters(workflow_id, last_seq) VALUES(?, ?)
		ON CONFLICT(workflow_id) DO UPDATE SET last_seq = MAX(last_seq, excluded.last_seq)`, workflowID, seq).Error; err != nil {
		return 0, err
	}
	return seq, nil
}

// checkWorkParent 校验父任务：同一张板、不能是自己、只允许一层嵌套（父任务自己不能有父任务，有子任务的任务也不能再当子任务）。
func checkWorkParent(tx *gorm.DB, boardKey string, selfID uint, parentID uint) error {
	if parentID == 0 {
		return nil
	}
	if parentID == selfID {
		return NewInvalidInputError("任务不能是自己的父任务")
	}
	var parent model.WorkItem
	if err := tx.First(&parent, parentID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return NewNotFoundError("父任务不存在")
		}
		return err
	}
	if parent.BoardKey != boardKey {
		if isProjectBoard(boardKey) {
			return NewInvalidInputError("父任务必须在同一张任务板里（同一个项目）")
		}
		return NewInvalidInputError("父任务必须在同一个工作流里")
	}
	if parent.ParentID != nil {
		return NewInvalidInputError("只支持一层嵌套：这个父任务自己已经是别的任务的子任务了")
	}
	if selfID != 0 {
		var kids int64
		if err := tx.Model(&model.WorkItem{}).Where("parent_id = ?", selfID).Count(&kids).Error; err != nil {
			return err
		}
		if kids > 0 {
			return NewInvalidInputError("这个任务已经有子任务，不能再成为别的任务的子任务（只支持一层嵌套）")
		}
	}
	return nil
}

// sessionExists 判断会话是否存在（任务只关联真实存在的会话）。
func sessionExists(tx *gorm.DB, sessionID string) bool {
	if sessionID == "" {
		return false
	}
	var n int64
	if err := tx.Model(&model.FeedbackSession{}).Where("id = ?", sessionID).Count(&n).Error; err != nil {
		return false
	}
	return n > 0
}

func linkWorkSession(tx *gorm.DB, itemID uint, sessionID string, now time.Time) (bool, error) {
	if !sessionExists(tx, sessionID) {
		return false, nil
	}
	var n int64
	if err := tx.Model(&model.WorkItemSession{}).Where("work_item_id = ? AND session_id = ?", itemID, sessionID).Count(&n).Error; err != nil {
		return false, err
	}
	if n == 0 {
		if err := tx.Create(&model.WorkItemSession{WorkItemID: itemID, SessionID: sessionID, CreatedAt: now}).Error; err != nil {
			return false, err
		}
	}
	return true, nil
}

// appendWorkUpdate 写一条时间线记录，并把超出上限的旧记录挤掉。
func appendWorkUpdate(tx *gorm.DB, u *model.WorkItemUpdate) error {
	if err := tx.Create(u).Error; err != nil {
		return err
	}
	var ids []uint
	if err := tx.Model(&model.WorkItemUpdate{}).Where("work_item_id = ?", u.WorkItemID).
		Order("id DESC").Offset(model.WorkUpdatesKeep).Pluck("id", &ids).Error; err != nil {
		return err
	}
	if len(ids) > 0 {
		return tx.Where("id IN ?", ids).Delete(&model.WorkItemUpdate{}).Error
	}
	return nil
}

// cancelStateFlip 处理「状态改了又马上改回去」造成的无效记录。
//
// 人在页面上点状态：误点成「进行中」再改回「验证中」，时间线上会留下两条互相抵消的记录；
// 来回点几次就是一长串毫无信息量的流水。这里判定：新的这次状态变化 u 恰好是上一条记录的反向操作
// （上一条 A→B，这一条 B→A），而且两条都不带文字、出自同一个操作者（同类型、同会话），
// 间隔在 model.WorkFlipMergeMinutes 之内 —— 那么净效果为零，把上一条撤掉，这一条也不写（返回 true）。
//
// 刻意不做的事：
//   - 带备注 / 进度文字的记录一律保留（文字本身就是信息）；
//   - 向前推进的链条（todo→doing，十分钟后 doing→review）不合并，每一步都是真实的里程碑；
//   - 超过窗口的反复（昨天确认、今天重开）是真实的重开，照常记录；
//   - 创建记录（没有 FromState）不会被撤。
func cancelStateFlip(tx *gorm.DB, itemID uint, u *model.WorkItemUpdate) (bool, error) {
	if u.Text != "" || u.FromState == "" || u.ToState == "" {
		return false, nil
	}
	var last model.WorkItemUpdate
	err := tx.Where("work_item_id = ?", itemID).Order("id DESC").Limit(1).Take(&last).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if last.Text != "" || last.ActorKind != u.ActorKind || last.SessionID != u.SessionID {
		return false, nil
	}
	if last.FromState != u.ToState || last.ToState != u.FromState {
		return false, nil
	}
	if u.CreatedAt.Sub(last.CreatedAt) > model.WorkFlipMergeMinutes*time.Minute {
		return false, nil
	}
	if err := tx.Delete(&model.WorkItemUpdate{}, last.ID).Error; err != nil {
		return false, err
	}
	return true, nil
}

// 智能体置「已完成」却没写结论时的提示：结论就是「验证过了」的证据，没有它的「已完成」不收。
const doneNeedsConclusionMsg = "置为「已完成」要同时写结论（conclusion，1-3 行：做了什么、怎么验证的，如测试 / e2e / 上线冒烟）；验证还没跑完就先置「验证中」（review）"

// resolveWorkState 按操作者规整目标状态。
//
// 状态绑定智能体自己的开发流程（#110）：验证全部通过后智能体直接置「已完成」，不再被改写成「验证中」；
// 代价是必须带结论（current 是任务现在的状态，conclusion 是这次写入之后任务的结论）。
// 已经是「已完成」的任务再次置「已完成」是空操作，不重复要结论。人（Web 上）不受这条约束，可以置任何状态。
// 智能体也可以重新打开「已完成」的任务：它不再是「用户盖过章的东西」，谁改的由时间线记着。
func resolveWorkState(actor WorkActor, current, requested, conclusion string) (string, error) {
	state := model.NormalizeWorkState(requested)
	if state == "" {
		return "", NewInvalidInputError(fmt.Sprintf("未知的状态「%s」，可用：%s", requested, strings.Join(model.WorkStates, " / ")))
	}
	if actor.isAgent() && state == model.WorkStateDone && current != model.WorkStateDone && strings.TrimSpace(conclusion) == "" {
		return "", NewInvalidInputError(doneNeedsConclusionMsg)
	}
	return state, nil
}

// CreateWorkItem 新建任务。
func (s *Store) CreateWorkItem(ctx context.Context, in WorkItemInput, actor WorkActor) (*WorkItemResult, error) {
	workflowID := strings.TrimSpace(in.WorkflowID)
	if workflowID == "" {
		return nil, NewInvalidInputError("workflow_id 不能为空")
	}
	if runeLen(workflowID) > 128 {
		return nil, NewInvalidInputError("workflow_id 过长")
	}
	title := strings.TrimSpace(in.Title)
	if title == "" {
		return nil, NewInvalidInputError("任务标题不能为空")
	}
	if err := validateWorkText("标题", title, model.WorkTitleMax); err != nil {
		return nil, err
	}
	if strings.ContainsAny(title, "\r\n") {
		return nil, NewInvalidInputError("标题必须是一行")
	}
	kind := model.WorkKindFeature
	if strings.TrimSpace(in.Kind) != "" {
		if kind = model.NormalizeWorkKind(in.Kind); kind == "" {
			return nil, NewInvalidInputError(fmt.Sprintf("未知的类型「%s」，可用：%s", in.Kind, strings.Join(model.WorkKinds, " / ")))
		}
	}
	stage, err := normalizeStageInput(in.Stage)
	if err != nil {
		return nil, err
	}
	labels, err := NormalizeWorkLabels(in.Labels)
	if err != nil {
		return nil, err
	}
	refs, err := NormalizeWorkRefs(in.Refs)
	if err != nil {
		return nil, err
	}
	description := strings.TrimSpace(in.Description)
	if err := validateWorkText("描述", description, model.WorkDescriptionMax); err != nil {
		return nil, err
	}
	progress := strings.TrimSpace(in.Progress)
	if err := validateWorkText("进度", progress, model.WorkProgressMax); err != nil {
		return nil, err
	}
	note := strings.TrimSpace(in.Note)
	if err := validateWorkText("备注", note, model.WorkNoteMax); err != nil {
		return nil, err
	}
	var percent *int
	if in.Percent != nil {
		if err := validateWorkPercent(*in.Percent); err != nil {
			return nil, err
		}
		if *in.Percent >= 0 { // -1 = 不写显式完成度
			p := *in.Percent
			percent = &p
		}
	}
	assignee := strings.TrimSpace(in.Assignee)
	state := model.WorkStateTodo
	if strings.TrimSpace(in.State) != "" {
		// 新建时还没有结论可写，所以智能体不能直接建出「已完成」的任务：先建成待办 / 进行中，验证通过后再置「已完成」。
		if actor.isAgent() && model.NormalizeWorkState(in.State) == model.WorkStateDone {
			return nil, NewInvalidInputError("新建任务不能直接是「已完成」：先建成待办 / 进行中，验证通过后再更新状态并写结论（做了什么、怎么验证的）")
		}
		if state, err = resolveWorkState(actor, "", in.State, ""); err != nil {
			return nil, err
		}
	}

	res := &WorkItemResult{Changed: true}
	var id uint
	err = s.WithTx(ctx, func(tx *gorm.DB) error {
		// 板在事务里解析（只能用 tx：单连接下事务里回头用 s.db 会等自己持有的连接）。
		boardKey, err := boardKeyOn(ctx, tx, workflowID)
		if err != nil {
			return err
		}
		if err := checkWorkParent(tx, boardKey, 0, in.ParentID); err != nil {
			return err
		}
		if err := checkWorkModule(tx, boardKey, in.ModuleID); err != nil {
			return err
		}
		seq, err := nextBoardSeq(tx, boardKey, workflowID)
		if err != nil {
			return err
		}
		now := time.Now()
		item := &model.WorkItem{
			WorkflowID:      workflowID,
			BoardKey:        boardKey,
			Seq:             seq,
			Title:           title,
			Kind:            kind,
			Stage:           stage,
			Labels:          labels,
			State:           state,
			Description:     description,
			Progress:        progress,
			Refs:            refs,
			SourceSessionID: actor.SessionID,
			LastSessionID:   actor.SessionID,
			CreatedBy:       actor.kind(),
			CreatedAt:       now,
			UpdatedAt:       now,
			Percent:         percent,
		}
		if in.ParentID != 0 {
			p := in.ParentID
			item.ParentID = &p
		}
		if in.ModuleID != 0 {
			m := in.ModuleID
			item.ModuleID = &m
		}
		if model.IsClosedWorkState(state) {
			item.ClosedAt = &now
		}
		// 创建时同时指派：谁能指派、承接者是不是板上的工作流，规则与 UpdateWorkItem 一致（work_assign.go）。
		var assignEv *workEvent
		if assignee != "" {
			probe := &model.WorkItem{WorkflowID: workflowID, BoardKey: boardKey}
			if err := actor.guard(probe, workOpAssign, res); err != nil {
				return err
			}
			if err := checkAssignee(ctx, tx, boardKey, assignee); err != nil {
				return err
			}
			text := "创建并指派给 " + assignee
			if assignFields(item, assignee, actor, now) {
				text += "（自己承接）"
			}
			assignEv = &workEvent{model.WorkEventAssign, text}
		}
		if err := tx.Create(item).Error; err != nil {
			return err
		}
		id = item.ID
		text := note
		if text == "" {
			text = progress
		}
		first := &model.WorkItemUpdate{
			WorkItemID: item.ID, ActorKind: actor.kind(), SessionID: actor.SessionID, ToState: state, Text: text, CreatedAt: now,
			ActorWorkflowID: actor.workflowRef(),
		}
		if assignEv != nil {
			first.Event, first.Text = assignEv.name, eventText(assignEv, text)
		}
		if err := appendWorkUpdate(tx, first); err != nil {
			return err
		}
		if actor.SessionID != "" {
			linked, err := linkWorkSession(tx, item.ID, actor.SessionID, now)
			if err != nil {
				return err
			}
			res.SessionLinked = linked
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	res.Item, err = s.GetWorkItem(ctx, id)
	if err != nil {
		return nil, err
	}
	return res, nil
}

// UpdateWorkItem 更新任务。没有任何变化时不写库，Changed 为 false。
func (s *Store) UpdateWorkItem(ctx context.Context, id uint, patch WorkItemPatch, actor WorkActor) (*WorkItemResult, error) {
	note := strings.TrimSpace(patch.Note)
	if err := validateWorkText("备注", note, model.WorkNoteMax); err != nil {
		return nil, err
	}
	if patch.Percent != nil {
		if err := validateWorkPercent(*patch.Percent); err != nil {
			return nil, err
		}
	}
	res := &WorkItemResult{}
	err := s.WithTx(ctx, func(tx *gorm.DB) error {
		var item model.WorkItem
		if err := tx.First(&item, id).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return NewNotFoundError("任务不存在")
			}
			return err
		}
		before := item // 改动前的任务：任务级权限相对「改动前」判定（work_assign.go）
		prevState := item.State
		prevStage := item.Stage
		changed := false
		moduleChanged := false // 只调整归属不算「进展」：不动 updated_at，也不占时间线
		descChanged := false   // 只补充 / 修订描述同理：描述是「这件事是什么」，不是「做到哪」
		progressChanged := false

		if patch.Title != nil {
			t := strings.TrimSpace(*patch.Title)
			if t == "" {
				return NewInvalidInputError("任务标题不能为空")
			}
			if err := validateWorkText("标题", t, model.WorkTitleMax); err != nil {
				return err
			}
			if strings.ContainsAny(t, "\r\n") {
				return NewInvalidInputError("标题必须是一行")
			}
			if t != item.Title {
				item.Title, changed = t, true
			}
		}
		if patch.Kind != nil {
			k := model.NormalizeWorkKind(*patch.Kind)
			if k == "" {
				return NewInvalidInputError(fmt.Sprintf("未知的类型「%s」，可用：%s", *patch.Kind, strings.Join(model.WorkKinds, " / ")))
			}
			if k != item.Kind {
				item.Kind, changed = k, true
			}
		}
		if patch.Stage != nil {
			st, err := normalizeStageInput(*patch.Stage)
			if err != nil {
				return err
			}
			if st != item.Stage {
				item.Stage, changed = st, true
			}
		}
		if patch.Labels != nil {
			l, err := NormalizeWorkLabels(*patch.Labels)
			if err != nil {
				return err
			}
			if !sameStrings(l, item.Labels) {
				item.Labels, changed = l, true
			}
		}
		if patch.Refs != nil {
			r, err := NormalizeWorkRefs(*patch.Refs)
			if err != nil {
				return err
			}
			if !sameStrings(r, item.Refs) {
				item.Refs, changed = r, true
			}
		}
		if patch.Description != nil {
			d := strings.TrimSpace(*patch.Description)
			if err := validateWorkText("描述", d, model.WorkDescriptionMax); err != nil {
				return err
			}
			if d != item.Description {
				item.Description, descChanged = d, true
			}
		}
		if patch.Progress != nil {
			p := strings.TrimSpace(*patch.Progress)
			if err := validateWorkText("进度", p, model.WorkProgressMax); err != nil {
				return err
			}
			if p != item.Progress {
				item.Progress, changed, progressChanged = p, true, true
			}
		}
		if patch.Conclusion != nil {
			c := strings.TrimSpace(*patch.Conclusion)
			if err := validateWorkText("结论", c, model.WorkConclusionMax); err != nil {
				return err
			}
			if c != item.Conclusion {
				item.Conclusion, changed = c, true
			}
		}
		var itemBoard string // 任务所在的板；需要校验父任务 / 模块时才解析
		boardOfItem := func() (string, error) {
			if itemBoard == "" {
				if itemBoard = item.BoardKey; itemBoard == "" {
					k, err := boardKeyOn(ctx, tx, item.WorkflowID)
					if err != nil {
						return "", err
					}
					itemBoard = k
				}
			}
			return itemBoard, nil
		}
		if patch.ParentID != nil {
			bk, err := boardOfItem()
			if err != nil {
				return err
			}
			if err := checkWorkParent(tx, bk, item.ID, *patch.ParentID); err != nil {
				return err
			}
			switch {
			case *patch.ParentID == 0 && item.ParentID != nil:
				item.ParentID, changed = nil, true
			case *patch.ParentID != 0 && (item.ParentID == nil || *item.ParentID != *patch.ParentID):
				p := *patch.ParentID
				item.ParentID, changed = &p, true
			}
		}
		if patch.ModuleID != nil {
			bk, err := boardOfItem()
			if err != nil {
				return err
			}
			if err := checkWorkModule(tx, bk, *patch.ModuleID); err != nil {
				return err
			}
			switch {
			case *patch.ModuleID == 0 && item.ModuleID != nil:
				item.ModuleID, moduleChanged = nil, true
			case *patch.ModuleID != 0 && (item.ModuleID == nil || *item.ModuleID != *patch.ModuleID):
				m := *patch.ModuleID
				item.ModuleID, moduleChanged = &m, true
			}
		}
		if patch.Percent != nil {
			switch {
			case *patch.Percent < 0 && item.Percent != nil:
				item.Percent, changed = nil, true
			case *patch.Percent >= 0 && (item.Percent == nil || *item.Percent != *patch.Percent):
				p := *patch.Percent
				item.Percent, changed = &p, true
			}
		}

		now := time.Now()
		// 指派 / 接取 / 拒绝 / 交付 / 验收：先于状态处理，因为显式的 claim 会换算成状态变化（写进 patch.State）。
		var ev *workEvent
		if patch.Assignee != nil || strings.TrimSpace(patch.Claim) != "" {
			e, err := applyWorkAssignment(workAssignCtx{ctx: ctx, tx: tx, item: &item, patch: &patch, actor: actor, note: note, now: now, board: boardOfItem})
			if err != nil {
				return err
			}
			if ev = e; ev != nil {
				changed = true
			}
		}
		if patch.State != nil {
			// 这里 item.Conclusion 已经带上了本次 patch 里的结论：置「已完成」可以和写结论放在同一次调用里。
			next, err := resolveWorkState(actor, prevState, *patch.State, item.Conclusion)
			if err != nil {
				return err
			}
			if next != prevState {
				item.State, changed = next, true
				if model.IsClosedWorkState(next) {
					if !model.IsClosedWorkState(prevState) {
						item.ClosedAt = &now
					}
				} else {
					item.ClosedAt = nil
				}
			}
		}

		stateChanged := item.State != prevState
		stageChanged := item.Stage != prevStage
		// 没有显式 claim 时，从状态变化里识别隐式的接取 / 交付 / 验收 / 退回；然后按策略逐类判定这次写入涉及的操作。
		// 判定在任何写库之前，enforce 下拒绝会让整个事务回滚。
		if ev == nil {
			_, progress := workFieldsDiff(&before, &item)
			e, accept := implicitWorkEvent(&before, item.State, workRolesOf(actor, &before), progress || note != "")
			if ev = e; ev != nil && accept && item.ClaimState == model.WorkClaimOffered {
				item.ClaimState = model.WorkClaimAccepted
				at := now
				item.AcceptedAt = &at
				changed = true
			}
		}
		if err := actor.guardAll(&before, workNeeds(&before, &item, note, ev), res); err != nil {
			return err
		}
		if !changed && note == "" {
			// 没有字段变化也没有备注：只补会话关联（如果传了），不动 updated_at。
			// 仅调整了归属 / 描述时同样不动 updated_at：整理分类、补一段说明都不是进展，
			// 不应把旧任务顶到「最近进展」前面。
			quiet := map[string]any{}
			if moduleChanged {
				quiet["module_id"] = item.ModuleID
			}
			if descChanged {
				quiet["description"] = item.Description
			}
			if len(quiet) > 0 {
				if err := tx.Model(&model.WorkItem{}).Where("id = ?", item.ID).UpdateColumns(quiet).Error; err != nil {
					return err
				}
			}
			if actor.SessionID != "" {
				linked, err := linkWorkSession(tx, item.ID, actor.SessionID, now)
				if err != nil {
					return err
				}
				res.SessionLinked = linked
			}
			res.Changed = moduleChanged || descChanged
			return nil
		}

		res.Changed = true
		item.UpdatedAt = now
		if actor.SessionID != "" {
			item.LastSessionID = actor.SessionID
		}
		if err := tx.Save(&item).Error; err != nil {
			return err
		}
		// 时间线：状态变化、阶段变化、进度变化或备注之一发生才记一条；纯改标题 / 标签 / 引用不占时间线。
		if stateChanged || stageChanged || progressChanged || note != "" || ev != nil {
			text := note
			if text == "" && progressChanged {
				text = item.Progress
			}
			if text == "" && stageChanged && !stateChanged {
				text = fmt.Sprintf("阶段：%s → %s", model.WorkStageLabel(prevStage), model.WorkStageLabel(item.Stage))
			}
			u := &model.WorkItemUpdate{WorkItemID: item.ID, ActorKind: actor.kind(), SessionID: actor.SessionID, Text: text, CreatedAt: now, ActorWorkflowID: actor.workflowRef()}
			if ev != nil { // 协作事件：正文是一句可读的话，再接备注 / 进度；带文字的记录不会被「状态来回」合并掉
				u.Event, u.Text = ev.name, eventText(ev, text)
			}
			if stateChanged {
				u.FromState, u.ToState = prevState, item.State
			}
			// 状态误点后马上改回来：这一来一回互相抵消，上一条记录一并撤掉，时间线上不留痕迹。
			merged := false
			if stateChanged && !stageChanged {
				m, err := cancelStateFlip(tx, item.ID, u)
				if err != nil {
					return err
				}
				merged = m
			}
			if !merged {
				if err := appendWorkUpdate(tx, u); err != nil {
					return err
				}
			}
		}
		if actor.SessionID != "" {
			linked, err := linkWorkSession(tx, item.ID, actor.SessionID, now)
			if err != nil {
				return err
			}
			res.SessionLinked = linked
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	res.Item, err = s.GetWorkItem(ctx, id)
	if err != nil {
		return nil, err
	}
	return res, nil
}

// GetWorkItem 读取任务详情：含最近的时间线（新的在前）与关联的会话。
func (s *Store) GetWorkItem(ctx context.Context, id uint) (*model.WorkItem, error) {
	var item model.WorkItem
	if err := s.db.WithContext(ctx).First(&item, id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, NewNotFoundError("任务不存在")
		}
		return nil, err
	}
	return s.hydrateWorkItem(ctx, &item)
}

// GetWorkItemBySeq 按序号读取任务（智能体使用）：序号在工作流所在的任务板内唯一（#175）。
// 同一个项目的工作流共用一套 #N；单工作流的板与按工作流取完全一致。
// 找不到时依次回落：显式合并时改过号的旧 (工作流, 序号) 别名，以及旧版本回滚期间写入、还没归板的行。
func (s *Store) GetWorkItemBySeq(ctx context.Context, workflowID string, seq int) (*model.WorkItem, error) {
	boardKey, err := boardKeyOn(ctx, s.db, workflowID)
	if err != nil {
		return nil, err
	}
	db := s.db.WithContext(ctx)
	var item model.WorkItem
	err = db.Where("board_key = ? AND seq = ?", boardKey, seq).First(&item).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		var alias model.WorkItemAlias
		if e := db.Where("old_workflow_id = ? AND old_seq = ?", workflowID, seq).Take(&alias).Error; e == nil {
			err = db.First(&item, alias.WorkItemID).Error
		}
	}
	if errors.Is(err, gorm.ErrRecordNotFound) {
		err = db.Where("board_key = '' AND workflow_id = ? AND seq = ?", workflowID, seq).First(&item).Error
	}
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			if isProjectBoard(boardKey) {
				return nil, NewNotFoundError(fmt.Sprintf("工作流 %s 所在的项目任务板里没有 #%d 号任务", workflowID, seq))
			}
			return nil, NewNotFoundError(fmt.Sprintf("工作流 %s 里没有 #%d 号任务", workflowID, seq))
		}
		return nil, err
	}
	return s.hydrateWorkItem(ctx, &item)
}

func (s *Store) hydrateWorkItem(ctx context.Context, item *model.WorkItem) (*model.WorkItem, error) {
	db := s.db.WithContext(ctx)
	if err := db.Where("work_item_id = ?", item.ID).Order("id DESC").Limit(model.WorkUpdatesKeep).Find(&item.Updates).Error; err != nil {
		return nil, err
	}
	if err := db.Where("work_item_id = ?", item.ID).Order("created_at ASC").Find(&item.Sessions).Error; err != nil {
		return nil, err
	}
	item.SessionNum = len(item.Sessions)
	if len(item.Sessions) > 0 {
		ids := make([]string, 0, len(item.Sessions))
		for _, ls := range item.Sessions {
			ids = append(ids, ls.SessionID)
		}
		var sess []model.FeedbackSession
		if err := db.Select("id", "title", "status", "summary").Where("id IN ?", ids).Find(&sess).Error; err != nil {
			return nil, err
		}
		byID := make(map[string]model.FeedbackSession, len(sess))
		for _, fs := range sess {
			byID[fs.ID] = fs
		}
		for i := range item.Sessions {
			if fs, ok := byID[item.Sessions[i].SessionID]; ok {
				item.Sessions[i].Title = fs.Title
				if item.Sessions[i].Title == "" {
					item.Sessions[i].Title = truncateRunes(strings.TrimSpace(fs.Summary), 40)
				}
				item.Sessions[i].Status = fs.Status
			}
		}
	}
	var kids []model.WorkItem
	if err := db.Select("id", "state").Where("parent_id = ?", item.ID).Find(&kids).Error; err != nil {
		return nil, err
	}
	item.ChildTotal = len(kids)
	for _, k := range kids {
		if k.State == model.WorkStateDone {
			item.ChildDone++
		}
	}
	if item.ParentID != nil {
		var parent model.WorkItem
		if err := db.Select("id", "seq", "title").First(&parent, *item.ParentID).Error; err == nil {
			item.ParentSeq, item.ParentTitle = parent.Seq, parent.Title
		}
	}
	if item.ModuleID != nil {
		boardKey := item.BoardKey
		if boardKey == "" { // 旧版本回滚期间写入、还没归板的行
			var err error
			if boardKey, err = boardKeyOn(ctx, s.db, item.WorkflowID); err != nil {
				return nil, err
			}
		}
		paths, err := s.workModulePaths(ctx, []string{boardKey})
		if err != nil {
			return nil, err
		}
		item.ModulePath = paths[*item.ModuleID]
	}
	item.ProgressPct, item.ProgressFrom = model.EffectivePercent(item.State, item.Percent, item.ChildTotal, item.ChildDone)
	return item, nil
}

func escapeLike(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return r.Replace(s)
}

// ListWorkItems 列出任务（不含时间线），最近更新的在前；同时补齐子任务进度、父任务和关联会话数。
func (s *Store) ListWorkItems(ctx context.Context, f WorkItemFilter) ([]model.WorkItem, error) {
	db := s.db.WithContext(ctx).Model(&model.WorkItem{})
	if f.WorkflowID != "" {
		// 按工作流所在的任务板列：同一个项目的工作流看到同一份清单（#175）。
		boardKey, err := boardKeyOn(ctx, s.db, f.WorkflowID)
		if err != nil {
			return nil, err
		}
		db = db.Where("board_key = ?", boardKey)
	}
	if len(f.States) > 0 {
		states := make([]string, 0, len(f.States))
		for _, st := range f.States {
			if n := model.NormalizeWorkState(st); n != "" {
				states = append(states, n)
			}
		}
		if len(states) == 0 {
			return []model.WorkItem{}, nil
		}
		db = db.Where("state IN ?", states)
	}
	if len(f.Kinds) > 0 {
		kinds := make([]string, 0, len(f.Kinds))
		for _, k := range f.Kinds {
			if n := model.NormalizeWorkKind(k); n != "" {
				kinds = append(kinds, n)
			}
		}
		if len(kinds) == 0 {
			return []model.WorkItem{}, nil
		}
		db = db.Where("kind IN ?", kinds)
	}
	if len(f.Stages) > 0 {
		stages := make([]string, 0, len(f.Stages))
		for _, st := range f.Stages {
			t := strings.ToLower(strings.TrimSpace(st))
			if t == "" || t == "none" || t == "未标注" {
				stages = append(stages, "")
			} else if n := model.NormalizeWorkStage(t); n != "" {
				stages = append(stages, n)
			}
		}
		if len(stages) == 0 {
			return []model.WorkItem{}, nil
		}
		db = db.Where("stage IN ?", stages)
	}
	if sid := strings.TrimSpace(f.SessionID); sid != "" {
		db = db.Where("id IN (?)", s.db.WithContext(ctx).Model(&model.WorkItemSession{}).Select("work_item_id").Where("session_id = ?", sid))
	}
	if f.ParentID != nil {
		db = db.Where("parent_id = ?", *f.ParentID)
	} else if f.TopLevel {
		db = db.Where("parent_id IS NULL")
	}
	if f.Unassigned {
		db = db.Where("module_id IS NULL")
	} else if f.ModuleID != 0 {
		ids, err := s.WorkModuleScope(ctx, f.ModuleID)
		if err != nil {
			return nil, err
		}
		db = db.Where("module_id IN ?", ids)
	}
	if q := strings.TrimSpace(f.Query); q != "" {
		like := "%" + escapeLike(strings.ToLower(q)) + "%"
		db = db.Where(`(LOWER(title) LIKE ? ESCAPE '\' OR LOWER(labels) LIKE ? ESCAPE '\' OR LOWER(description) LIKE ? ESCAPE '\' OR LOWER(progress) LIKE ? ESCAPE '\' OR LOWER(conclusion) LIKE ? ESCAPE '\')`, like, like, like, like, like)
	}
	if as := strings.TrimSpace(f.Assignee); as != "" {
		if strings.EqualFold(as, "none") {
			db = db.Where("assignee_workflow_id = ''")
		} else {
			db = db.Where("assignee_workflow_id = ?", as)
		}
	}
	if by := strings.TrimSpace(f.AssignedBy); by != "" {
		db = db.Where("assignee_workflow_id <> '' AND assigned_by_workflow_id = ?", by)
	}
	if f.ClosedDays > 0 {
		cutoff := time.Now().AddDate(0, 0, -f.ClosedDays)
		db = db.Where("(state NOT IN ('done','shelved') OR closed_at IS NULL OR closed_at >= ?)", cutoff)
	}
	limit := WorkItemListLimit(f.Limit)
	var items []model.WorkItem
	if err := db.Order("updated_at DESC, id DESC").Limit(limit).Find(&items).Error; err != nil {
		return nil, err
	}
	if len(items) == 0 {
		return []model.WorkItem{}, nil
	}
	if err := s.annotateWorkItems(ctx, items); err != nil {
		return nil, err
	}
	if sid := strings.TrimSpace(f.SessionID); sid != "" {
		if err := s.annotateSessionCtx(ctx, items, sid); err != nil {
			return nil, err
		}
	}
	return items, nil
}

// normalizeStageInput 规整调用方传来的阶段：空串 = 未标注（合法）；无法识别时报错并列出可用值。
func normalizeStageInput(raw string) (string, error) {
	t := strings.TrimSpace(raw)
	if t == "" {
		return "", nil
	}
	if n := model.NormalizeWorkStage(t); n != "" {
		return n, nil
	}
	return "", NewInvalidInputError(fmt.Sprintf("未知的阶段「%s」，可用：%s（留空表示未标注）", raw, strings.Join(model.WorkStages, " / ")))
}

// annotateSessionCtx 补上「这个会话对每条任务做了什么」：是否它创建的、时间线上写了几条、状态从哪推到哪。
// 只依赖任务时间线里还保留的条目（每个任务最近 WorkUpdatesKeep 条）。
func (s *Store) annotateSessionCtx(ctx context.Context, items []model.WorkItem, sessionID string) error {
	ids := make([]uint, 0, len(items))
	for _, it := range items {
		ids = append(ids, it.ID)
	}
	var ups []model.WorkItemUpdate
	if err := s.db.WithContext(ctx).Where("work_item_id IN ? AND session_id = ?", ids, sessionID).Order("id ASC").Find(&ups).Error; err != nil {
		return err
	}
	byItem := make(map[uint]*model.WorkItemSessionCtx, len(items))
	for _, u := range ups {
		c := byItem[u.WorkItemID]
		if c == nil {
			c = &model.WorkItemSessionCtx{}
			byItem[u.WorkItemID] = c
		}
		c.Updates++
		at := u.CreatedAt
		c.LastAt = &at
		if u.ToState != "" && u.FromState != "" { // 创建那一条只有 ToState，不算「改变状态」
			if c.FromState == "" {
				c.FromState = u.FromState
			}
			c.ToState = u.ToState
		}
	}
	for i := range items {
		c := byItem[items[i].ID]
		if c == nil {
			c = &model.WorkItemSessionCtx{}
		}
		c.Created = items[i].SourceSessionID == sessionID
		items[i].SessionCtx = c
	}
	return nil
}

// ListWorkflowSessionLinks 返回一个工作流所在任务板上所有「会话 ↔ 任务」的关联，以及每个会话对每个任务做了什么。
// 会话页按轮次展开任务时用：一次查询取回整张板，不必为每一轮各查一次。
// 「做了什么」只依赖任务时间线里还保留的条目（每个任务最近 WorkUpdatesKeep 条）。
func (s *Store) ListWorkflowSessionLinks(ctx context.Context, workflowID string) ([]model.WorkItemSessionLink, error) {
	workflowID = strings.TrimSpace(workflowID)
	if workflowID == "" {
		return []model.WorkItemSessionLink{}, nil
	}
	boardKey, err := boardKeyOn(ctx, s.db, workflowID)
	if err != nil {
		return nil, err
	}
	db := s.db.WithContext(ctx)
	type linkRow struct {
		SessionID       string
		WorkItemID      uint
		SourceSessionID string
	}
	var rows []linkRow
	if err := db.Model(&model.WorkItemSession{}).
		Select("work_item_sessions.session_id AS session_id, work_item_sessions.work_item_id AS work_item_id, work_items.source_session_id AS source_session_id").
		Joins("JOIN work_items ON work_items.id = work_item_sessions.work_item_id").
		Where("work_items.board_key = ?", boardKey).
		Order("work_item_sessions.session_id ASC, work_item_sessions.work_item_id ASC").
		Scan(&rows).Error; err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return []model.WorkItemSessionLink{}, nil
	}

	var ups []model.WorkItemUpdate
	if err := db.Where("session_id <> '' AND work_item_id IN (?)",
		db.Model(&model.WorkItem{}).Select("id").Where("board_key = ?", boardKey)).
		Order("id ASC").Find(&ups).Error; err != nil {
		return nil, err
	}
	type key struct {
		session string
		item    uint
	}
	ctxOf := make(map[key]*model.WorkItemSessionCtx, len(rows))
	for _, u := range ups {
		k := key{u.SessionID, u.WorkItemID}
		c := ctxOf[k]
		if c == nil {
			c = &model.WorkItemSessionCtx{}
			ctxOf[k] = c
		}
		c.Updates++
		at := u.CreatedAt
		c.LastAt = &at
		if u.ToState != "" && u.FromState != "" { // 创建那一条只有 ToState，不算「改变状态」
			if c.FromState == "" {
				c.FromState = u.FromState
			}
			c.ToState = u.ToState
		}
	}
	out := make([]model.WorkItemSessionLink, 0, len(rows))
	for _, r := range rows {
		link := model.WorkItemSessionLink{SessionID: r.SessionID, WorkItemID: r.WorkItemID}
		if c := ctxOf[key{r.SessionID, r.WorkItemID}]; c != nil {
			link.WorkItemSessionCtx = *c
		}
		link.Created = r.SourceSessionID == r.SessionID
		out = append(out, link)
	}
	return out, nil
}

// annotateWorkItems 补齐列表所需的派生字段（子任务进度、父任务、关联会话数），一次查询一类，避免逐条查库。
func (s *Store) annotateWorkItems(ctx context.Context, items []model.WorkItem) error {
	db := s.db.WithContext(ctx)
	ids := make([]uint, 0, len(items))
	parentIDs := map[uint]bool{}
	for _, it := range items {
		ids = append(ids, it.ID)
		if it.ParentID != nil {
			parentIDs[*it.ParentID] = true
		}
	}
	type kidRow struct {
		ParentID uint
		State    string
		N        int
	}
	var kids []kidRow
	if err := db.Model(&model.WorkItem{}).Select("parent_id, state, COUNT(*) AS n").
		Where("parent_id IN ?", ids).Group("parent_id, state").Scan(&kids).Error; err != nil {
		return err
	}
	total, done := map[uint]int{}, map[uint]int{}
	for _, k := range kids {
		total[k.ParentID] += k.N
		if k.State == model.WorkStateDone {
			done[k.ParentID] += k.N
		}
	}
	type sessRow struct {
		WorkItemID uint
		N          int
	}
	var sessions []sessRow
	if err := db.Model(&model.WorkItemSession{}).Select("work_item_id, COUNT(*) AS n").
		Where("work_item_id IN ?", ids).Group("work_item_id").Scan(&sessions).Error; err != nil {
		return err
	}
	sessN := map[uint]int{}
	for _, r := range sessions {
		sessN[r.WorkItemID] = r.N
	}
	parents := map[uint]model.WorkItem{}
	if len(parentIDs) > 0 {
		pids := make([]uint, 0, len(parentIDs))
		for id := range parentIDs {
			pids = append(pids, id)
		}
		var ps []model.WorkItem
		if err := db.Select("id", "seq", "title").Where("id IN ?", pids).Find(&ps).Error; err != nil {
			return err
		}
		for _, p := range ps {
			parents[p.ID] = p
		}
	}
	// 模块路径：只查有任务归了类的任务板。
	boardSet := map[string]bool{}
	for _, it := range items {
		if it.ModuleID != nil && it.BoardKey != "" {
			boardSet[it.BoardKey] = true
		}
	}
	var paths map[uint]string
	if len(boardSet) > 0 {
		boards := make([]string, 0, len(boardSet))
		for b := range boardSet {
			boards = append(boards, b)
		}
		var err error
		if paths, err = s.workModulePaths(ctx, boards); err != nil {
			return err
		}
	}
	for i := range items {
		it := &items[i]
		it.ChildTotal, it.ChildDone, it.SessionNum = total[it.ID], done[it.ID], sessN[it.ID]
		if it.ParentID != nil {
			if p, ok := parents[*it.ParentID]; ok {
				it.ParentSeq, it.ParentTitle = p.Seq, p.Title
			}
		}
		if it.ModuleID != nil {
			it.ModulePath = paths[*it.ModuleID]
		}
		it.ProgressPct, it.ProgressFrom = model.EffectivePercent(it.State, it.Percent, it.ChildTotal, it.ChildDone)
	}
	return nil
}

// CountWorkItemsByState 统计各状态的任务数（workflowID 为空 = 全部工作流）。
// 按「创建者工作流」数：这是某个智能体自己的任务量（会话守护、工作流模式用）；
// 要与任务清单对得上（整张板）用 CountBoardWorkItemsByState。
func (s *Store) CountWorkItemsByState(ctx context.Context, workflowID string) (map[string]int, error) {
	db := s.db.WithContext(ctx).Model(&model.WorkItem{})
	if workflowID != "" {
		db = db.Where("workflow_id = ?", workflowID)
	}
	return countItemsByState(db)
}

// CountBoardWorkItemsByState 统计工作流所在任务板的各状态任务数，与按这个工作流列出的任务清单口径一致。
func (s *Store) CountBoardWorkItemsByState(ctx context.Context, workflowID string) (map[string]int, error) {
	boardKey, err := boardKeyOn(ctx, s.db, workflowID)
	if err != nil {
		return nil, err
	}
	return s.countItemsByStateOnBoard(ctx, boardKey)
}

func (s *Store) countItemsByStateOnBoard(ctx context.Context, boardKey string) (map[string]int, error) {
	return countItemsByState(s.db.WithContext(ctx).Model(&model.WorkItem{}).Where("board_key = ?", boardKey))
}

func countItemsByState(db *gorm.DB) (map[string]int, error) {
	type row struct {
		State string
		N     int
	}
	var rows []row
	if err := db.Select("state, COUNT(*) AS n").Group("state").Scan(&rows).Error; err != nil {
		return nil, err
	}
	out := make(map[string]int, len(model.WorkStates))
	for _, st := range model.WorkStates {
		out[st] = 0
	}
	for _, r := range rows {
		out[r.State] = r.N
	}
	return out, nil
}

// ListWorkItemWorkflows 列出有任务的工作流及其概况，最近更新的在前。
// 同一张任务板上的工作流各占一条，数字都是整张板的（见 WorkflowStat）。
func (s *Store) ListWorkItemWorkflows(ctx context.Context) ([]WorkflowStat, error) {
	// 只取几列在 Go 里汇总：时间列由 GORM 按类型解析，避免依赖 SQLite 聚合函数返回的时间文本格式。
	var rows []model.WorkItem
	if err := s.db.WithContext(ctx).Select("workflow_id", "board_key", "state", "updated_at").Find(&rows).Error; err != nil {
		return nil, err
	}
	type boardAgg struct {
		stat    WorkflowStat
		members map[string]time.Time // 工作流 → 它自己最近一次更新
	}
	boards := map[string]*boardAgg{}
	order := make([]string, 0)
	for _, r := range rows {
		key := r.BoardKey
		if key == "" { // 旧版本回滚期间写入、还没归板的行：按工作流各自成板
			key = WorkflowBoardKey(r.WorkflowID)
		}
		b, ok := boards[key]
		if !ok {
			b = &boardAgg{stat: WorkflowStat{BoardKey: key}, members: map[string]time.Time{}}
			boards[key] = b
			order = append(order, key)
		}
		st := &b.stat
		st.Total++
		if !model.IsClosedWorkState(r.State) {
			st.Active++
		}
		switch r.State {
		case model.WorkStateDoing:
			st.Doing++
		case model.WorkStateReview:
			st.Review++
		}
		if r.UpdatedAt.After(st.UpdatedAt) {
			st.UpdatedAt = r.UpdatedAt
		}
		if t, seen := b.members[r.WorkflowID]; !seen || r.UpdatedAt.After(t) {
			b.members[r.WorkflowID] = r.UpdatedAt
		}
	}
	out := make([]WorkflowStat, 0, len(order))
	for _, key := range order {
		b := boards[key]
		ids := make([]string, 0, len(b.members))
		for wf := range b.members {
			ids = append(ids, wf)
		}
		sort.Slice(ids, func(i, j int) bool {
			ti, tj := b.members[ids[i]], b.members[ids[j]]
			if !ti.Equal(tj) {
				return ti.After(tj)
			}
			return ids[i] < ids[j]
		})
		for _, wf := range ids {
			st := b.stat
			st.WorkflowID = wf
			if len(ids) > 1 {
				st.Members = ids
			}
			out = append(out, st)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if !out[i].UpdatedAt.Equal(out[j].UpdatedAt) {
			return out[i].UpdatedAt.After(out[j].UpdatedAt)
		}
		return out[i].WorkflowID < out[j].WorkflowID
	})
	names, err := s.sharedBoardNames(ctx, out)
	if err != nil {
		return nil, err
	}
	for i := range out {
		out[i].BoardName = names[out[i].BoardKey]
	}
	return out, nil
}

// sharedBoardNames 返回多个工作流共用的项目板对应的项目名（板 key → 名称）。
// 没有共用板时不查库（生产现状），所以平时多一次也没有。归档的项目名字照样返回：板是粘性的，不随项目归档而消失。
func (s *Store) sharedBoardNames(ctx context.Context, stats []WorkflowStat) (map[string]string, error) {
	keyOf := map[uint]string{} // 项目编号 → 板 key
	for _, st := range stats {
		if len(st.Members) < 2 {
			continue
		}
		if pid, _, ok := ParseBoardKey(st.BoardKey); ok && pid != 0 {
			keyOf[pid] = st.BoardKey
		}
	}
	if len(keyOf) == 0 {
		return nil, nil
	}
	ids := make([]uint, 0, len(keyOf))
	for id := range keyOf {
		ids = append(ids, id)
	}
	var projects []model.Project
	if err := s.db.WithContext(ctx).Select("id", "name").Where("id IN ?", ids).Find(&projects).Error; err != nil {
		return nil, err
	}
	out := make(map[string]string, len(projects))
	for _, p := range projects {
		out[keyOf[p.ID]] = p.Name
	}
	return out, nil
}

// DeleteWorkItem 删除任务及其时间线与会话关联；它的子任务变成顶层任务。
func (s *Store) DeleteWorkItem(ctx context.Context, id uint) (*model.WorkItem, error) {
	var deleted model.WorkItem
	err := s.WithTx(ctx, func(tx *gorm.DB) error {
		if err := tx.First(&deleted, id).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return NewNotFoundError("任务不存在")
			}
			return err
		}
		if err := tx.Model(&model.WorkItem{}).Where("parent_id = ?", id).Update("parent_id", nil).Error; err != nil {
			return err
		}
		if err := tx.Where("work_item_id = ?", id).Delete(&model.WorkItemUpdate{}).Error; err != nil {
			return err
		}
		if err := tx.Where("work_item_id = ?", id).Delete(&model.WorkItemSession{}).Error; err != nil {
			return err
		}
		if err := tx.Where("work_item_id = ?", id).Delete(&model.WorkItemAlias{}).Error; err != nil {
			return err
		}
		return tx.Delete(&model.WorkItem{}, id).Error
	})
	if err != nil {
		return nil, err
	}
	return &deleted, nil
}

// LinkWorkItemSession 把一个已存在的会话关联到任务。
func (s *Store) LinkWorkItemSession(ctx context.Context, id uint, sessionID string) error {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return NewInvalidInputError("session_id 不能为空")
	}
	return s.WithTx(ctx, func(tx *gorm.DB) error {
		var n int64
		if err := tx.Model(&model.WorkItem{}).Where("id = ?", id).Count(&n).Error; err != nil {
			return err
		}
		if n == 0 {
			return NewNotFoundError("任务不存在")
		}
		linked, err := linkWorkSession(tx, id, sessionID, time.Now())
		if err != nil {
			return err
		}
		if !linked {
			return NewNotFoundError("会话不存在")
		}
		return nil
	})
}

// UnlinkWorkItemSession 解除任务与会话的关联（不影响会话本身）。
func (s *Store) UnlinkWorkItemSession(ctx context.Context, id uint, sessionID string) error {
	return s.WithTx(ctx, func(tx *gorm.DB) error {
		res := tx.Where("work_item_id = ? AND session_id = ?", id, sessionID).Delete(&model.WorkItemSession{})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			return NewNotFoundError("这个会话没有关联到该任务")
		}
		return nil
	})
}

// CompactWorkItems 压缩关闭已久的任务：时间线与「最新进度」不再保留，没有结论的从进度里截一段当结论。
// 标题 / 类型 / 标签 / 日期 / 结论 / 引用 / 关联会话 都保留。可重复执行，返回本次压缩的任务数。
func (s *Store) CompactWorkItems(ctx context.Context, now time.Time) (int, error) {
	cutoff := now.AddDate(0, 0, -model.WorkCompactAfterDays)
	var candidates []model.WorkItem
	if err := s.db.WithContext(ctx).Where("state IN ('done','shelved') AND closed_at IS NOT NULL AND closed_at < ?", cutoff).
		Find(&candidates).Error; err != nil {
		return 0, err
	}
	compacted := 0
	for _, c := range candidates {
		err := s.WithTx(ctx, func(tx *gorm.DB) error {
			var n int64
			if err := tx.Model(&model.WorkItemUpdate{}).Where("work_item_id = ?", c.ID).Count(&n).Error; err != nil {
				return err
			}
			if n == 0 && strings.TrimSpace(c.Progress) == "" {
				return nil // 已经压缩过
			}
			updates := map[string]any{"progress": ""}
			if strings.TrimSpace(c.Conclusion) == "" && strings.TrimSpace(c.Progress) != "" {
				updates["conclusion"] = truncateRunes(strings.TrimSpace(c.Progress), model.WorkConclusionFallback)
			}
			// 不改 updated_at：压缩是后台整理，不应让旧任务重新排到列表前面。
			if err := tx.Model(&model.WorkItem{}).Where("id = ?", c.ID).UpdateColumns(updates).Error; err != nil {
				return err
			}
			if err := tx.Where("work_item_id = ?", c.ID).Delete(&model.WorkItemUpdate{}).Error; err != nil {
				return err
			}
			compacted++
			return nil
		})
		if err != nil {
			return compacted, err
		}
	}
	return compacted, nil
}
