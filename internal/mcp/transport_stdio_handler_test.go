package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// syncBuffer 是线程安全的输出缓冲（传输层从多个协程写响应）。
type syncBuffer struct {
	mu  sync.Mutex
	buf strings.Builder
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// 请求并发处理、响应可以乱序但每行都是完整的 JSON，通知不产生响应。
func TestStdioTransportWithHandlerConcurrencyAndPurity(t *testing.T) {
	const n = 24
	var running, maxRunning int32
	handler := func(ctx context.Context, msg StdioMessage) []byte {
		if msg.IsNotification {
			return nil
		}
		cur := atomic.AddInt32(&running, 1)
		for {
			prev := atomic.LoadInt32(&maxRunning)
			if cur <= prev || atomic.CompareAndSwapInt32(&maxRunning, prev, cur) {
				break
			}
		}
		// id 越小睡得越久：响应一定乱序
		id, _ := msg.ID.(float64)
		time.Sleep(time.Duration(n-int(id)) * 5 * time.Millisecond)
		atomic.AddInt32(&running, -1)
		return []byte(fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"result":{"ok":true}}`, int(id)))
	}

	var in strings.Builder
	for i := 1; i <= n; i++ {
		fmt.Fprintf(&in, `{"jsonrpc":"2.0","id":%d,"method":"tools/call","params":{}}`+"\n", i)
		if i%5 == 0 {
			in.WriteString(`{"jsonrpc":"2.0","method":"notifications/initialized"}` + "\n")
		}
	}
	out := &syncBuffer{}
	tr := NewStdioTransportWithHandler(handler, strings.NewReader(in.String()), out)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := tr.Run(ctx); err != nil {
		t.Fatalf("Run 返回错误：%v", err)
	}

	seen := map[int]bool{}
	sc := bufio.NewScanner(strings.NewReader(out.String()))
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		line := sc.Text()
		if !json.Valid([]byte(line)) {
			t.Fatalf("stdout 上出现了不是完整 JSON 的行：%q", line)
		}
		var r struct {
			ID int `json:"id"`
		}
		_ = json.Unmarshal([]byte(line), &r)
		seen[r.ID] = true
	}
	if len(seen) != n {
		t.Fatalf("应收到 %d 条响应（通知不产生响应），实际 %d", n, len(seen))
	}
	if atomic.LoadInt32(&maxRunning) < 2 {
		t.Fatalf("请求应并发处理，最大并发只有 %d", maxRunning)
	}
}

// notifications/cancelled 取消对应请求的上下文（转发进程靠它中断 HTTP 调用）。
func TestStdioTransportWithHandlerCancelNotification(t *testing.T) {
	started := make(chan struct{})
	sawCancel := make(chan struct{})
	handler := func(ctx context.Context, msg StdioMessage) []byte {
		if msg.IsNotification {
			return nil
		}
		close(started)
		select {
		case <-ctx.Done():
			close(sawCancel)
		case <-time.After(5 * time.Second):
		}
		return nil // 被取消的请求不回复
	}

	pr, pw := io.Pipe()
	out := &syncBuffer{}
	tr := NewStdioTransportWithHandler(handler, pr, out)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- tr.Run(ctx) }()

	fmt.Fprintln(pw, `{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{}}`)
	<-started
	fmt.Fprintln(pw, `{"jsonrpc":"2.0","method":"notifications/cancelled","params":{"requestId":5}}`)
	select {
	case <-sawCancel:
	case <-time.After(3 * time.Second):
		t.Fatalf("notifications/cancelled 应取消对应请求的上下文")
	}

	_ = pw.Close() // stdin EOF：正常收尾
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("EOF 收尾应返回 nil，实际 %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatalf("stdin 关闭后应退出")
	}
	if got := out.String(); got != "" {
		t.Fatalf("被取消的请求不应有输出，实际 %q", got)
	}
}

// stdin 一直没有新数据时，收到终止信号（ctx 取消）也要能及时退出——别的窗口靠主进程退出来接管。
func TestStdioTransportReturnsPromptlyOnContextCancelWithIdleStdin(t *testing.T) {
	pr, pw := io.Pipe()
	defer pw.Close()
	tr := NewStdioTransportWithHandler(func(context.Context, StdioMessage) []byte { return nil }, pr, io.Discard)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- tr.Run(ctx) }()

	time.Sleep(100 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("应返回 context.Canceled，实际 %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("ctx 取消后应立刻退出，不能等 stdin 来下一行")
	}
}

// 解析失败的行回 -32700，之后的消息照常处理。
func TestStdioTransportWithHandlerParseErrorKeepsGoing(t *testing.T) {
	handler := func(ctx context.Context, msg StdioMessage) []byte {
		return []byte(fmt.Sprintf(`{"jsonrpc":"2.0","id":%v,"result":{}}`, msg.ID))
	}
	out := &syncBuffer{}
	in := "{not json\n" + `{"jsonrpc":"2.0","id":2,"method":"x"}` + "\n"
	tr := NewStdioTransportWithHandler(handler, strings.NewReader(in), out)
	if err := tr.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	if !strings.Contains(text, "-32700") || !strings.Contains(text, `"id":2`) {
		t.Fatalf("应有 -32700 与 id=2 的响应：%q", text)
	}
}
