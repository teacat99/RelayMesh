package store

import (
	"context"
	"errors"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/teacat99/RelayMesh/v2/internal/model"
)

// 系统开关（system_settings 键值表）里保存的终端相关键。
const (
	// SettingTerminalExecPaused 为 "1" 时全局暂停一切模型运行（G5 的全局开关，重启后保持）。
	SettingTerminalExecPaused = "terminal_exec_paused"
)

// RunFilter 是模型运行列表的过滤条件。
type RunFilter struct {
	Target       string
	ThreadKey    string
	CredentialID uint
	// CredentialName 与 Thread 按调用方名字与线程标签精确匹配（内部身份没有凭据编号，靠名字与标签认领自己的运行）。
	CredentialName string
	Thread         string
	States         []string
	BeforeID       uint
	Limit          int
}

// CreateTerminalRun 写入一条新的运行记录。
func (s *Store) CreateTerminalRun(ctx context.Context, r *model.TerminalRun) error {
	if r.CreatedAt.IsZero() {
		r.CreatedAt = time.Now()
	}
	return s.WithTx(ctx, func(tx *gorm.DB) error {
		return tx.Create(r).Error
	})
}

// UpdateTerminalRun 无条件更新一条运行记录的指定字段，返回是否命中了记录。
func (s *Store) UpdateTerminalRun(ctx context.Context, runID string, fields map[string]any) (bool, error) {
	if len(fields) == 0 {
		return false, nil
	}
	fields["updated_at"] = time.Now()
	var hit bool
	err := s.WithTx(ctx, func(tx *gorm.DB) error {
		res := tx.Model(&model.TerminalRun{}).Where("run_id = ?", runID).Updates(fields)
		hit = res.RowsAffected > 0
		return res.Error
	})
	return hit, err
}

// UpdateTerminalRunIf 只有当记录当前处于 fromStates 之一时才更新，返回是否生效。
// 终态不会被反向覆盖：服务端据此保证「一次运行只会有一个终态」。
func (s *Store) UpdateTerminalRunIf(ctx context.Context, runID string, fromStates []string, fields map[string]any) (bool, error) {
	if len(fields) == 0 || len(fromStates) == 0 {
		return false, nil
	}
	fields["updated_at"] = time.Now()
	var hit bool
	err := s.WithTx(ctx, func(tx *gorm.DB) error {
		res := tx.Model(&model.TerminalRun{}).
			Where("run_id = ? AND state IN ?", runID, fromStates).Updates(fields)
		hit = res.RowsAffected > 0
		return res.Error
	})
	return hit, err
}

// GetTerminalRun 按运行 ID 查询；不存在时返回 (nil, nil)。
func (s *Store) GetTerminalRun(ctx context.Context, runID string) (*model.TerminalRun, error) {
	var r model.TerminalRun
	err := s.db.WithContext(ctx).Where("run_id = ?", runID).First(&r).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return &r, nil
}

// ListTerminalRuns 按 ID 倒序返回运行记录（游标翻页）。
func (s *Store) ListTerminalRuns(ctx context.Context, f RunFilter) ([]model.TerminalRun, error) {
	limit := f.Limit
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	q := s.db.WithContext(ctx).Order(clause.OrderByColumn{Column: clause.Column{Name: "id"}, Desc: true}).Limit(limit)
	if f.Target != "" {
		q = q.Where("target = ?", f.Target)
	}
	if f.ThreadKey != "" {
		q = q.Where("thread_key = ?", f.ThreadKey)
	}
	if f.CredentialID != 0 {
		q = q.Where("credential_id = ?", f.CredentialID)
	}
	if f.CredentialName != "" {
		q = q.Where("credential_name = ?", f.CredentialName)
	}
	if f.Thread != "" {
		q = q.Where("thread = ?", f.Thread)
	}
	if len(f.States) > 0 {
		q = q.Where("state IN ?", f.States)
	}
	if f.BeforeID > 0 {
		q = q.Where("id < ?", f.BeforeID)
	}
	var out []model.TerminalRun
	if err := q.Find(&out).Error; err != nil {
		return nil, err
	}
	return out, nil
}

// ThreadFilter 是线程汇总的过滤条件。
type ThreadFilter struct {
	Target       string
	CredentialID uint
	Limit        int
}

// ThreadStat 是一个线程（线程键）的汇总：运行总数、进行中与待批准的数量，以及首末两条运行的主键。
type ThreadStat struct {
	ThreadKey string `json:"thread_key"`
	Total     int64  `json:"total"`
	Active    int64  `json:"active"`
	Awaiting  int64  `json:"awaiting"`
	FirstID   uint   `json:"first_id"`
	LastID    uint   `json:"last_id"`
}

// ListTerminalThreads 按「最近一次运行」倒序返回线程汇总。只聚合元数据，不读取预览等大字段。
func (s *Store) ListTerminalThreads(ctx context.Context, f ThreadFilter) ([]ThreadStat, error) {
	limit := f.Limit
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	q := s.db.WithContext(ctx).Model(&model.TerminalRun{}).
		Select("thread_key, COUNT(*) AS total, "+
			"SUM(CASE WHEN state IN (?, ?) THEN 1 ELSE 0 END) AS active, "+
			"SUM(CASE WHEN state = ? THEN 1 ELSE 0 END) AS awaiting, "+
			"MIN(id) AS first_id, MAX(id) AS last_id",
			model.RunAwaitingApproval, model.RunRunning, model.RunAwaitingApproval).
		Group("thread_key").
		Order("last_id DESC").
		Limit(limit)
	if f.Target != "" {
		q = q.Where("target = ?", f.Target)
	}
	if f.CredentialID != 0 {
		q = q.Where("credential_id = ?", f.CredentialID)
	}
	var out []ThreadStat
	if err := q.Scan(&out).Error; err != nil {
		return nil, err
	}
	return out, nil
}

// ListTerminalRunsByIDs 按主键批量读取运行记录（线程汇总取首末两条用）。
func (s *Store) ListTerminalRunsByIDs(ctx context.Context, ids []uint) ([]model.TerminalRun, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	var out []model.TerminalRun
	if err := s.db.WithContext(ctx).Where("id IN ?", ids).Find(&out).Error; err != nil {
		return nil, err
	}
	return out, nil
}

// FindTerminalRunByIdempotency 按 (凭据, 幂等键) 查找已有运行；用于 terminal_exec 的重试去重。
func (s *Store) FindTerminalRunByIdempotency(ctx context.Context, credentialID uint, key string) (*model.TerminalRun, error) {
	if strings.TrimSpace(key) == "" {
		return nil, nil
	}
	var r model.TerminalRun
	err := s.db.WithContext(ctx).
		Where("credential_id = ? AND idempotency_key = ?", credentialID, key).
		Order("id DESC").First(&r).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return &r, nil
}

// ListActiveTerminalRuns 返回所有尚未终结的运行（待批准或运行中），Hub 启动时据此恢复内存索引。
func (s *Store) ListActiveTerminalRuns(ctx context.Context) ([]model.TerminalRun, error) {
	var out []model.TerminalRun
	err := s.db.WithContext(ctx).
		Where("state IN ?", []string{model.RunAwaitingApproval, model.RunRunning}).
		Order("id ASC").Find(&out).Error
	return out, err
}

// PruneTerminalRuns 删除结束时间早于 cutoff 的运行元数据，返回删除条数。
func (s *Store) PruneTerminalRuns(ctx context.Context, cutoff time.Time) (int64, error) {
	var n int64
	err := s.WithTx(ctx, func(tx *gorm.DB) error {
		res := tx.Where("ended_at IS NOT NULL AND ended_at < ?", cutoff).Delete(&model.TerminalRun{})
		n = res.RowsAffected
		return res.Error
	})
	return n, err
}

// DeleteTerminalRunsOfHost 删除一台主机的全部运行元数据（删除主机时调用）。
func (s *Store) DeleteTerminalRunsOfHost(ctx context.Context, machineCode string) error {
	return s.WithTx(ctx, func(tx *gorm.DB) error {
		return tx.Where("target = ?", machineCode).Delete(&model.TerminalRun{}).Error
	})
}

// HostModelUpdate 描述对主机「模型访问」设置的一次修改；nil 字段表示不修改。
type HostModelUpdate struct {
	AllowModel  *bool
	ExecMode    *string
	ModelPaused *bool
	// DefaultForNewModels 是主机对新建模型授权的默认态（allow / deny）；归一后比较，deny 与空串视为同一状态。
	DefaultForNewModels *string
}

// ValidateHostExecMode 校验主机的执行模式（空串等价于 auto）。
func ValidateHostExecMode(v string) error {
	switch v {
	case "", model.TerminalModeAuto, model.TerminalModeApprove, model.TerminalModeReadonly:
		return nil
	}
	return errors.New("执行模式只能是 auto / approve / readonly")
}

// SetTerminalHostModel 修改主机的模型访问设置，返回修改后的记录与是否有变化。
func (s *Store) SetTerminalHostModel(ctx context.Context, machineCode string, u HostModelUpdate) (host *model.TerminalHost, changed bool, err error) {
	if u.ExecMode != nil {
		if err := ValidateHostExecMode(*u.ExecMode); err != nil {
			return nil, false, err
		}
	}
	if u.DefaultForNewModels != nil {
		if err := ValidateHostDefaultForNewModels(*u.DefaultForNewModels); err != nil {
			return nil, false, err
		}
	}
	err = s.WithTx(ctx, func(tx *gorm.DB) error {
		var h model.TerminalHost
		if err := tx.Where("machine_code = ?", machineCode).First(&h).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrTerminalHostNotFound
			}
			return err
		}
		updates := map[string]any{}
		if u.AllowModel != nil && h.AllowModel != *u.AllowModel {
			updates["allow_model"] = *u.AllowModel
			h.AllowModel = *u.AllowModel
		}
		if u.ExecMode != nil && h.ExecMode != *u.ExecMode {
			updates["exec_mode"] = *u.ExecMode
			h.ExecMode = *u.ExecMode
		}
		if u.ModelPaused != nil && h.ModelPaused != *u.ModelPaused {
			updates["model_paused"] = *u.ModelPaused
			h.ModelPaused = *u.ModelPaused
		}
		if u.DefaultForNewModels != nil {
			if want := HostDefaultState(*u.DefaultForNewModels); HostDefaultState(h.DefaultForNewModels) != want {
				updates["default_for_new_models"] = want
				h.DefaultForNewModels = want
			}
		}
		host = &h
		if len(updates) == 0 {
			return nil
		}
		changed = true
		return tx.Model(&model.TerminalHost{}).Where("machine_code = ?", machineCode).Updates(updates).Error
	})
	return host, changed, err
}

// GetSystemFlag 读取一个布尔系统开关（值 "1" 为真）；不存在视为假。
func (s *Store) GetSystemFlag(ctx context.Context, key string) (bool, error) {
	var rec model.SystemSetting
	err := s.db.WithContext(ctx).Where("key = ?", key).First(&rec).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return false, nil
		}
		return false, err
	}
	return rec.Value == "1", nil
}

// SetSystemFlag 写入一个布尔系统开关。
func (s *Store) SetSystemFlag(ctx context.Context, key string, on bool) error {
	val := "0"
	if on {
		val = "1"
	}
	return s.WithTx(ctx, func(tx *gorm.DB) error {
		return tx.Save(&model.SystemSetting{Key: key, Value: val, UpdatedAt: time.Now()}).Error
	})
}
