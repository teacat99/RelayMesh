//go:build !windows

package terminal

// 本文件是 Hub 与「真实 Agent + 真实 PTY」的端到端集成测试：
// Hub 用 testenv 里的真实 HTTP 服务，Agent 用 internal/agent 的正式实现，shell 是本机的 /bin/sh。
// 它验证单元测试覆盖不到的跨组件性质：握手 → 发现 → 启用 → 开会话 → 附着 → 输入输出 → 断线重连 →
// 总闸翻转 → 流控，以及最重要的不变量——PTY 的寿命与网络连接无关。

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/teacat99/RelayMesh/v2/internal/agent"
	"github.com/teacat99/RelayMesh/v2/internal/agent/ptyhost"
	"github.com/teacat99/RelayMesh/v2/internal/termproto"
)

const liveWait = 10 * time.Second

// ---------- 真实 Agent 的测试外壳 ----------

type liveOpts struct {
	temp   bool
	trust  *agent.TrustOptions
	limits agent.Limits
	dir    string // 持久身份目录；空则用临时目录
	shell  agent.ShellFunc

	// 会话持有方式；零值为内嵌（与历史行为一致）。
	holder       agent.HolderMode
	holderSocket string
}

type liveAgent struct {
	t      *testing.T
	a      *agent.Agent
	code   string
	cancel context.CancelFunc
	done   chan struct{}

	mu     sync.Mutex
	events []agent.Event
}

func testShell(home string) agent.ShellFunc {
	return func(rows, cols uint16) (ptyhost.Spec, error) {
		return ptyhost.Spec{
			Path: "/bin/sh",
			Env:  []string{"PATH=/usr/local/bin:/usr/bin:/bin", "HOME=" + home, "TERM=xterm-256color", "PS1=$ ", "LANG=C.UTF-8"},
			Dir:  home,
			Rows: rows,
			Cols: cols,
		}, nil
	}
}

func startLiveAgent(t *testing.T, e *testEnv, o liveOpts) *liveAgent {
	t.Helper()
	target, err := agent.ParseServer(e.srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	var id *agent.Identity
	if o.temp {
		id, err = agent.NewTemporaryIdentity()
	} else {
		dir := o.dir
		if dir == "" {
			dir = t.TempDir()
		}
		id, err = agent.LoadOrCreateIdentity(dir)
	}
	if err != nil {
		t.Fatal(err)
	}
	trust := agent.TrustOptions{Insecure: true}
	if o.trust != nil {
		trust = *o.trust
	}
	shell := o.shell
	if shell == nil {
		shell = testShell(t.TempDir())
	}
	la := &liveAgent{t: t, code: id.MachineCode(), done: make(chan struct{})}
	a, err := agent.New(agent.Options{
		Server:        target,
		Trust:         trust,
		Identity:      id,
		Version:       "1.7.0-live-test",
		Limits:        o.limits,
		Shell:         shell,
		Logf:          t.Logf,
		OnEvent:       la.onEvent,
		Holder:        o.holder,
		HolderSocket:  o.holderSocket,
		MinBackoff:    20 * time.Millisecond,
		MaxBackoff:    150 * time.Millisecond,
		SlowRetry:     250 * time.Millisecond,
		DisabledRetry: 400 * time.Millisecond,
		Jitter:        func(d time.Duration) time.Duration { return d },
	})
	if err != nil {
		t.Fatal(err)
	}
	la.a = a
	ctx, cancel := context.WithCancel(context.Background())
	la.cancel = cancel
	go func() {
		defer close(la.done)
		_ = a.Run(ctx)
	}()
	t.Cleanup(la.stop)
	return la
}

func (la *liveAgent) onEvent(ev agent.Event) {
	la.mu.Lock()
	la.events = append(la.events, ev)
	la.mu.Unlock()
}

func (la *liveAgent) stop() {
	la.cancel()
	select {
	case <-la.done:
	case <-time.After(10 * time.Second):
		la.t.Error("Agent 未在 10 秒内退出")
	}
}

// mark 返回当前事件数，配合 waitEvent 只观察之后发生的事件。
func (la *liveAgent) mark() int {
	la.mu.Lock()
	defer la.mu.Unlock()
	return len(la.events)
}

func (la *liveAgent) snapshot(from int) []agent.Event {
	la.mu.Lock()
	defer la.mu.Unlock()
	if from > len(la.events) {
		from = len(la.events)
	}
	return append([]agent.Event(nil), la.events[from:]...)
}

func (la *liveAgent) waitEvent(from int, what string, match func(agent.Event) bool) agent.Event {
	la.t.Helper()
	var found agent.Event
	deadline := time.Now().Add(liveWait)
	for time.Now().Before(deadline) {
		for _, ev := range la.snapshot(from) {
			if match(ev) {
				found = ev
				return found
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	la.t.Fatalf("等待 Agent 事件超时：%s；最近的事件: %s", what, briefEvents(la.snapshot(from), 4))
	return found
}

func (la *liveAgent) waitState(from int, st agent.State) agent.Event {
	la.t.Helper()
	return la.waitEvent(from, string(st), func(ev agent.Event) bool { return ev.State == st })
}

func (la *liveAgent) waitRetryCode(from int, code int) agent.Event {
	la.t.Helper()
	return la.waitEvent(from, fmt.Sprintf("retrying/%d", code), func(ev agent.Event) bool {
		return ev.State == agent.StateRetrying && ev.Code == code
	})
}

// briefEvents 把最近几个事件压缩成一行，避免失败日志被重复的重试事件淹没。
func briefEvents(evs []agent.Event, last int) string {
	if len(evs) > last {
		evs = evs[len(evs)-last:]
	}
	parts := make([]string, 0, len(evs))
	for _, ev := range evs {
		d := ev.Detail
		if r := []rune(d); len(r) > 160 {
			d = string(r[:160]) + "…"
		}
		parts = append(parts, fmt.Sprintf("%s(code=%d %q)", ev.State, ev.Code, d))
	}
	return strings.Join(parts, " | ")
}

// enrollLive 走完整的启用流程：发现窗口 → 待启用 → 管理员启用 → Agent 上线。
func (e *testEnv) enrollLive(la *liveAgent, hdr []string, remark string) {
	e.t.Helper()
	la.waitState(0, agent.StatePending)
	eventually(e.t, liveWait, "待启用列表出现该主机", func() bool {
		res := e.call("GET", "/api/v1/terminal/hosts", nil, "Authorization", humanAuth(testUser))
		pend, _ := res.Body["pending"].([]any)
		for _, p := range pend {
			if m, ok := p.(map[string]any); ok && m["machine_code"] == la.code {
				return true
			}
		}
		return false
	})
	mark := la.mark()
	res := e.call("POST", "/api/v1/terminal/hosts/"+la.code+"/enable", map[string]string{"remark": remark}, hdr...)
	if res.Status != 200 {
		e.t.Fatalf("启用失败: %d %s", res.Status, res.Raw)
	}
	la.waitState(mark, agent.StateOnline)
	eventually(e.t, liveWait, "Hub 登记为在线", func() bool { return e.hub.onlineAgent(la.code) != nil })
}

// liveSetup 是最常见的开局：开发现窗口 → 启动持久身份的真实 Agent → 启用。
func liveSetup(t *testing.T, o liveOpts, envOpts ...envOption) (*testEnv, *liveAgent, []string) {
	t.Helper()
	e := newTestEnv(t, envOpts...)
	hdr := e.admin()
	e.openDiscovery(hdr)
	la := startLiveAgent(t, e, o)
	e.enrollLive(la, hdr, "live-box")
	return e, la, hdr
}

func (e *testEnv) openLiveSession(code string, hdr []string, name string) string {
	e.t.Helper()
	res := e.call("POST", "/api/v1/terminal/hosts/"+code+"/sessions", map[string]any{"name": name, "rows": 30, "cols": 100}, hdr...)
	if res.Status != 200 {
		e.t.Fatalf("新建会话失败: %d %s", res.Status, res.Raw)
	}
	sess, _ := res.Body["session"].(map[string]any)
	sid, _ := sess["sid"].(string)
	if !termproto.ValidSessionID(sid) {
		e.t.Fatalf("返回的 sid 非法: %v", sess)
	}
	return sid
}

// ---------- 浏览器侧客户端 ----------

type termClient struct {
	t       *testing.T
	ws      *websocket.Conn
	ready   termproto.BrowserReady
	autoAck bool
	cancel  context.CancelFunc
	done    chan struct{}

	mu        sync.Mutex
	out       []byte
	first     uint64
	haveFirst bool
	end       uint64
	exit      *termproto.BrowserExit
	closedMsg string
	code      websocket.StatusCode
}

func (e *testEnv) attachLive(code, sid string, hdr []string, since uint64, autoAck bool) *termClient {
	e.t.Helper()
	res := e.call("POST", "/api/v1/terminal/hosts/"+code+"/sessions/"+sid+"/ticket", map[string]any{"since": since, "rows": 40, "cols": 120}, hdr...)
	if res.Status != 200 || res.str("ticket") == "" {
		e.t.Fatalf("签发票据失败: %d %s", res.Status, res.Raw)
	}
	ws, resp, err := e.dialBrowser(res.str("ticket"), nil)
	if err != nil {
		status := 0
		if resp != nil {
			status = resp.StatusCode
		}
		e.t.Fatalf("拨号终端 WebSocket 失败: %v (http %d)", err, status)
	}
	c := &termClient{t: e.t, ws: ws, autoAck: autoAck, done: make(chan struct{}), code: -1}
	c.ready = readReady(e.t, ws)
	ctx, cancel := context.WithCancel(context.Background())
	c.cancel = cancel
	go c.loop(ctx)
	e.t.Cleanup(c.close)
	return c
}

func (c *termClient) loop(ctx context.Context) {
	defer close(c.done)
	for {
		typ, msg, err := c.ws.Read(ctx)
		if err != nil {
			c.mu.Lock()
			c.code = websocket.CloseStatus(err)
			c.mu.Unlock()
			return
		}
		switch typ {
		case websocket.MessageBinary:
			off, payload, err := termproto.DecodeBrowserOutput(msg)
			if err != nil {
				c.t.Errorf("解码输出帧失败: %v", err)
				return
			}
			c.mu.Lock()
			if !c.haveFirst {
				c.first, c.haveFirst = off, true
			}
			c.out = append(c.out, payload...)
			c.end = off + uint64(len(payload))
			end := c.end
			c.mu.Unlock()
			if c.autoAck {
				c.ack(end)
			}
		case websocket.MessageText:
			var head struct {
				T string `json:"t"`
			}
			_ = json.Unmarshal(msg, &head)
			switch head.T {
			case termproto.TExit:
				var ex termproto.BrowserExit
				_ = json.Unmarshal(msg, &ex)
				c.mu.Lock()
				c.exit = &ex
				c.mu.Unlock()
			case "closed":
				var cl termproto.BrowserClosed
				_ = json.Unmarshal(msg, &cl)
				c.mu.Lock()
				c.closedMsg = cl.Reason
				c.mu.Unlock()
			}
		}
	}
}

func (c *termClient) write(typ websocket.MessageType, b []byte) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = c.ws.Write(ctx, typ, b)
}

func (c *termClient) ack(off uint64) {
	b, _ := json.Marshal(termproto.BrowserAck{T: termproto.TAck, Offset: off})
	c.write(websocket.MessageText, b)
}

func (c *termClient) send(s string) {
	c.write(websocket.MessageBinary, termproto.EncodeBrowserInput([]byte(s)))
}

func (c *termClient) resize(rows, cols uint16) {
	b, _ := json.Marshal(termproto.BrowserResize{T: termproto.TResize, Rows: rows, Cols: cols})
	c.write(websocket.MessageText, b)
}

func (c *termClient) text() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return string(c.out)
}

func (c *termClient) received() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.out)
}

func (c *termClient) endOffset() uint64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.end
}

func (c *termClient) firstOffset() uint64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.first
}

func (c *termClient) waitFor(substr string) {
	c.t.Helper()
	deadline := time.Now().Add(liveWait)
	for time.Now().Before(deadline) {
		if strings.Contains(c.text(), substr) {
			return
		}
		select {
		case <-c.done:
			if strings.Contains(c.text(), substr) {
				return
			}
			c.t.Fatalf("连接已关闭（%d），仍未等到 %q；已收输出: %q", c.closeCode(), substr, tail(c.text(), 400))
		case <-time.After(10 * time.Millisecond):
		}
	}
	c.t.Fatalf("等待输出 %q 超时；已收输出: %q", substr, tail(c.text(), 400))
}

func (c *termClient) closeCode() websocket.StatusCode {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.code
}

func (c *termClient) waitClosed() websocket.StatusCode {
	c.t.Helper()
	select {
	case <-c.done:
		return c.closeCode()
	case <-time.After(liveWait):
		c.t.Fatal("等待浏览器连接被关闭超时")
		return 0
	}
}

func (c *termClient) close() {
	c.cancel()
	_ = c.ws.CloseNow()
	<-c.done
}

func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return "…" + s[len(s)-n:]
}

// ---------- 测试 ----------

func TestLiveAgent_EnrollAndInteract(t *testing.T) {
	e, la, hdr := liveSetup(t, liveOpts{})

	// Hub 侧视图来自真实 Agent 的握手元信息。
	res := e.call("GET", "/api/v1/terminal/hosts", nil, "Authorization", humanAuth(testUser))
	hosts, _ := res.Body["hosts"].([]any)
	if len(hosts) != 1 {
		t.Fatalf("期望 1 台主机，得到 %s", res.Raw)
	}
	h := hosts[0].(map[string]any)
	if h["machine_code"] != la.code || h["online"] != true || h["remark"] != "live-box" {
		t.Fatalf("主机视图不对: %v", h)
	}
	if h["agent_version"] != "1.7.0-live-test" || h["os"] == "" || h["hostname"] == "" {
		t.Fatalf("握手元信息未贯通: %v", h)
	}

	sid := e.openLiveSession(la.code, hdr, "main")
	c := e.attachLive(la.code, sid, hdr, 0, true)
	if c.ready.State != "running" {
		t.Fatalf("ready.state = %q", c.ready.State)
	}

	c.send("stty size\r")
	c.waitFor("40 120") // 票据里的尺寸被应用到了真实 PTY

	c.send("echo 终端-$((6*7))\r")
	c.waitFor("终端-42") // UTF-8 往返

	c.resize(33, 101)
	c.send("stty size\r")
	c.waitFor("33 101") // 浏览器上报的尺寸变化生效
}

func TestLiveAgent_DetachedOutputReplayedFromAck(t *testing.T) {
	e, la, hdr := liveSetup(t, liveOpts{})
	sid := e.openLiveSession(la.code, hdr, "replay")

	c1 := e.attachLive(la.code, sid, hdr, 0, true)
	c1.send("echo FIRST-$((40+2))\r")
	c1.waitFor("FIRST-42")
	ackOff := c1.endOffset()

	// 启动一个 1 秒后才输出的后台任务，然后断开浏览器——输出发生时没有任何人附着。
	c1.send("(sleep 1; echo LATE-$((20+22))) &\r")
	c1.waitFor("$((20+22))) &")
	c1.close()

	time.Sleep(1500 * time.Millisecond)

	c2 := e.attachLive(la.code, sid, hdr, ackOff, true)
	if c2.ready.Truncated {
		t.Fatal("环形缓冲足够大，不应标记 truncated")
	}
	if c2.ready.From != ackOff {
		t.Fatalf("应从 ack 偏移续传: from=%d want=%d", c2.ready.From, ackOff)
	}
	if c2.ready.Prefix != "" {
		t.Fatalf("续传（since 在缓冲内）不应携带模式前缀")
	}
	c2.waitFor("LATE-42")
	if got := c2.firstOffset(); got != ackOff {
		t.Fatalf("续传的首个输出偏移应为 %d，得到 %d（中间丢失或重复）", ackOff, got)
	}
	if strings.Contains(c2.text(), "FIRST-42") {
		t.Fatalf("续传不应重复 ack 之前的内容: %q", c2.text())
	}
}

func TestLiveAgent_LinkLossKeepsSessionsAlive(t *testing.T) {
	e, la, hdr := liveSetup(t, liveOpts{})
	sid := e.openLiveSession(la.code, hdr, "survivor")

	c := e.attachLive(la.code, sid, hdr, 0, true)
	c.send("echo BEFORE-$((1+1))\r")
	c.waitFor("BEFORE-2")

	// 模拟网络突然中断：Hub 侧直接掐断 TCP，不发 Close 帧。
	mark := la.mark()
	old := e.hub.onlineAgent(la.code)
	if old == nil {
		t.Fatal("Agent 应当在线")
	}
	_ = old.ws.CloseNow()

	la.waitState(mark, agent.StateRetrying)
	la.waitState(mark, agent.StateOnline)
	eventually(t, liveWait, "Hub 重新登记 Agent", func() bool {
		a := e.hub.onlineAgent(la.code)
		return a != nil && a != old
	})

	// INV-P1：会话随 Agent 进程存活，重连后仍在列表里，且输出 / 输入都正常。
	if n := la.a.Manager().Count(); n != 1 {
		t.Fatalf("断线期间会话不应被销毁，得到 %d 个会话", n)
	}
	eventually(t, liveWait, "重连后 Hub 重新获得会话列表", func() bool {
		a := e.hub.onlineAgent(la.code)
		return a != nil && len(a.sessionList()) == 1
	})

	c2 := e.attachLive(la.code, sid, hdr, 0, true)
	c2.waitFor("BEFORE-2") // 断线前的输出被回放
	c2.send("echo AFTER-$((2+3))\r")
	c2.waitFor("AFTER-5")
}

func TestLiveAgent_KillSessionAndExit(t *testing.T) {
	e, la, hdr := liveSetup(t, liveOpts{})

	// 1. shell 自己退出：浏览器收到 exit 与 4410，会话保留为 exited（可回看）。
	sid := e.openLiveSession(la.code, hdr, "exiter")
	c := e.attachLive(la.code, sid, hdr, 0, true)
	c.send("echo BYE-$((3+4))\r")
	c.waitFor("BYE-7")
	c.send("exit 3\r")
	if code := c.waitClosed(); code != termproto.CloseSessionEnded {
		t.Fatalf("会话结束应以 %d 关闭浏览器连接，得到 %d", termproto.CloseSessionEnded, code)
	}
	c.mu.Lock()
	ex := c.exit
	c.mu.Unlock()
	if ex == nil || ex.Code == nil || *ex.Code != 3 {
		t.Fatalf("应收到退出码 3，得到 %+v", ex)
	}

	eventually(t, liveWait, "Hub 会话列表标记 exited", func() bool {
		a := e.hub.onlineAgent(la.code)
		if a == nil {
			return false
		}
		for _, s := range a.sessionList() {
			if s.SID == sid && s.State == "exited" {
				return true
			}
		}
		return false
	})
	c2 := e.attachLive(la.code, sid, hdr, 0, true)
	if c2.ready.State != "exited" || c2.ready.ExitCode == nil || *c2.ready.ExitCode != 3 {
		t.Fatalf("重新附着到已退出会话应得到 exited/3: %+v", c2.ready)
	}
	c2.waitFor("BYE-7") // 可以回看历史

	// 2. 显式 kill 一个运行中的会话：进程被终止，会话从两侧消失。
	sid2 := e.openLiveSession(la.code, hdr, "victim")
	c3 := e.attachLive(la.code, sid2, hdr, 0, true)
	c3.send("echo ALIVE-$((5+5))\r")
	c3.waitFor("ALIVE-10")
	if r := e.call("POST", "/api/v1/terminal/hosts/"+la.code+"/sessions/"+sid2+"/kill", nil, hdr...); r.Status != 200 && r.Status != 202 {
		t.Fatalf("kill 失败: %d %s", r.Status, r.Raw)
	}
	c3.waitClosed()
	eventually(t, liveWait, "被 kill 的会话从 Agent 移除", func() bool { return la.a.Manager().Get(sid2) == nil })

	// 3. 对已退出的会话 kill 会立即清除它。
	if r := e.call("POST", "/api/v1/terminal/hosts/"+la.code+"/sessions/"+sid+"/kill", nil, hdr...); r.Status != 200 && r.Status != 202 {
		t.Fatalf("kill 已退出会话失败: %d %s", r.Status, r.Raw)
	}
	eventually(t, liveWait, "已退出会话被清除", func() bool { return la.a.Manager().Count() == 0 })
}

func TestLiveAgent_GateFlipKillsSessionsAndRecovers(t *testing.T) {
	e, la, hdr := liveSetup(t, liveOpts{})
	sid := e.openLiveSession(la.code, hdr, "doomed")
	c := e.attachLive(la.code, sid, hdr, 0, true)
	c.send("echo PRE-$((9*9))\r")
	c.waitFor("PRE-81")

	// 管理员清除了 Web 密码：总闸立即关闭，Agent 上的全部会话必须被销毁。
	mark := la.mark()
	e.creds.set(testUser, "", nil)
	eventually(t, liveWait, "总闸关闭后 Agent 会话被全部销毁", func() bool { return la.a.Manager().Count() == 0 })
	c.waitClosed()
	la.waitRetryCode(mark, termproto.CloseGateClosed) // 之后拨号被 403 拒绝（总闸关闭）

	// 重新设置密码：总闸重开，Agent 自动恢复上线，并可再次开会话。
	mark = la.mark()
	e.creds.set(testUser, testPassword, nil)
	la.waitState(mark, agent.StateOnline)
	hdr2 := e.admin()
	sid2 := e.openLiveSession(la.code, hdr2, "reborn")
	c2 := e.attachLive(la.code, sid2, hdr2, 0, true)
	c2.send("echo POST-$((8+8))\r")
	c2.waitFor("POST-16")
}

func TestLiveAgent_DisableThenReenable(t *testing.T) {
	e, la, hdr := liveSetup(t, liveOpts{})
	sid := e.openLiveSession(la.code, hdr, "to-be-killed-by-disable")
	c := e.attachLive(la.code, sid, hdr, 0, true)
	c.send("echo ok-$((1+0))\r")
	c.waitFor("ok-1")

	mark := la.mark()
	if r := e.call("POST", "/api/v1/terminal/hosts/"+la.code+"/disable", nil, hdr...); r.Status != 200 {
		t.Fatalf("停用失败: %d %s", r.Status, r.Raw)
	}
	la.waitRetryCode(mark, termproto.CloseDisabled)
	eventually(t, liveWait, "停用后 Agent 会话被销毁", func() bool { return la.a.Manager().Count() == 0 })

	// 被停用的主机不会疯狂重连（慢节奏重试），重新启用后自动恢复。
	if r := e.call("POST", "/api/v1/terminal/hosts/"+la.code+"/enable", map[string]string{"remark": "again"}, hdr...); r.Status != 200 {
		t.Fatalf("重新启用失败: %d %s", r.Status, r.Raw)
	}
	la.waitState(mark, agent.StateOnline)
	eventually(t, liveWait, "Hub 重新登记为在线", func() bool { return e.hub.onlineAgent(la.code) != nil })
}

func TestLiveAgent_UnknownMachineRetriesUntilDiscoveryOpens(t *testing.T) {
	e := newTestEnv(t)
	hdr := e.admin()

	la := startLiveAgent(t, e, liveOpts{})
	ev := la.waitRetryCode(0, termproto.CloseUnknownMachine) // 发现窗口关闭：陌生机器被拒
	if ev.Retry <= 0 {
		t.Fatalf("应给出重试等待时间: %+v", ev)
	}
	for _, s := range la.snapshot(0) {
		if s.State == agent.StatePending || s.State == agent.StateOnline {
			t.Fatalf("发现窗口关闭时不应进入 pending/online: %+v", s)
		}
	}

	mark := la.mark()
	e.openDiscovery(hdr)
	la.waitState(mark, agent.StatePending) // 窗口打开后，下一次重试就进入待启用
	e.enrollLive(la, hdr, "late-comer")
}

func TestLiveAgent_TemporaryIdentity(t *testing.T) {
	e := newTestEnv(t)
	hdr := e.admin()
	e.openDiscovery(hdr)

	la := startLiveAgent(t, e, liveOpts{temp: true})
	e.enrollLive(la, hdr, "temp-box")

	res := e.call("GET", "/api/v1/terminal/hosts", nil, "Authorization", humanAuth(testUser))
	hosts, _ := res.Body["hosts"].([]any)
	if len(hosts) != 1 || hosts[0].(map[string]any)["temporary"] != true {
		t.Fatalf("临时身份应标记 temporary: %s", res.Raw)
	}
	sid := e.openLiveSession(la.code, hdr, "t")
	c := e.attachLive(la.code, sid, hdr, 0, true)
	c.send("echo TEMP-$((6+6))\r")
	c.waitFor("TEMP-12")

	// 临时身份不落盘：重新运行就是另一台主机，需要重新启用。
	first := la.code
	la.stop()
	la2 := startLiveAgent(t, e, liveOpts{temp: true})
	if la2.code == first {
		t.Fatal("临时身份每次运行都应不同")
	}
	la2.waitState(0, agent.StatePending)
}

func TestLiveAgent_PersistentIdentitySurvivesRestart(t *testing.T) {
	e := newTestEnv(t)
	hdr := e.admin()
	e.openDiscovery(hdr)
	dir := t.TempDir()

	la := startLiveAgent(t, e, liveOpts{dir: dir})
	e.enrollLive(la, hdr, "stable")
	la.stop()
	eventually(t, liveWait, "Hub 标记离线", func() bool { return e.hub.onlineAgent(la.code) == nil })

	// 同一个身份目录 → 同一个机器码 → 无需再次启用，直接上线（即使发现窗口已关闭）。
	e.call("DELETE", "/api/v1/terminal/discovery", nil, hdr...)
	la2 := startLiveAgent(t, e, liveOpts{dir: dir})
	if la2.code != la.code {
		t.Fatalf("持久身份应保持机器码: %s != %s", la2.code, la.code)
	}
	la2.waitState(0, agent.StateOnline)
}

func TestLiveAgent_AltScreenReattachRestoresModes(t *testing.T) {
	e, la, hdr := liveSetup(t, liveOpts{})
	sid := e.openLiveSession(la.code, hdr, "fullscreen")

	c1 := e.attachLive(la.code, sid, hdr, 0, true)
	// 前台子进程：收到 SIGWINCH 打印标记；进入备用屏幕并开启鼠标上报 / 括号粘贴后保持运行（像 vim）。
	// 标记用算术表达式计算，以便与 tty 对键入内容的回显区分开。
	// 保持运行用「有界」循环（至多 60 秒）：测试结束后这个前台子进程不一定随会话一起被收掉，
	// 写成无限循环的话每跑一次测试就在机器上留下一个永不退出的孤儿 sh（开发机上累计过几十个）。
	c1.send("sh -c 'trap \"echo WINCH-$((1+1))\" WINCH; " +
		"printf \"\\033[?1049h\\033[?1000h\\033[?2004h\\033[HALTBODY-$((3+4))\"; " +
		"i=0; while [ $i -lt 600 ]; do sleep 0.1; i=$((i+1)); done'\r")
	c1.waitFor("ALTBODY-7")
	c1.close()

	// 全新附着（since=0）到一个处于备用屏幕的会话：必须先恢复模式，再触发一次重绘。
	c2 := e.attachLive(la.code, sid, hdr, 0, true)
	if c2.ready.Prefix == "" {
		t.Fatal("全新附着到备用屏幕会话应带模式前缀")
	}
	raw, err := base64.StdEncoding.DecodeString(c2.ready.Prefix)
	if err != nil {
		t.Fatalf("前缀不是合法 base64: %v", err)
	}
	for _, want := range []string{"\x1b[?1049h", "\x1b[?1000h", "\x1b[?2004h"} {
		if !bytes.Contains(raw, []byte(want)) {
			t.Fatalf("模式前缀缺少 %q: %q", want, raw)
		}
	}
	c2.waitFor("ALTBODY-7") // 备用屏幕的内容被回放
	c2.waitFor("WINCH-2")   // 尺寸抖动把 SIGWINCH 送达了前台应用，让它自己重绘
}

func TestLiveAgent_LargeOutputThroughHub(t *testing.T) {
	e, la, hdr := liveSetup(t, liveOpts{})
	sid := e.openLiveSession(la.code, hdr, "bulk")
	c := e.attachLive(la.code, sid, hdr, 0, true)

	const n = 6_000_000
	c.send("head -c 6000000 /dev/zero | tr '\\0' 'y'; echo DONE-$((7*7))\r")
	c.waitFor("DONE-49")
	got := strings.Count(c.text(), "y")
	// 输入回显里还有几个 'y'；只允许极少量偏差，任何丢包都会使数量明显不足。
	if got < n || got > n+64 {
		t.Fatalf("输出字节数异常: 期望约 %d，得到 %d", n, got)
	}
}

func TestLiveAgent_FlowControlEndToEnd(t *testing.T) {
	e, la, hdr := liveSetup(t, liveOpts{})
	sid := e.openLiveSession(la.code, hdr, "paced")
	c := e.attachLive(la.code, sid, hdr, 0, false) // 不自动 ack

	c.send("head -c 3000000 /dev/zero | tr '\\0' 'z'; echo FIN-$((6*6))\r")
	time.Sleep(1500 * time.Millisecond)

	// 浏览器迟迟不 ack：Agent 只应在窗口（256 KiB）内继续发送，随后暂停读取 PTY。
	got := c.received()
	if got == 0 {
		t.Fatal("应先收到窗口内的输出")
	}
	if limit := termproto.AttachWindow + 3*termproto.MaxDataPayload; got > limit {
		t.Fatalf("流控失效：未 ack 时收到 %d 字节，超过窗口 %d（含少量在途帧）", got, limit)
	}
	stalled := got

	// 开始 ack：输出恢复流动并最终完整送达。
	c.ack(c.endOffset())
	deadline := time.Now().Add(liveWait)
	for time.Now().Before(deadline) && !strings.Contains(c.text(), "FIN-36") {
		c.ack(c.endOffset())
		time.Sleep(5 * time.Millisecond)
	}
	if !strings.Contains(c.text(), "FIN-36") {
		t.Fatalf("恢复 ack 后输出应继续并完成（已收 %d，停滞时 %d）", c.received(), stalled)
	}
	if n := strings.Count(c.text(), "z"); n < 3_000_000 {
		t.Fatalf("输出丢失: %d < 3000000", n)
	}
}

func TestLiveAgent_SessionLimitEnforcedByAgent(t *testing.T) {
	lim := agent.DefaultLimits()
	lim.MaxSessions = 2
	e, la, hdr := liveSetup(t, liveOpts{limits: lim})

	e.openLiveSession(la.code, hdr, "s1")
	e.openLiveSession(la.code, hdr, "s2")
	res := e.call("POST", "/api/v1/terminal/hosts/"+la.code+"/sessions", map[string]any{"name": "s3", "rows": 30, "cols": 100}, hdr...)
	if res.Status == 200 {
		t.Fatalf("超过 Agent 限额的会话不应创建成功: %s", res.Raw)
	}
	if n := la.a.Manager().Count(); n != 2 {
		t.Fatalf("Agent 上应只有 2 个会话，得到 %d", n)
	}
}

func TestLiveAgent_ServerRestartReconnects(t *testing.T) {
	e, la, hdr := liveSetup(t, liveOpts{})
	sid := e.openLiveSession(la.code, hdr, "across-restart")
	c := e.attachLive(la.code, sid, hdr, 0, true)
	c.send("echo KEEP-$((4*4))\r")
	c.waitFor("KEEP-16")

	// Hub 优雅关闭（例如服务端升级重启）：Agent 进入重试，但会话不受影响。
	mark := la.mark()
	old := e.hub.onlineAgent(la.code)
	old.closeWith(websocket.StatusGoingAway, "server restarting")
	la.waitState(mark, agent.StateRetrying)
	la.waitState(mark, agent.StateOnline)
	if la.a.Manager().Count() != 1 {
		t.Fatal("服务端重启期间会话应保持")
	}
}

func TestLiveAgent_TLSTrustModes(t *testing.T) {
	e := newTestEnv(t, withTLS())
	hdr := e.admin()
	e.openDiscovery(hdr)
	pin := agent.Fingerprint(e.srv.Certificate().Raw)
	host := strings.TrimPrefix(e.srv.URL, "https://")

	neverPending := func(t *testing.T, la *liveAgent) {
		t.Helper()
		time.Sleep(400 * time.Millisecond) // 足够发生多次重试
		for _, ev := range la.snapshot(0) {
			if ev.State == agent.StatePending || ev.State == agent.StateOnline {
				t.Fatalf("信任校验失败后绝不能进入 pending/online: %s", briefEvents(la.snapshot(0), 4))
			}
		}
		if e.hub.onlineAgent(la.code) != nil {
			t.Fatal("Hub 上不应出现该 Agent")
		}
	}

	t.Run("正确的指纹可以连接并走完启用流程", func(t *testing.T) {
		e := e.sub(t)
		la := startLiveAgent(t, e, liveOpts{trust: &agent.TrustOptions{Pins: []string{pin}}})
		e.enrollLive(la, hdr, "pinned")
		sid := e.openLiveSession(la.code, hdr, "tls")
		c := e.attachLive(la.code, sid, hdr, 0, true)
		c.send("echo TLS-$((3*3))\r")
		c.waitFor("TLS-9")
	})

	t.Run("错误的指纹被拒绝", func(t *testing.T) {
		la := startLiveAgent(t, e, liveOpts{trust: &agent.TrustOptions{Pins: []string{"sha256:" + strings.Repeat("ab", 32)}}})
		ev := la.waitEvent(0, "指纹不匹配", func(ev agent.Event) bool {
			return ev.State == agent.StateRetrying && strings.Contains(ev.Detail, "不匹配")
		})
		if ev.Retry <= 0 {
			t.Fatalf("应给出重试等待: %+v", ev)
		}
		neverPending(t, la)
	})

	t.Run("自签名证书且没有任何信任配置时被拒绝", func(t *testing.T) {
		la := startLiveAgent(t, e, liveOpts{trust: &agent.TrustOptions{}})
		la.waitEvent(0, "证书校验失败", func(ev agent.Event) bool {
			return ev.State == agent.StateRetrying && strings.Contains(strings.ToLower(ev.Detail), "certificate")
		})
		neverPending(t, la)
	})

	t.Run("TOFU：确认一次后记住，指纹变化后拒绝", func(t *testing.T) {
		known := filepath.Join(t.TempDir(), "known_servers")
		prompted := 0
		first := startLiveAgent(t, e, liveOpts{trust: &agent.TrustOptions{
			KnownServersPath: known,
			Prompt:           func(h, fp string) bool { prompted++; return h == host && fp == pin },
		}})
		first.waitState(0, agent.StatePending)
		if prompted != 1 {
			t.Fatalf("首次应询问一次，得到 %d", prompted)
		}

		// 第二个 Agent 沿用同一份记录：不再询问即可连接。
		second := startLiveAgent(t, e, liveOpts{trust: &agent.TrustOptions{
			KnownServersPath: known,
			Prompt:           func(string, string) bool { t.Error("已记录的服务器不应再次询问"); return false },
		}})
		second.waitState(0, agent.StatePending)

		// 记录被改成另一个指纹（模拟服务端证书被中间人替换）：拒绝，且不询问。
		if err := os.WriteFile(known, []byte(host+" sha256:"+strings.Repeat("cd", 32)+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		third := startLiveAgent(t, e, liveOpts{trust: &agent.TrustOptions{
			KnownServersPath: known,
			Prompt:           func(string, string) bool { t.Error("指纹不一致时不应询问"); return true },
		}})
		third.waitEvent(0, "指纹不一致", func(ev agent.Event) bool {
			return ev.State == agent.StateRetrying && strings.Contains(ev.Detail, "不一致")
		})
		neverPending(t, third)
	})
}
