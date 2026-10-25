package api

import (
	"net/http"
	"strings"
	"testing"
	"time"
)

func resetNtfyTestThrottle(srv *Server) {
	srv.handler.ntfyGuard.mu.Lock()
	srv.handler.ntfyGuard.lastTest = time.Now().Add(-time.Minute)
	srv.handler.ntfyGuard.mu.Unlock()
}

func ntfyTableOf(t *testing.T, out map[string]any) map[string]any {
	t.Helper()
	cfg, _ := out["config"].(map[string]any)
	table, ok := cfg["priority_by_kind"].(map[string]any)
	if !ok {
		t.Fatalf("priority_by_kind 应永远是对象：%v", cfg["priority_by_kind"])
	}
	return table
}

func TestNtfyAPI_KindsCatalogAndPriorityTableRoundTrip(t *testing.T) {
	srv, _ := newNtfyTestServer(t, nil)
	const path = "/api/v1/notify/ntfy"

	// 目录：每一类都有名字 / 说明 / 内置默认，异常类默认不推送。
	code, out, raw := ntfyCall(t, srv, http.MethodGet, path, nil)
	if code != http.StatusOK {
		t.Fatalf("GET：%d %s", code, raw)
	}
	kinds, _ := out["kinds"].([]any)
	if len(kinds) < 10 {
		t.Fatalf("应返回完整的消息类型目录：%s", raw)
	}
	byID := map[string]map[string]any{}
	for _, k := range kinds {
		m := k.(map[string]any)
		byID[m["id"].(string)] = m
		if m["label"] == "" || m["desc"] == "" || m["group"] == "" {
			t.Errorf("每一行都要有名字、说明与分组：%v", m)
		}
	}
	if byID["mention"]["default_priority"] != float64(4) || byID["ask_card"]["default_priority"] != float64(3) {
		t.Errorf("新消息类的内置默认应是 4 / 3：%v %v", byID["mention"], byID["ask_card"])
	}
	for _, id := range []string{"agent_error", "agent_stalled", "host_offline", "security_alert"} {
		if byID[id]["default_on"] != false || byID[id]["group"] != "exception" {
			t.Errorf("异常类 %s 默认必须不推送：%v", id, byID[id])
		}
	}
	if len(ntfyTableOf(t, out)) != 0 {
		t.Fatalf("全新安装不该有覆盖：%s", raw)
	}

	base := func(extra map[string]any) map[string]any {
		m := map[string]any{"enabled": false, "server": "https://ntfy.example.com", "topic": "rm-topic-1", "priority": 4}
		for k, v := range extra {
			m[k] = v
		}
		return m
	}

	// 保存覆盖表（含 0 = 不推送；未登记的键被丢掉），读回一致。
	code, out, raw = ntfyCall(t, srv, http.MethodPut, path, base(map[string]any{
		"priority_by_kind": map[string]any{"mention": 2, "ask_card": 0, "security_alert": 5, "ghost": 3},
	}))
	if code != http.StatusOK {
		t.Fatalf("PUT：%d %s", code, raw)
	}
	table := ntfyTableOf(t, out)
	if len(table) != 3 || table["mention"] != float64(2) || table["ask_card"] != float64(0) || table["security_alert"] != float64(5) {
		t.Fatalf("覆盖表不对（0 必须原样保留，未登记的键应丢掉）：%v", table)
	}
	_, out, _ = ntfyCall(t, srv, http.MethodGet, path, nil)
	if got := ntfyTableOf(t, out); len(got) != 3 || got["ask_card"] != float64(0) {
		t.Fatalf("读回的覆盖表不一致：%v", got)
	}

	// 老页面 / 别的客户端保存时没带这个字段：保持原样，不能顺手清掉。
	code, out, raw = ntfyCall(t, srv, http.MethodPut, path, base(map[string]any{"priority": 3}))
	if code != http.StatusOK {
		t.Fatalf("PUT：%d %s", code, raw)
	}
	if got := ntfyTableOf(t, out); len(got) != 3 || got["mention"] != float64(2) {
		t.Fatalf("没带字段应保持原覆盖表：%v", got)
	}

	// 值越界：400，并指出是哪一类。
	code, _, raw = ntfyCall(t, srv, http.MethodPut, path, base(map[string]any{"priority_by_kind": map[string]any{"mention": 7}}))
	if code != http.StatusBadRequest || !strings.Contains(raw, "群里 @ 了你") {
		t.Fatalf("越界应被拒绝并指出是哪一类：%d %s", code, raw)
	}
	if _, out, _ = ntfyCall(t, srv, http.MethodGet, path, nil); len(ntfyTableOf(t, out)) != 3 {
		t.Fatal("被拒绝的保存不该改动已存的覆盖表")
	}

	// 带空对象 = 清空，全部回到内置默认。
	code, out, raw = ntfyCall(t, srv, http.MethodPut, path, base(map[string]any{"priority_by_kind": map[string]any{}}))
	if code != http.StatusOK || len(ntfyTableOf(t, out)) != 0 {
		t.Fatalf("空对象应清空：%d %s", code, raw)
	}
}

func TestNtfyAPI_TestEndpointWithKind(t *testing.T) {
	fake := newFakeNtfy(t)
	srv, _ := newNtfyTestServer(t, nil)
	const testPath = "/api/v1/notify/ntfy/test"

	save := map[string]any{
		"enabled": false, "server": fake.URL, "topic": "saved-topic", "priority": 4,
		"priority_by_kind": map[string]any{"mention": 2},
	}
	if code, _, raw := ntfyCall(t, srv, http.MethodPut, "/api/v1/notify/ntfy", save); code != http.StatusOK {
		t.Fatalf("保存失败：%d %s", code, raw)
	}

	// 按已保存的表试发：用 mention 的 2 级，返回里写明类型与实际优先级。
	code, out, raw := ntfyCall(t, srv, http.MethodPost, testPath, map[string]any{"kind": "mention"})
	if code != http.StatusOK || out["ok"] != true || out["priority"] != float64(2) || out["kind"] != "mention" {
		t.Fatalf("应按 2 级试发并返回实际优先级：%d %s", code, raw)
	}
	posts := fake.waitFor(t, 1)
	if posts[0].Body["priority"] != float64(2) || !strings.Contains(posts[0].Body["title"].(string), "群里 @ 了你") {
		t.Fatalf("报文不对：%+v", posts[0])
	}

	// 用尚未保存的草稿试发：草稿里的表优先（让人先试后存）；默认关闭的异常类用建议值。
	resetNtfyTestThrottle(srv)
	draft := map[string]any{"draft": map[string]any{
		"enabled": false, "server": fake.URL, "topic": "saved-topic", "priority": 4,
		"priority_by_kind": map[string]any{"host_offline": 5},
	}, "kind": "host_offline"}
	code, out, raw = ntfyCall(t, srv, http.MethodPost, testPath, draft)
	if code != http.StatusOK || out["priority"] != float64(5) {
		t.Fatalf("草稿里的表应生效：%d %s", code, raw)
	}
	resetNtfyTestThrottle(srv)
	code, out, raw = ntfyCall(t, srv, http.MethodPost, testPath, map[string]any{"kind": "security_alert"})
	if code != http.StatusOK || out["priority"] != float64(5) {
		t.Fatalf("默认关闭的异常类试发应走建议值 5：%d %s", code, raw)
	}

	// 不带类型：普通测试推送，用默认优先级。
	resetNtfyTestThrottle(srv)
	code, out, raw = ntfyCall(t, srv, http.MethodPost, testPath, nil)
	if code != http.StatusOK || out["priority"] != float64(4) {
		t.Fatalf("普通测试推送应用默认优先级：%d %s", code, raw)
	}
	if _, has := out["kind"]; has {
		t.Errorf("普通测试推送不该带 kind：%s", raw)
	}

	// 不认识的类型：400，不发任何请求。
	resetNtfyTestThrottle(srv)
	before := len(fake.snapshot())
	if code, _, raw = ntfyCall(t, srv, http.MethodPost, testPath, map[string]any{"kind": "nope"}); code != http.StatusBadRequest {
		t.Fatalf("不认识的类型应返回 400：%d %s", code, raw)
	}
	if len(fake.snapshot()) != before {
		t.Error("被拒绝的试发不该发出请求")
	}
}
