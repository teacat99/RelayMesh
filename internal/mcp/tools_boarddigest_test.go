package mcp

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/teacat99/RelayMesh/v2/internal/notify"
	"github.com/teacat99/RelayMesh/v2/internal/store"
)

// 回执头里的 board 行（#180 B3a）：文案排版、等待时长的写法、以及在真实回执里出现 / 消失的时机。

func boardGroup(now time.Time, total int, waited time.Duration, seqs ...int) store.BoardDigestGroup {
	g := store.BoardDigestGroup{Total: total, Seqs: seqs}
	if waited > 0 {
		g.Oldest = now.Add(-waited)
	}
	return g
}

func TestFormatBoardDigest(t *testing.T) {
	now := time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)

	if got := formatBoardDigest(&store.BoardDigest{}, now, 2*time.Hour); got != "" {
		t.Fatalf("没有待办时不应输出任何内容: %q", got)
	}

	// 只有待接取，且多于列出的个数：编号后面加「等」，带最久已等多久。
	got := formatBoardDigest(&store.BoardDigest{ToAccept: boardGroup(now, 7, 47*time.Minute, 3, 4, 5, 6, 7)}, now, 2*time.Hour)
	want := `board: 7 个指派待你接取（#3 #4 #5 #6 #7 等，最久已等 47 分钟）→ task_update {task_id, claim:"accept"} 接取，做不了就 claim:"decline" + note 写理由`
	if got != want {
		t.Fatalf("待接取的文案不符:\n got: %s\nwant: %s", got, want)
	}

	// 个数与列出的一样多：不加「等」。
	one := formatBoardDigest(&store.BoardDigest{AwaitingReview: boardGroup(now, 1, 0, 9)}, now, 2*time.Hour)
	if !strings.Contains(one, "1 个你派出的任务已交付待验收（#9）→") || strings.Contains(one, "等") || strings.Contains(one, "已等") {
		t.Fatalf("待验收不写等待时长也不加「等」: %s", one)
	}

	// 六类全有：承接者侧在前、派发者侧在后，整行是单行，每类之间用「 ｜ 」隔开。
	all := &store.BoardDigest{
		ToAccept:       boardGroup(now, 2, 3*time.Hour, 1, 2),
		Returned:       boardGroup(now, 1, 0, 3),
		AwaitingReview: boardGroup(now, 1, 0, 4),
		Declined:       boardGroup(now, 1, 0, 5),
		Stale:          boardGroup(now, 2, 50*time.Hour, 6, 7),
		StaleGone:      boardGroup(now, 1, 5*time.Hour, 8),
	}
	line := formatBoardDigest(all, now, 2*time.Hour)
	if strings.Contains(line, "\n") || !strings.HasPrefix(line, "board: ") {
		t.Fatalf("应是以 board: 开头的单行: %q", line)
	}
	heads := []string{"2 个指派待你接取", "1 个交付被退回", "1 个你派出的任务已交付待验收", "1 个你派出的指派被拒绝",
		"2 个你派出的指派超过 2 小时没人接", "1 个你派出的指派承接者已不存在"}
	last := -1
	for _, h := range heads {
		i := strings.Index(line, h)
		if i < 0 || i < last {
			t.Fatalf("缺少 %q 或顺序不对: %s", h, line)
		}
		last = i
	}
	if n := strings.Count(line, " ｜ "); n != 5 {
		t.Fatalf("六类之间应有 5 个分隔，得到 %d: %s", n, line)
	}
	for _, w := range []string{"最久已等 3 小时", "最久已等 2 天", "最久已等 5 小时"} {
		if !strings.Contains(line, w) {
			t.Fatalf("应包含 %q: %s", w, line)
		}
	}
}

func TestHumanWaitAndThreshold(t *testing.T) {
	for _, c := range []struct {
		d    time.Duration
		want string
	}{
		{-5 * time.Minute, "不到 1 分钟"}, {0, "不到 1 分钟"}, {59 * time.Second, "不到 1 分钟"},
		{time.Minute, "1 分钟"}, {59*time.Minute + 59*time.Second, "59 分钟"},
		{time.Hour, "1 小时"}, {47*time.Hour + 59*time.Minute, "47 小时"},
		{48 * time.Hour, "2 天"}, {100 * time.Hour, "4 天"},
	} {
		if got := humanWait(c.d); got != c.want {
			t.Errorf("humanWait(%v) = %q，想要 %q", c.d, got, c.want)
		}
	}
	for _, c := range []struct {
		d    time.Duration
		want string
	}{
		{30 * time.Minute, "30 分钟"}, {90 * time.Minute, "90 分钟"}, {2 * time.Hour, "2 小时"}, {24 * time.Hour, "24 小时"},
	} {
		if got := humanThreshold(c.d); got != c.want {
			t.Errorf("humanThreshold(%v) = %q，想要 %q", c.d, got, c.want)
		}
	}
}

// 真实回执：board 行出现在 tasks 行之后、标记行之前；没有待办时回执里没有 board 行（与升级前逐字节一致）。
// 接取 / 交付 / 退回 / 拒绝一路走完时两侧 board 行的变化，用同一个生成函数逐步核对（真实回执每次要等 1 秒超时，只走三次）。
func TestBoardDigestHeaderInReceipts(t *testing.T) {
	srv := assignFixture(t)
	ctx := context.Background()
	for _, wf := range []string{"wf-a", "wf-b", "wf-c"} {
		completeLatestPending(t, srv, wf) // 夹具给每个工作流造了一条待回复的会话，先让它们完成
	}
	fb := func(wf string) string {
		t.Helper()
		out := callToolText(t, srv, "interactive_feedback", map[string]any{
			"workflow_id": wf, "project_directory": "/code/a", "summary": "汇报 " + wf,
		})
		completeLatestPending(t, srv, wf)
		return out
	}
	hasBoard := func(receipt string) bool {
		for _, ln := range strings.Split(receipt, "\n") {
			if strings.HasPrefix(ln, "board: ") {
				return true
			}
		}
		return false
	}
	line := func(wf string) string { return srv.boardDigestHeader(ctx, wf) }

	// 还没有任何协作：回执里没有 board 行（真实回执 ①）。
	if got := fb("wf-b"); hasBoard(got) {
		t.Fatalf("没有协作指派时回执里不应有 board 行:\n%s", got)
	}

	// wf-a 派两个任务给 wf-b：承接者的回执头出现 board 行，位置在 tasks 行之后、标记行之前（真实回执 ②）。
	wcDo(t, srv, "wf-a", map[string]any{"action": "task_create", "title": "写迁移脚本", "description": "把旧表迁到新表", "assignee": "wf-b"})
	wcDo(t, srv, "wf-a", map[string]any{"action": "task_create", "title": "补回滚脚本", "description": "迁移失败时能退回去", "assignee": "wf-b"})
	got := fb("wf-b")
	if !strings.Contains(got, "\nboard: 2 个指派待你接取（#1 #2，最久已等 不到 1 分钟）→ task_update {task_id, claim:\"accept\"}") {
		t.Fatalf("承接者回执里的 board 行不符:\n%s", got)
	}
	if ti, bi, mi := strings.Index(got, "tasks:"), strings.Index(got, "board:"), strings.Index(got, "=== "); !(ti >= 0 && ti < bi && bi < mi) {
		t.Fatalf("board 行应在 tasks 行之后、标记行之前（%d %d %d）:\n%s", ti, bi, mi, got)
	}
	if l := line("wf-a"); l != "" {
		t.Fatalf("刚派出的指派对派发者不是待办: %q", l)
	}

	// 接取 #1 后只剩 #2；无关的工作流完全没有。
	wcDo(t, srv, "wf-b", map[string]any{"action": "task_update", "task_id": 1, "claim": "accept"})
	if l := line("wf-b"); !strings.HasPrefix(l, "board: 1 个指派待你接取（#2，") {
		t.Fatalf("接取 #1 后应只剩 #2: %q", l)
	}
	if l := line("wf-c"); l != "" {
		t.Fatalf("无关的工作流不应有 board 行: %q", l)
	}

	// 交付 #1：派发者这边出现「待验收」；退回后派发者清零，承接者看到「被退回」。
	wcDo(t, srv, "wf-b", map[string]any{"action": "task_update", "task_id": 1, "claim": "deliver", "progress": "脚本写好了，行数核对一致"})
	if l := line("wf-a"); !strings.HasPrefix(l, "board: 1 个你派出的任务已交付待验收（#1）→") {
		t.Fatalf("派发者应看到待验收: %q", l)
	}
	wcDo(t, srv, "wf-a", map[string]any{"action": "task_update", "task_id": 1, "claim": "reject_delivery", "note": "缺回滚脚本"})
	if l := line("wf-a"); l != "" {
		t.Fatalf("退回后派发者没有待办了: %q", l)
	}
	if l := line("wf-b"); !strings.Contains(l, "1 个指派待你接取（#2，") || !strings.Contains(l, "1 个交付被退回（#1）→") {
		t.Fatalf("承接者应同时看到待接取的 #2 和被退回的 #1: %q", l)
	}

	// 拒绝 #2：承接者这一项消失，派发者出现「被拒绝」（真实回执 ③）。
	wcDo(t, srv, "wf-b", map[string]any{"action": "task_update", "task_id": 2, "claim": "decline", "note": "手上排满了"})
	if l := line("wf-b"); strings.Contains(l, "待你接取") {
		t.Fatalf("拒绝后 #2 不应再算待接取: %q", l)
	}
	if got := fb("wf-a"); !strings.Contains(got, "\nboard: 1 个你派出的指派被拒绝（#2）→") {
		t.Fatalf("派发者的回执应出现被拒绝:\n%s", got)
	}

	// 没有协作的工作流直接调用返回空串（查询失败同样只是少一行软提示，不会让回执报错）。
	if s := line("wf-nobody"); s != "" {
		t.Fatalf("没有协作的工作流应返回空串: %q", s)
	}
}

// 「久无人接」的阈值读通知偏好（#180 B3d）：没改过是默认 2 小时；改过的跟着走；坏值被收进范围；
// 回执头、网页行的变红时间与久无人接的提醒因此始终是同一个口径。
func TestBoardStaleAfterFollowsPrefs(t *testing.T) {
	srv := assignFixture(t)
	ctx := context.Background()
	set := func(minutes int) {
		t.Helper()
		if _, err := srv.store.UpdateNotifyPrefs(ctx, func(cur notify.Prefs) (notify.Prefs, error) {
			cur.BoardStaleMinutes = minutes
			return cur, nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	if got := srv.boardStaleAfter(ctx); got != 2*time.Hour {
		t.Fatalf("没改过应是默认 2 小时: %v", got)
	}
	if store.BoardDigestStaleDefault != 2*time.Hour {
		t.Fatalf("store 里的默认阈值要与通知偏好里的默认一致: %v", store.BoardDigestStaleDefault)
	}
	set(90)
	if got := srv.boardStaleAfter(ctx); got != 90*time.Minute {
		t.Fatalf("设成 90 分钟应生效: %v", got)
	}
	set(1440)
	if got := srv.boardStaleAfter(ctx); got != 24*time.Hour {
		t.Fatalf("设成 24 小时应生效: %v", got)
	}
	set(0)
	if got := srv.boardStaleAfter(ctx); got != 2*time.Hour {
		t.Fatalf("0 应恢复默认: %v", got)
	}
}
