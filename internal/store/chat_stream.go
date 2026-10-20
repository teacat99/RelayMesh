package store

import (
	"context"
	"strconv"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/teacat99/RelayMesh/v2/internal/model"
)

// 聊天消息流的取数（设计稿 03）：1:1 会话的消息读时合成，来源是四张旧表。
// 这里只负责「按来源、按时间边界取一小页原始行」；合成消息、排序与分页的拼装在 internal/chat。
//
// 每个来源各自按自己的时间字段取，互不依赖：一行 feedback_sessions 可以产出两条消息（汇报在 created_at，回复在 responded_at），
// 所以它拆成「汇报 / 回复 / 终止」三个独立的取数函数。边界是毫秒时间，同一毫秒内的先后由 chat 包按（时间，来源序，主键）裁决，
// 这里取数时把同一毫秒整个包进来，不会漏。

// StreamDir 是取流的方向。
type StreamDir int

const (
	// StreamOlder 从边界往更早的方向取（没有边界就是最新的一页）。
	StreamOlder StreamDir = iota
	// StreamNewer 从边界往更新的方向取。
	StreamNewer
)

// StreamRange 约束一次取流。
type StreamRange struct {
	Dir StreamDir
	// Ms 是边界的 Unix 毫秒；0 表示没有边界。Older 包含这一毫秒（由调用方按完整排序键再裁掉边界之外的），Newer 同理。
	Ms int64
	// Limit 是每个来源最多取多少条。调用方传「页大小 + 1」：多出来的一条用来判断还有没有更多。
	Limit int
	// Query 是子串搜索词；空表示不搜。
	Query string
}

// msLocal 把毫秒时间换成进程本地时区的时间：库里的时间是按本地偏移写成文本的，SQLite 里逐字符比较，
// 参数不换成同一个时区，+08:00 与 Z 之间会整段比错（见 ListWorkflowUpdates 里的同一处说明）。
func msLocal(ms int64) time.Time { return time.UnixMilli(ms).In(time.Local) }

// apply 加上时间边界、排序与条数。col 是时间字段（可以是表达式），idCol 是同一行的主键字段，用来稳定同一时刻的先后。
func (r StreamRange) apply(q *gorm.DB, col, idCol string) *gorm.DB {
	if r.Dir == StreamNewer {
		if r.Ms > 0 {
			q = q.Where(col+" >= ?", msLocal(r.Ms))
		}
		q = q.Order(col + " ASC, " + idCol + " ASC")
	} else {
		if r.Ms > 0 {
			q = q.Where(col+" < ?", msLocal(r.Ms+1))
		}
		q = q.Order(col + " DESC, " + idCol + " DESC")
	}
	if r.Limit > 0 {
		q = q.Limit(r.Limit)
	}
	return q
}

// search 在给定的文本列里做子串搜索（不区分大小写的 ASCII；中文按字面）。
func (r StreamRange) search(q *gorm.DB, cols ...string) *gorm.DB {
	term := strings.TrimSpace(r.Query)
	if term == "" || len(cols) == 0 {
		return q
	}
	pattern := "%" + escapeLike(term) + "%"
	conds := make([]string, len(cols))
	args := make([]any, len(cols))
	for i, c := range cols {
		conds[i] = c + ` LIKE ? ESCAPE '\'`
		args[i] = pattern
	}
	return q.Where("("+strings.Join(conds, " OR ")+")", args...)
}

// replyTimeSQL 是「你的回复」的时间：优先 responded_at，旧数据依次回退到 consumed_at、updated_at。
const replyTimeSQL = "COALESCE(responded_at, consumed_at, updated_at)"

// hasReplySQL 判断一行会话里有没有你的回复内容（responded_at 之前的旧数据只能看内容）。
const hasReplySQL = "(responded_at IS NOT NULL OR (response_text IS NOT NULL AND response_text <> '') OR (user_messages IS NOT NULL AND CAST(user_messages AS TEXT) NOT IN ('', '[]', 'null')))"

// StreamReports 取 Agent 的阻塞式汇报（不含进度气泡）。不取你的回复与图片，免得白读大字段。
func (s *Store) StreamReports(ctx context.Context, workflowID string, r StreamRange) ([]model.FeedbackSession, error) {
	q := s.db.WithContext(ctx).Model(&model.FeedbackSession{}).
		Omit("response_text", "user_messages", "images").
		Where("workflow_id = ? AND "+notNoticeSQL, workflowID)
	q = r.search(q, "title", "summary")
	var rows []model.FeedbackSession
	if err := r.apply(q, "created_at", "id").Find(&rows).Error; err != nil {
		return nil, err
	}
	return rows, nil
}

// StreamNotices 取 Agent 的进度气泡（interactive_feedback wait:false 发布的 notice）。
func (s *Store) StreamNotices(ctx context.Context, workflowID string, r StreamRange) ([]model.FeedbackSession, error) {
	q := s.db.WithContext(ctx).Model(&model.FeedbackSession{}).
		Omit("response_text", "user_messages", "images").
		Where("workflow_id = ? AND COALESCE(kind, '') = ?", workflowID, model.FeedbackKindNotice)
	q = r.search(q, "title", "summary")
	var rows []model.FeedbackSession
	if err := r.apply(q, "created_at", "id").Find(&rows).Error; err != nil {
		return nil, err
	}
	return rows, nil
}

// StreamReplies 取你对各轮汇报的回复（含秒回带走的追加）。时间见 replyTimeSQL。
func (s *Store) StreamReplies(ctx context.Context, workflowID string, r StreamRange) ([]model.FeedbackSession, error) {
	q := s.db.WithContext(ctx).Model(&model.FeedbackSession{}).
		Omit("summary").
		Where("workflow_id = ? AND "+notNoticeSQL+" AND "+hasReplySQL, workflowID)
	q = r.search(q, "response_text", "user_messages")
	var rows []model.FeedbackSession
	if err := r.apply(q, replyTimeSQL, "id").Find(&rows).Error; err != nil {
		return nil, err
	}
	return rows, nil
}

// StreamEndings 取「等待超时 / 已取消」的汇报（聊天里显示成一行系统文字），时间是最后更新时间。
func (s *Store) StreamEndings(ctx context.Context, workflowID string, r StreamRange) ([]model.FeedbackSession, error) {
	if strings.TrimSpace(r.Query) != "" {
		return nil, nil // 系统行不参与搜索
	}
	q := s.db.WithContext(ctx).Model(&model.FeedbackSession{}).
		Omit("summary", "response_text", "user_messages", "images").
		Where("workflow_id = ? AND "+notNoticeSQL+" AND status IN ('timeout', 'cancelled')", workflowID)
	var rows []model.FeedbackSession
	if err := r.apply(q, "updated_at", "id").Find(&rows).Error; err != nil {
		return nil, err
	}
	return rows, nil
}

// systemInboxKinds 是由系统投递的收件箱行：确认项答复与群聊 @ 提醒。它们不是「你追加的话」，不进追加列表，也不并入秒回。
var systemInboxKinds = []string{model.InboxKindAnswer, model.InboxKindChatMention}

// inboxVisibleSQL 与 ListInboxItems 的筛选一致：你追加的话里，排队中、被 Agent 经 hook / 回执 / 工具读走的、已过期的。
// 被「秒回」并进某一轮的不在其中（它们的内容已经在那一轮的回复里）；确认项答复不在其中（它是卡的投递通道）；已撤回的另有系统行。
func inboxVisibleSQL() string {
	return "workflow_id = ? AND (kind IS NULL OR kind NOT IN ?) AND ((status = 'pending' AND created_at >= ?) OR (status = 'consumed' AND delivered_via IN ('hook','receipt','tool')) " +
		"OR (status = 'consumed' AND delivered_via = 'feedback' AND origin_ref LIKE 's:%') OR status = 'expired')"
}

// StreamMergedInbox 取被「秒回」并进指定几轮汇报的追加（origin_ref = s:<会话编号>），按被取走的先后排好。
// 聊天里它们各是一条独立的消息；那一轮的回复只剩合并之外的部分（见 chat.Builder.ReplyAfterMerge）。
func (s *Store) StreamMergedInbox(ctx context.Context, workflowID string, sessionIDs []string) (map[string][]model.QueuedFeedback, error) {
	out := map[string][]model.QueuedFeedback{}
	if len(sessionIDs) == 0 {
		return out, nil
	}
	refs := make([]string, 0, len(sessionIDs))
	for _, id := range sessionIDs {
		refs = append(refs, "s:"+id)
	}
	var rows []model.QueuedFeedback
	err := s.db.WithContext(ctx).Model(&model.QueuedFeedback{}).
		Where("workflow_id = ? AND status = 'consumed' AND delivered_via = 'feedback' AND origin_ref IN ?", workflowID, refs).
		Order("created_at ASC, id ASC").Find(&rows).Error
	if err != nil {
		return nil, err
	}
	for _, q := range rows {
		id := strings.TrimPrefix(q.OriginRef, "s:")
		out[id] = append(out[id], q)
	}
	return out, nil
}

// StreamInbox 取你随时追加的话（收件箱），状态由 DeriveState 推导。
func (s *Store) StreamInbox(ctx context.Context, workflowID string, r StreamRange) ([]model.QueuedFeedback, error) {
	q := s.db.WithContext(ctx).Model(&model.QueuedFeedback{}).
		Where(inboxVisibleSQL(), workflowID, systemInboxKinds, inboxCutoff(time.Now()))
	q = r.search(q, "response_text", "user_messages")
	var rows []model.QueuedFeedback
	if err := r.apply(q, "created_at", "id").Find(&rows).Error; err != nil {
		return nil, err
	}
	fillInboxStates(rows)
	return rows, nil
}

// StreamRetractions 取你撤回的追加（聊天里显示成「你撤回了一条消息」，不带正文），时间是撤回的时刻。
func (s *Store) StreamRetractions(ctx context.Context, workflowID string, r StreamRange) ([]model.QueuedFeedback, error) {
	if strings.TrimSpace(r.Query) != "" {
		return nil, nil
	}
	q := s.db.WithContext(ctx).Model(&model.QueuedFeedback{}).
		Omit("response_text", "user_messages", "images").
		Where("workflow_id = ? AND (kind IS NULL OR kind NOT IN ?) AND status = 'revoked'", workflowID, systemInboxKinds)
	var rows []model.QueuedFeedback
	if err := r.apply(q, "updated_at", "id").Find(&rows).Error; err != nil {
		return nil, err
	}
	fillInboxStates(rows)
	return rows, nil
}

// StreamAnswerDeliveries 取一个时间窗里的确认项答复投递行（kind=answer、origin_ref 不为空），
// 聊天据此给「你的答复」消息标上已送达 / 已读 / 已处理。窗口取这一页答复的最早与最晚时间，前后各留 2 秒。
func (s *Store) StreamAnswerDeliveries(ctx context.Context, workflowID string, from, to time.Time) ([]model.QueuedFeedback, error) {
	var rows []model.QueuedFeedback
	err := s.db.WithContext(ctx).Model(&model.QueuedFeedback{}).
		Omit("response_text", "user_messages", "images").
		Where("workflow_id = ? AND kind = ? AND origin_ref IS NOT NULL AND origin_ref <> '' AND created_at >= ? AND created_at <= ?",
			workflowID, model.InboxKindAnswer, from.Add(-2*time.Second).In(time.Local), to.Add(2*time.Second).In(time.Local)).
		Order("created_at ASC, id ASC").
		Find(&rows).Error
	if err != nil {
		return nil, err
	}
	fillInboxStates(rows)
	return rows, nil
}

// StreamCards 取 Agent 的提问卡与计划确认卡（async_ask / 计划）；文档决策卡另按文档汇总，见 StreamDecisionItems。
func (s *Store) StreamCards(ctx context.Context, workflowID string, r StreamRange) ([]model.ConfirmItem, error) {
	q := s.db.WithContext(ctx).Model(&model.ConfirmItem{}).
		Where("workflow_id = ? AND kind IN (?, ?)", workflowID, model.ConfirmKindAsk, model.ConfirmKindPlan)
	q = r.search(q, "title", "body")
	var rows []model.ConfirmItem
	if err := r.apply(q, "created_at", "id").Find(&rows).Error; err != nil {
		return nil, err
	}
	return rows, nil
}

// confirmAnswerTimeSQL 是「你对卡的答复」的时间：作答时间；放弃的卡没有作答时间，取最后更新时间。
const confirmAnswerTimeSQL = "COALESCE(answered_at, updated_at)"

// StreamAnswers 取你对提问卡 / 计划卡的答复（已作答或已忽略）。
func (s *Store) StreamAnswers(ctx context.Context, workflowID string, r StreamRange) ([]model.ConfirmItem, error) {
	q := s.db.WithContext(ctx).Model(&model.ConfirmItem{}).
		Where("workflow_id = ? AND kind IN (?, ?) AND status IN (?, ?)", workflowID,
			model.ConfirmKindAsk, model.ConfirmKindPlan, model.ConfirmStatusAnswered, model.ConfirmStatusDismissed)
	q = r.search(q, "title", "answer_json")
	var rows []model.ConfirmItem
	if err := r.apply(q, confirmAnswerTimeSQL, "id").Find(&rows).Error; err != nil {
		return nil, err
	}
	return rows, nil
}

// StreamDecisionItems 取一个工作流里全部文档决策项（最多 500 条，按创建时间升序）：它们按文档汇总成卡，
// 汇总要看到同一文档的全部决策，所以不分页，交给 chat 包按边界裁剪。
func (s *Store) StreamDecisionItems(ctx context.Context, workflowID string) ([]model.ConfirmItem, error) {
	var rows []model.ConfirmItem
	err := s.db.WithContext(ctx).Model(&model.ConfirmItem{}).
		Where("workflow_id = ? AND kind = ?", workflowID, model.ConfirmKindDecision).
		Order("created_at ASC, id ASC").
		Limit(500).
		Find(&rows).Error
	if err != nil {
		return nil, err
	}
	return rows, nil
}

// StreamTaskUpdates 取 Agent 写下的任务进展；since 非零时只要这个时刻之后的（用来对齐「开启异步之后」的口径）。
func (s *Store) StreamTaskUpdates(ctx context.Context, workflowID string, since time.Time, r StreamRange) ([]WorkflowUpdate, error) {
	q := s.db.WithContext(ctx).
		Table("work_item_updates AS u").
		Select("u.id AS id, w.seq AS seq, w.title AS title, w.state AS state, "+
			"u.from_state AS from_state, u.to_state AS to_state, u.text AS text, "+
			"u.actor_kind AS actor_kind, u.created_at AS created_at").
		Joins("JOIN work_items AS w ON w.id = u.work_item_id").
		Where("w.workflow_id = ? AND u.actor_kind = ?", workflowID, model.WorkActorAgent)
	if !since.IsZero() {
		q = q.Where("u.created_at >= ?", since.In(time.Local))
	}
	q = r.search(q, "w.title", "u.text")
	var rows []WorkflowUpdate
	if err := r.apply(q, "u.created_at", "u.id").Scan(&rows).Error; err != nil {
		return nil, err
	}
	return rows, nil
}

// StreamEvents 取会话时间线上的系统事件（Agent 收尾、模式切换…）。
func (s *Store) StreamEvents(ctx context.Context, conversationID string, r StreamRange) ([]model.ConversationEvent, error) {
	if strings.TrimSpace(r.Query) != "" {
		return nil, nil
	}
	q := s.db.WithContext(ctx).Model(&model.ConversationEvent{}).Where("conversation_id = ?", conversationID)
	var rows []model.ConversationEvent
	if err := r.apply(q, "created_at", "id").Find(&rows).Error; err != nil {
		return nil, err
	}
	return rows, nil
}

// AddConversationEvent 写一条会话系统事件。Text 是给人看的一句话（不超过 400 字，超出会被截断）。
func (s *Store) AddConversationEvent(ctx context.Context, ev model.ConversationEvent) (*model.ConversationEvent, error) {
	ev.ConversationID = strings.TrimSpace(ev.ConversationID)
	if ev.ConversationID == "" || strings.TrimSpace(ev.Kind) == "" {
		return nil, NewInvalidInputError("conversation_id 与 kind 都不能为空")
	}
	if ev.CreatedAt.IsZero() {
		ev.CreatedAt = time.Now()
	}
	ev.Text = clipRunesTo(strings.TrimSpace(ev.Text), 400)
	if err := s.db.WithContext(ctx).Create(&ev).Error; err != nil {
		return nil, err
	}
	return &ev, nil
}

// CountUnreadAgentMessages 数一个工作流里 Agent 在 afterMs（Unix 毫秒）之后发的消息条数（系统行不计）：
// 阻塞式汇报、进度气泡、提问 / 计划卡，以及（includeUpdates 时）任务进展。
func (s *Store) CountUnreadAgentMessages(ctx context.Context, workflowID string, afterMs int64, updatesSince time.Time, includeUpdates bool) (int, error) {
	// 严格晚于 afterMs 这一毫秒：消息的发送毫秒 > afterMs ⇔ 时间 >= afterMs+1ms。
	return s.countAgentMessages(ctx, workflowID, msLocal(afterMs+1), time.Time{}, updatesSince, includeUpdates)
}

// CountAgentMessagesBetween 数一个工作流里 Agent 在 [from, to) 时间窗内发的消息条数（口径同 CountUnreadAgentMessages，含任务进展）；
// to 为零值表示不设上界。用来回答「某一次运行期间智能体有没有说过话」：窗口外（比如运行结束之后另一个会话发的）不算。
func (s *Store) CountAgentMessagesBetween(ctx context.Context, workflowID string, from, to time.Time) (int, error) {
	return s.countAgentMessages(ctx, workflowID, from.In(time.Local), to.In(time.Local), time.Time{}, true)
}

// countAgentMessages 是上面两个计数的共同实现：cut 是下界（含），until 非零时是上界（不含）。
func (s *Store) countAgentMessages(ctx context.Context, workflowID string, cut, until, updatesSince time.Time, includeUpdates bool) (int, error) {
	total := 0
	count := func(q *gorm.DB) error {
		var n int64
		if err := q.Count(&n).Error; err != nil {
			return err
		}
		total += int(n)
		return nil
	}
	// 上界（不含）：只在设了 until 时加一个条件；col 是各表的 created_at 列。
	bounded := func(q *gorm.DB, col string) *gorm.DB {
		if until.IsZero() {
			return q
		}
		return q.Where(col+" < ?", until)
	}
	db := s.db.WithContext(ctx)
	if err := count(bounded(db.Model(&model.FeedbackSession{}).Where("workflow_id = ? AND created_at >= ?", workflowID, cut), "created_at")); err != nil {
		return 0, err
	}
	if err := count(bounded(db.Model(&model.ConfirmItem{}).Where("workflow_id = ? AND kind IN (?, ?) AND created_at >= ?",
		workflowID, model.ConfirmKindAsk, model.ConfirmKindPlan, cut), "created_at")); err != nil {
		return 0, err
	}
	if includeUpdates {
		since := cut
		if !updatesSince.IsZero() && updatesSince.In(time.Local).After(since) {
			since = updatesSince.In(time.Local)
		}
		if err := count(bounded(db.Table("work_item_updates AS u").
			Joins("JOIN work_items AS w ON w.id = u.work_item_id").
			Where("w.workflow_id = ? AND u.actor_kind = ? AND u.created_at >= ?", workflowID, model.WorkActorAgent, since), "u.created_at")); err != nil {
			return 0, err
		}
	}
	return total, nil
}

// GetConversationRead 取一个读者在一个会话里的已读游标；还没有记录时返回零值（不报错）。
func (s *Store) GetConversationRead(ctx context.Context, conversationID, readerKind, readerRef string) (model.ConversationRead, error) {
	var row model.ConversationRead
	err := s.db.WithContext(ctx).
		Where("conversation_id = ? AND reader_kind = ? AND reader_ref = ?", conversationID, readerKind, readerRef).
		Limit(1).Find(&row).Error
	return row, err
}

// AdvanceConversationRead 把已读游标推到 (ms, seq)：两个分量各自只增不减（多设备、乱序请求取最大值）。
// 返回推进之后的游标。ms、seq 传 0 表示该分量不动。
func (s *Store) AdvanceConversationRead(ctx context.Context, conversationID, readerKind, readerRef string, ms, seq int64) (model.ConversationRead, error) {
	conversationID, readerKind, readerRef = strings.TrimSpace(conversationID), strings.TrimSpace(readerKind), strings.TrimSpace(readerRef)
	if conversationID == "" || readerKind == "" || readerRef == "" {
		return model.ConversationRead{}, NewInvalidInputError("conversation_id、reader_kind、reader_ref 都不能为空")
	}
	if ms < 0 || seq < 0 {
		return model.ConversationRead{}, NewInvalidInputError("游标不能为负数")
	}
	var out model.ConversationRead
	err := s.WithTx(ctx, func(tx *gorm.DB) error {
		row := model.ConversationRead{
			ConversationID: conversationID, ReaderKind: readerKind, ReaderRef: readerRef,
			ReadMs: ms, ReadSeq: seq, UpdatedAt: time.Now(),
		}
		if err := tx.Clauses(clause.OnConflict{
			Columns: []clause.Column{{Name: "conversation_id"}, {Name: "reader_kind"}, {Name: "reader_ref"}},
			DoUpdates: clause.Assignments(map[string]any{
				"read_ms":    gorm.Expr("MAX(read_ms, ?)", ms),
				"read_seq":   gorm.Expr("MAX(read_seq, ?)", seq),
				"updated_at": row.UpdatedAt,
			}),
		}).Create(&row).Error; err != nil {
			return err
		}
		return tx.Where("conversation_id = ? AND reader_kind = ? AND reader_ref = ?", conversationID, readerKind, readerRef).
			Limit(1).Find(&out).Error
	})
	return out, err
}

// ConversationReadEpochKey 是「已读纪元」的设置键：聊天功能上线（第一次用到）的时刻。
// 纪元之前发的消息一律算已读——否则上线当天每个工作流都会冒出几百条「未读」。
const ConversationReadEpochKey = "conversation_read_epoch_ms"

// ConversationReadEpoch 返回已读纪元（Unix 毫秒）；第一次调用时把当前时刻记下来。
func (s *Store) ConversationReadEpoch(ctx context.Context) (int64, error) {
	read := func() (int64, bool, error) {
		var rec model.SystemSetting
		err := s.db.WithContext(ctx).Where("key = ?", ConversationReadEpochKey).Limit(1).Find(&rec).Error
		if err != nil || rec.Key == "" {
			return 0, false, err
		}
		n, perr := strconv.ParseInt(strings.TrimSpace(rec.Value), 10, 64)
		return n, perr == nil, nil
	}
	if n, ok, err := read(); err != nil || ok {
		return n, err
	}
	now := time.Now()
	rec := model.SystemSetting{Key: ConversationReadEpochKey, Value: strconv.FormatInt(now.UnixMilli(), 10), UpdatedAt: now}
	if err := s.db.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(&rec).Error; err != nil {
		return 0, err
	}
	n, _, err := read()
	return n, err
}
