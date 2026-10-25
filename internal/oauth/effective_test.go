package oauth_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/teacat99/RelayMesh/v2/internal/api"
	"github.com/teacat99/RelayMesh/v2/internal/config"
	"github.com/teacat99/RelayMesh/v2/internal/oauth"
	"github.com/teacat99/RelayMesh/v2/internal/store"
)

func newEffectiveRequest(host string, headers map[string]string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "http://"+host+"/api/v1/oauth/policy", nil)
	r.Host = host
	for k, v := range headers {
		r.Header.Set(k, v)
	}
	return r
}

// 对外地址的来源优先级固定为 环境变量 > 设置页 > 请求推导；状态卡靠 public_base_source 告诉用户它来自哪里。
func TestEffectivePublicBaseSources(t *testing.T) {
	st, err := store.New(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	savePublicURL := func(u string) {
		t.Helper()
		p, _ := st.GetOAuthPolicy(ctx)
		p.PublicURL = u
		if err := st.SaveOAuthPolicy(ctx, p); err != nil {
			t.Fatal(err)
		}
	}

	t.Run("什么都没设：按请求推导，带代理头时跟代理头走", func(t *testing.T) {
		cfg := config.LoadWithMode("stdio")
		cfg.OAuthOff = false
		cfg.PublicURL = ""
		e := oauth.Effective(cfg, st, newEffectiveRequest("192.168.1.5:18775", nil))
		if e.PublicBaseSource != oauth.PublicBaseDerived || e.PublicBase != "http://192.168.1.5:18775" {
			t.Fatalf("推导结果不对: %+v", e)
		}
		if e.ConnectorURL != "http://192.168.1.5:18775/mcp" {
			t.Fatalf("连接器地址应为对外地址 + /mcp，得到 %q", e.ConnectorURL)
		}
		e2 := oauth.Effective(cfg, st, newEffectiveRequest("hub.internal:18775", map[string]string{
			"X-Forwarded-Proto": "https", "X-Forwarded-Host": "relay.example.com",
		}))
		if e2.PublicBase != "https://relay.example.com" || e2.PublicBaseSource != oauth.PublicBaseDerived {
			t.Fatalf("代理头没生效: %+v", e2)
		}
	})

	t.Run("设置页填了：来源是 settings，末尾斜杠被去掉", func(t *testing.T) {
		savePublicURL("https://relay.example.com/")
		defer savePublicURL("")
		cfg := config.LoadWithMode("stdio")
		cfg.OAuthOff = false
		cfg.PublicURL = ""
		e := oauth.Effective(cfg, st, newEffectiveRequest("192.168.1.5:18775", nil))
		if e.PublicBaseSource != oauth.PublicBaseFromSettings || e.PublicBase != "https://relay.example.com" {
			t.Fatalf("settings 来源不对: %+v", e)
		}
		if e.SettingsPublicURLShadowed {
			t.Fatalf("没有环境变量时设置页的值不可能被盖掉: %+v", e)
		}
		if e.ResourceMetadataURL != "https://relay.example.com/.well-known/oauth-protected-resource/mcp" ||
			e.AuthServerMetadataURL != "https://relay.example.com/.well-known/oauth-authorization-server" {
			t.Fatalf("元数据地址不对: %+v", e)
		}
	})

	t.Run("环境变量优先，并且设置页里不同的值被标成「被盖掉」", func(t *testing.T) {
		savePublicURL("https://from-settings.example.com")
		defer savePublicURL("")
		cfg := config.LoadWithMode("stdio")
		cfg.OAuthOff = false
		cfg.PublicURL = "https://from-env.example.com"
		e := oauth.Effective(cfg, st, newEffectiveRequest("x:1", nil))
		if e.PublicBaseSource != oauth.PublicBaseFromEnv || e.PublicBase != "https://from-env.example.com" {
			t.Fatalf("env 应当优先: %+v", e)
		}
		if !e.SettingsPublicURLShadowed {
			t.Fatalf("设置页的值与环境变量不同，应标记为被盖掉: %+v", e)
		}
		// 两处写的是同一个地址（只差末尾斜杠）就不算被盖掉。
		savePublicURL("https://from-env.example.com/")
		if oauth.Effective(cfg, st, newEffectiveRequest("x:1", nil)).SettingsPublicURLShadowed {
			t.Fatal("同一个地址不应标记为被盖掉")
		}
		// 设置页没填也不算。
		savePublicURL("")
		if oauth.Effective(cfg, st, newEffectiveRequest("x:1", nil)).SettingsPublicURLShadowed {
			t.Fatal("设置页没填不应标记为被盖掉")
		}
	})
}

// enabled 是「协议端点此刻真的在服务」：总开关打开且没有被环境变量关掉，二者缺一不可。
func TestEffectiveEnabledFollowsSwitchAndEnv(t *testing.T) {
	st, err := store.New(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.LoadWithMode("stdio")
	cfg.OAuthOff = false
	cfg.PublicURL = "https://hub.example.test"
	req := newEffectiveRequest("hub.example.test", nil)

	if e := oauth.Effective(cfg, st, req); e.Enabled || e.EnvOff {
		t.Fatalf("默认总开关关闭: %+v", e)
	}
	enableOAuthServer(t, st)
	if e := oauth.Effective(cfg, st, req); !e.Enabled || e.EnvOff {
		t.Fatalf("总开关打开后应生效: %+v", e)
	}
	cfg.OAuthOff = true
	if e := oauth.Effective(cfg, st, req); e.Enabled || !e.EnvOff {
		t.Fatalf("环境变量 RELAYMESH_OAUTH=off 应盖掉策略里的开关: %+v", e)
	}
}

func getPolicyJSON(t *testing.T, engine http.Handler, jwt string) (int, map[string]json.RawMessage) {
	t.Helper()
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/oauth/policy", nil)
	req.Header.Set("Authorization", "Bearer "+jwt)
	engine.ServeHTTP(w, req)
	var body map[string]json.RawMessage
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	return w.Code, body
}

// 设置页读到的策略里带只读的 effective；而且被环境变量整个关掉的 Hub 也要能读（页面要解释「为什么没生效」，不能是 404）。
func TestPolicyEndpointReturnsEffective(t *testing.T) {
	gin.SetMode(gin.TestMode)

	t.Run("正常 Hub", func(t *testing.T) {
		st, _ := store.New(":memory:")
		enableOAuthServer(t, st)
		cfg := oauthTestCfg()
		srv := api.NewServer(cfg, st, nil)
		jwt := loginJWT(t, srv.Engine(), "admin", "oauth-test-pass")

		code, body := getPolicyJSON(t, srv.Engine(), jwt)
		if code != http.StatusOK {
			t.Fatalf("status %d", code)
		}
		if _, ok := body["policy"]; !ok {
			t.Fatal("响应里缺少 policy（旧字段必须保留）")
		}
		var eff oauth.EffectiveStatus
		if err := json.Unmarshal(body["effective"], &eff); err != nil {
			t.Fatalf("effective 解析失败: %v (%s)", err, body["effective"])
		}
		if !eff.Enabled || eff.EnvOff {
			t.Fatalf("总开关打开，应生效: %+v", eff)
		}
		if eff.PublicBase != "https://hub.example.test" || eff.PublicBaseSource != oauth.PublicBaseFromEnv {
			t.Fatalf("对外地址与来源不对: %+v", eff)
		}
		if eff.ConnectorURL != "https://hub.example.test/mcp" {
			t.Fatalf("连接器地址不对: %q", eff.ConnectorURL)
		}
	})

	t.Run("被 RELAYMESH_OAUTH=off 关掉的 Hub：仍能读到状态，写入与授权路由不存在", func(t *testing.T) {
		st, _ := store.New(":memory:")
		enableOAuthServer(t, st) // 策略里写着开，但环境变量把它盖掉
		cfg := oauthTestCfg()
		cfg.OAuthOff = true
		srv := api.NewServer(cfg, st, nil)
		jwt := loginJWT(t, srv.Engine(), "admin", "oauth-test-pass")

		code, body := getPolicyJSON(t, srv.Engine(), jwt)
		if code != http.StatusOK {
			t.Fatalf("被环境变量关掉时读策略也应返回 200（页面要据此解释原因），得到 %d", code)
		}
		var eff oauth.EffectiveStatus
		if err := json.Unmarshal(body["effective"], &eff); err != nil {
			t.Fatal(err)
		}
		if !eff.EnvOff || eff.Enabled {
			t.Fatalf("应报告被环境变量关闭且未生效: %+v", eff)
		}
		var pol struct {
			Enabled bool `json:"enabled"`
		}
		_ = json.Unmarshal(body["policy"], &pol)
		if !pol.Enabled {
			t.Fatal("策略本身应原样返回（总开关写着开），由 effective 说明它没生效")
		}

		for _, tc := range []struct{ method, path string }{
			{http.MethodPut, "/api/v1/oauth/policy"},
			{http.MethodGet, "/api/v1/oauth/grants"},
		} {
			w := httptest.NewRecorder()
			req := httptest.NewRequest(tc.method, tc.path, nil)
			req.Header.Set("Authorization", "Bearer "+jwt)
			srv.Engine().ServeHTTP(w, req)
			if w.Code != http.StatusNotFound {
				t.Fatalf("%s %s 在 OAuth 被环境变量关掉时应不存在（404），得到 %d", tc.method, tc.path, w.Code)
			}
		}
	})

	t.Run("未登录不能读", func(t *testing.T) {
		st, _ := store.New(":memory:")
		srv := api.NewServer(oauthTestCfg(), st, nil)
		w := httptest.NewRecorder()
		srv.Engine().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/oauth/policy", nil))
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("未登录读取策略应为 401，得到 %d", w.Code)
		}
	})
}
