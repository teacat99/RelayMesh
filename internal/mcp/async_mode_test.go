package mcp

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/teacat99/RelayMesh/v2/internal/model"
	"github.com/teacat99/RelayMesh/v2/internal/store"
)

// 异步对话模式在 MCP 层的行为：回执头、wait:false 的发布与回退、模式变化只提醒一次、notice 的边界。
// 同步工作流的回执逐字节不变由 TestSyncReceiptsGolden 守着；这里只测「开启之后」会发生什么。

const asyncTestProject = "/tmp/async-proj"

func asyncCred() *CredentialContext {
	return &CredentialContext{CredentialName: "agent", Permissions: model.AllPermissions(), Source: "db_credential"}
}

// seedRound 发一轮普通汇报（FeedbackTimeoutSeconds=1，所以约 1 秒后以超时回执返回），让工作流存在。
func seedRound(t *testing.T, srv *Server, wf string) string {
	t.Helper()
	res := inboxCall(t, srv, asyncCred(), "interactive_feedback", map[string]any{
		"workflow_id": wf, "summary": "开工汇报", "title": "开工", "project_directory": asyncTestProject,
	})
	return resultText(res)
}

// enableAsync 让工作流进入有效的异步模式：先记一次 hook，再开开关。
func enableAsync(t *testing.T, srv *Server, wf string) {
	t.Helper()
	now := time.Now()
	srv.store.RecordHookSeen(wf, now)
	on := true
	if _, err := srv.store.UpdateWorkflowMode(context.Background(), wf, store.ModePatch{Enabled: &on}, now); err != nil {
		t.Fatalf("开启异步失败: %v", err)
	}
}

func disableAsync(t *testing.T, srv *Server, wf string) {
	t.Helper()
	off := false
	if _, err := srv.store.UpdateWorkflowMode(context.Background(), wf, store.ModePatch{Enabled: &off}, time.Now()); err != nil {
		t.Fatalf("关闭异步失败: %v", err)
	}
}

// headerBetween 取回执里从 from 开头的那一行起、到 to 开头的下一行之前的内容（含续行）；找不到 from 返回空串。
func headerBetween(text, from, to string) string {
	i := strings.Index(text, "\n"+from)
	if i < 0 {
		return ""
	}
	rest := text[i+1:]
	if j := strings.Index(rest, "\n"+to); j >= 0 {
		return rest[:j]
	}
	return rest
}

func modeBlocks(res toolCallResult) []string {
	var out []string
	for _, c := range res.Content {
		if c.Type == "text" && strings.HasPrefix(c.Text, "mode:") {
			out = append(out, c.Text)
		}
	}
	return out
}

// 没开启过异步的工作流：回执里没有任何 mode / async / auth / wait_ignored 字样。
func TestAsyncReceiptSilentForSyncWorkflow(t *testing.T) {
	srv := setupTestMCPServer(t)
	text := seedRound(t, srv, "wf-sync-only")
	for _, bad := range []string{"\nmode:", "\nasync:", "\nauth:", "wait_ignored"} {
		if strings.Contains(text, bad) {
			t.Fatalf("同步工作流的回执不该出现 %q：\n%s", bad, text)
		}
	}
	if !strings.Contains(text, "phase_prompt:") {
		t.Fatalf("同步工作流应保留阶段提示词：\n%s", text)
	}
}

func TestAsyncReceiptHeaderWhenEffective(t *testing.T) {
	srv := setupTestMCPServer(t)
	const wf = "wf-async-header"
	seedRound(t, srv, wf)
	enableAsync(t, srv, wf)

	text := seedRound(t, srv, wf)
	if !strings.Contains(text, "\nmode: async") {
		t.Fatalf("应有 mode: async：\n%s", text)
	}
	contract := headerBetween(text, "async:", "auth:")
	if contract == "" {
		t.Fatalf("应有 async: 契约：\n%s", text)
	}
	if n := utf8.RuneCountInString(strings.TrimRight(contract, "\n")); n > 760 {
		t.Fatalf("async: 契约 %d 字，超过 760 字上限", n)
	}
	auth := headerBetween(text, "auth:", "checkpoint:")
	if auth == "" {
		auth = headerBetween(text, "auth:", "anchor:")
	}
	if auth == "" {
		auth = headerBetween(text, "auth:", "=== ")
	}
	firstLine := strings.SplitN(auth, "\n", 2)[0]
	if !strings.HasPrefix(firstLine, "auth: 标准授权 [L1]") {
		t.Fatalf("auth: 行应是默认档位「标准授权」：%q", firstLine)
	}
	if n := utf8.RuneCountInString(firstLine); n > 300 {
		t.Fatalf("auth: 行 %d 字，超过 300 字上限", n)
	}
	if strings.Contains(text, "phase_prompt:") {
		t.Fatalf("异步生效时授权档位应取代阶段提示词：\n%s", text)
	}
	if !strings.Contains(text, "current_phase:") {
		t.Fatalf("current_phase 仍应保留：\n%s", text)
	}
}

// hook 太久没出现：开着但降级为同步，回执头写明原因，阶段提示词回来。
func TestAsyncDegradesWhenHookStale(t *testing.T) {
	srv := setupTestMCPServer(t)
	const wf = "wf-async-stale"
	seedRound(t, srv, wf)
	now := time.Now()
	srv.store.RecordHookSeen(wf, now.Add(-2*time.Hour))
	on := true
	if _, err := srv.store.UpdateWorkflowMode(context.Background(), wf, store.ModePatch{Enabled: &on}, now); err != nil {
		t.Fatal(err)
	}
	text := seedRound(t, srv, wf)
	if !strings.Contains(text, "\nmode: sync") || !strings.Contains(text, "60 分钟没有出现") {
		t.Fatalf("应降级为同步并写明 hook 过期：\n%s", text)
	}
	if strings.Contains(text, "\nasync:") || strings.Contains(text, "\nauth:") {
		t.Fatalf("降级后不该再有 async / auth 行：\n%s", text)
	}
	if !strings.Contains(text, "phase_prompt:") {
		t.Fatalf("降级后阶段提示词应回来：\n%s", text)
	}
}

func TestWaitFalsePublishesNoticeWithoutBlocking(t *testing.T) {
	srv := setupTestMCPServer(t)
	const wf = "wf-async-notice"
	seedRound(t, srv, wf)
	enableAsync(t, srv, wf)
	ctx := context.Background()
	roundBefore, err := srv.store.GetLatestWorkflowFeedbackSession(ctx, wf)
	if err != nil {
		t.Fatal(err)
	}

	start := time.Now()
	res := inboxCall(t, srv, asyncCred(), "interactive_feedback", map[string]any{
		"workflow_id": wf, "summary": "进度：已完成第 1 步", "title": "进度", "project_directory": asyncTestProject, "wait": false,
	})
	if elapsed := time.Since(start); elapsed > 500*time.Millisecond {
		t.Fatalf("wait:false 不该阻塞，却用了 %v", elapsed)
	}
	text := resultText(res)
	if !strings.Contains(text, "=== 已发布（不阻塞）===") || !strings.Contains(text, "\nmode: async") {
		t.Fatalf("回执应是不阻塞的发布确认并带 mode 头：\n%s", text)
	}
	if strings.Contains(text, "等待回执") || strings.Contains(text, "反馈超时") {
		t.Fatalf("不该进入等待：\n%s", text)
	}

	// 通知落库为 notice：已完成、已消费，不是工作流的「最新轮次」。
	sessions, err := srv.store.GetSessionsByWorkflow(ctx, wf, 20)
	if err != nil {
		t.Fatal(err)
	}
	var notices []model.FeedbackSession
	for _, s := range sessions {
		if s.Kind == model.FeedbackKindNotice {
			notices = append(notices, s)
		}
	}
	if len(notices) != 1 {
		t.Fatalf("应有 1 条 notice，实际 %d", len(notices))
	}
	n := notices[0]
	if n.Status != "completed" || !n.ConsumedByAI || n.Summary != "进度：已完成第 1 步" {
		t.Fatalf("notice 状态不对: %+v", n)
	}
	latest, err := srv.store.GetLatestWorkflowFeedbackSession(ctx, wf)
	if err != nil || latest.ID != roundBefore.ID {
		t.Fatalf("notice 不该成为最新轮次: %v %+v", err, latest)
	}
	rounds, err := srv.store.GetFeedbackRoundsByWorkflow(ctx, wf, 20)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rounds {
		if r.Kind == model.FeedbackKindNotice {
			t.Fatalf("GetFeedbackRoundsByWorkflow 不该含 notice")
		}
	}
}

// notice 不能把收件箱里的追加当成回复吞掉。
func TestNoticeDoesNotSwallowInbox(t *testing.T) {
	srv := setupTestMCPServer(t)
	const wf = "wf-async-inbox"
	seedRound(t, srv, wf)
	enableAsync(t, srv, wf)
	id := queueInbox(t, srv, wf, "请顺便检查一下边界")

	res := inboxCall(t, srv, asyncCred(), "interactive_feedback", map[string]any{
		"workflow_id": wf, "summary": "进度", "project_directory": asyncTestProject, "wait": false,
	})
	if !strings.Contains(resultText(res), "已发布（不阻塞）") {
		t.Fatalf("应是发布确认：%s", resultText(res))
	}
	items, err := srv.store.ListInboxItems(context.Background(), wf)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, q := range items {
		if q.ID == id {
			found = true
			if q.State != model.InboxStateQueued {
				t.Fatalf("追加不该被 notice 消费，状态=%s", q.State)
			}
		}
	}
	if !found {
		t.Fatalf("追加 #%d 不见了", id)
	}
	// 回执末尾照常提示有未读追加
	if len(hintBlocks(res)) == 0 {
		t.Fatalf("回执末尾应提示 inbox 有未读追加：%+v", res.Content)
	}
}

// 没有生效的异步：wait:false 被忽略，照常阻塞，回执头说明原因。
func TestWaitFalseIgnoredWhenNotAsync(t *testing.T) {
	srv := setupTestMCPServer(t)
	cred := asyncCred()

	t.Run("从未开启", func(t *testing.T) {
		const wf = "wf-wait-never"
		seedRound(t, srv, wf)
		res := inboxCall(t, srv, cred, "interactive_feedback", map[string]any{
			"workflow_id": wf, "summary": "进度", "project_directory": asyncTestProject, "wait": false,
		})
		text := resultText(res)
		if !strings.Contains(text, "wait_ignored:") || !strings.Contains(text, "没有开启异步对话") {
			t.Fatalf("应说明 wait:false 被忽略：\n%s", text)
		}
		if strings.Contains(text, "已发布（不阻塞）") {
			t.Fatalf("不该发布 notice：\n%s", text)
		}
		sessions, _ := srv.store.GetSessionsByWorkflow(context.Background(), wf, 20)
		for _, s := range sessions {
			if s.Kind == model.FeedbackKindNotice {
				t.Fatalf("不该产生 notice 行")
			}
		}
	})

	t.Run("开启过又关了", func(t *testing.T) {
		const wf = "wf-wait-disabled"
		seedRound(t, srv, wf)
		enableAsync(t, srv, wf)
		disableAsync(t, srv, wf)
		text := resultText(inboxCall(t, srv, cred, "interactive_feedback", map[string]any{
			"workflow_id": wf, "summary": "进度", "project_directory": asyncTestProject, "wait": false,
		}))
		if !strings.Contains(text, "wait_ignored:") || !strings.Contains(text, "用户已关闭异步对话") {
			t.Fatalf("应写明用户已关闭：\n%s", text)
		}
	})

	t.Run("缺 workflow_id", func(t *testing.T) {
		text := resultText(inboxCall(t, srv, cred, "interactive_feedback", map[string]any{
			"summary": "进度", "project_directory": asyncTestProject, "wait": false,
		}))
		if !strings.Contains(text, "wait_ignored:") || !strings.Contains(text, "workflow_id") {
			t.Fatalf("应说明需要显式 workflow_id：\n%s", text)
		}
	})
}

// wait 缺省 / true / 乱写：一律按普通汇报阻塞，也不出现 wait_ignored（INV-2：不改变同步行为）。
func TestWaitOtherValuesBlockAsUsual(t *testing.T) {
	srv := setupTestMCPServer(t)
	const wf = "wf-wait-others"
	seedRound(t, srv, wf)
	enableAsync(t, srv, wf)
	for _, v := range []any{true, "true", "", "maybe", 1} {
		text := resultText(inboxCall(t, srv, asyncCred(), "interactive_feedback", map[string]any{
			"workflow_id": wf, "summary": "要回复", "project_directory": asyncTestProject, "wait": v,
		}))
		if strings.Contains(text, "已发布（不阻塞）") || strings.Contains(text, "wait_ignored:") {
			t.Fatalf("wait=%#v 应照常阻塞且不报忽略：\n%s", v, text)
		}
	}
}

func TestExplicitFalseParsing(t *testing.T) {
	cases := map[string]bool{
		``: false, `null`: false, `true`: false, `"true"`: false, `""`: false, `"maybe"`: false, `1`: false,
		`false`: true, `"false"`: true, `"FALSE"`: true, `0`: true, `"0"`: true, `"no"`: true,
	}
	for raw, want := range cases {
		if got := explicitFalse([]byte(raw)); got != want {
			t.Errorf("explicitFalse(%s) = %v，想要 %v", raw, got, want)
		}
	}
}

// 模式变化只提醒一次：开启、关闭、换档位各一次，之后的调用不重复。
func TestModeChangeBlockIsSentOnce(t *testing.T) {
	srv := setupTestMCPServer(t)
	cred := asyncCred()
	const wf = "wf-async-told"
	seedRound(t, srv, wf)

	call := func() toolCallResult {
		return inboxCall(t, srv, cred, "workflow_context", map[string]any{"action": "task_list", "workflow_id": wf})
	}
	if blocks := modeBlocks(call()); len(blocks) != 0 {
		t.Fatalf("从未开启的工作流不该有 mode 块：%v", blocks)
	}

	enableAsync(t, srv, wf)
	blocks := modeBlocks(call())
	if len(blocks) != 1 || !strings.HasPrefix(blocks[0], "mode: async") || !strings.Contains(blocks[0], "auth:") {
		t.Fatalf("开启后应有一次 mode: async 提醒：%v", blocks)
	}
	if blocks := modeBlocks(call()); len(blocks) != 0 {
		t.Fatalf("同一状态不该重复提醒：%v", blocks)
	}

	// 收件箱动作同样要带提醒（用户刚切模式，智能体多半正是读收件箱时才发现）。
	disableAsync(t, srv, wf)
	res := inboxCall(t, srv, cred, "workflow_context", map[string]any{"action": "inbox", "workflow_id": wf})
	blocks = modeBlocks(res)
	if len(blocks) != 1 || !strings.Contains(blocks[0], "mode: sync") || !strings.Contains(blocks[0], "用户已关闭异步对话") {
		t.Fatalf("关闭后读收件箱应有一次降级提醒：%v", blocks)
	}
	if blocks := modeBlocks(call()); len(blocks) != 0 {
		t.Fatalf("降级提醒不该重复：%v", blocks)
	}

	// 换档位也算变化。
	enableAsync(t, srv, wf)
	_ = modeBlocks(call()) // 吃掉「重新开启」的提醒
	name, level, rules := "谨慎", "L0", "只读分析；任何写入都先用 async_ask 带默认值问。"
	p, err := srv.store.CreateAuthProfile(context.Background(), store.AuthProfileInput{Name: &name, Level: &level, Rules: &rules})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := srv.store.UpdateWorkflowMode(context.Background(), wf, store.ModePatch{ProfileID: &p.ID}, time.Now()); err != nil {
		t.Fatal(err)
	}
	blocks = modeBlocks(call())
	if len(blocks) != 1 || !strings.Contains(blocks[0], "auth: 谨慎 [L0]") {
		t.Fatalf("换档位应有一次提醒并带新档位：%v", blocks)
	}

	// 档位的内容被改了也算变化：用户改了规则，智能体得知道。
	ctx := context.Background()
	newRules := "只读分析；写入一律先问。"
	if _, err := srv.store.UpdateAuthProfile(ctx, p.ID, store.AuthProfileInput{Rules: &newRules}); err != nil {
		t.Fatal(err)
	}
	blocks = modeBlocks(call())
	if len(blocks) != 1 || !strings.Contains(blocks[0], newRules) {
		t.Fatalf("档位规则被改后应有一次提醒并带新规则：%v", blocks)
	}
	if blocks := modeBlocks(call()); len(blocks) != 0 {
		t.Fatalf("改规则的提醒不该重复：%v", blocks)
	}

	// 工作流选用的档位被删掉：回落到默认档位，同样要告知。
	if err := srv.store.DeleteAuthProfile(ctx, p.ID); err != nil {
		t.Fatal(err)
	}
	blocks = modeBlocks(call())
	if len(blocks) != 1 || !strings.Contains(blocks[0], "auth: 标准授权") {
		t.Fatalf("选用的档位被删后应回落默认档位并提醒一次：%v", blocks)
	}
}

// 带完整回执头的汇报已经把当前状态告诉了智能体，后面的工具调用不再重复。
func TestFeedbackHeaderMarksModeAsTold(t *testing.T) {
	srv := setupTestMCPServer(t)
	cred := asyncCred()
	const wf = "wf-async-told-header"
	seedRound(t, srv, wf)
	enableAsync(t, srv, wf)
	seedRound(t, srv, wf) // 回执头带了 mode: async，并记为已告知
	res := inboxCall(t, srv, cred, "workflow_context", map[string]any{"action": "task_list", "workflow_id": wf})
	if blocks := modeBlocks(res); len(blocks) != 0 {
		t.Fatalf("回执头已经告知过，不该再补一块：%v", blocks)
	}
}

func TestSetPhaseInAsyncModeCarriesAuthInsteadOfPhasePrompt(t *testing.T) {
	srv := setupTestMCPServer(t)
	cred := asyncCred()
	const syncWF, asyncWF = "wf-phase-sync", "wf-phase-async"
	seedRound(t, srv, syncWF)
	seedRound(t, srv, asyncWF)
	enableAsync(t, srv, asyncWF)

	out := firstJSON(t, inboxCall(t, srv, cred, "workflow_context", map[string]any{"action": "set_phase", "workflow_id": syncWF, "phase_id": "plan"}))
	if p, _ := out["phase_prompt"].(string); strings.TrimSpace(p) == "" {
		t.Fatalf("同步工作流的 set_phase 应带阶段提示词：%v", out)
	}
	if _, has := out["mode"]; has {
		t.Fatalf("同步工作流的 set_phase 不该多出 mode 字段：%v", out)
	}

	out = firstJSON(t, inboxCall(t, srv, cred, "workflow_context", map[string]any{"action": "set_phase", "workflow_id": asyncWF, "phase_id": "plan"}))
	if _, has := out["phase_prompt"]; has {
		t.Fatalf("异步生效时 set_phase 不该再带阶段提示词：%v", out)
	}
	if out["mode"] != "async" {
		t.Fatalf("应标明 mode=async：%v", out)
	}
	if auth, _ := out["auth"].(string); !strings.HasPrefix(auth, "auth: 标准授权 [L1]") {
		t.Fatalf("应带授权档位：%v", out)
	}
}

func TestContinueFeedbackOnNoticeSessionExplains(t *testing.T) {
	srv := setupTestMCPServer(t)
	const wf = "wf-async-continue"
	enableAsync(t, srv, wf) // 工作流还没有任何汇报轮次：notice 是它的第一条记录
	cred := asyncCred()
	res := inboxCall(t, srv, cred, "interactive_feedback", map[string]any{
		"workflow_id": wf, "summary": "只有通知", "project_directory": asyncTestProject, "wait": false,
	})
	text := resultText(res)
	if !strings.Contains(text, "已发布（不阻塞）") {
		t.Fatalf("应是发布确认：%s", text)
	}
	sessions, err := srv.store.GetSessionsByWorkflow(context.Background(), wf, 5)
	if err != nil || len(sessions) != 1 {
		t.Fatalf("应只有一条 notice: %v %d", err, len(sessions))
	}
	res = inboxCall(t, srv, cred, "continue_feedback_session", map[string]any{"session_id": sessions[0].ID})
	if !res.IsError || !strings.Contains(resultText(res), "不阻塞的进度通知") {
		t.Fatalf("对 notice 续等应给出解释性错误：%+v", res)
	}
}

func TestNoticeRateLimit(t *testing.T) {
	srv := setupTestMCPServer(t)
	const wf = "wf-async-rate"
	seedRound(t, srv, wf)
	enableAsync(t, srv, wf)
	cred := asyncCred()
	for i := 0; i < noticeRatePerHour; i++ {
		res := inboxCall(t, srv, cred, "interactive_feedback", map[string]any{
			"workflow_id": wf, "summary": fmt.Sprintf("进度 %d", i), "project_directory": asyncTestProject, "wait": false,
		})
		if res.IsError {
			t.Fatalf("第 %d 条不该被限流：%s", i+1, resultText(res))
		}
	}
	res := inboxCall(t, srv, cred, "interactive_feedback", map[string]any{
		"workflow_id": wf, "summary": "超出", "project_directory": asyncTestProject, "wait": false,
	})
	if !res.IsError || !strings.Contains(resultText(res), "达到上限") {
		t.Fatalf("超过每小时上限应被拒绝并说明：%+v", res)
	}
}

// notice 是进度气泡，不是「轮」：会话列表按工作流汇总时不计数，历史里单独标出。
func TestSessionListingsTreatNoticesAsNotRounds(t *testing.T) {
	srv := setupTestMCPServer(t)
	const wf = "wf-async-listing"
	seedRound(t, srv, wf)
	enableAsync(t, srv, wf)
	cred := asyncCred()
	for i := 0; i < 2; i++ {
		inboxCall(t, srv, cred, "interactive_feedback", map[string]any{
			"workflow_id": wf, "summary": fmt.Sprintf("进度 %d", i), "project_directory": asyncTestProject, "wait": false,
		})
	}

	hist := firstJSON(t, inboxCall(t, srv, cred, "get_session_history", map[string]any{"workflow_id": wf}))
	if hist["rounds"] != float64(1) || hist["notices"] != float64(2) {
		t.Fatalf("历史应是 1 轮 + 2 条通知：%v", hist)
	}
	items, _ := hist["history"].([]any)
	kinds := 0
	for _, it := range items {
		if m, _ := it.(map[string]any); m["kind"] == "notice" {
			kinds++
		}
	}
	if kinds != 2 {
		t.Fatalf("历史里应有 2 条带 kind=notice 的条目：%v", hist)
	}

	grouped := firstJSON(t, inboxCall(t, srv, cred, "list_sessions", map[string]any{"project_directory": asyncTestProject, "group_by": "workflow"}))
	wfs, _ := grouped["workflows"].([]any)
	var count float64 = -1
	for _, w := range wfs {
		if m, _ := w.(map[string]any); m["workflow_id"] == wf {
			count, _ = m["session_count"].(float64)
		}
	}
	if count != 1 {
		t.Fatalf("按工作流汇总的轮次数不该计入通知，应为 1，实际 %v：%v", count, grouped)
	}

	// 回执里的 recent_workflows 同样只数轮：再发一条汇报（复用同一个 pending 轮次）后仍是 1 轮，不含 2 条通知。
	text := seedRound(t, srv, wf)
	if !strings.Contains(text, wf+"(") || !strings.Contains(text, ", 1轮,") {
		t.Fatalf("recent_workflows 应只数汇报轮次（1 轮）：\n%s", text)
	}
}

// #109：recent_workflows 先取最近 100 条会话再过滤通知时，每发一条通知就把一轮更早的汇报挤出窗口，
// 回执里的「N 轮」随通知越变越少；通知必须在取数时就排除，轮次数与通知数量无关。
func TestRecentWorkflowsCountIgnoresNoticeFlood(t *testing.T) {
	srv := setupTestMCPServer(t)
	const wf = "wf-notice-flood"
	ctx := context.Background()
	seedRound(t, srv, wf)
	enableAsync(t, srv, wf)

	// 直接写库造出远超 100 条的通知（经 MCP 发布会撞每小时 60 条的上限），再补一轮较新的汇报。
	for i := 0; i < 130; i++ {
		if _, err := srv.store.CreateNoticeSession(ctx, store.CreateSessionInput{
			WorkflowID: wf, ProjectDirectory: asyncTestProject, Summary: fmt.Sprintf("进度 %d", i),
		}); err != nil {
			t.Fatalf("写入通知 %d 失败: %v", i, err)
		}
	}

	text := seedRound(t, srv, wf)
	if !strings.Contains(text, wf+"(") || !strings.Contains(text, ", 1轮,") {
		t.Fatalf("130 条通知之后 recent_workflows 仍应是 1 轮（通知不能把轮次挤出 100 条窗口）：\n%s", text)
	}

	// store 层同口径：按轮列出时一条通知都不该出现，且不受 limit 窗口影响
	rounds, err := srv.store.ListFeedbackRoundSessions(ctx, asyncTestProject, "", 100)
	if err != nil {
		t.Fatalf("按轮列出失败: %v", err)
	}
	if len(rounds) != 1 {
		t.Fatalf("按轮列出应只有 1 轮，实际 %d", len(rounds))
	}
	for _, s := range rounds {
		if s.Kind == model.FeedbackKindNotice {
			t.Fatalf("按轮列出不该含通知：%+v", s)
		}
	}
}
