//go:build !windows

package terminal

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/teacat99/RelayMesh/v2/internal/model"
	"github.com/teacat99/RelayMesh/v2/internal/sshexec/sshdtest"
	"github.com/teacat99/RelayMesh/v2/internal/termproto"
)

// Hub 本地会话（Hub 直连 SSH 主机的人工终端）的测试：真实的浏览器 WebSocket 协议 + 进程内 SSH 服务器。

// localBrowser 是对一个本地会话的浏览器附着：读取并确认输出，同时校验偏移始终连续。
//
// 读取放在独立协程里、通过通道交付：coder/websocket 的 Read 在传入的 context 到期时会关闭整条连接，
// 所以「等一会儿看有没有数据」不能靠给 Read 设超时来实现。
type localBrowser struct {
	t        *testing.T
	ws       *websocket.Conn
	Ready    termproto.BrowserReady
	buf      bytes.Buffer
	end      uint64
	texts    []string
	noAck    bool
	frames   chan wsFrame
	closeErr error // 读到的连接关闭错误；非 nil 表示连接已经结束
}

type wsFrame struct {
	typ  websocket.MessageType
	data []byte
	err  error
}

// startReader 在后台持续读取 ws，直到连接结束或 stop 被关闭。
func startReader(ws *websocket.Conn, stop <-chan struct{}) chan wsFrame {
	frames := make(chan wsFrame, 8192)
	go func() {
		for {
			typ, data, err := ws.Read(context.Background())
			select {
			case frames <- wsFrame{typ: typ, data: data, err: err}:
			case <-stop:
				return
			}
			if err != nil {
				return
			}
		}
	}()
	return frames
}

func lastChars(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return "…" + s[len(s)-n:]
}

// openLocal 通过 REST 在 SSH 主机上新建一个本地会话，返回完整响应。
func (f *sshFixture) openLocal(body map[string]any) apiResult {
	return f.env.call("POST", "/api/v1/terminal/hosts/"+f.code+"/sessions", body, f.adm...)
}

func (f *sshFixture) mustOpenLocal(t *testing.T, name string) string {
	t.Helper()
	res := f.openLocal(map[string]any{"name": name, "rows": 30, "cols": 100})
	if res.Status != 200 {
		t.Fatalf("新建本地会话失败: %d %s", res.Status, res.Raw)
	}
	sid, _ := mapOf(res.Body["session"])["sid"].(string)
	if !termproto.ValidSessionID(sid) {
		t.Fatalf("返回的 sid 非法: %s", res.Raw)
	}
	return sid
}

// attachLocal 签发票据并附着；since 为续传起点。
func (f *sshFixture) attachLocal(t *testing.T, sid string, since uint64) *localBrowser {
	t.Helper()
	res := f.env.call("POST", "/api/v1/terminal/hosts/"+f.code+"/sessions/"+sid+"/ticket",
		map[string]any{"since": since, "rows": 40, "cols": 120}, f.adm...)
	if res.Status != 200 || res.str("ticket") == "" {
		t.Fatalf("签发票据失败: %d %s", res.Status, res.Raw)
	}
	ws, _, err := f.env.dialBrowser(res.str("ticket"), nil)
	if err != nil {
		t.Fatalf("浏览器拨号失败: %v", err)
	}
	stop := make(chan struct{})
	t.Cleanup(func() {
		close(stop)
		_ = ws.CloseNow()
	})
	ready := readReady(t, ws)
	return &localBrowser{t: t, ws: ws, Ready: ready, end: ready.From, frames: startReader(ws, stop)}
}

// hostSessions 读取主机列表里这台 SSH 主机名下的会话清单。
func (f *sshFixture) hostSessions(t *testing.T) []map[string]any {
	t.Helper()
	list := f.env.call("GET", "/api/v1/terminal/hosts", nil, "Authorization", humanAuth(testUser))
	if list.Status != 200 {
		t.Fatalf("主机列表: %d %s", list.Status, list.Raw)
	}
	for _, h := range list.Body["hosts"].([]any) {
		hm := mapOf(h)
		if hm["machine_code"] != f.code {
			continue
		}
		var out []map[string]any
		for _, s := range hm["sessions"].([]any) {
			out = append(out, mapOf(s))
		}
		return out
	}
	t.Fatalf("主机列表里没有 %s: %s", f.code, list.Raw)
	return nil
}

func (b *localBrowser) text() string { return b.buf.String() }

// pumpUntil 持续读取（并确认）输出，直到 cond 成立；超时即失败。
func (b *localBrowser) pumpUntil(what string, timeout time.Duration, cond func() bool) {
	b.t.Helper()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	for !cond() {
		if b.closeErr != nil {
			b.t.Fatalf("连接已关闭（%s）: %v（已收 %d 字节，结尾 %q）", what, b.closeErr, b.buf.Len(), lastChars(b.text(), 160))
		}
		select {
		case fr := <-b.frames:
			b.consume(fr)
		case <-timer.C:
			b.t.Fatalf("等待超时：%s（已收 %d 字节，结尾 %q）", what, b.buf.Len(), lastChars(b.text(), 160))
		}
	}
}

func (b *localBrowser) consume(fr wsFrame) {
	b.t.Helper()
	if fr.err != nil {
		b.closeErr = fr.err
		return
	}
	switch fr.typ {
	case websocket.MessageBinary:
		off, payload, err := termproto.DecodeBrowserOutput(fr.data)
		if err != nil {
			b.t.Fatal(err)
		}
		if off != b.end {
			b.t.Fatalf("输出偏移不连续：期望 %d，得到 %d", b.end, off)
		}
		b.buf.Write(payload)
		b.end += uint64(len(payload))
		if !b.noAck {
			b.ack()
		}
	case websocket.MessageText:
		b.texts = append(b.texts, string(fr.data))
	}
}

// ack 确认已收到的全部输出；对端已关闭时静默忽略（关闭握手期间的确认没有意义）。
func (b *localBrowser) ack() {
	raw, _ := json.Marshal(termproto.BrowserAck{T: termproto.TAck, Offset: b.end})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = b.ws.Write(ctx, websocket.MessageText, raw)
}

func (b *localBrowser) waitFor(sub string) {
	b.t.Helper()
	b.pumpUntil(fmt.Sprintf("输出里出现 %q", sub), 8*time.Second, func() bool { return strings.Contains(b.text(), sub) })
}

// drainFor 在 d 内尽量读取（不要求有数据到达）；用于观察「没有确认时发送会停下来」。
func (b *localBrowser) drainFor(d time.Duration) {
	b.t.Helper()
	timer := time.NewTimer(d)
	defer timer.Stop()
	for b.closeErr == nil {
		select {
		case fr := <-b.frames:
			b.consume(fr)
		case <-timer.C:
			return
		}
	}
}

// settle 持续读取（沿用当前的确认设置），直到 quiet 时长内没有新的帧到达，说明发送方已经停下；
// 超过 max 仍然没有安静下来就失败。比「睡固定时长再看」稳：慢机器上只是更晚才安静，不会误判。
func (b *localBrowser) settle(quiet, max time.Duration) {
	b.t.Helper()
	deadline := time.NewTimer(max)
	defer deadline.Stop()
	idle := time.NewTimer(quiet)
	defer idle.Stop()
	for b.closeErr == nil {
		select {
		case fr := <-b.frames:
			b.consume(fr)
			if !idle.Stop() {
				select {
				case <-idle.C:
				default:
				}
			}
			idle.Reset(quiet)
		case <-idle.C:
			return
		case <-deadline.C:
			b.t.Fatalf("输出在 %v 内一直没有安静下来（已收 %d 字节）", max, b.buf.Len())
		}
	}
}

func (b *localBrowser) send(s string) {
	b.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := b.ws.Write(ctx, websocket.MessageBinary, termproto.EncodeBrowserInput([]byte(s))); err != nil {
		b.t.Fatalf("发送输入失败: %v", err)
	}
}

// waitClose 读取直到连接被关闭，返回关闭码。
func (b *localBrowser) waitClose(timeout time.Duration) websocket.StatusCode {
	b.t.Helper()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	for b.closeErr == nil {
		select {
		case fr := <-b.frames:
			b.consume(fr)
		case <-timer.C:
			b.t.Fatalf("等待连接关闭超时（已收 %d 字节，结尾 %q）", b.buf.Len(), lastChars(b.text(), 160))
		}
	}
	return websocket.CloseStatus(b.closeErr)
}

func (b *localBrowser) sawText(sub string) bool {
	for _, s := range b.texts {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

// ---------- 基本流程 ----------

func TestLocalTerminalOpenAttachInputAndExit(t *testing.T) {
	f := newSSHFixture(t)

	res := f.openLocal(map[string]any{"name": "t1", "rows": 30, "cols": 100})
	if res.Status != 200 {
		t.Fatalf("新建本地会话失败: %d %s", res.Status, res.Raw)
	}
	sess := mapOf(res.Body["session"])
	sid, _ := sess["sid"].(string)
	if sess["state"] != termproto.SessionRunning || sess["name"] != "t1" || sess["rec"] != termproto.RecOff || sess["shell"] != "ssh" {
		t.Fatalf("会话信息不符: %+v", sess)
	}
	if p := f.srv.Pty(); p.Rows != 30 || p.Cols != 100 {
		t.Fatalf("远端伪终端应按新建时的尺寸申请: %+v", p)
	}

	b := f.attachLocal(t, sid, 0)
	if b.Ready.SID != sid || b.Ready.State != termproto.SessionRunning || b.Ready.Rows != 40 || b.Ready.Cols != 120 {
		t.Fatalf("ready 不符: %+v", b.Ready)
	}
	b.waitFor("welcome tester")
	eventually(t, 3*time.Second, "远端窗口尺寸跟随附着方", func() bool {
		p := f.srv.Pty()
		return p.Rows == 40 && p.Cols == 120
	})

	b.send("echo hello world\r")
	b.waitFor("hello world")

	// 会话挂在这台 SSH 主机名下，附着数可见
	eventually(t, 3*time.Second, "清单里显示附着数", func() bool {
		ss := f.hostSessions(t)
		return len(ss) == 1 && ss[0]["sid"] == sid && ss[0]["attached"] == float64(1)
	})

	b.send("exit 3\r")
	if code := b.waitClose(8 * time.Second); code != termproto.CloseSessionEnded {
		t.Fatalf("会话结束应以 %d 关闭，得到 %d", termproto.CloseSessionEnded, code)
	}
	if !b.sawText(`"t":"exit"`) || !b.sawText(`"code":3`) {
		t.Fatalf("应收到带退出码的 exit 通知: %v", b.texts)
	}
	eventually(t, 3*time.Second, "清单里显示已退出", func() bool {
		ss := f.hostSessions(t)
		return len(ss) == 1 && ss[0]["state"] == termproto.SessionExited && ss[0]["exit_code"] == float64(3) && ss[0]["attached"] == float64(0)
	})

	// 已退出的会话保留到被清除；清除即移出清单
	kill := f.env.call("POST", "/api/v1/terminal/hosts/"+f.code+"/sessions/"+sid+"/kill", nil, f.adm...)
	if kill.Status != 202 {
		t.Fatalf("清除已退出会话: %d %s", kill.Status, kill.Raw)
	}
	if ss := f.hostSessions(t); len(ss) != 0 {
		t.Fatalf("清除后清单应为空: %+v", ss)
	}
}

// ---------- 续传 ----------

func TestLocalTerminalResumeFromOffsetAndFullReplay(t *testing.T) {
	f := newSSHFixture(t)
	sid := f.mustOpenLocal(t, "resume")

	a := f.attachLocal(t, sid, 0)
	a.waitFor("$ ")
	aEnd := a.end
	_ = a.ws.Close(websocket.StatusNormalClosure, "")

	// 另一个附着在 A 断开期间产生输出
	b := f.attachLocal(t, sid, 0)
	if b.Ready.From != 0 || b.Ready.Truncated {
		t.Fatalf("新终端应从头回放: %+v", b.Ready)
	}
	b.waitFor("welcome tester")
	b.send("echo while-away\r")
	b.waitFor("while-away")

	// A 带着已收到的偏移回来：只补发断开期间的内容，不重复欢迎信息
	a2 := f.attachLocal(t, sid, aEnd)
	if a2.Ready.From != aEnd || a2.Ready.Truncated || a2.Ready.Prefix != "" {
		t.Fatalf("续传应从 %d 接着发，且不带模式恢复序列: %+v", aEnd, a2.Ready)
	}
	a2.waitFor("while-away")
	if strings.Contains(a2.text(), "welcome tester") {
		t.Fatalf("续传不应重复已收到的内容: %q", a2.text())
	}
}

// ---------- 备用屏 ----------

func TestLocalTerminalReattachRestoresAlternateScreen(t *testing.T) {
	f := newSSHFixture(t)
	sid := f.mustOpenLocal(t, "alt")

	a := f.attachLocal(t, sid, 0)
	a.waitFor("$ ")
	// 迷你 shell 会把输入回显、echo 也会再输出一次：进入备用屏的序列因此出现在输出流里
	a.send("echo \x1b[?1049h\x1b[?1000h\r")
	eventually(t, 5*time.Second, "输出里出现进入备用屏的序列", func() bool {
		a.drainFor(150 * time.Millisecond)
		return strings.Contains(a.text(), "\x1b[?1049h")
	})
	eventually(t, 3*time.Second, "清单里标记备用屏", func() bool {
		ss := f.hostSessions(t)
		return len(ss) == 1 && ss[0]["alt"] == true
	})

	// 新终端附着：只回放自进入备用屏以来的内容，并先恢复鼠标等模式
	b := f.attachLocal(t, sid, 0)
	if b.Ready.Prefix == "" || b.Ready.From == 0 || b.Ready.Truncated {
		t.Fatalf("备用屏中的新附着应带模式恢复序列并跳过主屏历史: %+v", b.Ready)
	}
}

// ---------- 流控 ----------

func TestLocalTerminalFlowControlPausesWithoutAckAndDeliversEverythingInOrder(t *testing.T) {
	f := newSSHFixture(t)
	sid := f.mustOpenLocal(t, "flow")

	b := f.attachLocal(t, sid, 0)
	b.waitFor("$ ")
	b.noAck = true
	const lines = 30000 // 约 350 KiB，超过 256 KiB 的流控窗口
	b.send(fmt.Sprintf("big %d\r", lines))
	// 按条件等，而不是睡一个固定时长：竞态检测器下链路慢得多，固定 1.2 秒时一半窗口的数据经常还没到（任务 #133）。
	// 先等到发出至少半个窗口的数据，再等输出安静下来，确认它停在流控窗口内。
	b.pumpUntil("没有确认之前也应发出窗口大小的数据", 20*time.Second, func() bool {
		return b.end-b.Ready.From >= termproto.AttachWindow/2
	})
	b.settle(500*time.Millisecond, 20*time.Second)

	got := b.end - b.Ready.From
	if got < termproto.AttachWindow/2 {
		t.Fatalf("没有确认之前也应发出窗口大小的数据，实际只有 %d 字节", got)
	}
	if limit := uint64(termproto.AttachWindow + 2*termproto.MaxDataPayload); got > limit {
		t.Fatalf("没有确认时发送应停在流控窗口内：发了 %d 字节，上限 %d", got, limit)
	}

	// 恢复确认后其余内容陆续到达，顺序完整、偏移连续
	b.noAck = false
	b.ack()
	b.waitFor(fmt.Sprintf("line-%d\r\n", lines-1))
	if n := strings.Count(b.text(), "line-"); n != lines {
		t.Fatalf("应收到 %d 行，实际 %d", lines, n)
	}
	last := -1
	for _, ln := range strings.Split(b.text(), "\r\n") {
		var i int
		if _, err := fmt.Sscanf(ln, "line-%d", &i); err == nil {
			if i != last+1 {
				t.Fatalf("行序错乱：%d 之后出现 %d", last, i)
			}
			last = i
		}
	}
}

// ---------- 管理 ----------

func TestLocalTerminalRenameKillLimitAndUnknownSession(t *testing.T) {
	f := newSSHFixture(t)
	sid := f.mustOpenLocal(t, "first")

	rn := f.env.call("POST", "/api/v1/terminal/hosts/"+f.code+"/sessions/"+sid+"/name", map[string]string{"name": "改名后"}, "Authorization", humanAuth(testUser))
	if rn.Status != 202 {
		t.Fatalf("重命名: %d %s", rn.Status, rn.Raw)
	}
	if ss := f.hostSessions(t); len(ss) != 1 || ss[0]["name"] != "改名后" {
		t.Fatalf("重命名未生效: %+v", ss)
	}

	// 不存在的会话：票据、终止、重命名都是 404，不会碰到别的会话
	bogus := strings.Repeat("a", 32)
	for _, p := range []string{"/ticket", "/kill"} {
		r := f.env.call("POST", "/api/v1/terminal/hosts/"+f.code+"/sessions/"+bogus+p, nil, f.adm...)
		if r.Status != 404 || r.str("code") != "session_not_found" {
			t.Fatalf("%s 对不存在的会话应为 404: %d %s", p, r.Status, r.Raw)
		}
	}

	// 名额：每台主机最多 maxLocalSessionsPerHost 个
	sids := []string{sid}
	for i := 1; i < maxLocalSessionsPerHost; i++ {
		sids = append(sids, f.mustOpenLocal(t, fmt.Sprintf("s%d", i)))
	}
	over := f.openLocal(map[string]any{"name": "over"})
	if over.Status != 409 || over.str("code") != "session_limit" {
		t.Fatalf("超过上限应为 409 session_limit: %d %s", over.Status, over.Raw)
	}
	if r := f.env.call("POST", "/api/v1/terminal/hosts/"+f.code+"/sessions/"+sids[0]+"/kill", nil, f.adm...); r.Status != 202 {
		t.Fatalf("终止: %d %s", r.Status, r.Raw)
	}
	eventually(t, 8*time.Second, "被终止的会话移出清单并释放名额", func() bool { return len(f.hostSessions(t)) == maxLocalSessionsPerHost-1 })
	f.mustOpenLocal(t, "again")
}

func TestLocalTerminalEndsWhenHostDisabled(t *testing.T) {
	f := newSSHFixture(t)
	sid := f.mustOpenLocal(t, "doomed")
	b := f.attachLocal(t, sid, 0)
	b.waitFor("welcome tester")

	if r := f.env.call("POST", "/api/v1/terminal/hosts/"+f.code+"/disable", nil, f.adm...); r.Status != 200 {
		t.Fatalf("停用: %d %s", r.Status, r.Raw)
	}
	if code := b.waitClose(8 * time.Second); code != termproto.CloseSessionEnded {
		t.Fatalf("停用主机应结束会话并以 %d 关闭，得到 %d", termproto.CloseSessionEnded, code)
	}
	eventually(t, 8*time.Second, "会话移出清单", func() bool { return len(f.env.hub.local.list(f.code)) == 0 })
	if r := f.openLocal(map[string]any{"name": "after"}); r.Status != 404 || r.str("code") != "host_not_found" {
		t.Fatalf("已停用的主机不能再开终端: %d %s", r.Status, r.Raw)
	}
	if evs := f.env.auditEvents(EvSessionKill); len(evs) == 0 {
		t.Fatal("停用时终止本地会话应写审计")
	}
}

func TestLocalTerminalIdleReclaim(t *testing.T) {
	f := newSSHFixture(t, withFakeClock())
	res := f.openLocal(map[string]any{"name": "idle", "idle_seconds": 3600})
	if res.Status != 200 {
		t.Fatalf("新建: %d %s", res.Status, res.Raw)
	}
	info := mapOf(res.Body["session"])
	if info["idle"] != float64(3600) || info["idle_own"] != true {
		t.Fatalf("应记下单独设置的空闲回收: %+v", info)
	}
	sid := info["sid"].(string)

	// 刚过半小时：还不到时限
	f.env.clock.Advance(30 * time.Minute)
	f.env.hub.local.maintain(f.env.clock.Now())
	if len(f.env.hub.local.list(f.code)) != 1 {
		t.Fatal("未到空闲时限，不应回收")
	}

	// 有人附着时空闲计时不走（时钟走过半小时之后解锁令牌已过期，重新解锁）
	f.adm = f.env.admin()
	b := f.attachLocal(t, sid, 0)
	b.waitFor("$ ")
	f.env.clock.Advance(3 * time.Hour)
	f.env.hub.local.maintain(f.env.clock.Now())
	if len(f.env.hub.local.list(f.code)) != 1 {
		t.Fatal("有人附着时不应回收")
	}

	// 断开之后重新计时，满一小时才回收
	_ = b.ws.Close(websocket.StatusNormalClosure, "")
	eventually(t, 3*time.Second, "附着数降为 0", func() bool {
		l := f.env.hub.local.list(f.code)
		return len(l) == 1 && l[0].Attached == 0
	})
	f.env.clock.Advance(20 * time.Minute)
	f.env.hub.local.maintain(f.env.clock.Now())
	if len(f.env.hub.local.list(f.code)) != 1 {
		t.Fatal("断开后未满时限，不应回收")
	}
	f.env.clock.Advance(time.Hour)
	f.env.hub.local.maintain(f.env.clock.Now())
	eventually(t, 8*time.Second, "空闲会话被回收", func() bool { return len(f.env.hub.local.list(f.code)) == 0 })
}

// ---------- 失败路径 ----------

func TestLocalTerminalOpenFailuresAreMappedAndNeverLeakSessions(t *testing.T) {
	f := newSSHFixture(t)

	// 主机换了密钥：拒绝连接，不向冒充者发认证，并把主机标成密钥不一致
	f.srv.SetHostSigner(sshdtest.NewSigner(t))
	f.srv.DropConnections()
	attempts := f.srv.AuthAttempts()
	r := f.openLocal(map[string]any{"name": "x"})
	if r.Status != 409 || r.str("code") != "ssh_host_key_mismatch" {
		t.Fatalf("密钥不一致应为 409 ssh_host_key_mismatch: %d %s", r.Status, r.Raw)
	}
	if got := f.srv.AuthAttempts(); got != attempts {
		t.Fatalf("向冒充者发出了认证尝试: %d → %d", attempts, got)
	}
	if len(f.env.hub.local.list(f.code)) != 0 {
		t.Fatal("失败的新建不应留下会话")
	}
	eventually(t, 3*time.Second, "主机被标记为密钥不一致", func() bool { return f.host(t).SSHKeyState == model.SSHKeyMismatch })
	if r := f.openLocal(map[string]any{"name": "y"}); r.Status != 409 {
		t.Fatalf("已标记密钥不一致时应在闸门处直接拒绝: %d %s", r.Status, r.Raw)
	}
}

func TestLocalTerminalUnreachableHost(t *testing.T) {
	f := newSSHFixture(t)
	f.srv.Close()
	r := f.openLocal(map[string]any{"name": "x"})
	if r.Status != 502 || r.str("code") != "ssh_unreachable" {
		t.Fatalf("连不上应为 502 ssh_unreachable: %d %s", r.Status, r.Raw)
	}
	if len(f.env.hub.local.list(f.code)) != 0 {
		t.Fatal("失败的新建不应留下会话")
	}
	// 名额没有泄漏：连续失败很多次之后，服务恢复仍然可以新建
	for i := 0; i < maxLocalSessionsPerHost+2; i++ {
		_ = f.openLocal(map[string]any{"name": "x"})
	}
	if n := func() int { f.env.hub.local.mu.Lock(); defer f.env.hub.local.mu.Unlock(); return f.env.hub.local.total }(); n != 0 {
		t.Fatalf("失败的新建不得占用名额: total=%d", n)
	}
}

func TestLocalTerminalSurvivesBrowserDisconnectAndEndsOnRemoteLoss(t *testing.T) {
	f := newSSHFixture(t)
	sid := f.mustOpenLocal(t, "persist")
	a := f.attachLocal(t, sid, 0)
	a.waitFor("$ ")
	_ = a.ws.CloseNow()
	eventually(t, 3*time.Second, "浏览器断开后会话继续运行且附着数归零", func() bool {
		ss := f.hostSessions(t)
		return len(ss) == 1 && ss[0]["state"] == termproto.SessionRunning && ss[0]["attached"] == float64(0)
	})

	// 远端连接中断：会话标记为已退出，并留下可见的原因
	b := f.attachLocal(t, sid, 0)
	b.waitFor("$ ")
	f.srv.DropConnections()
	if code := b.waitClose(10 * time.Second); code != termproto.CloseSessionEnded {
		t.Fatalf("连接中断应以 %d 关闭，得到 %d", termproto.CloseSessionEnded, code)
	}
	if !strings.Contains(b.text(), "SSH 连接已断开") {
		t.Fatalf("回放里应留下连接中断的提示: %q", lastChars(b.text(), 200))
	}
}
