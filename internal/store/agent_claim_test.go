package store

import (
	"context"
	"testing"
	"time"

	"github.com/teacat99/RelayMesh/v2/internal/agentstate"
	"github.com/teacat99/RelayMesh/v2/internal/model"
)

// 对话认领（#182）：Hub 发起的运行清楚自己在跑哪个对话、属于哪个工作流；
// 而 hook 汇报带的工作流来自项目目录里的 .cursor/relaymesh.json，常常是另一个（同一个仓库的多个工作树共用一份配置）。
// 认领之后，这个对话在状态核心里的一切（会话、旧链路的对话编号）都归发起运行的工作流。

const (
	claimWF    = "wf-web"    // 发起运行的工作流
	claimOther = "wf-hooked" // hook 配置里写的工作流
	claimConv  = "conv-new"  // 新对话
)

// hookUnder 造一条 hook 汇报：工作流取 wf（模拟 .cursor/relaymesh.json 里写的那个）。
func hookUnder(wf string, ms int, conv, name string) AgentIngestInput {
	in := hookIn(ms, conv, name)
	in.WorkflowID = wf
	return in
}

func sessionsOf(t *testing.T, st *Store, wf string) []AgentSessionView {
	t.Helper()
	list, err := st.ListAgentSessions(context.Background(), AgentSessionFilter{WorkflowID: wf}, at(60_000))
	if err != nil {
		t.Fatal(err)
	}
	return list
}

func TestClaimConversationRehomesAWronglyBoundSession(t *testing.T) {
	st := newMemStore(t)
	ctx := context.Background()

	// 现场：新对话一开始，hook 就按目录里的配置把它报到了 wf-hooked 名下。
	mustIngest(t, st, hookUnder(claimOther, 0, claimConv, agentstate.EvSessionStart))
	tool := hookUnder(claimOther, 100, claimConv, agentstate.EvToolStart)
	tool.Event.Tool = "Shell"
	mustIngest(t, st, tool)
	if len(sessionsOf(t, st, claimOther)) != 1 || len(sessionsOf(t, st, claimWF)) != 0 {
		t.Fatal("setup: the session must start under the hook's workflow")
	}

	from, err := st.ClaimConversation(ctx, claimWF, claimConv, at(10_000).Add(time.Hour), at(10_000))
	if err != nil {
		t.Fatal(err)
	}
	if len(from) != 1 || from[0] != claimOther {
		t.Fatalf("moved from = %v, want [%s]", from, claimOther)
	}
	if got := sessionsOf(t, st, claimWF); len(got) != 1 || got[0].ConversationID != claimConv || got[0].Sub != "tool:Shell" {
		t.Fatalf("session must move to the claiming workflow with its state intact: %+v", got)
	}
	if got := sessionsOf(t, st, claimOther); len(got) != 0 {
		t.Fatalf("the hook's workflow must not keep a phantom session: %+v", got)
	}
	var row model.AgentSession
	if err := st.db.Where("conversation_id = ?", claimConv).Take(&row).Error; err != nil || row.WorkflowID != claimWF {
		t.Fatalf("persisted row = %+v err=%v", row, err)
	}

	// 再认领一次（每次轮询都会续期）：没有东西可搬，也不动库。
	if from, err := st.ClaimConversation(ctx, claimWF, claimConv, at(20_000).Add(time.Hour), at(20_000)); err != nil || len(from) != 0 {
		t.Fatalf("renewal must be a no-op: %v %v", from, err)
	}
}

func TestClaimedConversationOverridesTheHooksWorkflow(t *testing.T) {
	st := newMemStore(t)
	ctx := context.Background()
	if _, err := st.ClaimConversation(ctx, claimWF, claimConv, at(0).Add(time.Hour), at(0)); err != nil {
		t.Fatal(err)
	}

	// 认领在先、hook 在后：会话一开始就建在认领的工作流名下。
	res := mustIngest(t, st, hookUnder(claimOther, 100, claimConv, agentstate.EvSessionStart))
	if !res.NewSession || res.SessionID == 0 {
		t.Fatalf("first hook report must create the session: %+v", res)
	}
	if got := sessionsOf(t, st, claimWF); len(got) != 1 {
		t.Fatalf("session must belong to the claiming workflow: %+v", got)
	}
	if got := sessionsOf(t, st, claimOther); len(got) != 0 {
		t.Fatalf("the hook's workflow must stay clean: %+v", got)
	}

	// 别的对话不受影响：照旧按 hook 带的工作流记。
	mustIngest(t, st, hookUnder(claimOther, 200, "conv-other", agentstate.EvSessionStart))
	if got := sessionsOf(t, st, claimOther); len(got) != 1 || got[0].ConversationID != "conv-other" {
		t.Fatalf("an unclaimed conversation must follow its hook: %+v", got)
	}
}

// 现场的真实顺序：hook 先把会话建在别的工作流名下，运行认领它，hook 配置随后改绑到认领的工作流；
// 运行权威（45 秒一续）让 hook 事件只留记录——以前会话就此卡在原来的工作流里，再也搬不回来。
func TestHookEventsUnderRunAuthorityStillRehomeAClaimedSession(t *testing.T) {
	st := newMemStore(t)
	mustIngest(t, st, hookUnder(claimOther, 0, claimConv, agentstate.EvSessionStart))
	mustIngest(t, st, hookUnder(claimOther, 100, claimConv, agentstate.EvPrompt))

	// 只登记认领，不搬（模拟「认领在前，搬的那一步没跑成」）：状态核心必须在事件到来时自己纠正。
	st.agent.mu.Lock()
	st.agent.init()
	st.agent.owners[claimConv] = agentOwner{wf: claimWF, until: at(0).Add(time.Hour)}
	st.agent.mu.Unlock()
	st.SetAgentRunAuthority(claimWF, claimConv, at(0).Add(time.Hour))

	res := mustIngest(t, st, hookUnder(claimWF, 5_000, claimConv, agentstate.EvThought))
	if res.Change.Note != agentstate.NoteRunAuthority {
		t.Fatalf("setup: the event must be swallowed by the run authority: %+v", res.Change)
	}
	if got := sessionsOf(t, st, claimWF); len(got) != 1 {
		t.Fatalf("the session must be re-homed even when the event is record-only: %+v", got)
	}
	if got := sessionsOf(t, st, claimOther); len(got) != 0 {
		t.Fatalf("no phantom under the old workflow: %+v", got)
	}
}

func TestClaimedRunEventFindsTheSessionUnderAnotherWorkflow(t *testing.T) {
	st := newMemStore(t)
	mustIngest(t, st, hookUnder(claimOther, 0, claimConv, agentstate.EvSessionStart))

	// 没有认领：别的工作流名下的同名对话，运行事件找不到（既有语义不变）。
	run := runIn(1_000, claimConv, agentstate.EvPrompt, "", "")
	run.WorkflowID = claimWF
	if r := mustIngest(t, st, run); r.IdentityID != 0 || r.SessionID != 0 {
		t.Fatalf("an unclaimed foreign conversation must not be touched: %+v", r)
	}

	// 认领之后（只登记认领，不搬）：运行事件认得出这个对话，并把会话归过来。
	st.agent.mu.Lock()
	st.agent.owners[claimConv] = agentOwner{wf: claimWF, until: at(0).Add(time.Hour)}
	st.agent.mu.Unlock()
	run2 := runIn(2_000, claimConv, agentstate.EvToolStart, "Shell", "")
	run2.WorkflowID = claimWF
	r := mustIngest(t, st, run2)
	if r.IdentityID == 0 || r.SessionID == 0 || r.Snapshot.Sub != "tool:Shell" || r.Snapshot.Source != agentstate.SourceRun {
		t.Fatalf("run event must reach the claimed conversation: %+v", r)
	}
	if got := sessionsOf(t, st, claimWF); len(got) != 1 || got[0].StateSource != "run" {
		t.Fatalf("session must be driven by the run under the claiming workflow: %+v", got)
	}
}

func TestConversationClaimExpires(t *testing.T) {
	st := newMemStore(t)
	ctx := context.Background()
	if _, err := st.ClaimConversation(ctx, claimWF, claimConv, at(1_000), at(0)); err != nil {
		t.Fatal(err)
	}
	if got := st.ConversationOwner(claimConv, at(500)); got != claimWF {
		t.Fatalf("owner before expiry = %q", got)
	}
	if got := st.ConversationOwner(claimConv, at(1_000)); got != "" {
		t.Fatalf("owner at expiry = %q, want none", got)
	}
	// 过期之后 hook 汇报恢复按自己带的工作流记。
	mustIngest(t, st, hookUnder(claimOther, 5_000, claimConv, agentstate.EvSessionStart))
	if got := sessionsOf(t, st, claimOther); len(got) != 1 {
		t.Fatalf("after expiry the hook's workflow wins again: %+v", got)
	}
}

// 运行收尾时把认领缩短到「收尾期」，与运行权威同时失效；换了认领者则视为新的认领（会话跟着搬）。
func TestConversationClaimRenewalShortensAndAnotherClaimantTakesOver(t *testing.T) {
	st := newMemStore(t)
	ctx := context.Background()
	if _, err := st.ClaimConversation(ctx, claimWF, claimConv, at(60_000), at(0)); err != nil {
		t.Fatal(err)
	}
	if from, err := st.ClaimConversation(ctx, claimWF, claimConv, at(2_000), at(1_000)); err != nil || len(from) != 0 {
		t.Fatalf("renewal: %v %v", from, err)
	}
	if got := st.ConversationOwner(claimConv, at(1_500)); got != claimWF {
		t.Fatalf("owner within the shortened claim = %q", got)
	}
	if got := st.ConversationOwner(claimConv, at(2_500)); got != "" {
		t.Fatalf("the shortened claim must have ended: %q", got)
	}

	// 另一个工作流的运行认领同一个对话：新的认领者说了算，已有会话跟着搬过去。
	mustIngest(t, st, hookUnder(claimWF, 3_000, claimConv, agentstate.EvSessionStart))
	if got := sessionsOf(t, st, claimWF); len(got) != 1 {
		t.Fatalf("setup: %+v", got)
	}
	from, err := st.ClaimConversation(ctx, claimOther, claimConv, at(60_000), at(4_000))
	if err != nil || len(from) != 1 || from[0] != claimWF {
		t.Fatalf("takeover: %v %v", from, err)
	}
	if got := sessionsOf(t, st, claimOther); len(got) != 1 {
		t.Fatalf("session must follow the new claimant: %+v", got)
	}
	if got := st.ConversationOwner(claimConv, at(5_000)); got != claimOther {
		t.Fatalf("owner = %q", got)
	}
}

func TestClaimConversationIgnoresBlankAndInvalidInput(t *testing.T) {
	st := newMemStore(t)
	ctx := context.Background()
	for _, c := range []struct{ wf, conv string }{{"", claimConv}, {claimWF, ""}, {claimWF, "  "}, {claimWF, "bad conv/.."}} {
		from, err := st.ClaimConversation(ctx, c.wf, c.conv, at(60_000), at(0))
		if err != nil || len(from) != 0 {
			t.Errorf("claim(%q,%q) = %v, %v", c.wf, c.conv, from, err)
		}
	}
	st.agent.mu.Lock()
	n := len(st.agent.owners)
	st.agent.mu.Unlock()
	if n != 0 {
		t.Fatalf("blank or invalid claims must not be stored, got %d", n)
	}
}

func TestRecordAgentEventFollowsTheClaim(t *testing.T) {
	st := newMemStore(t)
	ctx := context.Background()
	if _, err := st.ClaimConversation(ctx, claimWF, claimConv, at(0).Add(time.Hour), at(0)); err != nil {
		t.Fatal(err)
	}
	st.RecordAgentEvent(ctx, claimOther, AgentEvent{Kind: AgentEventTool, ConversationID: claimConv}, at(1_000))

	if l := st.AgentLinkOf(ctx, claimOther); l.ConversationID != "" || !l.LastToolAt.IsZero() {
		t.Fatalf("the hook's workflow must not take the claimed conversation or its liveness: %+v", l)
	}
	if l := st.AgentLinkOf(ctx, claimWF); l.ConversationID != claimConv || l.LastToolAt.IsZero() {
		t.Fatalf("the claiming workflow must own them: %+v", l)
	}
	// 没有对话编号的汇报（旧版 hook）没法认领，照旧记在自己的工作流上。
	st.RecordAgentEvent(ctx, claimOther, AgentEvent{Kind: AgentEventTool}, at(2_000))
	if l := st.AgentLinkOf(ctx, claimOther); l.LastToolAt.IsZero() {
		t.Fatalf("an event without a conversation stays with its own workflow: %+v", l)
	}
}

// 认领之前 hook 可能已经把新对话报到了别的工作流名下，那边的「续聊目标」会被换成这个对话——
// 之后那个工作流的预设运行就会去续聊别人的对话。认领要把这份污染清掉（只清编号，其余不动）。
func TestClaimConversationDropsTheConversationFromOtherWorkflowLinks(t *testing.T) {
	st := newMemStore(t)
	ctx := context.Background()
	st.RecordAgentEvent(ctx, claimOther, AgentEvent{Kind: AgentEventTool, ConversationID: claimConv}, at(0))
	st.RecordAgentEvent(ctx, "wf-keep", AgentEvent{Kind: AgentEventTool, ConversationID: "conv-keep"}, at(0))
	st.RecordAgentEvent(ctx, claimWF, AgentEvent{ConversationID: claimConv}, at(0))
	if l := st.AgentLinkOf(ctx, claimOther); l.ConversationID != claimConv {
		t.Fatalf("setup: the hook's workflow must hold the conversation first: %+v", l)
	}

	if _, err := st.ClaimConversation(ctx, claimWF, claimConv, at(0).Add(time.Hour), at(1_000)); err != nil {
		t.Fatal(err)
	}
	if l := st.AgentLinkOf(ctx, claimOther); l.ConversationID != "" || l.LastToolAt.IsZero() {
		t.Fatalf("the conversation must leave the other workflow's link, its activity stays: %+v", l)
	}
	if l := st.AgentLinkOf(ctx, "wf-keep"); l.ConversationID != "conv-keep" {
		t.Fatalf("an unrelated link must stay: %+v", l)
	}
	if l := st.AgentLinkOf(ctx, claimWF); l.ConversationID != claimConv {
		t.Fatalf("the claiming workflow keeps its own link: %+v", l)
	}

	// 内存清空（相当于 Hub 重启）之后也不会从库里读回来。
	st.activity.mu.Lock()
	st.activity.link = nil
	st.activity.mu.Unlock()
	if l := st.AgentLinkOf(ctx, claimOther); l.ConversationID != "" {
		t.Fatalf("the persisted link must be gone too: %+v", l)
	}
	if l := st.AgentLinkOf(ctx, "wf-keep"); l.ConversationID != "conv-keep" {
		t.Fatalf("an unrelated persisted link must stay: %+v", l)
	}
	if l := st.AgentLinkOf(ctx, claimWF); l.ConversationID != claimConv {
		t.Fatalf("the claiming workflow's persisted link must stay: %+v", l)
	}
}

func TestWorkflowBindingComesFromTheSessionRows(t *testing.T) {
	st := newMemStore(t)
	ctx := context.Background()
	if h, d := st.WorkflowBinding(ctx, "wf-none"); h != "" || d != "" {
		t.Fatalf("no sessions: %q %q", h, d)
	}
	// 网页新建的占位行没有目录；智能体自己的汇报才带主机与目录。
	if err := st.db.Create(&model.FeedbackSession{ID: "sess-web-1", WorkflowID: "wf-x", HostName: "hub-box", Summary: "s"}).Error; err != nil {
		t.Fatal(err)
	}
	if err := st.db.Create(&model.FeedbackSession{ID: "sess-2", WorkflowID: "wf-x", HostName: "devhost", ProjectDirectory: "/data/wt/slot-1/", Summary: "s"}).Error; err != nil {
		t.Fatal(err)
	}
	h, d := st.WorkflowBinding(ctx, "wf-x")
	if h != "devhost" || d != "/data/wt/slot-1" {
		t.Fatalf("binding = %q %q", h, d)
	}
}
