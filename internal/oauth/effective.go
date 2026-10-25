package oauth

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/teacat99/RelayMesh/v2/internal/config"
	"github.com/teacat99/RelayMesh/v2/internal/model"
	"github.com/teacat99/RelayMesh/v2/internal/store"
)

// EffectiveStatus 是设置页「状态卡」用的只读视图：这一刻 OAuth 到底有没有在服务、对外地址是什么、
// ChatGPT / Claude 里的连接器该填哪个地址。
//
// 全部现算、不落库，只描述事实，不含任何密钥；它与策略（可编辑的那份）分开返回，
// 因为「策略里写着开」不等于「真的开着」——环境变量 RELAYMESH_OAUTH=off 会把它整个盖掉。
type EffectiveStatus struct {
	// EnvOff：环境变量 RELAYMESH_OAUTH=off 把 OAuth 整个关了（此时策略里的开关改了也没用，要去掉环境变量并重启）。
	EnvOff bool `json:"env_off"`
	// Enabled：协议端点此刻真的在服务（策略总开关打开，且没有被环境变量关掉）。
	Enabled bool `json:"enabled"`
	// PublicBase：此刻对外使用的根地址（无尾斜杠），PublicBaseSource 说明它从哪来（env / settings / derived）。
	PublicBase       string           `json:"public_base"`
	PublicBaseSource PublicBaseSource `json:"public_base_source"`
	// SettingsPublicURLShadowed：环境变量设置了对外地址，而设置页里也填了一个不同的值——设置页的那个被盖掉了。
	SettingsPublicURLShadowed bool `json:"settings_public_url_shadowed"`
	// ConnectorURL：在 ChatGPT / Claude 里添加连接器时要粘贴的地址（对外地址 + /mcp）。
	ConnectorURL string `json:"connector_url"`
	// 两个元数据地址：自检与排错时用。
	ResourceMetadataURL   string `json:"resource_metadata_url"`
	AuthServerMetadataURL string `json:"authorization_server_metadata_url"`
	// DCR：自动注册此刻的状态（方式 / 现在受不受理 / 接入窗口的剩余时间；#239）。Effective 只能按策略算出方式与「常开时受理」，
	// 接入窗口的开合在授权服务里，API 层用 Service.DCRStatus 覆盖这个字段。
	DCR DCRStatus `json:"dcr"`
}

// Effective 按这次请求（决定了「推导出来的地址」）计算 OAuth 的生效状态。
func Effective(cfg *config.Config, st *store.Store, r *http.Request) EffectiveStatus {
	base, src := ResolvePublicBaseSource(cfg, st, r)
	out := EffectiveStatus{
		EnvOff:                cfg != nil && cfg.OAuthOff,
		Enabled:               ProtocolEnabled(cfg, st),
		PublicBase:            base,
		PublicBaseSource:      src,
		ConnectorURL:          base + resourcePath,
		ResourceMetadataURL:   base + "/.well-known/oauth-protected-resource" + resourcePath,
		AuthServerMetadataURL: base + "/.well-known/oauth-authorization-server",
	}
	policy := model.DefaultOAuthPolicy()
	if st != nil {
		if p, err := st.GetOAuthPolicy(context.Background()); err == nil {
			policy = p
			if src == PublicBaseFromEnv {
				if u := strings.TrimRight(strings.TrimSpace(p.PublicURL), "/"); u != "" && u != base {
					out.SettingsPublicURLShadowed = true
				}
			}
		}
	}
	out.DCR = BuildDCRStatus(policy, time.Now(), time.Time{}, out.Enabled)
	return out
}
