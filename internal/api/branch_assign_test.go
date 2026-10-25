package api

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/teacat99/RelayMesh/v2/internal/model"
	"github.com/teacat99/RelayMesh/v2/internal/store"
)

// 主会话给分支派单（#236 P5）的接口层：用户在网页结束分支时，主会话派给它、还没交付的任务退回主会话名下——
// 退回的任务序号随响应返回，任务页收到刷新事件；没有任务可退时响应与升级前一致，也不多广播。
// 权限例外、退回规则本身见 internal/store/branch_assign_test.go，MCP 一侧见 internal/mcp/tools_branch_assign_test.go。

func TestCloseBranch_APIReturnsAssignedTasks(t *testing.T) {
	srv, st := branchAPIFixture(t)
	ctx := context.Background()
	proj, err := st.CreateProject(ctx, store.ProjectInput{Name: "A", Slug: "a"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.AddProjectDir(ctx, proj.ID, "h", "/code/a", ""); err != nil {
		t.Fatal(err)
	}
	board := "p" + strconv.Itoa(int(proj.ID))

	busy, _ := newBranchViaAPI(t, srv, "派了任务的分支", nil)
	quiet, _ := newBranchViaAPI(t, srv, "没派过任务的分支", nil)
	// 网页新建的分支此刻还没有会话（要分支里的 Agent 连上来才有），不在任何项目板上；有了会话、目录在项目里，才能被派单。
	for _, wf := range []string{busy, quiet} {
		if _, err := st.CreateFeedbackSession(ctx, store.CreateSessionInput{SessionID: "s-" + wf, WorkflowID: wf, EnvHostName: "h", ProjectDirectory: "/code/a", Title: "t", Summary: "s"}); err != nil {
			t.Fatal(err)
		}
	}
	main := store.WorkActor{Kind: model.WorkActorAgent, WorkflowID: branchMain, Policy: store.WorkPolicyOff}
	var seqs []int
	for _, title := range []string{"待接取一", "待接取二"} {
		res, err := st.CreateWorkItem(ctx, store.WorkItemInput{WorkflowID: branchMain, Title: title, Assignee: busy}, main)
		if err != nil {
			t.Fatalf("派单应成功（同一张项目板）: %v", err)
		}
		seqs = append(seqs, res.Item.Seq)
	}
	sse := listenSSE(t, srv)

	// 没有任务可退的分支：响应里没有 returned_tasks，也不广播任务事件。
	code, out, raw := ntfyCall(t, srv, http.MethodPost, "/api/v1/workflows/"+quiet+"/branch/close", nil)
	if code != http.StatusOK || out["changed"] != true {
		t.Fatalf("结束应返回 200 且 changed=true: %d %s", code, raw)
	}
	if _, has := out["returned_tasks"]; has {
		t.Fatalf("没有任务可退就不该有 returned_tasks: %s", raw)
	}
	if j := strings.Join(drainSSE(sse), "\n"); strings.Contains(j, "work_item_updated") {
		t.Fatalf("没有任务可退不该广播任务事件:\n%s", j)
	}

	// 有任务：响应带退回的序号（升序），广播一条整板刷新事件。
	code, out, raw = ntfyCall(t, srv, http.MethodPost, "/api/v1/workflows/"+busy+"/branch/close", nil)
	if code != http.StatusOK || out["changed"] != true {
		t.Fatalf("结束应返回 200 且 changed=true: %d %s", code, raw)
	}
	got, _ := out["returned_tasks"].([]any)
	if len(got) != 2 || toIntAPI(got[0]) != seqs[0] || toIntAPI(got[1]) != seqs[1] {
		t.Fatalf("应退回 %v: %s", seqs, raw)
	}
	j := strings.Join(drainSSE(sse), "\n")
	// 事件名要整行匹配（以换行结尾）：只看前缀的话，把事件改名成 work_item_updated_x 也会通过。
	if !strings.Contains(j, "event: work_item_updated\n") || !strings.Contains(j, `"bulk":true`) || !strings.Contains(j, `"board_key":"`+board+`"`) || !strings.Contains(j, `"workflow_id":"`+branchMain+`"`) {
		t.Fatalf("应广播带 board_key 的整板刷新事件:\n%s", j)
	}
	for _, seq := range seqs {
		it, err := st.GetWorkItemBySeq(ctx, branchMain, seq)
		if err != nil || it.AssigneeWorkflowID != "" || it.AssignedByWorkflowID != "" || it.ClaimState != "" || it.State != model.WorkStateTodo {
			t.Fatalf("任务 #%d 应已退回主会话（状态不变）: %+v %v", seq, it, err)
		}
	}

	// 幂等：再结束一次，没有 returned_tasks，也不再广播。
	code, out, raw = ntfyCall(t, srv, http.MethodPost, "/api/v1/workflows/"+busy+"/branch/close", nil)
	if code != http.StatusOK || out["changed"] != false {
		t.Fatalf("重复结束应是 changed=false: %d %s", code, raw)
	}
	if _, has := out["returned_tasks"]; has {
		t.Fatalf("重复结束不该再有 returned_tasks: %s", raw)
	}
	if j := strings.Join(drainSSE(sse), "\n"); strings.Contains(j, "work_item_updated") {
		t.Fatalf("重复结束不该再广播任务事件:\n%s", j)
	}
}
