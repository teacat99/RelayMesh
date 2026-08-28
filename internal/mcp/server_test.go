package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/teacat99/RelayMesh/internal/config"
	"github.com/teacat99/RelayMesh/internal/store"
)

func setupTestMCPServer(t *testing.T) *Server {
	cfg := &config.Config{
		ProjectID:              "test-proj",
		ConfigureToken:         "cfg-token-123456",
		ExecutionToken:         "exec-token-123456",
		FeedbackTimeoutSeconds: 1,
		WaitAfterMinutes:       5,
		MaxNoFeedbackChecks:    3,
	}
	st, err := store.New(":memory:")
	if err != nil {
		t.Fatalf("failed to create memory store: %v", err)
	}
	return NewServer(cfg, st, nil)
}

func TestMCPServer_InitializeAndToolsList(t *testing.T) {
	srv := setupTestMCPServer(t)

	// 1. Initialize
	reqBody := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`
	req := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewBufferString(reqBody))
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	var initResp jsonRPCResponse
	if err := json.Unmarshal(w.Body.Bytes(), &initResp); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}
	if initResp.Error != nil {
		t.Fatalf("unexpected error: %v", initResp.Error)
	}

	// 2. Tools list (all tools)
	reqBody = `{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}`
	req = httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewBufferString(reqBody))
	w = httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	var toolsResp jsonRPCResponse
	if err := json.Unmarshal(w.Body.Bytes(), &toolsResp); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}
	resMap := toolsResp.Result.(map[string]any)
	toolsList := resMap["tools"].([]any)
	if len(toolsList) != 7 {
		t.Fatalf("expected 7 tools, got %d", len(toolsList))
	}

	// 3. Configure token tools list
	req = httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewBufferString(reqBody))
	req.Header.Set("Authorization", "Bearer cfg-token-123456")
	w = httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	json.Unmarshal(w.Body.Bytes(), &toolsResp)
	resMap = toolsResp.Result.(map[string]any)
	toolsList = resMap["tools"].([]any)
	if len(toolsList) != 1 {
		t.Fatalf("expected 1 tool for configure token, got %d", len(toolsList))
	}
}

func TestMCPServer_ConfigureTaskCreate(t *testing.T) {
	srv := setupTestMCPServer(t)

	callReq := `{
		"jsonrpc": "2.0",
		"id": 10,
		"method": "tools/call",
		"params": {
			"name": "configure_task",
			"arguments": {
				"action": "create",
				"task_id": "test-mcp-01",
				"segments": [
					{"name": "rules", "content": "Rule 1 content"}
				]
			}
		}
	}`
	req := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewBufferString(callReq))
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	var resp jsonRPCResponse
	json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.Error != nil {
		t.Fatalf("unexpected error: %v", resp.Error)
	}
}

func TestMCPServer_FeedbackSessionFlow(t *testing.T) {
	srv := setupTestMCPServer(t)

	// 1. 创建反馈会话 (1 秒超时用于测试)
	callReq := `{
		"jsonrpc": "2.0",
		"id": 20,
		"method": "tools/call",
		"params": {
			"name": "interactive_feedback",
			"arguments": {
				"project_directory": "/test/dir",
				"summary": "请确认重构方案",
				"timeout": 1
			}
		}
	}`
	req := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewBufferString(callReq))
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	var resp jsonRPCResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}
	if resp.Error != nil {
		t.Fatalf("unexpected error: %v", resp.Error)
	}

	resultMap, ok := resp.Result.(map[string]any)
	if !ok {
		t.Fatalf("expected map[string]any result, got %T", resp.Result)
	}

	contentList, ok := resultMap["content"].([]any)
	if !ok || len(contentList) == 0 {
		t.Fatalf("expected non-empty content in result, got: %+v", resultMap)
	}

	firstItem := contentList[0].(map[string]any)
	resultStr := firstItem["text"].(string)

	// 校验返回的是否包含统一 session_id 头与 === 等待回执 ===
	if !strings.Contains(resultStr, "session_id:") || !strings.Contains(resultStr, "=== 等待回执 ===") || !strings.Contains(resultStr, "continue_feedback_session") {
		t.Fatalf("expected wait poll prompt with unified format in result, got: %s", resultStr)
	}

	// 2. 模拟调用 continue_feedback_session 达到最大上限，触发超时回执
	sess, err := srv.store.GetFeedbackSession(context.Background(), "test-sess-timeout")
	if err == nil && sess != nil {
		// ignored
	}
	// 创建一个已达上限的 session 直接验证超时回执
	timeoutSession, _ := srv.store.CreateFeedbackSession(context.Background(), store.CreateSessionInput{
		SessionID:      "test-exhausted-sess",
		Summary:        "测试超时",
		TimeoutSeconds: 1,
	})
	srv.store.UpdateSessionMaxChecks(context.Background(), timeoutSession.ID, 1)
	// 触发一次 keepalive 增加 checks 计数
	srv.store.KeepaliveFeedbackSession(context.Background(), timeoutSession.ID, 1)

	contReq := `{
		"jsonrpc": "2.0",
		"id": 21,
		"method": "tools/call",
		"params": {
			"name": "continue_feedback_session",
			"arguments": {
				"session_id": "test-exhausted-sess",
				"timeout": 1
			}
		}
	}`
	req = httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewBufferString(contReq))
	w = httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	json.Unmarshal(w.Body.Bytes(), &resp)
	resultMap = resp.Result.(map[string]any)
	contentList = resultMap["content"].([]any)
	firstItem = contentList[0].(map[string]any)
	timeoutStr := firstItem["text"].(string)

	if !strings.Contains(timeoutStr, "=== 反馈超时 ===") || !strings.Contains(timeoutStr, "用户反馈已超时") {
		t.Fatalf("expected exhausted timeout prompt in result, got: %s", timeoutStr)
	}

	// 3. 模拟“提前反馈缓存”场景：用户在 AI sleep 等待期间提前提交了反馈，AI 调用 continue_feedback_session 时立即秒级命中返回
	earlySession, _ := srv.store.CreateFeedbackSession(context.Background(), store.CreateSessionInput{
		SessionID:      "test-early-cached-sess",
		WorkflowID:     "wf-test-01",
		Summary:        "测试提前反馈缓存",
		TimeoutSeconds: 1,
	})
	// 用户提前通过 Web 端调用 SubmitFeedback
	srv.store.SubmitFeedback(context.Background(), store.SubmitFeedbackInput{
		SessionID:    earlySession.ID,
		ResponseText: "用户提前批准通过！无需等待！",
	})

	earlyReq := `{
		"jsonrpc": "2.0",
		"id": 22,
		"method": "tools/call",
		"params": {
			"name": "continue_feedback_session",
			"arguments": {
				"session_id": "test-early-cached-sess",
				"timeout": 120
			}
		}
	}`
	req = httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewBufferString(earlyReq))
	w = httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	json.Unmarshal(w.Body.Bytes(), &resp)
	resultMap = resp.Result.(map[string]any)
	contentList = resultMap["content"].([]any)
	firstItem = contentList[0].(map[string]any)
	cachedResultStr := firstItem["text"].(string)

	if !strings.Contains(cachedResultStr, "session_id: test-early-cached-sess, workflow_id: wf-test-01") ||
		!strings.Contains(cachedResultStr, "=== 用户反馈 ===") ||
		!strings.Contains(cachedResultStr, "用户提前批准通过！无需等待！") {
		t.Fatalf("expected early cached response with unified header, got: %s", cachedResultStr)
	}

	// 4. 模拟“用户主动取消会话”场景
	cancelSession, _ := srv.store.CreateFeedbackSession(context.Background(), store.CreateSessionInput{
		SessionID:      "test-cancelled-sess",
		WorkflowID:     "wf-test-cancel",
		Summary:        "测试取消",
		TimeoutSeconds: 120,
	})
	srv.store.CancelFeedbackSession(context.Background(), cancelSession.ID)

	cancelReq := `{
		"jsonrpc": "2.0",
		"id": 23,
		"method": "tools/call",
		"params": {
			"name": "continue_feedback_session",
			"arguments": {
				"session_id": "test-cancelled-sess",
				"timeout": 120
			}
		}
	}`
	req = httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewBufferString(cancelReq))
	w = httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	json.Unmarshal(w.Body.Bytes(), &resp)
	resultMap = resp.Result.(map[string]any)
	contentList = resultMap["content"].([]any)
	firstItem = contentList[0].(map[string]any)
	cancelResultStr := firstItem["text"].(string)

	if !strings.Contains(cancelResultStr, "=== 取消反馈 ===") ||
		!strings.Contains(cancelResultStr, "用户已取消当前信息反馈，请重新询问用户的新目标。") {
		t.Fatalf("expected cancelled response with unified format, got: %s", cancelResultStr)
	}
}

func TestMCPServer_GlobalMCPTokenAndQueryParam(t *testing.T) {
	cfg := &config.Config{
		ProjectID: "test-proj-auth",
		MCPToken:  "super-secret-mcp-token-888",
	}
	st, err := store.New(":memory:")
	if err != nil {
		t.Fatalf("failed to create memory store: %v", err)
	}
	srv := NewServer(cfg, st, nil)

	// 1. 未携带 Token 请求 -> 应该返回 401
	reqBody := `{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`
	req := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewBufferString(reqBody))
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 Unauthorized when missing token, got %d", w.Code)
	}

	// 2. 携带错误的 Header Token -> 应该返回 401
	req = httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewBufferString(reqBody))
	req.Header.Set("Authorization", "Bearer wrong-token")
	w = httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 Unauthorized for wrong token, got %d", w.Code)
	}

	// 3. 携带正确的 Header Token -> 应该返回 200 成功
	req = httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewBufferString(reqBody))
	req.Header.Set("Authorization", "Bearer super-secret-mcp-token-888")
	w = httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 OK for valid header token, got %d", w.Code)
	}

	// 4. 通过 URL Query 参数 ?token= 携带正确 Token -> 应该返回 200 成功
	req = httptest.NewRequest(http.MethodPost, "/mcp?token=super-secret-mcp-token-888", bytes.NewBufferString(reqBody))
	w = httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 OK for valid query param token, got %d", w.Code)
	}
}
