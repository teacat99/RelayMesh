package api

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/teacat99/RelayMesh/v2/internal/store"
)

func groupMsgPath(id, tail string) string {
	return "/api/v1/conversations/" + url.PathEscape(id) + tail
}

func enableChatMode(t *testing.T, e *cfEnv, wf string) {
	t.Helper()
	e.srv.store.RecordHookSeen(wf, time.Now())
	if res := e.call("PUT", "/api/v1/workflows/"+wf+"/async-mode", map[string]any{"enabled": true}); res.Code != http.StatusOK {
		t.Fatalf("开启 %s 的聊天模式: %d %s", wf, res.Code, res.Raw)
	}
}

func TestGroupsREST_RequiresLogin(t *testing.T) {
	e := newCfEnv(t)
	for _, tc := range []struct{ method, path string }{
		{"GET", "/api/v1/groups"},
		{"GET", "/api/v1/groups/candidates"},
		{"POST", "/api/v1/groups"},
		{"GET", "/api/v1/groups/grp:abcd1234"},
		{"PUT", "/api/v1/groups/grp:abcd1234"},
		{"POST", "/api/v1/groups/grp:abcd1234/members"},
		{"DELETE", "/api/v1/groups/grp:abcd1234/members/wf-x"},
		{"PUT", "/api/v1/groups/grp:abcd1234/members/wf-x/policy"},
		{"POST", groupMsgPath("grp:abcd1234", "/messages")},
	} {
		if res := wiredCall(e.srv, tc.method, tc.path, map[string]any{"title": "x"}); res.Code != http.StatusUnauthorized {
			t.Fatalf("%s %s 匿名应 401，实际 %d", tc.method, tc.path, res.Code)
		}
	}
}

func TestGroupsREST_CreateSendRead(t *testing.T) {
	e := newCfEnv(t)
	enableChatMode(t, e, "wf-g1")
	enableChatMode(t, e, "wf-g2")

	// 没开聊天模式的工作流不能进群，且说明原因；什么都没建。
	res := e.call("POST", "/api/v1/groups", map[string]any{"title": "x", "workflow_ids": []string{"wf-g1", "wf-sync"}})
	if res.Code != http.StatusBadRequest || res.str("code") != "not_chat_mode" || !strings.Contains(res.str("error"), "wf-sync") {
		t.Fatalf("同步工作流应被拒绝: %d %s", res.Code, res.Raw)
	}
	if res := e.call("GET", "/api/v1/groups", nil); len(res.Body["groups"].([]any)) != 0 {
		t.Fatalf("被拒绝的建群不该留下群: %s", res.Raw)
	}

	res = e.call("POST", "/api/v1/groups", map[string]any{"title": "联调群", "workflow_ids": []string{"wf-g1", "wf-g2"}})
	if res.Code != http.StatusOK {
		t.Fatalf("建群: %d %s", res.Code, res.Raw)
	}
	gid := res.Body["group"].(map[string]any)["id"].(string)
	if !strings.HasPrefix(gid, "grp:") {
		t.Fatalf("群编号: %s", gid)
	}

	// 详情带成员；列表带群。
	info := e.call("GET", "/api/v1/groups/"+url.PathEscape(gid), nil)
	if info.Code != http.StatusOK || len(info.Body["members"].([]any)) != 3 {
		t.Fatalf("群详情应有 人+2 个 Agent: %d %s", info.Code, info.Raw)
	}

	// 人发消息：@ 其中一个；返回投递数；消息流里出现且 mentions 带出。
	send := e.call("POST", groupMsgPath(gid, "/messages"), map[string]any{"text": "g1 先看一下", "mentions": []string{"wf-g1"}})
	if send.Code != http.StatusOK || send.Body["delivered_to"] == nil {
		t.Fatalf("发送: %d %s", send.Code, send.Raw)
	}
	if res := e.call("POST", groupMsgPath(gid, "/messages"), map[string]any{"text": "  "}); res.Code != http.StatusBadRequest {
		t.Fatalf("空消息应 400: %d", res.Code)
	}
	if res := e.call("POST", groupMsgPath(gid, "/messages"), map[string]any{"text": "x", "mentions": []string{"wf-nobody"}}); res.Code == http.StatusOK {
		t.Fatalf("@ 外人不应成功: %s", res.Raw)
	}
	page := e.call("GET", groupMsgPath(gid, "/messages"), nil)
	items := chatItems(t, page)
	var got bool
	for _, it := range items {
		if it["text"] == "g1 先看一下" {
			got = true
			if it["sender"].(map[string]any)["kind"] != "human" {
				t.Fatalf("发送者: %v", it["sender"])
			}
		}
	}
	if !got || page.Body["group"] == nil {
		t.Fatalf("消息流应含这条且带群信息: %s", page.Raw)
	}
	if res := e.call("POST", groupMsgPath(gid, "/read"), map[string]any{}); res.Code != http.StatusOK {
		t.Fatalf("已读: %d %s", res.Code, res.Raw)
	}

	// 移出成员 → 只剩一个 Agent；归档后不能再发。
	if res := e.call("DELETE", "/api/v1/groups/"+url.PathEscape(gid)+"/members/wf-g2", nil); res.Code != http.StatusOK {
		t.Fatalf("移出: %d %s", res.Code, res.Raw)
	}
	if res := e.call("PUT", "/api/v1/groups/"+url.PathEscape(gid)+"/members/wf-g1/policy", map[string]any{"push_policy": "none"}); res.Code != http.StatusOK {
		t.Fatalf("改策略: %d %s", res.Code, res.Raw)
	}
	if res := e.call("PUT", "/api/v1/groups/"+url.PathEscape(gid), map[string]any{"archived": true}); res.Code != http.StatusOK {
		t.Fatalf("归档: %d %s", res.Code, res.Raw)
	}
	if res := e.call("POST", groupMsgPath(gid, "/messages"), map[string]any{"text": "还能发吗"}); res.Code != http.StatusConflict && res.Code != http.StatusBadRequest {
		t.Fatalf("归档后发送应被拒绝: %d %s", res.Code, res.Raw)
	}
}

// 候选既含有 Agent 状态会话的工作流，也含只发过汇报的工作流；没开聊天模式的置灰并带原因。
func TestGroupsREST_Candidates(t *testing.T) {
	e := newCfEnv(t)
	enableChatMode(t, e, "wf-cand-ok")
	for _, wf := range []string{"wf-cand-ok", "wf-cand-sync"} {
		if _, err := e.srv.store.CreateFeedbackSession(context.Background(), store.CreateSessionInput{WorkflowID: wf, ProjectDirectory: "/tmp/proj-" + wf, Summary: "汇报", TimeoutSeconds: 60}); err != nil {
			t.Fatal(err)
		}
	}
	res := e.call("GET", "/api/v1/groups/candidates", nil)
	if res.Code != http.StatusOK {
		t.Fatalf("候选: %d %s", res.Code, res.Raw)
	}
	byWF := map[string]map[string]any{}
	for _, c := range res.Body["candidates"].([]any) {
		m := c.(map[string]any)
		byWF[m["workflow_id"].(string)] = m
	}
	if ok := byWF["wf-cand-ok"]; ok == nil || ok["eligible"] != true || ok["name"] != "proj-wf-cand-ok" {
		t.Fatalf("聊天模式的工作流应可选且名字取目录名: %v", ok)
	}
	if sync := byWF["wf-cand-sync"]; sync == nil || sync["eligible"] != false || !strings.Contains(sync["reason"].(string), "聊天") {
		t.Fatalf("同步工作流应置灰并说明: %v", sync)
	}
}

func TestGroupsSSE_TouchesGroupConversation(t *testing.T) {
	b := NewSSEBroker()
	ch := make(chan string, 20)
	b.clientsMu.Lock()
	b.clients[ch] = true
	b.clientsMu.Unlock()
	for i := 0; i < 4; i++ {
		b.Broadcast("conversation_changed", map[string]any{"group_id": "grp:abcd1234"})
	}
	time.Sleep(conversationDebounce + 150*time.Millisecond)
	n := 0
	for len(ch) > 0 {
		m := <-ch
		if strings.HasPrefix(m, "event: conversation_updated") && strings.Contains(m, `"conversation_id":"grp:abcd1234"`) {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("同一群的多次变化应合并成 1 次推送，实际 %d", n)
	}
}
