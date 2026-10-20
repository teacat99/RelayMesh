package api

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

// 工作流文档列表默认不带正文（#177）：目录树 / 链接索引 / 选择器从不读正文，却每次都背着全文；?with_body=1 才带，单篇接口一直带。
func TestListProjectDocs_BodyIsOptIn(t *testing.T) {
	srv := newWiredServer(t, nil)
	auth := []string{"Authorization", "Bearer " + wiredLogin(t, srv)}
	wf := "wf-doc-list-body"
	base := fmt.Sprintf("/api/v1/workflows/%s/docs", wf)

	const marker = "只在正文里出现的标记Ωmega"
	create := wiredCall(srv, "POST", base, map[string]any{"path": "design/list-body.md", "title": "列表正文", "body": "# 列表正文\n" + strings.Repeat("填充 ", 100) + marker}, auth...)
	if create.Code != http.StatusCreated {
		t.Fatalf("创建文档: %d %s", create.Code, create.Raw)
	}

	itemsOf := func(r wiredResp) []any {
		t.Helper()
		items, _ := r.Body["items"].([]any)
		if len(items) != 1 {
			t.Fatalf("应有 1 篇文档：%s", r.Raw)
		}
		return items
	}

	// 默认：有路径 / 标题等元数据，没有正文，并显式标注 body_omitted。
	def := wiredCall(srv, "GET", base, nil, auth...)
	if def.Code != http.StatusOK {
		t.Fatalf("列表: %d %s", def.Code, def.Raw)
	}
	it := itemsOf(def)[0].(map[string]any)
	if it["path"] != "design/list-body.md" || it["title"] != "列表正文" {
		t.Fatalf("元数据应齐全：%v", it)
	}
	if b, _ := it["body"].(string); b != "" {
		t.Fatalf("默认列表不该带正文：%d 字节", len(b))
	}
	if strings.Contains(def.Raw, marker) {
		t.Fatalf("默认列表响应里不该出现正文内容")
	}
	if def.Body["body_omitted"] != true {
		t.Fatalf("应标注 body_omitted=true：%s", def.Raw)
	}

	// 带 q= 搜索：服务端读正文来打分，但响应里同样不带正文。
	q := wiredCall(srv, "GET", base+"?q="+url.QueryEscape("Ωmega"), nil, auth...)
	if q.Code != http.StatusOK {
		t.Fatalf("搜索列表: %d %s", q.Code, q.Raw)
	}
	itemsOf(q)
	if strings.Contains(q.Raw, marker) {
		t.Fatalf("带 q 的列表也不该返回正文")
	}

	// ?with_body=1 才带正文（同时 body_omitted=false）。
	for _, v := range []string{"1", "true", "TRUE"} {
		wb := wiredCall(srv, "GET", base+"?with_body="+v, nil, auth...)
		if wb.Code != http.StatusOK {
			t.Fatalf("with_body=%s: %d %s", v, wb.Code, wb.Raw)
		}
		wit := itemsOf(wb)[0].(map[string]any)
		if b, _ := wit["body"].(string); !strings.Contains(b, marker) {
			t.Fatalf("with_body=%s 应带正文", v)
		}
		if wb.Body["body_omitted"] != false {
			t.Fatalf("with_body=%s 时 body_omitted 应为 false：%s", v, wb.Raw)
		}
	}

	// 单篇接口不受影响，仍带正文。
	one := wiredCall(srv, "GET", base+"/content?path="+url.QueryEscape("design/list-body.md"), nil, auth...)
	if one.Code != http.StatusOK || !strings.Contains(one.Raw, marker) {
		t.Fatalf("单篇接口应仍带正文: %d", one.Code)
	}
}
