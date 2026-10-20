package acl

import (
	"strings"
	"testing"
)

// 调用方：human、项目 1 里的 10 与 20、项目 2 里的 30、认不出身份的 anon。
var (
	human    = Caller{Human: true}
	id10     = Caller{IdentityID: 10, ProjectID: 1}
	id20     = Caller{IdentityID: 20, ProjectID: 1}
	id30     = Caller{IdentityID: 30, ProjectID: 2}
	anon     = Caller{}
	callerOf = map[string]Caller{"human": human, "id10": id10, "id20": id20, "id30": id30, "anon": anon}
)

var needs = []struct {
	name string
	role Role
}{{"read", RoleRead}, {"edit", RoleEdit}, {"manage", RoleManage}}

// 规则矩阵：每个资源 × 每个调用方 × 读 / 写 / 管理，期望值逐条手写（y = 放行，n = 拒绝）。
func TestAuthorizeRuleMatrix(t *testing.T) {
	rows := []struct {
		name string
		res  Resource
		want map[string]string // 调用方 → "读写管" 三位
	}{
		{"自己负责的文档", Resource{Kind: KindDoc, ProjectID: 1, DocPath: "design/a.md", OwnerIdentityID: 10, WorkflowIdentityID: 10},
			map[string]string{"human": "yyy", "id10": "yyy", "id20": "ynn", "id30": "nnn", "anon": "nnn"}},
		{"别人负责的文档", Resource{Kind: KindDoc, ProjectID: 1, DocPath: "design/b.md", OwnerIdentityID: 20, WorkflowIdentityID: 20},
			map[string]string{"human": "yyy", "id10": "ynn", "id20": "yyy", "id30": "nnn", "anon": "nnn"}},
		{"模块负责人名下别人写的文档", Resource{Kind: KindDoc, ProjectID: 1, ModulePath: "chat", DocPath: "modules/chat/x.md", OwnerIdentityID: 20, ModuleOwnerID: 10, WorkflowIdentityID: 20},
			map[string]string{"human": "yyy", "id10": "yyy", "id20": "yyy", "id30": "nnn", "anon": "nnn"}},
		{"没有负责人的文档", Resource{Kind: KindDoc, ProjectID: 1, DocPath: "overview.md"},
			map[string]string{"human": "yyy", "id10": "ynn", "id20": "ynn", "id30": "nnn", "anon": "nnn"}},
		{"10 的会话数据", Resource{Kind: KindSession, ProjectID: 1, WorkflowID: "wf-10", WorkflowIdentityID: 10},
			map[string]string{"human": "yyy", "id10": "yyy", "id20": "ynn", "id30": "ynn", "anon": "ynn"}},
		{"30 的会话数据", Resource{Kind: KindSession, ProjectID: 2, WorkflowID: "wf-30", WorkflowIdentityID: 30},
			map[string]string{"human": "yyy", "id10": "ynn", "id20": "ynn", "id30": "yyy", "anon": "ynn"}},
		{"10 的收件箱", Resource{Kind: KindInbox, ProjectID: 1, WorkflowID: "wf-10", WorkflowIdentityID: 10},
			map[string]string{"human": "yyy", "id10": "yyy", "id20": "nnn", "id30": "nnn", "anon": "nnn"}},
		{"另一个项目的文档", Resource{Kind: KindDoc, ProjectID: 2, DocPath: "design/c.md", OwnerIdentityID: 30, WorkflowIdentityID: 30},
			map[string]string{"human": "yyy", "id10": "nnn", "id20": "nnn", "id30": "yyy", "anon": "nnn"}},
		{"不属于任何项目的文档", Resource{Kind: KindDoc, ProjectID: 0, DocPath: "design/d.md", WorkflowIdentityID: 10},
			map[string]string{"human": "yyy", "id10": "yyy", "id20": "nnn", "id30": "nnn", "anon": "nnn"}},
		{"项目级（模块树）", Resource{Kind: KindProject, ProjectID: 1},
			map[string]string{"human": "yyy", "id10": "ynn", "id20": "ynn", "id30": "nnn", "anon": "nnn"}},
		{"模块本身（负责人 10）", Resource{Kind: KindModule, ProjectID: 1, ModulePath: "chat", ModuleOwnerID: 10},
			map[string]string{"human": "yyy", "id10": "yyy", "id20": "ynn", "id30": "nnn", "anon": "nnn"}},
		{"没有工作流编号的会话数据", Resource{Kind: KindSession, ProjectID: 1},
			map[string]string{"human": "yyy", "id10": "nnn", "id20": "nnn", "id30": "nnn", "anon": "nnn"}},
	}
	for _, row := range rows {
		for who, want := range row.want {
			for i, n := range needs {
				d := Authorize(Request{Mode: ModeEnforce, Caller: callerOf[who], Res: row.res, Need: n.role})
				if wantAllow := want[i] == 'y'; d.Allow != wantAllow {
					t.Errorf("%s / %s / %s：期望 %v，得到 %+v", row.name, who, n.name, wantAllow, d)
				}
			}
		}
	}
}

func TestAuthorizeRuleNames(t *testing.T) {
	doc := Resource{Kind: KindDoc, ProjectID: 1, OwnerIdentityID: 10}
	sess := Resource{Kind: KindSession, ProjectID: 2, WorkflowID: "wf-x", WorkflowIdentityID: 30}
	cases := []struct {
		name string
		req  Request
		rule string
		have Role
	}{
		{"human R1", Request{Mode: ModeEnforce, Caller: human, Res: doc, Need: RoleManage}, RuleHuman, RoleManage},
		{"负责人 R3", Request{Mode: ModeEnforce, Caller: id10, Res: doc, Need: RoleEdit}, RuleOwner, RoleManage},
		{"同项目 R2", Request{Mode: ModeEnforce, Caller: id20, Res: doc, Need: RoleRead}, RuleProject, RoleRead},
		{"知道编号 R4", Request{Mode: ModeEnforce, Caller: id10, Res: sess, Need: RoleRead}, RuleKnownID, RoleRead},
		{"拒绝 R7", Request{Mode: ModeEnforce, Caller: id30, Res: doc, Need: RoleRead}, RuleNoAccess, RoleNone},
		{"收件箱所有者", Request{Mode: ModeAudit, Caller: id10, Res: Resource{Kind: KindInbox, WorkflowIdentityID: 10}, Need: RoleEdit}, RuleInbox, RoleManage},
		{"授权", Request{Mode: ModeEnforce, Caller: id30, Res: doc, Need: RoleRead, Grants: []Grant{{ScopeKind: ScopeProject, ScopeRef: "1", Role: RoleRead}}}, RuleGrant, RoleRead},
	}
	for _, c := range cases {
		d := Authorize(c.req)
		if d.Rule != c.rule || d.Have != c.have || !d.Allow == (c.rule != RuleNoAccess) {
			t.Errorf("%s：%+v", c.name, d)
		}
	}
}

func TestAuthorizeGrants(t *testing.T) {
	mod := Resource{Kind: KindDoc, ProjectID: 1, ModulePath: "chat/groups", DocPath: "modules/chat/groups/design.md", DocID: 7, WorkflowID: "wf-a"}
	cases := []struct {
		name   string
		caller Caller
		res    Resource
		need   Role
		grants []Grant
		want   bool
	}{
		{"项目授权 read 给跨项目身份", id30, mod, RoleRead, []Grant{{ScopeKind: ScopeProject, ScopeRef: "1", Role: RoleRead}}, true},
		{"项目授权 read 不够写", id30, mod, RoleEdit, []Grant{{ScopeKind: ScopeProject, ScopeRef: "1", Role: RoleRead}}, false},
		{"项目授权 comment 不够写", id20, mod, RoleEdit, []Grant{{ScopeKind: ScopeProject, ScopeRef: "1", Role: RoleComment}}, false},
		{"项目授权 edit 够写", id20, mod, RoleEdit, []Grant{{ScopeKind: ScopeProject, ScopeRef: "1", Role: RoleEdit}}, true},
		{"项目授权 edit 不够管理", id20, mod, RoleManage, []Grant{{ScopeKind: ScopeProject, ScopeRef: "1", Role: RoleEdit}}, false},
		{"授权 manage 被压成 edit：不能转授管理", id20, mod, RoleManage, []Grant{{ScopeKind: ScopeProject, ScopeRef: "1", Role: RoleManage}}, false},
		{"授权 manage 被压成 edit：写仍可", id20, mod, RoleEdit, []Grant{{ScopeKind: ScopeProject, ScopeRef: "1", Role: RoleManage}}, true},
		{"别的项目编号的项目授权不生效", id30, mod, RoleRead, []Grant{{ScopeKind: ScopeProject, ScopeRef: "2", Role: RoleRead}}, false},
		{"授权记录的项目与资源项目不符", id30, mod, RoleRead, []Grant{{ProjectID: 2, ScopeKind: ScopeProject, ScopeRef: "1", Role: RoleRead}}, false},
		{"模块授权：同一模块", id20, mod, RoleEdit, []Grant{{ScopeKind: ScopeModule, ScopeRef: "chat/groups", Role: RoleEdit}}, true},
		{"模块授权：父模块覆盖子模块", id20, mod, RoleEdit, []Grant{{ScopeKind: ScopeModule, ScopeRef: "chat", Role: RoleEdit}}, true},
		{"模块授权：子模块不覆盖父模块", id20, Resource{Kind: KindDoc, ProjectID: 1, ModulePath: "chat"}, RoleEdit, []Grant{{ScopeKind: ScopeModule, ScopeRef: "chat/groups", Role: RoleEdit}}, false},
		{"模块授权：前缀相同但不是子模块", id20, Resource{Kind: KindDoc, ProjectID: 1, ModulePath: "chatroom"}, RoleEdit, []Grant{{ScopeKind: ScopeModule, ScopeRef: "chat", Role: RoleEdit}}, false},
		{"模块授权：文档没绑模块", id20, Resource{Kind: KindDoc, ProjectID: 1}, RoleEdit, []Grant{{ScopeKind: ScopeModule, ScopeRef: "chat", Role: RoleEdit}}, false},
		{"文档授权：按路径", id20, mod, RoleEdit, []Grant{{ScopeKind: ScopeDoc, ScopeRef: "modules/chat/groups/design.md", Role: RoleEdit}}, true},
		{"文档授权：按编号", id20, mod, RoleEdit, []Grant{{ScopeKind: ScopeDoc, ScopeRef: "7", Role: RoleEdit}}, true},
		{"文档授权：d 前缀编号", id20, mod, RoleEdit, []Grant{{ScopeKind: ScopeDoc, ScopeRef: "d7", Role: RoleEdit}}, true},
		{"文档授权：别的文档", id20, mod, RoleEdit, []Grant{{ScopeKind: ScopeDoc, ScopeRef: "modules/chat/other.md", Role: RoleEdit}}, false},
		{"文档授权：空引用", id20, mod, RoleEdit, []Grant{{ScopeKind: ScopeDoc, ScopeRef: "", Role: RoleEdit}}, false},
		{"工作流授权：命中", id30, Resource{Kind: KindSession, WorkflowID: "wf-a", ProjectID: 1}, RoleEdit, []Grant{{ScopeKind: ScopeWorkflow, ScopeRef: "wf-a", Role: RoleEdit}}, true},
		{"工作流授权：别的工作流", id30, Resource{Kind: KindSession, WorkflowID: "wf-b", ProjectID: 1}, RoleEdit, []Grant{{ScopeKind: ScopeWorkflow, ScopeRef: "wf-a", Role: RoleEdit}}, false},
		{"授权对收件箱无效", id30, Resource{Kind: KindInbox, WorkflowID: "wf-a", WorkflowIdentityID: 10}, RoleRead, []Grant{{ScopeKind: ScopeWorkflow, ScopeRef: "wf-a", Role: RoleEdit}}, false},
		{"未知范围种类", id20, mod, RoleEdit, []Grant{{ScopeKind: "weird", ScopeRef: "x", Role: RoleEdit}}, false},
		{"角色为空的授权", id20, mod, RoleEdit, []Grant{{ScopeKind: ScopeProject, ScopeRef: "1", Role: RoleNone}}, false},
		{"多条授权取最高", id30, mod, RoleEdit, []Grant{{ScopeKind: ScopeProject, ScopeRef: "1", Role: RoleRead}, {ScopeKind: ScopeModule, ScopeRef: "chat", Role: RoleEdit}}, true},
		{"授权不影响无关调用方的拒绝（没传授权）", id30, mod, RoleRead, nil, false},
	}
	for _, c := range cases {
		d := Authorize(Request{Mode: ModeEnforce, Caller: c.caller, Res: c.res, Need: c.need, Grants: c.grants})
		if d.Allow != c.want {
			t.Errorf("%s：期望 %v，得到 %+v", c.name, c.want, d)
		}
	}
}

func TestAuthorizeModes(t *testing.T) {
	res := Resource{Kind: KindDoc, ProjectID: 1, OwnerIdentityID: 10}
	for _, mode := range []string{"", "off", "bogus"} {
		if d := Authorize(Request{Mode: mode, Caller: id30, Res: res, Need: RoleManage}); !d.Allow || d.Rule != RuleOff {
			t.Errorf("档位 %q 应一律放行: %+v", mode, d)
		}
	}
	for _, mode := range []string{ModeAudit, ModeEnforce} {
		if d := Authorize(Request{Mode: mode, Caller: id30, Res: res, Need: RoleRead}); d.Allow {
			t.Errorf("档位 %s 判定应一致（拒绝）: %+v", mode, d)
		}
	}
	// 没有指定 Need 按读处理。
	if d := Authorize(Request{Mode: ModeEnforce, Caller: id20, Res: res}); !d.Allow {
		t.Errorf("Need 为空应按 read: %+v", d)
	}
}

func TestDenyReasonsGuideTheCaller(t *testing.T) {
	res := Resource{Kind: KindDoc, ProjectID: 1, OwnerIdentityID: 10}
	d := Authorize(Request{Mode: ModeEnforce, Caller: anon, Res: res, Need: RoleEdit})
	if !strings.Contains(d.Reason, "project_directory") {
		t.Errorf("没有身份时应提示补 project_directory: %q", d.Reason)
	}
	d = Authorize(Request{Mode: ModeEnforce, Caller: id20, Res: res, Need: RoleEdit})
	if !strings.Contains(d.Reason, "授权") || d.Have != RoleRead {
		t.Errorf("写被拒应说明授权来源: %+v", d)
	}
	d = Authorize(Request{Mode: ModeEnforce, Caller: id30, Res: res, Need: RoleRead})
	if !strings.Contains(d.Reason, "跨项目") {
		t.Errorf("跨项目读被拒应说明可行路径: %q", d.Reason)
	}
	d = Authorize(Request{Mode: ModeEnforce, Caller: id20, Res: Resource{Kind: KindInbox, WorkflowIdentityID: 10}, Need: RoleRead})
	if !strings.Contains(d.Reason, "收件箱") {
		t.Errorf("收件箱被拒的原因: %q", d.Reason)
	}
}

func TestRoleParsingAndOrder(t *testing.T) {
	for in, want := range map[string]Role{"read": RoleRead, " COMMENT ": RoleComment, "edit": RoleEdit, "manage": RoleManage, "": RoleNone, "x": RoleNone} {
		if got := ParseRole(in); got != want {
			t.Errorf("ParseRole(%q)=%v 期望 %v", in, got, want)
		}
	}
	if !(RoleRead < RoleComment && RoleComment < RoleEdit && RoleEdit < RoleManage) {
		t.Fatal("角色必须逐级包含")
	}
	if RoleEdit.String() != "edit" || RoleNone.String() != "none" {
		t.Fatal("String")
	}
}

// 单调性：能写就一定能读；human 永远放行；任何调用方拿到的权限不会因为多一条授权而变少。
func TestAuthorizeMonotonic(t *testing.T) {
	resources := []Resource{
		{Kind: KindDoc, ProjectID: 1, OwnerIdentityID: 10},
		{Kind: KindSession, ProjectID: 2, WorkflowID: "w", WorkflowIdentityID: 30},
		{Kind: KindInbox, WorkflowID: "w", WorkflowIdentityID: 10},
		{Kind: KindModule, ProjectID: 1, ModulePath: "a/b", ModuleOwnerID: 20},
	}
	extra := Grant{ScopeKind: ScopeProject, ScopeRef: "1", Role: RoleRead}
	for _, res := range resources {
		for name, c := range callerOf {
			for _, mode := range []string{ModeAudit, ModeEnforce} {
				w := Authorize(Request{Mode: mode, Caller: c, Res: res, Need: RoleEdit})
				r := Authorize(Request{Mode: mode, Caller: c, Res: res, Need: RoleRead})
				if w.Allow && !r.Allow {
					t.Errorf("%s/%s：能写却不能读", name, res.Kind)
				}
				if name == "human" && !(w.Allow && r.Allow) {
					t.Errorf("human 必须放行")
				}
				with := Authorize(Request{Mode: mode, Caller: c, Res: res, Need: RoleRead, Grants: []Grant{extra}})
				if r.Allow && !with.Allow {
					t.Errorf("%s/%s：多一条授权反而被拒", name, res.Kind)
				}
			}
		}
	}
}

// 任务管理权（#175 B2c）：只认显式授权——tasks 范围，或项目范围 edit 及以上；其余规则（同项目只读、负责人、知道编号）一概不适用。
func TestCanManageTasks(t *testing.T) {
	tasks1 := Grant{ProjectID: 1, ScopeKind: ScopeTasks, ScopeRef: "1", Role: RoleEdit}
	project1Edit := Grant{ProjectID: 1, ScopeKind: ScopeProject, ScopeRef: "1", Role: RoleEdit}
	project1Read := Grant{ProjectID: 1, ScopeKind: ScopeProject, ScopeRef: "1", Role: RoleRead}
	project1Comment := Grant{ProjectID: 1, ScopeKind: ScopeProject, ScopeRef: "1", Role: RoleComment}
	tasks2 := Grant{ProjectID: 2, ScopeKind: ScopeTasks, ScopeRef: "2", Role: RoleEdit}
	moduleEdit := Grant{ProjectID: 1, ScopeKind: ScopeModule, ScopeRef: "chat", Role: RoleEdit}
	docEdit := Grant{ProjectID: 1, ScopeKind: ScopeDoc, ScopeRef: "design/a.md", Role: RoleEdit}
	wfEdit := Grant{ProjectID: 1, ScopeKind: ScopeWorkflow, ScopeRef: "wf-10", Role: RoleEdit}
	// 一条被写成 manage 的授权（库里不该有，判定也不能因此放大）：照样按 edit 封顶，管理权判定只要 edit 就够。
	tasksManage := Grant{ProjectID: 1, ScopeKind: ScopeTasks, ScopeRef: "1", Role: RoleManage}

	rows := []struct {
		name      string
		id        uint
		project   uint
		grants    []Grant
		wantAllow bool
		wantRule  string
	}{
		{"没有任何授权", 10, 1, nil, false, RuleNoAccess},
		{"tasks 范围授权", 10, 1, []Grant{tasks1}, true, RuleGrant},
		{"项目范围 edit 也算（项目级编辑本来就包含改所有任务）", 10, 1, []Grant{project1Edit}, true, RuleGrant},
		{"项目范围只读不算", 10, 1, []Grant{project1Read}, false, RuleNoAccess},
		{"项目范围评论不算", 10, 1, []Grant{project1Comment}, false, RuleNoAccess},
		{"别的项目的 tasks 授权不算", 10, 1, []Grant{tasks2}, false, RuleNoAccess},
		{"模块范围的编辑权不算", 10, 1, []Grant{moduleEdit}, false, RuleNoAccess},
		{"文档范围的编辑权不算", 10, 1, []Grant{docEdit}, false, RuleNoAccess},
		{"工作流范围的编辑权不算", 10, 1, []Grant{wfEdit}, false, RuleNoAccess},
		{"多条授权里有一条够就够", 10, 1, []Grant{moduleEdit, project1Read, tasks1}, true, RuleGrant},
		{"授权被封顶在 edit，仍然够", 10, 1, []Grant{tasksManage}, true, RuleGrant},
		{"认不出身份但有（群）授权也算", 0, 1, []Grant{tasks1}, true, RuleGrant},
		{"项目号为 0（不在任何项目的板上）永远不是管理 Agent", 10, 0, []Grant{tasks1, project1Edit}, false, RuleNoAccess},
	}
	for _, row := range rows {
		d := CanManageTasks(row.id, row.project, row.grants)
		if d.Allow != row.wantAllow || d.Rule != row.wantRule {
			t.Errorf("%s：期望 allow=%v rule=%s，得到 %+v", row.name, row.wantAllow, row.wantRule, d)
		}
	}
}

// tasks 范围的授权只覆盖任务管理权：给了它不等于能编辑文档、模块或别人的会话数据；
// 反过来，同项目身份的「只读」与「负责人」规则也不会让谁变成管理 Agent。
func TestTasksScopeDoesNotLeak(t *testing.T) {
	tasks1 := Grant{ProjectID: 1, ScopeKind: ScopeTasks, ScopeRef: "1", Role: RoleEdit}
	for _, res := range []Resource{
		{Kind: KindDoc, ProjectID: 1, DocPath: "design/a.md", OwnerIdentityID: 20},
		{Kind: KindModule, ProjectID: 1, ModulePath: "chat", ModuleOwnerID: 20},
		{Kind: KindProject, ProjectID: 1},
		{Kind: KindSession, ProjectID: 1, WorkflowID: "wf-20", WorkflowIdentityID: 20},
		{Kind: KindInbox, ProjectID: 1, WorkflowID: "wf-20", WorkflowIdentityID: 20},
	} {
		d := Authorize(Request{Mode: ModeEnforce, Caller: id10, Res: res, Need: RoleEdit, Grants: []Grant{tasks1}})
		if d.Allow {
			t.Errorf("tasks 授权不该放行对 %s 的写入: %+v", res.Kind, d)
		}
	}
	// 同项目、同为负责人 / 工作流所有者，没有显式授权：任务管理资源一律拒绝。
	own := Resource{Kind: KindTasks, ProjectID: 1, OwnerIdentityID: 10, ModuleOwnerID: 10, WorkflowIdentityID: 10, WorkflowID: "wf-10"}
	if d := Authorize(Request{Mode: ModeEnforce, Caller: id10, Res: own, Need: RoleRead}); d.Allow {
		t.Errorf("同项目、负责人、知道编号都不该让人读到任务管理权: %+v", d)
	}
	// 人类永远可以；档位 off 不判定。
	if d := Authorize(Request{Mode: ModeEnforce, Caller: human, Res: own, Need: RoleEdit}); !d.Allow || d.Rule != RuleHuman {
		t.Errorf("人类应放行: %+v", d)
	}
	if d := Authorize(Request{Mode: ModeOff, Caller: id10, Res: own, Need: RoleEdit}); !d.Allow || d.Rule != RuleOff {
		t.Errorf("off 档不判定: %+v", d)
	}
}
