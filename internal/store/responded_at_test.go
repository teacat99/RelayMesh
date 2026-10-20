package store

import (
	"context"
	"testing"
	"time"

	"github.com/teacat99/RelayMesh/v2/internal/model"
)

// 回复时间（RespondedAt）：页面上「您的确认回复」显示的是用户提交回复的时刻，
// 而不是智能体取走它的时刻（取走会刷新 UpdatedAt）。这里把几条会让它变样的路径都钉住。

func newPendingSession(t *testing.T, st *Store, wf string) *model.FeedbackSession {
	t.Helper()
	sess, err := st.CreateFeedbackSession(context.Background(), CreateSessionInput{WorkflowID: wf, Title: "汇报", Summary: "s", TimeoutSeconds: 60})
	if err != nil {
		t.Fatalf("CreateFeedbackSession: %v", err)
	}
	if sess.Status != "pending" || sess.RespondedAt != nil {
		t.Fatalf("新轮次应是 pending 且没有回复时间: %+v", sess)
	}
	return sess
}

func reloadSession(t *testing.T, st *Store, id string) model.FeedbackSession {
	t.Helper()
	var s model.FeedbackSession
	if err := st.DB().Where("id = ?", id).First(&s).Error; err != nil {
		t.Fatalf("load session %s: %v", id, err)
	}
	return s
}

func TestRespondedAt_SetOnSubmitAndSurvivesConsume(t *testing.T) {
	st := newInboxStore(t)
	ctx := context.Background()
	sess := newPendingSession(t, st, "wf-r1")

	time.Sleep(15 * time.Millisecond)
	got, err := st.SubmitFeedback(ctx, SubmitFeedbackInput{SessionID: sess.ID, ResponseText: "按推荐先做"})
	if err != nil {
		t.Fatalf("SubmitFeedback: %v", err)
	}
	if got.RespondedAt == nil || got.RespondedAt.Before(sess.CreatedAt) {
		t.Fatalf("提交后应记下回复时间: %+v", got.RespondedAt)
	}
	sent := *got.RespondedAt

	// 智能体隔了一阵才来取：updated_at 会被刷新成取走的时刻，responded_at 必须原样不动。
	time.Sleep(30 * time.Millisecond)
	if err := st.MarkSessionConsumedByAI(ctx, sess.ID); err != nil {
		t.Fatalf("MarkSessionConsumedByAI: %v", err)
	}
	after := reloadSession(t, st, sess.ID)
	if after.RespondedAt == nil || !after.RespondedAt.Equal(sent) {
		t.Fatalf("取走不应改动回复时间: before=%v after=%v", sent, after.RespondedAt)
	}
	if after.ConsumedAt == nil || !after.ConsumedAt.After(sent) {
		t.Fatalf("取走时间应晚于回复时间: sent=%v consumed=%v", sent, after.ConsumedAt)
	}
	if !after.UpdatedAt.After(sent) {
		t.Fatalf("前提：取走会刷新 updated_at（这正是旧界面把它当成回复时间的缘由）: sent=%v updated=%v", sent, after.UpdatedAt)
	}
}

func TestRespondedAt_AppendKeepsFirstTime(t *testing.T) {
	st := newInboxStore(t)
	ctx := context.Background()
	sess := newPendingSession(t, st, "wf-r2")

	first, err := st.SubmitFeedback(ctx, SubmitFeedbackInput{SessionID: sess.ID, ResponseText: "第一句"})
	if err != nil {
		t.Fatalf("SubmitFeedback: %v", err)
	}
	sent := *first.RespondedAt

	// 智能体还没来取时又补了一句：合并进同一条，回复时间仍是第一次。
	time.Sleep(20 * time.Millisecond)
	merged, err := st.SubmitFeedback(ctx, SubmitFeedbackInput{SessionID: sess.ID, ResponseText: "补一句"})
	if err != nil {
		t.Fatalf("SubmitFeedback(append): %v", err)
	}
	if merged.ResponseText != "第一句\n补一句" {
		t.Fatalf("应合并: %q", merged.ResponseText)
	}
	if merged.RespondedAt == nil || !merged.RespondedAt.Equal(sent) {
		t.Fatalf("合并追加应保留第一次的回复时间: want=%v got=%v", sent, merged.RespondedAt)
	}
	if !merged.UpdatedAt.After(sent) {
		t.Fatalf("合并追加仍应刷新 updated_at: %v", merged.UpdatedAt)
	}
}

func TestRespondedAt_LegacyRowGetsItOnAppend(t *testing.T) {
	st := newInboxStore(t)
	ctx := context.Background()
	sess := newPendingSession(t, st, "wf-r3")
	if _, err := st.SubmitFeedback(ctx, SubmitFeedbackInput{SessionID: sess.ID, ResponseText: "旧回复"}); err != nil {
		t.Fatalf("SubmitFeedback: %v", err)
	}
	// 模拟升级前写入的行：没有 responded_at 这一列的值。
	if err := st.DB().Model(&model.FeedbackSession{}).Where("id = ?", sess.ID).Update("responded_at", nil).Error; err != nil {
		t.Fatalf("clear responded_at: %v", err)
	}
	if reloadSession(t, st, sess.ID).RespondedAt != nil {
		t.Fatalf("前提：历史行应为空")
	}
	merged, err := st.SubmitFeedback(ctx, SubmitFeedbackInput{SessionID: sess.ID, ResponseText: "新补充"})
	if err != nil {
		t.Fatalf("SubmitFeedback(append): %v", err)
	}
	if merged.RespondedAt == nil {
		t.Fatalf("历史行在合并追加时应补上回复时间")
	}
}

func TestRespondedAt_RevokeClearsItAndResubmitSetsNew(t *testing.T) {
	st := newInboxStore(t)
	ctx := context.Background()
	sess := newPendingSession(t, st, "wf-r4")
	first, err := st.SubmitFeedback(ctx, SubmitFeedbackInput{SessionID: sess.ID, ResponseText: "写错了"})
	if err != nil {
		t.Fatalf("SubmitFeedback: %v", err)
	}
	sent := *first.RespondedAt

	if _, revoked, err := st.RevokeSessionFeedback(ctx, sess.ID); err != nil {
		t.Fatalf("RevokeSessionFeedback: %v", err)
	} else if revoked.RespondedAt != nil || revoked.Status != "pending" {
		t.Fatalf("撤回后应回到 pending 且清空回复时间: %+v", revoked)
	}
	if reloadSession(t, st, sess.ID).RespondedAt != nil {
		t.Fatalf("撤回应落库清空回复时间")
	}

	time.Sleep(15 * time.Millisecond)
	again, err := st.SubmitFeedback(ctx, SubmitFeedbackInput{SessionID: sess.ID, ResponseText: "改好了"})
	if err != nil {
		t.Fatalf("SubmitFeedback(again): %v", err)
	}
	if again.RespondedAt == nil || !again.RespondedAt.After(sent) {
		t.Fatalf("重新提交应是新的回复时间: first=%v again=%v", sent, again.RespondedAt)
	}
}

func TestRespondedAt_InstantReplyUsesEarliestQueuedTime(t *testing.T) {
	st := newInboxStore(t)
	ctx := context.Background()
	a := queueText(t, st, "wf-r5", "先说的这句")
	time.Sleep(20 * time.Millisecond)
	_ = queueText(t, st, "wf-r5", "后说的这句")
	time.Sleep(20 * time.Millisecond)

	// 智能体此刻才回来发起新一轮：两条追加被一并「秒回」。
	sess, err := st.CreateFeedbackSession(ctx, CreateSessionInput{WorkflowID: "wf-r5", Title: "汇报", Summary: "s", TimeoutSeconds: 60})
	if err != nil {
		t.Fatalf("CreateFeedbackSession: %v", err)
	}
	if sess.Status != "completed" || !sess.ConsumedByAI {
		t.Fatalf("应秒回: %+v", sess)
	}
	if sess.RespondedAt == nil {
		t.Fatalf("秒回应带回复时间")
	}
	if !sess.RespondedAt.Equal(a.CreatedAt) {
		t.Fatalf("回复时间应是最早一条追加被敲下的时刻: want=%v got=%v", a.CreatedAt, *sess.RespondedAt)
	}
	if !sess.RespondedAt.Before(sess.CreatedAt) {
		t.Fatalf("回复时间应早于这一轮被创建的时刻: responded=%v created=%v", *sess.RespondedAt, sess.CreatedAt)
	}
	if sess.ConsumedAt == nil || !sess.ConsumedAt.Equal(sess.CreatedAt) {
		t.Fatalf("秒回是创建本轮时同时取走的，取走时间应等于创建时间: %+v", sess.ConsumedAt)
	}

	back := reloadSession(t, st, sess.ID)
	if back.RespondedAt == nil || !back.RespondedAt.Equal(a.CreatedAt) {
		t.Fatalf("落库后应一致: %+v", back.RespondedAt)
	}
}

func TestRespondedAt_NoQueuedMeansPendingWithoutIt(t *testing.T) {
	st := newInboxStore(t)
	sess := newPendingSession(t, st, "wf-r6")
	if sess.ConsumedAt != nil {
		t.Fatalf("没有秒回时不应有取走时间: %+v", sess.ConsumedAt)
	}
}
