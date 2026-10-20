package oauth

import (
	"context"

	"github.com/teacat99/RelayMesh/v2/internal/config"
	"github.com/teacat99/RelayMesh/v2/internal/store"
)

// ProtocolEnabled 环境硬开关关闭或策略总开关关闭时，OAuth 协议端点与 MCP 元数据均不启用。
func ProtocolEnabled(cfg *config.Config, st *store.Store) bool {
	if cfg == nil || cfg.OAuthOff {
		return false
	}
	if st == nil {
		return false
	}
	p, err := st.GetOAuthPolicy(context.Background())
	if err != nil || !p.Enabled {
		return false
	}
	return true
}
