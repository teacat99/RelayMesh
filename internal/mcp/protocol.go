package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/teacat99/RelayMesh/internal/model"
)

// Raw JSON-RPC 2.0 structures
type jsonRPCRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"` // Preserved raw to distinguish absent ID (notification) from null ID
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// HasID returns true if an "id" field was present in the JSON-RPC request payload.
func (r *jsonRPCRequest) HasID() bool {
	return len(r.ID) > 0
}

// ParsedID returns unmarshaled ID as string, number, or nil.
func (r *jsonRPCRequest) ParsedID() any {
	if len(r.ID) == 0 {
		return nil
	}
	var id any
	if err := json.Unmarshal(r.ID, &id); err != nil {
		return string(r.ID)
	}
	return id
}

type jsonRPCResponse struct {
	JSONRPC string    `json:"jsonrpc"`
	ID      any       `json:"id"`
	Result  any       `json:"result,omitempty"`
	Error   *rpcError `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

type mcpContentItem struct {
	Type     string `json:"type"`
	Text     string `json:"text,omitempty"`
	Data     string `json:"data,omitempty"`
	MIMEType string `json:"mimeType,omitempty"`
}

type toolCallResult struct {
	Content []mcpContentItem `json:"content"`
	IsError bool             `json:"isError"`
}

type CredentialContext struct {
	CredentialID   uint
	CredentialName string
	HostName       string
	TokenString    string
	BaseURL        string
	Permissions    model.Permissions
	Source         string // "db_credential", "env_token", "open_access", "local_stdio"
}

type credCtxKeyType struct{}

var credCtxKey = credCtxKeyType{}

func CredentialFromContext(ctx context.Context) *CredentialContext {
	if v, ok := ctx.Value(credCtxKey).(*CredentialContext); ok {
		return v
	}
	return nil
}

// LocalStdioCredential returns the trusted local stdio credential context with full permissions.
func LocalStdioCredential(defaultHost ...string) *CredentialContext {
	host := "localhost"
	if len(defaultHost) > 0 && strings.TrimSpace(defaultHost[0]) != "" {
		host = strings.TrimSpace(defaultHost[0])
	}
	return &CredentialContext{
		CredentialName: "local_stdio",
		HostName:       host,
		Permissions:    model.AllPermissions(),
		Source:         "local_stdio",
	}
}

// RPCExecutionResult represents the outcome of an RPC call.
type RPCExecutionResult struct {
	Response       *jsonRPCResponse
	IsNotification bool
}

// HandleRPCRequest processes a parsed JSON-RPC request in a transport-neutral manner.
func (s *Server) HandleRPCRequest(ctx context.Context, credCtx *CredentialContext, req *jsonRPCRequest) RPCExecutionResult {
	// Notifications (no ID or explicit notifications/ method) MUST NOT produce responses in stdio
	if !req.HasID() || strings.HasPrefix(req.Method, "notifications/") {
		switch req.Method {
		case "notifications/initialized":
			// Client initialized notification
			return RPCExecutionResult{IsNotification: true}
		case "notifications/cancelled":
			// Handle client cancellation notification if requestId is specified
			if len(req.Params) > 0 {
				var cancelParams struct {
					RequestID any    `json:"requestId"`
					Reason    string `json:"reason,omitempty"`
				}
				_ = json.Unmarshal(req.Params, &cancelParams)
			}
			return RPCExecutionResult{IsNotification: true}
		default:
			return RPCExecutionResult{IsNotification: true}
		}
	}

	resp := &jsonRPCResponse{
		JSONRPC: "2.0",
		ID:      req.ParsedID(),
	}

	switch req.Method {
	case "initialize":
		resp.Result = map[string]any{
			"protocolVersion": "2024-11-05",
			"capabilities": map[string]any{
				"tools": map[string]any{
					"listChanged": false,
				},
				"prompts": map[string]any{
					"listChanged": false,
				},
			},
			"serverInfo": map[string]any{
				"name":    "RelayMesh",
				"version": s.cfg.Version,
			},
		}

	case "tools/list":
		tools := GetToolDefinitionsForPermissions(credCtx.Permissions)
		resp.Result = map[string]any{
			"tools": tools,
		}

	case "tools/call":
		var callParams struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		}
		if err := json.Unmarshal(req.Params, &callParams); err != nil {
			resp.Error = &rpcError{Code: -32602, Message: "Invalid params"}
			return RPCExecutionResult{Response: resp}
		}

		res, isErr, err := s.dispatchTool(ctx, credCtx, callParams.Name, callParams.Arguments)
		if err != nil {
			resp.Result = toolCallResult{
				Content: []mcpContentItem{
					{Type: "text", Text: fmt.Sprintf("Error: %v", err)},
				},
				IsError: true,
			}
		} else if customRes, ok := res.(toolCallResult); ok {
			resp.Result = customRes
		} else if items, ok := res.([]mcpContentItem); ok {
			resp.Result = toolCallResult{
				Content: items,
				IsError: isErr,
			}
		} else {
			var text string
			if str, ok := res.(string); ok {
				text = str
			} else {
				bytes, _ := json.MarshalIndent(res, "", "  ")
				text = string(bytes)
			}

			resp.Result = toolCallResult{
				Content: []mcpContentItem{
					{Type: "text", Text: text},
				},
				IsError: isErr,
			}
		}

	case "prompts/list":
		resp.Result = map[string]any{
			"prompts": []map[string]any{
				{
					"name":        "chat",
					"description": "Start interactive feedback session with user via RelayMesh Web UI.",
				},
			},
		}

	case "prompts/get":
		resp.Result = map[string]any{
			"description": "Start interactive feedback session with user via RelayMesh Web UI.",
			"messages": []map[string]any{
				{
					"role": "user",
					"content": map[string]any{
						"type": "text",
						"text": "Please use interactive_feedback to interact with user through RelayMesh Web UI.",
					},
				},
			},
		}

	default:
		resp.Error = &rpcError{Code: -32601, Message: fmt.Sprintf("Method not found: %s", req.Method)}
	}

	return RPCExecutionResult{Response: resp}
}
