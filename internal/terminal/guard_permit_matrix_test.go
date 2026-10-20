package terminal

import (
	"testing"
	"time"

	"github.com/teacat99/RelayMesh/v2/internal/store"
)

// 唤醒许可状态机：底层 evaluateGuard × 许可事实 → 最终状态、告警、持久化建议。

func markedPF(setAt time.Time) guardPermitFacts {
	return guardPermitFacts{Marked: true, SetAt: setAt}
}

func factsCrashed(sid string, code int) guardFacts {
	f := baseFacts()
	f.Session = nil
	f.Ended = &guardEnd{SID: sid, At: ago(time.Hour), Reason: "exit", ExitCode: ptrInt(code)}
	return f
}

func factsStalled() guardFacts {
	f := baseFacts()
	f.Link = store.AgentLink{ConversationID: "c", LastToolAt: ago(time.Hour), StopAt: ago(10 * time.Minute), StopStatus: store.StopError}
	return f
}

func factsIdle() guardFacts {
	f := baseFacts()
	f.PresentSince = ago(2 * time.Hour)
	f.Link.LastToolAt = ago(3 * time.Hour)
	f.HookSeen = ago(3 * time.Hour)
	return f
}

func factsEndedNormal() guardFacts {
	f := baseFacts()
	f.Session, f.Ended = nil, &guardEnd{SID: "s", At: ago(time.Hour), Reason: "exit", ExitCode: ptrInt(0)}
	return f
}

func factsOffline() guardFacts {
	f := baseFacts()
	f.HostOnline, f.Session = false, nil
	return f
}

func factsUnknown() guardFacts {
	f := baseFacts()
	f.Session = nil
	return f
}

func runPermit(f guardFacts, pf guardPermitFacts) (GuardVerdict, permitEffect) {
	return applyWakePermit(evaluateGuard(f), f, pf)
}

func permitWouldAlert(v GuardVerdict, ps *store.GuardPresetState) bool {
	send, _ := guardAlertDue(v, ps, guardT0)
	return send
}

func TestGuardDoneNotAlerting(t *testing.T) {
	if GuardDone.Alerting() {
		t.Fatal("done 不应触发推送 / 红点")
	}
}

func TestWakePermitMatrixMarkedToDone(t *testing.T) {
	setAt := ago(10 * time.Minute)
	cases := []struct {
		name      string
		f         guardFacts
		wantState GuardState
		wantAlert bool
		wantCons  bool
	}{
		{"crashed→done 不告警且消费", factsCrashed("s1", 2), GuardDone, false, true},
		{"stalled→done 不告警且消费", factsStalled(), GuardDone, false, true},
		{"idle→done 不告警（无 episode 不消费）", factsIdle(), GuardDone, false, false},
		{"ended 正常退出→done 不告警", factsEndedNormal(), GuardDone, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			v, eff := runPermit(tc.f, markedPF(setAt))
			if v.State != tc.wantState {
				t.Fatalf("state=%s reason=%q", v.State, v.Reason)
			}
			if v.Episode != "" {
				t.Fatalf("done 应清空 episode，得到 %q", v.Episode)
			}
			if permitWouldAlert(v, nil) != tc.wantAlert {
				t.Fatalf("alert=%v", tc.wantAlert)
			}
			if eff.Consume != tc.wantCons {
				t.Fatalf("consume=%v suppress=%q", eff.Consume, eff.SuppressEpisode)
			}
			if tc.wantCons && eff.SuppressEpisode == "" {
				t.Fatal("消费应记下 suppressed_episode")
			}
		})
	}
}

func TestWakePermitMatrixWorkingWaitingPresentUnchanged(t *testing.T) {
	setAt := ago(time.Minute)
	cases := []struct {
		name string
		f    guardFacts
	}{
		{"working", baseFacts()},
		{"waiting", func() guardFacts {
			f := baseFacts()
			f.Feedback = &guardFeedback{Status: "pending", UpdatedAt: ago(time.Minute), DeadlineAt: ptrTime(guardT0.Add(4 * time.Minute)), MaxChecks: 24}
			return f
		}()},
		{"present", func() guardFacts {
			f := baseFacts()
			f.PresentSince = ago(5 * time.Minute)
			return f
		}()},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw := evaluateGuard(tc.f)
			v, eff := runPermit(tc.f, markedPF(setAt))
			if v.State != raw.State {
				t.Fatalf("标记期间不应改 %s → %s", raw.State, v.State)
			}
			if eff.Consume || eff.ClearMark {
				t.Fatalf("不应消费或清除: %+v", eff)
			}
		})
	}
}

func TestWakePermitMatrixOfflineUnknownUnchanged(t *testing.T) {
	setAt := ago(time.Minute)
	for name, f := range map[string]guardFacts{"offline": factsOffline(), "unknown": factsUnknown()} {
		t.Run(name, func(t *testing.T) {
			raw := evaluateGuard(f)
			v, eff := runPermit(f, markedPF(setAt))
			if v.State != raw.State {
				t.Fatalf("标记不改变 %s", v.State)
			}
			if eff.Consume {
				t.Fatal("不应消费")
			}
		})
	}
}

func TestWakePermitMatrixClearMark(t *testing.T) {
	setAt := ago(time.Hour)
	f := factsCrashed("s1", 1)
	t.Run("24h 过期", func(t *testing.T) {
		pf := guardPermitFacts{Marked: true, SetAt: guardT0.Add(-guardPermitMaxAge - time.Minute)}
		_, eff := runPermit(f, pf)
		if !eff.ClearMark {
			t.Fatalf("应清除: %+v", eff)
		}
	})
	t.Run("标记后唤醒", func(t *testing.T) {
		pf := markedPF(setAt)
		pf.WakeSinceMark = true
		_, eff := runPermit(f, pf)
		if !eff.ClearMark {
			t.Fatalf("应清除: %+v", eff)
		}
	})
	t.Run("标记后新收件箱", func(t *testing.T) {
		ff := f
		ff.InboxUnread = 1
		ff.InboxOldestUnread = ago(5 * time.Minute)
		_, eff := runPermit(ff, markedPF(setAt))
		if !eff.ClearMark {
			t.Fatalf("应清除: %+v", eff)
		}
	})
}

func TestWakePermitSameEpisodeNoRepeatAlert(t *testing.T) {
	f := factsCrashed("s1", 3)
	setAt := ago(10 * time.Minute)
	raw := evaluateGuard(f)
	v1, e1 := runPermit(f, markedPF(setAt))
	if v1.State != GuardDone || !e1.Consume {
		t.Fatalf("第一次: v=%s consume=%v", v1.State, e1.Consume)
	}
	if permitWouldAlert(v1, nil) {
		t.Fatal("done 不应告警")
	}
	// 消费后：许可回到 1，但同一 episode 仍显示 done
	pfAfter := guardPermitFacts{SuppressedEpisode: e1.SuppressEpisode}
	v2, e2 := runPermit(f, pfAfter)
	if v2.State != GuardDone || e2.Consume {
		t.Fatalf("同一 episode 仍 done、不重复消费: v=%s eff=%+v", v2.State, e2)
	}
	if permitWouldAlert(v2, &store.GuardPresetState{NotifiedEpisode: raw.Episode}) {
		t.Fatal("同一 episode 不应再次推送")
	}
}

func TestWakePermitNewEpisodeAlertsAfterConsume(t *testing.T) {
	f1 := factsCrashed("s1", 3)
	setAt := ago(10 * time.Minute)
	_, e1 := runPermit(f1, markedPF(setAt))
	pfAfter := guardPermitFacts{SuppressedEpisode: e1.SuppressEpisode} // Marked=false

	f2 := factsCrashed("s2", 5)
	raw2 := evaluateGuard(f2)
	v, eff := runPermit(f2, pfAfter)
	if v.State != raw2.State || v.State != GuardCrashed {
		t.Fatalf("新 episode 应正常 crashed，得到 %s", v.State)
	}
	if eff.Consume {
		t.Fatal("许可已恢复，不应再消费")
	}
	if !permitWouldAlert(v, nil) {
		t.Fatal("新 episode 应告警")
	}
}

func TestWakePermitApplyIdempotentSameTick(t *testing.T) {
	f := factsCrashed("s1", 2)
	pf := markedPF(ago(5 * time.Minute))
	raw := evaluateGuard(f)
	v1, e1 := runPermit(f, pf)
	pf2 := pf
	if e1.Consume {
		pf2.Marked = false
		pf2.SuppressedEpisode = e1.SuppressEpisode
	}
	v2, e2 := runPermit(f, pf2)
	if v1.State != v2.State {
		t.Fatalf("重复计算状态应一致: %s vs %s", v1.State, v2.State)
	}
	if e2.Consume {
		t.Fatal("同一 tick 二次计算不应再次消费")
	}
	_ = raw
}

func TestWakePermitClassifyEndWithPermit(t *testing.T) {
	setAt := ago(time.Minute)
	for _, tc := range []struct {
		name  string
		code  int
		want  GuardState
		alert bool
	}{
		{"exec 正常退出 0 + 标记 → done", 0, GuardDone, false},
		{"exec 异常退出 + 标记 → done", 3, GuardDone, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := factsCrashed("exec-s", tc.code)
			v, _ := runPermit(f, markedPF(setAt))
			if v.State != tc.want {
				t.Fatalf("state=%s", v.State)
			}
			if permitWouldAlert(v, nil) != tc.alert {
				t.Fatalf("alert=%v", tc.alert)
			}
		})
	}
	st, why := classifyEnd("exit", ptrInt(0))
	if st != GuardEnded {
		t.Fatalf("classifyEnd exit0: %s %s", st, why)
	}
	st, _ = classifyEnd("exit", ptrInt(3))
	if st != GuardCrashed {
		t.Fatalf("classifyEnd exit3: %s", st)
	}
}

func TestWakePermitPermit1Passthrough(t *testing.T) {
	f := factsCrashed("s1", 2)
	raw := evaluateGuard(f)
	v, eff := runPermit(f, guardPermitFacts{})
	if v.State != raw.State || eff.Consume {
		t.Fatalf("许可=1 时保持底层裁决")
	}
	if !permitWouldAlert(v, nil) {
		t.Fatal("crashed 应告警")
	}
}
