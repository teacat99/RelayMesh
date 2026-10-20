package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"github.com/teacat99/RelayMesh/v2/internal/config"
	"github.com/teacat99/RelayMesh/v2/internal/store"
)

// 这组测试守住「读不到登录凭据 ≠ 没设密码」：
// 部署方在网页里轮换密码后，会把 compose 里作废的 RELAYMESH_WEB_PASSWORD 清掉，此时凭据只存在数据库里。
// 如果这时数据库读取失败，回退到环境变量得到的是空串，而空串代表「免登录」——必须失败即关闭，而不是放行。

const (
	failClosedDBUser     = "root"
	failClosedDBPassword = "fresh-db-password-456"
	failClosedEnvPass    = "stale-env-password-123"
)

func newCredsTestAuth(t *testing.T, st *store.Store, envPassword string) (*AuthHandler, *gin.Engine) {
	t.Helper()
	cfg := &config.Config{
		ProjectID:   "test-proj",
		WebUsername: "admin",
		WebPassword: envPassword,
		JWTSecret:   testStrongJWTSecret,
	}
	auth := NewAuthHandler(cfg, st, nil)

	gin.SetMode(gin.TestMode)
	engine := gin.New()
	ok := func(c *gin.Context) { c.String(http.StatusOK, "ok") }
	engine.GET("/plain", auth.Middleware(), ok)
	engine.GET("/stream", auth.StreamMiddleware(), ok)
	engine.POST("/ticket", auth.Middleware(), auth.IssueSSETicket)
	engine.POST("/login", auth.Login)
	engine.GET("/status", auth.Status)
	// 不经过中间件直接挂处理器：验证处理器自身在读不到凭据时也不会跳过旧密码校验。
	engine.POST("/change-direct", auth.ChangeCredentials)
	return auth, engine
}

func newMemStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.New(":memory:")
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func postJSON(engine *gin.Engine, target, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, target, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	engine.ServeHTTP(w, req)
	return w
}

func decodeBody(t *testing.T, w *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &m); err != nil {
		t.Fatalf("响应不是 JSON: %v\n%s", err, w.Body.String())
	}
	return m
}

func adminBearer(t *testing.T) map[string]string {
	t.Helper()
	return map[string]string{"Authorization": "Bearer " + signTestJWT(t, jwt.SigningMethodHS256, testStrongJWTSecret, adminClaims())}
}

// 凭据只在数据库里（环境变量已清理）时，数据库读取失败必须让所有「判断是否需要登录」的入口失败即关闭。
func TestCredentialReadFailureFailsClosedWhenOnlyDatabaseHoldsCredentials(t *testing.T) {
	st := newMemStore(t)
	if err := st.SaveAuthCredentials(t.Context(), failClosedDBUser, failClosedDBPassword); err != nil {
		t.Fatal(err)
	}
	_, engine := newCredsTestAuth(t, st, "")

	// 数据库健康时：没有令牌的请求照常被拒（401），有效令牌放行。
	if w := doReq(engine, http.MethodGet, "/plain", nil); w.Code != http.StatusUnauthorized {
		t.Fatalf("健康时无令牌应 401，实际 %d", w.Code)
	}
	if w := doReq(engine, http.MethodGet, "/plain", adminBearer(t)); w.Code != http.StatusOK {
		t.Fatalf("健康时有效令牌应放行，实际 %d", w.Code)
	}

	// 模拟数据库故障。
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	for name, w := range map[string]*httptest.ResponseRecorder{
		"普通接口（无令牌）":   doReq(engine, http.MethodGet, "/plain", nil),
		"普通接口（有效令牌）":  doReq(engine, http.MethodGet, "/plain", adminBearer(t)),
		"流式接口（无令牌）":   doReq(engine, http.MethodGet, "/stream", nil),
		"签发 SSE 票据":   doReq(engine, http.MethodPost, "/ticket", adminBearer(t)),
		"登录（密码正确也一样）": postJSON(engine, "/login", `{"username":"`+failClosedDBUser+`","password":"`+failClosedDBPassword+`"}`),
		"改密（处理器自身）":   postJSON(engine, "/change-direct", `{"old_password":"","new_username":"x","new_password":"another-password-789"}`),
	} {
		if w.Code != http.StatusServiceUnavailable {
			t.Fatalf("%s：读不到凭据时应返回 503，实际 %d：%s", name, w.Code, w.Body.String())
		}
		body := decodeBody(t, w)
		if body["code"] != "credentials_unavailable" || body["auth_required"] != true {
			t.Fatalf("%s：503 响应应带 credentials_unavailable 且 auth_required=true，实际 %v", name, body)
		}
		if tok, _ := body["token"].(string); tok != "" {
			t.Fatalf("%s：失败时不得签发令牌", name)
		}
	}

	// 匿名状态接口保持 200（探活 / 前端启动都依赖它），但必须报告「需要登录」，不能展示成免登录。
	w := doReq(engine, http.MethodGet, "/status", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("状态接口应保持 200，实际 %d", w.Code)
	}
	if body := decodeBody(t, w); body["auth_required"] != true {
		t.Fatalf("读不到凭据时状态接口必须报告 auth_required=true，实际 %v", body["auth_required"])
	}
}

// 环境变量里仍有密码时保持既有行为：读取失败回退到它，鉴权不会被绕过，有效令牌照常工作。
func TestCredentialReadFailureKeepsEnvFallbackWhenEnvPasswordSet(t *testing.T) {
	st := newMemStore(t)
	_, engine := newCredsTestAuth(t, st, failClosedEnvPass)
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	if w := doReq(engine, http.MethodGet, "/plain", nil); w.Code != http.StatusUnauthorized {
		t.Fatalf("有环境变量兜底时无令牌应 401（不能放行），实际 %d", w.Code)
	}
	if w := doReq(engine, http.MethodGet, "/plain", adminBearer(t)); w.Code != http.StatusOK {
		t.Fatalf("有环境变量兜底时有效令牌应放行，实际 %d", w.Code)
	}
	if body := decodeBody(t, doReq(engine, http.MethodGet, "/status", nil)); body["auth_required"] != true {
		t.Fatalf("状态应报告需要登录，实际 %v", body["auth_required"])
	}
}

// 真正没有设置密码（数据库健康、环境变量为空）仍是免登录模式：这是本地开发的默认行为，不能被上面的修复误伤。
func TestOpenModeUnchangedWhenNoPasswordAnywhereAndDatabaseHealthy(t *testing.T) {
	st := newMemStore(t)
	_, engine := newCredsTestAuth(t, st, "")

	if w := doReq(engine, http.MethodGet, "/plain", nil); w.Code != http.StatusOK {
		t.Fatalf("免登录模式下应放行，实际 %d", w.Code)
	}
	if w := doReq(engine, http.MethodGet, "/stream", nil); w.Code != http.StatusOK {
		t.Fatalf("免登录模式下流式接口应放行，实际 %d", w.Code)
	}
	w := postJSON(engine, "/login", `{"username":"x","password":"y"}`)
	if w.Code != http.StatusOK || decodeBody(t, w)["auth_required"] != false {
		t.Fatalf("免登录模式下登录应返回 auth_required=false，实际 %d %s", w.Code, w.Body.String())
	}
	if body := decodeBody(t, doReq(engine, http.MethodGet, "/status", nil)); body["auth_required"] != false {
		t.Fatalf("状态应报告免登录，实际 %v", body["auth_required"])
	}
}

// 清理作废环境变量的前提：数据库里的凭据优先，旧的环境变量密码本来就登录不了，去掉它也不影响数据库里的新密码。
func TestDatabasePasswordWinsOverStaleEnvAndRemovingEnvDoesNotAffectLogin(t *testing.T) {
	st := newMemStore(t)
	if err := st.SaveAuthCredentials(t.Context(), failClosedDBUser, failClosedDBPassword); err != nil {
		t.Fatal(err)
	}
	_, withStaleEnv := newCredsTestAuth(t, st, failClosedEnvPass)
	_, withoutEnv := newCredsTestAuth(t, st, "")

	for name, engine := range map[string]*gin.Engine{"保留旧环境变量": withStaleEnv, "已清理环境变量": withoutEnv} {
		stale := postJSON(engine, "/login", `{"username":"admin","password":"`+failClosedEnvPass+`"}`)
		if stale.Code != http.StatusUnauthorized {
			t.Fatalf("%s：旧环境变量密码不应再能登录，实际 %d", name, stale.Code)
		}
		fresh := postJSON(engine, "/login", `{"username":"`+failClosedDBUser+`","password":"`+failClosedDBPassword+`"}`)
		if fresh.Code != http.StatusOK {
			t.Fatalf("%s：数据库里的新密码应能登录，实际 %d：%s", name, fresh.Code, fresh.Body.String())
		}
		if tok, _ := decodeBody(t, fresh)["token"].(string); tok == "" {
			t.Fatalf("%s：登录成功应签发令牌", name)
		}
	}
}
