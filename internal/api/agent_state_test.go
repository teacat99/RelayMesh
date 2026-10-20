package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/teacat99/RelayMesh/v2/internal/agentstate"
	"github.com/teacat99/RelayMesh/v2/internal/config"
	"github.com/teacat99/RelayMesh/v2/internal/store"
)

// Agent 身份与状态的 Web 接口（#113）。状态机与持久化的行为由 agentstate / store 的测试覆盖，这里验证接口层：
// 返回的内容、过滤与参数校验、改名与广播、以及登录保护。

func ingestForAPI(t *testing.T, st *store.Store, wf, host, dir, conv string, names ...string) store.AgentIngestResult {
	t.Helper()
	var res store.AgentIngestResult
	for _, n := range names {
		var err error
		res, err = st.IngestAgentEvent(context.Background(), store.AgentIngestInput{
			WorkflowID: wf, CredentialID: 1, HostName: host, ProjectDirectory: dir, ConversationID: conv, ClientKind: "cli",
			Event: agentstate.Event{Source: agentstate.SourceHook, Name: n, Received: time.Now()},
		})
		if err != nil {
			t.Fatalf("摄入 %s 失败: %v", n, err)
		}
		time.Sleep(2 * time.Millisecond)
	}
	return res
}

func mapList(t *testing.T, out map[string]any, key string) []map[string]any {
	t.Helper()
	raws, ok := out[key].([]any)
	if !ok {
		t.Fatalf("响应里没有数组 %q: %+v", key, out)
	}
	list := make([]map[string]any, 0, len(raws))
	for _, r := range raws {
		list = append(list, r.(map[string]any))
	}
	return list
}

func TestAgentStateAPI_ListIdentitiesAndSessions(t *testing.T) {
	srv, st := newNtfyTestServer(t, nil)

	// 空库：两个列表都是空数组（不是 null）。
	code, out, raw := ntfyCall(t, srv, http.MethodGet, "/api/v1/identities", nil)
	if code != http.StatusOK || len(mapList(t, out, "identities")) != 0 {
		t.Fatalf("空库的身份列表应为空数组: %d %s", code, raw)
	}
	code, out, raw = ntfyCall(t, srv, http.MethodGet, "/api/v1/agent-sessions", nil)
	if code != http.StatusOK || len(mapList(t, out, "sessions")) != 0 || !strings.Contains(raw, `"sessions":[]`) {
		t.Fatalf("空库的会话列表应为空数组: %d %s", code, raw)
	}

	alpha := ingestForAPI(t, st, "wf-alpha", "box", "/work/Alpha", "conv-a1", agentstate.EvSessionStart, agentstate.EvPrompt)
	ingestForAPI(t, st, "wf-alpha", "box", "/work/Alpha", "conv-a2", agentstate.EvSessionStart)
	beta := ingestForAPI(t, st, "wf-beta", "box", "/work/Beta", "conv-b1", agentstate.EvSessionStart, agentstate.EvPrompt, agentstate.EvToolStart)
	if alpha.IdentityID == 0 || beta.IdentityID == 0 || alpha.IdentityID == beta.IdentityID {
		t.Fatalf("应有两个不同的身份: %+v %+v", alpha, beta)
	}

	code, out, raw = ntfyCall(t, srv, http.MethodGet, "/api/v1/identities", nil)
	if code != http.StatusOK {
		t.Fatalf("GET identities: %d %s", code, raw)
	}
	ids := mapList(t, out, "identities")
	if len(ids) != 2 || out["now"] == nil {
		t.Fatalf("应有两个身份并带服务端时间: %s", raw)
	}
	labels := map[string]bool{}
	for _, v := range ids {
		labels[v["label"].(string)] = true
		if v["client_kind"] != "cli" || v["host_name"] != "box" {
			t.Fatalf("身份的字段不对: %+v", v)
		}
	}
	if !labels["Alpha"] || !labels["Beta"] {
		t.Fatalf("显示名应取目录名: %+v", labels)
	}

	code, out, raw = ntfyCall(t, srv, http.MethodGet, "/api/v1/agent-sessions", nil)
	if code != http.StatusOK {
		t.Fatalf("GET agent-sessions: %d %s", code, raw)
	}
	sessions := mapList(t, out, "sessions")
	if len(sessions) != 3 {
		t.Fatalf("应有三个会话: %s", raw)
	}
	if sessions[0]["conversation_id"] != "conv-b1" || sessions[0]["state"] != "working" || sessions[0]["sub"] != "tool:unknown" {
		t.Fatalf("最近有动静的会话应排在最前: %+v", sessions[0])
	}
	if sessions[0]["identity_label"] != "Beta" || sessions[0]["project_directory"] != "/work/Beta" {
		t.Fatalf("会话应带身份信息: %+v", sessions[0])
	}

	// 按工作流 / 身份过滤，limit 生效。
	_, out, _ = ntfyCall(t, srv, http.MethodGet, "/api/v1/agent-sessions?workflow_id=wf-alpha", nil)
	if got := mapList(t, out, "sessions"); len(got) != 2 {
		t.Fatalf("按工作流过滤应得两个会话: %+v", got)
	}
	_, out, _ = ntfyCall(t, srv, http.MethodGet, "/api/v1/agent-sessions?identity_id="+itoa(beta.IdentityID), nil)
	if got := mapList(t, out, "sessions"); len(got) != 1 || got[0]["conversation_id"] != "conv-b1" {
		t.Fatalf("按身份过滤应只得一个会话: %+v", got)
	}
	_, out, _ = ntfyCall(t, srv, http.MethodGet, "/api/v1/agent-sessions?limit=1", nil)
	if got := mapList(t, out, "sessions"); len(got) != 1 {
		t.Fatalf("limit=1 应只返回一个: %+v", got)
	}
	_, out, _ = ntfyCall(t, srv, http.MethodGet, "/api/v1/agent-sessions?workflow_id=wf-none", nil)
	if got := mapList(t, out, "sessions"); len(got) != 0 {
		t.Fatalf("没有的工作流应得空数组: %+v", got)
	}

	// 参数不合法：400。
	for _, q := range []string{"identity_id=abc", "identity_id=-1", "limit=abc", "limit=-3"} {
		if code, _, raw := ntfyCall(t, srv, http.MethodGet, "/api/v1/agent-sessions?"+q, nil); code != http.StatusBadRequest {
			t.Fatalf("%s 应返回 400: %d %s", q, code, raw)
		}
	}
}

func itoa(n uint) string { return strconv.FormatUint(uint64(n), 10) }

func TestAgentStateAPI_RenameIdentity(t *testing.T) {
	srv, st := newNtfyTestServer(t, nil)
	events := listenSSE(t, srv)
	res := ingestForAPI(t, st, "wf-r", "box", "/work/App", "conv-1", agentstate.EvSessionStart)
	path := "/api/v1/identities/" + itoa(res.IdentityID)

	code, out, raw := ntfyCall(t, srv, http.MethodPut, path, map[string]any{"name": "  后端组  "})
	if code != http.StatusOK {
		t.Fatalf("改名应成功: %d %s", code, raw)
	}
	ident := out["identity"].(map[string]any)
	if ident["name"] != "后端组" || ident["label"] != "后端组" || ident["directory_name"] != "App" {
		t.Fatalf("改名后的显示名不对: %+v", ident)
	}
	var sawEvent bool
	for _, m := range drainSSE(events) {
		if strings.Contains(m, "event: agent_identity_updated") && strings.Contains(m, `"identity_id":`+itoa(res.IdentityID)) {
			sawEvent = true
		}
	}
	if !sawEvent {
		t.Fatal("改名后应广播 agent_identity_updated")
	}
	// 会话里的显示名跟着变。
	_, out, _ = ntfyCall(t, srv, http.MethodGet, "/api/v1/agent-sessions?workflow_id=wf-r", nil)
	if got := mapList(t, out, "sessions"); len(got) != 1 || got[0]["identity_label"] != "后端组" {
		t.Fatalf("会话应带新的显示名: %+v", got)
	}

	// 空串恢复用目录名。
	code, out, raw = ntfyCall(t, srv, http.MethodPut, path, map[string]any{"name": ""})
	if code != http.StatusOK || out["identity"].(map[string]any)["label"] != "App" {
		t.Fatalf("空名应恢复目录名: %d %s", code, raw)
	}

	// 非法输入。
	if code, _, raw = ntfyCall(t, srv, http.MethodPut, path, map[string]any{"name": strings.Repeat("长", 81)}); code != http.StatusBadRequest {
		t.Fatalf("超长名字应返回 400: %d %s", code, raw)
	}
	if code, _, raw = ntfyCall(t, srv, http.MethodPut, path, map[string]any{"other": 1}); code != http.StatusBadRequest {
		t.Fatalf("没带 name 应返回 400: %d %s", code, raw)
	}
	if code, _, raw = ntfyCall(t, srv, http.MethodPut, path, nil); code != http.StatusBadRequest {
		t.Fatalf("没有请求体应返回 400: %d %s", code, raw)
	}
	if code, _, raw = ntfyCall(t, srv, http.MethodPut, "/api/v1/identities/abc", map[string]any{"name": "x"}); code != http.StatusBadRequest {
		t.Fatalf("非数字编号应返回 400: %d %s", code, raw)
	}
	if code, _, raw = ntfyCall(t, srv, http.MethodPut, "/api/v1/identities/0", map[string]any{"name": "x"}); code != http.StatusBadRequest {
		t.Fatalf("编号 0 应返回 400: %d %s", code, raw)
	}
	if code, _, raw = ntfyCall(t, srv, http.MethodPut, "/api/v1/identities/99999", map[string]any{"name": "x"}); code != http.StatusNotFound {
		t.Fatalf("不存在的身份应返回 404: %d %s", code, raw)
	}
	// 请求体过大：被拒绝，名字不变。
	if code, _, _ = ntfyCall(t, srv, http.MethodPut, path, map[string]any{"name": strings.Repeat("x", 4096)}); code != http.StatusBadRequest {
		t.Fatalf("过大的请求体应返回 400: %d", code)
	}
	// 项目约定局部修改用 PUT：PATCH 没有路由。
	if code, _, _ = ntfyCall(t, srv, http.MethodPatch, path, map[string]any{"name": "x"}); code == http.StatusOK {
		t.Fatalf("PATCH 不应被接受: %d", code)
	}
}

// 设了 Web 密码时，三个接口都必须带登录令牌。
func TestAgentStateAPI_RequiresLogin(t *testing.T) {
	srv, _ := newNtfyTestServer(t, func(c *config.Config) {
		c.WebUsername, c.WebPassword, c.JWTSecret = "admin", "correct-horse-battery-staple", testStrongJWTSecret
	})
	for _, c := range []struct{ method, path string }{
		{http.MethodGet, "/api/v1/identities"},
		{http.MethodPut, "/api/v1/identities/1"},
		{http.MethodGet, "/api/v1/agent-sessions"},
	} {
		req := httptest.NewRequest(c.method, c.path, strings.NewReader(`{"name":"x"}`))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		srv.Engine().ServeHTTP(w, req)
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("%s %s 没带令牌应返回 401: %d %s", c.method, c.path, w.Code, w.Body.String())
		}
	}
}
