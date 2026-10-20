package store

import (
	"context"
	"testing"
	"time"

	"github.com/teacat99/RelayMesh/v2/internal/model"
)

func sampleRun(id, thread, target, state string) *model.TerminalRun {
	return &model.TerminalRun{
		RunID: id, ThreadKey: thread, Target: target, State: state,
		CredentialID: 7, CredentialName: "tok", Command: "echo hi", ActorKind: "model",
	}
}

func TestTerminalRunCRUDAndFilter(t *testing.T) {
	st := newMemStore(t)
	ctx := context.Background()

	for i, r := range []*model.TerminalRun{
		sampleRun("a1", "t1", "hostA", model.RunRunning),
		sampleRun("a2", "t1", "hostA", model.RunSucceeded),
		sampleRun("b1", "t2", "hostB", model.RunAwaitingApproval),
	} {
		r.IdempotencyKey = ""
		if i == 1 {
			r.IdempotencyKey = "idem-1"
		}
		if err := st.CreateTerminalRun(ctx, r); err != nil {
			t.Fatalf("create %s: %v", r.RunID, err)
		}
	}

	got, err := st.GetTerminalRun(ctx, "a1")
	if err != nil || got == nil || got.Target != "hostA" {
		t.Fatalf("get a1 = %+v, %v", got, err)
	}
	if miss, err := st.GetTerminalRun(ctx, "zzz"); err != nil || miss != nil {
		t.Fatalf("missing run should be (nil,nil), got %+v, %v", miss, err)
	}

	list, err := st.ListTerminalRuns(ctx, RunFilter{Target: "hostA"})
	if err != nil || len(list) != 2 || list[0].RunID != "a2" {
		t.Fatalf("list hostA = %+v, %v", list, err)
	}
	list, _ = st.ListTerminalRuns(ctx, RunFilter{ThreadKey: "t2"})
	if len(list) != 1 || list[0].RunID != "b1" {
		t.Fatalf("list thread t2 = %+v", list)
	}
	list, _ = st.ListTerminalRuns(ctx, RunFilter{States: []string{model.RunRunning, model.RunAwaitingApproval}})
	if len(list) != 2 {
		t.Fatalf("list active states = %d, want 2", len(list))
	}
	page, _ := st.ListTerminalRuns(ctx, RunFilter{Limit: 1})
	if len(page) != 1 {
		t.Fatalf("limit 1 returned %d", len(page))
	}
	rest, _ := st.ListTerminalRuns(ctx, RunFilter{BeforeID: page[0].ID})
	if len(rest) != 2 {
		t.Fatalf("keyset page returned %d, want 2", len(rest))
	}

	if dup, err := st.FindTerminalRunByIdempotency(ctx, 7, "idem-1"); err != nil || dup == nil || dup.RunID != "a2" {
		t.Fatalf("idempotency lookup = %+v, %v", dup, err)
	}
	if none, _ := st.FindTerminalRunByIdempotency(ctx, 8, "idem-1"); none != nil {
		t.Fatalf("idempotency must be scoped per credential")
	}
	if none, _ := st.FindTerminalRunByIdempotency(ctx, 7, ""); none != nil {
		t.Fatalf("empty key must never match")
	}

	active, err := st.ListActiveTerminalRuns(ctx)
	if err != nil || len(active) != 2 {
		t.Fatalf("active = %d, %v", len(active), err)
	}
}

func TestTerminalRunConditionalUpdateKeepsSingleFinalState(t *testing.T) {
	st := newMemStore(t)
	ctx := context.Background()
	if err := st.CreateTerminalRun(ctx, sampleRun("r1", "t", "h", model.RunRunning)); err != nil {
		t.Fatal(err)
	}
	code := 0
	now := time.Now()
	hit, err := st.UpdateTerminalRunIf(ctx, "r1", []string{model.RunRunning}, map[string]any{
		"state": model.RunSucceeded, "exit_code": &code, "ended_at": &now,
	})
	if err != nil || !hit {
		t.Fatalf("first transition = %v, %v", hit, err)
	}
	// 终态之后再来一个迟到的「取消」不应覆盖。
	hit, err = st.UpdateTerminalRunIf(ctx, "r1", []string{model.RunRunning, model.RunAwaitingApproval}, map[string]any{"state": model.RunCancelled})
	if err != nil || hit {
		t.Fatalf("late transition must be ignored, got hit=%v err=%v", hit, err)
	}
	got, _ := st.GetTerminalRun(ctx, "r1")
	if got.State != model.RunSucceeded || got.ExitCode == nil || *got.ExitCode != 0 {
		t.Fatalf("final state corrupted: %+v", got)
	}
}

func TestTerminalRunPruneAndHostDelete(t *testing.T) {
	st := newMemStore(t)
	ctx := context.Background()
	old := time.Now().Add(-40 * 24 * time.Hour)
	recent := time.Now().Add(-time.Hour)

	oldRun := sampleRun("old", "t", "h1", model.RunSucceeded)
	oldRun.EndedAt = &old
	newRun := sampleRun("new", "t", "h1", model.RunSucceeded)
	newRun.EndedAt = &recent
	activeRun := sampleRun("act", "t", "h2", model.RunRunning)
	for _, r := range []*model.TerminalRun{oldRun, newRun, activeRun} {
		if err := st.CreateTerminalRun(ctx, r); err != nil {
			t.Fatal(err)
		}
	}
	n, err := st.PruneTerminalRuns(ctx, time.Now().Add(-30*24*time.Hour))
	if err != nil || n != 1 {
		t.Fatalf("prune = %d, %v; want 1", n, err)
	}
	if g, _ := st.GetTerminalRun(ctx, "old"); g != nil {
		t.Fatalf("old run should be pruned")
	}
	if g, _ := st.GetTerminalRun(ctx, "act"); g == nil {
		t.Fatalf("unfinished run must never be pruned")
	}
	if err := st.DeleteTerminalRunsOfHost(ctx, "h1"); err != nil {
		t.Fatal(err)
	}
	if g, _ := st.GetTerminalRun(ctx, "new"); g != nil {
		t.Fatalf("host delete should remove its runs")
	}
}

func TestSetTerminalHostModelAndFlags(t *testing.T) {
	st := newMemStore(t)
	ctx := context.Background()
	if err := st.SaveTerminalHost(ctx, sampleHost("host-1")); err != nil {
		t.Fatal(err)
	}
	yes := true
	h, changed, err := st.SetTerminalHostModel(ctx, "host-1", HostModelUpdate{AllowModel: &yes})
	if err != nil || !changed || !h.AllowModel || h.ExecMode != "" || h.ModelPaused {
		t.Fatalf("allow = %+v changed=%v err=%v", h, changed, err)
	}
	_, changed, _ = st.SetTerminalHostModel(ctx, "host-1", HostModelUpdate{AllowModel: &yes})
	if changed {
		t.Fatalf("repeating the same value must report unchanged")
	}
	mode := model.TerminalModeApprove
	h, changed, err = st.SetTerminalHostModel(ctx, "host-1", HostModelUpdate{ExecMode: &mode, ModelPaused: &yes})
	if err != nil || !changed || h.ExecMode != "approve" || !h.ModelPaused || !h.AllowModel {
		t.Fatalf("mode+pause = %+v changed=%v err=%v", h, changed, err)
	}
	bad := "root"
	if _, _, err := st.SetTerminalHostModel(ctx, "host-1", HostModelUpdate{ExecMode: &bad}); err == nil {
		t.Fatalf("invalid exec mode must be rejected")
	}
	if _, _, err := st.SetTerminalHostModel(ctx, "nope", HostModelUpdate{AllowModel: &yes}); err != ErrTerminalHostNotFound {
		t.Fatalf("missing host err = %v", err)
	}
	got, _ := st.GetTerminalHost(ctx, "host-1")
	if !got.AllowModel || got.ExecMode != "approve" || !got.ModelPaused {
		t.Fatalf("persisted host = %+v", got)
	}

	if on, err := st.GetSystemFlag(ctx, SettingTerminalExecPaused); err != nil || on {
		t.Fatalf("default flag = %v, %v", on, err)
	}
	if err := st.SetSystemFlag(ctx, SettingTerminalExecPaused, true); err != nil {
		t.Fatal(err)
	}
	if on, _ := st.GetSystemFlag(ctx, SettingTerminalExecPaused); !on {
		t.Fatalf("flag should be on")
	}
	_ = st.SetSystemFlag(ctx, SettingTerminalExecPaused, false)
	if on, _ := st.GetSystemFlag(ctx, SettingTerminalExecPaused); on {
		t.Fatalf("flag should be off again")
	}
}
