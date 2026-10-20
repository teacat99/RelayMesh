package store

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/teacat99/RelayMesh/v2/internal/model"
)

func TestNormalizeAgentEvent(t *testing.T) {
	cases := []struct {
		name string
		in   AgentEvent
		want AgentEvent
		ok   bool
	}{
		{"工具事件清掉状态", AgentEvent{Kind: " TOOL ", Status: "error", ConversationID: "abc-123_X"}, AgentEvent{Kind: "tool", ConversationID: "abc-123_X"}, true},
		{"正常收尾", AgentEvent{Kind: "stop", Status: "Completed"}, AgentEvent{Kind: "stop", Status: "completed"}, true},
		{"被中止", AgentEvent{Kind: "stop", Status: "aborted"}, AgentEvent{Kind: "stop", Status: "aborted"}, true},
		{"出错", AgentEvent{Kind: "stop", Status: "error"}, AgentEvent{Kind: "stop", Status: "error"}, true},
		{"没见过的状态记为 other", AgentEvent{Kind: "stop", Status: "cancelled"}, AgentEvent{Kind: "stop", Status: "other"}, true},
		{"没有状态也记为 other", AgentEvent{Kind: "stop"}, AgentEvent{Kind: "stop", Status: "other"}, true},
		{"对话编号格式不对就丢掉", AgentEvent{Kind: "tool", ConversationID: "bad id/../x"}, AgentEvent{Kind: "tool"}, true},
		{"对话编号太长就丢掉", AgentEvent{Kind: "tool", ConversationID: strings.Repeat("a", 65)}, AgentEvent{Kind: "tool"}, true},
		{"旧版 hook 只带编号", AgentEvent{ConversationID: "abc"}, AgentEvent{ConversationID: "abc"}, true},
		{"未知种类且没有编号：没有可记的", AgentEvent{Kind: "weird", Status: "error"}, AgentEvent{}, false},
		{"全空", AgentEvent{}, AgentEvent{}, false},
	}
	for _, c := range cases {
		got, ok := NormalizeAgentEvent(c.in)
		if ok != c.ok || got != c.want {
			t.Fatalf("%s：得到 %+v ok=%v，应为 %+v ok=%v", c.name, got, ok, c.want, c.ok)
		}
	}
}

func TestRecordAgentEventTracksToolStopAndConversation(t *testing.T) {
	st := newMemStore(t)
	ctx := context.Background()
	t0 := time.Now().Truncate(time.Second)

	if l := st.AgentLinkOf(ctx, "wf"); l != (AgentLink{}) {
		t.Fatalf("没汇报过应为零值: %+v", l)
	}

	st.RecordAgentEvent(ctx, "wf", AgentEvent{Kind: "tool", ConversationID: "conv-1"}, t0)
	l := st.AgentLinkOf(ctx, "wf")
	if !l.LastToolAt.Equal(t0) || l.ConversationID != "conv-1" || !l.StopAt.IsZero() || l.StopStatus != "" {
		t.Fatalf("工具事件: %+v", l)
	}

	st.RecordAgentEvent(ctx, "wf", AgentEvent{Kind: "stop", Status: "error", ConversationID: "conv-1"}, t0.Add(time.Minute))
	l = st.AgentLinkOf(ctx, "wf")
	if !l.LastToolAt.Equal(t0) || !l.StopAt.Equal(t0.Add(time.Minute)) || l.StopStatus != "error" {
		t.Fatalf("停止事件不应动工具时间: %+v", l)
	}

	// 之后又有工具调用：工具时间更新，上一轮的结束记录保留（守护靠比较两者的先后判断是不是又干起活来了）。
	st.RecordAgentEvent(ctx, "wf", AgentEvent{Kind: "tool"}, t0.Add(2*time.Minute))
	l = st.AgentLinkOf(ctx, "wf")
	if !l.LastToolAt.Equal(t0.Add(2*time.Minute)) || !l.StopAt.Equal(t0.Add(time.Minute)) || l.StopStatus != "error" || l.ConversationID != "conv-1" {
		t.Fatalf("工具事件后的状态: %+v", l)
	}

	// 对话编号换了（开了新对话）：以最新的为准。
	st.RecordAgentEvent(ctx, "wf", AgentEvent{Kind: "tool", ConversationID: "conv-2"}, t0.Add(3*time.Minute))
	if l = st.AgentLinkOf(ctx, "wf"); l.ConversationID != "conv-2" {
		t.Fatalf("对话编号应更新: %+v", l)
	}

	// 别的工作流互不影响。
	if other := st.AgentLinkOf(ctx, "wf-other"); other != (AgentLink{}) {
		t.Fatalf("别的工作流不应有记录: %+v", other)
	}
}

func TestRecordAgentEventIgnoresEmptyWorkflowAndJunk(t *testing.T) {
	st := newMemStore(t)
	ctx := context.Background()
	now := time.Now()
	st.RecordAgentEvent(ctx, "", AgentEvent{Kind: "tool", ConversationID: "c"}, now)
	st.RecordAgentEvent(ctx, "   ", AgentEvent{Kind: "tool"}, now)
	st.RecordAgentEvent(ctx, "wf", AgentEvent{Kind: "weird"}, now)
	st.RecordAgentEvent(ctx, "wf", AgentEvent{}, now)
	if l := st.AgentLinkOf(ctx, "wf"); l != (AgentLink{}) {
		t.Fatalf("垃圾事件不应留下记录: %+v", l)
	}
	if l := st.AgentLinkOf(ctx, ""); l != (AgentLink{}) {
		t.Fatalf("空工作流应返回零值: %+v", l)
	}
	var n int64
	st.db.Model(&model.SystemSetting{}).Where("key LIKE ?", "agent_link:%").Count(&n)
	if n != 0 {
		t.Fatalf("垃圾事件不应写库: %d", n)
	}
}

// 对话编号落库：模拟重启（内存清空）后仍能读到，供会话意外结束后恢复同一个对话。
func TestAgentLinkConversationSurvivesRestart(t *testing.T) {
	st := newMemStore(t)
	ctx := context.Background()
	now := time.Now()
	st.RecordAgentEvent(ctx, "wf-long-"+strings.Repeat("x", 100), AgentEvent{Kind: "tool", ConversationID: "conv-9"}, now)

	st.activity.mu.Lock()
	st.activity.link = nil // 重启
	st.activity.mu.Unlock()

	l := st.AgentLinkOf(ctx, "wf-long-"+strings.Repeat("x", 100))
	if l.ConversationID != "conv-9" || !l.LastToolAt.IsZero() {
		t.Fatalf("重启后应只剩落库的对话编号（事件时间不保留）: %+v", l)
	}
	// 键按哈希生成，不能超过 system_settings 的键长上限。
	if k := agentLinkKey("wf-long-" + strings.Repeat("x", 100)); len(k) > 64 {
		t.Fatalf("键太长: %d", len(k))
	}
	// 别的工作流读不到它。
	if other := st.AgentLinkOf(ctx, "wf-other"); other.ConversationID != "" {
		t.Fatalf("别的工作流不应读到: %+v", other)
	}
}

// 对话编号没变就不再写库：hook 每十几秒汇报一次，不能每次都写。
func TestRecordAgentEventWritesConversationOnlyWhenChanged(t *testing.T) {
	st := newMemStore(t)
	ctx := context.Background()
	now := time.Now()
	st.RecordAgentEvent(ctx, "wf", AgentEvent{Kind: "tool", ConversationID: "conv-1"}, now)
	key := agentLinkKey("wf")
	if err := st.db.Where("key = ?", key).Delete(&model.SystemSetting{}).Error; err != nil {
		t.Fatal(err)
	}
	st.RecordAgentEvent(ctx, "wf", AgentEvent{Kind: "tool", ConversationID: "conv-1"}, now.Add(time.Second))
	var n int64
	st.db.Model(&model.SystemSetting{}).Where("key = ?", key).Count(&n)
	if n != 0 {
		t.Fatal("对话编号没变不应再写库")
	}
	st.RecordAgentEvent(ctx, "wf", AgentEvent{Kind: "tool", ConversationID: "conv-2"}, now.Add(2*time.Second))
	st.db.Model(&model.SystemSetting{}).Where("key = ?", key).Count(&n)
	if n != 1 {
		t.Fatal("对话编号变了应写库")
	}
}

func TestRecordAgentEventEvictsStaleEntries(t *testing.T) {
	st := newMemStore(t)
	ctx := context.Background()
	old := time.Now().Add(-2 * InboxExpireAfter)
	for i := 0; i < 2003; i++ {
		st.RecordAgentEvent(ctx, fmt.Sprintf("wf-%d", i), AgentEvent{Kind: "tool"}, old)
	}
	st.RecordAgentEvent(ctx, "wf-fresh", AgentEvent{Kind: "tool"}, time.Now())
	st.activity.mu.Lock()
	size := len(st.activity.link)
	st.activity.mu.Unlock()
	if size > 10 {
		t.Fatalf("超过 2000 条后应清掉陈旧记录，剩 %d 条", size)
	}
	if l := st.AgentLinkOf(ctx, "wf-fresh"); l.LastToolAt.IsZero() {
		t.Fatal("新记录应保留")
	}
}
