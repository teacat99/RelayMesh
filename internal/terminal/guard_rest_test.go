package terminal

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/teacat99/RelayMesh/v2/internal/store"
)

// 会话守护的界面接口：GET /guard 登录即可读；POST /guard/:id/wake 要求刚刚解锁、写审计（不含提示词与命令）。

const (
	guardListPath = "/api/v1/terminal/guard"
	wakePath      = "/api/v1/terminal/guard/p1/wake"
)

func loginHdr() []string { return []string{"Authorization", humanAuth(testUser)} }

func (g *guardEnv) wake(id string, body map[string]string) apiResult {
	g.t.Helper()
	if body == nil {
		body = map[string]string{}
	}
	return g.call("POST", "/api/v1/terminal/guard/"+id+"/wake", body, g.admin()...)
}

func planOf(t *testing.T, res apiResult) map[string]any {
	t.Helper()
	if res.Status != http.StatusOK {
		t.Fatalf("应返回计划: %d %s", res.Status, res.Raw)
	}
	plan, _ := res.Body["plan"].(map[string]any)
	if plan == nil {
		t.Fatalf("响应里没有 plan: %s", res.Raw)
	}
	return plan
}

func (g *guardEnv) listGuard() []map[string]any {
	g.t.Helper()
	res := g.call("GET", guardListPath, nil, loginHdr()...)
	if res.Status != http.StatusOK {
		g.t.Fatalf("GET /guard: %d %s", res.Status, res.Raw)
	}
	var out []map[string]any
	for _, it := range res.Body["items"].([]any) {
		out = append(out, it.(map[string]any))
	}
	return out
}

func TestGuardListNeedsLoginAndOnlyListsGuardedPresets(t *testing.T) {
	g := newGuardEnv(t)
	if res := g.call("GET", guardListPath, nil); res.Status != http.StatusUnauthorized && res.Status != http.StatusForbidden {
		t.Fatalf("未登录不应能读: %d %s", res.Status, res.Raw)
	}
	if items := g.listGuard(); len(items) != 0 {
		t.Fatalf("没有预设时应是空数组: %v", items)
	}

	g.savePreset("p1", guardPresetName, map[string]any{"enabled": true, "workflow_id": "wf-a", "silence_minutes": 45})
	g.savePreset("p2", "Plain", nil)
	g.savePreset("p3", "Paused", map[string]any{"enabled": false, "workflow_id": "wf-z"})
	g.online(g.liveSession(sidOf('a'), g.clock.Now()))

	res := g.call("GET", guardListPath, nil, loginHdr()...)
	if res.Body["wake_max"] != float64(3) || res.Body["wake_window_hours"] != float64(24) || res.Body["progress_minutes"] != float64(10) {
		t.Fatalf("应带唤醒次数上限、窗口与观察时长: %s", res.Raw)
	}
	items := g.listGuard()
	if len(items) != 2 || items[0]["preset_id"] != "p1" || items[1]["preset_id"] != "p3" {
		t.Fatalf("只列出配置了守护的预设（按预设顺序）: %v", items)
	}
	first := items[0]
	if first["enabled"] != true || first["workflow_id"] != "wf-a" || first["silence_minutes"] != float64(45) ||
		first["wake_mode"] != GuardWakeType || first["wakes_24h"] != float64(0) || first["chat_known"] != false {
		t.Fatalf("p1 的概况不对: %v", first)
	}
	if items[1]["enabled"] != false || items[1]["silence_minutes"] != float64(store.DefaultGuardSilenceMinutes) {
		t.Fatalf("没打开的守护也列出来，停摆时长取默认: %v", items[1])
	}
	if first["last_wake"] != nil {
		t.Fatalf("没唤醒过不该有 last_wake: %v", first)
	}
}

func TestGuardWakeNeedsUnlockAndAuditsWithoutContent(t *testing.T) {
	g := newGuardEnv(t)
	g.savePreset("p1", guardPresetName, map[string]any{"enabled": true, "workflow_id": "wf-a", "wake_prompt": "密语不进审计 %n"})
	g.online(g.liveSession(sidOf('a'), g.clock.Now()))

	if res := g.call("POST", wakePath, map[string]string{}); res.Status != http.StatusUnauthorized && res.Status != http.StatusForbidden {
		t.Fatalf("未登录不应能唤醒: %d %s", res.Status, res.Raw)
	}
	if res := g.call("POST", wakePath, map[string]string{}, loginHdr()...); res.Status != http.StatusForbidden || res.str("code") != "unlock_required" {
		t.Fatalf("只有登录、没有解锁不应能唤醒（它等于替人敲键盘）: %d %s", res.Status, res.Raw)
	}
	if n := len(g.guardState().Presets); n != 0 {
		t.Fatalf("被拒绝的请求不应登记唤醒: %d", n)
	}

	plan := planOf(t, g.wake("p1", nil))
	if !strings.Contains(plan["prompt"].(string), "密语不进审计") {
		t.Fatalf("自定义正文应进计划: %v", plan["prompt"])
	}
	logs := g.auditEvents(EvGuardWake)
	if len(logs) != 1 || logs[0].Detail != "preset=p1 mode=type n=1" || logs[0].MachineCode != testLauncherHost {
		t.Fatalf("应有 1 条审计（只记预设、方式与次数）: %+v", logs)
	}
	all, _ := g.store.ListTerminalAudit(context.Background(), 500, 0)
	for _, l := range all {
		if strings.Contains(l.Detail, "密语") || strings.Contains(l.Detail, "agent") {
			t.Fatalf("审计里不能出现提示词或命令: %+v", l)
		}
	}
}

func TestGuardWakeTypesIntoALiveSession(t *testing.T) {
	g := newGuardEnv(t)
	g.savePreset("p1", guardPresetName, map[string]any{"enabled": true, "workflow_id": "wf-a"})
	g.online(g.liveSession(sidOf('a'), g.clock.Now()))
	g.clock.Advance(time.Minute)

	plan := planOf(t, g.wake("p1", nil))
	if plan["mode"] != GuardWakeType || plan["session_sid"] != sidOf('a') || plan["name"] != guardPresetName || plan["host"] != testLauncherHost {
		t.Fatalf("会话活着应输入到它里面: %v", plan)
	}
	if plan["command"] != nil || plan["dir"] != nil {
		t.Fatalf("输入方式不该带启动命令: %v", plan)
	}
	prompt := plan["prompt"].(string)
	if !strings.HasPrefix(prompt, "【系统唤醒 1/3 · 原因：") || !strings.Contains(prompt, "RelayMesh") {
		t.Fatalf("提示词应以固定开头起头并带默认正文: %q", prompt)
	}
	if strings.ContainsAny(prompt, store.GuardPromptUnsafe) {
		t.Fatalf("提示词里不能有 shell 特殊字符: %q", prompt)
	}
	if plan["n"] != float64(1) || plan["max"] != float64(3) || plan["over_budget"] != false {
		t.Fatalf("次数不对: %v", plan)
	}

	st := g.guardState().Presets["p1"]
	if st == nil || len(st.Wakes) != 1 || st.Wakes[0].Mode != GuardWakeType {
		t.Fatalf("应登记一次唤醒: %+v", st)
	}
	items := g.listGuard()
	lw, _ := items[0]["last_wake"].(map[string]any)
	if items[0]["wakes_24h"] != float64(1) || lw == nil || lw["mode"] != GuardWakeType || lw["progress"] != "pending" {
		t.Fatalf("列表应反映这次唤醒（还在观察）: %v", items[0])
	}
}

func TestGuardWakeRelaunchesAnEndedSessionAndResumesTheKnownChat(t *testing.T) {
	g := newGuardEnv(t)
	g.savePreset("p1", guardPresetName, map[string]any{
		"enabled": true, "workflow_id": "wf-a", "resume_command": "agent --resume {chat_id} --force", "wake_prompt": "接着做 %reason 第 %n 次，上限 %max",
	})
	g.online()
	code := 137
	g.index(sidOf('a'), guardPresetName, g.clock.Now().Add(-time.Hour), true, &code)
	g.stalledByError("wf-a", "chat-9", g.clock.Now())
	g.clock.Advance(time.Minute)

	items := g.listGuard()
	if items[0]["wake_mode"] != GuardWakeRelaunch || items[0]["chat_known"] != true {
		t.Fatalf("会话已结束且知道对话编号：应重新拉起并接着原来的对话: %v", items[0])
	}
	plan := planOf(t, g.wake("p1", nil))
	if plan["mode"] != GuardWakeRelaunch || plan["command"] != "agent --resume chat-9 --force" || plan["resume"] != true ||
		plan["append_prompt"] != true || plan["dir"] != "/home/user/RelayMesh" || plan["session_sid"] != nil {
		t.Fatalf("重新拉起的计划不对: %v", plan)
	}
	if want := "【系统唤醒 1/3 · 原因：会话意外结束】接着做 会话意外结束 第 1 次，上限 3"; plan["prompt"] != want {
		t.Fatalf("提示词 = %q，期望 %q", plan["prompt"], want)
	}
	if w, _ := plan["warnings"].([]any); len(w) != 0 {
		t.Fatalf("接着原来的对话不该有警告: %v", w)
	}
}

func TestGuardWakeFallsBackToAFreshChatWhenTheChatIsUnknown(t *testing.T) {
	g := newGuardEnv(t)
	g.savePreset("p1", guardPresetName, map[string]any{"enabled": true, "workflow_id": "wf-a", "resume_command": "agent --resume {chat_id}"})
	g.online()
	code := 1
	g.index(sidOf('a'), guardPresetName, g.clock.Now().Add(-time.Hour), true, &code)
	g.clock.Advance(time.Minute)

	plan := planOf(t, g.wake("p1", nil))
	warns, _ := plan["warnings"].([]any)
	if plan["command"] != "agent" || plan["resume"] == true || len(warns) != 1 || !strings.Contains(warns[0].(string), "对话编号") {
		t.Fatalf("不知道对话编号时应退回预设命令（新开对话）并提醒: %v", plan)
	}
}

func TestGuardWakeWarnsWhenThePromptCannotBeAppended(t *testing.T) {
	g := newGuardEnv(t)
	g.savePreset("p1", guardPresetName, map[string]any{"enabled": true, "workflow_id": "wf-a", "resume_command": "agent --resume x | tee run.log"})
	g.online()
	code := 1
	g.index(sidOf('a'), guardPresetName, g.clock.Now().Add(-time.Hour), true, &code)
	g.clock.Advance(time.Minute)

	plan := planOf(t, g.wake("p1", nil))
	warns, _ := plan["warnings"].([]any)
	if plan["append_prompt"] == true || len(warns) != 1 || !strings.Contains(warns[0].(string), "没法安全地把提示词追加进去") {
		t.Fatalf("带管道的命令不能追加提示词，应提醒: %v", plan)
	}
}

func TestGuardWakeCountsRollingDayAndOnlyWarnsWhenOverBudget(t *testing.T) {
	g := newGuardEnv(t)
	g.savePreset("p1", guardPresetName, map[string]any{"enabled": true, "workflow_id": "wf-a"})
	g.online(g.liveSession(sidOf('a'), g.clock.Now()))

	for i := 1; i <= 4; i++ {
		g.clock.Advance(time.Minute)
		plan := planOf(t, g.wake("p1", nil))
		if plan["n"] != float64(i) || plan["over_budget"] != (i > guardWakeMax) {
			t.Fatalf("第 %d 次: n=%v over_budget=%v", i, plan["n"], plan["over_budget"])
		}
		if !strings.HasPrefix(plan["prompt"].(string), "【系统唤醒 ") {
			t.Fatalf("超限也只是提醒，计划照常生成: %v", plan)
		}
	}
	if items := g.listGuard(); items[0]["wakes_24h"] != float64(4) {
		t.Fatalf("列表应显示 24 小时内唤醒了 4 次: %v", items[0])
	}

	// 滚动窗口：一天之后，次数重新算起。
	g.clock.Advance(guardWakeWindow + time.Minute)
	if plan := planOf(t, g.wake("p1", nil)); plan["n"] != float64(1) || plan["over_budget"] != false {
		t.Fatalf("窗口滑过之后应重新计数: %v", plan)
	}
}

func TestGuardWakeRefusals(t *testing.T) {
	g := newGuardEnv(t)
	g.savePreset("p1", guardPresetName, map[string]any{"enabled": true, "workflow_id": "wf-a"})
	g.savePreset("bare", "Bare", nil)
	g.online()
	code := 1
	g.index(sidOf('a'), guardPresetName, g.clock.Now().Add(-time.Hour), true, &code)
	g.clock.Advance(time.Minute)

	cases := []struct {
		name   string
		id     string
		body   map[string]string
		status int
		code   string
	}{
		{"预设不存在", "nope", nil, http.StatusNotFound, "launcher_not_found"},
		{"预设没有配置守护", "bare", nil, http.StatusConflict, "guard_not_set"},
		{"不认识的方式", "p1", map[string]string{"mode": "teleport"}, http.StatusBadRequest, "bad_mode"},
		{"没有存活的会话却要求输入", "p1", map[string]string{"mode": "type"}, http.StatusConflict, "wake_unavailable"},
	}
	for _, tc := range cases {
		res := g.wake(tc.id, tc.body)
		if res.Status != tc.status || res.str("code") != tc.code {
			t.Errorf("%s: 期望 %d %s，得到 %d %s", tc.name, tc.status, tc.code, res.Status, res.Raw)
		}
	}

	g.offline()
	if res := g.wake("p1", nil); res.Status != http.StatusConflict || res.str("code") != "host_offline" {
		t.Errorf("主机离线: %d %s", res.Status, res.Raw)
	}
	if st := g.guardState().Presets["p1"]; st != nil && len(st.Wakes) != 0 {
		t.Errorf("被拒绝的唤醒不该登记: %+v", st)
	}
	if n := len(g.auditEvents(EvGuardWake)); n != 0 {
		t.Errorf("被拒绝的唤醒不该写审计: %d", n)
	}
}

func TestGuardWakeRefusesToRelaunchWithoutAnyCommand(t *testing.T) {
	g := newGuardEnv(t)
	body := map[string]any{"name": "Empty", "host": testLauncherHost, "dir": "/root", "command": "", "guard": map[string]any{"enabled": true}}
	if res := g.call("PUT", "/api/v1/terminal/launchers/p1", body, g.admin()...); res.Status != 200 {
		t.Fatalf("保存: %d %s", res.Status, res.Raw)
	}
	g.online()
	g.clock.Advance(time.Minute)
	res := g.wake("p1", nil)
	if res.Status != http.StatusConflict || res.str("code") != "no_command" {
		t.Fatalf("没有任何命令时不能重新拉起: %d %s", res.Status, res.Raw)
	}
	if st := g.guardState().Presets["p1"]; st != nil && len(st.Wakes) != 0 {
		t.Fatalf("失败的唤醒不该登记: %+v", st)
	}
}

func TestGuardListReportsWakeEffectAndNoProgressStreak(t *testing.T) {
	g := newGuardEnv(t)
	g.savePreset("p1", guardPresetName, map[string]any{"enabled": true, "workflow_id": "wf-a"})
	g.online(g.liveSession(sidOf('a'), g.clock.Now()))
	g.stalledByError("wf-a", "chat-a", g.clock.Now())

	for i := 0; i < 2; i++ {
		g.clock.Advance(time.Hour)
		planOf(t, g.wake("p1", nil))
		g.clock.Advance(guardProgressWindow + time.Minute)
		g.tick() // 补上「这次唤醒没有任何效果」的结论
	}
	item := g.listGuard()[0]
	lw, _ := item["last_wake"].(map[string]any)
	if item["no_progress_streak"] != float64(2) || lw == nil || lw["progress"] != "none" {
		t.Fatalf("连续两次唤醒都没有动静：%v", item)
	}

	// 观察窗口还没过完的这一次还没有结论：不计入「没效果」，但前面已有结论的两次照常数。
	g.clock.Advance(time.Hour)
	planOf(t, g.wake("p1", nil))
	item = g.listGuard()[0]
	lw, _ = item["last_wake"].(map[string]any)
	if lw["progress"] != "pending" || item["no_progress_streak"] != float64(2) {
		t.Fatalf("还在观察的这一次不该计入，前两次照常数：%v", item)
	}

	// 一旦有动静，就是有效。
	g.store.RecordAgentEvent(context.Background(), "wf-a", store.AgentEvent{Kind: store.AgentEventTool}, g.clock.Now().Add(time.Minute))
	g.clock.Advance(2 * time.Minute)
	item = g.listGuard()[0]
	lw, _ = item["last_wake"].(map[string]any)
	if lw["progress"] != "progress" || item["no_progress_streak"] != float64(0) {
		t.Fatalf("有动静应显示有效：%v", item)
	}
}

func TestLauncherSaveCarriesGuardAndValidatesIt(t *testing.T) {
	g := newGuardEnv(t)
	good := map[string]any{
		"enabled": true, "workflow_id": " relaymesh-continuation ", "silence_minutes": 30,
		"resume_command": "agent --resume {chat_id}", "wake_prompt": "会话曾中断   请继续 %n/%max",
	}
	res := g.savePreset("p1", guardPresetName, good)
	prefs, _ := res.Body["prefs"].(map[string]any)
	launchers, _ := prefs["launchers"].([]any)
	if len(launchers) != 1 {
		t.Fatalf("应保存 1 个预设: %s", res.Raw)
	}
	saved, _ := launchers[0].(map[string]any)["guard"].(map[string]any)
	if saved["enabled"] != true || saved["workflow_id"] != "relaymesh-continuation" || saved["silence_minutes"] != float64(30) ||
		saved["resume_command"] != "agent --resume {chat_id}" || saved["wake_prompt"] != "会话曾中断 请继续 %n/%max" {
		t.Fatalf("守护配置应被规整后保存: %v", saved)
	}

	lim, _ := prefs["limits"].(map[string]any)
	if lim["guard_silence_min"] != float64(store.MinGuardSilenceMinutes) || lim["guard_silence_max"] != float64(store.MaxGuardSilenceMinutes) ||
		lim["guard_silence_default"] != float64(store.DefaultGuardSilenceMinutes) || lim["guard_workflow_max"] != float64(store.MaxGuardWorkflowRunes) ||
		lim["guard_prompt_max"] != float64(store.MaxGuardPromptRunes) || lim["guard_prompt_unsafe"] != store.GuardPromptUnsafe || lim["guard_wake_max"] != float64(guardWakeMax) {
		t.Fatalf("状态应带守护的取值范围: %v", lim)
	}

	bad := []struct {
		name  string
		guard map[string]any
		want  string
	}{
		{"停摆时长太短", map[string]any{"enabled": true, "silence_minutes": 1}, "停摆判定时长"},
		{"工作流编号里有空格", map[string]any{"enabled": true, "workflow_id": "a b"}, "工作流编号"},
		{"提示词里有 shell 特殊字符", map[string]any{"enabled": true, "wake_prompt": "继续 $(reboot)"}, "唤醒提示词"},
		{"重新拉起命令有换行", map[string]any{"enabled": true, "resume_command": "agent\nrm -rf /"}, "重新拉起命令"},
	}
	for _, tc := range bad {
		body := map[string]any{"name": guardPresetName, "host": testLauncherHost, "dir": "/root", "command": "agent", "guard": tc.guard}
		res := g.call("PUT", "/api/v1/terminal/launchers/p1", body, g.admin()...)
		if res.Status != http.StatusBadRequest || res.str("code") != "bad_launcher" || !strings.Contains(res.str("error"), tc.want) {
			t.Errorf("%s：应 400 bad_launcher 并说明「%s」: %d %s", tc.name, tc.want, res.Status, res.Raw)
		}
	}
	// 被拒绝的保存不改变原来的配置。
	if items := g.listGuard(); len(items) != 1 || items[0]["silence_minutes"] != float64(30) {
		t.Fatalf("被拒绝的保存不应生效: %v", items)
	}

	// 去掉 guard（不带该字段）就是关掉守护配置。
	g.savePreset("p1", guardPresetName, nil)
	if items := g.listGuard(); len(items) != 0 {
		t.Fatalf("去掉守护配置后不该再列出: %v", items)
	}
}

// 手动唤醒会在这个工作流的聊天里记一条系统行（谁、第几次、怎么唤醒；不含提示词与命令），并通知 Web 刷新。
func TestGuardWakeLeavesASystemLineInTheChat(t *testing.T) {
	g := newGuardEnv(t)
	var notified []string
	g.hub.SetWakeHook(func(wf string) { notified = append(notified, wf) })
	g.savePreset("p1", guardPresetName, map[string]any{"enabled": true, "workflow_id": "wf-a", "wake_prompt": "密语-不该出现 %n"})
	g.online(g.liveSession(sidOf('a'), g.clock.Now()))
	g.clock.Advance(time.Minute)

	planOf(t, g.wake("p1", nil))
	planOf(t, g.wake("p1", nil))

	evs, err := g.store.StreamEvents(context.Background(), "wf:wf-a", store.StreamRange{Dir: store.StreamOlder, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 2 {
		t.Fatalf("两次唤醒应有两条系统行: %+v", evs)
	}
	var texts []string
	for _, e := range evs {
		texts = append(texts, e.Text)
		if e.Kind != "wake" || e.ActorKind != "human" || e.WorkflowID != "wf-a" {
			t.Fatalf("系统行字段不对: %+v", e)
		}
		if strings.Contains(e.Text+e.Meta, "密语") {
			t.Fatalf("系统行不该含提示词: %+v", e)
		}
	}
	joined := strings.Join(texts, "|")
	if !strings.Contains(joined, "第 1/3 次 · 键入提示词") || !strings.Contains(joined, "第 2/3 次") {
		t.Fatalf("文字应带次数与方式: %q", joined)
	}
	if len(notified) != 2 || notified[0] != "wf-a" {
		t.Fatalf("每次唤醒应通知 Web 刷新: %v", notified)
	}
}
