package oauth

import (
	"net/http"

	"github.com/teacat99/RelayMesh/v2/internal/config"
	"github.com/teacat99/RelayMesh/v2/internal/store"
)

const resourcePath = "/mcp"

// PublicBase 返回对外可见的根 URL（无尾斜杠）与是否来自请求推导。
func PublicBase(cfg *config.Config, st *store.Store, r *http.Request) (string, bool) {
	return ResolvePublicBase(cfg, st, r)
}

func Issuer(cfg *config.Config, st *store.Store, r *http.Request) string {
	base, _ := PublicBase(cfg, st, r)
	return base
}

func ResourceURL(cfg *config.Config, st *store.Store, r *http.Request) string {
	base, _ := PublicBase(cfg, st, r)
	return base + resourcePath
}

func ResourceMetadataURL(cfg *config.Config, st *store.Store, r *http.Request) string {
	base, _ := PublicBase(cfg, st, r)
	return base + "/.well-known/oauth-protected-resource" + resourcePath
}

func WWWAuthenticateValue(cfg *config.Config, st *store.Store, r *http.Request) string {
	if !ProtocolEnabled(cfg, st) {
		return ""
	}
	meta := ResourceMetadataURL(cfg, st, r)
	return `Bearer resource_metadata="` + meta + `"` // RFC 9728
}
