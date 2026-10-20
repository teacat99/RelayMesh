package api

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/teacat99/RelayMesh/v2/internal/config"
	"github.com/teacat99/RelayMesh/v2/internal/store"
)

// /api/v1/auth/status 是匿名可访问的：run_mode 只是 serve / stdio 两个枚举值，前端据此决定首次打开时侧栏是否默认收起。
func TestAuthStatusReportsRunMode(t *testing.T) {
	cases := []struct{ mode, want string }{
		{"stdio", "stdio"},
		{"serve", "serve"},
		{"", "serve"},
		{"something-else", "serve"},
	}
	for _, tc := range cases {
		cfg := &config.Config{Mode: tc.mode, ProjectID: "p", JWTSecret: testStrongJWTSecret}
		st, err := store.New(":memory:")
		if err != nil {
			t.Fatal(err)
		}
		auth := NewAuthHandler(cfg, st, nil)
		gin.SetMode(gin.TestMode)
		engine := gin.New()
		engine.GET("/api/v1/auth/status", auth.Status)

		w := doReq(engine, http.MethodGet, "/api/v1/auth/status", nil)
		if w.Code != http.StatusOK {
			t.Fatalf("mode %q：状态接口应返回 200，实际 %d", tc.mode, w.Code)
		}
		var body struct {
			RunMode string `json:"run_mode"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if body.RunMode != tc.want {
			t.Errorf("mode %q：run_mode 应为 %q，实际 %q", tc.mode, tc.want, body.RunMode)
		}
		_ = st.Close()
	}
}
