package termstate

import (
	"strings"
	"testing"
	"time"
)

// 屏幕帧来自实验台样本（CLI 2026.10.12-d47ca18，交互 TUI）。每帧前面夹一些转义序列，模拟真实重绘。
const (
	esc     = "\x1b"
	clrLine = esc + "[2K" + esc + "[1G"
	hide    = esc + "[?25l"
	sgrDim  = esc + "[2m"
	sgrOff  = esc + "[0m"
)

func frame(lines ...string) []byte {
	return []byte(hide + clrLine + sgrDim + strings.Join(lines, "\r\n") + sgrOff)
}

var t0 = time.Date(2026, 10, 15, 10, 0, 0, 0, time.UTC)

func TestScanner_StatusLines(t *testing.T) {
	cases := []struct {
		name   string
		line   string
		word   string
		tokens int
	}{
		{"just submitted", "⠀⠞ Working", "Working", -1},
		{"thinking", "⠠⠛ Thinking  4 tokens", "Thinking", 4},
		{"generating", "⠘⠣ Working  79 tokens", "Working", 79},
		{"globbing", "⠰⠰ Globbing  205 tokens", "Globbing", 205},
		{"reading", "⠰⠳ Reading  296 tokens", "Reading", 296},
		{"editing", "⠰⠳ Editing  430 tokens", "Editing", 430},
		{"running", "⠰⠰ Running  317 tokens", "Running", 317},
		{"exploring", "⠰⠳ Exploring  114 tokens", "Exploring", 114},
		{"thousands k", "⠰⠳ Working  1.2k tokens", "Working", 1200},
		{"thousands comma", "⠰⠳ Working  1,234 tokens", "Working", 1234},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := NewScanner()
			s.Feed(t0, frame("Tip: Use /run-everything to skip all approvals.", c.line))
			got := s.Info(t0.Add(500 * time.Millisecond))
			if !got.Recognized || got.Word != c.word || got.Tokens != c.tokens {
				t.Fatalf("got %+v, want word=%q tokens=%d", got, c.word, c.tokens)
			}
			if !got.Animating {
				t.Fatalf("should be animating: %+v", got)
			}
			if got.Approval || got.Cancelled {
				t.Fatalf("unexpected flags: %+v", got)
			}
		})
	}
}

func TestScanner_LastStatusLineWins(t *testing.T) {
	s := NewScanner()
	s.Feed(t0, frame("⠠⠛ Thinking  4 tokens"))
	s.Feed(t0.Add(time.Second), frame("⠰⠰ Globbing  205 tokens"))
	got := s.Info(t0.Add(1500 * time.Millisecond))
	if got.Word != "Globbing" || got.Tokens != 205 {
		t.Fatalf("got %+v", got)
	}
}

func TestScanner_StatusDroppedWhenQuiet(t *testing.T) {
	s := NewScanner()
	s.Feed(t0, frame("⠰⠰ Running  317 tokens"))
	got := s.Info(t0.Add(10 * time.Second))
	if got.Word != "" || got.Tokens != -1 || got.Animating {
		t.Fatalf("stale status line must not be reported as live: %+v", got)
	}
	if got.QuietFor != 10*time.Second {
		t.Fatalf("QuietFor = %v", got.QuietFor)
	}
}

func TestScanner_Approval(t *testing.T) {
	s := NewScanner()
	s.Feed(t0, frame("⠰⠰ Running  317 tokens"))
	s.Feed(t0.Add(time.Second), frame(
		"$ sleep 45; echo done-45 Waiting for approval...",
		"Not in allowlist: echo, sleep",
		"Add Shell(sleep), Shell(echo) to allowlist? (tab)",
		"Skip & tell the agent what to do instead (esc or n)",
	))
	got := s.Info(t0.Add(8 * time.Second))
	if !got.Approval || !got.Recognized {
		t.Fatalf("want approval: %+v", got)
	}
	if got.Animating {
		t.Fatalf("approval screen is byte-silent: %+v", got)
	}

	// 批准后画面继续：出现新的状态行，审批标志必须消失。
	s.Feed(t0.Add(9*time.Second), frame("⠰⠰ Running  330 tokens"))
	got = s.Info(t0.Add(9500 * time.Millisecond))
	if got.Approval || got.Word != "Running" {
		t.Fatalf("approval should clear after the screen moves on: %+v", got)
	}
}

func TestScanner_CancelledClearsApproval(t *testing.T) {
	s := NewScanner()
	s.Feed(t0, frame("$ sleep 90; echo done-90 Waiting for approval..."))
	s.Feed(t0.Add(2*time.Second), frame("$ sleep 90; echo done-90 Cancelled • 15s"))
	got := s.Info(t0.Add(3 * time.Second))
	if got.Approval || !got.Cancelled {
		t.Fatalf("got %+v", got)
	}
}

func TestScanner_FooterContextPct(t *testing.T) {
	s := NewScanner()
	s.Feed(t0, frame("> ", "Claude Sonnet 5.5 300K Max · 4.4%"))
	got := s.Info(t0.Add(time.Second))
	if !got.Recognized || got.CtxPct != 4.4 {
		t.Fatalf("got %+v", got)
	}
	s.Feed(t0.Add(2*time.Second), frame("Claude Sonnet 5.5 300K Max · 12%"))
	got = s.Info(t0.Add(3 * time.Second))
	if got.CtxPct != 12 {
		t.Fatalf("latest footer wins: %+v", got)
	}
	s.Feed(t0.Add(4*time.Second), frame("x · 250%"))
	if got = s.Info(t0.Add(5 * time.Second)); got.CtxPct != 12 {
		t.Fatalf("out-of-range pct must be ignored, got %+v", got)
	}
}

func TestScanner_Unrecognized(t *testing.T) {
	s := NewScanner()
	got := s.Info(t0)
	if got.Recognized || got.Tokens != -1 || got.CtxPct != -1 || got.QuietFor != -1 {
		t.Fatalf("empty scanner: %+v", got)
	}
	s.Feed(t0, []byte("$ ls -la\r\ntotal 8\r\ndrwxr-xr-x 2 root root 4096 Oct 15 10:00 .\r\n"))
	got = s.Info(t0.Add(time.Second))
	if got.Recognized || got.Word != "" || got.Approval {
		t.Fatalf("a plain shell must stay unrecognized: %+v", got)
	}
	if got.BytesPerSec <= 0 {
		t.Fatalf("byte rate should still be tracked: %+v", got)
	}
}

func TestScanner_UnknownTokenNotationDegrades(t *testing.T) {
	s := NewScanner()
	s.Feed(t0, frame("⠰⠳ Working  12x tokens"))
	got := s.Info(t0.Add(time.Second))
	// 「12x tokens」这种没见过的写法：状态行动词可以认，token 数必须降级为未知而不是猜。
	if got.Tokens != -1 {
		t.Fatalf("got %+v", got)
	}
}

func TestScanner_RateWindow(t *testing.T) {
	s := NewScanner()
	for i := 0; i < 5; i++ {
		s.Feed(t0.Add(time.Duration(i)*time.Second), make([]byte, 100))
	}
	if got := s.Info(t0.Add(4500 * time.Millisecond)); got.BytesPerSec != 100 {
		t.Fatalf("rate = %v", got.BytesPerSec)
	}
	// 5 秒无输出后窗口应当清空。
	if got := s.Info(t0.Add(20 * time.Second)); got.BytesPerSec != 0 {
		t.Fatalf("rate after silence = %v", got.BytesPerSec)
	}
	// 时钟回拨：不 panic，计数不丢。
	s.Feed(t0.Add(30*time.Second), make([]byte, 50))
	s.Feed(t0.Add(10*time.Second), make([]byte, 50))
	if got := s.Info(t0.Add(30 * time.Second)); got.BytesPerSec != 20 {
		t.Fatalf("rate after clock skew = %v", got.BytesPerSec)
	}
}

func TestScanner_TailBounded(t *testing.T) {
	s := NewScanner()
	s.Feed(t0, frame("⠰⠰ Running  1 tokens"))
	s.Feed(t0.Add(time.Second), []byte(strings.Repeat("x", 3*scanTailBytes)))
	if len(s.tail) > scanTailBytes {
		t.Fatalf("tail grew to %d", len(s.tail))
	}
	// 旧状态行已被挤出尾部。
	if got := s.Info(t0.Add(1500 * time.Millisecond)); got.Word != "" {
		t.Fatalf("old status line should be gone: %+v", got)
	}
}

func TestStripANSI(t *testing.T) {
	cases := map[string]string{
		"plain":                                    "plain",
		esc + "[31mred" + esc + "[0m":              "red",
		esc + "[?25l" + "x" + esc + "[?25h":        "x",
		esc + "]0;title\x07" + "body":              "body",
		esc + "]8;;http://x" + esc + "\\link":      "link",
		"a\r\nb":                                   "a\n\nb",
		"bell\x07":                                 "bell",
		esc + "7saved" + esc + "8":                 "saved",
		esc + "(B" + "text":                        "text",
		"trunc" + esc:                              "trunc",
		"partial" + esc + "[3":                     "partial",
		"⠰⠳ Reading" + esc + "[2K":                 "⠰⠳ Reading",
		"\x00\x01ok":                               "ok",
		"tab\tkept":                                "tab\tkept",
		esc + "[38;2;1;2;3m" + "rgb" + esc + "[0m": "rgb",
	}
	for in, want := range cases {
		if got := StripANSI([]byte(in)); got != want {
			t.Errorf("StripANSI(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestTUIVersionVerified(t *testing.T) {
	if !TUIVersionVerified("2026.10.12-d47ca18") {
		t.Fatal("experiment version must be verified")
	}
	for _, v := range []string{"", "  ", "2026.10.13-aaaaaaa", "9999"} {
		if TUIVersionVerified(v) {
			t.Fatalf("%q must not be verified", v)
		}
	}
}
