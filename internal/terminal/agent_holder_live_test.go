//go:build !windows

package terminal

// 会话守护进程（holder）与 Hub 的端到端测试：真实的守护进程（本地套接字）+ 真实的 Agent + 真实的 PTY。
// 这里验证 M3 的核心承诺——Agent 升级 / 重启 / 崩溃不会打断终端会话；以及守护进程不可用、被停止、
// 内嵌模式这几种情形下，界面拿到的是如实的状态而不是静默的假象。

import (
	"context"
	"os"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/teacat99/RelayMesh/v2/internal/agent"
	"github.com/teacat99/RelayMesh/v2/internal/agent/holder"
	"github.com/teacat99/RelayMesh/v2/internal/termproto"
)

// liveHolder 是测试里运行的真实会话守护进程。
type liveHolder struct {
	t       *testing.T
	dataDir string
	socket  string
	cancel  context.CancelFunc
	done    chan error
}

func shortTempDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "rmh")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

func startLiveHolder(t *testing.T, dataDir string) *liveHolder {
	t.Helper()
	ready := make(chan string, 1)
	ctx, cancel := context.WithCancel(context.Background())
	lh := &liveHolder{t: t, dataDir: dataDir, cancel: cancel, done: make(chan error, 1)}
	home := t.TempDir()
	go func() {
		lh.done <- holder.Run(ctx, holder.RunOptions{
			DataDir: dataDir,
			Version: "1.7.0-holder-test",
			Shell:   testShell(home),
			Logf:    func(f string, a ...any) { t.Logf("[holder] "+f, a...) },
			Ready:   func(s string) { ready <- s },
		})
	}()
	select {
	case lh.socket = <-ready:
	case err := <-lh.done:
		t.Fatalf("守护进程提前退出: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("守护进程没有在 5 秒内就绪")
	}
	t.Cleanup(lh.stop)
	return lh
}

// stop 以「有意停止」的方式结束守护进程（等同 systemctl stop）：会话随之终止。
func (lh *liveHolder) stop() {
	lh.cancel()
	select {
	case <-lh.done:
		lh.done <- nil // 允许重复调用
	case <-time.After(10 * time.Second):
		lh.t.Error("守护进程未在 10 秒内退出")
	}
}

// status 以管理端身份查询守护进程。
func (lh *liveHolder) status() holder.IPCStatusReply {
	lh.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, err := holder.Dial(ctx, lh.socket)
	if err != nil {
		lh.t.Fatalf("连接守护进程失败: %v", err)
	}
	cli, err := holder.Handshake(ctx, conn, holder.RoleAdmin, "test")
	if err != nil {
		lh.t.Fatalf("握手失败: %v", err)
	}
	defer cli.Conn.Close()
	st, err := holder.Status(ctx, cli)
	if err != nil {
		lh.t.Fatalf("查询状态失败: %v", err)
	}
	return st
}

// hostHolder 取 Hub 的主机视图里该主机的 holder 字段（没有则为 nil）。
func (e *testEnv) hostHolder(code string) map[string]any {
	e.t.Helper()
	res := e.call("GET", "/api/v1/terminal/hosts", nil, "Authorization", humanAuth(testUser))
	hosts, _ := res.Body["hosts"].([]any)
	for _, h := range hosts {
		if m, ok := h.(map[string]any); ok && m["machine_code"] == code {
			hv, _ := m["holder"].(map[string]any)
			return hv
		}
	}
	e.t.Fatalf("主机视图里没有 %s: %s", code, res.Raw)
	return nil
}

func (e *testEnv) waitHolderState(code, state string) map[string]any {
	e.t.Helper()
	var got map[string]any
	eventually(e.t, liveWait, "Hub 看到守护进程状态 "+state, func() bool {
		got = e.hostHolder(code)
		return got != nil && got["state"] == state
	})
	return got
}

// newExternalEnv 搭好 Hub、守护进程与一个外置模式的 Agent，并完成启用。
func newExternalEnv(t *testing.T) (e *testEnv, la *liveAgent, lh *liveHolder, hdr []string, idDir string) {
	t.Helper()
	dataDir := shortTempDir(t)
	lh = startLiveHolder(t, dataDir)
	idDir = t.TempDir()
	e = newTestEnv(t)
	hdr = e.admin()
	e.openDiscovery(hdr)
	la = startLiveAgent(t, e, liveOpts{dir: idDir, holder: agent.HolderExternal, holderSocket: lh.socket})
	e.enrollLive(la, hdr, "holder-box")
	return
}

// ---------- 测试 ----------

func TestLiveHolder_SessionsSurviveAgentRestart(t *testing.T) {
	e, la, lh, hdr, idDir := newExternalEnv(t)

	hv := e.waitHolderState(la.code, "ready")
	if hv["persistent"] != true || hv["version"] != "1.7.0-holder-test" {
		t.Fatalf("外置守护进程应报告持久性与自己的版本: %v", hv)
	}
	if la.a.Manager() != nil {
		t.Fatal("外置模式下 Agent 不应自带会话管理器")
	}

	sid := e.openLiveSession(la.code, hdr, "survivor")
	c := e.attachLive(la.code, sid, hdr, 0, true)
	c.send("KEEP=still-here-$((6*7)); echo PID-$$-END\r")
	c.waitFor("PID-")
	c.waitFor("-END")
	pid := extractPID(t, c.text())
	c.send("echo BEFORE-$((1+1))\r")
	c.waitFor("BEFORE-2")

	// 模拟升级 / 重启：Agent 进程整个退出。
	c.close()
	la.stop()

	// 守护进程里的会话与 shell 进程都还在。
	st := lh.status()
	if len(st.Sessions) != 1 || st.Sessions[0].SID != sid || st.Sessions[0].State != termproto.SessionRunning {
		t.Fatalf("Agent 退出后会话应继续运行: %+v", st.Sessions)
	}
	if st.Controller {
		t.Fatal("Agent 退出后守护进程不应还认为有控制端")
	}
	if err := syscall.Kill(pid, 0); err != nil {
		t.Fatalf("shell 进程 %d 应仍然存活: %v", pid, err)
	}

	// 新的 Agent（同一身份）接上：Hub 重新看到会话，历史输出被回放，shell 里的状态原封不动。
	la2 := startLiveAgent(t, e, liveOpts{dir: idDir, holder: agent.HolderExternal, holderSocket: lh.socket})
	la2.waitState(0, agent.StateOnline)
	eventually(t, liveWait, "Hub 重新看到原会话", func() bool {
		a := e.hub.onlineAgent(la.code)
		if a == nil || !a.isEnabled() {
			return false
		}
		for _, s := range a.sessionList() {
			if s.SID == sid {
				return true
			}
		}
		return false
	})
	c2 := e.attachLive(la.code, sid, hdr, 0, true)
	c2.waitFor("BEFORE-2")
	c2.send("echo $KEEP\r")
	c2.waitFor("still-here-42")
}

func extractPID(t *testing.T, text string) int {
	t.Helper()
	i := strings.LastIndex(text, "PID-")
	if i < 0 {
		t.Fatalf("输出里没有 PID: %q", text)
	}
	rest := text[i+len("PID-"):]
	j := strings.Index(rest, "-END")
	if j < 0 {
		t.Fatalf("输出里没有 PID 结束标记: %q", text)
	}
	pid := 0
	for _, r := range rest[:j] {
		if r < '0' || r > '9' {
			t.Fatalf("PID 不是数字: %q", rest[:j])
		}
		pid = pid*10 + int(r-'0')
	}
	return pid
}

func TestLiveHolder_ControllerSupersededByNewAgentInstance(t *testing.T) {
	// 同一身份的两个 Agent 实例先后连上同一个守护进程：Hub 会让旧连接让位（CloseSuperseded），
	// 守护进程里的会话不受影响，新实例接管。
	e, la, lh, hdr, idDir := newExternalEnv(t)
	sid := e.openLiveSession(la.code, hdr, "shared")

	la2 := startLiveAgent(t, e, liveOpts{dir: idDir, holder: agent.HolderExternal, holderSocket: lh.socket})
	la2.waitState(0, agent.StateOnline)

	eventually(t, liveWait, "新实例接管后会话仍可见", func() bool {
		a := e.hub.onlineAgent(la.code)
		if a == nil {
			return false
		}
		for _, s := range a.sessionList() {
			if s.SID == sid {
				return true
			}
		}
		return false
	})
	if st := lh.status(); len(st.Sessions) != 1 {
		t.Fatalf("接管不应影响守护进程里的会话: %+v", st.Sessions)
	}
}

func TestLiveHolder_UnavailableIsReportedThenRecovers(t *testing.T) {
	dataDir := shortTempDir(t)
	socket := holder.SocketPath(dataDir)
	e := newTestEnv(t)
	hdr := e.admin()
	e.openDiscovery(hdr)
	la := startLiveAgent(t, e, liveOpts{holder: agent.HolderExternal, holderSocket: socket})
	e.enrollLive(la, hdr, "no-holder-yet")

	// 守护进程没起来：Agent 照常在线，但如实告诉 Hub「不可用」，开会话明确失败，而不是悄悄退化。
	hv := e.waitHolderState(la.code, "unavailable")
	if hv["reason"] != termproto.HolderReasonUnreachable {
		t.Fatalf("原因应为 unreachable: %v", hv)
	}
	res := e.call("POST", "/api/v1/terminal/hosts/"+la.code+"/sessions", map[string]any{"name": "x", "rows": 24, "cols": 80}, hdr...)
	if res.Status == 200 {
		t.Fatalf("守护进程不可用时新建会话必须失败: %s", res.Raw)
	}

	// 守护进程起来之后，Agent 在退避重试里自动接上，无需重启 Agent。
	lh := startLiveHolder(t, dataDir)
	_ = lh
	hv = e.waitHolderState(la.code, "ready")
	if hv["persistent"] != true {
		t.Fatalf("恢复后应为持久: %v", hv)
	}
	sid := e.openLiveSession(la.code, hdr, "after-recovery")
	c := e.attachLive(la.code, sid, hdr, 0, true)
	c.send("echo UP-$((20+22))\r")
	c.waitFor("UP-42")
}

func TestLiveHolder_StoppedHolderEndsSessionsAndReportsWhy(t *testing.T) {
	e, la, lh, hdr, _ := newExternalEnv(t)
	e.waitHolderState(la.code, "ready")
	sid := e.openLiveSession(la.code, hdr, "doomed")
	c := e.attachLive(la.code, sid, hdr, 0, true)
	c.send("echo HELLO-$((1+2))\r")
	c.waitFor("HELLO-3")

	// 有意停止守护进程：浏览器附着被收尾，Hub 看到「不可用」。
	lh.stop()
	c.waitClosed()
	e.waitHolderState(la.code, "unavailable")

	// 同一数据目录重新启动：新守护进程如实解释那个会话为什么没了。
	lh2 := startLiveHolder(t, lh.dataDir)
	_ = lh2
	hv := e.waitHolderState(la.code, "ready")
	lost, _ := hv["lost"].([]any)
	if len(lost) != 1 {
		t.Fatalf("应报告 1 个丢失的会话: %v", hv)
	}
	l := lost[0].(map[string]any)
	if l["sid"] != sid || l["name"] != "doomed" || l["reason"] != termproto.LostHolderStopped {
		t.Fatalf("丢失记录不符: %v", l)
	}

	// 审计里留下 sessions_lost 与 holder_state 记录。
	eventually(t, liveWait, "审计记录 sessions_lost", func() bool {
		res := e.call("GET", "/api/v1/terminal/audit?limit=200", nil, "Authorization", humanAuth(testUser))
		return strings.Contains(res.Raw, EvSessionsLost) && strings.Contains(res.Raw, EvHolderState)
	})
}

func TestLiveHolder_EmbeddedModeIsLabeledNonPersistent(t *testing.T) {
	e, la, _ := liveSetup(t, liveOpts{})
	hv := e.waitHolderState(la.code, "ready")
	if hv["persistent"] != false {
		t.Fatalf("内嵌模式必须如实标注为不具持久性: %v", hv)
	}
}
