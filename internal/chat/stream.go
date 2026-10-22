package chat

import (
	"context"
	"sort"
	"strings"
	"time"

	"github.com/teacat99/RelayMesh/v2/internal/model"
	"github.com/teacat99/RelayMesh/v2/internal/store"
)

const (
	// DefaultLimit / MaxLimit 是一页消息的条数。
	DefaultLimit = 50
	MaxLimit     = 200
	// readerUser 是你（唯一的人类读者）在已读游标表里的 reader_ref。
	readerUser = "user"
)

// Service 把四个旧来源合成统一的消息流。
type Service struct {
	Store *store.Store
	// Now 可在测试里替换。
	Now func() time.Time
}

// NewService 创建服务。
func NewService(st *store.Store) *Service { return &Service{Store: st, Now: time.Now} }

// ListParams 是一次取消息的参数。Before / After / Around 互斥，优先级 Around > After > Before。
type ListParams struct {
	Limit  int
	Before string // 游标：取比它更早的（不含它）
	After  string // 游标：取比它更新的（不含它）
	Around string // 游标：取以它为中心的一窗（含它）
	Query  string // 子串搜索
	Kinds  []string
}

// Page 是一页消息（按发送时间升序）。
type Page struct {
	ConversationID string    `json:"conversation_id"`
	Items          []Message `json:"items"`
	// BeforeCursor 是第一条的游标：往前翻页时作为 before 传回；AfterCursor 是最后一条的游标。
	BeforeCursor  string `json:"before_cursor,omitempty"`
	AfterCursor   string `json:"after_cursor,omitempty"`
	HasMoreBefore bool   `json:"has_more_before"`
	HasMoreAfter  bool   `json:"has_more_after"`
	// Unread 是你的已读游标之后 Agent 发的消息数（系统行不计）；ReadMs 是游标本身（Unix 毫秒）。
	Unread int   `json:"unread"`
	ReadMs int64 `json:"read_ms"`
	// Agent 是这个会话里 Agent 一方的显示信息。
	Agent Sender `json:"agent"`
	// 群会话专有：群信息（含成员）与你在群里读到的序号。
	Group   *GroupInfo `json:"group,omitempty"`
	ReadSeq int64      `json:"read_seq,omitempty"`
}

// env 是一次取数共用的上下文：合成器、已读游标、任务进展的起算时间。
type env struct {
	b            Builder
	readMs       int64
	updatesSince time.Time
	withUpdates  bool
}

// load 取一个工作流的合成上下文。
func (s *Service) load(ctx context.Context, workflowID string) (env, error) {
	now := s.Now()
	name := ""
	if list, err := s.Store.ListAgentSessions(ctx, store.AgentSessionFilter{WorkflowID: workflowID, Limit: 1}, now); err == nil && len(list) > 0 {
		name = list[0].IdentityLabel
	}
	readMs, err := s.readMs(ctx, workflowID)
	if err != nil {
		return env{}, err
	}
	e := env{b: NewBuilder(workflowID, name, readMs), readMs: readMs}
	// 任务进展只显示「开启异步之后」的变化（与问答视图口径一致）：从没开启过的工作流不带进展。
	if info, err := s.Store.WorkflowModeInfo(ctx, workflowID, now); err == nil {
		if info.Row.EnabledAt != nil {
			e.updatesSince, e.withUpdates = *info.Row.EnabledAt, true
		} else if info.Row.EverEnabled || info.Row.AsyncEnabled {
			e.withUpdates = true
		}
	}
	return e, nil
}

// readMs 是你的有效已读游标：游标本身与「已读纪元」取较大者。
func (s *Service) readMs(ctx context.Context, workflowID string) (int64, error) {
	epoch, err := s.Store.ConversationReadEpoch(ctx)
	if err != nil {
		return 0, err
	}
	row, err := s.Store.GetConversationRead(ctx, DirectID(workflowID), model.ConversationReaderHuman, readerUser)
	if err != nil {
		return 0, err
	}
	if row.ReadMs > epoch {
		return row.ReadMs, nil
	}
	return epoch, nil
}

// kindSet 把 kinds 参数变成集合；空表示不过滤。
func kindSet(kinds []string) map[string]bool {
	if len(kinds) == 0 {
		return nil
	}
	set := map[string]bool{}
	for _, k := range kinds {
		if k = strings.TrimSpace(k); k != "" {
			set[k] = true
		}
	}
	if len(set) == 0 {
		return nil
	}
	return set
}

func wants(set map[string]bool, produced ...string) bool {
	if set == nil {
		return true
	}
	for _, k := range produced {
		if set[k] {
			return true
		}
	}
	return false
}

// gather 从各来源取候选消息（每个来源最多 limit 条，已按方向与时间边界裁过）。
func (s *Service) gather(ctx context.Context, workflowID string, e env, rng store.StreamRange, kinds map[string]bool) ([]Message, error) {
	b := e.b
	var out []Message
	st := s.Store

	if wants(kinds, KindText, KindBlock) {
		rows, err := st.StreamReports(ctx, workflowID, rng)
		if err != nil {
			return nil, err
		}
		for _, r := range rows {
			out = append(out, b.Report(r))
		}
		replies, err := st.StreamReplies(ctx, workflowID, rng)
		if err != nil {
			return nil, err
		}
		ids := make([]string, 0, len(replies))
		for _, r := range replies {
			ids = append(ids, r.ID)
		}
		merged, err := st.StreamMergedInbox(ctx, workflowID, ids)
		if err != nil {
			return nil, err
		}
		for _, r := range replies {
			if m, ok := b.ReplyAfterMerge(r, merged[r.ID]); ok {
				out = append(out, m)
			}
		}
		inbox, err := st.StreamInbox(ctx, workflowID, rng)
		if err != nil {
			return nil, err
		}
		for _, q := range inbox {
			out = append(out, b.InboxItem(q))
		}
	}
	if wants(kinds, KindProgress) {
		rows, err := st.StreamNotices(ctx, workflowID, rng)
		if err != nil {
			return nil, err
		}
		for _, r := range rows {
			out = append(out, b.Notice(r))
		}
		if e.withUpdates {
			ups, err := st.StreamTaskUpdates(ctx, workflowID, e.updatesSince, rng)
			if err != nil {
				return nil, err
			}
			for _, u := range ups {
				out = append(out, b.TaskUpdate(u))
			}
		}
	}
	if wants(kinds, KindSystem) {
		ends, err := st.StreamEndings(ctx, workflowID, rng)
		if err != nil {
			return nil, err
		}
		for _, r := range ends {
			out = append(out, b.Ending(r))
		}
		revs, err := st.StreamRetractions(ctx, workflowID, rng)
		if err != nil {
			return nil, err
		}
		for _, q := range revs {
			out = append(out, b.Retraction(q))
		}
		evs, err := st.StreamEvents(ctx, e.b.ConversationID, rng)
		if err != nil {
			return nil, err
		}
		for _, ev := range evs {
			out = append(out, b.Event(ev))
		}
	}
	if wants(kinds, KindAsk, KindPlan) {
		cards, err := st.StreamCards(ctx, workflowID, rng)
		if err != nil {
			return nil, err
		}
		views, err := st.ConfirmViews(ctx, cards)
		if err != nil {
			return nil, err
		}
		for _, v := range views {
			out = append(out, b.Card(v))
		}
	}
	if wants(kinds, KindText) {
		ans, err := s.answers(ctx, workflowID, b, rng)
		if err != nil {
			return nil, err
		}
		out = append(out, ans...)
	}
	if wants(kinds, KindDecision) {
		dec, err := s.decisions(ctx, workflowID, b, rng.Query)
		if err != nil {
			return nil, err
		}
		out = append(out, dec...)
	}
	return out, nil
}

// answers 合成你对卡的答复，并把对应的收件箱投递行挂上去（给出四态）。
func (s *Service) answers(ctx context.Context, workflowID string, b Builder, rng store.StreamRange) ([]Message, error) {
	items, err := s.Store.StreamAnswers(ctx, workflowID, rng)
	if err != nil || len(items) == 0 {
		return nil, err
	}
	views, err := s.Store.ConfirmViews(ctx, items)
	if err != nil {
		return nil, err
	}
	from, to := AnswerTime(items[0]), AnswerTime(items[0])
	for _, it := range items {
		if t := AnswerTime(it); t.Before(from) {
			from = t
		} else if t.After(to) {
			to = t
		}
	}
	rows, err := s.Store.StreamAnswerDeliveries(ctx, workflowID, from, to)
	if err != nil {
		return nil, err
	}
	byRef := map[string]*model.QueuedFeedback{}
	for i := range rows {
		for _, ref := range strings.Split(rows[i].OriginRef, ",") {
			if ref = strings.TrimSpace(ref); strings.HasPrefix(ref, "c:") {
				byRef[ref] = &rows[i]
			}
		}
	}
	out := make([]Message, 0, len(items))
	for i, it := range items {
		out = append(out, b.Answer(it, views[i], byRef["c:"+views[i].Ref]))
	}
	return out, nil
}

// decisions 把文档决策项按文档汇总成卡。决策项总量有限（每个工作流最多 500），不分页，交给边界裁剪。
func (s *Service) decisions(ctx context.Context, workflowID string, b Builder, query string) ([]Message, error) {
	items, err := s.Store.StreamDecisionItems(ctx, workflowID)
	if err != nil || len(items) == 0 {
		return nil, err
	}
	views, err := s.Store.ConfirmViews(ctx, items)
	if err != nil {
		return nil, err
	}
	groups := map[uint]*DecisionGroup{}
	first := map[uint]time.Time{}
	var order []uint
	for i, v := range views {
		g := groups[v.DocID]
		if g == nil {
			g = &DecisionGroup{DocID: v.DocID, DocPath: v.DocPath, Title: v.DocTitle}
			if g.Title == "" {
				g.Title = v.DocPath
			}
			groups[v.DocID] = g
			first[v.DocID] = items[i].CreatedAt
			order = append(order, v.DocID)
		}
		g.Items = append(g.Items, v)
		if v.Status == model.ConfirmStatusPending {
			g.Pending++
		}
	}
	q := strings.ToLower(strings.TrimSpace(query))
	out := make([]Message, 0, len(order))
	for _, id := range order {
		g := groups[id]
		if q != "" && !groupMatches(g, q) {
			continue
		}
		out = append(out, b.Decision(*g, first[id]))
	}
	return out, nil
}

func groupMatches(g *DecisionGroup, q string) bool {
	if strings.Contains(strings.ToLower(g.Title), q) {
		return true
	}
	for _, it := range g.Items {
		if strings.Contains(strings.ToLower(it.Title+" "+it.Body), q) {
			return true
		}
	}
	return false
}

// pick 从无序的候选里按方向与边界取出一页（升序）。more 表示边界另一侧还有没取完的。
// Older：键严格小于 bound（inclusive 时含 bound），取最靠近 bound 的 n 条；Newer：键严格大于 bound，取最靠近的 n 条。
func pick(cands []Message, dir store.StreamDir, bound *Key, inclusive bool, n int) (page []Message, more bool) {
	kept := cands[:0:0]
	for _, m := range cands {
		if bound != nil {
			k := m.Key()
			switch {
			case dir == store.StreamOlder && !(k.Less(*bound) || (inclusive && k.Equal(*bound))):
				continue
			case dir == store.StreamNewer && !bound.Less(k):
				continue
			}
		}
		kept = append(kept, m)
	}
	sort.SliceStable(kept, func(i, j int) bool { return kept[i].Key().Less(kept[j].Key()) })
	if len(kept) <= n {
		return kept, false
	}
	if dir == store.StreamOlder {
		return kept[len(kept)-n:], true
	}
	return kept[:n], true
}

func clampLimit(n int) int {
	switch {
	case n <= 0:
		return DefaultLimit
	case n > MaxLimit:
		return MaxLimit
	}
	return n
}

// window 取一个方向上的一页。
func (s *Service) window(ctx context.Context, workflowID string, e env, p ListParams, dir store.StreamDir, bound *Key, inclusive bool, n int) ([]Message, bool, error) {
	rng := store.StreamRange{Dir: dir, Limit: n + 1, Query: p.Query}
	if bound != nil {
		rng.Ms = bound.Ms
	}
	kinds := kindSet(p.Kinds)
	cands, err := s.gather(ctx, workflowID, e, rng, kinds)
	if err != nil {
		return nil, false, err
	}
	if kinds != nil {
		filtered := cands[:0:0]
		for _, m := range cands {
			if kinds[m.Kind] {
				filtered = append(filtered, m)
			}
		}
		cands = filtered
	}
	page, more := pick(cands, dir, bound, inclusive, n)
	return page, more, nil
}

// List 取一个工作流 1:1 会话的一页消息。
func (s *Service) List(ctx context.Context, workflowID string, p ListParams) (*Page, error) {
	workflowID = strings.TrimSpace(workflowID)
	if workflowID == "" {
		return nil, store.NewInvalidInputError("workflow_id 不能为空")
	}
	n := clampLimit(p.Limit)
	e, err := s.load(ctx, workflowID)
	if err != nil {
		return nil, err
	}
	parse := func(raw string) (*Key, error) {
		k, err := ParseCursor(raw)
		if err != nil {
			return nil, store.NewInvalidInputError(err.Error())
		}
		return &k, nil
	}

	page := &Page{ConversationID: e.b.ConversationID, Agent: e.b.Agent, ReadMs: e.readMs}
	switch {
	case strings.TrimSpace(p.Around) != "":
		center, err := parse(p.Around)
		if err != nil {
			return nil, err
		}
		olderN := n/2 + 1 // 含中心
		older, moreOlder, err := s.window(ctx, workflowID, e, p, store.StreamOlder, center, true, olderN)
		if err != nil {
			return nil, err
		}
		newer, moreNewer, err := s.window(ctx, workflowID, e, p, store.StreamNewer, center, false, n-olderN)
		if err != nil {
			return nil, err
		}
		page.Items = append(older, newer...)
		page.HasMoreBefore, page.HasMoreAfter = moreOlder, moreNewer
	case strings.TrimSpace(p.After) != "":
		after, err := parse(p.After)
		if err != nil {
			return nil, err
		}
		items, more, err := s.window(ctx, workflowID, e, p, store.StreamNewer, after, false, n)
		if err != nil {
			return nil, err
		}
		page.Items, page.HasMoreAfter, page.HasMoreBefore = items, more, true
	default:
		var before *Key
		if strings.TrimSpace(p.Before) != "" {
			if before, err = parse(p.Before); err != nil {
				return nil, err
			}
		}
		items, more, err := s.window(ctx, workflowID, e, p, store.StreamOlder, before, false, n)
		if err != nil {
			return nil, err
		}
		page.Items, page.HasMoreBefore = items, more
		page.HasMoreAfter = before != nil
	}
	if len(page.Items) > 0 {
		page.BeforeCursor = page.Items[0].Cursor
		page.AfterCursor = page.Items[len(page.Items)-1].Cursor
	}
	if page.Items == nil {
		page.Items = []Message{}
	}
	page.Unread, err = s.Store.CountUnreadAgentMessages(ctx, workflowID, e.readMs, e.updatesSince, e.withUpdates)
	if err != nil {
		return nil, err
	}
	return page, nil
}

// ReadState 是推进已读游标之后的结果。
type ReadState struct {
	ReadMs int64 `json:"read_ms"`
	Unread int   `json:"unread"`
}

// MarkRead 把你的已读游标推到 cursor 对应的消息（空表示现在）。游标只增不减，多设备取最大值。
func (s *Service) MarkRead(ctx context.Context, workflowID, cursor string) (ReadState, error) {
	workflowID = strings.TrimSpace(workflowID)
	if workflowID == "" {
		return ReadState{}, store.NewInvalidInputError("workflow_id 不能为空")
	}
	ms := s.Now().UnixMilli()
	if strings.TrimSpace(cursor) != "" {
		k, err := ParseCursor(cursor)
		if err != nil {
			return ReadState{}, store.NewInvalidInputError(err.Error())
		}
		ms = k.Ms
	}
	if _, err := s.Store.AdvanceConversationRead(ctx, DirectID(workflowID), model.ConversationReaderHuman, readerUser, ms, 0); err != nil {
		return ReadState{}, err
	}
	e, err := s.load(ctx, workflowID)
	if err != nil {
		return ReadState{}, err
	}
	n, err := s.Store.CountUnreadAgentMessages(ctx, workflowID, e.readMs, e.updatesSince, e.withUpdates)
	return ReadState{ReadMs: e.readMs, Unread: n}, err
}

// Summary 是会话列表里一个 1:1 会话的一行：最后一条消息与未读数。
type Summary struct {
	ID         string   `json:"id"`
	WorkflowID string   `json:"workflow_id"`
	Unread     int      `json:"unread"`
	Last       *Message `json:"last,omitempty"`
	Preview    string   `json:"preview,omitempty"`
}

// Summaries 取一批工作流的会话摘要（侧栏的最后一条预览与未读徽标）。
func (s *Service) Summaries(ctx context.Context, workflowIDs []string) ([]Summary, error) {
	out := make([]Summary, 0, len(workflowIDs))
	for _, wf := range workflowIDs {
		wf = strings.TrimSpace(wf)
		if wf == "" {
			continue
		}
		pg, err := s.List(ctx, wf, ListParams{Limit: 1})
		if err != nil {
			return nil, err
		}
		sum := Summary{ID: DirectID(wf), WorkflowID: wf, Unread: pg.Unread}
		if len(pg.Items) > 0 {
			last := pg.Items[len(pg.Items)-1]
			sum.Last = &last
			sum.Preview = Preview(last, 80)
		}
		out = append(out, sum)
	}
	return out, nil
}

// Delivered 把刚发出去的一次交付渲染成消息：Session 不为空是成了某一轮的回复，否则 Queued 是收件箱里排队的那条。
func (s *Service) Delivered(ctx context.Context, workflowID string, sess *model.FeedbackSession, queued *model.QueuedFeedback) (Message, error) {
	e, err := s.load(ctx, workflowID)
	if err != nil {
		return Message{}, err
	}
	if sess != nil {
		merged, err := s.Store.StreamMergedInbox(ctx, workflowID, []string{sess.ID})
		if err != nil {
			return Message{}, err
		}
		if m, ok := e.b.ReplyAfterMerge(*sess, merged[sess.ID]); ok {
			return m, nil
		}
		return e.b.Reply(*sess), nil
	}
	if queued == nil {
		return Message{}, store.NewInvalidInputError("没有可渲染的交付")
	}
	q := *queued
	q.State = q.DeriveState()
	return e.b.InboxItem(q), nil
}

// ListSince 取发送毫秒严格晚于 afterMs 的消息（升序，最多 n 条）。给 Agent 的读游标用：它没有消息游标，只有一个毫秒位置。
func (s *Service) ListSince(ctx context.Context, workflowID string, afterMs int64, n int) (items []Message, more bool, err error) {
	workflowID = strings.TrimSpace(workflowID)
	if workflowID == "" {
		return nil, false, store.NewInvalidInputError("workflow_id 不能为空")
	}
	e, err := s.load(ctx, workflowID)
	if err != nil {
		return nil, false, err
	}
	// 键 (afterMs, 99, 最大) 排在这一毫秒的所有消息之后：「严格大于」即「晚于这一毫秒」。
	bound := Key{Ms: afterMs, Rank: 99, ID: "\xff"}
	return s.window(ctx, workflowID, e, ListParams{}, store.StreamNewer, &bound, false, clampLimit(n))
}

// AgentReader 是 Agent 一方在已读游标表里的 reader_ref。
func AgentReader(workflowID string) string { return workflowID }
