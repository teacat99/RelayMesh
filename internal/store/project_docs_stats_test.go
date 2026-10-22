package store

import (
	"context"
	"testing"
	"time"

	"github.com/teacat99/RelayMesh/v2/internal/model"
)

// 修复前 glebarez/sqlite 上 MAX(updated_at) 以 string 返回，Scan 进 time.Time 失败，例如：
// sql: Scan error on column index 2, name "updated_at": unsupported Scan, storing driver.Value type string into type *time.Time
func TestListDocWorkflowStatsSQLite(t *testing.T) {
	st, err := New(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	mk := func(wf, path, status string) {
		t.Helper()
		_, err := st.CreateProjectDoc(ctx, ProjectDocInput{
			WorkflowID: wf,
			Path:       path,
			Body:       "# " + path + "\n",
			Status:     status,
		}, DocActor{Kind: model.DocActorHuman})
		if err != nil {
			t.Fatalf("create %s/%s: %v", wf, path, err)
		}
	}

	mk("wf-stats-a", "a1.md", model.DocStatusDraft)
	mk("wf-stats-a", "a2.md", model.DocStatusReview)
	mk("wf-stats-b", "b1.md", model.DocStatusDraft)
	mk("wf-stats-b", "archived.md", model.DocStatusArchived)

	stats, err := st.ListDocWorkflowStats(ctx)
	if err != nil {
		t.Fatalf("ListDocWorkflowStats 应成功，SQLite 错误: %q", err.Error())
	}

	byWF := map[string]DocWorkflowStat{}
	for _, s := range stats {
		byWF[s.WorkflowID] = s
	}

	if len(byWF) != 2 {
		t.Fatalf("期望 2 个工作流统计（不含归档-only 计数），got %d stats=%+v", len(byWF), stats)
	}
	if byWF["wf-stats-a"].Total != 2 {
		t.Fatalf("wf-stats-a total=%d want 2", byWF["wf-stats-a"].Total)
	}
	if byWF["wf-stats-b"].Total != 1 {
		t.Fatalf("wf-stats-b total=%d want 1 (archived excluded)", byWF["wf-stats-b"].Total)
	}
	if byWF["wf-stats-a"].UpdatedAt.IsZero() || byWF["wf-stats-b"].UpdatedAt.IsZero() {
		t.Fatal("updated_at 不应为零值")
	}
	_ = time.Now() // keep import if we extend assertions
}
