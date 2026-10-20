package hookcli

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestGoPostToolUse_RemindsWithServerMessage(t *testing.T) {
	s := newGoSandbox(t)
	f := newFakeMCP(t, map[string]any{"remind": true, "unread": 1, "message": remindMsg})
	s.config(map[string]any{"workflow_id": "wf-1", "mcp_url": f.url(), "interval_seconds": 0})
	r := s.run(s.project, toolPayload(s.project), tokenEnv(), "post-tool-use")
	out := r.json(t)
	if out["additional_context"] != remindMsg || len(out) != 1 {
		t.Fatalf("应原样转述: %v", out)
	}
	hits := f.hits()
	if len(hits) != 1 || hits[0].auth != "Bearer "+testToken {
		t.Fatalf("Authorization: %+v", hits)
	}
	args := hits[0].args()
	want := map[string]any{"action": "inbox_peek", "workflow_id": "wf-1", "reminder": true, "source": "hook", "agent_event": "tool"}
	for k, v := range want {
		if args[k] != v {
			t.Fatalf("参数 %s: want %v got %v", k, v, args[k])
		}
	}
}

func TestGoPostToolUse_NothingToSay(t *testing.T) {
	s := newGoSandbox(t)
	f := newFakeMCP(t, map[string]any{"remind": false, "unread": 0})
	s.config(map[string]any{"workflow_id": "wf-1", "mcp_url": f.url(), "interval_seconds": 0})
	if len(s.run(s.project, toolPayload(s.project), tokenEnv(), "post-tool-use").json(t)) != 0 {
		t.Fatal("应输出 {}")
	}
	f.mu.Lock()
	f.peek = map[string]any{"remind": true, "unread": 1}
	f.mu.Unlock()
	if len(s.run(s.project, toolPayload(s.project), tokenEnv(), "post-tool-use").json(t)) != 0 {
		t.Fatal("无文案不应输出")
	}
}

func TestGoPostToolUse_ThrottlesLocally(t *testing.T) {
	s := newGoSandbox(t)
	f := newFakeMCP(t, map[string]any{"remind": true, "unread": 1, "message": remindMsg})
	s.config(map[string]any{"workflow_id": "wf-1", "mcp_url": f.url()})
	first := s.run(s.project, toolPayload(s.project), tokenEnv(), "post-tool-use").json(t)
	if first["additional_context"] != remindMsg {
		t.Fatal("第一次应提醒")
	}
	if len(s.run(s.project, toolPayload(s.project), tokenEnv(), "post-tool-use").json(t)) != 0 || len(f.hits()) != 1 {
		t.Fatal("15 秒内不应再问")
	}
	states, _ := filepath.Glob(filepath.Join(s.tmp, "relaymesh-inbox-*.json"))
	if len(states) != 1 {
		t.Fatalf("节流文件: %v", states)
	}
	old := time.Now().Add(-time.Hour).Unix()
	_ = os.WriteFile(states[0], []byte(`{"last": `+itoaJSON(old)+`}`), 0o644)
	third := s.run(s.project, toolPayload(s.project), tokenEnv(), "post-tool-use").json(t)
	if third["additional_context"] != remindMsg || len(f.hits()) != 2 {
		t.Fatalf("间隔后应再问: %v %d", third, len(f.hits()))
	}
	b, _ := os.ReadFile(states[0])
	assertNoSecret(t, string(b), "节流文件", testToken)
}

func TestGoPostToolUse_ThrottleIsPerWorkflow(t *testing.T) {
	s := newGoSandbox(t)
	f := newFakeMCP(t, map[string]any{"remind": false})
	s.config(map[string]any{"workflow_id": "wf-a", "mcp_url": f.url()})
	s.run(s.project, toolPayload(s.project), tokenEnv(), "post-tool-use")
	s.config(map[string]any{"workflow_id": "wf-b", "mcp_url": f.url()})
	s.run(s.project, toolPayload(s.project), tokenEnv(), "post-tool-use")
	if len(f.hits()) != 2 {
		t.Fatalf("工作流独立节流: %d", len(f.hits()))
	}
}

func TestGoStop_BlocksOnceWhenUnread(t *testing.T) {
	s := newGoSandbox(t)
	f := newFakeMCP(t, map[string]any{"unread": 2, "stop_message": stopMsg})
	s.config(map[string]any{"workflow_id": "wf-1", "mcp_url": f.url()})
	out := s.run(s.project, stopPayload(s.project, "completed", 0), tokenEnv(), "stop").json(t)
	if out["followup_message"] != stopMsg || len(out) != 1 {
		t.Fatalf("应拦一次: %v", out)
	}
	args := f.hits()[0].args()
	if _, has := args["reminder"]; has {
		t.Fatalf("stop 不消耗提醒: %v", args)
	}
}

func TestGoStop_LetsGoWhenNothingUnread(t *testing.T) {
	s := newGoSandbox(t)
	f := newFakeMCP(t, map[string]any{"unread": 0})
	s.config(map[string]any{"workflow_id": "wf-1", "mcp_url": f.url()})
	if len(s.run(s.project, stopPayload(s.project, "completed", 0), tokenEnv(), "stop").json(t)) != 0 {
		t.Fatal("无未读不拦")
	}
	f.mu.Lock()
	f.peek = map[string]any{"unread": 3}
	f.mu.Unlock()
	if len(s.run(s.project, stopPayload(s.project, "completed", 0), tokenEnv(), "stop").json(t)) != 0 {
		t.Fatal("无文案不拦")
	}
}

func TestGoStop_OnlyOnceAndOnlyWhenCompleted(t *testing.T) {
	s := newGoSandbox(t)
	f := newFakeMCP(t, map[string]any{"unread": 2, "stop_message": stopMsg})
	s.config(map[string]any{"workflow_id": "wf-1", "mcp_url": f.url()})
	cases := []struct {
		name, status string
		loop         int
	}{
		{"已经拦过一次", "completed", 1},
		{"拦过很多次", "completed", 5},
		{"用户中止", "aborted", 0},
		{"出错", "error", 0},
	}
	for _, c := range cases {
		if len(s.run(s.project, stopPayload(s.project, c.status, c.loop), tokenEnv(), "stop").json(t)) != 0 {
			t.Fatalf("%s 不应拦", c.name)
		}
	}
	hits := f.hits()
	if len(hits) != len(cases) {
		t.Fatalf("应汇报 %d 次", len(cases))
	}
	for i, c := range cases {
		args := hits[i].args()
		if args["agent_event"] != "stop" || args["agent_status"] != c.status {
			t.Fatalf("%s: %v", c.name, args)
		}
	}
}

func TestGoStop_MissingStatusIsReportedAsCompleted(t *testing.T) {
	s := newGoSandbox(t)
	f := newFakeMCP(t, map[string]any{"unread": 0})
	s.config(map[string]any{"workflow_id": "wf-1", "mcp_url": f.url()})
	payload, _ := json.Marshal(map[string]any{"hook_event_name": "stop", "conversation_id": "conv-7", "workspace_roots": []string{s.project}})
	s.run(s.project, string(payload), tokenEnv(), "stop")
	args := f.hits()[0].args()
	if args["agent_event"] != "stop" || args["agent_status"] != "completed" || args["conversation_id"] != "conv-7" {
		t.Fatalf("缺 status 按 completed: %v", args)
	}
}

func TestGoReport_ForwardsOnlyEventConversationAndStatus(t *testing.T) {
	s := newGoSandbox(t)
	f := newFakeMCP(t, map[string]any{"remind": false, "unread": 0})
	s.config(map[string]any{"workflow_id": "wf-1", "mcp_url": f.url(), "interval_seconds": 0})
	rich := func(event string, extra map[string]any) string {
		m := map[string]any{
			"hook_event_name": event, "conversation_id": "conv-7", "user_email": "someone@example.com",
			"tool_input": map[string]any{"command": "cat secrets"}, "workspace_roots": []string{s.project},
		}
		for k, v := range extra {
			m[k] = v
		}
		b, _ := json.Marshal(m)
		return string(b)
	}
	s.run(s.project, rich("postToolUse", nil), tokenEnv(), "post-tool-use")
	s.run(s.project, rich("stop", map[string]any{"status": "error", "loop_count": 0}), tokenEnv(), "stop")
	hits := f.hits()
	// v2 在原有三个字段之外只多了「发生了什么」的元数据：时间、工具名、耗时、客户端类型与版本、项目目录。
	allowed := map[string]bool{
		"action": true, "workflow_id": true, "source": true, "reminder": true,
		"agent_event": true, "agent_status": true, "conversation_id": true,
		"agent_ts": true, "agent_tool": true, "agent_duration_ms": true,
		"client_kind": true, "cursor_version": true, "project_directory": true,
	}
	for i, h := range hits {
		for k := range h.args() {
			if !allowed[k] {
				t.Fatalf("第 %d 次多余字段 %q", i, k)
			}
		}
		raw, _ := json.Marshal(h.body)
		for _, secret := range []string{"someone@example.com", "cat secrets"} {
			if strings.Contains(string(raw), secret) {
				t.Fatalf("泄露 %q", secret)
			}
		}
	}
}

func TestGoReport_ConversationIDIsBounded(t *testing.T) {
	s := newGoSandbox(t)
	f := newFakeMCP(t, map[string]any{"remind": false})
	s.config(map[string]any{"workflow_id": "wf-1", "mcp_url": f.url(), "interval_seconds": 0})
	long, _ := json.Marshal(map[string]any{"hook_event_name": "postToolUse", "conversation_id": strings.Repeat("a", 200), "workspace_roots": []string{s.project}})
	s.run(s.project, string(long), tokenEnv(), "post-tool-use")
	weird, _ := json.Marshal(map[string]any{"hook_event_name": "postToolUse", "conversation_id": 12345, "workspace_roots": []string{s.project}})
	s.run(s.project, string(weird), tokenEnv(), "post-tool-use")
	hits := f.hits()
	if got, _ := hits[0].args()["conversation_id"].(string); len(got) != 64 {
		t.Fatalf("截断 64: %d", len(got))
	}
	if _, has := hits[1].args()["conversation_id"]; has {
		t.Fatal("非字符串应忽略")
	}
}

func TestGoFailOpen(t *testing.T) {
	s := newGoSandbox(t)
	good := newFakeMCP(t, map[string]any{"remind": true, "unread": 1, "message": remindMsg})
	closed := httptest.NewServer(http.NotFoundHandler())
	u := closed.URL + "/mcp"
	closed.Close()
	cases := []struct {
		name  string
		setup func()
		stdin string
		env   []string
	}{
		{"没有配置文件", func() {}, toolPayload(s.project), tokenEnv()},
		{"工作流为空", func() { s.config(map[string]any{"workflow_id": " ", "mcp_url": good.url(), "interval_seconds": 0}) }, toolPayload(s.project), tokenEnv()},
		{"enabled false", func() { s.config(map[string]any{"workflow_id": "wf-1", "mcp_url": good.url(), "enabled": false}) }, toolPayload(s.project), tokenEnv()},
		{"没有令牌", func() { s.config(map[string]any{"workflow_id": "wf-1", "mcp_url": good.url(), "interval_seconds": 0}) }, toolPayload(s.project), nil},
		{"没有 MCP 地址", func() { s.config(map[string]any{"workflow_id": "wf-1"}) }, toolPayload(s.project), tokenEnv()},
		{"stdin 非 JSON", func() { s.config(map[string]any{"workflow_id": "wf-1", "mcp_url": good.url(), "interval_seconds": 0}) }, "bad{", tokenEnv()},
		{"服务端不可达", func() { s.config(map[string]any{"workflow_id": "wf-1", "mcp_url": u, "interval_seconds": 0}) }, toolPayload(s.project), tokenEnv()},
	}
	for _, c := range cases {
		c.setup()
		r := s.run(s.project, c.stdin, c.env, "post-tool-use")
		if len(r.json(t)) != 0 && c.name != "stdin 非 JSON" {
			t.Fatalf("%s: %v", c.name, r.out)
		}
	}
	s.config(map[string]any{"workflow_id": "wf-1", "mcp_url": good.url(), "interval_seconds": 0})
	if got := s.run(s.project, "bad{", tokenEnv(), "post-tool-use").json(t); got["additional_context"] != remindMsg {
		t.Fatalf("坏 stdin 仍按 cwd 工作: %v", got)
	}
}

func TestGoFailOpen_BadServerReplies(t *testing.T) {
	s := newGoSandbox(t)
	f := newFakeMCP(t, map[string]any{"remind": true, "message": remindMsg})
	s.config(map[string]any{"workflow_id": "wf-1", "mcp_url": f.url(), "interval_seconds": 0})
	for _, c := range []struct {
		name string
		set  func()
	}{
		{"HTTP 500", func() { f.status = 500 }},
		{"HTTP 401", func() { f.status = 401 }},
		{"非 JSON", func() { f.raw = "<html>" }},
		{"无文本", func() { f.raw = `{"jsonrpc":"2.0","id":1,"result":{"content":[]}}` }},
		{"工具错误", func() { f.isError = true }},
	} {
		f.mu.Lock()
		f.status, f.raw, f.isError = 0, "", false
		c.set()
		f.mu.Unlock()
		if len(s.run(s.project, toolPayload(s.project), tokenEnv(), "post-tool-use").json(t)) != 0 {
			t.Fatalf("%s 应 {}", c.name)
		}
	}
}

func TestGoFailOpen_SlowServerWithinTimeout(t *testing.T) {
	s := newGoSandbox(t)
	f := newFakeMCP(t, map[string]any{"remind": true, "message": remindMsg})
	f.delay = 4 * time.Second
	s.config(map[string]any{"workflow_id": "wf-1", "mcp_url": f.url(), "interval_seconds": 0})
	r := s.run(s.project, toolPayload(s.project), tokenEnv(), "post-tool-use")
	if len(r.json(t)) != 0 {
		t.Fatal("超时应静默")
	}
	if r.took > 3500*time.Millisecond {
		t.Fatalf("超过 HTTP 超时上限: %s", r.took)
	}
	if r.took > httpTimeout+200*time.Millisecond {
		t.Fatalf("应不超过 hook 配置的 %s: %s", httpTimeout, r.took)
	}
}

func TestGoStop_SlowServerFailsOpen(t *testing.T) {
	s := newGoSandbox(t)
	f := newFakeMCP(t, map[string]any{"unread": 2, "stop_message": stopMsg})
	f.delay = 4 * time.Second
	s.config(map[string]any{"workflow_id": "wf-1", "mcp_url": f.url()})
	r := s.run(s.project, stopPayload(s.project, "error", 0), tokenEnv(), "stop")
	if len(r.json(t)) != 0 || r.took > 3500*time.Millisecond {
		t.Fatalf("stop 出错应快速放行: %s", r.took)
	}
}

func TestGoFindsConfigThroughWorkspaceRootsAndCwd(t *testing.T) {
	s := newGoSandbox(t)
	f := newFakeMCP(t, map[string]any{"remind": true, "message": remindMsg})
	s.config(map[string]any{"workflow_id": "wf-1", "mcp_url": f.url(), "interval_seconds": 0})
	elsewhere := t.TempDir()
	if got := s.run(elsewhere, toolPayload(s.project), tokenEnv(), "post-tool-use").json(t); got["additional_context"] != remindMsg {
		t.Fatal("workspace_roots")
	}
	sub := filepath.Join(s.project, "frontend", "src")
	_ = os.MkdirAll(sub, 0o755)
	payload, _ := json.Marshal(map[string]any{"cwd": sub})
	if got := s.run(elsewhere, string(payload), tokenEnv(), "post-tool-use").json(t); got["additional_context"] != remindMsg {
		t.Fatal("cwd 向上找")
	}
}

func TestGoDoesNotUseConfigAtOrAboveHome(t *testing.T) {
	s := newGoSandbox(t)
	f := newFakeMCP(t, map[string]any{"remind": true, "message": remindMsg})
	s.writeJSON(filepath.Join(s.home, ".cursor", "relaymesh.json"), map[string]any{"workflow_id": "wf-home", "mcp_url": f.url(), "interval_seconds": 0})
	out := s.run(s.project, toolPayload(s.project), tokenEnv(), "post-tool-use").json(t)
	if len(out) != 0 || len(f.hits()) != 0 {
		t.Fatal("不借用主目录配置")
	}
}

func TestGoTokenFromMcpJSON(t *testing.T) {
	s := newGoSandbox(t)
	f := newFakeMCP(t, map[string]any{"remind": true, "message": remindMsg})
	s.config(map[string]any{"workflow_id": "wf-1", "mcp_url": f.url(), "interval_seconds": 0})
	s.mcpJSON(map[string]any{
		"relaymesh": map[string]any{"url": f.url() + "/", "headers": map[string]any{"authorization": "Bearer from-mcp-json"}},
	})
	if got := s.run(s.project, toolPayload(s.project), nil, "post-tool-use").json(t); got["additional_context"] != remindMsg {
		t.Fatal("mcp.json 令牌")
	}
	s.run(s.project, toolPayload(s.project), tokenEnv(), "post-tool-use")
	if f.hits()[1].auth != "Bearer "+testToken {
		t.Fatal("环境变量优先")
	}
}

func TestGoDefaultsToRelaymeshEntryInMcpJSON(t *testing.T) {
	s := newGoSandbox(t)
	f := newFakeMCP(t, map[string]any{"remind": true, "message": remindMsg})
	s.config(map[string]any{"workflow_id": "wf-1", "interval_seconds": 0})
	s.mcpJSON(map[string]any{"relaymesh": map[string]any{"url": f.url(), "headers": map[string]any{"Authorization": "Bearer default-entry"}}})
	if got := s.run(s.project, toolPayload(s.project), nil, "post-tool-use").json(t); got["additional_context"] != remindMsg {
		t.Fatal("默认 relaymesh 条目")
	}
}

func TestGoTokenInProjectConfigURL_IsNotUsed(t *testing.T) {
	s := newGoSandbox(t)
	f := newFakeMCP(t, map[string]any{"remind": true, "message": remindMsg})
	s.config(map[string]any{"workflow_id": "wf-1", "mcp_url": f.url() + "?token=rm-url-secret-123456", "interval_seconds": 0})
	out := s.run(s.project, toolPayload(s.project), nil, "post-tool-use").json(t)
	if len(out) != 0 || len(f.hits()) != 0 {
		t.Fatal("项目文件 URL 令牌不用")
	}
}

func TestGoThrottleKeyIgnoresTheToken(t *testing.T) {
	s := newGoSandbox(t)
	f := newFakeMCP(t, map[string]any{"remind": true, "message": remindMsg})
	s.config(map[string]any{"workflow_id": "wf-1"})
	s.mcpJSON(map[string]any{"relaymesh": map[string]any{"url": f.url() + "?token=rm-first-token-111111"}})
	s.run(s.project, toolPayload(s.project), nil, "post-tool-use")
	s.mcpJSON(map[string]any{"relaymesh": map[string]any{"url": f.url() + "?token=rm-second-token-222222"}})
	if len(s.run(s.project, toolPayload(s.project), nil, "post-tool-use").json(t)) != 0 || len(f.hits()) != 1 {
		t.Fatal("换令牌不重置节流")
	}
}
