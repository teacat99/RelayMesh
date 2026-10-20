package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"

	"github.com/teacat99/RelayMesh/v2/internal/model"
)

// TerminalPrefsKey 是终端偏好在 system_settings 表里的键，值是 JSON（见 TerminalPrefs）。
const TerminalPrefsKey = "terminal_prefs"

// 终端偏好的取值范围；0 一律表示「关闭 / 不限制」。
const (
	// 固定终端列数（宽度保持）：小于 40 列几乎没法用，大于 400 列即使缩到最小字号也放不进手机屏幕。
	MinFixedCols = 40
	MaxFixedCols = 400
	// 单会话记录保留的输出行数上限：小于 5000 行连一屏回看都不够；上限 1000 万行只是防止填错数字，实际受字节配额约束。
	MinRecordMaxLines = 5000
	MaxRecordMaxLines = 10_000_000
)

// ErrInvalidTerminalPrefs 表示偏好的取值超出范围；错误信息可以直接展示给用户。
var ErrInvalidTerminalPrefs = errors.New("终端偏好取值不合法")

// TerminalPrefs 是终端的服务端偏好：保存在 Hub 里，所有浏览器共享，换设备查看时保持一致。
type TerminalPrefs struct {
	// FixedCols 大于 0 时开启「宽度保持」：每个会话的终端都固定为这个列数，浏览器窗口再窄也不改变，
	// 窗口放不下就缩小字号去适配；0 表示关闭，终端宽度跟随浏览器窗口（默认）。
	FixedCols int `json:"fixed_cols"`
	// RecordMaxLines 大于 0 时开启「终端限高」：每个会话在主机上的记录至少保留最近这么多行输出，
	// 更早的内容按分段滚动丢弃；0 表示不按行数限制（默认，仍受字节配额约束）。
	RecordMaxLines int `json:"record_max_lines"`
	// AgentAutoUpgrade 是 Hub 的「Agent 自动升级」总闸（默认关）：关着时，Hub 对主机 Agent 的版本检查一律回答「不提供」，
	// 即使某台主机的所有者已经在本机开启了自动升级。打开它并不会让任何主机自动升级——每台主机仍需要它的所有者在本机同意。
	AgentAutoUpgrade bool `json:"agent_auto_upgrade"`
}

// Validate 检查取值范围；错误信息面向用户。
func (p TerminalPrefs) Validate() error {
	if p.FixedCols != 0 && (p.FixedCols < MinFixedCols || p.FixedCols > MaxFixedCols) {
		return fmt.Errorf("%w：固定列数应为 0（关闭）或 %d~%d", ErrInvalidTerminalPrefs, MinFixedCols, MaxFixedCols)
	}
	if p.RecordMaxLines != 0 && (p.RecordMaxLines < MinRecordMaxLines || p.RecordMaxLines > MaxRecordMaxLines) {
		return fmt.Errorf("%w：保留行数应为 0（不限制）或 %d~%d", ErrInvalidTerminalPrefs, MinRecordMaxLines, MaxRecordMaxLines)
	}
	return nil
}

// GetTerminalPrefs 读取终端偏好；从未设置过时返回全部关闭的默认值。
func (s *Store) GetTerminalPrefs(ctx context.Context) (TerminalPrefs, error) {
	var rec model.SystemSetting
	err := s.db.WithContext(ctx).Where("key = ?", TerminalPrefsKey).First(&rec).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return TerminalPrefs{}, nil
		}
		return TerminalPrefs{}, err
	}
	return decodeTerminalPrefs(rec.Value), nil
}

// UpdateTerminalPrefs 在一个事务里「读—改—写」终端偏好，避免两个设置项同时保存时互相覆盖。
// mutate 只改自己关心的字段；改完的结果必须通过 Validate，否则不写入并返回 ErrInvalidTerminalPrefs。
func (s *Store) UpdateTerminalPrefs(ctx context.Context, mutate func(*TerminalPrefs)) (TerminalPrefs, error) {
	var out TerminalPrefs
	err := s.WithTx(ctx, func(tx *gorm.DB) error {
		var rec model.SystemSetting
		cur := TerminalPrefs{}
		if err := tx.Where("key = ?", TerminalPrefsKey).First(&rec).Error; err == nil {
			cur = decodeTerminalPrefs(rec.Value)
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		mutate(&cur)
		if err := cur.Validate(); err != nil {
			return err
		}
		raw, err := json.Marshal(cur)
		if err != nil {
			return err
		}
		if err := tx.Save(&model.SystemSetting{Key: TerminalPrefsKey, Value: string(raw), UpdatedAt: time.Now()}).Error; err != nil {
			return err
		}
		out = cur
		return nil
	})
	return out, err
}

// decodeTerminalPrefs 容忍损坏或越界的值：解析失败按默认处理，越界的字段单独归零（宁可关掉也不要用一个坏值）。
func decodeTerminalPrefs(raw string) TerminalPrefs {
	var p TerminalPrefs
	if json.Unmarshal([]byte(raw), &p) != nil {
		return TerminalPrefs{}
	}
	if p.FixedCols != 0 && (p.FixedCols < MinFixedCols || p.FixedCols > MaxFixedCols) {
		p.FixedCols = 0
	}
	if p.RecordMaxLines != 0 && (p.RecordMaxLines < MinRecordMaxLines || p.RecordMaxLines > MaxRecordMaxLines) {
		p.RecordMaxLines = 0
	}
	return p
}
