package hookcli

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// 本文件测的是 hook v2 的「不阻塞上报」：父进程只生成白名单上报并把任务交给分离子进程（测试里的替身记下任务，
// 再由 flush 当作子进程去执行）。真正的进程分离见 binary_smoke_test.go。

func eventPayload(root, event string, extra map[string]any) string {
	m := richPayload(root, map[string]any{"hook_event_name": event})
	for k, v := range extra {
		m[k] = v
	}
	b, _ := json.Marshal(m)
	return string(b)
}

func asyncSpecs() []eventSpec {
	var out []eventSpec
	for _, spec := range eventSpecs {
		if spec.Async {
			out = append(out, spec)
		}
	}
	return out
}

func setPeek(f *fakeMCP, peek map[string]any) {
	f.mu.Lock()
	f.peek = peek
	f.mu.Unlock()
}

func glob1(t *testing.T, dir, pattern string) string {
	t.Helper()
	m, _ := filepath.Glob(filepath.Join(dir, pattern))
	if len(m) != 1 {
		t.Fatalf("%s 应恰好有 1 个，实际 %v", pattern, m)
	}
	return m[0]
}

func TestAsyncEvents_ParentOnlyQueuesThenChildReports(t *testing.T) {
	s := newGoSandbox(t)
	f := newFakeMCP(t, map[string]any{"unread": 0})
	s.config(map[string]any{"workflow_id": "wf-1", "mcp_url": f.url(), "interval_seconds": 0})

	specs := asyncSpecs()
	if len(specs) != 8 {
		t.Fatalf("应有 8 个异步事件，实际 %d", len(specs))
	}
	for _, spec := range specs {
		start := time.Now()
		out := s.run(s.project, eventPayload(s.project, spec.Cursor, nil), tokenEnv(), spec.Sub).json(t)
		if time.Since(start) > 500*time.Millisecond {
			t.Fatalf("%s: 父进程不该等网络", spec.Cursor)
		}
		wantContinue := spec.Cursor == "beforeSubmitPrompt"
		if wantContinue && (len(out) != 1 || out["continue"] != true) {
			t.Fatalf("beforeSubmitPrompt 必须明确放行: %v", out)
		}
		if !wantContinue && len(out) != 0 {
			t.Fatalf("%s 的标准输出应是空对象: %v", spec.Cursor, out)
		}
	}
	if n := len(f.hits()); n != 0 {
		t.Fatalf("父进程不该自己发请求，实际 %d 次", n)
	}
	jobs := s.reports()
	if len(jobs) != len(specs) {
		t.Fatalf("应交出 %d 个任务，实际 %d", len(specs), len(jobs))
	}
	for i, job := range jobs {
		if job.Report["agent_event"] != specs[i].Wire {
			t.Fatalf("任务 %d 的事件应是 %s: %v", i, specs[i].Wire, job.Report)
		}
		if job.ConfigPath != filepath.Join(s.project, ".cursor", "relaymesh.json") {
			t.Fatalf("任务里应带配置文件路径: %q", job.ConfigPath)
		}
		raw, _ := json.Marshal(job)
		assertNoSecret(t, string(raw), "交给子进程的任务", testToken)
		var generic map[string]any
		_ = json.Unmarshal(raw, &generic)
		if strings.Join(keysOf(generic), ",") != "config_path,report" {
			t.Fatalf("任务里只该有配置路径与上报: %s", raw)
		}
	}

	if n := s.flush(tokenEnv()); n != len(specs) {
		t.Fatalf("flush 了 %d 个", n)
	}
	hits := f.hits()
	if len(hits) != len(specs) {
		t.Fatalf("子进程应各发一次请求，实际 %d", len(hits))
	}
	allowed := map[string]bool{
		"action": true, "workflow_id": true, "source": true,
		"agent_event": true, "agent_status": true, "conversation_id": true,
		"agent_ts": true, "agent_tool": true, "agent_duration_ms": true,
		"client_kind": true, "cursor_version": true, "project_directory": true,
	}
	for i, h := range hits {
		args := h.args()
		if h.auth != "Bearer "+testToken {
			t.Fatalf("子进程应在自己这里解析令牌: %q", h.auth)
		}
		if args["action"] != "inbox_peek" || args["source"] != "hook" || args["workflow_id"] != "wf-1" {
			t.Fatalf("请求形态不对: %v", args)
		}
		if _, has := args["reminder"]; has {
			t.Fatalf("状态事件不消耗提醒: %v", args)
		}
		if args["agent_event"] != specs[i].Wire {
			t.Fatalf("第 %d 次应是 %s: %v", i, specs[i].Wire, args)
		}
		if _, ok := args["agent_ts"].(float64); !ok {
			t.Fatalf("要带发送方时间: %v", args)
		}
		for k := range args {
			if !allowed[k] {
				t.Fatalf("多余字段 %q", k)
			}
		}
		raw, _ := json.Marshal(h.body)
		for _, secret := range []string{"someone@example.com", "SECRET", "gen-1", "some-model"} {
			if strings.Contains(string(raw), secret) {
				t.Fatalf("泄露 %q: %s", secret, raw)
			}
		}
	}
}

func TestAsyncEvents_FailOpenWithoutQueueing(t *testing.T) {
	s := newGoSandbox(t)
	f := newFakeMCP(t, map[string]any{"unread": 0})
	cases := []struct {
		name  string
		setup func()
		env   []string
	}{
		{"没有配置文件", func() {}, tokenEnv()},
		{"工作流为空", func() { s.config(map[string]any{"workflow_id": " ", "mcp_url": f.url()}) }, tokenEnv()},
		{"enabled false", func() { s.config(map[string]any{"workflow_id": "wf-1", "mcp_url": f.url(), "enabled": false}) }, tokenEnv()},
		{"没有令牌", func() { s.config(map[string]any{"workflow_id": "wf-1", "mcp_url": f.url()}) }, nil},
		{"没有 MCP 地址", func() { s.config(map[string]any{"workflow_id": "wf-1"}) }, tokenEnv()},
	}
	for _, c := range cases {
		c.setup()
		for _, spec := range asyncSpecs() {
			out := s.run(s.project, eventPayload(s.project, spec.Cursor, nil), c.env, spec.Sub).json(t)
			if spec.Cursor == "beforeSubmitPrompt" {
				if out["continue"] != true {
					t.Fatalf("%s: 就算什么都不做，beforeSubmitPrompt 也必须放行: %v", c.name, out)
				}
			} else if len(out) != 0 {
				t.Fatalf("%s/%s: %v", c.name, spec.Cursor, out)
			}
		}
	}
	if len(s.reports()) != 0 || len(f.hits()) != 0 {
		t.Fatalf("这些情况下不该排队也不该发请求: 排队 %d 请求 %d", len(s.reports()), len(f.hits()))
	}
	// 坏的标准输入照样放行，并按 cwd 找配置。
	s.config(map[string]any{"workflow_id": "wf-1", "mcp_url": f.url()})
	if out := s.run(s.project, "bad{", tokenEnv(), "before-submit-prompt").json(t); out["continue"] != true {
		t.Fatalf("%v", out)
	}
	if len(s.reports()) != 1 {
		t.Fatalf("坏 stdin 仍按 cwd 找到配置并排队上报: %d", len(s.reports()))
	}
}

func TestAsyncEvents_UnknownEventIsIgnored(t *testing.T) {
	s := newGoSandbox(t)
	f := newFakeMCP(t, map[string]any{"unread": 0})
	s.config(map[string]any{"workflow_id": "wf-1", "mcp_url": f.url(), "interval_seconds": 0})
	if out := s.run(s.project, toolPayload(s.project), tokenEnv(), "bogus-event").json(t); len(out) != 0 {
		t.Fatalf("%v", out)
	}
	if len(s.reports()) != 0 || len(f.hits()) != 0 {
		t.Fatal("未知事件什么都不该做")
	}
}

func TestAsyncEvents_SpawnFailureIsSwallowed(t *testing.T) {
	s := newGoSandbox(t)
	f := newFakeMCP(t, map[string]any{"unread": 0})
	s.config(map[string]any{"workflow_id": "wf-1", "mcp_url": f.url(), "interval_seconds": 0})
	spawnReport = func(reportJob) error { return errors.New("起不来") }
	if out := s.run(s.project, eventPayload(s.project, "sessionStart", nil), tokenEnv(), "session-start").json(t); len(out) != 0 {
		t.Fatalf("起不来子进程也要静默放行: %v", out)
	}
}

func TestAsyncEvents_ThoughtIsThrottledPerConversation(t *testing.T) {
	s := newGoSandbox(t)
	f := newFakeMCP(t, map[string]any{"unread": 0})
	s.config(map[string]any{"workflow_id": "wf-1", "mcp_url": f.url()})
	thought := func(conv string) {
		s.run(s.project, eventPayload(s.project, "afterAgentThought", map[string]any{"conversation_id": conv}), tokenEnv(), "after-agent-thought")
	}
	thought("conv-a")
	thought("conv-a")
	thought("conv-a")
	if n := len(s.reports()); n != 1 {
		t.Fatalf("同一对话 5 秒内只上报一次，实际 %d", n)
	}
	thought("conv-b")
	if n := len(s.reports()); n != 2 {
		t.Fatalf("另一个对话各算各的，实际 %d", n)
	}

	// 把记录拨到 10 秒前：节流窗口过了，应当再报一次。
	state := glob1(t, s.tmp, "relaymesh-ev-*.json")
	raw, _ := os.ReadFile(state)
	var m map[string]int64
	if json.Unmarshal(raw, &m) != nil || len(m) != 2 {
		t.Fatalf("节流记录应有两条: %s", raw)
	}
	for k := range m {
		m[k] -= 10_000
	}
	b, _ := json.Marshal(m)
	_ = os.WriteFile(state, b, 0o644)
	thought("conv-a")
	if n := len(s.reports()); n != 3 {
		t.Fatalf("窗口过了应再报一次，实际 %d", n)
	}
	assertNoSecret(t, string(raw), "节流记录", testToken)

	// 其他事件不受思考节流影响。
	s.run(s.project, eventPayload(s.project, "preToolUse", nil), tokenEnv(), "pre-tool-use")
	s.run(s.project, eventPayload(s.project, "preToolUse", nil), tokenEnv(), "pre-tool-use")
	if n := len(s.reports()); n != 5 {
		t.Fatalf("工具开始每次都要报，实际 %d", n)
	}
}

func TestPostToolUse_PiggybacksStatusFieldsOnTheReminderPeek(t *testing.T) {
	s := newGoSandbox(t)
	f := newFakeMCP(t, map[string]any{"remind": true, "unread": 1, "message": remindMsg})
	s.config(map[string]any{"workflow_id": "wf-1", "mcp_url": f.url()}) // 默认 15 秒节流

	out := s.run(s.project, eventPayload(s.project, "postToolUse", nil), tokenEnv(), "post-tool-use").json(t)
	if out["additional_context"] != remindMsg || len(out) != 1 {
		t.Fatalf("应原样转述提醒: %v", out)
	}
	hits := f.hits()
	if len(hits) != 1 || len(s.reports()) != 0 {
		t.Fatalf("节流放行时只发一次同步请求，不再另起子进程: 请求 %d 排队 %d", len(hits), len(s.reports()))
	}
	args := hits[0].args()
	if args["reminder"] != true || args["agent_event"] != "tool" || args["agent_tool"] != "Shell" || args["agent_duration_ms"] != 1234.0 {
		t.Fatalf("同一次请求要带上提醒标记与状态字段: %v", args)
	}
	if _, ok := args["agent_ts"].(float64); !ok {
		t.Fatalf("要带发送方时间: %v", args)
	}

	// 15 秒内再来一次：不再问提醒，但工具结束仍要报给状态核心——走子进程。
	out = s.run(s.project, eventPayload(s.project, "postToolUse", nil), tokenEnv(), "post-tool-use").json(t)
	if len(out) != 0 || len(f.hits()) != 1 {
		t.Fatalf("被节流的这次不该同步请求: %v 请求 %d", out, len(f.hits()))
	}
	jobs := s.reports()
	if len(jobs) != 1 || jobs[0].Report["agent_event"] != "tool" || jobs[0].Report["agent_tool"] != "Shell" {
		t.Fatalf("被节流的这次应把工具结束交给子进程: %+v", jobs)
	}
	s.flush(tokenEnv())
	hits = f.hits()
	if len(hits) != 2 {
		t.Fatalf("flush 后应收到第二次请求: %d", len(hits))
	}
	if _, has := hits[1].args()["reminder"]; has {
		t.Fatalf("子进程的上报不消耗提醒: %v", hits[1].args())
	}
}

func TestStop_KeepsLegacyWireNameAndAddsStatusFields(t *testing.T) {
	s := newGoSandbox(t)
	f := newFakeMCP(t, map[string]any{"unread": 2, "stop_message": stopMsg})
	s.config(map[string]any{"workflow_id": "wf-1", "mcp_url": f.url()})
	out := s.run(s.project, eventPayload(s.project, "stop", map[string]any{"status": "completed", "loop_count": 0}), tokenEnv(), "stop").json(t)
	if out["followup_message"] != stopMsg || len(out) != 1 {
		t.Fatalf("应拦一次: %v", out)
	}
	if len(s.reports()) != 0 {
		t.Fatal("stop 是同步的，不另起子进程")
	}
	args := f.hits()[0].args()
	if args["agent_event"] != "stop" || args["agent_status"] != "completed" || args["conversation_id"] != "conv-7" ||
		args["cursor_version"] != "3.2.1" || args["project_directory"] != s.project {
		t.Fatalf("%v", args)
	}
	if _, ok := args["agent_ts"].(float64); !ok {
		t.Fatalf("要带发送方时间: %v", args)
	}
	if _, has := args["reminder"]; has {
		t.Fatalf("stop 不消耗提醒: %v", args)
	}
}

func TestClientKindAndVersionTravelOnTheReport(t *testing.T) {
	s := newGoSandbox(t)
	f := newFakeMCP(t, map[string]any{"unread": 0})
	s.config(map[string]any{"workflow_id": "wf-1", "mcp_url": f.url()})
	env := append([]string{"CURSOR_INVOKED_AS=agent", "CURSOR_VERSION=2026.10.01-abc"}, tokenEnv()...)
	s.run(s.project, eventPayload(s.project, "sessionStart", map[string]any{"cursor_version": nil}), env, "session-start")
	s.flush(env)
	args := f.hits()[0].args()
	if args["client_kind"] != "cli" || args["cursor_version"] != "2026.10.01-abc" {
		t.Fatalf("CLI 标记与环境里的版本号应带上: %v", args)
	}
}

func TestBreaker_SkipsSpawningWhileTheHubIsDown(t *testing.T) {
	s := newGoSandbox(t)
	down := httptest.NewServer(http.NotFoundHandler())
	badURL := down.URL + "/mcp"
	down.Close()
	s.config(map[string]any{"workflow_id": "wf-1", "mcp_url": badURL})

	s.run(s.project, eventPayload(s.project, "sessionStart", nil), tokenEnv(), "session-start")
	if len(s.reports()) != 1 {
		t.Fatal("熔断前应排队")
	}
	// 子进程发现 Hub 连不上：留下熔断标记。
	s.flush(tokenEnv())
	marker := glob1(t, s.tmp, "relaymesh-ev-*.fail")
	b, _ := os.ReadFile(marker)
	assertNoSecret(t, string(b), "熔断标记", testToken)

	s.run(s.project, eventPayload(s.project, "preToolUse", nil), tokenEnv(), "pre-tool-use")
	s.run(s.project, eventPayload(s.project, "sessionEnd", nil), tokenEnv(), "session-end")
	if n := len(s.reports()); n != 0 {
		t.Fatalf("熔断期间不该再起子进程，实际排了 %d 个", n)
	}

	// 熔断过期后恢复。
	_ = os.WriteFile(marker, []byte("1"), 0o644)
	s.run(s.project, eventPayload(s.project, "preToolUse", nil), tokenEnv(), "pre-tool-use")
	if n := len(s.reports()); n != 1 {
		t.Fatalf("熔断过期后应恢复排队，实际 %d", n)
	}

	// Hub 恢复后成功上报，熔断标记清掉。
	f := newFakeMCP(t, map[string]any{"unread": 0})
	s.config(map[string]any{"workflow_id": "wf-1", "mcp_url": f.url()})
	// 配置换了地址，标记按（工作流 + 地址）区分，旧地址的标记不影响新地址。
	s.run(s.project, eventPayload(s.project, "preToolUse", nil), tokenEnv(), "pre-tool-use")
	s.flush(tokenEnv())
	if len(f.hits()) != 2 {
		t.Fatalf("新地址上应收到 2 次上报（旧地址排队的那条也会发到新配置里）: %d", len(f.hits()))
	}
}

func TestBreaker_SuccessfulReportClearsTheMarker(t *testing.T) {
	s := newGoSandbox(t)
	defer pushEnv(map[string]string{"TMPDIR": s.tmp})() // 下面直接调用熔断函数，标记要落在沙盒的临时目录里
	f := newFakeMCP(t, map[string]any{"unread": 0})
	s.config(map[string]any{"workflow_id": "wf-1", "mcp_url": f.url()})
	markReportFailure("wf-1", f.url())
	if !breakerOpen("wf-1", f.url()) {
		t.Fatal("标记失败应让熔断生效")
	}
	// 熔断中：父进程不排队。
	s.run(s.project, eventPayload(s.project, "sessionStart", nil), tokenEnv(), "session-start")
	if len(s.reports()) != 0 {
		t.Fatal("熔断中不该排队")
	}
	// 另一个工作流不受影响。
	if breakerOpen("wf-2", f.url()) {
		t.Fatal("熔断按工作流区分")
	}
	// 手动给一个任务让它成功一次，标记应被清掉。
	job, _ := json.Marshal(reportJob{ConfigPath: filepath.Join(s.project, ".cursor", "relaymesh.json"), Report: map[string]any{"agent_event": "session_start"}})
	if code := s.runReport(job, tokenEnv()); code != 0 {
		t.Fatalf("子进程退出码: %d", code)
	}
	if breakerOpen("wf-1", f.url()) {
		t.Fatal("成功后应清掉熔断标记")
	}
	if len(f.hits()) != 1 {
		t.Fatalf("应发出 1 次请求: %d", len(f.hits()))
	}
}

func TestPostToolUse_HubDownOpensBreakerForAsyncEventsOnly(t *testing.T) {
	s := newGoSandbox(t)
	defer pushEnv(map[string]string{"TMPDIR": s.tmp})()
	down := httptest.NewServer(http.NotFoundHandler())
	badURL := down.URL + "/mcp"
	down.Close()
	s.config(map[string]any{"workflow_id": "wf-1", "mcp_url": badURL, "interval_seconds": 0})
	// 同步路径：照常尝试并放行（失败静默），同时记下熔断。
	if out := s.run(s.project, eventPayload(s.project, "postToolUse", nil), tokenEnv(), "post-tool-use").json(t); len(out) != 0 {
		t.Fatalf("Hub 不可达也要放行: %v", out)
	}
	if !breakerOpen("wf-1", badURL) {
		t.Fatal("同步请求失败也应开启熔断，免得后面的异步事件各自去等超时")
	}
	s.run(s.project, eventPayload(s.project, "preToolUse", nil), tokenEnv(), "pre-tool-use")
	if len(s.reports()) != 0 {
		t.Fatal("熔断期间异步事件不排队")
	}
	// 同步路径不受熔断限制：每次仍会尝试（stop 必须能拿到 Hub 的拦截回复）。
	f := newFakeMCP(t, map[string]any{"unread": 2, "stop_message": stopMsg})
	s.config(map[string]any{"workflow_id": "wf-1", "mcp_url": f.url(), "interval_seconds": 0})
	markReportFailure("wf-1", f.url())
	out := s.run(s.project, eventPayload(s.project, "stop", nil), tokenEnv(), "stop").json(t)
	if out["followup_message"] != stopMsg {
		t.Fatalf("熔断不能影响 stop 的同步路径: %v", out)
	}
	if breakerOpen("wf-1", f.url()) {
		t.Fatal("同步请求成功后应清掉熔断")
	}
}

func TestRunReport_FailOpen(t *testing.T) {
	s := newGoSandbox(t)
	f := newFakeMCP(t, map[string]any{"unread": 0})
	cfgPath := filepath.Join(s.project, ".cursor", "relaymesh.json")
	ok := map[string]any{"agent_event": "session_start"}
	job := func(path string, report map[string]any) []byte {
		b, _ := json.Marshal(reportJob{ConfigPath: path, Report: report})
		return b
	}
	s.config(map[string]any{"workflow_id": "wf-1", "mcp_url": f.url()})
	cases := []struct {
		name  string
		stdin []byte
		env   []string
	}{
		{"标准输入为空", nil, tokenEnv()},
		{"不是 JSON", []byte("bad{"), tokenEnv()},
		{"没有配置路径", job("", ok), tokenEnv()},
		{"配置文件不存在", job(filepath.Join(s.project, "nope.json"), ok), tokenEnv()},
		{"未知事件", job(cfgPath, map[string]any{"agent_event": "rm -rf"}), tokenEnv()},
		{"没有事件", job(cfgPath, map[string]any{}), tokenEnv()},
		{"没有令牌", job(cfgPath, ok), nil},
	}
	for _, c := range cases {
		if code := s.runReport(c.stdin, c.env); code != 0 {
			t.Fatalf("%s: 退出码 %d", c.name, code)
		}
	}
	s.config(map[string]any{"workflow_id": "wf-1", "mcp_url": f.url(), "enabled": false})
	_ = s.runReport(job(cfgPath, ok), tokenEnv())
	s.config(map[string]any{"workflow_id": " ", "mcp_url": f.url()})
	_ = s.runReport(job(cfgPath, ok), tokenEnv())
	if n := len(f.hits()); n != 0 {
		t.Fatalf("这些情况都不该发请求，实际 %d", n)
	}

	s.config(map[string]any{"workflow_id": "wf-1", "mcp_url": f.url()})
	if code := s.runReport(job(cfgPath, ok), tokenEnv()); code != 0 || len(f.hits()) != 1 {
		t.Fatalf("正常任务应发出 1 次请求: 退出码 %d 请求 %d", code, len(f.hits()))
	}
}

func TestRunReport_RescrubsWhateverItIsGiven(t *testing.T) {
	s := newGoSandbox(t)
	f := newFakeMCP(t, map[string]any{"unread": 0})
	s.config(map[string]any{"workflow_id": "wf-1", "mcp_url": f.url()})
	dirty := map[string]any{
		"agent_event": "thought", "conversation_id": "c1",
		"prompt": "PROMPT-SECRET", "tool_input": map[string]any{"x": "TOOL-SECRET"}, "workflow_id": "other-wf", "reminder": true,
		"action": "inbox_ack", "source": "evil", "token": "TOKEN-SECRET", "client_kind": "toaster",
	}
	job, _ := json.Marshal(reportJob{ConfigPath: filepath.Join(s.project, ".cursor", "relaymesh.json"), Report: dirty})
	s.runReport(job, tokenEnv())
	hits := f.hits()
	if len(hits) != 1 {
		t.Fatalf("应发出 1 次请求: %d", len(hits))
	}
	args := hits[0].args()
	if args["action"] != "inbox_peek" || args["workflow_id"] != "wf-1" || args["source"] != "hook" {
		t.Fatalf("固定字段不能被任务覆盖: %v", args)
	}
	if _, has := args["reminder"]; has {
		t.Fatalf("任务不能偷偷消耗提醒: %v", args)
	}
	raw, _ := json.Marshal(hits[0].body)
	for _, secret := range []string{"SECRET", "other-wf", "evil", "toaster", "inbox_ack"} {
		if strings.Contains(string(raw), secret) {
			t.Fatalf("子进程应再清洗一遍，泄露了 %q: %s", secret, raw)
		}
	}
}

func TestChildEnv_DropsTheEmailVariableOnly(t *testing.T) {
	in := []string{"HOME=/h", "CURSOR_USER_EMAIL=a@b.c", "RELAYMESH_TOKEN=t", "cursor_user_email=lower@b.c", "PATH=/bin"}
	got := childEnv(in)
	if strings.Join(got, "|") != "HOME=/h|RELAYMESH_TOKEN=t|PATH=/bin" {
		t.Fatalf("只应去掉邮箱变量: %v", got)
	}
	for _, kv := range got {
		if strings.Contains(kv, "@") {
			t.Fatalf("不该剩下邮箱: %q", kv)
		}
	}
}

func TestEventThrottleOK_BoundsItsStateFile(t *testing.T) {
	s := newGoSandbox(t)
	defer pushEnv(map[string]string{"TMPDIR": s.tmp})()
	for i := 0; i < 150; i++ {
		if !eventThrottleOK("wf-1", "http://hub/mcp", "conv-"+string(rune('a'+i%26))+itoaJSON(int64(i)), "thought", 5*time.Second) {
			t.Fatalf("不同对话不该互相节流: %d", i)
		}
	}
	state := glob1(t, s.tmp, "relaymesh-ev-*.json")
	var m map[string]int64
	raw, _ := os.ReadFile(state)
	if json.Unmarshal(raw, &m) != nil {
		t.Fatalf("状态文件应是 JSON: %s", raw)
	}
	if len(m) > 64 {
		t.Fatalf("状态文件只留最近的 64 条，实际 %d", len(m))
	}
	if !eventThrottleOK("wf-1", "http://hub/mcp", "x", "thought", 0) {
		t.Fatal("间隔为 0 表示不节流")
	}
}
