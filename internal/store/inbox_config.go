package store

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"gorm.io/gorm"

	"github.com/teacat99/RelayMesh/v2/internal/model"
)

// InboxConfigKey 是收件箱开关在 system_settings 表里的键，值是 JSON（见 InboxConfig）。
const InboxConfigKey = "inbox_config"

// inboxConfigTTL：开关被每一次工具调用读取，缓存几秒就够了；改开关时会立刻失效。
const inboxConfigTTL = 3 * time.Second

// InboxConfig 是追加指令收件箱的手动开关：一个总开关，外加三层发现机制各自的开关。
// 总开关关闭等于回到「被动模式」：不再追加回执提示行、不再让 hook 提醒、不再在指引里提收件箱；
// 已经排队的追加一条都不会丢，智能体下次发起交互时仍然会被秒回带走。
type InboxConfig struct {
	Enabled         bool `json:"enabled"`           // 总开关
	ReceiptHint     bool `json:"receipt_hint"`      // 第二层：工具回执里追加 inbox: 提示
	Hooks           bool `json:"hooks"`             // 第一层：Cursor hook 的提醒 / 拦截（hook 查询时由服务端判断）
	PromptClause    bool `json:"prompt_clause"`     // 第三层：MCP 指引里的收件箱条款
	HookInstallHint bool `json:"hook_install_hint"` // 未装 hook 时在回执末尾提示安装（可关）
}

// DefaultInboxConfig 默认全部打开。
func DefaultInboxConfig() InboxConfig {
	return InboxConfig{Enabled: true, ReceiptHint: true, Hooks: true, PromptClause: true, HookInstallHint: true}
}

// Effective 叠加环境变量的总闸（RELAYMESH_INBOX=off）：它只能关，不能开。
func (c InboxConfig) Effective(envOff bool) InboxConfig {
	if envOff {
		c.Enabled = false
	}
	return c
}

// HintOn / HooksOn / ClauseOn 是「总开关 ∧ 分层开关」。
func (c InboxConfig) HintOn() bool   { return c.Enabled && c.ReceiptHint }
func (c InboxConfig) HooksOn() bool  { return c.Enabled && c.Hooks }
func (c InboxConfig) ClauseOn() bool { return c.Enabled && c.PromptClause }

// HookInstallHintOn 是否在回执里提示安装 hook。
func (c InboxConfig) HookInstallHintOn() bool { return c.Enabled && c.HookInstallHint }

type inboxConfigCache struct {
	mu      sync.Mutex
	cfg     InboxConfig
	expires time.Time
}

func (c *inboxConfigCache) get(now time.Time) (InboxConfig, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if now.Before(c.expires) {
		return c.cfg, true
	}
	return InboxConfig{}, false
}

func (c *inboxConfigCache) put(cfg InboxConfig, now time.Time) {
	c.mu.Lock()
	c.cfg, c.expires = cfg, now.Add(inboxConfigTTL)
	c.mu.Unlock()
}

func (c *inboxConfigCache) drop() {
	c.mu.Lock()
	c.expires = time.Time{}
	c.mu.Unlock()
}

// GetInboxConfig 读取收件箱开关；从未设置过时是全部打开。读不到库时按默认处理（宁可多提醒，不要把功能悄悄关掉）。
func (s *Store) GetInboxConfig(ctx context.Context) InboxConfig {
	now := time.Now()
	if cfg, ok := s.inboxCfg.get(now); ok {
		return cfg
	}
	cfg := DefaultInboxConfig()
	var rec model.SystemSetting
	err := s.db.WithContext(ctx).Where("key = ?", InboxConfigKey).First(&rec).Error
	switch {
	case err == nil:
		var stored InboxConfig
		if json.Unmarshal([]byte(rec.Value), &stored) == nil {
			cfg = stored
		}
	case errors.Is(err, gorm.ErrRecordNotFound):
	default:
		return cfg // 读库失败：不缓存，下次再试
	}
	s.inboxCfg.put(cfg, now)
	return cfg
}

// UpdateInboxConfig 在事务里读—改—写收件箱开关。
func (s *Store) UpdateInboxConfig(ctx context.Context, mutate func(cur InboxConfig) InboxConfig) (InboxConfig, error) {
	var out InboxConfig
	err := s.WithTx(ctx, func(tx *gorm.DB) error {
		cur := DefaultInboxConfig()
		var rec model.SystemSetting
		if err := tx.Where("key = ?", InboxConfigKey).First(&rec).Error; err == nil {
			var stored InboxConfig
			if json.Unmarshal([]byte(rec.Value), &stored) == nil {
				cur = stored
			}
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		next := mutate(cur)
		raw, err := json.Marshal(next)
		if err != nil {
			return err
		}
		if err := tx.Save(&model.SystemSetting{Key: InboxConfigKey, Value: string(raw), UpdatedAt: time.Now()}).Error; err != nil {
			return err
		}
		out = next
		return nil
	})
	s.inboxCfg.drop()
	return out, err
}
