package mcp

import (
	"context"
	"testing"
	"time"

	"github.com/teacat99/RelayMesh/v2/internal/model"
	"github.com/teacat99/RelayMesh/v2/internal/store"
)

func newKeepaliveSession(t *testing.T, srv *Server, wf string) *model.FeedbackSession {
	t.Helper()
	sess, err := srv.store.CreateFeedbackSession(context.Background(), store.CreateSessionInput{
		WorkflowID:       wf,
		ProjectDirectory: "/test",
		Title:            "keepalive",
		Summary:          "keepalive test",
	})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	return sess
}

func submitReply(t *testing.T, srv *Server, id string) *model.FeedbackSession {
	t.Helper()
	ctx := context.Background()
	if _, err := srv.store.SubmitFeedback(ctx, store.SubmitFeedbackInput{SessionID: id, ResponseText: "收到"}); err != nil {
		t.Fatalf("submit feedback: %v", err)
	}
	got, err := srv.store.GetFeedbackSession(ctx, id)
	if err != nil {
		t.Fatalf("get session: %v", err)
	}
	return got
}

// 用户在智能体休眠期间提交了回复：「最近一次空回执」的时刻要保留，界面才能继续显示「等待」并接着往下算；
// 智能体取走回复之后才忘掉它。
func TestKeepalive_SurvivesCompletionUntilConsumed(t *testing.T) {
	srv := setupTestMCPServer(t)
	sess := newKeepaliveSession(t, srv, "wf-keepalive-survive")

	srv.RecordKeepaliveResponse(sess.ID)
	before := srv.GetLastKeepaliveTime(sess.ID)
	if before == nil {
		t.Fatal("空回执之后应有记录")
	}

	done := submitReply(t, srv, sess.ID)
	srv.NotifySessionCompleted(done)

	after := srv.GetLastKeepaliveTime(sess.ID)
	if after == nil {
		t.Fatal("回复提交后空回执时刻不应被清掉（否则界面上「等待」计时会跳回起点）")
	}
	if !after.Equal(*before) {
		t.Fatalf("空回执时刻不应改变: before=%v after=%v", before, after)
	}

	srv.markConsumedAndBroadcast(context.Background(), done)
	if got := srv.GetLastKeepaliveTime(sess.ID); got != nil {
		t.Fatalf("智能体取走回复之后应忘掉空回执时刻，仍有 %v", got)
	}
}

// 挂起超时 → 空回执：记下时刻。
func TestKeepalive_RecordedOnEmptyReceipt(t *testing.T) {
	srv := setupTestMCPServer(t)
	sess := newKeepaliveSession(t, srv, "wf-keepalive-empty")

	if _, err := srv.waitForFeedback(context.Background(), sess.ID, 1); err != nil {
		t.Fatalf("waitForFeedback: %v", err)
	}
	if srv.GetLastKeepaliveTime(sess.ID) == nil {
		t.Fatal("空回执之后应记下时刻")
	}
	if srv.HasActiveWaiter(sess.ID) {
		t.Fatal("挂起结束后不应还有活跃等待者")
	}
}

// 客户端中途断开（智能体放弃了这次长轮询）：同样算它离开，记下时刻。
func TestKeepalive_RecordedWhenClientDisconnects(t *testing.T) {
	srv := setupTestMCPServer(t)
	sess := newKeepaliveSession(t, srv, "wf-keepalive-cancel")

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		_, err := srv.waitForFeedback(ctx, sess.ID, 30)
		errCh <- err
	}()
	waitUntil(t, "出现等待者", func() bool { return srv.HasActiveWaiter(sess.ID) })

	cancel()
	select {
	case err := <-errCh:
		if err == nil {
			t.Fatal("取消后应返回 ctx 错误")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("取消后 waitForFeedback 没有返回")
	}
	if srv.GetLastKeepaliveTime(sess.ID) == nil {
		t.Fatal("客户端断开也应记下它离开的时刻")
	}
}

// 挂着等的时候用户回复了：智能体当场拿到回复，不属于「空回执」，不能留下空回执时刻。
func TestKeepalive_NotRecordedWhenReplyDelivered(t *testing.T) {
	srv := setupTestMCPServer(t)
	sess := newKeepaliveSession(t, srv, "wf-keepalive-delivered")

	type result struct {
		out any
		err error
	}
	resCh := make(chan result, 1)
	go func() {
		out, err := srv.waitForFeedback(context.Background(), sess.ID, 30)
		resCh <- result{out, err}
	}()
	waitUntil(t, "出现等待者", func() bool { return srv.HasActiveWaiter(sess.ID) })

	done := submitReply(t, srv, sess.ID)
	srv.NotifySessionCompleted(done)

	select {
	case r := <-resCh:
		if r.err != nil {
			t.Fatalf("waitForFeedback: %v", r.err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("回复提交后 waitForFeedback 没有返回")
	}
	if got := srv.GetLastKeepaliveTime(sess.ID); got != nil {
		t.Fatalf("回复已当场交付，不应留下空回执时刻: %v", got)
	}
	cur, err := srv.store.GetFeedbackSession(context.Background(), sess.ID)
	if err != nil {
		t.Fatalf("get session: %v", err)
	}
	if !cur.ConsumedByAI {
		t.Fatal("当场交付后应标记为已被智能体取走")
	}
}

// 新的长轮询开始时清掉旧的空回执时刻（此时界面显示的是「剩余」倒计时）。
func TestKeepalive_ClearedWhenNewWaiterRegisters(t *testing.T) {
	srv := setupTestMCPServer(t)
	srv.RecordKeepaliveResponse("sess-x")
	ch := srv.registerSessionWaiter("sess-x", 5)
	defer srv.unregisterSessionWaiter("sess-x", ch)
	if got := srv.GetLastKeepaliveTime("sess-x"); got != nil {
		t.Fatalf("新等待者登记后应清掉空回执时刻: %v", got)
	}
}

// 内存里的记录会定期清理很久以前的条目，不随会话数无限增长。
func TestKeepalive_PruneDropsStaleEntries(t *testing.T) {
	srv := setupTestMCPServer(t)
	stale := time.Now().Add(-keepaliveRetention - time.Hour)
	fresh := time.Now().Add(-time.Hour)

	srv.lastKeepaliveMu.Lock()
	srv.lastKeepalives["sess-stale"] = stale
	srv.lastKeepalives["sess-fresh"] = fresh
	srv.lastKeepalivePrunedAt = time.Time{} // 从未清理过
	srv.lastKeepaliveMu.Unlock()

	srv.RecordKeepaliveResponse("sess-new")

	if srv.GetLastKeepaliveTime("sess-stale") != nil {
		t.Fatal("过期条目应被清理")
	}
	if srv.GetLastKeepaliveTime("sess-fresh") == nil || srv.GetLastKeepaliveTime("sess-new") == nil {
		t.Fatal("未过期的条目与刚记录的条目都应保留")
	}

	// 一小时内不重复扫描：刚被放回去的过期条目这次不会被清掉
	srv.lastKeepaliveMu.Lock()
	srv.lastKeepalives["sess-stale2"] = stale
	srv.lastKeepaliveMu.Unlock()
	srv.RecordKeepaliveResponse("sess-new2")
	if srv.GetLastKeepaliveTime("sess-stale2") == nil {
		t.Fatal("清理有最短间隔，间隔内不应再次扫描")
	}
}

func waitUntil(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("等待超时: %s", what)
}
