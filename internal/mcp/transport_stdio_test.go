package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/teacat99/RelayMesh/internal/config"
	"github.com/teacat99/RelayMesh/internal/store"
)

func TestStdioTransport_EndToEnd(t *testing.T) {
	cfg := &config.Config{
		ProjectID: "test-stdio-proj",
		Version:   "1.1.0-test",
	}
	st, err := store.New(":memory:")
	if err != nil {
		t.Fatalf("failed to create memory store: %v", err)
	}
	srv := NewServer(cfg, st, nil)

	// Prepare mock input containing:
	// 1. initialize request
	// 2. notifications/initialized
	// 3. tools/list request
	// 4. tools/call request (get_system_info)
	inputData := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"get_system_info","arguments":{}}}`,
		"", // Trailing newline
	}, "\n")

	in := bytes.NewBufferString(inputData)
	out := &bytes.Buffer{}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	err = RunStdio(ctx, srv, in, out)
	if err != nil {
		t.Fatalf("RunStdio returned unexpected error: %v", err)
	}

	// Output must only contain responses for id 1, id 2, id 3 (notifications/initialized must NOT produce response)
	outStr := strings.TrimSpace(out.String())
	lines := strings.Split(outStr, "\n")
	if len(lines) != 3 {
		t.Fatalf("expected exactly 3 response lines, got %d:\n%s", len(lines), outStr)
	}

	// TEST-005: stdout purity — every line must be valid JSON
	for i, line := range lines {
		if !json.Valid([]byte(line)) {
			t.Fatalf("line %d is not valid JSON: %s", i, line)
		}
	}

	// Index responses by ID (numbers unmarshal to float64 in any)
	respsByID := make(map[int]jsonRPCResponse)
	for i, line := range lines {
		if !json.Valid([]byte(line)) {
			t.Fatalf("line %d is not valid JSON: %s", i, line)
		}
		var r jsonRPCResponse
		if err := json.Unmarshal([]byte(line), &r); err != nil {
			t.Fatalf("line %d unmarshal error: %v", i, err)
		}
		if num, ok := r.ID.(float64); ok {
			respsByID[int(num)] = r
		}
	}

	if len(respsByID) != 3 {
		t.Fatalf("expected 3 responses indexed by ID, got %d", len(respsByID))
	}

	// Check response 1 (initialize)
	resp1, ok1 := respsByID[1]
	if !ok1 || resp1.Error != nil {
		t.Fatalf("response 1 error: %+v", resp1.Error)
	}

	// Check response 2 (tools/list)
	resp2, ok2 := respsByID[2]
	if !ok2 || resp2.Error != nil {
		t.Fatalf("response 2 error: %+v", resp2.Error)
	}
	resMap2 := resp2.Result.(map[string]any)
	toolsList := resMap2["tools"].([]any)
	if len(toolsList) != 10 {
		t.Fatalf("expected 10 tools in tools/list, got %d", len(toolsList))
	}

	// Check response 3 (tools/call get_system_info)
	resp3, ok3 := respsByID[3]
	if !ok3 || resp3.Error != nil {
		t.Fatalf("response 3 error: %+v", resp3.Error)
	}
	resMap3 := resp3.Result.(map[string]any)
	contentList := resMap3["content"].([]any)
	if len(contentList) == 0 {
		t.Fatalf("expected non-empty content in tools/call result")
	}
	textItem := contentList[0].(map[string]any)
	textStr := textItem["text"].(string)
	if !strings.Contains(textStr, "RelayMesh") {
		t.Fatalf("expected get_system_info text to contain RelayMesh, got: %s", textStr)
	}
}

func TestStdioTransport_Cancellation(t *testing.T) {
	cfg := &config.Config{
		ProjectID: "test-cancel",
	}
	st, _ := store.New(":memory:")
	srv := NewServer(cfg, st, nil)

	in := &bytes.Buffer{}
	out := &bytes.Buffer{}
	transport := NewStdioTransport(srv, in, out)

	cancelled := false
	cancelKey := transport.registerCancel("req-99", func() {
		cancelled = true
	})
	if cancelKey != "req-99" {
		t.Fatalf("expected cancelKey == req-99, got %s", cancelKey)
	}

	transport.triggerCancel("req-99")
	if !cancelled {
		t.Fatalf("expected cancel function to be called")
	}

	// Triggering again should not panic or fail
	transport.triggerCancel("req-99")
}
