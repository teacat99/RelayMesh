package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/teacat99/RelayMesh/v2/internal/config"
	"github.com/teacat99/RelayMesh/v2/internal/model"
	"github.com/teacat99/RelayMesh/v2/internal/store"
)

// askCall 经 HandleRPCRequest 调 async_ask，返回结果与是否出错；出错时结果文本就是错误说明。
func askCall(t *testing.T, srv *Server, args map[string]any) (map[string]any, string, bool) {
	t.Helper()
	params, err := json.Marshal(map[string]any{"name": "async_ask", "arguments": args})
	if err != nil {
		t.Fatal(err)
	}
	req := jsonRPCRequest{JSONRPC: "2.0", Method: "tools/call", Params: params}
	req.ID = json.RawMessage(`1`)
	out := srv.HandleRPCRequest(context.Background(), feedbackCred(), &req)
	if out.Response == nil || out.Response.Error != nil {
		t.Fatalf("async_ask 调用失败: %+v", out.Response)
	}
	res, ok := out.Response.Result.(toolCallResult)
	if !ok {
		t.Fatalf("结果类型不对: %T", out.Response.Result)
	}
	text := resultText(res)
	if res.IsError {
		return nil, text, true
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(res.Content[0].Text), &m); err != nil {
		t.Fatalf("第一块内容不是 JSON: %v\n%s", err, res.Content[0].Text)
	}
	return m, text, false
}

func askOK(t *testing.T, srv *Server, args map[string]any) map[string]any {
	t.Helper()
	m, text, isErr := askCall(t, srv, args)
	if isErr {
		t.Fatalf("async_ask 不应出错: %s", text)
	}
	return m
}

func askFail(t *testing.T, srv *Server, args map[string]any) string {
	t.Helper()
	_, text, isErr := askCall(t, srv, args)
	if !isErr {
		t.Fatalf("async_ask 应出错，却成功了: %s", text)
	}
	return text
}

func twoOptions() []map[string]any {
	return []map[string]any{{"label": "先做 A"}, {"label": "先做 B", "recommended": true}}
}

// 不可逆操作（L2）在服务端拒绝，而且拒绝排在其他校验之前：缺标题 / 缺选项的 L2 请求也只会看到这一条，
// 不会让智能体以为「补全参数就能提交」。
func TestAsyncAsk_L2IsRefusedBeforeAnythingElse(t *testing.T) {
	srv := setupTestMCPServer(t)
	wf := "wf-l2"
	for _, risk := range []string{"L2", "l2", " L2 "} {
		text := askFail(t, srv, map[string]any{"workflow_id": wf, "risk": risk})
		if !strings.Contains(text, "interactive_feedback") || !strings.Contains(text, "L2") {
			t.Fatalf("risk=%q 的拒绝说明应指向 interactive_feedback: %s", risk, text)
		}
		// 参数齐全的 L2 同样被拒
		askFail(t, srv, map[string]any{"workflow_id": wf, "title": "部署到生产？", "options": twoOptions(), "risk": risk})
	}
	if n, _ := srv.store.CountPendingConfirms(context.Background(), wf); n != 0 {
		t.Fatalf("被拒绝的请求不应留下任何确认项: %d", n)
	}
	// 其余写法的 risk 也不放行
	if text := askFail(t, srv, map[string]any{"workflow_id": wf, "title": "x", "options": twoOptions(), "risk": "L3"}); !strings.Contains(text, "L0 或 L1") {
		t.Fatalf("非法 risk 应说明可用取值: %s", text)
	}
}

func TestAsyncAsk_CreateAssignsOptionIDsAndReportsHowToReadTheAnswer(t *testing.T) {
	srv := setupTestMCPServer(t)
	wf := "wf-create"
	out := askOK(t, srv, map[string]any{
		"workflow_id": wf, "title": "先做哪个？", "body": "A 改动小，B 收益大。",
		"options": append(twoOptions(), map[string]any{"label": "都不做"}), "default_option": "b",
	})
	if out["ref"] != "Q1" || out["request_id"] != "Q1" || out["status"] != "pending" || out["created"] != true {
		t.Fatalf("创建结果不对: %+v", out)
	}
	if out["pending_count"].(float64) != 1 || out["default_option"] != "b" {
		t.Fatalf("应回报待处理数与默认选项: %+v", out)
	}
	if hint, _ := out["hint"].(string); !strings.Contains(hint, "inbox") || !strings.Contains(hint, "不是用户的答复") {
		t.Fatalf("应说明答复怎么读、默认值不是答复: %v", out["hint"])
	}
	got := askOK(t, srv, map[string]any{"workflow_id": wf, "action": "get", "ref": "Q1"})["item"].(map[string]any)
	var ids []string
	for _, o := range got["options"].([]any) {
		ids = append(ids, o.(map[string]any)["id"].(string))
	}
	if strings.Join(ids, ",") != "a,b,c" {
		t.Fatalf("没写 id 的选项应按位置补 a、b、c: %v", ids)
	}
	if got["risk"] != "L1" || got["kind"] != "ask" || got["created_by"] != model.ConfirmCreatedByAgent {
		t.Fatalf("默认 risk=L1、kind=ask、来源为智能体: %+v", got)
	}
	// 数字引用与大小写不敏感的 q 前缀都能取到同一条
	for _, ref := range []string{"1", "q1"} {
		if g := askOK(t, srv, map[string]any{"workflow_id": wf, "action": "get", "ref": ref})["item"].(map[string]any); g["ref"] != "Q1" {
			t.Fatalf("ref=%q 应取到 Q1: %+v", ref, g)
		}
	}
}

func TestAsyncAsk_PlanNeedsARealTaskAndBindsIt(t *testing.T) {
	srv := setupTestMCPServer(t)
	wf := "wf-plan"
	args := map[string]any{"workflow_id": wf, "kind": "plan", "title": "重构存储层的计划", "body": "1. 抽接口\n2. 迁移", "options": []map[string]any{{"label": "批准"}, {"label": "要修改"}}}
	if text := askFail(t, srv, args); !strings.Contains(text, "task_id") {
		t.Fatalf("没有 task_id 的计划应提示先建任务: %s", text)
	}
	args["task_id"] = 7
	if text := askFail(t, srv, args); !strings.Contains(text, "#7") {
		t.Fatalf("不存在的任务应指出是哪一号: %s", text)
	}
	task, err := srv.store.CreateWorkItem(context.Background(), store.WorkItemInput{WorkflowID: wf, Title: "重构存储层"}, store.WorkActor{Kind: model.WorkActorAgent})
	if err != nil {
		t.Fatal(err)
	}
	args["task_id"] = task.Item.Seq
	out := askOK(t, srv, args)
	got := askOK(t, srv, map[string]any{"workflow_id": wf, "action": "get", "ref": out["ref"]})["item"].(map[string]any)
	if got["kind"] != "plan" || int(got["task_seq"].(float64)) != task.Item.Seq {
		t.Fatalf("计划应绑定任务: %+v", got)
	}
	// task_id 也接受字符串写法（智能体常把数字写成字符串）
	args["title"] = "第二份计划"
	args["task_id"] = fmt.Sprint(task.Item.Seq)
	askOK(t, srv, args)
	// 只有 ask / plan 两种 kind 能由智能体创建；decision 是文档围栏同步出来的
	if text := askFail(t, srv, map[string]any{"workflow_id": wf, "kind": "decision", "title": "x", "options": twoOptions()}); !strings.Contains(text, "ask 或 plan") {
		t.Fatalf("智能体不能创建 decision 类型: %s", text)
	}
}

func TestAsyncAsk_IdenticalPendingQuestionIsDeduplicated(t *testing.T) {
	srv := setupTestMCPServer(t)
	wf := "wf-dedupe"
	args := map[string]any{"workflow_id": wf, "title": "要不要升级依赖？", "options": twoOptions()}
	first := askOK(t, srv, args)
	again := askOK(t, srv, args)
	if again["created"] != false || again["ref"] != first["ref"] || again["pending_count"].(float64) != 1 {
		t.Fatalf("重试应返回已有那条而不是新建: first=%+v again=%+v", first, again)
	}
	if note, _ := again["note"].(string); !strings.Contains(note, "没有重复创建") {
		t.Fatalf("去重应有说明: %+v", again)
	}
	// 选项不同就是另一个问题
	other := askOK(t, srv, map[string]any{"workflow_id": wf, "title": "要不要升级依赖？", "options": []map[string]any{{"label": "升"}, {"label": "不升"}}})
	if other["created"] != true || other["ref"] != "Q2" {
		t.Fatalf("选项不同应新建: %+v", other)
	}
	// 已处理的同一问题可以再问（不再算重复）
	if _, err := srv.store.AnswerConfirmItem(context.Background(), wf, "Q1", store.ConfirmAnswerInput{OptionIDs: []string{"a"}, By: "human"}); err != nil {
		t.Fatal(err)
	}
	if again := askOK(t, srv, args); again["created"] != true || again["ref"] != "Q3" {
		t.Fatalf("已答复后再问同一题应新建: %+v", again)
	}
}

func TestAsyncAsk_RateLimitAndMergeHint(t *testing.T) {
	srv := setupTestMCPServer(t)
	wf := "wf-rate"
	ask := func(i int) (map[string]any, string, bool) {
		return askCall(t, srv, map[string]any{"workflow_id": wf, "title": fmt.Sprintf("第 %d 个问题", i), "options": twoOptions()})
	}
	for i := 1; i <= model.ConfirmCreateRateHour; i++ {
		out, text, isErr := ask(i)
		if isErr {
			t.Fatalf("第 %d 条不应被限流: %s", i, text)
		}
		_, hasMerge := out["merge_hint"]
		if want := i > model.ConfirmPendingWarn; hasMerge != want {
			t.Fatalf("第 %d 条：merge_hint 出现=%v，应为 %v（待处理超过 %d 条才提示合并）", i, hasMerge, want, model.ConfirmPendingWarn)
		}
	}
	_, text, isErr := ask(model.ConfirmCreateRateHour + 1)
	if !isErr || !strings.Contains(text, "一小时内") || !strings.Contains(text, "合并") {
		t.Fatalf("超过每小时上限应拒绝并建议合并: err=%v %s", isErr, text)
	}
	// 同一问题的重试不受限流影响（命中去重直接返回已有项）
	if out, _, isErr := ask(1); isErr || out["created"] != false {
		t.Fatalf("去重先于限流: %+v err=%v", out, isErr)
	}
	// 限流按工作流计：别的工作流不受影响
	askOK(t, srv, map[string]any{"workflow_id": "wf-rate-other", "title": "别处的问题", "options": twoOptions()})
}

func TestAsyncAsk_ListGetAreScopedToTheWorkflow(t *testing.T) {
	srv := setupTestMCPServer(t)
	askOK(t, srv, map[string]any{"workflow_id": "wf-a", "title": "A 的问题", "options": twoOptions()})
	askOK(t, srv, map[string]any{"workflow_id": "wf-a", "title": "A 的第二个问题", "options": twoOptions()})
	askOK(t, srv, map[string]any{"workflow_id": "wf-b", "title": "B 的问题", "options": twoOptions()})

	// B 里的 Q1 是 B 自己的，不是 A 的 Q1
	b1 := askOK(t, srv, map[string]any{"workflow_id": "wf-b", "action": "get", "ref": "Q1"})["item"].(map[string]any)
	if b1["title"] != "B 的问题" {
		t.Fatalf("应只在本工作流里按序号查: %+v", b1)
	}
	// B 里没有 Q2：不会漏出 A 的 Q2
	if text := askFail(t, srv, map[string]any{"workflow_id": "wf-b", "action": "get", "ref": "Q2"}); !strings.Contains(text, "not_found") {
		t.Fatalf("跨工作流取不到应是「找不到」: %s", text)
	}
	// 用数据库 id 也不能越过工作流边界：A 的第二条在库里的 id 是 2，在 B 里取这个 id 必须失败
	aItem := askOK(t, srv, map[string]any{"workflow_id": "wf-a", "action": "get", "ref": "Q2"})["item"].(map[string]any)
	if aID := int(aItem["id"].(float64)); aID > 0 {
		askFail(t, srv, map[string]any{"workflow_id": "wf-b", "action": "get", "ref": fmt.Sprint(aID)})
	}
	list := askOK(t, srv, map[string]any{"workflow_id": "wf-a", "action": "list"})
	if list["count"].(float64) != 2 {
		t.Fatalf("A 应有 2 条待处理: %+v", list)
	}
	items := list["items"].([]any)
	if items[0].(map[string]any)["ref"] != "Q1" {
		t.Fatalf("待处理列表应旧的在前: %+v", items)
	}
	// 作答后默认列表不再含它，status=all 仍含
	if _, err := srv.store.AnswerConfirmItem(context.Background(), "wf-a", "Q1", store.ConfirmAnswerInput{OptionIDs: []string{"a"}, By: "human"}); err != nil {
		t.Fatal(err)
	}
	if n := askOK(t, srv, map[string]any{"workflow_id": "wf-a", "action": "list"})["count"].(float64); n != 1 {
		t.Fatalf("作答后待处理应剩 1 条: %v", n)
	}
	if n := askOK(t, srv, map[string]any{"workflow_id": "wf-a", "action": "list", "status": "all"})["count"].(float64); n != 2 {
		t.Fatalf("status=all 应含已答复的: %v", n)
	}
	if n := askOK(t, srv, map[string]any{"workflow_id": "wf-a", "action": "list", "status": "answered"})["count"].(float64); n != 1 {
		t.Fatalf("status=answered 应只有 1 条: %v", n)
	}
	got := askOK(t, srv, map[string]any{"workflow_id": "wf-a", "action": "get", "ref": "Q1"})["item"].(map[string]any)
	if got["status"] != "answered" || got["answer"] == nil {
		t.Fatalf("get 应带上答复: %+v", got)
	}
}

func TestAsyncAsk_WithdrawOnlyAgentCreatedPendingItems(t *testing.T) {
	srv := setupTestMCPServer(t)
	ctx := context.Background()
	wf := "wf-withdraw"
	askOK(t, srv, map[string]any{"workflow_id": wf, "title": "我提的问题", "options": twoOptions()})
	out := askOK(t, srv, map[string]any{"workflow_id": wf, "action": "withdraw", "ref": "Q1"})["item"].(map[string]any)
	if out["status"] != "withdrawn" {
		t.Fatalf("撤回后应为 withdrawn: %+v", out)
	}
	// 撤回后不能再作答；列表里也不再待处理
	if _, err := srv.store.AnswerConfirmItem(ctx, wf, "Q1", store.ConfirmAnswerInput{OptionIDs: []string{"a"}, By: "human"}); err == nil {
		t.Fatal("已撤回的确认项不应还能作答")
	}
	if n := askOK(t, srv, map[string]any{"workflow_id": wf, "action": "list"})["count"].(float64); n != 0 {
		t.Fatalf("撤回后待处理应为 0: %v", n)
	}
	// 已答复的不能撤回
	askOK(t, srv, map[string]any{"workflow_id": wf, "title": "第二个", "options": twoOptions()})
	if _, err := srv.store.AnswerConfirmItem(ctx, wf, "Q2", store.ConfirmAnswerInput{OptionIDs: []string{"a"}, By: "human"}); err != nil {
		t.Fatal(err)
	}
	askFail(t, srv, map[string]any{"workflow_id": wf, "action": "withdraw", "ref": "Q2"})
	// 不是智能体创建的（这里模拟用户 / 文档来源）不能被智能体撤回
	if _, _, err := srv.store.CreateConfirmItem(ctx, store.ConfirmCreateInput{
		WorkflowID: wf, Title: "用户建的", Options: []model.ConfirmOption{{ID: "a", Label: "甲"}, {ID: "b", Label: "乙"}}, CreatedBy: "human",
	}); err != nil {
		t.Fatal(err)
	}
	askFail(t, srv, map[string]any{"workflow_id": wf, "action": "withdraw", "ref": "Q3"})
	// 缺 ref
	askFail(t, srv, map[string]any{"workflow_id": wf, "action": "withdraw"})
	askFail(t, srv, map[string]any{"workflow_id": wf, "action": "get"})
}

func TestAsyncAsk_BadCallsExplainThemselves(t *testing.T) {
	srv := setupTestMCPServer(t)
	if text := askFail(t, srv, map[string]any{"title": "x", "options": twoOptions()}); !strings.Contains(text, "workflow_id") {
		t.Fatalf("缺 workflow_id: %s", text)
	}
	if text := askFail(t, srv, map[string]any{"workflow_id": "wf", "action": "frobnicate"}); !strings.Contains(text, "create / get / list / withdraw") {
		t.Fatalf("未知 action 应列出可用的: %s", text)
	}
	if text := askFail(t, srv, map[string]any{"workflow_id": "wf", "title": "只有一个选项", "options": []map[string]any{{"label": "唯一"}}}); !strings.Contains(text, "选项") {
		t.Fatalf("选项不足: %s", text)
	}
	if text := askFail(t, srv, map[string]any{"workflow_id": "wf", "title": "默认值不存在", "options": twoOptions(), "default_option": "zzz"}); !strings.Contains(text, "default_option") {
		t.Fatalf("default_option 必须是某个选项: %s", text)
	}
	if text := askFail(t, srv, map[string]any{"workflow_id": "wf", "title": "绑定到不存在的文档", "options": twoOptions(), "doc_path": "nope/missing.md"}); !strings.Contains(text, "doc_path") {
		t.Fatalf("doc_path 无效应点名: %s", text)
	}
}

// 用户作答后，答复经收件箱送达：回执提示行、inbox 读取、inbox_peek 都要认得它是「确认项答复」，
// 而不是「追加指令」；interactive_feedback 的秒回不会把它吞掉（那是 store 层测的，这里验证 MCP 一侧的措辞）。
func TestAsyncAsk_AnswerReachesTheAgentThroughTheInbox(t *testing.T) {
	srv := setupTestMCPServer(t)
	cred := feedbackCred()
	wf := "wf-answer-inbox"
	askOK(t, srv, map[string]any{"workflow_id": wf, "title": "先做哪个？", "options": twoOptions(), "allow_note": true})
	if _, err := srv.store.AnswerConfirmItem(context.Background(), wf, "Q1", store.ConfirmAnswerInput{OptionIDs: []string{"b"}, Note: "B 优先，A 下周", By: "human"}); err != nil {
		t.Fatal(err)
	}

	peek := firstJSON(t, inboxCall(t, srv, cred, "workflow_context", map[string]any{"action": "inbox_peek", "workflow_id": wf}))
	if peek["unread"].(float64) != 1 || peek["unread_answers"].(float64) != 1 {
		t.Fatalf("peek 应报 1 条未读且都是答复: %+v", peek)
	}
	// 任意带 workflow_id 的调用，回执末尾都带 inbox: 提示，措辞按「确认项答复」
	res := inboxCall(t, srv, cred, "workflow_context", map[string]any{"action": "checkpoint_list", "workflow_id": wf})
	hints := hintBlocks(res)
	if len(hints) != 1 {
		t.Fatalf("应恰有一块 inbox: 提示: %+v", res.Content)
	}
	for _, want := range []string{"1 条确认项答复", "答复了你的确认项", `"inbox"`, wf} {
		if !strings.Contains(hints[0], want) {
			t.Fatalf("提示缺少 %q:\n%s", want, hints[0])
		}
	}
	if strings.Contains(hints[0], "追加指令还没读") {
		t.Fatalf("纯答复的提示不该说成追加指令:\n%s", hints[0])
	}
	// 读取全文：标签是「确认项答复」，正文是结构化的一句话
	read := resultText(inboxCall(t, srv, cred, "workflow_context", map[string]any{"action": "inbox", "workflow_id": wf}))
	for _, want := range []string{"=== 确认项答复 #", "Q1", "先做 B", "B 优先，A 下周"} {
		if !strings.Contains(read, want) {
			t.Fatalf("读取结果缺少 %q:\n%s", want, read)
		}
	}
	if strings.Contains(read, "=== 追加 #") {
		t.Fatalf("答复不该套用「追加」的标题:\n%s", read)
	}
	// 读过之后不再提示；get 仍能查到答复
	if again := inboxCall(t, srv, cred, "async_ask", map[string]any{"workflow_id": wf, "action": "get", "ref": "Q1"}); len(hintBlocks(again)) != 0 {
		t.Fatalf("读过之后不应再提示: %+v", again.Content)
	}
}

// 追加指令与答复混在一起时，提示行分开数，且两者都提醒到。
func TestAsyncAsk_MixedInboxHintCountsBothKinds(t *testing.T) {
	srv := setupTestMCPServer(t)
	cred := feedbackCred()
	wf := "wf-mixed"
	askOK(t, srv, map[string]any{"workflow_id": wf, "title": "选一个", "options": twoOptions()})
	queueInbox(t, srv, wf, "顺便把日志级别调低")
	if _, err := srv.store.AnswerConfirmItem(context.Background(), wf, "Q1", store.ConfirmAnswerInput{OptionIDs: []string{"a"}, By: "human"}); err != nil {
		t.Fatal(err)
	}
	hints := hintBlocks(inboxCall(t, srv, cred, "workflow_context", map[string]any{"action": "checkpoint_list", "workflow_id": wf}))
	if len(hints) != 1 || !strings.Contains(hints[0], "2 条未读（1 条确认项答复、1 条追加指令）") || !strings.Contains(hints[0], "另有用户的追加指令") {
		t.Fatalf("混合提示不对: %+v", hints)
	}
	stop := formatInboxStopMessage(wf, store.InboxSummary{Unread: 2, UnreadAnswers: 1})
	if !strings.Contains(stop, "2 条未读（1 条确认项答复、1 条追加指令）") {
		t.Fatalf("收尾拦截文案也要分开数: %s", stop)
	}
	// 没有答复时，两处文案与原来逐字相同（INV-2）
	if got := formatInboxStopMessage(wf, store.InboxSummary{Unread: 2}); !strings.Contains(got, "用户有 2 条追加指令你还没读到") {
		t.Fatalf("没有答复时应保持原措辞: %s", got)
	}
}

// 读文档时决策卡带上确认项的编号、状态与答复；导出的差量哈希随作答变化，
// 否则智能体带着旧哈希来，会漏掉「决策刚被作答」的文档。
func TestAsyncAsk_ProjectDocsCarryDecisionAnswers(t *testing.T) {
	srv := setupTestMCPServer(t)
	ctx := context.Background()
	wf := "wf-doc-decision"
	body := "# 方案\n\n```decision\n{\"id\":\"db\",\"title\":\"用哪个数据库\",\"note\":true,\"options\":[{\"id\":\"sqlite\",\"label\":\"SQLite\",\"recommended\":true},{\"id\":\"pg\",\"label\":\"PostgreSQL\"}]}\n```\n"
	save, _ := json.Marshal(map[string]any{"workflow_id": wf, "action": "save", "path": "design/db.md", "body": body})
	if _, err := srv.handleProjectDocs(ctx, save); err != nil {
		t.Fatal(err)
	}
	get := func() map[string]any {
		a, _ := json.Marshal(map[string]any{"workflow_id": wf, "action": "get", "path": "design/db.md"})
		out, err := srv.handleProjectDocs(ctx, a)
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := json.Marshal(out)
		var m map[string]any
		_ = json.Unmarshal(raw, &m)
		return m
	}
	decisionOf := func(m map[string]any) map[string]any {
		ds, _ := m["decisions"].([]any)
		if len(ds) != 1 {
			t.Fatalf("应解析出 1 张决策卡: %+v", m["decisions"])
		}
		return ds[0].(map[string]any)
	}
	before := decisionOf(get())
	if before["confirm_ref"] != "Q1" || before["status"] != "pending" {
		t.Fatalf("保存文档后决策卡应对应 Q1 且待决: %+v", before)
	}
	// 导出一次，记下哈希
	exportHash := func(known map[string]string) (string, int) {
		a, _ := json.Marshal(map[string]any{"workflow_id": wf, "action": "export", "known": known})
		out, err := srv.handleProjectDocs(ctx, a)
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := json.Marshal(out)
		var m struct {
			Files []struct {
				Path, Hash, Content string
			} `json:"files"`
		}
		_ = json.Unmarshal(raw, &m)
		if len(m.Files) == 0 {
			return "", 0
		}
		return m.Files[0].Hash, len(m.Files)
	}
	h1, n := exportHash(nil)
	if n != 1 || h1 == "" {
		t.Fatalf("首次导出应含这份文档: n=%d", n)
	}
	if _, skipped := exportHash(map[string]string{"design/db.md": h1}); skipped != 0 {
		t.Fatal("哈希没变时差量导出应跳过这份文档")
	}
	// 用户作答（走 store，与 REST 同一条路径）
	if _, err := srv.store.AnswerConfirmItem(ctx, wf, "Q1", store.ConfirmAnswerInput{OptionIDs: []string{"pg"}, Note: "团队只会用它", By: "human"}); err != nil {
		t.Fatal(err)
	}
	after := decisionOf(get())
	if after["status"] != "answered" {
		t.Fatalf("作答后决策卡应为 answered: %+v", after)
	}
	if ans, _ := after["answer"].(map[string]any); ans == nil || !strings.Contains(fmt.Sprint(ans), "pg") || !strings.Contains(fmt.Sprint(ans), "团队只会用它") {
		t.Fatalf("决策卡应带上答复（选项与备注）: %+v", after)
	}
	h2, n := exportHash(map[string]string{"design/db.md": h1})
	if n != 1 || h2 == h1 {
		t.Fatalf("作答后即便正文没变，带旧哈希的差量导出也必须带上这份文档: n=%d h1==h2:%v", n, h2 == h1)
	}
	if _, skipped := exportHash(map[string]string{"design/db.md": h2}); skipped != 0 {
		t.Fatal("拿到新哈希后再导出应跳过")
	}
}

// async_ask 创建 / 去重命中 / 撤回时向界面广播 confirm_updated；去重命中不重复广播。
func TestAsyncAsk_BroadcastsConfirmUpdated(t *testing.T) {
	st, err := store.New(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var events []string
	srv := NewServer(&config.Config{ProjectID: "p", FeedbackTimeoutSeconds: 1}, st, func(kind string, data any) {
		if kind != "confirm_updated" {
			return
		}
		m, _ := data.(map[string]any)
		mu.Lock()
		// 第三段是事件里带的「这个工作流此刻待处理的条数」（侧栏会话卡直接据此校准，#159）
		events = append(events, fmt.Sprintf("%v:%v:%v", m["ref"], m["status"], m["pending_count"]))
		mu.Unlock()
	})
	wf := "wf-sse"
	args := map[string]any{"workflow_id": wf, "title": "要广播吗", "options": twoOptions()}
	askOK(t, srv, args)
	askOK(t, srv, args) // 去重命中，不应再广播
	askOK(t, srv, map[string]any{"workflow_id": wf, "action": "withdraw", "ref": "Q1"})
	mu.Lock()
	defer mu.Unlock()
	if strings.Join(events, ",") != "Q1:pending:1,Q1:withdrawn:0" {
		t.Fatalf("广播序列不对: %v", events)
	}
}

func TestAsyncAsk_ToolIsAdvertisedAndPermissioned(t *testing.T) {
	if !(model.Permissions{Feedback: true}).AllowsTool("async_ask") || !(model.Permissions{Sessions: true}).AllowsTool("async_ask") {
		t.Fatal("反馈 / 会话权限应能用 async_ask")
	}
	if (model.Permissions{Terminal: true}).AllowsTool("async_ask") || (model.Permissions{Execute: true}).AllowsTool("async_ask") {
		t.Fatal("纯终端 / 执行权限不应能用 async_ask")
	}
	defs := GetToolDefinitionsForPermissions(model.Permissions{Feedback: true})
	var found *ToolDefinition
	for i := range defs {
		if defs[i].Name == "async_ask" {
			found = &defs[i]
		}
	}
	if found == nil {
		t.Fatal("tools/list 应包含 async_ask")
	}
	for _, want := range []string{"interactive_feedback", "risk=L2", "default_option", "workflow_context(action:\"inbox\")", "30 new items per hour"} {
		if !strings.Contains(found.Description, want) {
			t.Fatalf("工具说明缺少 %q:\n%s", want, found.Description)
		}
	}
	raw, _ := json.Marshal(found.InputSchema)
	for _, want := range []string{"create", "withdraw", "plan", "task_id", "workflow_id"} {
		if !strings.Contains(string(raw), want) {
			t.Fatalf("入参 schema 缺少 %q: %s", want, raw)
		}
	}
}
