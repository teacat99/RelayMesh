package mcp

import (
	"context"
	"strings"
	"testing"
	"time"
)

// 用户点「+1 分钟」：正挂着的这次长轮询不会在原定时间发空回执，而是多挂一段。
func TestPresence_PlusExtendsHold(t *testing.T) {
	srv := setupTestMCPServer(t)
	sess := newKeepaliveSession(t, srv, "wf-presence-plus")

	type res struct {
		out string
		at  time.Duration
	}
	ch := make(chan res, 1)
	start := time.Now()
	go func() {
		out, _ := srv.waitForFeedback(context.Background(), sess.ID, 1)
		s, _ := out.(string)
		ch <- res{s, time.Since(start)}
	}()

	// 等挂起登记好
	deadline := time.Now().Add(2 * time.Second)
	for !srv.HasActiveWaiter(sess.ID) && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if _, ok := srv.PresencePlus(sess.ID); !ok {
		t.Fatal("有人在挂着时 PresencePlus 应成功")
	}

	select {
	case r := <-ch:
		// 原定 1 秒，加了 60 秒，不应在几秒内返回
		t.Fatalf("加了一分钟却在 %v 就返回了: %.60s", r.at, r.out)
	case <-time.After(2500 * time.Millisecond):
	}
	// 清理：提交回复让挂起收场
	done := submitReply(t, srv, sess.ID)
	srv.NotifySessionCompleted(done)
	select {
	case <-ch:
	case <-time.After(3 * time.Second):
		t.Fatal("提交回复后挂起应立即结束")
	}
}

// 没有人在挂着：没有可延的东西。
func TestPresence_NoWaiter(t *testing.T) {
	srv := setupTestMCPServer(t)
	if _, ok := srv.PresencePlus("nope"); ok {
		t.Fatal("没有等待者时 PresencePlus 应返回 false")
	}
	if _, ok := srv.PresenceTyping("nope"); ok {
		t.Fatal("没有等待者时 PresenceTyping 应返回 false")
	}
}

// 正在输入（心跳新鲜）时到点也不发空回执；停手之后才发。
func TestPresence_TypingHoldsUntilIdle(t *testing.T) {
	srv := setupTestMCPServer(t)
	sess := newKeepaliveSession(t, srv, "wf-presence-typing")

	ch := make(chan time.Duration, 1)
	start := time.Now()
	go func() {
		_, _ = srv.waitForFeedback(context.Background(), sess.ID, 1)
		ch <- time.Since(start)
	}()
	deadline := time.Now().Add(2 * time.Second)
	for !srv.HasActiveWaiter(sess.ID) && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	// 每 300ms 一次心跳，持续 2 秒（原定 1 秒到点）
	end := time.Now().Add(2 * time.Second)
	for time.Now().Before(end) {
		srv.PresenceTyping(sess.ID)
		select {
		case d := <-ch:
			t.Fatalf("输入期间不应超时，%v 就返回了", d)
		case <-time.After(300 * time.Millisecond):
		}
	}
	// 停手后应在新鲜期（10s）内结束，而不是永远挂着；这里只确认最终会返回并发出空回执
	select {
	case d := <-ch:
		if d < 2*time.Second {
			t.Fatalf("返回得太早: %v", d)
		}
	case <-time.After(14 * time.Second):
		t.Fatal("停手后应发出空回执")
	}
}

func TestClosingGraceNote(t *testing.T) {
	n := closingGraceNote(2, 3, 2)
	for _, want := range []string{"第 3 轮", "共 3 轮", "AwaitShell({ block_until_ms: 120000 })", "禁止用 shell sleep"} {
		if !strings.Contains(n, want) {
			t.Fatalf("收尾宽限说明缺少 %q:\n%s", want, n)
		}
	}
	if !strings.Contains(closingGraceNote(1, 0, 1), "共 不限 轮") {
		t.Fatal("上限 0 应写成不限")
	}
}
