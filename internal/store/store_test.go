package store

import (
	"context"
	"strings"
	"testing"

	"github.com/teacat99/RelayMesh/internal/model"
	"gorm.io/gorm"
)

func setupTestStore(t *testing.T) *Store {
	st, err := New(":memory:")
	if err != nil {
		t.Fatalf("failed to create memory store: %v", err)
	}
	return st
}

func TestStore_CreateAndSyncTask(t *testing.T) {
	st := setupTestStore(t)
	ctx := context.Background()

	// 1. Create task
	task, err := st.CreateTask(ctx, model.CreateTaskInput{
		ProjectID: "test-proj",
		TaskID:    "task-001",
		Segments: []model.Segment{
			{Name: "work_order", Content: "Implement feature A"},
			{Name: "development_rules", Content: "Follow rules"},
		},
		WaitPolicy: model.WaitPolicy{
			AfterMinutes:        5,
			MaxNoFeedbackChecks: 3,
			WaitInstruction:     "Wait {minutes}m",
		},
	})
	if err != nil {
		t.Fatalf("CreateTask failed: %v", err)
	}
	if task.ID != "task-001" || task.Revision != 1 || len(task.Segments) != 2 {
		t.Fatalf("unexpected task state: %+v", task)
	}

	// 2. Sync from worker
	syncRes, err := st.Sync(ctx, model.SyncInput{
		ProjectID:         "test-proj",
		TaskID:            "task-001",
		KnownTaskRevision: 0,
	})
	if err != nil {
		t.Fatalf("Sync failed: %v", err)
	}
	if len(syncRes.Segments) != 2 {
		t.Fatalf("expected 2 segments on initial sync, got %d", len(syncRes.Segments))
	}

	// 3. Worker adds a report
	repRes, err := st.AddReport(ctx, model.AddReportInput{
		ProjectID: "test-proj",
		TaskID:    "task-001",
		Kind:      "progress",
		Body:      "Completed step 1",
		References: []model.PathReference{
			{Path: "src/main.go", Line: 10, Description: "entrypoint"},
		},
	})
	if err != nil {
		t.Fatalf("AddReport failed: %v", err)
	}
	if repRes.Report.Sequence != 1 || repRes.Wait.State != "waiting" {
		t.Fatalf("unexpected report result: %+v", repRes)
	}

	// 4. Master sends feedback
	fb, err := st.SendFeedback(ctx, model.SendFeedbackInput{
		ProjectID: "test-proj",
		TaskID:    "task-001",
		Body:      "Looks good, proceed to step 2",
	})
	if err != nil {
		t.Fatalf("SendFeedback failed: %v", err)
	}
	if fb.Sequence != 1 {
		t.Fatalf("expected feedback sequence 1, got %d", fb.Sequence)
	}

	// 5. Worker checks feedback
	checkRes, err := st.CheckFeedback(ctx, model.CheckFeedbackInput{
		ProjectID:             "test-proj",
		TaskID:                "task-001",
		AfterFeedbackSequence: 0,
	})
	if err != nil {
		t.Fatalf("CheckFeedback failed: %v", err)
	}
	if len(checkRes.Feedback) != 1 || checkRes.Feedback[0].Body != "Looks good, proceed to step 2" {
		t.Fatalf("unexpected feedback check: %+v", checkRes)
	}

	// 6. Master acks reports
	ackSummary, err := st.AckReports(ctx, "test-proj", "task-001", 1)
	if err != nil {
		t.Fatalf("AckReports failed: %v", err)
	}
	if ackSummary.UnreadReportCount != 0 {
		t.Fatalf("expected 0 unread reports, got %d", ackSummary.UnreadReportCount)
	}
}

func TestStore_FeedbackSession(t *testing.T) {
	st := setupTestStore(t)
	ctx := context.Background()

	// 1. Create session
	sess, err := st.CreateFeedbackSession(ctx, CreateSessionInput{
		WorkflowID:       "wf-123",
		ProjectDirectory: "/test/dir",
		Title:            "Plan Review",
		Summary:          "Please review the proposed plan.",
		TimeoutSeconds:   600,
	})
	if err != nil {
		t.Fatalf("CreateFeedbackSession failed: %v", err)
	}
	if sess.Status != "pending" || sess.WorkflowID != "wf-123" {
		t.Fatalf("unexpected session: %+v", sess)
	}

	// 2. Submit feedback
	completed, err := st.SubmitFeedback(ctx, SubmitFeedbackInput{
		SessionID:    sess.ID,
		ResponseText: "Approved, proceed with plan",
		UserMessages: []string{"Looks solid"},
	})
	if err != nil {
		t.Fatalf("SubmitFeedback failed: %v", err)
	}
	if completed.Status != "completed" || completed.ResponseText != "Approved, proceed with plan" {
		t.Fatalf("unexpected completed session: %+v", completed)
	}

	// 3. Archive by workflow ID
	archived, err := st.ArchiveFeedbackSession(ctx, "wf-123")
	if err != nil {
		t.Fatalf("ArchiveFeedbackSession failed: %v", err)
	}
	if archived.Status != "archived" {
		t.Fatalf("expected archived status, got %s", archived.Status)
	}

	// 4. Verify in ListFeedbackSessions
	archivedList, err := st.ListFeedbackSessions(ctx, "", "archived", 100)
	if err != nil || len(archivedList) != 1 {
		t.Fatalf("expected 1 archived session, got %d (err: %v)", len(archivedList), err)
	}

	// 5. Unarchive by workflow ID
	unarchived, err := st.UnarchiveFeedbackSession(ctx, "wf-123")
	if err != nil {
		t.Fatalf("UnarchiveFeedbackSession failed: %v", err)
	}
	if unarchived.Status != "completed" {
		t.Fatalf("expected completed status, got %s", unarchived.Status)
	}
}

func TestStore_WorkflowDraft(t *testing.T) {
	st := setupTestStore(t)
	ctx := context.Background()

	// 1. Initial draft is nil
	draft, err := st.GetWorkflowDraft(ctx, "wf-test-draft")
	if err != nil {
		t.Fatalf("GetWorkflowDraft failed: %v", err)
	}
	if draft != nil {
		t.Fatalf("expected nil draft initially, got %+v", draft)
	}

	// 2. Save draft
	draftJSON := `{"activeIndex":1,"drafts":[{"id":"1","text":"Draft 1"},{"id":"2","text":"Draft 2"}]}`
	saved, err := st.SaveWorkflowDraft(ctx, "wf-test-draft", 1, draftJSON)
	if err != nil {
		t.Fatalf("SaveWorkflowDraft failed: %v", err)
	}
	if saved.ActiveIndex != 1 || saved.DraftsJSON != draftJSON {
		t.Fatalf("unexpected saved draft: %+v", saved)
	}

	// 3. Retrieve draft
	fetched, err := st.GetWorkflowDraft(ctx, "wf-test-draft")
	if err != nil {
		t.Fatalf("GetWorkflowDraft failed: %v", err)
	}
	if fetched == nil || fetched.ActiveIndex != 1 || fetched.DraftsJSON != draftJSON {
		t.Fatalf("unexpected fetched draft: %+v", fetched)
	}

	// 4. Delete draft
	if err := st.DeleteWorkflowDraft(ctx, "wf-test-draft"); err != nil {
		t.Fatalf("DeleteWorkflowDraft failed: %v", err)
	}
	afterDel, err := st.GetWorkflowDraft(ctx, "wf-test-draft")
	if err != nil || afterDel != nil {
		t.Fatalf("expected nil draft after delete, got %+v", afterDel)
	}
}

func TestStore_CredentialHostnameCascade(t *testing.T) {
	st := setupTestStore(t)
	ctx := context.Background()

	cred := &model.MCPCredential{
		Name:     "Test WSL Token",
		HostName: "initial-host",
		IsActive: true,
	}
	if err := st.CreateCredential(ctx, cred); err != nil {
		t.Fatalf("failed to create credential: %v", err)
	}

	// 1. 创建绑定该凭据的会话
	sess1, err := st.CreateFeedbackSession(ctx, CreateSessionInput{
		WorkflowID:         "wf-cascade-test",
		CredentialID:       cred.ID,
		CredentialHostName: cred.HostName,
		Title:              "Session 1",
		Summary:            "Summary 1",
	})
	if err != nil {
		t.Fatalf("failed to create session 1: %v", err)
	}
	if sess1.CredentialID == nil || *sess1.CredentialID != cred.ID {
		t.Fatalf("expected CredentialID %d, got %+v", cred.ID, sess1.CredentialID)
	}

	// 2. 创建未绑定凭据的历史旧会话
	sess2, err := st.CreateFeedbackSession(ctx, CreateSessionInput{
		WorkflowID:  "wf-cascade-test-legacy",
		EnvHostName: "server-default",
		Title:       "Session Legacy",
		Summary:     "Summary Legacy",
	})
	if err != nil {
		t.Fatalf("failed to create legacy session: %v", err)
	}

	// 3. 更新凭据的 host_name 为 "wsl-machine"
	_, err = st.UpdateCredential(ctx, cred.ID, map[string]any{
		"host_name": "wsl-machine",
	})
	if err != nil {
		t.Fatalf("failed to update credential: %v", err)
	}

	// 4. 验证 sess1 和 sess2 的 host_name 均被追溯级联更新为 "wsl-machine"
	updated1, err := st.GetFeedbackSession(ctx, sess1.ID)
	if err != nil {
		t.Fatalf("failed to get session 1: %v", err)
	}
	if updated1.HostName != "wsl-machine" {
		t.Fatalf("expected session 1 HostName to be 'wsl-machine', got %q", updated1.HostName)
	}

	updated2, err := st.GetFeedbackSession(ctx, sess2.ID)
	if err != nil {
		t.Fatalf("failed to get session 2: %v", err)
	}
	if updated2.HostName != "wsl-machine" {
		t.Fatalf("expected legacy session HostName to be 'wsl-machine', got %q", updated2.HostName)
	}
	if updated2.CredentialID == nil || *updated2.CredentialID != cred.ID {
		t.Fatalf("expected legacy session to be associated with credential %d, got %+v", cred.ID, updated2.CredentialID)
	}
}

func TestStore_WorkflowIDAutoDerivationAndSelfHealing(t *testing.T) {
	st := setupTestStore(t)
	ctx := context.Background()

	// 1. 测试未提供 WorkflowID 时自动派生规范的 wf-YYYYMMDD-xxxx
	sess, err := st.CreateFeedbackSession(ctx, CreateSessionInput{
		WorkflowID: "",
		Title:      "测试无工作流ID",
		Summary:    "测试正文内容",
	})
	if err != nil {
		t.Fatalf("failed to create session: %v", err)
	}
	if !strings.HasPrefix(sess.WorkflowID, "wf-") {
		t.Fatalf("expected auto-derived WorkflowID to start with 'wf-', got: %q", sess.WorkflowID)
	}

	// 2. 模拟老旧存量数据（强行插入一条空 workflow_id 的记录）
	legacyID := "sess-legacy-test"
	st.db.Exec("INSERT INTO feedback_sessions (id, workflow_id, summary, status, created_at, updated_at) VALUES (?, '', '老旧遗留数据', 'completed', datetime('now'), datetime('now'))", legacyID)

	// 3. 模拟 Store 初始化触发自动自愈迁移
	st.db.Model(&model.FeedbackSession{}).
		Where("workflow_id IS NULL OR workflow_id = ''").
		Updates(map[string]interface{}{
			"workflow_id": gorm.Expr("'wf-' || replace(id, 'sess-', '')"),
		})

	healedSess, err := st.GetFeedbackSession(ctx, legacyID)
	if err != nil {
		t.Fatalf("failed to get legacy session: %v", err)
	}
	if healedSess.WorkflowID != "wf-legacy-test" {
		t.Fatalf("expected healed workflow_id to be 'wf-legacy-test', got: %q", healedSess.WorkflowID)
	}
}

