package terminal

import (
	"context"
	"math/rand"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/teacat99/RelayMesh/v2/internal/model"
	"github.com/teacat99/RelayMesh/v2/internal/termexec"
	"github.com/teacat99/RelayMesh/v2/internal/termproto"
)

// ---------- 总闸监视：关闭流程里的判定不算数 ----------

// lastDecision 读出总闸记录的最近一次判定。
func lastDecision(g *Gate) Decision {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.last
}

// ctxCreds 模拟真实的凭据读取：ctx 一旦结束就返回 ctx.Err()（与数据库查询被取消时一样）。
type ctxCreds struct{ user, pass string }

func (c ctxCreds) EffectiveCredentials(ctx context.Context) (string, string, error) {
	if err := ctx.Err(); err != nil {
		return "", "", err
	}
	return c.user, c.pass, nil
}

// 判定期间上下文已经结束（Hub 正在关闭，或调用方放弃了）：读凭据失败是被取消打断的，不是真的「无法确认前置条件」。
// 这样的判定既不回调、也不记录——否则热更时旧 Hub 关闭的那一瞬间会被当成「总闸关闭」，把所有进行中的模型运行取消掉。
func TestGateObserveIgnoresCancelledContext(t *testing.T) {
	g := NewGate(ModeAuto, strongSecret, ctxCreds{user: "admin", pass: strongSecret})
	var calls atomic.Int32
	onChange := func(prev, cur Decision) { calls.Add(1) }

	// 基线：总闸是开的。
	g.observe(context.Background(), onChange)
	base := calls.Load()
	if cur := lastDecision(g); !cur.Open {
		t.Fatalf("基线应当是开: %+v", cur)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	g.observe(ctx, onChange)
	if n := calls.Load(); n != base {
		t.Fatalf("上下文已取消时的判定不该触发回调: %d → %d", base, n)
	}
	if cur := lastDecision(g); !cur.Open || cur.Reason != ReasonOK {
		t.Fatalf("取消的判定不该改写当前状态: %+v", cur)
	}

	// 之后正常的判定仍然是开，也不会因为之前那一次而补一次「翻转」。
	g.observe(context.Background(), onChange)
	if n := calls.Load(); n != base {
		t.Fatalf("恢复正常判定后不该有翻转回调: %d → %d", base, n)
	}
}

// 真正的关闭（凭据被清空）仍然要被发现：证明忽略取消中的判定没有把正常的翻转也吞掉。
func TestGateObserveStillReportsRealClosing(t *testing.T) {
	creds := &fakeCreds{user: "admin", pass: strongSecret}
	g := NewGate(ModeAuto, strongSecret, creds)
	var flips []Decision
	onChange := func(prev, cur Decision) { flips = append(flips, cur) }
	g.observe(context.Background(), onChange)
	creds.set("", "", nil)
	g.observe(context.Background(), onChange)
	if len(flips) == 0 || flips[len(flips)-1].Open {
		t.Fatalf("清空密码后应当报告总闸关闭: %+v", flips)
	}
}

// 随机时刻结束监视：无论 select 先选中定时器还是 ctx，都不能把「正在结束」报成总闸翻转。
func TestGateWatchNeverFlipsWhenContextEnds(t *testing.T) {
	var flips atomic.Int32
	for i := 0; i < 200; i++ {
		g := NewGate(ModeAuto, strongSecret, ctxCreds{user: "admin", pass: strongSecret})
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		go func() {
			defer close(done)
			g.Watch(ctx, 100*time.Microsecond, func(prev, cur Decision) { flips.Add(1) })
		}()
		time.Sleep(time.Duration(rand.Intn(1200)) * time.Microsecond)
		cancel()
		<-done
	}
	if n := flips.Load(); n != 0 {
		t.Fatalf("监视结束的过程中出现了 %d 次总闸翻转回调", n)
	}
}

// ---------- Hub 关闭过程中的总闸变化：不拆除运行 ----------

// 热更时旧进程关闭：即使此刻总闸被判成关闭，也不能取消主机上进行中的模型运行
// （它们由主机上的守护进程持有，新 Hub 起来后会对账接回）。
func TestExecGateFlipWhileHubClosingKeepsRuns(t *testing.T) {
	f := newExecFixture(t)
	f.sim.onRun = func(s *hostSim, m termproto.Run) { s.started(m.ID) }
	res, err := f.exec(t, f.caller(nil), withTarget(waitSet(0), f.a.code, "sleep 300"))
	if err != nil {
		t.Fatal(err)
	}

	hub := f.env.hub
	hub.mu.Lock()
	hub.closing = true
	hub.mu.Unlock()
	hub.onGateChange(Decision{Open: true, Reason: ReasonOK}, Decision{Reason: ReasonInternal})

	time.Sleep(200 * time.Millisecond)
	if n := f.sim.cancelCount(); n != 0 {
		t.Fatalf("Hub 关闭过程中的总闸变化不该取消运行，主机收到 %d 条取消", n)
	}
	if st := f.dbRun(t, res.RunID).State; st != model.RunRunning {
		t.Fatalf("运行应仍在进行: %s", st)
	}
	if n := f.auditCount(t, EvGateChanged); n != 0 {
		t.Fatalf("关闭过程中的翻转不该写 gate_changed 审计（它不是真实的总闸变化）: %d", n)
	}
}

// ---------- 取消原因具体化 ----------

func (f *execFixture) firstCancel(t *testing.T, what string) termproto.RunCancel {
	t.Helper()
	eventually(t, 3*time.Second, what, func() bool { return f.sim.cancelCount() > 0 })
	f.sim.mu.Lock()
	defer f.sim.mu.Unlock()
	return f.sim.cancels[0]
}

func TestExecGateClosedCancelsWithSpecificReason(t *testing.T) {
	f := newExecFixture(t)
	f.sim.onRun = func(s *hostSim, m termproto.Run) { s.started(m.ID) }
	res, err := f.exec(t, f.caller(nil), withTarget(waitSet(0), f.a.code, "sleep 300"))
	if err != nil {
		t.Fatal(err)
	}

	f.svc.gateClosed()
	c := f.firstCancel(t, "总闸关闭时主机收到取消")
	if c.ID != res.RunID || c.Reason != cancelGateClosed {
		t.Fatalf("取消应带具体原因 %q，得到 %+v", cancelGateClosed, c)
	}
	eventually(t, 3*time.Second, "系统取消写进审计（带原因）", func() bool {
		return auditHas(t, f.env, EvExecCancel, "reason="+cancelGateClosed)
	})

	// 主机原样回报原因 → 运行记录里留下具体原因，界面与模型据此解释。
	f.sim.endFull(longEnd(res.RunID, termproto.RunCancelled, nil, c.Reason))
	eventually(t, 3*time.Second, "运行以取消结束", func() bool { return f.dbRun(t, res.RunID).State == model.RunCancelled })
	rec := f.dbRun(t, res.RunID)
	if rec.DenyReason != cancelGateClosed {
		t.Fatalf("运行记录里应留下具体原因: %+v", rec)
	}
	if m := failMessage(rec.DenyReason); !strings.Contains(m, "总闸") {
		t.Fatalf("原因应有中文解释: %q", m)
	}
}

func TestExecHostRemovedAndTokenChangedReasons(t *testing.T) {
	t.Run("主机停用", func(t *testing.T) {
		f := newExecFixture(t)
		f.sim.onRun = func(s *hostSim, m termproto.Run) { s.started(m.ID) }
		if _, err := f.exec(t, f.caller(nil), withTarget(waitSet(0), f.a.code, "sleep 300")); err != nil {
			t.Fatal(err)
		}
		f.env.hub.kickAgent(f.a.code, "host_disabled")
		if c := f.firstCancel(t, "停用主机时主机收到取消"); c.Reason != cancelHostRemoved {
			t.Fatalf("取消原因 = %q，期望 %q", c.Reason, cancelHostRemoved)
		}
	})
	t.Run("令牌变更", func(t *testing.T) {
		f := newExecFixture(t)
		f.sim.onRun = func(s *hostSim, m termproto.Run) { s.started(m.ID) }
		cred := &model.MCPCredential{Name: "temp", Permissions: model.Permissions{Terminal: true}, TerminalHosts: model.StringList{f.a.code}}
		if err := f.env.store.CreateCredential(context.Background(), cred); err != nil {
			t.Fatal(err)
		}
		caller := f.caller(func(x *termexec.Caller) { x.CredentialID, x.CredentialName, x.Credential = cred.ID, cred.Name, cred })
		if _, err := f.exec(t, caller, withTarget(waitSet(0), f.a.code, "sleep 300")); err != nil {
			t.Fatal(err)
		}
		f.env.hub.MCPCredentialChanged(cred.ID, "deleted")
		if c := f.firstCancel(t, "令牌删除时主机收到取消"); c.Reason != cancelTokenChanged {
			t.Fatalf("取消原因 = %q，期望 %q", c.Reason, cancelTokenChanged)
		}
		eventually(t, 3*time.Second, "系统取消写进审计（带原因）", func() bool {
			return auditHas(t, f.env, EvExecCancel, "reason="+cancelTokenChanged)
		})
	})
	t.Run("管理员操作不记系统审计", func(t *testing.T) {
		f := newExecFixture(t)
		f.sim.onRun = func(s *hostSim, m termproto.Run) { s.started(m.ID) }
		if _, err := f.exec(t, f.caller(nil), withTarget(waitSet(0), f.a.code, "sleep 300")); err != nil {
			t.Fatal(err)
		}
		f.setModel(t, map[string]any{"allow_model": false})
		if c := f.firstCancel(t, "收回授权时主机收到取消"); c.Reason != cancelRevoked {
			t.Fatalf("取消原因 = %q，期望 %q", c.Reason, cancelRevoked)
		}
		if auditHas(t, f.env, EvExecCancel, "reason="+cancelRevoked) {
			t.Fatalf("管理员主动收回授权不是系统取消，不该写 system 的 exec_cancel 审计")
		}
	})
}

func TestExecReconcileCancelsUnknownRunWithReason(t *testing.T) {
	f := newExecFixture(t)
	stranger := strings.Repeat("cd", 16)
	f.sim.a.send(termproto.Runs{T: termproto.TRuns, Running: []termproto.RunBrief{{ID: stranger, Started: 1}}})
	c := f.firstCancel(t, "不认识的运行被要求取消")
	if c.ID != stranger || c.Reason != cancelUnknownRun {
		t.Fatalf("取消 = %+v，期望原因 %q", c, cancelUnknownRun)
	}
}

func TestCancelReasonsHaveExplanations(t *testing.T) {
	for _, code := range []string{"user", "model", cancelPause, cancelRevoked, cancelGateClosed, cancelHostRemoved, cancelTokenChanged, cancelUnknownRun, "system"} {
		if failMessage(code) == "" {
			t.Errorf("取消原因 %q 没有中文解释", code)
		}
	}
	for by, want := range map[string]bool{
		cancelGateClosed: true, cancelHostRemoved: true, cancelTokenChanged: true, cancelUnknownRun: true, "system": true,
		"user": false, "model": false, cancelPause: false, cancelRevoked: false, "": false,
	} {
		if got := isSystemCancel(by); got != want {
			t.Errorf("isSystemCancel(%q) = %v，期望 %v", by, got, want)
		}
	}
}

func TestRunEndTextExplainsCancelReason(t *testing.T) {
	subj := "会话页的非交互续聊"
	rec := model.TerminalRun{State: model.RunCancelled, DenyReason: cancelGateClosed, DurationMS: 13_530_603}
	text := runEndText(subj, rec)
	for _, want := range []string{"被取消", "原因", "总闸"} {
		if !strings.Contains(text, want) {
			t.Fatalf("结束行缺少 %q: %q", want, text)
		}
	}
	legacy := runEndText(subj, model.TerminalRun{State: model.RunCancelled, DenyReason: "system", DurationMS: 1000})
	if !strings.Contains(legacy, "旧版本") {
		t.Fatalf("旧记录（system）应说明当时没有记下具体原因: %q", legacy)
	}
	if bare := runEndText(subj, model.TerminalRun{State: model.RunCancelled}); bare != subj+"已结束：被取消" {
		t.Fatalf("没有原因时保持原样: %q", bare)
	}
}

// 预设清单的现状条目：被取消的运行带原因，正常结束与带退出码的失败没有。
func TestLauncherRunViewCarriesCancelReason(t *testing.T) {
	f, id := launcherFixture(t, map[string]any{"workflow_id": launcherWF})
	ctx := context.Background()
	f.sim.onRun = func(s *hostSim, m termproto.Run) { s.started(m.ID) }
	plan, err := f.runLauncher(id)
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, 2*time.Second, "运行已开始", func() bool {
		lr := f.svc.liveRun(plan.RunID)
		return lr != nil && lr.snapshot().StartedAt != nil
	})
	f.sim.endFull(longEnd(plan.RunID, termproto.RunCancelled, nil, cancelGateClosed))
	f.waitRunFinal(t, plan.RunID)

	views, err := f.env.hub.guardian.launcherRunViews(ctx, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	v := views[id]
	if v.State != model.RunCancelled || !strings.Contains(v.Why, "总闸") {
		t.Fatalf("被取消的运行应带原因: %+v", v)
	}
	eventually(t, 3*time.Second, "聊天里的结束行写明原因", func() bool {
		for _, s := range f.chatTexts(launcherWF) {
			if strings.Contains(s, "已结束：被取消") && strings.Contains(s, "总闸") {
				return true
			}
		}
		return false
	})

	// 正常结束的运行没有 why。
	f.completeWith(launcherResultOut)
	plan2, err := f.runLauncher(id)
	if err != nil {
		t.Fatal(err)
	}
	f.waitRunFinal(t, plan2.RunID)
	views, _ = f.env.hub.guardian.launcherRunViews(ctx, time.Now())
	if v := views[id]; v.RunID != plan2.RunID || v.Why != "" {
		t.Fatalf("成功的运行不该带原因: %+v", v)
	}
}
