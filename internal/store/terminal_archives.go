package store

import (
	"context"
	"time"

	"github.com/teacat99/RelayMesh/v2/internal/model"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// MaxTerminalArchives 是归档记录的总数上限。超过时丢弃最早归档的几条：
// 代价只是对应的条目重新出现在侧栏里，不会丢任何会话、记录或审计。
const MaxTerminalArchives = 5000

// ListTerminalArchives 返回全部已归档条目的 key（最近归档的在前）。
func (s *Store) ListTerminalArchives(ctx context.Context) ([]string, error) {
	keys := []string{}
	err := s.db.WithContext(ctx).Model(&model.TerminalArchive{}).
		Order("archived_at DESC, item_key ASC").
		Pluck("item_key", &keys).Error
	return keys, err
}

// SetTerminalArchived 归档（archived=true）或取消归档一批条目，返回实际发生变化的条数：
// 重复归档、取消并未归档的条目都不算变化，所以界面上重复点击是安全的。
func (s *Store) SetTerminalArchived(ctx context.Context, keys []string, archived bool, now time.Time) (int64, error) {
	if len(keys) == 0 {
		return 0, nil
	}
	var changed int64
	err := s.WithTx(ctx, func(tx *gorm.DB) error {
		if !archived {
			res := tx.Where("item_key IN ?", keys).Delete(&model.TerminalArchive{})
			changed = res.RowsAffected
			return res.Error
		}
		rows := make([]model.TerminalArchive, len(keys))
		for i, k := range keys {
			rows[i] = model.TerminalArchive{ItemKey: k, ArchivedAt: now}
		}
		res := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&rows)
		if res.Error != nil {
			return res.Error
		}
		changed = res.RowsAffected

		var total int64
		if err := tx.Model(&model.TerminalArchive{}).Count(&total).Error; err != nil {
			return err
		}
		if over := total - MaxTerminalArchives; over > 0 {
			oldest := tx.Model(&model.TerminalArchive{}).Select("item_key").Order("archived_at ASC, item_key ASC").Limit(int(over))
			return tx.Where("item_key IN (?)", oldest).Delete(&model.TerminalArchive{}).Error
		}
		return nil
	})
	return changed, err
}

// DeleteTerminalArchivesOfHost 删除一台主机上人工会话的归档记录（删除主机时调用；key 以「机器码:」开头）。
func (s *Store) DeleteTerminalArchivesOfHost(ctx context.Context, machineCode string) error {
	prefix := machineCode + ":"
	return s.WithTx(ctx, func(tx *gorm.DB) error {
		return tx.Where("substr(item_key, 1, ?) = ?", len(prefix), prefix).Delete(&model.TerminalArchive{}).Error
	})
}
