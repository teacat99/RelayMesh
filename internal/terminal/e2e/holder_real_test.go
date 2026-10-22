//go:build e2e && !windows

package e2e

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/teacat99/RelayMesh/v2/internal/terminal"
	"github.com/teacat99/RelayMesh/v2/internal/termproto"
)

var bgPidRe = regexp.MustCompile(`BG:(\d+):END`)

func waitHost(t *testing.T, h *hub, code string, what string, cond func(terminal.HostView) bool) terminal.HostView {
	t.Helper()
	var got terminal.HostView
	waitUntil(t, 40*time.Second, what, func() bool {
		v, ok := h.api.host(code)
		if ok && cond(v) {
			got = v
			return true
		}
		return false
	})
	return got
}

func sessionByID(list []termproto.SessionInfo, sid string) (termproto.SessionInfo, bool) {
	for _, s := range list {
		if s.SID == sid {
			return s, true
		}
	}
	return termproto.SessionInfo{}, false
}

// 核心场景（AC-P11 / AC-P12）：会话由独立的守护进程持有，Agent 被 kill -9 之后
// Shell 与其后台进程继续存活；Agent 重新起来后 Web 能重新接上同一个会话并回放历史。
func TestRealAgentKillKeepsSessionsAndProcesses(t *testing.T) {
	hubBin, agentBin := binaries(t)
	h := startHub(t, hubBin)
	d := newHostDirs(t)
	hp := startHolder(t, agentBin, d)

	ag1 := startAgent(t, agentBin, h, d, "agent1", "--holder", "external")
	code := h.api.enrollOnly()
	v := waitHost(t, h, code, "主机上线且守护进程就绪", func(v terminal.HostView) bool {
		return v.Online && v.Holder != nil && v.Holder.State == "ready"
	})
	if !v.Holder.Persistent {
		t.Fatalf("外置守护进程应标注为具备持久性: %+v", v.Holder)
	}

	sess := h.api.openSession(code, "e2e-persist")
	if sess.PID <= 0 {
		t.Fatalf("会话应带有 Shell 进程号: %+v", sess)
	}
	tc := h.api.attach(h, code, sess.SID, 0)
	marker := "survives-" + randHex(4)
	tc.send("export RM_MARK=" + marker + "; sleep 777 & echo BG:$!:END; echo SH:$$:END\r")
	tc.waitFor(fmt.Sprintf("SH:%d:END", sess.PID), 10*time.Second)
	m := bgPidRe.FindStringSubmatch(tc.output())
	if m == nil {
		t.Fatalf("没有取到后台进程号: %q", lastN(tc.output(), 200))
	}
	bgPID, _ := strconv.Atoi(m[1])
	tc.close()

	// —— 强杀 Agent（模拟崩溃 / 升级过程中被杀）——
	ag1.kill9()
	if !hp.alive() {
		t.Fatal("Agent 被杀后守护进程不应受影响")
	}
	if !pidAlive(sess.PID) || !pidAlive(bgPID) {
		t.Fatalf("Agent 被杀后 Shell(%d) / 后台进程(%d) 应继续存活", sess.PID, bgPID)
	}
	waitHost(t, h, code, "Hub 感知到主机离线", func(v terminal.HostView) bool { return !v.Online })

	// —— 新的 Agent 接手 ——
	startAgent(t, agentBin, h, d, "agent2", "--holder", "external")
	waitHost(t, h, code, "主机重新上线", func(v terminal.HostView) bool {
		return v.Online && v.Holder != nil && v.Holder.State == "ready"
	})
	_, list := h.api.sessions(code)
	got, ok := sessionByID(list, sess.SID)
	if !ok || got.State != "running" || got.PID != sess.PID {
		t.Fatalf("会话应原样保留（同一个 Shell 进程）: ok=%v %+v", ok, got)
	}

	tc2 := h.api.attach(h, code, sess.SID, 0)
	tc2.waitFor(fmt.Sprintf("SH:%d:END", sess.PID), 10*time.Second) // 回放了 Agent 被杀之前的输出
	tc2.send(fmt.Sprintf("echo VAR:$RM_MARK:END; kill -0 %d && echo BGALIVE\r", bgPID))
	tc2.waitFor("VAR:"+marker+":END", 10*time.Second) // Shell 变量还在：是同一个 Shell
	tc2.waitFor("BGALIVE", 10*time.Second)
	tc2.send("kill " + strconv.Itoa(bgPID) + "; exit\r")
}

// 守护进程被有意停止：会话随之结束；重新启动后 Hub 能看到「丢失了哪些会话、为什么」。
func TestRealHolderStopReportsLostSessions(t *testing.T) {
	hubBin, agentBin := binaries(t)
	h := startHub(t, hubBin)
	d := newHostDirs(t)
	startHolder(t, agentBin, d)
	startAgent(t, agentBin, h, d, "agent", "--holder", "external")
	code := h.api.enrollOnly()
	waitHost(t, h, code, "主机上线且守护进程就绪", func(v terminal.HostView) bool {
		return v.Online && v.Holder != nil && v.Holder.State == "ready"
	})

	sess := h.api.openSession(code, "doomed-session")
	holderStop(t, agentBin, d)
	waitHost(t, h, code, "Hub 看到守护进程不可用", func(v terminal.HostView) bool {
		return v.Holder != nil && v.Holder.State == "unavailable"
	})
	waitUntil(t, 10*time.Second, "被守护进程持有的 Shell 随之结束", func() bool { return !pidAlive(sess.PID) })

	startHolder(t, agentBin, d)
	v := waitHost(t, h, code, "守护进程重新就绪并报告丢失的会话", func(v terminal.HostView) bool {
		return v.Holder != nil && v.Holder.State == "ready" && len(v.Holder.Lost) > 0
	})
	var lost *termproto.LostSession
	for i := range v.Holder.Lost {
		if v.Holder.Lost[i].SID == sess.SID {
			lost = &v.Holder.Lost[i]
		}
	}
	if lost == nil || lost.Reason != termproto.LostHolderStopped || lost.Name != "doomed-session" {
		t.Fatalf("应报告会话因「守护进程被停止」而丢失: %+v", v.Holder.Lost)
	}
	_, list := h.api.sessions(code)
	if _, ok := sessionByID(list, sess.SID); ok {
		t.Fatal("丢失的会话不应还出现在会话列表里")
	}

	// 审计里有这条记录。
	var audit struct {
		Logs []struct {
			Event     string `json:"event"`
			SessionID string `json:"session_id"`
			Detail    string `json:"detail"`
		} `json:"logs"`
	}
	if c := h.api.raw("GET", "/api/v1/terminal/audit?limit=200", nil, &audit, true); c != 200 {
		t.Fatalf("读取审计失败: HTTP %d", c)
	}
	found := false
	for _, e := range audit.Logs {
		// 审计按「一次报告」记一条，明细是各原因的会话数；逐会话的名字在主机视图的 holder.lost 里。
		if e.Event == terminal.EvSessionsLost && strings.Contains(e.Detail, "holder_stopped=1") {
			found = true
		}
	}
	if !found {
		t.Fatalf("审计应记录 %s: %+v", terminal.EvSessionsLost, audit.Logs)
	}
}

// 守护进程自己被强杀（崩溃）：重启后报告原因是 holder_exit，而不是被停止。
func TestRealHolderCrashIsDistinguishedFromStop(t *testing.T) {
	hubBin, agentBin := binaries(t)
	h := startHub(t, hubBin)
	d := newHostDirs(t)
	hp := startHolder(t, agentBin, d)
	startAgent(t, agentBin, h, d, "agent", "--holder", "external")
	code := h.api.enrollOnly()
	waitHost(t, h, code, "守护进程就绪", func(v terminal.HostView) bool { return v.Online && v.Holder != nil && v.Holder.State == "ready" })
	sess := h.api.openSession(code, "crash-victim")

	hp.kill9()
	waitHost(t, h, code, "Hub 看到守护进程不可用", func(v terminal.HostView) bool { return v.Holder != nil && v.Holder.State == "unavailable" })

	startHolder(t, agentBin, d)
	v := waitHost(t, h, code, "守护进程恢复并报告丢失", func(v terminal.HostView) bool {
		return v.Holder != nil && v.Holder.State == "ready" && len(v.Holder.Lost) > 0
	})
	if v.Holder.Lost[0].SID != sess.SID || v.Holder.Lost[0].Reason != termproto.LostHolderExit {
		t.Fatalf("守护进程崩溃应报告 holder_exit: %+v", v.Holder.Lost)
	}
}

// 内嵌模式（Agent 自己持有会话）：界面要如实标注「不具持久性」，Agent 被杀会话确实会丢。
func TestRealEmbeddedModeIsLabelledAndNotPersistent(t *testing.T) {
	hubBin, agentBin := binaries(t)
	h := startHub(t, hubBin)
	d := newHostDirs(t)
	ag := startAgent(t, agentBin, h, d, "agent", "--holder", "embedded")
	code := h.api.enrollOnly()
	v := waitHost(t, h, code, "主机上线", func(v terminal.HostView) bool { return v.Online && v.Holder != nil && v.Holder.State == "ready" })
	if v.Holder.Persistent {
		t.Fatalf("内嵌模式不应标注为持久: %+v", v.Holder)
	}
	sess := h.api.openSession(code, "fragile")
	tc := h.api.attach(h, code, sess.SID, 0)
	tc.send("echo alive:$$:END\r")
	tc.waitFor(fmt.Sprintf("alive:%d:END", sess.PID), 10*time.Second)
	tc.close()
	ag.kill9()
	waitUntil(t, 10*time.Second, "内嵌模式下 Agent 被杀，Shell 随之结束", func() bool { return !pidAlive(sess.PID) })
}

// auto 模式：有守护进程就交给它（持久），没有就内嵌（并如实标注）。
func TestRealAutoModeFollowsHolderAvailability(t *testing.T) {
	hubBin, agentBin := binaries(t)

	t.Run("有守护进程", func(t *testing.T) {
		h := startHub(t, hubBin)
		d := newHostDirs(t)
		startHolder(t, agentBin, d)
		startAgent(t, agentBin, h, d, "agent") // 不写 --holder：默认 auto
		code := h.api.enrollOnly()
		v := waitHost(t, h, code, "主机上线", func(v terminal.HostView) bool { return v.Online && v.Holder != nil && v.Holder.State == "ready" })
		if !v.Holder.Persistent {
			t.Fatalf("auto 模式探测到守护进程应使用它: %+v", v.Holder)
		}
	})
	t.Run("没有守护进程", func(t *testing.T) {
		h := startHub(t, hubBin)
		d := newHostDirs(t)
		startAgent(t, agentBin, h, d, "agent")
		code := h.api.enrollOnly()
		v := waitHost(t, h, code, "主机上线", func(v terminal.HostView) bool { return v.Online && v.Holder != nil && v.Holder.State == "ready" })
		if v.Holder.Persistent {
			t.Fatalf("auto 模式没有守护进程应退回内嵌并如实标注: %+v", v.Holder)
		}
	})
}

// Hub 重启：Agent 自动重连，守护进程里的会话原样保留，Web 重新接上能回放。
func TestRealHubRestartKeepsSessions(t *testing.T) {
	hubBin, agentBin := binaries(t)
	h := startHub(t, hubBin)
	d := newHostDirs(t)
	startHolder(t, agentBin, d)
	startAgent(t, agentBin, h, d, "agent", "--holder", "external")
	code := h.api.enrollOnly()
	waitHost(t, h, code, "主机上线", func(v terminal.HostView) bool { return v.Online && v.Holder != nil && v.Holder.State == "ready" })

	sess := h.api.openSession(code, "across-hub-restart")
	tc := h.api.attach(h, code, sess.SID, 0)
	tc.send("echo before-restart-$$-END\r")
	tc.waitFor(fmt.Sprintf("before-restart-%d-END", sess.PID), 10*time.Second)
	tc.close()

	h.restart()
	waitHost(t, h, code, "Agent 重连 Hub 后主机重新上线", func(v terminal.HostView) bool {
		return v.Online && v.Holder != nil && v.Holder.State == "ready"
	})
	_, list := h.api.sessions(code)
	if got, ok := sessionByID(list, sess.SID); !ok || got.PID != sess.PID {
		t.Fatalf("Hub 重启后会话应原样保留: ok=%v %+v", ok, got)
	}
	tc2 := h.api.attach(h, code, sess.SID, 0)
	tc2.waitFor(fmt.Sprintf("before-restart-%d-END", sess.PID), 10*time.Second)
}
