package store

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/teacat99/RelayMesh/v2/internal/agentstate"
	"github.com/teacat99/RelayMesh/v2/internal/model"
)

// 分支概况里的「状态」三项（#236 P4）：Agent 状态、名下任务数、启动方式。只给活动关系填，状态恒向主会话开放。

func branchBriefOf(t *testing.T, st *Store, branch string) BranchBrief {
	t.Helper()
	rel, err := st.BranchOf(context.Background(), branch)
	if err != nil || rel == nil {
		t.Fatalf("读取关系 %s: %v %v", branch, rel, err)
	}
	return st.BranchBriefs(context.Background(), []model.WorkflowBranch{*rel})[0]
}

// ingestAt 给一个工作流记一条状态事件（at 是事件发生的时间）。
func ingestAt(t *testing.T, st *Store, wf, conv, name string, at time.Time, end bool) {
	t.Helper()
	ev := agentstate.Event{Source: agentstate.SourceHook, Name: name, At: at, Received: at}
	if end {
		ev.Status = agentstate.EndCompleted
	}
	mustIngest(t, st, AgentIngestInput{
		WorkflowID: wf, CredentialID: 7, HostName: "devhost", ProjectDirectory: "/home/user/" + wf, ConversationID: conv, Event: ev,
	})
}

func TestBranchBriefsStatusIsOnlyForActiveRelations(t *testing.T) {
	st := newBranchStore(t)
	mkWF(t, st, "wf-main", "主会话")
	mkWF(t, st, "wf-b1", "分支一")
	mkWF(t, st, "wf-b2", "分支二")
	mustLink(t, st, "wf-main", "wf-b1", model.BranchCreatedByHuman)
	mustLink(t, st, "wf-main", "wf-b2", model.BranchCreatedByHuman)
	if _, err := st.CloseBranch(context.Background(), "wf-b2", model.BranchCreatedByHuman); err != nil {
		t.Fatal(err)
	}

	// 活动的：什么数据都没有时，状态是 unknown（不是缺省），没有任务、启动方式写明「没开、没有预设」。
	b1 := branchBriefOf(t, st, "wf-b1")
	if b1.AgentState != BranchAgentStateUnknown || b1.AgentStateAt != nil || b1.Tasks != nil ||
		b1.Launch == nil || b1.Launch.Open || b1.Launch.Presets != 0 || b1.Launch.Preset != "" {
		t.Fatalf("活动分支的缺省状态不对: %+v tasks=%+v launch=%+v", b1, b1.Tasks, b1.Launch)
	}
	raw, _ := json.Marshal(b1)
	for _, want := range []string{`"agent_state":"unknown"`, `"launch":{"open":false,"presets":0}`} {
		if !strings.Contains(string(raw), want) {
			t.Fatalf("JSON 里应有 %s: %s", want, raw)
		}
	}
	// 注意 expose 里本来就有 "tasks":"assign" 与 "launch":false 两个键：要找的是对象形式。
	if strings.Contains(string(raw), `"tasks":{`) || strings.Contains(string(raw), `"agent_state_at"`) {
		t.Fatalf("没有任务、没有状态时间时这两个字段不该出现: %s", raw)
	}

	// 已结束的：四项都不填（状态是「这个分支现在在干什么」，关系结束后不再对主会话有意义），JSON 里一个字都不多。
	b2 := branchBriefOf(t, st, "wf-b2")
	if b2.AgentState != "" || b2.AgentStateAt != nil || b2.Tasks != nil || b2.Launch != nil {
		t.Fatalf("已结束的关系不该带状态: %+v", b2)
	}
	raw, _ = json.Marshal(b2)
	for _, banned := range []string{"agent_state", `"tasks":{`, `"launch":{`} {
		if strings.Contains(string(raw), banned) {
			t.Fatalf("已结束的关系 JSON 里不该出现 %s: %s", banned, raw)
		}
	}
}

func TestBranchBriefsTasksUseTheGateRule(t *testing.T) {
	st := newBranchStore(t)
	mkWF(t, st, "wf-main", "主会话")
	mkWF(t, st, "wf-b1", "分支一")
	mkWF(t, st, "wf-b2", "分支二")
	mustLink(t, st, "wf-main", "wf-b1", model.BranchCreatedByHuman)
	mustLink(t, st, "wf-main", "wf-b2", model.BranchCreatedByHuman)

	// 分支一名下：自己建的待办 1、自己建的进行中 1、主会话派来并已接取的进行中 1、自己建的验证中 1；
	// 已完成、已搁置不算；主会话派来但被拒绝的（回到派发者手里）不算；分支二自己的任务不能串到分支一。
	putGateTask(t, st, "wf-b1", 1, model.WorkStateTodo, "", "", "")
	putGateTask(t, st, "wf-b1", 2, model.WorkStateDoing, "", "", "")
	putGateTask(t, st, "wf-main", 3, model.WorkStateDoing, "wf-b1", "wf-main", model.WorkClaimAccepted)
	putGateTask(t, st, "wf-b1", 4, model.WorkStateReview, "", "", "")
	putGateTask(t, st, "wf-b1", 5, model.WorkStateDone, "", "", "")
	putGateTask(t, st, "wf-b1", 6, model.WorkStateShelved, "", "", "")
	putGateTask(t, st, "wf-main", 7, model.WorkStateTodo, "wf-b1", "wf-main", model.WorkClaimDeclined)
	putGateTask(t, st, "wf-b2", 8, model.WorkStateTodo, "", "", "")

	b1 := branchBriefOf(t, st, "wf-b1")
	if b1.Tasks == nil || b1.Tasks.Todo != 1 || b1.Tasks.Doing != 2 || b1.Tasks.Review != 1 {
		t.Fatalf("分支一的任务数应是 待办1 / 进行中2 / 验证中1: %+v", b1.Tasks)
	}
	b2 := branchBriefOf(t, st, "wf-b2")
	if b2.Tasks == nil || b2.Tasks.Todo != 1 || b2.Tasks.Doing != 0 || b2.Tasks.Review != 0 {
		t.Fatalf("分支二只有自己的一个待办: %+v", b2.Tasks)
	}
	raw, _ := json.Marshal(b2)
	if !strings.Contains(string(raw), `"tasks":{"todo":1}`) {
		t.Fatalf("为 0 的项不输出: %s", raw)
	}

	// 口径与收尾门同源：数出来的总数 == gateTasks 的总数。
	if _, total := st.gateTasks(context.Background(), "wf-b1", 100); total != b1.Tasks.total() {
		t.Fatalf("应与收尾门同口径: gate=%d brief=%d", total, b1.Tasks.total())
	}
}

func TestBranchBriefsAgentStateFollowsTheSessionPickRule(t *testing.T) {
	st := newBranchStore(t)
	ctx := context.Background()
	mkWF(t, st, "wf-main", "主会话")
	mkWF(t, st, "wf-b1", "分支一")
	mkWF(t, st, "wf-b2", "分支二")
	mustLink(t, st, "wf-main", "wf-b1", model.BranchCreatedByHuman)
	mustLink(t, st, "wf-main", "wf-b2", model.BranchCreatedByHuman)

	// 刚有动静：working，状态开始时间有值。
	now := time.Now()
	ingestAt(t, st, "wf-b1", "conv-b1", agentstate.EvPrompt, now.Add(-time.Second), false)
	b1 := branchBriefOf(t, st, "wf-b1")
	if b1.AgentState != "working" || b1.AgentStateAt == nil {
		t.Fatalf("刚提交过提示词应是 working: %q %v", b1.AgentState, b1.AgentStateAt)
	}

	// 与网页的挑选规则一致：只有已结束的会话时取最近的那个（ended）；又来一个活动会话后取活动的。
	ingestAt(t, st, "wf-b2", "conv-old", agentstate.EvPrompt, now.Add(-40*time.Minute), false)
	ingestAt(t, st, "wf-b2", "conv-old", agentstate.EvSessionEnd, now.Add(-30*time.Minute), true)
	if b2 := branchBriefOf(t, st, "wf-b2"); b2.AgentState != "ended" {
		t.Fatalf("只有已结束的会话时应是 ended: %q", b2.AgentState)
	}
	ingestAt(t, st, "wf-b2", "conv-new", agentstate.EvPrompt, now.Add(-2*time.Second), false)
	if b2 := branchBriefOf(t, st, "wf-b2"); b2.AgentState != "working" {
		t.Fatalf("有活动会话时应取活动的: %q", b2.AgentState)
	}

	// 卡住盖过其他展示态（与网页状态芯片一致），其余原样取展示态：不同的久无动静时长下，概况与会话视图逐一对得上。
	for _, age := range []time.Duration{0, 20 * time.Minute, 3 * time.Hour, 30 * time.Hour} {
		wf := "wf-age-" + age.String()
		mkWF(t, st, wf, "分支 "+age.String())
		// 循环里一口气建好几个分支：跳过「每小时新建数」限制（迁移命令同款），每轮结束再结束它，不撞活动分支上限。
		if _, err := st.LinkBranch(ctx, LinkBranchInput{ParentWorkflowID: "wf-main", BranchWorkflowID: wf, CreatedBy: model.BranchCreatedByHuman, SkipRateLimit: true}); err != nil {
			t.Fatal(err)
		}
		ingestAt(t, st, wf, "conv-"+wf, agentstate.EvPrompt, time.Now().Add(-age), false)
		views, err := st.ListAgentSessions(ctx, AgentSessionFilter{WorkflowID: wf}, time.Now())
		if err != nil || len(views) != 1 {
			t.Fatalf("%s：读取会话视图: %v %+v", wf, err, views)
		}
		want := views[0].State
		if views[0].Stalled {
			want = BranchAgentStateStalled
		}
		if got := branchBriefOf(t, st, wf).AgentState; got != want {
			t.Fatalf("%s：概况里的状态 %q 应与会话视图（卡住优先）%q 一致", wf, got, want)
		}
		// 一个活动分支腾出名额给下一轮。
		if _, err := st.CloseBranch(ctx, wf, model.BranchCreatedByHuman); err != nil {
			t.Fatal(err)
		}
	}
}

func TestPickBranchSession(t *testing.T) {
	mk := func(states ...string) []AgentSessionView {
		out := make([]AgentSessionView, 0, len(states))
		for i, s := range states {
			out = append(out, AgentSessionView{ID: uint(i + 1), State: s})
		}
		return out
	}
	for name, c := range map[string]struct {
		in   []AgentSessionView
		want uint // 0 = nil
	}{
		"空":      {nil, 0},
		"活动的排前面": {mk("working", "ended"), 1},
		"最近的结束了，更早的还活着": {mk("ended", "idle", "working"), 2},
		"离线不算活动，没结束的优先": {mk("ended", "offline", "ended"), 2},
		"全结束取最近的":       {mk("ended", "ended"), 1},
		"状态为空的会话跳过":     {mk("", "idle"), 2},
	} {
		got := pickBranchSession(c.in)
		switch {
		case c.want == 0 && got != nil:
			t.Errorf("%s：应为 nil，得到 %+v", name, got)
		case c.want != 0 && (got == nil || got.ID != c.want):
			t.Errorf("%s：应取编号 %d，得到 %+v", name, c.want, got)
		}
	}
}

func TestBranchBriefsLaunchSummarisesThePresetsThatTargetTheBranch(t *testing.T) {
	st := newBranchStore(t)
	ctx := context.Background()
	mkWF(t, st, "wf-main", "主会话")
	mkWF(t, st, "wf-b1", "分支一")
	mkWF(t, st, "wf-b2", "分支二")
	mustLink(t, st, "wf-main", "wf-b1", model.BranchCreatedByHuman)
	managed, _ := model.BranchExposePreset(model.ExposePresetManaged)
	if _, err := st.LinkBranch(ctx, LinkBranchInput{ParentWorkflowID: "wf-main", BranchWorkflowID: "wf-b2", CreatedBy: model.BranchCreatedByHuman, Expose: &managed}); err != nil {
		t.Fatal(err)
	}

	save := func(l TerminalLauncher) {
		t.Helper()
		if _, _, err := st.UpsertTerminalLauncher(ctx, l); err != nil {
			t.Fatal(err)
		}
	}
	mkHeadless := func(id, name, wf string) TerminalLauncher {
		l := okHeadless(id, name)
		l.Run.WorkflowID = wf
		return l
	}
	save(mkHeadless("p-b1-z", "Z-续聊", "wf-b1"))
	save(mkHeadless("p-b1-a", "a-续聊", "wf-b1")) // 名字排序不分大小写：a 排在 Z 前面
	save(mkHeadless("p-b2", "B二号", "wf-b2"))
	save(mkHeadless("p-other", "别人的", "wf-elsewhere"))
	save(okLauncher("t-1", "终端预设"))       // 不是非交互续聊
	nowf := okHeadless("p-nowf", "没有工作流") // 没有目标工作流：只指定对话编号
	nowf.Run = &HeadlessRun{ConversationID: "conv-x", Posture: HeadlessPostureForce}
	save(nowf)

	b1 := branchBriefOf(t, st, "wf-b1")
	if b1.Launch == nil || b1.Launch.Open || b1.Launch.Presets != 2 || b1.Launch.Preset != "p-b1-a" || b1.Launch.Name != "a-续聊" {
		t.Fatalf("分支一：启动没开、两条预设、排在前面的是 a-续聊: %+v", b1.Launch)
	}
	b2 := branchBriefOf(t, st, "wf-b2")
	if b2.Launch == nil || !b2.Launch.Open || b2.Launch.Presets != 1 || b2.Launch.Preset != "p-b2" {
		t.Fatalf("分支二（托管档：启动打开）：一条预设: %+v", b2.Launch)
	}

	// 用户在网页里关掉 / 打开「启动」：概况下一次读取就反映出来。
	e := b2.Expose
	e.Launch = false
	if _, err := st.UpdateBranchSettings(ctx, "wf-b2", UpdateBranchInput{Expose: &e}); err != nil {
		t.Fatal(err)
	}
	if got := branchBriefOf(t, st, "wf-b2").Launch; got == nil || got.Open || got.Presets != 1 {
		t.Fatalf("关掉启动之后应显示没开: %+v", got)
	}
}
