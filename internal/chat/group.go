package chat

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/teacat99/RelayMesh/v2/internal/model"
	"github.com/teacat99/RelayMesh/v2/internal/store"
)

// 群会话（阶段 2b）：消息是原生的 chat_messages，排序键 = （发送毫秒，0，序号），与 1:1 共用游标与分页接口。

// IsGroupID 报告会话 id 是不是群。
func IsGroupID(id string) bool { return strings.HasPrefix(strings.TrimSpace(id), GroupPrefix) }

// GroupMember 是群成员的展示信息。
type GroupMember struct {
	Kind       string     `json:"kind"` // human | workflow
	Ref        string     `json:"ref"`  // user | 工作流编号
	Name       string     `json:"name"`
	Role       string     `json:"role"`
	WorkflowID string     `json:"workflow_id,omitempty"`
	PushPolicy string     `json:"push_policy"`
	MutedUntil *time.Time `json:"muted_until,omitempty"`
	JoinedAt   time.Time  `json:"joined_at"`
	Left       bool       `json:"left,omitempty"`
	// 下面几项只有 Agent 成员有：来自 Agent 状态机的展示状态。
	State      string `json:"state,omitempty"`
	Sub        string `json:"sub,omitempty"`
	WaitingYou bool   `json:"waiting_you,omitempty"`
	ProjectDir string `json:"project_directory,omitempty"`
	HostName   string `json:"host_name,omitempty"`
	ReadSeq    int64  `json:"read_seq"`
}

// GroupInfo 是群的基本信息与成员。
type GroupInfo struct {
	ID           string        `json:"id"`
	Title        string        `json:"title"`
	ProjectID    string        `json:"project_id,omitempty"`
	CreatedAt    time.Time     `json:"created_at"`
	Archived     bool          `json:"archived"`
	Members      []GroupMember `json:"members"`
	CrossProject bool          `json:"cross_project"`
	Projects     []string      `json:"projects,omitempty"`
}

// MentionRef 是消息里被点名的对象。
type MentionRef struct {
	Ref  string `json:"ref"` // 工作流编号 | all | human
	Name string `json:"name"`
}

// ReadInfo 是一条群消息的已读情况（G-3）：N/M 与具体名单。
type ReadInfo struct {
	Read    int      `json:"read"`
	Total   int      `json:"total"`
	Readers []string `json:"readers,omitempty"`
	Unread  []string `json:"unread,omitempty"`
}

// Delivery 是被点名的成员各自的投递状态（已送达 / 已读 / 已处理）。
type Delivery struct {
	Ref    string `json:"ref"`
	Name   string `json:"name"`
	Status string `json:"status"`
}

// groupEnv 是一次取数共用的群上下文。
type groupEnv struct {
	conv     *model.Conversation
	members  []model.ConversationMember
	labels   map[string]string // 成员引用 → 显示名
	views    map[string]store.AgentSessionView
	reads    map[string]int64 // "kind:ref" → 已读序号
	humanSeq int64
}

func (e groupEnv) label(ref string) string {
	if ref == store.GroupHumanRef || ref == model.ChatMentionHuman {
		return HumanName
	}
	if l := e.labels[ref]; l != "" {
		return l
	}
	return ref
}

func (s *Service) loadGroup(ctx context.Context, groupID string) (*groupEnv, error) {
	conv, err := s.Store.GetGroup(ctx, groupID)
	if err != nil {
		return nil, err
	}
	members, err := s.Store.ListGroupMembers(ctx, groupID, true)
	if err != nil {
		return nil, err
	}
	reads, err := s.Store.GroupReadSeqs(ctx, groupID)
	if err != nil {
		return nil, err
	}
	e := &groupEnv{conv: conv, members: members, labels: map[string]string{}, views: map[string]store.AgentSessionView{}, reads: reads}
	now := s.Now()
	for _, m := range members {
		if m.MemberKind != model.MemberKindWorkflow {
			continue
		}
		if list, err := s.Store.ListAgentSessions(ctx, store.AgentSessionFilter{WorkflowID: m.MemberRef, Limit: 1}, now); err == nil && len(list) > 0 {
			e.views[m.MemberRef] = list[0]
			e.labels[m.MemberRef] = list[0].IdentityLabel
		}
	}
	e.humanSeq = reads[model.ConversationReaderHuman+":"+store.GroupHumanRef]
	disambiguateLabels(e.labels)
	return e, nil
}

// disambiguateLabels 让群里的显示名互不相同：同一个项目里的几个 Agent 默认标签一样，
// 而 @ 与「谁发的」都靠名字区分。重名的在后面补上工作流编号的末 4 位；也避开「你」「所有人」这两个保留名。
func disambiguateLabels(labels map[string]string) {
	count := map[string]int{HumanName: 1, "所有人": 1}
	for _, l := range labels {
		count[l]++
	}
	for ref, l := range labels {
		if count[l] > 1 {
			labels[ref] = l + " · " + tailRunes(ref, 4)
		}
	}
}

func tailRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[len(r)-n:])
}

// info 把群与成员翻成展示信息。
func (e *groupEnv) info() *GroupInfo {
	g := &GroupInfo{ID: e.conv.ID, Title: e.conv.Title, ProjectID: e.conv.ProjectID, CreatedAt: e.conv.CreatedAt, Archived: e.conv.ArchivedAt != nil, Members: []GroupMember{}}
	projects := map[string]bool{}
	var projectList []string
	for _, m := range e.members {
		gm := GroupMember{
			Kind: m.MemberKind, Ref: m.MemberRef, Name: e.label(m.MemberRef), Role: m.Role, PushPolicy: m.PushPolicy,
			MutedUntil: m.MutedUntil, JoinedAt: m.JoinedAt, Left: m.LeftAt != nil,
			ReadSeq: e.reads[readerKindOfMember(m.MemberKind)+":"+m.MemberRef],
		}
		if m.MemberKind == model.MemberKindWorkflow {
			gm.WorkflowID = m.MemberRef
			if v, ok := e.views[m.MemberRef]; ok {
				gm.State, gm.Sub, gm.WaitingYou, gm.ProjectDir, gm.HostName = v.State, v.Sub, v.WaitingYou, v.ProjectDir, v.HostName
				if m.LeftAt == nil {
					if name := lastSegment(v.ProjectDir); name != "" && !projects[name] {
						projects[name] = true
						projectList = append(projectList, name)
					}
				}
			}
		}
		g.Members = append(g.Members, gm)
	}
	g.Projects = projectList
	g.CrossProject = len(projectList) > 1
	return g
}

func readerKindOfMember(memberKind string) string {
	if memberKind == model.MemberKindHuman {
		return model.ConversationReaderHuman
	}
	return model.ConversationReaderWorkflow
}

func lastSegment(dir string) string {
	d := strings.TrimRight(strings.TrimSpace(dir), `/\`)
	if i := strings.LastIndexAny(d, `/\`); i >= 0 {
		d = d[i+1:]
	}
	return d
}

// systemText 把系统行的事件码翻成一句话。
func (e *groupEnv) systemText(event, ref string) string {
	who := e.label(ref)
	switch event {
	case model.GroupEventCreated:
		return "群聊已创建"
	case model.GroupEventJoined:
		return who + " 加入了群聊"
	case model.GroupEventLeft:
		return who + " 离开了群聊"
	case model.GroupEventArchived:
		return "群已归档，只读保留"
	case model.GroupEventHopLimit:
		return who + " 的消息因回复链过长没有发出（避免 Agent 之间互相 @ 空转）"
	case model.GroupEventCooling:
		return "群里 Agent 之间的消息太密，已暂停互相投递；你说一句话即可恢复"
	}
	return event
}

// message 把一行群消息翻成统一消息。states 是被点名成员的提醒行状态（可为空）。
func (e *groupEnv) message(m model.ChatMessage, states map[string]string) Message {
	out := Message{
		ID: "m:" + strconv.FormatUint(uint64(m.ID), 10), ConversationID: e.conv.ID, SentAt: m.CreatedAt, ReplyTo: m.ReplyTo, Hops: m.Hop,
		Source: Source{Type: "chat_message", ID: strconv.FormatUint(uint64(m.ID), 10)},
	}
	switch m.SenderKind {
	case model.ChatSenderHuman:
		out.Sender = human()
	case model.ChatSenderWorkflow:
		out.Sender = Sender{Kind: SenderAgent, ID: m.SenderRef, Name: e.label(m.SenderRef), WorkflowID: m.SenderRef}
	default:
		out.Sender = system()
	}
	if m.Kind == model.ChatKindSystem {
		out.Kind = KindSystem
		out.Text = e.systemText(m.Body, m.SenderRef)
		out.Status = StatusSent
	} else {
		out.Kind = KindText
		out.Text = m.Body
		for _, ref := range m.Mentions {
			name := e.label(ref)
			if ref == model.ChatMentionAll {
				name = "所有人"
			}
			out.Mentions = append(out.Mentions, MentionRef{Ref: ref, Name: name})
		}
		out.Reads = e.readInfo(m)
		out.Status = StatusSent
		if out.Reads != nil && out.Reads.Total > 0 && out.Reads.Read == out.Reads.Total {
			out.Status = StatusRead
		}
		for _, ref := range sortedKeys(states) {
			out.Deliveries = append(out.Deliveries, Delivery{Ref: ref, Name: e.label(ref), Status: deliveryStatus(states[ref])})
		}
	}
	out.Meta = map[string]any{"seq": m.Seq}
	out.key = Key{Ms: m.CreatedAt.UnixMilli(), Rank: 0, ID: numKey(uint(m.Seq))}
	out.Cursor = out.key.Encode()
	return out
}

func deliveryStatus(state string) string {
	switch state {
	case model.InboxStateDelivered:
		return StatusRead
	case model.InboxStateAcked:
		return StatusHandled
	case model.InboxStateExpired:
		return StatusExpired
	case model.InboxStateRevoked, model.InboxStateRetracted:
		return StatusRetracted
	}
	return StatusDelivered
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	for i := 1; i < len(keys); i++ {
		for j := i; j > 0 && keys[j] < keys[j-1]; j-- {
			keys[j], keys[j-1] = keys[j-1], keys[j]
		}
	}
	return keys
}

// readInfo 算一条消息的已读：参与者是发送时在群里的所有成员（除发送者自己），读过 = 已读序号不小于这条的序号。
func (e *groupEnv) readInfo(m model.ChatMessage) *ReadInfo {
	info := &ReadInfo{}
	for _, mem := range e.members {
		if mem.MemberRef == m.SenderRef && readerKindOfMember(mem.MemberKind) == senderReader(m.SenderKind) {
			continue
		}
		if mem.JoinedAt.After(m.CreatedAt) || (mem.LeftAt != nil && mem.LeftAt.Before(m.CreatedAt)) {
			continue
		}
		info.Total++
		name := e.label(mem.MemberRef)
		if e.reads[readerKindOfMember(mem.MemberKind)+":"+mem.MemberRef] >= m.Seq {
			info.Read++
			info.Readers = append(info.Readers, name)
		} else {
			info.Unread = append(info.Unread, name)
		}
	}
	return info
}

func senderReader(senderKind string) string {
	if senderKind == model.ChatSenderHuman {
		return model.ConversationReaderHuman
	}
	return model.ConversationReaderWorkflow
}

// gather 取一个方向上的一批原始行并翻成消息。
func (s *Service) gatherGroup(ctx context.Context, e *groupEnv, rng store.StreamRange, vis *store.GroupVisibility, boundSeq int64, inclusive bool) ([]Message, error) {
	rows, err := s.Store.StreamGroupMessages(ctx, e.conv.ID, rng, vis, boundSeq, inclusive)
	if err != nil {
		return nil, err
	}
	ids := make([]uint, 0, len(rows))
	for _, r := range rows {
		if len(r.Mentions) > 0 || r.SenderKind == model.ChatSenderHuman {
			ids = append(ids, r.ID)
		}
	}
	states, err := s.Store.GroupMentionStates(ctx, e.conv.ID, ids)
	if err != nil {
		return nil, err
	}
	out := make([]Message, 0, len(rows))
	for _, r := range rows {
		out = append(out, e.message(r, states[r.ID]))
	}
	return out, nil
}

func (s *Service) groupWindow(ctx context.Context, e *groupEnv, p ListParams, dir store.StreamDir, bound *Key, inclusive bool, n int) ([]Message, bool, error) {
	rng := store.StreamRange{Dir: dir, Limit: n + 1, Query: p.Query}
	var boundSeq int64
	if bound != nil {
		seq, err := strconv.ParseInt(strings.TrimLeft(bound.ID, "0"), 10, 64)
		if err != nil {
			return nil, false, store.NewInvalidInputError("游标不是群消息的游标")
		}
		boundSeq = seq
	}
	cands, err := s.gatherGroup(ctx, e, rng, nil, boundSeq, inclusive)
	if err != nil {
		return nil, false, err
	}
	page, more := pick(cands, dir, bound, inclusive, n)
	return page, more, nil
}

// ListGroup 取一个群的一页消息（语义与 List 相同）。
func (s *Service) ListGroup(ctx context.Context, groupID string, p ListParams) (*Page, error) {
	groupID = strings.TrimSpace(groupID)
	n := clampLimit(p.Limit)
	e, err := s.loadGroup(ctx, groupID)
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
	page := &Page{ConversationID: groupID, Group: e.info(), ReadSeq: e.humanSeq}
	switch {
	case strings.TrimSpace(p.Around) != "":
		center, err := parse(p.Around)
		if err != nil {
			return nil, err
		}
		olderN := n/2 + 1
		older, moreOlder, err := s.groupWindow(ctx, e, p, store.StreamOlder, center, true, olderN)
		if err != nil {
			return nil, err
		}
		newer, moreNewer, err := s.groupWindow(ctx, e, p, store.StreamNewer, center, false, n-olderN)
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
		items, more, err := s.groupWindow(ctx, e, p, store.StreamNewer, after, false, n)
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
		items, more, err := s.groupWindow(ctx, e, p, store.StreamOlder, before, false, n)
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
	page.Unread, _, err = s.Store.GroupUnread(ctx, groupID, model.ConversationReaderHuman, store.GroupHumanRef, time.Time{})
	if err != nil {
		return nil, err
	}
	return page, nil
}

// GroupInfoOf 取群信息与成员。
func (s *Service) GroupInfoOf(ctx context.Context, groupID string) (*GroupInfo, error) {
	e, err := s.loadGroup(ctx, strings.TrimSpace(groupID))
	if err != nil {
		return nil, err
	}
	return e.info(), nil
}

// MarkGroupRead 把你在群里的已读游标推到 cursor 对应的消息（空表示现在最新的一条）。
func (s *Service) MarkGroupRead(ctx context.Context, groupID, cursor string) (ReadState, error) {
	groupID = strings.TrimSpace(groupID)
	seq := int64(0)
	if strings.TrimSpace(cursor) != "" {
		k, err := ParseCursor(cursor)
		if err != nil {
			return ReadState{}, store.NewInvalidInputError(err.Error())
		}
		n, err := strconv.ParseInt(strings.TrimLeft(k.ID, "0"), 10, 64)
		if err != nil {
			return ReadState{}, store.NewInvalidInputError("游标不是群消息的游标")
		}
		seq = n
	} else {
		var err error
		if seq, err = s.Store.GroupLastSeq(ctx, groupID); err != nil {
			return ReadState{}, err
		}
	}
	if _, err := s.Store.GetGroup(ctx, groupID); err != nil {
		return ReadState{}, err
	}
	if err := s.Store.AdvanceGroupRead(ctx, groupID, model.ConversationReaderHuman, store.GroupHumanRef, seq); err != nil {
		return ReadState{}, err
	}
	n, _, err := s.Store.GroupUnread(ctx, groupID, model.ConversationReaderHuman, store.GroupHumanRef, time.Time{})
	return ReadState{ReadMs: s.Now().UnixMilli(), Unread: n}, err
}

// GroupSummary 是会话列表里一个群的一行。
type GroupSummary struct {
	ID           string    `json:"id"`
	Title        string    `json:"title"`
	Archived     bool      `json:"archived"`
	MemberCount  int       `json:"member_count"`
	Unread       int       `json:"unread"`
	Mentioned    int       `json:"mentioned"` // 其中点名了你（或 @all）的条数
	Last         *Message  `json:"last,omitempty"`
	Preview      string    `json:"preview,omitempty"`
	CrossProject bool      `json:"cross_project"`
	UpdatedAt    time.Time `json:"updated_at"`
}

// GroupSummaries 列出群及未读（最近有动静的在前）。
func (s *Service) GroupSummaries(ctx context.Context, includeArchived bool) ([]GroupSummary, error) {
	groups, err := s.Store.ListGroups(ctx, includeArchived)
	if err != nil {
		return nil, err
	}
	out := make([]GroupSummary, 0, len(groups))
	for _, g := range groups {
		e, err := s.loadGroup(ctx, g.ID)
		if err != nil {
			return nil, err
		}
		info := e.info()
		active := 0
		for _, m := range info.Members {
			if !m.Left {
				active++
			}
		}
		sum := GroupSummary{ID: g.ID, Title: g.Title, Archived: g.ArchivedAt != nil, MemberCount: active, CrossProject: info.CrossProject, UpdatedAt: g.UpdatedAt}
		// 点名数按「你」的已读游标算：unread 里点名了人类读者的条数
		sum.Unread, sum.Mentioned, err = s.Store.GroupUnread(ctx, g.ID, model.ConversationReaderHuman, store.GroupHumanRef, time.Time{})
		if err != nil {
			return nil, err
		}
		if last, err := s.Store.GroupLastMessage(ctx, g.ID); err == nil && last != nil {
			m := e.message(*last, nil)
			sum.Last = &m
			sum.Preview = Preview(m, 80)
		}
		out = append(out, sum)
	}
	return out, nil
}

// ---- 发送与 Agent 读取 ----

// PostHuman 以你的身份发一条群消息。
func (s *Service) PostHuman(ctx context.Context, groupID, text string, mentions []string, replyTo string) (Message, *store.PostGroupResult, error) {
	res, err := s.Store.PostGroupMessage(ctx, store.PostGroupInput{
		ConversationID: strings.TrimSpace(groupID), SenderKind: model.ChatSenderHuman, SenderRef: store.GroupHumanRef,
		Body: text, Mentions: mentions, ReplyTo: replyTo,
	})
	if err != nil {
		return Message{}, nil, err
	}
	msg, err := s.renderPosted(ctx, res)
	return msg, res, err
}

// PostAgent 以某个工作流的身份发一条群消息。
func (s *Service) PostAgent(ctx context.Context, groupID, workflowID, text string, mentions []string, replyTo, idemKey string) (Message, *store.PostGroupResult, error) {
	label, project := s.agentLabel(ctx, workflowID)
	res, err := s.Store.PostGroupMessage(ctx, store.PostGroupInput{
		ConversationID: strings.TrimSpace(groupID), SenderKind: model.ChatSenderWorkflow, SenderRef: workflowID,
		Body: text, Mentions: mentions, ReplyTo: replyTo, IdemKey: idemKey, SenderLabel: label, SenderProject: project,
	})
	if err != nil {
		return Message{}, nil, err
	}
	if res.Message == nil { // 回复链过长被丢弃
		return Message{}, res, nil
	}
	msg, err := s.renderPosted(ctx, res)
	return msg, res, err
}

func (s *Service) renderPosted(ctx context.Context, res *store.PostGroupResult) (Message, error) {
	if res.Message == nil {
		return Message{}, errors.New("没有可渲染的消息")
	}
	e, err := s.loadGroup(ctx, res.Group.ID)
	if err != nil {
		return Message{}, err
	}
	states, err := s.Store.GroupMentionStates(ctx, res.Group.ID, []uint{res.Message.ID})
	if err != nil {
		return Message{}, err
	}
	return e.message(*res.Message, states[res.Message.ID]), nil
}

// agentLabel 取一个工作流的身份标签与项目名（群里的提示前缀用）。
func (s *Service) agentLabel(ctx context.Context, workflowID string) (label, project string) {
	if list, err := s.Store.ListAgentSessions(ctx, store.AgentSessionFilter{WorkflowID: workflowID, Limit: 1}, s.Now()); err == nil && len(list) > 0 {
		return list[0].IdentityLabel, lastSegment(list[0].ProjectDir)
	}
	return workflowID, ""
}

// AgentRead 是 Agent 的 chat read 在群里的结果。
type AgentRead struct {
	Items []Message
	More  bool
	Group *GroupInfo
	// MaxSeq 是返回的最大序号（已读游标据此推进）。
	MaxSeq int64
}

// ReadForAgent 取某个成员在群里还没读过的消息（只有入群之后、离开之前的）；peek=false 时推进它的已读游标，
// 并把对应的 @ 提醒行标为已读取并确认。afterSeq > 0 时从这个序号之后取（不依赖游标）。
func (s *Service) ReadForAgent(ctx context.Context, groupID, workflowID string, afterSeq int64, limit int, peek bool) (*AgentRead, error) {
	groupID = strings.TrimSpace(groupID)
	e, err := s.loadGroup(ctx, groupID)
	if err != nil {
		return nil, err
	}
	var mem *model.ConversationMember
	for i := range e.members {
		if e.members[i].MemberKind == model.MemberKindWorkflow && e.members[i].MemberRef == workflowID && e.members[i].LeftAt == nil {
			mem = &e.members[i]
		}
	}
	if mem == nil {
		return nil, &store.GroupError{Code: "not_member", Message: "你不是这个群的成员"}
	}
	vis := &store.GroupVisibility{From: mem.JoinedAt, To: mem.LeftAt}
	if afterSeq <= 0 {
		afterSeq = e.reads[model.ConversationReaderWorkflow+":"+workflowID]
	}
	rows, more, err := s.Store.GroupMessagesAfterSeq(ctx, groupID, afterSeq, limit, vis)
	if err != nil {
		return nil, err
	}
	ids := make([]uint, 0, len(rows))
	for _, r := range rows {
		ids = append(ids, r.ID)
	}
	states, err := s.Store.GroupMentionStates(ctx, groupID, ids)
	if err != nil {
		return nil, err
	}
	out := &AgentRead{Group: e.info(), More: more}
	for _, r := range rows {
		out.Items = append(out.Items, e.message(r, states[r.ID]))
		if r.Seq > out.MaxSeq {
			out.MaxSeq = r.Seq
		}
	}
	if !peek && out.MaxSeq > 0 {
		if err := s.Store.AdvanceGroupRead(ctx, groupID, model.ConversationReaderWorkflow, workflowID, out.MaxSeq); err != nil {
			return nil, err
		}
		if err := s.Store.MarkGroupMentionsRead(ctx, groupID, workflowID, ids); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// AgentGroupDigest 是某个工作流在一个群里的未读情况（chat list 与回执尾行用）。
type AgentGroupDigest struct {
	GroupID   string
	Title     string
	Unread    int
	Mentioned int
}

// DigestForAgent 列出某个工作流所在的群及各自的未读（只看入群之后的消息）。
func (s *Service) DigestForAgent(ctx context.Context, workflowID string) ([]AgentGroupDigest, error) {
	groups, err := s.Store.GroupsOfWorkflow(ctx, workflowID)
	if err != nil {
		return nil, err
	}
	out := make([]AgentGroupDigest, 0, len(groups))
	for _, g := range groups {
		mem, err := s.Store.GetGroupMember(ctx, g.ID, model.MemberKindWorkflow, workflowID)
		if err != nil {
			continue
		}
		n, m, err := s.Store.GroupUnread(ctx, g.ID, model.ConversationReaderWorkflow, workflowID, mem.JoinedAt)
		if err != nil {
			return nil, err
		}
		out = append(out, AgentGroupDigest{GroupID: g.ID, Title: g.Title, Unread: n, Mentioned: m})
	}
	return out, nil
}

// DescribeGroupError 把群聊规则错误翻成一句给 Agent / 界面看的话。
func DescribeGroupError(err error) string {
	var ge *store.GroupError
	if errors.As(err, &ge) {
		return fmt.Sprintf("%s：%s", ge.Code, ge.Message)
	}
	return err.Error()
}
