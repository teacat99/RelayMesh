package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/teacat99/RelayMesh/internal/config"
	"github.com/teacat99/RelayMesh/internal/mcp"
	"github.com/teacat99/RelayMesh/internal/model"
	"github.com/teacat99/RelayMesh/internal/store"
)

func setupTestAPIServer(t *testing.T) (*Server, *store.Store) {
	cfg := &config.Config{
		ProjectID: "test-proj",
		JWTSecret: "test-secret",
	}
	st, err := store.New(":memory:")
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	srv := NewServer(cfg, st, nil)
	return srv, st
}

func TestAPI_SessionWorkflow(t *testing.T) {
	srv, st := setupTestAPIServer(t)
	ctx := context.Background()

	// 1. Create a session in store
	sess, err := st.CreateFeedbackSession(ctx, store.CreateSessionInput{
		WorkflowID:       "wf-001",
		ProjectDirectory: "/test/dir",
		Title:            "Plan Approval",
		Summary:          "Please confirm implementation.",
		TimeoutSeconds:   600,
	})
	if err != nil {
		t.Fatalf("create session error: %v", err)
	}

	// 2. Query current session via API
	req := httptest.NewRequest(http.MethodGet, "/api/v1/sessions/current", nil)
	w := httptest.NewRecorder()
	srv.Engine().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	var curResp struct {
		HasSession bool `json:"has_session"`
		Session    struct {
			ID string `json:"session_id"`
		} `json:"session"`
	}
	json.Unmarshal(w.Body.Bytes(), &curResp)
	if !curResp.HasSession || curResp.Session.ID != sess.ID {
		t.Fatalf("unexpected current session response: %s", w.Body.String())
	}

	// 3. Submit feedback via API
	submitReq := SubmitFeedbackRequest{
		ResponseText: "Approved! Continue.",
		UserMessages: []string{"Looks solid"},
	}
	body, _ := json.Marshal(submitReq)
	req = httptest.NewRequest(http.MethodPost, "/api/v1/sessions/"+sess.ID+"/submit", bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	srv.Engine().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 for submit, got %d: %s", w.Code, w.Body.String())
	}

	// 4. Verify session is now completed
	updated, err := st.GetFeedbackSession(ctx, sess.ID)
	if err != nil {
		t.Fatalf("get session error: %v", err)
	}
	if updated.Status != "completed" || updated.ResponseText != "Approved! Continue." {
		t.Fatalf("unexpected session status: %+v", updated)
	}
}

func TestAPI_WorkflowDrafts(t *testing.T) {
	srv, _ := setupTestAPIServer(t)

	// 1. Get draft on empty workflow
	req := httptest.NewRequest(http.MethodGet, "/api/v1/workflows/wf-test-draft/drafts", nil)
	w := httptest.NewRecorder()
	srv.Engine().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	// 2. Save draft
	saveBody := `{"active_index":2,"drafts_json":"{\"activeIndex\":2,\"drafts\":[{\"id\":\"1\",\"text\":\"Hello\"}]}"}`
	req = httptest.NewRequest(http.MethodPut, "/api/v1/workflows/wf-test-draft/drafts", bytes.NewBufferString(saveBody))
	req.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	srv.Engine().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 for PUT draft, got %d: %s", w.Code, w.Body.String())
	}

	// 3. Get draft and verify
	req = httptest.NewRequest(http.MethodGet, "/api/v1/workflows/wf-test-draft/drafts", nil)
	w = httptest.NewRecorder()
	srv.Engine().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var getResp struct {
		Draft *struct {
			WorkflowID  string `json:"workflow_id"`
			ActiveIndex int    `json:"active_index"`
			DraftsJSON  string `json:"drafts_json"`
		} `json:"draft"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &getResp); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}
	if getResp.Draft == nil || getResp.Draft.ActiveIndex != 2 {
		t.Fatalf("unexpected draft returned: %+v", getResp)
	}

	// 4. Delete draft
	req = httptest.NewRequest(http.MethodDelete, "/api/v1/workflows/wf-test-draft/drafts", nil)
	w = httptest.NewRecorder()
	srv.Engine().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 for DELETE, got %d", w.Code)
	}
}

func TestAPI_UsernamePasswordAuth(t *testing.T) {
	cfg := &config.Config{
		ProjectID:   "test-proj",
		WebUsername: "teacat_admin",
		WebPassword: "strong_password_999",
		JWTSecret:   "test-jwt-secret-123456",
	}
	st, err := store.New(":memory:")
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	srv := NewServer(cfg, st, nil)

	// 1. 未登录访问受保护 API -> 应该返回 401
	req := httptest.NewRequest(http.MethodGet, "/api/v1/sessions/current", nil)
	w := httptest.NewRecorder()
	srv.Engine().ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 Unauthorized, got %d", w.Code)
	}

	// 2. 错误账号或密码登录 -> 应该返回 401
	loginBody := `{"username":"wrong_user","password":"strong_password_999"}`
	req = httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", bytes.NewBufferString(loginBody))
	req.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	srv.Engine().ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for wrong username, got %d", w.Code)
	}

	loginBody = `{"username":"teacat_admin","password":"wrong_password"}`
	req = httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", bytes.NewBufferString(loginBody))
	req.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	srv.Engine().ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for wrong password, got %d", w.Code)
	}

	// 3. 正确账号与密码登录 -> 应该返回 200 并签发 Token
	loginBody = `{"username":"teacat_admin","password":"strong_password_999"}`
	req = httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", bytes.NewBufferString(loginBody))
	req.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	srv.Engine().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 OK for valid credentials, got %d", w.Code)
	}

	var loginResp struct {
		Token string `json:"token"`
	}
	json.Unmarshal(w.Body.Bytes(), &loginResp)
	if loginResp.Token == "" {
		t.Fatalf("expected non-empty token")
	}

	// 4. 携带生成的 Token 请求受保护 API -> 应该返回 200
	req = httptest.NewRequest(http.MethodGet, "/api/v1/sessions/current", nil)
	req.Header.Set("Authorization", "Bearer "+loginResp.Token)
	w = httptest.NewRecorder()
	srv.Engine().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 OK with valid bearer token, got %d", w.Code)
	}

	// 5. 修改账号和密码
	changeBody := `{"old_password":"strong_password_999","new_username":"new_super_user","new_password":"brand_new_secret_pwd"}`
	req = httptest.NewRequest(http.MethodPost, "/api/v1/auth/change_credentials", bytes.NewBufferString(changeBody))
	req.Header.Set("Authorization", "Bearer "+loginResp.Token)
	req.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	srv.Engine().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 OK for change credentials, got %d: %s", w.Code, w.Body.String())
	}

	// 6. 用旧账号密码登录 -> 应失败 (401)
	loginBody = `{"username":"teacat_admin","password":"strong_password_999"}`
	req = httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", bytes.NewBufferString(loginBody))
	req.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	srv.Engine().ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 with old credentials, got %d", w.Code)
	}

	// 7. 用新账号密码登录 -> 应成功 (200)
	loginBody = `{"username":"new_super_user","password":"brand_new_secret_pwd"}`
	req = httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", bytes.NewBufferString(loginBody))
	req.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	srv.Engine().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 with new credentials, got %d", w.Code)
	}

	// 8. 重置账号密码 -> 应回退为环境变量值
	req = httptest.NewRequest(http.MethodPost, "/api/v1/auth/reset_credentials", nil)
	req.Header.Set("Authorization", "Bearer "+loginResp.Token)
	w = httptest.NewRecorder()
	srv.Engine().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 for reset credentials, got %d", w.Code)
	}

	// 9. 再次用原环境变量账号密码登录 -> 应恢复成功 (200)
	loginBody = `{"username":"teacat_admin","password":"strong_password_999"}`
	req = httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", bytes.NewBufferString(loginBody))
	req.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	srv.Engine().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 with reset env credentials, got %d", w.Code)
	}
}

func TestAPI_BriefSessionsAndWorkflowSessions(t *testing.T) {
	srv, st := setupTestAPIServer(t)
	ctx := context.Background()

	longSummary := "This is a very long summary text that should be excluded in brief list mode."
	_, err := st.CreateFeedbackSession(ctx, store.CreateSessionInput{
		WorkflowID: "wf-brief-test",
		Title:      "Brief Test Session",
		Summary:    longSummary,
	})
	if err != nil {
		t.Fatalf("failed to create test session: %v", err)
	}

	// 1. 测试 GET /api/v1/sessions?brief=true -> summary 应为空
	reqBrief := httptest.NewRequest(http.MethodGet, "/api/v1/sessions?brief=true", nil)
	wBrief := httptest.NewRecorder()
	srv.Engine().ServeHTTP(wBrief, reqBrief)

	if wBrief.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d", wBrief.Code)
	}
	var respBrief struct {
		Sessions []struct {
			ID      string `json:"session_id"`
			Title   string `json:"title"`
			Summary string `json:"summary"`
		} `json:"sessions"`
	}
	if err := json.Unmarshal(wBrief.Body.Bytes(), &respBrief); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}
	if len(respBrief.Sessions) == 0 {
		t.Fatalf("expected at least 1 session in brief response")
	}
	if respBrief.Sessions[0].Summary != "" {
		t.Fatalf("expected empty summary in brief mode, got: %q", respBrief.Sessions[0].Summary)
	}

	// 2. 测试 GET /api/v1/sessions (默认) -> summary 应当完整包含
	reqFull := httptest.NewRequest(http.MethodGet, "/api/v1/sessions", nil)
	wFull := httptest.NewRecorder()
	srv.Engine().ServeHTTP(wFull, reqFull)

	if wFull.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d", wFull.Code)
	}
	var respFull struct {
		Sessions []struct {
			ID      string `json:"session_id"`
			Summary string `json:"summary"`
		} `json:"sessions"`
	}
	if err := json.Unmarshal(wFull.Body.Bytes(), &respFull); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}
	if respFull.Sessions[0].Summary != longSummary {
		t.Fatalf("expected full summary, got: %q", respFull.Sessions[0].Summary)
	}

	// 3. 测试 GET /api/v1/workflows/:workflow_id/sessions -> 应当完整返回
	reqWf := httptest.NewRequest(http.MethodGet, "/api/v1/workflows/wf-brief-test/sessions", nil)
	wWf := httptest.NewRecorder()
	srv.Engine().ServeHTTP(wWf, reqWf)

	if wWf.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d", wWf.Code)
	}
	var respWf struct {
		Sessions []struct {
			ID      string `json:"session_id"`
			Summary string `json:"summary"`
		} `json:"sessions"`
	}
	if err := json.Unmarshal(wWf.Body.Bytes(), &respWf); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}
	if len(respWf.Sessions) == 0 || respWf.Sessions[0].Summary != longSummary {
		t.Fatalf("expected full summary in workflow sessions endpoint")
	}
}

func TestAPI_SessionImageAndCredentials(t *testing.T) {
	st, err := store.New(":memory:")
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer st.Close()

	cfg := &config.Config{
		ProjectID: "test-img-proj",
		Host:      "127.0.0.1",
		Port:      18775,
		JWTSecret: "test-secret-456",
		MCPToken:  "test-env-mcp-token-xyz",
		Version:   "v1.2.0-dev-test",
	}

	mcpSrv := mcp.NewServer(cfg, st, nil)
	srv := NewServer(cfg, st, nil)
	srv.mcpServer = mcpSrv

	// 1. 测试 GET /api/v1/auth/status 包含 version
	reqStatus := httptest.NewRequest(http.MethodGet, "/api/v1/auth/status", nil)
	wStatus := httptest.NewRecorder()
	srv.Engine().ServeHTTP(wStatus, reqStatus)
	if wStatus.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", wStatus.Code)
	}
	var authStatus struct {
		Version string `json:"version"`
	}
	json.Unmarshal(wStatus.Body.Bytes(), &authStatus)
	if authStatus.Version != "v1.2.0-dev-test" {
		t.Fatalf("expected version v1.2.0-dev-test, got %q", authStatus.Version)
	}

	// 2. 测试 GET /api/v1/credentials 包含环境变量凭据且标记 is_env: true
	reqCreds := httptest.NewRequest(http.MethodGet, "/api/v1/credentials", nil)
	wCreds := httptest.NewRecorder()
	srv.Engine().ServeHTTP(wCreds, reqCreds)
	if wCreds.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", wCreds.Code)
	}
	var credsResp struct {
		Credentials []struct {
			ID    uint   `json:"id"`
			Name  string `json:"name"`
			IsEnv bool   `json:"is_env"`
		} `json:"credentials"`
		EnvCreds []any `json:"env_credentials"`
	}
	json.Unmarshal(wCreds.Body.Bytes(), &credsResp)
	if len(credsResp.Credentials) == 0 {
		t.Fatalf("expected at least 1 credential")
	}
	foundEnv := false
	var envCredID uint
	for _, c := range credsResp.Credentials {
		if c.IsEnv {
			foundEnv = true
			envCredID = c.ID
			break
		}
	}
	if !foundEnv {
		t.Fatalf("expected to find is_env=true credential")
	}

	// 3. 测试试图删除环境变量凭据应被拒绝
	reqDel := httptest.NewRequest(http.MethodDelete, fmt.Sprintf("/api/v1/credentials/%d", envCredID), nil)
	wDel := httptest.NewRecorder()
	srv.Engine().ServeHTTP(wDel, reqDel)
	if wDel.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 Bad Request when deleting env credential, got %d", wDel.Code)
	}

	// 4. 创建带图片的 Session 并测试 GET /api/v1/sessions/:id/images/:index
	// 1x1 透明 PNG base64
	samplePNG := "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNkYAAAAAYAAjCB0C8AAAAASUVORK5CYII="
	sess, err := st.CreateFeedbackSession(context.Background(), store.CreateSessionInput{
		WorkflowID: "wf-img-test",
		Title:      "Image Test",
		Summary:    "Testing images",
	})
	if err != nil {
		t.Fatalf("failed to create session: %v", err)
	}

	// 提交反馈附带图片
	_, err = st.SubmitFeedback(context.Background(), store.SubmitFeedbackInput{
		SessionID:    sess.ID,
		ResponseText: "Here is screenshot",
		Images: []model.SessionImage{
			{
				Name:   "test.png",
				Format: "png",
				Data:   samplePNG,
			},
		},
	})
	if err != nil {
		t.Fatalf("failed to submit feedback with image: %v", err)
	}

	// GET 图片端点
	reqImg := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/v1/sessions/%s/images/0", sess.ID), nil)
	wImg := httptest.NewRecorder()
	srv.Engine().ServeHTTP(wImg, reqImg)
	if wImg.Code != http.StatusOK {
		t.Fatalf("expected 200 OK for image, got %d: %s", wImg.Code, wImg.Body.String())
	}
	if ct := wImg.Header().Get("Content-Type"); ct != "image/png" {
		t.Fatalf("expected image/png content type, got %s", ct)
	}
	if wImg.Body.Len() == 0 {
		t.Fatalf("expected non-empty image body")
	}
}
