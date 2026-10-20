package terminal

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/teacat99/RelayMesh/v2/internal/model"
	"github.com/teacat99/RelayMesh/v2/internal/termproto"
)

// 不属于任何预设的非交互运行（#147）：启动器弹窗的「其他非交互运行」区读它。
// 来源有三类：旧的会话页运行接口、会话守护的非交互唤醒、所属预设已被删除的预设运行；
// 预设自己的运行在预设下面显示，不算在内。

const otherPresetWF = "wf-other-preset"

var seedRunSeq atomic.Int64

func otherIDs(list []LauncherOtherRun) []string {
	out := make([]string, 0, len(list))
	for _, o := range list {
		out = append(out, o.RunID)
	}
	return out
}

func (f *execFixture) otherRuns(t *testing.T, now time.Time) []LauncherOtherRun {
	t.Helper()
	list, err := f.env.hub.guardian.otherRunViews(context.Background(), now)
	if err != nil {
		t.Fatal(err)
	}
	return list
}

// seedRun 直接写一条运行记录（不经主机）：用来造「很久以前结束」「早已残留」「一大批」这类时间与数量上的情形。
// ended 为 nil 表示没有结束时间；返回运行编号。
func (f *execFixture) seedRun(t *testing.T, caller, thread, state string, created time.Time, ended *time.Time) string {
	t.Helper()
	n := seedRunSeq.Add(1)
	rec := &model.TerminalRun{
		RunID: fmt.Sprintf("seed%05d", n), ThreadKey: fmt.Sprintf("seedthread%05d", n), Backend: "agent", ActorKind: ActorSystem,
		CredentialName: caller, WorkflowID: sessWF, Thread: thread, Target: f.a.code, Cwd: "/work/app",
		Description: "造出来的运行", Command: "agent -p --resume chat-x", State: state, TimeoutSec: 600,
		EndedAt: ended, CreatedAt: created,
	}
	if err := f.env.store.CreateTerminalRun(context.Background(), rec); err != nil {
		t.Fatal(err)
	}
	return rec.RunID
}

func TestOtherRunsListsOnlyRunsThatBelongToNoPreset(t *testing.T) {
	f := sessionFixture(t)
	now := time.Now()

	if got := f.otherRuns(t, now); got == nil || len(got) != 0 {
		t.Fatalf("什么都没运行过：应是空清单（不是 nil，JSON 里要是 []）: %#v", got)
	}

	// 预设自己的运行在预设下面显示，不算「其他」。
	f.saveLauncher(t, "p1", "续聊", map[string]any{"workflow_id": otherPresetWF, "conversation_id": "chat-p1"})
	presetPlan, err := f.runLauncher("p1")
	if err != nil {
		t.Fatal(err)
	}
	if got := f.otherRuns(t, now); len(got) != 0 {
		t.Fatalf("还在的预设自己的运行不该算其他: %+v", got)
	}

	// 旧接口（POST /terminal/session-run）提交的运行不属于任何预设。
	sess, err := f.runSession(sessionRunRequest{WorkflowID: sessWF, ConversationID: sessConv})
	if err != nil {
		t.Fatal(err)
	}
	got := f.otherRuns(t, now)
	if len(got) != 1 {
		t.Fatalf("应只有旧接口的这一次运行: %+v", got)
	}
	o := got[0]
	if o.RunID != sess.RunID || o.Source != OtherRunSessionRun || !o.Active || o.State != model.RunRunning {
		t.Fatalf("旧接口运行的现状不对: %+v", o)
	}
	if o.WorkflowID != sessWF || o.Host != f.a.code || o.Dir != "/work/app" || o.ThreadKey == "" || !strings.Contains(o.Description, "会话页运行") {
		t.Fatalf("应带工作流、主机、目录、线程键与说明（界面据此归类与链到 ExecMesh）: %+v", o)
	}

	// 删掉预设：它留下的运行成了「其他」，新的在前。
	if res := f.env.call("DELETE", "/api/v1/terminal/launchers/p1", nil, f.adm...); res.Status != 200 {
		t.Fatalf("删除预设失败: %d %s", res.Status, res.Raw)
	}
	got = f.otherRuns(t, now)
	if ids := otherIDs(got); len(ids) != 2 || ids[0] != sess.RunID || ids[1] != presetPlan.RunID {
		t.Fatalf("预设删除后它的运行应成为其他、且新的在前: %v（旧接口 %s，预设 %s）", ids, sess.RunID, presetPlan.RunID)
	}
	if got[1].Source != OtherRunDeletedPreset || !got[1].Active || got[1].WorkflowID != otherPresetWF {
		t.Fatalf("已删除预设的运行来源不对: %+v", got[1])
	}
}

func TestOtherRunsListsWakeRunsAndKeepsFinishedOnesFor24Hours(t *testing.T) {
	f := sessionFixture(t)
	now := time.Now()
	ended := now.Add(-time.Hour)
	wake := f.seedRun(t, headlessCallerName, "", model.RunSucceeded, ended.Add(-time.Minute), &ended)

	got := f.otherRuns(t, now)
	if len(got) != 1 || got[0].RunID != wake || got[0].Source != OtherRunWake || got[0].Active {
		t.Fatalf("非交互唤醒的运行应列为其他: %+v", got)
	}
	if n := len(f.otherRuns(t, now.Add(22*time.Hour))); n != 1 {
		t.Fatalf("结束 22 小时后还应在清单里: %d", n)
	}
	if n := len(f.otherRuns(t, now.Add(24*time.Hour+time.Minute))); n != 0 {
		t.Fatalf("结束超过 24 小时就该退出清单: %d", n)
	}
}

func TestOtherRunsShowsTheReplyOfAFinishedRun(t *testing.T) {
	f := sessionFixture(t)
	f.completeWith(launcherResultOut)
	plan, err := f.runSession(sessionRunRequest{WorkflowID: sessWF, ConversationID: sessConv})
	if err != nil {
		t.Fatal(err)
	}
	f.waitRunFinal(t, plan.RunID)

	got := f.otherRuns(t, time.Now())
	if len(got) != 1 {
		t.Fatalf("结束不久的运行还应在清单里: %+v", got)
	}
	o := got[0]
	if o.State != model.RunSucceeded || o.Active || o.Reply != "已恢复现场：收件箱为空。" || o.ExitCode == nil || *o.ExitCode != 0 {
		t.Fatalf("结束的运行应带最后一条回复与退出码: %+v", o)
	}
}

func TestOtherRunsSkipsStaleLeftoversAndOldFinishedOnes(t *testing.T) {
	f := sessionFixture(t)
	now := time.Now()
	old := now.Add(-30 * time.Hour)
	oldEnd := old.Add(time.Minute)
	f.seedRun(t, sessionRunCaller, sessionThreadFor(sessWF), model.RunSucceeded, old, &oldEnd)                 // 30 小时前结束
	f.seedRun(t, sessionRunCaller, sessionThreadFor(sessWF), model.RunRunning, now.Add(-10*24*time.Hour), nil) // 残留：早已过了自己的时限
	// 刚被拒绝的运行没有结束时间：退回最后更新时间，仍算 24 小时之内。
	denied := f.seedRun(t, sessionRunCaller, sessionThreadFor(sessWF), model.RunDenied, now.Add(-time.Minute), nil)

	got := f.otherRuns(t, now)
	if ids := otherIDs(got); len(ids) != 1 || ids[0] != denied {
		t.Fatalf("只应剩下刚被拒绝的那条（旧的结束记录与残留记录都不列）: %v", ids)
	}
	if got[0].Active || got[0].State != model.RunDenied {
		t.Fatalf("被拒绝的运行不在进行: %+v", got[0])
	}
}

func TestOtherRunsCapsFinishedOnesButNeverDropsActiveOnes(t *testing.T) {
	f := sessionFixture(t)
	now := time.Now()
	callers := []struct{ caller, thread string }{
		{sessionRunCaller, sessionThreadFor(sessWF)},
		{headlessCallerName, ""},
		{launcherRunCaller, launcherThreadPrefix + "gone"},
	}
	n := 0
	seed := func(state string) string {
		c := callers[n%len(callers)]
		created := now.Add(-time.Duration(60-n) * time.Second)
		n++
		if state == model.RunRunning {
			return f.seedRun(t, c.caller, c.thread, state, created, nil)
		}
		ended := created.Add(time.Second)
		return f.seedRun(t, c.caller, c.thread, state, created, &ended)
	}
	var older, newer []string
	for i := 0; i < 6; i++ {
		older = append(older, seed(model.RunSucceeded))
	}
	activeA := seed(model.RunRunning)
	for i := 0; i < 6; i++ {
		newer = append(newer, seed(model.RunFailed))
	}
	activeB := seed(model.RunRunning)

	// 两条进行中的一条不少；结束了的补到共 10 条（最新的 8 条），全部新的在前。
	want := []string{activeB}
	for i := len(newer) - 1; i >= 0; i-- {
		want = append(want, newer[i])
	}
	want = append(want, activeA, older[5], older[4])
	got := otherIDs(f.otherRuns(t, now))
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("清单不对：\n got %v\nwant %v", got, want)
	}

	// 进行中的比 10 条还多：全部列出，结束了的一条不留。
	for i := 0; i < 11; i++ {
		seed(model.RunRunning)
	}
	list := f.otherRuns(t, now)
	if len(list) != 13 {
		t.Fatalf("13 条进行中的一条都不能少: %d", len(list))
	}
	for _, o := range list {
		if !o.Active {
			t.Fatalf("进行中的比上限还多时不该再有结束了的: %+v", o)
		}
	}
}

// 弹窗用的接口：登录即可读；取消用的是 ExecMesh 现成的接口（登录即可，不需要解锁）。
func TestLauncherRunsRESTListsOthersAndCancelWorks(t *testing.T) {
	f := sessionFixture(t)
	path := "/api/v1/terminal/launchers/runs"
	if res := f.env.call("GET", path, nil); res.Status != 401 {
		t.Fatalf("没登录应 401: %d %s", res.Status, res.Raw)
	}

	plan, err := f.runSession(sessionRunRequest{WorkflowID: sessWF, ConversationID: sessConv})
	if err != nil {
		t.Fatal(err)
	}
	auth := []string{"Authorization", humanAuth(testUser)}
	res := f.env.call("GET", path, nil, auth...)
	others, _ := res.Body["others"].([]any)
	if res.Status != 200 || len(others) != 1 {
		t.Fatalf("应带 1 条其他运行: %d %s", res.Status, res.Raw)
	}
	if _, ok := res.Body["runs"].(map[string]any); !ok {
		t.Fatalf("runs 仍应是对象: %s", res.Raw)
	}
	o, _ := others[0].(map[string]any)
	if o["run_id"] != plan.RunID || o["source"] != OtherRunSessionRun || o["workflow_id"] != sessWF || o["host"] != f.a.code ||
		o["dir"] != "/work/app" || o["active"] != true || o["state"] != model.RunRunning || o["thread_key"] == "" {
		t.Fatalf("其他运行的字段不对: %s", res.Raw)
	}

	// 只登录（不解锁）就能取消：取消永远是安全方向的操作。
	if c := f.env.call("POST", "/api/v1/terminal/exec/runs/"+plan.RunID+"/cancel", nil, auth...); c.Status != 200 {
		t.Fatalf("取消失败: %d %s", c.Status, c.Raw)
	}
	cancel := f.firstCancel(t, "主机收到取消")
	f.sim.endFull(longEnd(plan.RunID, termproto.RunCancelled, nil, cancel.Reason))
	f.waitRunFinal(t, plan.RunID)

	res = f.env.call("GET", path, nil, auth...)
	others, _ = res.Body["others"].([]any)
	o, _ = others[0].(map[string]any)
	if len(others) != 1 || o["state"] != model.RunCancelled || o["active"] != false || o["why"] == "" {
		t.Fatalf("取消之后应显示已取消并带原因: %s", res.Raw)
	}
}
