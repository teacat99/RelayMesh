package store

import (
	"context"
	"errors"
	"time"

	"github.com/teacat99/RelayMesh/v2/internal/model"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// TerminalSessionSeen 是 Agent 报告的一个会话的快照（字段均已净化）。
// 只含标签与录制索引，不含任何终端内容。
type TerminalSessionSeen struct {
	SID       string
	Name      string
	Shell     string
	StartedAt int64
	Exited    bool
	ExitCode  *int
	Rec       string
	RecBytes  int64
	RecFirst  int64
	RecLast   int64
	RecGap    bool
}

// 会话结束原因的精确程度：推断出来的 gone（清单里不再有它）最弱，exit（进程退出，不知道是谁结束的）次之，
// 其余都是明确的原因（killed / host_reboot / holder_stopped / idle_ttl / policy …）。
// 弱原因随时可被更精确的原因覆盖，反过来则不行。
const (
	terminalEndedGone = "gone"
	terminalEndedExit = "exit"
)

// SyncTerminalSessions 用一台主机当前的会话清单更新索引：新会话建行、已有会话刷新标签与录制索引、
// 已退出的会话记下结束；清单里不再有的运行中会话视为已结束（原因 gone，之后可被精确原因覆盖）。
// 返回写入（新建或更新）的行数。
func (s *Store) SyncTerminalSessions(ctx context.Context, machineCode string, seen []TerminalSessionSeen, now time.Time) (int, error) {
	changed := 0
	err := s.WithTx(ctx, func(tx *gorm.DB) error {
		var rows []model.TerminalSession
		if err := tx.Where("machine_code = ?", machineCode).Find(&rows).Error; err != nil {
			return err
		}
		have := make(map[string]*model.TerminalSession, len(rows))
		for i := range rows {
			have[rows[i].SID] = &rows[i]
		}
		present := make(map[string]struct{}, len(seen))
		for _, in := range seen {
			present[in.SID] = struct{}{}
			row, ok := have[in.SID]
			if !ok {
				row = &model.TerminalSession{SID: in.SID, MachineCode: machineCode}
			}
			if !applySeen(row, in, now) && ok {
				continue
			}
			if err := tx.Save(row).Error; err != nil {
				return err
			}
			changed++
		}
		for i := range rows {
			r := &rows[i]
			if _, ok := present[r.SID]; ok || r.EndedAt != nil {
				continue
			}
			t := now
			r.EndedAt, r.EndedReason = &t, terminalEndedGone
			if err := tx.Save(r).Error; err != nil {
				return err
			}
			changed++
		}
		return nil
	})
	return changed, err
}

// applySeen 把快照合并进行，返回是否有任何字段变化。
func applySeen(r *model.TerminalSession, in TerminalSessionSeen, now time.Time) bool {
	changed := r.ID == 0
	set := func(dst *string, v string) {
		if *dst != v {
			*dst, changed = v, true
		}
	}
	setI := func(dst *int64, v int64) {
		if *dst != v {
			*dst, changed = v, true
		}
	}
	set(&r.Name, in.Name)
	set(&r.Shell, in.Shell)
	setI(&r.StartedAt, in.StartedAt)
	set(&r.RecState, in.Rec)
	setI(&r.RecBytes, in.RecBytes)
	setI(&r.RecFirst, in.RecFirst)
	setI(&r.RecLast, in.RecLast)
	if r.RecGap != in.RecGap {
		r.RecGap, changed = in.RecGap, true
	}
	switch {
	case in.Exited && r.EndedAt == nil:
		t := now
		r.EndedAt, r.EndedReason, r.ExitCode = &t, terminalEndedExit, in.ExitCode
		changed = true
	case in.Exited:
		// 已经被记为结束：保留（可能更精确的）原因，只补上退出码；「推断」出来的 gone 升级为 exit。
		if r.ExitCode == nil && in.ExitCode != nil {
			r.ExitCode, changed = in.ExitCode, true
		}
		if r.EndedReason == terminalEndedGone {
			r.EndedReason, changed = terminalEndedExit, true
		}
	case r.EndedAt != nil && r.EndedReason == terminalEndedGone:
		// 主机又报告了这个会话且仍在运行：之前「清单里不见了」只是推断，撤销。
		r.EndedAt, r.EndedReason, changed = nil, "", true
	}
	return changed
}

// TerminalSessionEnd 描述一个会话已经结束及原因。会话不在索引里时按这里的信息补建一行
// （例如主机重启后守护进程报告的「已丢失会话」：它们的记录仍在主机磁盘上，值得保留入口）。
type TerminalSessionEnd struct {
	SID       string
	Name      string
	StartedAt int64
	EndedAt   time.Time // 零值表示「现在」
	Reason    string
}

func weakEndReason(r string) bool {
	return r == "" || r == terminalEndedGone || r == terminalEndedExit
}

// RecordTerminalSessionEnds 把若干会话标记为已结束并记下（精确的）原因：
// 不在索引里的补建；尚未结束的记下结束时间；已结束但原因较弱（gone / exit）的改写为这个更精确的原因，
// 已有精确原因的不会被覆盖。
func (s *Store) RecordTerminalSessionEnds(ctx context.Context, machineCode string, ends []TerminalSessionEnd, now time.Time) error {
	if len(ends) == 0 {
		return nil
	}
	return s.WithTx(ctx, func(tx *gorm.DB) error {
		for _, e := range ends {
			at := e.EndedAt
			if at.IsZero() {
				at = now
			}
			var row model.TerminalSession
			err := tx.Where("machine_code = ? AND sid = ?", machineCode, e.SID).First(&row).Error
			switch {
			case err == nil:
				changed := false
				if row.EndedAt == nil {
					t := at
					row.EndedAt, changed = &t, true
				}
				if e.Reason != "" && weakEndReason(row.EndedReason) && row.EndedReason != e.Reason {
					row.EndedReason, changed = e.Reason, true
				}
				if row.Name == "" && e.Name != "" {
					row.Name, changed = e.Name, true
				}
				if row.StartedAt == 0 && e.StartedAt != 0 {
					row.StartedAt, changed = e.StartedAt, true
				}
				if changed {
					if err := tx.Save(&row).Error; err != nil {
						return err
					}
				}
			case errors.Is(err, gorm.ErrRecordNotFound):
				t := at
				row = model.TerminalSession{
					SID: e.SID, MachineCode: machineCode, Name: e.Name, StartedAt: e.StartedAt,
					EndedAt: &t, EndedReason: e.Reason,
				}
				if err := tx.Create(&row).Error; err != nil {
					return err
				}
			default:
				return err
			}
		}
		return nil
	})
}

// ListTerminalSessions 返回一台主机的会话索引（最近创建的在前）；endedOnly 时只返回已结束的。
// 记录已被主机清理（RecPurged）的已结束会话不再列出。
func (s *Store) ListTerminalSessions(ctx context.Context, machineCode string, endedOnly bool, limit int) ([]model.TerminalSession, error) {
	if limit <= 0 || limit > 1000 {
		limit = 200
	}
	q := s.db.WithContext(ctx).Where("machine_code = ?", machineCode).
		Order(clause.OrderByColumn{Column: clause.Column{Name: "id"}, Desc: true}).Limit(limit)
	if endedOnly {
		q = q.Where("ended_at IS NOT NULL AND rec_purged = ?", false)
	}
	var out []model.TerminalSession
	if err := q.Find(&out).Error; err != nil {
		return nil, err
	}
	return out, nil
}

// GetTerminalSession 按主机与会话 ID 读取一行；不存在返回 (nil, nil)。
func (s *Store) GetTerminalSession(ctx context.Context, machineCode, sid string) (*model.TerminalSession, error) {
	var r model.TerminalSession
	err := s.db.WithContext(ctx).Where("machine_code = ? AND sid = ?", machineCode, sid).First(&r).Error
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, nil
		}
		return nil, err
	}
	return &r, nil
}

// MarkTerminalSessionPurged 标记主机上的记录已不存在（过期或被清理）。
func (s *Store) MarkTerminalSessionPurged(ctx context.Context, machineCode, sid string) error {
	return s.WithTx(ctx, func(tx *gorm.DB) error {
		return tx.Model(&model.TerminalSession{}).
			Where("machine_code = ? AND sid = ?", machineCode, sid).
			Update("rec_purged", true).Error
	})
}

// PruneTerminalSessions 删除结束时间早于 cutoff 的会话索引，返回删除行数。
func (s *Store) PruneTerminalSessions(ctx context.Context, cutoff time.Time) (int64, error) {
	var n int64
	err := s.WithTx(ctx, func(tx *gorm.DB) error {
		res := tx.Where("ended_at IS NOT NULL AND ended_at < ?", cutoff).Delete(&model.TerminalSession{})
		n = res.RowsAffected
		return res.Error
	})
	return n, err
}

// DeleteTerminalSessionsOfHost 删除一台主机的全部会话索引（删除主机时调用）。
func (s *Store) DeleteTerminalSessionsOfHost(ctx context.Context, machineCode string) error {
	return s.WithTx(ctx, func(tx *gorm.DB) error {
		return tx.Where("machine_code = ?", machineCode).Delete(&model.TerminalSession{}).Error
	})
}

// SetTerminalHostIdle 设置主机上会话的默认生命周期（秒，0 = 永久保持），返回是否有变化。
func (s *Store) SetTerminalHostIdle(ctx context.Context, machineCode string, sec int) (changed bool, err error) {
	err = s.WithTx(ctx, func(tx *gorm.DB) error {
		var h model.TerminalHost
		if err := tx.Where("machine_code = ?", machineCode).First(&h).Error; err != nil {
			if err == gorm.ErrRecordNotFound {
				return ErrTerminalHostNotFound
			}
			return err
		}
		if h.IdleSec == sec {
			return nil
		}
		changed = true
		return tx.Model(&model.TerminalHost{}).Where("machine_code = ?", machineCode).Update("idle_sec", sec).Error
	})
	return changed, err
}

// SetTerminalHostRecOff 设置主机的「关闭终端记录」策略，返回是否有变化。
func (s *Store) SetTerminalHostRecOff(ctx context.Context, machineCode string, off bool) (changed bool, err error) {
	err = s.WithTx(ctx, func(tx *gorm.DB) error {
		var h model.TerminalHost
		if err := tx.Where("machine_code = ?", machineCode).First(&h).Error; err != nil {
			if err == gorm.ErrRecordNotFound {
				return ErrTerminalHostNotFound
			}
			return err
		}
		if h.RecOff == off {
			return nil
		}
		changed = true
		return tx.Model(&model.TerminalHost{}).Where("machine_code = ?", machineCode).Update("rec_off", off).Error
	})
	return changed, err
}
