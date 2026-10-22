package mcp

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/teacat99/RelayMesh/v2/internal/model"
	"github.com/teacat99/RelayMesh/v2/internal/store"
)

func TestProjectDocsOverviewAndSave(t *testing.T) {
	srv := setupTestMCPServer(t)
	wf := "wf-pdocs-mcp"
	args, _ := json.Marshal(map[string]any{
		"workflow_id": wf,
		"action":      "save",
		"path":        "design/test.md",
		"body":        "# Test\n\nHello",
	})
	out, err := srv.handleProjectDocs(context.Background(), args)
	if err != nil {
		t.Fatal(err)
	}
	m := out.(map[string]any)
	if m["created"] != true {
		t.Fatalf("%+v", m)
	}
	args2, _ := json.Marshal(map[string]any{"workflow_id": wf, "action": "overview"})
	ov, err := srv.handleProjectDocs(context.Background(), args2)
	if err != nil {
		t.Fatal(err)
	}
	tot := ov.(map[string]any)["total"]
	if tot != float64(1) && tot != 1 {
		t.Fatalf("%+v", ov)
	}
}

func TestProjectDocsPermissions(t *testing.T) {
	p := model.Permissions{Feedback: true}
	if !p.AllowsTool("project_docs") {
		t.Fatal("feedback should allow project_docs")
	}
	if (model.Permissions{Terminal: true}).AllowsTool("project_docs") {
		t.Fatal("terminal only should not")
	}
	_ = strings.Contains
}

func TestProjectDocsExportPaginationAndKnown(t *testing.T) {
	srv := setupTestMCPServer(t)
	wf := "wf-export"
	for _, p := range []string{"a.md", "b.md", "c.md"} {
		args, _ := json.Marshal(map[string]any{"workflow_id": wf, "action": "save", "path": p, "body": "body " + p})
		if _, err := srv.handleProjectDocs(context.Background(), args); err != nil {
			t.Fatal(err)
		}
	}
	args, _ := json.Marshal(map[string]any{
		"workflow_id": wf, "action": "export", "max_bytes": 30, "known": map[string]string{"a.md": "dummy"},
	})
	out, err := srv.handleProjectDocs(context.Background(), args)
	if err != nil {
		t.Fatal(err)
	}
	m := out.(map[string]any)
	if m["files"] == nil || m["index_md"] == "" {
		t.Fatalf("%+v", m)
	}
	args2, _ := json.Marshal(map[string]any{"workflow_id": wf, "action": "export", "paths": []string{"a.md"}})
	out2, err := srv.handleProjectDocs(context.Background(), args2)
	if err != nil {
		t.Fatal(err)
	}
	f2m := out2.(map[string]any)
	switch files2 := f2m["files"].(type) {
	case []map[string]any:
		if len(files2) != 1 {
			t.Fatalf("%+v", out2)
		}
	case []any:
		if len(files2) != 1 {
			t.Fatalf("%+v", out2)
		}
	default:
		t.Fatalf("files type %T", f2m["files"])
	}
}

// 项目范围（#114）：不带 scope 的老调用不变；带 scope=project 才跨工作流。
func TestProjectDocsProjectScope(t *testing.T) {
	srv := setupTestMCPServer(t)
	ctx := context.Background()
	call := func(m map[string]any) (map[string]any, error) {
		raw, _ := json.Marshal(m)
		out, err := srv.handleProjectDocs(ctx, raw)
		if err != nil {
			return nil, err
		}
		b, _ := json.Marshal(out) // 统一成 JSON 视角，免得断言依赖具体 Go 类型
		var res map[string]any
		_ = json.Unmarshal(b, &res)
		return res, nil
	}
	mustCall := func(m map[string]any) map[string]any {
		t.Helper()
		out, err := call(m)
		if err != nil {
			t.Fatalf("%v: %v", m, err)
		}
		return out
	}

	// 项目还没建：scope=project 必须明确报错并说明谁能建项目，不能悄悄退回工作流范围。
	if _, err := srv.store.CreateFeedbackSession(ctx, store.CreateSessionInput{SessionID: "s-pa", WorkflowID: "wf-pa", EnvHostName: "h", ProjectDirectory: "/code/p", Title: "t", Summary: "s"}); err != nil {
		t.Fatal(err)
	}
	if _, err := srv.store.CreateFeedbackSession(ctx, store.CreateSessionInput{SessionID: "s-pb", WorkflowID: "wf-pb", EnvHostName: "h", ProjectDirectory: "/code/p-feat", Title: "t", Summary: "s"}); err != nil {
		t.Fatal(err)
	}
	if _, err := call(map[string]any{"workflow_id": "wf-pa", "scope": "project"}); err == nil || !strings.Contains(err.Error(), "不能自建项目") {
		t.Fatalf("无项目应报明确错误: %v", err)
	}
	// 老调用：照常保存，不需要项目。
	if r := mustCall(map[string]any{"workflow_id": "wf-pa", "action": "save", "path": "design/old.md", "body": "# 老文档"}); r["created"] != true {
		t.Fatalf("老调用: %+v", r)
	}

	p, err := srv.store.CreateProject(ctx, store.ProjectInput{Name: "P", Slug: "p"})
	if err != nil {
		t.Fatal(err)
	}
	_, _ = srv.store.AddProjectDir(ctx, p.ID, "h", "/code/p", "")
	_, _ = srv.store.AddProjectDir(ctx, p.ID, "h", "/code/p-feat", "")
	_, _ = srv.store.UpsertProjectModule(ctx, p.ID, store.ModuleInput{Path: "chat", Title: "聊天"})

	// 老调用在加入项目之后依旧只看本工作流。
	if r := mustCall(map[string]any{"workflow_id": "wf-pb", "action": "list"}); r["count"] != float64(0) {
		t.Fatalf("工作流范围的 list 不应看到别的工作流: %+v", r)
	}

	// 两个工作流各写一篇，模块自动绑定；渲染方式可选。
	r := mustCall(map[string]any{"workflow_id": "wf-pa", "action": "save", "path": "modules/chat/index.md", "body": "# 聊天总述\n投递规则", "scope": "project"})
	if r["created"] != true || r["doc"].(map[string]any)["module_path"] != "chat" {
		t.Fatalf("按路径自动绑定模块: %+v", r)
	}
	r = mustCall(map[string]any{"workflow_id": "wf-pb", "action": "save", "path": "modules/chat/flow.md", "body": "graph TD; A-->B", "render": "mermaid", "module_path": "chat"})
	if r["doc"].(map[string]any)["render"] != "mermaid" {
		t.Fatalf("render: %+v", r)
	}

	// 项目范围读：总览、列表（无正文）、搜索（跨工作流）、get（别的工作流的文档）。
	ov := mustCall(map[string]any{"workflow_id": "wf-pb", "scope": "project"})
	// 加入项目之前写的老文档要等用户确认迁移才归入，所以这里只有 2 篇。
	if ov["total"] != float64(2) || len(ov["modules"].([]any)) != 1 {
		t.Fatalf("项目总览: %+v", ov)
	}
	ls := mustCall(map[string]any{"workflow_id": "wf-pb", "action": "list", "scope": "project", "module": "chat"})
	if ls["count"] != float64(2) {
		t.Fatalf("项目 list: %+v", ls)
	}
	if b, _ := json.Marshal(ls); strings.Contains(string(b), "投递规则") {
		t.Fatal("项目 list 不带正文")
	}
	sr := mustCall(map[string]any{"workflow_id": "wf-pb", "action": "search", "scope": "project", "query": "投递规则"})
	if hits := sr["hits"].([]any); len(hits) != 1 || hits[0].(map[string]any)["workflow_id"] != "wf-pa" {
		t.Fatalf("跨工作流搜索: %+v", sr)
	}
	gt := mustCall(map[string]any{"workflow_id": "wf-pb", "action": "get", "scope": "project", "path": "modules/chat/index.md"})
	if !strings.Contains(gt["body"].(string), "聊天总述") {
		t.Fatalf("get 别的工作流的文档: %+v", gt)
	}
	mp := mustCall(map[string]any{"workflow_id": "wf-pb", "action": "module", "module": "chat"})
	if mp["page"].(map[string]any)["module"].(map[string]any)["path"] != "chat" {
		t.Fatalf("module 页: %+v", mp)
	}
	if bp := mustCall(map[string]any{"workflow_id": "wf-pb", "action": "blueprint", "scope": "project"}); bp["gaps"].(float64) < 1 {
		t.Fatalf("项目蓝图: %+v", bp)
	}

	// 改别的工作流的文档：必须带 expected_rev；带了就能改，且 rev 递增。
	if _, err := call(map[string]any{"workflow_id": "wf-pb", "action": "save", "scope": "project", "path": "modules/chat/index.md", "append": "补充一行"}); err == nil || !strings.Contains(err.Error(), "expected_rev") {
		t.Fatalf("跨工作流改文档应要求 expected_rev: %v", err)
	}
	up := mustCall(map[string]any{"workflow_id": "wf-pb", "action": "save", "scope": "project", "path": "modules/chat/index.md", "append": "补充一行", "expected_rev": 1})
	if up["doc"].(map[string]any)["rev"] != float64(2) || up["doc"].(map[string]any)["workflow_id"] != "wf-pa" {
		t.Fatalf("跨工作流更新: %+v", up)
	}
	// 非项目范围的 save 撞到项目内已有路径：仍被拒（项目内路径唯一）。
	if _, err := call(map[string]any{"workflow_id": "wf-pb", "action": "save", "path": "modules/chat/index.md", "body": "# 撞路径"}); err == nil {
		t.Fatal("项目内路径唯一：另一个工作流不能再建同路径")
	}
	// 范围外的动作给出指引。
	if _, err := call(map[string]any{"workflow_id": "wf-pb", "action": "delete", "scope": "project", "path": "x"}); err == nil || !strings.Contains(err.Error(), "不带 scope") {
		t.Fatalf("delete 在项目范围应给指引: %v", err)
	}
	// 用 project_directory 也能解析项目（工作流尚无会话时）。
	if r := mustCall(map[string]any{"workflow_id": "wf-new", "scope": "project", "project_directory": "/code/p", "host_name": "h"}); r["total"] == nil {
		t.Fatalf("按目录解析项目: %+v", r)
	}
	// 工作表导入：属于项目的工作流放进 sessions/<wf>/。
	if err := srv.store.SaveNote(ctx, &model.WorkflowNote{WorkflowID: "wf-pb", NoteKey: "session_doc", Content: "# 工作表\n内容"}); err != nil {
		t.Fatal(err)
	}
	r = mustCall(map[string]any{"workflow_id": "wf-pb", "action": "import_sheet"})
	if d, _ := r["doc"].(map[string]any); d == nil || d["path"] != "sessions/wf-pb/workflow-sheet.md" {
		t.Fatalf("import_sheet 路径: %+v", r)
	}
}

// Artifact 数据与评论：workflow 范围与 project 范围都能用；数据只能挂在 html / data 文档上；
// 乐观并发、上限、审计（不含值）、评论读写与解决。
func TestProjectDocsArtifactActions(t *testing.T) {
	srv := setupTestMCPServer(t)
	ctx := context.Background()
	call := func(m map[string]any) (map[string]any, error) {
		raw, _ := json.Marshal(m)
		out, err := srv.handleProjectDocs(ctx, raw)
		if err != nil {
			return nil, err
		}
		b, _ := json.Marshal(out)
		var res map[string]any
		_ = json.Unmarshal(b, &res)
		return res, nil
	}
	must := func(m map[string]any) map[string]any {
		t.Helper()
		out, err := call(m)
		if err != nil {
			t.Fatalf("%v: %v", m, err)
		}
		return out
	}
	if _, err := srv.store.CreateFeedbackSession(ctx, store.CreateSessionInput{SessionID: "s-ar", WorkflowID: "wf-ar", EnvHostName: "h", ProjectDirectory: "/code/ar", Title: "t", Summary: "s"}); err != nil {
		t.Fatal(err)
	}
	must(map[string]any{"workflow_id": "wf-ar", "action": "save", "path": "artifacts/board.md", "body": "<p>board</p>", "render": "html"})
	must(map[string]any{"workflow_id": "wf-ar", "action": "save", "path": "notes/plain.md", "body": "# 普通文档"})

	// 数据只能挂在 Artifact 上。
	if _, err := call(map[string]any{"workflow_id": "wf-ar", "action": "data_set", "path": "notes/plain.md", "key": "k", "value": 1}); err == nil || !strings.Contains(err.Error(), "不是 Artifact") {
		t.Fatalf("普通文档不能挂数据: %v", err)
	}
	if _, err := call(map[string]any{"workflow_id": "wf-ar", "action": "data_set", "path": "artifacts/board.md", "key": "k"}); err == nil || !strings.Contains(err.Error(), "value") {
		t.Fatalf("缺 value 应报错: %v", err)
	}
	// 写 → 读 → 版本冲突 → 列 → 删。
	r := must(map[string]any{"workflow_id": "wf-ar", "action": "data_set", "path": "artifacts/board.md", "key": "cards", "value": map[string]any{"todo": []string{"a"}}, "expected_version": 0})
	if r["version"] != float64(1) {
		t.Fatalf("首次写入版本应为 1: %+v", r)
	}
	if _, err := call(map[string]any{"workflow_id": "wf-ar", "action": "data_set", "path": "artifacts/board.md", "key": "cards", "value": 2, "expected_version": 0}); err == nil || !strings.Contains(err.Error(), "已被别人更新") {
		t.Fatalf("期望新建但已存在应冲突: %v", err)
	}
	g := must(map[string]any{"workflow_id": "wf-ar", "action": "data_get", "path": "artifacts/board.md", "key": "cards"})
	if v, _ := g["value"].(map[string]any); v == nil || g["version"] != float64(1) {
		t.Fatalf("读回的值应是原样 JSON: %+v", g)
	}
	must(map[string]any{"workflow_id": "wf-ar", "action": "data_set", "path": "artifacts/board.md", "key": "n", "value": nil})
	if l := must(map[string]any{"workflow_id": "wf-ar", "action": "data_list", "path": "artifacts/board.md"}); l["count"] != float64(2) {
		t.Fatalf("列数据: %+v", l)
	}
	if d := must(map[string]any{"workflow_id": "wf-ar", "action": "data_delete", "path": "artifacts/board.md", "key": "n"}); d["deleted"] != true {
		t.Fatalf("删: %+v", d)
	}

	// 评论：Agent 自己留一条，读到，解决。
	c := must(map[string]any{"workflow_id": "wf-ar", "action": "comment", "path": "artifacts/board.md", "body": "卡片 2 缺负责人", "anchor": "card-2"})
	id := c["id"]
	cs := must(map[string]any{"workflow_id": "wf-ar", "action": "comments", "path": "artifacts/board.md"})
	if cs["count"] != float64(1) || cs["open"] != float64(1) {
		t.Fatalf("应有 1 条未解决评论: %+v", cs)
	}
	if _, err := call(map[string]any{"workflow_id": "wf-ar", "action": "comment_resolve", "path": "artifacts/board.md"}); err == nil || !strings.Contains(err.Error(), "comment_id") {
		t.Fatalf("缺 comment_id: %v", err)
	}
	must(map[string]any{"workflow_id": "wf-ar", "action": "comment_resolve", "path": "artifacts/board.md", "comment_id": id})
	if cs := must(map[string]any{"workflow_id": "wf-ar", "action": "comments", "path": "artifacts/board.md"}); cs["count"] != float64(0) {
		t.Fatalf("解决后默认不再列出: %+v", cs)
	}
	if cs := must(map[string]any{"workflow_id": "wf-ar", "action": "comments", "path": "artifacts/board.md", "include_resolved": true}); cs["count"] != float64(1) {
		t.Fatalf("include_resolved 应列出: %+v", cs)
	}

	// 审计：写入与删除各一条，且不含值。
	ev, _ := srv.store.ListAuditEvents(ctx, store.AuditFilter{Action: "artifact.data_write", Limit: 20})
	if len(ev) != 3 {
		t.Fatalf("应有 3 条数据审计（写 cards、写 n、删 n）: %d", len(ev))
	}
	for _, e := range ev {
		if strings.Contains(string(e.Detail), "todo") {
			t.Fatalf("审计不应含数据内容: %s", e.Detail)
		}
	}
}

func TestProjectDocsLinksAndSince(t *testing.T) {
	srv := setupTestMCPServer(t)
	ctx := context.Background()
	call := func(m map[string]any) (map[string]any, error) {
		raw, _ := json.Marshal(m)
		out, err := srv.handleProjectDocs(ctx, raw)
		if err != nil {
			return nil, err
		}
		b, _ := json.Marshal(out)
		var res map[string]any
		_ = json.Unmarshal(b, &res)
		return res, nil
	}
	must := func(m map[string]any) map[string]any {
		t.Helper()
		out, err := call(m)
		if err != nil {
			t.Fatalf("%v: %v", m, err)
		}
		return out
	}
	must(map[string]any{"workflow_id": "wf-lk", "action": "save", "path": "design/a.md", "body": "见 [[design/b]] 和 [[no/such]]"})
	must(map[string]any{"workflow_id": "wf-lk", "action": "save", "path": "design/b.md", "body": "# B"})

	b := must(map[string]any{"workflow_id": "wf-lk", "action": "links", "path": "design/b.md"})
	back, _ := b["backlinks"].([]any)
	if len(back) != 1 || back[0].(map[string]any)["path"] != "design/a.md" {
		t.Fatalf("反链不对: %v", b)
	}
	a := must(map[string]any{"workflow_id": "wf-lk", "action": "links", "path": "design/a.md"})
	out, _ := a["outlinks"].([]any)
	if len(out) != 2 || out[0].(map[string]any)["exists"] != true || out[1].(map[string]any)["exists"] != false {
		t.Fatalf("出链不对: %v", a)
	}

	// since：未来的时间点什么也没有；过去的时间点两篇都在；格式不对要报错。
	future := must(map[string]any{"workflow_id": "wf-lk", "action": "list", "since": "2999-01-01T00:00:00Z"})
	if future["count"].(float64) != 0 {
		t.Fatalf("since 在未来应为空: %v", future)
	}
	past := must(map[string]any{"workflow_id": "wf-lk", "action": "list", "since": "2000-01-01T00:00:00Z"})
	if past["count"].(float64) != 2 {
		t.Fatalf("since 在过去应有两篇: %v", past)
	}
	// list 只给元数据、不带正文（要正文用 get，批量用 export；#136）：每行有目录与版本信息，没有 body。
	rows, _ := past["items"].([]any)
	if len(rows) != 2 {
		t.Fatalf("list 的行数不对: %v", past)
	}
	for _, it := range rows {
		row := it.(map[string]any)
		if _, has := row["body"]; has {
			t.Fatalf("list 不应带正文: %v", row)
		}
		for _, k := range []string{"id", "workflow_id", "seq", "path", "title", "kind", "status", "rev", "hash", "created_at", "updated_at"} {
			if _, ok := row[k]; !ok {
				t.Fatalf("list 的行缺少 %s: %v", k, row)
			}
		}
	}
	if g := must(map[string]any{"workflow_id": "wf-lk", "action": "get", "path": "design/b.md"}); !strings.Contains(g["body"].(string), "# B") {
		t.Fatalf("get 仍应带正文: %v", g)
	}
	if _, err := call(map[string]any{"workflow_id": "wf-lk", "action": "list", "since": "昨天"}); err == nil {
		t.Fatal("非法 since 应报错")
	}
}
