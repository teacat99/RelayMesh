//go:build e2e && !windows

package e2e

import (
	"encoding/json"
	"io"
	"net/http"
	"sort"
	"strings"
	"testing"
	"time"
)

// ExecMesh 档令牌的端到端（AC-P16）：一个独立的 MCP 客户端（只会说 JSON-RPC / HTTP）用 ExecMesh 档令牌连上真实的
// Hub、守护进程与 Agent，检查「只有终端工具、没有治理文本、REST / SSE 全部 403、运行在 Web 可见」，
// 以及开启「ExecMesh 兼容命名」后同一套能力以 ExecMesh 的工具名与返回形状工作。

// rpcCall 发一个 JSON-RPC 请求，返回完整响应（result 与 error 都保留，由调用方判断）。
func (m *mcpClient) rpcCall(method string, params map[string]any) (result map[string]any, rpcErr string) {
	m.t.Helper()
	m.id++
	body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": m.id, "method": method, "params": params})
	resp, err := m.hc.Do(m.newRequest(body))
	if err != nil {
		m.t.Fatalf("MCP %s 失败: %v", method, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var out struct {
		Result map[string]any            `json:"result"`
		Error  *struct{ Message string } `json:"error"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		m.t.Fatalf("MCP %s 响应不是 JSON: HTTP %d %s", method, resp.StatusCode, lastN(string(raw), 300))
	}
	if out.Error != nil {
		return nil, out.Error.Message
	}
	return out.Result, ""
}

func (m *mcpClient) toolNames() []string {
	m.t.Helper()
	res, e := m.rpcCall("tools/list", map[string]any{})
	if e != "" {
		m.t.Fatalf("tools/list: %s", e)
	}
	var names []string
	for _, it := range res["tools"].([]any) {
		names = append(names, it.(map[string]any)["name"].(string))
	}
	sort.Strings(names)
	return names
}

func (m *mcpClient) instructions() string {
	m.t.Helper()
	res, e := m.rpcCall("initialize", map[string]any{
		"protocolVersion": "2025-03-26",
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "independent-e2e-client", "version": "1.0"},
	})
	if e != "" {
		m.t.Fatalf("initialize: %s", e)
	}
	s, _ := res["instructions"].(string)
	return s
}

// callDenied 调用一个工具并期望被拒绝（JSON-RPC 错误或 isError），返回错误文本。
func (m *mcpClient) callDenied(name string, args map[string]any) string {
	m.t.Helper()
	res, e := m.rpcCall("tools/call", map[string]any{"name": name, "arguments": args})
	if e != "" {
		return e
	}
	if isErr, _ := res["isError"].(bool); !isErr {
		m.t.Fatalf("%s 不应被允许: %v", name, res)
	}
	content, _ := res["content"].([]any)
	if len(content) > 0 {
		if first, ok := content[0].(map[string]any); ok {
			s, _ := first["text"].(string)
			return s
		}
	}
	return "isError"
}

// newCompatToken 签发一把开启「ExecMesh 兼容命名」的 ExecMesh 档令牌，只绑定本实验主机。
func (l *execLab) newCompatToken(name string) string {
	l.t.Helper()
	var out struct {
		Token string `json:"token"`
	}
	c := l.h.api.raw("POST", "/api/v1/credentials", map[string]any{
		"name": name, "permissions": map[string]any{"terminal": true, "terminal_compat": true}, "terminal_hosts": []string{l.code},
	}, &out, true)
	if c != 200 || out.Token == "" {
		l.t.Fatalf("创建兼容令牌失败: HTTP %d", c)
	}
	return out.Token
}

// httpCode 用给定的 MCP 令牌请求 REST / SSE，返回状态码与响应体。
func httpCode(t *testing.T, method, url, token string) (int, string) {
	t.Helper()
	req, err := http.NewRequest(method, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if method == "POST" || method == "PUT" {
		req.Header.Set("Content-Type", "application/json")
	}
	hc := &http.Client{Timeout: 10 * time.Second}
	resp, err := hc.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	return resp.StatusCode, string(b)
}

func TestRealExecMeshTierEndToEnd(t *testing.T) {
	l := newExecLab(t, execOn())
	m := l.mcp

	// 1) initialize：精简的终端指引，不含 RelayMesh 治理文本。
	ins := m.instructions()
	for _, must := range []string{"terminal_targets", "terminal_exec", "terminal_run"} {
		if !strings.Contains(ins, must) {
			t.Fatalf("ExecMesh 档指引缺少 %s:\n%s", must, ins)
		}
	}
	for _, mustNot := range []string{"interactive_feedback", "continue_feedback_session", "workflow_id", "exec_start"} {
		if strings.Contains(ins, mustNot) {
			t.Fatalf("ExecMesh 档指引不应出现 %q:\n%s", mustNot, ins)
		}
	}

	// 2) tools/list：只有三个终端工具。
	if got := strings.Join(m.toolNames(), ","); got != "terminal_exec,terminal_run,terminal_targets" {
		t.Fatalf("ExecMesh 档 tools/list = %s", got)
	}

	// 3) 治理类工具一律不可用。
	for _, tool := range []string{"interactive_feedback", "list_sessions", "workflow_context", "task_board", "get_system_info"} {
		if msg := m.callDenied(tool, map[string]any{"title": "x", "summary": "y"}); msg == "" {
			t.Fatalf("%s 应被拒绝", tool)
		}
	}

	// 4) 完整走一遍终端工具；ExecMesh 档不需要 workflow_id / description。
	tg, isErr := m.tool("terminal_targets", map[string]any{})
	if isErr || len(tg["targets"].([]any)) != 1 {
		t.Fatalf("terminal_targets: %v", tg)
	}
	ok := m.exec(l.code, "echo from-execmesh; echo e 1>&2", map[string]any{"thread": "execmesh-e2e", "description": "AC-P16"})
	if ok.str("state") != "succeeded" || ok.obj("stdout").str("text") != "from-execmesh\n" || ok.num("exit_code") != 0 {
		t.Fatalf("terminal_exec 结果不符: %v", ok)
	}
	runID := ok.str("run_id")
	page, isErr := m.run("output", runID, map[string]any{"stream": "stdout"})
	if isErr || page.str("text") != "from-execmesh\n" || page["eof"] != true {
		t.Fatalf("terminal_run output: %v", page)
	}

	// 5) 这把令牌对 REST / SSE 没有任何访问（403 + mcp_token_forbidden），包括用 ?token= 的写法。
	base := l.h.base()
	for _, rt := range []struct{ method, path string }{
		{"GET", "/api/v1/sessions"},
		{"GET", "/api/v1/credentials"},
		{"POST", "/api/v1/credentials"},
		{"GET", "/api/v1/settings"},
		{"GET", "/api/v1/terminal/hosts"},
		{"GET", "/api/v1/terminal/exec/threads"},
		{"GET", "/api/v1/terminal/exec/runs"},
		{"GET", "/api/v1/terminal/exec/events"},
		{"GET", "/api/v1/events"},
		{"GET", "/sse/events"},
	} {
		code, body := httpCode(t, rt.method, base+rt.path, m.token)
		if code != http.StatusForbidden || !strings.Contains(body, "mcp_token_forbidden") {
			t.Fatalf("ExecMesh 令牌访问 %s %s 应得到 403 mcp_token_forbidden，实际 %d %s", rt.method, rt.path, code, lastN(body, 200))
		}
	}
	for _, path := range []string{"/api/v1/events", "/sse/events", "/api/v1/terminal/exec/events"} {
		if code, _ := httpCode(t, "GET", base+path+"?token="+m.token, ""); code < 400 {
			t.Fatalf("?token= 写法访问 %s 不应成功: %d", path, code)
		}
	}

	// 6) 运行在 Web（人类 JWT）可见：线程聚合与运行详情。
	var threads struct {
		Threads []struct {
			Key            string `json:"key"`
			CredentialName string `json:"credential_name"`
			Thread         string `json:"thread"`
			Target         string `json:"target"`
			Total          int    `json:"total"`
		} `json:"threads"`
	}
	if c := l.h.api.raw("GET", "/api/v1/terminal/exec/threads", nil, &threads, false); c != 200 {
		t.Fatalf("线程聚合: HTTP %d", c)
	}
	found := ""
	for _, th := range threads.Threads {
		if th.CredentialName == "model-e2e" && th.Thread == "execmesh-e2e" && th.Target == l.code && th.Total >= 1 {
			found = th.Key
		}
	}
	if found == "" {
		t.Fatalf("Web 的线程列表里看不到 ExecMesh 档令牌的运行: %+v", threads.Threads)
	}
	var runs struct {
		Runs []struct {
			RunID       string `json:"run_id"`
			Description string `json:"description"`
			State       string `json:"state"`
			ActorKind   string `json:"actor_kind"`
		} `json:"runs"`
	}
	if c := l.h.api.raw("GET", "/api/v1/terminal/exec/runs?thread="+found, nil, &runs, false); c != 200 {
		t.Fatalf("线程运行列表: HTTP %d", c)
	}
	seen := false
	for _, r := range runs.Runs {
		if r.RunID == runID && r.Description == "AC-P16" && r.State == "succeeded" && r.ActorKind == "model" {
			seen = true
		}
	}
	if !seen {
		t.Fatalf("线程里没有这次运行（%s）: %+v", runID, runs.Runs)
	}
}

func TestRealExecMeshCompatNaming(t *testing.T) {
	l := newExecLab(t, execOn())
	m := &mcpClient{t: t, base: l.h.base(), token: l.newCompatToken("compat-e2e"), hc: &http.Client{Timeout: 90 * time.Second}}

	// initialize / tools/list：只有 ExecMesh 的七个工具名，指引里也是这些名字。
	ins := m.instructions()
	for _, must := range []string{"targets_list", "exec_start", "job_status", "job_output", "job_cancel"} {
		if !strings.Contains(ins, must) {
			t.Fatalf("兼容命名指引缺少 %s:\n%s", must, ins)
		}
	}
	for _, mustNot := range []string{"interactive_feedback", "terminal_exec", "terminal_run"} {
		if strings.Contains(ins, mustNot) {
			t.Fatalf("兼容命名指引不应出现 %q", mustNot)
		}
	}
	want := []string{"exec", "exec_start", "job_cancel", "job_output", "job_status", "target_info", "targets_list"}
	if got := m.toolNames(); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("兼容命名的 tools/list = %v，期望 %v", got, want)
	}
	// 原生名字不可用。
	if msg := m.callDenied("terminal_exec", map[string]any{"target": l.code, "command": "id"}); msg == "" {
		t.Fatalf("兼容令牌不应能调原生名字")
	}

	// targets_list / target_info。
	tl, isErr := m.tool("targets_list", map[string]any{})
	if isErr {
		t.Fatalf("targets_list: %v", tl)
	}
	list, _ := tl["targets"].([]any)
	if len(list) != 1 {
		t.Fatalf("targets_list = %v", tl)
	}
	tgt := toolRes(list[0].(map[string]any))
	if tgt.str("id") != l.code || tgt["enabled"] != true || tgt["online"] != true || tgt.obj("capabilities")["exec"] != true || tgt.str("default_cwd") == "" {
		t.Fatalf("targets_list[0] = %v", tgt)
	}
	ti, isErr := m.tool("target_info", map[string]any{"target": l.code})
	if isErr || ti.obj("target").str("id") != l.code || ti.obj("target").obj("limits").num("max_exec_seconds") <= 0 {
		t.Fatalf("target_info = %v %v", isErr, ti)
	}
	if res, isErr := m.tool("target_info", map[string]any{"target": "no-such-host"}); !isErr || res.errCode() != "host_not_found" {
		t.Fatalf("不存在的主机应是 host_not_found: %v %v", isErr, res)
	}

	// exec：ExecMesh 的返回形状。
	ex, isErr := m.tool("exec", map[string]any{"target": l.code, "command": "echo hi; echo err 1>&2", "thread": "compat-e2e", "description": "兼容命名"})
	if isErr || ex.str("state") != "succeeded" || ex.str("stdout") != "hi\n" || ex.str("stderr") != "err\n" || ex.num("exit_code") != 0 ||
		ex["stdout_truncated"] != false || ex["stderr_truncated"] != false || ex.str("job_id") == "" || ex.str("target") != l.code || ex.num("duration_ms") < 0 {
		t.Fatalf("exec 结果不符: %v %v", isErr, ex)
	}
	bad, _ := m.tool("exec", map[string]any{"target": l.code, "command": "exit 3"})
	if bad.str("state") != "failed" || bad.num("exit_code") != 3 {
		t.Fatalf("非 0 退出码: %v", bad)
	}
	if res, isErr := m.tool("exec", map[string]any{"target": l.code, "command": "id", "env": map[string]any{"A": "1"}}); !isErr || res.errCode() != "bad_request" {
		t.Fatalf("env 应被明确拒绝: %v %v", isErr, res)
	}

	// exec_start + job_status + job_output：长任务的异步流程。
	st, isErr := m.tool("exec_start", map[string]any{"target": l.code, "command": "printf abc; sleep 2; printf def", "thread": "compat-e2e"})
	if isErr || st.str("state") != "running" || st.str("job_id") == "" || st.str("started_at") == "" {
		t.Fatalf("exec_start 结果: %v %v", isErr, st)
	}
	job := st.str("job_id")
	running, _ := m.tool("job_status", map[string]any{"job_id": job})
	if running.str("state") != "running" || running["exit_code"] != nil {
		t.Fatalf("运行中的 job_status: %v", running)
	}
	done, _ := m.tool("job_status", map[string]any{"job_id": job, "wait_seconds": 30})
	if done.str("state") != "succeeded" || done.num("exit_code") != 0 || done.num("stdout_bytes") != 6 || done.str("finished_at") == "" {
		t.Fatalf("结束后的 job_status: %v", done)
	}
	out, isErr := m.tool("job_output", map[string]any{"job_id": job, "stream": "stdout"})
	if isErr || out.str("data") != "abcdef" || out["eof"] != true || out.num("next_offset") != 6 {
		t.Fatalf("job_output: %v %v", isErr, out)
	}
	// limit 有 1 KiB 的下限（与原生 terminal_run 一致），所以这里只用 offset 验证续读位置。
	part, _ := m.tool("job_output", map[string]any{"job_id": job, "offset": 3, "limit": 2})
	if part.str("data") != "def" || part.num("offset") != 3 || part.num("next_offset") != 6 || part["eof"] != true {
		t.Fatalf("按 offset 续读 job_output: %v", part)
	}

	// job_cancel。
	long, _ := m.tool("exec_start", map[string]any{"target": l.code, "command": "sleep 120", "description": "要被取消"})
	cj := long.str("job_id")
	c1, isErr := m.tool("job_cancel", map[string]any{"job_id": cj})
	if isErr || c1.str("job_id") != cj || c1.str("message") == "" {
		t.Fatalf("job_cancel: %v %v", isErr, c1)
	}
	waitUntil(t, 20*time.Second, "被取消的任务进入终态", func() bool {
		s, _ := m.tool("job_status", map[string]any{"job_id": cj})
		return s.str("state") == "cancelled"
	})
	if res, isErr := m.tool("job_status", map[string]any{"job_id": "0123456789abcdef0123456789abcdef"}); !isErr || res.errCode() != "run_not_found" {
		t.Fatalf("不存在的 job: %v %v", isErr, res)
	}

	// 兼容命名的运行同样在 Web 可见。
	var threads struct {
		Threads []struct {
			CredentialName string `json:"credential_name"`
			Thread         string `json:"thread"`
			Total          int    `json:"total"`
		} `json:"threads"`
	}
	if c := l.h.api.raw("GET", "/api/v1/terminal/exec/threads", nil, &threads, false); c != 200 {
		t.Fatalf("线程聚合: HTTP %d", c)
	}
	// 同一把令牌的运行按 thread 分组：未带 thread 的归一组，带了 thread 的各归各组；合计应覆盖本测试发起的全部运行。
	total, named := 0, false
	for _, th := range threads.Threads {
		if th.CredentialName != "compat-e2e" {
			continue
		}
		total += th.Total
		if th.Thread == "compat-e2e" {
			named = true
		}
	}
	if total < 4 || !named {
		t.Fatalf("Web 里看不到兼容命名令牌的运行（合计 %d，带 thread 的分组 %v）: %+v", total, named, threads.Threads)
	}

	// 兼容令牌同样不能访问 REST / SSE。
	if code, body := httpCode(t, "GET", l.h.base()+"/api/v1/sessions", m.token); code != http.StatusForbidden || !strings.Contains(body, "mcp_token_forbidden") {
		t.Fatalf("兼容令牌访问 REST: %d %s", code, body)
	}
}
