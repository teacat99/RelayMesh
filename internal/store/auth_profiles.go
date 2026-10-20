package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"gorm.io/gorm"

	"github.com/teacat99/RelayMesh/v2/internal/model"
)

// 授权档位（异步对话模式，见 model/workflow_mode.go）。
//
// 档位是全局的，工作流各自选当前用哪一档（workflow_modes.profile_id）。永远至少有一档、有且只有一档是默认：
// 默认档位不能删（要删先把别的档位设为默认），工作流没选或选的档位被删掉时都按默认档位处理——
// 这样回执里的 auth: 行任何时候都有内容可写。

const (
	defaultAuthProfileName  = "标准授权"
	defaultAuthProfileRules = "读与分析、跑测试、工作树内可回滚的改动与本地提交直接做；部署、推送远端、删数据、改凭据、不可逆迁移先用 interactive_feedback 阻塞确认；大改动先发计划确认卡（async_ask 的 kind:\"plan\"）等批准。"
)

// SeedDefaultAuthProfile 保证至少有一档默认档位；幂等。
func (s *Store) SeedDefaultAuthProfile(ctx context.Context) error {
	var n int64
	if err := s.db.WithContext(ctx).Model(&model.AuthProfile{}).Count(&n).Error; err != nil {
		return err
	}
	if n > 0 {
		return nil
	}
	now := time.Now()
	return s.db.WithContext(ctx).Create(&model.AuthProfile{
		Name: defaultAuthProfileName, Level: model.AuthLevelL1, Rules: defaultAuthProfileRules,
		IsDefault: true, CreatedAt: now, UpdatedAt: now,
	}).Error
}

// ListAuthProfiles 按排序列出全部档位（默认档位不一定排第一，由界面决定怎么展示）。
func (s *Store) ListAuthProfiles(ctx context.Context) ([]model.AuthProfile, error) {
	if err := s.SeedDefaultAuthProfile(ctx); err != nil {
		return nil, err
	}
	var rows []model.AuthProfile
	err := s.db.WithContext(ctx).Order("sort ASC, id ASC").Find(&rows).Error
	return rows, err
}

// GetAuthProfile 取一档；不存在返回 NotFound。
func (s *Store) GetAuthProfile(ctx context.Context, id uint) (*model.AuthProfile, error) {
	var p model.AuthProfile
	if err := s.db.WithContext(ctx).First(&p, id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, NewNotFoundError(fmt.Sprintf("授权档位 %d 不存在", id))
		}
		return nil, err
	}
	return &p, nil
}

// DefaultAuthProfile 返回默认档位；没有标记默认的（不应出现）就取排序最靠前的一档并顺手修正标记。
func (s *Store) DefaultAuthProfile(ctx context.Context) (*model.AuthProfile, error) {
	if err := s.SeedDefaultAuthProfile(ctx); err != nil {
		return nil, err
	}
	var p model.AuthProfile
	err := s.db.WithContext(ctx).Where("is_default = ?", true).Order("sort ASC, id ASC").First(&p).Error
	if err == nil {
		return &p, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}
	if err := s.db.WithContext(ctx).Order("sort ASC, id ASC").First(&p).Error; err != nil {
		return nil, err
	}
	p.IsDefault = true
	if err := s.db.WithContext(ctx).Model(&model.AuthProfile{}).Where("id = ?", p.ID).Update("is_default", true).Error; err != nil {
		return nil, err
	}
	return &p, nil
}

// ResolveAuthProfile 取工作流选用的档位：id 为 0 或档位已不存在时回退到默认档位。
func (s *Store) ResolveAuthProfile(ctx context.Context, id uint) (*model.AuthProfile, error) {
	if id != 0 {
		var p model.AuthProfile
		err := s.db.WithContext(ctx).First(&p, id).Error
		if err == nil {
			return &p, nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, err
		}
	}
	return s.DefaultAuthProfile(ctx)
}

// AuthProfileInput 是新建 / 修改档位的入参；修改时只改非 nil 的字段。
type AuthProfileInput struct {
	Name  *string
	Level *string
	Rules *string
	Sort  *int
}

func validateAuthProfileFields(name, level, rules string) error {
	if name == "" {
		return NewInvalidInputError("档位名称不能为空")
	}
	if strings.ContainsAny(name, "\r\n") {
		return NewInvalidInputError("档位名称不能换行")
	}
	if utf8.RuneCountInString(name) > model.AuthProfileNameMax {
		return NewInvalidInputError(fmt.Sprintf("档位名称不能超过 %d 个字", model.AuthProfileNameMax))
	}
	if level != model.AuthLevelL0 && level != model.AuthLevelL1 {
		return NewInvalidInputError("档位等级只能是 L0（只读，写入先问）或 L1（可回滚的改动直接做）；不可逆操作在任何档位下都要阻塞确认")
	}
	if rules == "" {
		return NewInvalidInputError("规则摘要不能为空：它是写给智能体看的，说明哪些事可以直接做、哪些先问")
	}
	if utf8.RuneCountInString(rules) > model.AuthProfileRulesMax {
		return NewInvalidInputError(fmt.Sprintf("规则摘要不能超过 %d 个字（回执里的 auth: 行整行有 300 字上限）", model.AuthProfileRulesMax))
	}
	return nil
}

func isUniqueViolation(err error) bool {
	return err != nil && strings.Contains(strings.ToLower(err.Error()), "unique")
}

// CreateAuthProfile 新增一档。名称不能重复，总数有上限；新档位不是默认。
func (s *Store) CreateAuthProfile(ctx context.Context, in AuthProfileInput) (*model.AuthProfile, error) {
	name, level, rules := "", model.AuthLevelL1, ""
	if in.Name != nil {
		name = strings.TrimSpace(*in.Name)
	}
	if in.Level != nil {
		level = strings.ToUpper(strings.TrimSpace(*in.Level))
	}
	if in.Rules != nil {
		rules = strings.TrimSpace(*in.Rules)
	}
	if err := validateAuthProfileFields(name, level, rules); err != nil {
		return nil, err
	}
	var created model.AuthProfile
	err := s.WithTx(ctx, func(tx *gorm.DB) error {
		var n int64
		if err := tx.Model(&model.AuthProfile{}).Count(&n).Error; err != nil {
			return err
		}
		if n >= model.AuthProfileMax {
			return NewConflictError(fmt.Sprintf("授权档位最多 %d 档，请先删掉不用的", model.AuthProfileMax), 0)
		}
		now := time.Now()
		sort := int(n)
		if in.Sort != nil {
			sort = *in.Sort
		}
		created = model.AuthProfile{Name: name, Level: level, Rules: rules, Sort: sort, CreatedAt: now, UpdatedAt: now}
		if err := tx.Create(&created).Error; err != nil {
			if isUniqueViolation(err) {
				return NewConflictError(fmt.Sprintf("已有名为「%s」的档位", name), 0)
			}
			return err
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &created, nil
}

// UpdateAuthProfile 修改一档（含默认档位）：只改入参里给了的字段。
func (s *Store) UpdateAuthProfile(ctx context.Context, id uint, in AuthProfileInput) (*model.AuthProfile, error) {
	var out model.AuthProfile
	err := s.WithTx(ctx, func(tx *gorm.DB) error {
		var p model.AuthProfile
		if err := tx.First(&p, id).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return NewNotFoundError(fmt.Sprintf("授权档位 %d 不存在", id))
			}
			return err
		}
		if in.Name != nil {
			p.Name = strings.TrimSpace(*in.Name)
		}
		if in.Level != nil {
			p.Level = strings.ToUpper(strings.TrimSpace(*in.Level))
		}
		if in.Rules != nil {
			p.Rules = strings.TrimSpace(*in.Rules)
		}
		if in.Sort != nil {
			p.Sort = *in.Sort
		}
		if err := validateAuthProfileFields(p.Name, p.Level, p.Rules); err != nil {
			return err
		}
		p.UpdatedAt = time.Now()
		if err := tx.Save(&p).Error; err != nil {
			if isUniqueViolation(err) {
				return NewConflictError(fmt.Sprintf("已有名为「%s」的档位", p.Name), 0)
			}
			return err
		}
		out = p
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// SetDefaultAuthProfile 把某一档设为默认（原来的默认档位随之变成普通档位，可以删除了）。
func (s *Store) SetDefaultAuthProfile(ctx context.Context, id uint) (*model.AuthProfile, error) {
	var out model.AuthProfile
	err := s.WithTx(ctx, func(tx *gorm.DB) error {
		var p model.AuthProfile
		if err := tx.First(&p, id).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return NewNotFoundError(fmt.Sprintf("授权档位 %d 不存在", id))
			}
			return err
		}
		if err := tx.Model(&model.AuthProfile{}).Where("id <> ? AND is_default = ?", id, true).Update("is_default", false).Error; err != nil {
			return err
		}
		p.IsDefault = true
		p.UpdatedAt = time.Now()
		if err := tx.Save(&p).Error; err != nil {
			return err
		}
		out = p
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// DeleteAuthProfile 删除一档。默认档位不能删；选用了这一档的工作流改回「跟随默认」（profile_id = 0）。
func (s *Store) DeleteAuthProfile(ctx context.Context, id uint) error {
	return s.WithTx(ctx, func(tx *gorm.DB) error {
		var p model.AuthProfile
		if err := tx.First(&p, id).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return NewNotFoundError(fmt.Sprintf("授权档位 %d 不存在", id))
			}
			return err
		}
		if p.IsDefault {
			return NewConflictError("默认档位不能删除：请先把另一档设为默认", 0)
		}
		if err := tx.Model(&model.WorkflowMode{}).Where("profile_id = ?", id).Update("profile_id", 0).Error; err != nil {
			return err
		}
		return tx.Delete(&p).Error
	})
}
