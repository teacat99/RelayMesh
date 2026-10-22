package mcp

import (
	"context"
	"strconv"
	"strings"
	"testing"

	"github.com/teacat99/RelayMesh/v2/internal/model"
	"github.com/teacat99/RelayMesh/v2/internal/store"
)

// policyFixture 建一个项目（目录 a / b / c 各一个工作流，身份互不相同）与一个项目外的工作流 wf-solo。
type policyFixture struct {
	t       *testing.T
	srv     *Server
	project *model.Project
	ids     map[string]uint // 工作流 → 身份编号
	dirs    map[string]string
}

func newPolicyFixture(t *testing.T) *policyFixture {
	t.Helper()
	srv, _ := setupWorkItemMCP(t)
	ctx := context.Background()
	st := srv.store
	f := &policyFixture{t: t, srv: srv, ids: map[string]uint{}, dirs: map[string]string{"wf-a": "/code/a", "wf-b": "/code/b", "wf-c": "/code/c", "wf-solo": "/code/solo"}}
	p, err := st.CreateProject(ctx, store.ProjectInput{Name: "P", Slug: "p"})
	if err != nil {
		t.Fatal(err)
	}
	f.project = p
	for wf, dir := range f.dirs {
		if wf != "wf-solo" {
			if _, err := st.AddProjectDir(ctx, p.ID, "h", dir, ""); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := st.CreateFeedbackSession(ctx, store.CreateSessionInput{SessionID: "s-" + wf, WorkflowID: wf, EnvHostName: "h", ProjectDirectory: dir, Title: "t", Summary: "s"}); err != nil {
			t.Fatal(err)
		}
		id, _ := st.ResolveCallerIdentity(ctx, wf, 0, "h", dir, timeNow())
		if id == 0 {
			t.Fatalf("%s 应得到身份", wf)
		}
		f.ids[wf] = id
	}
	return f
}

func (f *policyFixture) mode(m string) {
	f.t.Helper()
	if _, err := f.srv.store.UpdateProject(context.Background(), f.project.ID, store.ProjectPatch{ACLMode: &m}); err != nil {
		f.t.Fatal(err)
	}
}

// as 以某个工作流的身份发调用（带断言）。
func (f *policyFixture) as(wf string, m map[string]any) map[string]any {
	out := map[string]any{"workflow_id": wf, "project_directory": f.dirs[wf], "host_name": "h"}
	for k, v := range m {
		out[k] = v
	}
	return out
}

func (f *policyFixture) do(tool string, args map[string]any) map[string]any {
	f.t.Helper()
	return callToolJSON(f.t, f.srv, tool, args)
}

func (f *policyFixture) fail(tool string, args map[string]any, sub string) string {
	f.t.Helper()
	text, isErr := callToolRaw(f.t, f.srv, tool, args)
	if !isErr || !strings.Contains(text, sub) {
		f.t.Fatalf("%s %v 应失败并含 %q: err=%v %s", tool, args, sub, isErr, text)
	}
	return text
}

// batchTask 用 task_board batch 建一个任务（创建者 = wf），返回任务号。
func (f *policyFixture) batchTask(wf, title string) int {
	f.t.Helper()
	res := f.do("task_board", f.as(wf, map[string]any{"action": "batch", "ops": []any{map[string]any{"title": title, "kind": "feature"}}}))
	r := asList(res["results"])
	if len(r) != 1 {
		f.t.Fatalf("batch 回执异常: %v", res)
	}
	ref, _ := asMap(r[0])["ref"].(string)
	seq, err := strconv.Atoi(strings.TrimPrefix(ref, "#"))
	if err != nil || seq <= 0 {
		f.t.Fatalf("batch 回执里没有任务号: %v", res)
	}
	return seq
}

func (f *policyFixture) update(wf string, seq int, extra map[string]any) map[string]any {
	f.t.Helper()
	args := map[string]any{"action": "task_update", "task_id": seq}
	for k, v := range extra {
		args[k] = v
	}
	return f.do("workflow_context", f.as(wf, args))
}

func (f *policyFixture) updateFails(wf string, seq int, extra map[string]any, sub string) string {
	f.t.Helper()
	args := map[string]any{"action": "task_update", "task_id": seq}
	for k, v := range extra {
		args[k] = v
	}
	return f.fail("workflow_context", f.as(wf, args), sub)
}

// taskAudits 取任务级权限的审计（acl.denied，范围 tasks）。
func (f *policyFixture) taskAudits() []model.AuditEvent {
	f.t.Helper()
	items, err := f.srv.store.ListAuditEvents(context.Background(), store.AuditFilter{Action: model.AuditACLDenied, Result: model.AuditDeny, Limit: 200})
	if err != nil {
		f.t.Fatal(err)
	}
	var out []model.AuditEvent
	for _, e := range items {
		if e.ScopeKind == model.ScopeTasks {
			out = append(out, e)
		}
	}
	return out
}

func (f *policyFixture) grantTasks(wf string) *model.ACLGrant {
	f.t.Helper()
	g, err := f.srv.store.CreateGrant(context.Background(), store.GrantInput{
		ProjectID: f.project.ID, PrincipalKind: model.PrincipalIdentity, PrincipalRef: strconv.Itoa(int(f.ids[wf])),
		ScopeKind: model.ScopeTasks, Role: model.RoleEdit, GrantedByKind: "human", GrantedByRef: "human", GrantedByLabel: "你",
	})
	if err != nil {
		f.t.Fatal(err)
	}
	return g
}

func hintOf(res map[string]any) string {
	s, _ := res["policy_hint"].(string)
	return s
}

// 任务级权限的接线（#175 B2c）：audit 档照常执行并记「若启用会被拒绝」，enforce 档真正拒绝；
// 项目管理 Agent（用户在项目页指定 = tasks 范围授权）能派任务、改所有人的任务；撤销后立刻失效。
func TestTaskPolicyAuditManagersAndEnforce(t *testing.T) {
	f := newPolicyFixture(t)
	f.mode("audit")

	// 各自的任务：A 建 #1 #2 #3（此时一切正常，不产生任何违规）。
	t1 := f.batchTask("wf-a", "迁移脚本")
	t2 := f.batchTask("wf-a", "回滚演练")
	t3 := f.batchTask("wf-a", "清理旧表")
	if t1 != 1 || t2 != 2 || t3 != 3 {
		t.Fatalf("任务号应是 1 2 3: %d %d %d", t1, t2, t3)
	}
	if n := len(f.taskAudits()); n != 0 {
		t.Fatalf("自己写自己的任务不该有违规审计: %d", n)
	}

	// audit 档：普通 Agent B 改 A 的任务——照常执行，回执里提醒，审计记 would_deny。
	res := f.update("wf-b", t1, map[string]any{"progress": "B 顺手补了一句"})
	if res["status"] != "updated" || !strings.Contains(hintOf(res), "audit") {
		t.Fatalf("audit 档应放行并在回执里提醒: %v", res)
	}
	audits := f.taskAudits()
	if len(audits) != 1 {
		t.Fatalf("应记 1 条任务级违规审计: %d", len(audits))
	}
	if audits[0].ScopeRef != strconv.Itoa(int(f.project.ID)) || !strings.Contains(audits[0].Target, "task_update#1") ||
		!strings.Contains(audits[0].Detail, `"would_deny":true`) || !strings.Contains(audits[0].Detail, `"manager":false`) {
		t.Fatalf("审计内容不对: %+v", audits[0])
	}
	// 30 分钟内同一条任务同一动作只记一条。
	f.update("wf-b", t1, map[string]any{"progress": "再补一句"})
	if n := len(f.taskAudits()); n != 1 {
		t.Fatalf("同一主体 + 同一任务应去重: %d", n)
	}
	// 普通 Agent 派任务：同样放行并记录。
	res = f.update("wf-c", t2, map[string]any{"assignee": "wf-b"})
	if !strings.Contains(hintOf(res), "管理 Agent") {
		t.Fatalf("普通 Agent 指派应带提醒: %v", res)
	}
	if n := len(f.taskAudits()); n != 2 {
		t.Fatalf("指派应再记 1 条: %d", n)
	}
	f.update("wf-c", t2, map[string]any{"assignee": ""}) // 撤销，恢复原状
	before := len(f.taskAudits())

	// 用户在项目页勾选 A 为管理 Agent（tasks 范围授权）：A 派任务、改别人的任务都不再有违规。
	f.grantTasks("wf-a")
	f.update("wf-a", t2, map[string]any{"assignee": "wf-b"})
	res = f.update("wf-a", t3, map[string]any{"assignee": "wf-c"})
	if hintOf(res) != "" {
		t.Fatalf("管理 Agent 不该有提醒: %v", res)
	}
	bt := f.batchTask("wf-b", "B 自己的事")
	res = f.update("wf-a", bt, map[string]any{"progress": "管理 Agent 改别人的任务"})
	if res["status"] != "updated" || hintOf(res) != "" {
		t.Fatalf("管理 Agent 改所有人的任务不该有提醒: %v", res)
	}
	if n := len(f.taskAudits()); n != before {
		t.Fatalf("管理 Agent 的操作不该产生违规审计: %d → %d", before, n)
	}
	// 不带 project_directory 的调用（身份按工作流推断）同样被认出是管理 Agent。
	res = f.do("workflow_context", map[string]any{"workflow_id": "wf-a", "action": "task_update", "task_id": bt, "progress": "推断身份的管理调用"})
	if hintOf(res) != "" {
		t.Fatalf("按工作流推断出身份的管理 Agent 也不该有提醒: %v", res)
	}

	// enforce 档：真正拒绝，错误是写给智能体看的那句话，审计记 would_deny=false。
	f.mode("enforce")
	msg := f.updateFails("wf-b", t1, map[string]any{"progress": "不该成功"}, "不是你的工作流创建")
	if !strings.Contains(msg, "只能读") && !strings.Contains(msg, "不由你承接") {
		t.Fatalf("拒绝信息应说明只能读: %s", msg)
	}
	f.updateFails("wf-b", t3, map[string]any{"assignee": "wf-b"}, "管理 Agent")
	// B 仍能写自己的任务、答复派给自己的任务；A（管理 Agent）照常派发与改所有任务。
	f.update("wf-b", bt, map[string]any{"progress": "自己的任务照常写"})
	f.update("wf-b", t2, map[string]any{"claim": "accept"})
	f.update("wf-b", t2, map[string]any{"claim": "deliver", "note": "做完了"})
	f.update("wf-a", t2, map[string]any{"claim": "accept_delivery", "conclusion": "验收通过"})
	f.update("wf-a", bt, map[string]any{"title": "B 自己的事（A 改名）"})

	// 撤销授权后，A 立刻不再是管理 Agent：派任务与改别人的任务都被拒绝。
	g := f.grantTasks("wf-a")
	if _, err := f.srv.store.RevokeGrant(context.Background(), f.project.ID, g.ID); err != nil {
		t.Fatal(err)
	}
	f.updateFails("wf-a", bt, map[string]any{"title": "撤销后不该成功"}, "不是你的工作流创建")
	// enforce 档的真实拒绝记 would_deny=false（B 此前在 audit 档已有记录，同一主体窗口内只记一条，所以用 A 来验）。
	last := f.taskAudits()[0] // 新的在前
	if last.ActorRef != "identity:"+strconv.Itoa(int(f.ids["wf-a"])) || !strings.Contains(last.Detail, `"would_deny":false`) || !strings.Contains(last.Detail, `"mode":"enforce"`) {
		t.Fatalf("enforce 的真实拒绝应记 would_deny=false: %+v", last)
	}
	// batch 的拒绝在逐条结果里：整体不报错，那一条记 failed。
	res = f.do("task_board", f.as("wf-a", map[string]any{"action": "batch", "ops": []any{map[string]any{"task_id": t3, "assignee": ""}}}))
	if toInt(asMap(res["summary"])["failed"]) != 1 || res["status"] != "failed" {
		t.Fatalf("撤销授权后 batch 里的指派操作应失败: %v", res)
	}
}

// batch 里的拒绝是逐条结果；enforce 档下被拒的那条记 failed、不影响其余，审计同样会记。
func TestTaskPolicyBatchEnforcePerOp(t *testing.T) {
	f := newPolicyFixture(t)
	f.mode("enforce")
	t1 := f.batchTask("wf-a", "A 的任务")
	own := f.batchTask("wf-b", "B 的任务")
	res := f.do("task_board", f.as("wf-b", map[string]any{"action": "batch", "ops": []any{
		map[string]any{"task_id": t1, "progress": "改别人的"},
		map[string]any{"task_id": own, "progress": "改自己的"},
	}}))
	sum := asMap(res["summary"])
	if toInt(sum["failed"]) != 1 || toInt(sum["updated"]) != 1 || res["status"] != "partial" {
		t.Fatalf("应有 1 条被拒、1 条成功: %v", res)
	}
	var denied string
	for _, e := range asList(res["results"]) {
		if m := asMap(e); m["ok"] == false {
			denied, _ = m["error"].(string)
		}
	}
	if !strings.Contains(denied, "不是你的工作流创建") {
		t.Fatalf("被拒的那条应带拒绝原因: %v", res["results"])
	}
	if n := len(f.taskAudits()); n != 1 {
		t.Fatalf("batch 里的拒绝应记 1 条审计: %d", n)
	}
	if !strings.Contains(f.taskAudits()[0].Target, "task_board:batch#1") {
		t.Fatalf("审计目标应带任务号: %+v", f.taskAudits()[0])
	}
}

// 项目范围的编辑授权同样算管理 Agent；只读不算。off 档、独立板上的工作流完全不受影响。
func TestTaskPolicyProjectGrantOffAndSolo(t *testing.T) {
	f := newPolicyFixture(t)
	st := f.srv.store
	ctx := context.Background()
	f.mode("enforce")
	t1 := f.batchTask("wf-a", "A 的任务")

	proj := store.GrantInput{ProjectID: f.project.ID, PrincipalKind: model.PrincipalIdentity, PrincipalRef: strconv.Itoa(int(f.ids["wf-c"])), ScopeKind: model.ScopeProject, Role: model.RoleRead, GrantedByKind: "human"}
	if _, err := st.CreateGrant(ctx, proj); err != nil {
		t.Fatal(err)
	}
	f.updateFails("wf-c", t1, map[string]any{"progress": "只读授权不够"}, "不是你的工作流创建")
	proj.Role = model.RoleEdit
	if _, err := st.CreateGrant(ctx, proj); err != nil {
		t.Fatal(err)
	}
	f.update("wf-c", t1, map[string]any{"progress": "项目范围 edit 算管理 Agent"})

	// off 档：什么都不校验、不记审计。
	f.mode("off")
	before := len(f.taskAudits())
	f.update("wf-b", t1, map[string]any{"progress": "off 档不校验"})
	if n := len(f.taskAudits()); n != before {
		t.Fatalf("off 档不该产生审计: %d → %d", before, n)
	}

	// 独立板（不在任何项目里）：即使别的项目在 enforce，也不校验。
	f.mode("enforce")
	solo := f.batchTask("wf-solo", "独立板的任务")
	res := f.update("wf-solo", solo, map[string]any{"progress": "独立板照常写"})
	if res["status"] != "updated" || hintOf(res) != "" {
		t.Fatalf("独立板不该受任务级权限影响: %v", res)
	}
}

// 任务管理权只有用户能授予：Agent 经 project_docs grant 申请 tasks / project 范围一律被拒绝，也不能自授。
func TestTaskManagerCannotBeSelfGranted(t *testing.T) {
	f := newPolicyFixture(t)
	f.mode("enforce")
	for _, scope := range []string{"tasks", "project"} {
		f.fail("project_docs", f.as("wf-a", map[string]any{
			"action": "grant", "grantee": strconv.Itoa(int(f.ids["wf-b"])), "grant_scope": scope, "grant_ref": strconv.Itoa(int(f.project.ID)), "role": "edit",
		}), "grant_scope")
	}
	t1 := f.batchTask("wf-b", "B 的任务")
	f.updateFails("wf-a", t1, map[string]any{"assignee": "wf-a"}, "管理 Agent")
}
