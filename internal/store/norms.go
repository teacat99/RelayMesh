package store

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/teacat99/RelayMesh/internal/model"
	"gorm.io/gorm"
)

const (
	NormNameMaxLen    = 128
	NormSummaryMaxLen = 500
	NormContentMaxLen = 20000
	NormMaxCount      = 50
)

func (s *Store) ListUserNorms(ctx context.Context) ([]model.UserNorm, error) {
	var norms []model.UserNorm
	err := s.db.WithContext(ctx).Order("sort_order ASC, created_at ASC").Find(&norms).Error
	return norms, err
}

func (s *Store) ListActiveUserNorms(ctx context.Context) ([]model.UserNorm, error) {
	var norms []model.UserNorm
	err := s.db.WithContext(ctx).Where("is_active = ?", true).Order("sort_order ASC, created_at ASC").Find(&norms).Error
	return norms, err
}

func (s *Store) GetUserNorm(ctx context.Context, name string) (*model.UserNorm, error) {
	var norm model.UserNorm
	err := s.db.WithContext(ctx).Where("name = ?", name).First(&norm).Error
	if err == gorm.ErrRecordNotFound {
		return nil, NewNotFoundError(fmt.Sprintf("norm %q not found", name))
	}
	return &norm, err
}

func (s *Store) CreateUserNorm(ctx context.Context, norm *model.UserNorm) error {
	name := strings.TrimSpace(norm.Name)
	if name == "" {
		return NewInvalidInputError("name is required")
	}
	if len(name) > NormNameMaxLen {
		return NewInvalidInputError(fmt.Sprintf("name exceeds %d characters", NormNameMaxLen))
	}
	if len(norm.Summary) > NormSummaryMaxLen {
		return NewInvalidInputError(fmt.Sprintf("summary exceeds %d characters", NormSummaryMaxLen))
	}
	if len(norm.Content) > NormContentMaxLen {
		return NewInvalidInputError(fmt.Sprintf("content exceeds %d characters", NormContentMaxLen))
	}

	return s.WithTx(ctx, func(tx *gorm.DB) error {
		var count int64
		if err := tx.Model(&model.UserNorm{}).Count(&count).Error; err != nil {
			return err
		}
		if count >= NormMaxCount {
			return NewInvalidInputError(fmt.Sprintf("maximum %d norms reached", NormMaxCount))
		}

		var existing model.UserNorm
		if err := tx.Where("name = ?", name).First(&existing).Error; err == nil {
			return NewConflictError(fmt.Sprintf("norm %q already exists", name), 0)
		}

		now := time.Now()
		norm.Name = name
		norm.CreatedAt = now
		norm.UpdatedAt = now
		return tx.Create(norm).Error
	})
}

func (s *Store) UpdateUserNorm(ctx context.Context, name string, updates map[string]any) (*model.UserNorm, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, NewInvalidInputError("name is required")
	}

	if v, ok := updates["summary"]; ok {
		if s, ok := v.(string); ok && len(s) > NormSummaryMaxLen {
			return nil, NewInvalidInputError(fmt.Sprintf("summary exceeds %d characters", NormSummaryMaxLen))
		}
	}
	if v, ok := updates["content"]; ok {
		if s, ok := v.(string); ok && len(s) > NormContentMaxLen {
			return nil, NewInvalidInputError(fmt.Sprintf("content exceeds %d characters", NormContentMaxLen))
		}
	}

	var norm model.UserNorm
	err := s.WithTx(ctx, func(tx *gorm.DB) error {
		if err := tx.Where("name = ?", name).First(&norm).Error; err != nil {
			if err == gorm.ErrRecordNotFound {
				return NewNotFoundError(fmt.Sprintf("norm %q not found", name))
			}
			return err
		}
		updates["updated_at"] = time.Now()
		if err := tx.Model(&norm).Updates(updates).Error; err != nil {
			return err
		}
		return tx.Where("name = ?", name).First(&norm).Error
	})
	return &norm, err
}

func (s *Store) DeleteUserNorm(ctx context.Context, name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return NewInvalidInputError("name is required")
	}

	return s.WithTx(ctx, func(tx *gorm.DB) error {
		result := tx.Where("name = ?", name).Delete(&model.UserNorm{})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return NewNotFoundError(fmt.Sprintf("norm %q not found", name))
		}
		return nil
	})
}
