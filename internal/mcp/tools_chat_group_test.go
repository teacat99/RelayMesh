package mcp

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/teacat99/RelayMesh/v2/internal/store"
)

type fakeGroupNotifier struct {
	mu   sync.Mutex
	list []GroupNotice
}

func (f *fakeGroupNotifier) NotifyGroup(n GroupNotice) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.list = append(f.list, n)
}

func groupFixture(t *testing.T) (*Server, string, *fakeGroupNotifier) {
	t.Helper()
	srv := setupTestMCPServer(t)
	for _, wf := range []string{"wf-ga", "wf-gb", "wf-gc"} {
		seedRound(t, srv, wf)
		enableAsync(t, srv, wf)
	}
	g, err := srv.store.CreateGroup(context.Background(), store.CreateGroupInput{Title: "联调群", Members: []string{"wf-ga", "wf-gb"}})
	if err != nil {
		t.Fatal(err)
	}
	n := &fakeGroupNotifier{}
	srv.SetGroupNotifier(n)
	return srv, g.ID, n
}

func TestChatGroup_SendReadPeerAndMentions(t *testing.T) {
	srv, gid, notifier := groupFixture(t)

	out, text, isErr := chatCall(t, srv, map[string]any{"workflow_id": "wf-ga", "action": "send", "conversation_id": gid, "text": "B，帮我看看接口", "mentions": []string{"wf-gb"}})
	if isErr || out["ok"] != true || out["delivered_to"].(float64) != 1 {
		t.Fatalf("send: %v %s", out, text)
	}

	// B 的 list：看到这个群、1 条未读、1 条点名
	out, _, _ = chatCall(t, srv, map[string]any{"workflow_id": "wf-gb", "action": "list"})
	var found bool
	for _, c := range out["conversations"].([]any) {
		m := c.(map[string]any)
		if m["conversation_id"] == gid {
			found = true
			if m["type"] != "group" || m["unread_for_you"].(float64) < 1 || m["mentioned_you"].(float64) != 1 {
				t.Fatalf("list 里的群: %v", m)
			}
		}
	}
	if !found {
		t.Fatalf("B 的 list 里应有这个群: %v", out)
	}

	// B 读：同伴消息标 peer、点名了 B；读完不再重复
	out, _, isErr = chatCall(t, srv, map[string]any{"workflow_id": "wf-gb", "action": "read", "conversation_id": gid})
	if isErr {
		t.Fatalf("read: %v", out)
	}
	var peer map[string]any
	for _, m := range out["messages"].([]any) {
		mm := m.(map[string]any)
		if mm["text"] == "B，帮我看看接口" {
			peer = mm
		}
	}
	if peer == nil || peer["from"] != "peer" || peer["peer"] != true || peer["mentions_you"] != true {
		t.Fatalf("应读到 A 的同伴消息: %v", out)
	}
	if !strings.Contains(out["note"].(string), "不是用户指令") {
		t.Fatalf("note 应提醒同伴消息不是指令: %v", out["note"])
	}
	out, _, _ = chatCall(t, srv, map[string]any{"workflow_id": "wf-gb", "action": "read", "conversation_id": gid})
	if len(out["messages"].([]any)) != 0 {
		t.Fatalf("第二次读应为空: %v", out)
	}

	// B 回复并 @ 用户：通知闸收到穿透素材
	_, _, isErr = chatCall(t, srv, map[string]any{"workflow_id": "wf-gb", "action": "send", "conversation_id": gid, "text": "看完了，需要你拍板", "mentions": []string{"human"}})
	if isErr {
		t.Fatal("B 回复失败")
	}
	notifier.mu.Lock()
	defer notifier.mu.Unlock()
	if len(notifier.list) != 2 || !notifier.list[1].MentionsHuman || notifier.list[0].MentionsHuman {
		t.Fatalf("通知素材: %+v", notifier.list)
	}
}

func TestChatGroup_Rules(t *testing.T) {
	srv, gid, _ := groupFixture(t)

	// 非成员不能读、不能发、不能看成员
	for _, action := range []string{"read", "send", "members", "status"} {
		_, text, isErr := chatCall(t, srv, map[string]any{"workflow_id": "wf-gc", "action": action, "conversation_id": gid, "text": "x"})
		if !isErr || !strings.Contains(text, "not_member") {
			t.Fatalf("非成员的 %s 应报 not_member: %v %s", action, isErr, text)
		}
	}
	// @ 不存在的成员
	if _, text, isErr := chatCall(t, srv, map[string]any{"workflow_id": "wf-ga", "action": "send", "conversation_id": gid, "text": "x", "mentions": []string{"wf-zzz"}}); !isErr || !strings.Contains(text, "不是这个群的成员") {
		t.Fatalf("@ 外人应报错: %s", text)
	}
	// 没有 @ 的消息只留在群里
	out, _, _ := chatCall(t, srv, map[string]any{"workflow_id": "wf-ga", "action": "send", "conversation_id": gid, "text": "自言自语"})
	if out["delivered_to"].(float64) != 0 || !strings.Contains(out["note"].(string), "没有 @") {
		t.Fatalf("无 @: %v", out)
	}
	// 幂等
	args := map[string]any{"workflow_id": "wf-ga", "action": "send", "conversation_id": gid, "text": "只一次", "idem_key": "k-1", "mentions": []string{"wf-gb"}}
	first, _, _ := chatCall(t, srv, args)
	again, _, _ := chatCall(t, srv, args)
	if again["duplicate"] != true || again["message_id"] != first["message_id"] {
		t.Fatalf("幂等: %v %v", first, again)
	}
	// 群里不能 end，也不能用 before/q 翻历史
	if _, text, isErr := chatCall(t, srv, map[string]any{"workflow_id": "wf-ga", "action": "end", "conversation_id": gid}); !isErr || !strings.Contains(text, "1:1") {
		t.Fatalf("群里 end 应报错: %s", text)
	}
	if _, text, isErr := chatCall(t, srv, map[string]any{"workflow_id": "wf-ga", "action": "read", "conversation_id": gid, "q": "x"}); !isErr || !strings.Contains(text, "since_seq") {
		t.Fatalf("群里 q 应指引 since_seq: %s", text)
	}
	// 别人的 1:1 编号
	if _, text, isErr := chatCall(t, srv, map[string]any{"workflow_id": "wf-ga", "action": "read", "conversation_id": "wf:wf-gb"}); !isErr || !strings.Contains(text, "conversation_id 无效") {
		t.Fatalf("别人的 1:1 应被拒绝: %s", text)
	}
	// 成员列表：A 看得到自己与 B 与用户
	out, _, _ = chatCall(t, srv, map[string]any{"workflow_id": "wf-ga", "action": "members", "conversation_id": gid})
	if len(out["members"].([]any)) != 3 {
		t.Fatalf("成员应是 你 + A + B: %v", out)
	}
	// 限速
	var last string
	for i := 0; i < 12; i++ {
		_, last2, isErr := chatCall(t, srv, map[string]any{"workflow_id": "wf-ga", "action": "send", "conversation_id": gid, "text": "刷屏"})
		if isErr {
			last = last2
			break
		}
	}
	if !strings.Contains(last, "rate_limited") {
		t.Fatalf("应触发限速: %q", last)
	}
}

// 群聊 @ 提醒在回执里的提示单独成句，不当作「用户追加的指令」。
func TestChatGroup_ReceiptHintIsDistinct(t *testing.T) {
	srv, gid, _ := groupFixture(t)
	if _, _, isErr := chatCall(t, srv, map[string]any{"workflow_id": "wf-ga", "action": "send", "conversation_id": gid, "text": "看一下", "mentions": []string{"wf-gb"}}); isErr {
		t.Fatal("send 失败")
	}
	sum, err := srv.store.InboxSummary(context.Background(), "wf-gb", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	hint := formatInboxHint("wf-gb", sum, time.Now())
	if !strings.Contains(hint, "群聊 @ 提醒") || strings.Contains(hint, "追加指令") {
		t.Fatalf("提示行: %s", hint)
	}
}
