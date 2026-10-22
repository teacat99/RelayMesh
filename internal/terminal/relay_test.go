package terminal

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/teacat99/RelayMesh/v2/internal/termproto"
)

func writeJSONFrame(t *testing.T, ws *websocket.Conn, v any) {
	t.Helper()
	b, _ := json.Marshal(v)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := ws.Write(ctx, websocket.MessageText, b); err != nil {
		t.Fatalf("发送控制帧失败: %v", err)
	}
}

// readUntilClose 读取直到连接被关闭，返回关闭码；期间收到的文本帧交给 onText。
func readUntilClose(t *testing.T, ws *websocket.Conn, timeout time.Duration, onText func([]byte)) websocket.StatusCode {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	for {
		typ, data, err := ws.Read(ctx)
		if err != nil {
			if ctx.Err() != nil {
				t.Fatalf("等待连接关闭超时")
			}
			return websocket.CloseStatus(err)
		}
		if typ == websocket.MessageText && onText != nil {
			onText(data)
		}
	}
}

// attachedSession 是建立好的「主机在线 + 会话 + 浏览器附着」场景。
type attachedSession struct {
	env   *testEnv
	agent *fakeAgent
	sid   string
	hdr   []string
	ws    *websocket.Conn
	ready termproto.BrowserReady
}

func (e *testEnv) attachedSession(since uint64) *attachedSession {
	e.t.Helper()
	hdr := e.admin()
	a := e.enrollAgent(testMeta("relay-host"), "中继主机")
	sid := e.openSession(a, hdr, "main")
	ticket := e.ticket(a, sid, hdr, since)
	ws, _, err := e.dialBrowser(ticket, nil)
	if err != nil {
		e.t.Fatalf("浏览器拨号失败: %v", err)
	}
	e.t.Cleanup(func() { _ = ws.CloseNow() })
	ready := readReady(e.t, ws)
	return &attachedSession{env: e, agent: a, sid: sid, hdr: hdr, ws: ws, ready: ready}
}

func TestBrowserAttachEchoRoundTrip(t *testing.T) {
	e := newTestEnv(t)
	s := e.attachedSession(0)

	if s.ready.SID != s.sid || s.ready.State != termproto.SessionRunning || s.ready.Rows == 0 || s.ready.Cols == 0 {
		t.Fatalf("ready 内容不符: %+v", s.ready)
	}
	// 票据里绑定的尺寸（40×120）应随 attach 下发给 Agent
	s.agent.mu.Lock()
	att := s.agent.attaches
	s.agent.mu.Unlock()
	if len(att) != 1 || att[0].Rows != 40 || att[0].Cols != 120 || att[0].SID != s.sid || att[0].Since != 0 {
		t.Fatalf("Agent 收到的 attach 不符: %+v", att)
	}

	// 输入 → Agent 收到（精确字节）→ 回显输出 → 浏览器收到（带偏移）
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	payload := []byte("echo 你好\r")
	if err := s.ws.Write(ctx, websocket.MessageBinary, termproto.EncodeBrowserInput(payload)); err != nil {
		t.Fatal(err)
	}
	out, first := readOutput(t, s.ws, len(payload))
	if !bytes.Equal(out, payload) || first != 0 {
		t.Fatalf("回显应逐字节一致且从偏移 0 开始: %q @%d", out, first)
	}
	s.agent.mu.Lock()
	got := bytes.Join(s.agent.inputs, nil)
	s.agent.mu.Unlock()
	if !bytes.Equal(got, payload) {
		t.Fatalf("Agent 收到的输入不一致: %q", got)
	}

	// ack 被转发给 Agent，且不超过已发送的偏移
	writeAck(t, s.ws, uint64(len(payload)))
	eventually(t, 2*time.Second, "Agent 收到 ack", func() bool {
		s.agent.mu.Lock()
		defer s.agent.mu.Unlock()
		return len(s.agent.acks) > 0 && s.agent.acks[len(s.agent.acks)-1] == uint64(len(payload))
	})

	// resize 被转发
	writeJSONFrame(t, s.ws, termproto.BrowserResize{T: termproto.TResize, Rows: 50, Cols: 200})
	eventually(t, 2*time.Second, "Agent 收到 resize", func() bool {
		s.agent.mu.Lock()
		defer s.agent.mu.Unlock()
		return len(s.agent.resizes) == 1 && s.agent.resizes[0].Rows == 50 && s.agent.resizes[0].Cols == 200 && s.agent.resizes[0].SID == s.sid
	})

	// 应用层 ping / pong
	writeJSONFrame(t, s.ws, termproto.Envelope{T: termproto.TPing})
	gotPong := false
	for i := 0; i < 5 && !gotPong; i++ {
		typ, data, err := s.ws.Read(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if typ == websocket.MessageText {
			if tt, _ := termproto.ParseType(data); tt == termproto.TPong {
				gotPong = true
			}
		}
	}
	if !gotPong {
		t.Fatal("应收到 pong")
	}

	// 浏览器断开 → Agent 收到 detach，Hub 释放附着名额
	_ = s.ws.Close(websocket.StatusNormalClosure, "")
	eventually(t, 2*time.Second, "Agent 收到 detach", func() bool {
		s.agent.mu.Lock()
		defer s.agent.mu.Unlock()
		return len(s.agent.detaches) == 1
	})
	eventually(t, 2*time.Second, "附着名额被归还", func() bool {
		e.hub.mu.RLock()
		defer e.hub.mu.RUnlock()
		return e.hub.attachTotal == 0
	})
}

func TestBrowserWSTicketRules(t *testing.T) {
	e := newTestEnv(t, withFakeClock())
	hdr := e.admin()
	a := e.enrollAgent(testMeta("ticket-host"), "")
	sid := e.openSession(a, hdr, "s")

	t.Run("票据只能用一次", func(t *testing.T) {
		ticket := e.ticket(a, sid, hdr, 0)
		ws, _, err := e.dialBrowser(ticket, nil)
		if err != nil {
			t.Fatal(err)
		}
		readReady(t, ws)
		_ = ws.CloseNow()
		_, resp, err := e.dialBrowser(ticket, nil)
		if err == nil || resp == nil || resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("重放票据应返回 401: err=%v resp=%+v", err, resp)
		}
	})

	t.Run("票据 30 秒过期", func(t *testing.T) {
		ticket := e.ticket(a, sid, hdr, 0)
		e.clock.Advance(31 * time.Second)
		_, resp, err := e.dialBrowser(ticket, nil)
		if err == nil || resp == nil || resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("过期票据应返回 401: err=%v resp=%+v", err, resp)
		}
	})

	t.Run("缺少或伪造票据", func(t *testing.T) {
		for _, tk := range []string{"", "forged-ticket-value", strings.Repeat("A", 43)} {
			_, resp, err := e.dialBrowser(tk, nil)
			if err == nil || resp == nil || resp.StatusCode != http.StatusUnauthorized {
				t.Fatalf("票据 %q 应返回 401: err=%v resp=%+v", tk, err, resp)
			}
		}
	})

	t.Run("URL 参数中的票据无效", func(t *testing.T) {
		ticket := e.ticket(a, sid, hdr, 0)
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_, resp, err := websocket.Dial(ctx, e.browserURL()+"?ticket="+ticket, &websocket.DialOptions{
			Subprotocols: []string{termproto.SubprotocolBrowser},
		})
		if err == nil || resp == nil || resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("只应从子协议头读取票据: err=%v resp=%+v", err, resp)
		}
	})

	t.Run("握手响应不回显票据", func(t *testing.T) {
		ticket := e.ticket(a, sid, hdr, 0)
		ws, resp, err := e.dialBrowser(ticket, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer ws.CloseNow()
		if got := resp.Header.Get("Sec-WebSocket-Protocol"); got != termproto.SubprotocolBrowser {
			t.Fatalf("响应的子协议应只有 %q，得到 %q", termproto.SubprotocolBrowser, got)
		}
		if ws.Subprotocol() != termproto.SubprotocolBrowser {
			t.Fatalf("协商结果不符: %q", ws.Subprotocol())
		}
	})

	t.Run("票据绑定会话：主机下线后拨号会被告知", func(t *testing.T) {
		ticket := e.ticket(a, sid, hdr, 0)
		a.close()
		eventually(t, 2*time.Second, "主机离线", func() bool { return e.hub.onlineAgent(a.code) == nil })
		ws, _, err := e.dialBrowser(ticket, nil)
		if err != nil {
			t.Fatalf("应先完成 WebSocket 升级再以应用码告知: %v", err)
		}
		defer ws.CloseNow()
		if code := readUntilClose(t, ws, 3*time.Second, nil); code != termproto.CloseNotFound {
			t.Fatalf("主机离线应以 4404 关闭: %d", code)
		}
	})
}

func TestBrowserWSOriginPolicy(t *testing.T) {
	e := newTestEnv(t, withOrigins("https://relay.example.com"))
	hdr := e.admin()
	a := e.enrollAgent(testMeta("origin-host"), "")
	sid := e.openSession(a, hdr, "s")

	try := func(origin string) (*http.Response, error) {
		h := http.Header{}
		if origin != "" {
			h.Set("Origin", origin)
		}
		ws, resp, err := e.dialBrowser(e.ticket(a, sid, hdr, 0), h)
		if err == nil {
			_ = ws.CloseNow()
		}
		return resp, err
	}

	hostOrigin := "http://" + strings.TrimPrefix(e.srv.URL, "http://")
	if _, err := try(hostOrigin); err != nil {
		t.Errorf("同源应放行: %v", err)
	}
	if _, err := try(""); err != nil {
		t.Errorf("无 Origin（非浏览器客户端）凭有效票据应放行: %v", err)
	}
	if _, err := try("https://relay.example.com"); err != nil {
		t.Errorf("白名单内的 Origin 应放行: %v", err)
	}
	for _, bad := range []string{"https://evil.example", "http://localhost:9999", "null", "https://relay.example.com.evil.example"} {
		resp, err := try(bad)
		if err == nil || resp == nil || resp.StatusCode != http.StatusForbidden {
			t.Errorf("跨源 %q 应返回 403: err=%v resp=%+v", bad, err, resp)
		}
	}
}

func TestReplayFromOffset(t *testing.T) {
	e := newTestEnv(t)
	hdr := e.admin()
	a := e.enrollAgent(testMeta("replay-host"), "")
	sid := e.openSession(a, hdr, "s")

	// 会话在无人附着时产生了输出
	history := bytes.Repeat([]byte("0123456789"), 10) // 100 字节
	a.emit(sid, history)

	ticket := e.ticket(a, sid, hdr, 40) // 从偏移 40 开始回放
	ws, _, err := e.dialBrowser(ticket, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ws.CloseNow()
	ready := readReady(t, ws)
	if ready.From != 40 || ready.End != 100 {
		t.Fatalf("应从 40 回放到 100: %+v", ready)
	}
	out, first := readOutput(t, ws, 60)
	if first != 40 || !bytes.Equal(out, history[40:]) {
		t.Fatalf("回放内容或起始偏移不符: @%d %q", first, out)
	}
}

func TestKillNotifiesBrowserAndRemovesSession(t *testing.T) {
	e := newTestEnv(t)
	s := e.attachedSession(0)

	res := e.call("POST", "/api/v1/terminal/hosts/"+s.agent.code+"/sessions/"+s.sid+"/kill", nil, s.hdr...)
	if res.Status != http.StatusAccepted {
		t.Fatalf("终止失败: %d %s", res.Status, res.Raw)
	}
	var sawExit bool
	code := readUntilClose(t, s.ws, 3*time.Second, func(b []byte) {
		if tt, _ := termproto.ParseType(b); tt == termproto.TExit {
			var m termproto.BrowserExit
			_ = json.Unmarshal(b, &m)
			sawExit = m.Code != nil && *m.Code == 137
		}
	})
	if !sawExit {
		t.Fatal("关闭前应先通知浏览器进程退出码（exit 帧必须先于关闭送达）")
	}
	if code != termproto.CloseSessionEnded {
		t.Fatalf("会话结束应以 4410 关闭: %d", code)
	}
	eventually(t, 2*time.Second, "会话从清单消失", func() bool {
		r := e.call("GET", "/api/v1/terminal/hosts/"+s.agent.code+"/sessions", nil, "Authorization", humanAuth(testUser))
		ss, _ := r.Body["sessions"].([]any)
		return len(ss) == 0
	})
}

func TestAgentDropClosesBrowserAttachments(t *testing.T) {
	e := newTestEnv(t)
	s := e.attachedSession(0)
	s.agent.close()
	if code := readUntilClose(t, s.ws, 3*time.Second, nil); code != termproto.CloseNotFound {
		t.Fatalf("Agent 掉线应以 4404 关闭浏览器连接: %d", code)
	}
	eventually(t, 2*time.Second, "名额归还", func() bool {
		e.hub.mu.RLock()
		defer e.hub.mu.RUnlock()
		return e.hub.attachTotal == 0
	})
}

func TestStalledBrowserIsDisconnected(t *testing.T) {
	e := newTestEnv(t, withFakeClock())
	s := e.attachedSession(0)

	// Agent 产生超过半个窗口的输出，而浏览器一个 ack 都不发
	s.agent.emit(s.sid, bytes.Repeat([]byte("x"), termproto.AttachWindow/2+4096))
	time.Sleep(150 * time.Millisecond) // 让 Hub 把数据交给发送队列
	e.clock.Advance(termproto.StallTimeout + time.Second)

	code := readUntilClose(t, s.ws, 5*time.Second, nil)
	if code != termproto.CloseStalled {
		t.Fatalf("长时间不确认应以 4412 断开: %d", code)
	}
	eventually(t, 2*time.Second, "Agent 收到 detach", func() bool {
		s.agent.mu.Lock()
		defer s.agent.mu.Unlock()
		return len(s.agent.detaches) == 1
	})
}

func TestHealthyBrowserIsNotMistakenForStalled(t *testing.T) {
	e := newTestEnv(t, withFakeClock())
	s := e.attachedSession(0)

	chunk := bytes.Repeat([]byte("y"), termproto.AttachWindow/2+1024)
	s.agent.emit(s.sid, chunk)
	got, _ := readOutput(t, s.ws, len(chunk))
	if len(got) != len(chunk) {
		t.Fatal("输出不完整")
	}
	writeAck(t, s.ws, uint64(len(chunk))) // 及时确认
	time.Sleep(100 * time.Millisecond)
	e.clock.Advance(termproto.StallTimeout * 3)
	time.Sleep(1300 * time.Millisecond) // 让 1 秒一次的停滞检查至少运行一轮

	// 连接仍然可用
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := s.ws.Write(ctx, websocket.MessageBinary, termproto.EncodeBrowserInput([]byte("ok"))); err != nil {
		t.Fatalf("已确认输出的浏览器不应被断开: %v", err)
	}
	out, _ := readOutput(t, s.ws, 2)
	if string(out) != "ok" {
		t.Fatalf("回显不符: %q", out)
	}
}

func TestAckBeyondDeliveredIsClamped(t *testing.T) {
	e := newTestEnv(t)
	s := e.attachedSession(0)
	s.agent.emit(s.sid, []byte("abcde"))
	readOutput(t, s.ws, 5)
	writeAck(t, s.ws, 1<<40) // 浏览器声称确认了远超已发送的数据
	eventually(t, 2*time.Second, "Agent 收到被夹取的 ack", func() bool {
		s.agent.mu.Lock()
		defer s.agent.mu.Unlock()
		return len(s.agent.acks) > 0
	})
	s.agent.mu.Lock()
	last := s.agent.acks[len(s.agent.acks)-1]
	s.agent.mu.Unlock()
	if last != 5 {
		t.Fatalf("ack 应被夹取到已发送偏移 5，实际 %d", last)
	}
}

func TestOversizedBrowserInputIsRejected(t *testing.T) {
	e := newTestEnv(t)
	s := e.attachedSession(0)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	big := termproto.EncodeBrowserInput(bytes.Repeat([]byte("z"), termproto.MaxInputFrame+1))
	if err := s.ws.Write(ctx, websocket.MessageBinary, big); err != nil {
		t.Fatal(err)
	}
	if code := readUntilClose(t, s.ws, 3*time.Second, nil); code != websocket.StatusUnsupportedData {
		t.Fatalf("超长输入帧应以 1003 关闭: %d", code)
	}
	s.agent.mu.Lock()
	n := len(s.agent.inputs)
	s.agent.mu.Unlock()
	if n != 0 {
		t.Fatalf("超长输入不得被转发: %d", n)
	}
}

func TestSessionLimitAndAgentRefusals(t *testing.T) {
	e := newTestEnv(t, withOpenTimeout(300*time.Millisecond), withAttachTimeout(300*time.Millisecond))
	hdr := e.admin()
	a := e.enrollAgent(testMeta("limit-host"), "")

	for i := 0; i < termproto.MaxSessionsPerHost; i++ {
		e.openSession(a, hdr, "s")
	}
	eventually(t, 2*time.Second, "会话清单同步", func() bool {
		return len(e.hub.onlineAgent(a.code).sessionList()) == termproto.MaxSessionsPerHost
	})
	res := e.call("POST", "/api/v1/terminal/hosts/"+a.code+"/sessions", nil, hdr...)
	if res.Status != http.StatusConflict || res.str("code") != "session_limit" {
		t.Fatalf("超过会话上限应返回 409 session_limit: %d %s", res.Status, res.Raw)
	}

	t.Run("Agent 拒绝新建", func(t *testing.T) {
		e2 := newTestEnv(t, withOpenTimeout(300*time.Millisecond))
		a2 := e2.enrollAgent(testMeta("refuse-host"), "")
		a2.mu.Lock()
		a2.refuseOpen = "pty unavailable"
		a2.mu.Unlock()
		res := e2.call("POST", "/api/v1/terminal/hosts/"+a2.code+"/sessions", nil, e2.admin()...)
		if res.Status != http.StatusBadGateway || res.str("code") != "agent_refused" {
			t.Fatalf("应返回 502 agent_refused: %d %s", res.Status, res.Raw)
		}
	})

	t.Run("Agent 不应答", func(t *testing.T) {
		e2 := newTestEnv(t, withOpenTimeout(300*time.Millisecond))
		a2 := e2.enrollAgent(testMeta("silent-host"), "")
		a2.mu.Lock()
		a2.silentOpen = true
		a2.mu.Unlock()
		res := e2.call("POST", "/api/v1/terminal/hosts/"+a2.code+"/sessions", nil, e2.admin()...)
		if res.Status != http.StatusGatewayTimeout || res.str("code") != "agent_timeout" {
			t.Fatalf("应返回 504 agent_timeout: %d %s", res.Status, res.Raw)
		}
	})

	t.Run("Agent 拒绝附着", func(t *testing.T) {
		e2 := newTestEnv(t, withAttachTimeout(300*time.Millisecond))
		hdr2 := e2.admin()
		a2 := e2.enrollAgent(testMeta("noattach-host"), "")
		sid := e2.openSession(a2, hdr2, "s")
		a2.mu.Lock()
		a2.refuseAttach = "busy"
		a2.mu.Unlock()
		ws, _, err := e2.dialBrowser(e2.ticket(a2, sid, hdr2, 0), nil)
		if err != nil {
			t.Fatal(err)
		}
		defer ws.CloseNow()
		if code := readUntilClose(t, ws, 3*time.Second, nil); code != termproto.CloseNotFound {
			t.Fatalf("附着被拒应以 4404 关闭: %d", code)
		}
	})
}

func TestOfflineHostOperationsReturnConflict(t *testing.T) {
	e := newTestEnv(t)
	hdr := e.admin()
	a := e.enrollAgent(testMeta("gone-host"), "")
	sid := e.openSession(a, hdr, "s")
	a.close()
	eventually(t, 2*time.Second, "离线", func() bool { return e.hub.onlineAgent(a.code) == nil })

	base := "/api/v1/terminal/hosts/" + a.code
	for name, res := range map[string]apiResult{
		"新建会话": e.call("POST", base+"/sessions", nil, hdr...),
		"签发票据": e.call("POST", base+"/sessions/"+sid+"/ticket", nil, hdr...),
		"终止会话": e.call("POST", base+"/sessions/"+sid+"/kill", nil, hdr...),
	} {
		if res.Status != http.StatusConflict || res.str("code") != "host_offline" {
			t.Errorf("%s: 离线主机应返回 409 host_offline: %d %s", name, res.Status, res.Raw)
		}
	}
	// 只读的会话列表不报错，只标明离线
	res := e.call("GET", base+"/sessions", nil, "Authorization", humanAuth(testUser))
	if res.Status != 200 || res.Body["online"] != false {
		t.Errorf("会话列表应返回 online=false: %d %s", res.Status, res.Raw)
	}
}

func TestBadIdentifiersAreRejected(t *testing.T) {
	e := newTestEnv(t)
	hdr := e.admin()
	for name, path := range map[string]string{
		"机器码":   "/api/v1/terminal/hosts/not-a-code/sessions",
		"会话 ID": "/api/v1/terminal/hosts/" + sampleCode + "/sessions/NOT-HEX/ticket",
	} {
		method := "GET"
		if strings.HasSuffix(path, "/ticket") {
			method = "POST"
		}
		res := e.call(method, path, nil, hdr...)
		if res.Status != http.StatusBadRequest {
			t.Errorf("%s 非法应返回 400: %d %s", name, res.Status, res.Raw)
		}
	}
}

func TestGateFlipWhileAttachedTearsEverythingDown(t *testing.T) {
	e := newTestEnv(t)
	s := e.attachedSession(0)
	hdr := s.hdr

	// 管理员清空了 Web 密码
	e.creds.set(testUser, "", nil)

	if code := readUntilClose(t, s.ws, 3*time.Second, nil); code != termproto.CloseGateClosed {
		t.Fatalf("浏览器连接应以 4403 关闭: %d", code)
	}
	if code := s.agent.waitClosed(3 * time.Second); code != termproto.CloseGateClosed {
		t.Fatalf("Agent 连接应以 4403 关闭: %d", code)
	}
	s.agent.mu.Lock()
	var gotPolicy *termproto.Policy
	if n := len(s.agent.policies); n > 0 {
		gotPolicy = &s.agent.policies[n-1]
	}
	s.agent.mu.Unlock()
	if gotPolicy == nil || gotPolicy.Terminal || gotPolicy.Reason != string(ReasonNoPassword) {
		t.Fatalf("断开前应通知 Agent 终端已关闭及原因: %+v", gotPolicy)
	}

	// 令牌已被撤销：即使总闸重新开启，旧的解锁令牌也不能再用
	e.creds.set(testUser, testPassword, nil)
	eventually(t, 2*time.Second, "总闸重新开启", func() bool { return e.hub.Enabled(t.Context()) })
	if res := e.call("POST", "/api/v1/terminal/discovery", nil, hdr...); res.Status != http.StatusForbidden || res.str("code") != "unlock_required" {
		t.Fatalf("总闸关闭期间旧解锁令牌必须已被撤销: %d %s", res.Status, res.Raw)
	}
	eventually(t, 2*time.Second, "总闸关闭与重新开启各记录一次审计", func() bool {
		logs, _ := e.store.ListTerminalAudit(t.Context(), 100, 0)
		flips := 0
		for _, l := range logs {
			if l.Event == EvGateChanged {
				flips++
			}
		}
		return flips == 2
	})
}

func TestGateFlipDropsDiscoveryAndPending(t *testing.T) {
	e := newTestEnv(t)
	hdr := e.admin()
	e.openDiscovery(hdr)
	_, priv := newFakeIdentity(t)
	a, _, _ := e.dialAgent(priv, testMeta("p"), nil)
	defer a.close()
	if err := a.handshake(testMeta("p")); err != nil {
		t.Fatal(err)
	}
	a.start()
	eventually(t, 2*time.Second, "待启用", func() bool { return len(e.hub.pending.snapshot()) == 1 })

	e.creds.set(testUser, "", nil)
	if code := a.waitClosed(3 * time.Second); code != termproto.CloseGateClosed {
		t.Fatalf("待启用连接应以 4403 关闭: %d", code)
	}
	if n := len(e.hub.pending.snapshot()); n != 0 {
		t.Fatalf("总闸关闭应清空待启用: %d", n)
	}
	if active, _, _ := e.hub.disc.state(time.Now()); active {
		t.Fatal("总闸关闭应关闭发现窗口")
	}
}

func TestCredentialsChangedRevokesTokensAndAttachments(t *testing.T) {
	e := newTestEnv(t)
	s := e.attachedSession(0)
	ticket := e.ticket(s.agent, s.sid, s.hdr, 0) // 尚未使用的票据

	e.hub.CredentialsChanged("admin", "127.0.0.1")

	if code := readUntilClose(t, s.ws, 3*time.Second, nil); code != termproto.CloseGateClosed {
		t.Fatalf("凭据变更应断开浏览器终端: %d", code)
	}
	if _, resp, err := e.dialBrowser(ticket, nil); err == nil || resp == nil || resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("凭据变更后未使用的票据必须作废: err=%v resp=%+v", err, resp)
	}
	if res := e.call("POST", "/api/v1/terminal/discovery", nil, s.hdr...); res.Status != http.StatusForbidden {
		t.Fatalf("凭据变更后解锁令牌必须作废: %d", res.Status)
	}
	// Agent 是机器身份，不受管理员改密影响
	if e.hub.onlineAgent(s.agent.code) == nil {
		t.Fatal("改密不应断开 Agent 连接")
	}
}

func TestShutdownClosesEverythingGracefully(t *testing.T) {
	e := newTestEnv(t)
	s := e.attachedSession(0)

	// 浏览器端要在 Shutdown 期间持续读取，才能像真实浏览器一样及时回应关闭帧。
	browserClosed := make(chan websocket.StatusCode, 1)
	go func() { browserClosed <- readUntilClose(t, s.ws, 5*time.Second, nil) }()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	start := time.Now()
	e.hub.Shutdown(ctx)
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("对端及时响应时 Shutdown 应迅速完成，实际耗时 %v", d)
	}

	if code := <-browserClosed; code != websocket.StatusGoingAway {
		t.Fatalf("浏览器连接应以 1001 关闭: %d", code)
	}
	if code := s.agent.waitClosed(3 * time.Second); code != websocket.StatusGoingAway {
		t.Fatalf("Agent 连接应以 1001 关闭: %d", code)
	}
	// 关闭后拒绝新连接
	if _, resp, err := e.dialRaw(nil); err == nil || resp == nil || resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("关闭后应拒绝新的 Agent 连接: err=%v resp=%+v", err, resp)
	}
}

func TestWebSocketsSurviveHTTPServerTimeouts(t *testing.T) {
	// 生产服务器 ReadTimeout=30s / WriteTimeout=600s；这里缩到 300ms 验证被劫持的 WebSocket 不受它们影响。
	e := newTestEnv(t, withHTTPTimeouts(300*time.Millisecond, 300*time.Millisecond))
	s := e.attachedSession(0)

	time.Sleep(900 * time.Millisecond) // 超过读 / 写超时的 3 倍
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := s.ws.Write(ctx, websocket.MessageBinary, termproto.EncodeBrowserInput([]byte("still alive"))); err != nil {
		t.Fatalf("超时之后写入失败: %v", err)
	}
	out, _ := readOutput(t, s.ws, len("still alive"))
	if string(out) != "still alive" {
		t.Fatalf("回显不符: %q", out)
	}
}

func TestManyBrowsersOnOneSessionStayConsistent(t *testing.T) {
	e := newTestEnv(t)
	hdr := e.admin()
	a := e.enrollAgent(testMeta("multi-host"), "")
	sid := e.openSession(a, hdr, "shared")

	const browsers = 8
	conns := make([]*websocket.Conn, browsers)
	for i := range conns {
		ws, _, err := e.dialBrowser(e.ticket(a, sid, hdr, 0), nil)
		if err != nil {
			t.Fatal(err)
		}
		readReady(t, ws)
		conns[i] = ws
		defer ws.CloseNow()
	}

	// Agent 并发产生输出，所有浏览器都必须按同样的偏移收到同样的字节流
	var want []byte
	for i := 0; i < 50; i++ {
		chunk := []byte(strings.Repeat(string(rune('a'+i%26)), 100+i))
		want = append(want, chunk...)
		a.emit(sid, chunk)
	}
	var wg sync.WaitGroup
	results := make([][]byte, browsers)
	for i, ws := range conns {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			var got []byte
			next := uint64(0)
			for len(got) < len(want) {
				typ, data, err := ws.Read(ctx)
				if err != nil {
					return
				}
				if typ != websocket.MessageBinary {
					continue
				}
				off, payload, err := termproto.DecodeBrowserOutput(data)
				if err != nil || off != next {
					return // 偏移不连续即判为失败（results[i] 将不完整）
				}
				next += uint64(len(payload))
				got = append(got, payload...)
			}
			results[i] = got
		}()
	}
	wg.Wait()
	for i, got := range results {
		if !bytes.Equal(got, want) {
			t.Fatalf("浏览器 %d 收到的字节流不一致（%d/%d 字节）", i, len(got), len(want))
		}
	}
}
