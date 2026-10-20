package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"time"

	"github.com/teacat99/RelayMesh/v2/internal/model"
	"gorm.io/gorm"
)

const (
	oauthPolicyKey = "oauth_policy"
	OAuthClientMax = 200
)

func (s *Store) GetOAuthPolicy(ctx context.Context) (model.OAuthPolicy, error) {
	var setting model.SystemSetting
	err := s.db.WithContext(ctx).Where("key = ?", oauthPolicyKey).First(&setting).Error
	if err == gorm.ErrRecordNotFound {
		return model.DefaultOAuthPolicy(), nil
	}
	if err != nil {
		return model.OAuthPolicy{}, err
	}
	var p model.OAuthPolicy
	if err := json.Unmarshal([]byte(setting.Value), &p); err != nil {
		return model.DefaultOAuthPolicy(), nil
	}
	if p.AccessTokenTTLMinutes <= 0 {
		p.AccessTokenTTLMinutes = 60
	}
	if p.RefreshIdleDays <= 0 {
		p.RefreshIdleDays = 30
	}
	if p.RefreshAbsoluteDays <= 0 {
		p.RefreshAbsoluteDays = 90
	}
	return p, nil
}

func (s *Store) SaveOAuthPolicy(ctx context.Context, p model.OAuthPolicy) error {
	raw, err := json.Marshal(p)
	if err != nil {
		return err
	}
	return s.db.WithContext(ctx).Save(&model.SystemSetting{Key: oauthPolicyKey, Value: string(raw)}).Error
}

func (s *Store) CountOAuthClients(ctx context.Context) (int64, error) {
	var n int64
	err := s.db.WithContext(ctx).Model(&model.OAuthClient{}).Count(&n).Error
	return n, err
}

func (s *Store) CreateOAuthClient(ctx context.Context, c *model.OAuthClient) error {
	return s.db.WithContext(ctx).Create(c).Error
}

func (s *Store) GetOAuthClient(ctx context.Context, clientID string) (*model.OAuthClient, error) {
	var c model.OAuthClient
	err := s.db.WithContext(ctx).Where("client_id = ?", clientID).First(&c).Error
	if err == gorm.ErrRecordNotFound {
		return nil, nil
	}
	return &c, err
}

func (s *Store) TouchOAuthClient(ctx context.Context, clientID string) {
	now := time.Now()
	_ = s.db.WithContext(ctx).Model(&model.OAuthClient{}).Where("client_id = ?", clientID).Update("last_used_at", now).Error
}

// PurgeUnusedOAuthClients 删除从未成功授权且超过 maxAge 的 DCR 客户端。
func (s *Store) PurgeUnusedOAuthClients(ctx context.Context, maxAge time.Duration) error {
	cut := time.Now().Add(-maxAge)
	return s.db.WithContext(ctx).
		Where("last_used_at IS NULL AND created_at < ?", cut).
		Delete(&model.OAuthClient{}).Error
}

// RecycleOldestUnusedOAuthClient 回收最老的从未使用客户端，成功返回 true。
func (s *Store) RecycleOldestUnusedOAuthClient(ctx context.Context) bool {
	var cl model.OAuthClient
	err := s.db.WithContext(ctx).Where("last_used_at IS NULL").Order("created_at ASC").First(&cl).Error
	if err != nil {
		return false
	}
	if s.db.WithContext(ctx).Delete(&cl).Error != nil {
		return false
	}
	return true
}

func (s *Store) CreateOAuthGrant(ctx context.Context, g *model.OAuthGrant) error {
	return s.db.WithContext(ctx).Create(g).Error
}

func (s *Store) GetOAuthGrant(ctx context.Context, id uint) (*model.OAuthGrant, error) {
	var g model.OAuthGrant
	err := s.db.WithContext(ctx).First(&g, id).Error
	if err == gorm.ErrRecordNotFound {
		return nil, nil
	}
	return &g, err
}

func (s *Store) GetOAuthGrantByRefreshHash(ctx context.Context, hash string) (*model.OAuthGrant, error) {
	var g model.OAuthGrant
	err := s.db.WithContext(ctx).Where("refresh_token_hash = ?", hash).First(&g).Error
	if err == gorm.ErrRecordNotFound {
		// 宽限期：上一枚刷新令牌
		err = s.db.WithContext(ctx).Where("previous_refresh_hash = ?", hash).First(&g).Error
		if err == gorm.ErrRecordNotFound {
			return nil, nil
		}
	}
	if err != nil {
		return nil, err
	}
	return &g, nil
}

func (s *Store) ListOAuthGrants(ctx context.Context) ([]model.OAuthGrant, error) {
	var list []model.OAuthGrant
	err := s.db.WithContext(ctx).Order("created_at DESC").Find(&list).Error
	return list, err
}

func (s *Store) RevokeOAuthGrant(ctx context.Context, id uint) error {
	now := time.Now()
	return s.db.WithContext(ctx).Model(&model.OAuthGrant{}).Where("id = ? AND revoked_at IS NULL", id).
		Update("revoked_at", now).Error
}

func (s *Store) UpdateOAuthGrantRefresh(ctx context.Context, id uint, newHash string, graceUntil *time.Time) error {
	return s.db.WithContext(ctx).Model(&model.OAuthGrant{}).Where("id = ?", id).Updates(map[string]any{
		"previous_refresh_hash": gorm.Expr("refresh_token_hash"),
		"refresh_token_hash":    newHash,
		"refresh_grace_until":   graceUntil,
		"updated_at":            time.Now(),
	}).Error
}

func (s *Store) InvalidateOAuthGrantFamily(ctx context.Context, id uint) error {
	now := time.Now()
	return s.db.WithContext(ctx).Model(&model.OAuthGrant{}).Where("id = ?", id).Update("revoked_at", now).Error
}

func (s *Store) InsertOAuthAccessToken(ctx context.Context, hash string, grantID uint, expires time.Time) error {
	t := model.OAuthAccessToken{TokenHash: hash, GrantID: grantID, ExpiresAt: expires, CreatedAt: time.Now()}
	return s.db.WithContext(ctx).Create(&t).Error
}

func HashOAuthToken(plain string) string {
	sum := sha256.Sum256([]byte(plain))
	return hex.EncodeToString(sum[:])
}

func (s *Store) FindOAuthAccessToken(ctx context.Context, plainToken string) (*model.OAuthAccessToken, *model.OAuthGrant, *model.MCPCredential, error) {
	hash := HashOAuthToken(plainToken)
	var tok model.OAuthAccessToken
	err := s.db.WithContext(ctx).Where("token_hash = ?", hash).First(&tok).Error
	if err == gorm.ErrRecordNotFound {
		return nil, nil, nil, nil
	}
	if err != nil {
		return nil, nil, nil, err
	}
	if time.Now().After(tok.ExpiresAt) {
		return nil, nil, nil, nil
	}
	grant, err := s.GetOAuthGrant(ctx, tok.GrantID)
	if err != nil || grant == nil || grant.RevokedAt != nil {
		return nil, nil, nil, err
	}
	cred, err := s.GetCredential(ctx, grant.CredentialID)
	if err != nil || cred == nil || !cred.IsActive {
		return nil, nil, nil, err
	}
	return &tok, grant, cred, nil
}

func (s *Store) DeleteOAuthAccessTokensForGrant(ctx context.Context, grantID uint) error {
	return s.db.WithContext(ctx).Where("grant_id = ?", grantID).Delete(&model.OAuthAccessToken{}).Error
}

func (s *Store) CreateHiddenOAuthCredential(ctx context.Context, grantID uint, name string, cred *model.MCPCredential) error {
	cred.Hidden = true
	cred.OAuthSource = "oauth"
	gid := grantID
	cred.OAuthGrantID = &gid
	if cred.Token == "" {
		tok, err := GenerateHiddenCredentialToken()
		if err != nil {
			return err
		}
		cred.Token = tok
	}
	cred.Name = name
	return s.db.WithContext(ctx).Create(cred).Error
}

func (s *Store) ListVisibleCredentials(ctx context.Context) ([]model.MCPCredential, error) {
	var creds []model.MCPCredential
	err := s.db.WithContext(ctx).Where("hidden = ? OR hidden IS NULL", false).Order("created_at ASC").Find(&creds).Error
	return creds, err
}

func (s *Store) CountVisibleCredentials(ctx context.Context) (int64, error) {
	var n int64
	err := s.db.WithContext(ctx).Model(&model.MCPCredential{}).Where("hidden = ? OR hidden IS NULL", false).Count(&n).Error
	return n, err
}

func (s *Store) UpdateOAuthGrantCredential(ctx context.Context, grantID, credID uint) error {
	return s.db.WithContext(ctx).Model(&model.OAuthGrant{}).Where("id = ?", grantID).Update("credential_id", credID).Error
}

func (s *Store) UpdateOAuthGrantRefreshMeta(ctx context.Context, grantID uint, hash string, idle, abs *time.Time) error {
	return s.db.WithContext(ctx).Model(&model.OAuthGrant{}).Where("id = ?", grantID).Updates(map[string]any{
		"refresh_token_hash":  hash,
		"refresh_expires_at":  idle,
		"absolute_expires_at": abs,
	}).Error
}

func (s *Store) UpdateOAuthGrantRefreshIdle(ctx context.Context, grantID uint, idle *time.Time) error {
	return s.db.WithContext(ctx).Model(&model.OAuthGrant{}).Where("id = ?", grantID).Update("refresh_expires_at", idle).Error
}

func (s *Store) DeactivateCredential(ctx context.Context, id uint) error {
	return s.db.WithContext(ctx).Model(&model.MCPCredential{}).Where("id = ?", id).Update("is_active", false).Error
}
