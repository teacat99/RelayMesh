package terminal

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/teacat99/RelayMesh/v2/internal/model"
	"github.com/teacat99/RelayMesh/v2/internal/store"
)

// 主会话经 preset_run 唤醒自己的分支（#236 P4）：预设的资格改按「目标工作流是不是我的活动分支」分流。
//   - 目标是活动分支：只有它的主会话看得到、运行得了，别人（哪怕是同项目的管理 Agent）一律当「不存在」；分支在哪个任务板上不限；
//   - 「启动」开关是用户给的，每次现查现判：关着就列出来但不可运行（branch_launch_closed）；
//   - 其余（没有分支关系、关系已结束）：同板规则一个字没变。

// linkBranch 把 branch 连成 parent 的分支（协作档的开放范围，launch 决定「启动」开不开）。
func (fx *agentFx) linkBranch(t *testing.T, parent, branch string, launch bool) {
	t.Helper()
	e, _ := model.BranchExposePreset(model.ExposePresetCollab)
	e.Launch = launch
	if _, err := fx.env.store.LinkBranch(context.Background(), store.LinkBranchInput{
		ParentWorkflowID: parent, BranchWorkflowID: branch, CreatedBy: model.BranchCreatedByHuman, Expose: &e,
	}); err != nil {
		t.Fatal(err)
	}
}

// setLaunch 像用户在网页里改开放范围那样，只动「启动」这一项。
func (fx *agentFx) setLaunch(t *testing.T, branch string, launch bool) {
	t.Helper()
	rel, err := fx.env.store.BranchOf(context.Background(), branch)
	if err != nil || rel == nil {
		t.Fatalf("读取分支关系 %s: %v %v", branch, rel, err)
	}
	e := rel.Expose
	e.Launch = launch
	if _, err := fx.env.store.UpdateBranchSettings(context.Background(), branch, store.UpdateBranchInput{Expose: &e}); err != nil {
		t.Fatal(err)
	}
}

// otherManager 是同一个项目 P 里的另一个管理 Agent（工作流 wf-other）；qManager 是项目 Q 的管理 Agent（工作流 wf-q-mgr）。
func (fx *agentFx) otherManager(t *testing.T) AgentRunActor {
	t.Helper()
	fx.place(t, "wf-other", "/code/p")
	return AgentRunActor{IdentityID: 11, Label: "另一个管理", WorkflowID: "wf-other", ProjectID: fx.proj.ID}
}

func (fx *agentFx) qManager(t *testing.T) AgentRunActor {
	t.Helper()
	fx.place(t, "wf-q-mgr", "/code/q")
	return AgentRunActor{IdentityID: 8, Label: "Q 管理", WorkflowID: "wf-q-mgr", ProjectID: fx.other.ID}
}

func presetIDs(views []AgentPresetView) []string {
	out := make([]string, 0, len(views))
	for _, v := range views {
		out = append(out, v.Preset)
	}
	return out
}

func viewOf(views []AgentPresetView, id string) *AgentPresetView {
	for i := range views {
		if views[i].Preset == id {
			return &views[i]
		}
	}
	return nil
}

func wantAgentPresets(t *testing.T, who string, views []AgentPresetView, want ...string) {
	t.Helper()
	got := presetIDs(views)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("%s 应看到 %v，得到 %v", who, want, got)
	}
}

func TestAgentBranchPresetIsVisibleOnlyToItsMain(t *testing.T) {
	fx := newAgentFx(t)
	fx.completeWith("")
	other := fx.otherManager(t)

	// 没有分支关系：两个管理 Agent 看到的完全一样，视图里没有 branch 字段。
	before := fx.presets(t, fx.actor)
	wantAgentPresets(t, "主会话（连分支之前）", before, "p-child", "p-child2")
	// 另一个管理 Agent（wf-other）与主会话（wf-mgr）在同一块板上：对它来说 wf-mgr 的预设（p-own）是「别人的子会话」，看得到。
	wantAgentPresets(t, "另一个管理 Agent（连分支之前）", fx.presets(t, other), "p-child", "p-child2", "p-own")
	raw, _ := json.Marshal(before)
	if strings.Contains(string(raw), `"branch"`) {
		t.Fatalf("没有分支关系时视图里不该出现 branch 字段（网页 / MCP 回执与以前一字不差）：%s", raw)
	}

	fx.linkBranch(t, "wf-mgr", "wf-alpha", true)

	// 主会话：照旧看到两条，p-child 带上分支标记且可运行；p-child2 没有分支关系，不受影响。
	views := fx.presets(t, fx.actor)
	wantAgentPresets(t, "主会话", views, "p-child", "p-child2")
	if v := viewOf(views, "p-child"); !v.Branch || !v.CanRun || v.WhyNot != "" || v.WhyCode != "" || v.WorkflowID != "wf-alpha" {
		t.Fatalf("分支的启动预设应标 branch 且可运行: %+v", v)
	}
	if v := viewOf(views, "p-child2"); v.Branch || !v.CanRun {
		t.Fatalf("没有分支关系的预设不该带分支标记: %+v", v)
	}

	// 同项目的另一个管理 Agent：分支的预设对它不存在——列表里没有，状态与运行的拒绝和「根本没有这个预设」一模一样。
	wantAgentPresets(t, "另一个管理 Agent", fx.presets(t, other), "p-child2", "p-own")
	unknown := wantAgentCode(t, errOf(fx.run(other, "nope")), "preset_not_found")
	for _, ref := range []string{"p-child", "A-子会话"} {
		_, serr := fx.env.hub.AgentPresetStatus(context.Background(), other, ref)
		if e := wantAgentCode(t, serr, "preset_not_found"); e.Message != unknown.Message {
			t.Fatalf("%q：别人的分支预设应与不存在的预设同一句话（不泄露）：%q vs %q", ref, e.Message, unknown.Message)
		}
		if e := wantAgentCode(t, errOf(fx.run(other, ref)), "preset_not_found"); e.Message != unknown.Message {
			t.Fatalf("%q：运行的拒绝也要同一句话：%q vs %q", ref, e.Message, unknown.Message)
		}
	}
	if fx.sim.runCount() != 0 {
		t.Fatalf("被拒绝的请求不该到达主机: %d", fx.sim.runCount())
	}

	// 自己工作流的预设照旧是 own_workflow（主会话不是分支，BranchOf 查不到关系）。
	wantAgentCode(t, errOf(fx.run(fx.actor, "p-own")), "own_workflow")
}

func TestAgentBranchPresetWorksOnAnyBoard(t *testing.T) {
	fx := newAgentFx(t)
	fx.completeWith("")
	q := fx.qManager(t)

	// 分支在别的项目的任务板上：wf-alpha（项目 P 的板）是 Q 的管理 Agent（wf-q-mgr）的分支。
	fx.linkBranch(t, "wf-q-mgr", "wf-alpha", true)

	wantAgentPresets(t, "Q 的管理 Agent", fx.presets(t, q), "p-child", "p-q") // 自己的分支 + 自己项目板上的 wf-q
	if v := viewOf(fx.presets(t, q), "p-child"); !v.Branch || !v.CanRun {
		t.Fatalf("别的项目板上的分支，主会话也能运行: %+v", v)
	}
	if v := viewOf(fx.presets(t, q), "p-q"); v.Branch {
		t.Fatalf("同板预设不该带分支标记: %+v", v)
	}
	// P 的管理 Agent：wf-alpha 现在是别人的分支，哪怕它在 P 的板上也不再属于 P 的管理 Agent。
	wantAgentPresets(t, "P 的管理 Agent", fx.presets(t, fx.actor), "p-child2")
	wantAgentCode(t, errOf(fx.run(fx.actor, "p-child")), "preset_not_found")

	res, err := fx.run(q, "p-child")
	if err != nil {
		t.Fatalf("Q 的管理 Agent 应能运行自己分支的预设: %v", err)
	}
	if !res.Branch || res.WorkflowID != "wf-alpha" {
		t.Fatalf("回执应标明唤醒的是分支: %+v", res)
	}
	fx.waitRunFinal(t, res.RunID)

	// 分支不在任何项目的任务板上（wf-solo）：主会话照样能运行，别人看不到。
	fx.linkBranch(t, "wf-mgr", "wf-solo", true)
	views := fx.presets(t, fx.actor)
	wantAgentPresets(t, "P 的管理 Agent", views, "p-child2", "p-solo")
	if v := viewOf(views, "p-solo"); !v.Branch || !v.CanRun {
		t.Fatalf("项目外的分支，主会话也能运行: %+v", v)
	}
	if _, err := fx.run(fx.actor, "p-solo"); err != nil {
		t.Fatalf("项目外的分支应能运行: %v", err)
	}
	for _, id := range presetIDs(fx.presets(t, q)) {
		if id == "p-solo" {
			t.Fatal("别人看不到项目外分支的预设")
		}
	}
	wantAgentCode(t, errOf(fx.run(q, "p-solo")), "preset_not_found")

	// 没有项目的调用方：哪怕自称是主会话也一律看不到（门槛没有放宽）。
	loner := AgentRunActor{IdentityID: 12, Label: "无项目", WorkflowID: "wf-mgr"}
	if got := fx.presets(t, loner); len(got) != 0 {
		t.Fatalf("没有项目就没有资格: %+v", got)
	}
	wantAgentCode(t, errOf(fx.run(loner, "p-solo")), "preset_not_found")
}

func TestAgentBranchPresetLaunchSwitchIsCheckedLive(t *testing.T) {
	fx := newAgentFx(t)
	fx.completeWith("")
	fx.limits(AgentRunLimits{MaxPerHour: 1}) // 只剩每小时 1 次：被拒绝的尝试若占了名额，后面就跑不了
	fx.linkBranch(t, "wf-mgr", "wf-alpha", false)

	// 「启动」关着：预设仍列出来（让主会话知道下一步），但不可运行，原因点名了开关。
	v := viewOf(fx.presets(t, fx.actor), "p-child")
	if v == nil || !v.Branch || v.CanRun || v.WhyCode != "branch_launch_closed" || !strings.Contains(v.WhyNot, "启动") || v.LastRun != nil {
		t.Fatalf("关着时应列出但不可运行: %+v", v)
	}
	st, err := fx.env.hub.AgentPresetStatus(context.Background(), fx.actor, "p-child")
	if err != nil || st.CanRun || st.WhyCode != "branch_launch_closed" || !st.Branch {
		t.Fatalf("状态也应写明原因: %v %+v", err, st)
	}
	ae := wantAgentCode(t, errOf(fx.run(fx.actor, "p-child")), "branch_launch_closed")
	if !strings.Contains(ae.Message, "启动") {
		t.Fatalf("拒绝要点名「启动」开关与下一步: %q", ae.Message)
	}
	if fx.sim.runCount() != 0 || len(fx.agentAudits(t, ResultOK)) != 0 {
		t.Fatal("关着时不该到达主机、也不该进成功账本")
	}
	denies := fx.agentAudits(t, ResultDeny)
	if len(denies) != 1 || !strings.Contains(denies[0].Detail, "preset=p-child") || !strings.Contains(denies[0].Detail, "code=branch_launch_closed") ||
		denies[0].Actor != "agent:7" {
		t.Fatalf("被拒绝的尝试应留一条带预设与拒绝码的审计: %+v", denies)
	}

	// 用户打开「启动」：不用重启、不用重连，下一次调用就生效；被拒绝的尝试没有占名额。
	fx.setLaunch(t, "wf-alpha", true)
	if v := viewOf(fx.presets(t, fx.actor), "p-child"); !v.CanRun || v.WhyCode != "" {
		t.Fatalf("打开后应可运行: %+v", v)
	}
	res, err := fx.run(fx.actor, "p-child")
	if err != nil {
		t.Fatalf("打开后第一次真正的运行应成功: %v", err)
	}
	fx.waitRunFinal(t, res.RunID)

	// 用户又关上：再运行立刻被拒绝（先于任何别的判断：不会因为「刚运行过」换成别的拒绝码），已经提交的那次不受影响。
	fx.setLaunch(t, "wf-alpha", false)
	wantAgentCode(t, errOf(fx.run(fx.actor, "p-child")), "branch_launch_closed")
	if rec := fx.dbRun(t, res.RunID); rec.State != model.RunSucceeded {
		t.Fatalf("已经提交的运行不受开关影响: %+v", rec)
	}

	// 用户在网页上手动运行同一个预设，不受「启动」开关约束（开关只管主会话的 Agent）。
	if _, err := fx.runLauncher("p-child"); err != nil {
		t.Fatalf("手动运行不该受分支「启动」开关影响: %v", err)
	}
}

func TestAgentBranchPresetRunKeepsEveryGateAndMarksTheAudit(t *testing.T) {
	fx := newAgentFx(t)
	fx.completeWith("")
	fx.limits(AgentRunLimits{MaxPerHour: 1})
	fx.linkBranch(t, "wf-mgr", "wf-alpha", true)

	res, err := fx.run(fx.actor, "p-child")
	if err != nil {
		t.Fatal(err)
	}
	if !res.Branch || res.Preset != "p-child" || res.WorkflowID != "wf-alpha" {
		t.Fatalf("回执不对: %+v", res)
	}
	fx.waitRunFinal(t, res.RunID)

	// 审计：与普通预设同一个事件与账本来源，说明里多一个 branch=1 标记（在 by= 之前）。
	oks := fx.agentAudits(t, ResultOK)
	if len(oks) != 1 || oks[0].Detail != "preset=p-child workflow=wf-alpha posture=force branch=1 by="+agentLabel {
		t.Fatalf("成功审计应带分支标记: %+v", oks)
	}

	// 频率账本一视同仁：每小时 1 次已用完，别的预设（非分支）也被同一条上限拦下。
	wantAgentCode(t, errOf(fx.run(fx.actor, "p-child2")), "agent_run_hourly")

	// 非分支预设的审计不带标记（同一个 Agent、换一个小时上限放开后运行）。
	fx.limits(AgentRunLimits{})
	res2, err := fx.run(fx.actor, "p-child2")
	if err != nil {
		t.Fatal(err)
	}
	if res2.Branch {
		t.Fatalf("非分支预设的回执不该带分支标记: %+v", res2)
	}
	fx.waitRunFinal(t, res2.RunID)
	for _, a := range fx.agentAudits(t, ResultOK) {
		if strings.Contains(a.Detail, "preset=p-child2") && strings.Contains(a.Detail, "branch=") {
			t.Fatalf("非分支预设的审计不该带分支标记: %q", a.Detail)
		}
	}
	// 回执 / 审计里仍然没有提示词、命令与目录。
	raw, _ := json.Marshal(res)
	for _, banned := range []string{"SECRET-PROMPT", "agent -p", "--resume", "/work/app", "chat-child"} {
		if strings.Contains(string(raw), banned) {
			t.Fatalf("回执里不该出现 %q：%s", banned, raw)
		}
	}
}

func TestAgentBranchPresetFallsBackToTheBoardRuleWhenTheRelationEnds(t *testing.T) {
	fx := newAgentFx(t)
	fx.completeWith("")
	q := fx.qManager(t)
	fx.linkBranch(t, "wf-q-mgr", "wf-alpha", true)
	wantAgentPresets(t, "关系生效时 P 的管理 Agent", fx.presets(t, fx.actor), "p-child2")

	// 分支结束：关系不再生效，预设回到同板规则——P 的管理 Agent 又看得到它，不再带分支标记；Q 的管理 Agent 不再有资格。
	if _, err := fx.env.store.CloseBranch(context.Background(), "wf-alpha", model.BranchCreatedByHuman); err != nil {
		t.Fatal(err)
	}
	views := fx.presets(t, fx.actor)
	wantAgentPresets(t, "关系结束后 P 的管理 Agent", views, "p-child", "p-child2")
	if v := viewOf(views, "p-child"); v.Branch || !v.CanRun {
		t.Fatalf("关系结束后应按普通预设处理: %+v", v)
	}
	wantAgentCode(t, errOf(fx.run(q, "p-child")), "preset_not_found")
	res, err := fx.run(fx.actor, "p-child")
	if err != nil || res.Branch {
		t.Fatalf("P 的管理 Agent 应能按普通预设运行: %v %+v", err, res)
	}
	fx.waitRunFinal(t, res.RunID)

	// 重新打开关系：又回到只有主会话可见。
	fx.linkBranch(t, "wf-q-mgr", "wf-alpha", true)
	wantAgentPresets(t, "重新连上后 P 的管理 Agent", fx.presets(t, fx.actor), "p-child2")
	wantAgentPresets(t, "重新连上后 Q 的管理 Agent", fx.presets(t, q), "p-child", "p-q")
}

func TestAgentBranchLookupFailureFailsClosed(t *testing.T) {
	fx := newAgentFx(t)
	fx.completeWith("")
	other := fx.otherManager(t)
	fx.linkBranch(t, "wf-mgr", "wf-alpha", true)

	// 分支关系读不出来时不能退回「同板规则」：那会让别的管理 Agent 误得到分支的预设。必须报错，什么都不列、不运行。
	if err := fx.env.store.DB().Migrator().DropTable(&model.WorkflowBranch{}); err != nil {
		t.Fatal(err)
	}
	if got, err := fx.env.hub.AgentPresets(context.Background(), other); err == nil {
		t.Fatalf("读不出分支关系应整体报错，不能列出任何预设: %+v", got)
	}
	if _, err := fx.run(other, "p-child"); err == nil {
		t.Fatal("读不出分支关系时不能运行任何预设")
	}
	if fx.sim.runCount() != 0 {
		t.Fatalf("不该到达主机: %d", fx.sim.runCount())
	}
}
