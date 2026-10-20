package api

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/teacat99/RelayMesh/v2/internal/store"
)

func TestProjectsRESTLifecycle(t *testing.T) {
	srv := newWiredServer(t, nil)
	tok := wiredLogin(t, srv)
	auth := []string{"Authorization", "Bearer " + tok}
	call := func(m, p string, body any) wiredResp { return wiredCall(srv, m, p, body, auth...) }
	ctx := context.Background()

	// 未登录 / MCP 令牌都不能碰项目接口。
	for _, p := range []string{"/api/v1/projects", "/api/v1/audit"} {
		if res := wiredCall(srv, "GET", p, nil); res.Code != http.StatusUnauthorized {
			t.Fatalf("匿名访问 %s 应 401: %d", p, res.Code)
		}
		if res := wiredCall(srv, "GET", p, nil, "Authorization", "Bearer "+wiredMCPToken); res.Code != http.StatusForbidden {
			t.Fatalf("MCP 令牌访问 %s 应 403: %d", p, res.Code)
		}
	}

	// 两个工作流在不同目录，各写一篇文档（老式：只有 workflow_id）。
	for _, wf := range []struct{ id, dir string }{{"wf-a", "/code/proj"}, {"wf-b", "/code/proj-feat"}} {
		if _, err := srv.store.CreateFeedbackSession(ctx, store.CreateSessionInput{SessionID: "s-" + wf.id, WorkflowID: wf.id, EnvHostName: "h", ProjectDirectory: wf.dir, Title: "t", Summary: "s"}); err != nil {
			t.Fatal(err)
		}
	}
	if res := call("POST", "/api/v1/workflows/wf-a/docs", map[string]any{"path": "design/x.md", "body": "# X\n投递规则"}); res.Code != http.StatusCreated {
		t.Fatalf("建文档: %d %s", res.Code, res.Raw)
	}
	if res := call("POST", "/api/v1/workflows/wf-b/docs", map[string]any{"path": "design/x.md", "body": "# X2"}); res.Code != http.StatusCreated {
		t.Fatalf("建文档: %d %s", res.Code, res.Raw)
	}

	// 项目建议：两个目录各自出现，未归属。
	res := call("GET", "/api/v1/projects/suggestions", nil)
	if res.Code != 200 {
		t.Fatalf("suggestions: %d %s", res.Code, res.Raw)
	}
	if items, _ := res.Body["items"].([]any); len(items) != 2 {
		t.Fatalf("应有 2 条建议: %s", res.Raw)
	}

	// 创建项目：重名 slug 冲突、非法档位 400。
	res = call("POST", "/api/v1/projects", map[string]any{
		"name": "主项目", "slug": "main",
		"dirs": []map[string]any{{"host_name": "h", "project_directory": "/code/proj"}, {"host_name": "h", "project_directory": "/code/proj-feat"}},
	})
	if res.Code != http.StatusCreated {
		t.Fatalf("创建项目: %d %s", res.Code, res.Raw)
	}
	pid := int(res.Body["project"].(map[string]any)["id"].(float64))
	base := "/api/v1/projects/" + strconv.Itoa(pid)
	if r := call("POST", "/api/v1/projects", map[string]any{"name": "又一个", "slug": "main"}); r.Code != http.StatusConflict {
		t.Fatalf("slug 冲突应 409: %d %s", r.Code, r.Raw)
	}
	if r := call("PUT", base, map[string]any{"acl_mode": "nope"}); r.Code != http.StatusBadRequest {
		t.Fatalf("非法档位应 400: %d %s", r.Code, r.Raw)
	}
	if r := call("GET", "/api/v1/projects/abc", nil); r.Code != http.StatusBadRequest {
		t.Fatalf("坏编号应 400: %d", r.Code)
	}
	if r := call("GET", "/api/v1/projects/99999", nil); r.Code != http.StatusNotFound {
		t.Fatalf("不存在应 404: %d", r.Code)
	}

	// 详情：两个目录、两个工作流；工作流反查得到项目。
	res = call("GET", base, nil)
	if res.Code != 200 || len(res.Body["dirs"].([]any)) != 2 || len(res.Body["workflows"].([]any)) != 2 {
		t.Fatalf("详情: %d %s", res.Code, res.Raw)
	}
	if r := call("GET", "/api/v1/workflows/wf-a/project", nil); r.Code != 200 || r.Body["project"] == nil {
		t.Fatalf("工作流反查: %s", r.Raw)
	}
	if r := call("GET", "/api/v1/workflows/nobody/project", nil); r.Code != 200 || r.Body["project"] != nil {
		t.Fatalf("无归属应为 null: %s", r.Raw)
	}

	// 模块：先建，再把文档绑进去（带 module_path 保存）。
	if r := call("PUT", base+"/modules", map[string]any{"path": "chat/groups", "title": "群聊"}); r.Code != 200 {
		t.Fatalf("建模块: %d %s", r.Code, r.Raw)
	}
	if r := call("POST", "/api/v1/workflows/wf-a/docs", map[string]any{"path": "modules/chat/groups/index.md", "body": "# 群聊", "module_path": "chat/groups", "render": "md"}); r.Code != http.StatusCreated {
		t.Fatalf("带模块建文档: %d %s", r.Code, r.Raw)
	}
	if r := call("POST", "/api/v1/workflows/wf-a/docs", map[string]any{"path": "modules/zzz/i.md", "body": "x", "module_path": "zzz"}); r.Code != http.StatusBadRequest {
		t.Fatalf("模块不存在应 400: %d %s", r.Code, r.Raw)
	}

	// 迁移：dry-run 看到冲突改名，确认时哈希不对被拒，对了才落地。
	res = call("GET", base+"/migration", nil)
	rep := res.Body["report"].(map[string]any)
	hash, _ := rep["hash"].(string)
	if res.Code != 200 || hash == "" {
		t.Fatalf("dry-run: %d %s", res.Code, res.Raw)
	}
	// items 列出每一篇待迁移的文档（含路径不变的），篇数 = docs；知识库页靠它在迁移完成前继续列出这些文档。
	rawItems, _ := rep["items"].([]any)
	if len(rawItems) == 0 || float64(len(rawItems)) != rep["docs"].(float64) {
		t.Fatalf("items 应列全部待迁移的文档: %s", res.Raw)
	}
	if r := call("POST", base+"/migration", map[string]any{"hash": "stale"}); r.Code != http.StatusConflict {
		t.Fatalf("哈希不符应 409: %d %s", r.Code, r.Raw)
	}
	if r := call("POST", base+"/migration", map[string]any{"hash": hash}); r.Code != 200 {
		t.Fatalf("确认迁移: %d %s", r.Code, r.Raw)
	}
	// 审计留下这次并入项目的全部文档编号：路径不变的文档只有 doc_ids 能找回来（还原 = 把 project_id 置回 0）。
	au := call("GET", "/api/v1/audit?project="+strconv.Itoa(pid)+"&action=project.migrate", nil)
	auItems, _ := au.Body["items"].([]any)
	if au.Code != 200 || len(auItems) != 1 {
		t.Fatalf("迁移审计: %d %s", au.Code, au.Raw)
	}
	var auDetail struct {
		Docs   int    `json:"docs"`
		DocIDs []uint `json:"doc_ids"`
	}
	rawDetail, _ := auItems[0].(map[string]any)["detail"].(string)
	if err := json.Unmarshal([]byte(rawDetail), &auDetail); err != nil || len(auDetail.DocIDs) != len(rawItems) || auDetail.Docs != len(rawItems) {
		t.Fatalf("审计 detail 应带全部 %d 个 doc_ids: %v %s", len(rawItems), err, rawDetail)
	}

	// 项目视角：目录树 + 搜索 + 模块页 + 蓝图。
	res = call("GET", base+"/docs", nil)
	items := res.Body["items"].([]any)
	if res.Code != 200 || len(items) != 3 {
		t.Fatalf("项目文档: %d %s", res.Code, res.Raw)
	}
	if strings.Contains(res.Raw, "投递规则") {
		t.Fatal("列表不应带正文")
	}
	if r := call("GET", base+"/docs/search?q="+"%E6%8A%95%E9%80%92", nil); r.Code != 200 || len(r.Body["items"].([]any)) != 1 {
		t.Fatalf("搜索: %s", r.Raw)
	}
	if r := call("GET", base+"/docs?module=chat", nil); len(r.Body["items"].([]any)) != 1 {
		t.Fatalf("模块过滤: %s", r.Raw)
	}
	// 整个项目打成一个压缩包：3 篇文档 + 索引。
	zr := call("GET", base+"/docs/export.zip", nil)
	if zr.Code != 200 {
		t.Fatalf("项目 zip: %d", zr.Code)
	}
	zf, zerr := zip.NewReader(bytes.NewReader([]byte(zr.Raw)), int64(len(zr.Raw)))
	if zerr != nil || len(zf.File) != 4 {
		t.Fatalf("项目 zip 应有 4 个条目（3 篇 + INDEX）: %v %d", zerr, len(zf.File))
	}
	if r := call("GET", base+"/module-page?path=chat/groups", nil); r.Code != 200 || r.Body["page"] == nil {
		t.Fatalf("模块页: %d %s", r.Code, r.Raw)
	}
	if r := call("GET", base+"/module-page?path=nope", nil); r.Code != http.StatusNotFound {
		t.Fatalf("模块页不存在应 404: %d", r.Code)
	}
	if r := call("GET", base+"/blueprint?templates=1", nil); r.Code != 200 || r.Body["gaps"].(float64) < 1 {
		t.Fatalf("蓝图: %s", r.Raw)
	}

	// 审计：动作都记下了，正文不在里面；CSV 可导出且防公式注入。
	res = call("GET", "/api/v1/audit?project="+strconv.Itoa(pid), nil)
	if res.Code != 200 {
		t.Fatalf("审计: %d %s", res.Code, res.Raw)
	}
	for _, want := range []string{"project.create", "project.dir_add", "module.write", "project.migrate"} {
		if !strings.Contains(res.Raw, want) {
			t.Errorf("审计缺少 %s: %s", want, res.Raw)
		}
	}
	if strings.Contains(res.Raw, "投递规则") {
		t.Fatal("审计不能含正文")
	}
	csvRes := wiredCall(srv, "GET", "/api/v1/audit/export.csv?project="+strconv.Itoa(pid), nil, auth...)
	if csvRes.Code != 200 || !strings.Contains(csvRes.Raw, "project.create") {
		t.Fatalf("csv: %d %s", csvRes.Code, csvRes.Raw)
	}
	if csvSafe("=cmd()") != "'=cmd()" || csvSafe("ok") != "ok" || csvSafe("") != "" {
		t.Fatal("csvSafe")
	}

	// 目录移除、归档。
	if r := call("PUT", base, map[string]any{"acl_mode": "enforce"}); r.Code != 200 {
		t.Fatalf("改档位: %d %s", r.Code, r.Raw)
	}
	if r := call("DELETE", base, nil); r.Code != 200 {
		t.Fatalf("归档: %d", r.Code)
	}
	if r := call("GET", "/api/v1/projects", nil); len(r.Body["items"].([]any)) != 0 {
		t.Fatalf("归档后列表应为空: %s", r.Raw)
	}
}

func TestProjectGrantsREST(t *testing.T) {
	srv := newWiredServer(t, nil)
	tok := wiredLogin(t, srv)
	auth := []string{"Authorization", "Bearer " + tok}
	call := func(m, p string, body any) wiredResp { return wiredCall(srv, m, p, body, auth...) }
	ctx := context.Background()

	if _, err := srv.store.CreateFeedbackSession(ctx, store.CreateSessionInput{SessionID: "s-g", WorkflowID: "wf-g", EnvHostName: "h", ProjectDirectory: "/code/g", Title: "t", Summary: "s"}); err != nil {
		t.Fatal(err)
	}
	idG, _ := srv.store.ResolveCallerIdentity(ctx, "wf-g", 0, "h", "/code/g", time.Now())
	res := call("POST", "/api/v1/projects", map[string]any{"name": "G", "slug": "g"})
	pid := int(res.Body["project"].(map[string]any)["id"].(float64))
	base := "/api/v1/projects/" + strconv.Itoa(pid)
	if r := call("PUT", base+"/modules", map[string]any{"path": "chat", "title": "聊天"}); r.Code != 200 {
		t.Fatal(r.Raw)
	}
	grant := map[string]any{"principal_kind": "identity", "principal_ref": strconv.Itoa(int(idG)), "scope_kind": "module", "scope_ref": "chat", "role": "edit", "expires_hours": 2}
	r := call("POST", base+"/grants", grant)
	if r.Code != http.StatusCreated {
		t.Fatalf("授权: %d %s", r.Code, r.Raw)
	}
	gid := int(r.Body["grant"].(map[string]any)["id"].(float64))
	if r := call("POST", base+"/grants", map[string]any{"principal_kind": "identity", "principal_ref": strconv.Itoa(int(idG)), "scope_kind": "module", "scope_ref": "chat", "role": "manage"}); r.Code != http.StatusBadRequest {
		t.Fatalf("manage 不能授予: %d %s", r.Code, r.Raw)
	}
	if r := call("POST", base+"/grants", map[string]any{"principal_kind": "identity", "principal_ref": "99999", "scope_kind": "project", "role": "read"}); r.Code != http.StatusNotFound {
		t.Fatalf("身份不存在: %d %s", r.Code, r.Raw)
	}
	if r := call("GET", base+"/grants", nil); len(r.Body["items"].([]any)) != 1 {
		t.Fatalf("列表: %s", r.Raw)
	}
	// 被授权方收到提醒。
	inbox, _ := srv.store.ListInboxItems(ctx, "wf-g")
	if len(inbox) != 1 || !strings.Contains(inbox[0].ResponseText, "授权通知") {
		t.Fatalf("应有授权提醒: %+v", inbox)
	}
	if r := call("DELETE", base+"/grants/"+strconv.Itoa(gid), nil); r.Code != 200 {
		t.Fatalf("撤销: %d %s", r.Code, r.Raw)
	}
	if r := call("DELETE", base+"/grants/9999", nil); r.Code != http.StatusNotFound {
		t.Fatalf("不存在: %d", r.Code)
	}
	if r := call("GET", base+"/grants", nil); len(r.Body["items"].([]any)) != 0 {
		t.Fatalf("撤销后有效列表应空: %s", r.Raw)
	}
	if r := call("GET", base+"/grants?all=1", nil); len(r.Body["items"].([]any)) != 1 {
		t.Fatalf("all=1 含已撤销: %s", r.Raw)
	}
	au := call("GET", "/api/v1/audit?project="+strconv.Itoa(pid), nil)
	for _, want := range []string{`"grant"`, `"revoke"`} {
		if !strings.Contains(au.Raw, want) {
			t.Errorf("审计缺 %s: %s", want, au.Raw)
		}
	}
}
