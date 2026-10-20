package terminal

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"
)

const guardPermitPath = "/api/v1/terminal/launchers/p1/guard/permit"

func (g *guardEnv) setPermit(id string, permit int, note string) apiResult {
	g.t.Helper()
	body := map[string]any{"permit": permit}
	if note != "" {
		body["note"] = note
	}
	return g.call("PUT", "/api/v1/terminal/launchers/"+id+"/guard/permit", body, g.admin()...)
}

func TestGuardPermitRESTAuthAndValidation(t *testing.T) {
	g := newGuardEnv(t)
	g.savePreset("p1", guardPresetName, map[string]any{"enabled": true, "workflow_id": "wf-a"})

	if res := g.call("PUT", guardPermitPath, map[string]any{"permit": 0}); res.Status != http.StatusUnauthorized && res.Status != http.StatusForbidden {
		t.Fatalf("未登录: %d %s", res.Status, res.Raw)
	}
	if res := g.call("PUT", guardPermitPath, map[string]any{"permit": 0}, loginHdr()...); res.Status != http.StatusForbidden || res.str("code") != "unlock_required" {
		t.Fatalf("未解锁: %d %s", res.Status, res.Raw)
	}
	if res := g.call("PUT", guardPermitPath, map[string]any{"permit": 0}, "Authorization", "Bearer mcp-valid-1", unlockHeader, g.unlock(testUser)); res.Status != http.StatusForbidden || res.str("code") != "mcp_token_forbidden" {
		t.Fatalf("MCP 令牌: %d %s", res.Status, res.Raw)
	}
	if res := g.setPermit("p1", 9, ""); res.Status != http.StatusBadRequest {
		t.Fatalf("非法 permit: %d %s", res.Status, res.Raw)
	}
	if res := g.setPermit("missing", 0, ""); res.Status != http.StatusNotFound {
		t.Fatalf("预设不存在: %d %s", res.Status, res.Raw)
	}
	g.savePreset("p2", "NoGuard", nil)
	if res := g.setPermit("p2", 0, ""); res.Status != http.StatusConflict || res.str("code") != "guard_not_enabled" {
		t.Fatalf("未开守护: %d %s", res.Status, res.Raw)
	}
}

func TestGuardPermitRESTMarkUnmarkAndGuardList(t *testing.T) {
	g := newGuardEnv(t)
	g.savePreset("p1", guardPresetName, map[string]any{"enabled": true, "workflow_id": "wf-a"})
	g.online()

	res := g.setPermit("p1", 0, "备注不进审计")
	if res.Status != http.StatusOK || res.Body["permit"] == nil {
		t.Fatalf("标记: %d %s", res.Status, res.Raw)
	}
	items := g.listGuard()
	if len(items) != 1 || items[0]["permit_marked"] != true {
		t.Fatalf("GET /guard 应体现 permit_marked: %v", items[0])
	}

	logs := g.auditEvents(EvGuardPermit)
	if len(logs) != 1 || logs[0].Detail != "preset=p1 permit=0 by=human" {
		t.Fatalf("审计: %+v", logs)
	}
	for _, l := range logs {
		if strings.Contains(l.Detail, "备注") || strings.Contains(l.Detail, "note") {
			t.Fatalf("审计不含 note: %+v", l)
		}
	}

	if res := g.setPermit("p1", 0, ""); res.Status != http.StatusOK {
		t.Fatalf("重复标记应幂等: %d %s", res.Status, res.Raw)
	}
	if res := g.setPermit("p1", 1, ""); res.Status != http.StatusOK {
		t.Fatalf("取消标记: %d %s", res.Status, res.Raw)
	}
	items = g.listGuard()
	if items[0]["permit_marked"] == true {
		t.Fatalf("取消后不应 marked: %v", items[0])
	}
	if n := len(g.auditEvents(EvGuardPermit)); n < 2 {
		t.Fatalf("至少 2 条 guard_permit 审计（含取消标记）: %d", n)
	}
}

func TestGuardTickPermitMarkedNoAlertButConsumes(t *testing.T) {
	g := newGuardEnv(t)
	g.savePreset("p1", guardPresetName, map[string]any{"enabled": true, "workflow_id": "wf-a"})
	g.online()
	if res := g.setPermit("p1", 0, ""); res.Status != http.StatusOK {
		t.Fatalf("标记: %d %s", res.Status, res.Raw)
	}

	code := 3
	g.index(sidOf('a'), guardPresetName, g.clock.Now().Add(-time.Hour), true, &code)
	g.clock.Advance(time.Minute)
	g.tick()

	if n := len(g.alerts.all()); n != 0 {
		t.Fatalf("标记期间意外结束不应推送: %+v", g.alerts.all())
	}
	items := g.listGuard()
	if items[0]["verdict"].(map[string]any)["state"] != string(GuardDone) {
		t.Fatalf("应显示 done: %v", items[0]["verdict"])
	}
	st := g.guardState().Presets["p1"]
	if st == nil || st.Permit == nil || st.Permit.Permit != 1 {
		t.Fatalf("消费后许可应回到 1: %+v", st.Permit)
	}
	if st.Permit.SuppressedEpisode == "" {
		t.Fatal("应记下 suppressed_episode")
	}

	g.clock.Advance(time.Minute)
	g.tick()
	if n := len(g.alerts.all()); n != 0 {
		t.Fatalf("同一 episode 不应再告警: %d", n)
	}
}

func TestGuardTickPermitNewEpisodeAlertsAfterConsume(t *testing.T) {
	g := newGuardEnv(t)
	g.savePreset("p1", guardPresetName, map[string]any{"enabled": true, "workflow_id": "wf-a"})
	g.online()
	g.setPermit("p1", 0, "")
	code := 3
	g.index(sidOf('a'), guardPresetName, g.clock.Now().Add(-2*time.Hour), true, &code)
	g.clock.Advance(time.Minute)
	g.tick()
	if st := g.guardState().Presets["p1"]; st.Permit.Permit != 1 {
		t.Fatal("第一次应已消费")
	}

	code2 := 5
	g.index(sidOf('b'), guardPresetName, g.clock.Now().Add(-time.Hour), true, &code2)
	g.clock.Advance(time.Minute)
	g.tick()
	if n := len(g.alerts.all()); n != 1 {
		t.Fatalf("新 episode 应告警: %+v", g.alerts.all())
	}
}

func TestSetWakePermitIdempotentWrites(t *testing.T) {
	g := newGuardEnv(t)
	g.savePreset("p1", guardPresetName, map[string]any{"enabled": true, "workflow_id": "wf-a"})
	ctx := context.Background()
	now := g.clock.Now()
	p1, err := g.hub.guardian.SetWakePermit(ctx, "p1", 0, "agent", "n1", now)
	if err != nil || p1.Permit != 0 {
		t.Fatalf("set 0: %v %+v", err, p1)
	}
	at := p1.SetAt
	p2, err := g.hub.guardian.SetWakePermit(ctx, "p1", 0, "agent", "n2", now.Add(time.Minute))
	if err != nil || !p2.SetAt.After(at) {
		t.Fatalf("再次 set 0 应刷新 SetAt: was %v now %v", at, p2.SetAt)
	}
	p3, err := g.hub.guardian.SetWakePermit(ctx, "p1", 1, "human", "", now.Add(2*time.Minute))
	if err != nil || p3.Permit != 1 {
		t.Fatalf("set 1: %v %+v", err, p3)
	}
	p4, err := g.hub.guardian.SetWakePermit(ctx, "p1", 1, "human", "", now.Add(3*time.Minute))
	if err != nil || p4.Permit != 1 {
		t.Fatalf("重复 set 1: %v %+v", err, p4)
	}
}
