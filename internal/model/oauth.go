package model

import "time"

// OAuthClient 表示通过 DCR 注册的动态客户端；CIMD 客户端不落库，只在授权时校验元数据。
type OAuthClient struct {
	ClientID      string     `gorm:"primaryKey;size:512" json:"client_id"`
	ClientName    string     `gorm:"size:256" json:"client_name"`
	RedirectURIs  StringList `gorm:"type:text" json:"redirect_uris"`
	GrantTypes    StringList `gorm:"type:text" json:"grant_types"`
	ResponseTypes StringList `gorm:"type:text" json:"response_types"`
	TokenAuth     string     `gorm:"size:32" json:"token_endpoint_auth_method"` // none
	CreatedAt     time.Time  `json:"created_at"`
	LastUsedAt    *time.Time `json:"last_used_at"`
	// CreatedIP 是登记（POST /oauth/register）时的来源地址（#239），只给管理员在设置页「刚登记、还没授权的客户端」
	// 清单里核对用；json:"-" 让这个模型无论从哪条路径序列化都不会带上它，清单接口用自己的行结构显式给出。
	// 这一列是可空的增量列：旧版本登记的客户端为空，旧版本读取到这一列也直接忽略。
	CreatedIP string `gorm:"size:64" json:"-"`
}

// OAuthGrant 一次用户批准产生的授权：绑定隐藏 MCP 凭据与刷新令牌哈希。
type OAuthGrant struct {
	ID                  uint       `gorm:"primaryKey" json:"id"`
	ClientID            string     `gorm:"index;size:512;not null" json:"client_id"`
	ClientDisplayName   string     `gorm:"size:256" json:"client_display_name"`
	RedirectURI         string     `gorm:"size:2048" json:"redirect_uri"`
	Resource            string     `gorm:"size:2048" json:"resource"`
	Scopes              StringList `gorm:"type:text" json:"scopes"`
	CredentialID        uint       `gorm:"index;not null" json:"credential_id"`
	RefreshTokenHash    string     `gorm:"size:64;index" json:"-"`
	RefreshExpiresAt    *time.Time `json:"refresh_expires_at"`
	AbsoluteExpiresAt   *time.Time `json:"absolute_expires_at"`
	RevokedAt           *time.Time `json:"revoked_at"`
	RefreshGraceUntil   *time.Time `json:"-"` // 并发刷新宽限
	PreviousRefreshHash string     `gorm:"size:64" json:"-"`
	CreatedAt           time.Time  `json:"created_at"`
	UpdatedAt           time.Time  `json:"updated_at"`
}

// OAuthAccessToken 存访问令牌的 SHA-256（明文 rmo_ 只在签发时返回一次）。
type OAuthAccessToken struct {
	TokenHash string    `gorm:"primaryKey;size:64" json:"-"`
	GrantID   uint      `gorm:"index;not null" json:"grant_id"`
	ExpiresAt time.Time `gorm:"index" json:"expires_at"`
	CreatedAt time.Time `json:"created_at"`
}

// OAuthPolicy 持久化在 system_settings 的 oauth_policy 键。
//
// 真正的闸门只有两个：总开关 Enabled 与 AllowDCR（关闭后 POST /oauth/register 返回 403）。
// 早期版本里还有 allow_chatgpt / allow_claude / allow_loopback / client_host_allowlist 四项「信任来源」，
// 它们只决定批准页要不要标黄警告、不拒绝任何授权，#238 起删除（批准页的提示改为固定的内置规则，见
// oauth.WarnUntrustedClient）。库里旧版本存下的这几个键在读取时被忽略，用户下次保存策略时自然消失。
//
// 自动注册有三种方式（#239），由 AllowDCR 与 DCRAlways 两个布尔值组合表达：
//
//	AllowDCR=false                  关闭：POST /oauth/register 一律 403 registration_disabled；
//	AllowDCR=true,  DCRAlways=false 限时窗口（默认）：管理员在设置里开启「接入窗口」期间才受理，窗口外 403 registration_window_closed；
//	AllowDCR=true,  DCRAlways=true  常开：任何时候都受理（等同 2.0.0 的行为，公网上不建议）。
type OAuthPolicy struct {
	Enabled               bool   `json:"enabled"`    // OAuth 授权服务器总开关，默认 false
	PublicURL             string `json:"public_url"` // 对外根地址（https://…），缺省用环境变量或请求推导
	AllowDCR              bool   `json:"allow_dcr"`
	DCRAlways             bool   `json:"dcr_always"`               // 自动注册常开（不需要接入窗口）；只在 AllowDCR 为真时有意义，旧库没有这个键 = false = 限时窗口
	AccessTokenTTLMinutes int    `json:"access_token_ttl_minutes"` // 默认 60
	RefreshIdleDays       int    `json:"refresh_idle_days"`        // 默认 30
	RefreshAbsoluteDays   int    `json:"refresh_absolute_days"`    // 默认 90
}

func DefaultOAuthPolicy() OAuthPolicy {
	return OAuthPolicy{
		Enabled:               false,
		AllowDCR:              true,
		AccessTokenTTLMinutes: 60,
		RefreshIdleDays:       30,
		RefreshAbsoluteDays:   90,
	}
}
