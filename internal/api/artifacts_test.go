package api

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/teacat99/RelayMesh/v2/internal/store"
)

func TestArtifactRenderTicketAndHeaders(t *testing.T) {
	srv := newWiredServer(t, nil)
	tok := wiredLogin(t, srv)
	auth := []string{"Authorization", "Bearer " + tok}
	call := func(m, p string, body any) wiredResp { return wiredCall(srv, m, p, body, auth...) }

	if _, err := srv.store.CreateFeedbackSession(t.Context(), store.CreateSessionInput{SessionID: "s-art", WorkflowID: "wf-art", EnvHostName: "h", ProjectDirectory: "/code/a", Title: "t", Summary: "s"}); err != nil {
		t.Fatal(err)
	}
	res := call("POST", "/api/v1/workflows/wf-art/docs", map[string]any{"path": "artifacts/board.md", "body": "<!doctype html><html><head><title>t</title></head><body><script>1</script></body></html>", "render": "html"})
	if res.Code != http.StatusCreated {
		t.Fatalf("建 HTML 文档: %d %s", res.Code, res.Raw)
	}
	id := int(res.Body["doc"].(map[string]any)["id"].(float64))
	idStr := strconv.Itoa(id)
	mdRes := call("POST", "/api/v1/workflows/wf-art/docs", map[string]any{"path": "notes/plain.md", "body": "# x"})
	mdID := strconv.Itoa(int(mdRes.Body["doc"].(map[string]any)["id"].(float64)))

	// 匿名拿不到票据，也读不到渲染页。
	if r := wiredCall(srv, "POST", "/api/v1/artifacts/"+idStr+"/ticket", nil); r.Code != http.StatusUnauthorized {
		t.Fatalf("匿名要票据应 401: %d", r.Code)
	}
	if r := wiredCall(srv, "GET", "/api/v1/artifacts/"+idStr+"/render", nil); r.Code != http.StatusUnauthorized {
		t.Fatalf("无票据读渲染页应 401: %d", r.Code)
	}
	// 只有 HTML Artifact 才发票据。
	if r := call("POST", "/api/v1/artifacts/"+mdID+"/ticket", nil); r.Code != http.StatusBadRequest {
		t.Fatalf("markdown 文档不发票据: %d %s", r.Code, r.Raw)
	}
	tk := call("POST", "/api/v1/artifacts/"+idStr+"/ticket", nil)
	url := tk.str("url")
	if tk.Code != 200 || !strings.Contains(url, "?ticket=") {
		t.Fatalf("发票据: %d %s", tk.Code, tk.Raw)
	}

	get := func(u string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		srv.Engine().ServeHTTP(w, httptest.NewRequest("GET", u, nil))
		return w
	}
	w := get(url)
	if w.Code != 200 {
		t.Fatalf("带票据读渲染页: %d %s", w.Code, w.Body.String())
	}
	csp := w.Header().Get("Content-Security-Policy")
	for _, want := range []string{"sandbox allow-scripts", "default-src 'none'", "connect-src 'none'", "frame-ancestors 'self'"} {
		if !strings.Contains(csp, want) {
			t.Fatalf("CSP 缺 %q: %s", want, csp)
		}
	}
	if strings.Contains(csp, "allow-same-origin") {
		t.Fatal("沙箱绝不能带 allow-same-origin")
	}
	if w.Header().Get("Cache-Control") != "no-store" || !strings.HasPrefix(w.Header().Get("Content-Type"), "text/html") {
		t.Fatalf("响应头不对: %v", w.Header())
	}
	body := w.Body.String()
	if !strings.HasPrefix(body, "<!doctype html>") || !strings.Contains(body, "window.relaymesh") {
		t.Fatalf("桥脚本应注入在 <head> 之后且 doctype 仍在最前: %.120s", body)
	}
	if strings.Index(body, "window.relaymesh") > strings.Index(body, "<title>") {
		t.Fatal("桥脚本应在 <head> 一开始就加载")
	}
	// 票据单次有效；绑定别的文档也不行。
	if w2 := get(url); w2.Code != http.StatusUnauthorized {
		t.Fatalf("票据重放应 401: %d", w2.Code)
	}
	tk2 := call("POST", "/api/v1/artifacts/"+idStr+"/ticket", nil)
	other := strings.Replace(tk2.str("url"), "/artifacts/"+idStr+"/", "/artifacts/"+mdID+"/", 1)
	if w3 := get(other); w3.Code != http.StatusUnauthorized {
		t.Fatalf("票据换文档应 401: %d", w3.Code)
	}
}

func TestInjectArtifactBridge(t *testing.T) {
	cases := map[string]string{
		"<!doctype html><html lang=en><head><meta></head><body></body></html>": "<head>",
		"<html><body>x</body></html>":                                          "<html>",
		"<body class=a>x</body>":                                               "",
		"<p>片段</p>":                                                            "",
	}
	for in := range cases {
		out := injectArtifactBridge(in)
		if !strings.Contains(out, "window.relaymesh") || !strings.Contains(out, strings.TrimSpace(in[:5])) {
			t.Fatalf("注入失败: %q -> %.80s", in, out)
		}
	}
	if out := injectArtifactBridge("<!doctype html><html><head></head></html>"); !strings.HasPrefix(out, "<!doctype html>") {
		t.Fatal("doctype 必须仍在最前，否则页面进入怪异模式")
	}
	if out := injectArtifactBridge("<body>x</body>"); strings.Index(out, "<script>") > strings.Index(out, "<body>") {
		t.Fatal("没有 head 时脚本放在 body 之前")
	}
}

func TestArtifactDataAndCommentsREST(t *testing.T) {
	srv := newWiredServer(t, nil)
	tok := wiredLogin(t, srv)
	auth := []string{"Authorization", "Bearer " + tok}
	call := func(m, p string, body any) wiredResp { return wiredCall(srv, m, p, body, auth...) }
	if _, err := srv.store.CreateFeedbackSession(t.Context(), store.CreateSessionInput{SessionID: "s-d", WorkflowID: "wf-d", EnvHostName: "h", ProjectDirectory: "/code/d", Title: "t", Summary: "s"}); err != nil {
		t.Fatal(err)
	}
	res := call("POST", "/api/v1/workflows/wf-d/docs", map[string]any{"path": "artifacts/k.md", "body": "<p>k</p>", "render": "html"})
	id := strconv.Itoa(int(res.Body["doc"].(map[string]any)["id"].(float64)))
	base := "/api/v1/artifacts/" + id

	if r := wiredCall(srv, "GET", base+"/data", nil); r.Code != http.StatusUnauthorized {
		t.Fatalf("匿名读数据应 401: %d", r.Code)
	}
	if r := call("PUT", base+"/data/board", map[string]any{"value": map[string]any{"cols": []string{"todo"}}, "expected_version": 0}); r.Code != 200 {
		t.Fatalf("写数据: %d %s", r.Code, r.Raw)
	}
	if r := call("PUT", base+"/data/board", map[string]any{"value": 1, "expected_version": 0}); r.Code != http.StatusConflict {
		t.Fatalf("版本冲突应 409: %d %s", r.Code, r.Raw)
	}
	if r := call("PUT", base+"/data/bad%20key", map[string]any{"value": 1}); r.Code != http.StatusBadRequest {
		t.Fatalf("坏键名应 400: %d %s", r.Code, r.Raw)
	}
	if r := call("PUT", base+"/data/n", map[string]any{"value": nil}); r.Code != 200 {
		t.Fatalf("null 是合法值: %d %s", r.Code, r.Raw)
	}
	if r := call("GET", base+"/data", nil); r.Code != 200 || len(r.Body["items"].([]any)) != 2 {
		t.Fatalf("列数据: %d %s", r.Code, r.Raw)
	}
	if r := call("DELETE", base+"/data/n", nil); r.Code != 200 || r.Body["deleted"] != true {
		t.Fatalf("删数据: %d %s", r.Code, r.Raw)
	}
	// 审计里记了写入，且不含值。
	ev, _ := srv.store.ListAuditEvents(t.Context(), store.AuditFilter{Action: "artifact.data_write", Limit: 10})
	if len(ev) < 2 {
		t.Fatalf("应有数据写入审计: %d", len(ev))
	}
	for _, e := range ev {
		if strings.Contains(string(e.Detail), "cols") {
			t.Fatalf("审计不应含数据内容: %s", e.Detail)
		}
	}

	// 评论：新增 → 列出 → 解决；同时给文档所属工作流排了一条追加消息。
	r := call("POST", base+"/comments", map[string]any{"anchor": "card-2", "text": "这张卡片要拆分"})
	if r.Code != 200 {
		t.Fatalf("评论: %d %s", r.Code, r.Raw)
	}
	cid := strconv.Itoa(int(r.Body["item"].(map[string]any)["id"].(float64)))
	if r := call("POST", base+"/comments", map[string]any{"text": " "}); r.Code != http.StatusBadRequest {
		t.Fatalf("空评论应 400: %d", r.Code)
	}
	if r := call("GET", base+"/comments", nil); len(r.Body["items"].([]any)) != 1 {
		t.Fatalf("列评论: %s", r.Raw)
	}
	if r := call("POST", base+"/comments/"+cid+"/resolve", map[string]any{"resolved": true}); r.Code != 200 {
		t.Fatalf("解决: %d %s", r.Code, r.Raw)
	}
	if r := call("GET", base+"/comments", nil); len(r.Body["items"].([]any)) != 0 {
		t.Fatalf("默认不列已解决: %s", r.Raw)
	}
	if r := call("GET", base+"/comments?resolved=1", nil); len(r.Body["items"].([]any)) != 1 {
		t.Fatalf("含已解决: %s", r.Raw)
	}
	sum := call("GET", base, nil)
	if sum.Code != 200 || sum.Body["render"] != "html" || sum.Body["title"] == "" {
		t.Fatalf("摘要: %d %s", sum.Code, sum.Raw)
	}
	if _, has := sum.Body["body"]; has {
		t.Fatal("HTML Artifact 的摘要不返回正文（要走沙箱渲染页）")
	}
	if r := call("GET", "/api/v1/artifacts/99999/data", nil); r.Code != http.StatusNotFound {
		t.Fatalf("不存在的文档应 404: %d", r.Code)
	}
}
