package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/teacat99/RelayMesh/v2/internal/confirm"
	"github.com/teacat99/RelayMesh/v2/internal/docs"
	"github.com/teacat99/RelayMesh/v2/internal/model"
	"gorm.io/gorm"
)

// 确认项（ConfirmItem）的存储层。
//
// 两条贯穿全文件的约定：
//   - 写库只有一个连接（SetMaxOpenConns(1)）：WithTx 回调里严禁再调用任何走 s.db 的方法（会等自己释放连接而死锁），
//     事务里一律使用传进来的 tx；对外的 Get / List 才走 s.db。
//   - 「作答 + 投递给智能体的收件箱消息」在同一个事务里提交：要么都成功，要么都不留痕，
//     不会出现「界面显示已答复、智能体却永远收不到」的半成品状态。

// ConfirmCreateInput 创建确认项（REST 不暴露；MCP async_ask 与文档同步使用）。
type ConfirmCreateInput struct {
	WorkflowID    string
	Kind          string
	Title         string
	Body          string
	Options       []model.ConfirmOption
	Multi         bool
	AllowNote     bool
	DefaultOption string
	Risk          string
	TaskSeq       int
	DocID         uint
	DocKey        string
	Anchor        string
	CreatedBy     string
}

// ConfirmFilter 列表筛选。Ascending=false 时新的在前（序号倒序），true 时旧的在前（待确认队列用）。
type ConfirmFilter struct {
	WorkflowID string
	Statuses   []string
	Kinds      []string
	TaskSeq    int
	DocID      uint
	Limit      int
	Offset     int
	Ascending  bool
}

// ConfirmAnswerInput 一次作答：选项 id + 可选备注 + 可选的自定义答复（#183）+ 作答来源。
type ConfirmAnswerInput struct {
	OptionIDs []string
	Note      string
	Custom    string
	By        string
}

// ConfirmBatchItem 是批量作答里的一项；Options 为空且批量要求「采用推荐」时取该项的推荐选项。
type ConfirmBatchItem struct {
	Ref     string   `json:"ref"`
	Options []string `json:"options,omitempty"`
	Note    string   `json:"note,omitempty"`
}

// ConfirmBatchResult 批量作答的结果：已作答的项与投递给智能体的那条收件箱文案。
type ConfirmBatchResult struct {
	Items   []*model.ConfirmItem
	Message string
}

const (
	confirmOptionIDMax     = 64
	confirmOptionLabelMax  = 200
	confirmOptionDetailMax = 2000
	confirmAnswerBy        = "human"
)

// nextConfirmSeq 在事务里分配工作流内的下一个序号。
// 与 nextDocSeq 同一套路：计数器行不存在时用现有最大序号播种（兼容手工写入的行），然后自增；
// 写库被 WithTx 串行化，不需要（SQLite 也不支持）行锁。
func nextConfirmSeq(tx *gorm.DB, workflowID string) (int, error) {
	var c model.ConfirmItemCounter
	err := tx.Where("workflow_id = ?", workflowID).First(&c).Error
	switch {
	case err == nil:
	case errors.Is(err, gorm.ErrRecordNotFound):
		var max int
		if e := tx.Model(&model.ConfirmItem{}).Where("workflow_id = ?", workflowID).
			Select("COALESCE(MAX(seq), 0)").Scan(&max).Error; e != nil {
			return 0, e
		}
		c = model.ConfirmItemCounter{WorkflowID: workflowID, LastSeq: max}
	default:
		return 0, err
	}
	c.LastSeq++
	if err := tx.Save(&c).Error; err != nil {
		return 0, err
	}
	return c.LastSeq, nil
}

// NormalizeConfirmOptions 校验并规整选项：2～8 个，id 非空唯一，文案非空，各字段限长。
func NormalizeConfirmOptions(in []model.ConfirmOption) ([]model.ConfirmOption, error) {
	if len(in) < model.ConfirmOptionsMin || len(in) > model.ConfirmOptionsMax {
		return nil, NewInvalidInputError(fmt.Sprintf("选项需要 %d～%d 个，当前 %d 个", model.ConfirmOptionsMin, model.ConfirmOptionsMax, len(in)))
	}
	out := make([]model.ConfirmOption, 0, len(in))
	seen := make(map[string]bool, len(in))
	for i, o := range in {
		id := strings.TrimSpace(o.ID)
		label := strings.TrimSpace(o.Label)
		detail := strings.TrimSpace(o.Detail)
		switch {
		case id == "":
			return nil, NewInvalidInputError(fmt.Sprintf("第 %d 个选项缺少 id", i+1))
		case label == "":
			return nil, NewInvalidInputError(fmt.Sprintf("选项 %s 缺少 label", id))
		case utf8.RuneCountInString(id) > confirmOptionIDMax:
			return nil, NewInvalidInputError(fmt.Sprintf("选项 id 不能超过 %d 字", confirmOptionIDMax))
		case utf8.RuneCountInString(label) > confirmOptionLabelMax:
			return nil, NewInvalidInputError(fmt.Sprintf("选项 %s 的 label 不能超过 %d 字", id, confirmOptionLabelMax))
		case utf8.RuneCountInString(detail) > confirmOptionDetailMax:
			return nil, NewInvalidInputError(fmt.Sprintf("选项 %s 的 detail 不能超过 %d 字", id, confirmOptionDetailMax))
		case seen[id]:
			return nil, NewInvalidInputError(fmt.Sprintf("选项 id 重复：%s", id))
		}
		seen[id] = true
		out = append(out, model.ConfirmOption{ID: id, Label: label, Detail: detail, Recommended: o.Recommended})
	}
	return out, nil
}

func encodeConfirmOptions(opts []model.ConfirmOption) (string, string, error) {
	b, err := json.Marshal(opts)
	if err != nil {
		return "", "", err
	}
	return string(b), confirm.OptionsFingerprint(opts), nil
}

func decodeConfirmOptions(raw string) []model.ConfirmOption {
	var opts []model.ConfirmOption
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	if err := json.Unmarshal([]byte(raw), &opts); err != nil {
		return nil
	}
	return opts
}

func clipRunesTo(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n])
}

// CreateConfirmItem 创建一条确认项。created=false 表示命中了「同一问题仍在待处理」的去重，返回的是已有那条（智能体重试不会产生重复项）。
func (s *Store) CreateConfirmItem(ctx context.Context, in ConfirmCreateInput) (item *model.ConfirmItem, created bool, err error) {
	wf := strings.TrimSpace(in.WorkflowID)
	if wf == "" {
		return nil, false, NewInvalidInputError("workflow_id 不能为空")
	}
	title := strings.TrimSpace(in.Title)
	if title == "" {
		return nil, false, NewInvalidInputError("title 不能为空")
	}
	if utf8.RuneCountInString(title) > model.ConfirmTitleMax {
		return nil, false, NewInvalidInputError(fmt.Sprintf("title 不能超过 %d 字", model.ConfirmTitleMax))
	}
	body := strings.TrimSpace(in.Body)
	if utf8.RuneCountInString(body) > model.ConfirmBodyMax {
		return nil, false, NewInvalidInputError(fmt.Sprintf("body 不能超过 %d 字", model.ConfirmBodyMax))
	}
	kind := strings.TrimSpace(in.Kind)
	if kind == "" {
		kind = model.ConfirmKindAsk
	}
	switch kind {
	case model.ConfirmKindAsk, model.ConfirmKindPlan, model.ConfirmKindDecision:
	default:
		return nil, false, NewInvalidInputError("kind 必须是 ask / plan / decision")
	}
	opts, err := NormalizeConfirmOptions(in.Options)
	if err != nil {
		return nil, false, err
	}
	optsJSON, fp, err := encodeConfirmOptions(opts)
	if err != nil {
		return nil, false, err
	}
	def := strings.TrimSpace(in.DefaultOption)
	if def != "" {
		found := false
		for _, o := range opts {
			if o.ID == def {
				found = true
				break
			}
		}
		if !found {
			return nil, false, NewInvalidInputError("default_option 必须是某个选项的 id")
		}
	}
	risk := model.NormalizeConfirmRisk(in.Risk)
	createdBy := strings.TrimSpace(in.CreatedBy)
	if createdBy == "" {
		createdBy = model.ConfirmCreatedByAgent
	}
	// 计划确认必须绑定一个真实存在的任务：计划是「这件事打算怎么做」，脱离任务就没有可确认的对象。
	if kind == model.ConfirmKindPlan {
		if in.TaskSeq <= 0 {
			return nil, false, NewInvalidInputError("kind=plan 必须带 task_id（先用 task_board / workflow_context 建好任务）")
		}
	}
	if in.TaskSeq > 0 {
		if _, err := s.GetWorkItemBySeq(ctx, wf, in.TaskSeq); err != nil {
			return nil, false, err
		}
	}

	now := time.Now()
	err = s.WithTx(ctx, func(tx *gorm.DB) error {
		if createdBy == model.ConfirmCreatedByAgent {
			// 同一问题（标题 + 选项 + 绑定任务 + 风险档都相同）仍在待处理：直接返回已有的，不重复建。
			// 风险档必须算进去（#217）：否则先发的 L1 版会把同题的 L2 请求「吞掉」，返回的卡片就带着 L1 的批量 /
			// 一键采用推荐资格，智能体却以为自己发的是 L2。
			var dup model.ConfirmItem
			derr := tx.Where("workflow_id = ? AND status = ? AND kind = ? AND title = ? AND options_fp = ? AND task_seq = ? AND risk = ?",
				wf, model.ConfirmStatusPending, kind, title, fp, in.TaskSeq, risk).Order("id DESC").First(&dup).Error
			if derr == nil {
				item = &dup
				return nil
			}
			if !errors.Is(derr, gorm.ErrRecordNotFound) {
				return derr
			}
			var recent int64
			if err := tx.Model(&model.ConfirmItem{}).
				Where("workflow_id = ? AND created_by = ? AND created_at >= ?", wf, model.ConfirmCreatedByAgent, now.Add(-time.Hour)).
				Count(&recent).Error; err != nil {
				return err
			}
			if recent >= model.ConfirmCreateRateHour {
				return NewConflictError(fmt.Sprintf("这个工作流一小时内已通过 async_ask 创建了 %d 条确认项，请合并成一次提问，或稍后再试", model.ConfirmCreateRateHour), 0)
			}
		}
		seq, err := nextConfirmSeq(tx, wf)
		if err != nil {
			return err
		}
		row := model.ConfirmItem{
			WorkflowID: wf, Seq: seq, Kind: kind, Title: title, Body: body,
			OptionsJSON: optsJSON, Multi: in.Multi, AllowNote: in.AllowNote,
			DefaultOption: def, Risk: risk,
			TaskSeq: in.TaskSeq, DocID: in.DocID, DocKey: in.DocKey, Anchor: in.Anchor,
			OptionsFP: fp, Status: model.ConfirmStatusPending, CreatedBy: createdBy,
			CreatedAt: now, UpdatedAt: now,
		}
		if err := tx.Create(&row).Error; err != nil {
			return err
		}
		item = &row
		created = true
		return nil
	})
	if err != nil {
		return nil, false, err
	}
	return item, created, nil
}

// getConfirmItem 按引用（Q12 / 12 / 数据库 id）取一条；db 可以是 s.db，也可以是事务里的 tx。
func getConfirmItem(db *gorm.DB, workflowID, ref string) (*model.ConfirmItem, error) {
	seq, id, ok := confirm.ParseRef(ref)
	if !ok {
		return nil, NewInvalidInputError("无效的确认项引用（应为 Q12 或数字）")
	}
	q := db.Where("workflow_id = ?", workflowID)
	if id > 0 {
		q = q.Where("id = ?", id)
	} else {
		q = q.Where("seq = ?", seq)
	}
	var item model.ConfirmItem
	if err := q.First(&item).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, NewNotFoundError("确认项不存在")
		}
		return nil, err
	}
	return &item, nil
}

// GetConfirmItemByRef 取一条确认项（限定工作流，不能跨工作流读取）。
func (s *Store) GetConfirmItemByRef(ctx context.Context, workflowID, ref string) (*model.ConfirmItem, error) {
	return getConfirmItem(s.db.WithContext(ctx), workflowID, ref)
}

// CountPendingConfirms 统计待处理的确认项数。
func (s *Store) CountPendingConfirms(ctx context.Context, workflowID string) (int, error) {
	var n int64
	err := s.db.WithContext(ctx).Model(&model.ConfirmItem{}).
		Where("workflow_id = ? AND status = ?", workflowID, model.ConfirmStatusPending).Count(&n).Error
	return int(n), err
}

// CountPendingConfirmsByWorkflow 一次查出多个工作流各自待处理的确认项数（侧栏会话卡用，避免逐个工作流查询；#159）。
// 返回表里没有的工作流表示 0 条待处理；传入空列表直接返回空表。
func (s *Store) CountPendingConfirmsByWorkflow(ctx context.Context, workflowIDs []string) (map[string]int, error) {
	out := make(map[string]int, len(workflowIDs))
	// 分批：会话列表上限 200 行，一般一批就够；SQLite 的绑定参数个数有上限，宁可多分几批。
	const batch = 400
	for start := 0; start < len(workflowIDs); start += batch {
		end := start + batch
		if end > len(workflowIDs) {
			end = len(workflowIDs)
		}
		var rows []struct {
			WorkflowID string
			N          int64
		}
		err := s.db.WithContext(ctx).Model(&model.ConfirmItem{}).
			Select("workflow_id AS workflow_id, COUNT(*) AS n").
			Where("workflow_id IN ? AND status = ?", workflowIDs[start:end], model.ConfirmStatusPending).
			Group("workflow_id").Scan(&rows).Error
		if err != nil {
			return nil, err
		}
		for _, r := range rows {
			out[r.WorkflowID] = int(r.N)
		}
	}
	return out, nil
}

// ListConfirmItems 列出确认项。
func (s *Store) ListConfirmItems(ctx context.Context, f ConfirmFilter) ([]model.ConfirmItem, error) {
	q := s.db.WithContext(ctx).Where("workflow_id = ?", f.WorkflowID)
	if len(f.Statuses) > 0 {
		q = q.Where("status IN ?", f.Statuses)
	}
	if len(f.Kinds) > 0 {
		q = q.Where("kind IN ?", f.Kinds)
	}
	if f.TaskSeq > 0 {
		q = q.Where("task_seq = ?", f.TaskSeq)
	}
	if f.DocID > 0 {
		q = q.Where("doc_id = ?", f.DocID)
	}
	limit := f.Limit
	if limit <= 0 || limit > 200 {
		limit = 100
	}
	order := "seq DESC"
	if f.Ascending {
		order = "seq ASC"
	}
	var list []model.ConfirmItem
	if err := q.Order(order).Limit(limit).Offset(f.Offset).Find(&list).Error; err != nil {
		return nil, err
	}
	return list, nil
}

// ConfirmViews 把确认项转成对外视图，并给绑定了文档的项补上文档路径与标题。
func (s *Store) ConfirmViews(ctx context.Context, items []model.ConfirmItem) ([]model.ConfirmItemView, error) {
	views := make([]model.ConfirmItemView, len(items))
	docIDs := map[uint]bool{}
	for i := range items {
		views[i] = model.NewConfirmItemView(&items[i])
		if items[i].DocID > 0 {
			docIDs[items[i].DocID] = true
		}
	}
	if len(docIDs) == 0 {
		return views, nil
	}
	ids := make([]uint, 0, len(docIDs))
	for id := range docIDs {
		ids = append(ids, id)
	}
	var docRows []model.ProjectDoc
	if err := s.db.WithContext(ctx).Select("id", "path", "title").Where("id IN ?", ids).Find(&docRows).Error; err != nil {
		return nil, err
	}
	byID := make(map[uint]model.ProjectDoc, len(docRows))
	for _, d := range docRows {
		byID[d.ID] = d
	}
	for i := range views {
		if d, ok := byID[views[i].DocID]; ok {
			views[i].DocPath, views[i].DocTitle = d.Path, d.Title
		}
	}
	return views, nil
}

// ConfirmView 是 ConfirmViews 的单条版本。
func (s *Store) ConfirmView(ctx context.Context, item *model.ConfirmItem) (model.ConfirmItemView, error) {
	views, err := s.ConfirmViews(ctx, []model.ConfirmItem{*item})
	if err != nil {
		return model.ConfirmItemView{}, err
	}
	return views[0], nil
}

// validateConfirmAnswer 校验一次作答：选项都属于该项、单选只选一个、不重复、备注与自定义答复合规。
//
// 自定义答复（#183）是「预设选项都不合适」时的出路，所有确认项都接受，不需要智能体事先开启：
// 单选时它与预设选项二选一（给了自定义就不能再选选项），多选时可以与选中的选项并存；
// 只写自定义、一个选项都不选是合法的。备注仍受 allow_note 约束，与自定义答复互不替代。
// 返回规整（去首尾空白）后的备注与自定义答复。
func validateConfirmAnswer(item *model.ConfirmItem, optionIDs []string, note, custom string) (string, string, error) {
	opts := decodeConfirmOptions(item.OptionsJSON)
	allowed := make(map[string]bool, len(opts))
	for _, o := range opts {
		allowed[o.ID] = true
	}
	custom = strings.TrimSpace(custom)
	if utf8.RuneCountInString(custom) > model.ConfirmCustomMax {
		return "", "", NewInvalidInputError(fmt.Sprintf("自定义答复不能超过 %d 字", model.ConfirmCustomMax))
	}
	if len(optionIDs) == 0 && custom == "" {
		return "", "", NewInvalidInputError("必须选择至少一个选项，或写下自定义答复")
	}
	if !item.Multi {
		if custom != "" && len(optionIDs) > 0 {
			return "", "", NewInvalidInputError("该项是单选：选预设选项和写自定义答复只能二选一")
		}
		if custom == "" && len(optionIDs) != 1 {
			return "", "", NewInvalidInputError("该项是单选，只能选一个选项")
		}
	}
	seen := make(map[string]bool, len(optionIDs))
	for _, id := range optionIDs {
		if !allowed[id] {
			return "", "", NewInvalidInputError(fmt.Sprintf("选项 %q 不属于 %s", id, model.ConfirmRef(item.Seq)))
		}
		if seen[id] {
			return "", "", NewInvalidInputError("选项不能重复")
		}
		seen[id] = true
	}
	note = strings.TrimSpace(note)
	if note != "" && !item.AllowNote {
		return "", "", NewInvalidInputError(fmt.Sprintf("%s 不接受备注", model.ConfirmRef(item.Seq)))
	}
	if utf8.RuneCountInString(note) > model.ConfirmNoteMax {
		return "", "", NewInvalidInputError(fmt.Sprintf("备注不能超过 %d 字", model.ConfirmNoteMax))
	}
	return note, custom, nil
}

// answerConfirmTx 在事务里把一条待处理的确认项置为已答复。
func answerConfirmTx(tx *gorm.DB, item *model.ConfirmItem, optionIDs []string, note, custom, by string, now time.Time) error {
	if item.Status != model.ConfirmStatusPending {
		return NewConflictError(fmt.Sprintf("%s 已处理（%s），不能重复作答", model.ConfirmRef(item.Seq), item.Status), 0)
	}
	if optionIDs == nil {
		optionIDs = []string{} // 只写自定义答复时序列化成 [] 而不是 null，读取方（界面 / 智能体）都按数组处理
	}
	ans := model.ConfirmAnswer{OptionIDs: optionIDs, Note: note, Custom: custom, By: by}
	b, err := json.Marshal(ans)
	if err != nil {
		return err
	}
	res := tx.Model(&model.ConfirmItem{}).
		Where("id = ? AND status = ?", item.ID, model.ConfirmStatusPending).
		Updates(map[string]any{
			"status": model.ConfirmStatusAnswered, "answer_json": string(b),
			"answered_at": now, "updated_at": now,
		})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return NewConflictError(fmt.Sprintf("%s 已被处理，不能重复作答", model.ConfirmRef(item.Seq)), 0)
	}
	item.Status = model.ConfirmStatusAnswered
	item.AnswerJSON = string(b)
	item.AnsweredAt = &now
	item.UpdatedAt = now
	return nil
}

// queueAnswerMessageTx 在事务里往收件箱放一条「确认项答复」。它不占追加条数上限，
// 也不会被 interactive_feedback 的秒回带走（见 consumeInboxTx）：只经 inbox / hook / 回执提示行送达。
//
// originRef 记下这条答复属于哪些确认项（c:Q12，多条用逗号分隔），聊天消息流据此把投递状态挂回对应的卡。
func queueAnswerMessageTx(tx *gorm.DB, workflowID, text, originRef string, now time.Time) error {
	q := model.QueuedFeedback{
		WorkflowID: workflowID, ResponseText: text, Status: "pending",
		Kind: model.InboxKindAnswer, OriginRef: originRef, CreatedAt: now, UpdatedAt: now,
	}
	return tx.Create(&q).Error
}

// confirmOriginRef 把确认项序号写成收件箱的 origin_ref 记号（c:Q12）。
func confirmOriginRef(seqs ...int) string {
	refs := make([]string, 0, len(seqs))
	for _, s := range seqs {
		refs = append(refs, "c:"+model.ConfirmRef(s))
	}
	return strings.Join(refs, ",")
}

// AnswerConfirmItem 单条作答。第二次作答（并发或重复点击）返回 409；作答与投递在同一事务里。
func (s *Store) AnswerConfirmItem(ctx context.Context, workflowID, ref string, in ConfirmAnswerInput) (*model.ConfirmItem, error) {
	by := strings.TrimSpace(in.By)
	if by == "" {
		by = confirmAnswerBy
	}
	var out *model.ConfirmItem
	err := s.WithTx(ctx, func(tx *gorm.DB) error {
		item, err := getConfirmItem(tx, workflowID, ref)
		if err != nil {
			return err
		}
		if item.Status != model.ConfirmStatusPending {
			return NewConflictError(fmt.Sprintf("%s 已处理（%s），不能重复作答", model.ConfirmRef(item.Seq), item.Status), 0)
		}
		note, custom, err := validateConfirmAnswer(item, in.OptionIDs, in.Note, in.Custom)
		if err != nil {
			return err
		}
		now := time.Now()
		if err := answerConfirmTx(tx, item, in.OptionIDs, note, custom, by, now); err != nil {
			return err
		}
		if err := queueAnswerMessageTx(tx, workflowID, confirm.FormatAnswerLine(item, in.OptionIDs, note, custom), confirmOriginRef(item.Seq), now); err != nil {
			return err
		}
		out = item
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// AnswerConfirmBatch 批量作答，整批原子：只要有一项不合格（不存在 / 已处理 / 选项非法 / 没有可采用的推荐 / L2），
// 整批都不落库，错误里一次列出全部问题。L2 项永远不进批量：不可逆的决定必须逐条确认。
// adoptRecommended=true 时，没写选项的项取各自的推荐选项（单选取第一个、多选取全部）。
func (s *Store) AnswerConfirmBatch(ctx context.Context, workflowID string, items []ConfirmBatchItem, adoptRecommended bool) (*ConfirmBatchResult, error) {
	if len(items) == 0 {
		return nil, NewInvalidInputError("items 不能为空")
	}
	if len(items) > model.ConfirmBatchMax {
		return nil, NewInvalidInputError(fmt.Sprintf("一次最多作答 %d 条", model.ConfirmBatchMax))
	}
	res := &ConfirmBatchResult{}
	err := s.WithTx(ctx, func(tx *gorm.DB) error {
		type prepared struct {
			item *model.ConfirmItem
			opts []string
			note string
		}
		var preps []prepared
		var problems []string
		seen := map[uint]bool{}
		for _, it := range items {
			item, err := getConfirmItem(tx, workflowID, it.Ref)
			if err != nil {
				problems = append(problems, fmt.Sprintf("%s：%s", strings.TrimSpace(it.Ref), appErrMessage(err)))
				continue
			}
			ref := model.ConfirmRef(item.Seq)
			switch {
			case seen[item.ID]:
				problems = append(problems, ref+"：重复出现在同一批里")
				continue
			case item.Status != model.ConfirmStatusPending:
				problems = append(problems, fmt.Sprintf("%s：已处理（%s）", ref, item.Status))
				continue
			case item.Risk == model.ConfirmRiskL2:
				problems = append(problems, ref+"：L2（不可逆 / 高风险）不能批量处理，请逐条确认")
				continue
			}
			seen[item.ID] = true
			optionIDs := it.Options
			if len(optionIDs) == 0 && adoptRecommended {
				optionIDs = confirm.RecommendedOptionIDs(decodeConfirmOptions(item.OptionsJSON), item.Multi)
				if len(optionIDs) == 0 {
					problems = append(problems, ref+"：没有推荐选项，需要手动选择")
					continue
				}
			}
			// 批量入口只做「采用推荐 / 逐项点选」，不带自定义答复（自定义答复需要逐条写，走单条作答）
			note, _, err := validateConfirmAnswer(item, optionIDs, it.Note, "")
			if err != nil {
				problems = append(problems, fmt.Sprintf("%s：%s", ref, appErrMessage(err)))
				continue
			}
			preps = append(preps, prepared{item: item, opts: optionIDs, note: note})
		}
		if len(problems) > 0 {
			return NewInvalidInputError("批量作答失败，没有任何一项被保存：\n" + strings.Join(problems, "\n"))
		}
		now := time.Now()
		lines := make([]string, 0, len(preps))
		seqs := make([]int, 0, len(preps))
		for _, p := range preps {
			if err := answerConfirmTx(tx, p.item, p.opts, p.note, "", confirmAnswerBy, now); err != nil {
				return err
			}
			lines = append(lines, confirm.FormatAnswerLine(p.item, p.opts, p.note, ""))
			seqs = append(seqs, p.item.Seq)
			res.Items = append(res.Items, p.item)
		}
		res.Message = strings.Join(lines, "\n")
		return queueAnswerMessageTx(tx, workflowID, res.Message, confirmOriginRef(seqs...), now)
	})
	if err != nil {
		return nil, err
	}
	return res, nil
}

// appErrMessage 取出业务错误里给人看的那句话。
func appErrMessage(err error) string {
	var ae *AppError
	if errors.As(err, &ae) {
		return ae.Message
	}
	return err.Error()
}

// DismissConfirmItem 用户放弃一条待处理的确认项，并告诉智能体「不用再等它」。
func (s *Store) DismissConfirmItem(ctx context.Context, workflowID, ref, reason string) (*model.ConfirmItem, error) {
	reason = clipRunesTo(strings.TrimSpace(reason), model.ConfirmNoteMax)
	var out *model.ConfirmItem
	err := s.WithTx(ctx, func(tx *gorm.DB) error {
		item, err := getConfirmItem(tx, workflowID, ref)
		if err != nil {
			return err
		}
		if item.Status != model.ConfirmStatusPending {
			return NewConflictError(fmt.Sprintf("%s 已处理（%s），不能再忽略", model.ConfirmRef(item.Seq), item.Status), 0)
		}
		now := time.Now()
		res := tx.Model(&model.ConfirmItem{}).
			Where("id = ? AND status = ?", item.ID, model.ConfirmStatusPending).
			Updates(map[string]any{"status": model.ConfirmStatusDismissed, "updated_at": now})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			return NewConflictError(fmt.Sprintf("%s 已被处理", model.ConfirmRef(item.Seq)), 0)
		}
		item.Status, item.UpdatedAt = model.ConfirmStatusDismissed, now
		if err := queueAnswerMessageTx(tx, workflowID, confirm.FormatDismissLine(item, reason), confirmOriginRef(item.Seq), now); err != nil {
			return err
		}
		out = item
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// WithdrawConfirmItem 撤回一条待处理的确认项（智能体撤回自己提的问题）。
// agentOnly=true 时只能撤回智能体创建的：文档决策卡由文档内容决定，智能体不能绕过文档去撤销。
func (s *Store) WithdrawConfirmItem(ctx context.Context, workflowID, ref string, agentOnly bool) (*model.ConfirmItem, error) {
	var out *model.ConfirmItem
	err := s.WithTx(ctx, func(tx *gorm.DB) error {
		item, err := getConfirmItem(tx, workflowID, ref)
		if err != nil {
			return err
		}
		if item.Status != model.ConfirmStatusPending {
			return NewConflictError(fmt.Sprintf("%s 已处理（%s），不能撤回", model.ConfirmRef(item.Seq), item.Status), 0)
		}
		if agentOnly && item.CreatedBy != model.ConfirmCreatedByAgent {
			return NewConflictError(fmt.Sprintf("%s 不是智能体创建的（来自文档决策卡），不能通过 async_ask 撤回", model.ConfirmRef(item.Seq)), 0)
		}
		now := time.Now()
		res := tx.Model(&model.ConfirmItem{}).
			Where("id = ? AND status = ?", item.ID, model.ConfirmStatusPending).
			Updates(map[string]any{"status": model.ConfirmStatusWithdrawn, "updated_at": now})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			return NewConflictError(fmt.Sprintf("%s 已被处理", model.ConfirmRef(item.Seq)), 0)
		}
		item.Status, item.UpdatedAt = model.ConfirmStatusWithdrawn, now
		out = item
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// ---------- 文档决策卡 ↔ 确认项 ----------

// decisionWant 是文档里一张有效决策卡对应的确认项期望字段。
type decisionWant struct {
	key       string
	title     string
	body      string
	opts      []model.ConfirmOption
	optsJSON  string
	fp        string
	def       string
	multi     bool
	allowNote bool
	risk      string
}

// wantedDecisions 解析文档正文里的有效决策卡；重复的 id 只取第一张，校验不过的围栏跳过（页面上会显示错误）。
func wantedDecisions(body string) []decisionWant {
	var out []decisionWant
	seen := map[string]bool{}
	for _, pd := range docs.ParseDecisions(body) {
		if len(pd.Errors) > 0 {
			continue
		}
		key := strings.TrimSpace(pd.Card.ID)
		if key == "" || seen[key] {
			continue
		}
		opts, def, multi, allowNote, risk := confirm.FromDecisionCard(pd.Card)
		opts, err := NormalizeConfirmOptions(opts)
		if err != nil {
			continue
		}
		optsJSON, fp, err := encodeConfirmOptions(opts)
		if err != nil {
			continue
		}
		seen[key] = true
		out = append(out, decisionWant{
			key:   key,
			title: clipRunesTo(strings.TrimSpace(pd.Card.Title), model.ConfirmTitleMax),
			body:  clipRunesTo(strings.TrimSpace(pd.Card.Body), model.ConfirmBodyMax),
			opts:  opts, optsJSON: optsJSON, fp: fp, def: def, multi: multi, allowNote: allowNote, risk: risk,
		})
	}
	return out
}

func docIsLive(doc *model.ProjectDoc) bool {
	return doc != nil && doc.DeletedAt == nil && doc.Status != model.DocStatusArchived
}

// SyncDecisionConfirmItems 在文档保存后，让「文档里的 decision 围栏」与「确认项」保持一致。返回是否改动了数据。
//
// 对每个决策 id，只看它最新的一条确认项（序号最大）：
//   - 没有：新建待处理；
//   - 待处理：把展示字段（标题、说明、选项、默认值、风险……）更新成文档里的最新内容，原地保留序号；
//   - 已答复：选项没变就保留答复；选项变了（指纹不同）则旧答复作废（expired）并新建一条待处理——答案是针对旧选项给的，不能套到新选项上；
//   - 已忽略：选项没变就一直忽略，不会因为再次保存文档而「复活」；选项变了才新建；
//   - 已撤回 / 已过期：围栏回来了就重新新建一条。
//
// 文档里已经没有的围栏，其待处理项撤回；已答复 / 已忽略的历史保留。文档归档或删除时，待处理项全部撤回。
// 幂等：内容没变的重复保存不写库、不换序号。
func (s *Store) SyncDecisionConfirmItems(ctx context.Context, doc *model.ProjectDoc, body string) (bool, error) {
	if doc == nil || doc.ID == 0 || doc.WorkflowID == "" {
		return false, nil
	}
	var wants []decisionWant
	if docIsLive(doc) {
		wants = wantedDecisions(body)
	}
	changed := false
	err := s.WithTx(ctx, func(tx *gorm.DB) error {
		var existing []model.ConfirmItem
		if err := tx.Where("workflow_id = ? AND doc_id = ? AND kind = ?", doc.WorkflowID, doc.ID, model.ConfirmKindDecision).
			Order("seq ASC").Find(&existing).Error; err != nil {
			return err
		}
		latest := map[string]*model.ConfirmItem{}
		for i := range existing {
			latest[existing[i].DocKey] = &existing[i] // 序号升序遍历，后写覆盖前写 = 取最新
		}
		now := time.Now()
		create := func(w decisionWant) error {
			seq, err := nextConfirmSeq(tx, doc.WorkflowID)
			if err != nil {
				return err
			}
			changed = true
			return tx.Create(&model.ConfirmItem{
				WorkflowID: doc.WorkflowID, Seq: seq, Kind: model.ConfirmKindDecision,
				Title: w.title, Body: w.body, OptionsJSON: w.optsJSON, Multi: w.multi, AllowNote: w.allowNote,
				DefaultOption: w.def, Risk: w.risk, DocID: doc.ID, DocKey: w.key, Anchor: w.key, OptionsFP: w.fp,
				Status: model.ConfirmStatusPending, CreatedBy: model.ConfirmCreatedByDoc,
				CreatedAt: now, UpdatedAt: now,
			}).Error
		}
		wanted := map[string]bool{}
		for _, w := range wants {
			wanted[w.key] = true
			cur := latest[w.key]
			if cur == nil {
				if err := create(w); err != nil {
					return err
				}
				continue
			}
			switch cur.Status {
			case model.ConfirmStatusPending:
				if cur.OptionsFP == w.fp && cur.Title == w.title && cur.Body == w.body && cur.DefaultOption == w.def &&
					cur.Multi == w.multi && cur.AllowNote == w.allowNote && cur.Risk == w.risk {
					continue
				}
				if err := tx.Model(&model.ConfirmItem{}).Where("id = ? AND status = ?", cur.ID, model.ConfirmStatusPending).
					Updates(map[string]any{
						"title": w.title, "body": w.body, "options_json": w.optsJSON, "options_fp": w.fp,
						"default_option": w.def, "multi": w.multi, "allow_note": w.allowNote, "risk": w.risk, "updated_at": now,
					}).Error; err != nil {
					return err
				}
				changed = true
			case model.ConfirmStatusAnswered:
				if cur.OptionsFP == w.fp {
					continue
				}
				if err := tx.Model(&model.ConfirmItem{}).Where("id = ?", cur.ID).
					Updates(map[string]any{"status": model.ConfirmStatusExpired, "updated_at": now}).Error; err != nil {
					return err
				}
				if err := create(w); err != nil {
					return err
				}
			case model.ConfirmStatusDismissed:
				if cur.OptionsFP == w.fp {
					continue
				}
				if err := create(w); err != nil {
					return err
				}
			default: // withdrawn / expired：围栏回来了
				if err := create(w); err != nil {
					return err
				}
			}
		}
		for key, cur := range latest {
			if wanted[key] || cur.Status != model.ConfirmStatusPending {
				continue
			}
			if err := tx.Model(&model.ConfirmItem{}).Where("id = ? AND status = ?", cur.ID, model.ConfirmStatusPending).
				Updates(map[string]any{"status": model.ConfirmStatusWithdrawn, "updated_at": now}).Error; err != nil {
				return err
			}
			changed = true
		}
		return nil
	})
	return changed, err
}

// WithdrawDocPendingConfirms 文档归档 / 删除时，撤回它所有待处理的决策项。
func (s *Store) WithdrawDocPendingConfirms(ctx context.Context, docID uint) error {
	return s.db.WithContext(ctx).Model(&model.ConfirmItem{}).
		Where("doc_id = ? AND status = ?", docID, model.ConfirmStatusPending).
		Updates(map[string]any{"status": model.ConfirmStatusWithdrawn, "updated_at": time.Now()}).Error
}

// DecisionConfirmsByKey 返回文档内各决策 id 对应的最新确认项（序号最大的那条）。
func (s *Store) DecisionConfirmsByKey(ctx context.Context, docID uint) (map[string]*model.ConfirmItem, error) {
	var list []model.ConfirmItem
	if err := s.db.WithContext(ctx).
		Where("doc_id = ? AND kind = ?", docID, model.ConfirmKindDecision).
		Order("seq DESC").Find(&list).Error; err != nil {
		return nil, err
	}
	out := map[string]*model.ConfirmItem{}
	for i := range list {
		if _, ok := out[list[i].DocKey]; !ok {
			out[list[i].DocKey] = &list[i]
		}
	}
	return out, nil
}

// BackfillDecisionConfirmItems 启动时给「升级前就存在的文档」补上决策卡对应的确认项（第一波只解析展示，没有确认项）。
// 幂等，对已经同步过的文档不写库；单篇失败只记日志，不拖垮启动。
func (s *Store) BackfillDecisionConfirmItems(ctx context.Context) {
	var docRows []model.ProjectDoc
	if err := s.db.WithContext(ctx).
		Where("deleted_at IS NULL AND status <> ? AND body LIKE ?", model.DocStatusArchived, "%```decision%").
		Find(&docRows).Error; err != nil {
		log.Printf("[confirm] 回填决策卡确认项：读取文档失败: %v", err)
		return
	}
	for i := range docRows {
		if _, err := s.SyncDecisionConfirmItems(ctx, &docRows[i], docRows[i].Body); err != nil {
			log.Printf("[confirm] 回填决策卡确认项失败（文档 #%d）: %v", docRows[i].ID, err)
		}
	}
}
