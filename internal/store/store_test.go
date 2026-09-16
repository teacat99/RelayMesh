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

	// 4. 测试 D-153 同项目目录自愈继承能力：当后续调用遗漏 workflow_id 时，自动继承同项目目录活跃工作流
	initialWorkflow := "relaymesh-continuation"
	projAlpha := "/home/teacat/projects/alpha"
	firstSess, err := st.CreateFeedbackSession(ctx, CreateSessionInput{
		WorkflowID:       initialWorkflow,
		ProjectDirectory: projAlpha,
		Title:            "初始主会话",
		Summary:          "阶段一工作进行中",
	})
	if err != nil {
		t.Fatalf("failed to create first session: %v", err)
	}
	if firstSess.WorkflowID != initialWorkflow {
		t.Fatalf("expected WorkflowID %q, got %q", initialWorkflow, firstSess.WorkflowID)
	}

	// 模拟后续调用因上下文压缩遗漏了 WorkflowID 参数，但携带了相同的项目目录
	healedTurn, err := st.CreateFeedbackSession(ctx, CreateSessionInput{
		WorkflowID:       "", // 遗漏传参
		ProjectDirectory: projAlpha,
		Title:            "后续汇报（未传工作流ID）",
		Summary:          "汇报最新阶段进展",
	})
	if err != nil {
		t.Fatalf("failed to create healed session: %v", err)
	}
	if healedTurn.WorkflowID != initialWorkflow {
		t.Fatalf("expected auto-healed WorkflowID to inherit %q, but got %q", initialWorkflow, healedTurn.WorkflowID)
	}

	// 模拟全新未曾见过的独立项目目录，无历史记录时应派生全新的 wf-YYYYMMDD-xxxx
	projBeta := "/home/teacat/projects/beta-brand-new"
	brandNewSess, err := st.CreateFeedbackSession(ctx, CreateSessionInput{
		WorkflowID:       "",
		ProjectDirectory: projBeta,
		Title:            "全新项目第一轮",
		Summary:          "全新项目初始化",
	})
	if err != nil {
		t.Fatalf("failed to create brand new session: %v", err)
	}
	if !strings.HasPrefix(brandNewSess.WorkflowID, "wf-") || brandNewSess.WorkflowID == initialWorkflow {
		t.Fatalf("expected brand new WorkflowID to start with 'wf-' and not equal initialWorkflow, got %q", brandNewSess.WorkflowID)
	}
}

func TestStore_D159_D161_Protections(t *testing.T) {
	st := setupTestStore(t)
	ctx := context.Background()

	projA := "/workspace/project-alpha"
	projB := "/workspace/project-beta"
	wfID := "wf-shared-test"

	// 1. 项目 A 创建第一轮会话 (pending)
	sess1, err := st.CreateFeedbackSession(ctx, CreateSessionInput{
		WorkflowID:       wfID,
		ProjectDirectory: projA,
		Title:            "项目 A 会话 1",
		Summary:          "项目 A 第一次汇报",
	})
	if err != nil {
		t.Fatalf("failed to create sess1: %v", err)
	}

	// 2. 项目 B 误用相同 workflow_id，应被 D-161 Fast-Fail 拦截拒绝跨项目覆写
	_, err = st.CreateFeedbackSession(ctx, CreateSessionInput{
		WorkflowID:       wfID,
		ProjectDirectory: projB,
		Title:            "项目 B 误用工作流",
		Summary:          "试图覆盖项目 A 的汇报",
	})
	if err == nil {
		t.Fatalf("expected conflict error when project B tries to use workflow of project A, got nil")
	}

	// 3. 用户在项目 A 上提交反馈，进入 completed 状态（待 AI 提取）
	_, err = st.SubmitFeedback(ctx, SubmitFeedbackInput{
		SessionID:    sess1.ID,
		ResponseText: "用户确认通过",
	})
	if err != nil {
		t.Fatalf("failed to submit feedback: %v", err)
	}

	// 检查此时 sess1.ConsumedByAI 应该是 false
	checkSess1, _ := st.GetFeedbackSession(ctx, sess1.ID)
	if checkSess1.ConsumedByAI {
		t.Fatalf("expected sess1 not yet consumed by AI")
	}

	// 4. 项目 A AI 发起第二轮交互，应该自动级联将第一轮打标为已消费 (D-159)
	sess2, err := st.CreateFeedbackSession(ctx, CreateSessionInput{
		WorkflowID:       wfID,
		ProjectDirectory: projA,
		Title:            "项目 A 会话 2",
		Summary:          "项目 A 第二次汇报",
	})
	if err != nil {
		t.Fatalf("failed to create sess2: %v", err)
	}

	checkSess1After, _ := st.GetFeedbackSession(ctx, sess1.ID)
	if !checkSess1After.ConsumedByAI {
		t.Fatalf("expected sess1 to be auto-marked as consumed by AI after sess2 created")
	}

	// 5. 试图对历史非最新轮次 sess1 执行 Keepalive，应被 D-159 冻结拦截
	_, err = st.KeepaliveFeedbackSession(ctx, sess1.ID, 120)
	if err == nil {
		t.Fatalf("expected keepalive on historical sess1 to be rejected, got nil")
	}

	// 6. 测试删除会话 DeleteFeedbackSession
	_, err = st.DeleteFeedbackSession(ctx, sess2.ID)
	if err != nil {
		t.Fatalf("failed to delete sess2: %v", err)
	}
	_, err = st.GetFeedbackSession(ctx, sess2.ID)
	if err == nil {
		t.Fatalf("expected sess2 to be deleted")
	}
}

func TestStore_D164_WorkflowFuzzyHealing(t *testing.T) {
	st := setupTestStore(t)
	ctx := context.Background()
	proj := "/workspace/my-app"

	// 1. 创建基准工作流 relaymesh-continuation
	baseSess, err := st.CreateFeedbackSession(ctx, CreateSessionInput{
		WorkflowID:       "relaymesh-continuation",
		ProjectDirectory: proj,
		Title:            "初始主会话",
		Summary:          "主干流程第一轮",
	})
	if err != nil {
		t.Fatalf("failed to create base session: %v", err)
	}
	if baseSess.WorkflowID != "relaymesh-continuation" {
		t.Fatalf("expected workflow_id to be relaymesh-continuation, got: %s", baseSess.WorkflowID)
	}

	// 2. 模拟大模型传了常见变体（少写了后缀 relaymesh）
	fuzzySess1, err := st.CreateFeedbackSession(ctx, CreateSessionInput{
		WorkflowID:       "relaymesh",
		ProjectDirectory: proj,
		Title:            "第二轮",
		Summary:          "汇报",
	})
	if err != nil {
		t.Fatalf("failed to create fuzzy session 1: %v", err)
	}
	if fuzzySess1.WorkflowID != "relaymesh-continuation" {
		t.Fatalf("expected fuzzy 'relaymesh' to be healed to 'relaymesh-continuation', got: %s", fuzzySess1.WorkflowID)
	}

	// 3. 模拟大模型传了下划线变体 relaymesh_continuation
	fuzzySess2, err := st.CreateFeedbackSession(ctx, CreateSessionInput{
		WorkflowID:       "relaymesh_continuation",
		ProjectDirectory: proj,
		Title:            "第三轮",
		Summary:          "汇报",
	})
	if err != nil {
		t.Fatalf("failed to create fuzzy session 2: %v", err)
	}
	if fuzzySess2.WorkflowID != "relaymesh-continuation" {
		t.Fatalf("expected underscore variant to be healed, got: %s", fuzzySess2.WorkflowID)
	}

	// 4. 模拟大模型传了带 wf- 前缀的变体 wf-relaymesh-continuation
	fuzzySess3, err := st.CreateFeedbackSession(ctx, CreateSessionInput{
		WorkflowID:       "wf-relaymesh-continuation",
		ProjectDirectory: proj,
		Title:            "第四轮",
		Summary:          "汇报",
	})
	if err != nil {
		t.Fatalf("failed to create fuzzy session 3: %v", err)
	}
	if fuzzySess3.WorkflowID != "relaymesh-continuation" {
		t.Fatalf("expected prefix variant to be healed, got: %s", fuzzySess3.WorkflowID)
	}

	// 5. 传一个完全不同的、合法的全新工作流名称，应正常创建新工作流
	diffSess, err := st.CreateFeedbackSession(ctx, CreateSessionInput{
		WorkflowID:       "brand-new-feature",
		ProjectDirectory: proj,
		Title:            "全新独立分支",
		Summary:          "完全不同的需求",
	})
	if err != nil {
		t.Fatalf("failed to create diff session: %v", err)
	}
	if diffSess.WorkflowID != "brand-new-feature" {
		t.Fatalf("expected completely distinct workflow to be retained as 'brand-new-feature', got: %s", diffSess.WorkflowID)
	}
}

