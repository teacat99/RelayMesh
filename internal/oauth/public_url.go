package oauth

import (
	"context"
	"net/http"
	"strings"

	"github.com/teacat99/RelayMesh/v2/internal/config"
	"github.com/teacat99/RelayMesh/v2/internal/store"
)

// PublicBaseSource 说明对外根地址是从哪里来的。设置页的状态卡靠它回答两个问题：
// 「我在设置页填的地址是不是被环境变量盖掉了」「现在用的是不是只按请求头推导出来的（公网上的客户端用不了）」。
type PublicBaseSource string

const (
	PublicBaseFromEnv      PublicBaseSource = "env"      // 环境变量 RELAYMESH_PUBLIC_URL
	PublicBaseFromSettings PublicBaseSource = "settings" // 设置页保存的「对外地址」
	PublicBaseDerived      PublicBaseSource = "derived"  // 两处都没设：按请求的 Host / X-Forwarded-* 推导
)

// ResolvePublicBaseSource 返回对外根 URL（无尾斜杠）与它的来源；优先级固定为 环境变量 > 设置页 > 请求推导。
func ResolvePublicBaseSource(cfg *config.Config, st *store.Store, r *http.Request) (string, PublicBaseSource) {
	if cfg != nil {
		if u := strings.TrimRight(strings.TrimSpace(cfg.PublicURL), "/"); u != "" {
			return u, PublicBaseFromEnv
		}
	}
	if st != nil {
		p, err := st.GetOAuthPolicy(context.Background())
		if err == nil {
			if u := strings.TrimRight(strings.TrimSpace(p.PublicURL), "/"); u != "" {
				return u, PublicBaseFromSettings
			}
		}
	}
	return derivePublicBaseFromRequest(r), PublicBaseDerived
}

// ResolvePublicBase 返回 issuer 根 URL 与是否来自请求推导（推导时需 no-store / Vary）。
func ResolvePublicBase(cfg *config.Config, st *store.Store, r *http.Request) (base string, derived bool) {
	base, src := ResolvePublicBaseSource(cfg, st, r)
	return base, src == PublicBaseDerived
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
