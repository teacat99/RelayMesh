package store

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestGuardPermitPersistsAcrossStoreReopen(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "relaymesh.db")
	ctx := context.Background()
	now := time.Date(2026, 10, 13, 8, 0, 0, 0, time.UTC)

	st1, err := New(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	out, err := st1.UpdateTerminalGuardState(ctx, now, func(d *GuardStateDoc) {
		d.Preset("p1").Permit = &GuardWakePermitState{
			Permit: 0, SetAt: now, SetBy: "agent", Note: "secret", SuppressedEpisode: "crashed:s1:3",
		}
	})
	if err != nil {
		t.Fatalf("write: %v", err)
	}
	if out.Presets["p1"].Permit.Note != "secret" {
		t.Fatal("note 应落库")
	}
	if err := st1.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	st2, err := New(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	t.Cleanup(func() { _ = st2.Close() })
	got, err := st2.GetTerminalGuardState(ctx)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	p := got.Presets["p1"]
	if p == nil || p.Permit == nil || p.Permit.Permit != 0 || p.Permit.SetBy != "agent" || p.Permit.SuppressedEpisode != "crashed:s1:3" {
		t.Fatalf("重开应读到许可: %+v", p)
	}

	_, err = st2.UpdateTerminalGuardState(ctx, now.Add(time.Minute), func(d *GuardStateDoc) {
		st := d.Preset("p1")
		st.Permit.Permit = 1
		st.Permit.Note = ""
	})
	if err != nil {
		t.Fatalf("consume write: %v", err)
	}
	got2, _ := st2.GetTerminalGuardState(ctx)
	if got2.Presets["p1"].Permit.Permit != 1 || got2.Presets["p1"].Permit.Note != "" {
		t.Fatalf("消费落库: %+v", got2.Presets["p1"].Permit)
	}
}

func TestGuardPermitConcurrentUpdates(t *testing.T) {
	st := newMemStore(t)
	ctx := context.Background()
	base := time.Date(2026, 10, 13, 9, 0, 0, 0, time.UTC)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			id := []string{"pa", "pb", "pc"}[i%3]
			permit := i % 2
			_, _ = st.UpdateTerminalGuardState(ctx, base.Add(time.Duration(i)*time.Millisecond), func(d *GuardStateDoc) {
				d.Preset(id).Permit = &GuardWakePermitState{Permit: permit, SetAt: base, SetBy: "test"}
			})
		}(i)
	}
	wg.Wait()
	doc, err := st.GetTerminalGuardState(ctx)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if len(doc.Presets) == 0 {
		t.Fatal("并发写入后应有预设状态")
	}
	for id, p := range doc.Presets {
		if p.Permit == nil {
			t.Fatalf("%s 缺少 permit", id)
		}
	}
}
