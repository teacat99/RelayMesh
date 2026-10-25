package terminal

import (
	"strings"
	"testing"

	"github.com/teacat99/RelayMesh/v2/internal/termproto"
)

// 在线主机 + 预设的测试环境（托管终端屏幕信号的测试在用，screen_test.go）。
// 它原先叫 guardEnv，和会话守护的测试放在一起（guard_test.go）；守护在 #245 第二期下线后只留下这一小块。

const guardPresetName = "Cursor CLI"

func sidOf(c byte) string { return strings.Repeat(string(c), 32) }

type hostEnv struct {
	*testEnv
	agent *agentConn
}

func newHostEnv(t *testing.T, opts ...envOption) *hostEnv {
	t.Helper()
	return &hostEnv{testEnv: newTestEnv(t, append([]envOption{withFakeClock()}, opts...)...)}
}

// savePreset 放一个带守护配置的预设（屏幕信号靠它把终端会话认到工作流上；交互式预设不能再经 REST 新建，putLauncher 会直接写库）。
func (g *hostEnv) savePreset(id, name string, guard map[string]any) apiResult {
	g.t.Helper()
	body := map[string]any{"name": name, "host": testLauncherHost, "dir": "/home/user/RelayMesh", "command": "agent"}
	if guard != nil {
		body["guard"] = guard
	}
	res := g.putLauncher("/api/v1/terminal/launchers/"+id, body, g.admin()...)
	if res.Status != 200 {
		g.t.Fatalf("保存预设失败: %d %s", res.Status, res.Raw)
	}
	return res
}

// online 让主机上线并报告这些会话。
func (g *hostEnv) online(sessions ...termproto.SessionInfo) {
	g.t.Helper()
	a := &agentConn{hub: g.hub, code: testLauncherHost, enabled: true, sessions: sessions}
	g.hub.registerAgent(a)
	g.t.Cleanup(func() { g.hub.unregisterAgent(a) })
	g.agent = a
}

func (g *hostEnv) offline() {
	g.hub.unregisterAgent(g.agent)
}

// wantWakeCode 断言错误是带这个错误码的 *wakeError（运行类操作的失败原因，见 wake_line.go）。
func wantWakeCode(t *testing.T, err error, code string) {
	t.Helper()
	we, ok := err.(*wakeError)
	if !ok || we.Code != code {
		t.Fatalf("期望运行错误码 %q，得到 %v", code, err)
	}
}
