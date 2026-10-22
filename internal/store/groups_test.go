package store

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/teacat99/RelayMesh/v2/internal/model"
)

func newGroupStore(t *testing.T) *Store {
	t.Helper()
	st, err := New(":memory:")
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	return st
}

func mkGroup(t *testing.T, st *Store, members ...string) *model.Conversation {
	t.Helper()
	g, err := st.CreateGroup(context.Background(), CreateGroupInput{Title: "测试群", Members: members})
	if err != nil {
		t.Fatalf("create group: %v", err)
	}
	return g
}

func post(t *testing.T, st *Store, g *model.Conversation, kind, ref, body string, mentions ...string) *PostGroupResult {
	t.Helper()
	res, err := st.PostGroupMessage(context.Background(), PostGroupInput{ConversationID: g.ID, SenderKind: kind, SenderRef: ref, Body: body, Mentions: mentions, SenderLabel: ref})
	if err != nil {
		t.Fatalf("post %s: %v", body, err)
	}
	return res
}

func pendingMentions(t *testing.T, st *Store, wf string) int {
	t.Helper()
	var n int64
	if err := st.db.Model(&model.QueuedFeedback{}).Where("workflow_id = ? AND kind = ? AND status = 'pending'", wf, model.InboxKindChatMention).Count(&n).Error; err != nil {
		t.Fatal(err)
	}
	return int(n)
}

func TestCreateGroup_MembersAndSystemRows(t *testing.T) {
	st := newGroupStore(t)
	ctx := context.Background()
	g := mkGroup(t, st, "wf-a", "wf-b", "wf-a")
	members, err := st.ListGroupMembers(ctx, g.ID, false)
	if err != nil || len(members) != 3 {
		t.Fatalf("成员应是 你 + 2 个工作流（去重）: %v %v", len(members), err)
	}
	if members[0].Role != model.GroupRoleOwner || members[0].MemberKind != model.MemberKindHuman {
		t.Fatalf("第一个应是 owner（你）: %+v", members[0])
	}
	rows, _ := st.StreamGroupMessages(ctx, g.ID, StreamRange{Dir: StreamOlder, Limit: 50}, nil, 0, false)
	if len(rows) != 3 { // 群已创建 + 两个加入
		t.Fatalf("应有 3 条系统行: %d", len(rows))
	}
	if _, err := st.CreateGroup(ctx, CreateGroupInput{Title: " ", Members: []string{"x"}}); err == nil {
		t.Fatal("空群名应被拒绝")
	}
	if _, err := st.CreateGroup(ctx, CreateGroupInput{Title: "x"}); err == nil {
		t.Fatal("没有成员应被拒绝")
	}
	many := make([]string, GroupMaxMembers)
	for i := range many {
		many[i] = fmt.Sprintf("wf-%d", i)
	}
	if _, err := st.CreateGroup(ctx, CreateGroupInput{Title: "太多", Members: many}); err == nil {
		t.Fatal("成员数（含你）超过上限应被拒绝")
	}
}

// 设计稿 03 §3.1 的规则表：谁发、点谁、成员的推送策略 → 谁的收件箱里多一行。
func TestPostGroupMessage_DeliveryRules(t *testing.T) {
	cases := []struct {
		name     string
		sender   string // human | workflow
		ref      string
		mentions []string
		policy   map[string]string // 成员 → 推送策略
		want     []string
	}{
		{"你发、无 @、默认策略", "human", "user", nil, nil, nil},
		{"你发、无 @、成员 all", "human", "user", nil, map[string]string{"wf-b": "all"}, []string{"wf-b"}},
		{"你发、@A", "human", "user", []string{"wf-a"}, nil, []string{"wf-a"}},
		{"你发、@A、B 是 all", "human", "user", []string{"wf-a"}, map[string]string{"wf-b": "all"}, []string{"wf-a", "wf-b"}},
		{"你发、@all", "human", "user", []string{"all"}, nil, []string{"wf-a", "wf-b", "wf-c"}},
		{"你发、@A、A 是 none", "human", "user", []string{"wf-a"}, map[string]string{"wf-a": "none"}, nil},
		{"A 发、无 @", "workflow", "wf-a", nil, nil, nil},
		{"A 发、@B", "workflow", "wf-a", []string{"wf-b"}, nil, []string{"wf-b"}},
		{"A 发、@你", "workflow", "wf-a", []string{"human"}, nil, nil},
		{"A 发、@all（不含自己）", "workflow", "wf-a", []string{"all"}, nil, []string{"wf-b", "wf-c"}},
		{"A 发、@自己被忽略", "workflow", "wf-a", []string{"wf-a"}, nil, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			st := newGroupStore(t)
			g := mkGroup(t, st, "wf-a", "wf-b", "wf-c")
			for ref, p := range c.policy {
				if err := st.SetGroupMemberPolicy(context.Background(), g.ID, ref, p, nil, false); err != nil {
					t.Fatal(err)
				}
			}
			res := post(t, st, g, c.sender, c.ref, "你好", c.mentions...)
			got := append([]string(nil), res.Recipients...)
			if fmt.Sprint(got) != fmt.Sprint(c.want) && !(len(got) == 0 && len(c.want) == 0) {
				t.Fatalf("收件人 = %v，期望 %v", got, c.want)
			}
			for _, wf := range []string{"wf-a", "wf-b", "wf-c"} {
				want := 0
				for _, w := range c.want {
					if w == wf {
						want = 1
					}
				}
				if n := pendingMentions(t, st, wf); n != want {
					t.Fatalf("%s 的收件箱 %d 行，期望 %d", wf, n, want)
				}
			}
			wantHuman := c.sender == "workflow" && len(c.mentions) > 0 && (c.mentions[0] == "human" || c.mentions[0] == "all")
			if res.MentionsHuman != wantHuman {
				t.Fatalf("MentionsHuman = %v，期望 %v", res.MentionsHuman, wantHuman)
			}
		})
	}
}

func TestPostGroupMessage_Rejections(t *testing.T) {
	st := newGroupStore(t)
	ctx := context.Background()
	g := mkGroup(t, st, "wf-a", "wf-b")
	code := func(err error) string { c, _ := GroupErrorCode(err); return c }

	if _, err := st.PostGroupMessage(ctx, PostGroupInput{ConversationID: g.ID, SenderKind: "workflow", SenderRef: "wf-x", Body: "我是外人"}); code(err) != "not_member" {
		t.Fatalf("非成员应是 not_member: %v", err)
	}
	if _, err := st.PostGroupMessage(ctx, PostGroupInput{ConversationID: g.ID, SenderKind: "workflow", SenderRef: "wf-a", Body: string(make([]byte, GroupMaxBodyBytes+1))}); code(err) != "too_large" {
		t.Fatalf("超长应是 too_large: %v", err)
	}
	if _, err := st.PostGroupMessage(ctx, PostGroupInput{ConversationID: g.ID, SenderKind: "workflow", SenderRef: "wf-a", Body: "x", Mentions: []string{"wf-zzz"}}); err == nil {
		t.Fatal("@ 不存在的成员应被拒绝")
	}
	// 被移出的成员不能再发，也不能被 @
	if err := st.RemoveGroupMember(ctx, g.ID, "wf-b"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.PostGroupMessage(ctx, PostGroupInput{ConversationID: g.ID, SenderKind: "workflow", SenderRef: "wf-b", Body: "x"}); code(err) != "not_member" {
		t.Fatalf("已离开的成员应是 not_member: %v", err)
	}
	if _, err := st.PostGroupMessage(ctx, PostGroupInput{ConversationID: g.ID, SenderKind: "human", SenderRef: "user", Body: "x", Mentions: []string{"wf-b"}}); err == nil {
		t.Fatal("@ 已离开的成员应被拒绝")
	}
	// 归档后只读
	archived := true
	if _, err := st.UpdateGroup(ctx, g.ID, UpdateGroupInput{Archived: &archived}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.PostGroupMessage(ctx, PostGroupInput{ConversationID: g.ID, SenderKind: "human", SenderRef: "user", Body: "x"}); code(err) != "group_archived" {
		t.Fatalf("归档后应是 group_archived: %v", err)
	}
}

func TestPostGroupMessage_Idempotent(t *testing.T) {
	st := newGroupStore(t)
	g := mkGroup(t, st, "wf-a", "wf-b")
	in := PostGroupInput{ConversationID: g.ID, SenderKind: "workflow", SenderRef: "wf-a", Body: "只发一次", Mentions: []string{"wf-b"}, IdemKey: "k1"}
	first, err := st.PostGroupMessage(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	second, err := st.PostGroupMessage(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	if !second.Duplicate || second.Message.ID != first.Message.ID {
		t.Fatalf("重试应命中第一次: %+v", second)
	}
	if n := pendingMentions(t, st, "wf-b"); n != 1 {
		t.Fatalf("重试不应再投递: %d", n)
	}
}

func TestPostGroupMessage_RateLimit(t *testing.T) {
	st := newGroupStore(t)
	g := mkGroup(t, st, "wf-a")
	for i := 0; i < GroupRatePerMinute; i++ {
		post(t, st, g, "workflow", "wf-a", fmt.Sprintf("m%d", i))
	}
	_, err := st.PostGroupMessage(context.Background(), PostGroupInput{ConversationID: g.ID, SenderKind: "workflow", SenderRef: "wf-a", Body: "第 11 条"})
	if c, _ := GroupErrorCode(err); c != "rate_limited" {
		t.Fatalf("应是 rate_limited: %v", err)
	}
	// 你不受限速约束
	post(t, st, g, "human", "user", "我可以发")
}

// 回复链：你 @A（hop 0）→ A @B（1）→ B @A（2）→ A @B（3）→ B @A 到第 4 跳被丢弃并留系统行。
func TestPostGroupMessage_HopLimit(t *testing.T) {
	st := newGroupStore(t)
	ctx := context.Background()
	g := mkGroup(t, st, "wf-a", "wf-b")
	post(t, st, g, "human", "user", "开始", "wf-a")
	steps := []struct{ from, to string }{{"wf-a", "wf-b"}, {"wf-b", "wf-a"}, {"wf-a", "wf-b"}}
	for i, s := range steps {
		r := post(t, st, g, "workflow", s.from, fmt.Sprintf("第 %d 跳", i+1), s.to)
		if r.Message.Hop != i+1 {
			t.Fatalf("第 %d 条的 hop = %d", i+1, r.Message.Hop)
		}
	}
	r, err := st.PostGroupMessage(ctx, PostGroupInput{ConversationID: g.ID, SenderKind: "workflow", SenderRef: "wf-b", Body: "第 4 跳", Mentions: []string{"wf-a"}})
	if err != nil {
		t.Fatal(err)
	}
	if !r.HopDropped || r.Message != nil {
		t.Fatalf("第 4 跳应被丢弃: %+v", r)
	}
	rows, _ := st.StreamGroupMessages(ctx, g.ID, StreamRange{Dir: StreamOlder, Limit: 100}, nil, 0, false)
	var hopRows int
	for _, m := range rows {
		if m.Kind == model.ChatKindSystem && m.Body == model.GroupEventHopLimit {
			hopRows++
		}
		if m.Body == "第 4 跳" {
			t.Fatal("被丢弃的消息不应落库")
		}
	}
	if hopRows != 1 {
		t.Fatalf("应留一条系统行: %d", hopRows)
	}
	// 再丢一次，5 分钟内系统行不重复
	_, _ = st.PostGroupMessage(ctx, PostGroupInput{ConversationID: g.ID, SenderKind: "workflow", SenderRef: "wf-b", Body: "又来", Mentions: []string{"wf-a"}})
	rows, _ = st.StreamGroupMessages(ctx, g.ID, StreamRange{Dir: StreamOlder, Limit: 100}, nil, 0, false)
	hopRows = 0
	for _, m := range rows {
		if m.Body == model.GroupEventHopLimit {
			hopRows++
		}
	}
	if hopRows != 1 {
		t.Fatalf("同一人 5 分钟内系统行不应重复: %d", hopRows)
	}
	// 你说话之后链条重置：下一条 Agent 消息的触发是你的新消息
	post(t, st, g, "human", "user", "继续", "wf-b")
	r2 := post(t, st, g, "workflow", "wf-b", "收到", "wf-a")
	if r2.Message.Hop != 1 {
		t.Fatalf("人说话之后 hop 应重新从 1 算: %d", r2.Message.Hop)
	}
}

// 冷却：5 分钟内 Agent→Agent 消息达到阈值且期间没有人说话 → 新消息只落库、不投递，并留一条系统行。
func TestPostGroupMessage_Cooling(t *testing.T) {
	st := newGroupStore(t)
	ctx := context.Background()
	g := mkGroup(t, st, "wf-a", "wf-b")
	// 直接铺 12 条 Agent→Agent 的历史（绕开限速与跳数，只测冷却判定）
	now := time.Now()
	for i := 0; i < GroupCoolThreshold; i++ {
		m := model.ChatMessage{ConversationID: g.ID, Seq: int64(100 + i), SenderKind: "workflow", SenderRef: "wf-a", Kind: model.ChatKindText, Body: "x", Mentions: model.StringArray{"wf-b"}, CreatedAt: now.Add(-time.Minute)}
		if err := st.db.Create(&m).Error; err != nil {
			t.Fatal(err)
		}
	}
	r := post(t, st, g, "workflow", "wf-b", "再来一条", "wf-a")
	if !r.Cooling || !r.CoolingStarted {
		t.Fatalf("应进入冷却: %+v", r)
	}
	if len(r.Recipients) != 0 || pendingMentions(t, st, "wf-a") != 0 {
		t.Fatal("冷却中不应投递")
	}
	if r.Message == nil {
		t.Fatal("冷却中仍应落库")
	}
	r = post(t, st, g, "workflow", "wf-b", "还来", "wf-a")
	if !r.Cooling || r.CoolingStarted {
		t.Fatalf("冷却已通知过，不应再次触发: %+v", r)
	}
	// 你说一句，冷却解除，且你的消息正常投递
	r = post(t, st, g, "human", "user", "好了", "wf-a")
	if r.Cooling || len(r.Recipients) != 1 {
		t.Fatalf("你说话应解除冷却并正常投递: %+v", r)
	}
	_ = ctx
}

func TestPostGroupMessage_ConcurrentSeqNoGaps(t *testing.T) {
	st := newGroupStore(t)
	ctx := context.Background()
	g := mkGroup(t, st, "wf-a")
	before, _ := st.GroupLastSeq(ctx, g.ID)
	const n = 100
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := st.PostGroupMessage(ctx, PostGroupInput{ConversationID: g.ID, SenderKind: "human", SenderRef: "user", Body: fmt.Sprintf("并发 %d", i)})
			errs <- err
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	rows, err := st.StreamGroupMessages(ctx, g.ID, StreamRange{Dir: StreamNewer, Limit: 500}, nil, 0, false)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[int64]bool{}
	for _, m := range rows {
		if seen[m.Seq] {
			t.Fatalf("序号重复 %d", m.Seq)
		}
		seen[m.Seq] = true
	}
	last, _ := st.GroupLastSeq(ctx, g.ID)
	if last != before+n {
		t.Fatalf("最后序号 %d，期望 %d", last, before+n)
	}
	for s := int64(1); s <= last; s++ {
		if !seen[s] {
			t.Fatalf("序号有空洞 %d", s)
		}
	}
}

func TestGroupReads_UnreadAndVisibility(t *testing.T) {
	st := newGroupStore(t)
	ctx := context.Background()
	g := mkGroup(t, st, "wf-a", "wf-b")
	post(t, st, g, "workflow", "wf-a", "A 的话")
	post(t, st, g, "workflow", "wf-a", "A 点名你", "human")

	n, _, err := st.GroupUnread(ctx, g.ID, model.ConversationReaderHuman, GroupHumanRef, time.Time{})
	if err != nil || n != 2 {
		t.Fatalf("你的未读应是 A 的 2 条: %d %v", n, err)
	}
	// 你发消息 = 你看过这个群：游标推到你这条，之前的都算已读
	post(t, st, g, "human", "user", "你的话", "wf-a")
	if n, _, _ := st.GroupUnread(ctx, g.ID, model.ConversationReaderHuman, GroupHumanRef, time.Time{}); n != 0 {
		t.Fatalf("你发消息之后未读应清零: %d", n)
	}
	n, m, _ := st.GroupUnread(ctx, g.ID, model.ConversationReaderWorkflow, "wf-b", time.Time{})
	if n != 3 || m != 0 {
		t.Fatalf("B 的未读应是 3 条（含你的话）、点名 0: %d %d", n, m)
	}
	n, m, _ = st.GroupUnread(ctx, g.ID, model.ConversationReaderWorkflow, "wf-a", time.Time{})
	if n != 1 || m != 1 {
		t.Fatalf("A 的未读应是你的那 1 条、其中点名 1: %d %d", n, m)
	}
	last, _ := st.GroupLastSeq(ctx, g.ID)
	if err := st.AdvanceGroupRead(ctx, g.ID, model.ConversationReaderWorkflow, "wf-b", last); err != nil {
		t.Fatal(err)
	}
	if n, _, _ := st.GroupUnread(ctx, g.ID, model.ConversationReaderWorkflow, "wf-b", time.Time{}); n != 0 {
		t.Fatalf("推进游标后未读应为 0: %d", n)
	}
	if err := st.AdvanceGroupRead(ctx, g.ID, model.ConversationReaderHuman, GroupHumanRef, last); err != nil {
		t.Fatal(err)
	}
	// 游标只增不减
	_ = st.AdvanceGroupRead(ctx, g.ID, model.ConversationReaderHuman, GroupHumanRef, 1)
	reads, _ := st.GroupReadSeqs(ctx, g.ID)
	if reads["human:user"] != last {
		t.Fatalf("游标不应后退: %v", reads)
	}
}

// 群聊 @ 提醒不是「你追加的话」：不进追加列表、不并入秒回、不占追加额度。
func TestChatMention_NotInOneToOneInbox(t *testing.T) {
	st := newGroupStore(t)
	ctx := context.Background()
	g := mkGroup(t, st, "wf-a")
	post(t, st, g, "human", "user", "在群里点你", "wf-a")
	items, err := st.ListInboxItems(ctx, "wf-a")
	if err != nil || len(items) != 0 {
		t.Fatalf("追加列表里不该有群提醒: %d %v", len(items), err)
	}
	sess, err := st.CreateFeedbackSession(ctx, CreateSessionInput{WorkflowID: "wf-a", ProjectDirectory: "/tmp/p", Summary: "新一轮", TimeoutSeconds: 600})
	if err != nil {
		t.Fatal(err)
	}
	if sess.Status != "pending" {
		t.Fatalf("群提醒不应触发秒回: %s", sess.Status)
	}
	if n := pendingMentions(t, st, "wf-a"); n != 1 {
		t.Fatalf("群提醒应仍在收件箱里等 Agent 读取: %d", n)
	}
	// Agent 读了群消息 → 提醒行变成已读取并确认
	msgs, _, _ := st.GroupMessagesAfterSeq(ctx, g.ID, 0, 50, nil)
	var ids []uint
	for _, m := range msgs {
		ids = append(ids, m.ID)
	}
	if err := st.MarkGroupMentionsRead(ctx, g.ID, "wf-a", ids); err != nil {
		t.Fatal(err)
	}
	if n := pendingMentions(t, st, "wf-a"); n != 0 {
		t.Fatalf("读过之后不应再 pending: %d", n)
	}
	states, _ := st.GroupMentionStates(ctx, g.ID, ids)
	var found bool
	for _, byWF := range states {
		if byWF["wf-a"] == model.InboxStateAcked {
			found = true
		}
	}
	if !found {
		t.Fatalf("提醒行状态应是 acked: %v", states)
	}
}

func TestGroupMembers_RejoinAndVisibility(t *testing.T) {
	st := newGroupStore(t)
	ctx := context.Background()
	g := mkGroup(t, st, "wf-a")
	post(t, st, g, "human", "user", "入群前的话")
	time.Sleep(5 * time.Millisecond)
	if err := st.AddGroupMembers(ctx, g.ID, "user", []string{"wf-b"}); err != nil {
		t.Fatal(err)
	}
	b, err := st.GetGroupMember(ctx, g.ID, model.MemberKindWorkflow, "wf-b")
	if err != nil {
		t.Fatal(err)
	}
	vis := &GroupVisibility{From: b.JoinedAt}
	rows, _, _ := st.GroupMessagesAfterSeq(ctx, g.ID, 0, 50, vis)
	for _, m := range rows {
		if m.Body == "入群前的话" {
			t.Fatal("新成员不应看到入群之前的消息")
		}
	}
	// 移出后再拉回来：从重新加入起看
	if err := st.RemoveGroupMember(ctx, g.ID, "wf-b"); err != nil {
		t.Fatal(err)
	}
	if err := st.AddGroupMembers(ctx, g.ID, "user", []string{"wf-b"}); err != nil {
		t.Fatal(err)
	}
	b2, _ := st.GetGroupMember(ctx, g.ID, model.MemberKindWorkflow, "wf-b")
	if b2.LeftAt != nil || !b2.JoinedAt.After(b.JoinedAt) {
		t.Fatalf("重新加入应清掉 left_at 并刷新入群时间: %+v", b2)
	}
	gs, _ := st.GroupsOfWorkflow(ctx, "wf-b")
	if len(gs) != 1 || gs[0].ID != g.ID {
		t.Fatalf("GroupsOfWorkflow: %+v", gs)
	}
}
