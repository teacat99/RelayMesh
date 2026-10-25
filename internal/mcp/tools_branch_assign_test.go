package mcp

import (
	"context"
	"strings"
	"testing"

	"github.com/teacat99/RelayMesh/v2/internal/model"
	"github.com/teacat99/RelayMesh/v2/internal/store"
)

// 主会话给分支派单（#236 P5）的 MCP 侧：newWorkPolicy 把主会话与各分支的关系放进 WorkActor，store 层的带范围例外据此放行；
// branch list 写明「派单」的开没开 / 是否同板与下一步；派单与结束分支的回执提示。

// branchAssignFixture 在 policyFixture 的基础上建分支关系：wf-a 是主会话；wf-b 协作（任务 = 派单）、wf-c 仅可见（任务 = 只读）、
// wf-solo 协作但不在项目的板上。
func branchAssignFixture(t *testing.T) *policyFixture {
	t.Helper()
	f := newPolicyFixture(t)
	visible, _ := model.BranchExposePreset(model.ExposePresetVisible)
	for branch, exp := range map[string]*model.BranchExpose{"wf-b": nil, "wf-c": &visible, "wf-solo": nil} {
		if _, err := f.srv.store.LinkBranch(context.Background(), store.LinkBranchInput{
			ParentWorkflowID: "wf-a", BranchWorkflowID: branch, CreatedBy: model.BranchCreatedByHuman, Expose: exp, SkipRateLimit: true,
		}); err != nil {
			t.Fatalf("LinkBranch %s: %v", branch, err)
		}
	}
	return f
}

func assignedTo(t *testing.T, f *policyFixture, wf string, seq int) (assignee, assigner, claim string) {
	t.Helper()
	it, err := f.srv.store.GetWorkItemBySeq(context.Background(), wf, seq)
	if err != nil {
		t.Fatal(err)
	}
	return it.AssigneeWorkflowID, it.AssignedByWorkflowID, it.ClaimState
}

// branch list（主会话）：每个分支一个 assign 概况，三种情形各有下一步；开放范围多一项 tasks，摘要写「可派任务 / 不可派任务」。
func TestBranchListAssignView(t *testing.T) {
	f := branchAssignFixture(t)
	out := branchDo(t, f.srv, "wf-a", map[string]any{"action": "list"})
	byBranch := map[string]map[string]any{}
	for _, it := range asList(out["branches"]) {
		m := asMap(it)
		byBranch[m["branch"].(string)] = m
	}
	check := func(branch string, open, shared bool, howSub, exposeTasks, summarySuffix string) {
		t.Helper()
		item := byBranch[branch]
		if item == nil {
			t.Fatalf("branch list 里没有 %s: %v", branch, out)
		}
		a := asMap(item["assign"])
		if a == nil || a["open"] != open || a["board_shared"] != shared {
			t.Fatalf("%s 的 assign = %v，期望 open=%v board_shared=%v", branch, item["assign"], open, shared)
		}
		if how, _ := a["how"].(string); !strings.Contains(how, howSub) {
			t.Fatalf("%s 的 how 应含 %q: %v", branch, howSub, a["how"])
		}
		ex := asMap(item["expose"])
		if ex["tasks"] != exposeTasks || !strings.HasSuffix(ex["summary"].(string), summarySuffix) {
			t.Fatalf("%s 的开放范围: %v", branch, ex)
		}
	}
	check("wf-b", true, true, "assignee: \"wf-b\"", "assign", "，可派任务")
	check("wf-c", false, true, "把「任务」打开", "read", "，不可派任务")
	check("wf-solo", true, false, "登记进你所在的项目", "assign", "，可派任务")

	// 分支一侧：开放了派单就告诉它怎么接、怎么交；只读的没有这句。
	b := branchDo(t, f.srv, "wf-b", map[string]any{"action": "list"})
	if b["role"] != "branch" || asMap(b["expose_to_main"])["tasks"] != "assign" {
		t.Fatalf("分支视角: %v", b)
	}
	if s, _ := b["tasks_from_main"].(string); !strings.Contains(s, "claim=accept") || !strings.Contains(s, "claim=deliver") {
		t.Fatalf("开放了派单的分支应被告知怎么接取 / 交付: %v", b["tasks_from_main"])
	}
	c := branchDo(t, f.srv, "wf-c", map[string]any{"action": "list"})
	if _, has := c["tasks_from_main"]; has {
		t.Fatalf("只读的分支不该有 tasks_from_main: %v", c)
	}
}

// enforce 档整圈：主会话（普通 Agent，没有管理授权）派给自己的分支 → 分支接取 → 交付 → 主会话验收；负例拒绝文案点明原因；成功的派单不产生拒绝审计。
func TestBranchAssignEnforceFullLoop(t *testing.T) {
	f := branchAssignFixture(t)
	f.mode("enforce")
	seq := f.batchTask("wf-a", "改接口")

	res := f.update("wf-a", seq, map[string]any{"assignee": "wf-b"})
	if res["status"] != "updated" || hintOf(res) != "" {
		t.Fatalf("主会话派给自己开放了派单的分支应成功且没有违规提示: %v", res)
	}
	if h, _ := res["branch_hint"].(string); !strings.Contains(h, "wf-b") || !strings.Contains(h, "不会因为派单被叫醒") || !strings.Contains(h, "branch(action: \"say\"") {
		t.Fatalf("派单回执应提示「放进待办、不会叫醒」: %v", res["branch_hint"])
	}
	if a, by, claim := assignedTo(t, f, "wf-a", seq); a != "wf-b" || by != "wf-a" || claim != model.WorkClaimOffered {
		t.Fatalf("指派字段: %s %s %s", a, by, claim)
	}
	if n := len(f.taskAudits()); n != 0 {
		t.Fatalf("成功的派单不该有拒绝审计: %d", n)
	}

	// 同一次写入里没有指派动作（只改进展）：不再重复提示。
	prog := f.update("wf-a", seq, map[string]any{"note": "补充说明"})
	if _, has := prog["branch_hint"]; has {
		t.Fatalf("没有指派动作就不该提示: %v", prog)
	}

	// 分支接取、交付；主会话验收。
	acc := f.update("wf-b", seq, map[string]any{"claim": "accept"})
	if asMap(acc["task"])["state"] != "doing" {
		t.Fatalf("接取后应是 doing: %v", acc)
	}
	dlv := f.update("wf-b", seq, map[string]any{"claim": "deliver", "note": "已完成并验证", "conclusion": "接口改造完成，e2e 通过"})
	if asMap(dlv["task"])["state"] != "review" {
		t.Fatalf("交付后应是 review: %v", dlv)
	}
	f.updateFails("wf-b", seq, map[string]any{"claim": "accept_delivery"}, "验收")
	done := f.update("wf-a", seq, map[string]any{"claim": "accept_delivery"})
	if asMap(done["task"])["state"] != "done" || hintOf(done) != "" {
		t.Fatalf("主会话验收应通过: %v", done)
	}

	// 负例：派给只读的分支 / 派给自己 / 分支自己派（原文案不变）/ 别的工作流建的任务 / 不在这张板上的分支。
	s2 := f.batchTask("wf-a", "负例任务")
	f.updateFails("wf-a", s2, map[string]any{"assignee": "wf-c"}, "开放范围里「任务」是只读")
	f.updateFails("wf-a", s2, map[string]any{"assignee": "wf-a"}, "不是你的分支")
	f.updateFails("wf-a", s2, map[string]any{"assignee": "wf-solo"}, "不在这张任务板上")
	text := f.updateFails("wf-b", s2, map[string]any{"assignee": "wf-c"}, "指派 / 改派 / 撤销指派只有管理 Agent 或人能做")
	if strings.Contains(text, "你是主会话") {
		t.Fatalf("没有分支的普通 Agent 的拒绝文案不该附带分支说明: %s", text)
	}
	cTask := f.batchTask("wf-c", "别人的任务")
	f.updateFails("wf-a", cTask, map[string]any{"assignee": "wf-b"}, "主会话只能派自己创建的任务")
	if a, _, _ := assignedTo(t, f, "wf-a", s2); a != "" {
		t.Fatalf("被拒绝的指派不该落库: %s", a)
	}
	// 真实拒绝记审计（同一目标同一原因 30 分钟内只记一条，所以不断言条数）：enforce 档一律是 would_deny=false。
	audits := f.taskAudits()
	if len(audits) == 0 {
		t.Fatal("enforce 档的拒绝应记审计")
	}
	for _, e := range audits {
		if !strings.Contains(e.Detail, `"would_deny":false`) {
			t.Fatalf("enforce 档的真实拒绝应记 would_deny=false: %s", e.Detail)
		}
	}
}

// task_create 带承接者、task_board batch 里的指派，同样放行并给提示；撤销自己派出的指派放行。
func TestBranchAssignViaCreateAndBatch(t *testing.T) {
	f := branchAssignFixture(t)
	f.mode("enforce")

	created := f.do("workflow_context", f.as("wf-a", map[string]any{"action": "task_create", "title": "创建时就派", "kind": "feature", "description": "d", "assignee": "wf-b"}))
	if created["status"] != "created" || !strings.Contains(created["branch_hint"].(string), "wf-b") {
		t.Fatalf("task_create 带承接者: %v", created)
	}
	seq1 := int(asMap(created["task"])["task_id"].(float64))
	if a, by, _ := assignedTo(t, f, "wf-a", seq1); a != "wf-b" || by != "wf-a" {
		t.Fatalf("创建时指派: %s %s", a, by)
	}

	seq2 := f.batchTask("wf-a", "批量里派")
	batch := f.do("task_board", f.as("wf-a", map[string]any{"action": "batch", "ops": []any{
		map[string]any{"task_id": seq2, "assignee": "wf-b"},
		map[string]any{"title": "批量新建并派", "kind": "feature", "assignee": "wf-b"},
	}}))
	if h, _ := batch["branch_hint"].(string); !strings.Contains(h, "wf-b") || strings.Contains(h, " ｜ ") {
		t.Fatalf("同一个分支的提示应只出现一次: %v", batch["branch_hint"])
	}
	if a, _, _ := assignedTo(t, f, "wf-a", seq2); a != "wf-b" {
		t.Fatalf("批量更新里的指派: %s", a)
	}

	// 撤销自己派出的指派：放行；撤销之后可以再派给别的开放的分支（这里没有第二个开放的，验证撤销本身即可）。
	rev := f.update("wf-a", seq1, map[string]any{"assignee": ""})
	if rev["status"] != "updated" {
		t.Fatalf("撤销自己派出的指派: %v", rev)
	}
	if a, by, claim := assignedTo(t, f, "wf-a", seq1); a != "" || by != "" || claim != "" {
		t.Fatalf("撤销应清空指派: %s %s %s", a, by, claim)
	}
	if rev["branch_hint"] != nil {
		t.Fatalf("撤销不该给派单提示: %v", rev)
	}
	if n := len(f.taskAudits()); n != 0 {
		t.Fatalf("全程不该有拒绝审计: %d", n)
	}
}

// audit 档：主会话派给自己的分支不再记违规；派给只读的分支照常执行但记一条（带原因）。
func TestBranchAssignAuditMode(t *testing.T) {
	f := branchAssignFixture(t)
	f.mode("audit")
	s1 := f.batchTask("wf-a", "审计 1")
	ok := f.update("wf-a", s1, map[string]any{"assignee": "wf-b"})
	if hintOf(ok) != "" || len(f.taskAudits()) != 0 {
		t.Fatalf("audit 档：派给开放了派单的分支不该记违规: %v audits=%d", ok, len(f.taskAudits()))
	}
	s2 := f.batchTask("wf-a", "审计 2")
	bad := f.update("wf-a", s2, map[string]any{"assignee": "wf-c"})
	if bad["status"] != "updated" || !strings.Contains(hintOf(bad), "只读") {
		t.Fatalf("audit 档照常执行并提示原因: %v", bad)
	}
	audits := f.taskAudits()
	if len(audits) != 1 || !strings.Contains(audits[0].Detail, `"would_deny":true`) || !strings.Contains(audits[0].Detail, "只读") {
		t.Fatalf("应记一条 would_deny 审计并写明原因: %+v", audits)
	}
}

// 项目关了访问控制（off 档）：不做任务级判定，派单提示照给（提示不依赖策略）。
func TestBranchAssignOffModeStillHints(t *testing.T) {
	f := branchAssignFixture(t)
	f.mode("off")
	seq := f.batchTask("wf-a", "off 档")
	res := f.update("wf-a", seq, map[string]any{"assignee": "wf-b"})
	if res["status"] != "updated" || hintOf(res) != "" || !strings.Contains(res["branch_hint"].(string), "wf-b") {
		t.Fatalf("off 档派单: %v", res)
	}
}

// 派单提示只给「主会话派自己的分支」：项目管理 Agent 把任务派给别人的分支（wf-b 是 wf-a 的分支、不是 wf-c 的），不提示。
func TestBranchAssignHintOnlyForOwnBranch(t *testing.T) {
	f := branchAssignFixture(t)
	f.mode("enforce")
	f.grantTasks("wf-c") // wf-c 是 wf-a 的另一个分支，同时是项目管理 Agent：能派任务给板上任何工作流
	seq := f.batchTask("wf-c", "管理者的任务")
	res := f.update("wf-c", seq, map[string]any{"assignee": "wf-b"})
	if res["status"] != "updated" {
		t.Fatalf("管理 Agent 应能派给板上的工作流: %v", res)
	}
	if _, has := res["branch_hint"]; has {
		t.Fatalf("不是派给调用方自己的分支，不该给派单提示: %v", res["branch_hint"])
	}
}

// 结束分支：派给它、还没交付的任务退回主会话，回执写明；已交付待验收的保留。
func TestBranchCloseReturnsTasksMCP(t *testing.T) {
	f := branchAssignFixture(t)
	f.mode("enforce")
	todo := f.batchTask("wf-a", "待接取")
	doing := f.batchTask("wf-a", "进行中")
	review := f.batchTask("wf-a", "待验收")
	for _, s := range []int{todo, doing, review} {
		f.update("wf-a", s, map[string]any{"assignee": "wf-b"})
	}
	f.update("wf-b", doing, map[string]any{"claim": "accept"})
	f.update("wf-b", review, map[string]any{"claim": "accept"})
	f.update("wf-b", review, map[string]any{"claim": "deliver", "note": "交付", "conclusion": "做完了"})

	out := branchDo(t, f.srv, "wf-a", map[string]any{"action": "close", "branch": "wf-b"})
	rt := asList(out["returned_tasks"])
	if len(rt) != 2 || int(rt[0].(float64)) != todo || int(rt[1].(float64)) != doing {
		t.Fatalf("应退回待接取与进行中的两个任务: %v", out)
	}
	if note, _ := out["returned_note"].(string); !strings.Contains(note, "已退回你名下") || !strings.Contains(note, "等你验收") {
		t.Fatalf("回执应写明退回与验收: %v", out["returned_note"])
	}
	for _, s := range []int{todo, doing} {
		if a, by, claim := assignedTo(t, f, "wf-a", s); a != "" || by != "" || claim != "" {
			t.Fatalf("任务 #%d 应已退回: %s %s %s", s, a, by, claim)
		}
	}
	if a, by, _ := assignedTo(t, f, "wf-a", review); a != "wf-b" || by != "wf-a" {
		t.Fatalf("待验收的任务不该被动: %s %s", a, by)
	}

	// 没有任务可退的分支：回执里没有 returned_* 字段（与升级前一致）。
	out = branchDo(t, f.srv, "wf-a", map[string]any{"action": "close", "branch": "wf-c"})
	if _, has := out["returned_tasks"]; has {
		t.Fatalf("没有任务可退就不该出现 returned_tasks: %v", out)
	}
	if _, has := out["returned_note"]; has {
		t.Fatalf("没有任务可退就不该出现 returned_note: %v", out)
	}
}
