package chat

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/teacat99/RelayMesh/v2/internal/model"
	"github.com/teacat99/RelayMesh/v2/internal/store"
)

const testWF = "wf-chat-test"

// seed 造一段混合历史：N 轮「汇报 + 回复」，穿插收件箱追加和通知。返回总条数下界。
func seed(t *testing.T, st *store.Store, rounds int) {
	t.Helper()
	ctx := context.Background()
	for i := 0; i < rounds; i++ {
		s, err := st.CreateFeedbackSession(ctx, store.CreateSessionInput{
			WorkflowID: testWF, ProjectDirectory: "/tmp/p", Summary: fmt.Sprintf("汇报 %d 需要你确认", i), TimeoutSeconds: 600,
		})
		if err != nil {
			t.Fatalf("create session %d: %v", i, err)
		}
		// 收件箱里有追加时，新汇报会被它「秒回」（会话一创建就是 completed）；此时不能再提交。
		if s.Status == "pending" {
			if _, err := st.SubmitFeedback(ctx, store.SubmitFeedbackInput{SessionID: s.ID, ResponseText: fmt.Sprintf("回复 %d", i)}); err != nil {
				t.Fatalf("submit %d: %v", i, err)
			}
		}
		if i%3 == 0 {
			if _, err := st.QueueWorkflowFeedback(ctx, store.QueueFeedbackInput{WorkflowID: testWF, ResponseText: fmt.Sprintf("追加 %d", i)}); err != nil {
				t.Fatalf("queue %d: %v", i, err)
			}
		}
		if i%4 == 0 {
			if _, err := st.CreateNoticeSession(ctx, store.CreateSessionInput{WorkflowID: testWF, ProjectDirectory: "/tmp/p", Summary: fmt.Sprintf("进度 %d", i)}); err != nil {
				t.Fatalf("notice %d: %v", i, err)
			}
		}
		// 拉开毫秒，既覆盖「跨毫秒」也让同毫秒平局有机会出现。
		if i%5 == 4 {
			time.Sleep(3 * time.Millisecond)
		}
	}
}

func newService(t *testing.T) (*Service, *store.Store) {
	t.Helper()
	st, err := store.New(":memory:")
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	return NewService(st), st
}

func ids(ms []Message) []string {
	out := make([]string, len(ms))
	for i, m := range ms {
		out[i] = m.ID
	}
	return out
}

func assertAscending(t *testing.T, ms []Message) {
	t.Helper()
	for i := 1; i < len(ms); i++ {
		if !ms[i-1].Key().Less(ms[i].Key()) {
			t.Fatalf("消息不是严格升序: [%d]=%s [%d]=%s", i-1, ms[i-1].ID, i, ms[i].ID)
		}
	}
}

func TestList_PagingBackwardNoDupNoGap(t *testing.T) {
	svc, st := newService(t)
	seed(t, st, 24)
	ctx := context.Background()

	all, err := svc.List(ctx, testWF, ListParams{Limit: MaxLimit})
	if err != nil {
		t.Fatal(err)
	}
	if all.HasMoreBefore {
		t.Fatalf("一次取满应没有更早的了，实际 has_more_before=true（%d 条）", len(all.Items))
	}
	assertAscending(t, all.Items)
	if len(all.Items) < 24*2 {
		t.Fatalf("消息条数偏少: %d", len(all.Items))
	}

	// 7 条一页往前翻，拼回来必须与一次取满完全一致。
	var got []Message
	before := ""
	for pages := 0; pages < 100; pages++ {
		pg, err := svc.List(ctx, testWF, ListParams{Limit: 7, Before: before})
		if err != nil {
			t.Fatal(err)
		}
		got = append(append([]Message{}, pg.Items...), got...)
		if !pg.HasMoreBefore {
			break
		}
		before = pg.BeforeCursor
	}
	assertAscending(t, got)
	want, have := ids(all.Items), ids(got)
	if fmt.Sprint(want) != fmt.Sprint(have) {
		t.Fatalf("分页拼接与整体不一致\nwant=%v\nhave=%v", want, have)
	}
}

func TestList_PagingForwardMatchesAndAround(t *testing.T) {
	svc, st := newService(t)
	seed(t, st, 18)
	ctx := context.Background()

	all, _ := svc.List(ctx, testWF, ListParams{Limit: MaxLimit})

	var got []Message
	after := ""
	for pages := 0; pages < 100; pages++ {
		// 从最老一条之前开始：先取最老一页，再向后。
		var pg *Page
		var err error
		if after == "" {
			// 取最老一页：往前翻到尽头
			cur := ""
			for {
				p, e := svc.List(ctx, testWF, ListParams{Limit: 6, Before: cur})
				if e != nil {
					t.Fatal(e)
				}
				pg = p
				if !p.HasMoreBefore {
					break
				}
				cur = p.BeforeCursor
			}
		} else {
			pg, err = svc.List(ctx, testWF, ListParams{Limit: 6, After: after})
			if err != nil {
				t.Fatal(err)
			}
		}
		got = append(got, pg.Items...)
		if after != "" && !pg.HasMoreAfter {
			break
		}
		if len(pg.Items) == 0 {
			break
		}
		after = pg.AfterCursor
	}
	// 第一页（最老一页）不一定是 6 条整，但向后拼接必须覆盖其后的全部。
	if fmt.Sprint(ids(all.Items)[len(all.Items)-len(got):]) != fmt.Sprint(ids(got)) {
		t.Fatalf("向后翻页与整体不一致")
	}

	mid := all.Items[len(all.Items)/2]
	pg, err := svc.List(ctx, testWF, ListParams{Limit: 10, Around: mid.Cursor})
	if err != nil {
		t.Fatal(err)
	}
	assertAscending(t, pg.Items)
	found := false
	for _, m := range pg.Items {
		if m.ID == mid.ID {
			found = true
		}
	}
	if !found || !pg.HasMoreBefore || !pg.HasMoreAfter {
		t.Fatalf("around 应包含中心且两侧都还有更多: found=%v before=%v after=%v", found, pg.HasMoreBefore, pg.HasMoreAfter)
	}
}

func TestList_SearchAndKinds(t *testing.T) {
	svc, st := newService(t)
	seed(t, st, 10)
	ctx := context.Background()

	pg, err := svc.List(ctx, testWF, ListParams{Limit: MaxLimit, Query: "汇报 3"})
	if err != nil {
		t.Fatal(err)
	}
	if len(pg.Items) != 1 || pg.Items[0].ID[:2] != "s:" {
		t.Fatalf("搜索应只命中第 3 轮汇报: %v", ids(pg.Items))
	}
	// 用户搜索里的通配符必须按字面处理。
	pg, err = svc.List(ctx, testWF, ListParams{Limit: MaxLimit, Query: "%"})
	if err != nil {
		t.Fatal(err)
	}
	if len(pg.Items) != 0 {
		t.Fatalf("%% 不应匹配全部: %v", ids(pg.Items))
	}
	pg, err = svc.List(ctx, testWF, ListParams{Limit: MaxLimit, Kinds: []string{KindProgress}})
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range pg.Items {
		if m.Kind != KindProgress {
			t.Fatalf("kinds 过滤漏了 %s(%s)", m.ID, m.Kind)
		}
	}
	if len(pg.Items) == 0 {
		t.Fatal("应当有通知类消息")
	}
}

func TestReadCursor_MonotonicAndUnread(t *testing.T) {
	svc, st := newService(t)
	ctx := context.Background()
	// 先触发已读纪元，再造历史——纪元之前的历史算已读，之后才有未读。
	if _, err := svc.List(ctx, testWF, ListParams{}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(5 * time.Millisecond)
	seed(t, st, 6)
	// 收尾：把还留在收件箱里的追加用一轮秒回消耗掉，后面新建的汇报才会真的停在 pending。
	tail, err := st.CreateFeedbackSession(ctx, store.CreateSessionInput{WorkflowID: testWF, ProjectDirectory: "/tmp/p", Summary: "收尾", TimeoutSeconds: 600})
	if err != nil {
		t.Fatal(err)
	}
	if tail.Status == "pending" {
		if _, err := st.SubmitFeedback(ctx, store.SubmitFeedbackInput{SessionID: tail.ID, ResponseText: "好"}); err != nil {
			t.Fatal(err)
		}
	}

	pg, err := svc.List(ctx, testWF, ListParams{Limit: MaxLimit})
	if err != nil {
		t.Fatal(err)
	}
	if pg.Unread < 6 {
		t.Fatalf("纪元之后的 Agent 消息应算未读，实际 %d", pg.Unread)
	}

	rs, err := svc.MarkRead(ctx, testWF, "")
	if err != nil {
		t.Fatal(err)
	}
	if rs.Unread != 0 {
		t.Fatalf("标已读后未读应为 0，实际 %d", rs.Unread)
	}
	// 游标只增不减：拿一个很旧的游标去推，不能把游标拉回去。
	old := pg.Items[0].Cursor
	rs2, err := svc.MarkRead(ctx, testWF, old)
	if err != nil {
		t.Fatal(err)
	}
	if rs2.ReadMs < rs.ReadMs || rs2.Unread != 0 {
		t.Fatalf("已读游标回退了: %+v -> %+v", rs, rs2)
	}
	// 之后新来的汇报重新变成未读。
	time.Sleep(3 * time.Millisecond)
	if _, err := st.CreateFeedbackSession(ctx, store.CreateSessionInput{WorkflowID: testWF, ProjectDirectory: "/tmp/p", Summary: "新汇报", TimeoutSeconds: 600}); err != nil {
		t.Fatal(err)
	}
	pg, _ = svc.List(ctx, testWF, ListParams{Limit: 5})
	if pg.Unread != 1 {
		t.Fatalf("新汇报应是 1 条未读，实际 %d", pg.Unread)
	}
	// 还没回的汇报是「待你处理」的 block。
	last := pg.Items[len(pg.Items)-1]
	if last.Kind != KindBlock || last.Status != "pending" {
		t.Fatalf("待答汇报应是 block/pending: %s %s", last.Kind, last.Status)
	}
}

func TestList_BadCursor(t *testing.T) {
	svc, _ := newService(t)
	if _, err := svc.List(context.Background(), testWF, ListParams{Before: "###"}); err == nil {
		t.Fatal("坏游标应报错")
	}
	if _, err := svc.List(context.Background(), " ", ListParams{}); err == nil {
		t.Fatal("空 workflow_id 应报错")
	}
}

// 追加被下一轮汇报「秒回」带走之后：每条追加仍是一条独立的消息（状态为已读），那一轮不再多出一条把它们拼起来的回复。
func TestList_MergedInboxStaysIndividual(t *testing.T) {
	svc, st := newService(t)
	ctx := context.Background()
	for i := 1; i <= 3; i++ {
		if _, err := st.QueueWorkflowFeedback(ctx, store.QueueFeedbackInput{WorkflowID: testWF, ResponseText: fmt.Sprintf("追加 %d", i)}); err != nil {
			t.Fatal(err)
		}
		time.Sleep(2 * time.Millisecond)
	}
	sess, err := st.CreateFeedbackSession(ctx, store.CreateSessionInput{WorkflowID: testWF, ProjectDirectory: "/tmp/p", Summary: "新一轮", TimeoutSeconds: 600})
	if err != nil {
		t.Fatal(err)
	}
	if sess.Status != "completed" {
		t.Fatalf("有排队追加时应被秒回: %s", sess.Status)
	}
	pg, err := svc.List(ctx, testWF, ListParams{Limit: MaxLimit})
	if err != nil {
		t.Fatal(err)
	}
	var inbox, replies int
	for _, m := range pg.Items {
		switch {
		case m.Source.Type == SourceQueuedFeedback:
			inbox++
			if m.Status != StatusRead {
				t.Fatalf("随秒回带走的追加应是已读: %s %s", m.ID, m.Status)
			}
		case m.ID[:2] == "r:":
			replies++
		}
	}
	if inbox != 3 || replies != 0 {
		t.Fatalf("应是 3 条独立追加、没有拼接回复: inbox=%d replies=%d (%v)", inbox, replies, ids(pg.Items))
	}
}

func TestReplyAfterMerge_Table(t *testing.T) {
	b := NewBuilder(testWF, "", 0)
	mk := func(text string) model.QueuedFeedback { return model.QueuedFeedback{ResponseText: text} }
	cases := []struct {
		name   string
		reply  string
		merged []model.QueuedFeedback
		want   string
		ok     bool
	}{
		{"没有合并", "你好", nil, "你好", true},
		{"全是合并的", "甲\n\n乙", []model.QueuedFeedback{mk("甲"), mk("乙")}, "", false},
		{"合并之后又补了话", "甲\n\n乙\n丙", []model.QueuedFeedback{mk("甲"), mk("乙")}, "丙", true},
		{"对不上合并的写法时原样保留", "被改过的", []model.QueuedFeedback{mk("甲")}, "被改过的", true},
	}
	for _, c := range cases {
		m, ok := b.ReplyAfterMerge(model.FeedbackSession{ID: "sess-aaaaaaaa", ResponseText: c.reply}, c.merged)
		if ok != c.ok || (ok && m.Text != c.want) {
			t.Fatalf("%s: ok=%v text=%q，期望 ok=%v text=%q", c.name, ok, m.Text, c.ok, c.want)
		}
	}
}
