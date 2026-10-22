package store

import (
	"context"
	"strings"
	"testing"
	"time"
)

const (
	sidA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	sidB = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	sidC = "cccccccccccccccccccccccccccccccc"
)

func intp(v int) *int { return &v }

func TestSyncTerminalSessionsLifecycle(t *testing.T) {
	st := newTerminalTestStore(t)
	ctx := context.Background()
	t0 := time.Unix(1_700_000_000, 0)

	// 新会话建行；字段原样保存。
	n, err := st.SyncTerminalSessions(ctx, "host1", []TerminalSessionSeen{
		{SID: sidA, Name: "build", Shell: "/bin/bash", StartedAt: 100, Rec: "on", RecBytes: 10},
		{SID: sidB, Name: "logs", Shell: "/bin/sh", StartedAt: 101, Rec: "incognito"},
	}, t0)
	if err != nil || n != 2 {
		t.Fatalf("首次同步: n=%d err=%v", n, err)
	}
	rows, _ := st.ListTerminalSessions(ctx, "host1", false, 0)
	if len(rows) != 2 {
		t.Fatalf("应有 2 行，得到 %d", len(rows))
	}
	if rows[0].SID != sidB || rows[1].SID != sidA {
		t.Fatalf("应按最近创建在前排序: %s %s", rows[0].SID, rows[1].SID)
	}

	// 内容无变化时不写库。
	n, _ = st.SyncTerminalSessions(ctx, "host1", []TerminalSessionSeen{
		{SID: sidA, Name: "build", Shell: "/bin/bash", StartedAt: 100, Rec: "on", RecBytes: 10},
		{SID: sidB, Name: "logs", Shell: "/bin/sh", StartedAt: 101, Rec: "incognito"},
	}, t0.Add(time.Second))
	if n != 0 {
		t.Fatalf("无变化应不写库，实际写了 %d 行", n)
	}

	// 会话 B 不再出现在清单里：视为已结束，原因 gone（推断）。
	n, _ = st.SyncTerminalSessions(ctx, "host1", []TerminalSessionSeen{
		{SID: sidA, Name: "build2", Shell: "/bin/bash", StartedAt: 100, Rec: "on", RecBytes: 99},
	}, t0.Add(2*time.Second))
	if n != 2 {
		t.Fatalf("A 更新 + B 结束应写 2 行，得到 %d", n)
	}
	b, _ := st.GetTerminalSession(ctx, "host1", sidB)
	if b == nil || b.EndedAt == nil || b.EndedReason != "gone" {
		t.Fatalf("B 应被推断为 gone: %+v", b)
	}

	// 会话 A 退出：记为 exit 并带退出码。
	_, _ = st.SyncTerminalSessions(ctx, "host1", []TerminalSessionSeen{
		{SID: sidA, Name: "build2", Shell: "/bin/bash", StartedAt: 100, Rec: "on", RecBytes: 99, Exited: true, ExitCode: intp(3)},
	}, t0.Add(3*time.Second))
	a, _ := st.GetTerminalSession(ctx, "host1", sidA)
	if a.EndedAt == nil || a.EndedReason != "exit" || a.ExitCode == nil || *a.ExitCode != 3 {
		t.Fatalf("A 应记为 exit(3): %+v", a)
	}

	// 主机又报告 B 仍在运行：之前的 gone 只是推断，撤销。
	_, _ = st.SyncTerminalSessions(ctx, "host1", []TerminalSessionSeen{
		{SID: sidB, Name: "logs", Shell: "/bin/sh", StartedAt: 101, Rec: "incognito"},
		{SID: sidA, Name: "build2", Shell: "/bin/bash", StartedAt: 100, Rec: "on", RecBytes: 99, Exited: true, ExitCode: intp(3)},
	}, t0.Add(4*time.Second))
	b, _ = st.GetTerminalSession(ctx, "host1", sidB)
	if b.EndedAt != nil || b.EndedReason != "" {
		t.Fatalf("重新出现的会话应恢复为运行中: %+v", b)
	}

	// 已结束的 A 不会因为清单里没有它而被改写。
	_, _ = st.SyncTerminalSessions(ctx, "host1", nil, t0.Add(5*time.Second))
	a, _ = st.GetTerminalSession(ctx, "host1", sidA)
	if a.EndedReason != "exit" {
		t.Fatalf("已结束会话的原因不应被改写: %+v", a)
	}

	// 不同主机互不影响。
	other, _ := st.ListTerminalSessions(ctx, "host2", false, 0)
	if len(other) != 0 {
		t.Fatalf("host2 不应有会话: %+v", other)
	}
}

func TestSameSIDOnDifferentHostsDoesNotCollide(t *testing.T) {
	st := newTerminalTestStore(t)
	ctx := context.Background()
	now := time.Now()
	for _, code := range []string{"hostA", "hostB"} {
		if _, err := st.SyncTerminalSessions(ctx, code, []TerminalSessionSeen{{SID: sidA, Name: code}}, now); err != nil {
			t.Fatalf("%s: %v", code, err)
		}
	}
	a, _ := st.GetTerminalSession(ctx, "hostA", sidA)
	b, _ := st.GetTerminalSession(ctx, "hostB", sidA)
	if a == nil || b == nil || a.Name != "hostA" || b.Name != "hostB" {
		t.Fatalf("同一个 SID 在不同主机上应各自独立: %+v %+v", a, b)
	}
}

func TestRecordTerminalSessionEndsPrecedence(t *testing.T) {
	st := newTerminalTestStore(t)
	ctx := context.Background()
	t0 := time.Unix(1_700_000_000, 0)

	_, _ = st.SyncTerminalSessions(ctx, "h", []TerminalSessionSeen{{SID: sidA, Name: "a"}, {SID: sidB, Name: "b"}}, t0)

	// 弱原因（gone / exit）可被精确原因覆盖。
	_, _ = st.SyncTerminalSessions(ctx, "h", []TerminalSessionSeen{{SID: sidB, Name: "b", Exited: true, ExitCode: intp(137)}}, t0.Add(time.Second)) // A 变 gone，B 变 exit
	if err := st.RecordTerminalSessionEnds(ctx, "h", []TerminalSessionEnd{
		{SID: sidA, Reason: "killed"},
		{SID: sidB, Reason: "killed"},
	}, t0.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	for _, sid := range []string{sidA, sidB} {
		r, _ := st.GetTerminalSession(ctx, "h", sid)
		if r.EndedReason != "killed" {
			t.Fatalf("%s 应被覆盖为 killed: %+v", sid, r)
		}
	}
	b, _ := st.GetTerminalSession(ctx, "h", sidB)
	if b.ExitCode == nil || *b.ExitCode != 137 {
		t.Fatalf("覆盖原因不应丢失退出码: %+v", b)
	}

	// 精确原因不会被后来的推断改写。
	if err := st.RecordTerminalSessionEnds(ctx, "h", []TerminalSessionEnd{{SID: sidA, Reason: "host_reboot"}}, t0.Add(3*time.Second)); err != nil {
		t.Fatal(err)
	}
	a, _ := st.GetTerminalSession(ctx, "h", sidA)
	if a.EndedReason != "killed" {
		t.Fatalf("已有精确原因不应被改写: %+v", a)
	}

	// 索引里没有的会话补建一行（主机重启后丢失的会话仍可回放）。
	at := t0.Add(-time.Hour)
	if err := st.RecordTerminalSessionEnds(ctx, "h", []TerminalSessionEnd{
		{SID: sidC, Name: "old", StartedAt: 42, EndedAt: at, Reason: "host_reboot"},
	}, t0); err != nil {
		t.Fatal(err)
	}
	c, _ := st.GetTerminalSession(ctx, "h", sidC)
	if c == nil || c.Name != "old" || c.StartedAt != 42 || c.EndedReason != "host_reboot" || c.EndedAt == nil || !c.EndedAt.Equal(at) {
		t.Fatalf("应补建丢失会话: %+v", c)
	}
}

func TestListHistoryHidesPurgedAndPrunes(t *testing.T) {
	st := newTerminalTestStore(t)
	ctx := context.Background()
	t0 := time.Unix(1_700_000_000, 0)
	_ = st.RecordTerminalSessionEnds(ctx, "h", []TerminalSessionEnd{
		{SID: sidA, Name: "old", EndedAt: t0.Add(-40 * 24 * time.Hour), Reason: "exit"},
		{SID: sidB, Name: "recent", EndedAt: t0.Add(-time.Hour), Reason: "exit"},
		{SID: sidC, Name: "gone", EndedAt: t0.Add(-time.Minute), Reason: "exit"},
	}, t0)
	if err := st.MarkTerminalSessionPurged(ctx, "h", sidC); err != nil {
		t.Fatal(err)
	}
	rows, _ := st.ListTerminalSessions(ctx, "h", true, 0)
	names := []string{}
	for _, r := range rows {
		names = append(names, r.Name)
	}
	if got := strings.Join(names, ","); got != "recent,old" {
		t.Fatalf("历史列表应隐藏记录已被清理的会话（且最近创建在前）: %s", got)
	}

	n, err := st.PruneTerminalSessions(ctx, t0.Add(-30*24*time.Hour))
	if err != nil || n != 1 {
		t.Fatalf("应清理 1 行 30 天前结束的索引: n=%d err=%v", n, err)
	}
	if err := st.DeleteTerminalSessionsOfHost(ctx, "h"); err != nil {
		t.Fatal(err)
	}
	rows, _ = st.ListTerminalSessions(ctx, "h", false, 0)
	if len(rows) != 0 {
		t.Fatalf("删除主机后不应残留索引: %+v", rows)
	}
}

func TestSetTerminalHostRecOff(t *testing.T) {
	st := newTerminalTestStore(t)
	ctx := context.Background()
	if err := st.SaveTerminalHost(ctx, sampleHost("h1")); err != nil {
		t.Fatal(err)
	}
	if ch, err := st.SetTerminalHostRecOff(ctx, "h1", true); err != nil || !ch {
		t.Fatalf("首次关闭应有变化: %v %v", ch, err)
	}
	if ch, _ := st.SetTerminalHostRecOff(ctx, "h1", true); ch {
		t.Fatal("重复设置应无变化")
	}
	h, _ := st.GetTerminalHost(ctx, "h1")
	if !h.RecOff {
		t.Fatalf("应已关闭记录: %+v", h)
	}
	if _, err := st.SetTerminalHostRecOff(ctx, "nope", true); err == nil {
		t.Fatal("不存在的主机应报错")
	}
}
