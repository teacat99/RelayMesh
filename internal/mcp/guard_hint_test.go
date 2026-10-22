package mcp

import (
	"context"
	"strings"
	"testing"

	"github.com/teacat99/RelayMesh/v2/internal/store"
)

func TestGuardPermitHintHeaderOnlyWhenBoundEnabledGuard(t *testing.T) {
	srv := setupTestMCPServer(t)
	ctx := context.Background()
	wf := "wf-guard-hint"
	sess, err := srv.store.CreateFeedbackSession(ctx, store.CreateSessionInput{
		WorkflowID:       wf,
		ProjectDirectory: "/tmp",
		Title:            "hint test",
		Summary:          "summary",
	})
	if err != nil {
		t.Fatalf("session: %v", err)
	}

	base := srv.formatSessionHeaderWithContext(ctx, sess)
	if strings.Contains(base, "guard:") {
		t.Fatalf("无绑定预设时不应出现 guard 行:\n%s", base)
	}

	_, _, err = srv.store.UpsertTerminalLauncher(ctx, store.TerminalLauncher{
		ID: "lg1", Name: "G", Host: "RM-H3B4-5AW2-4DPP-ZAW6", Dir: "/tmp", Command: "agent",
		Guard: &store.LauncherGuard{Enabled: true, WorkflowID: wf},
	})
	if err != nil {
		t.Fatalf("launcher: %v", err)
	}
	withGuard := srv.formatSessionHeaderWithContext(ctx, sess)
	if !strings.Contains(withGuard, guardPermitHintLine) {
		t.Fatalf("绑定且启用守护时应包含 guard 行\n%s", withGuard)
	}
	if strings.Count(withGuard, "guard:") != 1 {
		t.Fatalf("guard 行应只出现一次: %s", withGuard)
	}

	// 守护关闭：与 base 一致
	_, _, err = srv.store.UpsertTerminalLauncher(ctx, store.TerminalLauncher{
		ID: "lg1", Name: "G", Host: "RM-H3B4-5AW2-4DPP-ZAW6", Dir: "/tmp", Command: "agent",
		Guard: &store.LauncherGuard{Enabled: false, WorkflowID: wf},
	})
	if err != nil {
		t.Fatalf("launcher update: %v", err)
	}
	disabled := srv.formatSessionHeaderWithContext(ctx, sess)
	if disabled != base {
		t.Fatalf("守护关闭时回执头应与无绑定时逐字节一致\nbase:\n%s\ndisabled:\n%s", base, disabled)
	}
}
