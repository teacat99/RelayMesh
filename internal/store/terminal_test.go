package store

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/teacat99/RelayMesh/v2/internal/model"
)

func newTerminalTestStore(t *testing.T) *Store {
	t.Helper()
	st, err := New(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func sampleHost(code string) *model.TerminalHost {
	now := time.Now()
	return &model.TerminalHost{
		MachineCode: code,
		PublicKey:   "pk-" + code,
		Remark:      "dev box",
		Status:      "enabled",
		Hostname:    "box",
		OS:          "linux",
		Arch:        "amd64",
		Privilege:   "root",
		EnabledAt:   &now,
	}
}

func TestTerminalHostLifecycle(t *testing.T) {
	ctx := context.Background()
	st := newTerminalTestStore(t)

	if h, err := st.GetTerminalHost(ctx, "RM-NONE"); err != nil || h != nil {
		t.Fatalf("不存在应返回 (nil,nil): %v %v", h, err)
	}

	h := sampleHost("RM-AAAA-BBBB-CCCC-DDDD")
	if err := st.SaveTerminalHost(ctx, h); err != nil {
		t.Fatal(err)
	}
	got, err := st.GetTerminalHost(ctx, h.MachineCode)
	if err != nil || got == nil || got.Remark != "dev box" || got.Status != "enabled" || got.PublicKey != "pk-"+h.MachineCode {
		t.Fatalf("读取不符: %+v %v", got, err)
	}

	// 公钥一致时更新状态与备注
	update := sampleHost(h.MachineCode)
	update.Status = "disabled"
	update.Remark = "renamed"
	if err := st.SaveTerminalHost(ctx, update); err != nil {
		t.Fatal(err)
	}
	got, _ = st.GetTerminalHost(ctx, h.MachineCode)
	if got.Status != "disabled" || got.Remark != "renamed" {
		t.Fatalf("更新未生效: %+v", got)
	}

	// 公钥不一致必须拒绝（机器码是公钥指纹，不一致意味着异常）
	evil := sampleHost(h.MachineCode)
	evil.PublicKey = "another-key"
	if err := st.SaveTerminalHost(ctx, evil); !errors.Is(err, ErrTerminalKeyMismatch) {
		t.Fatalf("应返回 ErrTerminalKeyMismatch: %v", err)
	}
	got, _ = st.GetTerminalHost(ctx, h.MachineCode)
	if got.PublicKey != "pk-"+h.MachineCode {
		t.Fatal("公钥不得被覆盖")
	}

	if ok, err := st.DeleteTerminalHost(ctx, h.MachineCode); err != nil || !ok {
		t.Fatalf("删除失败: %v %v", ok, err)
	}
	if ok, _ := st.DeleteTerminalHost(ctx, h.MachineCode); ok {
		t.Fatal("重复删除应返回 false")
	}
}

func TestTerminalHostStatusAndRemarkAndTouch(t *testing.T) {
	ctx := context.Background()
	st := newTerminalTestStore(t)
	h := sampleHost("RM-1111-2222-3333-4444")
	h.Status = "disabled"
	h.EnabledAt = nil
	if err := st.SaveTerminalHost(ctx, h); err != nil {
		t.Fatal(err)
	}

	changed, err := st.SetTerminalHostStatus(ctx, h.MachineCode, "enabled")
	if err != nil || !changed {
		t.Fatalf("应发生变化: %v %v", changed, err)
	}
	got, _ := st.GetTerminalHost(ctx, h.MachineCode)
	if got.Status != "enabled" || got.EnabledAt == nil {
		t.Fatalf("启用应记录启用时间: %+v", got)
	}
	if changed, _ := st.SetTerminalHostStatus(ctx, h.MachineCode, "enabled"); changed {
		t.Fatal("状态未变不应报告变化")
	}
	if _, err := st.SetTerminalHostStatus(ctx, "RM-NOPE", "enabled"); !errors.Is(err, ErrTerminalHostNotFound) {
		t.Fatalf("不存在应返回 ErrTerminalHostNotFound: %v", err)
	}

	if err := st.SetTerminalHostRemark(ctx, h.MachineCode, "生产跳板机"); err != nil {
		t.Fatal(err)
	}
	if err := st.SetTerminalHostRemark(ctx, "RM-NOPE", "x"); !errors.Is(err, ErrTerminalHostNotFound) {
		t.Fatalf("不存在应返回 ErrTerminalHostNotFound: %v", err)
	}

	at := time.Now().Truncate(time.Second)
	if err := st.TouchTerminalHost(ctx, h.MachineCode, TerminalHostSeen{
		Hostname: "new-name", OS: "debian", Arch: "arm64", AgentVersion: "1.7.0", Privilege: "root", IP: "10.0.0.5", At: at,
	}); err != nil {
		t.Fatal(err)
	}
	got, _ = st.GetTerminalHost(ctx, h.MachineCode)
	if got.Remark != "生产跳板机" || got.Hostname != "new-name" || got.Arch != "arm64" || got.LastIP != "10.0.0.5" || got.LastSeenAt == nil {
		t.Fatalf("Touch 结果不符（不得改动备注与状态）: %+v", got)
	}
	if err := st.TouchTerminalHost(ctx, "RM-NOPE", TerminalHostSeen{At: at}); !errors.Is(err, ErrTerminalHostNotFound) {
		t.Fatalf("不存在应返回 ErrTerminalHostNotFound: %v", err)
	}

	hosts, err := st.ListTerminalHosts(ctx)
	if err != nil || len(hosts) != 1 {
		t.Fatalf("List: %v %d", err, len(hosts))
	}
}

func TestTerminalAuditAppendListPrune(t *testing.T) {
	ctx := context.Background()
	st := newTerminalTestStore(t)
	for i := 1; i <= 25; i++ {
		if err := st.AppendTerminalAudit(ctx, &model.TerminalAuditLog{
			Event: "test", Result: "ok", Detail: fmt.Sprintf("n=%d", i),
		}); err != nil {
			t.Fatal(err)
		}
	}

	page1, err := st.ListTerminalAudit(ctx, 10, 0)
	if err != nil || len(page1) != 10 || page1[0].Detail != "n=25" || page1[9].Detail != "n=16" {
		t.Fatalf("第一页应为最新 10 条倒序: %v len=%d first=%q", err, len(page1), firstDetail(page1))
	}
	page2, _ := st.ListTerminalAudit(ctx, 10, page1[9].ID)
	if len(page2) != 10 || page2[0].Detail != "n=15" {
		t.Fatalf("游标翻页不符: len=%d first=%q", len(page2), firstDetail(page2))
	}
	if all, _ := st.ListTerminalAudit(ctx, 9999, 0); len(all) != 25 {
		t.Fatalf("limit 被夹取后仍应返回全部 25 条，实际 %d", len(all))
	}

	removed, err := st.PruneTerminalAudit(ctx, 10)
	if err != nil || removed != 15 {
		t.Fatalf("应删除 15 条: %v %d", err, removed)
	}
	left, _ := st.ListTerminalAudit(ctx, 100, 0)
	if len(left) != 10 || left[0].Detail != "n=25" || left[9].Detail != "n=16" {
		t.Fatalf("应只保留最新 10 条: len=%d", len(left))
	}
	if removed, _ := st.PruneTerminalAudit(ctx, 10); removed != 0 {
		t.Fatalf("重复修剪不应再删除: %d", removed)
	}
}

// 项目管理 Agent 运行预设（#179）的频率账本读 ListTerminalAuditSince：只认事件名与时间下限，时间正序，limit 被夹取。
func TestListTerminalAuditSince(t *testing.T) {
	ctx := context.Background()
	st := newTerminalTestStore(t)
	base := time.Date(2026, 10, 19, 8, 0, 0, 0, time.UTC)
	addAs := func(event, result string, at time.Time, detail string) {
		t.Helper()
		if err := st.AppendTerminalAudit(ctx, &model.TerminalAuditLog{Event: event, Result: result, Detail: detail, CreatedAt: at}); err != nil {
			t.Fatal(err)
		}
	}
	add := func(event string, at time.Time, detail string) { addAs(event, "ok", at, detail) }
	add("launcher_agent_run", base.Add(-26*time.Hour), "old")
	add("launcher_run", base.Add(-10*time.Minute), "other-event")
	add("launcher_agent_run", base.Add(-3*time.Hour), "a")
	add("launcher_agent_run", base.Add(-30*time.Minute), "b")
	add("launcher_agent_run", base, "c")

	got, err := st.ListTerminalAuditSince(ctx, "launcher_agent_run", "", base.Add(-24*time.Hour), 100)
	if err != nil || len(got) != 3 || got[0].Detail != "a" || got[1].Detail != "b" || got[2].Detail != "c" {
		t.Fatalf("应只返回 24 小时内、同名事件，时间正序：%v %+v", err, got)
	}
	if got, _ := st.ListTerminalAuditSince(ctx, "launcher_agent_run", "", base.Add(-time.Hour), 100); len(got) != 2 {
		t.Fatalf("时间下限没生效：%d", len(got))
	}
	if got, _ := st.ListTerminalAuditSince(ctx, "no_such_event", "", base.Add(-24*time.Hour), 100); len(got) != 0 {
		t.Fatalf("没有这个事件应是空：%d", len(got))
	}
	// limit<=0 取默认值，而不是返回空。
	if got, _ := st.ListTerminalAuditSince(ctx, "launcher_agent_run", "", base.Add(-24*time.Hour), 0); len(got) != 3 {
		t.Fatalf("limit<=0 应取默认值：%d", len(got))
	}

	// 按结果过滤：被拒绝与失败的尝试不进成功的清单。
	addAs("launcher_agent_run", "deny", base.Add(time.Minute), "denied-1")
	addAs("launcher_agent_run", "fail", base.Add(2*time.Minute), "failed-1")
	if got, _ := st.ListTerminalAuditSince(ctx, "launcher_agent_run", "ok", base.Add(-24*time.Hour), 100); len(got) != 3 {
		t.Fatalf("只取 ok 应是 3 条，得到 %d", len(got))
	}
	if got, _ := st.ListTerminalAuditSince(ctx, "launcher_agent_run", "deny", base.Add(-24*time.Hour), 100); len(got) != 1 || got[0].Detail != "denied-1" {
		t.Fatalf("只取 deny 不对：%+v", got)
	}

	// 条数超过 limit：留下最新的、丢最旧的，结果仍是时间正序（反过来会让一批被拒绝的尝试把成功记录挤出窗口，限额失效）。
	if got, _ := st.ListTerminalAuditSince(ctx, "launcher_agent_run", "", base.Add(-24*time.Hour), 2); len(got) != 2 || got[0].Detail != "denied-1" || got[1].Detail != "failed-1" {
		t.Fatalf("超过 limit 应留下最新的两条并保持正序：%+v", got)
	}
	// 上限 500：传更大的数被夹取，不是无限也不是回退成默认。
	for i := 0; i < 520; i++ {
		addAs("launcher_agent_run", "deny", base.Add(time.Duration(10+i)*time.Second), fmt.Sprintf("flood-%d", i))
	}
	got, _ = st.ListTerminalAuditSince(ctx, "launcher_agent_run", "", base.Add(-24*time.Hour), 100000)
	if len(got) != 500 || got[len(got)-1].Detail != "flood-519" {
		t.Fatalf("limit 应被夹取到 500 且是最新的 500 条：%d 末条 %q", len(got), got[len(got)-1].Detail)
	}
	if ok, _ := st.ListTerminalAuditSince(ctx, "launcher_agent_run", "ok", base.Add(-24*time.Hour), 500); len(ok) != 3 {
		t.Fatalf("被拒绝的洪水不该挤掉成功的记录：%d", len(ok))
	}
}

func firstDetail(l []model.TerminalAuditLog) string {
	if len(l) == 0 {
		return ""
	}
	return l[0].Detail
}

func TestTerminalHostConcurrentSaveIsSerialized(t *testing.T) {
	ctx := context.Background()
	st := newTerminalTestStore(t)
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = st.SaveTerminalHost(ctx, sampleHost("RM-CONC-CONC-CONC-CONC"))
		}()
	}
	wg.Wait()
	hosts, err := st.ListTerminalHosts(ctx)
	if err != nil || len(hosts) != 1 {
		t.Fatalf("并发保存同一机器码只应有一条记录: %v %d", err, len(hosts))
	}
}
