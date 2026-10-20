package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/teacat99/RelayMesh/v2/internal/store"
)

// storeGuardHub 用 store 模拟 Hub 的 SetGuardWakePermit（只测 MCP 路由与参数，不测终端 REST 栈）。
type storeGuardHub struct {
	st *store.Store
}

func (h storeGuardHub) SetGuardWakePermit(ctx context.Context, presetID string, permit int, by, note string) (*store.GuardWakePermitState, error) {
	if permit != 0 && permit != 1 {
		return nil, errBadPermit
	}
	launchers, err := h.st.ListTerminalLaunchers(ctx)
	if err != nil {
		return nil, err
	}
	ok := false
	for _, l := range launchers {
		if l.ID == presetID && l.Guard != nil && l.Guard.Enabled {
			ok = true
			break
		}
	}
	if !ok {
		return nil, errGuardNotEnabled
	}
	var out *store.GuardWakePermitState
	now := time.Now()
	_, err = h.st.UpdateTerminalGuardState(ctx, now, func(d *store.GuardStateDoc) {
		st := d.Preset(presetID)
		if permit == 0 {
			st.Permit = &store.GuardWakePermitState{Permit: 0, SetAt: now, SetBy: by, Note: note}
		} else {
			st.Permit = &store.GuardWakePermitState{Permit: 1}
		}
		out = st.Permit
	})
	return out, err
}

var (
	errBadPermit       = errString("bad permit")
	errGuardNotEnabled = errString("guard not enabled")
)

type errString string

func (e errString) Error() string { return string(e) }

func seedGuardLauncher(t *testing.T, st *store.Store, id, wf string) {
	t.Helper()
	ctx := context.Background()
	_, _, err := st.UpsertTerminalLauncher(ctx, store.TerminalLauncher{
		ID: id, Name: "Guard", Host: "RM-H3B4-5AW2-4DPP-ZAW6", Dir: "/tmp", Command: "agent",
		Guard: &store.LauncherGuard{Enabled: true, WorkflowID: wf},
	})
	if err != nil {
		t.Fatalf("launcher: %v", err)
	}
}

func TestWakePermitMCPNoBindingFriendlySkip(t *testing.T) {
	srv := setupTestMCPServer(t)
	raw := json.RawMessage(`{"action":"wake_permit","workflow_id":"wf-none","permit":0}`)
	out, err := srv.handleWorkflowContext(context.Background(), raw)
	if err != nil {
		t.Fatalf("handle: %v", err)
	}
	m, _ := out.(map[string]any)
	if m["status"] != "skipped" || !strings.Contains(m["message"].(string), "没有绑定守护") {
		t.Fatalf("unexpected: %+v", out)
	}
}

func TestWakePermitMCPSetAndRepeat(t *testing.T) {
	srv := setupTestMCPServer(t)
	seedGuardLauncher(t, srv.store, "p1", "wf-permit")
	srv.SetTerminalGuardHub(storeGuardHub{st: srv.store})

	for _, permit := range []int{0, 1, 0} {
		raw := json.RawMessage(fmt.Sprintf(`{"action":"wake_permit","workflow_id":"wf-permit","permit":%d}`, permit))
		out, err := srv.handleWorkflowContext(context.Background(), raw)
		if err != nil {
			t.Fatalf("permit=%d: %v", permit, err)
		}
		m, _ := out.(map[string]any)
		if m["status"] != "ok" || m["preset_id"] != "p1" {
			t.Fatalf("permit=%d: %+v", permit, out)
		}
	}
}

func TestWakePermitMCPNoteNotInAuditPath(t *testing.T) {
	srv := setupTestMCPServer(t)
	seedGuardLauncher(t, srv.store, "p1", "wf-note")
	srv.SetTerminalGuardHub(storeGuardHub{st: srv.store})
	raw := json.RawMessage(`{"action":"wake_permit","workflow_id":"wf-note","permit":0,"note":"机密备注"}`)
	out, err := srv.handleWorkflowContext(context.Background(), raw)
	if err != nil {
		t.Fatalf("handle: %v", err)
	}
	st, _ := srv.store.GetTerminalGuardState(context.Background())
	if st.Presets["p1"].Permit.Note != "机密备注" {
		t.Fatal("note 应存库供界面用")
	}
	_ = out // MCP 回执可带 permit 结构；审计不含 note 由 REST TestGuardPermitREST 覆盖
}

func TestWakePermitMCPRequiresPermitAndHub(t *testing.T) {
	srv := setupTestMCPServer(t)
	seedGuardLauncher(t, srv.store, "p1", "wf-x")
	if _, err := srv.handleWakePermit(context.Background(), workflowContextArgs{WorkflowID: "wf-x"}); err == nil || !strings.Contains(err.Error(), "permit is required") {
		t.Fatalf("缺 permit: %v", err)
	}
	srv.SetTerminalGuardHub(storeGuardHub{st: srv.store})
	if _, err := srv.handleWakePermit(context.Background(), workflowContextArgs{WorkflowID: "wf-x", Permit: flexInt{V: 2, Set: true}}); err == nil || !strings.Contains(err.Error(), "0 or 1") {
		t.Fatalf("bad permit: %v", err)
	}
	srv.SetTerminalGuardHub(nil)
	if _, err := srv.handleWakePermit(context.Background(), workflowContextArgs{WorkflowID: "wf-x", Permit: flexInt{V: 0, Set: true}}); err == nil || !strings.Contains(err.Error(), "terminal hub not available") {
		t.Fatalf("无 hub: %v", err)
	}
}
