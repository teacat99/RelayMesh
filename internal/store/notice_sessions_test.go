package store

import (
	"context"
	"testing"

	"github.com/teacat99/RelayMesh/v2/internal/model"
)

func newRound(t *testing.T, st *Store, wf, dir, summary string) *model.FeedbackSession {
	t.Helper()
	sess, err := st.CreateFeedbackSession(context.Background(), CreateSessionInput{
		WorkflowID: wf, ProjectDirectory: dir, Title: "汇报", Summary: summary,
	})
	if err != nil {
		t.Fatalf("创建汇报失败: %v", err)
	}
	return sess
}

func TestNoticeSessionIsCompletedConsumedAndWithoutReply(t *testing.T) {
	st := newConfirmStore(t)
	ctx := context.Background()
	n, err := st.CreateNoticeSession(ctx, CreateSessionInput{
		WorkflowID: "wf-n", ProjectDirectory: "/proj", Title: "进度", Summary: "已完成第一步",
		GitCommit: "abc1234", GitSubject: "feat: x", CredentialHostName: "host-a",
	})
	if err != nil {
		t.Fatal(err)
	}
	if n.Kind != model.FeedbackKindNotice || n.Status != "completed" || !n.ConsumedByAI || n.ConsumedAt == nil ||
		n.RespondedAt != nil || n.ResponseText != "" || n.DeadlineAt != nil || n.HostName != "host-a" || n.GitCommit != "abc1234" {
		t.Fatalf("notice 的形态不对: %+v", n)
	}
	got, err := st.GetFeedbackSession(ctx, n.ID)
	if err != nil || got.Kind != model.FeedbackKindNotice || got.Summary != "已完成第一步" {
		t.Fatalf("读回不一致: %+v %v", got, err)
	}
	_, err = st.CreateNoticeSession(ctx, CreateSessionInput{WorkflowID: "wf-n", Summary: "  "})
	wantKind(t, err, ErrInvalidInput, "空内容")
	_, err = st.CreateNoticeSession(ctx, CreateSessionInput{WorkflowID: " ", Summary: "x"})
	wantKind(t, err, ErrInvalidInput, "没有 workflow_id")
}

func TestNoticeDoesNotSwallowQueuedAppendsNorTouchPendingRound(t *testing.T) {
	st := newConfirmStore(t)
	ctx := context.Background()
	const wf = "wf-keep"
	pending := newRound(t, st, wf, "/proj", "我在等你回复")
	if pending.Status != "pending" {
		t.Fatalf("前置：应是 pending: %s", pending.Status)
	}
	if _, err := st.QueueWorkflowFeedback(ctx, QueueFeedbackInput{WorkflowID: wf, ResponseText: "用户追加的一句话"}); err != nil {
		t.Fatal(err)
	}

	if _, err := st.CreateNoticeSession(ctx, CreateSessionInput{WorkflowID: wf, ProjectDirectory: "/proj", Summary: "顺带告知进度"}); err != nil {
		t.Fatal(err)
	}

	// 等待中的汇报原样保留（没被覆写成 notice，也没被完成）
	still, _ := st.GetFeedbackSession(ctx, pending.ID)
	if still.Status != "pending" || still.Summary != "我在等你回复" || still.Kind != "" {
		t.Fatalf("notice 不应改动等待中的汇报: %+v", still)
	}
	// 收件箱里的追加没被取走
	sum, err := st.InboxSummary(ctx, wf, still.UpdatedAt)
	if err != nil || sum.Unread != 1 {
		t.Fatalf("notice 不应吞掉用户的追加: %+v %v", sum, err)
	}
}

func TestNoticeDoesNotMarkEarlierCompletedRoundsConsumed(t *testing.T) {
	st := newConfirmStore(t)
	ctx := context.Background()
	const wf = "wf-unconsumed"
	r := newRound(t, st, wf, "/proj", "第一轮")
	if _, err := st.SubmitFeedback(ctx, SubmitFeedbackInput{SessionID: r.ID, ResponseText: "用户回复"}); err != nil {
		t.Fatal(err)
	}
	// 用户已回复、智能体还没取走
	if got, _ := st.GetFeedbackSession(ctx, r.ID); got.ConsumedByAI {
		t.Fatalf("前置：不应已消费")
	}
	if _, err := st.CreateNoticeSession(ctx, CreateSessionInput{WorkflowID: wf, ProjectDirectory: "/proj", Summary: "进度"}); err != nil {
		t.Fatal(err)
	}
	if got, _ := st.GetFeedbackSession(ctx, r.ID); got.ConsumedByAI {
		t.Fatalf("notice 不应把用户尚未被取走的回复标成已消费（那会让回复丢失）")
	}
}

func TestLatestWorkflowSessionIgnoresNoticesButHistoryKeepsThem(t *testing.T) {
	st := newConfirmStore(t)
	ctx := context.Background()
	const wf = "wf-latest"

	// 只有 notice：没有「汇报」可找
	if _, err := st.CreateNoticeSession(ctx, CreateSessionInput{WorkflowID: wf, ProjectDirectory: "/proj", Summary: "孤零零的通知"}); err != nil {
		t.Fatal(err)
	}
	_, err := st.GetLatestWorkflowFeedbackSession(ctx, wf)
	wantKind(t, err, ErrNotFound, "只有 notice 时没有最新汇报")

	round := newRound(t, st, wf, "/proj", "第一轮")
	if _, err := st.SubmitFeedback(ctx, SubmitFeedbackInput{SessionID: round.ID, ResponseText: "好"}); err != nil {
		t.Fatal(err)
	}
	// 之后又来了两条 notice：最新汇报仍是那一轮
	for _, text := range []string{"通知 A", "通知 B"} {
		if _, err := st.CreateNoticeSession(ctx, CreateSessionInput{WorkflowID: wf, ProjectDirectory: "/proj", Summary: text}); err != nil {
			t.Fatal(err)
		}
	}
	latest, err := st.GetLatestWorkflowFeedbackSession(ctx, wf)
	if err != nil || latest.ID != round.ID {
		t.Fatalf("最新汇报应是 %s，得到 %+v %v", round.ID, latest, err)
	}

	all, _ := st.GetSessionsByWorkflow(ctx, wf, 50)
	rounds, _ := st.GetFeedbackRoundsByWorkflow(ctx, wf, 50)
	if len(all) != 4 || len(rounds) != 1 || rounds[0].ID != round.ID {
		t.Fatalf("历史应含全部 4 条、汇报轮次只有 1 条: all=%d rounds=%d", len(all), len(rounds))
	}

	// 有 pending 的汇报时优先返回它
	pending := newRound(t, st, wf, "/proj", "第二轮")
	latest, _ = st.GetLatestWorkflowFeedbackSession(ctx, wf)
	if latest.ID != pending.ID {
		t.Fatalf("应优先 pending 汇报: %s", latest.ID)
	}
}

func TestLegacyRowsWithNullKindCountAsRounds(t *testing.T) {
	st := newConfirmStore(t)
	ctx := context.Background()
	r := newRound(t, st, "wf-legacy", "/proj", "旧会话")
	// 旧版本写入的行 kind 是 NULL（列是后来才加的）
	if err := st.db.Exec("UPDATE feedback_sessions SET kind = NULL WHERE id = ?", r.ID).Error; err != nil {
		t.Fatal(err)
	}
	got, err := st.GetLatestWorkflowFeedbackSession(ctx, "wf-legacy")
	if err != nil || got.ID != r.ID {
		t.Fatalf("kind 为 NULL 的旧会话应被当作汇报: %+v %v", got, err)
	}
	if rounds, _ := st.GetFeedbackRoundsByWorkflow(ctx, "wf-legacy", 10); len(rounds) != 1 {
		t.Fatalf("旧会话应计入轮次")
	}
}

func TestNoticeKeepsProjectOwnershipTitleLockAndHostInheritance(t *testing.T) {
	st := newConfirmStore(t)
	ctx := context.Background()
	const wf = "wf-own"
	first, err := st.CreateFeedbackSession(ctx, CreateSessionInput{
		WorkflowID: wf, ProjectDirectory: "/proj/a", Title: "t", Summary: "s", CredentialHostName: "host-origin",
	})
	if err != nil {
		t.Fatal(err)
	}

	// 另一个项目不能往同一个 workflow_id 里写 notice
	_, err = st.CreateNoticeSession(ctx, CreateSessionInput{WorkflowID: wf, ProjectDirectory: "/proj/b", Summary: "越界"})
	wantKind(t, err, ErrConflict, "跨项目写入")

	// 用户给工作流定过标题：notice 沿用
	if _, err := st.RenameFeedbackSession(ctx, wf, "用户定的标题"); err != nil {
		t.Fatalf("重命名失败: %v", err)
	}
	n, err := st.CreateNoticeSession(ctx, CreateSessionInput{WorkflowID: wf, ProjectDirectory: "/proj/a", Title: "随便", Summary: "进度"})
	if err != nil {
		t.Fatal(err)
	}
	if n.Title != "用户定的标题" {
		t.Fatalf("应沿用用户定的标题: %q", n.Title)
	}
	// 没有凭据 / 环境主机名时继承工作流已有的主机名与凭据
	if n.HostName != first.HostName || n.ProjectDirectory != "/proj/a" {
		t.Fatalf("应继承工作流的主机与项目: host=%q dir=%q", n.HostName, n.ProjectDirectory)
	}
}
