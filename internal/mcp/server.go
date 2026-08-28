package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/teacat99/RelayMesh/internal/config"
	"github.com/teacat99/RelayMesh/internal/model"
	"github.com/teacat99/RelayMesh/internal/store"
)

type sessionWaiterItem struct {
	ch         chan *model.FeedbackSession
	startedAt  time.Time
	timeoutSec int
}

type Server struct {
	cfg             *config.Config
	store           *store.Store
	waitersMu       sync.Mutex
	waiters         map[string][]sessionWaiterItem
	lastKeepaliveMu sync.Mutex
	lastKeepalives  map[string]time.Time
	onUpdate        func(eventType string, data any)
}

func NewServer(cfg *config.Config, st *store.Store, onUpdate func(eventType string, data any)) *Server {
	return &Server{
		cfg:            cfg,
		store:          st,
		waiters:        make(map[string][]sessionWaiterItem),
		lastKeepalives: make(map[string]time.Time),
		onUpdate:       onUpdate,
	}
}

func (s *Server) registerSessionWaiter(sessionID string, timeoutSec int) chan *model.FeedbackSession {
	s.waitersMu.Lock()
	defer s.waitersMu.Unlock()

	s.lastKeepaliveMu.Lock()
	delete(s.lastKeepalives, sessionID)
	s.lastKeepaliveMu.Unlock()

	ch := make(chan *model.FeedbackSession, 1)
	item := sessionWaiterItem{
		ch:         ch,
		startedAt:  time.Now(),
		timeoutSec: timeoutSec,
	}
	s.waiters[sessionID] = append(s.waiters[sessionID], item)
	return ch
}

func (s *Server) unregisterSessionWaiter(sessionID string, ch chan *model.FeedbackSession) {
	s.waitersMu.Lock()
	defer s.waitersMu.Unlock()

	list := s.waiters[sessionID]
	for i, item := range list {
		if item.ch == ch {
			s.waiters[sessionID] = append(list[:i], list[i+1:]...)
			break
		}
	}
	if len(s.waiters[sessionID]) == 0 {
		delete(s.waiters, sessionID)
	}
}

func (s *Server) RecordKeepaliveResponse(sessionID string) {
	s.lastKeepaliveMu.Lock()
	defer s.lastKeepaliveMu.Unlock()
	s.lastKeepalives[sessionID] = time.Now()
}

func (s *Server) GetLastKeepaliveTime(sessionID string) *time.Time {
	s.lastKeepaliveMu.Lock()
	defer s.lastKeepaliveMu.Unlock()
	if t, ok := s.lastKeepalives[sessionID]; ok {
		return &t
	}
	return nil
}

func (s *Server) HasActiveWaiter(sessionID string) bool {
	s.waitersMu.Lock()
	defer s.waitersMu.Unlock()
	list, ok := s.waiters[sessionID]
	return ok && len(list) > 0
}

func (s *Server) GetActiveWaiterInfo(sessionID string) (bool, *time.Time, int) {
	s.waitersMu.Lock()
	defer s.waitersMu.Unlock()
	list, ok := s.waiters[sessionID]
	if !ok || len(list) == 0 {
		return false, nil, 0
	}
	latest := list[len(list)-1]
	startedAt := latest.startedAt
	return true, &startedAt, latest.timeoutSec
}

func (s *Server) NotifySessionCompleted(sess *model.FeedbackSession) {
	s.waitersMu.Lock()
	defer s.waitersMu.Unlock()

	if sess == nil {
		return
	}

	s.lastKeepaliveMu.Lock()
	delete(s.lastKeepalives, sess.ID)
	s.lastKeepaliveMu.Unlock()

	if list, ok := s.waiters[sess.ID]; ok {
		for _, item := range list {
			select {
			case item.ch <- sess:
			default:
			}
		}
	}
	s.notifySessionUpdate(sess.ID)
}

func (s *Server) notifySessionUpdate(sessionID string) {
	if s.onUpdate != nil {
		s.onUpdate("session_update", map[string]string{"session_id": sessionID})
	}
}

func (s *Server) notifyTaskUpdate(taskID string) {
	if s.onUpdate != nil {
		s.onUpdate("task_update", map[string]string{"task_id": taskID})
	}
}

// HTTP JSON-RPC 2.0 Handler
type jsonRPCRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      any             `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type jsonRPCResponse struct {
	JSONRPC string      `json:"jsonrpc"`
	ID      any         `json:"id"`
	Result  any         `json:"result,omitempty"`
	Error   *rpcError   `json:"error,omitempty"`
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

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		// Server Info / Health
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"name":    "RelayMesh Streamable MCP Server",
			"version": "1.0.0",
			"status":  "running",
		})
		return
	}

	if r.Method != http.MethodPost {
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}

	// 提取 Token（优先级：1. Authorization Header -> 2. URL Query ?token=xxx -> 3. URL Path /mcp/token/xxx 或 /mcp/xxx）
	tokenString := ""
	authHeader := r.Header.Get("Authorization")
	if authHeader != "" && strings.HasPrefix(authHeader, "Bearer ") {
		tokenString = strings.TrimPrefix(authHeader, "Bearer ")
	} else if authHeader != "" {
		tokenString = authHeader
	}
	if tokenString == "" {
		tokenString = r.URL.Query().Get("token")
	}
	if tokenString == "" {
		// 支持 Path 路径嵌入 Token，如 /mcp/token/xxx 或 /mcp/xxx
		path := strings.TrimPrefix(r.URL.Path, "/mcp")
		path = strings.Trim(path, "/")
		if path != "" {
			if strings.HasPrefix(path, "token/") {
				tokenString = strings.TrimPrefix(path, "token/")
			} else if !strings.Contains(path, "/") {
				tokenString = path
			}
		}
	}

	role := "all"
	// 角色权限判定与认证校验
	if s.cfg.ConfigureToken != "" && tokenString == s.cfg.ConfigureToken {
		role = "configure"
	} else if s.cfg.ExecutionToken != "" && tokenString == s.cfg.ExecutionToken {
		role = "execute"
	} else if s.cfg.MCPToken != "" {
		if tokenString == s.cfg.MCPToken {
			role = "all"
		} else {
			// 配置了全局 MCP Token 但未提供有效 Token -> 拒绝访问
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			json.NewEncoder(w).Encode(jsonRPCResponse{
				JSONRPC: "2.0",
				ID:      nil,
				Error:   &rpcError{Code: -32000, Message: "Unauthorized: invalid or missing MCP token"},
			})
			return
		}
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "Bad Request", http.StatusBadRequest)
		return
	}

	var req jsonRPCRequest
	if err := json.Unmarshal(body, &req); err != nil {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(jsonRPCResponse{
			JSONRPC: "2.0",
			ID:      nil,
			Error:   &rpcError{Code: -32700, Message: "Parse error"},
		})
		return
	}

	res := s.handleRPC(r.Context(), role, req)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(res)
}

func (s *Server) handleRPC(ctx context.Context, role string, req jsonRPCRequest) jsonRPCResponse {
	resp := jsonRPCResponse{
		JSONRPC: "2.0",
		ID:      req.ID,
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
				"version": "1.0.0",
			},
		}

	case "notifications/initialized":
		resp.Result = map[string]any{}

	case "tools/list":
		tools := GetToolDefinitions(role)
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
			return resp
		}

		res, isErr, err := s.dispatchTool(ctx, role, callParams.Name, callParams.Arguments)
		if err != nil {
			resp.Result = toolCallResult{
				Content: []mcpContentItem{
					{Type: "text", Text: fmt.Sprintf("Error: %v", err)},
				},
				IsError: true,
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

	return resp
}

func (s *Server) dispatchTool(ctx context.Context, role, toolName string, args json.RawMessage) (any, bool, error) {
	// Role check
	if role == "configure" && toolName != "configure_task" {
		return nil, true, fmt.Errorf("tool %q not allowed for configure token", toolName)
	}
	if role == "execute" && toolName != "report_progress" {
		return nil, true, fmt.Errorf("tool %q not allowed for execution token", toolName)
	}

	switch toolName {
	case "configure_task":
		res, err := s.handleConfigureTask(ctx, args)
		return res, err != nil, err
	case "report_progress":
		res, err := s.handleReportProgress(ctx, args)
		return res, err != nil, err
	case "interactive_feedback":
		res, err := s.handleInteractiveFeedback(ctx, args)
		return res, err != nil, err
	case "continue_feedback_session":
		res, err := s.handleContinueFeedbackSession(ctx, args)
		return res, err != nil, err
	case "list_sessions":
		res, err := s.handleListSessions(ctx, args)
		return res, err != nil, err
	case "get_session_history":
		res, err := s.handleGetSessionHistory(ctx, args)
		return res, err != nil, err
	case "get_system_info":
		res, err := s.handleGetSystemInfo(ctx, args)
		return res, err != nil, err
	default:
		return nil, true, fmt.Errorf("unknown tool: %s", toolName)
	}
}
