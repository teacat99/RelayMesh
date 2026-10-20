package hookcli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func fakeBinary(t *testing.T, dir string) string {
	t.Helper()
	p := filepath.Join(dir, "relaymesh-hook")
	if err := os.WriteFile(p, []byte("#!/bin/sh\necho ok\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func chdirProject(t *testing.T, project string) func() {
	t.Helper()
	wd, _ := os.Getwd()
	_ = os.Chdir(project)
	return func() { _ = os.Chdir(wd) }
}

func setHome(t *testing.T, home string) func() {
	t.Helper()
	return pushEnv(map[string]string{"HOME": home, "USERPROFILE": home})
}

func TestInstall_DryRunWritesNothing(t *testing.T) {
	s := newGoSandbox(t)
	defer chdirProject(t, s.project)()
	defer setHome(t, s.home)()
	bin := fakeBinary(t, s.tmp)
	if RunInstall(InstallOptions{Workflow: "wf-1", Command: bin, DryRun: true}) != 0 {
		t.Fatal("dry-run 应成功")
	}
	if _, err := os.Stat(filepath.Join(s.project, ".cursor")); !os.IsNotExist(err) {
		t.Fatal("预演不写文件")
	}
}

func TestInstall_FirstCreateNotEmptyHooksJSON(t *testing.T) {
	s := newGoSandbox(t)
	defer chdirProject(t, s.project)()
	defer setHome(t, s.home)()
	bin := fakeBinary(t, s.tmp)
	if RunInstall(InstallOptions{Workflow: "wf-1", Command: bin}) != 0 {
		t.Fatal("install 失败")
	}
	raw, _ := os.ReadFile(filepath.Join(s.project, ".cursor", "hooks.json"))
	if strings.TrimSpace(string(raw)) == "{}" {
		t.Fatal("回归：不能写出空 hooks.json")
	}
}

func TestInstall_PreservesOthersAndMigratesPython(t *testing.T) {
	s := newGoSandbox(t)
	defer chdirProject(t, s.project)()
	defer setHome(t, s.home)()
	bin := fakeBinary(t, s.tmp)
	hooksPath := filepath.Join(s.project, ".cursor", "hooks.json")
	s.writeJSON(hooksPath, map[string]any{
		"version": 1, "extra": "keep-me",
		"hooks": map[string]any{
			"preToolUse":  []any{map[string]any{"command": ".cursor/hooks/halt-guard.sh", "matcher": "Shell"}},
			"postToolUse": []any{map[string]any{"command": "python3 .cursor/hooks/relaymesh-inbox.py post-tool-use"}},
			"stop":        []any{map[string]any{"command": "python3 .cursor/hooks/relaymesh-inbox.py stop", "loop_limit": 1}},
		},
	})
	if RunInstall(InstallOptions{Workflow: "wf-1", Command: bin}) != 0 {
		t.Fatal("install 失败")
	}
	doc := loadJSON(hooksPath)
	if doc["extra"] != "keep-me" {
		t.Fatal("保留未知顶层键")
	}
	post := doc["hooks"].(map[string]any)["postToolUse"].([]any)
	if len(post) != 1 || !strings.Contains(post[0].(map[string]any)["command"].(string), "relaymesh-hook") {
		t.Fatalf("应迁移 Python 登记: %v", post)
	}
	pre := doc["hooks"].(map[string]any)["preToolUse"].([]any)
	if pre[0].(map[string]any)["command"] != ".cursor/hooks/halt-guard.sh" {
		t.Fatal("保留他人 hook")
	}
}

func TestInstall_IdempotentBytes(t *testing.T) {
	s := newGoSandbox(t)
	defer chdirProject(t, s.project)()
	defer setHome(t, s.home)()
	bin := fakeBinary(t, s.tmp)
	RunInstall(InstallOptions{Workflow: "wf-1", Command: bin, McpURL: "http://hub/mcp"})
	hooksPath := filepath.Join(s.project, ".cursor", "hooks.json")
	before, _ := os.ReadFile(hooksPath)
	if RunInstall(InstallOptions{Workflow: "wf-1", Command: bin, McpURL: "http://hub/mcp"}) != 0 {
		t.Fatal("第二次 install")
	}
	after, _ := os.ReadFile(hooksPath)
	if !bytes.Equal(before, after) {
		t.Fatal("重复安装不应改 hooks.json")
	}
}

func TestInstall_RefusesBrokenHooksJSON(t *testing.T) {
	s := newGoSandbox(t)
	defer chdirProject(t, s.project)()
	defer setHome(t, s.home)()
	bin := fakeBinary(t, s.tmp)
	hooksPath := filepath.Join(s.project, ".cursor", "hooks.json")
	_ = os.MkdirAll(filepath.Dir(hooksPath), 0o755)
	broken := []byte(`{ "hooks": { oops`)
	_ = os.WriteFile(hooksPath, broken, 0o644)
	if RunInstall(InstallOptions{Workflow: "wf-1", Command: bin}) == 0 {
		t.Fatal("坏 JSON 应拒绝")
	}
	got, _ := os.ReadFile(hooksPath)
	if !bytes.Equal(got, broken) {
		t.Fatal("不能覆盖坏文件")
	}
}

func TestInstall_UserScope(t *testing.T) {
	s := newGoSandbox(t)
	defer chdirProject(t, s.project)()
	defer setHome(t, s.home)()
	bin := fakeBinary(t, s.tmp)
	if RunInstall(InstallOptions{Workflow: "wf-1", Command: bin, UserScope: true}) != 0 {
		t.Fatal("user scope")
	}
	if _, err := os.Stat(filepath.Join(s.home, ".cursor", "hooks.json")); err != nil {
		t.Fatal("用户级 hooks.json")
	}
	if _, err := os.Stat(filepath.Join(s.project, ".cursor", "relaymesh.json")); !os.IsNotExist(err) {
		t.Fatal("user scope 不写项目 relaymesh.json")
	}
}

func TestInstall_CommandWithSpaces(t *testing.T) {
	s := newGoSandbox(t)
	defer chdirProject(t, s.project)()
	defer setHome(t, s.home)()
	bin := filepath.Join(s.tmp, "prog dir", "relaymesh-hook")
	_ = os.MkdirAll(filepath.Dir(bin), 0o755)
	_ = os.WriteFile(bin, []byte("#!/bin/sh\n"), 0o755)
	RunInstall(InstallOptions{Workflow: "wf-1", Command: bin})
	post := loadJSON(filepath.Join(s.project, ".cursor", "hooks.json"))
	cmd := post["hooks"].(map[string]any)["postToolUse"].([]any)[0].(map[string]any)["command"].(string)
	if !strings.HasPrefix(cmd, bin) {
		t.Fatalf("绝对路径含空格: %q", cmd)
	}
}

func TestUninstall_RemovesOnlyOurs(t *testing.T) {
	s := newGoSandbox(t)
	defer chdirProject(t, s.project)()
	defer setHome(t, s.home)()
	bin := fakeBinary(t, s.tmp)
	RunInstall(InstallOptions{Workflow: "wf-1", Command: bin})
	hooksPath := filepath.Join(s.project, ".cursor", "hooks.json")
	doc := loadJSON(hooksPath)
	hooks := doc["hooks"].(map[string]any)
	hooks["preToolUse"] = []any{map[string]any{"command": "keep.sh"}}
	_ = writeJSONAtomic(hooksPath, doc)
	RunUninstall(s.project, false, false)
	doc = loadJSON(hooksPath)
	if _, ok := doc["hooks"].(map[string]any)["postToolUse"]; ok {
		t.Fatal("应移除 postToolUse")
	}
	if pre := doc["hooks"].(map[string]any)["preToolUse"]; pre == nil {
		t.Fatal("保留 preToolUse")
	}
}

func TestDoctor_MissingBinary(t *testing.T) {
	s := newGoSandbox(t)
	defer chdirProject(t, s.project)()
	defer setHome(t, s.home)()
	bin := fakeBinary(t, s.tmp)
	RunInstall(InstallOptions{Workflow: "wf-1", Command: bin})
	_ = os.Chmod(bin, 0o644) // 不可执行
	out := captureStdout(t, func() { RunDoctor(s.project) })
	if !strings.Contains(out, "doctor") && !strings.Contains(out, "hooks.json") {
		t.Fatalf("doctor 应给出诊断: %s", out)
	}
}

func TestInstall_WindowsPathStyle(t *testing.T) {
	cmd := hookEntry("postToolUse", `C:\Users\me\bin\relaymesh-hook.exe`)
	c := cmd["command"].(string)
	if !strings.Contains(c, `\`) || !strings.Contains(c, ".exe") {
		t.Fatalf("Windows 路径登记: %q", c)
	}
}

func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w
	fn()
	_ = w.Close()
	os.Stdout = old
	var buf bytes.Buffer
	_, _ = buf.ReadFrom(r)
	return buf.String()
}

func TestInstall_RefusesATokenInMcpURL(t *testing.T) {
	for _, u := range []string{
		"http://127.0.0.1:1/mcp?token=" + urlSecret,
		"http://127.0.0.1:1/mcp/" + urlSecret,
		"http://127.0.0.1:1/mcp/token/" + urlSecret,
		"http://127.0.0.1:1/mcp/c/" + capSecret,
	} {
		s := newGoSandbox(t)
		bin := fakeBinary(t, s.tmp)
		so, se, code := s.runInstallCLI(s.project, nil, InstallOptions{Workflow: "wf-1", Command: bin, McpURL: u})
		combined := so + se
		if code == 0 || !strings.Contains(combined, "令牌") {
			t.Fatalf("带令牌的 --mcp-url 应被拒绝: %d\n%s", code, combined)
		}
		assertNoSecret(t, combined, "install 输出", urlSecret, capSecret)
		for _, p := range []string{
			filepath.Join(s.project, ".cursor", "relaymesh.json"),
			filepath.Join(s.project, ".cursor", "hooks.json"),
		} {
			if _, err := os.Stat(p); err == nil {
				t.Fatalf("拒绝后不应写 %s", p)
			}
		}
	}
	for _, u := range []string{"http://127.0.0.1:1/mcp", "http://127.0.0.1:1/mcp/"} {
		s := newGoSandbox(t)
		bin := fakeBinary(t, s.tmp)
		_, _, code := s.runInstallCLI(s.project, nil, InstallOptions{Workflow: "wf-1", Command: bin, McpURL: u})
		if code != 0 {
			t.Fatalf("无令牌 URL 应可安装: %s", u)
		}
		got := loadJSON(filepath.Join(s.project, ".cursor", "relaymesh.json"))
		if got["mcp_url"] != u {
			t.Fatalf("mcp_url 应原样写入: %v", got)
		}
	}
}

func TestInstall_ProjectRequiresWorkflow(t *testing.T) {
	s := newGoSandbox(t)
	defer chdirProject(t, s.project)()
	oldErr := os.Stderr
	r, w, _ := os.Pipe()
	os.Stderr = w
	code := RunInstall(InstallOptions{Command: fakeBinary(t, s.tmp)})
	_ = w.Close()
	os.Stderr = oldErr
	var se bytes.Buffer
	_, _ = se.ReadFrom(r)
	if code == 0 || !strings.Contains(se.String(), "--workflow") {
		t.Fatalf("缺 workflow 应失败: %d %s", code, se.String())
	}
}

func readHooksCommands(t *testing.T, path string) map[string][]map[string]any {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if json.Unmarshal(raw, &doc) != nil {
		t.Fatal("JSON")
	}
	hooks, _ := doc["hooks"].(map[string]any)
	out := map[string][]map[string]any{}
	for k, v := range hooks {
		lst, _ := v.([]any)
		for _, e := range lst {
			out[k] = append(out[k], e.(map[string]any))
		}
	}
	return out
}
