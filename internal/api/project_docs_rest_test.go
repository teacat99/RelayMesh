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
