package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"github.com/teacat99/RelayMesh/v2/internal/config"
	"github.com/teacat99/RelayMesh/v2/internal/store"
)

const testStrongJWTSecret = "test-jwt-secret-0123456789abcdef0123456789abcdef"

func newTokenTestAuth(t *testing.T, allowQueryToken bool) (*AuthHandler, *gin.Engine) {
	t.Helper()
	cfg := &config.Config{
		ProjectID:       "test-proj",
		WebUsername:     "admin",
		WebPassword:     "correct-password-123",
		JWTSecret:       testStrongJWTSecret,
		MCPToken:        "mcp-secret-token",
		AllowQueryToken: allowQueryToken,
	}
	st, err := store.New(":memory:")
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	auth := NewAuthHandler(cfg, st, nil)

	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.GET("/plain", auth.Middleware(), func(c *gin.Context) { c.String(http.StatusOK, "ok") })
	engine.GET("/stream", auth.StreamMiddleware(), func(c *gin.Context) { c.String(http.StatusOK, "ok") })
	engine.POST("/ticket", auth.Middleware(), auth.IssueSSETicket)
	return auth, engine
}

func signTestJWT(t *testing.T, method jwt.SigningMethod, secret string, claims jwt.MapClaims) string {
	t.Helper()
	tok := jwt.NewWithClaims(method, claims)
	s, err := tok.SignedString([]byte(secret))
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	return s
}

func adminClaims() jwt.MapClaims {
	return jwt.MapClaims{
		"sub":  "admin",
		"role": "admin",
		"exp":  time.Now().Add(24 * time.Hour).Unix(),
		"iat":  time.Now().Unix(),
	}
}

func doReq(engine *gin.Engine, method, target string, header map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, nil)
	for k, v := range header {
		req.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	engine.ServeHTTP(w, req)
	return w
}

// 普通接口不再接受 URL 上的 token：即使是有效 JWT 也必须被拒，避免凭据进入日志 / 历史 / Referer。
func TestPlainRoutesIgnoreQueryToken(t *testing.T) {
	_, engine := newTokenTestAuth(t, true)
	jwtTok := signTestJWT(t, jwt.SigningMethodHS256, testStrongJWTSecret, adminClaims())

	for _, q := range []string{"token=" + jwtTok, "token=mcp-secret-token", "ticket=anything", "access_token=" + jwtTok} {
		if w := doReq(engine, http.MethodGet, "/plain?"+q, nil); w.Code != http.StatusUnauthorized {
			t.Fatalf("普通接口携带 ?%.20s... 应返回 401，实际 %d", q, w.Code)
		}
	}
	if w := doReq(engine, http.MethodGet, "/plain", map[string]string{"Authorization": "Bearer " + jwtTok}); w.Code != http.StatusOK {
		t.Fatalf("Authorization 头仍应可用，实际 %d", w.Code)
	}
}

func TestStreamRouteLegacyQueryTokenToggle(t *testing.T) {
	jwtTok := signTestJWT(t, jwt.SigningMethodHS256, testStrongJWTSecret, adminClaims())

	_, allowed := newTokenTestAuth(t, true)
	if w := doReq(allowed, http.MethodGet, "/stream?token="+jwtTok, nil); w.Code != http.StatusOK {
		t.Fatalf("兼容开启时 SSE 应接受 ?token=，实际 %d", w.Code)
	}
	// v1.7 起 MCP 令牌不再能访问管理面：即使兼容开关打开，?token= 也只认人类 JWT。
	if w := doReq(allowed, http.MethodGet, "/stream?token=mcp-secret-token", nil); w.Code != http.StatusForbidden {
		t.Fatalf("兼容开启时 SSE 也必须拒绝 MCP 令牌（403），实际 %d", w.Code)
	}

	_, denied := newTokenTestAuth(t, false)
	if w := doReq(denied, http.MethodGet, "/stream?token="+jwtTok, nil); w.Code != http.StatusUnauthorized {
		t.Fatalf("兼容关闭后 SSE 必须拒绝 ?token=，实际 %d", w.Code)
	}
	if w := doReq(denied, http.MethodGet, "/stream", map[string]string{"Authorization": "Bearer " + jwtTok}); w.Code != http.StatusOK {
		t.Fatalf("Authorization 头不受兼容开关影响，实际 %d", w.Code)
	}
}

func issueTicket(t *testing.T, engine *gin.Engine, bearer string) string {
	t.Helper()
	w := doReq(engine, http.MethodPost, "/ticket", map[string]string{"Authorization": "Bearer " + bearer})
	if w.Code != http.StatusOK {
		t.Fatalf("签发票据应返回 200，实际 %d: %s", w.Code, w.Body.String())
	}
	var body struct {
		AuthRequired bool   `json:"auth_required"`
		Ticket       string `json:"ticket"`
		ExpiresIn    int    `json:"expires_in"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if !body.AuthRequired || body.Ticket == "" || body.ExpiresIn != 30 {
		t.Fatalf("票据响应不符: %+v", body)
	}
	return body.Ticket
}

func TestSSETicketFlow(t *testing.T) {
	_, engine := newTokenTestAuth(t, false) // 兼容通道关闭也必须可用
	jwtTok := signTestJWT(t, jwt.SigningMethodHS256, testStrongJWTSecret, adminClaims())

	// 未登录不得签发
	if w := doReq(engine, http.MethodPost, "/ticket", nil); w.Code != http.StatusUnauthorized {
		t.Fatalf("未登录签发票据应返回 401，实际 %d", w.Code)
	}

	ticket := issueTicket(t, engine, jwtTok)
	q := url.Values{"ticket": {ticket}}.Encode()

	// 票据不能用在普通接口
	if w := doReq(engine, http.MethodGet, "/plain?"+q, nil); w.Code != http.StatusUnauthorized {
		t.Fatalf("票据不得用于普通接口，实际 %d", w.Code)
	}
	// 用在 SSE：首次成功，第二次失败（单次有效）
	if w := doReq(engine, http.MethodGet, "/stream?"+q, nil); w.Code != http.StatusOK {
		t.Fatalf("票据首次兑换应成功，实际 %d", w.Code)
	}
	if w := doReq(engine, http.MethodGet, "/stream?"+q, nil); w.Code != http.StatusUnauthorized {
		t.Fatalf("票据二次兑换必须失败，实际 %d", w.Code)
	}
	// 伪造票据
	if w := doReq(engine, http.MethodGet, "/stream?ticket=forged", nil); w.Code != http.StatusUnauthorized {
		t.Fatalf("伪造票据必须 401，实际 %d", w.Code)
	}
}

// 带了无效票据时不应「回退」到 ?token= 放行：票据失败就是失败。
func TestInvalidTicketDoesNotFallBackToQueryToken(t *testing.T) {
	_, engine := newTokenTestAuth(t, true)
	jwtTok := signTestJWT(t, jwt.SigningMethodHS256, testStrongJWTSecret, adminClaims())
	q := url.Values{"ticket": {"forged"}, "token": {jwtTok}}.Encode()
	if w := doReq(engine, http.MethodGet, "/stream?"+q, nil); w.Code != http.StatusUnauthorized {
		t.Fatalf("无效票据不得回退到 ?token=，实际 %d", w.Code)
	}
}

func TestSSETicketWhenAuthNotRequired(t *testing.T) {
	cfg := &config.Config{ProjectID: "p", JWTSecret: testStrongJWTSecret} // 无密码
	st, err := store.New(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	auth := NewAuthHandler(cfg, st, nil)
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.POST("/ticket", auth.Middleware(), auth.IssueSSETicket)
	engine.GET("/stream", auth.StreamMiddleware(), func(c *gin.Context) { c.String(http.StatusOK, "ok") })

	w := doReq(engine, http.MethodPost, "/ticket", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("免密模式签发接口应返回 200，实际 %d", w.Code)
	}
	var body struct {
		AuthRequired bool   `json:"auth_required"`
		Ticket       string `json:"ticket"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	if body.AuthRequired || body.Ticket != "" {
		t.Fatalf("免密模式不应签发票据: %+v", body)
	}
	if w := doReq(engine, http.MethodGet, "/stream", nil); w.Code != http.StatusOK {
		t.Fatalf("免密模式 SSE 应放行，实际 %d", w.Code)
	}
}

func TestVerifyHumanJWT(t *testing.T) {
	auth, _ := newTokenTestAuth(t, true)

	valid := signTestJWT(t, jwt.SigningMethodHS256, testStrongJWTSecret, adminClaims())
	if sub, ok := auth.VerifyHumanJWT(valid); !ok || sub != "admin" {
		t.Fatalf("合法管理员 JWT 应通过: sub=%q ok=%v", sub, ok)
	}

	expired := adminClaims()
	expired["exp"] = time.Now().Add(-time.Minute).Unix()

	noRole := adminClaims()
	delete(noRole, "role")

	userRole := adminClaims()
	userRole["role"] = "viewer"

	noSub := adminClaims()
	delete(noSub, "sub")

	cases := map[string]string{
		"MCP 令牌":         "mcp-secret-token",
		"空串":             "",
		"乱码":             "not.a.jwt",
		"错误密钥":           signTestJWT(t, jwt.SigningMethodHS256, "another-secret-0123456789abcdef0123456789", adminClaims()),
		"已过期":            signTestJWT(t, jwt.SigningMethodHS256, testStrongJWTSecret, expired),
		"缺少 role":        signTestJWT(t, jwt.SigningMethodHS256, testStrongJWTSecret, noRole),
		"role 非 admin":   signTestJWT(t, jwt.SigningMethodHS256, testStrongJWTSecret, userRole),
		"缺少 sub":         signTestJWT(t, jwt.SigningMethodHS256, testStrongJWTSecret, noSub),
		"HS512（算法不在白名单）": signTestJWT(t, jwt.SigningMethodHS512, testStrongJWTSecret, adminClaims()),
	}
	for name, tok := range cases {
		if _, ok := auth.VerifyHumanJWT(tok); ok {
			t.Fatalf("%s 不应被当作人类管理员 JWT", name)
		}
	}

	// alg=none 的无签名令牌
	none := jwt.NewWithClaims(jwt.SigningMethodNone, adminClaims())
	noneStr, err := none.SignedString(jwt.UnsafeAllowNoneSignatureType)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := auth.VerifyHumanJWT(noneStr); ok {
		t.Fatal("alg=none 令牌必须被拒绝")
	}
	if _, ok := auth.VerifyJWTToken(noneStr); ok {
		t.Fatal("VerifyJWTToken 也不得接受 alg=none")
	}
}

func TestEffectiveCredentialsPrefersDatabaseAndReportsNoErrorWhenAbsent(t *testing.T) {
	auth, _ := newTokenTestAuth(t, true)
	ctx := t.Context()

	user, pass, err := auth.EffectiveCredentials(ctx)
	if err != nil || user != "admin" || pass != "correct-password-123" {
		t.Fatalf("无数据库凭据时应回退环境变量: %q %q %v", user, pass, err)
	}
	if err := auth.store.SaveAuthCredentials(ctx, "root", "db-password-456"); err != nil {
		t.Fatal(err)
	}
	user, pass, err = auth.EffectiveCredentials(ctx)
	if err != nil || user != "root" || pass != "db-password-456" {
		t.Fatalf("数据库凭据应优先: %q %q %v", user, pass, err)
	}
	if err := auth.store.ResetAuthCredentials(ctx); err != nil {
		t.Fatal(err)
	}
	user, pass, err = auth.EffectiveCredentials(ctx)
	if err != nil || user != "admin" || pass != "correct-password-123" {
		t.Fatalf("重置后应回退环境变量: %q %q %v", user, pass, err)
	}
}

func TestEffectiveCredentialsReportsDatabaseErrors(t *testing.T) {
	auth, _ := newTokenTestAuth(t, true)
	// 关闭底层连接模拟数据库读取失败：严格调用方必须拿到 error，而不是悄悄使用环境变量兜底值放行。
	if err := auth.store.Close(); err != nil {
		t.Fatal(err)
	}
	if _, _, err := auth.EffectiveCredentials(t.Context()); err == nil {
		t.Fatal("数据库不可用时 EffectiveCredentials 必须返回 error")
	}
}

// 匿名可访问的 /auth/status 不得泄露终端状态：终端是否启用是登录之后才应知道的信息。
func TestStatusDoesNotExposeTerminalState(t *testing.T) {
	auth, _ := newTokenTestAuth(t, true)
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.GET("/status", auth.Status)

	w := doReq(engine, http.MethodGet, "/status", nil)
	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	for key := range body {
		if strings.Contains(strings.ToLower(key), "terminal") {
			t.Fatalf("匿名状态接口不应包含终端相关字段 %q: %v", key, body)
		}
	}
}

// 凭据被修改或重置成功后必须通知订阅者（终端 Hub 借此撤销令牌与连接）。
func TestCredentialsChangeNotifiesHook(t *testing.T) {
	auth, _ := newTokenTestAuth(t, true)
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.POST("/change", auth.ChangeCredentials)
	engine.POST("/reset", auth.ResetCredentials)

	type call struct{ subject, ip string }
	var calls []call
	auth.SetCredentialsChangedHook(func(subject, ip string) { calls = append(calls, call{subject, ip}) })

	post := func(target string, body any) *httptest.ResponseRecorder {
		raw, _ := json.Marshal(body)
		req := httptest.NewRequest(http.MethodPost, target, bytes.NewReader(raw))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		engine.ServeHTTP(w, req)
		return w
	}

	// 旧密码错误：不得通知
	w := post("/change", map[string]string{"old_password": "wrong", "new_username": "root", "new_password": "new-secret-1"})
	// 必须是 403：401 会被前端当成登录过期，旧密码输错一次就被踢回登录页。
	if w.Code != http.StatusForbidden || len(calls) != 0 {
		t.Fatalf("旧密码错误应返回 403 且不触发通知: code=%d calls=%v", w.Code, calls)
	}

	// 成功修改
	w = post("/change", map[string]string{"old_password": "correct-password-123", "new_username": "root", "new_password": "new-secret-1"})
	if w.Code != http.StatusOK || len(calls) != 1 || calls[0].subject != "root" {
		t.Fatalf("成功修改应通知一次: code=%d calls=%v body=%s", w.Code, calls, w.Body.String())
	}

	// 重置
	w = post("/reset", nil)
	if w.Code != http.StatusOK || len(calls) != 2 {
		t.Fatalf("成功重置应再通知一次: code=%d calls=%v", w.Code, calls)
	}
}
