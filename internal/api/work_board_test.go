package api

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/teacat99/RelayMesh/v2/internal/store"
)

// 同一个项目的工作流共用一张任务板（#175 B1a）：REST 请求面仍按 workflow_id，数据按板取；SSE 事件带 board_key。
func TestWorkBoardRESTSharedAcrossWorkflowsOfOneProject(t *testing.T) {
	srv := newWiredServer(t, nil)
	tok := wiredLogin(t, srv)
	auth := []string{"Authorization", "Bearer " + tok}
	call := func(m, p string, body any) wiredResp { return wiredCall(srv, m, p, body, auth...) }
	ctx := context.Background()

	for _, wf := range []struct{ id, dir string }{{"wf-a", "/code/proj"}, {"wf-b", "/code/proj-feat"}, {"wf-out", "/code/elsewhere"}} {
		if _, err := srv.store.CreateFeedbackSession(ctx, store.CreateSessionInput{SessionID: "s-" + wf.id, WorkflowID: wf.id, EnvHostName: "h", ProjectDirectory: wf.dir, Title: "t", Summary: "s"}); err != nil {
			t.Fatal(err)
		}
	}
	res := call("POST", "/api/v1/projects", map[string]any{
		"name": "主项目", "slug": "main",
		"dirs": []map[string]any{{"host_name": "h", "project_directory": "/code/proj"}, {"host_name": "h", "project_directory": "/code/proj-feat"}},
	})
	if res.Code != http.StatusCreated {
		t.Fatalf("创建项目: %d %s", res.Code, res.Raw)
	}
	pid := int(res.Body["project"].(map[string]any)["id"].(float64))
	board := "p" + strconv.Itoa(pid)
	events := watchSSE(t, srv)

	// 两个工作流各建一条：共用一套序号，任务带 board_key，workflow_id 是创建者。
	ia := itemOf(t, call("POST", "/api/v1/work-items", map[string]any{"workflow_id": "wf-a", "title": "甲"}))
	ib := itemOf(t, call("POST", "/api/v1/work-items", map[string]any{"workflow_id": "wf-b", "title": "乙"}))
	if num(ia["seq"]) != 1 || num(ib["seq"]) != 2 || ia["board_key"] != board || ib["board_key"] != board || ib["workflow_id"] != "wf-b" {
		t.Fatalf("应共用项目板序号 #1 #2: %v / %v", ia, ib)
	}
	// SSE：任务事件带 board_key。
	var withBoard int
	for _, m := range events() {
		if strings.HasPrefix(m, "event: work_item_updated\n") && strings.Contains(m, `"board_key":"`+board+`"`) {
			withBoard++
		}
	}
	if withBoard != 2 {
		t.Fatalf("两次创建应各广播一条带 board_key 的任务事件: %d", withBoard)
	}

	// 任一成员的列表都是整张板；计数与清单同口径；响应带 board_key。
	for _, wf := range []string{"wf-a", "wf-b"} {
		res = call("GET", "/api/v1/work-items?workflow_id="+wf, nil)
		items, _ := res.Body["items"].([]any)
		counts, _ := res.Body["counts"].(map[string]any)
		if res.Code != 200 || len(items) != 2 || num(counts["todo"]) != 2 || res.Body["board_key"] != board {
			t.Fatalf("%s 应看到整张项目板: %d %s", wf, res.Code, res.Raw)
		}
	}
	// 清单按最近更新取前 N 条：条数顶到上限时带 truncated，页面据此如实说明「更早的没显示」；没顶到就不带这个字段。
	for _, c := range []struct {
		query string
		want  bool
	}{{"", false}, {"&limit=1", true}, {"&limit=3", false}} {
		res = call("GET", "/api/v1/work-items?workflow_id=wf-a"+c.query, nil)
		if got, _ := res.Body["truncated"].(bool); res.Code != 200 || got != c.want {
			t.Fatalf("limit 参数 %q：truncated 应为 %v，实际 %v（%s）", c.query, c.want, got, res.Raw)
		}
	}
	res = call("GET", "/api/v1/work-items?workflow_id=wf-out", nil)
	if items, _ := res.Body["items"].([]any); len(items) != 0 || res.Body["board_key"] != "w:wf-out" {
		t.Fatalf("项目外的工作流是独立板，看不到项目板的任务: %s", res.Raw)
	}
	// 不指定工作流 = 全部任务；工作流概况里同一张板的成员各占一条，带 board_key 与成员列表。
	res = call("GET", "/api/v1/work-items", nil)
	workflows, _ := res.Body["workflows"].([]any)
	if res.Body["board_key"] != nil || len(workflows) != 2 {
		t.Fatalf("全部任务的列表不带 board_key，概况应有 wf-a / wf-b 两条: %s", res.Raw)
	}
	for _, w := range workflows {
		m := w.(map[string]any)
		members, _ := m["workflows"].([]any)
		if m["board_key"] != board || num(m["total"]) != 2 || len(members) != 2 {
			t.Fatalf("概况应是整张板的数字并带成员: %v", m)
		}
		// 共用的项目板带项目名，界面据此给共享板起名（#175 B1b）。
		if m["board_name"] != "主项目" {
			t.Fatalf("共用的项目板应带项目名「主项目」: %v", m)
		}
	}

	// 规划树也是板的：wf-a 建模块，wf-b 看得到；事件带 board_key。
	events()
	res = call("POST", "/api/v1/work-modules", map[string]any{"workflow_id": "wf-a", "name": "后端"})
	if res.Code != http.StatusCreated {
		t.Fatalf("建模块: %d %s", res.Code, res.Raw)
	}
	if m := res.Body["module"].(map[string]any); m["board_key"] != board || m["workflow_id"] != "wf-a" {
		t.Fatalf("模块属于板、记创建者: %v", m)
	}
	var moduleEvents int
	for _, m := range events() {
		if strings.HasPrefix(m, "event: work_module_updated\n") && strings.Contains(m, `"board_key":"`+board+`"`) {
			moduleEvents++
		}
	}
	if moduleEvents != 1 {
		t.Fatalf("建模块应广播一条带 board_key 的规划事件: %d", moduleEvents)
	}
	res = call("GET", "/api/v1/work-modules?workflow_id=wf-b", nil)
	if mods, _ := res.Body["modules"].([]any); res.Code != 200 || len(mods) != 1 {
		t.Fatalf("wf-b 应看到 wf-a 建的模块: %s", res.Raw)
	}
	// wf-b 把任务归进共享的模块。
	res = call("PUT", "/api/v1/work-items/"+strconv.Itoa(num(ib["id"])), map[string]any{"module_id": num(res.Body["modules"].([]any)[0].(map[string]any)["id"])})
	if res.Code != 200 || itemOf(t, res)["module"] != "后端" {
		t.Fatalf("应能归入共享模块: %d %s", res.Code, res.Raw)
	}
}
