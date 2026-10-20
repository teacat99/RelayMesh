package terminal

import (
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

// dialRaw 只做 WebSocket 握手，不做应用层握手，用于构造畸形 / 超时场景。
func (e *testEnv) dialRaw(header http.Header) (*websocket.Conn, *http.Response, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return websocket.Dial(ctx, e.agentURL(), &websocket.DialOptions{
		Subprotocols: []string{termproto.SubprotocolAgent},
		HTTPHeader:   header,
	})
}

func xff(ip string) http.Header {
	h := http.Header{}
	h.Set("X-Forwarded-For", ip)
	return h
}

func TestUnknownAgentRejectedWhenDiscoveryClosed(t *testing.T) {
	e := newTestEnv(t)
	_, priv := newFakeIdentity(t)
	a, _, err := e.dialAgent(priv, testMeta("stranger"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer a.close()
	// 服务端可能在读完 hello 之后才关闭，也可能在写入 welcome 之前：两种都只接受 4405。
	err = a.handshake(testMeta("stranger"))
	if code := websocket.CloseStatus(err); code != termproto.CloseUnknownMachine {
		t.Fatalf("未知主机在发现窗口关闭时应以 4405 被拒绝，得到 err=%v code=%d", err, code)
	}
	if n := len(e.hub.pending.snapshot()); n != 0 {
		t.Fatalf("不得产生待启用条目: %d", n)
	}
	if recs, _ := e.store.ListTerminalHosts(t.Context()); len(recs) != 0 {
		t.Fatalf("不得写入数据库: %d", len(recs))
	}
}

func TestAgentWithOriginHeaderIsRejectedBeforeUpgrade(t *testing.T) {
	e := newTestEnv(t)
	h := http.Header{}
	h.Set("Origin", "https://evil.example")
	_, resp, err := e.dialRaw(h)
	if err == nil {
		t.Fatal("带 Origin 的请求不应升级成功")
	}
	if resp == nil || resp.StatusCode != http.StatusForbidden {
		t.Fatalf("应返回 403，得到 %+v", resp)
	}
}

func TestAgentHandshakeFailures(t *testing.T) {
	e := newTestEnv(t, withHandshakeTimeout(300*time.Millisecond))
	e.openDiscovery(e.admin())

	t.Run("签名不匹配", func(t *testing.T) {
		_, priv := newFakeIdentity(t)
		_, otherPriv := newFakeIdentity(t)
		a, _, err := e.dialAgent(priv, testMeta("x"), nil)
		if err != nil {
			t.Fatal(err)
		}
		defer a.close()
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_, data, _ := a.ws.Read(ctx)
		var ch termproto.Challenge
		_ = json.Unmarshal(data, &ch)
		nonce, _ := termproto.DecodeKey(ch.Nonce, 32)
		// 用别人的私钥签名：公钥与签名不匹配
		hello := termproto.Hello{T: termproto.THello, V: termproto.ProtocolVersion,
			Pub: termproto.EncodeKey(a.pub), Sig: termproto.EncodeKey(termproto.SignChallenge(otherPriv, nonce)), Meta: testMeta("x")}
		err = a.sendHelloAndWelcome(ctx, hello)
		if code := websocket.CloseStatus(err); code != termproto.CloseInvalidHandshake {
			t.Fatalf("签名无效应以 4401 关闭: err=%v code=%d", err, code)
		}
	})

	t.Run("协议版本不兼容", func(t *testing.T) {
		_, priv := newFakeIdentity(t)
		a, _, err := e.dialAgent(priv, testMeta("x"), nil)
		if err != nil {
			t.Fatal(err)
		}
		defer a.close()
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_, data, _ := a.ws.Read(ctx)
		var ch termproto.Challenge
		_ = json.Unmarshal(data, &ch)
		nonce, _ := termproto.DecodeKey(ch.Nonce, 32)
		hello := termproto.Hello{T: termproto.THello, V: 99,
			Pub: termproto.EncodeKey(a.pub), Sig: termproto.EncodeKey(termproto.SignChallenge(priv, nonce)), Meta: testMeta("x")}
		err = a.sendHelloAndWelcome(ctx, hello)
		if code := websocket.CloseStatus(err); code != termproto.CloseInvalidHandshake {
			t.Fatalf("版本不兼容应以 4401 关闭: err=%v code=%d", err, code)
		}
	})

	t.Run("首帧超过 8KiB", func(t *testing.T) {
		ws, _, err := e.dialRaw(nil)
		if err != nil {
			t.Fatal(err)
		}
		defer ws.CloseNow()
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_, _, _ = ws.Read(ctx) // challenge
		_ = ws.Write(ctx, websocket.MessageText, []byte(strings.Repeat("A", termproto.MaxPreAuthFrame+1024)))
		_, _, err = ws.Read(ctx)
		if err == nil {
			t.Fatal("超长首帧必须导致连接被关闭")
		}
		if code := websocket.CloseStatus(err); code != websocket.StatusMessageTooBig {
			t.Fatalf("应以 1009 关闭，得到 %d (%v)", code, err)
		}
	})

	t.Run("握手超时", func(t *testing.T) {
		ws, _, err := e.dialRaw(nil)
		if err != nil {
			t.Fatal(err)
		}
		defer ws.CloseNow()
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_, _, _ = ws.Read(ctx) // challenge
		start := time.Now()
		_, _, err = ws.Read(ctx) // 不发送 hello，等待服务端超时关闭
		if err == nil || time.Since(start) > 2*time.Second {
			t.Fatalf("服务端应在握手期限后关闭连接: err=%v 耗时=%v", err, time.Since(start))
		}
	})

	t.Run("hello 不是文本帧", func(t *testing.T) {
		ws, _, err := e.dialRaw(nil)
		if err != nil {
			t.Fatal(err)
		}
		defer ws.CloseNow()
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_, _, _ = ws.Read(ctx)
		_ = ws.Write(ctx, websocket.MessageBinary, []byte{1, 2, 3})
		_, _, err = ws.Read(ctx)
		if code := websocket.CloseStatus(err); code != termproto.CloseInvalidHandshake {
			t.Fatalf("应以 4401 关闭: %v", err)
		}
	})

	if n := len(e.hub.pending.snapshot()); n != 0 {
		t.Fatalf("握手失败的连接不得进入待启用列表: %d", n)
	}
}

func TestPreAuthConcurrencyCapPerIP(t *testing.T) {
	e := newTestEnv(t)
	var conns []*websocket.Conn
	defer func() {
		for _, c := range conns {
			c.CloseNow()
		}
	}()
	for i := 0; i < termproto.MaxPreAuthPerIP; i++ {
		ws, _, err := e.dialRaw(xff("198.51.100.7"))
		if err != nil {
			t.Fatalf("第 %d 个预认证连接应被接受: %v", i+1, err)
		}
		conns = append(conns, ws)
	}
	_, resp, err := e.dialRaw(xff("198.51.100.7"))
	if err == nil || resp == nil || resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("超过单 IP 预认证并发上限应返回 429: err=%v resp=%+v", err, resp)
	}
	// 其他来源不受影响
	ws, _, err := e.dialRaw(xff("198.51.100.8"))
	if err != nil {
		t.Fatalf("其他来源应不受影响: %v", err)
	}
	conns = append(conns, ws)
}

func TestEnrollmentLifecycle(t *testing.T) {
	e := newTestEnv(t)
	hdr := e.admin()
	a := e.enrollAgent(testMeta("build-01"), "构建机")

	// 已持久化、已启用
	rec, err := e.store.GetTerminalHost(t.Context(), a.code)
	if err != nil || rec == nil || rec.Status != termproto.HostStatusEnabled || rec.Remark != "构建机" {
		t.Fatalf("启用后应持久化: %+v %v", rec, err)
	}
	if rec.PublicKey != termproto.EncodeKey(a.pub) {
		t.Fatal("库中只应保存公钥")
	}

	// 列表中在线且带会话清单
	sid := e.openSession(a, hdr, "main")
	res := e.call("GET", "/api/v1/terminal/hosts", nil, "Authorization", humanAuth(testUser))
	hosts, _ := res.Body["hosts"].([]any)
	if len(hosts) != 1 {
		t.Fatalf("应有 1 台主机: %s", res.Raw)
	}
	h0 := hosts[0].(map[string]any)
	if h0["online"] != true || h0["remark"] != "构建机" {
		t.Fatalf("主机视图不符: %v", h0)
	}
	if ss, _ := h0["sessions"].([]any); len(ss) != 1 || ss[0].(map[string]any)["sid"] != sid {
		t.Fatalf("应包含刚创建的会话: %v", h0["sessions"])
	}

	// 改备注只需登录
	rm := e.call("POST", "/api/v1/terminal/hosts/"+a.code+"/remark", map[string]string{"remark": "  新备注\x07\u202e "}, "Authorization", humanAuth(testUser))
	if rm.Status != 200 {
		t.Fatalf("改备注失败: %d %s", rm.Status, rm.Raw)
	}
	rec, _ = e.store.GetTerminalHost(t.Context(), a.code)
	if rec.Remark != "新备注" {
		t.Fatalf("备注应被净化（去掉控制字符与双向控制符）: %q", rec.Remark)
	}

	// 停用：Agent 收到「终端关闭」策略并被以 4409 断开
	if res := e.call("POST", "/api/v1/terminal/hosts/"+a.code+"/disable", nil, hdr...); res.Status != 200 {
		t.Fatalf("停用失败: %d %s", res.Status, res.Raw)
	}
	if code := a.waitClosed(3 * time.Second); code != termproto.CloseDisabled {
		t.Fatalf("停用应以 4409 断开，得到 %d", code)
	}
	a.mu.Lock()
	gotPolicy := len(a.policies) > 0 && !a.policies[len(a.policies)-1].Terminal
	a.mu.Unlock()
	if !gotPolicy {
		t.Fatal("断开前应收到 terminal=false 的策略，令 Agent 终止全部会话")
	}
	eventually(t, 2*time.Second, "Hub 注销在线登记", func() bool { return e.hub.onlineAgent(a.code) == nil })

	// 停用后重连：被拒绝
	_, priv := a.priv, a.priv
	b, _, err := e.dialAgent(priv, testMeta("build-01"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := b.handshake(testMeta("build-01")); websocket.CloseStatus(err) != termproto.CloseDisabled {
		t.Fatalf("已停用主机重连应以 4409 拒绝: %v", err)
	}
	b.close()

	// 重新启用后可上线
	if res := e.call("POST", "/api/v1/terminal/hosts/"+a.code+"/enable", nil, hdr...); res.Status != 200 {
		t.Fatalf("重新启用失败: %d %s", res.Status, res.Raw)
	}
	c := e.connectEnabled(priv, testMeta("build-01"))
	eventually(t, 2*time.Second, "重新上线", func() bool { return e.hub.onlineAgent(c.code) != nil })

	// 删除：在线连接被断开，记录消失
	if res := e.call("DELETE", "/api/v1/terminal/hosts/"+a.code, nil, hdr...); res.Status != 200 {
		t.Fatalf("删除失败: %d %s", res.Status, res.Raw)
	}
	if code := c.waitClosed(3 * time.Second); code != termproto.CloseDisabled {
		t.Fatalf("删除应断开在线连接，得到 %d", code)
	}
	if rec, _ := e.store.GetTerminalHost(t.Context(), a.code); rec != nil {
		t.Fatal("删除后记录应消失")
	}
	// 删除后重连：记录已不存在，发现窗口仍开着时重新成为待启用；窗口关闭后则是未知主机。
	d, _, _ := e.dialAgent(priv, testMeta("build-01"), nil)
	if err := d.handshake(testMeta("build-01")); err != nil || d.welcome.Status != termproto.WelcomePending {
		t.Fatalf("删除后的主机应重新可被发现: %v %+v", err, d.welcome)
	}
	d.close()
	if res := e.call("DELETE", "/api/v1/terminal/discovery", nil, hdr...); res.Status != 200 {
		t.Fatalf("关闭发现窗口失败: %d %s", res.Status, res.Raw)
	}
	f, _, _ := e.dialAgent(priv, testMeta("build-01"), nil)
	defer f.close()
	if err := f.handshake(testMeta("build-01")); websocket.CloseStatus(err) != termproto.CloseUnknownMachine {
		t.Fatalf("窗口关闭后的已删除主机应被视为未知: %v", err)
	}
}

func TestIgnoreAndUnignore(t *testing.T) {
	e := newTestEnv(t)
	hdr := e.admin()
	e.openDiscovery(hdr)
	_, priv := newFakeIdentity(t)

	a, _, _ := e.dialAgent(priv, testMeta("noisy"), nil)
	defer a.close()
	if err := a.handshake(testMeta("noisy")); err != nil || a.welcome.Status != termproto.WelcomePending {
		t.Fatalf("应进入待启用: %v %+v", err, a.welcome)
	}
	a.start()
	eventually(t, 2*time.Second, "出现在待启用列表", func() bool { return len(e.hub.pending.snapshot()) == 1 })

	if res := e.call("POST", "/api/v1/terminal/hosts/"+a.code+"/ignore", nil, hdr...); res.Status != 200 {
		t.Fatalf("忽略失败: %d %s", res.Status, res.Raw)
	}
	if code := a.waitClosed(3 * time.Second); code != termproto.CloseDisabled {
		t.Fatalf("忽略应以 4409 断开: %d", code)
	}
	a.mu.Lock()
	rejected := a.rejected
	a.mu.Unlock()
	if !rejected {
		t.Fatal("断开前应收到 rejected")
	}
	rec, _ := e.store.GetTerminalHost(t.Context(), a.code)
	if rec == nil || rec.Status != termproto.HostStatusBlocked {
		t.Fatalf("被忽略的主机应记为 blocked: %+v", rec)
	}

	// 被忽略的主机不能被直接启用（需要先取消忽略）
	if res := e.call("POST", "/api/v1/terminal/hosts/"+a.code+"/enable", nil, hdr...); res.Status != http.StatusNotFound {
		t.Fatalf("blocked 主机不应能被直接启用: %d %s", res.Status, res.Raw)
	}
	if rec, _ := e.store.GetTerminalHost(t.Context(), a.code); rec == nil || rec.Status != termproto.HostStatusBlocked {
		t.Fatalf("失败的启用不得改变 blocked 状态: %+v", rec)
	}

	// 窗口仍开着，但 blocked 主机再次连接不得出现在待启用列表
	b, _, _ := e.dialAgent(priv, testMeta("noisy"), nil)
	defer b.close()
	if err := b.handshake(testMeta("noisy")); websocket.CloseStatus(err) != termproto.CloseDisabled {
		t.Fatalf("blocked 主机应被直接拒绝: %v", err)
	}
	if n := len(e.hub.pending.snapshot()); n != 0 {
		t.Fatalf("blocked 主机不得再进入待启用: %d", n)
	}

	// 取消忽略后可再次被发现
	if res := e.call("POST", "/api/v1/terminal/hosts/"+a.code+"/unignore", nil, hdr...); res.Status != 200 {
		t.Fatalf("取消忽略失败: %d %s", res.Status, res.Raw)
	}
	c, _, _ := e.dialAgent(priv, testMeta("noisy"), nil)
	defer c.close()
	if err := c.handshake(testMeta("noisy")); err != nil || c.welcome.Status != termproto.WelcomePending {
		t.Fatalf("取消忽略后应能再次进入待启用: %v %+v", err, c.welcome)
	}
}

func TestPendingLimits(t *testing.T) {
	e := newTestEnv(t)
	e.openDiscovery(e.admin())

	connect := func(ip string) (*fakeAgent, error) {
		_, priv := newFakeIdentity(t)
		a, _, err := e.dialAgent(priv, testMeta("h"), xff(ip))
		if err != nil {
			return nil, err
		}
		if err := a.handshake(testMeta("h")); err != nil {
			a.close()
			return nil, err
		}
		a.start()
		t.Cleanup(a.close)
		return a, nil
	}

	// 单 IP 上限
	for i := 0; i < MaxPendingPerIP; i++ {
		if _, err := connect("203.0.113.9"); err != nil {
			t.Fatalf("第 %d 个待启用应被接受: %v", i+1, err)
		}
	}
	if _, err := connect("203.0.113.9"); websocket.CloseStatus(err) != termproto.CloseRateLimited {
		t.Fatalf("同一来源超过 %d 个待启用应以 4429 拒绝: %v", MaxPendingPerIP, err)
	}

	// 全局上限：用不同来源补满到 MaxPending
	for i := MaxPendingPerIP; i < termproto.MaxPending; i++ {
		ip := "203.0.113." + itoaTest(100+i)
		if _, err := connect(ip); err != nil {
			t.Fatalf("第 %d 个待启用应被接受: %v", i+1, err)
		}
	}
	if _, err := connect("192.0.2.77"); websocket.CloseStatus(err) != termproto.CloseRateLimited {
		t.Fatalf("列表已满时应以 4429 拒绝新来者: %v", err)
	}
	if n := len(e.hub.pending.snapshot()); n != termproto.MaxPending {
		t.Fatalf("待启用条目数应为上限 %d，实际 %d", termproto.MaxPending, n)
	}
}

func itoaTest(n int) string {
	b, _ := json.Marshal(n)
	return string(b)
}

func TestDiscoveryWindowExpiryDropsPending(t *testing.T) {
	e := newTestEnv(t, withFakeClock())
	hdr := e.admin()
	if res := e.call("POST", "/api/v1/terminal/discovery", map[string]int{"minutes": 1}, hdr...); res.Status != 200 {
		t.Fatalf("打开窗口失败: %d %s", res.Status, res.Raw)
	}
	_, priv := newFakeIdentity(t)
	a, _, _ := e.dialAgent(priv, testMeta("late"), nil)
	defer a.close()
	if err := a.handshake(testMeta("late")); err != nil || a.welcome.Status != termproto.WelcomePending {
		t.Fatalf("窗口内应进入待启用: %v", err)
	}
	a.start()

	e.clock.Advance(61 * time.Second)
	if code := a.waitClosed(4 * time.Second); code != termproto.CloseUnknownMachine {
		t.Fatalf("窗口到期应以 4405 断开待启用连接: %d", code)
	}
	if n := len(e.hub.pending.snapshot()); n != 0 {
		t.Fatalf("到期后待启用应清空: %d", n)
	}
	st := e.call("GET", "/api/v1/terminal/status", nil, "Authorization", humanAuth(testUser))
	if disc, _ := st.Body["discovery"].(map[string]any); disc["open"] != false {
		t.Fatalf("窗口应已关闭: %s", st.Raw)
	}
}

func TestDiscoveryAlwaysMode(t *testing.T) {
	e := newTestEnv(t, withDiscoveryAlways())
	_, priv := newFakeIdentity(t)
	a, _, _ := e.dialAgent(priv, testMeta("walkin"), nil)
	defer a.close()
	if err := a.handshake(testMeta("walkin")); err != nil || a.welcome.Status != termproto.WelcomePending {
		t.Fatalf("常开模式下无需手动开窗也应进入待启用: %v", err)
	}
	st := e.call("GET", "/api/v1/terminal/status", nil, "Authorization", humanAuth(testUser))
	disc, _ := st.Body["discovery"].(map[string]any)
	if disc["open"] != true || disc["always"] != true {
		t.Fatalf("状态应显示常开: %s", st.Raw)
	}
	// 常开模式下「关闭窗口」不应真的关闭（由运维通过环境变量决定）
	hdr := e.admin()
	_ = e.call("DELETE", "/api/v1/terminal/discovery", nil, hdr...)
	st = e.call("GET", "/api/v1/terminal/status", nil, "Authorization", humanAuth(testUser))
	if disc, _ := st.Body["discovery"].(map[string]any); disc["open"] != true {
		t.Fatalf("常开模式不应被界面关闭: %s", st.Raw)
	}
}

func TestSelfReportedFieldsAreSanitizedAndDuplicateNamesFlagged(t *testing.T) {
	e := newTestEnv(t)
	hdr := e.admin()
	e.openDiscovery(hdr)

	hostile := termproto.Meta{
		Host: "evil\x1b[31m\u202ehost" + strings.Repeat("x", 500),
		OS:   "linux\x00",
		Arch: "amd64",
		Ver:  "1.0",
		Priv: "superuser", // 非法枚举值
		Kind: "weird",
	}
	_, p1 := newFakeIdentity(t)
	a, _, _ := e.dialAgent(p1, hostile, xff("10.0.0.1"))
	defer a.close()
	if err := a.handshake(hostile); err != nil {
		t.Fatal(err)
	}
	a.start()
	_, p2 := newFakeIdentity(t)
	b, _, _ := e.dialAgent(p2, hostile, xff("10.0.0.2"))
	defer b.close()
	if err := b.handshake(hostile); err != nil {
		t.Fatal(err)
	}
	b.start()
	eventually(t, 2*time.Second, "两个待启用", func() bool { return len(e.hub.pending.snapshot()) == 2 })

	res := e.call("GET", "/api/v1/terminal/hosts", nil, "Authorization", humanAuth(testUser))
	pend, _ := res.Body["pending"].([]any)
	for _, p := range pend {
		m := p.(map[string]any)
		host, _ := m["hostname"].(string)
		if strings.ContainsAny(host, "\x1b\u202e\x00") {
			t.Fatalf("主机名必须去掉控制字符与双向控制符: %q", host)
		}
		if n := len([]rune(host)); n > termproto.MaxHostnameRunes {
			t.Fatalf("主机名应被限长到 %d: %d", termproto.MaxHostnameRunes, n)
		}
		if m["privilege"] != termproto.PrivUser || m["kind"] != termproto.KindPersistent {
			t.Fatalf("非法枚举值应回落到安全默认: %v", m)
		}
		if m["duplicate_name"] != true {
			t.Fatalf("同名主机应带重名标记: %v", m)
		}
	}
}

func TestConcurrentEnableAndIgnoreHaveExactlyOneWinner(t *testing.T) {
	for round := 0; round < 10; round++ {
		e := newTestEnv(t)
		hdr := e.admin()
		e.openDiscovery(hdr)
		_, priv := newFakeIdentity(t)
		a, _, _ := e.dialAgent(priv, testMeta("race"), nil)
		if err := a.handshake(testMeta("race")); err != nil {
			t.Fatal(err)
		}
		a.start()
		eventually(t, 2*time.Second, "待启用", func() bool { return len(e.hub.pending.snapshot()) == 1 })

		var wg sync.WaitGroup
		results := make([]int, 2)
		wg.Add(2)
		go func() {
			defer wg.Done()
			results[0] = e.call("POST", "/api/v1/terminal/hosts/"+a.code+"/enable", nil, hdr...).Status
		}()
		go func() {
			defer wg.Done()
			results[1] = e.call("POST", "/api/v1/terminal/hosts/"+a.code+"/ignore", nil, hdr...).Status
		}()
		wg.Wait()

		ok, notFound := 0, 0
		for _, s := range results {
			switch s {
			case 200:
				ok++
			case 404:
				notFound++
			default:
				t.Fatalf("第 %d 轮：出现意外状态 %v", round, results)
			}
		}
		if ok != 1 || notFound != 1 {
			t.Fatalf("第 %d 轮：启用与忽略必须恰有一个成功、一个 404，得到 %v", round, results)
		}
		// 数据库状态与胜者一致，且唯一
		recs, _ := e.store.ListTerminalHosts(t.Context())
		if len(recs) != 1 {
			t.Fatalf("应恰有一条记录: %d", len(recs))
		}
		a.close()
	}
}

func TestAgentDisconnectedBeforeApprovalIsStillEnableable(t *testing.T) {
	e := newTestEnv(t)
	hdr := e.admin()
	e.openDiscovery(hdr)
	_, priv := newFakeIdentity(t)
	a, _, _ := e.dialAgent(priv, testMeta("flaky"), nil)
	if err := a.handshake(testMeta("flaky")); err != nil {
		t.Fatal(err)
	}
	a.start()
	eventually(t, 2*time.Second, "待启用", func() bool { return len(e.hub.pending.snapshot()) == 1 })
	a.close()
	eventually(t, 2*time.Second, "条目标记为离线", func() bool {
		s := e.hub.pending.snapshot()
		return len(s) == 1 && !s[0].OfflineAt.IsZero()
	})

	if res := e.call("POST", "/api/v1/terminal/hosts/"+a.code+"/enable", map[string]string{"remark": "稍后上线"}, hdr...); res.Status != 200 {
		t.Fatalf("离线条目也应可启用: %d %s", res.Status, res.Raw)
	}
	// 之后 Agent 重连，直接按已启用上线
	b := e.connectEnabled(priv, testMeta("flaky"))
	eventually(t, 2*time.Second, "上线", func() bool { return e.hub.onlineAgent(b.code) != nil })
}

func TestSameIdentityReconnectSupersedesOldConnection(t *testing.T) {
	e := newTestEnv(t)
	a := e.enrollAgent(testMeta("dup"), "")
	b := e.connectEnabled(a.priv, testMeta("dup"))

	if code := a.waitClosed(3 * time.Second); code != termproto.CloseSuperseded {
		t.Fatalf("旧连接应以 4413 被取代: %d", code)
	}
	eventually(t, 2*time.Second, "新连接为当前在线连接", func() bool { return e.hub.onlineAgent(b.code) != nil })
	// 旧连接的清理不得把新连接的登记一并注销
	time.Sleep(100 * time.Millisecond)
	if e.hub.onlineAgent(b.code) == nil {
		t.Fatal("旧连接清理误删了新连接的登记")
	}
}

func TestTemporaryHostIsMemoryOnly(t *testing.T) {
	e := newTestEnv(t, withFakeClock())
	hdr := e.admin()
	e.openDiscovery(hdr)
	meta := testMeta("win-temp")
	meta.Kind = termproto.KindTemporary
	_, priv := newFakeIdentity(t)

	a, _, _ := e.dialAgent(priv, meta, nil)
	if err := a.handshake(meta); err != nil || a.welcome.Status != termproto.WelcomePending {
		t.Fatalf("临时 Agent 也应先进入待启用: %v", err)
	}
	a.start()
	eventually(t, 2*time.Second, "待启用", func() bool { return len(e.hub.pending.snapshot()) == 1 })
	if res := e.call("POST", "/api/v1/terminal/hosts/"+a.code+"/enable", map[string]string{"remark": "临时"}, hdr...); res.Status != 200 {
		t.Fatalf("启用失败: %d %s", res.Status, res.Raw)
	}
	select {
	case <-a.approved:
	case <-time.After(3 * time.Second):
		t.Fatal("临时 Agent 未收到 approved")
	}
	if recs, _ := e.store.ListTerminalHosts(t.Context()); len(recs) != 0 {
		t.Fatalf("临时启用不得写入数据库: %d", len(recs))
	}

	res := e.call("GET", "/api/v1/terminal/hosts", nil, "Authorization", humanAuth(testUser))
	hosts, _ := res.Body["hosts"].([]any)
	if len(hosts) != 1 || hosts[0].(map[string]any)["temporary"] != true {
		t.Fatalf("应显示为临时主机: %s", res.Raw)
	}

	// 短暂断线后在宽限期内重连：自动保持启用
	a.close()
	eventually(t, 2*time.Second, "离线", func() bool { return e.hub.onlineAgent(a.code) == nil })
	e.clock.Advance(3 * time.Minute)
	b := e.connectEnabled(priv, meta)
	_ = b

	// 禁用 = 移除
	if res := e.call("POST", "/api/v1/terminal/hosts/"+a.code+"/disable", nil, hdr...); res.Status != 200 {
		t.Fatalf("停用临时主机失败: %d %s", res.Status, res.Raw)
	}
	if code := b.waitClosed(3 * time.Second); code != termproto.CloseDisabled {
		t.Fatalf("停用应断开连接: %d", code)
	}
	res = e.call("GET", "/api/v1/terminal/hosts", nil, "Authorization", humanAuth(testUser))
	if hosts, _ := res.Body["hosts"].([]any); len(hosts) != 0 {
		t.Fatalf("停用后临时主机应消失: %s", res.Raw)
	}
}

func TestTemporaryHostExpires(t *testing.T) {
	e := newTestEnv(t, withFakeClock())
	hdr := e.admin()
	e.openDiscovery(hdr)
	meta := testMeta("win-temp2")
	meta.Kind = termproto.KindTemporary
	_, priv := newFakeIdentity(t)
	a, _, _ := e.dialAgent(priv, meta, nil)
	if err := a.handshake(meta); err != nil {
		t.Fatal(err)
	}
	a.start()
	eventually(t, 2*time.Second, "待启用", func() bool { return len(e.hub.pending.snapshot()) == 1 })
	_ = e.call("POST", "/api/v1/terminal/hosts/"+a.code+"/enable", nil, hdr...)
	select {
	case <-a.approved:
	case <-time.After(3 * time.Second):
		t.Fatal("未收到 approved")
	}
	eventually(t, 2*time.Second, "在线", func() bool { return e.hub.onlineAgent(a.code) != nil })

	// 超过 24 小时：即使一直在线也会被收回启用资格
	e.clock.Advance(TempHostMaxAge + time.Minute)
	if code := a.waitClosed(4 * time.Second); code != termproto.CloseDisabled {
		t.Fatalf("临时启用超过 24 小时应被断开: %d", code)
	}
	a.mu.Lock()
	lastPolicy := termproto.Policy{Terminal: true}
	if n := len(a.policies); n > 0 {
		lastPolicy = a.policies[n-1]
	}
	a.mu.Unlock()
	if lastPolicy.Terminal || lastPolicy.Reason != "temporary_expired" {
		t.Fatalf("断开前应通知 Agent 终止会话: %+v", lastPolicy)
	}
}
