package mcp

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/teacat99/RelayMesh/internal/config"
	"github.com/teacat99/RelayMesh/internal/store"
)

func TestProtocol_NotificationHandling(t *testing.T) {
	cfg := &config.Config{
		ProjectID: "test-proj",
		Version:   "1.1.0-test",
	}
	st, err := store.New(":memory:")
	if err != nil {
		t.Fatalf("failed to create memory store: %v", err)
	}
	srv := NewServer(cfg, st, nil)
	credCtx := LocalStdioCredential()

	// 1. Notification (no id)
	reqJSON := []byte(`{"jsonrpc":"2.0","method":"notifications/initialized"}`)
	var req jsonRPCRequest
	if err := json.Unmarshal(reqJSON, &req); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}
	if req.HasID() {
		t.Fatalf("expected HasID() to be false for notification")
	}

	res := srv.HandleRPCRequest(context.Background(), credCtx, &req)
	if !res.IsNotification {
		t.Fatalf("expected IsNotification to be true")
	}
	if res.Response != nil {
		t.Fatalf("expected Response to be nil for notification, got: %+v", res.Response)
	}

	// 2. Request (with id)
	initJSON := []byte(`{"jsonrpc":"2.0","id":"req-1","method":"initialize","params":{}}`)
	var initReq jsonRPCRequest
	if err := json.Unmarshal(initJSON, &initReq); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}
	if !initReq.HasID() {
		t.Fatalf("expected HasID() to be true")
	}
	if idStr, ok := initReq.ParsedID().(string); !ok || idStr != "req-1" {
		t.Fatalf("expected ParsedID() == 'req-1', got: %v", initReq.ParsedID())
	}

	initRes := srv.HandleRPCRequest(context.Background(), credCtx, &initReq)
	if initRes.IsNotification {
		t.Fatalf("expected IsNotification to be false for initialize request")
	}
	if initRes.Response == nil {
		t.Fatalf("expected Response to be non-nil")
	}
	if initRes.Response.Error != nil {
		t.Fatalf("unexpected error: %v", initRes.Response.Error)
	}

	// 3. Tools list via LocalStdioCredential
	toolsJSON := []byte(`{"jsonrpc":"2.0","id":100,"method":"tools/list"}`)
	var toolsReq jsonRPCRequest
	json.Unmarshal(toolsJSON, &toolsReq)

	toolsRes := srv.HandleRPCRequest(context.Background(), credCtx, &toolsReq)
	if toolsRes.Response == nil || toolsRes.Response.Result == nil {
		t.Fatalf("expected valid tools result")
	}
	resMap := toolsRes.Response.Result.(map[string]any)
	if toolsList, ok := resMap["tools"].([]ToolDefinition); ok {
		if len(toolsList) != 9 {
			t.Fatalf("expected 9 tools for stdio credential, got %d", len(toolsList))
		}
	} else if toolsAny, ok := resMap["tools"].([]any); ok {
		if len(toolsAny) != 9 {
			t.Fatalf("expected 9 tools for stdio credential, got %d", len(toolsAny))
		}
	} else {
		t.Fatalf("unexpected type for tools in result: %T", resMap["tools"])
	}
}
