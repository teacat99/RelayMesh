package store

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"gorm.io/gorm"

	"github.com/teacat99/RelayMesh/v2/internal/model"
	"github.com/teacat99/RelayMesh/v2/internal/notify"
)

// NotifyPrefsKey 是通知偏好在 system_settings 表里的键，值是 JSON（见 notify.Prefs）。
// 只新增键、不改表结构；旧版本二进制不认识这个键，会直接忽略它。
const NotifyPrefsKey = "notify.prefs"

// GetNotifyPrefs 读取通知偏好；从未设置过、或存下来的值已损坏时返回出厂默认。已 Normalize（过期的静音不再出现）。
func (s *Store) GetNotifyPrefs(ctx context.Context) (notify.Prefs, error) {
	var rec model.SystemSetting
	err := s.db.WithContext(ctx).Where("key = ?", NotifyPrefsKey).First(&rec).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return notify.Prefs{}.Normalize(time.Now()), nil
		}
		return notify.Prefs{}, err
	}
	return decodeNotifyPrefs(rec.Value), nil
}

// UpdateNotifyPrefs 在一个事务里「读—改—写」：mutate 拿到当前偏好、返回新偏好（或返回错误拒绝这次修改）。
// 新偏好经 Normalize 与 Validate 之后才写入；校验不过返回包装了 notify.ErrInvalidPrefs 的错误，库里保持原样。
func (s *Store) UpdateNotifyPrefs(ctx context.Context, mutate func(cur notify.Prefs) (notify.Prefs, error)) (notify.Prefs, error) {
	var out notify.Prefs
	err := s.WithTx(ctx, func(tx *gorm.DB) error {
		cur := notify.Prefs{}.Normalize(time.Now())
		var rec model.SystemSetting
		if err := tx.Where("key = ?", NotifyPrefsKey).First(&rec).Error; err == nil {
			cur = decodeNotifyPrefs(rec.Value)
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}

		next, err := mutate(cur)
		if err != nil {
			return err
		}
		next = next.Normalize(time.Now())
		if err := next.Validate(); err != nil {
			return err
		}
		raw, err := json.Marshal(next)
		if err != nil {
			return err
		}
		if err := tx.Save(&model.SystemSetting{Key: NotifyPrefsKey, Value: string(raw), UpdatedAt: time.Now()}).Error; err != nil {
			return err
		}
		out = next
		return nil
	})
	return out, err
}

// decodeNotifyPrefs 容忍损坏的值：解析失败按出厂默认处理。
// 默认意味着「免打扰关、没有静音」——宁可多通知，也不因为一个坏值把关键通知吞掉。
func decodeNotifyPrefs(raw string) notify.Prefs {
	var p notify.Prefs
	if json.Unmarshal([]byte(raw), &p) != nil {
		return notify.Prefs{}.Normalize(time.Now())
	}
	return p.Normalize(time.Now())
}
