package holder

import (
	"testing"
	"time"
)

// 屏幕扫描器接入会话（#113 M3.2）：认出 TUI 才出现在 SessionInfo.Screen 里，识别结果变了才请 Hub 刷新清单。

func TestSessionScreen_PlainShellStaysSilent(t *testing.T) {
	e := newTestEnv(t, Limits{})
	s, term := e.open(t)
	term.Emit([]byte("$ ls\r\nfile-a  file-b\r\n"))
	eventually(t, "输出进入缓冲", func() bool { return s.info().End > 0 })
	if got := s.info().Screen; got != nil {
		t.Fatalf("普通 shell 不应有屏幕识别结果: %+v", got)
	}
	if s.screenChanged(e.clock.Now()) {
		t.Fatal("认不出任何东西时不应触发推送")
	}
}

func TestSessionScreen_ChangeDetectionAndRefresh(t *testing.T) {
	e := newTestEnv(t, Limits{})
	s, term := e.open(t)

	term.Emit([]byte("\x1b[2K⠠⠛ Thinking  4 tokens"))
	eventually(t, "认出状态行", func() bool {
		sc := s.info().Screen
		return sc != nil && sc.Word == "Thinking" && sc.Tokens == 4 && sc.Animating
	})
	if !s.screenChanged(e.clock.Now()) {
		t.Fatal("首次认出状态行应当推送")
	}
	if s.screenChanged(e.clock.Now()) {
		t.Fatal("摘要没变不应重复推送")
	}

	// 仅 token 数变化：摘要不变，不推送；但动画持续超过刷新间隔后要补推，让 Hub 看到计数在走。
	e.clock.Advance(time.Second)
	term.Emit([]byte("\x1b[2K⠰⠰ Thinking  9 tokens"))
	eventually(t, "token 更新", func() bool { return s.info().Screen.Tokens == 9 })
	if s.screenChanged(e.clock.Now()) {
		t.Fatal("只有 token 数变化时不应立刻推送")
	}
	e.clock.Advance(screenRefreshEvery)
	term.Emit([]byte("\x1b[2K⠰⠳ Thinking  30 tokens"))
	eventually(t, "token 再更新", func() bool { return s.info().Screen.Tokens == 30 })
	if !s.screenChanged(e.clock.Now()) {
		t.Fatal("动画持续超过刷新间隔应补推")
	}

	// 动词变化：立即推送。
	e.clock.Advance(time.Second)
	term.Emit([]byte("\x1b[2K⠰⠳ Reading  41 tokens"))
	eventually(t, "动词更新", func() bool { return s.info().Screen.Word == "Reading" })
	if !s.screenChanged(e.clock.Now()) {
		t.Fatal("状态词变化应推送")
	}

	// 安静下来：animating 翻转为 false，状态词被清掉，也要推送一次。
	e.clock.Advance(10 * time.Second)
	if !s.screenChanged(e.clock.Now()) {
		t.Fatal("动画停止应推送一次")
	}
	if sc := s.info().Screen; sc != nil && (sc.Animating || sc.Word != "") {
		t.Fatalf("静默后不应再报告现在的状态词: %+v", sc)
	}
}

func TestSessionScreen_ApprovalPush(t *testing.T) {
	e := newTestEnv(t, Limits{})
	s, term := e.open(t)
	term.Emit([]byte("⠰⠰ Running  317 tokens"))
	eventually(t, "认出", func() bool { return s.info().Screen != nil })
	s.screenChanged(e.clock.Now())

	e.clock.Advance(time.Second)
	term.Emit([]byte("$ sleep 45 Waiting for approval...\r\nNot in allowlist: sleep\r\n"))
	eventually(t, "认出审批", func() bool { sc := s.info().Screen; return sc != nil && sc.Approval })
	e.clock.Advance(3 * time.Second)
	if !s.screenChanged(e.clock.Now()) {
		t.Fatal("进入审批等待应推送")
	}
	if sc := s.info().Screen; sc == nil || !sc.Approval || sc.Animating {
		t.Fatalf("审批画面零字节，不应在动画: %+v", sc)
	}
}
