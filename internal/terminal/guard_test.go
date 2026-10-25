package terminal

import "testing"

func TestGuardNameKey(t *testing.T) {
	for in, want := range map[string]string{
		"Cursor CLI":     "cursor cli",
		"  cursor   cli": "cursor cli",
		"CURSOR\tCLI":    "cursor cli",
		"":               "",
	} {
		if got := guardNameKey(in); got != want {
			t.Errorf("guardNameKey(%q) = %q，期望 %q", in, got, want)
		}
	}
}
