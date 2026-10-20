package hookcli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

// 这些环境变量决定 client_kind / cursor_version / 项目目录。测试可能就跑在 Cursor 的终端或 CLI 里，
// 必须先清掉，再按用例设置，结果才不依赖跑测试的那台机器。
var clientEnvVars = []string{
	"CURSOR_INVOKED_AS", "CURSOR_CLI", "CURSOR_AGENT_CLI_LOCAL_MODE", "CURSOR_VERSION", "CURSOR_PROJECT_DIR",
	"VSCODE_IPC_HOOK_CLI", "VSCODE_PID", "TERM_PROGRAM", "CURSOR_USER_EMAIL",
}

func cleanClientEnv(sets map[string]string) func() {
	return pushEnv(sets, clientEnvVars...)
}

func envFunc(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func keysOf(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func TestEventSpecs_RegistryIsConsistent(t *testing.T) {
	if len(eventSpecs) != 10 {
		t.Fatalf("应登记 10 个事件，实际 %d", len(eventSpecs))
	}
	cursor, sub, wire := map[string]bool{}, map[string]bool{}, map[string]bool{}
	var core []string
	for _, spec := range eventSpecs {
		if spec.Cursor == "" || spec.Sub == "" || spec.Wire == "" {
			t.Fatalf("事件缺名字: %+v", spec)
		}
		if cursor[spec.Cursor] || sub[spec.Sub] || wire[spec.Wire] {
			t.Fatalf("事件名重复: %+v", spec)
		}
		cursor[spec.Cursor], sub[spec.Sub], wire[spec.Wire] = true, true, true
		if spec.Core {
			core = append(core, spec.Cursor)
			if spec.Async {
				t.Fatalf("核心事件要把 Hub 的回复交还给 Cursor，不能异步: %s", spec.Cursor)
			}
		} else if !spec.Async {
			t.Fatalf("状态事件不需要回包，必须走异步: %s", spec.Cursor)
		}
		if got := specForSub(spec.Sub); got == nil || got.Cursor != spec.Cursor {
			t.Fatalf("specForSub(%q) 不对", spec.Sub)
		}
		if got := specForCursor(spec.Cursor); got == nil || got.Sub != spec.Sub {
			t.Fatalf("specForCursor(%q) 不对", spec.Cursor)
		}
		if spec.Extra["timeout"] != float64(5) {
			t.Fatalf("%s 的 hook 超时应为 5 秒: %v", spec.Cursor, spec.Extra)
		}
		if !reportWireNames[spec.Wire] {
			t.Fatalf("子进程应接受上报名 %q", spec.Wire)
		}
	}
	if strings.Join(core, ",") != "postToolUse,stop" {
		t.Fatalf("核心事件应恰好是 postToolUse 与 stop: %v", core)
	}
	// 核心事件沿用旧事件名：旧版 Hub 只认 tool / stop。
	if specForCursor("postToolUse").Wire != "tool" || specForCursor("stop").Wire != "stop" {
		t.Fatal("postToolUse / stop 必须沿用旧上报名 tool / stop")
	}
	if specForCursor("stop").Extra["loop_limit"] != float64(1) {
		t.Fatal("stop 要保留 loop_limit: 1")
	}
	if out := specForCursor("beforeSubmitPrompt").Output; out["continue"] != true {
		t.Fatalf("beforeSubmitPrompt 必须明确放行: %v", out)
	}
	if specForSub("nope") != nil || specForCursor("nope") != nil {
		t.Fatal("未知事件应查不到")
	}
}

func TestHookEntry_CommandShapeForEveryEvent(t *testing.T) {
	for _, spec := range eventSpecs {
		entry := hookEntry(spec.Cursor, "/opt/relaymesh-hook")
		if entry["command"] != "/opt/relaymesh-hook "+spec.Sub {
			t.Fatalf("%s: %v", spec.Cursor, entry)
		}
		if entry["timeout"] != float64(5) {
			t.Fatalf("%s 缺 timeout: %v", spec.Cursor, entry)
		}
		if _, has := entry["loop_limit"]; has != (spec.Cursor == "stop") {
			t.Fatalf("只有 stop 带 loop_limit: %s %v", spec.Cursor, entry)
		}
	}
}

func richPayload(root string, extra map[string]any) map[string]any {
	m := map[string]any{
		"conversation_id": "conv-7", "generation_id": "gen-1", "model": "some-model",
		"cursor_version": "3.2.1", "workspace_roots": []any{root}, "cwd": root,
		"transcript_path": "/home/u/.cursor/transcripts/x.jsonl",
		"user_email":      "someone@example.com", "email": "someone@example.com",
		"prompt": "PROMPT-BODY-SECRET", "text": "THOUGHT-BODY-SECRET", "response": "RESPONSE-BODY-SECRET",
		"tool_name": "Shell", "tool_input": map[string]any{"command": "cat /etc/shadow-SECRET"}, "tool_output": "TOOL-OUTPUT-SECRET",
		"command": "cat secrets-SECRET", "file_path": "/etc/passwd-SECRET", "duration": 1234.0,
		"status": "completed", "loop_count": 0, "reason": "window_close", "final_status": "completed",
	}
	for k, v := range extra {
		m[k] = v
	}
	return m
}

func TestBuildEventReport_FieldsPerEvent(t *testing.T) {
	defer cleanClientEnv(nil)()
	root := "/work/proj"
	cfgPath := filepath.Join(root, ".cursor", "relaymesh.json")
	now := time.UnixMilli(1760000000123)
	common := []string{"agent_event", "agent_ts", "conversation_id", "cursor_version", "project_directory"}
	cases := []struct {
		cursor string
		wire   string
		extra  []string
		check  func(t *testing.T, r map[string]any)
	}{
		{"postToolUse", "tool", []string{"agent_tool", "agent_duration_ms"}, func(t *testing.T, r map[string]any) {
			if r["agent_tool"] != "Shell" || r["agent_duration_ms"] != int64(1234) {
				t.Fatalf("%v", r)
			}
		}},
		{"stop", "stop", []string{"agent_status"}, func(t *testing.T, r map[string]any) {
			if r["agent_status"] != "completed" {
				t.Fatalf("%v", r)
			}
		}},
		{"sessionStart", "session_start", nil, nil},
		{"sessionEnd", "session_end", []string{"agent_status"}, func(t *testing.T, r map[string]any) {
			if r["agent_status"] != "window_close" {
				t.Fatalf("结束原因应取 reason: %v", r)
			}
		}},
		{"beforeSubmitPrompt", "prompt", nil, nil},
		{"afterAgentThought", "thought", nil, nil},
		{"preToolUse", "tool_start", []string{"agent_tool"}, func(t *testing.T, r map[string]any) {
			if r["agent_tool"] != "Shell" {
				t.Fatalf("%v", r)
			}
		}},
		{"afterAgentResponse", "response", nil, nil},
		{"postToolUseFailure", "tool_fail", []string{"agent_tool", "agent_duration_ms"}, nil},
		{"preCompact", "compact", nil, nil},
	}
	if len(cases) != len(eventSpecs) {
		t.Fatalf("用例要覆盖全部 %d 个事件", len(eventSpecs))
	}
	for _, c := range cases {
		spec := specForCursor(c.cursor)
		if spec == nil {
			t.Fatalf("没有事件 %s", c.cursor)
		}
		r := buildEventReport(*spec, richPayload(root, nil), cfgPath, now)
		want := append(append([]string(nil), common...), c.extra...)
		sort.Strings(want)
		if got := keysOf(r); strings.Join(got, ",") != strings.Join(want, ",") {
			t.Fatalf("%s 的字段应恰好是 %v，实际 %v", c.cursor, want, got)
		}
		if r["agent_event"] != c.wire || r["agent_ts"] != int64(1760000000123) || r["conversation_id"] != "conv-7" ||
			r["cursor_version"] != "3.2.1" || r["project_directory"] != root {
			t.Fatalf("%s 的公共字段不对: %v", c.cursor, r)
		}
		if c.check != nil {
			c.check(t, r)
		}
		raw, _ := json.Marshal(r)
		for _, secret := range []string{"someone@example.com", "SECRET", "transcripts", "gen-1", "some-model", "/etc/passwd"} {
			if strings.Contains(string(raw), secret) {
				t.Fatalf("%s 的上报泄露了 %q: %s", c.cursor, secret, raw)
			}
		}
	}
}

func TestBuildEventReport_NeverReadsTheEmailVariable(t *testing.T) {
	defer cleanClientEnv(map[string]string{"CURSOR_USER_EMAIL": "leak-me@example.com", "CURSOR_INVOKED_AS": "agent"})()
	for _, spec := range eventSpecs {
		r := buildEventReport(spec, richPayload("/work/proj", nil), "", time.Now())
		raw, _ := json.Marshal(r)
		if strings.Contains(string(raw), "leak-me@example.com") || strings.Contains(string(raw), "@") {
			t.Fatalf("%s: 上报里不能出现邮箱: %s", spec.Cursor, raw)
		}
	}
}

// 静态守卫：除了 async.go 里把它从子进程环境里去掉那一处，任何源文件都不许提到 CURSOR_USER_EMAIL。
func TestSources_NeverTouchTheEmailVariable(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil || len(files) == 0 {
		t.Fatalf("找不到源文件: %v", err)
	}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		src := string(b)
		n := strings.Count(src, "CURSOR_USER_EMAIL")
		if f == "async.go" {
			if n != 1 {
				t.Fatalf("async.go 里只该有 childEnv 去掉它的那一处，实际 %d 处", n)
			}
			for _, line := range strings.Split(src, "\n") {
				if strings.Contains(line, "CURSOR_USER_EMAIL") && !strings.Contains(line, "HasPrefix") {
					t.Fatalf("async.go 只能用来过滤，不能读取: %s", line)
				}
			}
			continue
		}
		if n != 0 {
			t.Fatalf("%s 不许碰 CURSOR_USER_EMAIL", f)
		}
		if strings.Contains(src, "user_email") || strings.Contains(src, `"email"`) {
			t.Fatalf("%s 不许读取载荷里的邮箱字段", f)
		}
	}
}

func TestBuildEventReport_StopStatusMatchesV1(t *testing.T) {
	defer cleanClientEnv(nil)()
	stop := *specForCursor("stop")
	cases := []struct {
		name    string
		payload map[string]any
		want    any
		has     bool
	}{
		{"缺 status 按 completed", map[string]any{}, "completed", true},
		{"completed", map[string]any{"status": "completed"}, "completed", true},
		{"大小写与空白", map[string]any{"status": "  ABORTED "}, "aborted", true},
		{"超长截成 16", map[string]any{"status": strings.Repeat("x", 40)}, strings.Repeat("x", 16), true},
		{"非字符串不报", map[string]any{"status": 3.0}, nil, false},
		{"null 不报", map[string]any{"status": nil}, nil, false},
	}
	for _, c := range cases {
		r := buildEventReport(stop, c.payload, "", time.Now())
		got, has := r["agent_status"]
		if has != c.has || (has && got != c.want) {
			t.Fatalf("%s: %v", c.name, r)
		}
	}
}

func TestBuildEventReport_SessionEndReason(t *testing.T) {
	defer cleanClientEnv(nil)()
	end := *specForCursor("sessionEnd")
	cases := []struct {
		name    string
		payload map[string]any
		want    string
	}{
		{"取 reason", map[string]any{"reason": "window_close", "final_status": "completed"}, "window_close"},
		{"没有 reason 取 final_status", map[string]any{"final_status": "aborted"}, "aborted"},
		{"连字符与空格变下划线", map[string]any{"reason": "User-Closed win"}, "user_closed_win"},
		{"夹带符号被剔除", map[string]any{"reason": "bye!!;rm -rf"}, "byerm__rf"},
		{"超长截成 16", map[string]any{"reason": strings.Repeat("a", 50)}, strings.Repeat("a", 16)},
		{"都没有就不报", map[string]any{}, ""},
		{"非字符串不报", map[string]any{"reason": 5.0}, ""},
	}
	for _, c := range cases {
		r := buildEventReport(end, c.payload, "", time.Now())
		got, _ := r["agent_status"].(string)
		if got != c.want {
			t.Fatalf("%s: 想要 %q 得到 %q", c.name, c.want, got)
		}
	}
}

func TestCleanTool(t *testing.T) {
	cases := []struct {
		in   any
		want string
	}{
		{"Shell", "Shell"},
		{"  Read File ", "Read_File"},
		{"mcp:relaymesh/get_info", "mcp:relaymesh_get_info"},
		{"带中文?;|&", "_______"},
		{strings.Repeat("a", 100), strings.Repeat("a", 64)},
		{"", ""},
		{"   ", ""},
		{12, ""},
		{nil, ""},
	}
	for _, c := range cases {
		if got := cleanTool(c.in); got != c.want {
			t.Fatalf("cleanTool(%v) = %q，想要 %q", c.in, got, c.want)
		}
	}
	// 非 ASCII 字符一律换成下划线，所以截断永远不会把一个多字节字符切成两半。
	got := cleanTool(strings.Repeat("a", 63) + "文字")
	if got != strings.Repeat("a", 63)+"_" {
		t.Fatalf("截断: %q", got)
	}
}

func TestDurationMS(t *testing.T) {
	cases := []struct {
		in   any
		want int64
		ok   bool
	}{
		{1234.0, 1234, true},
		{0.5, 0, true},
		{0.0, 0, false},
		{-5.0, 0, false},
		{float64(25 * 3600 * 1000), 0, false},
		{"1234", 0, false},
		{nil, 0, false},
	}
	for _, c := range cases {
		got, ok := durationMS(c.in)
		if ok != c.ok || (ok && got != c.want) {
			t.Fatalf("durationMS(%v) = %d,%v", c.in, got, ok)
		}
	}
}

func TestCursorVersion(t *testing.T) {
	env := envFunc(map[string]string{"CURSOR_VERSION": "9.9.9"})
	cases := []struct {
		name    string
		payload map[string]any
		want    string
	}{
		{"载荷优先", map[string]any{"cursor_version": "3.2.1"}, "3.2.1"},
		{"载荷没有取环境变量", map[string]any{}, "9.9.9"},
		{"载荷不合格取环境变量", map[string]any{"cursor_version": "bad version!"}, "9.9.9"},
		{"载荷不是字符串", map[string]any{"cursor_version": 3}, "9.9.9"},
		{"带 + 与 _ 的合法版本", map[string]any{"cursor_version": "3.2.1+build_4"}, "3.2.1+build_4"},
	}
	for _, c := range cases {
		if got := cursorVersion(c.payload, env); got != c.want {
			t.Fatalf("%s: %q", c.name, got)
		}
	}
	empty := envFunc(nil)
	for _, bad := range []string{"", " ", "1 2", "<script>", strings.Repeat("1", 33), "v1;rm", "版本"} {
		if got := cursorVersion(map[string]any{"cursor_version": bad}, empty); got != "" {
			t.Fatalf("%q 不合格，应当丢弃，得到 %q", bad, got)
		}
	}
	if got := cursorVersion(map[string]any{}, envFunc(map[string]string{"CURSOR_VERSION": "bad value"})); got != "" {
		t.Fatalf("环境变量不合格也要丢: %q", got)
	}
}

func TestDetectClientKind(t *testing.T) {
	cases := []struct {
		name string
		env  map[string]string
		want string
	}{
		{"什么都没有", nil, ""},
		{"agent 启动脚本", map[string]string{"CURSOR_INVOKED_AS": "agent"}, "cli"},
		{"CLI 标记", map[string]string{"CURSOR_CLI": "1"}, "cli"},
		{"CLI 本地模式", map[string]string{"CURSOR_AGENT_CLI_LOCAL_MODE": "true"}, "cli"},
		{"本地模式为 false 不算", map[string]string{"CURSOR_AGENT_CLI_LOCAL_MODE": "false"}, ""},
		{"VS Code 系变量", map[string]string{"VSCODE_PID": "123"}, "ide"},
		{"VS Code IPC", map[string]string{"VSCODE_IPC_HOOK_CLI": "/tmp/x.sock"}, "ide"},
		{"终端程序是 vscode", map[string]string{"TERM_PROGRAM": "vscode"}, "ide"},
		{"别的终端程序不算", map[string]string{"TERM_PROGRAM": "iTerm.app"}, ""},
		{"在 IDE 终端里跑 CLI 以 CLI 为准", map[string]string{"CURSOR_INVOKED_AS": "agent", "TERM_PROGRAM": "vscode", "VSCODE_PID": "1"}, "cli"},
	}
	for _, c := range cases {
		if got := detectClientKind(envFunc(c.env)); got != c.want {
			t.Fatalf("%s: %q", c.name, got)
		}
	}
}

func TestProjectDirectory(t *testing.T) {
	roots := func(rs ...string) map[string]any {
		out := make([]any, 0, len(rs))
		for _, r := range rs {
			out = append(out, r)
		}
		return map[string]any{"workspace_roots": out}
	}
	cases := []struct {
		name    string
		payload map[string]any
		cfg     string
		env     map[string]string
		want    string
	}{
		{"配置所在的工作区优先", roots("/a/other", "/a/proj"), "/a/proj/.cursor/relaymesh.json", nil, "/a/proj"},
		{"配置在工作区的父目录", roots("/a/other", "/a/proj/sub"), "/a/proj/.cursor/relaymesh.json", nil, "/a/proj/sub"},
		{"没有配置取第一个", roots("/a/one", "/a/two"), "", nil, "/a/one"},
		{"配置不在任何工作区取第一个", roots("/a/one"), "/z/proj/.cursor/relaymesh.json", nil, "/a/one"},
		{"相对路径被丢弃", roots("rel/path", "/a/abs"), "", nil, "/a/abs"},
		{"含控制字符被丢弃", roots("/a/bad\npath", "/a/ok"), "", nil, "/a/ok"},
		{"超长被丢弃", roots("/" + strings.Repeat("d", 600)), "", nil, ""},
		{"没有工作区取 CURSOR_PROJECT_DIR", map[string]any{}, "", map[string]string{"CURSOR_PROJECT_DIR": "/env/proj"}, "/env/proj"},
		{"环境变量不是绝对路径", map[string]any{}, "", map[string]string{"CURSOR_PROJECT_DIR": "proj"}, ""},
		{"都没有", map[string]any{}, "", nil, ""},
		{"工作区不是数组", map[string]any{"workspace_roots": "/a/one"}, "", nil, ""},
	}
	for _, c := range cases {
		if got := projectDirectory(c.payload, c.cfg, envFunc(c.env)); got != c.want {
			t.Fatalf("%s: %q，想要 %q", c.name, got, c.want)
		}
	}
}

func TestSanitizeReport(t *testing.T) {
	good := map[string]any{
		"agent_event": "tool", "conversation_id": "c1", "agent_status": "Completed ", "agent_tool": "Shell",
		"agent_ts": 1760000000123.0, "agent_duration_ms": 50.0, "client_kind": "cli", "cursor_version": "3.2.1",
		"project_directory": "/work/proj",
	}
	out := sanitizeReport(good)
	if out["agent_event"] != "tool" || out["conversation_id"] != "c1" || out["agent_status"] != "completed" ||
		out["agent_tool"] != "Shell" || out["agent_ts"] != int64(1760000000123) || out["agent_duration_ms"] != int64(50) ||
		out["client_kind"] != "cli" || out["cursor_version"] != "3.2.1" || out["project_directory"] != "/work/proj" {
		t.Fatalf("合法字段应原样通过: %v", out)
	}

	if sanitizeReport(map[string]any{"agent_event": "rm -rf"}) != nil || sanitizeReport(map[string]any{}) != nil ||
		sanitizeReport(map[string]any{"agent_event": 7}) != nil {
		t.Fatal("未知事件名应整条丢弃")
	}

	dirty := map[string]any{
		"agent_event": "thought",
		"prompt":      "PROMPT-SECRET", "text": "THOUGHT-SECRET", "tool_input": map[string]any{"a": 1},
		"action": "inbox", "workflow_id": "other-workflow", "source": "evil", "reminder": true, "token": "TOKEN-SECRET",
		"client_kind": "toaster", "cursor_version": "bad version", "project_directory": "relative/path",
		"agent_ts": -1.0, "agent_duration_ms": -3.0, "agent_tool": "   ",
	}
	out = sanitizeReport(dirty)
	if strings.Join(keysOf(out), ",") != "agent_event" {
		t.Fatalf("多余与非法字段都应被丢弃: %v", out)
	}
	raw, _ := json.Marshal(out)
	for _, secret := range []string{"SECRET", "other-workflow", "evil"} {
		if strings.Contains(string(raw), secret) {
			t.Fatalf("泄露 %q: %s", secret, raw)
		}
	}

	if r := sanitizeReport(map[string]any{"agent_event": "tool", "agent_ts": 1e16}); r["agent_ts"] != nil {
		t.Fatalf("不合理的时间应丢弃: %v", r)
	}
	long := sanitizeReport(map[string]any{"agent_event": "tool", "conversation_id": strings.Repeat("c", 90)})
	if len(long["conversation_id"].(string)) != 64 {
		t.Fatalf("会话 ID 截成 64: %v", long)
	}
}
