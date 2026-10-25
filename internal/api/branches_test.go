package api

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/teacat99/RelayMesh/v2/internal/model"
	"github.com/teacat99/RelayMesh/v2/internal/store"
)

// 分支会话（#236 P1）的接口层：新建分支（POST /workflows 带 parent_workflow_id）、关系列表、会话页分支流、改开放范围、结束分支、
// 用户发言抄送主会话。store 层的不变量与限额见 internal/store/branches_test.go，这里只验接口：状态码、请求 / 响应形状、事件广播、抄送钩子。

const branchMain = "wf-main"

func branchAPIFixture(t *testing.T) (*Server, *store.Store) {
	t.Helper()
	srv, st := setupTestAPIServer(t)
	ctx := context.Background()
	for _, wf := range []string{branchMain, "wf-solo"} {
		if _, err := st.CreateFeedbackSession(ctx, store.CreateSessionInput{
			SessionID: "s-" + wf, WorkflowID: wf, EnvHostName: "h", ProjectDirectory: "/code/a", Title: "标题-" + wf, Summary: "s",
		}); err != nil {
			t.Fatal(err)
		}
	}
	return srv, st
}

// newBranchViaAPI 经 POST /workflows 建一个分支，返回它的 workflow_id 与完整响应。
func newBranchViaAPI(t *testing.T, srv *Server, title string, extra map[string]any) (string, map[string]any) {
	t.Helper()
	body := map[string]any{"title": title, "parent_workflow_id": branchMain}
	for k, v := range extra {
		body[k] = v
	}
	code, out, raw := createWorkflowCall(t, srv, body)
	if code != http.StatusCreated {
		t.Fatalf("建分支应返回 201: %d %s", code, raw)
	}
	wf, _ := out["workflow_id"].(string)
	if wf == "" {
		t.Fatalf("响应里没有 workflow_id: %s", raw)
	}
	return wf, out
}

func pendingTexts(t *testing.T, st *store.Store, wf string) []model.QueuedFeedback {
	t.Helper()
	rows, err := st.ListPendingQueuedFeedbacks(context.Background(), wf)
	if err != nil {
		t.Fatal(err)
	}
	return rows
}

func TestCreateWorkflow_AsBranch(t *testing.T) {
	srv, st := branchAPIFixture(t)
	ctx := context.Background()
	sse := listenSSE(t, srv)

	// 只填名字：响应里没有 branch 字段（与建分支之前逐字节兼容）。
	code, plain, raw := createWorkflowCall(t, srv, map[string]any{"title": "普通会话"})
	if code != http.StatusCreated {
		t.Fatalf("普通创建应返回 201: %d %s", code, raw)
	}
	if _, ok := plain["branch"]; ok {
		t.Fatalf("没带 parent_workflow_id 时响应里不该有 branch: %s", raw)
	}

	// 带 parent_workflow_id：建成分支，默认协作预设，抄送开。
	bwf, out := newBranchViaAPI(t, srv, "查缓存方案", nil)
	br, _ := out["branch"].(map[string]any)
	if br["parent_workflow_id"] != branchMain || br["branch_workflow_id"] != bwf || br["status"] != "active" || br["created_by"] != "human" {
		t.Fatalf("branch 字段不对: %v", br)
	}
	if br["title"] != "查缓存方案" || br["parent_title"] != "标题-"+branchMain || br["copy_user_notes"] != true {
		t.Fatalf("branch 概况不对: %v", br)
	}
	if ex, _ := br["expose"].(map[string]any); ex["history"] != "full" || ex["say"] != true {
		t.Fatalf("默认应是协作预设: %v", br["expose"])
	}
	joined := strings.Join(drainSSE(sse), "\n")
	if !strings.Contains(joined, "branch_changed") || !strings.Contains(joined, `"change":"created"`) {
		t.Fatalf("应广播 branch_changed(created):\n%s", joined)
	}
	// 用户建的分支：主会话收件箱里有一条系统通知（分支事务通知）。
	rows := pendingTexts(t, st, branchMain)
	if len(rows) != 1 || rows[0].Kind != model.InboxKindBranch || !strings.Contains(rows[0].ResponseText, "查缓存方案") {
		t.Fatalf("主会话收件箱里应有一条建分支通知: %+v", rows)
	}
	if a, _ := model.ParseMsgActor(rows[0].Actor); a.Kind != model.ActorKindSystem || a.Workflow != bwf {
		t.Fatalf("通知的发言者应是系统（附分支编号）: %q", rows[0].Actor)
	}

	// 指定预设与抄送开关。
	v, out2 := newBranchViaAPI(t, srv, "只读分支", map[string]any{"expose_preset": "visible", "copy_user_notes": false})
	br2, _ := out2["branch"].(map[string]any)
	if ex, _ := br2["expose"].(map[string]any); ex["history"] != "summary" || ex["say"] != false || br2["copy_user_notes"] != false {
		t.Fatalf("visible 预设 + 不抄送没生效: %v", br2)
	}
	if rel, err := st.BranchOf(ctx, v); err != nil || rel == nil || rel.Expose.PresetName() != "visible" || rel.CopyUserNotes {
		t.Fatalf("库里的关系不对: %+v err=%v", rel, err)
	}
}

func TestCreateWorkflow_AsBranchErrors(t *testing.T) {
	srv, st := branchAPIFixture(t)
	ctx := context.Background()

	// 没带 parent_workflow_id 却带了预设 / 抄送：400，不悄悄忽略。
	for _, body := range []map[string]any{
		{"title": "x", "expose_preset": "collab"},
		{"title": "x", "copy_user_notes": true},
	} {
		if code, _, raw := createWorkflowCall(t, srv, body); code != http.StatusBadRequest || !strings.Contains(raw, "parent_workflow_id") {
			t.Fatalf("缺 parent_workflow_id 应 400 并点明: %d %s", code, raw)
		}
	}
	// 主会话不存在：404，且不留下半个会话（指定了编号，事后应查不到）。
	if code, _, raw := createWorkflowCall(t, srv, map[string]any{"title": "孤儿", "workflow_id": "wf-orphan", "parent_workflow_id": "wf-nobody"}); code != http.StatusNotFound {
		t.Fatalf("主会话不存在应 404: %d %s", code, raw)
	}
	if ss, _ := st.GetSessionsByWorkflow(ctx, "wf-orphan", 5); len(ss) != 0 {
		t.Fatalf("建分支失败不应留下占位会话: %+v", ss)
	}
	// 预设写错：400，同样不留会话。
	if code, _, raw := createWorkflowCall(t, srv, map[string]any{"title": "坏预设", "workflow_id": "wf-badpreset", "parent_workflow_id": branchMain, "expose_preset": "wild"}); code != http.StatusBadRequest {
		t.Fatalf("预设写错应 400: %d %s", code, raw)
	}
	if ss, _ := st.GetSessionsByWorkflow(ctx, "wf-badpreset", 5); len(ss) != 0 {
		t.Fatalf("预设写错不应留下占位会话: %+v", ss)
	}
	// 主会话本身是分支：只允许一层。
	bwf, _ := newBranchViaAPI(t, srv, "第一层", nil)
	if code, _, raw := createWorkflowCall(t, srv, map[string]any{"title": "第二层", "parent_workflow_id": bwf}); code != http.StatusBadRequest || !strings.Contains(raw, "只允许一层") {
		t.Fatalf("分支不能再建分支: %d %s", code, raw)
	}
}

func TestBranchEndpoints_ListFeedUpdateClose(t *testing.T) {
	srv, st := branchAPIFixture(t)
	sse := listenSSE(t, srv)

	// 还没有任何关系：列表是空数组（不是 null）。
	code, out, raw := ntfyCall(t, srv, http.MethodGet, "/api/v1/branches", nil)
	if bs, ok := out["branches"].([]any); code != http.StatusOK || !ok || len(bs) != 0 {
		t.Fatalf("没有分支时应返回空数组: %d %s", code, raw)
	}

	bwf, _ := newBranchViaAPI(t, srv, "查缓存方案", nil)
	_, out, _ = ntfyCall(t, srv, http.MethodGet, "/api/v1/branches", nil)
	list, _ := out["branches"].([]any)
	if len(list) != 1 {
		t.Fatalf("应有 1 条关系: %v", out)
	}
	if first, _ := list[0].(map[string]any); first["branch_workflow_id"] != bwf || first["title"] != "查缓存方案" {
		t.Fatalf("列表条目不对: %v", first)
	}

	// 会话页分支流：主会话 / 分支 / 无关三种角色。
	feedOf := func(wf string) map[string]any {
		t.Helper()
		code, out, raw := ntfyCall(t, srv, http.MethodGet, "/api/v1/workflows/"+wf+"/branch-feed", nil)
		if code != http.StatusOK {
			t.Fatalf("branch-feed 应返回 200: %d %s", code, raw)
		}
		f, _ := out["branch_feed"].(map[string]any)
		if f == nil {
			t.Fatalf("响应里没有 branch_feed: %s", raw)
		}
		return f
	}
	if f := feedOf(branchMain); f["role"] != "main" || len(f["branches"].([]any)) != 1 {
		t.Fatalf("主会话的分支流不对: %v", f)
	}
	if f := feedOf(bwf); f["role"] != "branch" || f["relation"] == nil || f["parent_title"] != "标题-"+branchMain {
		t.Fatalf("分支的分支流不对: %v", f)
	}
	none := feedOf("wf-solo")
	if items, ok := none["items"].([]any); none["role"] != "none" || !ok || len(items) != 0 {
		t.Fatalf("无关会话 role=none 且 items 是空数组: %v", none)
	}
	if code, _, _ := ntfyCall(t, srv, http.MethodGet, "/api/v1/workflows/"+bwf+"/branch-feed?limit=1", nil); code != http.StatusOK {
		t.Fatalf("带 limit 也应成功: %d", code)
	}

	// 改开放范围：只读预设；响应带最新概况；广播 settings。
	drainSSE(sse)
	code, out, raw = ntfyCall(t, srv, http.MethodPut, "/api/v1/workflows/"+bwf+"/branch", map[string]any{"expose_preset": "visible"})
	if code != http.StatusOK {
		t.Fatalf("改预设应返回 200: %d %s", code, raw)
	}
	if ex, _ := out["branch"].(map[string]any)["expose"].(map[string]any); ex["history"] != "summary" || ex["say"] != false {
		t.Fatalf("预设没生效: %s", raw)
	}
	if j := strings.Join(drainSSE(sse), "\n"); !strings.Contains(j, `"change":"settings"`) {
		t.Fatalf("应广播 branch_changed(settings):\n%s", j)
	}
	// 显式取值 + 抄送开关。
	code, out, raw = ntfyCall(t, srv, http.MethodPut, "/api/v1/workflows/"+bwf+"/branch", map[string]any{
		"expose": map[string]any{"history": "full", "decide": false, "say": true, "params": "off", "launch": false, "tasks": "read"}, "copy_user_notes": false,
	})
	if code != http.StatusOK {
		t.Fatalf("显式取值应返回 200: %d %s", code, raw)
	}
	if br, _ := out["branch"].(map[string]any); br["copy_user_notes"] != false {
		t.Fatalf("抄送开关没生效: %s", raw)
	} else if ex, _ := br["expose"].(map[string]any); ex["preset"] != nil && ex["tasks"] != "read" || ex["history"] != "full" || ex["decide"] != false {
		t.Fatalf("显式取值没生效: %s", raw)
	}
	// 错误请求。
	for name, body := range map[string]map[string]any{
		"空请求":        {},
		"预设写错":       {"expose_preset": "wild"},
		"预设与取值同时给":   {"expose_preset": "visible", "expose": map[string]any{"history": "off"}},
		"history 非法": {"expose": map[string]any{"history": "everything", "params": "off", "tasks": "read"}},
		"取值不完整":      {"expose": map[string]any{"history": "full"}},
	} {
		if code, _, raw := ntfyCall(t, srv, http.MethodPut, "/api/v1/workflows/"+bwf+"/branch", body); code != http.StatusBadRequest {
			t.Fatalf("%s 应 400: %d %s", name, code, raw)
		}
	}
	if code, _, raw := ntfyCall(t, srv, http.MethodPut, "/api/v1/workflows/wf-solo/branch", map[string]any{"expose_preset": "visible"}); code != http.StatusNotFound {
		t.Fatalf("不是分支的工作流应 404: %d %s", code, raw)
	}

	// 结束分支：两边各收到系统通知；幂等；之后抄送停止。
	if n := len(pendingTexts(t, st, bwf)); n != 0 {
		t.Fatalf("结束前分支收件箱应为空，得到 %d", n)
	}
	before := len(pendingTexts(t, st, branchMain)) // 建分支通知
	drainSSE(sse)
	code, out, raw = ntfyCall(t, srv, http.MethodPost, "/api/v1/workflows/"+bwf+"/branch/close", nil)
	if code != http.StatusOK || out["changed"] != true {
		t.Fatalf("结束应返回 200 且 changed=true: %d %s", code, raw)
	}
	if br, _ := out["branch"].(map[string]any); br["status"] != "closed" {
		t.Fatalf("响应里的关系应是 closed: %s", raw)
	}
	if j := strings.Join(drainSSE(sse), "\n"); !strings.Contains(j, `"change":"closed"`) {
		t.Fatalf("应广播 branch_changed(closed):\n%s", j)
	}
	if n := len(pendingTexts(t, st, bwf)); n != 1 {
		t.Fatalf("分支收件箱应有 1 条收尾通知，得到 %d", n)
	}
	if n := len(pendingTexts(t, st, branchMain)); n != before+1 {
		t.Fatalf("用户结束时主会话也应收到 1 条通知（%d → %d）", before, n)
	}
	code, out, raw = ntfyCall(t, srv, http.MethodPost, "/api/v1/workflows/"+bwf+"/branch/close", nil)
	if code != http.StatusOK || out["changed"] != false {
		t.Fatalf("重复结束应是 changed=false: %d %s", code, raw)
	}
	if n := len(pendingTexts(t, st, bwf)); n != 1 {
		t.Fatalf("重复结束不应再写通知，得到 %d", n)
	}
	if code, _, raw := ntfyCall(t, srv, http.MethodPost, "/api/v1/workflows/wf-solo/branch/close", nil); code != http.StatusNotFound {
		t.Fatalf("不是分支的工作流 close 应 404: %d %s", code, raw)
	}
}

// 用户在分支会话里说的话抄送给主会话：三条送达路径（追加排队 / 回复待处理会话 / 追加并入已完成未消费的会话）都走；
// 关掉抄送 / 结束分支 / 不是分支时什么都不做。
func TestBranchUserNoteCopiedToMain(t *testing.T) {
	srv, st := branchAPIFixture(t)
	ctx := context.Background()
	bwf, _ := newBranchViaAPI(t, srv, "查缓存方案", nil)
	bwf2, _ := newBranchViaAPI(t, srv, "提交路径", nil)
	sse := listenSSE(t, srv)

	// ccRows 是主会话收件箱里「用户在 branch 里说」的抄送行。
	ccRows := func(branch string) []model.QueuedFeedback {
		var out []model.QueuedFeedback
		for _, r := range pendingTexts(t, st, branchMain) {
			if a, _ := model.ParseMsgActor(r.Actor); r.Kind == model.InboxKindBranch && a.Kind == model.ActorKindHuman && r.SenderRef == branch {
				out = append(out, r)
			}
		}
		return out
	}

	// 路径一：没有待处理会话，用户的追加排进分支的收件箱。
	appendText(t, srv, bwf, "先别动数据库，只出方案")
	rows := ccRows(bwf)
	if len(rows) != 1 {
		t.Fatalf("追加后主会话应收到 1 条抄送: %+v", rows)
	}
	want := "用户在分支「查缓存方案」（" + bwf + "）里说：先别动数据库，只出方案"
	if rows[0].ResponseText != want {
		t.Fatalf("抄送内容不对:\n got: %q\nwant: %q", rows[0].ResponseText, want)
	}
	if j := strings.Join(drainSSE(sse), "\n"); !strings.Contains(j, "branch_message") {
		t.Fatalf("应广播 branch_message:\n%s", j)
	}
	// 用户自己的追加原样在分支的收件箱里，不受抄送影响。
	if own := pendingTexts(t, st, bwf); len(own) != 1 || own[0].ResponseText != "先别动数据库，只出方案" || own[0].Kind == model.InboxKindBranch {
		t.Fatalf("分支自己的收件箱应只有用户原话: %+v", own)
	}

	// 路径二：分支里有一轮在等用户回复，用户答复它（SubmitFeedback）。
	sess, err := st.CreateFeedbackSession(ctx, store.CreateSessionInput{
		SessionID: "s-branch-pending", WorkflowID: bwf2, EnvHostName: "h", ProjectDirectory: "/code/a", Title: "汇报", Summary: "做完了", TimeoutSeconds: 600,
	})
	if err != nil {
		t.Fatal(err)
	}
	code, _, raw := ntfyCall(t, srv, http.MethodPost, "/api/v1/sessions/"+sess.ID+"/submit", map[string]any{"response_text": "方案 A，开工", "user_messages": []string{"可以"}})
	if code != http.StatusOK {
		t.Fatalf("回复待处理会话应返回 200: %d %s", code, raw)
	}
	if rows = ccRows(bwf2); len(rows) != 1 || !strings.HasSuffix(rows[0].ResponseText, "里说：方案 A，开工") {
		t.Fatalf("回复后主会话应收到 1 条抄送: %+v", rows)
	}

	// 路径三：用户的追加并入那一轮（已完成、Agent 还没取走的回复）。
	code, out, raw := ntfyCall(t, srv, http.MethodPost, "/api/v1/workflows/"+bwf2+"/append", map[string]any{"response_text": "补一句：别忘了回滚"})
	if code != http.StatusOK || out["type"] != "session" {
		t.Fatalf("追加应并入未消费的回复: %d %s", code, raw)
	}
	if rows = ccRows(bwf2); len(rows) != 2 || !strings.HasSuffix(rows[1].ResponseText, "里说：补一句：别忘了回滚") {
		t.Fatalf("并入回复的追加也应抄送: %+v", rows)
	}

	// 抄送开关关掉：用户的话照常送达分支，主会话不再收到。
	if code, _, raw := ntfyCall(t, srv, http.MethodPut, "/api/v1/workflows/"+bwf+"/branch", map[string]any{"copy_user_notes": false}); code != http.StatusOK {
		t.Fatalf("关抄送应返回 200: %d %s", code, raw)
	}
	appendText(t, srv, bwf, "这句不抄送")
	if rows = ccRows(bwf); len(rows) != 1 {
		t.Fatalf("关闭抄送后不应再增加: %+v", rows)
	}
	// 再打开；结束分支后停止。
	ntfyCall(t, srv, http.MethodPut, "/api/v1/workflows/"+bwf+"/branch", map[string]any{"copy_user_notes": true})
	appendText(t, srv, bwf, "这句抄送")
	if rows = ccRows(bwf); len(rows) != 2 {
		t.Fatalf("重新打开抄送后应增加: %+v", rows)
	}
	ntfyCall(t, srv, http.MethodPost, "/api/v1/workflows/"+bwf+"/branch/close", nil)
	appendText(t, srv, bwf, "结束以后的话")
	if rows = ccRows(bwf); len(rows) != 2 {
		t.Fatalf("分支结束后不应再抄送: %+v", rows)
	}

	// 不是分支的会话：追加照常送达，没有任何人收到抄送。
	mainBefore := len(pendingTexts(t, st, branchMain))
	if code, _, raw := ntfyCall(t, srv, http.MethodPost, "/api/v1/workflows/wf-solo/append", map[string]any{"response_text": "普通会话里的话"}); code != http.StatusOK {
		t.Fatalf("普通会话的追加应照常: %d %s", code, raw)
	}
	if got, err := st.GetSessionsByWorkflow(ctx, "wf-solo", 5); err != nil || len(got) != 1 || got[0].ResponseText != "普通会话里的话" {
		t.Fatalf("普通会话应收到用户原话: %+v err=%v", got, err)
	}
	if n := len(pendingTexts(t, st, branchMain)); n != mainBefore {
		t.Fatalf("普通会话的话不应抄送给任何人（%d → %d）", mainBefore, n)
	}
}
