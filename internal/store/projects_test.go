package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/teacat99/RelayMesh/v2/internal/model"
)

// 项目知识库存储的测试（#114 M4a.1）。

func projTestStore(t *testing.T) (*Store, context.Context) {
	t.Helper()
	st, err := New(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	return st, context.Background()
}

// seedSessionSeq 给 seedWorkflowDir 的会话编号加一个递增后缀：同一个工作流在同一毫秒里被造两次会话
// （boardFixture 已经造过 wf-solo，用例里再造一次）时，只靠毫秒时间戳会撞主键，整个包的回归就间歇性红一次。
var seedSessionSeq atomic.Uint64

// seedWorkflowDir 造一条汇报会话：让工作流「落在」某个（主机，目录）。
func seedWorkflowDir(t *testing.T, st *Store, wf, host, dir string, at time.Time) {
	t.Helper()
	row := model.FeedbackSession{
		ID: fmt.Sprintf("sess-%s-%s-%d", wf, at.Format("150405.000"), seedSessionSeq.Add(1)), WorkflowID: wf, HostName: host, ProjectDirectory: dir,
		Title: "t", Summary: "s", Status: "completed", CreatedAt: at, UpdatedAt: at,
	}
	if err := st.db.Create(&row).Error; err != nil {
		t.Fatal(err)
	}
}

func wantAppErr(t *testing.T, err error, sub string) {
	t.Helper()
	if err == nil {
		t.Fatalf("应该报错（含 %q）", sub)
	}
	var ae *AppError
	if !errors.As(err, &ae) {
		t.Fatalf("不是 AppError: %v", err)
	}
	if !strings.Contains(err.Error(), sub) {
		t.Fatalf("错误里应含 %q: %v", sub, err)
	}
}

func TestProjectCRUDAndValidation(t *testing.T) {
	st, ctx := projTestStore(t)
	p, err := st.CreateProject(ctx, ProjectInput{Name: "RelayMesh 主项目"})
	if err != nil {
		t.Fatal(err)
	}
	if p.ACLMode != model.ACLAudit || p.CreatedBy != "human" || p.Slug != "relaymesh" {
		t.Fatalf("默认值不对: %+v", p)
	}
	// 全是汉字转不出 slug：要求显式给
	_, err = st.CreateProject(ctx, ProjectInput{Name: "中文项目"})
	wantAppErr(t, err, "项目标识")
	q, err := st.CreateProject(ctx, ProjectInput{Name: "中文项目", Slug: "zh-demo", ACLMode: "off"})
	if err != nil || q.ACLMode != model.ACLOff {
		t.Fatalf("显式 slug + off: %+v %v", q, err)
	}
	_, err = st.CreateProject(ctx, ProjectInput{Name: "x", Slug: "zh-demo"})
	wantAppErr(t, err, "已被占用")
	_, err = st.CreateProject(ctx, ProjectInput{Name: "x", Slug: "Bad Slug"})
	wantAppErr(t, err, "项目标识")
	_, err = st.CreateProject(ctx, ProjectInput{Name: "x", Slug: "okslug", ACLMode: "strict"})
	wantAppErr(t, err, "ACL 档位")
	_, err = st.CreateProject(ctx, ProjectInput{Name: "  "})
	wantAppErr(t, err, "项目名称")

	nm, mode := "新名字", "enforce"
	u, err := st.UpdateProject(ctx, p.ID, ProjectPatch{Name: &nm, ACLMode: &mode})
	if err != nil || u.Name != nm || u.ACLMode != model.ACLEnforce {
		t.Fatalf("更新: %+v %v", u, err)
	}
	bad := "nope"
	if _, err := st.UpdateProject(ctx, p.ID, ProjectPatch{ACLMode: &bad}); err == nil {
		t.Fatal("非法档位应被拒绝")
	}
	if err := st.ArchiveProject(ctx, q.ID); err != nil {
		t.Fatal(err)
	}
	if err := st.ArchiveProject(ctx, q.ID); err == nil {
		t.Fatal("重复归档应报错")
	}
	list, _ := st.ListProjects(ctx, false)
	if len(list) != 1 || list[0].ID != p.ID {
		t.Fatalf("默认列表不含已归档: %+v", list)
	}
	all, _ := st.ListProjects(ctx, true)
	if len(all) != 2 {
		t.Fatalf("含已归档应有 2 个: %d", len(all))
	}
}

func TestProjectDirsAndWorkflowResolution(t *testing.T) {
	st, ctx := projTestStore(t)
	a, _ := st.CreateProject(ctx, ProjectInput{Name: "A", Slug: "a"})
	b, _ := st.CreateProject(ctx, ProjectInput{Name: "B", Slug: "b"})
	if _, err := st.AddProjectDir(ctx, a.ID, "devhost", "/home/user/a/", "main"); err != nil {
		t.Fatal(err)
	}
	// 目录规整（去末尾斜杠），重复添加同一项目是幂等的
	d, err := st.AddProjectDir(ctx, a.ID, "DEVHOST", "/home/user/a", "")
	if err != nil || d.ProjectDirectory != "/home/user/a" {
		t.Fatalf("幂等添加: %+v %v", d, err)
	}
	// 同一个目录不能属于两个项目
	_, err = st.AddProjectDir(ctx, b.ID, "devhost", "/home/user/a", "")
	wantAppErr(t, err, "已经属于项目")
	if _, err := st.AddProjectDir(ctx, b.ID, "devhost", "", ""); err == nil {
		t.Fatal("空目录应被拒绝")
	}
	// 同路径不同主机是两个目录
	if _, err := st.AddProjectDir(ctx, b.ID, "other-host", "/home/user/a", ""); err != nil {
		t.Fatal(err)
	}

	now := time.Now()
	seedWorkflowDir(t, st, "wf-1", "devhost", "/home/user/a", now.Add(-time.Hour))
	seedWorkflowDir(t, st, "wf-2", "other-host", "/home/user/a", now)
	seedWorkflowDir(t, st, "wf-3", "devhost", "/elsewhere", now)
	// wf-1 之后换到别处：以最近一次为准
	seedWorkflowDir(t, st, "wf-4", "devhost", "/elsewhere", now.Add(-2*time.Hour))
	seedWorkflowDir(t, st, "wf-4", "devhost", "/home/user/a", now.Add(-time.Minute))

	for wf, want := range map[string]uint{"wf-1": a.ID, "wf-2": b.ID, "wf-4": a.ID} {
		p, err := st.ProjectOfWorkflow(ctx, wf)
		if err != nil || p == nil || p.ID != want {
			t.Fatalf("%s 应属于 %d: %+v %v", wf, want, p, err)
		}
	}
	if p, _ := st.ProjectOfWorkflow(ctx, "wf-3"); p != nil {
		t.Fatalf("没归属目录的工作流不该有项目: %+v", p)
	}
	if p, _ := st.ProjectOfWorkflow(ctx, "wf-unknown"); p != nil {
		t.Fatal("没有会话的工作流不该有项目")
	}
	ids, _ := st.ProjectWorkflowIDs(ctx, a.ID)
	if strings.Join(ids, ",") != "wf-1,wf-4" {
		t.Fatalf("项目 A 的工作流: %v", ids)
	}

	// 归档项目后，它的目录不再让工作流归属于它
	_ = st.ArchiveProject(ctx, a.ID)
	if p, _ := st.ProjectOfWorkflow(ctx, "wf-1"); p != nil {
		t.Fatal("已归档项目不该再认领工作流")
	}
	dirs, _ := st.ListProjectDirs(ctx, a.ID)
	if len(dirs) != 1 {
		t.Fatalf("目录归属保留: %d", len(dirs))
	}
	if err := st.RemoveProjectDir(ctx, a.ID, dirs[0].ID); err != nil {
		t.Fatal(err)
	}
	if err := st.RemoveProjectDir(ctx, a.ID, dirs[0].ID); err == nil {
		t.Fatal("重复移除应报错")
	}
}

func TestProjectSuggestions(t *testing.T) {
	st, ctx := projTestStore(t)
	p, _ := st.CreateProject(ctx, ProjectInput{Name: "已归属", Slug: "owned"})
	_, _ = st.AddProjectDir(ctx, p.ID, "h1", "/w/owned", "")
	now := time.Now()
	seedWorkflowDir(t, st, "wf-a", "h1", "/w/owned", now)
	seedWorkflowDir(t, st, "wf-b", "h1", "/w/core-app", now)
	seedWorkflowDir(t, st, "wf-c", "h1", "/w/core-app", now)
	seedWorkflowDir(t, st, "wf-d", "h2", "/w/solo/", now)
	seedWorkflowDir(t, st, "wf-nohost", "", "/w/nohost", now)
	for i := 0; i < 3; i++ {
		_, err := st.CreateProjectDoc(ctx, ProjectDocInput{WorkflowID: "wf-b", Path: "n" + string(rune('a'+i)) + ".md", Body: "x"}, DocActor{Kind: model.DocActorAgent})
		if err != nil {
			t.Fatal(err)
		}
	}
	got, err := st.ProjectSuggestions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("应有 2 条建议（已归属的与没有主机名的不算）: %+v", got)
	}
	if got[0].ProjectDirectory != "/w/core-app" || got[0].SuggestedName != "core-app" || got[0].Docs != 3 || strings.Join(got[0].Workflows, ",") != "wf-b,wf-c" {
		t.Fatalf("第一条（文档多的在前）: %+v", got[0])
	}
	if got[1].ProjectDirectory != "/w/solo" || got[1].SuggestedSlug != "solo" {
		t.Fatalf("第二条: %+v", got[1])
	}
}

func TestProjectModules(t *testing.T) {
	st, ctx := projTestStore(t)
	p, _ := st.CreateProject(ctx, ProjectInput{Name: "M", Slug: "m"})
	m, err := st.UpsertProjectModule(ctx, p.ID, ModuleInput{Path: "chat/groups", Title: "群聊"})
	if err != nil || m.Title != "群聊" {
		t.Fatalf("%+v %v", m, err)
	}
	mods, _ := st.ListProjectModules(ctx, p.ID)
	if len(mods) != 2 || mods[0].Path != "chat" || mods[1].Path != "chat/groups" {
		t.Fatalf("应自动补建父级: %+v", mods)
	}
	// 改标题 / 指派负责人
	m2, err := st.UpsertProjectModule(ctx, p.ID, ModuleInput{Path: "chat/groups", Title: "群聊（改）", OwnerIdentityID: 7})
	if err != nil || m2.ID != m.ID || m2.OwnerIdentityID != 7 {
		t.Fatalf("按路径更新: %+v %v", m2, err)
	}
	for _, bad := range []string{"", "../x", "a//b", "a/./b", "a/b:c", strings.Repeat("a/", 9) + "z"} {
		if _, err := st.UpsertProjectModule(ctx, p.ID, ModuleInput{Path: bad}); err == nil {
			t.Fatalf("路径 %q 应被拒绝", bad)
		}
	}
	if _, err := st.UpsertProjectModule(ctx, 9999, ModuleInput{Path: "x"}); err == nil {
		t.Fatal("项目不存在应报错")
	}
	// 删除模块：子模块一起删，文档只解除绑定
	seedWorkflowDir(t, st, "wf-m", "h", "/m", time.Now())
	_, _ = st.AddProjectDir(ctx, p.ID, "h", "/m", "")
	d, err := st.CreateProjectDoc(ctx, ProjectDocInput{WorkflowID: "wf-m", Path: "x.md", Body: "b", ModulePath: "chat/groups"}, DocActor{})
	if err != nil || d.Doc.ModulePath != "chat/groups" {
		t.Fatalf("绑定模块: %+v %v", d, err)
	}
	if err := st.DeleteProjectModule(ctx, p.ID, "chat"); err != nil {
		t.Fatal(err)
	}
	mods, _ = st.ListProjectModules(ctx, p.ID)
	if len(mods) != 0 {
		t.Fatalf("子模块应一起删除: %+v", mods)
	}
	got, _ := st.GetProjectDocByID(ctx, d.Doc.ID)
	if got.ModulePath != "" || got.ProjectID != p.ID {
		t.Fatalf("文档解除绑定但仍在项目里: %+v", got)
	}
}

func TestDocsFollowTheirWorkflowIntoProjects(t *testing.T) {
	st, ctx := projTestStore(t)
	p, _ := st.CreateProject(ctx, ProjectInput{Name: "P", Slug: "p"})
	_, _ = st.AddProjectDir(ctx, p.ID, "h", "/p/main", "")
	_, _ = st.AddProjectDir(ctx, p.ID, "h", "/p/branch", "")
	now := time.Now()
	seedWorkflowDir(t, st, "wf-main", "h", "/p/main", now)
	seedWorkflowDir(t, st, "wf-branch", "h", "/p/branch", now)
	seedWorkflowDir(t, st, "wf-out", "h", "/other", now)
	ag := DocActor{Kind: model.DocActorAgent}

	// 没有归属项目的工作流：完全是老行为
	r, err := st.CreateProjectDoc(ctx, ProjectDocInput{WorkflowID: "wf-out", Path: "overview.md", Body: "x"}, ag)
	if err != nil || r.Doc.ProjectID != 0 || r.Doc.Render != model.DocRenderMarkdown {
		t.Fatalf("无项目: %+v %v", r, err)
	}
	if _, err := st.CreateProjectDoc(ctx, ProjectDocInput{WorkflowID: "wf-out", Path: "m.md", Body: "x", ModulePath: "a"}, ag); err == nil {
		t.Fatal("无项目不能绑定模块")
	}

	// 项目内：自动归入项目
	a, err := st.CreateProjectDoc(ctx, ProjectDocInput{WorkflowID: "wf-main", Path: "overview.md", Body: "main"}, ag)
	if err != nil || a.Doc.ProjectID != p.ID {
		t.Fatalf("应归入项目: %+v %v", a, err)
	}
	// 另一个分支写同一路径 → 冲突并指明占用者
	_, err = st.CreateProjectDoc(ctx, ProjectDocInput{WorkflowID: "wf-branch", Path: "overview.md", Body: "branch"}, ag)
	wantAppErr(t, err, "wf-main")
	// 不同路径正常
	b, err := st.CreateProjectDoc(ctx, ProjectDocInput{WorkflowID: "wf-branch", Path: "sessions/wf-branch/notes.md", Body: "n"}, ag)
	if err != nil || b.Doc.ProjectID != p.ID {
		t.Fatalf("%+v %v", b, err)
	}
	// 改路径撞上项目内别人的路径
	np := "overview.md"
	_, err = st.UpdateProjectDoc(ctx, b.Doc.ID, ProjectDocPatch{Path: &np}, ag)
	wantAppErr(t, err, "wf-main")

	// 模块：必须已建立；modules/<m>/… 自动绑定到最长匹配
	_, err = st.CreateProjectDoc(ctx, ProjectDocInput{WorkflowID: "wf-main", Path: "x.md", Body: "x", ModulePath: "nope"}, ag)
	wantAppErr(t, err, "还没有建立")
	_, _ = st.UpsertProjectModule(ctx, p.ID, ModuleInput{Path: "chat/groups", Title: "群聊"})
	c, err := st.CreateProjectDoc(ctx, ProjectDocInput{WorkflowID: "wf-main", Path: "modules/chat/groups/design.md", Body: "d"}, ag)
	if err != nil || c.Doc.ModulePath != "chat/groups" {
		t.Fatalf("自动绑定: %+v %v", c, err)
	}
	c2, _ := st.CreateProjectDoc(ctx, ProjectDocInput{WorkflowID: "wf-main", Path: "modules/chat/other/x.md", Body: "d"}, ag)
	if c2.Doc.ModulePath != "chat" {
		t.Fatalf("取最长匹配的已建模块（chat/other 没建，退到 chat）: %q", c2.Doc.ModulePath)
	}
	c3, _ := st.CreateProjectDoc(ctx, ProjectDocInput{WorkflowID: "wf-main", Path: "modules/zzz/x.md", Body: "d"}, ag)
	if c3.Doc.ModulePath != "" {
		t.Fatalf("没有对应模块不绑定: %q", c3.Doc.ModulePath)
	}
	// 更新：绑定 / 解绑 / 渲染类型
	mp, rd := "chat/groups", "mermaid"
	u, err := st.UpdateProjectDoc(ctx, c2.Doc.ID, ProjectDocPatch{ModulePath: &mp, Render: &rd}, ag)
	if err != nil || u.Doc.ModulePath != "chat/groups" || u.Doc.Render != "mermaid" || !u.Changed {
		t.Fatalf("更新绑定: %+v %v", u, err)
	}
	empty := ""
	u, err = st.UpdateProjectDoc(ctx, c2.Doc.ID, ProjectDocPatch{ModulePath: &empty}, ag)
	if err != nil || u.Doc.ModulePath != "" {
		t.Fatalf("解绑: %+v %v", u, err)
	}
	bad := "pdf"
	if _, err := st.UpdateProjectDoc(ctx, c2.Doc.ID, ProjectDocPatch{Render: &bad}, ag); err == nil {
		t.Fatal("非法渲染类型应被拒绝")
	}

	// 项目范围的列表：跨工作流，按模块 / 渲染过滤
	all, _ := st.ListProjectDocs(ctx, ProjectDocFilter{ProjectID: p.ID})
	if len(all) != 5 {
		t.Fatalf("项目里应有 5 篇（不含无项目的那篇）: %d", len(all))
	}
	// 此时 c（chat/groups）与 c2（已改绑到 chat/groups 后又解绑）——c2 解绑了，所以含子模块的 chat 只命中 c
	mod, _ := st.ListProjectDocs(ctx, ProjectDocFilter{ProjectID: p.ID, ModulePath: "chat"})
	if len(mod) != 1 || mod[0].ID != c.Doc.ID {
		t.Fatalf("模块过滤含子模块: %+v", mod)
	}
}

func TestProjectMigrationDryRunApplyAndCompat(t *testing.T) {
	st, ctx := projTestStore(t)
	now := time.Now()
	seedWorkflowDir(t, st, "wf-a", "h", "/p/a", now)
	seedWorkflowDir(t, st, "wf-b", "h", "/p/b", now)
	seedWorkflowDir(t, st, "wf-x", "h", "/x", now)
	ag := DocActor{Kind: model.DocActorAgent}
	mk := func(wf, path, body string) uint {
		r, err := st.CreateProjectDoc(ctx, ProjectDocInput{WorkflowID: wf, Path: path, Body: body}, ag)
		if err != nil {
			t.Fatal(err)
		}
		time.Sleep(2 * time.Millisecond)
		return r.Doc.ID
	}
	aOverview := mk("wf-a", "overview.md", "A 的总览")
	aSheet := mk("wf-a", "session/workflow-sheet.md", "A 的会话")
	bOverview := mk("wf-b", "overview.md", "B 的总览")
	bSheet := mk("wf-b", "session/workflow-sheet.md", "B 的会话")
	bUnique := mk("wf-b", "design/only-b.md", "只有 B")
	xDoc := mk("wf-x", "overview.md", "无关")
	// 追加一次修订，迁移后修订不变
	if _, err := st.UpdateProjectDoc(ctx, bUnique, ProjectDocPatch{Body: ptr("只有 B（改）")}, ag); err != nil {
		t.Fatal(err)
	}

	p, _ := st.CreateProject(ctx, ProjectInput{Name: "P", Slug: "p"})
	rep0, err := st.MigrationDryRun(ctx, p.ID)
	if err != nil || rep0.Docs != 0 || len(rep0.Workflows) != 0 {
		t.Fatalf("还没归入目录时什么都不迁: %+v %v", rep0, err)
	}
	if rep0.Items == nil || len(rep0.Items) != 0 {
		t.Fatalf("没有可迁的文档时 items 是空数组（序列化成 []，不是 null）: %#v", rep0.Items)
	}
	_, _ = st.AddProjectDir(ctx, p.ID, "h", "/p/a", "")
	_, _ = st.AddProjectDir(ctx, p.ID, "h", "/p/b", "")

	rep, err := st.MigrationDryRun(ctx, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Docs != 5 || rep.Assigned != 2 || len(rep.Moves) != 3 || rep.Applied || len(rep.Workflows) != 2 {
		t.Fatalf("dry-run 报告: %+v", rep)
	}
	moves := map[uint]MigrationMove{}
	for _, m := range rep.Moves {
		moves[m.DocID] = m
	}
	if m := moves[aSheet]; m.ToPath != "sessions/wf-a/workflow-sheet.md" || m.Reason != "sheet" {
		t.Fatalf("A 的会话文档: %+v", m)
	}
	if m := moves[bSheet]; m.ToPath != "sessions/wf-b/workflow-sheet.md" {
		t.Fatalf("B 的会话文档各有一份、不冲突: %+v", m)
	}
	if m := moves[bOverview]; m.ToPath != "legacy/wf-b/overview.md" || m.Reason != "conflict" {
		t.Fatalf("同路径的后来者落到 legacy: %+v", m)
	}
	// items 把每一篇待迁移的文档都列出来：路径不变的也在（原因为空、去向 = 原路径），改路径的与 moves 一致。
	if len(rep.Items) != rep.Docs {
		t.Fatalf("items 应列全部 %d 篇: %+v", rep.Docs, rep.Items)
	}
	items := map[uint]MigrationItem{}
	for _, it := range rep.Items {
		items[it.DocID] = it
	}
	for id, want := range map[uint]struct{ wf, from, to, reason string }{
		aOverview: {"wf-a", "overview.md", "overview.md", ""},
		bUnique:   {"wf-b", "design/only-b.md", "design/only-b.md", ""},
		aSheet:    {"wf-a", "session/workflow-sheet.md", "sessions/wf-a/workflow-sheet.md", "sheet"},
		bSheet:    {"wf-b", "session/workflow-sheet.md", "sessions/wf-b/workflow-sheet.md", "sheet"},
		bOverview: {"wf-b", "overview.md", "legacy/wf-b/overview.md", "conflict"},
	} {
		it, ok := items[id]
		if !ok || it.WorkflowID != want.wf || it.FromPath != want.from || it.ToPath != want.to || it.Reason != want.reason || it.Title == "" {
			t.Fatalf("文档 %d 的去向: %+v want %+v", id, it, want)
		}
	}
	if _, ok := items[xDoc]; ok {
		t.Fatal("项目外的工作流不该出现在 items 里")
	}
	// dry-run 不改任何东西
	if d, _ := st.GetProjectDocByID(ctx, bOverview); d.ProjectID != 0 || d.Path != "overview.md" {
		t.Fatalf("dry-run 不应落库: %+v", d)
	}
	// 没带哈希 / 哈希不对 → 拒绝
	if _, err := st.ApplyMigration(ctx, p.ID, ""); err == nil {
		t.Fatal("没带哈希应拒绝")
	}
	if _, err := st.ApplyMigration(ctx, p.ID, "deadbeef"); err == nil {
		t.Fatal("哈希不对应拒绝")
	}
	// 期间文档变了 → 哈希变了 → 拒绝
	if _, err := st.UpdateProjectDoc(ctx, aOverview, ProjectDocPatch{Body: ptr("A 的总览（又改）")}, ag); err != nil {
		t.Fatal(err)
	}
	wantAppErr(t, func() error { _, e := st.ApplyMigration(ctx, p.ID, rep.Hash); return e }(), "重新 dry-run")

	rep, _ = st.MigrationDryRun(ctx, p.ID)
	done, err := st.ApplyMigration(ctx, p.ID, rep.Hash)
	if err != nil || !done.Applied {
		t.Fatalf("确认迁移: %+v %v", done, err)
	}
	if len(done.Items) != 5 {
		t.Fatalf("确认后的报告同样带全部去向（审计靠它记下每一篇）: %+v", done.Items)
	}
	want := map[uint]string{
		aOverview: "overview.md", aSheet: "sessions/wf-a/workflow-sheet.md",
		bOverview: "legacy/wf-b/overview.md", bSheet: "sessions/wf-b/workflow-sheet.md", bUnique: "design/only-b.md",
	}
	for id, path := range want {
		d, err := st.GetProjectDocByID(ctx, id)
		if err != nil || d.ProjectID != p.ID || d.Path != path {
			t.Fatalf("文档 %d 应在 %s: %+v %v", id, path, d, err)
		}
	}
	if d, _ := st.GetProjectDocByID(ctx, xDoc); d.ProjectID != 0 {
		t.Fatal("项目外的工作流不受影响")
	}
	// id / 修订不变；旧调用按工作流 + 新路径照常读
	revs, _ := st.ListProjectDocRevisions(ctx, bUnique)
	if len(revs) != 2 {
		t.Fatalf("修订历史不变: %d", len(revs))
	}
	if d, err := st.GetProjectDocByRef(ctx, "wf-b", "legacy/wf-b/overview.md"); err != nil || d.ID != bOverview {
		t.Fatalf("按工作流读: %+v %v", d, err)
	}
	// 再来一次：没有可迁的了；已有的占着路径
	again, _ := st.MigrationDryRun(ctx, p.ID)
	if again.Docs != 0 || again.Already != 5 {
		t.Fatalf("迁移后再 dry-run: %+v", again)
	}
	// 迁移后项目内路径唯一生效
	_, err = st.CreateProjectDoc(ctx, ProjectDocInput{WorkflowID: "wf-b", Path: "overview.md", Body: "z"}, ag)
	wantAppErr(t, err, "wf-a")
}

func TestAuditEventsDedupeAndFilter(t *testing.T) {
	st, ctx := projTestStore(t)
	in := AuditInput{ProjectID: 3, ActorKind: "identity", ActorRef: "identity:5", ActorLabel: "core-app", Action: model.AuditDocReadCross,
		ScopeKind: "workflow", ScopeRef: "wf-1", Target: "design/a.md", Dedupe: AuditCrossReadDedupe, Detail: map[string]string{"via": "workflow_id"}}
	ok, err := st.AddAuditEvent(ctx, in)
	if err != nil || !ok {
		t.Fatal(ok, err)
	}
	if ok, _ := st.AddAuditEvent(ctx, in); ok {
		t.Fatal("窗口内同主体同范围应被去重")
	}
	in2 := in
	in2.ScopeRef = "wf-2"
	if ok, _ := st.AddAuditEvent(ctx, in2); !ok {
		t.Fatal("范围不同不该去重")
	}
	in3 := in
	in3.Dedupe = 0
	if ok, _ := st.AddAuditEvent(ctx, in3); !ok {
		t.Fatal("没开去重就都写")
	}
	_, _ = st.AddAuditEvent(ctx, AuditInput{ActorKind: "human", ActorRef: "human", Action: model.AuditGrant, ScopeKind: "project", ScopeRef: "p"})

	all, _ := st.ListAuditEvents(ctx, AuditFilter{})
	if len(all) != 4 || all[0].Action != model.AuditGrant || all[0].ActorKind != "human" || all[0].Result != model.AuditAllow {
		t.Fatalf("倒序且默认 allow: %+v", all)
	}
	if got, _ := st.ListAuditEvents(ctx, AuditFilter{Action: "doc."}); len(got) != 3 {
		t.Fatalf("前缀筛选: %d", len(got))
	}
	if got, _ := st.ListAuditEvents(ctx, AuditFilter{ActorRef: "identity:5", ScopeRef: "wf-2"}); len(got) != 1 {
		t.Fatalf("按主体 + 范围: %d", len(got))
	}
	if got, _ := st.ListAuditEvents(ctx, AuditFilter{Since: time.Now().Add(time.Minute)}); len(got) != 0 {
		t.Fatal("时间筛选")
	}
	page, _ := st.ListAuditEvents(ctx, AuditFilter{Limit: 2})
	next, _ := st.ListAuditEvents(ctx, AuditFilter{Limit: 2, BeforeID: page[1].ID})
	if len(page) != 2 || len(next) != 2 || next[0].ID >= page[1].ID {
		t.Fatalf("翻页: %d %d", len(page), len(next))
	}
	// 详情是 JSON，且只含传入的字段
	if !strings.Contains(all[3].Detail, `"via":"workflow_id"`) {
		t.Fatalf("detail: %q", all[3].Detail)
	}
}

// DedupeExact（#179）：同一目标、同一原因的重复才合并；不同的原因或目标各记各的，
// 否则一个管理 Agent 先被「不是管理 Agent」拒了一次，之后 30 分钟里所有别的拒绝原因都从审计里消失。
func TestAuditEventsDedupeExact(t *testing.T) {
	st, ctx := projTestStore(t)
	in := AuditInput{ProjectID: 3, ActorKind: "identity", ActorRef: "identity:5", ActorLabel: "mgr", Action: model.AuditLauncherRun, Result: model.AuditDeny,
		ScopeKind: model.ScopeTasks, ScopeRef: "3", Target: "preset:a", Dedupe: AuditCrossReadDedupe, DedupeExact: true, Detail: map[string]any{"code": "run_active"}}
	if ok, err := st.AddAuditEvent(ctx, in); err != nil || !ok {
		t.Fatal(ok, err)
	}
	if ok, _ := st.AddAuditEvent(ctx, in); ok {
		t.Fatal("同一目标、同一原因应被去重")
	}
	reason := in
	reason.Detail = map[string]any{"code": "agent_run_hourly"}
	if ok, _ := st.AddAuditEvent(ctx, reason); !ok {
		t.Fatal("原因不同不该去重")
	}
	other := in
	other.Target = "preset:b"
	if ok, _ := st.AddAuditEvent(ctx, other); !ok {
		t.Fatal("目标不同不该去重")
	}
	// 宽松去重（DedupeExact=false）的行为不变：同主体同范围同结果就合并，不看目标与详情。
	loose := in
	loose.DedupeExact = false
	loose.Target = "preset:c"
	loose.Detail = map[string]any{"code": "own_workflow"}
	if ok, _ := st.AddAuditEvent(ctx, loose); ok {
		t.Fatal("宽松去重应沿用旧行为（窗口内已有同主体同范围同结果就不写）")
	}
	all, _ := st.ListAuditEvents(ctx, AuditFilter{Action: model.AuditLauncherRun})
	if len(all) != 3 {
		t.Fatalf("应有三条（run_active/preset:a、agent_run_hourly/preset:a、run_active/preset:b）: %d", len(all))
	}
}

func TestProjectSearchModulePageAndBlueprint(t *testing.T) {
	st, ctx := projTestStore(t)
	p, _ := st.CreateProject(ctx, ProjectInput{Name: "P", Slug: "p"})
	_, _ = st.AddProjectDir(ctx, p.ID, "h", "/p/main", "")
	_, _ = st.AddProjectDir(ctx, p.ID, "h", "/p/branch", "")
	now := time.Now()
	seedWorkflowDir(t, st, "wf-main", "h", "/p/main", now)
	seedWorkflowDir(t, st, "wf-branch", "h", "/p/branch", now)
	ag := DocActor{Kind: model.DocActorAgent}
	_, _ = st.UpsertProjectModule(ctx, p.ID, ModuleInput{Path: "chat/groups", Title: "群聊", Description: "群与成员"})
	mk := func(wf, path, kind, body, render string) uint {
		r, err := st.CreateProjectDoc(ctx, ProjectDocInput{WorkflowID: wf, Path: path, Kind: kind, Body: body, Render: render}, ag)
		if err != nil {
			t.Fatal(err)
		}
		time.Sleep(2 * time.Millisecond)
		return r.Doc.ID
	}
	idx := mk("wf-main", "modules/chat/groups/index.md", "design", "# 群聊总述\n成员与投递规则", "")
	des := mk("wf-branch", "modules/chat/groups/design.md", "design", "# 设计\n投递规则表：人不 @ 只送 push_policy=all", "")
	dec := mk("wf-main", "modules/chat/groups/decisions.md", "decision", "# 决策\n群默认只在 @我 时通知", "")
	art := mk("wf-main", "modules/chat/groups/flow.mmd", "design", "graph TD; A-->B", "mermaid")
	_ = mk("wf-branch", "modules/chat/groups/api.md", "reference", "# API\nchat send", "")
	_ = mk("wf-main", "overview.md", "design", "# 总览\n整体设计", "")

	// 搜索跨工作流：命中另一个分支写的文档
	hits, err := st.SearchProject(ctx, ProjectSearchFilter{ProjectID: p.ID, Query: "投递规则"})
	if err != nil || len(hits) == 0 {
		t.Fatalf("hits=%v err=%v", hits, err)
	}
	foundDes := false
	for _, h := range hits {
		if h.DocID == des && h.WorkflowID == "wf-branch" && h.ModulePath == "chat/groups" && h.Snippet != "" {
			foundDes = true
		}
	}
	if !foundDes {
		t.Fatalf("应命中 wf-branch 的设计文档: %+v", hits)
	}
	// 模块 / 类型 / 渲染过滤；没有关键词时按最近更新排序
	byMod, _ := st.SearchProject(ctx, ProjectSearchFilter{ProjectID: p.ID, ModulePath: "chat"})
	if len(byMod) != 5 {
		t.Fatalf("模块过滤含子模块: %d", len(byMod))
	}
	if byMod[0].UpdatedAt.Before(byMod[len(byMod)-1].UpdatedAt) {
		t.Fatal("无关键词应按最近更新倒序")
	}
	if got, _ := st.SearchProject(ctx, ProjectSearchFilter{ProjectID: p.ID, Render: "mermaid"}); len(got) != 1 || got[0].DocID != art {
		t.Fatalf("按渲染过滤: %+v", got)
	}
	if got, _ := st.SearchProject(ctx, ProjectSearchFilter{ProjectID: p.ID, Kinds: []string{"decision"}, Query: "通知"}); len(got) != 1 || got[0].DocID != dec {
		t.Fatalf("按类型 + 关键词: %+v", got)
	}
	if got, _ := st.SearchProject(ctx, ProjectSearchFilter{ProjectID: p.ID, Query: "不存在的词"}); len(got) != 0 {
		t.Fatalf("无命中: %+v", got)
	}
	if got, _ := st.SearchProject(ctx, ProjectSearchFilter{ProjectID: p.ID, UpdatedAfter: time.Now().Add(time.Minute)}); len(got) != 0 {
		t.Fatal("时间过滤")
	}

	// 任务看板按名称对齐：模块「群聊」下的任务出现在模块页里
	parent := model.WorkModule{WorkflowID: "wf-main", Name: "聊天", CreatedAt: now, UpdatedAt: now}
	_ = st.db.Create(&parent).Error
	child := model.WorkModule{WorkflowID: "wf-main", ParentID: &parent.ID, Name: "群聊", CreatedAt: now, UpdatedAt: now}
	_ = st.db.Create(&child).Error
	other := model.WorkModule{WorkflowID: "wf-main", Name: "通知", CreatedAt: now, UpdatedAt: now}
	_ = st.db.Create(&other).Error
	for i, mid := range []uint{child.ID, child.ID, other.ID} {
		it := model.WorkItem{WorkflowID: "wf-main", Seq: i + 1, Title: "任务" + string(rune('A'+i)), State: "todo", ModuleID: &mid, CreatedAt: now, UpdatedAt: now}
		if err := st.db.Create(&it).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := st.db.Model(&model.ProjectModule{}).Where("project_id = ? AND path = ?", p.ID, "chat/groups").Update("owner_identity_id", 0).Error; err != nil {
		t.Fatal(err)
	}

	page, err := st.ModulePage(ctx, p.ID, "chat/groups")
	if err != nil {
		t.Fatal(err)
	}
	if page.Module.Title != "群聊" || page.Index == nil || page.Index.ID != idx || page.Design == nil || page.Design.ID != des {
		t.Fatalf("总述 / 设计: %+v", page)
	}
	if len(page.Decisions) != 1 || page.Decisions[0].ID != dec || len(page.Artifacts) != 1 || page.Artifacts[0].ID != art || len(page.Docs) != 1 {
		t.Fatalf("分组: dec=%d art=%d docs=%d", len(page.Decisions), len(page.Artifacts), len(page.Docs))
	}
	if len(page.Recent) != 5 || page.Recent[0].UpdatedAt.Before(page.Recent[4].UpdatedAt) {
		t.Fatalf("最近修订: %+v", page.Recent)
	}
	if len(page.Tasks) != 2 {
		t.Fatalf("相关任务按名称对齐（群聊 ×2，不含通知）: %+v", page.Tasks)
	}
	parentPage, err := st.ModulePage(ctx, p.ID, "chat")
	if err != nil || len(parentPage.Children) != 1 || parentPage.Children[0].Path != "chat/groups" || len(parentPage.Docs) != 0 && parentPage.Index != nil {
		t.Fatalf("父模块页: %+v %v", parentPage, err)
	}
	if _, err := st.ModulePage(ctx, p.ID, "nope"); err == nil {
		t.Fatal("模块不存在应报错")
	}

	// 蓝图：固定几篇 + 每个模块两篇；已有的标 present
	bp, err := st.ProjectBlueprint(ctx, p.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	present, missing := map[string]bool{}, 0
	for _, it := range bp {
		present[it.Path] = it.Present
		if !it.Present {
			missing++
			if it.Template == "" {
				t.Fatalf("缺口应带模板: %+v", it)
			}
		}
	}
	if !present["overview.md"] || !present["modules/chat/groups/index.md"] || !present["modules/chat/groups/design.md"] {
		t.Fatalf("已有的应标 present: %+v", present)
	}
	if present["architecture/context.md"] || present["modules/chat/index.md"] {
		t.Fatalf("没有的应标缺口: %+v", present)
	}
	if missing == 0 {
		t.Fatal("应有缺口")
	}
}

// 同一个工作流在同一毫秒里造两次会话不能撞主键（TestClaimStaleOffersDueAndOnce 曾因此间歇性失败：boardFixture 已造过 wf-solo）。
func TestSeedWorkflowDirSameMillisecond(t *testing.T) {
	st, _ := projTestStore(t)
	at := time.Now()
	seedWorkflowDir(t, st, "wf-x", "devhost", "/a", at)
	seedWorkflowDir(t, st, "wf-x", "devhost", "/b", at)
	var n int64
	if err := st.db.Model(&model.FeedbackSession{}).Where("workflow_id = ?", "wf-x").Count(&n).Error; err != nil || n != 2 {
		t.Fatalf("应有两条会话: n=%d err=%v", n, err)
	}
}
