package agent

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/coder/websocket"
	"github.com/teacat99/RelayMesh/v2/internal/termproto"
)

// 守护进程不可用时，网关操作必须得到一个明确的失败应答（而不是让服务端一直等到超时），
// 并且应答里不能回显请求里的任何凭据。
func TestRelaySSHOpWithoutHolderFailsFastAndNeverEchoesCredentials(t *testing.T) {
	g := newRelayRig(t, nil)
	req := termproto.SSHOp{
		T:   termproto.TSSHOp,
		Req: "req-1",
		Op:  termproto.SSHOpCheck,
		Target: termproto.SSHTarget{
			Addr: "10.0.0.5:22", User: "root", Auth: "password", Password: "rig-secret-password",
		},
	}
	data, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	g.r.handleText(data)

	typ, raw := g.next()
	if typ != websocket.MessageText {
		t.Fatal("期望文本帧")
	}
	var res termproto.SSHResult
	if err := json.Unmarshal(raw, &res); err != nil || res.T != termproto.TSSHResult {
		t.Fatalf("不是 ssh_result: %s (%v)", raw, err)
	}
	if res.Req != "req-1" || res.OK || res.Code != termproto.SSHCodeInternal {
		t.Fatalf("应答不对: %+v", res)
	}
	if strings.Contains(string(raw), "rig-secret-password") || strings.Contains(string(raw), "10.0.0.5") {
		t.Fatalf("应答不应回显请求内容: %s", raw)
	}
}

func (g *relayRig) sshOp(op termproto.SSHOp) {
	g.t.Helper()
	op.T = termproto.TSSHOp
	data, err := json.Marshal(op)
	if err != nil {
		g.t.Fatal(err)
	}
	g.r.handleText(data)
}

func (g *relayRig) sshRes() termproto.SSHResult {
	g.t.Helper()
	typ, data := g.next()
	if typ != websocket.MessageText {
		g.t.Fatalf("期望文本帧，收到二进制帧")
	}
	var res termproto.SSHResult
	if err := json.Unmarshal(data, &res); err != nil || res.T != termproto.TSSHResult {
		g.t.Fatalf("不是 ssh_result: %s (%v)", data, err)
	}
	return res
}

// 本机一票否决（--deny-model-exec）同样拦住经 SSH 网关代理的 exec：Agent 自己直接应答，根本不转发给守护进程；
// 其他类型的 ssh_op（探测 / 登记 / 同步）是人工操作，不受影响。
func TestRelayVetoesSSHExecOnly(t *testing.T) {
	g := newRelayRig(t, nil)
	g.r.denyExec = true

	g.sshOp(termproto.SSHOp{Req: "x1", Op: termproto.SSHOpExec, Cmd: "sh -s", Stdin: []byte("echo hi")})
	res := g.sshRes()
	if res.OK || res.Req != "x1" || res.Code != termproto.SSHCodeExecDenied {
		t.Fatalf("被一票否决的 exec 应得到 exec_denied: %+v", res)
	}

	// 没有被否决的操作走转发路径；测试里没有守护进程，转发失败会得到「守护进程不可用」，而不是 exec_denied。
	for _, op := range []string{termproto.SSHOpProbe, termproto.SSHOpCheck, termproto.SSHOpCredSync, termproto.SSHOpCredDel} {
		g.sshOp(termproto.SSHOp{Req: "o-" + op, Op: op})
		if r := g.sshRes(); r.Code == termproto.SSHCodeExecDenied || r.Req != "o-"+op {
			t.Fatalf("%s 不应被一票否决: %+v", op, r)
		}
	}

	// 没有一票否决时 exec 同样走转发。
	g.r.denyExec = false
	g.sshOp(termproto.SSHOp{Req: "x2", Op: termproto.SSHOpExec, Cmd: "sh -s"})
	if r := g.sshRes(); r.Code == termproto.SSHCodeExecDenied || r.Req != "x2" {
		t.Fatalf("没有否决时不应得到 exec_denied: %+v", r)
	}
}
