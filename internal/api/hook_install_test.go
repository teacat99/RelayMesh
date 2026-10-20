package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/teacat99/RelayMesh/v2/internal/config"
)

func TestServeHookBinaryRejectsBadName(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := &APIHandler{cfg: &config.Config{}}
	r := gin.New()
	r.GET("/hooks/cursor/bin/:name", h.ServeHookBinary)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/hooks/cursor/bin/evil.bin", nil)
	r.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("want 404 got %d", w.Code)
	}
}

func TestServeHookInstallSh(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := &APIHandler{cfg: &config.Config{}}
	r := gin.New()
	r.GET("/hooks/cursor/install.sh", h.ServeHookInstallSh)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/hooks/cursor/install.sh", nil)
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("want 200 got %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "relaymesh-hook") {
		t.Fatal("install.sh 应提到 relaymesh-hook")
	}
}

// 安装脚本在 Hub 没有 agent-dist 时回退到 GitHub Release：Hub 是正式版本就写入同版本号，开发版 / 未知留空，
// 且发出去的脚本里不能留着渲染用的标记。
func TestServeHookInstallScriptsCarryHubVersion(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cases := []struct {
		version string
		wantSh  string
		wantPs1 string
	}{
		{"v2.0.0", "HUB_VERSION='v2.0.0'", "$hubVersion = 'v2.0.0'"},
		{"2.1.3", "HUB_VERSION='v2.1.3'", "$hubVersion = 'v2.1.3'"},
		{"v2.0.0-rc.1", "HUB_VERSION='v2.0.0-rc.1'", "$hubVersion = 'v2.0.0-rc.1'"},
		{"v1.6.3-dev-20261020-15e4726", "HUB_VERSION=''", "$hubVersion = ''"},
		{"dev", "HUB_VERSION=''", "$hubVersion = ''"},
		{"", "HUB_VERSION=''", "$hubVersion = ''"},
		{"v2.0.0'; rm -rf /; echo '", "HUB_VERSION=''", "$hubVersion = ''"},
	}
	for _, tc := range cases {
		h := &APIHandler{cfg: &config.Config{Version: tc.version}}
		r := gin.New()
		r.GET("/hooks/cursor/install.sh", h.ServeHookInstallSh)
		r.GET("/hooks/cursor/install.ps1", h.ServeHookInstallPs1)
		for path, want := range map[string]string{
			"/hooks/cursor/install.sh":  tc.wantSh,
			"/hooks/cursor/install.ps1": tc.wantPs1,
		} {
			w := httptest.NewRecorder()
			r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
			if w.Code != http.StatusOK {
				t.Fatalf("%s: want 200 got %d", path, w.Code)
			}
			body := w.Body.String()
			if !strings.Contains(body, want) {
				t.Errorf("%s 版本 %q：应包含 %q", path, tc.version, want)
			}
			if strings.Contains(body, "@relaymesh-hub-version@") {
				t.Errorf("%s 版本 %q：发出去的脚本里不能留着渲染标记", path, tc.version)
			}
			if strings.Contains(body, "rm -rf") {
				t.Errorf("%s 版本 %q：不合格的版本号不能写进脚本", path, tc.version)
			}
		}
	}
	// cfg 为空（测试或极端情况）也要能出脚本，版本留空。
	h := &APIHandler{}
	r := gin.New()
	r.GET("/hooks/cursor/install.sh", h.ServeHookInstallSh)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/hooks/cursor/install.sh", nil))
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "HUB_VERSION=''") {
		t.Fatalf("cfg 为空时应返回版本留空的脚本，got %d", w.Code)
	}
}
