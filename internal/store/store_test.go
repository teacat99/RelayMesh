package store

import (
	"context"
	"testing"

	"github.com/teacat99/RelayMesh/internal/model"
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
