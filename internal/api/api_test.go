package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/teacat99/RelayMesh/internal/config"
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
