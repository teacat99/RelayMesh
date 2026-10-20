package api

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/teacat99/RelayMesh/v2/internal/config"
	"github.com/teacat99/RelayMesh/v2/internal/notify"
)

// 通知偏好的 Web 接口（#115）：默认值、局部修改、会话级策略、参数校验、广播与登录保护。
// 决策本身由 notify 包的测试覆盖，偏好的持久化由 store 的测试覆盖，这里只验证接口层。

func notifyPrefsOf(t *testing.T, out map[string]any) map[string]any {
	t.Helper()
	prefs, ok := out["prefs"].(map[string]any)
	if !ok {
		t.Fatalf("响应里没有 prefs：%+v", out)
	}
	return prefs
}

func sawEvent(msgs []string, name string) bool {
	for _, m := range msgs {
		if strings.Contains(m, "event: "+name) {
			return true
		}
	}
	return false
}

func TestNotifyPrefsAPI_DefaultsAndGlobalUpdate(t *testing.T) {
	srv, _ := newNtfyTestServer(t, nil)
	events := listenSSE(t, srv)

	code, out, raw := ntfyCall(t, srv, http.MethodGet, "/api/v1/notify/prefs", nil)
	if code != http.StatusOK {
		t.Fatalf("GET 应返回 200：%d %s", code, raw)
	}
	prefs := notifyPrefsOf(t, out)
	if prefs["dnd"] != false || prefs["ntfy_preview"] != "auto" || out["now"] == nil {
		t.Fatalf("出厂默认不对：%s", raw)
	}
	if _, has := prefs["conversations"]; has {
		t.Fatalf("没有会话设置时不应输出 conversations：%s", raw)
	}

	// 只改 dnd：预览模式保持。
	code, out, raw = ntfyCall(t, srv, http.MethodPut, "/api/v1/notify/prefs", map[string]any{"dnd": true})
	if code != http.StatusOK {
		t.Fatalf("PUT dnd 应成功：%d %s", code, raw)
	}
	if prefs = notifyPrefsOf(t, out); prefs["dnd"] != true || prefs["ntfy_preview"] != "auto" {
		t.Fatalf("只改 dnd 时预览模式应保持：%s", raw)
	}
	if !sawEvent(drainSSE(events), "notify_prefs_updated") {
		t.Fatal("修改后应广播 notify_prefs_updated")
	}

	// 只改预览模式：dnd 保持。
	code, out, raw = ntfyCall(t, srv, http.MethodPut, "/api/v1/notify/prefs", map[string]any{"ntfy_preview": "never"})
	if code != http.StatusOK {
		t.Fatalf("PUT ntfy_preview 应成功：%d %s", code, raw)
	}
	if prefs = notifyPrefsOf(t, out); prefs["dnd"] != true || prefs["ntfy_preview"] != "never" {
		t.Fatalf("只改预览模式时 dnd 应保持：%s", raw)
	}

	// 读回一致。
	_, out, raw = ntfyCall(t, srv, http.MethodGet, "/api/v1/notify/prefs", nil)
	if prefs = notifyPrefsOf(t, out); prefs["dnd"] != true || prefs["ntfy_preview"] != "never" {
		t.Fatalf("读回的偏好不对：%s", raw)
	}

	// 关掉免打扰。
	if code, out, _ = ntfyCall(t, srv, http.MethodPut, "/api/v1/notify/prefs", map[string]any{"dnd": false}); code != http.StatusOK || notifyPrefsOf(t, out)["dnd"] != false {
		t.Fatalf("应能关掉免打扰：%d %+v", code, out)
	}
}

func TestNotifyPrefsAPI_GlobalUpdateValidation(t *testing.T) {
	srv, st := newNtfyTestServer(t, nil)
	events := listenSSE(t, srv)

	bad := []struct {
		name string
		body any
	}{
		{"没有任何字段", map[string]any{}},
		{"只有无关字段", map[string]any{"other": 1}},
		{"非法预览模式", map[string]any{"ntfy_preview": "sometimes"}},
		{"空预览模式", map[string]any{"ntfy_preview": ""}},
		{"没有请求体", nil},
		{"过大的请求体", map[string]any{"ntfy_preview": strings.Repeat("x", 8192)}},
	}
	for _, c := range bad {
		if code, _, raw := ntfyCall(t, srv, http.MethodPut, "/api/v1/notify/prefs", c.body); code != http.StatusBadRequest {
			t.Errorf("%s：应返回 400，得到 %d %s", c.name, code, raw)
		}
	}
	// 被拒绝的请求既不落库也不广播。
	got, _ := st.GetNotifyPrefs(context.Background())
	if got.DND || got.NtfyPreview != notify.PreviewAuto {
		t.Fatalf("被拒绝的请求不应改变偏好：%+v", got)
	}
	if sawEvent(drainSSE(events), "notify_prefs_updated") {
		t.Fatal("被拒绝的请求不应广播")
	}
}

func TestNotifyPrefsAPI_ConversationPolicy(t *testing.T) {
	srv, _ := newNtfyTestServer(t, nil)
	events := listenSSE(t, srv)

	set := func(body map[string]any) (int, map[string]any, string) {
		return ntfyCall(t, srv, http.MethodPut, "/api/v1/notify/conversations", body)
	}
	convs := func(out map[string]any) map[string]any {
		c, _ := notifyPrefsOf(t, out)["conversations"].(map[string]any)
		return c
	}

	// 1:1 静音（没有期限）。
	code, out, raw := set(map[string]any{"conversation": "wf:demo", "policy": "muted"})
	if code != http.StatusOK {
		t.Fatalf("静音应成功：%d %s", code, raw)
	}
	if got := convs(out)["wf:demo"].(map[string]any); got["policy"] != "muted" || got["muted_until"] != nil {
		t.Fatalf("静音设置不对：%s", raw)
	}
	if !sawEvent(drainSSE(events), "notify_prefs_updated") {
		t.Fatal("设置会话策略后应广播 notify_prefs_updated")
	}

	// 带期限的静音。
	until := time.Now().Add(2 * time.Hour).UTC().Format(time.RFC3339)
	code, out, raw = set(map[string]any{"conversation": "wf:timed", "policy": "muted", "muted_until": until})
	if code != http.StatusOK || convs(out)["wf:timed"].(map[string]any)["muted_until"] == nil {
		t.Fatalf("带期限的静音应成功并带回期限：%d %s", code, raw)
	}

	// 群里显式设为 all（与群默认不同）会保留；设成与默认相同的 mentions 则不占位置。
	if code, out, _ = set(map[string]any{"conversation": "grp:g1", "policy": "all"}); code != http.StatusOK || convs(out)["grp:g1"] == nil {
		t.Fatalf("群显式 all 应保留：%d %+v", code, out)
	}
	if code, out, _ = set(map[string]any{"conversation": "grp:g1", "policy": "mentions"}); code != http.StatusOK || convs(out)["grp:g1"] != nil {
		t.Fatalf("设成默认策略应等于恢复默认（不占位置）：%d %+v", code, out)
	}
	if code, out, _ = set(map[string]any{"conversation": "wf:demo2", "policy": "all"}); code != http.StatusOK || convs(out)["wf:demo2"] != nil {
		t.Fatalf("1:1 设成 all（默认）不应占位置：%d %+v", code, out)
	}

	// 覆盖：静音改回 all 即恢复。
	if code, out, _ = set(map[string]any{"conversation": "wf:demo", "policy": "all"}); code != http.StatusOK || convs(out)["wf:demo"] != nil {
		t.Fatalf("改回 all 应恢复默认：%d %+v", code, out)
	}

	// 恢复默认：DELETE，没有设置过的也算成功。
	if code, out, _ = ntfyCall(t, srv, http.MethodDelete, "/api/v1/notify/conversations?conversation=wf:timed", nil); code != http.StatusOK || convs(out)["wf:timed"] != nil {
		t.Fatalf("DELETE 应恢复默认：%d %+v", code, out)
	}
	if code, _, raw = ntfyCall(t, srv, http.MethodDelete, "/api/v1/notify/conversations?conversation=wf:never-set", nil); code != http.StatusOK {
		t.Fatalf("没设置过的会话 DELETE 也应成功：%d %s", code, raw)
	}
}

func TestNotifyPrefsAPI_ConversationValidation(t *testing.T) {
	srv, st := newNtfyTestServer(t, nil)
	past := time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)
	future := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)

	bad := []struct {
		name string
		body any
	}{
		{"缺会话", map[string]any{"policy": "muted"}},
		{"会话 id 没有前缀", map[string]any{"conversation": "demo", "policy": "muted"}},
		{"会话 id 标识为空", map[string]any{"conversation": "wf:", "policy": "muted"}},
		{"会话 id 太长", map[string]any{"conversation": "wf:" + strings.Repeat("x", 201), "policy": "muted"}},
		{"缺策略", map[string]any{"conversation": "wf:demo"}},
		{"策略未知", map[string]any{"conversation": "wf:demo", "policy": "loud"}},
		{"期限已过", map[string]any{"conversation": "wf:demo", "policy": "muted", "muted_until": past}},
		{"期限与非静音策略并用", map[string]any{"conversation": "wf:demo", "policy": "all", "muted_until": future}},
		{"期限格式错误", map[string]any{"conversation": "wf:demo", "policy": "muted", "muted_until": "tomorrow"}},
		{"没有请求体", nil},
	}
	for _, c := range bad {
		if code, _, raw := ntfyCall(t, srv, http.MethodPut, "/api/v1/notify/conversations", c.body); code != http.StatusBadRequest {
			t.Errorf("%s：应返回 400，得到 %d %s", c.name, code, raw)
		}
	}
	if code, _, raw := ntfyCall(t, srv, http.MethodDelete, "/api/v1/notify/conversations", nil); code != http.StatusBadRequest {
		t.Errorf("DELETE 缺会话应返回 400：%d %s", code, raw)
	}
	if code, _, raw := ntfyCall(t, srv, http.MethodDelete, "/api/v1/notify/conversations?conversation=nope", nil); code != http.StatusBadRequest {
		t.Errorf("DELETE 会话 id 无效应返回 400：%d %s", code, raw)
	}
	if got, _ := st.GetNotifyPrefs(context.Background()); len(got.Conversations) != 0 {
		t.Fatalf("被拒绝的请求不应落库：%+v", got.Conversations)
	}
}

func TestNotifyPrefsAPI_ConversationLimit(t *testing.T) {
	srv, st := newNtfyTestServer(t, nil)
	ctx := context.Background()
	if _, err := st.UpdateNotifyPrefs(ctx, func(cur notify.Prefs) (notify.Prefs, error) {
		cur.Conversations = map[string]notify.ConvPref{}
		for i := 0; i < notify.MaxConversations; i++ {
			cur.Conversations[fmt.Sprintf("wf:c%d", i)] = notify.ConvPref{Policy: notify.PolicyMuted}
		}
		return cur, nil
	}); err != nil {
		t.Fatal(err)
	}

	// 第 501 个：400，并提示先清理。
	code, _, raw := ntfyCall(t, srv, http.MethodPut, "/api/v1/notify/conversations", map[string]any{"conversation": "wf:one-more", "policy": "muted"})
	if code != http.StatusBadRequest || !strings.Contains(raw, "最多") {
		t.Fatalf("超过上限应返回 400 并提示：%d %s", code, raw)
	}
	// 已有的会话仍可修改；删除一个之后又能新增。
	if code, _, raw = ntfyCall(t, srv, http.MethodPut, "/api/v1/notify/conversations", map[string]any{"conversation": "wf:c0", "policy": "mentions"}); code != http.StatusOK {
		t.Fatalf("修改已有会话不受上限影响：%d %s", code, raw)
	}
	if code, _, raw = ntfyCall(t, srv, http.MethodDelete, "/api/v1/notify/conversations?conversation=wf:c1", nil); code != http.StatusOK {
		t.Fatalf("删除应成功：%d %s", code, raw)
	}
	if code, _, raw = ntfyCall(t, srv, http.MethodPut, "/api/v1/notify/conversations", map[string]any{"conversation": "wf:one-more", "policy": "muted"}); code != http.StatusOK {
		t.Fatalf("腾出位置后应能新增：%d %s", code, raw)
	}
}

// 设了 Web 密码时，四个接口都必须带登录令牌。
func TestNotifyPrefsAPI_RequiresLogin(t *testing.T) {
	srv, _ := newNtfyTestServer(t, func(c *config.Config) {
		c.WebUsername, c.WebPassword, c.JWTSecret = "admin", "correct-horse-battery-staple", testStrongJWTSecret
	})
	for _, c := range []struct{ method, path string }{
		{http.MethodGet, "/api/v1/notify/prefs"},
		{http.MethodPut, "/api/v1/notify/prefs"},
		{http.MethodPut, "/api/v1/notify/conversations"},
		{http.MethodDelete, "/api/v1/notify/conversations?conversation=wf:demo"},
	} {
		req := httptest.NewRequest(c.method, c.path, strings.NewReader(`{"dnd":true}`))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		srv.Engine().ServeHTTP(w, req)
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("%s %s 没带令牌应返回 401：%d %s", c.method, c.path, w.Code, w.Body.String())
		}
	}
}
