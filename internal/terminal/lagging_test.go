package terminal

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/teacat99/RelayMesh/v2/internal/termproto"
)

// 「附着方持续跟不上输出」（#101）：会话有记录时，Hub 先告诉浏览器「正在追赶」，持续饱和就断开并要求重新附着；
// 没有记录的会话保持无损背压；回放本身慢不算跟不上。

// lagFrames 收集浏览器收到的 lag 控制帧（On 的取值序列）。
func lagCollector(into *[]bool) func([]byte) {
	return func(b []byte) {
		if tt, _ := termproto.ParseType(b); tt != termproto.TLag {
			return
		}
		var m termproto.BrowserLag
		if json.Unmarshal(b, &m) == nil {
			*into = append(*into, m.On)
		}
	}
}

// saturate 让会话在浏览器一个确认都不给的情况下产生足以让附着饱和的输出，并等 Hub 把它交给发送队列。
func saturate(s *attachedSession) {
	s.agent.emit(s.sid, bytes.Repeat([]byte("x"), termproto.LagUnacked+4096))
	time.Sleep(200 * time.Millisecond)
}

// tick 推进假时钟，并等待 Hub 每 250 毫秒一次的检查至少运行一轮。
func tick(e *testEnv, d time.Duration) {
	e.clock.Advance(d)
	time.Sleep(400 * time.Millisecond)
}

func TestLaggingBrowserOnRecordedSessionIsWarnedThenEjected(t *testing.T) {
	e := newTestEnv(t, withFakeClock())
	s := e.attachedSession(0)

	saturate(s)
	tick(e, termproto.LagWarnAfter+200*time.Millisecond) // 已饱和一会儿：先提醒
	tick(e, termproto.LagEjectAfter)                     // 持续饱和：断开

	var lags []bool
	code := readUntilClose(t, s.ws, 5*time.Second, lagCollector(&lags))
	if code != termproto.CloseLagging {
		t.Fatalf("持续跟不上输出应以 4414 断开: %d", code)
	}
	if len(lags) == 0 || !lags[0] {
		t.Fatalf("断开前应先收到 lag on: %v", lags)
	}
	eventually(t, 2*time.Second, "Agent 收到 detach", func() bool {
		s.agent.mu.Lock()
		defer s.agent.mu.Unlock()
		return len(s.agent.detaches) == 1
	})
}

func TestLaggingBrowserWithoutRecordingKeepsLosslessBackpressure(t *testing.T) {
	e := newTestEnv(t, withFakeClock())
	s := e.attachedSession(0)

	// 会话改为无痕（没有记录）：被跳过的内容读不回来，所以不能因为跟不上就断开。
	s.agent.mu.Lock()
	s.agent.sessions[s.sid].info.Rec = termproto.RecIncognito
	s.agent.mu.Unlock()
	s.agent.sendSessions()
	eventually(t, 2*time.Second, "Hub 看到无痕", func() bool {
		a := e.hub.onlineAgent(s.agent.code)
		if a == nil {
			return false
		}
		info, ok := a.findSession(s.sid)
		return ok && info.Rec == termproto.RecIncognito
	})

	saturate(s)
	tick(e, termproto.LagEjectAfter+time.Second)
	tick(e, time.Second)

	// 没有 lag 帧、没有断开：连接仍然可用（回显能回来）
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := s.ws.Write(ctx, websocket.MessageBinary, termproto.EncodeBrowserInput([]byte("ok"))); err != nil {
		t.Fatalf("无记录的会话不应因跟不上被断开: %v", err)
	}
	got, _ := readOutput(t, s.ws, termproto.LagUnacked+4096+2)
	if !bytes.HasSuffix(got, []byte("ok")) {
		t.Fatal("积压输出之后应能收到回显")
	}
}

func TestLaggingWarningIsWithdrawnWhenBrowserCatchesUp(t *testing.T) {
	e := newTestEnv(t, withFakeClock())
	s := e.attachedSession(0)

	chunk := termproto.LagUnacked + 4096
	saturate(s)
	tick(e, termproto.LagWarnAfter+200*time.Millisecond) // lag on
	got, _ := readOutput(t, s.ws, chunk)
	if len(got) != chunk {
		t.Fatalf("输出不完整: %d", len(got))
	}
	writeAck(t, s.ws, uint64(chunk)) // 追上了
	time.Sleep(150 * time.Millisecond)
	tick(e, time.Second) // 饱和已清除：撤回提醒

	var lags []bool
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	// 连接仍在：发一个输入，读到回显之前把控制帧都收集起来
	if err := s.ws.Write(ctx, websocket.MessageBinary, termproto.EncodeBrowserInput([]byte("k"))); err != nil {
		t.Fatalf("追上之后不应被断开: %v", err)
	}
	collect := lagCollector(&lags)
	for {
		typ, data, err := s.ws.Read(ctx)
		if err != nil {
			t.Fatalf("读取失败: %v", err)
		}
		if typ == websocket.MessageText {
			collect(data)
			continue
		}
		break // 回显到了
	}
	if len(lags) != 2 || !lags[0] || lags[1] {
		t.Fatalf("应依次收到 lag on / lag off: %v", lags)
	}
}

func TestSlowReplayIsNotMistakenForLagging(t *testing.T) {
	e := newTestEnv(t, withFakeClock())
	hdr := e.admin()
	a := e.enrollAgent(testMeta("replay-host"), "回放主机")
	sid := e.openSession(a, hdr, "main")
	// 附着之前会话已经积了一大段输出（超过窗口）
	a.emit(sid, bytes.Repeat([]byte("r"), termproto.AttachWindow+8192))

	ws, _, err := e.dialBrowser(e.ticket(a, sid, hdr, 0), nil)
	if err != nil {
		t.Fatalf("浏览器拨号失败: %v", err)
	}
	t.Cleanup(func() { _ = ws.CloseNow() })
	ready := readReady(t, ws)
	if ready.End < uint64(termproto.AttachWindow) {
		t.Fatalf("回放应超过一个窗口: end=%d", ready.End)
	}
	time.Sleep(200 * time.Millisecond)

	// 浏览器一个确认都没发，假时钟走了 5 秒（超过 LagEjectAfter、不到 StallTimeout）：回放慢不算跟不上
	tick(e, termproto.LagEjectAfter+time.Second)
	tick(e, time.Second)

	var lags []bool
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := ws.Write(ctx, websocket.MessageBinary, termproto.EncodeBrowserInput([]byte("k"))); err != nil {
		t.Fatalf("回放期间不应被断开: %v", err)
	}
	collect := lagCollector(&lags)
	var seen int
	for seen < int(ready.End)+1 {
		typ, data, err := ws.Read(ctx)
		if err != nil {
			t.Fatalf("读取失败（可能被错误断开了）: %v", err)
		}
		if typ == websocket.MessageText {
			collect(data)
			continue
		}
		if _, payload, derr := termproto.DecodeBrowserOutput(data); derr == nil {
			seen += len(payload)
		}
	}
	if len(lags) != 0 {
		t.Fatalf("回放不应触发 lag 帧: %v", lags)
	}
}

func TestAgentOverrunIsReportedAsLagging(t *testing.T) {
	e := newTestEnv(t)
	s := e.attachedSession(0)

	s.agent.mu.Lock()
	var ch uint32
	for c, fc := range s.agent.chans {
		if fc.sid == s.sid {
			ch = c
		}
	}
	s.agent.mu.Unlock()
	s.agent.send(termproto.Detached{T: termproto.TDetached, Ch: ch, Reason: "overrun"})

	if code := readUntilClose(t, s.ws, 3*time.Second, nil); code != termproto.CloseLagging {
		t.Fatalf("缓冲被越过应以 4414 关闭（让浏览器全新附着），而不是 4404: %d", code)
	}
}

func TestLaggingEjectIsDeferredWhileUserIsTyping(t *testing.T) {
	e := newTestEnv(t, withFakeClock())
	s := e.attachedSession(0)

	saturate(s)
	tick(e, termproto.LagWarnAfter+200*time.Millisecond) // lag on：遮罩已经盖上

	// 用户这时敲了一个键（比如想用 Ctrl+C 打断刷屏）：键要能送到程序，连接不能在这几秒里被断开
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := s.ws.Write(ctx, websocket.MessageBinary, termproto.EncodeBrowserInput([]byte{0x03})); err != nil {
		t.Fatalf("写入输入失败: %v", err)
	}
	time.Sleep(100 * time.Millisecond)
	tick(e, termproto.LagEjectAfter) // 饱和已超过断开时限，但刚输入过
	s.agent.mu.Lock()
	detached := len(s.agent.detaches)
	s.agent.mu.Unlock()
	if detached != 0 {
		t.Fatalf("刚敲过键不应因跟不上而断开（detach %d 次）", detached)
	}

	tick(e, termproto.LagInputGrace) // 输入停了，宽限已过、仍然跟不上：断开
	var lags []bool
	if code := readUntilClose(t, s.ws, 5*time.Second, lagCollector(&lags)); code != termproto.CloseLagging {
		t.Fatalf("输入停止、宽限过后应以 4414 断开: %d", code)
	}
}

// 提醒要在「输出到来的那一刻」发出，而不是等下一次定时检查：定时检查最多晚 250 毫秒，那段时间用户看到的正是要遮住的刷屏。
// 把定时检查拉长到一小时让它插不上手，只剩事件路径能发出提醒。
func TestLaggingWarningComesWithTheNextOutputNotTheNextTick(t *testing.T) {
	e := newTestEnv(t, withFakeClock())
	e.hub.attachTickOverride = time.Hour
	s := e.attachedSession(0)

	saturate(s)
	e.clock.Advance(termproto.LagWarnAfter + 100*time.Millisecond)
	s.agent.emit(s.sid, []byte("y")) // 下一帧输出到来

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	for {
		typ, data, err := s.ws.Read(ctx)
		if err != nil {
			t.Fatalf("没等到 lag on: %v", err)
		}
		if typ != websocket.MessageText {
			continue
		}
		var lags []bool
		lagCollector(&lags)(data)
		if len(lags) == 1 && lags[0] {
			return
		}
	}
}
