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

// 会话参数（#236 P3）的 MCP 侧：session_params 工具、回执头里的 params: 行、常驻规则回执前缀随来源切换。
// 权限与取值规则本身在 store 层有全套单测（branch_params_test.go），这里只验呈现、解析和接线。

func paramsDo(t *testing.T, srv *Server, wf string, args map[string]any) map[string]any {
	t.Helper()
	args["workflow_id"] = wf
	return callToolJSON(t, srv, "session_params", args)
}

// paramsFail 期望调用以工具错误返回，给出错误文本。
func paramsFail(t *testing.T, srv *Server, wf string, args map[string]any) string {
	t.Helper()
	if wf != "" {
		args["workflow_id"] = wf
	}
	text, isErr := callToolRaw(t, srv, "session_params", args)
	if !isErr {
		t.Fatalf("期望失败但成功了: %v → %s", args, text)
	}
	return text
}

// paramsBranch 由 wf-main 建一个分支（MCP 建的是协作预设：改参数开到「常驻规则」），返回分支的 workflow_id。
func paramsBranch(t *testing.T, srv *Server, title string) string {
	t.Helper()
	created := branchDo(t, srv, "wf-main", map[string]any{"action": "create", "title": title})
	bwf, _ := created["branch"].(string)
	if bwf == "" {
		t.Fatalf("建分支失败: %v", created)
	}
	return bwf
}

// setBranchParamsTier 模拟用户在网页里把某个分支的「改参数」调到指定档位，其余开放项保持协作预设。
func setBranchParamsTier(t *testing.T, srv *Server, bwf, level string) {
	t.Helper()
	e, _ := model.BranchExposePreset(model.ExposePresetCollab)
	e.Params = level
	if _, err := srv.store.UpdateBranchSettings(context.Background(), bwf, store.UpdateBranchInput{Expose: &e}); err != nil {
		t.Fatalf("设置档位 %s 失败: %v", level, err)
	}
}

func paramEventCount(t *testing.T, srv *Server, branch string) int {
	t.Helper()
	evs, err := srv.store.ListBranchParamEvents(context.Background(), branch, 200)
	if err != nil {
		t.Fatal(err)
	}
	return len(evs)
}

// 与分支无关的工作流（包括根本不存在的）调用 session_params：得到「没有分支关系」的说明，不报错、不写任何东西（INV-9）。
func TestSessionParams_NoBranchRelation(t *testing.T) {
	srv, events := branchFixture(t)
	for _, wf := range []string{"wf-solo", "wf-ghost"} {
		for _, args := range []map[string]any{
			{},
			{"action": "get"},
			{"action": "set", "patch": map[string]any{"phase": "dev"}},
			{"action": "set", "patch": map[string]any{"profile": "x"}}, // 没有关系时连键检查都不该抢先报错
		} {
			out := paramsDo(t, srv, wf, args)
			note, _ := out["note"].(string)
			if out["ok"] != false || out["role"] != "none" || !strings.Contains(note, "没有分支关系") {
				t.Fatalf("%s 没有分支关系应得到说明而不是错误: %v → %v", wf, args, out)
			}
		}
		if n := paramEventCount(t, srv, wf); n != 0 {
			t.Fatalf("%s 不该产生任何参数事件: %d", wf, n)
		}
	}
	if got := eventsNamed(events, "branch_changed"); len(got) != 0 {
		t.Fatalf("没有关系的调用不该广播 branch_changed: %v", got)
	}
}

func TestSessionParams_BadCalls(t *testing.T) {
	srv, _ := branchFixture(t)
	if msg := paramsFail(t, srv, "", map[string]any{"action": "get"}); !strings.Contains(msg, "workflow_id 必填") {
		t.Fatalf("缺 workflow_id: %s", msg)
	}
	if msg := paramsFail(t, srv, "wf-main", map[string]any{"action": "delete"}); !strings.Contains(msg, "未知 action") {
		t.Fatalf("未知 action: %s", msg)
	}
	bwf := paramsBranch(t, srv, "坏调用")
	for name, patch := range map[string]any{
		"空 patch":  map[string]any{},
		"缺 patch":  nil,
		"不认识的键":    map[string]any{"colour": "red"},
		"字符串字段给了数": map[string]any{"resident": 5},
		"异步给了乱值":   map[string]any{"async": "maybe"},
	} {
		args := map[string]any{"action": "set"}
		if patch != nil {
			args["patch"] = patch
		}
		_ = paramsFail(t, srv, "wf-main", args)
		if n := paramEventCount(t, srv, bwf); n != 0 {
			t.Fatalf("%s：被拒的调用不该留下事件: %d", name, n)
		}
	}
}

// 主会话读、写分支的参数：单个分支不用指定 target；写入落到常驻规则表（来源记为主会话）、广播事件、再读能看到最近变化。
func TestSessionParams_MainGetAndSet(t *testing.T) {
	srv, events := branchFixture(t)
	ctx := context.Background()
	bwf := paramsBranch(t, srv, "调研缓存方案")

	got := paramsDo(t, srv, "wf-main", map[string]any{"action": "get"})
	if got["role"] != "main" || got["branch"] != bwf || got["level"] != "resident" || got["branch_title"] != "调研缓存方案" {
		t.Fatalf("主会话 get 不对: %v", got)
	}
	if lm, _ := got["level_meaning"].(string); !strings.Contains(lm, "常驻规则") {
		t.Fatalf("应讲明这一档能改什么: %v", got["level_meaning"])
	}
	if ed, _ := got["editable"].([]any); len(ed) != 1 || ed[0] != "resident" {
		t.Fatalf("resident 档只能改常驻规则: %v", got["editable"])
	}
	vals, _ := got["values"].(map[string]any)
	res, _ := vals["resident"].(map[string]any)
	if res["set"] != false || toInt(res["rev"]) != 0 || res["written_by"] != "" {
		t.Fatalf("还没写过的常驻规则应是空的、没有来源: %v", res)
	}
	if _, has := got["recent_changes"]; has {
		t.Fatalf("还没有变化不该带 recent_changes: %v", got)
	}

	out := paramsDo(t, srv, "wf-main", map[string]any{"action": "set", "patch": map[string]any{"resident": "先读 docs/cache.md 再动手"}})
	applied, _ := out["applied"].([]any)
	if out["ok"] != true || out["level"] != "resident" || len(applied) != 1 {
		t.Fatalf("set 结果不对: %v", out)
	}
	a0, _ := applied[0].(map[string]any)
	if a0["field"] != "resident" || toInt(a0["rev"]) != 1 || a0["event_id"] == nil || a0["summary"] == "" {
		t.Fatalf("applied 条目不对: %v", a0)
	}
	if note, _ := out["note"].(string); !strings.Contains(note, "params: 行") || !strings.Contains(note, "撤销") {
		t.Fatalf("应讲明分支会在回执里被告知、用户能撤销: %v", out["note"])
	}
	row, err := srv.store.GetWorkflowPrompt(ctx, bwf)
	if err != nil || row.Resident != "先读 docs/cache.md 再动手" || row.ResidentRev != 1 {
		t.Fatalf("常驻规则没写进去: %+v %v", row, err)
	}
	if w := srv.store.ResidentWriter(ctx, bwf); w != "agent:wf-main" {
		t.Fatalf("来源应记为主会话: %q", w)
	}

	ev := eventsNamed(events, "branch_changed")
	last := ev[len(ev)-1]
	if last["change"] != "params" || last["parent"] != "wf-main" || last["branch"] != bwf {
		t.Fatalf("应广播 branch_changed(params): %v", last)
	}
	if fields, _ := last["fields"].([]string); len(fields) != 1 || fields[0] != "resident" {
		t.Fatalf("事件应带改了哪些字段: %v", last["fields"])
	}

	again := paramsDo(t, srv, "wf-main", map[string]any{"action": "get", "target": bwf})
	vals, _ = again["values"].(map[string]any)
	res, _ = vals["resident"].(map[string]any)
	if res["set"] != true || toInt(res["rev"]) != 1 || res["written_by"] != "main" || res["text"] != "先读 docs/cache.md 再动手" {
		t.Fatalf("再读应看到刚写的值与来源: %v", res)
	}
	recent, _ := again["recent_changes"].([]any)
	if len(recent) != 1 {
		t.Fatalf("recent_changes 应有 1 条: %v", again["recent_changes"])
	}
	if r0, _ := recent[0].(map[string]any); r0["field"] != "resident" || r0["by"] != "main" {
		t.Fatalf("recent_changes 条目不对: %v", r0)
	}

	// 写同样的值：不新增版本、不新增事件、不广播。
	before := len(eventsNamed(events, "branch_changed"))
	same := paramsDo(t, srv, "wf-main", map[string]any{"action": "set", "patch": map[string]any{"resident": "先读 docs/cache.md 再动手"}})
	if un, _ := same["unchanged"].([]any); same["ok"] != true || len(un) != 1 || un[0] != "resident" {
		t.Fatalf("同值应报 unchanged: %v", same)
	}
	if n := paramEventCount(t, srv, bwf); n != 1 {
		t.Fatalf("同值不该新增事件: %d", n)
	}
	if after := len(eventsNamed(events, "branch_changed")); after != before {
		t.Fatalf("同值不该广播: %d → %d", before, after)
	}
}

// patch 里的对象被模型序列化成 JSON 字符串时照样能用（部分客户端会这样传嵌套对象）。
func TestSessionParams_PatchGivenAsJSONString(t *testing.T) {
	srv, _ := branchFixture(t)
	bwf := paramsBranch(t, srv, "字符串 patch")
	out := paramsDo(t, srv, "wf-main", map[string]any{"action": "set", "patch": `{"resident": "字符串里的规则"}`})
	if out["ok"] != true {
		t.Fatalf("字符串形式的 patch 应被接受: %v", out)
	}
	if row, _ := srv.store.GetWorkflowPrompt(context.Background(), bwf); row.Resident != "字符串里的规则" {
		t.Fatalf("没写进去: %+v", row)
	}
	if msg := paramsFail(t, srv, "wf-main", map[string]any{"action": "set", "patch": `[1,2]`}); !strings.Contains(msg, "patch 需要是对象") {
		t.Fatalf("非对象应明说: %s", msg)
	}
}

// 超出档位的字段整条拒绝（常驻规则也不写）；永不开放的键被点名拒绝；分支不能改自己的参数；没开放的档位什么都看不到、改不了。
func TestSessionParams_PermissionBoundaries(t *testing.T) {
	srv, events := branchFixture(t)
	ctx := context.Background()
	bwf := paramsBranch(t, srv, "边界")

	msg := paramsFail(t, srv, "wf-main", map[string]any{"action": "set", "patch": map[string]any{"resident": "x", "phase": "dev"}})
	if !strings.Contains(msg, "phase") || !strings.Contains(msg, "整条没有写入") {
		t.Fatalf("越档应整条拒绝并说明: %s", msg)
	}
	if row, _ := srv.store.GetWorkflowPrompt(ctx, bwf); row.ResidentRev != 0 {
		t.Fatalf("整条拒绝时 resident 也不该写: %+v", row)
	}
	for _, key := range []string{"profile", "credentials", "directory", "host", "terminal", "launch", "expose", "params", "parent", "workflow_id", "strict_wrapup"} {
		if m := paramsFail(t, srv, "wf-main", map[string]any{"action": "set", "patch": map[string]any{key: "x"}}); !strings.Contains(m, "永不对主会话开放") {
			t.Fatalf("%s 应被点名拒绝: %s", key, m)
		}
	}
	// 分支自己：读得到，改不了。
	own := paramsDo(t, srv, bwf, map[string]any{"action": "get"})
	if own["role"] != "branch" || own["branch"] != bwf {
		t.Fatalf("分支读自己的参数: %v", own)
	}
	if note, _ := own["note"].(string); !strings.Contains(note, "你不能改自己的参数") {
		t.Fatalf("应讲明分支不能改自己的参数: %v", own["note"])
	}
	if m := paramsFail(t, srv, bwf, map[string]any{"action": "set", "patch": map[string]any{"resident": "我给自己加规则"}}); !strings.Contains(m, "分支不能改自己的参数") {
		t.Fatalf("分支改自己应被拒绝: %s", m)
	}
	if m := paramsFail(t, srv, bwf, map[string]any{"action": "set", "target": "wf-main", "patch": map[string]any{"phase": "dev"}}); !strings.Contains(m, "不是任何主会话的分支") && !strings.Contains(m, "不是你的分支") {
		t.Fatalf("分支改别的会话应被拒绝: %s", m)
	}
	if n := paramEventCount(t, srv, bwf); n != 0 {
		t.Fatalf("所有被拒的调用都不该留下事件: %d", n)
	}
	if got := eventsNamed(events, "branch_changed"); len(got) != 1 {
		t.Fatalf("被拒的调用不该广播（只有创建那一条）: %v", got)
	}

	// 用户把档位收回到 off：主会话看不到参数值，也改不了。
	setBranchParamsTier(t, srv, bwf, model.ExposeParamsOff)
	closed := paramsDo(t, srv, "wf-main", map[string]any{"action": "get"})
	if _, has := closed["values"]; has {
		t.Fatalf("没开放时不该泄露参数值: %v", closed)
	}
	if note, _ := closed["note"].(string); !strings.Contains(note, "没有向主会话开放") {
		t.Fatalf("应讲明需要用户打开: %v", closed["note"])
	}
	if m := paramsFail(t, srv, "wf-main", map[string]any{"action": "set", "patch": map[string]any{"resident": "x"}}); !strings.Contains(m, "没有向主会话开放") {
		t.Fatalf("off 档写应被拒绝: %s", m)
	}
}

// 越权调用：一个有分支、但目标不是自己分支的会话不能读写别人的分支。
func TestSessionParams_OtherMainCannotTouchBranch(t *testing.T) {
	srv, _ := branchFixture(t)
	ctx := context.Background()
	bwf := paramsBranch(t, srv, "wf-main 的分支")
	// wf-solo 自己也有一个分支，于是它「有关系」，越权才会走到权限检查。
	c := branchDo(t, srv, "wf-solo", map[string]any{"action": "create", "title": "wf-solo 的分支"})
	if c["ok"] != true {
		t.Fatalf("建分支失败: %v", c)
	}
	for _, args := range []map[string]any{
		{"action": "get", "target": bwf},
		{"action": "set", "target": bwf, "patch": map[string]any{"resident": "越权写"}},
	} {
		if m := paramsFail(t, srv, "wf-solo", args); !strings.Contains(m, "不是你的分支") {
			t.Fatalf("别人的分支应被拒绝: %v → %s", args, m)
		}
	}
	if row, _ := srv.store.GetWorkflowPrompt(ctx, bwf); row.ResidentRev != 0 {
		t.Fatalf("越权写不该落地: %+v", row)
	}
}

// 有多个活动分支时必须用 target 指明；写明有哪几个。
func TestSessionParams_MultipleBranchesNeedTarget(t *testing.T) {
	srv, _ := branchFixture(t)
	b1 := paramsBranch(t, srv, "分支甲")
	b2 := paramsBranch(t, srv, "分支乙")
	for _, args := range []map[string]any{
		{"action": "get"},
		{"action": "set", "patch": map[string]any{"resident": "x"}},
	} {
		m := paramsFail(t, srv, "wf-main", args)
		if !strings.Contains(m, "你有 2 个活动分支") || !strings.Contains(m, "分支甲") || !strings.Contains(m, "分支乙") {
			t.Fatalf("多分支缺 target 应列出各分支: %v → %s", args, m)
		}
	}
	out := paramsDo(t, srv, "wf-main", map[string]any{"action": "set", "target": b2, "patch": map[string]any{"resident": "只给乙"}})
	if out["branch"] != b2 {
		t.Fatalf("应写到指定的分支: %v", out)
	}
	if n1, n2 := paramEventCount(t, srv, b1), paramEventCount(t, srv, b2); n1 != 0 || n2 != 1 {
		t.Fatalf("只有乙该有事件: 甲 %d 乙 %d", n1, n2)
	}
}

// 档位升到 all：标题、阶段、唤醒词、异步（只能关）都能经工具写；标题改完会话列表里能看到。
func TestSessionParams_AllTierFields(t *testing.T) {
	srv, _ := branchFixture(t)
	ctx := context.Background()
	bwf := paramsBranch(t, srv, "全档")
	setBranchParamsTier(t, srv, bwf, model.ExposeParamsAll)

	got := paramsDo(t, srv, "wf-main", map[string]any{"action": "get"})
	if ed, _ := got["editable"].([]any); len(ed) != len(model.BranchParamFields) {
		t.Fatalf("all 档应能改全部字段: %v", got["editable"])
	}
	out := paramsDo(t, srv, "wf-main", map[string]any{"action": "set", "patch": map[string]any{
		"title": "缓存方案 v2", "phase": "plan", "wake_resume": "继续，工作流 %workflow", "resident": "规则甲",
	}})
	applied, _ := out["applied"].([]any)
	if out["ok"] != true || len(applied) != 4 {
		t.Fatalf("四个字段都应写入: %v", out)
	}
	if phase, _, err := srv.store.GetWorkflowPhaseWithDefaults(ctx, bwf); err != nil || phase != "plan" {
		t.Fatalf("阶段没改成 plan: %q %v", phase, err)
	}
	// 异步：从没被用户开启过，主会话开不了；关（本来就是关）算「没有变化」，不算写入。
	if m := paramsFail(t, srv, "wf-main", map[string]any{"action": "set", "patch": map[string]any{"async": true}}); !strings.Contains(m, "从没被用户开启过") {
		t.Fatalf("主会话不能第一次开启异步: %s", m)
	}
	off := paramsDo(t, srv, "wf-main", map[string]any{"action": "set", "patch": map[string]any{"async": false}})
	if un, _ := off["unchanged"].([]any); len(un) != 1 || un[0] != "async" {
		t.Fatalf("本来就关着，关闭应是 unchanged: %v", off)
	}
}

// 回执头里的 params: 行：主会话改过之后，分支下一次回执告知一次；告知之后不再重复；其它会话一个字都不多（INV-2）。
func TestSessionParams_BranchToldOnce(t *testing.T) {
	srv, events := branchFixture(t)
	ctx := context.Background()
	bwf := paramsBranch(t, srv, "回执里的参数变化")
	setBranchParamsTier(t, srv, bwf, model.ExposeParamsAll)

	// 还没改过：分支的 branch: 行是单行，没有 params:。
	if line := srv.branchHeaderLine(ctx, bwf); strings.Contains(line, "params:") || strings.Contains(line, "\n") {
		t.Fatalf("没有变化时不该有 params: 行: %q", line)
	}
	paramsDo(t, srv, "wf-main", map[string]any{"action": "set", "patch": map[string]any{"resident": "改动前先写测试", "title": "缓存方案 v2"}})
	stored, err := srv.store.ListBranchParamEvents(ctx, bwf, 10)
	if err != nil || len(stored) != 2 {
		t.Fatalf("应有两条事件: %v %v", stored, err)
	}

	// 别的会话看不到这条 params: 行，也不会把它「消费」掉。
	if line := srv.branchHeaderLine(ctx, "wf-main"); strings.Contains(line, "params:") {
		t.Fatalf("主会话的回执头不该有 params: %q", line)
	}
	if line := srv.branchHeaderLine(ctx, "wf-solo"); line != "" {
		t.Fatalf("没有关系的工作流不该有任何 branch / params 行: %q", line)
	}
	if untold, _ := srv.store.UntoldBranchParamEvents(ctx, bwf); len(untold) != 2 {
		t.Fatalf("别人的回执不该把事件记成已告知: %d", len(untold))
	}

	told := len(eventsNamed(events, "branch_changed"))
	line := srv.branchHeaderLine(ctx, bwf)
	parts := strings.Split(line, "\n")
	if len(parts) != 2 || !strings.HasPrefix(parts[0], "branch: 你是分支会话") || !strings.HasPrefix(parts[1], "params: 你的会话参数有变化——") {
		t.Fatalf("branch: 行后面应紧跟一行 params:: %q", line)
	}
	for _, want := range []string{"主会话：", "不是用户本人的指示", "不会因此放宽", "可在 Web 里撤销"} {
		if !strings.Contains(parts[1], want) {
			t.Fatalf("params: 行缺 %q: %s", want, parts[1])
		}
	}
	for _, ev := range stored {
		if !strings.Contains(parts[1], ev.Summary) {
			t.Fatalf("params: 行应带每条事件的摘要 %q: %s", ev.Summary, parts[1])
		}
	}
	if ev := eventsNamed(events, "branch_changed"); len(ev) != told+1 || ev[len(ev)-1]["change"] != "params_told" || ev[len(ev)-1]["branch"] != bwf {
		t.Fatalf("告知后应广播一次 params_told: %v", ev)
	}
	if untold, _ := srv.store.UntoldBranchParamEvents(ctx, bwf); len(untold) != 0 {
		t.Fatalf("告知后应记为已告知: %d", len(untold))
	}

	// 第二次：只剩 branch: 一行，也不再广播。
	told = len(eventsNamed(events, "branch_changed"))
	if again := srv.branchHeaderLine(ctx, bwf); strings.Contains(again, "params:") || strings.Contains(again, "\n") {
		t.Fatalf("已告知的变化不该重复: %q", again)
	}
	if after := len(eventsNamed(events, "branch_changed")); after != told {
		t.Fatalf("没有新告知就不该广播: %d → %d", told, after)
	}

	// 之后的新修改会再告知一次。
	paramsDo(t, srv, "wf-main", map[string]any{"action": "set", "patch": map[string]any{"phase": "dev"}})
	if next := srv.branchHeaderLine(ctx, bwf); !strings.Contains(next, "params: ") {
		t.Fatalf("新修改应再告知一次: %q", next)
	}
}

// 真实回执：params: 行紧跟在 branch: 行后面、在标记行之前，只出现一次。
func TestSessionParams_ParamsLineInRealReceipt(t *testing.T) {
	srv, _ := branchFixture(t)
	bwf := paramsBranch(t, srv, "真实回执")
	paramsDo(t, srv, "wf-main", map[string]any{"action": "set", "patch": map[string]any{"resident": "收尾前必须跑测试"}})

	fb := func() string {
		t.Helper()
		out := callToolText(t, srv, "interactive_feedback", map[string]any{
			"workflow_id": bwf, "project_directory": "/code/a", "summary": "分支汇报",
		})
		completeLatestPending(t, srv, bwf)
		return out
	}
	first := fb()
	bi, pi, mi := strings.Index(first, "\nbranch: "), strings.Index(first, "\nparams: "), strings.Index(first, "=== ")
	if bi < 0 || pi < 0 || !(bi < pi && pi < mi) {
		t.Fatalf("回执里应是 branch: → params: → 标记行（%d %d %d）:\n%s", bi, pi, mi, first)
	}
	if strings.Count(first, "\nparams: ") != 1 {
		t.Fatalf("params: 行只该出现一次:\n%s", first)
	}
	// 常驻规则这一版是主会话代写的：resident: 行用代写前缀，不冒充用户。
	if !strings.Contains(first, "resident: "+residentPrefixByMain) {
		t.Fatalf("resident: 行应写明是主会话代写的:\n%s", first)
	}
	if second := fb(); strings.Contains(second, "\nparams: ") {
		t.Fatalf("第二次回执不该再有 params: 行:\n%s", second)
	}
}

// formatBranchParamsLine：最近 5 条逐条列，更早的写成「另有 N 项」；只有用户本人的变化（比如撤销）时不带「代用户改」声明。
func TestFormatBranchParamsLine(t *testing.T) {
	mk := func(i int, by string) model.BranchParamEvent {
		return model.BranchParamEvent{ID: uint(i), By: by, Summary: fmt.Sprintf("第%d项变化", i)}
	}
	if got := formatBranchParamsLine(nil); got != "" {
		t.Fatalf("没有事件应是空串: %q", got)
	}
	var many []model.BranchParamEvent
	for i := 1; i <= 7; i++ {
		many = append(many, mk(i, "agent:wf-main"))
	}
	line := formatBranchParamsLine(many)
	for i := 3; i <= 7; i++ {
		if !strings.Contains(line, fmt.Sprintf("主会话：第%d项变化", i)) {
			t.Fatalf("应列出最近 5 条，缺第 %d 项: %s", i, line)
		}
	}
	if strings.Contains(line, "第1项变化") || strings.Contains(line, "第2项变化") || !strings.Contains(line, "另有 2 项更早的变化") {
		t.Fatalf("更早的应折叠成「另有 2 项」: %s", line)
	}
	if strings.Contains(line, "\n") {
		t.Fatalf("params: 行必须是单行（否则会冒充别的回执头 key）: %q", line)
	}

	users := formatBranchParamsLine([]model.BranchParamEvent{mk(1, "alice"), mk(2, "alice")})
	if strings.Contains(users, "不是用户本人的指示") || strings.Contains(users, "主会话：") {
		t.Fatalf("只有用户本人的变化不该带「代用户改」声明: %s", users)
	}
	mixed := formatBranchParamsLine([]model.BranchParamEvent{mk(1, "alice"), mk(2, "agent:wf-main")})
	if !strings.Contains(mixed, "第1项变化；主会话：第2项变化") || !strings.Contains(mixed, "不是用户本人的指示") {
		t.Fatalf("混合时用户的条目不加「主会话：」，仍带声明: %s", mixed)
	}
}

// 用户撤销了主会话的修改：分支被告知的是「用户撤销了…」，不带主会话的代写声明；常驻规则前缀随之恢复。
func TestSessionParams_UserRevertToldWithoutDelegationNotice(t *testing.T) {
	srv, _ := branchFixture(t)
	ctx := context.Background()
	bwf := paramsBranch(t, srv, "撤销")
	paramsDo(t, srv, "wf-main", map[string]any{"action": "set", "patch": map[string]any{"resident": "主会话写的规则"}})
	if line := srv.branchHeaderLine(ctx, bwf); !strings.Contains(line, "params: ") {
		t.Fatalf("先告知主会话的修改: %q", line)
	}
	evs, _ := srv.store.ListBranchParamEvents(ctx, bwf, 10)
	if len(evs) != 1 {
		t.Fatalf("应有一条事件: %v", evs)
	}
	if _, err := srv.store.RevertBranchParamEvent(ctx, bwf, evs[0].ID, "alice", time.Now()); err != nil {
		t.Fatalf("撤销失败: %v", err)
	}
	line := srv.branchHeaderLine(ctx, bwf)
	if !strings.Contains(line, "params: ") || strings.Contains(line, "不是用户本人的指示") {
		t.Fatalf("撤销应被告知，且不带代写声明: %q", line)
	}
	if w := srv.store.ResidentWriter(ctx, bwf); strings.HasPrefix(w, "agent:") {
		t.Fatalf("撤销后常驻规则的来源应是用户: %q", w)
	}
	if block := srv.residentNoticeBlock(ctx, bwf, true); strings.Contains(block, residentPrefixByMain) {
		t.Fatalf("撤销后不该再用代写前缀: %s", block)
	}
}

// 常驻规则回执前缀跟着来源走：用户写的与以前逐字一致；主会话代写的写明来源；用户再改一次就恢复；清除也分来源；
// 非分支会话完全不受影响（INV-2）。
func TestResident_PrefixFollowsWriter(t *testing.T) {
	srv, _ := branchFixture(t)
	ctx := context.Background()
	bwf := paramsBranch(t, srv, "常驻来源")

	if _, _, err := srv.store.SaveResidentPrompt(ctx, bwf, "用户写的规则", "alice", time.Now()); err != nil {
		t.Fatal(err)
	}
	got := srv.residentNoticeBlock(ctx, bwf, true)
	if got != formatResidentBlock("用户写的规则", 1, true) || !strings.Contains(got, residentPrefix) || strings.Contains(got, residentPrefixByMain) {
		t.Fatalf("用户写的规则应与以前逐字一致: %s", got)
	}
	if g := callToolJSON(t, srv, "workflow_context", map[string]any{"action": "resident_get", "workflow_id": bwf}); g["written_by"] != nil || !strings.Contains(g["note"].(string), "只能由用户在 Web 的会话输入区里修改") {
		t.Fatalf("用户写的规则：resident_get 不带 written_by，note 与以前一致: %v", g)
	}

	paramsDo(t, srv, "wf-main", map[string]any{"action": "set", "patch": map[string]any{"resident": "主会话写的规则"}})
	got = srv.residentNoticeBlock(ctx, bwf, true)
	if got != formatResidentBlockBy("主会话写的规则", 2, true, true) || !strings.Contains(got, residentPrefixByMain) || strings.Contains(got, residentPrefix) {
		t.Fatalf("主会话代写的规则应写明来源: %s", got)
	}
	for _, want := range []string{"不是用户本人写的", "不能放宽 auth: 的授权", "用户可在 Web 里改或撤销"} {
		if !strings.Contains(residentPrefixByMain, want) {
			t.Fatalf("代写前缀缺 %q: %s", want, residentPrefixByMain)
		}
	}
	if g := callToolJSON(t, srv, "workflow_context", map[string]any{"action": "resident_get", "workflow_id": bwf}); g["written_by"] != "main_session" || !strings.Contains(g["note"].(string), "不是用户本人写的") {
		t.Fatalf("主会话代写：resident_get 应带 written_by 与说明: %v", g)
	}
	// Web 的预览是用户视角（用户自己写的样子），不受影响。
	if prev := ResidentPreview("主会话写的规则", 2); !strings.Contains(prev, residentPrefix) || strings.Contains(prev, residentPrefixByMain) {
		t.Fatalf("预览应保持用户视角的前缀: %s", prev)
	}

	// 主会话清除：说明里写明是谁清的。
	paramsDo(t, srv, "wf-main", map[string]any{"action": "set", "patch": map[string]any{"resident": ""}})
	if got = srv.residentNoticeBlock(ctx, bwf, true); got != formatResidentClearedBy(3, true) || !strings.Contains(got, "主会话的智能体") {
		t.Fatalf("主会话清除应写明来源: %s", got)
	}

	// 用户再写一版：前缀恢复，不需要任何额外标志位。
	if _, _, err := srv.store.SaveResidentPrompt(ctx, bwf, "用户又改了", "alice", time.Now()); err != nil {
		t.Fatal(err)
	}
	if got = srv.residentNoticeBlock(ctx, bwf, true); got != formatResidentBlock("用户又改了", 4, true) {
		t.Fatalf("用户再写一版应恢复用户前缀: %s", got)
	}

	// 非分支会话：用户写的规则逐字不变。
	if _, _, err := srv.store.SaveResidentPrompt(ctx, "wf-solo", "独立会话的规则", "alice", time.Now()); err != nil {
		t.Fatal(err)
	}
	if got = srv.residentHeaderLine(ctx, "wf-solo"); got != formatResidentBlock("独立会话的规则", 1, true) {
		t.Fatalf("非分支会话的常驻块应与以前逐字一致: %s", got)
	}
	if g := callToolJSON(t, srv, "workflow_context", map[string]any{"action": "resident_get", "workflow_id": "wf-solo"}); g["written_by"] != nil {
		t.Fatalf("非分支会话不该有 written_by: %v", g)
	}
}

// 工具注册与权限：凭据能用 chat / branch 就能用 session_params；tools/list 里有它；对象 schema 里列出全部可改字段。
func TestSessionParams_ToolRegisteredAndPermissionsFollowBranch(t *testing.T) {
	for mask := 0; mask < 1<<6; mask++ {
		p := model.Permissions{
			Configure: mask&1 != 0, Execute: mask&2 != 0, Feedback: mask&4 != 0,
			Sessions: mask&8 != 0, SystemInfo: mask&16 != 0, Skills: mask&32 != 0,
		}
		if p.AllowsTool("session_params") != p.AllowsTool("branch") {
			t.Fatalf("权限 %+v：session_params 与 branch 应同进退", p)
		}
	}
	found := false
	for _, d := range GetToolDefinitionsForPermissions(model.Permissions{Feedback: true}) {
		if d.Name != "session_params" {
			continue
		}
		found = true
		schema := string(d.InputSchema)
		for _, f := range model.BranchParamFields {
			if !strings.Contains(schema, `"`+f+`"`) {
				t.Fatalf("schema 里应列出可改字段 %s: %s", f, schema)
			}
		}
		if !strings.Contains(schema, `"enum": ["get", "set"]`) {
			t.Fatalf("schema 里应列出动作: %s", schema)
		}
	}
	if !found {
		t.Fatal("tools/list 里应有 session_params")
	}
	// 读调用不算「干活」（缺省 action 等同 get），写入算。
	for action, want := range map[string]bool{"": true, "get": true, "GET": true, "set": false} {
		if got := isQueryCall("session_params", action); got != want {
			t.Fatalf("isQueryCall(session_params, %q) = %v，期望 %v", action, got, want)
		}
	}
}

// 分支回执头里开放范围的一句话写明「可改参数」；branch 工具的 list 里 expose_to_main 带 params 档位。
func TestSessionParams_ExposeWordingForBranch(t *testing.T) {
	srv, _ := branchFixture(t)
	ctx := context.Background()
	bwf := paramsBranch(t, srv, "措辞")
	if bl := srv.branchHeaderLine(ctx, bwf); !strings.Contains(bl, "可改参数：常驻规则") {
		t.Fatalf("协作预设的 branch: 行应写「可改参数：常驻规则」: %s", bl)
	}
	setBranchParamsTier(t, srv, bwf, model.ExposeParamsOff)
	if bl := srv.branchHeaderLine(ctx, bwf); !strings.Contains(bl, "不可改参数") {
		t.Fatalf("没开放时应写「不可改参数」: %s", bl)
	}
	list := branchDo(t, srv, bwf, map[string]any{"action": "list"})
	exp, _ := list["expose_to_main"].(map[string]any)
	if exp["params"] != "off" {
		t.Fatalf("expose_to_main 应带 params 档位: %v", exp)
	}
}

// 主会话改参数之后，除了 branch_changed(params)，字段原本在网页上的展示也各发它们的刷新事件：
// 阶段滑块（phase_changed）、常驻规则 / 唤醒词弹窗（workflow_prompts_updated，两者合并成一条）、标题（session_updated，
// 载荷是会话对象，由 Server 的广播钩子统一规整）；什么都没改时一个事件都不发。
func TestSessionParams_BroadcastsFieldEvents(t *testing.T) {
	srv, events := branchFixture(t)
	bwf := paramsBranch(t, srv, "广播分支")
	setBranchParamsTier(t, srv, bwf, model.ExposeParamsAll)
	named := func(name string) []workEvent {
		var out []workEvent
		for _, e := range *events {
			if e.Name == name {
				out = append(out, e)
			}
		}
		return out
	}

	*events = nil
	patch := map[string]any{"resident": "规则：先写测试", "phase": "dev", "title": "主会话改的标题", "wake_resume": "请继续"}
	paramsDo(t, srv, "wf-main", map[string]any{"action": "set", "patch": patch})

	if got := named("workflow_prompts_updated"); len(got) != 1 {
		t.Fatalf("常驻规则 + 唤醒词只发一条 workflow_prompts_updated: %+v", got)
	} else if m, _ := got[0].Data.(map[string]string); m["workflow_id"] != bwf {
		t.Fatalf("载荷应指向分支: %+v", got[0].Data)
	}
	if got := named("phase_changed"); len(got) != 1 {
		t.Fatalf("应发一条 phase_changed: %+v", got)
	} else if m, _ := got[0].Data.(map[string]string); m["workflow_id"] != bwf || m["phase"] != model.PhaseDev {
		t.Fatalf("phase_changed 载荷不对: %+v", got[0].Data)
	}
	if got := named("session_updated"); len(got) != 1 {
		t.Fatalf("应发一条 session_updated: %+v", got)
	} else if sess, ok := got[0].Data.(*model.FeedbackSession); !ok || sess.WorkflowID != bwf || sess.Title != "主会话改的标题" {
		t.Fatalf("session_updated 的载荷应是改名后的会话对象: %+v", got[0].Data)
	}
	if got := eventsNamed(events, "branch_changed"); len(got) != 1 || got[0]["change"] != "params" {
		t.Fatalf("仍应发 branch_changed(params): %+v", got)
	}
	if got := named("workflow_mode_updated"); len(got) != 0 {
		t.Fatalf("没改异步开关就不该发 workflow_mode_updated: %+v", got)
	}

	// 一样的取值再来一遍：没有任何字段被写入，也就一个事件都不发。
	*events = nil
	paramsDo(t, srv, "wf-main", map[string]any{"action": "set", "patch": patch})
	if len(*events) != 0 {
		t.Fatalf("什么都没改时不该广播: %+v", *events)
	}
}
