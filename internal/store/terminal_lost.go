package store

import (
	"context"
	"time"

	"github.com/teacat99/RelayMesh/v2/internal/model"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// 主机页「会话已不存在」提示的忽略记录（第六批 #28）。
//
// 提示来自主机上会话守护进程的上报，守护进程每次连接都会再报一遍；所以「忽略」要由 Hub 记住。
// 记录只影响提示是否显示：不碰会话记录、审计与历史入口。

// LostDismissalKeepDays：忽略记录保留多久。守护进程自己也会淘汰很久以前的丢失会话，
// 过了这么久还没从主机的上报里消失的条目极少，宁可让它重新提醒一次，也不让这张表无限增长。
const LostDismissalKeepDays = 90

// ListTerminalLostDismissals 返回全部忽略记录：机器码 → 已忽略的会话 ID 集合。
func (s *Store) ListTerminalLostDismissals(ctx context.Context) (map[string]map[string]struct{}, error) {
	var rows []model.TerminalLostDismissal
	if err := s.db.WithContext(ctx).Select("machine_code", "sid").Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make(map[string]map[string]struct{})
	for _, r := range rows {
		set := out[r.MachineCode]
		if set == nil {
			set = make(map[string]struct{})
			out[r.MachineCode] = set
		}
		set[r.SID] = struct{}{}
	}
	return out, nil
}

// DismissTerminalLost 忽略一台主机上的若干条「会话已不存在」提示，返回实际新增的条数
// （重复忽略同一条不算变化，所以界面重复点击是安全的）。同时顺手清掉过期的忽略记录。
func (s *Store) DismissTerminalLost(ctx context.Context, machineCode string, sids []string, now time.Time) (int64, error) {
	if machineCode == "" || len(sids) == 0 {
		return 0, nil
	}
	var changed int64
	err := s.WithTx(ctx, func(tx *gorm.DB) error {
		rows := make([]model.TerminalLostDismissal, len(sids))
		for i, sid := range sids {
			rows[i] = model.TerminalLostDismissal{MachineCode: machineCode, SID: sid, DismissedAt: now}
		}
		res := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&rows)
		if res.Error != nil {
			return res.Error
		}
		changed = res.RowsAffected
		cutoff := now.AddDate(0, 0, -LostDismissalKeepDays)
		return tx.Where("dismissed_at < ?", cutoff).Delete(&model.TerminalLostDismissal{}).Error
	})
	return changed, err
}

// DeleteTerminalLostDismissalsOfHost 删除一台主机的全部忽略记录（删除主机时调用）。
func (s *Store) DeleteTerminalLostDismissalsOfHost(ctx context.Context, machineCode string) error {
	return s.db.WithContext(ctx).Where("machine_code = ?", machineCode).Delete(&model.TerminalLostDismissal{}).Error
}
