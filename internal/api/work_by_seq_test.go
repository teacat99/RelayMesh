package api

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/teacat99/RelayMesh/v2/internal/store"
)

// 按「工作流 + 板内序号」取任务（#242）：任务页的 ?seq= 深链靠它把正文里的 #N 解析成任务。
// 序号与数据库编号刻意错开（别的板先垫任务）；同一个项目的工作流共用一套序号；已暂缓的任务照样取得到；
// 缺参数 / 非法序号 400、这张板上没有这个序号 404、未登录 401。
func TestWorkItemBySeqREST(t *testing.T) {
	srv := newWiredServer(t, nil)
	tok := wiredLogin(t, srv)
	auth := []string{"Authorization", "Bearer " + tok}
	call := func(m, p string, body any) wiredResp { return wiredCall(srv, m, p, body, auth...) }
	ctx := context.Background()

	// wf-a / wf-b 在同一个项目里（共用一张板、一套 #N）；wf-pad 是另一张独立板
	for _, wf := range []struct{ id, dir string }{{"wf-a", "/code/proj"}, {"wf-b", "/code/proj-feat"}, {"wf-pad", "/code/elsewhere"}} {
		if _, err := srv.store.CreateFeedbackSession(ctx, store.CreateSessionInput{SessionID: "s-" + wf.id, WorkflowID: wf.id, EnvHostName: "h", ProjectDirectory: wf.dir, Title: "t", Summary: "s"}); err != nil {
			t.Fatal(err)
		}
	}
	if r := call("POST", "/api/v1/projects", map[string]any{
		"name": "主项目", "slug": "main",
		"dirs": []map[string]any{{"host_name": "h", "project_directory": "/code/proj"}, {"host_name": "h", "project_directory": "/code/proj-feat"}},
	}); r.Code != http.StatusCreated {
		t.Fatalf("创建项目: %d %s", r.Code, r.Raw)
	}

	create := func(wf, title string) (id, seq int) {
		t.Helper()
		r := call("POST", "/api/v1/work-items", map[string]any{"workflow_id": wf, "title": title})
		if r.Code != http.StatusCreated {
			t.Fatalf("创建 %s: %d %s", title, r.Code, r.Raw)
		}
		it := itemOf(t, r)
		return num(it["id"]), num(it["seq"])
	}
	// 垫片先占掉数据库编号，之后每个任务都是「编号 ≠ 序号」
	padID1, padSeq1 := create("wf-pad", "垫片一")
	create("wf-pad", "垫片二")
	idA1, seqA1 := create("wf-a", "甲一")
	idA2, seqA2 := create("wf-a", "甲二")
	idB, seqB := create("wf-b", "乙在共享板上建的任务")
	if seqA1 != 1 || seqA2 != 2 || seqB != 3 || padSeq1 != 1 {
		t.Fatalf("板内序号不符（共享板上 wf-b 接着 wf-a 往下数，独立板各自从 1 起）: pad=%d a=%d,%d b=%d", padSeq1, seqA1, seqA2, seqB)
	}
	for name, p := range map[string][2]int{"甲一": {idA1, seqA1}, "甲二": {idA2, seqA2}, "乙": {idB, seqB}} {
		if p[0] == p[1] {
			t.Fatalf("夹具前提不成立：%s 的数据库编号与序号相同（%d），这个测试什么也证明不了", name, p[0])
		}
	}

	get := func(wf string, seq int) wiredResp {
		t.Helper()
		return call("GET", "/api/v1/work-items/by-seq?workflow_id="+wf+"&seq="+strconv.Itoa(seq), nil)
	}
	// want 断言取到的是哪一个任务（数据库编号、序号、标题都对）
	want := func(r wiredResp, id, seq int, title string) {
		t.Helper()
		if r.Code != http.StatusOK {
			t.Fatalf("应 200: %d %s", r.Code, r.Raw)
		}
		it := itemOf(t, r)
		if num(it["id"]) != id || num(it["seq"]) != seq || it["title"] != title {
			t.Fatalf("取到的不是 #%d「%s」（数据库编号 %d）: %s", seq, title, id, r.Raw)
		}
	}

	// ① 序号解析成对应任务，不是「数据库编号等于这个数」的那个任务
	want(get("wf-a", 1), idA1, 1, "甲一")
	want(get("wf-a", 2), idA2, 2, "甲二")
	if it := itemOf(t, get("wf-a", 2)); it["state"] != "todo" {
		t.Fatalf("详情应带完整字段（状态）: %v", it)
	}
	// 数据库编号 1 是 wf-pad 的垫片；wf-pad 上问 #1 取到的也是它（独立板各自从 1 数起），但那是它自己板上的 #1
	want(get("wf-pad", 1), padID1, 1, "垫片一")

	// ② 同一个项目的工作流共用一套 #N：在 wf-a 上问 #3 取到 wf-b 建的任务，在 wf-b 上问 #1 取到 wf-a 建的任务
	want(get("wf-a", 3), idB, 3, "乙在共享板上建的任务")
	want(get("wf-b", 1), idA1, 1, "甲一")
	// 板之间不串：wf-pad 的板上没有 #3（共享板上的 #3 与它无关）
	if r := get("wf-pad", 3); r.Code != http.StatusNotFound {
		t.Fatalf("独立板上没有 #3，应 404（不能取到别的板的任务）: %d %s", r.Code, r.Raw)
	}

	// ③ 已暂缓 / 已结束的任务照样取得到（任务页的默认清单里没有它们，深链不能依赖清单）
	if r := call("PUT", "/api/v1/work-items/"+strconv.Itoa(idA2), map[string]any{"state": "shelved", "progress": "等用户另行安排"}); r.Code != http.StatusOK {
		t.Fatalf("置暂缓: %d %s", r.Code, r.Raw)
	}
	r := get("wf-a", 2)
	want(r, idA2, 2, "甲二")
	if it := itemOf(t, r); it["state"] != "shelved" {
		t.Fatalf("暂缓的任务应照常取到: %v", it)
	}

	// ④ 这张板上没有这个序号：404 并说清楚
	if r := get("wf-a", 99); r.Code != http.StatusNotFound || !strings.Contains(r.Raw, "#99") {
		t.Fatalf("没有这个序号应 404 并写明编号: %d %s", r.Code, r.Raw)
	}
	if r := get("wf-nobody", 1); r.Code != http.StatusNotFound {
		t.Fatalf("没有这个工作流应 404: %d %s", r.Code, r.Raw)
	}

	// ⑤ 缺参数 / 非法序号：400
	for name, path := range map[string]string{
		"缺 workflow_id": "/api/v1/work-items/by-seq?seq=1",
		"缺 seq":         "/api/v1/work-items/by-seq?workflow_id=wf-a",
		"seq 不是数字":      "/api/v1/work-items/by-seq?workflow_id=wf-a&seq=abc",
		"seq 为 0":       "/api/v1/work-items/by-seq?workflow_id=wf-a&seq=0",
		"seq 为负":        "/api/v1/work-items/by-seq?workflow_id=wf-a&seq=-1",
		"seq 是小数":       "/api/v1/work-items/by-seq?workflow_id=wf-a&seq=1.5",
	} {
		if r := call("GET", path, nil); r.Code != http.StatusBadRequest {
			t.Fatalf("%s 应 400: %d %s", name, r.Code, r.Raw)
		}
	}

	// ⑥ 它不能把 /work-items/:id 抢走：按数据库编号取详情的老路照常
	if r := call("GET", "/api/v1/work-items/"+strconv.Itoa(idA1), nil); r.Code != http.StatusOK || num(itemOf(t, r)["seq"]) != 1 {
		t.Fatalf("按数据库编号取详情应照常: %d %s", r.Code, r.Raw)
	}

	// ⑦ 未登录 401
	if r := wiredCall(srv, "GET", "/api/v1/work-items/by-seq?workflow_id=wf-a&seq=1", nil); r.Code != http.StatusUnauthorized {
		t.Fatalf("未登录应 401: %d", r.Code)
	}
}
