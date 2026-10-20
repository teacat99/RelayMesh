package store

import (
	"context"
	"testing"
	"time"

	"github.com/teacat99/RelayMesh/v2/internal/model"
)

// seedReplySession 直接写库造一条会话：显式给编号、种类与创建时间。
// 不走 CreateFeedbackSession，是因为它会复用 pending 轮次，没法稳定造出「第 1 轮、通知、第 2 轮」这样的序列。
func seedReplySession(t *testing.T, st *Store, id, workflowID, kind string, at time.Time) {
	t.Helper()
	row := model.FeedbackSession{
		ID: id, WorkflowID: workflowID, Kind: kind,
		Title: "标题-" + id, Summary: "摘要-" + id, Status: "completed",
		CreatedAt: at, UpdatedAt: at,
	}
	if err := st.db.Create(&row).Error; err != nil {
		t.Fatalf("写入会话 %s 失败: %v", id, err)
	}
}

// 回复哈希（会话编号 sess- 之后的十六进制）只在同一个工作流里解析：认得出轮次序号，通知不占轮次，别的工作流的同名哈希不可见。
func TestFindReplyRefsResolvesWithinWorkflowOnly(t *testing.T) {
	st := newConfirmStore(t)
	ctx := context.Background()
	base := time.Now().Add(-time.Hour)
	seedReplySession(t, st, "sess-aaaa0001", "wf-r", "", base)                                        // 第 1 轮
	seedReplySession(t, st, "sess-bbbb0002", "wf-r", model.FeedbackKindNotice, base.Add(time.Minute)) // 通知：不占轮次
	seedReplySession(t, st, "sess-cccc0003", "wf-r", "", base.Add(2*time.Minute))                     // 第 2 轮（不能因为中间有通知变成第 3 轮）
	seedReplySession(t, st, "sess-dddd0004", "wf-other", "", base.Add(3*time.Minute))                 // 别的工作流

	got, err := st.FindReplyRefs(ctx, "wf-r", "cccc0003", 5)
	if err != nil || len(got) != 1 {
		t.Fatalf("应解析出 1 条：%+v %v", got, err)
	}
	if got[0].Session.ID != "sess-cccc0003" || got[0].Turn != 2 || got[0].IsNotice() {
		t.Fatalf("第二轮的轮次序号应是 2（通知不占轮次）：%+v", got[0])
	}

	got, err = st.FindReplyRefs(ctx, "wf-r", "bbbb0002", 5)
	if err != nil || len(got) != 1 || !got[0].IsNotice() || got[0].Turn != 0 {
		t.Fatalf("通知应被识别为通知且没有轮次序号：%+v %v", got, err)
	}

	got, err = st.FindReplyRefs(ctx, "wf-r", "aaaa0001", 5)
	if err != nil || len(got) != 1 || got[0].Turn != 1 {
		t.Fatalf("第一轮序号应是 1：%+v %v", got, err)
	}

	// 大小写不敏感，也容忍把 sess- 前缀一起写上
	for _, h := range []string{"CCCC0003", "sess-cccc0003", " Cccc0003 "} {
		got, err = st.FindReplyRefs(ctx, "wf-r", h, 5)
		if err != nil || len(got) != 1 || got[0].Session.ID != "sess-cccc0003" {
			t.Fatalf("哈希 %q 应能解析到第二轮：%+v %v", h, got, err)
		}
	}

	// 别的工作流里的会话：从本工作流看不见（不能靠哈希窥探别的工作流）
	got, err = st.FindReplyRefs(ctx, "wf-r", "dddd0004", 5)
	if err != nil || len(got) != 0 {
		t.Fatalf("不该跨工作流解析：%+v %v", got, err)
	}
	got, err = st.FindReplyRefs(ctx, "wf-other", "dddd0004", 5)
	if err != nil || len(got) != 1 || got[0].Turn != 1 {
		t.Fatalf("在它自己的工作流里应能解析且是第 1 轮：%+v %v", got, err)
	}
}

// 非法输入一律当作没有引用：太短、非十六进制、带 LIKE 通配符的都不能进到查询里。
func TestFindReplyRefsRejectsMalformedHashes(t *testing.T) {
	st := newConfirmStore(t)
	ctx := context.Background()
	seedReplySession(t, st, "sess-abcdef01", "wf-r", "", time.Now().Add(-time.Minute))

	for _, h := range []string{"", "abc", "abcde", "zzzzzz", "abcdef0123456", "ab%def", "abcd_f01", "abcdef01; DROP TABLE feedback_sessions"} {
		got, err := st.FindReplyRefs(ctx, "wf-r", h, 5)
		if err != nil || len(got) != 0 {
			t.Fatalf("非法哈希 %q 应返回空且不报错：%+v %v", h, got, err)
		}
	}
	if got, err := st.FindReplyRefs(ctx, "wf-r", "abcdef01", 5); err != nil || len(got) != 1 {
		t.Fatalf("合法哈希应能解析：%+v %v", got, err)
	}
	if got, err := st.FindReplyRefs(ctx, "  ", "abcdef01", 5); err != nil || len(got) != 0 {
		t.Fatalf("没有工作流时不解析：%+v %v", got, err)
	}
}

// 6 位前缀可能对上多条：按创建时间由早到晚返回，并受 limit 约束。
func TestFindReplyRefsPrefixMatchesAreOrderedAndLimited(t *testing.T) {
	st := newConfirmStore(t)
	ctx := context.Background()
	base := time.Now().Add(-time.Hour)
	seedReplySession(t, st, "sess-abcdef02", "wf-r", "", base.Add(time.Minute))
	seedReplySession(t, st, "sess-abcdef01", "wf-r", "", base)

	got, err := st.FindReplyRefs(ctx, "wf-r", "abcdef", 5)
	if err != nil || len(got) != 2 || got[0].Session.ID != "sess-abcdef01" || got[1].Session.ID != "sess-abcdef02" {
		t.Fatalf("应按创建时间升序返回两条：%+v %v", got, err)
	}
	if got[0].Turn != 1 || got[1].Turn != 2 {
		t.Fatalf("轮次序号应为 1、2：%+v", got)
	}
	got, err = st.FindReplyRefs(ctx, "wf-r", "abcdef", 1)
	if err != nil || len(got) != 1 || got[0].Session.ID != "sess-abcdef01" {
		t.Fatalf("limit=1 只应返回最早的一条：%+v %v", got, err)
	}
}
