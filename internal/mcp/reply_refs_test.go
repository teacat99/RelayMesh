package mcp

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/teacat99/RelayMesh/v2/internal/model"
	"github.com/teacat99/RelayMesh/v2/internal/store"
)

func TestExtractQuotedRefs(t *testing.T) {
	cases := []struct {
		name string
		text string
		want []quotedRef
	}{
		{"没有引用", "普通的一句话，没有任何标记", nil},
		{"回复哈希与胶囊，按出现顺序", "见【回复 9f02517c】，再看【恢复 r8】", []quotedRef{{Kind: "reply", Hash: "9f02517c"}, {Kind: "capsule", Revision: 8}}},
		{"同一个哈希大小写不同只算一处", "【回复 9f02517c】和【回复 9F02517C】是同一条", []quotedRef{{Kind: "reply", Hash: "9f02517c"}}},
		{"容忍多写的 sess- 前缀与空白", "【回复 sess-9f02517c】【回复9f02517c 】", []quotedRef{{Kind: "reply", Hash: "9f02517c"}}},
		{"胶囊可以不写 r", "【恢复 8】", []quotedRef{{Kind: "capsule", Revision: 8}}},
		{"胶囊序号为 0 不算", "【恢复 r0】", nil},
		{"太短的哈希不算", "【回复 abc】", nil},
		{"非十六进制的不算", "【回复 你好】和【回复 zzzzzz】", nil},
		{"按位置排序而不是先找完一种再找另一种", "【恢复 r2】先于【回复 aaaa0001】", []quotedRef{{Kind: "capsule", Revision: 2}, {Kind: "reply", Hash: "aaaa0001"}}},
		{"其他括号不是引用", "【图 1】[回复 9f02517c] 回复 9f02517c", nil},
		{"确认项：Q 可写可不写，同一项只算一处", "【确认 Q5】【确认 5】【确认q5】", []quotedRef{{Kind: "confirm", Seq: 5}}},
		{"确认项序号为 0 或写法不合法不算", "【确认 Q0】【确认 你好】【确认】【确认 Qabc】", nil},
		{"确认项与回复、胶囊按位置排序", "【确认 Q2】先于【恢复 r1】先于【回复 aaaa0001】", []quotedRef{{Kind: "confirm", Seq: 2}, {Kind: "capsule", Revision: 1}, {Kind: "reply", Hash: "aaaa0001"}}},
	}
	for _, c := range cases {
		got := extractQuotedRefs(c.text)
		if len(got) != len(c.want) {
			t.Fatalf("%s：期望 %+v，实际 %+v", c.name, c.want, got)
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Fatalf("%s：第 %d 处期望 %+v，实际 %+v", c.name, i, c.want[i], got[i])
			}
		}
	}
}

// seedQuoteWorkflow 造出一个工作流：第 1 轮（已回复）、一条进度通知、第 2 轮（待回复）、一份恢复胶囊 r1，
// 另外在别的工作流里放一轮带「机密」字样的汇报，用来证明引用不会跨工作流窥探。
func seedQuoteWorkflow(t *testing.T, srv *Server, wf string) (round2 *model.FeedbackSession) {
	t.Helper()
	ctx := context.Background()
	r1, err := srv.store.CreateFeedbackSession(ctx, store.CreateSessionInput{
		SessionID: "sess-aaaa0001", WorkflowID: wf, ProjectDirectory: asyncTestProject,
		Title: "方案一", Summary: "第一轮的汇报正文，说明了数据库迁移的方案",
	})
	if err != nil {
		t.Fatalf("创建第 1 轮失败: %v", err)
	}
	if _, err := srv.store.SubmitFeedback(ctx, store.SubmitFeedbackInput{SessionID: r1.ID, ResponseText: "先别迁移，再想想"}); err != nil {
		t.Fatalf("回复第 1 轮失败: %v", err)
	}
	time.Sleep(3 * time.Millisecond)
	if _, err := srv.store.CreateNoticeSession(ctx, store.CreateSessionInput{
		SessionID: "sess-bbbb0002", WorkflowID: wf, ProjectDirectory: asyncTestProject,
		Title: "进度", Summary: "进度：迁移脚本写完了",
	}); err != nil {
		t.Fatalf("创建通知失败: %v", err)
	}
	time.Sleep(3 * time.Millisecond)
	round2, err = srv.store.CreateFeedbackSession(ctx, store.CreateSessionInput{
		SessionID: "sess-cccc0003", WorkflowID: wf, ProjectDirectory: asyncTestProject,
		Title: "方案二", Summary: "第二轮的汇报",
	})
	if err != nil {
		t.Fatalf("创建第 2 轮失败: %v", err)
	}
	if _, err := srv.store.CreateFeedbackSession(ctx, store.CreateSessionInput{
		SessionID: "sess-dddd0004", WorkflowID: wf + "-other", ProjectDirectory: asyncTestProject,
		Title: "机密标题", Summary: "别的工作流里的机密内容",
	}); err != nil {
		t.Fatalf("创建别的工作流失败: %v", err)
	}
	if err := srv.store.SaveCheckpoint(ctx, &model.WorkflowCheckpoint{
		WorkflowID: wf, Status: "submitted", ContentJSON: `{"objective":"把迁移做完并验证"}`,
	}); err != nil {
		t.Fatalf("保存胶囊失败: %v", err)
	}
	return round2
}

// 用户在追加里引用了回复 / 胶囊：智能体读收件箱时，正文后面要跟一块「=== 引用 ===」，
// 说清楚引用的是第几轮、什么标题、开头是什么；找不到的如实说找不到，别的工作流的内容一个字也不能露出来。
func TestInboxResolvesReplyAndCapsuleQuotes(t *testing.T) {
	srv := setupTestMCPServer(t)
	cred := feedbackCred()
	const wf = "wf-quote"
	seedQuoteWorkflow(t, srv, wf)

	// 两条追加各带一组引用：每条的说明块跟在它自己的正文后面（单条最多展开 5 处，所以分开写）
	queueInbox(t, srv, wf, "关于【回复 aaaa0001】先不动；【回复 bbbb0002】那条通知我看过了；【回复 cccc0003】请继续；"+
		"还有【回复 dddd0004】和【回复 eeee0005】")
	queueInbox(t, srv, wf, "恢复的时候对照【恢复 r1】，不要看【恢复 r9】")
	read := resultText(inboxCall(t, srv, cred, "workflow_context", map[string]any{"action": "inbox", "workflow_id": wf}))

	for _, want := range []string{
		"=== 引用 ===",
		"【回复 aaaa0001】第 1 轮汇报「方案一」", "session_id=sess-aaaa0001", "数据库迁移", "先别迁移，再想想",
		"【回复 bbbb0002】进度通知「进度」", "迁移脚本写完了",
		"【回复 cccc0003】第 2 轮汇报「方案二」", // 中间夹着通知，第二份汇报仍是第 2 轮
		"【回复 dddd0004】没有找到",
		"【回复 eeee0005】没有找到",
		"【恢复 r1】恢复胶囊 r1", "把迁移做完并验证",
		"【恢复 r9】没有找到",
		"get_session_history",
	} {
		if !strings.Contains(read, want) {
			t.Fatalf("读取结果缺少 %q:\n%s", want, read)
		}
	}
	for _, leaked := range []string{"机密标题", "机密内容"} {
		if strings.Contains(read, leaked) {
			t.Fatalf("不能把别的工作流里的内容（%q）带进引用说明:\n%s", leaked, read)
		}
	}
	// 引用说明紧跟在它所属那条追加的正文后面，而不是丢在整个回执的末尾
	if strings.Index(read, "先不动") > strings.Index(read, "=== 引用 ===") {
		t.Fatalf("引用说明应在正文之后:\n%s", read)
	}
}

// 阻塞式汇报的用户反馈同样要解析引用，且说明块放在正文与图片之间。
func TestFeedbackResultResolvesQuotes(t *testing.T) {
	srv := setupTestMCPServer(t)
	ctx := context.Background()
	const wf = "wf-quote-fb"
	round2 := seedQuoteWorkflow(t, srv, wf)

	done, err := srv.store.SubmitFeedback(ctx, store.SubmitFeedbackInput{SessionID: round2.ID, ResponseText: "按【回复 aaaa0001】那样做"})
	if err != nil {
		t.Fatalf("提交反馈失败: %v", err)
	}
	text := srv.formatFeedbackResultWithCtx(ctx, done)
	feedbackAt, quoteAt := strings.Index(text, "=== 用户反馈 ==="), strings.Index(text, "=== 引用 ===")
	if feedbackAt < 0 || quoteAt < feedbackAt {
		t.Fatalf("反馈正文之后应跟引用说明:\n%s", text)
	}
	if !strings.Contains(text, "第 1 轮汇报「方案一」") {
		t.Fatalf("引用说明缺少被引用的那一轮:\n%s", text)
	}
}

// 没有引用时不多出任何内容；引用太多时只展开前几处，并说明还有几处没展开。
func TestQuotedRefsBlockEmptyAndCapped(t *testing.T) {
	srv := setupTestMCPServer(t)
	ctx := context.Background()
	const wf = "wf-quote-cap"
	seedQuoteWorkflow(t, srv, wf)

	if got := srv.quotedRefsBlock(ctx, wf, "没有引用的一句话"); got != "" {
		t.Fatalf("没有引用时应为空，实际 %q", got)
	}
	if got := srv.quotedRefsBlock(ctx, "", "【回复 aaaa0001】"); got != "" {
		t.Fatalf("没有工作流时不解析，实际 %q", got)
	}

	var refs []string
	for i := 1; i <= 7; i++ {
		refs = append(refs, fmt.Sprintf("【回复 e%07x】", i))
	}
	got := srv.quotedRefsBlock(ctx, wf, strings.Join(refs, " "))
	if n := strings.Count(got, "没有找到"); n != maxQuotedRefs {
		t.Fatalf("应只展开 %d 处，实际 %d:\n%s", maxQuotedRefs, n, got)
	}
	if !strings.Contains(got, "另有 2 处引用未展开") {
		t.Fatalf("应提示还有 2 处没展开:\n%s", got)
	}
}

// seedQuoteConfirms 造出几个确认项：Q1 已答复（选 b，带备注）、Q2 待答复的计划、Q3 已忽略、Q4 已撤回；
// 另外在别的工作流里放 5 个带「机密」字样的确认项，用来证明引用不会跨工作流窥探（本工作流没有 Q5）。
func seedQuoteConfirms(t *testing.T, srv *Server, wf string) {
	t.Helper()
	ctx := context.Background()
	opts := []model.ConfirmOption{{ID: "a", Label: "先迁移", Recommended: true}, {ID: "b", Label: "再等等"}}
	task, err := srv.store.CreateWorkItem(ctx, store.WorkItemInput{WorkflowID: wf, Title: "数据库迁移"}, store.WorkActor{Kind: model.WorkActorAgent})
	if err != nil {
		t.Fatalf("创建任务失败: %v", err)
	}
	mk := func(workflow, kind, title, body string, taskSeq int) {
		t.Helper()
		if _, _, err := srv.store.CreateConfirmItem(ctx, store.ConfirmCreateInput{
			WorkflowID: workflow, Kind: kind, Title: title, Body: body, Options: opts, AllowNote: true, Risk: model.ConfirmRiskL1,
			TaskSeq: taskSeq, CreatedBy: model.ConfirmCreatedByAgent,
		}); err != nil {
			t.Fatalf("创建确认项 %q 失败: %v", title, err)
		}
	}
	mk(wf, model.ConfirmKindAsk, "要不要先迁移数据库", "迁移会锁表约两分钟，\n需要停写", 0)
	mk(wf, model.ConfirmKindPlan, "迁移计划", "第一步建影子表\n\n第二步回填", task.Item.Seq)
	mk(wf, model.ConfirmKindAsk, "是否需要截图", "", 0)
	mk(wf, model.ConfirmKindAsk, "已经不用问的事", "", 0)
	if _, err := srv.store.AnswerConfirmItem(ctx, wf, "Q1", store.ConfirmAnswerInput{OptionIDs: []string{"b"}, Note: "等周五再说", By: "human"}); err != nil {
		t.Fatalf("答复 Q1 失败: %v", err)
	}
	if _, err := srv.store.DismissConfirmItem(ctx, wf, "Q3", ""); err != nil {
		t.Fatalf("忽略 Q3 失败: %v", err)
	}
	if _, err := srv.store.WithdrawConfirmItem(ctx, wf, "Q4", true); err != nil {
		t.Fatalf("撤回 Q4 失败: %v", err)
	}
	for i := 1; i <= 5; i++ {
		mk(wf+"-other", model.ConfirmKindAsk, fmt.Sprintf("机密标题 %d", i), "别的工作流里的机密内容", 0)
	}
}

// 引用确认项（#132）：已答复的写出全部选项、用户选了哪个与备注；待答复的写明还在等；已忽略 / 已撤回如实说明；
// 找不到的如实说找不到，别的工作流里的确认项一个字也不能露出来；用户的话照常放在说明块的前面。
func TestQuotedRefsBlockResolvesConfirmItems(t *testing.T) {
	srv := setupTestMCPServer(t)
	ctx := context.Background()
	const wf = "wf-quote-confirm"
	seedQuoteConfirms(t, srv, wf)

	got := srv.quotedRefsBlock(ctx, wf, "按【确认 Q1】的结论来，【确认 Q2】先别批，【确认 Q3】【确认 Q4】都算了，【确认 Q5】呢")
	for _, want := range []string{
		"=== 引用 ===", "「【确认 …】」", "async_ask 的 get 带 ref",
		"【确认 Q1】提问「要不要先迁移数据库」", "用户已答复", "选项：a「先迁移」（推荐）、b「再等等」",
		"用户的答复：选 b「再等等」，备注「等周五再说」", "正文开头：「迁移会锁表约两分钟， 需要停写」",
		"【确认 Q2】计划确认「迁移计划」", "还在等用户答复", "正文开头：「第一步建影子表 第二步回填」",
		"【确认 Q3】提问「是否需要截图」", "用户已忽略",
		"【确认 Q4】提问「已经不用问的事」", "已撤回",
		"【确认 Q5】没有找到",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("引用说明缺少 %q:\n%s", want, got)
		}
	}
	// 待答复的没有「用户的答复」；没有正文的不写「正文开头」
	q2 := got[strings.Index(got, "【确认 Q2】"):strings.Index(got, "【确认 Q3】")]
	if strings.Contains(q2, "用户的答复") {
		t.Fatalf("待答复的确认项不该有用户的答复:\n%s", q2)
	}
	q3 := got[strings.Index(got, "【确认 Q3】"):strings.Index(got, "【确认 Q4】")]
	if strings.Contains(q3, "正文开头") || strings.Contains(q3, "用户的答复") {
		t.Fatalf("没有正文、没有答复的确认项不该写这两项:\n%s", q3)
	}
	if strings.Contains(got, "机密") {
		t.Fatalf("不能把别的工作流里的确认项带进引用说明:\n%s", got)
	}
}

// 自定义答复（#183）：引用已答复的确认项时，只写了自定义答复的写成「没有选预设选项」，选项 + 自定义并存的两者都写出来。
func TestQuotedRefsBlockShowsCustomAnswer(t *testing.T) {
	srv := setupTestMCPServer(t)
	ctx := context.Background()
	const wf = "wf-quote-custom"
	opts := []model.ConfirmOption{{ID: "a", Label: "先迁移"}, {ID: "b", Label: "再等等"}}
	for i, title := range []string{"只写自定义", "选项加自定义"} {
		if _, _, err := srv.store.CreateConfirmItem(ctx, store.ConfirmCreateInput{
			WorkflowID: wf, Kind: model.ConfirmKindAsk, Title: title, Options: opts, Multi: i == 1, Risk: model.ConfirmRiskL1,
			CreatedBy: model.ConfirmCreatedByAgent,
		}); err != nil {
			t.Fatalf("创建确认项 %q 失败: %v", title, err)
		}
	}
	if _, err := srv.store.AnswerConfirmItem(ctx, wf, "Q1", store.ConfirmAnswerInput{Custom: "先备份，再迁移", By: "human"}); err != nil {
		t.Fatalf("答复 Q1 失败: %v", err)
	}
	if _, err := srv.store.AnswerConfirmItem(ctx, wf, "Q2", store.ConfirmAnswerInput{OptionIDs: []string{"a"}, Custom: "另外通知运维", By: "human"}); err != nil {
		t.Fatalf("答复 Q2 失败: %v", err)
	}
	got := srv.quotedRefsBlock(ctx, wf, "【确认 Q1】和【确认 Q2】")
	for _, want := range []string{
		"用户的答复：没有选预设选项，自定义答复「先备份，再迁移」",
		"用户的答复：选 a「先迁移」，自定义答复「另外通知运维」",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("引用说明缺少 %q:\n%s", want, got)
		}
	}
}

// 用户在追加里引用确认项：智能体读收件箱时，正文后面跟着引用说明；回复、胶囊、确认项混着写时都能解析。
func TestInboxResolvesConfirmQuotes(t *testing.T) {
	srv := setupTestMCPServer(t)
	cred := feedbackCred()
	const wf = "wf-quote-confirm-inbox"
	seedQuoteWorkflow(t, srv, wf)
	seedQuoteConfirms(t, srv, wf)

	queueInbox(t, srv, wf, "接着【确认 Q1】说，再看【回复 aaaa0001】和【恢复 r1】")
	read := resultText(inboxCall(t, srv, cred, "workflow_context", map[string]any{"action": "inbox", "workflow_id": wf}))
	for _, want := range []string{
		"接着【确认 Q1】说", "=== 引用 ===",
		"【确认 Q1】提问「要不要先迁移数据库」", "用户的答复：选 b「再等等」，备注「等周五再说」",
		"【回复 aaaa0001】第 1 轮汇报「方案一」", "【恢复 r1】恢复胶囊 r1",
	} {
		if !strings.Contains(read, want) {
			t.Fatalf("读取结果缺少 %q:\n%s", want, read)
		}
	}
	if strings.Contains(read, "机密") {
		t.Fatalf("不能把别的工作流里的确认项带进来:\n%s", read)
	}
}
