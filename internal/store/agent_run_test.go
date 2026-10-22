package store

import (
	"context"
	"testing"
	"time"

	"github.com/teacat99/RelayMesh/v2/internal/agentstate"
	"github.com/teacat99/RelayMesh/v2/internal/model"
)

// runIn 造一条「Hub 读到运行输出」的汇报：只知道工作流和对话 ID，不带凭据、主机、目录。
func runIn(ms int, conv, name, tool, status string) AgentIngestInput {
	t := agentBase.Add(time.Duration(ms) * time.Millisecond)
	return AgentIngestInput{
		WorkflowID: "wf-a", ConversationID: conv, Conversation: true,
		Event: agentstate.Event{Source: agentstate.SourceRun, Name: name, Tool: tool, Status: status, Received: t},
	}
}

func hookIn(ms int, conv, name string) AgentIngestInput {
	in := agentIn(agentEv(ms, name))
	in.ConversationID = conv
	return in
}

func at(ms int) time.Time { return agentBase.Add(time.Duration(ms) * time.Millisecond) }

// seedConversation 让这个对话有一个 hook 汇报过的会话（idle）。
func seedConversation(t *testing.T, st *Store, conv string) AgentIngestResult {
	t.Helper()
	mustIngest(t, st, hookIn(0, conv, agentstate.EvSessionStart))
	stop := hookIn(10, conv, agentstate.EvStop)
	stop.Event.Status = agentstate.StopCompleted
	return mustIngest(t, st, stop)
}

func TestConversationIngestTargetsTheNamedConversation(t *testing.T) {
	st := newMemStore(t)
	ctx := context.Background()

	// 两个对话：conv-1 最近动过（Latest 会选它），conv-2 才是运行要续的。
	seedConversation(t, st, "conv-2")
	mustIngest(t, st, hookIn(5_000, "conv-1", agentstate.EvPrompt))

	res := mustIngest(t, st, runIn(10_000, "conv-2", agentstate.EvPrompt, "", ""))
	if res.IdentityID == 0 || res.SessionID == 0 || res.NewSession {
		t.Fatalf("run prompt must land on the existing session: %+v", res)
	}
	if res.Snapshot.State != agentstate.StateWorking || res.Snapshot.Source != agentstate.SourceRun {
		t.Fatalf("snapshot = %+v", res.Snapshot)
	}
	tool := runIn(11_000, "conv-2", agentstate.EvToolStart, "Shell", "")
	if r := mustIngest(t, st, tool); r.Snapshot.Sub != "tool:Shell" || !r.Change.StateChanged {
		t.Fatalf("tool_start = %+v", r)
	}

	list, err := st.ListAgentSessions(ctx, AgentSessionFilter{WorkflowID: "wf-a"}, at(20_000))
	if err != nil || len(list) != 2 {
		t.Fatalf("list = %+v, %v", list, err)
	}
	byConv := map[string]AgentSessionView{}
	for _, v := range list {
		byConv[v.ConversationID] = v
	}
	if v := byConv["conv-2"]; v.State != "working" || v.Sub != "tool:Shell" || v.StateSource != "run" {
		t.Fatalf("conv-2 view = %+v", v)
	}
	if v := byConv["conv-1"]; v.State != "working" || v.Sub != "thinking" || v.StateSource != "hook" {
		t.Fatalf("conv-1 must be untouched by the run: %+v", v)
	}
	if n := countRows(t, st, &model.AgentIdentity{}); n != 1 {
		t.Fatalf("a run must never create an identity: %d", n)
	}
}

func TestConversationIngestFindsEndedSessionAndRevivesIt(t *testing.T) {
	st := newMemStore(t)
	seedConversation(t, st, "conv-1")
	end := hookIn(100, "conv-1", agentstate.EvSessionEnd)
	end.Event.Status = agentstate.EndClosed
	if r := mustIngest(t, st, end); r.Snapshot.State != agentstate.StateEnded {
		t.Fatalf("setup: %+v", r.Snapshot)
	}

	// Latest 只找没结束的会话，找不到这个；Conversation 能找到并把它拉起来。
	latest := runIn(60_000, "conv-1", agentstate.EvPrompt, "", "")
	latest.Conversation, latest.Latest = false, true
	if r := mustIngest(t, st, latest); r.IdentityID != 0 {
		t.Fatalf("Latest must not see an ended session: %+v", r)
	}
	res := mustIngest(t, st, runIn(60_000, "conv-1", agentstate.EvPrompt, "", ""))
	if res.Snapshot.State != agentstate.StateWorking || res.Change.Note != agentstate.NoteRevived || !res.Snapshot.EndedAt.IsZero() {
		t.Fatalf("revive = %+v / %+v", res.Snapshot, res.Change)
	}
}

func TestConversationIngestIgnoresUnknownOrInvalidConversation(t *testing.T) {
	st := newMemStore(t)
	seedConversation(t, st, "conv-1")
	before := countRows(t, st, &model.AgentSession{})

	for name, in := range map[string]AgentIngestInput{
		"unknown conversation": runIn(100, "conv-404", agentstate.EvPrompt, "", ""),
		"empty conversation":   runIn(100, "", agentstate.EvPrompt, "", ""),
		"invalid conversation": runIn(100, "bad conv/..", agentstate.EvPrompt, "", ""),
		"other workflow": func() AgentIngestInput {
			in := runIn(100, "conv-1", agentstate.EvPrompt, "", "")
			in.WorkflowID = "wf-other"
			return in
		}(),
	} {
		if r := mustIngest(t, st, in); r.IdentityID != 0 || r.SessionID != 0 {
			t.Errorf("%s: must record nothing, got %+v", name, r)
		}
	}
	if after := countRows(t, st, &model.AgentSession{}); after != before {
		t.Fatalf("sessions %d -> %d", before, after)
	}
}

func TestStateSourceSurvivesRestart(t *testing.T) {
	st := newMemStore(t)
	ctx := context.Background()
	seedConversation(t, st, "conv-1")
	mustIngest(t, st, runIn(1_000, "conv-1", agentstate.EvPrompt, "", ""))
	mustIngest(t, st, runIn(2_000, "conv-1", agentstate.EvToolStart, "Read", ""))

	st.agent = agentStateMem{} // 模拟 Hub 重启

	res := mustIngest(t, st, runIn(3_000, "conv-1", agentstate.EvToolEnd, "Read", ""))
	if res.NewSession || res.Snapshot.Source != agentstate.SourceRun {
		t.Fatalf("after restart: %+v", res)
	}
	list, err := st.ListAgentSessions(ctx, AgentSessionFilter{}, at(4_000))
	if err != nil || len(list) != 1 || list[0].StateSource != "run" {
		t.Fatalf("view after restart: %+v %v", list, err)
	}
}

func TestRunAuthorityMakesHookEventsRecordOnly(t *testing.T) {
	st := newMemStore(t)
	seedConversation(t, st, "conv-1")
	mustIngest(t, st, runIn(1_000, "conv-1", agentstate.EvPrompt, "", ""))
	st.SetAgentRunAuthority("wf-a", "conv-1", at(60_000))

	rowsBefore := countRows(t, st, &model.AgentEventRecord{})
	// 运行期间 hook 报了 sessionEnd 和思考心跳：不能把状态改掉。
	end := hookIn(5_000, "conv-1", agentstate.EvSessionEnd)
	end.Event.Status = agentstate.EndClosed
	res := mustIngest(t, st, end)
	if res.Change.Note != agentstate.NoteRunAuthority || res.Change.StateChanged || res.Snapshot.State != agentstate.StateWorking {
		t.Fatalf("hook sessionEnd under authority: %+v / %+v", res.Change, res.Snapshot)
	}
	if res.IdentityID == 0 || res.SessionID == 0 {
		t.Fatalf("result must still name the session: %+v", res)
	}
	thought := mustIngest(t, st, hookIn(5_100, "conv-1", agentstate.EvThought))
	if thought.Change.Note != agentstate.NoteRunAuthority || thought.Change.Record {
		t.Fatalf("thought heartbeats under authority are not recorded: %+v", thought.Change)
	}
	// 只多了 sessionEnd 这一行（带 run_authority 备注），思考心跳不留。
	if rows := countRows(t, st, &model.AgentEventRecord{}); rows != rowsBefore+1 {
		t.Fatalf("event rows %d -> %d, want +1", rowsBefore, rows)
	}
	var last model.AgentEventRecord
	if err := st.db.Order("id DESC").Take(&last).Error; err != nil {
		t.Fatal(err)
	}
	if last.Event != agentstate.EvSessionEnd || last.Source != "hook" || last.Extra == "" {
		t.Fatalf("recorded hook row = %+v", last)
	}

	// 运行输出自己的事件照常推进状态。
	if r := mustIngest(t, st, runIn(6_000, "conv-1", agentstate.EvToolStart, "Shell", "")); r.Snapshot.Sub != "tool:Shell" {
		t.Fatalf("run event under authority: %+v", r.Snapshot)
	}

	// 标记到期（运行结束后的尾巴过去了）：hook 恢复说话。
	late := hookIn(61_000, "conv-1", agentstate.EvPrompt)
	if r := mustIngest(t, st, late); r.Change.Note == agentstate.NoteRunAuthority || r.Snapshot.Source != agentstate.SourceHook {
		t.Fatalf("authority must expire: %+v / %+v", r.Change, r.Snapshot)
	}
}

func TestRunAuthorityOnlyCoversTheNamedConversationAndCanBeRevoked(t *testing.T) {
	st := newMemStore(t)
	seedConversation(t, st, "conv-1")
	seedConversation(t, st, "conv-2")
	st.SetAgentRunAuthority("wf-a", "conv-1", at(60_000))

	// 另一个对话不受影响。
	if r := mustIngest(t, st, hookIn(1_000, "conv-2", agentstate.EvPrompt)); r.Change.Note == agentstate.NoteRunAuthority || r.Snapshot.State != agentstate.StateWorking {
		t.Fatalf("conv-2 must follow hook events: %+v / %+v", r.Change, r.Snapshot)
	}
	// 别的工作流里同名对话也不受影响。
	other := hookIn(1_100, "conv-1", agentstate.EvPrompt)
	other.WorkflowID = "wf-other"
	if r := mustIngest(t, st, other); r.Change.Note == agentstate.NoteRunAuthority {
		t.Fatalf("authority is per workflow: %+v", r.Change)
	}
	// 撤销后立刻恢复。
	st.SetAgentRunAuthority("wf-a", "conv-1", time.Time{})
	if r := mustIngest(t, st, hookIn(2_000, "conv-1", agentstate.EvPrompt)); r.Change.Note == agentstate.NoteRunAuthority || r.Snapshot.State != agentstate.StateWorking {
		t.Fatalf("revoked authority: %+v / %+v", r.Change, r.Snapshot)
	}
}

func TestRunAuthorityDoesNotSwallowAFirstReport(t *testing.T) {
	st := newMemStore(t)
	// 这个对话还没有任何会话行：运行权威只管已经存在的会话，第一次 hook 汇报照常建会话。
	st.SetAgentRunAuthority("wf-a", "conv-new", at(60_000))
	res := mustIngest(t, st, hookIn(100, "conv-new", agentstate.EvSessionStart))
	if !res.NewSession || res.SessionID == 0 || res.Snapshot.State != agentstate.StateIdle {
		t.Fatalf("first report must create the session: %+v", res)
	}
}

func TestSetAgentRunAuthorityIgnoresBlankKeys(t *testing.T) {
	st := newMemStore(t)
	st.SetAgentRunAuthority("", "conv-1", at(1000))
	st.SetAgentRunAuthority("wf-a", "  ", at(1000))
	st.agent.mu.Lock()
	n := len(st.agent.runAuth)
	st.agent.mu.Unlock()
	if n != 0 {
		t.Fatalf("blank keys must not be stored, got %d", n)
	}
}
