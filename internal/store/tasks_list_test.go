package store

import (
	"context"
	"fmt"
	"reflect"
	"testing"

	"github.com/teacat99/RelayMesh/v2/internal/model"
)

// 托管自驾工单列表的翻页（#250）：next_cursor 必须让下一页从「上一页最后一条之后」接着取，
// 不能漏掉任何一条、也不能重复。以前 next_cursor 取的是「下一页的第一条」的编号，
// 而下一页的查询条件是 id > cursor，于是每个页界上都漏一条（REST 与 MCP list_tasks 都受影响）。

const pagingProject = "paging-proj"

// seedPagingTasks 建 n 个工单，编号 task-001 … task-NNN，返回按升序排好的编号。
func seedPagingTasks(t *testing.T, st *Store, n int) []string {
	t.Helper()
	ctx := context.Background()
	ids := make([]string, 0, n)
	for i := 1; i <= n; i++ {
		id := fmt.Sprintf("task-%03d", i)
		if _, err := st.CreateTask(ctx, model.CreateTaskInput{
			ProjectID: pagingProject,
			TaskID:    id,
			Segments:  []model.Segment{{Name: "work_order", Content: "x"}},
		}); err != nil {
			t.Fatalf("CreateTask %s: %v", id, err)
		}
		ids = append(ids, id)
	}
	return ids
}

// walkTaskPages 一页页翻到没有 next_cursor 为止，返回按顺序拼起来的编号、每页的条数。
func walkTaskPages(t *testing.T, st *Store, state string, limit int, updatesOnly bool) ([]string, []int) {
	t.Helper()
	ctx := context.Background()
	var ids []string
	var sizes []int
	cursor := ""
	for i := 0; ; i++ {
		if i > 100 {
			t.Fatalf("翻了 100 页还没有结束，游标可能在原地打转（cursor=%q）", cursor)
		}
		page, err := st.ListTasks(ctx, pagingProject, state, cursor, limit, updatesOnly)
		if err != nil {
			t.Fatalf("ListTasks(cursor=%q): %v", cursor, err)
		}
		sizes = append(sizes, len(page.Tasks))
		for _, s := range page.Tasks {
			ids = append(ids, s.ID)
		}
		if page.NextCursor == "" {
			return ids, sizes
		}
		cursor = page.NextCursor
	}
}

func TestListTasks_PagingCoversEveryRowOnce(t *testing.T) {
	cases := []struct {
		name      string
		total     int
		limit     int
		wantSizes []int
	}{
		{"不是整页：7 条每页 3 条", 7, 3, []int{3, 3, 1}},
		{"正好整页：6 条每页 3 条（最后一页没有 next_cursor）", 6, 3, []int{3, 3}},
		{"一页装得下：2 条每页 3 条", 2, 3, []int{2}},
		{"每页 1 条", 4, 1, []int{1, 1, 1, 1}},
		{"空列表", 0, 3, []int{0}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st := setupTestStore(t)
			want := seedPagingTasks(t, st, tc.total)

			got, sizes := walkTaskPages(t, st, "", tc.limit, false)
			if len(want) == 0 {
				want = nil
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("翻完所有页拿到的编号不对（漏了或重复了）：\n got  %v\n want %v", got, want)
			}
			if !reflect.DeepEqual(sizes, tc.wantSizes) {
				t.Fatalf("每页条数不对：got %v want %v", sizes, tc.wantSizes)
			}
		})
	}
}

// 带「只要有未读更新的」过滤时，过滤发生在翻页之前：每页都装满、翻完正好是过滤后的全部。
func TestListTasks_PagingWithUpdatesOnly(t *testing.T) {
	st := setupTestStore(t)
	all := seedPagingTasks(t, st, 9)

	// 偶数号工单有未读汇报（report_sequence > acknowledged_report_sequence）。
	var want []string
	for i, id := range all {
		if (i+1)%2 == 0 {
			if err := st.db.Model(&model.Task{}).Where("id = ?", id).Update("report_sequence", 1).Error; err != nil {
				t.Fatalf("标记未读 %s: %v", id, err)
			}
			want = append(want, id)
		}
	}

	got, sizes := walkTaskPages(t, st, "", 3, true)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("只取有更新的：got %v want %v", got, want)
	}
	if !reflect.DeepEqual(sizes, []int{3, 1}) {
		t.Fatalf("每页条数不对：got %v want [3 1]", sizes)
	}
}

// 带状态过滤时同理。
func TestListTasks_PagingWithStateFilter(t *testing.T) {
	st := setupTestStore(t)
	all := seedPagingTasks(t, st, 8)

	var want []string
	for i, id := range all {
		if i%3 == 0 { // 1、4、7 号改成 done
			if err := st.db.Model(&model.Task{}).Where("id = ?", id).Update("state", "done").Error; err != nil {
				t.Fatalf("改状态 %s: %v", id, err)
			}
			want = append(want, id)
		}
	}

	got, sizes := walkTaskPages(t, st, "done", 2, false)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("只取 done：got %v want %v", got, want)
	}
	if !reflect.DeepEqual(sizes, []int{2, 1}) {
		t.Fatalf("每页条数不对：got %v want [2 1]", sizes)
	}
}

// next_cursor 是不透明的「原样传回」凭据，但它的含义要对：取回的下一页第一条紧跟在上一页最后一条之后。
func TestListTasks_NextCursorFollowsLastRowOfPage(t *testing.T) {
	st := setupTestStore(t)
	ids := seedPagingTasks(t, st, 5)
	ctx := context.Background()

	p1, err := st.ListTasks(ctx, pagingProject, "", "", 2, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(p1.Tasks) != 2 || p1.NextCursor == "" {
		t.Fatalf("第一页应有 2 条且还有下一页：%d 条，cursor=%q", len(p1.Tasks), p1.NextCursor)
	}
	p2, err := st.ListTasks(ctx, pagingProject, "", p1.NextCursor, 2, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(p2.Tasks) == 0 || p2.Tasks[0].ID != ids[2] {
		first := ""
		if len(p2.Tasks) > 0 {
			first = p2.Tasks[0].ID
		}
		t.Fatalf("第二页的第一条应紧跟第一页最后一条（%s）之后，也就是 %s，实际是 %q", p1.Tasks[1].ID, ids[2], first)
	}
}
