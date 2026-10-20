package store

import (
	"context"
	"strings"
	"testing"
	"time"
)

// 主机页「会话已不存在」提示的忽略记录（第六批 #28）。

func TestDismissTerminalLostIsIdempotentAndScopedPerHost(t *testing.T) {
	st := setupTestStore(t)
	ctx := context.Background()
	now := time.Now()
	a, b := strings.Repeat("a", 32), strings.Repeat("b", 32)

	n, err := st.DismissTerminalLost(ctx, "RM-HOST-1", []string{a, b}, now)
	if err != nil || n != 2 {
		t.Fatalf("首次忽略应新增 2 条: n=%d err=%v", n, err)
	}
	// 重复忽略不算变化，所以界面上重复点击是安全的。
	if n, err = st.DismissTerminalLost(ctx, "RM-HOST-1", []string{a}, now); err != nil || n != 0 {
		t.Fatalf("重复忽略应为 0: n=%d err=%v", n, err)
	}
	// 同一个会话 ID 在另一台主机上互不影响。
	if n, err = st.DismissTerminalLost(ctx, "RM-HOST-2", []string{a}, now); err != nil || n != 1 {
		t.Fatalf("另一台主机应单独计: n=%d err=%v", n, err)
	}

	all, err := st.ListTerminalLostDismissals(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(all["RM-HOST-1"]) != 2 || len(all["RM-HOST-2"]) != 1 {
		t.Fatalf("按主机分组不对: %+v", all)
	}
	if _, ok := all["RM-HOST-2"][b]; ok {
		t.Fatalf("RM-HOST-2 不应带上 RM-HOST-1 的忽略: %+v", all)
	}

	// 空输入是空操作，不报错。
	if n, err = st.DismissTerminalLost(ctx, "RM-HOST-1", nil, now); err != nil || n != 0 {
		t.Fatalf("空列表应为 0: n=%d err=%v", n, err)
	}
	if n, err = st.DismissTerminalLost(ctx, "", []string{a}, now); err != nil || n != 0 {
		t.Fatalf("空机器码应为 0: n=%d err=%v", n, err)
	}
}

func TestDismissTerminalLostPrunesExpiredRecords(t *testing.T) {
	st := setupTestStore(t)
	ctx := context.Background()
	old := time.Now().AddDate(0, 0, -(LostDismissalKeepDays + 5))
	stale, fresh := strings.Repeat("c", 32), strings.Repeat("d", 32)

	if _, err := st.DismissTerminalLost(ctx, "RM-HOST-1", []string{stale}, old); err != nil {
		t.Fatal(err)
	}
	// 之后的一次忽略顺手清掉过期的记录：表不会无限增长。
	if _, err := st.DismissTerminalLost(ctx, "RM-HOST-1", []string{fresh}, time.Now()); err != nil {
		t.Fatal(err)
	}
	all, err := st.ListTerminalLostDismissals(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := all["RM-HOST-1"][stale]; ok {
		t.Fatalf("过期的忽略记录应被清理: %+v", all)
	}
	if _, ok := all["RM-HOST-1"][fresh]; !ok {
		t.Fatalf("新的忽略记录必须保留: %+v", all)
	}
}

func TestDeleteTerminalLostDismissalsOfHostOnlyTouchesThatHost(t *testing.T) {
	st := setupTestStore(t)
	ctx := context.Background()
	sid := strings.Repeat("e", 32)
	for _, code := range []string{"RM-HOST-1", "RM-HOST-2"} {
		if _, err := st.DismissTerminalLost(ctx, code, []string{sid}, time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.DeleteTerminalLostDismissalsOfHost(ctx, "RM-HOST-1"); err != nil {
		t.Fatal(err)
	}
	all, err := st.ListTerminalLostDismissals(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(all["RM-HOST-1"]) != 0 || len(all["RM-HOST-2"]) != 1 {
		t.Fatalf("只应清掉被删主机的记录: %+v", all)
	}
}
