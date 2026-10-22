package api

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/teacat99/RelayMesh/v2/internal/config"
	"github.com/teacat99/RelayMesh/v2/internal/store"
)

func TestOAuthConsentSPAFrameDenyHeaders(t *testing.T) {
	gin.SetMode(gin.TestMode)
	st, err := store.New(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.LoadWithMode("stdio")
	srv := NewServer(cfg, st, os.DirFS("."))
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/oauth/consent?req=x", nil)
	srv.Engine().ServeHTTP(w, req)
	if w.Header().Get("X-Frame-Options") != "DENY" {
		t.Fatalf("X-Frame-Options=%q", w.Header().Get("X-Frame-Options"))
	}
	if !strings.Contains(w.Header().Get("Content-Security-Policy"), "frame-ancestors") {
		t.Fatalf("CSP=%q", w.Header().Get("Content-Security-Policy"))
	}
}
