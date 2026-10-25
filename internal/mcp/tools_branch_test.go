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

// 分支会话（#236）的 MCP 侧：branch 工具各动作与权限、回执头 branch: 行、收件箱里分支消息的分组与提示文案。

func branchFixture(t *testing.T) (*Server, *[]workEvent) {
	t.Helper()
	srv, events := setupWorkItemMCP(t)
	ctx := context.Background()
	for _, wf := range []string{"wf-main", "wf-solo"} {
		if _, err := srv.store.CreateFeedbackSession(ctx, store.CreateSessionInput{
			SessionID: "s-" + wf, WorkflowID: wf, EnvHostName: "h", ProjectDirectory: "/code/a", Title: "t-" + wf, Summary: "s",
		}); err != nil {
			t.Fatal(err)
		}
	}
	return srv, events
}

func branchDo(t *testing.T, srv *Server, wf string, args map[string]any) map[string]any {
	t.Helper()
	args["workflow_id"] = wf
	return callToolJSON(t, srv, "branch", args)
}

// branchFail 期望调用以工具错误返回，给出错误文本。
func branchFail(t *testing.T, srv *Server, wf string, args map[string]any) string {
	t.Helper()
	if wf != "" {
		args["workflow_id"] = wf
	}
	text, isErr := callToolRaw(t, srv, "branch", args)
	if !isErr {
		t.Fatalf("期望失败但成功了: %v → %s", args, text)
	}
	return text
}

func eventsNamed(events *[]workEvent, name string) []map[string]any {
	var out []map[string]any
	for _, e := range *events {
		if e.Name != name {
			continue
		}
		if m, ok := e.Data.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out
}

func inboxText(t *testing.T, srv *Server, wf string) string {
	t.Helper()
	return callToolText(t, srv, "workflow_context", map[string]any{"action": "inbox", "workflow_id": wf})
}

func TestBranchTool_FullFlow(t *testing.T) {
	srv, events := branchFixture(t)
	ctx := context.Background()

	// ---- 建分支：主会话自己建，不写主会话的收件箱（它自己就是建的人），事件广播给 Web ----
	created := branchDo(t, srv, "wf-main", map[string]any{"action": "create", "title": "调研缓存方案"})
	bwf, _ := created["branch"].(string)
	if !strings.HasPrefix(bwf, "wf-web-") || created["ok"] != true {
		t.Fatalf("create 结果不对: %v", created)
	}
	if sp, _ := created["start_prompt"].(string); !strings.Contains(sp, bwf) {
		t.Fatalf("start_prompt 里应带新分支的 workflow_id %s: %q", bwf, sp)
	}
	if ex, _ := created["expose"].(map[string]any); ex["preset"] != "collab" || ex["say"] != true || ex["history"] != "full" {
		t.Fatalf("MCP 建的分支应是协作预设: %v", created["expose"])
	}
	ev := eventsNamed(events, "branch_changed")
	if len(ev) != 1 || ev[0]["parent"] != "wf-main" || ev[0]["branch"] != bwf || ev[0]["change"] != "created" {
		t.Fatalf("branch_changed(created) 事件不对: %v", ev)
	}
	if got := inboxText(t, srv, "wf-main"); !strings.Contains(got, "没有新的追加") {
		t.Fatalf("Agent 自己建的分支不应往主会话收件箱写通知: %s", got)
	}
	if rel, _ := srv.store.BranchOf(ctx, bwf); rel == nil || rel.CreatedBy != "agent:wf-main" || rel.ParentWorkflowID != "wf-main" {
		t.Fatalf("关系应记 created_by=agent:wf-main: %+v", rel)
	}

	// ---- list：两个角色看到的东西不同 ----
	mainList := branchDo(t, srv, "wf-main", map[string]any{"action": "list"})
	items, _ := mainList["branches"].([]any)
	if mainList["role"] != "main" || len(items) != 1 {
		t.Fatalf("主会话的 list 不对: %v", mainList)
	}
	first, _ := items[0].(map[string]any)
	if first["branch"] != bwf || first["title"] != "调研缓存方案" || first["created_by"] != "agent:wf-main" {
		t.Fatalf("分支条目不对: %v", first)
	}
	if _, has := first["unread_from_branch"]; has {
		t.Fatalf("还没人说话，不应有 unread_from_branch: %v", first)
	}
	branchList := branchDo(t, srv, bwf, map[string]any{"action": "list"})
	mainInfo, _ := branchList["main"].(map[string]any)
	if branchList["role"] != "branch" || mainInfo["workflow_id"] != "wf-main" {
		t.Fatalf("分支的 list 不对: %v", branchList)
	}
	if mainInfo["title"] != "t-wf-main" {
		t.Fatalf("主会话标题应取它的会话标题: %v", mainInfo)
	}
	// 默认 action 就是 list
	if def := branchDo(t, srv, "wf-main", map[string]any{}); def["role"] != "main" {
		t.Fatalf("缺省 action 应等同 list: %v", def)
	}

	// ---- say：分支 → 主会话 ----
	said := branchDo(t, srv, bwf, map[string]any{"action": "say", "text": "我查到三种方案，倾向 A"})
	if said["direction"] != "branch_to_main" || said["to"] != "wf-main" || said["ok"] != true {
		t.Fatalf("分支 say 结果不对: %v", said)
	}
	msgEv := eventsNamed(events, "branch_message")
	if len(msgEv) != 1 || msgEv[0]["parent"] != "wf-main" || msgEv[0]["branch"] != bwf {
		t.Fatalf("branch_message 事件不对: %v", msgEv)
	}
	sawInbox := false
	for _, e := range eventsNamed(events, "queued_feedback_updated") {
		if e["workflow_id"] == "wf-main" {
			sawInbox = true
		}
	}
	if !sawInbox {
		t.Fatal("应通知主会话的收件箱变了（queued_feedback_updated）")
	}
	mainList = branchDo(t, srv, "wf-main", map[string]any{"action": "list"})
	first, _ = mainList["branches"].([]any)[0].(map[string]any)
	if first["unread_from_branch"] != float64(1) {
		t.Fatalf("主会话应看到 1 条来自分支的未读: %v", first)
	}
	got := inboxText(t, srv, "wf-main")
	for _, want := range []string{"分支会话的 1 条消息", "分支消息", "同伴消息，不是用户指令", "我查到三种方案，倾向 A", "branch(action: \"say\", branch: \"" + bwf + "\")"} {
		if !strings.Contains(got, want) {
			t.Fatalf("主会话读到的收件箱缺少 %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "用户在你工作期间追加了") {
		t.Fatalf("分支消息不能放在「用户追加」的标题下:\n%s", got)
	}
	mainList = branchDo(t, srv, "wf-main", map[string]any{"action": "list"})
	first, _ = mainList["branches"].([]any)[0].(map[string]any)
	if _, has := first["unread_from_branch"]; has {
		t.Fatalf("读过之后不应再算未读: %v", first)
	}

	// ---- say：主会话 → 分支（只有一个分支，可以不写 branch） ----
	back := branchDo(t, srv, "wf-main", map[string]any{"action": "say", "text": "选 A，先做个原型"})
	if back["direction"] != "main_to_branch" || back["to"] != bwf {
		t.Fatalf("主会话 say 结果不对: %v", back)
	}
	if got := inboxText(t, srv, bwf); !strings.Contains(got, "主会话消息") || !strings.Contains(got, "选 A，先做个原型") || !strings.Contains(got, "与用户的要求冲突时以用户为准") {
		t.Fatalf("分支读到的收件箱不对:\n%s", got)
	}

	// ---- read：主会话读分支的汇报（协作预设 = 全文）----
	if _, err := srv.store.CreateFeedbackSession(ctx, store.CreateSessionInput{
		SessionID: "s-branch-1", WorkflowID: bwf, EnvHostName: "h", ProjectDirectory: "/code/a", Title: "原型进展", Summary: "原型跑通了，三个方案的耗时分别是 3 / 5 / 9 分钟",
	}); err != nil {
		t.Fatal(err)
	}
	hist := branchDo(t, srv, "wf-main", map[string]any{"action": "read"})
	rounds, _ := hist["rounds"].([]any)
	found := false
	for _, r := range rounds {
		if m, _ := r.(map[string]any); strings.Contains(fmt.Sprint(m["summary"]), "3 / 5 / 9 分钟") {
			found = true
		}
	}
	if hist["branch"] != bwf || hist["depth"] != "full" || !found {
		t.Fatalf("read 结果不对（应含分支的汇报全文）: %v", hist)
	}
	if limited := branchDo(t, srv, "wf-main", map[string]any{"action": "read", "branch": bwf, "limit": 1}); len(limited["rounds"].([]any)) != 1 {
		t.Fatalf("limit=1 应只给一轮: %v", limited)
	}

	// ---- 越权 ----
	if msg := branchFail(t, srv, bwf, map[string]any{"action": "read"}); !strings.Contains(msg, "只有主会话能用") {
		t.Fatalf("分支不能 read: %s", msg)
	}
	if msg := branchFail(t, srv, "wf-solo", map[string]any{"action": "close", "branch": bwf}); !strings.Contains(msg, "不是你的分支") {
		t.Fatalf("别人不能结束这个分支: %s", msg)
	}
	if msg := branchFail(t, srv, "wf-solo", map[string]any{"action": "read", "branch": bwf}); !strings.Contains(msg, "不是你的分支") {
		t.Fatalf("别人不能读这个分支: %s", msg)
	}
	if msg := branchFail(t, srv, "wf-main", map[string]any{"action": "say", "branch": "wf-solo", "text": "x"}); !strings.Contains(msg, "没有生效的主会话 / 分支关系") {
		t.Fatalf("不能给没有关系的工作流发话: %s", msg)
	}
	if msg := branchFail(t, srv, bwf, map[string]any{"action": "create", "title": "孙分支"}); !strings.Contains(msg, "只允许一层") {
		t.Fatalf("分支不能再建分支: %s", msg)
	}
	if msg := branchFail(t, srv, "wf-main", map[string]any{"action": "close"}); !strings.Contains(msg, "close 需要 branch") {
		t.Fatalf("close 要指明分支: %s", msg)
	}

	// ---- close：只有主会话；幂等；之后关系消失 ----
	closed := branchDo(t, srv, "wf-main", map[string]any{"action": "close", "branch": bwf})
	if closed["ok"] != true || closed["changed"] != true {
		t.Fatalf("close 结果不对: %v", closed)
	}
	if again := branchDo(t, srv, "wf-main", map[string]any{"action": "close", "branch": bwf}); again["changed"] != false {
		t.Fatalf("重复 close 应是无变化: %v", again)
	}
	var closedEvents int
	for _, e := range eventsNamed(events, "branch_changed") {
		if e["change"] == "closed" {
			closedEvents++
		}
	}
	if closedEvents != 1 {
		t.Fatalf("只有真的结束时才广播一次 closed 事件，得到 %d 次", closedEvents)
	}
	if got := inboxText(t, srv, bwf); !strings.Contains(got, "分支事务通知") {
		t.Fatalf("分支的 Agent 应在收件箱里收到收尾通知:\n%s", got)
	}
	for _, wf := range []string{"wf-main", bwf} {
		if l := branchDo(t, srv, wf, map[string]any{"action": "list"}); l["role"] != "none" {
			t.Fatalf("结束后 %s 不应再有活动关系: %v", wf, l)
		}
	}
	if msg := branchFail(t, srv, "wf-main", map[string]any{"action": "say", "text": "还在吗"}); !strings.Contains(msg, "没有可以发消息的对象") {
		t.Fatalf("结束后主会话没有可发的对象: %s", msg)
	}
	if msg := branchFail(t, srv, "wf-main", map[string]any{"action": "say", "branch": bwf, "text": "还在吗"}); !strings.Contains(msg, "没有生效的主会话 / 分支关系") {
		t.Fatalf("结束后指名发话也应被拒: %s", msg)
	}
}

// 发信一方页面上的「对方已读」脚注靠重新拉取消息流翻状态：对方第一次读取时必须有事件，否则它会一直停在「还没读」。
func TestBranchTool_FirstReadEmitsMessageEvent(t *testing.T) {
	srv, events := branchFixture(t)
	created := branchDo(t, srv, "wf-main", map[string]any{"action": "create", "title": "读状态"})
	bwf, _ := created["branch"].(string)

	count := func() int { return len(eventsNamed(events, "branch_message")) }
	lastMsg := func() map[string]any {
		all := eventsNamed(events, "branch_message")
		if len(all) == 0 {
			t.Fatal("应当有 branch_message 事件")
		}
		return all[len(all)-1]
	}
	same := func(a, b any) bool { return fmt.Sprint(a) == fmt.Sprint(b) }

	// 主会话 → 分支：say 本身发一次事件；分支第一次读再发一次（带同一条消息的编号），重复读不再发。
	toBranch := branchDo(t, srv, "wf-main", map[string]any{"action": "say", "text": "x"}) // 只有一个分支，可省略 branch
	afterSay := count()
	inboxText(t, srv, bwf)
	if count() != afterSay+1 {
		t.Fatalf("分支第一次读主会话的话应发一次事件，之前 %d 现在 %d", afterSay, count())
	}
	if ev := lastMsg(); ev["parent"] != "wf-main" || ev["branch"] != bwf || !same(ev["id"], toBranch["message_id"]) {
		t.Fatalf("主会话 → 分支被读的事件不对（parent 应是主会话、branch 应是分支、id 应是那条消息）: %v / 消息 %v", ev, toBranch["message_id"])
	}
	inboxText(t, srv, bwf)
	if count() != afterSay+1 {
		t.Fatalf("重复读取没有新送达的，不应再发事件，现在 %d", count())
	}

	// 分支 → 主会话：反方向同理，事件里的 parent / branch 不随读的是谁而颠倒。
	toMain := branchDo(t, srv, bwf, map[string]any{"action": "say", "text": "y"})
	afterSay = count()
	inboxText(t, srv, "wf-main")
	if count() != afterSay+1 {
		t.Fatalf("主会话第一次读分支的话应发一次事件，之前 %d 现在 %d", afterSay, count())
	}
	if ev := lastMsg(); ev["parent"] != "wf-main" || ev["branch"] != bwf || !same(ev["id"], toMain["message_id"]) {
		t.Fatalf("分支 → 主会话被读的事件不对: %v / 消息 %v", ev, toMain["message_id"])
	}

	// 用户在分支页里说的话（抄送给主会话，actor 是 human）被读：脚注不涉及送达，不发。
	if _, err := srv.store.CopyUserNoteToMain(context.Background(), bwf, "用户说了一句", 0); err != nil {
		t.Fatalf("抄送用户的话失败: %v", err)
	}
	beforeHuman := count()
	if got := inboxText(t, srv, "wf-main"); !strings.Contains(got, "用户说了一句") {
		t.Fatalf("主会话应读到抄送:\n%s", got)
	}
	if count() != beforeHuman {
		t.Fatalf("用户的抄送被读不应发 branch_message，之前 %d 现在 %d", beforeHuman, count())
	}
}

func TestBranchTool_ArgumentErrors(t *testing.T) {
	srv, _ := branchFixture(t)

	if msg := branchFail(t, srv, "", map[string]any{"action": "list"}); !strings.Contains(msg, "workflow_id 必填") {
		t.Fatalf("缺 workflow_id: %s", msg)
	}
	if msg := branchFail(t, srv, "wf-solo", map[string]any{"action": "explode"}); !strings.Contains(msg, "未知 action") {
		t.Fatalf("未知 action: %s", msg)
	}
	if msg := branchFail(t, srv, "wf-solo", map[string]any{"action": "create"}); !strings.Contains(msg, "create 需要 title") {
		t.Fatalf("create 缺 title: %s", msg)
	}
	if msg := branchFail(t, srv, "wf-solo", map[string]any{"action": "read"}); !strings.Contains(msg, "你没有活动分支可读") {
		t.Fatalf("没有分支时 read: %s", msg)
	}
	if msg := branchFail(t, srv, "wf-solo", map[string]any{"action": "say", "text": "喂"}); !strings.Contains(msg, "没有可以发消息的对象") {
		t.Fatalf("没有关系时 say: %s", msg)
	}
	if l := branchDo(t, srv, "wf-solo", map[string]any{"action": "list"}); l["role"] != "none" || !strings.Contains(fmt.Sprint(l["note"]), "create") {
		t.Fatalf("无关系的 list 应是 role none 并提示怎么建: %v", l)
	}

	// 建一个分支后再检查 say 的入参：空文本、超长。
	created := branchDo(t, srv, "wf-main", map[string]any{"action": "create", "title": "测试分支"})
	bwf := created["branch"].(string)
	if msg := branchFail(t, srv, bwf, map[string]any{"action": "say", "text": "   "}); !strings.Contains(msg, "text 必填") {
		t.Fatalf("空文本: %s", msg)
	}
	if msg := branchFail(t, srv, bwf, map[string]any{"action": "say", "text": strings.Repeat("长", store.BranchSayMaxRunes+1)}); !strings.Contains(msg, "消息太长") {
		t.Fatalf("超长: %s", msg)
	}
	// 主会话有两个活动分支时，read / say 必须点名。
	branchDo(t, srv, "wf-main", map[string]any{"action": "create", "title": "第二个分支"})
	if msg := branchFail(t, srv, "wf-main", map[string]any{"action": "read"}); !strings.Contains(msg, "有 2 个活动分支") {
		t.Fatalf("多个分支时 read 要点名: %s", msg)
	}
	if msg := branchFail(t, srv, "wf-main", map[string]any{"action": "say", "text": "喂"}); !strings.Contains(msg, "有 2 个活动分支") {
		t.Fatalf("多个分支时 say 要点名: %s", msg)
	}
}

func TestBranchTool_CreateHourlyLimit(t *testing.T) {
	srv, _ := branchFixture(t)
	for i := 0; i < store.BranchCreatePerHour; i++ {
		branchDo(t, srv, "wf-main", map[string]any{"action": "create", "title": fmt.Sprintf("分支 %d", i+1)})
	}
	if msg := branchFail(t, srv, "wf-main", map[string]any{"action": "create", "title": "多出来的一个"}); !strings.Contains(msg, "一小时内已经新建了") {
		t.Fatalf("应触发每小时限额: %s", msg)
	}
	if l := branchDo(t, srv, "wf-main", map[string]any{"action": "list"}); l["count"] != float64(store.BranchCreatePerHour) {
		t.Fatalf("被拒的创建不应留下半个分支: %v", l)
	}
}

func TestBranchTool_InboxSwitchOff(t *testing.T) {
	srv, _ := branchFixture(t)
	created := branchDo(t, srv, "wf-main", map[string]any{"action": "create", "title": "分支"})
	bwf := created["branch"].(string)
	srv.cfg.InboxOff = true
	if msg := branchFail(t, srv, bwf, map[string]any{"action": "say", "text": "汇报"}); !strings.Contains(msg, "收件箱总开关已关闭") {
		t.Fatalf("收件箱总开关关闭时 say 应明说送不出去: %s", msg)
	}
}

// branch 工具的权限与 chat 同一档：凭据能用 chat 就能用 branch，反之亦然；tools/list 里能看到它。
func TestBranchTool_PermissionsFollowChat(t *testing.T) {
	for mask := 0; mask < 1<<6; mask++ {
		p := model.Permissions{
			Configure: mask&1 != 0, Execute: mask&2 != 0, Feedback: mask&4 != 0,
			Sessions: mask&8 != 0, SystemInfo: mask&16 != 0, Skills: mask&32 != 0,
		}
		// 与 chat 同进退：凭据能用 chat 就能用 branch。
		if p.AllowsTool("branch") != p.AllowsTool("chat") {
			t.Fatalf("权限 %+v：branch 与 chat 应同进退", p)
		}
	}
	defs := GetToolDefinitionsForPermissions(model.Permissions{Feedback: true})
	found := false
	for _, d := range defs {
		if d.Name == "branch" {
			found = true
			if !strings.Contains(string(d.InputSchema), `"enum": ["list", "create", "say", "read", "close", "answer", "escalate"]`) {
				t.Fatalf("branch 的 schema 里应列出全部动作: %s", d.InputSchema)
			}
		}
	}
	if !found {
		t.Fatal("tools/list 里应有 branch")
	}
}

func TestBranchHeaderLine(t *testing.T) {
	srv, _ := branchFixture(t)
	ctx := context.Background()

	if got := srv.branchHeaderLine(ctx, "wf-solo"); got != "" {
		t.Fatalf("没有关系的工作流不应有 branch 行（INV-2）: %q", got)
	}
	if got := srv.branchHeaderLine(ctx, ""); got != "" {
		t.Fatalf("空工作流编号也不应有: %q", got)
	}

	var bwfs []string
	for i := 0; i < 4; i++ {
		c := branchDo(t, srv, "wf-main", map[string]any{"action": "create", "title": fmt.Sprintf("分支%d号", i+1)})
		bwfs = append(bwfs, c["branch"].(string))
	}
	line := srv.branchHeaderLine(ctx, "wf-main")
	if strings.Contains(line, "\n") || !strings.HasPrefix(line, "branch: 你有 4 个活动分支：") {
		t.Fatalf("主会话的 branch 行应是单行、写明个数: %q", line)
	}
	for _, want := range []string{"「分支1号」（" + bwfs[0] + "）", "「分支3号」", " 等 4 个", "branch(action: \"list\")"} {
		if !strings.Contains(line, want) {
			t.Fatalf("主会话的 branch 行缺少 %q: %s", want, line)
		}
	}
	if strings.Contains(line, "分支4号") {
		t.Fatalf("最多列 3 个名字: %s", line)
	}

	bl := srv.branchHeaderLine(ctx, bwfs[0])
	for _, want := range []string{"branch: 你是分支会话", "「t-wf-main」（wf-main）", "branch(action: \"say\")", "你向主会话开放："} {
		if !strings.Contains(bl, want) {
			t.Fatalf("分支的 branch 行缺少 %q: %s", want, bl)
		}
	}
	if strings.Contains(bl, "\n") {
		t.Fatalf("branch 行应是单行: %q", bl)
	}
}

// 真实回执：有关系时 branch 行出现在标记行之前；没有关系的工作流回执里没有 branch 行。
func TestBranchHeaderLineInReceipts(t *testing.T) {
	srv, _ := branchFixture(t)
	created := branchDo(t, srv, "wf-main", map[string]any{"action": "create", "title": "回执测试"})
	bwf := created["branch"].(string)

	fb := func(wf string) string {
		t.Helper()
		out := callToolText(t, srv, "interactive_feedback", map[string]any{
			"workflow_id": wf, "project_directory": "/code/a", "summary": "汇报 " + wf,
		})
		completeLatestPending(t, srv, wf)
		return out
	}
	hasBranchLine := func(receipt string) bool {
		for _, ln := range strings.Split(receipt, "\n") {
			if strings.HasPrefix(ln, "branch: ") {
				return true
			}
		}
		return false
	}
	completeLatestPending(t, srv, "wf-solo")
	if got := fb("wf-solo"); hasBranchLine(got) {
		t.Fatalf("没有关系的工作流回执里不应有 branch 行:\n%s", got)
	}
	completeLatestPending(t, srv, "wf-main")
	got := fb("wf-main")
	if !strings.Contains(got, "\nbranch: 你有 1 个活动分支：「回执测试」（"+bwf+"）") {
		t.Fatalf("主会话的回执应有 branch 行:\n%s", got)
	}
	if bi, mi := strings.Index(got, "branch:"), strings.Index(got, "=== "); !(bi >= 0 && bi < mi) {
		t.Fatalf("branch 行应在标记行之前（%d %d）:\n%s", bi, mi, got)
	}
}

func TestFormatInboxHint_BranchMessages(t *testing.T) {
	now := time.Now()
	only := formatInboxHint("wf-x", store.InboxSummary{Unread: 2, UnreadBranch: 2, LatestPreview: "分支里的话"}, now)
	if !strings.Contains(only, "有 2 条分支消息还没读") || strings.Contains(only, "追加指令") || strings.Contains(only, "分支里的话") {
		t.Fatalf("只有分支消息时的提示不对（不能说成用户追加，也不放预览）: %s", only)
	}
	if !strings.Contains(only, "branch(action: \"say\")") {
		t.Fatalf("提示里应告诉怎么回: %s", only)
	}
	mixed := formatInboxHint("wf-x", store.InboxSummary{Unread: 3, UnreadBranch: 1, LatestPreview: "用户的话"}, now)
	if !strings.Contains(mixed, "有 1 条分支消息还没读") || !strings.Contains(mixed, "有 2 条追加指令还没读") || !strings.Contains(mixed, "用户的话") {
		t.Fatalf("混合时两类各说各的: %s", mixed)
	}
	// 没有分支消息：与以前逐字节一致。
	plain := formatInboxHint("wf-x", store.InboxSummary{Unread: 2, LatestPreview: "用户的话"}, now)
	if strings.Contains(plain, "分支") || !strings.Contains(plain, "有 2 条追加指令还没读") {
		t.Fatalf("没有分支消息时不应提到分支: %s", plain)
	}

	stopBranch := formatInboxStopMessage("wf-x", store.InboxSummary{Unread: 1, UnreadBranch: 1})
	if !strings.Contains(stopBranch, "1 条分支消息") || strings.Contains(stopBranch, "追加指令") {
		t.Fatalf("只有分支消息时的收尾拦截不对: %s", stopBranch)
	}
	stopMixed := formatInboxStopMessage("wf-x", store.InboxSummary{Unread: 3, UnreadBranch: 1})
	if !strings.Contains(stopMixed, "用户有 2 条追加指令") {
		t.Fatalf("混合时按用户追加数说: %s", stopMixed)
	}
	if got := formatInboxStopMessage("wf-x", store.InboxSummary{Unread: 2}); !strings.Contains(got, "用户有 2 条追加指令你还没读到") {
		t.Fatalf("没有分支消息时与以前一致: %s", got)
	}
}

func TestSplitBranchRows(t *testing.T) {
	u1, b1, u2, b2 := model.QueuedFeedback{ID: 1}, model.QueuedFeedback{ID: 2, Kind: model.InboxKindBranch},
		model.QueuedFeedback{ID: 3, Kind: "answer"}, model.QueuedFeedback{ID: 4, Kind: model.InboxKindBranch}
	other, branch := splitBranchRows([]model.QueuedFeedback{u1, b1, u2, b2})
	ids := func(rs []model.QueuedFeedback) string {
		var s []string
		for _, r := range rs {
			s = append(s, fmt.Sprint(r.ID))
		}
		return strings.Join(s, ",")
	}
	if ids(other) != "1,3" || ids(branch) != "2,4" {
		t.Fatalf("应各自保持顺序: other=%s branch=%s", ids(other), ids(branch))
	}
	other, branch = splitBranchRows([]model.QueuedFeedback{u1, u2})
	if ids(other) != "1,3" || branch != nil {
		t.Fatalf("没有分支行时第一组是原样、第二组为空: %s %v", ids(other), branch)
	}
	other, branch = splitBranchRows([]model.QueuedFeedback{b1})
	if len(other) != 0 || ids(branch) != "2" {
		t.Fatalf("只有分支行: %s %s", ids(other), ids(branch))
	}
	if other, branch = splitBranchRows(nil); len(other) != 0 || len(branch) != 0 {
		t.Fatalf("空输入: %v %v", other, branch)
	}
}
