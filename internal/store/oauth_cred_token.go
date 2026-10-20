package store

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/teacat99/RelayMesh/v2/internal/model"
	"gorm.io/gorm"
)

const hiddenCredentialTokenBytes = 32

// GenerateHiddenCredentialToken 生成隐藏 OAuth 凭据的内部占位令牌（高熵、永不对外返回、不可作 MCP Bearer）。
func GenerateHiddenCredentialToken() (string, error) {
	b := make([]byte, hiddenCredentialTokenBytes)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// BackfillHiddenOAuthCredentialTokens 将可预测的 rm-oauth-internal-* 令牌重写为随机值（幂等）。
func (s *Store) BackfillHiddenOAuthCredentialTokens(ctx context.Context) error {
	var creds []model.MCPCredential
	if err := s.db.WithContext(ctx).Where("hidden = ?", true).Find(&creds).Error; err != nil {
		return err
	}
	for _, c := range creds {
		if !strings.HasPrefix(c.Token, "rm-oauth-internal-") {
			continue
		}
		tok, err := GenerateHiddenCredentialToken()
		if err != nil {
			return err
		}
		if err := s.db.WithContext(ctx).Model(&model.MCPCredential{}).Where("id = ?", c.ID).Update("token", tok).Error; err != nil {
			return err
		}
	}
	return nil
}

// ActivateCredential 启用凭据（OAuth 授权码首次兑换后）。
func (s *Store) ActivateCredential(ctx context.Context, id uint) error {
	return s.db.WithContext(ctx).Model(&model.MCPCredential{}).Where("id = ?", id).Update("is_active", true).Error
}

// GetCredentialIfVisible 按 ID 读取非隐藏凭据；隐藏凭据返回 gorm.ErrRecordNotFound 语义。
func (s *Store) GetCredentialIfVisible(ctx context.Context, id uint) (*model.MCPCredential, error) {
	var cred model.MCPCredential
	err := s.db.WithContext(ctx).Where("id = ? AND (hidden = ? OR hidden IS NULL)", id, false).First(&cred).Error
	if err == gorm.ErrRecordNotFound {
		return nil, fmt.Errorf("credential not found")
	}
	if err != nil {
		return nil, err
	}
	return &cred, nil
}
