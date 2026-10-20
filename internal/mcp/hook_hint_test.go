package mcp

import (
	"strings"
	"testing"
)

func TestFormatHookInstallHint(t *testing.T) {
	text := formatHookInstallHint("http://hub.test:18775", "wf-abc")
	for _, want := range []string{"hook:", "wf-abc", "/hooks/cursor/install.sh"} {
		if !strings.Contains(text, want) {
			t.Fatalf("提示应含 %q: %q", want, text)
		}
	}
	if strings.Contains(text, "Bearer") {
		t.Fatal("提示不能含 Bearer")
	}
}
