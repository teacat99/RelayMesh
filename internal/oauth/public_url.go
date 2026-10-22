package oauth

import (
	"context"
	"net/http"
	"strings"

	"github.com/teacat99/RelayMesh/v2/internal/config"
	"github.com/teacat99/RelayMesh/v2/internal/store"
)

// ResolvePublicBase 返回 issuer 根 URL 与是否来自请求推导（推导时需 no-store / Vary）。
func ResolvePublicBase(cfg *config.Config, st *store.Store, r *http.Request) (base string, derived bool) {
	if u := strings.TrimRight(strings.TrimSpace(cfg.PublicURL), "/"); u != "" {
		return u, false
	}
	if st != nil {
		p, err := st.GetOAuthPolicy(context.Background())
		if err == nil {
			if u := strings.TrimRight(strings.TrimSpace(p.PublicURL), "/"); u != "" {
				return u, false
			}
		}
	}
	return derivePublicBaseFromRequest(r), true
}

func derivePublicBaseFromRequest(r *http.Request) string {
	if r == nil {
		return "http://127.0.0.1"
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	if xf := strings.TrimSpace(r.Header.Get("X-Forwarded-Proto")); xf != "" {
		scheme = strings.ToLower(strings.Split(xf, ",")[0])
	}
	host := strings.TrimSpace(r.Host)
	if xh := strings.TrimSpace(r.Header.Get("X-Forwarded-Host")); xh != "" {
		host = strings.Split(xh, ",")[0]
	}
	host = strings.TrimSpace(host)
	if !ValidateRedirectHostSyntax(host) {
		host = "127.0.0.1"
	}
	if host == "" {
		host = "127.0.0.1"
	}
	return scheme + "://" + host
}
