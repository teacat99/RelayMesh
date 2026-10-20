package agentstate

import (
	"strings"
	"testing"
	"time"
)

func TestParseWire(t *testing.T) {
	cases := []struct {
		kind, status string
		name, norm   string
		ok           bool
	}{
		{"tool", "", EvToolEnd, "", true}, // 旧版 hook 的 postToolUse
		{" Tool ", "ignored", EvToolEnd, "", true},
		{"tool_end", "", EvToolEnd, "", true},
		{"tool_start", "", EvToolStart, "", true},
		{"tool_fail", "", EvToolFail, "", true},
		{"stop", "completed", EvStop, "completed", true},
		{"stop", "ABORTED", EvStop, "aborted", true},
		{"stop", "error", EvStop, "error", true},
		{"stop", "", EvStop, "other", true},
		{"stop", "something-new", EvStop, "other", true},
		{"session_start", "", EvSessionStart, "", true},
		{"session_end", "completed", EvSessionEnd, "completed", true},
		{"session_end", "window_close", EvSessionEnd, "closed", true},
		{"prompt", "", EvPrompt, "", true},
		{"thought", "", EvThought, "", true},
		{"response", "", EvResponse, "", true},
		{"compact", "", EvCompact, "", true},
		// Hub 自己产生的事件，hook 不能冒充
		{"chat_end", "completed", "", "", false},
		{"approval_wait", "", "", "", false},
		{"approval_done", "", "", "", false},
		{"", "", "", "", false},
		{"bogus", "", "", "", false},
	}
	for _, c := range cases {
		name, norm, ok := ParseWire(c.kind, c.status)
		if name != c.name || norm != c.norm || ok != c.ok {
			t.Errorf("ParseWire(%q,%q) = (%q,%q,%v), want (%q,%q,%v)", c.kind, c.status, name, norm, ok, c.name, c.norm, c.ok)
		}
	}
}

func TestSanitizeTool(t *testing.T) {
	cases := map[string]string{
		"Shell":                      "Shell",
		"  Read ":                    "Read",
		"mcp:relaymesh:interactive":  "mcp:relaymesh:interactive",
		"rm -rf /; echo $HOME":       "rm_-rf____echo__HOME",
		"带中文的工具":                     "______",
		"":                           "",
		"   ":                        "",
		strings.Repeat("a", 100):     strings.Repeat("a", 64),
		"line1\nline2":               "line1_line2",
		"tab\there":                  "tab_here",
		"quote\"and'single":          "quote_and_single",
		"sk-abc123/very/secret.json": "sk-abc123_very_secret.json",
	}
	for in, want := range cases {
		if got := SanitizeTool(in); got != want {
			t.Errorf("SanitizeTool(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestParseUnixMilli(t *testing.T) {
	now := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	if got := ParseUnixMilli(now.UnixMilli(), now); !got.Equal(now) {
		t.Fatalf("exact now = %v", got)
	}
	if got := ParseUnixMilli(now.Add(-time.Hour).UnixMilli(), now); got.IsZero() {
		t.Fatalf("an hour ago rejected")
	}
	for _, bad := range []int64{0, -5, time.Date(2019, 12, 31, 0, 0, 0, 0, time.UTC).UnixMilli(), now.Add(25 * time.Hour).UnixMilli()} {
		if got := ParseUnixMilli(bad, now); !got.IsZero() {
			t.Errorf("ParseUnixMilli(%d) = %v, want zero", bad, got)
		}
	}
	if got := ParseUnixMilli(now.Add(23*time.Hour).UnixMilli(), now); got.IsZero() {
		t.Fatalf("23h ahead should pass through (Apply clamps it)")
	}
}

func TestNormalizeClientKindAndVersion(t *testing.T) {
	for in, want := range map[string]string{"": "", " IDE ": "ide", "cli": "cli", "vscode": "unknown", "Cursor": "unknown"} {
		if got := NormalizeClientKind(in); got != want {
			t.Errorf("NormalizeClientKind(%q) = %q, want %q", in, got, want)
		}
	}
	for in, want := range map[string]string{
		"2026.10.12-d47ca18":    "2026.10.12-d47ca18",
		"1.7.0":                 "1.7.0",
		" 3.0.1 ":               "3.0.1",
		"":                      "",
		"bad version":           "",
		"x@y.com":               "", // 不留任何像邮箱的东西
		strings.Repeat("1", 33): "",
	} {
		if got := NormalizeVersion(in); got != want {
			t.Errorf("NormalizeVersion(%q) = %q, want %q", in, got, want)
		}
	}
}
