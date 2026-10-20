package store

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"github.com/teacat99/RelayMesh/v2/internal/model"
	"gorm.io/gorm"
)

const (
	CredentialTokenPrefix = "rm-"
	CredentialTokenBytes  = 32
	CredentialNameMaxLen  = 128
	CredentialNoteMaxLen  = 512
	CredentialMaxCount    = 50
)

func GenerateToken() (string, error) {
	b := make([]byte, CredentialTokenBytes)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return CredentialTokenPrefix + hex.EncodeToString(b), nil
}

// generateTokenFor 按凭据类型生成令牌：能力 URL 凭据用专用前缀，其余用普通前缀。
func generateTokenFor(capability bool) (string, error) {
	if capability {
		return GenerateCapabilityToken()
	}
	return GenerateToken()
}

func MaskToken(token string) string {
	if len(token) <= 10 {
		return "****"
	}
	return token[:6] + "****" + token[len(token)-4:]
}

func (s *Store) FindCredentialByToken(ctx context.Context, token string) (*model.MCPCredential, error) {
	var cred model.MCPCredential
	err := s.db.WithContext(ctx).Where("token = ?", token).First(&cred).Error
	if err == gorm.ErrRecordNotFound {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if cred.Hidden {
		return nil, nil
	}
	return &cred, nil
}

func (s *Store) HasAnyCredential(ctx context.Context) (bool, error) {
	var count int64
	err := s.db.WithContext(ctx).Model(&model.MCPCredential{}).Count(&count).Error
	return count > 0, err
}

func (s *Store) syncLegacySessionsToCredential(ctx context.Context, cred *model.MCPCredential) {
	if cred == nil || cred.ID == 0 {
		return
	}
	// 1. 将此前未绑定凭据（credential_id 为空或 0）的历史会话全部回填归属至该凭据
	_ = s.db.WithContext(ctx).Model(&model.FeedbackSession{}).
		Where("credential_id IS NULL OR credential_id = 0").
		Update("credential_id", cred.ID).Error

	// 2. 若该凭据已配置了自定义主机名（且非空），将历史会话的主机名同步刷新为最新主机名
	if cred.HostName != "" {
		_ = s.db.WithContext(ctx).Model(&model.FeedbackSession{}).
			Where("credential_id = ?", cred.ID).
			Update("host_name", cred.HostName).Error
	}
}

// EnsureEnvMCPCredential 确保环境变量 RELAYMESH_MCP_TOKEN 在数据库中存在对应的可管理凭据
func (s *Store) EnsureEnvMCPCredential(ctx context.Context, envToken string) (*model.MCPCredential, error) {
	envToken = strings.TrimSpace(envToken)
	if envToken == "" {
		return nil, nil
	}

	var resCred *model.MCPCredential
	var cred model.MCPCredential
	err := s.db.WithContext(ctx).Where("token = ?", envToken).First(&cred).Error
	if err == nil {
		resCred = &cred
	} else if err != gorm.ErrRecordNotFound {
		return nil, err
	} else {
		// 检查是否已有同名旧记录因 Token 变更而需更新
		var existingEnv model.MCPCredential
		if err := s.db.WithContext(ctx).Where("name = ?", "MCP Token (环境变量)").First(&existingEnv).Error; err == nil {
			existingEnv.Token = envToken
			existingEnv.UpdatedAt = time.Now()
			if err := s.db.WithContext(ctx).Save(&existingEnv).Error; err != nil {
				return nil, err
			}
			resCred = &existingEnv
		} else {
			now := time.Now()
			newCred := model.MCPCredential{
				Name:        "MCP Token (环境变量)",
				Token:       envToken,
				IsActive:    true,
				Permissions: model.AllPermissions(),
				Note:        "通过 RELAYMESH_MCP_TOKEN 环境变量配置",
				CreatedAt:   now,
				UpdatedAt:   now,
			}
			if err := s.db.WithContext(ctx).Create(&newCred).Error; err != nil {
				return nil, err
			}
			resCred = &newCred
		}
	}

	if resCred != nil {
		s.syncLegacySessionsToCredential(ctx, resCred)
	}
	return resCred, nil
}

func (s *Store) ListCredentials(ctx context.Context) ([]model.MCPCredential, error) {
	var creds []model.MCPCredential
	err := s.db.WithContext(ctx).Order("created_at ASC").Find(&creds).Error
	return creds, err
}

func (s *Store) GetCredential(ctx context.Context, id uint) (*model.MCPCredential, error) {
	var cred model.MCPCredential
	err := s.db.WithContext(ctx).Where("id = ?", id).First(&cred).Error
	if err == gorm.ErrRecordNotFound {
		return nil, NewNotFoundError(fmt.Sprintf("credential %d not found", id))
	}
	return &cred, err
}

func (s *Store) CreateCredential(ctx context.Context, cred *model.MCPCredential) error {
	name := strings.TrimSpace(cred.Name)
	if name == "" {
		return NewInvalidInputError("name is required")
	}
	if len(name) > CredentialNameMaxLen {
		return NewInvalidInputError(fmt.Sprintf("name exceeds %d characters", CredentialNameMaxLen))
	}
	if len(cred.Note) > CredentialNoteMaxLen {
		return NewInvalidInputError(fmt.Sprintf("note exceeds %d characters", CredentialNoteMaxLen))
	}
	if err := ValidateCredentialTerminal(cred); err != nil {
		return err
	}
	if err := validateCapabilityOnCreate(cred); err != nil {
		return err
	}
	// 使用统计由服务端维护，创建时不接受客户端预填。
	cred.LastUsedAt = nil
	cred.LastUsedIP = ""

	return s.WithTx(ctx, func(tx *gorm.DB) error {
		var count int64
		if err := tx.Model(&model.MCPCredential{}).Where("hidden = ? OR hidden IS NULL", false).Count(&count).Error; err != nil {
			return err
		}
		if count >= CredentialMaxCount {
			return NewInvalidInputError(fmt.Sprintf("maximum %d credentials reached", CredentialMaxCount))
		}

		if cred.Token == "" {
			token, err := generateTokenFor(cred.Capability)
			if err != nil {
				return fmt.Errorf("failed to generate token: %w", err)
			}
			cred.Token = token
		}
		if err := CredentialTerminalTokenError(cred); err != nil {
			return err
		}

		now := time.Now()
		cred.Name = name
		cred.CreatedAt = now
		cred.UpdatedAt = now
		return tx.Create(cred).Error
	})
}

func (s *Store) UpdateCredential(ctx context.Context, id uint, updates map[string]any) (*model.MCPCredential, error) {
	updates, err := SanitizeCredentialUpdates(updates)
	if err != nil {
		return nil, err
	}
	if v, ok := updates["name"]; ok {
		name, _ := v.(string)
		if strings.TrimSpace(name) == "" {
			return nil, NewInvalidInputError("name cannot be empty")
		}
		if len(name) > CredentialNameMaxLen {
			return nil, NewInvalidInputError(fmt.Sprintf("name exceeds %d characters", CredentialNameMaxLen))
		}
	}
	if v, ok := updates["note"]; ok {
		note, _ := v.(string)
		if len(note) > CredentialNoteMaxLen {
			return nil, NewInvalidInputError(fmt.Sprintf("note exceeds %d characters", CredentialNoteMaxLen))
		}
	}

	var cred model.MCPCredential
	err = s.WithTx(ctx, func(tx *gorm.DB) error {
		if err := tx.Where("id = ?", id).First(&cred).Error; err != nil {
			if err == gorm.ErrRecordNotFound {
				return NewNotFoundError(fmt.Sprintf("credential %d not found", id))
			}
			return err
		}
		if err := validateCapabilityOnUpdate(&cred, updates); err != nil {
			return err
		}
		// 本次更新会写入权限时，若结果带终端权限，则令牌必须足够强（服务端生成的令牌始终合格）。
		if permVal, ok := updates["permissions"]; ok {
			if perms, isPerms := permVal.(model.Permissions); isPerms {
				probe := cred
				probe.Permissions = perms
				if err := CredentialTerminalTokenError(&probe); err != nil {
					return err
				}
			}
		}
		// 文件权限与允许目录必须同时成立：按「更新之后的样子」整体校验，避免分两次请求绕过。
		{
			probe := cred
			if permVal, ok := updates["permissions"]; ok {
				if perms, isPerms := permVal.(model.Permissions); isPerms {
					probe.Permissions = perms
				}
			}
			if limVal, ok := updates["terminal_limits"]; ok {
				if lim, isLim := limVal.(model.TerminalLimits); isLim {
					probe.TerminalLimits = lim
				}
			}
			if err := CredentialFilesError(&probe); err != nil {
				return err
			}
		}
		updates["updated_at"] = time.Now()
		if err := tx.Model(&cred).Updates(updates).Error; err != nil {
			return err
		}
		// 若更新了 host_name，则同步追溯更新所有使用该凭据 (CredentialID) 的历史会话
		if hnVal, ok := updates["host_name"]; ok {
			newHostName, _ := hnVal.(string)
			newHostName = strings.TrimSpace(newHostName)
			// 将未绑定凭据的历史会话绑定至此凭据
			_ = tx.Model(&model.FeedbackSession{}).
				Where("credential_id IS NULL OR credential_id = 0").
				Update("credential_id", id).Error

			// 追溯级联更新同 Token (CredentialID) 的所有历史会话的主机名
			if err := tx.Model(&model.FeedbackSession{}).
				Where("credential_id = ?", id).
				Update("host_name", newHostName).Error; err != nil {
				return err
			}
		}
		return tx.Where("id = ?", id).First(&cred).Error
	})
	return &cred, err
}

func (s *Store) DeleteCredential(ctx context.Context, id uint) error {
	return s.WithTx(ctx, func(tx *gorm.DB) error {
		result := tx.Where("id = ?", id).Delete(&model.MCPCredential{})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return NewNotFoundError(fmt.Sprintf("credential %d not found", id))
		}
		return nil
	})
}

func (s *Store) RegenerateCredentialToken(ctx context.Context, id uint) (*model.MCPCredential, string, error) {
	var cred model.MCPCredential
	var token string
	err := s.WithTx(ctx, func(tx *gorm.DB) error {
		if err := tx.Where("id = ?", id).First(&cred).Error; err != nil {
			if err == gorm.ErrRecordNotFound {
				return NewNotFoundError(fmt.Sprintf("credential %d not found", id))
			}
			return err
		}
		var genErr error
		if token, genErr = generateTokenFor(cred.Capability); genErr != nil {
			return fmt.Errorf("failed to generate token: %w", genErr)
		}
		cred.Token = token
		cred.UpdatedAt = time.Now()
		return tx.Save(&cred).Error
	})
	if err != nil {
		return nil, "", err
	}
	return &cred, token, nil
}
