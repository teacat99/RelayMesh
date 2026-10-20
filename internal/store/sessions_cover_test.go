package store

import (
	"context"
	"fmt"
	"sort"
	"testing"
	"time"

	"github.com/teacat99/RelayMesh/v2/internal/model"
)

// seedCoverRounds 给一个工作流造 n 行已完成的会话，创建时间从 base 起每行加 step（直接改库里的 created_at，排序结果确定、不靠 sleep）。
// 返回按创建时间升序的会话编号。
func seedCoverRounds(t *testing.T, st *Store, workflow, dir string, n int, base time.Time, step time.Duration) []string {
	t.Helper()
	ctx := context.Background()
	ids := make([]string, 0, n)
	for i := 0; i < n; i++ {
		id := fmt.Sprintf("%s-%03d", workflow, i)
		if _, err := st.CreateFeedbackSession(ctx, CreateSessionInput{SessionID: id, WorkflowID: workflow, ProjectDirectory: dir, Summary: "汇报 " + id}); err != nil {
			t.Fatalf("创建会话 %s: %v", id, err)
		}
		// 完成这一轮，下一次创建才会新建一行而不是覆写 pending
		if _, err := st.SubmitFeedback(ctx, SubmitFeedbackInput{SessionID: id, ResponseText: "ok"}); err != nil {
			t.Fatalf("完成会话 %s: %v", id, err)
		}
		at := base.Add(time.Duration(i) * step)
		if err := st.db.Exec("UPDATE feedback_sessions SET created_at = ? WHERE id = ?", at, id).Error; err != nil {
			t.Fatalf("改会话 %s 的创建时间: %v", id, err)
		}
		ids = append(ids, id)
	}
	return ids
}

func sessionIDs(rows []model.FeedbackSession) []string {
	out := make([]string, len(rows))
	for i := range rows {
		out[i] = rows[i].ID
	}
	return out
}

func workflowSet(rows []model.FeedbackSession) map[string]int {
	out := map[string]int{}
	for i := range rows {
		out[rows[i].WorkflowID]++
	}
	return out
}

func assertNewestFirst(t *testing.T, rows []model.FeedbackSession) {
	t.Helper()
	if !sort.SliceIsSorted(rows, func(a, b int) bool { return rows[a].CreatedAt.After(rows[b].CreatedAt) }) {
		t.Fatalf("结果应按创建时间倒序: %v", sessionIDs(rows))
	}
}

// 复现 #173：话痨的工作流把较早的工作流挤出「最新 N 行」窗口，侧栏与归档库就凭空少了它们。
// 带「每个工作流补一行」的查询要保证每个工作流至少有一行，同时窗口本身（limit 的含义）不变。
func TestListFeedbackSessionsBriefCovering_KeepsEveryWorkflow(t *testing.T) {
	st := setupTestStore(t)
	ctx := context.Background()
	base := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)

	seedCoverRounds(t, st, "wf-old-a", "/tmp/cover-a", 2, base, time.Minute)                   // 最早的工作流，两轮
	seedCoverRounds(t, st, "wf-old-b", "/tmp/cover-b", 1, base.Add(10*time.Minute), time.Hour) // 第二早，一轮
	seedCoverRounds(t, st, "wf-busy", "/tmp/cover-busy", 30, base.Add(time.Hour), time.Minute) // 话痨：三十轮，全在后面

	// 前提：只看最新 10 行，窗口里只有话痨的工作流（这就是用户看到的「丢失」）
	plain, err := st.ListFeedbackSessionsBrief(ctx, "", "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if got := workflowSet(plain); len(got) != 1 || got["wf-busy"] != 10 {
		t.Fatalf("前提不成立：10 行窗口里应只有 wf-busy: %v", got)
	}

	rows, err := st.ListFeedbackSessionsBriefCovering(ctx, "", "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 12 {
		t.Fatalf("应是 10 行窗口 + 窗口外两个工作流各一行 = 12: %d %v", len(rows), sessionIDs(rows))
	}
	got := workflowSet(rows)
	if got["wf-busy"] != 10 || got["wf-old-a"] != 1 || got["wf-old-b"] != 1 {
		t.Fatalf("每个工作流都应在，窗口外的各补一行: %v", got)
	}
	ids := map[string]bool{}
	for _, id := range sessionIDs(rows) {
		if ids[id] {
			t.Fatalf("不应有重复行 %s: %v", id, sessionIDs(rows))
		}
		ids[id] = true
	}
	if !ids["wf-old-a-001"] || ids["wf-old-a-000"] {
		t.Fatalf("窗口外的工作流补的是最新那一行（wf-old-a-001），不是最早的: %v", sessionIDs(rows))
	}
	if !ids["wf-old-b-000"] {
		t.Fatalf("wf-old-b 唯一的一行应补进来: %v", sessionIDs(rows))
	}
	assertNewestFirst(t, rows)

	// 窗口本身不变：补行之前的 10 行与 ListFeedbackSessionsBrief 完全一致
	for i, id := range sessionIDs(plain) {
		if rows[i].ID != id {
			t.Fatalf("前 10 行应与最新窗口一致（第 %d 行 %s != %s）", i, rows[i].ID, id)
		}
	}
}

// 窗口已经覆盖所有工作流时，补行查询不多不少：结果与普通轻量列表逐行相同。
func TestListFeedbackSessionsBriefCovering_NoExtraWhenWindowCoversAll(t *testing.T) {
	st := setupTestStore(t)
	ctx := context.Background()
	base := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	seedCoverRounds(t, st, "wf-x", "/tmp/cover-x", 3, base, time.Minute)
	seedCoverRounds(t, st, "wf-y", "/tmp/cover-y", 3, base.Add(time.Hour), time.Minute)

	plain, err := st.ListFeedbackSessionsBrief(ctx, "", "", 200)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := st.ListFeedbackSessionsBriefCovering(ctx, "", "", 200)
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(sessionIDs(rows)) != fmt.Sprint(sessionIDs(plain)) || len(rows) != 6 {
		t.Fatalf("窗口已覆盖全部工作流时不应补行: got=%v want=%v", sessionIDs(rows), sessionIDs(plain))
	}
}

// 补的行同样遵守状态与项目目录过滤，归档库（status=archived）与按项目看都不会混进别的行。
func TestListFeedbackSessionsBriefCovering_HonoursFilters(t *testing.T) {
	st := setupTestStore(t)
	ctx := context.Background()
	base := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	seedCoverRounds(t, st, "wf-old-a", "/tmp/cover-a", 2, base, time.Minute)
	seedCoverRounds(t, st, "wf-old-b", "/tmp/cover-b", 2, base.Add(10*time.Minute), time.Minute)
	seedCoverRounds(t, st, "wf-busy", "/tmp/cover-busy", 20, base.Add(time.Hour), time.Minute)
	if err := st.db.Exec("UPDATE feedback_sessions SET status = 'archived' WHERE workflow_id = ?", "wf-old-b").Error; err != nil {
		t.Fatal(err)
	}

	// 默认（不带状态）：归档的工作流也要补一行（侧栏的「已归档」芯片与归档库靠它）
	all, err := st.ListFeedbackSessionsBriefCovering(ctx, "", "", 5)
	if err != nil {
		t.Fatal(err)
	}
	got := workflowSet(all)
	if got["wf-old-a"] != 1 || got["wf-old-b"] != 1 || got["wf-busy"] != 5 {
		t.Fatalf("不带过滤时三个工作流都应在: %v", got)
	}
	for i := range all {
		if all[i].WorkflowID == "wf-old-b" && all[i].Status != "archived" {
			t.Fatalf("补进来的 wf-old-b 应是归档状态: %+v", all[i])
		}
	}

	// 只看已归档：窗口就是它的两行，没有别的工作流混进来
	archived, err := st.ListFeedbackSessionsBriefCovering(ctx, "", "archived", 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(archived) != 1 || archived[0].WorkflowID != "wf-old-b" || archived[0].Status != "archived" {
		t.Fatalf("status=archived 只应有归档的工作流且各一行（窗口 1 行，已覆盖唯一的归档工作流）: %v", sessionIDs(archived))
	}

	// 只看已完成：归档的 wf-old-b 不算，补的只有 wf-old-a
	completed, err := st.ListFeedbackSessionsBriefCovering(ctx, "", "completed", 5)
	if err != nil {
		t.Fatal(err)
	}
	got = workflowSet(completed)
	if got["wf-old-b"] != 0 || got["wf-old-a"] != 1 || got["wf-busy"] != 5 {
		t.Fatalf("status=completed 时补行不应带归档的工作流: %v", got)
	}

	// 只看某个项目目录：只有这个目录里的工作流，且窗口窄也能补到它最新的一行
	scoped, err := st.ListFeedbackSessionsBriefCovering(ctx, "/tmp/cover-a", "", 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(scoped) != 1 || scoped[0].ID != "wf-old-a-001" {
		t.Fatalf("按项目目录过滤应只有该目录的工作流: %v", sessionIDs(scoped))
	}
}

// 补进来的行也要注入工作流自定义标题（与窗口里的行一致），否则侧栏里同一个工作流前后两个名字。
func TestListFeedbackSessionsBriefCovering_AppliesCustomTitles(t *testing.T) {
	st := setupTestStore(t)
	ctx := context.Background()
	base := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	seedCoverRounds(t, st, "wf-old-a", "/tmp/cover-a", 1, base, time.Minute)
	seedCoverRounds(t, st, "wf-busy", "/tmp/cover-busy", 5, base.Add(time.Hour), time.Minute)
	if err := st.db.Create(&model.SystemSetting{Key: "wf_custom_title:wf-old-a", Value: "老项目甲"}).Error; err != nil {
		t.Fatal(err)
	}

	rows, err := st.ListFeedbackSessionsBriefCovering(ctx, "", "", 2)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for i := range rows {
		if rows[i].WorkflowID == "wf-old-a" {
			found = true
			if rows[i].Title != "老项目甲" {
				t.Fatalf("补进来的行应带自定义标题: %q", rows[i].Title)
			}
		}
	}
	if !found {
		t.Fatalf("窗口外的 wf-old-a 应被补进来: %v", sessionIDs(rows))
	}
}

// 补行有上限：窗口外的工作流多到超过上限时，取最近有动静的那些，最老的被舍掉；窗口内的行不受影响。
func TestListFeedbackSessionsBriefCovering_CapsExtraWorkflows(t *testing.T) {
	st := setupTestStore(t)
	ctx := context.Background()
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	total := briefCoverExtraMax + 5
	for i := 0; i < total; i++ {
		seedCoverRounds(t, st, fmt.Sprintf("wf-solo-%04d", i), "/tmp/cover-solo", 1, base.Add(time.Duration(i)*time.Minute), time.Minute)
	}
	seedCoverRounds(t, st, "wf-busy", "/tmp/cover-busy", 3, base.Add(1000*time.Hour), time.Minute)

	rows, err := st.ListFeedbackSessionsBriefCovering(ctx, "", "", 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 3+briefCoverExtraMax {
		t.Fatalf("应是窗口 3 行 + 补行上限 %d: %d", briefCoverExtraMax, len(rows))
	}
	got := workflowSet(rows)
	if got["wf-busy"] != 3 {
		t.Fatalf("窗口内的行不受上限影响: %v", got)
	}
	for i := 0; i < 5; i++ {
		if got[fmt.Sprintf("wf-solo-%04d", i)] != 0 {
			t.Fatalf("最老的 5 个工作流应被上限舍掉: wf-solo-%04d", i)
		}
	}
	if got[fmt.Sprintf("wf-solo-%04d", total-1)] != 1 || got[fmt.Sprintf("wf-solo-%04d", 5)] != 1 {
		t.Fatalf("最近的 %d 个工作流应保留", briefCoverExtraMax)
	}
	assertNewestFirst(t, rows)
}

// 没有工作流编号的老会话行没有「工作流」可言：补行查询不碰它们（保持只看最新窗口的老行为），也不能因此报错。
func TestListFeedbackSessionsBriefCovering_SkipsRowsWithoutWorkflow(t *testing.T) {
	st := setupTestStore(t)
	ctx := context.Background()
	base := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	ids := seedCoverRounds(t, st, "wf-legacy", "/tmp/cover-legacy", 1, base, time.Minute)
	if err := st.db.Exec("UPDATE feedback_sessions SET workflow_id = '' WHERE id = ?", ids[0]).Error; err != nil {
		t.Fatal(err)
	}
	seedCoverRounds(t, st, "wf-busy", "/tmp/cover-busy", 4, base.Add(time.Hour), time.Minute)

	rows, err := st.ListFeedbackSessionsBriefCovering(ctx, "", "", 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("没有工作流编号的老行不补，只剩最新窗口 2 行: %v", sessionIDs(rows))
	}
}

// 每个工作流的总行数（含已归档），没有行的工作流不出现；编号超过一批（400）也要分批查全。
func TestCountFeedbackSessionsByWorkflow(t *testing.T) {
	st := setupTestStore(t)
	ctx := context.Background()
	base := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	seedCoverRounds(t, st, "wf-a", "/tmp/cover-a", 3, base, time.Minute)
	seedCoverRounds(t, st, "wf-b", "/tmp/cover-b", 1, base, time.Minute)
	if err := st.db.Exec("UPDATE feedback_sessions SET status = 'archived' WHERE id = ?", "wf-a-000").Error; err != nil {
		t.Fatal(err)
	}

	ids := []string{"wf-a", "wf-b", "wf-none"}
	for i := 0; i < 450; i++ {
		ids = append(ids, fmt.Sprintf("wf-pad-%03d", i))
	}
	ids = append(ids, "wf-a") // 重复编号不应让结果出错
	got, err := st.CountFeedbackSessionsByWorkflow(ctx, ids)
	if err != nil {
		t.Fatal(err)
	}
	if got["wf-a"] != 3 || got["wf-b"] != 1 {
		t.Fatalf("应数出每个工作流的总行数（含已归档）: %v", got)
	}
	if _, ok := got["wf-none"]; ok {
		t.Fatalf("没有会话行的工作流不应出现: %v", got)
	}
	if empty, err := st.CountFeedbackSessionsByWorkflow(ctx, nil); err != nil || len(empty) != 0 {
		t.Fatalf("空输入应得空结果: %v %v", empty, err)
	}
}
