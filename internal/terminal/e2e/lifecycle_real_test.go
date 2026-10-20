//go:build e2e && !windows

package e2e

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/teacat99/RelayMesh/v2/internal/terminal"
	"github.com/teacat99/RelayMesh/v2/internal/termproto"
)

func (a *api) openSessionWith(code string, body map[string]any) termproto.SessionInfo {
	a.t.Helper()
	var r struct {
		Session termproto.SessionInfo `json:"session"`
	}
	if _, ok := body["rows"]; !ok {
		body["rows"], body["cols"] = 30, 100
	}
	if c := a.call("POST", "/hosts/"+code+"/sessions", body, &r); c != 200 || r.Session.SID == "" {
		a.t.Fatalf("开会话失败: HTTP %d", c)
	}
	return r.Session
}

func (a *api) sessionInfo(code, sid string) (termproto.SessionInfo, bool) {
	a.t.Helper()
	_, list := a.sessions(code)
	return sessionByID(list, sid)
}

// 生命周期（H1）与「在原目录重建会话」：策略与单会话设置经 Hub → Agent → 守护进程真实下发，
// 守护进程报告的生效值随之变化；新会话在指定目录启动，目录无效时回落到默认目录。
func TestRealLifecycleSettingsAndRebuildInOriginalDirectory(t *testing.T) {
	h, _, code, _ := startRecordingHost(t)

	// 默认：永久保持。
	inherit := h.api.openSession(code, "inherit")
	if inherit.Idle != 0 || inherit.IdleOwn {
		t.Fatalf("默认应永久保持且沿用主机默认: %+v", inherit)
	}
	if inherit.Active <= 0 {
		t.Fatalf("会话应报告最后交互时间: %+v", inherit)
	}

	// 主机默认改为 1 天：已有的会话报告的生效值跟上。
	if c := h.api.call("POST", "/hosts/"+code+"/lifecycle", map[string]any{"idle_seconds": 86400}, nil); c != 200 {
		t.Fatalf("设置主机默认生命周期失败: HTTP %d", c)
	}
	waitHost(t, h, code, "主机视图反映 idle_sec", func(v terminal.HostView) bool { return v.IdleSec == 86400 })
	waitUntil(t, 15*time.Second, "守护进程按新的主机默认报告", func() bool {
		s, ok := h.api.sessionInfo(code, inherit.SID)
		return ok && s.Idle == 86400 && !s.IdleOwn
	})

	// 单独设置：永久保持 → 7 天 → 改回沿用主机默认。
	steps := []struct {
		body    map[string]any
		idle    int
		idleOwn bool
	}{
		{map[string]any{"idle_seconds": 0}, 0, true},
		{map[string]any{"idle_seconds": 7 * 86400}, 7 * 86400, true},
		{map[string]any{"idle_seconds": nil}, 86400, false},
	}
	for i, st := range steps {
		if c := h.api.call("POST", "/hosts/"+code+"/sessions/"+inherit.SID+"/lifecycle", st.body, nil); c != 200 {
			t.Fatalf("步骤 %d 设置会话生命周期失败: HTTP %d", i, c)
		}
		waitUntil(t, 15*time.Second, "守护进程报告新的会话生命周期", func() bool {
			s, ok := h.api.sessionInfo(code, inherit.SID)
			return ok && s.Idle == st.idle && s.IdleOwn == st.idleOwn
		})
	}

	// 在指定目录、带单独生命周期新建会话。
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	rebuilt := h.api.openSessionWith(code, map[string]any{"name": "rebuilt", "cwd": dir, "idle_seconds": 3600})
	if !rebuilt.IdleOwn || rebuilt.Idle != 3600 {
		t.Fatalf("新建时指定的生命周期应生效: %+v", rebuilt)
	}
	tc := h.api.attach(h, code, rebuilt.SID, 0)
	tc.send("pwd\r")
	tc.waitFor(dir, 10*time.Second)
	tc.close()

	// 目录不存在：忽略，回落到默认目录（不报错、不阻止新建）。
	fallback := h.api.openSessionWith(code, map[string]any{"name": "fallback", "cwd": filepath.Join(dir, "missing")})
	tc2 := h.api.attach(h, code, fallback.SID, 0)
	tc2.send("echo WD:$(pwd):END\r")
	tc2.waitFor("WD:/", 10*time.Second)
	if strings.Contains(tc2.output(), "missing") {
		t.Fatalf("不存在的目录不应成为工作目录: %q", lastN(tc2.output(), 200))
	}
	tc2.close()

	// 输入会刷新最后交互时间；纯输出不会。
	before, _ := h.api.sessionInfo(code, inherit.SID)
	time.Sleep(1200 * time.Millisecond)
	tc3 := h.api.attach(h, code, inherit.SID, 0)
	tc3.send("echo typed\r")
	tc3.waitFor("typed", 10*time.Second)
	tc3.close()
	waitUntil(t, 10*time.Second, "输入与附着刷新了最后交互时间", func() bool {
		s, ok := h.api.sessionInfo(code, inherit.SID)
		return ok && s.Active > before.Active
	})
}
