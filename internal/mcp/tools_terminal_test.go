package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/teacat99/RelayMesh/v2/internal/model"
	"github.com/teacat99/RelayMesh/v2/internal/termexec"
)

// fakeExecutor 记录 MCP 层交给 Hub 的参数，并按需返回预置的结果 / 错误。
type fakeExecutor struct {
	mu        sync.Mutex
	callers   []termexec.Caller
	targets   []string
	execs     []termexec.ExecRequest
	runs      []termexec.RunRequest
	execRes   any
	execErr   error
	runRes    any
	targetRes any
}

func (f *fakeExecutor) Targets(_ context.Context, c termexec.Caller, target string) (any, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.callers = append(f.callers, c)
	f.targets = append(f.targets, target)
	return f.targetRes, nil
}

func (f *fakeExecutor) Exec(_ context.Context, c termexec.Caller, r termexec.ExecRequest) (any, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.callers = append(f.callers, c)
	f.execs = append(f.execs, r)
	return f.execRes, f.execErr
}

func (f *fakeExecutor) Run(_ context.Context, c termexec.Caller, r termexec.RunRequest) (any, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.callers = append(f.callers, c)
	f.runs = append(f.runs, r)
	return f.runRes, nil
}

func newTerminalCredential(t *testing.T, srv *Server, name string, perms model.Permissions) *model.MCPCredential {
	t.Helper()
	c := &model.MCPCredential{Name: name, Permissions: perms}
	if err := srv.store.CreateCredential(context.Background(), c); err != nil {
		t.Fatalf("创建令牌 %s: %v", name, err)
	}
	return c
}

// callTool 通过 HTTP 入口调用工具，返回文本内容与 isError。
func callTool(t *testing.T, srv *Server, token, name, argsJSON string) (string, bool) {
	t.Helper()
	body := fmt.Sprintf(`{"jsonrpc":"2.0","id":9,"method":"tools/call","params":{"name":%q,"arguments":%s}}`, name, argsJSON)
	code, resp := rpcFrom(t, srv, token, "198.51.100.7:4040", body)
	if code != 200 || resp.Error != nil {
		t.Fatalf("tools/call %s: %d %#v", name, code, resp.Error)
	}
	res, _ := resp.Result.(map[string]any)
	content, _ := res["content"].([]any)
	if len(content) == 0 {
		t.Fatalf("没有内容: %#v", resp.Result)
	}
	first, _ := content[0].(map[string]any)
	text, _ := first["text"].(string)
	isErr, _ := res["isError"].(bool)
	return text, isErr
}

func toolNames(t *testing.T, srv *Server, token string) map[string]map[string]any {
	t.Helper()
	code, resp := rpcFrom(t, srv, token, "", `{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`)
	if code != 200 || resp.Error != nil {
		t.Fatalf("tools/list: %d %#v", code, resp.Error)
	}
	res, _ := resp.Result.(map[string]any)
	list, _ := res["tools"].([]any)
	out := map[string]map[string]any{}
	for _, it := range list {
		m, _ := it.(map[string]any)
		out[m["name"].(string)] = m
	}
	return out
}

func requiredOf(tool map[string]any) []string {
	schema, _ := tool["inputSchema"].(map[string]any)
	raw, _ := schema["required"].([]any)
	var out []string
	for _, r := range raw {
		out = append(out, r.(string))
	}
	sort.Strings(out)
	return out
}

func TestTerminalToolsListByTier(t *testing.T) {
	srv := setupTestMCPServer(t)
	relay := newTerminalCredential(t, srv, "relay", model.Permissions{Feedback: true, Sessions: true})
	exec := newTerminalCredential(t, srv, "exec", model.Permissions{Terminal: true})
	hybrid := newTerminalCredential(t, srv, "hybrid", model.Permissions{Feedback: true, Sessions: true, Terminal: true})

	if tools := toolNames(t, srv, relay.Token); tools["terminal_exec"] != nil || tools["terminal_run"] != nil || tools["terminal_targets"] != nil {
		t.Fatalf("RelayMesh 档不应看到终端工具: %v", keys(tools))
	}

	et := toolNames(t, srv, exec.Token)
	if len(et) != 3 || et["terminal_targets"] == nil || et["terminal_exec"] == nil || et["terminal_run"] == nil {
		t.Fatalf("ExecMesh 档应恰好有三个终端工具: %v", keys(et))
	}
	if got := strings.Join(requiredOf(et["terminal_exec"]), ","); got != "command,target" {
		t.Fatalf("ExecMesh 档 terminal_exec.required = %s", got)
	}

	ht := toolNames(t, srv, hybrid.Token)
	if ht["terminal_exec"] == nil || ht["interactive_feedback"] == nil {
		t.Fatalf("混合档应同时有治理与终端工具: %v", keys(ht))
	}
	if got := strings.Join(requiredOf(ht["terminal_exec"]), ","); got != "command,description,target,workflow_id" {
		t.Fatalf("混合档 terminal_exec.required = %s", got)
	}
	for _, name := range []string{"terminal_targets", "terminal_exec", "terminal_run"} {
		desc, _ := ht[name]["description"].(string)
		if strings.TrimSpace(desc) == "" {
			t.Fatalf("%s 缺少描述", name)
		}
		if _, ok := ht[name]["inputSchema"].(map[string]any); !ok {
			t.Fatalf("%s 缺少 inputSchema", name)
		}
	}
}

func keys(m map[string]map[string]any) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func TestTerminalCallsReachExecutorWithParsedArguments(t *testing.T) {
	srv := setupTestMCPServer(t)
	fx := &fakeExecutor{
		execRes:   map[string]any{"run_id": "abc", "state": "succeeded"},
		runRes:    map[string]any{"state": "running"},
		targetRes: map[string]any{"targets": []any{}},
	}
	srv.SetTerminalExecutor(fx)
	cred := newTerminalCredential(t, srv, "exec", model.Permissions{Terminal: true})

	// 数字参数的各种写法都应被接受；缺省的 wait_seconds 要被区分出来。
	text, isErr := callTool(t, srv, cred.Token, "terminal_exec",
		`{"target":"web","command":"ls -la","description":"look","thread":"t1","workflow_id":"wf","project_directory":"/p","cwd":"/srv","shell":"bash",`+
			`"timeout_seconds":"30","wait_seconds":5.0,"max_output_bytes":2048,"idempotency_key":"k1"}`)
	if isErr || !strings.Contains(text, `"run_id": "abc"`) {
		t.Fatalf("exec 结果 = %v %s", isErr, text)
	}
	got := fx.execs[0]
	want := termexec.ExecRequest{Target: "web", Command: "ls -la", Description: "look", Thread: "t1", WorkflowID: "wf", ProjectDirectory: "/p",
		Cwd: "/srv", Shell: "bash", TimeoutSeconds: 30, WaitSeconds: 5, WaitSet: true, MaxOutputBytes: 2048, IdempotencyKey: "k1"}
	if got != want {
		t.Fatalf("交给执行器的参数 = %+v\n期望 %+v", got, want)
	}
	c := fx.callers[0]
	if c.CredentialID != cred.ID || c.CredentialName != "exec" || c.TokenString != cred.Token || c.Credential == nil || !c.Permissions.Terminal ||
		c.RemoteIP == "" {
		t.Fatalf("Caller = %+v", c)
	}

	callTool(t, srv, cred.Token, "terminal_exec", `{"target":"web","command":"id"}`)
	if r := fx.execs[1]; r.WaitSet || r.WaitSeconds != 0 {
		t.Fatalf("未提供 wait_seconds 时 WaitSet 应为 false: %+v", r)
	}
	callTool(t, srv, cred.Token, "terminal_exec", `{"target":"web","command":"id","wait_seconds":0}`)
	if r := fx.execs[2]; !r.WaitSet || r.WaitSeconds != 0 {
		t.Fatalf("显式的 wait_seconds=0 要保留（立即返回）: %+v", r)
	}

	callTool(t, srv, cred.Token, "terminal_run", `{"action":"output","run_id":"r1","stream":"stderr","offset":"100","limit":4096.0,"wait_seconds":"2"}`)
	if r := fx.runs[0]; r != (termexec.RunRequest{Action: "output", RunID: "r1", Stream: "stderr", Offset: 100, Limit: 4096, WaitSeconds: 2}) {
		t.Fatalf("run 参数 = %+v", r)
	}

	callTool(t, srv, cred.Token, "terminal_targets", `{}`)
	callTool(t, srv, cred.Token, "terminal_targets", `{"target":"web"}`)
	if len(fx.targets) != 2 || fx.targets[0] != "" || fx.targets[1] != "web" {
		t.Fatalf("targets 参数 = %v", fx.targets)
	}
}

func TestTerminalCallErrorsAreStructured(t *testing.T) {
	srv := setupTestMCPServer(t)
	cred := newTerminalCredential(t, srv, "exec", model.Permissions{Terminal: true})

	// 未注入执行器：终端功能不可用。
	text, isErr := callTool(t, srv, cred.Token, "terminal_exec", `{"target":"web","command":"id"}`)
	if !isErr || !strings.Contains(text, `"code": "`+termexec.CodeDisabled+`"`) {
		t.Fatalf("未注入时应返回 exec_disabled: %v %s", isErr, text)
	}

	fx := &fakeExecutor{execErr: &termexec.Error{Code: termexec.CodePaused, Message: "模型执行已被暂停"}}
	srv.SetTerminalExecutor(fx)
	text, isErr = callTool(t, srv, cred.Token, "terminal_exec", `{"target":"web","command":"id"}`)
	var payload struct {
		Error struct{ Code, Message string }
	}
	if err := json.Unmarshal([]byte(text), &payload); err != nil || !isErr || payload.Error.Code != termexec.CodePaused || payload.Error.Message == "" {
		t.Fatalf("业务错误应带稳定错误码: %v %s", isErr, text)
	}

	// 未知类型的错误只暴露为普通错误文本。
	fx.execErr = errors.New("boom")
	text, isErr = callTool(t, srv, cred.Token, "terminal_exec", `{"target":"web","command":"id"}`)
	if !isErr || !strings.Contains(text, "boom") {
		t.Fatalf("其他错误 = %v %s", isErr, text)
	}

	// 参数无法解析：bad_request，且不会触达执行器。
	before := len(fx.execs)
	text, isErr = callTool(t, srv, cred.Token, "terminal_exec", `{"target":"web","command":"id","timeout_seconds":"soon"}`)
	if !isErr || !strings.Contains(text, termexec.CodeBadRequest) || len(fx.execs) != before {
		t.Fatalf("非法参数 = %v %s", isErr, text)
	}
}

func TestTerminalToolsRequireTerminalPermission(t *testing.T) {
	srv := setupTestMCPServer(t)
	fx := &fakeExecutor{execRes: map[string]any{"ok": true}}
	srv.SetTerminalExecutor(fx)
	relay := newTerminalCredential(t, srv, "relay", model.Permissions{Feedback: true, Sessions: true})

	for _, token := range []string{relay.Token, "cfg-token-123456", "exec-token-123456", ""} {
		for _, tool := range []string{"terminal_targets", "terminal_exec", "terminal_run"} {
			args := `{"target":"web","command":"id","action":"status","run_id":"x"}`
			body := fmt.Sprintf(`{"jsonrpc":"2.0","id":9,"method":"tools/call","params":{"name":%q,"arguments":%s}}`, tool, args)
			_, resp := rpcFrom(t, srv, token, "", body)
			res, _ := resp.Result.(map[string]any)
			if resp.Error == nil && res["isError"] != true {
				t.Fatalf("令牌 %q 不应能调用 %s: %#v", token, tool, resp.Result)
			}
		}
	}
	if len(fx.callers) != 0 {
		t.Fatalf("没有终端权限的调用不得到达执行器（%d 次）", len(fx.callers))
	}
}

func TestFlexInt(t *testing.T) {
	cases := []struct {
		in      string
		want    int
		set     bool
		wantErr bool
	}{
		{`30`, 30, true, false},
		{`30.0`, 30, true, false},
		{`"30"`, 30, true, false},
		{`" 30 "`, 0, false, true},
		{`1e2`, 100, true, false},
		{`null`, 0, false, false},
		{`""`, 0, false, false},
		{`-5`, -5, true, false},
		{`9999999999999999`, 1 << 40, true, false},
		{`"abc"`, 0, false, true},
		{`true`, 0, false, true},
	}
	for _, tc := range cases {
		var f flexInt
		err := f.UnmarshalJSON([]byte(tc.in))
		if (err != nil) != tc.wantErr || f.V != tc.want || f.Set != tc.set {
			t.Errorf("flexInt(%s) = {%d %v} err=%v，期望 {%d %v} err=%v", tc.in, f.V, f.Set, err, tc.want, tc.set, tc.wantErr)
		}
	}
}
