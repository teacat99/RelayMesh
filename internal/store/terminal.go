package store

import (
	"context"
	"errors"
	"time"

	"github.com/teacat99/RelayMesh/v2/internal/model"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// ErrTerminalHostExists 表示该机器码已存在记录（同一公钥）。
var ErrTerminalHostExists = errors.New("terminal host already exists")

// ErrTerminalHostNotFound 表示记录不存在。
var ErrTerminalHostNotFound = errors.New("terminal host not found")

// ErrTerminalKeyMismatch 表示同一机器码对应的已存公钥与传入公钥不一致（不应发生，发生即拒绝）。
var ErrTerminalKeyMismatch = errors.New("terminal host public key mismatch")

// TerminalHostSeen 是每次握手成功后要刷新的「自报且已净化」字段。
type TerminalHostSeen struct {
	Hostname     string
	OS           string
	Arch         string
	AgentVersion string
	AgentRev     string
	Privilege    string
	IP           string
	At           time.Time
}

// GetTerminalHost 按机器码读取主机，不存在返回 (nil, nil)。
func (s *Store) GetTerminalHost(ctx context.Context, machineCode string) (*model.TerminalHost, error) {
	var h model.TerminalHost
	err := s.db.WithContext(ctx).Where("machine_code = ?", machineCode).First(&h).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return &h, nil
}

// ListTerminalHosts 返回全部主机（按创建时间升序）。
func (s *Store) ListTerminalHosts(ctx context.Context) ([]model.TerminalHost, error) {
	var hosts []model.TerminalHost
	if err := s.db.WithContext(ctx).Order("created_at ASC, id ASC").Find(&hosts).Error; err != nil {
		return nil, err
	}
	return hosts, nil
}

// SaveTerminalHost 在事务内创建或更新主机状态，并校验公钥一致：
//   - 不存在：按传入内容创建；
//   - 已存在：公钥必须逐字节一致，否则返回 ErrTerminalKeyMismatch；更新状态 / 备注 / 自报字段。
//
// enabledAt 非 nil 时写入启用时间。
func (s *Store) SaveTerminalHost(ctx context.Context, h *model.TerminalHost) error {
	return s.WithTx(ctx, func(tx *gorm.DB) error {
		var existing model.TerminalHost
		err := tx.Where("machine_code = ?", h.MachineCode).First(&existing).Error
		switch {
		case errors.Is(err, gorm.ErrRecordNotFound):
			return tx.Create(h).Error
		case err != nil:
			return err
		}
		if existing.PublicKey != h.PublicKey {
			return ErrTerminalKeyMismatch
		}
		existing.Status = h.Status
		existing.Remark = h.Remark
		existing.Hostname = h.Hostname
		existing.OS = h.OS
		existing.Arch = h.Arch
		existing.AgentVersion = h.AgentVersion
		existing.AgentRev = h.AgentRev
		existing.Privilege = h.Privilege
		existing.LastIP = h.LastIP
		existing.LastSeenAt = h.LastSeenAt
		if h.EnabledAt != nil {
			existing.EnabledAt = h.EnabledAt
		}
		if err := tx.Save(&existing).Error; err != nil {
			return err
		}
		*h = existing
		return nil
	})
}

// TouchTerminalHost 刷新主机的最近在线信息（握手成功后调用）。主机不存在时返回 ErrTerminalHostNotFound。
func (s *Store) TouchTerminalHost(ctx context.Context, machineCode string, seen TerminalHostSeen) error {
	return s.WithTx(ctx, func(tx *gorm.DB) error {
		at := seen.At
		res := tx.Model(&model.TerminalHost{}).Where("machine_code = ?", machineCode).Updates(map[string]any{
			"hostname":      seen.Hostname,
			"os":            seen.OS,
			"arch":          seen.Arch,
			"agent_version": seen.AgentVersion,
			"agent_rev":     seen.AgentRev,
			"privilege":     seen.Privilege,
			"last_ip":       seen.IP,
			"last_seen_at":  &at,
			"updated_at":    at,
		})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			return ErrTerminalHostNotFound
		}
		return nil
	})
}

// SetTerminalHostRemark 修改备注。
func (s *Store) SetTerminalHostRemark(ctx context.Context, machineCode, remark string) error {
	return s.WithTx(ctx, func(tx *gorm.DB) error {
		res := tx.Model(&model.TerminalHost{}).Where("machine_code = ?", machineCode).
			Updates(map[string]any{"remark": remark, "updated_at": time.Now()})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			return ErrTerminalHostNotFound
		}
		return nil
	})
}

// SetTerminalHostStatus 修改状态（enabled / disabled / blocked），返回是否真的发生了变化。
func (s *Store) SetTerminalHostStatus(ctx context.Context, machineCode, status string) (changed bool, err error) {
	err = s.WithTx(ctx, func(tx *gorm.DB) error {
		var h model.TerminalHost
		if e := tx.Where("machine_code = ?", machineCode).First(&h).Error; e != nil {
			if errors.Is(e, gorm.ErrRecordNotFound) {
				return ErrTerminalHostNotFound
			}
			return e
		}
		if h.Status == status {
			return nil
		}
		now := time.Now()
		updates := map[string]any{"status": status, "updated_at": now}
		if status == "enabled" {
			updates["enabled_at"] = &now
		}
		if e := tx.Model(&model.TerminalHost{}).Where("id = ?", h.ID).Updates(updates).Error; e != nil {
			return e
		}
		changed = true
		return nil
	})
	return changed, err
}

// DeleteTerminalHost 删除记录，返回是否删除了记录。
func (s *Store) DeleteTerminalHost(ctx context.Context, machineCode string) (bool, error) {
	var deleted bool
	err := s.WithTx(ctx, func(tx *gorm.DB) error {
		res := tx.Where("machine_code = ?", machineCode).Delete(&model.TerminalHost{})
		if res.Error != nil {
			return res.Error
		}
		deleted = res.RowsAffected > 0
		return nil
	})
	return deleted, err
}

// AppendTerminalAudit 追加一条审计记录。
func (s *Store) AppendTerminalAudit(ctx context.Context, e *model.TerminalAuditLog) error {
	if e.CreatedAt.IsZero() {
		e.CreatedAt = time.Now()
	}
	return s.WithTx(ctx, func(tx *gorm.DB) error {
		return tx.Create(e).Error
	})
}

// ListTerminalAudit 按 ID 倒序返回审计记录；beforeID>0 时只返回更早的记录（游标翻页）。
func (s *Store) ListTerminalAudit(ctx context.Context, limit int, beforeID uint) ([]model.TerminalAuditLog, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	q := s.db.WithContext(ctx).Order(clause.OrderByColumn{Column: clause.Column{Name: "id"}, Desc: true}).Limit(limit)
	if beforeID > 0 {
		q = q.Where("id < ?", beforeID)
	}
	var logs []model.TerminalAuditLog
	if err := q.Find(&logs).Error; err != nil {
		return nil, err
	}
	return logs, nil
}

// ListTerminalAuditSince 返回某个事件在 since 之后的审计记录，按时间正序排好；result 非空时只取这个结果。
// 项目管理 Agent 经 MCP 运行预设（#179）的频率账本读它：次数来自审计记录，所以 Hub 重启不会清零。
//
// 条数超过 limit（默认 100，上限 500）时留下的是**最新**的 limit 条、丢的是最旧的：账本只关心最近的次数，
// 反过来（留最旧的）会让一大批被拒绝的尝试把成功的记录挤出窗口，限额就此失效。
// 事件名带索引；记录总量受 PruneTerminalAudit 的保留条数限制。
func (s *Store) ListTerminalAuditSince(ctx context.Context, event, result string, since time.Time, limit int) ([]model.TerminalAuditLog, error) {
	if limit <= 0 {
		limit = 100
	}
	if limit > 500 {
		limit = 500
	}
	q := s.db.WithContext(ctx).Where("event = ? AND created_at >= ?", event, since)
	if result != "" {
		q = q.Where("result = ?", result)
	}
	var logs []model.TerminalAuditLog
	if err := q.Order(clause.OrderByColumn{Column: clause.Column{Name: "id"}, Desc: true}).Limit(limit).Find(&logs).Error; err != nil {
		return nil, err
	}
	for i, j := 0, len(logs)-1; i < j; i, j = i+1, j-1 { // 查出来是新的在前，翻成时间正序
		logs[i], logs[j] = logs[j], logs[i]
	}
	return logs, nil
}

// PruneTerminalAudit 只保留最近 keep 条审计记录，返回删除条数。
func (s *Store) PruneTerminalAudit(ctx context.Context, keep int) (int64, error) {
	if keep <= 0 {
		keep = 20000
	}
	var removed int64
	err := s.WithTx(ctx, func(tx *gorm.DB) error {
		var maxID uint
		if err := tx.Model(&model.TerminalAuditLog{}).Select("COALESCE(MAX(id), 0)").Scan(&maxID).Error; err != nil {
			return err
		}
		if maxID <= uint(keep) {
			return nil
		}
		// ID 自增且只追加：保留 (maxID-keep, maxID]。
		res := tx.Where("id <= ?", maxID-uint(keep)).Delete(&model.TerminalAuditLog{})
		removed = res.RowsAffected
		return res.Error
	})
	return removed, err
}
