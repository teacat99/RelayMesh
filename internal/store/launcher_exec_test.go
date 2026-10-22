package store

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestClassifyLauncherExecCases(t *testing.T) {
	_, file, _, _ := runtime.Caller(0)
	raw, err := os.ReadFile(filepath.Join(filepath.Dir(file), "testdata/launcher_exec_cases.json"))
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		Name    string `json:"name"`
		Command string `json:"command"`
		OK      bool   `json:"ok"`
		Exec    string `json:"exec"`
	}
	if err := json.Unmarshal(raw, &cases); err != nil {
		t.Fatal(err)
	}
	for _, c := range cases {
		t.Run(c.Name, func(t *testing.T) {
			got, err := ClassifyLauncherExec(c.Command)
			if c.OK {
				if err != nil {
					t.Fatalf("unexpected err: %v", err)
				}
				if got != c.Exec {
					t.Fatalf("exec line: got %q want %q", got, c.Exec)
				}
			} else if err == nil {
				t.Fatalf("expected error, got %q", got)
			}
		})
	}
}
