package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"testing"

	"github.com/teacat99/RelayMesh/v2/internal/model"
	"github.com/teacat99/RelayMesh/v2/internal/termexec"
	"github.com/teacat99/RelayMesh/v2/internal/termproto"
)

// callJSON 调用工具并把文本内容解析成 JSON 对象。
func callJSON(t *testing.T, srv *Server, token, name, args string) (map[string]any, bool) {
	t.Helper()
	text, isErr := callTool(t, srv, token, name, args)
	var out map[string]any
	if err := json.Unmarshal([]byte(text), &out); err != nil {
		t.Fatalf("%s 的结果不是 JSON 对象: %v\n%s", name, err, text)
	}
	return out, isErr
}

func errCodeOf(m map[string]any) string {
	e, _ := m["error"].(map[string]any)
	c, _ := e["code"].(string)
	return c
}

func TestCompatToolsListAndGating(t *testing.T) {
	srv := setupTestMCPServer(t)
	fx := &fakeExecutor{execRes: map[string]any{"ok": true}, runRes: map[string]any{"ok": true}, targetRes: map[string]any{"ok": true}}
	srv.SetTerminalExecutor(fx)

	native := newTerminalCredential(t, srv, "native", model.Permissions{Terminal: true})
	compat := newTerminalCredential(t, srv, "compat", model.Permissions{Terminal: true, TerminalCompat: true})
	hybridCompat := newTerminalCredential(t, srv, "hybrid-compat", model.Permissions{Feedback: true, Sessions: true, Terminal: true, TerminalCompat: true})

	// 兼容令牌只看到 ExecMesh 的名字，看不到原生名字，也看不到治理工具。
	ct := toolNames(t, srv, compat.Token)
	want := CompatToolNames()
	sort.Strings(want)
	if got := keys(ct); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("兼容命名的 tools/list = %v，期望 %v", got, want)
	}
	for name, tool := range ct {
		if d, _ := tool["description"].(string); strings.TrimSpace(d) == "" {
			t.Fatalf("%s 缺少描述", name)
		}
		if _, ok := tool["inputSchema"].(map[string]any); !ok {
			t.Fatalf("%s 缺少 inputSchema", name)
		}
	}
	if got := strings.Join(requiredOf(ct["exec"]), ","); got != "command,target" {
		t.Fatalf("exec.required = %s", got)
	}
	if got := strings.Join(requiredOf(ct["exec_start"]), ","); got != "command,target" {
		t.Fatalf("exec_start.required = %s", got)
	}
	if got := strings.Join(requiredOf(ct["job_status"]), ","); got != "job_id" {
		t.Fatalf("job_status.required = %s", got)
	}
	if schema, _ := ct["exec"]["inputSchema"].(map[string]any); schema["properties"].(map[string]any)["env"] != nil {
		t.Fatalf("exec 的模式里不应出现 env（不支持的参数不能出现在模式里）")
	}

	// 原生令牌不受影响。
	nt := toolNames(t, srv, native.Token)
	if len(nt) != 3 || nt["terminal_exec"] == nil || nt["exec"] != nil {
		t.Fatalf("原生令牌的 tools/list = %v", keys(nt))
	}

	// 混合 + 兼容：治理工具与兼容名并存，exec / exec_start 要求 workflow_id 与 description。
	ht := toolNames(t, srv, hybridCompat.Token)
	if ht["interactive_feedback"] == nil || ht["exec"] == nil || ht["terminal_exec"] != nil {
		t.Fatalf("混合兼容令牌的 tools/list = %v", keys(ht))
	}
	for _, name := range []string{"exec", "exec_start"} {
		if got := strings.Join(requiredOf(ht[name]), ","); got != "command,description,target,workflow_id" {
			t.Fatalf("混合兼容令牌 %s.required = %s", name, got)
		}
	}

	// tools/call 与 tools/list 由同一个位决定：兼容令牌不能调原生名字，原生令牌不能调兼容名字。
	calls := []struct {
		token, tool, args string
	}{
		{compat.Token, "terminal_exec", `{"target":"web","command":"id"}`},
		{compat.Token, "terminal_targets", `{}`},
		{compat.Token, "terminal_run", `{"action":"status","run_id":"x"}`},
		{native.Token, "exec", `{"target":"web","command":"id"}`},
		{native.Token, "exec_start", `{"target":"web","command":"id"}`},
		{native.Token, "targets_list", `{}`},
		{native.Token, "job_status", `{"job_id":"x"}`},
	}
	for _, c := range calls {
		body := fmt.Sprintf(`{"jsonrpc":"2.0","id":9,"method":"tools/call","params":{"name":%q,"arguments":%s}}`, c.tool, c.args)
		_, resp := rpcFrom(t, srv, c.token, "", body)
		res, _ := resp.Result.(map[string]any)
		if resp.Error == nil && res["isError"] != true {
			t.Fatalf("令牌 %q 不应能调用 %s: %#v", c.token, c.tool, resp.Result)
		}
	}
	if len(fx.callers) != 0 {
		t.Fatalf("越位的调用不得到达执行器（%d 次）", len(fx.callers))
	}

	// 兼容位本身不是能力：没有终端位时，兼容名字一律不可用。
	p := model.Permissions{Feedback: true, TerminalCompat: true}
	for _, name := range CompatToolNames() {
		if p.AllowsTool(name) {
			t.Fatalf("没有 Terminal 位却允许了 %s", name)
		}
	}
	if (model.Permissions{Terminal: true, TerminalCompat: true}).AllowsTool("terminal_exec") {
		t.Fatalf("兼容令牌不应再允许原生名字")
	}
}

func TestCompatFlagIsNormalizedOnCreateAndUpdate(t *testing.T) {
	srv := setupTestMCPServer(t)
	ctx := context.Background()

	c := newTerminalCredential(t, srv, "orphan", model.Permissions{Feedback: true, TerminalCompat: true})
	got, err := srv.store.GetCredential(ctx, c.ID)
	if err != nil || got == nil {
		t.Fatalf("读取凭据: %v %v", got, err)
	}
	if got.Permissions.TerminalCompat {
		t.Fatalf("没有终端位的凭据不应保留兼容位: %+v", got.Permissions)
	}

	term := newTerminalCredential(t, srv, "term", model.Permissions{Terminal: true, TerminalCompat: true})
	if got, _ := srv.store.GetCredential(ctx, term.ID); got == nil || !got.Permissions.TerminalCompat {
		t.Fatalf("带终端位的凭据应保留兼容位: %+v", got)
	}

	// 更新时关闭终端位，兼容位随之被清掉。
	updated, err := srv.store.UpdateCredential(ctx, term.ID, map[string]any{
		"permissions": map[string]any{"feedback": true, "terminal": false, "terminal_compat": true},
	})
	if err != nil {
		t.Fatalf("更新凭据: %v", err)
	}
	if updated.Permissions.TerminalCompat || updated.Permissions.Terminal {
		t.Fatalf("更新后权限 = %+v", updated.Permissions)
	}
}

func TestCompatInstructionsVariants(t *testing.T) {
	srv := setupTestMCPServer(t)
	ctx := context.Background()

	execCompat := srv.InstructionsForPermissions(ctx, model.Permissions{Terminal: true, TerminalCompat: true})
	for _, must := range []string{"targets_list", "exec_start", "job_status", "job_output", "job_cancel"} {
		if !strings.Contains(execCompat, must) {
			t.Fatalf("兼容命名的 ExecMesh 档指引缺少 %s", must)
		}
	}
	for _, mustNot := range []string{"interactive_feedback", "terminal_exec", "terminal_run"} {
		if strings.Contains(execCompat, mustNot) {
			t.Fatalf("兼容命名的 ExecMesh 档指引不应出现 %s", mustNot)
		}
	}
	if native := srv.InstructionsForPermissions(ctx, model.Permissions{Terminal: true}); native != DefaultTerminalInstructions {
		t.Fatalf("原生 ExecMesh 档指引不应变化")
	}

	hybrid := srv.InstructionsForPermissions(ctx, model.Permissions{Feedback: true, Terminal: true, TerminalCompat: true})
	if !strings.Contains(hybrid, "interactive_feedback") || !strings.Contains(hybrid, "exec_start") || strings.Contains(hybrid, "terminal_exec") {
		t.Fatalf("混合兼容令牌的指引 = 治理文本 + 兼容命名的终端一节，实际:\n%s", hybrid)
	}
	hybridNative := srv.InstructionsForPermissions(ctx, model.Permissions{Feedback: true, Terminal: true})
	if !strings.Contains(hybridNative, "terminal_exec") || strings.Contains(hybridNative, "exec_start") {
		t.Fatalf("混合原生令牌的指引不应变化")
	}
}

func TestCompatCallsReachExecutorWithMappedArguments(t *testing.T) {
	srv := setupTestMCPServer(t)
	fx := &fakeExecutor{
		execRes:   &termexec.RunResult{RunID: "r1", State: "running", Target: "web"},
		runRes:    &termexec.RunResult{RunID: "r1", State: "succeeded", Target: "web"},
		targetRes: &termexec.TargetsResult{Targets: []termexec.Target{}},
	}
	srv.SetTerminalExecutor(fx)
	cred := newTerminalCredential(t, srv, "compat", model.Permissions{Terminal: true, TerminalCompat: true})

	// exec：等待时间 = timeout_seconds，但最多 120 秒；没给就用默认等待。
	callTool(t, srv, cred.Token, "exec", `{"target":"web","command":"ls","cwd":"/srv","timeout_seconds":300,"description":"d","thread":"t","workflow_id":"w"}`)
	callTool(t, srv, cred.Token, "exec", `{"target":"web","command":"ls","timeout_seconds":"20"}`)
	callTool(t, srv, cred.Token, "exec", `{"target":"web","command":"ls"}`)
	want := []termexec.ExecRequest{
		{Target: "web", Command: "ls", Cwd: "/srv", TimeoutSeconds: 300, WaitSeconds: 120, WaitSet: true, Description: "d", Thread: "t", WorkflowID: "w"},
		{Target: "web", Command: "ls", TimeoutSeconds: 20, WaitSeconds: 20, WaitSet: true},
		{Target: "web", Command: "ls"},
	}
	for i, w := range want {
		if fx.execs[i] != w {
			t.Fatalf("exec #%d 交给执行器的参数 = %+v\n期望 %+v", i, fx.execs[i], w)
		}
	}

	// exec_start：立即返回，长任务默认 1 小时超时；显式 timeout_seconds 优先。
	callTool(t, srv, cred.Token, "exec_start", `{"target":"web","command":"make"}`)
	callTool(t, srv, cred.Token, "exec_start", `{"target":"web","command":"make","timeout_seconds":90}`)
	if r := fx.execs[3]; r != (termexec.ExecRequest{Target: "web", Command: "make", TimeoutSeconds: compatStartDefaultTimeout, WaitSeconds: 0, WaitSet: true}) {
		t.Fatalf("exec_start 参数 = %+v", r)
	}
	if r := fx.execs[4]; r.TimeoutSeconds != 90 || !r.WaitSet || r.WaitSeconds != 0 {
		t.Fatalf("exec_start 带 timeout 的参数 = %+v", r)
	}

	// env 不受支持：明确拒绝，且不会触达执行器。
	before := len(fx.execs)
	for _, tool := range []string{"exec", "exec_start"} {
		res, isErr := callJSON(t, srv, cred.Token, tool, `{"target":"web","command":"ls","env":{"A":"1"}}`)
		if !isErr || errCodeOf(res) != termexec.CodeBadRequest {
			t.Fatalf("%s 带 env 应被拒绝: %v %v", tool, isErr, res)
		}
	}
	if len(fx.execs) != before {
		t.Fatalf("带 env 的调用不得到达执行器")
	}
	// 空的 env 对象无害。
	callTool(t, srv, cred.Token, "exec", `{"target":"web","command":"ls","env":{}}`)

	// job_*：映射到 Run 的三种动作；target 参数被忽略。
	callTool(t, srv, cred.Token, "job_status", `{"job_id":"r1","target":"web","wait_seconds":"5"}`)
	callTool(t, srv, cred.Token, "job_output", `{"job_id":"r1","stream":"stderr","offset":"100","limit":4096}`)
	callTool(t, srv, cred.Token, "job_cancel", `{"job_id":"r1"}`)
	wantRuns := []termexec.RunRequest{
		{Action: "status", RunID: "r1", WaitSeconds: 5},
		{Action: "output", RunID: "r1", Stream: "stderr", Offset: 100, Limit: 4096},
		{Action: "cancel", RunID: "r1"},
	}
	for i, w := range wantRuns {
		if fx.runs[i] != w {
			t.Fatalf("job 调用 #%d = %+v，期望 %+v", i, fx.runs[i], w)
		}
	}
	for _, tool := range []string{"job_status", "job_output", "job_cancel"} {
		res, isErr := callJSON(t, srv, cred.Token, tool, `{"job_id":"  "}`)
		if !isErr || errCodeOf(res) != termexec.CodeBadRequest {
			t.Fatalf("%s 的 job_id 为空应被拒绝: %v %v", tool, isErr, res)
		}
	}
	if len(fx.runs) != 3 {
		t.Fatalf("job_id 为空的调用不得到达执行器")
	}

	// targets_list / target_info。
	callTool(t, srv, cred.Token, "targets_list", `{}`)
	callTool(t, srv, cred.Token, "target_info", `{"target":"web"}`)
	if len(fx.targets) != 2 || fx.targets[0] != "" || fx.targets[1] != "web" {
		t.Fatalf("targets 参数 = %v", fx.targets)
	}
	res, isErr := callJSON(t, srv, cred.Token, "target_info", `{}`)
	if !isErr || errCodeOf(res) != termexec.CodeBadRequest {
		t.Fatalf("target_info 缺少 target 应被拒绝: %v %v", isErr, res)
	}
}

func intp(n int) *int { return &n }

func TestCompatResultShapes(t *testing.T) {
	srv := setupTestMCPServer(t)
	fx := &fakeExecutor{}
	srv.SetTerminalExecutor(fx)
	cred := newTerminalCredential(t, srv, "compat", model.Permissions{Terminal: true, TerminalCompat: true})
	str := func(m map[string]any, k string) string { s, _ := m[k].(string); return s }

	// exec：成功。字段对齐 ExecMesh：stdout / stderr 是字符串，另有 *_truncated、exit_code、duration_ms。
	fx.execRes = &termexec.RunResult{
		RunID: "r1", State: "succeeded", Target: "RM-AAA", Thread: "部署", Mode: "auto", ExitCode: intp(0), DurationMS: 12,
		StartedAt: "2026-10-02T00:00:00Z", FinishedAt: "2026-10-02T00:00:01Z",
		Stdout: &termexec.Output{Text: "hello\n", Bytes: 6}, Notice: termexec.Notice,
	}
	m, isErr := callJSON(t, srv, cred.Token, "exec", `{"target":"web","command":"echo hello"}`)
	if isErr || str(m, "stdout") != "hello\n" || str(m, "stderr") != "" || m["exit_code"] != float64(0) || m["duration_ms"] != float64(12) ||
		m["stdout_truncated"] != false || m["stderr_truncated"] != false || str(m, "job_id") != "r1" || str(m, "state") != "succeeded" ||
		str(m, "target") != "RM-AAA" || str(m, "thread") != "部署" || str(m, "notice") == "" || m["hint"] != nil {
		t.Fatalf("exec 成功结果 = %v %v", isErr, m)
	}

	// exec：输出被截断时给出尾部与继续读取的位置，提示指向 job_output 而不是 terminal_run。
	fx.execRes = &termexec.RunResult{
		RunID: "r2", State: "succeeded", Target: "RM-AAA", ExitCode: intp(1), DurationMS: 5,
		Stdout: &termexec.Output{Text: "head", Bytes: 9000, Truncated: true, NextOffset: 4, Tail: "tail"},
		Stderr: &termexec.Output{Text: "oops", Bytes: 4},
		Hint:   "输出被截断：用 terminal_run {action:\"output\"} 继续读取。",
	}
	m, _ = callJSON(t, srv, cred.Token, "exec", `{"target":"web","command":"x"}`)
	if m["stdout_truncated"] != true || str(m, "stdout_tail") != "tail" || m["stdout_next_offset"] != float64(4) || str(m, "stderr") != "oops" ||
		m["exit_code"] != float64(1) || !strings.Contains(str(m, "hint"), "job_output") || strings.Contains(str(m, "hint"), "terminal_run") {
		t.Fatalf("exec 截断结果 = %v", m)
	}

	// exec：仍在运行（超过等待时间）——exit_code 为 null，带 job_id 与指向 job_status 的提示。
	fx.execRes = &termexec.RunResult{RunID: "r3", State: "running", Target: "RM-AAA", Hint: "命令仍在运行：用 terminal_run …"}
	m, _ = callJSON(t, srv, cred.Token, "exec", `{"target":"web","command":"sleep 99"}`)
	if m["exit_code"] != nil || str(m, "state") != "running" || str(m, "job_id") != "r3" || !strings.Contains(str(m, "hint"), "job_status") ||
		strings.Contains(str(m, "hint"), "terminal_run") {
		t.Fatalf("exec 运行中结果 = %v", m)
	}
	if _, ok := m["exit_code"]; !ok {
		t.Fatalf("exit_code 必须以 null 出现，而不是缺失: %v", m)
	}

	// 等待批准。
	fx.execRes = &termexec.RunResult{RunID: "r4", State: "awaiting_approval", Target: "RM-AAA"}
	m, _ = callJSON(t, srv, cred.Token, "exec_start", `{"target":"web","command":"reboot"}`)
	if str(m, "job_id") != "r4" || str(m, "state") != "awaiting_approval" || !strings.Contains(str(m, "hint"), "批准") {
		t.Fatalf("exec_start 等待批准结果 = %v", m)
	}

	// exec_start：job_id / target / state / started_at。
	fx.execRes = &termexec.RunResult{RunID: "r5", State: "running", Target: "RM-AAA", Thread: "构建", Mode: "auto", StartedAt: "2026-10-02T00:00:00Z"}
	m, _ = callJSON(t, srv, cred.Token, "exec_start", `{"target":"web","command":"make"}`)
	if str(m, "job_id") != "r5" || str(m, "target") != "RM-AAA" || str(m, "state") != "running" || str(m, "started_at") != "2026-10-02T00:00:00Z" ||
		str(m, "thread") != "构建" || !strings.Contains(str(m, "hint"), "job_status") {
		t.Fatalf("exec_start 结果 = %v", m)
	}

	// job_status：只有状态与输出大小，没有输出正文。
	fx.runRes = &termexec.RunResult{
		RunID: "r1", State: "succeeded", Target: "RM-AAA", ExitCode: intp(0), DurationMS: 12,
		StartedAt: "2026-10-02T00:00:00Z", FinishedAt: "2026-10-02T00:00:01Z",
		Stdout: &termexec.Output{Text: "hello\n", Bytes: 6}, Stderr: &termexec.Output{Bytes: 3},
	}
	m, _ = callJSON(t, srv, cred.Token, "job_status", `{"job_id":"r1"}`)
	if str(m, "job_id") != "r1" || str(m, "state") != "succeeded" || m["exit_code"] != float64(0) || m["stdout_bytes"] != float64(6) ||
		m["stderr_bytes"] != float64(3) || str(m, "started_at") == "" || str(m, "finished_at") == "" || m["stdout"] != nil || m["data"] != nil {
		t.Fatalf("job_status 结果 = %v", m)
	}
	fx.runRes = &termexec.RunResult{RunID: "r6", State: "running", Target: "RM-AAA"}
	m, _ = callJSON(t, srv, cred.Token, "job_status", `{"job_id":"r6"}`)
	if m["exit_code"] != nil || m["stdout_bytes"] != float64(0) || !strings.Contains(str(m, "hint"), "job_output") {
		t.Fatalf("运行中的 job_status 结果 = %v", m)
	}

	// job_output：data / offset / next_offset / eof。
	fx.runRes = &termexec.OutputPage{RunID: "r1", Stream: "stdout", Offset: 0, NextOffset: 6, TotalBytes: 6, Text: "hello\n", EOF: true, Notice: termexec.Notice}
	m, _ = callJSON(t, srv, cred.Token, "job_output", `{"job_id":"r1"}`)
	if str(m, "data") != "hello\n" || m["next_offset"] != float64(6) || m["eof"] != true || m["offset"] != float64(0) || str(m, "stream") != "stdout" ||
		str(m, "job_id") != "r1" || m["total_bytes"] != float64(6) {
		t.Fatalf("job_output 结果 = %v", m)
	}

	// job_cancel：state + message。
	cancels := map[string]string{"cancelled": "已取消", "running": "已请求取消", "succeeded": "已经结束"}
	for state, frag := range cancels {
		fx.runRes = &termexec.RunResult{RunID: "r7", State: state, Target: "RM-AAA"}
		m, _ = callJSON(t, srv, cred.Token, "job_cancel", `{"job_id":"r7"}`)
		if str(m, "state") != state || !strings.Contains(str(m, "message"), frag) || str(m, "job_id") != "r7" || str(m, "target") != "RM-AAA" {
			t.Fatalf("job_cancel(%s) 结果 = %v", state, m)
		}
	}

	// targets_list：id 是机器码；enabled 等同 usable；能力随可用性变化。
	fx.targetRes = &termexec.TargetsResult{
		Targets: []termexec.Target{
			{MachineCode: "RM-AAA", Name: "构建机", Hostname: "build", OS: "linux", Arch: "amd64", Online: true, Shell: "bash", Home: "/home/u", PathSep: "/", Mode: "auto", Usable: true},
			{MachineCode: "RM-BBB", Name: "离线机", Mode: "approve", Blocked: "host_offline"},
		},
		Hint: "h",
	}
	m, _ = callJSON(t, srv, cred.Token, "targets_list", `{}`)
	ts, _ := m["targets"].([]any)
	if len(ts) != 2 {
		t.Fatalf("targets_list = %v", m)
	}
	a, _ := ts[0].(map[string]any)
	b, _ := ts[1].(map[string]any)
	caps, _ := a["capabilities"].(map[string]any)
	bcaps, _ := b["capabilities"].(map[string]any)
	if str(a, "id") != "RM-AAA" || str(a, "name") != "构建机" || a["enabled"] != true || str(a, "default_cwd") != "/home/u" || str(a, "shell") != "bash" ||
		caps["exec"] != true || caps["jobs"] != true || caps["upload"] != false || a["usable"] != true || a["online"] != true || str(a, "mode") != "auto" {
		t.Fatalf("targets_list[0] = %v", a)
	}
	if b["enabled"] != false || bcaps["exec"] != false || str(b, "blocked") != "host_offline" || b["online"] != false {
		t.Fatalf("targets_list[1] = %v", b)
	}
	if tags, ok := a["tags"].([]any); !ok || len(tags) != 0 {
		t.Fatalf("tags 应是空数组: %v", a["tags"])
	}

	// target_info：单台详情与限额；没有匹配或有歧义都是 host_not_found，不泄露存在性。
	fx.targetRes = &termexec.TargetsResult{Targets: []termexec.Target{{MachineCode: "RM-AAA", Name: "构建机", Online: true, Usable: true, Mode: "auto"}}}
	m, isErr = callJSON(t, srv, cred.Token, "target_info", `{"target":"RM-AAA"}`)
	tgt, _ := m["target"].(map[string]any)
	lim, _ := tgt["limits"].(map[string]any)
	if isErr || str(tgt, "id") != "RM-AAA" || lim["max_exec_seconds"] != float64(termproto.MaxRunTimeoutSec) || lim["max_upload_bytes"] != float64(0) {
		t.Fatalf("target_info 结果 = %v %v", isErr, m)
	}
	fx.targetRes = &termexec.TargetsResult{
		Targets: []termexec.Target{{MachineCode: "RM-AAA", Name: "构建机", Usable: true}},
		Limits:  &termexec.Limits{MaxConcurrent: 2, MaxWaitSeconds: 10, MaxTimeoutSeconds: 600, MaxOutputBytes: 2048, AllowedDirs: []string{"/srv"}},
	}
	m, _ = callJSON(t, srv, cred.Token, "target_info", `{"target":"构建机"}`)
	tgt, _ = m["target"].(map[string]any)
	lim, _ = tgt["limits"].(map[string]any)
	if lim["max_exec_seconds"] != float64(600) || lim["max_concurrent"] != float64(2) || lim["max_wait_seconds"] != float64(10) || lim["max_output_bytes"] != float64(2048) {
		t.Fatalf("带限额的 target_info = %v", lim)
	}
	if dirs, _ := lim["allowed_dirs"].([]any); len(dirs) != 1 || dirs[0] != "/srv" {
		t.Fatalf("allowed_dirs = %v", lim["allowed_dirs"])
	}
	fx.targetRes = &termexec.TargetsResult{Targets: []termexec.Target{}}
	m, isErr = callJSON(t, srv, cred.Token, "target_info", `{"target":"nope"}`)
	if !isErr || errCodeOf(m) != termexec.CodeHostNotFound {
		t.Fatalf("没有匹配的 target_info 应是 host_not_found: %v %v", isErr, m)
	}
	fx.targetRes = &termexec.TargetsResult{Targets: []termexec.Target{{MachineCode: "A"}, {MachineCode: "B"}}}
	m, isErr = callJSON(t, srv, cred.Token, "target_info", `{"target":"dup"}`)
	if !isErr || errCodeOf(m) != termexec.CodeHostNotFound {
		t.Fatalf("有歧义的 target_info 应是 host_not_found: %v %v", isErr, m)
	}

	// 执行器的业务错误原样透出稳定错误码。
	fx.execErr = &termexec.Error{Code: termexec.CodePaused, Message: "暂停"}
	m, isErr = callJSON(t, srv, cred.Token, "exec", `{"target":"web","command":"id"}`)
	if !isErr || errCodeOf(m) != termexec.CodePaused {
		t.Fatalf("exec 的业务错误 = %v %v", isErr, m)
	}
	m, isErr = callJSON(t, srv, cred.Token, "exec_start", `{"target":"web","command":"id"}`)
	if !isErr || errCodeOf(m) != termexec.CodePaused {
		t.Fatalf("exec_start 的业务错误 = %v %v", isErr, m)
	}
}

// 执行器返回了兼容层不认识的类型（例如测试替身）时原样透传，而不是丢弃或崩溃。
func TestCompatPassesThroughUnknownResultTypes(t *testing.T) {
	srv := setupTestMCPServer(t)
	fx := &fakeExecutor{
		execRes:   map[string]any{"custom": "exec"},
		runRes:    map[string]any{"custom": "run"},
		targetRes: map[string]any{"custom": "targets"},
	}
	srv.SetTerminalExecutor(fx)
	cred := newTerminalCredential(t, srv, "compat", model.Permissions{Terminal: true, TerminalCompat: true})
	cases := map[string]string{
		"exec": `{"target":"w","command":"c"}`, "exec_start": `{"target":"w","command":"c"}`, "job_status": `{"job_id":"x"}`,
		"job_output": `{"job_id":"x"}`, "job_cancel": `{"job_id":"x"}`, "targets_list": `{}`,
	}
	for tool, args := range cases {
		m, isErr := callJSON(t, srv, cred.Token, tool, args)
		if isErr || m["custom"] == nil {
			t.Fatalf("%s 应原样透传未知类型: %v %v", tool, isErr, m)
		}
	}
}
