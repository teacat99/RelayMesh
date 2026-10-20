package store

import (
	"context"
	"fmt"
	"testing"
)

// 侧栏会话卡一次要查很多个工作流各自待处理的确认项数（#159）：必须与逐个工作流的 CountPendingConfirms 口径一致，
// 已答复 / 已忽略的不算，没有确认项的工作流不出现在结果里（视为 0），超过一批的工作流编号也要查全。
func TestCountPendingConfirmsByWorkflow(t *testing.T) {
	st := newConfirmStore(t)
	ctx := context.Background()

	empty, err := st.CountPendingConfirmsByWorkflow(ctx, nil)
	if err != nil || len(empty) != 0 {
		t.Fatalf("空列表应返回空表: %v %v", empty, err)
	}

	newAsk(t, st, "wf-a", "a1")
	newAsk(t, st, "wf-a", "a2")
	newAsk(t, st, "wf-a", "a3")
	newAsk(t, st, "wf-b", "b1")
	if _, err := st.DismissConfirmItem(ctx, "wf-a", "Q1", "不需要了"); err != nil {
		t.Fatalf("忽略 Q1: %v", err)
	}

	got, err := st.CountPendingConfirmsByWorkflow(ctx, []string{"wf-a", "wf-b", "wf-none"})
	if err != nil {
		t.Fatalf("分组计数: %v", err)
	}
	if got["wf-a"] != 2 || got["wf-b"] != 1 {
		t.Fatalf("wf-a 应剩 2 条、wf-b 1 条: %v", got)
	}
	if n, ok := got["wf-none"]; ok {
		t.Fatalf("没有确认项的工作流不应出现在结果里: %d", n)
	}
	for _, wf := range []string{"wf-a", "wf-b", "wf-none"} {
		single, err := st.CountPendingConfirms(ctx, wf)
		if err != nil || single != got[wf] {
			t.Fatalf("%s：分组计数 %d 与单个计数 %d 口径不一致（%v）", wf, got[wf], single, err)
		}
	}

	// 超过一批（400）的工作流编号：排在最后的也要查到
	ids := make([]string, 0, 950)
	for i := 0; i < 948; i++ {
		ids = append(ids, fmt.Sprintf("wf-x-%d", i))
	}
	ids = append(ids, "wf-a", "wf-b")
	big, err := st.CountPendingConfirmsByWorkflow(ctx, ids)
	if err != nil || big["wf-a"] != 2 || big["wf-b"] != 1 {
		t.Fatalf("超过一批时应分批查全: %v %v", big, err)
	}
}
