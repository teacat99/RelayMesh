package store

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"github.com/teacat99/RelayMesh/internal/model"
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
	return &cred, nil
}

func (s *Store) HasAnyCredential(ctx context.Context) (bool, error) {
	var count int64
	err := s.db.WithContext(ctx).Model(&model.MCPCredential{}).Count(&count).Error
	return count > 0, err
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

	return s.WithTx(ctx, func(tx *gorm.DB) error {
		var count int64
		if err := tx.Model(&model.MCPCredential{}).Count(&count).Error; err != nil {
			return err
		}
		if count >= CredentialMaxCount {
			return NewInvalidInputError(fmt.Sprintf("maximum %d credentials reached", CredentialMaxCount))
		}

		if cred.Token == "" {
			token, err := GenerateToken()
			if err != nil {
				return fmt.Errorf("failed to generate token: %w", err)
			}
			cred.Token = token
		}

		now := time.Now()
		cred.Name = name
		cred.CreatedAt = now
		cred.UpdatedAt = now
		return tx.Create(cred).Error
	})
}

func (s *Store) UpdateCredential(ctx context.Context, id uint, updates map[string]any) (*model.MCPCredential, error) {
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
	err := s.WithTx(ctx, func(tx *gorm.DB) error {
		if err := tx.Where("id = ?", id).First(&cred).Error; err != nil {
			if err == gorm.ErrRecordNotFound {
				return NewNotFoundError(fmt.Sprintf("credential %d not found", id))
			}
			return err
		}
		updates["updated_at"] = time.Now()
		if err := tx.Model(&cred).Updates(updates).Error; err != nil {
			return err
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
	token, err := GenerateToken()
	if err != nil {
		return nil, "", fmt.Errorf("failed to generate token: %w", err)
	}

	var cred model.MCPCredential
	err = s.WithTx(ctx, func(tx *gorm.DB) error {
		if err := tx.Where("id = ?", id).First(&cred).Error; err != nil {
			if err == gorm.ErrRecordNotFound {
				return NewNotFoundError(fmt.Sprintf("credential %d not found", id))
			}
			return err
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
