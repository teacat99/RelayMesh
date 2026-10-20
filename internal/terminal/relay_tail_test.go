package terminal

import (
	"testing"

	"github.com/teacat99/RelayMesh/v2/internal/termproto"
)

// ticketTail 用指定的 since / tail_lines 签发票据、拨号，返回 Agent 收到的那条 attach。
func (e *testEnv) attachWithTail(since uint64, tail int) termproto.Attach {
	e.t.Helper()
	hdr := e.admin()
	a := e.enrollAgent(testMeta("tail-host"), "回放裁剪主机")
	sid := e.openSession(a, hdr, "main")
	res := e.call("POST", "/api/v1/terminal/hosts/"+a.code+"/sessions/"+sid+"/ticket",
		map[string]any{"since": since, "rows": 40, "cols": 120, "tail_lines": tail}, hdr...)
	if res.Status != 200 || res.str("ticket") == "" {
		e.t.Fatalf("签发票据失败: %d %s", res.Status, res.Raw)
	}
	ws, _, err := e.dialBrowser(res.str("ticket"), nil)
	if err != nil {
		e.t.Fatalf("浏览器拨号失败: %v", err)
	}
	e.t.Cleanup(func() { _ = ws.CloseNow() })
	readReady(e.t, ws)
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(a.attaches) != 1 {
		e.t.Fatalf("Agent 应恰好收到一条 attach: %+v", a.attaches)
	}
	return a.attaches[0]
}

func TestTicketTailLinesReachAgentOnFreshAttach(t *testing.T) {
	att := newTestEnv(t).attachWithTail(0, 800)
	if att.TailLines != 800 || att.Since != 0 {
		t.Fatalf("全新附着应把 tail_lines 原样交给 Agent: %+v", att)
	}
}

func TestTicketTailLinesClampedToMax(t *testing.T) {
	att := newTestEnv(t).attachWithTail(0, 60000)
	if att.TailLines != termproto.MaxTailLines {
		t.Fatalf("过大的 tail_lines 应按 %d 截断: %+v", termproto.MaxTailLines, att)
	}
}

func TestTicketTailLinesDroppedOnResume(t *testing.T) {
	att := newTestEnv(t).attachWithTail(1234, 800)
	if att.TailLines != 0 || att.Since != 1234 {
		t.Fatalf("续传不需要裁剪回放，tail_lines 应被清掉: %+v", att)
	}
}

func TestTicketWithoutTailLinesIsUnchanged(t *testing.T) {
	att := newTestEnv(t).attachWithTail(0, 0)
	if att.TailLines != 0 {
		t.Fatalf("不带 tail_lines 应保持旧行为: %+v", att)
	}
}
