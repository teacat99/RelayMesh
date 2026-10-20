package store

import (
	"testing"
	"time"
)

func TestNeedsHookInstallHint(t *testing.T) {
	s := &Store{}
	now := time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)
	wf := "wf-hint"

	if !s.NeedsHookInstallHint(wf, now) {
		t.Fatal("无 hook 痕迹时应提示")
	}
	s.MarkHookInstallHint(wf, now)
	if s.NeedsHookInstallHint(wf, now.Add(time.Hour)) {
		t.Fatal("6h 内不应重复提示")
	}
	if !s.NeedsHookInstallHint(wf, now.Add(7*time.Hour)) {
		t.Fatal("超过 6h 且无 hook 应再提示")
	}
	s.RecordHookSeen(wf, now.Add(7*time.Hour))
	if s.NeedsHookInstallHint(wf, now.Add(8*time.Hour)) {
		t.Fatal("有 hook 痕迹时不应提示")
	}
	s.activity.mu.Lock()
	delete(s.activity.hook, wf)
	s.activity.mu.Unlock()
	if !s.NeedsHookInstallHint(wf, now.Add(25*time.Hour)) {
		t.Fatal("hook 超过 24h 无活动应再提示")
	}
}
