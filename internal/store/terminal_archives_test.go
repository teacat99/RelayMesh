package store

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/teacat99/RelayMesh/v2/internal/model"
)

func TestTerminalArchivesSetListAndIdempotent(t *testing.T) {
	st, err := New(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	t0 := time.Date(2026, 10, 3, 8, 0, 0, 0, time.UTC)

	if n, err := st.SetTerminalArchived(ctx, []string{"m1:s1", "m1:s2"}, true, t0); err != nil || n != 2 {
		t.Fatalf("归档两条: n=%d err=%v", n, err)
	}
	if n, err := st.SetTerminalArchived(ctx, []string{"m1:s2", "r:t1"}, true, t0.Add(time.Minute)); err != nil || n != 1 {
		t.Fatalf("重复的不算变化，只有 r:t1 是新的: n=%d err=%v", n, err)
	}
	keys, err := st.ListTerminalArchives(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) != 3 || keys[0] != "r:t1" {
		t.Fatalf("应按归档时间倒序、共 3 条: %v", keys)
	}
	// 重复归档不会刷新已有条目的归档时间。
	var row model.TerminalArchive
	if err := st.db.Where("item_key = ?", "m1:s2").First(&row).Error; err != nil {
		t.Fatal(err)
	}
	if !row.ArchivedAt.Equal(t0) {
		t.Fatalf("重复归档不应改写归档时间: %v", row.ArchivedAt)
	}

	if n, err := st.SetTerminalArchived(ctx, []string{"m1:s1", "nope"}, false, t0); err != nil || n != 1 {
		t.Fatalf("取消归档: n=%d err=%v", n, err)
	}
	if keys, _ := st.ListTerminalArchives(ctx); len(keys) != 2 {
		t.Fatalf("取消后应剩 2 条: %v", keys)
	}
	if n, err := st.SetTerminalArchived(ctx, nil, true, t0); err != nil || n != 0 {
		t.Fatalf("空输入应无操作: n=%d err=%v", n, err)
	}
}

func TestTerminalArchivesAreCappedKeepingNewest(t *testing.T) {
	st, err := New(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	base := time.Date(2026, 10, 3, 8, 0, 0, 0, time.UTC)

	// 先塞满到上限（批量写入，时间戳递增，最早的是 k0000）。
	rows := make([]model.TerminalArchive, MaxTerminalArchives)
	for i := range rows {
		rows[i] = model.TerminalArchive{ItemKey: fmt.Sprintf("k%04d", i), ArchivedAt: base.Add(time.Duration(i) * time.Second)}
	}
	if err := st.db.CreateInBatches(&rows, 500).Error; err != nil {
		t.Fatal(err)
	}
	// 再归档 3 条新的：最早的 3 条被挤掉。
	if _, err := st.SetTerminalArchived(ctx, []string{"new1", "new2", "new3"}, true, base.Add(24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	keys, err := st.ListTerminalArchives(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) != MaxTerminalArchives {
		t.Fatalf("总数应保持在上限 %d，实际 %d", MaxTerminalArchives, len(keys))
	}
	have := map[string]bool{}
	for _, k := range keys {
		have[k] = true
	}
	for _, gone := range []string{"k0000", "k0001", "k0002"} {
		if have[gone] {
			t.Errorf("最早归档的 %s 应被挤掉", gone)
		}
	}
	for _, kept := range []string{"k0003", "new1", "new2", "new3"} {
		if !have[kept] {
			t.Errorf("%s 应保留", kept)
		}
	}
}

func TestDeleteTerminalArchivesOfHostOnlyTouchesThatHost(t *testing.T) {
	st, err := New(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := st.SetTerminalArchived(ctx, []string{"RM-A:s1", "RM-A:s2", "RM-AB:s1", "RM-B:s1", "r:thread"}, true, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := st.DeleteTerminalArchivesOfHost(ctx, "RM-A"); err != nil {
		t.Fatal(err)
	}
	keys, _ := st.ListTerminalArchives(ctx)
	have := map[string]bool{}
	for _, k := range keys {
		have[k] = true
	}
	if have["RM-A:s1"] || have["RM-A:s2"] {
		t.Fatalf("RM-A 的归档应被清掉: %v", keys)
	}
	for _, k := range []string{"RM-AB:s1", "RM-B:s1", "r:thread"} {
		if !have[k] {
			t.Fatalf("%s 不属于 RM-A，不应被删除（前缀必须带冒号）: %v", k, keys)
		}
	}
}
