package api

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/teacat99/RelayMesh/v2/internal/mcp"
	"github.com/teacat99/RelayMesh/v2/internal/model"
)

const wpWF = "wf-prompts-rest"

func wpPath(suffix string) string { return "/api/v1/workflows/" + wpWF + "/prompts" + suffix }

func wpView(t *testing.T, res wiredResp) map[string]any {
	t.Helper()
	v, ok := res.Body["prompts"].(map[string]any)
	if !ok {
		t.Fatalf("响应里没有 prompts: %d %s", res.Code, res.Raw)
	}
	return v
}

// 常驻提示词的写入口只给登录的 Web 用户：匿名 401，MCP 令牌 403。
func TestWorkflowPromptsREST_OnlyForWebUsers(t *testing.T) {
	e := newCfEnv(t)
	routes := []struct {
		method, path string
		body         any
	}{
		{"GET", wpPath(""), nil},
		{"PUT", wpPath(""), map[string]any{"resident": "x"}},
		{"POST", wpPath("/preview"), map[string]any{"resident": "x"}},
		{"POST", wpPath("/restore"), map[string]any{"revision_id": 1}},
	}
	tokens := map[string]string{"环境变量 MCP 令牌": wiredMCPToken, "凭据库令牌": e.cred}
	for _, rt := range routes {
		if res := wiredCall(e.srv, rt.method, rt.path, rt.body); res.Code != http.StatusUnauthorized {
			t.Errorf("匿名 %s %s 应 401，实际 %d", rt.method, rt.path, res.Code)
		}
		for name, tok := range tokens {
			res := wiredCall(e.srv, rt.method, rt.path, rt.body, "Authorization", "Bearer "+tok)
			if res.Code != http.StatusForbidden || res.str("code") != "mcp_token_forbidden" {
				t.Errorf("%s 访问 %s %s 应 403 mcp_token_forbidden，实际 %d %s", name, rt.method, rt.path, res.Code, res.Raw)
			}
		}
	}
	if v := wpView(t, e.call("GET", wpPath(""), nil)); v["resident_rev"] != float64(0) || v["resident"] != "" {
		t.Fatalf("被拒绝的请求什么也没改: %v", v)
	}
}

func TestWorkflowPromptsREST_SaveReadClearAndRestore(t *testing.T) {
	e := newCfEnv(t)

	v := wpView(t, e.call("GET", wpPath(""), nil))
	if v["resident"] != "" || v["resident_rev"] != float64(0) || v["told"] != true || v["max"] != float64(model.ResidentPromptMax) {
		t.Fatalf("初始：没配置、已告知（没有要告诉的）、带上限: %v", v)
	}
	if revs, _ := v["revisions"].([]any); revs == nil || len(revs) != 0 {
		t.Fatalf("初始历史应是空数组（不是 null）: %v", v["revisions"])
	}

	res := e.call("PUT", wpPath(""), map[string]any{"resident": "【规则】\r\n1. 先问再做。  "})
	if res.Code != http.StatusOK || res.Body["changed"] != true {
		t.Fatalf("保存应成功并 changed: %d %s", res.Code, res.Raw)
	}
	v = wpView(t, res)
	if v["resident"] != "【规则】\n1. 先问再做。" || v["resident_rev"] != float64(1) || v["told"] != false || v["updated_by"] == "" {
		t.Fatalf("保存后应整理文字、版本 1、智能体还没见到: %v", v)
	}

	// 内容没变：不涨版本、changed=false。
	res = e.call("PUT", wpPath(""), map[string]any{"resident": "【规则】\n1. 先问再做。"})
	if res.Code != http.StatusOK || res.Body["changed"] != false || wpView(t, res)["resident_rev"] != float64(1) {
		t.Fatalf("没变不该涨版本: %d %s", res.Code, res.Raw)
	}

	// 再改一次，然后回滚到第 1 版。
	e.call("PUT", wpPath(""), map[string]any{"resident": "第二版"})
	v = wpView(t, e.call("GET", wpPath(""), nil))
	revs := v["revisions"].([]any)
	if len(revs) != 2 || revs[0].(map[string]any)["rev"] != float64(2) || revs[1].(map[string]any)["text"] != "【规则】\n1. 先问再做。" {
		t.Fatalf("历史应新的在前: %v", revs)
	}
	firstID := revs[1].(map[string]any)["id"]
	res = e.call("POST", wpPath("/restore"), map[string]any{"revision_id": firstID})
	if res.Code != http.StatusOK || res.Body["changed"] != true {
		t.Fatalf("回滚应成功: %d %s", res.Code, res.Raw)
	}
	v = wpView(t, res)
	if v["resident"] != "【规则】\n1. 先问再做。" || v["resident_rev"] != float64(3) {
		t.Fatalf("回滚应存成第 3 版: %v", v)
	}

	// 清除：空串合法，版本继续涨。
	res = e.call("PUT", wpPath(""), map[string]any{"resident": ""})
	v = wpView(t, res)
	if res.Code != http.StatusOK || v["resident"] != "" || v["resident_rev"] != float64(4) {
		t.Fatalf("清除应成功并涨版本: %d %s", res.Code, res.Raw)
	}
}

// 预览与回执头逐字相同：同一个格式化函数；文字没变是当前版本，变了是下一个版本；不保存任何东西。
func TestWorkflowPromptsREST_PreviewMatchesReceipt(t *testing.T) {
	e := newCfEnv(t)

	// 没配置过：预览新文字，版本是 1；空文字没有这一行。
	res := e.call("POST", wpPath("/preview"), map[string]any{"resident": "  1. 先问再做\r\n2. 不动 main  "})
	if res.Code != http.StatusOK {
		t.Fatalf("预览应 200: %d %s", res.Code, res.Raw)
	}
	want := mcp.ResidentPreview("1. 先问再做\n2. 不动 main", 1)
	if res.str("preview") != want || res.Body["rev"] != float64(1) || res.Body["empty"] != false || res.Body["length"] != float64(len([]rune("1. 先问再做\n2. 不动 main"))) {
		t.Fatalf("预览应是回执头里的样子、版本 1: %s", res.Raw)
	}
	if !strings.HasPrefix(res.str("preview"), "resident: 常驻指令（") || !strings.Contains(res.str("preview"), "\n  1. 先问再做\n  2. 不动 main") {
		t.Fatalf("预览应带固定前缀且正文每行缩进两格: %q", res.str("preview"))
	}
	empty := e.call("POST", wpPath("/preview"), map[string]any{"resident": "   "})
	if empty.Code != http.StatusOK || empty.Body["empty"] != true || empty.str("preview") != "" {
		t.Fatalf("空文字预览应为空: %d %s", empty.Code, empty.Raw)
	}
	// 预览什么也没保存。
	if v := wpView(t, e.call("GET", wpPath(""), nil)); v["resident_rev"] != float64(0) {
		t.Fatalf("预览不该保存: %v", v)
	}

	// 保存之后：文字没变 → 当前版本；改了 → 下一个版本。
	e.call("PUT", wpPath(""), map[string]any{"resident": "第一版"})
	same := e.call("POST", wpPath("/preview"), map[string]any{"resident": "第一版"})
	next := e.call("POST", wpPath("/preview"), map[string]any{"resident": "第二版"})
	if same.Body["rev"] != float64(1) || next.Body["rev"] != float64(2) {
		t.Fatalf("没变是当前版本、改了是下一个版本: same=%s next=%s", same.Raw, next.Raw)
	}

	// 校验：缺字段 400、超长 400 并写明字数。
	if r := e.call("POST", wpPath("/preview"), map[string]any{}); r.Code != http.StatusBadRequest {
		t.Fatalf("缺 resident 应 400: %d %s", r.Code, r.Raw)
	}
	if r := e.call("POST", wpPath("/preview"), map[string]any{"resident": strings.Repeat("长", model.ResidentPromptMax+1)}); r.Code != http.StatusBadRequest || !strings.Contains(r.str("error"), "1201") {
		t.Fatalf("超长应 400 并写明字数: %d %s", r.Code, r.Raw)
	}
}

// 网页新建会话时带 resident：建好就是版本 1，历史里有一条，响应的 applied 写明字数；超长在落库之前 400、不留半个会话。
func TestCreateWorkflow_WithResidentRule(t *testing.T) {
	srv, st := setupTestAPIServer(t)
	ctx := context.Background()

	code, out, raw := createWorkflowCall(t, srv, map[string]any{
		"title": "带规则的会话", "workflow_id": "wf-resident-1", "resident": "  【规则】\r\n只做被分配的事  ",
	})
	if code != http.StatusCreated {
		t.Fatalf("应返回 201: %d %s", code, raw)
	}
	applied, _ := out["applied"].(map[string]any)
	if applied["resident"] != float64(len([]rune("【规则】\n只做被分配的事"))) {
		t.Fatalf("applied 应写明常驻规则的字数: %v", applied)
	}
	if _, ok := out["warnings"]; ok {
		t.Fatalf("成功时不该有 warnings: %s", raw)
	}
	row, err := st.GetWorkflowPrompt(ctx, "wf-resident-1")
	if err != nil || row.Resident != "【规则】\n只做被分配的事" || row.ResidentRev != 1 {
		t.Fatalf("常驻规则应落成版本 1: %+v err=%v", row, err)
	}
	if revs, _ := st.ListPromptRevisions(ctx, "wf-resident-1", model.PromptFieldResident); len(revs) != 1 {
		t.Fatalf("历史里应有一条: %d", len(revs))
	}

	// 不带 resident：不留任何痕迹（版本 0）。
	code, _, raw = createWorkflowCall(t, srv, map[string]any{"title": "没规则", "workflow_id": "wf-resident-2"})
	if code != http.StatusCreated {
		t.Fatalf("应返回 201: %d %s", code, raw)
	}
	if row, _ := st.GetWorkflowPrompt(ctx, "wf-resident-2"); row.ResidentRev != 0 {
		t.Fatalf("没带 resident 不该留下版本: %+v", row)
	}

	// 超长：400，会话没建出来。
	code, _, raw = createWorkflowCall(t, srv, map[string]any{
		"title": "超长规则", "workflow_id": "wf-resident-3", "resident": strings.Repeat("长", model.ResidentPromptMax+1),
	})
	if code != http.StatusBadRequest || !strings.Contains(raw, "1201") {
		t.Fatalf("超长应 400 并写明字数: %d %s", code, raw)
	}
	code, _, _ = createWorkflowCall(t, srv, map[string]any{"title": "同编号可再建", "workflow_id": "wf-resident-3"})
	if code != http.StatusCreated {
		t.Fatalf("被拒绝的请求不该留下半个会话（编号应还没被占用）: %d", code)
	}
}

func TestWorkflowPromptsREST_Validation(t *testing.T) {
	e := newCfEnv(t)
	if res := e.call("PUT", wpPath(""), map[string]any{}); res.Code != http.StatusBadRequest {
		t.Fatalf("缺 resident 应 400: %d %s", res.Code, res.Raw)
	}
	res := e.call("PUT", wpPath(""), map[string]any{"resident": strings.Repeat("长", model.ResidentPromptMax+1)})
	if res.Code != http.StatusBadRequest || !strings.Contains(res.str("error"), "1201") {
		t.Fatalf("超长应 400 并写明字数: %d %s", res.Code, res.Raw)
	}
	if res := e.call("POST", wpPath("/restore"), map[string]any{}); res.Code != http.StatusBadRequest {
		t.Fatalf("缺 revision_id 应 400: %d %s", res.Code, res.Raw)
	}
	if res := e.call("POST", wpPath("/restore"), map[string]any{"revision_id": 9999}); res.Code != http.StatusNotFound {
		t.Fatalf("不存在的版本应 404: %d %s", res.Code, res.Raw)
	}
	// 被拒绝的请求没有留下版本。
	if v := wpView(t, e.call("GET", wpPath(""), nil)); v["resident_rev"] != float64(0) {
		t.Fatalf("被拒的请求不该产生版本: %v", v)
	}
}
