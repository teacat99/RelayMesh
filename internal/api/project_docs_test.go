package api

import (
	"fmt"
	"net/http"
	"net/url"
	"sync"
	"testing"
)

func TestProjectDocUpdateConcurrentExpectedRev(t *testing.T) {
	srv := newWiredServer(t, nil)
	auth := []string{"Authorization", "Bearer " + wiredLogin(t, srv)}
	wf := "wf-doc-rev"

	create := wiredCall(srv, "POST", fmt.Sprintf("/api/v1/workflows/%s/docs", wf), map[string]any{
		"path": "concurrent.md",
		"body": "# v0\n",
	}, auth...)
	if create.Code != http.StatusCreated {
		t.Fatalf("创建文档: %d %s", create.Code, create.Raw)
	}
	doc, _ := create.Body["doc"].(map[string]any)
	if doc == nil {
		t.Fatalf("缺少 doc: %s", create.Raw)
	}
	rev := int(doc["rev"].(float64))
	path := doc["path"].(string)
	putPath := fmt.Sprintf("/api/v1/workflows/%s/docs/content?path=%s", wf, url.QueryEscape(path))

	start := make(chan struct{})
	var wg sync.WaitGroup
	results := make([]wiredResp, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			<-start
			results[idx] = wiredCall(srv, "PUT", putPath, map[string]any{
				"body":         fmt.Sprintf("# concurrent-%d\n", idx),
				"expected_rev": rev,
			}, auth...)
		}(i)
	}
	close(start)
	wg.Wait()

	var ok, conflict int
	for _, r := range results {
		switch r.Code {
		case http.StatusOK:
			ok++
		case http.StatusConflict:
			conflict++
			cur, has := r.Body["current_rev"]
			if !has {
				t.Fatalf("409 应含 current_rev: %s", r.Raw)
			}
			curRev := int(cur.(float64))
			if curRev != rev+1 {
				t.Fatalf("current_rev=%d want %d body=%s", curRev, rev+1, r.Raw)
			}
		default:
			t.Fatalf("意外状态 %d %s", r.Code, r.Raw)
		}
	}
	if ok != 1 || conflict != 1 {
		t.Fatalf("应恰好一个 200 一个 409，got ok=%d conflict=%d", ok, conflict)
	}
}

func TestProjectDocUpdateWithoutExpectedRev(t *testing.T) {
	srv := newWiredServer(t, nil)
	auth := []string{"Authorization", "Bearer " + wiredLogin(t, srv)}
	wf := "wf-doc-no-rev"

	create := wiredCall(srv, "POST", fmt.Sprintf("/api/v1/workflows/%s/docs", wf), map[string]any{
		"path": "no-rev.md",
		"body": "# a\n",
	}, auth...)
	doc, _ := create.Body["doc"].(map[string]any)
	rev := int(doc["rev"].(float64))
	putPath := fmt.Sprintf("/api/v1/workflows/%s/docs/content?path=%s", wf, url.QueryEscape(doc["path"].(string)))

	r1 := wiredCall(srv, "PUT", putPath, map[string]any{"body": "# b\n"}, auth...)
	if r1.Code != http.StatusOK {
		t.Fatalf("无 expected_rev 的第一次更新应 200: %d %s", r1.Code, r1.Raw)
	}
	d1, _ := r1.Body["doc"].(map[string]any)
	if int(d1["rev"].(float64)) != rev+1 {
		t.Fatalf("rev 应递增")
	}

	r2 := wiredCall(srv, "PUT", putPath, map[string]any{"body": "# c\n"}, auth...)
	if r2.Code != http.StatusOK {
		t.Fatalf("无 expected_rev 的第二次更新应 200: %d %s", r2.Code, r2.Raw)
	}
	d2, _ := r2.Body["doc"].(map[string]any)
	if int(d2["rev"].(float64)) != rev+2 {
		t.Fatalf("rev 应再次递增，got %v want %d", d2["rev"], rev+2)
	}
}
