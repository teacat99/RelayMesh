package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/teacat99/RelayMesh/v2/internal/model"
	"github.com/teacat99/RelayMesh/v2/internal/store"
)

// 1×1 的 PNG，用来测收件箱里带图片的追加。
const inboxTestPNG = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR4nGP4z8DwHwAFAAH/q842iQAAAABJRU5ErkJggg=="

// inboxCall 通过完整的 JSON-RPC 入口调用一个工具，返回它的结果（含 HandleRPCRequest 收尾追加的提示块）。
func inboxCall(t *testing.T, srv *Server, cred *CredentialContext, tool string, args map[string]any) toolCallResult {
	t.Helper()
	params, err := json.Marshal(map[string]any{"name": tool, "arguments": args})
	if err != nil {
		t.Fatal(err)
	}
	req := jsonRPCRequest{JSONRPC: "2.0", Method: "tools/call", Params: params}
	req.ID = json.RawMessage(`1`)
	out := srv.HandleRPCRequest(context.Background(), cred, &req)
	if out.Response == nil || out.Response.Error != nil {
		t.Fatalf("%s 调用失败: %+v", tool, out.Response)
	}
	res, ok := out.Response.Result.(toolCallResult)
	if !ok {
		t.Fatalf("%s 的结果类型不对: %T", tool, out.Response.Result)
	}
	return res
}

func resultText(res toolCallResult) string {
	var parts []string
	for _, c := range res.Content {
		if c.Type == "text" {
			parts = append(parts, c.Text)
		}
	}
	return strings.Join(parts, "\n")
}

func firstJSON(t *testing.T, res toolCallResult) map[string]any {
	t.Helper()
	if len(res.Content) == 0 {
		t.Fatal("结果没有内容")
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(res.Content[0].Text), &out); err != nil {
		t.Fatalf("第一块内容不是 JSON: %v\n%s", err, res.Content[0].Text)
	}
	return out
}

func feedbackCred() *CredentialContext {
	return &CredentialContext{CredentialName: "agent", Permissions: model.Permissions{Feedback: true}, Source: "db_credential"}
}

func queueInbox(t *testing.T, srv *Server, wf, text string) uint {
	t.Helper()
	q, err := srv.store.QueueWorkflowFeedback(context.Background(), store.QueueFeedbackInput{WorkflowID: wf, ResponseText: text})
	if err != nil {
		t.Fatalf("追加失败: %v", err)
	}
	return q.ID
}

func hintBlocks(res toolCallResult) []string {
	var hints []string
	for _, c := range res.Content {
		if c.Type == "text" && strings.HasPrefix(c.Text, "inbox:") {
			hints = append(hints, c.Text)
		}
	}
	return hints
}

func TestInbox_ReadPeekAckActions(t *testing.T) {
	srv := setupTestMCPServer(t)
	cred := feedbackCred()
	wf := "wf-actions"
	a := queueInbox(t, srv, wf, "把按钮改成蓝色")
	b := queueInbox(t, srv, wf, "再把标题加粗")

	peek := firstJSON(t, inboxCall(t, srv, cred, "workflow_context", map[string]any{"action": "inbox_peek", "workflow_id": wf}))
	if peek["unread"].(float64) != 2 || peek["delivered"].(float64) != 0 || peek["remind"].(bool) {
		t.Fatalf("peek 应是 2 条未读、不提醒: %+v", peek)
	}
	if !strings.Contains(peek["latest_preview"].(string), "再把标题加粗") {
		t.Fatalf("预览应是最新一条: %+v", peek)
	}

	read := resultText(inboxCall(t, srv, cred, "workflow_context", map[string]any{"action": "inbox", "workflow_id": wf}))
	for _, want := range []string{"把按钮改成蓝色", "再把标题加粗", fmt.Sprintf("追加 #%d", a), fmt.Sprintf("追加 #%d", b), "inbox_ack"} {
		if !strings.Contains(read, want) {
			t.Fatalf("读取结果缺少 %q:\n%s", want, read)
		}
	}
	if strings.Index(read, "把按钮改成蓝色") > strings.Index(read, "再把标题加粗") {
		t.Fatalf("应按时间顺序:\n%s", read)
	}

	// 读过之后：不再算未读，但在确认之前每次读取都会再列一遍
	peek = firstJSON(t, inboxCall(t, srv, cred, "workflow_context", map[string]any{"action": "inbox_peek", "workflow_id": wf}))
	if peek["unread"].(float64) != 0 || peek["delivered"].(float64) != 2 {
		t.Fatalf("读取后应是 0 未读、2 已读未确认: %+v", peek)
	}
	again := resultText(inboxCall(t, srv, cred, "workflow_context", map[string]any{"action": "inbox", "workflow_id": wf}))
	if !strings.Contains(again, "仍未确认") || !strings.Contains(again, "把按钮改成蓝色") {
		t.Fatalf("未确认的应再列一遍:\n%s", again)
	}

	// 只确认一条；不存在的编号被如实报告
	ack := firstJSON(t, inboxCall(t, srv, cred, "workflow_context", map[string]any{"action": "inbox_ack", "workflow_id": wf, "ids": []any{a, 99999}}))
	if ack["acked"].(float64) != 1 || ack["delivered_unacked"].(float64) != 1 {
		t.Fatalf("应只确认 1 条: %+v", ack)
	}
	if ignored, _ := ack["ignored_ids"].([]any); len(ignored) != 1 || ignored[0].(float64) != 99999 {
		t.Fatalf("没确认到的编号应在 ignored_ids 里: %+v", ack)
	}

	// 全部确认，编号写成字符串也认
	ack = firstJSON(t, inboxCall(t, srv, cred, "workflow_context", map[string]any{"action": "inbox_ack", "workflow_id": wf}))
	if ack["acked"].(float64) != 1 || ack["next"] != "收件箱已清空。" {
		t.Fatalf("应确认剩下的 1 条并清空: %+v", ack)
	}
	empty := resultText(inboxCall(t, srv, cred, "workflow_context", map[string]any{"action": "inbox", "workflow_id": wf}))
	if !strings.Contains(empty, "没有新的追加") {
		t.Fatalf("清空后应提示没有新消息: %s", empty)
	}

	recent := resultText(inboxCall(t, srv, cred, "workflow_context", map[string]any{"action": "inbox", "workflow_id": wf, "recent": true}))
	if !strings.Contains(recent, "最近已确认") || !strings.Contains(recent, "把按钮改成蓝色") {
		t.Fatalf("recent=true 应列出最近已确认的:\n%s", recent)
	}

	// 缺 workflow_id 是错误
	if _, err := srv.handleWorkflowContext(context.Background(), json.RawMessage(`{"action":"inbox"}`)); err == nil {
		t.Fatal("inbox 缺少 workflow_id 应报错")
	}
}

// 经 inbox 读到并确认的追加不会出现在对话轮次里；历史查询要把它们单独列出来，否则「纳入历史内容供给查询」就是句空话。
func TestInbox_HistoryListsAppendedMessagesReadOnly(t *testing.T) {
	srv := setupTestMCPServer(t)
	cred := &CredentialContext{CredentialName: "agent", Permissions: model.Permissions{Feedback: true, Sessions: true}, Source: "db_credential"}
	wf := "wf-history-appends"
	acked := queueInbox(t, srv, wf, "靠 inbox 读到的追加")
	delivered := queueInbox(t, srv, wf, "读到了但还没确认的追加")
	img, err := srv.store.QueueWorkflowFeedback(context.Background(), store.QueueFeedbackInput{
		WorkflowID: wf, ResponseText: "带图的追加", Images: []model.SessionImage{{Name: "a.png", Data: inboxTestPNG, Format: "png"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	// 智能体主动读取收件箱：三条都变成「已读取」（经 tool 送达），再只确认第一条
	if _, err := srv.store.InboxRead(context.Background(), wf, store.InboxReadOptions{}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := srv.store.InboxAck(context.Background(), wf, []uint{acked}, time.Now()); err != nil {
		t.Fatal(err)
	}
	// 再追加一条真正没读过的
	fresh := queueInbox(t, srv, wf, "刚追加的")

	hist := firstJSON(t, inboxCall(t, srv, cred, "get_session_history", map[string]any{"workflow_id": wf}))
	list, _ := hist["appended_messages"].([]any)
	if len(list) != 4 {
		t.Fatalf("应列出 4 条追加: %+v", hist)
	}
	byID := map[float64]map[string]any{}
	for _, it := range list {
		m := it.(map[string]any)
		byID[m["id"].(float64)] = m
	}
	if m := byID[float64(acked)]; m["state"] != "acked" || m["text"] != "靠 inbox 读到的追加" || m["delivered_via"] != "tool" || m["acked_at"] == nil {
		t.Fatalf("经 inbox 读到并确认的追加应带全文与送达路径: %+v", m)
	}
	if m := byID[float64(delivered)]; m["state"] != "delivered" {
		t.Fatalf("已读取未确认的应是 delivered: %+v", m)
	}
	if m := byID[float64(fresh)]; m["state"] != "queued" {
		t.Fatalf("没读过的应是 queued: %+v", m)
	}
	m := byID[float64(img.ID)]
	if m["image_count"].(float64) != 1 || m["image_session_id"] != fmt.Sprintf("inbox-%d", img.ID) {
		t.Fatalf("带图的追加应给出张数与图片寻址编号: %+v", m)
	}
	if strings.Contains(resultText(inboxCall(t, srv, cred, "get_session_history", map[string]any{"workflow_id": wf})), inboxTestPNG) {
		t.Fatal("历史里不应带图片数据")
	}
	if note, _ := hist["appended_messages_note"].(string); !strings.Contains(note, "inbox") {
		t.Fatalf("应提醒未读的要走 inbox 读取并确认: %q", note)
	}

	// 只读：列出历史不改变任何一条的状态（fresh 仍是 queued，仍会被下一次 inbox 作为新消息送达）
	if got := firstJSON(t, inboxCall(t, srv, cred, "workflow_context", map[string]any{"action": "inbox_peek", "workflow_id": wf})); got["unread"].(float64) != 1 {
		t.Fatalf("历史查询不应让未读变成已读: %+v", got)
	}

	// 没有任何追加的工作流：不带这两个字段
	plain := firstJSON(t, inboxCall(t, srv, cred, "get_session_history", map[string]any{"workflow_id": "wf-history-none"}))
	if _, has := plain["appended_messages"]; has {
		t.Fatalf("没有追加时不应出现 appended_messages: %+v", plain)
	}
}

func TestInbox_ReceiptHintOnOtherToolsAndNotOnInboxActions(t *testing.T) {
	srv := setupTestMCPServer(t)
	cred := feedbackCred()
	wf := "wf-hint"
	queueInbox(t, srv, wf, "记得先跑一遍单测再提交")

	// 任意带 workflow_id 的工具：回执末尾多一块 inbox: 提示，原有内容原样保留在前面
	res := inboxCall(t, srv, cred, "workflow_context", map[string]any{"action": "checkpoint_list", "workflow_id": wf})
	hints := hintBlocks(res)
	if len(hints) != 1 {
		t.Fatalf("应追加恰好一块 inbox: 提示: %+v", res.Content)
	}
	if last := res.Content[len(res.Content)-1].Text; last != hints[0] && !strings.HasPrefix(last, "hook:") {
		t.Fatalf("inbox 提示后最多再跟 hook: 安装提示: %+v", res.Content)
	}
	for _, want := range []string{"1 条追加指令", "workflow_context", `"inbox"`, wf, "记得先跑一遍单测再提交", "须读全文再行动"} {
		if !strings.Contains(hints[0], want) {
			t.Fatalf("提示缺少 %q:\n%s", want, hints[0])
		}
	}
	if res.IsError {
		t.Fatal("追加提示不应改变 IsError")
	}

	// 收件箱自己的动作不附提示
	for _, action := range []string{"inbox_peek", "inbox_ack"} {
		r := inboxCall(t, srv, cred, "workflow_context", map[string]any{"action": action, "workflow_id": wf})
		if len(hintBlocks(r)) != 0 || len(r.Content) != 1 {
			t.Fatalf("%s 的回执不应带提示: %+v", action, r.Content)
		}
	}
	// 读取之后，其他工具也不再带提示
	_ = inboxCall(t, srv, cred, "workflow_context", map[string]any{"action": "inbox", "workflow_id": wf})
	res = inboxCall(t, srv, cred, "workflow_context", map[string]any{"action": "checkpoint_list", "workflow_id": wf})
	if len(hintBlocks(res)) != 0 {
		t.Fatalf("读过之后不应再提示: %+v", res.Content)
	}

	// 别的工作流的追加不会串过来；没有 workflow_id 的调用没有提示
	queueInbox(t, srv, "wf-other", "别人的")
	res = inboxCall(t, srv, cred, "workflow_context", map[string]any{"action": "checkpoint_list", "workflow_id": wf})
	if len(hintBlocks(res)) != 0 {
		t.Fatalf("不应串到别的工作流: %+v", res.Content)
	}
	res = inboxCall(t, srv, cred, "workflow_context", map[string]any{"action": "list_workflows", "project_directory": "/x"})
	if len(hintBlocks(res)) != 0 {
		t.Fatalf("没有 workflow_id 的调用不附提示: %+v", res.Content)
	}
}

func TestInbox_HintSkippedForErrorsAndCredentialsThatCannotAct(t *testing.T) {
	srv := setupTestMCPServer(t)
	wf := "wf-skip"
	queueInbox(t, srv, wf, "有未读")

	// 出错的调用不附提示
	res := inboxCall(t, srv, feedbackCred(), "workflow_context", map[string]any{"action": "no_such_action", "workflow_id": wf})
	if !res.IsError || len(hintBlocks(res)) != 0 {
		t.Fatalf("出错的调用不附提示: %+v", res)
	}
	// 没有 workflow_context 权限的凭据（没法按提示行动）：不附提示
	sysOnly := &CredentialContext{CredentialName: "sys", Permissions: model.Permissions{SystemInfo: true}}
	res = inboxCall(t, srv, sysOnly, "get_system_info", map[string]any{"workflow_id": wf})
	if len(hintBlocks(res)) != 0 {
		t.Fatalf("无 workflow_context 权限不附提示: %+v", res.Content)
	}
	// 同一个工作流，能行动的凭据照常收到
	res = inboxCall(t, srv, feedbackCred(), "workflow_context", map[string]any{"action": "checkpoint_list", "workflow_id": wf})
	if len(hintBlocks(res)) != 1 {
		t.Fatalf("有权限的凭据应收到提示: %+v", res.Content)
	}
}

func TestInbox_SwitchesGateEachLayer(t *testing.T) {
	srv := setupTestMCPServer(t)
	ctx := context.Background()
	cred := feedbackCred()
	wf := "wf-switch"
	queueInbox(t, srv, wf, "开关测试")
	call := func() toolCallResult {
		return inboxCall(t, srv, cred, "workflow_context", map[string]any{"action": "checkpoint_list", "workflow_id": wf})
	}
	clause := func() bool {
		return strings.Contains(srv.InstructionsForPermissions(ctx, cred.Permissions), "11. 追加指令收件箱")
	}
	set := func(mut func(c store.InboxConfig) store.InboxConfig) {
		t.Helper()
		if _, err := srv.store.UpdateInboxConfig(ctx, mut); err != nil {
			t.Fatal(err)
		}
	}

	// 默认全开
	if len(hintBlocks(call())) != 1 || !clause() {
		t.Fatal("默认应开着提示行与提示词条款")
	}
	// 只关第二层：提示行消失，条款还在
	set(func(c store.InboxConfig) store.InboxConfig { c.ReceiptHint = false; return c })
	if len(hintBlocks(call())) != 0 || !clause() {
		t.Fatal("关掉回执提示行后提示应消失，条款不受影响")
	}
	// 只关第三层
	set(func(c store.InboxConfig) store.InboxConfig { c.ReceiptHint, c.PromptClause = true, false; return c })
	if len(hintBlocks(call())) != 1 || clause() {
		t.Fatal("关掉提示词条款后条款应消失，提示行不受影响")
	}
	// 总开关关：全部消失，但显式读取仍然可用，已排队的追加一条不丢
	set(func(c store.InboxConfig) store.InboxConfig { c.Enabled, c.PromptClause = false, true; return c })
	if len(hintBlocks(call())) != 0 || clause() {
		t.Fatal("总开关关掉后提示行与条款都应消失")
	}
	read := resultText(inboxCall(t, srv, cred, "workflow_context", map[string]any{"action": "inbox", "workflow_id": wf}))
	if !strings.Contains(read, "开关测试") {
		t.Fatalf("总开关关掉后仍应能显式读取:\n%s", read)
	}
	// 环境变量总闸：即使界面里是开着的，也强制关
	set(func(c store.InboxConfig) store.InboxConfig { return store.DefaultInboxConfig() })
	queueInbox(t, srv, wf, "环境变量测试")
	if len(hintBlocks(call())) != 1 || !clause() {
		t.Fatal("恢复默认后应重新出现")
	}
	srv.cfg.InboxOff = true
	if len(hintBlocks(call())) != 0 || clause() {
		t.Fatal("RELAYMESH_INBOX=off 应压过界面里的开关")
	}
	srv.cfg.InboxOff = false

	// 终端档（ExecMesh）的指引从不含收件箱条款
	if strings.Contains(srv.InstructionsForPermissions(ctx, model.Permissions{Terminal: true}), "追加指令收件箱") {
		t.Fatal("终端档指引不应有收件箱条款")
	}
	if strings.Contains(srv.InstructionsForPermissions(ctx, model.Permissions{SystemInfo: true}), "追加指令收件箱") {
		t.Fatal("没有 workflow_context 权限的令牌不应收到收件箱条款")
	}
}

func TestInbox_PeekReminderScheduleAndHookSwitch(t *testing.T) {
	srv := setupTestMCPServer(t)
	ctx := context.Background()
	cred := feedbackCred()
	wf := "wf-peek"
	queueInbox(t, srv, wf, "hook 提醒测试")
	peek := func(reminder bool) map[string]any {
		return firstJSON(t, inboxCall(t, srv, cred, "workflow_context", map[string]any{"action": "inbox_peek", "workflow_id": wf, "reminder": reminder}))
	}

	// 纯查询：不提醒、不记账，但给出 stop 用的文案
	p := peek(false)
	if p["remind"].(bool) || p["message"] != nil || !strings.Contains(p["stop_message"].(string), "还没读到") {
		t.Fatalf("纯查询不应提醒: %+v", p)
	}
	// 第一次 reminder=true：立即提醒，并带着现成的提醒正文
	p = peek(true)
	if !p["remind"].(bool) || !strings.Contains(p["message"].(string), "inbox:") || !strings.Contains(p["message"].(string), "hook 提醒测试") {
		t.Fatalf("第一次应立即提醒: %+v", p)
	}
	// 紧接着再问：按节奏不再提醒
	if p = peek(true); p["remind"].(bool) || p["message"] != nil {
		t.Fatalf("短时间内不应重复提醒: %+v", p)
	}
	// hook 开关关掉：hook 查询一律安静，也不给文案。这时来一条新追加（本来立刻该提醒），
	// 被静音的查询既不能出声，也不能把它的提醒次数偷偷用掉。
	if _, err := srv.store.UpdateInboxConfig(ctx, func(c store.InboxConfig) store.InboxConfig { c.Hooks = false; return c }); err != nil {
		t.Fatal(err)
	}
	queueInbox(t, srv, wf, "hook 关闭期间的新追加")
	p = peek(true)
	if p["hooks_enabled"].(bool) || p["remind"].(bool) || p["message"] != nil || p["stop_message"] != nil {
		t.Fatalf("hook 关闭后应安静: %+v", p)
	}
	if p["unread"].(float64) != 2 {
		t.Fatalf("关闭 hook 不影响计数: %+v", p)
	}
	// 总开关关掉同理
	if _, err := srv.store.UpdateInboxConfig(ctx, func(c store.InboxConfig) store.InboxConfig { c.Hooks, c.Enabled = true, false; return c }); err != nil {
		t.Fatal(err)
	}
	if p = peek(true); p["enabled"].(bool) || p["hooks_enabled"].(bool) || p["remind"].(bool) {
		t.Fatalf("总开关关闭后 hook 应安静: %+v", p)
	}
	// 重新打开：之前被静音时没有消耗提醒次数，这条新追加此刻仍然该立即提醒
	if _, err := srv.store.UpdateInboxConfig(ctx, func(c store.InboxConfig) store.InboxConfig { c.Enabled = true; return c }); err != nil {
		t.Fatal(err)
	}
	if p = peek(true); !p["remind"].(bool) || !strings.Contains(p["message"].(string), "hook 关闭期间的新追加") {
		t.Fatalf("重新打开后应补上提醒: %+v", p)
	}
}

func TestInbox_RetractionIsToldOnce(t *testing.T) {
	srv := setupTestMCPServer(t)
	ctx := context.Background()
	cred := feedbackCred()
	wf := "wf-retract"
	id := queueInbox(t, srv, wf, "先改 A 模块")
	_ = inboxCall(t, srv, cred, "workflow_context", map[string]any{"action": "inbox", "workflow_id": wf})

	res, err := srv.store.RevokeQueuedFeedback(ctx, id)
	if err != nil || res == nil || !res.Delivered {
		t.Fatalf("读过之后撤回应标记 Delivered: %+v %v", res, err)
	}
	// 其他工具的回执里会有撤回提示
	r := inboxCall(t, srv, cred, "workflow_context", map[string]any{"action": "checkpoint_list", "workflow_id": wf})
	hints := hintBlocks(r)
	if len(hints) != 1 || !strings.Contains(hints[0], "被用户撤回") {
		t.Fatalf("应提示撤回: %+v", r.Content)
	}
	// 读取收件箱时明确告诉哪一条被撤回，且只告诉一次
	read := resultText(inboxCall(t, srv, cred, "workflow_context", map[string]any{"action": "inbox", "workflow_id": wf}))
	if !strings.Contains(read, "撤回") || !strings.Contains(read, fmt.Sprintf("#%d", id)) || !strings.Contains(read, "先改 A 模块") {
		t.Fatalf("读取应指明被撤回的追加:\n%s", read)
	}
	again := resultText(inboxCall(t, srv, cred, "workflow_context", map[string]any{"action": "inbox", "workflow_id": wf}))
	if strings.Contains(again, "撤回") {
		t.Fatalf("撤回只通知一次:\n%s", again)
	}
	r = inboxCall(t, srv, cred, "workflow_context", map[string]any{"action": "checkpoint_list", "workflow_id": wf})
	if len(hintBlocks(r)) != 0 {
		t.Fatalf("通知过后提示行应消失: %+v", r.Content)
	}
}

// 编辑器 hook 的轮询（source=hook）不是「智能体在调用 RelayMesh」：不能刷新智能体的最近活动，
// 否则智能体在干别的事时 Web 上的「最近一次交互」会一直是刚刚；安装检查（source=check）两样都不记。
func TestInbox_HookQueriesAreNotAgentActivity(t *testing.T) {
	srv := setupTestMCPServer(t)
	cred := feedbackCred()
	wf := "wf-hook-source"
	var mu sync.Mutex
	var events []string
	srv.onUpdate = func(eventType string, data any) {
		mu.Lock()
		defer mu.Unlock()
		if m, ok := data.(map[string]any); ok && m["workflow_id"] == wf {
			events = append(events, eventType)
		}
	}
	count := func() int { mu.Lock(); defer mu.Unlock(); return len(events) }
	peek := func(extra map[string]any) toolCallResult {
		args := map[string]any{"action": "inbox_peek", "workflow_id": wf}
		for k, v := range extra {
			args[k] = v
		}
		return inboxCall(t, srv, cred, "workflow_context", args)
	}

	// 安装检查：什么都不记
	peek(map[string]any{"source": "check"})
	if _, ok := srv.store.AgentLastActivity(wf); ok {
		t.Fatal("source=check 不应记成智能体活动")
	}
	if _, ok := srv.store.HookLastSeen(wf); ok {
		t.Fatal("source=check 不应记成 hook 查询")
	}

	// hook 轮询：只记 hook 最近一次查询，并通知 Web（第一次）
	peek(map[string]any{"source": "hook", "reminder": true})
	if _, ok := srv.store.AgentLastActivity(wf); ok {
		t.Fatal("source=hook 不应记成智能体活动")
	}
	if _, ok := srv.store.HookLastSeen(wf); !ok {
		t.Fatal("source=hook 应记下 hook 最近一次查询")
	}
	if count() != 1 || events[0] != inboxEvent {
		t.Fatalf("hook 第一次出现应通知 Web: %v", events)
	}
	peek(map[string]any{"source": "HOOK "}) // 大小写与空白不敏感；一分钟内再来不再通知
	if count() != 1 {
		t.Fatalf("一分钟内的重复 hook 查询不应再通知: %v", events)
	}

	// 没有 source（智能体自己调用）：照旧算智能体活动，也不算 hook
	hookBefore, _ := srv.store.HookLastSeen(wf)
	peek(nil)
	if _, ok := srv.store.AgentLastActivity(wf); !ok {
		t.Fatal("智能体自己调用 inbox_peek 应算活动")
	}
	if at, _ := srv.store.HookLastSeen(wf); !at.Equal(hookBefore) {
		t.Fatal("智能体自己调用不应改 hook 时间")
	}
	// 不认识的来源一律当作智能体自己调用，免得被用来伪装
	wf2 := "wf-hook-source-2"
	inboxCall(t, srv, cred, "workflow_context", map[string]any{"action": "inbox_peek", "workflow_id": wf2, "source": "whatever"})
	if _, ok := srv.store.AgentLastActivity(wf2); !ok {
		t.Fatal("不认识的 source 应按智能体调用处理")
	}
	if _, ok := srv.store.HookLastSeen(wf2); ok {
		t.Fatal("不认识的 source 不应记成 hook")
	}
	// source 只对 inbox_peek 有意义：别的动作带上它也算智能体活动
	wf3 := "wf-hook-source-3"
	inboxCall(t, srv, cred, "workflow_context", map[string]any{"action": "checkpoint_list", "workflow_id": wf3, "source": "hook"})
	if _, ok := srv.store.AgentLastActivity(wf3); !ok {
		t.Fatal("source 只对 inbox_peek 生效")
	}
	if _, ok := srv.store.HookLastSeen(wf3); ok {
		t.Fatal("别的动作带 source=hook 不应记成 hook")
	}
}

// hook 随 inbox_peek 汇报的运行情况（工具调用 / 一轮对话结束 / 对话编号）只在 source=hook 时才记：
// 智能体自己调用、安装检查带上这些字段都不算，免得被用来伪造「我还活着」。
func TestInbox_HookReportsAgentEvents(t *testing.T) {
	srv := setupTestMCPServer(t)
	cred := feedbackCred()
	ctx := context.Background()
	wf := "wf-agent-events"
	peek := func(wf string, extra map[string]any) {
		args := map[string]any{"action": "inbox_peek", "workflow_id": wf}
		for k, v := range extra {
			args[k] = v
		}
		inboxCall(t, srv, cred, "workflow_context", args)
	}

	peek(wf, map[string]any{"source": "hook", "agent_event": "tool", "conversation_id": "conv-1"})
	l := srv.store.AgentLinkOf(ctx, wf)
	if l.LastToolAt.IsZero() || l.ConversationID != "conv-1" || !l.StopAt.IsZero() {
		t.Fatalf("hook 汇报的工具事件应被记下: %+v", l)
	}
	if _, ok := srv.store.AgentLastActivity(wf); ok {
		t.Fatal("hook 汇报不应记成智能体自己的活动")
	}

	peek(wf, map[string]any{"source": "hook", "reminder": false, "agent_event": "stop", "agent_status": "error", "conversation_id": "conv-1"})
	if l = srv.store.AgentLinkOf(ctx, wf); l.StopAt.IsZero() || l.StopStatus != store.StopError {
		t.Fatalf("hook 汇报的出错收尾应被记下: %+v", l)
	}

	// 对话编号格式不对：事件照记，编号当没有（不覆盖已有的）。
	peek(wf, map[string]any{"source": "hook", "agent_event": "tool", "conversation_id": "bad id/../x"})
	if l = srv.store.AgentLinkOf(ctx, wf); l.ConversationID != "conv-1" {
		t.Fatalf("非法对话编号不应覆盖已有的: %+v", l)
	}

	// 智能体自己调用 / 安装检查带上这些字段：都不记。
	for i, src := range []string{"", "check", "whatever"} {
		other := fmt.Sprintf("wf-agent-events-%d", i)
		extra := map[string]any{"agent_event": "tool", "conversation_id": "conv-x"}
		if src != "" {
			extra["source"] = src
		}
		peek(other, extra)
		if got := srv.store.AgentLinkOf(ctx, other); got != (store.AgentLink{}) {
			t.Fatalf("source=%q 带的运行情况不应被记下: %+v", src, got)
		}
	}

	// 老版本 hook（不带这些字段）照常工作：只记 hook 查询时间，不留运行情况。
	old := "wf-agent-events-old"
	peek(old, map[string]any{"source": "hook", "reminder": true})
	if _, ok := srv.store.HookLastSeen(old); !ok {
		t.Fatal("老版本 hook 应照常记 hook 查询")
	}
	if got := srv.store.AgentLinkOf(ctx, old); got != (store.AgentLink{}) {
		t.Fatalf("没带运行情况就不该有记录: %+v", got)
	}
}

func TestInbox_ActivityAndBroadcasts(t *testing.T) {
	srv := setupTestMCPServer(t)
	cred := feedbackCred()
	wf := "wf-events"
	var mu sync.Mutex
	var events []string
	srv.onUpdate = func(eventType string, data any) {
		mu.Lock()
		defer mu.Unlock()
		if m, ok := data.(map[string]any); ok && m["workflow_id"] == wf {
			events = append(events, eventType)
		}
	}
	count := func() int { mu.Lock(); defer mu.Unlock(); return len(events) }

	if _, ok := srv.store.AgentLastActivity(wf); ok {
		t.Fatal("还没有任何交互时不应有活动记录")
	}
	before := time.Now().Add(-time.Second)
	_ = inboxCall(t, srv, cred, "workflow_context", map[string]any{"action": "checkpoint_list", "workflow_id": wf})
	if at, ok := srv.store.AgentLastActivity(wf); !ok || at.Before(before) {
		t.Fatalf("工具调用后应记下活动时间: %v %v", at, ok)
	}
	if count() != 0 {
		t.Fatalf("没有收件箱变化时不应广播: %v", events)
	}

	queueInbox(t, srv, wf, "广播测试")
	// 第一次出现提示行：写了「已提醒」时间戳，Web 要刷新；一分钟内再出现不再广播
	_ = inboxCall(t, srv, cred, "workflow_context", map[string]any{"action": "checkpoint_list", "workflow_id": wf})
	if count() != 1 || events[0] != inboxEvent {
		t.Fatalf("第一次提示应广播一次: %v", events)
	}
	_ = inboxCall(t, srv, cred, "workflow_context", map[string]any{"action": "checkpoint_list", "workflow_id": wf})
	if count() != 1 {
		t.Fatalf("一分钟内的重复提示不应再广播: %v", events)
	}
	_ = inboxCall(t, srv, cred, "workflow_context", map[string]any{"action": "inbox", "workflow_id": wf})
	if count() != 2 {
		t.Fatalf("读取应广播: %v", events)
	}
	_ = inboxCall(t, srv, cred, "workflow_context", map[string]any{"action": "inbox", "workflow_id": wf}) // 没有新内容
	if count() != 2 {
		t.Fatalf("没有变化的读取不应广播: %v", events)
	}
	_ = inboxCall(t, srv, cred, "workflow_context", map[string]any{"action": "inbox_ack", "workflow_id": wf})
	if count() != 3 {
		t.Fatalf("确认应广播: %v", events)
	}
	_ = inboxCall(t, srv, cred, "workflow_context", map[string]any{"action": "inbox_ack", "workflow_id": wf}) // 没有可确认的
	if count() != 3 {
		t.Fatalf("没有变化的确认不应广播: %v", events)
	}
}

func TestInbox_InteractiveFeedbackStillCarriesQueuedAndAcksRead(t *testing.T) {
	srv := setupTestMCPServer(t)
	cred := feedbackCred()
	wf := "wf-merge"
	queueInbox(t, srv, wf, "已经读过的一条")
	_ = inboxCall(t, srv, cred, "workflow_context", map[string]any{"action": "inbox", "workflow_id": wf})
	queueInbox(t, srv, wf, "还没读的一条")
	queueInbox(t, srv, wf, "还没读的第二条")

	res := inboxCall(t, srv, cred, "interactive_feedback", map[string]any{"workflow_id": wf, "summary": "阶段汇报", "commit": "abc1234 test"})
	text := resultText(res)
	for _, want := range []string{"还没读的一条", "还没读的第二条"} {
		if !strings.Contains(text, want) {
			t.Fatalf("秒回应带走排队中的追加 %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "已经读过的一条") {
		t.Fatalf("已读取的追加不应在秒回里重复出现:\n%s", text)
	}
	if len(hintBlocks(res)) != 0 {
		t.Fatalf("秒回之后收件箱已空，不应再有提示: %+v", res.Content)
	}
	p := firstJSON(t, inboxCall(t, srv, cred, "workflow_context", map[string]any{"action": "inbox_peek", "workflow_id": wf}))
	if p["unread"].(float64) != 0 || p["delivered"].(float64) != 0 {
		t.Fatalf("汇报之后收件箱应已清空（读过的被隐式确认）: %+v", p)
	}
}

func TestInbox_ImagesAreReachableByInboxSessionID(t *testing.T) {
	srv := setupTestMCPServer(t)
	cred := feedbackCred()
	ctx := context.Background()
	wf := "wf-img"

	// 项目目录不可写（不存在）：给出 get_session_image 的调用方式，用 inbox-<编号> 寻址
	q, err := srv.store.QueueWorkflowFeedback(ctx, store.QueueFeedbackInput{
		WorkflowID: wf, ResponseText: "看这张图", ProjectDirectory: "/nonexistent/dir/for/test",
		Images: []model.SessionImage{{Name: "a.png", Format: "png", Data: inboxTestPNG}},
	})
	if err != nil {
		t.Fatal(err)
	}
	read := resultText(inboxCall(t, srv, cred, "workflow_context", map[string]any{"action": "inbox", "workflow_id": wf}))
	sid := store.InboxSessionID(q.ID)
	if !strings.Contains(read, "看这张图") || !strings.Contains(read, "附件图片") || !strings.Contains(read, `session_id: "`+sid+`"`) {
		t.Fatalf("应给出按 inbox-<编号> 取图的方式:\n%s", read)
	}
	img := inboxCall(t, srv, cred, "get_session_image", map[string]any{"session_id": sid, "image_index": 0})
	hasImage := false
	for _, c := range img.Content {
		if c.Type == "image" && c.Data != "" {
			hasImage = true
		}
	}
	if img.IsError || !hasImage {
		t.Fatalf("get_session_image 应能按 %s 取到图片: %+v", sid, img)
	}
	// 越界与不存在
	bad := inboxCall(t, srv, cred, "get_session_image", map[string]any{"session_id": sid, "image_index": 3})
	if !bad.IsError {
		t.Fatal("图片序号越界应报错")
	}
	missing := inboxCall(t, srv, cred, "get_session_image", map[string]any{"session_id": "inbox-424242"})
	if !missing.IsError || !strings.Contains(resultText(missing), "not found") {
		t.Fatalf("不存在的追加应报 not found: %+v", missing)
	}

	// 项目目录可写：直接落地成本地文件，给出路径
	dir := t.TempDir()
	q2, err := srv.store.QueueWorkflowFeedback(ctx, store.QueueFeedbackInput{
		WorkflowID: wf, ResponseText: "再来一张", ProjectDirectory: dir,
		Images: []model.SessionImage{{Name: "b.png", Format: "png", Data: inboxTestPNG}},
	})
	if err != nil {
		t.Fatal(err)
	}
	read = resultText(inboxCall(t, srv, cred, "workflow_context", map[string]any{"action": "inbox", "workflow_id": wf}))
	want := filepath.Join(dir, ".cursor", "tmp", "images", store.InboxSessionID(q2.ID)+"_1.png")
	if !strings.Contains(read, want) {
		t.Fatalf("应落地成本地文件并给出路径 %s:\n%s", want, read)
	}
	if st, err := os.Stat(want); err != nil || st.Size() == 0 {
		t.Fatalf("本地图片文件应存在: %v", err)
	}
}

func TestInbox_LooseIDListAcceptsCommonShapes(t *testing.T) {
	cases := map[string][]uint{
		`[12,13]`:        {12, 13},
		`["12","#13"]`:   {12, 13},
		`12`:             {12},
		`"12,13"`:        {12, 13},
		`"12, #13；14"`:   nil, // 全角分号不在分隔符里：应报错而不是悄悄漏掉
		`["12，13"]`:      {12, 13},
		`[12.0]`:         {12},
		`null`:           nil,
		`[]`:             {},
		`"#7 8\n9"`:      {7, 8, 9},
		`["1", 2, "#3"]`: {1, 2, 3},
	}
	for in, want := range cases {
		var got looseIDList
		err := json.Unmarshal([]byte(in), &got)
		if want == nil && in != "null" {
			if err == nil {
				t.Fatalf("%s 应报错，得到 %v", in, got)
			}
			continue
		}
		if err != nil {
			t.Fatalf("%s: %v", in, err)
		}
		if fmt.Sprint([]uint(got)) != fmt.Sprint(want) && !(len(got) == 0 && len(want) == 0) {
			t.Fatalf("%s: 得到 %v，期望 %v", in, got, want)
		}
	}
	for _, bad := range []string{`[0]`, `[-1]`, `["abc"]`, `[1.5]`, `{"a":1}`} {
		var got looseIDList
		if err := json.Unmarshal([]byte(bad), &got); err == nil {
			t.Fatalf("%s 应报错，得到 %v", bad, got)
		}
	}
}

func TestInbox_ToolDefinitionsAdvertiseTheActions(t *testing.T) {
	defs := GetToolDefinitionsForPermissions(model.Permissions{Feedback: true})
	var wc *ToolDefinition
	for i := range defs {
		if defs[i].Name == "workflow_context" {
			wc = &defs[i]
		}
	}
	if wc == nil {
		t.Fatal("缺少 workflow_context")
	}
	schema := string(wc.InputSchema)
	for _, want := range []string{`"inbox"`, `"inbox_peek"`, `"inbox_ack"`, `"ids"`, `"reminder"`, `"recent"`} {
		if !strings.Contains(schema, want) {
			t.Fatalf("schema 缺少 %s", want)
		}
	}
	if !strings.Contains(wc.Description, "Inbox Actions") || !strings.Contains(wc.Description, `starting with "inbox:"`) {
		t.Fatal("工具描述应说明收件箱动作与 inbox: 提示行")
	}
	var parsed map[string]any
	if err := json.Unmarshal(wc.InputSchema, &parsed); err != nil {
		t.Fatalf("schema 应是合法 JSON: %v", err)
	}
}
