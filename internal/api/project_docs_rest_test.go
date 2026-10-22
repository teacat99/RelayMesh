package api

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/teacat99/RelayMesh/v2/internal/config"
	"github.com/teacat99/RelayMesh/v2/internal/model"
	"github.com/teacat99/RelayMesh/v2/internal/store"
)

func TestGetProjectDocEncodedPath(t *testing.T) {
	gin.SetMode(gin.TestMode)
	st, _ := store.New(":memory:")
	srv := NewServer(&config.Config{}, st, nil)
	_, err := st.CreateProjectDoc(context.Background(), store.ProjectDocInput{
		WorkflowID: "wf1", Path: "design/e2e.md", Body: "# hi",
	}, store.DocActor{Kind: "human"})
	if err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/workflows/wf1/docs/content?path="+url.QueryEscape("design/e2e.md"), nil)
	srv.engine.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("HTTP %d %s", w.Code, w.Body.String())
	}
	var resp map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if resp["body"] != "# hi" {
		t.Fatalf("%v", resp)
	}
}

// 导出 zip 时文件名要再规整一次：库里被脏写进来的带 ../ 的路径不导出；
// 文档自己叫 INDEX.md 时，生成的目录文件改名而不是重名；下载文件名里的特殊字符被替换。
func TestExportProjectDocsZipNamesAreSafe(t *testing.T) {
	gin.SetMode(gin.TestMode)
	st, _ := store.New(":memory:")
	srv := NewServer(&config.Config{}, st, nil)
	ctx := context.Background()
	for _, p := range []string{"design/a.md", "INDEX.md"} {
		if _, err := st.CreateProjectDoc(ctx, store.ProjectDocInput{WorkflowID: "wf1", Path: p, Body: "# " + p}, store.DocActor{Kind: "human"}); err != nil {
			t.Fatal(err)
		}
	}
	// 绕过校验直接写一条带 ../ 的脏数据
	if err := st.DB().Create(&model.ProjectDoc{
		WorkflowID: "wf1", Seq: 99, Path: "../evil.md", Title: "evil", Kind: "note", Status: "draft",
		Body: "x", Rev: 1, Hash: "h", CreatedBy: "human", UpdatedBy: "human",
	}).Error; err != nil {
		t.Fatal(err)
	}

	w := httptest.NewRecorder()
	srv.engine.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/workflows/wf1/docs/export.zip", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("HTTP %d %s", w.Code, w.Body.String())
	}
	zr, err := zip.NewReader(bytes.NewReader(w.Body.Bytes()), int64(w.Body.Len()))
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]int{}
	for _, f := range zr.File {
		names[f.Name]++
		if strings.Contains(f.Name, "..") || strings.HasPrefix(f.Name, "/") {
			t.Fatalf("zip 条目名不安全: %q", f.Name)
		}
	}
	for _, want := range []string{"design/a.md", "INDEX.md", "INDEX.generated.md"} {
		if names[want] != 1 {
			t.Fatalf("zip 里应恰有一个 %q，got %v", want, names)
		}
	}
	if len(names) != 3 {
		t.Fatalf("脏路径不应导出，got %v", names)
	}

	// 下载文件名只含安全字符
	w2 := httptest.NewRecorder()
	srv.engine.ServeHTTP(w2, httptest.NewRequest(http.MethodGet, "/api/v1/workflows/"+url.PathEscape(`we"ird wf`)+"/docs/export.zip", nil))
	if cd := w2.Header().Get("Content-Disposition"); cd != `attachment; filename="we_ird_wf-docs.zip"` {
		t.Fatalf("Content-Disposition 应只含安全字符: %q", cd)
	}
}

func TestGetProjectDocLinksREST(t *testing.T) {
	gin.SetMode(gin.TestMode)
	st, _ := store.New(":memory:")
	srv := NewServer(&config.Config{}, st, nil)
	ctx := context.Background()
	for p, body := range map[string]string{"design/a.md": "见 [[design/b]]", "design/b.md": "# B"} {
		if _, err := st.CreateProjectDoc(ctx, store.ProjectDocInput{WorkflowID: "wf1", Path: p, Body: body}, store.DocActor{Kind: "human"}); err != nil {
			t.Fatal(err)
		}
	}
	get := func(p string) map[string]any {
		w := httptest.NewRecorder()
		srv.engine.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/workflows/wf1/docs/content/links?path="+url.QueryEscape(p), nil))
		if w.Code != http.StatusOK {
			t.Fatalf("HTTP %d %s", w.Code, w.Body.String())
		}
		var resp map[string]any
		_ = json.Unmarshal(w.Body.Bytes(), &resp)
		return resp
	}
	if out, _ := get("design/a.md")["outlinks"].([]any); len(out) != 1 {
		t.Fatalf("a 应有 1 条出链")
	}
	if back, _ := get("design/b.md")["backlinks"].([]any); len(back) != 1 {
		t.Fatalf("b 应有 1 条反链")
	}
	w := httptest.NewRecorder()
	srv.engine.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/workflows/wf1/docs/content/links?path=nope.md", nil))
	if w.Code != http.StatusNotFound {
		t.Fatalf("不存在的文档应 404，得到 %d", w.Code)
	}
}

// 网页「修订历史」里的「还原」走 POST …/docs/content/revisions/:rev/restore?path=…。
// 这条路径形式曾被处理函数漏掉（只认 :doc 路径参数，回 400「缺少文档引用」），点了没有任何效果（#214）。
func TestRestoreProjectDocRevisionByQueryPath(t *testing.T) {
	gin.SetMode(gin.TestMode)
	st, _ := store.New(":memory:")
	srv := NewServer(&config.Config{}, st, nil)
	const docPath = "design/e2e.md"
	if _, err := st.CreateProjectDoc(context.Background(), store.ProjectDocInput{WorkflowID: "wf1", Path: docPath, Body: "# v1"}, store.DocActor{Kind: "human"}); err != nil {
		t.Fatal(err)
	}
	do := func(method, target, body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(method, target, strings.NewReader(body))
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		srv.engine.ServeHTTP(w, req)
		return w
	}
	content := "/api/v1/workflows/wf1/docs/content?path=" + url.QueryEscape(docPath)
	if w := do(http.MethodPut, content, `{"body":"# v2","expected_rev":1}`); w.Code != http.StatusOK {
		t.Fatalf("改成 v2：HTTP %d %s", w.Code, w.Body.String())
	}

	restore := "/api/v1/workflows/wf1/docs/content/revisions/1/restore?path=" + url.QueryEscape(docPath)
	if w := do(http.MethodPost, restore, ""); w.Code != http.StatusOK {
		t.Fatalf("还原 rev 1：HTTP %d %s", w.Code, w.Body.String())
	}
	var got struct {
		Body string `json:"body"`
		Doc  struct {
			Rev int `json:"rev"`
		} `json:"doc"`
	}
	w := do(http.MethodGet, content, "")
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("解析：%v %s", err, w.Body.String())
	}
	// 还原是新修订：正文回到 v1，修订号继续往前走
	if got.Body != "# v1" || got.Doc.Rev != 3 {
		t.Fatalf("还原后应是 v1 / rev 3，得到 %q / rev %d", got.Body, got.Doc.Rev)
	}

	// 不存在的修订、不存在的文档：404，不是 400 / 500
	if w := do(http.MethodPost, "/api/v1/workflows/wf1/docs/content/revisions/99/restore?path="+url.QueryEscape(docPath), ""); w.Code != http.StatusNotFound {
		t.Fatalf("不存在的修订应 404，得到 %d %s", w.Code, w.Body.String())
	}
	if w := do(http.MethodPost, "/api/v1/workflows/wf1/docs/content/revisions/1/restore?path=nope.md", ""); w.Code != http.StatusNotFound {
		t.Fatalf("不存在的文档应 404，得到 %d %s", w.Code, w.Body.String())
	}
	// 没带 path：仍是 400（缺少文档引用），不是 panic
	if w := do(http.MethodPost, "/api/v1/workflows/wf1/docs/content/revisions/1/restore", ""); w.Code != http.StatusBadRequest {
		t.Fatalf("没带 path 应 400，得到 %d %s", w.Code, w.Body.String())
	}
}
