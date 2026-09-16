package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/teacat99/RelayMesh/internal/config"
	"github.com/teacat99/RelayMesh/internal/model"
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
		WaitCountdownMinutes:   0,
	}
	st, err := store.New(":memory:")
	if err != nil {
		t.Fatalf("failed to create memory store: %v", err)
	}
	_ = st.SaveSettings(context.Background(), map[string]any{
		"defaultWaitCountdownMinutes": 0,
	})
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
	if len(toolsList) != 10 {
		t.Fatalf("expected 10 tools, got %d", len(toolsList))
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

	// 3.1 模拟重复提取已消费会话：再次调用 continue_feedback_session 应该返回 Conflict 报错，杜绝重复反馈信息
	repeatReq := `{
		"jsonrpc": "2.0",
		"id": 222,
		"method": "tools/call",
		"params": {
			"name": "continue_feedback_session",
			"arguments": {
				"session_id": "test-early-cached-sess"
			}
		}
	}`
	req = httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewBufferString(repeatReq))
	w = httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	json.Unmarshal(w.Body.Bytes(), &resp)
	resultMap = resp.Result.(map[string]any)
	if isErr, _ := resultMap["isError"].(bool); !isErr {
		t.Fatalf("expected isError=true for repeat continue_feedback_session on consumed session, got: %+v", resultMap)
	}
	contentList = resultMap["content"].([]any)
	firstItem = contentList[0].(map[string]any)
	errText := firstItem["text"].(string)
	if !strings.Contains(errText, "already been completed and consumed by AI") {
		t.Fatalf("expected conflict error message, got: %s", errText)
	}

	// 4. 模拟“用户主动取消会话”场景
	cancelSession, _ := srv.store.CreateFeedbackSession(context.Background(), store.CreateSessionInput{
		SessionID:      "test-cancelled-sess",
		WorkflowID:     "wf-test-cancel",
		Summary:        "测试取消",
		TimeoutSeconds: 1,
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

func TestMCPServer_URLHostnameOverride(t *testing.T) {
	st, err := store.New(":memory:")
	if err != nil {
		t.Fatalf("failed to create in-memory store: %v", err)
	}

	cfg := &config.Config{
		MCPToken: "token-host-test",
		HostName: "server-default-host",
	}

	srv := NewServer(cfg, st, nil)

	// 调用 interactive_feedback 工具，且 URL 携带 ?token=token-host-test&hostname=wsl-box
	callBody := `{
		"jsonrpc": "2.0",
		"id": 1,
		"method": "tools/call",
		"params": {
			"name": "interactive_feedback",
			"arguments": {
				"workflow_id": "wf-host-test",
				"summary": "Testing hostname query param override"
			}
		}
	}`

	// 先往该 workflow 注入一个 queued feedback 秒回，防止挂起等待
	_, _ = st.QueueWorkflowFeedback(context.Background(), store.QueueFeedbackInput{
		WorkflowID:   "wf-host-test",
		ResponseText: "auto reply",
	})

	req := httptest.NewRequest(http.MethodPost, "/mcp?token=token-host-test&hostname=wsl-box", bytes.NewBufferString(callBody))
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d: %s", w.Code, w.Body.String())
	}

	// 验证存储在 DB 中的 session 其 host_name 字段被正确赋予了 wsl-box，而非服务端的 server-default-host
	sessions, err := st.ListFeedbackSessions(context.Background(), "", "", 10)
	if err != nil || len(sessions) == 0 {
		t.Fatalf("failed to query created session: %v", err)
	}

	if sessions[0].HostName != "wsl-box" {
		t.Fatalf("expected HostName to be 'wsl-box', got %q", sessions[0].HostName)
	}
}

func TestMCPServer_GetSessionImage(t *testing.T) {
	st, err := store.New(":memory:")
	if err != nil {
		t.Fatalf("failed to create test store: %v", err)
	}

	cfg := &config.Config{
		MCPToken: "test-image-token",
		HostName: "localhost",
		Port:     18775,
	}

	srv := NewServer(cfg, st, nil)

	// 1. 创建包含图片的会话
	sess, err := st.CreateFeedbackSession(context.Background(), store.CreateSessionInput{
		WorkflowID: "wf-img-test",
		Title:      "Image Test",
		Summary:    "Summary with image",
	})
	if err != nil {
		t.Fatalf("failed to create session: %v", err)
	}

	// 注入图片
	testBase64 := "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNk+M9QDwADhgGAWjR9awAAAABJRU5ErkJggg=="
	_, err = st.SubmitFeedback(context.Background(), store.SubmitFeedbackInput{
		SessionID:    sess.ID,
		ResponseText: "with image",
		Images: []model.SessionImage{
			{
				Name:   "pixel.png",
				Format: "png",
				Data:   testBase64,
			},
		},
	})
	if err != nil {
		t.Fatalf("failed to submit feedback with image: %v", err)
	}

	// 2. 测试 output_mode: "image" 原生视觉模式
	callBodyImage := fmt.Sprintf(`{
		"jsonrpc": "2.0",
		"id": 1,
		"method": "tools/call",
		"params": {
			"name": "get_session_image",
			"arguments": {
				"session_id": "%s",
				"image_index": 0,
				"output_mode": "image"
			}
		}
	}`, sess.ID)

	req := httptest.NewRequest(http.MethodPost, "/mcp?token=test-image-token", bytes.NewBufferString(callBodyImage))
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp jsonRPCResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to unmarshal rpc response: %v", err)
	}
	if resp.Error != nil {
		t.Fatalf("unexpected rpc error: %+v", resp.Error)
	}

	resMap, ok := resp.Result.(map[string]any)
	if !ok {
		t.Fatalf("expected map result, got %T", resp.Result)
	}
	contentList, ok := resMap["content"].([]any)
	if !ok || len(contentList) < 2 {
		t.Fatalf("expected at least 2 content items (text + image), got %d", len(contentList))
	}
	imgItem := contentList[1].(map[string]any)
	if imgItem["type"] != "image" {
		t.Fatalf("expected type 'image', got %v", imgItem["type"])
	}
	if imgItem["data"] != testBase64 {
		t.Fatalf("expected data to match base64")
	}

	// 3. 测试 output_mode: "base64" 模式
	callBodyBase64 := fmt.Sprintf(`{
		"jsonrpc": "2.0",
		"id": 2,
		"method": "tools/call",
		"params": {
			"name": "get_session_image",
			"arguments": {
				"session_id": "%s",
				"image_index": 0,
				"output_mode": "base64"
			}
		}
	}`, sess.ID)

	req = httptest.NewRequest(http.MethodPost, "/mcp?token=test-image-token", bytes.NewBufferString(callBodyBase64))
	w = httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp2 jsonRPCResponse
	json.Unmarshal(w.Body.Bytes(), &resp2)
	resMap2 := resp2.Result.(map[string]any)
	contentList2 := resMap2["content"].([]any)
	textItem := contentList2[0].(map[string]any)
	var base64Result map[string]any
	if err := json.Unmarshal([]byte(textItem["text"].(string)), &base64Result); err != nil {
		t.Fatalf("failed to parse base64 json result: %v", err)
	}
	if base64Result["name"] != "pixel.png" || base64Result["base64_data"] != testBase64 {
		t.Fatalf("unexpected base64 result: %+v", base64Result)
	}

	// 4. 测试 formatSessionImagesBlock 中统一复用 BuildSessionImageRelativeURL 的直链生成
	updatedSess, _ := st.GetFeedbackSession(context.Background(), sess.ID)
	imgSection := srv.formatSessionImagesBlock(updatedSess, nil, false)
	if !strings.Contains(imgSection, "/api/v1/sessions/"+sess.ID+"/images/0") {
		t.Fatalf("expected formatSessionImagesBlock to contain standard image URL, got: %s", imgSection)
	}
}

func TestMCPServer_InteractiveFeedbackContentPriority(t *testing.T) {
	srv := setupTestMCPServer(t)

	// 测试当客户端同时传入简短 summary 与详尽 content 时，优先选择最长的 content 作为正文
	callReq := `{
		"jsonrpc": "2.0",
		"id": 99,
		"method": "tools/call",
		"params": {
			"name": "interactive_feedback",
			"arguments": {
				"project_directory": "/test/dir",
				"summary": "简短的一句话概括",
				"content": "### 详尽完整的正文汇报内容\n\n1. 详情项一\n2. 详情项二\n包含更多字数与结构",
				"workflow_id": "wf-test-priority"
			}
		}
	}`
	req := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewBufferString(callReq))
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	// 检查存储在 DB 中的 session summary 是否为较长的 content
	sess, err := srv.store.GetLatestWorkflowFeedbackSession(context.Background(), "wf-test-priority")
	if err != nil {
		t.Fatalf("failed to get session: %v", err)
	}
	if !strings.Contains(sess.Summary, "### 详尽完整的正文汇报内容") {
		t.Fatalf("expected session.Summary to pick longer content, got: %s", sess.Summary)
	}

	// 测试智能防御：当客户端将长篇正文误填到 title，而 summary 填入简短一句话时，服务端自动识别对调
	swapReq := `{
		"jsonrpc": "2.0",
		"id": 100,
		"method": "tools/call",
		"params": {
			"name": "interactive_feedback",
			"arguments": {
				"project_directory": "/test/dir",
				"title": "### 长篇正文误填到了标题\n\n这里包含了很长很长的一段详细方案分析与架构推演说明，超过了一百个字符并且详细列举了改动文件与注意事项。",
				"summary": "简短的一句话标题",
				"workflow_id": "wf-test-swap"
			}
		}
	}`
	reqSwap := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewBufferString(swapReq))
	wSwap := httptest.NewRecorder()
	srv.ServeHTTP(wSwap, reqSwap)

	swapSess, err := srv.store.GetLatestWorkflowFeedbackSession(context.Background(), "wf-test-swap")
	if err != nil {
		t.Fatalf("failed to get swapped session: %v", err)
	}
	if !strings.Contains(swapSess.Summary, "### 长篇正文误填到了标题") {
		t.Fatalf("expected session.Summary to be swapped from title, got: %s", swapSess.Summary)
	}
	if swapSess.Title != "简短的一句话标题" {
		t.Fatalf("expected session.Title to be swapped from summary, got: %s", swapSess.Title)
	}
}

func TestMCPServer_ConsumedAtAndPhaseGuard(t *testing.T) {
	srv := setupTestMCPServer(t)
	ctx := context.Background()

	// 1. 创建会话并在 dev 阶段
	sess, err := srv.store.CreateFeedbackSession(ctx, store.CreateSessionInput{
		WorkflowID:       "wf-phase-guard-test",
		ProjectDirectory: "/test",
		Title:            "Phase Guard Test",
		Summary:          "Checking phase guard",
	})
	if err != nil {
		t.Fatalf("failed to create session: %v", err)
	}
	_ = srv.store.SetWorkflowPhase(ctx, "wf-phase-guard-test", "dev", false)

	// 提交反馈
	_, err = srv.store.SubmitFeedback(ctx, store.SubmitFeedbackInput{
		SessionID:    sess.ID,
		ResponseText: "User says ok",
	})
	if err != nil {
		t.Fatalf("failed to submit feedback: %v", err)
	}

	// 此时 MCP 消费反馈
	updatedSess, err := srv.store.GetFeedbackSession(ctx, sess.ID)
	if err != nil {
		t.Fatalf("failed to get session: %v", err)
	}
	srv.markConsumedAndBroadcast(ctx, updatedSess)

	// 验证 ConsumedAt 与 ConsumedByAI
	if !updatedSess.ConsumedByAI {
		t.Fatalf("expected ConsumedByAI to be true")
	}
	if updatedSess.ConsumedAt == nil {
		t.Fatalf("expected ConsumedAt to be non-nil")
	}

	// 验证处于 dev 阶段时不应该被重置为 assess
	currentPhase, _, _ := srv.store.GetWorkflowPhaseWithDefaults(ctx, "wf-phase-guard-test")
	if currentPhase != "dev" {
		t.Fatalf("expected phase to remain 'dev', got %q", currentPhase)
	}

	// 2. 将阶段设为 done 并再次消费新轮次
	_ = srv.store.SetWorkflowPhase(ctx, "wf-phase-guard-test", "done", false)
	sess2, _ := srv.store.CreateFeedbackSession(ctx, store.CreateSessionInput{
		WorkflowID:       "wf-phase-guard-test",
		ProjectDirectory: "/test",
		Title:            "Phase Guard Test 2",
		Summary:          "Checking done to assess transition",
	})
	_, _ = srv.store.SubmitFeedback(ctx, store.SubmitFeedbackInput{
		SessionID:    sess2.ID,
		ResponseText: "Cycle 2 user input",
	})
	updatedSess2, _ := srv.store.GetFeedbackSession(ctx, sess2.ID)
	srv.markConsumedAndBroadcast(ctx, updatedSess2)

	// 处于 done 阶段时，消费后应该自动流转回 assess
	currentPhase2, _, _ := srv.store.GetWorkflowPhaseWithDefaults(ctx, "wf-phase-guard-test")
	if currentPhase2 != "assess" {
		t.Fatalf("expected phase to reset to 'assess' from 'done', got %q", currentPhase2)
	}
}

func TestMCPServer_WorkflowContextPermission(t *testing.T) {
	srv := setupTestMCPServer(t)
	ctx := context.Background()

	// 创建带 feedback 权限的 credential
	cred := &model.MCPCredential{
		Name:     "test-agent",
		Token:    "test-token-12345",
		IsActive: true,
		Permissions: model.Permissions{
			Feedback: true,
		},
	}
	if err := srv.store.CreateCredential(ctx, cred); err != nil {
		t.Fatalf("failed to create credential: %v", err)
	}

	callReq := `{
		"jsonrpc": "2.0",
		"id": 101,
		"method": "tools/call",
		"params": {
			"name": "workflow_context",
			"arguments": {
				"action": "list_workflows"
			}
		}
	}`
	req := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewBufferString(callReq))
	req.Header.Set("Authorization", "Bearer "+cred.Token)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	var resp jsonRPCResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}
	if resp.Error != nil {
		t.Fatalf("unexpected error: %v", resp.Error)
	}
}

// TestMCPServer_AgentModesBuiltinNorm verifies that agent-modes is seeded automatically
// and accessible via manage_skills(action: "get", name: "agent-modes").
func TestMCPServer_AgentModesBuiltinNorm(t *testing.T) {
	srv := setupTestMCPServer(t)
	ctx := context.Background()

	cred := &model.MCPCredential{
		Name:     "test-agent-skills",
		Token:    "test-token-skills-12345",
		IsActive: true,
		Permissions: model.Permissions{
			Skills:   true,
			Feedback: true,
		},
	}
	if err := srv.store.CreateCredential(ctx, cred); err != nil {
		t.Fatalf("failed to create credential: %v", err)
	}

	callReq := `{
		"jsonrpc": "2.0",
		"id": 201,
		"method": "tools/call",
		"params": {
			"name": "manage_skills",
			"arguments": {
				"action": "get",
				"name": "agent-modes"
			}
		}
	}`
	req := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewBufferString(callReq))
	req.Header.Set("Authorization", "Bearer "+cred.Token)
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
		t.Fatalf("expected map result, got %T", resp.Result)
	}
	contentList, ok := resultMap["content"].([]any)
	if !ok || len(contentList) == 0 {
		t.Fatalf("expected non-empty content in result, got: %+v", resultMap)
	}
	firstItem := contentList[0].(map[string]any)
	resultStr := firstItem["text"].(string)

	var normData map[string]any
	if err := json.Unmarshal([]byte(resultStr), &normData); err != nil {
		t.Fatalf("failed to parse normData JSON: %v", err)
	}
	if normData["name"] != "agent-modes" {
		t.Fatalf("expected norm name 'agent-modes', got %v", normData["name"])
	}
	contentStr, _ := normData["content"].(string)
	if !strings.Contains(contentStr, "agent-modes · 会话场景模式") {
		t.Fatalf("expected content to contain agent-modes header, got: %s", contentStr)
	}
}

// TestMCPServer_WorkflowSheet_SessionDocSaveAndGet verifies saving and getting session_doc
// via workflow_context tool.
func TestMCPServer_WorkflowSheet_SessionDocSaveAndGet(t *testing.T) {
	srv := setupTestMCPServer(t)
	ctx := context.Background()

	cred := &model.MCPCredential{
		Name:     "test-agent-context",
		Token:    "test-token-context-12345",
		IsActive: true,
		Permissions: model.Permissions{
			Feedback: true,
		},
	}
	if err := srv.store.CreateCredential(ctx, cred); err != nil {
		t.Fatalf("failed to create credential: %v", err)
	}

	// 1. Save session_doc
	saveReqStr := `{
		"jsonrpc": "2.0",
		"id": 301,
		"method": "tools/call",
		"params": {
			"name": "workflow_context",
			"arguments": {
				"action": "session_doc_save",
				"workflow_id": "wf-sheet-mcp-test",
				"content": "# MCP Test Sheet\n- Goal: test session_doc_save"
			}
		}
	}`
	reqSave := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewBufferString(saveReqStr))
	reqSave.Header.Set("Authorization", "Bearer "+cred.Token)
	wSave := httptest.NewRecorder()
	srv.ServeHTTP(wSave, reqSave)

	var respSave jsonRPCResponse
	if err := json.Unmarshal(wSave.Body.Bytes(), &respSave); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}
	if respSave.Error != nil {
		t.Fatalf("unexpected error on session_doc_save: %v", respSave.Error)
	}

	// 2. Get session_doc
	getReqStr := `{
		"jsonrpc": "2.0",
		"id": 302,
		"method": "tools/call",
		"params": {
			"name": "workflow_context",
			"arguments": {
				"action": "session_doc_get",
				"workflow_id": "wf-sheet-mcp-test"
			}
		}
	}`
	reqGet := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewBufferString(getReqStr))
	reqGet.Header.Set("Authorization", "Bearer "+cred.Token)
	wGet := httptest.NewRecorder()
	srv.ServeHTTP(wGet, reqGet)

	var respGet jsonRPCResponse
	if err := json.Unmarshal(wGet.Body.Bytes(), &respGet); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}
	if respGet.Error != nil {
		t.Fatalf("unexpected error on session_doc_get: %v", respGet.Error)
	}
	resultMapGet, ok := respGet.Result.(map[string]any)
	if !ok {
		t.Fatalf("expected map result, got %T", respGet.Result)
	}
	contentListGet, ok := resultMapGet["content"].([]any)
	if !ok || len(contentListGet) == 0 {
		t.Fatalf("expected non-empty content in get result, got: %+v", resultMapGet)
	}
	firstItemGet := contentListGet[0].(map[string]any)
	resultStrGet := firstItemGet["text"].(string)

	var getMap map[string]any
	if err := json.Unmarshal([]byte(resultStrGet), &getMap); err != nil {
		t.Fatalf("failed to parse get result JSON: %v", err)
	}
	noteMap, ok := getMap["note"].(map[string]any)
	if !ok {
		t.Fatalf("expected note object, got %v", getMap["note"])
	}
	if noteMap["note_key"] != "session_doc" {
		t.Fatalf("expected note_key 'session_doc', got %v", noteMap["note_key"])
	}
	if !strings.Contains(noteMap["content"].(string), "MCP Test Sheet") {
		t.Fatalf("unexpected content: %v", noteMap["content"])
	}
}

// TestMCPServer_ServerInstructions_ContentCheck verifies that DefaultServerInstructions
// contains agent-modes norm notice and workflow sheet adaptive policy.
func TestMCPServer_ServerInstructions_ContentCheck(t *testing.T) {
	if !strings.Contains(store.DefaultServerInstructions, "agent-modes") {
		t.Fatalf("expected DefaultServerInstructions to contain agent-modes reference")
	}
	if !strings.Contains(store.DefaultServerInstructions, "Workflow Sheet") {
		t.Fatalf("expected DefaultServerInstructions to contain Workflow Sheet reference")
	}
	if !strings.Contains(store.DefaultServerInstructions, "session_doc_save") {
		t.Fatalf("expected DefaultServerInstructions to contain session_doc_save reference")
	}
	if !strings.Contains(store.DefaultServerInstructions, "D-165") {
		t.Fatalf("expected DefaultServerInstructions to contain D-165 context boundary reference")
	}
}

func TestMCPServer_D165_ListSessionsScopingAndSecurityTrimming(t *testing.T) {
	srv := setupTestMCPServer(t)
	ctx := context.Background()

	cred := &model.MCPCredential{
		Name:     "test-agent-d165",
		Token:    "test-token-d165",
		HostName: "test-host-x",
		IsActive: true,
		Permissions: model.Permissions{
			Sessions: true,
			Feedback: true,
		},
	}
	if err := srv.store.CreateCredential(ctx, cred); err != nil {
		t.Fatalf("failed to create credential: %v", err)
	}

	// 1. 创建 Project A 和 Project B 的两个会话
	_, err := srv.store.CreateFeedbackSession(ctx, store.CreateSessionInput{
		WorkflowID:       "wf-proj-a",
		ProjectDirectory: "/workspace/project-a",
		EnvHostName:      "test-host-x",
		Title:            "Project A Title",
		Summary:          "Project A Summary",
	})
	if err != nil {
		t.Fatalf("failed to create session A: %v", err)
	}

	_, err = srv.store.CreateFeedbackSession(ctx, store.CreateSessionInput{
		WorkflowID:       "wf-proj-b",
		ProjectDirectory: "/workspace/project-b",
		EnvHostName:      "test-host-x",
		Title:            "Project B Title",
		Summary:          "Project B Summary",
	})
	if err != nil {
		t.Fatalf("failed to create session B: %v", err)
	}

	// 2. 调用 list_sessions({})，未传 project_directory 且未传 workflow_id，应被直接拦截拒绝
	reqNoDir := `{
		"jsonrpc": "2.0",
		"id": 201,
		"method": "tools/call",
		"params": {
			"name": "list_sessions",
			"arguments": {}
		}
	}`
	httpReq1 := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewBufferString(reqNoDir))
	httpReq1.Header.Set("Authorization", "Bearer "+cred.Token)
	w1 := httptest.NewRecorder()
	srv.ServeHTTP(w1, httpReq1)

	var resp1 jsonRPCResponse
	_ = json.Unmarshal(w1.Body.Bytes(), &resp1)
	resMap1, ok := resp1.Result.(map[string]any)
	if !ok {
		t.Fatalf("expected map result, got %T (body: %s)", resp1.Result, w1.Body.String())
	}
	if isErr, _ := resMap1["isError"].(bool); !isErr {
		t.Fatalf("expected isError=true when calling list_sessions without project_directory or workflow_id, got: %+v", resMap1)
	}

	// 3. 调用 list_sessions 并传入 project_directory: "/workspace/project-a"
	reqWithDir := `{
		"jsonrpc": "2.0",
		"id": 202,
		"method": "tools/call",
		"params": {
			"name": "list_sessions",
			"arguments": {
				"project_directory": "/workspace/project-a"
			}
		}
	}`
	httpReq2 := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewBufferString(reqWithDir))
	httpReq2.Header.Set("Authorization", "Bearer "+cred.Token)
	w2 := httptest.NewRecorder()
	srv.ServeHTTP(w2, httpReq2)

	var resp2 jsonRPCResponse
	_ = json.Unmarshal(w2.Body.Bytes(), &resp2)
	if resp2.Error != nil {
		t.Fatalf("unexpected error: %v", resp2.Error)
	}
	resMap2 := resp2.Result.(map[string]any)
	contentList2 := resMap2["content"].([]any)
	firstItem2 := contentList2[0].(map[string]any)
	var listResult2 map[string]any
	_ = json.Unmarshal([]byte(firstItem2["text"].(string)), &listResult2)

	sessList2 := listResult2["sessions"].([]any)
	if len(sessList2) != 1 {
		t.Fatalf("expected 1 session for project-a, got %d", len(sessList2))
	}
	item0 := sessList2[0].(map[string]any)
	if item0["workflow_id"] != "wf-proj-a" {
		t.Fatalf("expected wf-proj-a, got %v", item0["workflow_id"])
	}
	// 验证暴露面剪裁：list_sessions 严禁泄漏 response_text 和 user_messages
	if _, exists := item0["response_text"]; exists {
		t.Fatalf("security violation: response_text should not be exposed in list_sessions")
	}
	if _, exists := item0["user_messages"]; exists {
		t.Fatalf("security violation: user_messages should not be exposed in list_sessions")
	}

	// 4. 人工显式指定 workflow_id 破例穿透测试：传入 workflow_id: "wf-proj-b"（无需 project_directory）
	reqExplicitWf := `{
		"jsonrpc": "2.0",
		"id": 203,
		"method": "tools/call",
		"params": {
			"name": "list_sessions",
			"arguments": {
				"workflow_id": "wf-proj-b"
			}
		}
	}`
	httpReq3 := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewBufferString(reqExplicitWf))
	httpReq3.Header.Set("Authorization", "Bearer "+cred.Token)
	w3 := httptest.NewRecorder()
	srv.ServeHTTP(w3, httpReq3)

	var resp3 jsonRPCResponse
	_ = json.Unmarshal(w3.Body.Bytes(), &resp3)
	if resp3.Error != nil {
		t.Fatalf("unexpected error on explicit workflow_id: %v", resp3.Error)
	}

	// 5. get_session_history 获取指定 workflow_id 全文
	reqHist := `{
		"jsonrpc": "2.0",
		"id": 204,
		"method": "tools/call",
		"params": {
			"name": "get_session_history",
			"arguments": {
				"workflow_id": "wf-proj-b"
			}
		}
	}`
	httpReq4 := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewBufferString(reqHist))
	httpReq4.Header.Set("Authorization", "Bearer "+cred.Token)
	w4 := httptest.NewRecorder()
	srv.ServeHTTP(w4, httpReq4)

	var resp4 jsonRPCResponse
	_ = json.Unmarshal(w4.Body.Bytes(), &resp4)
	if resp4.Error != nil {
		t.Fatalf("unexpected error on get_session_history: %v", resp4.Error)
	}
	resMap4 := resp4.Result.(map[string]any)
	contentList4 := resMap4["content"].([]any)
	firstItem4 := contentList4[0].(map[string]any)
	var histResult4 map[string]any
	_ = json.Unmarshal([]byte(firstItem4["text"].(string)), &histResult4)
	if histResult4["workflow_id"] != "wf-proj-b" {
		t.Fatalf("expected wf-proj-b history, got %v", histResult4["workflow_id"])
	}

	// 6. workflow_context list_workflows 隔离测试：传入 project_directory: "/workspace/project-a"
	reqListWf := `{
		"jsonrpc": "2.0",
		"id": 205,
		"method": "tools/call",
		"params": {
			"name": "workflow_context",
			"arguments": {
				"action": "list_workflows",
				"project_directory": "/workspace/project-a"
			}
		}
	}`
	httpReq5 := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewBufferString(reqListWf))
	httpReq5.Header.Set("Authorization", "Bearer "+cred.Token)
	w5 := httptest.NewRecorder()
	srv.ServeHTTP(w5, httpReq5)

	var resp5 jsonRPCResponse
	_ = json.Unmarshal(w5.Body.Bytes(), &resp5)
	if resp5.Error != nil {
		t.Fatalf("unexpected error on list_workflows: %v", resp5.Error)
	}
	resMap5 := resp5.Result.(map[string]any)
	contentList5 := resMap5["content"].([]any)
	firstItem5 := contentList5[0].(map[string]any)
	var wfResult5 map[string]any
	_ = json.Unmarshal([]byte(firstItem5["text"].(string)), &wfResult5)
	wfList5 := wfResult5["workflows"].([]any)
	if len(wfList5) != 1 {
		t.Fatalf("expected 1 workflow for project-a, got %d", len(wfList5))
	}
	wfItem0 := wfList5[0].(map[string]any)
	if wfItem0["workflow_id"] != "wf-proj-a" {
		t.Fatalf("expected wf-proj-a, got %v", wfItem0["workflow_id"])
	}
}

