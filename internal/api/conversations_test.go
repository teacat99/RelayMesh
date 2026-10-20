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

const chatWF = "wf-chat-rest"

func chatPath(wf, tail string) string {
	return "/api/v1/conversations/" + url.PathEscape("wf:"+wf) + tail
}

func chatItems(t *testing.T, res wiredResp) []map[string]any {
	t.Helper()
	raw, ok := res.Body["items"].([]any)
	if !ok {
		t.Fatalf("响应里没有 items: %d %s", res.Code, res.Raw)
	}
	out := make([]map[string]any, 0, len(raw))
	for _, it := range raw {
		out = append(out, it.(map[string]any))
	}
	return out
}

// 鉴权：匿名不能读写消息流。
func TestConversationsREST_RequiresLogin(t *testing.T) {
	e := newCfEnv(t)
	for _, tc := range []struct{ method, path string }{
		{"GET", chatPath(chatWF, "/messages")},
		{"POST", chatPath(chatWF, "/messages")},
		{"POST", chatPath(chatWF, "/read")},
		{"GET", "/api/v1/conversation-summaries?ids=wf:" + chatWF},
	} {
		if res := wiredCall(e.srv, tc.method, tc.path, map[string]any{"text": "x"}); res.Code != http.StatusUnauthorized {
			t.Fatalf("%s %s 匿名应 401，实际 %d", tc.method, tc.path, res.Code)
		}
	}
	if res := e.call("GET", "/api/v1/conversations/not-a-chat/messages", nil); res.Code != http.StatusBadRequest {
		t.Fatalf("会话编号无效应 400，实际 %d", res.Code)
	}
}

// 汇报 → 发送（并入这一轮）→ 再发（进收件箱）→ 确认卡与答复 → 已读：整条链路在一个消息流里。
func TestConversationsREST_SendCardsAndRead(t *testing.T) {
	e := newCfEnv(t)
	ctx := context.Background()
	st := e.srv.store

	// 先访问一次，让「已读纪元」落下，之后的汇报才算未读。
	if res := e.call("GET", chatPath(chatWF, "/messages"), nil); res.Code != http.StatusOK {
		t.Fatalf("首次读取: %d %s", res.Code, res.Raw)
	}
	time.Sleep(5 * time.Millisecond)

	sess, err := st.CreateFeedbackSession(ctx, store.CreateSessionInput{WorkflowID: chatWF, ProjectDirectory: "/tmp/p", Summary: "第一轮汇报", TimeoutSeconds: 600})
	if err != nil {
		t.Fatal(err)
	}
	res := e.call("GET", chatPath(chatWF, "/messages"), nil)
	items := chatItems(t, res)
	if len(items) != 1 || items[0]["kind"] != "block" || items[0]["status"] != "pending" {
		t.Fatalf("待答汇报应是一张 block/pending: %s", res.Raw)
	}
	if res.Body["unread"].(float64) != 1 {
		t.Fatalf("未读应为 1: %s", res.Raw)
	}

	// 引用这一轮发送：并入这一轮，文字带「【回复 哈希】」，状态不是 pending 了。
	hash := strings.TrimPrefix(sess.ID, "sess-")[:8]
	send := e.call("POST", chatPath(chatWF, "/messages"), map[string]any{"text": "收到，继续", "reply_to": "s:" + sess.ID})
	if send.Code != http.StatusOK || send.Body["delivered_as"] != "round_reply" {
		t.Fatalf("发送: %d %s", send.Code, send.Raw)
	}
	msg := send.Body["message"].(map[string]any)
	if !strings.HasPrefix(msg["text"].(string), "【回复 "+hash+"】") {
		t.Fatalf("引用前缀不对: %v", msg["text"])
	}
	if msg["sender"].(map[string]any)["kind"] != "human" {
		t.Fatalf("发送者应是 human: %v", msg["sender"])
	}
	if got := e.call("GET", chatPath(chatWF, "/messages"), nil); got.Body["unread"].(float64) != 0 {
		t.Fatalf("自己发消息后未读应清零: %s", got.Raw)
	}

	// 强制进收件箱：就算最近一轮能并入，也要排队。
	send = e.call("POST", chatPath(chatWF, "/messages"), map[string]any{"text": "另外一件事", "target": "inbox"})
	if send.Code != http.StatusOK || send.Body["delivered_as"] != "inbox" {
		t.Fatalf("target=inbox: %d %s", send.Code, send.Raw)
	}
	if res := e.call("POST", chatPath(chatWF, "/messages"), map[string]any{"text": "  "}); res.Code != http.StatusBadRequest {
		t.Fatalf("空消息应 400: %d", res.Code)
	}
	if res := e.call("POST", chatPath(chatWF, "/messages"), map[string]any{"text": "x", "target": "bogus"}); res.Code != http.StatusBadRequest {
		t.Fatalf("坏 target 应 400: %d", res.Code)
	}
	if res := e.call("POST", chatPath(chatWF, "/messages"), map[string]any{"text": "x", "reply_to": "bogus"}); res.Code != http.StatusBadRequest {
		t.Fatalf("坏 reply_to 应 400: %d", res.Code)
	}

	// 确认卡 + 答复：卡是 Agent 的消息，答复是你的，且答复有投递状态。
	e.ask(t, chatWF, "选哪个方案", "")
	if res := e.call("POST", cfBase(chatWF)+"/Q1/answer", map[string]any{"options": []string{"a"}}); res.Code != http.StatusOK {
		t.Fatalf("答复: %d %s", res.Code, res.Raw)
	}
	res = e.call("GET", chatPath(chatWF, "/messages"), nil)
	var card, answer map[string]any
	for _, it := range chatItems(t, res) {
		switch it["id"] {
		case "c:Q1":
			card = it
		case "a:Q1":
			answer = it
		}
	}
	if card == nil || answer == nil {
		t.Fatalf("消息流里应有卡与答复: %s", res.Raw)
	}
	if answer["sender"].(map[string]any)["kind"] != "human" {
		t.Fatalf("答复应是你发的: %v", answer)
	}

	// 摘要：最后一条预览 + 未读（卡是新的 Agent 消息，但答复推进了读状态以外的部分不动）。
	sum := e.call("GET", "/api/v1/conversation-summaries?ids="+url.QueryEscape("wf:"+chatWF), nil)
	list, _ := sum.Body["summaries"].([]any)
	if len(list) != 1 || list[0].(map[string]any)["preview"] == "" {
		t.Fatalf("摘要应有预览: %s", sum.Raw)
	}

	// 标已读：未读归零；坏游标 400。
	rd := e.call("POST", chatPath(chatWF, "/read"), map[string]any{})
	if rd.Code != http.StatusOK || rd.Body["unread"].(float64) != 0 {
		t.Fatalf("标已读: %d %s", rd.Code, rd.Raw)
	}
	if res := e.call("POST", chatPath(chatWF, "/read"), map[string]any{"cursor": "###"}); res.Code != http.StatusBadRequest {
		t.Fatalf("坏游标应 400: %d", res.Code)
	}
}

// SSE：一次答复会连着触发好几个事件，聊天流只收到合并后的一条 conversation_updated。
func TestConversationsSSE_DebouncedPerWorkflow(t *testing.T) {
	b := NewSSEBroker()
	ch := make(chan string, 20)
	b.clientsMu.Lock()
	b.clients[ch] = true
	b.clientsMu.Unlock()

	for i := 0; i < 5; i++ {
		b.Broadcast("queued_feedback_updated", map[string]any{"workflow_id": "wf-a"})
	}
	b.Broadcast("confirm_updated", map[string]any{"workflow_id": "wf-b"})
	b.Broadcast("agent_state", map[string]any{"workflow_id": "wf-c"}) // 不是消息流事件
	time.Sleep(conversationDebounce + 150*time.Millisecond)

	got := map[string]int{}
	for len(ch) > 0 {
		m := <-ch
		if strings.HasPrefix(m, "event: conversation_updated") {
			switch {
			case strings.Contains(m, `"workflow_id":"wf-a"`):
				got["a"]++
			case strings.Contains(m, `"workflow_id":"wf-b"`):
				got["b"]++
			default:
				got["other"]++
			}
		}
	}
	if got["a"] != 1 || got["b"] != 1 || got["other"] != 0 {
		t.Fatalf("防抖结果不对: %v", got)
	}
}
