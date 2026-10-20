package store

import (
	"strconv"
	"testing"
	"time"

	"github.com/teacat99/RelayMesh/v2/internal/acl"
	"github.com/teacat99/RelayMesh/v2/internal/model"
)

func TestGrantsLifecycleAndValidation(t *testing.T) {
	st, ctx := projTestStore(t)
	p, _ := st.CreateProject(ctx, ProjectInput{Name: "P", Slug: "p"})
	_, _ = st.UpsertProjectModule(ctx, p.ID, ModuleInput{Path: "chat", Title: "聊天"})
	seedWorkflowDir(t, st, "wf-a", "h", "/p", time.Now())
	_, _ = st.AddProjectDir(ctx, p.ID, "h", "/p", "")
	doc, err := st.CreateProjectDoc(ctx, ProjectDocInput{WorkflowID: "wf-a", Path: "modules/chat/i.md", Body: "x"}, DocActor{Kind: model.DocActorAgent})
	if err != nil {
		t.Fatal(err)
	}
	idA, asserted := st.ResolveCallerIdentity(ctx, "wf-a", 1, "h", "/p", time.Now())
	if idA == 0 || !asserted {
		t.Fatalf("断言了目录应得到身份: %d %v", idA, asserted)
	}
	idB, _ := st.ResolveCallerIdentity(ctx, "wf-b", 1, "h", "/other", time.Now())
	if idB == 0 || idB == idA {
		t.Fatalf("不同目录是不同身份: %d %d", idA, idB)
	}
	in := GrantInput{ProjectID: p.ID, PrincipalKind: model.PrincipalIdentity, PrincipalRef: strconv.Itoa(int(idB)), ScopeKind: model.ScopeModule, ScopeRef: "chat/", Role: model.RoleEdit, GrantedByKind: "human", GrantedByRef: "human", GrantedByLabel: "你"}

	// 校验：manage 不能转授、角色/范围/主体非法、模块/文档/身份不存在。
	bad := func(mut func(*GrantInput), sub string) {
		t.Helper()
		g := in
		mut(&g)
		_, err := st.CreateGrant(ctx, g)
		wantAppErr(t, err, sub)
	}
	bad(func(g *GrantInput) { g.Role = model.RoleManage }, "不能转授")
	bad(func(g *GrantInput) { g.Role = "boss" }, "角色")
	bad(func(g *GrantInput) { g.PrincipalKind = "robot" }, "主体")
	bad(func(g *GrantInput) { g.PrincipalRef = "x" }, "身份编号")
	bad(func(g *GrantInput) { g.PrincipalRef = "9999" }, "没有这个身份")
	bad(func(g *GrantInput) { g.ScopeRef = "nope" }, "没有这个模块")
	bad(func(g *GrantInput) { g.ScopeKind, g.ScopeRef = model.ScopeDoc, "nope.md" }, "没有这篇文档")
	bad(func(g *GrantInput) { g.ScopeKind = "planet" }, "授权范围")
	bad(func(g *GrantInput) { g.ScopeKind, g.ScopeRef = model.ScopeWorkflow, " " }, "工作流编号")
	bad(func(g *GrantInput) { g.ProjectID = 999 }, "")
	past := time.Now().Add(-time.Minute)
	bad(func(g *GrantInput) { g.ExpiresAt = &past }, "有效期")

	g1, err := st.CreateGrant(ctx, in)
	if err != nil || g1.ScopeRef != "chat" {
		t.Fatalf("创建: %+v %v", g1, err)
	}
	// 同主体同范围再授权 = 更新，不新建。
	in2 := in
	in2.Role = model.RoleRead
	g2, err := st.CreateGrant(ctx, in2)
	if err != nil || g2.ID != g1.ID || g2.Role != model.RoleRead {
		t.Fatalf("应更新同一条: %+v %v", g2, err)
	}
	// 文档范围按路径或编号都规整成路径；项目范围规整成项目编号。
	gd, err := st.CreateGrant(ctx, GrantInput{ProjectID: p.ID, PrincipalKind: model.PrincipalIdentity, PrincipalRef: strconv.Itoa(int(idB)), ScopeKind: model.ScopeDoc, ScopeRef: "d" + strconv.Itoa(int(doc.Doc.ID)), Role: model.RoleComment})
	if err != nil || gd.ScopeRef != "modules/chat/i.md" {
		t.Fatalf("文档范围: %+v %v", gd, err)
	}
	grpX, err := st.CreateGroup(ctx, CreateGroupInput{Title: "群X", Members: []string{"wf-x"}})
	if err != nil {
		t.Fatal(err)
	}
	gp, err := st.CreateGrant(ctx, GrantInput{ProjectID: p.ID, PrincipalKind: model.PrincipalGroup, PrincipalRef: grpX.ID, ScopeKind: model.ScopeProject, ScopeRef: "whatever", Role: model.RoleRead})
	if err != nil || gp.ScopeRef != strconv.Itoa(int(p.ID)) {
		t.Fatalf("项目范围: %+v %v", gp, err)
	}

	// 判定用的有效授权：身份 + 所在群；撤销后不再出现；过期的不出现。
	gs, err := st.ActiveGrantsFor(ctx, p.ID, idB, []string{grpX.ID})
	if err != nil || len(gs) != 3 {
		t.Fatalf("身份 + 群共 3 条: %d %v", len(gs), err)
	}
	if gs, _ := st.ActiveGrantsFor(ctx, p.ID, idB, nil); len(gs) != 2 {
		t.Fatalf("不在群里只有 2 条: %d", len(gs))
	}
	if gs, _ := st.ActiveGrantsFor(ctx, p.ID, idA, nil); len(gs) != 0 {
		t.Fatalf("别的身份没有授权: %d", len(gs))
	}
	if gs, _ := st.ActiveGrantsFor(ctx, p.ID, 0, nil); len(gs) != 0 {
		t.Fatal("没有身份没有授权")
	}
	soon := time.Now().Add(30 * time.Millisecond)
	if _, err := st.CreateGrant(ctx, GrantInput{ProjectID: p.ID, PrincipalKind: model.PrincipalIdentity, PrincipalRef: strconv.Itoa(int(idA)), ScopeKind: model.ScopeWorkflow, ScopeRef: "wf-x", Role: model.RoleRead, ExpiresAt: &soon}); err != nil {
		t.Fatal(err)
	}
	if gs, _ := st.ActiveGrantsFor(ctx, p.ID, idA, nil); len(gs) != 1 {
		t.Fatalf("未过期应可见: %d", len(gs))
	}
	time.Sleep(60 * time.Millisecond)
	if gs, _ := st.ActiveGrantsFor(ctx, p.ID, idA, nil); len(gs) != 0 {
		t.Fatalf("过期后不应可见: %d", len(gs))
	}
	if _, err := st.RevokeGrant(ctx, p.ID, g1.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.RevokeGrant(ctx, p.ID, g1.ID); err != nil {
		t.Fatalf("撤销应幂等: %v", err)
	}
	_, err = st.RevokeGrant(ctx, p.ID, 9999)
	wantAppErr(t, err, "没有这条授权")
	if gs, _ := st.ActiveGrantsFor(ctx, p.ID, idB, []string{grpX.ID}); len(gs) != 2 {
		t.Fatalf("撤销后剩 2 条: %d", len(gs))
	}
	active, _ := st.ListGrants(ctx, p.ID, false)
	all, _ := st.ListGrants(ctx, p.ID, true)
	if len(active) != 2 || len(all) != 4 {
		t.Fatalf("有效 %d / 全部 %d", len(active), len(all))
	}
	// 撤销后同范围可以重新授权（新建一条）。
	if g3, err := st.CreateGrant(ctx, in); err != nil || g3.ID == g1.ID {
		t.Fatalf("撤销后应新建: %+v %v", g3, err)
	}
}

func TestResolveIdentityAndWorkflowBinding(t *testing.T) {
	st, ctx := projTestStore(t)
	p, _ := st.CreateProject(ctx, ProjectInput{Name: "P", Slug: "p"})
	_, _ = st.AddProjectDir(ctx, p.ID, "h", "/p", "")
	now := time.Now()
	seedWorkflowDir(t, st, "wf-1", "h", "/p", now)

	// 断言了目录：身份明确；同一目录再开新工作流还是同一身份。
	id1, asserted := st.ResolveCallerIdentity(ctx, "wf-1", 5, "h", "/p", now)
	if id1 == 0 || !asserted {
		t.Fatal("断言身份")
	}
	if id2, _ := st.ResolveCallerIdentity(ctx, "wf-2", 5, "h", "/p/", now); id2 != id1 {
		t.Fatalf("同目录同凭据同身份: %d vs %d", id2, id1)
	}
	// 另一个凭据在同一目录是另一个身份（身份 = 凭据 + 主机 + 目录）。
	if id3, _ := st.ResolveCallerIdentity(ctx, "wf-1", 6, "h", "/p", now); id3 == id1 {
		t.Fatal("不同凭据应是不同身份")
	}
	// 没断言：按工作流最近断言过的目录推断，asserted=false。
	id4, asserted4 := st.ResolveCallerIdentity(ctx, "wf-1", 5, "", "", now)
	if id4 != id1 || asserted4 {
		t.Fatalf("推断身份: %d %v", id4, asserted4)
	}
	// 完全认不出：0。
	if id5, _ := st.ResolveCallerIdentity(ctx, "wf-unknown", 5, "", "", now); id5 != 0 {
		t.Fatalf("认不出应为 0: %d", id5)
	}
	if id6, _ := st.ResolveCallerIdentity(ctx, "", 5, "h", "/p", now); id6 != 0 {
		t.Fatal("没有工作流编号不判定")
	}

	// 工作流 → 身份：按汇报会话的目录找到（同目录最近活跃的身份）；身份 → 项目、标签。
	if got := st.WorkflowIdentityID(ctx, "wf-1"); got == 0 {
		t.Fatal("wf-1 应能找到身份")
	}
	if got := st.WorkflowIdentityID(ctx, "nope"); got != 0 {
		t.Fatalf("未知工作流: %d", got)
	}
	if pid := st.IdentityProjectID(ctx, id1); pid != p.ID {
		t.Fatalf("身份所在项目: %d", pid)
	}
	other, _ := st.ResolveCallerIdentity(ctx, "wf-9", 5, "h", "/elsewhere", now)
	if pid := st.IdentityProjectID(ctx, other); pid != 0 {
		t.Fatalf("不在项目目录里的身份: %d", pid)
	}
	if st.IdentityProjectID(ctx, 0) != 0 {
		t.Fatal("0 身份没有项目")
	}
	if l := st.IdentityLabelOf(ctx, id1); l != "p" {
		t.Fatalf("标签默认用目录名: %q", l)
	}
	if l := st.IdentityLabelOf(ctx, 0); l != "" {
		t.Fatalf("0 身份无标签: %q", l)
	}
	if l := st.IdentityLabelOf(ctx, 99999); l != "身份#99999" {
		t.Fatalf("不存在的身份标签: %q", l)
	}
}

// 群共享：把模块授权给一个群，群里任一工作流（以及这些工作流所属身份名下的其他工作流）都得到该授权；退群后失效。
func TestGroupGrantsReachMembers(t *testing.T) {
	st, ctx := projTestStore(t)
	p, _ := st.CreateProject(ctx, ProjectInput{Name: "P", Slug: "p"})
	_, _ = st.UpsertProjectModule(ctx, p.ID, ModuleInput{Path: "chat", Title: "聊天"})
	seedWorkflowDir(t, st, "wf-a", "h", "/p", time.Now())
	idA, _ := st.ResolveCallerIdentity(ctx, "wf-a", 1, "h", "/p", time.Now())

	if err := st.db.Create(&model.AgentSession{IdentityID: idA, ConversationID: "cv-a", WorkflowID: "wf-a"}).Error; err != nil {
		t.Fatal(err)
	}
	grp, err := st.CreateGroup(ctx, CreateGroupInput{Title: "共享群", Members: []string{"wf-a"}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = st.CreateGrant(ctx, GrantInput{ProjectID: p.ID, PrincipalKind: model.PrincipalGroup, PrincipalRef: "grp:nope", ScopeKind: model.ScopeModule, ScopeRef: "chat", Role: model.RoleRead, GrantedByKind: "human", GrantedByRef: "human", GrantedByLabel: "你"})
	wantAppErr(t, err, "群不存在")
	if _, err := st.CreateGrant(ctx, GrantInput{ProjectID: p.ID, PrincipalKind: model.PrincipalGroup, PrincipalRef: grp.ID, ScopeKind: model.ScopeModule, ScopeRef: "chat", Role: model.RoleEdit, GrantedByKind: "human", GrantedByRef: "human", GrantedByLabel: "你"}); err != nil {
		t.Fatal(err)
	}

	// 群成员：按调用的工作流命中。
	if g, _ := st.ActiveGrantsForCaller(ctx, p.ID, 0, "wf-a"); len(g) != 1 || g[0].Role.String() != "edit" {
		t.Fatalf("群成员应得到群授权: %+v", g)
	}
	// 同一身份在别的工作流里调用，也属于这个群（成员单位是工作流，身份是它们的共同主人）。
	if g, _ := st.ActiveGrantsForCaller(ctx, p.ID, idA, "wf-other"); len(g) != 1 {
		t.Fatalf("身份名下的工作流在群里，应命中: %+v", g)
	}
	// 不在群里的身份没有。
	if g, _ := st.ActiveGrantsForCaller(ctx, p.ID, 0, "wf-x"); len(g) != 0 {
		t.Fatalf("不在群里不应有授权: %+v", g)
	}
	// 退群后失效。
	if err := st.RemoveGroupMember(ctx, grp.ID, "wf-a"); err != nil {
		t.Fatal(err)
	}
	if g, _ := st.ActiveGrantsForCaller(ctx, p.ID, 0, "wf-a"); len(g) != 0 {
		t.Fatalf("退群后授权应失效: %+v", g)
	}
}

// 任务管理权（#175 B2c）：tasks 范围的授权只能由用户给单个 Agent 身份、角色只能是 edit；
// 授出之后经 ActiveGrantsForCaller 能被 acl.CanManageTasks 认出，撤销后立刻失效；项目范围的编辑权同样算。
func TestTasksGrantRulesAndManagerLookup(t *testing.T) {
	st, ctx := projTestStore(t)
	p, _ := st.CreateProject(ctx, ProjectInput{Name: "P", Slug: "p"})
	q, _ := st.CreateProject(ctx, ProjectInput{Name: "Q", Slug: "q"})
	seedWorkflowDir(t, st, "wf-a", "h", "/p", time.Now())
	_, _ = st.AddProjectDir(ctx, p.ID, "h", "/p", "")
	idA, _ := st.ResolveCallerIdentity(ctx, "wf-a", 1, "h", "/p", time.Now())
	idB, _ := st.ResolveCallerIdentity(ctx, "wf-b", 1, "h", "/other", time.Now())
	if idA == 0 || idB == 0 || idA == idB {
		t.Fatalf("身份: %d %d", idA, idB)
	}
	in := GrantInput{ProjectID: p.ID, PrincipalKind: model.PrincipalIdentity, PrincipalRef: strconv.Itoa(int(idA)), ScopeKind: model.ScopeTasks, ScopeRef: "ignored", Role: model.RoleEdit, GrantedByKind: "human", GrantedByRef: "human", GrantedByLabel: "你"}
	isManager := func(id uint, project uint) bool {
		t.Helper()
		grants, err := st.ActiveGrantsForCaller(ctx, project, id, "wf-a")
		if err != nil {
			t.Fatal(err)
		}
		return acl.CanManageTasks(id, project, grants).Allow
	}

	if isManager(idA, p.ID) {
		t.Fatal("没有授权时不是管理 Agent（默认没有人被指定）")
	}

	// 校验：角色只能是 edit、主体只能是单个身份、只有用户能授予。
	bad := func(mut func(*GrantInput), sub string) {
		t.Helper()
		g := in
		mut(&g)
		_, err := st.CreateGrant(ctx, g)
		wantAppErr(t, err, sub)
	}
	bad(func(g *GrantInput) { g.Role = model.RoleRead }, "只能是 edit")
	bad(func(g *GrantInput) { g.Role = model.RoleComment }, "只能是 edit")
	bad(func(g *GrantInput) { g.Role = model.RoleManage }, "不能转授")
	bad(func(g *GrantInput) { g.PrincipalKind, g.PrincipalRef = model.PrincipalGroup, "grp-1" }, "不能授予群")
	bad(func(g *GrantInput) { g.GrantedByKind = "identity" }, "只能由用户授予")
	if isManager(idA, p.ID) {
		t.Fatal("校验失败的授权不该生效")
	}

	// 授予：范围引用固定成项目编号（不管调用方写了什么）。
	g, err := st.CreateGrant(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	if g.ScopeKind != model.ScopeTasks || g.ScopeRef != strconv.Itoa(int(p.ID)) || g.Role != model.RoleEdit {
		t.Fatalf("授权内容不对: %+v", g)
	}
	if !isManager(idA, p.ID) {
		t.Fatal("授权后应是管理 Agent")
	}
	if isManager(idB, p.ID) {
		t.Fatal("别的身份不受影响")
	}
	if isManager(idA, q.ID) {
		t.Fatal("tasks 授权只对授权的那个项目有效")
	}
	// 重复授予只是续期，不重复建。
	g2, err := st.CreateGrant(ctx, in)
	if err != nil || g2.ID != g.ID {
		t.Fatalf("重复授予应更新同一条: %+v %v", g2, err)
	}

	// 撤销后立刻失效。
	if _, err := st.RevokeGrant(ctx, p.ID, g.ID); err != nil {
		t.Fatal(err)
	}
	if isManager(idA, p.ID) {
		t.Fatal("撤销后不再是管理 Agent")
	}

	// 项目范围的编辑权同样算；只读不算。
	proj := GrantInput{ProjectID: p.ID, PrincipalKind: model.PrincipalIdentity, PrincipalRef: strconv.Itoa(int(idB)), ScopeKind: model.ScopeProject, Role: model.RoleRead, GrantedByKind: "human"}
	if _, err := st.CreateGrant(ctx, proj); err != nil {
		t.Fatal(err)
	}
	if isManager(idB, p.ID) {
		t.Fatal("项目范围只读不算管理 Agent")
	}
	proj.Role = model.RoleEdit
	if _, err := st.CreateGrant(ctx, proj); err != nil {
		t.Fatal(err)
	}
	if !isManager(idB, p.ID) {
		t.Fatal("项目范围 edit 应算管理 Agent")
	}
}
