package api

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/teacat99/RelayMesh/v2/internal/model"
	"github.com/teacat99/RelayMesh/v2/internal/store"
)

// 人在网页上验收 / 退回协作任务（#180 B3c）：在派发者与承接者共同的群里留一条以「你」为主语的系统行。
// 验收走 PUT 的 claim，退回走「改进需求」（验证中的协作任务被重新打开就是一次退回）。
func TestWorkBoardLinesFromWebHuman(t *testing.T) {
	srv := newWiredServer(t, nil)
	tok := wiredLogin(t, srv)
	auth := []string{"Authorization", "Bearer " + tok}
	call := func(m, p string, body any) wiredResp { return wiredCall(srv, m, p, body, auth...) }
	ctx := context.Background()

	for _, wf := range []struct{ id, dir string }{{"wf-a", "/code/proj"}, {"wf-b", "/code/proj-feat"}} {
		if _, err := srv.store.CreateFeedbackSession(ctx, store.CreateSessionInput{SessionID: "s-" + wf.id, WorkflowID: wf.id, EnvHostName: "h", ProjectDirectory: wf.dir, Title: "t", Summary: "s"}); err != nil {
			t.Fatal(err)
		}
	}
	if res := call("POST", "/api/v1/projects", map[string]any{
		"name": "主项目", "slug": "main",
		"dirs": []map[string]any{{"host_name": "h", "project_directory": "/code/proj"}, {"host_name": "h", "project_directory": "/code/proj-feat"}},
	}); res.Code != http.StatusCreated {
		t.Fatalf("创建项目: %d %s", res.Code, res.Raw)
	}
	g, err := srv.store.CreateGroup(ctx, store.CreateGroupInput{Title: "协作群", Members: []string{"wf-a", "wf-b"}})
	if err != nil {
		t.Fatal(err)
	}
	lines := func() []string {
		rows, err := srv.store.StreamGroupMessages(ctx, g.ID, store.StreamRange{Dir: store.StreamOlder, Limit: 100}, nil, 0, false)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for i := len(rows) - 1; i >= 0; i-- {
			if rows[i].Kind == model.ChatKindSystem && strings.Contains(rows[i].Body, "{{m:") {
				out = append(out, rows[i].Body)
			}
		}
		return out
	}

	mgr := store.WorkActor{Kind: model.WorkActorAgent, WorkflowID: "wf-a", Manager: true, Policy: store.WorkPolicyEnforce}
	dev := store.WorkActor{Kind: model.WorkActorAgent, WorkflowID: "wf-b", Policy: store.WorkPolicyEnforce}
	// 派发者（wf-a）派给 wf-b；wf-b 接取并交付，任务停在验证中等人处理。
	delivered := func(title string) (id uint, seq int) {
		t.Helper()
		created, err := srv.store.CreateWorkItem(ctx, store.WorkItemInput{WorkflowID: "wf-a", Title: title, Assignee: "wf-b"}, mgr)
		if err != nil {
			t.Fatal(err)
		}
		for _, p := range []store.WorkItemPatch{{Claim: "accept"}, {Claim: "deliver", Note: "做好了"}} {
			if _, err := srv.store.UpdateWorkItem(ctx, created.Item.ID, p, dev); err != nil {
				t.Fatal(err)
			}
		}
		return created.Item.ID, created.Item.Seq
	}

	// 1. 人在网页上验收：「你 验收通过了 wf-b 交付的 #N」。（智能体自己的动作由 MCP 入口负责，这里只看网页入口。）
	id1, seq1 := delivered("接口改造")
	if r := call("PUT", workURL(id1), map[string]any{"claim": "accept_delivery", "conclusion": "已验证"}); r.Code != http.StatusOK {
		t.Fatalf("验收: %d %s", r.Code, r.Raw)
	}
	got := lines()
	if len(got) != 1 || got[0] != "{{m:user}} 验收通过了 {{m:wf-b}} 交付的 #"+strconv.Itoa(seq1)+"「接口改造」" {
		t.Fatalf("网页验收应在共同的群里留一条: %q", got)
	}
	// 同一个验收再来一次：任务已经完成，没有变化，不再重复说。
	call("PUT", workURL(id1), map[string]any{"claim": "accept_delivery", "conclusion": "已验证"})
	if got = lines(); len(got) != 1 {
		t.Fatalf("重复验收不该再写: %q", got)
	}

	// 2. 验证中的协作任务被「改进需求」重新打开：就是一次退回。
	id2, seq2 := delivered("文档补全")
	r := call("POST", workURL(id2)+"/improve", map[string]any{"text": "补上英文版"})
	if r.Code != http.StatusOK {
		t.Fatalf("改进需求: %d %s", r.Code, r.Raw)
	}
	got = lines()
	if len(got) != 2 || got[1] != "{{m:user}} 把 #"+strconv.Itoa(seq2)+"「文档补全」退回给 {{m:wf-b}} 返工" {
		t.Fatalf("网页上重新打开验证中的协作任务应写一条退回: %q", got)
	}

	// 3. 人派的任务（没有派发者工作流）、独立任务的改进：不说。
	plain := itemOf(t, call("POST", "/api/v1/work-items", map[string]any{"workflow_id": "wf-a", "title": "人派的", "assignee_workflow_id": "wf-b"}))
	call("PUT", workPath(plain), map[string]any{"claim": "accept"})
	call("PUT", workPath(plain), map[string]any{"claim": "deliver", "note": "好了"})
	call("PUT", workPath(plain), map[string]any{"claim": "accept_delivery", "conclusion": "通过"})
	if got = lines(); len(got) != 2 {
		t.Fatalf("人派的任务没有派发者工作流，不该写系统行: %q", got)
	}
}

func workURL(id uint) string { return fmt.Sprintf("/api/v1/work-items/%d", id) }
