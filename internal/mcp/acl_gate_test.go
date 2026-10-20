package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/teacat99/RelayMesh/v2/internal/model"
	"github.com/teacat99/RelayMesh/v2/internal/store"
)

// callToolRaw 经 /mcp 入口调用工具，返回文本和是否为工具错误。
func callToolRaw(t *testing.T, srv *Server, name string, args map[string]any) (string, bool) {
	t.Helper()
	body, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "tools/call",
		"params": map[string]any{"name": name, "arguments": args},
	})
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewReader(body)))
	var resp struct {
		Result struct {
			IsError bool `json:"isError"`
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"result"`
		Error any `json:"error"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil || resp.Error != nil || len(resp.Result.Content) == 0 {
		t.Fatalf("%s 响应异常: %v %s", name, err, w.Body.String())
	}
	return resp.Result.Content[0].Text, resp.Result.IsError
}

func TestACLGateAuditEnforceAndGrants(t *testing.T) {
	srv := setupTestMCPServer(t)
	ctx := context.Background()
	st := srv.store

	for _, w := range []struct{ id, dir string }{{"wf-a", "/code/a"}, {"wf-b", "/code/b"}, {"wf-x", "/code/x"}, {"wf-free", "/code/free"}} {
		if _, err := st.CreateFeedbackSession(ctx, store.CreateSessionInput{SessionID: "s-" + w.id, WorkflowID: w.id, EnvHostName: "h", ProjectDirectory: w.dir, Title: "t", Summary: "s"}); err != nil {
			t.Fatal(err)
		}
	}
	pp, _ := st.CreateProject(ctx, store.ProjectInput{Name: "P", Slug: "p"})
	_, _ = st.AddProjectDir(ctx, pp.ID, "h", "/code/a", "")
	_, _ = st.AddProjectDir(ctx, pp.ID, "h", "/code/b", "")
	pq, _ := st.CreateProject(ctx, store.ProjectInput{Name: "Q", Slug: "q"})
	_, _ = st.AddProjectDir(ctx, pq.ID, "h", "/code/x", "")

	// 以某个身份调用（带断言）/ 不带断言。
	as := func(wf, dir string, m map[string]any) map[string]any {
		out := map[string]any{"workflow_id": wf, "project_directory": dir, "host_name": "h"}
		for k, v := range m {
			out[k] = v
		}
		return out
	}
	anon := func(wf string, m map[string]any) map[string]any {
		out := map[string]any{"workflow_id": wf}
		for k, v := range m {
			out[k] = v
		}
		return out
	}
	mustOK := func(name string, args map[string]any) string {
		t.Helper()
		txt, isErr := callToolRaw(t, srv, name, args)
		if isErr {
			t.Fatalf("%s %v 不该失败: %s", name, args, txt)
		}
		return txt
	}
	mustFail := func(sub, name string, args map[string]any) {
		t.Helper()
		txt, isErr := callToolRaw(t, srv, name, args)
		if !isErr || !strings.Contains(txt, sub) {
			t.Fatalf("%s %v 应失败并含 %q: err=%v %s", name, args, sub, isErr, txt)
		}
	}
	auditCount := func(action, result string) int {
		t.Helper()
		items, err := st.ListAuditEvents(ctx, store.AuditFilter{Action: action, Result: result, Limit: 200})
		if err != nil {
			t.Fatal(err)
		}
		return len(items)
	}

	// A 先登记身份并建立模块、写第一篇文档（audit 档）；模块 chat 由 A 负责。
	mustOK("project_docs", as("wf-a", "/code/a", map[string]any{"action": "overview"}))
	idA, _ := st.ResolveCallerIdentity(ctx, "wf-a", 0, "h", "/code/a", timeNow())
	idB, _ := st.ResolveCallerIdentity(ctx, "wf-b", 0, "h", "/code/b", timeNow())
	idX, _ := st.ResolveCallerIdentity(ctx, "wf-x", 0, "h", "/code/x", timeNow())
	if idA == 0 || idB == 0 || idX == 0 || idA == idB {
		t.Fatalf("身份: %d %d %d", idA, idB, idX)
	}
	_, _ = st.UpsertProjectModule(ctx, pp.ID, store.ModuleInput{Path: "chat", Title: "聊天", OwnerIdentityID: idA})
	mustOK("project_docs", as("wf-a", "/code/a", map[string]any{"action": "save", "scope": "project", "path": "modules/chat/design.md", "body": "# 设计\nA 写的"}))
	doc, err := st.GetProjectDocInProject(ctx, pp.ID, "modules/chat/design.md")
	if err != nil || doc.OwnerIdentityID != idA {
		t.Fatalf("创建者应成为文档负责人: %+v %v", doc, err)
	}
	if auditCount(model.AuditDocWrite, model.AuditAllow) != 1 {
		t.Fatal("写入应记一条 doc.write（allow）")
	}

	// ① audit 档：B 改 A 的文档——放行，但记一条「若启用会被拒绝」。
	mustOK("project_docs", as("wf-b", "/code/b", map[string]any{"action": "save", "scope": "project", "path": "modules/chat/design.md", "append": "B 补充", "expected_rev": 1}))
	if n := auditCount(model.AuditACLDenied, model.AuditDeny); n != 1 {
		t.Fatalf("audit 档应记 1 条 acl.denied: %d", n)
	}
	// 没有身份断言的写：放行，记 assumed。
	mustOK("project_docs", anon("wf-b", map[string]any{"action": "save", "path": "design/b.md", "body": "# B"}))
	if n := auditCount(model.AuditACLDenied, model.AuditAssumed); n != 1 {
		t.Fatalf("没有断言应记 1 条 assumed: %d", n)
	}
	// 读别的工作流的文档：记 doc.read.cross。
	mustOK("project_docs", as("wf-b", "/code/b", map[string]any{"action": "get", "scope": "project", "path": "modules/chat/design.md"}))
	mustOK("project_docs", as("wf-b", "/code/b", map[string]any{"action": "get", "scope": "project", "path": "modules/chat/design.md"}))
	if n := auditCount(model.AuditDocReadCross, model.AuditAllow); n != 1 {
		t.Fatalf("30 分钟内同一范围只记一条: %d", n)
	}

	// ② 项目没归入 → 完全不判定；档位 off 也不判定。
	mustOK("project_docs", as("wf-free", "/code/free", map[string]any{"action": "save", "path": "x.md", "body": "x"}))
	before := auditCount(model.AuditACLDenied, "")
	off := "off"
	_, _ = st.UpdateProject(ctx, pp.ID, store.ProjectPatch{ACLMode: &off})
	mustOK("project_docs", as("wf-x", "/code/x", map[string]any{"action": "list", "workflow_id": "wf-a"}))
	if auditCount(model.AuditACLDenied, "") != before {
		t.Fatal("off 档不应产生审计")
	}

	// ③ enforce 档。
	enf := "enforce"
	if _, err := st.UpdateProject(ctx, pp.ID, store.ProjectPatch{ACLMode: &enf}); err != nil {
		t.Fatal(err)
	}
	// 没有身份断言的写 → identity_required；读不受影响。
	mustFail("identity_required", "project_docs", anon("wf-b", map[string]any{"action": "save", "path": "design/b2.md", "body": "x"}))
	mustOK("project_docs", anon("wf-b", map[string]any{"action": "list"}))
	// 别人改我负责的文档 → 拒绝；负责人自己可以。
	mustFail("acl_denied", "project_docs", as("wf-b", "/code/b", map[string]any{"action": "save", "scope": "project", "path": "modules/chat/design.md", "append": "再补", "expected_rev": 2}))
	mustOK("project_docs", as("wf-a", "/code/a", map[string]any{"action": "save", "scope": "project", "path": "modules/chat/design.md", "append": "A 再写", "expected_rev": 2}))
	// 在别人负责的模块里新建 → 拒绝；在没有负责人的位置新建 → 创建者成为负责人。
	mustFail("acl_denied", "project_docs", as("wf-b", "/code/b", map[string]any{"action": "save", "scope": "project", "path": "modules/chat/b-note.md", "body": "x"}))
	mustOK("project_docs", as("wf-b", "/code/b", map[string]any{"action": "save", "scope": "project", "path": "architecture/b.md", "body": "# 架构"}))
	// 同项目的只读：B 读 A 的文档 OK。
	mustOK("project_docs", as("wf-b", "/code/b", map[string]any{"action": "get", "scope": "project", "path": "modules/chat/design.md"}))

	// 跨项目：X 读 A 工作流的文档 → 拒绝；读任务（会话数据，R4）→ 允许；写任务 → 拒绝；收件箱 → 拒绝。
	mustFail("acl_denied", "project_docs", as("wf-x", "/code/x", map[string]any{"action": "list", "workflow_id": "wf-a"}))
	mustOK("task_board", as("wf-a", "/code/x", map[string]any{"action": "overview"}))
	mustFail("acl_denied", "task_board", as("wf-a", "/code/x", map[string]any{"action": "batch", "ops": []any{map[string]any{"title": "偷偷加任务"}}}))
	mustFail("acl_denied", "workflow_context", as("wf-a", "/code/x", map[string]any{"action": "inbox"}))
	// 自己的工作流照常。
	mustOK("task_board", as("wf-a", "/code/a", map[string]any{"action": "overview"}))

	// ④ 授权：A 对自己负责的模块授权给 B；B 不能转授；越权授权被拒。
	mustFail("identity_required", "project_docs", anon("wf-a", map[string]any{"action": "grant", "scope": "project", "grantee": itoa(idB), "grant_scope": "module", "grant_ref": "chat", "role": "edit"}))
	mustFail("管理权限", "project_docs", as("wf-b", "/code/b", map[string]any{"action": "grant", "scope": "project", "grantee": itoa(idX), "grant_scope": "module", "grant_ref": "chat", "role": "edit"}))
	mustFail("manage", "project_docs", as("wf-a", "/code/a", map[string]any{"action": "grant", "scope": "project", "grantee": itoa(idB), "grant_scope": "module", "grant_ref": "chat", "role": "manage"}))
	out := mustOK("project_docs", as("wf-a", "/code/a", map[string]any{"action": "grant", "scope": "project", "grantee": itoa(idB), "grant_scope": "module", "grant_ref": "chat", "role": "edit", "expires_hours": 24}))
	var granted struct {
		Grant model.ACLGrant `json:"grant"`
	}
	if err := json.Unmarshal([]byte(out), &granted); err != nil || granted.Grant.ID == 0 || granted.Grant.ExpiresAt == nil {
		t.Fatalf("授权结果: %v %s", err, out)
	}
	if n := auditCount(model.AuditGrant, model.AuditAllow); n != 1 {
		t.Fatalf("grant 应入审计: %d", n)
	}
	inbox, _ := st.ListInboxItems(ctx, "wf-b")
	found := false
	for _, it := range inbox {
		found = found || strings.Contains(it.ResponseText, "授权通知")
	}
	if !found {
		t.Fatal("被授权方的收件箱应收到授权通知")
	}
	// B 现在能在 chat 模块里写；授权清单两边都看得到；B 不能撤销别人的授权。
	mustOK("project_docs", as("wf-b", "/code/b", map[string]any{"action": "save", "scope": "project", "path": "modules/chat/b-note.md", "body": "# B 的笔记"}))
	if txt := mustOK("project_docs", as("wf-b", "/code/b", map[string]any{"action": "grants", "scope": "project"})); !strings.Contains(txt, `"count": 1`) && !strings.Contains(txt, `"count":1`) {
		t.Fatalf("B 应看到收到的授权: %s", txt)
	}
	mustFail("acl_denied", "project_docs", as("wf-b", "/code/b", map[string]any{"action": "revoke", "scope": "project", "id": itoa(granted.Grant.ID)}))
	mustOK("project_docs", as("wf-a", "/code/a", map[string]any{"action": "revoke", "scope": "project", "id": itoa(granted.Grant.ID)}))
	mustFail("acl_denied", "project_docs", as("wf-b", "/code/b", map[string]any{"action": "save", "scope": "project", "path": "modules/chat/b-note2.md", "body": "x"}))
	if n := auditCount(model.AuditRevoke, model.AuditAllow); n != 1 {
		t.Fatalf("revoke 应入审计: %d", n)
	}

	// ⑤ 审计里没有正文。
	all, _ := st.ListAuditEvents(ctx, store.AuditFilter{Limit: 500})
	for _, e := range all {
		if strings.Contains(e.Detail+e.Target, "A 写的") || strings.Contains(e.Detail+e.Target, "# 设计") {
			t.Fatalf("审计不得含正文: %+v", e)
		}
	}
}

func timeNow() time.Time              { return time.Now() }
func itoa[T ~uint | ~int](n T) string { return strconv.FormatUint(uint64(n), 10) }

// enforce 档下的 Artifact：同项目身份只读（R2）；评论要 comment、改数据要 edit，且都要负责人授权；跨项目什么都不行。
func TestACLArtifactDataAndComments(t *testing.T) {
	srv := setupTestMCPServer(t)
	ctx := context.Background()
	st := srv.store
	for _, w := range []struct{ id, dir string }{{"wf-a", "/code/a"}, {"wf-b", "/code/b"}, {"wf-x", "/code/x"}} {
		if _, err := st.CreateFeedbackSession(ctx, store.CreateSessionInput{SessionID: "s-" + w.id, WorkflowID: w.id, EnvHostName: "h", ProjectDirectory: w.dir, Title: "t", Summary: "s"}); err != nil {
			t.Fatal(err)
		}
	}
	pp, _ := st.CreateProject(ctx, store.ProjectInput{Name: "P", Slug: "p"})
	_, _ = st.AddProjectDir(ctx, pp.ID, "h", "/code/a", "")
	_, _ = st.AddProjectDir(ctx, pp.ID, "h", "/code/b", "")
	pq, _ := st.CreateProject(ctx, store.ProjectInput{Name: "Q", Slug: "q"})
	_, _ = st.AddProjectDir(ctx, pq.ID, "h", "/code/x", "")
	enf := "enforce"
	for _, id := range []uint{pp.ID, pq.ID} {
		if _, err := st.UpdateProject(ctx, id, store.ProjectPatch{ACLMode: &enf}); err != nil {
			t.Fatal(err)
		}
	}
	as := func(wf, dir string, m map[string]any) map[string]any {
		out := map[string]any{"workflow_id": wf, "project_directory": dir, "host_name": "h", "scope": "project"}
		for k, v := range m {
			out[k] = v
		}
		return out
	}
	ok := func(args map[string]any) string {
		t.Helper()
		txt, isErr := callToolRaw(t, srv, "project_docs", args)
		if isErr {
			t.Fatalf("%v 不该失败: %s", args, txt)
		}
		return txt
	}
	deny := func(args map[string]any) {
		t.Helper()
		txt, isErr := callToolRaw(t, srv, "project_docs", args)
		if !isErr || !strings.Contains(txt, "acl_denied") {
			t.Fatalf("%v 应被拒绝: err=%v %s", args, isErr, txt)
		}
	}
	idB, _ := st.ResolveCallerIdentity(ctx, "wf-b", 0, "h", "/code/b", time.Now())

	ok(as("wf-a", "/code/a", map[string]any{"action": "save", "path": "artifacts/board.md", "body": "<p>b</p>", "render": "html"}))
	ok(as("wf-a", "/code/a", map[string]any{"action": "data_set", "path": "artifacts/board.md", "key": "k", "value": 1}))

	// B：能读、不能写、不能评论。
	ok(as("wf-b", "/code/b", map[string]any{"action": "data_get", "path": "artifacts/board.md", "key": "k"}))
	ok(as("wf-b", "/code/b", map[string]any{"action": "comments", "path": "artifacts/board.md"}))
	deny(as("wf-b", "/code/b", map[string]any{"action": "data_set", "path": "artifacts/board.md", "key": "k", "value": 2}))
	deny(as("wf-b", "/code/b", map[string]any{"action": "comment", "path": "artifacts/board.md", "body": "hi"}))
	// 没有身份断言的写：enforce 要求补身份。
	txt, isErr := callToolRaw(t, srv, "project_docs", map[string]any{"workflow_id": "wf-b", "scope": "project", "action": "data_set", "path": "artifacts/board.md", "key": "k", "value": 3})
	if !isErr || !strings.Contains(txt, "identity_required") {
		t.Fatalf("未断言身份的写应 identity_required: %s", txt)
	}

	// A 授权 B 评论：能评论，但仍不能改数据。
	ok(as("wf-a", "/code/a", map[string]any{"action": "grant", "grantee": itoa(idB), "grant_scope": "doc", "grant_ref": "artifacts/board.md", "role": "comment"}))
	ok(as("wf-b", "/code/b", map[string]any{"action": "comment", "path": "artifacts/board.md", "body": "这里要改"}))
	deny(as("wf-b", "/code/b", map[string]any{"action": "data_set", "path": "artifacts/board.md", "key": "k", "value": 2}))
	// 升到 edit：能改数据，也能解决评论。
	ok(as("wf-a", "/code/a", map[string]any{"action": "grant", "grantee": itoa(idB), "grant_scope": "doc", "grant_ref": "artifacts/board.md", "role": "edit"}))
	ok(as("wf-b", "/code/b", map[string]any{"action": "data_set", "path": "artifacts/board.md", "key": "k", "value": 2, "expected_version": 1}))

	// 跨项目：X 在自己的项目里根本找不到这篇文档（不泄露别的项目里有什么）。
	txt, isErr = callToolRaw(t, srv, "project_docs", as("wf-x", "/code/x", map[string]any{"action": "data_get", "path": "artifacts/board.md", "key": "k"}))
	if !isErr || !strings.Contains(txt, "没有这篇文档") {
		t.Fatalf("跨项目应找不到文档: %s", txt)
	}
}
