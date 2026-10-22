package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
)

const (
	// MaxStdioMessageSize sets the upper limit for a single JSON-RPC message line (16MB).
	MaxStdioMessageSize = 16 * 1024 * 1024
)

// StdioMessage 是 stdio 读循环交给处理器的一条消息。
type StdioMessage struct {
	// Raw 是去掉首尾空白的原始一行 JSON（转发给别的进程时原样发送）。
	Raw []byte
	// Method 是 JSON-RPC 方法名。
	Method string
	// ID 是请求的 id（字符串 / 数字）；通知为 nil。
	ID any
	// IsNotification 为真表示这条消息没有 id，或方法以 notifications/ 开头：不得产生响应。
	IsNotification bool

	req *jsonRPCRequest // 已解析的请求；本进程内处理时直接用，省一次解析
}

// StdioHandler 处理一条消息：请求返回完整的 JSON-RPC 响应（单行 JSON，不含换行）；通知或不需要响应时返回 nil。
// ctx 在客户端发来 notifications/cancelled、stdin 关闭或进程退出时取消。
type StdioHandler func(ctx context.Context, msg StdioMessage) []byte

// StdioErrorResponse 生成一条 JSON-RPC 错误响应（单行 JSON）。转发进程在主进程暂时不可用时用它回复请求。
func StdioErrorResponse(id any, code int, message string) []byte {
	data, err := json.Marshal(&jsonRPCResponse{
		JSONRPC: "2.0",
		ID:      id,
		Error:   &rpcError{Code: code, Message: message},
	})
	if err != nil {
		return nil
	}
	return data
}

// LocalStdioHandler 返回在本进程内直接处理消息的处理器：用本机 stdio 的可信凭据调用 MCP 服务。
func LocalStdioHandler(server *Server) StdioHandler {
	return func(ctx context.Context, msg StdioMessage) []byte {
		req := msg.req
		if req == nil {
			// 没经过 StdioTransport 直接调用时的兜底：自己解析一次。
			var parsed jsonRPCRequest
			if err := json.Unmarshal(msg.Raw, &parsed); err != nil {
				return StdioErrorResponse(nil, -32700, "Parse error")
			}
			req = &parsed
		}
		return server.handleLocalMessage(ctx, server.cfg.HostName, req)
	}
}

// StdioTransport manages the stdio JSON-RPC loop with concurrent execution, write serialization, and cancellation.
// 消息交给 StdioHandler 处理：本进程直接处理（NewStdioTransport），或转发给同机另一个进程里的 Hub（见 LocalForwarder）。
type StdioTransport struct {
	handle  StdioHandler
	in      io.Reader
	out     io.Writer
	writeMu sync.Mutex

	cancelsMu sync.Mutex
	cancels   map[string]context.CancelFunc
	wg        sync.WaitGroup
}

// NewStdioTransport creates a new StdioTransport instance that handles every message in this process.
func NewStdioTransport(server *Server, in io.Reader, out io.Writer) *StdioTransport {
	return NewStdioTransportWithHandler(LocalStdioHandler(server), in, out)
}

// NewStdioTransportWithHandler 创建把消息交给指定处理器的传输层（读循环、并发、取消、写串行化行为与本进程直接处理时完全一致）。
func NewStdioTransportWithHandler(handle StdioHandler, in io.Reader, out io.Writer) *StdioTransport {
	return &StdioTransport{
		handle:  handle,
		in:      in,
		out:     out,
		cancels: make(map[string]context.CancelFunc),
	}
}

// stdioLine 是读协程交给主循环的一行（或读取错误）。
type stdioLine struct {
	data []byte
	err  error
}

// readLines 在单独的协程里读 stdin：主循环因此能在 stdin 没有新数据时也响应 ctx 取消（收到 SIGTERM 要能及时退出，
// 否则别的窗口要等到 stdin 关闭才知道主进程没了）。读到 EOF 后协程结束；doneCh 关闭后协程在下一次交付时退出。
func (t *StdioTransport) readLines(reader *bufio.Reader, doneCh <-chan struct{}) <-chan stdioLine {
	lines := make(chan stdioLine)
	go func() {
		defer close(lines)
		for {
			line, err := readLineBounded(reader, MaxStdioMessageSize)
			select {
			case lines <- stdioLine{data: line, err: err}:
			case <-doneCh:
				return
			}
			if err != nil && errors.Is(err, io.EOF) {
				return
			}
		}
	}()
	return lines
}

// Run executes the stdio read loop until EOF or ctx cancellation.
func (t *StdioTransport) Run(ctx context.Context) error {
	reader := bufio.NewReader(t.in)

	// Background watcher to cancel in-flight requests when main context is done
	doneCh := make(chan struct{})
	defer close(doneCh)
	go func() {
		select {
		case <-ctx.Done():
			t.cancelAll()
		case <-doneCh:
		}
	}()

	lines := t.readLines(reader, doneCh)

	for {
		var item stdioLine
		select {
		case <-ctx.Done():
			t.wg.Wait()
			return ctx.Err()
		case l, ok := <-lines:
			if !ok {
				// 读协程已经结束（EOF 已交付过）：按 EOF 收尾
				t.cancelAll()
				t.wg.Wait()
				return nil
			}
			item = l
		}

		if item.err != nil {
			if errors.Is(item.err, io.EOF) {
				// EOF reached: client closed stdin
				t.cancelAll()
				t.wg.Wait()
				return nil
			}
			t.writeError(nil, -32700, fmt.Sprintf("Read error: %v", item.err))
			continue
		}

		line := bytes.TrimSpace(item.data)
		if len(line) == 0 {
			continue
		}

		var req jsonRPCRequest
		if err := json.Unmarshal(line, &req); err != nil {
			t.writeError(nil, -32700, "Parse error")
			continue
		}

		// Handle cancellation notification
		if req.Method == "notifications/cancelled" {
			var cancelParams struct {
				RequestID any `json:"requestId"`
			}
			if err := json.Unmarshal(req.Params, &cancelParams); err == nil {
				t.triggerCancel(cancelParams.RequestID)
			}
			continue
		}

		msg := StdioMessage{
			Raw:            line,
			Method:         req.Method,
			ID:             req.ParsedID(),
			IsNotification: !req.HasID() || strings.HasPrefix(req.Method, "notifications/"),
			req:            &req,
		}

		// Notification: MUST NOT produce response
		if msg.IsNotification {
			_ = t.handle(ctx, msg)
			continue
		}

		// Request: dispatch in a new goroutine for concurrent processing
		t.wg.Add(1)
		reqCtx, cancel := context.WithCancel(ctx)
		cancelKey := t.registerCancel(msg.ID, cancel)

		go func(m StdioMessage, c context.Context, key string, canFn context.CancelFunc) {
			defer func() {
				t.unregisterCancel(key)
				canFn()
				t.wg.Done()
			}()

			resp := t.handle(c, m)
			if resp == nil {
				return
			}

			_ = t.writeRaw(resp)
		}(msg, reqCtx, cancelKey, cancel)
	}
}

// writeRaw 把一条已经序列化好的响应写成一行（写串行化，保证 stdout 上每行都是完整的 JSON）。
func (t *StdioTransport) writeRaw(resp []byte) error {
	data := make([]byte, 0, len(resp)+1)
	data = append(data, resp...)
	data = append(data, '\n')

	t.writeMu.Lock()
	defer t.writeMu.Unlock()

	_, err := t.out.Write(data)
	return err
}

func (t *StdioTransport) writeError(id any, code int, message string) {
	if data := StdioErrorResponse(id, code, message); data != nil {
		_ = t.writeRaw(data)
	}
}

func (t *StdioTransport) registerCancel(id any, cancel context.CancelFunc) string {
	if id == nil {
		return ""
	}
	key := fmt.Sprintf("%v", id)
	t.cancelsMu.Lock()
	defer t.cancelsMu.Unlock()
	t.cancels[key] = cancel
	return key
}

func (t *StdioTransport) unregisterCancel(key string) {
	if key == "" {
		return
	}
	t.cancelsMu.Lock()
	defer t.cancelsMu.Unlock()
	delete(t.cancels, key)
}

func (t *StdioTransport) triggerCancel(id any) {
	if id == nil {
		return
	}
	key := fmt.Sprintf("%v", id)
	t.cancelsMu.Lock()
	cancel, ok := t.cancels[key]
	if ok {
		delete(t.cancels, key)
	}
	t.cancelsMu.Unlock()
	if ok && cancel != nil {
		cancel()
	}
}

func (t *StdioTransport) cancelAll() {
	t.cancelsMu.Lock()
	defer t.cancelsMu.Unlock()
	for k, cancel := range t.cancels {
		if cancel != nil {
			cancel()
		}
		delete(t.cancels, k)
	}
}

// readLineBounded reads up to '\n' or EOF while enforcing a maximum byte limit.
func readLineBounded(r *bufio.Reader, maxBytes int) ([]byte, error) {
	var buf bytes.Buffer
	for {
		chunk, isPrefix, err := r.ReadLine()
		if err != nil {
			if err == io.EOF && buf.Len() > 0 {
				return buf.Bytes(), nil
			}
			return nil, err
		}
		if buf.Len()+len(chunk) > maxBytes {
			return nil, fmt.Errorf("message exceeded limit of %d bytes", maxBytes)
		}
		buf.Write(chunk)
		if !isPrefix {
			break
		}
	}
	return buf.Bytes(), nil
}

// RunStdio runs the stdio loop for the given MCP Server until EOF or ctx cancellation.
func RunStdio(ctx context.Context, server *Server, in io.Reader, out io.Writer) error {
	transport := NewStdioTransport(server, in, out)
	return transport.Run(ctx)
}
