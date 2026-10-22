package store

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/teacat99/RelayMesh/v2/internal/model"
)

func newInboxStore(t *testing.T) *Store {
	t.Helper()
	st, err := New(":memory:")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func queueText(t *testing.T, st *Store, wf, text string) *model.QueuedFeedback {
	t.Helper()
	q, err := st.QueueWorkflowFeedback(context.Background(), QueueFeedbackInput{WorkflowID: wf, ResponseText: text})
	if err != nil {
		t.Fatalf("QueueWorkflowFeedback(%q): %v", text, err)
	}
	return q
}

func rowByID(t *testing.T, st *Store, id uint) model.QueuedFeedback {
	t.Helper()
	var q model.QueuedFeedback
	if err := st.DB().First(&q, id).Error; err != nil {
		t.Fatalf("load row %d: %v", id, err)
	}
	q.State = q.DeriveState()
	return q
}

func TestInbox_EachAppendIsItsOwnRow(t *testing.T) {
	st := newInboxStore(t)
	a := queueText(t, st, "wf-a", "第一条")
	b := queueText(t, st, "wf-a", "第二条")
	if a.ID == b.ID {
		t.Fatalf("两条追加应是两行，却得到同一个编号 %d", a.ID)
	}
	if a.State != model.InboxStateQueued || b.State != model.InboxStateQueued || a.Kind != "append" {
		t.Fatalf("新追加应是 queued / append: %+v %+v", a, b)
	}
	list, err := st.ListPendingQueuedFeedbacks(context.Background(), "wf-a")
	if err != nil || len(list) != 2 {
		t.Fatalf("应有 2 条待处理: %v %v", len(list), err)
	}
	if list[0].ResponseText != "第一条" || list[1].ResponseText != "第二条" {
		t.Fatalf("应按时间顺序: %q, %q", list[0].ResponseText, list[1].ResponseText)
	}
}

func TestInbox_RejectsEmptyAndFullInbox(t *testing.T) {
	st := newInboxStore(t)
	ctx := context.Background()
	if _, err := st.QueueWorkflowFeedback(ctx, QueueFeedbackInput{WorkflowID: "wf", ResponseText: "  \n"}); err == nil {
		t.Fatal("完全空的追加应被拒绝")
	}
	if _, err := st.QueueWorkflowFeedback(ctx, QueueFeedbackInput{WorkflowID: "", ResponseText: "x"}); err == nil {
		t.Fatal("没有 workflow_id 应被拒绝")
	}
	// 只有快捷标签、只有图片都算有内容
	if _, err := st.QueueWorkflowFeedback(ctx, QueueFeedbackInput{WorkflowID: "wf", UserMessages: []string{"继续"}}); err != nil {
		t.Fatalf("只有标签应可以: %v", err)
	}
	if _, err := st.QueueWorkflowFeedback(ctx, QueueFeedbackInput{WorkflowID: "wf", Images: []model.SessionImage{{Name: "a.png", Data: "AAAA"}}}); err != nil {
		t.Fatalf("只有图片应可以: %v", err)
	}
	for i := 2; i < InboxMaxPending; i++ {
		queueText(t, st, "wf", "填满")
	}
	if _, err := st.QueueWorkflowFeedback(ctx, QueueFeedbackInput{WorkflowID: "wf", ResponseText: "再来一条"}); err == nil {
		t.Fatal("达到上限后应拒绝")
	}
	// 别的工作流不受影响
	queueText(t, st, "wf-other", "不受影响")
}

func TestInbox_ReadAckLifecycleAndWebListing(t *testing.T) {
	st := newInboxStore(t)
	ctx := context.Background()
	now := time.Now()
	a := queueText(t, st, "wf", "把按钮改成蓝色")
	b := queueText(t, st, "wf", "再加一个确认弹窗")

	sum, err := st.InboxSummary(ctx, "wf", now)
	if err != nil || sum.Unread != 2 || sum.Delivered != 0 {
		t.Fatalf("概况应是 2 未读: %+v %v", sum, err)
	}
	if sum.LatestPreview != "再加一个确认弹窗" {
		t.Fatalf("预览应是最新一条: %q", sum.LatestPreview)
	}

	res, err := st.InboxRead(ctx, "wf", InboxReadOptions{}, now)
	if err != nil {
		t.Fatalf("InboxRead: %v", err)
	}
	if len(res.New) != 2 || res.New[0].ID != a.ID || res.New[1].ID != b.ID {
		t.Fatalf("应按时间顺序送达 2 条: %+v", res.New)
	}
	for _, q := range res.New {
		if q.State != model.InboxStateDelivered || q.DeliveredVia != InboxViaTool || q.DeliveredAt == nil {
			t.Fatalf("主动读取应记为 delivered / tool: %+v", q)
		}
	}
	// 再读一次：不会再「新送达」，但会作为「已读取未确认」列出
	res2, _ := st.InboxRead(ctx, "wf", InboxReadOptions{}, now.Add(time.Second))
	if len(res2.New) != 0 || len(res2.Pending) != 2 {
		t.Fatalf("第二次读取应是 0 新 + 2 待确认: new=%d pending=%d", len(res2.New), len(res2.Pending))
	}
	sum, _ = st.InboxSummary(ctx, "wf", now)
	if sum.Unread != 0 || sum.Delivered != 2 {
		t.Fatalf("读取后概况应是 0 未读 2 待确认: %+v", sum)
	}

	// 只确认第一条；排队中的（没读过的）不能确认
	c := queueText(t, st, "wf", "还没读到的")
	acked, err := st.InboxAck(ctx, "wf", []uint{a.ID, c.ID}, now.Add(2*time.Second))
	if err != nil || len(acked) != 1 || acked[0] != a.ID {
		t.Fatalf("只应确认已读取的那条: %v %v", acked, err)
	}
	if got := rowByID(t, st, a.ID); got.State != model.InboxStateAcked || got.AckedAt == nil {
		t.Fatalf("a 应是 acked: %+v", got)
	}
	if got := rowByID(t, st, c.ID); got.State != model.InboxStateQueued {
		t.Fatalf("c 仍应是 queued: %+v", got)
	}

	// Web 列表：a（经 tool 确认，近一天内）、b（delivered）、c（queued）都在，按时间顺序
	items, err := st.ListInboxItems(ctx, "wf")
	if err != nil || len(items) != 3 {
		t.Fatalf("Web 应看到 3 条: %d %v", len(items), err)
	}
	wantStates := []string{model.InboxStateAcked, model.InboxStateDelivered, model.InboxStateQueued}
	for i, q := range items {
		if q.State != wantStates[i] {
			t.Fatalf("第 %d 条状态 = %s，期望 %s", i, q.State, wantStates[i])
		}
	}

	// ack 全部（ids 为空）：只确认「已读取未确认」的
	acked, _ = st.InboxAck(ctx, "wf", nil, now.Add(3*time.Second))
	if len(acked) != 1 || acked[0] != b.ID {
		t.Fatalf("空 ids 应确认全部已读取未确认的: %v", acked)
	}
}

func TestInbox_ListOmitsItemsAlreadyShownAsSessions(t *testing.T) {
	st := newInboxStore(t)
	ctx := context.Background()
	queueText(t, st, "wf", "会被秒回带走")
	if _, err := st.CreateFeedbackSession(ctx, CreateSessionInput{WorkflowID: "wf", Title: "t", Summary: "s", TimeoutSeconds: 60}); err != nil {
		t.Fatalf("CreateFeedbackSession: %v", err)
	}
	items, _ := st.ListInboxItems(ctx, "wf")
	if len(items) != 0 {
		t.Fatalf("随秒回带走的已经出现在对话记录里，不应再出现在收件箱列表: %+v", items)
	}
	// 经 tool 送达又确认过的，则要留在列表里（对话记录里没有它）
	queueText(t, st, "wf", "靠 inbox 读到的")
	_, _ = st.InboxRead(ctx, "wf", InboxReadOptions{}, time.Now())
	_, _ = st.InboxAck(ctx, "wf", nil, time.Now())
	items, _ = st.ListInboxItems(ctx, "wf")
	if len(items) != 1 || items[0].State != model.InboxStateAcked || items[0].DeliveredVia != InboxViaTool {
		t.Fatalf("经 inbox 读到并确认的应留在列表里: %+v", items)
	}
}

func TestInbox_InteractiveFeedbackMergesQueuedAndAcksDelivered(t *testing.T) {
	st := newInboxStore(t)
	ctx := context.Background()
	img1 := model.SessionImage{Name: "1.png", Data: "AAAA", Format: "png"}
	img2 := model.SessionImage{Name: "2.png", Data: "BBBB", Format: "png"}
	_, _ = st.QueueWorkflowFeedback(ctx, QueueFeedbackInput{WorkflowID: "wf", ResponseText: "已经读过的", UserMessages: []string{"旧标签"}})
	_, _ = st.InboxRead(ctx, "wf", InboxReadOptions{}, time.Now()) // 第一条被读取（未确认）
	_, _ = st.QueueWorkflowFeedback(ctx, QueueFeedbackInput{WorkflowID: "wf", ResponseText: "第二条", UserMessages: []string{"标签甲"}, Images: []model.SessionImage{img1}})
	_, _ = st.QueueWorkflowFeedback(ctx, QueueFeedbackInput{WorkflowID: "wf", ResponseText: "第三条", UserMessages: []string{"标签乙"}, Images: []model.SessionImage{img2}})

	sess, err := st.CreateFeedbackSession(ctx, CreateSessionInput{WorkflowID: "wf", Title: "汇报", Summary: "s", TimeoutSeconds: 60})
	if err != nil {
		t.Fatalf("CreateFeedbackSession: %v", err)
	}
	if sess.Status != "completed" || !sess.ConsumedByAI {
		t.Fatalf("有排队内容时应秒回: %+v", sess)
	}
	if sess.ResponseText != "第二条\n\n第三条" {
		t.Fatalf("应只合并没读到的两条，用空行隔开: %q", sess.ResponseText)
	}
	if len(sess.UserMessages) != 2 || sess.UserMessages[0] != "标签甲" || sess.UserMessages[1] != "标签乙" {
		t.Fatalf("标签应按顺序合并: %v", sess.UserMessages)
	}
	if len(sess.Images) != 2 || sess.Images[0].Name != "1.png" || sess.Images[1].Name != "2.png" {
		t.Fatalf("图片应按顺序合并: %+v", sess.Images)
	}

	var rows []model.QueuedFeedback
	st.DB().Order("id ASC").Find(&rows)
	if len(rows) != 3 {
		t.Fatalf("应有 3 行: %d", len(rows))
	}
	for i, q := range rows {
		if q.Status != "consumed" || q.AckedAt == nil || q.DeliveredAt == nil {
			t.Fatalf("第 %d 行应已送达并确认: %+v", i, q)
		}
	}
	if rows[0].DeliveredVia != InboxViaTool {
		t.Fatalf("先前经 inbox 读到的保留原来的送达路径: %q", rows[0].DeliveredVia)
	}
	if rows[1].DeliveredVia != InboxViaFeedback || rows[2].DeliveredVia != InboxViaFeedback {
		t.Fatalf("随秒回带走的应记为 feedback: %q %q", rows[1].DeliveredVia, rows[2].DeliveredVia)
	}
}

func TestInbox_OnlyDeliveredRowsAreAckedWithoutInstantReply(t *testing.T) {
	st := newInboxStore(t)
	ctx := context.Background()
	queueText(t, st, "wf", "读过了")
	_, _ = st.InboxRead(ctx, "wf", InboxReadOptions{}, time.Now())
	sess, err := st.CreateFeedbackSession(ctx, CreateSessionInput{WorkflowID: "wf", Title: "汇报", Summary: "s", TimeoutSeconds: 60})
	if err != nil {
		t.Fatal(err)
	}
	if sess.Status != "pending" || sess.ResponseText != "" {
		t.Fatalf("只有已读取的追加时不应秒回，要等用户的新回复: %+v", sess)
	}
	var q model.QueuedFeedback
	st.DB().First(&q)
	if q.Status != "consumed" || q.AckedAt == nil {
		t.Fatalf("智能体回来汇报，视为已确认: %+v", q)
	}
}

func TestInbox_LegacyPendingRowStillConsumedAsBefore(t *testing.T) {
	st := newInboxStore(t)
	ctx := context.Background()
	now := time.Now()
	// 旧版本写入的行：没有新列的值
	legacy := model.QueuedFeedback{WorkflowID: "wf", ResponseText: "旧版本时代的暂存", Status: "pending", CreatedAt: now, UpdatedAt: now}
	if err := st.DB().Create(&legacy).Error; err != nil {
		t.Fatal(err)
	}
	items, _ := st.ListInboxItems(ctx, "wf")
	if len(items) != 1 || items[0].State != model.InboxStateQueued {
		t.Fatalf("旧行应按 queued 展示: %+v", items)
	}
	sess, err := st.CreateFeedbackSession(ctx, CreateSessionInput{WorkflowID: "wf", Title: "t", Summary: "s", TimeoutSeconds: 60})
	if err != nil || sess.ResponseText != "旧版本时代的暂存" || sess.Status != "completed" {
		t.Fatalf("旧行应被秒回带走: %+v %v", sess, err)
	}
}

func TestInbox_RevokeAndRetract(t *testing.T) {
	st := newInboxStore(t)
	ctx := context.Background()
	now := time.Now()

	unread := queueText(t, st, "wf", "还没读就撤回")
	res, err := st.RevokeQueuedFeedback(ctx, unread.ID)
	if err != nil || res.Delivered || res.ResponseText != "还没读就撤回" {
		t.Fatalf("读到之前撤回: %+v %v", res, err)
	}
	if got := rowByID(t, st, unread.ID); got.State != model.InboxStateRevoked {
		t.Fatalf("应是 revoked: %+v", got)
	}
	if r, _ := st.InboxRead(ctx, "wf", InboxReadOptions{}, now); !r.Empty() {
		t.Fatalf("没读到就撤回的，智能体什么也不该看到: %+v", r)
	}

	read := queueText(t, st, "wf", "读到之后撤回")
	_, _ = st.InboxRead(ctx, "wf", InboxReadOptions{}, now)
	res, err = st.RevokeQueuedFeedback(ctx, read.ID)
	if err != nil || !res.Delivered {
		t.Fatalf("读到之后撤回应标注 Delivered: %+v %v", res, err)
	}
	if got := rowByID(t, st, read.ID); got.State != model.InboxStateRetracted || got.RetractedAt == nil {
		t.Fatalf("应是 retracted: %+v", got)
	}
	sum, _ := st.InboxSummary(ctx, "wf", now)
	if sum.RetractNotices != 1 {
		t.Fatalf("应有 1 条撤回通知待告知: %+v", sum)
	}
	r, _ := st.InboxRead(ctx, "wf", InboxReadOptions{}, now.Add(time.Second))
	if len(r.Retracted) != 1 || r.Retracted[0].ID != read.ID {
		t.Fatalf("下一次读取应告知撤回: %+v", r)
	}
	r, _ = st.InboxRead(ctx, "wf", InboxReadOptions{}, now.Add(2*time.Second))
	if len(r.Retracted) != 0 {
		t.Fatalf("撤回只告知一次: %+v", r)
	}
	// 撤回后它不会再被秒回带走
	sess, _ := st.CreateFeedbackSession(ctx, CreateSessionInput{WorkflowID: "wf", Title: "t", Summary: "s", TimeoutSeconds: 60})
	if sess.Status != "pending" {
		t.Fatalf("撤回过的不应被秒回带走: %+v", sess)
	}

	// 已确认的不能撤回
	done := queueText(t, st, "wf", "确认过了")
	_, _ = st.InboxRead(ctx, "wf", InboxReadOptions{}, now)
	_, _ = st.InboxAck(ctx, "wf", []uint{done.ID}, now)
	if _, err := st.RevokeQueuedFeedback(ctx, done.ID); err == nil {
		t.Fatal("已确认的不能撤回")
	}
	if _, err := st.RevokeQueuedFeedback(ctx, 999999); err == nil {
		t.Fatal("不存在的编号应报错")
	}
}

// #199：被 hook 提醒过、或被回执提示行带到过的追加，在智能体读到之前就被撤回——智能体下次读收件箱时要被明确告知
// 「这条撤回了，请忽略」（只告知一次、只给编号不给正文）；完全没让智能体知道的撤回不打扰它。
func TestInbox_RetractAfterReminderIsAnnouncedOnce(t *testing.T) {
	ctx := context.Background()
	t0 := time.Now()
	cases := []struct {
		name       string
		prepare    func(t *testing.T, st *Store, wf string)
		wantNotice bool
	}{
		{"被 hook 提醒过", func(t *testing.T, st *Store, wf string) {
			if p, err := st.InboxPeek(ctx, wf, true, t0); err != nil || !p.Remind {
				t.Fatalf("前置：hook 应已提醒: %+v %v", p, err)
			}
		}, true},
		{"被提示行带到过", func(t *testing.T, st *Store, wf string) {
			if wrote, err := st.MarkInboxReceiptHint(ctx, wf, t0); err != nil || !wrote {
				t.Fatalf("前置：提示行应已记账: %v %v", wrote, err)
			}
		}, true},
		{"完全没提醒过", func(t *testing.T, st *Store, wf string) {}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st := newInboxStore(t)
			wf := "wf-retract-unread"
			q := queueText(t, st, wf, "还没读就撤回的正文")
			tc.prepare(t, st, wf)

			res, err := st.RevokeQueuedFeedback(ctx, q.ID)
			if err != nil || res.Delivered {
				t.Fatalf("没读到就撤回，不应标注 Delivered: %+v %v", res, err)
			}
			if res.AgentNotified != tc.wantNotice {
				t.Fatalf("撤回结果的 AgentNotified（会不会告知智能体）期望 %v: %+v", tc.wantNotice, res)
			}
			got := rowByID(t, st, q.ID)
			wantState := model.InboxStateRevoked
			if tc.wantNotice {
				wantState = model.InboxStateRetracted // 智能体知道它存在：「知道之后被撤回」
			}
			if got.State != wantState {
				t.Fatalf("撤回后的状态期望 %s: %+v", wantState, got)
			}
			if (got.RetractedAt != nil) != tc.wantNotice {
				t.Fatalf("retracted_at 是否记下，期望 %v: %+v", tc.wantNotice, got.RetractedAt)
			}
			wantCount := 0
			if tc.wantNotice {
				wantCount = 1
			}
			sum, _ := st.InboxSummary(ctx, wf, t0.Add(time.Second))
			if sum.RetractNotices != wantCount || sum.Unread != 0 {
				t.Fatalf("撤回通知数期望 %d: %+v", wantCount, sum)
			}

			r, err := st.InboxRead(ctx, wf, InboxReadOptions{}, t0.Add(2*time.Second))
			if err != nil {
				t.Fatal(err)
			}
			if !tc.wantNotice {
				if !r.Empty() {
					t.Fatalf("智能体完全不知道的撤回，读取收件箱应什么也没有: %+v", r)
				}
				return
			}
			if len(r.Retracted) != 1 || r.Retracted[0].ID != q.ID || r.Retracted[0].DeliveredAt != nil {
				t.Fatalf("应告知这条（只被提醒过、没读过）已撤回: %+v", r)
			}
			if r, _ = st.InboxRead(ctx, wf, InboxReadOptions{}, t0.Add(3*time.Second)); len(r.Retracted) != 0 {
				t.Fatalf("撤回只告知一次: %+v", r)
			}
			if sum, _ = st.InboxSummary(ctx, wf, t0.Add(4*time.Second)); sum.RetractNotices != 0 {
				t.Fatalf("告知之后撤回计数应清零: %+v", sum)
			}
		})
	}
}

// #199：撤回之后 hook 也提醒一次（读过的与只被提醒过的一样），但不占用未读的提醒次数、不会重复；
// 提醒与告知都不能改写 updated_at——聊天里「你撤回了一条消息」拿它当撤回的时间。
func TestInbox_RetractReminderByHookIsOnceAndKeepsRetractionTime(t *testing.T) {
	st := newInboxStore(t)
	ctx := context.Background()
	wf := "wf-retract-hook"
	t0 := time.Now()

	notified := queueText(t, st, wf, "提醒过但没读")
	if p, _ := st.InboxPeek(ctx, wf, true, t0); !p.Remind {
		t.Fatalf("前置：应已提醒: %+v", p)
	}
	if _, err := st.RevokeQueuedFeedback(ctx, notified.ID); err != nil {
		t.Fatal(err)
	}
	revokedAt := rowByID(t, st, notified.ID).UpdatedAt

	p, err := st.InboxPeek(ctx, wf, true, t0.Add(10*time.Second))
	if err != nil || !p.Remind || p.RetractNotices != 1 || p.Unread != 0 {
		t.Fatalf("撤回之后 hook 应提醒一次: %+v %v", p, err)
	}
	row := rowByID(t, st, notified.ID)
	if row.NotifiedCount != 1 {
		t.Fatalf("撤回提醒不占未读的提醒次数: %d", row.NotifiedCount)
	}
	if !row.UpdatedAt.Equal(revokedAt) {
		t.Fatalf("hook 提醒不能改写撤回时间: %v → %v", revokedAt, row.UpdatedAt)
	}
	if p, _ = st.InboxPeek(ctx, wf, true, t0.Add(20*time.Second)); p.Remind || p.RetractNotices != 1 {
		t.Fatalf("同一次撤回不重复提醒（读收件箱之前计数仍在）: %+v", p)
	}
	if p, _ = st.InboxPeek(ctx, wf, false, t0.Add(25*time.Second)); p.Remind {
		t.Fatalf("只读查询从不提醒: %+v", p)
	}

	r, _ := st.InboxRead(ctx, wf, InboxReadOptions{}, t0.Add(30*time.Second))
	if len(r.Retracted) != 1 || r.Retracted[0].ID != notified.ID {
		t.Fatalf("读取应告知撤回: %+v", r)
	}
	if got := rowByID(t, st, notified.ID); !got.UpdatedAt.Equal(revokedAt) {
		t.Fatalf("告知撤回不能改写撤回时间: %v → %v", revokedAt, got.UpdatedAt)
	}
	if p, _ = st.InboxPeek(ctx, wf, true, t0.Add(40*time.Second)); p.Remind || p.RetractNotices != 0 {
		t.Fatalf("告知之后 hook 不再提醒: %+v", p)
	}

	// 读到全文之后被撤回的（旧行为）同样提醒一次
	read := queueText(t, st, wf, "读过再撤回")
	_, _ = st.InboxRead(ctx, wf, InboxReadOptions{}, t0.Add(50*time.Second))
	if _, err := st.RevokeQueuedFeedback(ctx, read.ID); err != nil {
		t.Fatal(err)
	}
	if p, _ = st.InboxPeek(ctx, wf, true, t0.Add(60*time.Second)); !p.Remind || p.RetractNotices != 1 {
		t.Fatalf("读过之后被撤回也应由 hook 提醒一次: %+v", p)
	}
	if p, _ = st.InboxPeek(ctx, wf, true, t0.Add(70*time.Second)); p.Remind {
		t.Fatalf("不重复提醒: %+v", p)
	}
}

// #199：历史查询里的撤回记录——只有「智能体读过或被提醒过」的撤回，从早到晚、只有编号与时间，按工作流隔离。
func TestInbox_ListRetractionEvents(t *testing.T) {
	st := newInboxStore(t)
	ctx := context.Background()
	t0 := time.Now()

	if _, err := st.ListRetractionEvents(ctx, ""); err == nil {
		t.Fatal("没有 workflow_id 应被拒绝")
	}
	if evs, err := st.ListRetractionEvents(ctx, "wf-none"); err != nil || len(evs) != 0 {
		t.Fatalf("没有撤回时应为空: %+v %v", evs, err)
	}

	read := queueText(t, st, "wf", "读过")
	_, _ = st.InboxRead(ctx, "wf", InboxReadOptions{}, t0)
	if _, err := st.RevokeQueuedFeedback(ctx, read.ID); err != nil {
		t.Fatal(err)
	}
	notified := queueText(t, st, "wf", "只被提醒")
	_, _ = st.InboxPeek(ctx, "wf", true, t0.Add(time.Second))
	if _, err := st.RevokeQueuedFeedback(ctx, notified.ID); err != nil {
		t.Fatal(err)
	}
	unknown := queueText(t, st, "wf", "完全不知道")
	if _, err := st.RevokeQueuedFeedback(ctx, unknown.ID); err != nil {
		t.Fatal(err)
	}
	other := queueText(t, st, "wf-other", "别的工作流")
	_, _ = st.InboxPeek(ctx, "wf-other", true, t0.Add(time.Second))
	if _, err := st.RevokeQueuedFeedback(ctx, other.ID); err != nil {
		t.Fatal(err)
	}
	_ = queueText(t, st, "wf", "还在排队")

	evs, err := st.ListRetractionEvents(ctx, "wf")
	if err != nil || len(evs) != 2 {
		t.Fatalf("应只有读过的与被提醒过的两条: %+v %v", evs, err)
	}
	if evs[0].ID != read.ID || !evs[0].Read || evs[1].ID != notified.ID || evs[1].Read {
		t.Fatalf("应从早到晚、并标明读没读过: %+v", evs)
	}
	for _, ev := range evs {
		if ev.RetractedAt.IsZero() {
			t.Fatalf("撤回记录要带时间: %+v", ev)
		}
	}

	// 数量上限：只留最近的
	var ids []uint
	for i := 0; i < RetractionEventsMax+3; i++ {
		q := queueText(t, st, "wf-many", fmt.Sprintf("第 %d 条", i))
		ids = append(ids, q.ID)
		_, _ = st.InboxRead(ctx, "wf-many", InboxReadOptions{}, t0.Add(time.Duration(i)*time.Second))
		if _, err := st.RevokeQueuedFeedback(ctx, q.ID); err != nil {
			t.Fatal(err)
		}
	}
	many, err := st.ListRetractionEvents(ctx, "wf-many")
	if err != nil || len(many) != RetractionEventsMax {
		t.Fatalf("应最多 %d 条: %d %v", RetractionEventsMax, len(many), err)
	}
	for i := range many {
		if want := ids[len(ids)-RetractionEventsMax+i]; many[i].ID != want {
			t.Fatalf("应是最近的 %d 条、从早到晚：第 %d 项期望 #%d，得到 #%d", RetractionEventsMax, i, want, many[i].ID)
		}
	}
}

func TestInbox_ExpiredRowsAreNeitherDeliveredNorConsumed(t *testing.T) {
	st := newInboxStore(t)
	ctx := context.Background()
	old := queueText(t, st, "wf", "八天前的")
	if err := st.DB().Model(&model.QueuedFeedback{}).Where("id = ?", old.ID).
		Update("created_at", time.Now().Add(-8*24*time.Hour)).Error; err != nil {
		t.Fatal(err)
	}
	fresh := queueText(t, st, "wf", "新的")

	sum, _ := st.InboxSummary(ctx, "wf", time.Now())
	if sum.Unread != 1 {
		t.Fatalf("过期的不计入未读: %+v", sum)
	}
	if got := rowByID(t, st, old.ID); got.State != model.InboxStateExpired {
		t.Fatalf("应标为 expired: %+v", got)
	}
	sess, _ := st.CreateFeedbackSession(ctx, CreateSessionInput{WorkflowID: "wf", Title: "t", Summary: "s", TimeoutSeconds: 60})
	if sess.ResponseText != "新的" {
		t.Fatalf("只应带走没过期的: %q", sess.ResponseText)
	}
	_ = fresh
}

func TestInbox_ReminderSchedule(t *testing.T) {
	st := newInboxStore(t)
	ctx := context.Background()
	q := queueText(t, st, "wf", "需要被提醒")
	t0 := time.Now()

	step := func(at time.Duration, wantRemind bool, wantCount int) {
		t.Helper()
		p, err := st.InboxPeek(ctx, "wf", true, t0.Add(at))
		if err != nil {
			t.Fatal(err)
		}
		if p.Remind != wantRemind {
			t.Fatalf("t+%v: remind=%v，期望 %v", at, p.Remind, wantRemind)
		}
		if got := rowByID(t, st, q.ID); got.NotifiedCount != wantCount {
			t.Fatalf("t+%v: 提醒次数=%d，期望 %d", at, got.NotifiedCount, wantCount)
		}
	}
	step(0, true, 1)               // 第 1 次：立即
	step(15*time.Second, false, 1) // 节流窗口内的重复查询不再提醒
	step(4*time.Minute, false, 1)  // 还不到 5 分钟
	step(5*time.Minute+time.Second, true, 2)
	step(10*time.Minute, false, 2) // 距上一次不到 15 分钟
	step(21*time.Minute, true, 3)  // 距上一次 ≥ 15 分钟
	step(2*time.Hour, false, 3)    // 最多 3 次
	step(3*time.Hour, false, 3)

	// 只读模式从不记账
	q2 := queueText(t, st, "wf2", "x")
	if p, _ := st.InboxPeek(ctx, "wf2", false, t0); p.Remind || rowByID(t, st, q2.ID).NotifiedCount != 0 {
		t.Fatal("reminder=false 应纯只读")
	}
	// 没有未读：不提醒
	if p, _ := st.InboxPeek(ctx, "wf-empty", true, t0); p.Remind || p.Unread != 0 {
		t.Fatalf("空收件箱不应提醒: %+v", p)
	}
	// 已读的不再提醒
	_, _ = st.InboxRead(ctx, "wf2", InboxReadOptions{}, t0)
	if p, _ := st.InboxPeek(ctx, "wf2", true, t0.Add(time.Hour)); p.Remind {
		t.Fatalf("读过的不再提醒: %+v", p)
	}
}

func TestInbox_DeliveredViaAttribution(t *testing.T) {
	st := newInboxStore(t)
	ctx := context.Background()
	t0 := time.Now()
	read := func(wf string, at time.Duration) model.QueuedFeedback {
		t.Helper()
		r, err := st.InboxRead(ctx, wf, InboxReadOptions{}, t0.Add(at))
		if err != nil || len(r.New) != 1 {
			t.Fatalf("读取: %v %+v", err, r)
		}
		return r.New[0]
	}

	queueText(t, st, "w-hook", "x")
	_, _ = st.InboxPeek(ctx, "w-hook", true, t0)
	if q := read("w-hook", 2*time.Minute); q.DeliveredVia != InboxViaHook {
		t.Fatalf("hook 提醒后读取应记为 hook: %q", q.DeliveredVia)
	}

	queueText(t, st, "w-receipt", "x")
	_, _ = st.MarkInboxReceiptHint(ctx, "w-receipt", t0)
	if q := read("w-receipt", time.Minute); q.DeliveredVia != InboxViaReceipt {
		t.Fatalf("回执提示后读取应记为 receipt: %q", q.DeliveredVia)
	}

	queueText(t, st, "w-tool", "x")
	if q := read("w-tool", time.Minute); q.DeliveredVia != InboxViaTool {
		t.Fatalf("没收到过提醒就读取应记为 tool: %q", q.DeliveredVia)
	}

	queueText(t, st, "w-stale", "x")
	_, _ = st.InboxPeek(ctx, "w-stale", true, t0)
	if q := read("w-stale", 45*time.Minute); q.DeliveredVia != InboxViaTool {
		t.Fatalf("提醒已过去 30 分钟以上不再归因: %q", q.DeliveredVia)
	}

	queueText(t, st, "w-both", "x")
	_, _ = st.InboxPeek(ctx, "w-both", true, t0)
	_, _ = st.MarkInboxReceiptHint(ctx, "w-both", t0.Add(5*time.Minute))
	if q := read("w-both", 6*time.Minute); q.DeliveredVia != InboxViaReceipt {
		t.Fatalf("两条路径都提醒过，取最近的: %q", q.DeliveredVia)
	}
}

func TestInbox_ReceiptHintMarkIsThrottled(t *testing.T) {
	st := newInboxStore(t)
	ctx := context.Background()
	q := queueText(t, st, "wf", "x")
	t0 := time.Now()
	if wrote, err := st.MarkInboxReceiptHint(ctx, "wf", t0); err != nil || !wrote {
		t.Fatalf("第一次应写库: %v %v", wrote, err)
	}
	first := rowByID(t, st, q.ID).ReceiptHintAt
	if wrote, _ := st.MarkInboxReceiptHint(ctx, "wf", t0.Add(10*time.Second)); wrote {
		t.Fatal("一分钟内不应反复写库")
	}
	if got := rowByID(t, st, q.ID).ReceiptHintAt; got == nil || !got.Equal(*first) {
		t.Fatalf("一分钟内时间戳不应变: %v vs %v", got, first)
	}
	if wrote, _ := st.MarkInboxReceiptHint(ctx, "wf", t0.Add(2*time.Minute)); !wrote {
		t.Fatal("过了一分钟应更新")
	}
	if got := rowByID(t, st, q.ID).ReceiptHintAt; got == nil || got.Equal(*first) {
		t.Fatal("过了一分钟时间戳应更新")
	}
	if wrote, err := st.MarkInboxReceiptHint(ctx, "no-such-workflow", t0); err != nil || wrote {
		t.Fatalf("没有未读的工作流不应写库: %v %v", wrote, err)
	}
}

func TestInbox_ConcurrentReadAndFeedbackDeliverEachMessageExactlyOnce(t *testing.T) {
	st := newInboxStore(t)
	ctx := context.Background()
	const n = 24
	want := map[string]bool{}
	for i := 0; i < n; i++ {
		text := "消息-" + string(rune('A'+i))
		want[text] = true
		queueText(t, st, "wf", text)
	}

	var mu sync.Mutex
	got := map[string]int{}
	record := func(text string) {
		mu.Lock()
		defer mu.Unlock()
		if text == "" {
			return
		}
		for _, line := range strings.Split(text, "\n\n") {
			got[line]++
		}
	}

	var wg sync.WaitGroup
	for i := 0; i < 6; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			r, err := st.InboxRead(ctx, "wf", InboxReadOptions{}, time.Now())
			if err != nil {
				t.Errorf("InboxRead: %v", err)
				return
			}
			for _, q := range r.New {
				record(q.ResponseText)
			}
		}()
		go func() {
			defer wg.Done()
			sess, err := st.CreateFeedbackSession(ctx, CreateSessionInput{WorkflowID: "wf", Title: "t", Summary: "s", TimeoutSeconds: 60})
			if err != nil {
				t.Errorf("CreateFeedbackSession: %v", err)
				return
			}
			if sess.Status == "completed" {
				record(sess.ResponseText)
			}
		}()
	}
	wg.Wait()

	for text := range want {
		if got[text] != 1 {
			t.Fatalf("%q 应恰好送达一次，实际 %d 次（全部: %v）", text, got[text], got)
		}
	}
	if len(got) != n {
		t.Fatalf("多出了不该有的内容: %v", got)
	}
}

func TestInbox_PreviewAndStates(t *testing.T) {
	long := strings.Repeat("长", 200)
	cases := []struct {
		name string
		q    model.QueuedFeedback
		want string
	}{
		{"折叠空白", model.QueuedFeedback{ResponseText: "  第一行\n\n  第二行\t尾 "}, "第一行 第二行 尾"},
		{"截断", model.QueuedFeedback{ResponseText: long}, strings.Repeat("长", InboxPreviewRunes) + "…"},
		{"只有标签", model.QueuedFeedback{UserMessages: []string{"继续", "加油"}}, "继续 加油"},
		{"只有图片", model.QueuedFeedback{Images: []model.SessionImage{{}, {}}}, "（2 张图片）"},
		{"文字加图片", model.QueuedFeedback{ResponseText: "看图", Images: []model.SessionImage{{}}}, "看图（附 1 张图片）"},
	}
	for _, c := range cases {
		if got := InboxPreview(c.q); got != c.want {
			t.Errorf("%s: %q，期望 %q", c.name, got, c.want)
		}
	}

	now := time.Now()
	states := []struct {
		q    model.QueuedFeedback
		want string
	}{
		{model.QueuedFeedback{}, model.InboxStateQueued},
		{model.QueuedFeedback{Status: "pending"}, model.InboxStateQueued},
		{model.QueuedFeedback{Status: "pending", DeliveredAt: &now}, model.InboxStateDelivered},
		{model.QueuedFeedback{Status: "consumed"}, model.InboxStateAcked},
		{model.QueuedFeedback{Status: "revoked"}, model.InboxStateRevoked},
		{model.QueuedFeedback{Status: "revoked", DeliveredAt: &now}, model.InboxStateRetracted},
		// #199：被 hook / 提示行提醒过、没读就撤回的，记了 RetractedAt，也算「知道之后被撤回」
		{model.QueuedFeedback{Status: "revoked", RetractedAt: &now}, model.InboxStateRetracted},
		{model.QueuedFeedback{Status: "expired"}, model.InboxStateExpired},
	}
	for _, c := range states {
		if got := c.q.DeriveState(); got != c.want {
			t.Errorf("status=%q delivered=%v → %s，期望 %s", c.q.Status, c.q.DeliveredAt != nil, got, c.want)
		}
	}
}

func TestInboxConfig_DefaultsOverrideAndCacheInvalidation(t *testing.T) {
	st := newInboxStore(t)
	ctx := context.Background()
	cfg := st.GetInboxConfig(ctx)
	if cfg != DefaultInboxConfig() || !cfg.Enabled || !cfg.HintOn() || !cfg.HooksOn() || !cfg.ClauseOn() {
		t.Fatalf("默认应全部打开: %+v", cfg)
	}
	if env := cfg.Effective(true); env.Enabled || env.HintOn() || env.HooksOn() || env.ClauseOn() {
		t.Fatalf("环境变量总闸应整体关闭: %+v", env)
	}

	next, err := st.UpdateInboxConfig(ctx, func(c InboxConfig) InboxConfig {
		c.ReceiptHint = false
		return c
	})
	if err != nil || next.ReceiptHint || !next.Enabled {
		t.Fatalf("更新: %+v %v", next, err)
	}
	// 缓存要立即失效
	if got := st.GetInboxConfig(ctx); got.ReceiptHint || got.HintOn() || !got.HooksOn() {
		t.Fatalf("改完应立刻生效: %+v", got)
	}
	_, _ = st.UpdateInboxConfig(ctx, func(c InboxConfig) InboxConfig { c.Enabled = false; return c })
	if got := st.GetInboxConfig(ctx); got.Enabled || got.HooksOn() || got.ClauseOn() {
		t.Fatalf("总开关关闭后各层都应关: %+v", got)
	}
}

func TestAgentActivity(t *testing.T) {
	st := newInboxStore(t)
	if _, ok := st.AgentLastActivity("wf"); ok {
		t.Fatal("没记录过应返回 false")
	}
	t0 := time.Now()
	st.RecordAgentActivity("wf", t0)
	st.RecordAgentActivity("", t0) // 空工作流忽略
	if got, ok := st.AgentLastActivity("wf"); !ok || !got.Equal(t0) {
		t.Fatalf("应记下: %v %v", got, ok)
	}
	st.RecordAgentActivity("wf", t0.Add(time.Minute))
	if got, _ := st.AgentLastActivity("wf"); !got.Equal(t0.Add(time.Minute)) {
		t.Fatalf("应更新为最新: %v", got)
	}
}

func TestHookSeen(t *testing.T) {
	st := newInboxStore(t)
	if _, ok := st.HookLastSeen("wf"); ok {
		t.Fatal("没记录过应返回 false")
	}
	t0 := time.Now()
	if !st.RecordHookSeen("wf", t0) {
		t.Fatal("第一次记录应通知 Web 刷新")
	}
	if st.RecordHookSeen("", t0) {
		t.Fatal("空工作流忽略，不通知")
	}
	if got, ok := st.HookLastSeen("wf"); !ok || !got.Equal(t0) {
		t.Fatalf("应记下: %v %v", got, ok)
	}
	// 一分钟内再来：时间照常更新，但不再通知 Web（hook 每十几秒问一次，不必每次都让所有页面重新拉取）
	if st.RecordHookSeen("wf", t0.Add(InboxHookSeenBroadcastEvery-time.Second)) {
		t.Fatal("间隔不足一分钟不应再通知")
	}
	if got, _ := st.HookLastSeen("wf"); !got.Equal(t0.Add(InboxHookSeenBroadcastEvery - time.Second)) {
		t.Fatalf("时间应更新为最新: %v", got)
	}
	if !st.RecordHookSeen("wf", t0.Add(3*InboxHookSeenBroadcastEvery)) {
		t.Fatal("间隔够了应再通知")
	}
	// 与「智能体活动」互不影响
	if _, ok := st.AgentLastActivity("wf"); ok {
		t.Fatal("hook 查询不应记成智能体活动")
	}
	st.RecordAgentActivity("wf2", t0)
	if _, ok := st.HookLastSeen("wf2"); ok {
		t.Fatal("智能体活动不应记成 hook 查询")
	}
}

func TestInbox_SummaryCountsImagesWithoutLoadingThem(t *testing.T) {
	st := newInboxStore(t)
	ctx := context.Background()
	imgs := []model.SessionImage{{Name: "a.png", Data: "AAAA"}, {Name: "b.png", Data: "BBBB"}}
	if _, err := st.QueueWorkflowFeedback(ctx, QueueFeedbackInput{WorkflowID: "wf", Images: imgs}); err != nil {
		t.Fatal(err)
	}
	sum, err := st.InboxSummary(ctx, "wf", time.Now())
	if err != nil || sum.Unread != 1 {
		t.Fatalf("概况: %+v %v", sum, err)
	}
	if sum.LatestPreview != "（2 张图片）" {
		t.Fatalf("只有图片时预览应注明张数，得到 %q", sum.LatestPreview)
	}
	if _, err := st.QueueWorkflowFeedback(ctx, QueueFeedbackInput{WorkflowID: "wf", ResponseText: "看这两张", Images: imgs}); err != nil {
		t.Fatal(err)
	}
	sum, _ = st.InboxSummary(ctx, "wf", time.Now())
	if sum.Unread != 2 || sum.LatestPreview != "看这两张（附 2 张图片）" {
		t.Fatalf("文字加图片的预览不对: %+v", sum)
	}
	if _, err := st.QueueWorkflowFeedback(ctx, QueueFeedbackInput{WorkflowID: "wf", ResponseText: "纯文字"}); err != nil {
		t.Fatal(err)
	}
	sum, _ = st.InboxSummary(ctx, "wf", time.Now())
	if sum.LatestPreview != "纯文字" {
		t.Fatalf("没有图片时不应出现图片提示: %q", sum.LatestPreview)
	}
}

func TestInbox_AckedItemsKeepNoImageDataInWebListing(t *testing.T) {
	st := newInboxStore(t)
	ctx := context.Background()
	q, err := st.QueueWorkflowFeedback(ctx, QueueFeedbackInput{WorkflowID: "wf", ResponseText: "带图", Images: []model.SessionImage{{Name: "a.png", Data: "AAAA"}}})
	if err != nil {
		t.Fatal(err)
	}
	items, _ := st.ListInboxItems(ctx, "wf")
	if len(items) != 1 || items[0].Images[0].Data != "AAAA" {
		t.Fatalf("排队中的条目应带图片数据（界面要显示缩略图）: %+v", items)
	}
	now := time.Now()
	if _, err := st.InboxRead(ctx, "wf", InboxReadOptions{}, now); err != nil {
		t.Fatal(err)
	}
	if _, err := st.InboxAck(ctx, "wf", nil, now); err != nil {
		t.Fatal(err)
	}
	items, _ = st.ListInboxItems(ctx, "wf")
	if len(items) != 1 || items[0].State != model.InboxStateAcked || len(items[0].Images) != 1 || items[0].Images[0].Data != "" {
		t.Fatalf("已确认的条目应保留张数但不带图片数据: %+v", items)
	}
	// 库里的原始数据不受影响，仍可按编号取回
	full, err := st.GetQueuedFeedback(ctx, q.ID)
	if err != nil || full == nil || full.Images[0].Data != "AAAA" {
		t.Fatalf("按编号应能取回完整图片: %+v %v", full, err)
	}
	if none, err := st.GetQueuedFeedback(ctx, 99999); err != nil || none != nil {
		t.Fatalf("不存在的编号应返回 nil: %+v %v", none, err)
	}
}

// 追加信息在会话页里是对话中的永久气泡：经 hook / 提示行 / 主动读取送达的，对话记录里没有它们，
// 列表里不能因为过了一天就消失；随「秒回」带走的则已在对话记录里，仍不重复展示。
func TestInbox_ListKeepsNonFeedbackDeliveredItemsPastOneDay(t *testing.T) {
	st := newInboxStore(t)
	ctx := context.Background()
	old := time.Now().Add(-72 * time.Hour)
	mk := func(text, via string) {
		t.Helper()
		q := model.QueuedFeedback{
			WorkflowID: "wf", ResponseText: text, Status: "consumed", Kind: "append",
			DeliveredAt: &old, DeliveredVia: via, AckedAt: &old, CreatedAt: old, UpdatedAt: old,
		}
		if err := st.DB().Create(&q).Error; err != nil {
			t.Fatal(err)
		}
	}
	mk("hook 送达的", InboxViaHook)
	mk("提示行送达的", InboxViaReceipt)
	mk("主动读取的", InboxViaTool)
	mk("随秒回带走的", InboxViaFeedback)

	items, err := st.ListInboxItems(ctx, "wf")
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, q := range items {
		got = append(got, q.ResponseText)
		if q.State != model.InboxStateAcked {
			t.Fatalf("应都是 acked: %+v", q)
		}
	}
	if len(got) != 3 || got[0] != "hook 送达的" || got[1] != "提示行送达的" || got[2] != "主动读取的" {
		t.Fatalf("三天前经 hook / 提示行 / 主动读取送达的应仍在列表里（按时间顺序），随秒回带走的不应出现: %v", got)
	}
}

// 过期的追加（保留期内一直没被读到）留一条记录说明智能体始终没看到它；图片数据不再内联。
func TestInbox_ListShowsExpiredAsRecordWithoutImageData(t *testing.T) {
	st := newInboxStore(t)
	ctx := context.Background()
	created := time.Now().Add(-InboxExpireAfter - time.Hour)
	q := model.QueuedFeedback{
		WorkflowID: "wf", ResponseText: "一直没被读到", Status: "pending", Kind: "append",
		Images: model.SessionImages{{Name: "a.png", Data: "AAAA"}}, CreatedAt: created, UpdatedAt: created,
	}
	if err := st.DB().Create(&q).Error; err != nil {
		t.Fatal(err)
	}
	items, err := st.ListInboxItems(ctx, "wf")
	if err != nil || len(items) != 1 {
		t.Fatalf("过期的应留一条记录: %+v %v", items, err)
	}
	if items[0].State != model.InboxStateExpired || len(items[0].Images) != 1 || items[0].Images[0].Data != "" {
		t.Fatalf("应是 expired，保留图片张数但不带数据: %+v", items[0])
	}
	// 不带数据，但图片要继续能看：给出按需取图的直链（inbox-<编号> + 内容哈希）。
	img := items[0].Images[0]
	wantURL := model.BuildSessionImageRelativeURL(InboxSessionID(items[0].ID), 0, img.Hash)
	if img.Hash == "" || img.URL != wantURL || !strings.HasPrefix(img.URL, "/api/v1/sessions/inbox-") {
		t.Fatalf("过期条目的图片应带直链与哈希: %+v", img)
	}
}

// 回归（追加指令 #44）：智能体读取并确认之后，Web 上追加信息里的图片不能消失——
// 列表里不再内联 base64，但每张图都要有可直接加载的直链；未确认的条目仍然内联（行为不变）。
func TestInbox_ListAckedItemsKeepImagesViaURL(t *testing.T) {
	st := newInboxStore(t)
	ctx := context.Background()
	now := time.Now().Add(-time.Minute)
	acked := model.QueuedFeedback{
		WorkflowID: "wf", ResponseText: "已读的带图追加", Status: "consumed", Kind: "append",
		Images: model.SessionImages{
			{Name: "a.png", Format: "png", Data: "data:image/png;base64,AAAA"},
			{Name: "b.png", Format: "png", Data: "BBBB", Hash: "abc123"},
		},
		DeliveredAt: &now, DeliveredVia: InboxViaHook, AckedAt: &now, CreatedAt: now, UpdatedAt: now,
	}
	if err := st.DB().Create(&acked).Error; err != nil {
		t.Fatal(err)
	}
	queued := model.QueuedFeedback{
		WorkflowID: "wf", ResponseText: "还没被读的带图追加", Status: "pending", Kind: "append",
		Images: model.SessionImages{{Name: "c.png", Format: "png", Data: "CCCC"}}, CreatedAt: now.Add(time.Second), UpdatedAt: now,
	}
	if err := st.DB().Create(&queued).Error; err != nil {
		t.Fatal(err)
	}

	items, err := st.ListInboxItems(ctx, "wf")
	if err != nil || len(items) != 2 {
		t.Fatalf("应有两条: %+v %v", items, err)
	}
	a, q := items[0], items[1]
	if a.State != model.InboxStateAcked || len(a.Images) != 2 {
		t.Fatalf("第一条应是已确认且保留两张图: %+v", a)
	}
	for i, img := range a.Images {
		if img.Data != "" {
			t.Fatalf("已确认条目不应内联图片数据: #%d %+v", i, img)
		}
		want := "/api/v1/sessions/" + InboxSessionID(a.ID) + "/images/" + strconv.Itoa(i) + "?hash=" + img.Hash
		if img.Hash == "" || img.URL != want {
			t.Fatalf("已确认条目的第 %d 张图应有直链 %q: %+v", i, want, img)
		}
	}
	if a.Images[1].Hash != "abc123" {
		t.Fatalf("已有哈希应原样保留: %+v", a.Images[1])
	}
	if q.State == model.InboxStateAcked || q.Images[0].Data != "CCCC" || q.Images[0].URL != "" {
		t.Fatalf("未确认条目的图片应仍然内联: %+v", q)
	}
	// 直链指向的图片接口按编号取回的仍是完整数据。
	full, err := st.GetQueuedFeedback(ctx, a.ID)
	if err != nil || full == nil || full.Images[0].Data != "data:image/png;base64,AAAA" {
		t.Fatalf("按编号应能取回完整图片: %+v %v", full, err)
	}
}

// 列表的上限要足以回看整个工作流的历史；超出时丢掉最早的、保留最近的，并保持从早到晚的顺序。
func TestInbox_ListCapKeepsMostRecentInOrder(t *testing.T) {
	st := newInboxStore(t)
	ctx := context.Background()
	total := InboxListMax + 5
	base := time.Now().Add(-time.Duration(total) * time.Minute)
	for i := 0; i < total; i++ {
		at := base.Add(time.Duration(i) * time.Minute)
		q := model.QueuedFeedback{
			WorkflowID: "wf", ResponseText: fmt.Sprintf("msg-%03d", i), Status: "consumed", Kind: "append",
			DeliveredAt: &at, DeliveredVia: InboxViaHook, AckedAt: &at, CreatedAt: at, UpdatedAt: at,
		}
		if err := st.DB().Create(&q).Error; err != nil {
			t.Fatal(err)
		}
	}
	items, err := st.ListInboxItems(ctx, "wf")
	if err != nil || len(items) != InboxListMax {
		t.Fatalf("应返回 %d 条: %d %v", InboxListMax, len(items), err)
	}
	if items[0].ResponseText != "msg-005" || items[len(items)-1].ResponseText != fmt.Sprintf("msg-%03d", total-1) {
		t.Fatalf("应丢掉最早的 5 条、保留最近的，且从早到晚: 首 %q 末 %q", items[0].ResponseText, items[len(items)-1].ResponseText)
	}
	if InboxListMax < InboxMaxPending {
		t.Fatalf("上限不应小于单个工作流的未确认上限 InboxMaxPending(%d)：%d", InboxMaxPending, InboxListMax)
	}
}

func TestInboxSessionID_RoundTrip(t *testing.T) {
	if got := InboxSessionID(12); got != "inbox-12" {
		t.Fatalf("InboxSessionID = %q", got)
	}
	for in, want := range map[string]uint{"inbox-12": 12, " inbox-7 ": 7, "inbox-1": 1} {
		if id, ok := ParseInboxSessionID(in); !ok || id != want {
			t.Fatalf("Parse(%q) = %d,%v", in, id, ok)
		}
	}
	for _, bad := range []string{"", "inbox-", "inbox-0", "inbox-x", "inbox--1", "inbox-1a", "sess-12", "inbox-99999999999999999999999"} {
		if id, ok := ParseInboxSessionID(bad); ok {
			t.Fatalf("Parse(%q) 不应成功，得到 %d", bad, id)
		}
	}
}
