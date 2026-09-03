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

// StdioTransport manages the stdio JSON-RPC loop with concurrent execution, write serialization, and cancellation.
type StdioTransport struct {
	server  *Server
	in      io.Reader
	out     io.Writer
	writeMu sync.Mutex

	cancelsMu sync.Mutex
	cancels   map[string]context.CancelFunc
	wg        sync.WaitGroup
}

// NewStdioTransport creates a new StdioTransport instance.
func NewStdioTransport(server *Server, in io.Reader, out io.Writer) *StdioTransport {
	return &StdioTransport{
		server:  server,
		in:      in,
		out:     out,
		cancels: make(map[string]context.CancelFunc),
	}
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

	for {
		select {
		case <-ctx.Done():
			t.wg.Wait()
			return ctx.Err()
		default:
		}

		line, err := readLineBounded(reader, MaxStdioMessageSize)
		if err != nil {
			if errors.Is(err, io.EOF) {
				// EOF reached: client closed stdin
				t.cancelAll()
				t.wg.Wait()
				return nil
			}
			t.writeError(nil, -32700, fmt.Sprintf("Read error: %v", err))
			continue
		}

		line = bytes.TrimSpace(line)
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

		// Notification: MUST NOT produce response
		if !req.HasID() || strings.HasPrefix(req.Method, "notifications/") {
			credCtx := LocalStdioCredential()
			t.server.HandleRPCRequest(ctx, credCtx, &req)
			continue
		}

		// Request: dispatch in a new goroutine for concurrent processing
		t.wg.Add(1)
		reqCtx, cancel := context.WithCancel(ctx)
		cancelKey := t.registerCancel(req.ParsedID(), cancel)

		go func(r jsonRPCRequest, c context.Context, key string, canFn context.CancelFunc) {
			defer func() {
				t.unregisterCancel(key)
				canFn()
				t.wg.Done()
			}()

			credCtx := LocalStdioCredential()
			reqCtxWithCred := context.WithValue(c, credCtxKey, credCtx)

			execRes := t.server.HandleRPCRequest(reqCtxWithCred, credCtx, &r)
			if execRes.IsNotification || execRes.Response == nil {
				return
			}

			_ = t.writeResponse(execRes.Response)
		}(req, reqCtx, cancelKey, cancel)
	}
}

func (t *StdioTransport) writeResponse(resp *jsonRPCResponse) error {
	data, err := json.Marshal(resp)
	if err != nil {
		return err
	}
	data = append(data, '\n')

	t.writeMu.Lock()
	defer t.writeMu.Unlock()

	_, err = t.out.Write(data)
	return err
}

func (t *StdioTransport) writeError(id any, code int, message string) {
	resp := &jsonRPCResponse{
		JSONRPC: "2.0",
		ID:      id,
		Error: &rpcError{
			Code:    code,
			Message: message,
		},
	}
	_ = t.writeResponse(resp)
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
