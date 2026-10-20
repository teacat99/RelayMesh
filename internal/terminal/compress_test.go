package terminal

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// wireCount 统计一条连接在 socket 上实际收发的字节数（已经过压缩、位于 TCP 之上）。
type wireCount struct {
	in, out atomic.Int64
}

type countingConn struct {
	net.Conn
	w *wireCount
}

func (c *countingConn) Read(p []byte) (int, error) {
	n, err := c.Conn.Read(p)
	c.w.in.Add(int64(n))
	return n, err
}

func (c *countingConn) Write(p []byte) (int, error) {
	n, err := c.Conn.Write(p)
	c.w.out.Add(int64(n))
	return n, err
}

// countingHTTPClient 返回拨号时给 socket 包一层计数器的 http.Client，供 websocket.Dial 使用。
func countingHTTPClient(w *wireCount) *http.Client {
	return &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			c, err := (&net.Dialer{}).DialContext(ctx, network, addr)
			if err != nil {
				return nil, err
			}
			return &countingConn{Conn: c, w: w}, nil
		},
	}}
}

// tuiFrames 生成像内联 TUI 重绘那样的输出：每一帧先「擦除并上移」若干行，再把整段内容重画一遍，重复度很高。
func tuiFrames(total int) []byte {
	var b bytes.Buffer
	for i := 0; b.Len() < total; i++ {
		for j := 0; j < 20; j++ {
			b.WriteString("\x1b[2K\x1b[1A")
		}
		for j := 0; j < 20; j++ {
			fmt.Fprintf(&b, "\x1b[38;5;245m│\x1b[0m 第 %02d 行 · 会话输出是高度重复的文本，正是压缩擅长的内容 %03d\r\n", j, (i*7+j)%113)
		}
	}
	return b.Bytes()[:total]
}

// 浏览器与 Agent 两条链路都协商 permessage-deflate 时：一大段重复的终端输出逐字节一致地送达，
// 且 socket 上实际传输的字节数远小于原文；不提出压缩的旧客户端则照旧不压缩（兼容性）。
func TestCompressedLinksCarryLargeOutputIntact(t *testing.T) {
	const total = 192 << 10 // 小于 AttachWindow（256 KiB），不需要中途 ack
	payload := tuiFrames(total)

	type result struct {
		agentOut, browserIn  int64
		agentExt, browserExt string
	}
	run := func(t *testing.T, compress bool) result {
		t.Helper()
		var opts []envOption
		if compress {
			opts = append(opts, withCompression())
		}
		e := newTestEnv(t, opts...)
		e.browserWire = &wireCount{}
		hdr := e.admin()
		a := e.enrollAgent(testMeta("zip-host"), "压缩主机")
		if a.wire == nil {
			t.Fatal("伪 Agent 应带 socket 字节计数器")
		}
		sid := e.openSession(a, hdr, "main")
		ws, resp, err := e.dialBrowser(e.ticket(a, sid, hdr, 0), nil)
		if err != nil {
			t.Fatalf("浏览器拨号失败: %v", err)
		}
		t.Cleanup(func() { _ = ws.CloseNow() })
		readReady(t, ws)
		eventually(t, 2*time.Second, "Agent 收到 attach", func() bool {
			a.mu.Lock()
			defer a.mu.Unlock()
			return len(a.attaches) == 1
		})

		baseOut, baseIn := a.wire.out.Load(), e.browserWire.in.Load()
		for off := 0; off < len(payload); off += 16 << 10 {
			end := off + 16<<10
			if end > len(payload) {
				end = len(payload)
			}
			a.emit(sid, payload[off:end])
		}
		got, first := readOutput(t, ws, total)
		if first != 0 || !bytes.Equal(got, payload) {
			t.Fatalf("输出应逐字节一致且从偏移 0 开始（收 %d 字节，起点 %d）", len(got), first)
		}
		return result{
			agentOut:   a.wire.out.Load() - baseOut,
			browserIn:  e.browserWire.in.Load() - baseIn,
			agentExt:   a.ext,
			browserExt: resp.Header.Get("Sec-WebSocket-Extensions"),
		}
	}

	t.Run("两侧都协商压缩", func(t *testing.T) {
		r := run(t, true)
		if !strings.Contains(r.agentExt, "permessage-deflate") || !strings.Contains(r.browserExt, "permessage-deflate") {
			t.Fatalf("两条链路都应协商出 permessage-deflate，Agent=%q 浏览器=%q", r.agentExt, r.browserExt)
		}
		// 实测真实会话录制约 5~7 倍；这里的合成输出重复度更高，门槛放宽到三分之一，只为证明压缩确实在线上生效。
		if limit := int64(total / 3); r.agentOut >= limit || r.browserIn >= limit {
			t.Fatalf("压缩后传输量应明显小于原文 %d 字节：Agent→Hub %d，Hub→浏览器 %d", total, r.agentOut, r.browserIn)
		}
		t.Logf("原文 %d 字节 → Agent→Hub %d 字节，Hub→浏览器 %d 字节", total, r.agentOut, r.browserIn)
	})

	t.Run("旧客户端不提压缩时照旧传输", func(t *testing.T) {
		r := run(t, false)
		if r.agentExt != "" || r.browserExt != "" {
			t.Fatalf("没提出压缩就不应协商：Agent=%q 浏览器=%q", r.agentExt, r.browserExt)
		}
		if r.agentOut < total || r.browserIn < total {
			t.Fatalf("不压缩时传输量不应小于原文：Agent→Hub %d，Hub→浏览器 %d", r.agentOut, r.browserIn)
		}
	})
}
