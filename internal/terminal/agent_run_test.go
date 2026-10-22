package terminal

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/teacat99/RelayMesh/v2/internal/model"
	"github.com/teacat99/RelayMesh/v2/internal/store"
)

// 项目管理 Agent 经 MCP 运行已有预设（#179）：资格、频率账本、审计与「不泄露预设内容」。
// MCP 一侧的权限位与「是不是项目管理 Agent」的判定在 internal/mcp 测；这里测的是 Hub 一侧的全部规则。

// ---------- 纯函数 ----------

func TestAgentRunSuffixRoundTripAndLabelCannotForgeTheMarker(t *testing.T) {
	desc := "预设「续聊」的非交互续聊" + agentRunSuffix("项目管理")
	if got := parseAgentRunSuffix(desc); got != "项目管理" {
		t.Fatalf("应取回发起者的名字: %q", got)
	}
	if got := parseAgentRunSuffix("预设「续聊」的非交互续聊"); got != "" {
		t.Fatalf("手动运行没有标记: %q", got)
	}
	if got := parseAgentRunSuffix("预设「续聊」的非交互续聊（自动续跑 1/3）"); got != "" {
		t.Fatalf("自动续跑不是 Agent 发起的: %q", got)
	}
	// 标记只认说明末尾：预设名字里写一个假的，不会被当成发起者（说明总以「的非交互续聊」收尾）。
	forged := "预设「x（项目管理 Agent「冒充」发起）」的非交互续聊"
	if got := parseAgentRunSuffix(forged); got != "" {
		t.Fatalf("预设名字里的假标记不该被认: %q", got)
	}
	// 名字里的括号 / 引号 / 换行被去掉，伪造不了标记边界；空名字写「未命名」；过长被截断。
	evil := "甲」发起）（项目管理 Agent「乙\n丙"
	label := cleanAgentLabel(evil)
	if strings.ContainsAny(label, "「」（）\n\r\t") {
		t.Fatalf("名字里不该留标记字符: %q", label)
	}
	if got := parseAgentRunSuffix("预设「p」的非交互续聊" + agentRunSuffix(evil)); got != label {
		t.Fatalf("带怪字符的名字也应原样取回清洗后的结果: %q vs %q", got, label)
	}
	if got := cleanAgentLabel("  \n "); got != "未命名" {
		t.Fatalf("空名字应写未命名: %q", got)
	}
	long := cleanAgentLabel(strings.Repeat("长", 100))
	if n := len([]rune(long)); n != agentRunLabelRunes+1 || !strings.HasSuffix(long, "…") {
		t.Fatalf("过长的名字应截断并加省略号: %d %q", n, long)
	}
}

func TestAgentLedgerCheckOrder(t *testing.T) {
	now := time.Now()
	l := &agentLedger{
		now: now, lim: AgentRunLimits{MaxConcurrent: 1, MaxPerHour: 1, MaxPerDay: 1, PresetGap: time.Hour},
		dayAll: 5, hourMine: 5, activeMine: 5, lastByPreset: map[string]time.Time{"p": now.Add(-time.Minute)},
	}
	want := func(code string) {
		t.Helper()
		e := l.check("p")
		if code == "" {
			if e != nil {
				t.Fatalf("应放行，得到 %+v", e)
			}
			return
		}
		if e == nil || e.Code != code {
			t.Fatalf("期望 %s，得到 %+v", code, e)
		}
		if e.Message == "" {
			t.Fatalf("拒绝要带一句话的原因: %+v", e)
		}
	}
	// 同时命中四条：先报最要紧的，逐条放宽后依次报下一条。
	want("agent_run_daily_cap")
	l.lim.MaxPerDay = 0
	want("agent_run_preset_gap")
	l.lim.PresetGap = 0
	want("agent_run_concurrency")
	l.lim.MaxConcurrent = 0
	want("agent_run_hourly")
	l.lim.MaxPerHour = 0
	want("")

	// 刚好在上限内放行、到上限就拒绝（边界）。
	l.lim = AgentRunLimits{MaxPerHour: 3}
	l.hourMine = 2
	want("")
	l.hourMine = 3
	want("agent_run_hourly")
	// 间隔只看同一个预设；隔够了就放行。
	l.lim = AgentRunLimits{PresetGap: 10 * time.Minute}
	if e := l.check("别的预设"); e != nil {
		t.Fatalf("间隔不该影响别的预设: %+v", e)
	}
	l.lastByPreset["p"] = now.Add(-11 * time.Minute)
	want("")
	l.lastByPreset["p"] = now.Add(-9 * time.Minute)
	want("agent_run_preset_gap")
}

func TestRoughDuration(t *testing.T) {
	for in, want := range map[time.Duration]string{
		-time.Second: "0 秒", 0: "0 秒", 1500 * time.Millisecond: "2 秒", 59 * time.Second: "59 秒",
		time.Minute: "1 分钟", 5 * time.Minute: "5 分钟", 90 * time.Second: "1 分 30 秒", 61 * time.Second: "1 分 1 秒",
	} {
		if got := roughDuration(in); got != want {
			t.Errorf("%v：得到 %q，期望 %q", in, got, want)
		}
	}
}

func TestPresetOfAuditDetail(t *testing.T) {
	for in, want := range map[string]string{
		"preset=p-child workflow=wf-a posture=force by=甲": "p-child",
		"code=x preset=p2":            "p2",
		"workflow=wf-a posture=force": "",
		"":                            "",
		"mypreset=p3":                 "",
	} {
		if got := presetOfAuditDetail(in); got != want {
			t.Errorf("%q：得到 %q，期望 %q", in, got, want)
		}
	}
}

func TestAgentRunUnavailableWithoutGuard(t *testing.T) {
	var h *Hub
	_, err := h.AgentRunPreset(context.Background(), AgentRunActor{IdentityID: 1, ProjectID: 1}, "p")
	var ae *AgentRunError
	if !errors.As(err, &ae) || ae.Code != "terminal_unavailable" {
		t.Fatalf("没有终端功能应明确报不可用: %v", err)
	}
	if _, err := h.AgentPresets(context.Background(), AgentRunActor{}); err == nil {
		t.Fatal("没有终端功能列出预设也应报错")
	}
	if _, err := h.AgentPresetStatus(context.Background(), AgentRunActor{}, "p"); err == nil {
		t.Fatal("没有终端功能看状态也应报错")
	}
}

// ---------- 夹具 ----------

const (
	agentPromptSecret = "SECRET-PROMPT-%workflow-DO-NOT-LEAK"
	agentLabel        = "项目管理"
)

type agentFx struct {
	*execFixture
	proj, other *model.Project
	actor       AgentRunActor
}

// newAgentFx 造两个项目 P（/code/p）与 Q（/code/q），工作流分布：
//
//	P：wf-mgr（管理 Agent 自己）、wf-alpha、wf-bravo；Q：wf-q；项目外：wf-solo。
//
// 工作流编号不能互相是对方的前缀 / 子串（如 wf-child 与 wf-child2）：创建会话时孤儿工作流编号会被模糊吸附到同目录里相近的既有工作流（D-164），
// 谁先建就变成随机的。
//
// 预设：p-child（A-子会话，wf-alpha，带自定义提示词与模型，force）、p-child2（B-第二个，wf-bravo）、p-own（目标是 wf-mgr）、
// p-q（目标在 Q）、p-solo（目标不在任何项目）、p-nowf（没有目标工作流）、t1（终端预设）。
func newAgentFx(t *testing.T) *agentFx {
	t.Helper()
	f := newExecFixture(t)
	ctx := context.Background()
	st := f.env.store
	mk := func(name, slug, dir string) *model.Project {
		p, err := st.CreateProject(ctx, store.ProjectInput{Name: name, Slug: slug})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := st.AddProjectDir(ctx, p.ID, "h", dir, ""); err != nil {
			t.Fatal(err)
		}
		return p
	}
	fx := &agentFx{execFixture: f, proj: mk("P", "p", "/code/p"), other: mk("Q", "q", "/code/q")}
	for wf, dir := range map[string]string{
		"wf-mgr": "/code/p", "wf-alpha": "/code/p", "wf-bravo": "/code/p", "wf-q": "/code/q", "wf-solo": "/tmp/solo",
	} {
		fx.place(t, wf, dir)
	}
	f.saveLauncher(t, "p-child", "A-子会话", map[string]any{
		"workflow_id": "wf-alpha", "conversation_id": "chat-child", "model": "claude-sonnet-5-5-max", "posture": "force", "prompt": agentPromptSecret,
	})
	f.saveLauncher(t, "p-child2", "B-第二个", map[string]any{"workflow_id": "wf-bravo", "conversation_id": "chat-child2"})
	f.saveLauncher(t, "p-own", "C-自己", map[string]any{"workflow_id": "wf-mgr", "conversation_id": "chat-mgr"})
	f.saveLauncher(t, "p-q", "D-别的项目", map[string]any{"workflow_id": "wf-q", "conversation_id": "chat-q"})
	f.saveLauncher(t, "p-solo", "E-项目外", map[string]any{"workflow_id": "wf-solo", "conversation_id": "chat-solo"})
	f.saveLauncher(t, "p-nowf", "F-无工作流", map[string]any{"conversation_id": "chat-nowf"})
	res := f.env.call("PUT", "/api/v1/terminal/launchers/t1", map[string]any{"name": "G-终端", "host": f.a.code, "dir": "/work/app", "command": "agent"}, f.adm...)
	if res.Status != 200 {
		t.Fatalf("保存终端预设: %d %s", res.Status, res.Raw)
	}
	fx.actor = AgentRunActor{IdentityID: 7, Label: agentLabel, WorkflowID: "wf-mgr", ProjectID: fx.proj.ID}
	return fx
}

func (fx *agentFx) place(t *testing.T, wf, dir string) {
	t.Helper()
	if _, err := fx.env.store.CreateFeedbackSession(context.Background(), store.CreateSessionInput{
		SessionID: "s-" + wf, WorkflowID: wf, EnvHostName: "h", ProjectDirectory: dir, Title: "标题-" + wf, Summary: "s",
	}); err != nil {
		t.Fatal(err)
	}
}

func (fx *agentFx) limits(l AgentRunLimits) { fx.env.hub.guardian.agentLimits = l }

func (fx *agentFx) presets(t *testing.T, a AgentRunActor) []AgentPresetView {
	t.Helper()
	v, err := fx.env.hub.AgentPresets(context.Background(), a)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func (fx *agentFx) run(a AgentRunActor, ref string) (*AgentRunResult, error) {
	return fx.env.hub.AgentRunPreset(context.Background(), a, ref)
}

func wantAgentCode(t *testing.T, err error, code string) *AgentRunError {
	t.Helper()
	var ae *AgentRunError
	if !errors.As(err, &ae) || ae.Code != code {
		t.Fatalf("期望拒绝码 %q，得到 %v", code, err)
	}
	if strings.TrimSpace(ae.Message) == "" {
		t.Fatalf("拒绝要带一句话的原因: %+v", ae)
	}
	return ae
}

// agentAudits 返回 launcher_agent_run 事件的审计记录（按结果过滤；result 为空取全部）。
func (fx *agentFx) agentAudits(t *testing.T, result string) []model.TerminalAuditLog {
	t.Helper()
	var out []model.TerminalAuditLog
	for _, l := range fx.audits(t) {
		if l.Event == EvLauncherAgentRun && (result == "" || l.Result == result) {
			out = append(out, l)
		}
	}
	return out
}

// ledgerRow 直接往审计里写一条「某个 Agent 成功运行了预设」：用来造「刚才运行过」「已经运行了很多次」这类时间上的情形。
func (fx *agentFx) ledgerRow(t *testing.T, actor string, at time.Time, preset, runID string) {
	t.Helper()
	err := fx.env.store.AppendTerminalAudit(context.Background(), &model.TerminalAuditLog{
		Event: EvLauncherAgentRun, Result: ResultOK, Actor: actor, ActorKind: ActorModel, RunID: runID,
		Detail: "preset=" + preset + " workflow=wf-x posture=allowlist", CreatedAt: at,
	})
	if err != nil {
		t.Fatal(err)
	}
}

// ---------- 资格与列表 ----------

func TestAgentPresetsListsOnlyWhatTheManagerMayRun(t *testing.T) {
	fx := newAgentFx(t)
	views := fx.presets(t, fx.actor)
	if len(views) != 2 || views[0].Preset != "p-child" || views[1].Preset != "p-child2" {
		t.Fatalf("只该看到本项目任务板上、不是自己工作流的两条预设（按名字排序）: %+v", views)
	}
	v := views[0]
	if v.Name != "A-子会话" || v.WorkflowID != "wf-alpha" || v.WorkflowTitle != "标题-wf-alpha" || v.Posture != "force" ||
		v.Model != "claude-sonnet-5-5-max" || v.MaxRunMinutes != headlessTimeoutSec/60 || v.Host != "build box" || !v.CanRun || v.WhyNot != "" || v.LastRun != nil {
		t.Fatalf("预设视图不对: %+v", v)
	}

	// 视图里没有提示词、命令、目录与对话编号：这些是人定死的，不经这条通道返回。
	raw, _ := json.Marshal(views)
	for _, banned := range []string{"SECRET-PROMPT", "DO-NOT-LEAK", "agent -p", "--resume", "/work/app", "chat-child", "prompt", "command"} {
		if strings.Contains(string(raw), banned) {
			t.Fatalf("列表里不该出现 %q：%s", banned, raw)
		}
	}

	// 别的项目的管理 Agent 只看得到自己项目的预设；不在任何项目里（ProjectID=0）什么也看不到。
	q := AgentRunActor{IdentityID: 8, Label: "Q 管理", WorkflowID: "wf-q-mgr", ProjectID: fx.other.ID}
	if got := fx.presets(t, q); len(got) != 1 || got[0].Preset != "p-q" {
		t.Fatalf("Q 项目的管理 Agent 只该看到 p-q: %+v", got)
	}
	if got := fx.presets(t, AgentRunActor{IdentityID: 9, WorkflowID: "wf-x"}); len(got) != 0 {
		t.Fatalf("没有项目就没有资格: %+v", got)
	}
	// 自己工作流的预设不在清单里，哪怕它在同一个项目的板上。
	for _, p := range views {
		if p.Preset == "p-own" {
			t.Fatalf("不能列出自己工作流的预设: %+v", p)
		}
	}
}

func TestAgentPresetStatusMatchesByIDOrName(t *testing.T) {
	fx := newAgentFx(t)
	for _, ref := range []string{"p-child", "A-子会话", "  a-子会话 ", "A-子会话"} {
		v, err := fx.env.hub.AgentPresetStatus(context.Background(), fx.actor, ref)
		if err != nil || v.Preset != "p-child" || !v.CanRun {
			t.Fatalf("按 %q 应找到 p-child: %v %+v", ref, err, v)
		}
	}
	for _, ref := range []string{"p-q", "p-solo", "p-nowf", "t1", "nope", "", "  "} {
		_, err := fx.env.hub.AgentPresetStatus(context.Background(), fx.actor, ref)
		wantAgentCode(t, err, "preset_not_found")
	}
	_, err := fx.env.hub.AgentPresetStatus(context.Background(), fx.actor, "p-own")
	wantAgentCode(t, err, "own_workflow")
}

// ---------- 运行 ----------

func TestAgentRunPresetSubmitsExactlyLikeAManualRunAndLeavesTraces(t *testing.T) {
	fx := newAgentFx(t)
	fx.completeWith(launcherResultOut)

	res, err := fx.run(fx.actor, "p-child")
	if err != nil {
		t.Fatal(err)
	}
	if res.RunID == "" || res.Preset != "p-child" || res.Name != "A-子会话" || res.WorkflowID != "wf-alpha" || res.Posture != "force" ||
		res.Model != "claude-sonnet-5-5-max" || res.MaxRunMinutes != headlessTimeoutSec/60 || res.State == "" || res.Message == "" {
		t.Fatalf("回执不对: %+v", res)
	}

	// 到达主机的命令只由预设决定：自定义提示词、固定的对话、模型、force；调用方没有任何办法改它。
	eventually(t, 2*time.Second, "假主机收到运行", func() bool { return fx.sim.runCount() == 1 })
	run := fx.sim.lastRun()
	wantCmd := "agent -p --resume chat-child --approve-mcps --trust --model 'claude-sonnet-5-5-max' --force --workspace '/work/app' --output-format stream-json '" +
		strings.ReplaceAll(strings.ReplaceAll(agentPromptSecret, "%workflow", "wf-alpha"), "'", `'\''`) + "'"
	if run.Cmd != wantCmd || run.Cwd != "/work/app" {
		t.Fatalf("命令应与手动运行完全一致：\n got %s\nwant %s", run.Cmd, wantCmd)
	}

	// 回执不含提示词、命令、目录与对话编号。
	raw, _ := json.Marshal(res)
	for _, banned := range []string{"SECRET-PROMPT", "agent -p", "--resume", "/work/app", "chat-child"} {
		if strings.Contains(string(raw), banned) {
			t.Fatalf("回执里不该出现 %q：%s", banned, raw)
		}
	}

	// 运行记录：说明里带「项目管理 Agent「…」发起」，启动器清单的最近一次运行据此标出发起者。
	rec := fx.waitRunFinal(t, res.RunID)
	if rec.CredentialName != launcherRunCaller || rec.Thread != launcherThreadPrefix+"p-child" || rec.WorkflowID != "wf-alpha" ||
		!strings.Contains(rec.Description, "预设「A-子会话」的非交互续聊") || !strings.HasSuffix(rec.Description, "（项目管理 Agent「"+agentLabel+"」发起）") || rec.State != model.RunSucceeded {
		t.Fatalf("运行记录不对: %+v", rec)
	}
	views, err := fx.env.hub.guardian.launcherRunViews(context.Background(), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if v := views["p-child"]; v.By != agentLabel || v.RunID != res.RunID {
		t.Fatalf("启动器清单应标出发起者: %+v", v)
	}

	// 目标工作流的聊天里有「发起」与「结束」两条系统行，发起行写明是哪个 Agent；都不含提示词与命令。
	var texts []string
	eventually(t, 3*time.Second, "聊天里出现发起与结束两行", func() bool {
		texts = fx.chatTexts("wf-alpha")
		return len(texts) == 2
	})
	wantLine := "项目管理 Agent「" + agentLabel + "」运行了预设「A-子会话」（非交互续聊 · 接着对话 chat-chi… · claude-sonnet-5-5-max · 放开全部命令（--force））" // 对话编号与手动运行一样只显示前 8 位
	if texts[0] != wantLine {
		t.Fatalf("发起行不对：\n got %q\nwant %q", texts[0], wantLine)
	}
	for _, line := range texts {
		for _, banned := range []string{"SECRET-PROMPT", "agent -p", "--resume"} {
			if strings.Contains(line, banned) {
				t.Fatalf("聊天行里不该出现 %q: %q", banned, line)
			}
		}
	}
	if got := fx.chatTexts("wf-mgr"); len(got) != 0 {
		t.Fatalf("管理 Agent 自己的工作流里不该有这些行: %v", got)
	}

	// 审计：一条成功记录，谁、哪个预设、哪个工作流、哪个运行；说明里没有提示词。
	oks := fx.agentAudits(t, ResultOK)
	if len(oks) != 1 {
		t.Fatalf("应有一条成功的审计记录: %+v", oks)
	}
	a := oks[0]
	if a.Actor != "agent:7" || a.ActorKind != ActorModel || a.RunID != res.RunID || a.MachineCode != fx.a.code ||
		a.Detail != "preset=p-child workflow=wf-alpha posture=force by="+agentLabel {
		t.Fatalf("审计记录不对: %+v", a)
	}
	if strings.Contains(a.Detail, "SECRET") {
		t.Fatalf("审计里不该有提示词: %q", a.Detail)
	}

	// 手动运行不受影响：没有发起者标记，也不进 Agent 的账本。
	if _, err := fx.runLauncher("p-child2"); err != nil {
		t.Fatal(err)
	}
	views, _ = fx.env.hub.guardian.launcherRunViews(context.Background(), time.Now())
	if v := views["p-child2"]; v.By != "" {
		t.Fatalf("手动运行不该带发起者: %+v", v)
	}
	if got := fx.agentAudits(t, ResultOK); len(got) != 1 {
		t.Fatalf("手动运行不该进 Agent 账本: %+v", got)
	}

	// 现状里带上最近一次运行与它的回复（回复来自子会话，调用方当数据读）。
	st, err := fx.env.hub.AgentPresetStatus(context.Background(), fx.actor, "p-child")
	if err != nil || st.LastRun == nil || st.LastRun.RunID != res.RunID || st.LastRun.By != agentLabel || st.LastRun.State != model.RunSucceeded ||
		st.LastRun.Reply != "已恢复现场：收件箱为空。" {
		t.Fatalf("现状应带最近一次运行: %v %+v", err, st)
	}
}

func TestAgentRunPresetRefusals(t *testing.T) {
	fx := newAgentFx(t)
	fx.completeWith("")

	// 找不到、别的项目的、项目外的、没有目标工作流的、终端预设：对调用方来说它们不存在，且说法完全一样（不泄露是否存在）。
	unknown := wantAgentCode(t, errOf(fx.run(fx.actor, "nope")), "preset_not_found")
	for _, ref := range []string{"p-q", "p-solo", "p-nowf", "t1", "", "D-别的项目"} {
		e := wantAgentCode(t, errOf(fx.run(fx.actor, ref)), "preset_not_found")
		if e.Message != unknown.Message {
			t.Fatalf("%q：别的项目的预设与不存在的预设应是同一句话（不泄露）：%q vs %q", ref, e.Message, unknown.Message)
		}
	}
	// 自己工作流的预设：明确告诉原因（避免自己唤醒自己）。
	own := wantAgentCode(t, errOf(fx.run(fx.actor, "p-own")), "own_workflow")
	if !strings.Contains(own.Message, "自己") {
		t.Fatalf("应说明不能运行自己的: %q", own.Message)
	}
	// 没有项目的调用方：一律找不到。
	wantAgentCode(t, errOf(fx.run(AgentRunActor{IdentityID: 9, WorkflowID: "wf-x"}, "p-child")), "preset_not_found")

	if fx.sim.runCount() != 0 {
		t.Fatalf("被拒绝的请求不该到达主机: %d", fx.sim.runCount())
	}
	if oks := fx.agentAudits(t, ResultOK); len(oks) != 0 {
		t.Fatalf("被拒绝的请求不该进成功账本: %+v", oks)
	}
	denies := fx.agentAudits(t, ResultDeny)
	if len(denies) == 0 || len(denies) > auditRateBurst+1 {
		t.Fatalf("被拒绝的尝试应有审计且按调用方限流（不刷屏）：%d 条", len(denies))
	}
	for _, d := range denies {
		if (d.Actor != "agent:7" && d.Actor != "agent:9") || !strings.Contains(d.Detail, "code=") {
			t.Fatalf("拒绝记录不对: %+v", d)
		}
	}
}

func errOf(_ *AgentRunResult, err error) error { return err }

func TestAgentRunPresetKeepsEveryExecutionGate(t *testing.T) {
	t.Run("主机没开模型访问：被闸门拒绝，不占账本", func(t *testing.T) {
		fx := newAgentFx(t)
		fx.completeWith("")
		fx.limits(AgentRunLimits{MaxPerHour: 1}) // 只剩每小时 1 次：被拒绝的尝试若占了名额，后面就跑不了
		fx.setModel(t, map[string]any{"allow_model": false})
		_, err := fx.run(fx.actor, "p-child")
		var ae *AgentRunError
		if !errors.As(err, &ae) || ae.Message == "" {
			t.Fatalf("应被执行闸门拒绝并换成面向智能体的错误: %T %v", err, err)
		}
		if fx.sim.runCount() != 0 || len(fx.agentAudits(t, ResultOK)) != 0 {
			t.Fatalf("被拒绝的运行不该到达主机也不该进账本")
		}
		if len(fx.agentAudits(t, "")) == 0 {
			t.Fatal("被拒绝的尝试应留审计")
		}
		fx.setModel(t, map[string]any{"allow_model": true})
		if _, err := fx.run(fx.actor, "p-child"); err != nil {
			t.Fatalf("放开之后第一次真正的运行应成功（前面被拒绝的尝试不占名额）: %v", err)
		}
	})
	t.Run("需批准模式：停在待批准，并告诉调用方", func(t *testing.T) {
		fx := newAgentFx(t)
		fx.setModel(t, map[string]any{"allow_model": true, "exec_mode": "approve"})
		res, err := fx.run(fx.actor, "p-child")
		if err != nil {
			t.Fatal(err)
		}
		if res.State != model.RunAwaitingApproval || fx.sim.runCount() != 0 {
			t.Fatalf("应停在待批准且不到达主机: %+v runs=%d", res, fx.sim.runCount())
		}
		if !strings.Contains(res.Message, "需批准") {
			t.Fatalf("回执应说明要用户批准: %q", res.Message)
		}
		if rec := fx.dbRun(t, res.RunID); rec.State != model.RunAwaitingApproval {
			t.Fatalf("运行记录应是待批准: %+v", rec)
		}
		// 待批准的运行是进行中的：同一个预设不能再运行（沿用单飞），列表里也不可运行。
		_, err = fx.run(fx.actor, "p-child")
		wantAgentCode(t, err, "run_active")
		for _, v := range fx.presets(t, fx.actor) {
			if v.Preset == "p-child" && (v.CanRun || v.WhyCode != "run_active" || v.LastRun == nil || !v.LastRun.Active) {
				t.Fatalf("列表里应标明不可运行与原因: %+v", v)
			}
		}
	})
	t.Run("上一次运行还没结束：单飞", func(t *testing.T) {
		fx := newAgentFx(t) // 没有 completeWith：假主机收到运行后一直不结束
		first, err := fx.run(fx.actor, "p-child")
		if err != nil {
			t.Fatal(err)
		}
		eventually(t, 2*time.Second, "假主机收到运行", func() bool { return fx.sim.runCount() == 1 })
		e := wantAgentCode(t, errOf(fx.run(fx.actor, "p-child")), "run_active")
		if !strings.Contains(e.Message, first.RunID) {
			t.Fatalf("应带上还在跑的运行编号: %q", e.Message)
		}
		if fx.sim.runCount() != 1 {
			t.Fatalf("第二次不该到达主机: %d", fx.sim.runCount())
		}
	})
}

// ---------- 频率账本 ----------

func TestAgentRunLedgerPresetGapIsPerPresetAndAcrossAgents(t *testing.T) {
	fx := newAgentFx(t)
	fx.completeWith("")
	fx.limits(AgentRunLimits{PresetGap: 10 * time.Minute})

	if _, err := fx.run(fx.actor, "p-child"); err != nil {
		t.Fatal(err)
	}
	fx.waitRunFinal(t, fx.agentAudits(t, ResultOK)[0].RunID)

	e := wantAgentCode(t, errOf(fx.run(fx.actor, "p-child")), "agent_run_preset_gap")
	if !strings.Contains(e.Message, "至少隔 10 分钟") {
		t.Fatalf("应写明最短间隔: %q", e.Message)
	}
	// 列表与现状也如实标出（不是等点了才知道）。
	for _, v := range fx.presets(t, fx.actor) {
		switch v.Preset {
		case "p-child":
			if v.CanRun || v.WhyCode != "agent_run_preset_gap" {
				t.Fatalf("刚运行过的预设应标为不可运行: %+v", v)
			}
		case "p-child2":
			if !v.CanRun {
				t.Fatalf("别的预设不受影响: %+v", v)
			}
		}
	}
	// 间隔只管同一个预设：另一个预设立刻可以运行。
	if _, err := fx.run(fx.actor, "p-child2"); err != nil {
		t.Fatalf("别的预设不该被挡: %v", err)
	}
	// 间隔不分是哪个 Agent 运行的：另一个管理 Agent 紧接着运行同一个预设也被挡。
	other := AgentRunActor{IdentityID: 11, Label: "另一个", WorkflowID: "wf-mgr", ProjectID: fx.proj.ID}
	wantAgentCode(t, errOf(fx.run(other, "p-child")), "agent_run_preset_gap")
	// 失败的拒绝没有进成功账本。
	if got := len(fx.agentAudits(t, ResultOK)); got != 2 {
		t.Fatalf("成功账本应只有 2 条: %d", got)
	}
}

func TestAgentRunLedgerHourlyAndDailyAndConcurrency(t *testing.T) {
	now := time.Now()

	t.Run("每小时次数：只数调用方自己的、一小时内的", func(t *testing.T) {
		fx := newAgentFx(t)
		fx.completeWith("")
		fx.limits(AgentRunLimits{MaxPerHour: 3})
		for i := 0; i < 2; i++ {
			fx.ledgerRow(t, "agent:7", now.Add(-time.Duration(20+i)*time.Minute), fmt.Sprintf("zz%d", i), "")
		}
		fx.ledgerRow(t, "agent:7", now.Add(-90*time.Minute), "zz-old", "")   // 一小时之外：不算
		fx.ledgerRow(t, "agent:99", now.Add(-5*time.Minute), "zz-other", "") // 别的 Agent：不算
		if _, err := fx.run(fx.actor, "p-child"); err != nil {
			t.Fatalf("两次在窗口内、加上这次是第三次：应放行: %v", err)
		}
		e := wantAgentCode(t, errOf(fx.run(fx.actor, "p-child2")), "agent_run_hourly")
		if !strings.Contains(e.Message, "每小时最多 3 次") {
			t.Fatalf("应写明上限: %q", e.Message)
		}
	})
	t.Run("每天合计：所有 Agent 加在一起，是循环的熔断", func(t *testing.T) {
		fx := newAgentFx(t)
		fx.completeWith("")
		fx.limits(AgentRunLimits{MaxPerDay: 4})
		for i := 0; i < 4; i++ { // 别的 Agent 的记录，早于一小时、在 24 小时之内
			fx.ledgerRow(t, "agent:99", now.Add(-time.Duration(2+i)*time.Hour), fmt.Sprintf("zz%d", i), "")
		}
		fx.ledgerRow(t, "agent:99", now.Add(-25*time.Hour), "zz-old", "") // 24 小时之外：不算
		e := wantAgentCode(t, errOf(fx.run(fx.actor, "p-child")), "agent_run_daily_cap")
		if !strings.Contains(e.Message, "每天 4 次") || !strings.Contains(e.Message, "用户") {
			t.Fatalf("应写明上限并让调用方去找用户: %q", e.Message)
		}
		if fx.sim.runCount() != 0 {
			t.Fatal("被熔断的请求不该到达主机")
		}
		// 24 小时之外的不算：少一条就放行。
		fx2 := newAgentFx(t)
		fx2.completeWith("")
		fx2.limits(AgentRunLimits{MaxPerDay: 4})
		for i := 0; i < 3; i++ {
			fx2.ledgerRow(t, "agent:99", now.Add(-time.Duration(2+i)*time.Hour), fmt.Sprintf("zz%d", i), "")
		}
		fx2.ledgerRow(t, "agent:99", now.Add(-25*time.Hour), "zz-old", "")
		if _, err := fx2.run(fx2.actor, "p-child"); err != nil {
			t.Fatalf("三条在窗口内：应放行: %v", err)
		}
	})
	t.Run("同时在跑：数调用方自己发起、此刻还在进行的运行", func(t *testing.T) {
		fx := newAgentFx(t)
		fx.completeWith("")
		fx.limits(AgentRunLimits{MaxConcurrent: 2})
		act1 := fx.seedRun(t, launcherRunCaller, "launcher-x1", model.RunRunning, now.Add(-5*time.Minute), nil)
		act2 := fx.seedRun(t, launcherRunCaller, "launcher-x2", model.RunRunning, now.Add(-4*time.Minute), nil)
		ended := now.Add(-time.Minute)
		done := fx.seedRun(t, launcherRunCaller, "launcher-x3", model.RunSucceeded, now.Add(-10*time.Minute), &ended)
		fx.ledgerRow(t, "agent:7", now.Add(-3*time.Hour), "zz1", act1)
		fx.ledgerRow(t, "agent:7", now.Add(-3*time.Hour), "zz2", act2)
		fx.ledgerRow(t, "agent:7", now.Add(-3*time.Hour), "zz3", done)  // 已结束：不算
		fx.ledgerRow(t, "agent:99", now.Add(-3*time.Hour), "zz4", act1) // 别的 Agent 的：不算在调用方头上
		e := wantAgentCode(t, errOf(fx.run(fx.actor, "p-child")), "agent_run_concurrency")
		if !strings.Contains(e.Message, "2 条还在进行") {
			t.Fatalf("应写明在跑的数量: %q", e.Message)
		}
		// 另一个管理 Agent 不受这个 Agent 在跑的影响。
		other := AgentRunActor{IdentityID: 12, Label: "另一个", WorkflowID: "wf-mgr", ProjectID: fx.proj.ID}
		if _, err := fx.run(other, "p-child"); err != nil {
			t.Fatalf("同时在跑的数量按 Agent 分: %v", err)
		}
	})
	t.Run("重启不清零：账本来自审计记录，新建的 Hub 视图读到同样的次数", func(t *testing.T) {
		fx := newAgentFx(t)
		fx.completeWith("")
		fx.limits(AgentRunLimits{MaxPerHour: 1})
		if _, err := fx.run(fx.actor, "p-child"); err != nil {
			t.Fatal(err)
		}
		// 账本没有任何内存计数：丢掉所有进程内状态（锁以外）后重新读，结果一样。
		fx.env.hub.guardian.agentLimits = AgentRunLimits{MaxPerHour: 1}
		led, err := fx.env.hub.guardian.loadAgentLedger(context.Background(), fx.actor, time.Now())
		if err != nil || led.hourMine != 1 || led.dayAll != 1 {
			t.Fatalf("应从审计里读到 1 次: %v %+v", err, led)
		}
		wantAgentCode(t, errOf(fx.run(fx.actor, "p-child2")), "agent_run_hourly")
	})
}

// 一大批被拒绝的尝试不能把成功的记录挤出账本窗口：否则限额恰好在被刷的时候失效。
func TestAgentRunLedgerIgnoresFloodOfDeniedAttempts(t *testing.T) {
	fx := newAgentFx(t)
	fx.completeWith("")
	fx.limits(AgentRunLimits{MaxPerHour: 1})
	now := time.Now()
	fx.ledgerRow(t, "agent:7", now.Add(-10*time.Minute), "zz-ok", "")
	for i := 0; i < 600; i++ {
		err := fx.env.store.AppendTerminalAudit(context.Background(), &model.TerminalAuditLog{
			Event: EvLauncherAgentRun, Result: ResultDeny, Actor: "agent:7", ActorKind: ActorModel,
			Detail: fmt.Sprintf("preset=p-child code=x%d", i), CreatedAt: now.Add(-time.Duration(i) * time.Second),
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	wantAgentCode(t, errOf(fx.run(fx.actor, "p-child")), "agent_run_hourly")
}

// 检查与提交在同一把锁里：多个调用同时来，放行的数量不会超过上限。
func TestAgentRunLedgerIsAtomicUnderConcurrency(t *testing.T) {
	fx := newAgentFx(t)
	fx.completeWith("")
	for i := 1; i <= 6; i++ {
		wf := fmt.Sprintf("wf-m%d", i)
		fx.place(t, wf, "/code/p")
		fx.saveLauncher(t, fmt.Sprintf("p-m%d", i), fmt.Sprintf("M-%d", i), map[string]any{"workflow_id": wf, "conversation_id": fmt.Sprintf("chat-m%d", i)})
	}
	fx.limits(AgentRunLimits{MaxPerHour: 2})

	var wg sync.WaitGroup
	results := make([]error, 6)
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, results[i] = fx.run(fx.actor, fmt.Sprintf("p-m%d", i+1))
		}(i)
	}
	wg.Wait()
	ok := 0
	for _, err := range results {
		if err == nil {
			ok++
			continue
		}
		wantAgentCode(t, err, "agent_run_hourly")
	}
	if ok != 2 {
		t.Fatalf("每小时上限 2：同时来 6 个只能放行 2 个，实际 %d（%v）", ok, results)
	}
	if got := len(fx.agentAudits(t, ResultOK)); got != 2 {
		t.Fatalf("成功账本应正好 2 条: %d", got)
	}
}

func TestAgentRunDefaultLimitsAreTheDesignedOnes(t *testing.T) {
	fx := newAgentFx(t)
	if got := fx.env.hub.guardian.agentRunLimits(); got != (AgentRunLimits{MaxConcurrent: 3, MaxPerHour: 10, MaxPerDay: 30, PresetGap: 5 * time.Minute}) {
		t.Fatalf("默认护栏应是设计稿 8.4 的值: %+v", got)
	}
}
