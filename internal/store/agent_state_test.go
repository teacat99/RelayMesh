package store

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/teacat99/RelayMesh/v2/internal/agentstate"
	"github.com/teacat99/RelayMesh/v2/internal/model"
)

var agentBase = time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)

func agentEv(ms int, name string) agentstate.Event {
	t := agentBase.Add(time.Duration(ms) * time.Millisecond)
	return agentstate.Event{Source: agentstate.SourceHook, Name: name, At: t, Received: t}
}

func agentIn(ev agentstate.Event) AgentIngestInput {
	return AgentIngestInput{
		WorkflowID: "wf-a", CredentialID: 7, HostName: "devhost", ProjectDirectory: "/home/user/RelayMesh",
		ConversationID: "conv-1", Event: ev,
	}
}

func mustIngest(t *testing.T, st *Store, in AgentIngestInput) AgentIngestResult {
	t.Helper()
	res, err := st.IngestAgentEvent(context.Background(), in)
	if err != nil {
		t.Fatalf("IngestAgentEvent(%s): %v", in.Event.Name, err)
	}
	return res
}

func countRows(t *testing.T, st *Store, m any) int64 {
	t.Helper()
	var n int64
	if err := st.db.Model(m).Count(&n).Error; err != nil {
		t.Fatal(err)
	}
	return n
}

func TestNormalizeProjectDir(t *testing.T) {
	for in, want := range map[string]string{
		"/home/user/RelayMesh/":  "/home/user/RelayMesh",
		"  /a/b  ":               "/a/b",
		`C:\Users\me\app\`:       `C:\Users\me\app`,
		"/":                      "/",
		"":                       "",
		"   ":                    "",
		strings.Repeat("x", 600): strings.Repeat("x", 512),
		"relative/dir///":        "relative/dir",
	} {
		if got := normalizeProjectDir(in); got != want {
			t.Errorf("normalizeProjectDir(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestIngestCreatesIdentitySessionAndEvents(t *testing.T) {
	st := newMemStore(t)
	ctx := context.Background()

	mustIngest(t, st, agentIn(agentEv(0, agentstate.EvSessionStart)))
	mustIngest(t, st, agentIn(agentEv(100, agentstate.EvPrompt)))
	tool := agentEv(200, agentstate.EvToolStart)
	tool.Tool = "Shell"
	res := mustIngest(t, st, agentIn(tool))
	if res.IdentityID == 0 || res.SessionID == 0 || !res.Change.StateChanged || res.Snapshot.Sub != "tool:Shell" {
		t.Fatalf("tool_start result = %+v", res)
	}
	end := agentEv(900, agentstate.EvToolEnd)
	end.Tool, end.Duration = "Shell", 700*time.Millisecond
	mustIngest(t, st, agentIn(end))
	stop := agentEv(1000, agentstate.EvStop)
	stop.Status = agentstate.StopCompleted
	mustIngest(t, st, agentIn(stop))

	if n := countRows(t, st, &model.AgentIdentity{}); n != 1 {
		t.Fatalf("identities = %d, want 1", n)
	}
	if n := countRows(t, st, &model.AgentSession{}); n != 1 {
		t.Fatalf("sessions = %d, want 1", n)
	}
	var evs []model.AgentEventRecord
	if err := st.db.Order("id").Find(&evs).Error; err != nil {
		t.Fatal(err)
	}
	if len(evs) != 5 {
		t.Fatalf("event rows = %d, want 5", len(evs))
	}
	if evs[3].Event != agentstate.EvToolEnd || evs[3].DurationMS != 700 || evs[3].Tool != "Shell" {
		t.Fatalf("tool_end row = %+v", evs[3])
	}
	if !evs[2].At.Equal(agentBase.Add(200 * time.Millisecond)) {
		t.Fatalf("event time = %v", evs[2].At)
	}

	sessions, err := st.ListAgentSessions(ctx, AgentSessionFilter{WorkflowID: "wf-a"}, agentBase.Add(time.Minute))
	if err != nil || len(sessions) != 1 {
		t.Fatalf("ListAgentSessions = %v, %v", sessions, err)
	}
	v := sessions[0]
	if v.State != "idle" || v.BaseState != "idle" || v.ConversationID != "conv-1" || v.IdentityLabel != "RelayMesh" ||
		v.HostName != "devhost" || v.ProjectDir != "/home/user/RelayMesh" || v.LastTool != "Shell" {
		t.Fatalf("session view = %+v", v)
	}
	if v.StartedAt == nil || !v.StartedAt.Equal(agentBase) || v.TurnEndedAt == nil || v.EndedAt != nil {
		t.Fatalf("session times = %+v", v)
	}
	ids, err := st.ListAgentIdentities(ctx)
	if err != nil || len(ids) != 1 || ids[0].Label != "RelayMesh" || ids[0].LastHookAt == nil {
		t.Fatalf("ListAgentIdentities = %+v, %v", ids, err)
	}
}

func TestIngestUsesWorkflowBindingWhenDirectoryMissing(t *testing.T) {
	st := newMemStore(t)
	ctx := context.Background()
	in := agentIn(agentEv(0, agentstate.EvPrompt))
	in.ProjectDirectory, in.HostName = "", ""

	// 没有绑定：认不出身份，什么都不记，也不报错。
	res, err := st.IngestAgentEvent(ctx, in)
	if err != nil || res.IdentityID != 0 || res.SessionID != 0 {
		t.Fatalf("unbound result = %+v err=%v", res, err)
	}
	if countRows(t, st, &model.AgentIdentity{}) != 0 || countRows(t, st, &model.AgentSession{}) != 0 || countRows(t, st, &model.AgentEventRecord{}) != 0 {
		t.Fatal("rows were written for an unidentifiable agent")
	}

	// 工作流上有汇报会话（记录了主机与目录）：绑定缓存过期后能认出来。
	if err := st.db.Create(&model.FeedbackSession{ID: "sess-1", WorkflowID: "wf-a", HostName: "box", ProjectDirectory: "/work/app/", Summary: "s"}).Error; err != nil {
		t.Fatal(err)
	}
	later := agentEv(int((2*time.Minute)/time.Millisecond), agentstate.EvPrompt)
	in.Event = later
	res = mustIngest(t, st, in)
	if res.IdentityID == 0 {
		t.Fatalf("binding not used: %+v", res)
	}
	var id model.AgentIdentity
	if err := st.db.First(&id, res.IdentityID).Error; err != nil {
		t.Fatal(err)
	}
	if id.HostName != "box" || id.ProjectDirectory != "/work/app" || id.CredentialID != 7 {
		t.Fatalf("identity = %+v", id)
	}
}

func TestIngestExplicitDirectoryBeatsBindingAndSeedsIt(t *testing.T) {
	st := newMemStore(t)
	if err := st.db.Create(&model.FeedbackSession{ID: "sess-1", WorkflowID: "wf-a", HostName: "box", ProjectDirectory: "/old/place", Summary: "s"}).Error; err != nil {
		t.Fatal(err)
	}
	res := mustIngest(t, st, agentIn(agentEv(0, agentstate.EvPrompt))) // 带 /home/user/RelayMesh
	var id model.AgentIdentity
	if err := st.db.First(&id, res.IdentityID).Error; err != nil {
		t.Fatal(err)
	}
	if id.ProjectDirectory != "/home/user/RelayMesh" {
		t.Fatalf("explicit directory lost: %+v", id)
	}
	// 之后同一个工作流不带目录的汇报，归到刚断言的目录（不是旧绑定）。
	in := agentIn(agentEv(1000, agentstate.EvThought))
	in.ProjectDirectory, in.HostName = "", ""
	res2 := mustIngest(t, st, in)
	if res2.IdentityID == 0 {
		t.Fatalf("follow-up not identified: %+v", res2)
	}
}

func TestIngestSeparatesIdentitiesAndConversations(t *testing.T) {
	st := newMemStore(t)
	a := agentIn(agentEv(0, agentstate.EvPrompt))
	mustIngest(t, st, a)
	b := agentIn(agentEv(10, agentstate.EvPrompt))
	b.ProjectDirectory = "/home/user/Other"
	b.WorkflowID = "wf-b"
	mustIngest(t, st, b)
	c := agentIn(agentEv(20, agentstate.EvPrompt))
	c.ConversationID = "conv-2"
	mustIngest(t, st, c)
	d := agentIn(agentEv(30, agentstate.EvPrompt))
	d.CredentialID = 8 // 同一目录、另一把 Token：另一个身份
	mustIngest(t, st, d)

	if n := countRows(t, st, &model.AgentIdentity{}); n != 3 {
		t.Fatalf("identities = %d, want 3", n)
	}
	if n := countRows(t, st, &model.AgentSession{}); n != 4 {
		t.Fatalf("sessions = %d, want 4", n)
	}
}

func TestIngestDropsDuplicatesAndThrottlesThoughts(t *testing.T) {
	st := newMemStore(t)
	mustIngest(t, st, agentIn(agentEv(0, agentstate.EvPrompt)))
	mustIngest(t, st, agentIn(agentEv(1000, agentstate.EvThought)))
	mustIngest(t, st, agentIn(agentEv(1020, agentstate.EvThought))) // 重复
	mustIngest(t, st, agentIn(agentEv(2000, agentstate.EvThought))) // 被节流
	mustIngest(t, st, agentIn(agentEv(3000, agentstate.EvThought))) // 被节流
	mustIngest(t, st, agentIn(agentEv(6500, agentstate.EvThought)))
	if n := countRows(t, st, &model.AgentEventRecord{}); n != 3 { // prompt + 两条被记下的 thought
		t.Fatalf("event rows = %d, want 3", n)
	}
}

func TestIngestWritesSessionHeartbeatOnlyEveryThirtySeconds(t *testing.T) {
	st := newMemStore(t)
	mustIngest(t, st, agentIn(agentEv(0, agentstate.EvPrompt)))
	read := func() time.Time {
		var s model.AgentSession
		if err := st.db.First(&s).Error; err != nil {
			t.Fatal(err)
		}
		return s.LastEventAt
	}
	mustIngest(t, st, agentIn(agentEv(6000, agentstate.EvThought))) // 仅心跳：不写库
	if got := read(); !got.Equal(agentBase) {
		t.Fatalf("heartbeat was written early: %v", got)
	}
	mustIngest(t, st, agentIn(agentEv(31000, agentstate.EvThought))) // 距上次写库已过 30 秒：写
	if got := read(); !got.Equal(agentBase.Add(31 * time.Second)) {
		t.Fatalf("heartbeat not written after 30s: %v", got)
	}
}

func TestIngestRestoresSessionFromDatabaseAfterRestart(t *testing.T) {
	st := newMemStore(t)
	ctx := context.Background()
	mustIngest(t, st, agentIn(agentEv(0, agentstate.EvSessionStart)))
	mustIngest(t, st, agentIn(agentEv(100, agentstate.EvPrompt)))
	tool := agentEv(200, agentstate.EvToolStart)
	tool.Tool = "Read"
	mustIngest(t, st, agentIn(tool))

	st.agent = agentStateMem{} // 模拟 Hub 重启：内存清空，库还在

	end := agentEv(300, agentstate.EvToolEnd)
	end.Tool = "Read"
	res := mustIngest(t, st, agentIn(end))
	if res.NewSession {
		t.Fatal("restart created a second session")
	}
	if res.Snapshot.State != agentstate.StateWorking || res.Snapshot.Sub != agentstate.SubThinking ||
		!res.Snapshot.StartedAt.Equal(agentBase) || !res.Snapshot.TurnStartedAt.Equal(agentBase.Add(100*time.Millisecond)) {
		t.Fatalf("snapshot after restart = %+v", res.Snapshot)
	}
	stop := agentEv(400, agentstate.EvStop)
	stop.Status = "completed"
	mustIngest(t, st, agentIn(stop))
	list, err := st.ListAgentSessions(ctx, AgentSessionFilter{}, agentBase.Add(time.Minute))
	if err != nil || len(list) != 1 || list[0].State != "idle" {
		t.Fatalf("after restart: %+v %v", list, err)
	}
	if n := countRows(t, st, &model.AgentIdentity{}); n != 1 {
		t.Fatalf("identities after restart = %d", n)
	}
}

func TestIngestAbortSequenceEndsAsAborted(t *testing.T) {
	st := newMemStore(t)
	mustIngest(t, st, agentIn(agentEv(0, agentstate.EvPrompt)))
	tool := agentEv(100, agentstate.EvToolStart)
	tool.Tool = "Shell"
	mustIngest(t, st, agentIn(tool))
	ab := agentEv(200, agentstate.EvStop)
	ab.Status = "aborted"
	mustIngest(t, st, agentIn(ab))
	er := agentEv(300, agentstate.EvStop)
	er.Status = "error"
	res := mustIngest(t, st, agentIn(er))
	if res.Snapshot.State != agentstate.StateAborted || res.Change.Note != agentstate.NoteFoldedError {
		t.Fatalf("abort sequence result = %+v", res)
	}
	var extras []string
	st.db.Model(&model.AgentEventRecord{}).Where("event = 'stop'").Order("id").Pluck("extra", &extras)
	if len(extras) != 2 || !strings.Contains(extras[1], "folded_error") {
		t.Fatalf("stop extras = %v", extras)
	}
}

func TestIngestStoresNothingSensitive(t *testing.T) {
	st := newMemStore(t)
	ev := agentEv(0, agentstate.EvToolStart)
	ev.Tool = "Shell; someone@example.com"
	in := agentIn(ev)
	in.ClientKind, in.CursorVersion = "IDE", "2026.10.12-d47ca18"
	mustIngest(t, st, in)
	end := agentEv(100, agentstate.EvStop)
	end.Status = "someone@example.com"
	mustIngest(t, st, agentIn(end))

	var evs []model.AgentEventRecord
	st.db.Find(&evs)
	for _, e := range evs {
		blob, _ := json.Marshal(e)
		if strings.Contains(string(blob), "@") || strings.Contains(e.Tool, " ") || strings.Contains(e.Extra, "example") {
			t.Fatalf("event row leaks free text: %s", blob)
		}
	}
	var id model.AgentIdentity
	st.db.First(&id)
	if id.ClientKind != "ide" || id.CursorVersion != "2026.10.12-d47ca18" {
		t.Fatalf("identity client = %q %q", id.ClientKind, id.CursorVersion)
	}
}

func TestIngestKeepsKnownClientKindWhenLaterReportsUnknown(t *testing.T) {
	st := newMemStore(t)
	in := agentIn(agentEv(0, agentstate.EvPrompt))
	in.ClientKind = "cli"
	mustIngest(t, st, in)
	in2 := agentIn(agentEv(1000, agentstate.EvThought))
	in2.ClientKind = "weird-client"
	mustIngest(t, st, in2)
	in3 := agentIn(agentEv(2000, agentstate.EvThought))
	mustIngest(t, st, in3) // 没报：保持
	var id model.AgentIdentity
	st.db.First(&id)
	if id.ClientKind != "cli" {
		t.Fatalf("client kind = %q", id.ClientKind)
	}
	var s model.AgentSession
	st.db.First(&s)
	if s.ClientKind != "cli" {
		t.Fatalf("session client kind = %q", s.ClientKind)
	}
}

func TestIngestIgnoresEmptyInput(t *testing.T) {
	st := newMemStore(t)
	for _, in := range []AgentIngestInput{
		{},
		{WorkflowID: "wf", ProjectDirectory: "/a"},                                         // 没有事件
		{WorkflowID: "  ", ProjectDirectory: "/a", Event: agentEv(0, agentstate.EvPrompt)}, // 没有工作流
	} {
		res, err := st.IngestAgentEvent(context.Background(), in)
		if err != nil || res.IdentityID != 0 {
			t.Fatalf("input %+v: res=%+v err=%v", in, res, err)
		}
	}
	// 事件本身没有任何时间信息：不建会话行。
	in := agentIn(agentstate.Event{Name: agentstate.EvPrompt})
	in.Event.Received = time.Time{}
	if _, err := st.IngestAgentEvent(context.Background(), in); err != nil {
		t.Fatal(err)
	}
}

func TestListAgentSessionsOverlaysWaitingYouFromPendingFeedback(t *testing.T) {
	st := newMemStore(t)
	ctx := context.Background()
	mustIngest(t, st, agentIn(agentEv(0, agentstate.EvPrompt)))
	now := agentBase.Add(time.Minute)
	get := func() AgentSessionView {
		v, err := st.ListAgentSessions(ctx, AgentSessionFilter{WorkflowID: "wf-a"}, now)
		if err != nil || len(v) != 1 {
			t.Fatalf("list = %v err=%v", v, err)
		}
		return v[0]
	}
	if v := get(); v.State != "working" || v.WaitingYou {
		t.Fatalf("before pending: %+v", v)
	}
	// 异步模式的 notice 气泡不算「在等人」。
	st.db.Create(&model.FeedbackSession{ID: "sess-n", WorkflowID: "wf-a", Kind: "notice", Status: "pending", Summary: "n"})
	if v := get(); v.WaitingYou {
		t.Fatalf("notice counted as waiting: %+v", v)
	}
	st.db.Create(&model.FeedbackSession{ID: "sess-p", WorkflowID: "wf-a", Status: "pending", Summary: "p"})
	if v := get(); v.State != "waiting_you" || !v.WaitingYou || v.BaseState != "working" || v.Stalled {
		t.Fatalf("with pending feedback: %+v", v)
	}
	st.db.Model(&model.FeedbackSession{}).Where("id = ?", "sess-p").Update("status", "completed")
	if v := get(); v.WaitingYou {
		t.Fatalf("completed feedback still waiting: %+v", v)
	}
}

func TestListAgentSessionsFlagsStalledAndMCPActivityClearsIt(t *testing.T) {
	st := newMemStore(t)
	ctx := context.Background()
	mustIngest(t, st, agentIn(agentEv(0, agentstate.EvPrompt)))
	later := agentBase.Add(40 * time.Minute)
	v, _ := st.ListAgentSessions(ctx, AgentSessionFilter{}, later)
	if len(v) != 1 || !v[0].Stalled || v[0].Reason != agentstate.ReasonQuiet {
		t.Fatalf("expected stalled: %+v", v)
	}
	// 同一个工作流的 MCP 调用算活动（hook 静默期间 Agent 仍在调工具）。
	st.RecordAgentActivity("wf-a", later.Add(-time.Minute))
	v, _ = st.ListAgentSessions(ctx, AgentSessionFilter{}, later)
	if v[0].Stalled || v[0].StalledSoft || v[0].QuietSeconds != 60 {
		t.Fatalf("mcp activity ignored: %+v", v[0])
	}
}

func TestListAgentSessionsFiltersAndOrders(t *testing.T) {
	st := newMemStore(t)
	ctx := context.Background()
	mustIngest(t, st, agentIn(agentEv(0, agentstate.EvPrompt)))
	b := agentIn(agentEv(1000, agentstate.EvPrompt))
	b.WorkflowID, b.ConversationID, b.ProjectDirectory = "wf-b", "conv-b", "/home/user/Other"
	rb := mustIngest(t, st, b)
	c := agentIn(agentEv(2000, agentstate.EvPrompt))
	c.ConversationID = "conv-c"
	mustIngest(t, st, c)

	all, _ := st.ListAgentSessions(ctx, AgentSessionFilter{}, agentBase.Add(time.Minute))
	if len(all) != 3 || all[0].ConversationID != "conv-c" || all[2].ConversationID != "conv-1" {
		t.Fatalf("order = %+v", all)
	}
	onlyA, _ := st.ListAgentSessions(ctx, AgentSessionFilter{WorkflowID: "wf-a"}, agentBase.Add(time.Minute))
	if len(onlyA) != 2 {
		t.Fatalf("workflow filter = %d rows", len(onlyA))
	}
	byID, _ := st.ListAgentSessions(ctx, AgentSessionFilter{IdentityID: rb.IdentityID}, agentBase.Add(time.Minute))
	if len(byID) != 1 || byID[0].WorkflowID != "wf-b" {
		t.Fatalf("identity filter = %+v", byID)
	}
	one, _ := st.ListAgentSessions(ctx, AgentSessionFilter{Limit: 1}, agentBase.Add(time.Minute))
	if len(one) != 1 {
		t.Fatalf("limit = %d rows", len(one))
	}
	none, err := st.ListAgentSessions(ctx, AgentSessionFilter{WorkflowID: "nope"}, agentBase)
	if err != nil || none == nil || len(none) != 0 {
		t.Fatalf("empty result = %#v err=%v", none, err)
	}
}

func TestRenameAgentIdentityAndLabels(t *testing.T) {
	st := newMemStore(t)
	ctx := context.Background()
	a := mustIngest(t, st, agentIn(agentEv(0, agentstate.EvPrompt)))
	in := agentIn(agentEv(10, agentstate.EvPrompt))
	in.WorkflowID, in.HostName, in.ProjectDirectory, in.ConversationID = "wf-b", "laptop", "/Users/me/RelayMesh", "conv-b"
	b := mustIngest(t, st, in)

	ids, _ := st.ListAgentIdentities(ctx)
	if len(ids) != 2 {
		t.Fatalf("identities = %+v", ids)
	}
	for _, v := range ids {
		if !v.LabelDegraded || !strings.Contains(v.Label, ":") {
			t.Fatalf("duplicate names not degraded: %+v", v)
		}
	}

	v, err := st.RenameAgentIdentity(ctx, b.IdentityID, "  前端助手\n", "tester")
	if err != nil {
		t.Fatal(err)
	}
	if v.Name != "前端助手" || v.Label != "前端助手" || v.LabelDegraded {
		t.Fatalf("renamed view = %+v", v)
	}
	// 改名之后另一个不再重名，恢复目录名。
	ids, _ = st.ListAgentIdentities(ctx)
	for _, x := range ids {
		if x.ID == a.IdentityID && (x.Label != "RelayMesh" || x.LabelDegraded) {
			t.Fatalf("other identity not restored: %+v", x)
		}
	}
	var audit []model.AgentEventRecord
	st.db.Where("event = 'rename'").Find(&audit)
	if len(audit) != 1 || audit[0].IdentityID != b.IdentityID || audit[0].Source != "web" || !strings.Contains(audit[0].Extra, "tester") {
		t.Fatalf("audit rows = %+v", audit)
	}
	// 重复提交同名：不再写审计。
	if _, err := st.RenameAgentIdentity(ctx, b.IdentityID, "前端助手", "tester"); err != nil {
		t.Fatal(err)
	}
	st.db.Where("event = 'rename'").Find(&audit)
	if len(audit) != 1 {
		t.Fatalf("no-op rename wrote audit: %d", len(audit))
	}
	// 清空 = 恢复目录名。
	v, err = st.RenameAgentIdentity(ctx, b.IdentityID, "", "tester")
	if err != nil || v.Name != "" || !v.LabelDegraded {
		t.Fatalf("clear rename = %+v err=%v", v, err)
	}
}

func TestRenameAgentIdentityValidation(t *testing.T) {
	st := newMemStore(t)
	ctx := context.Background()
	a := mustIngest(t, st, agentIn(agentEv(0, agentstate.EvPrompt)))
	if _, err := st.RenameAgentIdentity(ctx, a.IdentityID, strings.Repeat("长", AgentNameMaxRunes+1), "x"); err == nil {
		t.Fatal("over-long name accepted")
	}
	if _, err := st.RenameAgentIdentity(ctx, a.IdentityID, strings.Repeat("长", AgentNameMaxRunes), "x"); err != nil {
		t.Fatalf("max-length name rejected: %v", err)
	}
	if _, err := st.RenameAgentIdentity(ctx, 9999, "x", "x"); err == nil {
		t.Fatal("missing identity accepted")
	} else if ae, ok := err.(*AppError); !ok || ae.Kind != ErrNotFound {
		t.Fatalf("missing identity error = %#v", err)
	}
}

func TestTouchAgentIdentityRecordsMCPAndQueryTimes(t *testing.T) {
	st := newMemStore(t)
	ctx := context.Background()
	in := AgentTouchInput{WorkflowID: "wf-a", CredentialID: 7, HostName: "devhost", ProjectDirectory: "/home/user/RelayMesh"}
	if err := st.TouchAgentIdentity(ctx, in, agentBase); err != nil {
		t.Fatal(err)
	}
	var id model.AgentIdentity
	if err := st.db.First(&id).Error; err != nil {
		t.Fatal(err)
	}
	if id.LastMCPAt == nil || !id.LastMCPAt.Equal(agentBase) || id.LastQueryAt != nil {
		t.Fatalf("first touch: mcp=%v query=%v", id.LastMCPAt, id.LastQueryAt)
	}
	in.Query = true
	st.TouchAgentIdentity(ctx, in, agentBase.Add(5*time.Second)) // 节流内：不写库
	st.db.First(&id)
	if !id.LastMCPAt.Equal(agentBase) || id.LastQueryAt != nil {
		t.Fatalf("throttled touch was written: mcp=%v query=%v", id.LastMCPAt, id.LastQueryAt)
	}
	st.TouchAgentIdentity(ctx, in, agentBase.Add(25*time.Second)) // 节流到期：写库
	st.db.First(&id)
	if id.LastMCPAt == nil || !id.LastMCPAt.Equal(agentBase.Add(25*time.Second)) {
		t.Fatalf("last_mcp_at = %v", id.LastMCPAt)
	}
	if id.LastQueryAt == nil || !id.LastQueryAt.Equal(agentBase.Add(25*time.Second)) {
		t.Fatalf("last_query_at = %v", id.LastQueryAt)
	}
	if n := countRows(t, st, &model.AgentEventRecord{}); n != 0 {
		t.Fatalf("MCP touches produced %d event rows", n)
	}
	if n := countRows(t, st, &model.AgentSession{}); n != 0 {
		t.Fatalf("MCP touches created %d sessions", n)
	}
	// 认不出项目目录：不记。
	if err := st.TouchAgentIdentity(ctx, AgentTouchInput{WorkflowID: "wf-unknown"}, agentBase); err != nil {
		t.Fatal(err)
	}
	if n := countRows(t, st, &model.AgentIdentity{}); n != 1 {
		t.Fatalf("identities = %d", n)
	}
	if err := st.TouchAgentIdentity(ctx, AgentTouchInput{}, agentBase); err != nil {
		t.Fatal(err)
	}
}

// 库里的 last_* 按 20 秒节流写；读取路径（身份列表、会话视图、改名返回）必须看到内存里最新的时间戳，
// 否则身份卡上「最后查询：刚刚」会显示成「—」或一个旧时间（端到端测试 agent-state-ui.mjs 抓到过）。
func TestIdentityViewsOverlayLiveTimestamps(t *testing.T) {
	st := newMemStore(t)
	ctx := context.Background()
	first := mustIngest(t, st, agentIn(agentEv(0, agentstate.EvSessionStart))) // 建立身份与会话，第一次触摸立刻写库
	touch := AgentTouchInput{WorkflowID: "wf-a", CredentialID: 7, HostName: "devhost", ProjectDirectory: "/home/user/RelayMesh"}

	queryAt := agentBase.Add(5 * time.Second)
	q := touch
	q.Query = true
	if err := st.TouchAgentIdentity(ctx, q, queryAt); err != nil {
		t.Fatal(err)
	}
	var row model.AgentIdentity
	if err := st.db.First(&row, first.IdentityID).Error; err != nil {
		t.Fatal(err)
	}
	if row.LastQueryAt != nil {
		t.Fatalf("测试前提不成立：节流窗口内的触摸不应写库，库里 last_query_at = %v", row.LastQueryAt)
	}

	list, err := st.ListAgentIdentities(ctx)
	if err != nil || len(list) != 1 {
		t.Fatalf("ListAgentIdentities = %v, %v", list, err)
	}
	if got := list[0]; got.LastQueryAt == nil || !got.LastQueryAt.Equal(queryAt) || got.LastMCPAt == nil || !got.LastMCPAt.Equal(queryAt) || !got.LastSeenAt.Equal(queryAt) {
		t.Fatalf("身份列表应带内存里最新的时间: query=%v mcp=%v seen=%v want %v", got.LastQueryAt, got.LastMCPAt, got.LastSeenAt, queryAt)
	}

	sessions, err := st.ListAgentSessions(ctx, AgentSessionFilter{WorkflowID: "wf-a"}, queryAt)
	if err != nil || len(sessions) != 1 {
		t.Fatalf("ListAgentSessions = %v, %v", sessions, err)
	}
	if got := sessions[0].LastQueryAt; got == nil || !got.Equal(queryAt) {
		t.Fatalf("会话视图的最后查询 = %v, want %v", got, queryAt)
	}

	renamed, err := st.RenameAgentIdentity(ctx, first.IdentityID, "我的名字", "test")
	if err != nil {
		t.Fatal(err)
	}
	if renamed.LastQueryAt == nil || !renamed.LastQueryAt.Equal(queryAt) {
		t.Fatalf("改名返回的视图应带最新时间: %v", renamed.LastQueryAt)
	}

	hookAt := agentBase.Add(8 * time.Second)
	h := touch
	h.Hook = true
	if err := st.TouchAgentIdentity(ctx, h, hookAt); err != nil {
		t.Fatal(err)
	}
	list, err = st.ListAgentIdentities(ctx)
	if err != nil || len(list) != 1 {
		t.Fatalf("ListAgentIdentities = %v, %v", list, err)
	}
	if got := list[0]; got.LastHookAt == nil || !got.LastHookAt.Equal(hookAt) || !got.LastSeenAt.Equal(hookAt) {
		t.Fatalf("hook 的时间也应是最新的: hook=%v seen=%v want %v", got.LastHookAt, got.LastSeenAt, hookAt)
	}
	// 库里的时间不会因为读取而被改写（节流照旧）。
	if err := st.db.First(&row, first.IdentityID).Error; err != nil {
		t.Fatal(err)
	}
	if row.LastQueryAt != nil {
		t.Fatalf("读取不应写库: last_query_at = %v", row.LastQueryAt)
	}
}

func TestTouchBeforeHookEventSharesTheIdentity(t *testing.T) {
	st := newMemStore(t)
	ctx := context.Background()
	st.TouchAgentIdentity(ctx, AgentTouchInput{WorkflowID: "wf-a", CredentialID: 7, HostName: "devhost", ProjectDirectory: "/home/user/RelayMesh/"}, agentBase)
	mustIngest(t, st, agentIn(agentEv(1000, agentstate.EvPrompt)))
	if n := countRows(t, st, &model.AgentIdentity{}); n != 1 {
		t.Fatalf("identities = %d, want 1 (trailing slash must not split)", n)
	}
}

func TestPruneAgentStateRemovesOldAndCapsRows(t *testing.T) {
	st := newMemStore(t)
	ctx := context.Background()
	a := mustIngest(t, st, agentIn(agentEv(0, agentstate.EvPrompt)))
	now := agentBase.Add(24 * time.Hour)

	old := make([]model.AgentEventRecord, 0, 10)
	for i := 0; i < 10; i++ {
		old = append(old, model.AgentEventRecord{IdentityID: a.IdentityID, SessionID: a.SessionID, At: now.Add(-8 * 24 * time.Hour), Source: "hook", Event: "thought"})
	}
	if err := st.db.Create(&old).Error; err != nil {
		t.Fatal(err)
	}
	fresh := make([]model.AgentEventRecord, 0, agentEventsKeepRows+50)
	for i := 0; i < agentEventsKeepRows+50; i++ {
		fresh = append(fresh, model.AgentEventRecord{IdentityID: a.IdentityID, SessionID: a.SessionID, At: now.Add(-time.Hour), Source: "hook", Event: "thought"})
	}
	if err := st.db.CreateInBatches(&fresh, 500).Error; err != nil {
		t.Fatal(err)
	}
	// 一个 100 天没有动静的会话连同它的事件一起清掉。
	dead := model.AgentSession{IdentityID: a.IdentityID, ConversationID: "dead", State: "idle", LastEventAt: now.Add(-100 * 24 * time.Hour)}
	st.db.Create(&dead)
	st.db.Create(&model.AgentEventRecord{IdentityID: a.IdentityID, SessionID: dead.ID, At: now.Add(-time.Hour), Source: "hook", Event: "stop"})

	st.agent.mu.Lock()
	st.pruneAgentStateLocked(ctx, now)
	st.agent.mu.Unlock()

	var oldLeft int64
	st.db.Model(&model.AgentEventRecord{}).Where("at < ?", now.Add(-7*24*time.Hour)).Count(&oldLeft)
	if oldLeft != 0 {
		t.Fatalf("%d week-old events survived", oldLeft)
	}
	var deadEvents, deadSessions, total int64
	st.db.Model(&model.AgentEventRecord{}).Where("session_id = ?", dead.ID).Count(&deadEvents)
	st.db.Model(&model.AgentSession{}).Where("id = ?", dead.ID).Count(&deadSessions)
	total = countRows(t, st, &model.AgentEventRecord{})
	if deadEvents != 0 || deadSessions != 0 {
		t.Fatalf("dead session survived: events=%d sessions=%d", deadEvents, deadSessions)
	}
	if total > agentEventsKeepRows {
		t.Fatalf("event rows = %d, cap is %d", total, agentEventsKeepRows)
	}
	if total < agentEventsKeepRows-10 {
		t.Fatalf("event rows = %d, pruned far below the cap", total)
	}
	if n := countRows(t, st, &model.AgentSession{}); n != 1 {
		t.Fatalf("live session removed: %d sessions left", n)
	}
}

func TestIngestPrunesPeriodically(t *testing.T) {
	st := newMemStore(t)
	a := mustIngest(t, st, agentIn(agentEv(0, agentstate.EvPrompt)))
	st.db.Create(&model.AgentEventRecord{IdentityID: a.IdentityID, SessionID: a.SessionID, At: agentBase.Add(-30 * 24 * time.Hour), Source: "hook", Event: "old"})
	st.agent.mu.Lock()
	st.agent.ingested = agentPruneEveryIngests - 1
	st.agent.mu.Unlock()
	mustIngest(t, st, agentIn(agentEv(7000, agentstate.EvThought)))
	var n int64
	st.db.Model(&model.AgentEventRecord{}).Where("event = 'old'").Count(&n)
	if n != 0 {
		t.Fatal("periodic prune did not run")
	}
}

func TestIngestIsSafeUnderConcurrency(t *testing.T) {
	st := newMemStore(t)
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < 60; i++ {
				in := agentIn(agentEv(g*100000+i*100, agentstate.EvToolEnd))
				in.WorkflowID = "wf-" + string(rune('a'+g%3))
				in.ConversationID = "conv-" + string(rune('a'+g))
				if _, err := st.IngestAgentEvent(context.Background(), in); err != nil {
					t.Error(err)
					return
				}
				_ = st.TouchAgentIdentity(context.Background(), AgentTouchInput{WorkflowID: in.WorkflowID, CredentialID: 7, HostName: "devhost", ProjectDirectory: "/home/user/RelayMesh"}, agentBase.Add(time.Duration(i)*time.Second))
			}
		}(g)
	}
	wg.Wait()
	if n := countRows(t, st, &model.AgentSession{}); n != 8 {
		t.Fatalf("sessions = %d, want 8", n)
	}
	if n := countRows(t, st, &model.AgentIdentity{}); n != 1 {
		t.Fatalf("identities = %d, want 1", n)
	}
}

// 设计稿 04 §6 第 6 条：每分钟 1000 条事件不能拖慢 Hub。这里用一个宽松的上限挡住「每条事件都要几十毫秒」这种退化。
func TestIngestThroughputIsReasonable(t *testing.T) {
	st := newMemStore(t)
	const n = 3000
	start := time.Now()
	for i := 0; i < n; i++ {
		ev := agentEv(i*20, []string{agentstate.EvThought, agentstate.EvToolStart, agentstate.EvToolEnd}[i%3])
		ev.Tool = "Read"
		in := agentIn(ev)
		in.ConversationID = "conv-" + string(rune('a'+i%5))
		if _, err := st.IngestAgentEvent(context.Background(), in); err != nil {
			t.Fatal(err)
		}
	}
	if d := time.Since(start); d > 20*time.Second {
		t.Fatalf("%d events took %v", n, d)
	} else {
		t.Logf("%d events in %v (%.2f ms/event)", n, d, float64(d.Microseconds())/1000/n)
	}
}

func BenchmarkIngestAgentEvent(b *testing.B) {
	st, err := New(":memory:")
	if err != nil {
		b.Fatal(err)
	}
	defer st.Close()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ev := agentEv(i*20, []string{agentstate.EvThought, agentstate.EvToolStart, agentstate.EvToolEnd}[i%3])
		ev.Tool = "Read"
		if _, err := st.IngestAgentEvent(context.Background(), agentIn(ev)); err != nil {
			b.Fatal(err)
		}
	}
}
