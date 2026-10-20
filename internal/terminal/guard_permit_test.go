package terminal

import (
	"testing"
	"time"

	"github.com/teacat99/RelayMesh/v2/internal/store"
)

func TestApplyWakePermitMarkedToDone(t *testing.T) {
	f := baseFacts()
	f.Session, f.Ended = nil, &guardEnd{SID: "s", At: ago(time.Hour), Reason: "exit", ExitCode: ptrInt(2)}
	raw := evaluateGuard(f)
	pf := guardPermitFacts{Marked: true, SetAt: ago(10 * time.Minute)}
	out, eff := applyWakePermit(raw, f, pf)
	if out.State != GuardDone {
		t.Fatalf("state=%s", out.State)
	}
	if eff.Consume && eff.SuppressEpisode == "" {
		t.Fatal("expected suppress episode on consume")
	}
}

func TestApplyWakePermitWorkingUnchanged(t *testing.T) {
	f := baseFacts()
	raw := evaluateGuard(f)
	pf := guardPermitFacts{Marked: true, SetAt: ago(time.Minute)}
	out, _ := applyWakePermit(raw, f, pf)
	if out.State != GuardWorking {
		t.Fatalf("state=%s", out.State)
	}
}

func TestApplyWakePermitClearOnNewInbox(t *testing.T) {
	f := baseFacts()
	f.Session, f.Ended = nil, &guardEnd{SID: "s", At: ago(time.Hour), Reason: "exit", ExitCode: ptrInt(1)}
	f.InboxUnread = 1
	f.InboxOldestUnread = ago(5 * time.Minute)
	pf := guardPermitFacts{Marked: true, SetAt: ago(time.Hour)}
	_, eff := applyWakePermit(evaluateGuard(f), f, pf)
	if !eff.ClearMark {
		t.Fatal("expected clear on new inbox after mark")
	}
}

func TestApplyWakePermitSuppressedEpisode(t *testing.T) {
	f := baseFacts()
	f.Session, f.Ended = nil, &guardEnd{SID: "s", At: ago(time.Hour), Reason: "host_reboot"}
	raw := evaluateGuard(f)
	ep := raw.Episode
	pf := guardPermitFacts{SuppressedEpisode: ep}
	out, _ := applyWakePermit(raw, f, pf)
	if out.State != GuardDone {
		t.Fatalf("state=%s", out.State)
	}
}

func TestGuardPresetStatePermitRoundTrip(t *testing.T) {
	st := store.GuardWakePermitState{Permit: 0, SetAt: time.Now(), SetBy: "agent", Note: "done"}
	if st.Note == "" {
		t.Fatal("note stored")
	}
}
