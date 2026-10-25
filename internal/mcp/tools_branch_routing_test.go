package mcp

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"github.com/teacat99/RelayMesh/v2/internal/model"
	"github.com/teacat99/RelayMesh/v2/internal/store"
)

// 决策路由（#236 P2）的 MCP 侧：分支里的 async_ask 先送主会话、branch 的 answer / escalate、回执头与收件箱呈现、通知。

// askNotifierFake 记下通知闸收到的新请示（NotifyAsk）：路由给主会话的请示建立时不应通知用户，转交 / 升级时才通知。
type askNotifierFake struct {
	mu   sync.Mutex
	asks []string
}

func (f *askNotifierFake) NotifyNotice(*model.FeedbackSession) {}
func (f *askNotifierFake) NotifyAsk(it *model.ConfirmItem) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.asks = append(f.asks, it.WorkflowID+":"+model.ConfirmRef(it.Seq))
}
func (f *askNotifierFake) list() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.asks...)
}

func routingFixture(t *testing.T) (*Server, *[]workEvent, *askNotifierFake, string) {
	t.Helper()
	srv, events := branchFixture(t)
	fake := &askNotifierFake{}
	srv.SetEventNotifier(fake)
	created := branchDo(t, srv, "wf-main", map[string]any{"action": "create", "title": "路由分支"})
	return srv, events, fake, created["branch"].(string)
}

func askArgs(title, risk string) map[string]any {
	return map[string]any{
		"action": "create", "title": title, "risk": risk, "allow_note": true,
		"options": []map[string]any{{"id": "a", "label": "甲方案"}, {"id": "b", "label": "乙方案"}},
	}
}

func asyncDo(t *testing.T, srv *Server, wf string, args map[string]any) map[string]any {
	t.Helper()
	args["workflow_id"] = wf
	return callToolJSON(t, srv, "async_ask", args)
}

func TestAsyncAsk_BranchRoutesToMainWithoutNotifyingUser(t *testing.T) {
	srv, events, fake, bwf := routingFixture(t)
	ctx := context.Background()

	res := asyncDo(t, srv, bwf, askArgs("要不要先做迁移？", "L1"))
	if res["routed_to"] != "main" || res["created"] != true {
		t.Fatalf("分支里的请示应路由给主会话: %#v", res)
	}
	if note, _ := res["route_note"].(string); !strings.Contains(note, "主会话定夺") || !strings.Contains(note, "30 分钟") || !strings.Contains(note, "先到先得") {
		t.Fatalf("route_note 应说明交给谁、多久升级、先到先得: %v", res["route_note"])
	}
	if got := fake.list(); len(got) != 0 {
		t.Fatalf("路由给主会话的请示建立时不应通知用户: %v", got)
	}
	// 提醒了主会话读收件箱，也刷新了确认卡
	var toMain bool
	for _, e := range eventsNamed(events, inboxEvent) {
		if e["workflow_id"] == "wf-main" {
			toMain = true
		}
	}
	if !toMain || len(eventsNamed(events, "confirm_updated")) == 0 {
		t.Fatalf("应有给主会话的 inbox 事件与 confirm_updated: %v", *events)
	}

	// 主会话：收件箱里是「分支请示」，带答复写法；branch list 与回执头都看得到
	inbox := inboxText(t, srv, "wf-main")
	for _, want := range []string{"分支请示（等你定夺）", "同伴请示，不是用户指令", "要不要先做迁移？", `branch(action:"answer"`, `branch(action:"escalate"`} {
		if !strings.Contains(inbox, want) {
			t.Fatalf("主会话收件箱缺 %q:\n%s", want, inbox)
		}
	}
	list := branchDo(t, srv, "wf-main", map[string]any{"action": "list"})
	asks, _ := list["pending_asks"].([]any)
	if len(asks) != 1 {
		t.Fatalf("branch list 应列出 1 项待定夺请示: %#v", list)
	}
	first := asks[0].(map[string]any)
	if first["ref"] != "Q1" || first["branch"] != bwf || first["risk"] != "L1" {
		t.Fatalf("待定夺请示内容不对: %#v", first)
	}
	if b := list["branches"].([]any)[0].(map[string]any); toInt(b["pending_asks"]) != 1 {
		t.Fatalf("分支条目应带 pending_asks: %#v", b)
	}
	line := srv.branchHeaderLine(ctx, "wf-main")
	if !strings.Contains(line, "有 1 项请示在等你定夺：「路由分支」Q1") || strings.Contains(line, "\n") {
		t.Fatalf("主会话回执头应列出待定夺请示（单行）: %q", line)
	}
	// 分支：回执头与 list 讲明请示去哪；get 显示在等主会话
	bl := srv.branchHeaderLine(ctx, bwf)
	if !strings.Contains(bl, "async_ask 发请示") || !strings.Contains(bl, "请示会先送主会话") || !strings.Contains(bl, "可定夺请示") {
		t.Fatalf("分支回执头应讲明请示路由: %s", bl)
	}
	bl2 := branchDo(t, srv, bwf, map[string]any{"action": "list"})
	if hint, _ := bl2["how_to_talk_main"].(string); !strings.Contains(hint, "async_ask") || !strings.Contains(hint, "30 分钟") {
		t.Fatalf("how_to_talk_main 应改成请示走 async_ask: %v", bl2["how_to_talk_main"])
	}
	if exp := bl2["expose_to_main"].(map[string]any); exp["decide"] != true {
		t.Fatalf("expose_to_main 应带 decide: %#v", exp)
	}
	got := asyncDo(t, srv, bwf, map[string]any{"action": "get", "ref": "Q1"})
	if got["awaiting"] != "main" {
		t.Fatalf("get 应显示在等主会话: %#v", got)
	}
}

func TestAsyncAsk_PlainWorkflowUnchanged(t *testing.T) {
	srv, _, fake, _ := routingFixture(t)
	res := asyncDo(t, srv, "wf-solo", askArgs("普通会话的请示", "L1"))
	if _, has := res["routed_to"]; has {
		t.Fatalf("普通会话不应有路由字段（INV-2）: %#v", res)
	}
	if _, has := res["route_note"]; has {
		t.Fatalf("普通会话不应有 route_note: %#v", res)
	}
	if got := fake.list(); len(got) != 1 || got[0] != "wf-solo:Q1" {
		t.Fatalf("普通会话的请示照常通知用户: %v", got)
	}
	if g := asyncDo(t, srv, "wf-solo", map[string]any{"action": "get", "ref": "Q1"}); g["awaiting"] != "user" {
		t.Fatalf("普通请示 get 应显示在等用户: %#v", g)
	}
	if line := srv.branchHeaderLine(context.Background(), "wf-solo"); line != "" {
		t.Fatalf("无关系的工作流不应有 branch 行: %q", line)
	}
}

func TestBranchAnswer_ViaTool(t *testing.T) {
	srv, events, fake, bwf := routingFixture(t)
	ctx := context.Background()
	asyncDo(t, srv, bwf, askArgs("选哪个方案？", "L1"))
	*events = (*events)[:0]

	// 分支自己不能 answer
	if msg := branchFail(t, srv, bwf, map[string]any{"action": "answer", "ref": "Q1", "options": []string{"a"}}); !strings.Contains(msg, "只有主会话能用") {
		t.Fatalf("分支调用 answer 应被拒绝: %s", msg)
	}
	// 缺 ref / 选项不属于
	if msg := branchFail(t, srv, "wf-main", map[string]any{"action": "answer", "options": []string{"a"}}); !strings.Contains(msg, "ref 必填") {
		t.Fatalf("缺 ref: %s", msg)
	}
	if msg := branchFail(t, srv, "wf-main", map[string]any{"action": "answer", "ref": "Q1", "options": []string{"zzz"}}); !strings.Contains(msg, "不属于") {
		t.Fatalf("非法选项: %s", msg)
	}
	// 不是自己的分支
	if msg := branchFail(t, srv, "wf-solo", map[string]any{"action": "answer", "branch": bwf, "ref": "Q1", "options": []string{"a"}}); !strings.Contains(msg, "不是你的活动分支") {
		t.Fatalf("越权: %s", msg)
	}

	res := branchDo(t, srv, "wf-main", map[string]any{"action": "answer", "ref": "Q1", "options": "b", "note": "我的判断理由"})
	if res["ok"] != true || res["status"] != "answered" || res["answered_by"] != "main" || res["branch"] != bwf {
		t.Fatalf("answer 应成功（只有一个分支时可省略 branch；options 容忍写成字符串）: %#v", res)
	}
	// 事件：确认卡刷新 + 提醒分支读收件箱
	if len(eventsNamed(events, "confirm_updated")) == 0 {
		t.Fatalf("应发 confirm_updated: %v", *events)
	}
	var toBranch bool
	for _, e := range eventsNamed(events, inboxEvent) {
		if e["workflow_id"] == bwf {
			toBranch = true
		}
	}
	if !toBranch {
		t.Fatalf("应提醒分支读收件箱: %v", *events)
	}
	if got := fake.list(); len(got) != 0 {
		t.Fatalf("代答不应通知用户新请示: %v", got)
	}
	// 分支读到的答复带「主会话代为定夺」标注
	inbox := inboxText(t, srv, bwf)
	for _, want := range []string{"确认项答复", "主会话代为定夺，不是用户本人的答复", "Q1", "乙方案", "我的判断理由"} {
		if !strings.Contains(inbox, want) {
			t.Fatalf("分支收件箱缺 %q:\n%s", want, inbox)
		}
	}
	got := asyncDo(t, srv, bwf, map[string]any{"action": "get", "ref": "Q1"})
	item := got["item"].(map[string]any)
	ans := item["answer"].(map[string]any)
	if item["status"] != "answered" || ans["by"] != "agent:wf-main" {
		t.Fatalf("分支 get 应看到主会话代答: %#v", item)
	}
	if _, waiting := got["awaiting"]; waiting {
		t.Fatalf("已答复的请示不应再有 awaiting: %#v", got)
	}
	// 主会话不再有待定夺请示；再答一次冲突
	if line := srv.branchHeaderLine(ctx, "wf-main"); strings.Contains(line, "在等你定夺") {
		t.Fatalf("答完后回执头不应再提示待定夺: %s", line)
	}
	if msg := branchFail(t, srv, "wf-main", map[string]any{"action": "answer", "ref": "Q1", "options": []string{"a"}}); !strings.Contains(msg, "已处理") {
		t.Fatalf("重复作答: %s", msg)
	}
}

func TestBranchEscalate_ViaTool(t *testing.T) {
	srv, events, fake, bwf := routingFixture(t)
	asyncDo(t, srv, bwf, askArgs("涉及预算的事", "L1"))
	*events = (*events)[:0]

	if msg := branchFail(t, srv, "wf-main", map[string]any{"action": "escalate", "ref": "Q1"}); !strings.Contains(msg, "note 必填") {
		t.Fatalf("缺 note: %s", msg)
	}
	res := branchDo(t, srv, "wf-main", map[string]any{"action": "escalate", "ref": "Q1", "note": "涉及预算，需要你本人拍板"})
	if res["ok"] != true || res["awaiting"] != "user" {
		t.Fatalf("escalate 应成功并说明在等用户: %#v", res)
	}
	if got := fake.list(); len(got) != 1 || got[0] != bwf+":Q1" {
		t.Fatalf("转交时应按新请示通知用户: %v", got)
	}
	if len(eventsNamed(events, "confirm_updated")) == 0 {
		t.Fatalf("应发 confirm_updated: %v", *events)
	}
	got := asyncDo(t, srv, bwf, map[string]any{"action": "get", "ref": "Q1"})
	item := got["item"].(map[string]any)
	esc, _ := item["escalation"].(map[string]any)
	if got["awaiting"] != "user" || esc["by"] != "agent:wf-main" || esc["note"] != "涉及预算，需要你本人拍板" || item["escalated_at"] == nil {
		t.Fatalf("分支 get 应看到转交信息: %#v", got)
	}
	// 转交后主会话不能再答
	if msg := branchFail(t, srv, "wf-main", map[string]any{"action": "answer", "ref": "Q1", "options": []string{"a"}}); !strings.Contains(msg, "已转交给用户") {
		t.Fatalf("转交后再答: %s", msg)
	}
	if line := srv.branchHeaderLine(context.Background(), "wf-main"); strings.Contains(line, "在等你定夺") {
		t.Fatalf("转交后回执头不应再提示: %s", line)
	}
}

func TestBranchAnswer_BranchArgResolution(t *testing.T) {
	srv, _, _, b1 := routingFixture(t)
	c2 := branchDo(t, srv, "wf-main", map[string]any{"action": "create", "title": "第二个分支"})
	b2 := c2["branch"].(string)
	asyncDo(t, srv, b1, askArgs("分支一的请示", "L1"))
	asyncDo(t, srv, b2, askArgs("分支二的请示", "L1"))

	// 两个活动分支时必须指定 branch，错误里讲明原因
	if msg := branchFail(t, srv, "wf-main", map[string]any{"action": "answer", "ref": "Q1", "options": []string{"a"}}); !strings.Contains(msg, "你有 2 个活动分支") || !strings.Contains(msg, "各自分支内唯一") {
		t.Fatalf("多分支缺 branch: %s", msg)
	}
	// 两个分支都有 Q1：答 b2 的 Q1 不影响 b1 的
	branchDo(t, srv, "wf-main", map[string]any{"action": "answer", "branch": b2, "ref": "Q1", "options": []string{"a"}})
	if g := asyncDo(t, srv, b1, map[string]any{"action": "get", "ref": "Q1"}); g["awaiting"] != "main" {
		t.Fatalf("b1 的 Q1 应仍在等主会话: %#v", g)
	}
	list := branchDo(t, srv, "wf-main", map[string]any{"action": "list"})
	if asks := list["pending_asks"].([]any); len(asks) != 1 || asks[0].(map[string]any)["branch"] != b1 {
		t.Fatalf("应只剩 b1 的请示待定夺: %#v", list["pending_asks"])
	}
}

func TestBranchClose_EscalatesRoutedAsksAndNotifies(t *testing.T) {
	srv, events, fake, bwf := routingFixture(t)
	asyncDo(t, srv, bwf, askArgs("结束时还在等的请示", "L1"))
	*events = (*events)[:0]
	branchDo(t, srv, "wf-main", map[string]any{"action": "close", "branch": bwf})
	if got := fake.list(); len(got) != 1 || got[0] != bwf+":Q1" {
		t.Fatalf("结束分支时被转交的请示应按新请示通知用户: %v", got)
	}
	g := asyncDo(t, srv, bwf, map[string]any{"action": "get", "ref": "Q1"})
	esc, _ := g["item"].(map[string]any)["escalation"].(map[string]any)
	if g["awaiting"] != "user" || esc["by"] != "system" || !strings.Contains(esc["note"].(string), "分支已结束") {
		t.Fatalf("结束后请示应转交用户: %#v", g)
	}
}

func TestBranchAnswer_L2GuardAndDecideOff(t *testing.T) {
	srv, _, _, bwf := routingFixture(t)
	ctx := context.Background()
	if _, err := srv.store.UpdateBranchPrefs(ctx, func(cur model.BranchPrefs) (model.BranchPrefs, error) { cur.L2Guard = true; return cur, nil }); err != nil {
		t.Fatal(err)
	}
	asyncDo(t, srv, bwf, askArgs("不可逆的操作", "L2"))
	if msg := branchFail(t, srv, "wf-main", map[string]any{"action": "answer", "ref": "Q1", "options": []string{"a"}}); !strings.Contains(msg, "兜底开关") || !strings.Contains(msg, "escalate") {
		t.Fatalf("L2 兜底: %s", msg)
	}
	branchDo(t, srv, "wf-main", map[string]any{"action": "escalate", "ref": "Q1", "note": "L2 只能你来批"})

	// 分支「仅可见」：请示不路由；branch 行如实说明直发用户
	if _, err := srv.store.UpdateBranchSettings(ctx, bwf, store.UpdateBranchInput{Preset: model.ExposePresetVisible}); err != nil {
		t.Fatal(err)
	}
	res := asyncDo(t, srv, bwf, askArgs("没被授权定夺时", "L1"))
	if _, has := res["routed_to"]; has {
		t.Fatalf("decide 关时不应路由: %#v", res)
	}
	bl := srv.branchHeaderLine(ctx, bwf)
	if !strings.Contains(bl, "主会话没有被授权定夺") || !strings.Contains(bl, "不可定夺请示") {
		t.Fatalf("decide 关时分支回执头应如实说明: %s", bl)
	}
	if msg := branchFail(t, srv, "wf-main", map[string]any{"action": "answer", "ref": "Q2", "options": []string{"a"}}); !strings.Contains(msg, "没有向主会话开放") {
		t.Fatalf("decide 关时主会话不能答: %s", msg)
	}
}

func TestLooseStringList(t *testing.T) {
	cases := map[string][]string{
		`["a","b"]`:    {"a", "b"},
		`"a"`:          {"a"},
		`"a, b"`:       {"a", "b"},
		`["a, b","c"]`: {"a", "b", "c"},
		`"a，b、c"`:      {"a", "b", "c"},
		`null`:         nil,
		`[]`:           nil,
	}
	for in, want := range cases {
		var got looseStringList
		if err := json.Unmarshal([]byte(in), &got); err != nil {
			t.Fatalf("%s: %v", in, err)
		}
		if strings.Join(got, "|") != strings.Join(want, "|") {
			t.Errorf("%s → %v，期望 %v", in, []string(got), want)
		}
	}
	var bad looseStringList
	if err := json.Unmarshal([]byte(`[1,2]`), &bad); err == nil || !strings.Contains(err.Error(), "options") {
		t.Errorf("非字符串应报明确的错: %v", err)
	}
}

// 主会话页的分支流要补「主会话代为定夺 / 请示已转交」事件行，靠 branch_changed 触发它重取：代答与转交各发一次，带主会话与分支编号。
func TestBranchAnswerAndEscalate_EmitBranchChanged(t *testing.T) {
	srv, events, _, bwf := routingFixture(t)
	q1 := asyncDo(t, srv, bwf, askArgs("代答的请示", "L1"))["ref"].(string)
	q2 := asyncDo(t, srv, bwf, askArgs("转交的请示", "L1"))["ref"].(string)
	changeOf := func(change string) []map[string]any {
		var out []map[string]any
		for _, e := range eventsNamed(events, "branch_changed") {
			if e["change"] == change {
				out = append(out, e)
			}
		}
		return out
	}

	branchDo(t, srv, "wf-main", map[string]any{"action": "answer", "branch": bwf, "ref": q1, "options": []string{"a"}})
	if got := changeOf("ask_answered"); len(got) != 1 || got[0]["parent"] != "wf-main" || got[0]["branch"] != bwf {
		t.Fatalf("代答应发一次 branch_changed(ask_answered): %v", got)
	}
	if got := changeOf("ask_escalated"); len(got) != 0 {
		t.Fatalf("代答不该发转交事件: %v", got)
	}

	branchDo(t, srv, "wf-main", map[string]any{"action": "escalate", "branch": bwf, "ref": q2, "note": "涉及预算"})
	if got := changeOf("ask_escalated"); len(got) != 1 || got[0]["parent"] != "wf-main" || got[0]["branch"] != bwf {
		t.Fatalf("转交应发一次 branch_changed(ask_escalated): %v", got)
	}

	// 被拒绝的操作（已答复的再答）不发事件
	branchFail(t, srv, "wf-main", map[string]any{"action": "answer", "branch": bwf, "ref": q1, "options": []string{"b"}})
	if got := changeOf("ask_answered"); len(got) != 1 {
		t.Fatalf("被拒绝的代答不该再发事件: %v", got)
	}
}
