package hookcli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestRunInstallCreatesHooksWhenMissing(t *testing.T) {
	goHookTestMu.Lock()
	defer goHookTestMu.Unlock()
	root := hookInstallRoot(t)
	proj := filepath.Join(root, "proj")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(root, "relaymesh-hook")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	cwd, _ := os.Getwd()
	if err := os.Chdir(proj); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(cwd) })

	if RunInstall(InstallOptions{Workflow: "wf-x", Command: bin}) != 0 {
		t.Fatal("install 应成功")
	}
	raw, err := os.ReadFile(filepath.Join(proj, ".cursor", "hooks.json"))
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if json.Unmarshal(raw, &doc) != nil {
		t.Fatal("hooks.json 应是合法 JSON")
	}
	hooks, _ := doc["hooks"].(map[string]any)
	if hooks == nil || len(hooks) == 0 {
		t.Fatalf("应登记 hook: %s", raw)
	}
}
