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
type OAuthPolicy struct {
	Enabled               bool     `json:"enabled"`    // OAuth 授权服务器总开关，默认 false
	PublicURL             string   `json:"public_url"` // 对外根地址（https://…），缺省用环境变量或请求推导
	AllowDCR              bool     `json:"allow_dcr"`
	AccessTokenTTLMinutes int      `json:"access_token_ttl_minutes"` // 默认 60
	RefreshIdleDays       int      `json:"refresh_idle_days"`        // 默认 30
	RefreshAbsoluteDays   int      `json:"refresh_absolute_days"`    // 默认 90
	ClientHostAllowlist   []string `json:"client_host_allowlist"`    // 额外域名
	AllowChatGPT          bool     `json:"allow_chatgpt"`            // 默认 true
	AllowClaude           bool     `json:"allow_claude"`             // 默认 true
	AllowLoopback         bool     `json:"allow_loopback"`           // 默认 true
}

func DefaultOAuthPolicy() OAuthPolicy {
	return OAuthPolicy{
		Enabled:               false,
		AllowDCR:              true,
		AccessTokenTTLMinutes: 60,
		RefreshIdleDays:       30,
		RefreshAbsoluteDays:   90,
		AllowChatGPT:          true,
		AllowClaude:           true,
		AllowLoopback:         true,
	}
}
