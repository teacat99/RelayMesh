package store

import (
	"context"
	"testing"
	"time"
)

func newAnchorSession(t *testing.T, st *Store, id, workflow, commit, subject string) {
	t.Helper()
	_, err := st.CreateFeedbackSession(context.Background(), CreateSessionInput{
		SessionID:        id,
		WorkflowID:       workflow,
		ProjectDirectory: "/test/anchor-store",
		Summary:          "交付 " + id,
		GitCommit:        commit,
		GitSubject:       subject,
	})
	if err != nil {
		t.Fatalf("创建会话 %s: %v", id, err)
	}
	// 让会话一定处于 completed，下一次创建才会新建一轮而不是覆写 pending。
	if _, err := st.SubmitFeedback(context.Background(), SubmitFeedbackInput{SessionID: id, ResponseText: "ok"}); err != nil {
		t.Fatalf("完成会话 %s: %v", id, err)
	}
	// 保证 created_at 严格递增，排序结果可预期。
	time.Sleep(5 * time.Millisecond)
}

func TestCreateSessionPersistsAnchor(t *testing.T) {
	st := setupTestStore(t)
	newAnchorSession(t, st, "sess-anc-1", "wf-anc", "57bb6d1", "docs: first")

	got, err := st.GetFeedbackSession(context.Background(), "sess-anc-1")
	if err != nil {
		t.Fatal(err)
	}
	if got.GitCommit != "57bb6d1" || got.GitSubject != "docs: first" {
		t.Fatalf("锚点应随会话入库: %#v", got)
	}
}

func TestGetLatestAnchoredSession(t *testing.T) {
	st := setupTestStore(t)
	ctx := context.Background()
	newAnchorSession(t, st, "sess-a1", "wf-latest", "1111111", "one")
	newAnchorSession(t, st, "sess-a2", "wf-latest", "", "")
	newAnchorSession(t, st, "sess-a3", "wf-latest", "3333333", "three")
	newAnchorSession(t, st, "sess-a4", "wf-latest", "", "")

	latest, err := st.GetLatestAnchoredSession(ctx, "wf-latest", "")
	if err != nil || latest == nil || latest.ID != "sess-a3" || latest.GitCommit != "3333333" {
		t.Fatalf("应跳过没有锚点的会话取到最近一次: %#v %v", latest, err)
	}

	prev, err := st.GetLatestAnchoredSession(ctx, "wf-latest", "sess-a3")
	if err != nil || prev == nil || prev.ID != "sess-a1" {
		t.Fatalf("排除当前会话后应取到上一次: %#v %v", prev, err)
	}

	none, err := st.GetLatestAnchoredSession(ctx, "wf-latest", "sess-a1")
	if err != nil || none == nil || none.ID != "sess-a3" {
		t.Fatalf("排除较旧会话不应影响最近一次: %#v %v", none, err)
	}

	missing, err := st.GetLatestAnchoredSession(ctx, "wf-without-anchor", "")
	if err != nil || missing != nil {
		t.Fatalf("没有锚点的工作流应返回 nil: %#v %v", missing, err)
	}
	if blank, err := st.GetLatestAnchoredSession(ctx, "  ", ""); err != nil || blank != nil {
		t.Fatalf("空工作流应返回 nil: %#v %v", blank, err)
	}
}

func TestFindAnchoredSessionByCommitMatchesPrefixes(t *testing.T) {
	st := setupTestStore(t)
	ctx := context.Background()
	newAnchorSession(t, st, "sess-f1", "wf-find", "57bb6d1", "short")
	newAnchorSession(t, st, "sess-f2", "wf-find", "7321232aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "long")

	if got, _ := st.FindAnchoredSessionByCommit(ctx, "wf-find", "57BB6D1"); got == nil || got.ID != "sess-f1" {
		t.Fatalf("应不区分大小写命中: %#v", got)
	}
	if got, _ := st.FindAnchoredSessionByCommit(ctx, "wf-find", "7321232"); got == nil || got.ID != "sess-f2" {
		t.Fatalf("较短的哈希应命中较长的锚点: %#v", got)
	}
	if got, _ := st.FindAnchoredSessionByCommit(ctx, "wf-find", "57bb6d1ffffffff"); got == nil || got.ID != "sess-f1" {
		t.Fatalf("较长的哈希应命中较短的锚点: %#v", got)
	}
	if got, _ := st.FindAnchoredSessionByCommit(ctx, "wf-find", "deadbeef"); got != nil {
		t.Fatalf("不存在的哈希应返回 nil: %#v", got)
	}
	if got, _ := st.FindAnchoredSessionByCommit(ctx, "wf-other", "57bb6d1"); got != nil {
		t.Fatalf("不得跨工作流命中: %#v", got)
	}
}

// 历史数据：新增的两列对旧行是 NULL，读取不能报错，字段保持空串。
func TestLegacyRowsWithNullAnchorColumnsStayReadable(t *testing.T) {
	st := setupTestStore(t)
	ctx := context.Background()
	newAnchorSession(t, st, "sess-legacy", "wf-legacy", "57bb6d1", "will be nulled")
	if err := st.db.Exec("UPDATE feedback_sessions SET git_commit = NULL, git_subject = NULL WHERE id = ?", "sess-legacy").Error; err != nil {
		t.Fatal(err)
	}

	got, err := st.GetFeedbackSession(ctx, "sess-legacy")
	if err != nil {
		t.Fatalf("读取 NULL 锚点列的旧行不应失败: %v", err)
	}
	if got.GitCommit != "" || got.GitSubject != "" {
		t.Fatalf("NULL 应读成空串: %#v", got)
	}
	if anchored, err := st.GetLatestAnchoredSession(ctx, "wf-legacy", ""); err != nil || anchored != nil {
		t.Fatalf("NULL 不算锚点: %#v %v", anchored, err)
	}
	brief, err := st.ListFeedbackSessionsBrief(ctx, "", "", 50)
	if err != nil {
		t.Fatalf("轻量列表读取旧行失败: %v", err)
	}
	if len(brief) == 0 {
		t.Fatal("轻量列表应包含该会话")
	}
}

// 侧栏用的轻量列表必须带出锚点，否则前端拿不到短哈希。
func TestBriefListIncludesAnchor(t *testing.T) {
	st := setupTestStore(t)
	newAnchorSession(t, st, "sess-brief", "wf-brief", "57bb6d1", "brief subject")

	brief, err := st.ListFeedbackSessionsBrief(context.Background(), "", "", 50)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range brief {
		if s.ID == "sess-brief" {
			if s.GitCommit != "57bb6d1" || s.GitSubject != "brief subject" {
				t.Fatalf("轻量列表应带出锚点: %#v", s)
			}
			return
		}
	}
	t.Fatal("轻量列表里没找到会话")
}

// 覆写 pending 会话（同一工作流上一轮仍在等待）时，锚点以新调用为准，漏传则保留。
func TestPendingOverwriteUpdatesAnchor(t *testing.T) {
	st := setupTestStore(t)
	ctx := context.Background()
	mk := func(commit, subject string) *CreateSessionInput {
		return &CreateSessionInput{WorkflowID: "wf-pending", ProjectDirectory: "/test/anchor-store", Summary: "s", GitCommit: commit, GitSubject: subject}
	}
	if _, err := st.CreateFeedbackSession(ctx, *mk("1111111", "old")); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateFeedbackSession(ctx, *mk("2222222", "new")); err != nil {
		t.Fatal(err)
	}
	sessions, _ := st.GetSessionsByWorkflow(ctx, "wf-pending", 10)
	if len(sessions) != 1 || sessions[0].GitCommit != "2222222" || sessions[0].GitSubject != "new" {
		t.Fatalf("覆写应更新锚点: %#v", sessions)
	}
	if _, err := st.CreateFeedbackSession(ctx, *mk("", "")); err != nil {
		t.Fatal(err)
	}
	sessions, _ = st.GetSessionsByWorkflow(ctx, "wf-pending", 10)
	if len(sessions) != 1 || sessions[0].GitCommit != "2222222" {
		t.Fatalf("漏传应保留原锚点: %#v", sessions)
	}
}
