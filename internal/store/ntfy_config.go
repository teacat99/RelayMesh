package store

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"gorm.io/gorm"

	"github.com/teacat99/RelayMesh/v2/internal/model"
	"github.com/teacat99/RelayMesh/v2/internal/ntfy"
)

// NtfyConfigKey 是 ntfy 推送配置在 system_settings 表里的键，值是 JSON（见 ntfy.Config，含访问令牌 / 密码）。
//
// 它独立于全局设置（GlobalSettingsKey）：全局设置会整体返回给浏览器，而这里的凭据只许写、不许读。
const NtfyConfigKey = "ntfy_config"

// GetNtfyConfig 读取 ntfy 推送配置；从未设置过时返回关闭状态的默认值。已 Normalize。
func (s *Store) GetNtfyConfig(ctx context.Context) (ntfy.Config, error) {
	var rec model.SystemSetting
	err := s.db.WithContext(ctx).Where("key = ?", NtfyConfigKey).First(&rec).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ntfy.Config{}.Normalize(), nil
		}
		return ntfy.Config{}, err
	}
	return decodeNtfyConfig(rec.Value), nil
}

// UpdateNtfyConfig 在一个事务里「读—改—写」：mutate 拿到当前配置、返回新配置；
// 新配置经 Normalize 与校验后才写入，校验不过返回 ntfy.ErrInvalidConfig（不写库）。
// 启用时主题必填；关闭状态下允许保存只填了一半的配置。
func (s *Store) UpdateNtfyConfig(ctx context.Context, mutate func(cur ntfy.Config) ntfy.Config) (ntfy.Config, error) {
	var out ntfy.Config
	err := s.WithTx(ctx, func(tx *gorm.DB) error {
		cur := ntfy.Config{}.Normalize()
		var rec model.SystemSetting
		if err := tx.Where("key = ?", NtfyConfigKey).First(&rec).Error; err == nil {
			cur = decodeNtfyConfig(rec.Value)
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}

		next := mutate(cur).Normalize()
		if err := next.Validate(next.Enabled); err != nil {
			return err
		}
		raw, err := json.Marshal(next)
		if err != nil {
			return err
		}
		if err := tx.Save(&model.SystemSetting{Key: NtfyConfigKey, Value: string(raw), UpdatedAt: time.Now()}).Error; err != nil {
			return err
		}
		out = next
		return nil
	})
	return out, err
}

// decodeNtfyConfig 容忍损坏的值：解析失败按「未配置」处理（宁可不推送，也不要用半截配置去推送）。
func decodeNtfyConfig(raw string) ntfy.Config {
	var c ntfy.Config
	if json.Unmarshal([]byte(raw), &c) != nil {
		return ntfy.Config{}.Normalize()
	}
	return c.Normalize()
}
